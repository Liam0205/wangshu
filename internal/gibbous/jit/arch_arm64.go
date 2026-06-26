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

// arenaBaseOffArm64 验 arenaBaseOff(jitContext 字段 byte 偏移)在 arm64
// LDR scaled offset unsigned 12-bit + 8 字节对齐范围内,溢出 panic
// (结构性前提硬化为运行期不变式)。
//
// arm64 `LDR Xt, [Xn, #pimm12]` 接收的是 8 字节 scaled offset(实际 byteOff =
// pimm12 * 8,范围 [0, 32760]),`EmitLdrXtFromXnDisp` 对非法值静默兜底
// byteOff=0 → 静默读 [x27+0] 误命中错字段。本 helper 把检查从注释提升到
// 运行期 panic,防 JITContext 未来字段重排把 arenaBase 推到 ≥32760 时静默
// 失效。
//
// 当前 JITContextArenaBaseOffset = 0(arenaBase 是 JITContext 首字段),不可达;
// 留作未来加固。
func arenaBaseOffArm64(arenaBaseOff int32) uint16 {
	if arenaBaseOff < 0 || arenaBaseOff > 32760 || arenaBaseOff%8 != 0 {
		panic("internal/gibbous/jit/arm64: arenaBaseOff out of range or not 8-byte aligned")
	}
	return uint16(arenaBaseOff)
}

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

// arm64ArithOpSelForSseOp 把 amd64 SSE opcode 字节(F2 0F xx ModRM 的 xx)
// 翻译到 arm64 PJ2 投机模板的 opSel 字节(用于
// EmitArithSpeculativeBinopWithGuardArm64)。承同 jitarm64.ArithOp*Arm64
// 常量定义。
//
//	0x58 ADDSD → ArithOpAddArm64 (0x28)
//	0x5C SUBSD → ArithOpSubArm64 (0x38)
//	0x59 MULSD → ArithOpMulArm64 (0x08)
//	0x5E DIVSD → ArithOpDivArm64 (0x18)
//
// 返回 (opSel, true) 若匹配,(0, false) 若未识别(caller 应静默放弃)。
func arm64ArithOpSelForSseOp(sseOp byte) (uint8, bool) {
	switch sseOp {
	case 0x58: // ADDSD
		return jitarm64.ArithOpAddArm64, true
	case 0x5C: // SUBSD
		return jitarm64.ArithOpSubArm64, true
	case 0x59: // MULSD
		return jitarm64.ArithOpMulArm64, true
	case 0x5E: // DIVSD
		return jitarm64.ArithOpDivArm64, true
	default:
		return 0, false
	}
}

// archEmitArithSpecBinopWithGuard arm64 端 PJ2 投机 reg-reg 模板真接入
// (108 字节,对位 amd64 EmitArithSpeculativeBinopWithGuard 92 字节;
// arm64 因 RISC fixed-length 多 16 字节)。
//
// **接入路径未通**(本批仅暴露字节级模板代理,Compile 派发仍由
// archSupportsSpec()=false 阻止):
//   - 真启用需 archCallJITSpec arm64 spec trampoline 真实现 +
//     archSupportsSpec() 翻 true(留 PJ8+ 同批);
//   - 字节级模板与单测均已落地,本批纯接线降低未来真接入工程量。
//
// sseOp 自动翻译:0x58/0x5C/0x59/0x5E → arm64 ArithOpAdd/Sub/Mul/Div;
// 未识别 op 静默返原 buf(对位 amd64 stub 同款放弃语义)。
func archEmitArithSpecBinopWithGuard(buf []byte, sseOp byte, a, b, c uint8, deoptCode uint64) []byte {
	opSel, ok := arm64ArithOpSelForSseOp(sseOp)
	if !ok {
		return buf
	}
	return jitarm64.EmitArithSpeculativeBinopWithGuardArm64(buf, opSel, a, b, c, deoptCode)
}

