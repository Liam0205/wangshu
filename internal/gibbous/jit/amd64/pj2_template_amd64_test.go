//go:build wangshu_p4 && linux && amd64

package amd64

import (
	"math"
	"runtime"
	"testing"
	"unsafe"
)

// pj2TestStack 是 PJ2 真接入测试用的全局 heap slice——必须 heap 分配,
// Go 栈分配会被 morestack 搬走让 vsBase 指针 stale(承 05 §1.3「JIT 不持
// 任何 Go 栈指针」纪律)。Go 自动逃逸分析:全局 var make 必定 heap。
var pj2TestStack = make([]uint64, 16)

// TestPJ2_SpeculativeAddRoundTrip 真接入测试:用 EmitArithSpeculativeAdd
// 拼装的模板 + MmapCode + CallJITSpec 在本机 amd64 真执行,验证双 number
// 快路径模板正确返回 R(B) + R(C) 的浮点和。
//
// **prove-the-path 命中证据**:字节级单测(TestPJ2_SpeculativeAddTemplate)
// 只验编码字节正确;**本测真 mmap+RX+execute** 段,验证 ADDSD 在真 CPU
// 上工作 + rbx 寻址正确 + ret 弹回 trampoline。
//
// **arena 视图别名雷区实证**:本测的 pj2TestStack 必须 heap 分配
// (全局 var),Go 栈分配的 slice 在 trampoline 期间可能被 morestack 搬走,
// 让 vsBase 指针 stale → 段写到陈旧地址 → 测试看不到结果。这正好实证 P4
// 设计 05 §1.3「JIT 不持 Go 栈指针」纪律——真 P4 路径上 arena.Words 在
// Go heap(arena 是 heap object),不会被搬。
func TestPJ2_SpeculativeAddRoundTrip(t *testing.T) {
	pj2TestStack[0] = math.Float64bits(3.0)
	pj2TestStack[1] = math.Float64bits(4.0)
	pj2TestStack[2] = 0

	vsBase := uintptr(unsafe.Pointer(&pj2TestStack[0]))

	// 拼模板:ADD A=2 B=0 C=1
	var buf []byte
	buf = EmitArithSpeculativeAdd(buf, 2, 0, 1)

	page, err := MmapCode(buf)
	if err != nil {
		t.Fatalf("MmapCode failed: %v", err)
	}
	defer func() { _ = page.Munmap() }()

	CallJITSpec(page.Addr(), 0, vsBase)
	runtime.KeepAlive(pj2TestStack) // 防 GC 在 trampoline 期间动 slice

	got := math.Float64frombits(pj2TestStack[2])
	if got != 7.0 {
		t.Errorf("R(2) = %v, want 7.0(R(0) + R(1) = 3.0 + 4.0)", got)
	}

	// 多档值
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
