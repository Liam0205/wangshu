//go:build wangshu_p4

// dispatcher.go —— Go-side dispatcher for the P4 Option B Spike 1 trampoline
// exit-resume protocol (per
// docs/design/p4-method-jit/implementation-progress.md §9.20.9 (5) Go-side
// dispatcher detailed design + implementation order 5 commits commit-3a).
//
// **Protocol position** (per §9.20.9 (1)):
//
//	[mmap segment] exit-helper-request segment writes jitCtx.exitArg0=HelperRunCallee + ret
//	   │
//	   ▼
//	[trampoline asm] CMPQ AX, $ExitInlineHelper / CALL ·dispatchInlineHelper
//	   │
//	   ▼
//	[this file dispatchInlineHelper] switch jitCtx.exitArg0 routes:
//	   case HelperRunCallee: via P4HostState.ExecuteCalleeFromInlineFrame
//	   case HelperGrowStack: arena grow (future)
//	   case HelperGCBarrier: GC write barrier (future, only when writing the Go heap)
//	   │
//	   ▼
//	[this file returns resumeAddr] trampoline re-CALLs with codePageAddr + resumeOff
//	   │
//	   ▼
//	[mmap segment resume entry] PopVoid0Arg + ret (callee frame already run by dispatcher)
//
// **Current Spike 1 stage status** (2026-06-28, commit-3a):
//   - archSupportsFrameInline=false blocks real triggering; this dispatcher is
//     never reached on the production path, but its call site CALLed from the
//     trampoline asm (commit-3b) reserves the address + is used by test hooks.
//   - This batch is a panic placeholder (per the same engineering anchor as
//     jit.HelperRunCalleeAfterFrameInline); the real implementation lands with
//     commit-5 flipping archSupportsFrameInline=true in the same batch.
//   - host routing goes through the P4HostState interface injected by *Compiler
//     (per compiler.go::hostState field), without introducing a new
//     jitContext.hostStatePtr field (reduces the ABI surface; the real
//     implementation forwards from the trampoline asm via a dedicated helper
//     function).
//
// **Future real implementation path** (Step C-2, once archSupportsFrameInline
// flips to true):
//  1. dispatcher takes jitCtx and also obtains host (injected via a dedicated setter)
//  2. switch jitCtx.exitArg0 case HelperRunCallee: host.ExecuteCalleeFromInlineFrame
//  3. return codePageAddr + resumeOff to let the trampoline resume
//
// Design basis:
//   - §9.20.9 (5) Go-side dispatcher detailed design (switch + executeFrom)
//   - §9.20.9 (8) risk: executeFrom inside the dispatcher is not nosplit → must
//     switch back to the Go stack before calling (per §9.20.6 (4) SP switch protocol)
//   - §9.20.9 (8) error bubbling: when HelperRunCalleeAfterFrameInline raises, it
//     sets jitCtx.exitReason=ExitError + pendingErr, and the dispatcher returns 0
//     to make the trampoline take the error path
package jit

// dispatchInlineHelper is the helper-request router for after the trampoline
// exits its segment (per §9.20.9 (5)). When the trampoline asm detects
// AX=ExitInlineHelper it CALLs this function; this function reads
// jitCtx.exitArg0, routes to the corresponding helper, and returns resumeAddr
// (0 means the error path).
//
// **Params**:
//   - jitCtx: *JITContext (per the trampoline asm copying r15 to rdi/SysV ABI)
//
// **Returns**:
//   - resumeAddr (uintptr): resume entry address inside the mmap segment
//     (codePageAddr + resumeOff); 0 means error (trampoline takes the error path)
//
// **Not yet implemented in the Spike 1 stage**: this stub panics to mark the
// path is not really wired up — archSupportsFrameInline is currently false, so
// the Compile path never really emits the ExitInlineHelper protocol; the
// trampoline asm also does not CALL this function (commit-3b adds the dispatcher
// CALL segment but the dispatcher does not route); the production path is
// shielded from SIGSEGV risk. The real implementation lands with commit-5
// flipping archSupportsFrameInline=true in the same batch.
//
// **nosplit + noinline**: per the §9.20.6 (4) helper ABI protocol + §9.20.9 (8)
// risk mitigation.
//
//go:nosplit
//go:noinline
func dispatchInlineHelper(jitCtx *JITContext) uintptr {
	_ = jitCtx
	// **Not-yet-implemented placeholder**: for the commit-5 real implementation,
	// remove the panic and add the switch jitCtx.exitArg0 + host routing logic.
	// archSupportsFrameInline=false currently blocks the real call site; this
	// panic is an engineering anchor (reaching it = real wiring not enabled /
	// Compile bug).
	panic("internal/gibbous/jit.dispatchInlineHelper: not implemented (Spike 1 commit-5 占位)")
}
