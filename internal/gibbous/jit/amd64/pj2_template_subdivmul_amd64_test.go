//go:build wangshu_p4 && linux && amd64

package amd64

import (
	"math"
	"runtime"
	"testing"
	"unsafe"
)

// pj2_template_subdivmul_amd64_test.go —— PJ2 投机模板扩 SUB/MUL/DIV 三档
// 字节级 + 真 mmap+RX round-trip 验证。承 03-speculation-ic.md §2 f64 投机
// 白名单(IEEE 754 单条 SSE 指令家族)。
//
// 测试结构与 ADD 三件套对位:
//   - RoundTrip:无 guard 单 op 模板真执行,验 SSE binop 字节正确 +
//     rbx 寻址 + ret 返回 trampoline
//   - WithGuard_FastPath:guard 通过走快路径
//   - WithGuard_Deopt(B/C):guard 失败跳 deopt block,返 deoptCode
//
// pj2TestStack 共享自 pj2_template_amd64_test.go(全局 heap slice,
// 必须 heap 分配避免 morestack 搬走指针,承 05 §1.3 纪律)。

// TestPJ2_SpeculativeSubRoundTrip:R(0)=10 - R(1)=3 = 7.
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

	// 负结果
	pj2TestStack[0] = math.Float64bits(2.5)
	pj2TestStack[1] = math.Float64bits(7.5)
	pj2TestStack[2] = 0
	CallJITSpec(page.Addr(), 0, vsBase)
	runtime.KeepAlive(pj2TestStack)
	if got := math.Float64frombits(pj2TestStack[2]); got != -5.0 {
		t.Errorf("SUB: R(2) = %v, want -5.0(2.5-7.5)", got)
	}
}

// TestPJ2_SpeculativeMulRoundTrip:R(0)=6 * R(1)=7 = 42.
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

	// 浮点 + 0 守卫
	pj2TestStack[0] = math.Float64bits(1.5)
	pj2TestStack[1] = math.Float64bits(0.0)
	pj2TestStack[2] = 0
	CallJITSpec(page.Addr(), 0, vsBase)
	runtime.KeepAlive(pj2TestStack)
	if got := math.Float64frombits(pj2TestStack[2]); got != 0.0 {
		t.Errorf("MUL: R(2) = %v, want 0.0(1.5*0)", got)
	}
}

// TestPJ2_SpeculativeDivRoundTrip:R(0)=42 / R(1)=6 = 7.
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

	// 浮点 1/3
	pj2TestStack[0] = math.Float64bits(1.0)
	pj2TestStack[1] = math.Float64bits(3.0)
	pj2TestStack[2] = 0
	CallJITSpec(page.Addr(), 0, vsBase)
	runtime.KeepAlive(pj2TestStack)
	if got := math.Float64frombits(pj2TestStack[2]); math.Abs(got-(1.0/3.0)) > 1e-15 {
		t.Errorf("DIV: R(2) = %v, want 1/3", got)
	}

	// 除以零 → IEEE 754 +Inf(双 number 投机不抛错,与解释器路径不同,
	// caller deopt 判定该形态由 useSpec 启用决定;这里只验 SSE 字节正确)
	pj2TestStack[0] = math.Float64bits(1.0)
	pj2TestStack[1] = math.Float64bits(0.0)
	pj2TestStack[2] = 0
	CallJITSpec(page.Addr(), 0, vsBase)
	runtime.KeepAlive(pj2TestStack)
	if got := math.Float64frombits(pj2TestStack[2]); !math.IsInf(got, 1) {
		t.Errorf("DIV by 0: R(2) = %v, want +Inf", got)
	}
}
