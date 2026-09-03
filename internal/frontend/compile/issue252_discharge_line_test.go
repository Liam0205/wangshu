package compile

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/bytecode"

	"github.com/Liam0205/wangshu/internal/frontend/lex"
	"github.com/Liam0205/wangshu/internal/frontend/parse"
)

// TestDischargeLineIsLastLine pins the line a deferred GETTABLE carries: the line of the point that
// DISCHARGES it, not the line of the indexing operator (#252).
//
// PUC does not pass a line to luaK_codeABC at all -- it stamps every instruction with ls->lastline, the line
// of the last token the lexer consumed when the instruction is emitted. #248 read that as "the indexing
// operator's line" and pinned it via expDesc.opLine. The two coincide only when the index is consumed right
// where it is written; they diverge whenever consumption is deferred past a newline.
//
// The case that ruled the operator-line model out is `local v = A.x<nl><nl>+1`: it has NO parens, the
// operator is on line 1, and PUC still reports line 3, because the `+` is what discharges the index. So this
// is not a parenthesis special case -- it is the general lastline rule, and parens matter only because they
// are one way to defer the discharge point.
//
// Expected values are `luac5.1 -p -l` output on the same sources, except for the trailing RETURN: we emit
// the function's closing RETURN with line 0 where luac repeats the last source line. That predates #252
// (the #248 table asserts 0 there too) and is not what these cases are about.
func TestDischargeLineIsLastLine(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want []int32 // expected LineInfo, one entry per instruction
	}{
		// The #248 shapes must not regress: consumed right where written, so operator line == lastline.
		{"no paren, index across newline", "local v = A\n.x", []int32{1, 2, 0}},
		{"no paren, bracket index", "local v = A\n[1]", []int32{1, 2, 0}},

		// Parens defer the discharge to the CLOSING paren, which is where PUC's lastline stands by then.
		{"paren closes two lines later", "local v = (A.x\n\n)", []int32{1, 3, 0}},
		{"paren, operator also moved", "local v = (A\n.x\n\n)", []int32{1, 4, 0}},

		// No parens at all: the arithmetic operator is the discharge point. This is the case that proves
		// the rule is about lastline and not about parens.
		{"arithmetic defers discharge", "local v = A.x\n\n+1", []int32{1, 3, 3, 0}},
	} {
		block, err := parse.Parse(lex.New([]byte(tc.src), "z"), "z")
		if err != nil {
			t.Fatalf("%s: parse: %v", tc.name, err)
		}
		mainID, protos, err := Compile(block, "z")
		if err != nil {
			t.Fatalf("%s: compile: %v", tc.name, err)
		}
		got := protos[mainID].LineInfo
		if len(got) != len(tc.want) {
			t.Errorf("%s: %d instructions, want %d (lines %v)", tc.name, len(got), len(tc.want), got)
			continue
		}
		for i := range tc.want {
			if got[i] != tc.want[i] {
				t.Errorf("%s: pc=%d line=%d, want %d (full: %v)", tc.name, i, got[i], tc.want[i], got)
			}
		}
	}
}

// TestNestedParenDischargeLine pins that each paren advances the discharge point independently, so the
// GETTABLE and the CALL land on DIFFERENT lines (#252).
//
// `((A.A<nl>)<nl>)()` puts GETTABLE on 2 (the inner paren, which discharges the index) and CALL on 3 (the
// outer one). A single whole-expression end line cannot express that, which is why EndLine lives on
// ParenExpr per node rather than being computed for the outermost expression.
func TestNestedParenDischargeLine(t *testing.T) {
	const src = "((A.A\n)\n)()"
	block, err := parse.Parse(lex.New([]byte(src), "z"), "z")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	mainID, protos, err := Compile(block, "z")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	p := protos[mainID]
	// luac5.1: GETGLOBAL [1], GETTABLE [2], CALL [3].
	for _, want := range []struct {
		op   bytecode.OpCode
		line int32
	}{
		{bytecode.GETTABLE, 2},
		{bytecode.CALL, 3},
	} {
		found := false
		for pc, ins := range p.Code {
			if bytecode.Op(ins) != want.op {
				continue
			}
			found = true
			if p.LineInfo[pc] != want.line {
				t.Errorf("%v at pc=%d has line %d, want %d (lines %v)",
					want.op, pc, p.LineInfo[pc], want.line, p.LineInfo)
			}
		}
		if !found {
			t.Errorf("no %v emitted (lines %v)", want.op, p.LineInfo)
		}
	}
}
