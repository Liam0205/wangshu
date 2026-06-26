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

// SSE binop opcode bytes —— F2 0F <op> C1 形态(ADDSD/SUBSD/MULSD/DIVSD
// xmm0, xmm1)。承 Intel SDM Vol 2:
//   - ADDSD = F2 0F 58
//   - MULSD = F2 0F 59
//   - SUBSD = F2 0F 5C
//   - DIVSD = F2 0F 5E
const (
	SseOpAddsd byte = 0x58
	SseOpMulsd byte = 0x59
	SseOpSubsd byte = 0x5C
	SseOpDivsd byte = 0x5E
)

// EmitArithSpeculativeBinop 拼接「双 number 投机 BINOP A B C 模板」字节级序列。
//
// 形态(以 ADD 为例):
//
//	movsd xmm0, [rbx + B*8]    ; 读 R(B) 到 xmm0(8 字节)
//	movsd xmm1, [rbx + C*8]    ; 读 R(C) 到 xmm1(8 字节)
//	<sseOp> xmm0, xmm1         ; xmm0 OP xmm1(4 字节;OP = add/sub/mul/div)
//	movsd [rbx + A*8], xmm0    ; 写回 R(A)(8 字节)
//	ret                        ; 段返回(1 字节)
//	——— 总计 29 字节 ———
//
// **预设条件**:
//   - rbx = valueStackBase(在 callJITSpec trampoline 加载)
//   - R(B)、R(C) 都是 number(IsNumber guard 已通过——本模板假设双 number
//     快路径,失败路径的 deopt jcc 在 WithGuard 版本里发)
//
// 参数:sseOp 是 SSE opcode 字节(SseOpAddsd/Subsd/Mulsd/Divsd);a/b/c 是
// 寄存器号 [0,254];buf 是要追加字节的目标。
//
// 返回追加后的 buf。
func EmitArithSpeculativeBinop(buf []byte, sseOp byte, a, b, c uint8) []byte {
	// 假设 valueStackBase 在 rbx,reg*8 是 disp32
	buf = EmitMovsdXmmFromMem(buf, 0, 3 /* rbx */, int32(b)*8) // movsd xmm0, [rbx + B*8]
	buf = EmitMovsdXmmFromMem(buf, 1, 3 /* rbx */, int32(c)*8) // movsd xmm1, [rbx + C*8]
	// SSE binop xmm0, xmm1:F2 0F <op> C0|(0<<3)|1 = F2 0F <op> C1
	buf = append(buf, 0xF2, 0x0F, sseOp, 0xC1)
	buf = EmitMovsdMemFromXmm(buf, 0, 3 /* rbx */, int32(a)*8) // movsd [rbx + A*8], xmm0
	buf = EmitRet(buf)                                         // ret
	return buf
}

