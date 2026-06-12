// Package parse implements the Lua 5.1 recursive-descent parser with precedence
// climbing for expressions (04 §3-§4). LL(2) lookahead is held in the parser
// (一格 ahead),lexer 只暴露 Next()(03 §2)。
//
// 错误策略:首错即停(04 §13 末条),配合 commit-msg / pre-commit hooks。
package parse

import (
	"fmt"

	"github.com/Liam0205/wangshu/internal/frontend/ast"
	"github.com/Liam0205/wangshu/internal/frontend/lex"
	"github.com/Liam0205/wangshu/internal/frontend/token"
)

// Error carries source/line for diagnostics.
type Error struct {
	Source string
	Line   int32
	Msg    string
}

func (e *Error) Error() string { return fmt.Sprintf("%s:%d: %s", e.Source, e.Line, e.Msg) }

// Parser holds the lexer + a one-token lookahead (04 §4.1, 03 §2).
type Parser struct {
	lx       *lex.Lexer
	source   string
	tok      token.Token
	ahead    token.Token
	hasAhead bool

	// 在 vararg 函数体内 → 允许 `...`(VarargExpr)。enterFuncBody 切换。
	insideVararg bool

	// depth 是语法嵌套深度(表达式递归 + 块嵌套),护栏防深嵌套打爆 Go 栈
	// (对齐 Lua 5.1 LUAI_MAXCCALLS 思路;fatal stack overflow 不可恢复,
	// 必须在之前用可恢复错误拦下)。
	depth int
}

// maxParseDepth 是语法嵌套上限(5.1 的 200 偏保守,Go 栈帧更大,取同值)。
const maxParseDepth = 200

// enterDepth 进入一层语法递归;超限报 5.1 同款措辞。
func (p *Parser) enterDepth() error {
	p.depth++
	if p.depth > maxParseDepth {
		return p.errorf("chunk has too many syntax levels")
	}
	return nil
}

func (p *Parser) leaveDepth() { p.depth-- }

// Parse parses an entire chunk (top-level Block) into AST.
//
// chunk 顶层等价于一个 vararg 函数体(Lua 5.1 main chunk 接受 `...`),所以
// insideVararg 初始置 true。
func Parse(lx *lex.Lexer, source string) (*ast.Block, error) {
	p := &Parser{lx: lx, source: source, insideVararg: true}
	if err := p.next(); err != nil {
		return nil, err
	}
	body, err := p.parseBlock()
	if err != nil {
		return nil, err
	}
	if p.tok.Kind != token.EOF {
		return nil, p.errorf("'<eof>' expected near %s", p.tok.String())
	}
	return body, nil
}

// next advances to the next token: from ahead if buffered, else pull from lexer.
func (p *Parser) next() error {
	if p.hasAhead {
		p.tok = p.ahead
		p.hasAhead = false
		return nil
	}
	t, err := p.lx.Next()
	if err != nil {
		return p.wrapLexErr(err)
	}
	p.tok = t
	return nil
}

// peek returns the lookahead token, fetching from lexer if needed.
func (p *Parser) peek() (token.Token, error) {
	if !p.hasAhead {
		t, err := p.lx.Next()
		if err != nil {
			return token.Token{}, p.wrapLexErr(err)
		}
		p.ahead = t
		p.hasAhead = true
	}
	return p.ahead, nil
}

func (p *Parser) wrapLexErr(err error) *Error {
	if le, ok := err.(*lex.Error); ok {
		return &Error{Source: le.Source, Line: le.Line, Msg: le.Msg}
	}
	return &Error{Source: p.source, Line: p.lx.Line(), Msg: err.Error()}
}

func (p *Parser) errorf(format string, args ...any) *Error {
	return &Error{Source: p.source, Line: p.tok.Line, Msg: fmt.Sprintf(format, args...)}
}

// expect consumes the current token if it matches kind; otherwise errors.
func (p *Parser) expect(k token.Kind) error {
	if p.tok.Kind != k {
		return p.errorf("'%s' expected near %s", token.KindName(k), p.tok.String())
	}
	return p.next()
}

// match checks whether the current token kind == k (no consumption).
func (p *Parser) match(k token.Kind) bool { return p.tok.Kind == k }

// consume returns true and advances iff p.tok.Kind == k.
func (p *Parser) consume(k token.Kind) (bool, error) {
	if !p.match(k) {
		return false, nil
	}
	return true, p.next()
}

// parseBlock parses a stmt list until a block-terminating token (04 §4.2).
func (p *Parser) parseBlock() (*ast.Block, error) {
	if err := p.enterDepth(); err != nil {
		return nil, err
	}
	defer p.leaveDepth()
	block := &ast.Block{}
	for !isBlockEnd(p.tok.Kind) {
		// `;` 空语句直接吃掉。
		if p.match(token.SEMI) {
			if err := p.next(); err != nil {
				return nil, err
			}
			continue
		}
		// `return` / `break` 是 block 的最后一句(Lua 5.1 限制)。
		if p.match(token.KW_RETURN) {
			ret, err := p.parseReturn()
			if err != nil {
				return nil, err
			}
			block.Stmts = append(block.Stmts, ret)
			// return 后允许一个 `;`,然后必须是 block 终结。
			if _, err := p.consume(token.SEMI); err != nil {
				return nil, err
			}
			break
		}
		if p.match(token.KW_BREAK) {
			line := p.tok.Line
			if err := p.next(); err != nil {
				return nil, err
			}
			block.Stmts = append(block.Stmts, &ast.BreakStmt{Line: line})
			if _, err := p.consume(token.SEMI); err != nil {
				return nil, err
			}
			break
		}
		stmt, err := p.parseStatement()
		if err != nil {
			return nil, err
		}
		if stmt != nil {
			block.Stmts = append(block.Stmts, stmt)
		}
	}
	return block, nil
}

func isBlockEnd(k token.Kind) bool {
	return k == token.EOF || k == token.KW_END || k == token.KW_ELSE ||
		k == token.KW_ELSEIF || k == token.KW_UNTIL
}
