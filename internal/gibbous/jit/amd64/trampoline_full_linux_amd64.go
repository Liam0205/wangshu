//go:build wangshu_p4 && linux && amd64

package amd64

// CallJITFull — the full trampoline (switch registers + set up jitContext +
// save callee-saved).
//
// **PJ2 status: draft (NOT WIRED)** — fully implemented, but GibbousCode.Run
// currently does **not** call this function (SupportsAllOpcodes is still all
// false). The point of landing this function is to get the Go runtime
// compatibility issues (callee-saved save/restore / loading jitContext into
// r15) through compile-time checks + unit tests already in the PJ2 phase; PJ3+
// wires it directly into GibbousCode.Run at startup.
//
// Params:
//   - codeAddr: mmap segment start ((*CodePage returned by MmapCode).Addr())
//   - jitCtx: the uintptr for JITContext (unsafe.Pointer(*JITContext))
//
// Returns: the RAX value at the mmap segment's RET.
//
// Differences from callJIT:
//   - callJIT: simplified form, doesn't save callee-saved, doesn't load r15;
//     the template only runs mov+ret;
//   - callJITFull: full form, saves rbx/rbp/r12/r13/r15 (r14 = Go G register,
//     left untouched)
//   - loads r15 = jitContext; the template can read r15+offset to get
//     arenaBase / value-stack base / preemptFlag / helper table (reserved for
//     PJ3+ template extensions);
//
//go:noescape
func callJITFull(codeAddr uintptr, jitCtx uintptr) uint64

// CallJITFull is the visible wrapper around callJITFull (for unit tests +
// caller wiring).
func CallJITFull(codeAddr uintptr, jitCtx uintptr) uint64 {
	return callJITFull(codeAddr, jitCtx)
}
