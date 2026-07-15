// State machine tests (`docs/design/p2-bridge/04-try-compile-fallback.md` §2-§5).
//
// Validates the four considerPromotion paths + the one-way TierState + absorbing states:
//
//	(P1) already-absorbed state → no-op (debounce)
//	(P2) CompUnknown / CompNotCompilable → TierStuck
//	(P3) Compilable + try-compile succeeds → TierGibbous
//	(P3-fail) Compilable + try-compile err → TierStuck
//
// **Zero-deopt invariant**: after any transition, the state never returns to
// TierInterp (formalized in 04 §2.4).
package bridge

import (
	"errors"
	"testing"

	"github.com/Liam0205/wangshu/internal/bytecode"
)

// TestStateMachine_NotCompilable_Stuck (P2) path: CompNotCompilable → Stuck.
func TestStateMachine_NotCompilable_Stuck(t *testing.T) {
	b := NewBridge()
	p := makeProtoWithCode(bytecode.ADD)
	pd := b.ProfileOf(p)

	// Simulate PB3 analysis having judged NotCompilable
	pd.Compilable = CompNotCompilable
	pd.Reasons = ReasonVararg

	// Trigger a threshold-crossing considerPromotion
	for i := uint32(0); i < HotEntryThreshold; i++ {
		b.OnEnter(p, true)
	}

	if pd.TierState != TierStuck {
		t.Errorf("TierState = %v, want TierStuck after NotCompilable promotion", pd.TierState)
	}
	if !pd.CompileTried {
		t.Errorf("CompileTried should be true after Stuck transition")
	}
}

// TestStateMachine_Unknown_Stuck CompUnknown is treated as NotCompilable (03 §5.5
// + 04 §3.2) → Stuck. This is the P1-only build degradation fallback.
func TestStateMachine_Unknown_Stuck(t *testing.T) {
	b := NewBridge()
	p := makeProtoWithCode(bytecode.ADD)
	pd := b.ProfileOf(p)
	// default CompUnknown (P1 placeholder)

	for i := uint32(0); i < HotEntryThreshold; i++ {
		b.OnEnter(p, true)
	}

	if pd.TierState != TierStuck {
		t.Errorf("CompUnknown should also transition to TierStuck, got %v", pd.TierState)
	}
}

// TestStateMachine_Compilable_Promoted (P3-success) path: Compilable +
// compile succeeds → TierGibbous.
func TestStateMachine_Compilable_Promoted(t *testing.T) {
	b := NewBridge()
	b.SetP3Compiler(dummyCompileP3{})
	p := makeProtoWithCode(bytecode.ADD)
	pd := b.ProfileOf(p)
	pd.Compilable = CompCompilable

	for i := uint32(0); i < HotEntryThreshold; i++ {
		b.OnEnter(p, true)
	}

	if pd.TierState != TierGibbous {
		t.Errorf("Compilable + success → TierGibbous, got %v", pd.TierState)
	}
	if pd.Feedback == nil {
		t.Errorf("Feedback should be populated on promotion path")
	}
	if _, ok := b.gibbousCodes[p]; !ok {
		t.Errorf("gibbousCodes should have entry for promoted Proto")
	}
}

// TestStateMachine_CompileFail_Stuck (P3-fail) path: Compilable + Compile
// returns err → TierStuck.
func TestStateMachine_CompileFail_Stuck(t *testing.T) {
	b := NewBridge()
	b.SetP3Compiler(failingP3{err: errors.New("synthetic compile error")})
	p := makeProtoWithCode(bytecode.ADD)
	pd := b.ProfileOf(p)
	pd.Compilable = CompCompilable

	for i := uint32(0); i < HotEntryThreshold; i++ {
		b.OnEnter(p, true)
	}

	if pd.TierState != TierStuck {
		t.Errorf("Compile fail → TierStuck, got %v", pd.TierState)
	}
}

// TestStateMachine_BackendPanic_Stuck a P3-internal panic is converted to an err
// via defer recover → TierStuck; the panic must not cross the interface.
func TestStateMachine_BackendPanic_Stuck(t *testing.T) {
	b := NewBridge()
	b.SetP3Compiler(panicP3{})
	p := makeProtoWithCode(bytecode.ADD)
	pd := b.ProfileOf(p)
	pd.Compilable = CompCompilable

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("panic must not escape considerPromotion, got %v", r)
		}
	}()

	for i := uint32(0); i < HotEntryThreshold; i++ {
		b.OnEnter(p, true)
	}

	if pd.TierState != TierStuck {
		t.Errorf("backend panic → TierStuck, got %v", pd.TierState)
	}
}

