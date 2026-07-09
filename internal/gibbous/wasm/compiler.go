//go:build wangshu_p3

// Package wasm is the P3 gibbous tier:字节码 → Wasm 编译器 + wazero 执行环境
// (docs/design/p3-wasm-tier/)。
//
// 仅 wangshu_p3 build 编译——默认 / wangshu_profile build 下本包不进 import
// 图,主库不链接 wazero 运行期代码。
//
// PW 进度(02-translation §1.3 渐进白名单):
//   - PW1(本轮):包骨架 + Compiler 实现 bridge.P3Compiler;
//     SupportsAllOpcodes 永远 false(supported 全 false)→ 无 Proto 升层 →
//     与 P1-only byte-equal。arena 收养 wazero memory 经 memadapter 子包。
//   - PW2+:逐档扩 supported 白名单 + emit 翻译(见 02-translation §3)。
package wasm

import (
	"context"
	"fmt"
	"runtime/debug"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"

	"github.com/Liam0205/wangshu/internal/bridge"
	"github.com/Liam0205/wangshu/internal/bytecode"
)

// Compiler 实现 bridge.P3Compiler(02-translation §5)。
//
// 一个 Compiler 服务一个 State(持该 State 的 wazero Runtime,与 memadapter
// 的 MemoryHolder 同源)。多 State 各持独立 Compiler / Runtime / Memory
// (arena 单 State 私有,03 §1.5)。
type Compiler struct {
	ctx     context.Context
	runtime wazero.Runtime

	// host 是注入的执行期状态抽象(crescent.State 实现 HostState)——
	// helper callback 经它操作 CallInfo/值栈/upvalue,解 crescent⇄gibbous 环。
	host HostState

	// hostModInstantiated 标记 env host module(含 memory + helpers)是否已
	// 注册到 runtime。首次 Compile 时注册一次,后续 Proto 复用。
	hostModReady bool

	// supported[op] = 该 opcode 是否已实装 Wasm 翻译。
	// PW1:全 false(保守缺省,02-translation §1.3 + §5.2)。
	// PW2:直线 opcode(MOVE/LOADK/LOADBOOL/LOADNIL/GETUPVAL/SETUPVAL/JMP)。
	supported [numOpcodes]bool

	// slotOf:Proto → 共享 env.table 槽号(PW10 Arch-2)。每个升层 Proto 在
	// Compile 时分配一个单调递增 slot,其 module 经 element 段把 run 注册进该槽;
	// gibbous→gibbous CALL 据被调 Proto 的 slot 经 call_indirect 跨 module 直达
	// (R3 接线)。-1(不在表内)= 未升层/超容量 → 回退 h_call。
	slotOf   map[*bytecode.Proto]uint32
	nextSlot uint32
}

// maxTableSlots 是 Compiler 可分配的 slot 上限(= memadapter.TableSlots,env
// 共享表容量)。超出则该 Proto 不分配 slot(回退 h_call,正确性不破)。
// 硬编码避免 wasm 包(非测试代码)反向 import memadapter 形成 import 环倒置;
// 两值须一致(TestPW10_SlotCapAligned 断言对齐)。
const maxTableSlots = 8192

// numOpcodes 是 P1 活跃 opcode 数(0..37)+ 预留区上界守卫。
// bytecode.OpCode 是 6-bit(0..63);supported 数组按 64 长度建,
// 越界 opcode(38..63 预留)天然落 false。
const numOpcodes = 64

