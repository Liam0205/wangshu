//go:build wangshu_p3

package wasm

// Closure construction + scoped upvalue closing (PW7, 02-translation §3.7).
//
// CLOSURE/CLOSE both go through helpers: CLOSURE involves arena allocation +
// open/close upvalue chain management (gibbous itself never allocates,
// 00-overview §8); CLOSE closing open upvalues is a Go-side open-chain
// operation. Both reuse the interpreter's makeClosure/closeUpvals, so
// byte-equal holds naturally.
//
// CLOSURE is followed by SubNUps[Bx] pseudo-instructions (MOVE/GETUPVAL
// describing upvalue capture) that the emit layer skips translating
// (translate.go emitOpcode returns skip); makeClosure inside the helper reads
// ci.pc to consume them.

import "github.com/Liam0205/wangshu/internal/bytecode"

// emitClosure CLOSURE A Bx —— R(A) := closure(Proto[Bx]) (02 §3.7.1).
//
//	(local.set $st (call h_closure(base,pc,a,bx)))   ;; helper: makeClosure + setReg + safepoint
//	(if (i32.eq $st 1) (then (return 1)))            ;; ERR bubble-up (CLOSURE should not error in theory, defensive)
//
// No base refresh needed: makeClosure does not enter a nested Lua frame and
// does not growStack (allocation triggers arena.grow64, keeping the segment
// offset unchanged, form Y safe).
func (c *Compiler) emitClosure(em *emitter, ins bytecode.Instruction, pc int32) {
	a := int32(bytecode.A(ins))
	bx := int32(bytecode.Bx(ins))
	em.localGet(localBase)
	em.i32Const(pc)
	em.i32Const(a)
	em.i32Const(bx)
	em.call(helperClosure)
	em.localTee(localI32)
	em.i32Const(1)
	em.i32Eq()
	em.ifVoid()
	em.i32Const(1)
	em.ret()
	em.end()
}

// emitClose CLOSE A —— close all open upvalues ≥ R(A) (02 §3.7.2, pure state operation).
//
//	(call h_close(base,pc,a))   ;; helper: closeUpvals; returns status always 0, discarded
//
// CLOSE has no error and no return value; the helper returns i32 status
// (signature reuses typeForPrep), and the emit side drops it.
func (c *Compiler) emitClose(em *emitter, ins bytecode.Instruction, pc int32) {
	a := int32(bytecode.A(ins))
	em.localGet(localBase)
	em.i32Const(pc)
	em.i32Const(a)
	em.call(helperClose)
	em.drop() // status always 0, discarded
}
