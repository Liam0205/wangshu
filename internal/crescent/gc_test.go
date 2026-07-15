// GC pressure test — verify that under frequent allocation the main loop does
// not produce byte-different results due to the GC wrongly collecting live
// objects (M10 acceptance).
package crescent

import (
	"strings"
	"testing"

	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/frontend/compile"
	"github.com/Liam0205/wangshu/internal/frontend/lex"
	"github.com/Liam0205/wangshu/internal/frontend/parse"
	"github.com/Liam0205/wangshu/internal/value"
)

// TestGC_StressClosure checks that under frequent closure allocation a closure
// can still correctly read its captured locals.
func TestGC_StressClosure(t *testing.T) {
	src := `
local function makeAdder(x)
  return function(y) return x + y end
end
local a = makeAdder(10)
local sum = 0
for i = 1, 200 do
  -- 每次创建新闭包,触发若干次 NEWTABLE 那种分配压力的代用品
  local f = makeAdder(i)
  sum = sum + f(1)
end
result = sum + a(5)
`
	st := runLua(t, src)
	v, _ := st.tableGet(st.globals, st.makeStringValue("result"))
	// sum_{i=1..200}(i+1) + (10+5) = (sum 1..200 + 200) + 15 = 20100 + 200 + 15 = 20315
	if !value.IsNumber(v) || value.AsNumber(v) != 20315 {
		t.Errorf("result = %v, want 20315", debugVal(st, v))
	}
}

// TestGC_StressTable checks that table data stays correct under frequent table creation.
func TestGC_StressTable(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("local total = 0\n")
	for i := 0; i < 50; i++ {
		sb.WriteString("local t")
		sb.WriteString("a = { 1, 2, 3 }\n")
		sb.WriteString("total = total + ta[1] + ta[2] + ta[3]\n")
	}
	sb.WriteString("result = total\n")
	st := runLua(t, sb.String())
	v, _ := st.tableGet(st.globals, st.makeStringValue("result"))
	want := float64(50 * (1 + 2 + 3))
	if !value.IsNumber(v) || value.AsNumber(v) != want {
		t.Errorf("result = %v, want %v", debugVal(st, v), want)
	}
}

// TestGC_StressConcat verifies no wrong collection when CONCAT repeatedly interns strings.
func TestGC_StressConcat(t *testing.T) {
	src := `
local s = "x"
for i = 1, 100 do
  s = s .. "y"
end
result = s
`
	st := runLua(t, src)
	v, _ := st.tableGet(st.globals, st.makeStringValue("result"))
	if value.Tag(v) != value.TagString {
		t.Fatalf("result is not string: %v", debugVal(st, v))
	}
	got, _ := st.toStringBytes(v)
	want := "x" + strings.Repeat("y", 100)
	if string(got) != want {
		t.Errorf("result len=%d, want len=%d", len(got), len(want))
	}
}

// TestGC_DirectCollect explicitly triggers one Collect and verifies the live stack is not wrongly collected.
func TestGC_DirectCollect(t *testing.T) {
	src := `
local t = { 100, 200, 300 }
result = t[1] + t[2] + t[3]
`
	st := runLua(t, src)
	// trigger one Collect
	st.gc.Collect()
	v, _ := st.tableGet(st.globals, st.makeStringValue("result"))
	if !value.IsNumber(v) || value.AsNumber(v) != 600 {
		t.Errorf("result = %v, want 600", debugVal(st, v))
	}
}

// TestGC_CollectMidExecution forces a Collect during script execution (via a
// host fn) and verifies that a live closure/upvalue remains usable after being
// marked (the scanClosure path). This is the primary line-of-defense test for
// "the GC does not wrongly collect while the interpreter is running" (06 §6 / 05 §5.3).
func TestGC_CollectMidExecution(t *testing.T) {
	src := `
local function makeAdder(x)
  return function(y) return x + y end
end
local add10 = makeAdder(10)
collectgarbage_test()
result = add10(5)
`
	lxSrc := []byte(src)
	st := New()
	// register a test host fn that forces a full GC during execution
	id := st.RegisterHostFn(func(s *State, _ []value.Value) ([]value.Value, *LuaError) {
		s.gc.Collect()
		return nil, nil
	})
	cl := st.MakeHostClosure(id)
	st.SetGlobal("collectgarbage_test", value.MakeGC(value.TagFunction, cl))

	prog := mustCompile(t, lxSrc)
	mainCl := st.LoadProgram(prog.mainID, prog.protos)
	if _, err := st.Call(mainCl, nil, 0); err != nil {
		t.Fatalf("call: %v", err)
	}
	v, _ := st.tableGet(st.globals, st.makeStringValue("result"))
	if !value.IsNumber(v) || value.AsNumber(v) != 15 {
		t.Errorf("result = %v, want 15 (closed upvalue x=10 must survive GC)", debugVal(st, v))
	}
}

// TestGC_CollectWithOpenUpvalue triggers a GC while an upvalue is still open (pointing at a live stack slot).
func TestGC_CollectWithOpenUpvalue(t *testing.T) {
	src := `
local n = 0
local function inc() n = n + 1; return n end
inc()
collectgarbage_test()
inc()
result = inc()
`
	st := New()
	id := st.RegisterHostFn(func(s *State, _ []value.Value) ([]value.Value, *LuaError) {
		s.gc.Collect()
		return nil, nil
	})
	cl := st.MakeHostClosure(id)
	st.SetGlobal("collectgarbage_test", value.MakeGC(value.TagFunction, cl))

	prog := mustCompile(t, []byte(src))
	mainCl := st.LoadProgram(prog.mainID, prog.protos)
	if _, err := st.Call(mainCl, nil, 0); err != nil {
		t.Fatalf("call: %v", err)
	}
	v, _ := st.tableGet(st.globals, st.makeStringValue("result"))
	if !value.IsNumber(v) || value.AsNumber(v) != 3 {
		t.Errorf("result = %v, want 3 (open upvalue n must survive GC)", debugVal(st, v))
	}
}

type compiled struct {
	mainID uint32
	protos []*bytecode.Proto
}

func mustCompile(t *testing.T, src []byte) compiled {
	t.Helper()
	lx := lex.New(src, "gctest")
	block, err := parse.Parse(lx, "gctest")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	mainID, protos, err := compile.Compile(block, "gctest")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return compiled{mainID: mainID, protos: protos}
}
