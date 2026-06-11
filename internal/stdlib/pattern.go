// Lua 5.1 pattern matcher (10 §7) — 对齐 lstrlib.c 的 match 引擎。
//
// 支持:字符类(%a %d %s %w %x %p %c %l %u + 取反大写)、集合 [..]/[^..]、
// 量词(* + - ?)、锚点 ^ / $、捕获 () 与位置捕获、%b 平衡匹配、%1-%9 反向引用。
// P1 不做 %f(frontier,5.1 未文档化特性)。
package stdlib

import (
	"fmt"
)

const (
	maxCaptures   = 32
	capPosition   = -2 // 位置捕获标记
	capUnfinished = -1
)

type capture struct {
	init int // 捕获起点(字节下标)
	len  int // capPosition / capUnfinished / 实际长度
}

type matchState struct {
	src      []byte
	pat      []byte
	level    int
	captures [maxCaptures]capture
	depth    int // 递归保护
}

const maxMatchDepth = 200

// classMatch 判定字符 c 是否属于类 cl(%a 等单字母类)。
func classMatch(c byte, cl byte) bool {
	var res bool
	switch lower(cl) {
	case 'a':
		res = isalpha(c)
	case 'c':
		res = iscntrl(c)
	case 'd':
		res = isdigit(c)
	case 'l':
		res = islower(c)
	case 'p':
		res = ispunct(c)
	case 's':
		res = isspace(c)
	case 'u':
		res = isupper(c)
	case 'w':
		res = isalnum(c)
	case 'x':
		res = isxdigit(c)
	default:
		return cl == c
	}
	if isupper(cl) {
		return !res
	}
	return res
}

func lower(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 32
	}
	return c
}
func isalpha(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
func isdigit(c byte) bool { return c >= '0' && c <= '9' }
func isalnum(c byte) bool { return isalpha(c) || isdigit(c) }
func isspace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'
}
func iscntrl(c byte) bool  { return c < 0x20 || c == 0x7F }
func islower(c byte) bool  { return c >= 'a' && c <= 'z' }
func isupper(c byte) bool  { return c >= 'A' && c <= 'Z' }
func ispunct(c byte) bool  { return c >= 0x21 && c <= 0x7E && !isalnum(c) }
func isxdigit(c byte) bool { return isdigit(c) || (lower(c) >= 'a' && lower(c) <= 'f') }

// classEnd 返回模式中从 p 开始的"单个匹配单元"之后的位置(lstrlib classEnd)。
func (ms *matchState) classEnd(p int) (int, error) {
	c := ms.pat[p]
	p++
	if c == '%' {
		if p >= len(ms.pat) {
			return 0, fmt.Errorf("malformed pattern (ends with '%%')")
		}
		return p + 1, nil
	}
	if c == '[' {
		if p < len(ms.pat) && ms.pat[p] == '^' {
			p++
		}
		// 找配对 ]:首字符可以是 ](字面量)
		for {
			if p >= len(ms.pat) {
				return 0, fmt.Errorf("malformed pattern (missing ']')")
			}
			cc := ms.pat[p]
			p++
			if cc == '%' {
				if p >= len(ms.pat) {
					return 0, fmt.Errorf("malformed pattern (ends with '%%')")
				}
				p++
			} else if cc == ']' && p > 0 {
				// 注意:跳过开头紧跟 ^ 后的第一个 ](Lua 允许 []] 形式)
				// 此处简化:cc==']' 即结束;开头第一个字符若是 ] 由调用序覆盖。
				return p, nil
			}
		}
	}
	return p, nil
}

// singleMatch 判定 src[s] 是否匹配 pat[p..ep)(一个匹配单元)。
func (ms *matchState) singleMatch(s, p, ep int) bool {
	if s >= len(ms.src) {
		return false
	}
	c := ms.src[s]
	switch ms.pat[p] {
	case '.':
		return true
	case '%':
		return classMatch(c, ms.pat[p+1])
	case '[':
		return ms.matchClass(c, p, ep-1)
	default:
		return ms.pat[p] == c
	}
}

// matchClass 判定 c 是否落在集合 [p..ec)(p 指向 '[',ec 指向 ']')。
func (ms *matchState) matchClass(c byte, p, ec int) bool {
	neg := false
	p++ // 跳过 '['
	if ms.pat[p] == '^' {
		neg = true
		p++
	}
	for p < ec {
		if ms.pat[p] == '%' {
			p++
			if classMatch(c, ms.pat[p]) {
				return !neg
			}
			p++
		} else if p+2 < ec && ms.pat[p+1] == '-' {
			// 范围 a-z
			if ms.pat[p] <= c && c <= ms.pat[p+2] {
				return !neg
			}
			p += 3
		} else {
			if ms.pat[p] == c {
				return !neg
			}
			p++
		}
	}
	return neg
}

