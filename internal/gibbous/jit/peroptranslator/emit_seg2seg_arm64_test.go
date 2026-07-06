//go:build wangshu_p4 && arm64

package peroptranslator

import (
	"testing"

	jitarm64 "github.com/Liam0205/wangshu/internal/gibbous/jit/arm64"
)

// word reads the little-endian arm64 instruction word at byte offset i.
func word(b []byte, i int) uint32 {
	return uint32(b[i]) | uint32(b[i+1])<<8 | uint32(b[i+2])<<16 | uint32(b[i+3])<<24
}

// TestArm64Seg2SegRawEncodings guards the hand-encoded SP-relative and
// branch-patch helpers (issue #50 Spike 5 arm64 port). These bypass the
// shared emitters (which clamp Rn>30 to 0, so they can't address SP), so
// their exact encodings must be pinned. Runs on real arm64 via CI.
func TestArm64Seg2SegRawEncodings(t *testing.T) {
	// str x30, [sp, #0]  = 0xF90003FE
	// str x26, [sp, #8]  = 0xF90007FA
	// ldr x30, [sp, #0]  = 0xF94003FE
	// ldr x26, [sp, #8]  = 0xF94007FA
	// sub sp, sp, #32    = 0xD10083FF
	// add sp, sp, #32    = 0x910083FF
	cases := []struct {
		name string
		emit func(cb *codeBuf)
		want uint32
	}{
		{"str x30,[sp,#0]", func(cb *codeBuf) { a64StrXSp(cb, regX30, 0) }, 0xF90003FE},
		{"str x26,[sp,#8]", func(cb *codeBuf) { a64StrXSp(cb, regX26, 8) }, 0xF90007FA},
		{"str x15,[sp,#16]", func(cb *codeBuf) { a64StrXSp(cb, 15, 16) }, 0xF9000BEF},
		{"ldr x30,[sp,#0]", func(cb *codeBuf) { a64LdrXSp(cb, regX30, 0) }, 0xF94003FE},
		{"ldr x26,[sp,#8]", func(cb *codeBuf) { a64LdrXSp(cb, regX26, 8) }, 0xF94007FA},
		{"sub sp,sp,#32", func(cb *codeBuf) { a64SubSp(cb, 32) }, 0xD10083FF},
		{"add sp,sp,#32", func(cb *codeBuf) { a64AddSp(cb, 32) }, 0x910083FF},
	}
	for _, tc := range cases {
		cb := newCodeBuf(1)
		tc.emit(cb)
		if len(cb.bytes) != 4 {
			t.Fatalf("%s: emitted %d bytes, want 4", tc.name, len(cb.bytes))
		}
		if got := word(cb.bytes, 0); got != tc.want {
			t.Errorf("%s: got %#08x, want %#08x", tc.name, got, tc.want)
		}
	}
}

// TestArm64PatchRel19 checks that a64PatchRel19 writes a correct signed
// word offset into a B.cond / CBZ imm19 field (bits 5..23) without
// disturbing the opcode / cond / Rt bits.
func TestArm64PatchRel19(t *testing.T) {
	cb := newCodeBuf(1)
	// cbz x14, <placeholder>  at offset 0; target 5 instructions ahead.
	insnOff := len(cb.bytes)
	cb.emit(jitarm64.EmitCbzX(nil, 14, 0))
	for i := 0; i < 4; i++ {
		cb.emit([]byte{0x1F, 0x20, 0x03, 0xD5}) // nop
	}
	targetOff := len(cb.bytes) // 5 words after insnOff
	a64PatchRel19(cb, insnOff, targetOff)
	insn := word(cb.bytes, insnOff)
	if imm19 := (insn >> 5) & 0x7FFFF; imm19 != 5 {
		t.Errorf("forward imm19 = %d, want 5", imm19)
	}
	// Rt (bits 0..4) preserved = 14.
	if insn&0x1F != 14 {
		t.Errorf("Rt clobbered: %d, want 14", insn&0x1F)
	}
}

// TestArm64PatchRel26 checks a64PatchRel26 for the unconditional B.
func TestArm64PatchRel26(t *testing.T) {
	cb := newCodeBuf(1)
	insnOff := len(cb.bytes)
	cb.emit([]byte{0x00, 0x00, 0x00, 0x14}) // b #0
	for i := 0; i < 3; i++ {
		cb.emit([]byte{0x1F, 0x20, 0x03, 0xD5})
	}
	targetOff := len(cb.bytes) // 4 words ahead
	a64PatchRel26(cb, insnOff, targetOff)
	insn := word(cb.bytes, insnOff)
	if imm26 := insn & 0x03FFFFFF; imm26 != 4 {
		t.Errorf("imm26 = %d, want 4", imm26)
	}
	if insn>>26 != 0x05 { // top 6 bits of B = 000101
		t.Errorf("B opcode clobbered: top6 = %#x", insn>>26)
	}
}
