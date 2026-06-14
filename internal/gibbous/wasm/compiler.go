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
}

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
	// PW5+ 逐档解锁(02-translation §1.3)。VARARG 永不加入。
	return c
}

// SupportsAllOpcodes 实现 F7 后端能力查询(03 §3.7 + 02 §5.2)。
//
// 纯只读、不修改 Proto、不 panic(越界 opcode 编号天然落 false)。
//
// 限制:
//   - 所有可达 BB 的 opcode 都在 supported 白名单;
//   - 多 BB 时 CFG 必须可约简(relooper 只处理 reducible CFG,PW4);
//   - 含字符串常量的 LOADK:Consts 是 State 私有惰性 intern(编译期 Nil
//     占位),烧不出真 GCRef → 拒(留 PW5 经助手取)。
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

	// ③ 组 module 二进制
	bin := buildGibbousModuleBinary(body)

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
	}, nil
}

// ensureHostModule 注册 host module(helper Go 函数)到 runtime,一次性。
//
// helper callback 经 c.host(HostState)转发到执行期状态(crescent.State)。
// module name "host",与 gibbous module 的 `import "host" "h_*"` 对应。
func (c *Compiler) ensureHostModule() error {
	if c.hostModReady {
		return nil
	}
	hs := &helperSet{host: c.host}
	_, err := c.runtime.NewHostModuleBuilder("host").
		NewFunctionBuilder().WithFunc(hs.goGetUpval).Export("h_getupval").
		NewFunctionBuilder().WithFunc(hs.goSetUpval).Export("h_setupval").
		NewFunctionBuilder().WithFunc(hs.goReturn).Export("h_return").
		NewFunctionBuilder().WithFunc(hs.goSafepoint).Export("h_safepoint").
		NewFunctionBuilder().WithFunc(hs.goArith).Export("h_arith").
		NewFunctionBuilder().WithFunc(hs.goUnm).Export("h_unm").
		NewFunctionBuilder().WithFunc(hs.goLen).Export("h_len").
		NewFunctionBuilder().WithFunc(hs.goConcat).Export("h_concat").
		NewFunctionBuilder().WithFunc(hs.goCompare).Export("h_compare").
		NewFunctionBuilder().WithFunc(hs.goEq).Export("h_eq").
		NewFunctionBuilder().WithFunc(hs.goForPrep).Export("h_forprep").
		NewFunctionBuilder().WithFunc(hs.goGetTable).Export("h_gettable").
		NewFunctionBuilder().WithFunc(hs.goSetTable).Export("h_settable").
		NewFunctionBuilder().WithFunc(hs.goGetGlobal).Export("h_getglobal").
		NewFunctionBuilder().WithFunc(hs.goSetGlobal).Export("h_setglobal").
		NewFunctionBuilder().WithFunc(hs.goSelf).Export("h_self").
		NewFunctionBuilder().WithFunc(hs.goNewTable).Export("h_newtable").
		NewFunctionBuilder().WithFunc(hs.goSetList).Export("h_setlist").
		NewFunctionBuilder().WithFunc(hs.goCall).Export("h_call").
		Instantiate(c.ctx)
	if err != nil {
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
