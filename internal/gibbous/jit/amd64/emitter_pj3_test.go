//go:build wangshu_p4 && linux && amd64

package amd64

import "testing"

// TestPJ3_EmitMovImm64ToReg tests per-register mov encoding + mmap execution
// (prove-the-path hit evidence, extends the straight-line family of 06 §3.7).
//
// Via EmitMovImm64ToReg(reg=N) → mmap segment → segment ret → callJIT reads RAX to verify:
// when reg=RAX, RAX = imm; when reg ≠ RAX, RAX = the current RAX value at segment RET
// (= the RAX value before the mov; this test does not depend on that value — it only
// tests that the mov byte encoding produces no SEGV/SIGILL).
//
// **PJ3 simplified-form boundary**: this test mainly verifies "mov regN, imm64 byte
// encoding works"; the full MOVE template (R(A) := R(B)) also needs
// jitContext.valueStackBase + a load/store sequence, which lands together when stack
// switching is enabled in PJ4+.
func TestPJ3_EmitMovImm64ToReg(t *testing.T) {
	cases := []struct {
		regNum uint8
		imm    uint64
	}{
		{0, 0xdeadbeef},         // RAX
		{1, 0xcafebabe},         // RCX
		{2, 0xfeedface},         // RDX
		{3, 0x0123456789abcdef}, // RBX
		{6, 0xa5a5a5a5},         // RSI
		{7, ^uint64(0)},         // RDI
	}
	for _, tc := range cases {
		t.Run("", func(t *testing.T) {
			// emit: mov regN, imm; mov rax, sentinel; ret
			// (we load RAX last with the sentinel to verify the trampoline reads RAX correctly;
			// the write to regN only verifies the byte encoding does not crash — a bad encoding
			// would SIGILL/SEGV)
			sentinel := uint64(0xfeedfacecafebabe)
			var buf []byte
			buf = EmitMovImm64ToReg(buf, tc.regNum, tc.imm)
			buf = EmitMovRaxImm64(buf, sentinel)
			buf = EmitRet(buf)

			page, err := MmapCode(buf)
			if err != nil {
				t.Fatalf("MmapCode failed: %v", err)
			}
			defer func() { _ = page.Munmap() }()

			got := CallJIT(page.Addr())
			if got != sentinel {
				t.Errorf("RAX after seq = 0x%x, want sentinel 0x%x (mov regN encoding may be wrong)", got, sentinel)
			}
		})
	}
}

// TestPJ3_EmitNop tests nop byte encoding + mmap execution without crashing (extends the 06 §3.7 padding case).
func TestPJ3_EmitNop(t *testing.T) {
	imm := uint64(0xdeadbeef)
	// emit: nop; nop; mov rax, imm; ret
	var buf []byte
	for i := 0; i < 16; i++ {
		buf = EmitNop(buf)
	}
	buf = EmitMovRaxImm64(buf, imm)
	buf = EmitRet(buf)

	page, err := MmapCode(buf)
	if err != nil {
		t.Fatalf("MmapCode failed: %v", err)
	}
	defer func() { _ = page.Munmap() }()

	got := CallJIT(page.Addr())
	if got != imm {
		t.Errorf("RAX after seq with NOPs = 0x%x, want 0x%x", got, imm)
	}
}

// TestPJ3_EncodedLengths verifies the encoded-length constants match the actual byte count written by the emit functions (guards against drift).
func TestPJ3_EncodedLengths(t *testing.T) {
	cases := []struct {
		name     string
		encode   func() []byte
		expected int
	}{
		{
			name:     "mov rax, imm64",
			encode:   func() []byte { return EmitMovRaxImm64(nil, 0) },
			expected: EncodedMovRaxImm64Len,
		},
		{
			name:     "ret",
			encode:   func() []byte { return EmitRet(nil) },
			expected: EncodedRetLen,
		},
		{
			name:     "mov regN, imm64",
			encode:   func() []byte { return EmitMovImm64ToReg(nil, 3, 0) },
			expected: EncodedMovImm64ToRegLen,
		},
		{
			name:     "nop",
			encode:   func() []byte { return EmitNop(nil) },
			expected: EncodedNopLen,
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
