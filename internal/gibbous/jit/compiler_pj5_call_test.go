//go:build wangshu_p4

package jit

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/value"
)

// TestPJ5_AnalyzeCallVoidForm_Recognize verifies analyzeCallVoidForm positively recognizes
// the MOVE+CALL+RETURN void three-op single-BB form (`function(g) g() end` kind, form A).
func TestPJ5_AnalyzeCallVoidForm_Recognize(t *testing.T) {
	// Form A: MOVE 1 0; CALL 1 1 1; RETURN 0 1
	//  - MOVE.A=1 (callee slot) MOVE.B=0 (arg source, i.e. function param g slot)
	//  - CALL.A=1 (callee slot matches MOVE.A) CALL.B=1 (0 args) CALL.C=1 (0 returns)
	//  - RETURN.A=0 RETURN.B=1 (0 return values)
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0),
			bytecode.EncodeABC(bytecode.CALL, 1, 1, 1),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
	}
	info, ok := analyzeCallVoidForm(proto)
	if !ok {
		t.Fatal("analyzeCallVoidForm should accept MOVE+CALL+RETURN void form")
	}
	if !info.isCallVoid {
		t.Error("info.isCallVoid should be true")
	}
	if info.isCallUpval {
		t.Error("info.isCallUpval should be false for MOVE form A")
	}
	if info.callA != 1 || info.callB != 1 || info.callC != 1 {
		t.Errorf("callA/B/C = %d/%d/%d, want 1/1/1", info.callA, info.callB, info.callC)
	}
	if info.retA != 0 || info.retB != 1 || info.retPC != 2 {
		t.Errorf("retA/B/PC = %d/%d/%d, want 0/1/2", info.retA, info.retB, info.retPC)
	}
	if info.preludeOp != uint8(bytecode.CALL) {
		t.Errorf("preludeOp = %d, want %d (CALL)", info.preludeOp, bytecode.CALL)
	}
	if info.preludeArg != 0 {
		t.Errorf("preludeArg = %d, want 0 (MOVE.B)", info.preludeArg)
	}
}

// TestPJ5_AnalyzeCallVoidForm_RecognizeUpval verifies analyzeCallVoidForm positively recognizes
// GETUPVAL+CALL+RETURN void (form B, `local function f()...end; function() f() end`).
func TestPJ5_AnalyzeCallVoidForm_RecognizeUpval(t *testing.T) {
	// Form B: GETUPVAL 0 3; CALL 0 1 1; RETURN 0 1
	//  - GETUPVAL.A=0 (callee slot) GETUPVAL.B=3 (upvalue index)
	//  - CALL.A=0 (callee slot matches GETUPVAL.A) CALL.B=1 C=1
	//  - RETURN.A=0 B=1
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.GETUPVAL, 0, 3, 0),
			bytecode.EncodeABC(bytecode.CALL, 0, 1, 1),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
	}
	info, ok := analyzeCallVoidForm(proto)
	if !ok {
		t.Fatal("analyzeCallVoidForm should accept GETUPVAL+CALL+RETURN void form B")
	}
	if !info.isCallVoid {
		t.Error("info.isCallVoid should be true")
	}
	if !info.isCallUpval {
		t.Error("info.isCallUpval should be true for GETUPVAL form B")
	}
	if info.callA != 0 {
		t.Errorf("callA = %d, want 0", info.callA)
	}
	if info.preludeArg != 3 {
		t.Errorf("preludeArg = %d, want 3 (GETUPVAL.B)", info.preludeArg)
	}
}

