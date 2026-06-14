// State machine 测试(`docs/design/p2-bridge/04-try-compile-fallback.md` §2-§5)。
//
// 验证 considerPromotion 四路径 + TierState 单向 + 吸收态:
//
//	(P1) 已吸收态 → no-op(防抖)
//	(P2) CompUnknown / CompNotCompilable → TierStuck
//	(P3) Compilable + try-compile 成功 → TierGibbous
//	(P3-fail) Compilable + try-compile err → TierStuck
//
// **零 deopt 不变式**:任何转移后状态都不会回 TierInterp(04 §2.4 形式化论证)。
package bridge

import (
	"errors"
	"testing"

	"github.com/Liam0205/wangshu/internal/bytecode"
)

// TestStateMachine_NotCompilable_Stuck (P2) 路径:CompNotCompilable → Stuck。
func TestStateMachine_NotCompilable_Stuck(t *testing.T) {
	b := NewBridge()
	p := makeProtoWithCode(bytecode.ADD)
	pd := b.ProfileOf(p)

	// 模拟 PB3 已分析判 NotCompilable
	pd.Compilable = CompNotCompilable
	pd.Reasons = ReasonVararg

	// 触发越阈值 considerPromotion
	for i := uint32(0); i < HotEntryThreshold; i++ {
		b.OnEnter(p, true)
	}

	if pd.TierState != TierStuck {
		t.Errorf("TierState = %v, want TierStuck after NotCompilable promotion", pd.TierState)
	}
	if !pd.CompileTried {
		t.Errorf("CompileTried should be true after Stuck transition")
	}
}

// TestStateMachine_Unknown_Stuck CompUnknown 视同 NotCompilable(03 §5.5
// + 04 §3.2)→ Stuck。这是 P1-only build 退化兜底。
func TestStateMachine_Unknown_Stuck(t *testing.T) {
	b := NewBridge()
	p := makeProtoWithCode(bytecode.ADD)
	pd := b.ProfileOf(p)
	// 默认 CompUnknown(P1 占位)

	for i := uint32(0); i < HotEntryThreshold; i++ {
		b.OnEnter(p, true)
	}

	if pd.TierState != TierStuck {
		t.Errorf("CompUnknown should also transition to TierStuck, got %v", pd.TierState)
	}
}

// TestStateMachine_Compilable_Promoted (P3-success) 路径:Compilable +
// 编译成功 → TierGibbous。
func TestStateMachine_Compilable_Promoted(t *testing.T) {
	b := NewBridge()
	b.SetP3Compiler(dummyCompileP3{})
	p := makeProtoWithCode(bytecode.ADD)
	pd := b.ProfileOf(p)
	pd.Compilable = CompCompilable

	for i := uint32(0); i < HotEntryThreshold; i++ {
		b.OnEnter(p, true)
	}

	if pd.TierState != TierGibbous {
		t.Errorf("Compilable + success → TierGibbous, got %v", pd.TierState)
	}
	if pd.Feedback == nil {
		t.Errorf("Feedback should be populated on promotion path")
	}
	if _, ok := b.gibbousCodes[p]; !ok {
		t.Errorf("gibbousCodes should have entry for promoted Proto")
	}
}

// TestStateMachine_CompileFail_Stuck (P3-fail) 路径:Compilable + Compile
// 返 err → TierStuck。
func TestStateMachine_CompileFail_Stuck(t *testing.T) {
	b := NewBridge()
	b.SetP3Compiler(failingP3{err: errors.New("synthetic compile error")})
	p := makeProtoWithCode(bytecode.ADD)
	pd := b.ProfileOf(p)
	pd.Compilable = CompCompilable

	for i := uint32(0); i < HotEntryThreshold; i++ {
		b.OnEnter(p, true)
	}

	if pd.TierState != TierStuck {
		t.Errorf("Compile fail → TierStuck, got %v", pd.TierState)
	}
}

// TestStateMachine_BackendPanic_Stuck P3 内部 panic 经 defer recover 转 err
// → TierStuck;不让 panic 穿越接口。
func TestStateMachine_BackendPanic_Stuck(t *testing.T) {
	b := NewBridge()
	b.SetP3Compiler(panicP3{})
	p := makeProtoWithCode(bytecode.ADD)
	pd := b.ProfileOf(p)
	pd.Compilable = CompCompilable

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("panic must not escape considerPromotion, got %v", r)
		}
	}()

	for i := uint32(0); i < HotEntryThreshold; i++ {
		b.OnEnter(p, true)
	}

	if pd.TierState != TierStuck {
		t.Errorf("backend panic → TierStuck, got %v", pd.TierState)
	}
}

