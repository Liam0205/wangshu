//go:build wangshu_p4 && !amd64 && !arm64

// arch_other.go —— P4 PJ8 arch 路由非 amd64/arm64 build stub。
//
// 当前 P4 仅 amd64/arm64 双后端(承 06-backends.md §1)。其它 GOARCH(386/
// mips/riscv64 等)build 下提供编译期可见的 stub:archEmitLoadKReturn 返空
// → archMmapCode 返错(空段被 emitter_nonamd64.go 那条路径同款拒)→
// Compile 返 ErrCompileUnsupportedShape ⇒ TierStuck,行为等价 P1。
package jit

import "errors"

// archCodePage stub(空 struct 占位,跨 arch 编译期可见)。
type archCodePage struct{}

// Addr 占位返 0(永不真用)。
func (*archCodePage) Addr() uintptr { return 0 }

// Length 占位返 0。
func (*archCodePage) Length() int { return 0 }

// Munmap 占位 no-op。
func (*archCodePage) Munmap() error { return nil }

// archEmitLoadKReturn stub:返空 buf(MmapCode 见空段返错)。
func archEmitLoadKReturn(buf []byte, value uint64) []byte {
	_ = value
	return buf
}

// archMmapCode stub:返错(无 amd64/arm64 后端可用)。
func archMmapCode(code []byte) (*archCodePage, error) {
	_ = code
	return nil, errors.New("internal/gibbous/jit: P4 unsupported on this GOARCH (only amd64/arm64)")
}

// archCallJITFull stub:不应被调到(MmapCode 已返错让 Compile 拒)。防御
// 性 panic 让违约场景显式。
func archCallJITFull(codeAddr uintptr, jitCtxAddr uintptr) uint64 {
	_ = codeAddr
	_ = jitCtxAddr
	panic("internal/gibbous/jit: archCallJITFull called on unsupported GOARCH")
}

// archCallJITSpec stub:同 archCallJITFull。
func archCallJITSpec(codeAddr uintptr, jitCtxAddr uintptr, vsBase uintptr) uint64 {
	_ = codeAddr
	_ = jitCtxAddr
	_ = vsBase
	panic("internal/gibbous/jit: archCallJITSpec called on unsupported GOARCH")
}

// archSseOpForArith 其它 arch 不支持(sentinel 返 false)。
func archSseOpForArith(op uint8) (byte, bool) {
	_ = op
	return 0, false
}

// archEmitArithSpecBinopWithGuard 其它 arch 不支持。
func archEmitArithSpecBinopWithGuard(buf []byte, sseOp byte, a, b, c uint8, deoptCode uint64) []byte {
	_ = sseOp
	_ = a
	_ = b
	_ = c
	_ = deoptCode
	return buf
}

// archEmitArithSpecBinopRegKWithGuard 其它 arch 不支持。
func archEmitArithSpecBinopRegKWithGuard(buf []byte, sseOp byte, a, b uint8, kvalue, deoptCode uint64) []byte {
	_ = sseOp
	_ = a
	_ = b
	_ = kvalue
	_ = deoptCode
	return buf
}

// archEmitArithSpecChainKKWithGuard 其它 arch 不支持。
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

// archEmitForLoopEmptyConst 其它 arch 不支持。
func archEmitForLoopEmptyConst(buf []byte, kInit, kLimit, kStep uint64, preemptFlagOff int32) []byte {
	_ = kInit
	_ = kLimit
	_ = kStep
	_ = preemptFlagOff
	return buf
}

// archEmitForLoopRegLimit 其它 arch 不支持。
func archEmitForLoopRegLimit(buf []byte, kInit, kStep uint64, limitReg uint8, deoptCode uint64, preemptFlagOff int32) []byte {
	_ = kInit
	_ = kStep
	_ = limitReg
	_ = deoptCode
	_ = preemptFlagOff
	return buf
}

// archEmitForLoopWithBody 其它 arch 不支持。
func archEmitForLoopWithBody(buf []byte, kS, kInit, kLimit, kStep, kBody uint64,
	aS uint8, sseOp byte, preemptFlagOff int32) []byte {
	_ = kS
	_ = kInit
	_ = kLimit
	_ = kStep
	_ = kBody
	_ = aS
	_ = sseOp
	_ = preemptFlagOff
	return buf
}

// archEmitForLoopWithBody2 其它 arch 不支持。
func archEmitForLoopWithBody2(buf []byte, kS, kInit, kLimit, kStep, kBody1, kBody2 uint64,
	aS uint8, sseOp1, sseOp2 byte, preemptFlagOff int32) []byte {
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
	return buf
}

// archEmitGetTableArrayHit 其它 arch 不支持。
func archEmitGetTableArrayHit(buf []byte, aReg, bReg uint8, stableShape, stableIndex uint32, arenaBaseOff int32, deoptCode uint64) []byte {
	_ = aReg
	_ = bReg
	_ = stableShape
	_ = stableIndex
	_ = arenaBaseOff
	_ = deoptCode
	return buf
}

// archEmitGetTableNodeHit 其它 arch 不支持。
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

// archEmitSetTableArrayHit 其它 arch 不支持。
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

// archEmitSelfArrayHit 其它 arch 不支持。
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

// archEmitSetTableNodeHit 其它 arch 不支持。
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

// archEmitSelfNodeHit 其它 arch 不支持。
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

// archEmitSelfNodeHitNoRet 其它 arch 占位(archSupportsFrameInline=false 屏蔽)。
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

// archEmitSpecArgLoadK / archEmitSpecArgLoadReg 其它 arch stub。
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

// archSupportsSpec 其它 arch 不支持。
func archSupportsSpec() bool { return false }

// archSupportsForLoop 其它 arch 不支持(无 emitter)。
func archSupportsForLoop() bool { return false }

// archEmitHelperCall 其它 arch 不支持(无 emitter)。
func archEmitHelperCall(buf []byte, helperAddr uint64) []byte {
	_ = helperAddr
	return buf
}

// archEncodedHelperCallLen 其它 arch 占位 0(本 build 下不调到)。
const archEncodedHelperCallLen = 0

// archSupportsFrameInline 其它 arch 不支持(承 §9.20 Option B Spike 1)。
func archSupportsFrameInline() bool { return false }

// archEmitFrameInlineBuildVoid0ArgSkeleton 其它 arch 占位(本 build 下
// archSupportsFrameInline=false 屏蔽 Compile 路径不触达)。
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

// archEmitFrameInlinePopVoid0ArgSkeleton 其它 arch 占位。
func archEmitFrameInlinePopVoid0ArgSkeleton(buf []byte, ciDepthAddrOff int32) []byte {
	_ = ciDepthAddrOff
	return buf
}

// archEmitFrameInlineExitHelperRequest 其它 arch 占位。
func archEmitFrameInlineExitHelperRequest(buf []byte,
	exitReasonOff, exitArg0Off int32, helperCode uint64) []byte {
	_ = exitReasonOff
	_ = exitArg0Off
	_ = helperCode
	return buf
}

// archEncodedFrameInlineBuildVoid0ArgSkeletonLen 其它 arch 占位 0。
const archEncodedFrameInlineBuildVoid0ArgSkeletonLen = 0

// archEncodedFrameInlinePopVoid0ArgSkeletonLen 其它 arch 占位 0。
const archEncodedFrameInlinePopVoid0ArgSkeletonLen = 0

// archEncodedFrameInlineExitHelperRequestLen 其它 arch 占位 0。
const archEncodedFrameInlineExitHelperRequestLen = 0
