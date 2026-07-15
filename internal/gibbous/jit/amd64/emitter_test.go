//go:build wangshu_p4 && linux && amd64

package amd64

import "testing"

// TestPJ1_Emitter_MovRaxRet is an end-to-end check: the Emitter emits the
// "mov rax, imm; ret" sequence -> MmapCode flips it to executable -> CallJIT
// reads back imm. Mirrors spike/p4tramp::TestSpike_S1, but this test lives in
// the main tree internal/gibbous/jit/amd64, so it means "spike gate passed ->
// main-tree emitter works".
//
// This is the minimal check for the PJ1 acceptance criteria (00 §4 PJ1 row +
// 06 §6.1): a straight-line LOADK + RETURN Proto compiled by the P4 amd64
// backend -> mmap segment -> callJIT -> byte-equal with the crescent
// interpreter result (this test only covers the first half: "imm goes through
// emit -> execute -> return and the pipeline works").
func TestPJ1_Emitter_MovRaxRet(t *testing.T) {
	cases := []uint64{
		0,
		1,
		0xdeadbeef,
		0xcafebabedeadbeef,
		^uint64(0),
	}
	for _, imm := range cases {
		t.Run("", func(t *testing.T) {
			// emit: [mov rax, imm; ret]
			var buf []byte
			buf = EmitMovRaxImm64(buf, imm)
			buf = EmitRet(buf)
			if len(buf) != EncodedMovRaxImm64Len+EncodedRetLen {
				t.Fatalf("encoded length should be %d bytes, got %d",
					EncodedMovRaxImm64Len+EncodedRetLen, len(buf))
			}

			// W^X flip + mmap
			page, err := MmapCode(buf)
			if err != nil {
				t.Fatalf("MmapCode failed: %v", err)
			}
			defer func() {
				if err := page.Munmap(); err != nil {
					t.Errorf("Munmap failed: %v", err)
				}
			}()

			// trampoline: Go → mmap → ret
			got := CallJIT(page.Addr())
			if got != imm {
				t.Errorf("CallJIT returned 0x%x, expected 0x%x", got, imm)
			}
		})
	}
}

// TestPJ1_RepeatedCalls checks that repeated calls to the same segment return a
// stable value (follows the same shape as spike S2, mirrored in the main tree).
func TestPJ1_RepeatedCalls(t *testing.T) {
	imm := uint64(0xfeedface00c0ffee)
	var buf []byte
	buf = EmitMovRaxImm64(buf, imm)
	buf = EmitRet(buf)

	page, err := MmapCode(buf)
	if err != nil {
		t.Fatalf("MmapCode failed: %v", err)
	}
	defer func() { _ = page.Munmap() }()

	const N = 1000
	for i := 0; i < N; i++ {
		got := CallJIT(page.Addr())
		if got != imm {
			t.Fatalf("call #%d: got 0x%x, want 0x%x", i, got, imm)
		}
	}
}

// BenchmarkPJ1_CallJIT is the single-CALL cost baseline (main-tree version,
// mirrors the spike Bench's measured 1.95ns/op).
//
// This bench is the starting point of the PJ1 performance baseline; as PJ2-PJ5
// progressively extend the trampoline (switching SP + callee-saved regs, etc.),
// this number will drift upward -- the cost gap between "the simplified PJ1
// shape vs the full trampoline" is exactly the evidence behind the PJ1+
// engineering trade-offs.
func BenchmarkPJ1_CallJIT(b *testing.B) {
	imm := uint64(0xdeadbeef)
	var buf []byte
	buf = EmitMovRaxImm64(buf, imm)
	buf = EmitRet(buf)

	page, err := MmapCode(buf)
	if err != nil {
		b.Fatalf("MmapCode failed: %v", err)
	}
	defer func() { _ = page.Munmap() }()

	addr := page.Addr()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		got := CallJIT(addr)
		if got != imm {
			b.Fatalf("call #%d: got 0x%x, want 0x%x", i, got, imm)
		}
	}
}

