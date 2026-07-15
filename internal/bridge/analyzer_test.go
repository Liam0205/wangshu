// Compilability analyzer tests (`docs/design/p2-bridge/03-compilability-analysis.md` §3 acceptance).
//
// Each of the F1-F7 shapes has a corresponding set of test scripts asserting
// that AnalyzeProto returns CompNotCompilable and that reasonsBitmap holds the
// matching bit.
//
// Also verifies the "conservative first" rule: zero false negatives on
// non-compilable shapes (no shape ever slips from CompNotCompilable back to
// CompCompilable).
package bridge

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/frontend/ast"
)

// makeFuncBody builds a FuncExpr (body = a single ReturnStmt with no
// expressions) for F1-style tests that only inspect metadata such as
// IsVararg/UpvalDescs.
func makeFuncBody(stmts ...ast.Stmt) *ast.FuncExpr {
	return &ast.FuncExpr{
		Body: &ast.Block{Stmts: stmts},
	}
}

// makeProto builds a Proto that passes the protoIsVararg / Code / MaxStack checks.
func makeProto(opts ...func(*bytecode.Proto)) *bytecode.Proto {
	p := &bytecode.Proto{
		Code: []bytecode.Instruction{},
		IC:   []bytecode.ICSlot{},
	}
	for _, o := range opts {
		o(p)
	}
	if len(p.IC) != len(p.Code) {
		p.IC = make([]bytecode.ICSlot, len(p.Code))
	}
	return p
}

// helpers for makeProto opts
func withVararg(b bool) func(*bytecode.Proto) { return func(p *bytecode.Proto) { p.IsVararg = b } }
func withCodeLen(n int) func(*bytecode.Proto) {
	return func(p *bytecode.Proto) { p.Code = make([]bytecode.Instruction, n) }
}
func withMaxStack(n uint8) func(*bytecode.Proto) { return func(p *bytecode.Proto) { p.MaxStack = n } }
func withUpvalCount(n int) func(*bytecode.Proto) {
	return func(p *bytecode.Proto) {
		p.UpvalDescs = make([]bytecode.UpvalDesc, n)
	}
}

// TestAnalyze_F1_Vararg `function(...)`: triple detection via AST + Proto +
// opcode; any one hit marks it NotCompilable.
func TestAnalyze_F1_Vararg(t *testing.T) {
	b := NewBridge()
	fn := &ast.FuncExpr{
		Body:     &ast.Block{Stmts: []ast.Stmt{}},
		IsVararg: true,
	}
	p := makeProto(withVararg(true))
	got := b.AnalyzeProto(fn, p)
	if got != CompNotCompilable {
		t.Errorf("vararg func should be NotCompilable, got %v", got)
	}
	pd := b.ProfileOf(p)
	if pd.Reasons&ReasonVararg == 0 {
		t.Errorf("ReasonVararg bit should be set, got reasons=%016b", pd.Reasons)
	}
}

// TestAnalyze_F2_Yield `coroutine.yield(...)` direct call → ReasonYield + ReasonCoroutine.
func TestAnalyze_F2_Yield(t *testing.T) {
	b := NewBridge()
	// Lua equivalent: `function() coroutine.yield(1) end`
	yieldCall := &ast.CallStmt{
		Call: &ast.CallExpr{
			Fn: &ast.IndexExpr{
				Obj: &ast.NameExpr{Name: "coroutine"},
				Key: &ast.StringExpr{Val: "yield"},
			},
			Args: []ast.Expr{&ast.NumberExpr{Val: 1}},
		},
	}
	fn := makeFuncBody(yieldCall)
	p := makeProto()

	got := b.AnalyzeProto(fn, p)
	if got != CompNotCompilable {
		t.Errorf("yield call should be NotCompilable, got %v", got)
	}
	pd := b.ProfileOf(p)
	if pd.Reasons&ReasonYield == 0 {
		t.Errorf("ReasonYield bit should be set")
	}
	if pd.Reasons&ReasonCoroutine == 0 {
		t.Errorf("ReasonCoroutine bit should be set (any coroutine.* call)")
	}
}

