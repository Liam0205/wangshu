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

// VarargExpr's Line is the `...` token's line; EmitLine is ls->lastline when PUC's simpleexp emits
// VARARG, i.e. BEFORE `...` is consumed, so it is the preceding token's end line -- `return<nl>...` puts
// VARARG on 1. Zero falls back to Line (#262).
type VarargExpr struct {
	Line     int32
	EmitLine int32
}

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
	// ObjEndLine is ls->lastline when PUC's field/yindex discharges the OBJECT: the `.`/`[` is the
	// current token, so this is the end line of the last consumed token -- normally the object's own
	// last token, but inside a table constructor the NAME/`=` lookahead has already scanned the
	// operator, and PUC's lastline follows the scanner: `{<nl>A<nl>.x}` puts A's GETGLOBAL on 3, the
	// `.x` line. Zero falls back to Obj.Pos() (#262).
	ObjEndLine int32
	// KeyEndLine is ls->lastline when PUC's yindex runs luaK_exp2val on a bracket key: the key's last
	// token, before `]` is consumed. A deferred GETTABLE inside the key is discharged there
	// (`A[B<nl>.c]` indexes B on 2). CloseLine is the `]` line, where luaK_indexed's exp2RK then turns a
	// comparison key into its LOADBOOL pair. Both zero for a name key (#262).
	KeyEndLine int32
	CloseLine  int32
	Obj        Expr
	Key        Expr
}

// ParenExpr wraps a parenthesized expression: `(f())` forces a single value (04 §9.4 / Lua 5.1 semantics).
type ParenExpr struct {
	Line int32
	// EndLine is the line of the CLOSING paren, mirroring ls->lastline at the point PUC's primaryexp
	// discharges the inner expression: it calls luaK_dischargevars only AFTER check_match(')'), so a
	// deferred GETTABLE inside the parens is stamped with the closing paren's line, not the opening
	// one. Nested parens each advance it, so `((A.A<nl>)<nl>)()` puts GETTABLE on the INNER paren's
	// line (2) and CALL on the outer's (3) -- which is why this is per-node and not a whole-expression
	// end line (#252).
	EndLine int32
	E       Expr
}

func (e *NameExpr) Pos() int32  { return e.Line }
func (e *IndexExpr) Pos() int32 { return e.Line }
func (e *ParenExpr) Pos() int32 { return e.Line }
func (*NameExpr) exprNode()     {}
func (*IndexExpr) exprNode()    {}
func (*ParenExpr) exprNode()    {}

// ----- Calls -----

type CallExpr struct {
	// Line is where the CALLEE expression starts; ArgsLine is where the argument list does.
	//
	// They differ only for a multi-line call, and PUC keeps them separate: primaryexp
	// materializes the callee at its own line, then funcargs' luaK_fixline moves only the CALL
	// instruction to the argument list's line. Using one line for both put the callee's
	// GETTABLE on the argument line too, so `t.x\n{1}` blamed the wrong line for indexing nil.
	Line     int32
	ArgsLine int32
	// FnEndLine is ls->lastline when PUC's funcargs pushes the CALLEE (luaK_exp2nextreg at its start):
	// the end line of the last consumed token before the argument list, i.e. the callee's own last
	// token -- a closing paren for `(0<nl>)(...)`, the lookahead's line inside a table constructor.
	// Zero falls back to Line (#252 calleeEndLine, generalized in #262).
	FnEndLine int32
	// ArgEndLines[i] is ls->lastline at the point PUC materializes Args[i]: the line of the separator
	// that follows it (a ',' for every argument but the last, the ')' for the last one). Each argument
	// gets its own, because `f(A.A<nl>,1<nl>)` discharges the first on 2 and the second on 3 (#252).
	// Nil or short means "no end line known", and the argument falls back to ArgsLine.
	ArgEndLines []int32
	Fn          Expr
	Args        []Expr
}
type MethodCallExpr struct {
	// Line is the method-name line (used for SELF, as PUC's luaK_self does); ArgsLine is the
	// argument list's line, which only the CALL uses.
	Line     int32
	ArgsLine int32
	// ArgEndLines mirrors CallExpr.ArgEndLines (#252).
	ArgEndLines []int32
	Recv        Expr
	Method      string
	Args        []Expr
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
	// Line is the operator's line: where PUC's luaK_infix runs, i.e. where the LEFT operand is
	// materialized (and TEST/JMP for and/or are emitted).
	Line int32
	// EndLine is ls->lastline when PUC's luaK_posfix runs: the line of the last token of the RIGHT
	// operand. The right operand's materialization, the arithmetic/comparison instruction itself, the
	// comparison's JMP, and CONCAT are all stamped with it, so `error("boom"%<nl>0)` reports line 2, the
	// line of `0`, not the line of `%` (#262).
	EndLine int32
	Op      BinOp
	L, R    Expr
}
type UnExpr struct {
	// Line is the operator's line.
	Line int32
	// EndLine is ls->lastline when PUC's luaK_prefix runs: the line of the operand's last token. The
	// operand's materialization and the UNM/NOT/LEN instruction take it (#262).
	EndLine int32
	Op      UnOp
	E       Expr
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
	// EndLine is the `end` keyword's line. PUC's body() emits the CLOSURE (and its MOVE/GETUPVAL
	// pseudo-instructions) from pushclosure after check_match(TK_END), so they carry it too (#262).
	EndLine int32
}

