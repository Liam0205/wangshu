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

// =============================================================================
// arm64 浮点 emit 原语(承 06-backends.md §4.2 + ARMv8 ARM C7.2 FP/SIMD)
// =============================================================================
//
// **arm64 vs amd64 浮点对位**:
//   - amd64 SSE2 用 xmm0-xmm15 寄存器,操作 movsd / addsd / subsd / mulsd /
//     divsd / ucomisd 等(F2 0F xx C0+reg 5 字节)
//   - arm64 用 D0-D31 寄存器(双精度),操作 FMOV / FADD / FSUB / FMUL /
//     FDIV / FCMPE 等(每条 4 字节)
//
// 双精度 NaN-box 转换:Lua number 是 NaN-box uint64(高 16 位标 0x7FF8 普通
// number / 高 16 位是 NaN-box tag 是 GCRef)。FMOV Dd, Xn 从 GP 寄存器位级
// 复制到 FP 寄存器,语义等价(IEEE 754 unbox)。

// EmitFmovDdFromXn 发射 arm64「fmov Dd, Xn」(GP→FP 64-bit 位级复制)。
//
// 编码:1001_1110_0110_0111_0000_00nn_nnnn_dddd = 0x9E670000 + Rn<<5 + Rd
//
// 用例:PJ2 投机模板 arm64 端——把值栈 R(B) load 到 X0(经 LDR)后
// fmov 到 D0,然后 FADD D0, D0, D1(D1 来自 R(C))算 R(A) = R(B)+R(C)。
// 对位 amd64 movsd xmm0, [rbx+B*8] + addsd xmm0, [rbx+C*8]。
func EmitFmovDdFromXn(buf []byte, dd, xn uint8) []byte {
	if dd > 31 {
		dd = 0
	}
	if xn > 30 {
		xn = 0
	}
	insn := uint32(0x9E670000) | (uint32(xn)&0x1F)<<5 | uint32(dd)&0x1F
	return appendArm64Insn(buf, insn)
}

// EncodedFmovDdFromXnLen = 4.
const EncodedFmovDdFromXnLen = 4

// EmitFmovXdFromDn 发射 arm64「fmov Xd, Dn」(FP→GP 位级复制,对位
// EmitFmovDdFromXn)。
//
// 编码:1001_1110_0110_0110_0000_00nn_nnnn_dddd = 0x9E660000 + Rn<<5 + Rd
//
// 用例:PJ2 投机模板 arm64 端 store 回值栈——FADD D0... 后 fmov X0, D0,
// 然后 STR X0, [Rb_vsBase + A*8]。对位 amd64 movsd [rbx+A*8], xmm0。
func EmitFmovXdFromDn(buf []byte, xd, dn uint8) []byte {
	if xd > 30 {
		xd = 0
	}
	if dn > 31 {
		dn = 0
	}
	insn := uint32(0x9E660000) | (uint32(dn)&0x1F)<<5 | uint32(xd)&0x1F
	return appendArm64Insn(buf, insn)
}

// EncodedFmovXdFromDnLen = 4.
const EncodedFmovXdFromDnLen = 4

// EmitFaddDdDnDm 发射 arm64「fadd Dd, Dn, Dm」(双精度加,IEEE 754)。
//
// 编码:0001_1110_011_mmmmm_001010_nnnnn_ddddd = 0x1E602800 + Rm<<16 + Rn<<5 + Rd
//   - size=01(double precision),op=0010(FADD),Rm 操作数 2
//
// 用例:PJ2 投机模板 arm64 端核心算术(对位 amd64 ADDSD F2 0F 58 C0+reg)。
func EmitFaddDdDnDm(buf []byte, dd, dn, dm uint8) []byte {
	if dd > 31 {
		dd = 0
	}
	if dn > 31 {
		dn = 0
	}
	if dm > 31 {
		dm = 0
	}
	insn := uint32(0x1E602800) | (uint32(dm)&0x1F)<<16 | (uint32(dn)&0x1F)<<5 | uint32(dd)&0x1F
	return appendArm64Insn(buf, insn)
}

