//go:build wangshu_p4

package jit

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/value"
)

// compiler_pj5_tailcall_test.go —— PJ5 TAILCALL 形态识别 + Run prelude 路径
// 单测(承 docs/design/p4-method-jit/05-system-pipeline.md §4.3 + 09 §9.14
// PJ5 TAILCALL 真接入主路径)。
//
// 形态(luac stmtReturn 单 CallExpr 快路径产物):
//   - 长度 4(0 参):MOVE/GETUPVAL + TAILCALL B=1 C=0 + dead RETURN B=0 + 隐式 RETURN B=1
//   - 长度 5(1 K 参):... + LOADK + TAILCALL B=2 C=0 + ...
//   - 长度 5(1 reg 参):... + MOVE + TAILCALL B=2 C=0 + ...
//   - 长度 6(2 K 参):... + LOADK + LOADK + TAILCALL B=3 C=0 + ...

// TestPJ5_AnalyzeTailCallForm_Recognize 验形态 TA0(parameter callee,0 参)识别。
func TestPJ5_AnalyzeTailCallForm_Recognize(t *testing.T) {
	// 形态 TA0:MOVE 1 0; TAILCALL 1 1 0; RETURN 1 0 0; RETURN 0 1 0
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0),
			bytecode.EncodeABC(bytecode.TAILCALL, 1, 1, 0),
			bytecode.EncodeABC(bytecode.RETURN, 1, 0, 0),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
	}
	info, ok := analyzeTailCallForm(proto)
	if !ok {
		t.Fatal("analyzeTailCallForm should accept MOVE+TAILCALL+RETURN B=0+RETURN B=1 form TA0")
	}
	if !info.isTailCall {
		t.Error("info.isTailCall should be true")
	}
	if info.isCallUpval {
		t.Error("info.isCallUpval should be false for MOVE form TA0")
	}
	if info.callA != 1 || info.callB != 1 || info.callC != 0 {
		t.Errorf("callA/B/C = %d/%d/%d, want 1/1/0", info.callA, info.callB, info.callC)
	}
	// retPC 指 dead RETURN(pc 2),retA=callA=1,retB=0(多值 to-top)
	if info.retA != 1 || info.retB != 0 || info.retPC != 2 {
		t.Errorf("retA/B/PC = %d/%d/%d, want 1/0/2", info.retA, info.retB, info.retPC)
	}
	if info.preludeOp != uint8(bytecode.TAILCALL) {
		t.Errorf("preludeOp = %d, want %d (TAILCALL)", info.preludeOp, bytecode.TAILCALL)
	}
	if info.preludeArg != 0 {
		t.Errorf("preludeArg = %d, want 0 (MOVE.B)", info.preludeArg)
	}
}

// TestPJ5_AnalyzeTailCallForm_RecognizeUpval 验形态 TB0(upvalue callee,0 参)识别。
func TestPJ5_AnalyzeTailCallForm_RecognizeUpval(t *testing.T) {
	// 形态 TB0:GETUPVAL 0 3; TAILCALL 0 1 0; RETURN 0 0 0; RETURN 0 1 0
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.GETUPVAL, 0, 3, 0),
			bytecode.EncodeABC(bytecode.TAILCALL, 0, 1, 0),
			bytecode.EncodeABC(bytecode.RETURN, 0, 0, 0),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
	}
	info, ok := analyzeTailCallForm(proto)
	if !ok {
		t.Fatal("analyzeTailCallForm should accept GETUPVAL+TAILCALL+RETURN form TB0")
	}
	if !info.isTailCall {
		t.Error("info.isTailCall should be true")
	}
	if !info.isCallUpval {
		t.Error("info.isCallUpval should be true for GETUPVAL form TB0")
	}
	if info.callA != 0 {
		t.Errorf("callA = %d, want 0", info.callA)
	}
	if info.preludeArg != 3 {
		t.Errorf("preludeArg = %d, want 3 (GETUPVAL.B)", info.preludeArg)
	}
}

// TestPJ5_AnalyzeTailCallForm_Recognize1ArgK 验形态 TB1K(1 K 参)识别。
func TestPJ5_AnalyzeTailCallForm_Recognize1ArgK(t *testing.T) {
	// 形态 TB1K:GETUPVAL 0 1; LOADK 1 0; TAILCALL 0 2 0; RETURN 0 0 0; RETURN 0 1 0
	const kVal uint64 = 0x4040000000000000
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.GETUPVAL, 0, 1, 0),
			bytecode.EncodeABx(bytecode.LOADK, 1, 0),
			bytecode.EncodeABC(bytecode.TAILCALL, 0, 2, 0),
			bytecode.EncodeABC(bytecode.RETURN, 0, 0, 0),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
		Consts: []value.Value{value.Value(kVal)},
	}
	info, ok := analyzeTailCallForm(proto)
	if !ok {
		t.Fatal("analyzeTailCallForm should accept form TB1K")
	}
	if info.callArgCount != 1 {
		t.Errorf("callArgCount = %d, want 1", info.callArgCount)
	}
	if !info.callArg1IsK {
		t.Error("callArg1IsK should be true")
	}
	if info.callArg1K != kVal {
		t.Errorf("callArg1K = 0x%016x, want 0x%016x", info.callArg1K, kVal)
	}
	if info.retPC != 3 {
		t.Errorf("retPC = %d, want 3 (dead RETURN pc 3)", info.retPC)
	}
	if info.callB != 2 {
		t.Errorf("callB = %d, want 2 (1 arg)", info.callB)
	}
}

