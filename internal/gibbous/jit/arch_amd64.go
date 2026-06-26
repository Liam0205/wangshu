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

// archEmitArithSpecAddWithGuard 拼接 PJ2 ADD 投机模板(IsNumber×2 guard +
// 双 number 快路径 + deopt block)字节级序列。amd64 端代理到
// jitamd64.EmitArithSpeculativeAddWithGuard(92 字节)。
func archEmitArithSpecAddWithGuard(buf []byte, a, b, c uint8, deoptCode uint64) []byte {
	return jitamd64.EmitArithSpeculativeAddWithGuard(buf, a, b, c, deoptCode)
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

// archSupportsSpec 返 true 当本 arch 支持 PJ2 投机模板真接入。
// amd64 ✅;arm64/其它 ❌(留 PJ8+)。
func archSupportsSpec() bool { return true }
