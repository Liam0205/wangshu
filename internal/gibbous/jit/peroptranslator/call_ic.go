//go:build wangshu_p4

// call_ic.go — per-CALL-site inline cache (issue #50 Spike 1
// infrastructure).
//
// Each CALL bytecode in a Proto gets one CallIC slot indexed by pc. On
// first execution the exit-reason CALL dispatcher (translator_native_dispatch)
// records the callee proto meta (protoID + NumParams + MaxStack + Flags);
// subsequent executions can consult the slot from inside the mmap segment
// to gate an EmitCallInline fast path (Spike 2+).
//
// Spike 1 mission: land the plumbing (types + population + probes) with
// zero behavior change — emitCALL still lowers to the historical
// HelperCall exit-reason regardless of IC state, and Run's dispatcher
// simply fills the slot after invoking host.CallBaseline. Once the
// probes prove the slot gets populated on the call-heavy kernels, Spike
// 2 wires the segment-side guard + segment-side frame build.
package peroptranslator

import (
	"sync/atomic"

	"github.com/Liam0205/wangshu/internal/bytecode"
)

// CallIC is the per-CALL-site inline cache slot. Mono-only IC: a
// differing callee at the next call clears the slot (matches P3 wasm's
// shape-monomorphic policy). All heavy Lua benchmarks in scope
// (fib/binary-trees/fannkuch) issue each CALL against a stable callee.
//
// Fields are read/written with atomic ops so the mmap segment guard
// (Spike 2+) can inspect the slot under race with the Go-side dispatcher
// populate.
//
//   - CalleeProtoID: the callee Proto ID at the last known hit.
//     0 = unpopulated. Populated on first Hit; a subsequent miss with
//     a differing protoID clears it back to 0 (megamorphic → slow path
//     again forever, tracked via Flags).
//   - CalleeNumParams: proto.NumParams (0..255).
//   - CalleeMaxStack:  proto.MaxStack (0..255 in practice — Lua 5.1
//     luac never generates a Proto with MaxStack > 250 in benchmark
//     workloads, but we widen to 255 to keep the byte packing).
//   - Flags: CallICFlag* bits. FlagIsHost / FlagIsVararg / FlagNeedsArg
//     poison the slot. FlagStuck marks a shape-change past the budget
//     — the segment guard MUST always miss on Stuck.
type CallIC struct {
	CalleeProtoID   uint32
	CalleeNumParams uint8
	CalleeMaxStack  uint8
	Flags           uint8
	// IntrinsicID (offset 7, formerly pad) caches the math intrinsic kind
	// (jit.Intrinsic*, 0 = none) when the callee is a recognized
	// pure-numeric host closure (issue #77). Set by PopulateHostIntrinsic
	// alongside CallICFlagIsIntrinsic; the segment reads it to pick which
	// SSE/NEON op to emit after the callee-identity guard passes.
	IntrinsicID uint8

	// Hits: increments on every EmitCallInline fast-path hit
	// (Spike 2+). Currently unused (Spike 1 only populates the slot;
	// no fast path yet).
	Hits uint32
	// Misses: exit-reason path increments on shape change (differing
	// callee protoID) or host/vararg observation. Prove-the-path tests
	// assert Hits vs Misses distribution.
	Misses uint32

	// CalleeSegAddr is the absolute entry address of the callee's PJ10
	// native mmap segment, or 0 if the callee is not native-compiled
	// (issue #50 Spike 5). Populated by the dispatcher alongside the
	// shape meta. The segment-to-segment fast path loads this and, if
	// nonzero, `call`s into the callee segment directly instead of the
	// host round trip. Stored as uint64 for a stable field width in
	// the segment guard's disp encoding.
	//
	// Written non-atomically alongside the atomic ProtoID store. Read
	// order in the segment: the segment loads ProtoID first (atomic
	// via the aligned uint32), and only reaches CalleeSegAddr after
	// the ProtoID compare passes — so a torn read can't route into a
	// stale segment (a shape change zeroes ProtoID, forcing the slow
	// path before CalleeSegAddr is consulted).
	CalleeSegAddr uint64

	// IntrinsicCalleeVal (offset 24) holds the full NaN-boxed callee value
	// (MakeGC(TagFunction, closureGCRef)) recorded when the site is a math
	// intrinsic (issue #77). The segment's intrinsic fast path guards
	// `R(A) == IntrinsicCalleeVal` — one 64-bit compare that pins the
	// exact host closure (tag + GCRef). A different callee (Lua fn, other
	// host fn, shape change) misses and falls to the exit-reason CALL path.
	// Stable for the State's lifetime: the stdlib math closure is rooted
	// via the globals table and the arena is non-moving (same invariant as
	// the EQ-K / NodeHit / LOADK baked-GCRef fast paths; #12 copy-compact
	// GC would need to revisit all of them). Only meaningful when
	// CallICFlagIsIntrinsic is set.
	IntrinsicCalleeVal uint64
}