// TestPJ5_AnalyzeTailCallForm_Recognize1ArgReg 验形态 TB1R(1 reg 参)识别。
func TestPJ5_AnalyzeTailCallForm_Recognize1ArgReg(t *testing.T) {
	// 形态 TB1R:GETUPVAL 1 0; MOVE 2 0; TAILCALL 1 2 0; RETURN 1 0 0; RETURN 0 1 0
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.GETUPVAL, 1, 0, 0),
			bytecode.EncodeABC(bytecode.MOVE, 2, 0, 0),
			bytecode.EncodeABC(bytecode.TAILCALL, 1, 2, 0),
			bytecode.EncodeABC(bytecode.RETURN, 1, 0, 0),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
	}
	info, ok := analyzeTailCallForm(proto)
	if !ok {
		t.Fatal("analyzeTailCallForm should accept form TB1R")
	}
	if info.callArgCount != 1 {
		t.Errorf("callArgCount = %d, want 1", info.callArgCount)
	}
	if info.callArg1IsK {
		t.Error("callArg1IsK should be false (reg form)")
	}
	if info.callArg1RegSrc != 0 {
		t.Errorf("callArg1RegSrc = %d, want 0 (MOVE.B)", info.callArg1RegSrc)
	}
	if info.callA != 1 {
		t.Errorf("callA = %d, want 1", info.callA)
	}
}

// TestPJ5_AnalyzeTailCallForm_Recognize2ArgK 验形态 TB2K(2 K 参)识别。
func TestPJ5_AnalyzeTailCallForm_Recognize2ArgK(t *testing.T) {
	const k1, k2 uint64 = 0x4014000000000000, 0x4022000000000000 // 5.0, 9.0
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.GETUPVAL, 0, 0, 0),
			bytecode.EncodeABx(bytecode.LOADK, 1, 0),
			bytecode.EncodeABx(bytecode.LOADK, 2, 1),
			bytecode.EncodeABC(bytecode.TAILCALL, 0, 3, 0),
			bytecode.EncodeABC(bytecode.RETURN, 0, 0, 0),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
		Consts: []value.Value{value.Value(k1), value.Value(k2)},
	}
	info, ok := analyzeTailCallForm(proto)
	if !ok {
		t.Fatal("analyzeTailCallForm should accept form TB2K")
	}
	if info.callArgCount != 2 {
		t.Errorf("callArgCount = %d, want 2", info.callArgCount)
	}
	if info.callArg1K != k1 || info.callArg2K != k2 {
		t.Errorf("callArg1K/2K = 0x%x/0x%x, want 0x%x/0x%x",
			info.callArg1K, info.callArg2K, k1, k2)
	}
	if info.callB != 3 {
		t.Errorf("callB = %d, want 3 (2 args)", info.callB)
	}
	if info.retPC != 4 {
		t.Errorf("retPC = %d, want 4 (dead RETURN pc 4)", info.retPC)
	}
}

