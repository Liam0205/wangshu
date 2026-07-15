//go:build linux && amd64

package p4tramp

import (
	"testing"
)

// TestSpike_S1_RoundTrip gates ①②③: exec mmap + W^X flip + the minimal
// Go → mmap → ret round-trip, verifying that the mmap segment's return value
// == the imm64 burned in by EmitMovRaxImm64Ret.
//
// This is the foundational test of the PJ1 spike — if it fails, the physical
// basis of the P4 amd64 backend (executing JIT segments) does not hold and the
// emitter trait should not land (per 06 §1.7).
func TestSpike_S1_RoundTrip(t *testing.T) {
	cases := []uint64{
		0,
		1,
		0xdeadbeef,
		0xcafebabedeadbeef,
		^uint64(0),
	}
	for _, imm := range cases {
		t.Run("", func(t *testing.T) {
			code := EmitMovRaxImm64Ret(imm)
			if len(code) != 11 {
				t.Fatalf("encoded length should be 11 bytes, got %d", len(code))
			}
			page, err := MmapCode(code)
			if err != nil {
				t.Fatalf("MmapCode failed: %v", err)
			}
			defer func() {
				if err := page.Munmap(); err != nil {
					t.Errorf("Munmap failed: %v", err)
				}
			}()

			got := CallJIT(page.Addr())
			if got != imm {
				t.Errorf("CallJIT returned 0x%x, expected 0x%x", got, imm)
			}
		})
	}
}

// TestSpike_S2_RepeatedCalls gates ② symmetry: repeatedly CALLing the same
// mmap segment does not corrupt state.
//
// This simulates the "the same Proto's compiled output is called many times"
// scenario — the full PJ1 trampoline must guarantee that many entries/exits do
// not touch the runtime / leak the stack / disturb the Go scheduler. This
// minimal spike does not switch SP, but it still needs to verify that "stable
// return value across many round-trips" is the minimum requirement for a
// side-effect-free trampoline design.
func TestSpike_S2_RepeatedCalls(t *testing.T) {
	imm := uint64(0xfeedface00c0ffee)
	code := EmitMovRaxImm64Ret(imm)
	page, err := MmapCode(code)
	if err != nil {
		t.Fatalf("MmapCode failed: %v", err)
	}
	defer func() { _ = page.Munmap() }()

	const N = 10000
	for i := 0; i < N; i++ {
		got := CallJIT(page.Addr())
		if got != imm {
			t.Fatalf("call #%d: got 0x%x, want 0x%x", i, got, imm)
		}
	}
}

// TestSpike_S3_MultiplePages gates ② isolation: holding multiple mmap segments
// at once, each returns its own independent value.
//
// This simulates the "multiple Protos, each with its own compiled output"
// scenario.
func TestSpike_S3_MultiplePages(t *testing.T) {
	pages := make([]*CodePage, 8)
	imms := make([]uint64, 8)
	for i := range pages {
		imms[i] = uint64(0x10000+i) << 32 // high-bit difference for easy distinction
		code := EmitMovRaxImm64Ret(imms[i])
		var err error
		pages[i], err = MmapCode(code)
		if err != nil {
			t.Fatalf("page %d MmapCode failed: %v", i, err)
		}
	}
	defer func() {
		for _, p := range pages {
			_ = p.Munmap()
		}
	}()

	// Interleaved calls: out of order, more than once, to verify no cross-talk between segments
	for round := 0; round < 100; round++ {
		for i := range pages {
			j := (i + round) % len(pages)
			got := CallJIT(pages[j].Addr())
			if got != imms[j] {
				t.Errorf("round %d page %d: got 0x%x, want 0x%x", round, j, got, imms[j])
			}
		}
	}
}

// BenchmarkSpike_CallJIT gates ④: measures the per-call cost of Go → mmap
// segment → ret (per the spike template).
//
// It corresponds to the P3 spike S1 empty round-trip of 18.9ns (per
// docs/design/p3-wasm-tier/implementation-progress.md §0.1) — the physical
// basis for a P4 mmap-segment single CALL being cheaper than a wazero boundary
// crossing is that it "does not go through the wazero/Wasm intermediary". This
// bench provides the cost baseline for the full PJ1 trampoline.
//
// Form: emit a 9-byte "mov rax, IMM; ret" segment (straight-line; the real P4
// LOADK template form), which Go calls repeatedly via CallJIT, amortized over
// the b.N loop.
func BenchmarkSpike_CallJIT(b *testing.B) {
	imm := uint64(0xdeadbeef)
	code := EmitMovRaxImm64Ret(imm)
	page, err := MmapCode(code)
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
