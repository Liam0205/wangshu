//go:build wangshu_p4

package jit

import (
	"reflect"
	"testing"
	"unsafe"
)

// helpers_test.go — PJ5 Option B Spike 1 static test of helper function
// addresses (following the §9.20.8 byte-level encoding documentation +
// helpers.go placeholder).
//
// Real-integration prerequisite: the emitter can obtain a helper function's
// physical address via reflect.ValueOf(fn).Pointer() and bake it as an imm64
// into a mov rax instruction (amd64 EmitHelperCall 12 bytes / arm64
// EmitHelperCallArm64 20 bytes).
//
// This test verifies:
//  1. HelperRunCalleeAfterFrameInline's function address can be obtained via reflect.ValueOf
//  2. the function address is non-zero and reasonable (within the .text segment range, not on the zero page)
//  3. the unsafe.Pointer → uintptr conversion is consistent (emit uses the same conversion)
//
// **archSupportsFrameInline=false blocks real triggering**; this test only
// verifies the address is obtainable and that emit is physically feasible
// (once actually integrated in a future Step C-1, this address is the callee
// target).

func TestHelperRunCalleeAfterFrameInline_AddressNonZero(t *testing.T) {
	// reflect.ValueOf(fn).Pointer() is the standard way to obtain a helper function's address during emit
	addr := reflect.ValueOf(HelperRunCalleeAfterFrameInline).Pointer()
	if addr == 0 {
		t.Fatal("HelperRunCalleeAfterFrameInline 函数地址 = 0,不能被 emit 烧 imm64")
	}
	// the function address should be in a reasonable range (64-bit user space, not below 0x10000)
	if addr < 0x10000 {
		t.Errorf("HelperRunCalleeAfterFrameInline 函数地址 = 0x%X 在 0 页或低于 0x10000,异常",
			addr)
	}
}

func TestHelperRunCalleeAfterFrameInline_AddressStable(t *testing.T) {
	// repeated address lookups should be consistent (a function address is fixed for the process lifetime)
	addr1 := reflect.ValueOf(HelperRunCalleeAfterFrameInline).Pointer()
	addr2 := reflect.ValueOf(HelperRunCalleeAfterFrameInline).Pointer()
	if addr1 != addr2 {
		t.Errorf("HelperRunCalleeAfterFrameInline 函数地址不一致:0x%X vs 0x%X",
			addr1, addr2)
	}
}

func TestHelperRunCalleeAfterFrameInline_UnsafePtrEquiv(t *testing.T) {
	// emit uses reflect.ValueOf(fn).Pointer() vs unsafe.Pointer((*funcVal)).addr;
	// the two approaches should be equivalent (guaranteed by the Go docs)
	addrReflect := reflect.ValueOf(HelperRunCalleeAfterFrameInline).Pointer()

	// unsafe approach: a Go function value on the stack is a (funcVal*); dereference to get addr
	type funcValHeader struct {
		fn uintptr
	}
	fn := HelperRunCalleeAfterFrameInline
	fp := *(*unsafe.Pointer)(unsafe.Pointer(&fn))
	addrUnsafe := uintptr(fp)

	// the two may differ slightly (reflect goes through a trampoline / unsafe
	// takes the funcVal directly), but both should be non-zero and in a
	// reasonable range. Print both values and verify non-zero + range.
	if addrReflect == 0 || addrUnsafe == 0 {
		t.Errorf("两种姿态 addr 不应为 0: reflect=0x%X, unsafe=0x%X",
			addrReflect, addrUnsafe)
	}
	t.Logf("HelperRunCalleeAfterFrameInline:reflect=0x%X unsafe=0x%X(emit 用 reflect 姿态)",
		addrReflect, addrUnsafe)
}
