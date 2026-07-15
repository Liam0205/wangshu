//go:build wangshu_p4

package jit

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/value"
)

// TestPJ7_IdentityForm_NoOverwrite prove-the-path hit evidence dedicated to the
// writeRetA=false path for the identity form (`function(x) return x end`).
//
// **Background**: the writeRetA=false path only has implicit e2e coverage; it
// lacks an explicit positive unit test inside the jit package. This test ensures
// the identity function path (R(A) not overwritten by the mmap segment's dummy
// RAX) has explicit hit evidence. It asserts:
//   - SetReg is not called (R(A) is not overwritten by the mmap segment)
//   - DoReturn is called once (the frame-pop path is reached)
//
// This is a [[prove-the-path-under-test]] instance — it guards against the blind
// spot where a mistaken change to writeRetA logic makes the identity function
// silently return the wrong result (return arg → return nil) while the LOADK e2e
// test still passes.
func TestPJ7_IdentityForm_NoOverwrite(t *testing.T) {
	// `function(x) return x end` compiles to RETURN 0 2 (luac-optimized form, length 1 / 2)
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.RETURN, 0, 2, 0), // real execution: return R(0)
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0), // dead (luac trailing redundancy)
		},
	}
	gc, host := compileWithHost(t, proto)
	defer tryDispose(t, gc)
	stack := make([]uint64, 4)
	if status := gc.Run(stack, 0); status != 0 {
		t.Errorf("Run status = %d, want 0", status)
	}
	if len(host.regs) != 0 {
		t.Errorf("identity 形态不应调 SetReg(R(0) 已是参数值,不应被覆盖),got regs=%v", host.regs)
	}
	if host.doReturnCalls != 1 {
		t.Errorf("DoReturn 应调 1 次, got %d", host.doReturnCalls)
	}
	// retA should be RETURN's A=0
	if host.lastReturnA != 0 {
		t.Errorf("DoReturn A = %d, want 0", host.lastReturnA)
	}
	// retB = 2 (returns 1 value)
	if host.lastReturnB != 2 {
		t.Errorf("DoReturn B = %d, want 2", host.lastReturnB)
	}
	// retPC = 0 (the first RETURN is the one really executed, length-2 luac-optimized form)
	if host.lastReturnPC != 0 {
		t.Errorf("DoReturn PC = %d, want 0", host.lastReturnPC)
	}
}

// TestPJ7_EmptyFunctionForm_NoOverwrite `function() end` single RETURN 0 1
// (B=1, 0 return values) form: writeRetA=false + retB=1 both skip writing the slot.
func TestPJ7_EmptyFunctionForm_NoOverwrite(t *testing.T) {
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0), // RETURN 0 1
		},
	}
	gc, host := compileWithHost(t, proto)
	defer tryDispose(t, gc)
	stack := make([]uint64, 4)
	if status := gc.Run(stack, 0); status != 0 {
		t.Errorf("Run status = %d, want 0", status)
	}
	if len(host.regs) != 0 {
		t.Errorf("空函数形态不应调 SetReg, got regs=%v", host.regs)
	}
	if host.doReturnCalls != 1 {
		t.Errorf("DoReturn 应调 1 次, got %d", host.doReturnCalls)
	}
	if host.lastReturnB != 1 {
		t.Errorf("DoReturn B = %d, want 1(0 返回值)", host.lastReturnB)
	}
}

// TestPJ7_MoveForm_RetargetA MOVE A B + RETURN A 2 form: retA should be set to B
// (skip the R(A) = R(B) staging move and return R(B) directly).
func TestPJ7_MoveForm_RetargetA(t *testing.T) {
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0), // MOVE 1 0 (R(1) = R(0))
			bytecode.EncodeABC(bytecode.RETURN, 1, 2, 0),
		},
	}
	gc, host := compileWithHost(t, proto)
	defer tryDispose(t, gc)
	stack := make([]uint64, 4)
	if status := gc.Run(stack, 0); status != 0 {
		t.Errorf("Run status = %d, want 0", status)
	}
	if len(host.regs) != 0 {
		t.Errorf("MOVE+RETURN 形态不应调 SetReg(R(B) 已是参数值), got regs=%v", host.regs)
	}
	// DoReturn's A should be MOVE's B (0, skipping the staging move and returning R(0) directly)
	if host.lastReturnA != 0 {
		t.Errorf("DoReturn A = %d, want 0(retA 设为 MOVE B 跳过中转)", host.lastReturnA)
	}
}

