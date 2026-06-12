// Raw table operations on the arena-native array+hash layout (01 §5.2).
//
// 替换 M9-M14 期间的 Go map 旁路(tableSide):
//   - 数组段:整数键 1..asize 直达 array[k-1];
//   - 哈希段:主位置 = hash(key) & hmask,开放寻址 + 冲突链(next 索引);
//   - rehash:数组与哈希按 Lua 5.1 luaH_resize 思路重算(装填率 > 50%),
//     rehash / 数组↔哈希迁移 BumpGen(IC 失效,05 §6.5);
//   - border:数组段二分(# 语义,01 §5.2)。
package crescent

import (
	"math"

	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// hashValue 把任意 Lua 键散列成 32-bit(对齐 5.1 的按类型散列思路)。
//
// -0.0 归一 +0.0 后再散列(01 §5.2 键归类规则)。string 直接用对象头里缓存的
// JSHash。其余 boxed 用 bits 折叠。
func (st *State) hashValue(key value.Value) uint32 {
	if value.IsNumber(key) {
		f := value.AsNumber(key)
		if f == 0 { // 归一 -0.0
			f = 0
		}
		bits := math.Float64bits(f)
		return uint32(bits) ^ uint32(bits>>32)
	}
	if value.Tag(key) == value.TagString {
		return object.StringHash(st.arena, value.GCRefOf(key))
	}
	bits := uint64(key)
	return uint32(bits) ^ uint32(bits>>32)
}

// normKey 把 -0.0 数字键归一为 +0.0(键比较与散列一致)。
func normKey(key value.Value) value.Value {
	if value.IsNumber(key) && value.AsNumber(key) == 0 {
		return value.NumberValue(0)
	}
	return key
}

// keyEqual 判定两个键相等(rawequal 语义:数字浮点比较,其余 bits 比较;
// string 已 intern 故 bits 等价内容)。
func keyEqual(a, b value.Value) bool {
	if value.IsNumber(a) && value.IsNumber(b) {
		return value.AsNumber(a) == value.AsNumber(b)
	}
	return a == b
}

// arrayIndex 判定 key 是否落数组段:k == floor(k) 且 1 <= k <= asize。
// 返回 (idx0based, true) 或 (0, false)。
func arrayIndex(key value.Value, asize uint32) (uint32, bool) {
	if !value.IsNumber(key) {
		return 0, false
	}
	f := value.AsNumber(key)
	k := uint32(f)
	if float64(k) == f && k >= 1 && k <= asize {
		return k - 1, true
	}
	return 0, false
}

// rawGet 在 arena 原生布局上查 key(不触发元方法)。
func (st *State) rawGet(t arena.GCRef, key value.Value) value.Value {
	if key == value.Nil {
		return value.Nil
	}
	key = normKey(key)
	asize := object.TableASize(st.arena, t)
	if idx, ok := arrayIndex(key, asize); ok {
		return object.TableArrayAt(st.arena, t, idx)
	}
	hsize := object.TableHSize(st.arena, t)
	if hsize == 0 {
		return value.Nil
	}
	hmask := hsize - 1
	i := int32(st.hashValue(key) & hmask)
	for i >= 0 {
		k := object.NodeKey(st.arena, t, uint32(i))
		if keyEqual(k, key) {
			return object.NodeVal(st.arena, t, uint32(i))
		}
		i = object.NodeNext(st.arena, t, uint32(i))
	}
	return value.Nil
}

// rawSet 在 arena 原生布局上写 key=val(不触发元方法)。
//
// val==Nil 是删除(槽位保留 key=Nil 化,链不收缩——与 5.1 一致,等 rehash 清理)。
func (st *State) rawSet(t arena.GCRef, key, val value.Value) *LuaError {
	if key == value.Nil {
		return errf("table index is nil")
	}
	if value.IsNumber(key) {
		f := value.AsNumber(key)
		if f != f {
			return errf("table index is NaN")
		}
	}
	key = normKey(key)
	asize := object.TableASize(st.arena, t)
	if idx, ok := arrayIndex(key, asize); ok {
		object.SetTableArrayAt(st.arena, t, idx, val)
		return nil
	}
	// 哈希段查找已有槽
	hsize := object.TableHSize(st.arena, t)
	if hsize > 0 {
		hmask := hsize - 1
		i := int32(st.hashValue(key) & hmask)
		for i >= 0 {
			k := object.NodeKey(st.arena, t, uint32(i))
			if keyEqual(k, key) {
				// 已存在:改值(val=Nil 即删,key 槽改 Nil 等 rehash 回收)
				if val == value.Nil {
					st.nodeSetKV(t, uint32(i), value.Nil, value.Nil)
				} else {
					st.nodeSetVal(t, uint32(i), val)
				}
				return nil
			}
			i = object.NodeNext(st.arena, t, uint32(i))
		}
	}
	if val == value.Nil {
		return nil // 删除不存在的键 = no-op
	}
	// 新键插入
	return st.insertNewKey(t, key, val)
}

// nodeSetVal 改一个哈希槽的 val(保留 key/next)。
func (st *State) nodeSetVal(t arena.GCRef, idx uint32, val value.Value) {
	k := object.NodeKey(st.arena, t, idx)
	next := object.NodeNext(st.arena, t, idx)
	object.SetNode(st.arena, t, idx, k, val, next)
}

// nodeSetKV 改一个哈希槽的 key+val(保留 next)。
func (st *State) nodeSetKV(t arena.GCRef, idx uint32, key, val value.Value) {
	next := object.NodeNext(st.arena, t, idx)
	object.SetNode(st.arena, t, idx, key, val, next)
}

// insertNewKey 插入一个新键(对齐 5.1 luaH_newkey 的链式策略,简化 Brent:
// 主位置被占时找空槽,若占用者主位置不同则迁走占用者)。
func (st *State) insertNewKey(t arena.GCRef, key, val value.Value) *LuaError {
	hsize := object.TableHSize(st.arena, t)
	if hsize == 0 {
		st.rehash(t, key)
		return st.rawSet(t, key, val)
	}
	hmask := hsize - 1
	mainPos := st.hashValue(key) & hmask
	mk := object.NodeKey(st.arena, t, mainPos)
	if mk == value.Nil && object.NodeVal(st.arena, t, mainPos) == value.Nil &&
		object.NodeNext(st.arena, t, mainPos) < 0 {
		// 主位置空:直接放
		object.SetNode(st.arena, t, mainPos, key, val, -1)
		return nil
	}
	// 主位置被占:找一个空槽
	free, ok := st.findFreeNode(t, hsize)
	if !ok {
		// 哈希满:rehash 后重插
		st.rehash(t, key)
		return st.rawSet(t, key, val)
	}
	occKey := object.NodeKey(st.arena, t, mainPos)
	occMain := st.hashValue(occKey) & hmask
	if occKey != value.Nil && occMain != mainPos {
		// 占用者不在它自己的主位置:把占用者迁到 free,腾出主位置给新键
		// 1) 找到占用者链上的前驱(从 occMain 沿链找指向 mainPos 的节点)
		prev := int32(occMain)
		for object.NodeNext(st.arena, t, uint32(prev)) != int32(mainPos) {
			prev = object.NodeNext(st.arena, t, uint32(prev))
		}
		// 2) 迁占用者
		occVal := object.NodeVal(st.arena, t, mainPos)
		occNext := object.NodeNext(st.arena, t, mainPos)
		object.SetNode(st.arena, t, free, occKey, occVal, occNext)
		// 3) 前驱指向新位置
		pk := object.NodeKey(st.arena, t, uint32(prev))
		pv := object.NodeVal(st.arena, t, uint32(prev))
		object.SetNode(st.arena, t, uint32(prev), pk, pv, int32(free))
		// 4) 新键落主位置
		object.SetNode(st.arena, t, mainPos, key, val, -1)
		return nil
	}
	// 占用者就在自己的主位置:新键放 free,链入主位置链
	mainNext := object.NodeNext(st.arena, t, mainPos)
	object.SetNode(st.arena, t, free, key, val, mainNext)
	mv := object.NodeVal(st.arena, t, mainPos)
	object.SetNode(st.arena, t, mainPos, occKey, mv, int32(free))
	return nil
}

// findFreeNode 自 lastfree 起向前找空槽(key=Nil 且 val=Nil 且 next=-1)。
func (st *State) findFreeNode(t arena.GCRef, hsize uint32) (uint32, bool) {
	lf := object.TableLastFree(st.arena, t)
	if lf >= hsize {
		lf = hsize
	}
	for lf > 0 {
		lf--
		if object.NodeKey(st.arena, t, lf) == value.Nil &&
			object.NodeVal(st.arena, t, lf) == value.Nil &&
			object.NodeNext(st.arena, t, lf) < 0 {
			object.SetTableLastFree(st.arena, t, lf)
			return lf, true
		}
	}
	return 0, false
}

// rehash 重算最优 asize/hsize 并把全部活键重插(extraKey 是即将插入的键,
// 计入统计)。对齐 5.1 luaH_resize 思路:数组装填率 > 50%。
func (st *State) rehash(t arena.GCRef, extraKey value.Value) {
	// 1) 收集全部活键值
	type kv struct{ k, v value.Value }
	var all []kv
	asize := object.TableASize(st.arena, t)
	for i := uint32(0); i < asize; i++ {
		v := object.TableArrayAt(st.arena, t, i)
		if v != value.Nil {
			all = append(all, kv{value.NumberValue(float64(i + 1)), v})
		}
	}
	hsize := object.TableHSize(st.arena, t)
	for i := uint32(0); i < hsize; i++ {
		k := object.NodeKey(st.arena, t, i)
		v := object.NodeVal(st.arena, t, i)
		if k != value.Nil && v != value.Nil {
			all = append(all, kv{k, v})
		}
	}
	// 2) 统计整数键分布,选最优 asize(2 的幂桶:装填率 > 50%)
	intCount := map[uint32]uint32{} // 桶 2^i 内的整数键数
	totalInt := uint32(0)
	maxKey := uint32(0)
	countIntKey := func(k value.Value) {
		if !value.IsNumber(k) {
			return
		}
		f := value.AsNumber(k)
		u := uint32(f)
		if float64(u) != f || u < 1 {
			return
		}
		totalInt++
		if u > maxKey {
			maxKey = u
		}
		// 落入桶 ceil(log2(u))
		b := uint32(0)
		for (uint32(1) << b) < u {
			b++
		}
		intCount[b]++
	}
	for _, e := range all {
		countIntKey(e.k)
	}
	countIntKey(extraKey)
	// 选 asize = 最大的 2^b 使 [1..2^b] 内整数键 > 2^(b-1)
	bestASize := uint32(0)
	acc := uint32(0)
	for b := uint32(0); b <= 31; b++ {
		acc += intCount[b]
		size := uint32(1) << b
		if acc > size/2 {
			bestASize = size
		}
		if size >= maxKey {
			break
		}
	}
	// 3) hsize = 容纳其余键的最小 2 的幂(留 1 空槽余量)
	nHash := uint32(len(all)) + 1 // +1 给 extraKey
	if bestASize > 0 {
		// 落数组的键不占哈希
		inArray := uint32(0)
		for _, e := range all {
			if _, ok := arrayIndex(normKey(e.k), bestASize); ok {
				inArray++
			}
		}
		if _, ok := arrayIndex(normKey(extraKey), bestASize); ok {
			inArray++
		}
		nHash -= inArray
	}
	newHSize := uint32(1)
	for newHSize < nHash+1 {
		newHSize <<= 1
	}
	if nHash == 0 {
		newHSize = 0
	}
	// 4) 分配新段并替换;旧段归还 freelist(附属块由头对象独占,无别名)
	oldArr := object.TableArrayRef(st.arena, t)
	oldASize := asize
	oldNode := object.TableNodeRef(st.arena, t)
	oldHSize := hsize
	var newArr arena.GCRef
	if bestASize > 0 {
		newArr = object.AllocTableArray(st.arena, bestASize)
		st.gc.AllocCharge(bestASize * 8)
	}
	var newNode arena.GCRef
	if newHSize > 0 {
		newNode = object.AllocTableNode(st.arena, newHSize)
		st.gc.AllocCharge(newHSize * 3 * 8)
	}
	object.SetTableArray(st.arena, t, newArr, bestASize)
	object.SetTableNode(st.arena, t, newNode, newHSize)
	object.SetTableLastFree(st.arena, t, newHSize)
	object.BumpGen(st.arena, t) // 形状变化 → IC 失效(05 §6.5)
	if !oldArr.IsNull() {
		st.arena.Free(oldArr, oldASize*8)
	}
	if !oldNode.IsNull() {
		st.arena.Free(oldNode, oldHSize*3*8)
	}
	// 5) 重插全部键值
	for _, e := range all {
		_ = st.rawSet(t, e.k, e.v)
	}
}

// rawBorder 计算 #t:数组段二分(t[n]~=nil && t[n+1]==nil);数组满则探哈希。
func (st *State) rawBorder(t arena.GCRef) uint32 {
	asize := object.TableASize(st.arena, t)
	if asize > 0 && object.TableArrayAt(st.arena, t, asize-1) == value.Nil {
		// 数组段内有 border:二分
		lo, hi := uint32(0), asize
		for hi-lo > 1 {
			m := (lo + hi) / 2
			if object.TableArrayAt(st.arena, t, m-1) == value.Nil {
				hi = m
			} else {
				lo = m
			}
		}
		return lo
	}
	// 数组满(或无数组段):从 asize 起在哈希里线性探测
	n := asize
	for {
		if st.rawGet(t, value.NumberValue(float64(n+1))) == value.Nil {
			return n
		}
		n++
	}
}

// rawNext 实现 next(t, key) 的迭代序:先数组段(索引序),后哈希段(槽序)。
//
// key=Nil 从头开始。返回 (nextKey, nextVal, ok);ok=false 表示迭代结束。
// 迭代序确定性:同一表同一形状下序稳定(12 pairs 序口径的"严格逐字节"前提)。
func (st *State) rawNext(t arena.GCRef, key value.Value) (value.Value, value.Value, bool, *LuaError) {
	asize := object.TableASize(st.arena, t)
	hsize := object.TableHSize(st.arena, t)
	// 决定起始位置
	startArr := uint32(0)  // 下一个要检查的数组下标
	startNode := uint32(0) // 下一个要检查的哈希槽
	inHash := false
	if key != value.Nil {
		key = normKey(key)
		if idx, ok := arrayIndex(key, asize); ok {
			startArr = idx + 1
		} else {
			// 在哈希段找到 key 的槽位,从下一槽继续
			found := false
			if hsize > 0 {
				hmask := hsize - 1
				i := int32(st.hashValue(key) & hmask)
				for i >= 0 {
					if keyEqual(object.NodeKey(st.arena, t, uint32(i)), key) {
						startNode = uint32(i) + 1
						found = true
						break
					}
					i = object.NodeNext(st.arena, t, uint32(i))
				}
			}
			if !found {
				return value.Nil, value.Nil, false, errf("invalid key to 'next'")
			}
			inHash = true
		}
	}
	if !inHash {
		for i := startArr; i < asize; i++ {
			v := object.TableArrayAt(st.arena, t, i)
			if v != value.Nil {
				return value.NumberValue(float64(i + 1)), v, true, nil
			}
		}
	}
	for i := startNode; i < hsize; i++ {
		k := object.NodeKey(st.arena, t, i)
		v := object.NodeVal(st.arena, t, i)
		if k != value.Nil && v != value.Nil {
			return k, v, true, nil
		}
	}
	return value.Nil, value.Nil, false, nil
}