// archEmitArithSpecBinopRegKWithGuard arm64 端 stub——arm64 端 reg-K
// 形态模板字节级尚未落地(留 PJ8+ 与 spec trampoline 同批,模板形态
// 见 amd64 EmitArithSpeculativeBinopRegKWithGuard:fmov 装 K + cmp +
// b.hs deopt + fadd const + 写回 + ret)。
func archEmitArithSpecBinopRegKWithGuard(buf []byte, sseOp byte, a, b uint8, kvalue, deoptCode uint64) []byte {
	_ = sseOp
	_ = a
	_ = b
	_ = kvalue
	_ = deoptCode
	return buf
}

// archEmitArithSpecChainKKWithGuard arm64 端 stub——arm64 端 chain-KK
// 形态模板字节级尚未落地(留 PJ8+ 同批)。
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

// archEmitForLoopEmptyConst arm64 端 PJ3 FORLOOP 空 body 模板真接入
// (84 字节无 safepoint / 92 字节含 safepoint;preemptFlagOff < 0 跳
// safepoint,>= 0 启用)。对位 amd64 EmitForLoopEmptyConst 69/83 字节,
// arm64 因 MOV imm64 序列 16B vs amd64 movq 15B 累积 + RISC fixed-length
// 共多 15 字节。
//
// **接入路径**:Compile 主路径不经 spec trampoline,直接经 callJITFull
// 路径调本模板;不依赖 archSupportsSpec。真 mmap+RX 端到端等物理 runner。
func archEmitForLoopEmptyConst(buf []byte, kInit, kLimit, kStep uint64, preemptFlagOff int32) []byte {
	return jitarm64.EmitForLoopEmptyConstArm64(buf, kInit, kLimit, kStep, preemptFlagOff)
}

// archEmitForLoopRegLimit arm64 端 PJ3 FORLOOP reg-limit 模板真接入
// (120 字节无 safepoint / 128 字节含 safepoint)。对位 amd64
// EmitForLoopRegLimit 103/117 字节,arm64 多 17/11 字节(MOV imm64 序列
// 16B vs amd64 movq 15B 累积 + RISC fixed-length)。
//
// guard:LDR R(limitReg) → CMP qNanBoxBase → B.HS deopt(若非 number)。
func archEmitForLoopRegLimit(buf []byte, kInit, kStep uint64, limitReg uint8, deoptCode uint64, preemptFlagOff int32) []byte {
	return jitarm64.EmitForLoopRegLimitArm64(buf, kInit, kStep, limitReg, deoptCode, preemptFlagOff)
}

// archEmitForLoopWithBody arm64 端 PJ3 FORLOOP body 含 reg-K op 形态
// 模板真接入(144 字节无 safepoint / 152 字节含 safepoint)。对位 amd64
// EmitForLoopWithRegKBody 121/135 字节,arm64 多 23/17 字节(MOV imm64
// 序列 + FMOV 中转 GP↔FP)。
//
// sseOp 自动翻译:0x58/0x5C/0x59/0x5E → arm64 FADD/FSUB/FMUL/FDIV。
func archEmitForLoopWithBody(buf []byte, kS, kInit, kLimit, kStep, kBody uint64,
	aS uint8, sseOp byte, preemptFlagOff int32) []byte {
	return jitarm64.EmitForLoopWithRegKBodyArm64(buf, kS, kInit, kLimit, kStep, kBody,
		aS, sseOp, preemptFlagOff)
}

// archEmitForLoopWithBody2 arm64 端 PJ3 FORLOOP 二段 body 模板真接入
// (168 字节无 safepoint / 176 字节含 safepoint)。对位 amd64
// EmitForLoopWithRegKBody2 140/154 字节,arm64 多 28/22 字节(MOV imm64
// 累积 + FMOV 中转)。
//
// 二段 body 共享 d3 寄存器(对位 amd64 xmm3),节省一次 LDR/STR R(aS)
// round-trip(节省 8 字节 / iter)。
func archEmitForLoopWithBody2(buf []byte, kS, kInit, kLimit, kStep, kBody1, kBody2 uint64,
	aS uint8, sseOp1, sseOp2 byte, preemptFlagOff int32) []byte {
	return jitarm64.EmitForLoopWithRegKBody2Arm64(buf, kS, kInit, kLimit, kStep,
		kBody1, kBody2, aS, sseOp1, sseOp2, preemptFlagOff)
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
		stableShape, stableIndex, arenaBaseOffArm64(arenaBaseOff), deoptCode)
}

