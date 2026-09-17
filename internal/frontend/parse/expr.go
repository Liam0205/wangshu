// Expression parsing — precedence climbing (04 §4.3) + prefix/primary chain (04 §4 / §7).
package parse

import (
	"github.com/Liam0205/wangshu/internal/frontend/ast"
	"github.com/Liam0205/wangshu/internal/frontend/token"
)

// The (left, right) priorities of binary operators (04 §4.3, matching Lua 5.1
// lparser.c). Right-associative ⟺ right < left.
type binPrio struct{ left, right uint8 }

var binPriorities = map[ast.BinOp]binPrio{
	ast.OpOr:  {1, 1},
	ast.OpAnd: {2, 2},
	ast.OpLt:  {3, 3}, ast.OpGt: {3, 3}, ast.OpLe: {3, 3}, ast.OpGe: {3, 3}, ast.OpNe: {3, 3}, ast.OpEq: {3, 3},
	ast.OpConcat: {5, 4}, // right-associative
	ast.OpAdd:    {6, 6}, ast.OpSub: {6, 6},
	ast.OpMul: {7, 7}, ast.OpDiv: {7, 7}, ast.OpMod: {7, 7},
	// unary = 8
	ast.OpPow: {10, 9}, // right-associative, higher than unary
}

const unaryPriority uint8 = 8

func tokenToBinOp(k token.Kind) (ast.BinOp, bool) {
	switch k {
	case token.PLUS:
		return ast.OpAdd, true
	case token.MINUS:
		return ast.OpSub, true
	case token.STAR:
		return ast.OpMul, true
	case token.SLASH:
		return ast.OpDiv, true
	case token.PERCENT:
		return ast.OpMod, true
	case token.CARET:
		return ast.OpPow, true
	case token.CONCAT:
		return ast.OpConcat, true
	case token.EQEQ:
		return ast.OpEq, true
	case token.NEQ:
		return ast.OpNe, true
	case token.LT:
		return ast.OpLt, true
	case token.LE:
		return ast.OpLe, true
	case token.GT:
		return ast.OpGt, true
	case token.GE:
		return ast.OpGe, true
	case token.KW_AND:
		return ast.OpAnd, true
	case token.KW_OR:
		return ast.OpOr, true
	}
	return 0, false
}

func tokenToUnOp(k token.Kind) (ast.UnOp, bool) {
	switch k {
	case token.MINUS:
		return ast.OpUnm, true
	case token.KW_NOT:
		return ast.OpNot, true
	case token.HASH:
		return ast.OpLen, true
	}
	return 0, false
}

// parseExpr is precedence-climbing subexpr (04 §4.3). limit = 0 parses a full expression.
func (p *Parser) parseExpr(limit uint8) (ast.Expr, error) {
	if err := p.enterDepth(); err != nil {
		return nil, err
	}
	defer p.leaveDepth()
	var e ast.Expr
	if uop, ok := tokenToUnOp(p.tok.Kind); ok {
		line := p.tok.Line
		if err := p.next(); err != nil {
			return nil, err
		}
		sub, err := p.parseExpr(unaryPriority)
		if err != nil {
			return nil, err
		}
		// p.lastLine IS the reference ls->lastline at luaK_prefix time: the operand has just been parsed
		// and nothing after it has been consumed (#262).
		e = &ast.UnExpr{Line: line, EndLine: p.lastLine, Op: uop, E: sub}
	} else {
		var err error
		e, err = p.parseSimpleExpr()
		if err != nil {
			return nil, err
		}
	}
	for {
		bop, ok := tokenToBinOp(p.tok.Kind)
		if !ok || binPriorities[bop].left <= limit {
			break
		}
		line := p.tok.Line
		if err := p.next(); err != nil {
			return nil, err
		}
		rhs, err := p.parseExpr(binPriorities[bop].right)
		if err != nil {
			return nil, err
		}
		// p.lastLine IS the reference ls->lastline at luaK_posfix time: the right operand's last token,
		// which is where PUC stamps the operation (#262). The lookahead token that ended the operand has
		// been scanned but not consumed, so it has not advanced lastline -- same as luaX_next's order.
		e = &ast.BinExpr{Line: line, EndLine: p.lastLine, Op: bop, L: e, R: rhs}
	}
	return e, nil
}

