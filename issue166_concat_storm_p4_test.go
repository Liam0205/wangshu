//go:build wangshu_p4 && wangshu_profile

// issue166_concat_storm_p4_test.go — proves the CONCAT byte-charge
// (chargeBulkWork in the shared doConcat) actually bounds the storm on the
// PROMOTED path, not just the interpreter. P4 is the tier where a promoted
// byte-heavy concat is exercisable: force-all promotion compiles cat
// eagerly, so its CONCAT runs as native code that dispatches through
// HelperConcat -> (*State).Concat -> doConcat -> chargeBulkWork.
//
// P3 is intentionally not covered here: P3 does not promote a
// concat-bearing loop in these shapes (promotion stays 0 even without a
// budget), so a promoted P3 concat cannot be exercised to assert against.
// P3's h_concat routes through the same charged (*State).Concat -> doConcat
// by construction, and the interpreter-path bound (issue166_concat_storm_
// test.go, no build tag) runs in the p3 job too. The #166/#167 crashers
// themselves were on the interpreter path (forensics stack: executeLoop ->
// doConcat), which the untagged test covers directly.

package wangshu_test

import (
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
	"github.com/Liam0205/wangshu/internal/crescent"
)

// TestIssue166_P4PromotedConcatCharged: under force-all promotion the
// ~15KiB concat inside cat runs as native code, and the byte-charge must
// still stop the storm. Prove-the-path (llmdoc/guides/prove-the-path-
// under-test.md): the budget error alone is not enough -- the interpreter
// returns the identical error, so the test would pass even if nothing
// promoted. Two orthogonal signals confirm the concat ran natively:
// PromotionCount() > 0 and a non-zero crescent.ConcatHelperHits delta (a
// promoted segment routed CONCAT through the shared (*State).Concat helper,
// which the interpreter never touches).
func TestIssue166_P4PromotedConcatCharged(t *testing.T) {
	if raceEnabled {
		t.Skip("tiered mmap paths not race-safe; covered by non-race jobs")
	}
	lit := strings.Repeat("A", 15000)
	src := `local function cat(i) return "` + lit + `" .. i end
	        local out=""; for i=1,1000000000 do glob = cat(i) end; return out`
	prog, err := wangshu.Compile([]byte(src), "i166p4")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{MaxArenaBytes: 64 << 20})
	st.SetStepBudget(1 << 20)
	st.SetForceAllPromote(true)

	hitsBefore := crescent.ConcatHelperHits.Load()
	_, rerr := prog.Run(st)

	if !isBudgetErr(rerr) {
		t.Fatalf("promoted byte-heavy concat did not hit the budget: err=%v", rerr)
	}
	if pc := st.PromotionCount(); pc == 0 {
		t.Fatalf("nothing promoted (PromotionCount=0) — concat ran interpreted, not native")
	}
	// ConcatHelperHits is a process-global counter, so this delta is only
	// exclusively ours while the profile tests run serially (none call
	// t.Parallel()). The assertion direction keeps it safe regardless:
	// concurrent runs could only ADD hits, never zero the delta, so a
	// parallel future would weaken isolation but not produce a false pass.
	if delta := crescent.ConcatHelperHits.Load() - hitsBefore; delta == 0 {
		t.Fatal("no CONCAT routed through the promoted (*State).Concat helper — byte bound proven only on the interpreter path")
	}
}