// EmitArithSpeculativeAdd 是 EmitArithSpeculativeBinop(SseOpAddsd, ...) 的
// 向后兼容 wrapper。新代码用 EmitArithSpeculativeBinop 直接发 op。
func EmitArithSpeculativeAdd(buf []byte, a, b, c uint8) []byte {
	return EmitArithSpeculativeBinop(buf, SseOpAddsd, a, b, c)
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

// EmitArithSpeculativeBinopWithGuard 拼接「IsNumber guard ×2 + 双 number
// BINOP 快路径 + ret」完整投机模板(通用版本,sseOp 选 add/sub/mul/div)。
//
// 序列(以 ADD 为例):
//
//	[guard-B] mov rax, [rbx+B*8] ; mov rcx, qNanBoxBase ; cmp ; jae deopt
//	[guard-C] mov rax, [rbx+C*8] ; mov rcx, qNanBoxBase ; cmp ; jae deopt
//	movsd xmm0, [rbx+B*8]
//	movsd xmm1, [rbx+C*8]
//	<sseOp> xmm0, xmm1
//	movsd [rbx+A*8], xmm0
//	ret
//	[deopt:] mov rax, deoptCode ; ret
//
// 总长 = 26*2 + 29 + 11(deopt block) = 92 字节(与 sseOp 无关——所有
// SSE binop 都是 F2 0F <op> C1 = 4 字节,模板字节布局不变)。
//
// **注**:guard rel32 在拼接时 patch 为「跳到 deopt block 起点」的相对
// 偏移。deoptCode 是 OSR exit 原因(承 04-osr-deopt.md),caller 出段
// 后检测 != 0 走 host helper 慢路径。
func EmitArithSpeculativeBinopWithGuard(buf []byte, sseOp byte, a, b, c uint8, deoptCode uint64) []byte {
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

	// BINOP 快路径(29 字节)
	buf = EmitArithSpeculativeBinop(buf, sseOp, a, b, c)

	// deopt block:mov rax, deoptCode; ret(11 字节)
	buf = EmitMovRaxImm64(buf, deoptCode)
	buf = EmitRet(buf)

	_ = startLen
	return buf
}

// EmitArithSpeculativeAddWithGuard 是 EmitArithSpeculativeBinopWithGuard 的
// ADD 形态向后兼容 wrapper(承既有 PJ2 ADD 真接入路径)。新代码用
// EmitArithSpeculativeBinopWithGuard 直接发 op。
func EmitArithSpeculativeAddWithGuard(buf []byte, a, b, c uint8, deoptCode uint64) []byte {
	return EmitArithSpeculativeBinopWithGuard(buf, SseOpAddsd, a, b, c, deoptCode)
}

// EmitArithSpeculativeBinopRegK 拼接「reg + 常量 投机 BINOP A B kvalue 模板」
// 字节级序列(B 是 reg ∈ [0,254],kvalue 是编译期烧入的 NaN-box raw bits)。
//
// 形态(以 ADD 为例,kvalue = NumberValue(K).bits()):
//
//	movsd xmm0, [rbx + B*8]        ; 读 R(B) 到 xmm0(8 字节)
//	mov rax, imm64=kvalue           ; 烧 K 常量 raw bits 入 rax(10 字节)
//	movq xmm1, rax                  ; rax → xmm1(5 字节)
//	<sseOp> xmm0, xmm1              ; xmm0 OP xmm1(4 字节)
//	movsd [rbx + A*8], xmm0         ; 写回 R(A)(8 字节)
//	ret                             ; 段返回(1 字节)
//	——— 总计 36 字节 ———
//
// **预设条件**:
//   - rbx = valueStackBase(在 callJITSpec trampoline 加载)
//   - R(B) 必须是 number(IsNumber guard 已通过——本模板假设 reg 端
//     是 number,常量 K 已在编译期校验为 number;失败路径的 deopt jcc 在
//     WithGuard 版本里发)
//
// 参数:sseOp 是 SSE opcode 字节;a/b 是寄存器号 [0,254];kvalue 是 K[c]
// 的 raw NaN-box bits(由 caller 经 value.NumberValue(K).bits() 算好,
// 直接 mov rax,imm64 烧入)。
//
// 返回追加后的 buf。
func EmitArithSpeculativeBinopRegK(buf []byte, sseOp byte, a, b uint8, kvalue uint64) []byte {
	buf = EmitMovsdXmmFromMem(buf, 0, 3 /* rbx */, int32(b)*8) // movsd xmm0, [rbx + B*8]
	buf = EmitMovRaxImm64(buf, kvalue)                         // mov rax, imm64=kvalue(10)
	buf = EmitMovqXmmFromRax(buf, 1)                           // movq xmm1, rax(5)
	buf = append(buf, 0xF2, 0x0F, sseOp, 0xC1)                 // <sseOp> xmm0, xmm1
	buf = EmitMovsdMemFromXmm(buf, 0, 3 /* rbx */, int32(a)*8) // movsd [rbx + A*8], xmm0
	buf = EmitRet(buf)                                         // ret
	return buf
}

// EncodedArithSpecBinopRegKLen 是 reg-K 模板字节数:
// 8(movsd xmm0,mem) + 11(mov rax,imm64) + 5(movq xmm1,rax) + 4(sse binop)
// + 8(movsd mem,xmm0) + 1(ret) = 37 字节。
//
// 注:EmitMovRaxImm64 = REX.W 0xB8 imm64 = 1+1+8 = 10 字节,但下面我们用
// 完整版本(48 B8 + 8 bytes = 10),所以总长 = 8+10+5+4+8+1 = 36。需根据
// EmitMovRaxImm64 真实长度对齐。
const EncodedArithSpecBinopRegKLen = EncodedMovsdMemLen + EncodedMovRaxImm64Len +
	EncodedMovqXmmFromRaxLen + EncodedSseBinopLen + EncodedMovsdMemLen + EncodedRetLen

// EmitArithSpeculativeBinopRegKWithGuard 拼接 reg-K 投机模板带 guard(仅
// guard reg 端是 number,K 端编译期已校验,不必 runtime guard)。
//
// 序列:
//
//	[guard-B] mov rax, [rbx+B*8] ; mov rcx, qNanBoxBase ; cmp ; jae deopt
//	movsd xmm0, [rbx+B*8]
//	mov rax, imm64=kvalue
//	movq xmm1, rax
//	<sseOp> xmm0, xmm1
//	movsd [rbx+A*8], xmm0
//	ret
//	[deopt:] mov rax, deoptCode ; ret
//
// 总长 = 26(guard×1) + 36(fast path) + 11(deopt) = 73 字节。
//
// 比 reg-reg WithGuard(92 字节)少 19 字节——只 guard 一边 reg。
func EmitArithSpeculativeBinopRegKWithGuard(buf []byte, sseOp byte, a, b uint8, kvalue uint64, deoptCode uint64) []byte {
	fastLen := EncodedArithSpecBinopRegKLen
	// guard1 jcc end = startLen + guardLen
	// fast 段结束 = startLen + guardLen + fastLen
	// deopt 起点 = startLen + guardLen + fastLen
	// jcc1 之后 PC = startLen + guardLen
	// rel1 = (deopt 起点) - (jcc1 之后 PC) = fastLen
	rel1 := int32(fastLen)

	buf = EmitIsNumberGuard(buf, b, rel1)

	// fast path(reg-K 形态,36 字节)
	buf = EmitArithSpeculativeBinopRegK(buf, sseOp, a, b, kvalue)

	// deopt block:mov rax, deoptCode; ret(11 字节)
	buf = EmitMovRaxImm64(buf, deoptCode)
	buf = EmitRet(buf)

	return buf
}

// EncodedArithSpecBinopRegKWithGuardLen 是 reg-K WithGuard 字节数:
// 26 + 36 + 11 = 73 字节。
const EncodedArithSpecBinopRegKWithGuardLen = EncodedIsNumberGuardLen +
	EncodedArithSpecBinopRegKLen + EncodedMovRaxImm64Len + EncodedRetLen

// EmitArithSpeculativeChainKKWithGuard 拼接「二段算术链式 reg-K-K 投机模板」
// 字节级序列——形态 `R(A) = R(B) op1 K1 op2 K2`(luac 编 `x*2+1` 等)。
//
// 字节布局(以 MUL+ADD 为例,即 x*K1+K2):
//
//	[guard-B]   mov rax,[rbx+B*8]; mov rcx,qNanBox; cmp; jae deopt  (26)
//	movsd xmm0, [rbx+B*8]                                          (8)
//	mov rax, K1_value; movq xmm1, rax; <sseOp1> xmm0, xmm1         (10+5+4)
//	mov rax, K2_value; movq xmm1, rax; <sseOp2> xmm0, xmm1         (10+5+4)
//	movsd [rbx+A*8], xmm0                                          (8)
//	ret                                                            (1)
//	[deopt:] mov rax, deoptCode; ret                               (11)
//	——— 总计 26 + 8 + 19 + 19 + 8 + 1 + 11 = 92 字节 ———
//
// 与 reg-reg WithGuard(92 字节)同长但语义不同——这里完成两次 SSE binop,
// 等价 host.Arith × 2,省一次 boundary 跨界 + reg-stack 中转。
//
// **预设条件**:K1/K2 在编译期已校验为 number,运行期不再 guard;只 guard
// R(B) 端是 number。chainB == retA(中间值经 xmm0 复用,不写回 stack)。
func EmitArithSpeculativeChainKKWithGuard(buf []byte, sseOp1, sseOp2 byte, a, b uint8, k1value, k2value, deoptCode uint64) []byte {
	guardLen := EncodedIsNumberGuardLen
	// 快路径布局长度:8(movsd load)+ 19(K1 + sseOp1)+ 19(K2 + sseOp2)
	//                + 8(movsd store)+ 1(ret) = 55 字节
	fastLen := EncodedMovsdMemLen + // movsd xmm0, [rbx+B*8]
		(EncodedMovRaxImm64Len + EncodedMovqXmmFromRaxLen + EncodedSseBinopLen) + // K1 + sseOp1
		(EncodedMovRaxImm64Len + EncodedMovqXmmFromRaxLen + EncodedSseBinopLen) + // K2 + sseOp2
		EncodedMovsdMemLen + // movsd [rbx+A*8], xmm0
		EncodedRetLen

	// rel1 = (deopt 起点) - (jcc1 之后 PC)
	// deopt 起点 = startLen + guardLen + fastLen
	// jcc1 之后 PC = startLen + guardLen
	// → rel1 = fastLen
	_ = guardLen
	rel1 := int32(fastLen)

	buf = EmitIsNumberGuard(buf, b, rel1)

	// fast path
	buf = EmitMovsdXmmFromMem(buf, 0, 3 /* rbx */, int32(b)*8) // movsd xmm0, [rbx+B*8]
	// 第一段:xmm0 = xmm0 op1 K1
	buf = EmitMovRaxImm64(buf, k1value)
	buf = EmitMovqXmmFromRax(buf, 1)
	buf = append(buf, 0xF2, 0x0F, sseOp1, 0xC1) // <sseOp1> xmm0, xmm1
	// 第二段:xmm0 = xmm0 op2 K2
	buf = EmitMovRaxImm64(buf, k2value)
	buf = EmitMovqXmmFromRax(buf, 1)
	buf = append(buf, 0xF2, 0x0F, sseOp2, 0xC1) // <sseOp2> xmm0, xmm1
	// 写回
	buf = EmitMovsdMemFromXmm(buf, 0, 3 /* rbx */, int32(a)*8) // movsd [rbx+A*8], xmm0
	buf = EmitRet(buf)

	// deopt block
	buf = EmitMovRaxImm64(buf, deoptCode)
	buf = EmitRet(buf)

	return buf
}

// EncodedArithSpecChainKKWithGuardLen 是二段链式 reg-K-K 模板字节数:
// 26(guard) + 55(fast) + 11(deopt) = 92 字节。
const EncodedArithSpecChainKKWithGuardLen = EncodedIsNumberGuardLen +
	EncodedMovsdMemLen +
	(EncodedMovRaxImm64Len + EncodedMovqXmmFromRaxLen + EncodedSseBinopLen) +
	(EncodedMovRaxImm64Len + EncodedMovqXmmFromRaxLen + EncodedSseBinopLen) +
	EncodedMovsdMemLen +
	EncodedRetLen +
	EncodedMovRaxImm64Len + EncodedRetLen

// EncodedArithSpecAddWithGuardLen 是完整投机 ADD 模板(含 IsNumber×2
// guard + 快路径 + deopt block)字节数:26*2 + 29 + 11 = 92。
const EncodedArithSpecAddWithGuardLen = 2*EncodedIsNumberGuardLen +
	EncodedArithSpecAddLen + EncodedMovRaxImm64Len + EncodedRetLen
