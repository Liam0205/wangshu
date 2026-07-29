package regression

import (
	"os"
	"testing"

	wangshu "github.com/Liam0205/wangshu"
)

// withEmptyStdin points os.Stdin at /dev/null for the duration of a test.
//
// Tests here must not depend on the ambient stdin. An earlier version did, and it both HUNG
// when run from a terminal (a blocking read with no input) and FAILED when given piped data --
// asserting the opposite of the property it meant to pin. `go test` happens to supply
// /dev/null, so it passed there and broke only when the compiled binary was run directly,
// which is how `make test` runs it.
func withEmptyStdin(t *testing.T) {
	t.Helper()
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		t.Skip("no /dev/null")
	}
	orig := os.Stdin
	os.Stdin = devnull
	t.Cleanup(func() {
		os.Stdin = orig
		_ = devnull.Close()
	})
}

// TestStdinReadAtEOFIsNil pins that a read with no input available returns nil rather than
// blocking, panicking, or returning an empty string.
//
// The stdin reader is process-global and mutex-guarded. It was a bare global (which raced
// across States), then per-State (which silently LOST input, because os.Stdin is one
// descriptor with one offset and the first State's buffered lookahead went away with it), and
// is now shared again with a lock. That history is why this file exists.
func TestStdinReadAtEOFIsNil(t *testing.T) {
	withEmptyStdin(t)
	for _, tc := range []struct{ name, src, want string }{
		{"*l at EOF", `return tostring(io.read("*l"))`, "nil"},
		// "*a" is the one format that never fails: it yields "" at end of file.
		{"*a at EOF", `return string.format("%q", tostring(io.read("*a")))`, `""`},
		{"*n at EOF", `return tostring(io.read("*n"))`, "nil"},
		{"count at EOF", `return tostring(io.read(4))`, "nil"},
		{"zero count at EOF", `return tostring(io.read(0))`, "nil"},
	} {
		st := wangshu.NewState(wangshu.Options{})
		prog, err := wangshu.Compile([]byte(tc.src), "t")
		if err != nil {
			t.Fatalf("%s: compile: %v", tc.name, err)
		}
		res, rerr := prog.Run(st)
		if rerr != nil {
			t.Fatalf("%s: run: %v", tc.name, rerr)
		}
		if got := res[0].Str(); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestIOReadHugeCountDoesNotOOM pins that a script-supplied count is not pre-allocated.
//
// io.read(1e10) used to do make([]byte, n) before reading a byte, asking for 10 GB. That
// allocation is outside MaxArenaBytes and outside Program.call's recover, so a Go fatal OOM
// took the host process down -- and a fuzz seed could kill the worker with it.
func TestIOReadHugeCountDoesNotOOM(t *testing.T) {
	withEmptyStdin(t)
	st := wangshu.NewState(wangshu.Options{MaxArenaBytes: 16 << 20})
	prog, err := wangshu.Compile([]byte(`return tostring(io.read(1e10))`), "t")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if _, rerr := prog.Run(st); rerr != nil {
		t.Fatalf("run: %v", rerr)
	}
}