// TestPJ5_AnalyzeCallVoidForm_Reject verifies the form guard rejects non-simplified forms.
func TestPJ5_AnalyzeCallVoidForm_Reject(t *testing.T) {
	cases := []struct {
		name string
		code []bytecode.Instruction
	}{
		{
			"wrong length 2",
			[]bytecode.Instruction{
				bytecode.EncodeABC(bytecode.CALL, 1, 1, 1),
				bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
			},
		},
		{
			"wrong length 4",
			[]bytecode.Instruction{
				bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0),
				bytecode.EncodeABC(bytecode.MOVE, 2, 1, 0),
				bytecode.EncodeABC(bytecode.CALL, 1, 1, 1),
				bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
			},
		},
		{
			"not MOVE first",
			[]bytecode.Instruction{
				bytecode.EncodeABx(bytecode.LOADK, 1, 0),
				bytecode.EncodeABC(bytecode.CALL, 1, 1, 1),
				bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
			},
		},
		{
			"not CALL middle",
			[]bytecode.Instruction{
				bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0),
				bytecode.EncodeABC(bytecode.MOVE, 2, 1, 0),
				bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
			},
		},
		{
			"not RETURN last",
			[]bytecode.Instruction{
				bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0),
				bytecode.EncodeABC(bytecode.CALL, 1, 1, 1),
				bytecode.EncodeABC(bytecode.CALL, 1, 1, 1),
			},
		},
		{
			"MOVE.A != CALL.A",
			[]bytecode.Instruction{
				bytecode.EncodeABC(bytecode.MOVE, 2, 0, 0),
				bytecode.EncodeABC(bytecode.CALL, 1, 1, 1),
				bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
			},
		},
		{
			"CALL.B != 1 (has args)",
			[]bytecode.Instruction{
				bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0),
				bytecode.EncodeABC(bytecode.CALL, 1, 2, 1),
				bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
			},
		},
		{
			"CALL.C != 1 (has return)",
			[]bytecode.Instruction{
				bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0),
				bytecode.EncodeABC(bytecode.CALL, 1, 1, 2),
				bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
			},
		},
		{
			"RETURN.B != 1 (has return)",
			[]bytecode.Instruction{
				bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0),
				bytecode.EncodeABC(bytecode.CALL, 1, 1, 1),
				bytecode.EncodeABC(bytecode.RETURN, 0, 2, 0),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proto := &bytecode.Proto{Code: tc.code}
			if _, ok := analyzeCallVoidForm(proto); ok {
				t.Errorf("analyzeCallVoidForm should reject %s", tc.name)
			}
		})
	}
}

// TestPJ5_RunCallVoidPath verifies the Run prelude CALL case end to end:
//   - mmap segment executes (dummy mov rax,0;ret), Run takes the prelude switch CALL case
//   - MOVE preprocess: host.GetReg(0) + SetReg(1) copies the callee from R(0) to R(1)
//   - host.CallBaseline is invoked, callA=1/callB=1/callC=1/pc=1 (CALL's own pc)
//   - host.DoReturn called once (retPC=2/retA=0/retB=1)
func TestPJ5_RunCallVoidPath(t *testing.T) {
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0),
			bytecode.EncodeABC(bytecode.CALL, 1, 1, 1),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
	}
	gc, host := compileWithHost(t, proto)
	defer tryDispose(t, gc)

	// Preset R(0) = fake function NaN-box; Run should SetReg it into R(1).
	const fakeFuncVal uint64 = 0xFFF9_DEAD_BEEF_0000
	host.regs[0] = fakeFuncVal

	stack := make([]uint64, 4)
	status := gc.Run(stack, 0)
	if status != 0 {
		t.Errorf("Run status = %d, want 0 (OK)", status)
	}
	// MOVE preprocess: R(1) should be R(0)'s fakeFuncVal
	got, ok := host.regs[1]
	if !ok {
		t.Fatal("SetReg(1, ...) not called by MOVE preprocess")
	}
	if got != fakeFuncVal {
		t.Errorf("R(1) = 0x%016x, want 0x%016x (fakeFunc)", got, fakeFuncVal)
	}
	// host.CallBaseline path
	if host.callCalls != 1 {
		t.Errorf("CallBaseline called %d times, want 1", host.callCalls)
	}
	if host.lastCallA != 1 || host.lastCallB != 1 || host.lastCallC != 1 {
		t.Errorf("CallBaseline(A,B,C) = (%d,%d,%d), want (1,1,1)",
			host.lastCallA, host.lastCallB, host.lastCallC)
	}
	if host.lastCallPC != 1 {
		t.Errorf("CallBaseline pc = %d, want 1 (CALL pc)", host.lastCallPC)
	}
	// host.DoReturn path
	if host.doReturnCalls != 1 {
		t.Errorf("DoReturn called %d times, want 1", host.doReturnCalls)
	}
	if host.lastReturnPC != 2 || host.lastReturnA != 0 || host.lastReturnB != 1 {
		t.Errorf("DoReturn(pc,A,B) = (%d,%d,%d), want (2,0,1)",
			host.lastReturnPC, host.lastReturnA, host.lastReturnB)
	}
}

