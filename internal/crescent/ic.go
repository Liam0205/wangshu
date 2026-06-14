// Inline cache execution (05 §6) — mono IC on GETGLOBAL/SETGLOBAL/GETTABLE/
// SETTABLE/SELF.
//
// 命中 = 同表(tableRef 低 32 位)+ 同代次(gen)→ 直达 array/node 槽,跳过
// 散列与冲突链。未命中 = 完整查找 + 回填 slot。写侧失效:rehash/setmetatable
// BumpGen(rawtable.go / object.SetTableMeta)。
package crescent

import (
	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// icGetTable 带 IC 的表读(GETTABLE/SELF/GETGLOBAL 共用):
// 先查 IC 直达;miss 走 rawGetWithLoc 回填;raw nil 再走 __index 链。
//
// 命中校验 = 同表 + 同代次 + 同键:GETTABLE/SETTABLE 的 key 是动态 RK,
// 同一 pc 可能轮换不同 key(典型:循环里 t[i]),array 命中必须验
// arrayIndex(key)==Index,node 命中必须验 NodeKey==key。
func (st *State) icGetTable(th *thread, ci *callInfo, pc int32, obj, key value.Value) (value.Value, *LuaError) {
	if value.Tag(obj) != value.TagTable {
		return st.indexWithMeta(th, obj, key)
	}
	t := value.GCRefOf(obj)
	slot := &st.protoOf(ci).IC[pc]
	if slot.Kind == bytecode.ICKindArrayHit &&
		slot.TableRef == uint32(t) &&
		slot.Shape == object.TableGen(st.arena, t) {
		if idx, ok := arrayIndex(normKey(key), object.TableASize(st.arena, t)); ok && idx == slot.Index {
			v := object.TableArrayAt(st.arena, t, slot.Index)
			if v != value.Nil {
				return v, nil
			}
		}
		// 键不同或槽位变 nil:退回慢路径
	} else if slot.Kind == bytecode.ICKindNodeHit &&
		slot.TableRef == uint32(t) &&
		slot.Shape == object.TableGen(st.arena, t) {
		if keyEqual(object.NodeKey(st.arena, t, slot.Index), normKey(key)) {
			v := object.NodeVal(st.arena, t, slot.Index)
			if v != value.Nil {
				return v, nil
			}
		}
	}
	// —— 未命中:完整查找 + 回填 ——
	v, where, idx := st.rawGetWithLoc(t, key)
	if where != locNone && v != value.Nil {
		// P2+ #4 megamorphic 主动识别:若该 slot 已被填过(kind != 0)且
		// 当前要重填的目标表不同(TableRef != t)或代次不同(Shape != gen),
		// 即「miss-after-fill 重填」事件——累计 Refill 计数,P2 聚合时阈值
		// 触发翻译为 FBTableMega(02 §6.2 方案 (B) 简化版)。
		if slot.Kind != bytecode.ICKindNone &&
			(slot.TableRef != uint32(t) || slot.Shape != object.TableGen(st.arena, t)) {
			if slot.Refill < ^uint8(0) {
				slot.Refill++
			}
		}
		slot.TableRef = uint32(t)
		slot.Shape = object.TableGen(st.arena, t)
		slot.Index = idx
		if where == locArray {
			slot.Kind = bytecode.ICKindArrayHit
		} else {
			slot.Kind = bytecode.ICKindNodeHit
		}
		return v, nil
	}
	// raw miss → __index 链
	return st.indexWithMeta(th, obj, key)
}

// icSetTable 带 IC 的表写(SETTABLE/SETGLOBAL 共用):
// IC 命中 = 已存在键的"改值"快路径(改值不 bump gen,IC 持续有效)。
func (st *State) icSetTable(th *thread, ci *callInfo, pc int32, obj, key, val value.Value) *LuaError {
	if value.Tag(obj) != value.TagTable {
		return st.setIndexWithMeta(th, obj, key, val)
	}
	t := value.GCRefOf(obj)
	slot := &st.protoOf(ci).IC[pc]
	if val != value.Nil { // 删除(置 nil)走慢路径(可能 rehash 语义)
		if slot.Kind == bytecode.ICKindArrayHit &&
			slot.TableRef == uint32(t) &&
			slot.Shape == object.TableGen(st.arena, t) {
			if idx, ok := arrayIndex(normKey(key), object.TableASize(st.arena, t)); ok && idx == slot.Index {
				if object.TableArrayAt(st.arena, t, slot.Index) != value.Nil {
					object.SetTableArrayAt(st.arena, t, slot.Index, val)
					return nil
				}
			}
		} else if slot.Kind == bytecode.ICKindNodeHit &&
			slot.TableRef == uint32(t) &&
			slot.Shape == object.TableGen(st.arena, t) {
			if keyEqual(object.NodeKey(st.arena, t, slot.Index), normKey(key)) &&
				object.NodeVal(st.arena, t, slot.Index) != value.Nil {
				st.nodeSetVal(t, slot.Index, val)
				return nil
			}
		}
	}
	// 未命中:完整写路径(__newindex 链);写后若键已就位,回填 IC
	if e := st.setIndexWithMeta(th, obj, key, val); e != nil {
		return e
	}
	if val != value.Nil {
		if _, where, idx := st.rawGetWithLoc(t, key); where != locNone {
			// P2+ #4 同 doGetTable:miss-after-fill 重填计数
			if slot.Kind != bytecode.ICKindNone &&
				(slot.TableRef != uint32(t) || slot.Shape != object.TableGen(st.arena, t)) {
				if slot.Refill < ^uint8(0) {
					slot.Refill++
				}
			}
			slot.TableRef = uint32(t)
			slot.Shape = object.TableGen(st.arena, t)
			slot.Index = idx
			if where == locArray {
				slot.Kind = bytecode.ICKindArrayHit
			} else {
				slot.Kind = bytecode.ICKindNodeHit
			}
		}
	}
	return nil
}

// 查找位置标记(05 §6.3 RawGetWithLoc)。
type tableLoc uint8

const (
	locNone tableLoc = iota
	locArray
	locNode
)

// rawGetWithLoc 完整查找并返回命中位置(array 下标 / node 槽号),供 IC 回填。
func (st *State) rawGetWithLoc(t arena.GCRef, key value.Value) (value.Value, tableLoc, uint32) {
	if key == value.Nil {
		return value.Nil, locNone, 0
	}
	key = normKey(key)
	asize := object.TableASize(st.arena, t)
	if idx, ok := arrayIndex(key, asize); ok {
		return object.TableArrayAt(st.arena, t, idx), locArray, idx
	}
	hsize := object.TableHSize(st.arena, t)
	if hsize == 0 {
		return value.Nil, locNone, 0
	}
	hmask := hsize - 1
	i := int32(st.hashValue(key) & hmask)
	for i >= 0 {
		if keyEqual(object.NodeKey(st.arena, t, uint32(i)), key) {
			return object.NodeVal(st.arena, t, uint32(i)), locNode, uint32(i)
		}
		i = object.NodeNext(st.arena, t, uint32(i))
	}
	return value.Nil, locNone, 0
}
