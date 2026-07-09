//go:build wangshu_p4 && (amd64 || arm64)

// translator_native_dispatch.go - arch-shared half of the PJ10 native
// exit-reason protocol (issue #38 / #37).
//
// Both the amd64 and arm64 nativeCode types share the same dispatcher:
// the mmap segment packs (helperCode, a, b, c, pc) into jitCtx.exitArg0,
// sets the segment status to ExitInlineHelper, and RETs; Run's Go-side
// loop unpacks the request, invokes the corresponding host method, and
// reenters the segment at codePage + resumeOff. Only the emit side (how
// the packing instructions are encoded) is arch-specific.
package peroptranslator

import (
	"sync/atomic"
	"unsafe"

	"github.com/Liam0205/wangshu/internal/bytecode"
	jit "github.com/Liam0205/wangshu/internal/gibbous/jit"
	"github.com/Liam0205/wangshu/internal/value"
)

// DispatchHelperCount counts exit-reason round trips handled by
// dispatchHelper across all nativeCode instances. White-box hit counter
// for prove-the-path assertions: a test that expects an op to ride the
// exit-reason protocol (e.g. arm64 GETUPVAL) checks this increments —
// "output is correct" alone can't distinguish the native path from an
// interpreter fallback.
var DispatchHelperCount atomic.Int64

// TailCallRunCount counts HelperTailCall exits handled by the Run
// loops (both arches). TAILCALL terminates the run and is handled
// before dispatchHelper (like HelperReturn), so DispatchHelperCount
// never sees it — prove-the-path tests for issue #52's TAILCALL
// acceptance check this counter instead.
var TailCallRunCount atomic.Int64

// CallInlineFastHitCount counts times the segment-side EmitCallInline
// fast body fired (guard passed → HelperExecutePlainCall exit-reason,
// issue #50 Spike 2 step 4b prove-the-path probe). Incremented from
// the dispatcher's HelperExecutePlainCall case, so a non-zero value
// proves the segment guard succeeded AND the helper actually ran.
var CallInlineFastHitCount atomic.Int64

// CallICPopulateCount and CallICWarmedCount count the CALL IC
// populate path in the exit-reason CALL dispatcher (issue #50
// Spike 1 probe).
//
// CallICPopulateCount: every populateCallIC invocation with a matching
// pc → CallSitePCs slot.
// CallICWarmedCount: subset that transitioned an empty slot to
// populated (first-time observation with a non-zero, non-host protoID).
//
// Both are read by prove-the-path tests to confirm the warmup path
// fires under real workloads. They can't detect Spike 2's segment
// guard (which lands later); they only defend the warmup half of the
// EmitCallInline plumbing.
var (
	CallICPopulateCount atomic.Int64
	CallICWarmedCount   atomic.Int64
)

// CallICSegAddrCount counts times populateCallIC recorded a nonzero
// callee native segment address (issue #50 Spike 5 probe). Nonzero
// proves the callee was native-compiled AND the bridge/host lookup
// chain (NativeCalleeSegAddr → GibbousCodeOf → NativeSegAddrer) works —
// the prerequisite for segment-to-segment dispatch.
var CallICSegAddrCount atomic.Int64

// CallICIntrinsicCount counts times populateCallIC cached a math
// intrinsic host closure (issue #77 prove-the-path probe): nonzero proves
// the observe → registry → PopulateHostIntrinsic warmup chain fires. The
// segment-side hit is counted separately by IntrinsicHitCount.
var CallICIntrinsicCount atomic.Int64

// IntrinsicHitCount counts math-intrinsic fast-path hits from inside the
// mmap segment (issue #77). Incremented via an `inc qword [imm64]` the
// intrinsic body emits, exactly like SegToSegHitCount — proves the inline
// path (not the exit-reason CALL) actually executed.
var IntrinsicHitCount atomic.Int64

// SegToSegHitCount counts segment-to-segment dispatch hits (issue #50
// Spike 5). Incremented directly from the mmap segment via an
// `inc qword [imm64]` the caller fast body emits on the seg2seg path,
// so a nonzero value proves the in-segment `call` into the callee
// segment actually executed (not the exit-reason fallback). The
// address of the underlying int64 is baked into the segment at emit
// time (SegToSegHitCountAddr).
var SegToSegHitCount atomic.Int64

// SegToSegHitCountAddr returns the address of the SegToSegHitCount
// counter's underlying int64, for the emit to bake as an imm64 into an
// in-segment `inc qword [addr]`. atomic.Int64 wraps a single int64 as
// its first field, so &the atomic == &the int64 on the platforms this
// project targets.
func SegToSegHitCountAddr() uint64 {
	return uint64(uintptr(unsafe.Pointer(&SegToSegHitCount)))
}

