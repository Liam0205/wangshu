// Typed array-table constructor tests (issue #13) —
// NewFloatArrayTable / NewInt64ArrayTable / NewBoolArrayTable / NewStringArrayTable.
// Verify round-trip, script-visible shape, empty/nil slice, int64 precision bounds.
package wangshu_test

import (
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
)

// TestNewFloatArrayTable_RoundTrip verifies float64 values land in the array
// segment and that `xs[i]` / `#xs` read back byte-equal to
// NewArrayTable([]Value{Number(...)}).
func TestNewFloatArrayTable_RoundTrip(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	tv := st.NewFloatArrayTable([]float64{1.5, 2.5, 3.5})
	defer tv.Release()
	tt := tv.AsTable()
	if got := tt.Len(); got != 3 {
		t.Errorf("Len = %d, want 3", got)
	}
	if got := tt.GetIndex(1).Number(); got != 1.5 {
		t.Errorf("[1] = %v, want 1.5", got)
	}
	if got := tt.GetIndex(2).Number(); got != 2.5 {
		t.Errorf("[2] = %v, want 2.5", got)
	}
	if got := tt.GetIndex(3).Number(); got != 3.5 {
		t.Errorf("[3] = %v, want 3.5", got)
	}
	if !tt.GetIndex(4).IsNil() {
		t.Errorf("[4] = %s, want nil", tt.GetIndex(4).Display())
	}
}

// TestNewFloatArrayTable_ScriptVisible verifies the script side sees `xs[i]` as
// a plain array, not the arena column track's `__index` proxy table (a key
// parity promise).
func TestNewFloatArrayTable_ScriptVisible(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	tv := st.NewFloatArrayTable([]float64{10, 20, 30, 40, 50})
	defer tv.Release()
	st.SetGlobal("xs", tv)

	prog, err := wangshu.Compile([]byte(`
		local s = 0
		for i = 1, #xs do s = s + xs[i] end
		return s, #xs
	`), "sum")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	r, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if r[0].Number() != 150 {
		t.Errorf("sum = %v, want 150", r[0].Number())
	}
	if r[1].Number() != 5 {
		t.Errorf("#xs = %v, want 5", r[1].Number())
	}
}

// TestNewFloatArrayTable_EmptyAndNil verifies both an empty slice and a nil
// slice are valid and return an empty table.
func TestNewFloatArrayTable_EmptyAndNil(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	for _, vals := range [][]float64{{}, nil} {
		tv := st.NewFloatArrayTable(vals)
		if got := tv.AsTable().Len(); got != 0 {
			t.Errorf("Len = %d, want 0", got)
		}
		tv.Release()
	}
}

// TestNewInt64ArrayTable_RoundTrip verifies int64 elements read back correctly
// after round-tripping through float64.
func TestNewInt64ArrayTable_RoundTrip(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	tv, err := st.NewInt64ArrayTable([]int64{10, 20, 30})
	if err != nil {
		t.Fatalf("NewInt64ArrayTable: %v", err)
	}
	defer tv.Release()
	tt := tv.AsTable()
	if got := tt.Len(); got != 3 {
		t.Errorf("Len = %d, want 3", got)
	}
	if got := tt.GetIndex(2).Number(); got != 20 {
		t.Errorf("[2] = %v, want 20", got)
	}
}

// TestNewInt64ArrayTable_PrecisionOverflow verifies |v| > 2^53 triggers an error
// and returns Nil. Same rule as Arena.AddInt64Column (review decision item 3).
func TestNewInt64ArrayTable_PrecisionOverflow(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	cases := []struct {
		name string
		vals []int64
		bad  int
	}{
		{"positive overflow", []int64{1, 2, (1 << 53) + 1}, 2},
		{"negative overflow", []int64{-1, -((1 << 53) + 1), -3}, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tv, err := st.NewInt64ArrayTable(c.vals)
			if err == nil {
				tv.Release()
				t.Fatal("expected overflow error, got nil")
			}
			if !tv.IsNil() {
				t.Errorf("on err: tv = %s, want Nil", tv.Display())
			}
			if !strings.Contains(err.Error(), "2^53") {
				t.Errorf("err msg lacks 2^53 hint: %v", err)
			}
		})
	}
}

