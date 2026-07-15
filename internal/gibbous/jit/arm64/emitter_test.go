//go:build wangshu_p4 && arm64 && linux

package arm64

import (
	"encoding/binary"
	"testing"
)

// TestPJ8_EmitMovX0Imm64Encoding verifies that the arm64 movz+movk sequence is
// byte-encoded correctly.
//
// It does not actually execute the code (the linux/arm64 mmap+W^X infrastructure
// is already in place, but end-to-end execution needs the trampoline asm, wired
// up in the full PJ8+ version); this test only checks that "the byte encoding
// conforms to the ARM64 ISA" — using the official movz/movk encoding format as
// reference (`Arm Architecture Reference Manual`).
func TestPJ8_EmitMovX0Imm64Encoding(t *testing.T) {
	const imm = uint64(0xdead_beef_cafe_babe)

	var buf []byte
	buf = EmitMovX0Imm64(buf, imm)
	buf = EmitRet(buf)

	if len(buf) != EncodedMovX0Imm64Len+EncodedRetLen {
		t.Fatalf("encoded length = %d, want %d", len(buf), EncodedMovX0Imm64Len+EncodedRetLen)
	}

	// Parse each 32-bit instruction, verifying the movz/movk imm16 field
	expected := []uint16{0xbabe, 0xcafe, 0xbeef, 0xdead}
	for i, want := range expected {
		insn := binary.LittleEndian.Uint32(buf[i*4 : (i+1)*4])
		// movz/movk instructions: base 0xD2800000 (movz) / 0xF2800000 (movk),
		// imm16 in bits [20:5]
		gotImm16 := uint16((insn >> 5) & 0xFFFF)
		if gotImm16 != want {
			t.Errorf("insn %d imm16 = 0x%04x, want 0x%04x", i, gotImm16, want)
		}
	}

	// The 5th instruction should be ret (0xd65f03c0 LE)
	gotRet := binary.LittleEndian.Uint32(buf[16:20])
	if gotRet != 0xd65f03c0 {
		t.Errorf("ret encoding = 0x%08x, want 0xd65f03c0", gotRet)
	}
}

// TestPJ8_EmitMovXdImm64 covers the generic Xd register version (rd != 0).
func TestPJ8_EmitMovXdImm64(t *testing.T) {
	var buf []byte
	buf = EmitMovXdImm64(buf, 5, 0x12345678) // mov x5, ...

	if len(buf) != EncodedMovXdImm64Len {
		t.Fatalf("len = %d, want %d", len(buf), EncodedMovXdImm64Len)
	}
	// First instruction is movz x5, imm[15:0] = 0x5678; Rd field (low 5 bits) = 5
	insn0 := binary.LittleEndian.Uint32(buf[0:4])
	if insn0&0x1F != 5 {
		t.Errorf("Rd = %d, want 5", insn0&0x1F)
	}
	imm0 := uint16((insn0 >> 5) & 0xFFFF)
	if imm0 != 0x5678 {
		t.Errorf("imm[15:0] = 0x%04x, want 0x5678", imm0)
	}
}

// TestPJ8_EmitMovXdFromXn:mov Xd, Xn(reg-to-reg)= ORR Xd, XZR, Xn.
func TestPJ8_EmitMovXdFromXn(t *testing.T) {
	var buf []byte
	buf = EmitMovXdFromXn(buf, 3, 5) // mov x3, x5

	if len(buf) != EncodedMovXdFromXnLen {
		t.Fatalf("len = %d, want %d", len(buf), EncodedMovXdFromXnLen)
	}
	insn := binary.LittleEndian.Uint32(buf[0:4])
	// ORR base 0xAA000000, Rn=5 (bits 16-20), Rm/shift_reg=31 (XZR, bits 16-20 same as the Rn field)
	// Actual encoding: Rn=Rm=5 (when we emit, the Rn field holds Rm=5), Rm field (bits 16-20)
	// wait — our implementation: Rn=5 in bits 16-20, XZR (31) in bits 5-9, Rd=3 in bits 0-4
	if insn&0x1F != 3 {
		t.Errorf("Rd = %d, want 3", insn&0x1F)
	}
	if (insn>>5)&0x1F != 31 {
		t.Errorf("Rn(should be XZR=31) = %d", (insn>>5)&0x1F)
	}
	if (insn>>16)&0x1F != 5 {
		t.Errorf("Rm = %d, want 5", (insn>>16)&0x1F)
	}
}

