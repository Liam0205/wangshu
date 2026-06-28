//go:build wangshu_p4 && amd64

// arch_amd64.go —— P4 PJ8 arch 路由 amd64 实装(对位 arch_arm64.go)。
//
// 把 compiler.go / code.go 里硬编码的 jitamd64 依赖移到此 arch 适配层,
// 让 jit 包主体不依赖具体 GOARCH,arm64 build 下自动切到 jitarm64。
//
// 承 docs/design/p4-method-jit/06-backends.md §1「共享骨架 + per-arch 发射器」
// 决议——per-arch 发射函数按 build tag 各一份,jit 主包 import 中性接口。
package jit

import (
	"github.com/Liam0205/wangshu/internal/bytecode"
	jitamd64 "github.com/Liam0205/wangshu/internal/gibbous/jit/amd64"
)

// archCodePage 是 arch 抽象的可执行段——本 build 下别名 jitamd64.CodePage。
type archCodePage = jitamd64.CodePage

// archEmitLoadKReturn 发射「mov RAX, value; ret」直线模板(amd64 端 11 字节)。
// 常量族(LOADK/LOADBOOL/LOADNIL)烧 NaN-box value;prelude/比较折叠族 RAX
// 是 dummy(由 Run 端忽略),value 仍要写入(模板字节数固定)。
func archEmitLoadKReturn(buf []byte, value uint64) []byte {
	buf = jitamd64.EmitMovRaxImm64(buf, value)
	buf = jitamd64.EmitRet(buf)
	return buf
}

// archMmapCode 把 code 写入 W^X 段(PROT_RW alloc → copy → PROT_RX 翻面)。
func archMmapCode(code []byte) (*archCodePage, error) {
	return jitamd64.MmapCode(code)
}

// archCallJITFull 跳进 mmap 段(完整 trampoline:保存 callee-saved + 装
// jitContext 到 r15 + CALL + 恢复)。返 RAX。
func archCallJITFull(codeAddr uintptr, jitCtxAddr uintptr) uint64 {
	return jitamd64.CallJITFull(codeAddr, jitCtxAddr)
}

// archCallJITSpec 跳进 PJ2 投机模板 mmap 段(callJITSpec trampoline 同时
// 装 r15=jitContext + rbx=valueStackBase)。返 RAX(段最后一条 mov/movsd
// 的值,或 deopt block 烧入的 deoptCode)。
//
// 用例:PJ2 投机模板真接入(ADD/SUB/MUL/DIV 双 number 快路径)。
func archCallJITSpec(codeAddr uintptr, jitCtxAddr uintptr, vsBase uintptr) uint64 {
	return jitamd64.CallJITSpec(codeAddr, jitCtxAddr, vsBase)
}

// archSseOpForArith 把 Lua 算术 opcode 映射到 SSE binop opcode 字节。
// 不支持的 op(MOD/POW——MOD 用 floor-mod 不是单条 SSE,POW 用 pow() helper)
// 返回 (0, false)。
//
// **承 03-speculation-ic.md §2 投机白名单**:f64 快路径投机仅对 ADD/SUB/
// MUL/DIV(IEEE 754 单条 SSE 指令)成立,其它算术族走 host helper 慢路径
// (与解释器 byte-equal,无加速但正确性兜底)。
func archSseOpForArith(op uint8) (byte, bool) {
	switch bytecode.OpCode(op) {
	case bytecode.ADD:
		return jitamd64.SseOpAddsd, true
	case bytecode.SUB:
		return jitamd64.SseOpSubsd, true
	case bytecode.MUL:
		return jitamd64.SseOpMulsd, true
	case bytecode.DIV:
		return jitamd64.SseOpDivsd, true
	default:
		return 0, false
	}
}