// TestPJ7_GetUpvalForm_HostRoute GETUPVAL A B + RETURN A 2 form: Run calls
// host.GetUpval + SetReg (prelude opcode path).
func TestPJ7_GetUpvalForm_HostRoute(t *testing.T) {
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.GETUPVAL, 0, 0, 0), // GETUPVAL 0 0
			bytecode.EncodeABC(bytecode.RETURN, 0, 2, 0),
		},
	}
	gc, host := compileWithHost(t, proto)
	defer tryDispose(t, gc)
	// preset upval[0] = NaN-box number 99
	expected := uint64(value.NumberValue(99))
	host.upvals[0] = expected
	stack := make([]uint64, 4)
	if status := gc.Run(stack, 0); status != 0 {
		t.Errorf("Run status = %d, want 0", status)
	}
	got, ok := host.regs[0]
	if !ok {
		t.Fatal("GETUPVAL 形态:Run 应经 host.SetReg(0, upval[0]) 写 R(0)")
	}
	if got != expected {
		t.Errorf("R(0) = 0x%x, want 0x%x(upval[0] 经 GetUpval 路径写入)", got, expected)
	}
	if host.doReturnCalls != 1 {
		t.Errorf("DoReturn 应调 1 次, got %d", host.doReturnCalls)
	}
}

// TestPJ7_ArithForm_HostRoute_OK ADD/SUB/MUL/DIV/MOD/POW + RETURN A 2 form:
// Run calls the host.Arith helper to do the arithmetic; the OK path writes R(A)
// via SetReg + DoReturn pops the frame.
//
// **Background**: this batch extends PJ7 to support the arithmetic family
// (`function(x, y) return x + y end` / `function(x) return x + 1 end`, etc.).
// The mmap segment stays a dummy (`mov rax, _; ret`); the real value is produced
// by the Go-side Run prelude path calling host.Arith. The mock host skips the
// arithmetic semantics and only checks the mechanics: "prelude path reached +
// arguments passed correctly + R(A) written + DoReturn".
//
// **prove-the-path hit evidence**: if analyzeShape mistakenly falls back to
// rejecting ADD/SUB/... (preludeOp left unset), this test catches it
// immediately: arithCalls=0 → R(A) not written → assertion fails.
func TestPJ7_ArithForm_HostRoute_OK(t *testing.T) {
	cases := []struct {
		name string
		op   bytecode.OpCode
	}{
		{"ADD", bytecode.ADD},
		{"SUB", bytecode.SUB},
		{"MUL", bytecode.MUL},
		{"DIV", bytecode.DIV},
		{"MOD", bytecode.MOD},
		{"POW", bytecode.POW},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// op A=2 B=0 C=1 + RETURN A=2 B=2 (`function(x,y) return x+y end`
			// form: R(2) = R(0) <op> R(1); return R(2))
			proto := &bytecode.Proto{
				Code: []bytecode.Instruction{
					bytecode.EncodeABC(tc.op, 2, 0, 1),
					bytecode.EncodeABC(bytecode.RETURN, 2, 2, 0),
				},
			}
			c := New()
			host := newMockP4Host()
			c.SetHostState(host)
			expected := uint64(value.NumberValue(42))
			host.arithResult = expected
			gc, err := c.Compile(proto, nil)
			if err != nil {
				t.Fatalf("Compile failed: %v", err)
			}
			defer tryDispose(t, gc)
			stack := make([]uint64, 8)
			if status := gc.Run(stack, 0); status != 0 {
				t.Errorf("Run status = %d, want 0", status)
			}
			if host.arithCalls != 1 {
				t.Errorf("Arith called %d times, want 1", host.arithCalls)
			}
			if host.lastArithOp != int32(tc.op) {
				t.Errorf("Arith op = %d, want %d (%s)", host.lastArithOp, int32(tc.op), tc.name)
			}
			if host.lastArithA != 2 || host.lastArithB != 0 || host.lastArithC != 1 {
				t.Errorf("Arith ABC = (%d,%d,%d), want (2,0,1)", host.lastArithA, host.lastArithB, host.lastArithC)
			}
			got, ok := host.regs[2]
			if !ok {
				t.Fatal("Arith OK 路径:SetReg(2, result) 应被调用(mock 经 arithResult 写)")
			}
			if got != expected {
				t.Errorf("R(2) = 0x%x, want 0x%x(Arith result)", got, expected)
			}
			if host.doReturnCalls != 1 {
				t.Errorf("DoReturn 应调 1 次, got %d", host.doReturnCalls)
			}
		})
	}
}

