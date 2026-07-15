//go:build wangshu_p4 && linux && amd64

package amd64

import (
	"math"
	"runtime"
	"testing"
	"unsafe"
)

// pj2TestStack is the global heap slice used by the PJ2 integration test — it
// must be heap-allocated. A Go stack allocation could be moved by morestack,
// leaving the vsBase pointer stale (per 05 §1.3 "JIT holds no Go stack
// pointers"). Go's escape analysis guarantees a global var make always lands on the heap.
var pj2TestStack = make([]uint64, 16)

// TestPJ2_SpeculativeAddRoundTrip integration test: a template assembled with
// EmitArithSpeculativeAdd + MmapCode + CallJITSpec, executed for real on this
// amd64 machine, verifying that the two-number fast-path template correctly
// returns the floating-point sum of R(B) + R(C).
//
// **prove-the-path evidence**: the byte-level unit test (TestPJ2_SpeculativeAddTemplate)
// only verifies encoding-byte correctness; **this test actually mmap+RX+executes**
// the segment, verifying that ADDSD works on a real CPU + rbx addressing is
// correct + ret returns to the trampoline.
//
// **arena view-aliasing hazard evidence**: this test's pj2TestStack must be
// heap-allocated (a global var); a stack-allocated slice could be moved by
// morestack during the trampoline, leaving vsBase stale → the segment writes
// to a stale address → the test never sees the result. This is exactly the
// evidence for P4 design 05 §1.3 "JIT holds no Go stack pointers" — on the real
// P4 path arena.Words lives in the Go heap (arena is a heap object) and won't be moved.
func TestPJ2_SpeculativeAddRoundTrip(t *testing.T) {
	pj2TestStack[0] = math.Float64bits(3.0)
	pj2TestStack[1] = math.Float64bits(4.0)
	pj2TestStack[2] = 0

	vsBase := uintptr(unsafe.Pointer(&pj2TestStack[0]))

	// Assemble the template: ADD A=2 B=0 C=1
	var buf []byte
	buf = EmitArithSpeculativeAdd(buf, 2, 0, 1)

	page, err := MmapCode(buf)
	if err != nil {
		t.Fatalf("MmapCode failed: %v", err)
	}
	defer func() { _ = page.Munmap() }()

	CallJITSpec(page.Addr(), 0, vsBase)
	runtime.KeepAlive(pj2TestStack) // prevent GC from moving the slice during the trampoline

	got := math.Float64frombits(pj2TestStack[2])
	if got != 7.0 {
		t.Errorf("R(2) = %v, want 7.0(R(0) + R(1) = 3.0 + 4.0)", got)
	}

	// Multiple value cases
	pj2TestStack[0] = math.Float64bits(1.5)
	pj2TestStack[1] = math.Float64bits(2.5)
	pj2TestStack[2] = 0
	CallJITSpec(page.Addr(), 0, vsBase)
	runtime.KeepAlive(pj2TestStack)
	if got := math.Float64frombits(pj2TestStack[2]); got != 4.0 {
		t.Errorf("R(2) = %v, want 4.0(1.5 + 2.5)", got)
	}

	pj2TestStack[0] = math.Float64bits(-10.0)
	pj2TestStack[1] = math.Float64bits(3.14)
	pj2TestStack[2] = 0
	CallJITSpec(page.Addr(), 0, vsBase)
	runtime.KeepAlive(pj2TestStack)
	if got := math.Float64frombits(pj2TestStack[2]); math.Abs(got-(-6.86)) > 1e-10 {
		t.Errorf("R(2) = %v, want -6.86(-10 + 3.14)", got)
	}
}

