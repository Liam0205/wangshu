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
