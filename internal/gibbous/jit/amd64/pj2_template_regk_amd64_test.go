//go:build wangshu_p4 && linux && amd64

package amd64

import (
	"math"
	"runtime"
	"testing"
	"unsafe"
)

// pj2_template_regk_amd64_test.go —— PJ2 reg-K form speculative-template real
// mmap+RX round-trip verification. In the form `R(A) = R(B) op K`, K is baked
// into an imm64 at compile time, and the single guard only checks R(B).
//
// **Design basis**: hot paths like `x + 1` / `n * 2` in a constant-folded
// form -- luac compiles `x+1` into `ADD A B 256+kidx` (C high bit set = a
// constant), where K[kidx] is NumberValue(1). At compile time, K must resolve
// to a number (otherwise fall back to host), then it is baked into an imm64
// emitted directly into the segment.

// TestPJ2_SpeculativeBinopRegK_ADD: R(0)=10 + K(5) = 15.
func TestPJ2_SpeculativeBinopRegK_ADD(t *testing.T) {
	pj2TestStack[0] = math.Float64bits(10.0)
	pj2TestStack[1] = 0

	vsBase := uintptr(unsafe.Pointer(&pj2TestStack[0]))

	kvalue := math.Float64bits(5.0)

	var buf []byte
	buf = EmitArithSpeculativeBinopRegK(buf, SseOpAddsd, 1, 0, kvalue)
	if len(buf) != EncodedArithSpecBinopRegKLen {
		t.Fatalf("encoded length = %d, want %d", len(buf), EncodedArithSpecBinopRegKLen)
	}

	page, err := MmapCode(buf)
	if err != nil {
		t.Fatalf("MmapCode failed: %v", err)
	}
	defer func() { _ = page.Munmap() }()

	CallJITSpec(page.Addr(), 0, vsBase)
	runtime.KeepAlive(pj2TestStack)

	if got := math.Float64frombits(pj2TestStack[1]); got != 15.0 {
		t.Errorf("R(1) = %v, want 15.0(10+5)", got)
	}
}

// TestPJ2_SpeculativeBinopRegK_SUB: R(0)=10 - K(3) = 7.
func TestPJ2_SpeculativeBinopRegK_SUB(t *testing.T) {
	pj2TestStack[0] = math.Float64bits(10.0)
	pj2TestStack[1] = 0

	vsBase := uintptr(unsafe.Pointer(&pj2TestStack[0]))

	var buf []byte
	buf = EmitArithSpeculativeBinopRegK(buf, SseOpSubsd, 1, 0, math.Float64bits(3.0))

	page, err := MmapCode(buf)
	if err != nil {
		t.Fatalf("MmapCode failed: %v", err)
	}
	defer func() { _ = page.Munmap() }()

	CallJITSpec(page.Addr(), 0, vsBase)
	runtime.KeepAlive(pj2TestStack)

	if got := math.Float64frombits(pj2TestStack[1]); got != 7.0 {
		t.Errorf("R(1) = %v, want 7.0(10-3)", got)
	}
}

// TestPJ2_SpeculativeBinopRegK_MUL: R(0)=7 * K(6) = 42.
func TestPJ2_SpeculativeBinopRegK_MUL(t *testing.T) {
	pj2TestStack[0] = math.Float64bits(7.0)
	pj2TestStack[1] = 0

	vsBase := uintptr(unsafe.Pointer(&pj2TestStack[0]))

	var buf []byte
	buf = EmitArithSpeculativeBinopRegK(buf, SseOpMulsd, 1, 0, math.Float64bits(6.0))

	page, err := MmapCode(buf)
	if err != nil {
		t.Fatalf("MmapCode failed: %v", err)
	}
	defer func() { _ = page.Munmap() }()

	CallJITSpec(page.Addr(), 0, vsBase)
	runtime.KeepAlive(pj2TestStack)

	if got := math.Float64frombits(pj2TestStack[1]); got != 42.0 {
		t.Errorf("R(1) = %v, want 42.0(7*6)", got)
	}
}

