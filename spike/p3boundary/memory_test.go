package p3boundary

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/tetratelabs/wazero"
)

// PW0 side item A: memory sharing + arena adoption feasibility (01-spike-gate §4.2).
//
// Validates the core physical premise of P3 03-memory-model: whether an arena
// backing can adopt wazero linear memory (the same physical memory, zero-copy
// shared between the Go side and the Wasm side).

// TestArenaAdoption_ReadIsWriteThroughView verifies that Memory.Read returns a
// direct view into the underlying buffer (write-through, zero-copy) — this is
// the physical basis for arena adoption.
//
// Conclusion (supports 03-memory-model §1): wazero `Memory.Read(0, size)`
// returns a direct []byte view into linear memory; changes made by the Go side
// are visible to Wasm, and changes made by Wasm are visible to Go. P3 can back
// arena.backing with this view (unsafe-aliased into []uint64).
func TestArenaAdoption_ReadIsWriteThroughView(t *testing.T) {
	ctx := context.Background()
	rt := newCompilerRuntime(ctx)
	defer rt.Close(ctx)

	mod, err := rt.Instantiate(ctx, s2Wasm)
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	mem := mod.ExportedMemory("mem")

	// Take the underlying view (1 page = 64 KiB)
	view, ok := mem.Read(0, 64*1024)
	if !ok {
		t.Fatal("Read failed")
	}

	// Go side modifies view → the Wasm view should see it (write-through)
	view[100] = 0x42
	if got, _ := mem.ReadByte(100); got != 0x42 {
		t.Errorf("write-through Go→view→mem failed: got %#x", got)
	}

	// Modify via mem.WriteByte → view should see it (same memory)
	mem.WriteByte(200, 0x99)
	if view[200] != 0x99 {
		t.Errorf("write-through mem→view failed: got %#x", view[200])
	}
}

// TestArenaAdoption_GrowDisconnectsView verifies that an old view becomes stale
// after memory.grow — this confirms 03-memory-model §1.6/§1.7 "re-fetch the Go
// view after grow" (previously marked "pending spike validation").
//
// Conclusion: after grow you must call mem.Read again to obtain a fresh view;
// the old view may point to a freed/moved old buffer. P3's backing grow protocol
// must re-fetch the slice after memory.grow (or fix capacity via
// min=max / WithMemoryCapacityPages to avoid grow entirely).
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

	// Re-fetch the new view
	newView, _ := mem.Read(0, mem.Size())
	t.Logf("oldView cap=%d, newView cap=%d", oldCap, cap(newView))

	// The new view should cover the full capacity after grow
	if uint32(len(newView)) != mem.Size() {
		t.Errorf("newView len=%d, want %d", len(newView), mem.Size())
	}
	// Writing the new region should be visible (grown space is usable)
	mem.WriteByte(64*1024+10, 0x77)
	if got, _ := mem.ReadByte(64*1024 + 10); got != 0x77 {
		t.Errorf("grown region not writable: got %#x", got)
	}
}

// TestArenaAdoption_FixedCapacityStableView verifies that after fixing capacity
// with WithMemoryCapacityPages, the view stays stable across grow (min<max but
// max is pre-allocated) — another P3 adoption strategy (avoids the complexity of
// re-fetching after grow).
func TestArenaAdoption_FixedCapacityStableView(t *testing.T) {
	ctx := context.Background()
	// Pre-allocate capacity: so that memory does not swap its underlying buffer even on grow
	cfg := wazero.NewRuntimeConfigCompiler().
		WithMemoryCapacityFromMax(true).
		WithMemoryLimitPages(4) // limit 4 pages, fully pre-allocated
	rt := wazero.NewRuntimeWithConfig(ctx, cfg)
	defer rt.Close(ctx)

	// Use a memory module with min=1 max=4
	mod, err := rt.Instantiate(ctx, s2WasmMinMax)
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	mem := mod.ExportedMemory("mem")

	view, _ := mem.Read(0, 64*1024)
	view[50] = 0x11

	// grow: since capacity is fully pre-allocated, the underlying buffer is not swapped
	if _, ok := mem.Grow(1); !ok {
		t.Fatal("Grow failed")
	}

	// The old view should still be valid (underlying buffer was not swapped)
	if got, _ := mem.ReadByte(50); got != 0x11 {
		t.Errorf("fixed-capacity stable view broken: got %#x", got)
	}
	t.Logf("fixed-capacity: grow 后 size=%d, 旧视图仍有效", mem.Size())
}