// TestAnalyze_F2_UnknownCall unknown function call (not a known local) → ReasonUnknownCall.
func TestAnalyze_F2_UnknownCall(t *testing.T) {
	b := NewBridge()
	// `function() print("hi") end` — print is a global, not a known local
	fn := makeFuncBody(&ast.CallStmt{
		Call: &ast.CallExpr{
			Fn:   &ast.NameExpr{Name: "print"},
			Args: []ast.Expr{&ast.StringExpr{Val: "hi"}},
		},
	})
	p := makeProto()

	got := b.AnalyzeProto(fn, p)
	if got != CompNotCompilable {
		t.Errorf("global function call should be NotCompilable (unknown), got %v", got)
	}
	pd := b.ProfileOf(p)
	if pd.Reasons&ReasonUnknownCall == 0 {
		t.Errorf("ReasonUnknownCall bit should be set, got %016b", pd.Reasons)
	}
}

// TestAnalyze_F2c_SelfCall `obj:m(...)` → ReasonSelfCall (occupy bit), and does
// NOT also set ReasonUnknownCall (per the P4 PJ5 SELF-inline decision to truly
// wire it up). After the P4 runtime injection, `recheckCompilabilityRuntime`
// clears the bit and SupportsAllOpcodes gates it.
func TestAnalyze_F2c_SelfCall(t *testing.T) {
	b := NewBridge()
	// `function(o) o:m() end`
	methodCall := &ast.CallStmt{
		Call: &ast.MethodCallExpr{
			Recv:   &ast.NameExpr{Name: "o"},
			Method: "m",
			Args:   nil,
		},
	}
	fn := makeFuncBody(methodCall)
	p := makeProto()

	got := b.AnalyzeProto(fn, p)
	if got != CompNotCompilable {
		t.Errorf("method call should default NotCompilable (occupy reason), got %v", got)
	}
	pd := b.ProfileOf(p)
	if pd.Reasons&ReasonSelfCall == 0 {
		t.Errorf("ReasonSelfCall bit should be set, got %016b", pd.Reasons)
	}
	if pd.Reasons&ReasonUnknownCall != 0 {
		t.Errorf("ReasonUnknownCall must NOT be set for SELF method call (occupy reason is independent), got %016b", pd.Reasons)
	}
}

// TestAnalyze_F2_KnownLocalCall_Pure a known local function call where the local
// itself is pure computation → should not trigger unknown (user decision:
// isKnownLocalCall is genuinely implemented).
func TestAnalyze_F2_KnownLocalCall_Pure(t *testing.T) {
	b := NewBridge()
	// `function() local function helper(x) return x*x end; local s = helper(5) end`
	helper := &ast.FuncExpr{
		Body: &ast.Block{Stmts: []ast.Stmt{
			&ast.ReturnStmt{
				Exprs: []ast.Expr{
					&ast.BinExpr{
						Op: ast.OpMul,
						L:  &ast.NameExpr{Name: "x"},
						R:  &ast.NameExpr{Name: "x"},
					},
				},
			},
		}},
		Params: []string{"x"},
	}
	fn := makeFuncBody(
		&ast.LocalFuncStmt{Name: "helper", Fn: helper},
		&ast.LocalStmt{
			Names: []string{"s"},
			Exprs: []ast.Expr{
				&ast.CallExpr{
					Fn:   &ast.NameExpr{Name: "helper"},
					Args: []ast.Expr{&ast.NumberExpr{Val: 5}},
				},
			},
		},
	)
	// With no P3, b.p3 == nil → always judged non-compilable (F7 triggers). We
	// want to test that F2 does not trigger, so inject a mock P3 that keeps F7
	// from triggering.
	b.SetP3Compiler(allowAllOpcodes{})
	p := makeProto()

	got := b.AnalyzeProto(fn, p)
	if got != CompCompilable {
		pd := b.ProfileOf(p)
		t.Errorf("known local pure call should be Compilable, got %v reasons=%016b",
			got, pd.Reasons)
	}
}

