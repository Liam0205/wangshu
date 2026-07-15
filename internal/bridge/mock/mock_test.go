// Mock subpackage smoke tests — verify the three mock behavior variants do
// drive the Bridge state machine down the corresponding paths.
package mock

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/bridge"
	"github.com/Liam0205/wangshu/internal/bytecode"
)

func makeProto() *bytecode.Proto {
	// Code length ≥ MinPromotableCodeLen=10 (issue #21): the mock test drives
	// the natural heat path, short protos are blocked by the guard. The
	// specific opcode values are irrelevant (the mock P3 compiler does not
	// parse proto.Code), only the length needs to be enough to pass
	// MinPromotableCodeLen.
	code := make([]bytecode.Instruction, bridge.MinPromotableCodeLen)
	for i := range code {
		code[i] = bytecode.Instruction(uint32(bytecode.ADD))
	}
	return &bytecode.Proto{
		Code: code,
		IC:   make([]bytecode.ICSlot, bridge.MinPromotableCodeLen),
	}
}

// TestMock_DummyCompile_PromotesToGibbous DummyCompile + Compilable → Gibbous.
func TestMock_DummyCompile_PromotesToGibbous(t *testing.T) {
	b := bridge.NewBridge()
	b.SetP3Compiler(DummyCompile{})
	p := makeProto()
	pd := b.ProfileOf(p)
	pd.Compilable = bridge.CompCompilable

	for i := uint32(0); i < bridge.HotEntryThreshold; i++ {
		b.OnEnter(p, true)
	}
	if pd.TierState != bridge.TierGibbous {
		t.Errorf("DummyCompile should promote to Gibbous, got %v", pd.TierState)
	}
}

// TestMock_RejectAll_F7Stuck RejectAll + AnalyzeProto → F7 blocks, Stuck.
func TestMock_RejectAll_F7Stuck(t *testing.T) {
	b := bridge.NewBridge()
	b.SetP3Compiler(RejectAll{})
	p := makeProto()
	// Simulate PB3 already analyzed (any Proto, even if it passes F1-F6, is
	// still blocked by F7)
	b.SetCompilability(p, bridge.CompNotCompilable, bridge.ReasonBackendUnsupp)

	pd := b.ProfileOf(p)
	for i := uint32(0); i < bridge.HotEntryThreshold; i++ {
		b.OnEnter(p, true)
	}
	if pd.TierState != bridge.TierStuck {
		t.Errorf("RejectAll should leave Proto in Stuck, got %v", pd.TierState)
	}
}

// TestMock_PanicOnce_RecoveredToStuck PanicOnce → defer recover turns to Stuck,
// panic does not escape.
func TestMock_PanicOnce_RecoveredToStuck(t *testing.T) {
	b := bridge.NewBridge()
	b.SetP3Compiler(PanicOnce{})
	p := makeProto()
	pd := b.ProfileOf(p)
	pd.Compilable = bridge.CompCompilable

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("panic must not escape, got %v", r)
		}
	}()

	for i := uint32(0); i < bridge.HotEntryThreshold; i++ {
		b.OnEnter(p, true)
	}
	if pd.TierState != bridge.TierStuck {
		t.Errorf("PanicOnce should leave Proto in Stuck, got %v", pd.TierState)
	}
}
