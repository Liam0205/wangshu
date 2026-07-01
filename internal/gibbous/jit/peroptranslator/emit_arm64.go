//go:build wangshu_p4 && arm64

// emit_arm64.go - arm64 counterparts of the PJ10 native op emitters.
//
// arm64 register convention (per docs/design/p4-method-jit/06-backends.md):
//   - X26 = valueStackBase   (analog of amd64 RBX)
//   - X27 = jitContext        (analog of amd64 R15)
//   - X28 = Go G              (analog of amd64 R14; permanent)
//
// Since X28 stays as G naturally (Go arm64 uses it as G), we don't need
// a save/restore protocol like amd64's savedGoG dance. Go ABIInternal on
// arm64 preserves X26 and X27 across calls... actually no, X26 is caller-
// saved. So we still need to reload X26 = vsBase after each helper call.
//
// For this initial arm64 landing we mirror the amd64 shim strategy but
// with arm64 encodings. Test coverage is provided in
// e2e_arm64_test.go under //go:build ...arm64.
package peroptranslator

import (
	jitarm64 "github.com/Liam0205/wangshu/internal/gibbous/jit/arm64"
	"github.com/Liam0205/wangshu/internal/value"
)

const (
	// regX0 is the arm64 first int arg (Go ABIInternal maps arg0 here).
	regX0 uint8 = 0
	// regX26 = vsBase.
	regX26 uint8 = 26
	// regX27 = jitCtx.
	regX27 uint8 = 27
)

// emitMOVEArm64 emits `ldr x0, [x26+B*8]; str x0, [x26+A*8]`.
// R(A) := R(B).
func emitMOVEArm64(cb *codeBuf, a, b uint8) {
	cb.emit(jitarm64.EmitLdrXtFromXnDisp(nil, 0, regX26, uint16(b)*8))
	cb.emit(jitarm64.EmitStrXtToXnDisp(nil, 0, regX26, uint16(a)*8))
}

// emitLOADKArm64 emits `mov x0, imm64; str x0, [x26+A*8]`.
func emitLOADKArm64(cb *codeBuf, a uint8, imm uint64) {
	cb.emit(jitarm64.EmitMovXdImm64(nil, 0, imm))
	cb.emit(jitarm64.EmitStrXtToXnDisp(nil, 0, regX26, uint16(a)*8))
}

// emitLOADBOOLArm64_valueOnly emits R(A) := True/False.
func emitLOADBOOLArm64_valueOnly(cb *codeBuf, a, b uint8) {
	var imm uint64
	if b != 0 {
		imm = uint64(value.True)
	} else {
		imm = uint64(value.False)
	}
	emitLOADKArm64(cb, a, imm)
}

// emitLOADNILArm64 emits R(A..B) := nil (inclusive).
func emitLOADNILArm64(cb *codeBuf, a, b uint8) {
	nilBits := uint64(value.Nil)
	for i := int32(a); i <= int32(b); i++ {
		emitLOADKArm64(cb, uint8(i), nilBits)
	}
}

// emitRetArm64 emits arm64 `ret`.
func emitRetArm64(cb *codeBuf) {
	cb.emit(jitarm64.EmitRet(nil))
}

// emitJMPArm64 emits `b rel26` with a placeholder + fixup. The rel26 is
// in units of 4 bytes and is patched by resolveLabels.
//
// arm64 `b imm26` opcode: 0x14000000 | (imm26 & 0x03ffffff). We emit
// zero placeholder; resolver patches. Since arm64 branches are relative
// to the instruction PC in units of 4, we need special handling in
// codeBuf.resolveLabels for arm64 -- OR we can encode the rel32 as a
// byte-level 32-bit displacement in the label patch and then post-
// process to arm64 rel26 shift.
//
// For simplicity: emit the placeholder as a full 32-bit encoded B with
// zero offset, and add a fixup with a marker that the resolveLabels
// path (arm64 build) shifts +2 bits. This is a TODO for the arm64
// bringup; for the first landing we emit b with zero and require the
// caller to handle patching by shifting rel32 >> 2 before writing.
//
// Simpler: use a bespoke arm64 label resolver that patches the low 26
// bits after shifting. Add here to keep contained.
func emitJMPArm64(cb *codeBuf, targetBB int) {
	patchOff := cb.pos()
	// Placeholder: b #0
	cb.emit([]byte{0x00, 0x00, 0x00, 0x14})
	cb.addFixup(patchOff, cb.pos(), targetBB)
	// The generic resolveLabels writes a 32-bit rel32 in bytes. For
	// arm64 we would need a different resolver. See resolveLabelsArm64
	// (TODO) for the arm64-specific patch. For this initial landing we
	// don't call resolveLabels for arm64 code; tests exercise linear
	// segments only.
}
