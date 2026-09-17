package regression

import (
	"testing"

	"github.com/Liam0205/wangshu/test/testutil"
)

// TestOperatorErrorLineIsOperandEnd covers #262 (fuzz seed `print(pcall(function() error("boom"%<CR>0) end))`):
// the line an arithmetic, comparison, concat, unary, or store error is reported on must be PUC's -- the
// line of the operand's LAST token, not the operator's line. The seed used a bare carriage return as its
// newline; the lexer already counted that as a line, so the defect was in codegen, and \n reproduces it
// the same way.
//
// PUC stamps the operation with ls->lastline at luaK_posfix time (the right operand's end), the store with
// lastline at luaK_storevar time (the statement's end), and FORPREP with lastline after `do`. Each case here
// asserts the full pcall message, line included, against `lua5.1` output on the same source. Every case in
// this table fails on the base commit with a lower line.
func TestOperatorErrorLineIsOperandEnd(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"#262 seed, arith right operand on next line",
			"local _, e = pcall(function() error(\"boom\"%\n0) end) return e",
			`[string "test"]:2: attempt to perform arithmetic on a string value`},
		{"arith, both operands moved",
			"local _, e = pcall(function() return {}\n%\n1 end) return e",
			`[string "test"]:3: attempt to perform arithmetic on a table value`},
		{"compare, right operand moved",
			"local _, e = pcall(function() return 1 <\n\"a\" end) return e",
			`[string "test"]:2: attempt to compare number with string`},
		{"concat, right operand moved",
			"local _, e = pcall(function() return \"a\"\n..\n{} end) return e",
			`[string "test"]:3: attempt to concatenate a table value`},
		{"and, right index moved",
			"local _, e = pcall(function() return 1 and A\n.x end) return e",
			`[string "test"]:2: attempt to index global 'A' (a nil value)`},
		{"unary minus, operand index moved",
			"local _, e = pcall(function() return -A\n.x end) return e",
			`[string "test"]:2: attempt to index global 'A' (a nil value)`},
		{"length, operand index moved",
			"local _, e = pcall(function() return #A\n.x end) return e",
			`[string "test"]:2: attempt to index global 'A' (a nil value)`},
		// The store side: SETTABLE on a nil object raises at the statement's END.
		{"settable on nil, rhs spans lines",
			"local _, e = pcall(function() x.y = 1\n+\n1 end) return e",
			`[string "test"]:3: attempt to index global 'x' (a nil value)`},
		{"settable, key split across lines",
			"local _, e = pcall(function() x[1\n] = 1 end) return e",
			`[string "test"]:2: attempt to index global 'x' (a nil value)`},
		// error(msg, 2) inside __newindex blames the store's line.
		{"__newindex level 2 blames the store line",
			"local t = setmetatable({}, {__newindex = function() error(\"nx\", 2) end})\n" +
				"local _, e = pcall(function() t.y = 1\n+\n1 end) return e",
			`[string "test"]:4: nx`},
		// Numeric for: FORPREP is emitted after `do`, so its checks blame that line.
		{"for initial value, do on next line",
			"local _, e = pcall(function() for i = \"x\", 2\ndo end end) return e",
			`[string "test"]:2: 'for' initial value must be a number`},
		{"for limit, blank lines before do",
			"local _, e = pcall(function() for i = 1, \"x\"\n\ndo end end) return e",
			`[string "test"]:3: 'for' limit must be a number`},
		// Found by the first independent review: TFORLOOP carries the iterator list's first line, and a
		// constructor field's SETTABLE the value's last line.
		{"generic for, non-callable generator on the next line",
			"local _, e = pcall(function() for k in\nnil do end end) return e",
			`[string "test"]:2: attempt to call a nil value`},
		{"constructor, nil key spanning lines",
			"local _, e = pcall(function() local t = {\n[nil]\n=\n1} end) return e",
			`[string "test"]:4: table index is nil`},
		{"constructor, NaN key spanning lines",
			"local _, e = pcall(function() local t = {\n[0/0]\n=\n1} end) return e",
			`[string "test"]:4: table index is NaN`},
		{"constructor, positional index after lookahead",
			"local _, e = pcall(function() local t = {\nA\n.x\n} end) return e",
			`[string "test"]:4: attempt to index global 'A' (a nil value)`},
		{"method call, receiver index split",
			"local _, e = pcall(function() A.b\n:m() end) return e",
			`[string "test"]:2: attempt to index global 'A' (a nil value)`},
		{"return, index split",
			"local _, e = pcall(function() return\nA\n.x end) return e",
			`[string "test"]:3: attempt to index global 'A' (a nil value)`},
	} {
		if got := testutil.RunOne(t, tc.src).Str(); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