// TestPJ2_SSE_Encoding verifies that PJ2's byte-level arithmetic SSE
// instruction encodings conform to the Intel x86-64 ISA.
//
// It does not actually execute (full mmap+RX execution needs jitContext to
// switch SP + register-allocation codegen, deferred to the full PJ2-PJ5
// version); it only asserts that the byte encodings match the ISA docs.
func TestPJ2_SSE_Encoding(t *testing.T) {
	// MOVSD xmm0, [rax+0]: F2 0F 10 00 + disp32=0 (4 bytes) = 8 bytes
	t.Run("MovsdXmmFromMem_xmm0_rax_0", func(t *testing.T) {
		var buf []byte
		buf = EmitMovsdXmmFromMem(buf, 0, 0, 0)
		want := []byte{0xF2, 0x0F, 0x10, 0x80, 0x00, 0x00, 0x00, 0x00}
		if !bytesEqual(buf, want) {
			t.Errorf("MOVSD xmm0,[rax+0] = %x, want %x", buf, want)
		}
	})

	// MOVSD xmm1, [rcx+8]: F2 0F 10 89 08 00 00 00
	t.Run("MovsdXmmFromMem_xmm1_rcx_8", func(t *testing.T) {
		var buf []byte
		buf = EmitMovsdXmmFromMem(buf, 1, 1, 8)
		want := []byte{0xF2, 0x0F, 0x10, 0x89, 0x08, 0x00, 0x00, 0x00}
		if !bytesEqual(buf, want) {
			t.Errorf("MOVSD xmm1,[rcx+8] = %x, want %x", buf, want)
		}
	})

	// MOVSD [rax+0], xmm0: F2 0F 11 80 + disp32=0
	t.Run("MovsdMemFromXmm_xmm0_rax_0", func(t *testing.T) {
		var buf []byte
		buf = EmitMovsdMemFromXmm(buf, 0, 0, 0)
		want := []byte{0xF2, 0x0F, 0x11, 0x80, 0x00, 0x00, 0x00, 0x00}
		if !bytesEqual(buf, want) {
			t.Errorf("MOVSD [rax+0],xmm0 = %x, want %x", buf, want)
		}
	})

	// ADDSD xmm0, xmm1: F2 0F 58 C1
	t.Run("AddsdXmmXmm_xmm0_xmm1", func(t *testing.T) {
		var buf []byte
		buf = EmitAddsdXmmXmm(buf, 0, 1)
		want := []byte{0xF2, 0x0F, 0x58, 0xC1}
		if !bytesEqual(buf, want) {
			t.Errorf("ADDSD xmm0,xmm1 = %x, want %x", buf, want)
		}
	})

	// SUBSD xmm0, xmm1: F2 0F 5C C1
	t.Run("SubsdXmmXmm_xmm0_xmm1", func(t *testing.T) {
		var buf []byte
		buf = EmitSubsdXmmXmm(buf, 0, 1)
		want := []byte{0xF2, 0x0F, 0x5C, 0xC1}
		if !bytesEqual(buf, want) {
			t.Errorf("SUBSD xmm0,xmm1 = %x, want %x", buf, want)
		}
	})

	// MULSD xmm0, xmm1: F2 0F 59 C1
	t.Run("MulsdXmmXmm_xmm0_xmm1", func(t *testing.T) {
		var buf []byte
		buf = EmitMulsdXmmXmm(buf, 0, 1)
		want := []byte{0xF2, 0x0F, 0x59, 0xC1}
		if !bytesEqual(buf, want) {
			t.Errorf("MULSD xmm0,xmm1 = %x, want %x", buf, want)
		}
	})

	// DIVSD xmm0, xmm1: F2 0F 5E C1
	t.Run("DivsdXmmXmm_xmm0_xmm1", func(t *testing.T) {
		var buf []byte
		buf = EmitDivsdXmmXmm(buf, 0, 1)
		want := []byte{0xF2, 0x0F, 0x5E, 0xC1}
		if !bytesEqual(buf, want) {
			t.Errorf("DIVSD xmm0,xmm1 = %x, want %x", buf, want)
		}
	})

	// disp32 range test: -128 should encode as a negative LE value
	t.Run("MovsdXmmFromMem_disp32_negative", func(t *testing.T) {
		var buf []byte
		buf = EmitMovsdXmmFromMem(buf, 0, 0, -128)
		// disp32 = -128 = 0xFFFFFF80 (LE: 80 FF FF FF)
		want := []byte{0xF2, 0x0F, 0x10, 0x80, 0x80, 0xFF, 0xFF, 0xFF}
		if !bytesEqual(buf, want) {
			t.Errorf("MOVSD xmm0,[rax-128] = %x, want %x", buf, want)
		}
	})

	// byte-count constants
	t.Run("Constants", func(t *testing.T) {
		if EncodedMovsdMemLen != 8 {
			t.Errorf("EncodedMovsdMemLen = %d, want 8", EncodedMovsdMemLen)
		}
		if EncodedSseBinopLen != 4 {
			t.Errorf("EncodedSseBinopLen = %d, want 4", EncodedSseBinopLen)
		}
	})
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestPJ2_MovqMemEncoding verifies the byte-level ISA encodings for
// "mov rax, [r15+disp32]" + "mov rax, [reg+disp32]".
func TestPJ2_MovqMemEncoding(t *testing.T) {
	// mov rax, [r15+0]: 49 8B 87 00 00 00 00
	t.Run("MovqRaxFromR15Disp_0", func(t *testing.T) {
		var buf []byte
		buf = EmitMovqRaxFromR15Disp(buf, 0)
		want := []byte{0x49, 0x8B, 0x87, 0x00, 0x00, 0x00, 0x00}
		if !bytesEqual(buf, want) {
			t.Errorf("MOV rax,[r15+0] = %x, want %x", buf, want)
		}
	})

	// mov rax, [r15+16]: 49 8B 87 10 00 00 00
	t.Run("MovqRaxFromR15Disp_16", func(t *testing.T) {
		var buf []byte
		buf = EmitMovqRaxFromR15Disp(buf, 16)
		want := []byte{0x49, 0x8B, 0x87, 0x10, 0x00, 0x00, 0x00}
		if !bytesEqual(buf, want) {
			t.Errorf("MOV rax,[r15+16] = %x, want %x", buf, want)
		}
	})

	// mov rax, [rax+0]: 48 8B 80 00 00 00 00 (reg=0=rax)
	t.Run("MovqRaxFromMemReg_rax_0", func(t *testing.T) {
		var buf []byte
		buf = EmitMovqRaxFromMemReg(buf, 0, 0)
		want := []byte{0x48, 0x8B, 0x80, 0x00, 0x00, 0x00, 0x00}
		if !bytesEqual(buf, want) {
			t.Errorf("MOV rax,[rax+0] = %x, want %x", buf, want)
		}
	})

	// mov rax, [rcx+24]: 48 8B 81 18 00 00 00
	t.Run("MovqRaxFromMemReg_rcx_24", func(t *testing.T) {
		var buf []byte
		buf = EmitMovqRaxFromMemReg(buf, 1, 24)
		want := []byte{0x48, 0x8B, 0x81, 0x18, 0x00, 0x00, 0x00}
		if !bytesEqual(buf, want) {
			t.Errorf("MOV rax,[rcx+24] = %x, want %x", buf, want)
		}
	})

	t.Run("Constants", func(t *testing.T) {
		if EncodedMovqFromR15DispLen != 7 {
			t.Errorf("EncodedMovqFromR15DispLen = %d, want 7", EncodedMovqFromR15DispLen)
		}
		if EncodedMovqFromMemRegLen != 7 {
			t.Errorf("EncodedMovqFromMemRegLen = %d, want 7", EncodedMovqFromMemRegLen)
		}
	})
}

// TestPJ2_StoreAndJccEncoding verifies the byte-level encodings for store +
// compare + the full set of jcc instructions.
func TestPJ2_StoreAndJccEncoding(t *testing.T) {
	t.Run("MovqMemRegFromRax_rax_0", func(t *testing.T) {
		var buf []byte
		buf = EmitMovqMemRegFromRax(buf, 0, 0)
		want := []byte{0x48, 0x89, 0x80, 0x00, 0x00, 0x00, 0x00}
		if !bytesEqual(buf, want) {
			t.Errorf("MOV [rax+0],rax = %x, want %x", buf, want)
		}
	})
	t.Run("UcomisdXmmXmm_xmm0_xmm1", func(t *testing.T) {
		var buf []byte
		buf = EmitUcomisdXmmXmm(buf, 0, 1)
		want := []byte{0x66, 0x0F, 0x2E, 0xC1}
		if !bytesEqual(buf, want) {
			t.Errorf("UCOMISD xmm0,xmm1 = %x, want %x", buf, want)
		}
	})
	cases := []struct {
		name string
		emit func([]byte, int32) []byte
		op   byte
	}{
		{"Je", EmitJeRel32, 0x84},
		{"Jne", EmitJneRel32, 0x85},
		{"Jb", EmitJbRel32, 0x82},
		{"Jbe", EmitJbeRel32, 0x86},
		{"Ja", EmitJaRel32, 0x87},
	}
	for _, tc := range cases {
		t.Run(tc.name+"_rel0", func(t *testing.T) {
			var buf []byte
			buf = tc.emit(buf, 0)
			want := []byte{0x0F, tc.op, 0x00, 0x00, 0x00, 0x00}
			if !bytesEqual(buf, want) {
				t.Errorf("%s rel32=0 = %x, want %x", tc.name, buf, want)
			}
		})
	}
	t.Run("Constants", func(t *testing.T) {
		if EncodedMovqMemFromRaxLen != 7 {
			t.Errorf("EncodedMovqMemFromRaxLen = %d", EncodedMovqMemFromRaxLen)
		}
		if EncodedUcomisdLen != 4 {
			t.Errorf("EncodedUcomisdLen = %d", EncodedUcomisdLen)
		}
		if EncodedJccRel32Len != 6 {
			t.Errorf("EncodedJccRel32Len = %d", EncodedJccRel32Len)
		}
	})
}

// TestPJ2_SpeculativeAddTemplate verifies that the two-number ADD speculative
// template assembled by EmitArithSpeculativeAdd matches the ISA docs byte for
// byte.
//
// It does not actually execute (full wiring needs the trampoline to switch SP +
// rbx to load valueStackBase + the IsNumber guard codegen + the OSR exit path,
// deferred to the full PJ2 version). This test only asserts that the byte
// assembly is correct.
func TestPJ2_SpeculativeAddTemplate(t *testing.T) {
	// ADD A=2 B=0 C=1, two numbers (rbx = valueStackBase):
	//   movsd xmm0, [rbx+0]    F2 0F 10 83 00 00 00 00
	//   movsd xmm1, [rbx+8]    F2 0F 10 8B 08 00 00 00
	//   addsd xmm0, xmm1       F2 0F 58 C1
	//   movsd [rbx+16], xmm0   F2 0F 11 83 10 00 00 00
	//   ret                    C3
	var buf []byte
	buf = EmitArithSpeculativeAdd(buf, 2, 0, 1)

	if len(buf) != EncodedArithSpecAddLen {
		t.Fatalf("encoded length = %d, want %d", len(buf), EncodedArithSpecAddLen)
	}

	want := []byte{
		// movsd xmm0, [rbx+0]
		0xF2, 0x0F, 0x10, 0x83, 0x00, 0x00, 0x00, 0x00,
		// movsd xmm1, [rbx+8]
		0xF2, 0x0F, 0x10, 0x8B, 0x08, 0x00, 0x00, 0x00,
		// addsd xmm0, xmm1
		0xF2, 0x0F, 0x58, 0xC1,
		// movsd [rbx+16], xmm0
		0xF2, 0x0F, 0x11, 0x83, 0x10, 0x00, 0x00, 0x00,
		// ret
		0xC3,
	}
	if !bytesEqual(buf, want) {
		t.Errorf("EmitArithSpeculativeAdd =\n  %x\nwant\n  %x", buf, want)
	}
}
