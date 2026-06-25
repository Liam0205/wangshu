//go:build wangshu_p4 && amd64

// pj2_template.go —— P4 PJ2 投机 ADD 双 number 模板的字节级拼接(承
// docs/design/p4-method-jit/03-speculation-ic.md §2 IsNumber×2 投机模板)。
//
// **范围**:本文件仅是模板字节级拼接 + 单测验证编码正确——**不**接入
// SupportsAllOpcodes 白名单。完整接入需要:
//   1. trampoline 切 SP 到 jitContext.spillBase(估 +0.5 人月)
//   2. valueStackBase 装到 callee-saved(rbx)+ Run 入口装值正确
//   3. NaN-box 常量(qNanBoxBase = 0xFFF8_0000_0000_0000)烧 imm64 入段
//   4. OSR exit 路径:guard 失败时跳出段 + trampoline 检测 exitReasonCode
//      后调 host.Arith 慢路径降级
//
// 以上每步需要本机 amd64 真 mmap+RX+execute 调试 + gdb 跟踪;本会话仅
// 落字节拼接 + 单测对照 ISA 文档。**真完整接入估 +1-2 人月**(承
// implementation-progress.md PJ10 行差距分析)。

package amd64

// EmitArithSpeculativeAdd 拼接「双 number 投机 ADD A B C 模板」字节级序列。
//
// 形态:
//
//	movsd xmm0, [rbx + B*8]    ; 读 R(B) 到 xmm0(8 字节)
//	movsd xmm1, [rbx + C*8]    ; 读 R(C) 到 xmm1(8 字节)
//	addsd xmm0, xmm1           ; xmm0 += xmm1(4 字节)
//	movsd [rbx + A*8], xmm0    ; 写回 R(A)(8 字节)
//	ret                        ; 段返回(1 字节)
//	——— 总计 29 字节 ———
//
// **预设条件**(本批不实装,留 PJ2 完整版):
//   - rbx = valueStackBase(在 trampoline 切 SP 时由 r15 +
//     ValueStackBaseOffset 装入)
//   - R(B)、R(C) 都是 number(IsNumber guard 已通过——本模板假设双
//     number 快路径,失败路径的 deopt jcc 留 PJ2 完整版)
//
// 参数:a/b/c 是寄存器号([0,254]);buf 是要追加字节的目标。
//
// 返回追加后的 buf。
func EmitArithSpeculativeAdd(buf []byte, a, b, c uint8) []byte {
	// 假设 valueStackBase 在 rbx,reg*8 是 disp32
	buf = EmitMovsdXmmFromMem(buf, 0, 3 /* rbx */, int32(b)*8) // movsd xmm0, [rbx + B*8]
	buf = EmitMovsdXmmFromMem(buf, 1, 3 /* rbx */, int32(c)*8) // movsd xmm1, [rbx + C*8]
	buf = EmitAddsdXmmXmm(buf, 0, 1)                           // addsd xmm0, xmm1
	buf = EmitMovsdMemFromXmm(buf, 0, 3 /* rbx */, int32(a)*8) // movsd [rbx + A*8], xmm0
	buf = EmitRet(buf)                                         // ret
	return buf
}

// EncodedArithSpecAddLen 是 EmitArithSpeculativeAdd 字节数:
// 8 + 8 + 4 + 8 + 1 = 29 字节。
const EncodedArithSpecAddLen = EncodedMovsdMemLen + EncodedMovsdMemLen +
	EncodedSseBinopLen + EncodedMovsdMemLen + EncodedRetLen

// qNanBoxBaseConst 是 NaN-box 非数字段下界(承 internal/value/value.go::
// qNanBoxBase = 0xFFF8_0000_0000_0000)。值小于此即 number。
const qNanBoxBaseConst uint64 = 0xFFF8_0000_0000_0000

