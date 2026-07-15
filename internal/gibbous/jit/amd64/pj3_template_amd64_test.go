//go:build wangshu_p4 && linux && amd64

package amd64

import (
	"math"
	"runtime"
	"testing"
)

// pj3_template_amd64_test.go —— PJ3 FORLOOP template real mmap+RX round-trip
// verification. Follows docs/design/p4-method-jit/05-system-pipeline.md §6.3.

// TestPJ3_ForLoopEmptyConst_RoundTrip: `for i=1,100 do end`, all-constant empty
// body. After the segment runs, rax is not read (rax state is irrelevant at the
// template's terminating ret; the point is to verify the segment RETs cleanly).
//
// What is actually verified: the template **really runs 99 back-edges + a final
// ja exit under mmap+RX** -- if any byte of the backward jmp / ucomisd / ja is
// wrong, it SIGSEGVs / SIGILLs / spins forever (test timeout). A passing test =
// hard evidence of physical feasibility.
func TestPJ3_ForLoopEmptyConst_RoundTrip(t *testing.T) {
	cases := []struct {
		init, limit, step float64
		name              string
	}{
		{1, 100, 1, "1..100 step 1"},
		{1, 10, 1, "1..10 step 1"},
		{0, 0, 1, "0..0 step 1(单次)"},
		{1, 1, 1, "1..1 step 1(单次)"},
		{1, 1000, 1, "1..1000 step 1"},
		{1, 10, 2, "1..10 step 2"},
		{1, 100, 0.5, "1..100 step 0.5(200 次)"},
		// #117/#118 unordered-exit pins: a NaN in limit or init must
		// exit on the FIRST compare. Current emitter form:
		// `ucomisd limit, idx; jb after_loop` -- ucomisd sets
		// CF=ZF=PF=1 on unordered, so jb (CF=1) TAKES the exit jump.
		// (The original bug was the inverted `ucomisd idx, limit;
		// ja exit`: ja needs CF=0&&ZF=0, false on unordered, so the
		// segment spun forever.) Source-level carriers no longer
		// exist -- PUC-parity constant folding refuses to fold 0%0,
		// so a NaN can never reach a Proto const slot from source --
		// but the emitters still accept arbitrary bits and must stay
		// unordered-safe.
		{1, math.NaN(), 1, "NaN limit(unordered 立即退出)"},
		{math.NaN(), 10, 1, "NaN init(unordered 立即退出)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf []byte
			buf, _ = EmitForLoopEmptyConst(buf,
				math.Float64bits(tc.init),
				math.Float64bits(tc.limit),
				math.Float64bits(tc.step),
				-1, /* no safepoint check */
				-1, 0, 0)

			if len(buf) != EncodedForLoopEmptyConstLen {
				t.Fatalf("buf len=%d, want %d", len(buf), EncodedForLoopEmptyConstLen)
			}

			page, err := MmapCode(buf)
			if err != nil {
				t.Fatalf("MmapCode: %v", err)
			}
			defer func() { _ = page.Munmap() }()

			// CallJITSpec does not read vsBase (the template doesn't address
			// rbx); 0 works, but pass a dummy non-zero address as insurance in
			// case the segment reads rbx.
			dummyStack := make([]uint64, 4)
			vsBase := uintptr(0)
			_ = vsBase
			rax := CallJITSpec(page.Addr(), 0, uintptr(0))
			runtime.KeepAlive(dummyStack)

			// rax is the segment return value; at this template's terminating
			// ret rax is not a meaningful value (it may be the rax left after
			// the flag effect of the ucomisd before ja, or whatever the upper
			// trampoline preserved). We only verify the segment RETs normally
			// -- a spinning backward jmp → testing timeout; a wrong byte →
			// SIGSEGV.
			t.Logf("%s: rax=0x%x(段正常退出,backward jmp + ja 字节级跑通)",
				tc.name, rax)
		})
	}
}
