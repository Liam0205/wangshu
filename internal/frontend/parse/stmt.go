// Statement dispatch (04 §4.2).
package parse

import (
	"github.com/Liam0205/wangshu/internal/frontend/ast"
	"github.com/Liam0205/wangshu/internal/frontend/token"
)

func (p *Parser) parseStatement() (ast.Stmt, error) {
	switch p.tok.Kind {
	case token.KW_LOCAL:
		return p.parseLocal()
	case token.KW_IF:
		return p.parseIf()
	case token.KW_WHILE:
		return p.parseWhile()
	case token.KW_DO:
		return p.parseDo()
	case token.KW_FOR:
		return p.parseFor()
	case token.KW_REPEAT:
		return p.parseRepeat()
	case token.KW_FUNCTION:
		return p.parseFunctionStmt()
	default:
		return p.parseExprStmt()
	}
}

// local Name {, Name} ['=' explist]  |  local function Name funcbody
func (p *Parser) parseLocal() (ast.Stmt, error) {
	line := p.tok.Line
	if err := p.next(); err != nil { // eat 'local'
		return nil, err
	}
	if p.match(token.KW_FUNCTION) {
		if err := p.next(); err != nil {
			return nil, err
		}
		if !p.match(token.NAME) {
			return nil, p.errorf("<name> expected near '%s'", p.tok.String())
		}
		name := p.tok.Str
		if err := p.next(); err != nil {
			return nil, err
		}
		fn, err := p.parseFuncBody(line, false)
		if err != nil {
			return nil, err
		}
		return &ast.LocalFuncStmt{Line: line, Name: name, Fn: fn}, nil
	}
	// names
	names := []string{}
	for {
		if !p.match(token.NAME) {
			return nil, p.errorf("<name> expected near '%s'", p.tok.String())
		}
		names = append(names, p.tok.Str)
		if err := p.next(); err != nil {
			return nil, err
		}
		ok, err := p.consume(token.COMMA)
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
	}
	var exprs []ast.Expr
	if p.match(token.EQ) {
		if err := p.next(); err != nil {
			return nil, err
		}
		var err error
		exprs, err = p.parseExprList()
		if err != nil {
			return nil, err
		}
	}
	return &ast.LocalStmt{Line: line, Names: names, Exprs: exprs}, nil
}

// if cond then block {elseif cond then block} [else block] end
func (p *Parser) parseIf() (ast.Stmt, error) {
	line := p.tok.Line
	if err := p.next(); err != nil { // 'if'
		return nil, err
	}
	clauses := []ast.IfClause{}
	for {
		cond, err := p.parseExpr(0)
		if err != nil {
			return nil, err
		}
		if err := p.expect(token.KW_THEN); err != nil {
			return nil, err
		}
		body, err := p.parseBlock()
		if err != nil {
			return nil, err
		}
		clauses = append(clauses, ast.IfClause{Cond: cond, Body: body})
		if !p.match(token.KW_ELSEIF) {
			break
		}
		if err := p.next(); err != nil {
			return nil, err
		}
	}
	var elseBody *ast.Block
	if p.match(token.KW_ELSE) {
		if err := p.next(); err != nil {
			return nil, err
		}
		var err error
		elseBody, err = p.parseBlock()
		if err != nil {
			return nil, err
		}
	}
	if err := p.expect(token.KW_END); err != nil {
		return nil, err
	}
	return &ast.IfStmt{Line: line, Clauses: clauses, Else: elseBody}, nil
}

// while cond do block end
func (p *Parser) parseWhile() (ast.Stmt, error) {
	line := p.tok.Line
	if err := p.next(); err != nil {
		return nil, err
	}
	cond, err := p.parseExpr(0)
	if err != nil {
		return nil, err
	}
	if err := p.expect(token.KW_DO); err != nil {
		return nil, err
	}
	p.loopDepth++
	body, err := p.parseBlock()
	p.loopDepth--
	if err != nil {
		return nil, err
	}
	if err := p.expect(token.KW_END); err != nil {
		return nil, err
	}
	return &ast.WhileStmt{Line: line, Cond: cond, Body: body}, nil
}

