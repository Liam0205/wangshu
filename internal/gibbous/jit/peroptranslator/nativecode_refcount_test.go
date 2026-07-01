//go:build wangshu_p4 && amd64 && linux

package peroptranslator

import (
	"testing"

	jit "github.com/Liam0205/wangshu/internal/gibbous/jit"
	jitamd64 "github.com/Liam0205/wangshu/internal/gibbous/jit/amd64"
)

// TestNativeCode_Run_RefusesAfterDispose verifies that nativeCode.Run
// wires the CodePage.Enter refcount protocol: once Dispose has been
// called, Run must refuse (return error sentinel 1) instead of touching
// the disposed segment.
//
// This is the acceptance test for the follow-up review comment on PR #31
// where the initial refcount fix landed the protocol in code.go /
// peropcode.go but missed nativeCode (translator_native.go /
// translator_native_arm64.go). Without wiring Enter/Exit into
// nativeCode.Run, multi-State concurrent Dispose could munmap the
// segment while another State's Run was still executing native code
// through CallJITSpec.
func TestNativeCode_Run_RefusesAfterDispose(t *testing.T) {
	// Minimal RET-only executable segment (0xC3 = amd64 RET). Big enough
	// to satisfy MmapCode's non-empty check; MmapCode pads to a page.
	page, err := jitamd64.MmapCode([]byte{0xC3})
	if err != nil {
		t.Fatalf("MmapCode: %v", err)
	}

	nc := &nativeCode{
		codePage: page,
		jitCtx:   &jit.JITContext{},
		host:     &fakeHost{regs: map[int32]uint64{}, upvals: map[int32]uint64{}},
		retPC:    0,
		retA:     0,
		retB:     1,
	}

	// Dispose flips the disposed flag on the CodePage and drops the
	// constructor's initial ref. With no active Enter, refcount reaches
	// 0 here and the segment is munmap'd synchronously.
	nc.Dispose()

	// A subsequent Run must not touch the disposed segment. If Enter
	// wiring is missing (the bug this test guards), the raw CallJITSpec
	// would jump into a released page and crash (SIGSEGV or worse).
	// With refcount wiring in place, Enter returns false and Run returns
	// the error sentinel 1 without executing the native code.
	stack := make([]uint64, 4)
	status := nc.Run(stack, 0)
	if status != 1 {
		t.Fatalf("Run after Dispose: status = %d, want 1 (Enter refusal sentinel)", status)
	}
	if stack[0] != 1 {
		t.Fatalf("Run after Dispose: stack[0] = %d, want 1 (error sentinel written)", stack[0])
	}
}

// TestNativeCode_Dispose_Idempotent verifies double-Dispose does not
// panic. Bridge teardown paths occasionally call Dispose from multiple
// spots (compile failure cleanup + normal tier teardown) and the refcount
// protocol must tolerate that.
func TestNativeCode_Dispose_Idempotent(t *testing.T) {
	page, err := jitamd64.MmapCode([]byte{0xC3})
	if err != nil {
		t.Fatalf("MmapCode: %v", err)
	}
	nc := &nativeCode{codePage: page, jitCtx: &jit.JITContext{}, host: &fakeHost{}}
	nc.Dispose()
	nc.Dispose() // second call must not panic on nil codePage.
	nc.Dispose() // and a third.
}