// simpleexp ::= NUMBER | STRING | nil | true | false | '...' | tableexpr | functionexpr | prefixexp
func (p *Parser) parseSimpleExpr() (ast.Expr, error) {
	line := p.tok.Line
	switch p.tok.Kind {
	case token.NUMBER:
		v := p.tok.Num
		if err := p.next(); err != nil {
			return nil, err
		}
		return &ast.NumberExpr{Line: line, Val: v}, nil
	case token.STRING:
		s := p.tok.Str
		if err := p.next(); err != nil {
			return nil, err
		}
		return &ast.StringExpr{Line: line, Val: s}, nil
	case token.KW_NIL:
		if err := p.next(); err != nil {
			return nil, err
		}
		return &ast.NilExpr{Line: line}, nil
	case token.KW_TRUE:
		if err := p.next(); err != nil {
			return nil, err
		}
		return &ast.TrueExpr{Line: line}, nil
	case token.KW_FALSE:
		if err := p.next(); err != nil {
			return nil, err
		}
		return &ast.FalseExpr{Line: line}, nil
	case token.ELLIPSIS:
		if !p.insideVararg {
			return nil, p.errorf("cannot use '...' outside a vararg function")
		}
		emitLine := p.lastLine // PUC emits VARARG before consuming `...`
		if err := p.next(); err != nil {
			return nil, err
		}
		return &ast.VarargExpr{Line: line, EmitLine: emitLine}, nil
	case token.LBRACE:
		return p.parseTableExpr()
	case token.KW_FUNCTION:
		if err := p.next(); err != nil {
			return nil, err
		}
		return p.parseFuncBody(line, false)
	}
	return p.parsePrefixExpr()
}