// TestPJ8_EmitAddXdImm12:add Xd, Xn, #imm12.
func TestPJ8_EmitAddXdImm12(t *testing.T) {
	var buf []byte
	buf = EmitAddXdImm12(buf, 0, 0, 100) // add x0, x0, #100

	if len(buf) != EncodedAddXdImm12Len {
		t.Fatalf("len = %d, want %d", len(buf), EncodedAddXdImm12Len)
	}
	insn := binary.LittleEndian.Uint32(buf[0:4])
	// base 0x91000000, Rd=0, Rn=0, imm12=100
	if insn&0x1F != 0 {
		t.Errorf("Rd = %d, want 0", insn&0x1F)
	}
	if (insn>>10)&0xFFF != 100 {
		t.Errorf("imm12 = %d, want 100", (insn>>10)&0xFFF)
	}
}

// TestPJ8_EmitSubXdImm12:sub Xd, Xn, #imm12.
func TestPJ8_EmitSubXdImm12(t *testing.T) {
	var buf []byte
	buf = EmitSubXdImm12(buf, 1, 1, 1) // sub x1, x1, #1

	if len(buf) != EncodedSubXdImm12Len {
		t.Fatalf("len = %d, want %d", len(buf), EncodedSubXdImm12Len)
	}
	insn := binary.LittleEndian.Uint32(buf[0:4])
	// base 0xD1000000
	if (insn & 0xFFE00000) != 0xD1000000 {
		t.Errorf("opcode = 0x%08x, want base 0xD1000000", insn&0xFFE00000)
	}
}

// TestPJ8_EmitB:b imm26 unconditional branch.
func TestPJ8_EmitB(t *testing.T) {
	var buf []byte
	negImm := int32(-2)
	buf = EmitB(buf, negImm) // b backward 2 instructions

	if len(buf) != EncodedBLen {
		t.Fatalf("len = %d, want %d", len(buf), EncodedBLen)
	}
	insn := binary.LittleEndian.Uint32(buf[0:4])
	// base 0x14000000; imm26 = -2 in two's complement 26-bit = 0x3FFFFFE
	wantInsn := uint32(0x14000000) | (uint32(negImm) & 0x03FFFFFF)
	if insn != wantInsn {
		t.Errorf("b -2 = 0x%08x, want 0x%08x", insn, wantInsn)
	}
}

// TestPJ8_EmitLdrXtFromXnDisp verifies "ldr Xt, [Xn, #pimm12]" 64-bit load.
// Encoding: 0xF9400000 base + imm12<<10 (scaled by 8) + Rn<<5 + Rt.
//
// Use case: the arm64 side of PJ4 IC inline — load arena base / table words
// (mirroring amd64 `mov rax, [r14+rcx+disp]`, 8 bytes).
func TestPJ8_EmitLdrXtFromXnDisp(t *testing.T) {
	cases := []struct {
		name    string
		rt, rn  uint8
		byteOff uint16
		// expected insn (LE 32-bit)
	}{
		// ldr x0, [x1, #0]: 0xF9400020 (rt=0 rn=1 imm12=0)
		{"ldr x0, [x1, #0]", 0, 1, 0}, // imm12=0

		// ldr x2, [x3, #8]: imm12=1 → byte off 8
		{"ldr x2, [x3, #8]", 2, 3, 8},
		// ldr x5, [x6, #40]: imm12=5 → byte off 40 (table.word5 access)
		{"ldr x5, [x6, #40] (table.word5)", 5, 6, 40},
		// ldr x10, [x11, #32760]: imm12=4095 (max) → byte off 32760
		{"ldr x10, [x11, #max]", 10, 11, 32760},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf []byte
			buf = EmitLdrXtFromXnDisp(buf, tc.rt, tc.rn, tc.byteOff)
			if len(buf) != EncodedLdrXtFromXnDispLen {
				t.Fatalf("len = %d, want %d", len(buf), EncodedLdrXtFromXnDispLen)
			}
			insn := binary.LittleEndian.Uint32(buf[0:4])
			// verify each field
			gotImm12 := (insn >> 10) & 0xFFF
			gotRn := (insn >> 5) & 0x1F
			gotRt := insn & 0x1F
			gotBase := insn & 0xFFC003E0 // upper/middle fixed bits, excluding Rd/Rn
			wantImm12 := uint32(tc.byteOff / 8)
			if gotImm12 != wantImm12 {
				t.Errorf("imm12 = %d, want %d", gotImm12, wantImm12)
			}
			if uint8(gotRn) != tc.rn || uint8(gotRt) != tc.rt {
				t.Errorf("Rn/Rt = %d/%d, want %d/%d", gotRn, gotRt, tc.rn, tc.rt)
			}
			// upper + middle fixed base bits (ignoring the Rn/Rt/imm12 bits)
			// 0xF9400000 upper 22 bits = size+V+opc+L+fixed
			if (insn & 0xFFC00000) != 0xF9400000 {
				t.Errorf("base bits = 0x%08x, want 0xF940 prefix", insn&0xFFC00000)
			}
			_ = gotBase
		})
	}
}

