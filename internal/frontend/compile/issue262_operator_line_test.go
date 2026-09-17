package compile

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/frontend/lex"
	"github.com/Liam0205/wangshu/internal/frontend/parse"
)

// TestOperatorLinesAreLastLine pins the line an operator's instructions carry: PUC's ls->lastline at the
// point luaK_posfix / luaK_prefix runs, which is the line of the operand's LAST token, not the operator's
// line (#262, fuzz seed `error("boom"%<nl>0)`: PUC reports 2, we reported 1).
//
// PUC has two emission points per binary operator. luaK_infix runs right after the operator token is
// consumed, so the LEFT operand materializes on the operator's line (and/or's TEST+JMP are emitted there
// too). luaK_posfix runs after the right operand is parsed, so the RIGHT operand's materialization and the
// operation itself -- arithmetic, comparison + its JMP, CONCAT -- land on the right operand's last line.
// A unary operator has only the prefix point, after its operand: everything lands on the operand's last
// line. Before #262 all of these used the operator's line, which is correct only while the right operand
// stays on that line -- the #252 table happened to contain only such shapes (`A.x<nl><nl>+1`, where the
// operator and its right operand share line 3).
//
// Expected values are `luac5.1 -p -l` on the same sources, minus the trailing RETURN (we emit it with line
// 0; predates #252).
func TestOperatorLinesAreLastLine(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want []int32 // expected LineInfo, one entry per instruction
	}{
		// The #262 shape: a right operand on its own line moves the MOD there. The left operand is a
		// numeral, deferred past infix, so it too is materialized at posfix time -- nothing of it is
		// emitted here, but see "numeral left, global right" below.
		{"arith, right operand moved", "local v = A\n%\n1", []int32{2, 3, 0}},
		{"arith, both constants", "local v = 1\n%\n0", []int32{3, 0}},
		{"arith, string left", "local v = \"a\" %\n0", []int32{2, 0}},
		// A deferred numeral left plus a global right: the GETGLOBAL/GETTABLE take the right operand's
		// line, and so does the MOD.
		{"numeral left, global right", "local v = 1 %\n\nA.x", []int32{3, 3, 3, 0}},
		// Left-associative chain: each operation is stamped where ITS right operand ends.
		{"arith chain", "local v = a % 1\n+ 2\n* 3", []int32{1, 1, 3, 0}},
		// Right-associative: the whole `2 ^ 3` folds, then the outer POW lands on the last line.
		{"pow chain", "local v = A.x ^\n2 ^\n3", []int32{1, 1, 3, 0}},
		// The MOD is on 3 (`}`). The NEWTABLE is 3 where luac5.1 says 2: PUC's constructor emits it
		// BEFORE checknext('{'), so it carries the line of the token preceding the brace. That is the
		// same pre-existing quirk the #252 table records for `t.x<nl>{1}`; NEWTABLE cannot raise, and it
		// is asserted as-is so this table stays a record of what we emit.
		{"table right operand", "local v = a\n%\n{}", []int32{2, 3, 3, 0}},

		// Comparisons: the right operand, the compare and its JMP all take the right operand's line;
		// only the LOADBOOL pair after them already did (they are emitted by exp2reg at the statement).
		{"compare, right operand moved", "local v = 1 ==\nA.x", []int32{2, 2, 2, 2, 2, 2, 0}},
		{"compare, right index split", "local v = 1 <\nA\n.x", []int32{2, 3, 3, 3, 3, 3, 0}},

		// and/or: TEST+JMP at the operator (infix), the right operand at its own last line (posfix).
		{"and, right index moved", "local v = a and A\n.x", []int32{1, 1, 1, 1, 2, 0}},
		{"and, operator moved", "local v = a\nand\nA.x", []int32{2, 2, 2, 3, 3, 0}},
		{"or, right index moved", "local v = a or A\n.x", []int32{1, 1, 1, 1, 2, 0}},

		// Concat: each operand but the last is pushed by the `..` that FOLLOWS it (that operator's
		// line); the last operand and the CONCAT itself take the last operand's line.
		{"concat, right moved", "local v = 1 ..\nA.x", []int32{1, 2, 2, 2, 0}},
		{"concat chain, constants", "local v = 1\n..\n2\n..\nA.x", []int32{2, 4, 5, 5, 5, 0}},
		{"concat chain, index first", "local v = A.x\n..\n2\n..\n3", []int32{1, 2, 4, 5, 5, 0}},

		// Unary: operand and operation at the operand's last line.
		{"unm", "local v = -A\n.x", []int32{1, 2, 2, 0}},
		{"not", "local v = not A\n.x", []int32{1, 2, 2, 0}},
		{"len", "local v = #A\n.x", []int32{1, 2, 2, 0}},
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

// TestStoreLineIsStatementEnd pins the line of a single-target assignment's STORE: PUC's lastline when
// luaK_storevar runs, i.e. the statement's last token (#262).
//
// storeVar used to put the store on the statement's first line (SETGLOBAL/SETUPVAL) or the indexing
// operator's line (SETTABLE), recording the difference as an unobservable gap because those stores "cannot
// raise". SETTABLE can: on a nil object, or through a __newindex that calls error(msg, 2), PUC reports the
// statement's LAST line -- `x.y = 1<nl>+<nl>1` says 3. The multi-target path already used the statement
// end; this aligns the fast path with it.
func TestStoreLineIsStatementEnd(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want []int32
	}{
		// The RHS folds, so only GETGLOBAL x and the SETTABLE remain: the store is on 3.
		{"settable, rhs spans lines", "x.y = 1\n+\n1", []int32{1, 3, 0}},
		{"settable, everything split", "x\n.y\n=\n1", []int32{1, 4, 0}},
		{"settable, bracket key split", "x[1\n] = 1", []int32{1, 2, 0}},
		// The #248 shape keeps its answer (2) because the statement also ends on 2.
		{"settable, #248 shape", "A\n.x = 1", []int32{1, 2, 0}},
		// SETGLOBAL takes the same rule even though it cannot raise, so the two store kinds do not drift.
		{"setglobal, call rhs", "x = f(1,\n2)", []int32{1, 1, 2, 1, 2, 0}},
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

// TestNumericForHeaderLines pins the numeric for's header instructions: each expression at its own last
// token (exp1 materializes it as soon as it is parsed), the default step's LOADK at the limit's line, and
// FORPREP at the `do` -- which is where "'for' initial value must be a number" is reported, so
// `for i = "x", 2<nl>do end` says 2 (#262). FORLOOP stays on the `for` line (forbody's luaK_fixline).
func TestNumericForHeaderLines(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want []int32
	}{
		{"init then newline before comma", "for i = \"x\"\n, 2 do end", []int32{1, 2, 2, 2, 1, 0}},
		{"do on its own line", "for i = \"x\", 2\ndo end", []int32{1, 1, 1, 2, 1, 0}},
		{"every part on its own line", "for i = 1\n,\n2\n,\n3\ndo end", []int32{1, 3, 5, 6, 1, 0}},
		{"blank lines before do", "for i = 1, 2, A.x\n\n\ndo end", []int32{1, 1, 1, 1, 4, 1, 0}},
		{"parenthesized init", "for i = (1\n), 2 do end", []int32{2, 2, 2, 2, 1, 0}},
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
