//go:build wangshu_p4 && linux && amd64

package amd64

import (
	"math"
	"runtime"
	"testing"
	"unsafe"
)

// pj2_template_subdivmul_amd64_test.go —— extends the PJ2 speculative template
// with the SUB/MUL/DIV trio. Byte-level plus real mmap+RX round-trip
// verification. Follows 03-speculation-ic.md §2 f64 speculation whitelist
// (the single-instruction IEEE 754 SSE family).
//
// The test structure mirrors the ADD trio:
//   - RoundTrip: real execution of a guardless single-op template, checking the
//     SSE binop bytes plus rbx addressing plus the ret-return trampoline
//   - WithGuard_FastPath: guard passes and takes the fast path
//   - WithGuard_Deopt(B/C): guard fails, jumps to the deopt block, returns deoptCode
//
// pj2TestStack is shared from pj2_template_amd64_test.go (a global heap slice;
// it must be heap-allocated so morestack cannot move the pointer, per 05 §1.3).

// TestPJ2_SpeculativeSubRoundTrip: R(0)=10 - R(1)=3 = 7.
func TestPJ2_SpeculativeSubRoundTrip(t *testing.T) {
	pj2TestStack[0] = math.Float64bits(10.0)
	pj2TestStack[1] = math.Float64bits(3.0)
	pj2TestStack[2] = 0

	vsBase := uintptr(unsafe.Pointer(&pj2TestStack[0]))

	var buf []byte
	buf = EmitArithSpeculativeBinop(buf, SseOpSubsd, 2, 0, 1)
	if len(buf) != EncodedArithSpecAddLen {
		t.Fatalf("encoded length = %d, want %d", len(buf), EncodedArithSpecAddLen)
	}

	page, err := MmapCode(buf)
	if err != nil {
		t.Fatalf("MmapCode failed: %v", err)
	}
	defer func() { _ = page.Munmap() }()

	CallJITSpec(page.Addr(), 0, vsBase)
	runtime.KeepAlive(pj2TestStack)

	if got := math.Float64frombits(pj2TestStack[2]); got != 7.0 {
		t.Errorf("SUB: R(2) = %v, want 7.0(10-3)", got)
	}

	// negative result
	pj2TestStack[0] = math.Float64bits(2.5)
	pj2TestStack[1] = math.Float64bits(7.5)
	pj2TestStack[2] = 0
	CallJITSpec(page.Addr(), 0, vsBase)
	runtime.KeepAlive(pj2TestStack)
	if got := math.Float64frombits(pj2TestStack[2]); got != -5.0 {
		t.Errorf("SUB: R(2) = %v, want -5.0(2.5-7.5)", got)
	}
}

// TestPJ2_SpeculativeMulRoundTrip: R(0)=6 * R(1)=7 = 42.
func TestPJ2_SpeculativeMulRoundTrip(t *testing.T) {
	pj2TestStack[0] = math.Float64bits(6.0)
	pj2TestStack[1] = math.Float64bits(7.0)
	pj2TestStack[2] = 0

	vsBase := uintptr(unsafe.Pointer(&pj2TestStack[0]))

	var buf []byte
	buf = EmitArithSpeculativeBinop(buf, SseOpMulsd, 2, 0, 1)
	if len(buf) != EncodedArithSpecAddLen {
		t.Fatalf("encoded length = %d, want %d", len(buf), EncodedArithSpecAddLen)
	}

	page, err := MmapCode(buf)
	if err != nil {
		t.Fatalf("MmapCode failed: %v", err)
	}
	defer func() { _ = page.Munmap() }()

	CallJITSpec(page.Addr(), 0, vsBase)
	runtime.KeepAlive(pj2TestStack)

	if got := math.Float64frombits(pj2TestStack[2]); got != 42.0 {
		t.Errorf("MUL: R(2) = %v, want 42.0(6*7)", got)
	}

	// float times zero
	pj2TestStack[0] = math.Float64bits(1.5)
	pj2TestStack[1] = math.Float64bits(0.0)
	pj2TestStack[2] = 0
	CallJITSpec(page.Addr(), 0, vsBase)
	runtime.KeepAlive(pj2TestStack)
	if got := math.Float64frombits(pj2TestStack[2]); got != 0.0 {
		t.Errorf("MUL: R(2) = %v, want 0.0(1.5*0)", got)
	}
}

