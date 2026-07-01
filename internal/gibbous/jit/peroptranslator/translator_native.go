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
}

func (c *nativeCode) Proto() *bytecode.Proto { return c.proto }

func (c *nativeCode) Run(stack []uint64, base uint32) int32 {
	if c.codePage == nil || c.jitCtx == nil || c.host == nil {
		if len(stack) > 0 {
			stack[0] = 1
		}
		return 1
	}
	// Refresh jitCtx addresses (arena may have grown between calls).
	c.jitCtx.SetArenaBase(c.host.ArenaBaseAddr())
	c.jitCtx.SetValueStackBase(c.host.ValueStackBaseAddr(int32(base)))
	c.jitCtx.SetCIDepthAddr(c.host.CIDepthHostAddr())
	c.jitCtx.SetCISegBaseAddr(c.host.CISegBaseHostAddr())
	c.jitCtx.SetTopAddr(c.host.TopHostAddr())
	// Snapshot Go G for helper-call safety.
	saveGoG(c.jitCtx.SavedGoGSlot())
	// Install host interface header so shims can reconstruct P4HostState.
	c.jitCtx.SetHostRef(hostIfaceHeader(c.host))

	jitCtxAddr := uintptr(unsafe.Pointer(c.jitCtx))
	vsBaseAddr := c.host.ValueStackBaseAddr(int32(base))
	status := jitamd64.CallJITSpec(c.codePage.Addr(), jitCtxAddr, vsBaseAddr)
	return int32(status)
}

func (c *nativeCode) PendingErr() error    { return nil }
func (c *nativeCode) Slot() (uint32, bool) { return 0, false }

// hostIfaceHeader extracts the (itab, data) header from a P4HostState
// interface value. Same pattern as e2e_shim_ops_amd64_test.go's
// hostToIfaceHeader but callable from production code.
func hostIfaceHeader(h jit.P4HostState) [2]uintptr {
	return *(*[2]uintptr)(unsafe.Pointer(&h))
}

// AnalyzeNative reports whether the native emit path can handle a Proto:
// reducible CFG + all live opcodes in the supported set + no string
// constants (F7 gate) + no VARARG. Called from perOpAnalyzer as the
// fallback when AnalyzeShape.ok is false.
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
			op := bytecode.Op(proto.Code[pc])
			if !opSupported(op) {
				return false
			}
		}
	}
	return true
}

// opSupported reports whether a given op is emit-covered by the native
// path.
//
// **Current gate is extremely conservative**: only the arithmetic-heavy
// loop ops. This is the target set for the V15b heavy_floatloop kernel:
// numeric-only workloads that AnalyzeShape rejects because they contain
// FORLOOP. Widening the gate to touch GETUPVAL/SETUPVAL/... requires
// resolving the "call helper from mmap crashes trampoline on RET"
// issue (likely Go-runtime morestack interaction; see
// [[project-pj10-native-longtask]]).
//
// Currently enabled:
//
//	MOVE, LOADK, LOADBOOL, LOADNIL,
//	ADD/SUB/MUL/DIV/MOD/POW, UNM, NOT, LEN,
//	JMP, FORPREP, FORLOOP,
//	RETURN (with B == 1 - no return values, purely for exit)
//
// The RETURN restriction avoids the shim-call path entirely for the
// exit -- for B == 1, we can just emit `xor eax, eax; ret` (status 0).
// TODO: extend once helper-call morestack issue is resolved.
func opSupported(op bytecode.OpCode) bool {
	switch op {
	case bytecode.MOVE, bytecode.LOADK, bytecode.LOADBOOL, bytecode.LOADNIL,
		bytecode.ADD, bytecode.SUB, bytecode.MUL, bytecode.DIV,
		bytecode.MOD, bytecode.POW,
		bytecode.UNM, bytecode.NOT, bytecode.LEN,
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
	return &nativeCode{
		proto:    proto,
		codePage: page,
		jitCtx:   jit.NewJITContext(),
		host:     host,
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
	b := uint8(bytecode.B(ins))
	c := uint8(bytecode.C(ins))
	bx := bytecode.Bx(ins)

	switch op {
	case bytecode.MOVE:
		emitMOVE(buf, a, b)
	case bytecode.LOADK:
		if buf.proto == nil || int(bx) >= len(buf.proto.Consts) {
			return fmt.Errorf("LOADK: const idx %d out of range", bx)
		}
		emitLOADK(buf, a, buf.proto.Consts[bx])
	case bytecode.LOADBOOL:
		emitLOADBOOL_valueOnly(buf, a, b)
	case bytecode.LOADNIL:
		emitLOADNIL(buf, a, b)
	case bytecode.GETUPVAL:
		emitGETUPVAL(buf, a, b)
	case bytecode.SETUPVAL:
		emitSETUPVAL(buf, a, b)
	case bytecode.GETGLOBAL:
		emitGETGLOBAL(buf, pc, a, uint16(bx))
	case bytecode.SETGLOBAL:
		emitSETGLOBAL(buf, pc, a, uint16(bx))
	case bytecode.GETTABLE:
		emitGETTABLE(buf, pc, a, b, c)
	case bytecode.SETTABLE:
		emitSETTABLE(buf, pc, a, b, c)
	case bytecode.NEWTABLE:
		emitNEWTABLE(buf, pc, a, b, c)
	case bytecode.SELF:
		emitSELF(buf, pc, a, b, c)
	case bytecode.ADD, bytecode.SUB, bytecode.MUL, bytecode.DIV,
		bytecode.MOD, bytecode.POW:
		emitARITH(buf, op, pc, a, b, c)
	case bytecode.UNM:
		emitUNM(buf, pc, a, b)
	case bytecode.NOT:
		emitNOT(buf, a, b)
	case bytecode.LEN:
		emitLEN(buf, pc, a, b)
	case bytecode.CONCAT:
		emitCONCAT(buf, pc, a, b, c)
	case bytecode.CALL:
		emitCALL(buf, pc, a, b, c)
	case bytecode.CLOSURE:
		emitCLOSURE(buf, pc, a, uint16(bx))
	case bytecode.CLOSE:
		emitCLOSE(buf, pc, a)
	case bytecode.SETLIST:
		emitSETLIST(buf, pc, a, b, c)
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

	switch op {
	case bytecode.RETURN:
		emitRETURN(buf, pc, a, b)
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
		emitCompare(buf, op, pc, a, b, cc, bb.succs[0], bb.succs[1])
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
