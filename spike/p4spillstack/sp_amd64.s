// sp_amd64.s — return the caller's stack pointer for stack-identity asserts.

#include "textflag.h"

// func currentSP() uintptr
TEXT ·currentSP(SB),NOSPLIT,$0-8
	MOVQ SP, AX
	MOVQ AX, ret+0(FP)
	RET
