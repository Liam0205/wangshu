// Aggregator unit tests (`docs/design/p2-bridge/02-ic-feedback.md` §6 acceptance).
//
// Three classes of synthetic ICSlot input map to three FeedbackKind outputs + Confidence computation:
//
//   - arithmetic IC dual-count: numHits=99 metaHits=1 ⇒ FBArithStableNumber, conf=0.99
//   - table IC kind=2 (node hit) ⇒ FBTableMono(GETTABLE)/FBGlobalStable
//     (GETGLOBAL)/FBSelfMono(SELF)
//   - table IC kind=4 (megamorphic) ⇒ FBTableMega
//
// Edge cases:
//   - kind=0 (unobserved) ⇒ Points[pc] is the zero value (FBUnstable, conf=0, skipped)
//   - arithmetic IC numHits+metaHits < MinObservations ⇒ FBUnstable (too few samples)
//   - ratio between 0.5..0.99 ⇒ FBUnstable (mixed state)
package bridge

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/bytecode"
)

// makeProtoWithCode builds a Proto containing specific opcodes + an equal-length IC array.
//
// **MinPromotableCodeLen padding** (issue #21): if the op count is less than
// MinPromotableCodeLen, it is automatically padded (with NOP) up to
// MinPromotableCodeLen length, so the considerPromotion path under test is not
// blocked by the guard. The first len(ops) opcodes remain the test-specified
// forms, and the IC indices correspond to those first len(ops) slots (the IC
// slots of the padding NOPs are all zero values).
func makeProtoWithCode(ops ...bytecode.OpCode) *bytecode.Proto {
	n := len(ops)
	if n < MinPromotableCodeLen {
		n = MinPromotableCodeLen
	}
	code := make([]bytecode.Instruction, n)
	for i, op := range ops {
		code[i] = bytecode.Instruction(uint32(op))
	}
	// padding slots default to 0, i.e. OpCode=MOVE (never actually decoded, only to pass the guard's length check)
	return &bytecode.Proto{
		Code: code,
		IC:   make([]bytecode.ICSlot, n),
	}
}

// TestAggregator_ArithStable: arithmetic IC ratio ≥ 0.99 + total hits ≥ 100 ⇒
// FBArithStableNumber.
func TestAggregator_ArithStable(t *testing.T) {
	p := makeProtoWithCode(bytecode.ADD, bytecode.MUL, bytecode.SUB)
	// pc=0 ADD: 99% number, 1% meta (99 + 1 = 100, boundary equal to threshold)
	p.IC[0] = bytecode.ICSlot{Shape: 990, Index: 10, Kind: 1} // 990/(990+10)=0.99
	// pc=1 MUL: all number, 200 hits
	p.IC[1] = bytecode.ICSlot{Shape: 200, Index: 0, Kind: 1}
	// pc=2 SUB: unobserved
	p.IC[2] = bytecode.ICSlot{Kind: 0}

	a := NewAggregator()
	fb := a.Aggregate(p)

	if fb.Points[0].Kind != FBArithStableNumber {
		t.Errorf("pc=0 kind = %v, want FBArithStableNumber", fb.Points[0].Kind)
	}
	if fb.Points[0].Confidence < 0.989 || fb.Points[0].Confidence > 0.991 {
		t.Errorf("pc=0 confidence = %v, want ~0.99", fb.Points[0].Confidence)
	}
	if fb.Points[0].Observations != 1000 {
		t.Errorf("pc=0 observations = %d, want 1000", fb.Points[0].Observations)
	}

	if fb.Points[1].Kind != FBArithStableNumber {
		t.Errorf("pc=1 kind = %v, want FBArithStableNumber", fb.Points[1].Kind)
	}
	if fb.Points[1].Confidence != 1.0 {
		t.Errorf("pc=1 confidence = %v, want 1.0", fb.Points[1].Confidence)
	}

	if fb.Points[2].Kind != FBUnstable || fb.Points[2].Confidence != 0 {
		t.Errorf("pc=2 should be unobserved zero-value, got %+v", fb.Points[2])
	}
}

