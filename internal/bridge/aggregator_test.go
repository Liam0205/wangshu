// Aggregator 单测(`docs/design/p2-bridge/02-ic-feedback.md` §6 验收)。
//
// 三类合成 ICSlot 输入对应三类 FeedbackKind 输出 + Confidence 计算:
//
//   - 算术 IC 双计数:numHits=99 metaHits=1 ⇒ FBArithStableNumber, conf=0.99
//   - 表 IC kind=2 (node hit) ⇒ FBTableMono(GETTABLE)/FBGlobalStable
//     (GETGLOBAL)/FBSelfMono(SELF)
//   - 表 IC kind=4 (megamorphic) ⇒ FBTableMega
//
// 边界情况:
//   - kind=0(未观测)⇒ Points[pc] 是零值(FBUnstable, conf=0,跳过)
//   - 算术 IC numHits+metaHits < MinObservations ⇒ FBUnstable(样本不足)
//   - 比例介于 0.5..0.99 ⇒ FBUnstable(混合态)
package bridge

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/bytecode"
)

// makeProtoWithCode 造一个含特定 opcode 的 Proto + 等长 IC 数组。
//
// **MinPromotableCodeLen padding**(issue #21):若 ops 数少于 MinPromotableCodeLen,
// 自动 padding 到 MinPromotableCodeLen 长度(用 NOP),让 considerPromotion 路径
// 测试不被守卫拦截。前 len(ops) 个 opcode 仍是测试指定的形态,IC 索引也对应前
// len(ops) 个 slot(后续 padding NOP 的 IC slot 都是零值)。
func makeProtoWithCode(ops ...bytecode.OpCode) *bytecode.Proto {
	n := len(ops)
	if n < MinPromotableCodeLen {
		n = MinPromotableCodeLen
	}
	code := make([]bytecode.Instruction, n)
	for i, op := range ops {
		code[i] = bytecode.Instruction(uint32(op))
	}
	// padding 位置默认是 0,即 OpCode=MOVE(实际不会被解析,只是过守卫长度)
	return &bytecode.Proto{
		Code: code,
		IC:   make([]bytecode.ICSlot, n),
	}
}

// TestAggregator_ArithStable 算术 IC 比例 ≥ 0.99 + 总命中 ≥ 100 ⇒
// FBArithStableNumber。
func TestAggregator_ArithStable(t *testing.T) {
	p := makeProtoWithCode(bytecode.ADD, bytecode.MUL, bytecode.SUB)
	// pc=0 ADD: 99% number, 1% meta(99 + 1 = 100,边界等于阈值)
	p.IC[0] = bytecode.ICSlot{Shape: 990, Index: 10, Kind: 1} // 990/(990+10)=0.99
	// pc=1 MUL: 全 number, 200 hits
	p.IC[1] = bytecode.ICSlot{Shape: 200, Index: 0, Kind: 1}
	// pc=2 SUB: 未观测
	p.IC[2] = bytecode.ICSlot{Kind: 0}

	a := NewAggregator()
	fb := a.Aggregate(p)

	if fb.Points[0].Kind != FBArithStableNumber {
		t.Errorf("pc=0 kind = %v, want FBArithStableNumber", fb.Points[0].Kind)
	}
	if fb.Points[0].Confidence < 0.989 || fb.Points[0].Confidence > 0.991 {
		t.Errorf("pc=0 confidence = %v, want ~0.99", fb.Points[0].Confidence)
	}
	if fb.Points[0].Observations != 1000 {
		t.Errorf("pc=0 observations = %d, want 1000", fb.Points[0].Observations)
	}

	if fb.Points[1].Kind != FBArithStableNumber {
		t.Errorf("pc=1 kind = %v, want FBArithStableNumber", fb.Points[1].Kind)
	}
	if fb.Points[1].Confidence != 1.0 {
		t.Errorf("pc=1 confidence = %v, want 1.0", fb.Points[1].Confidence)
	}

	if fb.Points[2].Kind != FBUnstable || fb.Points[2].Confidence != 0 {
		t.Errorf("pc=2 should be unobserved zero-value, got %+v", fb.Points[2])
	}
}

