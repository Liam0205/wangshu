//go:build wangshu_p4

// Package jit —— the P4 compiler core (wangshu_p4 build).
//
// PJ0 stage: SupportsAllOpcodes is all-false ⇒ every Proto still runs on
// crescent.
// PJ2 wired version: Compile recognizes the simplest shape
// "LOADK A K(0); RETURN A 1" and emits an mmap segment; p4Code.Run fetches
// RAX via callJITFull and writes it back to R(A) — but SupportsAllOpcodes
// is **still all-false** ⇒ the bridge never reaches Compile on the main
// library path, and this path is only exercised by PJ2's internal
// prove-the-path unit test (per implementation-progress.md §6 PJ2 scope
// ruling).
//
// Full crescent end-to-end byte-equal wiring is deferred to PJ3+
// (SupportsAllOpcodes opens a whitelist + the crescent.enterGibbousJIT
// path + accompanying -race / difftest validation).
package jit

import (
	"errors"

	"github.com/Liam0205/wangshu/internal/bridge"
	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/value"
)

// Compiler implements the `bridge.P3Compiler` interface
// (`p2-bridge/05-p3-p4-interface.md` §2).
type Compiler struct {
	// hostState is the injected host (crescent.State) abstraction, used by
	// p4Code.Run to pop frames.
	//
	// **per-Compiler singleton** (per the wireP4 single-goroutine calling
	// contract): each State gets its own *Compiler; wireP4 injects the
	// *State via SetHostState; when Compile produces a p4Code it copies this
	// field into p4Code.host; p4Code.Run uses its own held host, independent
	// of other States' *p4Code (no concurrent write, V18 -race friendly).
	hostState P4HostState

	// PJ3+ field slots:
	//   - codePagePool *codePagePool  // exec mmap code page pool (05 §2.1)
	//   - emitter      *amd64.Emitter // per-arch emitter (06 §2.4)
	//   - state        *p4SpecState   // P4 speculative sub state machine (03 §4 option A)
	//
	// Left empty in PJ2 (p4Code holds its own codePage, Compiler state free).
}

// New constructs the P4 Compiler.
func New() *Compiler {
	return &Compiler{}
}

// MinPromotableLen implements bridge.MinPromotableLener. P4 dispatches
// through a direct mmap CALL — cheaper than P3's wasm module boundary —
// but nativeCode.Run still pays fixed per-call costs (codePage refcount
// atomics, jitCtx addr refresh, Go-side DoReturn). Measured on the
// 3-op const-predicate shape: promoted 111 ns vs interpreter 78 ns, so
// tiny protos still lose. Keep the same floor as the package default;
// this override is a hook for future tuning once the fixed Run costs
// shrink.
func (c *Compiler) MinPromotableLen() int { return 10 }

// ExemptFromFloor implements bridge.FloorExempter (issue #67): a
// seg2seg-eligible proto is exempt from the short-proto floor. The
// floor's calibration (tiny protos lose to nativeCode.Run's fixed
// per-call costs) measured the host dispatch channel; a seg2seg
// callee's hot channel is an in-segment call from an already-promoted
// caller, which skips those costs entirely. Flooring such a proto is
// strictly worse: every call from a promoted caller then pays an
// ExecutePlainCall exit-reason round trip instead (spectral-norm's
// 9-op `A(i,j)`: 144k round trips per run, auto 3.7x slower than
// force). The occasional interpreter-frame entry still pays the Run
// overhead, but for the "hot promoted caller + tiny pure callee"
// shape that triggers this path, the in-segment channel dominates.
//
// Consulted by the bridge in auto mode only, once per proto past the
// heat threshold (verdict cached bridge-side), so the eligibility
// scan's cost is off the steady-state path.
func (c *Compiler) ExemptFromFloor(proto *bytecode.Proto) bool {
	return perOpSeg2SegAnalyzer != nil && perOpSeg2SegAnalyzer(proto)
}

// SupportsAllOpcodes checks whether every opcode in the Proto is within the
// backend's supported set.
//
// **PJ7 wired implementation**: opens the whitelist to the "single value
// production + RETURN A 1" single-BB shape — this is the Lua subset that
// the spike gate ⊕ trampoline ⊕ emitter trio plus the Go-side frame teardown
// mechanism can byte-equal validate.
//
// Supported shapes (must satisfy: Code length == 2, second insn RETURN A 1):
//   - LOADK A K(Bx); RETURN A 1 (constant return, **including string
//     constants** — proto.Consts[bx] is already a NaN-box GCRef, see the
//     string-section note in analyzeShape)
//   - LOADBOOL A B 0; RETURN A 1 (bool return, C=0 no skip)
//   - LOADNIL A A; RETURN A 1 (single nil return, A==B)
//   - MOVE A B / GETUPVAL A B / ADD..POW A B C + RETURN A 2 (see
//     analyzeShape)
//
// **Key point**: the constant family (LOADK/LOADBOOL/LOADNIL) share the
// property that "R(A)'s final NaN-box u64 value can be computed at compile
// time" (the mmap segment only emits mov rax, imm64; ret); the
// MOVE/GETUPVAL/arithmetic families instead go through the Go-side prelude
// path to call a host helper, and the mmap segment is just a placeholder
// trampoline.
//
// PJ8+ startup expands supported (register IsNumber guard speculation +
// direct table IC slot, etc.), which needs jitContext load/store of the
// value stack + a speculative deopt protocol — deferred to the next stage.
func (c *Compiler) SupportsAllOpcodes(proto *bytecode.Proto) bool {
	if analyzeShape(proto).ok {
		return true
	}
	// PJ10 per-op translator fall-through: shapes PJ7's analyzeShape
	// rejects may still be in PJ10's supported subset (constant tuples
	// of more than one return value, etc.). The hook is nil when the
	// peroptranslator sub-package is not imported, preserving exact PJ7
	// behaviour. See internal/gibbous/jit/perop_hook.go.
	if perOpAnalyzer != nil && perOpAnalyzer(proto) {
		return true
	}
	return false
}

// shapeInfo is analyzeShape's return value — the P4 PJ7 shape recognition
// result.
type shapeInfo struct {
	ok         bool   // shape is valid
	retA       uint8  // RETURN A register number
	retB       uint8  // RETURN B field
	retPC      uint8  // RETURN instruction pc
	value      uint64 // R(retA)'s NaN-box u64 value (burned into the mmap segment when writeRetA=true)
	writeRetA  bool   // whether the RAX returned by the mmap segment needs to be written to R(retA)
	preludeOp  uint8  // prelude opcode before RETURN (0=none, GETUPVAL=4 / ADD=12 / SUB=13 / GETGLOBAL=5 / SETGLOBAL=7 / SETTABLE=9 etc.)
	preludeArg uint32 // the prelude opcode's B field (GETUPVAL/UNM/LEN are register numbers 0-255; arithmetic family B is RK 0-511; NEWTABLE B is Fb 0-255; GETGLOBAL/SETGLOBAL are Bx 0-262143, needs 18-bit)
	preludeC   uint16 // arithmetic family / table family prelude's C field — may be RK (constants carry a 256 offset), 0-511
	cmpA       uint8  // comparison-fold shape: the A field of EQ/LT/LE (0=negate result / 1=take result directly, used to fold into BoolValue(packed.bit0 == cmpA))
	// two-stage arithmetic chain shape (MUL+ADD+RETURN etc.): second-stage arithmetic op + B + C
	chainOp uint8  // second-stage op (0=no chain; ADD/SUB/MUL/DIV/MOD/POW)
	chainB  uint16 // second-stage B field (RK 0-511)
	chainC  uint16 // second-stage C field (RK 0-511)

	// PJ3 FORLOOP byte-level inline shape recognition (empty body / all-constant init/limit/step):
	//   - isForLoop = true: this shape is a FORLOOP shape, Compile takes the
	//     emit FORLOOP template (float idx+=step / ucomisd limit / backward jcc) path
	//   - forA: the A field of FORPREP/FORLOOP (R(A)..R(A+3) are idx/limit/step/i).
	//     **The current empty-body shape emit does not read forA** (the template
	//     only burns forInitK/forLimitK/forStepK into imm64, and does not address
	//     the R(A) slot); **deferred to the PJ3+ body inline expansion**, where
	//     forA is needed to compute the R(A+3)=i slot offset for body-internal refs.
	//   - forInitK / forLimitK / forStepK: the three constant NaN-box raw bits (burned into imm64 at compile time)
	//   - forLimitReg + forLimitIsReg: reg-limit shape uses R(limitReg) instead of K
	//   - forLimitUpvalIdx: the upvalue index + 1 for the upvalue-limit shape (1-based;
	//     0 means the upvalue path is not taken, going directly to the MOVE/LOADK shape).
	//     The Run side first calls host.GetUpval(idx-1) to write the R(forLimitReg)
	//     slot, then callJITSpec takes the reg-limit template byte-level inline.
	//   - hasBody + bodyOp/bodyKValue/forBodyAS: body contains a single reg-K op shape
	//     (`s = s op K`): when hasBody=true the template contains init R(aS)=K_s + body inline.
	isForLoop        bool
	forA             uint8
	forInitK         uint64
	forLimitK        uint64
	forStepK         uint64
	forLimitReg      uint8 // the source register number when limit is a reg (forLimitIsReg=true)
	forLimitIsReg    bool  // true = limit is read from R(forLimitReg) + IsNumber guard; false = K burned into imm at compile time
	forLimitUpvalIdx uint8 // the upvalue index + 1 for the upvalue-limit shape (0 = not the upval path)
	hasBody          bool  // true = FORLOOP contains a reg-K body op
	bodyOp           uint8 // the body's SSE op byte (SseOpAddsd / Subsd / Mulsd / Divsd)
	bodyKValue       uint64
	forBodyAS        uint8  // the body's R(aS) register number (the s slot)
	forBodyKS        uint64 // the initial K value of R(aS) under the body shape (K_s)
	// two-stage body shape (2 reg-K ops sharing R(aS); the body2 template reuses
	// xmm3 across both stages to save one load/store):
	hasBody2    bool   // true = two-stage body shape (`s=s op1 K1; s=s op2 K2`)
	bodyOp2     uint8  // second-stage op SSE byte
	bodyKValue2 uint64 // second-stage K value

	// PJ4 table IC ArrayHit shape (`function(t) return t[K] end`):
	//   - icArrayHit = true: takes the PJ4 IC direct-slot inline template
	//   - icAReg / icBReg: GETTABLE A B
	//   - icStableShape / icStableIndex: frozen at compile time from feedback / IC slot
	icArrayHit    bool
	icAReg        uint8
	icBReg        uint8
	icStableShape uint32
	icStableIndex uint32

	// PJ4 table IC NodeHit shape (mirrors ArrayHit but IC kind=NodeHit, hash section):
	//   - icNodeHit = true: takes the PJ4 IC NodeHit byte-level direct-slot inline template
	//   - icStableKey: freezes the stableKey NaN-box from proto.Consts[KIdx] at compile time,
	//     the template verifies NodeKey == stableKey inside to guard against key degradation
	icNodeHit   bool
	icStableKey uint64

	// PJ4 table IC SETTABLE ArrayHit shape (`function(t,v) t[K] = v end`):
	//   - icSetArrayHit = true: takes the PJ4 SETTABLE IC byte-level inline reverse-write template
	//   - icSetCReg: the value register number R(C) (C<256, a reg not a constant)
	icSetArrayHit bool
	icSetCReg     uint8

	// PJ4 SELF IC ArrayHit shape (the leading SELF of `function(obj) obj:method() end`):
	//   - icSelfArrayHit = true: takes the PJ4 SELF IC byte-level inline template (139 bytes)
	//   - reuses icAReg (SELF.A, the method result) / icBReg (SELF.B, obj) /
	//     icStableShape / icStableIndex fields. R(A+1) is copied from R(B) by the template.
	icSelfArrayHit bool

	// PJ4 SETTABLE NodeHit shape (`function(t, v) t["x"] = v end`):
	//   - icSetNodeHit = true: takes the PJ4 SETTABLE IC NodeHit byte-level inline template
	//     (140 bytes, hash-section NodeKey compare + reverse store NodeVal)
	//   - reuses icSetCReg (value reg) / icStableShape / icStableIndex / icStableKey
	icSetNodeHit bool

	// PJ4 SELF NodeHit shape (`function(obj) obj:method() end`, the truly common OOP call):
	//   - icSelfNodeHit = true: takes the PJ4 SELF IC NodeHit byte-level inline template
	//     (166 bytes, SELF ArrayHit 139 + key compare 27)
	//   - reuses icAReg (SELF.A i.e. the method result) / icBReg (SELF.B i.e. obj) /
	//     icStableShape / icStableIndex / icStableKey fields
	icSelfNodeHit bool

	// PJ5 CALL void shape (`function(g) g() end` class):
	//   - isCallVoid = true: the Run side prelude path calls host.CallBaseline to
	//     complete the baseline CALL (byte-equal to P1 doCall dispatch, covering
	//     host/crescent/__call/gibbous)
	//   - isCallUpval = true: shape B, i.e. GETUPVAL+CALL+RETURN void (the callee
	//     source is an upvalue, such as an outer local fn); false = shape A
	//     MOVE+CALL+RETURN void (the callee source is a parameter / local reg)
	//   - callA / callB / callC: the three CALL A B C fields passed straight to host.CallBaseline
	//
	// **retA / retPC field settings** (setter shape returns 0 values, same as the
	// existing setter paths SETTABLE/SETGLOBAL): retA=0 (the Run path does not read
	// retA, since the setter shape's host.DoReturn does not write R(A)); retPC=2
	// (RETURN is at pc 2, CALL itself is at pc=1 which the Run side computes as
	// callPC from retPC-1, derived inside the prelude switch CALL case).
	//
	// preludeArg: for shape A = MOVE.B (source reg) / for shape B = GETUPVAL.B
	// (upvalue index)
	//
	// Shape recognition is in analyzeCallVoidForm, the typical luac compiled shapes (length 3, 4, 5):
	//   shape A0/B0: 0 args 0 returns (length 3)
	//   shape A1K/B1K: 1 K arg 0 returns (length 4)
	//   shape A1R/B1R: 1 reg arg 0 returns (length 4)
	//   shape AR1/BR1: 0 args 1 return getter (length 4 with a dead RETURN)
	//   shape A2K/B2K: 2 K args 0 returns (length 5, expanded in this batch)
	isCallVoid     bool
	isCallUpval    bool
	callA          uint8
	callB          uint8
	callC          uint8
	callArgCount   uint8 // 0 / 1 / 2 / 3
	callMultiRet   uint8 // N=0/1 existing shape (setter/getter 1 return); N>=2 means an N-return-value getter
	callArg1IsK    bool
	callArg1K      uint64
	callArg1RegSrc uint8
	callArg2IsK    bool
	callArg2K      uint64
	callArg2RegSrc uint8
	callArg3IsK    bool
	callArg3K      uint64
	callArg3RegSrc uint8
	callArg4IsK    bool
	callArg4K      uint64
	callArg4RegSrc uint8
	callArg5IsK    bool
	callArg5K      uint64
	callArg5RegSrc uint8
	callArg6IsK    bool
	callArg6K      uint64
	callArg6RegSrc uint8
	callArg7IsK    bool
	callArg7K      uint64
	callArg7RegSrc uint8

	// PJ5 TAILCALL shape (`function() return f() end` class):
	//   - isTailCall = true: the Run side prelude path calls host.TailCall with a
	//     three-way branch (0=Lua tail completed, skip DoReturn / 1=ERR / 2=host
	//     lands the tail, with a dead RETURN B=0 to-top)
	//
	// luac stmtReturn (frontend/compile/stmt.go::stmtReturn) translates a return of
	// a single CallExpr into TAILCALL + RETURN A B=0 (dead trailer) + an implicit
	// RETURN A=0 B=1. The shape reuses the same field set as CALL void
	// (callA/callB/callC/callArgCount/callArg1*/callArg2K + isCallUpval + preludeArg)
	// but retPC points to the dead RETURN B=0 to-top rather than the setter
	// RETURN B=1; this frame is completed by host.TailCall or forwarded by the dead
	// RETURN to-top.
	//
	// Mutually exclusive with isCallVoid (preludeOp=CALL → isCallVoid;
	// preludeOp=TAILCALL → isTailCall). Shape recognition is in analyzeTailCallForm.
	isTailCall bool

	// PJ5 SELF method call inline shape (`obj:method(args)` class):
	//   - isSelfCall = true: the Run side prelude path calls host.Self +
	//     (CallBaseline|TailCall) to complete SELF + CALL/TAILCALL byte-equal to
	//     P1 doCall dispatch.
	//   - selfCallA / selfMethodRK: SELF.A (the method result) / SELF.C (the RK
	//     method-name constant index)
	//   - selfRecvSrcReg / selfRecvIsUpval: whether the receiver comes from
	//     R(selfRecvSrcReg) or an upvalue index (luac emits MOVE/GETUPVAL before
	//     SELF to load the recv into R(SELF.A)=R(SELF.B))
	//
	// Reuses the isTailCall / callA / callB / callC / callArgCount /
	// callArg1*..callArg7* fields — SELF + CALL = isCallVoid=false isTailCall=false
	// isSelfCall=true CALL branch; SELF + TAILCALL = isTailCall=true isSelfCall=true
	// TAILCALL branch.
	//
	// Shape recognition is in analyzeSelfCallForm.
	isSelfCall      bool
	selfCallA       uint8  // SELF.A = the method result register (same as callA)
	selfMethodRK    uint16 // SELF.C field (RK method-name constant index 0-511)
	selfRecvSrcReg  uint8  // recv source reg (form M*) / upvalue index (form U*)
	selfRecvIsUpval bool   // true = recv comes from an upvalue; false = from a reg

	// PJ5 SELF + CALL spec template wiring (per §9.10, reusing PJ4 EmitSelfNodeHit):
	//   - useSpecSelfCall = true: the SELF section takes the byte-level
	//     EmitSelfNodeHit template (IC NodeHit guard + stableKey compare + NodeVal
	//     store R(A)=method), skipping the host.Self round-trip; on failure it
	//     deopts down to host.Self. The CALL section still takes host.CallBaseline.
	//   - reuses icAReg/icBReg/icStableShape/icStableIndex/icStableKey (same field
	//     set as PJ4 SELF NodeHit).
	useSpecSelfCall bool

	// PJ5 Option B Spike 1/2/3/4 frame-build inlining (per §9.20 + commit-5p/5q/5r):
	//   - useFrameInline = true: the CALL section goes through the mmap byte-level
	//     enterLuaFrame inline (BuildVoid0ArgSkeleton + ExitHelperRequest +
	//     PopVoid0ArgSkeleton), replacing the host.CallBaseline round-trip.
	//   - **guards** (per §9.20.4 + commit-5p Spike 2 expansion):
	//     - archSupportsFrameInline()=true (amd64)
	//     - callArgCount <= 7 (Spike 2 N-arg fixed-args expansion, original Spike 1 limited to 0)
	//     - isCallVoid (preludeOp=CALL covers the setter/getter/multi-ret shapes)
	//     - !isTailCall (avoids complicating the frame-stack semantics)
	//     - IC NodeHit + FBSelfMono (stacked on top of the useSpecSelfCall guard)
	//   - vararg callee auto-compatible (Spike 3): enterLuaFrame internally handles
	//     the vararg lower-region rearrangement per NumParams<nargs, the helper API
	//     needs no expansion
	//   - multi-return multi-shape (Spike 4): nresults = callC - 1 (callC=1=0 returns
	//     / 2=1 return / 3..16 = N=2..15 return drop multi-ret), the helper takes an
	//     nresults argument
	//   - not passed → falls back to useSpecSelfCall (SELF section inline + host.CallBaseline)
	//   - **Spike 1/2/3/4 amd64 fully wired end-to-end** (commit-5m..5r): RunHits
	//     prove-the-path hits confirmed (49/199/99/99 per Spike)
	useFrameInline bool
}

// analyzeGetTableArrayHit recognizes the PJ4 IC ArrayHit shape:
// `function(t) return t[K] end` (GETTABLE A B C(constant K idx) + RETURN A 2).
//
// **Complementary** to analyzeShape's GETTABLE path: analyzeShape takes the
// host.GetTable slow path; this function takes the byte-level IC ArrayHit
// direct-slot inline, skipping the hash.
//
// **Trigger conditions** (returns true only if all hold):
//   - Code length 2 or 3 ([0]=GETTABLE / [1]=RETURN / [2]?=dead RETURN)
//   - GETTABLE A B C: A==RETURN.A, B<=254, C>=256 (K constant index)
//   - RETURN A=GETTABLE.A B=2
//   - proto.IC[0].Kind == ICKindArrayHit (the P1 interpreter observed an array hit)
//   - feedback.Points[0].Kind == FBTableMono (stable mono after P2 aggregation)
//   - feedback.Points[0].Confidence >= 0.99 (speculation threshold)
//   - feedback / proto.IC stableShape & stableIndex agree (consistent when no race)
//
// Any failing condition returns (shapeInfo{}, false) — takes the original
// analyzeShape path (host helper).
func analyzeGetTableArrayHit(proto *bytecode.Proto, feedback *bridge.TypeFeedback) (shapeInfo, bool) {
	codeLen := len(proto.Code)
	if codeLen != 2 && codeLen != 3 {
		return shapeInfo{}, false
	}
	if bytecode.Op(proto.Code[0]) != bytecode.GETTABLE ||
		bytecode.Op(proto.Code[1]) != bytecode.RETURN {
		return shapeInfo{}, false
	}
	if codeLen == 3 && bytecode.Op(proto.Code[2]) != bytecode.RETURN {
		return shapeInfo{}, false
	}
	gtA := bytecode.A(proto.Code[0])
	gtB := bytecode.B(proto.Code[0])
	gtC := bytecode.C(proto.Code[0])
	retA := bytecode.A(proto.Code[1])
	retB := bytecode.B(proto.Code[1])
	if gtA != retA || retB != 2 {
		return shapeInfo{}, false
	}
	if gtB > 254 || gtC < 256 {
		// C must be a constant index (>=256) — otherwise the key is a dynamic
		// reg, and the IC slot may rotate to different keys, so byte-level
		// inline cannot assume stableIndex
		return shapeInfo{}, false
	}
	// IC slot check (proto.IC length = len(proto.Code))
	if len(proto.IC) <= 0 {
		return shapeInfo{}, false
	}
	icSlot := proto.IC[0]
	if icSlot.Kind != bytecode.ICKindArrayHit {
		return shapeInfo{}, false
	}
	// feedback check (may be nil — on the main path wireP4 passes it via
	// ProfileData, but jit-package unit tests may pass nil)
	if feedback == nil || len(feedback.Points) < 1 {
		return shapeInfo{}, false
	}
	pf := feedback.Points[0]
	if pf.Kind != bridge.FBTableMono || pf.Confidence < 0.99 {
		return shapeInfo{}, false
	}
	// stableShape / stableIndex must agree (feedback and IC slot are same-source,
	// but a race may cause slight divergence; require strict agreement to speculate)
	if pf.StableShape != icSlot.Shape || pf.StableIndex != icSlot.Index {
		return shapeInfo{}, false
	}

	return shapeInfo{
		ok:            true,
		retA:          uint8(retA),
		retB:          uint8(retB),
		retPC:         1,
		preludeOp:     uint8(bytecode.GETTABLE), // Run-side deopt takes host.GetTable
		preludeArg:    uint32(gtB),
		preludeC:      uint16(gtC),
		icArrayHit:    true,
		icAReg:        uint8(retA),
		icBReg:        uint8(gtB),
		icStableShape: pf.StableShape,
		icStableIndex: pf.StableIndex,
	}, true
}

// analyzeGetTableNodeHit recognizes the PJ4 IC NodeHit shape:
// `function(t) return t["x"] end` (GETTABLE A B C(constant K idx) + RETURN A 2),
// where IC[0].Kind=NodeHit (the P1 interpreter hit the hash section, not the array section).
//
// Almost the same trigger conditions as analyzeGetTableArrayHit, differences:
//   - proto.IC[0].Kind == ICKindNodeHit (the P1 interpreter observed a hash hit)
//   - freeze stableKey = proto.Consts[KIdx] at compile time: the NodeHit template
//     needs to verify NodeKey == stableKey to guard against key degradation
//     (__index chain / rehash and similar scenarios)
//
// **stableKey compile-time freeze conditions**:
//   - proto.Consts index is valid (KIdx < len(Consts))
//   - that Const is not Nil (LoadProgram has already interned string constants,
//     and number constants are set up at compile time; a Nil slot is abnormal —
//     do not speculate)
//
// Any failing condition returns (shapeInfo{}, false) — takes the analyzeShape
// host.GetTable path (byte-equal to P1).
func analyzeGetTableNodeHit(proto *bytecode.Proto, feedback *bridge.TypeFeedback) (shapeInfo, bool) {
	codeLen := len(proto.Code)
	if codeLen != 2 && codeLen != 3 {
		return shapeInfo{}, false
	}
	if bytecode.Op(proto.Code[0]) != bytecode.GETTABLE ||
		bytecode.Op(proto.Code[1]) != bytecode.RETURN {
		return shapeInfo{}, false
	}
	if codeLen == 3 && bytecode.Op(proto.Code[2]) != bytecode.RETURN {
		return shapeInfo{}, false
	}
	gtA := bytecode.A(proto.Code[0])
	gtB := bytecode.B(proto.Code[0])
	gtC := bytecode.C(proto.Code[0])
	retA := bytecode.A(proto.Code[1])
	retB := bytecode.B(proto.Code[1])
	if gtA != retA || retB != 2 {
		return shapeInfo{}, false
	}
	if gtB > 254 || gtC < 256 {
		// C must be a constant index (>=256); otherwise the key is a
		// dynamic reg and the IC slot may rotate through different keys.
		return shapeInfo{}, false
	}
	// IC slot check
	if len(proto.IC) <= 0 {
		return shapeInfo{}, false
	}
	icSlot := proto.IC[0]
	if icSlot.Kind != bytecode.ICKindNodeHit {
		return shapeInfo{}, false
	}
	// feedback check
	if feedback == nil || len(feedback.Points) < 1 {
		return shapeInfo{}, false
	}
	pf := feedback.Points[0]
	if pf.Kind != bridge.FBTableMono || pf.Confidence < 0.99 {
		return shapeInfo{}, false
	}
	if pf.StableShape != icSlot.Shape || pf.StableIndex != icSlot.Index {
		return shapeInfo{}, false
	}

	// **stableKey compile-time freeze** (one step more than ArrayHit for NodeHit):
	// take the NaN-box key from proto.Consts[KIdx] (LoadProgram has already
	// interned the string).
	kIdx := bytecode.KIdx(int(gtC))
	if kIdx < 0 || kIdx >= len(proto.Consts) {
		return shapeInfo{}, false
	}
	stableKey := uint64(proto.Consts[kIdx])
	// **Nil slot check**: `value.Nil = 0xFFF8_0000_0000_0000` (TagNil=0xFFF8,
	// per internal/value/value.go::Nil). A string slot LoadProgram has not
	// finished loading is genuinely Nil (non-zero). Note: **do not use
	// stableKey == 0 as a sentinel** — the IEEE 754 number key 0.0 NaN-box is
	// 0x0000_0000_0000_0000, which collides with the sentinel type, so the
	// number key `t[0]` would be wrongly rejected for speculation (fixed in this
	// repo per external review feedback in commit c7034b2).
	if stableKey == uint64(value.Nil) {
		return shapeInfo{}, false
	}

	return shapeInfo{
		ok:            true,
		retA:          uint8(retA),
		retB:          uint8(retB),
		retPC:         1,
		preludeOp:     uint8(bytecode.GETTABLE), // Run-side deopt takes host.GetTable
		preludeArg:    uint32(gtB),
		preludeC:      uint16(gtC),
		icNodeHit:     true,
		icAReg:        uint8(retA),
		icBReg:        uint8(gtB),
		icStableShape: pf.StableShape,
		icStableIndex: pf.StableIndex,
		icStableKey:   stableKey,
	}, true
}

// analyzeSetTableArrayHit recognizes the PJ4 SETTABLE IC ArrayHit shape:
// `function(t,v) t[K] = v end` where K is a numeric constant hitting the
// array part and v is a reg.
//
// **Shape** (luac emits 2 ops, setter form):
//   - [0] SETTABLE A B C: A=R(t) table reg, B=K idx (>=256) key constant, C=R(v) value reg (<256)
//   - [1] RETURN A 1 (setter, 0 return values)
//
// **Trigger conditions** (all must hold to return true):
//   - Code length 2 or 3
//   - SETTABLE A B C: A<=254, B>=256 (K constant index), C<256 (value is a reg)
//   - RETURN B=1 (setter)
//   - proto.IC[0].Kind == ICKindArrayHit (P1 interpreter observed an array hit)
//   - feedback.Points[0].Kind == FBTableMono + Confidence >= 0.99
//   - stableShape / stableIndex consistent
//
// **Design simplifications** (per pj4_template.go::EmitSetTableArrayHit godoc):
//   - Does not verify existing array[stableIndex] != nil (new-key path guard) —
//     relies on the P1 interpreter to bump gen + RequestRefresh on key
//     degradation, accepting that this frame already wrote incorrectly
//   - Does not verify __newindex metatable presence (meta freeze assumption) —
//     metamethod scenarios should trigger a gen change handled by the IC
//     invalidation path
//
// These two simplifications are the PJ4 SETTABLE engineering boundary; the
// strict version is left for PJ4+.
//
// Any failing condition returns (shapeInfo{}, false) — falls through to
// analyzeShape host.SetTable (byte-equal with P1).
func analyzeSetTableArrayHit(proto *bytecode.Proto, feedback *bridge.TypeFeedback) (shapeInfo, bool) {
	codeLen := len(proto.Code)
	if codeLen != 2 && codeLen != 3 {
		return shapeInfo{}, false
	}
	if bytecode.Op(proto.Code[0]) != bytecode.SETTABLE ||
		bytecode.Op(proto.Code[1]) != bytecode.RETURN {
		return shapeInfo{}, false
	}
	if codeLen == 3 && bytecode.Op(proto.Code[2]) != bytecode.RETURN {
		return shapeInfo{}, false
	}
	stA := bytecode.A(proto.Code[0])
	stB := bytecode.B(proto.Code[0])
	stC := bytecode.C(proto.Code[0])
	retB := bytecode.B(proto.Code[1])
	if retB != 1 { // setter must have 0 return values
		return shapeInfo{}, false
	}
	if stA > 254 || stB < 256 || stC > 254 {
		// A: table reg <=254
		// B: K constant index >=256 (a dynamic reg key would make stableIndex unstable)
		// C: value reg <256 (constant value is not speculated — burning imm into rdx needs another primitive)
		return shapeInfo{}, false
	}
	// IC slot check
	if len(proto.IC) <= 0 {
		return shapeInfo{}, false
	}
	icSlot := proto.IC[0]
	if icSlot.Kind != bytecode.ICKindArrayHit {
		return shapeInfo{}, false
	}
	// feedback check
	if feedback == nil || len(feedback.Points) < 1 {
		return shapeInfo{}, false
	}
	pf := feedback.Points[0]
	if pf.Kind != bridge.FBTableMono || pf.Confidence < 0.99 {
		return shapeInfo{}, false
	}
	if pf.StableShape != icSlot.Shape || pf.StableIndex != icSlot.Index {
		return shapeInfo{}, false
	}

	return shapeInfo{
		ok:            true,
		retA:          uint8(stA),
		retB:          uint8(retB),
		retPC:         1,
		preludeOp:     uint8(bytecode.SETTABLE), // Run-side deopt takes host.SetTable
		preludeArg:    uint32(stB),
		preludeC:      uint16(stC),
		icSetArrayHit: true,
		icAReg:        uint8(stA),
		icSetCReg:     uint8(stC),
		icStableShape: pf.StableShape,
		icStableIndex: pf.StableIndex,
	}, true
}

