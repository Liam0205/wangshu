//go:build wangshu_p4 && amd64

// emitter_pj4.go — P4 PJ4 table-IC stableShape/stableIndex direct-slot,
// byte-level emit primitives (per docs/design/p4-method-jit/03-speculation-ic.md §6).
//
// **PJ4 scope**: for GETTABLE A B C where C is a constant index + IC slot
// kind=ArrayHit + a stableShape/stableIndex hit, byte-level inline the
// direct-slot read to skip hashing. Current engineering-groundwork stage:
// add primitives for SIB addressing / r14 base loading / GCRef tag check /
// table header word reads, etc.
//
// Table layout (per internal/object/table.go):
//   - header is 6 words (48 bytes)
//   - word0: GCHeader (otype=TABLE)
//   - word1: asize | hmask
//   - word2: arrayRef → Value[asize] segment
//   - word3: nodeRef → Node[hmask+1] segment
//   - word4: metaRef
//   - word5: lastfree | gen (high 32 bits = gen, the IC generation)
//
// NaN-box table tag: 0xFFFC_<48-bit GCRef bytes>.
//
// **PJ4 spike engineering groundwork**: this file adds the emit primitives;
// wiring the actual IC inline into the main path is left to the next step.
// Follows the direct-slot form of design §6.

package amd64

// EmitMovqR14FromR15Disp emits "mov r14, [r15+disp32]" (REX.W+B+R+0x4C
// + 0x8B + ModRM), used inside a segment to load the arena base from
// jitContext into r14.
//
// Encoding: 4D 8B B7 disp32
//   - 4D = REX(W=1 R=1 X=0 B=1) → makes the reg field = 6 (R14=r6+REX.R)
//     and the ModRM rm field = 7 (R15+REX.B)
//   - 8B = MOV r64, r/m64
//   - B7 = ModRM: mod=10(disp32) reg=110(R14 w/ REX.R) rm=111(R15 w/ REX.B)
//
// 7 bytes total.
func EmitMovqR14FromR15Disp(buf []byte, disp32 int32) []byte {
	buf = append(buf, 0x4D, 0x8B, 0xB7)
	buf = append(buf,
		byte(uint32(disp32)),
		byte(uint32(disp32)>>8),
		byte(uint32(disp32)>>16),
		byte(uint32(disp32)>>24))
	return buf
}

// EncodedMovqR14FromR15DispLen is the byte length of "mov r14, [r15+disp32]" (7).
const EncodedMovqR14FromR15DispLen = 7

// EmitMovzxRaxFromMemR14PlusReg emits "movzx rax, dword ptr [r14+rax+disp32]"
// (SIB addressing, rax as index; but rax is also the destination, which
// conflicts). **Redesign**: use rdx as the index and rax as the destination:
//
//	mov rdx, [...original rax content...]
//	mov rax, [r14 + rdx*1 + disp32]
//
// To simplify, provide a generic "mov rax, [r14+reg64+disp32]" primitive where
// reg64 is a pre-computed offset (GCRef + word_offset).
//
// Encoding mov rax, [r14 + rcx + disp32] (GCRef in rcx):
//
//	4A 8B 84 0E disp32
//	- 4A = REX.W + REX.X (for the index high bit)? Not needed, rcx<8 needs no REX.X.
//	  Actual: 48 8B 84 0E disp32 = REX.W mov reg, r/m with SIB
//	  but base=r14 needs REX.B: 49 8B 84 0E disp32 (REX.W+B)
//	  ModRM = 0x84 = 10 000 100: mod=10 disp32 / reg=000 (rax) / rm=100 (SIB)
//	  SIB  = 0x0E = 00 001 110: scale=00(*1) index=001 (rcx) / base=110 (r14)
//	  but base=110 + REX.B=0 is actually r14 (REX.B selects the high 8 registers r8-r15)
//
// Encoding: 49 8B 84 0E disp32 (8 bytes)
func EmitMovqRaxFromR14PlusRcxDisp(buf []byte, disp32 int32) []byte {
	buf = append(buf, 0x49, 0x8B, 0x84, 0x0E)
	buf = append(buf,
		byte(uint32(disp32)),
		byte(uint32(disp32)>>8),
		byte(uint32(disp32)>>16),
		byte(uint32(disp32)>>24))
	return buf
}

