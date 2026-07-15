// Public-facing Table API: lets the host Go side construct, read, write, and
// pass Lua tables (issue #2: pineapple common-mode columnar kernel feeding
// N-item whole-column data into the VM).
// Design: 11 §4.5 sum-type Value adds a kTable kind; like kFunction it registers
// in the State pin table as a GC root; Set/Get/SetIndex/GetIndex/Len forward to
// internal RawGet/RawSet/RawBorder.
package wangshu

import (
	"fmt"

	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/value"
)

// Table is the public-facing opaque handle to a Lua table, created via
// State.NewTable or extracted from a GetGlobal / Call return value
// (Value.AsTable()). Its lifetime is controlled by the Release() of the owning
// Value (same pin-table mechanism as kFunction).
//
// Concurrency: same as State, single goroutine (11 §8).
//
// Performance tier: Set/Get/SetIndex/GetIndex/ForEach each cross the Go ↔ VM
// boundary once (per-item form, design-premises premise one). Suited to
// "construct once and feed" and "read out once from script return" setup/teardown
// patterns; **not suited** to repeatedly calling SetIndex/GetIndex on large
// batches of data inside a Go-side loop (that is the arena column-track scenario,
// see [[embedding-contract]] arena ABI section, zero-copy read). Len is an
// O(log N) binary search over the array segment and may be used at low frequency.
type Table struct {
	st     *State
	pinIdx uint32
}

// AsTable extracts an operable handle from a table-kind Value; returns nil for a
// non-table-kind Value.
func (v Value) AsTable() *Table {
	if !v.IsTable() {
		return nil
	}
	return &Table{st: v.fnState, pinIdx: v.pinIdx}
}

// NewTable creates an empty Lua table and returns it as a Value (table-kind,
// registered as a GC root via the State pin table). The returned value must be
// Release()d when no longer used, otherwise pin slots accumulate.
func (st *State) NewTable() Value {
	ref := st.core.NewLibTable(0)
	idx := st.core.PinRef(ref)
	return Value{kind: kTable, fnState: st, pinIdx: idx}
}

// NewArrayTable builds a Lua array table from a Go slice in one shot (issue #10
// direction 2).
//
// Internally allocates an array segment of len(vals), writes vals, with no rehash
// storm. **Far faster than** NewTable + repeated SetIndex (rehash storm → O(N²);
// this method is O(N)). Returns a table-kind Value, registered as a GC root via
// the pin table.
//
// Typical use: the host lifts []float64 / []string column data into a Lua table
// to feed a script.
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

// NewFloatArrayTable builds a Lua array table from a []float64 in one shot
// (issue #13).
//
// Difference from NewArrayTable([]Value): skips host-side []Value materialization
// and the per-element toInner type dispatch, NaN-boxing float64 directly into the
// arena array segment—the fast path for pineapple-class boundary-dominated
// embedders bulk-loading columns in common mode, avoiding the three-layer detour
// of `[]any → []Value → []value.Value`.
//
// The script side sees a plain array table: `xs[i]` (1-based), `#xs`, and
// `for k,v in pairs(xs)` all work normally. It is **not** an arena column-track
// `__index` proxy table—zero script changes, and cross-engine `lua_script`
// byte-equality is unaffected.
//
// Performance tier: same O(N) bulk write as `NewArrayTable` (via internal
// NewArrayTableFromVals, skipping the rehash storm); compared to NewArrayTable it
// saves one intermediate []Value slice plus N type dispatches.
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

// NewInt64ArrayTable builds a Lua array table from a []int64 in one shot
// (issue #13).
//
// Same form as NewFloatArrayTable; int64 elements are converted to float64 then
// NaN-boxed. Inherits the |v| > 2^53 error rule of `Arena.AddInt64Column`
// (review decision item 3, see the AddInt64Column section of `arena_abi.go`):
// elements exceeding float64 mantissa precision return an error before
// materialization and pinning, avoiding silent precision loss.
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

// NewBoolArrayTable builds a Lua array table from a []bool in one shot
// (issue #13).
//
// Same form as NewFloatArrayTable; bool elements are NaN-boxed directly, with no
// boxing overhead.
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

// NewStringArrayTable builds a Lua array table from a []string in one shot
// (issue #13).
//
// Same form as NewFloatArrayTable; each string element goes through InternForEmbed
// into the VM's internal string intern table (identical literals are
// automatically deduplicated), and the GCRef is written into the array segment.
// Reading them back from the script is fully equivalent to wangshu.String.
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

// Preallocate pre-allocates the table's array segment to n slots (issue #10
// direction 2).
//
// Typical use: NewTable + Preallocate(N) + SetIndex(1..N) to bypass repeated
// rehash storms. Grow-only (n ≤ current asize → no-op); existing array segment
// data is preserved. The hash segment is untouched.
//
//	tv := st.NewTable()
//	tv.AsTable().Preallocate(1000)
//	for i := 1; i <= 1000; i++ {
//	    tv.AsTable().SetIndex(i, wangshu.Number(float64(i)))  // all O(1) into the array
//	}
//
// When the size is known upfront, NewArrayTable is simpler (no per-item
// SetIndex); Preallocate suits the "fill in stages but final size known" case.
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

// ref extracts the GCRef from the pin table; the caller guarantees t.st is non-nil.
func (t *Table) ref() arena.GCRef { return t.st.core.PinnedRefAt(t.pinIdx) }

// Set writes a raw key-value pair (does not trigger metamethods). The key must be
// hashable (Nil/NaN key behavior matches Lua 5.1.5—nil errors, NaN likewise—see
// internal rawSet); val may be any public kind (scalar / function / table).
// A cross-State function/table is mapped to Nil as a fallback at toInner.
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

// SetIndex writes a 1-based integer key (Lua convention, equivalent to
// t[i] = val).
func (t *Table) SetIndex(i int, val Value) error {
	return t.Set(Number(float64(i)), val)
}

// Get reads a raw key (does not trigger metamethods); a missing key returns Nil.
// Composite values (table/function) go through fromInnerWithPin and are
// automatically registered as a new pin slot—the caller must pair the returned
// Value with a Release().
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

// GetIndex reads a 1-based integer key.
func (t *Table) GetIndex(i int) Value {
	return t.Get(Number(float64(i)))
}

// Len returns the #t semantics (array-segment binary-search border, 01 §5.2 /
// table.go rawBorder). Same source as the Lua `#t` operator and same source as
// the stdlib table.getn.
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

// ForEach iterates over all key-value pairs of the table (raw iteration, does not
// trigger metamethods; same source as stdlib next/pairs). fn returning false
// terminates the iteration early, returning true continues.
//
// Iteration order: array segment first (index order), then hash segment (slot
// order)—stable for the same table under the same shape (12 pairs order
// convention). fn runs synchronously within the ForEach call stack; concurrency-
// wise the same State is single goroutine (11 §8).
//
// key/val go through fromInnerWithPin and are automatically registered in a pin
// slot—if the key/val fn receives is a composite value (table/function) and needs
// to be retained outside fn, the caller is responsible for Release(). When fn does
// not retain it outside (the typical ForEach use: iterate and bridge to a Go-side
// data structure), you may Release the composite val at the end of fn to prevent
// pin-slot accumulation.
//
// Errors: table already Released / internal RawNext error (rare: table structure
// mutated mid-iteration) returns a Go error; otherwise returns nil.
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
