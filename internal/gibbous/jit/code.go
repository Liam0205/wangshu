//go:build wangshu_p4

package jit

import (
	"errors"

	"github.com/Liam0205/wangshu/internal/bridge"
	"github.com/Liam0205/wangshu/internal/bytecode"
)

// p4Code 实装 `bridge.GibbousCode` 接口(`p2-bridge/05-p3-p4-interface.md`
// §6 + p4-method-jit/00-overview.md §1 边界表 GibbousCode 实现方行)。
//
// **PJ0 实装:全方法返错 / 占位**——本 struct 在 PJ0 不会被实例化(Compile
// 全返 ErrCompileNotImplemented),保留实装是为 PJ1 时直接填字段(codeAddr /
// jitContext / per-Proto 元数据)。
//
// 与 P3 `*p3Code`(`internal/gibbous/wasm/code.go`)的对位:
//   - P3 的 GibbousCode 包 wazero CompiledModule + api.Function 句柄;
//   - P4 的 GibbousCode 包原生码段(unsafe.Pointer)+ jitContext 句柄。
//
// 接口 4 方法(p3compiler.go 接口签名):
//   - Proto():反查 Proto,trampoline 校验用;
//   - Run(stack, base):跨层入口,经 trampoline asm stub 进 JIT 世界(05 §4.2);
//   - PendingErr():最近一次 Run 的内部错误(暂 P4 不需要,留对位);
//   - Slot():共享 funcref 表槽号——P4 原生码无 wasm 表概念,恒返 (0, false)
//     (永走同步 fallback,gibbous→gibbous 直跳走 P4 自家协议而非 wasm
//     call_indirect)。
type p4Code struct {
	proto *bytecode.Proto

	// PJ1+ 字段位:
	//   - codeAddr   uintptr  // exec 段起点(PROT_RX 后)
	//   - codeLen    uintptr  // 段长度
	//   - codePage   *codePage // 释放时 munmap
	//   - entryPC    map[uint32]uintptr // 字节码 pc → 机器地址(OSR exit 用,§4 物化)
}

// Proto 反向指针(trampoline 校验)。
func (c *p4Code) Proto() *bytecode.Proto {
	return c.proto
}

// Run 是 crescent→gibbous 跨层入口。
//
// **PJ0 实装:返错**(本路径 PJ0 不会被走到,因 Compile 全返错)。
//
// PJ1 起实装:
//   - 经 amd64/jitEnter asm stub 切 SP 到自管栈、装 jitContext 入 r15、
//     跳入 codeAddr;
//   - 三出口:0=OK / 1=ERR / 2=DEOPT(P4 投机失败 OSR exit;P3 永不返 2);
//   - stack[0] 入参=base、返回=status(同 P3 CallWithStack 协议)。
func (c *p4Code) Run(stack []uint64, base uint32) int32 {
	_ = stack
	_ = base
	// PJ0 不应被调到——bridge.installGibbous 没装产物因为 Compile 失败。
	// 防御性返 ERR(1)+ 设 pendingErr。
	return 1
}

// PendingErr 默认返本帧 Run 的内部错误(P4 PJ0 暂不需要,留对位 P3)。
func (c *p4Code) PendingErr() error {
	return ErrRunNotImplemented
}

// Slot 返回共享 funcref 表槽号 + 是否登记。
//
// **P4 原生码无 wasm 表概念**——恒返 (0, false),让上层走同步 Run fallback
// (gibbous→gibbous 调用走 P4 自家直跳协议,而非经 wasm `call_indirect`)。
// 承 `p2-bridge/05-p3-p4-interface.md` §6 GibbousCode.Slot 注:「P4 原生码
// 永走回退」。
func (c *p4Code) Slot() (uint32, bool) {
	return 0, false
}

// ErrRunNotImplemented:PJ0 占位错误——p4Code.Run 尚未实装。
var ErrRunNotImplemented = errors.New("internal/gibbous/jit: PJ0 skeleton — Run not implemented")

// 编译期断言:Compiler 实现 bridge.P3Compiler 接口;p4Code 实现 bridge.GibbousCode
// (任何接口签名漂移立即在编译期暴露,不等运行期)。
var (
	_ bridge.P3Compiler  = (*Compiler)(nil)
	_ bridge.GibbousCode = (*p4Code)(nil)
)
