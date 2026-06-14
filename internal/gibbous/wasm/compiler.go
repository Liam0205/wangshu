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
	// PW3+ 逐档解锁(02-translation §1.3)。VARARG 永不加入。
	return c
}

// SupportsAllOpcodes 实现 F7 后端能力查询(03 §3.7 + 02 §5.2)。
//
// O(N) 单遍扫 proto.Code;任一 opcode 不在 supported 即返 false。
// 纯只读、不修改 Proto、不 panic(越界 opcode 编号天然落 false)。
//
// PW2 额外限制(单 BB 路径 + 可烧常量):
//   - 含 JMP 的 Proto:虽 JMP 在白名单,但 buildCFG 会切多 BB,translate
//     的单 BB 路径处理不了 → 这里提前拒(多 BB 留 PW3 完整 relooper);
//   - 含字符串常量的 LOADK:Consts 是 State 私有惰性 intern(编译期 Nil
//     占位),烧不出真 GCRef → 拒(留 PW5 经助手取)。
func (c *Compiler) SupportsAllOpcodes(proto *bytecode.Proto) bool {
	if len(proto.Code) == 0 {
		return true // 空 Proto vacuously supported(实际不会被 P2 判热点)
	}
	// PW2 单 BB 限制:只数**可达** BB(死代码块——RETURN 后的兜底 RETURN——
	// 不可达,不算多 BB)。多可达 BB(有控制流)留 PW3 relooper。
	cfg := buildCFG(proto)
	reach := cfg.reachableBlocks()
	if len(reach) != 1 {
		return false
	}
	// 只扫可达入口 BB 的 opcode(死代码块的指令永不执行,不影响支持判定)。
	entry := cfg.blocks[cfg.entry]
	for pc := entry.startPC; pc < entry.endPC; pc++ {
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
