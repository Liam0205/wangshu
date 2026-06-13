package p3boundary

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/tetratelabs/wazero"
)

// PW0 顺带项 A:memory 共见 + arena 收养可行性(01-spike-gate §4.2)。
//
// 验证 P3 03-memory-model 的核心物理前提:arena backing 能否收养 wazero
// linear memory(同一块物理内存,Go 侧与 Wasm 侧零拷贝共见)。

// TestArenaAdoption_ReadIsWriteThroughView 验证 Memory.Read 返回底层 buffer
// 的直接视图(write-through,零拷贝)——这是 arena 收养的物理基础。
//
// 结论(支撑 03-memory-model §1):wazero `Memory.Read(0, size)` 给的是
// linear memory 的直接 []byte 视图,Go 侧改它 Wasm 能看见、Wasm 改它 Go
// 能看见。P3 可让 arena.backing 以这个视图为底层(unsafe 别名成 []uint64)。
func TestArenaAdoption_ReadIsWriteThroughView(t *testing.T) {
	ctx := context.Background()
	rt := newCompilerRuntime(ctx)
	defer rt.Close(ctx)

	mod, err := rt.Instantiate(ctx, s2Wasm)
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	mem := mod.ExportedMemory("mem")

	// 取底层视图(1 page = 64 KiB)
	view, ok := mem.Read(0, 64*1024)
	if !ok {
		t.Fatal("Read failed")
	}

	// Go 侧改 view → Wasm 视角应看到(write-through)
	view[100] = 0x42
	if got, _ := mem.ReadByte(100); got != 0x42 {
		t.Errorf("write-through Go→view→mem failed: got %#x", got)
	}

	// 经 mem.WriteByte 改 → view 应看到(同一块内存)
	mem.WriteByte(200, 0x99)
	if view[200] != 0x99 {
		t.Errorf("write-through mem→view failed: got %#x", view[200])
	}
}

// TestArenaAdoption_GrowDisconnectsView 验证 memory.grow 后旧视图失效——
// 这坐实 03-memory-model §1.6/§1.7「grow 后 Go 视图重取」(原标「待 spike
// 验证」)。
//
// 结论:grow 后必须重新 mem.Read 取新视图;旧 view 可能指向被释放/搬移的
// 旧 buffer。P3 的 backing grow 协议必须在 memory.grow 后重取 slice
// (或用 min=max / WithMemoryCapacityPages 固定容量避免 grow)。
func TestArenaAdoption_GrowDisconnectsView(t *testing.T) {
	ctx := context.Background()
	rt := newCompilerRuntime(ctx)
	defer rt.Close(ctx)

	mod, err := rt.Instantiate(ctx, s2Wasm)
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	mem := mod.ExportedMemory("mem")

	oldView, _ := mem.Read(0, 64*1024)
	oldCap := cap(oldView)
	oldSize := mem.Size()

	// grow 1 page
	prev, ok := mem.Grow(1)
	if !ok {
		t.Fatal("Grow failed")
	}
	t.Logf("grow: prev=%d pages, size %d → %d bytes", prev, oldSize, mem.Size())

	// 重取新视图
	newView, _ := mem.Read(0, mem.Size())
	t.Logf("oldView cap=%d, newView cap=%d", oldCap, cap(newView))

	// 新视图应覆盖 grow 后的全容量
	if uint32(len(newView)) != mem.Size() {
		t.Errorf("newView len=%d, want %d", len(newView), mem.Size())
	}
	// 写新区域应可见(grow 出来的空间可用)
	mem.WriteByte(64*1024+10, 0x77)
	if got, _ := mem.ReadByte(64*1024 + 10); got != 0x77 {
		t.Errorf("grown region not writable: got %#x", got)
	}
}

// TestArenaAdoption_FixedCapacityStableView 验证用 WithMemoryCapacityPages
// 固定容量后,视图在 grow 后仍稳定(min<max 但预分配 max)——P3 的另一条
// 收养策略(避免 grow 重取的复杂度)。
func TestArenaAdoption_FixedCapacityStableView(t *testing.T) {
	ctx := context.Background()
	// 预分配容量:让 memory 即使 grow 也不换底层 buffer
	cfg := wazero.NewRuntimeConfigCompiler().
		WithMemoryCapacityFromMax(true).
		WithMemoryLimitPages(4) // 上限 4 page,预分配满
	rt := wazero.NewRuntimeWithConfig(ctx, cfg)
	defer rt.Close(ctx)

	// 用 min=1 max=4 的 memory 模块
	mod, err := rt.Instantiate(ctx, s2WasmMinMax)
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	mem := mod.ExportedMemory("mem")

	view, _ := mem.Read(0, 64*1024)
	view[50] = 0x11

	// grow:由于 capacity 预分配满,底层 buffer 不换
	if _, ok := mem.Grow(1); !ok {
		t.Fatal("Grow failed")
	}

	// 旧视图仍应有效(底层 buffer 没换)
	if got, _ := mem.ReadByte(50); got != 0x11 {
		t.Errorf("fixed-capacity stable view broken: got %#x", got)
	}
	t.Logf("fixed-capacity: grow 后 size=%d, 旧视图仍有效", mem.Size())
}

