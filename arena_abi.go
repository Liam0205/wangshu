// Arena ABI — host-side columnar data container (11 §3.5, P1 carried by Go) plus VM zero-copy read views (11 §4-§5).
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
	presence []uint64 // nil = all present; otherwise bit i = row i present
	f64      []float64
	i64      []int64
	boolBits []uint64
	strSlots []strSlot
}

type strSlot struct{ off, len uint32 }

// Arena is the host-side columnar data container (11 §3.5). The host builds
// columns with typed methods; the VM reads elements zero-copy (reading one
// element NaN-boxes it in place, the whole column is never copied).
type Arena struct {
	nrows    uint32
	cols     []column
	names    map[string]int
	strBytes []byte // ColString shared byte pool (11 §3.3.4)
	strDedup map[string]strSlot
}

// NewArena creates a columnar container with nrows rows; all columns must have equal length = nrows (11 §3.1).
func NewArena(nrows int) *Arena {
	return &Arena{
		nrows:    uint32(nrows),
		names:    map[string]int{},
		strDedup: map[string]strSlot{},
	}
}

// Rows returns the row count.
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

// AddFloatColumn adds a float64 column (zero-copy reference to vals; present=nil means all present).
func (a *Arena) AddFloatColumn(name string, vals []float64, present []bool) error {
	if err := a.checkLen(name, len(vals)); err != nil {
		return err
	}
	a.names[name] = len(a.cols)
	a.cols = append(a.cols, column{tag: colFloat64, name: name, f64: vals, presence: packPresence(present)})
	return nil
}

// AddInt64Column adds an int64 column (read out as double; |v| > 2^53 errors, per review decision item 3).
func (a *Arena) AddInt64Column(name string, vals []int64, present []bool) error {
	if err := a.checkLen(name, len(vals)); err != nil {
		return err
	}
	a.names[name] = len(a.cols)
	a.cols = append(a.cols, column{tag: colInt64, name: name, i64: vals, presence: packPresence(present)})
	return nil
}

// AddBoolColumn adds a bool column (bit-packed into u64, 11 §3.3.3).
func (a *Arena) AddBoolColumn(name string, vals []bool, present []bool) error {
	if err := a.checkLen(name, len(vals)); err != nil {
		return err
	}
	a.names[name] = len(a.cols)
	a.cols = append(a.cols, column{tag: colBool, name: name, boolBits: packPresence(vals), presence: packPresence(present)})
	return nil
}

// AddStringColumn adds a string column (bytes copied into the shared pool, identical strings deduped, 11 §3.3.4 scheme α).
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

// present reports whether row `row` is non-null.
func (c *column) present(row uint32) bool {
	if c.presence == nil {
		return true
	}
	return c.presence[row/64]&(1<<(row%64)) != 0
}
