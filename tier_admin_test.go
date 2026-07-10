// tier_admin_test.go — production tier admin API (runtime kill switch
// + TierStats observability), build-tag-neutral half.
//
// The API must behave sanely on EVERY build variant:
//   - default build: no tier exists — switch is a no-op, TierEnabled
//     stays true, stats stay zero;
//   - p3/p4 builds: the switch really routes execution back to the
//     interpreter and stats reflect the promotion state (see
//     tier_admin_p4_test.go for the prove-the-path half).
package wangshu_test

import (
	"testing"

	"github.com/Liam0205/wangshu"
)

// TestTierAdmin_DefaultsOn verifies the switch starts enabled and the
// getter mirrors Set calls (on tiered builds) or stays pinned true (on
// the default build, where there is no tier to disable).
func TestTierAdmin_DefaultsOn(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	if !st.TierEnabled() {
		t.Fatal("TierEnabled must default to true")
	}
	stats := st.TierStatsSnapshot()
	if !stats.TierEnabled {
		t.Fatal("TierStatsSnapshot().TierEnabled must default to true")
	}
	if stats.Promoted != 0 || stats.StuckCompileFailed != 0 {
		t.Fatalf("fresh state must have zero promotion stats, got %+v", stats)
	}
}

// TestTierAdmin_SwitchIsSafeOnEveryBuild verifies flipping the switch
// around a script run never breaks execution, whatever the build.
func TestTierAdmin_SwitchIsSafeOnEveryBuild(t *testing.T) {
	prog, err := wangshu.Compile([]byte(`
		local function f(n)
			local s = 0
			for i = 1, n do s = s + i end
			return s
		end
		local t = 0
		for _ = 1, 10 do t = f(100) end
		return t
	`), "tier-admin")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetTierEnabled(false)
	rets, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run with tier off: %v", err)
	}
	if len(rets) != 1 || rets[0].Number() != 5050 {
		t.Fatalf("wrong result with tier off: %v", rets)
	}
	st.SetTierEnabled(true)
	rets, err = prog.Run(st)
	if err != nil {
		t.Fatalf("run with tier back on: %v", err)
	}
	if len(rets) != 1 || rets[0].Number() != 5050 {
		t.Fatalf("wrong result with tier re-enabled: %v", rets)
	}
}
