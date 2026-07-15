//go:build wangshu_p4 && linux && amd64

package amd64

import "testing"

// TestPJ4_EmitCmpRaxImm32_RoundTrip runs compare + jump on an mmap'd segment
// (the physical basis for the 06 §3.2 IsNumber guard).
//
// It constructs the sequence "rax = sentA / cmp rax, sentA / je .skip / mov rax,
// sentB / .skip: ret" (if cmp does not work or the jcc does not jump, RAX will
// be sentB rather than sentA).
//
// **PJ4 simplified-form boundary**: this test only verifies "cmp + jcc byte
// encoding works + the jcc genuinely jumps on the flag"; the full IsNumber guard
// template (NaN-box boundary + OSR exit path) lands in the same batch as the
// stack switch is enabled in PJ4+.
func TestPJ4_EmitCmpRaxImm32_Equal(t *testing.T) {
	// rax = 100; cmp rax, 100; je after_branch; mov rax, 0xbad; after_branch: ret
	// JE rel8: 74 ii (2 bytes; this test uses the 6-byte jae rel32 instead, since
	// rel8 jcc is not implemented yet; it actually verifies "rax >= 100 jae
	// after_branch", 100 >= 100 ⇒ jae skips the mov bad)
	const sent = 100
	const bad = uint64(0xbad)

	// Compute jae rel32: skip mov rax, 0xbad (11 bytes, EncodedMovRaxImm64Len)
	// jae rel32 is itself 6 bytes; rel32 is "the offset from the next instruction",
	// so the distance skipped = 11 bytes
	// (mov rax, bad takes 10 bytes + 0 bytes other = 10 bytes; but EncodedMovRaxImm64Len = 10)
	rel := int32(EncodedMovRaxImm64Len)

	var buf []byte
	buf = EmitMovRaxImm64(buf, sent) // 10 bytes — rax = 100
	buf = EmitCmpRaxImm32(buf, sent) // 7  bytes — cmp rax, 100
	buf = EmitJaeRel32(buf, rel)     // 6  bytes — jae +10 (if rax >= 100, skip mov bad)
	buf = EmitMovRaxImm64(buf, bad)  // 10 bytes — mov rax, 0xbad (skipped)
	buf = EmitRet(buf)               // 1  byte  — ret (rax = sent)

	page, err := MmapCode(buf)
	if err != nil {
		t.Fatalf("MmapCode failed: %v", err)
	}
	defer func() { _ = page.Munmap() }()

	got := CallJIT(page.Addr())
	if got != sent {
		t.Errorf("RAX = 0x%x, want 0x%x (jae should skip mov bad)", got, sent)
	}
}

// TestPJ4_EmitJmpRel32_RoundTrip uses an unconditional jmp to skip a dead-code segment.
func TestPJ4_EmitJmpRel32_RoundTrip(t *testing.T) {
	const good = uint64(0xfeedface)
	const bad = uint64(0xdeadbeef)

	// jmp +10 (skip mov rax, bad); mov rax, bad; mov rax, good; ret
	rel := int32(EncodedMovRaxImm64Len)

	var buf []byte
	buf = EmitJmpRel32(buf, rel)     // 5  bytes — jmp +10
	buf = EmitMovRaxImm64(buf, bad)  // 10 bytes — skipped
	buf = EmitMovRaxImm64(buf, good) // 10 bytes — rax = good
	buf = EmitRet(buf)

	page, err := MmapCode(buf)
	if err != nil {
		t.Fatalf("MmapCode failed: %v", err)
	}
	defer func() { _ = page.Munmap() }()

	got := CallJIT(page.Addr())
	if got != good {
		t.Errorf("RAX = 0x%x, want 0x%x (jmp should skip mov bad)", got, good)
	}
}

// TestPJ4_EncodedLengths verifies the encoded-length constants newly added in PJ4.
func TestPJ4_EncodedLengths(t *testing.T) {
	cases := []struct {
		name     string
		encode   func() []byte
		expected int
	}{
		{
			name:     "cmp rax, imm32",
			encode:   func() []byte { return EmitCmpRaxImm32(nil, 0) },
			expected: EncodedCmpRaxImm32Len,
		},
		{
			name:     "jmp rel32",
			encode:   func() []byte { return EmitJmpRel32(nil, 0) },
			expected: EncodedJmpRel32Len,
		},
		{
			name:     "jae rel32",
			encode:   func() []byte { return EmitJaeRel32(nil, 0) },
			expected: EncodedJaeRel32Len,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf := tc.encode()
			if len(buf) != tc.expected {
				t.Errorf("%s encoded %d bytes, want %d", tc.name, len(buf), tc.expected)
			}
		})
	}
}

