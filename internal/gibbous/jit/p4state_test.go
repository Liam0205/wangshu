//go:build wangshu_p4

package jit

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/bytecode"
)

// p4state_test.go — unit tests for the P4 internal speculation sub-state machine
// (follows docs/design/p4-method-jit/04-osr-deopt.md §5 state-transition table).
//
// This batch lands the engineering groundwork for the OSR exit protocol (the
// p4SpecState map + DeoptThreshold / MaxRecompileTries threshold placeholders +
// onOSRExit / onP4Install transition functions + probes); it activates once the
// in-segment EmitCallInline speculative template is actually wired in.

// TestP4SpecState_DefaultIsUnknown: an unregistered Proto defaults to P4SpecUnknown.
func TestP4SpecState_DefaultIsUnknown(t *testing.T) {
	ResetP4SpecState()
	proto := &bytecode.Proto{}
	if got := P4SpecStateOf(proto); got != P4SpecUnknown {
		t.Errorf("默认状态 = %v, want P4SpecUnknown", got)
	}
}

// TestP4SpecState_NilProtoSafe: a nil Proto safely returns P4SpecUnknown without panicking.
func TestP4SpecState_NilProtoSafe(t *testing.T) {
	ResetP4SpecState()
	if got := P4SpecStateOf(nil); got != P4SpecUnknown {
		t.Errorf("nil proto = %v, want P4SpecUnknown", got)
	}
	onOSRExit(nil) // does not panic
	onP4Install(nil)
}

// TestP4SpecState_InstallTransitions onP4Install: first install → P4Speculative.
func TestP4SpecState_InstallTransitions(t *testing.T) {
	ResetP4SpecState()
	proto := &bytecode.Proto{}
	onP4Install(proto)
	if got := P4SpecStateOf(proto); got != P4Speculative {
		t.Errorf("首次 install 后状态 = %v, want P4Speculative", got)
	}
}

// TestP4SpecState_DeoptCountUnderThreshold: a single OSR exit does not switch state.
func TestP4SpecState_DeoptCountUnderThreshold(t *testing.T) {
	ResetP4SpecState()
	ResetSpecHits()
	proto := &bytecode.Proto{}
	onP4Install(proto)
	for i := 0; i < int(DeoptThreshold-1); i++ {
		onOSRExit(proto)
	}
	if got := P4SpecStateOf(proto); got != P4Speculative {
		t.Errorf("deoptCount < 阈值时状态 = %v, want P4Speculative", got)
	}
	if SpecP4DeoptHits() != 0 {
		t.Errorf("阈值前 SpecP4DeoptHits = %d, want 0", SpecP4DeoptHits())
	}
}

// TestP4SpecState_DeoptCountReachThreshold: reaching DeoptThreshold → P4Deoptimized.
func TestP4SpecState_DeoptCountReachThreshold(t *testing.T) {
	ResetP4SpecState()
	ResetSpecHits()
	proto := &bytecode.Proto{}
	onP4Install(proto)
	for i := 0; i < int(DeoptThreshold); i++ {
		onOSRExit(proto)
	}
	if got := P4SpecStateOf(proto); got != P4Deoptimized {
		t.Errorf("达阈值后状态 = %v, want P4Deoptimized", got)
	}
	if SpecP4DeoptHits() != 1 {
		t.Errorf("SpecP4DeoptHits = %d, want 1", SpecP4DeoptHits())
	}
}

// TestP4SpecState_RecompileTransitions P4Deoptimized → P4Speculative
// (recompile, recompileCount += 1).
func TestP4SpecState_RecompileTransitions(t *testing.T) {
	ResetP4SpecState()
	ResetSpecHits()
	proto := &bytecode.Proto{}
	onP4Install(proto)
	// trigger deopt
	for i := 0; i < int(DeoptThreshold); i++ {
		onOSRExit(proto)
	}
	if got := P4SpecStateOf(proto); got != P4Deoptimized {
		t.Fatalf("应已 P4Deoptimized,got %v", got)
	}
	// recompile
	onP4Install(proto)
	if got := P4SpecStateOf(proto); got != P4Speculative {
		t.Errorf("重编译后状态 = %v, want P4Speculative", got)
	}
}

// TestP4SpecState_MaxRecompileTriesReachedStuck: recompile count hits the cap → P4StuckSpeculation.
func TestP4SpecState_MaxRecompileTriesReachedStuck(t *testing.T) {
	ResetP4SpecState()
	ResetSpecHits()
	proto := &bytecode.Proto{}
	onP4Install(proto)
	// loop: trigger deopt + recompile MaxRecompileTries times
	for r := uint32(0); r < MaxRecompileTries; r++ {
		for i := 0; i < int(DeoptThreshold); i++ {
			onOSRExit(proto)
		}
		if got := P4SpecStateOf(proto); got != P4Deoptimized {
			t.Fatalf("第 %d 次 deopt 后应 P4Deoptimized,got %v", r, got)
		}
		onP4Install(proto) // recompile
	}
	// deopt again: recompileCount has reached MaxRecompileTries, so this batch of deopt switches to P4StuckSpeculation
	for i := 0; i < int(DeoptThreshold); i++ {
		onOSRExit(proto)
	}
	if got := P4SpecStateOf(proto); got != P4StuckSpeculation {
		t.Errorf("达 MaxRecompileTries 后状态 = %v, want P4StuckSpeculation", got)
	}
	if SpecP4StuckHits() != 1 {
		t.Errorf("SpecP4StuckHits = %d, want 1", SpecP4StuckHits())
	}
}