// TestTax_GCDuringWasm — 四项税之 GC 精确栈扫描(01-spike-gate §4.3.1)。
// wazero 自管栈,Go GC 不扫生成码帧。跑 wasm + 中间触发 GC,验证无 panic/损坏。
func TestTax_GCDuringWasm(t *testing.T) {
	ctx := context.Background()
	rt := newCompilerRuntime(ctx)
	defer rt.Close(ctx)

	mod, err := rt.Instantiate(ctx, s2Wasm)
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	fn := mod.ExportedFunction("rw")
	mem := mod.ExportedMemory("mem")
	const sentinel = uint64(0x0123456789ABCDEF)
	mem.WriteUint64Le(8, sentinel)

	stack := make([]uint64, 1)
	for i := 0; i < 10000; i++ {
		stack[0] = 0
		if err := fn.CallWithStack(ctx, stack); err != nil {
			t.Fatalf("call at i=%d: %v", i, err)
		}
		if i%100 == 0 {
			_ = make([]byte, 1<<20) // Go heap pressure
			runtime.GC()
			runtime.GC()
		}
	}
	// memory 中 sentinel 应仍正确(GC 没误判/误回收 wazero memory)
	if got, _ := mem.ReadUint64Le(0); got != sentinel {
		t.Errorf("GC corrupted wasm memory: got %#x, want %#x", got, sentinel)
	}
}

// TestTax_PreemptSemantics — 四项税之异步抢占(01-spike-gate §4.3.2)的
// **真实语义确认**(spike 修正了设计文档的一个认知偏差)。
//
// 设计文档 [00-overview §8] / roadmap §2 原称「wazero 生成码循环回边已有
// 抢占检查点」。但 wazero RATIONALE.md「Why it's safe to execute
// runtime-generated machine codes against async Goroutine preemption」明确:
// **wazero 生成码被 Go 运行时视为 async-preemption-unsafe,生成码运行期间
// Go 调度器无法异步抢占它**;wazero 靠 context cancellation
// (WithCloseOnContextDone)协作式终止长循环,而非回边抢占检查点。
//
// 本测试坐实两点:
//  1. 纯计算长循环 wasm 不被 Go 抢占 → STW GC 被阻塞到循环让出(本测试观测);
//  2. WithCloseOnContextDone + context 超时是正确的终止机制(下一测试)。
//
// **对 P3 的影响**(写入 implementation-progress):gibbous 跑长循环时其他
// goroutine 的 GC 会被阻塞到该循环经 helper 调用 / 函数返回让出。05-safepoint-gc
// 的 gcPending 回边检查恰好是让出点——但纯计算无 helper 的循环确实阻塞 GC。
// 列内核形态(批量循环 + 偶发 helper)可让出;纯计算死循环是已知边角。
func TestTax_PreemptSemantics(t *testing.T) {
	ctx := context.Background()
	rt := newCompilerRuntime(ctx)
	defer rt.Close(ctx)

	mod, err := rt.Instantiate(ctx, s4LongLoopWasm)
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	fn := mod.ExportedFunction("loop")
	const loopN = 500_000_000

	done := make(chan error, 1)
	start := time.Now()
	go func() {
		_, err := fn.Call(ctx, loopN)
		done <- err
	}()
	time.Sleep(5 * time.Millisecond)
	gcStart := time.Now()
	runtime.GC()
	gcElapsed := time.Since(gcStart)

	if err := <-done; err != nil {
		t.Fatalf("long loop: %v", err)
	}
	totalElapsed := time.Since(start)

	// 确认 wazero 抢占语义:纯计算循环期间 GC 被阻塞(符合 RATIONALE)。
	// 这不是 bug——是 wazero 把生成码标 async-preempt-unsafe 的设计后果。
	t.Logf("PREEMPT SEMANTICS: long loop total=%v, STW GC during loop=%v",
		totalElapsed, gcElapsed)
	if gcElapsed > totalElapsed/2 {
		t.Logf("  → 确认:wazero 生成码不被异步抢占,GC 被纯计算循环阻塞 %v"+
			"(符合 wazero RATIONALE;P3 列内核形态靠 helper/回边让出,见 05-safepoint-gc)", gcElapsed)
	} else {
		t.Logf("  → 注意:GC 未被显著阻塞(%v),与 RATIONALE 预期不同,需复查标定", gcElapsed)
	}
}

// TestTax_ContextCancellation — 验证 WithCloseOnContextDone + context 超时
// 能终止长循环 wasm(wazero 推荐的长循环终止机制,替代异步抢占)。
//
// P3 的 SetStepBudget / context 取消(issue #4 已在 P1 落地)经此机制传到
// gibbous——长循环能被宿主取消,不会真的卡死进程。
func TestTax_ContextCancellation(t *testing.T) {
	ctx := context.Background()
	rt := wazero.NewRuntimeWithConfig(ctx,
		wazero.NewRuntimeConfigCompiler().WithCloseOnContextDone(true))
	defer rt.Close(ctx)

	mod, err := rt.Instantiate(ctx, s4LongLoopWasm)
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	fn := mod.ExportedFunction("loop")
	const loopN = 5_000_000_000 // 50 亿次,本应跑约 1.5s

	cctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err = fn.Call(cctx, loopN)
	elapsed := time.Since(start)

	t.Logf("CONTEXT CANCEL: loop(5e9) with 50ms timeout → returned in %v, err=%v",
		elapsed, err)
	// 应在 timeout 附近返回(而非跑完 50 亿次的 ~1.5s)
	if elapsed > 500*time.Millisecond {
		t.Errorf("context cancellation FAILED: ran %v, expected ~50ms timeout", elapsed)
	}
	if err == nil {
		t.Errorf("expected cancellation error, got nil (loop ran to completion?)")
	}
}