// NewCompiler 构造一个 Compiler(PW1:supported 全 false)。
//
// runtime 由门面层(crescent 在 wangshu_p3 build 下)创建并注入,与
// memadapter.MemoryHolder 共用同一 Runtime——确保 gibbous module 经 import
// memory 能共享 arena 收养的那块 linear memory。host 是执行期状态抽象
// (crescent.State 实现 HostState),helper callback 经它操作执行期状态。
//
// PW2 supported 白名单:直线 opcode(02-translation §1.3 PW2 档)。
func NewCompiler(ctx context.Context, runtime wazero.Runtime, host HostState) *Compiler {
	c := &Compiler{
		ctx:     ctx,
		runtime: runtime,
		host:    host,
		slotOf:  make(map[*bytecode.Proto]uint32),
	}
	c.supported[bytecode.MOVE] = true
	c.supported[bytecode.LOADK] = true
	c.supported[bytecode.LOADBOOL] = true
	c.supported[bytecode.LOADNIL] = true
	c.supported[bytecode.GETUPVAL] = true
	c.supported[bytecode.SETUPVAL] = true
	c.supported[bytecode.JMP] = true
	c.supported[bytecode.RETURN] = true // 单 BB Proto 出口必需
	// PW3:直线算术(不切 BB)。比较 EQ/LT/LE/TEST/TESTSET 切 BB,留 PW4。
	c.supported[bytecode.ADD] = true
	c.supported[bytecode.SUB] = true
	c.supported[bytecode.MUL] = true
	c.supported[bytecode.DIV] = true
	c.supported[bytecode.MOD] = true
	c.supported[bytecode.POW] = true
	c.supported[bytecode.UNM] = true
	c.supported[bytecode.NOT] = true
	c.supported[bytecode.LEN] = true
	c.supported[bytecode.CONCAT] = true
	// PW4:控制流 + 比较(relooper 解锁多 BB)。
	c.supported[bytecode.EQ] = true
	c.supported[bytecode.LT] = true
	c.supported[bytecode.LE] = true
	c.supported[bytecode.TEST] = true
	c.supported[bytecode.TESTSET] = true
	c.supported[bytecode.FORPREP] = true
	c.supported[bytecode.FORLOOP] = true
	// PW5:表 IC opcode(inline 快照固化 + 失效降级助手)。
	c.supported[bytecode.GETGLOBAL] = true
	c.supported[bytecode.SETGLOBAL] = true
	c.supported[bytecode.GETTABLE] = true
	c.supported[bytecode.SETTABLE] = true
	c.supported[bytecode.SELF] = true
	c.supported[bytecode.NEWTABLE] = true
	c.supported[bytecode.SETLIST] = true
	// PW6:CALL 三向分派 + base 刷新(跨层互调)。
	c.supported[bytecode.CALL] = true
	c.supported[bytecode.TAILCALL] = true
	// PW7:闭包构造 + 作用域 upvalue 关闭(全经助手)。
	c.supported[bytecode.CLOSURE] = true
	c.supported[bytecode.CLOSE] = true
	// PW4b:TFORLOOP 泛型 for(经 h_tforloop 调迭代器 + base 刷新)。
	c.supported[bytecode.TFORLOOP] = true
	// PW5+ 逐档解锁(02-translation §1.3)。VARARG 永不加入。
	return c
}

// SlotOf 返回 Proto 在共享 env.table 的槽号 + 是否已登记(PW10 Arch-2)。
//
// R3 CALL 翻译用:被调 Proto 已升 gibbous 且有有效 slot(< maxTableSlots)⟹
// 经 call_indirect <slot> 跨 module 直达;否则(未登记 / 表满哨兵)回退 h_call。
// ok=false 表示该 Proto 尚未编译过(无 slot);返回 maxTableSlots 表示表满未入表。
func (c *Compiler) SlotOf(proto *bytecode.Proto) (uint32, bool) {
	s, ok := c.slotOf[proto]
	return s, ok
}

