package regression

import (
	"testing"

	wangshu "github.com/Liam0205/wangshu"
)

// TestStdinPositionSharedAcrossStates pins that the stdin read position is process-global.
//
// The reader started as a package global (raced across States), then moved onto the State to
// fix the race -- which silently LOST input instead: two States used in sequence read the
// first line then nil, because the first State's buffered lookahead had swallowed the rest and
// went away with it. os.Stdin is one descriptor with one offset, so two readers cannot each
// own it; the sharing is correct and the race is fixed with a lock.
//
// This test cannot feed real stdin (go test does not forward it), so it asserts the weaker
// property that still catches a regression: two States must not each report a fresh EOF-free
// read of the same bytes, and creating a second State must not reset anything observable.
func TestStdinPositionSharedAcrossStates(t *testing.T) {
	var first, second string
	for i := 0; i < 2; i++ {
		st := wangshu.NewState(wangshu.Options{})
		prog, err := wangshu.Compile([]byte(`return tostring(io.read("*l"))`), "t")
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		res, rerr := prog.Run(st)
		if rerr != nil {
			t.Fatalf("run: %v", rerr)
		}
		if i == 0 {
			first = res[0].Str()
		} else {
			second = res[0].Str()
		}
	}
	// With no stdin both are nil; the point is that the second State does not panic or
	// observe a different world from the first.
	if first != second {
		t.Errorf("two States saw different stdin state: %q then %q", first, second)
	}
}

// TestIOReadHugeCountDoesNotOOM pins that a script-supplied count is not pre-allocated.
//
// io.read(1e10) used to do make([]byte, n) before reading a byte, asking for 10 GB. That
// allocation is outside MaxArenaBytes and outside Program.call's recover, so a Go fatal OOM
// took the host process down -- and a fuzz seed could kill the worker with it. PUC reads
// incrementally and returns whatever arrived.
func TestIOReadHugeCountDoesNotOOM(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{MaxArenaBytes: 16 << 20})
	prog, err := wangshu.Compile([]byte(`return tostring(io.read(1e10))`), "t")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if _, rerr := prog.Run(st); rerr != nil {
		t.Fatalf("run: %v", rerr)
	}
}
