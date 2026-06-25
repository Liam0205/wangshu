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
