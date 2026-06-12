// Package token defines the token type emitted by the lexer (03 §3).
package token

import "fmt"

// Kind 是 token 种类枚举。涵盖 Lua 5.1 全部 21 个关键字、全部符号/算符、字面量类、EOF。
//
// 注意:无 KwGoto(那是 Lua 5.2+,roadmap §6 锁 5.1 已排除)。
type Kind uint8

const (
	// 字面量与标识符。
	EOF Kind = iota
	NUMBER
	STRING
	NAME

	// 21 个关键字(Lua 5.1)。
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

	// 单字符算符 / 标点。
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

	// 多字符算符。
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
	Line int32 // 1-based 源行号

	// 字面量载荷:
	//   NUMBER → Num
	//   STRING / NAME → Str(已解码字符串内容,长字符串/转义已处理)
	// 其它 token 不使用这两个字段。
	Num float64
	Str string

	// Raw 是 NUMBER/STRING 的源文原样切片(官方 llex.c txtToken 语义:
	// 错误消息 near '1.000' / near ''aa'' 用原文,不用解析后的值)。
	Raw string
}

// String returns the official-5.1 "near" rendering(parser 错误消息直接
// 拼本输出):NAME/NUMBER/STRING 用源文原样(txtToken),其余用 kind 名。
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

// Keywords 提供识别用的快速查表(lexer 先识别 identifier 再查此表,03 §4)。
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
