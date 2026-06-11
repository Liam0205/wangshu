// Arena ABI — 宿主侧列数据容器(11 §3.5 P1 Go 承载)+ VM 零拷贝读视图(11 §4-§5)。
package wangshu

import (
	"fmt"
)

type colType uint8

const (
	colFloat64 colType = iota
	colInt64
	colBool
	colString
)

type column struct {
	tag      colType
	name     string
	presence []uint64 // nil = 全 present;否则 bit i = 第 i 行 present
	f64      []float64
	i64      []int64
	boolBits []uint64
	strSlots []strSlot
}

type strSlot struct{ off, len uint32 }

// Arena 是宿主侧的列数据容器(11 §3.5)。宿主用类型化方法构造列;
// VM 零拷贝读其元素(读一个元素就地 NaN-box,整列不复制)。
type Arena struct {
	nrows    uint32
	cols     []column
	names    map[string]int
	strBytes []byte // ColString 共享字节池(11 §3.3.4)
	strDedup map[string]strSlot
}

// NewArena 创建一个 nrows 行的列容器;所有列必须等长 = nrows(11 §3.1)。
func NewArena(nrows int) *Arena {
	return &Arena{
		nrows:    uint32(nrows),
		names:    map[string]int{},
		strDedup: map[string]strSlot{},
	}
}

// Rows 返回行数。
func (a *Arena) Rows() int { return int(a.nrows) }

func (a *Arena) checkLen(name string, n int) error {
	if uint32(n) != a.nrows {
		return fmt.Errorf("wangshu: column %q has %d values, want %d rows", name, n, a.nrows)
	}
	if _, dup := a.names[name]; dup {
		return fmt.Errorf("wangshu: duplicate column %q", name)
	}
	return nil
}

func packPresence(present []bool) []uint64 {
	if present == nil {
		return nil
	}
	out := make([]uint64, (len(present)+63)/64)
	for i, p := range present {
		if p {
			out[i/64] |= 1 << (i % 64)
		}
	}
	return out
}

// AddFloatColumn 加一列 float64(零拷贝引用 vals;present=nil 表示全 present)。
func (a *Arena) AddFloatColumn(name string, vals []float64, present []bool) error {
	if err := a.checkLen(name, len(vals)); err != nil {
		return err
	}
	a.names[name] = len(a.cols)
	a.cols = append(a.cols, column{tag: colFloat64, name: name, f64: vals, presence: packPresence(present)})
	return nil
}

// AddInt64Column 加一列 int64(读出转 double;|v| > 2^53 报错,评审决策第 3 项)。
func (a *Arena) AddInt64Column(name string, vals []int64, present []bool) error {
	if err := a.checkLen(name, len(vals)); err != nil {
		return err
	}
	a.names[name] = len(a.cols)
	a.cols = append(a.cols, column{tag: colInt64, name: name, i64: vals, presence: packPresence(present)})
	return nil
}

// AddBoolColumn 加一列 bool(u64 位打包,11 §3.3.3)。
func (a *Arena) AddBoolColumn(name string, vals []bool, present []bool) error {
	if err := a.checkLen(name, len(vals)); err != nil {
		return err
	}
	a.names[name] = len(a.cols)
	a.cols = append(a.cols, column{tag: colBool, name: name, boolBits: packPresence(vals), presence: packPresence(present)})
	return nil
}

// AddStringColumn 加一列 string(字节拷进共享池,相同串去重,11 §3.3.4 方案 α)。
func (a *Arena) AddStringColumn(name string, vals []string, present []bool) error {
	if err := a.checkLen(name, len(vals)); err != nil {
		return err
	}
	slots := make([]strSlot, len(vals))
	for i, s := range vals {
		if slot, ok := a.strDedup[s]; ok {
			slots[i] = slot
			continue
		}
		slot := strSlot{off: uint32(len(a.strBytes)), len: uint32(len(s))}
		a.strBytes = append(a.strBytes, s...)
		a.strDedup[s] = slot
		slots[i] = slot
	}
	a.names[name] = len(a.cols)
	a.cols = append(a.cols, column{tag: colString, name: name, strSlots: slots, presence: packPresence(present)})
	return nil
}

// present 判定第 row 行是否非 null。
func (c *column) present(row uint32) bool {
	if c.presence == nil {
		return true
	}
	return c.presence[row/64]&(1<<(row%64)) != 0
}
