package compile

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/bytecode"

	"github.com/Liam0205/wangshu/internal/frontend/lex"
	"github.com/Liam0205/wangshu/internal/frontend/parse"
)

// TestIndexLinesAcrossNewline pins which line each instruction of an index expression carries (#248).
//
// PUC attributes "attempt to index global 'A'" to the line of the indexing OPERATOR, because that is the
// line of the GETTABLE that faults. wangshu reported the line the expression started on, so
// `A<newline>.x` said line 1 where PUC says 2.
//
// Two separate mistakes produced that, and both are pinned here because either alone still gives a wrong
// message. exprIndex discharged the OBJECT at e.Line -- the operator's line -- putting the operator's line
// on the object's own GETGLOBAL; and the GETTABLE carried no line of its own, so it took whatever line the
// later discharge supplied, which for a local declaration is the statement's line.
//
// A local object cannot show either bug: it is already in a register, so exprIndex emits nothing for it and
// the GETTABLE is discharged immediately. That is why the pre-existing TestIndexExprLineIsOperatorLine,
// which uses a local, passed while this was broken -- the case that mattered was the deferred load.
func TestIndexLinesAcrossNewline(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want []int32 // expected LineInfo, one entry per instruction
	}{
		// GETGLOBAL A on line 1 (where A is), GETTABLE on line 2 (where .x is).
		{"global object", "local v = A\n.x", []int32{1, 2, 0}},
		// A local needs no load, so only the GETTABLE appears, on the operator's line.
		{"local object", "local t={}\nlocal v = t\n.x", []int32{1, 3, 0}}, // .x is on line 3 here
		{"bracket index", "local v = A\n[1]", []int32{1, 2, 0}},
		// Assignment TARGETS take the same rule, on two separate code paths that an audit found the first
		// fix had missed: storeVar for a single target, and the multi-target path for `a, b = ...`.
		{"assign target, dot", "A\n.x = 1", []int32{1, 2, 0}},
		{"assign target, bracket", "A\n[1] = 1", []int32{1, 2, 0}},
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

// TestIndexStoreLineOnEveryPath pins the line of the STORE instruction on the paths the table-based test
// above cannot cover, because their surrounding instruction order differs from PUC's for reasons unrelated
// to #248 (PUC emits SETGLOBAL before SETTABLE in a multi-assignment, and puts CLOSURE on its own line).
//
// Asserting the whole table there would encode those incidental differences as if they were the property
// under test. What actually governs the error message is the line of the instruction that faults, so that is
// what is checked here -- one case per code path, since the first version of this test used only
// single-target assignments and stayed green when the multi-target half was reverted.
//
// Expected values were taken from `luac5.1 -p -l` on the same sources.
func TestIndexStoreLineOnEveryPath(t *testing.T) {
	for _, tc := range []struct {
		name     string
		src      string
		op       bytecode.OpCode
		wantLine int32
	}{
		// stmtAssign's multi-target loop: SETTABLE on the indexing operator's line.
		{"multi-target", "A\n.x, b = 1, 2", bytecode.SETTABLE, 2},
		// stmtFunc: refixed to the `function` keyword's line, as PUC's luaK_fixline does.
		{"function sugar", "function\nA\n.b() end", bytecode.SETTABLE, 1},
		// storeVar's single-target path, for completeness on the same assertion style.
		{"single target", "A\n.x = 1", bytecode.SETTABLE, 2},
	} {
		block, err := parse.Parse(lex.New([]byte(tc.src), "z"), "z")
		if err != nil {
			t.Fatalf("%s: parse: %v", tc.name, err)
		}
		mainID, protos, err := Compile(block, "z")
		if err != nil {
			t.Fatalf("%s: compile: %v", tc.name, err)
		}
		p := protos[mainID]
		found := false
		for pc, ins := range p.Code {
			if bytecode.Op(ins) != tc.op {
				continue
			}
			found = true
			if p.LineInfo[pc] != tc.wantLine {
				t.Errorf("%s: %v at pc=%d has line %d, want %d (lines %v)",
					tc.name, tc.op, pc, p.LineInfo[pc], tc.wantLine, p.LineInfo)
			}
		}
		if !found {
			t.Errorf("%s: no %v emitted (lines %v)", tc.name, tc.op, p.LineInfo)
		}
	}
}
