//go:build wangshu_p3

package wasm

// Translation main flow + 7 straight-line opcode emits (02-translation §3.1 + §6.2).
//
// **PW2 control-flow scope**: the relooper analysis layer (cfg.go/relooper.go)
// is built and verified, but the structured generation layer (arbitrary
// reducible CFG → nested block/loop + br depth) is left for PW3 to complete
// (by then there is end-to-end feedback verification with conditional jumps +
// loops). The PW2 translator only handles **single-basic-block Protos**
// (no jumps / pure straight-line + trailing RETURN) — this covers the minimal
// acceptable form of PW2's completion definition "5-op Proto lift byte-equal".
//
// Protos containing JMP but with more than one BB in the CFG: isStructurable
// returns false → translate returns unsupported → Compile returns error → P2
// falls back on that Proto (conservative, correct). PW3 uses the full relooper
// to unlock multi-BB.

import (
	"fmt"

	"github.com/Liam0205/wangshu/internal/bytecode"
)

// Wasm function local slot allocation (temporaries used inside the gibbous
// function body). The param $base occupies local 0; translation temporaries
// start at 1. The declaration order (module.go codeSectionEntry) must match
// this: 2×i64 + 1×i32 + 1×f64.
const (
	localBase = 0 // param $base i32
	localI64a = 1 // i64 temp a (load/store scratch / arithmetic operand vb)
	localI64b = 2 // i64 temp b (arithmetic operand vc)
	localI32  = 3 // i32 temp (helper status, etc.)
	localF64  = 4 // f64 temp (arithmetic result)
	localI32b = 5 // i32 temp b (PW5 table byte address)
	localI64c = 6 // i64 temp c (PW5 key / slot value scratch)

	// localSavedTop is the caller's snapshot for self-restoring top (PW10
	// zero-cross ③a). The function prologue reads the top mirror word once and
	// stores it (at this moment = this frame's base+MaxStack slot index, just
	// set by enterLuaFrame); after each fixed-arity (C≠0) CALL returns via
	// call_indirect, the top word is written back — the callee's emitReturn
	// fast path (③b) no longer restores the caller top, the caller restores it
	// itself. Stores the slot index (grow-safe: a callee's nested growStack
	// changes stackBaseW but the slot index is unchanged, avoiding stackBaseW
	// conversion).
	localSavedTop = 7

	// Compat aliases (PW2 straight-line opcodes use localTmp64/localTmp32).
	localTmp64 = localI64a
	localTmp32 = localI32
)

// translateError indicates a Proto cannot be translated by PW2 (control flow
// too complex / contains an unimplemented opcode form) — Compile returns
// unsupported based on this, and P2 falls back.
type translateError struct{ reason string }

func (e *translateError) Error() string { return e.reason }

// translate translates Proto.Code into Wasm function body bytes (excluding the
// local decls and the trailing end, which the module assembles and wraps).
// Returns (body, error).
//
// A single reachable BB takes the PW2/PW3 straight-line path; multiple BBs take
// the PW4 relooper structured generation.
func (c *Compiler) translate(proto *bytecode.Proto) ([]byte, error) {
	cfg := buildCFG(proto)
	reach := cfg.reachableBlocks()
	em := newEmitter()
	c.emitPrologue(em)

	if len(reach) == 1 {
		// Single reachable BB: straight-line translation (dead blocks — the
		// fallback RETURN after RETURN — are not emitted).
		entry := cfg.blocks[cfg.entry]
		for pc := entry.startPC; pc < entry.endPC; {
			skip, err := c.emitOpcode(em, proto, pc)
			if err != nil {
				return nil, err
			}
			pc += 1 + int32(skip) // skip CLOSURE's trailing pseudo-instructions
		}
		em.i32Const(0)
		em.ret()
		return em.bytes(), nil
	}

	// Multiple BBs: PW4 relooper structured generation.
	plan, err := buildStructPlan(cfg)
	if err != nil {
		return nil, &translateError{reason: err.Error()}
	}
	if err := c.emitStructured(em, proto, cfg, plan); err != nil {
		return nil, &translateError{reason: err.Error()}
	}
	// Fallback return 0 (in theory every exit BB has already emitted RETURN;
	// this defends the wasm validation "missing value at function end" — after
	// structured emission control flow may fall to the function body's end).
	em.i32Const(0)
	em.ret()
	return em.bytes(), nil
}

