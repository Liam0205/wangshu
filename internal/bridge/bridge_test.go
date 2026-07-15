// Bridge package skeleton smoke tests (PB0) — verify type definitions, Bridge
// construction, no-op hook points, lazy profileTable population, and the
// zero-allocation steady state.
//
// **This test does not verify state-machine transitions** (that comes after
// PB4 lands) — in the PB0 phase considerPromotion is a no-op placeholder, so
// crossing any threshold does not actually promote a tier.
package bridge

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/bytecode"
)

// TestEnumStrings pins the String() output format — tier-promotion logs
// (04 §6) and diagnostic tools both depend on these strings, so they must not
// drift as the implementation evolves.
func TestEnumStrings(t *testing.T) {
	t.Helper()
	cases := []struct {
		got, want string
	}{
		{TierInterp.String(), "interp"},
		{TierGibbous.String(), "gibbous"},
		{TierStuck.String(), "stuck"},

		{CompUnknown.String(), "Unknown"},
		{CompCompilable.String(), "Compilable"},
		{CompNotCompilable.String(), "NotCompilable"},

		{FBUnstable.String(), "Unstable"},
		{FBArithStableNumber.String(), "ArithStableNumber"},
		{FBTableMono.String(), "TableMono"},
		{FBTableMega.String(), "TableMega"},
		{FBGlobalStable.String(), "GlobalStable"},
		{FBSelfMono.String(), "SelfMono"},

		{CompileErrUnsupportedOpcodeShape.String(), "unsupported_opcode_shape"},
		{CompileErrOutOfResources.String(), "out_of_resources"},
		{CompileErrBackendPanic.String(), "backend_panic"},
		{CompileErrBackendDeclined.String(), "backend_declined"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("String mismatch: got %q want %q", c.got, c.want)
		}
	}
}

// TestProfileDataZeroValue pins that the Go zero value is exactly TierInterp +
// CompUnknown (01 §6.5, the cornerstone of lazy profileTable population —
// `pd := &ProfileData{}` is a valid starting point with no explicit set
// needed).
func TestProfileDataZeroValue(t *testing.T) {
	pd := &ProfileData{}
	if pd.TierState != TierInterp {
		t.Errorf("zero TierState = %v, want TierInterp", pd.TierState)
	}
	if pd.Compilable != CompUnknown {
		t.Errorf("zero Compilable = %v, want CompUnknown", pd.Compilable)
	}
	if pd.EntryCount != 0 || pd.BackEdge != nil {
		t.Errorf("zero counters not clean: entry=%d backEdge=%v", pd.EntryCount, pd.BackEdge)
	}
	if pd.Reasons.HasAny() {
		t.Errorf("zero Reasons should not have any bits set")
	}
}

// TestBridgeProfileOfLazy verifies lazy profileTable population (repeated
// ProfileOf on the same Proto must return the same pointer; different Protos
// get different pds).
func TestBridgeProfileOfLazy(t *testing.T) {
	b := NewBridge()
	p1 := &bytecode.Proto{Code: make([]bytecode.Instruction, 4)}
	p2 := &bytecode.Proto{Code: make([]bytecode.Instruction, 8)}

	pd1a := b.ProfileOf(p1)
	pd1b := b.ProfileOf(p1)
	if pd1a != pd1b {
		t.Error("ProfileOf must return same pointer for same Proto")
	}
	pd2 := b.ProfileOf(p2)
	if pd2 == pd1a {
		t.Error("ProfileOf must return distinct pointers for distinct Protos")
	}
}

// TestOnBackEdgeAccumulates verifies that the back-edge counter increments and
// that no tier promotion is triggered before the threshold.
//
// **PB0 has no real promotion** — the considerPromotion that OnBackEdge calls
// after crossing the threshold is a no-op, so TierState stays TierInterp. This
// test pins the PB0 placeholder semantics; it will be strengthened after PB4
// lands (at which point TierState should transition to TierStuck:
// Compilable=CompUnknown is treated as CompNotCompilable, 03 §5.5).
func TestOnBackEdgeAccumulates(t *testing.T) {
	b := NewBridge()
	p := &bytecode.Proto{Code: make([]bytecode.Instruction, 8)}

	for i := uint32(0); i < 5; i++ {
		b.OnBackEdge(p, 3, true)
	}
	pd := b.ProfileOf(p)
	if pd.BackEdge[3] != 5 {
		t.Errorf("backEdge[3] = %d, want 5", pd.BackEdge[3])
	}
	if pd.TierState != TierInterp {
		t.Errorf("TierState = %v, want TierInterp (PB0 no-op)", pd.TierState)
	}
}

// TestOnEnterAccumulates increments the function-entry counter.
func TestOnEnterAccumulates(t *testing.T) {
	b := NewBridge()
	p := &bytecode.Proto{Code: make([]bytecode.Instruction, 4)}

	for i := 0; i < 3; i++ {
		b.OnEnter(p, true)
	}
	pd := b.ProfileOf(p)
	if pd.EntryCount != 3 {
		t.Errorf("entryCount = %d, want 3", pd.EntryCount)
	}
}

// TestTierGuardBlocksCounting verifies that when TierState != TierInterp,
// onBackEdge / onEnter return immediately (01 §4.1 guard) — a Proto already
// promoted to Gibbous or stuck at Stuck should no longer accumulate counts.
func TestTierGuardBlocksCounting(t *testing.T) {
	b := NewBridge()
	p := &bytecode.Proto{Code: make([]bytecode.Instruction, 4)}
	pd := b.ProfileOf(p)

	pd.TierState = TierStuck
	b.OnBackEdge(p, 0, true)
	b.OnEnter(p, true)

	if pd.EntryCount != 0 {
		t.Errorf("entryCount must stay 0 under TierStuck guard, got %d", pd.EntryCount)
	}
	if pd.BackEdge != nil {
		t.Errorf("backEdge must remain nil under TierStuck guard, got %v", pd.BackEdge)
	}
}

// TestSetCompilability pins the write-once, read-only-at-runtime semantics
// (03 §5.4).
func TestSetCompilability(t *testing.T) {
	b := NewBridge()
	p := &bytecode.Proto{Code: make([]bytecode.Instruction, 4)}

	if got := b.CompilabilityOf(p); got != CompUnknown {
		t.Errorf("initial CompilabilityOf = %v, want CompUnknown", got)
	}
	b.SetCompilability(p, CompNotCompilable, ReasonVararg|ReasonOverSize)
	if got := b.CompilabilityOf(p); got != CompNotCompilable {
		t.Errorf("CompilabilityOf after set = %v, want CompNotCompilable", got)
	}
	pd := b.ProfileOf(p)
	if !pd.Reasons.HasAny() {
		t.Errorf("Reasons should have bits set after SetCompilability")
	}
}

// TestProfileDataMaxBackEdge verifies that MaxBackEdge returns the maximum
// per-back-edge count (used by the diagnostic log "N accumulated back edges",
// 01 §2.5 (a) which keeps backEdge after promotion).
func TestProfileDataMaxBackEdge(t *testing.T) {
	pd := &ProfileData{BackEdge: []uint32{3, 17, 5, 9}}
	if got := pd.MaxBackEdge(); got != 17 {
		t.Errorf("MaxBackEdge = %d, want 17", got)
	}

	emptyPd := &ProfileData{}
	if got := emptyPd.MaxBackEdge(); got != 0 {
		t.Errorf("empty MaxBackEdge = %d, want 0", got)
	}
}
