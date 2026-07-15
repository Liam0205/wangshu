//go:build wangshu_p3

package wasm

// PW4 control-flow terminator emission: compares (EQ/LT/LE/TEST/TESTSET),
// FORPREP, FORLOOP. These are BB-terminating instructions; the successor edges
// are computed by the structured-generation layer from the scope stack as br
// depths (02 §3.3/§3.5).
//
// "compare + JMP merge" (02 §3.3.1): a compare instruction is always followed by
// a JMP. The CFG splits the compare BB into two successors: succExec (pc+1 = the
// BB holding the adjacent JMP; falling into it executes the JMP and jumps to the
// JMP target), and succSkip (pc+2 = the BB that skips the JMP). Interpreter
// semantics `if res != bool(A) then pc++`: res != boolA → skip the JMP
// (succSkip); res == boolA → execute the JMP (succExec).

import (
	"fmt"

	"github.com/Liam0205/wangshu/internal/bytecode"
)

// compareSuccs resolves the two successors of a compare BB: returns (succExec, succSkip).
//   - succExec: falls to lastPC+1 (executes the adjacent JMP)
//   - succSkip: falls to lastPC+2 (pc++ skips the JMP)
func (c *Compiler) compareSuccs(cfg *cfg, lastPC int32) (succExec, succSkip int, err error) {
	idExec, ok1 := cfg.pcToBB[lastPC+1]
	idSkip, ok2 := cfg.pcToBB[lastPC+2]
	if !ok1 || !ok2 {
		return 0, 0, fmt.Errorf("p4: compare at pc=%d missing succ BB (exec=%v skip=%v)", lastPC, ok1, ok2)
	}
	return idExec, idSkip, nil
}

// emitCompareTerm emits a compare terminator (EQ/LT/LE/TEST/TESTSET) + the
// conditional edges (02 §3.3).
func (c *Compiler) emitCompareTerm(em *emitter, proto *bytecode.Proto, cfg *cfg, plan *structPlan, stack *[]scope, bb int, ins bytecode.Instruction, lastPC int32) error {
	op := bytecode.Op(ins)
	a := bytecode.A(ins)
	succExec, succSkip, err := c.compareSuccs(cfg, lastPC)
	if err != nil {
		return err
	}

	if op == bytecode.TESTSET {
		return c.emitTestSetTerm(em, cfg, ins, plan, stack, bb, succExec, succSkip)
	}

	// Compute the compare result → localI32 (0/1).
	// boolField is the byte that the result is compared against:
	//   - EQ/LT/LE: A (Lua reference: `if (RK(B) <op> RK(C)) ~= A then pc++`)
	//   - TEST:     C (Lua reference: `if not (R(A) <=> C) then pc++`)
	boolField := a
	switch op {
	case bytecode.TEST:
		c.emitTruthy(em, bytecode.A(ins)) // Truthy(R(A)) → localI32
		boolField = bytecode.C(ins)
	case bytecode.LT, bytecode.LE, bytecode.EQ:
		if e := c.emitNumCompareOrHelper(em, proto, ins, lastPC); e != nil {
			return e
		}
	default:
		return fmt.Errorf("p4: unexpected compare op %s", op)
	}

	// if (vt != bool(boolField)) then skip the JMP (succSkip) else execute the JMP (succExec)
	em.localGet(localI32)
	em.i32Const(boolToI32(boolField))
	em.i32Ne()
	em.ifVoid()
	*stack = append(*stack, scope{kind: scIf, target: -1})
	if e := c.emitEdge(em, cfg, plan, *stack, bb, succSkip); e != nil {
		return e
	}
	em.elseOp()
	if e := c.emitEdge(em, cfg, plan, *stack, bb, succExec); e != nil {
		return e
	}
	em.end()
	*stack = (*stack)[:len(*stack)-1]
	return nil
}