// IntrinsicHitCountAddr returns the address of the IntrinsicHitCount
// counter's underlying int64, for the intrinsic fast path to bake as an
// imm64 into an in-segment `inc qword [addr]` (issue #77).
func IntrinsicHitCountAddr() uint64 {
	return uint64(uintptr(unsafe.Pointer(&IntrinsicHitCount)))
}

// SegToSegDeoptCount counts top-level seg2seg deopt-redo events (issue
// #50 Spike 5 / issue #66 subtask 3). A seg2seg callee whose in-segment
// guard misses at run time (arith IsNumber, compare, GETTABLE ArrayHit
// shape, nested-CALL cold IC) sets segCallDeopt + rets; the deopt
// propagates up the native call chain, and the TOP of the chain
// (segCallDepth == 0 after decrement) clears the flag and redoes the
// whole call via the exit-reason host path. The caller fast body
// increments this counter on exactly that top-level redo branch, so a
// nonzero value proves the deopt-redo path executed (as opposed to the
// proto never having promoted at all). Address baked into the segment
// at emit time (SegToSegDeoptCountAddr).
var SegToSegDeoptCount atomic.Int64

// SegToSegDeoptCountAddr returns the address of the SegToSegDeoptCount
// counter's underlying int64, mirroring SegToSegHitCountAddr.
func SegToSegDeoptCountAddr() uint64 {
	return uint64(uintptr(unsafe.Pointer(&SegToSegDeoptCount)))
}

// LoopFuelExitCount counts HelperLoopFuel exits handled by the
// dispatcher (issue #102): an in-segment loop back-edge drained
// segCallFuel to zero and round-tripped to host.LoopPreempt. White-box
// prove-the-path probe — a budgeted loop test asserts this increments,
// since "the budget error was raised" alone can't distinguish the
// back-edge fuel guard from some other billing point.
var LoopFuelExitCount atomic.Int64

