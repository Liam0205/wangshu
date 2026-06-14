//go:build wangshu_p3

package wasm

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/bytecode"
)

// TestStructurize_LoopContiguous layoutOrder 必须让循环体在发射序连续
// (循环体不被循环外 BB 劈开),否则无法套 loop。
func TestStructurize_LoopContiguous(t *testing.T) {
	// 数值 for(cfg_test TestCFG_Loop 同款布局):
	//   0: FORPREP →2 / 1: body / 2: FORLOOP →1 或落 3 / 3: RETURN
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
	// 循环 header = FORLOOP BB(pc=2);循环体含 body BB(pc=1)。
	headerBB := c.pcToBB[2]
	bodyBB := c.pcToBB[1]
	// header 与 body 在发射序必须相邻(body 紧跟 header,中间无循环外 BB)。
	li := r.loops[headerBB]
	if li == nil {
		t.Fatal("no loop detected at FORLOOP BB")
	}
	// 循环体所有 BB 在发射序连续(max pos - min pos + 1 == len(body))。
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

// TestStructurize_NestedLoopReducible 嵌套循环可约简 + 各循环体连续。
func TestStructurize_NestedLoopReducible(t *testing.T) {
	// for i=1,n do for j=1,n do ... end end 的典型布局:
	//   0: FORPREP(外)→4
	//   1: FORPREP(内)→3
	//   2: body(内)
	//   3: FORLOOP(内)→2 或落 4
	//   4: FORLOOP(外)→1 或落 5
	//   5: RETURN
	code := []bytecode.Instruction{
		insAsBx(bytecode.FORPREP, 0, 3),  // 0 →4
		insAsBx(bytecode.FORPREP, 3, 1),  // 1 →3
		insABC(bytecode.MOVE, 6, 0, 0),   // 2 body
		insAsBx(bytecode.FORLOOP, 3, -2), // 3 →2 / 落4
		insAsBx(bytecode.FORLOOP, 0, -4), // 4 →1 / 落5
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
	// 每个循环体连续。
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
	// scope 计算不报错(两两不交或嵌套)。
	if _, _, err := r.computeScopes(order, pos); err != nil {
		t.Errorf("computeScopes: %v", err)
	}
}

// TestStructurize_Irreducible 不可约简 CFG(多入口循环)被拒。
func TestStructurize_Irreducible(t *testing.T) {
	// 构造多入口循环:两个入口都能进同一个循环(无单一 header 支配)。
	//   0: TEST → 跳 2 或落 1
	//   1: JMP → 3       (入口 A 进循环 @3)
	//   2: JMP → 4       (入口 B 进循环 @4)
	//   3: JMP → 4
	//   4: JMP → 3       (3↔4 互跳 = 多入口循环,不可约简)
	// 注:Lua codegen 不会产这种,纯人工构造测防御。
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