// TestPJ5_AnalyzeTailCallForm_Reject 验负向拒识(各类不匹配场景)。
func TestPJ5_AnalyzeTailCallForm_Reject(t *testing.T) {
	cases := []struct {
		name string
		code []bytecode.Instruction
	}{
		{
			name: "wrong length 3",
			code: []bytecode.Instruction{
				bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0),
				bytecode.EncodeABC(bytecode.TAILCALL, 1, 1, 0),
				bytecode.EncodeABC(bytecode.RETURN, 1, 0, 0),
			},
		},
		{
			name: "first op not MOVE/GETUPVAL",
			code: []bytecode.Instruction{
				bytecode.EncodeABC(bytecode.LOADBOOL, 0, 1, 0),
				bytecode.EncodeABC(bytecode.TAILCALL, 0, 1, 0),
				bytecode.EncodeABC(bytecode.RETURN, 0, 0, 0),
				bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
			},
		},
		{
			name: "second op not TAILCALL",
			code: []bytecode.Instruction{
				bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0),
				bytecode.EncodeABC(bytecode.CALL, 1, 1, 1),
				bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
				bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
			},
		},
		{
			name: "TAILCALL.C non-zero",
			code: []bytecode.Instruction{
				bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0),
				bytecode.EncodeABC(bytecode.TAILCALL, 1, 1, 1), // C=1
				bytecode.EncodeABC(bytecode.RETURN, 1, 0, 0),
				bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
			},
		},
		{
			name: "dead RETURN.B not 0",
			code: []bytecode.Instruction{
				bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0),
				bytecode.EncodeABC(bytecode.TAILCALL, 1, 1, 0),
				bytecode.EncodeABC(bytecode.RETURN, 1, 2, 0), // B=2
				bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
			},
		},
		{
			name: "implicit RETURN.B not 1",
			code: []bytecode.Instruction{
				bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0),
				bytecode.EncodeABC(bytecode.TAILCALL, 1, 1, 0),
				bytecode.EncodeABC(bytecode.RETURN, 1, 0, 0),
				bytecode.EncodeABC(bytecode.RETURN, 0, 2, 0), // B=2
			},
		},
		{
			name: "MOVE.A != TAILCALL.A",
			code: []bytecode.Instruction{
				bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0),
				bytecode.EncodeABC(bytecode.TAILCALL, 2, 1, 0), // A=2
				bytecode.EncodeABC(bytecode.RETURN, 2, 0, 0),
				bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
			},
		},
		{
			name: "TAILCALL.B mismatch argCount",
			code: []bytecode.Instruction{
				bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0),
				bytecode.EncodeABC(bytecode.TAILCALL, 1, 3, 0), // B=3 but 0 args
				bytecode.EncodeABC(bytecode.RETURN, 1, 0, 0),
				bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
			},
		},
		{
			name: "dead RETURN.A != callA",
			code: []bytecode.Instruction{
				bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0),
				bytecode.EncodeABC(bytecode.TAILCALL, 1, 1, 0),
				bytecode.EncodeABC(bytecode.RETURN, 0, 0, 0), // A=0
				bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			proto := &bytecode.Proto{Code: c.code}
			if _, ok := analyzeTailCallForm(proto); ok {
				t.Errorf("expected analyzeTailCallForm to reject form %q", c.name)
			}
		})
	}
}

// TestPJ5_RunTailCallPath 验形态 TB0 端到端 Run 路径:
//   - 装载 R(callA)=upval + 调 host.TailCall(返 0=Lua 尾完成 → 跳过 DoReturn)
func TestPJ5_RunTailCallPath(t *testing.T) {
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.GETUPVAL, 0, 1, 0),
			bytecode.EncodeABC(bytecode.TAILCALL, 0, 1, 0),
			bytecode.EncodeABC(bytecode.RETURN, 0, 0, 0),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
	}
	gc, host := compileWithHost(t, proto)
	defer tryDispose(t, gc)

	const fakeFuncVal uint64 = 0xFFF9_BEEF_0001_0000
	host.upvals[1] = fakeFuncVal
	host.tailCallRetCode = 0 // Lua 尾完成

	stack := make([]uint64, 4)
	status := gc.Run(stack, 0)
	if status != 0 {
		t.Errorf("Run status = %d, want 0", status)
	}
	got := host.regs[0]
	if got != fakeFuncVal {
		t.Errorf("R(0) = 0x%016x, want 0x%016x (upval load)", got, fakeFuncVal)
	}
	if host.tailCallCalls != 1 {
		t.Errorf("TailCall called %d times, want 1", host.tailCallCalls)
	}
	if host.lastTailCallA != 0 || host.lastTailCallB != 1 || host.lastTailCallC != 0 {
		t.Errorf("TailCall(A,B,C) = (%d,%d,%d), want (0,1,0)",
			host.lastTailCallA, host.lastTailCallB, host.lastTailCallC)
	}
	// Lua 尾完成路径不调 DoReturn(本帧已弹)
	if host.doReturnCalls != 0 {
		t.Errorf("DoReturn called %d times (Lua 尾完成应跳过 DoReturn,want 0)", host.doReturnCalls)
	}
}

// TestPJ5_RunTailCallHostPath 验三态 = 2(host 尾完成)路径:fall through 走
// 末尾 DoReturn(retB=0 多值 to-top)。
func TestPJ5_RunTailCallHostPath(t *testing.T) {
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.GETUPVAL, 0, 1, 0),
			bytecode.EncodeABC(bytecode.TAILCALL, 0, 1, 0),
			bytecode.EncodeABC(bytecode.RETURN, 0, 0, 0),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
	}
	gc, host := compileWithHost(t, proto)
	defer tryDispose(t, gc)

	host.upvals[1] = 0xDEAD_BEEF_0000_0000
	host.tailCallRetCode = 2 // host 尾完成

	stack := make([]uint64, 4)
	status := gc.Run(stack, 0)
	if status != 0 {
		t.Errorf("Run status = %d, want 0", status)
	}
	if host.tailCallCalls != 1 {
		t.Errorf("TailCall called %d times, want 1", host.tailCallCalls)
	}
	// host 尾完成路径走 DoReturn 弹本帧(retB=0 多值 to-top)
	if host.doReturnCalls != 1 {
		t.Errorf("DoReturn called %d times, want 1 (host 尾完成路径)", host.doReturnCalls)
	}
}