// emitPrologue emits the function entry prologue (PW10 zero-cross ③a):
// snapshot the top mirror word into localSavedTop.
//
//	(local.set $savedTop (i32.load offset=topAddr (i32.const 0)))
//
// At this moment (run entry, enterLuaFrame has just setTop(base+MaxStack)) the
// top word = this frame's base+MaxStack slot index, which is exactly the th.top
// that must be restored after each of this frame's fixed-arity CALLs returns.
// The caller self-restores based on this, so the callee's emitReturn fast path
// (③b) need not reach across the function to fetch caller.MaxStack / convert
// stackBaseW.
//
// **③a is behavior-neutral**: in this stage the callee still goes through
// helperReturn (DoReturn already restored the same value base+MaxStack), and
// the caller's write-back = writing the same value, purely idempotent; once ③b
// lands the callee no longer restores, and the caller's write-back becomes the
// sole restore point.
func (c *Compiler) emitPrologue(em *emitter) {
	em.i32Const(0)
	em.i32Load(c.host.TopAddr())
	em.localSet(localSavedTop)
}

// this layer handles based on successors and the scope stack). Terminator
// instructions (JMP / comparisons / FOR* / RETURN) do not emit control flow in
// emitOpcode — only this layer knows the successor BB's br depth.
func (c *Compiler) emitBlockBody(em *emitter, proto *bytecode.Proto, cfg *cfg, plan *structPlan, bb int, stack *[]scope) error {
	blk := cfg.blocks[bb]
	if blk.startPC >= blk.endPC {
		return nil
	}
	lastPC := blk.endPC - 1
	term := proto.Code[lastPC]
	termOp := bytecode.Op(term)

	// Straight-line prefix (all instructions before the terminator).
	for pc := blk.startPC; pc < lastPC; {
		skip, err := c.emitOpcode(em, proto, pc)
		if err != nil {
			return err
		}
		pc += 1 + int32(skip) // skip CLOSURE's trailing pseudo-instructions
	}

	switch termOp {
	case bytecode.RETURN:
		// Self-contained return, no successor edge.
		_, err := c.emitOpcode(em, proto, lastPC)
		return err

	case bytecode.TAILCALL:
		// Tail call reuses the frame (PW6-b): self-closing return (Lua
		// completes / host lands RETURN / ERR bubbles up).
		c.emitTailCall(em, proto.Code[lastPC], lastPC)
		return nil

	case bytecode.JMP:
		// Unconditional jump: emit the edge to the sole successor.
		return c.emitJmpTerm(em, cfg, plan, stack, bb)

	case bytecode.EQ, bytecode.LT, bytecode.LE, bytecode.TEST, bytecode.TESTSET:
		return c.emitCompareTerm(em, proto, cfg, plan, stack, bb, term, lastPC)

	case bytecode.FORPREP:
		return c.emitForPrepTerm(em, cfg, plan, stack, bb, lastPC)

	case bytecode.FORLOOP:
		return c.emitForLoopTerm(em, proto, cfg, plan, stack, bb, term, lastPC)

	case bytecode.TFORLOOP:
		return c.emitTForLoopTerm(em, cfg, plan, stack, bb, lastPC)

	default:
		// An ordinary op splits the BB because "the next instruction is a
		// leader" (single-successor fallthrough). Emit that op first, then
		// emit the fallthrough edge. (Ordinary op skip=0; CLOSURE never lands
		// in this branch — it is followed by pseudo-instructions, and its BB
		// boundary is after those pseudo-instructions, so CLOSURE is not a BB's
		// last instruction.)
		if _, err := c.emitOpcode(em, proto, lastPC); err != nil {
			return err
		}
		if len(blk.succs) == 1 {
			return c.emitEdge(em, cfg, plan, *stack, bb, blk.succs[0])
		}
		if len(blk.succs) == 0 {
			return nil
		}
		return &translateError{reason: fmt.Sprintf("p4: unexpected %d succs after %s", len(blk.succs), termOp)}
	}
}

