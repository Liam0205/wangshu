//go:build wangshu_p4

package jit

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/bridge"
	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/value"
)

// PJ5 SELF method call inline form-recognition unit tests. Coverage:
//   - length 4: 0-arg 0-ret SELF + CALL void / SELF + TAILCALL
//   - length 5: 0-arg 1-ret SELF + CALL getter / 1 K/reg-arg SELF + CALL void /
//     1 K/reg-arg SELF + TAILCALL
//   - length 6: 1 K/reg-arg SELF + CALL getter 1-ret / 2 K/reg-arg SELF + CALL void /
//     2 K/reg-arg SELF + TAILCALL
//
// Both receiver forms (M*=MOVE reg / U*=GETUPVAL upval) are covered pairwise.

// TestPJ5_AnalyzeSelfCallForm_M0_VoidCall form M0:
// MOVE+SELF+CALL+RETURN void (`function(o) o:m() end`).
func TestPJ5_AnalyzeSelfCallForm_M0_VoidCall(t *testing.T) {
	// MOVE 1 0;        // R(1) = R(0) recv
	// SELF 1 1 256;    // R(1)=R(1)[K0]; R(2)=R(1) self  (C=256 i.e. K(0))
	// CALL 1 2 1;      // 0 args 0 rets
	// RETURN 0 1
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0),
			bytecode.EncodeABC(bytecode.SELF, 1, 1, 256),
			bytecode.EncodeABC(bytecode.CALL, 1, 2, 1),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
	}
	info, ok := analyzeSelfCallForm(proto)
	if !ok {
		t.Fatal("analyzeSelfCallForm should accept MOVE+SELF+CALL+RETURN void form M0")
	}
	if !info.isSelfCall || !info.isCallVoid {
		t.Errorf("flags = isSelfCall=%v isCallVoid=%v, want both true", info.isSelfCall, info.isCallVoid)
	}
	if info.isCallUpval {
		t.Error("isCallUpval should be false for MOVE recv form")
	}
	if info.callA != 1 || info.callB != 2 || info.callC != 1 {
		t.Errorf("callA/B/C = %d/%d/%d, want 1/2/1", info.callA, info.callB, info.callC)
	}
	if info.callArgCount != 0 {
		t.Errorf("callArgCount = %d, want 0", info.callArgCount)
	}
	if info.selfCallA != 1 || info.selfMethodRK != 256 {
		t.Errorf("selfCallA/MethodRK = %d/%d, want 1/256", info.selfCallA, info.selfMethodRK)
	}
	if info.selfRecvIsUpval {
		t.Error("selfRecvIsUpval should be false for MOVE recv form")
	}
	if info.selfRecvSrcReg != 0 {
		t.Errorf("selfRecvSrcReg = %d, want 0 (MOVE.B)", info.selfRecvSrcReg)
	}
}

// TestPJ5_AnalyzeSelfCallForm_U0_VoidCall form U0:
// GETUPVAL+SELF+CALL+RETURN void (`function() o:m() end`, o is an upval).
func TestPJ5_AnalyzeSelfCallForm_U0_VoidCall(t *testing.T) {
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.GETUPVAL, 0, 2, 0), // upval[2] → R(0)
			bytecode.EncodeABC(bytecode.SELF, 0, 0, 257),   // K(1) method
			bytecode.EncodeABC(bytecode.CALL, 0, 2, 1),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
	}
	info, ok := analyzeSelfCallForm(proto)
	if !ok {
		t.Fatal("analyzeSelfCallForm should accept GETUPVAL+SELF+CALL+RETURN void form U0")
	}
	if !info.isSelfCall || !info.isCallVoid || !info.isCallUpval {
		t.Errorf("flags = isSelfCall=%v isCallVoid=%v isCallUpval=%v, want all true",
			info.isSelfCall, info.isCallVoid, info.isCallUpval)
	}
	if !info.selfRecvIsUpval {
		t.Error("selfRecvIsUpval should be true for GETUPVAL recv form")
	}
	if info.selfRecvSrcReg != 2 {
		t.Errorf("selfRecvSrcReg = %d, want 2 (upval idx)", info.selfRecvSrcReg)
	}
	if info.selfMethodRK != 257 {
		t.Errorf("selfMethodRK = %d, want 257", info.selfMethodRK)
	}
}

