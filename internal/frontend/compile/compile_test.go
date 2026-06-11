// Golden bytecode tests — 04 §10 端到端示例与 02 §8 逐字节一致(自洽承诺验收)。
package compile

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/frontend/lex"
	"github.com/Liam0205/wangshu/internal/frontend/parse"
)

// compileSource 编译一段 Lua 源码,返回主 chunk Proto 与全部子 Proto 注册表。
func compileSource(t *testing.T, src string) (*bytecode.Proto, []*bytecode.Proto) {
	t.Helper()
	lx := lex.New([]byte(src), "test")
	block, err := parse.Parse(lx, "test")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	mainID, protos, err := Compile(block, "test")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return protos[mainID], protos
}

// disasm 反汇编一条指令为可读字符串。
func disasm(ins bytecode.Instruction) string {
	op := bytecode.Op(ins)
	switch bytecode.FormatOf(op) {
	case bytecode.FmtABx:
		return fmt.Sprintf("%s A=%d Bx=%d", op, bytecode.A(ins), bytecode.Bx(ins))
	case bytecode.FmtAsBx:
		return fmt.Sprintf("%s A=%d sBx=%d", op, bytecode.A(ins), bytecode.SBx(ins))
	default:
		return fmt.Sprintf("%s A=%d B=%d C=%d", op, bytecode.A(ins), bytecode.B(ins), bytecode.C(ins))
	}
}

// dumpProto 把一个 Proto 渲染成"OPNAME A=.. B=.. C=.."序列(每行一条),
// 便于黄金字节码用字符串切片做精确比较。
func dumpProto(p *bytecode.Proto) []string {
	out := make([]string, 0, len(p.Code))
	for _, ins := range p.Code {
		out = append(out, disasm(ins))
	}
	return out
}

// TestGolden_SumOfSquares 验证 04 §10 / 02 §8 的求和函数寄存器分配与字节码逐字节一致。
//
// 源:
//
//	local function f(n)
//	  local s = 0
//	  for i=1,n do s = s + i*i end
//	  return s
//	end
func TestGolden_SumOfSquares(t *testing.T) {
	src := `
local function f(n)
  local s = 0
  for i=1,n do s = s + i*i end
  return s
end
`
	_, protos := compileSource(t, src)
	// 子 Proto f 是第一个被编译的(main 在末尾)。
	if len(protos) < 2 {
		t.Fatalf("expected at least 2 protos (f + main), got %d", len(protos))
	}
	f := protos[0]
	if f.NumParams != 1 || f.IsVararg {
		t.Errorf("f: NumParams=%d IsVararg=%v, want 1/false", f.NumParams, f.IsVararg)
	}
	want := []string{
		// LOADK     R1  K0          ; s = 0
		"LOADK A=1 Bx=0",
		// LOADK     R2  K1          ; (for index) = 1
		"LOADK A=2 Bx=1",
		// MOVE      R3  R0          ; (for limit) = n
		"MOVE A=3 B=0 C=0",
		// LOADK     R4  K1          ; (for step) = 1
		"LOADK A=4 Bx=1",
		// FORPREP   R2  -> L1       ; sBx 跳到 FORLOOP
		"FORPREP A=2 sBx=2",
		// MUL   R6  R5  R5
		"MUL A=6 B=5 C=5",
		// ADD   R1  R1  R6
		"ADD A=1 B=1 C=6",
		// FORLOOP R2  -> L0(回边)
		"FORLOOP A=2 sBx=-3",
		// RETURN R1 2(返回 1 个值)
		"RETURN A=1 B=2 C=0",
		// 隐式 RETURN A=0 B=1
		"RETURN A=0 B=1 C=0",
	}
	got := dumpProto(f)
	if len(got) != len(want) {
		t.Fatalf("instruction count mismatch:\nwant %d:\n  %s\ngot %d:\n  %s",
			len(want), strings.Join(want, "\n  "),
			len(got), strings.Join(got, "\n  "))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("instr[%d]: got %q want %q", i, got[i], want[i])
		}
	}
	// 02 §8:MaxStack = 7(R0..R6)
	if f.MaxStack != 7 {
		t.Errorf("MaxStack=%d, want 7", f.MaxStack)
	}
	// 常量池:K0=0.0 K1=1.0
	if len(f.Consts) != 2 {
		t.Fatalf("Consts len=%d, want 2", len(f.Consts))
	}
}

