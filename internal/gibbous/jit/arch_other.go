//go:build wangshu_p4 && !amd64 && !arm64

// arch_other.go — P4 PJ8 arch-routing build stub for non-amd64/arm64.
//
// P4 currently ships only the amd64/arm64 dual backends (per 06-backends.md §1).
// On other GOARCH builds (386/mips/riscv64 etc.) this provides compile-time
// visible stubs: archEmitLoadKReturn returns an empty buffer → archMmapCode
// returns an error (the empty segment is rejected the same way as the
// emitter_nonamd64.go path) → Compile returns ErrCompileUnsupportedShape ⇒
// TierStuck, behaving the same as P1.
package jit

import "errors"

// archCodePage stub (empty struct placeholder, compile-time visible across archs).
type archCodePage struct{}

// Addr placeholder returns 0 (never actually used).
func (*archCodePage) Addr() uintptr { return 0 }

// Length placeholder returns 0.
func (*archCodePage) Length() int { return 0 }

// Munmap placeholder no-op.
func (*archCodePage) Munmap() error { return nil }

// archEmitLoadKReturn stub: returns an empty buf (MmapCode errors on empty segment).
func archEmitLoadKReturn(buf []byte, value uint64) []byte {
	_ = value
	return buf
}

// archMmapCode stub: returns an error (no amd64/arm64 backend available).
func archMmapCode(code []byte) (*archCodePage, error) {
	_ = code
	return nil, errors.New("internal/gibbous/jit: P4 unsupported on this GOARCH (only amd64/arm64)")
}

// archCallJITFull stub: should never be reached (MmapCode already errors so
// Compile rejects). Defensive panic makes contract violations explicit.
func archCallJITFull(codeAddr uintptr, jitCtxAddr uintptr) uint64 {
	_ = codeAddr
	_ = jitCtxAddr
	panic("internal/gibbous/jit: archCallJITFull called on unsupported GOARCH")
}

// archCallJITSpec stub: same as archCallJITFull.
func archCallJITSpec(codeAddr uintptr, jitCtxAddr uintptr, vsBase uintptr) uint64 {
	_ = codeAddr
	_ = jitCtxAddr
	_ = vsBase
	panic("internal/gibbous/jit: archCallJITSpec called on unsupported GOARCH")
}

// archSseOpForArith not supported on other archs (sentinel returns false).
func archSseOpForArith(op uint8) (byte, bool) {
	_ = op
	return 0, false
}

// archEmitArithSpecBinopWithGuard not supported on other archs.
func archEmitArithSpecBinopWithGuard(buf []byte, sseOp byte, a, b, c uint8, deoptCode uint64) []byte {
	_ = sseOp
	_ = a
	_ = b
	_ = c
	_ = deoptCode
	return buf
}

// archEmitArithSpecBinopRegKWithGuard not supported on other archs.
func archEmitArithSpecBinopRegKWithGuard(buf []byte, sseOp byte, a, b uint8, kvalue, deoptCode uint64) []byte {
	_ = sseOp
	_ = a
	_ = b
	_ = kvalue
	_ = deoptCode
	return buf
}

// archEmitArithSpecChainKKWithGuard not supported on other archs.
func archEmitArithSpecChainKKWithGuard(buf []byte, sseOp1, sseOp2 byte, a, b uint8, k1value, k2value, deoptCode uint64) []byte {
	_ = sseOp1
	_ = sseOp2
	_ = a
	_ = b
	_ = k1value
	_ = k2value
	_ = deoptCode
	return buf
}

// archEmitForLoopEmptyConst not supported on other archs.
func archEmitForLoopEmptyConst(buf []byte, kInit, kLimit, kStep uint64,
	preemptFlagOff, loopFuelOff, loopSpillOff int32, loopFuelCode uint64) ([]byte, int) {
	_ = kInit
	_ = kLimit
	_ = kStep
	_ = preemptFlagOff
	_ = loopFuelOff
	_ = loopSpillOff
	_ = loopFuelCode
	return buf, 0
}