// TestPJ5_AnalyzeSelfCallForm_M0_TailCall form TM0:
// MOVE+SELF+TAILCALL+RETURN (B=0 dead) (`function(o) return o:m() end`).
func TestPJ5_AnalyzeSelfCallForm_M0_TailCall(t *testing.T) {
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0),
			bytecode.EncodeABC(bytecode.SELF, 1, 1, 256),
			bytecode.EncodeABC(bytecode.TAILCALL, 1, 2, 0),
			bytecode.EncodeABC(bytecode.RETURN, 1, 0, 0),
		},
	}
	info, ok := analyzeSelfCallForm(proto)
	if !ok {
		t.Fatal("analyzeSelfCallForm should accept MOVE+SELF+TAILCALL form TM0")
	}
	if !info.isSelfCall || !info.isTailCall {
		t.Errorf("flags = isSelfCall=%v isTailCall=%v, want both true", info.isSelfCall, info.isTailCall)
	}
	if info.isCallVoid {
		t.Error("isCallVoid should be false for TAILCALL form")
	}
	if info.callArgCount != 0 {
		t.Errorf("callArgCount = %d, want 0", info.callArgCount)
	}
}

// TestPJ5_AnalyzeSelfCallForm_M0_GetterCall form MR1:
// MOVE+SELF+CALL+RETURN(callA,2)+RETURN(0,1) 0-arg 1-ret
// (`function(o) return o:m() end` compiled by luac takes the SubProto main path
// — measured as TAILCALL, but this synthetic driver validates the form).
func TestPJ5_AnalyzeSelfCallForm_M0_GetterCall(t *testing.T) {
	// MOVE 1 0;        SELF 1 1 256;
	// CALL 1 2 2;      // 0 args 1 ret
	// RETURN 1 2;
	// RETURN 0 1;
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0),
			bytecode.EncodeABC(bytecode.SELF, 1, 1, 256),
			bytecode.EncodeABC(bytecode.CALL, 1, 2, 2),
			bytecode.EncodeABC(bytecode.RETURN, 1, 2, 0),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
	}
	info, ok := analyzeSelfCallForm(proto)
	if !ok {
		t.Fatal("analyzeSelfCallForm should accept SELF+CALL getter 0-arg 1-ret form MR1")
	}
	if !info.isSelfCall || !info.isCallVoid {
		t.Errorf("flags isSelfCall=%v isCallVoid=%v, want both true", info.isSelfCall, info.isCallVoid)
	}
	if info.callArgCount != 0 {
		t.Errorf("callArgCount = %d, want 0", info.callArgCount)
	}
	if info.retA != 1 || info.retB != 2 {
		t.Errorf("retA/retB = %d/%d, want 1/2", info.retA, info.retB)
	}
}

// TestPJ5_AnalyzeSelfCallForm_M1K_VoidCall form M1K:
// MOVE+SELF+LOADK+CALL+RETURN void (`function(o) o:m(1) end`) 1 K-arg 0-ret.
func TestPJ5_AnalyzeSelfCallForm_M1K_VoidCall(t *testing.T) {
	// MOVE 1 0;     SELF 1 1 256 (K0=method);
	// LOADK 3 1 (K1=1);  CALL 1 3 1;  RETURN 0 1
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0),
			bytecode.EncodeABC(bytecode.SELF, 1, 1, 256),
			bytecode.EncodeABx(bytecode.LOADK, 3, 1),
			bytecode.EncodeABC(bytecode.CALL, 1, 3, 1),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
		Consts: []value.Value{value.Value(0x1), value.Value(0x42)},
	}
	info, ok := analyzeSelfCallForm(proto)
	if !ok {
		t.Fatal("analyzeSelfCallForm should accept MOVE+SELF+LOADK+CALL void form M1K")
	}
	if !info.isSelfCall || !info.isCallVoid {
		t.Errorf("flags isSelfCall=%v isCallVoid=%v", info.isSelfCall, info.isCallVoid)
	}
	if info.callArgCount != 1 {
		t.Errorf("callArgCount = %d, want 1", info.callArgCount)
	}
	if !info.callArg1IsK {
		t.Error("callArg1IsK should be true (LOADK form)")
	}
	if info.callArg1K != 0x42 {
		t.Errorf("callArg1K = %#x, want 0x42", info.callArg1K)
	}
}

