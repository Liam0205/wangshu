//go:build wangshu_p4 && linux && amd64

package amd64

import (
	"math"
	"runtime"
	"testing"
	"unsafe"
)

// pj3_template_body_amd64_test.go —— byte-level + real mmap+RX round-trip
// verification of a PJ3 FORLOOP body containing a reg-K op (the `local s=K;
// for i=K1,K2 do s=s op K3 end; return s` form).

// TestPJ3_ForLoopWithBody_ADD:s=0; for i=1,100 do s = s + 1 end → s=100.
func TestPJ3_ForLoopWithBody_ADD(t *testing.T) {
	// R(0) = s, R(1..4) = for slots
	pj2TestStack[0] = 0 // s slot left for emit to write
	vsBase := uintptr(unsafe.Pointer(&pj2TestStack[0]))

	var buf []byte
	buf, _ = EmitForLoopWithRegKBody(buf,
		math.Float64bits(0),   // K_s = 0
		math.Float64bits(1),   // K_init = 1
		math.Float64bits(100), // K_limit = 100
		math.Float64bits(1),   // K_step = 1
		math.Float64bits(1),   // K_body = 1 (s += 1)
		0,                     // aS = R(0)
		SseOpAddsd,
		-1, // no safepoint
		-1, 0, 0)

	page, err := MmapCode(buf)
	if err != nil {
		t.Fatalf("MmapCode: %v", err)
	}
	defer func() { _ = page.Munmap() }()

	CallJITSpec(page.Addr(), 0, vsBase)
	runtime.KeepAlive(pj2TestStack)

	if got := math.Float64frombits(pj2TestStack[0]); got != 100 {
		t.Errorf("R(0) = %v, want 100(sum 100 次 s+=1)", got)
	}
}

// TestPJ3_ForLoopWithBody_ADD_BigK:s=0; for i=1,1000 do s=s+2 end → s=2000.
func TestPJ3_ForLoopWithBody_ADD_BigK(t *testing.T) {
	pj2TestStack[0] = 0
	vsBase := uintptr(unsafe.Pointer(&pj2TestStack[0]))

	var buf []byte
	buf, _ = EmitForLoopWithRegKBody(buf,
		math.Float64bits(0),
		math.Float64bits(1), math.Float64bits(1000), math.Float64bits(1),
		math.Float64bits(2),
		0, SseOpAddsd, -1,
		-1, 0, 0)

	page, _ := MmapCode(buf)
	defer page.Munmap()
	CallJITSpec(page.Addr(), 0, vsBase)
	runtime.KeepAlive(pj2TestStack)

	if got := math.Float64frombits(pj2TestStack[0]); got != 2000 {
		t.Errorf("s = %v, want 2000", got)
	}
}

// TestPJ3_ForLoopWithBody_MUL:s=1; for i=1,5 do s = s * 2 end → s=32.
func TestPJ3_ForLoopWithBody_MUL(t *testing.T) {
	pj2TestStack[0] = 0
	vsBase := uintptr(unsafe.Pointer(&pj2TestStack[0]))

	var buf []byte
	buf, _ = EmitForLoopWithRegKBody(buf,
		math.Float64bits(1),
		math.Float64bits(1), math.Float64bits(5), math.Float64bits(1),
		math.Float64bits(2),
		0, SseOpMulsd, -1,
		-1, 0, 0)

	page, _ := MmapCode(buf)
	defer page.Munmap()
	CallJITSpec(page.Addr(), 0, vsBase)
	runtime.KeepAlive(pj2TestStack)

	if got := math.Float64frombits(pj2TestStack[0]); got != 32 {
		t.Errorf("s = %v, want 32(2^5)", got)
	}
}

// TestPJ3_ForLoopWithBody_SUB:s=100; for i=1,10 do s = s - 3 end → s=70.
func TestPJ3_ForLoopWithBody_SUB(t *testing.T) {
	pj2TestStack[0] = 0
	vsBase := uintptr(unsafe.Pointer(&pj2TestStack[0]))

	var buf []byte
	buf, _ = EmitForLoopWithRegKBody(buf,
		math.Float64bits(100),
		math.Float64bits(1), math.Float64bits(10), math.Float64bits(1),
		math.Float64bits(3),
		0, SseOpSubsd, -1,
		-1, 0, 0)

	page, _ := MmapCode(buf)
	defer page.Munmap()
	CallJITSpec(page.Addr(), 0, vsBase)
	runtime.KeepAlive(pj2TestStack)

	if got := math.Float64frombits(pj2TestStack[0]); got != 70 {
		t.Errorf("s = %v, want 70", got)
	}
}

// TestPJ3_ForLoopWithBody2_AddMul: s=0; for i=1,5 do s = s+1; s = s*2 end
// → each iter runs (s+1)*2:
//
//	iter1: s=(0+1)*2=2
//	iter2: s=(2+1)*2=6
//	iter3: s=(6+1)*2=14
//	iter4: s=(14+1)*2=30
//	iter5: s=(30+1)*2=62
func TestPJ3_ForLoopWithBody2_AddMul(t *testing.T) {
	pj2TestStack[0] = 0
	vsBase := uintptr(unsafe.Pointer(&pj2TestStack[0]))

	var buf []byte
	buf, _ = EmitForLoopWithRegKBody2(buf,
		math.Float64bits(0),
		math.Float64bits(1), math.Float64bits(5), math.Float64bits(1),
		math.Float64bits(1), // K_body1 = 1 (s+=1)
		math.Float64bits(2), // K_body2 = 2 (s*=2)
		0, SseOpAddsd, SseOpMulsd, -1,
		-1, 0, 0)

	page, _ := MmapCode(buf)
	defer page.Munmap()
	CallJITSpec(page.Addr(), 0, vsBase)
	runtime.KeepAlive(pj2TestStack)

	if got := math.Float64frombits(pj2TestStack[0]); got != 62 {
		t.Errorf("s = %v, want 62((((((0+1)*2+1)*2+1)*2+1)*2+1)*2 = 62)", got)
	}
}
