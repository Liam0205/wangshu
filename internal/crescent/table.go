// Table get/set / upvalue / string utilities.
//
// tableGet/tableSet 是 raw 访问入口(不触发元方法;元方法链在 meta.go)。
// 自 P1 收尾轮起,表数据走 arena 原生 array+hash 布局(rawtable.go),
// 旁路 Go map 已移除。
package crescent

import (
	"math"
	"strconv"

	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// tableGet 实现 raw get。
func (st *State) tableGet(t arena.GCRef, key value.Value) (value.Value, *LuaError) {
	return st.rawGet(t, key), nil
}

// tableSet 实现 raw set。
func (st *State) tableSet(t arena.GCRef, key, val value.Value) *LuaError {
	return st.rawSet(t, key, val)
}

// tableSetInt 是 SETLIST 的快路径:整数键写入。
func (st *State) tableSetInt(t arena.GCRef, idx uint32, val value.Value) {
	_ = st.rawSet(t, value.NumberValue(float64(idx)), val)
}

// upvalGet / upvalSet:开放/关闭分派(05 §8.1)。
//
// 开放 upvalue 经 uvOwner 找属主 thread 的栈(协程各有独立栈,01 §5.4 的
// threadRef 语义);属主缺失时回退当前 thread(主线程场景)。
func (st *State) upvalGet(th *thread, uv arena.GCRef) value.Value {
	if object.UpvalIsClosed(st.arena, uv) {
		return object.UpvalClosedValue(st.arena, uv)
	}
	owner := st.uvOwnerOf(uv, th)
	idx := object.UpvalStackIdx(st.arena, uv)
	return owner.stack[idx]
}

func (st *State) upvalSet(th *thread, uv arena.GCRef, v value.Value) {
	if object.UpvalIsClosed(st.arena, uv) {
		st.arena.SetWordAt(uv+8*2, uint64(v))
		return
	}
	owner := st.uvOwnerOf(uv, th)
	idx := object.UpvalStackIdx(st.arena, uv)
	owner.stack[idx] = v
}

func (st *State) uvOwnerOf(uv arena.GCRef, fallback *thread) *thread {
	if st.uvOwner != nil {
		if o, ok := st.uvOwner[uv]; ok {
			return o
		}
	}
	return fallback
}

// findOrCreateUpval 查找或新建一个指向 thread.stack[stackIdx] 的开放 upvalue
// (按 stackIdx 降序链;05 §8.3)。
//
// P1 简化:用 thread 上的 Go map 缓存 stackIdx → uvRef(不影响共享语义);
// 完整降序链结构是值栈 arena 化的一部分(见 implementation-progress)。
func (st *State) findOrCreateUpval(th *thread, stackIdx uint32) arena.GCRef {
	if th.openUvs == nil {
		th.openUvs = map[uint32]arena.GCRef{}
	}
	if uv, ok := th.openUvs[stackIdx]; ok {
		return uv
	}
	uv := st.allocOpenUpvalue(0, stackIdx, 0)
	th.openUvs[stackIdx] = uv
	if st.uvOwner == nil {
		st.uvOwner = map[arena.GCRef]*thread{}
	}
	st.uvOwner[uv] = th
	return uv
}

// closeUpvals 关闭所有 stackIdx ≥ level 的开放 upvalue。
func (st *State) closeUpvals(th *thread, level int) {
	if th.openUvs == nil {
		return
	}
	for idx, uv := range th.openUvs {
		if int(idx) >= level {
			val := th.stack[idx]
			object.CloseUpvalue(st.arena, uv, val)
			delete(th.openUvs, idx)
			delete(st.uvOwner, uv)
		}
	}
}

// toStringBytes 把 Value 转为 []byte(用于 CONCAT)。
func (st *State) toStringBytes(v value.Value) ([]byte, bool) {
	if value.IsNumber(v) {
		f := value.AsNumber(v)
		return []byte(formatLuaNumber(f)), true
	}
	if value.Tag(v) == value.TagString {
		return object.StringBytes(st.arena, value.GCRefOf(v)), true
	}
	return nil, false
}

// FormatLuaNumber 暴露 %.14g 格式(stdlib tostring 与 difftest 共用一套,
// 保证 tostring(x) 与 CONCAT 的数字格式逐字节一致)。
func FormatLuaNumber(f float64) string { return formatLuaNumber(f) }

// formatLuaNumber 用 %.14g 格式化(05 §4.6)。
//
// Inf/NaN 措辞对齐 Lua 5.1 C printf:"inf" / "-inf" / "nan"(Go 默认 "+Inf"
// 等,差分会 byte-diff,12 §10 口径)。
func formatLuaNumber(f float64) string {
	if math.IsInf(f, 1) {
		return "inf"
	}
	if math.IsInf(f, -1) {
		return "-inf"
	}
	if f != f {
		return "nan"
	}
	return strconv.FormatFloat(f, 'g', 14, 64)
}

// stringCompare 字典序逐字节比较(05 §4.4)。返回 -1/0/+1。
func stringCompare(st *State, a, b arena.GCRef) int {
	ab := object.StringBytes(st.arena, a)
	bb := object.StringBytes(st.arena, b)
	for i := 0; i < len(ab) && i < len(bb); i++ {
		if ab[i] < bb[i] {
			return -1
		}
		if ab[i] > bb[i] {
			return +1
		}
	}
	if len(ab) < len(bb) {
		return -1
	}
	if len(ab) > len(bb) {
		return +1
	}
	return 0
}
