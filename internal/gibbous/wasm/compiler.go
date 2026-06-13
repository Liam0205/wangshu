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

	// supported[op] = 该 opcode 是否已实装 Wasm 翻译。
	// PW1:全 false(保守缺省,02-translation §1.3 + §5.2)。
	// PW2+:各 PW 初始化时把对应 opcode 标 true。
	supported [numOpcodes]bool
}

// numOpcodes 是 P1 活跃 opcode 数(0..37)+ 预留区上界守卫。
// bytecode.OpCode 是 6-bit(0..63);supported 数组按 64 长度建,
// 越界 opcode(38..63 预留)天然落 false。
const numOpcodes = 64

// NewCompiler 构造一个 Compiler(PW1:supported 全 false)。
//
// runtime 由门面层(wangshu.NewState 在 wangshu_p3 build 下)创建并注入,
// 与 memadapter.MemoryHolder 共用同一 Runtime——确保 gibbous module 经
// import memory 能共享 arena 收养的那块 linear memory。
func NewCompiler(ctx context.Context, runtime wazero.Runtime) *Compiler {
	return &Compiler{
		ctx:     ctx,
		runtime: runtime,
		// PW1:supported 全 false(零值)。PW2 起在此标 true。
	}
}

// SupportsAllOpcodes 实现 F7 后端能力查询(03 §3.7 + 02 §5.2)。
//
// O(N) 单遍扫 proto.Code;任一 opcode 不在 supported 即返 false。
// 纯只读、不修改 Proto、不 panic(越界 opcode 编号天然落 false)。
//
// PW1:supported 全 false ⇒ 对任何非空 Proto 都返 false ⇒ F7 拦下所有
// Proto ⇒ 无 Proto 升层(与 P1-only 等价)。
func (c *Compiler) SupportsAllOpcodes(proto *bytecode.Proto) bool {
	for _, ins := range proto.Code {
		op := bytecode.Op(ins)
		if int(op) >= numOpcodes || !c.supported[op] {
			return false
		}
	}
	// 空 Proto(无指令)理论上 supported,但 PW1 supported 全 false 时
	// 上面循环对非空 Proto 必返 false;空 Proto 走到这里返 true 也无害
	// (空 Proto 不会被 P2 判热点)。PW1 阶段实际不会有 Proto 过 F7。
	return true
}

// Compile 把 Proto 编译成 GibbousCode(02 §5.1 + §5.5 panic 兜底)。
//
// PW1:无任何 opcode 翻译实装。理论上本函数不会被调用(SupportsAllOpcodes
// 永远 false ⇒ F7 拦下 ⇒ considerPromotion 的 try-compile 路径不会进)。
// 防御性返回 BackendDeclined error——若真被调到(F7 漏判),P2 转 TierStuck
// 永久解释,不崩溃。
//
// defer recover 兜底:后端 panic 转 *CompileError(Kind=BackendPanic),
// 不让 panic 穿越本接口(02 §1.4 失败原子性)。
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

	// PW1 占位:无 opcode 翻译实装,声明性拒绝。PW2 起替换为真翻译主流程
	// (02 §6.2)。
	return nil, &bridge.CompileError{
		Kind:   bridge.CompileErrBackendDeclined,
		Proto:  proto,
		Reason: "p3 PW1: no opcode translation implemented yet (SupportsAllOpcodes should have gated this)",
	}
}