// TestPJ7_ArithForm_HostRoute_Err ADD + RETURN form, Arith error path: the
// helper returns 1 → Run returns 1 (ERR) → enterGibbous picks up pendingErr and
// bubbles it. This test checks that Run does not call DoReturn (the ERR path
// bypasses the frame pop, which enterGibbous handles as a fallback).
//
// **Background**: the arithmetic-family prelude introduces an error path (raise
// on "perform arithmetic on string/table", etc.), unlike the LOADK/MOVE/GETUPVAL
// trio which "never errors". This test asserts the error-bubbling mechanics:
// status != 0 → DoReturn skipped.
func TestPJ7_ArithForm_HostRoute_Err(t *testing.T) {
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.ADD, 0, 0, 1),
			bytecode.EncodeABC(bytecode.RETURN, 0, 2, 0),
		},
	}
	c := New()
	host := newMockP4Host()
	c.SetHostState(host)
	host.arithRetCode = 1 // simulate helper raise
	gc, err := c.Compile(proto, nil)
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}
	defer tryDispose(t, gc)
	stack := make([]uint64, 4)
	status := gc.Run(stack, 0)
	if status != 1 {
		t.Errorf("Run status = %d, want 1(ERR 冒泡)", status)
	}
	if host.arithCalls != 1 {
		t.Errorf("Arith called %d times, want 1", host.arithCalls)
	}
	if host.doReturnCalls != 0 {
		t.Errorf("DoReturn 应跳过(ERR 路径不经 RETURN),got %d", host.doReturnCalls)
	}
	if _, ok := host.regs[0]; ok {
		t.Errorf("Arith ERR 路径不应写 R(A),got regs=%v", host.regs)
	}
}

// TestPJ7_UnaryForm_HostRoute_OK UNM/LEN A B + RETURN A 2 form: Run calls the
// host.Unm/Len helper; the OK path writes R(A).
//
// **Background**: UNM (`function(x) return -x end`) / LEN (`function(s)
// return #s end`) are the unary-arithmetic + length family. The host helper
// signature (base, pc, b, a) int32 differs from Arith's (base, pc, op, b, c, a);
// analyzeShape uses a separate case and Run uses a separate host helper
// interface method.
//
// **prove-the-path hit evidence**: the mock host unaryCalls counter +
// lastUnaryOp tag verify that UNM calls Unm / LEN calls Len (no cross-dispatch).
func TestPJ7_UnaryForm_HostRoute_OK(t *testing.T) {
	cases := []struct {
		name      string
		op        bytecode.OpCode
		expectTag int32 // tag inside mock: 1=Unm / 2=Len
	}{
		{"UNM", bytecode.UNM, 1},
		{"LEN", bytecode.LEN, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// op A=1 B=0 + RETURN A=1 B=2 (`function(x) return op(x) end`
			// form: R(1) = op R(0); return R(1))
			proto := &bytecode.Proto{
				Code: []bytecode.Instruction{
					bytecode.EncodeABC(tc.op, 1, 0, 0),
					bytecode.EncodeABC(bytecode.RETURN, 1, 2, 0),
				},
			}
			c := New()
			host := newMockP4Host()
			c.SetHostState(host)
			expected := uint64(value.NumberValue(99))
			host.unaryResult = expected
			gc, err := c.Compile(proto, nil)
			if err != nil {
				t.Fatalf("Compile failed: %v", err)
			}
			defer tryDispose(t, gc)
			stack := make([]uint64, 4)
			if status := gc.Run(stack, 0); status != 0 {
				t.Errorf("Run status = %d, want 0", status)
			}
			if host.unaryCalls != 1 {
				t.Errorf("unary helper called %d times, want 1", host.unaryCalls)
			}
			if host.lastUnaryOp != tc.expectTag {
				t.Errorf("called wrong unary helper:tag = %d, want %d(%s)",
					host.lastUnaryOp, tc.expectTag, tc.name)
			}
			if host.lastUnaryA != 1 || host.lastUnaryB != 0 {
				t.Errorf("unary BA = (%d,%d), want (0,1)", host.lastUnaryB, host.lastUnaryA)
			}
			got, ok := host.regs[1]
			if !ok {
				t.Fatal("OK 路径:SetReg(1, result) 应被调用")
			}
			if got != expected {
				t.Errorf("R(1) = 0x%x, want 0x%x", got, expected)
			}
			if host.doReturnCalls != 1 {
				t.Errorf("DoReturn 应调 1 次, got %d", host.doReturnCalls)
			}
		})
	}
}