// EncodedMovqRaxFromR14PlusRcxDispLen is the byte length of the SIB mov (8).
const EncodedMovqRaxFromR14PlusRcxDispLen = 8

// EmitMovqRcxFromRax emits "mov rcx, rax" (REX.W + 89 modrm, 3 bytes).
// Encoding: 48 89 C1 (REX.W / opcode 89 / ModRM C1=mod11 reg=000(rax) rm=001(rcx))
func EmitMovqRcxFromRax(buf []byte) []byte {
	return append(buf, 0x48, 0x89, 0xC1)
}

// EncodedMovqRcxFromRaxLen is the byte length of "mov rcx, rax" (3).
const EncodedMovqRcxFromRaxLen = 3

// EmitAndRaxImm32 emits "and rax, imm32-sign-extended" (REX.W + 81 /4 + imm32,
// 7 bytes). Used to extract the 48-bit GCRef payload from a NaN-box:
//
//	and rax, 0x0000_FFFF_FFFF_FFFF
//
// Encoding: 48 81 E0 imm32
//   - 48 = REX.W
//   - 81 = ALU r/m64 imm32
//   - E0 = ModRM: mod=11 reg=100(/4=AND) rm=000(rax)
//
// **Note**: 0x0000_FFFF_FFFF_FFFF is 48-bit and cannot be expressed as a
// sign-extended imm32. Actually using this mask requires the mov rcx, imm64 +
// and rax, rcx path (10+3 = 13 bytes). This primitive is kept as a generic
// imm32-mask interface.
func EmitAndRaxImm32(buf []byte, imm32 int32) []byte {
	buf = append(buf, 0x48, 0x81, 0xE0)
	buf = append(buf,
		byte(uint32(imm32)),
		byte(uint32(imm32)>>8),
		byte(uint32(imm32)>>16),
		byte(uint32(imm32)>>24))
	return buf
}

// EncodedAndRaxImm32Len is the byte length of "and rax, imm32" (7).
const EncodedAndRaxImm32Len = 7

// EmitShrRcxImm8 emits "shr rcx, imm8" (REX.W + C1 /5 + imm8, 4 bytes).
// Used to extract gen from table.word5 = lastfree|gen (shift right by 32 bits).
//
// Encoding: 48 C1 E9 imm8
//   - 48 = REX.W
//   - C1 = SHR r/m64, imm8
//   - E9 = ModRM: mod=11 reg=101(/5=SHR) rm=001(rcx)
func EmitShrRcxImm8(buf []byte, imm8 byte) []byte {
	return append(buf, 0x48, 0xC1, 0xE9, imm8)
}

// EncodedShrRcxImm8Len is the byte length of "shr rcx, imm8" (4).
const EncodedShrRcxImm8Len = 4

// EmitCmpEcxImm32 emits "cmp ecx, imm32" (operates on the 32-bit rcx, to
// verify gen). Encoding: 81 F9 imm32 (6 bytes)
func EmitCmpEcxImm32(buf []byte, imm32 int32) []byte {
	buf = append(buf, 0x81, 0xF9)
	buf = append(buf,
		byte(uint32(imm32)),
		byte(uint32(imm32)>>8),
		byte(uint32(imm32)>>16),
		byte(uint32(imm32)>>24))
	return buf
}

// EncodedCmpEcxImm32Len is the byte length of "cmp ecx, imm32" (6).
const EncodedCmpEcxImm32Len = 6

// EmitShrRaxImm8 emits "shr rax, imm8" (REX.W + C1 /5 + imm8, 4 bytes).
// Used to extract the high 16-bit tag from a NaN-box — after `shr rax, 48`
// the high 48 bits of rax are cleared and the low 16 bits are the tag value
// (0xFFFC = TagTable / 0xFFFB = TagString etc.).
//
// Encoding: 48 C1 E8 imm8
//   - 48 = REX.W
//   - C1 = SHR r/m64, imm8
//   - E8 = ModRM: mod=11 reg=101(/5=SHR) rm=000(rax)
//
// Use case: strict byte-level IsTable guard — `shr rax, 48; cmp eax, 0xFFFC;
// jne deopt` replaces the simplified `mov rcx, 0xFFFC<<48; cmp rax, rcx;
// jb deopt` (which false-positives on higher tags like string/userdata). The
// strict version precisely checks that the high 16 bits = TagTable, excluding
// all non-table tags.
func EmitShrRaxImm8(buf []byte, imm8 byte) []byte {
	return append(buf, 0x48, 0xC1, 0xE8, imm8)
}

