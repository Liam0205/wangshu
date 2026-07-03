//go:build wangshu_p4 && arm64

// translator_native_arm64.go - arm64 counterpart of translator_native.go.
//
// Mirrors the amd64 CFG-based emit pipeline with arm64 encodings:
//   - X26 = valueStackBase (analog of amd64 RBX)
//   - X27 = jitCtx         (analog of amd64 R15)
//   - X28 = Go G           (permanent; no save/restore ritual)
//
// The op subset is identical to amd64's opSupported gate: only ops
// whose emit sequence makes NO shim call to Go (RETURN is dispatched
// Go-side, arithmetic and compare go through inline NEON FADD/FSUB/...
// + FCMPE). This avoids the mmap-morestack incompatibility that
// crashes Go's stack unwinder under nested + concurrent load.
package peroptranslator

import (
	"errors"
	"fmt"
	"unsafe"

	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/gibbous/jit"
	jitarm64 "github.com/Liam0205/wangshu/internal/gibbous/jit/arm64"
	"github.com/Liam0205/wangshu/internal/value"
)

// nativeCode is the arm64 bridge.GibbousCode implementation for the
// native emit path. Same shape as the amd64 nativeCode but built by
// arm64-specific emit sequences.
type nativeCode struct {
	proto    *bytecode.Proto
	codePage *jitarm64.CodePage
	jitCtx   *jit.JITContext
	host     jit.P4HostState

	retA  int32
	retB  int32
	retPC int32
}

func (c *nativeCode) Proto() *bytecode.Proto { return c.proto }

func (c *nativeCode) Run(stack []uint64, base uint32) (status int32) {
	NativeRunCount.Add(1)
	defer func() {
		if r := recover(); r != nil {
			if len(stack) > 0 {
				stack[0] = 1
			}
			status = 1
		}
	}()
	if c.codePage == nil || c.jitCtx == nil || c.host == nil {
		if len(stack) > 0 {
			stack[0] = 1
		}
		return 1
	}
	// Refcount acquire, same protocol as p4Code.Run / PerOpCode.Run /
	// amd64 nativeCode.Run. See internal/gibbous/jit/amd64/codepage_linux.go
	// for the refcount + deferred munmap rationale. This is the PJ10
	// native emit main execution path on arm64.
	if !c.codePage.Enter() {
		if len(stack) > 0 {
			stack[0] = 1
		}
		return 1
	}
	defer c.codePage.Exit()
	c.host.RefreshJitCtxAddrs(c.jitCtx, int32(base))
	c.jitCtx.SetHostRef(hostIfaceHeader(c.host))

	jitCtxAddr := uintptr(unsafe.Pointer(c.jitCtx))
	vsBaseAddr := c.jitCtx.ValueStackBase()
	rawStatus := jitarm64.CallJITSpec(c.codePage.Addr(), jitCtxAddr, vsBaseAddr)
	// Exit-reason dispatcher loop (issue #37, mirroring amd64): the mmap
	// segment cannot call Go shims at all on arm64 (BL into a Go function
	// from an unregistered code page breaks the stack unwinder), so ops
	// like GETUPVAL / CALL / GETGLOBAL lower to an exit-reason packing +
	// RET. Handle the request Go-side and reenter via codePage +
	// resumeOff until the segment returns a non-helper status. arm64
	// needs no saveGoG dance: X28 = G is permanent and the trampoline
	// reloads X26/X27 on every entry.
	for uint32(rawStatus) == jit.ExitInlineHelper {
		// HelperReturn terminates the run: multi-return Protos lower
		// each RETURN to this exit-reason so every site carries its
		// own (a, b, pc).
		if arg0 := c.jitCtx.ExitArg0(); arg0&jit.HelperCodeMask == jit.HelperReturn {
			a := int32((arg0 >> 16) & 0xFF)
			b := int32((arg0 >> 24) & 0x1FF)
			pc := int32((arg0 >> 42) & 0x3FFFFF)
			return c.host.DoReturn(int32(base), pc, a, b)
		}
		// Snapshot resumeOff BEFORE dispatching: HelperCall drives the
		// callee synchronously, and a recursive call into this same
		// Proto reenters this same nativeCode and clobbers the shared
		// per-Proto jitCtx (resumeOff / exitArg0 / addr fields).
		resumeOff := c.jitCtx.ResumeOff()
		if !c.dispatchHelper(int32(base)) {
			return 1
		}
		// Arena may have grown during the host call; refresh addr
		// fields before reentering the mmap segment. This also repairs
		// any jitCtx fields a recursive inner Run overwrote.
		c.host.RefreshJitCtxAddrs(c.jitCtx, int32(base))
		c.jitCtx.SetHostRef(hostIfaceHeader(c.host))
		vsBaseAddr = c.jitCtx.ValueStackBase()
		resumeAddr := c.codePage.Addr() + uintptr(resumeOff)
		rawStatus = jitarm64.CallJITSpec(resumeAddr, jitCtxAddr, vsBaseAddr)
	}
	status = int32(rawStatus)
	if status == 0 {
		if drStatus := c.host.DoReturn(int32(base), c.retPC, c.retA, c.retB); drStatus != 0 {
			status = drStatus
		}
	}
	return status
}

func (c *nativeCode) PendingErr() error    { return nil }
func (c *nativeCode) Slot() (uint32, bool) { return 0, false }

// IsPJ10Native marks this code as the CFG-based PJ10 native path. See
// amd64 counterpart for rationale.
func (c *nativeCode) IsPJ10Native() bool { return true }

// Dispose releases the mmap'd code page. Safe under concurrent Run: the
// refcount protocol defers the actual munmap until the last active Run's
// Exit. See amd64 counterpart / internal/gibbous/jit/amd64/codepage_linux.go.
func (c *nativeCode) Dispose() {
	if c == nil || c.codePage == nil {
		return
	}
	_ = c.codePage.Dispose()
	c.codePage = nil
}

// hostIfaceHeader / dispatchHelper (arch-shared) live in
// translator_native_dispatch.go.

// PreferNative reports whether Compiler should skip shape-spec fast
// paths and route this Proto directly to the native emitter. See amd64
// counterpart for rationale.
func PreferNative(proto *bytecode.Proto) bool {
	if !AnalyzeNative(proto) {
		return false
	}
	c := buildCFG(proto)
	reach := c.reachableBlocks()
	live := 0
	hasBigBodyBB := false
	for id, bb := range c.blocks {
		if !reach[id] {
			continue
		}
		live++
		if id > 0 && bb.endPC-bb.startPC >= 4 {
			hasBigBodyBB = true
		}
	}
	return live >= 2 && hasBigBodyBB
}

