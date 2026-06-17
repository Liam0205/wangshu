// Mock 子包冒烟测试——验证三种 mock 行为变体确实驱动 Bridge 状态机走对应路径。
package mock

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/bridge"
	"github.com/Liam0205/wangshu/internal/bytecode"
)

func makeProto() *bytecode.Proto {
	// Code 长度 ≥ MinPromotableCodeLen=10(issue #21):mock 测试 driver 自然热度
	// 路径,short proto 被守卫拦截。具体 opcode 值无关(mock P3 compiler 不解析
	// proto.Code),只需要长度足够过 MinPromotableCodeLen。
	code := make([]bytecode.Instruction, bridge.MinPromotableCodeLen)
	for i := range code {
		code[i] = bytecode.Instruction(uint32(bytecode.ADD))
	}
	return &bytecode.Proto{
		Code: code,
		IC:   make([]bytecode.ICSlot, bridge.MinPromotableCodeLen),
	}
}

// TestMock_DummyCompile_PromotesToGibbous DummyCompile + Compilable → Gibbous。
func TestMock_DummyCompile_PromotesToGibbous(t *testing.T) {
	b := bridge.NewBridge()
	b.SetP3Compiler(DummyCompile{})
	p := makeProto()
	pd := b.ProfileOf(p)
	pd.Compilable = bridge.CompCompilable

	for i := uint32(0); i < bridge.HotEntryThreshold; i++ {
		b.OnEnter(p, true)
	}
	if pd.TierState != bridge.TierGibbous {
		t.Errorf("DummyCompile should promote to Gibbous, got %v", pd.TierState)
	}
}

// TestMock_RejectAll_F7Stuck RejectAll + AnalyzeProto → F7 拦下,Stuck。
func TestMock_RejectAll_F7Stuck(t *testing.T) {
	b := bridge.NewBridge()
	b.SetP3Compiler(RejectAll{})
	p := makeProto()
	// 模拟 PB3 已分析(任何 Proto 即便 F1-F6 全过,也被 F7 拦下)
	b.SetCompilability(p, bridge.CompNotCompilable, bridge.ReasonBackendUnsupp)

	pd := b.ProfileOf(p)
	for i := uint32(0); i < bridge.HotEntryThreshold; i++ {
		b.OnEnter(p, true)
	}
	if pd.TierState != bridge.TierStuck {
		t.Errorf("RejectAll should leave Proto in Stuck, got %v", pd.TierState)
	}
}

// TestMock_PanicOnce_RecoveredToStuck PanicOnce → defer recover 转 Stuck,
// panic 不逃逸。
func TestMock_PanicOnce_RecoveredToStuck(t *testing.T) {
	b := bridge.NewBridge()
	b.SetP3Compiler(PanicOnce{})
	p := makeProto()
	pd := b.ProfileOf(p)
	pd.Compilable = bridge.CompCompilable

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("panic must not escape, got %v", r)
		}
	}()

	for i := uint32(0); i < bridge.HotEntryThreshold; i++ {
		b.OnEnter(p, true)
	}
	if pd.TierState != bridge.TierStuck {
		t.Errorf("PanicOnce should leave Proto in Stuck, got %v", pd.TierState)
	}
}