// EmitIsNumberGuard 发射 IsNumber guard:读 [rbx+regOff*8] 到 rax,
// 与 qNanBoxBase(经 rcx 装载)cmp,大于等于(unsigned >=)即非 number,
// 跳到 deoptRel32(相对当前 jcc 之后的 PC 的 rel32 偏移)。
//
// 字节序列:
//
//	mov rax, [rbx + reg*8]    ; 7 字节(EncodedMovqFromMemRegLen)
//	mov rcx, qNanBoxBase      ; 10 字节(EncodedMovRcxImm64Len)
//	cmp rax, rcx              ; 3 字节(EncodedCmpRaxRcxLen)
//	jae deopt_rel32           ; 6 字节(EncodedJccRel32Len)
//	—— 总计 26 字节 ——
//
// **注**:本模板只发字节,不算 deopt rel32 偏移——caller(PJ2 完整版
// codegen)需在已知整个段长度后回填 deoptRel32 = (deopt 标签的相对偏移)。
// 当前 stub 用 0,真接入时改 patch。
func EmitIsNumberGuard(buf []byte, reg uint8, deoptRel32 int32) []byte {
	buf = EmitMovqRaxFromMemReg(buf, 3 /* rbx */, int32(reg)*8) // mov rax, [rbx+reg*8]
	buf = EmitMovRcxImm64(buf, qNanBoxBaseConst)                // mov rcx, qNanBoxBase
	buf = EmitCmpRaxRcx(buf)                                    // cmp rax, rcx
	buf = EmitJaeRel32(buf, deoptRel32)                         // jae deopt
	return buf
}

// EncodedIsNumberGuardLen 是 EmitIsNumberGuard 字节数(7+10+3+6 = 26)。
const EncodedIsNumberGuardLen = EncodedMovqFromMemRegLen + EncodedMovRcxImm64Len +
	EncodedCmpRaxRcxLen + EncodedJccRel32Len

// EmitArithSpeculativeAddWithGuard 拼接「IsNumber guard ×2 + 双 number ADD
// 快路径 + ret」完整投机模板。
//
// 序列:
//
//	[guard-B] mov rax, [rbx+B*8] ; mov rcx, qNanBoxBase ; cmp ; jae deopt
//	[guard-C] mov rax, [rbx+C*8] ; mov rcx, qNanBoxBase ; cmp ; jae deopt
//	movsd xmm0, [rbx+B*8]
//	movsd xmm1, [rbx+C*8]
//	addsd xmm0, xmm1
//	movsd [rbx+A*8], xmm0
//	ret
//	[deopt:] mov rax, deoptCode ; ret
//
// 总长 = 26*2 + 29 + 11(deopt block) = 92 字节。
//
// **注**:guard rel32 在拼接时 patch 为「跳到 deopt block 起点」的相对
// 偏移。deoptCode 是 OSR exit 原因(承 04-osr-deopt.md),caller 出段
// 后检测 != 0 走 host helper 慢路径。
func EmitArithSpeculativeAddWithGuard(buf []byte, a, b, c uint8, deoptCode uint64) []byte {
	startLen := len(buf)

	// 计算 deopt 块偏移:guard×2 + ADD 模板(去尾 ret 单字节,因 deopt 紧跟段尾)
	// 实际算法:先写 guard×2 + ADD 模板 + 末 ret,然后 deopt block 紧跟。
	// rel32 = (deopt 起点) - (jcc 之后的 PC) = total - jcc_end_offset
	guardLen := EncodedIsNumberGuardLen
	addLen := EncodedArithSpecAddLen
	// guard1 jcc end = startLen + guardLen
	// guard2 jcc end = startLen + 2*guardLen
	// add 段结束 = startLen + 2*guardLen + addLen
	// deopt 起点 = startLen + 2*guardLen + addLen
	// jcc1 之后 PC = startLen + guardLen
	// jcc2 之后 PC = startLen + 2*guardLen
	rel1 := int32(2*guardLen + addLen - guardLen) // = guardLen + addLen
	rel2 := int32(addLen)                         // = addLen(jcc2 跳到 deopt 起点)

	buf = EmitIsNumberGuard(buf, b, rel1)
	// 第二个 guard 的 rel2 计算:从 guard2 jcc 之后到 deopt 起点 = addLen
	buf = EmitIsNumberGuard(buf, c, rel2)

	// ADD 快路径(29 字节)
	buf = EmitArithSpeculativeAdd(buf, a, b, c)

	// deopt block:mov rax, deoptCode; ret(11 字节)
	buf = EmitMovRaxImm64(buf, deoptCode)
	buf = EmitRet(buf)

	_ = startLen
	return buf
}

// EncodedArithSpecAddWithGuardLen 是完整投机 ADD 模板(含 IsNumber×2
// guard + 快路径 + deopt block)字节数:26*2 + 29 + 11 = 92。
const EncodedArithSpecAddWithGuardLen = 2*EncodedIsNumberGuardLen +
	EncodedArithSpecAddLen + EncodedMovRaxImm64Len + EncodedRetLen