// TestStateMachine_Idempotent_Stuck once Stuck, considerPromotion is never
// triggered again — subsequent threshold-crossings are stopped by the guard, and
// the P3 compile count stays at 0.
func TestStateMachine_Idempotent_Stuck(t *testing.T) {
	mock := &countingP3{}
	b := NewBridge()
	b.SetP3Compiler(mock)
	p := makeProtoWithCode(bytecode.ADD)
	pd := b.ProfileOf(p)
	pd.Compilable = CompNotCompilable // straight to the Stuck path

	// Run enough EntryCount rounds; the guard returns directly every time
	for i := 0; i < 10*int(HotEntryThreshold); i++ {
		b.OnEnter(p, true)
	}
	if mock.compileCalls != 0 {
		t.Errorf("Stuck should never trigger Compile, got %d calls", mock.compileCalls)
	}

	// Even after switching to Compilable it still holds — TierState is already Stuck, the guard keeps stopping it
	pd.Compilable = CompCompilable
	for i := 0; i < int(HotEntryThreshold); i++ {
		b.OnEnter(p, true)
	}
	if pd.TierState != TierStuck {
		t.Errorf("Stuck must not transition to anything else, got %v", pd.TierState)
	}
}

// TestStateMachine_Idempotent_Gibbous Gibbous is also an absorbing state —
// subsequent threshold-crossings do not trigger recompilation.
func TestStateMachine_Idempotent_Gibbous(t *testing.T) {
	mock := &countingP3{}
	b := NewBridge()
	b.SetP3Compiler(mock)
	p := makeProtoWithCode(bytecode.ADD)
	pd := b.ProfileOf(p)
	pd.Compilable = CompCompilable

	for i := uint32(0); i < HotEntryThreshold; i++ {
		b.OnEnter(p, true)
	}
	if pd.TierState != TierGibbous {
		t.Fatalf("first round should promote, got %v", pd.TierState)
	}
	if mock.compileCalls != 1 {
		t.Fatalf("first round should compile once, got %d", mock.compileCalls)
	}

	// Continued OnEnter ⇒ the guard stops it, no more Compile calls
	for i := 0; i < 10*int(HotEntryThreshold); i++ {
		b.OnEnter(p, true)
	}
	if mock.compileCalls != 1 {
		t.Errorf("Gibbous should not re-compile, got %d calls", mock.compileCalls)
	}
}

// TestStateMachine_NoReverseEdge "zero deopt" formalized: after any transition,
// manually setting TierState back to TierInterp and then calling considerPromotion —
// the state machine will not "downgrade" on its own. This assertion is the
// code-level embodiment of 04 §2.4: the state machine has no
// TierGibbous → TierInterp / TierStuck → TierInterp reverse edge — only a user's
// deliberate reset can return the state to the start (this test does not attempt to
// prevent a deliberate reset, it only verifies that the "natural transition
// sequence" does not go back).
func TestStateMachine_NoReverseEdge(t *testing.T) {
	b := NewBridge()
	b.SetP3Compiler(dummyCompileP3{})
	p := makeProtoWithCode(bytecode.ADD)
	pd := b.ProfileOf(p)
	pd.Compilable = CompCompilable

	for i := uint32(0); i < HotEntryThreshold; i++ {
		b.OnEnter(p, true)
	}
	if pd.TierState != TierGibbous {
		t.Fatalf("expected Gibbous, got %v", pd.TierState)
	}

	// Simulate many subsequent events — TierGibbous should hold
	for i := 0; i < 1000; i++ {
		b.OnEnter(p, true)
		b.OnBackEdge(p, 0, true)
	}
	if pd.TierState != TierGibbous {
		t.Errorf("Gibbous broken by subsequent events, got %v", pd.TierState)
	}
}

// ----- mock P3 helpers -----

// decliningP3: SupportsAllOpcodes is always false and counts calls (tests the
// issue #40 recheck dedup — within the forceAll retry window, each back edge must
// not rerun the full backend analysis).
type decliningP3 struct{ supportsCalls int }

