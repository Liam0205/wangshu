// Package ast defines the AST node types produced by the parser (04 §3).
//
// 节点是纯 Go 堆数据(不入 arena);编译后可被 GC。所有节点带 Line int32(取自首 token,
// 用于错误与未来 LineInfo)。Sealed-interface(空方法 exprNode/stmtNode)使 codegen 的类型
// switch 穷尽性更易审查。
package ast

// Node is the common interface for all AST nodes.
type Node interface{ Pos() int32 }

// Expr is the interface for expressions (sealed via exprNode()).
type Expr interface {
	Node
	exprNode()
}

// Stmt is the interface for statements (sealed via stmtNode()).
type Stmt interface {
	Node
	stmtNode()
}

// ----- Literals -----

type NilExpr struct{ Line int32 }
type TrueExpr struct{ Line int32 }
type FalseExpr struct{ Line int32 }
type NumberExpr struct {
	Line int32
	Val  float64
}
type StringExpr struct {
	Line int32
	Val  string
}
type VarargExpr struct{ Line int32 }

func (e *NilExpr) Pos() int32    { return e.Line }
func (e *TrueExpr) Pos() int32   { return e.Line }
func (e *FalseExpr) Pos() int32  { return e.Line }
func (e *NumberExpr) Pos() int32 { return e.Line }
func (e *StringExpr) Pos() int32 { return e.Line }
func (e *VarargExpr) Pos() int32 { return e.Line }
func (*NilExpr) exprNode()       {}
func (*TrueExpr) exprNode()      {}
func (*FalseExpr) exprNode()     {}
func (*NumberExpr) exprNode()    {}
func (*StringExpr) exprNode()    {}
func (*VarargExpr) exprNode()    {}

// ----- Variables / prefix -----

type NameExpr struct {
	Line int32
	Name string
}
type IndexExpr struct {
	Line int32
	Obj  Expr
	Key  Expr
}

// ParenExpr 包裹括号表达式:`(f())` 强制单值(04 §9.4 / Lua 5.1 语义)。
type ParenExpr struct {
	Line int32
	E    Expr
}

func (e *NameExpr) Pos() int32  { return e.Line }
func (e *IndexExpr) Pos() int32 { return e.Line }
func (e *ParenExpr) Pos() int32 { return e.Line }
func (*NameExpr) exprNode()     {}
func (*IndexExpr) exprNode()    {}
func (*ParenExpr) exprNode()    {}

// ----- Calls -----

type CallExpr struct {
	Line int32
	Fn   Expr
	Args []Expr
}
type MethodCallExpr struct {
	Line   int32
	Recv   Expr
	Method string
	Args   []Expr
}

func (e *CallExpr) Pos() int32       { return e.Line }
func (e *MethodCallExpr) Pos() int32 { return e.Line }
func (*CallExpr) exprNode()          {}
func (*MethodCallExpr) exprNode()    {}

// ----- Operators -----

type BinOp uint8

const (
	OpAdd BinOp = iota
	OpSub
	OpMul
	OpDiv
	OpMod
	OpPow
	OpConcat
	OpEq
	OpNe
	OpLt
	OpLe
	OpGt
	OpGe
	OpAnd
	OpOr
)

type UnOp uint8

const (
	OpUnm UnOp = iota
	OpNot
	OpLen
)

type BinExpr struct {
	Line int32
	Op   BinOp
	L, R Expr
}
type UnExpr struct {
	Line int32
	Op   UnOp
	E    Expr
}

func (e *BinExpr) Pos() int32 { return e.Line }
func (e *UnExpr) Pos() int32  { return e.Line }
func (*BinExpr) exprNode()    {}
func (*UnExpr) exprNode()     {}

// ----- Functions -----

