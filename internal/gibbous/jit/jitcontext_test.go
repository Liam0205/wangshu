//go:build wangshu_p4

package jit

import (
	"sync"
	"testing"
)

// TestJITContext_NewReturnsNonNil: construction is non-nil (relied on by the
// wireP4 load path).
func TestJITContext_NewReturnsNonNil(t *testing.T) {
	ctx := NewJITContext()
	if ctx == nil {
		t.Fatal("NewJITContext should return non-nil")
	}
}

// TestJITContext_PreemptFlag: the preempt flag works atomically (V18 -race
// friendly; guarantees atomic.Uint32 introduces no data race).
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

// TestJITContext_OptionBAddrFields PJ5 Option B Spike 1: verifies the
// ciDepthAddr / ciSegBaseAddr / topAddr setters/getters work + initial 0
// (per §9.20).
func TestJITContext_OptionBAddrFields(t *testing.T) {
	ctx := NewJITContext()

	// initial 0 (not yet wired)
	if ctx.CIDepthAddr() != 0 {
		t.Errorf("初始 ciDepthAddr 应 0,得 %d", ctx.CIDepthAddr())
	}
	if ctx.CISegBaseAddr() != 0 {
		t.Errorf("初始 ciSegBaseAddr 应 0,得 %d", ctx.CISegBaseAddr())
	}
	if ctx.TopAddr() != 0 {
		t.Errorf("初始 topAddr 应 0,得 %d", ctx.TopAddr())
	}

	// write then read back consistently
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

// TestJITContext_OptionBAddrOffsetsStable PJ5 Option B Spike 1: verifies that
// JITContextCIDepthAddrOffset / CISegBaseAddrOffset / TopAddrOffset are stable
// compile-time constants (for use by byte-level emit of the mmap template).
func TestJITContext_OptionBAddrOffsetsStable(t *testing.T) {
	// The three offsets must be mutually non-overlapping and non-zero (per the
	// field definitions coming after other fields)
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
	// 8-byte aligned (uintptr field)
	if JITContextCIDepthAddrOffset%8 != 0 {
		t.Errorf("JITContextCIDepthAddrOffset %% 8 = %d, want 0", JITContextCIDepthAddrOffset%8)
	}
}

// TestJITContext_ConcurrentPreempt: -race verifies preemptFlag is safe across
// multiple goroutines (V18 acceptance criterion).
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

	// after at least N SetPreemptFlag calls it must be 1
	if !ctx.PreemptFlagPending() {
		t.Error("N 次并发 SetPreemptFlag 后 preemptFlag 应为 1")
	}
}

// TestJITContext_ExitResumeFields PJ5 §9.20.9 trampoline exit-resume protocol
// commit-1: verifies the exitArg0 / resumeOff setters/getters work + initial 0.
func TestJITContext_ExitResumeFields(t *testing.T) {
	ctx := NewJITContext()

	// initial 0 (not yet enabled)
	if ctx.ExitArg0() != 0 {
		t.Errorf("初始 exitArg0 应 0,得 %d", ctx.ExitArg0())
	}
	if ctx.ResumeOff() != 0 {
		t.Errorf("初始 resumeOff 应 0,得 %d", ctx.ResumeOff())
	}

	// write then read back consistently
	ctx.SetExitArg0(HelperRunCallee)
	ctx.SetResumeOff(120)

	if ctx.ExitArg0() != HelperRunCallee {
		t.Errorf("SetExitArg0(HelperRunCallee=%d) 后回读 = %d", HelperRunCallee, ctx.ExitArg0())
	}
	if ctx.ResumeOff() != 120 {
		t.Errorf("SetResumeOff(120) 后回读 = %d", ctx.ResumeOff())
	}
}

// TestJITContext_ExitResumeOffsetsStable: verifies JITContextExitArg0Offset /
// ResumeOffOffset are stable compile-time constants (for use by byte-level emit
// of the trampoline asm).
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
	// 8-byte aligned (uint64 + uint32 with padding; exitArg0 is uint64, must be 8-aligned)
	if JITContextExitArg0Offset%8 != 0 {
		t.Errorf("JITContextExitArg0Offset %% 8 = %d, want 0 (uint64 字段)",
			JITContextExitArg0Offset%8)
	}
}

// TestJITContext_ExitReasonCodesUnique: verifies the exit reason constants are
// unique (guards against protocol collisions).
func TestJITContext_ExitReasonCodesUnique(t *testing.T) {
	codes := []uint32{ExitNormal, ExitError, ExitOSR, ExitInlineHelper}
	seen := make(map[uint32]bool)
	for _, c := range codes {
		if seen[c] {
			t.Errorf("exit reason code %d 重复", c)
		}
		seen[c] = true
	}
	// Make the protocol status codes explicit (per §9.20.9 (3))
	if ExitNormal != 0 || ExitError != 1 || ExitOSR != 2 || ExitInlineHelper != 3 {
		t.Errorf("协议状态码值变化(承 §9.20.9 (3) 设计)— ExitNormal=%d Error=%d OSR=%d InlineHelper=%d",
			ExitNormal, ExitError, ExitOSR, ExitInlineHelper)
	}
}

// TestJITContext_HelperRequestCodesUnique: verifies helper request codes are unique.
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

// TestJITContext_CodePageAddrField PJ5 §9.20.9 trampoline exit-resume protocol
// commit-3b: verifies the codePageAddr setter/getter works + initial 0 (the
// dispatcher computes resume entry = codePageAddr + resumeOff).
func TestJITContext_CodePageAddrField(t *testing.T) {
	ctx := NewJITContext()

	if ctx.CodePageAddr() != 0 {
		t.Errorf("初始 codePageAddr 应 0,得 %d", ctx.CodePageAddr())
	}

	ctx.SetCodePageAddr(0xCAFE0000)

	if ctx.CodePageAddr() != 0xCAFE0000 {
		t.Errorf("SetCodePageAddr(0xCAFE0000) 后回读 = 0x%X", ctx.CodePageAddr())
	}
}

// TestJITContext_CodePageAddrOffsetStable: verifies JITContextCodePageAddrOffset
// is a stable compile-time constant (for use by byte-level emit of the
// trampoline asm + Go wrapper).
func TestJITContext_CodePageAddrOffsetStable(t *testing.T) {
	if JITContextCodePageAddrOffset == 0 {
		t.Error("JITContextCodePageAddrOffset 不应为 0(承字段在末尾)")
	}
	// 8-byte aligned (uintptr field)
	if JITContextCodePageAddrOffset%8 != 0 {
		t.Errorf("JITContextCodePageAddrOffset %% 8 = %d, want 0",
			JITContextCodePageAddrOffset%8)
	}
	// should be after resumeOff(72) + padding(4) = 76, i.e. ≥ 80
	if JITContextCodePageAddrOffset < JITContextResumeOffOffset+4 {
		t.Errorf("CodePageAddrOffset = %d, 应 ≥ ResumeOffOffset+4 = %d",
			JITContextCodePageAddrOffset, JITContextResumeOffOffset+4)
	}
}