// archEmitArithSpecBinopWithGuard 拼接 PJ2 BINOP 投机模板(IsNumber×2 guard
// + 双 number 快路径 + deopt block)字节级序列,通用版本——sseOp 由 caller
// 经 archSseOpForArith 选好。amd64 端代理到 jitamd64.EmitArithSpeculative
// BinopWithGuard(92 字节,与 op 无关)。
func archEmitArithSpecBinopWithGuard(buf []byte, sseOp byte, a, b, c uint8, deoptCode uint64) []byte {
	return jitamd64.EmitArithSpeculativeBinopWithGuard(buf, sseOp, a, b, c, deoptCode)
}

// archEmitArithSpecBinopRegKWithGuard 拼接 PJ2 reg-K 形态投机模板
// (B 是 reg + K 编译期烧 imm64,单 guard reg 端)。amd64 端代理到
// jitamd64.EmitArithSpeculativeBinopRegKWithGuard(73 字节)。
func archEmitArithSpecBinopRegKWithGuard(buf []byte, sseOp byte, a, b uint8, kvalue uint64, deoptCode uint64) []byte {
	return jitamd64.EmitArithSpeculativeBinopRegKWithGuard(buf, sseOp, a, b, kvalue, deoptCode)
}

// archEmitArithSpecChainKKWithGuard 拼接 PJ2 二段链式 reg-K-K 投机模板
// (`R(A) = R(B) op1 K1 op2 K2`)。amd64 端代理 92 字节 chain 模板。
func archEmitArithSpecChainKKWithGuard(buf []byte, sseOp1, sseOp2 byte, a, b uint8, k1value, k2value, deoptCode uint64) []byte {
	return jitamd64.EmitArithSpeculativeChainKKWithGuard(buf, sseOp1, sseOp2, a, b, k1value, k2value, deoptCode)
}

// archEmitForLoopEmptyConst 拼接 PJ3 全常量 init/limit/step 空 body FORLOOP
// 模板(无 safepoint 69 字节 / 含 safepoint 83 字节,浮点 idx 累加 +
// ucomisd limit + backward jcc + 可选 r15+disp byte cmp safepoint check)。
// amd64 端代理 jitamd64.EmitForLoopEmptyConst。
//
// preemptFlagOff >= 0 时模板含 safepoint check(承 V18 -race 抢占纪律);
// < 0 时省略(单测 / spike 用例)。
func archEmitForLoopEmptyConst(buf []byte, kInit, kLimit, kStep uint64, preemptFlagOff int32) []byte {
	return jitamd64.EmitForLoopEmptyConst(buf, kInit, kLimit, kStep, preemptFlagOff)
}

// archEmitForLoopRegLimit 拼接 PJ3 reg-limit 空 body FORLOOP 模板(hot path
// 形态 `for i=1, n do end`):IsNumber guard + 浮点 loop + 可选 safepoint
// + deopt block。amd64 端代理 jitamd64.EmitForLoopRegLimit。
func archEmitForLoopRegLimit(buf []byte, kInit, kStep uint64, limitReg uint8, deoptCode uint64, preemptFlagOff int32) []byte {
	return jitamd64.EmitForLoopRegLimit(buf, kInit, kStep, limitReg, deoptCode, preemptFlagOff)
}

// archEmitForLoopWithBody 拼接 PJ3 FORLOOP body 含 reg-K op 形态模板
// (`local s=K_s; for i=K1,K2 do s = s op K3 end; return s`)。135 字节
// 含 safepoint check。
func archEmitForLoopWithBody(buf []byte, kS, kInit, kLimit, kStep, kBody uint64,
	aS uint8, sseOp byte, preemptFlagOff int32) []byte {
	return jitamd64.EmitForLoopWithRegKBody(buf, kS, kInit, kLimit, kStep, kBody, aS, sseOp, preemptFlagOff)
}

// archEmitForLoopWithBody2 拼接 PJ3 FORLOOP 二段 body 模板
// (`local s; for i=K1,K2 do s = s op1 K3; s = s op2 K4 end; return s`)。
// 154 字节复用 xmm3 跨两段省一次 load/store。
func archEmitForLoopWithBody2(buf []byte, kS, kInit, kLimit, kStep, kBody1, kBody2 uint64,
	aS uint8, sseOp1, sseOp2 byte, preemptFlagOff int32) []byte {
	return jitamd64.EmitForLoopWithRegKBody2(buf, kS, kInit, kLimit, kStep, kBody1, kBody2, aS, sseOp1, sseOp2, preemptFlagOff)
}

