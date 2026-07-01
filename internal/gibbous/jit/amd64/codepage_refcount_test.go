//go:build wangshu_p4 && linux && amd64

package amd64

import (
	"sync"
	"sync/atomic"
	"testing"
)

// mkPage allocates a small executable segment for lifecycle tests. The
// segment body only needs to be a valid RET so any code that lands on it
// returns cleanly, but these tests never execute the code -- they only
// exercise the refcount lifecycle.
func mkPage(t *testing.T) *CodePage {
	t.Helper()
	// Single-byte RET (0xC3) is enough to satisfy MmapCode's non-empty
	// input check; MmapCode rounds to a full page.
	page, err := MmapCode([]byte{0xC3})
	if err != nil {
		t.Fatalf("MmapCode: %v", err)
	}
	return page
}

// TestCodePage_InitialRefcount verifies the constructor sets refcount to 1
// (the caller / constructor's own reference).
func TestCodePage_InitialRefcount(t *testing.T) {
	page := mkPage(t)
	defer page.Dispose()
	if r := page.refcount.Load(); r != 1 {
		t.Fatalf("initial refcount = %d, want 1", r)
	}
	if page.disposed.Load() {
		t.Fatal("initial disposed = true, want false")
	}
}

// TestCodePage_EnterExitPair verifies Enter/Exit are refcount-balanced.
func TestCodePage_EnterExitPair(t *testing.T) {
	page := mkPage(t)
	defer page.Dispose()

	if !page.Enter() {
		t.Fatal("Enter returned false on live page")
	}
	if r := page.refcount.Load(); r != 2 {
		t.Fatalf("refcount after Enter = %d, want 2", r)
	}
	page.Exit()
	if r := page.refcount.Load(); r != 1 {
		t.Fatalf("refcount after Exit = %d, want 1", r)
	}
}

// TestCodePage_DisposeBlocksEnter verifies that once Dispose is called,
// subsequent Enter returns false. This is the primary anti-UAF guarantee:
// a Run started after Dispose refuses to execute.
func TestCodePage_DisposeBlocksEnter(t *testing.T) {
	page := mkPage(t)
	if err := page.Dispose(); err != nil {
		t.Fatalf("Dispose: %v", err)
	}
	if page.Enter() {
		t.Fatal("Enter returned true after Dispose; expected false")
	}
	if !page.disposed.Load() {
		t.Fatal("disposed flag not set after Dispose")
	}
}

// TestCodePage_DisposeIdempotent verifies repeat Dispose is a no-op.
func TestCodePage_DisposeIdempotent(t *testing.T) {
	page := mkPage(t)
	if err := page.Dispose(); err != nil {
		t.Fatalf("first Dispose: %v", err)
	}
	if err := page.Dispose(); err != nil {
		t.Fatalf("second Dispose: %v", err)
	}
}

// TestCodePage_DeferredMunmap verifies that Dispose during an active Enter
// does not munmap; the actual release is deferred to the last Exit.
func TestCodePage_DeferredMunmap(t *testing.T) {
	page := mkPage(t)
	if !page.Enter() {
		t.Fatal("Enter returned false on live page")
	}
	// mem should still be non-nil while Enter holds a ref.
	if page.mem == nil {
		t.Fatal("mem nil after Enter (should be alive)")
	}
	// Dispose flips the flag but does not munmap (the Enter above still
	// holds a ref).
	if err := page.Dispose(); err != nil {
		t.Fatalf("Dispose: %v", err)
	}
	if page.mem == nil {
		t.Fatal("mem nil after Dispose while Enter still holds a ref; munmap fired too early")
	}
	if !page.disposed.Load() {
		t.Fatal("disposed flag not set")
	}
	// Now Exit drops the last ref; munmap fires here.
	page.Exit()
	if page.mem != nil {
		t.Fatal("mem still non-nil after last Exit; munmap did not fire")
	}
}

// TestCodePage_ConcurrentRunVsDispose is the multi-State UAF regression
// test: N goroutines repeatedly Enter/Exit while one goroutine Dispose's
// the segment. The test passes if:
//   - No goroutine panics on nil mem access.
//   - After the storm settles, refcount == 0 and mem == nil.
//   - Every Enter that succeeded was paired with a matched Exit (checked
//     via a workDone counter symmetric with an enterCount counter -- if
//     the refcount protocol is intact, workDone == enterCount).
//
// Runs with -race to catch any read/write race on refcount / disposed /
// mem observable to the Go race detector.
func TestCodePage_ConcurrentRunVsDispose(t *testing.T) {
	const workers = 32
	const iterationsPerWorker = 1000

	page := mkPage(t)

	var enterCount, workDone atomic.Int64

	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterationsPerWorker; j++ {
				if !page.Enter() {
					// Post-Dispose Enter refusal is expected and safe.
					continue
				}
				enterCount.Add(1)
				// Simulate a tiny amount of work inside the "hot" section.
				// If disposed too eagerly and mem was nil here, subsequent
				// tests would fail on the final invariants.
				_ = page.Addr()
				workDone.Add(1)
				page.Exit()
			}
		}()
	}

	// Give workers a chance to spin up before Dispose fires.
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Dispose can happen at any point; the invariant is that Run's Enter
		// refusal + Dispose's flag + refcounted deferred munmap co-operate.
		_ = page.Dispose()
	}()

	wg.Wait()

	// Post-storm invariants:
	if enterCount.Load() != workDone.Load() {
		t.Fatalf("Enter and Exit counts drift: enterCount=%d workDone=%d",
			enterCount.Load(), workDone.Load())
	}
	if !page.disposed.Load() {
		t.Fatal("disposed flag not set after storm")
	}
	if r := page.refcount.Load(); r != 0 {
		t.Fatalf("refcount after storm = %d, want 0", r)
	}
	if page.mem != nil {
		t.Fatal("mem still non-nil after storm; munmap did not fire")
	}
}
