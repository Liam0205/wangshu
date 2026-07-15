// Golden bytecode tests — 04 §10 end-to-end examples, byte-for-byte matching
// 02 §8 (self-consistency promise acceptance).
package compile

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/frontend/lex"
	"github.com/Liam0205/wangshu/internal/frontend/parse"
)

// compileSource compiles a snippet of Lua source, returning the main chunk
// Proto and the registry of all child Protos.
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

// disasm disassembles a single instruction into a readable string.
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

// dumpProto renders a Proto into an "OPNAME A=.. B=.. C=.." sequence (one per
// line), making it easy for golden bytecode tests to compare precisely via
// string slices.
func dumpProto(p *bytecode.Proto) []string {
	out := make([]string, 0, len(p.Code))
	for _, ins := range p.Code {
		out = append(out, disasm(ins))
	}
	return out
}

// TestGolden_SumOfSquares verifies that the summation function's register
// allocation and bytecode from 04 §10 / 02 §8 match byte for byte.
//
// Source:
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
	// The child Proto f is compiled first (main comes last).
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
		// FORPREP   R2  -> L1       ; sBx jumps to FORLOOP
		"FORPREP A=2 sBx=2",
		// MUL   R6  R5  R5
		"MUL A=6 B=5 C=5",
		// ADD   R1  R1  R6
		"ADD A=1 B=1 C=6",
		// FORLOOP R2  -> L0 (back edge)
		"FORLOOP A=2 sBx=-3",
		// RETURN R1 2 (returns 1 value)
		"RETURN A=1 B=2 C=0",
		// implicit RETURN A=0 B=1
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
	// 02 §8: MaxStack = 7 (R0..R6)
	if f.MaxStack != 7 {
		t.Errorf("MaxStack=%d, want 7", f.MaxStack)
	}
	// Constant pool: K0=0.0 K1=1.0
	if len(f.Consts) != 2 {
		t.Fatalf("Consts len=%d, want 2", len(f.Consts))
	}
}

// TestGolden_LocalAndArith: simple arithmetic + locals + single return.
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

// TestGolden_IfElse verifies jump backpatching for if/else.
//
// For comparison semantics see 02 §4: `if (RK(B)<RK(C)) ≠ bool(A) then pc++`.
// codecomp defaults to cond=1 (emitting LT A=1 = "jump if true"); goIfTrue
// flips A=0 via invertJmp, i.e. "jump if false" — which, together with the
// immediately following JMP, skips the then body.
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

// TestGolden_TailCall verifies tail-call recognition (04 §9.4): return f(x) →
// TAILCALL + RETURN(B=0).
func TestGolden_TailCall(t *testing.T) {
	src := `
local function bounce(g, x)
  return g(x)
end
`
	_, protos := compileSource(t, src)
	f := protos[0]
	got := dumpProto(f)
	// MOVE R2 R0; MOVE R3 R1; TAILCALL R2 B=2 C=0; RETURN R2 B=0; implicit RETURN
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

// TestGolden_GlobalGetSet verifies GETGLOBAL / SETGLOBAL.
//
// Single-assignment fast-path storeVar order: first resolveName(LHS) interns
// "x" → K0, then expr(RHS) interns "y" → K1. GETGLOBAL Bx=1 fetches y;
// SETGLOBAL Bx=0 stores x.
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

// TestGolden_TableConstructor: mixed array + hash construction.
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
	// Find SETLIST + SETTABLE + RETURN
	if !sliceContains(got, "SETLIST A=0 B=2 C=1") {
		t.Errorf("missing SETLIST: %v", got)
	}
	// SETTABLE uses RK: key = K2 ⇒ B=258 (256+2)
	if !sliceHasPrefix(got, "SETTABLE A=0") {
		t.Errorf("missing SETTABLE: %v", got)
	}
}

// TestGolden_WhileLoop verifies the loop back-edge sBx and the conditional-exit
// backpatch.
//
// Single-assignment storeVar writes the ADD directly into the local i's
// register (R1), with no intermediate MOVE.
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
		"LT A=0 B=1 C=0",    // i < n, A=0 ⇒ jump if false
		"JMP A=0 sBx=2",     // jump out of loop
		"ADD A=1 B=1 C=257", // i = i + RK(K1=1)
		"JMP A=0 sBx=-4",    // back edge
		"RETURN A=1 B=2 C=0",
		"RETURN A=0 B=1 C=0",
	}
	if !equalSlices(got, want) {
		t.Errorf("got:\n  %s\nwant:\n  %s",
			strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// TestCompile_Errors verifies the compile-time error catalog (04 §9 main items).
func TestCompile_Errors(t *testing.T) {
	cases := []struct {
		src     string
		wantSub string
	}{
		{src: `break`, wantSub: "no loop to break"},
		{src: `function () end`, wantSub: ""}, // this is a syntax error, reported by the parser
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

// TestCompile_VarargOutsideFunc verifies that `...` is disallowed inside a
// non-vararg function body.
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

// TestCompile_Closure verifies CLOSURE + trailing pseudo-instructions (MOVE
// indicating capture of an enclosing local).
func TestCompile_Closure(t *testing.T) {
	src := `
local function outer()
  local x = 1
  local function inner() return x end
  return inner
end
`
	_, protos := compileSource(t, src)
	// inner is the Proto compiled first (depth-first)
	inner := protos[0]
	if len(inner.UpvalDescs) != 1 {
		t.Fatalf("inner UpvalDescs len=%d want 1: %v", len(inner.UpvalDescs), inner.UpvalDescs)
	}
	if !inner.UpvalDescs[0].InStack || inner.UpvalDescs[0].Idx != 0 {
		t.Errorf("inner upval = %+v, want InStack=true Idx=0 (capturing outer's x at R0)", inner.UpvalDescs[0])
	}
	// outer's CLOSURE should be followed by a MOVE pseudo-instruction (B=0
	// indicates capture of R0)
	outer := protos[1]
	got := dumpProto(outer)
	// LOADK R0 K0(1); CLOSURE R1 Bx=0; MOVE 0 0 0 (pseudo-instr); MOVE R2 R1; RETURN ...
	// Find the first instruction after CLOSURE
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
