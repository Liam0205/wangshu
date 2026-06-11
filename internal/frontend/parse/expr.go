// Expression parsing — precedence climbing (04 §4.3) + prefix/primary chain (04 §4 / §7)。
package parse

import (
	"github.com/Liam0205/wangshu/internal/frontend/ast"
	"github.com/Liam0205/wangshu/internal/frontend/token"
)

// 二元运算符的 (left, right) 优先级(04 §4.3,与 Lua 5.1 lparser.c 一致)。
// 右结合 ⟺ right < left。
type binPrio struct{ left, right uint8 }

var binPriorities = map[ast.BinOp]binPrio{
	ast.OpOr:  {1, 1},
	ast.OpAnd: {2, 2},
	ast.OpLt:  {3, 3}, ast.OpGt: {3, 3}, ast.OpLe: {3, 3}, ast.OpGe: {3, 3}, ast.OpNe: {3, 3}, ast.OpEq: {3, 3},
	ast.OpConcat: {5, 4}, // 右结合
	ast.OpAdd:    {6, 6}, ast.OpSub: {6, 6},
	ast.OpMul: {7, 7}, ast.OpDiv: {7, 7}, ast.OpMod: {7, 7},
	// unary = 8
	ast.OpPow: {10, 9}, // 右结合,高于一元
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

// parseExpr is precedence-climbing subexpr (04 §4.3)。limit = 0 时解析完整表达式。
func (p *Parser) parseExpr(limit uint8) (ast.Expr, error) {
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
		e = &ast.UnExpr{Line: line, Op: uop, E: sub}
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
		e = &ast.BinExpr{Line: line, Op: bop, L: e, R: rhs}
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
		if err := p.next(); err != nil {
			return nil, err
		}
		return &ast.VarargExpr{Line: line}, nil
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
		// 括号强制单值:仅当内部是多值源(Call/Vararg)时需要 ParenExpr 包裹
		switch inner.(type) {
		case *ast.CallExpr, *ast.MethodCallExpr, *ast.VarargExpr:
			e = &ast.ParenExpr{Line: line, E: inner}
		default:
			e = inner
		}
	default:
		return nil, p.errorf("unexpected symbol near %s", p.tok.String())
	}
	for {
		switch p.tok.Kind {
		case token.DOT:
			if err := p.next(); err != nil {
				return nil, err
			}
			if !p.match(token.NAME) {
				return nil, p.errorf("<name> expected near %s", p.tok.String())
			}
			e = &ast.IndexExpr{Line: p.tok.Line, Obj: e, Key: &ast.StringExpr{Line: p.tok.Line, Val: p.tok.Str}}
			if err := p.next(); err != nil {
				return nil, err
			}
		case token.LBRACK:
			if err := p.next(); err != nil {
				return nil, err
			}
			key, err := p.parseExpr(0)
			if err != nil {
				return nil, err
			}
			if err := p.expect(token.RBRACK); err != nil {
				return nil, err
			}
			e = &ast.IndexExpr{Line: e.Pos(), Obj: e, Key: key}
		case token.COLON:
			if err := p.next(); err != nil {
				return nil, err
			}
			if !p.match(token.NAME) {
				return nil, p.errorf("<name> expected near %s", p.tok.String())
			}
			method := p.tok.Str
			line := p.tok.Line
			if err := p.next(); err != nil {
				return nil, err
			}
			args, err := p.parseArgs()
			if err != nil {
				return nil, err
			}
			e = &ast.MethodCallExpr{Line: line, Recv: e, Method: method, Args: args}
		case token.LPAREN, token.STRING, token.LBRACE:
			args, err := p.parseArgs()
			if err != nil {
				return nil, err
			}
			e = &ast.CallExpr{Line: e.Pos(), Fn: e, Args: args}
		default:
			return e, nil
		}
	}
}