// TestPJ7_UnaryForm_HostRoute_Err UNM error path (helper returns 1).
func TestPJ7_UnaryForm_HostRoute_Err(t *testing.T) {
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.UNM, 1, 0, 0),
			bytecode.EncodeABC(bytecode.RETURN, 1, 2, 0),
		},
	}
	c := New()
	host := newMockP4Host()
	c.SetHostState(host)
	host.unaryRetCode = 1
	gc, err := c.Compile(proto, nil)
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}
	defer tryDispose(t, gc)
	stack := make([]uint64, 4)
	status := gc.Run(stack, 0)
	if status != 1 {
		t.Errorf("Run status = %d, want 1(ERR)", status)
	}
	if host.doReturnCalls != 0 {
		t.Errorf("DoReturn 应跳过(ERR), got %d", host.doReturnCalls)
	}
}

// TestPJ7_NotForm_HostRoute_OK NOT A B + RETURN A 2 form — with the GetReg
// interface added in this batch, the NOT path routes through host.GetReg +
// value.Truthy + SetReg.
//
// **History**: the earlier TestPJ7_NotForm_Rejected regression test asserted
// "NOT should be rejected" (no GetReg interface). This batch adds GetReg → NOT
// can now be supported, so the test is changed to check the "OK path".
func TestPJ7_NotForm_HostRoute_OK(t *testing.T) {
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.NOT, 1, 0, 0), // NOT 1 0
			bytecode.EncodeABC(bytecode.RETURN, 1, 2, 0),
		},
	}
	c := New()
	host := newMockP4Host()
	c.SetHostState(host)
	// preset R(0) = number 0 (Truthy=true → NOT = false)
	host.regs[0] = uint64(value.NumberValue(0))
	gc, err := c.Compile(proto, nil)
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}
	defer tryDispose(t, gc)
	stack := make([]uint64, 4)
	if status := gc.Run(stack, 0); status != 0 {
		t.Errorf("Run status = %d, want 0", status)
	}
	got, ok := host.regs[1]
	if !ok {
		t.Fatal("NOT 形态:SetReg(1, BoolValue(!Truthy(R(0)))) 应被调用")
	}
	if value.Value(got) != value.False {
		t.Errorf("R(1) = 0x%x, want False(=0x%x;NOT number 0 = false 因 0 在 Lua 为 truthy)",
			got, uint64(value.False))
	}
	// now test nil (Truthy=false → NOT = true)
	host = newMockP4Host()
	c.SetHostState(host)
	host.regs[0] = uint64(value.Nil)
	gc2, err := c.Compile(proto, nil)
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}
	defer tryDispose(t, gc2)
	if status := gc2.Run(stack, 0); status != 0 {
		t.Errorf("Run status = %d, want 0", status)
	}
	if value.Value(host.regs[1]) != value.True {
		t.Errorf("R(1) = 0x%x, want True(NOT nil = true)", host.regs[1])
	}
}

// TestPJ7_SetUpvalForm_HostRoute_OK SETUPVAL A B + RETURN A 1 setter form.
func TestPJ7_SetUpvalForm_HostRoute_OK(t *testing.T) {
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.SETUPVAL, 0, 1, 0), // SETUPVAL A=0 B=1
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
	}
	c := New()
	host := newMockP4Host()
	c.SetHostState(host)
	gc, err := c.Compile(proto, nil)
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}
	defer tryDispose(t, gc)
	stack := make([]uint64, 4)
	if status := gc.Run(stack, 0); status != 0 {
		t.Errorf("Run status = %d, want 0", status)
	}
	if host.setUpvalCalls != 1 {
		t.Errorf("SetUpvalFromReg 应调 1 次, got %d", host.setUpvalCalls)
	}
	if host.lastSetUpvalA != 0 || host.lastSetUpvalB != 1 {
		t.Errorf("SetUpvalFromReg AB = (%d,%d), want (0,1)", host.lastSetUpvalA, host.lastSetUpvalB)
	}
	if host.doReturnCalls != 1 {
		t.Errorf("DoReturn 应调 1 次, got %d", host.doReturnCalls)
	}
}

