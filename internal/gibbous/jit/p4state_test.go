//go:build wangshu_p4

package jit

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/bytecode"
)

// p4state_test.go — P4 内部投机子状态机单测(承
// docs/design/p4-method-jit/04-osr-deopt.md §5 状态转移表)。
//
// 本批落地是 OSR exit 协议的工程基础(p4SpecState map + DeoptThreshold /
// MaxRecompileTries 阈值占位 + onOSRExit / onP4Install 转移函数 + 探针),
// 等段内 EmitCallInline 投机模板真接入时激活。

// TestP4SpecState_DefaultIsUnknown 未注册 Proto 默认 P4SpecUnknown。
func TestP4SpecState_DefaultIsUnknown(t *testing.T) {
	ResetP4SpecState()
	proto := &bytecode.Proto{}
	if got := P4SpecStateOf(proto); got != P4SpecUnknown {
		t.Errorf("默认状态 = %v, want P4SpecUnknown", got)
	}
}

// TestP4SpecState_NilProtoSafe nil Proto 安全返回 P4SpecUnknown 不 panic。
func TestP4SpecState_NilProtoSafe(t *testing.T) {
	ResetP4SpecState()
	if got := P4SpecStateOf(nil); got != P4SpecUnknown {
		t.Errorf("nil proto = %v, want P4SpecUnknown", got)
	}
	onOSRExit(nil) // 不 panic
	onP4Install(nil)
}

// TestP4SpecState_InstallTransitions onP4Install:首次 → P4Speculative。
func TestP4SpecState_InstallTransitions(t *testing.T) {
	ResetP4SpecState()
	proto := &bytecode.Proto{}
	onP4Install(proto)
	if got := P4SpecStateOf(proto); got != P4Speculative {
		t.Errorf("首次 install 后状态 = %v, want P4Speculative", got)
	}
}

// TestP4SpecState_DeoptCountUnderThreshold 单次 OSR exit 不切状态。
func TestP4SpecState_DeoptCountUnderThreshold(t *testing.T) {
	ResetP4SpecState()
	ResetSpecHits()
	proto := &bytecode.Proto{}
	onP4Install(proto)
	for i := 0; i < int(DeoptThreshold-1); i++ {
		onOSRExit(proto)
	}
	if got := P4SpecStateOf(proto); got != P4Speculative {
		t.Errorf("deoptCount < 阈值时状态 = %v, want P4Speculative", got)
	}
	if SpecP4DeoptHits() != 0 {
		t.Errorf("阈值前 SpecP4DeoptHits = %d, want 0", SpecP4DeoptHits())
	}
}

// TestP4SpecState_DeoptCountReachThreshold 达 DeoptThreshold → P4Deoptimized。
func TestP4SpecState_DeoptCountReachThreshold(t *testing.T) {
	ResetP4SpecState()
	ResetSpecHits()
	proto := &bytecode.Proto{}
	onP4Install(proto)
	for i := 0; i < int(DeoptThreshold); i++ {
		onOSRExit(proto)
	}
	if got := P4SpecStateOf(proto); got != P4Deoptimized {
		t.Errorf("达阈值后状态 = %v, want P4Deoptimized", got)
	}
	if SpecP4DeoptHits() != 1 {
		t.Errorf("SpecP4DeoptHits = %d, want 1", SpecP4DeoptHits())
	}
}

// TestP4SpecState_RecompileTransitions P4Deoptimized → P4Speculative
// (重编译 recompileCount += 1)。
func TestP4SpecState_RecompileTransitions(t *testing.T) {
	ResetP4SpecState()
	ResetSpecHits()
	proto := &bytecode.Proto{}
	onP4Install(proto)
	// 触发 deopt
	for i := 0; i < int(DeoptThreshold); i++ {
		onOSRExit(proto)
	}
	if got := P4SpecStateOf(proto); got != P4Deoptimized {
		t.Fatalf("应已 P4Deoptimized,got %v", got)
	}
	// 重编译
	onP4Install(proto)
	if got := P4SpecStateOf(proto); got != P4Speculative {
		t.Errorf("重编译后状态 = %v, want P4Speculative", got)
	}
}

// TestP4SpecState_MaxRecompileTriesReachedStuck 重编译次数达上限 → P4StuckSpeculation。
func TestP4SpecState_MaxRecompileTriesReachedStuck(t *testing.T) {
	ResetP4SpecState()
	ResetSpecHits()
	proto := &bytecode.Proto{}
	onP4Install(proto)
	// 循环触发 deopt + 重编译 MaxRecompileTries 次
	for r := uint32(0); r < MaxRecompileTries; r++ {
		for i := 0; i < int(DeoptThreshold); i++ {
			onOSRExit(proto)
		}
		if got := P4SpecStateOf(proto); got != P4Deoptimized {
			t.Fatalf("第 %d 次 deopt 后应 P4Deoptimized,got %v", r, got)
		}
		onP4Install(proto) // 重编译
	}
	// 再次 deopt:recompileCount 已达 MaxRecompileTries,本批 deopt 切 P4StuckSpeculation
	for i := 0; i < int(DeoptThreshold); i++ {
		onOSRExit(proto)
	}
	if got := P4SpecStateOf(proto); got != P4StuckSpeculation {
		t.Errorf("达 MaxRecompileTries 后状态 = %v, want P4StuckSpeculation", got)
	}
	if SpecP4StuckHits() != 1 {
		t.Errorf("SpecP4StuckHits = %d, want 1", SpecP4StuckHits())
	}
}
