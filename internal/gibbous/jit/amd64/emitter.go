//go:build wangshu_p4 && amd64

// emitter.go —— P4 amd64 后端直线模板发射器(PJ1 范围)。
//
// 承 docs/design/p4-method-jit/06-backends.md §2.4 emitter trait 接口 + §3.7
// 直线族(MOVE/LOADK/LOADBOOL/LOADNIL,编号 0-3)+ §3.4 控制流族 JMP(编号 22)
// + §3.5 调用族 RETURN(编号 30)。
//
// **PJ1 简化形态**(承 spike DECISION.md「极简形态的限制」+ spike 闸门绿后
// 解锁的 emitter 接口):本 emitter 不引入 jitContext / 切 SP / 自管栈,直线
// 模板的形态是「mov rax, imm; ret」一类极简序列(spike S1 同款)。这能让 PJ1
// 真正可工作的最小子集落地——即「LOADK 烧 imm,RETURN 跳出」单 BB 直线 Proto
// 经 mmap 段执行返回 RAX 给 trampoline → callJIT 拿到值。
//
// **PJ1 范围内 supported = LOADK + RETURN**(单 BB 直线,无 jump)——这是 spike
// 闸门四档 + 「最小可执行 P4 形态」的首个交集。MOVE / LOADBOOL / LOADNIL / JMP
// 留 PJ2 起渐进扩(它们涉及多寄存器或前向 fixup,不在 PJ1 极简形态内)。
//
// 完整 Emitter trait + per-opcode 发射函数(承 06 §3.x 各族)留 PJ2-PJ7 渐进
// 填实——本文件先建最小骨架,让 PJ1 的「end-to-end mmap 段 round-trip 工作」
// 成立。
package amd64

import "encoding/binary"

// EmitMovRaxImm64 发射「mov rax, imm64」9 字节序列(REX.W + B8+rd)。
//
// amd64 编码:48 b8 ii ii ii ii ii ii ii ii(10 字节)
//   - 0x48:REX prefix,W=1 表 64-bit operand
//   - 0xb8:B8+rd opcode,rd=0(RAX)
//   - imm64:little-endian 8 字节立即数
//
// 用于 LOADK 直线快速路径——把 Proto.Constants[Bx] 的 NaN-box u64 烧入。
// PJ1 不实装常量池转 NaN-box(那是值表示模块的事);本 emitter 接口只
// 暴露「写 u64 imm」原语,调用方决定怎么用。
func EmitMovRaxImm64(buf []byte, imm uint64) []byte {
	buf = append(buf, 0x48, 0xb8) // REX.W mov rax, imm64
	var imm8 [8]byte
	binary.LittleEndian.PutUint64(imm8[:], imm)
	buf = append(buf, imm8[:]...)
	return buf
}

// EmitRet 发射「ret」单字节序列(0xc3)。
//
// 用于 RETURN 模板尾——把当前 RAX 值返回 trampoline。PJ1 简化形态下 RAX
// = 模板烧入的最近一个 imm(LOADK 后的常量值),trampoline 经 callJIT 拿到。
func EmitRet(buf []byte) []byte {
	return append(buf, 0xc3)
}

// EncodedMovRaxImm64Len 是「mov rax, imm64」编码后的字节数(常量,固定 10)。
const EncodedMovRaxImm64Len = 10

// EncodedRetLen 是「ret」编码后的字节数(常量,固定 1)。
const EncodedRetLen = 1

// =============================================================================
// PJ3+ Emitter 原语扩展(渐进白名单,承 06 §3.x 各族)
// =============================================================================
//
// 本节扩 PJ1 的 EmitMovRaxImm64 + EmitRet 二原语,加入控制流 / 算术 / 表 IC
// 各族的最小指令编码原语。这些原语本身不构成完整 opcode 模板(完整模板还需
// jitContext 字段 inline 访问 + helper 调用 + guard 失败 OSR exit 等机制,留
// 后续 PJ 扩),但作为 emitter 接口面建好让 PJ4+ 启动时直接用。
//
// **PJ3 范围内 SupportsAllOpcodes 仍全 false**——本节原语经单测 prove-the-path
// 走到(每个 EmitXxx 的字节码序列经 mmap 段执行验证),但 bridge 主路径不
// 触达。

