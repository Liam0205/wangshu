package regression

import (
	"strings"
	"testing"
)

// TestParserErrorsMatchLua51 covers the parser half of #282, checked against lparser.c's error
// points one by one:
//   - exprstat: a prefix expression that is a call is a call statement, anything else must be an
//     assignment, so `x x` gives "'=' expected" and `f() = 1` "unexpected symbol near '='";
//   - every name check is error_expected(TK_NAME), "'<name>' expected", quoted; parlist has its own
//     "<name> or '...' expected";
//   - check_match names the opening token and its line when the closing one is missing on another
//     line;
//   - errors report ls->linenumber, the line the scanner is on, so an error after a multi-line long
//     string is on its last line;
//   - errorlimit: "main function has more than 200 local variables" / "function at line N has more
//     than 60 upvalues", raised while parsing, at the name that goes over;
//   - "cannot use '...' outside a vararg function" ends in the near suffix.
//
// Expectations are what lua5.1's loadstring returns for the same source. The three assignment-limit
// rows depend on the C-call depth, LUAI_MAXCCALLS - nCcalls; they come from the 5.1.5 embedded by
// internal/oracle, which runs a chunk at the depth wangshu's Run does (the standalone lua5.1 runs it
// one C call deeper, inside lua_cpcall, and reports one less).
func TestParserErrorsMatchLua51(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"name followed by a name", "x x",
			"[string \"x x\"]:1: '=' expected near 'x'"},
		{"bare name", "x",
			"[string \"x\"]:1: '=' expected near '<eof>'"},
		{"call then =", "f() = 1",
			"[string \"f() = 1\"]:1: unexpected symbol near '='"},
		{"call with table argument then =", "f{} = 1",
			"[string \"f{} = 1\"]:1: unexpected symbol near '='"},
		{"call with string argument then =", "f'' = 1",
			"[string \"f'' = 1\"]:1: unexpected symbol near '='"},
		{"call then name", "f() f",
			"[string \"f() f\"]:1: '=' expected near '<eof>'"},
		{"field", "a.b",
			"[string \"a.b\"]:1: '=' expected near '<eof>'"},
		{"two fields", "a.b.c",
			"[string \"a.b.c\"]:1: '=' expected near '<eof>'"},
		{"index", "a[1]",
			"[string \"a[1]\"]:1: '=' expected near '<eof>'"},
		{"parenthesized target", "(a) = 1",
			"[string \"(a) = 1\"]:1: syntax error near '='"},
		{"parenthesized second target", "a, (b) = 1, 2",
			"[string \"a, (b) = 1, 2\"]:1: syntax error near '='"},
		{"call as second target", "a, f() = 1, 2",
			"[string \"a, f() = 1, 2\"]:1: syntax error near '='"},
		{"goto is a name in 5.1", "goto x",
			"[string \"goto x\"]:1: '=' expected near 'x'"},
		{"a = b = c", "a = b = c",
			"[string \"a = b = c\"]:1: unexpected symbol near '='"},
		{"local without a name", "local",
			"[string \"local\"]:1: '<name>' expected near '<eof>'"},
		{"local name list cut short", "local a,",
			"[string \"local a,\"]:1: '<name>' expected near '<eof>'"},
		{"local function without a name", "local function",
			"[string \"local function\"]:1: '<name>' expected near '<eof>'"},
		{"local function with a number", "local function 1",
			"[string \"local function 1\"]:1: '<name>' expected near '1'"},
		{"function without a name", "function",
			"[string \"function\"]:1: '<name>' expected near '<eof>'"},
		{"function a.", "function a.",
			"[string \"function a.\"]:1: '<name>' expected near '<eof>'"},
		{"function a:", "function a:",
			"[string \"function a:\"]:1: '<name>' expected near '<eof>'"},
		{"for without a name", "for",
			"[string \"for\"]:1: '<name>' expected near '<eof>'"},
		{"for with a number", "for 1",
			"[string \"for 1\"]:1: '<name>' expected near '1'"},
		{"for a b", "for a b",
			"[string \"for a b\"]:1: '=' or 'in' expected near 'b'"},
		{"generic for name list cut short", "for a, in b do end",
			"[string \"for a, in b do end\"]:1: '<name>' expected near 'in'"},
		{"field without a name", "x = a.",
			"[string \"x = a.\"]:1: '<name>' expected near '<eof>'"},
		{"method without a name", "x = a:",
			"[string \"x = a:\"]:1: '<name>' expected near '<eof>'"},
		{"parameter list cut short", "x = function(",
			"[string \"x = function(\"]:1: <name> or '...' expected near '<eof>'"},
		{"parameter after a comma", "x = function(a,",
			"[string \"x = function(a,\"]:1: <name> or '...' expected near '<eof>'"},
		{"parameter list with an empty slot", "x = function(a,,b) end",
			"[string \"x = function(a,,b) end\"]:1: <name> or '...' expected near ','"},
		{"vararg outside a vararg function", "function f() x = ... end",
			"[string \"function f() x = ... end\"]:1: cannot use '...' outside a vararg function near '...'"},
		{"unclosed if", "if x then\n\n y()",
			"[string \"if x then...\"]:3: 'end' expected (to close 'if' at line 1) near '<eof>'"},
		{"unclosed if on one line", "if x then y()",
			"[string \"if x then y()\"]:1: 'end' expected near '<eof>'"},
		{"unclosed while", "while x do\n\n",
			"[string \"while x do...\"]:3: 'end' expected (to close 'while' at line 1) near '<eof>'"},
		{"unclosed repeat", "repeat\n\n x()",
			"[string \"repeat...\"]:3: 'until' expected (to close 'repeat' at line 1) near '<eof>'"},
		{"unclosed call", "f(\n\n1",
			"[string \"f(...\"]:3: ')' expected (to close '(' at line 1) near '<eof>'"},
		{"unclosed constructor", "x = {\n\n1",
			"[string \"x = {...\"]:3: '}' expected (to close '{' at line 1) near '<eof>'"},
		{"unclosed parenthesis", "x = (\n\n1",
			"[string \"x = (...\"]:3: ')' expected (to close '(' at line 1) near '<eof>'"},
		{"unclosed for", "for i = 1, 2 do\n\n",
			"[string \"for i = 1, 2 do...\"]:3: 'end' expected (to close 'for' at line 1) near '<eof>'"},
		{"unclosed do", "do\n\n x()",
			"[string \"do...\"]:3: 'end' expected (to close 'do' at line 1) near '<eof>'"},
		{"unclosed function", "function f()\n\n x()",
			"[string \"function f()...\"]:3: 'end' expected (to close 'function' at line 1) near '<eof>'"},
		{"unclosed call after a long string", "f(\n[[a\nb]] x",
			"[string \"f(...\"]:3: ')' expected (to close '(' at line 1) near 'x'"},
		{"error line is where the scanner stands", "x = {\n[[a\nb]] [[c\nd]]",
			"[string \"x = {...\"]:4: '}' expected (to close '{' at line 1) near '[[c\nd]]'"},
		{"name expected after a long string", "local\n\n[[a\nb]]",
			"[string \"local...\"]:4: '<name>' expected near '[[a\nb]]'"},
		{"too many locals in one statement", "local " + strings.Repeat("a,", 200) + "\n\na",
			"[string \"local a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a...\"]:3: main function has more than 200 local variables"},
		{"too many locals reported at the 201st name", "local " + strings.Repeat("a,", 199) + "a,\n\nb\n= 1",
			"[string \"local a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a...\"]:4: main function has more than 200 local variables"},
		{"too many locals in a function", "local function f()\n  local " + strings.Repeat("a,", 200) + "a\nend",
			"[string \"local function f()...\"]:3: function at line 1 has more than 200 local variables"},
		{"too many locals in a vararg method", "local t = {}\nfunction t.f(...)\n" + strings.Repeat("local a\n", 200) + "end",
			"[string \"local t = {}...\"]:203: function at line 2 has more than 200 local variables"},
		{"too many parameters", "x = function(" + strings.Repeat("a,", 200) + "b) end",
			"[string \"x = function(a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,...\"]:1: function at line 1 has more than 200 local variables"},
		{"numeric for control variables count", "for i = 1, 2 do\n" + strings.Repeat("local a\n", 197) + "end",
			"[string \"for i = 1, 2 do...\"]:199: main function has more than 200 local variables"},
		{"numeric for just under the limit", "for i = 1, 2 do\n" + strings.Repeat("local a\n", 196) + "end",
			"ok"},
		{"generic for control variables count", "for k, v in x do\n" + strings.Repeat("local a\n", 196) + "end",
			"[string \"for k, v in x do...\"]:198: main function has more than 200 local variables"},
		{"generic for just under the limit", "for k, v in x do\n" + strings.Repeat("local a\n", 195) + "end",
			"ok"},
		{"for after 199 locals", strings.Repeat("local a\n", 199) + "for i = 1, 2 do end",
			"[string \"local a...\"]:200: main function has more than 200 local variables"},
		{"for after 197 locals", strings.Repeat("local a\n", 197) + "for i = 1, 2 do end",
			"[string \"local a...\"]:198: main function has more than 200 local variables"},
		{"60 upvalues", "local u1,u2,u3,u4,u5,u6,u7,u8,u9,u10,u11,u12,u13,u14,u15,u16,u17,u18,u19,u20,u21,u22,u23,u24,u25,u26,u27,u28,u29,u30,u31,u32,u33,u34,u35,u36,u37,u38,u39,u40,u41,u42,u43,u44,u45,u46,u47,u48,u49,u50,u51,u52,u53,u54,u55,u56,u57,u58,u59,u60 = 1\nreturn function() return u1+u2+u3+u4+u5+u6+u7+u8+u9+u10+u11+u12+u13+u14+u15+u16+u17+u18+u19+u20+u21+u22+u23+u24+u25+u26+u27+u28+u29+u30+u31+u32+u33+u34+u35+u36+u37+u38+u39+u40+u41+u42+u43+u44+u45+u46+u47+u48+u49+u50+u51+u52+u53+u54+u55+u56+u57+u58+u59+u60 end",
			"ok"},
		{"61 upvalues", "local u1,u2,u3,u4,u5,u6,u7,u8,u9,u10,u11,u12,u13,u14,u15,u16,u17,u18,u19,u20,u21,u22,u23,u24,u25,u26,u27,u28,u29,u30,u31,u32,u33,u34,u35,u36,u37,u38,u39,u40,u41,u42,u43,u44,u45,u46,u47,u48,u49,u50,u51,u52,u53,u54,u55,u56,u57,u58,u59,u60,u61 = 1\nreturn function()\n return u1+u2+u3+u4+u5+u6+u7+u8+u9+u10+u11+u12+u13+u14+u15+u16+u17+u18+u19+u20+u21+u22+u23+u24+u25+u26+u27+u28+u29+u30+u31+u32+u33+u34+u35+u36+u37+u38+u39+u40+u41+u42+u43+u44+u45+u46+u47+u48+u49+u50+u51+u52+u53+u54+u55+u56+u57+u58+u59+u60+u61 end",
			"[string \"local u1,u2,u3,u4,u5,u6,u7,u8,u9,u10,u11,u12,u13,u14,u15,u16,u1...\"]:3: function at line 2 has more than 60 upvalues"},
		{"61st upvalue on a later line", "local u1,u2,u3,u4,u5,u6,u7,u8,u9,u10,u11,u12,u13,u14,u15,u16,u17,u18,u19,u20,u21,u22,u23,u24,u25,u26,u27,u28,u29,u30,u31,u32,u33,u34,u35,u36,u37,u38,u39,u40,u41,u42,u43,u44,u45,u46,u47,u48,u49,u50,u51,u52,u53,u54,u55,u56,u57,u58,u59,u60,u61 = 1\nreturn function()\n return u1+u2+u3+u4+u5+u6+u7+u8+u9+u10+u11+u12+u13+u14+u15+u16+u17+u18+u19+u20+u21+u22+u23+u24+u25+u26+u27+u28+u29+u30+u31+u32+u33+u34+u35+u36+u37+u38+u39+u40+u41+u42+u43+u44+u45+u46+u47+u48+u49+u50+u51+u52+u53+u54+u55+u56+u57+u58+u59+u60+\nu61 end",
			"[string \"local u1,u2,u3,u4,u5,u6,u7,u8,u9,u10,u11,u12,u13,u14,u15,u16,u1...\"]:4: function at line 2 has more than 60 upvalues"},
		{"an intermediate function goes over the upvalue limit", "local u1,u2,u3,u4,u5,u6,u7,u8,u9,u10,u11,u12,u13,u14,u15,u16,u17,u18,u19,u20,u21,u22,u23,u24,u25,u26,u27,u28,u29,u30,u31,u32,u33,u34,u35,u36,u37,u38,u39,u40,u41,u42,u43,u44,u45,u46,u47,u48,u49,u50,u51,u52,u53,u54,u55,u56,u57,u58,u59,u60,u61 = 1\nreturn function()\n return function() return u1+u2+u3+u4+u5+u6+u7+u8+u9+u10+u11+u12+u13+u14+u15+u16+u17+u18+u19+u20+u21+u22+u23+u24+u25+u26+u27+u28+u29+u30+u31+u32+u33+u34+u35+u36+u37+u38+u39+u40+u41+u42+u43+u44+u45+u46+u47+u48+u49+u50+u51+u52+u53+u54+u55+u56+u57+u58+u59+u60+u61 end end",
			"[string \"local u1,u2,u3,u4,u5,u6,u7,u8,u9,u10,u11,u12,u13,u14,u15,u16,u1...\"]:3: function at line 2 has more than 60 upvalues"},
		{"the same outer local twice is one upvalue", "local " + strings.Repeat("a,", 60) + "z\nreturn function() return " + strings.Repeat("a+", 60) + "z end",
			"ok"},
		{"249 return values", "return " + strings.Repeat("1,", 248) + "1",
			"ok"},
		{"assignment target limit", strings.Repeat("a,", 199) + "a = 1",
			"[string \"a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a...\"]:1: main function has more than 198 variables in assignment"},
		{"assignment target limit inside blocks", "do do " + strings.Repeat("a,", 198) + "a = 1 end end",
			"[string \"do do a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a,a...\"]:1: main function has more than 196 variables in assignment"},
		{"assignment target limit inside a function", "local function f()\n" + strings.Repeat("a,", 198) + "a = 1 end",
			"[string \"local function f()...\"]:2: function at line 1 has more than 197 variables in assignment"},
	} {
		if got := loadMessage(t, tc.src); got != tc.want {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
}
