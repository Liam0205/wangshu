//go:build wangshu_p4 && linux && amd64

package amd64

import (
	"testing"
	"unsafe"
)

// emitter_pj3_loop_spike_test.go —— PJ3 byte-level backward jmp + safepoint
// check physical-feasibility spike (per docs/design/p4-method-jit/05-system-pipeline.md
// §1.2.2 preemption checkpoint + §6.3 back edge + 06-backends.md §3.3 numeric for inlining).
//
// **spike goal**: emit a simple counter loop inside a real mmap+RX segment:
//
//   mov rax, 0                 ; accumulator
//   mov rcx, N                 ; counter
//   loop_start:
//     inc rax                  ; accumulate ++
//     cmp byte [r15+pfOff], 0  ; read jitContext.preemptFlag
//     jne after_loop           ; pf!=0 early exit
//     dec rcx                  ; counter --
//     jne loop_start           ; back edge, backward jmp
//   after_loop:
//     ret                      ; rax = accumulated value
//
// Verification points:
//   ① backward jmp (negative rel32) actually runs under mmap+RX
//   ② cmp byte [r15+disp] actually reads a jitContext field (r15 set via trampoline)
//   ③ setting preemptFlag=1 mid-run → loop exits early → rax < N
//
// **prove-the-path**: this test verifies both byte-level encoding correctness
// and the physical behavior of backward jmp + r15 load — this is the most
// central physical evidence for PJ3 FORLOOP byte-level inline integration.

// Use a global heap-allocated jitContext (per 05 §1.3.4 + test §3.6, must be heap)
// to avoid morestack moving the *JITContext pointer.

// Computing the preemptFlag offset via the in-package JITContext struct +
// Offsetof — but the amd64 package cannot import the jit package (import cycle).
// Compute the offset by hand: JITContext struct field order is arenaBase /
// valueStackBase / preemptFlag / exitReasonCode / spillBase / spillTop. Three
// uintptr (24) are followed by an atomic.Uint32 (essentially a 4-byte uint32,
// but padded to 8-byte alignment) → preemptFlag offset = 24 bytes (stable on
// the amd64 64-bit platform).
//
// To avoid depending on the jit package's internal layout, this spike uses a
// **local mini ctx struct** (same field layout but defined inside the test
// package), with the offset computed via unsafe.Offsetof.

type spikeCtx struct {
	_           [3]uintptr // arenaBase + valueStackBase + ... (padding to align with JITContext's leading fields)
	preemptFlag uint32     // only 0/1 in practice, so a byte cmp suffices
}

var spikePreemptOff = int32(unsafe.Offsetof(spikeCtx{}.preemptFlag))

// Global heap-allocated ctx — per the prove-the-path rule "JIT holds no Go stack pointers".
var spikeCtxInstance = &spikeCtx{}

// TestPJ3_BackwardJmpLoop_Normal: preemptFlag=0, loop runs the full N iterations, rax=N.
func TestPJ3_BackwardJmpLoop_Normal(t *testing.T) {
	spikeCtxInstance.preemptFlag = 0
	const N = int32(100)

	buf := buildSpikeLoop(t, N, spikePreemptOff)
	page, err := MmapCode(buf)
	if err != nil {
		t.Fatalf("MmapCode: %v", err)
	}
	defer func() { _ = page.Munmap() }()

	ctxAddr := uintptr(unsafe.Pointer(spikeCtxInstance))
	rax := CallJITFull(page.Addr(), ctxAddr)

	if int32(rax) != N {
		t.Errorf("rax=%d, want N=%d(loop 应跑满 N 次)", int32(rax), N)
	}
}

// TestPJ3_BackwardJmpLoop_EarlyExit: preemptFlag=1, loop exits at the first
// safepoint check, rax=1 (the accumulator runs inc only once).
func TestPJ3_BackwardJmpLoop_EarlyExit(t *testing.T) {
	spikeCtxInstance.preemptFlag = 1
	defer func() { spikeCtxInstance.preemptFlag = 0 }()

	const N = int32(100)
	buf := buildSpikeLoop(t, N, spikePreemptOff)
	page, err := MmapCode(buf)
	if err != nil {
		t.Fatalf("MmapCode: %v", err)
	}
	defer func() { _ = page.Munmap() }()

	ctxAddr := uintptr(unsafe.Pointer(spikeCtxInstance))
	rax := CallJITFull(page.Addr(), ctxAddr)

	// preemptFlag=1 → after the first inc rax, cmp ≠ 0 jumps to after_loop, rax=1
	if int32(rax) != 1 {
		t.Errorf("rax=%d, want 1(preemptFlag=1 应让第一次循环就退出)", int32(rax))
	}
}

// buildSpikeLoop assembles the counter loop at the byte level (demonstrating the emit-then-patch flow).
func buildSpikeLoop(t *testing.T, n int32, pfOff int32) []byte {
	t.Helper()
	var buf []byte

	// 1. mov rax, 0 (7 bytes, EmitMovReg64Imm32SignExt)
	buf = EmitMovReg64Imm32SignExt(buf, 0 /*rax*/, 0)

	// 2. mov rcx, N (7 bytes)
	buf = EmitMovReg64Imm32SignExt(buf, 1 /*rcx*/, n)

	// 3. loop_start label, record the in-segment offset
	loopStart := len(buf)

	// 4. inc rax (3 bytes)
	buf = EmitIncReg64(buf, 0 /*rax*/)

	// 5. cmp byte [r15 + pfOff], 0 (8 bytes)
	buf = EmitCmpByteR15DispImm8(buf, pfOff, 0)

	// 6. jne after_loop placeholder rel32=0 (6 bytes) -- forward fixup
	buf = EmitJneRel32(buf, 0)
	jneForwardRel32Off := len(buf) - 4

	// 7. dec rcx (3 bytes)
	buf = EmitDecReg64(buf, 1 /*rcx*/)

	// 8. jne loop_start (6 bytes) -- backward jmp, rel32 can be computed right here
	//    backward jcc end = len(buf) + 6
	//    rel32 = loopStart - (jccEnd) = loopStart - (len(buf) + 6)
	backwardRel32 := int32(loopStart - (len(buf) + EncodedJccRel32Len))
	buf = EmitJneRel32(buf, backwardRel32)

	// 9. after_loop label
	afterLoop := len(buf)

	// 10. ret (1 byte)
	buf = EmitRet(buf)

	// 11. patch the forward jne (step 6): rel32 = afterLoop - (jne_end)
	//     jne_end = jne_start + EncodedJccRel32Len; jne_start = jneForwardRel32Off - 2;
	//     jne_end = jneForwardRel32Off + 4 (rel32 start + 4)
	forwardRel32 := int32(afterLoop) - int32(jneForwardRel32Off+4)
	PatchRel32(buf, jneForwardRel32Off, forwardRel32)

	return buf
}
