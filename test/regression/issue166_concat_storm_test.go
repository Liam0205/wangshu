// issue166_concat_storm_test.go — issues #166/#167 (concat-storm family
// stack with `panic: deadlocked!` from Go fuzz's per-input watchdog. The
// deaths were never memory OOMs; they were Go fuzz's 10s per-input watchdog
// firing on CPU-bound concat work. Fix: chargeBulkWork bills len(result)>>6
// (1 step per 64 bytes) to the budget inside the shared doConcat, which
// every tier routes through (P1 executeLoop, P3 wasm h_concat, P4 native
// host.Concat).
//
// Killer flight records (from the run artifacts' fuzz-forensics/):
//   #166: for i=1,777777776 do qut = out .. cat(i) end  (cat returns a
//         ~60B literal .. i)  — light minimized form, replays clean
//   #167: for i=1,777777776 do ouX = cat(i)   end  (cat returns a
//         ~1200B literal .. i) — the concat is INSIDE cat's return

package regression

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
// concatenates a ~15KiB literal must trip the instruction budget. The
// discard form throws the result away (global glob), so the arena stays
// bounded and the ONLY thing that can stop the loop is the byte-charged
// step budget -- there is no arena-cap escape hatch.
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

	// Contract: the byte-charged budget stops the loop. Non-termination
	// (charge removed) is caught by the package `go test -timeout`, not an
	// in-test wall-clock bound -- see llmdoc unreproducible-crasher-triage:
	// in-test deadlines bet against shared-runner speed. The wall-clock
	// property is guarded by TestIssue166_HarnessWatchdogMargin against Go
	// fuzz's real fixed 10s watchdog.
	if _, rerr := prog.Run(st); !isBudgetErr(rerr) {
		t.Fatalf("byte-heavy concat did not hit the instruction budget: err=%v", rerr)
	}
}

// TestIssue166_HarnessWatchdogMargin is the primary regression guard: it
// mirrors the FuzzAutoPromote harness (4x prog.Run per input) and asserts
// the total beats Go fuzz's 10s per-input "deadlocked!" watchdog -- the
// exact fixed external threshold that killed the workers, a real
// production deadline rather than an arbitrary in-test bound. Pre-fix the
// 15KiB discard loop did ~2.7s of byte work per run (~13.5s for 4x on the
// ~10x-slower CI runners, which is what failed PR #168's first CI); the
// byte charge cuts each run to bounded work, so 4x completes in well under
// a second locally and ~1s on CI. The 8s guard (under the 10s watchdog)
// leaves ~10x margin post-fix while still failing on the pre-fix behavior.
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
		t.Fatalf("4x prog.Run took %v — past the safety margin under Go fuzz's 10s watchdog (pre-fix was ~13.5s on CI)", elapsed)
	}
}

// TestIssue166_LegitConcatUnaffected proves the byte charge does not
// over-reject ordinary code. The charge is len>>6, so concats below 64
// bytes cost zero extra steps, and a single legitimate ~1MiB concat costs
// only ~16K steps against a 1<<20 budget.
func TestIssue166_LegitConcatUnaffected(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		{
			// 50K iterations of small concats: byte charge is tiny (each
			// result is a few dozen bytes), so only preempt steps
			// dominate (~2/iter well under 1<<20).
			name: "many-small-concats",
			src:  `local n=0; for i=1,50000 do local x = "prefix-" .. i .. "-suffix"; n = n + #x end; return n`,
		},
		{
			// One legitimate ~1MiB concat: ~16K charged steps.
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
