//go:build wangshu_p4 && amd64 && linux

// e2e_arith_amd64_test.go - end-to-end tests for arithmetic + logic ops.
package peroptranslator

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/value"
)

// TestPJ10Native_E2E_ADD_inline: emit ADD A=2 B=0 C=1 with reg-reg
// operands. Verify the inline SSE fast path computes R(2) = R(0) + R(1)
// without any shim call.
func TestPJ10Native_E2E_ADD_inline(t *testing.T) {
	cb := newCodeBuf(1)
	cb.bindLabel(0)
	emitADD(cb, 5 /*pc*/, 2 /*A*/, 0 /*B*/, 1 /*C*/)
	emitRet(cb)

	stack, _ := runNoShimSegment(t, cb, func(s []uint64) {
		s[0] = uint64(value.NumberValue(10))
		s[1] = uint64(value.NumberValue(32))
	})
	got := value.AsNumber(value.Value(stack[2]))
	if got != 42 {
		t.Errorf("R(2) = %v, want 42 (10 + 32)", got)
	}
}

// TestPJ10Native_E2E_ADD_RK_shim: ADD with C >= 256 (K-encoded) falls
// through to the shim path.
func TestPJ10Native_E2E_ADD_RK_shim(t *testing.T) {
	cb := newCodeBuf(1)
	cb.bindLabel(0)
	emitADD(cb, 5 /*pc*/, 2 /*A*/, 0 /*B*/, 256 /*C=K[0]*/)
	emitRet(cb)

	host := newArithHost()
	_ = runShimSegment(t, cb, host)

	if host.arithCalls != 1 {
		t.Fatalf("Arith calls = %d, want 1 (shim fallback for RK operand)", host.arithCalls)
	}
	if host.arithOp != int32(bytecode.ADD) {
		t.Errorf("Arith op = %d, want ADD=%d", host.arithOp, bytecode.ADD)
	}
	if host.arithB != 0 || host.arithC != 256 || host.arithA != 2 {
		t.Errorf("Arith B=%d C=%d A=%d, want 0/256/2", host.arithB, host.arithC, host.arithA)
	}
}

// TestPJ10Native_E2E_NOT: emit NOT A=1 B=0 with R(0) = false.
// Verify R(1) = true (falsy input inverts to true).
func TestPJ10Native_E2E_NOT_falseToTrue(t *testing.T) {
	cb := newCodeBuf(1)
	cb.bindLabel(0)
	emitNOT(cb, 1 /*A*/, 0 /*B*/)
	emitRet(cb)

	stack, _ := runNoShimSegment(t, cb, func(s []uint64) {
		s[0] = uint64(value.False) // R(0) := false
	})
	if stack[1] != uint64(value.True) {
		t.Errorf("R(1) = %x, want True (%x)", stack[1], uint64(value.True))
	}
}

// TestPJ10Native_E2E_NOT_trueToFalse: input truthy (e.g. a number).
func TestPJ10Native_E2E_NOT_trueToFalse(t *testing.T) {
	cb := newCodeBuf(1)
	cb.bindLabel(0)
	emitNOT(cb, 1 /*A*/, 0 /*B*/)
	emitRet(cb)

	stack, _ := runNoShimSegment(t, cb, func(s []uint64) {
		s[0] = uint64(value.NumberValue(42)) // R(0) := 42
	})
	if stack[1] != uint64(value.False) {
		t.Errorf("R(1) = %x, want False (%x)", stack[1], uint64(value.False))
	}
}

// TestPJ10Native_E2E_NOT_nilToTrue: nil is falsy in Lua.
func TestPJ10Native_E2E_NOT_nilToTrue(t *testing.T) {
	cb := newCodeBuf(1)
	cb.bindLabel(0)
	emitNOT(cb, 1 /*A*/, 0 /*B*/)
	emitRet(cb)

	stack, _ := runNoShimSegment(t, cb, func(s []uint64) {
		s[0] = uint64(value.Nil) // R(0) := nil
	})
	if stack[1] != uint64(value.True) {
		t.Errorf("R(1) = %x, want True (%x)", stack[1], uint64(value.True))
	}
}