// TestPJ2_SpeculativeDivRoundTrip: R(0)=42 / R(1)=6 = 7.
func TestPJ2_SpeculativeDivRoundTrip(t *testing.T) {
	pj2TestStack[0] = math.Float64bits(42.0)
	pj2TestStack[1] = math.Float64bits(6.0)
	pj2TestStack[2] = 0

	vsBase := uintptr(unsafe.Pointer(&pj2TestStack[0]))

	var buf []byte
	buf = EmitArithSpeculativeBinop(buf, SseOpDivsd, 2, 0, 1)
	if len(buf) != EncodedArithSpecAddLen {
		t.Fatalf("encoded length = %d, want %d", len(buf), EncodedArithSpecAddLen)
	}

	page, err := MmapCode(buf)
	if err != nil {
		t.Fatalf("MmapCode failed: %v", err)
	}
	defer func() { _ = page.Munmap() }()

	CallJITSpec(page.Addr(), 0, vsBase)
	runtime.KeepAlive(pj2TestStack)

	if got := math.Float64frombits(pj2TestStack[2]); got != 7.0 {
		t.Errorf("DIV: R(2) = %v, want 7.0(42/6)", got)
	}

	// float 1/3
	pj2TestStack[0] = math.Float64bits(1.0)
	pj2TestStack[1] = math.Float64bits(3.0)
	pj2TestStack[2] = 0
	CallJITSpec(page.Addr(), 0, vsBase)
	runtime.KeepAlive(pj2TestStack)
	if got := math.Float64frombits(pj2TestStack[2]); math.Abs(got-(1.0/3.0)) > 1e-15 {
		t.Errorf("DIV: R(2) = %v, want 1/3", got)
	}

	// divide by zero → IEEE 754 +Inf (double-number speculation does not raise,
	// unlike the interpreter path; the caller's deopt decision for this case is
	// governed by whether useSpec is enabled — here we only check the SSE bytes)
	pj2TestStack[0] = math.Float64bits(1.0)
	pj2TestStack[1] = math.Float64bits(0.0)
	pj2TestStack[2] = 0
	CallJITSpec(page.Addr(), 0, vsBase)
	runtime.KeepAlive(pj2TestStack)
	if got := math.Float64frombits(pj2TestStack[2]); !math.IsInf(got, 1) {
		t.Errorf("DIV by 0: R(2) = %v, want +Inf", got)
	}
}

// ============================================================================
// SUB/MUL/DIV WithGuard byte-level unit tests — mirroring the ADD trio
// (TestPJ2_SpeculativeAddWithGuard_FastPath/DeoptPath/DeoptOnC).
//
// Follows external review increment-9 🟠 #1: add 9 WithGuard tests covering the
// fast path plus both deopt paths for SUB/MUL/DIV, byte-equal with the ADD trio,
// so that a mistaken edit to archEmitArithSpecBinopWithGuard that the ADD tests
// would catch cannot slip through for SUB/MUL/DIV.
//
// Test format:
//   - FastPath: R(B) and R(C) are both numbers → guard passes → SSE op → write R(A).
//   - DeoptPath_B: R(B) is non-number (string NaN-box 0xFFFB...) → first guard
//     fails → jump to deopt block → rax = deoptCode, R(A) not written.
//   - DeoptPath_C: R(C) is non-number (table NaN-box 0xFFFC...) → second guard
//     fails → jump to deopt block → rax = deoptCode, R(A) not written.
//
// All 9 tests share the same shape, distinguished by the SseOp byte (Sub/Mul/Div).
// ============================================================================

// runSpecBinopWithGuardFastPath is a shared fast-path test helper:
// inputs R(B), R(C) both numbers, checks R(A) = R(B) op R(C), rax != deoptCode.
func runSpecBinopWithGuardFastPath(t *testing.T, name string, sseOp byte,
	bVal, cVal, want float64) {
	t.Helper()
	pj2TestStack[0] = math.Float64bits(bVal)
	pj2TestStack[1] = math.Float64bits(cVal)
	pj2TestStack[2] = 0

	vsBase := uintptr(unsafe.Pointer(&pj2TestStack[0]))
	const deoptCode uint64 = 0xDEAD

	var buf []byte
	buf = EmitArithSpeculativeBinopWithGuard(buf, sseOp, 2, 0, 1, deoptCode)
	if len(buf) != EncodedArithSpecAddWithGuardLen {
		t.Fatalf("%s: encoded length = %d, want %d", name,
			len(buf), EncodedArithSpecAddWithGuardLen)
	}

	page, err := MmapCode(buf)
	if err != nil {
		t.Fatalf("%s: MmapCode: %v", name, err)
	}
	defer func() { _ = page.Munmap() }()

	rax := CallJITSpec(page.Addr(), 0, vsBase)
	runtime.KeepAlive(pj2TestStack)

	if rax == deoptCode {
		t.Errorf("%s 快路径不应进 deopt block,rax 不应等于 deoptCode 0x%x",
			name, deoptCode)
	}
	got := math.Float64frombits(pj2TestStack[2])
	if got != want {
		t.Errorf("%s 快路径:R(2) = %v, want %v", name, got, want)
	}
}

