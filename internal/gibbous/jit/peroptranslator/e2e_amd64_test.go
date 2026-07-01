//go:build wangshu_p4 && amd64 && linux

// e2e_amd64_test.go — real end-to-end test: emit real amd64 bytes into a
// mmap segment, execute via CallJITSpec, verify observable side effects.
//
// This is the smallest possible "actually native code is running" test —
// no host helpers, no Proto compilation, just direct emit + mmap + call.
package peroptranslator

import (
	"testing"
	"unsafe"

	jit "github.com/Liam0205/wangshu/internal/gibbous/jit"
	jitamd64 "github.com/Liam0205/wangshu/internal/gibbous/jit/amd64"
	"github.com/Liam0205/wangshu/internal/value"
)

// TestPJ10Native_E2E_LOADK verifies real native emit end-to-end:
//   - Allocate a 4-slot value stack ([4]uint64) in Go.
//   - Emit `mov rax, value.False; mov [rbx+0], rax; ret`.
//   - CallJITSpec with vsBase = &stack[0].
//   - Assert stack[0] == uint64(value.False).
func TestPJ10Native_E2E_LOADK(t *testing.T) {
	var stack [4]uint64
	// pre-poison so a bug that skips the write is visible
	for i := range stack {
		stack[i] = 0xdeadbeefcafebabe
	}

	cb := newCodeBuf(1)
	cb.bindLabel(0)
	emitLOADK(cb, 0, uint64(value.False))
	// ret (0xC3) — return from mmap back to trampoline
	cb.emit([]byte{0xC3})

	page, err := jitamd64.MmapCode(cb.bytes)
	if err != nil {
		t.Fatalf("MmapCode: %v", err)
	}
	defer page.Munmap()

	ctx := jit.NewJITContext()
	vsBase := uintptr(unsafe.Pointer(&stack[0]))
	_ = jitamd64.CallJITSpec(page.Addr(), uintptr(unsafe.Pointer(ctx)), vsBase)

	if stack[0] != uint64(value.False) {
		t.Errorf("stack[0] = %x, want %x (value.False)", stack[0], uint64(value.False))
	}
	// Other slots must not have been touched.
	for i := 1; i < len(stack); i++ {
		if stack[i] != 0xdeadbeefcafebabe {
			t.Errorf("stack[%d] = %x, want unchanged", i, stack[i])
		}
	}
}

// TestPJ10Native_E2E_MOVE verifies MOVE: seed R(1) = value.True, run
// `MOVE R(0), R(1)`, assert R(0) == value.True.
func TestPJ10Native_E2E_MOVE(t *testing.T) {
	var stack [4]uint64
	stack[1] = uint64(value.True)
	// R(0) starts as garbage; MOVE should overwrite it.
	stack[0] = 0xdeadbeef

	cb := newCodeBuf(1)
	cb.bindLabel(0)
	emitMOVE(cb, 0, 1)
	cb.emit([]byte{0xC3})

	page, err := jitamd64.MmapCode(cb.bytes)
	if err != nil {
		t.Fatalf("MmapCode: %v", err)
	}
	defer page.Munmap()

	ctx := jit.NewJITContext()
	vsBase := uintptr(unsafe.Pointer(&stack[0]))
	_ = jitamd64.CallJITSpec(page.Addr(), uintptr(unsafe.Pointer(ctx)), vsBase)

	if stack[0] != uint64(value.True) {
		t.Errorf("stack[0] = %x, want %x (value.True)", stack[0], uint64(value.True))
	}
}

// TestPJ10Native_E2E_LOADNIL_multiSlot verifies LOADNIL A=1 B=3 writes
// nil into R(1)..R(3) but leaves R(0) untouched.
func TestPJ10Native_E2E_LOADNIL_multiSlot(t *testing.T) {
	var stack [4]uint64
	for i := range stack {
		stack[i] = 0xdeadbeef
	}

	cb := newCodeBuf(1)
	cb.bindLabel(0)
	emitLOADNIL(cb, 1, 3)
	cb.emit([]byte{0xC3})

	page, err := jitamd64.MmapCode(cb.bytes)
	if err != nil {
		t.Fatalf("MmapCode: %v", err)
	}
	defer page.Munmap()

	ctx := jit.NewJITContext()
	vsBase := uintptr(unsafe.Pointer(&stack[0]))
	_ = jitamd64.CallJITSpec(page.Addr(), uintptr(unsafe.Pointer(ctx)), vsBase)

	if stack[0] != 0xdeadbeef {
		t.Errorf("R(0) = %x, want unchanged", stack[0])
	}
	nilBits := uint64(value.Nil)
	for i := 1; i <= 3; i++ {
		if stack[i] != nilBits {
			t.Errorf("R(%d) = %x, want %x (Nil)", i, stack[i], nilBits)
		}
	}
}

// TestPJ10Native_E2E_JMP: emit `LOADK R(0)=True; JMP $end; LOADK R(0)=False; $end: ret`.
// The JMP should skip the second LOADK so R(0) ends as True.
func TestPJ10Native_E2E_JMP(t *testing.T) {
	var stack [4]uint64

	cb := newCodeBuf(2) // BB0: emit body, BB1: end (ret only)
	cb.bindLabel(0)
	emitLOADK(cb, 0, uint64(value.True))
	emitJMP(cb, 1)
	// Dead code — LOADK False that should not execute
	emitLOADK(cb, 0, uint64(value.False))
	cb.bindLabel(1)
	cb.emit([]byte{0xC3})

	if err := cb.resolveLabels(); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	page, err := jitamd64.MmapCode(cb.bytes)
	if err != nil {
		t.Fatalf("MmapCode: %v", err)
	}
	defer page.Munmap()

	ctx := jit.NewJITContext()
	vsBase := uintptr(unsafe.Pointer(&stack[0]))
	_ = jitamd64.CallJITSpec(page.Addr(), uintptr(unsafe.Pointer(ctx)), vsBase)

	if stack[0] != uint64(value.True) {
		t.Errorf("stack[0] = %x, want value.True (JMP should skip the second LOADK)",
			stack[0])
	}
}