// TestPJ8_EmitStrXtToXnDisp verifies "str Xt, [Xn, #pimm12]" 64-bit store.
// Encoding same as LDR but opc=00 → base 0xF9000000.
func TestPJ8_EmitStrXtToXnDisp(t *testing.T) {
	var buf []byte
	buf = EmitStrXtToXnDisp(buf, 5, 6, 56) // str x5, [x6, #56] (SET NodeVal slot)

	if len(buf) != EncodedStrXtToXnDispLen {
		t.Fatalf("len = %d, want %d", len(buf), EncodedStrXtToXnDispLen)
	}
	insn := binary.LittleEndian.Uint32(buf[0:4])
	if (insn & 0xFFC00000) != 0xF9000000 {
		t.Errorf("STR base bits = 0x%08x, want 0xF900 prefix", insn&0xFFC00000)
	}
	gotImm12 := (insn >> 10) & 0xFFF
	if gotImm12 != 7 { // 56/8 = 7
		t.Errorf("imm12 = %d, want 7", gotImm12)
	}
	if (insn>>5)&0x1F != 6 || insn&0x1F != 5 {
		t.Errorf("Rn/Rt fields wrong: 0x%08x", insn)
	}
}

// TestPJ8_EmitCmpXnXm verifies "cmp Xn, Xm" (actually SUBS XZR, Xn, Xm) at the byte level.
// Encoding: 0xEB00001F base + Xm<<16 + Xn<<5.
func TestPJ8_EmitCmpXnXm(t *testing.T) {
	var buf []byte
	buf = EmitCmpXnXm(buf, 1, 2) // cmp x1, x2 (SUBS XZR, X1, X2)

	if len(buf) != EncodedCmpXnXmLen {
		t.Fatalf("len = %d, want %d", len(buf), EncodedCmpXnXmLen)
	}
	insn := binary.LittleEndian.Uint32(buf[0:4])
	// verify base bits + Rm/Rn fields
	// 0xEB00001F + Rm=2 << 16 = 0xEB02001F + Rn=1 << 5 = 0xEB02003F
	wantInsn := uint32(0xEB00001F) | uint32(2)<<16 | uint32(1)<<5
	if insn != wantInsn {
		t.Errorf("cmp x1, x2 = 0x%08x, want 0x%08x", insn, wantInsn)
	}
	// Rd field = XZR (31)
	if insn&0x1F != 31 {
		t.Errorf("Rd = %d, want 31 (XZR)", insn&0x1F)
	}
}