// TestPJ5_AnalyzeSelfCallForm_M1R_VoidCall form M1R:
// MOVE+SELF+MOVE+CALL+RETURN void (`function(o,a) o:m(a) end`) 1 reg-arg 0-ret.
func TestPJ5_AnalyzeSelfCallForm_M1R_VoidCall(t *testing.T) {
	// Note: luac actually compiles `function(o,a) o:m(a) end` as
	// MOVE 2 0;SELF 2 2 256;MOVE 4 1;CALL 2 3 1;RETURN 0 1
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.MOVE, 2, 0, 0),
			bytecode.EncodeABC(bytecode.SELF, 2, 2, 256),
			bytecode.EncodeABC(bytecode.MOVE, 4, 1, 0),
			bytecode.EncodeABC(bytecode.CALL, 2, 3, 1),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
	}
	info, ok := analyzeSelfCallForm(proto)
	if !ok {
		t.Fatal("analyzeSelfCallForm should accept MOVE+SELF+MOVE+CALL void form M1R")
	}
	if info.callArgCount != 1 {
		t.Errorf("callArgCount = %d, want 1", info.callArgCount)
	}
	if info.callArg1IsK {
		t.Error("callArg1IsK should be false (MOVE form)")
	}
	if info.callArg1RegSrc != 1 {
		t.Errorf("callArg1RegSrc = %d, want 1 (MOVE.B)", info.callArg1RegSrc)
	}
}

// TestPJ5_AnalyzeSelfCallForm_RejectShortCode rejects recognition when length < 4.
func TestPJ5_AnalyzeSelfCallForm_RejectShortCode(t *testing.T) {
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
	}
	if _, ok := analyzeSelfCallForm(proto); ok {
		t.Error("analyzeSelfCallForm should reject length < 4")
	}
}

// TestPJ5_AnalyzeSelfCallForm_RejectNoSelf rejects the form when [1] != SELF.
func TestPJ5_AnalyzeSelfCallForm_RejectNoSelf(t *testing.T) {
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0),
			bytecode.EncodeABC(bytecode.GETTABLE, 1, 1, 256), // not SELF
			bytecode.EncodeABC(bytecode.CALL, 1, 2, 1),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
	}
	if _, ok := analyzeSelfCallForm(proto); ok {
		t.Error("analyzeSelfCallForm should reject when [1] != SELF")
	}
}

// TestPJ5_AnalyzeSelfCallForm_RejectMethodReg rejects SELF.C < 256 (reg form).
func TestPJ5_AnalyzeSelfCallForm_RejectMethodReg(t *testing.T) {
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0),
			bytecode.EncodeABC(bytecode.SELF, 1, 1, 0), // C=0 i.e. R(0)
			bytecode.EncodeABC(bytecode.CALL, 1, 2, 1),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
	}
	if _, ok := analyzeSelfCallForm(proto); ok {
		t.Error("analyzeSelfCallForm should reject SELF.C < 256 (reg method name)")
	}
}