// TestPJ7_NewTableForm_HostRoute NEWTABLE A B C + RETURN A 2 form: Run calls the
// host.NewTable helper, which never raises.
func TestPJ7_NewTableForm_HostRoute(t *testing.T) {
	// NEWTABLE A=0 B=0 C=0 + RETURN A=0 B=2(`function() return {} end`)
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.NEWTABLE, 0, 0, 0),
			bytecode.EncodeABC(bytecode.RETURN, 0, 2, 0),
		},
	}
	c := New()
	host := newMockP4Host()
	c.SetHostState(host)
	expected := uint64(0x1234567890abcdef) // simulated NaN-box table ref
	host.tableResult = expected
	gc, err := c.Compile(proto, nil)
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}
	defer tryDispose(t, gc)
	stack := make([]uint64, 4)
	if status := gc.Run(stack, 0); status != 0 {
		t.Errorf("Run status = %d, want 0", status)
	}
	if host.tableCalls != 1 {
		t.Errorf("NewTable called %d times, want 1", host.tableCalls)
	}
	if host.lastTableOp != 1 {
		t.Errorf("called wrong helper:tag = %d, want 1(NewTable)", host.lastTableOp)
	}
	if host.lastTableA != 0 {
		t.Errorf("NewTable A = %d, want 0", host.lastTableA)
	}
	got, ok := host.regs[0]
	if !ok || got != expected {
		t.Errorf("R(0) = 0x%x (ok=%v), want 0x%x", got, ok, expected)
	}
	if host.doReturnCalls != 1 {
		t.Errorf("DoReturn 应调 1 次, got %d", host.doReturnCalls)
	}
}

// TestPJ7_GetTableForm_HostRoute_OK GETTABLE A B C + RETURN A 2 form: Run calls
// the host.GetTable helper; the OK path writes R(A).
func TestPJ7_GetTableForm_HostRoute_OK(t *testing.T) {
	// GETTABLE A=2 B=0 C=1 + RETURN A=2 B=2(`function(t, k) return t[k] end`)
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.GETTABLE, 2, 0, 1),
			bytecode.EncodeABC(bytecode.RETURN, 2, 2, 0),
		},
	}
	c := New()
	host := newMockP4Host()
	c.SetHostState(host)
	expected := uint64(value.NumberValue(123))
	host.tableResult = expected
	gc, err := c.Compile(proto, nil)
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}
	defer tryDispose(t, gc)
	stack := make([]uint64, 8)
	if status := gc.Run(stack, 0); status != 0 {
		t.Errorf("Run status = %d, want 0", status)
	}
	if host.tableCalls != 1 || host.lastTableOp != 2 {
		t.Errorf("GetTable not called(calls=%d / tag=%d, want 1/2)", host.tableCalls, host.lastTableOp)
	}
	if host.lastTableA != 2 || host.lastTableB != 0 || host.lastTableC != 1 {
		t.Errorf("GetTable ABC = (%d,%d,%d), want (2,0,1)", host.lastTableA, host.lastTableB, host.lastTableC)
	}
	got, ok := host.regs[2]
	if !ok || got != expected {
		t.Errorf("R(2) = 0x%x (ok=%v), want 0x%x", got, ok, expected)
	}
}

// TestPJ7_GetTableForm_HostRoute_Err GETTABLE error path (helper returns 1).
func TestPJ7_GetTableForm_HostRoute_Err(t *testing.T) {
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.GETTABLE, 2, 0, 1),
			bytecode.EncodeABC(bytecode.RETURN, 2, 2, 0),
		},
	}
	c := New()
	host := newMockP4Host()
	c.SetHostState(host)
	host.tableRetCode = 1
	gc, err := c.Compile(proto, nil)
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}
	defer tryDispose(t, gc)
	stack := make([]uint64, 8)
	status := gc.Run(stack, 0)
	if status != 1 {
		t.Errorf("Run status = %d, want 1(ERR)", status)
	}
	if host.doReturnCalls != 0 {
		t.Errorf("DoReturn 应跳过(ERR), got %d", host.doReturnCalls)
	}
}

