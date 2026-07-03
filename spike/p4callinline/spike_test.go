//go:build linux && amd64

package p4callinline

import (
	"testing"
	"unsafe"
)

// TestSA_CrossSegmentCall: a caller mmap segment CALLs into a separate
// callee mmap segment (baked absolute address), the callee computes and
// RETs back into the caller, the caller increments and RETs to Go.
//
// Green gate: result == (arg + calleeImm) + 1 — proving (a) the CALL
// crossed pages, (b) the callee executed, (c) control returned into the
// caller segment at the instruction after CALL.
func TestSA_CrossSegmentCall(t *testing.T) {
	callee, err := MmapCode(EmitLeafAddImm(1000))
	if err != nil {
		t.Fatalf("callee MmapCode: %v", err)
	}
	defer callee.Munmap() //nolint:errcheck // spike cleanup

	caller, err := MmapCode(EmitCallerToCallee(callee.Addr(), 7))
	if err != nil {
		t.Fatalf("caller MmapCode: %v", err)
	}
	defer caller.Munmap() //nolint:errcheck // spike cleanup

	got := CallSeg(caller.Addr(), 0, 0, 0)
	want := uint64(1000 + 7 + 1)
	if got != want {
		t.Fatalf("cross-segment call returned %d, want %d", got, want)
	}
}

// TestSA_CrossSegmentCall_Repeated: 1e6 iterations for stability — no
// state corruption across repeated cross-page calls.
func TestSA_CrossSegmentCall_Repeated(t *testing.T) {
	callee, err := MmapCode(EmitLeafAddImm(42))
	if err != nil {
		t.Fatal(err)
	}
	defer callee.Munmap() //nolint:errcheck // spike cleanup
	caller, err := MmapCode(EmitCallerToCallee(callee.Addr(), 8))
	if err != nil {
		t.Fatal(err)
	}
	defer caller.Munmap() //nolint:errcheck // spike cleanup
	for i := 0; i < 1_000_000; i++ {
		if got := CallSeg(caller.Addr(), 0, 0, 0); got != 51 {
			t.Fatalf("iter %d: got %d, want 51", i, got)
		}
	}
}

// TestSB_FrameWriteFromSegment: the callee segment writes 5 CI words +
// increments the ciDepth word + writes the top word, then decrements
// ciDepth (teardown half), all as raw stores. Go-side memory observes
// every write — the mirror-word protocol (syncCurFromSeg contract)
// works when the writer is an mmap segment instead of Go code.
func TestSB_FrameWriteFromSegment(t *testing.T) {
	ciSlot := make([]uint64, 5)
	mirror := make([]uint64, 2) // [0]=ciDepth word, [1]=top word
	mirror[0] = 3               // pre-existing depth

	w := [5]uint64{
		0x00000007_00000009, // base=9, funcIdx=7
		0x00000000_0000000E, // top=14, pc=0
		0x0001_0000002A,     // protoID=42, nresults=1
		0xBEEF,              // cl GCRef
		0,                   // nVarargs
	}
	code := EmitFrameWriteCallee(w, 14)
	seg, err := MmapCode(code)
	if err != nil {
		t.Fatal(err)
	}
	defer seg.Munmap() //nolint:errcheck // spike cleanup

	CallSeg(seg.Addr(),
		uintptr(unsafe.Pointer(&ciSlot[0])),
		uintptr(unsafe.Pointer(&mirror[0])),
		uintptr(unsafe.Pointer(&mirror[1])),
	)

	for i, want := range w {
		if ciSlot[i] != want {
			t.Errorf("CI word %d = %#x, want %#x", i, ciSlot[i], want)
		}
	}
	// inc then dec: net zero, but the top write persists.
	if mirror[0] != 3 {
		t.Errorf("ciDepth word = %d, want 3 (inc+dec net zero)", mirror[0])
	}
	if mirror[1] != 14 {
		t.Errorf("top word = %d, want 14", mirror[1])
	}
}

// BenchmarkSA_PerCrossCall measures the amortized per-call cost of a
// segment-to-segment CALL+RET pair: the caller loops n times calling
// the minimal `ret` callee, so each iteration is push/push/call/ret/
// pop/pop/dec/jnz ≈ the frame-management-free floor of EmitCallInline.
//
// Compare against the exit-reason round trip (~600ns/call measured in
// the issue #50 amd64 profile) — the decision gate is < 20ns.
func BenchmarkSA_PerCrossCall(b *testing.B) {
	callee, err := MmapCode(EmitLeafRet())
	if err != nil {
		b.Fatal(err)
	}
	defer callee.Munmap() //nolint:errcheck // spike cleanup

	const innerN = 1024
	caller, err := MmapCode(EmitCallerLoop(callee.Addr(), innerN))
	if err != nil {
		b.Fatal(err)
	}
	defer caller.Munmap() //nolint:errcheck // spike cleanup

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		CallSeg(caller.Addr(), 0, 0, 0)
	}
	b.StopTimer()
	nsPerCross := float64(b.Elapsed().Nanoseconds()) / float64(b.N) / innerN
	b.ReportMetric(nsPerCross, "ns/crosscall")
}

// BenchmarkSB_FrameWrite measures the raw-store frame build+teardown
// cost from inside a segment (5 CI words + inc/dec depth + top).
func BenchmarkSB_FrameWrite(b *testing.B) {
	ciSlot := make([]uint64, 5)
	mirror := make([]uint64, 2)
	code := EmitFrameWriteCallee([5]uint64{1, 2, 3, 4, 5}, 14)
	seg, err := MmapCode(code)
	if err != nil {
		b.Fatal(err)
	}
	defer seg.Munmap() //nolint:errcheck // spike cleanup
	rcx := uintptr(unsafe.Pointer(&ciSlot[0]))
	rdx := uintptr(unsafe.Pointer(&mirror[0]))
	r8 := uintptr(unsafe.Pointer(&mirror[1]))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		CallSeg(seg.Addr(), rcx, rdx, r8)
	}
}
