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

// archEmitForLoopWithBody2 arm64 端 stub。
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

// archEmitGetTableArrayHit arm64 端 PJ4 IC ArrayHit 字节级直达槽模板
// (168 字节,严密 IsTable guard + SIB 替代 + gen check + array 直达 +
// nil check + 写 R(A) + deopt block)。arm64 端代理
// jitarm64.EmitGetTableArrayHitArm64。
//
// **真接入 vs amd64 差异**:arm64 端经 trampoline_arm64.s 协议(x26=vsBase
// / x27=jitContext / x28=Go G / x14=arena base);模板因 SIB 替代(ADD+LDR
// 替代单条 SIB ldr)与 MOV imm64 序列(movz+movk×3)比 amd64 132 字节
// 长 36 字节。arenaBaseOff 签名 amd64 int32 而 arm64 uint16 因 arm64 LDR
// 用 unsigned 12-bit scaled offset(int32 安全转 uint16,jitContext 字段
// 偏移在数十字节量级)。
func archEmitGetTableArrayHit(buf []byte, aReg, bReg uint8, stableShape, stableIndex uint32, arenaBaseOff int32, deoptCode uint64) []byte {
	return jitarm64.EmitGetTableArrayHitArm64(buf, aReg, bReg,
		stableShape, stableIndex, uint16(arenaBaseOff), deoptCode)
}

// archEmitGetTableNodeHit arm64 端 PJ4 IC NodeHit 字节级直达槽模板
// (196 字节,IsTable guard + SIB + gen check + nodeRef + node[stableIndex]
// + key 比对 + NodeVal load + nil check + 写 R(A) + deopt block)。
func archEmitGetTableNodeHit(buf []byte, aReg, bReg uint8,
	stableShape, stableIndex uint32, stableKey uint64,
	arenaBaseOff int32, deoptCode uint64) []byte {
	return jitarm64.EmitGetTableNodeHitArm64(buf, aReg, bReg,
		stableShape, stableIndex, stableKey, uint16(arenaBaseOff), deoptCode)
}

// archEmitSetTableArrayHit arm64 端 PJ4 SETTABLE IC ArrayHit 字节级反向
// 写模板(144 字节)。
func archEmitSetTableArrayHit(buf []byte, aReg, cReg uint8,
	stableShape, stableIndex uint32, arenaBaseOff int32, deoptCode uint64) []byte {
	return jitarm64.EmitSetTableArrayHitArm64(buf, aReg, cReg,
		stableShape, stableIndex, uint16(arenaBaseOff), deoptCode)
}

// archEmitSelfArrayHit arm64 端 PJ4 SELF IC ArrayHit 字节级 inline 模板
// (172 字节,GETTABLE ArrayHit 168 + R(A+1) 拷段 4)。
func archEmitSelfArrayHit(buf []byte, aReg, bReg uint8,
	stableShape, stableIndex uint32, arenaBaseOff int32, deoptCode uint64) []byte {
	return jitarm64.EmitSelfArrayHitArm64(buf, aReg, bReg,
		stableShape, stableIndex, uint16(arenaBaseOff), deoptCode)
}

// archEmitSetTableNodeHit arm64 端 PJ4 SETTABLE IC NodeHit 字节级反向
// 写模板(172 字节)。
func archEmitSetTableNodeHit(buf []byte, aReg, cReg uint8,
	stableShape, stableIndex uint32, stableKey uint64,
	arenaBaseOff int32, deoptCode uint64) []byte {
	return jitarm64.EmitSetTableNodeHitArm64(buf, aReg, cReg,
		stableShape, stableIndex, stableKey, uint16(arenaBaseOff), deoptCode)
}

// archEmitSelfNodeHit arm64 端 PJ4 SELF IC NodeHit 字节级 inline 模板
// (200 字节,GETTABLE NodeHit 196 + R(A+1) 拷段 4)。
func archEmitSelfNodeHit(buf []byte, aReg, bReg uint8,
	stableShape, stableIndex uint32, stableKey uint64,
	arenaBaseOff int32, deoptCode uint64) []byte {
	return jitarm64.EmitSelfNodeHitArm64(buf, aReg, bReg,
		stableShape, stableIndex, stableKey, uint16(arenaBaseOff), deoptCode)
}

// archSupportsSpec arm64 端 PJ8 工程组件完整就绪但**端到端验证尚未通过**:
//   - ✅ PJ2 投机三形态字节级模板 byte-tested 完整(reg-reg 108B + reg-K 92B
//   - chain-KK 116B,13 字节级单测覆盖)
//   - ✅ archEmitArithSpec 三 stub 真代理(sseOp 翻译 amd64→arm64)
//   - ✅ archCallJITSpec arm64 spec trampoline asm 实装(framesize $80-32,
//     装 x26=vsBase + x27=jitCtx + BL + LDP,trampoline_arm64.s::callJITSpec)
//   - ✅ trampoline_other.go cross-build stub(darwin/arm64 等 panic on call)
//   - ⏳ 物理 self-hosted runner 端到端 V1-V22 验证(QEMU 不真模拟 i-cache
//   - PROT_EXEC,本地翻 true 后端到端崩高风险)
//
// **翻 true 前置条件**(留 PJ8+ 同批落地):
//  1. 物理 arm64 self-hosted runner CI 接入(真 mmap + PROT_EXEC + 真
//     i-cache + 真 d-cache flush)
//  2. CI test-arm64-physical 跑 main 包测试(经 Compile 派发 + archCall*
//     路径)+ crescent e2e WarmupThenForce + V1-V22 byte-equal P1
//  3. 翻 true 后启用范围:Compile 端 arm64 上 PJ2 投机模板 + PJ3 FORLOOP
//     body/body2/RegLimit 三路径(经 archCallJITSpec)+ PJ4 IC 六模板
//     (经 archCallJITFull,本就启用)
//
// **当前状态**:仍返 false,Compile 端 PJ2 投机 + PJ3 body/body2/RegLimit
// 路径**编译期返 ErrCompileUnsupportedShape**(承本会话 review 22 ℹ️ 遗留
// fix),Tier 框架退回 P1 解释器,行为等价 byte-equal P1。
func archSupportsSpec() bool { return false }