// callICSegAddrByteOffset is the byte offset of CalleeSegAddr within
// CallIC, baked into the segment-to-segment dispatch emit as a disp8.
// Layout: ProtoID(4) + NumParams(1) + MaxStack(1) + Flags(1) +
// IntrinsicID(1) + Hits(4) + Misses(4) = 16, then CalleeSegAddr(8), then
// IntrinsicCalleeVal(8) = 32. Verified by TestCallICLayout so a struct
// reorder can't silently break the emit.
const callICSegAddrByteOffset = 16

// callICFlagsByteOffset is the byte offset of the Flags field within
// CallIC (ProtoID(4) + NumParams(1) + MaxStack(1) = 6). The
// segment-to-segment emit tests the NeverExits bit at [icSlot+6].
const callICFlagsByteOffset = 6

// callICIntrinsicIDByteOffset is the byte offset of the IntrinsicID field
// (offset 7, immediately after Flags). The intrinsic fast path reads it
// to dispatch on the math kind (issue #77).
const callICIntrinsicIDByteOffset = 7

// callICIntrinsicValByteOffset is the byte offset of IntrinsicCalleeVal
// (offset 24, after CalleeSegAddr). The intrinsic fast path loads it for
// the callee-identity guard (issue #77).
const callICIntrinsicValByteOffset = 24

// segToSegDepthCap bounds native segment-to-segment recursion. With the
// self-managed spill stack wired (issue #89), each seg2seg level's `sub sp`
// descends on jitCtx's 64 KiB spill buffer instead of the goroutine stack's
// NOSPLIT allowance, so the cap is a spill-stack-capacity bound rather than
// the ~800 B NOSPLIT budget. Past the cap, a caller falls back to the
// exit-reason path (host executeFrom handles deeper recursion on the
// heap-allocated CI chain).
//
// History: PR #86 had to drop this from 128 to 16 because the trampoline
// did NOT switch SP — each level's `sub sp` was invisible to Go's linker
// nosplit accounting and, when Run was entered with SP near the stack
// guard (the deep-Lua-recursion workloads that drive seg2seg hard), a
// ~4 KB descent (cap=128) punched through the ~800 B allowance and
// corrupted adjacent heap objects (GC "found pointer to free object",
// FuzzAutoPromote seed 7f161a85c466adbf). issue #89 wired the spill stack
// (05 §3.4; amd64 SP switch proven in spike/p4spillstack, arm64 mirrored
// and verified on CI), so the cap moves back up.
//
// Per-level spill-stack cost: amd64 ~24 B, arm64 32 B. The spill stack is
// SpillStackSize = 64 KiB, i.e. ~2048 levels at 32 B/level. cap=128 uses
// at most 128*32 = 4 KiB, a 16x margin inside the buffer, and is well past
// any real recursion depth that stays on the native tier (deeper recursion
// is not a native-tier win — it round-trips via exit-reason). Regression:
// TestI86_DeepRecursionGCStress must pass 3/3 at this cap under GOGC=1.
const segToSegDepthCap = 128

