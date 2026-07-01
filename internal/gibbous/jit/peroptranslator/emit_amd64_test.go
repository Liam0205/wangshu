//go:build wangshu_p4 && amd64

package peroptranslator

import (
	"bytes"
	"testing"

	"github.com/Liam0205/wangshu/internal/value"
)

// TestPJ10Native_Emit_MOVE_Bytes: MOVE A=1 B=2 must emit
//
//	48 8B 43 10       ; mov rax, [rbx+16]     ; R(2)*8 = 16
//	48 89 43 08       ; mov [rbx+8], rax      ; R(1)*8 = 8
//
// (Total 14 bytes; mod=01 form + disp8, but our emitter uses disp32 →
//
//	actually 7+7 = 14 bytes with disp32.)
func TestPJ10Native_Emit_MOVE_Bytes(t *testing.T) {
	cb := newCodeBuf(1)
	cb.bindLabel(0)
	emitMOVE(cb, 1 /*A*/, 2 /*B*/)

	// Expected bytes: EmitMovqRaxFromMemReg(rbx, 16) is
	//   48 8B 83 10 00 00 00      (7 bytes)
	// EmitMovqMemRegFromRax(rbx, 8) is
	//   48 89 83 08 00 00 00      (7 bytes)
	want := []byte{
		0x48, 0x8B, 0x83, 0x10, 0x00, 0x00, 0x00,
		0x48, 0x89, 0x83, 0x08, 0x00, 0x00, 0x00,
	}
	if !bytes.Equal(cb.bytes, want) {
		t.Errorf("MOVE bytes:\n got  %x\n want %x", cb.bytes, want)
	}
}

// TestPJ10Native_Emit_LOADK_Bytes: LOADK A=0 with imm = value.False must
// emit `mov rax, imm64; mov [rbx+0], rax` = 10 + 7 = 17 bytes.
func TestPJ10Native_Emit_LOADK_Bytes(t *testing.T) {
	cb := newCodeBuf(1)
	cb.bindLabel(0)
	imm := uint64(value.False)
	emitLOADK(cb, 0 /*A*/, imm)

	if len(cb.bytes) != 17 {
		t.Errorf("LOADK byte count = %d, want 17", len(cb.bytes))
	}
	// mov rax, imm64: 48 B8 <imm64-LE>
	if cb.bytes[0] != 0x48 || cb.bytes[1] != 0xB8 {
		t.Errorf("LOADK prefix = %x %x, want 48 B8", cb.bytes[0], cb.bytes[1])
	}
	// Check imm64 payload matches value.False (little-endian).
	var got uint64
	for i := 0; i < 8; i++ {
		got |= uint64(cb.bytes[2+i]) << (8 * i)
	}
	if got != imm {
		t.Errorf("LOADK imm payload = %x, want %x", got, imm)
	}
	// mov [rbx+0], rax: 48 89 83 00 00 00 00
	want := []byte{0x48, 0x89, 0x83, 0x00, 0x00, 0x00, 0x00}
	if !bytes.Equal(cb.bytes[10:], want) {
		t.Errorf("LOADK store-suffix = %x, want %x", cb.bytes[10:], want)
	}
}

// TestPJ10Native_Emit_LOADNIL_Bytes: LOADNIL A=1 B=3 must emit three
// stores of value.Nil to R(1), R(2), R(3) = 3 * 17 = 51 bytes.
func TestPJ10Native_Emit_LOADNIL_Bytes(t *testing.T) {
	cb := newCodeBuf(1)
	cb.bindLabel(0)
	emitLOADNIL(cb, 1, 3)

	if len(cb.bytes) != 51 {
		t.Errorf("LOADNIL byte count = %d, want 51 (3 stores * 17)", len(cb.bytes))
	}
	// Each store starts with `48 B8` + 8 bytes of value.Nil.
	nilBits := uint64(value.Nil)
	for i := 0; i < 3; i++ {
		off := i * 17
		if cb.bytes[off] != 0x48 || cb.bytes[off+1] != 0xB8 {
			t.Errorf("store %d prefix = %x %x", i, cb.bytes[off], cb.bytes[off+1])
		}
		var got uint64
		for j := 0; j < 8; j++ {
			got |= uint64(cb.bytes[off+2+j]) << (8 * j)
		}
		if got != nilBits {
			t.Errorf("store %d imm = %x, want %x", i, got, nilBits)
		}
	}
}

// TestPJ10Native_Emit_JMP_ResolvesForward: emitJMP records a fixup that
// resolveLabels patches to the actual displacement.
func TestPJ10Native_Emit_JMP_ResolvesForward(t *testing.T) {
	cb := newCodeBuf(2)
	cb.bindLabel(0)
	emitJMP(cb, 1)        // jmp to BB1, currently unbound
	cb.emit([]byte{0x90}) // one nop between jmp end and BB1 target
	cb.bindLabel(1)

	if err := cb.resolveLabels(); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	// jmp is 5 bytes, jmp_end = 5, target BB1 = 6, rel32 = 1
	// Read little-endian int32 from bytes 1..5
	var rel int32
	for i := 0; i < 4; i++ {
		rel |= int32(cb.bytes[1+i]) << (8 * i)
	}
	if rel != 1 {
		t.Errorf("JMP rel32 = %d, want 1", rel)
	}
}
