// 公共面 Table API——让宿主 Go 端构造、读写、传递 Lua 表
// (issue #2:pineapple common-mode 列内核形态投喂 N item 整列数据进 VM)。
// 设计形态:11 §4.5 sum-type Value 加 kTable kind;同 kFunction 走 State pin
// 表当 GC 根;Set/Get/SetIndex/GetIndex/Len 转发 internal RawGet/RawSet/RawBorder。
package wangshu

import (
	"fmt"

	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/value"
)

// Table 是公共面对 Lua 表的不透明句柄,经 State.NewTable 创建或由
// GetGlobal / Call 返回值取出(Value.AsTable())。生命期由所属 Value
// 的 Release() 控制(同 kFunction 的 pin 表机制)。
//
// 并发:同 State,单 goroutine(11 §8)。
//
// 性能档位:Set/Get/SetIndex/GetIndex/ForEach 每次跨 Go ↔ VM 边界
// 一次(per-item 形态,design-premises 前提一)。适合「构造一次投喂」
// 「脚本返一次读出」类 setup/teardown 形态;**不适合**在 Go 端循环里
// 反复 SetIndex/GetIndex 大批量数据(那是 arena 列轨场景,见
// [[embedding-contract]] arena ABI 节,零拷贝读)。Len 是 O(log N)
// 数组段二分,可低频用。
type Table struct {
	st     *State
	pinIdx uint32
}

// AsTable 把一个 table-kind Value 取出可操作句柄;非 table-kind 返回 nil。
func (v Value) AsTable() *Table {
	if !v.IsTable() {
		return nil
	}
	return &Table{st: v.fnState, pinIdx: v.pinIdx}
}

// NewTable 创建一个空 Lua 表并以 Value 形式返回(table-kind,经 State pin
// 表登记为 GC 根)。返回值需在不再使用时 Release(),否则 pin 槽位累积。
func (st *State) NewTable() Value {
	ref := st.core.NewLibTable(0)
	idx := st.core.PinRef(ref)
	return Value{kind: kTable, fnState: st, pinIdx: idx}
}

// NewArrayTable 一次性从 Go slice 构建 Lua array table(issue #10 方向 2)。
//
// 内部分配 array 段 = len(vals),写入 vals,无 rehash 风暴。**远快于** NewTable +
// 反复 SetIndex(rehash 风暴 → O(N²);本方法 O(N))。返回 table-kind Value,经
// pin 表登记 GC 根。
//
// 典型用法:host 把 []float64 / []string 列数据 lift 进 Lua table 投喂脚本。
//
//	tv := st.NewArrayTable([]wangshu.Value{
//	    wangshu.Number(1.0), wangshu.Number(2.0), wangshu.Number(3.0),
//	})
//	defer tv.Release()
//	st.SetGlobal("xs", tv)
func (st *State) NewArrayTable(vals []Value) Value {
	inner := make([]value.Value, len(vals))
	for i, v := range vals {
		inner[i] = v.toInner(st)
	}
	ref := st.core.NewArrayTableFromVals(inner)
	idx := st.core.PinRef(ref)
	return Value{kind: kTable, fnState: st, pinIdx: idx}
}

// NewFloatArrayTable 从 []float64 一次性构建 Lua array table(issue #13)。
//
// 与 NewArrayTable([]Value) 的区别:跳过宿主侧 []Value 物化与逐元素 toInner
// 类型分支,直接把 float64 NaN-box 进 arena 数组段——pineapple 一类
// boundary-dominated 嵌入者 common-mode 批量灌列的快路径,免走
// `[]any → []Value → []value.Value` 三层中转。
//
// 脚本侧看到的是普通 array table:`xs[i]`(1-based)、`#xs`、`for k,v in pairs(xs)`
// 全部正常。**不是** arena 列轨的 `__index` 代理表——零脚本改动,跨引擎
// `lua_script` 字节对等不受影响。
//
// 性能档位:`NewArrayTable` 同款 O(N) bulk 写入(走 internal NewArrayTableFromVals,
// 跳 rehash 风暴);相比 NewArrayTable 省一个 []Value 中间 slice + N 次类型分支。
//
//	tv := st.NewFloatArrayTable([]float64{1.0, 2.0, 3.0})
//	defer tv.Release()
//	st.SetGlobal("xs", tv)
func (st *State) NewFloatArrayTable(vals []float64) Value {
	inner := make([]value.Value, len(vals))
	for i, f := range vals {
		inner[i] = value.NumberValue(f)
	}
	ref := st.core.NewArrayTableFromVals(inner)
	idx := st.core.PinRef(ref)
	return Value{kind: kTable, fnState: st, pinIdx: idx}
}

