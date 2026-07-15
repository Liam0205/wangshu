//go:build wangshu_p4 && arm64 && (linux || (darwin && cgo)) && !wangshu_qemu

// This test really goes through MmapCode + CallJITFull / CallJITSpec to jump
// into the mmap segment and execute arm64 instructions (per the §9.20.9
// trampoline protocol).
//
// **build tag design** (per F2 QEMU + F3-#3b real-hardware M1 SIGSEGV fix):
//   - linux || (darwin && cgo): **both ends share the same Plan 9 ABI0 asm**
//     (per 509d5af build tag extension + F3-#3b STP/LDP offset +8 fix for the
//     LR slot overwrite bug; on real M1 hardware round-trip 5/5 + spec
//     round-trip 4/4 + 1000-iteration callee-saved verification all pass). The
//     original linux-only restriction was an F3-#3a debugging placeholder,
//     reverted once the root cause was isolated to the trampoline.
//   - !wangshu_qemu: QEMU user-mode emulation does not truly emulate i-cache
//     flush / PROT_EXEC; observed SIGSEGV at the RET byte of the mmap segment
//     (per F2 + ci.yml L94-99).
//
// CI currently covers three paths: linux/amd64 main path + linux/arm64 QEMU
// (skips this test) + macos-latest M1 physical e2e (runs this test).

package arm64

import (
	"testing"
	"unsafe"
)

// dummyJITCtx returns the address of a zeroed buffer large enough to stand
// in for a *JITContext in trampoline round-trip tests. It must be at least
// as large as the real struct so the trampoline's issue #89 spill-stack
// read ([jitCtx+spillBaseOff], [jitCtx+savedGoSPOff]) stays in bounds. All
// bytes are 0, so spillBase reads as 0 and the trampoline takes the
// no-switch path — these tests exercise jitCtx loading, not the SP switch.
// (This package can't import the jit package for the real JITContext: jit
// imports jit/arm64, which would be an import cycle.)
func dummyJITCtx() uintptr {
	buf := new([32]uint64) // 256 B, well past savedGoSP at offset 176
	return uintptr(unsafe.Pointer(buf))
}

// TestPJ8_CallJITFull_RoundTrip verifies the full trampoline asm works:
//   - saving callee-saved registers (X19-X27)
//   - loading X27 = jitCtx (passed in as a uintptr argument)
//   - BL into the mmap segment + ret, taking X0
//   - restoring callee-saved
//
// The template is the same as PJ8's simplified form (mov X0, imm; ret),
// verifying that the "full path with callee-saved protection" also runs — this
// is the physical basis for introducing helper CALL sub-calls when PJ3+ starts
// up (mirroring amd64 TestPJ2_CallJITFull_RoundTrip).
func TestPJ8_CallJITFull_RoundTrip(t *testing.T) {
	cases := []uint64{
		0,
		1,
		0xdeadbeef,
		0xcafebabedeadbeef,
		^uint64(0),
	}
	jitCtxAddr := dummyJITCtx()

	for _, imm := range cases {
		t.Run("", func(t *testing.T) {
			var buf []byte
			buf = EmitMovX0Imm64(buf, imm)
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

// TestPJ8_CallJITSpec_RoundTrip verifies the spec trampoline asm works:
//   - saving callee-saved registers (X19-X27)
//   - loading X27 = jitCtx + **X26 = vsBase** (the spec form loads vsBase too)
//   - BL into the mmap segment + ret, taking X0
//   - restoring callee-saved
//
// The template is the same simplified form as callJITFull (mov X0, imm; ret);
// the vsBase argument is not dereferenced inside the segment (the template does
// not read X26), so this only verifies the trampoline path is correct
// (mirroring the same verification form as amd64 callJITSpec). The physical
// runner end-to-end further verifies the PJ2 speculative template + PJ3 FORLOOP
// body/body2/RegLimit really addressing the value stack via [x26+disp].
func TestPJ8_CallJITSpec_RoundTrip(t *testing.T) {
	cases := []uint64{
		0,
		0xdeadbeef,
		0xcafebabe_deadbeef,
		^uint64(0),
	}
	jitCtxAddr := dummyJITCtx()
	// vsBase uses a placeholder dummy address (the template does not read it;
	// the trampoline loads it but the mmap segment never dereferences it)
	vsBase := struct{ dummy uint64 }{dummy: 0xcafefeed}
	vsBaseAddr := uintptr(unsafe.Pointer(&vsBase))

	for _, imm := range cases {
		t.Run("", func(t *testing.T) {
			var buf []byte
			buf = EmitMovX0Imm64(buf, imm)
			buf = EmitRet(buf)

			page, err := MmapCode(buf)
			if err != nil {
				t.Fatalf("MmapCode failed: %v", err)
			}
			defer func() { _ = page.Munmap() }()

			got := CallJITSpec(page.Addr(), jitCtxAddr, vsBaseAddr)
			if got != imm {
				t.Errorf("CallJITSpec returned 0x%x, expected 0x%x", got, imm)
			}
		})
	}
}

// TestPJ8_CallJITSpec_CalleeSavedPreserved verifies the spec trampoline does
// not clobber the caller's callee-saved registers (X19-X27).
//
// Verification approach: Go calls CallJITSpec several times and then runs some
// Go code (allocation + computation); if callee-saved is corrupted, the Go
// runtime will SEGV or get wrong values during scheduling / GC. This test
// mainly relies on "no SEGV + normal Go runtime behavior" as implicit
// verification; -race adds more stress.
func TestPJ8_CallJITSpec_CalleeSavedPreserved(t *testing.T) {
	imm := uint64(0xfeedface)
	var buf []byte
	buf = EmitMovX0Imm64(buf, imm)
	buf = EmitRet(buf)

	page, err := MmapCode(buf)
	if err != nil {
		t.Fatalf("MmapCode failed: %v", err)
	}
	defer func() { _ = page.Munmap() }()

	jitCtxAddr := dummyJITCtx()
	vsBase := struct{ dummy uint64 }{dummy: 0xcafefeed}
	vsBaseAddr := uintptr(unsafe.Pointer(&vsBase))

	// Run N times, interleaving Go allocation + computation; if callee-saved
	// is corrupted, this will SEGV or get wrong values
	const N = 1000
	sum := uint64(0)
	for i := 0; i < N; i++ {
		got := CallJITSpec(page.Addr(), jitCtxAddr, vsBaseAddr)
		if got != imm {
			t.Errorf("iter %d: CallJITSpec returned 0x%x, expected 0x%x", i, got, imm)
			return
		}
		// Interleave Go computation + trigger occasional GC scenarios
		// (make a short-lived slice)
		s := make([]byte, 16)
		for j := range s {
			s[j] = byte(i + j)
		}
		sum += uint64(s[0])
	}
	if sum == 0 {
		// Prevent sum from being optimized away by the Go compiler
		t.Errorf("sum should not be 0 (escape sentinel)")
	}
}
