//go:build wangshu_p3

package wasm

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/bytecode"
)

// Small helpers for constructing Proto.Code: each instruction uses op + A/B/C or sBx.

func insABC(op bytecode.OpCode, a, b, c int) bytecode.Instruction {
	return bytecode.EncodeABC(op, a, b, c)
}
func insAsBx(op bytecode.OpCode, a, sbx int) bytecode.Instruction {
	return bytecode.EncodeAsBx(op, a, sbx)
}

// TestCFG_StraightLine pure straight-line code (no jumps) = a single BB.
func TestCFG_StraightLine(t *testing.T) {
	code := []bytecode.Instruction{
		insABC(bytecode.MOVE, 1, 0, 0),
		insABC(bytecode.MOVE, 2, 1, 0),
		insABC(bytecode.RETURN, 0, 1, 0),
	}
	c := buildCFG(&bytecode.Proto{Code: code})
	if len(c.blocks) != 1 {
		t.Fatalf("straight line should be 1 BB, got %d", len(c.blocks))
	}
	if len(c.blocks[0].succs) != 0 {
		t.Errorf("RETURN BB should have no succ, got %v", c.blocks[0].succs)
	}
}

// TestCFG_ForwardJump if-then form: TEST + JMP (forward jump skipping the then body).
//
//	0: TEST   R0
//	1: JMP    +1   (jump to 3 if false, fall through to 2 if true)
//	2: MOVE   (then body)
//	3: RETURN
func TestCFG_ForwardJump(t *testing.T) {
	code := []bytecode.Instruction{
		insABC(bytecode.TEST, 0, 0, 0),   // 0
		insAsBx(bytecode.JMP, 0, 1),      // 1: jmp +1 → pc 3
		insABC(bytecode.MOVE, 1, 0, 0),   // 2: then
		insABC(bytecode.RETURN, 0, 1, 0), // 3
	}
	c := buildCFG(&bytecode.Proto{Code: code})
	r := analyzeRelooper(c)

	// No back edge ⇒ no loop
	if len(r.loops) != 0 {
		t.Errorf("forward-only CFG should have no loops, got %d", len(r.loops))
	}
	// The entry BB dominates all BBs
	for _, bb := range c.blocks {
		if !r.dominates(c.entry, bb.id) {
			t.Errorf("entry should dominate BB %d", bb.id)
		}
	}
}

// TestCFG_Loop numeric for loop: FORPREP jumps to FORLOOP, FORLOOP jumps back to body.
//
// Real Lua 5.1 layout (stmt.go stmtNumFor):
//
//	0: FORPREP R0 +1   (jumps to pc 2 = FORLOOP)
//	1: MOVE            (loop body, FORPREP+1)
//	2: FORLOOP R0 -2   (jumps back to pc 1 = body, or falls out to pc 3)
//	3: RETURN
//
// CFG edges: pc0→pc2 (FORPREP jump), pc1→pc2 (body falls into FORLOOP),
// pc2→pc1 (jump back) + pc2→pc3 (fall out).
// Loop header = pc2 (FORLOOP) — it is the join point of entry (via pc0) and latch (pc1),
// and it dominates the body (pc1's only entry is the pc2 jump-back); the back edge is pc1→pc2.
func TestCFG_Loop(t *testing.T) {
	code := []bytecode.Instruction{
		insAsBx(bytecode.FORPREP, 0, 1),  // 0: → pc 2
		insABC(bytecode.MOVE, 4, 0, 0),   // 1: body
		insAsBx(bytecode.FORLOOP, 0, -2), // 2: → pc 1 (jump back) or falls out to 3
		insABC(bytecode.RETURN, 0, 1, 0), // 3
	}
	c := buildCFG(&bytecode.Proto{Code: code})
	r := analyzeRelooper(c)

	// Should identify exactly one loop
	if len(r.loops) != 1 {
		t.Fatalf("for-loop should have 1 natural loop, got %d", len(r.loops))
	}
	// The loop header is the BB containing FORLOOP (pc=2) — the join point + dominates the body
	headerBB := c.pcToBB[2]
	if _, ok := r.loops[headerBB]; !ok {
		t.Errorf("loop header should be BB at pc=2 (FORLOOP, id=%d), loops=%v", headerBB, r.loops)
	}
	// The body (pc=1) should be inside the loop
	bodyBB := c.pcToBB[1]
	if r.loopOf[bodyBB] != headerBB {
		t.Errorf("body BB %d should belong to loop header %d, got loopOf=%d",
			bodyBB, headerBB, r.loopOf[bodyBB])
	}
}

// TestCFG_RPO_DominatorConsistency dominator-tree self-consistency: each non-entry BB's idom
// must precede it in RPO (the dominator appears first).
func TestCFG_RPO_DominatorConsistency(t *testing.T) {
	code := []bytecode.Instruction{
		insABC(bytecode.TEST, 0, 0, 0),
		insAsBx(bytecode.JMP, 0, 2),
		insABC(bytecode.MOVE, 1, 0, 0),
		insAsBx(bytecode.JMP, 0, 1),
		insABC(bytecode.MOVE, 2, 0, 0),
		insABC(bytecode.RETURN, 0, 1, 0),
	}
	c := buildCFG(&bytecode.Proto{Code: code})
	r := analyzeRelooper(c)
	for _, bb := range c.blocks {
		if bb.id == c.entry {
			continue
		}
		id := r.idom[bb.id]
		if id == -1 {
			t.Errorf("BB %d has no idom (unreachable?)", bb.id)
			continue
		}
		if r.rpoIndex[id] >= r.rpoIndex[bb.id] {
			t.Errorf("idom(%d)=%d not before in RPO (%d >= %d)",
				bb.id, id, r.rpoIndex[id], r.rpoIndex[bb.id])
		}
	}
}
