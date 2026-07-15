//go:build linux && amd64

package p4tramp

// CallJIT jumps into the mmap segment and executes it, expecting the segment
// to end with `ret` (the return value is in RAX).
//
// **PJ1 spike simplified version**: does not switch to a self-managed stack,
// does not install a jitContext, does not pass any arguments; it only treats
// codeAddr as a CALL target and expects the segment to return RAX to Go. The
// full PJ1 trampoline (switch SP / load r15=jitContext / r14=arena base /
// rbx=value-stack base) is landed in
// internal/gibbous/jit/amd64/trampoline_amd64.s after the spike goes green.
//
// **What it does not do** (follows the spike gate minimization discipline):
//   - does not save callee-saved registers (the spike segment's mov+ret does
//     not touch them);
//   - does not switch SP (the spike segment does not recurse deeply / does not
//     call any helper);
//   - does not carry GC-safe-point discipline (the spike segment executes
//     instantaneously; a Go runtime async preemption landing on an mmap PC is
//     unrecoverable -- this is exactly one of the problems the full PJ1
//     version must solve; this spike works around the risk with an extremely
//     short straight-line segment: `mov+ret` is 9 bytes, actually executes in
//     ~ns, and the probability of being async-preempted is near 0);
//
// The codeAddr argument must point to a PROT_READ|PROT_EXEC segment (what
// MmapCode returns, CodePage.Addr()).
//
// Implementation (callJITAmd64.s): a `JMP AX`-style CALL -- relies on the Go
// ABI0 convenience that "the CALL instruction does not expect the callee to
// use an SP frame"; the mmap segment ends with ret, landing at the `RET` at
// the end of callJIT in the same form (the Go side gets the mmap segment's RAX
// return value).
func CallJIT(codeAddr uintptr) uint64

// The Go-side usable name for callJIT (for tests -- exposes the internal asm
// symbol).
//
// Note: this spike does not introduce codeAddr validation (nil ptr /
// out-of-bounds checks are left for the full PJ1 version); the test side is
// responsible for passing a valid address.
