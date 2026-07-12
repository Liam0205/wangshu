package lex

import (
	"math"
	"strings"
	"testing"

	"github.com/Liam0205/wangshu/internal/frontend/token"
)

// scan all 把整段源码扫成 token 列表(忽略 EOF)。
func scanAll(t *testing.T, src string) []token.Token {
	t.Helper()
	lx := New([]byte(src), "test")
	var out []token.Token
	for {
		tk, err := lx.Next()
		if err != nil {
			t.Fatalf("lex error: %v", err)
		}
		if tk.Kind == token.EOF {
			break
		}
		out = append(out, tk)
	}
	return out
}

func TestKeywords(t *testing.T) {
	src := "and break do else elseif end false for function if in local nil not or repeat return then true until while"
	toks := scanAll(t, src)
	want := []token.Kind{
		token.KW_AND, token.KW_BREAK, token.KW_DO, token.KW_ELSE, token.KW_ELSEIF,
		token.KW_END, token.KW_FALSE, token.KW_FOR, token.KW_FUNCTION, token.KW_IF,
		token.KW_IN, token.KW_LOCAL, token.KW_NIL, token.KW_NOT, token.KW_OR,
		token.KW_REPEAT, token.KW_RETURN, token.KW_THEN, token.KW_TRUE,
		token.KW_UNTIL, token.KW_WHILE,
	}
	if len(toks) != len(want) {
		t.Fatalf("got %d tokens, want %d", len(toks), len(want))
	}
	for i, k := range want {
		if toks[i].Kind != k {
			t.Errorf("tok[%d] = %s, want %s", i, token.KindName(toks[i].Kind), token.KindName(k))
		}
	}
}

func TestIdentifierVsKeyword(t *testing.T) {
	toks := scanAll(t, "endx _foo bar1 nil")
	wantKinds := []token.Kind{token.NAME, token.NAME, token.NAME, token.KW_NIL}
	for i, k := range wantKinds {
		if toks[i].Kind != k {
			t.Errorf("tok[%d] kind: got %s want %s", i, token.KindName(toks[i].Kind), token.KindName(k))
		}
	}
	if toks[0].Str != "endx" || toks[1].Str != "_foo" || toks[2].Str != "bar1" {
		t.Errorf("identifier names: %q %q %q", toks[0].Str, toks[1].Str, toks[2].Str)
	}
}

func TestNumbers(t *testing.T) {
	cases := []struct {
		src string
		num float64
	}{
		{"0", 0},
		{"42", 42},
		{"3.14", 3.14},
		{"1e3", 1000},
		{"1.5E-2", 0.015},
		{".5", 0.5},
		{"0x10", 16},
		{"0xFF", 255},
	}
	for _, c := range cases {
		toks := scanAll(t, c.src)
		if len(toks) != 1 || toks[0].Kind != token.NUMBER {
			t.Fatalf("%q → %v", c.src, toks)
		}
		if toks[0].Num != c.num {
			t.Errorf("%q: got %v, want %v", c.src, toks[0].Num, c.num)
		}
	}
}

func TestNumberMalformed(t *testing.T) {
	// 官方 read_numeral 是「贪心吃完 + 整体校验」:数字后紧跟字母/多余的点
	// 整段进入校验并报 malformed number。结构化扫描"吃不动就停"会把这些
	// 拆成相邻 token 静默接受——`return 1or 2` 曾被错误执行返回 1。
	for _, s := range []string{
		"0x", "1ee", "1e+",
		"1or", "3..5", "1.2.3", "1abc",
		"0x.8", "1_000", "0x1p+4", "0xGG",
	} {
		lx := New([]byte(s), "test")
		_, err := lx.Next()
		if err == nil {
			t.Errorf("%q: expected lex error (malformed number)", s)
		}
	}
}

func TestNumberGreedyEdges(t *testing.T) {
	// 与 oracle 5.1.5 逐项核对的合法边角:
	// 1. = 1 / 1.e2 = 100(尾点合法);0x1p4 = 16(系统 strtod 的 C99 hex float);
	// 超 64-bit hex 取浮点近似;10e500 溢出取 +Inf。
	cases := []struct {
		src string
		num float64
	}{
		{"1.", 1},
		{"1.e2", 100},
		{"0x1p4", 16},
		{"0xFFFFFFFFFFFFFFFFF", 2.9514790517935283e+20},
		{"10e500", math.Inf(1)},
	}
	for _, c := range cases {
		toks := scanAll(t, c.src)
		if len(toks) != 1 || toks[0].Kind != token.NUMBER {
			t.Fatalf("%q → %v", c.src, toks)
		}
		if toks[0].Num != c.num {
			t.Errorf("%q: got %v, want %v", c.src, toks[0].Num, c.num)
		}
	}
	// `1e5.5`:贪心段在第二个点前停止(指数后不吃点),官方同样是
	// NUMBER(1e5) 后跟 parser 级 syntax error,词法层不报错。
	toks := scanAll(t, "1e5.5")
	if len(toks) != 2 || toks[0].Num != 1e5 {
		t.Errorf("1e5.5: got %v, want NUMBER(100000) NUMBER(0.5)", toks)
	}
}