// emitJmpTerm JMP terminator: sole successor (jumpTarget).
func (c *Compiler) emitJmpTerm(em *emitter, cfg *cfg, plan *structPlan, stack *[]scope, bb int) error {
	blk := cfg.blocks[bb]
	if len(blk.succs) != 1 {
		return &translateError{reason: fmt.Sprintf("p4: JMP BB %d has %d succs", bb, len(blk.succs))}
	}
	return c.emitEdge(em, cfg, plan, *stack, bb, blk.succs[0])
}

// emitOpcode translates one non-terminator straight-line instruction. Returns
// (skip, err): skip = the number of trailing instructions this instruction
// consumes additionally (CLOSURE is followed by SubNUps pseudo-instructions =
// data, not opcodes, which must be skipped and not translated); all other
// opcodes have skip=0. The caller steps pc by skip (pc += 1 + skip).
func (c *Compiler) emitOpcode(em *emitter, proto *bytecode.Proto, pc int32) (int, error) {
	ins := proto.Code[pc]
	op := bytecode.Op(ins)
	switch op {
	case bytecode.MOVE:
		c.emitMove(em, ins)
	case bytecode.LOADK:
		c.emitLoadK(em, proto, ins)
	case bytecode.LOADBOOL:
		c.emitLoadBool(em, proto, ins, pc)
	case bytecode.LOADNIL:
		c.emitLoadNil(em, ins)
	case bytecode.GETUPVAL:
		c.emitGetUpval(em, ins, pc)
	case bytecode.SETUPVAL:
		c.emitSetUpval(em, ins, pc)
	case bytecode.RETURN:
		c.emitReturn(em, ins, pc)
	case bytecode.TAILCALL:
		// Tail call on the single-BB path (after TAILCALL only dead-code
		// RETURN, reachableBlocks==1). The multi-BB path is dispatched by
		// emitBlockBody's terminator (both paths call emitTailCall,
		// self-closing return).
		c.emitTailCall(em, ins, pc)
	case bytecode.ADD, bytecode.SUB, bytecode.MUL, bytecode.DIV, bytecode.MOD, bytecode.POW:
		c.emitArith(em, proto, ins, pc)
	case bytecode.UNM:
		c.emitUnm(em, ins, pc)
	case bytecode.NOT:
		c.emitNot(em, ins)
	case bytecode.LEN:
		c.emitLen(em, ins, pc)
	case bytecode.CONCAT:
		c.emitConcat(em, ins, pc)
	case bytecode.GETGLOBAL:
		c.emitGetGlobal(em, proto, ins, pc)
	case bytecode.SETGLOBAL:
		c.emitSetGlobal(em, proto, ins, pc)
	case bytecode.GETTABLE:
		c.emitGetTable(em, proto, ins, pc)
	case bytecode.SETTABLE:
		c.emitSetTable(em, proto, ins, pc)
	case bytecode.SELF:
		c.emitSelf(em, proto, ins, pc)
	case bytecode.NEWTABLE:
		c.emitNewTable(em, ins, pc)
	case bytecode.SETLIST:
		c.emitSetList(em, ins, pc)
	case bytecode.CALL:
		c.emitCall(em, proto, ins, pc)
	case bytecode.CLOSE:
		c.emitClose(em, ins, pc)
	case bytecode.CLOSURE:
		// Followed by SubNUps[Bx] pseudo-instructions (MOVE/GETUPVAL,
		// describing upvalue capture) which are data, not opcodes; translation
		// skips them (makeClosure reads them inside the helper); return skip so
		// the caller steps pc.
		c.emitClosure(em, ins, pc)
		bx := bytecode.Bx(ins)
		if bx < len(proto.SubNUps) {
			return int(proto.SubNUps[bx]), nil
		}
		return 0, nil
	default:
		return 0, &translateError{reason: fmt.Sprintf("p3 PW3: opcode %s not implemented (pc=%d)", op, pc)}
	}
	return 0, nil
}