// AnalyzeNative reports whether the arm64 native path can handle the Proto.
// Identical acceptance criteria as amd64.
func AnalyzeNative(proto *bytecode.Proto) bool {
	if proto == nil || len(proto.Code) == 0 {
		return false
	}
	// F7-a: LOADK for string constants isn't inlined (arena-relative
	// bake required). GETGLOBAL / SETGLOBAL / GETTABLE / SETTABLE / SELF
	// route through host shims that index proto.Consts directly, so those
	// stay fine; only reject when a live LOADK actually loads a string.
	stringConst := func(bx int) bool {
		if bx < 0 || bx >= len(proto.StringLitIdx) {
			return false
		}
		return proto.StringLitIdx[bx] >= 0
	}
	if proto.IsVararg {
		return false
	}
	c := buildCFG(proto)
	if !c.isReducible() {
		return false
	}
	reach := c.reachableBlocks()
	for id, bb := range c.blocks {
		if !reach[id] {
			continue
		}
		for pc := bb.startPC; pc < bb.endPC; pc++ {
			ins := proto.Code[pc]
			op := bytecode.Op(ins)
			if !opSupported(op) {
				return false
			}
			switch op {
			case bytecode.LOADK:
				if stringConst(bytecode.Bx(ins)) {
					return false
				}
			case bytecode.ADD, bytecode.SUB, bytecode.MUL, bytecode.DIV:
				// arm64 native arith has no shim fallback for
				// non-number reg operands (see file header: arm64
				// mmap segment can't safely call Go helpers under
				// stack unwind), so the IsNumber guard can only exit
				// the segment with a generic error — which diverges
				// from P1 on legitimate coercion (`"5" + 1`) and
				// __add metamethod inputs. Until the arm64 shim
				// unwinder conflict is resolved, reject the whole
				// proto so shape-spec / interpreter handles arith.
				// (amd64 keeps native arith because it can fall
				// through to a real shim call safely.)
				return false
			case bytecode.LT, bytecode.LE:
				// Same rationale as arith: FCMPE on non-number bit
				// patterns produces meaningless flags (P1 LT/LE has
				// string ordering and __lt / __le metamethods that
				// we can't replicate inline on arm64 without a shim
				// fallback). Reject until the fallback path exists.
				return false
			case bytecode.EQ:
				// arm64 inlineRawEqArm64 doesn't yet emit K operand
				// paths; keep the strict reg-reg gate here so we don't
				// silently fall out of the native emit halfway through.
				if bytecode.B(ins) >= 256 || bytecode.C(ins) >= 256 {
					return false
				}
			case bytecode.CALL:
				// CALL rides the exit-reason path; the dispatcher runs
				// host.CallBaseline which drives the callee to
				// completion synchronously. B=0 (args to top) and C=0
				// (multret) depend on a live `top` the native segment
				// doesn't maintain per-op — reject those forms. Same
				// gate as amd64.
				if bytecode.B(ins) == 0 || bytecode.C(ins) == 0 {
					return false
				}
			case bytecode.GETGLOBAL, bytecode.SETGLOBAL:
				// Only enter native emit when the IC snapshot says
				// NodeHit — the inline fast path is a gen check + fixed
				// node slot access. An un-warmed site would pay an
				// exit-reason round trip per access, which loses to
				// shape-spec / interpreter (amd64 measured a ~14%
				// Transform CallInto regression without this gate).
				if int(pc) >= len(proto.IC) {
					return false
				}
				if proto.IC[pc].Kind != bytecode.ICKindNodeHit {
					return false
				}
			case bytecode.GETTABLE, bytecode.SETTABLE:
				// ArrayHit gets the inline fast path; NodeHit rides the
				// exit-reason slow path (host.GetTable/SetTable are
				// byte-equal to the interpreter's IC path). Un-warmed
				// (None) or meta/megamorphic sites stay on P1 — those
				// would exit on every access. Same gate as amd64.
				if int(pc) >= len(proto.IC) {
					return false
				}
				if k := proto.IC[pc].Kind; k != bytecode.ICKindArrayHit &&
					k != bytecode.ICKindNodeHit {
					return false
				}
			case bytecode.NEWTABLE:
				// NEWTABLE rides the exit-reason path (host allocates).
				// B/C carry Fb-encoded presize hints; the packed arg
				// slots are 9-bit so larger hints would truncate —
				// reject (same as amd64).
				if bytecode.B(ins) >= 256 || bytecode.C(ins) >= 256 {
					return false
				}
			}
		}
	}
	// Single reachable RETURN only (multi-return lowering is step 6 of
	// the arm64 exit-reason port).
	//
	// CALL density gate (mirror of amd64): every CALL is an exit-reason
	// round trip (mmap RET -> Go dispatch -> host.CallBaseline -> mmap
	// reentry) costing roughly 15-25 interpreted ops. On amd64 the
	// measured break-even was totalOps/callCount >= 16; arm64 round-trip
	// cost is the same order (trampoline + dispatch, no reflection), so
	// start from the same threshold and re-measure on hardware when the
	// full op set has landed (issue #40 stage 2 bench pass).
	returnCount := 0
	callCount := 0
	totalOps := 0
	for id, bb := range c.blocks {
		if !reach[id] {
			continue
		}
		for pc := bb.startPC; pc < bb.endPC; pc++ {
			totalOps++
			switch bytecode.Op(proto.Code[pc]) {
			case bytecode.RETURN:
				returnCount++
			case bytecode.CALL:
				callCount++
			}
		}
	}
	if returnCount != 1 {
		return false
	}
	if callCount > 0 && totalOps/callCount < 16 {
		return false
	}
	return true
}

// opSupported: arm64 subset. Ops beyond the original 18-op mmap-safe
// set are added stepwise as the exit-reason protocol port progresses
// (issue #37): GETUPVAL / SETUPVAL landed first (simplest end-to-end
// round trip — never raise, no IC gate).
func opSupported(op bytecode.OpCode) bool {
	switch op {
	case bytecode.MOVE, bytecode.LOADK, bytecode.LOADBOOL, bytecode.LOADNIL,
		bytecode.ADD, bytecode.SUB, bytecode.MUL, bytecode.DIV,
		bytecode.NOT,
		bytecode.EQ, bytecode.LT, bytecode.LE,
		bytecode.TEST, bytecode.TESTSET,
		bytecode.JMP, bytecode.FORPREP, bytecode.FORLOOP,
		bytecode.GETUPVAL, bytecode.SETUPVAL,
		bytecode.CALL,
		bytecode.GETGLOBAL, bytecode.SETGLOBAL,
		bytecode.GETTABLE, bytecode.SETTABLE, bytecode.NEWTABLE,
		bytecode.RETURN:
		return true
	default:
		return false
	}
}

