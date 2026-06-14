//go:build wangshu_p3

package wasm

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/bytecode"
)

// 构造 Proto.Code 的小工具:每条指令用 op + A/B/C 或 sBx。

func insABC(op bytecode.OpCode, a, b, c int) bytecode.Instruction {
	return bytecode.EncodeABC(op, a, b, c)
}
func insAsBx(op bytecode.OpCode, a, sbx int) bytecode.Instruction {
	return bytecode.EncodeAsBx(op, a, sbx)
}

// TestCFG_StraightLine 纯直线代码(无跳转)= 单 BB。
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

// TestCFG_ForwardJump if-then 形态:TEST + JMP(前向跳过 then 体)。
//
//	0: TEST   R0
//	1: JMP    +1   (假则跳到 3,真则落 2)
//	2: MOVE   (then 体)
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

	// 无回边 ⇒ 无循环
	if len(r.loops) != 0 {
		t.Errorf("forward-only CFG should have no loops, got %d", len(r.loops))
	}
	// entry BB 支配所有 BB
	for _, bb := range c.blocks {
		if !r.dominates(c.entry, bb.id) {
			t.Errorf("entry should dominate BB %d", bb.id)
		}
	}
}

// TestCFG_Loop 数值 for 循环:FORPREP 跳到 FORLOOP,FORLOOP 回跳 body。
//
// 真实 Lua 5.1 布局(stmt.go stmtNumFor):
//
//	0: FORPREP R0 +1   (跳到 pc 2 = FORLOOP)
//	1: MOVE            (循环体,FORPREP+1)
//	2: FORLOOP R0 -2   (回跳到 pc 1 = body,或落出 pc 3)
//	3: RETURN
//
// CFG 边:pc0→pc2(FORPREP 跳)、pc1→pc2(body 落入 FORLOOP)、
// pc2→pc1(回跳)+ pc2→pc3(落出)。
// 循环头 = pc2(FORLOOP)——它是 entry(经 pc0)与 latch(pc1)的汇合点,
// 且支配 body(pc1 唯一入口是 pc2 回跳);回边是 pc1→pc2。
func TestCFG_Loop(t *testing.T) {
	code := []bytecode.Instruction{
		insAsBx(bytecode.FORPREP, 0, 1),  // 0: → pc 2
		insABC(bytecode.MOVE, 4, 0, 0),   // 1: body
		insAsBx(bytecode.FORLOOP, 0, -2), // 2: → pc 1 (回跳) 或落 3
		insABC(bytecode.RETURN, 0, 1, 0), // 3
	}
	c := buildCFG(&bytecode.Proto{Code: code})
	r := analyzeRelooper(c)

	// 应识别一个循环
	if len(r.loops) != 1 {
		t.Fatalf("for-loop should have 1 natural loop, got %d", len(r.loops))
	}
	// 循环头是 FORLOOP 所在 BB(pc=2)——汇合点 + 支配 body
	headerBB := c.pcToBB[2]
	if _, ok := r.loops[headerBB]; !ok {
		t.Errorf("loop header should be BB at pc=2 (FORLOOP, id=%d), loops=%v", headerBB, r.loops)
	}
	// body(pc=1)应在循环体内
	bodyBB := c.pcToBB[1]
	if r.loopOf[bodyBB] != headerBB {
		t.Errorf("body BB %d should belong to loop header %d, got loopOf=%d",
			bodyBB, headerBB, r.loopOf[bodyBB])
	}
}

// TestCFG_RPO_DominatorConsistency 支配树自洽性:每个非 entry BB 的 idom
// 必须在 RPO 中先于它(支配者先出现)。
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