// Call-IC flag bits (single byte in Flags).
const (
	CallICFlagIsVararg uint8 = 1 << 0
	CallICFlagNeedsArg uint8 = 1 << 1
	CallICFlagIsHost   uint8 = 1 << 2
	// CallICFlagNeverExits marks the callee as one whose native segment
	// runs start-to-finish without exiting to a Go helper (issue #50
	// Spike 5). Only such callees are eligible for segment-to-segment
	// dispatch (a mid-execution exit-reason RET would be misread by the
	// caller segment as a plain return). Set by the dispatcher when it
	// records a native callee that also passes ProtoNeverExitsSegment.
	CallICFlagNeverExits uint8 = 1 << 3
	// CallICFlagIsIntrinsic marks the slot as a recognized math intrinsic
	// host closure (issue #77): the segment's intrinsic fast path is
	// eligible, guarded by IntrinsicCalleeVal identity + arg IsNumber.
	// Distinct from (and set instead of) CallICFlagStuck for these host
	// callees — a normal host closure still goes Stuck. Not part of the
	// Lua-callee fast-path poison mask (0x87), so the existing guard's
	// flags test ignores it; the intrinsic path checks it explicitly.
	CallICFlagIsIntrinsic uint8 = 1 << 4
	// CallICFlagStuck marks the slot as permanently at the slow path:
	// host callee observed, or repeated shape change past the budget.
	CallICFlagStuck uint8 = 1 << 7
)

// ProtoNeverExitsSegment reports whether a Proto's native-emitted
// segment runs start-to-finish without ever exiting to a Go-side
// exit-reason helper (issue #50 Spike 5 segment-to-segment dispatch
// prerequisite).
//
// When true, a caller segment can `call` into this callee's segment
// directly and rely on it returning via `ret` (in-segment teardown),
// never via an exit-reason RET the caller would misread as a plain
// return. It also means no GC safepoint / allocation fires mid-
// execution, so the caller need not materialise a complete CI frame
// before the call — the callee frame is "virtual" (lives only on the
// native stack + value-stack registers).
//
// **Gate**: only ops whose emit is UNCONDITIONALLY inline (no
// exit-reason, no guard-miss fallthrough to a helper) qualify:
//
//	MOVE, LOADK (numeric; string LOADK is rejected by AnalyzeNative),
//	LOADBOOL, LOADNIL, JMP, RETURN, FORPREP, FORLOOP, TEST, TESTSET.
//
// FORPREP/FORLOOP do assume-number NEON/SSE arithmetic but never emit
// an exit-reason (a NaN is a correctness concern for the RESULT, the
// same one the normal native path already accepts — not an exit). TEST/
// TESTSET are pure Nil/False bit-compares, also no exit. These give
// multi-BB never-exits shapes (via FORLOOP back-edge / TEST branch),
// which is what makes a callee both native-compiled (PreferNative needs
// multi-BB) AND a valid segment-to-segment target.
//
// Excluded — each can exit mid-execution:
//   - ADD/SUB/MUL/DIV/UNM: IsNumber guard miss → HelperArithSlow.
//   - LT/LE/EQ: compare guard miss → HelperCompareSlow.
//   - MOD/POW/LEN/CONCAT/GET*/SET*/NEWTABLE/CALL/SELF: exit-reason.
//
// AnalyzeNative is arch-specific (amd64 / arm64 build-tagged) so this
// arch-neutral file picks up the right one at build time.
func ProtoNeverExitsSegment(proto *bytecode.Proto) bool {
	if proto == nil || len(proto.Code) == 0 {
		return false
	}
	if !AnalyzeNative(proto) {
		return false
	}
	for _, ins := range proto.Code {
		switch bytecode.Op(ins) {
		case bytecode.MOVE, bytecode.LOADK, bytecode.LOADBOOL,
			bytecode.LOADNIL, bytecode.JMP, bytecode.RETURN,
			bytecode.FORPREP, bytecode.FORLOOP,
			bytecode.TEST, bytecode.TESTSET:
			// unconditionally inline (no exit-reason emit)
		default:
			return false
		}
	}
	return true
}

