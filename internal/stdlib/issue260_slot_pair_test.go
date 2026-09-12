package stdlib

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/crescent"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// TestIssue260SlotReusePairStillHolds guards the e2e pin
// TestReusedGlobalSlotDoesNotAliasDeletedKey (test/regression): it only bites
// if k2621 lands in the node slot that deleting v4927 vacated, and that
// depends on the string hashes AND on the globals table's hsize, i.e. on how
// many globals OpenAll registers. Assert the placement on a State built like
// wangshu.NewState (stdlib loaded) so a future stdlib or hashing change fails
// here with a hint instead of letting the pin silently degrade into an
// ordinary P1/P4 parity check.
func TestIssue260SlotReusePairStillHolds(t *testing.T) {
	st := crescent.New()
	OpenAll(st)
	st.SetGlobal("v4927", value.NumberValue(1))
	idx0, ok := st.GlobalNodeSlot("v4927")
	if !ok {
		t.Fatal("v4927 is not in the globals hash part")
	}
	st.SetGlobal("v4927", value.Nil)
	st.SetGlobal("k2621", value.NumberValue(7))
	idx1, ok := st.GlobalNodeSlot("k2621")
	if !ok || idx1 != idx0 {
		t.Fatalf("k2621 landed in slot %d (in hash part: %v) but v4927 vacated slot %d (globals hsize=%d); "+
			"the pair in test/regression/fuzz_260_test.go no longer reuses the slot — search for a new (victim, intruder) pair",
			idx1, ok, idx0, object.TableHSize(st.Arena(), st.Globals()))
	}
}
