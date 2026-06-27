//go:build wangshu_p4

package jit

import (
	"sync"
	"testing"
)

// TestJITContext_NewReturnsNonNil 构造不 nil(承 wireP4 装载路径依赖)。
func TestJITContext_NewReturnsNonNil(t *testing.T) {
	ctx := NewJITContext()
	if ctx == nil {
		t.Fatal("NewJITContext should return non-nil")
	}
}

// TestJITContext_PreemptFlag preempt 标志位 atomic 工作(V18 -race 友好,承
// 承诺 atomic.Uint32 不引入数据竞争)。
func TestJITContext_PreemptFlag(t *testing.T) {
	ctx := NewJITContext()

	if ctx.PreemptFlagPending() {
		t.Error("初始 preemptFlag 应为 0")
	}

	ctx.SetPreemptFlag()
	if !ctx.PreemptFlagPending() {
		t.Error("SetPreemptFlag 后应为 1")
	}

	ctx.ClearPreemptFlag()
	if ctx.PreemptFlagPending() {
		t.Error("ClearPreemptFlag 后应为 0")
	}
}

// TestJITContext_OptionBAddrFields PJ5 Option B Spike 1:验 ciDepthAddr /
// ciSegBaseAddr / topAddr 三 setter/getter 工作 + 初始 0(承 §9.20)。
func TestJITContext_OptionBAddrFields(t *testing.T) {
	ctx := NewJITContext()

	// 初始 0(未接入)
	if ctx.CIDepthAddr() != 0 {
		t.Errorf("初始 ciDepthAddr 应 0,得 %d", ctx.CIDepthAddr())
	}
	if ctx.CISegBaseAddr() != 0 {
		t.Errorf("初始 ciSegBaseAddr 应 0,得 %d", ctx.CISegBaseAddr())
	}
	if ctx.TopAddr() != 0 {
		t.Errorf("初始 topAddr 应 0,得 %d", ctx.TopAddr())
	}

	// 写入后回读一致
	ctx.SetCIDepthAddr(0x100)
	ctx.SetCISegBaseAddr(0x200)
	ctx.SetTopAddr(0x300)

	if ctx.CIDepthAddr() != 0x100 {
		t.Errorf("SetCIDepthAddr(0x100) 后回读 = %d, want 0x100", ctx.CIDepthAddr())
	}
	if ctx.CISegBaseAddr() != 0x200 {
		t.Errorf("SetCISegBaseAddr(0x200) 后回读 = %d, want 0x200", ctx.CISegBaseAddr())
	}
	if ctx.TopAddr() != 0x300 {
		t.Errorf("SetTopAddr(0x300) 后回读 = %d, want 0x300", ctx.TopAddr())
	}
}

// TestJITContext_OptionBAddrOffsetsStable PJ5 Option B Spike 1:验
// JITContextCIDepthAddrOffset / CISegBaseAddrOffset / TopAddrOffset
// 编译期常量稳定(供 mmap 模板字节级 emit 使用)。
func TestJITContext_OptionBAddrOffsetsStable(t *testing.T) {
	// 三 offset 必须互不重叠且非 0(承字段定义后于其他字段)
	if JITContextCIDepthAddrOffset == 0 {
		t.Error("JITContextCIDepthAddrOffset 不应为 0(承字段在 spillTop 后)")
	}
	if JITContextCISegBaseAddrOffset == 0 {
		t.Error("JITContextCISegBaseAddrOffset 不应为 0")
	}
	if JITContextTopAddrOffset == 0 {
		t.Error("JITContextTopAddrOffset 不应为 0")
	}
	if JITContextCIDepthAddrOffset == JITContextCISegBaseAddrOffset {
		t.Error("ciDepth/ciSegBase offset 不应相等")
	}
	if JITContextCISegBaseAddrOffset == JITContextTopAddrOffset {
		t.Error("ciSegBase/top offset 不应相等")
	}
	// 8 字节对齐(uintptr field)
	if JITContextCIDepthAddrOffset%8 != 0 {
		t.Errorf("JITContextCIDepthAddrOffset %% 8 = %d, want 0", JITContextCIDepthAddrOffset%8)
	}
}