// prefixexp ::= ( '(' expr ')' | Name ) { '.' Name | '[' expr ']' | ':' Name args | args }
func (p *Parser) parsePrefixExpr() (ast.Expr, error) {
	var e ast.Expr
	switch p.tok.Kind {
	case token.NAME:
		e = &ast.NameExpr{Line: p.tok.Line, Name: p.tok.Str}
		if err := p.next(); err != nil {
			return nil, err
		}
	case token.LPAREN:
		line := p.tok.Line
		if err := p.next(); err != nil {
			return nil, err
		}
		inner, err := p.parseExpr(0)
		if err != nil {
			return nil, err
		}
		if err := p.expect(token.RPAREN); err != nil {
			return nil, err
		}
		// Always wrap in ParenExpr: ① collapse a multi-value source to a
		// single value; ② a parenthesized expression is an rvalue, and
		// isAssignable must be able to reject `(a) = 5` (official syntax
		// error; unwrapping the single-value core would restore a NameExpr
		// that gets wrongly accepted). Zero overhead in codegen for the
		// single-value core.
		// After expect(RPAREN) consumed it, p.lastLine IS the closing paren's line, and IS the reference
		// ls->lastline at the point primaryexp discharges the inner expression (#252).
		e = &ast.ParenExpr{Line: line, EndLine: p.lastLine, E: inner}
	default:
		return nil, p.errorf("unexpected symbol near '%s'", p.tok.String())
	}
	for {
		switch p.tok.Kind {
		case token.DOT:
			opLine := p.tok.Line
			objEnd := p.lastLine // PUC's field discharges the object before skipping the dot
			if err := p.next(); err != nil {
				return nil, err
			}
			if !p.match(token.NAME) {
				return nil, p.errorf("<name> expected near '%s'", p.tok.String())
			}
			e = &ast.IndexExpr{Line: opLine, ObjEndLine: objEnd, Obj: e, Key: &ast.StringExpr{Line: p.tok.Line, Val: p.tok.Str}}
			if err := p.next(); err != nil {
				return nil, err
			}
		case token.LBRACK:
			opLine := p.tok.Line
			objEnd := p.lastLine // PUC's primaryexp discharges the object before yindex
			if err := p.next(); err != nil {
				return nil, err
			}
			key, err := p.parseExpr(0)
			if err != nil {
				return nil, err
			}
			keyEnd := p.lastLine // yindex's exp2val runs before `]` is consumed
			if err := p.expect(token.RBRACK); err != nil {
				return nil, err
			}
			e = &ast.IndexExpr{Line: opLine, ObjEndLine: objEnd, KeyEndLine: keyEnd, CloseLine: p.lastLine, Obj: e, Key: key}
		case token.COLON:
			if err := p.next(); err != nil {
				return nil, err
			}
			if !p.match(token.NAME) {
				return nil, p.errorf("<name> expected near '%s'", p.tok.String())
			}
			method := p.tok.Str
			line := p.tok.Line
			if err := p.next(); err != nil {
				return nil, err
			}
			argsLine := p.tokEndLine()
			args, argEnds, err := p.parseArgs()
			if err != nil {
				return nil, err
			}
			e = &ast.MethodCallExpr{Line: line, ArgsLine: argsLine, ArgEndLines: argEnds, Recv: e, Method: method, Args: args}
		case token.LPAREN, token.STRING, token.LBRACE:
			// The line of the ARGUMENT LIST, not of the callee expression.
			//
			// PUC records the call at the line where its arguments begin, so
			//   (0
			//   )()
			// reports line 2 -- the line of the "()" -- while using the callee's own line
			// reported 1. Only multi-line callee expressions differ, which is why this went
			// unnoticed: on one line the two are the same.
			// The END line of the argument token, matching PUC: funcargs reads ls->linenumber
			// AFTER the token is scanned, so a long string spanning newlines puts the CALL on
			// its LAST line. Using the start line reported A[[\n]] at line 1 where lua5.1
			// says 2.
			argsLine := p.tokEndLine()
			fnEnd := p.lastLine // funcargs pushes the callee before touching the argument list
			args, argEnds, err := p.parseArgs()
			if err != nil {
				return nil, err
			}
			// The callee keeps its OWN line; only the CALL uses the argument list's.
			e = &ast.CallExpr{Line: e.Pos(), ArgsLine: argsLine, FnEndLine: fnEnd, ArgEndLines: argEnds, Fn: e, Args: args}
		default:
			return e, nil
		}
	}
}

// args ::= '(' [explist] ')' | tableexpr | STRING
//
// The second result is the per-argument end line (ls->lastline at the point PUC materializes each one); see
// parseExprListEnds. The `f{...}` and `f"..."` sugar report their single argument's last token (#262).
func (p *Parser) parseArgs() ([]ast.Expr, []int32, error) {
	switch p.tok.Kind {
	case token.LPAREN:
		// 5.1 ad-hoc check (lparser.c funcargs): a '(' on a different line
		// than the last token of the function prefix reports ambiguous syntax
		// -- `f\n(3)` looks like both a call and a new statement, and the
		// official compiler rejects it (removed in 5.2; kept since we lock to
		// 5.1). The STRING/LBRACE argument forms are not checked. Both plain
		// calls and obj:m\n(3) method calls go through here.
		if p.tok.Line != p.lastLine {
			return nil, nil, p.errorf("ambiguous syntax (function call x new statement) near '('")
		}
		if err := p.next(); err != nil {
			return nil, nil, err
		}
		if p.match(token.RPAREN) {
			if err := p.next(); err != nil {
				return nil, nil, err
			}
			return nil, nil, nil
		}
		exprs, ends, err := p.parseExprListEnds()
		if err != nil {
			return nil, nil, err
		}
		if err := p.expect(token.RPAREN); err != nil {
			return nil, nil, err
		}
		// The ')' is what discharges the LAST argument, so it takes the closing paren's line.
		if n := len(ends); n > 0 {
			ends[n-1] = p.lastLine
		}
		return exprs, ends, nil
	case token.LBRACE:
		// The sugar argument is pushed by funcargs' luaK_exp2nextreg after the whole constructor (or
		// string) has been consumed, so it materializes at its own last token: `f<nl>{<nl>}` puts the
		// constructor's relocation on the `}` line and `f<nl>"s"` the LOADK on the string's (#262).
		t, err := p.parseTableExpr()
		if err != nil {
			return nil, nil, err
		}
		return []ast.Expr{t}, []int32{p.lastLine}, nil
	case token.STRING:
		line := p.tok.Line
		s := p.tok.Str
		if err := p.next(); err != nil {
			return nil, nil, err
		}
		return []ast.Expr{&ast.StringExpr{Line: line, Val: s}}, []int32{p.lastLine}, nil
	}
	return nil, nil, p.errorf("function arguments expected near '%s'", p.tok.String())
}