// TestPJ5_RunCallVoidErrPropagate verifies that when host.CallBaseline returns ERR=1, Run
// returns 1 directly and does not call DoReturn (the ERR path does not pop the frame; the
// upper layer's raiseGibbous + enterGibbous takes it over).
func TestPJ5_RunCallVoidErrPropagate(t *testing.T) {
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0),
			bytecode.EncodeABC(bytecode.CALL, 1, 1, 1),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
	}
	gc, host := compileWithHost(t, proto)
	defer tryDispose(t, gc)

	host.callRetCode = 1 // simulate ERR
	stack := make([]uint64, 4)
	status := gc.Run(stack, 0)
	if status != 1 {
		t.Errorf("Run status = %d, want 1 (ERR)", status)
	}
	if host.callCalls != 1 {
		t.Errorf("CallBaseline called %d times, want 1", host.callCalls)
	}
	if host.doReturnCalls != 0 {
		t.Errorf("DoReturn should NOT be called on ERR path (called %d times)", host.doReturnCalls)
	}
}

// TestPJ5_RunCallVoidUpvalPath verifies form B (GETUPVAL+CALL+RETURN void): the Run
// prelude takes the host.GetUpval(base, preludeArg) + SetReg(callA) + CallBaseline
// path (mirroring form A's GetReg + SetReg).
func TestPJ5_RunCallVoidUpvalPath(t *testing.T) {
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.GETUPVAL, 0, 3, 0),
			bytecode.EncodeABC(bytecode.CALL, 0, 1, 1),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
	}
	gc, host := compileWithHost(t, proto)
	defer tryDispose(t, gc)

	// Preset upvalue[3] = fake function NaN-box; Run should GetUpval it into R(0).
	const fakeFuncVal uint64 = 0xFFF9_CAFE_BABE_0001
	host.upvals[3] = fakeFuncVal

	stack := make([]uint64, 4)
	status := gc.Run(stack, 0)
	if status != 0 {
		t.Errorf("Run status = %d, want 0", status)
	}
	// GETUPVAL preprocess: R(0) should be upvals[3]'s fakeFuncVal
	got, ok := host.regs[0]
	if !ok {
		t.Fatal("SetReg(0, ...) not called by GETUPVAL preprocess")
	}
	if got != fakeFuncVal {
		t.Errorf("R(0) = 0x%016x, want 0x%016x (upvals[3])", got, fakeFuncVal)
	}
	if host.callCalls != 1 {
		t.Errorf("CallBaseline called %d times, want 1", host.callCalls)
	}
	if host.lastCallA != 0 || host.lastCallB != 1 || host.lastCallC != 1 {
		t.Errorf("CallBaseline(A,B,C) = (%d,%d,%d), want (0,1,1)",
			host.lastCallA, host.lastCallB, host.lastCallC)
	}
}

// TestPJ5_SupportsAllOpcodesGate_AcceptsCallVoid verifies SupportsAllOpcodes accepts
// the MOVE+CALL+RETURN void form (the F7 gate recognizes this form).
func TestPJ5_SupportsAllOpcodesGate_AcceptsCallVoid(t *testing.T) {
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0),
			bytecode.EncodeABC(bytecode.CALL, 1, 1, 1),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
	}
	c := New()
	if !c.SupportsAllOpcodes(proto) {
		t.Error("SupportsAllOpcodes should accept MOVE+CALL+RETURN void form")
	}
}

// TestPJ5_SpecCallVoidHits verifies that when Compile hits the PJ5 CALL void form,
// the specCallVoidHits probe increments (white-box prove-the-path evidence, per
// llmdoc/guides/prove-the-path-under-test §4).
func TestPJ5_SpecCallVoidHits(t *testing.T) {
	ResetSpecHits()
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0),
			bytecode.EncodeABC(bytecode.CALL, 1, 1, 1),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
	}
	gc, _ := compileWithHost(t, proto)
	defer tryDispose(t, gc)
	if got := SpecCallVoidHits(); got != 1 {
		t.Errorf("SpecCallVoidHits = %d, want 1 (Compile 应命中 PJ5 CALL void inline)", got)
	}
}

