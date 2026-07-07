// SetHotThresholds override tests (auto-mode coverage testing knob).
package bridge

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/bytecode"
)

// TestSetHotThresholds_Override: the testing-only threshold override
// moves WHEN the auto decision runs. Lowered entry threshold promotes
// after that many entries; raised-to-max thresholds never promote.
func TestSetHotThresholds_Override(t *testing.T) {
	b := NewBridge()
	b.SetP3Compiler(dummyCompileP3{})
	b.SetHotThresholds(3, 0)
	p := makeProtoWithCode(bytecode.ADD)
	pd := b.ProfileOf(p)
	pd.Compilable = CompCompilable

	b.OnEnter(p, true)
	b.OnEnter(p, true)
	if pd.TierState != TierInterp {
		t.Fatalf("below lowered threshold: TierState = %v, want TierInterp", pd.TierState)
	}
	b.OnEnter(p, true)
	if pd.TierState != TierGibbous {
		t.Fatalf("at lowered threshold: TierState = %v, want TierGibbous", pd.TierState)
	}

	// Raised to max: promotion unreachable.
	b2 := NewBridge()
	b2.SetP3Compiler(dummyCompileP3{})
	b2.SetHotThresholds(^uint32(0), ^uint32(0))
	p2 := makeProtoWithCode(bytecode.ADD)
	pd2 := b2.ProfileOf(p2)
	pd2.Compilable = CompCompilable
	for i := uint32(0); i < HotEntryThreshold*2; i++ {
		b2.OnEnter(p2, true)
	}
	if pd2.TierState != TierInterp {
		t.Fatalf("max thresholds: TierState = %v, want TierInterp (never promotes)", pd2.TierState)
	}

	// Zero keeps current values (no accidental reset to 0 == promote-always).
	b3 := NewBridge()
	b3.SetP3Compiler(dummyCompileP3{})
	b3.SetHotThresholds(0, 0)
	p3 := makeProtoWithCode(bytecode.ADD)
	pd3 := b3.ProfileOf(p3)
	pd3.Compilable = CompCompilable
	b3.OnEnter(p3, true)
	if pd3.TierState != TierInterp {
		t.Fatalf("SetHotThresholds(0,0) must keep defaults; TierState = %v after 1 entry", pd3.TierState)
	}
}