// TestPJ7_GetGlobalForm_HostRoute_OK GETGLOBAL A Bx + RETURN A 2 form.
func TestPJ7_GetGlobalForm_HostRoute_OK(t *testing.T) {
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABx(bytecode.GETGLOBAL, 0, 5),
			bytecode.EncodeABC(bytecode.RETURN, 0, 2, 0),
		},
		Consts:       make([]value.Value, 10), // placeholder length, Bx=5 in bounds
		StringLitIdx: make([]int32, 10),
	}
	c := New()
	host := newMockP4Host()
	c.SetHostState(host)
	expected := uint64(value.NumberValue(7))
	host.tableResult = expected
	gc, err := c.Compile(proto, nil)
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}
	defer tryDispose(t, gc)
	stack := make([]uint64, 4)
	if status := gc.Run(stack, 0); status != 0 {
		t.Errorf("Run status = %d, want 0", status)
	}
	if host.tableCalls != 1 || host.lastTableOp != 4 {
		t.Errorf("DoGetGlobal not called(calls=%d / tag=%d, want 1/4)", host.tableCalls, host.lastTableOp)
	}
	if host.lastTableA != 0 || host.lastTableB != 5 {
		t.Errorf("DoGetGlobal A/Bx = (%d,%d), want (0,5)", host.lastTableA, host.lastTableB)
	}
	got, ok := host.regs[0]
	if !ok || got != expected {
		t.Errorf("R(0) = 0x%x (ok=%v), want 0x%x", got, ok, expected)
	}
}

// TestPJ7_SetTableForm_HostRoute_OK SETTABLE A B C + RETURN A 1 setter form.
// Verifies that retB=1 goes through the prelude (previously the retB>=2 guard
// would reject it; the new split guard fixes this).
func TestPJ7_SetTableForm_HostRoute_OK(t *testing.T) {
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.SETTABLE, 0, 1, 2),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0), // setter retB=1
		},
	}
	c := New()
	host := newMockP4Host()
	c.SetHostState(host)
	gc, err := c.Compile(proto, nil)
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}
	defer tryDispose(t, gc)
	stack := make([]uint64, 8)
	if status := gc.Run(stack, 0); status != 0 {
		t.Errorf("Run status = %d, want 0", status)
	}
	if host.tableCalls != 1 || host.lastTableOp != 3 {
		t.Errorf("SetTable not called(calls=%d / tag=%d, want 1/3)", host.tableCalls, host.lastTableOp)
	}
	if host.lastTableA != 0 || host.lastTableB != 1 || host.lastTableC != 2 {
		t.Errorf("SetTable ABC = (%d,%d,%d), want (0,1,2)", host.lastTableA, host.lastTableB, host.lastTableC)
	}
	// setter does not write R(A)
	if _, ok := host.regs[0]; ok {
		t.Errorf("setter 不应写 R(A), got regs=%v", host.regs)
	}
	// retB=1 → DoReturn still called (frame pop)
	if host.doReturnCalls != 1 {
		t.Errorf("DoReturn 应调 1 次, got %d", host.doReturnCalls)
	}
}

// TestPJ7_SetTableForm_HostRoute_Err SETTABLE error path.
func TestPJ7_SetTableForm_HostRoute_Err(t *testing.T) {
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.SETTABLE, 0, 1, 2),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
	}
	c := New()
	host := newMockP4Host()
	c.SetHostState(host)
	host.tableRetCode = 1
	gc, err := c.Compile(proto, nil)
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}
	defer tryDispose(t, gc)
	stack := make([]uint64, 8)
	status := gc.Run(stack, 0)
	if status != 1 {
		t.Errorf("Run status = %d, want 1(ERR)", status)
	}
	if host.doReturnCalls != 0 {
		t.Errorf("DoReturn 应跳过(ERR), got %d", host.doReturnCalls)
	}
}

