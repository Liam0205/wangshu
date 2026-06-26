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

// ============================================================================
// SUB/MUL/DIV WithGuard 字节级单测——对位 ADD 三件套
// (TestPJ2_SpeculativeAddWithGuard_FastPath/DeoptPath/DeoptOnC)。
//
// 承外部 review increment-9 🟠 #1:补 9 个 WithGuard 测试覆盖 SUB/MUL/DIV
// 的快路径 + 双 deopt 路径,与 ADD 三件套 byte-equal,防误改
// archEmitArithSpecBinopWithGuard 时 ADD 单测能抓出但 SUB/MUL/DIV 漏过。
//
// 测试格式:
//   - FastPath:R(B) + R(C) 都是 number → guard 通过 → SSE op → 写 R(A)。
//   - DeoptPath_B:R(B) 非 number(string NaN-box 0xFFFB...)→ 第一 guard
//     失败 → 跳 deopt block → rax = deoptCode,R(A) 不写。
//   - DeoptPath_C:R(C) 非 number(table NaN-box 0xFFFC...)→ 第二 guard
//     失败 → 跳 deopt block → rax = deoptCode,R(A) 不写。
//
// 9 个测试同款形态,通过 SseOp 字节(Sub/Mul/Div)区分。
// ============================================================================

// runSpecBinopWithGuardFastPath 通用快路径测试 helper:
// 输入 R(B), R(C) 双 number,验 R(A) = R(B) op R(C),rax != deoptCode。
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

// runSpecBinopWithGuardDeopt 通用 deopt 路径测试 helper:
// 验 R(A) 不被写,rax = deoptCode。bIsNonNum=true 时 R(B) 是 string NaN-box,
// 触发第一 guard 失败;false 时 R(C) 是 table NaN-box,触发第二 guard 失败。
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

// SUB WithGuard 三件套:R(B)=10 - R(C)=3 = 7.
func TestPJ2_SpeculativeSubWithGuard_FastPath(t *testing.T) {
	runSpecBinopWithGuardFastPath(t, "SUB", SseOpSubsd, 10.0, 3.0, 7.0)
}
func TestPJ2_SpeculativeSubWithGuard_DeoptPath_B(t *testing.T) {
	runSpecBinopWithGuardDeopt(t, "SUB-B-string", SseOpSubsd, true, 0xCAFE01)
}
func TestPJ2_SpeculativeSubWithGuard_DeoptPath_C(t *testing.T) {
	runSpecBinopWithGuardDeopt(t, "SUB-C-table", SseOpSubsd, false, 0xCAFE02)
}

// MUL WithGuard 三件套:R(B)=6 * R(C)=7 = 42.
func TestPJ2_SpeculativeMulWithGuard_FastPath(t *testing.T) {
	runSpecBinopWithGuardFastPath(t, "MUL", SseOpMulsd, 6.0, 7.0, 42.0)
}
func TestPJ2_SpeculativeMulWithGuard_DeoptPath_B(t *testing.T) {
	runSpecBinopWithGuardDeopt(t, "MUL-B-string", SseOpMulsd, true, 0xCAFE03)
}
func TestPJ2_SpeculativeMulWithGuard_DeoptPath_C(t *testing.T) {
	runSpecBinopWithGuardDeopt(t, "MUL-C-table", SseOpMulsd, false, 0xCAFE04)
}

// DIV WithGuard 三件套:R(B)=42 / R(C)=6 = 7.
func TestPJ2_SpeculativeDivWithGuard_FastPath(t *testing.T) {
	runSpecBinopWithGuardFastPath(t, "DIV", SseOpDivsd, 42.0, 6.0, 7.0)
}
func TestPJ2_SpeculativeDivWithGuard_DeoptPath_B(t *testing.T) {
	runSpecBinopWithGuardDeopt(t, "DIV-B-string", SseOpDivsd, true, 0xCAFE05)
}
func TestPJ2_SpeculativeDivWithGuard_DeoptPath_C(t *testing.T) {
	runSpecBinopWithGuardDeopt(t, "DIV-C-table", SseOpDivsd, false, 0xCAFE06)
}
