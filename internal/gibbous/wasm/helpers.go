//go:build wangshu_p3

package wasm

// imported helper 的 Go 侧实装(02-translation §6.4 + 04-trampoline §3)。
//
// **依赖解环**:helper 需要操作执行期状态(CallInfo、值栈、upvalue、返回值
// 回填),这些归 crescent.State。但 crescent 要 import gibbous/wasm 取
// p3Code.Run(trampoline),若 gibbous/wasm 反 import crescent 则成环。
// 解法(同 P2 bridge 不依赖 crescent 的接口注入手法):gibbous/wasm 定义
// helper 需要的最小抽象接口 HostState,crescent 实现并在升层安装时注入。
//
// PW2 helper 集:h_getupval / h_setupval / h_return / h_safepoint。

// HostState 是 gibbous helper 操作执行期状态的最小抽象(crescent.State 实现)。
//
// 所有方法以 base(当前帧 R0 的字节偏移)为坐标 —— gibbous 帧与解释器帧
// 共享同一值栈(03-memory-model),base 唯一定位本帧。
//
// 方法语义直接复用解释器侧已有实装(execute.go 对应行),保证 byte-equal。
type HostState interface {
	// GetUpval 取当前 closure 的 upvalue B 的值(execute.go GETUPVAL 段)。
	GetUpval(base int32, b int32) uint64
	// SetUpval 写当前 closure 的 upvalue B(execute.go SETUPVAL 段)。
	SetUpval(base int32, b int32, val uint64)
	// DoReturn 处理 RETURN A B:返回值回填到调用者期望槽 + 记 savedPC。
	// 返回 status(0=OK / 1=ERR);gibbous 函数据此 return。
	DoReturn(base int32, pc int32, a int32, b int32) int32
	// Safepoint 回边 GC 检查点(PW4 用;PW2 先声明)。
	Safepoint(base int32, pc int32)
	// SetSavedPC 写回 CallInfo.savedPC(pc 物化,02 §4.2)。
	SetSavedPC(base int32, pc int32)
}

// helperSet 持有注入的 HostState,提供给 wazero 注册的 Go callback。
//
// 每个 State 一份(arena/Runtime 单 State 私有)。wazero host module 的
// callback 闭包捕获 helperSet,调用时转发到 HostState。
type helperSet struct {
	host HostState
}

// --- wazero host function callback(签名匹配 module.go 的 type 声明)---
//
// wazero 用反射匹配 Go 函数签名到 Wasm 类型:i32→int32/uint32,i64→uint64。
// callback 参数顺序与 module.go import 声明的 type 一致。

// goGetUpval: (base i32, b i32) -> (i64)  对应 type 0 / h_getupval。
func (h *helperSet) goGetUpval(base int32, b int32) uint64 {
	return h.host.GetUpval(base, b)
}

// goSetUpval: (base i32, b i32, val i64) -> ()  对应 type 1 / h_setupval。
func (h *helperSet) goSetUpval(base int32, b int32, val uint64) {
	h.host.SetUpval(base, b, val)
}

// goReturn: (base i32, pc i32, a i32, b i32) -> (i32)  对应 type 2 / h_return。
func (h *helperSet) goReturn(base int32, pc int32, a int32, b int32) int32 {
	return h.host.DoReturn(base, pc, a, b)
}

// goSafepoint: (base i32, pc i32) -> ()  对应 type 3 / h_safepoint(PW4 用)。
func (h *helperSet) goSafepoint(base int32, pc int32) {
	h.host.Safepoint(base, pc)
}