// dispatchHelper handles a single ExitInlineHelper request from the
// mmap segment. Returns true on success (segment can be re-entered
// at resumeOff), false on error (host method raised → caller returns
// status=1).
//
// The exit-reason protocol packs into exitArg0:
//
//	bits  0..15 : helper code (jit.HelperXxx)
//	bits 16..23 : op arg A (0-255)
//	bits 24..32 : op arg B (0-511)
//	bits 33..41 : op arg C (0-511)
//	bits 42..63 : op pc (0..4M)
//
// Each helper unpacks the fields it needs; the pc field is present
// for all helpers because host methods use it to materialise ci.pc.
func (c *nativeCode) dispatchHelper(base int32) bool {
	DispatchHelperCount.Add(1)
	arg0 := c.jitCtx.ExitArg0()
	helperCode := arg0 & jit.HelperCodeMask
	a := int32((arg0 >> 16) & 0xFF)
	b := int32((arg0 >> 24) & 0x1FF)
	cc := int32((arg0 >> 33) & 0x1FF)
	pc := int32((arg0 >> 42) & 0x3FFFFF)
	switch helperCode {
	case jit.HelperGetTable:
		if st := c.host.GetTable(base, pc, a, b, cc); st != 0 {
			return false
		}
	case jit.HelperSetTable:
		if st := c.host.SetTable(base, pc, a, b, cc); st != 0 {
			return false
		}
	case jit.HelperNewTable:
		if st := c.host.NewTable(base, pc, a, b, cc); st != 0 {
			return false
		}
	case jit.HelperSetList:
		if st := c.host.SetList(base, pc, a, b, cc); st != 0 {
			return false
		}
	case jit.HelperForPrep:
		// FORPREP slow path (issue #78): a loop slot failed the inline
		// IsNumber guard. host.ForPrep coerces init/limit/step per PUC
		// 5.1 (raising "'for' initial value/limit/step must be a
		// number" when coercion fails) and normalizes the slots, so
		// the resumed FORLOOP keeps its numbers-only assumption.
		if st := c.host.ForPrep(base, pc, a); st != 0 {
			return false
		}
	case jit.HelperLoopFuel:
		// In-segment loop back-edge fuel exhausted (issue #102):
		// host.LoopPreempt bills the spent fuel to the step budget,
		// refills, and raises "instruction budget exceeded" /
		// "context canceled" when tripped. On 0 the segment resumes
		// at the back-edge continuation.
		LoopFuelExitCount.Add(1)
		if st := c.host.LoopPreempt(c.jitCtx, base, pc); st != 0 {
			return false
		}
	case jit.HelperGetUpval:
		// GETUPVAL A B: R(A) := U(B). Never raises.
		c.host.SetReg(a, c.host.GetUpval(base, b))
	case jit.HelperSetUpval:
		// SETUPVAL A B: U(B) := R(A). Never raises.
		c.host.SetUpvalFromReg(base, a, b)
	case jit.HelperUnm:
		if st := c.host.Unm(base, pc, b, a); st != 0 {
			return false
		}
	case jit.HelperCall:
		// Snapshot the callee observation BEFORE dispatching to
		// host.CallBaseline: R(A) after CallBaseline is overwritten
		// by the return-value moveResults, so any post-call read
		// would see the first return value's tag, not the callee
		// closure. Populate the IC once the call succeeds — a raise
		// still gets to record the miss so shape-change tracking
		// isn't blind to error paths.
		observed := c.snapshotCallCallee(base, a)
		if st := c.host.CallBaseline(base, pc, a, b, cc); st != 0 {
			return false
		}
		c.populateCallIC(pc, observed)
	case jit.HelperExecutePlainCall:
		CallInlineFastHitCount.Add(1)
		callA := a
		nargs := b & 0xFF
		// nresults rides the full 9-bit c slot: emitCallInline packs
		// (a=callA, b=nargs, c=nresults) through the standard
		// emitExitReason layout, and cc was already masked with 0x1FF
		// by the unpacking above. A narrower mask here would silently
		// truncate CALLs expecting >= 16 fixed results (PR #62 review
		// finding).
		nresults := cc
		if st := c.host.ExecutePlainCallInlineFrame(base, callA, nargs, nresults); st != 0 {
			return false
		}
	case jit.HelperGetGlobal:
		// bx split across b (low 9) and c (high 9) slots.
		if st := c.host.DoGetGlobal(base, pc, a, b|(cc<<9)); st != 0 {
			return false
		}
	case jit.HelperSetGlobal:
		if st := c.host.DoSetGlobal(base, pc, a, b|(cc<<9)); st != 0 {
			return false
		}
	case jit.HelperArithSlow:
		// Arith guard-miss / non-inline slow path (both arches; issue
		// #45 removed amd64's legacy in-segment shim fallback). The
		// packed fields carry (a, b, c); the op is re-derived from
		// proto.Code[pc] — it doesn't fit the packing and the bytecode
		// is immutable, so the lookup is exact.
		op := int32(bytecode.Op(c.proto.Code[pc]))
		if st := c.host.Arith(base, pc, op, b, cc, a); st != 0 {
			return false
		}
	case jit.HelperLen:
		if st := c.host.Len(base, pc, b, a); st != 0 {
			return false
		}
	case jit.HelperConcat:
		if st := c.host.Concat(base, pc, a, b, cc); st != 0 {
			return false
		}
	case jit.HelperSelf:
		if st := c.host.Self(base, pc, a, b, cc); st != 0 {
			return false
		}
	case jit.HelperCompareSlow:
		// Compare guard-miss slow path (LT/LE with non-number operands:
		// string ordering / __lt / __le metamethods; EQ rides the same
		// helper — host.Compare's doCompare handles all three ops, re-
		// derived from proto.Code[pc]). host.Compare returns packed
		// bit0=result, bit1=err. The result is handed back to the
		// segment through exitArg0: the compare emit's resume block
		// reads it and branches to the exec/skip successor (the branch
		// decision must happen inside the segment — the dispatcher has
		// no notion of BB targets).
		op := int32(bytecode.Op(c.proto.Code[pc]))
		packed := c.host.Compare(base, pc, op, b, cc)
		if packed&2 != 0 {
			return false
		}
		c.jitCtx.SetExitArg0(uint64(packed & 1))
	case jit.HelperTForLoop:
		// TFORLOOP A C (issue #52): host.TForLoop invokes the iterator
		// R(A)(R(A+1), R(A+2)), writes R(A+3..A+2+C), and updates the
		// control variable. Its i64 return is a tri-state: >= 0 means
		// continue (the value is the refreshed frame base for P3 wasm's
		// benefit — the P4 Run loop refreshes addrs itself, so only the
		// sign matters here), -2 means exit (first result nil), -1
		// means error raised. The continue verdict rides exitArg0 back
		// into the segment (1 = continue, 0 = exit), mirror of
		// HelperCompareSlow's branch-result protocol.
		ret := c.host.TForLoop(base, pc, a, cc)
		if ret == -1 {
			return false
		}
		if ret >= 0 {
			c.jitCtx.SetExitArg0(1)
		} else {
			c.jitCtx.SetExitArg0(0)
		}
	case jit.HelperClosure:
		// CLOSURE A Bx (issue #52): host.Closure builds the closure
		// into R(A), consuming the pseudo-instruction words after
		// CLOSURE (one MOVE/GETUPVAL per upvalue) via ci.pc. Bx rides
		// the 18-bit b|c split (same packing as GETGLOBAL).
		if st := c.host.Closure(base, pc, a, b|(cc<<9)); st != 0 {
			return false
		}
	case jit.HelperClose:
		// CLOSE A (issue #52): close all open upvalues >= R(A).
		if st := c.host.Close(base, pc, a); st != 0 {
			return false
		}
	default:
		return false
	}
	return true
}