// EncodedFaddDdDnDmLen = 4.
const EncodedFaddDdDnDmLen = 4

// EmitFsubDdDnDm 发射 arm64「fsub Dd, Dn, Dm」(双精度减)。
// 编码:0x1E603800 base(同 FADD 但 op=0011)。对位 amd64 SUBSD。
func EmitFsubDdDnDm(buf []byte, dd, dn, dm uint8) []byte {
	if dd > 31 {
		dd = 0
	}
	if dn > 31 {
		dn = 0
	}
	if dm > 31 {
		dm = 0
	}
	insn := uint32(0x1E603800) | (uint32(dm)&0x1F)<<16 | (uint32(dn)&0x1F)<<5 | uint32(dd)&0x1F
	return appendArm64Insn(buf, insn)
}

// EncodedFsubDdDnDmLen = 4.
const EncodedFsubDdDnDmLen = 4

// EmitFmulDdDnDm 发射 arm64「fmul Dd, Dn, Dm」(双精度乘)。
// 编码:0x1E600800 base(op=0000)。对位 amd64 MULSD。
func EmitFmulDdDnDm(buf []byte, dd, dn, dm uint8) []byte {
	if dd > 31 {
		dd = 0
	}
	if dn > 31 {
		dn = 0
	}
	if dm > 31 {
		dm = 0
	}
	insn := uint32(0x1E600800) | (uint32(dm)&0x1F)<<16 | (uint32(dn)&0x1F)<<5 | uint32(dd)&0x1F
	return appendArm64Insn(buf, insn)
}

// EncodedFmulDdDnDmLen = 4.
const EncodedFmulDdDnDmLen = 4

// EmitFdivDdDnDm 发射 arm64「fdiv Dd, Dn, Dm」(双精度除)。
// 编码:0x1E601800 base(op=0001)。对位 amd64 DIVSD。
func EmitFdivDdDnDm(buf []byte, dd, dn, dm uint8) []byte {
	if dd > 31 {
		dd = 0
	}
	if dn > 31 {
		dn = 0
	}
	if dm > 31 {
		dm = 0
	}
	insn := uint32(0x1E601800) | (uint32(dm)&0x1F)<<16 | (uint32(dn)&0x1F)<<5 | uint32(dd)&0x1F
	return appendArm64Insn(buf, insn)
}

// EncodedFdivDdDnDmLen = 4.
const EncodedFdivDdDnDmLen = 4

// EmitFcmpeDnDm 发射 arm64「fcmpe Dn, Dm」(双精度比较 + signaling NaN,
// 设 NZCV flag 给下一条 B.cond 用)。
//
// 编码:0001_1110_011_mmmmm_001000_nnnnn_10000 = 0x1E602010 + Rm<<16 + Rn<<5
//   - Rd 字段 = 10000(opc2=10000 signaling 模式)
//   - opcode2 = 10 (signaling QNaN 抛 exception,语义比 fcmp 严格,精确等价
//     amd64 ucomisd-style ordered compare)
//
// 用例:PJ3 FORLOOP arm64 端 limit 比较(对位 amd64 ucomisd xmm0, xmm1
// 4 字节)。后续 EmitBCond(CondLE/GT/...) 利用 fcmpe 设的 NZCV 跳转。
func EmitFcmpeDnDm(buf []byte, dn, dm uint8) []byte {
	if dn > 31 {
		dn = 0
	}
	if dm > 31 {
		dm = 0
	}
	insn := uint32(0x1E602010) | (uint32(dm)&0x1F)<<16 | (uint32(dn)&0x1F)<<5
	return appendArm64Insn(buf, insn)
}

// EncodedFcmpeDnDmLen = 4.
const EncodedFcmpeDnDmLen = 4

// =============================================================================
// arm64 PJ4 IC 模板基础原语(承 06-backends.md §4.2 + ARMv8 ARM C5/C6 整数族)
// =============================================================================