// explist ::= expr {',' expr}
//
// parseExprListEnds parses an explist and additionally reports, per expression, the line of the last token
// consumed BEFORE the next one starts -- that is, ls->lastline at the moment PUC materializes it.
//
// PUC discharges each list element when it reaches the separator that follows it, and stamps the resulting
// instruction with lastline. So in `f(A.A<nl>,1<nl>)` the first element's GETTABLE lands on 2 (where the
// comma is) and the second's LOADK on 3 (where the ')' is) -- one shared end line for the whole list cannot
// express that (#252).
//
// The end line of the LAST element is not known here: it depends on the token that closes the construct,
// which only the caller consumes (a ')' for call args, `do` for a generic for, nothing at all for a return).
// It is left 0 and the caller fills it in.
func (p *Parser) parseExprListEnds() ([]ast.Expr, []int32, error) {
	first, err := p.parseExpr(0)
	if err != nil {
		return nil, nil, err
	}
	out := []ast.Expr{first}
	ends := []int32{0}
	for p.match(token.COMMA) {
		// p.lastLine is the line of the token just before this comma; the comma is what discharges the
		// element preceding it, and PUC reads lastline after consuming it.
		if err := p.next(); err != nil {
			return nil, nil, err
		}
		ends[len(ends)-1] = p.lastLine
		e, err := p.parseExpr(0)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, e)
		ends = append(ends, 0)
	}
	return out, ends, nil
}