// WorthPromoting implements bridge.PromotionGater (issue #39):
// profitability judgment, consulted in auto mode after capability
// (SupportsAllOpcodes) passes. Returns false for protos whose op mix
// is dominated by helper-round-trip ops — every GETTABLE / SETTABLE /
// SELF miss, every CALL, and every LEN / CONCAT / NEWTABLE / SETLIST
// crosses the wasm→Go host boundary (~tens of ns each, plus the IC
// inline fast path only covers warmed mono sites). On helper-dense
// float kernels (nbody's advance / energy: ~1 table op per 3 opcodes)
// the promoted code runs ~2x SLOWER than the interpreter — measured
// 43.5ms → 89.7ms after 45b8b53 unblocked their promotion (issue #39).
//
// Density judgment mirrors P4's CALL density gate (AnalyzeNative,
// bebbd44) but over the wasm helper set: require enough total ops per
// helper-bound op to amortize the boundary crossings. Pure-arithmetic
// loop kernels (heavy_arith / heavy_floatloop: zero helper-bound ops)
// promote and keep their measured P3 wins; helper-dense kernels
// (nbody advance ~1/3, fannkuch shuffles ~1/3, fib call bodies 1/6,
// spectral-norm accessor loops ~1/7) stay on the interpreter.
func (c *Compiler) WorthPromoting(proto *bytecode.Proto) bool {
	if len(proto.Code) == 0 {
		return true
	}
	// Back-edge dimension (issue #92): P3's win comes from dispatch saved
	// INSIDE loops; the price is a per-call wasm boundary round trip
	// (~130ns/call vs ~73ns/call interpreted, measured on the Arith
	// kernel). A straight-line body (no back edge) has nothing to
	// amortize that tax — a small one loses on every call (Arith kernel:
	// promoted 6450ns vs interpreted 3666ns, 1.76x SLOWER). Reject small
	// straight-line bodies; large ones (>= straightLineMinCodeLen) carry
	// enough per-call dispatch savings to cover the boundary.
	hasBackEdge := false
	total := 0
	helperBound := 0
	for _, ins := range proto.Code {
		total++
		switch op := bytecode.Op(ins); op {
		case bytecode.GETTABLE, bytecode.SETTABLE, bytecode.SELF,
			bytecode.GETGLOBAL, bytecode.SETGLOBAL,
			bytecode.CALL, bytecode.TAILCALL,
			bytecode.LEN, bytecode.CONCAT,
			bytecode.NEWTABLE, bytecode.SETLIST,
			bytecode.CLOSURE, bytecode.CLOSE:
			helperBound++
		case bytecode.FORLOOP, bytecode.TFORLOOP:
			hasBackEdge = true
		case bytecode.JMP:
			if bytecode.SBx(ins) < 0 {
				hasBackEdge = true
			}
		}
	}
	if !hasBackEdge && len(proto.Code) < straightLineMinCodeLen {
		return false
	}
	if helperBound == 0 {
		return true
	}
	// Floor 7, measured on the realworld + heavy suites (Xeon
	// Platinum, benchtime=2s count=3, 2026-07-03): every kernel that
	// measured promoted-slower-than-interpreter falls under it (nbody
	// advance 74/26 ≈ 2.8, fib 12/2 = 6, spectral-norm accessors
	// 20/3 ≈ 6.7 — at floor 5 fib still promoted and lost ~2.3x), and
	// every kernel that wins on P3 has no helper-bound ops at all
	// (heavy_arith / heavy_floatloop return early above). Auto-mode
	// results with this floor: nbody 89.7→43.2ms, fib 24.9→10.9ms,
	// binary-trees 104→38.4ms, spectral-norm 40→20.7ms; heavy three
	// unchanged (86.3 / 5.68 / 50.9ms).
	return total/helperBound >= wasmHelperDensityFloor
}

// wasmHelperDensityFloor is the minimum total-ops-per-helper-bound-op
// ratio for promotion to be predicted profitable (see WorthPromoting).
const wasmHelperDensityFloor = 7

// straightLineMinCodeLen is the minimum Code length for a proto with NO
// back edge to be predicted profitable (issue #92). Balance point: the
// per-call boundary tax (call_indirect entry/exit, ~57ns measured on
// the Arith kernel: 130ns promoted vs 73ns interpreted per call) over
// the per-instruction dispatch saving (~2-4ns). At 32+ straight-line
// instructions the saved dispatch covers the boundary; below it the
// call is a net loss on every entry. Calibrated on darwin/arm64 (issue
// #92 PoC) and re-checked on amd64 Xeon: Arith kernel (10 insns)
// 6450 -> 3551 ns/op back at interpreter level, Loop kernel (back edge)
// keeps promoting, heavy suite unchanged (all have back edges).
const straightLineMinCodeLen = 32