// TestPJ5_AnalyzeSelfCallSpecForm_M0 verifies analyzeSelfCallSpecForm recognizes the
// length-4 SELF + CALL void 0-arg form + IC NodeHit feedback hit → useSpecSelfCall=true.
//
// Form: MOVE 1 0; SELF 1 1 256; CALL 1 2 1; RETURN 0 1
// IC[1] (SELF pc) = NodeHit + feedback.Points[1] = FBTableMono.
func TestPJ5_AnalyzeSelfCallSpecForm_M0(t *testing.T) {
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0),
			bytecode.EncodeABC(bytecode.SELF, 1, 1, 256), // C=256 → K[0]
			bytecode.EncodeABC(bytecode.CALL, 1, 2, 1),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
		IC:     make([]bytecode.ICSlot, 4),
		Consts: []value.Value{value.Value(0x42)}, // K[0] method key (non-Nil)
	}
	// IC[1] = SELF's IC slot: NodeHit + Shape=7 + Index=2
	proto.IC[1] = bytecode.ICSlot{Kind: bytecode.ICKindNodeHit, Shape: 7, Index: 2}

	// feedback.Points[1] aligned to SELF pc=1
	feedback := &bridge.TypeFeedback{
		Points: []bridge.PointFeedback{
			{}, // Points[0] dummy (MOVE pc=0)
			{Kind: bridge.FBSelfMono, Confidence: 1.0, StableShape: 7, StableIndex: 2},
		},
	}

	info, ok := analyzeSelfCallSpecForm(proto, feedback)
	if !ok {
		t.Fatal("analyzeSelfCallSpecForm 应返 true(SELF + CALL void + IC NodeHit + feedback mono)")
	}
	if !info.useSpecSelfCall {
		t.Error("info.useSpecSelfCall 应为 true")
	}
	if !info.isSelfCall || !info.isCallVoid {
		t.Errorf("isSelfCall=%v isCallVoid=%v, want both true", info.isSelfCall, info.isCallVoid)
	}
	if info.icAReg != 1 || info.icBReg != 1 {
		t.Errorf("icAReg/icBReg = %d/%d, want 1/1", info.icAReg, info.icBReg)
	}
	if info.icStableShape != 7 || info.icStableIndex != 2 {
		t.Errorf("stableShape/Index = %d/%d, want 7/2", info.icStableShape, info.icStableIndex)
	}
	if info.icStableKey != 0x42 {
		t.Errorf("icStableKey = %#x, want 0x42", info.icStableKey)
	}
	if info.callA != 1 || info.callB != 2 || info.callC != 1 {
		t.Errorf("callA/B/C = %d/%d/%d, want 1/2/1", info.callA, info.callB, info.callC)
	}
}

// TestPJ5_AnalyzeSelfCallSpecForm_RejectNoFeedback rejects when there is no feedback (falls back to the plain host.Self path).
func TestPJ5_AnalyzeSelfCallSpecForm_RejectNoFeedback(t *testing.T) {
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0),
			bytecode.EncodeABC(bytecode.SELF, 1, 1, 256),
			bytecode.EncodeABC(bytecode.CALL, 1, 2, 1),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
		IC:     make([]bytecode.ICSlot, 4),
		Consts: []value.Value{value.Value(0x42)},
	}
	proto.IC[1] = bytecode.ICSlot{Kind: bytecode.ICKindNodeHit, Shape: 7, Index: 2}
	if _, ok := analyzeSelfCallSpecForm(proto, nil); ok {
		t.Error("analyzeSelfCallSpecForm 无 feedback 应返 false")
	}
}

// TestPJ5_AnalyzeSelfCallSpecForm_RejectNoNodeHit rejects when the IC is not NodeHit.
func TestPJ5_AnalyzeSelfCallSpecForm_RejectNoNodeHit(t *testing.T) {
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0),
			bytecode.EncodeABC(bytecode.SELF, 1, 1, 256),
			bytecode.EncodeABC(bytecode.CALL, 1, 2, 1),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
		IC:     make([]bytecode.ICSlot, 4),
		Consts: []value.Value{value.Value(0x42)},
	}
	// IC[1] = ArrayHit (not NodeHit)
	proto.IC[1] = bytecode.ICSlot{Kind: bytecode.ICKindArrayHit, Shape: 7, Index: 2}
	feedback := &bridge.TypeFeedback{
		Points: []bridge.PointFeedback{
			{},
			{Kind: bridge.FBSelfMono, Confidence: 1.0, StableShape: 7, StableIndex: 2},
		},
	}
	if _, ok := analyzeSelfCallSpecForm(proto, feedback); ok {
		t.Error("analyzeSelfCallSpecForm IC 非 NodeHit 应返 false")
	}
}