// archEmitGetTableArrayHit 拼接 PJ4 IC ArrayHit 字节级直达槽模板
// (132 字节,IsTable guard + arena base load + gen check + array 直达 +
// nil check + 写 R(A) + deopt block)。amd64 端代理 jitamd64.EmitGetTableArrayHit。
func archEmitGetTableArrayHit(buf []byte, aReg, bReg uint8, stableShape, stableIndex uint32, arenaBaseOff int32, deoptCode uint64) []byte {
	return jitamd64.EmitGetTableArrayHit(buf, aReg, bReg, stableShape, stableIndex, arenaBaseOff, deoptCode)
}

// archEmitGetTableNodeHit 拼接 PJ4 IC NodeHit 字节级直达槽模板
// (159 字节,严密 IsTable guard + arena base + gen check + nodeRef +
// node[stableIndex] + key 比对 + NodeVal load + nil check + 写 R(A) +
// deopt block)。amd64 端代理 jitamd64.EmitGetTableNodeHit。
//
// 与 ArrayHit 关键差异:
//   - 取 word3=nodeRef(offset 24)而非 word2=arrayRef(offset 16)
//   - node 步长 24 字节(nodeWords=3)而非 array 8 字节
//   - 多 key 比对(NodeKey == stableKey 防键退化 / __index 链)
func archEmitGetTableNodeHit(buf []byte, aReg, bReg uint8,
	stableShape, stableIndex uint32, stableKey uint64,
	arenaBaseOff int32, deoptCode uint64) []byte {
	return jitamd64.EmitGetTableNodeHit(buf, aReg, bReg,
		stableShape, stableIndex, stableKey, arenaBaseOff, deoptCode)
}

// archEmitSetTableArrayHit 拼接 PJ4 SETTABLE IC ArrayHit 字节级反向写模板
// (113 字节,严密 IsTable guard + arena base + gen check + arrayRef +
// load R(C) value → rdx + 反向 store [r14+rcx+stableIndex*8] from rdx +
// ret + deopt block)。amd64 端代理 jitamd64.EmitSetTableArrayHit。
//
// **setter 形态**:retB=1(SETTABLE 0 返回值),Run 端不写 R(A)。
func archEmitSetTableArrayHit(buf []byte, aReg, cReg uint8,
	stableShape, stableIndex uint32, arenaBaseOff int32, deoptCode uint64) []byte {
	return jitamd64.EmitSetTableArrayHit(buf, aReg, cReg,
		stableShape, stableIndex, arenaBaseOff, deoptCode)
}

// archEmitSelfArrayHit 拼接 PJ4 SELF IC ArrayHit 字节级 inline 模板
// (139 字节,GETTABLE ArrayHit 132 + R(A+1) 拷段 7 字节)。amd64 端代理
// jitamd64.EmitSelfArrayHit。
//
// **SELF 形态**:R(A+1) := R(B);R(A) := R(B)[K]。模板入口先 store
// R(A+1) = R(B),然后走 GETTABLE ArrayHit 同款流程取 R(A)。
func archEmitSelfArrayHit(buf []byte, aReg, bReg uint8,
	stableShape, stableIndex uint32, arenaBaseOff int32, deoptCode uint64) []byte {
	return jitamd64.EmitSelfArrayHit(buf, aReg, bReg,
		stableShape, stableIndex, arenaBaseOff, deoptCode)
}

// archEmitSetTableNodeHit 拼接 PJ4 SETTABLE IC NodeHit 字节级反向写模板
// (140 字节,GetTable NodeHit 159 - getter 段 34 + setter 段 15)。amd64
// 端代理 jitamd64.EmitSetTableNodeHit。
//
// **setter NodeHit 形态**:hash 段 NodeKey 比对 + 反向写 NodeVal。
func archEmitSetTableNodeHit(buf []byte, aReg, cReg uint8,
	stableShape, stableIndex uint32, stableKey uint64,
	arenaBaseOff int32, deoptCode uint64) []byte {
	return jitamd64.EmitSetTableNodeHit(buf, aReg, cReg,
		stableShape, stableIndex, stableKey, arenaBaseOff, deoptCode)
}