// EmitMovImm64ToReg 发射「mov regNum, imm64」10 字节序列(承 06 §3.7 直线族,
// regNum ∈ [0, 7] = RAX/RCX/RDX/RBX/RSP/RBP/RSI/RDI)。
//
// **关键防御**(承审查 🟠 #1):reg=4(RSP)/ reg=5(RBP)是 amd64 合法编码,
// 但语义上 mov rsp/rbp, imm64 会破坏 trampoline 栈协议(返回时 ret 跳无效
// 地址 SEGV)。本函数对 4/5 兜底为 RAX(0)。reg 6/7(RSI/RDI)合法可用。
func EmitMovImm64ToReg(buf []byte, regNum uint8, imm uint64) []byte {
	if regNum > 7 || regNum == 4 || regNum == 5 {
		// RSP/RBP 不安全,防御性兜底为 RAX
		regNum = 0
	}
	buf = append(buf, 0x48, 0xb8|regNum)
	for i := 0; i < 8; i++ {
		buf = append(buf, byte(imm>>(8*i)))
	}
	return buf
}

// EmitNop 发射「nop」单字节序列(0x90)——padding / 调试用。
//
// PJ4+ 用例:模板间对齐填充(amd64 fast path 偶有需要按 16 字节对齐启动循环
// 入口,nop padding 是常用手法)。
func EmitNop(buf []byte) []byte {
	return append(buf, 0x90)
}

// EncodedMovImm64ToRegLen 是「mov regN, imm64」编码后的字节数(常量,固定 10)。
const EncodedMovImm64ToRegLen = 10

// EncodedNopLen 是「nop」编码后的字节数(常量,固定 1)。
const EncodedNopLen = 1

// =============================================================================
// PJ4+ 比较族 + 跳转编码原语(承 06 §3.2 比较族 + §3.4 控制流 JMP)
// =============================================================================

// EmitCmpRaxImm32 发射「cmp rax, imm32」6 字节序列(承 06 §3.2 比较族基础)。
//
// amd64 编码:48 3d ii ii ii ii(REX.W cmp rax, imm32)。imm32 是有符号扩展
// 到 64 位的立即数;调用方负责确保 imm 在 [-2^31, 2^31) 内。
//
// PJ4+ 用例:IsNumber guard 的核心比较——「cmp rax, NaNBoxBase; jae .deopt」
// 是 NaN-box 单 u64 比较模式(承 03 §2.2 + design-premises 前提四)。当前
// imm32 实装够用 NaN-box 边界判定的高 32 位场景;扩 imm64 留 PJ4 实装。
func EmitCmpRaxImm32(buf []byte, imm int32) []byte {
	buf = append(buf, 0x48, 0x3d)
	for i := 0; i < 4; i++ {
		buf = append(buf, byte(imm>>(8*i)))
	}
	return buf
}

// EmitJmpRel32 发射「jmp rel32」5 字节序列(承 06 §3.4 JMP 直跳)。
//
// amd64 编码:e9 ii ii ii ii(JMP rel32,32 位有符号偏移,从下条指令起算)。
//
// PJ4+ 用例:JMP 指令翻译——目标 PC 的机器地址在编译期算好(forwardJump
// fixup 表,承 06 §2.2.1 PatchJump 协议)写入 rel32。调用方负责保证 imm
// 是「目标地址 - (本指令地址 + 5)」。
func EmitJmpRel32(buf []byte, rel32 int32) []byte {
	buf = append(buf, 0xe9)
	for i := 0; i < 4; i++ {
		buf = append(buf, byte(rel32>>(8*i)))
	}
	return buf
}

// EmitJaeRel32 发射「jae rel32」6 字节序列(承 06 §3.2 IsNumber guard 出口)。
//
// amd64 编码:0f 83 ii ii ii ii(JAE rel32 = if CF=0 jump,即「>=」无符号
// 比较跳)。
//
// PJ4+ 用例:IsNumber guard 失败跳 OSR exit——「cmp rax, NaNBoxBase; jae
// .deopt」(rax >= NaNBoxBase 表 rax 是 boxed 非数字 ⇒ 投机失败)。
func EmitJaeRel32(buf []byte, rel32 int32) []byte {
	buf = append(buf, 0x0f, 0x83)
	for i := 0; i < 4; i++ {
		buf = append(buf, byte(rel32>>(8*i)))
	}
	return buf
}

