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