// runSpecBinopWithGuardDeopt is a shared deopt-path test helper:
// checks R(A) is not written and rax = deoptCode. When bIsNonNum=true, R(B) is a
// string NaN-box that trips the first guard; when false, R(C) is a table NaN-box
// that trips the second guard.
func runSpecBinopWithGuardDeopt(t *testing.T, name string, sseOp byte,
	bIsNonNum bool, deoptCode uint64) {
	t.Helper()
	if bIsNonNum {
		pj2TestStack[0] = 0xFFFB000000000001 // string NaN-box
		pj2TestStack[1] = math.Float64bits(4.0)
	} else {
		pj2TestStack[0] = math.Float64bits(3.0)
		pj2TestStack[1] = 0xFFFC000000000001 // table NaN-box
	}
	pj2TestStack[2] = 0

	vsBase := uintptr(unsafe.Pointer(&pj2TestStack[0]))

	var buf []byte
	buf = EmitArithSpeculativeBinopWithGuard(buf, sseOp, 2, 0, 1, deoptCode)

	page, err := MmapCode(buf)
	if err != nil {
		t.Fatalf("%s: MmapCode: %v", name, err)
	}
	defer func() { _ = page.Munmap() }()

	rax := CallJITSpec(page.Addr(), 0, vsBase)
	runtime.KeepAlive(pj2TestStack)

	if rax != deoptCode {
		t.Errorf("%s deopt 路径:rax = 0x%x, want 0x%x", name, rax, deoptCode)
	}
	if pj2TestStack[2] != 0 {
		t.Errorf("%s deopt 路径不应写 R(2),got 0x%x",
			name, pj2TestStack[2])
	}
}

// SUB WithGuard trio: R(B)=10 - R(C)=3 = 7.
func TestPJ2_SpeculativeSubWithGuard_FastPath(t *testing.T) {
	runSpecBinopWithGuardFastPath(t, "SUB", SseOpSubsd, 10.0, 3.0, 7.0)
}
func TestPJ2_SpeculativeSubWithGuard_DeoptPath_B(t *testing.T) {
	runSpecBinopWithGuardDeopt(t, "SUB-B-string", SseOpSubsd, true, 0xCAFE01)
}
func TestPJ2_SpeculativeSubWithGuard_DeoptPath_C(t *testing.T) {
	runSpecBinopWithGuardDeopt(t, "SUB-C-table", SseOpSubsd, false, 0xCAFE02)
}

// MUL WithGuard trio: R(B)=6 * R(C)=7 = 42.
func TestPJ2_SpeculativeMulWithGuard_FastPath(t *testing.T) {
	runSpecBinopWithGuardFastPath(t, "MUL", SseOpMulsd, 6.0, 7.0, 42.0)
}
func TestPJ2_SpeculativeMulWithGuard_DeoptPath_B(t *testing.T) {
	runSpecBinopWithGuardDeopt(t, "MUL-B-string", SseOpMulsd, true, 0xCAFE03)
}
func TestPJ2_SpeculativeMulWithGuard_DeoptPath_C(t *testing.T) {
	runSpecBinopWithGuardDeopt(t, "MUL-C-table", SseOpMulsd, false, 0xCAFE04)
}

// DIV WithGuard trio: R(B)=42 / R(C)=6 = 7.
func TestPJ2_SpeculativeDivWithGuard_FastPath(t *testing.T) {
	runSpecBinopWithGuardFastPath(t, "DIV", SseOpDivsd, 42.0, 6.0, 7.0)
}
func TestPJ2_SpeculativeDivWithGuard_DeoptPath_B(t *testing.T) {
	runSpecBinopWithGuardDeopt(t, "DIV-B-string", SseOpDivsd, true, 0xCAFE05)
}
func TestPJ2_SpeculativeDivWithGuard_DeoptPath_C(t *testing.T) {
	runSpecBinopWithGuardDeopt(t, "DIV-C-table", SseOpDivsd, false, 0xCAFE06)
}
