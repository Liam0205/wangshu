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

	// --- PW3 算术慢路径助手(快路径双 number 在 Wasm 内直发 f64,失败回 Go)---

	// Arith 处理算术 opcode 慢路径(ADD/SUB/MUL/DIV/MOD/POW)的 coercion +
	// 元方法链(复用 execute.go doArithSlow)。op 是 bytecode.OpCode 值。
	// 返回 status(0=OK / 1=ERR)。
	Arith(base, pc, op, b, c, a int32) int32
	// Unm 处理 UNM 慢路径(string coercion + __unm)。
	Unm(base, pc, b, a int32) int32
	// Len 处理 LEN(string 长度 / table border / 异类报错;复用 execute.go LEN 段)。
	Len(base, pc, b, a int32) int32
	// Concat 处理 CONCAT(复用 execute.go doConcat 全逻辑 + safepoint)。
	Concat(base, pc, a, b, c int32) int32

	// --- PW4 控制流慢路径助手 ---

	// Compare 处理 LT/LE 慢路径(string 比较 / __lt/__le 元方法)。op 是
	// bytecode.OpCode。返回 packed:bit0=比较结果(0/1),bit1=错误标志。
	Compare(base, pc, op, b, c int32) int32
	// Eq 处理 EQ 的 __eq 元方法路径(raw 不等时;复用 rawEqual + __eq)。
	// 返回 packed:bit0=结果,bit1=错误。
	Eq(base, pc, b, c int32) int32
	// ForPrep 处理 FORPREP:三槽校验 + coercion + 预减(复用 execute.go
	// FORPREP 段,byte-equal 错误消息)。返回 status(0=OK / 1=ERR)。
	ForPrep(base, pc, a int32) int32
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

// goArith: (base,pc,op,b,c,a i32) -> (i32)  对应 type 5 / h_arith(PW3)。
func (h *helperSet) goArith(base, pc, op, b, c, a int32) int32 {
	return h.host.Arith(base, pc, op, b, c, a)
}

// goUnm: (base,pc,b,a i32) -> (i32)  对应 type 6 / h_unm(PW3)。
func (h *helperSet) goUnm(base, pc, b, a int32) int32 {
	return h.host.Unm(base, pc, b, a)
}

// goLen: (base,pc,b,a i32) -> (i32)  对应 type 7 / h_len(PW3)。
func (h *helperSet) goLen(base, pc, b, a int32) int32 {
	return h.host.Len(base, pc, b, a)
}

// goConcat: (base,pc,a,b,c i32) -> (i32)  对应 type 8 / h_concat(PW3)。
func (h *helperSet) goConcat(base, pc, a, b, c int32) int32 {
	return h.host.Concat(base, pc, a, b, c)
}

// goCompare: (base,pc,op,b,c i32) -> (i32 packed)  对应 type 8 / h_compare(PW4)。
func (h *helperSet) goCompare(base, pc, op, b, c int32) int32 {
	return h.host.Compare(base, pc, op, b, c)
}

// goEq: (base,pc,b,c i32) -> (i32 packed)  对应 type 6 / h_eq(PW4)。
func (h *helperSet) goEq(base, pc, b, c int32) int32 {
	return h.host.Eq(base, pc, b, c)
}

// goForPrep: (base,pc,a i32) -> (i32 status)  对应 type 9 / h_forprep(PW4)。
func (h *helperSet) goForPrep(base, pc, a int32) int32 {
	return h.host.ForPrep(base, pc, a)
}