// Issue #50 Spike 5 emit gates (arch-shared so amd64 + arm64 emit paths
// reference the same flags).
//
//   - callInlineEnabled: emitCALL emits the segment-side EmitCallInline
//     guard + fast path (vs the historical HelperCall exit-reason).
//   - inlineGetUpvalEnabled: GETUPVAL is emitted inline (emitGETUPVALInline)
//     instead of the HelperGetUpval exit-reason. Independent of
//     segToSegEnabled: correct on its own (at segCallDepth==0 the
//     open/foreign fallback exit-reasons; only inside a seg2seg subtree
//     does it deopt).
//   - segToSegEnabled: the caller CALL fast body `call`s directly into a
//     native callee segment, and the callee RETURN tears its frame down
//     in-segment and rets back. Activates BOTH the caller dispatch and
//     the callee dual-semantics RETURN (they must land together).
var (
	callInlineEnabled     = true
	inlineGetUpvalEnabled = true
	segToSegEnabled       = true
	// mathIntrinsicsEnabled gates the issue #77 math.* intrinsic fast
	// path (sqrt/floor/ceil/abs/max/min emitted inline instead of an
	// exit-reason CALL). On by default; toggle for A/B benchmarking and
	// to isolate correctness regressions.
	mathIntrinsicsEnabled = true
)

// SetMathIntrinsicsEnabledForTest toggles the math intrinsic fast path
// and returns a restore func. Test / benchmark only. Because it affects
// emit, compile Protos AFTER toggling for the change to take effect.
func SetMathIntrinsicsEnabledForTest(on bool) (restore func()) {
	prev := mathIntrinsicsEnabled
	mathIntrinsicsEnabled = on
	return func() { mathIntrinsicsEnabled = prev }
}

// SetSegToSegEnabledForTest toggles the segment-to-segment dispatch gate
// and returns a restore func. Test / benchmark only. Because it affects
// emit, compile Protos AFTER toggling for the change to take effect.
func SetSegToSegEnabledForTest(on bool) (restore func()) {
	prev := segToSegEnabled
	segToSegEnabled = on
	return func() { segToSegEnabled = prev }
}

// SetInlineGetUpvalEnabledForTest toggles inline GETUPVAL and returns a
// restore func (compile Protos after toggling for it to take effect).
func SetInlineGetUpvalEnabledForTest(on bool) (restore func()) {
	prev := inlineGetUpvalEnabled
	inlineGetUpvalEnabled = on
	return func() { inlineGetUpvalEnabled = prev }
}

