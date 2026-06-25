//go:build wangshu_p4 && linux && amd64

package amd64

import (
	"math"
	"runtime"
	"testing"
	"unsafe"
)

// pj2_template_chainkk_amd64_test.go —— PJ2 二段链式 reg-K-K 投机模板真
// mmap+RX round-trip 验证。
//
// 形态:`R(A) = R(B) op1 K1 op2 K2`(luac 编 `x*2+1` 等 = MUL+ADD 链式)。
// 模板复用 xmm0 不写回 stack 中间值,一次 mmap 段调用完成两次 SSE binop。

// TestPJ2_SpecChainKK_MUL_ADD:R(0)=3 * K1(2) + K2(1) = 7.
func TestPJ2_SpecChainKK_MUL_ADD(t *testing.T) {
	pj2TestStack[0] = math.Float64bits(3.0)
	pj2TestStack[1] = 0

	vsBase := uintptr(unsafe.Pointer(&pj2TestStack[0]))
	const deoptCode uint64 = 0xDEAD

	var buf []byte
	buf = EmitArithSpeculativeChainKKWithGuard(buf, SseOpMulsd, SseOpAddsd, 1, 0,
		math.Float64bits(2.0), math.Float64bits(1.0), deoptCode)
	if len(buf) != EncodedArithSpecChainKKWithGuardLen {
		t.Fatalf("encoded length = %d, want %d", len(buf), EncodedArithSpecChainKKWithGuardLen)
	}

	page, err := MmapCode(buf)
	if err != nil {
		t.Fatalf("MmapCode failed: %v", err)
	}
	defer func() { _ = page.Munmap() }()

	rax := CallJITSpec(page.Addr(), 0, vsBase)
	runtime.KeepAlive(pj2TestStack)

	if rax == deoptCode {
		t.Errorf("快路径不应进 deopt,rax=0x%x", rax)
	}
	if got := math.Float64frombits(pj2TestStack[1]); got != 7.0 {
		t.Errorf("R(1) = %v, want 7.0(3*2+1)", got)
	}
}

// TestPJ2_SpecChainKK_ADD_MUL:R(0)=3 + K1(1) * K2(2) = 8(注意:Lua 优
// 先级实际是 (3+1)*2=8;模板按 op 顺序串行,xmm0 链接).
func TestPJ2_SpecChainKK_ADD_MUL(t *testing.T) {
	pj2TestStack[0] = math.Float64bits(3.0)
	pj2TestStack[1] = 0

	vsBase := uintptr(unsafe.Pointer(&pj2TestStack[0]))
	const deoptCode uint64 = 0xBEEF

	var buf []byte
	buf = EmitArithSpeculativeChainKKWithGuard(buf, SseOpAddsd, SseOpMulsd, 1, 0,
		math.Float64bits(1.0), math.Float64bits(2.0), deoptCode)

	page, err := MmapCode(buf)
	if err != nil {
		t.Fatalf("MmapCode failed: %v", err)
	}
	defer func() { _ = page.Munmap() }()

	CallJITSpec(page.Addr(), 0, vsBase)
	runtime.KeepAlive(pj2TestStack)

	if got := math.Float64frombits(pj2TestStack[1]); got != 8.0 {
		t.Errorf("R(1) = %v, want 8.0((3+1)*2)", got)
	}
}

// TestPJ2_SpecChainKK_Deopt:R(0) 非 number → guard 失败 → rax=deoptCode.
func TestPJ2_SpecChainKK_Deopt(t *testing.T) {
	pj2TestStack[0] = 0xFFFC000000000001 // table NaN-box
	pj2TestStack[1] = 0

	vsBase := uintptr(unsafe.Pointer(&pj2TestStack[0]))
	const deoptCode uint64 = 0xCAFE

	var buf []byte
	buf = EmitArithSpeculativeChainKKWithGuard(buf, SseOpMulsd, SseOpAddsd, 1, 0,
		math.Float64bits(2.0), math.Float64bits(1.0), deoptCode)

	page, err := MmapCode(buf)
	if err != nil {
		t.Fatalf("MmapCode failed: %v", err)
	}
	defer func() { _ = page.Munmap() }()

	rax := CallJITSpec(page.Addr(), 0, vsBase)
	runtime.KeepAlive(pj2TestStack)

	if rax != deoptCode {
		t.Errorf("deopt 路径:rax=0x%x, want 0x%x", rax, deoptCode)
	}
	if pj2TestStack[1] != 0 {
		t.Errorf("deopt 路径不应写 R(1), got 0x%x", pj2TestStack[1])
	}
}