// emitTestSetTerm TESTSET A B C (02 §3.3.5): Truthy(R(B))==bool(C) → R(A):=R(B),
// fall to succExec (execute the JMP); otherwise skip the JMP (succSkip).
func (c *Compiler) emitTestSetTerm(em *emitter, cfg *cfg, ins bytecode.Instruction, plan *structPlan, stack *[]scope, bb, succExec, succSkip int) error {
	a := uint32(bytecode.A(ins))
	b := uint32(bytecode.B(ins))
	cc := bytecode.C(ins)
	// vb := R(B); vt := Truthy(vb)
	em.localGet(localBase)
	em.i64Load(8 * b)
	em.localSet(localI64a)
	c.emitTruthyOf(em, localI64a) // → localI32
	// if (vt == bool(C)) then R(A):=R(B); execute the JMP else skip the JMP
	em.localGet(localI32)
	em.i32Const(boolToI32(cc))
	em.raw(0x46) // i32.eq
	em.ifVoid()
	*stack = append(*stack, scope{kind: scIf, target: -1})
	em.localGet(localBase)
	em.localGet(localI64a)
	em.i64Store(8 * a)
	if e := c.emitEdge(em, cfg, plan, *stack, bb, succExec); e != nil {
		return e
	}
	em.elseOp()
	if e := c.emitEdge(em, cfg, plan, *stack, bb, succSkip); e != nil {
		return e
	}
	em.end()
	*stack = (*stack)[:len(*stack)-1]
	return nil
}

// emitTruthy computes Truthy(R(a)) → localI32 (register-reading variant).
func (c *Compiler) emitTruthy(em *emitter, a int) {
	em.localGet(localBase)
	em.i64Load(8 * uint32(a))
	em.localSet(localI64a)
	c.emitTruthyOf(em, localI64a)
}

// emitTruthyOf computes Truthy(local) → localI32: v != Nil && v != False.
func (c *Compiler) emitTruthyOf(em *emitter, vlocal uint32) {
	em.localGet(vlocal)
	em.i64Const(nilRawU64())
	em.i64Ne()
	em.localGet(vlocal)
	em.i64Const(falseRawU64())
	em.i64Ne()
	em.i32And()
	em.localSet(localI32)
}

// emitNumCompareOrHelper LT/LE/EQ: two-number fast path (f64 compare) + slow-path
// helper. The result (0/1) is left in localI32; on slow-path error, return 1
// (status bubbles up).
func (c *Compiler) emitNumCompareOrHelper(em *emitter, proto *bytecode.Proto, ins bytecode.Instruction, pc int32) error {
	op := bytecode.Op(ins)
	b := bytecode.B(ins)
	cc := bytecode.C(ins)
	c.loadRK(em, proto, b)
	em.localSet(localI64a)
	c.loadRK(em, proto, cc)
	em.localSet(localI64b)
	// IsNumber(vb) && IsNumber(vc)
	em.localGet(localI64a)
	em.i64Const(qNanBoxBase)
	em.i64LtU()
	em.localGet(localI64b)
	em.i64Const(qNanBoxBase)
	em.i64LtU()
	em.i32And()
	em.ifVoid()
	// fast path: f64 compare → localI32
	em.localGet(localI64a)
	em.f64ReinterpretI64()
	em.localGet(localI64b)
	em.f64ReinterpretI64()
	switch op {
	case bytecode.LT:
		em.f64Lt()
	case bytecode.LE:
		em.f64Le()
	case bytecode.EQ:
		em.f64Eq()
	}
	em.localSet(localI32)
	em.elseOp()
	// slow path: EQ first checks raw-bit equality then h_eq; LT/LE go straight to h_compare.
	if op == bytecode.EQ {
		c.emitEqSlow(em, b, cc, pc)
	} else {
		em.localGet(localBase)
		em.i32Const(pc)
		em.i32Const(int32(op))
		em.i32Const(int32(b))
		em.i32Const(int32(cc))
		em.call(helperCompare)
		c.emitUnpackCompare(em)
	}
	em.end()
	return nil
}

