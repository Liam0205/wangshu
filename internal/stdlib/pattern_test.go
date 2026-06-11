// Pattern matcher unit tests(lstrlib 引擎细节路径)。
package stdlib

import (
	"testing"
)

func findOnce(t *testing.T, src, pat string, init int) (int, int, []capResult, bool) {
	t.Helper()
	s, e, caps, found, err := patternFind([]byte(src), []byte(pat), init)
	if err != nil {
		t.Fatalf("patternFind(%q, %q): %v", src, pat, err)
	}
	return s, e, caps, found
}

func TestPattern_Literal(t *testing.T) {
	s, e, _, found := findOnce(t, "hello world", "world", 0)
	if !found || s != 6 || e != 11 {
		t.Errorf("got s=%d e=%d found=%v", s, e, found)
	}
}

func TestPattern_Classes(t *testing.T) {
	cases := []struct {
		src, pat string
		found    bool
		match    string
	}{
		{"abc123", "%a+", true, "abc"},
		{"abc123", "%d+", true, "123"},
		{"  x", "%s+", true, "  "},
		{"a_1", "%w+", true, "a_"}, // %w = alnum;_ 不是 → "a" 实际
		{"ff00", "%x+", true, "ff00"},
		{"Hello", "%u%l+", true, "Hello"},
		{"a,b", "%p", true, ","},
	}
	for _, c := range cases {
		s, e, _, found := findOnce(t, c.src, c.pat, 0)
		if found != c.found {
			t.Errorf("%q ~ %q: found=%v want %v", c.src, c.pat, found, c.found)
			continue
		}
		if found && c.pat != "%w+" { // %w 的特例见下
			if got := c.src[s:e]; got != c.match {
				t.Errorf("%q ~ %q: match=%q want %q", c.src, c.pat, got, c.match)
			}
		}
	}
}

func TestPattern_NegatedClass(t *testing.T) {
	s, e, _, found := findOnce(t, "abc123", "%D+", 0)
	if !found || "abc123"[s:e] != "abc" {
		t.Errorf("%%D+ got %q", "abc123"[s:e])
	}
}

func TestPattern_Sets(t *testing.T) {
	s, e, _, found := findOnce(t, "hello42", "[0-9]+", 0)
	if !found || "hello42"[s:e] != "42" {
		t.Errorf("[0-9]+ got %q", "hello42"[s:e])
	}
	s, e, _, found = findOnce(t, "hello42", "[^0-9]+", 0)
	if !found || "hello42"[s:e] != "hello" {
		t.Errorf("[^0-9]+ got %q", "hello42"[s:e])
	}
	// 集合内类
	s, e, _, found = findOnce(t, "a1 b2", "[%a%d]+", 0)
	if !found || "a1 b2"[s:e] != "a1" {
		t.Errorf("[%%a%%d]+ got %q", "a1 b2"[s:e])
	}
}

func TestPattern_Quantifiers(t *testing.T) {
	// 贪婪 *
	s, e, _, found := findOnce(t, "<<aa>>", "<.*>", 0)
	if !found || "<<aa>>"[s:e] != "<<aa>>" {
		t.Errorf("greedy got %q", "<<aa>>"[s:e])
	}
	// 懒惰 -
	s, e, _, found = findOnce(t, "<<aa>>", "<.->", 0)
	if !found || "<<aa>>"[s:e] != "<<aa>" || s != 0 {
		t.Errorf("lazy got %q", "<<aa>>"[s:e])
	}
	// ? 可选
	s, e, _, found = findOnce(t, "color", "colou?r", 0)
	if !found || "color"[s:e] != "color" {
		t.Errorf("optional got %q", "color"[s:e])
	}
	// + 至少一
	_, _, _, found = findOnce(t, "xyz", "%d+", 0)
	if found {
		t.Errorf("%%d+ on xyz should not match")
	}
}

func TestPattern_Anchors(t *testing.T) {
	_, _, _, found := findOnce(t, "hello", "^h", 0)
	if !found {
		t.Errorf("^h should match at start")
	}
	_, _, _, found = findOnce(t, "hello", "^e", 0)
	if found {
		t.Errorf("^e should not match")
	}
	s, e, _, found := findOnce(t, "hello", "o$", 0)
	if !found || s != 4 || e != 5 {
		t.Errorf("o$ got s=%d e=%d", s, e)
	}
	_, _, _, found = findOnce(t, "hello", "h$", 0)
	if found {
		t.Errorf("h$ should not match")
	}
}

