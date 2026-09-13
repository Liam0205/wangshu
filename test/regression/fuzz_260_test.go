//go:build wangshu_p4 && wangshu_profile

// fuzz_260_test.go — regression pins for issue #260 (fuzz seed
// 96aaf5cc29cb7f3c): a global deleted with `s=nil` and then re-created read
// as nil on the P4 native path while P1 saw the new value. Two independent
// defects overlapped on that seed:
//
//   - the native inline GETGLOBAL/SETGLOBAL/GETTABLE/SETTABLE NodeHit and
//     ArrayHit fast paths compared the slot against 0xFFFE<<48 (TagUserdata)
//     instead of value.Nil (0xFFF8<<48), so a Nil slot passed the "!= Nil"
//     guard and was handed back as the value (pinned by
//     TestNilImmMatchesValueNil in the emitter packages, and by
//     TestNilArraySlotRoutesToIndexMetamethod here);
//   - deleting a key (rawSet with val=Nil, weak-table sweep) did not bump the
//     table gen although the freed slot can be reused by a different key or
//     the same key can be re-inserted elsewhere, so gen-only inline consumers
//     kept a stale slot index (TestRawTable_*BumpsGen in internal/crescent,
//     and TestReusedGlobalSlotDoesNotAliasDeletedKey here).
package regression

import (
	"testing"

	"github.com/Liam0205/wangshu"
)

func assertP1P4Equal(t *testing.T, name, src string) {
	t.Helper()
	prog, err := wangshu.Compile([]byte(src), name)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	run := func(forceAll bool) ([]wangshu.Value, error) {
		st := wangshu.NewState(wangshu.Options{MaxArenaBytes: 64 << 20})
		st.SetStepBudget(1 << 22)
		st.SetForceAllPromote(forceAll)
		return prog.Run(st)
	}
	resP1, errP1 := run(false)
	if errP1 != nil {
		t.Fatalf("P1 interpreter must succeed, got %v", errP1)
	}
	resP4, errP4 := run(true)
	if errP4 != nil {
		t.Fatalf("P4 diverged from P1: %v", errP4)
	}
	if len(resP1) != len(resP4) {
		t.Fatalf("result count: P1=%d P4=%d", len(resP1), len(resP4))
	}
	for i := range resP1 {
		if resP1[i].Display() != resP4[i].Display() {
			t.Errorf("result[%d]: P1=%q P4=%q", i, resP1[i].Display(), resP4[i].Display())
		}
	}
}

// TestDeletedGlobalMissesInlineNodeHit is the minimized #260 seed as it was
// found. Both defects contribute to it (the deleted key's slot keeps
// next>=0, so re-inserting `s` lands in a new slot without a gen bump, and
// the stale slot's Nil then slips past the mis-encoded guard), so this pin
// fails only when both fixes are reverted; the two tests below isolate them.
func TestDeletedGlobalMissesInlineNodeHit(t *testing.T) {
	assertP1P4Equal(t, "fuzz-260",
		`function f() s=nil s=0 local function g() B=s+0 end g() g() end `+
			`f() f() return B`)
}

// TestNilArraySlotRoutesToIndexMetamethod isolates the Nil immediate: the
// array part has no key->slot indirection, so gen never changes when t[2]
// is set to nil, and only the inline GETTABLE ArrayHit "slot != Nil" guard
// stands between the native code and a wrong answer. With the guard
// comparing against TagUserdata bits it accepted the Nil slot and returned
// -1 where P1 consults __index and returns 12. Precondition: the first two
// g() calls run on P1 and back-fill the GETTABLE IC as ArrayHit; the native
// compile that force-all triggers on a later frame entry bakes that
// snapshot, which is what makes the inline path (and its Nil guard) the
// one under test.
func TestNilArraySlotRoutesToIndexMetamethod(t *testing.T) {
	assertP1P4Equal(t, "fuzz-260-nil-array",
		`local t=setmetatable({1,2,3},{__index=function() return 9 end}) `+
			`local function g() local v=t[2] if v==nil then return -1 end local a=v+1 local b=a+1 local c=b+1 return c end `+
			`g() g() t[2]=nil return g()`)
}

// TestReusedGlobalSlotDoesNotAliasDeletedKey isolates the gen defect: g is
// natively compiled with a NodeHit snapshot for v4927, then v4927 is deleted
// and k2621 happens to be inserted into the very slot v4927 occupied. The
// two names were found by search; whether they share a slot depends on the
// string hashes AND on the globals table's hsize (how many globals the stdlib
// registers), so internal/stdlib's TestIssue260SlotReusePairStillHolds
// asserts the placement on a stdlib-loaded State and fails loudly if it ever
// stops holding. Without the gen bump on deletion the inline GETGLOBAL
// returned k2621's 7, and g computed 10 where P1 returns -1.
func TestReusedGlobalSlotDoesNotAliasDeletedKey(t *testing.T) {
	assertP1P4Equal(t, "fuzz-260-reuse",
		`v4927=1 `+
			`local function g() local q=v4927 if q==nil then return -1 end local a=q+1 local b=a+1 local c=b+1 return c end `+
			`g() g() g() v4927=nil k2621=7 return g()`)
}