// EncodedShrRaxImm8Len is the byte length of "shr rax, imm8" (4).
const EncodedShrRaxImm8Len = 4

// EmitCmpEaxImm32 emits "cmp eax, imm32" (operates on the 32-bit rax).
// Encoding: 3D imm32 (5 bytes, no ModRM — AL/AX/EAX/RAX is the implicit
// operand of the ALU-immediate instruction, short form).
//
// Use case: strict byte-level IsTable guard — `shr rax, 48; cmp eax, 0xFFFC`
// (the high 48 bits are already cleared by shr, so only the low 16 bits =
// tag value are compared).
func EmitCmpEaxImm32(buf []byte, imm32 int32) []byte {
	buf = append(buf, 0x3D)
	buf = append(buf,
		byte(uint32(imm32)),
		byte(uint32(imm32)>>8),
		byte(uint32(imm32)>>16),
		byte(uint32(imm32)>>24))
	return buf
}

// EncodedCmpEaxImm32Len is the byte length of "cmp eax, imm32" (5).
const EncodedCmpEaxImm32Len = 5

// EmitMovRdxImm64 emits "mov rdx, imm64" (REX.W + B8+r + imm64, 10 bytes).
// Use case: the PJ4 NodeHit template burns in stableKey (for verifying
// NodeKey == stableKey on an IC hit).
//
// Encoding: 48 BA imm64 (REX.W=1 / B8+rd=BA where rd=010=rdx / imm64 LE 8 bytes).
func EmitMovRdxImm64(buf []byte, imm64 uint64) []byte {
	buf = append(buf, 0x48, 0xBA)
	for i := 0; i < 8; i++ {
		buf = append(buf, byte(imm64>>(i*8)))
	}
	return buf
}

// EncodedMovRdxImm64Len is the byte length of "mov rdx, imm64" (10).
const EncodedMovRdxImm64Len = 10

// EmitCmpRaxRdx emits "cmp rax, rdx" (REX.W + 39 / ModRM C2, 3 bytes).
// Use case: the PJ4 NodeHit template verifies that NodeKey (rax loaded from
// [r14+rcx+stableIndex*24]) matches stableKey (rdx loaded from imm64).
//
// Encoding: 48 39 D0 (REX.W / opcode 39 = CMP r/m64, r64 / ModRM 0xD0:
// mod=11 reg=010(rdx) rm=000(rax)).
func EmitCmpRaxRdx(buf []byte) []byte {
	return append(buf, 0x48, 0x39, 0xD0)
}

// EncodedCmpRaxRdxLen is the byte length of "cmp rax, rdx" (3).
const EncodedCmpRaxRdxLen = 3

// EmitMovqMemR14PlusRcxFromRax emits the reverse store "mov [r14 + rcx + disp32], rax"
// (SIB addressing, base=r14 / index=rcx / scale=1 / disp32).
//
// Use case: PJ4 SETTABLE IC byte-level inline reverse-write to the NodeVal slot —
// rcx = nodeRef offset, disp32 = stableIndex*24+8 (NodeVal is at node[idx].word1).
//
// Encoding: 49 89 84 0E disp32 (8 bytes)
//   - 49 = REX.W + REX.B (base r14 high-bit selection)
//   - 89 = MOV r/m64, r64
//   - 84 = ModRM: mod=10(disp32) reg=000(rax) rm=100(SIB)
//   - 0E = SIB: scale=00(*1) index=001(rcx) base=110(r14 w/ REX.B)
//
// Note: the rax src ModRM reg field is fixed = 000(rax).
func EmitMovqMemR14PlusRcxFromRax(buf []byte, disp32 int32) []byte {
	buf = append(buf, 0x49, 0x89, 0x84, 0x0E)
	buf = append(buf,
		byte(uint32(disp32)),
		byte(uint32(disp32)>>8),
		byte(uint32(disp32)>>16),
		byte(uint32(disp32)>>24))
	return buf
}

// EncodedMovqMemR14PlusRcxFromRaxLen is the byte length of the reverse SIB store (8).
const EncodedMovqMemR14PlusRcxFromRaxLen = 8

