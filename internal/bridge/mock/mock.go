// Package mock provides P3Compiler test doubles for P2 bridge users
// (`docs/design/p2-bridge/05-p3-p4-interface.md` §7 + 06 §11.1 RB-T4).
//
// Three mock behavior variants (covering the PB7 end-to-end test matrix):
//
//   - DummyCompile: SupportsAllOpcodes=true / Compile always succeeds
//     (TierGibbous path acceptance)
//   - RejectAll:    SupportsAllOpcodes=false (F7 always fires, every Proto
//     stays interpreted forever, equivalent to P1-only behavior)
//   - PanicOnce:    Compile panics on the first call, never fires again (tests
//     the defer recover fallback + the Stuck no-retry discipline)
//
// Why the sub-package export: the bridge main package's _test.go already has
// the same mocks (state_machine_test and std_logger_test), but the wangshu
// main package's e2e tests need them too — cross-package sharing goes through a
// shared export (internal/bridge/mock); the package path stays under internal
// so the public surface is unaffected.
package mock

import (
	"github.com/Liam0205/wangshu/internal/bridge"
	"github.com/Liam0205/wangshu/internal/bytecode"
)

// DummyCompile P3: SupportsAllOpcodes=true, Compile always succeeds producing an empty GibbousCode.
type DummyCompile struct{}

func (DummyCompile) SupportsAllOpcodes(_ *bytecode.Proto) bool { return true }
func (DummyCompile) Compile(p *bytecode.Proto, _ *bridge.TypeFeedback) (bridge.GibbousCode, error) {
	return dummyCode{proto: p}, nil
}

// RejectAll P3: SupportsAllOpcodes=false, F7-rejects any Proto.
//
// Semantically equivalent to P3 not yet being injected (b.p3 == nil), but
// injecting this mock explicitly is clearer in tests (avoids conflating the
// no-P3 F7 special path with an explicit "not supported").
type RejectAll struct{}

func (RejectAll) SupportsAllOpcodes(_ *bytecode.Proto) bool { return false }
func (RejectAll) Compile(_ *bytecode.Proto, _ *bridge.TypeFeedback) (bridge.GibbousCode, error) {
	// When SupportsAllOpcodes=false, F7 should already have stopped this at
	// the AnalyzeProto stage — this path is theoretically unreachable.
	// Return err defensively.
	return nil, &bridge.CompileError{
		Kind:   bridge.CompileErrBackendDeclined,
		Reason: "RejectAll mock: SupportsAllOpcodes=false should have stopped this",
	}
}

// PanicOnce P3: Compile panics on the first call (tests defer recover);
// subsequent Compile calls still panic (Stuck means it never fires again, so
// "whether it panics later" can't be observed directly — but the semantics
// stay consistent).
type PanicOnce struct{}

func (PanicOnce) SupportsAllOpcodes(_ *bytecode.Proto) bool { return true }
func (PanicOnce) Compile(_ *bytecode.Proto, _ *bridge.TypeFeedback) (bridge.GibbousCode, error) {
	panic("synthetic backend bug for testing")
}

// dummyCode is the minimal implementation of GibbousCode (opaque from the P2
// perspective). Run is a no-op placeholder (the mock doesn't actually execute;
// trampoline end-to-end tests use real P3 gibbous, see the wangshu_p3 build).
type dummyCode struct{ proto *bytecode.Proto }

func (d dummyCode) Proto() *bytecode.Proto         { return d.proto }
func (d dummyCode) Run(_ []uint64, _ uint32) int32 { return 0 }
func (d dummyCode) PendingErr() error              { return nil }
func (d dummyCode) Slot() (uint32, bool)           { return 0, false }
