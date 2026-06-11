package parse

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/frontend/ast"
	"github.com/Liam0205/wangshu/internal/frontend/lex"
)

func parseSrc(t *testing.T, src string) *ast.Block {
	t.Helper()
	lx := lex.New([]byte(src), "test")
	b, err := Parse(lx, "test")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	return b
}

func parseErr(t *testing.T, src string) error {
	t.Helper()
	lx := lex.New([]byte(src), "test")
	_, err := Parse(lx, "test")
	return err
}

func TestEmptyChunk(t *testing.T) {
	b := parseSrc(t, "")
	if len(b.Stmts) != 0 {
		t.Errorf("empty chunk has stmts: %v", b.Stmts)
	}
}

func TestLocalAssignment(t *testing.T) {
	b := parseSrc(t, "local x, y = 1, 2")
	if len(b.Stmts) != 1 {
		t.Fatalf("got %d stmts", len(b.Stmts))
	}
	ls, ok := b.Stmts[0].(*ast.LocalStmt)
	if !ok {
		t.Fatalf("type: %T", b.Stmts[0])
	}
	if len(ls.Names) != 2 || ls.Names[0] != "x" || ls.Names[1] != "y" {
		t.Errorf("names: %v", ls.Names)
	}
	if len(ls.Exprs) != 2 {
		t.Errorf("exprs: %d", len(ls.Exprs))
	}
}

func TestArithExprPrecedence(t *testing.T) {
	// 1 + 2 * 3 应解析为 1 + (2 * 3)。
	b := parseSrc(t, "local x = 1 + 2 * 3")
	ls := b.Stmts[0].(*ast.LocalStmt)
	bin := ls.Exprs[0].(*ast.BinExpr)
	if bin.Op != ast.OpAdd {
		t.Fatalf("top op: %d", bin.Op)
	}
	r := bin.R.(*ast.BinExpr)
	if r.Op != ast.OpMul {
		t.Errorf("right op: %d", r.Op)
	}
}

func TestPowerRightAssoc(t *testing.T) {
	// 2^3^2 = 2^(3^2) = 512(右结合)。
	b := parseSrc(t, "local x = 2^3^2")
	ls := b.Stmts[0].(*ast.LocalStmt)
	bin := ls.Exprs[0].(*ast.BinExpr)
	if bin.Op != ast.OpPow {
		t.Fatalf("top op: %d", bin.Op)
	}
	if l, ok := bin.L.(*ast.NumberExpr); !ok || l.Val != 2 {
		t.Errorf("left: %v", bin.L)
	}
	if r, ok := bin.R.(*ast.BinExpr); !ok || r.Op != ast.OpPow {
		t.Errorf("right: %T", bin.R)
	}
}

func TestConcatRightAssoc(t *testing.T) {
	// "a".."b".."c" = "a"..("b".."c")
	b := parseSrc(t, `local s = "a".."b".."c"`)
	ls := b.Stmts[0].(*ast.LocalStmt)
	bin := ls.Exprs[0].(*ast.BinExpr)
	if bin.Op != ast.OpConcat {
		t.Fatalf("top: %d", bin.Op)
	}
	r := bin.R.(*ast.BinExpr)
	if r.Op != ast.OpConcat {
		t.Errorf("right not concat")
	}
}

func TestUnaryAndComparison(t *testing.T) {
	b := parseSrc(t, "local v = not -a < b")
	ls := b.Stmts[0].(*ast.LocalStmt)
	// `not -a < b` ⇒ not ((-a) < b)?
	// Lua 5.1:not 是一元(prio 8),< 是 (3,3),- 是 (8) 一元——
	// 路径:not <unary>(-a) → not 后是更高 prio 的子表达式;然后 < b 在外层。
	// `not (-a) < b` 实际是 not(-a) < b → not 优先级 8 高于 < 的 3,所以是 (not (-a)) < b。
	bin, ok := ls.Exprs[0].(*ast.BinExpr)
	if !ok {
		t.Fatalf("top is not BinExpr: %T", ls.Exprs[0])
	}
	if bin.Op != ast.OpLt {
		t.Errorf("top op: %d", bin.Op)
	}
}

func TestIfElseif(t *testing.T) {
	src := `
if a then
  x = 1
elseif b then
  x = 2
else
  x = 3
end`
	b := parseSrc(t, src)
	ifs := b.Stmts[0].(*ast.IfStmt)
	if len(ifs.Clauses) != 2 {
		t.Errorf("clauses: %d", len(ifs.Clauses))
	}
	if ifs.Else == nil {
		t.Errorf("missing else")
	}
}

func TestNumericFor(t *testing.T) {
	b := parseSrc(t, "for i = 1, 10, 2 do x = i end")
	nf := b.Stmts[0].(*ast.NumForStmt)
	if nf.Var != "i" || nf.Step == nil {
		t.Errorf("for fields: var=%q step=%v", nf.Var, nf.Step)
	}
	b2 := parseSrc(t, "for i = 1, 10 do end")
	nf2 := b2.Stmts[0].(*ast.NumForStmt)
	if nf2.Step != nil {
		t.Errorf("default step should be nil, got %v", nf2.Step)
	}
}

func TestGenericFor(t *testing.T) {
	b := parseSrc(t, "for k, v in pairs(t) do x = k end")
	gf := b.Stmts[0].(*ast.GenForStmt)
	if len(gf.Names) != 2 || gf.Names[0] != "k" {
		t.Errorf("names: %v", gf.Names)
	}
	if len(gf.Exprs) != 1 {
		t.Errorf("exprs: %d", len(gf.Exprs))
	}
}

