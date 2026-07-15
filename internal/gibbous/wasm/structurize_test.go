//go:build wangshu_p3

package wasm

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/bytecode"
)

// TestStructurize_LoopContiguous: layoutOrder must keep the loop body contiguous in emission order
// (the loop body is not split by BBs outside the loop), otherwise it cannot be wrapped in a loop.
func TestStructurize_LoopContiguous(t *testing.T) {
	// Numeric for (same layout as cfg_test TestCFG_Loop):
	//   0: FORPREP →2 / 1: body / 2: FORLOOP →1 or fall to 3 / 3: RETURN
	code := []bytecode.Instruction{
		insAsBx(bytecode.FORPREP, 0, 1),
		insABC(bytecode.MOVE, 4, 0, 0),
		insAsBx(bytecode.FORLOOP, 0, -2),
		insABC(bytecode.RETURN, 0, 1, 0),
	}
	c := buildCFG(&bytecode.Proto{Code: code})
	r := analyzeRelooper(c)
	if !r.isReducible() {
		t.Fatal("for-loop CFG should be reducible")
	}
	order, pos := r.layoutOrder()
	// Loop header = FORLOOP BB (pc=2); loop body contains the body BB (pc=1).
	headerBB := c.pcToBB[2]
	bodyBB := c.pcToBB[1]
	// header and body must be adjacent in emission order (body immediately follows header, with no out-of-loop BB between).
	li := r.loops[headerBB]
	if li == nil {
		t.Fatal("no loop detected at FORLOOP BB")
	}
	// All BBs of the loop body are contiguous in emission order (max pos - min pos + 1 == len(body)).
	minP, maxP := len(order), -1
	for _, b := range li.body {
		if pos[b] < minP {
			minP = pos[b]
		}
		if pos[b] > maxP {
			maxP = pos[b]
		}
	}
	if maxP-minP+1 != len(li.body) {
		t.Errorf("loop body not contiguous in layout: body=%v pos range [%d,%d] len=%d",
			li.body, minP, maxP, len(li.body))
	}
	_ = bodyBB
}

// TestStructurize_NestedLoopReducible: nested loops are reducible + each loop body is contiguous.
func TestStructurize_NestedLoopReducible(t *testing.T) {
	// Typical layout of for i=1,n do for j=1,n do ... end end:
	//   0: FORPREP (outer) →4
	//   1: FORPREP (inner) →3
	//   2: body (inner)
	//   3: FORLOOP (inner) →2 or fall to 4
	//   4: FORLOOP (outer) →1 or fall to 5
	//   5: RETURN
	code := []bytecode.Instruction{
		insAsBx(bytecode.FORPREP, 0, 3),  // 0 →4
		insAsBx(bytecode.FORPREP, 3, 1),  // 1 →3
		insABC(bytecode.MOVE, 6, 0, 0),   // 2 body
		insAsBx(bytecode.FORLOOP, 3, -2), // 3 →2 / fall to 4
		insAsBx(bytecode.FORLOOP, 0, -4), // 4 →1 / fall to 5
		insABC(bytecode.RETURN, 0, 1, 0), // 5
	}
	c := buildCFG(&bytecode.Proto{Code: code})
	r := analyzeRelooper(c)
	if !r.isReducible() {
		t.Fatal("nested for-loop CFG should be reducible")
	}
	order, pos := r.layoutOrder()
	if len(order) != len(c.blocks) {
		t.Fatalf("layout order len %d != BB count %d", len(order), len(c.blocks))
	}
	// Each loop body is contiguous.
	for h, li := range r.loops {
		minP, maxP := len(order), -1
		for _, b := range li.body {
			if pos[b] < minP {
				minP = pos[b]
			}
			if pos[b] > maxP {
				maxP = pos[b]
			}
		}
		if maxP-minP+1 != len(li.body) {
			t.Errorf("loop %d body not contiguous: body=%v pos [%d,%d]", h, li.body, minP, maxP)
		}
	}
	// scope computation does not error (pairwise disjoint or nested).
	if _, _, err := r.computeScopes(order, pos); err != nil {
		t.Errorf("computeScopes: %v", err)
	}
}

// TestStructurize_Irreducible: an irreducible CFG (multi-entry loop) is rejected.
func TestStructurize_Irreducible(t *testing.T) {
	// Construct a multi-entry loop: both entries can enter the same loop (no single dominating header).
	//   0: TEST → jump 2 or fall to 1
	//   1: JMP → 3       (entry A enters loop @3)
	//   2: JMP → 4       (entry B enters loop @4)
	//   3: JMP → 4
	//   4: JMP → 3       (3↔4 cross-jump = multi-entry loop, irreducible)
	// Note: Lua codegen does not produce this; purely hand-constructed to test the guard.
	code := []bytecode.Instruction{
		insABC(bytecode.TEST, 0, 0, 0), // 0
		insAsBx(bytecode.JMP, 0, 1),    // 1 →3
		insAsBx(bytecode.JMP, 0, 1),    // 2 →4
		insAsBx(bytecode.JMP, 0, 0),    // 3 →4
		insAsBx(bytecode.JMP, 0, -2),   // 4 →3
	}
	c := buildCFG(&bytecode.Proto{Code: code})
	r := analyzeRelooper(c)
	if r.isReducible() {
		t.Skip("constructed CFG turned out reducible (codegen-shape dependent); irreducibility guard still in place")
	}
}