type FuncExpr struct {
	Line     int32
	Params   []string
	IsVararg bool
	// NoArgTable:main chunk 合成的 FuncExpr 置 true——官方 main 只有
	// VARARG_ISVARARG,不带 HASARG(无隐式 arg 表;LUA_COMPAT_VARARG)。
	NoArgTable bool
	Body       *Block
	EndLine    int32
}

func (e *FuncExpr) Pos() int32 { return e.Line }
func (*FuncExpr) exprNode()    {}

// ----- Table constructor -----

type TableExpr struct {
	Line  int32
	AKeys []Expr // 数组部分:无键项,按出现序;末位可多值
	HKeys []Expr // 哈希部分键(与 HVals 等长)
	HVals []Expr // 哈希部分值
}

func (e *TableExpr) Pos() int32 { return e.Line }
func (*TableExpr) exprNode()    {}

// ----- Statements -----

type Block struct {
	Stmts []Stmt
}

type LocalStmt struct {
	Line  int32
	Names []string
	Exprs []Expr
}
type LocalFuncStmt struct {
	Line int32
	Name string
	Fn   *FuncExpr
}
type AssignStmt struct {
	Line    int32
	Targets []Expr // 每项必须是 NameExpr 或 IndexExpr(parser 校验)
	Exprs   []Expr
}
type CallStmt struct {
	Line int32
	Call Expr // CallExpr 或 MethodCallExpr
}
type DoStmt struct {
	Line int32
	Body *Block
}
type WhileStmt struct {
	Line int32
	Cond Expr
	Body *Block
}
type RepeatStmt struct {
	Line int32
	Body *Block
	Cond Expr // until 在 Body 作用域内可见局部
}

type IfClause struct {
	Cond Expr
	Body *Block
}
type IfStmt struct {
	Line    int32
	Clauses []IfClause
	Else    *Block // 可空
}

type NumForStmt struct {
	Line  int32
	Var   string
	Init  Expr
	Limit Expr
	Step  Expr // 可空 → 默认 1
	Body  *Block
}
type GenForStmt struct {
	Line  int32
	Names []string
	Exprs []Expr // 迭代器三元组来源
	Body  *Block
}
type FuncStmt struct {
	Line     int32
	Target   Expr // NameExpr / IndexExpr 链
	IsMethod bool // a.b:m → 给 Fn.Params 注入隐式 self
	Fn       *FuncExpr
}
type ReturnStmt struct {
	Line  int32
	Exprs []Expr
}
type BreakStmt struct{ Line int32 }

func (s *LocalStmt) Pos() int32     { return s.Line }
func (s *LocalFuncStmt) Pos() int32 { return s.Line }
func (s *AssignStmt) Pos() int32    { return s.Line }
func (s *CallStmt) Pos() int32      { return s.Line }
func (s *DoStmt) Pos() int32        { return s.Line }
func (s *WhileStmt) Pos() int32     { return s.Line }
func (s *RepeatStmt) Pos() int32    { return s.Line }
func (s *IfStmt) Pos() int32        { return s.Line }
func (s *NumForStmt) Pos() int32    { return s.Line }
func (s *GenForStmt) Pos() int32    { return s.Line }
func (s *FuncStmt) Pos() int32      { return s.Line }
func (s *ReturnStmt) Pos() int32    { return s.Line }
func (s *BreakStmt) Pos() int32     { return s.Line }

func (*LocalStmt) stmtNode()     {}
func (*LocalFuncStmt) stmtNode() {}
func (*AssignStmt) stmtNode()    {}
func (*CallStmt) stmtNode()      {}
func (*DoStmt) stmtNode()        {}
func (*WhileStmt) stmtNode()     {}
func (*RepeatStmt) stmtNode()    {}
func (*IfStmt) stmtNode()        {}
func (*NumForStmt) stmtNode()    {}
func (*GenForStmt) stmtNode()    {}
func (*FuncStmt) stmtNode()      {}
func (*ReturnStmt) stmtNode()    {}
func (*BreakStmt) stmtNode()     {}
