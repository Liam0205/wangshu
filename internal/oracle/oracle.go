//go:build wangshu_oracle_cgo && cgo

// Package oracle embeds the official Lua 5.1.5 library (vendored under
// _lua515/, MIT) behind a cgo shim so differential fuzzing can compare
// wangshu against the reference implementation IN PROCESS -- go-fuzz
// exec rates (tens of thousands/sec) cannot afford the fork-per-script
// oracle model used by test/difftest.
//
// Build gating: the package only builds under
// `-tags wangshu_oracle_cgo` with CGO_ENABLED=1. The default build --
// and every existing build variant -- stays zero-cgo (same isolation
// discipline as internal/gibbous/jit/arm64/jitcgo).
//
// Each Exec creates a fresh lua_State and closes it before returning:
// no state leaks between fuzz inputs. Three shim-side guards bound
// hostile inputs (capped allocator, instruction-count hook, prelude
// output cap); all three surface as VerdictLimit, which callers treat
// as "not comparable, skip" rather than as a divergence.
package oracle

/*
// LUA_USE_POSIX (not LUA_USE_LINUX/MACOSX): the platform macros pull
// in READLINE (extra headers, standalone-only) and DLOPEN (dynamic C
// module loading -- an ability surface the oracle must NOT have).
// Plain POSIX gives mkstemp/isatty/popen/ulongjmp; loadlib.c falls
// back to its "dynamic libraries not enabled" stub, which is exactly
// the sandboxing we want.
#cgo CFLAGS: -O2 -DLUA_USE_POSIX
#cgo LDFLAGS: -lm

#include <stdlib.h>
#include "shim.h"
*/
import "C"

import "unsafe"

// Verdict classifies one oracle execution. Values mirror the
// WANGSHU_ORACLE_* codes in shim.h.
type Verdict int

const (
	// VerdictOK: the script ran to completion; Output is valid.
	VerdictOK Verdict = 0
	// VerdictError: compile or runtime error; Err carries the message,
	// Output carries whatever was printed before the error.
	VerdictError Verdict = 1
	// VerdictLimit: a shim-imposed resource limit fired (allocation
	// cap, instruction budget, output cap) or the prelude itself
	// failed. The run is NOT comparable; callers must skip.
	VerdictLimit Verdict = 2
)

// Result is the outcome of one Exec.
type Result struct {
	Verdict      Verdict
	Output       string // captured print/io.write bytes (via the prelude)
	KnownNaNSign bool   // output path observed a NaN spelling
	Err          string // error message (VerdictError / VerdictLimit)
}

// Limits bounds one Exec against hostile fuzz inputs.
type Limits struct {
	// MaxAllocBytes caps the total bytes the Lua allocator may hold
	// live at once. Zero means DefaultMaxAllocBytes.
	MaxAllocBytes uint
	// Budget caps executed VM instructions (LUA_MASKCOUNT hook).
	// Zero means DefaultBudget; negative disables (tests only).
	Budget int
}

const (
	// DefaultMaxAllocBytes (64 MiB) is far above what a 4 KiB fuzz
	// script legitimately needs, far below what stalls a CI runner.
	DefaultMaxAllocBytes = 64 << 20
	// DefaultBudget (50M instructions) bounds runaway loops at
	// roughly tens of milliseconds of native PUC execution.
	DefaultBudget = 50_000_000
)

// Exec runs prelude then src on a fresh official-Lua state.
//
// The prelude is trusted harness code (output capture, stdlib trim,
// determinism stubs -- see prelude.go); it runs before the instruction
// budget is armed. src is the untrusted fuzz input.
func Exec(src, prelude string, lim Limits) Result {
	maxAlloc := lim.MaxAllocBytes
	if maxAlloc == 0 {
		maxAlloc = DefaultMaxAllocBytes
	}
	budget := lim.Budget
	if budget == 0 {
		budget = DefaultBudget
	}
	if budget < 0 {
		budget = 0 // shim: <=0 disables the hook
	}

	// C.CBytes-free zero-copy view: pass Go string pointers directly;
	// the shim only reads them during the call (luaL_loadbuffer
	// copies), so no allocation or pinning games are needed beyond
	// what cgo already guarantees for call duration.
	var cSrc, cPrelude *C.char
	if len(src) > 0 {
		cSrc = (*C.char)(unsafe.Pointer(unsafe.StringData(src)))
	} else {
		cSrc = (*C.char)(unsafe.Pointer(&zeroByte))
	}
	if len(prelude) > 0 {
		cPrelude = (*C.char)(unsafe.Pointer(unsafe.StringData(prelude)))
	} else {
		cPrelude = (*C.char)(unsafe.Pointer(&zeroByte))
	}

	var out, errMsg *C.char
	var outLen, errLen C.size_t
	v := C.wangshu_oracle_exec(
		cSrc, C.size_t(len(src)),
		cPrelude, C.size_t(len(prelude)),
		C.size_t(maxAlloc), C.int(budget),
		&out, &outLen, &errMsg, &errLen,
	)

	res := Result{Verdict: Verdict(v)}
	if out != nil {
		readout := C.GoStringN(out, C.int(outLen))
		C.wangshu_oracle_free(out)
		var ok bool
		res.Output, res.KnownNaNSign, ok = DecodeOutput(readout)
		if !ok {
			res.Verdict = VerdictLimit
			res.Err = "oracle-harness: invalid readout"
		}
	}
	if errMsg != nil {
		res.Err = C.GoStringN(errMsg, C.int(errLen))
		C.wangshu_oracle_free(errMsg)
	}
	return res
}

// zeroByte backs the empty-string case: cgo rejects nil for a
// const char* that C dereference-guards anyway, so hand it a real
// (never-read, len 0) byte.
var zeroByte byte