// TranslateProtoNative is the arm64 entry called from init() to route
// native compile requests through the CFG + emit pipeline.
func TranslateProtoNative(proto *bytecode.Proto, host jit.P4HostState) (*nativeCode, error) {
	if host == nil {
		return nil, errors.New("peroptranslator: nil P4HostState")
	}
	if !AnalyzeNative(proto) {
		return nil, errors.New("peroptranslator: proto not supported by native path")
	}
	c := buildCFG(proto)
	buf := newCodeBuf(len(c.blocks))
	consts := make([]uint64, len(proto.Consts))
	for i, v := range proto.Consts {
		consts[i] = uint64(v)
	}
	icSnap := snapshotProtoIC(proto)
	buf.proto = &codeBufProto{Consts: consts, IC: icSnap}
	// Bake the globals table byte offset for the GETGLOBAL / SETGLOBAL
	// NodeHit inline fast path (same identity-is-stable contract as the
	// amd64 emit and P3 wasm's emitGetGlobal).
	buf.proto.GlobalsTaddr = uint32(host.GlobalsRaw() & 0x0000_FFFF_FFFF_FFFF)

	// Prologue: reload X26 = vsBase from jitCtx (X27+off). arm64 doesn't
	// need the amd64 saveGoG dance because X28 = G is permanent on Go
	// arm64 ABIInternal.
	buf.emit(jitarm64.EmitLdrXtFromXnDisp(nil, regX26, regX27,
		uint16(jit.JITContextValueStackBaseOffset)))

	reach := c.reachableBlocks()
	for id, bb := range c.blocks {
		if !reach[id] {
			continue
		}
		if err := buf.bindLabel(id); err != nil {
			return nil, fmt.Errorf("bindLabel BB %d: %w", id, err)
		}
		if err := emitBBArm64(buf, c, bb, id); err != nil {
			return nil, fmt.Errorf("emit BB %d: %w", id, err)
		}
	}
	if err := buf.resolveLabels(); err != nil {
		return nil, fmt.Errorf("resolveLabels: %w", err)
	}
	page, err := jitarm64.MmapCode(buf.bytes)
	if err != nil {
		return nil, err
	}
	NativeCompileCount.Add(1)
	return &nativeCode{
		proto:    proto,
		codePage: page,
		jitCtx:   jit.NewJITContext(),
		host:     host,
		retA:     buf.proto.RetA,
		retB:     buf.proto.RetB,
		retPC:    buf.proto.RetPC,
	}, nil
}

func emitBBArm64(buf *codeBuf, c *cfg, bb *basicBlock, bbID int) error {
	code := c.proto.Code
	lastPC := bb.endPC - 1
	if lastPC < bb.startPC {
		return nil
	}
	for pc := bb.startPC; pc < lastPC; pc++ {
		if err := emitLinearOpArm64(buf, code[pc], pc); err != nil {
			return err
		}
	}
	termIns := code[lastPC]
	return emitTerminatorArm64(buf, c, bb, bbID, termIns, lastPC)
}

func emitLinearOpArm64(buf *codeBuf, ins bytecode.Instruction, pc int32) error {
	emitResumePreludeIfPendingArm64(buf)
	op := bytecode.Op(ins)
	a := uint8(bytecode.A(ins))
	bReg := uint8(bytecode.B(ins))
	bRK := bytecode.B(ins)
	cRK := bytecode.C(ins)
	bx := bytecode.Bx(ins)

	switch op {
	case bytecode.MOVE:
		emitMOVEArm64(buf, a, bReg)
	case bytecode.LOADK:
		if buf.proto == nil || int(bx) >= len(buf.proto.Consts) {
			return fmt.Errorf("LOADK: const idx %d out of range", bx)
		}
		emitLOADKArm64(buf, a, buf.proto.Consts[bx])
	case bytecode.LOADBOOL:
		emitLOADBOOLArm64_valueOnly(buf, a, bReg)
	case bytecode.LOADNIL:
		emitLOADNILArm64(buf, a, bReg)
	case bytecode.GETUPVAL:
		emitGETUPVALArm64(buf, a, bReg)
	case bytecode.SETUPVAL:
		emitSETUPVALArm64(buf, a, bReg)
	case bytecode.CALL:
		emitCALLArm64(buf, pc, a, bReg, uint8(cRK))
	case bytecode.GETGLOBAL:
		emitGETGLOBALArm64(buf, pc, a, uint16(bx))
	case bytecode.SETGLOBAL:
		emitSETGLOBALArm64(buf, pc, a, uint16(bx))
	case bytecode.GETTABLE:
		emitGETTABLEArm64(buf, pc, a, bReg, cRK)
	case bytecode.SETTABLE:
		emitSETTABLEArm64(buf, pc, a, bRK, cRK)
	case bytecode.NEWTABLE:
		emitNEWTABLEArm64(buf, pc, a, bReg, uint8(cRK))
	case bytecode.NOT:
		emitNOTArm64Inline(buf, a, bReg)
	case bytecode.ADD, bytecode.SUB, bytecode.MUL, bytecode.DIV:
		// arm64 keeps arith fully inline (no shim call — see file
		// header: "arithmetic and compare go through inline NEON").
		// Reg operands run through an IsNumber guard first; on failure
		// the mmap segment returns status=1 to the trampoline, which
		// Go-side surfaces as a generic gibbous error (fuzz asserts
		// error-existence only, not the specific message).
		if !inlineArithNEONArm64WithGuard(buf, op, a, bRK, cRK) {
			return fmt.Errorf("emitLinearOpArm64: inline arith failed pc=%d", pc)
		}
	default:
		return fmt.Errorf("emitLinearOpArm64: unsupported op %v at pc %d", op, pc)
	}
	return nil
}

