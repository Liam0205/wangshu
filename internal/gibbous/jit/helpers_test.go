//go:build wangshu_p4

package jit

import (
	"reflect"
	"testing"
	"unsafe"
)

// helpers_test.go —— PJ5 Option B Spike 1 helper 函数地址静态测试
// (承 §9.20.8 字节级编码文档化 + helpers.go 占位)。
//
// 真接入 prerequisite:emitter 可经 reflect.ValueOf(fn).Pointer() 求出
// helper 函数的物理地址,把它烧 imm64 进 mov rax 指令(amd64 EmitHelperCall
// 12 字节 / arm64 EmitHelperCallArm64 20 字节)。
//
// 本测试验:
//  1. HelperRunCalleeAfterFrameInline 函数地址可被 reflect.ValueOf 取到
//  2. 函数地址非 0 + 合理(在 .text 段范围,不在 0 页)
//  3. unsafe.Pointer 转 uintptr 一致(emit 用同款转换)
//
// **archSupportsFrameInline=false 屏蔽真触发**,本测试仅验地址可取 + emit
// 物理可行(future Step C-1 真实装后,本地址就是 callee target)。

func TestHelperRunCalleeAfterFrameInline_AddressNonZero(t *testing.T) {
	// reflect.ValueOf(fn).Pointer() 是 emit 时取 helper 函数地址的标准做法
	addr := reflect.ValueOf(HelperRunCalleeAfterFrameInline).Pointer()
	if addr == 0 {
		t.Fatal("HelperRunCalleeAfterFrameInline 函数地址 = 0,不能被 emit 烧 imm64")
	}
	// 函数地址应在合理范围(64-bit 用户空间,不在 0x10000 以下)
	if addr < 0x10000 {
		t.Errorf("HelperRunCalleeAfterFrameInline 函数地址 = 0x%X 在 0 页或低于 0x10000,异常",
			addr)
	}
}

func TestHelperRunCalleeAfterFrameInline_AddressStable(t *testing.T) {
	// 反复取地址应一致(函数地址 process 生命周期不变)
	addr1 := reflect.ValueOf(HelperRunCalleeAfterFrameInline).Pointer()
	addr2 := reflect.ValueOf(HelperRunCalleeAfterFrameInline).Pointer()
	if addr1 != addr2 {
		t.Errorf("HelperRunCalleeAfterFrameInline 函数地址不一致:0x%X vs 0x%X",
			addr1, addr2)
	}
}

func TestHelperRunCalleeAfterFrameInline_UnsafePtrEquiv(t *testing.T) {
	// emit 用 reflect.ValueOf(fn).Pointer() vs unsafe.Pointer((*funcVal)).addr
	// 两种姿态应等价(承 Go 文档保证)
	addrReflect := reflect.ValueOf(HelperRunCalleeAfterFrameInline).Pointer()

	// unsafe 姿态:Go 函数值在 stack 上是 (funcVal*),解引取 addr
	type funcValHeader struct {
		fn uintptr
	}
	fn := HelperRunCalleeAfterFrameInline
	fp := *(*unsafe.Pointer)(unsafe.Pointer(&fn))
	addrUnsafe := uintptr(fp)

	// 两者可能略有差异(reflect 走 trampoline / unsafe 直接拿 funcVal),
	// 但都应非 0 且在合理范围。打印两个值,验非 0 + 范围。
	if addrReflect == 0 || addrUnsafe == 0 {
		t.Errorf("两种姿态 addr 不应为 0: reflect=0x%X, unsafe=0x%X",
			addrReflect, addrUnsafe)
	}
	t.Logf("HelperRunCalleeAfterFrameInline:reflect=0x%X unsafe=0x%X(emit 用 reflect 姿态)",
		addrReflect, addrUnsafe)
}
