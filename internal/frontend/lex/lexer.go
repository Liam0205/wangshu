// Package lex implements the Lua 5.1 lexer (03).
//
// 接口契约(回答 04 §13 第一条缺口):
//   - pull 式 Next() 拉一个 token;parser 自缓存一格 lookahead(03 §2 与官方 llex.c 一致)。
//   - 数字字面量经 NumberValue 入口规范(canonicalize NaN);字符串字面量产 Go string,
//     intern 留 codegen(04 §11)。
//   - 长括号 / 长字符串 / 长注释共用一个扫描子程序。
//   - 行号:四种换行(\n / \r / \r\n / \n\r)统一计 1 行(Lua 5.1 inclinenumber 语义)。
//
// Lua 5.1 词法约束(roadmap §6 锁,显式排除 5.2+ 特性):
//   - 标识符:[A-Za-z_][A-Za-z0-9_]*(ASCII)
//   - 数字:十进制整/小数 + e/E 指数 + 0x/0X 十六进制(整数);无 hex float 0x1p4(那是 5.2+)
//   - 字符串转义:\a \b \f \n \r \t \v \\ \" \' \<newline> \ddd(最多 3 位十进制)
//     无 \x / \u{} / \z(那些是 5.2+/5.3)
package lex

import (
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
	source string // chunkname,用于错误前缀(03 §11)
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
	return fmt.Sprintf("%s:%d: %s", bytecode.ChunkID(e.Source), e.Line, e.Msg)
}

func (l *Lexer) errorf(format string, args ...any) *Error {
	return &Error{Source: l.source, Line: l.line, Msg: fmt.Sprintf(format, args...)}
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
			// 可能是 `-` 算符或 `--` 注释。
			if l.peek(1) != '-' {
				return nil
			}
			l.pos += 2 // 吃掉 `--`
			// 长注释 `--[[ ... ]]` / `--[=*[ ... ]=*]`?
			if l.pos < len(l.src) && l.src[l.pos] == '[' {
				if level, ok := l.tryReadLongBracketOpen(); ok {
					if err := l.skipLongBracket(level); err != nil {
						return err
					}
					continue
				}
			}
			// 短注释:到行尾。
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
// 成功:level 返回等号数;失败:l.pos 复位。
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
		l.pos++ // 吃掉第二个 [
		// 紧跟的换行被丢弃(Lua 5.1 长字符串/注释规则)。
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
		case ']':
			if l.matchLongBracketClose(level) {
				return nil
			}
			l.pos++
		default:
			l.pos++
		}
	}
	return l.errorf("unfinished long comment")
}

// matchLongBracketClose: at l.pos = ']'; tries to match `]=*]` of given level.
// 成功:l.pos 移动到关闭括号之后并返回 true;失败:l.pos 不变。
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
// 返回去掉对齐前的紧随换行后的内容(无转义解码)。
func (l *Lexer) readLongString(level int) (string, error) {
	startLine := l.line
	contentStart := l.pos
	for !l.atEnd() {
		c := l.src[l.pos]
		switch c {
		case '\n', '\r':
			l.inclineFromCurrent()
		case ']':
			contentEnd := l.pos
			if l.matchLongBracketClose(level) {
				return string(l.src[contentStart:contentEnd]), nil
			}
			l.pos++
		default:
			l.pos++
		}
	}
	l.line = startLine
	return "", l.errorf("unfinished long string (started at line %d)", startLine)
}