func emitTerminatorArm64(buf *codeBuf, c *cfg, bb *basicBlock, bbID int, ins bytecode.Instruction, pc int32) error {
	emitResumePreludeIfPendingArm64(buf)
	op := bytecode.Op(ins)
	a := uint8(bytecode.A(ins))
	b := uint8(bytecode.B(ins))
	bRK := bytecode.B(ins)
	cRK := bytecode.C(ins)

	switch op {
	case bytecode.RETURN:
		if buf.proto != nil {
			buf.proto.RetA = int32(a)
			buf.proto.RetB = int32(b)
			buf.proto.RetPC = pc
		}
		// mov x0, #0; ret
		buf.emit(jitarm64.EmitMovXdImm64(nil, 0, 0))
		buf.emit(jitarm64.EmitRet(nil))
	case bytecode.JMP:
		if len(bb.succs) != 1 {
			return fmt.Errorf("JMP with %d succs at pc %d", len(bb.succs), pc)
		}
		emitJMPArm64Fixup(buf, bb.succs[0])
	case bytecode.FORPREP:
		if len(bb.succs) != 1 {
			return fmt.Errorf("FORPREP with %d succs at pc %d", len(bb.succs), pc)
		}
		emitFORPREPArm64Inline(buf, a, bb.succs[0])
	case bytecode.FORLOOP:
		if len(bb.succs) != 2 {
			return fmt.Errorf("FORLOOP with %d succs at pc %d", len(bb.succs), pc)
		}
		emitFORLOOPArm64Inline(buf, a, bb.succs[0], bb.succs[1])
	case bytecode.LT, bytecode.LE:
		if len(bb.succs) != 2 {
			return fmt.Errorf("%v with %d succs at pc %d", op, len(bb.succs), pc)
		}
		if !inlineNumericCompareArm64(buf, op, a, bRK, cRK, bb.succs[0], bb.succs[1]) {
			return fmt.Errorf("emitTerminatorArm64: inline compare failed pc=%d", pc)
		}
	case bytecode.EQ:
		if len(bb.succs) != 2 {
			return fmt.Errorf("EQ with %d succs at pc %d", len(bb.succs), pc)
		}
		if !inlineRawEqArm64(buf, a, bRK, cRK, bb.succs[0], bb.succs[1]) {
			return fmt.Errorf("emitTerminatorArm64: inline EQ failed pc=%d", pc)
		}
	case bytecode.TEST:
		if len(bb.succs) != 2 {
			return fmt.Errorf("TEST with %d succs at pc %d", len(bb.succs), pc)
		}
		emitTESTArm64Inline(buf, a, uint8(bytecode.C(ins)), bb.succs[0], bb.succs[1])
	case bytecode.TESTSET:
		if len(bb.succs) != 2 {
			return fmt.Errorf("TESTSET with %d succs at pc %d", len(bb.succs), pc)
		}
		emitTESTSETArm64Inline(buf, a, b, uint8(bytecode.C(ins)), bb.succs[0], bb.succs[1])
	case bytecode.LOADBOOL:
		emitLOADBOOLArm64_valueOnly(buf, a, b)
		if len(bb.succs) != 1 {
			return fmt.Errorf("LOADBOOL terminator with %d succs at pc %d", len(bb.succs), pc)
		}
		emitJMPArm64Fixup(buf, bb.succs[0])
	default:
		if err := emitLinearOpArm64(buf, ins, pc); err != nil {
			return err
		}
		if len(bb.succs) == 1 {
			emitJMPArm64Fixup(buf, bb.succs[0])
		}
	}
	return nil
}

// emitJMPArm64Fixup emits an unconditional B with a rel26 fixup.
func emitJMPArm64Fixup(buf *codeBuf, targetBB int) {
	patchOff := buf.pos()
	// Placeholder: b #0 (encoding 0x14000000)
	buf.emit([]byte{0x00, 0x00, 0x00, 0x14})
	buf.addFixupKind(patchOff, buf.pos(), targetBB, fixupKindArm64B26)
}

// inlineArithNEONArm64WithGuard emits inline NEON arith with an
// IsNumber guard on each reg operand. Guard fail exits the mmap
// segment early with X0=1 (Go-side converts this to a generic
// "gibbous: run failed" error, which is enough for fuzz
// error-existence parity with P1).
//
// Layout (K-K falls through to inlineArithNEONArm64 with no guard):
//
//	[guard-B (28 bytes)] ldr X0, [X26+B*8]
//	                     mov X4, qNanBoxBase       ; 4 insns (16B)
//	                     cmp X0, X4                ; 4B
//	                     b.hs err                  ; 4B (patched later)
//	[guard-C (28 bytes)] same pattern, only if c is reg
//	<inline NEON body>   ldr / fmov / fadd/fmul/... / fmov X0,D0 / str
//	b done               ; 4B skip past err block
//	err:                 ; guard failure target
//	  mov X0, #1         ; 16B (movz+3 movks; err path only, size fixed)
//	  ret                ; 4B
//	done:
//
// Reg operands go through the guard; K operands are always numeric
// (AnalyzeNative rejected non-numeric K).
func inlineArithNEONArm64WithGuard(cb *codeBuf, op bytecode.OpCode, a uint8, b, c int) bool {
	// K-K shape: no guard, no exit path, direct inline.
	if b >= 256 && c >= 256 {
		return inlineArithNEONArm64(cb, op, a, b, c)
	}

	// Record guard b.hs offsets so we can patch them once err_off known.
	var guardFixups []int
	emitRegNumberGuardArm64 := func(reg int) {
		if reg >= 256 {
			return
		}
		// ldr X0, [X26 + reg*8]
		cb.emit(jitarm64.EmitLdrXtFromXnDisp(nil, 0, regX26, uint16(reg)*8))
		// mov X4, qNanBoxBase (16B: 4 insns). Value 0xFFF8_0000_0000_0000
		// is the lower bound of the non-number NaN-box space; any raw
		// uint64 >= this constant is a tagged non-number.
		cb.emit(jitarm64.EmitMovXdImm64(nil, 4, 0xFFF8_0000_0000_0000))
		// cmp X0, X4
		cb.emit(jitarm64.EmitCmpXnXm(nil, 0, 4))
		// b.hs err (placeholder imm19=0, patched later)
		patchOff := int(cb.pos())
		cb.emit(jitarm64.EmitBCond(nil, jitarm64.CondHS, 0))
		guardFixups = append(guardFixups, patchOff)
	}
	emitRegNumberGuardArm64(b)
	emitRegNumberGuardArm64(c)

	// Fast-path body (mirrors inlineArithNEONArm64 without the return
	// value, since we've already committed to the fast path).
	if !inlineLoadOperandToDArm64(cb, 0, b) {
		return false
	}
	if !inlineLoadOperandToDArm64(cb, 1, c) {
		return false
	}
	switch op {
	case bytecode.ADD:
		cb.emit(jitarm64.EmitFaddDdDnDm(nil, 0, 0, 1))
	case bytecode.SUB:
		cb.emit(jitarm64.EmitFsubDdDnDm(nil, 0, 0, 1))
	case bytecode.MUL:
		cb.emit(jitarm64.EmitFmulDdDnDm(nil, 0, 0, 1))
	case bytecode.DIV:
		cb.emit(jitarm64.EmitFdivDdDnDm(nil, 0, 0, 1))
	default:
		return false
	}
	cb.emit(jitarm64.EmitFmovXdFromDn(nil, 0, 0))
	cb.emit(jitarm64.EmitStrXtToXnDisp(nil, 0, regX26, uint16(a)*8))

	// b done (skip err block on success). arm64 B rel26: target = PC
	// + imm26*4. err block after b is 16B (movz+3 movks) + 4B (ret) =
	// 20B, so done sits at PC + 24 (b itself + 20B). imm26 = 6.
	cb.emit(jitarm64.EmitB(nil, 6))

	// err block: mov X0, #1; ret. Guard fixups target here.
	errOff := int(cb.pos())
	cb.emit(jitarm64.EmitMovXdImm64(nil, 0, 1))
	cb.emit(jitarm64.EmitRet(nil))

	// Patch guard fixups: b.hs imm19 = (errOff - patchOff) / 4.
	for _, po := range guardFixups {
		imm19 := int32(errOff-po) / 4
		patchBCondImm19Local(cb.bytes, po, imm19)
	}
	return true
}