func (e *FuncExpr) Pos() int32 { return e.Line }
func (*FuncExpr) exprNode()    {}

// ----- Table constructor -----

type TableExpr struct {
	// Line is the `{` token's line. NewTableLine is ls->lastline when PUC's constructor emits
	// NEWTABLE: it does so BEFORE checknext('{'), so the instruction carries the line of the token
	// preceding the brace -- `f<nl>{}` puts NEWTABLE on 1. Zero falls back to Line (#262).
	Line         int32
	NewTableLine int32
	// CloseLine is the `}` line: lastlistfield flushes the final SETLIST there, whatever line the last
	// positional item was pushed on (#262).
	CloseLine int32
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
	// KeyEndLine is ls->lastline when PUC's yindex discharges a bracket key (the key's last token,
	// before `]`); the name's line for a name key. EqLine is the `=` line, where recfield's exp2RK turns
	// a comparison key into its LOADBOOL pair. EndLine is where the item is materialized: for a
	// key-value field, the value's last token (recfield emits SETTABLE right after the value); for a
	// positional item, the separator that follows it (closelistfield runs after testnext consumed it),
	// or -- only when it is the constructor's LAST field -- the closing brace (lastlistfield). A
	// mid-constructor SETLIST flush takes the line of the item that filled the batch; the final one
	// takes TableExpr.CloseLine. Zero means unknown and falls back to the constructor's line (#262).
	KeyEndLine int32
	EqLine     int32
	EndLine    int32
}

func (e *TableExpr) Pos() int32 { return e.Line }
func (*TableExpr) exprNode()    {}

// ----- Statements -----

type Block struct {
	Stmts []Stmt
	// EndLine is ls->lastline after the block's last token, before the closing keyword (`end`, `else`,
	// `elseif`, `until`) is consumed: PUC's leaveblock emits the block's CLOSE there, and a `break`'s
	// CLOSE+JMP carry the `break` token itself. Zero falls back to the enclosing statement's line (#262).
	EndLine int32
}

