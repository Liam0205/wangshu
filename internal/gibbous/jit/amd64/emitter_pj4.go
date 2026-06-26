//go:build wangshu_p4 && amd64

// emitter_pj4.go —— P4 PJ4 表 IC stableShape/stableIndex 直达槽字节级
// emit 原语(承 docs/design/p4-method-jit/03-speculation-ic.md §6)。
//
// **PJ4 范围**:GETTABLE A B C 中 C 是常量索引 + IC slot kind=ArrayHit +
// 命中 stableShape/stableIndex 时,字节级 inline 直达槽读跳过哈希。当前
// 工程基础阶段:加 SIB 寻址 / r14 base 装载 / GCRef tag check / table
// header word 读取等原语。
//
// 表布局(承 internal/object/table.go):
//   - 表头 6 words(48 字节)
//   - word0: GCHeader (otype=TABLE)
//   - word1: asize | hmask
//   - word2: arrayRef → Value[asize] 段
//   - word3: nodeRef → Node[hmask+1] 段
//   - word4: metaRef
//   - word5: lastfree | gen(高 32 bit = gen,IC 代次)
//
// NaN-box table tag:0xFFFC_<48-bit GCRef bytes>。
//
// **PJ4 spike 工程基础**:本文件加 emit 原语,实际 IC inline 接入主路径
// 留下一步。承设计 §6 直达槽形态。

package amd64

// EmitMovqR14FromR15Disp 发射「mov r14, [r15+disp32]」(REX.W+B+R+0x4C
// + 0x8B + ModRM),用于段内从 jitContext 装载 arena base 到 r14。
//
// 编码:4D 8B B7 disp32
//   - 4D = REX(W=1 R=1 X=0 B=1)→ 让 reg 字段 = 6(R14=r6+REX.R)
//     ModRM rm 字段 = 7(R15+REX.B)
//   - 8B = MOV r64, r/m64
//   - B7 = ModRM:mod=10(disp32) reg=110(R14 w/ REX.R) rm=111(R15 w/ REX.B)
//
// 共 7 字节。
func EmitMovqR14FromR15Disp(buf []byte, disp32 int32) []byte {
	buf = append(buf, 0x4D, 0x8B, 0xB7)
	buf = append(buf,
		byte(uint32(disp32)),
		byte(uint32(disp32)>>8),
		byte(uint32(disp32)>>16),
		byte(uint32(disp32)>>24))
	return buf
}

// EncodedMovqR14FromR15DispLen 是「mov r14, [r15+disp32]」字节数(7)。
const EncodedMovqR14FromR15DispLen = 7

// EmitMovzxRaxFromMemR14PlusReg 发射「movzx rax, dword ptr [r14+rax+disp32]」
// (用 SIB 寻址,rax 作 index;但 rax 同时是 destination 会冲突)。
// **重新设计**:用 rdx 作 index,rax 作 destination:
//
//	mov rdx, [...原 rax 内容...]
//	mov rax, [r14 + rdx*1 + disp32]
//
// 为简化,提供一个通用「mov rax, [r14+reg64+disp32]」原语,reg64 是
// pre-computed offset(GCRef + word_offset)。
//
// 编码 mov rax, [r14 + rcx + disp32](GCRef in rcx):
//
//	4A 8B 84 0E disp32
//	- 4A = REX.W + REX.X(index 高位用)? 不需要,rcx<8 不需要 REX.X
//	  实际:48 8B 84 0E disp32 = REX.W mov reg, r/m with SIB
//	  但 base=r14 需 REX.B:49 8B 84 0E disp32(REX.W+B)
//	  ModRM = 0x84 = 10 000 100:mod=10 disp32 / reg=000 (rax) / rm=100 (SIB)
//	  SIB  = 0x0E = 00 001 110:scale=00(*1) index=001 (rcx) / base=110 (r14)
//	  但 base=110 + REX.B=0 实际是 r14(REX.B 选高 8 寄存器 r8-r15)
//
// 编码:49 8B 84 0E disp32(8 字节)
func EmitMovqRaxFromR14PlusRcxDisp(buf []byte, disp32 int32) []byte {
	buf = append(buf, 0x49, 0x8B, 0x84, 0x0E)
	buf = append(buf,
		byte(uint32(disp32)),
		byte(uint32(disp32)>>8),
		byte(uint32(disp32)>>16),
		byte(uint32(disp32)>>24))
	return buf
}

// EncodedMovqRaxFromR14PlusRcxDispLen 是 SIB mov 字节数(8)。
const EncodedMovqRaxFromR14PlusRcxDispLen = 8

// EmitMovqRcxFromRax 发射「mov rcx, rax」(REX.W + 89 modrm,3 字节)。
// 编码:48 89 C1(REX.W / opcode 89 / ModRM C1=mod11 reg=000(rax) rm=001(rcx))
func EmitMovqRcxFromRax(buf []byte) []byte {
	return append(buf, 0x48, 0x89, 0xC1)
}

// EncodedMovqRcxFromRaxLen 是「mov rcx, rax」字节数(3)。
const EncodedMovqRcxFromRaxLen = 3

// EmitAndRaxImm32 发射「and rax, imm32-sign-extended」(REX.W + 81 /4 + imm32,
// 7 字节)。用于从 NaN-box 提取 48-bit GCRef payload:
//
//	and rax, 0x0000_FFFF_FFFF_FFFF
//
// 编码:48 81 E0 imm32
//   - 48 = REX.W
//   - 81 = ALU r/m64 imm32
//   - E0 = ModRM:mod=11 reg=100(/4=AND) rm=000(rax)
//
// **注**:0x0000_FFFF_FFFF_FFFF 是 48-bit,imm32 sign-ext 表达不了。
// 真要用此 mask 需用 mov rcx, imm64 + and rax, rcx 路径(10+3 = 13 字节)。
// 本原语保留作 imm32 mask 通用接口。
func EmitAndRaxImm32(buf []byte, imm32 int32) []byte {
	buf = append(buf, 0x48, 0x81, 0xE0)
	buf = append(buf,
		byte(uint32(imm32)),
		byte(uint32(imm32)>>8),
		byte(uint32(imm32)>>16),
		byte(uint32(imm32)>>24))
	return buf
}

