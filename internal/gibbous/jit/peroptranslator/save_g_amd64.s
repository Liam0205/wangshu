// save_g_amd64.s — save Go G register (R14) into a caller-supplied slot.
//
// Called from Go just before entering the P4 mmap segment. The emit path
// then loads R14 from this slot right before calling a Go helper, so the
// helper sees the correct G in R14 — Go's ABIInternal expects R14 = G
// on amd64 for stack-check preamble / morestack / getg / etc.
//
// Rationale: PJ0-PJ9 mmap emit does NOT call Go helpers (all Go-side work
// happens in the Go-side Run wrapper after mmap returns). PJ10 native
// wants to inline helper calls from mmap for hot ops (arithmetic slow
// path, RETURN, safepoint check). The trampoline already saves R14 on
// mmap entry so mmap can freely clobber it, but calling into Go after
// clobbering R14 crashes: Go's function prologue reads R14 = G to check
// stack overflow and reload TLS.
//
// This asm helper writes the caller's R14 (still = G, because it's called
// from ordinary Go code before the trampoline PUSHQ R14) into
// *(uintptr)(saveSlot). The emit path then before each helper call:
//   mov r14, [r15 + savedGoGOffset]
// restores G, calls the helper, and R14 stays = G on return (Go
// preserves R14 across ABIInternal calls).

//go:build wangshu_p4 && linux && amd64

#include "textflag.h"

// func saveGoG(slot *uintptr)
//
// Writes the current R14 (Go G on amd64 ABIInternal) into *slot.
// Called from ordinary Go code, so R14 = current G on entry.
TEXT ·saveGoG(SB), NOSPLIT|NOFRAME, $0-8
	MOVQ slot+0(FP), AX
	MOVQ R14, (AX)
	RET
