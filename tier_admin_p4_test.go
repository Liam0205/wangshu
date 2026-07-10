//go:build wangshu_p4 && wangshu_profile

// tier_admin_p4_test.go — prove-the-path half of the tier admin API
// (see tier_admin_test.go for the build-neutral half).
//
// "The result is correct" alone cannot distinguish the interpreter
// from the native path (llmdoc prove-the-path guide), so these tests
// pin the switch's routing effect with the NativeRunCount white-box
// probe: tier off → the promoted proto's next run adds ZERO native
// entries; tier back on → native entries resume without recompiling.
package wangshu_test

import (
	"testing"

	"github.com/Liam0205/wangshu"
	"github.com/Liam0205/wangshu/internal/gibbous/jit/peroptranslator"
)

const tierAdminSrc = `
	local function kernel(n)
		local s = 0
		for i = 1, n do s = s + i * i end
		return s
	end
	local t = 0
	for _ = 1, 20 do t = kernel(1000) end
	return t
`

const tierAdminWant = 333833500 // 20th assignment of sum(i*i, 1..1000)

// TestTierAdmin_KillSwitchRoutesToInterpreter drives the full
// production degrade-and-recover cycle on one State:
//  1. promote (force-all) and prove the native path runs;
//  2. flip the switch off — the SAME promoted proto must run with
//     zero new native entries and byte-equal results;
//  3. flip it back on — native execution resumes (installed code
//     reused; Promoted count unchanged, so no recompile happened).
func TestTierAdmin_KillSwitchRoutesToInterpreter(t *testing.T) {
	prog, err := wangshu.Compile([]byte(tierAdminSrc), "tier-admin-p4")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(true)

	// (1) warmup run promotes; second run executes native code.
	if _, err := prog.Run(st); err != nil {
		t.Fatalf("warmup: %v", err)
	}
	before := peroptranslator.NativeRunCount.Load()
	rets, err := prog.Run(st)
	if err != nil {
		t.Fatalf("promoted run: %v", err)
	}
	if rets[0].Number() != tierAdminWant {
		t.Fatalf("promoted run wrong result: %v", rets[0].Display())
	}
	if peroptranslator.NativeRunCount.Load() == before {
		t.Fatal("carrier did not exercise the native path; test is vacuous")
	}
	promotedBefore := st.TierStatsSnapshot().Promoted
	if promotedBefore == 0 {
		t.Fatal("TierStats.Promoted == 0 after force-all promotion")
	}

	// (2) kill switch off: same State, same promoted proto — zero new
	// native entries, same result.
	st.SetTierEnabled(false)
	if st.TierEnabled() {
		t.Fatal("TierEnabled still true after SetTierEnabled(false)")
	}
	off := peroptranslator.NativeRunCount.Load()
	rets, err = prog.Run(st)
	if err != nil {
		t.Fatalf("tier-off run: %v", err)
	}
	if rets[0].Number() != tierAdminWant {
		t.Fatalf("tier-off run wrong result: %v", rets[0].Display())
	}
	if got := peroptranslator.NativeRunCount.Load(); got != off {
		t.Fatalf("tier off but native path ran: NativeRunCount %d -> %d", off, got)
	}
	if st.TierStatsSnapshot().TierEnabled {
		t.Fatal("TierStatsSnapshot().TierEnabled must be false while off")
	}

	// (3) re-enable: native execution resumes, and Promoted is
	// unchanged (installed code reused — no recompile).
	st.SetTierEnabled(true)
	on := peroptranslator.NativeRunCount.Load()
	rets, err = prog.Run(st)
	if err != nil {
		t.Fatalf("re-enabled run: %v", err)
	}
	if rets[0].Number() != tierAdminWant {
		t.Fatalf("re-enabled run wrong result: %v", rets[0].Display())
	}
	if peroptranslator.NativeRunCount.Load() == on {
		t.Fatal("tier re-enabled but native path did not resume")
	}
	if got := st.TierStatsSnapshot().Promoted; got != promotedBefore {
		t.Fatalf("Promoted changed across off/on cycle: %d -> %d (unexpected recompile)", promotedBefore, got)
	}
}

// TestTierAdmin_OffBlocksNewPromotions verifies the other half of the
// switch: with the tier disabled from the start, force-all never
// promotes anything (sampling hooks short-circuit before counting).
func TestTierAdmin_OffBlocksNewPromotions(t *testing.T) {
	prog, err := wangshu.Compile([]byte(tierAdminSrc), "tier-admin-p4-nopromote")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(true)
	st.SetTierEnabled(false)
	rets, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rets[0].Number() != tierAdminWant {
		t.Fatalf("wrong result: %v", rets[0].Display())
	}
	if got := st.TierStatsSnapshot().Promoted; got != 0 {
		t.Fatalf("tier off from birth but Promoted = %d", got)
	}
}

// TestTierAdmin_StatsClassifyStuck verifies the Stuck breakdown: a
// vararg proto (compilability-excluded shape) lands in
// StuckNotCompilable, and nothing lands in StuckCompileFailed.
func TestTierAdmin_StatsClassifyStuck(t *testing.T) {
	// kernel is promotable; vk is vararg (F1-excluded) and hot enough
	// to reach the promotion decision, absorbing to Stuck. 100 calls:
	// force-all's warm-up retry window keeps a not-compilable proto in
	// TierInterp until entry 64, so 20 would never absorb.
	src := `
		local function vk(...)
			local s = 0
			for i = 1, 100 do s = s + i end
			return s + select('#', ...)
		end
		local function kernel(n)
			local s = 0
			for i = 1, n do s = s + i end
			return s
		end
		local t = 0
		for _ = 1, 100 do t = kernel(100) + vk(1, 2) end
		return t
	`
	prog, err := wangshu.Compile([]byte(src), "tier-admin-p4-stuck")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(true)
	if _, err := prog.Run(st); err != nil {
		t.Fatalf("run: %v", err)
	}
	stats := st.TierStatsSnapshot()
	if stats.Promoted == 0 {
		t.Fatalf("expected kernel promoted, stats=%+v", stats)
	}
	if stats.StuckNotCompilable == 0 {
		t.Fatalf("expected vararg proto in StuckNotCompilable, stats=%+v", stats)
	}
	if stats.StuckCompileFailed != 0 {
		t.Fatalf("unexpected compile failures: %+v", stats)
	}
	if stats.Profiled < stats.Promoted+stats.StuckNotCompilable {
		t.Fatalf("Profiled (%d) < Promoted+Stuck (%d+%d)", stats.Profiled, stats.Promoted, stats.StuckNotCompilable)
	}
}
