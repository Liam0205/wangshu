//go:build !wangshu_p4

package jit

// P4HostState placeholder interface for the default build.
type P4HostState interface {
	DoReturn(base int32, pc int32, a int32, b int32) int32
	SetReg(idx int32, val uint64)
	GetReg(idx int32) uint64
	GetUpval(base int32, b int32) uint64
	SetUpvalFromReg(base int32, a int32, b int32)
	Arith(base int32, pc int32, op int32, b int32, c int32, a int32) int32
	Unm(base int32, pc int32, b int32, a int32) int32
	Len(base int32, pc int32, b int32, a int32) int32
	NewTable(base int32, pc int32, a int32, b int32, c int32) int32
	GetTable(base int32, pc int32, a int32, b int32, c int32) int32
	SetTable(base int32, pc int32, a int32, b int32, c int32) int32
	DoGetGlobal(base int32, pc int32, a int32, bx int32) int32
	DoSetGlobal(base int32, pc int32, a int32, bx int32) int32
	Compare(base int32, pc int32, op int32, b int32, c int32) int32
	ArenaBaseAddr() uintptr
	ValueStackBaseAddr(base int32) uintptr
}