func TestAssignmentVsCallDisambig(t *testing.T) {
	// 赋值。
	b := parseSrc(t, "a, b.c = 1, 2")
	if _, ok := b.Stmts[0].(*ast.AssignStmt); !ok {
		t.Errorf("expected AssignStmt: %T", b.Stmts[0])
	}
	// 函数调用作语句。
	b2 := parseSrc(t, "f(x)")
	if cs, ok := b2.Stmts[0].(*ast.CallStmt); !ok {
		t.Errorf("expected CallStmt: %T", b2.Stmts[0])
	} else if _, ok := cs.Call.(*ast.CallExpr); !ok {
		t.Errorf("expected CallExpr: %T", cs.Call)
	}
	// 方法调用。
	b3 := parseSrc(t, "obj:m(1)")
	cs := b3.Stmts[0].(*ast.CallStmt)
	if _, ok := cs.Call.(*ast.MethodCallExpr); !ok {
		t.Errorf("expected MethodCallExpr: %T", cs.Call)
	}
	// 裸表达式不允许作语句。
	if err := parseErr(t, "a + b"); err == nil {
		t.Errorf("expected syntax error for bare expression")
	}
}

func TestTableConstructor(t *testing.T) {
	b := parseSrc(t, "local t = {1, 2, x = 3, [4+1] = 5; nested = {a=1}}")
	ls := b.Stmts[0].(*ast.LocalStmt)
	te := ls.Exprs[0].(*ast.TableExpr)
	if len(te.AKeys) != 2 {
		t.Errorf("array part: %d", len(te.AKeys))
	}
	if len(te.HKeys) != 3 {
		t.Errorf("hash part: %d", len(te.HKeys))
	}
}

func TestFunctionDefAndMethod(t *testing.T) {
	b := parseSrc(t, "function a.b:m(x, y) return x + y end")
	fs := b.Stmts[0].(*ast.FuncStmt)
	if !fs.IsMethod {
		t.Errorf("IsMethod should be true")
	}
	// self 自动注入。
	if len(fs.Fn.Params) < 1 || fs.Fn.Params[0] != "self" {
		t.Errorf("self injection: %v", fs.Fn.Params)
	}
}

func TestVarargFunctions(t *testing.T) {
	// vararg 函数体内 ... 合法。
	if err := parseErr(t, "local f = function(...) return ... end"); err != nil {
		t.Errorf("vararg fn: %v", err)
	}
	// 非 vararg 函数体内 ... 报错。
	if err := parseErr(t, "local f = function() return ... end"); err == nil {
		t.Errorf("expected error for ... in non-vararg fn")
	}
	// chunk 顶层是隐式 vararg。
	if err := parseErr(t, "return ..."); err != nil {
		t.Errorf("top-level vararg: %v", err)
	}
}

func TestRepeatScope(t *testing.T) {
	// repeat 的 until 在 body 作用域内可见局部(语法层 OK,作用域校验在 codegen)。
	b := parseSrc(t, "repeat local x = 1 until x > 0")
	rs := b.Stmts[0].(*ast.RepeatStmt)
	if rs.Cond == nil || len(rs.Body.Stmts) != 1 {
		t.Errorf("repeat fields")
	}
}

func TestReturnAndBreakAtEnd(t *testing.T) {
	// return 必须是 block 末句:之后还跟语句应报错。
	if err := parseErr(t, "return 1 local x = 2"); err == nil {
		t.Errorf("expected error: stmt after return")
	}
	// break 必须是 block 末句。
	if err := parseErr(t, "while true do break local x = 1 end"); err == nil {
		t.Errorf("expected error: stmt after break")
	}
}

func TestExample8FromDesignDoc(t *testing.T) {
	// 04 §10 / 02 §8 的 f(n) 求和示例。
	src := `local function f(n)
  local s = 0
  for i = 1, n do s = s + i*i end
  return s
end`
	b := parseSrc(t, src)
	if len(b.Stmts) != 1 {
		t.Fatalf("got %d stmts", len(b.Stmts))
	}
	lf, ok := b.Stmts[0].(*ast.LocalFuncStmt)
	if !ok {
		t.Fatalf("type: %T", b.Stmts[0])
	}
	if lf.Name != "f" {
		t.Errorf("name: %q", lf.Name)
	}
	if len(lf.Fn.Params) != 1 || lf.Fn.Params[0] != "n" {
		t.Errorf("params: %v", lf.Fn.Params)
	}
	if len(lf.Fn.Body.Stmts) != 3 {
		t.Errorf("body stmts: %d", len(lf.Fn.Body.Stmts))
	}
}

func TestLineNumbersPropagate(t *testing.T) {
	b := parseSrc(t, "local a = 1\n\nlocal b = 2")
	if b.Stmts[0].Pos() != 1 {
		t.Errorf("first stmt line: %d", b.Stmts[0].Pos())
	}
	if b.Stmts[1].Pos() != 3 {
		t.Errorf("second stmt line: %d", b.Stmts[1].Pos())
	}
}

func TestLexErrorPropagates(t *testing.T) {
	err := parseErr(t, "local x = ?")
	if err == nil {
		t.Fatal("expected error")
	}
	pe, ok := err.(*Error)
	if !ok {
		t.Fatalf("expected *parse.Error, got %T", err)
	}
	if pe.Source != "test" || pe.Line != 1 {
		t.Errorf("source/line: %q/%d", pe.Source, pe.Line)
	}
}