// EncodedCmpRaxImm32Len 是「cmp rax, imm32」编码后的字节数(常量,固定 6:
// REX.W 1 字节 + opcode 1 字节 + imm32 4 字节)。
const EncodedCmpRaxImm32Len = 6

// EncodedJmpRel32Len 是「jmp rel32」编码后的字节数(常量,固定 5)。
const EncodedJmpRel32Len = 5

// EncodedJaeRel32Len 是「jae rel32」编码后的字节数(常量,固定 6)。
const EncodedJaeRel32Len = 6

// =============================================================================
// PJ5+ 调用族 emitter 原语(承 06 §3.5 CALL/TAILCALL/RETURN 调用族)
// =============================================================================

// EmitCallRel32 发射「call rel32」5 字节序列(承 06 §3.5 helper 调用基础)。
//
// amd64 编码:e8 ii ii ii ii(CALL rel32,32 位有符号偏移,从下条指令起算)。
//
// PJ5+ 用例:gibbous-jit→host helper 调用——helper 函数地址在 jitContext
// helper 表(承 05 §4.3),编译期算好 rel32 = helperAddr - (本指令地址 + 5)。
// 但 helper 通常远超 ±2GB 范围,实际实装是「mov rax, helperAddr; call rax」
// (间接 CALL,留 PJ5+ 加 EmitCallReg)。本原语保留作 fallback。
func EmitCallRel32(buf []byte, rel32 int32) []byte {
	buf = append(buf, 0xe8)
	for i := 0; i < 4; i++ {
		buf = append(buf, byte(rel32>>(8*i)))
	}
	return buf
}

// EmitCallReg 发射「call regN」2 字节序列(承 06 §3.5 间接 CALL helper)。
//
// amd64 编码:ff (d0 + regN)(CALL r/m64,FF /2,reg field encoded in modrm)。
// 仅低 8 个寄存器(RAX-RDI);reg=4(RSP)语义不可用(承审查 🟢 #2)防御。
func EmitCallReg(buf []byte, regNum uint8) []byte {
	if regNum > 7 || regNum == 4 {
		regNum = 0 // RSP/超界兜底为 RAX
	}
	buf = append(buf, 0xff, 0xd0|regNum)
	return buf
}

// EmitPushReg 发射「push regN」1 字节序列。reg=4(RSP)/ reg=5(RBP)语义
// 危险——RBP 已被 trampoline 序言保存,业务码不该改;RSP push 无意义。本
// 函数对 4/5 兜底为 RAX(0)。
func EmitPushReg(buf []byte, regNum uint8) []byte {
	if regNum > 7 || regNum == 4 || regNum == 5 {
		regNum = 0
	}
	buf = append(buf, 0x50|regNum)
	return buf
}

// EmitPopReg 发射「pop regN」1 字节序列(对位 EmitPushReg 出栈)。
// reg=4/5 同 EmitPushReg 防御。
func EmitPopReg(buf []byte, regNum uint8) []byte {
	if regNum > 7 || regNum == 4 || regNum == 5 {
		regNum = 0
	}
	buf = append(buf, 0x58|regNum)
	return buf
}

// EncodedCallRel32Len 是「call rel32」编码后的字节数(常量,固定 5)。
const EncodedCallRel32Len = 5

// EncodedCallRegLen 是「call regN」编码后的字节数(常量,固定 2)。
const EncodedCallRegLen = 2

// EncodedPushRegLen 是「push regN」编码后的字节数(常量,固定 1,低 8 寄存器)。
const EncodedPushRegLen = 1

// EncodedPopRegLen 是「pop regN」编码后的字节数(常量,固定 1)。
const EncodedPopRegLen = 1

// =============================================================================
// PJ6+ 模板组合原语(承 06 §3.6 闭包族 + §3.7 直线族 + §3.5 RETURN)
// =============================================================================