func TestSymbols(t *testing.T) {
	src := "+-*/%^# = == ~= < <= > >= .. ... ( ) { } [ ] ; : , ."
	toks := scanAll(t, src)
	want := []token.Kind{
		token.PLUS, token.MINUS, token.STAR, token.SLASH, token.PERCENT, token.CARET, token.HASH,
		token.EQ, token.EQEQ, token.NEQ, token.LT, token.LE, token.GT, token.GE,
		token.CONCAT, token.ELLIPSIS,
		token.LPAREN, token.RPAREN, token.LBRACE, token.RBRACE, token.LBRACK, token.RBRACK,
		token.SEMI, token.COLON, token.COMMA, token.DOT,
	}
	if len(toks) != len(want) {
		t.Fatalf("got %d, want %d", len(toks), len(want))
	}
	for i, k := range want {
		if toks[i].Kind != k {
			t.Errorf("tok[%d] = %s, want %s", i, token.KindName(toks[i].Kind), token.KindName(k))
		}
	}
}

func TestShortString(t *testing.T) {
	cases := []struct {
		src  string
		want string
	}{
		{`"hello"`, "hello"},
		{`'world'`, "world"},
		{`"a\nb"`, "a\nb"},
		{`"\t\\\""`, "\t\\\""},
		{`"\65"`, "A"},
		{`"\255"`, "\xff"},
	}
	for _, c := range cases {
		toks := scanAll(t, c.src)
		if len(toks) != 1 || toks[0].Kind != token.STRING {
			t.Fatalf("%q → %v", c.src, toks)
		}
		if toks[0].Str != c.want {
			t.Errorf("%q: got %q, want %q", c.src, toks[0].Str, c.want)
		}
	}
}

func TestStringEscapeErrors(t *testing.T) {
	for _, s := range []string{`"unterminated`, "\"\nnewline\"", `"\999"`} {
		lx := New([]byte(s), "test")
		_, err := lx.Next()
		if err == nil {
			t.Errorf("%q: expected lex error", s)
		}
	}
}

// PUC 5.1 对未知转义按字面放行(llex.c read_string default 分支
// save_and_next):"\A" == "A","\x41" == "x41"(hex 转义是 5.2 特性,
// 5.1 里 x 只是普通未知转义)。cgo oracle 差分 fuzz 撞出旧行为(报
// invalid escape sequence)与官方 5.1.5 的分歧。
func TestStringUnknownEscapePassthrough(t *testing.T) {
	cases := []struct{ src, want string }{
		{`"\A"`, "A"},
		{`"\x41"`, "x41"},
		{`"\!"`, "!"},
	}
	for _, c := range cases {
		lx := New([]byte(c.src), "test")
		tok, err := lx.Next()
		if err != nil {
			t.Errorf("%q: unexpected error: %v", c.src, err)
			continue
		}
		if tok.Str != c.want {
			t.Errorf("%q: got %q, want %q", c.src, tok.Str, c.want)
		}
	}
}

func TestLongString(t *testing.T) {
	cases := []struct {
		src, want string
	}{
		{`[[hello]]`, "hello"},
		{`[==[a]=]b]==]`, "a]=]b"},
		// 长字符串内容紧随的换行被丢弃。
		{"[[\nfoo]]", "foo"},
		{"[[line1\nline2]]", "line1\nline2"},
	}
	for _, c := range cases {
		toks := scanAll(t, c.src)
		if len(toks) != 1 || toks[0].Kind != token.STRING || toks[0].Str != c.want {
			t.Errorf("%q: got %v, want STRING(%q)", c.src, toks, c.want)
		}
	}
}

func TestComments(t *testing.T) {
	src := `-- short comment
local x = 1 --[[ long
multiline
comment ]] return x`
	toks := scanAll(t, src)
	wantKinds := []token.Kind{
		token.KW_LOCAL, token.NAME, token.EQ, token.NUMBER, token.KW_RETURN, token.NAME,
	}
	if len(toks) != len(wantKinds) {
		t.Fatalf("got %d tokens, want %d", len(toks), len(wantKinds))
	}
	for i, k := range wantKinds {
		if toks[i].Kind != k {
			t.Errorf("tok[%d] = %s, want %s", i, token.KindName(toks[i].Kind), token.KindName(k))
		}
	}
}

func TestLineNumbers(t *testing.T) {
	// 四种换行序列各计 1 行(03 §9)。
	for _, nl := range []string{"\n", "\r", "\r\n", "\n\r"} {
		src := strings.Join([]string{"a", "b", "c"}, nl)
		toks := scanAll(t, src)
		if len(toks) != 3 {
			t.Fatalf("nl=%q: got %d tokens", nl, len(toks))
		}
		if toks[0].Line != 1 || toks[1].Line != 2 || toks[2].Line != 3 {
			t.Errorf("nl=%q: lines = %d/%d/%d", nl, toks[0].Line, toks[1].Line, toks[2].Line)
		}
	}
}

func TestErrorPrefix(t *testing.T) {
	lx := New([]byte("\n\nlocal x = ?"), "myfile.lua")
	for {
		tk, err := lx.Next()
		if err != nil {
			lerr, ok := err.(*Error)
			if !ok {
				t.Fatalf("expected *lex.Error, got %T", err)
			}
			if lerr.Source != "myfile.lua" {
				t.Errorf("source: %q", lerr.Source)
			}
			if lerr.Line != 3 {
				t.Errorf("line: %d, want 3", lerr.Line)
			}
			return
		}
		if tk.Kind == token.EOF {
			t.Fatal("expected error before EOF")
		}
	}
}