func (d *decliningP3) SupportsAllOpcodes(_ *bytecode.Proto) bool {
	d.supportsCalls++
	return false
}
func (*decliningP3) Compile(_ *bytecode.Proto, _ *TypeFeedback) (GibbousCode, error) {
	return nil, errors.New("declining backend must not be asked to compile")
}

// flippingP3: SupportsAllOpcodes declines first then accepts (accepts once
// accept=true), simulating an IC-gated backend that changes its verdict to accept
// after the interpreter has run a round and the IC has warmed up.
type flippingP3 struct {
	accept        bool
	supportsCalls int
	compileCalls  int
}

func (f *flippingP3) SupportsAllOpcodes(_ *bytecode.Proto) bool {
	f.supportsCalls++
	return f.accept
}
func (f *flippingP3) Compile(p *bytecode.Proto, _ *TypeFeedback) (GibbousCode, error) {
	f.compileCalls++
	return dummyCode{proto: p}, nil
}

// TestForceRetryWindow_RecheckDedupPerEntry (issue #40): while a proto declined by
// the backend under forceAll stays within the retry window, the massive number of
// back edges during a single entry (EntryCount unchanged) **must not** each rerun
// the full recheckCompilabilityRuntime analysis — the HeavyArith shape (a single
// entry + 2M back edges) measured this path at 22% CPU + 1.5 GB/op. After dedup,
// each pc runs at most 3 times per entry (entry + first back edge + HotBackEdgeThreshold).
func TestForceRetryWindow_RecheckDedupPerEntry(t *testing.T) {
	mock := &decliningP3{}
	b := NewBridge()
	b.SetP3Compiler(mock)
	b.SetForceAllPromote(true)
	p := makeProtoWithCode(bytecode.ADD)

	b.OnEnter(p, true)
	const edges = 10000
	for i := 0; i < edges; i++ {
		b.OnBackEdge(p, 0, true)
	}

	pd := b.ProfileOf(p)
	if pd.TierState != TierInterp {
		t.Fatalf("declined proto within retry window should stay TierInterp, got %v", pd.TierState)
	}
	// entry once + rearm once at back-edge count==1 + rearm once at
	// count==HotBackEdgeThreshold = 3; leave a little slack for future rearm-point
	// tweaks, but it must be far below the back-edge count.
	if mock.supportsCalls > 5 {
		t.Errorf("SupportsAllOpcodes ran %d times for %d back edges within one entry; dedup should cap it at ~3",
			mock.supportsCalls, edges)
	}
}

// TestForceRetryWindow_WarmICPromotesOnFirstBackEdge (the dual of the issue #40
// dedup, preserving the retry window's original purpose): an IC-gated backend
// declines on a cold IC, then changes its verdict to accept after the loop body has
// run a round — dedup rearms recheck at the first back edge (count==1), and the
// promotion point must not be later than before the fix.
func TestForceRetryWindow_WarmICPromotesOnFirstBackEdge(t *testing.T) {
	mock := &flippingP3{}
	b := NewBridge()
	b.SetP3Compiler(mock)
	b.SetForceAllPromote(true)
	p := makeProtoWithCode(bytecode.ADD)

	b.OnEnter(p, true) // cold IC: declines, enters the retry window
	pd := b.ProfileOf(p)
	if pd.TierState != TierInterp {
		t.Fatalf("cold decline should stay TierInterp, got %v", pd.TierState)
	}

	mock.accept = true // simulate the loop body finishing its first round, IC now warm
	b.OnBackEdge(p, 0, true)

	if pd.TierState != TierGibbous {
		t.Errorf("warm IC at first back edge should promote, got %v", pd.TierState)
	}
	if mock.compileCalls != 1 {
		t.Errorf("expected exactly one Compile after warm-IC accept, got %d", mock.compileCalls)
	}
}