// analyzeSelfArrayHit recognizes the PJ4 SELF IC ArrayHit shape:
// the leading SELF + RETURN part of `function(obj) return obj:method() end`.
//
// **Shape recognition subtlety**: a SELF is normally followed by a CALL to be
// complete; a RETURN directly after SELF is not luac's real compilation path
// (`return obj:method()` compiles to SELF + CALL + RETURN R(A) B).
// **However**: `local m = obj:method` compiles to SELF + RETURN (R(A) is the
// method function, R(A+1) is obj but ignored) — **this is the real source of
// the SELF + RETURN shape**, rare but possible. This batch conservatively
// accepts the SELF + RETURN 2-op shape (SELF writes R(A), RETURN A 2 returns
// R(A)).
//
// **Shape** (luac emits 2 ops):
//   - [0] SELF A B C: A=method result reg, B=obj reg, C=method key RK (must be >=256 constant index)
//   - [1] RETURN A 2 (take R(A) method function, return a single value)
//
// **Trigger conditions**:
//   - Code length 2 or 3
//   - SELF A B C: A<=253 (reserve R(A+1) slot), B<=254, C>=256 (K constant)
//   - RETURN A=SELF.A B=2
//   - proto.IC[0].Kind=ArrayHit + feedback FBTableMono + shape/index consistent
//
// Any failing condition returns (shapeInfo{}, false) — falls through to the
// analyzeShape path (if a SELF + RETURN host helper is available) or
// ErrCompileUnsupportedShape.
func analyzeSelfArrayHit(proto *bytecode.Proto, feedback *bridge.TypeFeedback) (shapeInfo, bool) {
	codeLen := len(proto.Code)
	if codeLen != 2 && codeLen != 3 {
		return shapeInfo{}, false
	}
	if bytecode.Op(proto.Code[0]) != bytecode.SELF ||
		bytecode.Op(proto.Code[1]) != bytecode.RETURN {
		return shapeInfo{}, false
	}
	if codeLen == 3 && bytecode.Op(proto.Code[2]) != bytecode.RETURN {
		return shapeInfo{}, false
	}
	selfA := bytecode.A(proto.Code[0])
	selfB := bytecode.B(proto.Code[0])
	selfC := bytecode.C(proto.Code[0])
	retA := bytecode.A(proto.Code[1])
	retB := bytecode.B(proto.Code[1])
	if selfA != retA || retB != 2 {
		return shapeInfo{}, false
	}
	// A max 253 (reserve R(A+1) slot <= 254); B <=254; C>=256 (K constant)
	if selfA > 253 || selfB > 254 || selfC < 256 {
		return shapeInfo{}, false
	}
	// IC slot check
	if len(proto.IC) <= 0 {
		return shapeInfo{}, false
	}
	icSlot := proto.IC[0]
	if icSlot.Kind != bytecode.ICKindArrayHit {
		return shapeInfo{}, false
	}
	// feedback check
	if feedback == nil || len(feedback.Points) < 1 {
		return shapeInfo{}, false
	}
	pf := feedback.Points[0]
	if pf.Kind != bridge.FBSelfMono || pf.Confidence < 0.99 {
		return shapeInfo{}, false
	}
	if pf.StableShape != icSlot.Shape || pf.StableIndex != icSlot.Index {
		return shapeInfo{}, false
	}

	return shapeInfo{
		ok:             true,
		retA:           uint8(retA),
		retB:           uint8(retB),
		retPC:          1,
		preludeOp:      uint8(bytecode.SELF), // Run-side deopt takes host.SelfTable (same path as GetTable)
		preludeArg:     uint32(selfB),
		preludeC:       uint16(selfC),
		icSelfArrayHit: true,
		icAReg:         uint8(retA),
		icBReg:         uint8(selfB),
		icStableShape:  pf.StableShape,
		icStableIndex:  pf.StableIndex,
	}, true
}

// analyzeSetTableNodeHit recognizes the PJ4 SETTABLE IC NodeHit shape:
// `function(t, v) t["x"] = v end` where the key is a string / arbitrary K
// hitting the hash part.
//
// **Shape** (luac emits 2 ops, setter):
//   - [0] SETTABLE A B C: A=R(t), B=K idx (>=256) key constant, C=R(v) value reg (<256)
//   - [1] RETURN A 1 (setter, 0 return values)
//
// **Trigger conditions**:
//   - Code length 2 or 3
//   - SETTABLE A B C: A<=254, B>=256 (K constant), C<256 (value is a reg)
//   - RETURN B=1
//   - proto.IC[0].Kind == ICKindNodeHit
//   - feedback FBTableMono + Confidence>=0.99 + shape/index consistent
//   - stableKey frozen at compile time from proto.Consts[KIdx] (guard against Nil slot: value.Nil)
//
// Any failing condition returns (shapeInfo{}, false) — falls through to
// analyzeShape host.SetTable, byte-equal with P1 (via icSetTable + __newindex
// metamethod chain).
func analyzeSetTableNodeHit(proto *bytecode.Proto, feedback *bridge.TypeFeedback) (shapeInfo, bool) {
	codeLen := len(proto.Code)
	if codeLen != 2 && codeLen != 3 {
		return shapeInfo{}, false
	}
	if bytecode.Op(proto.Code[0]) != bytecode.SETTABLE ||
		bytecode.Op(proto.Code[1]) != bytecode.RETURN {
		return shapeInfo{}, false
	}
	if codeLen == 3 && bytecode.Op(proto.Code[2]) != bytecode.RETURN {
		return shapeInfo{}, false
	}
	stA := bytecode.A(proto.Code[0])
	stB := bytecode.B(proto.Code[0])
	stC := bytecode.C(proto.Code[0])
	retB := bytecode.B(proto.Code[1])
	if retB != 1 {
		return shapeInfo{}, false
	}
	if stA > 254 || stB < 256 || stC > 254 {
		return shapeInfo{}, false
	}
	if len(proto.IC) <= 0 {
		return shapeInfo{}, false
	}
	icSlot := proto.IC[0]
	if icSlot.Kind != bytecode.ICKindNodeHit {
		return shapeInfo{}, false
	}
	if feedback == nil || len(feedback.Points) < 1 {
		return shapeInfo{}, false
	}
	pf := feedback.Points[0]
	if pf.Kind != bridge.FBTableMono || pf.Confidence < 0.99 {
		return shapeInfo{}, false
	}
	if pf.StableShape != icSlot.Shape || pf.StableIndex != icSlot.Index {
		return shapeInfo{}, false
	}
	// stableKey frozen at compile time (same as GetTable NodeHit)
	kIdx := bytecode.KIdx(int(stB))
	if kIdx < 0 || kIdx >= len(proto.Consts) {
		return shapeInfo{}, false
	}
	stableKey := uint64(proto.Consts[kIdx])
	if stableKey == uint64(value.Nil) {
		// LoadProgram did not load the string slot (rare, defensive)
		return shapeInfo{}, false
	}

	return shapeInfo{
		ok:            true,
		retA:          uint8(stA),
		retB:          uint8(retB),
		retPC:         1,
		preludeOp:     uint8(bytecode.SETTABLE),
		preludeArg:    uint32(stB),
		preludeC:      uint16(stC),
		icSetNodeHit:  true,
		icAReg:        uint8(stA),
		icSetCReg:     uint8(stC),
		icStableShape: pf.StableShape,
		icStableIndex: pf.StableIndex,
		icStableKey:   stableKey,
	}, true
}

// analyzeSelfNodeHit recognizes the PJ4 SELF IC NodeHit shape:
// the single-BB `local m = obj:method` / `obj:method()` form — the method is a
// string ident → hits the hash part. This is the typical shape of a real-world
// `obj:method()` call (luac emits SELF A=R(m) B=R(obj) C=K(string),
// IC[0]=NodeHit).
//
// **Shape** (luac emits 2 ops):
//   - [0] SELF A B C: A<=253 (reserve R(A+1) slot <=254), B<=254, C>=256 (K string constant)
//   - [1] RETURN A 2 (take R(A) method function)
//
// **Trigger conditions**:
//   - Code length 2 or 3
//   - SELF A B C + RETURN A 2 shape guard
//   - proto.IC[0].Kind == ICKindNodeHit
//   - feedback FBTableMono + Confidence >= 0.99 + shape/index consistent
//   - stableKey frozen at compile time (LoadProgram already interned the string)
//
// Any failing condition returns (shapeInfo{}, false) — falls through to the
// analyzeShape path (if a SELF + RETURN host helper is available) or
// ErrCompileUnsupportedShape.
func analyzeSelfNodeHit(proto *bytecode.Proto, feedback *bridge.TypeFeedback) (shapeInfo, bool) {
	codeLen := len(proto.Code)
	if codeLen != 2 && codeLen != 3 {
		return shapeInfo{}, false
	}
	if bytecode.Op(proto.Code[0]) != bytecode.SELF ||
		bytecode.Op(proto.Code[1]) != bytecode.RETURN {
		return shapeInfo{}, false
	}
	if codeLen == 3 && bytecode.Op(proto.Code[2]) != bytecode.RETURN {
		return shapeInfo{}, false
	}
	selfA := bytecode.A(proto.Code[0])
	selfB := bytecode.B(proto.Code[0])
	selfC := bytecode.C(proto.Code[0])
	retA := bytecode.A(proto.Code[1])
	retB := bytecode.B(proto.Code[1])
	if selfA != retA || retB != 2 {
		return shapeInfo{}, false
	}
	if selfA > 253 || selfB > 254 || selfC < 256 {
		return shapeInfo{}, false
	}
	if len(proto.IC) <= 0 {
		return shapeInfo{}, false
	}
	icSlot := proto.IC[0]
	if icSlot.Kind != bytecode.ICKindNodeHit {
		return shapeInfo{}, false
	}
	if feedback == nil || len(feedback.Points) < 1 {
		return shapeInfo{}, false
	}
	pf := feedback.Points[0]
	if pf.Kind != bridge.FBSelfMono || pf.Confidence < 0.99 {
		return shapeInfo{}, false
	}
	if pf.StableShape != icSlot.Shape || pf.StableIndex != icSlot.Index {
		return shapeInfo{}, false
	}
	// stableKey frozen at compile time
	kIdx := bytecode.KIdx(int(selfC))
	if kIdx < 0 || kIdx >= len(proto.Consts) {
		return shapeInfo{}, false
	}
	stableKey := uint64(proto.Consts[kIdx])
	if stableKey == uint64(value.Nil) {
		return shapeInfo{}, false
	}

	return shapeInfo{
		ok:            true,
		retA:          uint8(retA),
		retB:          uint8(retB),
		retPC:         1,
		preludeOp:     uint8(bytecode.SELF), // Run-side deopt takes host.GetTable (same source as P1 SELF case)
		preludeArg:    uint32(selfB),
		preludeC:      uint16(selfC),
		icSelfNodeHit: true,
		icAReg:        uint8(retA),
		icBReg:        uint8(selfB),
		icStableShape: pf.StableShape,
		icStableIndex: pf.StableIndex,
		icStableKey:   stableKey,
	}, true
}

// analyzeForLoopBody2Form recognizes the two-statement body shape:
// `local s=K_s; for i=K1,K2 do s = s op1 K3; s = s op2 K4 end; return s`.
// luac emits 10/11 ops, the body containing two serial reg-K arith ops
// writing to the same R(aS).
//
// luac encoding (example `local s=0; for i=1,5 do s=s+1; s=s*2 end; return s`):
//
//	[0] LOADK    A_s     -K_s  ; s=0
//	[1..3] LOADK A_init/+1/+2  ; init/limit/step
//	[4] FORPREP  A_init  sBx=2 ; jmp to body[6]
//	[5] arith1   A_s A_s C(K_body1)
//	[6] arith2   A_s A_s C(K_body2)
//	[7] FORLOOP  A_init  sBx=-3 ; jmp back to [5]
//	[8] RETURN   A_s     B=2
//	[9] dead RETURN (optional)
func analyzeForLoopBody2Form(proto *bytecode.Proto) (shapeInfo, bool) {
	codeLen := len(proto.Code)
	if codeLen != 9 && codeLen != 10 {
		return shapeInfo{}, false
	}
	if bytecode.Op(proto.Code[0]) != bytecode.LOADK ||
		bytecode.Op(proto.Code[1]) != bytecode.LOADK ||
		bytecode.Op(proto.Code[2]) != bytecode.LOADK ||
		bytecode.Op(proto.Code[3]) != bytecode.LOADK ||
		bytecode.Op(proto.Code[4]) != bytecode.FORPREP ||
		bytecode.Op(proto.Code[7]) != bytecode.FORLOOP ||
		bytecode.Op(proto.Code[8]) != bytecode.RETURN {
		return shapeInfo{}, false
	}
	if codeLen == 10 && bytecode.Op(proto.Code[9]) != bytecode.RETURN {
		return shapeInfo{}, false
	}
	bodyOp1 := bytecode.Op(proto.Code[5])
	bodyOp2 := bytecode.Op(proto.Code[6])
	if (bodyOp1 != bytecode.ADD && bodyOp1 != bytecode.SUB &&
		bodyOp1 != bytecode.MUL && bodyOp1 != bytecode.DIV) ||
		(bodyOp2 != bytecode.ADD && bodyOp2 != bytecode.SUB &&
			bodyOp2 != bytecode.MUL && bodyOp2 != bytecode.DIV) {
		return shapeInfo{}, false
	}
	// FORPREP sBx=2 (body length 2)
	if bytecode.SBx(proto.Code[4]) != 2 {
		return shapeInfo{}, false
	}
	// FORLOOP sBx=-3
	if bytecode.SBx(proto.Code[7]) != -3 {
		return shapeInfo{}, false
	}

	aS := bytecode.A(proto.Code[0])
	aInit := bytecode.A(proto.Code[1])
	aLimit := bytecode.A(proto.Code[2])
	aStep := bytecode.A(proto.Code[3])
	aPrep := bytecode.A(proto.Code[4])
	a1A := bytecode.A(proto.Code[5])
	a1B := bytecode.B(proto.Code[5])
	a2A := bytecode.A(proto.Code[6])
	a2B := bytecode.B(proto.Code[6])
	aLoop := bytecode.A(proto.Code[7])
	retA := bytecode.A(proto.Code[8])
	retB := bytecode.B(proto.Code[8])

	if aInit != aS+1 || aLimit != aInit+1 || aStep != aInit+2 ||
		aPrep != aInit || aLoop != aInit {
		return shapeInfo{}, false
	}
	// two body ops: A=B=A_s (s = s op K form)
	if a1A != aS || a1B != aS || a2A != aS || a2B != aS {
		return shapeInfo{}, false
	}
	if retA != aS || retB != 2 {
		return shapeInfo{}, false
	}

	// body C must both be K constants (>= 256) and the K must be a number
	b1C := bytecode.C(proto.Code[5])
	b2C := bytecode.C(proto.Code[6])
	if b1C < 256 || b2C < 256 ||
		int(b1C-256) >= len(proto.Consts) || int(b2C-256) >= len(proto.Consts) {
		return shapeInfo{}, false
	}
	kBody1 := proto.Consts[b1C-256]
	kBody2 := proto.Consts[b2C-256]
	if !value.IsNumber(kBody1) || !value.IsNumber(kBody2) {
		return shapeInfo{}, false
	}

	// init/limit/step/s all numbers
	kSIdx := bytecode.Bx(proto.Code[0])
	kInitIdx := bytecode.Bx(proto.Code[1])
	kLimitIdx := bytecode.Bx(proto.Code[2])
	kStepIdx := bytecode.Bx(proto.Code[3])
	if kSIdx >= len(proto.Consts) || kInitIdx >= len(proto.Consts) ||
		kLimitIdx >= len(proto.Consts) || kStepIdx >= len(proto.Consts) {
		return shapeInfo{}, false
	}
	kS := proto.Consts[kSIdx]
	kInit := proto.Consts[kInitIdx]
	kLimit := proto.Consts[kLimitIdx]
	kStep := proto.Consts[kStepIdx]
	if !value.IsNumber(kS) || !value.IsNumber(kInit) ||
		!value.IsNumber(kLimit) || !value.IsNumber(kStep) {
		return shapeInfo{}, false
	}
	if !(value.AsNumber(kStep) > 0) { // negated form: NaN step must also decline (#117/#118)
		return shapeInfo{}, false
	}

	mapSse := func(op bytecode.OpCode) byte {
		switch op {
		case bytecode.ADD:
			return 0x58
		case bytecode.SUB:
			return 0x5C
		case bytecode.MUL:
			return 0x59
		case bytecode.DIV:
			return 0x5E
		}
		return 0
	}

	return shapeInfo{
		ok:          true,
		retA:        uint8(aS),
		retB:        2,
		retPC:       8,
		isForLoop:   true,
		forA:        uint8(aInit),
		forInitK:    uint64(kInit),
		forLimitK:   uint64(kLimit),
		forStepK:    uint64(kStep),
		hasBody:     true, // reuse the hasBody path, but hasBody2 drives the body2 template
		hasBody2:    true,
		bodyOp:      mapSse(bodyOp1),
		bodyKValue:  uint64(kBody1),
		bodyOp2:     mapSse(bodyOp2),
		bodyKValue2: uint64(kBody2),
		forBodyAS:   uint8(aS),
		forBodyKS:   uint64(kS),
	}, true
}

// analyzeForLoopBodyForm recognizes the PJ3 FORLOOP shape with a reg-K op body:
// `function() local s=K_s; for i=K1,K2 do s = s op K3 end; return s end`.
//
// luac encoding (example `local s=0; for i=1,100 do s = s + 1 end; return s`):
//
//	[0] LOADK    A_s    -K_s    ; s = K_s
//	[1] LOADK    A_init -K_init ; init
//	[2] LOADK    A_init+1 -K_limit ; limit
//	[3] LOADK    A_init+2 -K_step  ; step
//	[4] FORPREP  A_init  sBx=1  ; jmp to body
//	[5] ADD/SUB/MUL/DIV A_s A_s C(K_body index) ; body = s op K
//	[6] FORLOOP  A_init  sBx=-2 ; jmp back to [5]
//	[7] RETURN   A_s     B=2    ; return s
//	[8] dead RETURN (optional)
//
// **Shape constraints**:
//   - proto.Code length 8 or 9
//   - [0/1/2/3] four LOADK + [4] FORPREP sBx=1 + [5] reg-K arith op
//   - [6] FORLOOP sBx=-2 + [7] RETURN A=A_s B=2 (optional [8] dead RETURN)
//   - body is reg-K (B = A_s = A, C is the K index) + SSE whitelist op
//     (ADD/SUB/MUL/DIV)
//   - A_init >= A_s + 1 (s slot lies outside the for slots, avoiding overwrite)
func analyzeForLoopBodyForm(proto *bytecode.Proto) (shapeInfo, bool) {
	codeLen := len(proto.Code)
	if codeLen != 8 && codeLen != 9 {
		return shapeInfo{}, false
	}
	if bytecode.Op(proto.Code[0]) != bytecode.LOADK ||
		bytecode.Op(proto.Code[1]) != bytecode.LOADK ||
		bytecode.Op(proto.Code[2]) != bytecode.LOADK ||
		bytecode.Op(proto.Code[3]) != bytecode.LOADK ||
		bytecode.Op(proto.Code[4]) != bytecode.FORPREP ||
		bytecode.Op(proto.Code[6]) != bytecode.FORLOOP ||
		bytecode.Op(proto.Code[7]) != bytecode.RETURN {
		return shapeInfo{}, false
	}
	if codeLen == 9 && bytecode.Op(proto.Code[8]) != bytecode.RETURN {
		return shapeInfo{}, false
	}
	bodyOp := bytecode.Op(proto.Code[5])
	// body must be an SSE whitelist op
	if bodyOp != bytecode.ADD && bodyOp != bytecode.SUB &&
		bodyOp != bytecode.MUL && bodyOp != bytecode.DIV {
		return shapeInfo{}, false
	}

	// FORPREP sBx=1 (jmp skips body of length 1)
	if bytecode.SBx(proto.Code[4]) != 1 {
		return shapeInfo{}, false
	}
	// FORLOOP sBx=-2 (jmp back to body)
	if bytecode.SBx(proto.Code[6]) != -2 {
		return shapeInfo{}, false
	}

	aS := bytecode.A(proto.Code[0])     // s slot
	aInit := bytecode.A(proto.Code[1])  // for slot base
	aLimit := bytecode.A(proto.Code[2]) // for+1
	aStep := bytecode.A(proto.Code[3])  // for+2
	aPrep := bytecode.A(proto.Code[4])
	aBody := bytecode.A(proto.Code[5])  // body's A, = s slot
	aBodyB := bytecode.B(proto.Code[5]) // body's B, = s slot
	aLoop := bytecode.A(proto.Code[6])
	retA := bytecode.A(proto.Code[7])
	retB := bytecode.B(proto.Code[7])

	if aInit != aS+1 || aLimit != aInit+1 || aStep != aInit+2 ||
		aPrep != aInit || aLoop != aInit {
		return shapeInfo{}, false
	}
	if aBody != aS || aBodyB != aS {
		return shapeInfo{}, false
	}
	// RETURN A=A_s B=2 (single return)
	if retA != aS || retB != 2 {
		return shapeInfo{}, false
	}

	// body's C must be a K constant (>= 256), and the K must be a number
	bodyC := bytecode.C(proto.Code[5])
	if bodyC < 256 || int(bodyC-256) >= len(proto.Consts) {
		return shapeInfo{}, false
	}
	kBody := proto.Consts[bodyC-256]
	if !value.IsNumber(kBody) {
		return shapeInfo{}, false
	}

	// init / limit / step / s must all be number Ks
	kSIdx := bytecode.Bx(proto.Code[0])
	kInitIdx := bytecode.Bx(proto.Code[1])
	kLimitIdx := bytecode.Bx(proto.Code[2])
	kStepIdx := bytecode.Bx(proto.Code[3])
	if kSIdx >= len(proto.Consts) || kInitIdx >= len(proto.Consts) ||
		kLimitIdx >= len(proto.Consts) || kStepIdx >= len(proto.Consts) {
		return shapeInfo{}, false
	}
	kS := proto.Consts[kSIdx]
	kInit := proto.Consts[kInitIdx]
	kLimit := proto.Consts[kLimitIdx]
	kStep := proto.Consts[kStepIdx]
	if !value.IsNumber(kS) || !value.IsNumber(kInit) ||
		!value.IsNumber(kLimit) || !value.IsNumber(kStep) {
		return shapeInfo{}, false
	}

	// step > 0 only (jcc=ja to exit)
	if !(value.AsNumber(kStep) > 0) { // negated form: NaN step must also decline (#117/#118)
		return shapeInfo{}, false
	}

	// map the SSE op
	var sseOp byte
	switch bodyOp {
	case bytecode.ADD:
		sseOp = 0x58 // ADDSD
	case bytecode.SUB:
		sseOp = 0x5C
	case bytecode.MUL:
		sseOp = 0x59
	case bytecode.DIV:
		sseOp = 0x5E
	}

	return shapeInfo{
		ok:         true,
		retA:       uint8(aS),
		retB:       2, // return s
		retPC:      7,
		isForLoop:  true,
		forA:       uint8(aInit),
		forInitK:   uint64(kInit),
		forLimitK:  uint64(kLimit),
		forStepK:   uint64(kStep),
		hasBody:    true,
		bodyOp:     sseOp,
		bodyKValue: uint64(kBody),
		forBodyAS:  uint8(aS),
		forBodyKS:  uint64(kS),
	}, true
}

// analyzeForLoopForm recognizes the simplest byte-level PJ3 FORLOOP inline
// shape: `function() for i=K1, K2 do end end` (all-constant init/limit/step +
// empty body).
//
// luac encoding (example `for i=1,100 do end`, assuming no outer local):
//
//	[0] LOADK    A   -kInit  ; R(A)=init = K[kInit]
//	[1] LOADK    A+1 -kLimit ; R(A+1)=limit = K[kLimit]
//	[2] LOADK    A+2 -kStep  ; R(A+2)=step = K[kStep]
//	[3] FORPREP  A   sBx=0   ; R(A)-=step; jmp to FORLOOP
//	[4] FORLOOP  A   sBx=-1  ; R(A)+=step; cmp limit; jmp back to [4] (empty body)
//	[5] RETURN   0   1       ; empty return
//	[6] RETURN   0   1       ; (optional dead RETURN, at the tail of luac's main chunk)
//
// **Shape constraints**:
//   - proto.Code length 6 or 7 (optional trailing dead RETURN)
//   - [0] LOADK A_init -kInit
//   - [1] LOADK A_init+1 -kLimit **or** MOVE A_init+1 limitReg
//     (reg-limit hot path: `for i=1, n do end` luac emits MOVE)
//   - [2] LOADK A_init+2 -kStep
//   - [3] FORPREP A_init sBx=0 (luac emits 0 when body is empty)
//   - [4] FORLOOP A_init sBx=-1 (back-edge jumps to itself)
//   - [5] RETURN A=0 B=1 (empty return)
//   - K[kInit / kStep] must both be numbers (else fall back to host); in the
//     LOADK form K[kLimit] must also be a number; in the MOVE form limitReg
//     gets a runtime IsNumber guard
//
// **Currently wired into the main path** (per the Compile side):
//   - LOADK limit form: 69/83-byte template (empty body, all constants),
//     measured at 7-25x over gopher-lua
//   - MOVE limit form: 117-byte template (IsNumber guard + deopt calling
//     host.ForPrep raise byte-equal with P1), the hot path is fully wired
//
// **Unsupported** (left for the full PJ3 extension):
//   - non-empty body (needs inline body opcodes + register allocation)
//   - nested for / containing break (JMP)
//   - non-default step (step=1 implicit; explicitly encoded step still takes
//     this path, since step is also a K)
func analyzeForLoopForm(proto *bytecode.Proto) (shapeInfo, bool) {
	codeLen := len(proto.Code)
	if codeLen != 6 && codeLen != 7 {
		return shapeInfo{}, false
	}
	// [0/1/2] LOADK / [3] FORPREP / [4] FORLOOP / [5] RETURN
	// **limit supports LOADK / MOVE / GETUPVAL**:
	//   - LOADK: constant limit (`for i=1,100 do end`)
	//   - MOVE : reg-limit hot path (`for i=1,n do end`, n=parameter reg)
	//   - GETUPVAL: upvalue-limit (closure capture, `local n=100; local
	//     function f() for i=1,n do end end`, n is an upvalue)
	if bytecode.Op(proto.Code[0]) != bytecode.LOADK ||
		(bytecode.Op(proto.Code[1]) != bytecode.LOADK &&
			bytecode.Op(proto.Code[1]) != bytecode.MOVE &&
			bytecode.Op(proto.Code[1]) != bytecode.GETUPVAL) ||
		bytecode.Op(proto.Code[2]) != bytecode.LOADK ||
		bytecode.Op(proto.Code[3]) != bytecode.FORPREP ||
		bytecode.Op(proto.Code[4]) != bytecode.FORLOOP ||
		bytecode.Op(proto.Code[5]) != bytecode.RETURN {
		return shapeInfo{}, false
	}
	if codeLen == 7 && bytecode.Op(proto.Code[6]) != bytecode.RETURN {
		return shapeInfo{}, false
	}

	// A fields consistent
	aInit := bytecode.A(proto.Code[0])
	aLimit := bytecode.A(proto.Code[1])
	aStep := bytecode.A(proto.Code[2])
	aPrep := bytecode.A(proto.Code[3])
	aLoop := bytecode.A(proto.Code[4])
	if aLimit != aInit+1 || aStep != aInit+2 || aPrep != aInit || aLoop != aInit {
		return shapeInfo{}, false
	}

	// FORPREP sBx == 0, FORLOOP sBx == -1
	if bytecode.SBx(proto.Code[3]) != 0 || bytecode.SBx(proto.Code[4]) != -1 {
		return shapeInfo{}, false
	}

	// RETURN A=0 B=1
	if bytecode.A(proto.Code[5]) != 0 || bytecode.B(proto.Code[5]) != 1 {
		return shapeInfo{}, false
	}

	// init / step: must be LOADK + K is a number
	kInitIdx := bytecode.Bx(proto.Code[0])
	kStepIdx := bytecode.Bx(proto.Code[2])
	if kInitIdx >= len(proto.Consts) || kStepIdx >= len(proto.Consts) {
		return shapeInfo{}, false
	}
	kInit := proto.Consts[kInitIdx]
	kStep := proto.Consts[kStepIdx]
	if !value.IsNumber(kInit) || !value.IsNumber(kStep) {
		return shapeInfo{}, false
	}

	// **only step > 0 is supported by this simplified template** (jcc picks
	// ja: exit when idx > limit). step ≤ 0 or negative step is left for the
	// PJ3+ extension (jcc picks jb: exit when idx < limit).
	stepF := value.AsNumber(kStep)
	if !(stepF > 0) { // negated form: NaN step must also decline (#117/#118)
		return shapeInfo{}, false
	}

	// limit: LOADK (constant) or MOVE (reg-limit hot path)
	si := shapeInfo{
		ok:        true,
		retA:      0, // RETURN A=0
		retB:      1, // empty return
		retPC:     5,
		isForLoop: true,
		forA:      uint8(aInit),
		forInitK:  uint64(kInit),
		forStepK:  uint64(kStep),
	}
	if bytecode.Op(proto.Code[1]) == bytecode.LOADK {
		kLimitIdx := bytecode.Bx(proto.Code[1])
		if kLimitIdx >= len(proto.Consts) {
			return shapeInfo{}, false
		}
		kLimit := proto.Consts[kLimitIdx]
		if !value.IsNumber(kLimit) {
			return shapeInfo{}, false
		}
		si.forLimitK = uint64(kLimit)
		si.forLimitIsReg = false
	} else if bytecode.Op(proto.Code[1]) == bytecode.MOVE {
		// **MOVE A B reg-limit form** (luac emits MOVE for the limit when
		// compiling `for i=1,n do end` with limit=n). The byte-level template
		// EmitForLoopRegLimit is implemented; the deopt path calls
		// host.ForPrep raise (`'for' limit must be a number`) byte-equal with
		// the interpreter (if R(limitReg) is not a number).
		moveB := bytecode.B(proto.Code[1])
		if moveB > 254 {
			return shapeInfo{}, false
		}
		si.forLimitReg = uint8(moveB)
		si.forLimitIsReg = true
	} else {
		// **GETUPVAL A B upvalue-limit form** (closure capture): when luac
		// compiles `for i=1,upval_n do end` inside a closure, [1] = GETUPVAL A
		// B, A=A_init+1 / B=upvalue index. The Run side calls host.GetUpval(B)
		// to fetch the value, then writes the R(A_init+1) slot directly via
		// host.SetReg, and afterwards takes the reg-limit template. Because
		// after host.GetUpval writes the slot the limit is already a number (if
		// the upvalue is a number); otherwise the reg-limit template's IsNumber
		// guard triggers deopt automatically.
		guvB := bytecode.B(proto.Code[1])
		// Cap at 254: `uint8(guvB) + 1` does not overflow. 255 → uint8(255)+1 =
		// 0, and 0 in this field's semantics means "do not take the upval
		// path", so the Run side skips host.GetUpval + SetReg → the reg-limit
		// template reads an unfilled R(forLimitReg) → wrong loop bound or a
		// spurious deopt. Reachability is extremely low (needs the 256th
		// upvalue as a FORLOOP upper bound), but it is a self-contradictory
		// boundary.
		if guvB > 254 {
			return shapeInfo{}, false
		}
		si.forLimitReg = uint8(aLimit)        // target slot = R(A_init+1)
		si.forLimitIsReg = true               // still takes the reg-limit template
		si.forLimitUpvalIdx = uint8(guvB) + 1 // 1-based (0 = do not take upval)
	}

	return si, true
}

