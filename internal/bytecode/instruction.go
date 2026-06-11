// Package bytecode defines Wangshu's register-based instruction set, encoding,
// Proto layout, and IC slot. See docs/design/p1-interpreter/02-bytecode-isa.md.
//
// 指令固定 32 bit:
//
//	格式 ABC :  | B:9 | C:9 | A:8 | OP:6 |  (31..23 = B, 22..14 = C, 13..6 = A, 5..0 = OP)
//	格式 ABx :  |     Bx:18      | A:8 | OP:6 |
//	格式 AsBx:  |     sBx:18     | A:8 | OP:6 |  (sBx = Bx - 131071)
//
// RK 编码:B/C 高位置 1(操作数 ≥ 256)= 常量 K[operand-256];否则 = 寄存器 R。
// 具体见 02 §2 / §4。
package bytecode

// Instruction is a 32-bit encoded VM instruction.
type Instruction uint32

// 字段位宽与上限(02 §2)。
const (
	OpBits = 6
	ABits  = 8
	BCBits = 9
	BxBits = 18

	OpMask = (uint32(1) << OpBits) - 1
	AMask  = (uint32(1) << ABits) - 1
	BCMask = (uint32(1) << BCBits) - 1
	BxMask = (uint32(1) << BxBits) - 1

	OpShift = 0
	AShift  = OpBits          // 6
	CShift  = AShift + ABits  // 14
	BShift  = CShift + BCBits // 23
	BxShift = AShift + ABits  // 14

	// MaxA / MaxBC / MaxBx / MaxStack(02 §2 / §9 不变式)。
	MaxA     = int(AMask)         // 255
	MaxBC    = int(BCMask)        // 511
	MaxK     = 256                // RK 槽位 0..255 = 寄存器,256..511 = 常量
	MaxBx    = int(BxMask)        // 262143
	SBxBias  = (int(BxMask) >> 1) // 131071
	MaxStack = 250                // 02 §2:留 fixstack 余量,与 5.1 一致

	// 编译期上限(09 / 04 §9 错误目录)。
	MaxLocVars     = 200 // LUAI_MAXVARS
	MaxUpvalues    = 60  // LUAI_MAXUPVALUES
	FieldsPerFlush = 50  // 02 §4 SETLIST 的 LFIELDS_PER_FLUSH
)

// IsK 判定一个 9-bit RK 操作数是否指向常量。
func IsK(rk int) bool { return rk >= MaxK }

// KIdx 取常量索引(rk - MaxK,前置 IsK)。
func KIdx(rk int) int { return rk - MaxK }

// 编解码 helper(02 §2)。

// Op 取 opcode。
func Op(i Instruction) OpCode { return OpCode(uint32(i) & OpMask) }

// A 取 A 字段(8-bit 寄存器)。
func A(i Instruction) int { return int((uint32(i) >> AShift) & AMask) }

// B 取 B 字段(9-bit RK)。
func B(i Instruction) int { return int((uint32(i) >> BShift) & BCMask) }

// C 取 C 字段(9-bit RK)。
func C(i Instruction) int { return int((uint32(i) >> CShift) & BCMask) }

// Bx 取 18-bit 无符号 Bx。
func Bx(i Instruction) int { return int((uint32(i) >> BxShift) & BxMask) }

// SBx 取 18-bit 有符号 sBx。
func SBx(i Instruction) int { return Bx(i) - SBxBias }

// EncodeABC encodes an iABC instruction.
func EncodeABC(op OpCode, a, b, c int) Instruction {
	return Instruction(uint32(op)&OpMask |
		(uint32(a)&AMask)<<AShift |
		(uint32(c)&BCMask)<<CShift |
		(uint32(b)&BCMask)<<BShift)
}

// EncodeABx encodes an iABx instruction.
func EncodeABx(op OpCode, a, bx int) Instruction {
	return Instruction(uint32(op)&OpMask |
		(uint32(a)&AMask)<<AShift |
		(uint32(bx)&BxMask)<<BxShift)
}

// EncodeAsBx encodes an iAsBx instruction (sBx is biased by SBxBias before storage).
func EncodeAsBx(op OpCode, a, sbx int) Instruction {
	return EncodeABx(op, a, sbx+SBxBias)
}

// NoRegister marks "no destination register yet" in TESTSET A field (04 §5.6)。
//
// 取 0xFF(超过 MaxStack=250),保证不会与任何合法寄存器号撞车,后续
// patchTestReg/exp2reg 时回填具体 reg 或退化为 TEST(无 A)。
const NoRegister = int(AMask) // 255

// SetA returns ins with the A field rewritten to a, keeping the OP/B/C/Bx region intact.
//
// 用于 codegen 的"占位指令回填"路径(GETGLOBAL/GETTABLE/算术 ABC、LOADK 的 ABx 等
// 指令在发射时 A 不知,后续 exp2reg 时回填——保留 B/C/Bx 共占的 18-bit 高位段)。
func SetA(ins Instruction, a int) Instruction {
	return Instruction((uint32(ins) &^ (AMask << AShift)) | (uint32(a)&AMask)<<AShift)
}

// SetSBx returns ins with the sBx field rewritten (keeps OP/A intact).
func SetSBx(ins Instruction, sbx int) Instruction {
	return Instruction((uint32(ins) &^ (BxMask << BxShift)) |
		(uint32(sbx+SBxBias)&BxMask)<<BxShift)
}

// SetB returns ins with the B field rewritten (keeps OP/A/C intact).
func SetB(ins Instruction, b int) Instruction {
	return Instruction((uint32(ins) &^ (BCMask << BShift)) | (uint32(b)&BCMask)<<BShift)
}

// SetC returns ins with the C field rewritten (keeps OP/A/B intact).
func SetC(ins Instruction, c int) Instruction {
	return Instruction((uint32(ins) &^ (BCMask << CShift)) | (uint32(c)&BCMask)<<CShift)
}
