// Package token defines the token type emitted by the lexer (03 §3).
package token

import "fmt"

// Kind is the token-kind enumeration. Covers all 21 Lua 5.1 keywords, all symbols/operators, literal kinds, and EOF.
//
// Note: no KwGoto (that is Lua 5.2+, excluded since roadmap §6 pins 5.1).
type Kind uint8

const (
	// Literals and identifiers.
	EOF Kind = iota
	NUMBER
	STRING
	NAME

	// The 21 keywords (Lua 5.1).
	KW_AND
	KW_BREAK
	KW_DO
	KW_ELSE
	KW_ELSEIF
	KW_END
	KW_FALSE
	KW_FOR
	KW_FUNCTION
	KW_IF
	KW_IN
	KW_LOCAL
	KW_NIL
	KW_NOT
	KW_OR
	KW_REPEAT
	KW_RETURN
	KW_THEN
	KW_TRUE
	KW_UNTIL
	KW_WHILE

	// Single-character operators / punctuation.
	PLUS    // +
	MINUS   // -
	STAR    // *
	SLASH   // /
	PERCENT // %
	CARET   // ^
	HASH    // #
	EQ      // =
	LPAREN  // (
	RPAREN  // )
	LBRACE  // {
	RBRACE  // }
	LBRACK  // [
	RBRACK  // ]
	SEMI    // ;
	COLON   // :
	COMMA   // ,
	DOT     // .

	// Multi-character operators.
	EQEQ     // ==
	NEQ      // ~=
	LT       // <
	LE       // <=
	GT       // >
	GE       // >=
	CONCAT   // ..
	ELLIPSIS // ...

	// CHAR is any other single byte. llex returns a character it does not recognize (a lone `~`, `$`,
	// `@`, a control or non-ASCII byte) as a token of its own, so the parser, not the lexer, reports it:
	// "unexpected symbol near '$'". Str holds the byte.
	CHAR
)

// Token is the unit consumed by the parser (03 §3.2).
type Token struct {
	Kind Kind
	Line int32 // 1-based source line number (where the token STARTS)

	// EndLine is the line the token ENDS on, which differs from Line only for a long string or
	// long comment spanning newlines. PUC's ls->linenumber is read AFTER a token is scanned, so
	// it is this value the parser must compare against. Without it, a call whose argument is a
	// long string containing a newline was rejected as ambiguous syntax (lua5.1 accepts it) and
	// reported the wrong line. Zero means "same as Line".
	EndLine int32

	// Literal payload:
	//   NUMBER → Num
	//   STRING / NAME → Str (decoded string content, long strings/escapes already handled)
	//   CHAR → Str (the byte)
	// Other tokens do not use these two fields.
	Num float64
	Str string

	// Text is what llex.c's txtToken shows for a NUMBER or STRING: the contents of the scan buffer.
	// For a number that is the numeral as written; for a string it is the delimiters around the
	// DECODED contents, since read_string saves an escape's value rather than the escape, and
	// read_long_string saves each newline as "\n" and drops the one right after the opener. So
	// `'a\65'` shows as 'aA' and a long string's CR LF as a single LF.
	Text string
}

// String returns the official-5.1 "near" rendering (parser error messages
// splice this output directly): NAME/NUMBER/STRING use the scan buffer (txtToken), the rest use luaX_token2str.
func (t Token) String() string {
	switch t.Kind {
	case NUMBER, STRING:
		if t.Text != "" {
			return t.Text
		}
		if t.Kind == NUMBER {
			return fmt.Sprintf("%v", t.Num)
		}
		return t.Str
	case NAME:
		return t.Str
	case CHAR:
		// luaX_token2str: a control character is spelled out, any other byte is shown as itself.
		if c := t.Str[0]; c < 0x20 || c == 0x7f {
			return fmt.Sprintf("char(%d)", c)
		}
		return t.Str
	default:
		return KindName(t.Kind)
	}
}

// HasNear reports whether an error at this token gets a " near" suffix. luaX_lexerror appends one
// only for a nonzero token, and a NUL byte read as a single-character token is token 0.
func (t Token) HasNear() bool { return t.Kind != CHAR || t.Str != "\x00" }

// Near formats the " near '<text>'" suffix luaX_lexerror appends. The text goes through
// luaO_pushfstring's %s, so it stops at the first NUL byte.
func Near(text string) string {
	for i := 0; i < len(text); i++ {
		if text[i] == 0 {
			text = text[:i]
			break
		}
	}
	return " near '" + text + "'"
}

// KindName returns a stable token kind name for diagnostics / errors.
func KindName(k Kind) string {
	if int(k) < len(kindNames) && kindNames[k] != "" {
		return kindNames[k]
	}
	return fmt.Sprintf("Kind(%d)", k)
}

var kindNames = [...]string{
	EOF:    "<eof>",
	NUMBER: "<number>",
	STRING: "<string>",
	NAME:   "<name>",

	KW_AND:      "and",
	KW_BREAK:    "break",
	KW_DO:       "do",
	KW_ELSE:     "else",
	KW_ELSEIF:   "elseif",
	KW_END:      "end",
	KW_FALSE:    "false",
	KW_FOR:      "for",
	KW_FUNCTION: "function",
	KW_IF:       "if",
	KW_IN:       "in",
	KW_LOCAL:    "local",
	KW_NIL:      "nil",
	KW_NOT:      "not",
	KW_OR:       "or",
	KW_REPEAT:   "repeat",
	KW_RETURN:   "return",
	KW_THEN:     "then",
	KW_TRUE:     "true",
	KW_UNTIL:    "until",
	KW_WHILE:    "while",

	PLUS:     "+",
	MINUS:    "-",
	STAR:     "*",
	SLASH:    "/",
	PERCENT:  "%",
	CARET:    "^",
	HASH:     "#",
	EQ:       "=",
	LPAREN:   "(",
	RPAREN:   ")",
	LBRACE:   "{",
	RBRACE:   "}",
	LBRACK:   "[",
	RBRACK:   "]",
	SEMI:     ";",
	COLON:    ":",
	COMMA:    ",",
	DOT:      ".",
	EQEQ:     "==",
	NEQ:      "~=",
	LT:       "<",
	LE:       "<=",
	GT:       ">",
	GE:       ">=",
	CONCAT:   "..",
	ELLIPSIS: "...",
}

// Keywords provides a fast lookup table for recognition (the lexer first recognizes an identifier, then consults this table, 03 §4).
var Keywords = map[string]Kind{
	"and":      KW_AND,
	"break":    KW_BREAK,
	"do":       KW_DO,
	"else":     KW_ELSE,
	"elseif":   KW_ELSEIF,
	"end":      KW_END,
	"false":    KW_FALSE,
	"for":      KW_FOR,
	"function": KW_FUNCTION,
	"if":       KW_IF,
	"in":       KW_IN,
	"local":    KW_LOCAL,
	"nil":      KW_NIL,
	"not":      KW_NOT,
	"or":       KW_OR,
	"repeat":   KW_REPEAT,
	"return":   KW_RETURN,
	"then":     KW_THEN,
	"true":     KW_TRUE,
	"until":    KW_UNTIL,
	"while":    KW_WHILE,
}