// TestAggregator_ArithUnstable: ratio < 0.99 ⇒ FBUnstable; but confidence still
// carries the ratio value for diagnostics.
func TestAggregator_ArithUnstable(t *testing.T) {
	p := makeProtoWithCode(bytecode.ADD, bytecode.MUL)
	// pc=0 70% number 30% meta (mixed state)
	p.IC[0] = bytecode.ICSlot{Shape: 700, Index: 300, Kind: 1}
	// pc=1 50/50 (diagnostic ratio)
	p.IC[1] = bytecode.ICSlot{Shape: 500, Index: 500, Kind: 1}

	a := NewAggregator()
	fb := a.Aggregate(p)

	if fb.Points[0].Kind != FBUnstable {
		t.Errorf("pc=0 kind = %v, want FBUnstable (ratio 0.70 < 0.99)", fb.Points[0].Kind)
	}
	if fb.Points[0].Confidence < 0.69 || fb.Points[0].Confidence > 0.71 {
		t.Errorf("pc=0 confidence = %v, want ~0.70 diagnostic", fb.Points[0].Confidence)
	}
	if fb.Points[1].Kind != FBUnstable {
		t.Errorf("pc=1 kind = %v, want FBUnstable", fb.Points[1].Kind)
	}
	if fb.Points[1].Confidence < 0.49 || fb.Points[1].Confidence > 0.51 {
		t.Errorf("pc=1 confidence = %v, want ~0.5", fb.Points[1].Confidence)
	}
}

// TestAggregator_ArithSampleTooFew: sample count < MinObservations (100) ⇒ FBUnstable.
func TestAggregator_ArithSampleTooFew(t *testing.T) {
	p := makeProtoWithCode(bytecode.ADD)
	// 99 numHits 0 metaHits = ratio 1.0 but sample count < 100
	p.IC[0] = bytecode.ICSlot{Shape: 99, Index: 0, Kind: 1}

	a := NewAggregator()
	fb := a.Aggregate(p)

	if fb.Points[0].Kind != FBUnstable {
		t.Errorf("kind = %v, want FBUnstable for under-min samples", fb.Points[0].Kind)
	}
	if fb.Points[0].Confidence != 0 {
		t.Errorf("confidence = %v, want 0 for sample-too-few", fb.Points[0].Confidence)
	}
	if fb.Points[0].Observations != 99 {
		t.Errorf("observations = %d, want 99", fb.Points[0].Observations)
	}
}

// TestAggregator_TableIC: table IC kind 2 (node hit) → FBTableMono(GETTABLE)/
// FBGlobalStable(GETGLOBAL)/FBSelfMono(SELF).
func TestAggregator_TableIC(t *testing.T) {
	p := makeProtoWithCode(bytecode.GETTABLE, bytecode.GETGLOBAL, bytecode.SELF, bytecode.SETTABLE, bytecode.SETGLOBAL)
	// set node hit on every pc
	for i := range p.IC {
		p.IC[i] = bytecode.ICSlot{
			Shape:    42,
			Index:    7,
			TableRef: 0xdeadbeef,
			Kind:     bytecode.ICKindNodeHit,
		}
	}

	a := NewAggregator()
	fb := a.Aggregate(p)

	cases := []struct {
		pc   int
		want FeedbackKind
	}{
		{0, FBTableMono},    // GETTABLE
		{1, FBGlobalStable}, // GETGLOBAL
		{2, FBSelfMono},     // SELF
		{3, FBTableMono},    // SETTABLE
		{4, FBGlobalStable}, // SETGLOBAL
	}
	for _, c := range cases {
		got := fb.Points[c.pc]
		if got.Kind != c.want {
			t.Errorf("pc=%d kind = %v, want %v", c.pc, got.Kind, c.want)
		}
		if got.Confidence != 1.0 {
			t.Errorf("pc=%d confidence = %v, want 1.0 (mono IC)", c.pc, got.Confidence)
		}
		if got.StableShape != 42 || got.StableIndex != 7 {
			t.Errorf("pc=%d stable shape/index = %d/%d, want 42/7",
				c.pc, got.StableShape, got.StableIndex)
		}
	}
}

// TestAggregator_TableMega kind=4 megamorphic → FBTableMega + confidence 0 +
// stable shape/index cleared to 0 (02 §6.3 defensive translation, P1 does not currently write kind=4).
func TestAggregator_TableMega(t *testing.T) {
	p := makeProtoWithCode(bytecode.GETTABLE)
	p.IC[0] = bytecode.ICSlot{
		Shape:    99,
		Index:    7,
		TableRef: 0xcafe,
		Kind:     bytecode.ICKindMegamorphic,
	}

	a := NewAggregator()
	fb := a.Aggregate(p)
	got := fb.Points[0]

	if got.Kind != FBTableMega {
		t.Errorf("kind = %v, want FBTableMega", got.Kind)
	}
	if got.Confidence != 0 {
		t.Errorf("confidence = %v, want 0 for mega", got.Confidence)
	}
	if got.StableShape != 0 || got.StableIndex != 0 {
		t.Errorf("stable shape/index should be cleared for mega")
	}
}