// NewInt64ArrayTable 从 []int64 一次性构建 Lua array table(issue #13)。
//
// 形态同 NewFloatArrayTable;int64 元素先转 float64 再 NaN-box。承袭
// `Arena.AddInt64Column` 的 |v| > 2^53 报错规则(评审决策第 3 项,见
// `arena_abi.go` AddInt64Column 节):超出 float64 尾数精度的元素会
// 在物化与 pin 之前提前返回错误,避免静默精度损失。
//
//	tv, err := st.NewInt64ArrayTable([]int64{10, 20, 30})
//	if err != nil { ... }
//	defer tv.Release()
//	st.SetGlobal("xs", tv)
func (st *State) NewInt64ArrayTable(vals []int64) (Value, error) {
	inner := make([]value.Value, len(vals))
	for i, v := range vals {
		if v > 1<<53 || v < -(1<<53) {
			return Nil(), fmt.Errorf("wangshu: NewInt64ArrayTable: element %d (=%d) exceeds 2^53 precision range", i, v)
		}
		inner[i] = value.NumberValue(float64(v))
	}
	ref := st.core.NewArrayTableFromVals(inner)
	idx := st.core.PinRef(ref)
	return Value{kind: kTable, fnState: st, pinIdx: idx}, nil
}

// NewBoolArrayTable 从 []bool 一次性构建 Lua array table(issue #13)。
//
// 形态同 NewFloatArrayTable;bool 元素直接 NaN-box,无装箱开销。
//
//	tv := st.NewBoolArrayTable([]bool{true, false, true})
//	defer tv.Release()
//	st.SetGlobal("flags", tv)
func (st *State) NewBoolArrayTable(vals []bool) Value {
	inner := make([]value.Value, len(vals))
	for i, b := range vals {
		inner[i] = value.BoolValue(b)
	}
	ref := st.core.NewArrayTableFromVals(inner)
	idx := st.core.PinRef(ref)
	return Value{kind: kTable, fnState: st, pinIdx: idx}
}

// NewStringArrayTable 从 []string 一次性构建 Lua array table(issue #13)。
//
// 形态同 NewFloatArrayTable;每个 string 元素经 InternForEmbed 装入 VM
// 内部字符串 intern 表(相同字面量自动去重),GCRef 写进 array 段。脚本
// 读出与 wangshu.String 完全等价。
//
//	tv := st.NewStringArrayTable([]string{"alice", "bob", "carol"})
//	defer tv.Release()
//	st.SetGlobal("names", tv)
func (st *State) NewStringArrayTable(vals []string) Value {
	inner := make([]value.Value, len(vals))
	for i, s := range vals {
		ref := st.core.InternForEmbed([]byte(s))
		inner[i] = value.MakeGC(value.TagString, ref)
	}
	ref := st.core.NewArrayTableFromVals(inner)
	idx := st.core.PinRef(ref)
	return Value{kind: kTable, fnState: st, pinIdx: idx}
}

// Preallocate 预分配 table 的 array 段到 n 槽(issue #10 方向 2)。
//
// 典型用法:NewTable + Preallocate(N) + SetIndex(1..N) 绕过反复 rehash 风暴。
// 仅扩不缩(n ≤ 当前 asize → no-op);原 array 段数据保留。原 hash 段不动。
//
//	tv := st.NewTable()
//	tv.AsTable().Preallocate(1000)
//	for i := 1; i <= 1000; i++ {
//	    tv.AsTable().SetIndex(i, wangshu.Number(float64(i)))  // 全部 O(1) 落 array
//	}
//
// 已知大小直接走 NewArrayTable 更简(无需逐项 SetIndex);Preallocate 适合「分次
// 填充但已知最终大小」的场景。
func (t *Table) Preallocate(n uint32) error {
	if t.st == nil {
		return fmt.Errorf("wangshu: Table.Preallocate: table has been released")
	}
	ref := t.ref()
	if ref.IsNull() {
		return fmt.Errorf("wangshu: Table.Preallocate: table has been released")
	}
	t.st.core.PreallocateArray(ref, n)
	return nil
}