// loadRK pushes an RK operand (register R(rk) or constant K(rk-256)) onto the
// Wasm stack top (i64).
//   - register (rk<MaxK): i64.load offset=8*rk (base)
//   - constant (rk≥MaxK): i64.const constant raw u64 (string constants are
//     already rejected by SupportsAllOpcodes)
func (c *Compiler) loadRK(em *emitter, proto *bytecode.Proto, rk int) {
	if rk < bytecode.MaxK {
		em.localGet(localBase)
		em.i64Load(8 * uint32(rk))
		return
	}
	em.i64Const(uint64(proto.Consts[rk-bytecode.MaxK]))
}

// emitArith ADD/SUB/MUL/DIV/MOD/POW —— double-number fast path (f64 emitted
// directly inside Wasm + NaN canonicalization) + slow-path helper (02 §3.2.1).
//
//	vb := RK(B); vc := RK(C)
//	if IsNumber(vb) && IsNumber(vc):
//	  r := f64(vb) op f64(vc); canonicalizeNaN(r); R(A) := r
//	else:
//	  status := h_arith(base,pc,op,b,c,a); if status==1 return 1
func (c *Compiler) emitArith(em *emitter, proto *bytecode.Proto, ins bytecode.Instruction, pc int32) {
	a := uint32(bytecode.A(ins))
	b := bytecode.B(ins)
	cc := bytecode.C(ins)
	op := bytecode.Op(ins)

	// POW has no f64.pow instruction: the whole thing goes through the
	// slow-path helper (Go math.Pow, byte-equal), no fast path emitted
	// (02 §3.2.2: POW baseline goes through the helper, simplest).
	if op == bytecode.POW {
		c.emitArithSlow(em, op, b, cc, a, pc)
		return
	}

	// vb, vc → local
	c.loadRK(em, proto, b)
	em.localSet(localI64a)
	c.loadRK(em, proto, cc)
	em.localSet(localI64b)

	// IsNumber(vb) && IsNumber(vc): vb < qNanBoxBase && vc < qNanBoxBase
	em.localGet(localI64a)
	em.i64Const(qNanBoxBase)
	em.i64LtU()
	em.localGet(localI64b)
	em.i64Const(qNanBoxBase)
	em.i64LtU()
	em.i32And()
	em.ifVoid()
	// --- Fast path: f64 arithmetic ---
	c.emitArithFast(em, op, a)
	em.elseOp()
	// --- Slow path: h_arith ---
	c.emitArithSlow(em, op, b, cc, a, pc)
	em.end()
}

// emitArithSlow emits the arithmetic slow-path helper call:
// h_arith(base,pc,op,b,c,a)→status; status==1 then return 1 (error bubbles up,
// 04 §4.1).
func (c *Compiler) emitArithSlow(em *emitter, op bytecode.OpCode, b, cc int, a uint32, pc int32) {
	em.localGet(localBase)
	em.i32Const(pc)
	em.i32Const(int32(op))
	em.i32Const(int32(b))
	em.i32Const(int32(cc))
	em.i32Const(int32(a))
	em.call(helperArith)
	em.localTee(localI32)
	em.i32Const(1)
	em.raw(0x46) // i32.eq
	em.ifVoid()
	em.i32Const(1)
	em.ret()
	em.end()
}

// emitArithFast emits the double-number fast path: f64(vb) op f64(vc) →
// canonicalize → store R(A). Operands are in localI64a/localI64b (POW does not
// take this path, no f64.pow).
func (c *Compiler) emitArithFast(em *emitter, op bytecode.OpCode, a uint32) {
	switch op {
	case bytecode.MOD:
		// Lua MOD: a - floor(a/b)*b.
		em.localGet(localI64a)
		em.f64ReinterpretI64()
		em.localGet(localI64a)
		em.f64ReinterpretI64()
		em.localGet(localI64b)
		em.f64ReinterpretI64()
		em.f64Div()
		em.f64Floor()
		em.localGet(localI64b)
		em.f64ReinterpretI64()
		em.f64Mul()
		em.f64Sub() // (vb) - (floor(vb/vc)*vc)
	default:
		em.localGet(localI64a)
		em.f64ReinterpretI64()
		em.localGet(localI64b)
		em.f64ReinterpretI64()
		switch op {
		case bytecode.ADD:
			em.f64Add()
		case bytecode.SUB:
			em.f64Sub()
		case bytecode.MUL:
			em.f64Mul()
		case bytecode.DIV:
			em.f64Div()
		}
	}
	// canonicalizeNaN:if r != r then r = canonNaN
	em.localTee(localF64)
	em.localGet(localF64)
	em.f64Ne()
	em.ifVoid()
	em.i64Const(canonNaNU64)
	em.f64ReinterpretI64()
	em.localSet(localF64)
	em.end()
	// store R(A) = i64.reinterpret(r)
	em.localGet(localBase)
	em.localGet(localF64)
	em.i64ReinterpretF64()
	em.i64Store(8 * a)
}

