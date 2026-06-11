// Table get/set / upvalue / string utilities.
//
// M9 简化:table 操作用 Go map 旁路实现(GCRef → map[Value]Value),
// 不走 arena 的 array/hash 节点段。M10 接入 IC 时换原生哈希实现。
package crescent

import (
	"strconv"

	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// tableSide 是 M9 的临时旁路存储(M10 用 arena 原生哈希取代)。
//
// 注:此处用 Go 堆 + map[uint64]Value,key 是 NaN-boxed Value 的 bits。
// 等价键(数字 +0/-0、不同 GCRef 的同值字符串)在此简化实现里不会合并,
// 但 M9 范围内不会触发(string 走 intern,数字键也不会有 +0/-0 用例)。
type tableSide struct {
	data map[uint64]value.Value
	meta arena.GCRef // metatable(M11;0 = 无)
}

func (st *State) sideOf(t arena.GCRef) *tableSide {
	if st.tableSides == nil {
		st.tableSides = map[arena.GCRef]*tableSide{}
	}
	s := st.tableSides[t]
	if s == nil {
		s = &tableSide{data: map[uint64]value.Value{}}
		st.tableSides[t] = s
	}
	return s
}

// keyHash 把 Value 当作哈希键的 uint64 bits。number 走 canonical bits;string 走 GCRef bits;
// nil/false/true 也是 bits。这与 Lua 5.1 的 raw equal 在 M9 范围内一致(不处理 +0/-0)。
func keyHash(v value.Value) uint64 { return uint64(v) }

// tableGet 实现 raw get(M9 不走 __index 元方法)。
func (st *State) tableGet(t arena.GCRef, key value.Value) (value.Value, *LuaError) {
	if key == value.Nil {
		return value.Nil, nil
	}
	s := st.sideOf(t)
	v, ok := s.data[keyHash(key)]
	if !ok {
		return value.Nil, nil
	}
	return v, nil
}

// tableSet 实现 raw set(M9 不走 __newindex 元方法)。
func (st *State) tableSet(t arena.GCRef, key, val value.Value) *LuaError {
	if key == value.Nil {
		return errf("table index is nil")
	}
	if value.IsNumber(key) {
		x := value.AsNumber(key)
		if x != x { // NaN
			return errf("table index is NaN")
		}
	}
	s := st.sideOf(t)
	if val == value.Nil {
		delete(s.data, keyHash(key))
	} else {
		s.data[keyHash(key)] = val
	}
	return nil
}

// tableSetInt 是 SETLIST 的快路径:整数键写入。
func (st *State) tableSetInt(t arena.GCRef, idx uint32, val value.Value) {
	key := value.NumberValue(float64(idx))
	s := st.sideOf(t)
	if val == value.Nil {
		delete(s.data, keyHash(key))
	} else {
		s.data[keyHash(key)] = val
	}
}

// tableBorder 计算 # 运算的 border:满足 t[n]≠nil 且 t[n+1]==nil 的最大 n(简单线性)。
func tableBorder(_ *arena.Arena, _ arena.GCRef) uint32 {
	// M9 简化版:旁路存储里没法快速找 border;返回 0(够 string len 不依赖即可)。
	// M10 切到 arena 哈希后做正确 border 二分。
	return 0
}

// upvalGet / upvalSet:开放/关闭分派(05 §8.1)。
func (st *State) upvalGet(th *thread, uv arena.GCRef) value.Value {
	if object.UpvalIsClosed(st.arena, uv) {
		return object.UpvalClosedValue(st.arena, uv)
	}
	idx := object.UpvalStackIdx(st.arena, uv)
	return th.stack[idx]
}

func (st *State) upvalSet(th *thread, uv arena.GCRef, v value.Value) {
	if object.UpvalIsClosed(st.arena, uv) {
		st.arena.SetWordAt(uv+8*2, uint64(v))
		return
	}
	idx := object.UpvalStackIdx(st.arena, uv)
	th.stack[idx] = v
}

// findOrCreateUpval 查找或新建一个指向 thread.stack[stackIdx] 的开放 upvalue
// (按 stackIdx 降序链;05 §8.3)。
//
// M9 简化:用 thread 上的 Go map 缓存 stackIdx → uvRef,不构建降序链(不影响
// 共享语义);完整链结构在 M10 接入闭合时切换。
func (st *State) findOrCreateUpval(th *thread, stackIdx uint32) arena.GCRef {
	if th.openUvs == nil {
		th.openUvs = map[uint32]arena.GCRef{}
	}
	if uv, ok := th.openUvs[stackIdx]; ok {
		return uv
	}
	uv := st.allocOpenUpvalue(0, stackIdx, 0)
	th.openUvs[stackIdx] = uv
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
		}
	}
}

// toStringBytes 把 Value 转为 []byte(用于 CONCAT)。
func (st *State) toStringBytes(v value.Value) ([]byte, bool) {
	if value.IsNumber(v) {
		// %.14g 格式(05 §4.6)
		f := value.AsNumber(v)
		return []byte(formatLuaNumber(f)), true
	}
	if value.Tag(v) == value.TagString {
		return object.StringBytes(st.arena, value.GCRefOf(v)), true
	}
	return nil, false
}

// formatLuaNumber 用 %.14g 格式化(05 §4.6)。
func formatLuaNumber(f float64) string {
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
