// Short-proto floor exemption tests (issue #67, bridge.FloorExempter).
//
// The floor (issue #21) blocks below-floor protos from auto-mode
// promotion because promoted tiny protos lose to the backend's fixed
// per-call dispatch costs. FloorExempter lets a backend lift the floor
// for protos whose hot dispatch channel skips those costs (P4: seg2seg
// callees). These tests drive the bridge side with a fake backend:
//
//   - exempt below-floor proto promotes on the natural heat path
//   - non-exempt below-floor proto stays floored (never promotes)
//   - the verdict is consulted exactly once per proto (cached), and
//     only after the heat threshold
//   - a backend without the interface keeps the historical behavior
package bridge

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/bytecode"
)

// makeShortProto builds a proto strictly below the default floor,
// WITHOUT the makeProtoWithCode padding (that helper exists to dodge
// the floor; here the floor is the subject under test).
func makeShortProto() *bytecode.Proto {
	n := MinPromotableCodeLen - 1
	code := make([]bytecode.Instruction, n)
	for i := range code {
		code[i] = bytecode.Instruction(uint32(bytecode.ADD))
	}
	return &bytecode.Proto{Code: code, IC: make([]bytecode.ICSlot, n)}
}

// exemptingP3: Compile always succeeds; ExemptFromFloor returns a
// configurable verdict and counts consultations.
type exemptingP3 struct {
	dummyCompileP3
	exempt      bool
	exemptCalls int
}

func (e *exemptingP3) ExemptFromFloor(_ *bytecode.Proto) bool {
	e.exemptCalls++
	return e.exempt
}

// TestFloorExempt_BelowFloorPromotes: an exempt below-floor proto
// promotes via the natural entry-heat path.
func TestFloorExempt_BelowFloorPromotes(t *testing.T) {
	b := NewBridge()
	mock := &exemptingP3{exempt: true}
	b.SetP3Compiler(mock)
	p := makeShortProto()
	pd := b.ProfileOf(p)
	pd.Compilable = CompCompilable

	for i := uint32(0); i < HotEntryThreshold; i++ {
		b.OnEnter(p, true)
	}

	if pd.TierState != TierGibbous {
		t.Fatalf("exempt below-floor proto: TierState = %v, want TierGibbous", pd.TierState)
	}
	if mock.exemptCalls != 1 {
		t.Errorf("ExemptFromFloor consulted %d times, want exactly 1 (cached verdict)",
			mock.exemptCalls)
	}
}

// TestFloorExempt_NotExemptStaysFloored: a non-exempt below-floor proto
// keeps the issue-#21 behavior — stays TierInterp, never compiles. The
// verdict is cached, so hammering entries re-consults nothing.
func TestFloorExempt_NotExemptStaysFloored(t *testing.T) {
	b := NewBridge()
	mock := &exemptingP3{exempt: false}
	b.SetP3Compiler(mock)
	p := makeShortProto()
	pd := b.ProfileOf(p)
	pd.Compilable = CompCompilable

	for i := uint32(0); i < HotEntryThreshold*3; i++ {
		b.OnEnter(p, true)
	}

	if pd.TierState != TierInterp {
		t.Fatalf("floored proto: TierState = %v, want TierInterp", pd.TierState)
	}
	if _, ok := b.gibbousCodes[p]; ok {
		t.Error("floored proto must not be compiled/installed")
	}
	if mock.exemptCalls != 1 {
		t.Errorf("ExemptFromFloor consulted %d times, want exactly 1 (cached verdict)",
			mock.exemptCalls)
	}
}

// TestFloorExempt_NotConsultedBeforeThreshold: the exemption is only
// asked once a below-floor proto actually crosses the heat threshold —
// cold protos never pay the eligibility scan.
func TestFloorExempt_NotConsultedBeforeThreshold(t *testing.T) {
	b := NewBridge()
	mock := &exemptingP3{exempt: true}
	b.SetP3Compiler(mock)
	p := makeShortProto()
	pd := b.ProfileOf(p)
	pd.Compilable = CompCompilable

	for i := uint32(0); i < HotEntryThreshold-1; i++ {
		b.OnEnter(p, true)
	}
	if mock.exemptCalls != 0 {
		t.Errorf("ExemptFromFloor consulted %d times below the heat threshold, want 0",
			mock.exemptCalls)
	}
}

// TestFloorExempt_AtOrAboveFloorNotConsulted: protos at/above the floor
// never consult the exemption — the floor doesn't block them at all.
func TestFloorExempt_AtOrAboveFloorNotConsulted(t *testing.T) {
	b := NewBridge()
	mock := &exemptingP3{exempt: false}
	b.SetP3Compiler(mock)
	p := makeProtoWithCode(bytecode.ADD) // padded to MinPromotableCodeLen
	pd := b.ProfileOf(p)
	pd.Compilable = CompCompilable

	for i := uint32(0); i < HotEntryThreshold; i++ {
		b.OnEnter(p, true)
	}

	if pd.TierState != TierGibbous {
		t.Fatalf("at-floor proto: TierState = %v, want TierGibbous", pd.TierState)
	}
	if mock.exemptCalls != 0 {
		t.Errorf("ExemptFromFloor consulted %d times for an at-floor proto, want 0",
			mock.exemptCalls)
	}
}

// TestFloorExempt_BackEdgePathAlsoExempts: the exemption applies on the
// back-edge heat path too (loop-heavy tiny protos).
func TestFloorExempt_BackEdgePathAlsoExempts(t *testing.T) {
	b := NewBridge()
	mock := &exemptingP3{exempt: true}
	b.SetP3Compiler(mock)
	p := makeShortProto()
	pd := b.ProfileOf(p)
	pd.Compilable = CompCompilable

	for i := uint32(0); i < HotBackEdgeThreshold; i++ {
		b.OnBackEdge(p, 0, true)
	}

	if pd.TierState != TierGibbous {
		t.Fatalf("exempt below-floor proto (back-edge path): TierState = %v, want TierGibbous",
			pd.TierState)
	}
}

// TestFloorExempt_NoInterfaceKeepsFloor: a backend that does not
// implement FloorExempter keeps the historical issue-#21 behavior.
func TestFloorExempt_NoInterfaceKeepsFloor(t *testing.T) {
	b := NewBridge()
	b.SetP3Compiler(dummyCompileP3{})
	p := makeShortProto()
	pd := b.ProfileOf(p)
	pd.Compilable = CompCompilable

	for i := uint32(0); i < HotEntryThreshold; i++ {
		b.OnEnter(p, true)
	}

	if pd.TierState != TierInterp {
		t.Fatalf("no-FloorExempter backend: TierState = %v, want TierInterp (floored)",
			pd.TierState)
	}
}