// TestAggregator_RefillTriggersMega P2+ #4: when Refill ≥ MegamorphicRefillThreshold
// (default 3), proactively translate to FBTableMega even if Kind is still mono.
func TestAggregator_RefillTriggersMega(t *testing.T) {
	p := makeProtoWithCode(bytecode.GETTABLE)
	// Kind=NodeHit looks monomorphic, but Refill=5 means it was refilled many times historically (polymorphic)
	p.IC[0] = bytecode.ICSlot{
		Shape:    42,
		Index:    7,
		TableRef: 0xdeadbeef,
		Kind:     bytecode.ICKindNodeHit,
		Refill:   5,
	}

	a := NewAggregator()
	fb := a.Aggregate(p)
	got := fb.Points[0]
	if got.Kind != FBTableMega {
		t.Errorf("Refill=5 should trigger FBTableMega, got %v", got.Kind)
	}
	if got.StableShape != 0 || got.StableIndex != 0 {
		t.Errorf("stable shape/index should be cleared on Refill-mega")
	}
}

// TestAggregator_RefillBelowThresholdStillMono: when Refill < threshold, still judge mono
// by kind (a monomorphic hit may occasionally refill but does not count as polymorphic).
func TestAggregator_RefillBelowThresholdStillMono(t *testing.T) {
	p := makeProtoWithCode(bytecode.GETTABLE)
	p.IC[0] = bytecode.ICSlot{
		Shape:    42,
		Index:    7,
		TableRef: 0xdeadbeef,
		Kind:     bytecode.ICKindNodeHit,
		Refill:   2, // < 3 threshold
	}

	a := NewAggregator()
	fb := a.Aggregate(p)
	got := fb.Points[0]
	if got.Kind != FBTableMono {
		t.Errorf("Refill=2 (<threshold) should stay FBTableMono, got %v", got.Kind)
	}
}

// TestAggregator_NonICOpsAreUnstable: slots for non-IC instructions are the FBUnstable zero value
// (LOADK/MOVE/RETURN/...) — P3/P4 should skip them.
func TestAggregator_NonICOpsAreUnstable(t *testing.T) {
	p := makeProtoWithCode(bytecode.LOADK, bytecode.MOVE, bytecode.RETURN)

	a := NewAggregator()
	fb := a.Aggregate(p)

	for i := 0; i < 3; i++ {
		if fb.Points[i].Kind != FBUnstable || fb.Points[i].Confidence != 0 {
			t.Errorf("pc=%d non-IC op should be FBUnstable zero, got %+v",
				i, fb.Points[i])
		}
	}
}

// TestAggregator_GenerationMonotonic: multiple Aggregate calls on the same Proto give a
// monotonically increasing generation — P3/P4 uses this to tell whether a feedback snapshot is stale.
func TestAggregator_GenerationMonotonic(t *testing.T) {
	p := makeProtoWithCode(bytecode.ADD)
	p.IC[0] = bytecode.ICSlot{Shape: 200, Index: 0, Kind: 1}

	a := NewAggregator()
	fb1 := a.Aggregate(p)
	fb2 := a.Aggregate(p)

	if fb2.Generation <= fb1.Generation {
		t.Errorf("generation not monotonic: fb1=%d fb2=%d", fb1.Generation, fb2.Generation)
	}
}

// TestBridgeInstallFeedback: per-State Feedback is installed once (02 §4.5 no-re-aggregation
// strategy) — calling installFeedback multiple times does not overwrite.
func TestBridgeInstallFeedback(t *testing.T) {
	b := NewBridge()
	p := makeProtoWithCode(bytecode.ADD)
	p.IC[0] = bytecode.ICSlot{Shape: 200, Index: 0, Kind: 1}

	fb1 := b.Aggregator().Aggregate(p)
	b.installFeedback(p, fb1)

	pd := b.ProfileOf(p)
	if pd.Feedback != fb1 {
		t.Errorf("Feedback not installed (got %p, want %p)", pd.Feedback, fb1)
	}

	// the second install should be ignored (the initial version aggregates only once)
	fb2 := b.Aggregator().Aggregate(p)
	b.installFeedback(p, fb2)
	if pd.Feedback != fb1 {
		t.Errorf("Feedback overwritten on second install (got %p, want %p first)",
			pd.Feedback, fb1)
	}
}