// emitEqSlow EQ slow path for non-two-number operands: raw-bit equality (same
// GCRef/bool/nil) is directly true; otherwise go through h_eq (__eq metamethod,
// only for two tables). Result → localI32.
func (c *Compiler) emitEqSlow(em *emitter, b, cc int, pc int32) {
	// if vb == vc (raw) then localI32 := 1 else h_eq
	em.localGet(localI64a)
	em.localGet(localI64b)
	em.i64Eq()
	em.ifVoid()
	em.i32Const(1)
	em.localSet(localI32)
	em.elseOp()
	em.localGet(localBase)
	em.i32Const(pc)
	em.i32Const(int32(b))
	em.i32Const(int32(cc))
	em.call(helperEq)
	c.emitUnpackCompare(em)
	em.end()
}

// emitUnpackCompare unpacks the packed return of h_compare/h_eq (i32 on top of
// stack): bit1=error → return 1; bit0 → localI32.
func (c *Compiler) emitUnpackCompare(em *emitter) {
	em.localTee(localI32)
	em.i32Const(2)
	em.i32And()
	em.ifVoid()
	em.i32Const(1)
	em.ret()
	em.end()
	em.localGet(localI32)
	em.i32Const(1)
	em.i32And()
	em.localSet(localI32)
}

// emitForPrepTerm FORPREP: validate the three slots + pre-decrement via
// h_forprep, then jump to FORLOOP (the only successor).
func (c *Compiler) emitForPrepTerm(em *emitter, cfg *cfg, plan *structPlan, stack *[]scope, bb int, lastPC int32) error {
	a := bytecode.A(cfg.proto.Code[lastPC])
	em.localGet(localBase)
	em.i32Const(lastPC)
	em.i32Const(int32(a))
	em.call(helperForPrep)
	em.localTee(localI32)
	em.i32Const(1)
	em.raw(0x46) // i32.eq
	em.ifVoid()
	em.i32Const(1)
	em.ret()
	em.end()
	// Jump to FORLOOP (the only successor).
	blk := cfg.blocks[bb]
	if len(blk.succs) != 1 {
		return fmt.Errorf("p4: FORPREP BB %d has %d succs", bb, len(blk.succs))
	}
	return c.emitEdge(em, cfg, plan, *stack, bb, blk.succs[0])
}

// emitForLoopTerm FORLOOP (02 §3.5.2): the three slots are already normalized to
// numbers by FORPREP, so the fast path is all f64. idx+=step; check bounds by
// direction; on continue, write back idx/v + back-edge safepoint + br back to
// loop; otherwise fall out of the loop (exit).
func (c *Compiler) emitForLoopTerm(em *emitter, proto *bytecode.Proto, cfg *cfg, plan *structPlan, stack *[]scope, bb int, ins bytecode.Instruction, lastPC int32) error {
	a := uint32(bytecode.A(ins))
	// Successors: back-jump (jumpTarget) = loop body; fall-out (lastPC+1) = exit.
	idBody, ok1 := cfg.pcToBB[lastPC+1+int32(bytecode.SBx(ins))]
	idOut, ok2 := cfg.pcToBB[lastPC+1]
	if !ok1 || !ok2 {
		return fmt.Errorf("p4: FORLOOP at pc=%d missing succ (body=%v out=%v)", lastPC, ok1, ok2)
	}

	// idx = R(A) + R(A+2) → localF64
	em.localGet(localBase)
	em.i64Load(8 * a)
	em.f64ReinterpretI64()
	em.localGet(localBase)
	em.i64Load(8 * (a + 2))
	em.f64ReinterpretI64()
	em.f64Add()
	em.localSet(localF64)
	// cont = (step>=0) ? idx<=limit : idx>=limit → localI32
	c.emitForContinueTest(em, a)
	em.localGet(localI32)
	em.ifVoid()
	*stack = append(*stack, scope{kind: scIf, target: -1})
	// continue: write back R(A)=idx, R(A+3)=idx
	em.localGet(localBase)
	em.localGet(localF64)
	em.i64ReinterpretF64()
	em.i64Store(8 * a)
	em.localGet(localBase)
	em.localGet(localF64)
	em.i64ReinterpretF64()
	em.i64Store(8 * (a + 3))
	// br back to the loop header (back edge). The back-edge safepoint (GC +
	// step-budget accounting) is emitted uniformly by emitEdge in the scLoop
	// branch (the single choke point, covering all loop forms), so it is not
	// emitted separately here, to avoid double emission for FORLOOP.
	if e := c.emitEdge(em, cfg, plan, *stack, bb, idBody); e != nil {
		return e
	}
	em.end()
	*stack = (*stack)[:len(*stack)-1]
	// Do not continue: falling out of the loop = exiting → idOut. In structured
	// form FORLOOP is the loop tail, so falling out means exiting the loop; if
	// idOut is not the fallthrough, a br is needed (handled by emitEdge).
	return c.emitEdge(em, cfg, plan, *stack, bb, idOut)
}