// Next emits the next token (or returns an error).
func (l *Lexer) Next() (token.Token, error) {
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
		// 可能是长字符串 / 普通 LBRACK。
		saved := l.pos
		if level, ok := l.tryReadLongBracketOpen(); ok {
			s, err := l.readLongString(level)
			if err != nil {
				return token.Token{}, err
			}
			return token.Token{Kind: token.STRING, Line: startLine, Str: s,
				Raw: string(l.src[saved:l.pos])}, nil
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

// scanNumber 对齐官方 llex.c read_numeral 的「贪心吃完 + 整体校验」(03 §5.2):
// 先吃所有数字与小数点,可选指数标记(eE 后可带 +-),再吃尾随的字母数字与
// 下划线,整段交 parseNumeral 校验;解析不尽即 malformed number。
// `1or`/`3..5`/`1.2.3`/`1abc` 等与官方一致拒绝——结构化扫描"吃不动就停"会把
// 它们拆成相邻 token 静默接受(`return 1or 2` 错误地执行返回 1)。
func (l *Lexer) scanNumber(startLine int32) (token.Token, error) {
	start := l.pos
	// 贪心一段:数字与 '.'(官方 do-while isdigit || '.')
	for !l.atEnd() && (isDigit(l.src[l.pos]) || l.src[l.pos] == '.') {
		l.pos++
	}
	// 可选指数标记(官方 check_next "Ee" + check_next "+-",各至多一次)
	if !l.atEnd() && (l.src[l.pos] == 'e' || l.src[l.pos] == 'E') {
		l.pos++
		if !l.atEnd() && (l.src[l.pos] == '+' || l.src[l.pos] == '-') {
			l.pos++
		}
	}
	// 贪心二段:字母数字与下划线(`1or` 的 or、`0x10` 的 x10 都吃进来整体校验)
	for !l.atEnd() {
		c := l.src[l.pos]
		if isAlpha(c) || isDigit(c) || c == '_' {
			l.pos++
			continue
		}
		break
	}
	lit := string(l.src[start:l.pos])
	f, ok := parseNumeral(lit)
	if !ok {
		return token.Token{}, l.errorf("malformed number near '%s'", lit)
	}
	return token.Token{Kind: token.NUMBER, Line: startLine, Num: f, Raw: lit}, nil
}

// parseNumeral 等价官方 luaO_str2d(整段消费,不尽即失败):
//   - 0x/0X 前缀走 hex 路径(任意位数累乘取浮点近似,可带 C99 p 指数——
//     oracle 5.1.5 经系统 strtod 接受 `0x1p4` = 16);
//   - 十进制走 strtod 语义(溢出 ErrRange 取 ±Inf,5.1 合法字面量);
//   - 下划线分组是 Go 字面量扩展,C strtod 不接受,先行拒绝。
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

// parseHexNumeral 解析 0x 后的部分:hexdigits(≥1,任意位数,float64 域累乘
// 容忍精度丢失),可选 [pP][decdigits](二进制指数)。负指数不可达:贪心
// 二段在 '-' 处停止,官方 read_numeral 缓冲同样到不了符号。
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
			exp = 1 << 16 // Ldexp 饱和 ±Inf,防 int 溢出
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

func (l *Lexer) scanShortString(startLine int32, quote byte) (token.Token, error) {
	rawStart := l.pos
	l.pos++ // 吃掉开始的引号
	var buf []byte
	for {
		if l.atEnd() || l.src[l.pos] == '\n' || l.src[l.pos] == '\r' {
			return token.Token{}, l.errorf("unfinished string")
		}
		c := l.src[l.pos]
		if c == quote {
			l.pos++
			return token.Token{Kind: token.STRING, Line: startLine, Str: string(buf),
				Raw: string(l.src[rawStart:l.pos])}, nil
		}
		if c == '\\' {
			l.pos++
			if l.atEnd() {
				return token.Token{}, l.errorf("unfinished string")
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
				// \<newline> 转成换行;一并按 inclineFromCurrent 处理(行号 +1)。
				buf = append(buf, '\n')
				l.inclineFromCurrent()
			default:
				if isDigit(esc) {
					// \ddd:1-3 位十进制,值 ≤ 255。
					n := 0
					for k := 0; k < 3; k++ {
						if l.atEnd() || !isDigit(l.src[l.pos]) {
							break
						}
						n = n*10 + int(l.src[l.pos]-'0')
						l.pos++
					}
					if n > 255 {
						return token.Token{}, l.errorf("escape sequence too large")
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
		return token.Token{}, l.errorf("invalid character '~' (expected '~=')")
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
		// `.5e1` 这类以 `.` 开头的数字字面量(Lua 5.1 允许)。
		if isDigit(l.peek(1)) {
			return l.scanNumber(startLine)
		}
		return mk(token.DOT, 1), nil
	}
	return token.Token{}, l.errorf("unexpected character '%c'", c)
}
