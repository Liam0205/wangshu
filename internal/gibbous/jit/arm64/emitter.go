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
