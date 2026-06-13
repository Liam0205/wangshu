// Compilability analyzer 测试(`docs/design/p2-bridge/03-compilability-analysis.md` §3 验收)。
//
// F1-F7 各形状的对应一组测试脚本,断言 AnalyzeProto 判 CompNotCompilable
// 且 reasonsBitmap 含对应位。
//
// 同时验证「保守第一」铁律:不可编译形状的零误判(没有任何形状会从
// CompNotCompilable 滑回 CompCompilable)。
package bridge

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/frontend/ast"
)

// makeFuncBody 构造一个 FuncExpr(body=单 ReturnStmt 不含表达式)用于 F1
// 等只看 IsVararg/UpvalDescs 等元数据的测试。
func makeFuncBody(stmts ...ast.Stmt) *ast.FuncExpr {
	return &ast.FuncExpr{
		Body: &ast.Block{Stmts: stmts},
	}
}

// makeProto 造一个能让 protoIsVararg / Code/MaxStack 检查走通的 Proto。
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

// TestAnalyze_F1_Vararg `function(...)`:三重识别 AST + Proto + opcode 任一
// 触发即判 NotCompilable。
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

// TestAnalyze_F2_Yield `coroutine.yield(...)` 直接调 → ReasonYield + ReasonCoroutine。
func TestAnalyze_F2_Yield(t *testing.T) {
	b := NewBridge()
	// Lua 等价:`function() coroutine.yield(1) end`
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

// TestAnalyze_F2_UnknownCall 未知函数调用(非 local known)→ ReasonUnknownCall。
func TestAnalyze_F2_UnknownCall(t *testing.T) {
	b := NewBridge()
	// `function() print("hi") end` —— print 是全局,不是 known local
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

// TestAnalyze_F2_KnownLocalCall_Pure 已知 local 函数调用 + 该 local 自身纯
// 计算 → 不应触发 unknown(用户拍板:isKnownLocalCall 真实现)。
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
	// F7 缺省 b.p3 == nil → 始终判不可编译(F7 触发)。我们要测 F2 不触发,
	// 所以注入一个 mock P3 使 F7 不触发。
	b.SetP3Compiler(allowAllOpcodes{})
	p := makeProto()

	got := b.AnalyzeProto(fn, p)
	if got != CompCompilable {
		pd := b.ProfileOf(p)
		t.Errorf("known local pure call should be Compilable, got %v reasons=%016b",
			got, pd.Reasons)
	}
}

// TestAnalyze_F2_KnownLocalCall_Yield 已知 local 但子函数 yield → 父也判 F2。
//
// 这是「保守第一」的体现:父调用子,子真 yield 时父也判不可编译——visitCallExpr
// 第 5 步「递归 walk 子函数体」把子的 callsYield 信号传染到父 visitor。
func TestAnalyze_F2_KnownLocalCall_Yield(t *testing.T) {
	b := NewBridge()
	// helper 含 yield
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

// TestAnalyze_F3_Debug `debug.traceback()` → ReasonDebug。
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

// TestAnalyze_F4_Setfenv `setfenv(1, env)` → ReasonSetfenv。
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

// TestAnalyze_F5_OverSize Code 长度 > MaxCompilableInsns → ReasonOverSize。
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

// TestAnalyze_F5_OverRegs MaxStack > MaxCompilableRegs → ReasonOverRegs。
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

// TestAnalyze_F6_NestedDeep 嵌套深度超过 MaxClosureDepth(=3)。
func TestAnalyze_F6_NestedDeep(t *testing.T) {
	b := NewBridge()
	// 嵌套 4 层 function:用户编写嵌套函数 D4 在 D3 内,D3 在 D2 内...
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

// TestAnalyze_F6_OverUpval upvalue 数 > MaxUpvalCount(=8)。
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

// TestAnalyze_F7_NoP3 P1-only build / P3 未注入 → F7 触发。
func TestAnalyze_F7_NoP3(t *testing.T) {
	b := NewBridge()
	// 简单纯计算函数,F1-F6 全过
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

// TestAnalyze_F1toF7_AllPass 全部 F1-F7 都不触发 → CompCompilable。
//
// 注入 allowAllOpcodes mock 让 F7 通过,合法 reasons 为零。
func TestAnalyze_F1toF7_AllPass(t *testing.T) {
	b := NewBridge()
	b.SetP3Compiler(allowAllOpcodes{})
	// `function(x) return x + 1 end`——纯计算 helper
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

// TestAnalyze_VarargMismatchPanic AST.IsVararg 与 Proto.IsVararg 不一致即
// codegen bug,AnalyzeProto 应 panic(03 §2.3 不变式 1)。
func TestAnalyze_VarargMismatchPanic(t *testing.T) {
	b := NewBridge()
	fn := &ast.FuncExpr{Body: &ast.Block{}, IsVararg: true}
	p := makeProto(withVararg(false)) // 不一致

	defer func() {
		if r := recover(); r == nil {
			t.Errorf("expected panic on AST/Proto IsVararg mismatch")
		}
	}()
	b.AnalyzeProto(fn, p)
}

// allowAllOpcodes 是 mock P3 编译器,SupportsAllOpcodes 永远返 true。
// 用于测试 F1-F6 的判定不被 F7 兜底影响。
type allowAllOpcodes struct{}

func (allowAllOpcodes) SupportsAllOpcodes(_ *bytecode.Proto) bool { return true }
func (allowAllOpcodes) Compile(_ *bytecode.Proto, _ *TypeFeedback) (GibbousCode, error) {
	return nil, nil
}