// emitUnm UNM A B —— R(A) := -R(B) (02 §3.2.3).
// Fast path f64.neg + result guard; otherwise h_unm.
//
// Result guard (issue #107): f64.neg never produces a NEW NaN, but it
// flips canonNaN (0x7FF8...) into 0xFFF8_0000_0000_0000 — exactly
// value.Nil's bit pattern. `-(0%0)` on the unguarded fast path stored
// that Nil, and the next arithmetic op raised "attempt to perform
// arithmetic on a nil value". Mirror of the P4 emitUNM fix from the
// issue #37 port round (fuzz seed f7f0bb1a NaN-aliasing family): when
// the flipped bits land back in the tag space (>= qNanBoxBase), route
// to h_unm, whose host side canonicalizes via NumberValue. Fast-path
// condition = IsNumber(vb) && negged < qNanBoxBase.
func (c *Compiler) emitUnm(em *emitter, ins bytecode.Instruction, pc int32) {
	a := uint32(bytecode.A(ins))
	b := uint32(bytecode.B(ins))
	em.localGet(localBase)
	em.i64Load(8 * b)
	em.localSet(localI64a)
	// negged = i64.reinterpret(f64.neg(f64.reinterpret(vb))). Garbage
	// bits for boxed non-numbers are harmless — the combined condition
	// below routes those to the slow path anyway.
	em.localGet(localI64a)
	em.f64ReinterpretI64()
	em.f64Neg()
	em.i64ReinterpretF64()
	em.localSet(localI64b)
	// IsNumber(vb) && negged < qNanBoxBase
	em.localGet(localI64a)
	em.i64Const(qNanBoxBase)
	em.i64LtU()
	em.localGet(localI64b)
	em.i64Const(qNanBoxBase)
	em.i64LtU()
	em.i32And()
	em.ifVoid()
	// Fast path: store negged directly.
	em.localGet(localBase)
	em.localGet(localI64b)
	em.i64Store(8 * a)
	em.elseOp()
	// Slow path: h_unm(base,pc,b,a)
	em.localGet(localBase)
	em.i32Const(pc)
	em.i32Const(int32(b))
	em.i32Const(int32(a))
	em.call(helperUnm)
	em.localTee(localI32)
	em.i32Const(1)
	em.raw(0x46) // i32.eq
	em.ifVoid()
	em.i32Const(1)
	em.ret()
	em.end()
	em.end()
}

// emitNot NOT A B —— R(A) := not R(B) (02 §3.2.4, no metamethod).
// Truthy(v) = v != Nil && v != False;not Truthy → BoolValue.
func (c *Compiler) emitNot(em *emitter, ins bytecode.Instruction) {
	a := uint32(bytecode.A(ins))
	b := uint32(bytecode.B(ins))
	em.localGet(localBase)
	em.i64Load(8 * b)
	em.localSet(localI64a)
	// vt = (vb != Nil) && (vb != False)
	em.localGet(localI64a)
	em.i64Const(nilRawU64())
	em.i64Ne()
	em.localGet(localI64a)
	em.i64Const(falseRawU64())
	em.i64Ne()
	em.i32And()
	// if !vt then R(A)=True else R(A)=False
	em.i32Eqz()
	em.ifVoid()
	em.localGet(localBase)
	em.i64Const(trueRawU64())
	em.i64Store(8 * a)
	em.elseOp()
	em.localGet(localBase)
	em.i64Const(falseRawU64())
	em.i64Store(8 * a)
	em.end()
}