// TestPJ2_SpeculativeAddWithGuard_FastPath: two number inputs take the fast
// path: IsNumber guard ×2 passes → ADDSD → write back to R(A) → ret with rax=0
// (on the fast path rax is some value left after movsd, but only the deopt
// block sets rax = deoptCode; the fast path never enters the deopt block, so
// rax is whatever the last movsd wrote, which differs from deoptCode — the
// caller detecting rax != deoptCode means the fast-path OK route was taken).
//
// Observed: R(0)=3.0 + R(1)=4.0 → R(2)=7.0 (fast path succeeds; at fast-path
// ret, rax is the NaN-box of stack[A]=7.0, which differs from deoptCode 0xDEAD).
func TestPJ2_SpeculativeAddWithGuard_FastPath(t *testing.T) {
	pj2TestStack[0] = math.Float64bits(3.0) // number
	pj2TestStack[1] = math.Float64bits(4.0) // number
	pj2TestStack[2] = 0

	vsBase := uintptr(unsafe.Pointer(&pj2TestStack[0]))

	const deoptCode uint64 = 0xDEAD

	var buf []byte
	buf = EmitArithSpeculativeAddWithGuard(buf, 2, 0, 1, deoptCode)
	if len(buf) != EncodedArithSpecAddWithGuardLen {
		t.Fatalf("encoded length = %d, want %d", len(buf), EncodedArithSpecAddWithGuardLen)
	}

	page, err := MmapCode(buf)
	if err != nil {
		t.Fatalf("MmapCode failed: %v", err)
	}
	defer func() { _ = page.Munmap() }()

	rax := CallJITSpec(page.Addr(), 0, vsBase)
	runtime.KeepAlive(pj2TestStack)

	t.Logf("快路径 rax=0x%x", rax)
	if rax == deoptCode {
		t.Error("快路径不应进 deopt block,rax 不应等于 deoptCode")
	}
	got := math.Float64frombits(pj2TestStack[2])
	if got != 7.0 {
		t.Errorf("快路径:R(2) = %v, want 7.0", got)
	}
}

// TestPJ2_SpeculativeAddWithGuard_DeoptPath: R(B) is a non-number (a NaN-box
// table/string etc.) → IsNumber guard fails, jumps to the deopt block → the
// segment returns rax = deoptCode → the caller should then fall back to the
// host helper slow path.
//
// Observed: R(0)=NaN-box (a fake GCRef, value 0xFFFB000000000001 simulating a
// string) → IsNumber=false → jae deopt → rax=deoptCode.
func TestPJ2_SpeculativeAddWithGuard_DeoptPath(t *testing.T) {
	// R(0) is a non-number (simulating a string NaN-box, Tag=0xFFFB)
	pj2TestStack[0] = 0xFFFB000000000001
	pj2TestStack[1] = math.Float64bits(4.0)
	pj2TestStack[2] = 0

	vsBase := uintptr(unsafe.Pointer(&pj2TestStack[0]))

	const deoptCode uint64 = 0xDEAD

	var buf []byte
	buf = EmitArithSpeculativeAddWithGuard(buf, 2, 0, 1, deoptCode)

	page, err := MmapCode(buf)
	if err != nil {
		t.Fatalf("MmapCode failed: %v", err)
	}
	defer func() { _ = page.Munmap() }()

	rax := CallJITSpec(page.Addr(), 0, vsBase)
	runtime.KeepAlive(pj2TestStack)

	t.Logf("deopt 路径 rax=0x%x(want 0x%x)", rax, deoptCode)
	if rax != deoptCode {
		t.Errorf("deopt 路径:rax = 0x%x, want 0x%x(string 非 number 应触发 IsNumber guard 失败)", rax, deoptCode)
	}
	// R(2) should not be written (the fast path never reaches movsd [rbx+A*8])
	if pj2TestStack[2] != 0 {
		t.Errorf("deopt 路径不应写 R(2),got 0x%x", pj2TestStack[2])
	}
}

// TestPJ2_SpeculativeAddWithGuard_DeoptOnC: R(C) is a non-number, triggering
// the second guard failure.
func TestPJ2_SpeculativeAddWithGuard_DeoptOnC(t *testing.T) {
	pj2TestStack[0] = math.Float64bits(3.0)
	pj2TestStack[1] = 0xFFFC000000000001 // table NaN-box
	pj2TestStack[2] = 0

	vsBase := uintptr(unsafe.Pointer(&pj2TestStack[0]))
	const deoptCode uint64 = 0xBEEF

	var buf []byte
	buf = EmitArithSpeculativeAddWithGuard(buf, 2, 0, 1, deoptCode)

	page, _ := MmapCode(buf)
	defer page.Munmap()

	rax := CallJITSpec(page.Addr(), 0, vsBase)
	runtime.KeepAlive(pj2TestStack)

	if rax != deoptCode {
		t.Errorf("deopt-on-C: rax=0x%x, want 0x%x", rax, deoptCode)
	}
	if pj2TestStack[2] != 0 {
		t.Errorf("R(2) 不应被写, got 0x%x", pj2TestStack[2])
	}
}