// TestPJ8_EmitBCond verifies "b.cond label" conditional branch at the byte level.
// Encoding: 0x54000000 base + imm19<<5 + cond.
func TestPJ8_EmitBCond(t *testing.T) {
	cases := []struct {
		name     string
		cond     uint8
		imm19    int32
		condBits uint32
	}{
		{"b.eq +4", CondEQ, 1, 0x0}, // forward 1 word (4 bytes)
		{"b.ne +0", CondNE, 0, 0x1},
		{"b.lt +8", CondLT, 2, 0xB},
		{"b.ge -4", CondGE, -1, 0xA}, // backward 1 word
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf []byte
			buf = EmitBCond(buf, tc.cond, tc.imm19)
			if len(buf) != EncodedBCondLen {
				t.Fatalf("len = %d, want %d", len(buf), EncodedBCondLen)
			}
			insn := binary.LittleEndian.Uint32(buf[0:4])
			// base 0x54000000, upper 8 bits = 0x54
			if (insn & 0xFF000000) != 0x54000000 {
				t.Errorf("base bits = 0x%08x, want 0x54 prefix", insn&0xFF000000)
			}
			gotCond := insn & 0xF
			if gotCond != tc.condBits {
				t.Errorf("cond = 0x%x, want 0x%x", gotCond, tc.condBits)
			}
			gotImm19 := int32((insn >> 5) & 0x7FFFF)
			// sign-extend 19-bit to 32-bit
			if gotImm19&0x40000 != 0 { // bit 18 set → negative
				gotImm19 |= ^int32(0x7FFFF)
			}
			if gotImm19 != tc.imm19 {
				t.Errorf("imm19 = %d, want %d", gotImm19, tc.imm19)
			}
		})
	}
}

// TestPJ8_EmitFmovDdFromXn verifies "fmov Dd, Xn" (GP→FP) at the byte level.
// Encoding: 0x9E670000 base + Xn<<5 + Dd.
func TestPJ8_EmitFmovDdFromXn(t *testing.T) {
	var buf []byte
	buf = EmitFmovDdFromXn(buf, 3, 5) // fmov d3, x5

	if len(buf) != EncodedFmovDdFromXnLen {
		t.Fatalf("len = %d, want %d", len(buf), EncodedFmovDdFromXnLen)
	}
	insn := binary.LittleEndian.Uint32(buf[0:4])
	wantInsn := uint32(0x9E670000) | uint32(5)<<5 | uint32(3)
	if insn != wantInsn {
		t.Errorf("fmov d3, x5 = 0x%08x, want 0x%08x", insn, wantInsn)
	}
}

// TestPJ8_EmitFmovXdFromDn verifies "fmov Xd, Dn" (FP→GP) at the byte level.
// Encoding: 0x9E660000 base + Dn<<5 + Xd.
func TestPJ8_EmitFmovXdFromDn(t *testing.T) {
	var buf []byte
	buf = EmitFmovXdFromDn(buf, 7, 2) // fmov x7, d2

	if len(buf) != EncodedFmovXdFromDnLen {
		t.Fatalf("len = %d, want %d", len(buf), EncodedFmovXdFromDnLen)
	}
	insn := binary.LittleEndian.Uint32(buf[0:4])
	wantInsn := uint32(0x9E660000) | uint32(2)<<5 | uint32(7)
	if insn != wantInsn {
		t.Errorf("fmov x7, d2 = 0x%08x, want 0x%08x", insn, wantInsn)
	}
}

// TestPJ8_EmitFaddDdDnDm verifies "fadd Dd, Dn, Dm" (double-precision add) at the byte level.
// Encoding: 0x1E602800 base + Dm<<16 + Dn<<5 + Dd.
func TestPJ8_EmitFaddDdDnDm(t *testing.T) {
	var buf []byte
	buf = EmitFaddDdDnDm(buf, 0, 1, 2) // fadd d0, d1, d2

	if len(buf) != EncodedFaddDdDnDmLen {
		t.Fatalf("len = %d, want %d", len(buf), EncodedFaddDdDnDmLen)
	}
	insn := binary.LittleEndian.Uint32(buf[0:4])
	wantInsn := uint32(0x1E602800) | uint32(2)<<16 | uint32(1)<<5 | uint32(0)
	if insn != wantInsn {
		t.Errorf("fadd d0, d1, d2 = 0x%08x, want 0x%08x", insn, wantInsn)
	}
}

// TestPJ8_EmitFsubDdDnDm verifies "fsub Dd, Dn, Dm" at the byte level. base 0x1E603800.
func TestPJ8_EmitFsubDdDnDm(t *testing.T) {
	var buf []byte
	buf = EmitFsubDdDnDm(buf, 0, 1, 2)

	if len(buf) != EncodedFsubDdDnDmLen {
		t.Fatalf("len = %d, want %d", len(buf), EncodedFsubDdDnDmLen)
	}
	insn := binary.LittleEndian.Uint32(buf[0:4])
	wantInsn := uint32(0x1E603800) | uint32(2)<<16 | uint32(1)<<5
	if insn != wantInsn {
		t.Errorf("fsub d0, d1, d2 = 0x%08x, want 0x%08x", insn, wantInsn)
	}
}

