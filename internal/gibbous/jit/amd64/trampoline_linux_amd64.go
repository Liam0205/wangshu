//go:build wangshu_p4 && linux && amd64

package amd64

// callJIT jumps into the mmap segment to execute, expecting the segment to end
// with RET (return value in RAX).
//
// **PJ1 simplified version** (following spike/p4tramp): does not switch to a
// self-managed stack, does not load jitContext. The full form (switch SP / load
// r15=jitContext / r14=arena base / rbx=value stack base + save callee-saved)
// is filled in progressively across PJ2-PJ5.
//
// This simplified form is only for PJ1 straight-line templates (LOADK burning
// an imm into RAX, LOADBOOL burning 0/1, etc.) — these opcodes don't call
// helpers, don't touch the stack, and hold no external state, so a single
// trampoline CALL + RET suffices. When PJ2 arithmetic / PJ4 table IC introduce
// helper calls, this stub is upgraded to the full version.
//
// The codeAddr argument must point to a PROT_READ|PROT_EXEC segment (the
// CodePage.Addr() returned by MmapCode).
//
// Implementation: trampoline_amd64.s::callJIT (NOSPLIT|NOFRAME).
//
//go:noescape
func callJIT(codeAddr uintptr) uint64

// CallJIT is the visible wrapper for callJIT. Tests/callers invoke through it,
// allowing in-package unit tests to assert that "the mmap segment was actually
// reached" (the prove-the-path-under-test discipline).
//
// Note: this PJ1 stage introduces no codeAddr validation (nil ptr / bounds
// checks are left to the PJ2+ full version). The caller is responsible for
// passing a valid address (the (*CodePage).Addr() returned by MmapCode).
func CallJIT(codeAddr uintptr) uint64 {
	return callJIT(codeAddr)
}