// EmitLoadKReturnTemplate 发射「LOADK A K(0); RETURN A 1」完整模板(11 字节)。
//
// 等价于 EmitMovRaxImm64(buf, konst) + EmitRet(buf),但作为命名模板暴露,
// 调用方易读。
//
// PJ6+ 用例:Compile 路径核心模板——单 BB「return CONST」直接调用本函数,
// 不必逐原语拼接。
func EmitLoadKReturnTemplate(buf []byte, konst uint64) []byte {
	buf = EmitMovRaxImm64(buf, konst)
	buf = EmitRet(buf)
	return buf
}

// EncodedLoadKReturnTemplateLen 「LOADK + RETURN 单 BB」模板字节数(11)。
const EncodedLoadKReturnTemplateLen = EncodedMovRaxImm64Len + EncodedRetLen

// EmitProlog 发射 trampoline 进入序言简化版(push rbx + push rbp,2 字节)。
//
// **与 trampoline_full_amd64.s 的关系**:本 emitter 原语仅作 emit 接口对齐
// (让 jit.Compile 在「需要保存 callee-saved 后跑模板」时按需经 emit 路径
// 生成 trampoline 序言);**完整 5 寄存器序言**(push rbx/rbp/r12/r13/r15,
// r14=Go G 不动)在 trampoline_full_amd64.s 直接实装。本简化版只覆盖低 8
// 寄存器(rbx/rbp);r12-r15 需 REX.B 前缀,留 PJ7+ 加 EmitPushRegHi 扩。
//
// **绕过 EmitPushReg/EmitPopReg 的 RBP 防御**:trampoline 序言保存 RBP 是
// callee-saved 协议合法用法(出口 pop 恢复),与业务码改 RBP 不同。直接发
// push 字节(0x55 = push rbp / 0x53 = push rbx)。
func EmitProlog(buf []byte) []byte {
	buf = append(buf, 0x53) // push rbx
	buf = append(buf, 0x55) // push rbp
	return buf
}

// EmitEpilog 发射 trampoline 出口序言(对位 EmitProlog,逆序 pop)。
func EmitEpilog(buf []byte) []byte {
	buf = append(buf, 0x5d) // pop rbp
	buf = append(buf, 0x5b) // pop rbx
	return buf
}

// EncodedPrologLen 是 EmitProlog 字节数(2,简化版)。
const EncodedPrologLen = 2

// EncodedEpilogLen 是 EmitEpilog 字节数(2,简化版)。
const EncodedEpilogLen = 2

// --- PJ2 字节级算术发射原语 ---
//
// 承 docs/design/p4-method-jit/03-speculation-ic.md §2 IsNumber×2 投机模板
// + 06-backends.md §3.2 amd64 算术族:双 number 快路径直发 SSE2 浮点指令
// (movsd / addsd / subsd / mulsd / divsd),无需调 host helper。
//
// **PJ2 物理基础**(本节原语本身可用,但完整投机模板需 jitContext 切 SP +
// 寄存器分配 + IsNumber guard codegen,留 PJ2-PJ5 完整版接入)。

// EmitMovsdXmmFromMem 发射「movsd xmm0, [reg+disp32]」从内存加载 64-bit
// double 到 xmm0。指令:F2 REX 0F 10 /0 modrm + disp32(8 字节)。
//
// 参数 baseReg 是基址寄存器号([0,7] 低 8 寄存器,高 8 需 REX.B 留 PJ3+)。
// disp32 是有符号 32-bit 偏移。
//
// 编码:F2 0F 10 80+baseReg disp32(若 baseReg<8 + 不需 REX.W;movsd 是
// SSE2 指令,xmm 寄存器编码不需要 REX.W)。
func EmitMovsdXmmFromMem(buf []byte, xmmDst uint8, baseReg uint8, disp32 int32) []byte {
	// 防御性兜底:xmm 范围 [0,7],base 范围 [0,7](高 8 寄存器留 PJ3+)
	if xmmDst > 7 {
		xmmDst = 0
	}
	if baseReg > 7 {
		baseReg = 0
	}
	// F2 prefix(scalar double),0F 10 = MOVSD xmm, xmm/m64
	buf = append(buf, 0xF2, 0x0F, 0x10)
	// modrm:mod=10(disp32) reg=xmmDst rm=baseReg
	modrm := byte(0x80) | (xmmDst&0x7)<<3 | (baseReg & 0x7)
	buf = append(buf, modrm)
	// disp32 LE
	buf = append(buf,
		byte(uint32(disp32)),
		byte(uint32(disp32)>>8),
		byte(uint32(disp32)>>16),
		byte(uint32(disp32)>>24))
	return buf
}