// ProtoSeg2SegEligible reports whether a Proto can serve as a
// segment-to-segment callee under the deopt-on-guard-miss protocol
// (issue #50 Spike 5, arith/compare/GETUPVAL callees like fib).
//
// It is a superset of ProtoNeverExitsSegment: in addition to the purely
// inline ops, it admits ops that CAN exit to a Go helper but whose exit
// path is guarded by emitSegCallDeoptGuard — when running as a seg2seg
// callee (segCallDepth>0) they deopt (set the flag + ret) instead of
// exiting mid-segment, and the whole call chain redoes at the top via
// host.ExecutePlainCallInlineFrame:
//
//   - ADD/SUB/MUL/DIV: IsNumber guard miss deopts (emitInlineArith slow).
//   - LT/LE/EQ: compare guard miss deopts (emitCompareExitTail).
//   - GETUPVAL: inlined (emitGETUPVALInline); the rare open/foreign-owner
//     fallback deopts.
//   - GETTABLE (ArrayHit-IC sites only): the inline array-slot read is
//     side-effect free (pure load, no __index on the fast path), so a
//     deopt redo is idempotent; the miss block (non-table / non-integer
//     key / out-of-bounds / Nil slot) deopts. Non-ArrayHit sites keep
//     the Proto off seg2seg — their every access would exit.
//   - CALL (fixed B/C): a nested call that can't itself go seg2seg (IC
//     cold / cap reached) deopts (the CALL fallback deopt guards).
//
// **Idempotency requirement**: deopt redoes the whole top-level call on
// the baseline, so the callee's inputs (closure at R(A), args at
// R(A+1..)) must survive a partial-then-aborted native run. That holds
// iff the callee never writes a parameter register — every
// register-writing op must have dest A >= NumParams. A callee that
// reassigns a parameter (`x = x + 1` → writes R(0)) is rejected so the
// redo can't read a clobbered arg.
//
// Excluded (exit-reason with NO deopt guard, or side effects that a redo
// would duplicate): MOD/POW, UNM/NOT/LEN/CONCAT, SETTABLE/GET*/SET*
// GLOBAL/NEWTABLE/SELF/SETLIST, and GETTABLE at non-ArrayHit sites.
// Their presence keeps a Proto off the seg2seg path.
func ProtoSeg2SegEligible(proto *bytecode.Proto) bool {
	if proto == nil || len(proto.Code) == 0 {
		return false
	}
	if !AnalyzeNative(proto) {
		return false
	}
	return seg2segOpsEligible(proto)
}

// seg2segOpsEligible is the op-set + no-param-write half of
// ProtoSeg2SegEligible, WITHOUT the AnalyzeNative gate. AnalyzeNative's
// own CALL density relaxation calls this (calling ProtoSeg2SegEligible
// there would recurse into AnalyzeNative). External callers should use
// ProtoSeg2SegEligible, which composes AnalyzeNative + this.
func seg2segOpsEligible(proto *bytecode.Proto) bool {
	if proto == nil || len(proto.Code) == 0 {
		return false
	}
	nparams := int(proto.NumParams)
	for pc, ins := range proto.Code {
		op := bytecode.Op(ins)
		switch op {
		case bytecode.MOVE, bytecode.LOADK, bytecode.LOADBOOL,
			bytecode.LOADNIL, bytecode.JMP, bytecode.RETURN,
			bytecode.FORPREP, bytecode.FORLOOP,
			bytecode.TEST, bytecode.TESTSET,
			bytecode.ADD, bytecode.SUB, bytecode.MUL, bytecode.DIV,
			bytecode.LT, bytecode.LE, bytecode.EQ,
			bytecode.GETUPVAL, bytecode.CALL:
			// seg2seg-safe: pure-inline, deopt-guarded, or inlined.
		case bytecode.GETTABLE:
			// Only ArrayHit-IC sites: those get the inline array-slot
			// fast path (side-effect-free load, deopt-guarded miss).
			// Any other IC kind would exit on every access.
			if pc >= len(proto.IC) ||
				proto.IC[pc].Kind != bytecode.ICKindArrayHit {
				return false
			}
		default:
			return false
		}
		// Multret RETURN (B==0, return R(A..top)) can't be torn down
		// in-segment: the callee doesn't track a live `top`, so the
		// in-segment moveResults has no static result count. Reject.
		if op == bytecode.RETURN && bytecode.B(ins) == 0 {
			return false
		}
		// No-param-write gate: any op with a register destination must
		// write at or above NumParams, so a deopt redo reads intact args.
		// All eligible register-writing ops put their lowest dest at A;
		// the comparison/branch/return ops below have no register dest.
		switch op {
		case bytecode.JMP, bytecode.RETURN, bytecode.TEST,
			bytecode.LT, bytecode.LE, bytecode.EQ:
			// no register destination
		default:
			if bytecode.A(ins) < nparams {
				return false
			}
		}
	}
	return true
}

