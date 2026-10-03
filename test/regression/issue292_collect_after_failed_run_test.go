package regression

import (
	"testing"

	wangshu "github.com/Liam0205/wangshu"
)

// TestCollectAfterAFailedRunKeepsEscapedUpvalues covers #292: a top-level Run, Call or CallInto that
// fails leaves the frames it unwound without their RETURNs, so their upvalues are still open. They were
// closed only when the next Run started, and between the two the main thread's stack is no GC root, so a
// host Collect() there freed what an escaped closure still referenced; the next Run then read whatever
// reused that memory. lua_pcall closes them before returning (luaD_pcall's luaF_close), so the closure
// keeps the value it saw. Each entry point is paired with a forced Collect, MaybeCollectNow with GC
// stress mode on (the threshold-gated entry, made to collect), and no collection at all as the control.
// Since #291 the collector also marks the value an open upvalue points at, which keeps the closure's
// values alive by itself, so this test fails only when both are gone; the close is pinned on its own by
// internal/crescent's TestCallOnStackErrorClosesUpvalues.
func TestCollectAfterAFailedRunKeepsEscapedUpvalues(t *testing.T) {
	const fail = `local x = {name = "kept"} local s = "str" .. tostring(#"kept")
function g() return x.name, s end
error("boom")`
	const read = `local t = {} for i = 1, 4000 do t[i] = {name = i} end
return g()`

	entries := []struct {
		name string
		run  func(t *testing.T, st *wangshu.State) error
	}{
		{"Run", func(t *testing.T, st *wangshu.State) error {
			prog, err := wangshu.Compile([]byte(fail), "=x")
			if err != nil {
				t.Fatal(err)
			}
			_, err = prog.Run(st)
			return err
		}},
		{"Call", func(t *testing.T, st *wangshu.State) error {
			prog, err := wangshu.Compile([]byte("F = function() "+fail+" end"), "=x")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := prog.Run(st); err != nil {
				t.Fatal(err)
			}
			_, err = st.Call(st.GetGlobal("F"))
			return err
		}},
		{"CallInto", func(t *testing.T, st *wangshu.State) error {
			prog, err := wangshu.Compile([]byte("F = function() "+fail+" end"), "=x")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := prog.Run(st); err != nil {
				t.Fatal(err)
			}
			_, err = st.CallInto(make([]wangshu.Value, 2), st.GetGlobal("F"))
			return err
		}},
	}
	collects := []struct {
		name string
		do   func(st *wangshu.State)
	}{
		{"no collection", func(*wangshu.State) {}},
		{"Collect", func(st *wangshu.State) { st.Collect() }},
		{"MaybeCollectNow", func(st *wangshu.State) {
			st.SetGCStressMode(true)
			st.MaybeCollectNow()
			st.SetGCStressMode(false)
		}},
	}
	for _, e := range entries {
		for _, c := range collects {
			st := wangshu.NewState(wangshu.Options{})
			if err := e.run(t, st); err == nil {
				t.Fatalf("%s: the failing chunk did not fail", e.name)
			}
			c.do(st)
			prog, err := wangshu.Compile([]byte(read), "=y")
			if err != nil {
				t.Fatal(err)
			}
			r, err := prog.Run(st)
			if err != nil {
				t.Errorf("%s / %s: %v", e.name, c.name, err)
				continue
			}
			if len(r) != 2 || r[0].Str() != "kept" || r[1].Str() != "str4" {
				t.Errorf("%s / %s: g() = %v, want \"kept\", \"str4\"", e.name, c.name, r)
			}
		}
	}
}
