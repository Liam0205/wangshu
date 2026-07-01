//go:build wangshu_p4 && amd64

// translator_native.go - CFG-based native code emit path.
//
// This is the "real" PJ10 translator: takes any reducible Proto whose
// opcodes are all in the supported set, walks the CFG, emits per-op
// native amd64 code, and returns a bridge.GibbousCode whose Run just
// jumps into the mmap segment.
//
// vs translator.go (Go-side replay): that path only handles single-BB
// linear head-op shapes (via AnalyzeShape) and dispatches via
// PerOpCode.Run running the mmap `xor eax,eax; ret` stub then replaying
// side effects in Go. This new path handles arbitrary CFG shapes and
// actually executes the native emit bytes.
package peroptranslator

import (
	"errors"
	"fmt"
	"os"
	"unsafe"

	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/gibbous/jit"
	jitamd64 "github.com/Liam0205/wangshu/internal/gibbous/jit/amd64"
	"github.com/Liam0205/wangshu/internal/value"
)

// debugMinimalNative, when set, makes TranslateProtoNative emit only
// `xor eax, eax; ret` (3 bytes) instead of the full op sequence. Used
// to isolate whether a suspected crash is in the emitted bytes or in
// the trampoline entry/exit protocol.
var debugMinimalNative = os.Getenv("PJ10_NATIVE_MINIMAL") != ""

// nativeCode is a bridge.GibbousCode implementation for the native emit
// path. Owns the mmap page and calls into it via CallJITSpec.
type nativeCode struct {
	proto    *bytecode.Proto
	codePage *jitamd64.CodePage
	jitCtx   *jit.JITContext
	host     jit.P4HostState

	// retA / retB / retPC are baked at compile time from the sole
	// RETURN instruction in the Proto's CFG. When the native mmap
	// segment RETs with status 0, Run calls host.DoReturn(retPC, retA,
	// retB) to perform the frame teardown. This avoids emitting a shim
	// call from inside the mmap for RETURN, dodging the morestack /
	// stack unwinder incompatibility (see project memory).
	retA  int32
	retB  int32
	retPC int32
}

func (c *nativeCode) Proto() *bytecode.Proto { return c.proto }

func (c *nativeCode) Run(stack []uint64, base uint32) (status int32) {
	NativeRunCount.Add(1)
	// Defense in depth: if the mmap segment corrupts the Go runtime state
	// enough to trigger a fault on RET, catch it and report an error
	// instead of taking down the host process. This is a stopgap while
	// the root cause of the nested + concurrent crash is being tracked
	// (see [[project-pj10-native-longtask]]).
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
	// Refcount acquire, same protocol as p4Code.Run / PerOpCode.Run. See
	// internal/gibbous/jit/amd64/codepage_linux.go for the refcount +
	// deferred munmap rationale. This is the PJ10 native emit main
	// execution path, so the refcount protection is load-bearing for the
	// multi-State Dispose vs Run UAF closure.
	if !c.codePage.Enter() {
		if len(stack) > 0 {
			stack[0] = 1
		}
		return 1
	}
	defer c.codePage.Exit()
	// A1: single batched host call replaces five per-field getters.
	c.host.RefreshJitCtxAddrs(c.jitCtx, int32(base))
	// Snapshot Go G for helper-call safety.
	saveGoG(c.jitCtx.SavedGoGSlot())
	// Install host interface header so shims can reconstruct P4HostState.
	c.jitCtx.SetHostRef(hostIfaceHeader(c.host))

	jitCtxAddr := uintptr(unsafe.Pointer(c.jitCtx))
	vsBaseAddr := c.jitCtx.ValueStackBase()
	rawStatus := jitamd64.CallJITSpec(c.codePage.Addr(), jitCtxAddr, vsBaseAddr)
	status = int32(rawStatus)
	// Perform Go-side frame teardown via host.DoReturn on success.
	// Emitting host.DoReturn as a shim call from inside the mmap segment
	// crashes the Go stack unwinder under nested + concurrent load;
	// doing it here avoids the mmap-to-Go shim call entirely.
	if status == 0 {
		if drStatus := c.host.DoReturn(int32(base), c.retPC, c.retA, c.retB); drStatus != 0 {
			status = drStatus
		}
	}
	return status
}