// do block end
func (p *Parser) parseDo() (ast.Stmt, error) {
	line := p.tok.Line
	if err := p.next(); err != nil {
		return nil, err
	}
	body, err := p.parseBlock()
	if err != nil {
		return nil, err
	}
	if err := p.expect(token.KW_END); err != nil {
		return nil, err
	}
	return &ast.DoStmt{Line: line, Body: body}, nil
}

// repeat block until cond  (locals are visible in the until scope, 04 §3.3)
func (p *Parser) parseRepeat() (ast.Stmt, error) {
	line := p.tok.Line
	if err := p.next(); err != nil {
		return nil, err
	}
	p.loopDepth++
	body, err := p.parseBlock()
	p.loopDepth--
	if err != nil {
		return nil, err
	}
	if err := p.expect(token.KW_UNTIL); err != nil {
		return nil, err
	}
	cond, err := p.parseExpr(0)
	if err != nil {
		return nil, err
	}
	return &ast.RepeatStmt{Line: line, Body: body, Cond: cond}, nil
}

// for Name '=' init ',' limit [',' step] do block end       (numeric)
// for Namelist 'in' explist do block end                     (generic)
func (p *Parser) parseFor() (ast.Stmt, error) {
	line := p.tok.Line
	if err := p.next(); err != nil { // 'for'
		return nil, err
	}
	if !p.match(token.NAME) {
		return nil, p.errorf("<name> expected near '%s'", p.tok.String())
	}
	first := p.tok.Str
	if err := p.next(); err != nil {
		return nil, err
	}
	switch p.tok.Kind {
	case token.EQ:
		// numeric for
		if err := p.next(); err != nil {
			return nil, err
		}
		init, err := p.parseExpr(0)
		if err != nil {
			return nil, err
		}
		if err := p.expect(token.COMMA); err != nil {
			return nil, err
		}
		limit, err := p.parseExpr(0)
		if err != nil {
			return nil, err
		}
		var step ast.Expr
		if p.match(token.COMMA) {
			if err := p.next(); err != nil {
				return nil, err
			}
			step, err = p.parseExpr(0)
			if err != nil {
				return nil, err
			}
		}
		if err := p.expect(token.KW_DO); err != nil {
			return nil, err
		}
		p.loopDepth++
		body, err := p.parseBlock()
		p.loopDepth--
		if err != nil {
			return nil, err
		}
		if err := p.expect(token.KW_END); err != nil {
			return nil, err
		}
		return &ast.NumForStmt{Line: line, Var: first, Init: init, Limit: limit, Step: step, Body: body}, nil
	case token.COMMA, token.KW_IN:
		// generic for
		names := []string{first}
		for p.match(token.COMMA) {
			if err := p.next(); err != nil {
				return nil, err
			}
			if !p.match(token.NAME) {
				return nil, p.errorf("<name> expected near '%s'", p.tok.String())
			}
			names = append(names, p.tok.Str)
			if err := p.next(); err != nil {
				return nil, err
			}
		}
		if err := p.expect(token.KW_IN); err != nil {
			return nil, err
		}
		exprs, err := p.parseExprList()
		if err != nil {
			return nil, err
		}
		if err := p.expect(token.KW_DO); err != nil {
			return nil, err
		}
		p.loopDepth++
		body, err := p.parseBlock()
		p.loopDepth--
		if err != nil {
			return nil, err
		}
		if err := p.expect(token.KW_END); err != nil {
			return nil, err
		}
		return &ast.GenForStmt{Line: line, Names: names, Exprs: exprs, Body: body}, nil
	default:
		return nil, p.errorf("'=' or 'in' expected near '%s'", p.tok.String())
	}
}