// emitForContinueTest computes the numeric-for continue condition → localI32:
// (step>0) ? idx<=limit : idx>=limit. idx is in localF64; step/limit are read
// from the stack slots on the fly.
//
// The step branch must use a strict `>` (PUC 5.1 lvm.c `luai_numlt(0, step)`):
// step==0 takes the descending branch, so `for i=0,1,0` iterates zero times and
// `for i=1,0,0` loops forever (issue #97 fixed the original `>=` here that was
// inverted relative to the interpreter).
func (c *Compiler) emitForContinueTest(em *emitter, a uint32) {
	// step = R(A+2)
	em.localGet(localBase)
	em.i64Load(8 * (a + 2))
	em.f64ReinterpretI64()
	em.f64Const(0)
	em.f64Gt()
	em.ifVoid()
	// step>0: idx <= limit
	em.localGet(localF64)
	em.localGet(localBase)
	em.i64Load(8 * (a + 1))
	em.f64ReinterpretI64()
	em.f64Le()
	em.localSet(localI32)
	em.elseOp()
	// step<=0: idx >= limit
	em.localGet(localF64)
	em.localGet(localBase)
	em.i64Load(8 * (a + 1))
	em.f64ReinterpretI64()
	em.f64Ge()
	em.localSet(localI32)
	em.end()
}

// boolToI32 converts a bytecode A/C flag (!=0) to i32 0/1.
func boolToI32(v int) int32 {
	if v != 0 {
		return 1
	}
	return 0
}

// emitTForLoopTerm TFORLOOP A C (PW4b, 02 §3.5.3): call the iterator
// R(A)(R(A+1),R(A+2)); results land in R(A+3..A+2+C); if the first value is
// non-nil → control variable R(A+2):=first value, fall to the back-edge JMP
// (continue); if the first value is nil → skip the back edge (exit). Everything
// goes through h_tforloop (cross-layer iterator call, reusing callLuaFromHost).
//
// h_tforloop returns i64: ≥0 = refreshed base (continue; the iterator call may
// growStack and relocate the segment) / -1 = ERR / -2 = exit. Structure:
//
//	(local.set $i64c (call h_tforloop(base,pc,a,c)))
//	(if (i64.eq $i64c -1) (then (return 1)))            ;; ERR bubbles
//	(if (i64.eq $i64c -2)
//	  (then <emitEdge succSkip exit>)
//	  (else (local.set $base (i32.wrap $i64c)) <emitEdge succExec back edge>))
//
// succExec = lastPC+1 (back-edge JMP BB), succSkip = lastPC+2 (exit BB); same as
// the compare terminator.
func (c *Compiler) emitTForLoopTerm(em *emitter, cfg *cfg, plan *structPlan, stack *[]scope, bb int, lastPC int32) error {
	succExec, succSkip, err := c.compareSuccs(cfg, lastPC)
	if err != nil {
		return err
	}
	a := bytecode.A(cfg.proto.Code[lastPC])
	cc := bytecode.C(cfg.proto.Code[lastPC])

	// $i64c = h_tforloop(base, pc, a, c)
	em.localGet(localBase)
	em.i32Const(lastPC)
	em.i32Const(int32(a))
	em.i32Const(int32(cc))
	em.call(helperTForLoop)
	em.localSet(localI64c)
	// ERR (== -1) → return 1
	em.localGet(localI64c)
	em.i64Const(^uint64(0)) // i64 bit pattern of -1
	em.i64Eq()
	em.ifVoid()
	em.i32Const(1)
	em.ret()
	em.end()
	// Exit (== -2) → succSkip; otherwise refresh base + back edge succExec
	em.localGet(localI64c)
	em.i64Const(^uint64(0) - 1) // i64 bit pattern of -2
	em.i64Eq()
	em.ifVoid()
	*stack = append(*stack, scope{kind: scIf, target: -1})
	if e := c.emitEdge(em, cfg, plan, *stack, bb, succSkip); e != nil {
		return e
	}
	em.elseOp()
	// Continue: refresh base (the iterator call may growStack and relocate the
	// segment; see PW6 / design-vs-physics §2)
	em.localGet(localI64c)
	em.i32WrapI64()
	em.localSet(localBase)
	if e := c.emitEdge(em, cfg, plan, *stack, bb, succExec); e != nil {
		return e
	}
	em.end()
	*stack = (*stack)[:len(*stack)-1]
	return nil
}