// TestPJ8_EmitFmulDdDnDm verifies "fmul Dd, Dn, Dm" at the byte level. base 0x1E600800.
func TestPJ8_EmitFmulDdDnDm(t *testing.T) {
	var buf []byte
	buf = EmitFmulDdDnDm(buf, 0, 1, 2)

	if len(buf) != EncodedFmulDdDnDmLen {
		t.Fatalf("len = %d, want %d", len(buf), EncodedFmulDdDnDmLen)
	}
	insn := binary.LittleEndian.Uint32(buf[0:4])
	wantInsn := uint32(0x1E600800) | uint32(2)<<16 | uint32(1)<<5
	if insn != wantInsn {
		t.Errorf("fmul d0, d1, d2 = 0x%08x, want 0x%08x", insn, wantInsn)
	}
}

// TestPJ8_EmitFdivDdDnDm verifies "fdiv Dd, Dn, Dm" at the byte level. base 0x1E601800.
func TestPJ8_EmitFdivDdDnDm(t *testing.T) {
	var buf []byte
	buf = EmitFdivDdDnDm(buf, 0, 1, 2)

	if len(buf) != EncodedFdivDdDnDmLen {
		t.Fatalf("len = %d, want %d", len(buf), EncodedFdivDdDnDmLen)
	}
	insn := binary.LittleEndian.Uint32(buf[0:4])
	wantInsn := uint32(0x1E601800) | uint32(2)<<16 | uint32(1)<<5
	if insn != wantInsn {
		t.Errorf("fdiv d0, d1, d2 = 0x%08x, want 0x%08x", insn, wantInsn)
	}
}

// TestPJ8_EmitFcmpeDnDm verifies "fcmpe Dn, Dm" (signaling ordered compare) at the byte level.
// Encoding: 0x1E602010 base + Dm<<16 + Dn<<5.
// Mirrors amd64 ucomisd xmm0, xmm1 (F2 0F 2E C0+reg, 4 bytes).
func TestPJ8_EmitFcmpeDnDm(t *testing.T) {
	var buf []byte
	buf = EmitFcmpeDnDm(buf, 1, 2) // fcmpe d1, d2

	if len(buf) != EncodedFcmpeDnDmLen {
		t.Fatalf("len = %d, want %d", len(buf), EncodedFcmpeDnDmLen)
	}
	insn := binary.LittleEndian.Uint32(buf[0:4])
	wantInsn := uint32(0x1E602010) | uint32(2)<<16 | uint32(1)<<5
	if insn != wantInsn {
		t.Errorf("fcmpe d1, d2 = 0x%08x, want 0x%08x", insn, wantInsn)
	}
}

// TestPJ8_EmitAddXdXnXm verifies "add Xd, Xn, Xm" (shifted register, shift=00) at the byte level.
// Encoding: 0x8B000000 base + Rm<<16 + Rn<<5 + Rd.
func TestPJ8_EmitAddXdXnXm(t *testing.T) {
	var buf []byte
	buf = EmitAddXdXnXm(buf, 2, 14, 1) // add x2, x14, x1

	if len(buf) != EncodedAddXdXnXmLen {
		t.Fatalf("len = %d, want %d", len(buf), EncodedAddXdXnXmLen)
	}
	insn := binary.LittleEndian.Uint32(buf[0:4])
	wantInsn := uint32(0x8B000000) | uint32(1)<<16 | uint32(14)<<5 | uint32(2)
	if insn != wantInsn {
		t.Errorf("add x2, x14, x1 = 0x%08x, want 0x%08x", insn, wantInsn)
	}
}

