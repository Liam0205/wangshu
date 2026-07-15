// P3Compiler interface (`docs/design/p2-bridge/05-p3-p4-interface.md` §2).
//
// The core "shared frontend" contract — P3 (wazero) / P4 (native) / mock all
// share the same interface surface; the P2 implementation side needs zero
// changes.
package bridge

import "github.com/Liam0205/wangshu/internal/bytecode"

// P3Compiler is the core interface P2 uses to call the downstream compilation
// layer (05 §2.1).
//
// **Interface stability is a hard constraint** (05 §0.3) — once the P2
// implementation side is stabilized, P3 launch / P4 launch / mock switch all
// integrate against the same interface, and the Bridge.considerPromotion /
// installGibbous code does not change at all. Any implementation that "lets P3 /
// P4 each redesign heat sampling / type feedback / tier-up decisions" is
// rejected outright.
type P3Compiler interface {
	// SupportsAllOpcodes checks whether every opcode in the Proto is within the
	// backend's supported set.
	//
	// Caller: the [03] §3.7 F7 gate (called within AnalyzeProto as the
	// opcode-level backstop after F1-F6 all pass).
	//
	// Implementer contract:
	//   - O(N) single-pass scan of proto.Code; return false if any one is
	//     unsupported;
	//   - the call is purely read-only; does not modify the Proto, does not
	//     persist any state;
	//   - on encountering an unrecognized opcode number (the 38..63 reserved
	//     range or a future extension), uniformly return false (conservative
	//     reject, 03 §3.7.4 "conservative default" principle);
	//   - **must not panic** — an unrecognized opcode number also takes the
	//     conservative reject.
	SupportsAllOpcodes(proto *bytecode.Proto) bool

	// Compile compiles a Proto into a GibbousCode (executable artifact).
	//
	// Caller: [04] §3 considerPromotion, called after confirming
	//   (1) TierState == TierInterp (2) Compilable == CompCompilable
	//   (3) heat crosses the threshold.
	//
	// Inputs:
	//   - proto: the target Proto (already passed the F1-F7 gates, compilable);
	//   - feedback: the type-feedback snapshot (02 §4 aggregate artifact);
	//     **the implementer must tolerate nil** (degrading to "no feedback hints"
	//     compilation, still correct).
	//
	// Error-return semantics (key contract, 04 §4.3 + 05 §2.2.2):
	//   - error != nil => P2 marks that Proto TierStuck (permanently
	//     interpreted, no retry); this call is the single "tier-up attempt" for
	//     that Proto, and failure means fallback;
	//   - the implementer should distinguish error kinds (unsupported_opcode_shape
	//     / out_of_resources / backend_panic / backend_declined);
	//   - a backend panic => the implementer recovers and converts to error, **not
	//     letting the panic cross this interface** (P2 must not crash on a backend
	//     bug, only fall back that Proto);
	//
	// Performance requirement: Compile is not on the hot path (called once at
	// tier-up only), so a few milliseconds is acceptable.
	//
	// Concurrency requirement: may be called concurrently by multiple States for
	// the same Proto; the implementer must guarantee thread safety.
	Compile(proto *bytecode.Proto, feedback *TypeFeedback) (GibbousCode, error)
}

// GibbousCode is the "install handle" for a P3/P4 compilation artifact (05 §6
// abstract type).
//
// From P2's view: an opaque token — installGibbous(proto, code) registers it
// into the P3 trampoline table; P2 does not interpret its internal fields. The
// concrete type is implemented by P3 (wazero CompiledModule) or P4 (native code
// segment).
//
// **Run/PendingErr cross-tier execution entry (VS0-d)**: crescent's trampoline,
// when doCall detects a Proto has been promoted to gibbous, jumps into the
// compilation artifact for execution via this interface (05 §6.2). Putting Run
// on the interface (rather than having crescent type-assert to a gibbous private
// type) keeps the trampoline logic in crescent's all-build code, without
// importing the p3-build-only gibbous package — P3/P4 share the same trampoline
// (04-trampoline §0.4).
type GibbousCode interface {
	// Proto is the back-pointer, for trampoline validation — ensuring the
	// GibbousCode is paired with the Proto.
	Proto() *bytecode.Proto

	// Run is the crescent->gibbous cross-tier entry (04-trampoline §2.2 step3).
	//   - stack: the reused stack (CallWithStack zero-alloc path, len>=1);
	//     stack[0]=base input arg, and on return stack[0]=status.
	//   - base: this frame's R0 byte offset in the shared linear memory (=
	//     stackSegByte+base*8).
	//   - returns status: 0=OK / 1=ERR (05 §2.1). P3 never returns 2 (deopt is
	//     P4-only).
	Run(stack []uint64, base uint32) int32

	// PendingErr returns the most recent Run's wazero-internal error (read on
	// trampoline ERR).
	PendingErr() error

	// Slot returns this artifact's run slot number in the shared env.table + a
	// flag for whether it is registered (P3 PW10 R3 Arch-2). A gibbous->gibbous
	// CALL reaches the callee directly across modules via call_indirect using
	// the callee GibbousCode's slot (avoiding h_call's double tier-cross).
	// ok=false (table-full sentinel / not in table) ==> fall back to synchronous
	// Run (baseline). Native P4 code has no wasm-table concept and can return
	// (0,false) (always taking the fallback).
	Slot() (uint32, bool)
}