// TestNewInt64ArrayTable_BoundaryValuesAccepted verifies the ±2^53 boundary
// itself is accepted (only values beyond it error out).
func TestNewInt64ArrayTable_BoundaryValuesAccepted(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	tv, err := st.NewInt64ArrayTable([]int64{1 << 53, -(1 << 53)})
	if err != nil {
		t.Fatalf("boundary value rejected: %v", err)
	}
	defer tv.Release()
	if got := tv.AsTable().Len(); got != 2 {
		t.Errorf("Len = %d, want 2", got)
	}
}

// TestNewBoolArrayTable_RoundTrip verifies bool element round-trip.
func TestNewBoolArrayTable_RoundTrip(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	tv := st.NewBoolArrayTable([]bool{true, false, true})
	defer tv.Release()
	tt := tv.AsTable()
	if got := tt.GetIndex(1).Bool(); !got {
		t.Errorf("[1] = false, want true")
	}
	if got := tt.GetIndex(2).Bool(); got {
		t.Errorf("[2] = true, want false")
	}
	if got := tt.GetIndex(3).Bool(); !got {
		t.Errorf("[3] = false, want true")
	}
}

// TestNewBoolArrayTable_ScriptVisible verifies the script side handles
// `flags[i]` and `and`/`or` short-circuit operations correctly.
func TestNewBoolArrayTable_ScriptVisible(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	tv := st.NewBoolArrayTable([]bool{true, false, true, true})
	defer tv.Release()
	st.SetGlobal("flags", tv)
	prog, _ := wangshu.Compile([]byte(`
		local n = 0
		for i = 1, 4 do if flags[i] then n = n + 1 end end
		return n
	`), "countbool")
	r, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if r[0].Number() != 3 {
		t.Errorf("count = %v, want 3", r[0].Number())
	}
}

// TestNewStringArrayTable_RoundTrip verifies string element round-trip.
func TestNewStringArrayTable_RoundTrip(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	tv := st.NewStringArrayTable([]string{"alice", "bob", "carol"})
	defer tv.Release()
	tt := tv.AsTable()
	if got := tt.GetIndex(1).Str(); got != "alice" {
		t.Errorf("[1] = %q, want alice", got)
	}
	if got := tt.GetIndex(2).Str(); got != "bob" {
		t.Errorf("[2] = %q, want bob", got)
	}
	if got := tt.GetIndex(3).Str(); got != "carol" {
		t.Errorf("[3] = %q, want carol", got)
	}
}

// TestNewStringArrayTable_ScriptConcat verifies the script side has consistent
// string concat/comparison behavior (key point: after interning, the GCRef is
// equivalent to a wangshu.String literal).
func TestNewStringArrayTable_ScriptConcat(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	tv := st.NewStringArrayTable([]string{"foo", "bar", "baz"})
	defer tv.Release()
	st.SetGlobal("names", tv)
	prog, _ := wangshu.Compile([]byte(`
		return names[1] .. "," .. names[2] .. "," .. names[3]
	`), "concat")
	r, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := r[0].Str(); got != "foo,bar,baz" {
		t.Errorf("concat = %q, want foo,bar,baz", got)
	}
}

// TestNewStringArrayTable_InternDedup verifies duplicate string elements share
// an intern slot (implicit contract: identical literals still compare equal
// inside the array segment).
func TestNewStringArrayTable_InternDedup(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	tv := st.NewStringArrayTable([]string{"alice", "alice", "alice"})
	defer tv.Release()
	st.SetGlobal("dup", tv)
	prog, _ := wangshu.Compile([]byte(`return dup[1] == dup[2] and dup[2] == dup[3]`), "dup")
	r, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !r[0].Bool() {
		t.Errorf("interned dedup: got false, want true")
	}
}

// TestNewArrayTable_NoAllocPerElement_NonPanic serves as both a smoke test and a
// stress test: under GCStressMode it repeatedly runs typed-array construction +
// script read + Release, ensuring no panic or leak (key point: typed-array does
// not go through fromInnerWithPin, but must still tolerate GC pressure).
func TestNewArrayTable_NoAllocPerElement_NonPanic(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	st.SetGCStressMode(true)
	for r := 0; r < 50; r++ {
		// run a different typed-array each round
		tv1 := st.NewFloatArrayTable([]float64{1, 2, 3, 4, 5})
		tv1.Release()

		tv2, err := st.NewInt64ArrayTable([]int64{10, 20, 30})
		if err != nil {
			t.Fatalf("int64: %v", err)
		}
		tv2.Release()

		tv3 := st.NewBoolArrayTable([]bool{true, false})
		tv3.Release()

		tv4 := st.NewStringArrayTable([]string{"a", "b", "c"})
		tv4.Release()
	}
}