// TestPJ2_SpeculativeBinopRegK_DIV: R(0)=42 / K(6) = 7.
func TestPJ2_SpeculativeBinopRegK_DIV(t *testing.T) {
	pj2TestStack[0] = math.Float64bits(42.0)
	pj2TestStack[1] = 0

	vsBase := uintptr(unsafe.Pointer(&pj2TestStack[0]))

	var buf []byte
	buf = EmitArithSpeculativeBinopRegK(buf, SseOpDivsd, 1, 0, math.Float64bits(6.0))

	page, err := MmapCode(buf)
	if err != nil {
		t.Fatalf("MmapCode failed: %v", err)
	}
	defer func() { _ = page.Munmap() }()

	CallJITSpec(page.Addr(), 0, vsBase)
	runtime.KeepAlive(pj2TestStack)

	if got := math.Float64frombits(pj2TestStack[1]); got != 7.0 {
		t.Errorf("R(1) = %v, want 7.0(42/6)", got)
	}
}

// TestPJ2_SpeculativeBinopRegK_WithGuard_FastPath: R(B) is a number → guard
// passes → takes the reg-K fast path → R(A) = R(B) + K.
func TestPJ2_SpeculativeBinopRegK_WithGuard_FastPath(t *testing.T) {
	pj2TestStack[0] = math.Float64bits(10.0)
	pj2TestStack[1] = 0

	vsBase := uintptr(unsafe.Pointer(&pj2TestStack[0]))
	const deoptCode uint64 = 0xDEAD

	var buf []byte
	buf = EmitArithSpeculativeBinopRegKWithGuard(buf, SseOpAddsd, 1, 0,
		math.Float64bits(5.0), deoptCode)
	if len(buf) != EncodedArithSpecBinopRegKWithGuardLen {
		t.Fatalf("encoded length = %d, want %d",
			len(buf), EncodedArithSpecBinopRegKWithGuardLen)
	}

	page, err := MmapCode(buf)
	if err != nil {
		t.Fatalf("MmapCode failed: %v", err)
	}
	defer func() { _ = page.Munmap() }()

	rax := CallJITSpec(page.Addr(), 0, vsBase)
	runtime.KeepAlive(pj2TestStack)

	if rax == deoptCode {
		t.Error("快路径不应进 deopt block,rax 不应等于 deoptCode")
	}
	if got := math.Float64frombits(pj2TestStack[1]); got != 15.0 {
		t.Errorf("快路径 R(1) = %v, want 15(10+5)", got)
	}
}

// TestPJ2_SpeculativeBinopRegK_WithGuard_DeoptPath: R(B) is a table NaN-box
// (not a number) → guard fails → jumps to the deopt block → rax = deoptCode.
func TestPJ2_SpeculativeBinopRegK_WithGuard_DeoptPath(t *testing.T) {
	pj2TestStack[0] = 0xFFFC000000000001 // table NaN-box
	pj2TestStack[1] = 0

	vsBase := uintptr(unsafe.Pointer(&pj2TestStack[0]))
	const deoptCode uint64 = 0xBEEF

	var buf []byte
	buf = EmitArithSpeculativeBinopRegKWithGuard(buf, SseOpAddsd, 1, 0,
		math.Float64bits(5.0), deoptCode)

	page, err := MmapCode(buf)
	if err != nil {
		t.Fatalf("MmapCode failed: %v", err)
	}
	defer func() { _ = page.Munmap() }()

	rax := CallJITSpec(page.Addr(), 0, vsBase)
	runtime.KeepAlive(pj2TestStack)

	if rax != deoptCode {
		t.Errorf("deopt 路径:rax = 0x%x, want 0x%x", rax, deoptCode)
	}
	if pj2TestStack[1] != 0 {
		t.Errorf("deopt 路径不应写 R(1),got 0x%x", pj2TestStack[1])
	}
}