// TestAnalyze_F2_KnownLocalCall_Yield known local but the callee yields → parent also judged F2.
//
// This embodies "conservative first": when a parent calls a child and the child
// actually yields, the parent is judged non-compilable too — step 5 of
// visitCallExpr ("recursively walk the child function body") propagates the
// child's callsYield signal up to the parent visitor.
func TestAnalyze_F2_KnownLocalCall_Yield(t *testing.T) {
	b := NewBridge()
	// helper contains yield
	helper := &ast.FuncExpr{
		Body: &ast.Block{Stmts: []ast.Stmt{
			&ast.CallStmt{
				Call: &ast.CallExpr{
					Fn: &ast.IndexExpr{
						Obj: &ast.NameExpr{Name: "coroutine"},
						Key: &ast.StringExpr{Val: "yield"},
					},
				},
			},
		}},
	}
	fn := makeFuncBody(
		&ast.LocalFuncStmt{Name: "helper", Fn: helper},
		&ast.CallStmt{
			Call: &ast.CallExpr{
				Fn: &ast.NameExpr{Name: "helper"},
			},
		},
	)
	p := makeProto()

	got := b.AnalyzeProto(fn, p)
	if got != CompNotCompilable {
		t.Errorf("parent calling yielding helper should be NotCompilable, got %v", got)
	}
	pd := b.ProfileOf(p)
	if pd.Reasons&(ReasonYield|ReasonCoroutine) == 0 {
		t.Errorf("yield/coroutine reason should be set, got %016b", pd.Reasons)
	}
}

// TestAnalyze_F3_Debug `debug.traceback()` → ReasonDebug.
func TestAnalyze_F3_Debug(t *testing.T) {
	b := NewBridge()
	fn := makeFuncBody(&ast.CallStmt{
		Call: &ast.CallExpr{
			Fn: &ast.IndexExpr{
				Obj: &ast.NameExpr{Name: "debug"},
				Key: &ast.StringExpr{Val: "traceback"},
			},
		},
	})
	p := makeProto()

	got := b.AnalyzeProto(fn, p)
	if got != CompNotCompilable {
		t.Errorf("debug.* call should be NotCompilable, got %v", got)
	}
	pd := b.ProfileOf(p)
	if pd.Reasons&ReasonDebug == 0 {
		t.Errorf("ReasonDebug bit should be set")
	}
}

// TestAnalyze_F4_Setfenv `setfenv(1, env)` → ReasonSetfenv.
func TestAnalyze_F4_Setfenv(t *testing.T) {
	b := NewBridge()
	fn := makeFuncBody(&ast.CallStmt{
		Call: &ast.CallExpr{
			Fn: &ast.NameExpr{Name: "setfenv"},
		},
	})
	p := makeProto()

	got := b.AnalyzeProto(fn, p)
	if got != CompNotCompilable {
		t.Errorf("setfenv call should be NotCompilable, got %v", got)
	}
	pd := b.ProfileOf(p)
	if pd.Reasons&ReasonSetfenv == 0 {
		t.Errorf("ReasonSetfenv bit should be set")
	}
}

// TestAnalyze_F5_OverSize Code length > MaxCompilableInsns → ReasonOverSize.
func TestAnalyze_F5_OverSize(t *testing.T) {
	b := NewBridge()
	fn := makeFuncBody()
	p := makeProto(withCodeLen(MaxCompilableInsns + 1))
	got := b.AnalyzeProto(fn, p)
	if got != CompNotCompilable {
		t.Errorf("oversized func should be NotCompilable, got %v", got)
	}
	pd := b.ProfileOf(p)
	if pd.Reasons&ReasonOverSize == 0 {
		t.Errorf("ReasonOverSize bit should be set")
	}
}

// TestAnalyze_F5_OverRegs MaxStack > MaxCompilableRegs → ReasonOverRegs.
func TestAnalyze_F5_OverRegs(t *testing.T) {
	b := NewBridge()
	fn := makeFuncBody()
	p := makeProto(withMaxStack(MaxCompilableRegs + 1))
	got := b.AnalyzeProto(fn, p)
	if got != CompNotCompilable {
		t.Errorf("over-regs func should be NotCompilable, got %v", got)
	}
	pd := b.ProfileOf(p)
	if pd.Reasons&ReasonOverRegs == 0 {
		t.Errorf("ReasonOverRegs bit should be set")
	}
}

