//go:build wangshu_p4 && arm64

// arch_arm64.go —— P4 PJ8 arch 路由 arm64 实装(对位 arch_amd64.go)。
//
// arm64 端 mmap+W^X+icache flush + trampoline + emitter 同款形态,经
// jitarm64 包提供。
//
// 承 docs/design/p4-method-jit/06-backends.md §4.2 arm64 寄存器约定
// (X28=Go G 不动 / X27 装 jitContext / X0 返回值)。
package jit

import (
	jitarm64 "github.com/Liam0205/wangshu/internal/gibbous/jit/arm64"
)

// archCodePage 是 arch 抽象的可执行段——本 build 下别名 jitarm64.CodePage。
type archCodePage = jitarm64.CodePage

// archEmitLoadKReturn 发射「mov X0, value; ret」直线模板(arm64 端 20 字节:
// movz+movk×3 共 16 字节 + ret 4 字节)。
//
// 常量族烧 NaN-box value;prelude/比较折叠族 X0 是 dummy(由 Run 端忽略)。
func archEmitLoadKReturn(buf []byte, value uint64) []byte {
	buf = jitarm64.EmitMovX0Imm64(buf, value)
	buf = jitarm64.EmitRet(buf)
	return buf
}

// archMmapCode 把 code 写入 W^X 段 + icache flush。
func archMmapCode(code []byte) (*archCodePage, error) {
	return jitarm64.MmapCode(code)
}

// archCallJITFull 跳进 mmap 段(arm64 trampoline:保存 callee-saved X19-X30 +
// 装 jitContext 到 X27 + BLR + 恢复)。返 X0。
func archCallJITFull(codeAddr uintptr, jitCtxAddr uintptr) uint64 {
	return jitarm64.CallJITFull(codeAddr, jitCtxAddr)
}

// archCallJITSpec arm64 端 PJ2 投机模板 stub——arm64 spec trampoline
// 留 PJ8+(对位 amd64 callJITSpec 同款形态:装 X27=jitContext + X28
// (or X26)=valueStackBase + BLR + 恢复)。当前 arm64 build 不调到 spec
// (Compile 不发 useSpec=true 给 arm64;留 PJ8+ 完整版同批)。
func archCallJITSpec(codeAddr uintptr, jitCtxAddr uintptr, vsBase uintptr) uint64 {
	_ = codeAddr
	_ = jitCtxAddr
	_ = vsBase
	panic("internal/gibbous/jit/arm64: archCallJITSpec not implemented (PJ8+ arm64 spec trampoline)")
}

// archEmitArithSpecAddWithGuard arm64 端 stub——arm64 spec 模板 codegen
// 留 PJ8+(对位 amd64 EmitArithSpeculativeAddWithGuard 同款形态:cmp
// + b.hs deopt + fmov + fadd + fmov + ret + deopt block)。
func archEmitArithSpecAddWithGuard(buf []byte, a, b, c uint8, deoptCode uint64) []byte {
	_ = a
	_ = b
	_ = c
	_ = deoptCode
	return buf // 空 buf → MmapCode 返错 → Compile 拒,Compile 路径会 fallback 到 host helper
}

// archSseOpForArith arm64 端 stub——arm64 不用 SSE op 字节(用 fadd/fsub/
// fmul/fdiv aarch64 指令,留 PJ8+ 完整版独立路径)。当前 archSupportsSpec
// 返 false,本函数不会被调用——sentinel 返 (0, false) 保底。
func archSseOpForArith(op uint8) (byte, bool) {
	_ = op
	return 0, false
}

// archEmitArithSpecBinopWithGuard arm64 端 stub——同 archEmitArithSpec
// AddWithGuard,留 PJ8+。
func archEmitArithSpecBinopWithGuard(buf []byte, sseOp byte, a, b, c uint8, deoptCode uint64) []byte {
	_ = sseOp
	_ = a
	_ = b
	_ = c
	_ = deoptCode
	return buf
}

// archEmitArithSpecBinopRegKWithGuard arm64 端 stub——留 PJ8+(对位 amd64
// reg-K 形态:fmov + cmp + b.hs deopt + fadd const + 写回 + ret)。
func archEmitArithSpecBinopRegKWithGuard(buf []byte, sseOp byte, a, b uint8, kvalue, deoptCode uint64) []byte {
	_ = sseOp
	_ = a
	_ = b
	_ = kvalue
	_ = deoptCode
	return buf
}

// archEmitArithSpecChainKKWithGuard arm64 端 stub。
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

// archEmitForLoopEmptyConst arm64 端 stub——留 PJ8+(对位 amd64 FORLOOP
// 模板:fmov + fcmpe + b.gt / b 等)。
func archEmitForLoopEmptyConst(buf []byte, kInit, kLimit, kStep uint64, preemptFlagOff int32) []byte {
	_ = kInit
	_ = kLimit
	_ = kStep
	_ = preemptFlagOff
	return buf
}

// archEmitForLoopRegLimit arm64 端 stub。
func archEmitForLoopRegLimit(buf []byte, kInit, kStep uint64, limitReg uint8, deoptCode uint64, preemptFlagOff int32) []byte {
	_ = kInit
	_ = kStep
	_ = limitReg
	_ = deoptCode
	_ = preemptFlagOff
	return buf
}

// archEmitForLoopWithBody arm64 端 stub——留 PJ8+。
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

// archSupportsSpec arm64 当前不支持(留 PJ8+)。
func archSupportsSpec() bool { return false }