func (c *nativeCode) PendingErr() error    { return nil }
func (c *nativeCode) Slot() (uint32, bool) { return 0, false }

// IsPJ10Native is a public marker method identifying this GibbousCode
// as the CFG-based PJ10 native emit path (as opposed to PerOpCode
// head-op replay or PJ0-PJ9 shape-spec templates). Callers use it to
// gate behavior that is only safe on the native path — most notably
// the tail-call gibbous dispatch in crescent/execute.go, which
// requires the callee to honor DoReturn's standard frame-teardown
// contract on a reused tail-call frame. PerOpCode's head-op replay
// assumes a fresh frame and doesn't compose with that lifecycle.
func (c *nativeCode) IsPJ10Native() bool { return true }

// Dispose releases the mmap'd code page. Safe to call multiple times
// and safe under concurrent Run in multi-State setups: CodePage.Dispose
// flips a disposed flag (blocking further Enter) and the refcount
// protocol defers the actual unix.Munmap until the last active Run's
// Exit. See internal/gibbous/jit/amd64/codepage_linux.go for the full
// protocol.
//
// Callers (bridge Proto teardown / recompile paths) should invoke this
// when they no longer need the compiled code — otherwise mmap pages
// accumulate for every recompile until process exit.
func (c *nativeCode) Dispose() {
	if c == nil || c.codePage == nil {
		return
	}
	_ = c.codePage.Dispose()
	c.codePage = nil
}

// hostIfaceHeader extracts the (itab, data) header from a P4HostState
// interface value. Same pattern as e2e_shim_ops_amd64_test.go's
// hostToIfaceHeader but callable from production code.
func hostIfaceHeader(h jit.P4HostState) [2]uintptr {
	return *(*[2]uintptr)(unsafe.Pointer(&h))
}

// AnalyzeNative reports whether the native emit path can handle a Proto:
// PreferNative reports whether Compiler should skip shape-spec fast
// paths and route this Proto directly to the native emitter.
//
// Native wins over shape-spec when the shape-spec fast paths can't
// optimize the body: the FORLOOP-with-body spec template only inlines
// 1- or 2-op reg-K bodies (see shapeInfo.hasBody / hasBody2), so a
// FORLOOP kernel with a 3+ op body falls back to per-op replay in
// shape-spec while native emits full inline SSE.
//
// Heuristic: there must be a non-entry reachable BB with >= 4 opcodes.
// "Non-entry" (BB.id != 0) excludes the FORPREP setup block whose ops
// are LOADK init + FORPREP; that block hits 4 ops even for empty for
// loops. Only counting non-entry BBs isolates the loop body, which is
// the shape shape-spec's body-inline template can't beat.
//
// Also require multi-BB CFG: single-BB Protos are the historical
// shape-spec spec-template use case (getter/setter/return-constant
// forms). Routing them to native breaks pre-existing tests that
// assert which spec fast path fires.
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
		// Skip entry BB (id 0): it's typically the FORPREP setup with
		// LOADK init + FORPREP terminator, ~4 ops even for empty loops.
		// The loop body BB (id >= 1) is what shape-spec's body-inline
		// template struggles with.
		if id > 0 && bb.endPC-bb.startPC >= 4 {
			hasBigBodyBB = true
		}
	}
	return live >= 2 && hasBigBodyBB
}