// TestAnalyze_F6_NestedDeep nesting depth exceeds MaxClosureDepth (=3).
func TestAnalyze_F6_NestedDeep(t *testing.T) {
	b := NewBridge()
	// 4 levels of nested function: user-written nested D4 inside D3, D3 inside D2...
	d4 := &ast.FuncExpr{Body: &ast.Block{}}
	d3 := &ast.FuncExpr{Body: &ast.Block{Stmts: []ast.Stmt{
		&ast.LocalFuncStmt{Name: "d4", Fn: d4},
	}}}
	d2 := &ast.FuncExpr{Body: &ast.Block{Stmts: []ast.Stmt{
		&ast.LocalFuncStmt{Name: "d3", Fn: d3},
	}}}
	d1 := &ast.FuncExpr{Body: &ast.Block{Stmts: []ast.Stmt{
		&ast.LocalFuncStmt{Name: "d2", Fn: d2},
	}}}
	fn := &ast.FuncExpr{Body: &ast.Block{Stmts: []ast.Stmt{
		&ast.LocalFuncStmt{Name: "d1", Fn: d1},
	}}}
	p := makeProto()
	got := b.AnalyzeProto(fn, p)
	if got != CompNotCompilable {
		t.Errorf("4-deep nested func should be NotCompilable, got %v", got)
	}
	pd := b.ProfileOf(p)
	if pd.Reasons&ReasonNestedDeep == 0 {
		t.Errorf("ReasonNestedDeep bit should be set")
	}
}

// TestAnalyze_F6_OverUpval upvalue count > MaxUpvalCount (=8).
func TestAnalyze_F6_OverUpval(t *testing.T) {
	b := NewBridge()
	fn := makeFuncBody()
	p := makeProto(withUpvalCount(MaxUpvalCount + 1))
	got := b.AnalyzeProto(fn, p)
	if got != CompNotCompilable {
		t.Errorf("over-upval func should be NotCompilable, got %v", got)
	}
	pd := b.ProfileOf(p)
	if pd.Reasons&ReasonOverUpval == 0 {
		t.Errorf("ReasonOverUpval bit should be set")
	}
}

// TestAnalyze_F7_NoP3 P1-only build / P3 not injected → F7 triggers.
func TestAnalyze_F7_NoP3(t *testing.T) {
	b := NewBridge()
	// simple pure-computation function; F1-F6 all pass
	fn := makeFuncBody(&ast.ReturnStmt{
		Exprs: []ast.Expr{&ast.NumberExpr{Val: 42}},
	})
	p := makeProto()
	got := b.AnalyzeProto(fn, p)
	if got != CompNotCompilable {
		t.Errorf("no P3 should fall to F7 NotCompilable, got %v", got)
	}
	pd := b.ProfileOf(p)
	if pd.Reasons != ReasonBackendUnsupp {
		t.Errorf("only ReasonBackendUnsupp should be set, got %016b", pd.Reasons)
	}
}

// TestAnalyze_F1toF7_AllPass none of F1-F7 trigger → CompCompilable.
//
// Inject the allowAllOpcodes mock so F7 passes; the valid reasons are zero.
func TestAnalyze_F1toF7_AllPass(t *testing.T) {
	b := NewBridge()
	b.SetP3Compiler(allowAllOpcodes{})
	// `function(x) return x + 1 end` — a pure-computation helper
	fn := &ast.FuncExpr{
		Params: []string{"x"},
		Body: &ast.Block{Stmts: []ast.Stmt{
			&ast.ReturnStmt{
				Exprs: []ast.Expr{
					&ast.BinExpr{
						Op: ast.OpAdd,
						L:  &ast.NameExpr{Name: "x"},
						R:  &ast.NumberExpr{Val: 1},
					},
				},
			},
		}},
	}
	p := makeProto()
	got := b.AnalyzeProto(fn, p)
	if got != CompCompilable {
		pd := b.ProfileOf(p)
		t.Errorf("all-pass func should be Compilable, got %v reasons=%016b",
			got, pd.Reasons)
	}
}