// analyzeCompareForm recognizes the folded EQ/LT/LE + JMP + LOADBOOL +
// LOADBOOL + RETURN (+ dead RETURN) shape (`function(x) return x == 1 end`
// class).
//
// luac encoding (EQ as example):
//
//	[0] EQ        A=cmpA B C    (cmpA=1: skip next when R(B)==RK(C); cmpA=0: the reverse)
//	[1] JMP       A=0 sBx=1     (jump to LOADBOOL true, i.e. [3])
//	[2] LOADBOOL  A=retA B=0 C=1 (false + skip next; if not reached, next runs)
//	[3] LOADBOOL  A=retA B=1 C=0 (true)
//	[4] RETURN    A=retA B=2
//	[5] RETURN    A=0 B=1       (dead, optional trailing redundancy)
//
// Equivalent semantics: `R(retA) = BoolValue(cmp(B,C) == (cmpA==1))` (packed
// bit0 compared with cmpA, returns true when equal). The Run path calls
// host.Compare(B, C) to get packed, then folds it into a BoolValue written to
// R(retA) via SetReg.
//
// Supports the three comparison ops EQ(23)/LT(24)/LE(25).
func analyzeCompareForm(proto *bytecode.Proto) (shapeInfo, bool) {
	if len(proto.Code) != 5 && len(proto.Code) != 6 {
		return shapeInfo{}, false
	}

	cmp := proto.Code[0]
	jmp := proto.Code[1]
	lbFalse := proto.Code[2]
	lbTrue := proto.Code[3]
	ret := proto.Code[4]

	// op 0: EQ/LT/LE
	cmpOp := bytecode.Op(cmp)
	if cmpOp != bytecode.EQ && cmpOp != bytecode.LT && cmpOp != bytecode.LE {
		return shapeInfo{}, false
	}
	cmpA := bytecode.A(cmp)
	cmpB := bytecode.B(cmp)
	cmpC := bytecode.C(cmp)
	if cmpA != 0 && cmpA != 1 {
		return shapeInfo{}, false
	}
	if cmpB > 511 || cmpC > 511 {
		return shapeInfo{}, false
	}

	// op 1: JMP sBx=1 (skip next)
	if bytecode.Op(jmp) != bytecode.JMP {
		return shapeInfo{}, false
	}
	if bytecode.SBx(jmp) != 1 {
		return shapeInfo{}, false
	}

	// op 2: LOADBOOL A=retA B=0 C=1 (false + skip next)
	if bytecode.Op(lbFalse) != bytecode.LOADBOOL {
		return shapeInfo{}, false
	}
	lbFalseA := bytecode.A(lbFalse)
	if bytecode.B(lbFalse) != 0 || bytecode.C(lbFalse) != 1 {
		return shapeInfo{}, false
	}

	// op 3: LOADBOOL A=retA B=1 C=0 (true)
	if bytecode.Op(lbTrue) != bytecode.LOADBOOL {
		return shapeInfo{}, false
	}
	lbTrueA := bytecode.A(lbTrue)
	if lbTrueA != lbFalseA {
		return shapeInfo{}, false
	}
	if bytecode.B(lbTrue) != 1 || bytecode.C(lbTrue) != 0 {
		return shapeInfo{}, false
	}

	// op 4: RETURN A=retA B=2
	if bytecode.Op(ret) != bytecode.RETURN {
		return shapeInfo{}, false
	}
	retA := bytecode.A(ret)
	retB := bytecode.B(ret)
	if retA != lbTrueA || retB != 2 {
		return shapeInfo{}, false
	}

	// op 5: optional dead RETURN (B=1)
	if len(proto.Code) == 6 {
		if bytecode.Op(proto.Code[5]) != bytecode.RETURN {
			return shapeInfo{}, false
		}
	}

	return shapeInfo{
		ok:         true,
		retA:       uint8(retA),
		retB:       uint8(retB),
		retPC:      4, // RETURN is at pc 4
		preludeOp:  uint8(cmpOp),
		preludeArg: uint32(cmpB),
		preludeC:   uint16(cmpC),
		cmpA:       uint8(cmpA),
	}, true
}

// analyzeArithChainForm recognizes the two-stage arithmetic chain shape
// (`function(x) return x*2+1 end` class), of length 3 or 4:
//
//	[0] arith1 A B C    (ADD/SUB/MUL/DIV/MOD/POW; A need not = retA, but A must
//	                     be the B input position of arith2)
//	[1] arith2 A B C    (B = arith1.A, chained input; A matches retA)
//	[2] RETURN A 2
//	[3] dead RETURN (optional)
//
// Equivalent semantics: Run serially calls host.Arith(op1, B1, C1, A1) then
// host.Arith(op2, B2=A1, C2, A2) — the intermediate value passes naturally
// through ci's reg slot, same source as interpreter execution.
//
// **Key constraint**: arith1.A must == arith2.B (chained input, and after luac
// encoding both ops' A match retA). This simplification only accepts the
// op1.A == op2.A == retA shape (luac's default output).
func analyzeArithChainForm(proto *bytecode.Proto) (shapeInfo, bool) {
	if len(proto.Code) != 3 && len(proto.Code) != 4 {
		return shapeInfo{}, false
	}
	op1 := proto.Code[0]
	op2 := proto.Code[1]
	ret := proto.Code[2]

	isArith := func(op bytecode.OpCode) bool {
		return op == bytecode.ADD || op == bytecode.SUB || op == bytecode.MUL ||
			op == bytecode.DIV || op == bytecode.MOD || op == bytecode.POW
	}
	if !isArith(bytecode.Op(op1)) || !isArith(bytecode.Op(op2)) {
		return shapeInfo{}, false
	}
	if bytecode.Op(ret) != bytecode.RETURN {
		return shapeInfo{}, false
	}
	retA := bytecode.A(ret)
	retB := bytecode.B(ret)
	if retB != 2 {
		return shapeInfo{}, false
	}

	// op1: A B C; op2: A B C
	op1A := bytecode.A(op1)
	op2A := bytecode.A(op2)
	op2B := bytecode.B(op2)
	if op1A != retA || op2A != retA {
		return shapeInfo{}, false
	}
	// op2.B must read op1's output (=op1.A=retA) — the chained input
	if op2B != retA {
		return shapeInfo{}, false
	}

	op1B := bytecode.B(op1)
	op1C := bytecode.C(op1)
	op2C := bytecode.C(op2)
	if op1B > 511 || op1C > 511 || op2C > 511 {
		return shapeInfo{}, false
	}

	// when length 4, [3] must be a dead RETURN
	if len(proto.Code) == 4 {
		if bytecode.Op(proto.Code[3]) != bytecode.RETURN {
			return shapeInfo{}, false
		}
	}

	return shapeInfo{
		ok:         true,
		retA:       uint8(retA),
		retB:       uint8(retB),
		retPC:      2, // RETURN is at pc 2
		preludeOp:  uint8(bytecode.Op(op1)),
		preludeArg: uint32(op1B),
		preludeC:   uint16(op1C),
		chainOp:    uint8(bytecode.Op(op2)),
		chainB:     uint16(op2B), // = retA (chained)
		chainC:     uint16(op2C),
	}, true
}

// analyzeCallVoidForm recognizes the PJ5 CALL void simplified shape (per
// docs/design/p4-method-jit/05-system-pipeline.md §4.3 + 06-backends.md §3.5):
// `function(g) g() end` / `function() noop() end` class — a single BB of three
// ops, where the op before the call is MOVE (callee is in a parameter slot,
// in-function parameter form) or GETUPVAL (a closure calling an outer known
// local fn, upvalue form).
//
// luac compilation shapes (two forms):
//
//	Form A: `function(g) g() end` (parameter callee)
//	  [0] MOVE     A=callee slot B=parameter source slot
//	  [1] CALL     A=callee slot B=1 (0 args) C=1 (0 returns)
//	  [2] RETURN   A=0 B=1 (0 returns)
//
//	Form B: `local function noop()...end; local function invoker() noop() end`
//	  [0] GETUPVAL A=callee slot B=upvalue index
//	  [1] CALL     A=callee slot B=1 C=1
//	  [2] RETURN   A=0 B=1
//
// **Trigger conditions** (common to both forms + form-specific):
//   - Code length = 3
//   - [0] = MOVE or GETUPVAL, [0].A matches [1].A (CALL.A)
//   - [1] = CALL, CALL.B == 1 (0 args) + CALL.C == 1 (0 returns)
//   - [2] = RETURN, RETURN.B == 1 (0 return values)
//   - reg / upvalue indices in the [0,254] range
//
// **PJ5 simplified form scope**: the Run-side prelude path branches on isCallUpval:
//   - Form A (isCallUpval=false): host.GetReg(MOVE.B) + SetReg(MOVE.A)
//     completes the MOVE preprocessing
//   - Form B (isCallUpval=true): host.GetUpval(base, GETUPVAL.B) +
//     SetReg(GETUPVAL.A) completes the upvalue-fetch preprocessing,
//     then calls host.CallBaseline(base, callPC, callA, callB, callC) +
//     host.DoReturn to pop the frame.
//
// If any condition fails, returns (shapeInfo{}, false) and falls through to the
// analyzeShape main dispatch (which may match other forms; e.g. the single-op
// GETUPVAL+RETURN A 2 form guard retB=2 rejects the setter form).
// decodeArgFromOp decodes the LOADK / MOVE op at proto.Code[codeIdx] into the
// argIsK + argK / argReg tri-state (reused by PJ5 CALL/TAILCALL form recognition).
//   - Expects op.A == expectA (argument slot R(expectA))
//   - LOADK: decode Bx to index proto.Consts, load argK + argIsK=true
//   - MOVE: decode B into argReg + argIsK=false (B must be ≤ 254 as a defensive fallback)
//
// If any condition fails, returns false and the caller falls through to shapeInfo{}, false.
func decodeArgFromOp(proto *bytecode.Proto, codeIdx int, expectA int,
	argIsK *bool, argK *uint64, argReg *uint8) bool {
	op := bytecode.Op(proto.Code[codeIdx])
	if op != bytecode.LOADK && op != bytecode.MOVE {
		return false
	}
	opA := bytecode.A(proto.Code[codeIdx])
	if opA != expectA {
		return false
	}
	if op == bytecode.LOADK {
		bx := bytecode.Bx(proto.Code[codeIdx])
		if bx < 0 || bx >= len(proto.Consts) {
			return false
		}
		*argK = uint64(proto.Consts[bx])
		*argIsK = true
	} else {
		b := bytecode.B(proto.Code[codeIdx])
		if b > 254 {
			return false
		}
		*argReg = uint8(b)
		*argIsK = false
	}
	return true
}

func analyzeCallVoidForm(proto *bytecode.Proto) (shapeInfo, bool) {
	codeLen := len(proto.Code)
	if codeLen < 3 || codeLen > 11 {
		return shapeInfo{}, false
	}
	op0 := bytecode.Op(proto.Code[0])
	if op0 != bytecode.MOVE && op0 != bytecode.GETUPVAL {
		return shapeInfo{}, false
	}
	op0A := bytecode.A(proto.Code[0])
	op0B := bytecode.B(proto.Code[0])
	if op0A > 254 || op0B > 254 {
		return shapeInfo{}, false
	}

	// Length 3: 0 args 0 returns (MOVE/GETUPVAL + CALL B=1 C=1 + RETURN B=1)
	// Length 4: three sub-forms
	//   - [1]=CALL B=1 C=2 + [2]=RETURN B=2 + [3]=dead RETURN: 0 args 1 return (getter)
	//   - [1]=LOADK, [2]=CALL B=2 C=1, [3]=RETURN B=1: 1 K arg 0 returns (setter)
	//   - [1]=MOVE, [2]=CALL B=2 C=1, [3]=RETURN B=1: 1 reg arg 0 returns (setter)
	// Length 5: 2 args 0 returns — GETUPVAL/MOVE + (LOADK|MOVE) + (LOADK|MOVE) + CALL B=3 C=1 + RETURN B=1
	//   four combinations K+K / K+R / R+K / R+R (all setters, callArgCount=2)
	var callIdx, retIdx int
	var argK uint64
	var argReg uint8
	var arg2K uint64
	var arg2Reg uint8
	var arg2IsK bool
	var arg3K uint64
	var arg3Reg uint8
	var arg3IsK bool
	var arg4K uint64
	var arg4Reg uint8
	var arg4IsK bool
	var arg5K uint64
	var arg5Reg uint8
	var arg5IsK bool
	var arg6K uint64
	var arg6Reg uint8
	var arg6IsK bool
	var arg7K uint64
	var arg7Reg uint8
	var arg7IsK bool
	var argCount uint8
	var argIsK bool
	switch codeLen {
	case 3:
		callIdx = 1
		retIdx = 2
		argCount = 0
	case 4:
		secondOp := bytecode.Op(proto.Code[1])
		switch secondOp {
		case bytecode.CALL:
			callIdx = 1
			retIdx = 2
			argCount = 0
			if bytecode.Op(proto.Code[3]) != bytecode.RETURN {
				return shapeInfo{}, false
			}
		case bytecode.LOADK:
			lkA := bytecode.A(proto.Code[1])
			lkBx := bytecode.Bx(proto.Code[1])
			if lkA != op0A+1 {
				return shapeInfo{}, false
			}
			if lkBx < 0 || lkBx >= len(proto.Consts) {
				return shapeInfo{}, false
			}
			argK = uint64(proto.Consts[lkBx])
			argIsK = true
			callIdx = 2
			retIdx = 3
			argCount = 1
		case bytecode.MOVE:
			mvA := bytecode.A(proto.Code[1])
			mvB := bytecode.B(proto.Code[1])
			if mvA != op0A+1 {
				return shapeInfo{}, false
			}
			if mvB > 254 {
				return shapeInfo{}, false
			}
			argReg = uint8(mvB)
			argIsK = false
			callIdx = 2
			retIdx = 3
			argCount = 1
		default:
			return shapeInfo{}, false
		}
	case 5:
		// Length 5 sub-branch discrimination:
		//   - getter with 1 K/reg arg 1 return: [0] MOVE/GETUPVAL, [1] LOADK/MOVE,
		//     [2] CALL B=2 C=2, [3] RETURN A=callA B=2, [4] implicit RETURN B=1
		//   - setter with 2 args 0 returns: [0] MOVE/GETUPVAL, [1] LOADK/MOVE, [2] LOADK/MOVE,
		//     [3] CALL B=3 C=1, [4] RETURN B=1
		// Key discriminator: Code[2] is CALL means getter 1 arg / otherwise setter 2 args.
		if bytecode.Op(proto.Code[2]) == bytecode.CALL {
			// getter 1 arg 1 return: [1] LOADK/MOVE, [2] CALL B=2 C=2, [3] RETURN A=callA B=2,
			// [4] implicit RETURN B=1
			secondOp := bytecode.Op(proto.Code[1])
			switch secondOp {
			case bytecode.LOADK:
				lkA := bytecode.A(proto.Code[1])
				lkBx := bytecode.Bx(proto.Code[1])
				if lkA != op0A+1 {
					return shapeInfo{}, false
				}
				if lkBx < 0 || lkBx >= len(proto.Consts) {
					return shapeInfo{}, false
				}
				argK = uint64(proto.Consts[lkBx])
				argIsK = true
			case bytecode.MOVE:
				mvA := bytecode.A(proto.Code[1])
				mvB := bytecode.B(proto.Code[1])
				if mvA != op0A+1 {
					return shapeInfo{}, false
				}
				if mvB > 254 {
					return shapeInfo{}, false
				}
				argReg = uint8(mvB)
				argIsK = false
			default:
				return shapeInfo{}, false
			}
			callIdx = 2
			retIdx = 3
			argCount = 1
			// Validate [4] implicit RETURN B=1
			implRet := proto.Code[4]
			if bytecode.Op(implRet) != bytecode.RETURN || bytecode.B(implRet) != 1 {
				return shapeInfo{}, false
			}
		} else {
			// setter 2 args 0 returns: GETUPVAL/MOVE + (LOADK|MOVE) + (LOADK|MOVE) + CALL + RETURN
			// four combinations: K+K / K+R / R+K / R+R (a morphological extension of the
			// same PJ5 main path that is actually wired up)
			secondOp := bytecode.Op(proto.Code[1])
			thirdOp := bytecode.Op(proto.Code[2])
			if (secondOp != bytecode.LOADK && secondOp != bytecode.MOVE) ||
				(thirdOp != bytecode.LOADK && thirdOp != bytecode.MOVE) {
				return shapeInfo{}, false
			}
			op2A := bytecode.A(proto.Code[1])
			op3A := bytecode.A(proto.Code[2])
			if op2A != op0A+1 || op3A != op0A+2 {
				return shapeInfo{}, false
			}
			// Load first arg
			if secondOp == bytecode.LOADK {
				lk1Bx := bytecode.Bx(proto.Code[1])
				if lk1Bx < 0 || lk1Bx >= len(proto.Consts) {
					return shapeInfo{}, false
				}
				argK = uint64(proto.Consts[lk1Bx])
				argIsK = true
			} else {
				mv1B := bytecode.B(proto.Code[1])
				if mv1B > 254 {
					return shapeInfo{}, false
				}
				argReg = uint8(mv1B)
				argIsK = false
			}
			// Load second arg
			if thirdOp == bytecode.LOADK {
				lk2Bx := bytecode.Bx(proto.Code[2])
				if lk2Bx < 0 || lk2Bx >= len(proto.Consts) {
					return shapeInfo{}, false
				}
				arg2K = uint64(proto.Consts[lk2Bx])
				arg2IsK = true
			} else {
				mv2B := bytecode.B(proto.Code[2])
				if mv2B > 254 {
					return shapeInfo{}, false
				}
				arg2Reg = uint8(mv2B)
				arg2IsK = false
			}
			callIdx = 3
			retIdx = 4
			argCount = 2
		}
	case 6:
		// Length 6 has three sub-forms (discriminator key:
		//   Code[1] is CALL → 0 args N=2 return-value getter (callee call + 2 MOVE copies + RETURN B=3)
		//   Code[3] is CALL → getter 2 args 1 return
		//   otherwise Code[3] is a load → setter 3 args 0 returns)
		if bytecode.Op(proto.Code[1]) == bytecode.CALL {
			// 0 args N=2 return-value getter: [0] MOVE/GETUPVAL, [1] CALL B=1 C=3,
			// [2] MOVE A=callA+0+2 B=callA+0, [3] MOVE A=callA+1+2 B=callA+1,
			// [4] RETURN A=callA+2 B=3, [5] implicit RETURN B=1
			//
			// The Run-side prelude does not execute the MOVE copies (it lands directly into
			// R(callA..) from CallBaseline) and still calls host.DoReturn(retA=callA, retB=3)
			// handled by the host multi-value path — but because luac emits RETURN.A=callA+2
			// rather than callA, Run must first do the N MOVE copies
			// (R(callA+nret+k) ← R(callA+k)) and then call DoReturn(retA=callA+nret, retB=nret+1)
			// to preserve byte-equal.
			callIdx = 1
			retIdx = 4
			argCount = 0
			// Validate [5] implicit RETURN B=1
			implRet := proto.Code[5]
			if bytecode.Op(implRet) != bytecode.RETURN || bytecode.B(implRet) != 1 {
				return shapeInfo{}, false
			}
		} else if bytecode.Op(proto.Code[3]) == bytecode.CALL {
			// getter 2 args 1 return
			if !decodeArgFromOp(proto, 1, op0A+1, &argIsK, &argK, &argReg) ||
				!decodeArgFromOp(proto, 2, op0A+2, &arg2IsK, &arg2K, &arg2Reg) {
				return shapeInfo{}, false
			}
			callIdx = 3
			retIdx = 4
			argCount = 2
			// Validate [5] implicit RETURN B=1
			implRet := proto.Code[5]
			if bytecode.Op(implRet) != bytecode.RETURN || bytecode.B(implRet) != 1 {
				return shapeInfo{}, false
			}
		} else {
			// setter 3 args 0 returns
			if !decodeArgFromOp(proto, 1, op0A+1, &argIsK, &argK, &argReg) ||
				!decodeArgFromOp(proto, 2, op0A+2, &arg2IsK, &arg2K, &arg2Reg) ||
				!decodeArgFromOp(proto, 3, op0A+3, &arg3IsK, &arg3K, &arg3Reg) {
				return shapeInfo{}, false
			}
			callIdx = 4
			retIdx = 5
			argCount = 3
		}
	case 7:
		// Length 7, four sub-forms (discriminant:
		//   Code[1] is CALL → 0-arg, N=3 return-value getter
		//   Code[2] is CALL → 1 K/reg arg, N=2 return-value getter
		//   Code[5] is CALL → setter, 4 args, 0 returns
		//   otherwise Code[4] is CALL → getter, 3 args, 1 return)
		if bytecode.Op(proto.Code[1]) == bytecode.CALL {
			// 0-arg, N=3 return-value getter: [0] MOVE/GETUPVAL, [1] CALL B=1 C=4,
			// [2..4] MOVE, [5] RETURN A=callA+3 B=4, [6] implicit RETURN B=1
			callIdx = 1
			retIdx = 5
			argCount = 0
			// Verify [6] implicit RETURN B=1
			implRet := proto.Code[6]
			if bytecode.Op(implRet) != bytecode.RETURN || bytecode.B(implRet) != 1 {
				return shapeInfo{}, false
			}
		} else if bytecode.Op(proto.Code[2]) == bytecode.CALL {
			// 1 K/reg arg, N=2 return-value getter: [0] MOVE/GETUPVAL, [1] (LOADK|MOVE),
			// [2] CALL B=2 C=3, [3] MOVE, [4] MOVE, [5] RETURN A=callA+2 B=3, [6] implicit RETURN B=1
			if !decodeArgFromOp(proto, 1, op0A+1, &argIsK, &argK, &argReg) {
				return shapeInfo{}, false
			}
			callIdx = 2
			retIdx = 5
			argCount = 1
			// Verify [6] implicit RETURN B=1
			implRet := proto.Code[6]
			if bytecode.Op(implRet) != bytecode.RETURN || bytecode.B(implRet) != 1 {
				return shapeInfo{}, false
			}
		} else if bytecode.Op(proto.Code[5]) == bytecode.CALL {
			// setter, 4 args, 0 returns: [0] MOVE/GETUPVAL, [1..4] (LOADK|MOVE),
			// [5] CALL B=5 C=1, [6] RETURN B=1
			if !decodeArgFromOp(proto, 1, op0A+1, &argIsK, &argK, &argReg) ||
				!decodeArgFromOp(proto, 2, op0A+2, &arg2IsK, &arg2K, &arg2Reg) ||
				!decodeArgFromOp(proto, 3, op0A+3, &arg3IsK, &arg3K, &arg3Reg) ||
				!decodeArgFromOp(proto, 4, op0A+4, &arg4IsK, &arg4K, &arg4Reg) {
				return shapeInfo{}, false
			}
			callIdx = 5
			retIdx = 6
			argCount = 4
		} else {
			// getter, 3 args, 1 return: [0] MOVE/GETUPVAL, [1..3] (LOADK|MOVE),
			// [4] CALL B=4 C=2, [5] RETURN A=callA B=2, [6] implicit RETURN B=1
			if !decodeArgFromOp(proto, 1, op0A+1, &argIsK, &argK, &argReg) ||
				!decodeArgFromOp(proto, 2, op0A+2, &arg2IsK, &arg2K, &arg2Reg) ||
				!decodeArgFromOp(proto, 3, op0A+3, &arg3IsK, &arg3K, &arg3Reg) {
				return shapeInfo{}, false
			}
			callIdx = 4
			retIdx = 5
			argCount = 3
			// Verify [6] implicit RETURN B=1
			implRet := proto.Code[6]
			if bytecode.Op(implRet) != bytecode.RETURN || bytecode.B(implRet) != 1 {
				return shapeInfo{}, false
			}
		}
	case 8:
		// Length 8, four sub-forms (discriminant:
		//   Code[2] is CALL → 1 K/reg arg, N=3 return-value getter
		//   Code[5] is CALL → getter, 4 args, 1 return
		//   Code[6] is CALL → setter, 5 args, 0 returns)
		if bytecode.Op(proto.Code[2]) == bytecode.CALL {
			// 1 K/reg arg, N=3 return-value getter: [0] MOVE/GETUPVAL, [1] (LOADK|MOVE),
			// [2] CALL B=2 C=4, [3..5] MOVE, [6] RETURN A=callA+3 B=4, [7] implicit RETURN B=1
			if !decodeArgFromOp(proto, 1, op0A+1, &argIsK, &argK, &argReg) {
				return shapeInfo{}, false
			}
			callIdx = 2
			retIdx = 6
			argCount = 1
			// Verify [7] implicit RETURN B=1
			implRet := proto.Code[7]
			if bytecode.Op(implRet) != bytecode.RETURN || bytecode.B(implRet) != 1 {
				return shapeInfo{}, false
			}
		} else if bytecode.Op(proto.Code[5]) == bytecode.CALL {
			// getter, 4 args, 1 return: [0] MOVE/GETUPVAL, [1..4] (LOADK|MOVE),
			// [5] CALL B=5 C=2, [6] RETURN A=callA B=2, [7] implicit RETURN B=1
			if !decodeArgFromOp(proto, 1, op0A+1, &argIsK, &argK, &argReg) ||
				!decodeArgFromOp(proto, 2, op0A+2, &arg2IsK, &arg2K, &arg2Reg) ||
				!decodeArgFromOp(proto, 3, op0A+3, &arg3IsK, &arg3K, &arg3Reg) ||
				!decodeArgFromOp(proto, 4, op0A+4, &arg4IsK, &arg4K, &arg4Reg) {
				return shapeInfo{}, false
			}
			callIdx = 5
			retIdx = 6
			argCount = 4
			// Verify [7] implicit RETURN B=1
			implRet := proto.Code[7]
			if bytecode.Op(implRet) != bytecode.RETURN || bytecode.B(implRet) != 1 {
				return shapeInfo{}, false
			}
		} else if bytecode.Op(proto.Code[6]) == bytecode.CALL {
			// setter, 5 args, 0 returns: [0] MOVE/GETUPVAL, [1..5] (LOADK|MOVE),
			// [6] CALL B=6 C=1, [7] RETURN B=1
			if !decodeArgFromOp(proto, 1, op0A+1, &argIsK, &argK, &argReg) ||
				!decodeArgFromOp(proto, 2, op0A+2, &arg2IsK, &arg2K, &arg2Reg) ||
				!decodeArgFromOp(proto, 3, op0A+3, &arg3IsK, &arg3K, &arg3Reg) ||
				!decodeArgFromOp(proto, 4, op0A+4, &arg4IsK, &arg4K, &arg4Reg) ||
				!decodeArgFromOp(proto, 5, op0A+5, &arg5IsK, &arg5K, &arg5Reg) {
				return shapeInfo{}, false
			}
			callIdx = 6
			retIdx = 7
			argCount = 5
		} else {
			return shapeInfo{}, false
		}
	case 9:
		// Length 9, two sub-forms (discriminant: Code[6] is CALL):
		//   - getter, 5 args, 1 return: Code[6]=CALL B=6 C=2 + Code[7]=RETURN A=callA B=2 + Code[8]=implicit
		//   - setter, 6 args, 0 returns: Code[7]=CALL B=7 C=1 + Code[8]=RETURN B=1
		if bytecode.Op(proto.Code[6]) == bytecode.CALL {
			// getter, 5 args, 1 return
			if !decodeArgFromOp(proto, 1, op0A+1, &argIsK, &argK, &argReg) ||
				!decodeArgFromOp(proto, 2, op0A+2, &arg2IsK, &arg2K, &arg2Reg) ||
				!decodeArgFromOp(proto, 3, op0A+3, &arg3IsK, &arg3K, &arg3Reg) ||
				!decodeArgFromOp(proto, 4, op0A+4, &arg4IsK, &arg4K, &arg4Reg) ||
				!decodeArgFromOp(proto, 5, op0A+5, &arg5IsK, &arg5K, &arg5Reg) {
				return shapeInfo{}, false
			}
			callIdx = 6
			retIdx = 7
			argCount = 5
			// Verify [8] implicit RETURN B=1
			implRet := proto.Code[8]
			if bytecode.Op(implRet) != bytecode.RETURN || bytecode.B(implRet) != 1 {
				return shapeInfo{}, false
			}
		} else if bytecode.Op(proto.Code[7]) == bytecode.CALL {
			// setter, 6 args, 0 returns
			if !decodeArgFromOp(proto, 1, op0A+1, &argIsK, &argK, &argReg) ||
				!decodeArgFromOp(proto, 2, op0A+2, &arg2IsK, &arg2K, &arg2Reg) ||
				!decodeArgFromOp(proto, 3, op0A+3, &arg3IsK, &arg3K, &arg3Reg) ||
				!decodeArgFromOp(proto, 4, op0A+4, &arg4IsK, &arg4K, &arg4Reg) ||
				!decodeArgFromOp(proto, 5, op0A+5, &arg5IsK, &arg5K, &arg5Reg) ||
				!decodeArgFromOp(proto, 6, op0A+6, &arg6IsK, &arg6K, &arg6Reg) {
				return shapeInfo{}, false
			}
			callIdx = 7
			retIdx = 8
			argCount = 6
		} else {
			return shapeInfo{}, false
		}
	case 10:
		// Length 10: setter 7 args 0 returns (Code[8]=CALL B=8 C=1) / getter 6 args 1 return (Code[7]=CALL B=7 C=2)
		// Discriminant: whether Code[7] vs Code[8] is CALL
		if bytecode.Op(proto.Code[7]) == bytecode.CALL {
			// getter, 6 args, 1 return (as above)
			if !decodeArgFromOp(proto, 1, op0A+1, &argIsK, &argK, &argReg) ||
				!decodeArgFromOp(proto, 2, op0A+2, &arg2IsK, &arg2K, &arg2Reg) ||
				!decodeArgFromOp(proto, 3, op0A+3, &arg3IsK, &arg3K, &arg3Reg) ||
				!decodeArgFromOp(proto, 4, op0A+4, &arg4IsK, &arg4K, &arg4Reg) ||
				!decodeArgFromOp(proto, 5, op0A+5, &arg5IsK, &arg5K, &arg5Reg) ||
				!decodeArgFromOp(proto, 6, op0A+6, &arg6IsK, &arg6K, &arg6Reg) {
				return shapeInfo{}, false
			}
			callIdx = 7
			retIdx = 8
			argCount = 6
			implRet := proto.Code[9]
			if bytecode.Op(implRet) != bytecode.RETURN || bytecode.B(implRet) != 1 {
				return shapeInfo{}, false
			}
		} else if bytecode.Op(proto.Code[8]) == bytecode.CALL {
			// setter, 7 args, 0 returns
			if !decodeArgFromOp(proto, 1, op0A+1, &argIsK, &argK, &argReg) ||
				!decodeArgFromOp(proto, 2, op0A+2, &arg2IsK, &arg2K, &arg2Reg) ||
				!decodeArgFromOp(proto, 3, op0A+3, &arg3IsK, &arg3K, &arg3Reg) ||
				!decodeArgFromOp(proto, 4, op0A+4, &arg4IsK, &arg4K, &arg4Reg) ||
				!decodeArgFromOp(proto, 5, op0A+5, &arg5IsK, &arg5K, &arg5Reg) ||
				!decodeArgFromOp(proto, 6, op0A+6, &arg6IsK, &arg6K, &arg6Reg) ||
				!decodeArgFromOp(proto, 7, op0A+7, &arg7IsK, &arg7K, &arg7Reg) {
				return shapeInfo{}, false
			}
			callIdx = 8
			retIdx = 9
			argCount = 7
		} else {
			return shapeInfo{}, false
		}
	case 11:
		// Length 11: getter 7 args 1 return (Code[8]=CALL B=8 C=2 + RETURN A=callA B=2 + implicit RETURN B=1)
		if bytecode.Op(proto.Code[8]) != bytecode.CALL {
			return shapeInfo{}, false
		}
		if !decodeArgFromOp(proto, 1, op0A+1, &argIsK, &argK, &argReg) ||
			!decodeArgFromOp(proto, 2, op0A+2, &arg2IsK, &arg2K, &arg2Reg) ||
			!decodeArgFromOp(proto, 3, op0A+3, &arg3IsK, &arg3K, &arg3Reg) ||
			!decodeArgFromOp(proto, 4, op0A+4, &arg4IsK, &arg4K, &arg4Reg) ||
			!decodeArgFromOp(proto, 5, op0A+5, &arg5IsK, &arg5K, &arg5Reg) ||
			!decodeArgFromOp(proto, 6, op0A+6, &arg6IsK, &arg6K, &arg6Reg) ||
			!decodeArgFromOp(proto, 7, op0A+7, &arg7IsK, &arg7K, &arg7Reg) {
			return shapeInfo{}, false
		}
		callIdx = 8
		retIdx = 9
		argCount = 7
		// Verify [10] implicit RETURN B=1
		implRet := proto.Code[10]
		if bytecode.Op(implRet) != bytecode.RETURN || bytecode.B(implRet) != 1 {
			return shapeInfo{}, false
		}
	}

	if bytecode.Op(proto.Code[callIdx]) != bytecode.CALL ||
		bytecode.Op(proto.Code[retIdx]) != bytecode.RETURN {
		return shapeInfo{}, false
	}
	// Every instruction strictly between the CALL and the RETURN must be a
	// multi-return MOVE copy (R(callA+nret+k) <- R(callA+k)); the call-void
	// prelude places all argument loads BEFORE the CALL. Any other op here
	// (e.g. SETGLOBAL / a return-slot-overwriting LOADK doing real work)
	// means this is NOT a call-void form — reject so it routes to a
	// faithful path instead of silently dropping those ops and returning
	// the CALL result. The clC>=3 N-return getter branch re-validates the
	// exact MOVE operands below; this guard also covers the clC==1 setter
	// and clC==2 1-return getter branches, which otherwise never inspected
	// the gap. (Nightly oracle fuzz #136: a length-6 proto
	// `GETUPVAL; CALL 0 1 2; SETGLOBAL 0; LOADK 0; RETURN 0 2` was
	// mis-accepted as a 1-return getter, so the callee closure was returned
	// instead of the LOADK'd 0 once the proto was entered a second time.)
	for k := callIdx + 1; k < retIdx; k++ {
		if bytecode.Op(proto.Code[k]) != bytecode.MOVE {
			return shapeInfo{}, false
		}
	}
	clA := bytecode.A(proto.Code[callIdx])
	clB := bytecode.B(proto.Code[callIdx])
	clC := bytecode.C(proto.Code[callIdx])
	rtA := bytecode.A(proto.Code[retIdx])
	rtB := bytecode.B(proto.Code[retIdx])
	if clA != op0A {
		return shapeInfo{}, false
	}
	// CALL.B = argCount + 1
	if int(clB) != int(argCount)+1 {
		return shapeInfo{}, false
	}
	// Return-value check: CALL.C/RETURN.B has 3 forms total:
	//   - setter: CALL.C=1 (0 return values) + RETURN.B=1 (0 return values) + retA=0
	//   - getter 1 return: CALL.C=2 (1 return value) + RETURN.B=2 (1 return value) + RETURN.A=callA
	//   - N-return getter (N>=2): CALL.C=N+1 + RETURN.B=N+1 + RETURN.A=callA+N
	//     (luac emits N MOVE ops copying R(callA..callA+N-1) to R(callA+N..callA+2N-1) then RETURN)
	var retACalc, retBCalc uint8
	var multiRet uint8
	if clC == 1 && rtB == 1 {
		// setter form: 0 return values, RETURN immediately follows CALL with no instructions between.
		if retIdx != callIdx+1 {
			return shapeInfo{}, false
		}
		retACalc = 0
		retBCalc = 1
	} else if clC == 2 && rtB == 2 {
		// getter 1-return form: RETURN.A must == callA (callee return value
		// lands in R(callA)). A single return value needs no intermediate MOVE
		// copy; RETURN immediately follows CALL with no instructions between —
		// luac never inserts instructions here. Explicitly assert there is no
		// gap (a stricter invariant than the all-MOVE CALL..RETURN guard above),
		// to prevent a future dispatch branch that pulls callIdx/retIdx apart
		// from silently swallowing a MOVE stuck in between when clC<3 (hardening
		// surface for nightly fuzz #136).
		if retIdx != callIdx+1 {
			return shapeInfo{}, false
		}
		if rtA != clA {
			return shapeInfo{}, false
		}
		retACalc = uint8(rtA)
		retBCalc = 2
	} else if clC >= 3 && int(rtB) == int(clC) {
		// N>=2 return-value getter: RETURN.A must == callA + (clC-1) = callA + nret
		// argCount may be 0 (no args) or >=1 (with args, e.g. `local a,b=f(arg); return a,b`)
		nret := clC - 1
		if rtA != clA+nret {
			return shapeInfo{}, false
		}
		// Verify the N intermediate MOVE copies (luac emits R(callA+nret+k) ← R(callA+k))
		for k := 0; k < nret; k++ {
			mv := proto.Code[callIdx+1+k]
			if bytecode.Op(mv) != bytecode.MOVE {
				return shapeInfo{}, false
			}
			if bytecode.A(mv) != clA+nret+k || bytecode.B(mv) != clA+k {
				return shapeInfo{}, false
			}
		}
		retACalc = uint8(rtA)
		retBCalc = uint8(rtB)
		multiRet = uint8(nret)
	} else {
		return shapeInfo{}, false
	}
	return shapeInfo{
		ok:             true,
		retA:           retACalc,
		retB:           retBCalc,
		retPC:          uint8(retIdx),
		preludeOp:      uint8(bytecode.CALL),
		preludeArg:     uint32(op0B),
		isCallVoid:     true,
		isCallUpval:    op0 == bytecode.GETUPVAL,
		callA:          uint8(clA),
		callB:          uint8(clB),
		callC:          uint8(clC),
		callArgCount:   argCount,
		callMultiRet:   multiRet,
		callArg1IsK:    argIsK,
		callArg1K:      argK,
		callArg1RegSrc: argReg,
		callArg2IsK:    arg2IsK,
		callArg2K:      arg2K,
		callArg2RegSrc: arg2Reg,
		callArg3IsK:    arg3IsK,
		callArg3K:      arg3K,
		callArg3RegSrc: arg3Reg,
		callArg4IsK:    arg4IsK,
		callArg4K:      arg4K,
		callArg4RegSrc: arg4Reg,
		callArg5IsK:    arg5IsK,
		callArg5K:      arg5K,
		callArg5RegSrc: arg5Reg,
		callArg6IsK:    arg6IsK,
		callArg6K:      arg6K,
		callArg6RegSrc: arg6Reg,
		callArg7IsK:    arg7IsK,
		callArg7K:      arg7K,
		callArg7RegSrc: arg7Reg,
	}, true
}

