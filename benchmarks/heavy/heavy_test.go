// Package heavy benchmarks pure-arithmetic hotspot scripts that are friendly to
// the upper tiers (P3 wasm / P4 jit): heavy_arith (single function + long
// FORLOOP + pure float) / heavy_recursion (tail-recursive collatz) /
// heavy_floatloop (flat float kernel + nested FORLOOP).
//
// Complementary to benchmarks/realworld: the 5 realworld scripts are classic
// benchmark-game workloads dominated by tables/strings/CALL, and their real
// work runs on the Go side via P3/P4 helpers, so the upper-tier gains are not
// obvious; these 3 heavy scripts deliberately drop the table/string/CALL math
// library dependencies and stress only "single big-function hotspot + long BB +
// arithmetic" — surfacing the shapes where the upper tiers can actually pay off.
//
// **P4 shape-subset coverage** (following the PJ7 scope in
// docs/design/p4-method-jit/implementation-progress.md): the current P4 PJ7
// wired-in subset is "single-BB value production + RETURN / byte-level FORLOOP
// inline / table IC direct dispatch / CALL void / SELF method", and the inner
// kernel shapes of the three heavy scripts (FORLOOP body with multiple
// arithmetic ops + accumulation / while single-condition + float / self
// recursion + arithmetic) are all **outside** the current PJ7 shape allowlist.
// **P4 is expected to gain 0**, with numbers ≈ the P1 interpreter — this is an
// honest coverage exposure, marked as a followup to extend the SAO allowlist in
// P4 PJ7+; we do not tweak the scripts to accommodate the P4 subset.
//
// The P3 wasm subset is broad (MOVE/arithmetic/comparison/FORLOOP/CALL are all
// enabled, see internal/gibbous/wasm/compiler.go NewCompiler), and all three
// scripts can lift one inner kernel Proto, with numbers reflecting the wazero
// bytecode-translation gains.
//
// This file has no build tag — `TestHeavy_OracleParity` is needed under all
// three p1/p3/p4 builds (it verifies wangshu's own behavior against the official
// 5.1.5, independent of the build variant). The benchmark parts (`_Wangshu` /
// `_Gopher`) are split into `heavy_bench_test.go`, carrying
// `!wangshu_p3 && !wangshu_p4` to keep the wangshu_profile sampling hooks from
// polluting the new-moon numbers. `_Gibbous` lives in `heavy_gibbous_test.go`
// (wangshu_p3 + wangshu_profile), and `_GibbousJIT` in
// `heavy_gibbous_jit_test.go` (wangshu_p4 + wangshu_profile).
package heavy

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
)

var scripts = []string{"heavy_arith", "heavy_recursion", "heavy_floatloop"}

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

func runLua51(tb testing.TB, src []byte, name string) (string, bool) {
	tb.Helper()
	bin, err := exec.LookPath("lua5.1")
	if err != nil {
		return "", false
	}
	tmp := filepath.Join(tb.TempDir(), name+".lua")
	if err := os.WriteFile(tmp, src, 0o644); err != nil {
		tb.Fatalf("write tmp: %v", err)
	}
	cmd := exec.Command(bin, tmp)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		tb.Fatalf("lua5.1 %s: %v\n%s", name, err, out.String())
	}
	return strings.TrimRight(out.String(), "\n"), true
}

// TestHeavy_OracleParity: wangshu vs the official lua5.1 interpreter,
// byte-for-byte (numbers may differ in the last floating-point digit; this test
// compares print semantics rather than requiring byte-equal — it only checks
// that the main path does not crash and the results are of the same magnitude).
//
// The scripts `return` numbers, which would need manual print wrapping on the
// lua5.1 interpreter side; here instead each script carries its own `print(...)`
// wrapper, so oracle and wangshu take the same shape.
//
// Actual purpose: a defensive confirmation that these 3 heavy scripts run
// through cleanly, without crashing, under wangshu's three tiers (p1/p3/p4
// builds) and the lua5.1 oracle.
func TestHeavy_OracleParity(t *testing.T) {
	for _, name := range scripts {
		t.Run(name, func(t *testing.T) {
			src := loadScript(t, name)
			got := runWangshu(t, src, name)
			// Wrap with print(...) on lua5.1 side: `return X` → `print((function() return X end)())`.
			oracleSrc := []byte("print((function()\n")
			oracleSrc = append(oracleSrc, src...)
			oracleSrc = append(oracleSrc, []byte("\nend)())\n")...)
			want, ok := runLua51(t, oracleSrc, name)
			if !ok {
				t.Skip("lua5.1 not in PATH; skipping oracle parity check")
				return
			}
			if got != want {
				t.Errorf("oracle mismatch for %s:\nwangshu: %q\nlua5.1 : %q", name, got, want)
			}
		})
	}
}
