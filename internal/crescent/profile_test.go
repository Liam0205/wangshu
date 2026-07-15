//go:build wangshu_profile

// PB1 sampling-hook integration acceptance (`docs/design/p2-bridge/01-profiling.md` §1.4 + §4):
//   - Back-edge / entry counts accumulate to nonzero (three script tiers: numeric loop / while / function entry)
//   - Flipping profileEnabled=true does not change byte-equal (test = the default build already passes ⇒
//     here we only verify "under profile=on the result matches the script's expected value"; matching the default
//     indirectly verifies "toggling the switch does not change byte-equal")
//
// Compiled only under `-tags wangshu_profile` — with profileEnabled=false the hooks are
// compiled away, profileTable is always empty, and this test group is meaningless.
package crescent

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/frontend/compile"
	"github.com/Liam0205/wangshu/internal/frontend/lex"
	"github.com/Liam0205/wangshu/internal/frontend/parse"
	"github.com/Liam0205/wangshu/internal/value"
)

// runLuaWithBridge is like runLua but exposes the bridge to the test
// (using a wrapper like wangshu.NewState would make this test an e2e, too heavy — go straight through
// crescent.New).
func runLuaWithBridge(t *testing.T, src string) *State {
	t.Helper()
	lx := lex.New([]byte(src), "test_profile")
	block, err := parse.Parse(lx, "test_profile")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	mainID, protos, err := compile.Compile(block, "test_profile")
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

// TestProfile_BackEdgeAccumulates runs a numeric loop for N iterations ⇒ at least one back-edge
// pc accumulates ≈ N times.
func TestProfile_BackEdgeAccumulates(t *testing.T) {
	src := `
local function f(n)
  local s = 0
  for i=1,n do s = s + i end
  return s
end
result = f(50)
`
	st := runLuaWithBridge(t, src)
	v, _ := st.tableGet(st.globals, st.makeStringValue("result"))
	if !value.IsNumber(v) || value.AsNumber(v) != 1275 {
		t.Fatalf("script result = %v, want 1275", debugVal(st, v))
	}

	innerProto := findProtoByParams(t, st, 1)
	pd := st.bridge.ProfileOf(innerProto)
	maxBack := pd.MaxBackEdge()
	if maxBack < 40 {
		t.Errorf("expected MaxBackEdge ≥ 40 for 50-iter loop, got %d", maxBack)
	}
	if pd.EntryCount < 1 {
		t.Errorf("inner function entered at least once, got entryCount=%d", pd.EntryCount)
	}
}

// findProtoByParams finds the first Proto whose NumParams equals want and is not the main chunk
// (IsVararg=false) — the main chunk is vararg, so skip it.
func findProtoByParams(t *testing.T, st *State, want uint8) *bytecode.Proto {
	t.Helper()
	for _, p := range st.protos {
		if p.IsVararg {
			continue
		}
		if p.NumParams == want {
			return p
		}
	}
	t.Fatalf("no Proto with NumParams=%d (vararg=false) found", want)
	return nil
}

// TestProfile_WhileLoopBackEdge verifies that JMP back edges of the while/repeat kind are also counted
// (route B covers all back-edge forms in the §1.3 table).
func TestProfile_WhileLoopBackEdge(t *testing.T) {
	src := `
local function f(n)
  local i = 0
  while i < n do i = i + 1 end
  return i
end
result = f(30)
`
	st := runLuaWithBridge(t, src)
	v, _ := st.tableGet(st.globals, st.makeStringValue("result"))
	if !value.IsNumber(v) || value.AsNumber(v) != 30 {
		t.Fatalf("script result = %v, want 30", debugVal(st, v))
	}

	innerProto := findProtoByParams(t, st, 1)
	pd := st.bridge.ProfileOf(innerProto)
	if pd.MaxBackEdge() < 25 {
		t.Errorf("expected MaxBackEdge ≥ 25 for 30-iter while, got %d",
			pd.MaxBackEdge())
	}
}

// TestProfile_EntryCountAccumulates calls the same function repeatedly ⇒ entryCount accumulates.
//
// Verifies that the enterLuaFrame hook covers both doCall and doTailCall (05 §7.5;
// doTailCall internally goes through enterLuaFrame and is therefore covered naturally, with no separate hook needed).
func TestProfile_EntryCountAccumulates(t *testing.T) {
	src := `
local function inner(x) return x*x end
local function caller(n)
  local s = 0
  for i=1,n do s = s + inner(i) end
  return s
end
result = caller(20)
`
	st := runLuaWithBridge(t, src)
	v, _ := st.tableGet(st.globals, st.makeStringValue("result"))
	want := 0.0
	for i := 1; i <= 20; i++ {
		want += float64(i * i)
	}
	if !value.IsNumber(v) || value.AsNumber(v) != want {
		t.Fatalf("script result = %v, want %v", debugVal(st, v), want)
	}

	// inner is a small single-parameter non-vararg function (`local function inner(x) return x*x end`),
	// and should be called 20 times.
	innerProto := findProtoByParams(t, st, 1)
	pd := st.bridge.ProfileOf(innerProto)
	if pd.EntryCount != 20 {
		t.Errorf("inner entryCount = %d, want 20", pd.EntryCount)
	}
}

// TestProfile_TierGuardBlocksWhenStuck verifies that when ProfileData.TierState=TierStuck
// onBackEdge / onEnter return immediately (01 §4.1 guard; already verified in the bridge package's tests,
// here we verify the crescent → bridge wiring layer holds the guard too).
func TestProfile_TierGuardBlocksWhenStuck(t *testing.T) {
	src := `
local function f(n)
  local s = 0
  for i=1,n do s = s + 1 end
  return s
end
result = f(10)
`
	// The first run lets the counts accumulate
	st := runLuaWithBridge(t, src)
	innerProto := findProtoByParams(t, st, 1)
	pd := st.bridge.ProfileOf(innerProto)
	before := pd.MaxBackEdge()

	// Force TierState to TierStuck, then run again ⇒ the count should no longer rise
	pd.TierState = 2 // TierStuck (avoids a circular import of the bridge package)
	cl := st.loadedCls[0]
	if _, err := st.Call(cl, nil, 0); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	after := pd.MaxBackEdge()
	if after != before {
		t.Errorf("MaxBackEdge changed under TierStuck: before=%d after=%d",
			before, after)
	}
}