// analyzeTailCallForm recognizes the PJ5 TAILCALL form (per
// docs/design/p4-method-jit/05-system-pipeline.md §4.3 + 06-backends.md §3.5):
// `function() return f() end` / `function() return f(K) end` etc. — a single-value
// CallExpr as the sole return expression, which luac translates into TAILCALL +
// trailing RETURN B=0 + implicit RETURN (the single-CallExpr fast path of
// stmtReturn, frontend/compile/stmt.go::stmtReturn).
//
// luac compiled forms (0/1 K/1 reg/2 K args × MOVE/GETUPVAL, 8 subforms total;
// TAILCALL.C is always 0, i.e. "return values to top"):
//
//	form TA0: `function(g) return g() end` (parameter callee, 0 args)
//	  [0] MOVE     A=callA B=callee source reg
//	  [1] TAILCALL A=callA B=1 C=0
//	  [2] RETURN   A=callA B=0 (dead, to-top)
//	  [3] RETURN   A=0 B=1     (implicit)
//
//	form TB0: `local function f()...; local function bounce() return f() end`
//	  [0] GETUPVAL A=callA B=upvalue index
//	  [1] TAILCALL A=callA B=1 C=0
//	  [2] RETURN   A=callA B=0
//	  [3] RETURN   A=0 B=1
//
//	form TA1K/TB1K: 1 K arg (length 5)
//	  [0] MOVE/GETUPVAL A=callA B=...
//	  [1] LOADK    A=callA+1 Bx=K idx
//	  [2] TAILCALL A=callA B=2 C=0
//	  [3] RETURN   A=callA B=0
//	  [4] RETURN   A=0 B=1
//
//	form TA1R/TB1R: 1 reg arg (length 5)
//	  [0] MOVE/GETUPVAL A=callA B=...
//	  [1] MOVE     A=callA+1 B=source reg
//	  [2] TAILCALL A=callA B=2 C=0
//	  [3] RETURN   A=callA B=0
//	  [4] RETURN   A=0 B=1
//
//	form TA2K/TB2K: 2 K args (length 6)
//	  [0] MOVE/GETUPVAL A=callA
//	  [1] LOADK    A=callA+1
//	  [2] LOADK    A=callA+2
//	  [3] TAILCALL A=callA B=3 C=0
//	  [4] RETURN   A=callA B=0
//	  [5] RETURN   A=0 B=1
//
// **Run-side prelude path** (see code.go::Run TAILCALL case):
//   - MOVE/GETUPVAL loads the callee into R(callA) (host.GetReg/GetUpval + SetReg)
//   - LOADK/MOVE loads args into R(callA+1)/R(callA+2)
//   - calls host.TailCall(base, tailPC, callA, callB, callC), a three-way branch:
//     0=Lua tail complete → Run returns 0 directly (skips DoReturn, this frame already popped)
//     1=ERR → return 1
//     2=host tail complete → Run falls into dead RETURN to-top via DoReturn (retB=0 to-top)
//
// Returns (shapeInfo{}, false) if any condition fails — falls back to the
// analyzeCallVoidForm path (CALL form) or the later analyzeShape main dispatch.
func analyzeTailCallForm(proto *bytecode.Proto) (shapeInfo, bool) {
	codeLen := len(proto.Code)
	// The TAILCALL form is at least length 4 (0 args + dead RETURN + implicit RETURN), at most 11 (7 args)
	if codeLen < 4 || codeLen > 11 {
		return shapeInfo{}, false
	}
	op0 := bytecode.Op(proto.Code[0])
	if op0 != bytecode.MOVE && op0 != bytecode.GETUPVAL {
		return shapeInfo{}, false
	}
	op0A := bytecode.A(proto.Code[0])
	op0B := bytecode.B(proto.Code[0])
	if op0A > 254 || op0B > 254 {
		return shapeInfo{}, false
	}

	var tailIdx int
	var argK uint64
	var argReg uint8
	var arg2K uint64
	var arg2Reg uint8
	var arg2IsK bool
	var arg3K uint64
	var arg3Reg uint8
	var arg3IsK bool
	var arg4K uint64
	var arg4Reg uint8
	var arg4IsK bool
	var arg5K uint64
	var arg5Reg uint8
	var arg5IsK bool
	var arg6K uint64
	var arg6Reg uint8
	var arg6IsK bool
	var arg7K uint64
	var arg7Reg uint8
	var arg7IsK bool
	var argCount uint8
	var argIsK bool
	switch codeLen {
	case 4:
		// 0 args: [0] MOVE/GETUPVAL, [1] TAILCALL, [2] RETURN B=0, [3] RETURN B=1
		tailIdx = 1
		argCount = 0
	case 5:
		// 1 arg: [0] MOVE/GETUPVAL, [1] LOADK/MOVE, [2] TAILCALL, [3] RETURN B=0,
		// [4] RETURN B=1
		secondOp := bytecode.Op(proto.Code[1])
		switch secondOp {
		case bytecode.LOADK:
			lkA := bytecode.A(proto.Code[1])
			lkBx := bytecode.Bx(proto.Code[1])
			if lkA != op0A+1 {
				return shapeInfo{}, false
			}
			if lkBx < 0 || lkBx >= len(proto.Consts) {
				return shapeInfo{}, false
			}
			argK = uint64(proto.Consts[lkBx])
			argIsK = true
			argCount = 1
		case bytecode.MOVE:
			mvA := bytecode.A(proto.Code[1])
			mvB := bytecode.B(proto.Code[1])
			if mvA != op0A+1 {
				return shapeInfo{}, false
			}
			if mvB > 254 {
				return shapeInfo{}, false
			}
			argReg = uint8(mvB)
			argIsK = false
			argCount = 1
		default:
			return shapeInfo{}, false
		}
		tailIdx = 2
	case 6:
		// 2 args: [0] MOVE/GETUPVAL, [1] (LOADK|MOVE), [2] (LOADK|MOVE), [3] TAILCALL,
		// [4] RETURN B=0, [5] RETURN B=1 — four combinations K+K / K+R / R+K / R+R
		secondOp := bytecode.Op(proto.Code[1])
		thirdOp := bytecode.Op(proto.Code[2])
		if (secondOp != bytecode.LOADK && secondOp != bytecode.MOVE) ||
			(thirdOp != bytecode.LOADK && thirdOp != bytecode.MOVE) {
			return shapeInfo{}, false
		}
		op2A := bytecode.A(proto.Code[1])
		op3A := bytecode.A(proto.Code[2])
		if op2A != op0A+1 || op3A != op0A+2 {
			return shapeInfo{}, false
		}
		// First arg load
		if secondOp == bytecode.LOADK {
			lk1Bx := bytecode.Bx(proto.Code[1])
			if lk1Bx < 0 || lk1Bx >= len(proto.Consts) {
				return shapeInfo{}, false
			}
			argK = uint64(proto.Consts[lk1Bx])
			argIsK = true
		} else {
			mv1B := bytecode.B(proto.Code[1])
			if mv1B > 254 {
				return shapeInfo{}, false
			}
			argReg = uint8(mv1B)
			argIsK = false
		}
		// Second arg load
		if thirdOp == bytecode.LOADK {
			lk2Bx := bytecode.Bx(proto.Code[2])
			if lk2Bx < 0 || lk2Bx >= len(proto.Consts) {
				return shapeInfo{}, false
			}
			arg2K = uint64(proto.Consts[lk2Bx])
			arg2IsK = true
		} else {
			mv2B := bytecode.B(proto.Code[2])
			if mv2B > 254 {
				return shapeInfo{}, false
			}
			arg2Reg = uint8(mv2B)
			arg2IsK = false
		}
		argCount = 2
		tailIdx = 3
	case 7:
		// 3 args: [0] MOVE/GETUPVAL, [1..3] (LOADK|MOVE), [4] TAILCALL,
		// [5] RETURN B=0, [6] RETURN B=1 — combinations K+K+K / K+K+R / ... / R+R+R, 8 subforms
		if !decodeArgFromOp(proto, 1, op0A+1, &argIsK, &argK, &argReg) ||
			!decodeArgFromOp(proto, 2, op0A+2, &arg2IsK, &arg2K, &arg2Reg) ||
			!decodeArgFromOp(proto, 3, op0A+3, &arg3IsK, &arg3K, &arg3Reg) {
			return shapeInfo{}, false
		}
		argCount = 3
		tailIdx = 4
	case 8:
		// 4 args: [0] MOVE/GETUPVAL, [1..4] (LOADK|MOVE), [5] TAILCALL,
		// [6] RETURN B=0, [7] RETURN B=1 — combinations expand to 16 but this batch only
		// supports all-K / all-reg mixes (decodeArgFromOp accepts any LOADK/MOVE combination)
		if !decodeArgFromOp(proto, 1, op0A+1, &argIsK, &argK, &argReg) ||
			!decodeArgFromOp(proto, 2, op0A+2, &arg2IsK, &arg2K, &arg2Reg) ||
			!decodeArgFromOp(proto, 3, op0A+3, &arg3IsK, &arg3K, &arg3Reg) ||
			!decodeArgFromOp(proto, 4, op0A+4, &arg4IsK, &arg4K, &arg4Reg) {
			return shapeInfo{}, false
		}
		argCount = 4
		tailIdx = 5
	case 9:
		// 5 args: [0] MOVE/GETUPVAL, [1..5] (LOADK|MOVE), [6] TAILCALL,
		// [7] RETURN B=0, [8] RETURN B=1
		if !decodeArgFromOp(proto, 1, op0A+1, &argIsK, &argK, &argReg) ||
			!decodeArgFromOp(proto, 2, op0A+2, &arg2IsK, &arg2K, &arg2Reg) ||
			!decodeArgFromOp(proto, 3, op0A+3, &arg3IsK, &arg3K, &arg3Reg) ||
			!decodeArgFromOp(proto, 4, op0A+4, &arg4IsK, &arg4K, &arg4Reg) ||
			!decodeArgFromOp(proto, 5, op0A+5, &arg5IsK, &arg5K, &arg5Reg) {
			return shapeInfo{}, false
		}
		argCount = 5
		tailIdx = 6
	case 10:
		// 6 args TAILCALL: [0] MOVE/GETUPVAL, [1..6] (LOADK|MOVE), [7] TAILCALL,
		// [8] RETURN B=0, [9] RETURN B=1
		if !decodeArgFromOp(proto, 1, op0A+1, &argIsK, &argK, &argReg) ||
			!decodeArgFromOp(proto, 2, op0A+2, &arg2IsK, &arg2K, &arg2Reg) ||
			!decodeArgFromOp(proto, 3, op0A+3, &arg3IsK, &arg3K, &arg3Reg) ||
			!decodeArgFromOp(proto, 4, op0A+4, &arg4IsK, &arg4K, &arg4Reg) ||
			!decodeArgFromOp(proto, 5, op0A+5, &arg5IsK, &arg5K, &arg5Reg) ||
			!decodeArgFromOp(proto, 6, op0A+6, &arg6IsK, &arg6K, &arg6Reg) {
			return shapeInfo{}, false
		}
		argCount = 6
		tailIdx = 7
	case 11:
		// 7 args TAILCALL: [0] MOVE/GETUPVAL, [1..7] (LOADK|MOVE), [8] TAILCALL,
		// [9] RETURN B=0, [10] RETURN B=1
		if !decodeArgFromOp(proto, 1, op0A+1, &argIsK, &argK, &argReg) ||
			!decodeArgFromOp(proto, 2, op0A+2, &arg2IsK, &arg2K, &arg2Reg) ||
			!decodeArgFromOp(proto, 3, op0A+3, &arg3IsK, &arg3K, &arg3Reg) ||
			!decodeArgFromOp(proto, 4, op0A+4, &arg4IsK, &arg4K, &arg4Reg) ||
			!decodeArgFromOp(proto, 5, op0A+5, &arg5IsK, &arg5K, &arg5Reg) ||
			!decodeArgFromOp(proto, 6, op0A+6, &arg6IsK, &arg6K, &arg6Reg) ||
			!decodeArgFromOp(proto, 7, op0A+7, &arg7IsK, &arg7K, &arg7Reg) {
			return shapeInfo{}, false
		}
		argCount = 7
		tailIdx = 8
	}

	// Verify the trailing triple TAILCALL + dead RETURN B=0 + implicit RETURN B=1
	if bytecode.Op(proto.Code[tailIdx]) != bytecode.TAILCALL {
		return shapeInfo{}, false
	}
	deadRet := proto.Code[tailIdx+1]
	implRet := proto.Code[tailIdx+2]
	if bytecode.Op(deadRet) != bytecode.RETURN || bytecode.B(deadRet) != 0 {
		return shapeInfo{}, false
	}
	if bytecode.Op(implRet) != bytecode.RETURN || bytecode.B(implRet) != 1 {
		return shapeInfo{}, false
	}
	tlA := bytecode.A(proto.Code[tailIdx])
	tlB := bytecode.B(proto.Code[tailIdx])
	tlC := bytecode.C(proto.Code[tailIdx])
	if tlA != op0A {
		return shapeInfo{}, false
	}
	// TAILCALL.B = argCount + 1; TAILCALL.C is always 0
	if int(tlB) != int(argCount)+1 {
		return shapeInfo{}, false
	}
	if tlC != 0 {
		return shapeInfo{}, false
	}
	// dead RETURN.A must == callA (returnRange spans callA to top)
	if bytecode.A(deadRet) != tlA {
		return shapeInfo{}, false
	}

	// **Run-side contract**: when host.TailCall returns 2 (host tail), Run falls
	// into dead RETURN B=0 (to-top) via DoReturn, so retPC points at the dead
	// RETURN, retA=callA, retB=0 (inside DoReturn B=0 → nret = top - (base + a),
	// reusing the interpreter's multi-value return path).
	return shapeInfo{
		ok:             true,
		retA:           uint8(tlA),
		retB:           0, // dead RETURN B=0, DoReturn multi-value to-top path
		retPC:          uint8(tailIdx + 1),
		preludeOp:      uint8(bytecode.TAILCALL),
		preludeArg:     uint32(op0B),
		isTailCall:     true,
		isCallUpval:    op0 == bytecode.GETUPVAL,
		callA:          uint8(tlA),
		callB:          uint8(tlB),
		callC:          uint8(tlC),
		callArgCount:   argCount,
		callArg1IsK:    argIsK,
		callArg1K:      argK,
		callArg1RegSrc: argReg,
		callArg2IsK:    arg2IsK,
		callArg2K:      arg2K,
		callArg2RegSrc: arg2Reg,
		callArg3IsK:    arg3IsK,
		callArg3K:      arg3K,
		callArg3RegSrc: arg3Reg,
		callArg4IsK:    arg4IsK,
		callArg4K:      arg4K,
		callArg4RegSrc: arg4Reg,
		callArg5IsK:    arg5IsK,
		callArg5K:      arg5K,
		callArg5RegSrc: arg5Reg,
		callArg6IsK:    arg6IsK,
		callArg6K:      arg6K,
		callArg6RegSrc: arg6Reg,
		callArg7IsK:    arg7IsK,
		callArg7K:      arg7K,
		callArg7RegSrc: arg7Reg,
	}, true
}

// analyzeSelfCallForm recognizes the PJ5 SELF method-call inline form (per
// docs/design/p4-method-jit/05-system-pipeline.md §4.3 + 09 §9.17):
//
// `function(o) o:m() end` / `function() o:m() end` (upval recv) /
// `function(o) return o:m() end` (SELF + TAILCALL) etc. luac emits the SELF + CALL/
// TAILCALL inline form with length 4..6 (progressive whitelist discipline; the
// first batch covers 0/1 K/1 reg/2 K args × both receivers (M/U) × CALL void /
// CALL getter 1-return / TAILCALL).
//
// **Typical luac compiled form** (0 args void, length 4):
//
//	form M0: `function(o) o:m() end` (recv from parameter reg)
//	  [0] MOVE     A=callA B=recvSrc   (copy recv into R(callA), read by SELF.B)
//	  [1] SELF     A=callA B=callA C=K_method (R(callA)=R(callA)[K_m]; R(callA+1)=R(callA))
//	  [2] CALL     A=callA B=2 C=1     (0 args, 0 returns)
//	  [3] RETURN   A=0     B=1
//
//	form U0: `function() o:m() end` (recv from upvalue, o is an upval)
//	  [0] GETUPVAL A=callA B=upvalIdx
//	  [1] SELF     A=callA B=callA C=K_m
//	  [2] CALL     A=callA B=2 C=1
//	  [3] RETURN   A=0     B=1
//
// **Trigger conditions** (shared):
//   - Code length 4..6
//   - [0] = MOVE or GETUPVAL, [0].A == [1].A == [1].B (consistent callA across the SELF/CALL chain)
//   - [1] = SELF, A=callA B=callA C>=256 (K constant index)
//
// Returns (shapeInfo{}, false) if any condition fails — falls back to the original analyzeShape main dispatch.
func analyzeSelfCallForm(proto *bytecode.Proto) (shapeInfo, bool) {
	codeLen := len(proto.Code)
	if codeLen < 4 || codeLen > 11 {
		return shapeInfo{}, false
	}
	op0 := bytecode.Op(proto.Code[0])
	if op0 != bytecode.MOVE && op0 != bytecode.GETUPVAL {
		return shapeInfo{}, false
	}
	op0A := bytecode.A(proto.Code[0])
	op0B := bytecode.B(proto.Code[0])
	if op0A > 254 || op0B > 254 {
		return shapeInfo{}, false
	}
	// [1] = SELF callA callA RK_method
	op1 := bytecode.Op(proto.Code[1])
	if op1 != bytecode.SELF {
		return shapeInfo{}, false
	}
	selfA := bytecode.A(proto.Code[1])
	selfB := bytecode.B(proto.Code[1])
	selfC := bytecode.C(proto.Code[1])
	if selfA != op0A || selfB != op0A {
		// SELF's A/B must == MOVE/GETUPVAL.A (because the preceding op loads recv into R(callA))
		return shapeInfo{}, false
	}
	if selfC < 256 {
		// SELF.C must be a K constant index (method name) — the reg form is left for PJ5+ to extend
		return shapeInfo{}, false
	}
	callA := uint8(selfA)
	selfRK := uint16(selfC)

	switch codeLen {
	case 4:
		return analyzeSelfCallForm4(proto, callA, selfRK, op0, op0B)
	case 5:
		return analyzeSelfCallForm5(proto, callA, selfRK, op0, op0B)
	case 6:
		return analyzeSelfCallForm6(proto, callA, selfRK, op0, op0B)
	case 7:
		return analyzeSelfCallForm7(proto, callA, selfRK, op0, op0B)
	case 8:
		return analyzeSelfCallForm8(proto, callA, selfRK, op0, op0B)
	case 9:
		return analyzeSelfCallForm9(proto, callA, selfRK, op0, op0B)
	case 10:
		return analyzeSelfCallFormN(proto, callA, selfRK, op0, op0B, 6)
	case 11:
		return analyzeSelfCallFormN(proto, callA, selfRK, op0, op0B, 7)
	}
	return shapeInfo{}, false
}

// analyzeSelfCallFormN handles the length (4 + N args) form: CALL void N args / TAILCALL (N-1) args.
// callOpIdx = 2 + N (CALL comes after N LOADK/MOVE ops).
//
// length 10: N=6, CALL void 6 args / TAILCALL 5 args (5-arg TAILCALL is already handled by form9; this form only recognizes coexisting CALL void 6 args / TAILCALL 5 args)
// length 11: N=7, CALL void 7 args / TAILCALL 6 args
//
// Simplification strategy: this function recognizes only the two forms CALL void N args + TAILCALL (N-1) args.
func analyzeSelfCallFormN(proto *bytecode.Proto, callA uint8, selfRK uint16,
	op0 bytecode.OpCode, op0B int, nArgs int) (shapeInfo, bool) {
	// CALL void N args: [2..1+N] LOADK/MOVE, [2+N] CALL B=N+2 C=1, [3+N] RETURN B=1
	callOpIdx := 2 + nArgs
	if bytecode.Op(proto.Code[callOpIdx]) == bytecode.CALL {
		argsIsK := make([]bool, nArgs)
		argsK := make([]uint64, nArgs)
		argsReg := make([]uint8, nArgs)
		for i := 0; i < nArgs; i++ {
			if !decodeArgFromOp(proto, 2+i, int(callA)+2+i, &argsIsK[i], &argsK[i], &argsReg[i]) {
				return shapeInfo{}, false
			}
		}
		cA := bytecode.A(proto.Code[callOpIdx])
		cB := bytecode.B(proto.Code[callOpIdx])
		cC := bytecode.C(proto.Code[callOpIdx])
		// cC=1 void (0 returns) / cC=3,4 N=2,3 returns drop multi-ret (`local a,b=t:m(K×N)` kind)
		if cA != int(callA) || cB != nArgs+2 || !isValidSpecCallRetCount(cC) {
			return shapeInfo{}, false
		}
		if bytecode.Op(proto.Code[callOpIdx+1]) != bytecode.RETURN ||
			bytecode.B(proto.Code[callOpIdx+1]) != 1 {
			return shapeInfo{}, false
		}
		info := shapeInfo{
			ok:              true,
			retA:            0,
			retB:            1,
			retPC:           uint8(callOpIdx + 1),
			preludeOp:       uint8(bytecode.CALL),
			preludeArg:      uint32(op0B),
			isCallVoid:      true,
			isCallUpval:     op0 == bytecode.GETUPVAL,
			callA:           callA,
			callB:           uint8(cB),
			callC:           uint8(cC),
			callArgCount:    uint8(nArgs),
			isSelfCall:      true,
			selfCallA:       callA,
			selfMethodRK:    selfRK,
			selfRecvSrcReg:  uint8(op0B),
			selfRecvIsUpval: op0 == bytecode.GETUPVAL,
		}
		assignArgsToShape(&info, argsIsK, argsK, argsReg)
		return info, true
	}
	// TAILCALL (N-1) args: actually [callOpIdx-1] TAILCALL, [callOpIdx] RETURN A=callA B=0,
	// [callOpIdx+1] RETURN B=1. That is, length 10 N=6 is actually a 5-arg TAILCALL (callOpIdx-1 = 7).
	// Simplification: the outer codeLen derives nArgs (= codeLen - 4) and only recognizes N-arg CALL forms;
	// leftover TAILCALL forms are handled by form7/8/9 (which already support length 7/8/9 = 2/3/4-arg TAILCALL).
	// length 10: TAILCALL 5 args — probed via form10's callOpIdx-1:
	tailOpIdx := callOpIdx - 1 // 6 args - 1 = 5 args TAILCALL, at callOpIdx-1
	if bytecode.Op(proto.Code[tailOpIdx]) == bytecode.TAILCALL {
		nTailArgs := nArgs - 1
		argsIsK := make([]bool, nTailArgs)
		argsK := make([]uint64, nTailArgs)
		argsReg := make([]uint8, nTailArgs)
		for i := 0; i < nTailArgs; i++ {
			if !decodeArgFromOp(proto, 2+i, int(callA)+2+i, &argsIsK[i], &argsK[i], &argsReg[i]) {
				return shapeInfo{}, false
			}
		}
		cA := bytecode.A(proto.Code[tailOpIdx])
		cB := bytecode.B(proto.Code[tailOpIdx])
		cC := bytecode.C(proto.Code[tailOpIdx])
		if cA != int(callA) || cB != nTailArgs+2 || cC != 0 {
			return shapeInfo{}, false
		}
		if bytecode.Op(proto.Code[tailOpIdx+1]) != bytecode.RETURN ||
			bytecode.A(proto.Code[tailOpIdx+1]) != int(callA) ||
			bytecode.B(proto.Code[tailOpIdx+1]) != 0 ||
			bytecode.Op(proto.Code[tailOpIdx+2]) != bytecode.RETURN ||
			bytecode.B(proto.Code[tailOpIdx+2]) != 1 {
			return shapeInfo{}, false
		}
		info := shapeInfo{
			ok:              true,
			retA:            uint8(bytecode.A(proto.Code[tailOpIdx+1])),
			retB:            0,
			retPC:           uint8(tailOpIdx + 1),
			preludeOp:       uint8(bytecode.TAILCALL),
			preludeArg:      uint32(op0B),
			isTailCall:      true,
			isCallUpval:     op0 == bytecode.GETUPVAL,
			callA:           callA,
			callB:           uint8(cB),
			callC:           uint8(cC),
			callArgCount:    uint8(nTailArgs),
			isSelfCall:      true,
			selfCallA:       callA,
			selfMethodRK:    selfRK,
			selfRecvSrcReg:  uint8(op0B),
			selfRecvIsUpval: op0 == bytecode.GETUPVAL,
		}
		assignArgsToShape(&info, argsIsK, argsK, argsReg)
		return info, true
	}
	return shapeInfo{}, false
}

