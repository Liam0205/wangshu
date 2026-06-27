//go:build wangshu_p4

package jit

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/bytecode"
)

// TestPJ5_AnalyzeCallVoidForm_Recognize 验 analyzeCallVoidForm 正向识别
// MOVE+CALL+RETURN void 三 op 单 BB(`function(g) g() end` 类)。
func TestPJ5_AnalyzeCallVoidForm_Recognize(t *testing.T) {
	// 形态:MOVE 1 0; CALL 1 1 1; RETURN 0 1
	//  - MOVE.A=1 (被调位) MOVE.B=0 (参数源,即函数参数 g 槽)
	//  - CALL.A=1 (被调位与 MOVE.A 一致) CALL.B=1 (0 参) CALL.C=1 (0 返)
	//  - RETURN.A=0 RETURN.B=1 (0 返值)
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
	if info.callA != 1 || info.callB != 1 || info.callC != 1 {
		t.Errorf("callA/B/C = %d/%d/%d, want 1/1/1", info.callA, info.callB, info.callC)
	}
	if info.retA != 0 || info.retB != 1 || info.retPC != 2 {
		t.Errorf("retA/B/PC = %d/%d/%d, want 0/1/2", info.retA, info.retB, info.retPC)
	}
	if info.preludeOp != uint8(bytecode.CALL) {
		t.Errorf("preludeOp = %d, want %d (CALL)", info.preludeOp, bytecode.CALL)
	}
}

// TestPJ5_AnalyzeCallVoidForm_Reject 验形态守卫拒非简化形态。
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

// TestPJ5_RunCallVoidPath 验 Run prelude CALL case 端到端:
//   - mmap 段执行(dummy mov rax,0;ret),Run 走 prelude switch CALL case
//   - 预处理 MOVE:host.GetReg(0) + SetReg(1) 把被调函数从 R(0) 拷到 R(1)
//   - host.CallBaseline 被调用,callA=1/callB=1/callC=1/pc=1(CALL 自身 pc)
//   - host.DoReturn 调一次(retPC=2/retA=0/retB=1)
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

	// 预设 R(0) = fake function NaN-box,Run 应当把它 SetReg 到 R(1)。
	const fakeFuncVal uint64 = 0xFFF9_DEAD_BEEF_0000
	host.regs[0] = fakeFuncVal

	stack := make([]uint64, 4)
	status := gc.Run(stack, 0)
	if status != 0 {
		t.Errorf("Run status = %d, want 0 (OK)", status)
	}
	// MOVE 预处理:R(1) 应是 R(0) 的 fakeFuncVal
	got, ok := host.regs[1]
	if !ok {
		t.Fatal("SetReg(1, ...) not called by MOVE preprocess")
	}
	if got != fakeFuncVal {
		t.Errorf("R(1) = 0x%016x, want 0x%016x (fakeFunc)", got, fakeFuncVal)
	}
	// host.CallBaseline 路径
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
	// host.DoReturn 路径
	if host.doReturnCalls != 1 {
		t.Errorf("DoReturn called %d times, want 1", host.doReturnCalls)
	}
	if host.lastReturnPC != 2 || host.lastReturnA != 0 || host.lastReturnB != 1 {
		t.Errorf("DoReturn(pc,A,B) = (%d,%d,%d), want (2,0,1)",
			host.lastReturnPC, host.lastReturnA, host.lastReturnB)
	}
}

// TestPJ5_RunCallVoidErrPropagate 验 host.CallBaseline 返 ERR=1 时 Run 直接
// 返 1 + 不调 DoReturn(ERR 路径不弹帧,由上层 raiseGibbous + enterGibbous 取走)。
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

	host.callRetCode = 1 // 模拟 ERR
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

// TestPJ5_SupportsAllOpcodesGate_AcceptsCallVoid 验 SupportsAllOpcodes 接受
// MOVE+CALL+RETURN void 形态(F7 闸门承认本形态)。
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
