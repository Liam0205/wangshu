// Package lex implements the Lua 5.1 lexer (03).
//
// Interface contract (answering the first gap in 04 §13):
//   - Pull-style Next() fetches one token; the parser caches one slot of lookahead itself (03 §2, consistent with the official llex.c).
//   - Number literals are normalized through the NumberValue entry point (canonicalize NaN); string literals produce a Go string,
//     with interning left to codegen (04 §11).
//   - Long brackets / long strings / long comments share a single scanning subroutine.
//   - Line numbers: all four newline forms (\n / \r / \r\n / \n\r) count as 1 line (Lua 5.1 inclinenumber semantics).
//
// Lua 5.1 lexical constraints (locked by roadmap §6, explicitly excluding 5.2+ features):
//   - Identifiers: [A-Za-z_][A-Za-z0-9_]* (ASCII)
//   - Numbers: decimal integer/fraction + e/E exponent + 0x/0X hexadecimal (integer); no hex float 0x1p4 (that is 5.2+)
//   - String escapes: \a \b \f \n \r \t \v \\ \" \' \<newline> \ddd (at most 3 decimal digits)
//     no \x / \u{} / \z (those are 5.2+/5.3)
package lex

import (
	"bytes"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/frontend/token"
)

// Lexer scans Lua 5.1 source bytes into a stream of Tokens.
type Lexer struct {
	src    []byte
	pos    int
	line   int32
	source string // chunkname, used as the error prefix (03 §11)
}

// New creates a Lexer for the given source bytes.
func New(src []byte, source string) *Lexer {
	return &Lexer{src: src, line: 1, source: source}
}

// Source returns the chunkname.
func (l *Lexer) Source() string { return l.source }

// Line returns the current line number (after the most recent token).
func (l *Lexer) Line() int32 { return l.line }

// Error encapsulates lexer errors with source/line context.
type Error struct {
	Source string
	Line   int32
	Msg    string
}

func (e *Error) Error() string {
	// luaX_lexerror formats the chunk name with MAXSRC, not LUA_IDSIZE.
	return fmt.Sprintf("%s:%d: %s", bytecode.ChunkIDN(e.Source, bytecode.MaxSrc), e.Line, e.Msg)
}

// errorNear is luaX_lexerror(msg, token) with a nonzero token: the message gets " near '<text>'",
// where text is what txtToken shows -- "<eof>" for TK_EOS, the scan buffer so far for a string or
// number. The line is the scanner's current one, so an unfinished long string reports where the
// input ran out, not where it started.
func (l *Lexer) errorNear(msg, text string) *Error {
	return &Error{Source: l.source, Line: l.line, Msg: msg + token.Near(text)}
}

// peek returns the byte at offset off ahead, or 0 if out of range.
func (l *Lexer) peek(off int) byte {
	i := l.pos + off
	if i >= len(l.src) {
		return 0
	}
	return l.src[i]
}

func (l *Lexer) atEnd() bool { return l.pos >= len(l.src) }

// inclineFromCurrent advances over a newline sequence (one of \n / \r / \r\n / \n\r),
// incrementing l.line by 1. l.pos must point at \n or \r when called.
func (l *Lexer) inclineFromCurrent() {
	cur := l.src[l.pos]
	l.pos++
	if !l.atEnd() {
		nxt := l.src[l.pos]
		if (cur == '\n' && nxt == '\r') || (cur == '\r' && nxt == '\n') {
			l.pos++
		}
	}
	l.line++
}

// skipWhitespaceAndComments skips spaces / tabs / newlines / short & long comments.
func (l *Lexer) skipWhitespaceAndComments() error {
	for !l.atEnd() {
		c := l.src[l.pos]
		switch c {
		case ' ', '\t', '\v', '\f':
			l.pos++
		case '\n', '\r':
			l.inclineFromCurrent()
		case '-':
			// May be a `-` operator or a `--` comment.
			if l.peek(1) != '-' {
				return nil
			}
			l.pos += 2 // consume `--`
			// A long comment `--[[ ... ]]` / `--[=*[ ... ]=*]`?
			if l.pos < len(l.src) && l.src[l.pos] == '[' {
				if level, ok := l.tryReadLongBracketOpen(); ok {
					if err := l.skipLongBracket(level); err != nil {
						return err
					}
					continue
				}
			}
			// Short comment: to end of line.
			for !l.atEnd() && l.src[l.pos] != '\n' && l.src[l.pos] != '\r' {
				l.pos++
			}
		default:
			return nil
		}
	}
	return nil
}