// ref 取出 pin 表中的 GCRef;调用方保证 t.st 非 nil。
func (t *Table) ref() arena.GCRef { return t.st.core.PinnedRefAt(t.pinIdx) }

// Set 写 raw 键值(不触发元方法)。key 必须可哈希(Nil/NaN key 行为同
// Lua 5.1.5——nil 报错,NaN 同——见 internal rawSet);val 任意公共
// kind(标量 / function / table)。跨 State 的 function/table 在
// toInner 处被映射为 Nil 兜底。
func (t *Table) Set(key, val Value) error {
	if t.st == nil {
		return fmt.Errorf("wangshu: Table.Set: table has been released")
	}
	ref := t.ref()
	if ref.IsNull() {
		return fmt.Errorf("wangshu: Table.Set: table has been released")
	}
	ik := key.toInner(t.st)
	iv := val.toInner(t.st)
	if e := t.st.core.RawSet(ref, ik, iv); e != nil {
		return fmt.Errorf("wangshu: Table.Set: %s", e.Msg)
	}
	return nil
}

// SetIndex 写 1-based 整数键(Lua 习惯,等价 t[i] = val)。
func (t *Table) SetIndex(i int, val Value) error {
	return t.Set(Number(float64(i)), val)
}

// Get 读 raw 键(不触发元方法);缺失键返回 Nil。复合值(table/function)
// 走 fromInnerWithPin 自动登记为新 pin 槽——使用者需对返回的 Value 配
// 套 Release()。
func (t *Table) Get(key Value) Value {
	if t.st == nil {
		return Nil()
	}
	ref := t.ref()
	if ref.IsNull() {
		return Nil()
	}
	ik := key.toInner(t.st)
	iv, _ := t.st.core.RawGet(ref, ik)
	return fromInnerWithPin(t.st, iv)
}

// GetIndex 读 1-based 整数键。
func (t *Table) GetIndex(i int) Value {
	return t.Get(Number(float64(i)))
}

// Len 返回 #t 语义(数组段二分边界,01 §5.2 / table.go rawBorder)。
// 与 Lua 表的 `#t` 运算符同源、与 stdlib table.getn 同源。
func (t *Table) Len() int {
	if t.st == nil {
		return 0
	}
	ref := t.ref()
	if ref.IsNull() {
		return 0
	}
	return int(t.st.core.RawBorder(ref))
}

// ForEach 遍历表的所有键值对(raw 迭代,不触发元方法;与 stdlib
// next/pairs 同源)。fn 返 false 提前终止遍历,返 true 继续。
//
// 迭代序:先数组段(索引序),后哈希段(槽序)——同一表同一形状下序
// 稳定(12 pairs 序口径)。fn 在 ForEach 调用栈内同步执行;并发上同
// State 单 goroutine(11 §8)。
//
// key/val 走 fromInnerWithPin 自动登记 pin 槽——若 fn 收到的 key/val
// 是复合值(table/function)且需在 fn 外保留,调用方负责 Release()。
// fn 不在外保留(典型 ForEach 用法:遍历到桥到 Go 端数据结构)时,
// 可在 fn 末尾顺手 Release 复合 val 防 pin 槽累积。
//
// 错误:表已 Release / internal RawNext 错(罕见:迭代中表结构被改),
// 返回 Go error;否则返 nil。
func (t *Table) ForEach(fn func(key, val Value) bool) error {
	if t.st == nil {
		return fmt.Errorf("wangshu: Table.ForEach: table has been released")
	}
	ref := t.ref()
	if ref.IsNull() {
		return fmt.Errorf("wangshu: Table.ForEach: table has been released")
	}
	key := value.Nil
	for {
		nextKey, nextVal, ok, e := t.st.core.RawNext(ref, key)
		if e != nil {
			return fmt.Errorf("wangshu: Table.ForEach: %s", e.Msg)
		}
		if !ok {
			return nil
		}
		pubKey := fromInnerWithPin(t.st, nextKey)
		pubVal := fromInnerWithPin(t.st, nextVal)
		if !fn(pubKey, pubVal) {
			return nil
		}
		key = nextKey
	}
}
