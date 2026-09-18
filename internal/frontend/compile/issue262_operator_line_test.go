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
		// The MOD is on 3 (`}`); the NEWTABLE on 2, because PUC's constructor emits it BEFORE
		// checknext('{') and so stamps it with the preceding token's line (the `%`).
		{"table right operand", "local v = a\n%\n{}", []int32{2, 2, 3, 0}},

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
		// Multi-target: a local target's MOVE is luaK_storevar's exp2reg at the same lastline as the other
		// stores (found by the final independent review; it was the statement's first line). The leading 1 is
		// our LOADNIL for `local a, b`, which luac elides.
		{"multi-target, local moves", "local a, b\na, b = 1,\n2", []int32{1, 2, 3, 3, 3, 0}},
		{"multi-target, local and global", "local a\na, x = 1,\n2", []int32{1, 2, 3, 3, 3, 0}},
		{"multi-target, local from call", "local a, b\na, b = f(\n)", []int32{1, 2, 2, 3, 3, 0}},
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

// TestStatementEmissionPointLines pins the remaining emission points that a luac5.1 line dump over ~1900
// multi-line shapes still disagreed on after the operator/store/for-header fixes, found by the first
// independent review of #262. Each row names the PUC function whose lastline the instruction carries:
//   - table constructor: recfield emits SETTABLE right after the value (`{<nl>[nil]<nl>=<nl>1}` reports
//     "table index is nil" on 4); a positional item is pushed by closelistfield after the following
//     separator, or by lastlistfield after `}`; NEWTABLE is emitted BEFORE checknext('{');
//   - generic for: forbody stamps TFORLOOP with forlist's line, read right after `in` (a non-callable
//     generator in `for k in<nl>nil` is reported on 2), the forward JMP after `do`, the back-edge after
//     the body;
//   - method call: primaryexp reads the method NAME before luaK_self discharges the receiver, so
//     `A.b<nl>:m()` indexes A on 2;
//   - return: luaK_ret runs after the list; the single-value path discharges there too;
//   - vararg: simpleexp emits VARARG before consuming `...`;
//   - conditions: cond() runs goiftrue at the condition's last token; if/while emit their escape and
//     back-edge JMPs after the block, before the closing keyword;
//   - local: adjust_assign pads LOADNIL right after the last initializer;
//   - inside a constructor the NAME/`=` lookahead has already scanned the next token, and PUC's lastline
//     follows the scanner (`{<nl>A<nl>.x}` loads A on 3).
//
// Expected values are `luac5.1 -p -l` minus the trailing RETURN.
func TestStatementEmissionPointLines(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want []int32
	}{
		// LOADNIL is our nil-key materialization (luac folds the key into the RK); it lands on the `=`
		// line, where recfield's exp2RK runs. The SETTABLE is what raises, and it is on 4.
		{"constructor, nil key on its own line", "local t = {\n[nil]\n=\n1}", []int32{1, 3, 4, 0}},
		{"constructor, name key, trailing comma", "local t = {\nx\n=\n1\n,\n}", []int32{1, 4, 0}},
		{"constructor, bracket key index", "local t = {\n[A\n.x]\n=\n1}", []int32{1, 2, 3, 5, 0}},
		{"constructor, positional index, lookahead", "local t = {\nA\n.x\n,\n1\n}", []int32{1, 3, 4, 6, 6, 0}},
		{"constructor, positional call", "local t = {\nf(\n)\n}", []int32{1, 2, 2, 4, 0}},
		{"constructor, semicolons", "local t = {1;\n2;\n}", []int32{1, 1, 3, 3, 0}},
		{"constructor, positional call with lookahead", "local t = {\nA\n(\n)\n}", []int32{1, 3, 3, 5, 0}},
		// The two LOADNIL are our iterator padding (luac elides the constant); TFORLOOP is on 2.
		{"generic for, nil generator", "for k in\nnil do end", []int32{2, 2, 2, 2, 2, 0}},
		{"generic for, iterator list split", "for k in\nx\n,\ny do end", []int32{3, 4, 4, 4, 2, 4, 0}},
		{"generic for, do and body lines", "for k in x\ndo\nf()\nend", []int32{1, 1, 2, 3, 3, 1, 3, 0}},
		{"method call, receiver index split", "A.b\n:m()", []int32{1, 2, 2, 2, 0}},
		{"return, index split", "return\nA\n.x", []int32{2, 3, 3, 0}},
		{"return, list", "return a\n,\nb", []int32{2, 3, 3, 0}},
		{"return, vararg", "return\n...", []int32{1, 2, 0}},
		{"local, nil padding", "local a, b\n= 1", []int32{2, 2, 0}},
		{"while, back edge", "while a\ndo\nf()\nend", []int32{1, 1, 1, 3, 3, 3, 0}},
		{"if, escape jumps", "if a then\nf()\nelseif b\nthen\nelse\nend", []int32{1, 1, 1, 2, 2, 2, 3, 3, 3, 4, 0}},
		{"if, not folded into TEST", "if not A\n.x then end", []int32{1, 2, 2, 2, 0}},
		{"sugar call, constructor", "local v = f\n{\n}", []int32{1, 1, 2, 0}},
		{"sugar call, string", "local v = f\n\"s\"", []int32{1, 2, 2, 0}},

		// Found by the second independent review. A positional item followed by k=v fields is pushed at
		// its own separator (closelistfield), not at `}`; only the final SETLIST takes the `}` line.
		{"constructor, positional then k=v", "local t = {A.x\n,\ny=2\n}", []int32{1, 1, 2, 3, 4, 0}},
		{"constructor, positional then [k]=v, semicolons", "local t = {A.x\n;\n[B]=2\n;\n}", []int32{1, 1, 2, 3, 3, 5, 0}},
		{"constructor, k=v between positionals", "local t = {A.x\n,\ny=2\n,\nB.z\n}", []int32{1, 1, 2, 3, 5, 6, 6, 0}},
		// A bracket key is discharged before `]` (yindex's exp2val) and made an RK after it: a deferred
		// GETTABLE in the key lands on the key's last line, a comparison key's LOADBOOLs on the `]` line.
		{"index, bracket key index split", "x = A[B\n.\nc]", []int32{1, 1, 3, 3, 3, 0}},
		{"index, comparison key", "x = A[B ==\nC\n]", []int32{1, 1, 2, 2, 2, 3, 3, 3, 3, 0}},
		{"index target, bracket key split", "A[B\n.c] = 1", []int32{1, 1, 2, 2, 0}},
		{"constructor, comparison key LOADBOOL at =", "local t = {[A == B\n]\n= 1}", []int32{1, 1, 1, 1, 1, 3, 3, 3, 0}},
		// A key with a pending short-circuit chain is put in a register by yindex's exp2val, on the key's
		// last line; only a bare comparison defers its LOADBOOL pair to `]` / `=` (found by the third
		// independent review; a dischargeVars there left the chain to the later exp2RK).
		{"index, short-circuit key", "x = A[B or true\n]", []int32{1, 1, 1, 1, 1, 2, 2, 0}},
		{"index, short-circuit key with constant", "x = A[B and 1\n]", []int32{1, 1, 1, 1, 1, 2, 2, 0}},
		{"index target, chain ending in comparison", "A[B and C == D\n] = 1", []int32{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 2, 0}},
		{"constructor, short-circuit key", "local t = {[B or true\n]\n= 1}", []int32{1, 1, 1, 1, 1, 3, 0}},
		// CLOSURE and its upvalue pseudo-instructions are emitted by pushclosure after `end`.
		{"closure at end", "x = function (\na , b )\nreturn a\nend", []int32{4, 4, 0}},
		{"local function closure at end", "local function f()\nend", []int32{2, 0}},
		// Block CLOSE is emitted by leaveblock before the closing keyword; a while body is its own scope
		// so its CLOSE precedes the back edge (and runs every iteration).
		{"do block close", "do\nlocal x = 1 local function f() return x end\nend", []int32{2, 2, 2, 2, 0}},
		{"while body close before back edge", "while a do\nlocal x = 1 f = function() return x end\nend", []int32{1, 1, 1, 2, 2, 2, 2, 2, 2, 0}},
		{"while break close", "while a do local x = 1 f = function() return x end\nbreak\nend", []int32{1, 1, 1, 1, 1, 1, 1, 2, 2, 2, 2, 0}},
		{"repeat with upvalue", "repeat local x = 1 f = function() return x end\nuntil\na", []int32{1, 1, 1, 1, 3, 3, 3, 3, 3, 3, 3, 0}},
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