// tableconstructor ::= '{' [field {fieldsep field} [fieldsep]] '}'
//
//	field    ::= '[' expr ']' '=' expr | Name '=' expr | expr
//	fieldsep ::= ',' | ';'
func (p *Parser) parseTableExpr() (ast.Expr, error) {
	line := p.tok.Line
	newTableLine := p.lastLine // PUC emits NEWTABLE before consuming `{`
	if err := p.expect(token.LBRACE); err != nil {
		return nil, err
	}
	t := &ast.TableExpr{Line: line, NewTableLine: newTableLine}
	// Item lines follow PUC's lastline at each emission point (#262). A key-value field is stored as
	// soon as its value is parsed, so it ends at the value's last token; a positional item is only
	// discharged by closelistfield after the following separator was consumed, or by lastlistfield
	// after the closing brace, so its line is set below, after the separator -- and, when it is the
	// constructor's last field, overwritten once `}` is consumed.
	lastPositional := -1
	for !p.match(token.RBRACE) {
		switch {
		case p.match(token.LBRACK):
			if err := p.next(); err != nil {
				return nil, err
			}
			k, err := p.parseExpr(0)
			if err != nil {
				return nil, err
			}
			keyEnd := p.lastLine // yindex discharges the key before `]` is consumed
			if err := p.expect(token.RBRACK); err != nil {
				return nil, err
			}
			if err := p.expect(token.EQ); err != nil {
				return nil, err
			}
			eqLine := p.lastLine // recfield's exp2RK of the key runs after checknext('=')
			v, err := p.parseExpr(0)
			if err != nil {
				return nil, err
			}
			t.Items = append(t.Items, ast.TableItem{Key: k, Val: v, KeyEndLine: keyEnd, EqLine: eqLine, EndLine: p.lastLine})
		case p.match(token.NAME):
			// Could be Name = expr, or Name as the start of a value expression.
			ahead, err := p.peek()
			if err != nil {
				return nil, err
			}
			if ahead.Kind == token.EQ {
				keyLine := p.tok.Line
				name := p.tok.Str
				if err := p.next(); err != nil {
					return nil, err
				}
				if err := p.expect(token.EQ); err != nil {
					return nil, err
				}
				eqLine := p.lastLine
				v, err := p.parseExpr(0)
				if err != nil {
					return nil, err
				}
				t.Items = append(t.Items, ast.TableItem{Key: &ast.StringExpr{Line: keyLine, Val: name}, Val: v,
					KeyEndLine: keyLine, EqLine: eqLine, EndLine: p.lastLine})
			} else {
				v, err := p.parseExpr(0)
				if err != nil {
					return nil, err
				}
				t.Items = append(t.Items, ast.TableItem{Val: v})
				lastPositional = len(t.Items) - 1
			}
		default:
			v, err := p.parseExpr(0)
			if err != nil {
				return nil, err
			}
			t.Items = append(t.Items, ast.TableItem{Val: v})
			lastPositional = len(t.Items) - 1
		}
		// fieldsep
		if !p.match(token.COMMA) && !p.match(token.SEMI) {
			break
		}
		if err := p.next(); err != nil {
			return nil, err
		}
		if lastPositional == len(t.Items)-1 {
			t.Items[lastPositional].EndLine = p.lastLine
		}
	}
	if err := p.expect(token.RBRACE); err != nil {
		return nil, err
	}
	t.CloseLine = p.lastLine
	// Only when the last positional item is the constructor's LAST field is it pushed by lastlistfield at
	// the `}`; with `k=v` fields after it, closelistfield already pushed it at its own separator.
	if lastPositional >= 0 && lastPositional == len(t.Items)-1 {
		t.Items[lastPositional].EndLine = p.lastLine
	}
	return t, nil
}

// funcbody ::= '(' [parlist] ')' block 'end'
//
//	parlist ::= namelist [',' '...'] | '...'
//
// When isMethod is true, an implicit "self" is injected at the head of Params.
func (p *Parser) parseFuncBody(startLine int32, isMethod bool) (*ast.FuncExpr, error) {
	if err := p.expect(token.LPAREN); err != nil {
		return nil, err
	}
	var params []string
	if isMethod {
		params = append(params, "self")
	}
	isVararg := false
	if !p.match(token.RPAREN) {
		for {
			if p.match(token.ELLIPSIS) {
				isVararg = true
				if err := p.next(); err != nil {
					return nil, err
				}
				break
			}
			if !p.match(token.NAME) {
				return nil, p.errorf("<name> expected near '%s'", p.tok.String())
			}
			params = append(params, p.tok.Str)
			if err := p.next(); err != nil {
				return nil, err
			}
			if !p.match(token.COMMA) {
				break
			}
			if err := p.next(); err != nil {
				return nil, err
			}
		}
	}
	if err := p.expect(token.RPAREN); err != nil {
		return nil, err
	}
	// Entering the function body: switch the insideVararg context; reset
	// loopDepth (break does not cross function boundaries).
	saved := p.insideVararg
	savedLoop := p.loopDepth
	p.insideVararg = isVararg
	p.loopDepth = 0
	body, err := p.parseBlock()
	p.insideVararg = saved
	p.loopDepth = savedLoop
	if err != nil {
		return nil, err
	}
	endLine := p.tok.Line
	if err := p.expect(token.KW_END); err != nil {
		return nil, err
	}
	return &ast.FuncExpr{Line: startLine, Params: params, IsVararg: isVararg, Body: body, EndLine: endLine}, nil
}