// archEmitSelfNodeHit 拼接 PJ4 SELF IC NodeHit 字节级 inline 模板
// (166 字节,SELF ArrayHit 139 + key 比对 27 字节)。amd64 端代理
// jitamd64.EmitSelfNodeHit。
//
// **SELF NodeHit 形态**:R(A+1) := R(B);R(A) := R(B)[K_string]
// (经 hash 段 NodeKey 比对 + NodeVal load)。这是 real-world
// `obj:method()` 调用的典型 IC 形态(method 是字符串 ident)。
func archEmitSelfNodeHit(buf []byte, aReg, bReg uint8,
	stableShape, stableIndex uint32, stableKey uint64,
	arenaBaseOff int32, deoptCode uint64) []byte {
	return jitamd64.EmitSelfNodeHit(buf, aReg, bReg,
		stableShape, stableIndex, stableKey, arenaBaseOff, deoptCode)
}

// archEmitSpecArgLoadK / archEmitSpecArgLoadReg arm-routed amd64 实装(承
// PJ5 SELF + CALL spec template args 装载字节级 inline,跳过 host.GetReg/
// SetReg round-trip)。arm64 端 stub(留 PJ8+ 物理 runner 启用前),其它
// arch 同 amd64 fallback 走非 spec 路径。
func archEmitSpecArgLoadK(buf []byte, dstReg uint8, k uint64) []byte {
	return jitamd64.EmitSpecArgLoadK(buf, dstReg, k)
}
func archEmitSpecArgLoadReg(buf []byte, dstReg uint8, srcReg uint8) []byte {
	return jitamd64.EmitSpecArgLoadReg(buf, dstReg, srcReg)
}

// archSupportsSpec 返 true 当本 arch 支持 PJ2 投机模板真接入。
// amd64 ✅;arm64/其它 ❌(留 PJ8+)。
func archSupportsSpec() bool { return true }

// archSupportsForLoop 返 true 当本 arch 支持 PJ3 FORLOOP 模板真接入
// (经 archCallJITFull 主路径,不经 spec trampoline)。amd64 ✅(本就经
// archSupportsSpec 启用,本函数为新 arch 提供解耦闸门);arm64 ✅
// (本会话 PJ8 arm64 PJ3 全四形态字节级模板真接入完整)。
func archSupportsForLoop() bool { return true }

// archEmitHelperCall 发射 helper call 通用宏(amd64 端:`mov rax, helperAddr
// imm64 + call rax`,12 字节)。对位 arm64 EmitHelperCallArm64(20 字节)。
//
// 用于 PJ5 CALL/TAILCALL 真接入 + PJ4 deopt 路径调 host helper(host.DoCall
// / host.GetTable / host.Arith 等)。helperAddr 是 helper function 物理
// 地址(经 jit Compile 时编译期求出,reflect.ValueOf(fn).Pointer())。
//
// **接入路径**:本函数当前无 caller,留作 PJ5 真接入工程基础——下一步
// archEmitHelperCall 嵌入 inline CALL 模板时调用本宏。
func archEmitHelperCall(buf []byte, helperAddr uint64) []byte {
	return jitamd64.EmitHelperCall(buf, helperAddr)
}

// archEncodedHelperCallLen 是 helper call 通用宏字节数(amd64 = 12,
// arm64 = 20)。caller 用于 inline CALL 模板长度预算。
const archEncodedHelperCallLen = jitamd64.EncodedHelperCallLen