// analyzeSelfCallSpecForm recognizes the PJ5 SELF + CALL spec-template entry form (per
// §9.10 PJ4 EmitSelfNodeHit reuse + §9.17 PJ5 SELF inline upgrade):
//
// **Form boundary** (extends analyzeSelfCallForm's full 0..7 arg void/getter/tail
// forms, gated by an IC NodeHit feedback hit):
//
//	[0] MOVE/GETUPVAL A=callA B=recvSrc  (load recv into R(callA))
//	[1] SELF     A=callA B=callA C=K_method  (IC[1] = NodeHit feedback)
//	[2..1+N] LOADK/MOVE args (args loaded into R(callA+2..callA+1+N))
//	[2+N] CALL/TAILCALL A=callA B=N+2 C=1/0/2 (N args, 0/0-return/1-return)
//	[3+N] RETURN ...
//
// **Trigger conditions**:
//   - analyzeSelfCallForm(proto) returns a plain SELF inline shapeInfo (any 0..7 arg form)
//   - proto.IC[1].Kind == ICKindNodeHit (the P1 interpreter observed a hash-segment hit)
//   - feedback.Points[1].Kind == FBSelfMono + Confidence >= 0.99
//   - stableShape/Index consistent + stableKey frozen at compile time != Nil
//
// Returns (shapeInfo{}, false) if any condition fails — falls back to the plain analyzeSelfCallForm (host.Self) path.
//
// **Run-side execution**: runSpecSelfCall (the spec segment runs EmitSelfNodeHit to skip host.Self,
// deopts to host.Self on failure) + load args + host.CallBaseline + host.DoReturn.
func analyzeSelfCallSpecForm(proto *bytecode.Proto, feedback *bridge.TypeFeedback) (shapeInfo, bool) {
	// First recognize the plain SELF inline form (any 0..7 arg void/getter/tail)
	info, ok := analyzeSelfCallForm(proto)
	if !ok {
		return shapeInfo{}, false
	}
	// spec-template enablement scope (following isCallVoid's actual semantics =
	// preludeOp=CALL, covering multiple forms): CALL void retB=1 setter / CALL
	// getter retB=2 1-return / CALL retB=1 + cC=3/4 N>=2 returns (`local a,b=t:m()`
	// kind drop multi-ret) + the TAILCALL three-way branch. host.CallBaseline lands
	// return values into R(callA..callA+nret-1) per callC; host.DoReturn follows the
	// caller RETURN semantics per retA/retB. The two-layer protocol is decoupled, and
	// the spec segment is solely responsible for SELF/args/recv.
	if !info.isCallVoid && !info.isTailCall {
		return shapeInfo{}, false
	}
	// IC slot check (SELF is at pc=1, so proto.IC[1])
	if len(proto.IC) <= 1 {
		return shapeInfo{}, false
	}
	icSlot := proto.IC[1]
	if icSlot.Kind != bytecode.ICKindNodeHit {
		return shapeInfo{}, false
	}
	// feedback check (Points[1] aligns with SELF pc=1). **SELF aggregates into
	// FBSelfMono** (aggregator.go::extractTableFeedback opSelf branch), not
	// FBTableMono — PJ5 SELF + CALL is the first path that truly reaches SELF
	// feedback (PJ4 SELF NodeHit never reached real SELF feedback because luac never
	// actually emits the SELF + RETURN 2-op form, so it was only driven by synthetic
	// unit tests and mistakenly used FBTableMono there; this path uses the correct FBSelfMono).
	if feedback == nil || len(feedback.Points) < 2 {
		return shapeInfo{}, false
	}
	pf := feedback.Points[1]
	if pf.Kind != bridge.FBSelfMono || pf.Confidence < 0.99 {
		return shapeInfo{}, false
	}
	if pf.StableShape != icSlot.Shape || pf.StableIndex != icSlot.Index {
		return shapeInfo{}, false
	}
	// stableKey frozen at compile time (SELF.C is a K constant index)
	selfC := bytecode.C(proto.Code[1])
	kIdx := bytecode.KIdx(selfC)
	if kIdx < 0 || kIdx >= len(proto.Consts) {
		return shapeInfo{}, false
	}
	stableKey := uint64(proto.Consts[kIdx])
	if stableKey == uint64(value.Nil) {
		return shapeInfo{}, false
	}
	// Overlay the spec-template fields (preserving all of analyzeSelfCallForm's form
	// fields: callArgCount / callArg1..7K/RegSrc / isCallVoid / isCallUpval etc.)
	info.useSpecSelfCall = true
	info.icAReg = info.callA
	info.icBReg = info.callA // SELF.B = callA (recv slot, loaded by MOVE/GETUPVAL)
	info.icStableShape = pf.StableShape
	info.icStableIndex = pf.StableIndex
	info.icStableKey = stableKey
	// PJ5 Option B Spike 1/2 frame-build inlining (commit-5m/5p):
	// Spike 1: 0-arg setter form (callArgCount=0)
	// Spike 2: N-arg fixed setter form (callArgCount=0..7, per §9.20.3 table Spike 2)
	// The spec template already emits args byte-for-byte into R(callA+2..callA+1+N);
	// inside the helper, enterLuaFrame(funcIdx, 1+callArgCount, 0) is equivalent to
	// host.CallBaseline's CALL.B=2+N nargs decoding.
	if archSupportsFrameInline() && info.callArgCount <= 7 &&
		info.isCallVoid && !info.isTailCall {
		info.useFrameInline = true
	}
	return info, true
}

// isValidSpecCallRetCount checks whether the CALL.C field is within the range the
// spec template allows (per §9.19 PJ5 SELF spec-template full form coverage + Lua CALL C field semantics):
//
//   - cC=0: variable return count (C=0 = multi-ret), not recognized by the spec template (left for PJ5+)
//   - cC=1: 0 returns (void / setter, callee return values discarded)
//   - cC=2: 1 return (getter / single return value assigned to R(callA))
//   - cC=3..16: N=2..15 returns drop multi-ret (`local a,b,..=t:m()` kind, callee
//     return values land in R(callA..callA+N-1) bound directly as locals)
//
// **This function's scope**: applies to the `retB=1` caller form (0 return values,
// callee return values dropped multi-ret form). getter 1-return (cC=2 + retB=2)
// takes a separate branch (form5 a / form6 a / form7 Code[4]=CALL etc.).
//
// Upper bound 16 (N=15 returns) rationale: practical method bodies typically have
// N<=8 return values, but the Lua 5.1 CALL C field maxes at 255 (0..254 returns);
// a conservative N<=15 covers nearly all real-world business forms.
func isValidSpecCallRetCount(cC int) bool {
	return cC == 1 || (cC >= 3 && cC <= 16)
}

// assignArgsToShape fills the args array (N=2..7) into the corresponding shapeInfo fields.
func assignArgsToShape(info *shapeInfo, argsIsK []bool, argsK []uint64, argsReg []uint8) {
	n := len(argsIsK)
	if n >= 1 {
		info.callArg1IsK = argsIsK[0]
		info.callArg1K = argsK[0]
		info.callArg1RegSrc = argsReg[0]
	}
	if n >= 2 {
		info.callArg2IsK = argsIsK[1]
		info.callArg2K = argsK[1]
		info.callArg2RegSrc = argsReg[1]
	}
	if n >= 3 {
		info.callArg3IsK = argsIsK[2]
		info.callArg3K = argsK[2]
		info.callArg3RegSrc = argsReg[2]
	}
	if n >= 4 {
		info.callArg4IsK = argsIsK[3]
		info.callArg4K = argsK[3]
		info.callArg4RegSrc = argsReg[3]
	}
	if n >= 5 {
		info.callArg5IsK = argsIsK[4]
		info.callArg5K = argsK[4]
		info.callArg5RegSrc = argsReg[4]
	}
	if n >= 6 {
		info.callArg6IsK = argsIsK[5]
		info.callArg6K = argsK[5]
		info.callArg6RegSrc = argsReg[5]
	}
	if n >= 7 {
		info.callArg7IsK = argsIsK[6]
		info.callArg7K = argsK[6]
		info.callArg7RegSrc = argsReg[6]
	}
}

// analyzeSelfCallForm4 handles the length-4 form: 0-arg 0-return CALL void / 0-arg TAILCALL.
func analyzeSelfCallForm4(proto *bytecode.Proto, callA uint8, selfRK uint16,
	op0 bytecode.OpCode, op0B int) (shapeInfo, bool) {
	op2 := bytecode.Op(proto.Code[2])
	op3 := bytecode.Op(proto.Code[3])
	if op2 != bytecode.CALL && op2 != bytecode.TAILCALL {
		return shapeInfo{}, false
	}
	if op3 != bytecode.RETURN {
		return shapeInfo{}, false
	}
	cA := bytecode.A(proto.Code[2])
	cB := bytecode.B(proto.Code[2])
	cC := bytecode.C(proto.Code[2])
	if cA != int(callA) || cB != 2 {
		return shapeInfo{}, false
	}
	if op2 == bytecode.CALL {
		if cC == 1 {
			if bytecode.B(proto.Code[3]) != 1 {
				return shapeInfo{}, false
			}
			return shapeInfo{
				ok:              true,
				retA:            0,
				retB:            1,
				retPC:           3,
				preludeOp:       uint8(bytecode.CALL),
				preludeArg:      uint32(op0B),
				isCallVoid:      true,
				isCallUpval:     op0 == bytecode.GETUPVAL,
				callA:           callA,
				callB:           uint8(cB),
				callC:           uint8(cC),
				callArgCount:    0,
				isSelfCall:      true,
				selfCallA:       callA,
				selfMethodRK:    selfRK,
				selfRecvSrcReg:  uint8(op0B),
				selfRecvIsUpval: op0 == bytecode.GETUPVAL,
			}, true
		}
		// N>=2 return-value getter: cC=3 (N=2) / cC=4 (N=3), `local a,b = o:m()` kind.
		// luac emits [3]=RETURN B=1 (the main chunk's implicit RETURN wrap-up; the N>=2
		// return values already land in R(callA..callA+nret-1) bound directly as locals;
		// the P4 frame does not return these locals out, so retB=1 is the correct 0-return wrap-up).
		if isValidSpecCallRetCount(cC) && cC != 1 {
			if bytecode.B(proto.Code[3]) != 1 {
				return shapeInfo{}, false
			}
			return shapeInfo{
				ok:              true,
				retA:            0,
				retB:            1,
				retPC:           3,
				preludeOp:       uint8(bytecode.CALL),
				preludeArg:      uint32(op0B),
				isCallVoid:      true,
				isCallUpval:     op0 == bytecode.GETUPVAL,
				callA:           callA,
				callB:           uint8(cB),
				callC:           uint8(cC),
				callArgCount:    0,
				isSelfCall:      true,
				selfCallA:       callA,
				selfMethodRK:    selfRK,
				selfRecvSrcReg:  uint8(op0B),
				selfRecvIsUpval: op0 == bytecode.GETUPVAL,
			}, true
		}
		return shapeInfo{}, false
	}
	// TAILCALL
	if cC != 0 {
		return shapeInfo{}, false
	}
	retB := bytecode.B(proto.Code[3])
	if retB != 0 {
		return shapeInfo{}, false
	}
	return shapeInfo{
		ok:              true,
		retA:            uint8(bytecode.A(proto.Code[3])),
		retB:            uint8(retB),
		retPC:           3,
		preludeOp:       uint8(bytecode.TAILCALL),
		preludeArg:      uint32(op0B),
		isTailCall:      true,
		isCallUpval:     op0 == bytecode.GETUPVAL,
		callA:           callA,
		callB:           uint8(cB),
		callC:           uint8(cC),
		callArgCount:    0,
		isSelfCall:      true,
		selfCallA:       callA,
		selfMethodRK:    selfRK,
		selfRecvSrcReg:  uint8(op0B),
		selfRecvIsUpval: op0 == bytecode.GETUPVAL,
	}, true
}

// analyzeSelfCallForm5 handles the length-5 form: CALL getter 0-arg 1-return / CALL void 1 K/reg arg / TAILCALL 1 K/reg arg.
func analyzeSelfCallForm5(proto *bytecode.Proto, callA uint8, selfRK uint16,
	op0 bytecode.OpCode, op0B int) (shapeInfo, bool) {
	op2 := bytecode.Op(proto.Code[2])
	op3 := bytecode.Op(proto.Code[3])
	op4 := bytecode.Op(proto.Code[4])
	// (a) Code[2]=CALL → getter 0-arg 1-return
	if op2 == bytecode.CALL {
		cA := bytecode.A(proto.Code[2])
		cB := bytecode.B(proto.Code[2])
		cC := bytecode.C(proto.Code[2])
		if cA != int(callA) || cB != 2 || cC != 2 {
			return shapeInfo{}, false
		}
		if op3 != bytecode.RETURN || op4 != bytecode.RETURN {
			return shapeInfo{}, false
		}
		rA := bytecode.A(proto.Code[3])
		rB := bytecode.B(proto.Code[3])
		if rA != int(callA) || rB != 2 {
			return shapeInfo{}, false
		}
		if bytecode.B(proto.Code[4]) != 1 {
			return shapeInfo{}, false
		}
		return shapeInfo{
			ok:              true,
			retA:            uint8(rA),
			retB:            2,
			retPC:           3,
			preludeOp:       uint8(bytecode.CALL),
			preludeArg:      uint32(op0B),
			isCallVoid:      true,
			isCallUpval:     op0 == bytecode.GETUPVAL,
			callA:           callA,
			callB:           uint8(cB),
			callC:           uint8(cC),
			callArgCount:    0,
			isSelfCall:      true,
			selfCallA:       callA,
			selfMethodRK:    selfRK,
			selfRecvSrcReg:  uint8(op0B),
			selfRecvIsUpval: op0 == bytecode.GETUPVAL,
		}, true
	}
	// (a') Code[2]=TAILCALL 0-arg: [2] TAILCALL B=2 C=0, [3] RETURN A=callA B=0 (dead), [4] RETURN B=1 (implicit)
	if op2 == bytecode.TAILCALL {
		cA := bytecode.A(proto.Code[2])
		cB := bytecode.B(proto.Code[2])
		cC := bytecode.C(proto.Code[2])
		if cA != int(callA) || cB != 2 || cC != 0 {
			return shapeInfo{}, false
		}
		if op3 != bytecode.RETURN || op4 != bytecode.RETURN {
			return shapeInfo{}, false
		}
		if bytecode.B(proto.Code[3]) != 0 || bytecode.A(proto.Code[3]) != int(callA) {
			return shapeInfo{}, false
		}
		if bytecode.B(proto.Code[4]) != 1 {
			return shapeInfo{}, false
		}
		return shapeInfo{
			ok:              true,
			retA:            uint8(bytecode.A(proto.Code[3])),
			retB:            0,
			retPC:           3,
			preludeOp:       uint8(bytecode.TAILCALL),
			preludeArg:      uint32(op0B),
			isTailCall:      true,
			isCallUpval:     op0 == bytecode.GETUPVAL,
			callA:           callA,
			callB:           uint8(cB),
			callC:           uint8(cC),
			callArgCount:    0,
			isSelfCall:      true,
			selfCallA:       callA,
			selfMethodRK:    selfRK,
			selfRecvSrcReg:  uint8(op0B),
			selfRecvIsUpval: op0 == bytecode.GETUPVAL,
		}, true
	}
	// (b)(c): [2] = LOADK/MOVE  arg → R(callA+2)
	if op2 != bytecode.LOADK && op2 != bytecode.MOVE {
		return shapeInfo{}, false
	}
	var argIsK bool
	var argK uint64
	var argReg uint8
	if !decodeArgFromOp(proto, 2, int(callA)+2, &argIsK, &argK, &argReg) {
		return shapeInfo{}, false
	}
	if op3 != bytecode.CALL && op3 != bytecode.TAILCALL {
		return shapeInfo{}, false
	}
	cA := bytecode.A(proto.Code[3])
	cB := bytecode.B(proto.Code[3])
	cC := bytecode.C(proto.Code[3])
	if cA != int(callA) || cB != 3 {
		return shapeInfo{}, false
	}
	if op4 != bytecode.RETURN {
		return shapeInfo{}, false
	}
	retB := bytecode.B(proto.Code[4])
	if op3 == bytecode.CALL {
		if cC == 1 && retB == 1 {
			return shapeInfo{
				ok:              true,
				retA:            0,
				retB:            1,
				retPC:           4,
				preludeOp:       uint8(bytecode.CALL),
				preludeArg:      uint32(op0B),
				isCallVoid:      true,
				isCallUpval:     op0 == bytecode.GETUPVAL,
				callA:           callA,
				callB:           uint8(cB),
				callC:           uint8(cC),
				callArgCount:    1,
				callArg1IsK:     argIsK,
				callArg1K:       argK,
				callArg1RegSrc:  argReg,
				isSelfCall:      true,
				selfCallA:       callA,
				selfMethodRK:    selfRK,
				selfRecvSrcReg:  uint8(op0B),
				selfRecvIsUpval: op0 == bytecode.GETUPVAL,
			}, true
		}
		// N>=2 return-value getter with 1 K/reg arg: cC=3 (N=2) / cC=4 (N=3), `local a,b = o:m(K/R)` kind
		if (isValidSpecCallRetCount(cC) && cC != 1) && retB == 1 {
			return shapeInfo{
				ok:              true,
				retA:            0,
				retB:            1,
				retPC:           4,
				preludeOp:       uint8(bytecode.CALL),
				preludeArg:      uint32(op0B),
				isCallVoid:      true,
				isCallUpval:     op0 == bytecode.GETUPVAL,
				callA:           callA,
				callB:           uint8(cB),
				callC:           uint8(cC),
				callArgCount:    1,
				callArg1IsK:     argIsK,
				callArg1K:       argK,
				callArg1RegSrc:  argReg,
				isSelfCall:      true,
				selfCallA:       callA,
				selfMethodRK:    selfRK,
				selfRecvSrcReg:  uint8(op0B),
				selfRecvIsUpval: op0 == bytecode.GETUPVAL,
			}, true
		}
		return shapeInfo{}, false
	}
	// TAILCALL 1 arg
	if cC != 0 || retB != 0 {
		return shapeInfo{}, false
	}
	return shapeInfo{
		ok:              true,
		retA:            uint8(bytecode.A(proto.Code[4])),
		retB:            uint8(retB),
		retPC:           4,
		preludeOp:       uint8(bytecode.TAILCALL),
		preludeArg:      uint32(op0B),
		isTailCall:      true,
		isCallUpval:     op0 == bytecode.GETUPVAL,
		callA:           callA,
		callB:           uint8(cB),
		callC:           uint8(cC),
		callArgCount:    1,
		callArg1IsK:     argIsK,
		callArg1K:       argK,
		callArg1RegSrc:  argReg,
		isSelfCall:      true,
		selfCallA:       callA,
		selfMethodRK:    selfRK,
		selfRecvSrcReg:  uint8(op0B),
		selfRecvIsUpval: op0 == bytecode.GETUPVAL,
	}, true
}

// analyzeSelfCallForm6 handles the length-6 form: CALL getter 1 K/reg arg 1-return /
// CALL void 2 K/reg args / TAILCALL 2 K/reg args.
func analyzeSelfCallForm6(proto *bytecode.Proto, callA uint8, selfRK uint16,
	op0 bytecode.OpCode, op0B int) (shapeInfo, bool) {
	op2 := bytecode.Op(proto.Code[2])
	if op2 != bytecode.LOADK && op2 != bytecode.MOVE {
		return shapeInfo{}, false
	}
	op3 := bytecode.Op(proto.Code[3])
	// (a) Code[3]=CALL → getter 1-arg 1-return: [2] arg → R(callA+2), [3] CALL B=3 C=2, [4] RETURN A=callA B=2, [5] RETURN B=1
	if op3 == bytecode.CALL {
		var argIsK bool
		var argK uint64
		var argReg uint8
		if !decodeArgFromOp(proto, 2, int(callA)+2, &argIsK, &argK, &argReg) {
			return shapeInfo{}, false
		}
		cA := bytecode.A(proto.Code[3])
		cB := bytecode.B(proto.Code[3])
		cC := bytecode.C(proto.Code[3])
		if cA != int(callA) || cB != 3 || cC != 2 {
			return shapeInfo{}, false
		}
		if bytecode.Op(proto.Code[4]) != bytecode.RETURN ||
			bytecode.Op(proto.Code[5]) != bytecode.RETURN {
			return shapeInfo{}, false
		}
		rA := bytecode.A(proto.Code[4])
		rB := bytecode.B(proto.Code[4])
		if rA != int(callA) || rB != 2 || bytecode.B(proto.Code[5]) != 1 {
			return shapeInfo{}, false
		}
		return shapeInfo{
			ok:              true,
			retA:            uint8(rA),
			retB:            2,
			retPC:           4,
			preludeOp:       uint8(bytecode.CALL),
			preludeArg:      uint32(op0B),
			isCallVoid:      true,
			isCallUpval:     op0 == bytecode.GETUPVAL,
			callA:           callA,
			callB:           uint8(cB),
			callC:           uint8(cC),
			callArgCount:    1,
			callArg1IsK:     argIsK,
			callArg1K:       argK,
			callArg1RegSrc:  argReg,
			isSelfCall:      true,
			selfCallA:       callA,
			selfMethodRK:    selfRK,
			selfRecvSrcReg:  uint8(op0B),
			selfRecvIsUpval: op0 == bytecode.GETUPVAL,
		}, true
	}
	// (a') Code[3]=TAILCALL 1-arg: [2] arg → R(callA+2), [3] TAILCALL B=3 C=0,
	// [4] RETURN A=callA B=0 (dead), [5] RETURN B=1 (implicit)
	if op3 == bytecode.TAILCALL {
		var argIsK bool
		var argK uint64
		var argReg uint8
		if !decodeArgFromOp(proto, 2, int(callA)+2, &argIsK, &argK, &argReg) {
			return shapeInfo{}, false
		}
		cA := bytecode.A(proto.Code[3])
		cB := bytecode.B(proto.Code[3])
		cC := bytecode.C(proto.Code[3])
		if cA != int(callA) || cB != 3 || cC != 0 {
			return shapeInfo{}, false
		}
		if bytecode.Op(proto.Code[4]) != bytecode.RETURN ||
			bytecode.Op(proto.Code[5]) != bytecode.RETURN {
			return shapeInfo{}, false
		}
		if bytecode.A(proto.Code[4]) != int(callA) ||
			bytecode.B(proto.Code[4]) != 0 ||
			bytecode.B(proto.Code[5]) != 1 {
			return shapeInfo{}, false
		}
		return shapeInfo{
			ok:              true,
			retA:            uint8(bytecode.A(proto.Code[4])),
			retB:            0,
			retPC:           4,
			preludeOp:       uint8(bytecode.TAILCALL),
			preludeArg:      uint32(op0B),
			isTailCall:      true,
			isCallUpval:     op0 == bytecode.GETUPVAL,
			callA:           callA,
			callB:           uint8(cB),
			callC:           uint8(cC),
			callArgCount:    1,
			callArg1IsK:     argIsK,
			callArg1K:       argK,
			callArg1RegSrc:  argReg,
			isSelfCall:      true,
			selfCallA:       callA,
			selfMethodRK:    selfRK,
			selfRecvSrcReg:  uint8(op0B),
			selfRecvIsUpval: op0 == bytecode.GETUPVAL,
		}, true
	}
	// (b)(c): 2 K/reg args — [2][3] LOADK/MOVE
	if op3 != bytecode.LOADK && op3 != bytecode.MOVE {
		return shapeInfo{}, false
	}
	var arg1IsK bool
	var arg1K uint64
	var arg1Reg uint8
	var arg2IsK bool
	var arg2K uint64
	var arg2Reg uint8
	if !decodeArgFromOp(proto, 2, int(callA)+2, &arg1IsK, &arg1K, &arg1Reg) {
		return shapeInfo{}, false
	}
	if !decodeArgFromOp(proto, 3, int(callA)+3, &arg2IsK, &arg2K, &arg2Reg) {
		return shapeInfo{}, false
	}
	op4 := bytecode.Op(proto.Code[4])
	if op4 != bytecode.CALL && op4 != bytecode.TAILCALL {
		return shapeInfo{}, false
	}
	cA := bytecode.A(proto.Code[4])
	cB := bytecode.B(proto.Code[4])
	cC := bytecode.C(proto.Code[4])
	if cA != int(callA) || cB != 4 {
		return shapeInfo{}, false
	}
	if bytecode.Op(proto.Code[5]) != bytecode.RETURN {
		return shapeInfo{}, false
	}
	retB := bytecode.B(proto.Code[5])
	if op4 == bytecode.CALL {
		// cC=1 void (0 returns) / cC=3,4 N=2,3 returns drop multi-ret form (`local a,b=t:m(K,R)` kind)
		if !isValidSpecCallRetCount(cC) || retB != 1 {
			return shapeInfo{}, false
		}
		return shapeInfo{
			ok:              true,
			retA:            0,
			retB:            1,
			retPC:           5,
			preludeOp:       uint8(bytecode.CALL),
			preludeArg:      uint32(op0B),
			isCallVoid:      true,
			isCallUpval:     op0 == bytecode.GETUPVAL,
			callA:           callA,
			callB:           uint8(cB),
			callC:           uint8(cC),
			callArgCount:    2,
			callArg1IsK:     arg1IsK,
			callArg1K:       arg1K,
			callArg1RegSrc:  arg1Reg,
			callArg2IsK:     arg2IsK,
			callArg2K:       arg2K,
			callArg2RegSrc:  arg2Reg,
			isSelfCall:      true,
			selfCallA:       callA,
			selfMethodRK:    selfRK,
			selfRecvSrcReg:  uint8(op0B),
			selfRecvIsUpval: op0 == bytecode.GETUPVAL,
		}, true
	}
	// TAILCALL 2 args
	if cC != 0 || retB != 0 {
		return shapeInfo{}, false
	}
	return shapeInfo{
		ok:              true,
		retA:            uint8(bytecode.A(proto.Code[5])),
		retB:            uint8(retB),
		retPC:           5,
		preludeOp:       uint8(bytecode.TAILCALL),
		preludeArg:      uint32(op0B),
		isTailCall:      true,
		isCallUpval:     op0 == bytecode.GETUPVAL,
		callA:           callA,
		callB:           uint8(cB),
		callC:           uint8(cC),
		callArgCount:    2,
		callArg1IsK:     arg1IsK,
		callArg1K:       arg1K,
		callArg1RegSrc:  arg1Reg,
		callArg2IsK:     arg2IsK,
		callArg2K:       arg2K,
		callArg2RegSrc:  arg2Reg,
		isSelfCall:      true,
		selfCallA:       callA,
		selfMethodRK:    selfRK,
		selfRecvSrcReg:  uint8(op0B),
		selfRecvIsUpval: op0 == bytecode.GETUPVAL,
	}, true
}