// emitLen LEN A B —— R(A) := #R(B) (02 §3.2.5). All go through h_len (string
// length / table border / error on other types — inlining is too complex, the
// helper reuses execute.go's LEN section).
func (c *Compiler) emitLen(em *emitter, ins bytecode.Instruction, pc int32) {
	a := uint32(bytecode.A(ins))
	b := uint32(bytecode.B(ins))
	em.localGet(localBase)
	em.i32Const(pc)
	em.i32Const(int32(b))
	em.i32Const(int32(a))
	em.call(helperLen)
	em.localTee(localI32)
	em.i32Const(1)
	em.raw(0x46) // i32.eq
	em.ifVoid()
	em.i32Const(1)
	em.ret()
	em.end()
}

// emitConcat CONCAT A B C —— R(A) := R(B)..…..R(C) (02 §3.2.6). All go through
// h_concat (reuses execute.go doConcat's full logic + safepoint).
func (c *Compiler) emitConcat(em *emitter, ins bytecode.Instruction, pc int32) {
	a := uint32(bytecode.A(ins))
	b := uint32(bytecode.B(ins))
	cc := uint32(bytecode.C(ins))
	em.localGet(localBase)
	em.i32Const(pc)
	em.i32Const(int32(a))
	em.i32Const(int32(b))
	em.i32Const(int32(cc))
	em.call(helperConcat)
	em.localTee(localI32)
	em.i32Const(1)
	em.raw(0x46) // i32.eq
	em.ifVoid()
	em.i32Const(1)
	em.ret()
	em.end()
}

// emitMove MOVE A B —— R(A) := R(B) (02 §3.1.1).
//
//	(i64.store offset=8*A (local.get $base)
//	  (i64.load offset=8*B (local.get $base)))
func (c *Compiler) emitMove(em *emitter, ins bytecode.Instruction) {
	a := uint32(bytecode.A(ins))
	b := uint32(bytecode.B(ins))
	em.localGet(localBase) // store addr base
	em.localGet(localBase) // load addr base
	em.i64Load(8 * b)      // load R(B)
	em.i64Store(8 * a)     // store R(A)
}

// emitLoadK LOADK A Bx —— R(A) := K(Bx) (02 §3.1.2).
// The constant value is known at compile time, baked into an i64.const
// immediate.
//
// **PW2 limitation**: string constants are State-private lazy interns (Nil
// placeholders in Proto.Consts, the real value is filled only at load time), so
// the GCRef cannot be obtained at compile time — LOADK containing a string
// constant is not yet supported (returns unsupported, P2 falls back).
// Number/bool/nil constants can be baked.
func (c *Compiler) emitLoadK(em *emitter, proto *bytecode.Proto, ins bytecode.Instruction) {
	bx := bytecode.Bx(ins)
	a := uint32(bytecode.A(ins))
	// String constant: at compile time it is a Nil placeholder (IsStringConst),
	// the real value is State-private and cannot be baked. This case should
	// already be caught by isCompilableConsts in SupportsAllOpcodes; defensive
	// here.
	raw := uint64(proto.Consts[bx])
	em.localGet(localBase)
	em.i64Const(raw)
	em.i64Store(8 * a)
}

// emitLoadBool LOADBOOL A B C —— R(A) := bool(B); if C≠0 then pc++ (02 §3.1.3).
//
// PW2 single-BB path: the "pc++" when C≠0 is control flow (splits the BB), so
// it never enters the single-BB path. When C=0 it is a pure assignment.
func (c *Compiler) emitLoadBool(em *emitter, proto *bytecode.Proto, ins bytecode.Instruction, pc int32) {
	a := uint32(bytecode.A(ins))
	b := bytecode.B(ins)
	var v uint64
	if b != 0 {
		v = trueRawU64()
	} else {
		v = falseRawU64()
	}
	em.localGet(localBase)
	em.i64Const(v)
	em.i64Store(8 * a)
	// The pc++ when C≠0 is handled by the CFG splitting the BB, the single-BB
	// path does not handle it (if it appears, translate's single-BB assumption
	// has been violated, but LOADBOOL C≠0 makes buildCFG split the BB, so it
	// won't reach here).
}

