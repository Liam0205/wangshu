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

// decliningP3:SupportsAllOpcodes 恒 false 且计数(测 issue #40 recheck
// dedup——forceAll retry window 内每回边不应重跑全量后端分析)。
type decliningP3 struct{ supportsCalls int }

func (d *decliningP3) SupportsAllOpcodes(_ *bytecode.Proto) bool {
	d.supportsCalls++
	return false
}
func (*decliningP3) Compile(_ *bytecode.Proto, _ *TypeFeedback) (GibbousCode, error) {
	return nil, errors.New("declining backend must not be asked to compile")
}

// flippingP3:SupportsAllOpcodes 先拒后收(accept=true 后接受),模拟
// IC-gated 后端在解释器跑过一轮、IC 变暖之后改判接受。
type flippingP3 struct {
	accept        bool
	supportsCalls int
	compileCalls  int
}

func (f *flippingP3) SupportsAllOpcodes(_ *bytecode.Proto) bool {
	f.supportsCalls++
	return f.accept
}
func (f *flippingP3) Compile(p *bytecode.Proto, _ *TypeFeedback) (GibbousCode, error) {
	f.compileCalls++
	return dummyCode{proto: p}, nil
}

// TestForceRetryWindow_RecheckDedupPerEntry (issue #40):forceAll 下被后端
// 拒收的 proto 停留在 retry window 内时,同一次进入(EntryCount 不变)的
// 海量回边**不得**每条都重跑 recheckCompilabilityRuntime 全量分析——
// HeavyArith 形态(单次进入 + 2M 回边)实测该路径占 22% CPU + 1.5 GB/op。
// dedup 后每 pc 每次进入至多 3 次(入口 + 首回边 + HotBackEdgeThreshold)。
func TestForceRetryWindow_RecheckDedupPerEntry(t *testing.T) {
	mock := &decliningP3{}
	b := NewBridge()
	b.SetP3Compiler(mock)
	b.SetForceAllPromote(true)
	p := makeProtoWithCode(bytecode.ADD)

	b.OnEnter(p, true)
	const edges = 10000
	for i := 0; i < edges; i++ {
		b.OnBackEdge(p, 0, true)
	}

	pd := b.ProfileOf(p)
	if pd.TierState != TierInterp {
		t.Fatalf("declined proto within retry window should stay TierInterp, got %v", pd.TierState)
	}
	// 入口 1 次 + 回边 count==1 再武装 1 次 + count==HotBackEdgeThreshold
	// 再武装 1 次 = 3;留一点余量防未来再武装点微调,但必须远小于回边数。
	if mock.supportsCalls > 5 {
		t.Errorf("SupportsAllOpcodes ran %d times for %d back edges within one entry; dedup should cap it at ~3",
			mock.supportsCalls, edges)
	}
}

// TestForceRetryWindow_WarmICPromotesOnFirstBackEdge (issue #40 dedup 的
// 对偶面,保 retry window 原始目的):IC-gated 后端冷 IC 拒收、循环体跑过
// 一轮后改判接受——dedup 在首个回边(count==1)重新武装 recheck,升层点
// 不得比修复前更晚。
func TestForceRetryWindow_WarmICPromotesOnFirstBackEdge(t *testing.T) {
	mock := &flippingP3{}
	b := NewBridge()
	b.SetP3Compiler(mock)
	b.SetForceAllPromote(true)
	p := makeProtoWithCode(bytecode.ADD)

	b.OnEnter(p, true) // 冷 IC:拒收,进 retry window
	pd := b.ProfileOf(p)
	if pd.TierState != TierInterp {
		t.Fatalf("cold decline should stay TierInterp, got %v", pd.TierState)
	}

	mock.accept = true // 模拟循环体首轮跑完,IC 已暖
	b.OnBackEdge(p, 0, true)

	if pd.TierState != TierGibbous {
		t.Errorf("warm IC at first back edge should promote, got %v", pd.TierState)
	}
	if mock.compileCalls != 1 {
		t.Errorf("expected exactly one Compile after warm-IC accept, got %d", mock.compileCalls)
	}
}

// TestForceRetryWindow_AbsorbsToStuck 窗口语义不变:恒拒后端 + forceAll,
// 窗口关闭(EntryCount=64,覆盖递归 proto 的深 pc IC 预热——binary-trees
// `check` 的第三个 GETTABLE 要到左子树全部返回后才第一次执行)后吸收到
// TierStuck,之后不再分析。
func TestForceRetryWindow_AbsorbsToStuck(t *testing.T) {
	mock := &decliningP3{}
	b := NewBridge()
	b.SetP3Compiler(mock)
	b.SetForceAllPromote(true)
	p := makeProtoWithCode(bytecode.ADD)
	pd := b.ProfileOf(p)

	for i := 0; i < 63; i++ {
		b.OnEnter(p, true)
	}
	if pd.TierState != TierInterp {
		t.Fatalf("entries 1-63 should stay in the retry window, got %v", pd.TierState)
	}

	b.OnEnter(p, true) // EntryCount=64:窗口关闭
	if pd.TierState != TierStuck {
		t.Fatalf("entry 64 should absorb to TierStuck, got %v", pd.TierState)
	}

	callsAtStuck := mock.supportsCalls
	for i := 0; i < 1000; i++ {
		b.OnEnter(p, true)
		b.OnBackEdge(p, 0, true)
	}
	if mock.supportsCalls != callsAtStuck {
		t.Errorf("Stuck is absorbing; SupportsAllOpcodes must not run again (%d → %d)",
			callsAtStuck, mock.supportsCalls)
	}
}

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
func (d dummyCode) Slot() (uint32, bool)           { return 0, false }

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

