//go:build wangshu_p4

// helpers.go —— the P4 Option B Spike 1 helper function set (per
// docs/design/p4-method-jit/implementation-progress.md §9.20.6 helper call
// ABI protocol design).
//
// **Key ABI constraints** (per §9.20.6):
//   - `//go:nosplit`: disables morestack instrumentation; the helper runs on
//     the self-managed stack (per 05 §3.4 self-managed stack protocol)
//   - `//go:noinline`: avoids inlining breaking the stack frame protocol (the
//     mmap segment calls the helper address indirectly via archEmitHelperCall;
//     after inlining the address disappears)
//   - first arg `*JITContext` (amd64 rdi / arm64 x0 SysV ABI register)
//   - subsequent arguments in SysV order (rsi/rdx/rcx/r8/r9)
//   - return value rax / x0 (0=OK / 1=ERR, error status written to
//     jitContext.exitReason)
//   - r14=G (amd64) / x28=G (arm64) strictly untouched
//   - the mmap segment must not write Go heap pointers directly (GC tricolor
//     invariant)
//
// **Spike 1 phase status** (2026-06-28): the helper function declarations are
// in place + archEmitHelperCall reserves the call-site address, but the actual
// hookup on the Run side is deferred to Step E (compileSpecSelfCall enabling
// useFrameInline=true + archSupportsFrameInline flipping true in the same
// batch). Currently archSupportsFrameInline=false masks off the real call
// path, so this helper is not reached on the production path, but its function
// address can be referenced by archEmitHelperCall as an emit target.
package jit

// HelperRunCalleeAfterFrameInline is called once the mmap segment has completed
// the byte-level inline of enterLuaFrame via BuildVoid0ArgSkeleton, to run the
// callee Lua body execution (equivalent to skipping enterLuaFrame's doCall +
// executeFrom flow).
//
// Arguments (SysV ABI, amd64 rdi/rsi/rdx order):
//   - jitCtx: *JITContext (per r15 → rdi)
//   - base: caller frame R0 offset (per jitContext.valueStackBase to compute
//     the stack address)
//   - retA: the CALL segment A field (callee return values land in
//     R(retA..retA+N-1))
//
// Return value: 0=OK (the callee body has completed + return values landed in
// R(retA..)) / 1=ERR (error status written to jitContext.exitReason; the mmap
// segment tail checks rax=1 and jumps to the jitExit stub).
//
// **Not implemented in the Spike 1 phase**: this stub's panic marks that the
// path is not yet hooked up — archSupportsFrameInline is currently false, the
// Compile path will not actually emit a call to this function, and the
// production path masks off the SIGSEGV risk. Real implementation is deferred
// to the Step C-1 helper implementation batch (per §9.20.6 + §9.20.3 schedule
// estimate).
//
//go:nosplit
//go:noinline
func HelperRunCalleeAfterFrameInline(jitCtx *JITContext, base int32, retA int32) int32 {
	_ = jitCtx
	_ = base
	_ = retA
	// **Unimplemented placeholder**: when really implementing in Step C-1,
	// remove the panic and add the doCall + executeFrom logic. Currently
	// archSupportsFrameInline=false masks off the real call; this panic is an
	// engineering anchor point (call-site exposure = hookup not yet enabled /
	// Compile bug).
	panic("internal/gibbous/jit.HelperRunCalleeAfterFrameInline: not implemented (Spike 1 Step C-1 占位)")
}