type LocalStmt struct {
	Line int32
	// ExprEndLines[i] is ls->lastline at the point PUC materializes Exprs[i]: the line of the separator
	// that follows it, or of the statement's last token for the final one. PUC stamps instructions with
	// lastline rather than the statement's own line, so `local v = A<nl>.x` discharges the GETTABLE on
	// line 2, and `local a,b = A.x<nl>, 1` puts both on 2. Using Line put them on 1, which #248 papered
	// over with expDesc.opLine (#252). Nil or short means "unknown", falling back to Line.
	ExprEndLines []int32
	// EndLine is the statement's last token: with no initializer PUC's adjust_assign emits the LOADNIL
	// after the name list, so `local<nl>a` puts it on 2 (#262). Zero falls back to Line.
	EndLine int32
	Names   []string
	Exprs   []Expr
}
type LocalFuncStmt struct {
	Line int32
	Name string
	Fn   *FuncExpr
}
type AssignStmt struct {
	Line int32
	// EndLine is the line of the last token the statement consumed, mirroring PUC's ls->lastline at the point
	// the stores are emitted (#248). Needed because a multi-line RHS moves every store's line, and Pos() on
	// the sub-expressions only gives their START lines -- `A<nl>.x, b = 1, f(<nl>2<nl>)` extends to line 4
	// while max(Pos()) sees only 2. Zero means "unset"; callers fall back to Line.
	EndLine int32
	// ExprEndLines[i] is ls->lastline at the point PUC materializes Exprs[i] -- the RHS elements, which
	// is a different question from EndLine above: that one is where the STORES go. `x, y = A.x<nl>, 1`
	// puts the RHS GETTABLE on 2 (the comma), and `x = A.x<nl>` puts it on 1, since nothing follows
	// (#252). Nil or short means "unknown", falling back to Line.
	ExprEndLines []int32
	Targets      []Expr // each item must be a NameExpr or IndexExpr (parser-validated)
	Exprs        []Expr
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
	// CondEndLine is ls->lastline when PUC's cond() runs luaK_goiftrue: the condition's last token,
	// where the TEST/JMP land (`while not A<nl>.x do` puts TEST on 2). The back-edge JMP and the body's
	// CLOSE are emitted after the body, at Body.EndLine (`while a<nl>do<nl>end` puts the JMP on 2, the
	// `do`). Zero falls back to Cond.Pos() (#262).
	CondEndLine int32
	Cond        Expr
	Body        *Block
}
type RepeatStmt struct {
	Line int32
	Body *Block
	Cond Expr // until can see locals within the Body scope
	// CondEndLine mirrors WhileStmt.CondEndLine (#262).
	CondEndLine int32
}

type IfClause struct {
	Cond Expr
	Body *Block
	// CondEndLine mirrors WhileStmt.CondEndLine. The escape JMP that skips the remaining clauses is
	// emitted after the clause body, at Body.EndLine, so `if a then<nl>f()<nl>else<nl>end` puts it on
	// 2 (#262).
	CondEndLine int32
}
type IfStmt struct {
	Line    int32
	Clauses []IfClause
	Else    *Block // nullable
}

type NumForStmt struct {
	Line int32
	Var  string
	// ExprEndLines[i] is ls->lastline when PUC's exp1 materializes Init/Limit/Step: the line of that
	// expression's own last token. ExprEndLines[2] is set even without a Step, because the default
	// LOADK 1 is emitted right after the limit, at the same lastline (#262).
	ExprEndLines [3]int32
	// DoLine is the line of the `do` keyword: forbody emits FORPREP after checknext(TK_DO), so
	// "'for' initial value must be a number" is reported there -- `for i = "x", 2<nl>do end` says line 2
	// (#262). FORLOOP keeps Line (luaK_fixline "pretend that OP_FOR starts the loop").
	DoLine int32
	Init   Expr
	Limit  Expr
	Step   Expr // nullable → defaults to 1
	Body   *Block
}
type GenForStmt struct {
	Line  int32
	Names []string
	// IterLine is ls->linenumber right after PUC's forlist consumed `in`: the end line of the first
	// token of the iterator list. forbody's luaK_fixline stamps TFORLOOP with it, so a generator that
	// cannot be called is reported there -- `for k in<nl>nil do end` says line 2 (#262). Zero falls
	// back to Line.
	IterLine int32
	// DoLine mirrors NumForStmt.DoLine: forbody emits the forward JMP after checknext(TK_DO); the
	// back-edge JMP is emitted after the block, at Body.EndLine (#262).
	DoLine int32
	// ExprEndLines[i] is ls->lastline at the point PUC materializes Exprs[i]. The last element's line is
	// taken BEFORE `do` is consumed, so `for k in A.x<nl> do end` keeps the GETTABLE on 1 while
	// `for k in A.x<nl>, 1 do end` moves it to 2 (#252).
	ExprEndLines []int32
	Exprs        []Expr // source of the iterator triple
	Body         *Block
}
type FuncStmt struct {
	Line     int32
	Target   Expr // NameExpr / IndexExpr chain
	IsMethod bool // a.b:m → inject an implicit self into Fn.Params
	Fn       *FuncExpr
}
type ReturnStmt struct {
	Line int32
	// ExprEndLines[i] is ls->lastline at the point PUC materializes Exprs[i]: the following comma's line,
	// or -- for the last one -- the line of the statement's last token, since nothing closes a return.
	// `return A.x<nl>, 1` puts the GETTABLE on 2; `return A.x<nl>` puts it on 1 (#252). The RETURN
	// itself is emitted by luaK_ret right after the list, so it takes the last one too (#262).
	ExprEndLines []int32
	Exprs        []Expr
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
