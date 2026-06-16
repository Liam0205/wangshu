// Allocation helpers — 所有 GC 对象分配都过这里,完成 LinkSweep + 计费。
//
// safepoint 在分配点末尾由各 opcode 显式调 (st.safepoint(th, ci));M10 接入。
package crescent

import (
	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
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

// PreallocateArray 把表 t 的 array 段扩到 n 槽(issue #10 方向 2)。仅扩不缩;
// 原 array 段内容拷到新段(form-Y 现读 + 重写),原 hash 段不动。BumpGen(IC 失效)。
//
// 典型用法:NewTable + PreallocateArray(t, n) + SetIndex(1..n) 绕过反复 rehash
// 风暴——所有 SetIndex(1..n) 都落 array 段 O(1),整个构建净 O(n)。
func (st *State) PreallocateArray(t arena.GCRef, n uint32) {
	curr := object.TableASize(st.arena, t)
	if n <= curr {
		return // 仅扩不缩
	}
	// 读原 array 段内容(form-Y 现读,免缓存派生切片)
	oldArr := object.TableArrayRef(st.arena, t)
	var oldData []value.Value
	if curr > 0 {
		oldData = make([]value.Value, curr)
		for i := uint32(0); i < curr; i++ {
			oldData[i] = object.TableArrayAt(st.arena, t, i)
		}
	}
	// 分配新段 + 替换
	newArr := object.AllocTableArray(st.arena, n)
	st.gc.AllocCharge(n * 8)
	object.SetTableArray(st.arena, t, newArr, n)
	// 拷数据(SetTableArray 已让 t 指向 newArr,SetTableArrayAt 写新段)
	for i, v := range oldData {
		if v != value.Nil {
			object.SetTableArrayAt(st.arena, t, uint32(i), v)
		}
	}
	if !oldArr.IsNull() {
		st.arena.Free(oldArr, curr*8)
	}
	object.BumpGen(st.arena, t)
}

// NewArrayTableFromVals 建一个表,array 段预分配 n=len(vals) 槽,直接写入 vals;
// hash 段 0(无 hash)。issue #10 方向 2:从 Go slice 一次性构建 Lua array table,
// 免反复 rehash 风暴。返回 table-kind GCRef(调用方负责 PinRef)。
func (st *State) NewArrayTableFromVals(vals []value.Value) arena.GCRef {
	n := uint32(len(vals))
	t := st.allocTable(n, 0)
	for i, v := range vals {
		if v != value.Nil {
			object.SetTableArrayAt(st.arena, t, uint32(i), v)
		}
	}
	return t
}
