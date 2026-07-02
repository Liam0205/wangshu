//go:build wangshu_p4 && amd64

// emit_shim_amd64.go - shim-call emit primitives. Wraps the ABIInternal
// argument setup + Go G restore + call + vsBase reload into helpers so
// per-op emit code doesn't repeat the boilerplate.
//
// Go 1.17+ ABIInternal argument register order for int args:
//
//	arg0=RAX arg1=RBX arg2=RCX arg3=RDI arg4=RSI arg5=R8 arg6=R9 arg7=R10 arg8=R11
//
// All shims defined in shims.go take *JITContext as arg0. The mmap emit
// must place jitCtx (from R15) into RAX before the call.
//
// R14 (Go G) is preserved by Go across ABIInternal calls (its function
// prologue expects R14=G at entry). We restore R14 via emitRestoreGoG
// before every call so morestack/getg/stack-guard read correct G.
//
// R15 (jitCtx) is also preserved by Go (verified empirically -- see
// TestPJ10Native_R15PreservedAcrossGoCall). RBX is NOT preserved (used
// as ABIInternal arg1), so callers must emit emitReloadVsBase after
// the call to restore RBX = vsBase for R(N) memory operands.
package peroptranslator

import (
	"reflect"
	"unsafe"

	jit "github.com/Liam0205/wangshu/internal/gibbous/jit"
)

// abiIntArgRegs lists Go ABIInternal register numbers for int args in
// order. Index i corresponds to argN (0-indexed).
//
// Register numbering follows Intel: RAX=0, RCX=1, RDX=2, RBX=3, RSP=4,
// RBP=5, RSI=6, RDI=7, R8=8, R9=9, R10=10, R11=11, R12=12, R13=13,
// R14=14, R15=15.
var abiIntArgRegs = [9]uint8{
	0,  // RAX
	3,  // RBX
	1,  // RCX
	7,  // RDI
	6,  // RSI
	8,  // R8
	9,  // R9
	10, // R10
	11, // R11
}

// emitMovImm32SignExtToReg emits `mov reg, imm32-sign-extended` -- 7
// bytes for low regs, 8 bytes for high (R8-R15).
//
// Uses opcode 0xC7 /0 which sign-extends a 32-bit immediate to 64 bits.
// This is 3 bytes shorter than `mov reg, imm64` (0xB8+r + 8-byte imm).
// Adequate for our int args which fit in int32.
//
// Encoding for low regs (RAX/RCX/RDX/RBX/RSP/RBP/RSI/RDI):
//
//	48 C7 C0+r <imm32>   (7 bytes)   REX.W + C7 + ModRM(11 000 r)
//
// Encoding for high regs (R8-R15):
//
//	49 C7 C0+(r-8) <imm32>  (7 bytes)  REX.W|REX.B + C7 + ModRM
func emitMovImm32SignExtToReg(cb *codeBuf, reg uint8, imm int32) {
	rex := byte(0x48)
	if reg >= 8 {
		rex |= 0x01 // REX.B
	}
	modrm := byte(0xC0) | (reg & 0x7)
	cb.emit([]byte{
		rex, 0xC7, modrm,
		byte(uint32(imm)), byte(uint32(imm) >> 8),
		byte(uint32(imm) >> 16), byte(uint32(imm) >> 24),
	})
}

// emitMovR15ToReg emits `mov reg, r15` -- 3 bytes.
// Copies jitCtx from R15 into a scratch register (typically RAX for
// ABIInternal arg0). Encoding: 4C 89 (F8 + reg-low3) for RAX target.
// General form: 4C 89 ModRM where mod=11, reg=111 (R15), rm=target.
//
// For target reg < 8: 4C 89 (0xC0 | (7<<3) | rm) = 4C 89 (0xF8|rm)
// For target reg >= 8: 4D 89 (0xC0 | (7<<3) | (rm-8))
func emitMovR15ToReg(cb *codeBuf, dst uint8) {
	rex := byte(0x4C) // REX.W + REX.R (extends reg=R15)
	if dst >= 8 {
		rex |= 0x01 // REX.B
	}
	modrm := byte(0xC0) | (7 << 3) | (dst & 0x7) // reg=7 (R15 low 3)
	cb.emit([]byte{rex, 0x89, modrm})
}

// emitMovRbxImm64 emits `mov rbx, imm64` -- 10 bytes.
func emitMovRbxImm64(cb *codeBuf, imm uint64) {
	// 48 BB <imm64> = REX.W + B8+r (r=3=RBX)
	cb.emit([]byte{0x48, 0xBB})
	for i := 0; i < 8; i++ {
		cb.emit([]byte{byte(imm >> (8 * i))})
	}
}

// funcEntryPC returns the entry program counter of a Go function value.
// reflect.ValueOf(fn).Pointer() returns the funcval pointer in Go 1.17+;
// the entry PC is the first word of that struct.
//
// Kept for compat; per-shim helpers below use the direct unsafe idiom
// which the empirical helper e2e test proved works.
func funcEntryPC(fn interface{}) uint64 {
	fp := reflect.ValueOf(fn).Pointer()
	return uint64(*(*uintptr)(uintptrToPtr(fp)))
}

