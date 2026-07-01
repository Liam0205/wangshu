//go:build wangshu_p4 && amd64

// emit_amd64.go — PJ10 native amd64 op emitters. Each function appends
// the byte sequence for one op to a codeBuf. No helpers, pure memory
// moves for now (PJ10a subset that doesn't need to call into Go).
//
// Register conventions inherited from trampoline_spec_amd64.s + doc §4:
//   - RBX (reg 3) = valueStackBase — R(N) is at [rbx + N*8]
//   - R15 = jitCtx (untouched by PJ10a emits)
//   - RAX = scratch (single-op scope; not cross-op stable)
//
// Every emitter takes (*codeBuf, ...op args). It calls existing amd64
// primitives from internal/gibbous/jit/amd64/ so byte-level testing lives
// in one place. Length is fixed per op — helpful for the label resolver
// but not required.
package peroptranslator

import (
	jitamd64 "github.com/Liam0205/wangshu/internal/gibbous/jit/amd64"
	"github.com/Liam0205/wangshu/internal/value"
)

const (
	// regRBX is the SysV / P4-JIT valueStackBase register (baseReg=3).
	regRBX uint8 = 3
)

// emitMOVE emits `mov rax, [rbx+B*8]; mov [rbx+A*8], rax` — 14 bytes.
// R(A) := R(B).
func emitMOVE(cb *codeBuf, a, b uint8) {
	buf := jitamd64.EmitMovqRaxFromMemReg(nil, regRBX, int32(b)*8)
	buf = jitamd64.EmitMovqMemRegFromRax(buf, regRBX, int32(a)*8)
	cb.emit(buf)
}

// emitLOADK emits `mov rax, imm64; mov [rbx+A*8], rax` — 17 bytes.
// R(A) := K(Bx). The immediate is baked at compile time (already a
// nan-boxed uint64 for numbers / booleans / nil).
//
// Callers must reject string constants upstream (F7 gate) — the string
// Nil placeholder in Proto.Consts becomes a nil emit here, which is not
// what a string constant means. Standard PJ0-PJ9 practice; PJ10 inherits.
func emitLOADK(cb *codeBuf, a uint8, imm uint64) {
	buf := jitamd64.EmitMovRaxImm64(nil, imm)
	buf = jitamd64.EmitMovqMemRegFromRax(buf, regRBX, int32(a)*8)
	cb.emit(buf)
}

// emitLOADBOOL_valueOnly emits the R(A) := bool(B) part only.
// R(A) := true/false. LOADBOOL C != 0 also does pc++; that's handled at
// the terminator level by the CFG (buildCFG links only the live successor
// by C field), so this emit function only writes the value.
func emitLOADBOOL_valueOnly(cb *codeBuf, a, b uint8) {
	var imm uint64
	if b != 0 {
		imm = uint64(value.True)
	} else {
		imm = uint64(value.False)
	}
	emitLOADK(cb, a, imm)
}

// emitLOADNIL emits R(A..B) := nil as a loop of stores. `B` here is the
// last register index (inclusive), matching Lua 5.1 LOADNIL semantics.
//
// Each nil write is 17 bytes (mov rax, NilBits; mov [rbx+i*8], rax).
// For N slots, N*17 bytes. In practice LOADNIL almost always covers 1-2
// slots so the loop is tiny; we don't bother with rep-store optimizations.
//
// Note: we could hoist the `mov rax, NilBits` outside the loop to save
// (N-1)*10 bytes, but that requires reasoning about RAX liveness across
// stores. PJ10 groundwork keeps each emit self-contained (no cross-op
// register liveness) — matches the doc §4 "no cross-op regalloc" rule.
func emitLOADNIL(cb *codeBuf, a, b uint8) {
	nilBits := uint64(value.Nil)
	for i := int32(a); i <= int32(b); i++ {
		emitLOADK(cb, uint8(i), nilBits)
	}
}

// emitJMP emits `jmp rel32` — 5 bytes with a zero placeholder + fixup.
// Caller passes the target BB id; codeBuf.resolveLabels patches the rel32
// after all BBs are bound.
func emitJMP(cb *codeBuf, targetBB int) {
	patchOff := cb.pos() + 1 // skip the 0xE9 opcode byte
	cb.emit([]byte{0xE9, 0, 0, 0, 0})
	cb.addFixup(patchOff, cb.pos(), targetBB)
}