// TestAggregator_ArithUnstable 比例 < 0.99 ⇒ FBUnstable;但 confidence 仍带
// 比例值供诊断。
func TestAggregator_ArithUnstable(t *testing.T) {
	p := makeProtoWithCode(bytecode.ADD, bytecode.MUL)
	// pc=0 70% number 30% meta(混合态)
	p.IC[0] = bytecode.ICSlot{Shape: 700, Index: 300, Kind: 1}
	// pc=1 50/50(诊断比例)
	p.IC[1] = bytecode.ICSlot{Shape: 500, Index: 500, Kind: 1}

	a := NewAggregator()
	fb := a.Aggregate(p)

	if fb.Points[0].Kind != FBUnstable {
		t.Errorf("pc=0 kind = %v, want FBUnstable (ratio 0.70 < 0.99)", fb.Points[0].Kind)
	}
	if fb.Points[0].Confidence < 0.69 || fb.Points[0].Confidence > 0.71 {
		t.Errorf("pc=0 confidence = %v, want ~0.70 diagnostic", fb.Points[0].Confidence)
	}
	if fb.Points[1].Kind != FBUnstable {
		t.Errorf("pc=1 kind = %v, want FBUnstable", fb.Points[1].Kind)
	}
	if fb.Points[1].Confidence < 0.49 || fb.Points[1].Confidence > 0.51 {
		t.Errorf("pc=1 confidence = %v, want ~0.5", fb.Points[1].Confidence)
	}
}

// TestAggregator_ArithSampleTooFew 样本量 < MinObservations(100) ⇒ FBUnstable。
func TestAggregator_ArithSampleTooFew(t *testing.T) {
	p := makeProtoWithCode(bytecode.ADD)
	// 99 numHits 0 metaHits = 比例 1.0 但样本量 < 100
	p.IC[0] = bytecode.ICSlot{Shape: 99, Index: 0, Kind: 1}

	a := NewAggregator()
	fb := a.Aggregate(p)

	if fb.Points[0].Kind != FBUnstable {
		t.Errorf("kind = %v, want FBUnstable for under-min samples", fb.Points[0].Kind)
	}
	if fb.Points[0].Confidence != 0 {
		t.Errorf("confidence = %v, want 0 for sample-too-few", fb.Points[0].Confidence)
	}
	if fb.Points[0].Observations != 99 {
		t.Errorf("observations = %d, want 99", fb.Points[0].Observations)
	}
}

// TestAggregator_TableIC 表 IC kind 2 (node hit) → FBTableMono(GETTABLE)/
// FBGlobalStable(GETGLOBAL)/FBSelfMono(SELF)。
func TestAggregator_TableIC(t *testing.T) {
	p := makeProtoWithCode(bytecode.GETTABLE, bytecode.GETGLOBAL, bytecode.SELF, bytecode.SETTABLE, bytecode.SETGLOBAL)
	// 各 pc 都设 node hit
	for i := range p.IC {
		p.IC[i] = bytecode.ICSlot{
			Shape:    42,
			Index:    7,
			TableRef: 0xdeadbeef,
			Kind:     bytecode.ICKindNodeHit,
		}
	}

	a := NewAggregator()
	fb := a.Aggregate(p)

	cases := []struct {
		pc   int
		want FeedbackKind
	}{
		{0, FBTableMono},    // GETTABLE
		{1, FBGlobalStable}, // GETGLOBAL
		{2, FBSelfMono},     // SELF
		{3, FBTableMono},    // SETTABLE
		{4, FBGlobalStable}, // SETGLOBAL
	}
	for _, c := range cases {
		got := fb.Points[c.pc]
		if got.Kind != c.want {
			t.Errorf("pc=%d kind = %v, want %v", c.pc, got.Kind, c.want)
		}
		if got.Confidence != 1.0 {
			t.Errorf("pc=%d confidence = %v, want 1.0 (mono IC)", c.pc, got.Confidence)
		}
		if got.StableShape != 42 || got.StableIndex != 7 {
			t.Errorf("pc=%d stable shape/index = %d/%d, want 42/7",
				c.pc, got.StableShape, got.StableIndex)
		}
	}
}

// TestAggregator_TableMega kind=4 megamorphic → FBTableMega + confidence 0 +
// stable shape/index 清 0(02 §6.3 防御性翻译,P1 当前不写 kind=4)。
func TestAggregator_TableMega(t *testing.T) {
	p := makeProtoWithCode(bytecode.GETTABLE)
	p.IC[0] = bytecode.ICSlot{
		Shape:    99,
		Index:    7,
		TableRef: 0xcafe,
		Kind:     bytecode.ICKindMegamorphic,
	}

	a := NewAggregator()
	fb := a.Aggregate(p)
	got := fb.Points[0]

	if got.Kind != FBTableMega {
		t.Errorf("kind = %v, want FBTableMega", got.Kind)
	}
	if got.Confidence != 0 {
		t.Errorf("confidence = %v, want 0 for mega", got.Confidence)
	}
	if got.StableShape != 0 || got.StableIndex != 0 {
		t.Errorf("stable shape/index should be cleared for mega")
	}
}