// emitLoadNil LOADNIL A B —— R(A..B) := nil (closed interval, 02 §3.1.4).
func (c *Compiler) emitLoadNil(em *emitter, ins bytecode.Instruction) {
	a := bytecode.A(ins)
	b := bytecode.B(ins)
	nilRaw := nilRawU64()
	for r := a; r <= b; r++ {
		em.localGet(localBase)
		em.i64Const(nilRaw)
		em.i64Store(8 * uint32(r))
	}
}

// emitGetUpval GETUPVAL A B —— R(A) := Upval(B) (02 §3.1.5, via helper).
func (c *Compiler) emitGetUpval(em *emitter, ins bytecode.Instruction, pc int32) {
	a := uint32(bytecode.A(ins))
	b := int32(bytecode.B(ins))
	// $vb = h_getupval(base, b)
	em.localGet(localBase)
	em.i32Const(b)
	em.call(helperGetUpval)
	// store R(A)
	em.localSet(localTmp64)
	em.localGet(localBase)
	em.localGet(localTmp64)
	em.i64Store(8 * a)
}

// emitSetUpval SETUPVAL A B —— Upval(B) := R(A) (02 §3.1.6, via helper).
func (c *Compiler) emitSetUpval(em *emitter, ins bytecode.Instruction, pc int32) {
	a := uint32(bytecode.A(ins))
	b := int32(bytecode.B(ins))
	// h_setupval(base, b, R(A))
	em.localGet(localBase)
	em.i32Const(b)
	em.localGet(localBase)
	em.i64Load(8 * a)
	em.call(helperSetUpval)
}

// emitReturn RETURN A B (02 §3.6.3). PW10 zero-cross ③b: fixed-arity return
// (B≠0 and nret≤8) emits a guarded fast path (frame teardown inside Wasm,
// avoiding the h_return cross), and any guard failure does a br fallback to
// helperReturn; variadic return (B==0 to top) / oversized nret only emit
// helperReturn.
//
// **Byte-for-byte mirror of DoReturn** (gibbous_host.go): nret=B-1 results
// R(A..) → starting at funcIdx, decrement ciDepth, write the caller base
// transfer word. **Does not touch top** (the caller self-restores via ③a
// savedTop), does not materialize pc (RETURN raises no error, later-popped
// frames no longer read it), no safepoint (the fast path has no allocation/GC
// window).
//
// **Guards** (any failure br $slow → helperReturn, byte-for-byte fallback):
//   - G5 ciDepth<2: no gibbous caller can be torn down directly (the outermost
//     frame's RETURN returns to crescent) → Go teardown. Must be checked first,
//     otherwise the later reads of the caller frame (depth-2) go out of bounds.
//   - G3 openGuard≠0: this frame has open upvalues, closeUpvals is non-no-op →
//     Go closes them.
//   - G2 caller frame word2 bit50==0: caller is non-gibbous (top/transfer word
//     must be handled by Go) → Go.
//   - G4 callee frame nresults≠nret: over/under supply / want-all
//     (nresults=-1) → Go adjusts.
func (c *Compiler) emitReturn(em *emitter, ins bytecode.Instruction, pc int32) {
	a := int32(bytecode.A(ins))
	b := int32(bytecode.B(ins))
	nret := b - 1
	if b != 0 && nret >= 0 && nret <= maxReturnFast {
		c.emitReturnFast(em, a, nret)
	}
	// Fallback (slow path / guard-miss landing point): h_return(base,pc,a,b).
	em.localGet(localBase)
	em.i32Const(pc)
	em.i32Const(a)
	em.i32Const(b)
	em.call(helperReturn)
	em.ret() // return status (h_return's return value)
}

// emitCIFrameAddr emits the "byte address of frame (depth + idxDelta)'s segment"
// onto the Wasm stack (PW10 zero-cross ③b). Address = load(ciSegBaseAddr) +
// (load(ciDepthAddr)+idxDelta)*ciFrameBytes. idxDelta is -1 (callee top frame)
// or -2 (caller). The segment base / depth are read fresh each time (the
// segment may be relocated, avoiding a cached dangling pointer).
func (c *Compiler) emitCIFrameAddr(em *emitter, idxDelta int32) {
	em.i32Const(0)
	em.i32Load(c.host.CISegBaseAddr()) // segBase byte base address
	em.i32Const(0)
	em.i32Load(c.host.CIDepthAddr()) // depth
	em.i32Const(idxDelta)
	em.i32Add() // depth + idxDelta
	em.i32Const(ciFrameBytes)
	em.i32Mul() // *32
	em.i32Add() // segBase + (depth+idxDelta)*32
}

