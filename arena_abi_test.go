// Arena ABI end-to-end tests (11 §3-§5).
package wangshu_test

import (
	"testing"

	"github.com/Liam0205/wangshu"
)

func TestArena_ColumnKernelShape(t *testing.T) {
	// Column kernel shape (design-premises premise 1): the loop lives in Lua, one call enters the VM once
	prog, err := wangshu.Compile([]byte(`
local price = arena.price
local qty = arena.qty
local total = 0
for i = 1, arena.rows do
  total = total + price[i] * qty[i]
end
return total
`), "kernel")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	ar := wangshu.NewArena(4)
	if err := ar.AddFloatColumn("price", []float64{1.5, 2.0, 3.0, 4.5}, nil); err != nil {
		t.Fatalf("add price: %v", err)
	}
	if err := ar.AddInt64Column("qty", []int64{2, 3, 1, 2}, nil); err != nil {
		t.Fatalf("add qty: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	r, err := prog.Call(st, ar)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	want := 1.5*2 + 2.0*3 + 3.0*1 + 4.5*2
	if r[0].Number() != want {
		t.Errorf("total = %v, want %v", r[0].Display(), want)
	}
}

func TestArena_NullPresence(t *testing.T) {
	prog, err := wangshu.Compile([]byte(`
local v = arena.vals
local sum, nulls = 0, 0
for i = 1, arena.rows do
  if v[i] == nil then nulls = nulls + 1 else sum = sum + v[i] end
end
return sum, nulls
`), "nulls")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	ar := wangshu.NewArena(4)
	if err := ar.AddFloatColumn("vals", []float64{10, 0, 30, 0}, []bool{true, false, true, false}); err != nil {
		t.Fatalf("add: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	r, err := prog.Call(st, ar)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if r[0].Number() != 40 || r[1].Number() != 2 {
		t.Errorf("got sum=%v nulls=%v, want 40/2", r[0].Display(), r[1].Display())
	}
}

func TestArena_StringAndBoolColumns(t *testing.T) {
	prog, err := wangshu.Compile([]byte(`
local name = arena.name
local active = arena.active
local out = ""
for i = 1, arena.rows do
  if active[i] then out = out .. name[i] .. ";" end
end
return out
`), "strbool")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	ar := wangshu.NewArena(3)
	if err := ar.AddStringColumn("name", []string{"a", "b", "c"}, nil); err != nil {
		t.Fatalf("add name: %v", err)
	}
	if err := ar.AddBoolColumn("active", []bool{true, false, true}, nil); err != nil {
		t.Fatalf("add active: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	r, err := prog.Call(st, ar)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if r[0].Str() != "a;c;" {
		t.Errorf("got %q, want 'a;c;'", r[0].Str())
	}
}

func TestArena_ReadOnly(t *testing.T) {
	prog, err := wangshu.Compile([]byte(`
arena.vals[1] = 99
return 1
`), "ro")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	ar := wangshu.NewArena(1)
	_ = ar.AddFloatColumn("vals", []float64{1}, nil)
	st := wangshu.NewState(wangshu.Options{})
	_, err = prog.Call(st, ar)
	if err == nil {
		t.Fatalf("expected read-only error")
	}
}

func TestArena_Int64PrecisionGuard(t *testing.T) {
	prog, err := wangshu.Compile([]byte(`return arena.big[1]`), "prec")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	ar := wangshu.NewArena(1)
	_ = ar.AddInt64Column("big", []int64{1 << 60}, nil)
	st := wangshu.NewState(wangshu.Options{})
	_, err = prog.Call(st, ar)
	if err == nil {
		t.Fatalf("expected precision error for |v| > 2^53 (ColInt64 评审决策)")
	}
}

func TestArena_DuplicateColumnRejected(t *testing.T) {
	ar := wangshu.NewArena(1)
	if err := ar.AddFloatColumn("x", []float64{1}, nil); err != nil {
		t.Fatalf("first add: %v", err)
	}
	if err := ar.AddFloatColumn("x", []float64{2}, nil); err == nil {
		t.Fatalf("expected duplicate column error")
	}
}

func TestArena_LengthMismatchRejected(t *testing.T) {
	ar := wangshu.NewArena(3)
	if err := ar.AddFloatColumn("x", []float64{1, 2}, nil); err == nil {
		t.Fatalf("expected length mismatch error")
	}
}

func TestArena_RowsAccessor(t *testing.T) {
	ar := wangshu.NewArena(7)
	if ar.Rows() != 7 {
		t.Errorf("Rows() = %d, want 7", ar.Rows())
	}
}

func TestArena_StringDedupShared(t *testing.T) {
	// Identical strings deduplicated (shared byte pool): reads on the script side are still correct
	prog, err := wangshu.Compile([]byte(`
local n = arena.name
return n[1] .. n[2] .. n[3]`), "dedup")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	ar := wangshu.NewArena(3)
	_ = ar.AddStringColumn("name", []string{"dup", "dup", "uniq"}, nil)
	st := wangshu.NewState(wangshu.Options{})
	r, err := prog.Call(st, ar)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if r[0].Str() != "dupdupuniq" {
		t.Errorf("got %q", r[0].Str())
	}
}

func TestArena_OutOfRangeReadsNil(t *testing.T) {
	prog, err := wangshu.Compile([]byte(`
return tostring(arena.v[0]) .. tostring(arena.v[99]) .. tostring(arena.v["x"])`), "oob")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	ar := wangshu.NewArena(1)
	_ = ar.AddFloatColumn("v", []float64{1}, nil)
	st := wangshu.NewState(wangshu.Options{})
	r, err := prog.Call(st, ar)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if r[0].Str() != "nilnilnil" {
		t.Errorf("got %q, want 'nilnilnil'", r[0].Str())
	}
}