// AnalyzeNative reports whether the native emit path can handle a Proto:
func AnalyzeNative(proto *bytecode.Proto) bool {
	if proto == nil || len(proto.Code) == 0 {
		return false
	}
	// F7-a: string constants aren't inlined by LOADK (would need
	// arena-relative bake; deferred to a follow-up).
	for _, k := range proto.StringLitIdx {
		if k >= 0 {
			return false
		}
	}
	// Vararg functions aren't supported (permanent VARARG gate).
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
			// Arithmetic ops with RK-encoded B or C fall through to a
			// shim call in the current inline fast path; reject Protos
			// that have any such shape until inline RK is supported.
			switch op {
			case bytecode.ADD, bytecode.SUB, bytecode.MUL, bytecode.DIV,
				bytecode.LT, bytecode.LE:
				// Inline arith/compare fast paths require numeric K
				// operands. Reject Protos whose K would fall through.
				if bytecode.B(ins) >= 256 {
					kidx := bytecode.B(ins) - 256
					if kidx < 0 || kidx >= len(proto.Consts) {
						return false
					}
					if !value.IsNumber(value.Value(proto.Consts[kidx])) {
						return false
					}
				}
				if bytecode.C(ins) >= 256 {
					kidx := bytecode.C(ins) - 256
					if kidx < 0 || kidx >= len(proto.Consts) {
						return false
					}
					if !value.IsNumber(value.Value(proto.Consts[kidx])) {
						return false
					}
				}
			case bytecode.EQ:
				// inlineRawEq accepts any 64-bit-comparable K (numeric,
				// nil, bool). String constants are the only Lua 5.1
				// value where raw ptr-equal ≠ semantic equal *unless
				// interned* — the frontend interns strings so ptr-equal
				// == string-equal in practice, but for safety and to
				// dodge the string-loading path (arena-relative), reject
				// K operands here for now.
				if bytecode.B(ins) >= 256 || bytecode.C(ins) >= 256 {
					return false
				}
			}
		}
	}
	// Require exactly one RETURN in the reachable code. TranslateProtoNative
	// stashes retA/retB/retPC from that instruction on the codeBuf and
	// nativeCode.Run invokes host.DoReturn from the Go side after the
	// mmap segment returns.
	returnCount := 0
	for id, bb := range c.blocks {
		if !reach[id] {
			continue
		}
		for pc := bb.startPC; pc < bb.endPC; pc++ {
			if bytecode.Op(proto.Code[pc]) == bytecode.RETURN {
				returnCount++
			}
		}
	}
	if returnCount != 1 {
		return false
	}
	return true
}

// opSupported reports whether a given op is emit-covered by the native
// path.
//
// **Ultra-conservative gate for production wiring**: only ops that have
// NO shim call in their emitted sequence. Calling Go helpers from mmap
// crashes Go's stack unwinder under nested/concurrent load, so we avoid
// it entirely by:
//   - RETURN: emit `xor eax, eax; ret` inline; host.DoReturn is invoked
//     from Go side after CallJITSpec returns.
//   - Arithmetic: inline SSE fast path (inlineArithSSE) supports
//     reg-reg / reg-K / K-reg / K-K when the K operand is numeric.
//   - Compare: inline UCOMISd fast path for LT/LE with numeric operands;
//     inline raw 64-bit bit-equal for EQ (reg-reg only — AnalyzeNative
//     rejects K operands to dodge the arena-relative string const path).
//   - TEST / TESTSET: inline compare against Nil / False imm64 constants
//     with rel8 forward branches to a notTruthy label.
//   - FORPREP: inline `R(A) -= R(A+2); jmp FORLOOP` (assumes three slots
//     are numbers, matching the FORLOOP inline SSE fast path).
//
// Currently enabled with NO shim call in the emit output:
//
//	MOVE, LOADK (numeric consts only), LOADBOOL, LOADNIL,
//	ADD/SUB/MUL/DIV (reg-reg or numeric K operands),
//	NOT (inline compare),
//	EQ (reg-reg, inline 64-bit cmp),
//	LT/LE (inline UCOMISd compare),
//	TEST, TESTSET (inline Nil/False bit-compare),
//	JMP, FORPREP, FORLOOP,
//	RETURN
//
// **Excluded** because the emit would need a shim call:
//
//	GETUPVAL, SETUPVAL, LEN, CONCAT,
//	GETTABLE, SETTABLE, GETGLOBAL, SETGLOBAL, SELF, NEWTABLE, SETLIST,
//	CALL, TAILCALL, CLOSURE, CLOSE, TFORLOOP, MOD, POW, UNM
//
// AnalyzeNative additionally rejects Protos with non-numeric K operands
// on inline arithmetic / compare ops, and rejects EQ with any K operand.
func opSupported(op bytecode.OpCode) bool {
	switch op {
	case bytecode.MOVE, bytecode.LOADK, bytecode.LOADBOOL, bytecode.LOADNIL,
		bytecode.ADD, bytecode.SUB, bytecode.MUL, bytecode.DIV,
		bytecode.NOT,
		bytecode.EQ, bytecode.LT, bytecode.LE,
		bytecode.TEST, bytecode.TESTSET,
		bytecode.JMP, bytecode.FORPREP, bytecode.FORLOOP,
		bytecode.RETURN:
		return true
	default:
		return false
	}
}

