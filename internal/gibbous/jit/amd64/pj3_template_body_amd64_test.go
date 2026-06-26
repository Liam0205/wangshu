//go:build wangshu_p4 && linux && amd64

package amd64

import (
	"math"
	"runtime"
	"testing"
	"unsafe"
)

// pj3_template_body_amd64_test.go —— PJ3 FORLOOP body 含 reg-K op
// 字节级 + 真 mmap+RX round-trip 验证(`local s=K; for i=K1,K2 do s=s
// op K3 end; return s` 形态)。

// TestPJ3_ForLoopWithBody_ADD:s=0; for i=1,100 do s = s + 1 end → s=100.
func TestPJ3_ForLoopWithBody_ADD(t *testing.T) {
	// R(0) = s, R(1..4) = for slots
	pj2TestStack[0] = 0 // s 槽留给 emit 写
	vsBase := uintptr(unsafe.Pointer(&pj2TestStack[0]))

	var buf []byte
	buf = EmitForLoopWithRegKBody(buf,
		math.Float64bits(0),   // K_s = 0
		math.Float64bits(1),   // K_init = 1
		math.Float64bits(100), // K_limit = 100
		math.Float64bits(1),   // K_step = 1
		math.Float64bits(1),   // K_body = 1 (s += 1)
		0,                     // aS = R(0)
		SseOpAddsd,
		-1, // no safepoint
	)

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
	buf = EmitForLoopWithRegKBody(buf,
		math.Float64bits(0),
		math.Float64bits(1), math.Float64bits(1000), math.Float64bits(1),
		math.Float64bits(2),
		0, SseOpAddsd, -1,
	)

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
	buf = EmitForLoopWithRegKBody(buf,
		math.Float64bits(1),
		math.Float64bits(1), math.Float64bits(5), math.Float64bits(1),
		math.Float64bits(2),
		0, SseOpMulsd, -1,
	)

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
	buf = EmitForLoopWithRegKBody(buf,
		math.Float64bits(100),
		math.Float64bits(1), math.Float64bits(10), math.Float64bits(1),
		math.Float64bits(3),
		0, SseOpSubsd, -1,
	)

	page, _ := MmapCode(buf)
	defer page.Munmap()
	CallJITSpec(page.Addr(), 0, vsBase)
	runtime.KeepAlive(pj2TestStack)

	if got := math.Float64frombits(pj2TestStack[0]); got != 70 {
		t.Errorf("s = %v, want 70", got)
	}
}