// TestJITContext_ConcurrentPreempt -race 验证 preemptFlag 多 goroutine 安全
// (V18 验收口径)。
func TestJITContext_ConcurrentPreempt(t *testing.T) {
	ctx := NewJITContext()
	const N = 100

	var wg sync.WaitGroup
	wg.Add(2 * N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			ctx.SetPreemptFlag()
		}()
		go func() {
			defer wg.Done()
			_ = ctx.PreemptFlagPending()
		}()
	}
	wg.Wait()

	// 至少 N 次 SetPreemptFlag 后必为 1
	if !ctx.PreemptFlagPending() {
		t.Error("N 次并发 SetPreemptFlag 后 preemptFlag 应为 1")
	}
}

// TestJITContext_ExitResumeFields PJ5 §9.20.9 trampoline exit-resume 协议
// commit-1:验 exitArg0 / resumeOff 三 setter/getter 工作 + 初始 0。
func TestJITContext_ExitResumeFields(t *testing.T) {
	ctx := NewJITContext()

	// 初始 0(未启用)
	if ctx.ExitArg0() != 0 {
		t.Errorf("初始 exitArg0 应 0,得 %d", ctx.ExitArg0())
	}
	if ctx.ResumeOff() != 0 {
		t.Errorf("初始 resumeOff 应 0,得 %d", ctx.ResumeOff())
	}

	// 写入后回读一致
	ctx.SetExitArg0(HelperRunCallee)
	ctx.SetResumeOff(120)

	if ctx.ExitArg0() != HelperRunCallee {
		t.Errorf("SetExitArg0(HelperRunCallee=%d) 后回读 = %d", HelperRunCallee, ctx.ExitArg0())
	}
	if ctx.ResumeOff() != 120 {
		t.Errorf("SetResumeOff(120) 后回读 = %d", ctx.ResumeOff())
	}
}

// TestJITContext_ExitResumeOffsetsStable 验 JITContextExitArg0Offset /
// ResumeOffOffset 编译期常量稳定(供 trampoline asm 字节级 emit 使用)。
func TestJITContext_ExitResumeOffsetsStable(t *testing.T) {
	if JITContextExitArg0Offset == 0 {
		t.Error("JITContextExitArg0Offset 不应为 0(承字段位置在末尾)")
	}
	if JITContextResumeOffOffset == 0 {
		t.Error("JITContextResumeOffOffset 不应为 0")
	}
	if JITContextExitArg0Offset == JITContextResumeOffOffset {
		t.Error("exitArg0/resumeOff offset 不应相等")
	}
	// 8 字节对齐(uint64 + uint32 with padding;exitArg0 是 uint64 必 8 对齐)
	if JITContextExitArg0Offset%8 != 0 {
		t.Errorf("JITContextExitArg0Offset %% 8 = %d, want 0 (uint64 字段)",
			JITContextExitArg0Offset%8)
	}
}

// TestJITContext_ExitReasonCodesUnique 验 exit reason 常量唯一(防协议冲突)。
func TestJITContext_ExitReasonCodesUnique(t *testing.T) {
	codes := []uint32{ExitNormal, ExitError, ExitOSR, ExitInlineHelper}
	seen := make(map[uint32]bool)
	for _, c := range codes {
		if seen[c] {
			t.Errorf("exit reason code %d 重复", c)
		}
		seen[c] = true
	}
	// 显性化协议状态码(承 §9.20.9 (3))
	if ExitNormal != 0 || ExitError != 1 || ExitOSR != 2 || ExitInlineHelper != 3 {
		t.Errorf("协议状态码值变化(承 §9.20.9 (3) 设计)— ExitNormal=%d Error=%d OSR=%d InlineHelper=%d",
			ExitNormal, ExitError, ExitOSR, ExitInlineHelper)
	}
}

// TestJITContext_HelperRequestCodesUnique 验 helper request code 唯一。
func TestJITContext_HelperRequestCodesUnique(t *testing.T) {
	codes := []uint64{HelperRunCallee, HelperGrowStack, HelperGCBarrier}
	seen := make(map[uint64]bool)
	for _, c := range codes {
		if c == 0 {
			t.Errorf("helper code 不应为 0(0 留作 sentinel)")
		}
		if seen[c] {
			t.Errorf("helper code %d 重复", c)
		}
		seen[c] = true
	}
}