// TestAnalyze_VarargMismatchPanic an AST.IsVararg / Proto.IsVararg mismatch is a
// codegen bug; AnalyzeProto should panic (03 §2.3 invariant 1).
func TestAnalyze_VarargMismatchPanic(t *testing.T) {
	b := NewBridge()
	fn := &ast.FuncExpr{Body: &ast.Block{}, IsVararg: true}
	p := makeProto(withVararg(false)) // mismatch

	defer func() {
		if r := recover(); r == nil {
			t.Errorf("expected panic on AST/Proto IsVararg mismatch")
		}
	}()
	b.AnalyzeProto(fn, p)
}

// TestAnalyze_F2_StdlibSafeCalls P2 follow-up optimization round #1: once the
// stdlib whitelist is in effect, calls to type / tostring / math.sqrt /
// string.format / table.insert etc. are no longer flagged unknown, so a function
// doing pure computation + stdlib calls should be judged Compilable.
func TestAnalyze_F2_StdlibSafeCalls(t *testing.T) {
	cases := []struct {
		name string
		call *ast.CallExpr
	}{
		{
			"type-global",
			&ast.CallExpr{
				Fn:   &ast.NameExpr{Name: "type"},
				Args: []ast.Expr{&ast.NameExpr{Name: "x"}},
			},
		},
		{
			"tostring-global",
			&ast.CallExpr{
				Fn:   &ast.NameExpr{Name: "tostring"},
				Args: []ast.Expr{&ast.NumberExpr{Val: 1}},
			},
		},
		{
			"math-sqrt",
			&ast.CallExpr{
				Fn: &ast.IndexExpr{
					Obj: &ast.NameExpr{Name: "math"},
					Key: &ast.StringExpr{Val: "sqrt"},
				},
				Args: []ast.Expr{&ast.NumberExpr{Val: 16}},
			},
		},
		{
			"string-format",
			&ast.CallExpr{
				Fn: &ast.IndexExpr{
					Obj: &ast.NameExpr{Name: "string"},
					Key: &ast.StringExpr{Val: "format"},
				},
				Args: []ast.Expr{&ast.StringExpr{Val: "%d"}, &ast.NumberExpr{Val: 1}},
			},
		},
		{
			"table-insert",
			&ast.CallExpr{
				Fn: &ast.IndexExpr{
					Obj: &ast.NameExpr{Name: "table"},
					Key: &ast.StringExpr{Val: "insert"},
				},
				Args: []ast.Expr{&ast.NameExpr{Name: "t"}, &ast.NumberExpr{Val: 1}},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := NewBridge()
			b.SetP3Compiler(allowAllOpcodes{})
			fn := makeFuncBody(&ast.CallStmt{Call: c.call})
			p := makeProto()
			got := b.AnalyzeProto(fn, p)
			if got != CompCompilable {
				pd := b.ProfileOf(p)
				t.Errorf("safe stdlib %q should be Compilable, got %v reasons=%016b",
					c.name, got, pd.Reasons)
			}
		})
	}
}

