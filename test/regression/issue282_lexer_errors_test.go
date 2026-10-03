package regression

import (
	"testing"

	wangshu "github.com/Liam0205/wangshu"
)

// TestLexerErrorsMatchLua51 covers the lexer half of #282. llex returns a character it does not know
// as a token of its own, so the parser reports it ("unexpected symbol near '$'", a control byte as
// char(N), a NUL with no token at all). Every lexer error goes through luaX_lexerror with a token, so
// it ends in " near '<text>'", where the text is "<eof>" or the scan buffer so far -- the decoded
// contents of a string, not its source. An unfinished long string reports the line where the input
// ended. wangshu had its own wording for most of these and no near suffix. Expectations are what
// lua5.1's loadstring returns for the same source.
func TestLexerErrorsMatchLua51(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"unknown character @", "@@",
			"@:1: unexpected symbol near '@'"},
		{"unknown character $", "x = $",
			"[string \"x = $\"]:1: unexpected symbol near '$'"},
		{"unknown character backquote", "x = 1 ` 2",
			"[string \"x = 1 ` 2\"]:1: unexpected symbol near '`'"},
		{"lone ~", "x = ~",
			"[string \"x = ~\"]:1: unexpected symbol near '~'"},
		{"lone ~ before an operand", "x = ~ 1",
			"[string \"x = ~ 1\"]:1: unexpected symbol near '~'"},
		{"unknown character inside a call", "f(1 $",
			"[string \"f(1 $\"]:1: ')' expected near '$'"},
		{"control character", "\x01",
			"[string \"\x01\"]:1: unexpected symbol near 'char(1)'"},
		{"DEL", "x = \x7f",
			"[string \"x = \x7f\"]:1: unexpected symbol near 'char(127)'"},
		{"byte above 127", "x = \x80",
			"[string \"x = \x80\"]:1: unexpected symbol near '\x80'"},
		{"NUL byte names no token", "x = 1 \x00 2",
			"[string \"x = 1 \"]:1: unexpected symbol"},
		{"NUL inside a name list", "local \x01",
			"[string \"local \x01\"]:1: '<name>' expected near 'char(1)'"},
		{"unfinished string at end of input", "x = 'abc",
			"[string \"x = 'abc\"]:1: unfinished string near '<eof>'"},
		{"unfinished string at a newline", "x = 'abc\nx",
			"[string \"x = 'abc...\"]:1: unfinished string near ''abc'"},
		{"unfinished string shows decoded escapes", "x = 'a\\65\\tb\nx",
			"[string \"x = 'a\\65\\tb...\"]:1: unfinished string near ''aA\tb'"},
		{"unfinished string after a backslash-newline", "x = \"a\\\n\n",
			"[string \"x = \"a\\...\"]:2: unfinished string near '\"a\n'"},
		{"unfinished string ending in a backslash", "x = \"\\",
			"[string \"x = \"\\\"]:1: unfinished string near '<eof>'"},
		{"escape too large", "x = \"\\300\"",
			"[string \"x = \"\\300\"\"]:1: escape sequence too large near '\"'"},
		{"escape too large after text", "x = \"a\\300",
			"[string \"x = \"a\\300\"]:1: escape sequence too large near '\"a'"},
		{"escape too large with four digits", "x = '\\9999'",
			"[string \"x = '\\9999'\"]:1: escape sequence too large near '''"},
		{"unfinished long string reports where input ends", "x = [[abc\n\n\nd",
			"[string \"x = [[abc...\"]:4: unfinished long string near '<eof>'"},
		{"unfinished long string, level 2", "x = [==[\na\n]=]\n\n",
			"[string \"x = [==[...\"]:5: unfinished long string near '<eof>'"},
		{"unfinished long comment", "--[[abc\n\n\nd",
			"[string \"--[[abc...\"]:4: unfinished long comment near '<eof>'"},
		{"unfinished long comment, level 2", "--[==[ x ]=]",
			"[string \"--[==[ x ]=]\"]:1: unfinished long comment near '<eof>'"},
		{"invalid long string delimiter", "x = [=x",
			"[string \"x = [=x\"]:1: invalid long string delimiter near '[='"},
		{"invalid long string delimiter, two =", "x = [==x",
			"[string \"x = [==x\"]:1: invalid long string delimiter near '[=='"},
		{"nested long string", "x = [[ a [[ b ]] ]]",
			"[string \"x = [[ a [[ b ]] ]]\"]:1: nesting of [[...]] is deprecated near '['"},
		{"malformed number", "x = 3.4.5",
			"[string \"x = 3.4.5\"]:1: malformed number near '3.4.5'"},
		{"malformed hex", "x = 0x1g",
			"[string \"x = 0x1g\"]:1: malformed number near '0x1g'"},
		{"a NUL ends a numeral", "return 1\x00abc",
			"ok"},
		{"a NUL takes the exponent's place", "return 1e\x00",
			"[string \"return 1e\"]:1: malformed number near '1e'"},
		{"string token shows its decoded contents", "x = 1 'a\\nb\\065'",
			"[string \"x = 1 'a\\nb\\065'\"]:1: unexpected symbol near ''a\nbA''"},
		{"long string token drops the newline after the opener", "x = 1 [==[\nab]==]",
			"[string \"x = 1 [==[...\"]:2: unexpected symbol near '[==[ab]==]'"},
		{"long string token shows CR LF as LF", "x = 1 [[a\r\nb]]",
			"[string \"x = 1 [[a...\"]:2: unexpected symbol near '[[a\nb]]'"},
		{"number token as written", "x = 1 0x1F",
			"[string \"x = 1 0x1F\"]:1: unexpected symbol near '0x1F'"},
	} {
		if got := loadMessage(t, tc.src); got != tc.want {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
}

// TestLongStringNewlinesReadAsOne covers the value half: read_long_string saves every newline
// sequence as a single "\n", so a CR LF or LF CR pair in a long string is one byte, while CR CR is
// two newlines. wangshu kept the source bytes. Expectations are lua5.1's.
func TestLongStringNewlinesReadAsOne(t *testing.T) {
	src := "print(#[[a\r\nb]], #[[a\n\rb]], #[[a\r\rb]], #[[a\rb]], #[[a\n\nb]], [[a\r\nb]] == 'a\\nb')\n" +
		"print(#'a\\\r\nb', #'a\\\n\rb')\n"
	if got, want := printedBy(t, wangshu.Options{}, src), "3\t3\t4\t3\t4\ttrue\n3\t3\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
