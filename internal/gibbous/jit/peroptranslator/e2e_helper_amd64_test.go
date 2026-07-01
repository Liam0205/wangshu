//go:build wangshu_p4 && amd64 && linux

// e2e_helper_amd64_test.go — verify mmap can safely call a Go helper
// via emitRestoreGoG + emitHelperCall. This closes the "helper call from
// native emit" gate that PJ10 needs for RETURN / GETUPVAL / arithmetic
// slow path.
package peroptranslator

import (
	"testing"
	"unsafe"

	jit "github.com/Liam0205/wangshu/internal/gibbous/jit"
	jitamd64 "github.com/Liam0205/wangshu/internal/gibbous/jit/amd64"
)

// pj10TestHelper is a Go function called from mmap via the emit protocol.
// SysV ABI: arg in rdi, return in rax. Doubles its argument.
//
//go:noinline
func pj10TestHelper(x uint64) uint64 {
	return x * 2
}

// TestPJ10Native_E2E_HelperCall verifies that the emitRestoreGoG +
// emitHelperCall sequence:
//   - Restores R14 = G before the call
//   - Passes rdi to the helper (SysV ABI first arg)
//   - Receives rax from the helper (SysV ABI return)
//   - Preserves R14 = G on return so subsequent Go-heap operations are safe
//
// Sequence emitted:
//
//	mov rdi, imm64        ; load argument
//	mov r14, [r15+savedG] ; restore G
//	mov rax, helperAddr   ; helper Go func pointer
//	call rax              ; helper runs
//	mov [rbx+0], rax      ; store result at R(0)
//	ret
//
// TestPJ10Native_E2E_HelperCall verifies that the emitRestoreGoG +
// emitHelperCall sequence:
//   - Restores R14 = G before the call
//   - Passes RAX to the helper (Go ABIInternal first int arg)
//   - Receives RAX from the helper (Go ABIInternal return)
//   - Preserves R14 = G on return so subsequent Go-heap operations are safe
//
// Sequence emitted:
//
//	mov rax, imm64            ; ABIInternal first int arg
//	mov r14, [r15+savedG]     ; restore G
//	mov rcx, helperAddr       ; can't use rax since it holds the arg
//	call rcx
//	mov [rbx+0], rax          ; store result at R(0)
//	ret
//
// **ABI note**: Go 1.17+ uses ABIInternal by default (args in RAX/RBX/
// RCX/RDI/RSI/R8/R9/R10/R11, in that order). This test uses ABIInternal
// directly, which is fragile across Go versions — the long-term fix is
// ABI0 shim wrappers per helper.
func TestPJ10Native_E2E_HelperCall(t *testing.T) {
	var stack [4]uint64
	stack[0] = 0xdeadbeef

	// Go 1.17+: to call a Go func from asm/native, we need the entry PC.
	// reflect.ValueOf(fn).Pointer() returns the funcval pointer, not the
	// entry PC. Deref once to get the entry PC.
	fn := pj10TestHelper
	fpp := *(*unsafe.Pointer)(unsafe.Pointer(&fn)) // funcval pointer
	helperAddr := uint64(*(*uintptr)(fpp))         // funcval.fn = entry PC

	cb := newCodeBuf(1)
	cb.bindLabel(0)

	// mov rax, 21 — ABIInternal first int arg in RAX
	imm := uint64(21)
	cb.emit([]byte{0x48, 0xB8})
	for i := 0; i < 8; i++ {
		cb.emit([]byte{byte(imm >> (8 * i))})
	}
	// Restore R14 = G before the Go call
	emitRestoreGoG(cb)
	// mov rcx, helperAddr; call rcx — can't use RAX since it holds the arg
	cb.emit([]byte{0x48, 0xB9}) // mov rcx, imm64
	for i := 0; i < 8; i++ {
		cb.emit([]byte{byte(helperAddr >> (8 * i))})
	}
	cb.emit([]byte{0xFF, 0xD1}) // call rcx
	// mov [rbx+0], rax — store ABIInternal return value at R(0)
	cb.emit(jitamd64.EmitMovqMemRegFromRax(nil, regRBX, 0))
	emitRet(cb)

	page, err := jitamd64.MmapCode(cb.bytes)
	if err != nil {
		t.Fatalf("MmapCode: %v", err)
	}
	defer page.Munmap()

	ctx := jit.NewJITContext()
	// Save Go G into jitCtx.savedGoG BEFORE entering the mmap.
	saveGoG(ctx.SavedGoGSlot())
	if ctx.SavedGoG() == 0 {
		t.Fatal("saveGoG did not populate savedGoG")
	}

	vsBase := uintptr(unsafe.Pointer(&stack[0]))
	_ = jitamd64.CallJITSpec(page.Addr(), uintptr(unsafe.Pointer(ctx)), vsBase)

	if stack[0] != 42 {
		t.Errorf("stack[0] = %d, want 42 (helper returned 21*2)", stack[0])
	}
}
