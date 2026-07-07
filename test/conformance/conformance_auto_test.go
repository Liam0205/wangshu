//go:build (wangshu_p3 || wangshu_p4) && wangshu_profile

// conformance_auto_test.go — AUTO-mode conformance pass (issue:
// production runs auto, but the conformance harness only ever drove
// promotion via SetForceAllPromote(true)).
//
// TestConformance_Auto re-runs the full shared `cases` corpus on a
// State whose natural-heat thresholds are lowered (entry=2, backEdge=4)
// and which Runs each program repeatedly: whatever protos cross the
// thresholds promote mid-run through the REAL auto decision chain
// (runtime compilability recheck, profitability gate, short-proto
// floor + exemption — all of which force-all bypasses or never
// consults), and every run must still produce the case's expected
// bytes. Cases that never promote (below the gates) are still valid
// interpreter checks — the fail-stop path guard below asserts the
// suite as a whole promoted something, so it cannot silently
// degenerate to a duplicate interpreter pass.

package conformance

import (
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
)

// autoConfRuns: repeat count per case. With entry=2, a repeat-called
// kernel promotes during run 1; runs 2-3 execute tier-mixed.
const autoConfRuns = 3

// autoAnchorCases: shapes GUARANTEED to pass both backends' auto
// acceptance gates (long pure-arithmetic kernels: >= 10 opcodes clears
// the short-proto floor, zero helper-bound ops short-circuits P3's
// WorthPromoting density gate). The shared `cases` corpus is mostly
// one-shot short scripts that the auto gates rightly decline — these
// anchors are what makes the fail-stop path guard meaningful on every
// build.
var autoAnchorCases = []confCase{
	{"auto_anchor_arith_chain", `
local function f(x, y)
  local a = x * 2 + y
  local b = a - x / 2
  local c = b * b + a
  return c - y + a - b
end
local s = 0
for i = 1, 12 do s = s + f(i, i + 1) end
return s`, "4659.5"},
	{"auto_anchor_floatloop", `
local function accum(n)
  local s = 0
  for i = 1, n do s = s + i * 2 end
  return s
end
local total = 0
for i = 1, 8 do total = total + accum(10) end
return total`, "880"},
}

// TestConformance_Auto: the full conformance corpus, auto-promoted.
func TestConformance_Auto(t *testing.T) {
	promotedTotal := 0
	for _, c := range append(append([]confCase{}, cases...), autoAnchorCases...) {
		t.Run(c.name, func(t *testing.T) {
			prog, err := wangshu.Compile([]byte(c.src), c.name)
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			st := wangshu.NewState(wangshu.Options{})
			st.SetHotThresholds(2, 4)
			for run := 1; run <= autoConfRuns; run++ {
				results, err := prog.Run(st)
				if err != nil {
					t.Fatalf("run %d: %v", run, err)
				}
				parts := make([]string, len(results))
				for i, r := range results {
					parts[i] = r.Display()
				}
				got := strings.Join(parts, "\t")
				if got != c.want {
					t.Errorf("run %d: got %q, want %q", run, got, c.want)
				}
			}
			promotedTotal += st.PromotionCount()
		})
	}
	// Fail-stop path guard (same discipline as
	// TestConformance_P4PathTriggered): if nothing promoted across the
	// whole corpus, this suite silently re-tested the interpreter.
	if promotedTotal == 0 {
		t.Error("PromotionCount stayed 0 across the auto conformance pass — " +
			"no natural-heat promotion happened; the suite is not exercising the auto path")
	}
}
