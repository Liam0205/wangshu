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

// archCallJITSpec arm64 端 spec trampoline 真实现(对位 amd64 callJITSpec
// 装 rbx=vsBase + r15=jitCtx + BLR + 恢复)。jitarm64.CallJITSpec 经
// trampoline_arm64.s::callJITSpec 装 x26=valueStackBase + x27=jitContext
// 后 BL 跳进 mmap 段执行,期望段以 RET 收尾,返回值在 X0。
//
// 启用此真实现允许 archSupportsSpec() 翻 true → PJ2 投机模板(reg-reg /
// reg-K / chain-KK)+ PJ3 FORLOOP body/body2/RegLimit 三路径在 arm64 上
// Compile 端构造 useSpec=true 的 p4Code 后,Run 端经此函数跳进段执行。
//
// **真启用仍需 archSupportsSpec() 翻 true**(留 PJ8+ 与物理 self-hosted
// runner 端到端验证同批,QEMU 不真模拟 i-cache + PROT_EXEC 不能可靠
// e2e)。本批纯实装 trampoline asm + Go 包装,降低未来真启用工程量。
func archCallJITSpec(codeAddr uintptr, jitCtxAddr uintptr, vsBase uintptr) uint64 {
	return jitarm64.CallJITSpec(codeAddr, jitCtxAddr, vsBase)
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

// archEmitArithSpecBinopRegKWithGuard arm64 端 PJ2 投机 reg-K 模板真接入
// (92 字节,对位 amd64 EmitArithSpeculativeBinopRegKWithGuard 73 字节;
// arm64 多 19 字节)。sseOp 自动翻译 amd64→arm64 opSel。
//
// 同 reg-reg WithGuard 接线路径,真启用仍需 archCallJITSpec spec
// trampoline 实现 + archSupportsSpec() 翻 true(留 PJ8+)。
func archEmitArithSpecBinopRegKWithGuard(buf []byte, sseOp byte, a, b uint8, kvalue, deoptCode uint64) []byte {
	opSel, ok := arm64ArithOpSelForSseOp(sseOp)
	if !ok {
		return buf
	}
	return jitarm64.EmitArithSpeculativeBinopRegKWithGuardArm64(buf, opSel, a, b, kvalue, deoptCode)
}

// archEmitArithSpecChainKKWithGuard arm64 端 PJ2 投机 chain-KK 模板
// 真接入(116 字节,对位 amd64 EmitArithSpeculativeChainKKWithGuard
// 92 字节;arm64 多 24 字节)。两 sseOp 各翻译,任一未识别静默放弃。
func archEmitArithSpecChainKKWithGuard(buf []byte, sseOp1, sseOp2 byte, a, b uint8, k1value, k2value, deoptCode uint64) []byte {
	opSel1, ok1 := arm64ArithOpSelForSseOp(sseOp1)
	opSel2, ok2 := arm64ArithOpSelForSseOp(sseOp2)
	if !ok1 || !ok2 {
		return buf
	}
	return jitarm64.EmitArithSpeculativeChainKKWithGuardArm64(buf, opSel1, opSel2,
		a, b, k1value, k2value, deoptCode)
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

// archEmitSelfNodeHitNoRet arm64 端 NoRet 变体真实装(承 C5 commit 收口
// PR comment 8b4ff8e 占位回填教训):同 archEmitSelfNodeHit 但**成功路径
// 不 emit ret**,改发 B(无条件跳)跳过 deopt block 到段尾 fall-through 到
// 调用方 emit 的 BuildVoid0Arg 段。
//
// **接入路径**:archEmitFrameInlineExitHelperRequest + archCallJITSpec
// 形态下,SELF 段成功后 fall-through 到 BuildVoid0Arg + ExitHelperRequest +
// PopVoid0Arg + ret;翻 archSupportsFrameInline=true 后启用。
//
// **vs 原 panic 占位**(已退役):旧 panic 把 archSupportsFrameInline=true
// 的 NoRet 路径整路打死,真实装替之可让 SELF NodeHit 在 useFrameInline
// 形态下与 amd64 EmitSelfNodeHitNoRet byte-equal(同 200 字节,RET 4B 换为
// B 4B)。
func archEmitSelfNodeHitNoRet(buf []byte, aReg, bReg uint8,
	stableShape, stableIndex uint32, stableKey uint64,
	arenaBaseOff int32, deoptCode uint64) []byte {
	return jitarm64.EmitSelfNodeHitNoRetArm64(buf, aReg, bReg,
		stableShape, stableIndex, stableKey,
		arenaBaseOffArm64(arenaBaseOff), deoptCode)
}

// archEmitSpecArgLoadK / archEmitSpecArgLoadReg arm64 实装(承 PJ5 spec
// template 字节级 inline,跳过 host.GetReg/SetReg round-trip)。物理 runner
// 启用 archSupportsSpec=true 后即激活,与 amd64 字节级形态对等。
func archEmitSpecArgLoadK(buf []byte, dstReg uint8, k uint64) []byte {
	return jitarm64.EmitSpecArgLoadKArm64(buf, dstReg, k)
}
func archEmitSpecArgLoadReg(buf []byte, dstReg uint8, srcReg uint8) []byte {
	return jitarm64.EmitSpecArgLoadRegArm64(buf, dstReg, srcReg)
}

// archSupportsSpec arm64 端 PJ8 工程组件完整就绪,linux/arm64 翻 true 允许
// Compile 端走 useSpec 真路径;darwin/arm64 暂返 false(F3-#3 真物理
// macos-latest M1 实证 SIGSEGV at PC=0x2000,trampoline_arm64.s 跳 mmap 段
// 后真物理执行崩,根因 isolate 留 F3-#3b,见 codepage_darwin_test.go
// ExecSanityProbe 与 trampoline_test.go darwin skip)。
//
// 状态分布:
//   - amd64:走 arch_amd64.go(不在本文件)
//   - linux/arm64:✅ 翻 true(承 C7 + tmp/wangshu-p4-todo.md §二)
//   - darwin/arm64:✅ 翻 true(F3-#3b trampoline_arm64.s STP/LDP 偏移
//     +8 修复 LR slot 覆盖 bug 后真物理 M1 验证全过,详 trampoline_arm64.s
//     头注 + commit message)
//
// **激活范围**:Compile 端 PJ2 投机三形态 + PJ3 FORLOOP body/body2/RegLimit
// 三路径走 archCallJITSpec;PJ4 IC 六模板继续走 archCallJITFull(本就启用);
// PJ5 SELF spec template + useFrameInline。
func archSupportsSpec() bool {
	return true
}

// archSupportsForLoop arm64 端 PJ3 FORLOOP 模板已真接入(本会话 PJ8
// arm64 PJ3 全四形态:EmptyConst 84/92B / RegLimit 120/128B /
// WithRegKBody 144/152B / WithRegKBody2 168/176B,字节级单测全过);
// FORLOOP 经 archCallJITFull 主路径不经 spec trampoline。
//
// **darwin/arm64 同 archSupportsSpec 翻 true**(F3-#3b trampoline 修复后)。
func archSupportsForLoop() bool {
	return true
}

// archEmitHelperCall 发射 helper call 通用宏(arm64 端:`mov X16,
// helperAddr imm64 + blr X16`,20 字节)。对位 amd64 archEmitHelperCall
// (12 字节)。
//
// 用于 PJ5 CALL/TAILCALL 真接入(待 PJ8+ 翻 archSupportsSpec 后启用)。
// arm64 多 8 字节因 MOV imm64 序列 16 vs amd64 mov rax imm64 10 + BLR
// vs CALL reg 2。X16 是 ARMv8 IP0 scratch 寄存器(intra-procedure-call
// scratch,callee 可任意改写)。
func archEmitHelperCall(buf []byte, helperAddr uint64) []byte {
	return jitarm64.EmitHelperCallArm64(buf, helperAddr)
}

// archEncodedHelperCallLen 是 helper call 通用宏字节数(arm64 = 20,
// 对位 amd64 = 12)。caller 用于 inline CALL 模板长度预算(arm64 端
// 因 RISC fixed-length 比 amd64 多 8 字节)。
const archEncodedHelperCallLen = jitarm64.EncodedHelperCallArm64Len

// archSupportsFrameInline arm64 端 C7 翻闸门:linux/arm64 + darwin/arm64
// 均返 true 允许 useFrameInline 真路径(F3-#3b trampoline 修复后两端等价)。
//
// **依赖闭环**(C5/C6 已交付):
//   - archEmitSelfNodeHitNoRet:C5 真实装替 panic 占位
//   - archEmitFrameInlineExitHelperRequest:C6 真实装替 0 字节占位
//   - archEncodedFrameInlineExitHelperRequestLen:C6 从 0 → 36
func archSupportsFrameInline() bool {
	return true
}

// archEmitFrameInlineBuildVoid0ArgSkeleton arm64 端代理 jitarm64 同款 helper
// (164 字节,承 §9.20 Option B Spike 1)。注意 arm64 offset 用 uint16 形态
// (LDR Xt, [Xn, pimm] 编码限制,pimm 必须 0..32760 且 8 对齐)。
func archEmitFrameInlineBuildVoid0ArgSkeleton(buf []byte,
	ciDepthAddrOff, ciSegBaseAddrOff int32, callARecv uint8,
	w0, w1, w2, w4 uint64) []byte {
	return jitarm64.EmitFrameInlineBuildVoid0ArgSkeletonArm64(buf,
		uint16(ciDepthAddrOff), uint16(ciSegBaseAddrOff), callARecv,
		jitarm64.FrameInlineCISlotWordsArm64{Word0: w0, Word1: w1, Word2: w2, Word3: 0, Word4: w4})
}

// archEmitFrameInlinePopVoid0ArgSkeleton arm64 端代理 jitarm64 同款 helper
// (16 字节,等价 EmitFrameInlineCIDepthDecArm64)。
func archEmitFrameInlinePopVoid0ArgSkeleton(buf []byte, ciDepthAddrOff int32) []byte {
	return jitarm64.EmitFrameInlinePopVoid0ArgSkeletonArm64(buf, uint16(ciDepthAddrOff))
}

// archEmitFrameInlineExitHelperRequest arm64 端 Spike 1 exit-helper-request
// 段真实装(承 C6 commit 占位回填,承 §9.20.9 (4) trampoline exit-resume 协议
// arm64 对位)。
//
// 字节序列(36 字节,对位 amd64 24 字节):
//   - movz/movk x16, helperCode imm64(16B)
//   - str x16, [x27 + exitArg0Off](4B,64-bit STR)
//   - movz w16, #3(4B,ExitInlineHelper)
//   - str w16, [x27 + exitReasonOff](4B,32-bit STR)
//   - movz w0, #3(4B,设返值;trampoline 检 X0)
//   - ret(4B)
//
// **archSupportsFrameInline 翻 true 后启用**(C7);本 commit 替原 0 字节占位为
// 真实装,Compile/Run 端真接通 + 物理 runner 端到端验证留 C7 + 后续 PJ8
// macos-latest CI 跑。
func archEmitFrameInlineExitHelperRequest(buf []byte,
	exitReasonOff, exitArg0Off int32, helperCode uint64) []byte {
	return jitarm64.EmitFrameInlineExitHelperRequestArm64(buf,
		exitReasonOff, exitArg0Off, helperCode)
}

// archEncodedFrameInlineBuildVoid0ArgSkeletonLen arm64 Spike 1 enterLuaFrame
// 骨架字节数(164,承 §9.20 + jitarm64)。
const archEncodedFrameInlineBuildVoid0ArgSkeletonLen = jitarm64.EncodedFrameInlineBuildVoid0ArgSkeletonArm64Len

// archEncodedFrameInlinePopVoid0ArgSkeletonLen arm64 Spike 1 popCallInfo
// 骨架字节数(16,等价 CIDepthDecArm64)。
const archEncodedFrameInlinePopVoid0ArgSkeletonLen = jitarm64.EncodedFrameInlinePopVoid0ArgSkeletonArm64Len

// archEncodedFrameInlineExitHelperRequestLen arm64 Spike 1 exit-helper-request
// 段字节数(36,对位 amd64 24 字节;arm64 fixed-length 编码 + 无寄存器复用
// 多 12 字节)。承 C6 jitarm64.EmitFrameInlineExitHelperRequestArm64 真实装。
const archEncodedFrameInlineExitHelperRequestLen = jitarm64.EncodedFrameInlineExitHelperRequestArm64Len