// EmitMovsdMemFromXmm 发射「movsd [reg+disp32], xmm0」存 xmm0 到内存。
//
// 指令:F2 0F 11 modrm + disp32(8 字节)。
func EmitMovsdMemFromXmm(buf []byte, xmmSrc uint8, baseReg uint8, disp32 int32) []byte {
	if xmmSrc > 7 {
		xmmSrc = 0
	}
	if baseReg > 7 {
		baseReg = 0
	}
	// F2 0F 11 = MOVSD xmm/m64, xmm
	buf = append(buf, 0xF2, 0x0F, 0x11)
	modrm := byte(0x80) | (xmmSrc&0x7)<<3 | (baseReg & 0x7)
	buf = append(buf, modrm)
	buf = append(buf,
		byte(uint32(disp32)),
		byte(uint32(disp32)>>8),
		byte(uint32(disp32)>>16),
		byte(uint32(disp32)>>24))
	return buf
}

// EmitAddsdXmmXmm 发射「addsd xmmDst, xmmSrc」(xmm 双 double 加,4 字节)。
// 指令:F2 0F 58 modrm。
func EmitAddsdXmmXmm(buf []byte, xmmDst uint8, xmmSrc uint8) []byte {
	if xmmDst > 7 {
		xmmDst = 0
	}
	if xmmSrc > 7 {
		xmmSrc = 0
	}
	buf = append(buf, 0xF2, 0x0F, 0x58)
	modrm := byte(0xC0) | (xmmDst&0x7)<<3 | (xmmSrc & 0x7)
	buf = append(buf, modrm)
	return buf
}

// EmitSubsdXmmXmm 发射「subsd xmmDst, xmmSrc」(指令:F2 0F 5C modrm)。
func EmitSubsdXmmXmm(buf []byte, xmmDst uint8, xmmSrc uint8) []byte {
	if xmmDst > 7 {
		xmmDst = 0
	}
	if xmmSrc > 7 {
		xmmSrc = 0
	}
	buf = append(buf, 0xF2, 0x0F, 0x5C)
	modrm := byte(0xC0) | (xmmDst&0x7)<<3 | (xmmSrc & 0x7)
	buf = append(buf, modrm)
	return buf
}

// EmitMulsdXmmXmm 发射「mulsd xmmDst, xmmSrc」(指令:F2 0F 59 modrm)。
func EmitMulsdXmmXmm(buf []byte, xmmDst uint8, xmmSrc uint8) []byte {
	if xmmDst > 7 {
		xmmDst = 0
	}
	if xmmSrc > 7 {
		xmmSrc = 0
	}
	buf = append(buf, 0xF2, 0x0F, 0x59)
	modrm := byte(0xC0) | (xmmDst&0x7)<<3 | (xmmSrc & 0x7)
	buf = append(buf, modrm)
	return buf
}

// EmitDivsdXmmXmm 发射「divsd xmmDst, xmmSrc」(指令:F2 0F 5E modrm)。
func EmitDivsdXmmXmm(buf []byte, xmmDst uint8, xmmSrc uint8) []byte {
	if xmmDst > 7 {
		xmmDst = 0
	}
	if xmmSrc > 7 {
		xmmSrc = 0
	}
	buf = append(buf, 0xF2, 0x0F, 0x5E)
	modrm := byte(0xC0) | (xmmDst&0x7)<<3 | (xmmSrc & 0x7)
	buf = append(buf, modrm)
	return buf
}

// EncodedMovsdMemLen 是 MOVSD xmm <-> [base+disp32] 序列字节数(8)。
const EncodedMovsdMemLen = 8

// EncodedSseBinopLen 是 ADDSD/SUBSD/MULSD/DIVSD xmm,xmm 字节数(4)。
const EncodedSseBinopLen = 4