// emitReturnFast emits the ③b guarded fast path (see emitReturn's doc). Inside
// block $slow: any guard br_if 0 lands at the block's end (= emitReturn's
// helperReturn fallback); if all pass, the fast-path body returns 0 and exits
// the function.
func (c *Compiler) emitReturnFast(em *emitter, a, nret int32) {
	em.block() // $slow: guard failure jumps to this block's end (depth 0)

	// G5: ciDepth < 2 → slow (no gibbous caller frame; must be checked first to
	// prevent the caller frame read from going out of bounds).
	em.i32Const(0)
	em.i32Load(c.host.CIDepthAddr())
	em.i32Const(2)
	em.i32LtS()
	em.brIf(0)

	// G3: openGuard ≠ 0 → slow (has open upvalues, closeUpvals is non-no-op).
	em.i32Const(0)
	em.i32Load(c.host.OpenGuardAddr())
	em.brIf(0)

	// G2: caller frame word2 bit50 (gibbous) clear → slow.
	c.emitCIFrameAddr(em, -2) // caller frame address
	em.i64Load(ciWord2Off)
	em.i64Const(ciGibbousBit)
	em.i64And()
	em.i64Eqz() // (callerW2 & bit50)==0 → 1
	em.brIf(0)

	// G4: callee frame nresults ([47:32]) ≠ nret → slow (includes want-all
	// nresults=-1).
	c.emitCIFrameAddr(em, -1) // callee frame address
	em.i64Load(ciWord2Off)
	em.i64Const(32)
	em.i64ShrU()
	em.i32WrapI64()
	em.i32Const(0xffff)
	em.i32And() // nresults 16-bit field
	em.i32Const(nret)
	em.i32Ne()
	em.brIf(0)

	// --- Fast-path body (byte-for-byte mirror of DoReturn: moveResults →
	// transfer word → ciDepth--) ---
	// moveResults: dstBase = funcIdx byte address = localBase - 8
	// (base = funcIdx+1).
	em.localGet(localBase)
	em.i32Const(8)
	em.i32Sub()
	em.localSet(localI32b) // dstBase
	// nret results: mem[dstBase + 8k] = mem[localBase + 8*(a+k)] (forward copy,
	// source is above the destination, no read-after-write corruption — same as
	// DoReturn for k:=0..nret ascending).
	for k := int32(0); k < nret; k++ {
		em.localGet(localI32b)
		em.localGet(localBase)
		em.i64Load(uint32(8 * (a + k)))
		em.i64Store(uint32(8 * k))
	}

	// Transfer word = (stackBaseW+caller.base)*8 = localBase + (callerBase -
	// calleeBase)*8 (both bases read the low-32 difference from their own
	// segment's word0, avoiding stackBaseW; upholds the R3 base-refresh
	// contract). Must be before ciDepth-- (emitCIFrameAddr reads depth fresh).
	em.i32Const(0) // i32.store address operand (base 0 + offset=ciTransferAddr)
	em.localGet(localBase)
	c.emitCIFrameAddr(em, -2) // caller frame
	em.i32Load(0)             // callerBase (word0 low 32)
	c.emitCIFrameAddr(em, -1) // callee frame
	em.i32Load(0)             // calleeBase
	em.i32Sub()               // callerBase - calleeBase
	em.i32Const(8)
	em.i32Mul() // *8
	em.i32Add() // localBase + (callerBase-calleeBase)*8
	em.i32Store(c.host.CITransferAddr())

	// ciDepth-- (popCallInfo; the fast path has no GC window, order is
	// GC-insensitive, placed last).
	em.i32Const(0) // store address operand
	em.i32Const(0)
	em.i32Load(c.host.CIDepthAddr())
	em.i32Const(1)
	em.i32Sub()
	em.i32Store(c.host.CIDepthAddr())

	// Fast path done: return 0 (OK status).
	em.i32Const(0)
	em.ret()

	em.end() // $slow end: landing here = guard miss, continue emitReturn's helperReturn fallback
}
