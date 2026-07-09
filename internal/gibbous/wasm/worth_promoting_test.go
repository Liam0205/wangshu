//go:build wangshu_p3

package wasm

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/bytecode"
)

// WorthPromoting reads only the proto's op mix (no Compiler state), so a
// zero-value receiver is enough for these profitability-gate tests.

// TestWorthPromoting_StraightLineSmallDeclined (issue #92): a small
// straight-line body (no back edge) promotes to a per-call boundary tax
// with nothing to amortize it — must be declined.
func TestWorthPromoting_StraightLineSmallDeclined(t *testing.T) {
	c := &Compiler{}
	var code []bytecode.Instruction
	for i := 0; i < 9; i++ {
		code = append(code, bytecode.EncodeABC(bytecode.ADD, 0, 1, 2))
	}
	code = append(code, bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0))
	p := &bytecode.Proto{Code: code}
	if c.WorthPromoting(p) {
		t.Fatal("small straight-line body should be declined (boundary tax, no loop to amortize)")
	}
}

// TestWorthPromoting_StraightLineLargeAccepted: a straight-line body at
// or past straightLineMinCodeLen carries enough per-call dispatch
// savings to cover the boundary — keeps promoting.
func TestWorthPromoting_StraightLineLargeAccepted(t *testing.T) {
	c := &Compiler{}
	var code []bytecode.Instruction
	for i := 0; i < straightLineMinCodeLen; i++ {
		code = append(code, bytecode.EncodeABC(bytecode.ADD, 0, 1, 2))
	}
	code = append(code, bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0))
	p := &bytecode.Proto{Code: code}
	if !c.WorthPromoting(p) {
		t.Fatal("large straight-line body should still promote")
	}
}

// TestWorthPromoting_BackEdgeAccepted: FORLOOP, TFORLOOP, and negative
// JMP each count as a back edge — small bodies with any of them keep
// promoting (the loop amortizes the boundary).
func TestWorthPromoting_BackEdgeAccepted(t *testing.T) {
	c := &Compiler{}
	for _, tc := range []struct {
		name string
		ins  bytecode.Instruction
	}{
		{"FORLOOP", bytecode.EncodeAsBx(bytecode.FORLOOP, 0, -2)},
		{"TFORLOOP", bytecode.EncodeABC(bytecode.TFORLOOP, 0, 0, 1)},
		{"JMP-neg", bytecode.EncodeAsBx(bytecode.JMP, 0, -1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code := []bytecode.Instruction{
				bytecode.EncodeABC(bytecode.ADD, 0, 1, 2),
				bytecode.EncodeABC(bytecode.ADD, 0, 1, 2),
				tc.ins,
				bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
			}
			p := &bytecode.Proto{Code: code}
			if !c.WorthPromoting(p) {
				t.Fatalf("%s should count as a back edge and keep the proto promotable", tc.name)
			}
		})
	}
}

// TestWorthPromoting_ForwardJmpIsNotBackEdge: a forward JMP (sBx >= 0)
// is a branch, not a loop — a small body with only forward jumps is
// still straight-line for profitability purposes.
func TestWorthPromoting_ForwardJmpIsNotBackEdge(t *testing.T) {
	c := &Compiler{}
	code := []bytecode.Instruction{
		bytecode.EncodeABC(bytecode.ADD, 0, 1, 2),
		bytecode.EncodeAsBx(bytecode.JMP, 0, 1),
		bytecode.EncodeABC(bytecode.ADD, 0, 1, 2),
		bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
	}
	p := &bytecode.Proto{Code: code}
	if c.WorthPromoting(p) {
		t.Fatal("forward JMP must not count as a back edge")
	}
}

// TestWorthPromoting_HelperDensityStillApplies: the back-edge dimension
// composes with the existing helper-density floor — a loop body
// dominated by helper-bound ops is still declined.
func TestWorthPromoting_HelperDensityStillApplies(t *testing.T) {
	c := &Compiler{}
	code := []bytecode.Instruction{
		bytecode.EncodeABC(bytecode.GETTABLE, 0, 1, 2),
		bytecode.EncodeABC(bytecode.SETTABLE, 0, 1, 2),
		bytecode.EncodeAsBx(bytecode.FORLOOP, 0, -2), // back edge present
		bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
	}
	p := &bytecode.Proto{Code: code}
	if c.WorthPromoting(p) {
		t.Fatal("helper-dense loop body should still be declined by the density floor")
	}
}