// TestPJ5_RunTailCallErrPath 验三态 = 1(ERR)路径:Run 直接 return 1。
func TestPJ5_RunTailCallErrPath(t *testing.T) {
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.GETUPVAL, 0, 1, 0),
			bytecode.EncodeABC(bytecode.TAILCALL, 0, 1, 0),
			bytecode.EncodeABC(bytecode.RETURN, 0, 0, 0),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
	}
	gc, host := compileWithHost(t, proto)
	defer tryDispose(t, gc)

	host.upvals[1] = 0xCAFE_F00D_0000_0000
	host.tailCallRetCode = 1 // ERR

	stack := make([]uint64, 4)
	status := gc.Run(stack, 0)
	if status != 1 {
		t.Errorf("Run status = %d, want 1 (ERR)", status)
	}
	if host.doReturnCalls != 0 {
		t.Errorf("DoReturn called %d times (ERR 路径不弹帧 caller 端处理,want 0)", host.doReturnCalls)
	}
}

// TestPJ5_RunTailCall1ArgK 验形态 TB1K Run 路径:K 参装到 R(callA+1)。
func TestPJ5_RunTailCall1ArgK(t *testing.T) {
	const kVal uint64 = 0x4040000000000000
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.GETUPVAL, 0, 1, 0),
			bytecode.EncodeABx(bytecode.LOADK, 1, 0),
			bytecode.EncodeABC(bytecode.TAILCALL, 0, 2, 0),
			bytecode.EncodeABC(bytecode.RETURN, 0, 0, 0),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
		Consts: []value.Value{value.Value(kVal)},
	}
	gc, host := compileWithHost(t, proto)
	defer tryDispose(t, gc)

	const fakeFuncVal uint64 = 0xFFF9_BEEF_0002_0000
	host.upvals[1] = fakeFuncVal
	host.tailCallRetCode = 0

	stack := make([]uint64, 4)
	if status := gc.Run(stack, 0); status != 0 {
		t.Errorf("Run status = %d, want 0", status)
	}
	if host.regs[0] != fakeFuncVal {
		t.Errorf("R(0) = 0x%016x, want 0x%016x (upval load)", host.regs[0], fakeFuncVal)
	}
	if host.regs[1] != kVal {
		t.Errorf("R(1) = 0x%016x, want 0x%016x (K arg)", host.regs[1], kVal)
	}
	if host.tailCallCalls != 1 || host.lastTailCallB != 2 {
		t.Errorf("TailCall calls=%d B=%d, want 1/2", host.tailCallCalls, host.lastTailCallB)
	}
}

// TestPJ5_SupportsAllOpcodesGate_AcceptsTailCall 验 F7 闸门承认 TAILCALL 形态。
func TestPJ5_SupportsAllOpcodesGate_AcceptsTailCall(t *testing.T) {
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.GETUPVAL, 0, 1, 0),
			bytecode.EncodeABC(bytecode.TAILCALL, 0, 1, 0),
			bytecode.EncodeABC(bytecode.RETURN, 0, 0, 0),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
	}
	c := New()
	if !c.SupportsAllOpcodes(proto) {
		t.Error("SupportsAllOpcodes should accept TAILCALL form TB0")
	}
}

// TestPJ5_SpecTailCallHits 验 Compile 命中 PJ5 TAILCALL 形态时 specTailCallHits
// 探针 ++(白盒 prove-the-path 证据,承 llmdoc/guides/prove-the-path-under-test §4)。
func TestPJ5_SpecTailCallHits(t *testing.T) {
	ResetSpecHits()
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.GETUPVAL, 0, 1, 0),
			bytecode.EncodeABC(bytecode.TAILCALL, 0, 1, 0),
			bytecode.EncodeABC(bytecode.RETURN, 0, 0, 0),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
	}
	gc, _ := compileWithHost(t, proto)
	defer tryDispose(t, gc)
	if got := SpecTailCallHits(); got != 1 {
		t.Errorf("SpecTailCallHits = %d, want 1 (Compile 应命中 PJ5 TAILCALL inline)", got)
	}
	// CallVoidHits 不应被本路径误增
	if got := SpecCallVoidHits(); got != 0 {
		t.Errorf("SpecCallVoidHits = %d, want 0 (TAILCALL 形态不应误命中 CallVoid)", got)
	}
}