// tryReadLongBracketOpen tries to read a `[=*[` opener starting at l.pos (which is at '[').
// On success: level returns the number of equals signs; on failure: l.pos is reset.
func (l *Lexer) tryReadLongBracketOpen() (level int, ok bool) {
	if l.pos >= len(l.src) || l.src[l.pos] != '[' {
		return 0, false
	}
	saved := l.pos
	l.pos++
	for l.pos < len(l.src) && l.src[l.pos] == '=' {
		level++
		l.pos++
	}
	if l.pos < len(l.src) && l.src[l.pos] == '[' {
		l.pos++ // consume the second [
		// The immediately following newline is discarded (Lua 5.1 long string/comment rule).
		if !l.atEnd() && (l.src[l.pos] == '\n' || l.src[l.pos] == '\r') {
			l.inclineFromCurrent()
		}
		return level, true
	}
	l.pos = saved
	return 0, false
}

// skipLongBracket scans to the matching `]=*]` (level equal-signs).
func (l *Lexer) skipLongBracket(level int) error {
	for !l.atEnd() {
		c := l.src[l.pos]
		switch c {
		case '\n', '\r':
			l.inclineFromCurrent()
		case '[':
			// PUC LUA_COMPAT_LSTR == 1 (5.1.5 default): a nested `[[`
			// inside a level-0 long bracket raises. Oracle diff fuzz
			// catch (--[[[[]] parses clean here, errors on 5.1.5).
			if level == 0 && l.matchNestedOpen() {
				return l.errorNear("nesting of [[...]] is deprecated", "[")
			}
			l.pos++
		case ']':
			if l.matchLongBracketClose(level) {
				return nil
			}
			l.pos++
		default:
			l.pos++
		}
	}
	return l.errorNear("unfinished long comment", "<eof>")
}

// matchNestedOpen reports whether l.pos sits on the first '[' of a
// level-0 `[[` pair (PUC skip_sep == 0 check). Position is unchanged.
func (l *Lexer) matchNestedOpen() bool {
	return l.pos+1 < len(l.src) && l.src[l.pos+1] == '['
}

// matchLongBracketClose: at l.pos = ']'; tries to match `]=*]` of given level.
// On success: l.pos moves past the closing bracket and returns true; on failure: l.pos is unchanged.
func (l *Lexer) matchLongBracketClose(level int) bool {
	saved := l.pos
	if l.src[l.pos] != ']' {
		return false
	}
	l.pos++
	count := 0
	for l.pos < len(l.src) && l.src[l.pos] == '=' && count < level {
		count++
		l.pos++
	}
	if count == level && l.pos < len(l.src) && l.src[l.pos] == ']' {
		l.pos++
		return true
	}
	l.pos = saved
	return false
}

// readLongString reads a long string body after its opener has been consumed.
// It returns the content after the leading newline has been stripped (no escape decoding). Every
// newline sequence in it reads as a single "\n": read_long_string saves '\n' and lets inclinenumber
// skip the rest of a CR LF or LF CR pair, so `[[a<CR><LF>b]]` is 3 bytes long.
func (l *Lexer) readLongString(level int) (string, error) {
	contentStart := l.pos
	for !l.atEnd() {
		c := l.src[l.pos]
		switch c {
		case '\n', '\r':
			l.inclineFromCurrent()
		case '[':
			// PUC LUA_COMPAT_LSTR == 1: same deprecation error as in
			// long comments (read_long_string is shared upstream).
			if level == 0 && l.matchNestedOpen() {
				return "", l.errorNear("nesting of [[...]] is deprecated", "[")
			}
			l.pos++
		case ']':
			contentEnd := l.pos
			if l.matchLongBracketClose(level) {
				body := l.src[contentStart:contentEnd]
				if bytes.IndexByte(body, '\r') >= 0 {
					return normalizeNewlines(body), nil
				}
				return string(body), nil
			}
			l.pos++
		default:
			l.pos++
		}
	}
	return "", l.errorNear("unfinished long string", "<eof>")
}