// TestPJ5_AnalyzeCallVoidForm_Recognize1ArgK verifies recognition of form A1K (1 K arg):
// MOVE + LOADK + CALL B=2 C=1 + RETURN void.
func TestPJ5_AnalyzeCallVoidForm_Recognize1ArgK(t *testing.T) {
	// Form A1K: MOVE 1 0; LOADK 2 K0; CALL 1 2 1; RETURN 0 1
	//   - MOVE.A=1 (callee slot) MOVE.B=0 (arg source)
	//   - LOADK.A=2 (callee slot+1) LOADK.Bx=0 (K0 index)
	//   - CALL.A=1 CALL.B=2 (1 arg) CALL.C=1 (0 returns)
	//   - RETURN.B=1 (0 return values)
	const kVal uint64 = 0x4040000000000000 // NumberValue(32.0) NaN-box raw
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0),
			bytecode.EncodeABx(bytecode.LOADK, 2, 0),
			bytecode.EncodeABC(bytecode.CALL, 1, 2, 1),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
		Consts: []value.Value{value.Value(kVal)},
	}
	info, ok := analyzeCallVoidForm(proto)
	if !ok {
		t.Fatal("analyzeCallVoidForm should accept MOVE+LOADK+CALL+RETURN void form A1K")
	}
	if !info.isCallVoid {
		t.Error("info.isCallVoid should be true")
	}
	if info.callArgCount != 1 {
		t.Errorf("callArgCount = %d, want 1", info.callArgCount)
	}
	if info.callArg1K != kVal {
		t.Errorf("callArg1K = 0x%016x, want 0x%016x", info.callArg1K, kVal)
	}
	if info.retPC != 3 {
		t.Errorf("retPC = %d, want 3 (RETURN in pc 3)", info.retPC)
	}
	if info.callB != 2 {
		t.Errorf("callB = %d, want 2 (1 arg)", info.callB)
	}
}

// TestPJ5_RunCallVoid1ArgKPath verifies form A1K end to end: Run loads R(callA+1)=K
// then calls CallBaseline, callB=2 + callC=1.
func TestPJ5_RunCallVoid1ArgKPath(t *testing.T) {
	const kVal uint64 = 0x4040000000000000 // NumberValue(32.0)
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0),
			bytecode.EncodeABx(bytecode.LOADK, 2, 0),
			bytecode.EncodeABC(bytecode.CALL, 1, 2, 1),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
		Consts: []value.Value{value.Value(kVal)},
	}
	gc, host := compileWithHost(t, proto)
	defer tryDispose(t, gc)

	const fakeFuncVal uint64 = 0xFFF9_BEEF_0000_0000
	host.regs[0] = fakeFuncVal

	stack := make([]uint64, 4)
	status := gc.Run(stack, 0)
	if status != 0 {
		t.Errorf("Run status = %d, want 0", status)
	}
	if host.regs[1] != fakeFuncVal {
		t.Errorf("R(1) = 0x%016x, want 0x%016x (fakeFunc)", host.regs[1], fakeFuncVal)
	}
	if host.regs[2] != kVal {
		t.Errorf("R(2) = 0x%016x, want 0x%016x (kVal arg)", host.regs[2], kVal)
	}
	if host.callCalls != 1 {
		t.Errorf("CallBaseline called %d times, want 1", host.callCalls)
	}
	if host.lastCallA != 1 || host.lastCallB != 2 || host.lastCallC != 1 {
		t.Errorf("CallBaseline(A,B,C) = (%d,%d,%d), want (1,2,1)",
			host.lastCallA, host.lastCallB, host.lastCallC)
	}
	if host.lastCallPC != 2 {
		t.Errorf("CallBaseline pc = %d, want 2 (CALL pc=2 in length-4 form)", host.lastCallPC)
	}
}

// TestPJ5_AnalyzeCallVoidForm_Recognize1ArgReg verifies recognition of form A1R (1 reg arg):
// MOVE + MOVE + CALL B=2 C=1 + RETURN void.
func TestPJ5_AnalyzeCallVoidForm_Recognize1ArgReg(t *testing.T) {
	// Form A1R: MOVE 2 0; MOVE 3 1; CALL 2 2 1; RETURN 0 1
	//   - MOVE.A=2 (callee slot) MOVE.B=0 (arg source, i.e. function param g slot)
	//   - second MOVE.A=3 (callee slot+1) MOVE.B=1 (arg source, i.e. function param x slot)
	//   - CALL.A=2 CALL.B=2 (1 arg) CALL.C=1
	//   - RETURN.B=1 (0 return values)
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.MOVE, 2, 0, 0),
			bytecode.EncodeABC(bytecode.MOVE, 3, 1, 0),
			bytecode.EncodeABC(bytecode.CALL, 2, 2, 1),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
	}
	info, ok := analyzeCallVoidForm(proto)
	if !ok {
		t.Fatal("analyzeCallVoidForm should accept MOVE+MOVE+CALL+RETURN void form A1R")
	}
	if !info.isCallVoid {
		t.Error("info.isCallVoid should be true")
	}
	if info.callArgCount != 1 {
		t.Errorf("callArgCount = %d, want 1", info.callArgCount)
	}
	if info.callArg1IsK {
		t.Error("callArg1IsK should be false for reg arg form A1R")
	}
	if info.callArg1RegSrc != 1 {
		t.Errorf("callArg1RegSrc = %d, want 1 (MOVE.B)", info.callArg1RegSrc)
	}
	if info.retPC != 3 {
		t.Errorf("retPC = %d, want 3", info.retPC)
	}
}

