// Bridge 包骨架冒烟测试(PB0)——验证类型定义、Bridge 构造、钩点 no-op
// 形态、profileTable 惰性建表、零分配常态。
//
// **本测试不验证状态机转移**(那是 PB4 落地后的事)——PB0 阶段
// considerPromotion 是 no-op 占位,任何越阈值都不真正升层。
package bridge

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/bytecode"
)

// TestEnumStrings 锁定 String() 输出格式——升层日志(04 §6)与诊断工具
// 都依赖这些字符串,不能因实装迭代漂移。
func TestEnumStrings(t *testing.T) {
	t.Helper()
	cases := []struct {
		got, want string
	}{
		{TierInterp.String(), "interp"},
		{TierGibbous.String(), "gibbous"},
		{TierStuck.String(), "stuck"},

		{CompUnknown.String(), "Unknown"},
		{CompCompilable.String(), "Compilable"},
		{CompNotCompilable.String(), "NotCompilable"},

		{FBUnstable.String(), "Unstable"},
		{FBArithStableNumber.String(), "ArithStableNumber"},
		{FBTableMono.String(), "TableMono"},
		{FBTableMega.String(), "TableMega"},
		{FBGlobalStable.String(), "GlobalStable"},
		{FBSelfMono.String(), "SelfMono"},

		{CompileErrUnsupportedOpcodeShape.String(), "unsupported_opcode_shape"},
		{CompileErrOutOfResources.String(), "out_of_resources"},
		{CompileErrBackendPanic.String(), "backend_panic"},
		{CompileErrBackendDeclined.String(), "backend_declined"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("String mismatch: got %q want %q", c.got, c.want)
		}
	}
}

// TestProfileDataZeroValue 锁定 Go 零值即 TierInterp + CompUnknown
// (01 §6.5 profileTable 惰性建表的基石——`pd := &ProfileData{}` 即合法
// 起点,无需显式 set)。
func TestProfileDataZeroValue(t *testing.T) {
	pd := &ProfileData{}
	if pd.TierState != TierInterp {
		t.Errorf("zero TierState = %v, want TierInterp", pd.TierState)
	}
	if pd.Compilable != CompUnknown {
		t.Errorf("zero Compilable = %v, want CompUnknown", pd.Compilable)
	}
	if pd.EntryCount != 0 || pd.BackEdge != nil {
		t.Errorf("zero counters not clean: entry=%d backEdge=%v", pd.EntryCount, pd.BackEdge)
	}
	if pd.Reasons.HasAny() {
		t.Errorf("zero Reasons should not have any bits set")
	}
}

// TestBridgeProfileOfLazy 验证 profileTable 惰性建表(同一 Proto 多次 ProfileOf
// 应得到同一指针;不同 Proto 得到不同 pd)。
func TestBridgeProfileOfLazy(t *testing.T) {
	b := NewBridge()
	p1 := &bytecode.Proto{Code: make([]bytecode.Instruction, 4)}
	p2 := &bytecode.Proto{Code: make([]bytecode.Instruction, 8)}

	pd1a := b.ProfileOf(p1)
	pd1b := b.ProfileOf(p1)
	if pd1a != pd1b {
		t.Error("ProfileOf must return same pointer for same Proto")
	}
	pd2 := b.ProfileOf(p2)
	if pd2 == pd1a {
		t.Error("ProfileOf must return distinct pointers for distinct Protos")
	}
}

// TestOnBackEdgeAccumulates 验证回边计数自增 + 阈值前不触发升层。
//
// **PB0 没有真升层**——OnBackEdge 越阈值后调 considerPromotion 是 no-op,
// TierState 仍是 TierInterp。本测试锁定 PB0 的占位语义,PB4 落地后会被
// 加强(届时 TierState 应转 TierStuck:Compilable=CompUnknown 视同
// CompNotCompilable,03 §5.5)。
func TestOnBackEdgeAccumulates(t *testing.T) {
	b := NewBridge()
	p := &bytecode.Proto{Code: make([]bytecode.Instruction, 8)}

	for i := uint32(0); i < 5; i++ {
		b.OnBackEdge(p, 3)
	}
	pd := b.ProfileOf(p)
	if pd.BackEdge[3] != 5 {
		t.Errorf("backEdge[3] = %d, want 5", pd.BackEdge[3])
	}
	if pd.TierState != TierInterp {
		t.Errorf("TierState = %v, want TierInterp (PB0 no-op)", pd.TierState)
	}
}

// TestOnEnterAccumulates 函数入口计数自增。
func TestOnEnterAccumulates(t *testing.T) {
	b := NewBridge()
	p := &bytecode.Proto{Code: make([]bytecode.Instruction, 4)}

	for i := 0; i < 3; i++ {
		b.OnEnter(p)
	}
	pd := b.ProfileOf(p)
	if pd.EntryCount != 3 {
		t.Errorf("entryCount = %d, want 3", pd.EntryCount)
	}
}

// TestTierGuardBlocksCounting 验证 TierState != TierInterp 时 onBackEdge /
// onEnter 直接 return(01 §4.1 守卫)——已升 Gibbous / 已卡 Stuck 的 Proto
// 不应再累计计数。
func TestTierGuardBlocksCounting(t *testing.T) {
	b := NewBridge()
	p := &bytecode.Proto{Code: make([]bytecode.Instruction, 4)}
	pd := b.ProfileOf(p)

	pd.TierState = TierStuck
	b.OnBackEdge(p, 0)
	b.OnEnter(p)

	if pd.EntryCount != 0 {
		t.Errorf("entryCount must stay 0 under TierStuck guard, got %d", pd.EntryCount)
	}
	if pd.BackEdge != nil {
		t.Errorf("backEdge must remain nil under TierStuck guard, got %v", pd.BackEdge)
	}
}

// TestSetCompilability 锁定一次写、运行期只读语义(03 §5.4)。
func TestSetCompilability(t *testing.T) {
	b := NewBridge()
	p := &bytecode.Proto{Code: make([]bytecode.Instruction, 4)}

	if got := b.CompilabilityOf(p); got != CompUnknown {
		t.Errorf("initial CompilabilityOf = %v, want CompUnknown", got)
	}
	b.SetCompilability(p, CompNotCompilable, ReasonVararg|ReasonOverSize)
	if got := b.CompilabilityOf(p); got != CompNotCompilable {
		t.Errorf("CompilabilityOf after set = %v, want CompNotCompilable", got)
	}
	pd := b.ProfileOf(p)
	if !pd.Reasons.HasAny() {
		t.Errorf("Reasons should have bits set after SetCompilability")
	}
}

// TestProfileDataMaxBackEdge 验证 MaxBackEdge 取最大单回边累计(用于诊断
// 日志「累计 N 次回边」,01 §2.5 (a) 升层后保留 backEdge 用)。
func TestProfileDataMaxBackEdge(t *testing.T) {
	pd := &ProfileData{BackEdge: []uint32{3, 17, 5, 9}}
	if got := pd.MaxBackEdge(); got != 17 {
		t.Errorf("MaxBackEdge = %d, want 17", got)
	}

	emptyPd := &ProfileData{}
	if got := emptyPd.MaxBackEdge(); got != 0 {
		t.Errorf("empty MaxBackEdge = %d, want 0", got)
	}
}