// normalizeNewlines turns each newline sequence (\n, \r, \r\n or \n\r, as inclineFromCurrent reads
// them) into a single \n.
func normalizeNewlines(b []byte) string {
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		c := b[i]
		if c != '\n' && c != '\r' {
			out = append(out, c)
			continue
		}
		out = append(out, '\n')
		if i+1 < len(b) && (b[i+1] == '\n' || b[i+1] == '\r') && b[i+1] != c {
			i++
		}
	}
	return string(out)
}

// Next emits the next token (or returns an error).
func (l *Lexer) Next() (token.Token, error) {
	tok, err := l.next()
	if err != nil {
		return tok, err
	}
	// Stamp the line the token ENDED on, which is what PUC's ls->linenumber holds after a scan.
	// Done once here rather than in each scanner so a new token kind cannot forget it.
	tok.EndLine = l.line
	return tok, nil
}

func (l *Lexer) next() (token.Token, error) {
	if err := l.skipWhitespaceAndComments(); err != nil {
		return token.Token{}, err
	}
	if l.atEnd() {
		return token.Token{Kind: token.EOF, Line: l.line}, nil
	}
	startLine := l.line
	c := l.src[l.pos]
	switch c {
	case '"', '\'':
		return l.scanShortString(startLine, c)
	case '[':
		// May be a long string / a plain LBRACK.
		saved := l.pos
		if level, ok := l.tryReadLongBracketOpen(); ok {
			s, err := l.readLongString(level)
			if err != nil {
				return token.Token{}, err
			}
			// txtToken shows the scan buffer: the brackets around the content as read_long_string
			// saved it, which leaves out the newline right after the opener.
			sep := strings.Repeat("=", level)
			text := "[" + sep + "[" + s + "]" + sep + "]"
			return token.Token{Kind: token.STRING, Line: startLine, Str: text[2+level : len(text)-2-level],
				Text: text}, nil
		}
		// skip_sep: a `[` followed by '='s but no second `[` is an error, not a `[` token.
		n := 1
		for saved+n < len(l.src) && l.src[saved+n] == '=' {
			n++
		}
		if n > 1 {
			l.pos = saved + n
			return token.Token{}, l.errorNear("invalid long string delimiter", string(l.src[saved:l.pos]))
		}
		l.pos = saved + 1
		return token.Token{Kind: token.LBRACK, Line: startLine}, nil
	}
	switch {
	case isAlpha(c) || c == '_':
		return l.scanIdentifierOrKeyword(startLine), nil
	case isDigit(c):
		return l.scanNumber(startLine)
	}
	return l.scanSymbol(startLine)
}