// TestGolden_LocalAndArith 简单算术 + 局部变量 + 单 return。
func TestGolden_LocalAndArith(t *testing.T) {
	src := `
local function add(a, b)
  return a + b
end
`
	_, protos := compileSource(t, src)
	f := protos[0]
	if f.NumParams != 2 {
		t.Errorf("NumParams=%d want 2", f.NumParams)
	}
	got := dumpProto(f)
	want := []string{
		// ADD R2 R0 R1 (relocable A=2 = freereg)
		"ADD A=2 B=0 C=1",
		"RETURN A=2 B=2 C=0",
		"RETURN A=0 B=1 C=0",
	}
	if len(got) != len(want) {
		t.Fatalf("len mismatch: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] got %q want %q", i, got[i], want[i])
		}
	}
}

// TestGolden_IfElse 验证 if/else 的跳转回填。
//
// 比较语义见 02 §4:`if (RK(B)<RK(C)) ≠ bool(A) then pc++`。
// codecomp 默认 cond=1(产 LT A=1 = "真则跳"),goIfTrue 通过 invertJmp 翻 A=0,
// 即"假则跳"——配合紧跟的 JMP 跳过 then 体。
func TestGolden_IfElse(t *testing.T) {
	src := `
local function pick(a, b)
  if a < b then return a end
  return b
end
`
	_, protos := compileSource(t, src)
	f := protos[0]
	got := dumpProto(f)
	want := []string{
		"LT A=0 B=0 C=1",
		"JMP A=0 sBx=1",
		"RETURN A=0 B=2 C=0",
		"RETURN A=1 B=2 C=0",
		"RETURN A=0 B=1 C=0",
	}
	if !equalSlices(got, want) {
		t.Errorf("got:\n  %s\nwant:\n  %s",
			strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// TestGolden_TailCall 验证尾调用识别(04 §9.4):return f(x) → TAILCALL + RETURN(B=0)。
func TestGolden_TailCall(t *testing.T) {
	src := `
local function bounce(g, x)
  return g(x)
end
`
	_, protos := compileSource(t, src)
	f := protos[0]
	got := dumpProto(f)
	// MOVE R2 R0; MOVE R3 R1; TAILCALL R2 B=2 C=0; RETURN R2 B=0; 隐式 RETURN
	want := []string{
		"MOVE A=2 B=0 C=0",
		"MOVE A=3 B=1 C=0",
		"TAILCALL A=2 B=2 C=0",
		"RETURN A=2 B=0 C=0",
		"RETURN A=0 B=1 C=0",
	}
	if !equalSlices(got, want) {
		t.Errorf("got:\n  %s\nwant:\n  %s",
			strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// TestGolden_GlobalGetSet 验证 GETGLOBAL / SETGLOBAL。
//
// 单赋值快路径 storeVar 顺序:先 resolveName(LHS) intern "x" → K0,
// 再 expr(RHS) intern "y" → K1。GETGLOBAL Bx=1 取 y;SETGLOBAL Bx=0 存 x。
func TestGolden_GlobalGetSet(t *testing.T) {
	src := `
x = y
`
	main, _ := compileSource(t, src)
	got := dumpProto(main)
	want := []string{
		"GETGLOBAL A=0 Bx=1",
		"SETGLOBAL A=0 Bx=0",
		"RETURN A=0 B=1 C=0",
	}
	if !equalSlices(got, want) {
		t.Errorf("got:\n  %s\nwant:\n  %s",
			strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	if !main.IsStringConst(0) || !main.IsStringConst(1) {
		t.Errorf("expected K0/K1 to be string literal placeholders")
	}
	if main.StringLits[0] != "x" || main.StringLits[1] != "y" {
		t.Errorf("StringLits=%v, want [x y]", main.StringLits)
	}
}

// TestGolden_TableConstructor 数组 + 哈希混合构造。
func TestGolden_TableConstructor(t *testing.T) {
	src := `
local t = { 10, 20, x = 30 }
`
	main, _ := compileSource(t, src)
	got := dumpProto(main)
	// NEWTABLE R0 (B=int2fb(2), C=int2fb(1));
	// LOADK R1 K0(10); LOADK R2 K1(20); SETLIST R0 B=2 C=1;
	// SETTABLE R0 RK("x") RK(K2(30));
	// RETURN A=0 B=1
	wantPrefix := "NEWTABLE A=0"
	if len(got) < 1 || !strings.HasPrefix(got[0], wantPrefix) {
		t.Errorf("first instr = %q, want prefix %q", got[0], wantPrefix)
	}
	// 找 SETLIST + SETTABLE + RETURN
	if !sliceContains(got, "SETLIST A=0 B=2 C=1") {
		t.Errorf("missing SETLIST: %v", got)
	}
	// SETTABLE 用 RK:键 = K2 ⇒ B=258(256+2)
	if !sliceHasPrefix(got, "SETTABLE A=0") {
		t.Errorf("missing SETTABLE: %v", got)
	}
}

// TestGolden_WhileLoop 验证循环回边 sBx 与条件出口回填。
//
// 单赋值 storeVar 直接把 ADD 落到局部 i 的寄存器(R1),无中间 MOVE。
func TestGolden_WhileLoop(t *testing.T) {
	src := `
local function count(n)
  local i = 0
  while i < n do i = i + 1 end
  return i
end
`
	_, protos := compileSource(t, src)
	f := protos[0]
	got := dumpProto(f)
	want := []string{
		"LOADK A=1 Bx=0",    // i = 0
		"LT A=0 B=1 C=0",    // i < n,A=0 ⇒ 假则跳
		"JMP A=0 sBx=2",     // 跳出循环
		"ADD A=1 B=1 C=257", // i = i + RK(K1=1)
		"JMP A=0 sBx=-4",    // 回边
		"RETURN A=1 B=2 C=0",
		"RETURN A=0 B=1 C=0",
	}
	if !equalSlices(got, want) {
		t.Errorf("got:\n  %s\nwant:\n  %s",
			strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// TestCompile_Errors 验证编译期错误目录(04 §9 主要项)。
func TestCompile_Errors(t *testing.T) {
	cases := []struct {
		src     string
		wantSub string
	}{
		{src: `break`, wantSub: "no loop to break"},
		{src: `function () end`, wantSub: ""}, // 这个是 syntax,parser 报
	}
	for _, c := range cases {
		if c.wantSub == "" {
			continue
		}
		_, _, err := compileFromSrc(c.src)
		if err == nil {
			t.Errorf("src %q: expected error %q, got nil", c.src, c.wantSub)
			continue
		}
		if !strings.Contains(err.Error(), c.wantSub) {
			t.Errorf("src %q: error %q does not contain %q", c.src, err.Error(), c.wantSub)
		}
	}
}

// TestCompile_VarargOutsideFunc 验证 `...` 在非 vararg 函数体内被禁用。
func TestCompile_VarargOutsideFunc(t *testing.T) {
	src := `
local function f()
  return ...
end
`
	_, _, err := compileFromSrc(src)
	if err == nil {
		t.Fatalf("expected error for `...` outside vararg function")
	}
	if !strings.Contains(err.Error(), "outside a vararg function") {
		t.Errorf("error msg = %q, expected substring 'outside a vararg function'", err.Error())
	}
}

// TestCompile_Closure 验证 CLOSURE + 后随伪指令(MOVE 表示捕获外层局部)。
func TestCompile_Closure(t *testing.T) {
	src := `
local function outer()
  local x = 1
  local function inner() return x end
  return inner
end
`
	_, protos := compileSource(t, src)
	// inner 是先编译的 Proto(深度优先)
	inner := protos[0]
	if len(inner.UpvalDescs) != 1 {
		t.Fatalf("inner UpvalDescs len=%d want 1: %v", len(inner.UpvalDescs), inner.UpvalDescs)
	}
	if !inner.UpvalDescs[0].InStack || inner.UpvalDescs[0].Idx != 0 {
		t.Errorf("inner upval = %+v, want InStack=true Idx=0 (capturing outer's x at R0)", inner.UpvalDescs[0])
	}
	// outer 的 CLOSURE 后应有一条 MOVE 伪指令 (B=0 表示捕获 R0)
	outer := protos[1]
	got := dumpProto(outer)
	// LOADK R0 K0(1); CLOSURE R1 Bx=0; MOVE 0 0 0(伪指令); MOVE R2 R1; RETURN ...
	// 找 CLOSURE 后第一条
	for i, ins := range outer.Code {
		if bytecode.Op(ins) == bytecode.CLOSURE {
			if i+1 >= len(outer.Code) {
				t.Fatalf("CLOSURE at end without pseudo-instr")
			}
			next := outer.Code[i+1]
			if bytecode.Op(next) != bytecode.MOVE {
				t.Errorf("expected MOVE pseudo-instr after CLOSURE, got %s", bytecode.Op(next))
			}
			if bytecode.B(next) != 0 {
				t.Errorf("pseudo-instr B=%d, want 0 (captures outer R0)", bytecode.B(next))
			}
			return
		}
	}
	t.Fatalf("CLOSURE not found in outer: %v", got)
}

func compileFromSrc(src string) (uint32, []*bytecode.Proto, error) {
	lx := lex.New([]byte(src), "test")
	block, err := parse.Parse(lx, "test")
	if err != nil {
		return 0, nil, err
	}
	return Compile(block, "test")
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sliceContains(s []string, want string) bool {
	for _, x := range s {
		if x == want {
			return true
		}
	}
	return false
}

func sliceHasPrefix(s []string, prefix string) bool {
	for _, x := range s {
		if strings.HasPrefix(x, prefix) {
			return true
		}
	}
	return false
}
