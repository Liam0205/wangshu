//go:build (wangshu_p3 || wangshu_p4) && wangshu_profile

// issue166_concat_storm_tiered_test.go — tiered dual of
// issue166_concat_storm_test.go. All three tiers route CONCAT through the
// shared doConcat (P3 wasm h_concat, P4 native host.Concat), so
// chargeBulkWork bounds a byte-heavy concat storm on the promoted paths
// too. This pins that the single charge point covers every backend.

package wangshu_test

import (
	"strings"
	"testing"
	"time"

	"github.com/Liam0205/wangshu"
)

// TestIssue166_TieredConcatStormBounded: under force-all promotion the
// concat runs on the promoted (wasm/native) path and must still hit the
// budget in bounded wall-clock.
func TestIssue166_TieredConcatStormBounded(t *testing.T) {
	if raceEnabled {
		t.Skip("tiered mmap/wasm paths not race-safe; covered by non-race jobs")
	}
	lit := strings.Repeat("A", 15000)
	src := `local function cat(i) return "` + lit + `" .. i end
	        local out=""; for i=1,1000000000 do glob = cat(i) end; return out`
	prog, err := wangshu.Compile([]byte(src), "i166tier")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{MaxArenaBytes: 64 << 20})
	st.SetStepBudget(1 << 20)
	st.SetForceAllPromote(true)

	start := time.Now()
	_, rerr := prog.Run(st)
	elapsed := time.Since(start)

	if !isBudgetErr(rerr) {
		t.Fatalf("tiered byte-heavy concat did not hit the budget: err=%v (elapsed %v)", rerr, elapsed)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("tiered byte-heavy concat ran %v before the budget fired", elapsed)
	}
}
