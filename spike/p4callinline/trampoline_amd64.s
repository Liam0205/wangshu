// trampoline_amd64.s — issue #50 spike: Go → mmap segment round trip
// with three scratch registers pre-loaded (RCX / RDX / R8).
//
// Mirrors spike/p4tramp's CallJIT but passes segment arguments the way
// the production spec trampoline does (registers loaded before CALL).
// NOSPLIT|NOFRAME: no Go frame, no morestack window; the segments this
// spike runs are short straight-line code (~ns) so async preemption
// landing inside the segment is effectively impossible (same rationale
// as p4tramp).

//go:build linux && amd64

#include "textflag.h"

// func CallSeg(codeAddr, rcx, rdx, r8 uintptr) uint64
TEXT ·CallSeg(SB),NOSPLIT|NOFRAME,$0-40
	MOVQ rcx+8(FP), CX
	MOVQ rdx+16(FP), DX
	MOVQ r8+24(FP), R8
	MOVQ codeAddr+0(FP), AX
	CALL AX
	MOVQ AX, ret+32(FP)
	RET
