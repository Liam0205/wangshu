//go:build wangshu_p4

package jit

// P4HostState 是 P4 简化形态需要从 host(crescent)调用的最小抽象接口。
//
// **依赖解环**(承 docs/design/p4-method-jit/05-system-pipeline.md §4.3 +
// gibbous/wasm/helpers.go::HostState 同款手法):p4Code.Run 需要调 host 的
// DoReturn 弹帧(因为 P4 简化形态 mmap 段不内调 host helper),但 jit 包不能
// import crescent(成环)。解法:本接口由 crescent.State 实装,wireP4 时注入。
//
// **PJ7 真接入用**:p4Code 持本接口,Run 调用方完成「值已写回 R(A)」后,
// 调本接口的 DoReturn 让 host 完成「按 nresults 移结果到 funcIdx + 弹帧 +
// 恢复 caller top」(承 gibbous_host.go::DoReturn 同款语义)。
//
// **与 P3 HostState 的关系**:P3 HostState 是 wasm helper 集(GetUpval /
// SetUpval / DoReturn / Safepoint / Arith / GetTable 等 ~25 个方法);P4 简化
// 形态只需 DoReturn 一个,不引入完整 helper 表(留 PJ8+ 算术族真接入时扩)。
type P4HostState interface {
	// DoReturn 处理 P4 帧 RETURN A B:返回值回填到调用者期望槽 + 弹帧。
	//
	// 参数:
	//   - base:本帧 R0 字节偏移(= P4 编译产物 retA 写入位置的同款基准);
	//   - pc:RETURN 指令 pc(P4 编译期固化,= 1 即字节码下标 1);
	//   - a:RETURN A(返回值起点);
	//   - b:RETURN B(B-1 = 返回值个数,B=2 即返回 1 个值);
	//
	// 返回:status(0=OK / 1=ERR)。P4 简化形态下永返 0(无错路径)。
	//
	// 实装由 crescent.State 提供(gibbous_host.go::DoReturn,与 P3 共用)。
	DoReturn(base int32, pc int32, a int32, b int32) int32

	// SetReg 直接写当前帧的 R(idx) 槽位为 val(NaN-box u64)。
	//
	// **PJ7 P4 简化形态专用**:p4Code.Run 在 mmap 段执行后,需要把 RAX(NaN-box
	// 值)写到 R(retA) 槽位(arena 值栈,与 enterGibbous 给的 stack 参数无关——
	// P3 的 stack 参数是 wazero CallWithStack 1 槽 buffer 协议,与 P4 不兼容)。
	//
	// 参数:
	//   - idx:寄存器号(R(idx))
	//   - val:NaN-box u64 值
	//
	// 实装由 crescent.State 提供(经 thread.cur.base + idx 算 stack 位置写)。
	SetReg(idx int32, val uint64)
}

// SetHostState 把 host(crescent)抽象注入本 Compiler。
//
// **per-Compiler 单例**(承 wireP4 调用契约):每个 State 一份 *Compiler,本
// 方法在 wireP4 单 goroutine 内调一次;后续 Compile 产出 p4Code 时把 Compiler
// 的 hostState 复制到 p4Code 字段;p4Code.Run 用自己持有的 hostState(per-
// p4Code 单 writer-then-reader,无并发 write)。
//
// 这避免了 package-level global hostState 的多 State 并发写 race(V18 -race
// 友好,承 design-claims-vs-codebase-physics 纪律——每发现一次 race 修一次)。
func (c *Compiler) SetHostState(h P4HostState) {
	c.hostState = h
}