// patchBCondImm19Local wraps the jitarm64 helper (unexported so we
// re-implement inline to avoid an export churn).
func patchBCondImm19Local(buf []byte, off int, imm19 int32) {
	insn := uint32(buf[off]) | uint32(buf[off+1])<<8 |
		uint32(buf[off+2])<<16 | uint32(buf[off+3])<<24
	insn &= 0xFF00001F
	insn |= (uint32(imm19) & 0x7FFFF) << 5
	buf[off] = byte(insn)
	buf[off+1] = byte(insn >> 8)
	buf[off+2] = byte(insn >> 16)
	buf[off+3] = byte(insn >> 24)
}

// inlineArithNEONArm64 emits inline NEON double-precision arith for
// ADD/SUB/MUL/DIV. Supports reg-reg / reg-K / K-reg / K-K when K is
// numeric. Sequence per operand:
//
//	<load B to D0>   ; ldr Xt, [X26+B*8]; fmov D0, Xt   (or) movz X0, K; fmov D0, X0
//	<load C to D1>   ; ditto to D1
//	fADD/FSUB/... D0, D0, D1
//	fmov X0, D0
//	str X0, [X26+A*8]
//
// Returns true on success.
func inlineArithNEONArm64(cb *codeBuf, op bytecode.OpCode, a uint8, b, c int) bool {
	if !inlineLoadOperandToDArm64(cb, 0, b) {
		return false
	}
	if !inlineLoadOperandToDArm64(cb, 1, c) {
		return false
	}
	switch op {
	case bytecode.ADD:
		cb.emit(jitarm64.EmitFaddDdDnDm(nil, 0, 0, 1))
	case bytecode.SUB:
		cb.emit(jitarm64.EmitFsubDdDnDm(nil, 0, 0, 1))
	case bytecode.MUL:
		cb.emit(jitarm64.EmitFmulDdDnDm(nil, 0, 0, 1))
	case bytecode.DIV:
		cb.emit(jitarm64.EmitFdivDdDnDm(nil, 0, 0, 1))
	default:
		return false
	}
	// fmov X0, D0
	cb.emit(jitarm64.EmitFmovXdFromDn(nil, 0, 0))
	// str X0, [X26 + A*8]
	cb.emit(jitarm64.EmitStrXtToXnDisp(nil, 0, regX26, uint16(a)*8))
	return true
}

// inlineLoadOperandToDArm64 loads RK operand into Dd. reg -> ldr X0 +
// fmov Dd, X0. K -> movz X0, imm64 + fmov Dd, X0.
func inlineLoadOperandToDArm64(cb *codeBuf, dd uint8, rk int) bool {
	if rk < 256 {
		// ldr Xt=0, [X26+rk*8]; fmov Dd, X0
		cb.emit(jitarm64.EmitLdrXtFromXnDisp(nil, 0, regX26, uint16(rk)*8))
		cb.emit(jitarm64.EmitFmovDdFromXn(nil, dd, 0))
		return true
	}
	kidx := rk - 256
	if cb.proto == nil || kidx < 0 || kidx >= len(cb.proto.Consts) {
		return false
	}
	kbits := cb.proto.Consts[kidx]
	if !value.IsNumber(value.Value(kbits)) {
		return false
	}
	// mov X0, imm64 (4 instr movz+movk×3); fmov Dd, X0
	cb.emit(jitarm64.EmitMovXdImm64(nil, 0, kbits))
	cb.emit(jitarm64.EmitFmovDdFromXn(nil, dd, 0))
	return true
}

// inlineNumericCompareArm64 emits inline FCMPE + B.cond for LT/LE.
// Semantics: cond = (RK(B) op RK(C)); if cond != A then pc++ (succSkip);
// else fall through to JMP target (succExec).
//
// FCMPE flags after `fcmpe Dn, Dm`:
//   - N=1 iff Dn < Dm  (LT via BMI / cond=4)
//   - Z=1 iff Dn == Dm
//   - Others: unordered gets flags {N=0,Z=0,C=1,V=1}
//
// Convenient condition codes for numeric compare (non-NaN):
//   - CondMI (0x4): N set -> Dn < Dm
//   - CondPL (0x5): N clear -> Dn >= Dm
//   - CondLS (0x9): unsigned <= (C clear || Z set); for FP after FCMPE
//     with normal numbers, LS means "Dn <= Dm" -> use for LE(Dn,Dm)? no,
//     use signed variants below.
//   - CondLE (0xD): signed <=; for FP compares this means Dn <= Dm.
//   - CondGT (0xC): signed >; Dn > Dm.
//   - CondGE (0xA): signed >=; Dn >= Dm.
//   - CondLT (0xB): signed <; Dn < Dm.
//
// Lua: `if (RK(B) op RK(C)) != A then pc++`. Branch to succExec when
// cond matches A. So:
//
//	LT + A=0: match when NOT(B<C) i.e., B>=C -> CondGE
//	LT + A=1: match when B<C            -> CondLT
//	LE + A=0: match when NOT(B<=C) i.e., B>C -> CondGT
//	LE + A=1: match when B<=C           -> CondLE
func inlineNumericCompareArm64(cb *codeBuf, op bytecode.OpCode, a uint8, b, c int, succExec, succSkip int) bool {
	if op != bytecode.LT && op != bytecode.LE {
		return false
	}
	if !inlineLoadOperandToDArm64(cb, 0, b) {
		return false
	}
	if !inlineLoadOperandToDArm64(cb, 1, c) {
		return false
	}
	// fcmpe D0, D1
	cb.emit(jitarm64.EmitFcmpeDnDm(nil, 0, 1))
	var cond uint8
	switch op {
	case bytecode.LT:
		if a == 0 {
			cond = jitarm64.CondGE
		} else {
			cond = jitarm64.CondLT
		}
	case bytecode.LE:
		if a == 0 {
			cond = jitarm64.CondGT
		} else {
			cond = jitarm64.CondLE
		}
	}
	// B.<cond> <succExec>  (placeholder imm19=0)
	patchOff := cb.pos()
	cb.emit(jitarm64.EmitBCond(nil, cond, 0))
	cb.addFixupKind(patchOff, cb.pos(), succExec, fixupKindArm64Cond)
	// B <succSkip>
	emitJMPArm64Fixup(cb, succSkip)
	return true
}