// TestPJ7_CompareForm_AnalyzeShape verifies that analyzeCompareForm recognizes
// the EQ/LT/LE 6-op template form (following the previous batch's review 🟢:
// e2e covers it, but the jit-package targeted unit regression was missing).
//
// This test asserts directly against analyzeShape:
//   - EQ/LT/LE × cmpA(0/1) 5/6-op forms give ok=true with all fields mapped correctly
//   - any deviation in a template slot (JMP sBx≠1 / LOADBOOL out of order /
//     RETURN A inconsistent / length ∉ {5,6}) gives ok=false immediately.
func TestPJ7_CompareForm_AnalyzeShape(t *testing.T) {
	// helper: build the template EQ/LT/LE 6-op form (with optional dead RETURN).
	build := func(cmpOp bytecode.OpCode, cmpA, cmpB, cmpC, retA int, withDead bool) *bytecode.Proto {
		code := []bytecode.Instruction{
			bytecode.EncodeABC(cmpOp, cmpA, cmpB, cmpC),
			bytecode.EncodeAsBx(bytecode.JMP, 0, 1),
			bytecode.EncodeABC(bytecode.LOADBOOL, retA, 0, 1),
			bytecode.EncodeABC(bytecode.LOADBOOL, retA, 1, 0),
			bytecode.EncodeABC(bytecode.RETURN, retA, 2, 0),
		}
		if withDead {
			code = append(code, bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0))
		}
		return &bytecode.Proto{Code: code}
	}

	// OK sub-cases: EQ/LT/LE × cmpA(0/1) × dead(true/false)
	okCases := []struct {
		op   bytecode.OpCode
		cmpA int
		dead bool
	}{
		{bytecode.EQ, 1, true},
		{bytecode.EQ, 0, false},
		{bytecode.LT, 1, true},
		{bytecode.LT, 0, false},
		{bytecode.LE, 1, true},
		{bytecode.LE, 0, false},
	}
	for _, tc := range okCases {
		name := tc.op.String()
		if tc.cmpA == 0 {
			name += "_cmpA0"
		} else {
			name += "_cmpA1"
		}
		if tc.dead {
			name += "_withDead"
		}
		t.Run("OK/"+name, func(t *testing.T) {
			proto := build(tc.op, tc.cmpA, 0, 256, 1, tc.dead)
			info := analyzeShape(proto)
			if !info.ok {
				t.Fatalf("%s 形态应识别成功", name)
			}
			if info.preludeOp != uint8(tc.op) {
				t.Errorf("preludeOp = %d, want %d (%s)", info.preludeOp, tc.op, tc.op)
			}
			if info.cmpA != uint8(tc.cmpA) {
				t.Errorf("cmpA = %d, want %d", info.cmpA, tc.cmpA)
			}
			if info.preludeArg != 0 {
				t.Errorf("preludeArg(B) = %d, want 0", info.preludeArg)
			}
			if info.preludeC != 256 {
				t.Errorf("preludeC(C) = %d, want 256", info.preludeC)
			}
			if info.retA != 1 {
				t.Errorf("retA = %d, want 1", info.retA)
			}
			if info.retB != 2 {
				t.Errorf("retB = %d, want 2", info.retB)
			}
			if info.retPC != 4 {
				t.Errorf("retPC = %d, want 4 (RETURN 在 pc 4)", info.retPC)
			}
		})
	}

	// Reject sub-cases: template deviation should return ok=false immediately
	t.Run("Reject/cmpA_outOfRange", func(t *testing.T) {
		// cmpA=2 outside {0,1}
		if analyzeShape(build(bytecode.EQ, 2, 0, 1, 1, false)).ok {
			t.Error("cmpA=2 应被拒")
		}
	})

	t.Run("Reject/cmpOp_notSupported", func(t *testing.T) {
		// use ADD instead of EQ
		proto := build(bytecode.EQ, 1, 0, 1, 1, false)
		proto.Code[0] = bytecode.EncodeABC(bytecode.ADD, 1, 0, 1)
		if analyzeShape(proto).ok {
			t.Error("非 EQ/LT/LE op 应被拒")
		}
	})

	t.Run("Reject/JMP_sBx_notOne", func(t *testing.T) {
		proto := build(bytecode.EQ, 1, 0, 1, 1, false)
		proto.Code[1] = bytecode.EncodeAsBx(bytecode.JMP, 0, 2) // sBx=2 does not match
		if analyzeShape(proto).ok {
			t.Error("JMP sBx≠1 应被拒")
		}
	})

	t.Run("Reject/LOADBOOL_falseSlot_wrongBC", func(t *testing.T) {
		proto := build(bytecode.EQ, 1, 0, 1, 1, false)
		proto.Code[2] = bytecode.EncodeABC(bytecode.LOADBOOL, 1, 1, 0) // should be B=0 C=1
		if analyzeShape(proto).ok {
			t.Error("LOADBOOL slot[2] B/C 错应被拒")
		}
	})

	t.Run("Reject/LOADBOOL_A_inconsistent", func(t *testing.T) {
		proto := build(bytecode.EQ, 1, 0, 1, 1, false)
		proto.Code[3] = bytecode.EncodeABC(bytecode.LOADBOOL, 2, 1, 0) // A=2 ≠ retA=1
		if analyzeShape(proto).ok {
			t.Error("LOADBOOL A 不一致应被拒")
		}
	})

	t.Run("Reject/RETURN_A_inconsistent", func(t *testing.T) {
		proto := build(bytecode.EQ, 1, 0, 1, 1, false)
		proto.Code[4] = bytecode.EncodeABC(bytecode.RETURN, 2, 2, 0) // A=2 ≠ lbTrueA=1
		if analyzeShape(proto).ok {
			t.Error("RETURN A 不一致应被拒")
		}
	})

	t.Run("Reject/length_4", func(t *testing.T) {
		// length 4 not in {5,6}
		proto := &bytecode.Proto{
			Code: []bytecode.Instruction{
				bytecode.EncodeABC(bytecode.EQ, 1, 0, 1),
				bytecode.EncodeAsBx(bytecode.JMP, 0, 1),
				bytecode.EncodeABC(bytecode.LOADBOOL, 1, 0, 1),
				bytecode.EncodeABC(bytecode.LOADBOOL, 1, 1, 0),
			},
		}
		if analyzeShape(proto).ok {
			t.Error("长度 4 应被拒")
		}
	})

	t.Run("Reject/length_7", func(t *testing.T) {
		// length 7 not in {5,6}
		proto := build(bytecode.EQ, 1, 0, 1, 1, true)
		proto.Code = append(proto.Code, bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0))
		if analyzeShape(proto).ok {
			t.Error("长度 7 应被拒")
		}
	})
}