// EncodedAndRaxImm32Len 是「and rax, imm32」字节数(7)。
const EncodedAndRaxImm32Len = 7

// EmitShrRcxImm8 发射「shr rcx, imm8」(REX.W + C1 /5 + imm8,4 字节)。
// 用于从 table.word5 = lastfree|gen 提取 gen(shr 32 位)。
//
// 编码:48 C1 E9 imm8
//   - 48 = REX.W
//   - C1 = SHR r/m64, imm8
//   - E9 = ModRM:mod=11 reg=101(/5=SHR) rm=001(rcx)
func EmitShrRcxImm8(buf []byte, imm8 byte) []byte {
	return append(buf, 0x48, 0xC1, 0xE9, imm8)
}

// EncodedShrRcxImm8Len 是「shr rcx, imm8」字节数(4)。
const EncodedShrRcxImm8Len = 4

// EmitCmpEcxImm32 发射「cmp ecx, imm32」(操作 32-bit rcx,验 gen)。
// 编码:81 F9 imm32(6 字节)
func EmitCmpEcxImm32(buf []byte, imm32 int32) []byte {
	buf = append(buf, 0x81, 0xF9)
	buf = append(buf,
		byte(uint32(imm32)),
		byte(uint32(imm32)>>8),
		byte(uint32(imm32)>>16),
		byte(uint32(imm32)>>24))
	return buf
}

// EncodedCmpEcxImm32Len 是「cmp ecx, imm32」字节数(6)。
const EncodedCmpEcxImm32Len = 6

// EmitShrRaxImm8 发射「shr rax, imm8」(REX.W + C1 /5 + imm8,4 字节)。
// 用于从 NaN-box 提取高 16 位 tag——`shr rax, 48` 后 rax 高 48 位清零,
// 低 16 位是 tag value(0xFFFC = TagTable / 0xFFFB = TagString 等)。
//
// 编码:48 C1 E8 imm8
//   - 48 = REX.W
//   - C1 = SHR r/m64, imm8
//   - E8 = ModRM:mod=11 reg=101(/5=SHR) rm=000(rax)
//
// 用例:严密 IsTable guard 字节级——`shr rax, 48; cmp eax, 0xFFFC; jne deopt`
// 替换原简化版 `mov rcx, 0xFFFC<<48; cmp rax, rcx; jb deopt`(string/userdata
// 等高 tag 假阳),严密版精确验高 16 位 = TagTable,排除所有非 table tag。
func EmitShrRaxImm8(buf []byte, imm8 byte) []byte {
	return append(buf, 0x48, 0xC1, 0xE8, imm8)
}

// EncodedShrRaxImm8Len 是「shr rax, imm8」字节数(4)。
const EncodedShrRaxImm8Len = 4

// EmitCmpEaxImm32 发射「cmp eax, imm32」(操作 32-bit rax)。
// 编码:3D imm32(5 字节,无 ModRM——AL/AX/EAX/RAX 是 ALU 立即数指令的
// 隐式操作数,short form)。
//
// 用例:严密 IsTable guard 字节级——`shr rax, 48; cmp eax, 0xFFFC`
// (高 48 位已经 shr 清零,只比较低 16 位 = tag value)。
func EmitCmpEaxImm32(buf []byte, imm32 int32) []byte {
	buf = append(buf, 0x3D)
	buf = append(buf,
		byte(uint32(imm32)),
		byte(uint32(imm32)>>8),
		byte(uint32(imm32)>>16),
		byte(uint32(imm32)>>24))
	return buf
}

// EncodedCmpEaxImm32Len 是「cmp eax, imm32」字节数(5)。
const EncodedCmpEaxImm32Len = 5

// EmitMovRdxImm64 发射「mov rdx, imm64」(REX.W + B8+r + imm64,10 字节)。
// 用例:PJ4 NodeHit 模板烧入 stableKey(IC 命中验 NodeKey == stableKey 时)。
//
// 编码:48 BA imm64(REX.W=1 / B8+rd=BA where rd=010=rdx / imm64 LE 8 字节)。
func EmitMovRdxImm64(buf []byte, imm64 uint64) []byte {
	buf = append(buf, 0x48, 0xBA)
	for i := 0; i < 8; i++ {
		buf = append(buf, byte(imm64>>(i*8)))
	}
	return buf
}

// EncodedMovRdxImm64Len 是「mov rdx, imm64」字节数(10)。
const EncodedMovRdxImm64Len = 10

// EmitCmpRaxRdx 发射「cmp rax, rdx」(REX.W + 39 / ModRM C2,3 字节)。
// 用例:PJ4 NodeHit 模板验 NodeKey(rax 装 [r14+rcx+stableIndex*24])
// 与 stableKey(rdx 装 imm64)是否一致。
//
// 编码:48 39 D0(REX.W / opcode 39 = CMP r/m64, r64 / ModRM 0xD0:
// mod=11 reg=010(rdx) rm=000(rax))。
func EmitCmpRaxRdx(buf []byte) []byte {
	return append(buf, 0x48, 0x39, 0xD0)
}

// EncodedCmpRaxRdxLen 是「cmp rax, rdx」字节数(3)。
const EncodedCmpRaxRdxLen = 3