// EmitMovqRaxFromR15Disp 发射「mov rax, [r15+disp32]」从 r15+disp32 加载
// 64-bit 到 rax(指令:4C 是 REX.WR 不对,我们用 REX.B=1 base=r15;
// 实际编码 49 8B 87 disp32 = REX.W+B 8B /0 modrm)。
//
// 用例:PJ2 完整投机模板——mmap 段经 r15 读 jitContext 字段
// (arenaBase / valueStackBase / preemptFlag 等)。
//
// 编码:49 8B 87 disp32(7 字节)。
//   - 49 = REX prefix(W=1 64-bit + B=1 让 rm 字段用 r15 而非 r7)
//   - 8B = MOV r64, r/m64
//   - 87 = ModR/M:mod=10(disp32) reg=000(rax) rm=111(r15 with REX.B)
func EmitMovqRaxFromR15Disp(buf []byte, disp32 int32) []byte {
	// REX.W (0x48) | REX.B (0x01) = 0x49
	buf = append(buf, 0x49, 0x8B, 0x87)
	buf = append(buf,
		byte(uint32(disp32)),
		byte(uint32(disp32)>>8),
		byte(uint32(disp32)>>16),
		byte(uint32(disp32)>>24))
	return buf
}

// EmitMovqRaxFromMemReg 发射「mov rax, [reg+disp32]」从指定基址寄存器
// 加载到 rax(用于读 valueStackBase + reg*8 的值栈槽——但需要先把
// valueStackBase 装到某 base 寄存器)。
//
// 编码示例:48 8B 80+rd disp32(REX.W=1 不需 REX.B,reg<8)。
// 仅支持低 8 寄存器(rax-rdi,reg<8)——高 8 寄存器需 REX.B 留 PJ3+。
//
// **注**:本原语单纯读寄存器+偏移,不做 SIB 寻址(无 [base+index*8]),
// 故不能直接发「mov rax, [valueStackBase + reg_idx*8]」(那需要 SIB)。
// PJ2 简化策略是把 reg_idx*8 计算放在 Go 端(emit 时算 disp32 = idx*8),
// mmap 段只需 base+disp32 寻址。
func EmitMovqRaxFromMemReg(buf []byte, baseReg uint8, disp32 int32) []byte {
	if baseReg > 7 {
		baseReg = 0
	}
	buf = append(buf, 0x48, 0x8B)
	modrm := byte(0x80) | (baseReg & 0x7) // mod=10 reg=000(rax) rm=baseReg
	buf = append(buf, modrm)
	buf = append(buf,
		byte(uint32(disp32)),
		byte(uint32(disp32)>>8),
		byte(uint32(disp32)>>16),
		byte(uint32(disp32)>>24))
	return buf
}

// EncodedMovqFromR15DispLen 是「mov rax, [r15+disp32]」字节数(7)。
const EncodedMovqFromR15DispLen = 7

// EncodedMovqFromMemRegLen 是「mov rax, [low_reg+disp32]」字节数(7)。
const EncodedMovqFromMemRegLen = 7

// EmitMovqMemRegFromRax 发射「mov [reg+disp32], rax」存 rax 到内存。
// 编码:48 89 80+r disp32(7 字节)。
func EmitMovqMemRegFromRax(buf []byte, baseReg uint8, disp32 int32) []byte {
	if baseReg > 7 {
		baseReg = 0
	}
	buf = append(buf, 0x48, 0x89)
	modrm := byte(0x80) | (baseReg & 0x7)
	buf = append(buf, modrm)
	buf = append(buf,
		byte(uint32(disp32)),
		byte(uint32(disp32)>>8),
		byte(uint32(disp32)>>16),
		byte(uint32(disp32)>>24))
	return buf
}