// emitNOTArm64Inline emits R(A) := not R(B) inline (no shim).
//
// Lua truthiness: only nil and false are falsy; everything else truthy.
// Value encoding: value.Nil and value.False are specific NaN-box bits;
// need to check `R(B) == Nil || R(B) == False`.
//
// Sequence:
//
//	ldr X0, [X26 + B*8]           ; load R(B)
//	mov X1, #value.Nil            ; imm64 (4 instr)
//	cmp X0, X1
//	b.eq  isFalsy                 ; +N to isFalsy label
//	mov X1, #value.False          ; imm64
//	cmp X0, X1
//	b.ne  notFalsy                ; skip past True store
//	isFalsy:
//	mov X0, #value.True
//	b   done
//	notFalsy:
//	mov X0, #value.False
//	done:
//	str X0, [X26 + A*8]
//
// Layout gets tricky because imm64 movz+3xmovk = 16 bytes each. Use
// forward-branch-then-back approach:
//
//	Load R(B) -> X0
//	Compare X0 with Nil (via imm64 in X1); if equal -> X0 = True, done
//	Compare X0 with False; if equal -> X0 = True, done
//	Else X0 = False
//	Store X0 back
//
// This is ~48 bytes. Not tiny but no shim call.
func emitNOTArm64Inline(cb *codeBuf, a, b uint8) {
	nilBits := uint64(value.Nil)
	falseBits := uint64(value.False)
	trueBits := uint64(value.True)

	// ldr X0, [X26 + B*8]
	cb.emit(jitarm64.EmitLdrXtFromXnDisp(nil, 0, regX26, uint16(b)*8))
	// Assume falsy; will overwrite if turns out to be truthy.
	// mov X2, #True (16 bytes)
	cb.emit(jitarm64.EmitMovXdImm64(nil, 2, trueBits))
	// mov X3, #False (16 bytes)
	cb.emit(jitarm64.EmitMovXdImm64(nil, 3, falseBits))
	// mov X4, #Nil (16 bytes)
	cb.emit(jitarm64.EmitMovXdImm64(nil, 4, nilBits))

	// cmp X0, X4 (nil)
	cb.emit(jitarm64.EmitCmpXnXm(nil, 0, 4))
	// b.eq +N (skip false-compare and store nil-case-true directly)
	// Layout:
	//   [pos0] b.eq +M  -> jump to `mov X0, X2; str; end`
	//   [pos1] cmp X0, X3 (false)
	//   [pos2] b.eq +K  -> jump to `mov X0, X2; str; end`
	//   [pos3] mov X0, X3          ; not-falsy result = False (which we
	//                                already have in X3)
	//   [pos4] b +L to end
	//   [pos5] mov X0, X2          ; falsy result = True
	//   [pos6] end: str X0, [X26+A*8]
	//
	// Fixed 4-byte instructions make offsets predictable.

	// Save the position to patch b.eq later.
	beq1Off := int32(len(cb.bytes))
	cb.emit(jitarm64.EmitBCond(nil, jitarm64.CondEQ, 0)) // placeholder

	// cmp X0, X3 (false)
	cb.emit(jitarm64.EmitCmpXnXm(nil, 0, 3))
	beq2Off := int32(len(cb.bytes))
	cb.emit(jitarm64.EmitBCond(nil, jitarm64.CondEQ, 0)) // placeholder

	// Truthy path: R(A) := False (X3 already holds False)
	// mov X0, X3
	cb.emit(jitarm64.EmitMovXdFromXn(nil, 0, 3))
	// b +to_end
	bEndOff := int32(len(cb.bytes))
	cb.emit([]byte{0x00, 0x00, 0x00, 0x14}) // placeholder b

	// Falsy path: R(A) := True (X2 holds True)
	falsyLabelOff := int32(len(cb.bytes))
	cb.emit(jitarm64.EmitMovXdFromXn(nil, 0, 2))

	// End: store back.
	endLabelOff := int32(len(cb.bytes))
	cb.emit(jitarm64.EmitStrXtToXnDisp(nil, 0, regX26, uint16(a)*8))

	// Patch fixups.
	// b.eq (nil case) -> falsyLabelOff
	{
		wordDisp := (falsyLabelOff - beq1Off) / 4
		insn := uint32(cb.bytes[beq1Off]) | uint32(cb.bytes[beq1Off+1])<<8 |
			uint32(cb.bytes[beq1Off+2])<<16 | uint32(cb.bytes[beq1Off+3])<<24
		insn &= 0xFF00001F
		insn |= (uint32(wordDisp) & 0x7FFFF) << 5
		cb.bytes[beq1Off] = byte(insn)
		cb.bytes[beq1Off+1] = byte(insn >> 8)
		cb.bytes[beq1Off+2] = byte(insn >> 16)
		cb.bytes[beq1Off+3] = byte(insn >> 24)
	}
	// b.eq (false case) -> falsyLabelOff
	{
		wordDisp := (falsyLabelOff - beq2Off) / 4
		insn := uint32(cb.bytes[beq2Off]) | uint32(cb.bytes[beq2Off+1])<<8 |
			uint32(cb.bytes[beq2Off+2])<<16 | uint32(cb.bytes[beq2Off+3])<<24
		insn &= 0xFF00001F
		insn |= (uint32(wordDisp) & 0x7FFFF) << 5
		cb.bytes[beq2Off] = byte(insn)
		cb.bytes[beq2Off+1] = byte(insn >> 8)
		cb.bytes[beq2Off+2] = byte(insn >> 16)
		cb.bytes[beq2Off+3] = byte(insn >> 24)
	}
	// b (falsy skip) -> endLabelOff
	{
		wordDisp := (endLabelOff - bEndOff) / 4
		insn := uint32(0x14000000) | (uint32(wordDisp) & 0x03FFFFFF)
		cb.bytes[bEndOff] = byte(insn)
		cb.bytes[bEndOff+1] = byte(insn >> 8)
		cb.bytes[bEndOff+2] = byte(insn >> 16)
		cb.bytes[bEndOff+3] = byte(insn >> 24)
	}
}