// analyzeSelfCallForm7 handles the length-7 form (shared callee SELF + 3 ops):
//   - CALL void 3 args: [2..4] LOADK/MOVE, [5] CALL B=5 C=1, [6] RETURN B=1
//   - CALL getter 1-return 2 args: [2..3] LOADK/MOVE, [4] CALL B=4 C=2, [5] RETURN A=callA B=2, [6] RETURN B=1
//   - CALL N=2/3 returns 3 args drop multi-ret: [2..4] LOADK/MOVE, [5] CALL B=5 C=3/4, [6] RETURN B=1
//   - TAILCALL 2 args: [2..3] LOADK/MOVE, [4] TAILCALL B=4 C=0, [5] RETURN A=callA B=0, [6] RETURN B=1
func analyzeSelfCallForm7(proto *bytecode.Proto, callA uint8, selfRK uint16,
	op0 bytecode.OpCode, op0B int) (shapeInfo, bool) {
	// Discriminator: Code[4] = CALL → getter/N-return / Code[4] = TAILCALL → tail / Code[5] = CALL → void 3 args
	op4 := bytecode.Op(proto.Code[4])
	op5 := bytecode.Op(proto.Code[5])

	if op4 == bytecode.CALL || op4 == bytecode.TAILCALL {
		// 2-arg form (getter 1-return / TAILCALL): [2][3] = LOADK/MOVE, [4] = CALL/TAILCALL, [5] = RETURN
		if bytecode.Op(proto.Code[2]) != bytecode.LOADK && bytecode.Op(proto.Code[2]) != bytecode.MOVE {
			return shapeInfo{}, false
		}
		if bytecode.Op(proto.Code[3]) != bytecode.LOADK && bytecode.Op(proto.Code[3]) != bytecode.MOVE {
			return shapeInfo{}, false
		}
		var arg1IsK, arg2IsK bool
		var arg1K, arg2K uint64
		var arg1Reg, arg2Reg uint8
		if !decodeArgFromOp(proto, 2, int(callA)+2, &arg1IsK, &arg1K, &arg1Reg) {
			return shapeInfo{}, false
		}
		if !decodeArgFromOp(proto, 3, int(callA)+3, &arg2IsK, &arg2K, &arg2Reg) {
			return shapeInfo{}, false
		}
		cA := bytecode.A(proto.Code[4])
		cB := bytecode.B(proto.Code[4])
		cC := bytecode.C(proto.Code[4])
		if cA != int(callA) || cB != 4 {
			return shapeInfo{}, false
		}
		if op4 == bytecode.CALL {
			// CALL getter 2 args 1-return: cC=2, [5] RETURN A=callA B=2, [6] RETURN B=1
			if cC != 2 {
				return shapeInfo{}, false
			}
			if bytecode.Op(proto.Code[5]) != bytecode.RETURN ||
				bytecode.Op(proto.Code[6]) != bytecode.RETURN {
				return shapeInfo{}, false
			}
			if bytecode.A(proto.Code[5]) != int(callA) ||
				bytecode.B(proto.Code[5]) != 2 ||
				bytecode.B(proto.Code[6]) != 1 {
				return shapeInfo{}, false
			}
			return shapeInfo{
				ok:              true,
				retA:            uint8(bytecode.A(proto.Code[5])),
				retB:            2,
				retPC:           5,
				preludeOp:       uint8(bytecode.CALL),
				preludeArg:      uint32(op0B),
				isCallVoid:      true,
				isCallUpval:     op0 == bytecode.GETUPVAL,
				callA:           callA,
				callB:           uint8(cB),
				callC:           uint8(cC),
				callArgCount:    2,
				callArg1IsK:     arg1IsK,
				callArg1K:       arg1K,
				callArg1RegSrc:  arg1Reg,
				callArg2IsK:     arg2IsK,
				callArg2K:       arg2K,
				callArg2RegSrc:  arg2Reg,
				isSelfCall:      true,
				selfCallA:       callA,
				selfMethodRK:    selfRK,
				selfRecvSrcReg:  uint8(op0B),
				selfRecvIsUpval: op0 == bytecode.GETUPVAL,
			}, true
		}
		// TAILCALL 2 args: cC=0, [5] RETURN A=callA B=0, [6] RETURN B=1
		if cC != 0 {
			return shapeInfo{}, false
		}
		if bytecode.Op(proto.Code[5]) != bytecode.RETURN ||
			bytecode.Op(proto.Code[6]) != bytecode.RETURN {
			return shapeInfo{}, false
		}
		if bytecode.A(proto.Code[5]) != int(callA) ||
			bytecode.B(proto.Code[5]) != 0 ||
			bytecode.B(proto.Code[6]) != 1 {
			return shapeInfo{}, false
		}
		return shapeInfo{
			ok:              true,
			retA:            uint8(bytecode.A(proto.Code[5])),
			retB:            0,
			retPC:           5,
			preludeOp:       uint8(bytecode.TAILCALL),
			preludeArg:      uint32(op0B),
			isTailCall:      true,
			isCallUpval:     op0 == bytecode.GETUPVAL,
			callA:           callA,
			callB:           uint8(cB),
			callC:           uint8(cC),
			callArgCount:    2,
			callArg1IsK:     arg1IsK,
			callArg1K:       arg1K,
			callArg1RegSrc:  arg1Reg,
			callArg2IsK:     arg2IsK,
			callArg2K:       arg2K,
			callArg2RegSrc:  arg2Reg,
			isSelfCall:      true,
			selfCallA:       callA,
			selfMethodRK:    selfRK,
			selfRecvSrcReg:  uint8(op0B),
			selfRecvIsUpval: op0 == bytecode.GETUPVAL,
		}, true
	}

	// Code[5] = CALL → CALL void 3 args: [2..4] LOADK/MOVE, [5] CALL B=5 C=1, [6] RETURN B=1
	if op5 == bytecode.CALL {
		var arg1IsK, arg2IsK, arg3IsK bool
		var arg1K, arg2K, arg3K uint64
		var arg1Reg, arg2Reg, arg3Reg uint8
		if !decodeArgFromOp(proto, 2, int(callA)+2, &arg1IsK, &arg1K, &arg1Reg) {
			return shapeInfo{}, false
		}
		if !decodeArgFromOp(proto, 3, int(callA)+3, &arg2IsK, &arg2K, &arg2Reg) {
			return shapeInfo{}, false
		}
		if !decodeArgFromOp(proto, 4, int(callA)+4, &arg3IsK, &arg3K, &arg3Reg) {
			return shapeInfo{}, false
		}
		cA := bytecode.A(proto.Code[5])
		cB := bytecode.B(proto.Code[5])
		cC := bytecode.C(proto.Code[5])
		// cC=1 void (0 returns) / cC=3,4 N=2,3 returns drop multi-ret (`local a,b=t:m(K,K,K)` kind)
		if cA != int(callA) || cB != 5 || !isValidSpecCallRetCount(cC) {
			return shapeInfo{}, false
		}
		if bytecode.Op(proto.Code[6]) != bytecode.RETURN ||
			bytecode.B(proto.Code[6]) != 1 {
			return shapeInfo{}, false
		}
		return shapeInfo{
			ok:              true,
			retA:            0,
			retB:            1,
			retPC:           6,
			preludeOp:       uint8(bytecode.CALL),
			preludeArg:      uint32(op0B),
			isCallVoid:      true,
			isCallUpval:     op0 == bytecode.GETUPVAL,
			callA:           callA,
			callB:           uint8(cB),
			callC:           uint8(cC),
			callArgCount:    3,
			callArg1IsK:     arg1IsK,
			callArg1K:       arg1K,
			callArg1RegSrc:  arg1Reg,
			callArg2IsK:     arg2IsK,
			callArg2K:       arg2K,
			callArg2RegSrc:  arg2Reg,
			callArg3IsK:     arg3IsK,
			callArg3K:       arg3K,
			callArg3RegSrc:  arg3Reg,
			isSelfCall:      true,
			selfCallA:       callA,
			selfMethodRK:    selfRK,
			selfRecvSrcReg:  uint8(op0B),
			selfRecvIsUpval: op0 == bytecode.GETUPVAL,
		}, true
	}
	return shapeInfo{}, false
}

// analyzeSelfCallForm8 handles the length-8 form:
//   - CALL void 4 args: [2..5] LOADK/MOVE, [6] CALL B=6 C=1, [7] RETURN B=1
//   - CALL getter 3 args 1-return: [2..4] LOADK/MOVE, [5] CALL B=5 C=2, [6] RETURN A=callA B=2, [7] RETURN B=1
//   - CALL N=2/3 returns 4 args drop multi-ret: [2..5] LOADK/MOVE, [6] CALL B=6 C=3/4, [7] RETURN B=1
//   - TAILCALL 3 args: [2..4] LOADK/MOVE, [5] TAILCALL B=5 C=0, [6] RETURN A=callA B=0, [7] RETURN B=1
func analyzeSelfCallForm8(proto *bytecode.Proto, callA uint8, selfRK uint16,
	op0 bytecode.OpCode, op0B int) (shapeInfo, bool) {
	op5 := bytecode.Op(proto.Code[5])
	op6 := bytecode.Op(proto.Code[6])

	// Discriminator: Code[5]=CALL/TAILCALL → 3-arg getter/tail / Code[6]=CALL → 4-arg void
	if op5 == bytecode.CALL || op5 == bytecode.TAILCALL {
		var arg1IsK, arg2IsK, arg3IsK bool
		var arg1K, arg2K, arg3K uint64
		var arg1Reg, arg2Reg, arg3Reg uint8
		if !decodeArgFromOp(proto, 2, int(callA)+2, &arg1IsK, &arg1K, &arg1Reg) {
			return shapeInfo{}, false
		}
		if !decodeArgFromOp(proto, 3, int(callA)+3, &arg2IsK, &arg2K, &arg2Reg) {
			return shapeInfo{}, false
		}
		if !decodeArgFromOp(proto, 4, int(callA)+4, &arg3IsK, &arg3K, &arg3Reg) {
			return shapeInfo{}, false
		}
		cA := bytecode.A(proto.Code[5])
		cB := bytecode.B(proto.Code[5])
		cC := bytecode.C(proto.Code[5])
		if cA != int(callA) || cB != 5 {
			return shapeInfo{}, false
		}
		if op5 == bytecode.CALL {
			if cC != 2 {
				return shapeInfo{}, false
			}
			if bytecode.Op(proto.Code[6]) != bytecode.RETURN ||
				bytecode.Op(proto.Code[7]) != bytecode.RETURN ||
				bytecode.A(proto.Code[6]) != int(callA) ||
				bytecode.B(proto.Code[6]) != 2 ||
				bytecode.B(proto.Code[7]) != 1 {
				return shapeInfo{}, false
			}
			return shapeInfo{
				ok:              true,
				retA:            uint8(bytecode.A(proto.Code[6])),
				retB:            2,
				retPC:           6,
				preludeOp:       uint8(bytecode.CALL),
				preludeArg:      uint32(op0B),
				isCallVoid:      true,
				isCallUpval:     op0 == bytecode.GETUPVAL,
				callA:           callA,
				callB:           uint8(cB),
				callC:           uint8(cC),
				callArgCount:    3,
				callArg1IsK:     arg1IsK,
				callArg1K:       arg1K,
				callArg1RegSrc:  arg1Reg,
				callArg2IsK:     arg2IsK,
				callArg2K:       arg2K,
				callArg2RegSrc:  arg2Reg,
				callArg3IsK:     arg3IsK,
				callArg3K:       arg3K,
				callArg3RegSrc:  arg3Reg,
				isSelfCall:      true,
				selfCallA:       callA,
				selfMethodRK:    selfRK,
				selfRecvSrcReg:  uint8(op0B),
				selfRecvIsUpval: op0 == bytecode.GETUPVAL,
			}, true
		}
		// TAILCALL 3 args
		if cC != 0 {
			return shapeInfo{}, false
		}
		if bytecode.Op(proto.Code[6]) != bytecode.RETURN ||
			bytecode.Op(proto.Code[7]) != bytecode.RETURN ||
			bytecode.A(proto.Code[6]) != int(callA) ||
			bytecode.B(proto.Code[6]) != 0 ||
			bytecode.B(proto.Code[7]) != 1 {
			return shapeInfo{}, false
		}
		return shapeInfo{
			ok:              true,
			retA:            uint8(bytecode.A(proto.Code[6])),
			retB:            0,
			retPC:           6,
			preludeOp:       uint8(bytecode.TAILCALL),
			preludeArg:      uint32(op0B),
			isTailCall:      true,
			isCallUpval:     op0 == bytecode.GETUPVAL,
			callA:           callA,
			callB:           uint8(cB),
			callC:           uint8(cC),
			callArgCount:    3,
			callArg1IsK:     arg1IsK,
			callArg1K:       arg1K,
			callArg1RegSrc:  arg1Reg,
			callArg2IsK:     arg2IsK,
			callArg2K:       arg2K,
			callArg2RegSrc:  arg2Reg,
			callArg3IsK:     arg3IsK,
			callArg3K:       arg3K,
			callArg3RegSrc:  arg3Reg,
			isSelfCall:      true,
			selfCallA:       callA,
			selfMethodRK:    selfRK,
			selfRecvSrcReg:  uint8(op0B),
			selfRecvIsUpval: op0 == bytecode.GETUPVAL,
		}, true
	}
	// Code[6]=CALL → CALL void 4 args
	if op6 == bytecode.CALL {
		var arg1IsK, arg2IsK, arg3IsK, arg4IsK bool
		var arg1K, arg2K, arg3K, arg4K uint64
		var arg1Reg, arg2Reg, arg3Reg, arg4Reg uint8
		if !decodeArgFromOp(proto, 2, int(callA)+2, &arg1IsK, &arg1K, &arg1Reg) {
			return shapeInfo{}, false
		}
		if !decodeArgFromOp(proto, 3, int(callA)+3, &arg2IsK, &arg2K, &arg2Reg) {
			return shapeInfo{}, false
		}
		if !decodeArgFromOp(proto, 4, int(callA)+4, &arg3IsK, &arg3K, &arg3Reg) {
			return shapeInfo{}, false
		}
		if !decodeArgFromOp(proto, 5, int(callA)+5, &arg4IsK, &arg4K, &arg4Reg) {
			return shapeInfo{}, false
		}
		cA := bytecode.A(proto.Code[6])
		cB := bytecode.B(proto.Code[6])
		cC := bytecode.C(proto.Code[6])
		// cC=1 void (0 returns) / cC=3,4 N=2,3 returns drop multi-ret (`local a,b=t:m(K,K,K,K)` kind)
		if cA != int(callA) || cB != 6 || !isValidSpecCallRetCount(cC) {
			return shapeInfo{}, false
		}
		if bytecode.Op(proto.Code[7]) != bytecode.RETURN ||
			bytecode.B(proto.Code[7]) != 1 {
			return shapeInfo{}, false
		}
		return shapeInfo{
			ok:              true,
			retA:            0,
			retB:            1,
			retPC:           7,
			preludeOp:       uint8(bytecode.CALL),
			preludeArg:      uint32(op0B),
			isCallVoid:      true,
			isCallUpval:     op0 == bytecode.GETUPVAL,
			callA:           callA,
			callB:           uint8(cB),
			callC:           uint8(cC),
			callArgCount:    4,
			callArg1IsK:     arg1IsK,
			callArg1K:       arg1K,
			callArg1RegSrc:  arg1Reg,
			callArg2IsK:     arg2IsK,
			callArg2K:       arg2K,
			callArg2RegSrc:  arg2Reg,
			callArg3IsK:     arg3IsK,
			callArg3K:       arg3K,
			callArg3RegSrc:  arg3Reg,
			callArg4IsK:     arg4IsK,
			callArg4K:       arg4K,
			callArg4RegSrc:  arg4Reg,
			isSelfCall:      true,
			selfCallA:       callA,
			selfMethodRK:    selfRK,
			selfRecvSrcReg:  uint8(op0B),
			selfRecvIsUpval: op0 == bytecode.GETUPVAL,
		}, true
	}
	return shapeInfo{}, false
}

// analyzeSelfCallForm9 handles the length-9 form: CALL void 5 args / CALL getter 4 args 1-return /
// CALL N=2/3 returns 5 args drop multi-ret / TAILCALL 4 args.
func analyzeSelfCallForm9(proto *bytecode.Proto, callA uint8, selfRK uint16,
	op0 bytecode.OpCode, op0B int) (shapeInfo, bool) {
	op6 := bytecode.Op(proto.Code[6])
	op7 := bytecode.Op(proto.Code[7])

	if op6 == bytecode.CALL || op6 == bytecode.TAILCALL {
		var arg1IsK, arg2IsK, arg3IsK, arg4IsK bool
		var arg1K, arg2K, arg3K, arg4K uint64
		var arg1Reg, arg2Reg, arg3Reg, arg4Reg uint8
		if !decodeArgFromOp(proto, 2, int(callA)+2, &arg1IsK, &arg1K, &arg1Reg) {
			return shapeInfo{}, false
		}
		if !decodeArgFromOp(proto, 3, int(callA)+3, &arg2IsK, &arg2K, &arg2Reg) {
			return shapeInfo{}, false
		}
		if !decodeArgFromOp(proto, 4, int(callA)+4, &arg3IsK, &arg3K, &arg3Reg) {
			return shapeInfo{}, false
		}
		if !decodeArgFromOp(proto, 5, int(callA)+5, &arg4IsK, &arg4K, &arg4Reg) {
			return shapeInfo{}, false
		}
		cA := bytecode.A(proto.Code[6])
		cB := bytecode.B(proto.Code[6])
		cC := bytecode.C(proto.Code[6])
		if cA != int(callA) || cB != 6 {
			return shapeInfo{}, false
		}
		if op6 == bytecode.CALL {
			if cC != 2 {
				return shapeInfo{}, false
			}
			if bytecode.Op(proto.Code[7]) != bytecode.RETURN ||
				bytecode.Op(proto.Code[8]) != bytecode.RETURN ||
				bytecode.A(proto.Code[7]) != int(callA) ||
				bytecode.B(proto.Code[7]) != 2 ||
				bytecode.B(proto.Code[8]) != 1 {
				return shapeInfo{}, false
			}
			return shapeInfo{
				ok:              true,
				retA:            uint8(bytecode.A(proto.Code[7])),
				retB:            2,
				retPC:           7,
				preludeOp:       uint8(bytecode.CALL),
				preludeArg:      uint32(op0B),
				isCallVoid:      true,
				isCallUpval:     op0 == bytecode.GETUPVAL,
				callA:           callA,
				callB:           uint8(cB),
				callC:           uint8(cC),
				callArgCount:    4,
				callArg1IsK:     arg1IsK,
				callArg1K:       arg1K,
				callArg1RegSrc:  arg1Reg,
				callArg2IsK:     arg2IsK,
				callArg2K:       arg2K,
				callArg2RegSrc:  arg2Reg,
				callArg3IsK:     arg3IsK,
				callArg3K:       arg3K,
				callArg3RegSrc:  arg3Reg,
				callArg4IsK:     arg4IsK,
				callArg4K:       arg4K,
				callArg4RegSrc:  arg4Reg,
				isSelfCall:      true,
				selfCallA:       callA,
				selfMethodRK:    selfRK,
				selfRecvSrcReg:  uint8(op0B),
				selfRecvIsUpval: op0 == bytecode.GETUPVAL,
			}, true
		}
		// TAILCALL 4 args
		if cC != 0 {
			return shapeInfo{}, false
		}
		if bytecode.Op(proto.Code[7]) != bytecode.RETURN ||
			bytecode.Op(proto.Code[8]) != bytecode.RETURN ||
			bytecode.A(proto.Code[7]) != int(callA) ||
			bytecode.B(proto.Code[7]) != 0 ||
			bytecode.B(proto.Code[8]) != 1 {
			return shapeInfo{}, false
		}
		return shapeInfo{
			ok:              true,
			retA:            uint8(bytecode.A(proto.Code[7])),
			retB:            0,
			retPC:           7,
			preludeOp:       uint8(bytecode.TAILCALL),
			preludeArg:      uint32(op0B),
			isTailCall:      true,
			isCallUpval:     op0 == bytecode.GETUPVAL,
			callA:           callA,
			callB:           uint8(cB),
			callC:           uint8(cC),
			callArgCount:    4,
			callArg1IsK:     arg1IsK,
			callArg1K:       arg1K,
			callArg1RegSrc:  arg1Reg,
			callArg2IsK:     arg2IsK,
			callArg2K:       arg2K,
			callArg2RegSrc:  arg2Reg,
			callArg3IsK:     arg3IsK,
			callArg3K:       arg3K,
			callArg3RegSrc:  arg3Reg,
			callArg4IsK:     arg4IsK,
			callArg4K:       arg4K,
			callArg4RegSrc:  arg4Reg,
			isSelfCall:      true,
			selfCallA:       callA,
			selfMethodRK:    selfRK,
			selfRecvSrcReg:  uint8(op0B),
			selfRecvIsUpval: op0 == bytecode.GETUPVAL,
		}, true
	}
	// Code[7]=CALL → CALL void 5 args
	if op7 == bytecode.CALL {
		var arg1IsK, arg2IsK, arg3IsK, arg4IsK, arg5IsK bool
		var arg1K, arg2K, arg3K, arg4K, arg5K uint64
		var arg1Reg, arg2Reg, arg3Reg, arg4Reg, arg5Reg uint8
		if !decodeArgFromOp(proto, 2, int(callA)+2, &arg1IsK, &arg1K, &arg1Reg) {
			return shapeInfo{}, false
		}
		if !decodeArgFromOp(proto, 3, int(callA)+3, &arg2IsK, &arg2K, &arg2Reg) {
			return shapeInfo{}, false
		}
		if !decodeArgFromOp(proto, 4, int(callA)+4, &arg3IsK, &arg3K, &arg3Reg) {
			return shapeInfo{}, false
		}
		if !decodeArgFromOp(proto, 5, int(callA)+5, &arg4IsK, &arg4K, &arg4Reg) {
			return shapeInfo{}, false
		}
		if !decodeArgFromOp(proto, 6, int(callA)+6, &arg5IsK, &arg5K, &arg5Reg) {
			return shapeInfo{}, false
		}
		cA := bytecode.A(proto.Code[7])
		cB := bytecode.B(proto.Code[7])
		cC := bytecode.C(proto.Code[7])
		// cC=1 void (0 returns) / cC=3,4 N=2,3 returns drop multi-ret (`local a,b=t:m(K×5)` kind)
		if cA != int(callA) || cB != 7 || !isValidSpecCallRetCount(cC) {
			return shapeInfo{}, false
		}
		if bytecode.Op(proto.Code[8]) != bytecode.RETURN ||
			bytecode.B(proto.Code[8]) != 1 {
			return shapeInfo{}, false
		}
		return shapeInfo{
			ok:              true,
			retA:            0,
			retB:            1,
			retPC:           8,
			preludeOp:       uint8(bytecode.CALL),
			preludeArg:      uint32(op0B),
			isCallVoid:      true,
			isCallUpval:     op0 == bytecode.GETUPVAL,
			callA:           callA,
			callB:           uint8(cB),
			callC:           uint8(cC),
			callArgCount:    5,
			callArg1IsK:     arg1IsK,
			callArg1K:       arg1K,
			callArg1RegSrc:  arg1Reg,
			callArg2IsK:     arg2IsK,
			callArg2K:       arg2K,
			callArg2RegSrc:  arg2Reg,
			callArg3IsK:     arg3IsK,
			callArg3K:       arg3K,
			callArg3RegSrc:  arg3Reg,
			callArg4IsK:     arg4IsK,
			callArg4K:       arg4K,
			callArg4RegSrc:  arg4Reg,
			callArg5IsK:     arg5IsK,
			callArg5K:       arg5K,
			callArg5RegSrc:  arg5Reg,
			isSelfCall:      true,
			selfCallA:       callA,
			selfMethodRK:    selfRK,
			selfRecvSrcReg:  uint8(op0B),
			selfRecvIsUpval: op0 == bytecode.GETUPVAL,
		}, true
	}
	return shapeInfo{}, false
}

// analyzeShape recognizes the supported "single-value produce + RETURN A 1" forms.
//
// Supported forms:
//
//   - length 1: RETURN A 1/2 (0 or 1 return value) — R(A) is already the parameter/Nil slot
//   - length 2/3: LOADK/LOADBOOL/LOADNIL A ... + RETURN A 2 (constant return,
//     writeRetA=true)
//   - length 2/3: leading RETURN A 2 (luac-optimized form, R(A) is already the parameter)
//   - length 2/3: MOVE A B + RETURN A 2 (equivalent to RETURN B 2, retA=B skips the relay)
//   - length 2/3: GETUPVAL A B + RETURN A 2 (Go-side Run calls host.GetUpval +
//     SetReg, preludeOp=GETUPVAL)
//   - length 2/3: ADD/SUB/MUL/DIV/MOD/POW A B C + RETURN A 2 (Go-side Run calls
//     host.Arith, byte-for-byte isomorphic to the interpreter's doArith, preludeOp=arith op, can propagate ERR)
//   - length 2/3: UNM/LEN A B + RETURN A 2 (Go-side Run calls host.Unm/Len,
//     byte-for-byte isomorphic to the interpreter's UNM/LEN slow path, can propagate ERR)
//   - length 2/3: NEWTABLE A B C + RETURN A 2 (Go-side Run calls host.NewTable,
//     never raises — alloc + safepoint all within the helper)
//   - length 2/3: GETTABLE A B C + RETURN A 2 (Go-side Run calls host.GetTable,
//     via IC + hash + __index metamethod chain, can propagate ERR)
//   - **length 3/4 two-stage arithmetic chain**: arith1 A B C + arith2 A A C2 + RETURN A 2
//     (`function(x) return x*2+1 end` kind — MUL+ADD+RETURN). Run calls
//     host.Arith serially twice, with the intermediate value in R(A).
func analyzeShape(proto *bytecode.Proto) shapeInfo {
	if proto == nil {
		return shapeInfo{}
	}

	// Shape 0: length 1, RETURN A B (0 or 1 return value)
	if len(proto.Code) == 1 {
		ret := proto.Code[0]
		if bytecode.Op(ret) != bytecode.RETURN {
			return shapeInfo{}
		}
		retB := bytecode.B(ret)
		if retB != 1 && retB != 2 {
			return shapeInfo{}
		}
		return shapeInfo{ok: true, retA: uint8(bytecode.A(ret)), retB: uint8(retB), retPC: 0}
	}

	// PJ5 CALL void form (MOVE/GETUPVAL+CALL+RETURN void, length 3 or 4)
	// is tried before the length dispatch — supports both the 0-arg form (codeLen=3)
	// and the 1 K-arg form (codeLen=4).
	if cv, ok := analyzeCallVoidForm(proto); ok {
		return cv
	}

	// PJ5 TAILCALL form (MOVE/GETUPVAL+...+TAILCALL+dead RETURN B=0+RETURN B=1,
	// length 4/5/6) is tried before the length dispatch. Product of luac stmtReturn's single-CallExpr fast path.
	if tc, ok := analyzeTailCallForm(proto); ok {
		return tc
	}

	// PJ5 SELF method-call form (MOVE/GETUPVAL + SELF + ... + CALL/TAILCALL + RETURN,
	// length 4..6) is tried before the length dispatch. The luac compiled form of `obj:method(args)`.
	if sc, ok := analyzeSelfCallForm(proto); ok {
		return sc
	}

	// Shapes 1/2: length 2 or 3
	if len(proto.Code) != 2 && len(proto.Code) != 3 {
		// Length 5/6: may be a compare-fold shape EQ/LT/LE+JMP+LOADBOOL+LOADBOOL+RETURN(+RETURN)
		if cmp, ok := analyzeCompareForm(proto); ok {
			return cmp
		}
		// Length 3/4: may be a two-stage arithmetic chain shape (arith1 + arith2 + RETURN [+dead])
		if chain, ok := analyzeArithChainForm(proto); ok {
			return chain
		}
		// Length 6/7: may be the PJ3 empty-body all-constant FORLOOP shape
		if floop, ok := analyzeForLoopForm(proto); ok {
			return floop
		}
		// Length 8/9: may be the PJ3 FORLOOP shape whose body holds a reg-K op
		if floopBody, ok := analyzeForLoopBodyForm(proto); ok {
			return floopBody
		}
		// Length 9/10: may be the PJ3 FORLOOP body2 two-stage reg-K op shape
		if floopBody2, ok := analyzeForLoopBody2Form(proto); ok {
			return floopBody2
		}
		return shapeInfo{}
	}

	first := proto.Code[0]

	// When length is 3: the 3rd instruction must be RETURN (trailing redundancy)
	if len(proto.Code) == 3 {
		if bytecode.Op(proto.Code[2]) != bytecode.RETURN {
			return shapeInfo{}
		}
	}

	switch bytecode.Op(first) {
	case bytecode.RETURN:
		retA0 := bytecode.A(first)
		retB0 := bytecode.B(first)
		if retB0 != 1 && retB0 != 2 {
			return shapeInfo{}
		}
		return shapeInfo{ok: true, retA: uint8(retA0), retB: uint8(retB0), retPC: 0}

	case bytecode.MOVE:
		ret := proto.Code[1]
		if bytecode.Op(ret) != bytecode.RETURN {
			return shapeInfo{}
		}
		retA := bytecode.A(ret)
		retB := bytecode.B(ret)
		if retB != 2 {
			return shapeInfo{}
		}
		moveA := bytecode.A(first)
		moveB := bytecode.B(first)
		if moveA != retA {
			return shapeInfo{}
		}
		// B is a register number [0,254] (same defensive bound as the
		// GETTABLE/UNM/LEN register-number cases); luac's MAXSTACK cap of 250
		// never actually reaches this, so this is a purely defensive guard.
		if moveB > 254 {
			return shapeInfo{}
		}
		// Set retA to B (return R(B) directly), skipping the R(A) = R(B) relay
		return shapeInfo{ok: true, retA: uint8(moveB), retB: uint8(retB), retPC: 1}

	case bytecode.GETUPVAL:
		// GETUPVAL A B + RETURN A 2: Run calls host.GetUpval + SetReg.
		ret := proto.Code[1]
		if bytecode.Op(ret) != bytecode.RETURN {
			return shapeInfo{}
		}
		retA := bytecode.A(ret)
		retB := bytecode.B(ret)
		if retB != 2 {
			return shapeInfo{}
		}
		guvA := bytecode.A(first)
		guvB := bytecode.B(first)
		if guvA != retA {
			return shapeInfo{}
		}
		return shapeInfo{
			ok:         true,
			retA:       uint8(retA),
			retB:       uint8(retB),
			retPC:      1,
			preludeOp:  uint8(bytecode.GETUPVAL),
			preludeArg: uint32(guvB),
		}

	case bytecode.ADD, bytecode.SUB, bytecode.MUL, bytecode.DIV,
		bytecode.MOD, bytecode.POW:
		// ADD/SUB/MUL/DIV/MOD/POW A B C + RETURN A 2: Run calls the
		// host.Arith slow-path helper (byte-equal doArith, including the
		// fast-path recheck + slow-path coercion / metamethod, may raise).
		// This shape promotes the typical "pure binop + immediate return"
		// form (`function(x, y) return x + y end` /
		// `function(x) return x + 1 end`) into P4, mirroring P3's same
		// "translate through a helper" strategy.
		ret := proto.Code[1]
		if bytecode.Op(ret) != bytecode.RETURN {
			return shapeInfo{}
		}
		retA := bytecode.A(ret)
		retB := bytecode.B(ret)
		if retB != 2 {
			return shapeInfo{}
		}
		arithA := bytecode.A(first)
		arithB := bytecode.B(first)
		arithC := bytecode.C(first)
		if arithA != retA {
			return shapeInfo{}
		}
		// RK field ranges: B/C ∈ [0, 256) are register numbers,
		// [256, 256+len(Consts)) are constant indices (MaxK=256). The
		// register-number ceiling is 254 (luac max stack); the constant-index
		// ceiling depends on the proto. No extra validation is needed —
		// host.Arith reuses the interpreter's reg/RK parsing logic and the
		// helper reports its own error when out of range.
		if arithB > 511 || arithC > 511 { // defensive: max RK encoding 256+255=511
			return shapeInfo{}
		}
		return shapeInfo{
			ok:         true,
			retA:       uint8(retA),
			retB:       uint8(retB),
			retPC:      1,
			preludeOp:  uint8(bytecode.Op(first)),
			preludeArg: uint32(arithB),
			preludeC:   uint16(arithC),
		}

	case bytecode.UNM, bytecode.LEN:
		// UNM/LEN A B + RETURN A 2: the unary-op family, B is the source
		// register number (no RK encoding, read straight from a reg).
		//
		//   - UNM: Run calls host.Unm (byte-equal to the interpreter's UNM
		//     slow path, including string coercion + __unm metamethod, may
		//     raise);
		//   - LEN: Run calls host.Len (string byte length / table border /
		//     table __len / type-mismatch error, may raise).
		//
		// **NOT is handled in its own case** (`function(x) return not x end`
		// shape): see the `case bytecode.NOT` branch below — it goes through
		// host.GetReg(B) to read R(B) + SetReg(A, BoolValue(!Truthy(R(B)))).
		// Pure Truthy has no metamethod and cannot raise, so it is decoupled
		// from the UNM/LEN slow path and not merged into this case.
		ret := proto.Code[1]
		if bytecode.Op(ret) != bytecode.RETURN {
			return shapeInfo{}
		}
		retA := bytecode.A(ret)
		retB := bytecode.B(ret)
		if retB != 2 {
			return shapeInfo{}
		}
		uA := bytecode.A(first)
		uB := bytecode.B(first)
		if uA != retA {
			return shapeInfo{}
		}
		// UNM/LEN B is a register number, range [0, 254]
		if uB > 254 {
			return shapeInfo{}
		}
		return shapeInfo{
			ok:         true,
			retA:       uint8(retA),
			retB:       uint8(retB),
			retPC:      1,
			preludeOp:  uint8(bytecode.Op(first)),
			preludeArg: uint32(uB),
		}

	case bytecode.NEWTABLE:
		// NEWTABLE A B C + RETURN A 2: `function() return {} end` /
		// `function() return {1,2,3} end` (the single-NEWTABLE shape; the
		// latter also needs a SETLIST which is outside this simplified shape).
		// host.NewTable never raises (alloc + safepoint are all inside the
		// helper; only a Go runtime OOM crashes), unlike the arithmetic
		// family's may-raise path.
		ret := proto.Code[1]
		if bytecode.Op(ret) != bytecode.RETURN {
			return shapeInfo{}
		}
		retA := bytecode.A(ret)
		retB := bytecode.B(ret)
		if retB != 2 {
			return shapeInfo{}
		}
		ntA := bytecode.A(first)
		ntB := bytecode.B(first)
		ntC := bytecode.C(first)
		if ntA != retA {
			return shapeInfo{}
		}
		// NEWTABLE B/C are Fb-encoded initial-size hints, range [0, 255]
		if ntB > 255 || ntC > 255 {
			return shapeInfo{}
		}
		return shapeInfo{
			ok:         true,
			retA:       uint8(retA),
			retB:       uint8(retB),
			retPC:      1,
			preludeOp:  uint8(bytecode.NEWTABLE),
			preludeArg: uint32(ntB),
			preludeC:   uint16(ntC),
		}

	case bytecode.GETTABLE:
		// GETTABLE A B C + RETURN A 2: `function(t, k) return t[k] end` /
		// `function(t) return t[1] end` shape (C may be RK-encoded).
		// host.GetTable goes through IC + hash + __index metamethod chain,
		// may raise (attempt to index nil, etc.).
		ret := proto.Code[1]
		if bytecode.Op(ret) != bytecode.RETURN {
			return shapeInfo{}
		}
		retA := bytecode.A(ret)
		retB := bytecode.B(ret)
		if retB != 2 {
			return shapeInfo{}
		}
		gtA := bytecode.A(first)
		gtB := bytecode.B(first)
		gtC := bytecode.C(first)
		if gtA != retA {
			return shapeInfo{}
		}
		// B is a register number (the table object); C is RK-encoded (the
		// key), max value 511
		if gtB > 254 || gtC > 511 {
			return shapeInfo{}
		}
		return shapeInfo{
			ok:         true,
			retA:       uint8(retA),
			retB:       uint8(retB),
			retPC:      1,
			preludeOp:  uint8(bytecode.GETTABLE),
			preludeArg: uint32(gtB),
			preludeC:   uint16(gtC),
		}

	case bytecode.GETGLOBAL:
		// GETGLOBAL A Bx + RETURN A 2: `function() return print end` shape.
		// host.DoGetGlobal looks up Consts[bx] on `_G` via icGetTable, may
		// raise (metamethod path).
		ret := proto.Code[1]
		if bytecode.Op(ret) != bytecode.RETURN {
			return shapeInfo{}
		}
		retA := bytecode.A(ret)
		retB := bytecode.B(ret)
		if retB != 2 {
			return shapeInfo{}
		}
		ggA := bytecode.A(first)
		ggBx := bytecode.Bx(first)
		if ggA != retA {
			return shapeInfo{}
		}
		// Bx 18-bit, [0, 262143] — must be stored into preludeArg (uint32)
		if ggBx < 0 || ggBx > 262143 {
			return shapeInfo{}
		}
		if ggBx >= len(proto.Consts) {
			return shapeInfo{}
		}
		return shapeInfo{
			ok:         true,
			retA:       uint8(retA),
			retB:       uint8(retB),
			retPC:      1,
			preludeOp:  uint8(bytecode.GETGLOBAL),
			preludeArg: uint32(ggBx),
		}

	case bytecode.SETGLOBAL:
		// SETGLOBAL A Bx + RETURN A 1: setter shape (0 return values).
		// `function() x = 1 end` compiles to LOADK + SETGLOBAL + RETURN
		// (length 3), so recognizing SETGLOBAL as the prelude requires a
		// preceding LOADK to have already written R(A) — which violates the
		// "single prelude op + RETURN" simplified shape. **The SETGLOBAL
		// shape is not covered by the LOADK prelude and is not wired here** —
		// it needs a multi-prelude chain (LOADK + SETGLOBAL two ops + RETURN),
		// left for a later extension. Here we only handle the "source already
		// in R(A)" simplified shape (rare in practice), paired with the
		// retB=1 setter guard.
		ret := proto.Code[1]
		if bytecode.Op(ret) != bytecode.RETURN {
			return shapeInfo{}
		}
		retB := bytecode.B(ret)
		if retB != 1 { // setter must have 0 return values
			return shapeInfo{}
		}
		sgA := bytecode.A(first)
		sgBx := bytecode.Bx(first)
		if sgBx < 0 || sgBx > 262143 {
			return shapeInfo{}
		}
		if sgBx >= len(proto.Consts) {
			return shapeInfo{}
		}
		return shapeInfo{
			ok:         true,
			retA:       uint8(sgA),
			retB:       uint8(retB),
			retPC:      1,
			preludeOp:  uint8(bytecode.SETGLOBAL),
			preludeArg: uint32(sgBx),
		}

	case bytecode.SETTABLE:
		// SETTABLE A B C + RETURN A 1: `function(t,k,v) t[k]=v end` shape.
		// host.SetTable goes through icSetTable IC + hash + __newindex, may
		// raise.
		// **setter shape retB=1** (0 return values), does not write R(A).
		ret := proto.Code[1]
		if bytecode.Op(ret) != bytecode.RETURN {
			return shapeInfo{}
		}
		retB := bytecode.B(ret)
		if retB != 1 { // setter must have 0 return values
			return shapeInfo{}
		}
		stA := bytecode.A(first)
		stB := bytecode.B(first)
		stC := bytecode.C(first)
		// A is the table register number [0,254]; B/C are RK [0,511]
		if stA > 254 || stB > 511 || stC > 511 {
			return shapeInfo{}
		}
		return shapeInfo{
			ok:         true,
			retA:       uint8(stA),
			retB:       uint8(retB),
			retPC:      1,
			preludeOp:  uint8(bytecode.SETTABLE),
			preludeArg: uint32(stB),
			preludeC:   uint16(stC),
		}

	case bytecode.SETUPVAL:
		// SETUPVAL A B + RETURN A 1: `function(v) upval = v end` shape,
		// setter with 0 return values. host.SetUpvalFromReg reads the source
		// via reg(A) + writes the upvalue via upvalSet. Never raises.
		ret := proto.Code[1]
		if bytecode.Op(ret) != bytecode.RETURN {
			return shapeInfo{}
		}
		retB := bytecode.B(ret)
		if retB != 1 { // setter must have 0 return values
			return shapeInfo{}
		}
		suvA := bytecode.A(first)
		suvB := bytecode.B(first)
		// A is the source register [0,254]; B is the upvalue index [0,255]
		if suvA > 254 || suvB > 255 {
			return shapeInfo{}
		}
		return shapeInfo{
			ok:         true,
			retA:       uint8(suvA),
			retB:       uint8(retB),
			retPC:      1,
			preludeOp:  uint8(bytecode.SETUPVAL),
			preludeArg: uint32(suvB),
		}

	case bytecode.NOT:
		// NOT A B + RETURN A 2: `function(x) return not x end` shape.
		// Pure Truthy logic (no metamethod, no raise); Run goes directly
		// through host.GetReg to read R(B) + SetReg(A, BoolValue(!Truthy(...)))
		// to do the operation without calling a host helper (GetReg/SetReg go
		// through the host interface because the jit cannot access the arena
		// directly).
		ret := proto.Code[1]
		if bytecode.Op(ret) != bytecode.RETURN {
			return shapeInfo{}
		}
		retA := bytecode.A(ret)
		retB := bytecode.B(ret)
		if retB != 2 {
			return shapeInfo{}
		}
		notA := bytecode.A(first)
		notB := bytecode.B(first)
		if notA != retA {
			return shapeInfo{}
		}
		if notB > 254 {
			return shapeInfo{}
		}
		return shapeInfo{
			ok:         true,
			retA:       uint8(retA),
			retB:       uint8(retB),
			retPC:      1,
			preludeOp:  uint8(bytecode.NOT),
			preludeArg: uint32(notB),
		}

	case bytecode.LOADK, bytecode.LOADBOOL, bytecode.LOADNIL:
		ret := proto.Code[1]
		if bytecode.Op(ret) != bytecode.RETURN {
			return shapeInfo{}
		}
		retA := bytecode.A(ret)
		retB := bytecode.B(ret)
		if retB != 2 {
			return shapeInfo{}
		}

		switch bytecode.Op(first) {
		case bytecode.LOADK:
			loadA := bytecode.A(first)
			loadBx := bytecode.Bx(first)
			if loadA != retA {
				return shapeInfo{}
			}
			if loadBx < 0 || loadBx >= len(proto.Consts) {
				return shapeInfo{}
			}
			// LOADK string constant OK: `proto.Consts[bx]` on the State's
			// private Proto is already a NaN-box `MakeGC(TagString, intern_ref)`
			// (State.LoadProgram writes it via gc.Intern, see
			// state.go::LoadProgram §private Consts section).
			// **GC root liveness**: the string ref is registered via
			// `State.strRefs` (an R6 root) by LoadProgram and scanned into the
			// collector via visitProgramStringRefs; proto.Consts itself is
			// **not** traversed as a root, and p4Code holding the proto pointer
			// only keeps the proto indirectly alive, which is not the mechanism
			// that keeps the string ref alive. But the effect is the same in
			// practice (the strRefs LoadProgram registers share the proto's
			// lifetime), so the NaN-box u64 burned into the mmap is safe for the
			// duration the program is loaded.
			return shapeInfo{
				ok: true, retA: uint8(retA), retB: uint8(retB), retPC: 1,
				value: uint64(proto.Consts[loadBx]), writeRetA: true,
			}

		case bytecode.LOADBOOL:
			loadA := bytecode.A(first)
			loadB := bytecode.B(first)
			loadC := bytecode.C(first)
			if loadA != retA {
				return shapeInfo{}
			}
			if loadC != 0 {
				return shapeInfo{}
			}
			var v value.Value
			if loadB != 0 {
				v = value.BoolValue(true)
			} else {
				v = value.BoolValue(false)
			}
			return shapeInfo{
				ok: true, retA: uint8(retA), retB: uint8(retB), retPC: 1,
				value: uint64(v), writeRetA: true,
			}

		case bytecode.LOADNIL:
			loadA := bytecode.A(first)
			loadB := bytecode.B(first)
			if loadA != retA || loadA != loadB {
				return shapeInfo{}
			}
			return shapeInfo{
				ok: true, retA: uint8(retA), retB: uint8(retB), retPC: 1,
				value: uint64(value.Nil), writeRetA: true,
			}
		}
	}
	return shapeInfo{}
}