// archEmitGetTableNodeHit arm64 端 PJ4 IC NodeHit 字节级直达槽模板
// (196 字节,IsTable guard + SIB + gen check + nodeRef + node[stableIndex]
// + key 比对 + NodeVal load + nil check + 写 R(A) + deopt block)。
func archEmitGetTableNodeHit(buf []byte, aReg, bReg uint8,
	stableShape, stableIndex uint32, stableKey uint64,
	arenaBaseOff int32, deoptCode uint64) []byte {
	return jitarm64.EmitGetTableNodeHitArm64(buf, aReg, bReg,
		stableShape, stableIndex, stableKey, arenaBaseOffArm64(arenaBaseOff), deoptCode)
}

// archEmitSetTableArrayHit arm64 端 PJ4 SETTABLE IC ArrayHit 字节级反向
// 写模板(144 字节)。
func archEmitSetTableArrayHit(buf []byte, aReg, cReg uint8,
	stableShape, stableIndex uint32, arenaBaseOff int32, deoptCode uint64) []byte {
	return jitarm64.EmitSetTableArrayHitArm64(buf, aReg, cReg,
		stableShape, stableIndex, arenaBaseOffArm64(arenaBaseOff), deoptCode)
}

// archEmitSelfArrayHit arm64 端 PJ4 SELF IC ArrayHit 字节级 inline 模板
// (172 字节,GETTABLE ArrayHit 168 + R(A+1) 拷段 4)。
func archEmitSelfArrayHit(buf []byte, aReg, bReg uint8,
	stableShape, stableIndex uint32, arenaBaseOff int32, deoptCode uint64) []byte {
	return jitarm64.EmitSelfArrayHitArm64(buf, aReg, bReg,
		stableShape, stableIndex, arenaBaseOffArm64(arenaBaseOff), deoptCode)
}

// archEmitSetTableNodeHit arm64 端 PJ4 SETTABLE IC NodeHit 字节级反向
// 写模板(172 字节)。
func archEmitSetTableNodeHit(buf []byte, aReg, cReg uint8,
	stableShape, stableIndex uint32, stableKey uint64,
	arenaBaseOff int32, deoptCode uint64) []byte {
	return jitarm64.EmitSetTableNodeHitArm64(buf, aReg, cReg,
		stableShape, stableIndex, stableKey, arenaBaseOffArm64(arenaBaseOff), deoptCode)
}

// archEmitSelfNodeHit arm64 端 PJ4 SELF IC NodeHit 字节级 inline 模板
// (200 字节,GETTABLE NodeHit 196 + R(A+1) 拷段 4)。
func archEmitSelfNodeHit(buf []byte, aReg, bReg uint8,
	stableShape, stableIndex uint32, stableKey uint64,
	arenaBaseOff int32, deoptCode uint64) []byte {
	return jitarm64.EmitSelfNodeHitArm64(buf, aReg, bReg,
		stableShape, stableIndex, stableKey, arenaBaseOffArm64(arenaBaseOff), deoptCode)
}

// archSupportsSpec arm64 当前不支持(留 PJ8+)。
func archSupportsSpec() bool { return false }

// archSupportsForLoop arm64 端 PJ3 FORLOOP 模板已真接入(本会话 PJ8
// arm64 PJ3 全四形态:EmptyConst 84/92B / RegLimit 120/128B /
// WithRegKBody 144/152B / WithRegKBody2 168/176B,字节级单测全过);
// FORLOOP 经 archCallJITFull 主路径不经 spec trampoline,所以
// archSupportsForLoop 与 archSupportsSpec 解耦,arm64 返 true。
func archSupportsForLoop() bool { return true }
