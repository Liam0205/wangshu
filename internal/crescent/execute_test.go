// End-to-end interpreter tests — three tiers: arithmetic / loops / calls (05 M9 acceptance criteria).
package crescent

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/frontend/compile"
	"github.com/Liam0205/wangshu/internal/frontend/lex"
	"github.com/Liam0205/wangshu/internal/frontend/parse"
	"github.com/Liam0205/wangshu/internal/value"
)

// runLua compiles src, loads it, executes the main chunk and returns the result.
func runLua(t *testing.T, src string) *State {
	t.Helper()
	lx := lex.New([]byte(src), "test")
	block, err := parse.Parse(lx, "test")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	mainID, protos, err := compile.Compile(block, "test")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := New()
	cl := st.LoadProgram(mainID, protos)
	if _, err := st.Call(cl, nil, 0); err != nil {
		t.Fatalf("call: %v", err)
	}
	return st
}

// TestExec_LocalArith — simple arithmetic + locals: verifies NaN-box direct computation and R(A) writes.
func TestExec_LocalArith(t *testing.T) {
	src := `
local function add(a, b) return a + b end
result = add(3, 4)
`
	st := runLua(t, src)
	v, _ := st.tableGet(st.globals, st.makeStringValue("result"))
	if !value.IsNumber(v) || value.AsNumber(v) != 7 {
		t.Errorf("result = %v, want number 7", debugVal(st, v))
	}
}

// TestExec_NumericForLoop — 02 §8 summation function sum(n) = sum_{i=1}^{n} i*i, verifies FORPREP/FORLOOP.
func TestExec_NumericForLoop(t *testing.T) {
	src := `
local function f(n)
  local s = 0
  for i=1,n do s = s + i*i end
  return s
end
result = f(10)
`
	st := runLua(t, src)
	v, _ := st.tableGet(st.globals, st.makeStringValue("result"))
	want := 0.0
	for i := 1; i <= 10; i++ {
		want += float64(i * i)
	}
	if !value.IsNumber(v) || value.AsNumber(v) != want {
		t.Errorf("result = %v, want %v", debugVal(st, v), want)
	}
}

// TestExec_RecursiveCall — recursive fib(10), verifies reentry does not blow the Go stack and multi-level RETURN.
func TestExec_RecursiveCall(t *testing.T) {
	src := `
local function fib(n)
  if n < 2 then return n end
  return fib(n-1) + fib(n-2)
end
result = fib(10)
`
	st := runLua(t, src)
	v, _ := st.tableGet(st.globals, st.makeStringValue("result"))
	if !value.IsNumber(v) || value.AsNumber(v) != 55 {
		t.Errorf("fib(10) = %v, want 55", debugVal(st, v))
	}
}

// TestExec_TailCall — 1e3 tail-recursive calls without blowing the stack (verifies TAILCALL frame reuse).
func TestExec_TailCall(t *testing.T) {
	src := `
local function loop(n, acc)
  if n == 0 then return acc end
  return loop(n-1, acc+1)
end
result = loop(1000, 0)
`
	st := runLua(t, src)
	v, _ := st.tableGet(st.globals, st.makeStringValue("result"))
	if !value.IsNumber(v) || value.AsNumber(v) != 1000 {
		t.Errorf("loop(1000,0) = %v, want 1000", debugVal(st, v))
	}
}

// TestExec_WhileLoop — verifies LT + JMP back-edge.
func TestExec_WhileLoop(t *testing.T) {
	src := `
local function count(n)
  local i = 0
  while i < n do i = i + 1 end
  return i
end
result = count(100)
`
	st := runLua(t, src)
	v, _ := st.tableGet(st.globals, st.makeStringValue("result"))
	if !value.IsNumber(v) || value.AsNumber(v) != 100 {
		t.Errorf("count(100) = %v, want 100", debugVal(st, v))
	}
}

// TestExec_ClosureUpvalue — verifies CLOSURE + trailing pseudo-instructions + GETUPVAL/SETUPVAL.
func TestExec_ClosureUpvalue(t *testing.T) {
	src := `
local function make()
  local x = 10
  local function inc() x = x + 1; return x end
  return inc()
end
result = make()
`
	st := runLua(t, src)
	v, _ := st.tableGet(st.globals, st.makeStringValue("result"))
	if !value.IsNumber(v) || value.AsNumber(v) != 11 {
		t.Errorf("make() = %v, want 11", debugVal(st, v))
	}
}

// TestExec_IfElse — multi-branch jumps of if/elseif/else.
func TestExec_IfElse(t *testing.T) {
	src := `
local function pick(a, b)
  if a < b then return -1 end
  if a > b then return 1 end
  return 0
end
r1 = pick(1, 2)
r2 = pick(2, 1)
r3 = pick(3, 3)
`
	st := runLua(t, src)
	for k, want := range map[string]float64{"r1": -1, "r2": 1, "r3": 0} {
		v, _ := st.tableGet(st.globals, st.makeStringValue(k))
		if !value.IsNumber(v) || value.AsNumber(v) != want {
			t.Errorf("%s = %v, want %v", k, debugVal(st, v), want)
		}
	}
}

// makeStringValue interns a string literal and returns the corresponding Value (convenient for tests looking up globals).
func (st *State) makeStringValue(s string) value.Value {
	ref := st.gc.Intern([]byte(s))
	return value.MakeGC(value.TagString, ref)
}

// debugVal returns a human-readable string (printed on test failure).
func debugVal(st *State, v value.Value) string {
	if value.IsNumber(v) {
		return formatLuaNumber(value.AsNumber(v))
	}
	switch value.Tag(v) {
	case value.TagNil:
		return "nil"
	case value.TagString:
		return "\"" + string(toStringDebug(st, v)) + "\""
	}
	return typeName(v)
}

func toStringDebug(st *State, v value.Value) []byte {
	if value.Tag(v) == value.TagString {
		b, _ := st.toStringBytes(v)
		return b
	}
	return nil
}