// TestPJ8_EmitAndXdXnXm verifies "and Xd, Xn, Xm" at the byte level.
// Encoding: 0x8A000000 base + Rm<<16 + Rn<<5 + Rd.
func TestPJ8_EmitAndXdXnXm(t *testing.T) {
	var buf []byte
	buf = EmitAndXdXnXm(buf, 0, 0, 1) // and x0, x0, x1

	if len(buf) != EncodedAndXdXnXmLen {
		t.Fatalf("len = %d, want %d", len(buf), EncodedAndXdXnXmLen)
	}
	insn := binary.LittleEndian.Uint32(buf[0:4])
	wantInsn := uint32(0x8A000000) | uint32(1)<<16
	if insn != wantInsn {
		t.Errorf("and x0, x0, x1 = 0x%08x, want 0x%08x", insn, wantInsn)
	}
}

// TestPJ8_EmitLsrXdImm6 verifies "lsr Xd, Xn, #imm6" at the byte level (UBFM alias).
// Encoding: 0xD340FC00 base + immr=imm6<<16 + Rn<<5 + Rd.
func TestPJ8_EmitLsrXdImm6(t *testing.T) {
	cases := []struct {
		name string
		imm6 uint8
	}{
		{"lsr x0, x0, #48 (IsTable shift)", 48},
		{"lsr x0, x0, #32 (gen shift)", 32},
		{"lsr x1, x2, #0 (no-op edge)", 0},
		{"lsr x3, x4, #63 (max)", 63},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf []byte
			rd := uint8(0)
			rn := uint8(0)
			if tc.name == "lsr x1, x2, #0 (no-op edge)" {
				rd = 1
				rn = 2
			} else if tc.name == "lsr x3, x4, #63 (max)" {
				rd = 3
				rn = 4
			}
			buf = EmitLsrXdImm6(buf, rd, rn, tc.imm6)

			if len(buf) != EncodedLsrXdImm6Len {
				t.Fatalf("len = %d, want %d", len(buf), EncodedLsrXdImm6Len)
			}
			insn := binary.LittleEndian.Uint32(buf[0:4])
			wantInsn := uint32(0xD340FC00) | uint32(tc.imm6)<<16 | uint32(rn)<<5 | uint32(rd)
			if insn != wantInsn {
				t.Errorf("%s = 0x%08x, want 0x%08x", tc.name, insn, wantInsn)
			}
		})
	}
}

// TestPJ8_EmitLdrbWtFromXnDisp verifies "ldrb Wt, [Xn, #pimm12]" at the byte level
// (32-bit zero-extended byte load, byte-scaled offset).
func TestPJ8_EmitLdrbWtFromXnDisp(t *testing.T) {
	cases := []struct {
		name    string
		rt, rn  uint8
		byteOff uint16
	}{
		{"ldrb w0, [x27, #16]", 0, 27, 16},
		{"ldrb w0, [x27, #0]", 0, 27, 0},
		{"ldrb w1, [x14, #4095]", 1, 14, 4095},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf []byte
			buf = EmitLdrbWtFromXnDisp(buf, tc.rt, tc.rn, tc.byteOff)
			if len(buf) != EncodedLdrbWtFromXnDispLen {
				t.Fatalf("len = %d, want %d", len(buf), EncodedLdrbWtFromXnDispLen)
			}
			insn := binary.LittleEndian.Uint32(buf[0:4])
			want := uint32(0x39400000) | uint32(tc.byteOff)<<10 |
				uint32(tc.rn)<<5 | uint32(tc.rt)
			if insn != want {
				t.Errorf("%s = 0x%08x, want 0x%08x", tc.name, insn, want)
			}
		})
	}
}

// TestPJ8_EmitCbnzW verifies "cbnz Wt, label" at the byte level (32-bit compare-branch
// nonzero). imm19 is a sign-extended word offset, target = PC + imm19 * 4.
func TestPJ8_EmitCbnzW(t *testing.T) {
	cases := []struct {
		name  string
		rt    uint8
		imm19 int32
	}{
		{"cbnz w0, +20 (5 words forward)", 0, 5},
		{"cbnz w1, 0 (placeholder)", 1, 0},
		{"cbnz w2, -16 (-4 words backward)", 2, -4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf []byte
			buf = EmitCbnzW(buf, tc.rt, tc.imm19)
			if len(buf) != EncodedCbnzWLen {
				t.Fatalf("len = %d, want %d", len(buf), EncodedCbnzWLen)
			}
			insn := binary.LittleEndian.Uint32(buf[0:4])
			wantBase := uint32(0x35000000)
			if (insn & 0xFF000000) != wantBase {
				t.Errorf("%s base = 0x%08x, want prefix 0x35", tc.name, insn&0xFF000000)
			}
			gotRt := insn & 0x1F
			if gotRt != uint32(tc.rt) {
				t.Errorf("%s Rt = %d, want %d", tc.name, gotRt, tc.rt)
			}
			gotImm19 := (insn >> 5) & 0x7FFFF
			wantImm19 := uint32(tc.imm19) & 0x7FFFF
			if gotImm19 != wantImm19 {
				t.Errorf("%s imm19 = 0x%05x, want 0x%05x", tc.name, gotImm19, wantImm19)
			}
		})
	}
}

