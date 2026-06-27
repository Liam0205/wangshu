//go:build wangshu_p4

package jit

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/bridge"
	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/value"
)

// PJ5 SELF method call inline 形态识别单测。覆盖:
//   - 长度 4:0 参 0 返 SELF + CALL void / SELF + TAILCALL
//   - 长度 5:0 参 1 返 SELF + CALL getter / 1 K/reg 参 SELF + CALL void /
//     1 K/reg 参 SELF + TAILCALL
//   - 长度 6:1 K/reg 参 SELF + CALL getter 1 返 / 2 K/reg 参 SELF + CALL void /
//     2 K/reg 参 SELF + TAILCALL
//
// 双 receiver(M*=MOVE reg / U*=GETUPVAL upval)各形态对位。

// TestPJ5_AnalyzeSelfCallForm_M0_VoidCall 形态 M0:
// MOVE+SELF+CALL+RETURN void(`function(o) o:m() end`)。
func TestPJ5_AnalyzeSelfCallForm_M0_VoidCall(t *testing.T) {
	// MOVE 1 0;        // R(1) = R(0) recv
	// SELF 1 1 256;    // R(1)=R(1)[K0]; R(2)=R(1) self  (C=256 即 K(0))
	// CALL 1 2 1;      // 0 参 0 返
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

// TestPJ5_AnalyzeSelfCallForm_U0_VoidCall 形态 U0:
// GETUPVAL+SELF+CALL+RETURN void(`function() o:m() end`,o 是 upval)。
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

// TestPJ5_AnalyzeSelfCallForm_M0_TailCall 形态 TM0:
// MOVE+SELF+TAILCALL+RETURN(B=0 dead)(`function(o) return o:m() end`)。
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

// TestPJ5_AnalyzeSelfCallForm_M0_GetterCall 形态 MR1:
// MOVE+SELF+CALL+RETURN(callA,2)+RETURN(0,1) 0 参 1 返
// (`function(o) return o:m() end` 编 luac SubProto 主路径 — 实测 TAILCALL,
// 但合成驱动验形态)。
func TestPJ5_AnalyzeSelfCallForm_M0_GetterCall(t *testing.T) {
	// MOVE 1 0;        SELF 1 1 256;
	// CALL 1 2 2;      // 0 参 1 返
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

// TestPJ5_AnalyzeSelfCallForm_M1K_VoidCall 形态 M1K:
// MOVE+SELF+LOADK+CALL+RETURN void(`function(o) o:m(1) end`)1 K 参 0 返。
func TestPJ5_AnalyzeSelfCallForm_M1K_VoidCall(t *testing.T) {
	// MOVE 1 0;     SELF 1 1 256(K0=method);
	// LOADK 3 1(K1=1);  CALL 1 3 1;  RETURN 0 1
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

// TestPJ5_AnalyzeSelfCallForm_M1R_VoidCall 形态 M1R:
// MOVE+SELF+MOVE+CALL+RETURN void(`function(o,a) o:m(a) end`)1 reg 参 0 返。
func TestPJ5_AnalyzeSelfCallForm_M1R_VoidCall(t *testing.T) {
	// 注:实际 luac 编 `function(o,a) o:m(a) end` 是
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

// TestPJ5_AnalyzeSelfCallForm_RejectShortCode 拒识别长度 < 4。
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

// TestPJ5_AnalyzeSelfCallForm_RejectNoSelf 拒形态 [1] != SELF。
func TestPJ5_AnalyzeSelfCallForm_RejectNoSelf(t *testing.T) {
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0),
			bytecode.EncodeABC(bytecode.GETTABLE, 1, 1, 256), // 不是 SELF
			bytecode.EncodeABC(bytecode.CALL, 1, 2, 1),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
	}
	if _, ok := analyzeSelfCallForm(proto); ok {
		t.Error("analyzeSelfCallForm should reject when [1] != SELF")
	}
}

// TestPJ5_AnalyzeSelfCallForm_RejectMethodReg SELF.C < 256(reg 形态)拒。
func TestPJ5_AnalyzeSelfCallForm_RejectMethodReg(t *testing.T) {
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0),
			bytecode.EncodeABC(bytecode.SELF, 1, 1, 0), // C=0 即 R(0)
			bytecode.EncodeABC(bytecode.CALL, 1, 2, 1),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
	}
	if _, ok := analyzeSelfCallForm(proto); ok {
		t.Error("analyzeSelfCallForm should reject SELF.C < 256 (reg method name)")
	}
}

