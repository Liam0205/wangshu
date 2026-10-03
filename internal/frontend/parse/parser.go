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
	// luaX_syntaxerror goes through luaX_lexerror, which formats the chunk name with MAXSRC.
	return fmt.Sprintf("%s:%d: %s", bytecode.ChunkIDN(e.Source, bytecode.MaxSrc), e.Line, e.Msg)
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
	// depthLimit is the depth at which enterDepth trips: maxParseDepth, minus the C-call depth
	// already in use when the parse is a loadstring/load (see ParseAtCDepth).
	depthLimit int

	// fs is the function being parsed (see funcScope).
	fs *funcScope
}

// funcScope is the part of lparser.c's FuncState that PUC's parse-time limit checks read: the
// active local names (searchvar), the upvalue names (indexupvalue) and linedefined, which
// errorlimit's wording names. PUC makes these checks while parsing, so they come before any later
// syntax error and report the line the parser is on; codegen runs after the whole chunk is parsed
// and keeps only a fallback.
type funcScope struct {
	prev        *funcScope
	lineDefined int32
	actvars     []string
	upvals      []string
}

const (
	maxVars     = 200 // LUAI_MAXVARS
	maxUpvalues = 60  // LUAI_MAXUPVALUES
)

// maxParseDepth is the syntax nesting cap (5.1's 200 is conservative; Go stack
// frames are larger, but we keep the same value).
const maxParseDepth = 200

// enterDepth enters one level of syntactic recursion; on overflow it reports the
// same wording as 5.1.
func (p *Parser) enterDepth() error {
	p.depth++
	if p.depth > p.depthLimit {
		return p.plainError("chunk has too many syntax levels")
	}
	return nil
}

func (p *Parser) leaveDepth() { p.depth-- }

// Parse parses an entire chunk (top-level Block) into AST.
//
// The top-level chunk is equivalent to a vararg function body (a Lua 5.1 main
// chunk accepts `...`), so insideVararg starts out true.
func Parse(lx *lex.Lexer, source string) (*ast.Block, error) {
	return ParseAtCDepth(lx, source, 0)
}

// ParseAtCDepth parses with cDepth C calls already in use. lparser.c's enterlevel counts syntax levels
// on L->nCcalls itself (`++L->nCcalls > LUAI_MAXCCALLS`), so a chunk loaded from deep inside nested
// calls -- or from an xpcall handler running past the C limit -- has that much less room, and fails
// with "chunk has too many syntax levels" where a top-level load would not.
func ParseAtCDepth(lx *lex.Lexer, source string, cDepth int) (*ast.Block, error) {
	p := &Parser{lx: lx, source: source, insideVararg: true, depthLimit: maxParseDepth - cDepth, fs: &funcScope{}}
	if err := p.next(); err != nil {
		return nil, err
	}
	body, err := p.parseBlock()
	if err != nil {
		return nil, err
	}
	if p.tok.Kind != token.EOF {
		return nil, p.errorExpected(token.EOF)
	}
	return body, nil
}