// archEmitForLoopRegLimit not supported on other archs.
func archEmitForLoopRegLimit(buf []byte, kInit, kStep uint64, limitReg uint8, deoptCode uint64,
	preemptFlagOff, loopFuelOff, loopSpillOff int32, loopFuelCode uint64) ([]byte, int) {
	_ = kInit
	_ = kStep
	_ = limitReg
	_ = deoptCode
	_ = preemptFlagOff
	_ = loopFuelOff
	_ = loopSpillOff
	_ = loopFuelCode
	return buf, 0
}

// archEmitForLoopWithBody not supported on other archs.
func archEmitForLoopWithBody(buf []byte, kS, kInit, kLimit, kStep, kBody uint64,
	aS uint8, sseOp byte,
	preemptFlagOff, loopFuelOff, loopSpillOff int32, loopFuelCode uint64) ([]byte, int) {
	_ = kS
	_ = kInit
	_ = kLimit
	_ = kStep
	_ = kBody
	_ = aS
	_ = sseOp
	_ = preemptFlagOff
	_ = loopFuelOff
	_ = loopSpillOff
	_ = loopFuelCode
	return buf, 0
}

// archEmitForLoopWithBody2 not supported on other archs.
func archEmitForLoopWithBody2(buf []byte, kS, kInit, kLimit, kStep, kBody1, kBody2 uint64,
	aS uint8, sseOp1, sseOp2 byte,
	preemptFlagOff, loopFuelOff, loopSpillOff int32, loopFuelCode uint64) ([]byte, int) {
	_ = kS
	_ = kInit
	_ = kLimit
	_ = kStep
	_ = kBody1
	_ = kBody2
	_ = aS
	_ = sseOp1
	_ = sseOp2
	_ = preemptFlagOff
	_ = loopFuelOff
	_ = loopSpillOff
	_ = loopFuelCode
	return buf, 0
}

// archEmitGetTableArrayHit not supported on other archs.
func archEmitGetTableArrayHit(buf []byte, aReg, bReg uint8, stableShape, stableIndex uint32, arenaBaseOff int32, deoptCode uint64) []byte {
	_ = aReg
	_ = bReg
	_ = stableShape
	_ = stableIndex
	_ = arenaBaseOff
	_ = deoptCode
	return buf
}

// archEmitGetTableNodeHit not supported on other archs.
func archEmitGetTableNodeHit(buf []byte, aReg, bReg uint8,
	stableShape, stableIndex uint32, stableKey uint64,
	arenaBaseOff int32, deoptCode uint64) []byte {
	_ = aReg
	_ = bReg
	_ = stableShape
	_ = stableIndex
	_ = stableKey
	_ = arenaBaseOff
	_ = deoptCode
	return buf
}

// archEmitSetTableArrayHit not supported on other archs.
func archEmitSetTableArrayHit(buf []byte, aReg, cReg uint8,
	stableShape, stableIndex uint32, arenaBaseOff int32, deoptCode uint64) []byte {
	_ = aReg
	_ = cReg
	_ = stableShape
	_ = stableIndex
	_ = arenaBaseOff
	_ = deoptCode
	return buf
}

// archEmitSelfArrayHit not supported on other archs.
func archEmitSelfArrayHit(buf []byte, aReg, bReg uint8,
	stableShape, stableIndex uint32, arenaBaseOff int32, deoptCode uint64) []byte {
	_ = aReg
	_ = bReg
	_ = stableShape
	_ = stableIndex
	_ = arenaBaseOff
	_ = deoptCode
	return buf
}

// archEmitSetTableNodeHit not supported on other archs.
func archEmitSetTableNodeHit(buf []byte, aReg, cReg uint8,
	stableShape, stableIndex uint32, stableKey uint64,
	arenaBaseOff int32, deoptCode uint64) []byte {
	_ = aReg
	_ = cReg
	_ = stableShape
	_ = stableIndex
	_ = stableKey
	_ = arenaBaseOff
	_ = deoptCode
	return buf
}

