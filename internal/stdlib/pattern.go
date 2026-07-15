// Lua 5.1 pattern matcher (10 §7) — mirrors the match engine in lstrlib.c.
//
// Supports: character classes (%a %d %s %w %x %p %c %l %u + uppercase for
// negation), sets [..]/[^..] (including []] where the leading ] is literal),
// quantifiers (* + - ?), anchors ^ / $, captures () and position captures,
// %b balanced match, %f[set] frontier, %1-%9 back references.
package stdlib

import (
	"fmt"
)

const (
	maxCaptures   = 32
	capPosition   = -2 // position capture marker
	capUnfinished = -1
)

type capture struct {
	init int // capture start (byte offset)
	len  int // capPosition / capUnfinished / actual length
}

type matchState struct {
	src      []byte
	pat      []byte
	level    int
	captures [maxCaptures]capture
	depth    int // recursion guard (depth)
	steps    int // backtracking budget (width): the combinatorial backtracking of nested quantifiers is exponential, so it must be bounded
}

const (
	maxMatchDepth = 200
	// maxMatchSteps is the total budget for match calls within a single patternFind.
	// Catastrophic backtracking (e.g. ".*.+%A*" against a long subject) really can
	// run for a long time in the 5.1 C implementation; the pure-Go implementation
	// chooses bounded failure ("pattern too complex") to guarantee fuzz/embedded
	// scenarios do not hang.
	maxMatchSteps = 1 << 20
)

// classMatch reports whether byte c belongs to class cl (single-letter classes like %a).
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
	case 'z':
		// Specific to 5.1 (removed in 5.2): %z matches NUL (the pattern string,
		// passed through as a C string, cannot hold \0, so %z expresses it);
		// %Z is the negation = any non-NUL byte.
		res = c == 0
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

// classEnd returns the position just after the "single match unit" starting at p in the pattern (lstrlib classEnd).
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
		// Mirror the official do-while: unconditionally consume one character
		// before checking for the terminating ']' — so the first ']' after
		// `[` / `[^` is a literal ([]] = a class containing ]).
		for {
			if p >= len(ms.pat) {
				return 0, fmt.Errorf("malformed pattern (missing ']')")
			}
			cc := ms.pat[p]
			p++
			if cc == '%' && p < len(ms.pat) {
				p++ // skip the escape (e.g. %])
			}
			if p < len(ms.pat) && ms.pat[p] == ']' {
				return p + 1, nil
			}
			if p >= len(ms.pat) {
				return 0, fmt.Errorf("malformed pattern (missing ']')")
			}
		}
	}
	return p, nil
}

// singleMatch reports whether src[s] matches pat[p..ep) (a single match unit).
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

// matchClass reports whether c falls in the set [p..ec) (p points at '[', ec points at ']').
func (ms *matchState) matchClass(c byte, p, ec int) bool {
	neg := false
	p++ // skip '['
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
			// range a-z
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

// match is the main matching recursion (lstrlib do_match): returns the match end position or -1.
func (ms *matchState) match(s, p int) (int, error) {
	ms.depth++
	if ms.depth > maxMatchDepth {
		return -1, fmt.Errorf("pattern too complex")
	}
	defer func() { ms.depth-- }()
	ms.steps++
	if ms.steps > maxMatchSteps {
		return -1, fmt.Errorf("pattern too complex")
	}

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
			// '$' not at the end: treat as a literal
		case '%':
			if p+1 < len(ms.pat) {
				nc := ms.pat[p+1]
				if nc == 'b' {
					return ms.matchBalance(s, p+2)
				}
				if nc == 'f' {
					// %f[set] frontier (present in 5.1, documented only in the 5.2
					// manual): zero-width match when the previous character is not
					// in set (start of string treated as '\0') and the current
					// character is in set.
					fp := p + 2
					if fp >= len(ms.pat) || ms.pat[fp] != '[' {
						return -1, fmt.Errorf("missing '[' after '%%f' in pattern")
					}
					fep, err := ms.classEnd(fp)
					if err != nil {
						return -1, err
					}
					var prev byte // start of string = '\0'
					if s > 0 {
						prev = ms.src[s-1]
					}
					var cur byte // end of string = '\0' (official *s on a NUL-terminated string)
					if s < len(ms.src) {
						cur = ms.src[s]
					}
					if !ms.matchClass(prev, fp, fep-1) && ms.matchClass(cur, fp, fep-1) {
						p = fep
						continue
					}
					return -1, nil
				}
				if nc >= '0' && nc <= '9' {
					// %0 is not a valid back reference in a pattern (official
					// check_capture reports "invalid capture index" for l==-1);
					// %1-%9 are normal.
					if nc == '0' {
						return -1, fmt.Errorf("invalid capture index")
					}
					return ms.matchCapture(s, p, int(nc-'1'))
				}
			}
		}
		// ordinary match unit + optional quantifier
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

// maxExpand handles greedy quantifiers (* +): consume as much as possible, then back off.
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

// minExpand handles the lazy quantifier (-): try the shortest first.
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

// startCapture opens a capture.
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

// endCapture closes the most recent unclosed capture.
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

// matchBalance handles %bxy: match a balanced string x..y.
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
		switch ms.src[i] {
		case e:
			cont--
			if cont == 0 {
				return ms.match(i+1, p+2)
			}
		case b:
			cont++
		}
		i++
	}
	return -1, nil
}

// matchCapture handles %1-%9: back reference to a closed capture.
//
// A position capture (len=capPosition) cannot be back-referenced (5.1 reports invalid capture).
func (ms *matchState) matchCapture(s, p, l int) (int, error) {
	if l >= ms.level || ms.captures[l].len < 0 {
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

// patternFind finds the first match of pat in src[init:].
// Returns (start, end, captures, found): start/end is the 0-based byte range [start, end).
//
// The backtracking budget (maxMatchSteps) is shared across all start positions:
// retrying from each start does not reset steps, guaranteeing a single find has
// bounded total cost.
func patternFind(src, pat []byte, init int) (int, int, []capResult, bool, error) {
	anchored := len(pat) > 0 && pat[0] == '^'
	p := 0
	if anchored {
		p = 1
	}
	s := init
	ms := &matchState{src: src, pat: pat}
	for {
		ms.level = 0
		r, err := ms.match(s, p)
		if err != nil {
			return 0, 0, nil, false, err
		}
		if r != -1 {
			caps, err := collectCaptures(ms, s, r)
			if err != nil {
				return 0, 0, nil, false, err
			}
			return s, r, caps, true, nil
		}
		s++
		if anchored || s > len(src) {
			return 0, 0, nil, false, nil
		}
	}
}

// capResult is the materialized result of one capture.
type capResult struct {
	pos   bool // position capture (value = init+1, Lua 1-based)
	start int
	len   int
}

func collectCaptures(ms *matchState, s, e int) ([]capResult, error) {
	if ms.level == 0 {
		// no explicit captures: the whole match acts as capture 0 (the caller decides how to present it)
		return nil, nil
	}
	out := make([]capResult, ms.level)
	for i := 0; i < ms.level; i++ {
		c := ms.captures[i]
		switch c.len {
		case capPosition:
			out[i] = capResult{pos: true, start: c.init}
		case capUnfinished:
			// `"(."`: match succeeded but the capture is unclosed (official
			// push_onecapture reports this error; materializing a slice with
			// len=-1 would panic on out-of-range)
			return nil, fmt.Errorf("unfinished capture")
		default:
			out[i] = capResult{start: c.init, len: c.len}
		}
	}
	return out, nil
}
