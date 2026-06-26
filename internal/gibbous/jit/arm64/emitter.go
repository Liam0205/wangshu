//go:build wangshu_p4 && arm64

package arm64

import "encoding/binary"

// EmitMovX0Imm64 发射 arm64「mov x0, imm64」序列(承 06-backends.md §4.2 +
// §3.7 直线族 arm64 端镜像)。
//
// arm64 编码:movz + 3×movk 序列(每条 4 字节,共 16 字节)。
//
//	movz x0, imm[15:0]      ; 1 1010010 100 imm16 00000  → 0xd2800000 + imm0<<5
//	movk x0, imm[31:16] LSL #16
//	movk x0, imm[47:32] LSL #32
//	movk x0, imm[63:48] LSL #48
//
// hw(shift)字段:00=LSL #0, 01=LSL #16, 10=LSL #32, 11=LSL #48
//
// 完整编码(每条 32-bit,LE 写入):
//   - movz Xd, #imm:1101_0010_1hw_iiii_iiii_iiii_iiii_id_dd_dd
//     (sf=1, opc=10 movz, hw=00, imm16=...)
//   - movk Xd, #imm, LSL hw:1111_0010_1hw_iiii_iiii_iiii_iiii_id_dd_dd
//     (sf=1, opc=11 movk, hw=01/10/11)
//
// 用 X0 作目标(d=0)——arm64 默认返回值寄存器。
func EmitMovX0Imm64(buf []byte, imm uint64) []byte {
	// movz X0, imm[15:0]
	buf = appendArm64Insn(buf, encodeMovzMovk(false, 0, 0, uint16(imm)))
	// movk X0, imm[31:16] LSL 16
	buf = appendArm64Insn(buf, encodeMovzMovk(true, 0, 1, uint16(imm>>16)))
	// movk X0, imm[47:32] LSL 32
	buf = appendArm64Insn(buf, encodeMovzMovk(true, 0, 2, uint16(imm>>32)))
	// movk X0, imm[63:48] LSL 48
	buf = appendArm64Insn(buf, encodeMovzMovk(true, 0, 3, uint16(imm>>48)))
	return buf
}

// EmitRet 发射 arm64「ret」(返 LR/X30)4 字节(0xd65f03c0)。
func EmitRet(buf []byte) []byte {
	return appendArm64Insn(buf, 0xd65f03c0)
}

// encodeMovzMovk 编码 movz/movk 32-bit instruction word。
//
// movz: sf=1 opc=10 100101 hw imm16 Rd  → bit pattern 0xD2800000 base
// movk: sf=1 opc=11 100101 hw imm16 Rd  → bit pattern 0xF2800000 base
func encodeMovzMovk(isMovk bool, rd uint8, hw uint8, imm uint16) uint32 {
	var base uint32
	if isMovk {
		base = 0xF2800000
	} else {
		base = 0xD2800000
	}
	return base | (uint32(hw)&0x3)<<21 | uint32(imm)<<5 | uint32(rd)&0x1F
}

func appendArm64Insn(buf []byte, insn uint32) []byte {
	var b4 [4]byte
	binary.LittleEndian.PutUint32(b4[:], insn)
	return append(buf, b4[:]...)
}

// EncodedMovX0Imm64Len arm64 mov x0, imm64 序列字节数(4 × 4 = 16)。
const EncodedMovX0Imm64Len = 16

// EncodedRetLen arm64 ret 字节数(4)。
const EncodedRetLen = 4

// EmitMovXdImm64 发射 arm64「mov Xd, imm64」序列(通用 Xd 寄存器,
// 不仅 X0)。reg 范围 [0, 30](X31 = SP/XZR 特殊不在此通用编码内)。
//
// 编码同 EmitMovX0Imm64 但允许选不同 Xd。
func EmitMovXdImm64(buf []byte, rd uint8, imm uint64) []byte {
	if rd > 30 {
		rd = 0
	}
	buf = appendArm64Insn(buf, encodeMovzMovk(false, rd, 0, uint16(imm)))
	buf = appendArm64Insn(buf, encodeMovzMovk(true, rd, 1, uint16(imm>>16)))
	buf = appendArm64Insn(buf, encodeMovzMovk(true, rd, 2, uint16(imm>>32)))
	buf = appendArm64Insn(buf, encodeMovzMovk(true, rd, 3, uint16(imm>>48)))
	return buf
}

// EncodedMovXdImm64Len = 16(同 EncodedMovX0Imm64Len)。
const EncodedMovXdImm64Len = 16