// archEmitSelfNodeHit not supported on other archs.
func archEmitSelfNodeHit(buf []byte, aReg, bReg uint8,
	stableShape, stableIndex uint32, stableKey uint64,
	arenaBaseOff int32, deoptCode uint64) []byte {
	_ = aReg
	_ = bReg
	_ = stableShape
	_ = stableIndex
	_ = stableKey
	_ = arenaBaseOff
	_ = deoptCode
	return buf
}

// archEmitSelfNodeHitNoRet placeholder on other archs (masked by archSupportsFrameInline=false).
func archEmitSelfNodeHitNoRet(buf []byte, aReg, bReg uint8,
	stableShape, stableIndex uint32, stableKey uint64,
	arenaBaseOff int32, deoptCode uint64) []byte {
	_ = aReg
	_ = bReg
	_ = stableShape
	_ = stableIndex
	_ = stableKey
	_ = arenaBaseOff
	_ = deoptCode
	return buf
}

// archEmitSpecArgLoadK / archEmitSpecArgLoadReg stubs on other archs.
func archEmitSpecArgLoadK(buf []byte, dstReg uint8, k uint64) []byte {
	_ = dstReg
	_ = k
	return buf
}
func archEmitSpecArgLoadReg(buf []byte, dstReg uint8, srcReg uint8) []byte {
	_ = dstReg
	_ = srcReg
	return buf
}

// archSupportsSpec not supported on other archs.
func archSupportsSpec() bool { return false }

// archSupportsForLoop not supported on other archs (no emitter).
func archSupportsForLoop() bool { return false }

// archEmitHelperCall not supported on other archs (no emitter).
func archEmitHelperCall(buf []byte, helperAddr uint64) []byte {
	_ = helperAddr
	return buf
}

// archEncodedHelperCallLen placeholder 0 on other archs (not reached in this build).
const archEncodedHelperCallLen = 0

// archSupportsFrameInline not supported on other archs (per §9.20 Option B Spike 1).
func archSupportsFrameInline() bool { return false }

// archEmitFrameInlineBuildVoid0ArgSkeleton placeholder on other archs (in this
// build archSupportsFrameInline=false masks it so the Compile path never reaches it).
func archEmitFrameInlineBuildVoid0ArgSkeleton(buf []byte,
	ciDepthAddrOff, ciSegBaseAddrOff int32, callARecv uint8,
	w0, w1, w2, w4 uint64) []byte {
	_ = ciDepthAddrOff
	_ = ciSegBaseAddrOff
	_ = callARecv
	_ = w0
	_ = w1
	_ = w2
	_ = w4
	return buf
}

// archEmitFrameInlinePopVoid0ArgSkeleton placeholder on other archs.
func archEmitFrameInlinePopVoid0ArgSkeleton(buf []byte, ciDepthAddrOff int32) []byte {
	_ = ciDepthAddrOff
	return buf
}

// archEmitFrameInlineExitHelperRequest placeholder on other archs.
func archEmitFrameInlineExitHelperRequest(buf []byte,
	exitReasonOff, exitArg0Off int32, helperCode uint64) []byte {
	_ = exitReasonOff
	_ = exitArg0Off
	_ = helperCode
	return buf
}

// archEncodedFrameInlineBuildVoid0ArgSkeletonLen placeholder 0 on other archs.
const archEncodedFrameInlineBuildVoid0ArgSkeletonLen = 0

// archEncodedFrameInlinePopVoid0ArgSkeletonLen placeholder 0 on other archs.
const archEncodedFrameInlinePopVoid0ArgSkeletonLen = 0

// archEncodedFrameInlineExitHelperRequestLen placeholder 0 on other archs.
const archEncodedFrameInlineExitHelperRequestLen = 0
