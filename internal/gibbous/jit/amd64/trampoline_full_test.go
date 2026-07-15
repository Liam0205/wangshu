//go:build wangshu_p4 && linux && amd64

package amd64

import (
	"testing"
	"unsafe"
)

// TestPJ2_CallJITFull_RoundTrip verifies the full trampoline asm works:
//   - saves callee-saved registers (rbx/rbp/r12/r13/r15)
//   - loads r15 = jitCtx (passed in as a uintptr argument)
//   - CALLs the mmap segment + gets RAX from ret
//   - restores callee-saved
//
// The template is the same as the PJ1 simplified form (`mov rax, imm; ret`),
// verifying that the "full path with callee-saved protection" also runs — this
// is the physical basis for introducing helper CALL sub-calls starting at PJ3+.
func TestPJ2_CallJITFull_RoundTrip(t *testing.T) {
	cases := []uint64{
		0,
		1,
		0xdeadbeef,
		0xcafebabedeadbeef,
		^uint64(0),
	}
	// Prepare a jitCtx (this test never dereferences it, only verifies the trampoline path)
	ctx := struct{ dummy uint64 }{dummy: 0xfeedface}
	jitCtxAddr := uintptr(unsafe.Pointer(&ctx))

	for _, imm := range cases {
		t.Run("", func(t *testing.T) {
			var buf []byte
			buf = EmitMovRaxImm64(buf, imm)
			buf = EmitRet(buf)

			page, err := MmapCode(buf)
			if err != nil {
				t.Fatalf("MmapCode failed: %v", err)
			}
			defer func() { _ = page.Munmap() }()

			got := CallJITFull(page.Addr(), jitCtxAddr)
			if got != imm {
				t.Errorf("CallJITFull returned 0x%x, expected 0x%x", got, imm)
			}
		})
	}
}

// TestPJ2_CallJITFull_CalleeSavedPreserved: the full trampoline does not
// clobber the caller's callee-saved registers (rbx/rbp/r12/r13/r15).
//
// Verification method: after Go calls CallJITFull many times, run some Go code
// (allocation + computation); if callee-saved were corrupted, the Go runtime
// would SEGV or get wrong values during scheduling / GC. This test relies
// mainly on "no SEGV + Go runtime behaves normally" as implicit verification;
// -race adds more pressure.
func TestPJ2_CallJITFull_CalleeSavedPreserved(t *testing.T) {
	imm := uint64(0xfeedface)
	var buf []byte
	buf = EmitMovRaxImm64(buf, imm)
	buf = EmitRet(buf)

	page, err := MmapCode(buf)
	if err != nil {
		t.Fatalf("MmapCode failed: %v", err)
	}
	defer func() { _ = page.Munmap() }()

	ctx := struct{ dummy uint64 }{dummy: 0xfeedface}
	jitCtxAddr := uintptr(unsafe.Pointer(&ctx))

	const N = 1000
	// Run some Go code that uses callee-saved (slice allocation triggers GC, computation uses r12+)
	accum := uint64(0)
	for i := 0; i < N; i++ {
		got := CallJITFull(page.Addr(), jitCtxAddr)
		if got != imm {
			t.Fatalf("call #%d: got 0x%x, want 0x%x", i, got, imm)
		}
		// Explicitly do some Go computation that uses callee-saved
		s := make([]uint64, 4)
		for j := range s {
			s[j] = uint64(i + j)
			accum += s[j]
		}
	}
	if accum == 0 {
		t.Fatal("accumulator should be non-zero after N iterations")
	}
}

// BenchmarkPJ2_CallJITFull: cost of a single CALL through the full trampoline
// (against the PJ1 simplified form's 1.96ns; the full version is expected to be
// ~3-5ns, i.e. the "save/restore 5 callee-saved" overhead introduced by PJ2).
func BenchmarkPJ2_CallJITFull(b *testing.B) {
	imm := uint64(0xdeadbeef)
	var buf []byte
	buf = EmitMovRaxImm64(buf, imm)
	buf = EmitRet(buf)

	page, err := MmapCode(buf)
	if err != nil {
		b.Fatalf("MmapCode failed: %v", err)
	}
	defer func() { _ = page.Munmap() }()

	ctx := struct{ dummy uint64 }{dummy: 0xfeedface}
	jitCtxAddr := uintptr(unsafe.Pointer(&ctx))

	addr := page.Addr()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		got := CallJITFull(addr, jitCtxAddr)
		if got != imm {
			b.Fatalf("call #%d: got 0x%x, want 0x%x", i, got, imm)
		}
	}
}
