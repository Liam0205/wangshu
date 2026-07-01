//go:build wangshu_p4 && amd64 && linux

// e2e_test_op_amd64_test.go - regression tests for the inline TEST
// emit's forward rel8 branch arithmetic. Prevents recurrence of the
// bug reviewed in .code-review/from-ed2235b/from-ed2235b-to-1359a21.md
// (rel8 hand-count off by 8 bytes due to stale intermediate byte
// counts in the emit comment).
package peroptranslator

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/value"
)

// buildTestCB constructs a codeBuf with 3 BBs:
//
//	BB0: emit TEST A=0, C=<c>, succExec=BB1, succSkip=BB2
//	BB1: store MARKER_EXEC into R(1); ret
//	BB2: store MARKER_SKIP into R(1); ret
//
// After Run, R(1) tells which arm was taken.
func buildTestCB(t *testing.T, c uint8, seedR0 uint64) uint64 {
	t.Helper()
	cb := newCodeBuf(3)
	if err := cb.bindLabel(0); err != nil {
		t.Fatal(err)
	}
	// Forward-declare the two BBs (label binding happens after emit).
	emitTEST(cb, 0 /*A*/, c, 1 /*succExec=BB1*/, 2 /*succSkip=BB2*/)

	// BB1: R(1) := value.True (marker for "exec")
	if err := cb.bindLabel(1); err != nil {
		t.Fatal(err)
	}
	emitLOADK(cb, 1, uint64(value.True))
	emitRet(cb)

	// BB2: R(1) := value.False (marker for "skip")
	if err := cb.bindLabel(2); err != nil {
		t.Fatal(err)
	}
	emitLOADK(cb, 1, uint64(value.False))
	emitRet(cb)

	if err := cb.resolveLabels(); err != nil {
		t.Fatalf("resolveLabels: %v", err)
	}

	stack, _ := runNoShimSegment(t, cb, func(s []uint64) {
		s[0] = seedR0
	})
	return stack[1]
}

// TestPJ10Native_E2E_TEST_TruthyC1_TakesExec: R(0)=true, C=1 -> truthy
// matches C -> take succExec (BB1) -> R(1) == True.
func TestPJ10Native_E2E_TEST_TruthyC1_TakesExec(t *testing.T) {
	got := buildTestCB(t, 1, uint64(value.NumberValue(42))) // truthy input
	if got != uint64(value.True) {
		t.Errorf("R(1) = %x, want True (%x) — truthy input + C=1 should take succExec",
			got, uint64(value.True))
	}
}

// TestPJ10Native_E2E_TEST_TruthyC0_TakesSkip: R(0)=truthy, C=0 -> truthy
// doesn't match C=0 -> take succSkip (BB2) -> R(1) == False.
func TestPJ10Native_E2E_TEST_TruthyC0_TakesSkip(t *testing.T) {
	got := buildTestCB(t, 0, uint64(value.NumberValue(42))) // truthy
	if got != uint64(value.False) {
		t.Errorf("R(1) = %x, want False — truthy input + C=0 should take succSkip",
			got)
	}
}

// TestPJ10Native_E2E_TEST_NilC1_TakesSkip: R(0)=nil, C=1 -> not-truthy
// doesn't match C=1 -> take succSkip (BB2).
func TestPJ10Native_E2E_TEST_NilC1_TakesSkip(t *testing.T) {
	got := buildTestCB(t, 1, uint64(value.Nil))
	if got != uint64(value.False) {
		t.Errorf("R(1) = %x, want False — Nil input + C=1 should take succSkip",
			got)
	}
}

// TestPJ10Native_E2E_TEST_FalseC0_TakesExec: R(0)=false, C=0 -> not-truthy
// matches C=0 -> take succExec (BB1).
func TestPJ10Native_E2E_TEST_FalseC0_TakesExec(t *testing.T) {
	got := buildTestCB(t, 0, uint64(value.False))
	if got != uint64(value.True) {
		t.Errorf("R(1) = %x, want True — False input + C=0 should take succExec",
			got)
	}
}