// TestPJ5_RunCallVoid1ArgRegPath verifies form A1R end to end: Run loads into the arg
// slot via host.GetReg(callArg1RegSrc) + SetReg(callA+1).
func TestPJ5_RunCallVoid1ArgRegPath(t *testing.T) {
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.MOVE, 2, 0, 0),
			bytecode.EncodeABC(bytecode.MOVE, 3, 1, 0),
			bytecode.EncodeABC(bytecode.CALL, 2, 2, 1),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
	}
	gc, host := compileWithHost(t, proto)
	defer tryDispose(t, gc)

	const fakeFuncVal uint64 = 0xFFF9_BEEF_0000_0000
	const fakeArgVal uint64 = 0x4034000000000000 // NumberValue(20.0)
	host.regs[0] = fakeFuncVal
	host.regs[1] = fakeArgVal

	stack := make([]uint64, 4)
	status := gc.Run(stack, 0)
	if status != 0 {
		t.Errorf("Run status = %d, want 0", status)
	}
	if host.regs[2] != fakeFuncVal {
		t.Errorf("R(2) = 0x%016x, want 0x%016x (fakeFunc)", host.regs[2], fakeFuncVal)
	}
	if host.regs[3] != fakeArgVal {
		t.Errorf("R(3) = 0x%016x, want 0x%016x (fakeArg from R(1))", host.regs[3], fakeArgVal)
	}
	if host.callCalls != 1 {
		t.Errorf("CallBaseline called %d times, want 1", host.callCalls)
	}
}

// TestPJ5_AnalyzeCallVoidForm_RecognizeRetGetter verifies recognition of form BR1
// (0 args 1 return, getter): GETUPVAL + CALL B=1 C=2 + RETURN A=0 B=2 + dead RETURN.
func TestPJ5_AnalyzeCallVoidForm_RecognizeRetGetter(t *testing.T) {
	// Form BR1: GETUPVAL 0 0; CALL 0 1 2; RETURN 0 2; RETURN 0 1
	//   - GETUPVAL.A=0 (callee slot) GETUPVAL.B=0 (upvalue index)
	//   - CALL.A=0 CALL.B=1 (0 args) CALL.C=2 (1 return value)
	//   - RETURN.A=0 RETURN.B=2 (1 return value, returns R(0)=callee's return value)
	//   - dead RETURN (luac usually appends it, but not when length=3)
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.GETUPVAL, 0, 0, 0),
			bytecode.EncodeABC(bytecode.CALL, 0, 1, 2),
			bytecode.EncodeABC(bytecode.RETURN, 0, 2, 0),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
	}
	info, ok := analyzeCallVoidForm(proto)
	if !ok {
		t.Fatal("analyzeCallVoidForm should accept GETUPVAL+CALL+RETURN+dead RETURN getter form")
	}
	if !info.isCallVoid {
		t.Error("info.isCallVoid should be true")
	}
	if info.retA != 0 || info.retB != 2 {
		t.Errorf("retA/retB = %d/%d, want 0/2 (getter: 返 R(callA)=R(0))", info.retA, info.retB)
	}
	if info.callA != 0 || info.callB != 1 || info.callC != 2 {
		t.Errorf("callA/B/C = %d/%d/%d, want 0/1/2", info.callA, info.callB, info.callC)
	}
	if info.callArgCount != 0 {
		t.Errorf("callArgCount = %d, want 0", info.callArgCount)
	}
	if info.retPC != 2 {
		t.Errorf("retPC = %d, want 2 (RETURN in pc 2)", info.retPC)
	}
}