// TestStateMachine_Idempotent_Stuck 一旦 Stuck 不再触发 considerPromotion——
// 后续越阈值守卫拦下,P3 编译次数恒 0。
func TestStateMachine_Idempotent_Stuck(t *testing.T) {
	mock := &countingP3{}
	b := NewBridge()
	b.SetP3Compiler(mock)
	p := makeProtoWithCode(bytecode.ADD)
	pd := b.ProfileOf(p)
	pd.Compilable = CompNotCompilable // 直接 Stuck 路径

	// 跑足够多回 EntryCount,守卫每次都直接 return
	for i := 0; i < 10*int(HotEntryThreshold); i++ {
		b.OnEnter(p, true)
	}
	if mock.compileCalls != 0 {
		t.Errorf("Stuck should never trigger Compile, got %d calls", mock.compileCalls)
	}

	// 切到 Compilable 后仍守住——TierState 已是 Stuck,守卫继续拦
	pd.Compilable = CompCompilable
	for i := 0; i < int(HotEntryThreshold); i++ {
		b.OnEnter(p, true)
	}
	if pd.TierState != TierStuck {
		t.Errorf("Stuck must not transition to anything else, got %v", pd.TierState)
	}
}

// TestStateMachine_Idempotent_Gibbous Gibbous 也是吸收态——后续越阈值不
// 触发再编译。
func TestStateMachine_Idempotent_Gibbous(t *testing.T) {
	mock := &countingP3{}
	b := NewBridge()
	b.SetP3Compiler(mock)
	p := makeProtoWithCode(bytecode.ADD)
	pd := b.ProfileOf(p)
	pd.Compilable = CompCompilable

	for i := uint32(0); i < HotEntryThreshold; i++ {
		b.OnEnter(p, true)
	}
	if pd.TierState != TierGibbous {
		t.Fatalf("first round should promote, got %v", pd.TierState)
	}
	if mock.compileCalls != 1 {
		t.Fatalf("first round should compile once, got %d", mock.compileCalls)
	}

	// 持续 OnEnter ⇒ 守卫拦下,不再调 Compile
	for i := 0; i < 10*int(HotEntryThreshold); i++ {
		b.OnEnter(p, true)
	}
	if mock.compileCalls != 1 {
		t.Errorf("Gibbous should not re-compile, got %d calls", mock.compileCalls)
	}
}

// TestStateMachine_NoReverseEdge 「零 deopt」形式化:任何转移后,手工把
// TierState 设回 TierInterp 然后再 considerPromotion——状态机不会自然
// 「降」。这条断言是 04 §2.4 的代码级体现:状态机不存在
// TierGibbous → TierInterp / TierStuck → TierInterp 反向边——只有用户主动
// 重置才可能让状态回到起点(本测试不试图阻止主动重置,只验证「自然
// 转移序列」不会回头)。
func TestStateMachine_NoReverseEdge(t *testing.T) {
	b := NewBridge()
	b.SetP3Compiler(dummyCompileP3{})
	p := makeProtoWithCode(bytecode.ADD)
	pd := b.ProfileOf(p)
	pd.Compilable = CompCompilable

	for i := uint32(0); i < HotEntryThreshold; i++ {
		b.OnEnter(p, true)
	}
	if pd.TierState != TierGibbous {
		t.Fatalf("expected Gibbous, got %v", pd.TierState)
	}

	// 模拟许多次后续事件——TierGibbous 应保持
	for i := 0; i < 1000; i++ {
		b.OnEnter(p, true)
		b.OnBackEdge(p, 0, true)
	}
	if pd.TierState != TierGibbous {
		t.Errorf("Gibbous broken by subsequent events, got %v", pd.TierState)
	}
}

// ----- mock P3 helpers -----

// dummyCompileP3:Compile 永远成功,产出空 GibbousCode。
type dummyCompileP3 struct{}

func (dummyCompileP3) SupportsAllOpcodes(_ *bytecode.Proto) bool { return true }
func (dummyCompileP3) Compile(p *bytecode.Proto, _ *TypeFeedback) (GibbousCode, error) {
	return dummyCode{proto: p}, nil
}

type dummyCode struct{ proto *bytecode.Proto }

func (d dummyCode) Proto() *bytecode.Proto         { return d.proto }
func (d dummyCode) Run(_ []uint64, _ uint32) int32 { return 0 }
func (d dummyCode) PendingErr() error              { return nil }

// failingP3:Compile 总返指定 err。
type failingP3 struct{ err error }

func (failingP3) SupportsAllOpcodes(_ *bytecode.Proto) bool { return true }
func (f failingP3) Compile(_ *bytecode.Proto, _ *TypeFeedback) (GibbousCode, error) {
	return nil, f.err
}

// panicP3:Compile 直接 panic(测 defer recover)。
type panicP3 struct{}

func (panicP3) SupportsAllOpcodes(_ *bytecode.Proto) bool { return true }
func (panicP3) Compile(_ *bytecode.Proto, _ *TypeFeedback) (GibbousCode, error) {
	panic("synthetic backend bug")
}

// countingP3:记录 Compile 调用次数(测幂等)。
type countingP3 struct{ compileCalls int }

func (countingP3) SupportsAllOpcodes(_ *bytecode.Proto) bool { return true }
func (c *countingP3) Compile(p *bytecode.Proto, _ *TypeFeedback) (GibbousCode, error) {
	c.compileCalls++
	return dummyCode{proto: p}, nil
}