// TestPJ5_IsValidSpecCallRetCount verifies isValidSpecCallRetCount cC∈{1,3..16}
// strict upper bound (extends 84c7ed4 N=2..15 ret expansion + 7f5f641 N=15 upper-bound edge e2e).
func TestPJ5_IsValidSpecCallRetCount(t *testing.T) {
	tests := []struct {
		cC   int
		want bool
		desc string
	}{
		// accept
		{1, true, "cC=1 (0 返/void/setter)"},
		{3, true, "cC=3 (N=2 返)"},
		{4, true, "cC=4 (N=3 返)"},
		{5, true, "cC=5 (N=4 返)"},
		{9, true, "cC=9 (N=8 返)"},
		{16, true, "cC=16 (N=15 返上界)"},
		// reject
		{0, false, "cC=0 (multi-ret 不识别)"},
		{2, false, "cC=2 (1 返 getter 走独立分支)"},
		{17, false, "cC=17 (N=16 返超严格上界)"},
		{255, false, "cC=255 (Lua 5.1 CALL C 最大,超严格上界)"},
		{-1, false, "cC=-1 (无效输入兜底)"},
	}
	for _, tt := range tests {
		got := isValidSpecCallRetCount(tt.cC)
		if got != tt.want {
			t.Errorf("isValidSpecCallRetCount(%d) = %v, want %v (%s)",
				tt.cC, got, tt.want, tt.desc)
		}
	}
}

// TestPJ5_AnalyzeSelfCallSpecForm_RejectLowConfidence rejects when Confidence < 0.99.
//
// Follows compiler.go::analyzeSelfCallSpecForm line 2564: `pf.Confidence < 0.99
// should return false`. Follows 03-speculation-ic.md: when FBSelfMono polymorphizes
// and lowers Confidence, degrading to host.Self is safe.
func TestPJ5_AnalyzeSelfCallSpecForm_RejectLowConfidence(t *testing.T) {
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0),
			bytecode.EncodeABC(bytecode.SELF, 1, 1, 256),
			bytecode.EncodeABC(bytecode.CALL, 1, 2, 1),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
		IC:     make([]bytecode.ICSlot, 4),
		Consts: []value.Value{value.Value(0x42)},
	}
	proto.IC[1] = bytecode.ICSlot{Kind: bytecode.ICKindNodeHit, Shape: 7, Index: 2}
	// Confidence 0.5 (< 0.99 threshold)
	feedback := &bridge.TypeFeedback{
		Points: []bridge.PointFeedback{
			{},
			{Kind: bridge.FBSelfMono, Confidence: 0.5, StableShape: 7, StableIndex: 2},
		},
	}
	if _, ok := analyzeSelfCallSpecForm(proto, feedback); ok {
		t.Error("Confidence < 0.99 应返 false(避免多态化场景误启用 spec template)")
	}
}

