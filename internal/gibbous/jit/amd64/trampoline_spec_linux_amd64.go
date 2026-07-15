//go:build wangshu_p4 && linux && amd64

package amd64

// callJITSpec is the dedicated trampoline for the P4 PJ2 speculative template:
// besides loading r15=jitContext, it also loads rbx=valueStackBase so that
// in-segment byte-level codegen (EmitArithSpeculativeAdd, etc.) can read/write
// value-stack slots directly via rbx+disp32.
//
// Arguments:
//
//	codeAddr  ← mmap segment start (PROT_RX)
//	jitCtx    ← *JITContext cast to uintptr (via unsafe.Pointer)
//	vsBase    ← valueStackBase (arena.Words start + base*8)
//
// Implementation: trampoline_spec_amd64.s::callJITSpec (NOSPLIT|NOFRAME).
//
//go:noescape
func callJITSpec(codeAddr uintptr, jitCtx uintptr, vsBase uintptr) uint64

// CallJITSpec is the exported wrapper around callJITSpec.
func CallJITSpec(codeAddr uintptr, jitCtx uintptr, vsBase uintptr) uint64 {
	return callJITSpec(codeAddr, jitCtx, vsBase)
}
