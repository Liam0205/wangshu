// Package bytecode defines Wangshu's register-based instruction set, encoding,
// Proto layout, and IC slot. See docs/design/p1-interpreter/02-bytecode-isa.md.
//
// Fixed 32-bit instruction:
//
//	format ABC :  | B:9 | C:9 | A:8 | OP:6 |  (31..23 = B, 22..14 = C, 13..6 = A, 5..0 = OP)
//	format ABx :  |     Bx:18      | A:8 | OP:6 |
//	format AsBx:  |     sBx:18     | A:8 | OP:6 |  (sBx = Bx - 131071)
//
// RK encoding: B/C high bit set (operand ≥ 256) = constant K[operand-256]; otherwise
// = register R. See 02 §2 / §4 for details.
package bytecode

// Instruction is a 32-bit encoded VM instruction.
type Instruction uint32

// Field bit widths and limits (02 §2).
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

	// MaxA / MaxBC / MaxBx / MaxStack (02 §2 / §9 invariants).
	MaxA     = int(AMask)         // 255
	MaxBC    = int(BCMask)        // 511
	MaxK     = 256                // RK slots 0..255 = registers, 256..511 = constants
	MaxBx    = int(BxMask)        // 262143
	SBxBias  = (int(BxMask) >> 1) // 131071
	MaxStack = 250                // 02 §2: reserve fixstack headroom, consistent with 5.1

	// Compile-time limits (09 / 04 §9 error catalog).
	MaxLocVars     = 200 // LUAI_MAXVARS
	MaxUpvalues    = 60  // LUAI_MAXUPVALUES
	FieldsPerFlush = 50  // 02 §4 SETLIST's LFIELDS_PER_FLUSH
)

// IsK reports whether a 9-bit RK operand points to a constant.
func IsK(rk int) bool { return rk >= MaxK }

// KIdx returns the constant index (rk - MaxK, requires IsK first).
func KIdx(rk int) int { return rk - MaxK }

// Encode/decode helpers (02 §2).

// Op returns the opcode.
func Op(i Instruction) OpCode { return OpCode(uint32(i) & OpMask) }

// A returns the A field (8-bit register).
func A(i Instruction) int { return int((uint32(i) >> AShift) & AMask) }

// B returns the B field (9-bit RK).
func B(i Instruction) int { return int((uint32(i) >> BShift) & BCMask) }

// C returns the C field (9-bit RK).
func C(i Instruction) int { return int((uint32(i) >> CShift) & BCMask) }

// Bx returns the 18-bit unsigned Bx.
func Bx(i Instruction) int { return int((uint32(i) >> BxShift) & BxMask) }

// SBx returns the 18-bit signed sBx.
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

// NoRegister marks "no destination register yet" in TESTSET A field (04 §5.6).
//
// Set to 0xFF (beyond MaxStack=250), guaranteeing it never collides with any valid
// register number; a later patchTestReg/exp2reg backfills the concrete reg or
// degrades to TEST (no A).
const NoRegister = int(AMask) // 255

// SetA returns ins with the A field rewritten to a, keeping the OP/B/C/Bx region intact.
//
// Used by codegen's "placeholder instruction backfill" path (GETGLOBAL/GETTABLE,
// arithmetic ABC, LOADK's ABx, etc. don't know A at emit time and backfill it later
// during exp2reg -- preserving the 18-bit high segment shared by B/C/Bx).
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
