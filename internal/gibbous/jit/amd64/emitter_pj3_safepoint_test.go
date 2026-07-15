//go:build wangshu_p4 && linux && amd64

package amd64

import (
	"testing"
)

// emitter_pj3_safepoint_test.go — PJ3 safepoint check byte-level encoding +
// byte-layout verification (follows docs/design/p4-method-jit/05-system-pipeline.md
// §1.2.2 + §6.3 back-edge checkpoint).
//
// Byte-level tests only — really running the safepoint check in mmap+RX requires
// jitContext wiring + r15 loading; that engineering is left to the PJ3 wiring.

// TestPJ3_EmitCmpByteR15DispImm8_Encoding byte-level verification of "cmp byte
// [r15+disp], 0" encoding: 41 80 BF disp32 imm8.
func TestPJ3_EmitCmpByteR15DispImm8_Encoding(t *testing.T) {
	cases := []struct {
		disp int32
		imm  byte
		want []byte
	}{
		{0, 0, []byte{0x41, 0x80, 0xBF, 0x00, 0x00, 0x00, 0x00, 0x00}},
		{0x10, 0, []byte{0x41, 0x80, 0xBF, 0x10, 0x00, 0x00, 0x00, 0x00}},
		{0x10, 1, []byte{0x41, 0x80, 0xBF, 0x10, 0x00, 0x00, 0x00, 0x01}},
		{0x100, 0, []byte{0x41, 0x80, 0xBF, 0x00, 0x01, 0x00, 0x00, 0x00}},
		// negative disp32
		{-1, 0, []byte{0x41, 0x80, 0xBF, 0xFF, 0xFF, 0xFF, 0xFF, 0x00}},
	}
	for i, tc := range cases {
		got := EmitCmpByteR15DispImm8(nil, tc.disp, tc.imm)
		if len(got) != EncodedCmpByteR15DispImm8Len {
			t.Errorf("case %d: len=%d, want %d", i, len(got), EncodedCmpByteR15DispImm8Len)
		}
		if string(got) != string(tc.want) {
			t.Errorf("case %d: got %x, want %x", i, got, tc.want)
		}
	}
}

// TestPJ3_PatchRel32 byte-level verification of the forward jmp fixup tool.
func TestPJ3_PatchRel32(t *testing.T) {
	// Simulate: emit a jmp rel32 placeholder 0, then patch in the real rel32
	var buf []byte
	buf = EmitJmpRel32(buf, 0) // placeholder 5 bytes: E9 00 00 00 00

	// rel32 start = jmp start + 1 (skip E9 opcode)
	PatchRel32(buf, 1, 0x12345678)

	want := []byte{0xE9, 0x78, 0x56, 0x34, 0x12}
	if string(buf) != string(want) {
		t.Errorf("after patch: got %x, want %x", buf, want)
	}

	// negative rel32 patch
	var buf2 []byte
	buf2 = EmitJmpRel32(buf2, 0)
	PatchRel32(buf2, 1, -1)
	if string(buf2[1:]) != string([]byte{0xFF, 0xFF, 0xFF, 0xFF}) {
		t.Errorf("negative rel32 patch: got %x, want FF FF FF FF", buf2[1:])
	}
}

// TestPJ3_ForwardFixup_RoundTrip simulates the PJ3 wiring path: emit jcc
// placeholder rel32=0 → emit body → after knowing the body length, patch in rel32.
func TestPJ3_ForwardFixup_RoundTrip(t *testing.T) {
	var buf []byte
	// 1. emit jne placeholder rel32=0 (6 bytes)
	buf = EmitJneRel32(buf, 0)
	jccRel32Off := len(buf) - 4 // rel32 start

	// 2. emit body (pretend 13 bytes, actually arbitrary)
	body := []byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88}
	buf = append(buf, body...)

	// 3. patch: rel32 = body length (skip the body to the target)
	PatchRel32(buf, jccRel32Off, int32(len(body)))

	// Verify the jcc rel32 field after patch
	gotRel32 := int32(buf[jccRel32Off]) |
		int32(buf[jccRel32Off+1])<<8 |
		int32(buf[jccRel32Off+2])<<16 |
		int32(buf[jccRel32Off+3])<<24
	if gotRel32 != int32(len(body)) {
		t.Errorf("patched rel32 = %d, want %d", gotRel32, len(body))
	}
}

// TestPJ3_SafepointCheck_Encoding composite byte sequence: cmp + jne (the real
// form of the safepoint check). Verifies the combined byte layout, used later
// when PJ3 wiring inlines FORLOOP at the byte level.
func TestPJ3_SafepointCheck_Encoding(t *testing.T) {
	// Form: cmp byte [r15+0x10], 0; jne rel32_to_exit_stub (rel32=0x100)
	var buf []byte
	buf = EmitCmpByteR15DispImm8(buf, 0x10, 0)
	buf = EmitJneRel32(buf, 0x100)

	wantLen := EncodedCmpByteR15DispImm8Len + EncodedJccRel32Len
	if len(buf) != wantLen {
		t.Fatalf("safepoint check len=%d, want %d", len(buf), wantLen)
	}

	// first 8 bytes = cmp byte [r15+0x10], 0
	wantCmp := []byte{0x41, 0x80, 0xBF, 0x10, 0x00, 0x00, 0x00, 0x00}
	if string(buf[:8]) != string(wantCmp) {
		t.Errorf("cmp byte = %x, want %x", buf[:8], wantCmp)
	}
	// last 6 bytes = jne rel32 (0F 85 rel32)
	wantJne := []byte{0x0F, 0x85, 0x00, 0x01, 0x00, 0x00}
	if string(buf[8:]) != string(wantJne) {
		t.Errorf("jne = %x, want %x", buf[8:], wantJne)
	}
}