// Compile compiles a Proto into GibbousCode (the executable artifact).
//
// **PJ7 wired implementation**: recognizes the single-BB shapes analyzeShape
// supports (getter/setter/comparison-fold, ~25 kinds — see the analyzeShape
// godoc for the full list + the ErrCompileUnsupportedShape single-line list):
//  1. compute retA/retB/preludeOp/value/cmpA/... via analyzeShape;
//  2. the emitter emits `mov rax, value; ret` (11 bytes; the constant family
//     burns the NaN-box, while for the prelude / comparison-fold family RAX is
//     a dummy ignored by the Run side);
//  3. mmap PROT_RW + write code + mprotect PROT_RX (per 05 §2.1);
//  4. wrap a *p4Code (retA + each prelude field + host = a copy of c.hostState).
//
// Other shapes return ErrCompileUnsupportedShape (per
// `p2-bridge/05-p3-p4-interface.md` §2.2.2 error-return semantics) — once the
// bridge receives the error it marks that Proto TierStuck (interpret
// permanently, no retry).
func (c *Compiler) Compile(proto *bytecode.Proto, feedback *bridge.TypeFeedback) (bridge.GibbousCode, error) {
	// PJ10 native path preferred for Protos where shape-spec fast paths
	// don't help: multi-BB reducible CFG AND at least one live BB with
	// >=4 opcodes (heavy_arith / heavy_floatloop kernel shapes). Single-
	// BB Protos and small-body loops stay on the historical shape-spec
	// fast paths — those are tuned specifically for them and diverting
	// them to native would break pre-existing tests that assert which
	// spec fast path fires. See PreferNative's godoc for the heuristic.
	if perOpNativeAnalyzer != nil && perOpNativeAnalyzer(proto) && perOpTranslator != nil {
		if code, err := perOpTranslator(proto, c.hostState); err == nil && code != nil {
			CompilePreferNativeCount.Add(1)
			return code, nil
		}
	}
	// **PJ4 IC ArrayHit recognized first** (per 03 §6 stableShape/Index
	// direct slot).
	//
	// **Must try the IC inline first (before analyzeShape)**: the IC shape of
	// length 2/3 byte-overlaps completely with analyzeShape's GETTABLE host
	// helper shape (GETTABLE+RETURN A 2) — if the IC is not recognized first,
	// analyzeShape's GETTABLE case matches immediately and routes this proto
	// to the host.GetTable slow path (byte-equal to the P1 interpreter, but
	// without byte-level direct-slot acceleration).
	//
	// The IC trigger conditions are 4x stricter than the GETTABLE host helper:
	//   - proto.IC[0].Kind = ArrayHit (the P1 interpreter observed an array
	//     hit, not None / NodeHit / MonoMeta)
	//   - feedback.Points[0].Kind = FBTableMono (P2 aggregation confirmed mono)
	//   - feedback.Points[0].Confidence >= 0.99 (speculation threshold)
	//   - feedback / proto.IC stableShape & stableIndex agree
	//   - C field >= 256 (K constant index, not a dynamic reg)
	//
	// If any fails → analyzeGetTableArrayHit returns false → fall through to
	// analyzeShape, and the GETTABLE case routes to the host.GetTable
	// byte-equal slow path (correctness fallback).
	//
	// Doc reference: [[03-speculation-ic.md]] §6 ArrayHit direct slot + this
	// repo's
	// gibbous_pj4_table_e2e_test.go::TestPJ4_TableArrayHit_E2E_WarmupThenForce
	// proves the IC inline path grows SpecTableHits.
	if archSupportsSpec() && c.hostState != nil && c.hostState.ArenaBaseAddr() != 0 {
		if icInfo, ok := analyzeGetTableArrayHit(proto, feedback); ok {
			return c.compileIcArrayHit(proto, icInfo)
		}
		// **PJ4 IC NodeHit shape**: hash-section direct slot (`t["x"]` shape),
		// reusing the same IC + feedback double-check as ArrayHit; the
		// difference is IC[0].Kind=NodeHit + freezing stableKey from
		// proto.Consts at compile time. The NodeHit template is 159 bytes (27
		// bytes more than ArrayHit for the key-compare section); on a hit it
		// byte-inlines, on failure it falls through to host.GetTable byte-equal
		// P1.
		if icInfo, ok := analyzeGetTableNodeHit(proto, feedback); ok {
			return c.compileIcNodeHit(proto, icInfo)
		}
		// **PJ4 SETTABLE IC ArrayHit shape**: `function(t,v) t[K] = v end`
		// (setter, numeric key in the array section), 113-byte template
		// reverse-writes array[stableIndex] = R(C); on failure falls through to
		// host.SetTable byte-equal (through the __newindex metamethod chain).
		if icInfo, ok := analyzeSetTableArrayHit(proto, feedback); ok {
			return c.compileIcSetArrayHit(proto, icInfo)
		}
		// **PJ4 SELF IC ArrayHit shape**: `local m = obj:method` and other
		// SELF + RETURN shapes (rare but valid), 139-byte template:
		// R(A+1) := R(B) + array[stableIndex] load → R(A); on failure falls
		// through to host.GetTable byte-equal (R(A+1) already stored, byte-equal
		// to the same steps in the P1 SELF case).
		if icInfo, ok := analyzeSelfArrayHit(proto, feedback); ok {
			return c.compileIcSelfArrayHit(proto, icInfo)
		}
		// **PJ4 SETTABLE IC NodeHit shape**: `function(t,v) t["x"] = v end`
		// (setter, string / arbitrary key in the hash section), 140-byte
		// template reverse-writes node[stableIndex].val = R(C); on failure
		// falls through to host.SetTable byte-equal (through icSetTable +
		// __newindex metamethod chain).
		if icInfo, ok := analyzeSetTableNodeHit(proto, feedback); ok {
			return c.compileIcSetNodeHit(proto, icInfo)
		}
		// **PJ4 SELF IC NodeHit shape**: `local m = obj:method` and other
		// SELF+RETURN shapes where the method is a string ident → hits the hash
		// section (the typical real-world obj:method() shape), 166-byte
		// template: R(A+1) := R(B) + NodeKey compare + NodeVal load → R(A); on
		// failure falls through to host.GetTable byte-equal (R(A+1) already
		// stored, same steps as the P1 SELF case).
		if icInfo, ok := analyzeSelfNodeHit(proto, feedback); ok {
			return c.compileIcSelfNodeHit(proto, icInfo)
		}
		// **PJ5 SELF + CALL spec template shape** (per §9.10 reuse + §9.17
		// upgrade): `function(o) o:m() end` real OOP call (SELF + CALL + RETURN
		// void); on an IC NodeHit the SELF section takes the byte-level
		// EmitSelfNodeHit (skipping host.Self), while the CALL section still
		// takes host.CallBaseline; on failure it deopts down to host.Self. One
		// CALL step more than PJ4 SELF NodeHit, hence a separate Compile path.
		if icInfo, ok := analyzeSelfCallSpecForm(proto, feedback); ok {
			return c.compileSpecSelfCall(proto, icInfo)
		}
	}

	info := analyzeShape(proto)
	if !info.ok {
		// PJ10 per-op translator fall-through: hand the Proto to the
		// peroptranslator sub-package if its hook is registered. Same
		// nil-safe gating as SupportsAllOpcodes — when the sub-package
		// isn't imported, the hook stays nil and we fall straight to the
		// historical PJ7 "unsupported" return.
		if perOpTranslator != nil {
			if code, err := perOpTranslator(proto, c.hostState); err == nil && code != nil {
				return code, nil
			}
		}
		return nil, ErrCompileUnsupportedShape
	}

	// **PJ3 FORLOOP byte-level inline wired** (per 05 §6.3 + 06 §3.3):
	// the all-constant init/limit/step + empty-body FORLOOP shape
	// (`for i=1,K do end`) takes the byte-level FORLOOP template — a 69-byte
	// mmap+RX in-segment self-loop with the complete in-segment idx+=step
	// + ucomisd limit + backward jmp, no external side effects; the empty body
	// need not write R(A)..
	//
	// **mock host fallback**: same as the PJ2 path, degrade when
	// host.ArenaBaseAddr=0 — but the empty-body FORLOOP has no addressing at
	// all (the template does not read rbx), so the mock path can also enable
	// it. For a uniform wiring contract, still handle it with the same mock
	// host guard as PJ2.
	//
	// **arch gate**: use `archSupportsForLoop()` rather than
	// `archSupportsSpec()`, because FORLOOP goes through the `archCallJITFull`
	// main path (not the spec trampoline); the arm64 archSupportsSpec=false
	// should not block the FORLOOP arm64 emitter call (after this session
	// turned the stub into a wired implementation of all four PJ3 shapes, the
	// arm64 archSupportsForLoop can already return true and enable it).
	if info.isForLoop && archSupportsForLoop() &&
		c.hostState != nil && c.hostState.ArenaBaseAddr() != 0 {
		var buf []byte
		// safepoint check wired — the preemptFlag field offset is passed to
		// the template, which inserts at the end of the loop body
		// `cmp byte [r15+pfOff], 0; jne after_loop`
		// (per 05 §1.2.2 preemption discipline + V18 -race); the trampoline
		// has already loaded r15.
		pfOff := int32(JITContextPreemptFlagOffset)

		// **hasBody2 = true: two-stage body shape** (`local s; for i=K1,K2 do
		// s = s op1 K3; s = s op2 K4 end; return s`): a 154-byte template
		// reuses xmm3 across both SSE ops, saving one load/store. Decided
		// before the hasBody single-op path (because hasBody2 is an extension
		// of hasBody).
		//
		// **spec trampoline guard**: the body/body2/RegLimit three paths need
		// the spec trampoline to load vsBase into a callee-saved register
		// (amd64 rbx / arm64 x26) to address the value stack R(aS). The arm64
		// callJITFull trampoline does not load x26 → it must go through
		// archCallJITSpec; when `archSupportsSpec()=false` (arm64 currently) it
		// returns ErrCompileUnsupportedShape directly, letting the Tier
		// framework fall back to the interpreter, **avoiding a fallthrough to
		// LoadKReturn that would silently produce a wrong result** (per the
		// real bug lesson from the previous review round).
		if (info.hasBody2 || info.hasBody || info.forLimitIsReg) && !archSupportsSpec() {
			return nil, ErrCompileUnsupportedShape
		}

		if info.hasBody2 {
			buf = archEmitForLoopWithBody2(buf, info.forBodyKS, info.forInitK,
				info.forLimitK, info.forStepK,
				info.bodyKValue, info.bodyKValue2,
				info.forBodyAS, info.bodyOp, info.bodyOp2, pfOff)
			page, err := archMmapCode(buf)
			if err != nil {
				return nil, err
			}
			incSpecForLoopHits()
			return &p4Code{
				proto:         proto,
				codePage:      page,
				jitCtx:        NewJITContext(),
				retA:          info.retA,
				retB:          info.retB,
				retPC:         info.retPC,
				writeRetA:     false,
				host:          c.hostState,
				useSpec:       true,
				specDeoptCode: 0xFFFCDEAD_DEADFFFF,
			}, nil
		}

		// **hasBody = true: body contains a reg-K op shape** (`local s=K; for
		// i=K1,K2 do s = s op K3 end; return s`). A 135-byte template:
		// init R(aS)=K_s + FORLOOP setup + body inline (load s / mov K_body /
		// sseOp / store s) + safepoint + backward jmp + ret. **writeRetA=false**
		// (the body has already written R(aS)= s via movsd [rbx+aS*8] xmm3;
		// host.DoReturn reads it back to return).
		if info.hasBody {
			buf = archEmitForLoopWithBody(buf, info.forBodyKS, info.forInitK,
				info.forLimitK, info.forStepK, info.bodyKValue,
				info.forBodyAS, info.bodyOp, pfOff)
			page, err := archMmapCode(buf)
			if err != nil {
				return nil, err
			}
			incSpecForLoopHits()
			return &p4Code{
				proto:     proto,
				codePage:  page,
				jitCtx:    NewJITContext(),
				retA:      info.retA,
				retB:      info.retB,
				retPC:     info.retPC,
				writeRetA: false,
				host:      c.hostState,
				// use callJITSpec (loads rbx+r15); the template needs rbx to
				// address R(aS)
				useSpec: true,
				// **no deopt**: this simplest body shape has no guard, so
				// specDeoptCode uses a "never-collides" value; Run detects
				// raxSpec != deoptCode and takes the normal path directly.
				specDeoptCode: 0xFFFCDEAD_DEADFFFF,
			}, nil
		}

		if info.forLimitIsReg {
			// **reg-limit hot path wired** (`for i=1,n do end`): a 117-byte
			// template with an IsNumber guard + a float loop + safepoint + a
			// deopt block. useSpec=true takes callJITSpec (loads rbx=vsBase +
			// r15=jitCtx). The deopt path calls host.ForPrep raise ('for' limit
			// must be a number) byte-equal with the interpreter.
			//
			// **upvalue-limit sub-shape**: when forLimitUpvalIdx>0, the Run side
			// first calls host.GetUpval(idx-1) + host.SetReg(forLimitReg, val) to
			// write the upvalue into the R(forLimitReg) slot the reg-limit
			// template expects, then takes the reg-limit byte-level template
			// (guard + loop).
			const deoptCode uint64 = 0xFFFCDEAD_DEADBE00
			buf = archEmitForLoopRegLimit(buf, info.forInitK, info.forStepK,
				info.forLimitReg, deoptCode, pfOff)
			page, err := archMmapCode(buf)
			if err != nil {
				return nil, err
			}
			incSpecForLoopHits()
			return &p4Code{
				proto:           proto,
				codePage:        page,
				jitCtx:          NewJITContext(),
				retA:            info.retA,
				retB:            info.retB,
				retPC:           info.retPC,
				writeRetA:       false,
				preludeOp:       0, // does not go through the prelude switch
				host:            c.hostState,
				useSpec:         true,
				specDeoptCode:   deoptCode,
				forLoopDeopt:    true,
				forLoopA:        info.forA,
				forLoopLimitReg: info.forLimitReg,
				forLoopUpvalIdx: info.forLimitUpvalIdx,
			}, nil
		}

		// all-constant empty-body FORLOOP (landed in this batch)
		buf = archEmitForLoopEmptyConst(buf, info.forInitK, info.forLimitK, info.forStepK, pfOff)
		page, err := archMmapCode(buf)
		if err != nil {
			return nil, err
		}
		incSpecForLoopHits() // prove-the-path white-box hit evidence
		return &p4Code{
			proto:    proto,
			codePage: page,
			jitCtx:   NewJITContext(),
			retA:     info.retA,
			retB:     info.retB, // 1 = empty return
			retPC:    info.retPC,
			// the empty-body FORLOOP writes no R(A) slot; writeRetA=false +
			// preludeOp=0 → the Run path skips the prelude switch, does not
			// write RAX, and only calls DoReturn to pop the frame
			writeRetA: false,
			host:      c.hostState,
			// useSpec=false takes archCallJITFull (in-segment self-loop; the
			// full trampoline loads r15, which is unnecessary but OK — the
			// template does not read r15)
			useSpec: false,
		}, nil
	}

	// **PJ2 speculative arithmetic template wired** (per 03-speculation-ic.md
	// §2 IsNumber×2): emit the speculative template if and only if this arch is
	// supported (amd64) + the ADD/SUB/MUL/DIV A B C + RETURN A 2 shape + a real
	// host (not mock, ArenaBaseAddr != 0).
	//
	// Operand shape split (per ../bytecode/instruction.go RK encoding):
	//   - **reg-reg** (B/C ≤ 254 are both registers): 92-byte template,
	//     IsNumber guard×2 + a two-number fast path (movsd+<sseOp>+movsd+ret) +
	//     deopt block;
	//   - **reg-K** (B ≤ 254 reg + C ≥ 256 is a constant index, K[c-256] must be
	//     a number): 73-byte template, a single guard on the reg side + burn the
	//     K value as imm64 + fast path + deopt block; the K side is verified as a
	//     number at compile time and is no longer guarded at run time.
	// When Run detects the segment returns RAX == specDeoptCode it degrades to
	// host.Arith slow path (byte-equal with the interpreter). This PJ2 wiring is
	// the byte-level physical foundation of the PJ11 luajc stage.
	//
	// **Speculation scope** (per 03 §2 single-instruction IEEE 754 SSE):
	//   - ✅ ADD / SUB / MUL / DIV: a single SSE binop (F2 0F 58/5C/59/5E C1)
	//   - ❌ MOD: Lua floor-mod semantics (a - floor(a/b)*b) are not a single
	//     SSE, needing fpsub + sse round + sse sub three instructions, left to PJ3+
	//   - ❌ POW: goes through the math.Pow helper (C runtime), not a single-SSE
	//     path
	// Arithmetic ops outside the whitelist take the host helper slow path
	// (byte-equal with the interpreter).
	//
	// **mock host fallback**: at Compile time c.hostState.ArenaBaseAddr()
	// returns 0 (the jit-package unit-test mock has no real arena) → do not
	// enable spec (avoids a segment read [rbx+0]=read 0 SIGSEGV). On a real
	// crescent.State, ArenaBaseAddr is non-zero after LoadProgram, enabling spec.
	useSpec := false
	useSpecRegK := false
	useSpecChain := false
	var specSseOp byte
	var specSseOp2 byte
	var regKValue uint64
	var chainK1Value, chainK2Value uint64
	if archSupportsSpec() && info.chainOp == 0 &&
		c.hostState != nil && c.hostState.ArenaBaseAddr() != 0 {
		if op, ok := archSseOpForArith(info.preludeOp); ok {
			specSseOp = op
			// reg-reg shape: B/C both ≤ 254
			if info.preludeArg <= 254 && info.preludeC <= 254 {
				useSpec = true
			} else if info.preludeArg <= 254 && info.preludeC >= 256 &&
				int(info.preludeC-256) < len(proto.Consts) {
				// reg-K shape: B is a reg, C is a constant index; K must be a
				// number (otherwise degrade to host — the speculative template
				// only supports number constants; string/bool/table etc. need
				// doArith coercion logic)
				kIdx := int(info.preludeC - 256)
				kVal := proto.Consts[kIdx]
				if value.IsNumber(kVal) {
					useSpecRegK = true
					regKValue = uint64(kVal)
				}
			}
		}
	}
	// **chain reg-K-K**: `R(A) = R(B) op1 K1 op2 K2` (luac compiles `x*2+1`
	// and similar). chainB is already fixed = retA by analyzeArithChainForm
	// (the intermediate-value link); preludeArg is op1.B = the original reg.
	if archSupportsSpec() && info.chainOp != 0 &&
		c.hostState != nil && c.hostState.ArenaBaseAddr() != 0 {
		op1, ok1 := archSseOpForArith(info.preludeOp)
		op2, ok2 := archSseOpForArith(info.chainOp)
		if ok1 && ok2 && info.preludeArg <= 254 &&
			info.preludeC >= 256 && info.chainC >= 256 &&
			int(info.preludeC-256) < len(proto.Consts) &&
			int(info.chainC-256) < len(proto.Consts) {
			k1Val := proto.Consts[info.preludeC-256]
			k2Val := proto.Consts[info.chainC-256]
			if value.IsNumber(k1Val) && value.IsNumber(k2Val) {
				useSpecChain = true
				specSseOp = op1
				specSseOp2 = op2
				chainK1Value = uint64(k1Val)
				chainK2Value = uint64(k2Val)
			}
		}
	}

	var buf []byte
	if useSpec {
		// 92-byte speculative template. deoptCode picks a special value in the
		// high NaN-box range that no legal Lua value can collide with
		// (0xFFFC_DEAD_DEADBE00 = a mock deopt marker).
		const deoptCode uint64 = 0xFFFCDEAD_DEADBE00
		buf = archEmitArithSpecBinopWithGuard(buf, specSseOp, info.retA,
			uint8(info.preludeArg), uint8(info.preludeC), deoptCode)
		page, err := archMmapCode(buf)
		if err != nil {
			return nil, err
		}
		incSpecRegRegHits() // prove-the-path white-box hit evidence
		return &p4Code{
			proto:         proto,
			codePage:      page,
			jitCtx:        NewJITContext(),
			retA:          info.retA,
			retB:          info.retB,
			retPC:         info.retPC,
			writeRetA:     info.writeRetA,
			preludeOp:     info.preludeOp,
			preludeArg:    info.preludeArg,
			preludeC:      info.preludeC,
			cmpA:          info.cmpA,
			chainOp:       info.chainOp,
			chainB:        info.chainB,
			chainC:        info.chainC,
			host:          c.hostState,
			useSpec:       true,
			specDeoptCode: deoptCode,
		}, nil
	}
	if useSpecRegK {
		// 73-byte reg-K speculation template: single guard on B (reg) + K
		// burned in as imm64 emitted directly into the segment + SSE binop +
		// write-back + deopt block.
		const deoptCode uint64 = 0xFFFCDEAD_DEADBE00
		buf = archEmitArithSpecBinopRegKWithGuard(buf, specSseOp, info.retA,
			uint8(info.preludeArg), regKValue, deoptCode)
		page, err := archMmapCode(buf)
		if err != nil {
			return nil, err
		}
		incSpecRegKHits() // prove-the-path white-box hit evidence
		return &p4Code{
			proto:         proto,
			codePage:      page,
			jitCtx:        NewJITContext(),
			retA:          info.retA,
			retB:          info.retB,
			retPC:         info.retPC,
			writeRetA:     info.writeRetA,
			preludeOp:     info.preludeOp,
			preludeArg:    info.preludeArg,
			preludeC:      info.preludeC,
			cmpA:          info.cmpA,
			chainOp:       info.chainOp,
			chainB:        info.chainB,
			chainC:        info.chainC,
			host:          c.hostState,
			useSpec:       true,
			specDeoptCode: deoptCode,
		}, nil
	}
	if useSpecChain {
		// 92-byte chain template: single guard on reg-B + K1/K2 burned in as
		// imm64 + two SSE binops chained through xmm0 + write-back + deopt
		// block. One mmap-segment call performs both arithmetic ops, saving one
		// boundary + reg-stack round trip.
		//
		// **chainOp preserved**: on the Run-side deopt path, host.Arith must be
		// called twice serially (op1 + op2) to stay byte-equal with the
		// interpreter. The compiler must not clear chainOp, otherwise the deopt
		// fallback runs only op1 = wrong result (the chain template's success
		// path does not read chainOp; the deopt path reads chainOp to make the
		// two slow calls). writeRetA=false because the mmap segment has already
		// written R(A) via movsd [rbx+A*8] xmm0.
		const deoptCode uint64 = 0xFFFCDEAD_DEADBE00
		buf = archEmitArithSpecChainKKWithGuard(buf, specSseOp, specSseOp2,
			info.retA, uint8(info.preludeArg),
			chainK1Value, chainK2Value, deoptCode)
		page, err := archMmapCode(buf)
		if err != nil {
			return nil, err
		}
		incSpecChainHits() // prove-the-path white-box hit evidence
		return &p4Code{
			proto:         proto,
			codePage:      page,
			jitCtx:        NewJITContext(),
			retA:          info.retA,
			retB:          info.retB,
			retPC:         info.retPC,
			writeRetA:     info.writeRetA,
			preludeOp:     info.preludeOp,
			preludeArg:    info.preludeArg,
			preludeC:      info.preludeC,
			cmpA:          info.cmpA,
			chainOp:       info.chainOp, // preserved: Run-side deopt calls host.Arith x 2
			chainB:        info.chainB,
			chainC:        info.chainC,
			host:          c.hostState,
			useSpec:       true,
			specDeoptCode: deoptCode,
		}, nil
	}

	// Emit: LOADK/RETURN template (arch-routed — amd64 mov rax,imm + ret is
	// 11 bytes; arm64 movz+movk×3 + ret is 20 bytes). When writeRetA=false the
	// value is unused (the mmap segment's return value is a dummy), but the
	// template is still emitted because the mmap segment must be non-empty.
	buf = archEmitLoadKReturn(buf, info.value)

	page, err := archMmapCode(buf)
	if err != nil {
		return nil, err
	}

	// PJ5 CALL void shape Compile hit (prove-the-path white-box hit evidence).
	if info.isCallVoid {
		incSpecCallVoidHits()
	}
	// PJ5 TAILCALL shape Compile hit (prove-the-path white-box hit evidence).
	if info.isTailCall {
		incSpecTailCallHits()
	}
	// PJ5 SELF method call shape Compile hit (prove-the-path white-box hit evidence).
	if info.isSelfCall {
		incSpecSelfCallHits()
	}

	return &p4Code{
		proto:          proto,
		codePage:       page,
		jitCtx:         NewJITContext(),
		retA:           info.retA,
		retB:           info.retB,
		retPC:          info.retPC,
		writeRetA:      info.writeRetA,
		preludeOp:      info.preludeOp,
		preludeArg:     info.preludeArg,
		preludeC:       info.preludeC,
		cmpA:           info.cmpA,
		chainOp:        info.chainOp,
		chainB:         info.chainB,
		chainC:         info.chainC,
		host:           c.hostState,
		isCallVoid:     info.isCallVoid,
		isCallUpval:    info.isCallUpval,
		callA:          info.callA,
		callB:          info.callB,
		callC:          info.callC,
		callArgCount:   info.callArgCount,
		callMultiRet:   info.callMultiRet,
		callArg1IsK:    info.callArg1IsK,
		callArg1K:      info.callArg1K,
		callArg1RegSrc: info.callArg1RegSrc,
		callArg2IsK:    info.callArg2IsK,
		callArg2K:      info.callArg2K,
		callArg2RegSrc: info.callArg2RegSrc,
		callArg3IsK:    info.callArg3IsK,
		callArg3K:      info.callArg3K,
		callArg3RegSrc: info.callArg3RegSrc,
		callArg4IsK:    info.callArg4IsK,
		callArg4K:      info.callArg4K,
		callArg4RegSrc: info.callArg4RegSrc,
		callArg5IsK:    info.callArg5IsK,
		callArg5K:      info.callArg5K,
		callArg5RegSrc: info.callArg5RegSrc,
		callArg6IsK:    info.callArg6IsK,
		callArg6K:      info.callArg6K,
		callArg6RegSrc: info.callArg6RegSrc,
		callArg7IsK:    info.callArg7IsK,
		callArg7K:      info.callArg7K,
		callArg7RegSrc: info.callArg7RegSrc,
		isTailCall:     info.isTailCall,
		// PJ5 SELF inline shape fields
		isSelfCall:      info.isSelfCall,
		selfCallA:       info.selfCallA,
		selfMethodRK:    info.selfMethodRK,
		selfRecvSrcReg:  info.selfRecvSrcReg,
		selfRecvIsUpval: info.selfRecvIsUpval,
	}, nil
}