// TestPJ5_AnalyzeSelfCallSpecForm_RejectShapeMismatch rejects when IC.Shape != feedback.StableShape.
//
// Follows compiler.go::analyzeSelfCallSpecForm line 2567: return false when Shape/Index disagree
// (IC and feedback are out of sync, e.g. the IC slot was updated later or the feedback holds a stale shape).
func TestPJ5_AnalyzeSelfCallSpecForm_RejectShapeMismatch(t *testing.T) {
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0),
			bytecode.EncodeABC(bytecode.SELF, 1, 1, 256),
			bytecode.EncodeABC(bytecode.CALL, 1, 2, 1),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
		IC:     make([]bytecode.ICSlot, 4),
		Consts: []value.Value{value.Value(0x42)},
	}
	// IC.Shape = 7
	proto.IC[1] = bytecode.ICSlot{Kind: bytecode.ICKindNodeHit, Shape: 7, Index: 2}
	// feedback.StableShape = 99 (mismatch)
	feedback := &bridge.TypeFeedback{
		Points: []bridge.PointFeedback{
			{},
			{Kind: bridge.FBSelfMono, Confidence: 1.0, StableShape: 99, StableIndex: 2},
		},
	}
	if _, ok := analyzeSelfCallSpecForm(proto, feedback); ok {
		t.Error("Shape mismatch 应返 false")
	}
}

// TestPJ5_AnalyzeSelfCallSpecForm_RejectStableKeyNil rejects when stableKey=Nil
// (when SELF.C's constant is Nil it cannot be baked in; guards against a false SELF NodeHit guard hit).
func TestPJ5_AnalyzeSelfCallSpecForm_RejectStableKeyNil(t *testing.T) {
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0),
			bytecode.EncodeABC(bytecode.SELF, 1, 1, 256),
			bytecode.EncodeABC(bytecode.CALL, 1, 2, 1),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
		IC:     make([]bytecode.ICSlot, 4),
		Consts: []value.Value{value.Nil}, // K[0] = Nil
	}
	proto.IC[1] = bytecode.ICSlot{Kind: bytecode.ICKindNodeHit, Shape: 7, Index: 2}
	feedback := &bridge.TypeFeedback{
		Points: []bridge.PointFeedback{
			{},
			{Kind: bridge.FBSelfMono, Confidence: 1.0, StableShape: 7, StableIndex: 2},
		},
	}
	if _, ok := analyzeSelfCallSpecForm(proto, feedback); ok {
		t.Error("stableKey = Nil 应返 false(防 NodeHit guard 误命中 Nil)")
	}
}

// TestPJ5_AnalyzeSelfCallForm_RejectCodeLenTooSmall codeLen<4 should be rejected
// (SELF needs at least MOVE/GETUPVAL + SELF + CALL + RETURN = 4 ops).
func TestPJ5_AnalyzeSelfCallForm_RejectCodeLenTooSmall(t *testing.T) {
	for _, codeLen := range []int{1, 2, 3} {
		proto := &bytecode.Proto{
			Code:   make([]bytecode.Instruction, codeLen),
			Consts: []value.Value{},
		}
		if _, ok := analyzeSelfCallForm(proto); ok {
			t.Errorf("codeLen=%d 应返 false(SELF 最小形态 4 op)", codeLen)
		}
	}
}

// TestPJ5_AnalyzeSelfCallForm_RejectCodeLenTooLarge codeLen>11 should be rejected
// (8+ arg forms have codeLen >= 12; neither the spec template nor inline covers them, so they
// degrade to the host helper round-trip path; follows §9.19 N=0..7 arg coverage upper bound).
//
// This test makes the 8+ arg spec template boundary explicit, guarding against future regression
// (if form12+ gets covered, this test's upper bound must be updated accordingly).
func TestPJ5_AnalyzeSelfCallForm_RejectCodeLenTooLarge(t *testing.T) {
	for _, codeLen := range []int{12, 13, 14, 20} {
		proto := &bytecode.Proto{
			Code:   make([]bytecode.Instruction, codeLen),
			Consts: []value.Value{},
		}
		// Fill in a valid SELF form for the first 4 ops; the rest are NOP (never actually read)
		proto.Code[0] = bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0)
		proto.Code[1] = bytecode.EncodeABC(bytecode.SELF, 1, 1, 256)
		if _, ok := analyzeSelfCallForm(proto); ok {
			t.Errorf("codeLen=%d 应返 false(8+ 参形态超 spec template 上界)", codeLen)
		}
	}
}