// uintptrToPtr is a helper to satisfy Go's unsafe rules for the funcval
// deref. It exists to keep the funcEntryPC body tidy.
func uintptrToPtr(u uintptr) unsafe.Pointer { return unsafe.Pointer(u) }

// shim*Addr helpers moved to shims.go (arch-neutral) so both amd64 and
// arm64 emit paths can reference the same names.

// emitCallShim emits the full sequence for calling a Go helper shim:
//  1. Copy R15 into RAX (arg0 = jitCtx)
//  2. Load each int32 arg into its ABIInternal register
//  3. Restore R14 = G (mov r14, [r15+savedGoGOff])
//  4. Load shimAddr into R10 (scratch, not an arg reg) and CALL
//  5. Reload RBX = vsBase from jitCtx (RBX was clobbered by ABIInternal)
//
// nArgs must be 0..7 (max helper arity in our shims). The returned
// status is in RAX after the call.
//
// **Register clobbers**: RAX/RBX/RCX/RDI/RSI/R8/R9/R10/R11 are clobbered
// by ABIInternal. R14 stays = G (Go preserves). R15 stays = jitCtx
// (Go preserves). RBX is reloaded to vsBase by emitReloadVsBase.
//
// Callers may not use RAX to hold a live value across the call (unless
// they save/restore it themselves) since RAX = return.
func emitCallShim(cb *codeBuf, shimAddr uint64, args []int32) {
	if len(args) > 8 {
		panic("emitCallShim: at most 8 int32 args supported")
	}
	// arg0 = jitCtx (from R15) → RAX
	emitMovR15ToReg(cb, 0)
	// arg1..argN
	for i, v := range args {
		reg := abiIntArgRegs[i+1]
		emitMovImm32SignExtToReg(cb, reg, v)
	}
	// Restore R14 = G before the Go call
	emitRestoreGoG(cb)
	// mov r10, shimAddr; call r10
	// 49 BA <imm64>
	cb.emit([]byte{0x49, 0xBA})
	for i := 0; i < 8; i++ {
		cb.emit([]byte{byte(shimAddr >> (8 * i))})
	}
	// call r10: 41 FF D2
	cb.emit([]byte{0x41, 0xFF, 0xD2})
	// Reload RBX = vsBase (ABIInternal clobbered it)
	emitReloadVsBase(cb)
}

// emitStatusCheckAndBubble emits the standard "if rax != 0 then ret" tail
// after a shim call. Used by ops that can raise (Arith, GetTable, etc.):
// on error the mmap segment returns non-zero to the trampoline, which
// bubbles up to Go.
//
// Sequence (10 bytes):
//
//	48 85 C0        ; test rax, rax
//	74 05           ; jz +5 (skip the ret block)
//	48 83 C8 01     ; or rax, 1  (normalize to 1 for status)
//	C3              ; ret
//
// Wait -- the "or rax, 1" step is only relevant if shims can return
// non-zero non-1 values. Our shim contract is 0/1 (or packed for Eq/
// Compare, handled separately). Keep it simple: propagate whatever
// rax holds.
//
//	48 85 C0        ; test rax, rax        (3 bytes)
//	74 01           ; jz +1                (2 bytes, skip ret)
//	C3              ; ret                  (1 byte)
//
// Total: 6 bytes.
func emitStatusCheckAndBubble(cb *codeBuf) {
	cb.emit([]byte{
		0x48, 0x85, 0xC0, // test rax, rax
		0x74, 0x01, // jz +1 (skip ret)
		0xC3, // ret
	})
}

// ---------------------------------------------------------------------
// Op emitters that use shim calls
// ---------------------------------------------------------------------

// emitRETURN emits the RETURN A B sequence via shimDoReturn:
//
//	<call shimDoReturn(base=0, pc=pc, a=a, b=b)>
//	ret     ; segment done, status in rax = 0
//
// **base handling**: shimDoReturn treats base as an int32 -- it's used
// only for tracing / pc anchoring. The mmap doesn't pass a runtime base;
// the host reads its own thread state to find the frame. We pass 0.
//
// pc: the source Proto pc of this RETURN, baked at compile time.
func emitRETURN(cb *codeBuf, pc int32, a, b uint8) {
	addr := shimDoReturnAddr()
	emitCallShim(cb, addr, []int32{0, pc, int32(a), int32(b)})
	emitRet(cb)
}

// emitGETUPVAL emits GETUPVAL A B via the exit-reason protocol
// (issue #38): pack (HelperGetUpval, a, b) into exitArg0, RET; Run's
// dispatcher does host.SetReg(a, host.GetUpval(base, b)) and reenters.
func emitGETUPVAL(cb *codeBuf, a, b uint8) {
	emitExitReason(cb, jit.HelperGetUpval, 0, int32(a), int32(b), 0)
}

// emitSETUPVAL emits SETUPVAL A B via the exit-reason protocol:
// Run's dispatcher does host.SetUpvalFromReg(base, a, b) and reenters.
func emitSETUPVAL(cb *codeBuf, a, b uint8) {
	emitExitReason(cb, jit.HelperSetUpval, 0, int32(a), int32(b), 0)
}