// emitBackEdgeSafepoint emits the inline check for a back-edge safepoint (P3 PW9
// GC + loop step-budget accounting, the P3 dual of #102):
//
//	loopBudget := loopBudget - 1                       ;; i32, linear-memory word
//	i32.store loopBudget
//	(if (i32.or (i32.le_s loopBudget 0) (i32.load gcPending))
//	  (then
//	    (if (i32.eq (call h_safepoint base pc) 1)
//	      (then (return 1)))))                          ;; budget exceeded → bubble
//
// In a hot loop, loopBudget counts down from a refill amount (about 1 billion
// when no budget is set) and almost never reaches zero ⟹ each iteration pays
// only a few pure in-segment instructions ("load/sub/store + compare"), with zero
// cross-layer calls (mirroring the P4 loopFuel dec+jz). Only when budget/ctx is
// armed (fuzz/script quota) does it periodically (every quantum) make a
// cross-layer h_safepoint accounting call; or when GC is due (gcPending set) it
// crosses over to collect GC. Safepoint returns status, 1 = raise (budget
// exceeded / ctx cancelled), the segment returns 1 to bubble, byte-for-byte
// consistent with the interpreter preempt raising "instruction budget exceeded"
// at the back edge.
//
// Correctness: gcPending semantics are unchanged (the flag is always 1 when due,
// see the old comment); the newly added budget accounting only changes behavior
// when stepBudget>0 or ctx is armed (otherwise Safepoint only refills a large
// amount and never raises), so a steady state with no budget load has zero
// semantic impact.
func (c *Compiler) emitBackEdgeSafepoint(em *emitter, pc int32) {
	lbAddr := c.host.LoopBudgetAddr()
	// loopBudget -= 1 (read-decrement-write).
	em.i32Const(0)
	em.i32Const(0)
	em.i32Load(lbAddr)
	em.i32Const(1)
	em.i32Sub()
	em.i32Store(lbAddr)
	// cond = (loopBudget <= 0) || (gcPending != 0)
	em.i32Const(0)
	em.i32Load(lbAddr)
	em.i32Const(1)
	em.i32LtS() // loopBudget < 1  ⟺  loopBudget <= 0
	em.i32Const(0)
	em.i32Load(c.host.GCPendingAddr())
	em.raw(0x72) // i32.or
	em.ifVoid()
	// status = h_safepoint(base, pc); status==1 → return 1 (bubble).
	em.localGet(localBase)
	em.i32Const(pc)
	em.call(helperSafepoint)
	em.i32Const(1)
	em.i32Eq()
	em.ifVoid()
	em.i32Const(1)
	em.ret()
	em.end()
	em.end()
}