// NativeSegAddrer is an optional interface a GibbousCode may implement
// (issue #50 Spike 5): it exposes the absolute entry address of the
// PJ10 native mmap segment so a caller segment can `call qword ptr [addr]`
// directly into it (segment-to-segment dispatch, no host round trip).
//
// Only the PJ10 native emit path (peroptranslator.nativeCode) implements
// this; PerOpCode head-op replay and PJ0-PJ9 shape templates do not, so
// callers must type-assert and fall back to the exit-reason path when
// the assertion fails or the returned addr is 0 (segment disposed).
type NativeSegAddrer interface {
	// NativeSegEntryAddr returns the mmap segment's start address as a
	// uint64, or 0 if the segment isn't available (disposed / not
	// native). Callable only for a code value that also answers
	// IsPJ10Native() == true.
	NativeSegEntryAddr() uint64

	// NativeNeverExitsSegment reports whether this segment runs
	// start-to-finish without exiting to a Go helper (issue #50 Spike
	// 5). Only such callees are eligible for segment-to-segment
	// dispatch. False keeps the caller on the exit-reason path.
	NativeNeverExitsSegment() bool
}

// CompileErrKind is the category of a compilation failure (05 §2.2.2 error-return
// semantics / 04 §4.3).
//
// It lets tier-up logs distinguish "F7 miss" (a bug in [03]) / "resource
// exhaustion" (a transient runtime state) / "backend panic" (a bug in P3), so
// diagnostic tools can route alerts accordingly.
type CompileErrKind uint8

const (
	// CompileErrUnsupportedOpcodeShape: an F7 miss — SupportsAllOpcodes did not
	// recognize some sub-case of an opcode (e.g. GETTABLE's key is some special
	// form); [03] judged it Compilable overall but P3 actually cannot compile it.
	// Should file an issue to fix [03].
	CompileErrUnsupportedOpcodeShape CompileErrKind = iota

	// CompileErrOutOfResources: wazero module instantiation failure / out of
	// memory / resource limit. Theoretically retryable, but P2 does not
	// distinguish (04 §7.1 no-retry discipline).
	CompileErrOutOfResources

	// CompileErrBackendPanic: an internal panic in the P3 compiler (an
	// implementation bug or edge-case form). Backstopped and converted to this
	// category by b.tryCompile's defer recover. Should file an issue to fix P3.
	CompileErrBackendPanic

	// CompileErrBackendDeclined: P3 decided not to compile (e.g. a heuristic
	// judged the Proto's payoff insufficient). P2 PB0 does not expect this return
	// (P3 should reject in the SupportsAllOpcodes phase).
	CompileErrBackendDeclined
)

func (k CompileErrKind) String() string {
	switch k {
	case CompileErrUnsupportedOpcodeShape:
		return "unsupported_opcode_shape"
	case CompileErrOutOfResources:
		return "out_of_resources"
	case CompileErrBackendPanic:
		return "backend_panic"
	case CompileErrBackendDeclined:
		return "backend_declined"
	default:
		return "unknown"
	}
}

// CompileError is the standard wrapper for the err returned by P3.Compile (04
// §5.2).
//
// b.tryCompile's defer recover converts a backend panic into a *CompileError
// (Kind=CompileErrBackendPanic) — avoiding a panic crossing the interface and
// crashing the P1 interpreter main loop.
type CompileError struct {
	Kind   CompileErrKind
	Proto  *bytecode.Proto
	Reason string // human-readable reason (includes panic stack / OOM description, etc.)
}

func (e *CompileError) Error() string { return e.Kind.String() + ": " + e.Reason }