// hostIfaceHeader extracts the (itab, data) header from a P4HostState
// interface value. Same pattern as e2e_shim_ops_amd64_test.go's
// hostToIfaceHeader but callable from production code.
func hostIfaceHeader(h jit.P4HostState) [2]uintptr {
	return *(*[2]uintptr)(unsafe.Pointer(&h))
}

// snapshotCallCallee delegates to the host observe. Kept as a
// dispatcher method so future paths can inject test overrides without
// touching the host interface. The observation must be taken BEFORE
// host.CallBaseline dispatches — the callee slot is overwritten by
// return values once the call completes.
func (c *nativeCode) snapshotCallCallee(base int32, a int32) uint64 {
	if c.host == nil {
		return 0
	}
	return c.host.ObserveCallCallee(base, int32(a))
}

// populateCallIC updates the per-CALL-site inline cache with an
// observation snapshotted via snapshotCallCallee. observed is the
// packed uint64 from P4HostState.ObserveCallCallee — see the interface
// doc for the layout.
//
// The dispatcher never gates on IC state today (Spike 1); populate
// still runs so the probes (CallICPopulateCount / CallICWarmedCount)
// prove the warmup path fires before Spike 2 wires the segment guard.
func (c *nativeCode) populateCallIC(pc int32, observed uint64) {
	if len(c.callICs) == 0 {
		return
	}
	// Find the slot: linear scan is fine for the small N (Lua
	// functions typically have < 8 CALL sites; the hottest kernels
	// have 2). Bail on any miss instead of erroring — populate is
	// probe-only in Spike 1 and must not break correctness.
	idx := -1
	for i, sitePC := range c.callSitePCs {
		if sitePC == pc {
			idx = i
			break
		}
	}
	if idx < 0 {
		return
	}
	CallICPopulateCount.Add(1)
	// Math-intrinsic host closure (issue #77): kind rides bits 56..63 and
	// the closure GCRef rides bits 0..47. Reconstruct the full callee
	// value (MakeGC(TagFunction, gcref)) for the segment's identity guard
	// and cache the intrinsic instead of going Stuck like a plain host
	// closure. Takes precedence over the generic host handling below.
	if kind := uint8(observed >> 56); kind != 0 && uint8(observed>>48)&CallICFlagIsHost != 0 {
		gcref := observed & 0x0000_FFFF_FFFF_FFFF
		calleeVal := uint64(value.TagFunction)<<48 | gcref
		c.callICs[idx].PopulateHostIntrinsic(kind, calleeVal)
		if c.callICs[idx].Flags&CallICFlagStuck == 0 {
			CallICIntrinsicCount.Add(1)
		}
		return
	}
	protoID := uint32(observed)
	numParams := uint8(observed >> 32)
	maxStack := uint8(observed >> 40)
	flags := uint8(observed >> 48)
	prevStored := c.callICs[idx].CalleeProtoID
	prevFlags := c.callICs[idx].Flags
	c.callICs[idx].Populate(protoID, numParams, maxStack, flags)
	// Warmed transition: empty slot → populated with a valid Lua callee
	// observation (stored non-zero, no stuck flag). Uses the +1-biased
	// storage: empty is exactly 0.
	if prevStored == 0 && prevFlags&CallICFlagStuck == 0 &&
		c.callICs[idx].CalleeProtoID != 0 &&
		c.callICs[idx].Flags&CallICFlagStuck == 0 {
		CallICWarmedCount.Add(1)
	}
	// Spike 5: record the callee's native segment entry address (0 when
	// the callee isn't native-compiled). Only meaningful once the slot
	// holds a valid Lua callee; a stuck / host slot keeps CalleeSegAddr
	// at 0 so the segment-to-segment fast path never routes into it.
	if c.host != nil && c.callICs[idx].CalleeProtoID != 0 &&
		c.callICs[idx].Flags&CallICFlagStuck == 0 {
		segAddr := c.host.NativeCalleeSegAddr(protoID)
		c.callICs[idx].CalleeSegAddr = segAddr
		if segAddr != 0 {
			CallICSegAddrCount.Add(1)
			// Only a never-exits native callee is eligible for
			// segment-to-segment dispatch. OR the flag in (leaving the
			// shape flags intact).
			if c.host.CalleeNeverExitsSegment(protoID) {
				c.callICs[idx].Flags |= CallICFlagNeverExits
			}
		}
	} else {
		c.callICs[idx].CalleeSegAddr = 0
	}
}