// findCallSiteIndex returns the CallIC index for a given pc, or -1 if
// the pc has no corresponding CallIC slot (e.g. CFG changed between
// translate-time and emit-time, or the pc slice is nil). Arch-shared
// (both amd64 and arm64 emitCALL fast paths use it).
func findCallSiteIndex(callSitePCs []int32, pc int32) int {
	for i, sitePC := range callSitePCs {
		if sitePC == pc {
			return i
		}
	}
	return -1
}

// callSitePCsFor walks proto.Code and returns pc values whose op is
// CALL. Used by TranslateProtoNative to size codeBufProto.CallICs with
// one slot per CALL site.
func callSitePCsFor(proto *bytecode.Proto) []int32 {
	if proto == nil || len(proto.Code) == 0 {
		return nil
	}
	var out []int32
	for pc := int32(0); pc < int32(len(proto.Code)); pc++ {
		if bytecode.Op(proto.Code[pc]) == bytecode.CALL {
			out = append(out, pc)
		}
	}
	return out
}

// CallIC storage convention: CalleeProtoID stores (protoID+1). 0 in the
// field means "unpopulated" so the empty-slot sentinel doesn't collide
// with protoID=0 (which is a valid ID — st.protos is zero-indexed).
// The Populate / segment-guard sides both work with the +1 encoding;
// only the accessor CalleeProtoIDValue unbiases it for external
// inspection.

// PopulateCallIC records a Lua callee observation into the slot. Called
// from the exit-reason dispatcher after host.CallBaseline succeeds.
// Race-free by construction, not by atomics: the IC table is owned by
// its nativeCode, which is owned by a single State — the Go-side
// dispatcher and the segment execute serially on one goroutine, so
// Populate never runs concurrently with a segment read. The atomic
// accessors on CalleeProtoID/Misses keep `go test -race` quiet for
// white-box probes; CalleeSegAddr and the Flags OR-in are plain writes
// and would need real synchronization (atomic store + acquire loads in
// the segment) before an IC table could ever be shared across States.
//
// Semantics:
//
//   - Slot empty (CalleeProtoID == 0): populate — writes protoID+1.
//   - Slot occupied with same protoID (as protoID+1): no-op.
//   - Slot occupied with different protoID: transition to Stuck (mono
//     IC — one shape change ends the fast-path budget).
//   - flagIsHost / flagIsVararg / flagNeedsArg: whichever caller
//     observed is OR'd; those callees can never fast-path.
//
// The dispatcher passes the actual observed protoID (raw, unbiased);
// when the callee is a host closure it passes any protoID + flagIsHost
// and the slot enters Stuck.
func (ic *CallIC) Populate(protoID uint32, numParams, maxStack, flags uint8) {
	// Host observation: mark Stuck and return.
	if flags&CallICFlagIsHost != 0 {
		atomic.AddUint32(&ic.Misses, 1)
		atomic.StoreUint32(&ic.CalleeProtoID, 0)
		storeByte(&ic.Flags, CallICFlagStuck|CallICFlagIsHost)
		return
	}
	// Store the observation as protoID+1 so an empty slot is unambiguous.
	storedID := protoID + 1
	prevStored := atomic.LoadUint32(&ic.CalleeProtoID)
	prevFlags := loadByte(&ic.Flags)
	if prevFlags&CallICFlagStuck != 0 {
		// Already stuck — record miss for the probe, don't rewrite.
		atomic.AddUint32(&ic.Misses, 1)
		return
	}
	if prevStored == 0 {
		// First observation: populate.
		ic.CalleeNumParams = numParams
		ic.CalleeMaxStack = maxStack
		storeByte(&ic.Flags, flags)
		atomic.StoreUint32(&ic.CalleeProtoID, storedID)
		return
	}
	if prevStored != storedID {
		// Shape change: transition to Stuck.
		atomic.AddUint32(&ic.Misses, 1)
		storeByte(&ic.Flags, prevFlags|CallICFlagStuck)
		atomic.StoreUint32(&ic.CalleeProtoID, 0)
		return
	}
	// Same shape: no-op (Spike 2+ increments Hits from the segment side).
}