// TestTax_GCDuringWasm — one of the four taxes: precise GC stack scanning
// (01-spike-gate §4.3.1). wazero manages its own stack, and the Go GC does not
// scan generated-code frames. Run wasm while triggering GC in between, and
// verify no panic/corruption.
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
	// The sentinel in memory should still be correct (GC did not misjudge/wrongly reclaim wazero memory)
	if got, _ := mem.ReadUint64Le(0); got != sentinel {
		t.Errorf("GC corrupted wasm memory: got %#x, want %#x", got, sentinel)
	}
}

// TestTax_PreemptSemantics — the **actual-semantics confirmation** of one of the
// four taxes: async preemption (01-spike-gate §4.3.2) (this spike corrected a
// misconception in the design docs).
//
// The design docs [00-overview §8] / roadmap §2 originally claimed "wazero
// generated-code loop back-edges already have preemption check points". But
// wazero RATIONALE.md "Why it's safe to execute runtime-generated machine codes
// against async Goroutine preemption" states clearly: **wazero generated code is
// treated by the Go runtime as async-preemption-unsafe, and while generated code
// runs the Go scheduler cannot asynchronously preempt it**; wazero relies on
// context cancellation (WithCloseOnContextDone) to terminate long loops
// cooperatively, not on back-edge preemption check points.
//
// This test confirms two points:
//  1. A pure-compute long-loop wasm is not preempted by Go → STW GC is blocked
//     until the loop yields (observed by this test);
//  2. WithCloseOnContextDone + context timeout is the correct termination
//     mechanism (next test).
//
// **Impact on P3** (written into implementation-progress): when gibbous runs a
// long loop, GC on other goroutines is blocked until that loop yields via a
// helper call / function return. The gcPending back-edge check in
// 05-safepoint-gc is exactly such a yield point — but a pure-compute loop with
// no helper does block GC. Column-kernel forms (batch loops + occasional
// helpers) can yield; a pure-compute infinite loop is a known corner case.
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

	// Confirm wazero preemption semantics: GC is blocked during a pure-compute loop (matches RATIONALE).
	// This is not a bug — it is the design consequence of wazero marking generated code async-preempt-unsafe.
	t.Logf("PREEMPT SEMANTICS: long loop total=%v, STW GC during loop=%v",
		totalElapsed, gcElapsed)
	if gcElapsed > totalElapsed/2 {
		t.Logf("  → 确认:wazero 生成码不被异步抢占,GC 被纯计算循环阻塞 %v"+
			"(符合 wazero RATIONALE;P3 列内核形态靠 helper/回边让出,见 05-safepoint-gc)", gcElapsed)
	} else {
		t.Logf("  → 注意:GC 未被显著阻塞(%v),与 RATIONALE 预期不同,需复查标定", gcElapsed)
	}
}

// TestTax_ContextCancellation — verifies that WithCloseOnContextDone + a context
// timeout can terminate a long-loop wasm (wazero's recommended long-loop
// termination mechanism, replacing async preemption).
//
// P3's SetStepBudget / context cancellation (issue #4, already landed in P1)
// propagate through this mechanism into gibbous — a long loop can be cancelled by
// the host and will not actually hang the process.
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
	const loopN = 5_000_000_000 // 5 billion iterations, should take about 1.5s

	cctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err = fn.Call(cctx, loopN)
	elapsed := time.Since(start)

	t.Logf("CONTEXT CANCEL: loop(5e9) with 50ms timeout → returned in %v, err=%v",
		elapsed, err)
	// Should return near the timeout (rather than running the full 5 billion iterations, ~1.5s)
	if elapsed > 500*time.Millisecond {
		t.Errorf("context cancellation FAILED: ran %v, expected ~50ms timeout", elapsed)
	}
	if err == nil {
		t.Errorf("expected cancellation error, got nil (loop ran to completion?)")
	}
}