// emitFORPREPArm64Inline emits inline FORPREP: R(A) := R(A) - R(A+2);
// jmp to FORLOOP block. Assumes three slots are numbers.
func emitFORPREPArm64Inline(cb *codeBuf, a uint8, targetBB int) {
	// ldr X0, [X26 + A*8]     (index)
	cb.emit(jitarm64.EmitLdrXtFromXnDisp(nil, 0, regX26, uint16(a)*8))
	// fmov D0, X0
	cb.emit(jitarm64.EmitFmovDdFromXn(nil, 0, 0))
	// ldr X0, [X26 + (A+2)*8] (step)
	cb.emit(jitarm64.EmitLdrXtFromXnDisp(nil, 0, regX26, uint16(a+2)*8))
	// fmov D1, X0
	cb.emit(jitarm64.EmitFmovDdFromXn(nil, 1, 0))
	// fsub D0, D0, D1
	cb.emit(jitarm64.EmitFsubDdDnDm(nil, 0, 0, 1))
	// fmov X0, D0
	cb.emit(jitarm64.EmitFmovXdFromDn(nil, 0, 0))
	// str X0, [X26 + A*8]
	cb.emit(jitarm64.EmitStrXtToXnDisp(nil, 0, regX26, uint16(a)*8))
	// b targetBB (rel26 fixup)
	emitJMPArm64Fixup(cb, targetBB)
}

// emitFORLOOPArm64Inline emits inline FORLOOP back-edge (mirror of
// amd64 emitFORLOOP).
//
// Semantics:
//
//	R(A) += R(A+2)
//	if step > 0: cond = R(A) <= R(A+1)
//	else:        cond = R(A) >= R(A+1)
//	if cond: R(A+3) := R(A); jmp succBack
//	else:    jmp succOut
//
// arm64 encoding: fixed-4-byte instructions. Layout is straightforward
// forward-branch on inequality.
func emitFORLOOPArm64Inline(cb *codeBuf, a uint8, succBack, succOut int) {
	// ldr X0, [X26 + A*8]; fmov D0, X0        ; D0 = idx
	cb.emit(jitarm64.EmitLdrXtFromXnDisp(nil, 0, regX26, uint16(a)*8))
	cb.emit(jitarm64.EmitFmovDdFromXn(nil, 0, 0))
	// ldr X0, [X26 + (A+2)*8]; fmov D2, X0    ; D2 = step
	cb.emit(jitarm64.EmitLdrXtFromXnDisp(nil, 0, regX26, uint16(a+2)*8))
	cb.emit(jitarm64.EmitFmovDdFromXn(nil, 2, 0))
	// fadd D0, D0, D2
	cb.emit(jitarm64.EmitFaddDdDnDm(nil, 0, 0, 2))
	// fmov X0, D0; str X0, [X26+A*8]          ; write back idx
	cb.emit(jitarm64.EmitFmovXdFromDn(nil, 0, 0))
	cb.emit(jitarm64.EmitStrXtToXnDisp(nil, 0, regX26, uint16(a)*8))
	// ldr X0, [X26 + (A+1)*8]; fmov D1, X0    ; D1 = limit
	cb.emit(jitarm64.EmitLdrXtFromXnDisp(nil, 0, regX26, uint16(a+1)*8))
	cb.emit(jitarm64.EmitFmovDdFromXn(nil, 1, 0))
	// mov X3, #0; fmov D3, X3                 ; D3 = 0
	cb.emit(jitarm64.EmitMovXdImm64(nil, 3, 0))
	cb.emit(jitarm64.EmitFmovDdFromXn(nil, 3, 3))
	// fcmpe D2, D3                            ; step vs 0
	cb.emit(jitarm64.EmitFcmpeDnDm(nil, 2, 3))
	// b.gt stepPositive
	//
	// Layout (each instruction is 4 bytes):
	//
	//   [pos_A]  b.gt +N     (to stepPositive)
	//   step<=0 block:
	//   [pos_A + 4]  fcmpe D0, D1
	//   [pos_A + 8]  b.ge condTrue
	//   [pos_A +12]  b     condFalse
	//   stepPositive:
	//   [pos_A +16]  fcmpe D1, D0    ; note: compare limit vs idx
	//   [pos_A +20]  b.ge condTrue
	//   [pos_A +24]  b     condFalse
	//   condTrue:
	//   [pos_A +28]  fmov X0, D0
	//   [pos_A +32]  str X0, [X26 + (A+3)*8]
	//   [pos_A +36]  b succBack (rel26 fixup)
	//   condFalse:
	//   [pos_A +40]  b succOut (rel26 fixup)

	posA := int32(len(cb.bytes))
	// b.gt +4 -> stepPositive (pos_A+16). wordDisp = (16-0)/4 = 4.
	// b.cond imm19 field = wordDisp = 4.
	cb.emit(jitarm64.EmitBCond(nil, jitarm64.CondGT, 4))
	// step<=0 block:
	// fcmpe D0, D1
	cb.emit(jitarm64.EmitFcmpeDnDm(nil, 0, 1))
	// b.ge +N -> condTrue (pos_A+28). Current PC (after emit) = pos_A+8;
	// but for b.cond, imm19 = (target - PC_of_b.cond) / 4 = (28 - 8)/4 = 5.
	cb.emit(jitarm64.EmitBCond(nil, jitarm64.CondGE, 5))
	// b -> condFalse (pos_A+40). PC_of_b = pos_A+12. imm26 = (40-12)/4 = 7.
	cb.emit([]byte{0x07, 0x00, 0x00, 0x14})
	// stepPositive:
	// fcmpe D1, D0
	cb.emit(jitarm64.EmitFcmpeDnDm(nil, 1, 0))
	// b.ge +N -> condTrue (pos_A+28). PC_of_b.cond = pos_A+20. imm19 = (28-20)/4 = 2.
	cb.emit(jitarm64.EmitBCond(nil, jitarm64.CondGE, 2))
	// b -> condFalse (pos_A+40). PC = pos_A+24. imm26 = (40-24)/4 = 4.
	cb.emit([]byte{0x04, 0x00, 0x00, 0x14})
	// condTrue:
	// fmov X0, D0
	cb.emit(jitarm64.EmitFmovXdFromDn(nil, 0, 0))
	// str X0, [X26 + (A+3)*8]
	cb.emit(jitarm64.EmitStrXtToXnDisp(nil, 0, regX26, uint16(a+3)*8))
	// b succBack (rel26 fixup)
	backOff := int32(len(cb.bytes))
	cb.emit([]byte{0x00, 0x00, 0x00, 0x14})
	cb.addFixupKind(backOff, backOff+4, succBack, fixupKindArm64B26)
	// condFalse:
	outOff := int32(len(cb.bytes))
	cb.emit([]byte{0x00, 0x00, 0x00, 0x14})
	cb.addFixupKind(outOff, outOff+4, succOut, fixupKindArm64B26)
	_ = posA
}

