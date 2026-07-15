//go:build wangshu_p4 && arm64 && (linux || (darwin && cgo))

package arm64

// callJITFull jumps into the mmap segment to execute, expecting the segment to
// end with RET (0xd65f03c0) (return value in X0). The trampoline saves
// callee-saved X19-X27 + LR and loads X27=jitContext.
//
// **PJ8 simplified form** (matching amd64 PJ2): no SP switch — keeps running the
// mmap segment on the Go stack. Full SP switching is deferred to PJ3+ (enabled
// when arithmetic + helper calls introduce a sub-BLR).
//
// The codeAddr argument must point to a PROT_READ|PROT_EXEC segment (returned by
// MmapCode as CodePage.Addr()), and the segment must already be icache flushed
// (otherwise instruction fetch errors).
//
// Implementation: trampoline_arm64.s::callJITFull (NOSPLIT, $80 framesize — LR/FP
// are auto-managed by the Go compiler, callee-saved X19-X27 are saved manually
// via 5 STP pairs, aligned to 16 bytes).
//
//go:noescape
func callJITFull(codeAddr uintptr, jitCtxAddr uintptr) uint64

// CallJITFull is the visible wrapper for callJITFull. Tests/callers invoke it
// through here, allowing in-package unit tests to assert "the mmap segment was
// actually reached" (the prove-the-path-under-test discipline).
//
// Note: on arm64 the mmap segment must pass through flushICacheArm64 before it
// can execute, otherwise i-cache and d-cache are inconsistent (instruction fetch
// error, 03 §2.3.1). MmapCode already flushes at the end.
func CallJITFull(codeAddr uintptr, jitCtxAddr uintptr) uint64 {
	return callJITFull(codeAddr, jitCtxAddr)
}

// callJITSpec jumps into the mmap segment (spec template: PJ2 speculative
// arithmetic / PJ3 FORLOOP body/RegLimit), expecting the segment to end with RET
// (return value in X0). The trampoline loads X27=jitContext +
// **X26=valueStackBase** (the spec template needs value-stack addressing,
// matching amd64 callJITSpec loading rbx=vsBase + r15=jitCtx).
//
// **vs callJITFull**: one extra vsBaseAddr argument + loads X26. The FORLOOP
// body/RegLimit template + the PJ2 speculative template both use [x26+B*8] to
// address the value stack.
//
// Implementation: trampoline_arm64.s::callJITSpec (NOSPLIT, $80-32 framesize same
// as callJITFull, LR/FP auto-managed by the Go compiler, callee-saved X19-X27
// saved manually via 5 STP pairs, aligned to 16 bytes).
//
//go:noescape
func callJITSpec(codeAddr uintptr, jitCtxAddr uintptr, vsBaseAddr uintptr) uint64

// CallJITSpec is the visible wrapper for callJITSpec. Tests/callers invoke it
// through here, allowing in-package unit tests to assert "the mmap segment was
// actually reached + spec-form value-stack addressing works" (the
// prove-the-path-under-test discipline).
//
// Note: same as CallJITFull, the mmap segment must pass through flushICacheArm64
// before it can execute.
func CallJITSpec(codeAddr uintptr, jitCtxAddr uintptr, vsBaseAddr uintptr) uint64 {
	return callJITSpec(codeAddr, jitCtxAddr, vsBaseAddr)
}