// TestPJ7_CompareForm_RunFolding verifies the Run-path folding semantics:
//   - cmpA=1 + cmpResult=1 → True
//   - cmpA=1 + cmpResult=0 → False
//   - cmpA=0 + cmpResult=0 → True (negated)
//   - cmpA=0 + cmpResult=1 → False
//   - cmpErr → Run returns 1
func TestPJ7_CompareForm_RunFolding(t *testing.T) {
	cases := []struct {
		name       string
		cmpA       int
		cmpResult  bool
		cmpErr     bool
		wantStatus int32
		wantRetA   value.Value
		wantErr    bool
	}{
		{"cmpA1_eq_true", 1, true, false, 0, value.True, false},
		{"cmpA1_eq_false", 1, false, false, 0, value.False, false},
		{"cmpA0_neg_true", 0, false, false, 0, value.True, false},
		{"cmpA0_neg_false", 0, true, false, 0, value.False, false},
		{"err", 1, false, true, 1, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// EQ A=cmpA B=0 C=1 + JMP + LOADBOOL × 2 + RETURN
			proto := &bytecode.Proto{
				Code: []bytecode.Instruction{
					bytecode.EncodeABC(bytecode.EQ, tc.cmpA, 0, 1),
					bytecode.EncodeAsBx(bytecode.JMP, 0, 1),
					bytecode.EncodeABC(bytecode.LOADBOOL, 1, 0, 1),
					bytecode.EncodeABC(bytecode.LOADBOOL, 1, 1, 0),
					bytecode.EncodeABC(bytecode.RETURN, 1, 2, 0),
				},
			}
			c := New()
			host := newMockP4Host()
			c.SetHostState(host)
			host.cmpResult = tc.cmpResult
			host.cmpErr = tc.cmpErr
			gc, err := c.Compile(proto, nil)
			if err != nil {
				t.Fatalf("Compile failed: %v", err)
			}
			defer tryDispose(t, gc)
			stack := make([]uint64, 4)
			status := gc.Run(stack, 0)
			if status != tc.wantStatus {
				t.Errorf("Run status = %d, want %d", status, tc.wantStatus)
			}
			if host.cmpCalls != 1 {
				t.Errorf("Compare 应调 1 次, got %d", host.cmpCalls)
			}
			if tc.wantErr {
				if host.doReturnCalls != 0 {
					t.Errorf("ERR 路径 DoReturn 应跳过, got %d", host.doReturnCalls)
				}
				if _, ok := host.regs[1]; ok {
					t.Errorf("ERR 路径不应写 R(retA)")
				}
			} else {
				got, ok := host.regs[1]
				if !ok {
					t.Fatal("OK 路径 SetReg(retA) 应被调用")
				}
				if value.Value(got) != tc.wantRetA {
					t.Errorf("R(1) = 0x%x, want 0x%x", got, uint64(tc.wantRetA))
				}
				if host.doReturnCalls != 1 {
					t.Errorf("DoReturn 应调 1 次, got %d", host.doReturnCalls)
				}
			}
		})
	}
}
