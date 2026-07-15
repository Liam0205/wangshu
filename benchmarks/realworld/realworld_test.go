// Package realworld benchmarks the classic benchmark-game scripts (fib /
// binary-trees / spectral-norm / fannkuch / n-body) on Wangshu vs gopher-lua,
// using the official lua5.1 output as the correctness oracle
// (TestRealWorld_OracleParity).
//
// These complement the three-tier baseline microbenchmarks: they are a
// realistic mixed workload of calls / allocations / floating-point / table
// operations, addressing the "microbenchmark != real workload" gap in the
// performance story. Run: `make bench-p1`.
//
// This file has no build tag -- `TestRealWorld_OracleParity` is needed under
// both the p1 and p3 builds (it verifies that wangshu's own behavior matches
// official 5.1.5, independent of the build variant).
// The benchmark parts (`_Wangshu` / `_Gopher`) are split into
// `realworld_bench_test.go` with `!wangshu_p3` to avoid contamination from the
// p3 build's sampling hooks (issue #15 review).
// The `_Gibbous` benchmark lives in `realworld_gibbous_test.go` with its own
// wangshu_p3 tag.
package realworld

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
)

var scripts = []string{"fib", "binarytrees", "spectralnorm", "fannkuch", "nbody"}

func loadScript(tb testing.TB, name string) []byte {
	tb.Helper()
	src, err := os.ReadFile(filepath.Join("testdata", name+".lua"))
	if err != nil {
		tb.Fatalf("read %s: %v", name, err)
	}
	return src
}

func runWangshu(tb testing.TB, src []byte, name string) string {
	tb.Helper()
	prog, err := wangshu.Compile(src, name)
	if err != nil {
		tb.Fatalf("wangshu compile %s: %v", name, err)
	}
	st := wangshu.NewState(wangshu.Options{})
	results, err := prog.Run(st)
	if err != nil {
		tb.Fatalf("wangshu run %s: %v", name, err)
	}
	parts := make([]string, len(results))
	for i, r := range results {
		parts[i] = r.Display()
	}
	return strings.Join(parts, "\t")
}

// TestRealWorld_OracleParity checks each script's return value against the
// official lua5.1 oracle.
func TestRealWorld_OracleParity(t *testing.T) {
	oracle, err := exec.LookPath("lua5.1")
	if err != nil {
		t.Skip("lua5.1 oracle not found; skipping parity check")
	}
	for _, name := range scripts {
		t.Run(name, func(t *testing.T) {
			src := loadScript(t, name)
			// Oracle side: print the return values (\t join matches the
			// wangshu side)
			wrapped := "local function __chunk() " + string(src) + "\nend\n" +
				"print(__chunk())"
			cmd := exec.Command(oracle, "-")
			cmd.Stdin = bytes.NewReader([]byte(wrapped))
			var out bytes.Buffer
			cmd.Stdout = &out
			if err := cmd.Run(); err != nil {
				t.Fatalf("oracle: %v", err)
			}
			want := strings.TrimRight(out.String(), "\n")
			got := runWangshu(t, src, name)
			if got != want {
				t.Errorf("parity diff:\n  wangshu: %q\n  oracle:  %q", got, want)
			}
		})
	}
}