// EmitAddXdXnXm 发射 arm64「add Xd, Xn, Xm」(shifted register,shift=00
// LSL 0)。
//
// 编码:1000_1011_00_mmmmm_000000_nnnnn_ddddd = 0x8B000000 base
//   - sf=1, op=0(add), S=0, shift=00, imm6=000000(LSL 0)
//
// 用例:PJ4 IC 字节级 inline arm64 端——SIB 替代(`add x2, x14, x1`
// 把 arena base + GCRef offset 加到一起,然后 `ldr x0, [x2, #disp]`
// 替代 amd64 `mov rax, [r14+rcx+disp]` 单条 SIB 寻址)。
func EmitAddXdXnXm(buf []byte, rd, rn, rm uint8) []byte {
	if rd > 30 {
		rd = 0
	}
	if rn > 30 {
		rn = 0
	}
	if rm > 30 {
		rm = 0
	}
	insn := uint32(0x8B000000) | (uint32(rm)&0x1F)<<16 | (uint32(rn)&0x1F)<<5 | uint32(rd)&0x1F
	return appendArm64Insn(buf, insn)
}

// EncodedAddXdXnXmLen = 4.
const EncodedAddXdXnXmLen = 4

// EmitMulXdXnXm 发射 arm64「mul Xd, Xn, Xm」(实际是 MADD Xd, Xn, Xm, XZR
// 别名)。承 §9.20 Option B Spike 1 enterLuaFrame inline 算 depth * 40。
//
// 编码:MADD Xd, Xn, Xm, XZR:1001_1011_000_mmmmm_011111_nnnnn_ddddd
//   - 0x9B007C00 base(Xa=31=XZR,bit 14-10=11111)
//   - + (Xm<<16) + (Xn<<5) + Xd
//
// 用例:CI 段第 depth 帧地址算 `depth * ciSlotBytes(40)`,先 mov X18, #40
// 再 mul X17, X17, X18。
func EmitMulXdXnXm(buf []byte, rd, rn, rm uint8) []byte {
	if rd > 30 {
		rd = 0
	}
	if rn > 30 {
		rn = 0
	}
	if rm > 30 {
		rm = 0
	}
	insn := uint32(0x9B007C00) | (uint32(rm)&0x1F)<<16 | (uint32(rn)&0x1F)<<5 | uint32(rd)&0x1F
	return appendArm64Insn(buf, insn)
}

// EncodedMulXdXnXmLen = 4.
const EncodedMulXdXnXmLen = 4

// EmitAndXdXnXm 发射 arm64「and Xd, Xn, Xm」(shifted register,shift=00)。
//
// 编码:1000_1010_00_mmmmm_000000_nnnnn_ddddd = 0x8A000000 base
//
// 用例:PJ4 IC arm64 端——`and x0, x0, x1`(从 NaN-box 提取 GCRef
// payload,对位 amd64 `and rax, rcx` 3 字节)。
func EmitAndXdXnXm(buf []byte, rd, rn, rm uint8) []byte {
	if rd > 30 {
		rd = 0
	}
	if rn > 30 {
		rn = 0
	}
	if rm > 30 {
		rm = 0
	}
	insn := uint32(0x8A000000) | (uint32(rm)&0x1F)<<16 | (uint32(rn)&0x1F)<<5 | uint32(rd)&0x1F
	return appendArm64Insn(buf, insn)
}

// EncodedAndXdXnXmLen = 4.
const EncodedAndXdXnXmLen = 4

// EmitLsrXdImm6 发射 arm64「lsr Xd, Xn, #imm6」(逻辑右移,unsigned 6-bit
// 位数,等价 UBFM Xd, Xn, #imm6, #63)。
//
// 编码:1101_0011_01_immr_111111_nnnnn_ddddd = 0xD340FC00 base
//   - immr = imm6(右移位数 0-63)
//   - imms = 0x3F(=63,固定,标识 LSR variant)
//
// 用例:PJ4 IC arm64 端——
//   - `lsr x0, x0, #48`(严密 IsTable guard:shift 让 NaN-box tag 落到
//     低 16 位,对位 amd64 `shr rax, 48` 4 字节)
//   - `lsr x0, x0, #32`(table.word5 gen 在高 32 位,对位 amd64
//     `shr rax, 32` 4 字节)
func EmitLsrXdImm6(buf []byte, rd, rn uint8, imm6 uint8) []byte {
	if rd > 30 {
		rd = 0
	}
	if rn > 30 {
		rn = 0
	}
	if imm6 > 63 {
		imm6 = 0
	}
	insn := uint32(0xD340FC00) | (uint32(imm6)&0x3F)<<16 | (uint32(rn)&0x1F)<<5 | uint32(rd)&0x1F
	return appendArm64Insn(buf, insn)
}

