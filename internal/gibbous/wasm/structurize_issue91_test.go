//go:build wangshu_p3

package wasm

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/bytecode"
)

// TestStructurize_TopLevelDiamond — issue #91: a plain top-level
// if/else diamond (not inside any loop) used to CFAIL with "improper
// scope overlap". The two merge blocks of the diamond partially overlap
// (the earlier block's end falls inside the later block's range), which
// the old one-directional nesting repair never handled — it only
// widened the NEW scope when an EXISTING scope's begin fell inside it.
// normalizeScopes now runs a fixed-point pass over all pairs after all
// scopes are built, widening the block with the larger end to contain
// the other (a Wasm block can always start earlier; its end is pinned
// to the merge point, its begin is free).
//
// Bytecode shape (k(a,b): local r=0 if a<b then r=a else r=b end
// return r):
//
//	pc0 LOADK  pc1 LT  pc2 JMP(+2)  pc3 MOVE  pc4 JMP(+1)  pc5 MOVE  pc6 RETURN
//
// BBs: 0=[0,2) → {1,2}; 1=[2,3) → 3; 2=[3,5) → 4; 3=[5,6) → 4; 4=[6,7).
// Layout order [0 2 1 3 4] creates block scopes for merge BBs 1(!), 3
// and 4 whose ranges partially overlap.
func TestStructurize_TopLevelDiamond(t *testing.T) {
	code := []bytecode.Instruction{
		bytecode.EncodeABx(bytecode.LOADK, 2, 0),
		insABC(bytecode.LT, 0, 0, 1),
		insAsBx(bytecode.JMP, 0, 2),
		insABC(bytecode.MOVE, 2, 0, 0),
		insAsBx(bytecode.JMP, 0, 1),
		insABC(bytecode.MOVE, 2, 1, 0),
		insABC(bytecode.RETURN, 2, 2, 0),
	}
	c := buildCFG(&bytecode.Proto{Code: code})
	plan, err := buildStructPlan(c)
	if err != nil {
		t.Fatalf("top-level diamond must structurize (issue #91), got: %v", err)
	}
	// The produced scopes must be pairwise disjoint-or-nested.
	for i := range plan.scopes {
		for j := i + 1; j < len(plan.scopes); j++ {
			if overlapImproper(plan.scopes[i], plan.scopes[j]) {
				t.Errorf("scopes %v and %v improperly overlap after normalize",
					plan.scopes[i], plan.scopes[j])
			}
		}
	}
}

// TestStructurize_TopLevelSingleArmIf — issue #91 companion shape:
// `local r=b if a<b then r=a end return r` (single-arm if at function
// top level) hit the same overlap.
//
//	pc0 MOVE  pc1 LT  pc2 JMP(+1)  pc3 MOVE  pc4 RETURN
func TestStructurize_TopLevelSingleArmIf(t *testing.T) {
	code := []bytecode.Instruction{
		insABC(bytecode.MOVE, 2, 1, 0),
		insABC(bytecode.LT, 0, 0, 1),
		insAsBx(bytecode.JMP, 0, 1),
		insABC(bytecode.MOVE, 2, 0, 0),
		insABC(bytecode.RETURN, 2, 2, 0),
	}
	c := buildCFG(&bytecode.Proto{Code: code})
	if _, err := buildStructPlan(c); err != nil {
		t.Fatalf("top-level single-arm if must structurize (issue #91), got: %v", err)
	}
}

// TestNormalizeScopes_LoopOuterRejected — defensive arm: when the
// scope with the larger end is a LOOP (its begin is pinned to the
// header, it cannot be widened), normalizeScopes must reject instead
// of silently mis-nesting. Reducible Lua codegen never produces this
// (a forward edge into a loop body's middle is a second loop entry,
// rejected by isReducible), so the pair is constructed directly.
func TestNormalizeScopes_LoopOuterRejected(t *testing.T) {
	scopes := []scope{
		{kind: scBlock, target: 1, begin: 0, end: 2},
		{kind: scLoop, target: 2, begin: 1, end: 4},
	}
	if err := normalizeScopes(scopes); err == nil {
		t.Fatal("loop-outer improper overlap must be rejected, got nil error")
	}
}