// TranslateProtoNative is the entry called from init() to route native
// compile requests through the CFG + emit pipeline.
func TranslateProtoNative(proto *bytecode.Proto, host jit.P4HostState) (*nativeCode, error) {
	if host == nil {
		return nil, errors.New("peroptranslator: nil P4HostState")
	}
	if !AnalyzeNative(proto) {
		return nil, errors.New("peroptranslator: proto not supported by native path")
	}
	c := buildCFG(proto)
	buf := newCodeBuf(len(c.blocks))
	// Build a raw uint64 constant table so LOADK can bake the immediate
	// directly. Non-string constants (numbers, bool, nil) are already
	// nan-boxed in proto.Consts. AnalyzeNative rejected string consts.
	consts := make([]uint64, len(proto.Consts))
	for i, v := range proto.Consts {
		consts[i] = uint64(v)
	}
	buf.proto = &codeBufProto{Consts: consts}

	// DEBUG: emit just `xor eax, eax; ret` to isolate whether the crash
	// is in the mmap segment content or the trampoline entry/exit.
	if debugMinimalNative {
		buf.emit([]byte{0x31, 0xC0, 0xC3})
		page, err := jitamd64.MmapCode(buf.bytes)
		if err != nil {
			return nil, err
		}
		return &nativeCode{
			proto:    proto,
			codePage: page,
			jitCtx:   jit.NewJITContext(),
			host:     host,
		}, nil
	}

	// Emit a prologue that initializes RBX = vsBase from jitCtx. This
	// makes the mmap segment self-contained: it can be called by any
	// trampoline (CallJITSpec or CallJITFull) as long as R15 = jitCtx.
	// The prologue is the first instructions before BB 0.
	//
	// mov rbx, [r15 + valueStackBaseOff]  (7 bytes)
	buf.emit([]byte{0x49, 0x8B, 0x9F,
		byte(jit.JITContextValueStackBaseOffset),
		byte(jit.JITContextValueStackBaseOffset >> 8),
		byte(jit.JITContextValueStackBaseOffset >> 16),
		byte(jit.JITContextValueStackBaseOffset >> 24)})

	// Emit each BB in id order (which happens to be startPC order thanks
	// to buildCFG's leader sort). This is not the same as rPostOrder but
	// works for reducible CFGs where the entry is BB 0.
	reach := c.reachableBlocks()
	for id, bb := range c.blocks {
		if !reach[id] {
			continue
		}
		if err := buf.bindLabel(id); err != nil {
			return nil, fmt.Errorf("bindLabel BB %d: %w", id, err)
		}
		if err := emitBB(buf, c, bb, id); err != nil {
			return nil, fmt.Errorf("emit BB %d: %w", id, err)
		}
	}

	if err := buf.resolveLabels(); err != nil {
		return nil, fmt.Errorf("resolveLabels: %w", err)
	}

	page, err := jitamd64.MmapCode(buf.bytes)
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

// emitBB emits one basic block: linear ops via emit_amd64.go /
// emit_ops_amd64.go, then the terminator with successor BB fixups.
func emitBB(buf *codeBuf, c *cfg, bb *basicBlock, bbID int) error {
	code := c.proto.Code
	lastPC := bb.endPC - 1
	if lastPC < bb.startPC {
		return nil
	}
	// Emit straight-line prefix (all instructions except the terminator).
	for pc := bb.startPC; pc < lastPC; pc++ {
		if err := emitLinearOp(buf, code[pc], pc); err != nil {
			return err
		}
	}
	// Emit terminator with successor edges.
	termIns := code[lastPC]
	return emitTerminator(buf, c, bb, bbID, termIns, lastPC)
}

// emitLinearOp emits one non-terminator opcode.
func emitLinearOp(buf *codeBuf, ins bytecode.Instruction, pc int32) error {
	op := bytecode.Op(ins)
	a := uint8(bytecode.A(ins))
	// b and c may be RK-encoded (0..511 range) for arithmetic /
	// comparison / table ops - keep them as int, not uint8.
	bReg := uint8(bytecode.B(ins))
	cReg := uint8(bytecode.C(ins))
	bRK := bytecode.B(ins)
	cRK := bytecode.C(ins)
	bx := bytecode.Bx(ins)

	switch op {
	case bytecode.MOVE:
		emitMOVE(buf, a, bReg)
	case bytecode.LOADK:
		if buf.proto == nil || int(bx) >= len(buf.proto.Consts) {
			return fmt.Errorf("LOADK: const idx %d out of range", bx)
		}
		emitLOADK(buf, a, buf.proto.Consts[bx])
	case bytecode.LOADBOOL:
		emitLOADBOOL_valueOnly(buf, a, bReg)
	case bytecode.LOADNIL:
		emitLOADNIL(buf, a, bReg)
	case bytecode.GETUPVAL:
		emitGETUPVAL(buf, a, bReg)
	case bytecode.SETUPVAL:
		emitSETUPVAL(buf, a, bReg)
	case bytecode.GETGLOBAL:
		emitGETGLOBAL(buf, pc, a, uint16(bx))
	case bytecode.SETGLOBAL:
		emitSETGLOBAL(buf, pc, a, uint16(bx))
	case bytecode.GETTABLE:
		emitGETTABLE(buf, pc, a, bReg, cRK)
	case bytecode.SETTABLE:
		emitSETTABLE(buf, pc, a, bRK, cRK)
	case bytecode.NEWTABLE:
		emitNEWTABLE(buf, pc, a, bReg, cReg)
	case bytecode.SELF:
		emitSELF(buf, pc, a, bReg, cRK)
	case bytecode.ADD, bytecode.SUB, bytecode.MUL, bytecode.DIV,
		bytecode.MOD, bytecode.POW:
		emitARITH(buf, op, pc, a, bRK, cRK)
	case bytecode.UNM:
		emitUNM(buf, pc, a, bReg)
	case bytecode.NOT:
		emitNOT(buf, a, bReg)
	case bytecode.LEN:
		emitLEN(buf, pc, a, bReg)
	case bytecode.CONCAT:
		emitCONCAT(buf, pc, a, bReg, cReg)
	case bytecode.CALL:
		emitCALL(buf, pc, a, bReg, cReg)
	case bytecode.CLOSURE:
		emitCLOSURE(buf, pc, a, uint16(bx))
	case bytecode.CLOSE:
		emitCLOSE(buf, pc, a)
	case bytecode.SETLIST:
		emitSETLIST(buf, pc, a, bReg, cReg)
	default:
		return fmt.Errorf("emitLinearOp: unsupported op %v at pc %d", op, pc)
	}
	return nil
}

// emitTerminator emits the last instruction of a BB with the appropriate
// branching / return / call semantics.
func emitTerminator(buf *codeBuf, c *cfg, bb *basicBlock, bbID int, ins bytecode.Instruction, pc int32) error {
	op := bytecode.Op(ins)
	a := uint8(bytecode.A(ins))
	b := uint8(bytecode.B(ins))
	cc := uint8(bytecode.C(ins))
	bRK := bytecode.B(ins)
	cRK := bytecode.C(ins)

	switch op {
	case bytecode.RETURN:
		// Emit `xor eax, eax; ret` inline - no shim call. host.DoReturn
		// is invoked from nativeCode.Run's Go side after CallJITSpec
		// returns. This avoids all shim-from-mmap risk for the RETURN
		// path (which is at the end of every function).
		cb := buf
		// Stash retA/retB/retPC on the codeBuf so TranslateProtoNative
		// can lift them into nativeCode fields.
		if cb.proto != nil {
			cb.proto.RetA = int32(a)
			cb.proto.RetB = int32(b)
			cb.proto.RetPC = pc
		}
		cb.emit([]byte{0x31, 0xC0, 0xC3}) // xor eax, eax; ret
	case bytecode.TAILCALL:
		emitTAILCALL(buf, pc, a, b, cc)
		emitRet(buf)
	case bytecode.JMP:
		if len(bb.succs) != 1 {
			return fmt.Errorf("JMP with %d succs at pc %d", len(bb.succs), pc)
		}
		emitJMP(buf, bb.succs[0])
	case bytecode.FORPREP:
		if len(bb.succs) != 1 {
			return fmt.Errorf("FORPREP with %d succs at pc %d", len(bb.succs), pc)
		}
		emitFORPREP(buf, pc, a, bb.succs[0])
	case bytecode.FORLOOP:
		if len(bb.succs) != 2 {
			return fmt.Errorf("FORLOOP with %d succs at pc %d", len(bb.succs), pc)
		}
		// succs[0] = back-edge target, succs[1] = fall-out.
		emitFORLOOP(buf, a, bb.succs[0], bb.succs[1])
	case bytecode.TFORLOOP:
		if len(bb.succs) != 2 {
			return fmt.Errorf("TFORLOOP with %d succs at pc %d", len(bb.succs), pc)
		}
		// succs[0] = pc+1 = fall-out; succs[1] = pc+2 = ... actually
		// for TFORLOOP: fall-through means jump-back; pc++ means exit.
		// Linksuccs adds pc+1 then pc+2, so succs[0]=back, succs[1]=out.
		emitTFORLOOP(buf, pc, a, cc, bb.succs[0], bb.succs[1])
	case bytecode.EQ, bytecode.LT, bytecode.LE:
		if len(bb.succs) != 2 {
			return fmt.Errorf("%v with %d succs at pc %d", op, len(bb.succs), pc)
		}
		// linkSuccs: succs[0] = pc+1 (execute JMP), succs[1] = pc+2 (skip).
		emitCompare(buf, op, pc, a, bRK, cRK, bb.succs[0], bb.succs[1])
	case bytecode.TEST:
		if len(bb.succs) != 2 {
			return fmt.Errorf("TEST with %d succs at pc %d", len(bb.succs), pc)
		}
		emitTEST(buf, a, cc, bb.succs[0], bb.succs[1])
	case bytecode.TESTSET:
		if len(bb.succs) != 2 {
			return fmt.Errorf("TESTSET with %d succs at pc %d", len(bb.succs), pc)
		}
		emitTESTSET(buf, a, b, cc, bb.succs[0], bb.succs[1])
	case bytecode.LOADBOOL:
		// LOADBOOL emits the value; the CFG makes it a terminator when C
		// determines the successor edge.
		emitLOADBOOL_valueOnly(buf, a, b)
		if len(bb.succs) != 1 {
			return fmt.Errorf("LOADBOOL terminator with %d succs at pc %d", len(bb.succs), pc)
		}
		emitJMP(buf, bb.succs[0])
	default:
		// Non-terminator that happens to sit at BB end (rare): just
		// emit as linear and add a jmp to fall-through if applicable.
		if err := emitLinearOp(buf, ins, pc); err != nil {
			return err
		}
		if len(bb.succs) == 1 {
			emitJMP(buf, bb.succs[0])
		}
	}
	return nil
}

// --- Constant table plumbing ---
//
// See codeBuf.proto (in codebuf.go). TranslateProtoNative wires the
// consts table in before emit; emitLinearOp for LOADK consults it.
