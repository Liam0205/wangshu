// issue166_concat_storm_test.go — issues #166/#167 (concat-storm family
// #123-#167): the instruction budget counted preempt points (loop back
// edge + frame entry) but NOT the byte volume of a CONCAT. A tight loop
// calling a function that concatenates a large string literal therefore
// ran for minutes of wall-clock INSIDE the budgeted iteration count:
// ~500K iterations * ~15KiB each. The fuzz harness runs 4x prog.Run per
// input, so that byte work blew past Go fuzz's 10s per-input
// "deadlocked!" watchdog and surfaced as "hung or terminated
// unexpectedly: exit status 2" — misread as a memory OOM for weeks until
// PR #165's worker forensics captured the runnable doConcat -> Intern
// stack. Fix: chargeBulkWork bills len(result)>>10 (1 step per KiB) to
// the budget inside the shared doConcat, which every tier routes through
// (P1 executeLoop, P3 wasm h_concat, P4 native host.Concat).
//
// Killer flight records (from the run artifacts' fuzz-forensics/):
//   #166: for i=1,777777776 do qut = out .. cat(i) end  (cat returns a
//         ~60B literal .. i)  — light minimized form, replays clean
//   #167: for i=1,777777776 do ouX = cat(i)   end  (cat returns a
//         ~1200B literal .. i) — the concat is INSIDE cat's return

package wangshu_test

import (
	"strings"
	"testing"
	"time"

	"github.com/Liam0205/wangshu"
)

// isBudgetErr reports whether err is the instruction-budget overrun.
func isBudgetErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "instruction budget exceeded")
}

// TestIssue166_ByteHeavyConcatHitsBudget: a loop calling a function that
// concatenates a ~15KiB literal must trip the instruction budget in
// bounded wall-clock, not run until the fuzz watchdog fires. Before the
// fix a single run took ~2.7s (byte work uncounted); after, it hits the
// budget in a few hundred ms. Guard at 5s: the pre-fix single run was
// 2.7s and the harness's 4x amplification crossed the 10s watchdog, so
// any per-run time near 2.7s means the byte charge is missing.
func TestIssue166_ByteHeavyConcatHitsBudget(t *testing.T) {
	lit := strings.Repeat("A", 15000)
	// Discard form: the result is thrown away (global glob), so the
	// arena stays bounded and the ONLY thing that can stop the loop is
	// the byte-charged step budget (no arena-cap escape hatch).
	src := `local function cat(i) return "` + lit + `" .. i end
	        local out=""; for i=1,1000000000 do glob = cat(i) end; return out`
	prog, err := wangshu.Compile([]byte(src), "i166")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{MaxArenaBytes: 64 << 20})
	st.SetStepBudget(1 << 20)
	st.SetHotThresholds(^uint32(0), ^uint32(0)) // interpreter path

	start := time.Now()
	_, rerr := prog.Run(st)
	elapsed := time.Since(start)

	if !isBudgetErr(rerr) {
		t.Fatalf("byte-heavy concat did not hit the instruction budget: err=%v (elapsed %v)", rerr, elapsed)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("byte-heavy concat ran %v before the budget fired — byte charge missing (pre-fix ~2.7s single run)", elapsed)
	}
}

// TestIssue166_HarnessWatchdogMargin mirrors the FuzzAutoPromote harness
// (4x prog.Run per input) and asserts the total stays well under Go
// fuzz's 10s per-input "deadlocked!" watchdog — the exact condition that
// killed the workers.
func TestIssue166_HarnessWatchdogMargin(t *testing.T) {
	lit := strings.Repeat("A", 15000)
	src := `local function cat(i) return "` + lit + `" .. i end
	        local out=""; for i=1,1000000000 do glob = cat(i) end; return out`
	prog, err := wangshu.Compile([]byte(src), "i166w")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	start := time.Now()
	for r := 0; r < 4; r++ {
		st := wangshu.NewState(wangshu.Options{MaxArenaBytes: 64 << 20})
		st.SetStepBudget(1 << 20)
		st.SetHotThresholds(^uint32(0), ^uint32(0))
		_, _ = prog.Run(st)
	}
	if elapsed := time.Since(start); elapsed > 8*time.Second {
		t.Fatalf("4x prog.Run took %v — too close to Go fuzz's 10s watchdog", elapsed)
	}
}

// TestIssue166_LegitConcatUnaffected proves the byte charge does not
// over-reject ordinary code. The charge is len>>10, so concats below
// 1KiB cost zero extra steps, and a single legitimate ~1MiB concat costs
// only ~1024 steps against a 1<<20 budget.
func TestIssue166_LegitConcatUnaffected(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		{
			// 50K iterations of small (<1KiB) concats: byte charge is
			// zero, only preempt steps count (~2/iter well under 1<<20).
			name: "many-small-concats",
			src:  `local n=0; for i=1,50000 do local x = "prefix-" .. i .. "-suffix"; n = n + #x end; return n`,
		},
		{
			// One legitimate ~1MiB concat: ~1024 charged steps.
			name: "single-1mib-concat",
			src:  `local a = string.rep("x", 1000000); local b = a .. "!"; return #b`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prog, err := wangshu.Compile([]byte(tc.src), "i166legit")
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			st := wangshu.NewState(wangshu.Options{MaxArenaBytes: 64 << 20})
			st.SetStepBudget(1 << 20)
			st.SetHotThresholds(^uint32(0), ^uint32(0))
			if _, rerr := prog.Run(st); rerr != nil {
				t.Fatalf("legit concat tripped budget/limit: %v", rerr)
			}
		})
	}
}
