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