func isAlpha(c byte) bool { return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') }
func isDigit(c byte) bool { return c >= '0' && c <= '9' }
func isHexDigit(c byte) bool {
	return isDigit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func (l *Lexer) scanIdentifierOrKeyword(startLine int32) token.Token {
	start := l.pos
	for !l.atEnd() {
		c := l.src[l.pos]
		if isAlpha(c) || isDigit(c) || c == '_' {
			l.pos++
			continue
		}
		break
	}
	name := string(l.src[start:l.pos])
	if k, ok := token.Keywords[name]; ok {
		return token.Token{Kind: k, Line: startLine}
	}
	return token.Token{Kind: token.NAME, Line: startLine, Str: name}
}

// scanNumber follows the official llex.c read_numeral's "consume greedily + validate as a whole" (03 §5.2):
// it first consumes all digits and decimal points, an optional exponent marker (eE may be followed by +-), then
// trailing alphanumerics and underscores, handing the whole span to parseNumeral for validation; a partial parse is a malformed number.
// `1or`/`3..5`/`1.2.3`/`1abc` and the like are rejected consistently with the official implementation — a structured scan that "stops when it can no longer consume" would
// split them into adjacent tokens and silently accept them (`return 1or 2` would wrongly execute a return of 1).
func (l *Lexer) scanNumber(startLine int32) (token.Token, error) {
	start := l.pos
	// Greedy phase one: digits and '.' (official do-while isdigit || '.')
	for !l.atEnd() && (isDigit(l.src[l.pos]) || l.src[l.pos] == '.') {
		l.pos++
	}
	// Optional exponent marker (official check_next "Ee" + check_next "+-", each at most once).
	// check_next tests strchr(set, current), which also finds a NUL byte (the set's terminator), so a
	// NUL is taken as either; the numeral then ends there when luaO_str2d reads the buffer.
	if !l.atEnd() && (l.src[l.pos] == 'e' || l.src[l.pos] == 'E' || l.src[l.pos] == 0) {
		l.pos++
		if !l.atEnd() && (l.src[l.pos] == '+' || l.src[l.pos] == '-' || l.src[l.pos] == 0) {
			l.pos++
		}
	}
	// Greedy phase two: alphanumerics and underscores (the or in `1or` and the x10 in `0x10` are all consumed for whole-span validation)
	for !l.atEnd() {
		c := l.src[l.pos]
		if isAlpha(c) || isDigit(c) || c == '_' {
			l.pos++
			continue
		}
		break
	}
	lit := string(l.src[start:l.pos])
	num := lit
	if i := strings.IndexByte(num, 0); i >= 0 {
		num = num[:i]
	}
	f, ok := parseNumeral(num)
	if !ok {
		return token.Token{}, l.errorNear("malformed number", lit)
	}
	return token.Token{Kind: token.NUMBER, Line: startLine, Num: f, Text: lit}, nil
}

// parseNumeral is equivalent to the official luaO_str2d (consumes the whole span, failing if not fully consumed):
//   - a 0x/0X prefix takes the hex path (any number of digits, multiplied out into a float approximation, may carry a C99 p exponent —
//     oracle 5.1.5 accepts `0x1p4` = 16 via the system strtod);
//   - decimal follows strtod semantics (on overflow ErrRange it takes ±Inf, a valid 5.1 literal);
//   - underscore grouping is a Go literal extension that C strtod does not accept, so it is rejected up front.
func parseNumeral(s string) (float64, bool) {
	if strings.IndexByte(s, '_') >= 0 {
		return 0, false
	}
	if len(s) >= 2 && s[0] == '0' && (s[1] == 'x' || s[1] == 'X') {
		return parseHexNumeral(s[2:])
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		if ne, ok := err.(*strconv.NumError); ok && ne.Err == strconv.ErrRange {
			return f, true
		}
		return 0, false
	}
	return f, true
}

// parseHexNumeral parses the part after 0x: hexdigits (≥1, any number of digits, multiplied out in the
// float64 domain, tolerating precision loss), with an optional [pP][decdigits] (binary exponent). A negative
// exponent is unreachable: greedy phase two stops at '-', and the official read_numeral buffer likewise never reaches the sign.
func parseHexNumeral(s string) (float64, bool) {
	i := 0
	f := 0.0
	for i < len(s) && isHexDigit(s[i]) {
		f = f*16 + float64(hexDigitVal(s[i]))
		i++
	}
	if i == 0 {
		return 0, false
	}
	if i == len(s) {
		return f, true
	}
	if s[i] != 'p' && s[i] != 'P' {
		return 0, false
	}
	i++
	expStart := i
	exp := 0
	for i < len(s) && isDigit(s[i]) {
		exp = exp*10 + int(s[i]-'0')
		if exp > 1<<16 {
			exp = 1 << 16 // Ldexp saturates to ±Inf; guards against int overflow
		}
		i++
	}
	if i == expStart || i != len(s) {
		return 0, false
	}
	return math.Ldexp(f, exp), true
}

func hexDigitVal(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	default:
		return int(c-'A') + 10
	}
}

// scanShortString follows read_string. buf is its scan buffer: the opening delimiter, then the
// DECODED contents, which is what an error inside the string shows after "near" -- `"a\300` reports
// near '"a' -- and, with the closing delimiter, what txtToken shows for the finished token.
func (l *Lexer) scanShortString(startLine int32, quote byte) (token.Token, error) {
	l.pos++ // consume the opening quote
	buf := []byte{quote}
	for {
		if l.atEnd() {
			return token.Token{}, l.errorNear("unfinished string", "<eof>")
		}
		if l.src[l.pos] == '\n' || l.src[l.pos] == '\r' {
			return token.Token{}, l.errorNear("unfinished string", string(buf))
		}
		c := l.src[l.pos]
		if c == quote {
			l.pos++
			buf = append(buf, quote)
			text := string(buf)
			return token.Token{Kind: token.STRING, Line: startLine, Str: text[1 : len(text)-1], Text: text}, nil
		}
		if c == '\\' {
			l.pos++
			if l.atEnd() {
				return token.Token{}, l.errorNear("unfinished string", "<eof>")
			}
			esc := l.src[l.pos]
			switch esc {
			case 'a':
				buf = append(buf, '\a')
				l.pos++
			case 'b':
				buf = append(buf, '\b')
				l.pos++
			case 'f':
				buf = append(buf, '\f')
				l.pos++
			case 'n':
				buf = append(buf, '\n')
				l.pos++
			case 'r':
				buf = append(buf, '\r')
				l.pos++
			case 't':
				buf = append(buf, '\t')
				l.pos++
			case 'v':
				buf = append(buf, '\v')
				l.pos++
			case '\\', '"', '\'':
				buf = append(buf, esc)
				l.pos++
			case '\n', '\r':
				// \<newline> is turned into a newline; also handled via inclineFromCurrent (line number +1).
				buf = append(buf, '\n')
				l.inclineFromCurrent()
			default:
				if isDigit(esc) {
					// \ddd: 1-3 decimal digits, value ≤ 255.
					n := 0
					for k := 0; k < 3; k++ {
						if l.atEnd() || !isDigit(l.src[l.pos]) {
							break
						}
						n = n*10 + int(l.src[l.pos]-'0')
						l.pos++
					}
					if n > 255 {
						return token.Token{}, l.errorNear("escape sequence too large", string(buf))
					}
					buf = append(buf, byte(n))
				} else {
					// PUC 5.1 passes unknown escapes through literally
					// ("\A" == "A"; llex.c read_string default branch,
					// save_and_next). Rejecting is 5.2 behavior; caught
					// by the cgo oracle diff fuzz.
					buf = append(buf, esc)
					l.pos++
				}
			}
		} else {
			buf = append(buf, c)
			l.pos++
		}
	}
}

func (l *Lexer) scanSymbol(startLine int32) (token.Token, error) {
	c := l.src[l.pos]
	mk := func(k token.Kind, n int) token.Token {
		l.pos += n
		return token.Token{Kind: k, Line: startLine}
	}
	switch c {
	case '+':
		return mk(token.PLUS, 1), nil
	case '-':
		return mk(token.MINUS, 1), nil
	case '*':
		return mk(token.STAR, 1), nil
	case '/':
		return mk(token.SLASH, 1), nil
	case '%':
		return mk(token.PERCENT, 1), nil
	case '^':
		return mk(token.CARET, 1), nil
	case '#':
		return mk(token.HASH, 1), nil
	case '(':
		return mk(token.LPAREN, 1), nil
	case ')':
		return mk(token.RPAREN, 1), nil
	case '{':
		return mk(token.LBRACE, 1), nil
	case '}':
		return mk(token.RBRACE, 1), nil
	case ']':
		return mk(token.RBRACK, 1), nil
	case ';':
		return mk(token.SEMI, 1), nil
	case ':':
		return mk(token.COLON, 1), nil
	case ',':
		return mk(token.COMMA, 1), nil
	case '=':
		if l.peek(1) == '=' {
			return mk(token.EQEQ, 2), nil
		}
		return mk(token.EQ, 1), nil
	case '~':
		if l.peek(1) == '=' {
			return mk(token.NEQ, 2), nil
		}
	case '<':
		if l.peek(1) == '=' {
			return mk(token.LE, 2), nil
		}
		return mk(token.LT, 1), nil
	case '>':
		if l.peek(1) == '=' {
			return mk(token.GE, 2), nil
		}
		return mk(token.GT, 1), nil
	case '.':
		if l.peek(1) == '.' && l.peek(2) == '.' {
			return mk(token.ELLIPSIS, 3), nil
		}
		if l.peek(1) == '.' {
			return mk(token.CONCAT, 2), nil
		}
		// Number literals starting with `.` such as `.5e1` (allowed by Lua 5.1).
		if isDigit(l.peek(1)) {
			return l.scanNumber(startLine)
		}
		return mk(token.DOT, 1), nil
	}
	// llex returns any other character as a single-character token, a lone `~` included, and leaves the
	// error to the parser: "unexpected symbol near '$'", or "')' expected near '$'" inside a call.
	l.pos++
	return token.Token{Kind: token.CHAR, Line: startLine, Str: string([]byte{c})}, nil
}