// args ::= '(' [explist] ')' | tableexpr | STRING
func (p *Parser) parseArgs() ([]ast.Expr, error) {
	switch p.tok.Kind {
	case token.LPAREN:
		if err := p.next(); err != nil {
			return nil, err
		}
		if p.match(token.RPAREN) {
			if err := p.next(); err != nil {
				return nil, err
			}
			return nil, nil
		}
		exprs, err := p.parseExprList()
		if err != nil {
			return nil, err
		}
		if err := p.expect(token.RPAREN); err != nil {
			return nil, err
		}
		return exprs, nil
	case token.LBRACE:
		t, err := p.parseTableExpr()
		if err != nil {
			return nil, err
		}
		return []ast.Expr{t}, nil
	case token.STRING:
		line := p.tok.Line
		s := p.tok.Str
		if err := p.next(); err != nil {
			return nil, err
		}
		return []ast.Expr{&ast.StringExpr{Line: line, Val: s}}, nil
	}
	return nil, p.errorf("function arguments expected near %s", p.tok.String())
}

// explist ::= expr {',' expr}
func (p *Parser) parseExprList() ([]ast.Expr, error) {
	first, err := p.parseExpr(0)
	if err != nil {
		return nil, err
	}
	out := []ast.Expr{first}
	for p.match(token.COMMA) {
		if err := p.next(); err != nil {
			return nil, err
		}
		e, err := p.parseExpr(0)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// tableconstructor ::= '{' [field {fieldsep field} [fieldsep]] '}'
//
//	field    ::= '[' expr ']' '=' expr | Name '=' expr | expr
//	fieldsep ::= ',' | ';'
func (p *Parser) parseTableExpr() (ast.Expr, error) {
	line := p.tok.Line
	if err := p.expect(token.LBRACE); err != nil {
		return nil, err
	}
	t := &ast.TableExpr{Line: line}
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
			if err := p.expect(token.RBRACK); err != nil {
				return nil, err
			}
			if err := p.expect(token.EQ); err != nil {
				return nil, err
			}
			v, err := p.parseExpr(0)
			if err != nil {
				return nil, err
			}
			t.HKeys = append(t.HKeys, k)
			t.HVals = append(t.HVals, v)
		case p.match(token.NAME):
			// 可能是 Name = expr,也可能是 Name 作为值表达式起点。
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
				v, err := p.parseExpr(0)
				if err != nil {
					return nil, err
				}
				t.HKeys = append(t.HKeys, &ast.StringExpr{Line: keyLine, Val: name})
				t.HVals = append(t.HVals, v)
			} else {
				v, err := p.parseExpr(0)
				if err != nil {
					return nil, err
				}
				t.AKeys = append(t.AKeys, v)
			}
		default:
			v, err := p.parseExpr(0)
			if err != nil {
				return nil, err
			}
			t.AKeys = append(t.AKeys, v)
		}
		// fieldsep
		if !p.match(token.COMMA) && !p.match(token.SEMI) {
			break
		}
		if err := p.next(); err != nil {
			return nil, err
		}
	}
	if err := p.expect(token.RBRACE); err != nil {
		return nil, err
	}
	return t, nil
}

// funcbody ::= '(' [parlist] ')' block 'end'
//
//	parlist ::= namelist [',' '...'] | '...'
//
// 当 isMethod 为 true,在 Params 头部注入隐式 "self"。
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
				return nil, p.errorf("<name> expected near %s", p.tok.String())
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
	// 进入函数体:切换 insideVararg 上下文。
	saved := p.insideVararg
	p.insideVararg = isVararg
	body, err := p.parseBlock()
	p.insideVararg = saved
	if err != nil {
		return nil, err
	}
	endLine := p.tok.Line
	if err := p.expect(token.KW_END); err != nil {
		return nil, err
	}
	return &ast.FuncExpr{Line: startLine, Params: params, IsVararg: isVararg, Body: body, EndLine: endLine}, nil
}
