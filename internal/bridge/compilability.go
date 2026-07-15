// Compilability enum and reasons bitmap (`docs/design/p2-bridge/03-compilability-analysis.md`).
package bridge

// Compilability describes the static compilability verdict of a Proto (03 §5.1).
//
// Three-state enum:
//   - CompUnknown      not analyzed (P1-only build / Compile has not run AnalyzeProto yet)
//   - CompCompilable   F1-F7 all pass, eligible for promotion decisions
//   - CompNotCompilable any of F1-F7 triggers, interpret forever
//
// **Key discipline** (03 §1): **conservative first, prefer false negatives over
// false positives** — misjudging a non-compilable shape as compilable is
// catastrophic (P3 emits wrong code or crashes at runtime, fallback is never
// triggered, and the system does not know the result is wrong); misjudging a
// compilable shape as non-compilable merely "misses some speedup". So any shape
// we are unsure about is judged CompNotCompilable.
type Compilability uint8

const (
	// CompUnknown: Compile has not run AnalyzeProto yet (or P1-only build with P2 disabled).
	// The 04 state machine treats this as NotCompilable (one more layer of caution,
	// 03 §5.5), conservatively not promoting.
	CompUnknown Compilability = iota

	// CompCompilable: compilable — F1-F7 all pass, eligible for promotion decisions.
	// After promotion the 04 state machine may invoke P3 compilation.
	CompCompilable

	// CompNotCompilable: not compilable — any of F1-F7 triggers, interpret forever.
	// The 04 state machine's considerPromotion skips this Proto outright (always tier-0).
	CompNotCompilable
)

func (c Compilability) String() string {
	switch c {
	case CompCompilable:
		return "Compilable"
	case CompNotCompilable:
		return "NotCompilable"
	default:
		return "Unknown"
	}
}

// ReasonsBitmap is the bitmask of F1-F7 rejection reasons (03 §5.1 reasonsBitmap).
//
// Each F<n> shape maps to one bit (the constants below follow design doc 03 §3
// order); it is 0 when Compilable; at least one bit is 1 when NotCompilable
// (possibly several at once — an expression of "conservative first": multiple
// rules judging non-compilable at the same time, redundancy = safety).
type ReasonsBitmap uint16

const (
	// ReasonVararg (F1): vararg function (03 §3.1).
	ReasonVararg ReasonsBitmap = 1 << iota

	// ReasonYield (F2-a): direct call to coroutine.yield.
	ReasonYield

	// ReasonResume (F2-a'): direct call to coroutine.resume.
	ReasonResume

	// ReasonCoroutine (F2-a''): any coroutine.* call.
	ReasonCoroutine

	// ReasonUnknownCall (F2-b): calls a function that cannot be statically proven not to yield.
	ReasonUnknownCall

	// ReasonDebug (F3): references the debug table (03 §3.3).
	ReasonDebug

	// ReasonSetfenv (F4): calls setfenv / getfenv (03 §3.4).
	ReasonSetfenv

	// ReasonOverSize (F5): function instruction count exceeds MaxCompilableInsns (03 §3.5).
	ReasonOverSize

	// ReasonOverRegs (F5): register count exceeds MaxCompilableRegs (03 §3.5).
	ReasonOverRegs

	// ReasonNestedDeep (F6): nesting depth exceeds MaxClosureDepth (03 §3.6).
	ReasonNestedDeep

	// ReasonOverUpval (F6): upvalue count exceeds MaxUpvalCount (03 §3.6).
	ReasonOverUpval

	// ReasonBackendUnsupp (F7): P3 backend does not support some opcode (03 §3.7).
	ReasonBackendUnsupp

	// ReasonSelfCall (F2-c): `obj:method(...)` shape (SELF + CALL/TAILCALL).
	//
	// **Placeholder-bit semantics** (same technique as ReasonBackendUnsupp): the
	// method receiver's method table cannot be resolved statically, and the callee
	// may internally yield/setfenv/debug — so we conservatively mark it as rejected
	// as a placeholder at compile time. After the P4 jit is injected at runtime, if
	// `SupportsAllOpcodes(proto)` matches the PJ5 SELF inline shape (MOVE/GETUPVAL +
	// SELF + (args) + CALL/TAILCALL + RETURN), `recheckCompilabilityRuntime` clears
	// this bit — byte-equal with P1 is guaranteed by the whole handoff via
	// host.Self + host.CallBaseline / host.TailCall (callee-internal
	// yield/__call/meta all handled by the P1 doCall path).
	//
	// **Boundary with ReasonUnknownCall**: a method call no longer also sets
	// ReasonUnknownCall — placeholder vs real rejection are recorded separately, so
	// the runtime re-check can clear the bit precisely without touching the known
	// yield risk of F2-b.
	ReasonSelfCall
)

// HasAny reports whether any reason bit is set.
func (r ReasonsBitmap) HasAny() bool { return r != 0 }

// Threshold constants (03 §3.5 / §3.6 suggested values — calibrated after
// measurement, see 03 §9 gaps).
const (
	// MaxCompilableInsns: upper bound on Proto.Code length; exceeding it triggers F5.
	// 2000 instructions ≈ 100-300 lines of Lua; the vast majority of hot functions
	// are far smaller.
	MaxCompilableInsns = 2000

	// MaxCompilableRegs: upper bound on Proto.MaxStack; exceeding it triggers F5.
	// 200 is a padded version of the reasonable per-function register ceiling for
	// Lua 5.1 (02 §1 max regs ≈ 250).
	MaxCompilableRegs = 200

	// MaxClosureDepth: upper bound on nested function depth (F6).
	// Deliberately strict and conservative (1-2 levels of nesting covers the vast
	// majority); relaxed once the P3 upvalue compilation protocol matures.
	MaxClosureDepth = 3

	// MaxUpvalCount: upper bound on Proto.UpvalDescs length (F6).
	MaxUpvalCount = 8
)