// EmitUcomisdXmmXmm 发射「ucomisd xmmDst, xmmSrc」(无序比较 SD,设置 ZF/PF/CF)。
// 用于 IsNumber guard 的 NaN 检测后续 jcc。
// 指令:66 0F 2E modrm(4 字节)。
func EmitUcomisdXmmXmm(buf []byte, xmmDst uint8, xmmSrc uint8) []byte {
	if xmmDst > 7 {
		xmmDst = 0
	}
	if xmmSrc > 7 {
		xmmSrc = 0
	}
	buf = append(buf, 0x66, 0x0F, 0x2E)
	modrm := byte(0xC0) | (xmmDst&0x7)<<3 | (xmmSrc & 0x7)
	buf = append(buf, modrm)
	return buf
}

// EmitJeRel32 发射「je rel32」(0F 84 rel32,6 字节)等条件跳转。
func EmitJeRel32(buf []byte, rel32 int32) []byte {
	buf = append(buf, 0x0F, 0x84)
	buf = append(buf,
		byte(uint32(rel32)),
		byte(uint32(rel32)>>8),
		byte(uint32(rel32)>>16),
		byte(uint32(rel32)>>24))
	return buf
}

// EmitJneRel32 发射「jne rel32」(0F 85 rel32)。
func EmitJneRel32(buf []byte, rel32 int32) []byte {
	buf = append(buf, 0x0F, 0x85)
	buf = append(buf,
		byte(uint32(rel32)),
		byte(uint32(rel32)>>8),
		byte(uint32(rel32)>>16),
		byte(uint32(rel32)>>24))
	return buf
}

// EmitJbRel32 发射「jb rel32」(0F 82 rel32,unsigned <)。
func EmitJbRel32(buf []byte, rel32 int32) []byte {
	buf = append(buf, 0x0F, 0x82)
	buf = append(buf,
		byte(uint32(rel32)),
		byte(uint32(rel32)>>8),
		byte(uint32(rel32)>>16),
		byte(uint32(rel32)>>24))
	return buf
}

// EmitJbeRel32 发射「jbe rel32」(0F 86 rel32,unsigned <=)。
func EmitJbeRel32(buf []byte, rel32 int32) []byte {
	buf = append(buf, 0x0F, 0x86)
	buf = append(buf,
		byte(uint32(rel32)),
		byte(uint32(rel32)>>8),
		byte(uint32(rel32)>>16),
		byte(uint32(rel32)>>24))
	return buf
}

// EmitJaRel32 发射「ja rel32」(0F 87 rel32,unsigned >)。
func EmitJaRel32(buf []byte, rel32 int32) []byte {
	buf = append(buf, 0x0F, 0x87)
	buf = append(buf,
		byte(uint32(rel32)),
		byte(uint32(rel32)>>8),
		byte(uint32(rel32)>>16),
		byte(uint32(rel32)>>24))
	return buf
}

// EncodedMovqMemFromRaxLen 是「mov [reg+disp32], rax」字节数(7)。
const EncodedMovqMemFromRaxLen = 7

// EncodedUcomisdLen 是「ucomisd xmm,xmm」字节数(4)。
const EncodedUcomisdLen = 4

// EncodedJccRel32Len 是 0F 8x rel32 条件跳转字节数(6)。
const EncodedJccRel32Len = 6

// EmitMovRcxImm64 发射「mov rcx, imm64」(REX.W + B9+rd imm64,10 字节)。
// 用于装 NaN-box 阈值常量到 rcx 后做 cmp rax, rcx 比较。
func EmitMovRcxImm64(buf []byte, imm uint64) []byte {
	buf = append(buf, 0x48, 0xB9) // REX.W mov rcx, imm64
	buf = append(buf,
		byte(imm), byte(imm>>8), byte(imm>>16), byte(imm>>24),
		byte(imm>>32), byte(imm>>40), byte(imm>>48), byte(imm>>56))
	return buf
}

// EmitCmpRaxRcx 发射「cmp rax, rcx」(REX.W + 39 modrm,3 字节)。
// 编码:48 39 C8(modrm:mod=11 reg=001=rcx rm=000=rax)。
func EmitCmpRaxRcx(buf []byte) []byte {
	return append(buf, 0x48, 0x39, 0xC8)
}

// EncodedMovRcxImm64Len 是「mov rcx, imm64」字节数(10)。
const EncodedMovRcxImm64Len = 10

// EncodedCmpRaxRcxLen 是「cmp rax, rcx」字节数(3)。
const EncodedCmpRaxRcxLen = 3