// ErrCompileUnsupportedShape: the fallback error Compile returns when it
// rejects a Proto whose shape is not in the PJ7 wired subset. SupportsAllOpcodes
// already screens out the vast majority at F7; this error is the secondary shape
// check for the jit-package prove-the-path unit-test path that calls Compile
// directly, bypassing SupportsAllOpcodes. On receiving this error the bridge
// marks the Proto TierStuck (permanently interpreted, no retry).
//
// PJ7 wired supported shapes:
//   - length 1: RETURN A B (B=1 empty function / B=2 identity returning the parameter)
//   - length 2/3: leading RETURN A 2 (luac-optimized shape)
//   - length 2/3: MOVE A B + RETURN A 2 (retA=B skips the intermediate)
//   - length 2/3: GETUPVAL A B + RETURN A 2 (prelude path calls host.GetUpval)
//   - length 2/3: LOADK/LOADBOOL/LOADNIL A ... + RETURN A 2 (constant return)
//   - length 2/3: ADD/SUB/MUL/DIV/MOD/POW A B C + RETURN A 2 (prelude path
//     calls the host.Arith slow-path helper, may bubble ERR)
//   - length 2/3: UNM/LEN A B + RETURN A 2 (prelude path calls the host.Unm/Len
//     slow-path helper, may bubble ERR)
//   - length 2/3: NEWTABLE A B C + RETURN A 2 (prelude path calls host.NewTable,
//     never raises)
//   - length 2/3: GETTABLE A B C + RETURN A 2 (prelude path calls host.GetTable,
//     via IC + __index metamethod chain, may bubble ERR)
//   - length 2/3: GETGLOBAL A Bx + RETURN A 2 (prelude path calls host.DoGetGlobal,
//     may bubble ERR)
//   - length 2/3: SETTABLE A B C + RETURN A 1 (setter, 0 return values, prelude
//     path calls host.SetTable, via IC + __newindex metamethod chain, may bubble ERR)
//   - length 2/3: SETGLOBAL A Bx + RETURN A 1 (setter, prelude path calls
//     host.DoSetGlobal, may bubble ERR)
//   - **length 3 PJ5 CALL void**: MOVE A B + CALL A 1 1 + RETURN 0 1
//     (`function(g) g() end` class — the Run-side prelude path calls
//     host.CallBaseline to complete baseline doCall dispatch byte-equal with P1,
//     may bubble ERR)
var ErrCompileUnsupportedShape = errors.New("internal/gibbous/jit: P4 PJ7 unsupported shape (expected: single RETURN A B / single-BB MOVE|GETUPVAL|LOADK|LOADBOOL|LOADNIL|ADD..POW|UNM|LEN|NEWTABLE|GETTABLE|GETGLOBAL|SETTABLE|SETGLOBAL + RETURN A 2 (getter) / 1 (setter) / PJ5 MOVE+CALL+RETURN void)")

// compileIcArrayHit compiles the PJ4 IC ArrayHit shape (per
// analyzeGetTableArrayHit): emits a 129-byte IC inline template; on failure it
// deopts → the Run side calls host.GetTable byte-equal with P1.
//
// **deopt path**: the Run side detects raxSpec==deoptCode → calls host.GetTable
// (via IC + hash + __index metamethod chain, byte-equal with the interpreter).
// p4Code sets icArrayHitDeopt=true to distinguish it from the reg-limit FORLOOP
// host.ForPrep path.
func (c *Compiler) compileIcArrayHit(proto *bytecode.Proto, info shapeInfo) (bridge.GibbousCode, error) {
	const deoptCode uint64 = 0xFFFCDEAD_DEADBE00
	arenaBaseOff := int32(JITContextArenaBaseOffset)
	var buf []byte
	buf = archEmitGetTableArrayHit(buf, info.icAReg, info.icBReg,
		info.icStableShape, info.icStableIndex, arenaBaseOff, deoptCode)
	page, err := archMmapCode(buf)
	if err != nil {
		return nil, err
	}
	incSpecTableHits()
	return &p4Code{
		proto:         proto,
		codePage:      page,
		jitCtx:        NewJITContext(),
		retA:          info.retA,
		retB:          info.retB,
		retPC:         info.retPC,
		writeRetA:     false, // template already wrote R(A) via mov [rbx+aReg*8], rax
		preludeOp:     info.preludeOp,
		preludeArg:    info.preludeArg,
		preludeC:      info.preludeC,
		host:          c.hostState,
		useSpec:       true,
		specDeoptCode: deoptCode,
		icArrayHit:    true, // Run side distinguishes the deopt path via host.GetTable
	}, nil
}

// compileIcNodeHit compiles the PJ4 IC NodeHit shape (per
// analyzeGetTableNodeHit): it emits a 159-byte IC NodeHit inline template; on
// failure, deopt → the Run side calls host.GetTable byte-equal P1.
//
// Compared to compileIcArrayHit, it has one extra compile-time-fixed stableKey
// parameter (the template verifies NodeKey == stableKey to guard against key
// degradation). The Run deopt path shares the icArrayHit field with ArrayHit —
// both, on raxSpec==deoptCode, call host.GetTable byte-equal on the Run side
// (the P1 interpreter's same icGetTable path supports both ArrayHit and
// NodeHit, so no distinction is needed).
func (c *Compiler) compileIcNodeHit(proto *bytecode.Proto, info shapeInfo) (bridge.GibbousCode, error) {
	const deoptCode uint64 = 0xFFFCDEAD_DEADBE01 // distinct from ArrayHit but shares the host.GetTable path on the Run side
	arenaBaseOff := int32(JITContextArenaBaseOffset)
	var buf []byte
	buf = archEmitGetTableNodeHit(buf, info.icAReg, info.icBReg,
		info.icStableShape, info.icStableIndex, info.icStableKey,
		arenaBaseOff, deoptCode)
	page, err := archMmapCode(buf)
	if err != nil {
		return nil, err
	}
	incSpecTableHits() // reuses the SpecTableHits probe (ArrayHit + NodeHit combined)
	return &p4Code{
		proto:         proto,
		codePage:      page,
		jitCtx:        NewJITContext(),
		retA:          info.retA,
		retB:          info.retB,
		retPC:         info.retPC,
		writeRetA:     false, // template already wrote R(A) via mov [rbx+aReg*8], rax
		preludeOp:     info.preludeOp,
		preludeArg:    info.preludeArg,
		preludeC:      info.preludeC,
		host:          c.hostState,
		useSpec:       true,
		specDeoptCode: deoptCode,
		icArrayHit:    true, // Run side shares the host.GetTable path (P1 icGetTable compatible)
	}, nil
}

// compileIcSetArrayHit compiles the PJ4 SETTABLE IC ArrayHit shape (per
// analyzeSetTableArrayHit): it emits a 113-byte SETTABLE IC inline write-back
// template; on failure, deopt → the Run side calls host.SetTable byte-equal P1
// (through icSetTable + the __newindex metamethod chain).
//
// **setter shape retB=1** (SETTABLE, 0 return values) — the Run side's
// DoReturn does not read R(A).
//
// Template, 113 bytes: strict IsTable guard + arena base + gen check + arrayRef
// + load R(C) value → rdx + write-back store mov [r14+rcx+stableIndex*8], rdx +
// ret + deopt block. **Simplification**: this batch does not verify that the
// existing array[stableIndex] != nil (the new-key path) + does not verify the
// __newindex metatable (see the EmitSetTableArrayHit godoc).
func (c *Compiler) compileIcSetArrayHit(proto *bytecode.Proto, info shapeInfo) (bridge.GibbousCode, error) {
	const deoptCode uint64 = 0xFFFCDEAD_DEADBE02
	arenaBaseOff := int32(JITContextArenaBaseOffset)
	var buf []byte
	buf = archEmitSetTableArrayHit(buf, info.icAReg, info.icSetCReg,
		info.icStableShape, info.icStableIndex, arenaBaseOff, deoptCode)
	page, err := archMmapCode(buf)
	if err != nil {
		return nil, err
	}
	incSpecTableHits() // reuses the SpecTableHits probe (ArrayHit + NodeHit + SETTABLE combined)
	return &p4Code{
		proto:         proto,
		codePage:      page,
		jitCtx:        NewJITContext(),
		retA:          info.retA,
		retB:          info.retB, // setter retB=1
		retPC:         info.retPC,
		writeRetA:     false, // setter has no R(A) write
		preludeOp:     info.preludeOp,
		preludeArg:    info.preludeArg,
		preludeC:      info.preludeC,
		host:          c.hostState,
		useSpec:       true,
		specDeoptCode: deoptCode,
		icSetArrayHit: true, // Run-side deopt goes through host.SetTable
	}, nil
}

// compileIcSelfArrayHit compiles the PJ4 SELF IC ArrayHit shape (per
// analyzeSelfArrayHit): it emits a 139-byte SELF IC inline template (GETTABLE
// ArrayHit 132 + a 7-byte R(A+1) copy segment); on failure, deopt → the Run
// side calls host.GetTable byte-equal P1 (R(A+1) is already stored, and the P1
// SELF case performs the same steps byte-equal).
//
// **SELF shape retB=2** (SELF + RETURN A 2 taking R(A)). R(A+1) is copied by
// the template from R(B); the deopt path need not roll back R(A+1) — the P1
// SELF path likewise first does setReg(A+1, B).
func (c *Compiler) compileIcSelfArrayHit(proto *bytecode.Proto, info shapeInfo) (bridge.GibbousCode, error) {
	const deoptCode uint64 = 0xFFFCDEAD_DEADBE03
	arenaBaseOff := int32(JITContextArenaBaseOffset)
	var buf []byte
	buf = archEmitSelfArrayHit(buf, info.icAReg, info.icBReg,
		info.icStableShape, info.icStableIndex, arenaBaseOff, deoptCode)
	page, err := archMmapCode(buf)
	if err != nil {
		return nil, err
	}
	incSpecTableHits() // reuses the SpecTableHits probe (all PJ4 paths combined)
	return &p4Code{
		proto:         proto,
		codePage:      page,
		jitCtx:        NewJITContext(),
		retA:          info.retA,
		retB:          info.retB,
		retPC:         info.retPC,
		writeRetA:     false, // template already wrote R(A)
		preludeOp:     info.preludeOp,
		preludeArg:    info.preludeArg,
		preludeC:      info.preludeC,
		host:          c.hostState,
		useSpec:       true,
		specDeoptCode: deoptCode,
		icArrayHit:    true, // SELF deopt reuses the host.GetTable path (the P1 SELF case already did setReg(A+1, B) first)
	}, nil
}

// compileIcSetNodeHit compiles the PJ4 SETTABLE IC NodeHit shape (per
// analyzeSetTableNodeHit): it emits a 140-byte SETTABLE NodeHit IC inline
// write-back template (GetTable NodeHit 159 - getter segment 34 + setter
// segment 15); on failure, deopt → the Run side calls host.SetTable byte-equal
// P1 (through icSetTable + the __newindex metamethod chain).
//
// **setter shape retB=1**; the Run side's DoReturn does not read R(A).
//
// Template, 140 bytes: strict IsTable guard + arena base + gen check + nodeRef
// + node[stableIndex] + key comparison + load R(C) → rdx + write-back store
// NodeVal + ret + deopt block. The design simplification is the same as
// SetTable ArrayHit: no __newindex / does not verify the existing NodeVal (see
// the EmitSetTableNodeHit godoc).
func (c *Compiler) compileIcSetNodeHit(proto *bytecode.Proto, info shapeInfo) (bridge.GibbousCode, error) {
	const deoptCode uint64 = 0xFFFCDEAD_DEADBE04
	arenaBaseOff := int32(JITContextArenaBaseOffset)
	var buf []byte
	buf = archEmitSetTableNodeHit(buf, info.icAReg, info.icSetCReg,
		info.icStableShape, info.icStableIndex, info.icStableKey,
		arenaBaseOff, deoptCode)
	page, err := archMmapCode(buf)
	if err != nil {
		return nil, err
	}
	incSpecTableHits() // reuses the SpecTableHits probe (all PJ4 paths combined)
	return &p4Code{
		proto:         proto,
		codePage:      page,
		jitCtx:        NewJITContext(),
		retA:          info.retA,
		retB:          info.retB, // setter retB=1
		retPC:         info.retPC,
		writeRetA:     false, // setter has no R(A) write
		preludeOp:     info.preludeOp,
		preludeArg:    info.preludeArg,
		preludeC:      info.preludeC,
		host:          c.hostState,
		useSpec:       true,
		specDeoptCode: deoptCode,
		icSetArrayHit: true, // Run-side deopt reuses the host.SetTable path (P1 icSetTable supports both ArrayHit+NodeHit)
	}, nil
}

// compileIcSelfNodeHit compiles the PJ4 SELF IC NodeHit shape (per
// analyzeSelfNodeHit): it emits a 166-byte SELF NodeHit IC inline template
// (SELF ArrayHit 139 + a 27-byte key-comparison segment); on failure, deopt →
// the Run side calls host.GetTable byte-equal P1 (R(A+1) is already stored, and
// the P1 SELF case performs the same steps; P1 icGetTable supports NodeHit).
//
// **SELF shape retB=2** (taking R(A), the method function). R(A+1) is copied by
// the template from R(B); the deopt path need not roll back — the P1 SELF path
// likewise first does setReg(A+1, B). This is the typical real-world
// `obj:method()` call shape (the method is a string ident).
func (c *Compiler) compileIcSelfNodeHit(proto *bytecode.Proto, info shapeInfo) (bridge.GibbousCode, error) {
	const deoptCode uint64 = 0xFFFCDEAD_DEADBE05
	arenaBaseOff := int32(JITContextArenaBaseOffset)
	var buf []byte
	buf = archEmitSelfNodeHit(buf, info.icAReg, info.icBReg,
		info.icStableShape, info.icStableIndex, info.icStableKey,
		arenaBaseOff, deoptCode)
	page, err := archMmapCode(buf)
	if err != nil {
		return nil, err
	}
	incSpecTableHits() // reuses the SpecTableHits probe (all PJ4 paths combined)
	return &p4Code{
		proto:         proto,
		codePage:      page,
		jitCtx:        NewJITContext(),
		retA:          info.retA,
		retB:          info.retB,
		retPC:         info.retPC,
		writeRetA:     false, // template already wrote R(A)
		preludeOp:     info.preludeOp,
		preludeArg:    info.preludeArg,
		preludeC:      info.preludeC,
		host:          c.hostState,
		useSpec:       true,
		specDeoptCode: deoptCode,
		icArrayHit:    true, // SELF deopt reuses the host.GetTable path (the P1 SELF case already did setReg(A+1, B))
	}, nil
}

// compileSpecSelfCall compiles the PJ5 SELF + CALL spec template shape (per
// analyzeSelfCallSpecForm + §9.10 PJ4 EmitSelfNodeHit reuse):
//
// it emits a 166-byte SELF NodeHit IC inline template (SELF segment: IC NodeHit
// guard + stableKey comparison + NodeVal store R(callA)=method + store
// R(callA+1)=self); on failure, deopt → the Run side falls back to host.Self;
// **on success, the Run side continues through host.CallBaseline +
// host.DoReturn to complete the CALL segment** (the difference from PJ4 SELF
// NodeHit: one extra CALL step).
//
// **Run-side preprocessing** (per code.go::runSpecSelfCall): before
// callJITSpec, host.GetReg/GetUpval + SetReg first loads R(callA)=recv
// (emulating MOVE/GETUPVAL, because the spec segment reads the receiver at the
// byte level from R(callA)).
func (c *Compiler) compileSpecSelfCall(proto *bytecode.Proto, info shapeInfo) (bridge.GibbousCode, error) {
	const deoptCode uint64 = 0xFFFCDEAD_DEADBE06
	arenaBaseOff := int32(JITContextArenaBaseOffset)
	var buf []byte
	// PJ5 SELF + CALL spec template: **the args-loading segment is emitted
	// before the SELF segment** (per §9.19 amortization measurements: the
	// bottleneck making the 3-arg shape's ratio 1.017x → 1.x slow is the host
	// round-trip of args loading). Args are loaded into R(callA+2..callA+1+N)
	// via byte-level direct mov, skipping N host.GetReg/SetReg cross-Go
	// round-trips. After the SELF segment executes, method/self are already in
	// R(callA)/R(callA+1), the args segment has loaded R(callA+2..), and when
	// host.CallBaseline is called the args are already in place.
	//
	// **recv loading** (MOVE form, form M*): byte-level inline
	// R(callA)=R(srcReg), skipping 2 host.GetReg + SetReg crossings; the
	// GETUPVAL form (form U*) is left to the Run-side host helper round-trip
	// (the upvalue is not on the vsBase stack and needs complex closure
	// addressing).
	//
	// Slots do not conflict: args write R(callA+2..callA+1+N); the recv segment
	// writes R(callA)=R(srcReg); the SELF segment reads R(callA)=recv + writes
	// R(callA+1)=self + writes R(callA)=method.
	// Order: recv inline → args inline → SELF inline → ret (on the failure
	// deopt path, the args+recv segments have already executed and remain valid
	// when falling back to host.Self).
	callA := info.callA
	if !info.selfRecvIsUpval {
		// MOVE recv: byte-level R(callA) = R(srcReg), saving 2 host.GetReg+SetReg crossings
		buf = archEmitSpecArgLoadReg(buf, callA, info.selfRecvSrcReg)
	}
	if info.callArgCount >= 1 {
		dst := callA + 2 + 0
		if info.callArg1IsK {
			buf = archEmitSpecArgLoadK(buf, dst, info.callArg1K)
		} else {
			buf = archEmitSpecArgLoadReg(buf, dst, info.callArg1RegSrc)
		}
	}
	if info.callArgCount >= 2 {
		dst := callA + 2 + 1
		if info.callArg2IsK {
			buf = archEmitSpecArgLoadK(buf, dst, info.callArg2K)
		} else {
			buf = archEmitSpecArgLoadReg(buf, dst, info.callArg2RegSrc)
		}
	}
	if info.callArgCount >= 3 {
		dst := callA + 2 + 2
		if info.callArg3IsK {
			buf = archEmitSpecArgLoadK(buf, dst, info.callArg3K)
		} else {
			buf = archEmitSpecArgLoadReg(buf, dst, info.callArg3RegSrc)
		}
	}
	if info.callArgCount >= 4 {
		dst := callA + 2 + 3
		if info.callArg4IsK {
			buf = archEmitSpecArgLoadK(buf, dst, info.callArg4K)
		} else {
			buf = archEmitSpecArgLoadReg(buf, dst, info.callArg4RegSrc)
		}
	}
	if info.callArgCount >= 5 {
		dst := callA + 2 + 4
		if info.callArg5IsK {
			buf = archEmitSpecArgLoadK(buf, dst, info.callArg5K)
		} else {
			buf = archEmitSpecArgLoadReg(buf, dst, info.callArg5RegSrc)
		}
	}
	if info.callArgCount >= 6 {
		dst := callA + 2 + 5
		if info.callArg6IsK {
			buf = archEmitSpecArgLoadK(buf, dst, info.callArg6K)
		} else {
			buf = archEmitSpecArgLoadReg(buf, dst, info.callArg6RegSrc)
		}
	}
	if info.callArgCount >= 7 {
		dst := callA + 2 + 6
		if info.callArg7IsK {
			buf = archEmitSpecArgLoadK(buf, dst, info.callArg7K)
		} else {
			buf = archEmitSpecArgLoadReg(buf, dst, info.callArg7RegSrc)
		}
	}
	// PJ5 Option B Spike 1 frame-setup inlining (per §9.20.9 (6)
	// compileSpecSelfCall emit rework): when useFrameInline=true:
	//   1. the SELF NodeHit segment uses the NoRet variant (success
	//      falls through; the deopt path rets)
	//   2. BuildVoid0ArgSkeleton (amd64 120B / arm64 164B): enterLuaFrame
	//      byte-level inline, writing CallInfo[depth] 5 words + ciDepth++
	//   3. the ExitHelperRequest segment (amd64 24B / arm64 ~28B, 0 placeholder):
	//      writes jitCtx.exitReasonCode=ExitInlineHelper +
	//      jitCtx.exitArg0=HelperRunCallee + mov rax,ExitInlineHelper + ret —
	//      exits the mmap segment back to the trampoline, which checks RAX==3
	//      and routes to the Go dispatcher to run the callee's Lua body
	//   4. frameInlineResumeOff = len(buf) records the resume entry byte offset
	//   5. PopVoid0ArgSkeleton (amd64 10B / arm64 16B): popCallInfo byte-level
	//      inline, ciDepth-- + return to the trampoline to complete Run
	//
	// **per the commit-5i self-check**: when useFrameInline=false, it still uses
	// the standard archEmitSelfNodeHit (success rets at the segment tail),
	// keeping the existing PJ5 SELF + CALL spec template path's behavior
	// unchanged.
	var frameInlineResumeOff uint32
	if info.useFrameInline && archSupportsFrameInline() {
		// SELF NodeHit NoRet variant — the success path falls through to BuildVoid0Arg
		buf = archEmitSelfNodeHitNoRet(buf, info.icAReg, info.icBReg,
			info.icStableShape, info.icStableIndex, info.icStableKey,
			arenaBaseOff, deoptCode)
		ciDepthAddrOff := int32(JITContextCIDepthAddrOffset)
		ciSegBaseAddrOff := int32(JITContextCISegBaseAddrOffset)
		exitReasonOff := int32(JITContextExitReasonOffset)
		exitArg0Off := int32(JITContextExitArg0Offset)
		// **CI frame 5 words burned at compile time** (per §9.20.5 P3 PW10 same
		// source + 9.20.4 gating callee.NumParams=0 + !IsVararg + MaxStack≤32):
		//   word0 = base (callA + caller.base)
		//   word1 = top (base + callee.MaxStack)
		//   word2 = protoID (callee.protoID) | nresults<<32 | flags<<48
		//   word3 = closure GCRef (loaded at runtime by LoadClosureGCRef,
		//           computed inline within the Build segment)
		//   word4 = nVarargs (0, the callee is not vararg)
		//
		// **Spike 1 simplified shape**: word0/1/2/4 imm are computed at compile
		// time from quantities known to the caller proto; the real values need
		// callee.Proto metadata (MaxStack + protoID). Currently
		// archSupportsFrameInline=false blocks it and info.useFrameInline is not
		// actually set, so this batch lands 0 placeholders, awaiting the
		// commit-5 real integration where analyzeSelfCallSpecForm computes and
		// fills them.
		var word0, word1, word2, word4 uint64
		buf = archEmitFrameInlineBuildVoid0ArgSkeleton(buf,
			ciDepthAddrOff, ciSegBaseAddrOff, info.callA,
			word0, word1, word2, word4)
		// ExitHelperRequest segment (exits the segment back to the trampoline)
		buf = archEmitFrameInlineExitHelperRequest(buf,
			exitReasonOff, exitArg0Off, HelperRunCallee)
		// resume entry offset: the dispatcher returns codePage +
		// frameInlineResumeOff, and the trampoline re-CALLs into the mmap
		// segment to continue running PopVoid0Arg + ret
		frameInlineResumeOff = uint32(len(buf))
		buf = archEmitFrameInlinePopVoid0ArgSkeleton(buf, ciDepthAddrOff)
		// the segment-tail ret is emitted automatically at the end of
		// archMmapCode (equivalent to the normal segment-return exit after the
		// callee completes, RAX=ExitNormal, and the trampoline jumps to
		// skipDispatch for the normal stack pop)
		incSpecFrameInlineHits() // PJ5 Option B Spike 1 frame-setup inlining Compile hit
	} else {
		buf = archEmitSelfNodeHit(buf, info.icAReg, info.icBReg,
			info.icStableShape, info.icStableIndex, info.icStableKey,
			arenaBaseOff, deoptCode)
	}
	page, err := archMmapCode(buf)
	if err != nil {
		return nil, err
	}
	incSpecSelfCallHits()     // PJ5 SELF inline hit (prove-the-path, reuses the SELF probe)
	incSpecSelfCallSpecHits() // PJ5 SELF + CALL spec template dedicated hit
	onP4Install(proto)        // registers p4SpecState[proto] = P4Speculative (per §9.18 + 04 §5.2)
	return &p4Code{
		proto:           proto,
		codePage:        page,
		jitCtx:          NewJITContext(),
		retA:            info.retA,
		retB:            info.retB,
		retPC:           info.retPC,
		writeRetA:       false, // template already wrote R(callA)
		preludeOp:       info.preludeOp,
		preludeArg:      info.preludeArg,
		host:            c.hostState,
		useSpec:         true,
		specDeoptCode:   deoptCode,
		isCallVoid:      info.isCallVoid,
		isCallUpval:     info.isCallUpval,
		callA:           info.callA,
		callB:           info.callB,
		callC:           info.callC,
		callArgCount:    info.callArgCount,
		callArg1IsK:     info.callArg1IsK,
		callArg1K:       info.callArg1K,
		callArg1RegSrc:  info.callArg1RegSrc,
		callArg2IsK:     info.callArg2IsK,
		callArg2K:       info.callArg2K,
		callArg2RegSrc:  info.callArg2RegSrc,
		callArg3IsK:     info.callArg3IsK,
		callArg3K:       info.callArg3K,
		callArg3RegSrc:  info.callArg3RegSrc,
		callArg4IsK:     info.callArg4IsK,
		callArg4K:       info.callArg4K,
		callArg4RegSrc:  info.callArg4RegSrc,
		callArg5IsK:     info.callArg5IsK,
		callArg5K:       info.callArg5K,
		callArg5RegSrc:  info.callArg5RegSrc,
		callArg6IsK:     info.callArg6IsK,
		callArg6K:       info.callArg6K,
		callArg6RegSrc:  info.callArg6RegSrc,
		callArg7IsK:     info.callArg7IsK,
		callArg7K:       info.callArg7K,
		callArg7RegSrc:  info.callArg7RegSrc,
		isSelfCall:      info.isSelfCall,
		selfCallA:       info.selfCallA,
		selfMethodRK:    info.selfMethodRK,
		selfRecvSrcReg:  info.selfRecvSrcReg,
		selfRecvIsUpval: info.selfRecvIsUpval,
		useSpecSelfCall: true,
		// PJ5 Option B Spike 1 frame-setup inlining (per §9.20): passes through
		// info.useFrameInline. Currently analyzeSelfCallSpecForm does not set this
		// field (archSupportsFrameInline=false blocks it), so useFrameInline=false.
		// At Step C-2 real integration, analyzeSelfCallSpecForm adds extra gating
		// (callee Proto metadata known + NumParams=0 + !IsVararg + !NeedsArg +
		// MaxStack≤32) and sets info.useFrameInline=true.
		useFrameInline:       info.useFrameInline,
		frameInlineResumeOff: frameInlineResumeOff,
	}, nil
}
