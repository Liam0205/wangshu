//go:build wangshu_p4 && wangshu_profile

package regression

import (
	"testing"

	wangshu "github.com/Liam0205/wangshu"
)

// TestP4PromotionCallDoesNotReplayBeforeHostTailCall covers #273. When a function is promoted on
// entry (the threshold call), the interpreter keeps executing that same call, and the TAILCALL arm
// then dispatched "the tail callee's gibbous code" -- but for a HOST tail callee no frame is entered
// and ci is still the caller, so it ran the caller's freshly compiled code from pc 0. Everything
// before the TAILCALL ran a second time: the counter below ended one too high, and with GC stress on
// (a collection at every safepoint) the replay indexed a table the first pass had already dropped.
//
// Each case runs under force-all promotion with GC stress and, where the proto is long enough to
// clear the short-proto promotion floor, under auto promotion too (default thresholds, crossed
// mid-loop). Both are compared against the P1 interpreter, and PromotionCount guards against a
// vacuous pass where nothing was promoted.
func TestP4PromotionCallDoesNotReplayBeforeHostTailCall(t *testing.T) {
	for _, tc := range []struct {
		name, src string
		forceOnly bool // shorter than the auto-mode promotion floor
	}{
		{"upvalue increment then tail call with table constructor",
			`local nid = 0
			local function mk(v) nid = nid + 1 return rawequal({id = 1}, {}) end
			for i = 1, 1000 do mk(i) end
			return nid`, false},
		{"the increment feeds the next value",
			`local nid = 0
			local function mk(v) nid = nid + 1 return setmetatable({v = v, id = nid}, {}) end
			local s = 0 for i = 1, 1000 do s = s + mk(i).id end
			return nid .. " " .. s`, false},
		{"table constructor with a computed field as the tail call argument",
			`local function f(i) return type({v = i % 50}) end
			local n = 0 for i = 1, 1000 do if f(i) == "table" then n = n + 1 end end
			return n`, true},
	} {
		prog, err := wangshu.Compile([]byte(tc.src), "i273")
		if err != nil {
			t.Fatalf("%s: compile: %v", tc.name, err)
		}
		st1 := wangshu.NewState(wangshu.Options{})
		st1.SetTierEnabled(false)
		want, err := prog.Run(st1)
		if err != nil {
			t.Fatalf("%s: P1: %v", tc.name, err)
		}
		for _, mode := range []struct {
			name           string
			force, gcStres bool
		}{{"auto", false, false}, {"force+gcstress", true, true}} {
			if tc.forceOnly && !mode.force {
				continue
			}
			st := wangshu.NewState(wangshu.Options{})
			st.SetForceAllPromote(mode.force)
			st.SetGCStressMode(mode.gcStres)
			got, err := prog.Run(st)
			if err != nil {
				t.Errorf("%s [%s]: %v", tc.name, mode.name, err)
				continue
			}
			if st.PromotionCount() == 0 {
				t.Errorf("%s [%s]: nothing was promoted, the case does not reach P4", tc.name, mode.name)
			}
			if got[0].Str() != want[0].Str() {
				t.Errorf("%s [%s]: got %q, want %q (P1)", tc.name, mode.name, got[0].Str(), want[0].Str())
			}
		}
	}
}