// TestForceRetryWindow_AbsorbsToStuck the window semantics are unchanged: an
// always-declining backend + forceAll, once the window closes (EntryCount=64,
// covering deep-pc IC warmup for recursive protos — the third GETTABLE of
// binary-trees `check` only executes for the first time after the entire left
// subtree returns), it absorbs to TierStuck and no longer analyzes.
func TestForceRetryWindow_AbsorbsToStuck(t *testing.T) {
	mock := &decliningP3{}
	b := NewBridge()
	b.SetP3Compiler(mock)
	b.SetForceAllPromote(true)
	p := makeProtoWithCode(bytecode.ADD)
	pd := b.ProfileOf(p)

	for i := 0; i < 63; i++ {
		b.OnEnter(p, true)
	}
	if pd.TierState != TierInterp {
		t.Fatalf("entries 1-63 should stay in the retry window, got %v", pd.TierState)
	}

	b.OnEnter(p, true) // EntryCount=64: window closes
	if pd.TierState != TierStuck {
		t.Fatalf("entry 64 should absorb to TierStuck, got %v", pd.TierState)
	}

	callsAtStuck := mock.supportsCalls
	for i := 0; i < 1000; i++ {
		b.OnEnter(p, true)
		b.OnBackEdge(p, 0, true)
	}
	if mock.supportsCalls != callsAtStuck {
		t.Errorf("Stuck is absorbing; SupportsAllOpcodes must not run again (%d → %d)",
			callsAtStuck, mock.supportsCalls)
	}
}

// dummyCompileP3: Compile always succeeds, producing an empty GibbousCode.
type dummyCompileP3 struct{}

func (dummyCompileP3) SupportsAllOpcodes(_ *bytecode.Proto) bool { return true }
func (dummyCompileP3) Compile(p *bytecode.Proto, _ *TypeFeedback) (GibbousCode, error) {
	return dummyCode{proto: p}, nil
}

type dummyCode struct{ proto *bytecode.Proto }

func (d dummyCode) Proto() *bytecode.Proto         { return d.proto }
func (d dummyCode) Run(_ []uint64, _ uint32) int32 { return 0 }
func (d dummyCode) PendingErr() error              { return nil }
func (d dummyCode) Slot() (uint32, bool)           { return 0, false }

// failingP3: Compile always returns the specified err.
type failingP3 struct{ err error }

func (failingP3) SupportsAllOpcodes(_ *bytecode.Proto) bool { return true }
func (f failingP3) Compile(_ *bytecode.Proto, _ *TypeFeedback) (GibbousCode, error) {
	return nil, f.err
}

// panicP3: Compile panics directly (tests defer recover).
type panicP3 struct{}

func (panicP3) SupportsAllOpcodes(_ *bytecode.Proto) bool { return true }
func (panicP3) Compile(_ *bytecode.Proto, _ *TypeFeedback) (GibbousCode, error) {
	panic("synthetic backend bug")
}

// countingP3: records the Compile call count (tests idempotence).
type countingP3 struct{ compileCalls int }

func (countingP3) SupportsAllOpcodes(_ *bytecode.Proto) bool { return true }
func (c *countingP3) Compile(p *bytecode.Proto, _ *TypeFeedback) (GibbousCode, error) {
	c.compileCalls++
	return dummyCode{proto: p}, nil
}

// TestStateMachine_Coroutine_NoPromote (V11 coroutines do not promote): follows
// the onMain=false guard at bridge.go::considerPromotion line 263-265 + [07 §2.4] —
// inside a coroutine, even if hotness crosses the threshold there is no promotion
// (inherits the P3 rule verbatim).
//
// **Scenario**: OnEnter(p, false=onMain) on a coroutine thread fires repeatedly up
// to HotEntryThreshold, but because onMain=false, considerPromotion returns
// directly and the Proto stays TierInterp forever.
//
// **prove-the-path**: after HotEntryThreshold OnEnter(p, false) calls, pd.TierState
// is still TierInterp (on the main thread it would long since be TierGibbous or TierStuck).
func TestStateMachine_Coroutine_NoPromote(t *testing.T) {
	b := NewBridge()
	b.SetP3Compiler(dummyCompileP3{})
	p := makeProtoWithCode(bytecode.ADD)
	pd := b.ProfileOf(p)
	pd.Compilable = CompCompilable // on the main thread this should promote to TierGibbous

	// Coroutine trigger (onMain=false): per V11, no promotion
	for i := uint32(0); i < HotEntryThreshold*2; i++ {
		b.OnEnter(p, false) // coroutine thread
	}

	if pd.TierState != TierInterp {
		t.Errorf("协程内 TierState = %v, want TierInterp(V11 协程不升层)", pd.TierState)
	}
	if _, ok := b.gibbousCodes[p]; ok {
		t.Errorf("gibbousCodes 不应有 entry(协程不触发 Compile)")
	}
}