// TestPJ5_AnalyzeSelfCallSpecForm_M0 验 analyzeSelfCallSpecForm 识别长度 4
// SELF + CALL void 0 参形态 + IC NodeHit feedback 命中 → useSpecSelfCall=true。
//
// 形态:MOVE 1 0; SELF 1 1 256; CALL 1 2 1; RETURN 0 1
// IC[1](SELF pc)= NodeHit + feedback.Points[1] = FBTableMono。
func TestPJ5_AnalyzeSelfCallSpecForm_M0(t *testing.T) {
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0),
			bytecode.EncodeABC(bytecode.SELF, 1, 1, 256), // C=256 → K[0]
			bytecode.EncodeABC(bytecode.CALL, 1, 2, 1),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
		IC:     make([]bytecode.ICSlot, 4),
		Consts: []value.Value{value.Value(0x42)}, // K[0] method key(非 Nil)
	}
	// IC[1] = SELF 的 IC slot:NodeHit + Shape=7 + Index=2
	proto.IC[1] = bytecode.ICSlot{Kind: bytecode.ICKindNodeHit, Shape: 7, Index: 2}

	// feedback.Points[1] 对位 SELF pc=1
	feedback := &bridge.TypeFeedback{
		Points: []bridge.PointFeedback{
			{}, // Points[0] dummy(MOVE pc=0)
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

// TestPJ5_AnalyzeSelfCallSpecForm_RejectNoFeedback 无 feedback 时拒(走普通 host.Self 路径)。
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

// TestPJ5_AnalyzeSelfCallSpecForm_RejectNoNodeHit IC 非 NodeHit 时拒。
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
	// IC[1] = ArrayHit(非 NodeHit）
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

// TestPJ5_IsValidSpecCallRetCount 验 isValidSpecCallRetCount cC∈{1,3..16}
// 严格上界(承 84c7ed4 N=2..15 返扩 + 7f5f641 N=15 上界边界 e2e)。
func TestPJ5_IsValidSpecCallRetCount(t *testing.T) {
	tests := []struct {
		cC   int
		want bool
		desc string
	}{
		// 接受
		{1, true, "cC=1 (0 返/void/setter)"},
		{3, true, "cC=3 (N=2 返)"},
		{4, true, "cC=4 (N=3 返)"},
		{5, true, "cC=5 (N=4 返)"},
		{9, true, "cC=9 (N=8 返)"},
		{16, true, "cC=16 (N=15 返上界)"},
		// 拒
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

// TestPJ5_AnalyzeSelfCallSpecForm_RejectLowConfidence Confidence < 0.99 拒。
//
// 承 compiler.go::analyzeSelfCallSpecForm line 2564:`pf.Confidence < 0.99
// 应返 false`。承 03-speculation-ic.md FBSelfMono 多态化降低 Confidence
// 时降级 host.Self 安全。
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
	// Confidence 0.5(<0.99 阈值)
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

// TestPJ5_AnalyzeSelfCallSpecForm_RejectShapeMismatch IC.Shape != feedback.StableShape 拒。
//
// 承 compiler.go::analyzeSelfCallSpecForm line 2567:Shape/Index 不一致时返 false
// (IC 与 feedback 失同步,可能 IC slot 后更新或 feedback 期旧 shape)。
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
	// feedback.StableShape = 99(mismatch)
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

// TestPJ5_AnalyzeSelfCallSpecForm_RejectStableKeyNil stableKey=Nil 拒
// (SELF.C 常量为 Nil 时无法烧入,防 SELF NodeHit guard 误命中)。
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
