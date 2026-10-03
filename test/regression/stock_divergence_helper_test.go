package regression

import (
	"testing"

	wangshu "github.com/Liam0205/wangshu"
)

// printedBy runs src as a chunk named "@x" on a fresh State and returns what it printed, so a case can
// be compared with lua5.1 running the same source as a file named "x". A compile or run error fails
// the test. Shared by the #281-#288 tests.
func printedBy(t *testing.T, opts wangshu.Options, src string) string {
	t.Helper()
	prog, err := wangshu.Compile([]byte(src), "@x")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return captureStdout(t, func() {
		if _, err := prog.Run(wangshu.NewState(opts)); err != nil {
			t.Errorf("run: %v", err)
		}
	})
}

// loadMessage runs loadstring on src inside a fresh State and returns the error message lua5.1 would
// give, or "ok" when src compiles. loadstring is what the expectations were produced with, so the
// chunk name is src itself and any C-call depth it adds is the same on both sides.
func loadMessage(t *testing.T, src string) string {
	t.Helper()
	st := wangshu.NewState(wangshu.Options{})
	st.SetGlobal("SRC", wangshu.String(src))
	prog, err := wangshu.Compile([]byte(`local f, m = loadstring(SRC) return f and "ok" or m`), "=loadMessage")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	r, err := prog.Run(st)
	if err != nil || len(r) != 1 {
		t.Fatalf("run: %v, %v", r, err)
	}
	return r[0].Str()
}
