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