// EmitMovqMemR14PlusRcxFromRdx emits the reverse store "mov [r14 + rcx + disp32], rdx"
// (from rdx), also used by ArrayHit SETTABLE — rdx holds the value (R(C) loaded into rdx).
//
// Encoding: 49 89 94 0E disp32 (8 bytes)
//   - 49 = REX.W + REX.B
//   - 89 = MOV r/m64, r64
//   - 94 = ModRM: mod=10(disp32) reg=010(rdx) rm=100(SIB)
//   - 0E = SIB (same as EmitMovqMemR14PlusRcxFromRax)
func EmitMovqMemR14PlusRcxFromRdx(buf []byte, disp32 int32) []byte {
	buf = append(buf, 0x49, 0x89, 0x94, 0x0E)
	buf = append(buf,
		byte(uint32(disp32)),
		byte(uint32(disp32)>>8),
		byte(uint32(disp32)>>16),
		byte(uint32(disp32)>>24))
	return buf
}

// EncodedMovqMemR14PlusRcxFromRdxLen is the byte length of the reverse SIB store (from rdx) (8).
const EncodedMovqMemR14PlusRcxFromRdxLen = 8

// EmitMovqRdxFromMemReg emits "mov rdx, [reg+disp32]", loading from the given
// base register (base=reg, disp32) into rdx. Use case: PJ4 SETTABLE loads the
// R(C) value (from rbx + C*8) into rdx for the reverse store.
//
// When reg=rbx (3): encoding 48 8B 93 disp32 (7 bytes)
//   - 48 = REX.W
//   - 8B = MOV r64, r/m64
//   - 93 = ModRM: mod=10 (disp32) reg=010 (rdx) rm=011 (rbx)
//
// Note: this primitive supports only reg=rbx (3); it mirrors EmitMovqRaxFromMemReg
// but with a single target.
func EmitMovqRdxFromMemRbx(buf []byte, disp32 int32) []byte {
	buf = append(buf, 0x48, 0x8B, 0x93)
	buf = append(buf,
		byte(uint32(disp32)),
		byte(uint32(disp32)>>8),
		byte(uint32(disp32)>>16),
		byte(uint32(disp32)>>24))
	return buf
}

// EncodedMovqRdxFromMemRbxLen is the byte length of "mov rdx, [rbx+disp32]" (7).
const EncodedMovqRdxFromMemRbxLen = 7

// EmitMovqRcxFromMemRbx emits "mov rcx, [rbx+disp32]", loading from rbx + disp32
// into rcx (mirrors EmitMovqRdxFromMemRbx, rdx -> rcx).
//
// Encoding: 48 8B 8B disp32 (REX.W=1 / opcode 0x8B / ModRM=10_001_011 i.e. 0x8B,
// disp32 mode + reg=001 rcx + rm=011 rbx).
//
// Use case: PJ5 Option B Spike 1 — unpack R(callA) NaN-box, take the closure
// value into rcx, then mask to obtain the GCRef.
func EmitMovqRcxFromMemRbx(buf []byte, disp32 int32) []byte {
	buf = append(buf, 0x48, 0x8B, 0x8B)
	buf = append(buf,
		byte(uint32(disp32)),
		byte(uint32(disp32)>>8),
		byte(uint32(disp32)>>16),
		byte(uint32(disp32)>>24))
	return buf
}

// EncodedMovqRcxFromMemRbxLen = 7.
const EncodedMovqRcxFromMemRbxLen = 7

// EmitAndRcxRdx emits "and rcx, rdx" (REX.W 21 /r modrm, 3 bytes).
//
// Encoding: 48 21 D1 (REX.W=1 / opcode 0x21 AND r/m64 r64 / ModRM=11_010_001
// i.e. 0xD1, mod=11 reg-direct + reg=010 rdx + rm=001 rcx).
//
// Use case: Spike 1 — apply the NaN-box payload mask (rcx = rcx & 0x0000FFFFFFFFFFFF
// in rdx, yielding the 48-bit GCRef).
func EmitAndRcxRdx(buf []byte) []byte {
	return append(buf, 0x48, 0x21, 0xD1)
}

// EncodedAndRcxRdxLen = 3.
const EncodedAndRcxRdxLen = 3