// EmitMovXdFromXn 发射 arm64「mov Xd, Xn」(register move)。
// 实际编码:ORR Xd, XZR, Xn = 1010_1010_000n_nnnn_0000_0000_000d_dddd
// = 0xAA000000 | (rn << 16) | (31 << 5) | rd  ; XZR = R31
//
// 注:arm64 没有专门的 MOV reg-reg 指令,用 ORR with XZR 实现(汇编器
// 的语法糖)。
func EmitMovXdFromXn(buf []byte, rd, rn uint8) []byte {
	if rd > 30 {
		rd = 0
	}
	if rn > 30 {
		rn = 0
	}
	// ORR Xd, XZR(=31), Xn:0xAA000000 + (rn << 16) + (31 << 5) + rd
	insn := uint32(0xAA000000) | (uint32(rn)&0x1F)<<16 | (uint32(31)&0x1F)<<5 | uint32(rd)&0x1F
	return appendArm64Insn(buf, insn)
}

// EncodedMovXdFromXnLen = 4(arm64 一条指令)。
const EncodedMovXdFromXnLen = 4

// EmitAddXdImm12 发射 arm64「add Xd, Xn, #imm12」(unsigned 12-bit imm).
// 编码:1001_0001_00_iiiiiiiiiiii_nnnnn_ddddd = 0x91000000 base
//   - sf=1, op=0(add), S=0
//   - imm12 <= 0xFFF
//
// 用于段内累加 / 寄存器算术。
func EmitAddXdImm12(buf []byte, rd, rn uint8, imm12 uint16) []byte {
	if rd > 30 {
		rd = 0
	}
	if rn > 30 {
		rn = 0
	}
	if imm12 > 0xFFF {
		imm12 = 0xFFF
	}
	insn := uint32(0x91000000) | (uint32(imm12)&0xFFF)<<10 | (uint32(rn)&0x1F)<<5 | uint32(rd)&0x1F
	return appendArm64Insn(buf, insn)
}

// EncodedAddXdImm12Len = 4.
const EncodedAddXdImm12Len = 4

// EmitSubXdImm12 发射 arm64「sub Xd, Xn, #imm12」。
// 编码:1101_0001_00_iiiiiiiiiiii_nnnnn_ddddd = 0xD1000000 base.
func EmitSubXdImm12(buf []byte, rd, rn uint8, imm12 uint16) []byte {
	if rd > 30 {
		rd = 0
	}
	if rn > 30 {
		rn = 0
	}
	if imm12 > 0xFFF {
		imm12 = 0xFFF
	}
	insn := uint32(0xD1000000) | (uint32(imm12)&0xFFF)<<10 | (uint32(rn)&0x1F)<<5 | uint32(rd)&0x1F
	return appendArm64Insn(buf, insn)
}

// EncodedSubXdImm12Len = 4.
const EncodedSubXdImm12Len = 4

// EmitB 发射 arm64「b label」无条件跳转,imm26 是字数偏移(目标地址 =
// PC + imm26 * 4)。imm26 是有符号 [-2^25, 2^25-1]。
//
// 编码:0001_01_iiii_iiii_iiii_iiii_iiii_iiii_ii = 0x14000000 base
func EmitB(buf []byte, imm26 int32) []byte {
	insn := uint32(0x14000000) | uint32(imm26)&0x03FFFFFF
	return appendArm64Insn(buf, insn)
}

// EncodedBLen = 4.
const EncodedBLen = 4

// EmitLdrXtFromXnDisp 发射 arm64「ldr Xt, [Xn, #pimm12]」64-bit load with
// unsigned 12-bit offset(承 ARMv8 ARM A6.2 + 06-backends.md §4.2 arm64
// load 原语)。
//
// arm64 编码:1111_1001_01_iiiiiiiiiiii_nnnnn_ttttt = 0xF9400000 base
//   - size=11(64-bit)
//   - V=0(general register)
//   - opc=01(LDR unsigned offset)
//   - imm12 是**8-byte scaled offset**(byte offset = imm12 * 8),范围
//     [0, 32760],步长 8
//
// **参数 byteOff** 是字节偏移(必须 8 字节对齐 + ≤ 32760)。本函数自动
// 除以 8 编入 imm12 字段。超界或非对齐兜底 imm12=0(load Xt from [Xn+0])。
//
// 用例:PJ4 表 IC inline arm64 端——load arena base / table.word5 /
// arrayRef / NodeKey/Val 等(对位 amd64 `mov rax, [r14+rcx+disp]` 8 字节)。
func EmitLdrXtFromXnDisp(buf []byte, rt, rn uint8, byteOff uint16) []byte {
	if rt > 30 {
		rt = 0
	}
	if rn > 30 {
		rn = 0
	}
	// byteOff 必须 8 字节对齐 + ≤ 32760(imm12 = byteOff/8 ≤ 4095)
	if byteOff%8 != 0 || byteOff > 32760 {
		byteOff = 0
	}
	imm12 := uint32(byteOff / 8)
	insn := uint32(0xF9400000) | (imm12&0xFFF)<<10 | (uint32(rn)&0x1F)<<5 | uint32(rt)&0x1F
	return appendArm64Insn(buf, insn)
}

// EncodedLdrXtFromXnDispLen = 4(arm64 一条指令)。
const EncodedLdrXtFromXnDispLen = 4

