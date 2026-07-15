//go:build wangshu_p4 && amd64

// Package amd64 implements the P4 amd64 backend emitter.
//
// **PJ0 package skeleton**: only doc.go as a placeholder; from PJ1 onward fill
// in the Emitter trait + trampoline asm stub + straight-line templates
// (MOVE/LOADK/LOADBOOL/LOADNIL/JMP/RETURN).
//
// Register conventions (`docs/design/p4-method-jit/06-backends.md` §4.1, the
// single source of truth for P4):
//   - r15: jitContext (resident, loaded when trampoline switches SP)
//   - r14: arena base (resident, mirror of jitContext)
//   - rbx: value stack base (loaded per-call, written back to jitContext.exitBase on OSR exit)
//   - rax-r9: scratch (used per-template)
//   - xmm0-xmm3: used by the f64 fast path
//
// Go ABI0 compatibility: r12-r15 (callee-saved) + rbp (frame pointer) must be
// saved on trampoline entry and restored on exit; the Go runtime's GS/FS
// segment registers are left untouched.
package amd64