// SupportsAllOpcodes 实现 F7 后端能力查询(03 §3.7 + 02 §5.2)。
//
// 纯只读、不修改 Proto、不 panic(越界 opcode 编号天然落 false)。
//
// 限制:
//   - 所有可达 BB 的 opcode 都在 supported 白名单;
//   - 多 BB 时 CFG 必须可约简(relooper 只处理 reducible CFG,PW4);
//   - 含字符串常量的 LOADK:Consts 是 State 私有惰性 intern(编译期 Nil
//     占位),烧不出真 GCRef → 拒(留 PW5 经助手取)。
//
// 注意本方法只回答「能不能编」;「编了赚不赚」是 WorthPromoting 的职责。
func (c *Compiler) SupportsAllOpcodes(proto *bytecode.Proto) bool {
	if len(proto.Code) == 0 {
		return true // 空 Proto vacuously supported(实际不会被 P2 判热点)
	}
	cfg := buildCFG(proto)
	reach := cfg.reachableBlocks()
	// 多 BB:必须可约简(relooper 只处理 reducible CFG)。
	if len(reach) > 1 {
		if !analyzeRelooper(cfg).isReducible() {
			return false
		}
	}
	// 扫所有**可达** BB 的 opcode(死代码块永不执行,不影响支持判定)。
	for _, blk := range cfg.blocks {
		if !reach[blk.id] {
			continue
		}
		for pc := blk.startPC; pc < blk.endPC; pc++ {
			ins := proto.Code[pc]
			op := bytecode.Op(ins)
			if int(op) >= numOpcodes || !c.supported[op] {
				return false
			}
			// LOADK 字符串常量:编译期烧不出真值
			if op == bytecode.LOADK {
				bx := bytecode.Bx(ins)
				if proto.IsStringConst(bx) {
					return false
				}
			}
			// SETLIST C=0:下一指令字是大批次号(数据,非 opcode)——线性发射器
			// 会误当 opcode 翻译 → 拒。B=0(填到 top)依赖 gibbous 帧 top 维护
			// (PW7 前未接)→ 拒。常见 {1,2,3}(B≥1,C≥1)放行。
			if op == bytecode.SETLIST {
				if bytecode.C(ins) == 0 || bytecode.B(ins) == 0 {
					return false
				}
			}
			// CALL B=0(参数到 top)/ C=0(返回到 top)是多值窗口,依赖 th.top
			// 跨 opcode 维护——gibbous 直线代码不维护 top → 拒。定参定返(B≥1,C≥1)
			// 放行(常见 local x = f(a,b) 形态)。多值传播留后续。
			if op == bytecode.CALL {
				if bytecode.C(ins) == 0 || bytecode.B(ins) == 0 {
					return false
				}
			}
			// TAILCALL B=0(参数到 top)同 CALL 依赖 th.top → 拒(定参 B≥1 放行)。
			if op == bytecode.TAILCALL {
				if bytecode.B(ins) == 0 {
					return false
				}
			}
		}
	}
	return true
}

// Compile 把 Proto 编译成 GibbousCode(02 §5.1 + §5.5 panic 兜底)。
//
// 流程:① 确保 host module(helpers)已注册 ② translate 翻译产函数体
// ③ buildGibbousModuleBinary 组完整 module ④ wazero CompileModule +
// InstantiateModule(import env.memory + host.h_*)⑤ 包装 p3Code。
//
// defer recover 兜底:翻译 / wazero 调用的任何 panic 转
// *CompileError(Kind=BackendPanic),不穿越本接口(02 §1.4)。
func (c *Compiler) Compile(proto *bytecode.Proto, fb *bridge.TypeFeedback) (gc bridge.GibbousCode, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = &bridge.CompileError{
				Kind:   bridge.CompileErrBackendPanic,
				Proto:  proto,
				Reason: fmt.Sprintf("p3 backend panic: %v\nstack: %s", r, debug.Stack()),
			}
			gc = nil
		}
	}()

	// ① host module(helpers)注册一次
	if err := c.ensureHostModule(); err != nil {
		return nil, &bridge.CompileError{
			Kind: bridge.CompileErrOutOfResources, Proto: proto,
			Reason: "p3: register host module: " + err.Error(),
		}
	}

	// ② 翻译
	body, terr := c.translate(proto)
	if terr != nil {
		return nil, &bridge.CompileError{
			Kind: bridge.CompileErrUnsupportedOpcodeShape, Proto: proto,
			Reason: terr.Error(),
		}
	}

	// 分配共享 env.table 槽(PW10 Arch-2):本 module 的 run 经 element 段注册进
	// table[slot],gibbous→gibbous 经 call_indirect 此槽跨 module 直达(R3)。
	// 幂等:同 Proto 复编(多 State 共享同 Proto,理论上各 Compiler 独立)复用既有 slot。
	slot, ok := c.slotOf[proto]
	if !ok {
		if c.nextSlot >= maxTableSlots {
			// 表满:不分配 slot,该 Proto 的 run 不进表(R3 据「无 slot」回退 h_call)。
			// 仍可编译执行,只是 gibbous→它 走慢路径。用哨兵 maxTableSlots 标记。
			slot = maxTableSlots
		} else {
			slot = c.nextSlot
			c.nextSlot++
		}
		c.slotOf[proto] = slot
	}

	// ③ 组 module 二进制(slot 注册进 element 段;表满哨兵时仍发 element 写
	// table[maxTableSlots] 会越界——故表满时不发 element,见 buildGibbousModuleBinary)。
	bin := buildGibbousModuleBinary(body, slot)

	// ④ wazero 编译 + 实例化
	compiled, cerr := c.runtime.CompileModule(c.ctx, bin)
	if cerr != nil {
		return nil, &bridge.CompileError{
			Kind: bridge.CompileErrOutOfResources, Proto: proto,
			Reason: "p3: wazero compile: " + cerr.Error(),
		}
	}
	mod, ierr := c.runtime.InstantiateModule(c.ctx, compiled,
		wazero.NewModuleConfig().WithName(gibbousModuleName(proto)))
	if ierr != nil {
		_ = compiled.Close(c.ctx)
		return nil, &bridge.CompileError{
			Kind: bridge.CompileErrOutOfResources, Proto: proto,
			Reason: "p3: wazero instantiate: " + ierr.Error(),
		}
	}
	fn := mod.ExportedFunction("run")
	if fn == nil {
		_ = mod.Close(c.ctx)
		return nil, &bridge.CompileError{
			Kind: bridge.CompileErrBackendPanic, Proto: proto,
			Reason: "p3: gibbous module exports no 'run'",
		}
	}

	return &p3Code{
		compiled: compiled,
		module:   mod,
		fn:       fn,
		proto:    proto,
		ctx:      c.ctx,
		slot:     slot,
		hasSlot:  slot < maxTableSlots,
	}, nil
}

