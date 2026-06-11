// Allocation helpers — 所有 GC 对象分配都过这里,完成 LinkSweep + 计费。
//
// safepoint 在分配点末尾由各 opcode 显式调 (st.safepoint(th, ci));M10 接入。
package crescent

import (
	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/object"
)

// allocLuaClosure 分配一个 Lua closure 并 link 进 sweep 链 + 计费。
func (st *State) allocLuaClosure(protoID uint32, nupvals uint16) arena.GCRef {
	ref := object.AllocLuaClosure(st.arena, protoID, nupvals)
	st.gc.LinkSweep(ref)
	st.gc.AllocCharge(uint32(2+nupvals) * 8)
	return ref
}

// allocOpenUpvalue 分配一个开放 upvalue。
func (st *State) allocOpenUpvalue(threadRef arena.GCRef, stackIdx uint32, next arena.GCRef) arena.GCRef {
	ref := object.AllocOpenUpvalue(st.arena, threadRef, stackIdx, next)
	st.gc.LinkSweep(ref)
	st.gc.AllocCharge(3 * 8)
	return ref
}

// allocTable 分配一个表头(及 array/hash 附属块)。
func (st *State) allocTable(asize, hsize uint32) arena.GCRef {
	ref := object.AllocTable(st.arena, asize, hsize)
	st.gc.LinkSweep(ref)
	bytes := uint32(6*8) + asize*8 + hsize*3*8
	st.gc.AllocCharge(bytes)
	return ref
}

// safepoint 在分配 opcode 末尾(05 §5.2 /§5.3)调:
// 检查阈值,若需要则触发一次 STW Collect。
//
// 调用前活跃 Value 必须从根可达 — thread 栈与 currentCI 经 ExtraValues 自动覆盖,
// 中间 Go 局部里 transient 的 GCRef 由 caller 用 shadow stack 显式 push/pop。
func (st *State) safepoint(_ *thread, _ *callInfo) {
	st.gc.MaybeCollect()
}