// EncodedLsrXdImm6Len = 4.
const EncodedLsrXdImm6Len = 4

// EmitLdrbWtFromXnDisp 发射 arm64「ldrb Wt, [Xn, #pimm12]」(32-bit
// zero-extended byte load with unsigned 12-bit byte offset)。
//
// 编码:0011_1001_01_iiiiiiiiiiii_nnnnn_ttttt = 0x39400000 base
//   - size=00(byte),V=0,opc=01(LDRB unsigned offset)
//   - imm12 是**byte offset**(无 scale),范围 [0, 4095]
//
// 用例:PJ3 FORLOOP safepoint check arm64 端(对位 amd64
// `cmp byte [r15+pfOff], 0` 8 字节,arm64 端拆 ldrb + cbnz 8 字节同款长)。
func EmitLdrbWtFromXnDisp(buf []byte, rt, rn uint8, byteOff uint16) []byte {
	if rt > 30 {
		rt = 0
	}
	if rn > 30 {
		rn = 0
	}
	if byteOff > 4095 {
		byteOff = 0
	}
	insn := uint32(0x39400000) | (uint32(byteOff)&0xFFF)<<10 | (uint32(rn)&0x1F)<<5 | uint32(rt)&0x1F
	return appendArm64Insn(buf, insn)
}

// EncodedLdrbWtFromXnDispLen = 4.
const EncodedLdrbWtFromXnDispLen = 4

// EmitCbnzW 发射 arm64「cbnz Wt, label」(compare-and-branch-if-nonzero,
// 32-bit register)。imm19 是字偏移(目标地址 = PC + imm19 * 4),范围
// [-2^20, 2^20-1]。
//
// 编码:0011_0101_iiiiiiiiiiiiiiiiiiii_ttttt = 0x35000000 base
//   - sf=0(32-bit),op=0(CBNZ)
//   - imm19 sign-extended 字偏移
//
// 用例:PJ3 FORLOOP safepoint check arm64 端——
// 「ldrb W0, [x27+pfOff]; cbnz W0, after_loop」(对位 amd64
// `cmp byte [r15+pfOff], 0; jne after_loop` 14 字节,arm64 端 8 字节)。
func EmitCbnzW(buf []byte, rt uint8, imm19 int32) []byte {
	if rt > 30 {
		rt = 0
	}
	insn := uint32(0x35000000) | (uint32(imm19)&0x7FFFF)<<5 | uint32(rt)&0x1F
	return appendArm64Insn(buf, insn)
}

// EncodedCbnzWLen = 4.
const EncodedCbnzWLen = 4

// patchCbnzImm19 在 buf[off..off+4] 处的 CBNZ/CBZ 指令字内 patch imm19
// 字段(bit 5-23)。Rt 字段(bit 0-4)和 op/sf base(bit 24-31)保留,
// 只修改 imm19。
func patchCbnzImm19(buf []byte, off int, imm19 int32) {
	if off+4 > len(buf) {
		return
	}
	insn := uint32(buf[off]) | uint32(buf[off+1])<<8 |
		uint32(buf[off+2])<<16 | uint32(buf[off+3])<<24
	insn &= 0xFF00001F
	insn |= (uint32(imm19) & 0x7FFFF) << 5
	buf[off] = byte(insn)
	buf[off+1] = byte(insn >> 8)
	buf[off+2] = byte(insn >> 16)
	buf[off+3] = byte(insn >> 24)
}

