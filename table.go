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