// TestStateMachine_Coroutine_NoPromote (V11 协程不升层):承
// bridge.go::considerPromotion line 263-265 onMain=false 守门 + [07 §2.4]
// 协程内即便热度越阈值也不升层(原样继承 P3 规则)。
//
// **场景**:协程线程上 OnEnter(p, false=onMain) 反复触发达 HotEntryThreshold,
// 但因 onMain=false,considerPromotion 直接 return,Proto 永远 TierInterp。
//
// **prove-the-path**:HotEntryThreshold 次 OnEnter(p, false) 后,
// pd.TierState 仍 TierInterp(主线程下早就 TierGibbous 或 TierStuck)。
func TestStateMachine_Coroutine_NoPromote(t *testing.T) {
	b := NewBridge()
	b.SetP3Compiler(dummyCompileP3{})
	p := makeProtoWithCode(bytecode.ADD)
	pd := b.ProfileOf(p)
	pd.Compilable = CompCompilable // 主线程下应升 TierGibbous

	// 协程触发(onMain=false):承 V11 不升层
	for i := uint32(0); i < HotEntryThreshold*2; i++ {
		b.OnEnter(p, false) // 协程线程
	}

	if pd.TierState != TierInterp {
		t.Errorf("协程内 TierState = %v, want TierInterp(V11 协程不升层)", pd.TierState)
	}
	if _, ok := b.gibbousCodes[p]; ok {
		t.Errorf("gibbousCodes 不应有 entry(协程不触发 Compile)")
	}
}

// TestStateMachine_Coroutine_NoPromote_AfterMainPromote 主线程先升后协程
// 反复调用:主线程升 TierGibbous 后,协程内 OnEnter(p, false) 应 no-op
// (P1 已吸收态守门,与 onMain 无关)。验 V11 + 主线程升层不互扰。
func TestStateMachine_Coroutine_NoPromote_AfterMainPromote(t *testing.T) {
	b := NewBridge()
	b.SetP3Compiler(&countingP3{})
	p := makeProtoWithCode(bytecode.ADD)
	pd := b.ProfileOf(p)
	pd.Compilable = CompCompilable

	// 主线程升层
	for i := uint32(0); i < HotEntryThreshold; i++ {
		b.OnEnter(p, true)
	}
	if pd.TierState != TierGibbous {
		t.Fatalf("主线程升层前提失败:TierState = %v, want TierGibbous", pd.TierState)
	}

	// 协程反复调用:不应触发 Compile 二次(P1 守门 + onMain=false 双重保险)
	compileCallsBefore := b.p3.(*countingP3).compileCalls
	for i := uint32(0); i < HotEntryThreshold*2; i++ {
		b.OnEnter(p, false)
	}
	compileCallsAfter := b.p3.(*countingP3).compileCalls

	if compileCallsAfter != compileCallsBefore {
		t.Errorf("协程内 Compile 调用次数 %d → %d(应不变,V11 + P1 守门)",
			compileCallsBefore, compileCallsAfter)
	}
	if pd.TierState != TierGibbous {
		t.Errorf("协程后 TierState = %v, want TierGibbous(不动)", pd.TierState)
	}
}

// gatedP3:能编译一切,但 WorthPromoting 拒绝(测 PromotionGater,issue #39)。
type gatedP3 struct {
	dummyCompileP3
	gateCalls int
}

func (g *gatedP3) WorthPromoting(_ *bytecode.Proto) bool {
	g.gateCalls++
	return false
}

// TestPromotionGater_AutoDeclinesToStuck:auto 模式下后端 WorthPromoting
// 返 false → TierStuck 吸收(不 try-compile),且 Compile 从未被调。
func TestPromotionGater_AutoDeclinesToStuck(t *testing.T) {
	b := NewBridge()
	mock := &gatedP3{}
	b.SetP3Compiler(mock)
	p := makeProtoWithCode(bytecode.ADD)
	pd := b.ProfileOf(p)
	pd.Compilable = CompCompilable

	for i := uint32(0); i < HotEntryThreshold; i++ {
		b.OnEnter(p, true)
	}

	if pd.TierState != TierStuck {
		t.Errorf("gated proto → TierStuck, got %v", pd.TierState)
	}
	if mock.gateCalls == 0 {
		t.Error("WorthPromoting was never consulted")
	}
	if _, ok := b.gibbousCodes[p]; ok {
		t.Error("gated proto must not be compiled/installed")
	}
	// Stuck is absorbing: later entries must not re-consult the gate.
	callsAtStuck := mock.gateCalls
	b.OnEnter(p, true)
	if mock.gateCalls != callsAtStuck {
		t.Errorf("Stuck must absorb; gate re-consulted (%d → %d)", callsAtStuck, mock.gateCalls)
	}
}

// TestPromotionGater_ForceAllBypasses:forceAll 模式绕过 profitability
// gate——差分覆盖不因收益判断缩水(issue #39)。
func TestPromotionGater_ForceAllBypasses(t *testing.T) {
	b := NewBridge()
	mock := &gatedP3{}
	b.SetP3Compiler(mock)
	b.SetForceAllPromote(true)
	p := makeProtoWithCode(bytecode.ADD)
	pd := b.ProfileOf(p)
	pd.Compilable = CompCompilable

	b.OnEnter(p, true)

	if pd.TierState != TierGibbous {
		t.Errorf("forceAll must bypass the gate and promote, got %v", pd.TierState)
	}
	if mock.gateCalls != 0 {
		t.Errorf("forceAll must not consult WorthPromoting (called %d times)", mock.gateCalls)
	}
}