// —— PJ4 table IC inline byte-level primitives (see emitter_pj4.go) ——

func TestPJ4_EmitMovqR14FromR15Disp(t *testing.T) {
	cases := []struct {
		disp int32
		want []byte
	}{
		{0, []byte{0x4D, 0x8B, 0xB7, 0, 0, 0, 0}},
		{8, []byte{0x4D, 0x8B, 0xB7, 8, 0, 0, 0}},
		{16, []byte{0x4D, 0x8B, 0xB7, 16, 0, 0, 0}},
		{-1, []byte{0x4D, 0x8B, 0xB7, 0xFF, 0xFF, 0xFF, 0xFF}},
	}
	for i, tc := range cases {
		got := EmitMovqR14FromR15Disp(nil, tc.disp)
		if len(got) != EncodedMovqR14FromR15DispLen {
			t.Errorf("case %d: len=%d, want %d", i, len(got), EncodedMovqR14FromR15DispLen)
		}
		if string(got) != string(tc.want) {
			t.Errorf("case %d disp=%d: got %x, want %x", i, tc.disp, got, tc.want)
		}
	}
}

func TestPJ4_EmitMovqRaxFromR14PlusRcxDisp(t *testing.T) {
	got := EmitMovqRaxFromR14PlusRcxDisp(nil, 40)
	want := []byte{0x49, 0x8B, 0x84, 0x0E, 40, 0, 0, 0}
	if string(got) != string(want) {
		t.Errorf("got %x, want %x", got, want)
	}
	if len(got) != EncodedMovqRaxFromR14PlusRcxDispLen {
		t.Errorf("len=%d, want %d", len(got), EncodedMovqRaxFromR14PlusRcxDispLen)
	}
}

func TestPJ4_EmitMovqRcxFromRax(t *testing.T) {
	got := EmitMovqRcxFromRax(nil)
	want := []byte{0x48, 0x89, 0xC1}
	if string(got) != string(want) {
		t.Errorf("got %x, want %x", got, want)
	}
}

func TestPJ4_EmitShrRcxImm8(t *testing.T) {
	got := EmitShrRcxImm8(nil, 32)
	want := []byte{0x48, 0xC1, 0xE9, 32}
	if string(got) != string(want) {
		t.Errorf("got %x, want %x", got, want)
	}
}

func TestPJ4_EmitCmpEcxImm32(t *testing.T) {
	got := EmitCmpEcxImm32(nil, 0x12345678)
	want := []byte{0x81, 0xF9, 0x78, 0x56, 0x34, 0x12}
	if string(got) != string(want) {
		t.Errorf("got %x, want %x", got, want)
	}
}

func TestPJ4_EmitAndRaxImm32(t *testing.T) {
	got := EmitAndRaxImm32(nil, -1)
	want := []byte{0x48, 0x81, 0xE0, 0xFF, 0xFF, 0xFF, 0xFF}
	if string(got) != string(want) {
		t.Errorf("got %x, want %x", got, want)
	}
}

// TestPJ4_EmitShrRaxImm8 —— shr rax, 48 (byte-level first step of the strict IsTable guard).
// Encoding: 48 C1 E8 30 (30 = 48 decimal). Intel SDM Vol.2B SHR /5.
func TestPJ4_EmitShrRaxImm8(t *testing.T) {
	got := EmitShrRaxImm8(nil, 48)
	want := []byte{0x48, 0xC1, 0xE8, 48}
	if string(got) != string(want) {
		t.Errorf("got %x, want %x", got, want)
	}
}