// inlineRawEqArm64 emits inline 64-bit bit-equality for EQ. Semantics
// same as amd64 inlineRawEq: reg-reg only (AnalyzeNative rejects K
// operands for EQ). Returns false only if the (unreachable) K case is
// hit.
func inlineRawEqArm64(cb *codeBuf, a uint8, b, c int, succExec, succSkip int) bool {
	if b >= 256 || c >= 256 {
		return false
	}
	// ldr X0, [X26 + B*8]
	cb.emit(jitarm64.EmitLdrXtFromXnDisp(nil, 0, regX26, uint16(b)*8))
	// ldr X1, [X26 + C*8]
	cb.emit(jitarm64.EmitLdrXtFromXnDisp(nil, 1, regX26, uint16(c)*8))
	// cmp X0, X1
	cb.emit(jitarm64.EmitCmpXnXm(nil, 0, 1))
	// Branch to succExec when condition matches A.
	//   A=0: match when NOT(B==C) -> CondNE (0x1)
	//   A=1: match when B==C      -> CondEQ (0x0)
	var cond uint8
	if a == 0 {
		cond = jitarm64.CondNE
	} else {
		cond = jitarm64.CondEQ
	}
	patchOff := cb.pos()
	cb.emit(jitarm64.EmitBCond(nil, cond, 0))
	cb.addFixupKind(patchOff, cb.pos(), succExec, fixupKindArm64Cond)
	emitJMPArm64Fixup(cb, succSkip)
	return true
}

// emitTESTArm64Inline emits `if Truthy(R(A)) != C then pc++` inline.
//
// Sequence:
//
//	ldr X0, [X26 + A*8]        ; R(A)
//	mov X4, #Nil               ; imm64
//	cmp X0, X4
//	b.eq notTruthy
//	mov X4, #False             ; imm64
//	cmp X0, X4
//	b.eq notTruthy
//	; truthy: pick succ based on C
//	b <succExec if C != 0 else succSkip>
//	; notTruthy:
//	b <succSkip if C != 0 else succExec>
func emitTESTArm64Inline(cb *codeBuf, a, c uint8, succExec, succSkip int) {
	nilBits := uint64(value.Nil)
	falseBits := uint64(value.False)

	// ldr X0, [X26+A*8]
	cb.emit(jitarm64.EmitLdrXtFromXnDisp(nil, 0, regX26, uint16(a)*8))
	// mov X4, Nil
	cb.emit(jitarm64.EmitMovXdImm64(nil, 4, nilBits))
	// cmp X0, X4
	cb.emit(jitarm64.EmitCmpXnXm(nil, 0, 4))
	// b.eq notTruthy (patch later)
	beq1Off := int32(len(cb.bytes))
	cb.emit(jitarm64.EmitBCond(nil, jitarm64.CondEQ, 0))
	// mov X4, False
	cb.emit(jitarm64.EmitMovXdImm64(nil, 4, falseBits))
	// cmp X0, X4
	cb.emit(jitarm64.EmitCmpXnXm(nil, 0, 4))
	// b.eq notTruthy
	beq2Off := int32(len(cb.bytes))
	cb.emit(jitarm64.EmitBCond(nil, jitarm64.CondEQ, 0))

	// truthy: pick succ based on C
	var truthySucc, notTruthySucc int
	if c != 0 {
		truthySucc = succExec
		notTruthySucc = succSkip
	} else {
		truthySucc = succSkip
		notTruthySucc = succExec
	}
	// b truthySucc (via fixup)
	emitJMPArm64Fixup(cb, truthySucc)

	// notTruthy label:
	notTruthyOff := int32(len(cb.bytes))
	emitJMPArm64Fixup(cb, notTruthySucc)

	// Patch beq1/beq2 to notTruthyOff.
	patchBCondArm64(cb, beq1Off, notTruthyOff)
	patchBCondArm64(cb, beq2Off, notTruthyOff)
}

// patchBCondArm64 patches an already-emitted B.cond (or CBNZ) at
// bufOff to branch to targetOff, computing rel19 = (target - patchPC) / 4.
func patchBCondArm64(cb *codeBuf, bufOff, targetOff int32) {
	wordDisp := (targetOff - bufOff) / 4
	insn := uint32(cb.bytes[bufOff]) | uint32(cb.bytes[bufOff+1])<<8 |
		uint32(cb.bytes[bufOff+2])<<16 | uint32(cb.bytes[bufOff+3])<<24
	insn &= 0xFF00001F
	insn |= (uint32(wordDisp) & 0x7FFFF) << 5
	cb.bytes[bufOff] = byte(insn)
	cb.bytes[bufOff+1] = byte(insn >> 8)
	cb.bytes[bufOff+2] = byte(insn >> 16)
	cb.bytes[bufOff+3] = byte(insn >> 24)
}

// emitTESTSETArm64Inline emits `if Truthy(R(B)) != C then pc++ else R(A) := R(B)`.
//
// Sequence:
//
//	ldr X0, [X26 + B*8]        ; R(B)
//	mov X4, #Nil
//	cmp X0, X4
//	b.eq notTruthy
//	mov X4, #False
//	cmp X0, X4
//	b.eq notTruthy
//	; truthy: R(A) := R(B); pick succ based on C
//	str X0, [X26 + A*8]
//	b <succExec if C != 0 else succSkip>
//	; notTruthy: skip R(A) write
//	b <succSkip if C != 0 else succExec>
func emitTESTSETArm64Inline(cb *codeBuf, a, b, c uint8, succExec, succSkip int) {
	nilBits := uint64(value.Nil)
	falseBits := uint64(value.False)

	cb.emit(jitarm64.EmitLdrXtFromXnDisp(nil, 0, regX26, uint16(b)*8))
	cb.emit(jitarm64.EmitMovXdImm64(nil, 4, nilBits))
	cb.emit(jitarm64.EmitCmpXnXm(nil, 0, 4))
	beq1Off := int32(len(cb.bytes))
	cb.emit(jitarm64.EmitBCond(nil, jitarm64.CondEQ, 0))
	cb.emit(jitarm64.EmitMovXdImm64(nil, 4, falseBits))
	cb.emit(jitarm64.EmitCmpXnXm(nil, 0, 4))
	beq2Off := int32(len(cb.bytes))
	cb.emit(jitarm64.EmitBCond(nil, jitarm64.CondEQ, 0))

	// truthy branch: store R(A) := R(B) then dispatch
	cb.emit(jitarm64.EmitStrXtToXnDisp(nil, 0, regX26, uint16(a)*8))
	var truthySucc, notTruthySucc int
	if c != 0 {
		truthySucc = succExec
		notTruthySucc = succSkip
	} else {
		truthySucc = succSkip
		notTruthySucc = succExec
	}
	emitJMPArm64Fixup(cb, truthySucc)

	// notTruthy branch: no store
	notTruthyOff := int32(len(cb.bytes))
	emitJMPArm64Fixup(cb, notTruthySucc)

	patchBCondArm64(cb, beq1Off, notTruthyOff)
	patchBCondArm64(cb, beq2Off, notTruthyOff)
}
