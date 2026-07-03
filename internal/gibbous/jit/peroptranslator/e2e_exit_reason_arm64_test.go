//go:build wangshu_p4 && arm64 && (linux || (darwin && cgo)) && !wangshu_qemu

// e2e_exit_reason_arm64_test.go - arm64 exit-reason protocol port
// (issue #37 step 1): verify the emitExitReasonArm64 packing + the
// resume-prelude patching by actually executing the mmap segment on
// real arm64 hardware, and verify the Run-level dispatcher loop
// handles GETUPVAL / SETUPVAL round trips.
//
// prove-the-path notes: asserting only "output is correct" cannot
// distinguish the exit-reason path from an interpreter fallback, so
// these tests assert the protocol artifacts themselves (X0 status ==
// ExitInlineHelper, exitArg0 packing, resumeOff pointing at a
// reenterable instruction) and drive the second entry manually.
package peroptranslator

import (
	"testing"
	"unsafe"

	jit "github.com/Liam0205/wangshu/internal/gibbous/jit"
	jitarm64 "github.com/Liam0205/wangshu/internal/gibbous/jit/arm64"
)

// runSegmentArm64 mmaps cb's bytes and calls the segment once with a
// fresh jitCtx + scratch value stack. Returns (ctx, stack pointer,
// status). The caller drives any reentry itself via
// jitarm64.CallJITSpec(page.Addr()+resumeOff, ...).
func runSegmentArm64(t *testing.T, cb *codeBuf, host jit.P4HostState) (*jit.JITContext, uintptr, *jitarm64.CodePage, uint64) {
	t.Helper()
	page, err := jitarm64.MmapCode(cb.bytes)
	if err != nil {
		t.Fatalf("MmapCode: %v", err)
	}
	t.Cleanup(func() { _ = page.Dispose() })

	ctx := jit.NewJITContext()
	if host != nil {
		ctx.SetHostRef(hostIfaceHeader(host))
	}
	var scratch [16]uint64
	vsBase := uintptr(unsafe.Pointer(&scratch[0]))
	ctx.SetValueStackBase(vsBase)

	status := jitarm64.CallJITSpec(page.Addr(), uintptr(unsafe.Pointer(ctx)), vsBase)
	return ctx, vsBase, page, status
}

// TestArm64ExitReason_GETUPVAL_Packing: emit GETUPVAL A=1 B=2 followed
// by a RETURN-shaped exit. First entry must exit with ExitInlineHelper
// and exitArg0 carrying (HelperGetUpval, a=1, b=2); reentry at
// resumeOff must run the tail and exit with status 0.
func TestArm64ExitReason_GETUPVAL_Packing(t *testing.T) {
	cb := newCodeBuf(1)
	if err := cb.bindLabel(0); err != nil {
		t.Fatal(err)
	}
	emitGETUPVALArm64(cb, 1, 2)
	// Bind the resume entry (patches the pending movz/movk pair and
	// emits the X26 reload), then terminate with `mov x0, #0; ret`.
	emitResumePreludeIfPendingArm64(cb)
	cb.emit(jitarm64.EmitMovzWdImm16(nil, 0, 0))
	cb.emit(jitarm64.EmitRet(nil))

	ctx, vsBase, page, status := runSegmentArm64(t, cb, nil)
	if uint32(status) != jit.ExitInlineHelper {
		t.Fatalf("status = %d, want ExitInlineHelper(%d)", status, jit.ExitInlineHelper)
	}
	arg0 := ctx.ExitArg0()
	if got := arg0 & jit.HelperCodeMask; got != jit.HelperGetUpval {
		t.Fatalf("helper code = %d, want HelperGetUpval(%d)", got, jit.HelperGetUpval)
	}
	if a := (arg0 >> 16) & 0xFF; a != 1 {
		t.Errorf("packed a = %d, want 1", a)
	}
	if b := (arg0 >> 24) & 0x1FF; b != 2 {
		t.Errorf("packed b = %d, want 2", b)
	}
	// Reenter at resumeOff: must run the tail (mov x0, #0; ret).
	resumeOff := ctx.ResumeOff()
	if resumeOff == 0 {
		t.Fatal("resumeOff = 0: resume-prelude patch did not run")
	}
	status2 := jitarm64.CallJITSpec(page.Addr()+uintptr(resumeOff),
		uintptr(unsafe.Pointer(ctx)), vsBase)
	if status2 != 0 {
		t.Fatalf("reentry status = %d, want 0", status2)
	}
}

// TestArm64ExitReason_SETUPVAL_Packing: same shape for SETUPVAL A=3 B=1.
func TestArm64ExitReason_SETUPVAL_Packing(t *testing.T) {
	cb := newCodeBuf(1)
	if err := cb.bindLabel(0); err != nil {
		t.Fatal(err)
	}
	emitSETUPVALArm64(cb, 3, 1)
	emitResumePreludeIfPendingArm64(cb)
	cb.emit(jitarm64.EmitMovzWdImm16(nil, 0, 0))
	cb.emit(jitarm64.EmitRet(nil))

	ctx, _, _, status := runSegmentArm64(t, cb, nil)
	if uint32(status) != jit.ExitInlineHelper {
		t.Fatalf("status = %d, want ExitInlineHelper(%d)", status, jit.ExitInlineHelper)
	}
	arg0 := ctx.ExitArg0()
	if got := arg0 & jit.HelperCodeMask; got != jit.HelperSetUpval {
		t.Fatalf("helper code = %d, want HelperSetUpval(%d)", got, jit.HelperSetUpval)
	}
	if a := (arg0 >> 16) & 0xFF; a != 3 {
		t.Errorf("packed a = %d, want 3", a)
	}
	if b := (arg0 >> 24) & 0x1FF; b != 1 {
		t.Errorf("packed b = %d, want 1", b)
	}
}

// TestArm64ExitReason_ResumeOffWide: resumeOff over 0xFFFF must survive
// the movz/movk imm32 split. Pad the segment with >64 KiB of LOADK
// emits before the exit-reason op so the resume entry lands beyond the
// 16-bit boundary.
func TestArm64ExitReason_ResumeOffWide(t *testing.T) {
	cb := newCodeBuf(1)
	if err := cb.bindLabel(0); err != nil {
		t.Fatal(err)
	}
	// Each LOADK emit is 20 bytes (16B movz/movk×3 + 4B str); 3300 of
	// them ≈ 66 KB, pushing the exit-reason emit past 0xFFFF.
	for i := 0; i < 3300; i++ {
		emitLOADKArm64(cb, 4, 0x3FF0_0000_0000_0000)
	}
	emitGETUPVALArm64(cb, 1, 2)
	emitResumePreludeIfPendingArm64(cb)
	cb.emit(jitarm64.EmitMovzWdImm16(nil, 0, 0))
	cb.emit(jitarm64.EmitRet(nil))

	ctx, vsBase, page, status := runSegmentArm64(t, cb, nil)
	if uint32(status) != jit.ExitInlineHelper {
		t.Fatalf("status = %d, want ExitInlineHelper(%d)", status, jit.ExitInlineHelper)
	}
	resumeOff := ctx.ResumeOff()
	if resumeOff <= 0xFFFF {
		t.Fatalf("resumeOff = %d, want > 0xFFFF (test setup must push it past 16 bits)", resumeOff)
	}
	status2 := jitarm64.CallJITSpec(page.Addr()+uintptr(resumeOff),
		uintptr(unsafe.Pointer(ctx)), vsBase)
	if status2 != 0 {
		t.Fatalf("reentry status = %d, want 0", status2)
	}
}
