// Package ast defines the AST node types produced by the parser (04 §3).
//
// Nodes are plain Go heap data (not placed in the arena); they can be GC'd after compilation.
// Every node carries Line int32 (taken from the first token, used for errors and future LineInfo).
// The sealed interface (empty exprNode/stmtNode methods) makes the codegen type
// switch easier to audit for exhaustiveness.
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

// ParenExpr wraps a parenthesized expression: `(f())` forces a single value (04 §9.4 / Lua 5.1 semantics).
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
	// NoArgTable: the synthesized main-chunk FuncExpr sets this true —— the official main only has
	// VARARG_ISVARARG, without HASARG (no implicit arg table; LUA_COMPAT_VARARG).
	NoArgTable bool
	Body       *Block
	EndLine    int32
}

func (e *FuncExpr) Pos() int32 { return e.Line }
func (*FuncExpr) exprNode()    {}

// ----- Table constructor -----

type TableExpr struct {
	Line int32
	// Items holds all fields in **source appearance order**: PUC's constructor code
	// emits, in order and interleaved, SETTABLE (key-value fields, immediately) and
	// SETLIST (positional fields, batched), where later writes overwrite earlier ones
	// (in {B,0,C,[1]=""} the SETLIST positional item overwrites [1]="").
	// Splitting into separate array/hash lists would lose ordering (caught by cgo-oracle differential fuzzing).
	Items []TableItem
}

// TableItem is one field of a table constructor: Key == nil means a positional (array) item.
type TableItem struct {
	Key Expr // nil = positional item; non-nil = [k]=v or name=v
	Val Expr
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
	Targets []Expr // each item must be a NameExpr or IndexExpr (parser-validated)
	Exprs   []Expr
}
type CallStmt struct {
	Line int32
	Call Expr // CallExpr or MethodCallExpr
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
	Cond Expr // until can see locals within the Body scope
}

type IfClause struct {
	Cond Expr
	Body *Block
}
type IfStmt struct {
	Line    int32
	Clauses []IfClause
	Else    *Block // nullable
}

type NumForStmt struct {
	Line  int32
	Var   string
	Init  Expr
	Limit Expr
	Step  Expr // nullable → defaults to 1
	Body  *Block
}
type GenForStmt struct {
	Line  int32
	Names []string
	Exprs []Expr // source of the iterator triple
	Body  *Block
}
type FuncStmt struct {
	Line     int32
	Target   Expr // NameExpr / IndexExpr chain
	IsMethod bool // a.b:m → inject an implicit self into Fn.Params
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