// EmitStrXtToXnDisp 发射 arm64「str Xt, [Xn, #pimm12]」64-bit store with
// unsigned 12-bit offset(对位 EmitLdrXtFromXnDisp,反向写)。
//
// arm64 编码:1111_1001_00_iiiiiiiiiiii_nnnnn_ttttt = 0xF9000000 base
//   - size=11(64-bit),V=0,opc=00(STR unsigned offset)
//   - imm12 同 LDR 是 8-byte scaled offset
//
// 用例:PJ4 表 IC SETTABLE arm64 端——反向 store NodeVal / array[idx]
// (对位 amd64 `mov [r14+rcx+disp], rdx` 8 字节)。
func EmitStrXtToXnDisp(buf []byte, rt, rn uint8, byteOff uint16) []byte {
	if rt > 30 {
		rt = 0
	}
	if rn > 30 {
		rn = 0
	}
	if byteOff%8 != 0 || byteOff > 32760 {
		byteOff = 0
	}
	imm12 := uint32(byteOff / 8)
	insn := uint32(0xF9000000) | (imm12&0xFFF)<<10 | (uint32(rn)&0x1F)<<5 | uint32(rt)&0x1F
	return appendArm64Insn(buf, insn)
}

// EncodedStrXtToXnDispLen = 4.
const EncodedStrXtToXnDispLen = 4

// EmitCmpXnXm 发射 arm64「cmp Xn, Xm」(寄存器比较,设 NZCV flags 不写
// 结果)。实际编码 = SUBS XZR, Xn, Xm:
//
// 编码:1110_1011_000_mmmmm_000000_nnnnn_11111 = 0xEB00001F base
//   - sf=1, op=1(sub), S=1(SUBS, set flags), shift=00
//   - Rd=11111(XZR,丢结果只设 flag)
//
// **参数 rn/rm** 均为 0-30。超界兜底 0。
//
// 用例:PJ4 表 IC inline arm64 端——验 NodeKey == stableKey / gen ==
// stableShape 等(对位 amd64 `cmp rax, rdx` 3 字节)。
func EmitCmpXnXm(buf []byte, rn, rm uint8) []byte {
	if rn > 30 {
		rn = 0
	}
	if rm > 30 {
		rm = 0
	}
	insn := uint32(0xEB00001F) | (uint32(rm)&0x1F)<<16 | (uint32(rn)&0x1F)<<5
	return appendArm64Insn(buf, insn)
}

// EncodedCmpXnXmLen = 4.
const EncodedCmpXnXmLen = 4

// EmitBCond 发射 arm64「b.cond label」条件分支(承 ARMv8 ARM C6.2.B.cond)。
// imm19 是字数偏移(目标地址 = PC + imm19 * 4),范围 [-2^20, 2^20-1)。
//
// 编码:0101_0100_iiiiiiiiiiiiiiiiiiii_0_cond = 0x54000000 base
//   - imm19 sign-extended 字偏移(LSL 2 → byte offset)
//   - cond 4-bit 条件码
//
// 常用 cond 码(承 ARMv8 ARM C1.2 Condition codes):
//   - 0x0 EQ(equal)/ 0x1 NE(not equal)
//   - 0x2 CS/HS(carry set, unsigned >=)/ 0x3 CC/LO(carry clear, unsigned <)
//   - 0x4 MI(minus)/ 0x5 PL(plus or zero)
//   - 0x6 VS(overflow)/ 0x7 VC(no overflow)
//   - 0x8 HI(unsigned >)/ 0x9 LS(unsigned <=)
//   - 0xA GE(signed >=)/ 0xB LT(signed <)
//   - 0xC GT(signed >)/ 0xD LE(signed <=)
//
// 用例:PJ4 IC inline arm64 端——`cmp` 后的 deopt branch(对位 amd64
// jne/je rel32 6 字节)。
func EmitBCond(buf []byte, cond uint8, imm19 int32) []byte {
	if cond > 0xF {
		cond = 0
	}
	insn := uint32(0x54000000) | (uint32(imm19)&0x7FFFF)<<5 | uint32(cond)&0xF
	return appendArm64Insn(buf, insn)
}

// EncodedBCondLen = 4.
const EncodedBCondLen = 4

// arm64 condition codes(承 ARMv8 ARM C1.2)。
const (
	CondEQ uint8 = 0x0 // equal
	CondNE uint8 = 0x1 // not equal
	CondHS uint8 = 0x2 // unsigned >=
	CondLO uint8 = 0x3 // unsigned <
	CondMI uint8 = 0x4 // negative
	CondPL uint8 = 0x5 // positive or zero
	CondHI uint8 = 0x8 // unsigned >
	CondLS uint8 = 0x9 // unsigned <=
	CondGE uint8 = 0xA // signed >=
	CondLT uint8 = 0xB // signed <
	CondGT uint8 = 0xC // signed >
	CondLE uint8 = 0xD // signed <=
)