// function funcname funcbody              (Lua 5.1)
//
//	funcname ::= Name {'.' Name} [':' Name]
func (p *Parser) parseFunctionStmt() (ast.Stmt, error) {
	line := p.tok.Line
	if err := p.next(); err != nil { // 'function'
		return nil, err
	}
	if !p.match(token.NAME) {
		return nil, p.errorf("<name> expected near '%s'", p.tok.String())
	}
	var target ast.Expr = &ast.NameExpr{Line: p.tok.Line, Name: p.tok.Str}
	if err := p.next(); err != nil {
		return nil, err
	}
	for p.match(token.DOT) {
		if err := p.next(); err != nil {
			return nil, err
		}
		if !p.match(token.NAME) {
			return nil, p.errorf("<name> expected near '%s'", p.tok.String())
		}
		target = &ast.IndexExpr{Line: p.tok.Line, Obj: target, Key: &ast.StringExpr{Line: p.tok.Line, Val: p.tok.Str}}
		if err := p.next(); err != nil {
			return nil, err
		}
	}
	isMethod := false
	if p.match(token.COLON) {
		if err := p.next(); err != nil {
			return nil, err
		}
		if !p.match(token.NAME) {
			return nil, p.errorf("<name> expected near '%s'", p.tok.String())
		}
		target = &ast.IndexExpr{Line: p.tok.Line, Obj: target, Key: &ast.StringExpr{Line: p.tok.Line, Val: p.tok.Str}}
		isMethod = true
		if err := p.next(); err != nil {
			return nil, err
		}
	}
	fn, err := p.parseFuncBody(line, isMethod)
	if err != nil {
		return nil, err
	}
	return &ast.FuncStmt{Line: line, Target: target, IsMethod: isMethod, Fn: fn}, nil
}

// return [explist] [;]
func (p *Parser) parseReturn() (ast.Stmt, error) {
	line := p.tok.Line
	if err := p.next(); err != nil {
		return nil, err
	}
	if isBlockEnd(p.tok.Kind) || p.match(token.SEMI) {
		return &ast.ReturnStmt{Line: line}, nil
	}
	exprs, err := p.parseExprList()
	if err != nil {
		return nil, err
	}
	return &ast.ReturnStmt{Line: line, Exprs: exprs}, nil
}

// expression-statement: starts with a prefixexp; if followed by '='/',' it's an
// AssignStmt, otherwise a CallStmt (04 §4.4).
func (p *Parser) parseExprStmt() (ast.Stmt, error) {
	line := p.tok.Line
	first, err := p.parsePrefixExpr()
	if err != nil {
		return nil, err
	}
	if p.match(token.EQ) || p.match(token.COMMA) {
		// assignment.
		if !isAssignable(first) {
			return nil, p.errorf("syntax error near '%s'", p.tok.String())
		}
		targets := []ast.Expr{first}
		for p.match(token.COMMA) {
			if err := p.next(); err != nil {
				return nil, err
			}
			t, err := p.parsePrefixExpr()
			if err != nil {
				return nil, err
			}
			if !isAssignable(t) {
				return nil, p.errorf("syntax error near '%s'", p.tok.String())
			}
			targets = append(targets, t)
		}
		if err := p.expect(token.EQ); err != nil {
			return nil, err
		}
		exprs, err := p.parseExprList()
		if err != nil {
			return nil, err
		}
		return &ast.AssignStmt{Line: line, Targets: targets, Exprs: exprs}, nil
	}
	// call statement: first must be a Call/MethodCall.
	switch first.(type) {
	case *ast.CallExpr, *ast.MethodCallExpr:
		return &ast.CallStmt{Line: line, Call: first}, nil
	default:
		return nil, p.errorf("syntax error near '%s'", p.tok.String())
	}
}

func isAssignable(e ast.Expr) bool {
	switch e.(type) {
	case *ast.NameExpr, *ast.IndexExpr:
		return true
	}
	return false
}