// match 是匹配主递归(lstrlib do_match):返回匹配结束位置或 -1。
func (ms *matchState) match(s, p int) (int, error) {
	ms.depth++
	if ms.depth > maxMatchDepth {
		return -1, fmt.Errorf("pattern too complex")
	}
	defer func() { ms.depth-- }()

	for {
		if p >= len(ms.pat) {
			return s, nil
		}
		switch ms.pat[p] {
		case '(':
			if p+1 < len(ms.pat) && ms.pat[p+1] == ')' {
				return ms.startCapture(s, p+2, capPosition)
			}
			return ms.startCapture(s, p+1, capUnfinished)
		case ')':
			return ms.endCapture(s, p+1)
		case '$':
			if p+1 == len(ms.pat) {
				if s == len(ms.src) {
					return s, nil
				}
				return -1, nil
			}
			// '$' 不在末尾:按字面量
		case '%':
			if p+1 < len(ms.pat) {
				nc := ms.pat[p+1]
				if nc == 'b' {
					return ms.matchBalance(s, p+2)
				}
				if nc >= '1' && nc <= '9' {
					return ms.matchCapture(s, p, int(nc-'1'))
				}
			}
		}
		// 普通匹配单元 + 可选量词
		ep, err := ms.classEnd(p)
		if err != nil {
			return -1, err
		}
		var quant byte
		if ep < len(ms.pat) {
			quant = ms.pat[ep]
		}
		switch quant {
		case '?':
			if ms.singleMatch(s, p, ep) {
				r, err := ms.match(s+1, ep+1)
				if err != nil || r != -1 {
					return r, err
				}
			}
			p = ep + 1
			continue
		case '+':
			if !ms.singleMatch(s, p, ep) {
				return -1, nil
			}
			return ms.maxExpand(s+1, p, ep)
		case '*':
			return ms.maxExpand(s, p, ep)
		case '-':
			return ms.minExpand(s, p, ep)
		default:
			if !ms.singleMatch(s, p, ep) {
				return -1, nil
			}
			s++
			p = ep
			continue
		}
	}
}

// maxExpand 贪婪量词(* +):先吃到最长,再回退。
func (ms *matchState) maxExpand(s, p, ep int) (int, error) {
	i := 0
	for ms.singleMatch(s+i, p, ep) {
		i++
	}
	for i >= 0 {
		r, err := ms.match(s+i, ep+1)
		if err != nil {
			return -1, err
		}
		if r != -1 {
			return r, nil
		}
		i--
	}
	return -1, nil
}

// minExpand 懒惰量词(-):先试最短。
func (ms *matchState) minExpand(s, p, ep int) (int, error) {
	for {
		r, err := ms.match(s, ep+1)
		if err != nil {
			return -1, err
		}
		if r != -1 {
			return r, nil
		}
		if ms.singleMatch(s, p, ep) {
			s++
		} else {
			return -1, nil
		}
	}
}

// startCapture 开启一个捕获。
func (ms *matchState) startCapture(s, p, what int) (int, error) {
	if ms.level >= maxCaptures {
		return -1, fmt.Errorf("too many captures")
	}
	ms.captures[ms.level] = capture{init: s, len: what}
	ms.level++
	r, err := ms.match(s, p)
	if err != nil {
		return -1, err
	}
	if r == -1 {
		ms.level--
	}
	return r, nil
}

// endCapture 闭合最近未闭合捕获。
func (ms *matchState) endCapture(s, p int) (int, error) {
	l := ms.captureToClose()
	if l < 0 {
		return -1, fmt.Errorf("invalid pattern capture")
	}
	ms.captures[l].len = s - ms.captures[l].init
	r, err := ms.match(s, p)
	if err != nil {
		return -1, err
	}
	if r == -1 {
		ms.captures[l].len = capUnfinished
	}
	return r, nil
}

func (ms *matchState) captureToClose() int {
	for i := ms.level - 1; i >= 0; i-- {
		if ms.captures[i].len == capUnfinished {
			return i
		}
	}
	return -1
}

// matchBalance %bxy:匹配 x..y 平衡串。
func (ms *matchState) matchBalance(s, p int) (int, error) {
	if p+1 >= len(ms.pat) {
		return -1, fmt.Errorf("missing arguments to '%%b'")
	}
	if s >= len(ms.src) || ms.src[s] != ms.pat[p] {
		return -1, nil
	}
	b, e := ms.pat[p], ms.pat[p+1]
	cont := 1
	i := s + 1
	for i < len(ms.src) {
		if ms.src[i] == e {
			cont--
			if cont == 0 {
				return ms.match(i+1, p+2)
			}
		} else if ms.src[i] == b {
			cont++
		}
		i++
	}
	return -1, nil
}

// matchCapture %1-%9:反向引用已闭合捕获。
func (ms *matchState) matchCapture(s, p, l int) (int, error) {
	if l >= ms.level || ms.captures[l].len == capUnfinished {
		return -1, fmt.Errorf("invalid capture index %%%d", l+1)
	}
	clen := ms.captures[l].len
	if len(ms.src)-s >= clen &&
		bytesEqual(ms.src[ms.captures[l].init:ms.captures[l].init+clen], ms.src[s:s+clen]) {
		return ms.match(s+clen, p+2)
	}
	return -1, nil
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// patternFind 在 src[init:] 中找 pat 的首个匹配。
// 返回 (start, end, captures, found):start/end 是 0-based 字节区间 [start, end)。
func patternFind(src, pat []byte, init int) (int, int, []capResult, bool, error) {
	anchored := len(pat) > 0 && pat[0] == '^'
	p := 0
	if anchored {
		p = 1
	}
	s := init
	for {
		ms := &matchState{src: src, pat: pat}
		r, err := ms.match(s, p)
		if err != nil {
			return 0, 0, nil, false, err
		}
		if r != -1 {
			caps := collectCaptures(ms, s, r)
			return s, r, caps, true, nil
		}
		s++
		if anchored || s > len(src) {
			return 0, 0, nil, false, nil
		}
	}
}

// capResult 是一个捕获的物化结果。
type capResult struct {
	pos   bool // 位置捕获(值 = init+1,Lua 1-based)
	start int
	len   int
}

func collectCaptures(ms *matchState, s, e int) []capResult {
	if ms.level == 0 {
		// 无显式捕获:整个匹配作为捕获 0(由调用方决定如何呈现)
		return nil
	}
	out := make([]capResult, ms.level)
	for i := 0; i < ms.level; i++ {
		c := ms.captures[i]
		if c.len == capPosition {
			out[i] = capResult{pos: true, start: c.init}
		} else {
			out[i] = capResult{start: c.init, len: c.len}
		}
	}
	return out
}