// archEmitFrameInlineBuildVoid0ArgSkeleton 拼接 amd64 Spike 1 enterLuaFrame
// 字节级 inline 骨架(120 字节,承 §9.20 Option B Spike 1 + jitamd64.
// EmitFrameInlineBuildVoid0ArgSkeleton)。Compile 端 useFrameInline 分支用。
func archEmitFrameInlineBuildVoid0ArgSkeleton(buf []byte,
	ciDepthAddrOff, ciSegBaseAddrOff int32, callARecv uint8,
	w0, w1, w2, w4 uint64) []byte {
	return jitamd64.EmitFrameInlineBuildVoid0ArgSkeleton(buf,
		ciDepthAddrOff, ciSegBaseAddrOff, callARecv,
		jitamd64.FrameInlineCISlotWords{Word0: w0, Word1: w1, Word2: w2, Word3: 0, Word4: w4})
}

// archEmitFrameInlinePopVoid0ArgSkeleton 拼接 amd64 Spike 1 popCallInfo
// 字节级 inline 骨架(10 字节,承 §9.20 Option B Spike 1)。
func archEmitFrameInlinePopVoid0ArgSkeleton(buf []byte, ciDepthAddrOff int32) []byte {
	return jitamd64.EmitFrameInlinePopVoid0ArgSkeleton(buf, ciDepthAddrOff)
}

// archEmitFrameInlineExitHelperRequest 拼接 amd64 Spike 1 trampoline
// exit-resume 协议 exit-helper-request 段(24 字节,承 §9.20.9 (4))。
func archEmitFrameInlineExitHelperRequest(buf []byte,
	exitReasonOff, exitArg0Off int32, helperCode uint64) []byte {
	return jitamd64.EmitFrameInlineExitHelperRequest(buf,
		exitReasonOff, exitArg0Off, helperCode)
}

// archEncodedFrameInlineBuildVoid0ArgSkeletonLen amd64 Spike 1 enterLuaFrame
// 骨架字节数(120)。
const archEncodedFrameInlineBuildVoid0ArgSkeletonLen = jitamd64.EncodedFrameInlineBuildVoid0ArgSkeletonLen

// archEncodedFrameInlinePopVoid0ArgSkeletonLen amd64 Spike 1 popCallInfo
// 骨架字节数(10)。
const archEncodedFrameInlinePopVoid0ArgSkeletonLen = jitamd64.EncodedFrameInlinePopVoid0ArgSkeletonLen

// archEncodedFrameInlineExitHelperRequestLen amd64 Spike 1 exit-helper-request
// 段字节数(24,承 §9.20.9 (4) optimized form)。
const archEncodedFrameInlineExitHelperRequestLen = jitamd64.EncodedFrameInlineExitHelperRequestLen

// archSupportsFrameInline 返 true 当本 arch 支持 PJ5 Option B 帧建立内联
// 真接入(承 §9.20 Spike 1)。
//
// **当前 amd64 = false**:字节级 emit 模板(BuildVoid0ArgSkeleton 120B +
// PopVoid0ArgSkeleton 10B + LoadClosureGCRef 20B + WriteCIWord 14B +
// CIDepth++/-- 10B)已完整字节级实装并 8 字节级单测全过,但 Compile/Run
// 端守门 + helper call ABI 协议 + e2e prove-the-path 实证留 Spike 1 后续
// 工程。**翻 true 前置条件**:
//  1. analyzeSelfCallSpecForm 加 useFrameInline 守门(callee.NumParams=0 +
//     !IsVararg + !NeedsArg + MaxStack≤32)
//  2. compileSpecSelfCall 加 useFrameInline 分支 emit BuildVoid0ArgSkeleton +
//     archEmitHelperCall(跳 executeFrom)+ PopVoid0ArgSkeleton
//  3. runSpecSelfCallInline 实装(Run 端走 mmap 段 zero-cross 路径)
//  4. SpecFrameInlineHits 探针 + e2e WarmupThenForce 命中实证
//  5. benchmark 摊薄实证(简单 method 体 1.12x→≥1.0x;计算密集 0.94x→
//     0.7-0.8x)
//
// **arm64 / 其它 arch 同 false**(等物理 runner 翻 archSupportsSpec=true 同批)。
func archSupportsFrameInline() bool { return false }