// TestStateMachine_Coroutine_NoPromote_AfterMainPromote the main thread promotes
// first, then the coroutine calls repeatedly: after the main thread promotes to
// TierGibbous, OnEnter(p, false) inside a coroutine should be a no-op (the P1
// already-absorbed-state guard, independent of onMain). Verifies V11 + that main-thread
// promotion and coroutines do not interfere.
func TestStateMachine_Coroutine_NoPromote_AfterMainPromote(t *testing.T) {
	b := NewBridge()
	b.SetP3Compiler(&countingP3{})
	p := makeProtoWithCode(bytecode.ADD)
	pd := b.ProfileOf(p)
	pd.Compilable = CompCompilable

	// Main thread promotes
	for i := uint32(0); i < HotEntryThreshold; i++ {
		b.OnEnter(p, true)
	}
	if pd.TierState != TierGibbous {
		t.Fatalf("主线程升层前提失败:TierState = %v, want TierGibbous", pd.TierState)
	}

	// Coroutine calls repeatedly: must not trigger a second Compile (P1 guard + onMain=false double insurance)
	compileCallsBefore := b.p3.(*countingP3).compileCalls
	for i := uint32(0); i < HotEntryThreshold*2; i++ {
		b.OnEnter(p, false)
	}
	compileCallsAfter := b.p3.(*countingP3).compileCalls

	if compileCallsAfter != compileCallsBefore {
		t.Errorf("协程内 Compile 调用次数 %d → %d(应不变,V11 + P1 守门)",
			compileCallsBefore, compileCallsAfter)
	}
	if pd.TierState != TierGibbous {
		t.Errorf("协程后 TierState = %v, want TierGibbous(不动)", pd.TierState)
	}
}

// gatedP3: can compile anything, but WorthPromoting declines (tests PromotionGater, issue #39).
type gatedP3 struct {
	dummyCompileP3
	gateCalls int
}

func (g *gatedP3) WorthPromoting(_ *bytecode.Proto) bool {
	g.gateCalls++
	return false
}

// TestPromotionGater_AutoDeclinesToStuck: in auto mode, the backend's WorthPromoting
// returning false → TierStuck absorption (no try-compile), and Compile is never called.
func TestPromotionGater_AutoDeclinesToStuck(t *testing.T) {
	b := NewBridge()
	mock := &gatedP3{}
	b.SetP3Compiler(mock)
	p := makeProtoWithCode(bytecode.ADD)
	pd := b.ProfileOf(p)
	pd.Compilable = CompCompilable

	for i := uint32(0); i < HotEntryThreshold; i++ {
		b.OnEnter(p, true)
	}

	if pd.TierState != TierStuck {
		t.Errorf("gated proto → TierStuck, got %v", pd.TierState)
	}
	if mock.gateCalls == 0 {
		t.Error("WorthPromoting was never consulted")
	}
	if _, ok := b.gibbousCodes[p]; ok {
		t.Error("gated proto must not be compiled/installed")
	}
	// Stuck is absorbing: later entries must not re-consult the gate.
	callsAtStuck := mock.gateCalls
	b.OnEnter(p, true)
	if mock.gateCalls != callsAtStuck {
		t.Errorf("Stuck must absorb; gate re-consulted (%d → %d)", callsAtStuck, mock.gateCalls)
	}
}

// TestPromotionGater_ForceAllBypasses: forceAll mode bypasses the profitability
// gate — differential coverage does not shrink due to a profitability judgment (issue #39).
func TestPromotionGater_ForceAllBypasses(t *testing.T) {
	b := NewBridge()
	mock := &gatedP3{}
	b.SetP3Compiler(mock)
	b.SetForceAllPromote(true)
	p := makeProtoWithCode(bytecode.ADD)
	pd := b.ProfileOf(p)
	pd.Compilable = CompCompilable

	b.OnEnter(p, true)

	if pd.TierState != TierGibbous {
		t.Errorf("forceAll must bypass the gate and promote, got %v", pd.TierState)
	}
	if mock.gateCalls != 0 {
		t.Errorf("forceAll must not consult WorthPromoting (called %d times)", mock.gateCalls)
	}
}