// TestPJ4_EmitCmpEaxImm32 —— cmp eax, 0xFFFC (byte-level second step of the strict
// IsTable guard, verifying the high 16-bit tag = TagTable 0xFFFC).
// Encoding: 3D FC FF 00 00 (short form, no ModRM, RAX/EAX implicit).
func TestPJ4_EmitCmpEaxImm32(t *testing.T) {
	got := EmitCmpEaxImm32(nil, 0xFFFC)
	want := []byte{0x3D, 0xFC, 0xFF, 0x00, 0x00}
	if string(got) != string(want) {
		t.Errorf("got %x, want %x", got, want)
	}
}

// TestPJ4_EmitMovRdxImm64 —— mov rdx, imm64 (NodeHit burns the stableKey).
// Encoding: 48 BA imm64_LE_bytes.
func TestPJ4_EmitMovRdxImm64(t *testing.T) {
	got := EmitMovRdxImm64(nil, 0xFFFB_DEAD_BEEF_CAFE) // simulate a string NaN-box
	want := []byte{0x48, 0xBA,
		0xFE, 0xCA, 0xEF, 0xBE, 0xAD, 0xDE, 0xFB, 0xFF}
	if string(got) != string(want) {
		t.Errorf("got %x, want %x", got, want)
	}
}

// TestPJ4_EmitCmpRaxRdx —— cmp rax, rdx (NodeHit verifies NodeKey == stableKey).
// Encoding: 48 39 D0 (REX.W / opcode CMP r/m64,r64 / ModRM mod=11 reg=010 rm=000).
func TestPJ4_EmitCmpRaxRdx(t *testing.T) {
	got := EmitCmpRaxRdx(nil)
	want := []byte{0x48, 0x39, 0xD0}
	if string(got) != string(want) {
		t.Errorf("got %x, want %x", got, want)
	}
}

// TestPJ4_EmitMovqMemR14PlusRcxFromRax —— reverse SIB store:
// `mov [r14 + rcx*1 + disp32], rax` (8 bytes, used by PJ4 SETTABLE IC inline).
// Encoding: 49 89 84 0E disp32 (LE).
// SIB 0E = scale=00 index=001(rcx) base=110(r14 w/ REX.B).
func TestPJ4_EmitMovqMemR14PlusRcxFromRax(t *testing.T) {
	got := EmitMovqMemR14PlusRcxFromRax(nil, 0x12345678)
	want := []byte{0x49, 0x89, 0x84, 0x0E, 0x78, 0x56, 0x34, 0x12}
	if string(got) != string(want) {
		t.Errorf("got %x, want %x", got, want)
	}

	// A small disp also uses disp32 (the template byte layout is self-consistent, keeping 8 bytes even when disp is small)
	got2 := EmitMovqMemR14PlusRcxFromRax(nil, 32)
	want2 := []byte{0x49, 0x89, 0x84, 0x0E, 0x20, 0x00, 0x00, 0x00}
	if string(got2) != string(want2) {
		t.Errorf("got %x, want %x", got2, want2)
	}
}

// TestPJ4_EmitMovqMemR14PlusRcxFromRdx —— reverse SIB store from rdx:
// `mov [r14 + rcx*1 + disp32], rdx` (8 bytes, used by PJ4 SETTABLE IC inline value write).
// Encoding: 49 89 94 0E disp32 (LE).
// ModRM 94 = mod=10 reg=010(rdx) rm=100(SIB) (compare with 84's reg=000=rax).
func TestPJ4_EmitMovqMemR14PlusRcxFromRdx(t *testing.T) {
	got := EmitMovqMemR14PlusRcxFromRdx(nil, 0x12345678)
	want := []byte{0x49, 0x89, 0x94, 0x0E, 0x78, 0x56, 0x34, 0x12}
	if string(got) != string(want) {
		t.Errorf("got %x, want %x", got, want)
	}
}

// TestPJ4_EmitMovqRdxFromMemRbx —— load rdx from [rbx+disp32] (value addressing).
// Encoding: 48 8B 93 disp32 (7 bytes, ModRM 93=mod=10 reg=010(rdx) rm=011(rbx)).
func TestPJ4_EmitMovqRdxFromMemRbx(t *testing.T) {
	got := EmitMovqRdxFromMemRbx(nil, 16) // R(2) = [rbx+16]
	want := []byte{0x48, 0x8B, 0x93, 0x10, 0x00, 0x00, 0x00}
	if string(got) != string(want) {
		t.Errorf("got %x, want %x", got, want)
	}
}