// TestPJ8_EmitBlrXn verifies "blr Xn" at the byte level: base 0xD63F0000 + Rn<<5.
func TestPJ8_EmitBlrXn(t *testing.T) {
	cases := []struct {
		name string
		rn   uint8
	}{
		{"blr x0", 0},
		{"blr x16", 16},
		{"blr x17", 17},
		{"blr x30 (LR)", 30},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf []byte
			buf = EmitBlrXn(buf, tc.rn)
			if len(buf) != EncodedBlrXnLen {
				t.Fatalf("len = %d, want %d", len(buf), EncodedBlrXnLen)
			}
			insn := binary.LittleEndian.Uint32(buf[0:4])
			want := uint32(0xD63F0000) | uint32(tc.rn)<<5
			if insn != want {
				t.Errorf("%s = 0x%08x, want 0x%08x", tc.name, insn, want)
			}
		})
	}
}

// TestPJ8_EmitHelperCallArm64_Length verifies the helper call macro totals 20 bytes
// (mov X16 imm64 16 + blr X16 4).
func TestPJ8_EmitHelperCallArm64_Length(t *testing.T) {
	var buf []byte
	buf = EmitHelperCallArm64(buf, 0xDEAD_BEEF_CAFE_BABE)
	if len(buf) != EncodedHelperCallArm64Len {
		t.Errorf("len = %d, want %d", len(buf), EncodedHelperCallArm64Len)
	}
	if EncodedHelperCallArm64Len != 20 {
		t.Errorf("EncodedHelperCallArm64Len = %d, want 20", EncodedHelperCallArm64Len)
	}
}

// TestPJ8_EmitHelperCallArm64_ByteLayout verifies the byte layout:
//   - [0-15]  MOV X16, helperAddr imm64 (movz+movk×3, Rd=16)
//   - [16-19] BLR X16 (0xD63F0000 + Rn=16<<5)
func TestPJ8_EmitHelperCallArm64_ByteLayout(t *testing.T) {
	const helperAddr uint64 = 0xDEAD_BEEF_CAFE_BABE
	var buf []byte
	buf = EmitHelperCallArm64(buf, helperAddr)

	if len(buf) < 20 {
		t.Fatalf("buf too short: %d", len(buf))
	}

	// MOV X16 imm64: movz + 3×movk, verify each imm16 field + Rd=16
	expectedImm16 := [4]uint16{
		uint16(helperAddr & 0xFFFF),
		uint16((helperAddr >> 16) & 0xFFFF),
		uint16((helperAddr >> 32) & 0xFFFF),
		uint16((helperAddr >> 48) & 0xFFFF),
	}
	for i, exp := range expectedImm16 {
		insn := binary.LittleEndian.Uint32(buf[i*4 : (i+1)*4])
		got := uint16((insn >> 5) & 0xFFFF)
		if got != exp {
			t.Errorf("MOV X16 imm64 movz/movk[%d] imm16 = 0x%04x, want 0x%04x",
				i, got, exp)
		}
		if (insn & 0x1F) != 16 {
			t.Errorf("MOV X16 imm64 movz/movk[%d] Rd = %d, want 16",
				i, insn&0x1F)
		}
	}

	// [16-19] BLR X16
	insn := binary.LittleEndian.Uint32(buf[16:20])
	want := uint32(0xD63F0000) | uint32(16)<<5
	if insn != want {
		t.Errorf("[16] BLR X16 = 0x%08x, want 0x%08x", insn, want)
	}
}
