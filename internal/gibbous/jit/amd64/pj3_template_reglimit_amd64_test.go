//go:build wangshu_p4 && linux && amd64

package amd64

import (
	"math"
	"runtime"
	"testing"
	"unsafe"
)

// pj3_template_regelimit_amd64_test.go —— PJ3 FORLOOP reg limit 形态真
// mmap+RX round-trip 验证(`for i=1,n do end` hot path 形态)。

// 共享 pj2TestStack(全局 heap slice,承 05 §1.3 JIT 不持 Go 栈指针)。

// TestPJ3_ForLoopRegLimit_FastPath:R(0)=number(limit=100) → guard 通过 → loop 跑满.
func TestPJ3_ForLoopRegLimit_FastPath(t *testing.T) {
	pj2TestStack[0] = math.Float64bits(100.0) // R(0) = limit = 100(number)
	vsBase := uintptr(unsafe.Pointer(&pj2TestStack[0]))

	const deoptCode uint64 = 0xDEAD

	var buf []byte
	buf = EmitForLoopRegLimit(buf,
		math.Float64bits(1), // init
		math.Float64bits(1), // step
		0,                   // limitReg = R(0)
		deoptCode,
		-1 /* no safepoint(单测 r15 无 jitContext)*/)

	if len(buf) != EncodedForLoopRegLimitWithSafepointLen-EncodedCmpByteR15DispImm8Len-EncodedJccRel32Len {
		t.Logf("buf len=%d(无 safepoint 版本)", len(buf))
	}

	page, err := MmapCode(buf)
	if err != nil {
		t.Fatalf("MmapCode: %v", err)
	}
	defer func() { _ = page.Munmap() }()

	rax := CallJITSpec(page.Addr(), 0, vsBase)
	runtime.KeepAlive(pj2TestStack)

	if rax == deoptCode {
		t.Errorf("快路径不应进 deopt,rax=0x%x", rax)
	}
}

// TestPJ3_ForLoopRegLimit_DeoptPath:R(0)=table NaN-box → guard 失败 → deopt.
func TestPJ3_ForLoopRegLimit_DeoptPath(t *testing.T) {
	pj2TestStack[0] = 0xFFFC000000000001 // table NaN-box(非 number)
	vsBase := uintptr(unsafe.Pointer(&pj2TestStack[0]))

	const deoptCode uint64 = 0xCAFE

	var buf []byte
	buf = EmitForLoopRegLimit(buf,
		math.Float64bits(1),
		math.Float64bits(1),
		0,
		deoptCode,
		-1)

	page, err := MmapCode(buf)
	if err != nil {
		t.Fatalf("MmapCode: %v", err)
	}
	defer func() { _ = page.Munmap() }()

	rax := CallJITSpec(page.Addr(), 0, vsBase)
	runtime.KeepAlive(pj2TestStack)

	if rax != deoptCode {
		t.Errorf("deopt 路径:rax=0x%x, want 0x%x", rax, deoptCode)
	}
}

// TestPJ3_ForLoopRegLimit_LongLoop:limit=10000(reg)长循环验 backward jmp.
func TestPJ3_ForLoopRegLimit_LongLoop(t *testing.T) {
	pj2TestStack[0] = math.Float64bits(10000.0)
	vsBase := uintptr(unsafe.Pointer(&pj2TestStack[0]))

	var buf []byte
	buf = EmitForLoopRegLimit(buf,
		math.Float64bits(1),
		math.Float64bits(1),
		0,
		0xDEAD,
		-1)

	page, err := MmapCode(buf)
	if err != nil {
		t.Fatalf("MmapCode: %v", err)
	}
	defer func() { _ = page.Munmap() }()

	CallJITSpec(page.Addr(), 0, vsBase)
	runtime.KeepAlive(pj2TestStack)

	// 10000 iter 跑完,正常退出
}