// TestAggregator_RefillTriggersMega P2+ #4:Refill ≥ MegamorphicRefillThreshold
// (默认 3)即便 Kind 仍是 mono 也主动翻译为 FBTableMega。
func TestAggregator_RefillTriggersMega(t *testing.T) {
	p := makeProtoWithCode(bytecode.GETTABLE)
	// Kind=NodeHit 看似单态,但 Refill=5 表示历史经过多次重填(多态)
	p.IC[0] = bytecode.ICSlot{
		Shape:    42,
		Index:    7,
		TableRef: 0xdeadbeef,
		Kind:     bytecode.ICKindNodeHit,
		Refill:   5,
	}

	a := NewAggregator()
	fb := a.Aggregate(p)
	got := fb.Points[0]
	if got.Kind != FBTableMega {
		t.Errorf("Refill=5 should trigger FBTableMega, got %v", got.Kind)
	}
	if got.StableShape != 0 || got.StableIndex != 0 {
		t.Errorf("stable shape/index should be cleared on Refill-mega")
	}
}

// TestAggregator_RefillBelowThresholdStillMono Refill < threshold 时仍按
// kind 判 mono(单态命中虽偶有重填但不算多态)。
func TestAggregator_RefillBelowThresholdStillMono(t *testing.T) {
	p := makeProtoWithCode(bytecode.GETTABLE)
	p.IC[0] = bytecode.ICSlot{
		Shape:    42,
		Index:    7,
		TableRef: 0xdeadbeef,
		Kind:     bytecode.ICKindNodeHit,
		Refill:   2, // < 3 阈值
	}

	a := NewAggregator()
	fb := a.Aggregate(p)
	got := fb.Points[0]
	if got.Kind != FBTableMono {
		t.Errorf("Refill=2 (<threshold) should stay FBTableMono, got %v", got.Kind)
	}
}

// TestAggregator_NonICOpsAreUnstable 非 IC 指令对应槽是 FBUnstable 零值
// (LOADK/MOVE/RETURN/...)——P3/P4 应跳过。
func TestAggregator_NonICOpsAreUnstable(t *testing.T) {
	p := makeProtoWithCode(bytecode.LOADK, bytecode.MOVE, bytecode.RETURN)

	a := NewAggregator()
	fb := a.Aggregate(p)

	for i := 0; i < 3; i++ {
		if fb.Points[i].Kind != FBUnstable || fb.Points[i].Confidence != 0 {
			t.Errorf("pc=%d non-IC op should be FBUnstable zero, got %+v",
				i, fb.Points[i])
		}
	}
}

// TestAggregator_GenerationMonotonic 多次 Aggregate 同一 Proto generation 单调
// 递增——P3/P4 据此判 feedback 快照新旧。
func TestAggregator_GenerationMonotonic(t *testing.T) {
	p := makeProtoWithCode(bytecode.ADD)
	p.IC[0] = bytecode.ICSlot{Shape: 200, Index: 0, Kind: 1}

	a := NewAggregator()
	fb1 := a.Aggregate(p)
	fb2 := a.Aggregate(p)

	if fb2.Generation <= fb1.Generation {
		t.Errorf("generation not monotonic: fb1=%d fb2=%d", fb1.Generation, fb2.Generation)
	}
}

// TestBridgeInstallFeedback 单 State Feedback 一次性安装(02 §4.5 不重聚合
// 策略)——多次调 installFeedback 不覆盖。
func TestBridgeInstallFeedback(t *testing.T) {
	b := NewBridge()
	p := makeProtoWithCode(bytecode.ADD)
	p.IC[0] = bytecode.ICSlot{Shape: 200, Index: 0, Kind: 1}

	fb1 := b.Aggregator().Aggregate(p)
	b.installFeedback(p, fb1)

	pd := b.ProfileOf(p)
	if pd.Feedback != fb1 {
		t.Errorf("Feedback not installed (got %p, want %p)", pd.Feedback, fb1)
	}

	// 第二次 install 应被忽略(初版只聚合一次)
	fb2 := b.Aggregator().Aggregate(p)
	b.installFeedback(p, fb2)
	if pd.Feedback != fb1 {
		t.Errorf("Feedback overwritten on second install (got %p, want %p first)",
			pd.Feedback, fb1)
	}
}
