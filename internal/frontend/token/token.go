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
)

// Token is the unit consumed by the parser (03 §3.2).
type Token struct {
	Kind Kind
	Line int32 // 1-based source line number

	// Literal payload:
	//   NUMBER → Num
	//   STRING / NAME → Str (decoded string content, long strings/escapes already handled)
	// Other tokens do not use these two fields.
	Num float64
	Str string

	// Raw is the verbatim source slice of a NUMBER/STRING (official llex.c txtToken
	// semantics: error messages near '1.000' / near ''aa'' use the source text, not the parsed value).
	Raw string
}

// String returns the official-5.1 "near" rendering (parser error messages
// splice this output directly): NAME/NUMBER/STRING use the verbatim source text (txtToken), the rest use the kind name.
func (t Token) String() string {
	switch t.Kind {
	case NUMBER, STRING:
		if t.Raw != "" {
			return t.Raw
		}
		if t.Kind == NUMBER {
			return fmt.Sprintf("%v", t.Num)
		}
		return t.Str
	case NAME:
		return t.Str
	default:
		return KindName(t.Kind)
	}
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
