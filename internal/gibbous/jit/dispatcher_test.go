//go:build wangshu_p4

package jit

import (
	"reflect"
	"testing"
)

// dispatcher_test.go — Spike 1 trampoline exit-resume protocol commit-3a
// dispatcher function-address static test (per §9.20.9 (5) Go-side dispatcher
// protocol + the same template as helpers_test.go).
//
// Wiring prerequisite: the trampoline asm calls this function via
// `CALL ·dispatchInlineHelper(SB)`, and the asm linker resolves the address
// through the function symbol. This test uses reflect.ValueOf to verify the
// function symbol can be obtained from the Go side, ensuring the trampoline asm
// links without error.
//
// **archSupportsFrameInline=false disables real triggering**; this test only
// verifies the function symbol exists + the emit address is obtainable; the
// production path does not reach this dispatcher (enabled after the commit-5
// switch is flipped).

func TestDispatchInlineHelper_AddressNonZero(t *testing.T) {
	addr := reflect.ValueOf(dispatchInlineHelper).Pointer()
	if addr == 0 {
		t.Fatal("dispatchInlineHelper 函数地址 = 0,trampoline asm CALL 不能符号化")
	}
	if addr < 0x10000 {
		t.Errorf("dispatchInlineHelper 函数地址 = 0x%X 在 0 页或低于 0x10000,异常",
			addr)
	}
}

func TestDispatchInlineHelper_AddressStable(t *testing.T) {
	addr1 := reflect.ValueOf(dispatchInlineHelper).Pointer()
	addr2 := reflect.ValueOf(dispatchInlineHelper).Pointer()
	if addr1 != addr2 {
		t.Errorf("dispatchInlineHelper 函数地址不一致:0x%X vs 0x%X", addr1, addr2)
	}
}