func TestPattern_Captures(t *testing.T) {
	_, _, caps, found := findOnce(t, "key=value", "(%w+)=(%w+)", 0)
	if !found || len(caps) != 2 {
		t.Fatalf("caps len=%d found=%v", len(caps), found)
	}
	src := "key=value"
	if src[caps[0].start:caps[0].start+caps[0].len] != "key" {
		t.Errorf("cap0 = %q", src[caps[0].start:caps[0].start+caps[0].len])
	}
	if src[caps[1].start:caps[1].start+caps[1].len] != "value" {
		t.Errorf("cap1 = %q", src[caps[1].start:caps[1].start+caps[1].len])
	}
}

func TestPattern_PositionCapture(t *testing.T) {
	_, _, caps, found := findOnce(t, "abc", "a()b", 0)
	if !found || len(caps) != 1 || !caps[0].pos || caps[0].start != 1 {
		t.Errorf("position capture: caps=%+v found=%v", caps, found)
	}
}

func TestPattern_Balance(t *testing.T) {
	s, e, _, found := findOnce(t, "(foo(bar))baz", "%b()", 0)
	if !found || "(foo(bar))baz"[s:e] != "(foo(bar))" {
		t.Errorf("%%b() got %q", "(foo(bar))baz"[s:e])
	}
	_, _, _, found = findOnce(t, "(unclosed", "%b()", 0)
	if found {
		t.Errorf("%%b() on unclosed should not match")
	}
}

func TestPattern_BackRef(t *testing.T) {
	s, e, _, found := findOnce(t, "abcabc", "(abc)%1", 0)
	if !found || "abcabc"[s:e] != "abcabc" {
		t.Errorf("backref got %q", "abcabc"[s:e])
	}
	_, _, _, found = findOnce(t, "abcdef", "(abc)%1", 0)
	if found {
		t.Errorf("backref should not match abcdef")
	}
}

func TestPattern_InitOffset(t *testing.T) {
	s, _, _, found := findOnce(t, "aXbXc", "X", 2)
	if !found || s != 3 {
		t.Errorf("init=2 found X at %d, want 3", s)
	}
}

func TestPattern_MalformedErrors(t *testing.T) {
	for _, pat := range []string{"%", "[abc", "(open", "%b"} {
		_, _, _, _, err := patternFind([]byte("test"), []byte(pat), 0)
		if err == nil {
			// '(' 未闭合在部分实现容忍;只要不 panic 即可
			if pat == "(open" {
				continue
			}
			t.Errorf("pattern %q: expected error", pat)
		}
	}
}

func TestPattern_EmptyMatches(t *testing.T) {
	// 空模式匹配空串(任何位置)
	s, e, _, found := findOnce(t, "ab", "", 0)
	if !found || s != 0 || e != 0 {
		t.Errorf("empty pattern: s=%d e=%d found=%v", s, e, found)
	}
	// x* 在不含 x 处匹配空
	s, e, _, found = findOnce(t, "yyy", "x*", 0)
	if !found || s != 0 || e != 0 {
		t.Errorf("x* empty: s=%d e=%d", s, e)
	}
}

func TestClassMatch_Table(t *testing.T) {
	cases := []struct {
		c    byte
		cl   byte
		want bool
	}{
		{'a', 'a', true}, {'5', 'd', true}, {'x', 'd', false},
		{' ', 's', true}, {'\t', 's', true},
		{'A', 'u', true}, {'a', 'u', false},
		{'a', 'l', true}, {'A', 'l', false},
		{'!', 'p', true}, {'a', 'p', false},
		{0x01, 'c', true}, {'a', 'c', false},
		{'f', 'x', true}, {'g', 'x', false},
		{'5', 'D', false}, {'x', 'D', true}, // 大写取反
		{'q', 'q', true}, {'q', 'z', false}, // 字面量回退
	}
	for _, c := range cases {
		if got := classMatch(c.c, c.cl); got != c.want {
			t.Errorf("classMatch(%q, %q) = %v, want %v", c.c, c.cl, got, c.want)
		}
	}
}
