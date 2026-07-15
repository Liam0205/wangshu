// Package parse implements the Lua 5.1 recursive-descent parser with precedence
// climbing for expressions (04 §3-§4). LL(2) lookahead is held in the parser
// (one token ahead); the lexer only exposes Next() (03 §2).
//
// Error strategy: stop at the first error (04 §13, last item), paired with
// commit-msg / pre-commit hooks.
package parse

import (
	"fmt"

	"github.com/Liam0205/wangshu/internal/bytecode"
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

func (e *Error) Error() string {
	return fmt.Sprintf("%s:%d: %s", bytecode.ChunkID(e.Source), e.Line, e.Msg)
}

// Parser holds the lexer + a one-token lookahead (04 §4.1, 03 §2).
type Parser struct {
	lx       *lex.Lexer
	source   string
	tok      token.Token
	ahead    token.Token
	hasAhead bool

	// lastLine is the line number of the last consumed token (equivalent to
	// the reference ls->lastline; used by funcargs' ambiguous-syntax check).
	lastLine int32

	// Inside a vararg function body → `...` (VarargExpr) is allowed. Toggled by
	// enterFuncBody.
	insideVararg bool

	// depth is the syntax nesting depth (expression recursion + block nesting);
	// a guard against deep nesting blowing the Go stack (following Lua 5.1's
	// LUAI_MAXCCALLS idea; a fatal stack overflow is unrecoverable and must be
	// caught earlier with a recoverable error).
	depth int

	// loopDepth is the current loop nesting level (equivalent to the reference
	// fs->bl isbreakable chain): a break outside any loop reports
	// "no loop to break" at parse time (reference breakstat).
	// Saved/reset at function-body boundaries (break does not cross functions).
	loopDepth int
}

// maxParseDepth is the syntax nesting cap (5.1's 200 is conservative; Go stack
// frames are larger, but we keep the same value).
const maxParseDepth = 200

// enterDepth enters one level of syntactic recursion; on overflow it reports the
// same wording as 5.1.
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
// The top-level chunk is equivalent to a vararg function body (a Lua 5.1 main
// chunk accepts `...`), so insideVararg starts out true.
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
		return nil, p.errorf("'<eof>' expected near '%s'", p.tok.String())
	}
	return body, nil
}

// next advances to the next token: from ahead if buffered, else pull from lexer.
func (p *Parser) next() error {
	p.lastLine = p.tok.Line
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
		return p.errorf("'%s' expected near '%s'", token.KindName(k), p.tok.String())
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
		// 5.1 grammar: ';' is only a statement separator (chunk ::= {stat [';']}),
		// it cannot stand alone as a statement — `;` / `a=1;;` reports
		// unexpected symbol in the reference (relaxed only in 5.2).
		if p.match(token.SEMI) {
			return nil, p.errorf("unexpected symbol near '%s'", p.tok.String())
		}
		// `return` / `break` must be the last statement of a block (Lua 5.1
		// restriction).
		if p.match(token.KW_RETURN) {
			ret, err := p.parseReturn()
			if err != nil {
				return nil, err
			}
			block.Stmts = append(block.Stmts, ret)
			// After return one `;` is allowed, then the block must terminate.
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
			if p.loopDepth == 0 {
				return nil, p.errorf("no loop to break near '%s'", p.tok.String())
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
		// Statement trailing separator (at most one)
		if _, err := p.consume(token.SEMI); err != nil {
			return nil, err
		}
	}
	return block, nil
}

func isBlockEnd(k token.Kind) bool {
	return k == token.EOF || k == token.KW_END || k == token.KW_ELSE ||
		k == token.KW_ELSEIF || k == token.KW_UNTIL
}