// PopulateHostIntrinsic records a recognized math-intrinsic host closure
// (issue #77) into the slot: it caches the intrinsic kind + the callee
// identity value and sets CallICFlagIsIntrinsic (plus IsHost, so the
// normal Lua-callee fast path still treats it as host). Unlike a plain
// host observation this does NOT go Stuck — the segment's intrinsic fast
// path uses it. A later shape change to a different callee (identity
// guard miss) simply falls to the exit-reason path; if the SAME site
// later observes a different intrinsic value we transition to Stuck so
// the intrinsic guard stops firing on a moving target.
//
// Serialized with the segment read the same way as the rest of the IC
// (single owning State, dispatcher and segment run on one goroutine); the
// identity value + ID are plain writes ordered before the flag store.
func (ic *CallIC) PopulateHostIntrinsic(kind uint8, calleeVal uint64) {
	prevFlags := loadByte(&ic.Flags)
	if prevFlags&CallICFlagStuck != 0 {
		atomic.AddUint32(&ic.Misses, 1)
		return
	}
	if prevFlags&CallICFlagIsIntrinsic != 0 {
		// Already an intrinsic slot. Same callee → no-op; different
		// callee/kind → Stuck (mono IC budget, mirrors the Lua path).
		if ic.IntrinsicCalleeVal == calleeVal && ic.IntrinsicID == kind {
			return
		}
		atomic.AddUint32(&ic.Misses, 1)
		storeByte(&ic.Flags, prevFlags|CallICFlagStuck)
		return
	}
	if atomic.LoadUint32(&ic.CalleeProtoID) != 0 {
		// Slot already holds a Lua callee: a site that mixes a Lua fn and
		// a host intrinsic is polymorphic — go Stuck rather than flip.
		atomic.AddUint32(&ic.Misses, 1)
		storeByte(&ic.Flags, prevFlags|CallICFlagStuck)
		atomic.StoreUint32(&ic.CalleeProtoID, 0)
		return
	}
	// First observation of this intrinsic site.
	ic.IntrinsicID = kind
	ic.IntrinsicCalleeVal = calleeVal
	storeByte(&ic.Flags, CallICFlagIsIntrinsic|CallICFlagIsHost)
}

// CalleeProtoIDValue returns the raw protoID (0-based), or (0, false)
// if the slot is unpopulated / stuck. Consumers inspecting the IC (test
// probes, segment guard emit) should go through this accessor to avoid
// forgetting the +1 bias.
func (ic *CallIC) CalleeProtoIDValue() (uint32, bool) {
	stored := atomic.LoadUint32(&ic.CalleeProtoID)
	if stored == 0 {
		return 0, false
	}
	return stored - 1, true
}

// FlagsFromProto builds the flag byte from a Lua callee's proto meta.
// Never OR's IsHost — the caller distinguishes host vs Lua before
// calling Populate.
func FlagsFromProto(isVararg, needsArg bool) uint8 {
	var f uint8
	if isVararg {
		f |= CallICFlagIsVararg
	}
	if needsArg {
		f |= CallICFlagNeedsArg
	}
	return f
}

// storeByte / loadByte: aligned single-byte reads/writes are atomic on
// amd64/arm64 (the platforms this project targets); sync/atomic doesn't
// expose byte-sized primitives, so the go race detector treats these
// as races if we ever ran with -race across concurrent write and read.
// Segments (Spike 2+ readers) do a whole-uint32 load of the second
// 4-byte word (NumParams/MaxStack/Flags/pad) — the Flags byte is only
// read in Go, so a plain byte write is race-safe under the sequentially
// consistent Go memory model on those architectures.
func storeByte(p *uint8, v uint8) { *p = v }
func loadByte(p *uint8) uint8     { return *p }