// ensureHostModule 注册 host module(helper Go 函数)到 runtime,一次性。
//
// helper callback 经 c.host(HostState)转发到执行期状态(crescent.State)。
// module name "host",与 gibbous module 的 `import "host" "h_*"` 对应。
//
// **零分配注册(PW10 R3.5)**:用 WithGoFunction(api.GoFunc,stack-based)而非
// WithFunc(反射)——后者每次跨层 callGoFunc 都 reflect.New 逐参装箱(call 核
// ~14 allocs/调,支配退化)。stack-based 从 []uint64 直接解参/回写,零反射零分配。
// params/results ValueType 必须与 module.go 的 type 声明逐位对齐。
func (c *Compiler) ensureHostModule() error {
	if c.hostModReady {
		return nil
	}
	hs := &helperSet{host: c.host}
	// 复用的 wasm 值类型签名(对齐 module.go type 声明)。
	i32 := api.ValueTypeI32
	i64 := api.ValueTypeI64
	var (
		p2     = []api.ValueType{i32, i32}
		p3     = []api.ValueType{i32, i32, i32}
		p4     = []api.ValueType{i32, i32, i32, i32}
		p5     = []api.ValueType{i32, i32, i32, i32, i32}
		p6     = []api.ValueType{i32, i32, i32, i32, i32, i32}
		retI32 = []api.ValueType{i32}
		retI64 = []api.ValueType{i64}
		none   = []api.ValueType(nil)
	)
	b := c.runtime.NewHostModuleBuilder("host")
	add := func(name string, fn api.GoFunc, params, results []api.ValueType) {
		b.NewFunctionBuilder().WithGoFunction(fn, params, results).Export(name)
	}
	add("h_getupval", hs.goGetUpval, p2, retI64)
	add("h_setupval", hs.goSetUpval, []api.ValueType{i32, i32, i64}, none)
	add("h_return", hs.goReturn, p4, retI32)
	add("h_safepoint", hs.goSafepoint, p2, none)
	add("h_arith", hs.goArith, p6, retI32)
	add("h_unm", hs.goUnm, p4, retI32)
	add("h_len", hs.goLen, p4, retI32)
	add("h_concat", hs.goConcat, p5, retI32)
	add("h_compare", hs.goCompare, p5, retI32)
	add("h_eq", hs.goEq, p4, retI32)
	add("h_forprep", hs.goForPrep, p3, retI32)
	add("h_gettable", hs.goGetTable, p5, retI32)
	add("h_settable", hs.goSetTable, p5, retI32)
	add("h_getglobal", hs.goGetGlobal, p4, retI32)
	add("h_setglobal", hs.goSetGlobal, p4, retI32)
	add("h_self", hs.goSelf, p5, retI32)
	add("h_newtable", hs.goNewTable, p5, retI32)
	add("h_setlist", hs.goSetList, p5, retI32)
	add("h_call", hs.goCall, p5, retI64)
	add("h_tailcall", hs.goTailCall, p5, retI32)
	add("h_closure", hs.goClosure, p4, retI32)
	add("h_close", hs.goClose, p3, retI32)
	add("h_tforloop", hs.goTForLoop, p4, retI64)
	add("h_callerr", hs.goCallErr, none, none)
	if _, err := b.Instantiate(c.ctx); err != nil {
		return err
	}
	c.hostModReady = true
	return nil
}

// gibbousModuleName 给每个 Proto 的 gibbous module 一个唯一名(wazero 要求
// 已命名 module 名唯一)。用 Proto 指针地址 —— 单 State 内唯一且稳定。
func gibbousModuleName(proto *bytecode.Proto) string {
	return fmt.Sprintf("gib_%p", proto)
}