// next advances to the next token: from ahead if buffered, else pull from lexer.
func (p *Parser) next() error {
	// The END line of the consumed token, matching PUC's ls->linenumber, which is read after the
	// token is scanned. Using the start line rejected a call whose argument is a long string
	// containing a newline as ambiguous syntax.
	if p.tok.EndLine != 0 {
		p.lastLine = p.tok.EndLine
	} else {
		p.lastLine = p.tok.Line
	}
	if p.hasAhead {
		// PUC's luaX_next copies ls->linenumber, and a pending lookahead has already moved the scanner
		// past the consumed token, so lastline is where the LOOKAHEAD token ends. Only the table
		// constructor's NAME/`=` disambiguation peeks (as in PUC), so `{<nl>A<nl>.x}` discharges A's
		// GETGLOBAL on the `.x` line, as luac5.1 does. It also means `{ f<nl>(3) }` is NOT ambiguous
		// syntax -- the `(` was scanned before `f` was consumed, so funcargs sees equal lines -- which
		// matches luac5.1 and is pinned by TestAmbiguousSyntaxCrossLineCall (#262).
		if p.ahead.EndLine != 0 {
			p.lastLine = p.ahead.EndLine
		} else {
			p.lastLine = p.ahead.Line
		}
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

// curLine is PUC's ls->linenumber, the line every parser error reports: where the scanner stands,
// which is the end of the last token it read -- the lookahead when one is buffered, otherwise the
// current token. Reporting the current token's start line put an error after a multi-line long
// string on the string's first line.
func (p *Parser) curLine() int32 {
	if p.hasAhead {
		if p.ahead.EndLine != 0 {
			return p.ahead.EndLine
		}
		return p.ahead.Line
	}
	return p.tokEndLine()
}

// syntaxError is luaX_syntaxerror: msg followed by " near" the current token.
func (p *Parser) syntaxError(msg string) *Error {
	if p.tok.HasNear() {
		msg += token.Near(p.tok.String())
	}
	return p.plainError(msg)
}

// plainError is luaX_lexerror(ls, msg, 0), which names no token.
func (p *Parser) plainError(msg string) *Error {
	return &Error{Source: p.source, Line: p.curLine(), Msg: msg}
}

// errorExpected is error_expected: "'<token>' expected near ...".
func (p *Parser) errorExpected(k token.Kind) *Error {
	return p.syntaxError("'" + token.KindName(k) + "' expected")
}

// errorLimit is errorlimit: fs has gone over one of the parser's fixed limits.
func (p *Parser) errorLimit(fs *funcScope, limit int, what string) *Error {
	if fs.lineDefined == 0 {
		return p.plainError(fmt.Sprintf("main function has more than %d %s", limit, what))
	}
	return p.plainError(fmt.Sprintf("function at line %d has more than %d %s", fs.lineDefined, limit, what))
}

// expect consumes the current token if it matches kind; otherwise errors.
func (p *Parser) expect(k token.Kind) error {
	if p.tok.Kind != k {
		return p.errorExpected(k)
	}
	return p.next()
}

// expectMatch is check_match: the token closing a construct opened by who on line where. When the
// opener is on another line, the error names it and its line.
func (p *Parser) expectMatch(what, who token.Kind, where int32) error {
	if p.tok.Kind == what {
		return p.next()
	}
	if where == p.curLine() {
		return p.errorExpected(what)
	}
	return p.syntaxError(fmt.Sprintf("'%s' expected (to close '%s' at line %d)",
		token.KindName(what), token.KindName(who), where))
}

// checkName is str_checkname: the current token must be a name, which is consumed and returned.
func (p *Parser) checkName() (string, error) {
	if !p.match(token.NAME) {
		return "", p.errorExpected(token.NAME)
	}
	name := p.tok.Str
	return name, p.next()
}

// checkLocalLimit is new_localvar's check, made as the n-th name (from 0) of a declaration is read,
// before any of the declaration's names is active.
func (p *Parser) checkLocalLimit(n int) error {
	if len(p.fs.actvars)+n+1 > maxVars {
		return p.errorLimit(p.fs, maxVars, "local variables")
	}
	return nil
}

// activate is adjustlocalvars: the names become visible to the code that follows.
func (p *Parser) activate(names ...string) { p.fs.actvars = append(p.fs.actvars, names...) }

// resolveName is singlevaraux for a name just read: a name found in an enclosing function becomes an
// upvalue of every function between there and fs, and that is where PUC checks LUAI_MAXUPVALUES --
// against the first function to go over it, which may be an intermediate one. Upvalues are matched
// by name: while a function is parsed, the scopes it can see do not change, so one name always means
// the same outer variable (indexupvalue matches by variable). It reports whether name is a local or
// upvalue (not a global).
func (p *Parser) resolveName(fs *funcScope, name string) (bool, error) {
	if fs == nil {
		return false, nil
	}
	for i := len(fs.actvars) - 1; i >= 0; i-- {
		if fs.actvars[i] == name {
			return true, nil
		}
	}
	found, err := p.resolveName(fs.prev, name)
	if err != nil || !found {
		return found, err
	}
	for _, u := range fs.upvals {
		if u == name {
			return true, nil
		}
	}
	if len(fs.upvals)+1 > maxUpvalues {
		return false, p.errorLimit(fs, maxUpvalues, "upvalues")
	}
	fs.upvals = append(fs.upvals, name)
	return true, nil
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

// parseBlock parses a stmt list until a block-terminating token (04 §4.2). The locals it declares go
// out of scope at its end (leaveblock).
func (p *Parser) parseBlock() (*ast.Block, error) {
	saved := len(p.fs.actvars)
	b, err := p.parseBlockKeepScope()
	p.fs.actvars = p.fs.actvars[:saved]
	return b, err
}

// parseBlockKeepScope is parseBlock leaving the block's locals in scope, for repeat-until, whose
// condition still sees them.
func (p *Parser) parseBlockKeepScope() (*ast.Block, error) {
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
			return nil, p.syntaxError("unexpected symbol")
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
				return nil, p.syntaxError("no loop to break")
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
	// The closing keyword is the current token, not yet consumed: this is where PUC's leaveblock runs.
	block.EndLine = p.lastLine
	return block, nil
}

func isBlockEnd(k token.Kind) bool {
	return k == token.EOF || k == token.KW_END || k == token.KW_ELSE ||
		k == token.KW_ELSEIF || k == token.KW_UNTIL
}

// tokEndLine is the line the current token ends on, falling back to its start line. See
// token.Token.EndLine: PUC reads ls->linenumber after a scan, so a multi-line token's consumer
// sees its LAST line.
func (p *Parser) tokEndLine() int32 {
	if p.tok.EndLine != 0 {
		return p.tok.EndLine
	}
	return p.tok.Line
}