// patchBCondImm19 在 buf[off..off+4] 处的 B.cond 指令字内 patch imm19
// 字段(bit 5-23)。原指令字 cond 字段(bit 0-3)和 0x54 base(bit 24-31)
// 保留,只修改 imm19 19 位。
//
// 用例:PJ3 FORLOOP / PJ4 表 IC 模板的 forward B.cond 占位 deopt 后回填
// (placeholder imm19=0 → 实际 (deoptStart - bCondOff) / 4)。
func patchBCondImm19(buf []byte, off int, imm19 int32) {
	if off+4 > len(buf) {
		return
	}
	// 读原指令字
	insn := uint32(buf[off]) | uint32(buf[off+1])<<8 |
		uint32(buf[off+2])<<16 | uint32(buf[off+3])<<24
	// 清掉 imm19 字段(bit 5-23,共 19 位 = 0x7FFFF<<5 = 0x00FFFFE0)
	insn &= 0xFF00001F
	// 写入新 imm19
	insn |= (uint32(imm19) & 0x7FFFF) << 5
	// 写回 buf(LE)
	buf[off] = byte(insn)
	buf[off+1] = byte(insn >> 8)
	buf[off+2] = byte(insn >> 16)
	buf[off+3] = byte(insn >> 24)
}

// EmitBlrXn 发射 arm64「blr Xn」(branch with link to register,间接 call,
// LR=X30 设为返回地址)。对位 amd64 `call regN` 2 字节(`FF D0+reg`)。
//
// 编码:1101_0110_0011_1111_0000_00nn_nnn0_0000 = 0xD63F0000 base
//   - opc=0001(BLR), Rn 占 bit[9:5]
//
// **预设条件**:Rn ∈ [0, 30](X31 是 XZR,BLR XZR 行为未定义,sentinel
// 兜底 Rn=0)。caller 通常先 EmitMovXdImm64 装目标地址进 Xn,再 BLR Xn。
//
// 用例:PJ5 helper call macro arm64 端(对位 amd64 `mov rax, imm64 + call
// rax` 12 字节,arm64 端为 `mov X16/17, imm64 + blr X16/17` 20 字节,
// X16/X17 是 ARMv8 IP scratch 寄存器 callee-saved 不需要保留)。
func EmitBlrXn(buf []byte, rn uint8) []byte {
	if rn > 30 {
		rn = 0
	}
	insn := uint32(0xD63F0000) | (uint32(rn)&0x1F)<<5
	return appendArm64Insn(buf, insn)
}

// EncodedBlrXnLen = 4(arm64 一条指令)。
const EncodedBlrXnLen = 4

// EmitHelperCallArm64 发射 arm64 helper call 通用宏:
//
//	mov X16, helperAddr imm64    ; 16 字节(movz+movk×3)
//	blr X16                      ;  4 字节
//	——— 总长 20 字节 ———
//
// 对位 amd64 `mov rax, imm64 + call rax` 12 字节(arm64 多 8 字节因
// MOV imm64 序列 16 vs amd64 mov rax imm64 10 + BLR vs CALL reg)。
//
// 用 X16(IP0)作 trampoline scratch 寄存器:ARMv8 ABI 约定 X16/X17 是
// intra-procedure-call scratch(过程内调用临时寄存器,承 AAPCS),不需要 callee 保留;callee 可被任意改写。
// 调用后 X16 不需复原,LR=X30 也由 BLR 自动设。
//
// 用例:PJ5 CALL/TAILCALL 真接入 arm64 端(对位 amd64 EmitHelperCall),
// 调用 host helper(host.DoCall / host.GetTable / host.Arith 等),helperAddr
// 是 helper function 物理地址(经 jit Compile 时编译期求出)。
func EmitHelperCallArm64(buf []byte, helperAddr uint64) []byte {
	buf = EmitMovXdImm64(buf, 16, helperAddr) // mov x16, helperAddr
	buf = EmitBlrXn(buf, 16)                  // blr x16
	return buf
}

// EncodedHelperCallArm64Len = 20(MOV imm64 16 + BLR 4)。
const EncodedHelperCallArm64Len = EncodedMovXdImm64Len + EncodedBlrXnLen