// TestAnalyze_F2_StdlibUnsafeCalls stdlib functions not on the whitelist
// (string.gsub / table.foreach / pcall / pairs / print / error) are still judged
// unknown (F2 triggers).
func TestAnalyze_F2_StdlibUnsafeCalls(t *testing.T) {
	cases := []*ast.CallExpr{
		// string.gsub's third arg may be a fn ⇒ unsafe
		{
			Fn: &ast.IndexExpr{
				Obj: &ast.NameExpr{Name: "string"},
				Key: &ast.StringExpr{Val: "gsub"},
			},
		},
		// table.foreach takes a fn ⇒ unsafe
		{
			Fn: &ast.IndexExpr{
				Obj: &ast.NameExpr{Name: "table"},
				Key: &ast.StringExpr{Val: "foreach"},
			},
		},
		// os.execute is an IO boundary ⇒ unsafe
		{
			Fn: &ast.IndexExpr{
				Obj: &ast.NameExpr{Name: "os"},
				Key: &ast.StringExpr{Val: "execute"},
			},
		},
		// pcall not on the whitelist (runs arbitrary Lua, may contain yield/coroutine)
		{
			Fn: &ast.NameExpr{Name: "pcall"},
		},
		// pairs couples with the iterator protocol ⇒ not on the whitelist
		{
			Fn: &ast.NameExpr{Name: "pairs"},
		},
		// print is an IO boundary ⇒ not on the whitelist
		{
			Fn: &ast.NameExpr{Name: "print"},
		},
		// error triggers longjmp ⇒ not on the whitelist
		{
			Fn: &ast.NameExpr{Name: "error"},
		},
	}
	for _, c := range cases {
		t.Run(funcExprName(c.Fn), func(t *testing.T) {
			b := NewBridge()
			b.SetP3Compiler(allowAllOpcodes{})
			fn := makeFuncBody(&ast.CallStmt{Call: c})
			p := makeProto()
			got := b.AnalyzeProto(fn, p)
			if got != CompNotCompilable {
				t.Errorf("unsafe stdlib should be NotCompilable, got %v", got)
			}
			pd := b.ProfileOf(p)
			if pd.Reasons&ReasonUnknownCall == 0 {
				t.Errorf("ReasonUnknownCall bit should be set, got %016b", pd.Reasons)
			}
		})
	}
}

// funcExprName provides a name for the unsafe-stdlib tests (for readable failures).
func funcExprName(fn ast.Expr) string {
	if name, ok := fn.(*ast.NameExpr); ok {
		return name.Name
	}
	if idx, ok := fn.(*ast.IndexExpr); ok {
		if obj, ok := idx.Obj.(*ast.NameExpr); ok {
			if key, ok := idx.Key.(*ast.StringExpr); ok {
				return obj.Name + "." + key.Val
			}
		}
	}
	return "unknown"
}

// TestAnalyze_F2_RecursiveClosureLiteral_NoStackOverflow a closure literal inside
// a recursive local function body must not let known-local expansion recurse into
// each other forever (fuzz seed 648e96a2d9661b88:
// `local function A() return function() A() end end`).
//
// Crash chain: visitCallExpr expands A (inlinedKnownCalls marks A) → the FuncExpr
// inside A's body goes through walkFuncExpr and builds a **fresh sub-visitor**
// (the old version's guard table was not inherited) → the sub hits an A() call
// again, its guard is empty → expands A again → builds another sub → ... until
// the Go stack overflows (fatal, not recoverable). Fix: walkFuncExpr copies the
// ancestor guard table into the sub. Before the fix this test crashes the process
// with a stack overflow rather than failing an assert.
func TestAnalyze_F2_RecursiveClosureLiteral_NoStackOverflow(t *testing.T) {
	b := NewBridge()
	b.SetP3Compiler(allowAllOpcodes{})

	// `local function A() return function() A() end end`
	inner := &ast.FuncExpr{
		Body: &ast.Block{Stmts: []ast.Stmt{
			&ast.CallStmt{Call: &ast.CallExpr{Fn: &ast.NameExpr{Name: "A"}}},
		}},
	}
	outer := &ast.FuncExpr{
		Body: &ast.Block{Stmts: []ast.Stmt{
			&ast.ReturnStmt{Exprs: []ast.Expr{inner}},
		}},
	}
	fn := makeFuncBody(&ast.LocalFuncStmt{Name: "A", Fn: outer})
	p := makeProto()

	// As long as it returns (no stack overflow) the fix is working; the verdict
	// itself is either-way (after A expands the signals are clean → Compilable),
	// so we don't pin down the specific judgment.
	_ = b.AnalyzeProto(fn, p)
}

// allowAllOpcodes is a mock P3 compiler whose SupportsAllOpcodes always returns
// true. Used to test that the F1-F6 judgments are not affected by the F7 fallback.
type allowAllOpcodes struct{}

func (allowAllOpcodes) SupportsAllOpcodes(_ *bytecode.Proto) bool { return true }
func (allowAllOpcodes) Compile(_ *bytecode.Proto, _ *TypeFeedback) (GibbousCode, error) {
	return nil, nil
}
