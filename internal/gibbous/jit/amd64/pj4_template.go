//go:build wangshu_p4 && amd64

// pj4_template.go —— PJ4 table IC ArrayHit byte-level inline direct-hit slot
// template (see docs/design/p4-method-jit/03-speculation-ic.md §6 stableShape/
// stableIndex direct-hit slot).
//
// **Shape**: `function(t) return t[K] end` (GETTABLE A B C with constant index).
// When the IC slot kind=ArrayHit + stableShape/stableIndex hits, byte-level
// inline reads the array segment directly, skipping the hash lookup.
//
// **Byte-level flow** (amd64, ~125 bytes, strict IsTable guard version):
//
//	1. load R(B) into rax (candidate table NaN-box)
//	2. **strict IsTable guard**: shr rax,48 + cmp eax,0xFFFC + jne deopt
//	   (4+5+6=15 bytes, precisely checks the top 16-bit tag = TagTable)
//	3. since shr has clobbered rax, re-load R(B) → rax, then GCRef extract
//	4. load arena base into r14 (from jitContext via EmitMovqR14FromR15Disp)
//	5. mov rcx, rax (rcx = GCRef byte offset)
//	6. load table.word5 = [r14+rcx+40] into rax → shr 32 → compare to stableShape
//	7. load table.arrayRef = [r14+rcx+16] into rax
//	8. load array[stableIndex] = [r14+rcx+stableIndex*8] into rax
//	9. nil check: cmp rax, qNanBoxNil → je deopt
//	10. store R(A) = rax
//	11. ret
//	12. [deopt:] mov rax, deoptCode; ret
//
// **Preconditions**:
//   - rbx = valueStackBase (set up by callJITSpec)
//   - r15 = jitContext (set up by callJITSpec)
//   - r14 = scratch (loads arena base at template entry)
//
// **deopt path**: when the Run side sees raxSpec==deoptCode it calls
// host.GetTable (byte-equal to the P1 interpreter, via IC + hash + __index
// metamethod chain).
//
// **strict IsTable guard implementation** (following the external-review
// increment-9/10 ICTable guard false-positive suggestion): uses
// `shr rax, 48 + cmp eax, 0xFFFC + jne deopt` to precisely check the top 16
// bits = TagTable (0xFFFC). string (0xFFFB) / function (0xFFFD) / userdata
// (0xFFFE) / thread (0xFFFF) — every non-table NaN-box triggers deopt
// immediately, instead of falling through to the gen check (the original
// simplified version used a one-sided `rax < 0xFFFC<<48` jb, which is a false
// positive for the high tags function/userdata/thread; the subsequent gen
// check would almost certainly trigger deopt anyway but **runs an extra stretch
// of mmap-segment instructions**; the strict version deopts right away on
// IsTable failure and saves instructions).

package amd64

// qNanBoxTableTagShifted is the table tag NaN-box high bits: 0xFFFC << 48 = 0xFFFC_0000_0000_0000
const qNanBoxTableTagShifted uint64 = 0xFFFC_0000_0000_0000

// qNanBoxNilImm is the NaN-box raw bits of Nil (value.Nil = 0xFFFE_0000_0000_0000),
// following internal/value/value.go::Nil.
const qNanBoxNilImm uint64 = 0xFFFE_0000_0000_0000

// qNanBoxTableTagHigh16 is the bare value of TagTable in the NaN-box top 16 bits
// (0xFFFC), the immediate for the strict IsTable guard byte-level `cmp eax, 0xFFFC`.
const qNanBoxTableTagHigh16 int32 = 0xFFFC

// EmitGetTableArrayHit assembles the byte-level sequence of the IC ArrayHit
// direct-hit slot template.
//
// Parameters:
//   - aReg: destination R(A) register number
//   - bReg: table R(B) register number
//   - stableShape: the table.gen snapshot frozen at compile time
//   - stableIndex: the array slot index frozen at compile time
//   - arenaBaseOff: offset of the jitContext.arenaBase field (int32 bytes)
//   - deoptCode: returned as deoptCode when the guard fails
//
// Returns the appended buf.
//
// **Byte layout** (strict IsTable guard version, ~125 bytes):
//
//	[ 0] mov rax, [rbx + bReg*8]                     ; 7 (load R(B))
//	[ 7] shr rax, 48                                 ; 4 (extract top 16-bit tag)
//	[11] cmp eax, 0xFFFC                             ; 5 (exact TagTable compare)
//	[16] jne deopt                                   ; 6 (non-table → deopt)
//	     ; shr has clobbered rax, must re-load R(B)
//	[22] mov rax, [rbx + bReg*8]                     ; 7 (re-load R(B))
//	[29] mov rcx, 0x0000_FFFF_FFFF_FFFF              ; 10 (GCRef payload mask)
//	[39] and rax, rcx                                ; 3 (extract GCRef)
//	[42] mov rcx, rax                                ; 3 (rcx = GCRef offset)
//	[45] mov r14, [r15+arenaBaseOff]                 ; 7 (load arena base)
//	[52] mov rax, [r14+rcx+40]                       ; 8 (table.word5 → rax)
//	[60] shr rax, 32                                 ; 4 (gen is in the top 32 bits)
//	[64] cmp eax, stableShape                        ; 5 (gen compare)
//	[69] jne deopt                                   ; 6
//	[75] mov rax, [r14+rcx+16]                       ; 8 (table.arrayRef → rax)
//	[83] mov rcx, rax                                ; 3 (rcx = arrayRef offset)
//	[86] mov rax, [r14+rcx+stableIndex*8]            ; 8 (array[stableIndex])
//	[94] mov rcx, qNanBoxNilImm                      ; 10 (Nil bits)
//	[104] cmp rax, rcx                               ; 3
//	[107] je deopt                                   ; 6
//	[113] mov [rbx + aReg*8], rax                    ; 7 (store R(A))
//	[120] ret                                        ; 1
//	[121] ; deopt block
//	[121] mov rax, deoptCode imm64                   ; 10
//	[131] ret                                        ; 1
//	——— total 132 bytes (original simplified version 129 bytes; strict version +3 bytes for the re-load of R(B)) ———
//
// **strict IsTable guard**: `shr rax,48 + cmp eax,0xFFFC + jne deopt`
// (15 bytes) precisely checks the top 16 bits = TagTable (0xFFFC). string
// (0xFFFB) / function (0xFFFD) / userdata (0xFFFE) / thread (0xFFFF) — every
// non-table NaN-box triggers deopt immediately, instead of falling through to
// the gen check like the simplified version (false positive, then deopt, but
// running an extra stretch of mmap-segment instructions).
func EmitGetTableArrayHit(buf []byte, aReg, bReg uint8, stableShape, stableIndex uint32,
	arenaBaseOff int32, deoptCode uint64) []byte {
	// 1. load R(B) → rax
	buf = EmitMovqRaxFromMemReg(buf, 3 /*rbx*/, int32(bReg)*8)

	// 2. **strict IsTable guard**: shr rax,48 + cmp eax,0xFFFC + jne deopt
	//    (15 bytes) precisely checks the top 16-bit tag = TagTable, excluding all non-table NaN-boxes.
	buf = EmitShrRaxImm8(buf, 48)
	buf = EmitCmpEaxImm32(buf, qNanBoxTableTagHigh16)
	buf = EmitJneRel32(buf, 0) // placeholder rel32 → patch to deopt
	jneTagOff := len(buf) - 4

	// 3. shr has clobbered rax, re-load R(B) → rax
	buf = EmitMovqRaxFromMemReg(buf, 3 /*rbx*/, int32(bReg)*8)

	// 4. GCRef extract: and rax, payload_mask (via rcx)
	const payloadMask uint64 = 0x0000_FFFF_FFFF_FFFF
	buf = EmitMovRcxImm64(buf, payloadMask)
	// and rax, rcx: 48 21 C8 (REX.W + 21 + ModRM C8 = mod11 reg=001(rcx) rm=000(rax))
	buf = append(buf, 0x48, 0x21, 0xC8)

	// 5. mov rcx, rax (rcx = GCRef byte offset)
	buf = EmitMovqRcxFromRax(buf)

	// 6. load arena base → r14
	buf = EmitMovqR14FromR15Disp(buf, arenaBaseOff)

	// 7. load table.word5 = [r14+rcx+40] → rax
	buf = EmitMovqRaxFromR14PlusRcxDisp(buf, 40)

	// 8. shr rax, 32 (gen is in the top 32 bits)
	//    shr rax, imm8 = 48 C1 E8 imm8 (/5 = SHR, rm=000=rax)
	buf = append(buf, 0x48, 0xC1, 0xE8, 32)

	// 9. cmp eax, stableShape (32-bit cmp)
	//    cmp eax, imm32 = 3D imm32 (no ModRM, 5 bytes, EAX implicit)
	buf = append(buf, 0x3D)
	buf = append(buf,
		byte(stableShape),
		byte(stableShape>>8),
		byte(stableShape>>16),
		byte(stableShape>>24))

	// 10. jne deopt
	buf = EmitJneRel32(buf, 0)
	jneShapeOff := len(buf) - 4

	// 11. load table.arrayRef = [r14+rcx+16] → rax
	buf = EmitMovqRaxFromR14PlusRcxDisp(buf, 16)

	// 12. mov rcx, rax (rcx = arrayRef offset)
	buf = EmitMovqRcxFromRax(buf)

	// 13. load array[stableIndex] = [r14+rcx+stableIndex*8] → rax
	buf = EmitMovqRaxFromR14PlusRcxDisp(buf, int32(stableIndex)*8)

	// 14. nil check: cmp rax, qNanBoxNil + je deopt
	buf = EmitMovRcxImm64(buf, qNanBoxNilImm)
	buf = EmitCmpRaxRcx(buf)
	buf = EmitJeRel32(buf, 0)
	jeNilOff := len(buf) - 4

	// 15. store R(A) = rax
	buf = EmitMovqMemRegFromRax(buf, 3 /*rbx*/, int32(aReg)*8)

	// 16. ret (normal exit)
	buf = EmitRet(buf)

	// 17. deopt block
	deoptStart := len(buf)
	buf = EmitMovRaxImm64(buf, deoptCode)
	buf = EmitRet(buf)

	// 18. patch all forward jcc to deopt start
	PatchRel32(buf, jneTagOff, int32(deoptStart)-int32(jneTagOff+4))
	PatchRel32(buf, jneShapeOff, int32(deoptStart)-int32(jneShapeOff+4))
	PatchRel32(buf, jeNilOff, int32(deoptStart)-int32(jeNilOff+4))

	return buf
}

// EmitGetTableNodeHit assembles the byte-level sequence of the IC NodeHit
// direct-hit slot template (see
// docs/design/p4-method-jit/03-speculation-ic.md §6 NodeHit).
//
// **Shape**: `function(t) return t[K] end` (GETTABLE A B C with constant index).
// When the IC slot kind=NodeHit + stableShape/stableIndex hits, byte-level
// inline reads the node segment directly, skipping the hash lookup. NodeHit
// has one more key comparison than ArrayHit (NodeKey vs stableKey).
//
// Parameters:
//   - aReg: destination R(A) register number
//   - bReg: table R(B) register number
//   - stableShape: the table.gen snapshot frozen at compile time
//   - stableIndex: the node slot index frozen at compile time
//   - stableKey: the key NaN-box frozen at compile time (string ref / number bits etc.)
//   - arenaBaseOff: offset of the jitContext.arenaBase field (int32 bytes)
//   - deoptCode: returned as deoptCode when the guard fails
//
// Returns the appended buf.
//
// **Byte layout** (strict IsTable guard + key comparison version, ~159 bytes):
//
//	[ 0-6 ] mov rax, [rbx + bReg*8]                ; 7 (load R(B))
//	[ 7-10] shr rax, 48                            ; 4
//	[11-15] cmp eax, 0xFFFC                        ; 5 (strict IsTable)
//	[16-21] jne deopt                              ; 6
//	[22-28] mov rax, [rbx + bReg*8]                ; 7 (re-load R(B))
//	[29-38] mov rcx, payloadMask                   ; 10
//	[39-41] and rax, rcx                           ; 3 (GCRef extract)
//	[42-44] mov rcx, rax                           ; 3 (rcx = GCRef offset)
//	[45-51] mov r14, [r15+arenaBaseOff]            ; 7
//	[52-59] mov rax, [r14+rcx+40]                  ; 8 (table.word5)
//	[60-63] shr rax, 32                            ; 4 (gen is in the top 32 bits)
//	[64-68] cmp eax, stableShape                   ; 5
//	[69-74] jne deopt                              ; 6
//	[75-82] mov rax, [r14+rcx+24]                  ; 8 (table.nodeRef → rax,
//	                                                    word3=24=3*8)
//	[83-85] mov rcx, rax                           ; 3 (rcx = nodeRef offset)
//	[86-93] mov rax, [r14+rcx+stableIndex*24]      ; 8 (NodeKey load)
//	[94-103] mov rdx, stableKey                    ; 10
//	[104-106] cmp rax, rdx                         ; 3
//	[107-112] jne deopt                            ; 6 (NodeKey != stableKey)
//	[113-120] mov rax, [r14+rcx+stableIndex*24+8]  ; 8 (NodeVal load)
//	[121-130] mov rcx, qNanBoxNilImm               ; 10
//	[131-133] cmp rax, rcx                         ; 3
//	[134-139] je deopt                             ; 6 (NodeVal == Nil)
//	[140-146] mov [rbx + aReg*8], rax              ; 7 (store R(A))
//	[147] ret                                      ; 1
//	[148-157] mov rax, deoptCode imm64             ; 10 (deopt block)
//	[158] ret                                      ; 1
//	——— total ~159 bytes (ArrayHit 132 bytes + key comparison 27 bytes) ———
//
// **NodeHit vs ArrayHit differences**:
//   - fetches word3=nodeRef (offset 24) instead of word2=arrayRef (offset 16)
//   - node[idx] stride is 24 bytes (nodeWords=3) instead of array[idx]'s 8 bytes
//   - one extra key comparison (NodeKey == stableKey, to guard against key
//     degradation / __index chain)
//   - template length +27 bytes (key load + mov rdx + cmp + jne, 27 bytes total)
//
// **stableKey compile-time freeze**:
//   - number key: value.NumberValue(K) raw bits (IEEE 754 NaN-box)
//   - string key: value.MakeGC(TagString, ref) NaN-box, where ref is already
//     interned at compile time and immutable
//   - comparing the whole NaN-box is equivalent to keyEqual (same source as
//     ic.go::keyEqual)
//
// **deopt path**: when the Run side sees raxSpec==deoptCode it calls
// host.GetTable (byte-equal to the P1 interpreter, via IC + hash + __index
// metamethod chain).
func EmitGetTableNodeHit(buf []byte, aReg, bReg uint8,
	stableShape, stableIndex uint32, stableKey uint64,
	arenaBaseOff int32, deoptCode uint64) []byte {
	// 1. load R(B) → rax
	buf = EmitMovqRaxFromMemReg(buf, 3 /*rbx*/, int32(bReg)*8)

	// 2. strict IsTable guard: shr rax,48 + cmp eax,0xFFFC + jne deopt
	buf = EmitShrRaxImm8(buf, 48)
	buf = EmitCmpEaxImm32(buf, qNanBoxTableTagHigh16)
	buf = EmitJneRel32(buf, 0)
	jneTagOff := len(buf) - 4

	// 3. shr has clobbered rax, re-load R(B) → rax
	buf = EmitMovqRaxFromMemReg(buf, 3 /*rbx*/, int32(bReg)*8)

	// 4. GCRef extract: and rax, payload_mask (via rcx)
	const payloadMask uint64 = 0x0000_FFFF_FFFF_FFFF
	buf = EmitMovRcxImm64(buf, payloadMask)
	buf = append(buf, 0x48, 0x21, 0xC8) // and rax, rcx

	// 5. mov rcx, rax (rcx = GCRef byte offset)
	buf = EmitMovqRcxFromRax(buf)

	// 6. load arena base → r14
	buf = EmitMovqR14FromR15Disp(buf, arenaBaseOff)

	// 7. load table.word5 = [r14+rcx+40] → rax
	buf = EmitMovqRaxFromR14PlusRcxDisp(buf, 40)

	// 8. shr rax, 32 (gen is in the top 32 bits)
	buf = append(buf, 0x48, 0xC1, 0xE8, 32)

	// 9. cmp eax, stableShape (32-bit cmp)
	buf = append(buf, 0x3D)
	buf = append(buf,
		byte(stableShape),
		byte(stableShape>>8),
		byte(stableShape>>16),
		byte(stableShape>>24))

	// 10. jne deopt
	buf = EmitJneRel32(buf, 0)
	jneShapeOff := len(buf) - 4

	// 11. **NodeHit branch**: load table.nodeRef = [r14+rcx+24] → rax
	//     (word3=tableNodeIdx=3, 3*8=24 bytes; ArrayHit uses word2=16, i.e. arrayRef)
	buf = EmitMovqRaxFromR14PlusRcxDisp(buf, 24)

	// 12. mov rcx, rax (rcx = nodeRef offset)
	buf = EmitMovqRcxFromRax(buf)

	// 13. load NodeKey = [r14+rcx+stableIndex*24] → rax
	//     (node[idx] stride is 24 bytes, nodeWords=3, word0=key/word1=val/word2=next)
	buf = EmitMovqRaxFromR14PlusRcxDisp(buf, int32(stableIndex)*24)

	// 14. mov rdx, stableKey (key NaN-box frozen at compile time)
	buf = EmitMovRdxImm64(buf, stableKey)

	// 15. cmp rax, rdx + jne deopt (NodeKey != stableKey → deopt)
	buf = EmitCmpRaxRdx(buf)
	buf = EmitJneRel32(buf, 0)
	jneKeyOff := len(buf) - 4

	// 16. load NodeVal = [r14+rcx+stableIndex*24+8] → rax
	buf = EmitMovqRaxFromR14PlusRcxDisp(buf, int32(stableIndex)*24+8)

	// 17. nil check: cmp rax, qNanBoxNil + je deopt
	buf = EmitMovRcxImm64(buf, qNanBoxNilImm)
	buf = EmitCmpRaxRcx(buf)
	buf = EmitJeRel32(buf, 0)
	jeNilOff := len(buf) - 4

	// 18. store R(A) = rax
	buf = EmitMovqMemRegFromRax(buf, 3 /*rbx*/, int32(aReg)*8)

	// 19. ret (normal exit)
	buf = EmitRet(buf)

	// 20. deopt block
	deoptStart := len(buf)
	buf = EmitMovRaxImm64(buf, deoptCode)
	buf = EmitRet(buf)

	// 21. patch all forward jcc to deopt start
	PatchRel32(buf, jneTagOff, int32(deoptStart)-int32(jneTagOff+4))
	PatchRel32(buf, jneShapeOff, int32(deoptStart)-int32(jneShapeOff+4))
	PatchRel32(buf, jneKeyOff, int32(deoptStart)-int32(jneKeyOff+4))
	PatchRel32(buf, jeNilOff, int32(deoptStart)-int32(jeNilOff+4))

	return buf
}

// EmitSetTableArrayHit assembles the byte-level PJ4 SETTABLE IC ArrayHit inline
// reverse-write template (see
// docs/design/p4-method-jit/03-speculation-ic.md §6 SETTABLE).
//
// **Shape**: in `function(t, v) t[K] = v end`, K is a numeric constant hitting
// the array segment (luac compiles SETTABLE A B C with A=R(t) / B=K idx >=256 /
// C=R(v)). When IC[0].Kind = ArrayHit + Shape/Index hits, byte-level inline
// reverse-writes array[stableIndex].
//
// Parameters:
//   - aReg: table register number R(A) (SETTABLE's A, the table NaN-box is in this register)
//   - cReg: value register number R(C) (value is a reg, C<256)
//   - stableShape: the table.gen snapshot frozen at compile time
//   - stableIndex: the array slot index frozen at compile time
//   - arenaBaseOff: offset of the jitContext.arenaBase field (int32 bytes)
//   - deoptCode: returned as deoptCode when the guard fails
//
// Returns the appended buf.
//
// **Byte layout** (113 bytes; getter ArrayHit is 132 bytes, but the setter
// drops the 19-byte nil mask/check segment, then adds load R(C) value + reverse
// store):
//
//	[0-21]   load R(A) → rax + strict IsTable guard (22 bytes, reused)
//	[22-28]  re-load R(A) → rax (7 bytes)
//	[29-44]  GCRef extract + rcx = offset (16 bytes)
//	[45-51]  load arena base → r14 (7 bytes)
//	[52-74]  gen check (load word5 + shr + cmp eax + jne, 23 bytes)
//	[75-82]  load table.arrayRef = [r14+rcx+16] → rax (8 bytes)
//	[83-85]  mov rcx, rax (rcx = arrayRef offset, 3 bytes)
//	[86-92]  load R(C) → rdx (7 bytes, EmitMovqRdxFromMemRbx)
//	[93-100] mov [r14+rcx+stableIndex*8], rdx (8 bytes, reverse store)
//	[101]    ret (1 byte)
//	[102-112] deopt block (mov rax deoptCode + ret, 11 bytes)
//	——— total 113 bytes ———
//
// **Design simplifications** (this batch's SETTABLE engineering boundary):
//   - **does not verify that the existing array[stableIndex] != nil** (new-key
//     path) — the P1 interpreter's IC-hit protocol itself requires that slot to
//     be non-nil; the IC slot validation (shape consistent + slot not
//     invalidated) already guarantees it; the new-key path makes the IC refill,
//     and if this frame speculates wrongly, the P1 interpreter bumps gen +
//     RequestRefresh in the key-degradation scenario
//   - **assumes no __newindex metatable** (meta freeze assumption) — metamethod
//     scenarios should trigger a gen change handled by the IC invalidation path
//
// The strict version (adding ~13 bytes to verify existing nil + 13 bytes to
// verify __newindex) is left for PJ4+.
//
// **deopt path**: when the Run side sees raxSpec==deoptCode it calls
// host.SetTable (byte-equal to the P1 interpreter, via IC + hash + __newindex
// metamethod chain). The setter shape returns RETURN A 1 (no return value), and
// the Run side with retB=1 does not read R(A).
func EmitSetTableArrayHit(buf []byte, aReg, cReg uint8,
	stableShape, stableIndex uint32,
	arenaBaseOff int32, deoptCode uint64) []byte {
	// 1. load R(A) → rax (SETTABLE A is the table reg)
	buf = EmitMovqRaxFromMemReg(buf, 3 /*rbx*/, int32(aReg)*8)

	// 2. strict IsTable guard
	buf = EmitShrRaxImm8(buf, 48)
	buf = EmitCmpEaxImm32(buf, qNanBoxTableTagHigh16)
	buf = EmitJneRel32(buf, 0)
	jneTagOff := len(buf) - 4

	// 3. shr has clobbered rax, re-load R(A)
	buf = EmitMovqRaxFromMemReg(buf, 3 /*rbx*/, int32(aReg)*8)

	// 4. GCRef extract
	const payloadMask uint64 = 0x0000_FFFF_FFFF_FFFF
	buf = EmitMovRcxImm64(buf, payloadMask)
	buf = append(buf, 0x48, 0x21, 0xC8) // and rax, rcx

	// 5. mov rcx, rax (rcx = GCRef byte offset)
	buf = EmitMovqRcxFromRax(buf)

	// 6. load arena base → r14
	buf = EmitMovqR14FromR15Disp(buf, arenaBaseOff)

	// 7. load table.word5 = [r14+rcx+40] → rax (gen check)
	buf = EmitMovqRaxFromR14PlusRcxDisp(buf, 40)

	// 8. shr rax, 32
	buf = append(buf, 0x48, 0xC1, 0xE8, 32)

	// 9. cmp eax, stableShape
	buf = append(buf, 0x3D)
	buf = append(buf,
		byte(stableShape),
		byte(stableShape>>8),
		byte(stableShape>>16),
		byte(stableShape>>24))

	// 10. jne deopt
	buf = EmitJneRel32(buf, 0)
	jneShapeOff := len(buf) - 4

	// 11. load table.arrayRef = [r14+rcx+16] → rax
	buf = EmitMovqRaxFromR14PlusRcxDisp(buf, 16)

	// 12. mov rcx, rax (rcx = arrayRef offset)
	buf = EmitMovqRcxFromRax(buf)

	// 13. load R(C) value → rdx (7 bytes)
	buf = EmitMovqRdxFromMemRbx(buf, int32(cReg)*8)

	// 14. mov [r14+rcx+stableIndex*8], rdx (reverse store)
	buf = EmitMovqMemR14PlusRcxFromRdx(buf, int32(stableIndex)*8)

	// 15. ret (normal exit; SETTABLE has no return value, host.DoReturn does not read R(A) when retB=1)
	buf = EmitRet(buf)

	// 16. deopt block
	deoptStart := len(buf)
	buf = EmitMovRaxImm64(buf, deoptCode)
	buf = EmitRet(buf)

	// 17. patch all forward jcc to deopt start
	PatchRel32(buf, jneTagOff, int32(deoptStart)-int32(jneTagOff+4))
	PatchRel32(buf, jneShapeOff, int32(deoptStart)-int32(jneShapeOff+4))

	return buf
}

// EmitSelfArrayHit assembles the byte-level PJ4 SELF IC ArrayHit inline
// template (see docs/design/p4-method-jit/03-speculation-ic.md §6 + the SELF
// opcode semantics).
//
// **SELF opcode semantics** (see bytecode/opcode.go::SELF):
//
//	R(A+1) := R(B)
//	R(A)   := R(B)[RK(C)]
//
// i.e. the `obj:method()` shape: first copy obj into R(A+1) (the self/this
// argument), then R(A) = R(B).method to fetch the method function. Followed by
// CALL R(A) R(A+1) ... to invoke.
//
// IC ArrayHit hit condition: the method key is a numeric constant + hits the
// array segment (rare but a valid shape); the more common case is NodeHit (a
// string-key method name) — this batch does ArrayHit first as the engineering
// base for SELF, leaving NodeHit SELF for the next commit.
//
// Parameters:
//   - aReg: R(A) (method result) register number; R(A+1)=R(B) is written by the template
//   - bReg: R(B) (obj) register number
//   - stableShape / stableIndex / arenaBaseOff / deoptCode: same as GETTABLE
//
// **Byte layout** (139 bytes, ArrayHit 132 bytes + R(A+1) copy segment 7 bytes):
//
//	[0-6]    load R(B) → rax (7 bytes, obj NaN-box)
//	[7-13]   **extra**: store R(A+1) = rax (mov [rbx+(A+1)*8], rax)
//	         (7 bytes, EmitMovqMemRegFromRax with reg=rbx)
//	[14-17]  shr rax, 48 (4 bytes)
//	[18-22]  cmp eax, 0xFFFC (5 bytes)
//	[23-28]  jne deopt (6 bytes)
//	         **note**: continues into the ArrayHit template, reusing the strict IsTable guard
//	... same as ArrayHit getter: GCRef extract / gen check / arrayRef /
//	    array[stableIndex] / nil check / write R(A) / ret / deopt
//
// Measured exactly 139 bytes (summing primitives one by one:
// 7+7+4+5+6+7+10+3+3+7+8+4+5+6+8+3+8+10+3+6+7+1+10+1=139). EmitMovqMemRegFromRax
// uses the generic disp32 encoding (7 bytes), not the disp8 short form (so the
// template length does not fluctuate with the register number).
//
// **deopt path**: when the Run side sees raxSpec==deoptCode it calls
// host.GetTable, byte-equal to P1 (R(A+1)=R(B) has already been stored and does
// not need rollback — the R(A+1) write is SELF's first step, and the deopt path
// through host.GetTable still needs R(A+1) already set; the P1 interpreter's
// SELF case is the same source).
// **note**: the SELF deopt path calls host.GetTable with R(A+1) already set,
// byte-equal to the P1 SELF path (P1 execute.go SELF case does the same steps:
// setReg(A+1, B) → icGetTable → setReg(A)).
func EmitSelfArrayHit(buf []byte, aReg, bReg uint8,
	stableShape, stableIndex uint32,
	arenaBaseOff int32, deoptCode uint64) []byte {
	// 1. load R(B) → rax (obj NaN-box)
	buf = EmitMovqRaxFromMemReg(buf, 3 /*rbx*/, int32(bReg)*8)

	// 2. **SELF extra step**: store R(A+1) = rax (self/this argument)
	//    R(A+1) slot offset = (aReg+1)*8
	buf = EmitMovqMemRegFromRax(buf, 3 /*rbx*/, int32(aReg+1)*8)

	// 3. strict IsTable guard
	buf = EmitShrRaxImm8(buf, 48)
	buf = EmitCmpEaxImm32(buf, qNanBoxTableTagHigh16)
	buf = EmitJneRel32(buf, 0)
	jneTagOff := len(buf) - 4

	// 4. shr has clobbered rax, re-load R(B)
	buf = EmitMovqRaxFromMemReg(buf, 3 /*rbx*/, int32(bReg)*8)

	// 5. GCRef extract
	const payloadMask uint64 = 0x0000_FFFF_FFFF_FFFF
	buf = EmitMovRcxImm64(buf, payloadMask)
	buf = append(buf, 0x48, 0x21, 0xC8) // and rax, rcx

	// 6. mov rcx, rax (GCRef offset)
	buf = EmitMovqRcxFromRax(buf)

	// 7. load arena base → r14
	buf = EmitMovqR14FromR15Disp(buf, arenaBaseOff)

	// 8. gen check: load word5 + shr + cmp eax + jne
	buf = EmitMovqRaxFromR14PlusRcxDisp(buf, 40)
	buf = append(buf, 0x48, 0xC1, 0xE8, 32) // shr rax, 32
	buf = append(buf, 0x3D)                 // cmp eax, imm32
	buf = append(buf,
		byte(stableShape),
		byte(stableShape>>8),
		byte(stableShape>>16),
		byte(stableShape>>24))
	buf = EmitJneRel32(buf, 0)
	jneShapeOff := len(buf) - 4

	// 9. load table.arrayRef → rax
	buf = EmitMovqRaxFromR14PlusRcxDisp(buf, 16)
	buf = EmitMovqRcxFromRax(buf) // rcx = arrayRef offset

	// 10. load array[stableIndex] → rax
	buf = EmitMovqRaxFromR14PlusRcxDisp(buf, int32(stableIndex)*8)

	// 11. nil check
	buf = EmitMovRcxImm64(buf, qNanBoxNilImm)
	buf = EmitCmpRaxRcx(buf)
	buf = EmitJeRel32(buf, 0)
	jeNilOff := len(buf) - 4

	// 12. store R(A) = rax (method function)
	buf = EmitMovqMemRegFromRax(buf, 3 /*rbx*/, int32(aReg)*8)

	// 13. ret (normal exit)
	buf = EmitRet(buf)

	// 14. deopt block
	deoptStart := len(buf)
	buf = EmitMovRaxImm64(buf, deoptCode)
	buf = EmitRet(buf)

	// 15. patch all forward jcc to deopt start
	PatchRel32(buf, jneTagOff, int32(deoptStart)-int32(jneTagOff+4))
	PatchRel32(buf, jneShapeOff, int32(deoptStart)-int32(jneShapeOff+4))
	PatchRel32(buf, jeNilOff, int32(deoptStart)-int32(jeNilOff+4))

	return buf
}

// EmitSetTableNodeHit assembles the byte-level PJ4 SETTABLE IC NodeHit inline
// reverse-write template (see 03 §6 + GetTable NodeHit + SetTable ArrayHit,
// combining the same structures).
//
// **Shape**: in `function(t, v) t[K] = v end`, K is a string / arbitrary key in
// the hash segment (luac compiles SETTABLE A B C with A=R(t) / B=K idx >=256 /
// C=R(v)). When IC[0].Kind=NodeHit + Shape/Index/Key hits, byte-level inline
// reverse-writes node[stableIndex].val.
//
// Parameters:
//   - aReg: table register number R(A) (SETTABLE's A)
//   - cReg: value register number R(C) (value is a reg, C<256)
//   - stableShape: the table.gen snapshot frozen at compile time
//   - stableIndex: the node slot index frozen at compile time
//   - stableKey: the key NaN-box frozen at compile time (from proto.Consts[KIdx])
//   - arenaBaseOff / deoptCode: same as GetTable NodeHit
//
// **Byte layout** (140 bytes; GetTable NodeHit 159 - NodeVal/nil/storeRA 34 + load R(C)/reverse store 15):
//
//	[0-6]    load R(A) → rax (7 bytes)
//	[7-21]   strict IsTable guard (15 bytes)
//	[22-44]  re-load + GCRef extract + rcx = offset (23 bytes)
//	[45-51]  load arena base → r14 (7 bytes)
//	[52-74]  gen check word5/shr/cmp eax/jne (23 bytes)
//	[75-82]  load table.nodeRef = [r14+rcx+24] → rax (8 bytes)
//	[83-85]  mov rcx, rax (rcx = nodeRef offset) (3 bytes)
//	[86-93]  load NodeKey = [r14+rcx+stableIndex*24] → rax (8 bytes)
//	[94-103] mov rdx, stableKey (10 bytes)
//	[104-106] cmp rax, rdx (3 bytes)
//	[107-112] jne deopt (6 bytes, NodeKey != stableKey → deopt)
//	[113-119] load R(C) → rdx (7 bytes, setter loads value)
//	[120-127] mov [r14+rcx+stableIndex*24+8], rdx (8 bytes, reverse store NodeVal)
//	[128]     ret (1 byte)
//	[129-139] deopt block (mov rax, deoptCode + ret, 11 bytes)
//	——— total 140 bytes ———
//
// **Composite difference vs SetTable ArrayHit + GetTable NodeHit**:
//   - vs GetTable NodeHit: drops the NodeVal load (used by getter) + nil check + write R(A)
//   - vs SetTable ArrayHit: fetches word3=nodeRef + 24-byte stride + one extra key comparison
//   - rdx reuse: rdx first holds stableKey (key comparison), then after use is overwritten by R(C) value
//
// **deopt path**: when the Run side sees raxSpec==deoptCode it calls
// host.SetTable byte-equal to P1 (via icSetTable + __newindex metamethod chain;
// P1 icSetTable is compatible with NodeHit). The setter shape has retB=1, and
// the Run side's DoReturn does not read R(A).
//
// **Design simplifications** (same boundary as SetTable ArrayHit):
//   - does not verify that the existing NodeVal != nil (new-key path)
//   - assumes no __newindex metatable
func EmitSetTableNodeHit(buf []byte, aReg, cReg uint8,
	stableShape, stableIndex uint32, stableKey uint64,
	arenaBaseOff int32, deoptCode uint64) []byte {
	// 1. load R(A) → rax
	buf = EmitMovqRaxFromMemReg(buf, 3 /*rbx*/, int32(aReg)*8)

	// 2. strict IsTable guard
	buf = EmitShrRaxImm8(buf, 48)
	buf = EmitCmpEaxImm32(buf, qNanBoxTableTagHigh16)
	buf = EmitJneRel32(buf, 0)
	jneTagOff := len(buf) - 4

	// 3. shr has clobbered rax, re-load R(A)
	buf = EmitMovqRaxFromMemReg(buf, 3 /*rbx*/, int32(aReg)*8)

	// 4. GCRef extract
	const payloadMask uint64 = 0x0000_FFFF_FFFF_FFFF
	buf = EmitMovRcxImm64(buf, payloadMask)
	buf = append(buf, 0x48, 0x21, 0xC8) // and rax, rcx

	// 5. mov rcx, rax (rcx = GCRef offset)
	buf = EmitMovqRcxFromRax(buf)

	// 6. load arena base → r14
	buf = EmitMovqR14FromR15Disp(buf, arenaBaseOff)

	// 7. gen check
	buf = EmitMovqRaxFromR14PlusRcxDisp(buf, 40)
	buf = append(buf, 0x48, 0xC1, 0xE8, 32) // shr rax, 32
	buf = append(buf, 0x3D)                 // cmp eax, imm32
	buf = append(buf,
		byte(stableShape),
		byte(stableShape>>8),
		byte(stableShape>>16),
		byte(stableShape>>24))
	buf = EmitJneRel32(buf, 0)
	jneShapeOff := len(buf) - 4

	// 8. **NodeHit branch**: load table.nodeRef = [r14+rcx+24] → rax
	buf = EmitMovqRaxFromR14PlusRcxDisp(buf, 24)

	// 9. mov rcx, rax (rcx = nodeRef offset)
	buf = EmitMovqRcxFromRax(buf)

	// 10. load NodeKey = [r14+rcx+stableIndex*24] → rax
	buf = EmitMovqRaxFromR14PlusRcxDisp(buf, int32(stableIndex)*24)

	// 11. mov rdx, stableKey
	buf = EmitMovRdxImm64(buf, stableKey)

	// 12. cmp rax, rdx + jne deopt
	buf = EmitCmpRaxRdx(buf)
	buf = EmitJneRel32(buf, 0)
	jneKeyOff := len(buf) - 4

	// 13. **setter branch**: load R(C) value → rdx (overwrites stableKey, rdx reused)
	buf = EmitMovqRdxFromMemRbx(buf, int32(cReg)*8)

	// 14. reverse store: mov [r14+rcx+stableIndex*24+8], rdx (write NodeVal)
	buf = EmitMovqMemR14PlusRcxFromRdx(buf, int32(stableIndex)*24+8)

	// 15. ret (setter has no R(A) write)
	buf = EmitRet(buf)

	// 16. deopt block
	deoptStart := len(buf)
	buf = EmitMovRaxImm64(buf, deoptCode)
	buf = EmitRet(buf)

	// 17. patch all forward jcc to deopt start
	PatchRel32(buf, jneTagOff, int32(deoptStart)-int32(jneTagOff+4))
	PatchRel32(buf, jneShapeOff, int32(deoptStart)-int32(jneShapeOff+4))
	PatchRel32(buf, jneKeyOff, int32(deoptStart)-int32(jneKeyOff+4))

	return buf
}

// EmitSelfNodeHit assembles the byte-level PJ4 SELF IC NodeHit inline template
// (see 03 §6 + SELF ArrayHit + GetTable NodeHit, combining the same structures).
//
// **Shape**: in `function(obj) obj:method(args) end`, method is a string
// identifier (luac compiles SELF A B C with A=R(m) / B=R(obj) /
// C=K(string method name)). This is the typical shape of a real-world
// `obj:method()` call (nearly all OOP-style Lua code goes through this path).
//
// When IC[0].Kind=NodeHit + Shape/Index/Key hits, byte-level inline:
//
//	R(A+1) := R(B)  ; self/this argument
//	R(A)   := R(B)[K_string]  ; method function (direct hit via hash-segment NodeHit)
//
// **Byte layout** (166 bytes, SELF ArrayHit 139 + key comparison 27):
//
//	[0-6]    load R(B) → rax (7 bytes)
//	[7-13]   store R(A+1) = rax (7 bytes, SELF's first step)
//	[14-28]  strict IsTable guard (15 bytes)
//	[29-35]  re-load R(B) → rax (7 bytes)
//	[36-51]  GCRef extract + rcx = offset (16 bytes)
//	[52-58]  load arena base → r14 (7 bytes)
//	[59-81]  gen check (23 bytes)
//	[82-89]  load nodeRef = [r14+rcx+24] → rax (8 bytes, word3)
//	[90-92]  mov rcx, rax (rcx = nodeRef offset) (3 bytes)
//	[93-100] load NodeKey = [r14+rcx+stableIndex*24] → rax (8 bytes)
//	[101-110] mov rdx, stableKey (10 bytes)
//	[111-113] cmp rax, rdx (3 bytes)
//	[114-119] jne deopt (6 bytes)
//	[120-127] load NodeVal = [r14+rcx+stableIndex*24+8] → rax (8 bytes)
//	[128-137] mov rcx, qNanBoxNil (10 bytes)
//	[138-140] cmp rax, rcx (3 bytes)
//	[141-146] je deopt (6 bytes, NodeVal == Nil)
//	[147-153] store R(A) = rax (7 bytes)
//	[154]    ret (1 byte)
//	[155-165] deopt block (11 bytes)
//	——— total 166 bytes ———
//
// Key differences vs SELF ArrayHit:
//   - fetches word3=nodeRef (offset 24) instead of word2=arrayRef (offset 16)
//   - node stride is 24 bytes
//   - one extra key comparison segment (mov rdx stableKey + cmp rax rdx + jne)
//
// **deopt path**: when the Run side sees raxSpec==deoptCode it calls
// host.GetTable byte-equal to P1 (R(A+1)=R(B) already stored, same steps as the
// P1 SELF case; P1 icGetTable is compatible with NodeHit).
// EmitSelfNodeHitNoRet is the same as EmitSelfNodeHit, but its success path
// **does not emit ret** — it falls through into the caller-emitted following
// segment (see §9.20.9 commit-5j fixing the useFrameInline path so it is
// reached at Run time).
//
// **Design difference**:
//   - EmitSelfNodeHit ends its success-path segment with ret (a standalone spec
//     template; the Run side sees RAX=0 marking a normal segment exit)
//   - this function: success path falls through (the useFrameInline shape is
//     followed by BuildVoid0Arg + ExitHelperRequest + PopVoid0Arg + ret; after
//     the SELF segment stores R(A)=method, BuildVoid0Arg's
//     LoadClosureGCRef(callA) automatically reads the method GCRef payload)
//   - the deopt path is the same as EmitSelfNodeHit (write RAX=deoptCode + ret)
func EmitSelfNodeHitNoRet(buf []byte, aReg, bReg uint8,
	stableShape, stableIndex uint32, stableKey uint64,
	arenaBaseOff int32, deoptCode uint64) []byte {
	// 1. load R(B) → rax (obj NaN-box)
	buf = EmitMovqRaxFromMemReg(buf, 3 /*rbx*/, int32(bReg)*8)
	// 2. store R(A+1) = rax (self/this argument)
	buf = EmitMovqMemRegFromRax(buf, 3 /*rbx*/, int32(aReg+1)*8)
	// 3. strict IsTable guard
	buf = EmitShrRaxImm8(buf, 48)
	buf = EmitCmpEaxImm32(buf, qNanBoxTableTagHigh16)
	buf = EmitJneRel32(buf, 0)
	jneTagOff := len(buf) - 4
	// 4. shr has clobbered rax, re-load R(B)
	buf = EmitMovqRaxFromMemReg(buf, 3 /*rbx*/, int32(bReg)*8)
	// 5. GCRef extract
	const payloadMask uint64 = 0x0000_FFFF_FFFF_FFFF
	buf = EmitMovRcxImm64(buf, payloadMask)
	buf = append(buf, 0x48, 0x21, 0xC8)
	// 6. mov rcx, rax
	buf = EmitMovqRcxFromRax(buf)
	// 7. load arena base → r14
	buf = EmitMovqR14FromR15Disp(buf, arenaBaseOff)
	// 8. gen check
	buf = EmitMovqRaxFromR14PlusRcxDisp(buf, 40)
	buf = append(buf, 0x48, 0xC1, 0xE8, 32)
	buf = append(buf, 0x3D)
	buf = append(buf,
		byte(stableShape),
		byte(stableShape>>8),
		byte(stableShape>>16),
		byte(stableShape>>24))
	buf = EmitJneRel32(buf, 0)
	jneShapeOff := len(buf) - 4
	// 9. NodeHit branch: load nodeRef = [r14+rcx+24]
	buf = EmitMovqRaxFromR14PlusRcxDisp(buf, 24)
	// 10. mov rcx, rax (rcx = nodeRef offset)
	buf = EmitMovqRcxFromRax(buf)
	// 11. load NodeKey = [r14+rcx+stableIndex*24] → rax
	buf = EmitMovqRaxFromR14PlusRcxDisp(buf, int32(stableIndex)*24)
	// 12. mov rdx, stableKey
	buf = EmitMovRdxImm64(buf, stableKey)
	// 13. cmp rax, rdx + jne deopt
	buf = EmitCmpRaxRdx(buf)
	buf = EmitJneRel32(buf, 0)
	jneKeyOff := len(buf) - 4
	// 14. load NodeVal = [r14+rcx+stableIndex*24+8] → rax
	buf = EmitMovqRaxFromR14PlusRcxDisp(buf, int32(stableIndex)*24+8)
	// 15. nil check
	buf = EmitMovRcxImm64(buf, qNanBoxNilImm)
	buf = EmitCmpRaxRcx(buf)
	buf = EmitJeRel32(buf, 0)
	jeNilOff := len(buf) - 4
	// 16. store R(A) = rax (method function)
	buf = EmitMovqMemRegFromRax(buf, 3 /*rbx*/, int32(aReg)*8)
	// 17. **NO RET** — fall through into the caller-emitted BuildVoid0Arg segment
	//     (see §9.20.9 commit-5j fixing the useFrameInline path so it is reached at Run time)
	jmpSuccessOff := len(buf)
	buf = EmitJmpRel32(buf, 0) // jump over the deopt block to the segment tail (following BuildVoid0Arg)
	// 18. deopt block
	deoptStart := len(buf)
	buf = EmitMovRaxImm64(buf, deoptCode)
	buf = EmitRet(buf)
	// 19. patch forward jcc to deopt start
	PatchRel32(buf, jneTagOff, int32(deoptStart)-int32(jneTagOff+4))
	PatchRel32(buf, jneShapeOff, int32(deoptStart)-int32(jneShapeOff+4))
	PatchRel32(buf, jneKeyOff, int32(deoptStart)-int32(jneKeyOff+4))
	PatchRel32(buf, jeNilOff, int32(deoptStart)-int32(jeNilOff+4))
	// 20. patch the success jmp to jump past the deopt block (start of the following BuildVoid0Arg)
	PatchRel32(buf, jmpSuccessOff+1, int32(len(buf))-int32(jmpSuccessOff+5))
	return buf
}

// EmitSelfNodeHit ... (original function, kept unchanged)
func EmitSelfNodeHit(buf []byte, aReg, bReg uint8,
	stableShape, stableIndex uint32, stableKey uint64,
	arenaBaseOff int32, deoptCode uint64) []byte {
	// 1. load R(B) → rax (obj NaN-box)
	buf = EmitMovqRaxFromMemReg(buf, 3 /*rbx*/, int32(bReg)*8)

	// 2. **SELF extra**: store R(A+1) = rax (self/this argument)
	buf = EmitMovqMemRegFromRax(buf, 3 /*rbx*/, int32(aReg+1)*8)

	// 3. strict IsTable guard
	buf = EmitShrRaxImm8(buf, 48)
	buf = EmitCmpEaxImm32(buf, qNanBoxTableTagHigh16)
	buf = EmitJneRel32(buf, 0)
	jneTagOff := len(buf) - 4

	// 4. shr has clobbered rax, re-load R(B)
	buf = EmitMovqRaxFromMemReg(buf, 3 /*rbx*/, int32(bReg)*8)

	// 5. GCRef extract
	const payloadMask uint64 = 0x0000_FFFF_FFFF_FFFF
	buf = EmitMovRcxImm64(buf, payloadMask)
	buf = append(buf, 0x48, 0x21, 0xC8)

	// 6. mov rcx, rax
	buf = EmitMovqRcxFromRax(buf)

	// 7. load arena base → r14
	buf = EmitMovqR14FromR15Disp(buf, arenaBaseOff)

	// 8. gen check
	buf = EmitMovqRaxFromR14PlusRcxDisp(buf, 40)
	buf = append(buf, 0x48, 0xC1, 0xE8, 32)
	buf = append(buf, 0x3D)
	buf = append(buf,
		byte(stableShape),
		byte(stableShape>>8),
		byte(stableShape>>16),
		byte(stableShape>>24))
	buf = EmitJneRel32(buf, 0)
	jneShapeOff := len(buf) - 4

	// 9. **NodeHit branch**: load nodeRef = [r14+rcx+24]
	buf = EmitMovqRaxFromR14PlusRcxDisp(buf, 24)

	// 10. mov rcx, rax (rcx = nodeRef offset)
	buf = EmitMovqRcxFromRax(buf)

	// 11. load NodeKey = [r14+rcx+stableIndex*24] → rax
	buf = EmitMovqRaxFromR14PlusRcxDisp(buf, int32(stableIndex)*24)

	// 12. mov rdx, stableKey
	buf = EmitMovRdxImm64(buf, stableKey)

	// 13. cmp rax, rdx + jne deopt
	buf = EmitCmpRaxRdx(buf)
	buf = EmitJneRel32(buf, 0)
	jneKeyOff := len(buf) - 4

	// 14. load NodeVal = [r14+rcx+stableIndex*24+8] → rax
	buf = EmitMovqRaxFromR14PlusRcxDisp(buf, int32(stableIndex)*24+8)

	// 15. nil check
	buf = EmitMovRcxImm64(buf, qNanBoxNilImm)
	buf = EmitCmpRaxRcx(buf)
	buf = EmitJeRel32(buf, 0)
	jeNilOff := len(buf) - 4

	// 16. store R(A) = rax (method function)
	buf = EmitMovqMemRegFromRax(buf, 3 /*rbx*/, int32(aReg)*8)

	// 17. ret
	buf = EmitRet(buf)

	// 18. deopt block
	deoptStart := len(buf)
	buf = EmitMovRaxImm64(buf, deoptCode)
	buf = EmitRet(buf)

	// 19. patch all forward jcc to deopt start
	PatchRel32(buf, jneTagOff, int32(deoptStart)-int32(jneTagOff+4))
	PatchRel32(buf, jneShapeOff, int32(deoptStart)-int32(jneShapeOff+4))
	PatchRel32(buf, jneKeyOff, int32(deoptStart)-int32(jneKeyOff+4))
	PatchRel32(buf, jeNilOff, int32(deoptStart)-int32(jeNilOff+4))

	return buf
}

// EmitSpecArgLoadK writes R(dstReg) = K (NaN-box u64) — used by the PJ5 SELF
// spec template for byte-level inline arg loading, replacing the
// host.SetReg(dstReg, K) round-trip.
//
// Byte sequence (10+7 = 17 bytes):
//
//	mov rax, K_imm64        ; 10 bytes
//	mov [rbx + dstReg*8], rax  ; 7 bytes (disp32 mode)
func EmitSpecArgLoadK(buf []byte, dstReg uint8, k uint64) []byte {
	buf = EmitMovRaxImm64(buf, k)
	buf = EmitMovqMemRegFromRax(buf, 3 /*rbx*/, int32(dstReg)*8)
	return buf
}

// EmitSpecArgLoadReg writes R(dstReg) = R(srcReg) — used by the PJ5 SELF spec
// template for byte-level inline arg loading, replacing the double round-trip
// host.SetReg(dstReg, host.GetReg(srcReg)).
//
// Byte sequence (7+7 = 14 bytes):
//
//	mov rax, [rbx + srcReg*8]
//	mov [rbx + dstReg*8], rax
func EmitSpecArgLoadReg(buf []byte, dstReg uint8, srcReg uint8) []byte {
	buf = EmitMovqRaxFromMemReg(buf, 3 /*rbx*/, int32(srcReg)*8)
	buf = EmitMovqMemRegFromRax(buf, 3 /*rbx*/, int32(dstReg)*8)
	return buf
}

// EmitFrameInlineCIDepthInc emits the byte-level ciDepth++ inline template (see
// `docs/design/p4-method-jit/implementation-progress.md` §9.20 Option B
// Spike 1 opening building block): from the mmap segment, via r15 →
// dereference jitContext.ciDepthAddr into rax, then byte-level
// inc qword ptr [rax], equivalent to the ciDepth-word mirror write of
// `th.setCIDepth(th.ciDepth+1)` in enterLuaFrame (reusing the P3 PW10 Stage 1a
// mirror word).
//
// Byte sequence (10 bytes):
//
//	mov rax, [r15 + ciDepthAddrOffset]  ; 7 bytes (see EmitMovqRaxFromR15Disp,
//	                                    ;        actual encoding 49 8B 87 disp32:
//	                                    ;        REX.W+B makes the rm field use r15)
//	inc qword ptr [rax]                  ; 3 bytes (see EmitIncQwordPtrAtRax:
//	                                    ;        48 FF 00)
//
// **Parameter ciDepthAddrOffset**: `JITContextCIDepthAddrOffset` (see the
// jitcontext.go const) — the caller must pass the jit.JITContextCIDepthAddrOffset
// compile-time constant; this function does not depend on the jit package
// directly (to avoid a circular dependency).
//
// **§9.20 Spike 1 gating**: this template is only emitted in the callee.NumParams=0
// + !IsVararg + !NeedsArg shape; the callee frame build tears down the other 4
// word writes + popCallInfo the same way (EmitFrameInlineCIDepthDec is coming).
//
// **arena grow risk**: ciDepthAddr is computed fresh and injected at each Run
// entry by jitContext.SetCIDepthAddr (wired in at code.go::Run lines 268-271);
// when arena grow triggers a segment relocation it is reloaded on the next Run,
// so this field does not cache a pointer into a host address.
func EmitFrameInlineCIDepthInc(buf []byte, ciDepthAddrOffset int32) []byte {
	buf = EmitMovqRaxFromR15Disp(buf, ciDepthAddrOffset)
	buf = EmitIncQwordPtrAtRax(buf)
	return buf
}

// EmitFrameInlineCIDepthDec emits the byte-level ciDepth-- inline template (see
// §9.20 Option B Spike 1 popCallInfo, the reverse).
//
// Byte sequence (10 bytes): same as Inc but the final inc becomes dec
// (equivalent to `th.setCIDepth(th.ciDepth-1)` in popCallInfo).
func EmitFrameInlineCIDepthDec(buf []byte, ciDepthAddrOffset int32) []byte {
	buf = EmitMovqRaxFromR15Disp(buf, ciDepthAddrOffset)
	buf = EmitDecQwordPtrAtRax(buf)
	return buf
}

// EncodedFrameInlineCIDepthIncDecLen is the byte count of the "ciDepth++/--"
// byte-level inline template (7+3=10). See §9.20 Spike 1 caller unit test +
// Compile-segment length budget.
const EncodedFrameInlineCIDepthIncDecLen = 10

// EmitFrameInlineLoadCISlotAddr emits the byte-level template that loads the
// byte address of the depth-th CI-segment frame start into rax (see §9.20
// Option B Spike 1 enterLuaFrame inline first segment).
//
// This template computes ciSegBaseAddr + ciDepth * 40 (each frame is
// ciWords=5 words = 40 bytes) to get the CallInfo[depth] frame address, ready
// for the subsequent writeCIWordN writes of each word.
//
// Byte sequence (7+3+7+3+5+3 = 28 bytes):
//
//	mov rcx, [r15 + ciDepthAddrOffset]    ; 7 bytes = mov rcx, [r15+disp32]
//	                                        ;     actually EmitMovqRcxFromR15Disp does not exist,
//	                                        ;     use EmitMovqRaxFromR15Disp + mov rcx, rax
//	                                        ;     OR use EmitMovqRaxFromR15Disp + EmitMovqRcxFromRax
//	mov rcx, [rcx]                          ; 3 bytes (48 8B 09 = mov rcx, [rcx])
//	mov rax, [r15 + ciSegBaseAddrOffset]    ; 7 bytes
//	mov rax, [rax]                          ; 3 bytes (48 8B 00 = mov rax, [rax])
//	imul rcx, rcx, 40                       ; 4 bytes (48 6B C9 28 = imul rcx, rcx, 40)
//	add rax, rcx                            ; 3 bytes (48 01 C8 = add rax, rcx)
//
// After the template, rax = CallInfo[depth] byte address. The subsequent
// writeCIWordN(rax, word_idx, val) writes each word via mov [rax + word_idx*8], rcx.
//
// **Total length 28 bytes** (amd64 enterLuaFrame inline first segment).
//
// **arena grow note**: ciDepthAddr / ciSegBaseAddr are computed fresh at each
// Run entry, not cached (see §9.20 + the arena base reload protocol).
func EmitFrameInlineLoadCISlotAddr(buf []byte, ciDepthAddrOffset, ciSegBaseAddrOffset int32) []byte {
	// 1. rcx = ciDepth (the depth value)
	buf = EmitMovqRaxFromR15Disp(buf, ciDepthAddrOffset)
	buf = EmitMovqRcxFromRax(buf)
	// rcx is now ciDepthAddr. Dereference once more: mov rcx, [rcx]
	buf = append(buf, 0x48, 0x8B, 0x09) // mov rcx, [rcx]
	// 2. rax = ciSegBase byte offset (ciBaseW*8 word offset into arena)
	buf = EmitMovqRaxFromR15Disp(buf, ciSegBaseAddrOffset)
	// rax is now ciSegBaseAddr (host byte address pointing at the mirror word). Dereference: mov rax, [rax]
	buf = append(buf, 0x48, 0x8B, 0x00) // mov rax, [rax]
	// 3. imul rcx, rcx, 40 — depth * ciSlotBytes
	buf = append(buf, 0x48, 0x6B, 0xC9, 40) // imul rcx, rcx, 40
	// 4. add rax, rcx — rax = ciBaseW*8 + depth*40 (byte offset into arena)
	buf = append(buf, 0x48, 0x01, 0xC8) // add rax, rcx
	return buf
}

// EncodedFrameInlineLoadCISlotAddrLen is the byte count of the "load the
// depth-th CI-segment frame address into rax" template (7+3+3+7+3+4+3 = 30 —
// actual; EmitMovqRaxFromR15Disp is 7, EmitMovqRcxFromRax is 3, the subsequent
// mov rcx [rcx] is 3, mov rax [r15+...] is 7, mov rax [rax] is 3, imul 4, add 3,
// totalling 30 bytes, not 28).
const EncodedFrameInlineLoadCISlotAddrLen = 30

// EmitFrameInlineLoadCISlotAddrAbsolute is the same as
// EmitFrameInlineLoadCISlotAddr but the result rax is an **arena absolute
// address** (see §9.20.9 commit-5l fixing the ciSegBase mirror-word semantics
// bug):
//
// **bug origin** (P3 PW10 Stage 2 ciSegBase mirror-word protocol): ciSegBaseRef
// stores `ciBaseW * 8` (a byte offset into arena), not an absolute address.
// The rax = ciBaseW*8 + depth*40 that LoadCISlotAddr computes is a byte offset
// and cannot be dereferenced directly (SIGSEGV).
//
// **this function**: after LoadCISlotAddr, appends `add rax, r14` (adds the
// arena base to rax) so rax becomes an absolute address. **Precondition**: the
// caller has already set up r14 = arena base (same r14 borrow as SELF NodeHit /
// BuildVoid0Arg::LoadClosureGCRef).
//
// **Byte sequence**: LoadCISlotAddr (30B) + `mov r14, [r15+arenaBaseOff]` (7B) +
// `add rax, r14` (3B) = 40B.
//
// **Usage site** (useFrameInline path):
//  1. call this function: rax = CI[depth] absolute address (40B)
//  2. subsequent WriteCIWord/CIDepthInc/LoadClosureGCRef + WriteCIWordFromRcx use the same way
func EmitFrameInlineLoadCISlotAddrAbsolute(buf []byte, ciDepthAddrOffset, ciSegBaseAddrOffset, arenaBaseOffset int32) []byte {
	buf = EmitFrameInlineLoadCISlotAddr(buf, ciDepthAddrOffset, ciSegBaseAddrOffset)
	// load r14 = arena base
	buf = EmitMovqR14FromR15Disp(buf, arenaBaseOffset)
	// add rax, r14 (0x4C 0x01 0xF0 = REX.WR + 0x01 + ModRM 11_110_000 = add rax, r14)
	buf = append(buf, 0x4C, 0x01, 0xF0)
	return buf
}

// EncodedFrameInlineLoadCISlotAddrAbsoluteLen = LoadCISlotAddr 30 + load r14 7 + add 3 = 40.
const EncodedFrameInlineLoadCISlotAddrAbsoluteLen = EncodedFrameInlineLoadCISlotAddrLen + 7 + 3

// EmitFrameInlineWriteCIWord emits the byte-level template that writes imm64
// into CI frame word_idx (see §9.20 Option B Spike 1 enterLuaFrame inline
// second segment).
//
// Call contract: rax must already hold the CallInfo[depth] frame start byte
// address (already set up by EmitFrameInlineLoadCISlotAddr); word_idx range is
// [0,4] (ciWords=5).
//
// Byte sequence (10+4 = 14 bytes):
//
//	mov rcx, imm64                      ; 10 bytes (EmitMovRcxImm64)
//	mov [rax + word_idx*8], rcx         ; 4 bytes (48 89 48 disp8)
//
// **word layout** (see state.go::writeCISeg / packCIWord2):
//   - word0 = uint32(base) | uint32(funcIdx) << 32
//   - word1 = uint32(top)  | uint32(pc)      << 32
//   - word2 = uint32(protoID) | uint16(nresults) << 32 | flags<<48
//     (tailcall<<48 / fresh<<49 / gibbous<<50)
//   - word3 = uint64(cl) (arena.GCRef closure mirror)
//   - word4 = uint64(nVarargs) (other bits reserved)
//
// The Spike 1 caller calls this 5 times in word_idx=0..4 order to complete the
// CI-segment write of enterLuaFrame.
func EmitFrameInlineWriteCIWord(buf []byte, wordIdx uint8, imm64 uint64) []byte {
	if wordIdx > 4 {
		wordIdx = 0 // fallback to guard against out-of-bounds
	}
	buf = EmitMovRcxImm64(buf, imm64)
	// mov [rax + wordIdx*8], rcx — 48 89 48 disp8
	// REX.W = 0x48 / opcode 0x89 (MOV r/m64, r64)
	// ModRM = mod 01 (disp8) + reg 001 (rcx) + rm 000 (rax) = 0x48
	// disp8 = wordIdx * 8
	buf = append(buf, 0x48, 0x89, 0x48, byte(int8(wordIdx)*8))
	return buf
}

// EncodedFrameInlineWriteCIWordLen is the byte count of the "write CI frame
// word_idx" byte-level template (10+4=14). See §9.20 Spike 1 caller length budget.
const EncodedFrameInlineWriteCIWordLen = 14

// FrameInlineCISlotWords is the CI frame 5-word input group used by amd64 Spike 1
// (see state.go::writeCISeg + packCIWord2; each word is burned in as an imm64 by
// the caller at compile time).
type FrameInlineCISlotWords struct {
	Word0 uint64 // base | funcIdx << 32
	Word1 uint64 // top | pc << 32
	Word2 uint64 // protoID | nresults<<32 | flags<<48 (tailcall<<48 / fresh<<49 / gibbous<<50)
	Word3 uint64 // cl (arena.GCRef closure mirror)
	Word4 uint64 // nVarargs
}

// EmitFrameInlineBuildVoid0ArgSkeleton emits the amd64 Spike 1 enterLuaFrame
// byte-level inline skeleton v2 (see §9.20 Option B Spike 1, with word3 switched
// to the runtime closure GCRef):
//
//  1. LoadCISlotAddr: rax = CallInfo[depth] frame start byte address (30 bytes)
//  2. WriteCIWord(0/1/2): write word0/1/2 imm (14*3 = 42 bytes)
//  3. LoadClosureGCRef(callA): rcx = R(callA) unpacked NaN-box GCRef payload (20 bytes)
//  4. WriteCIWordFromRcx(3): CI[depth].word3 = rcx (4 bytes)
//  5. WriteCIWord(4): write word4 imm (14 bytes)
//  6. CIDepthInc: ciDepth++ (10 bytes)
//
// **Total length**: 30 + 42 + 20 + 4 + 14 + 10 = 120 bytes (v1 = 110, word3
// switched to runtime loading adds 10 bytes)
//
// **The input words.Word3 is ignored** (the field position is kept so as not to
// break callers; v2 uses runtime cl loading, word3 is computed fresh by
// unpacking callA's NaN-box for the GCRef payload).
//
// **Gating** (the Spike 1 stage caller must guarantee):
//   - callee.NumParams=0 + !IsVararg + !NeedsArg + MaxStack≤32
//   - words.Word0/1/2/4 are burned in by the caller at compile time (base /
//     funcIdx / top / pc / protoID / nresults=0 / nVarargs=0; the word3 cl field
//     is ignored)
//   - at the template exit rax is the CallInfo[depth] frame address (for the
//     subsequent helper call / popCallInfo)
//
// **Still-remaining Spike 1 follow-up work** (not implemented in this batch):
//   - jump to the helper to enter callee execution (executeFrom or callee P4 segment)
//   - popCallInfo reverse (LoadCISlotAddr + CIDepthDec) + multi-return handling
//   - Compile/Run side wiring + e2e prove-the-path
func EmitFrameInlineBuildVoid0ArgSkeleton(buf []byte,
	ciDepthAddrOffset, ciSegBaseAddrOffset int32,
	callARecv uint8, // the SELF-segment callee is placed in the R(callARecv) slot
	words FrameInlineCISlotWords) []byte {
	// 1. rax = CallInfo[depth] frame start
	buf = EmitFrameInlineLoadCISlotAddr(buf, ciDepthAddrOffset, ciSegBaseAddrOffset)
	// 2. write word0/1/2
	buf = EmitFrameInlineWriteCIWord(buf, 0, words.Word0)
	buf = EmitFrameInlineWriteCIWord(buf, 1, words.Word1)
	buf = EmitFrameInlineWriteCIWord(buf, 2, words.Word2)
	// 3. rcx = R(callARecv) NaN-box payload (GCRef)
	buf = EmitFrameInlineLoadClosureGCRef(buf, callARecv)
	// 4. CI[depth].word3 = rcx
	buf = EmitFrameInlineWriteCIWordFromRcx(buf, 3)
	// 5. write word4
	buf = EmitFrameInlineWriteCIWord(buf, 4, words.Word4)
	// 6. ciDepth++
	buf = EmitFrameInlineCIDepthInc(buf, ciDepthAddrOffset)
	return buf
}

// EncodedFrameInlineBuildVoid0ArgSkeletonLen = 30 + 14*3 + 20 + 4 + 14 + 10 = 120.
const EncodedFrameInlineBuildVoid0ArgSkeletonLen = 120

// EmitFrameInlineBuildVoid0ArgSkeletonAbsolute is the same as
// EmitFrameInlineBuildVoid0ArgSkeleton but uses LoadCISlotAddrAbsolute
// (rax = absolute address, see §9.20.9 commit-5l bug fix).
//
// **Design difference**: the original BuildVoid0ArgSkeleton's LoadCISlotAddr
// computes rax = ciBaseW*8 + depth*40, a word offset into arena that cannot be
// dereferenced directly; this function uses the Absolute version, so
// rax = absolute address and the subsequent WriteCIWord writes are directly valid.
//
// **Byte sequence** (total length 130 bytes):
//  1. LoadCISlotAddrAbsolute (40B, 30 + 7 mov r14 + 3 add rax,r14)
//  2. WriteCIWord(0/1/2) imm 3 * 14 = 42B
//  3. LoadClosureGCRef (20B): rcx = R(callA) GCRef payload
//  4. WriteCIWordFromRcx(3) (4B): CI[depth].word3 = rcx
//  5. WriteCIWord(4) imm (14B)
//  6. CIDepthInc (10B)
//
// **Total**: 40 + 42 + 20 + 4 + 14 + 10 = 130 bytes (original 120 + 10 because the absolute load adds 10).
func EmitFrameInlineBuildVoid0ArgSkeletonAbsolute(buf []byte,
	ciDepthAddrOffset, ciSegBaseAddrOffset, arenaBaseOffset int32,
	callARecv uint8,
	words FrameInlineCISlotWords) []byte {
	// 1. rax = CI[depth] absolute address (Absolute version)
	buf = EmitFrameInlineLoadCISlotAddrAbsolute(buf, ciDepthAddrOffset, ciSegBaseAddrOffset, arenaBaseOffset)
	// 2. write word0/1/2
	buf = EmitFrameInlineWriteCIWord(buf, 0, words.Word0)
	buf = EmitFrameInlineWriteCIWord(buf, 1, words.Word1)
	buf = EmitFrameInlineWriteCIWord(buf, 2, words.Word2)
	// 3. rcx = R(callARecv) NaN-box payload (GCRef) — LoadClosureGCRef will reset r14 internally
	//    with the payloadMask; but step 1 already used r14 = arena base, and inside
	//    LoadClosureGCRef the payloadMask uses rdx, not r14. **Wait**: the original
	//    LoadClosureGCRef implementation is `EmitMovRdxImm64 + EmitAndRcxRdx`, r14 is not touched.
	buf = EmitFrameInlineLoadClosureGCRef(buf, callARecv)
	// 4. CI[depth].word3 = rcx
	buf = EmitFrameInlineWriteCIWordFromRcx(buf, 3)
	// 5. write word4
	buf = EmitFrameInlineWriteCIWord(buf, 4, words.Word4)
	// 6. ciDepth++
	buf = EmitFrameInlineCIDepthInc(buf, ciDepthAddrOffset)
	return buf
}

// EncodedFrameInlineBuildVoid0ArgSkeletonAbsoluteLen = 40 + 42 + 20 + 4 + 14 + 10 = 130.
const EncodedFrameInlineBuildVoid0ArgSkeletonAbsoluteLen = EncodedFrameInlineBuildVoid0ArgSkeletonLen + 10

// EmitFrameInlineLoadClosureGCRef emits the amd64 byte-level template that
// parses R(srcReg) NaN-box → rcx 48-bit GCRef (see §9.20 Option B Spike 1, the
// prerequisite for setting enterLuaFrame inline word3=cl).
//
// Byte sequence (7 + 10 + 3 = 20 bytes):
//
//	mov rcx, [rbx + srcReg*8]    ; 7 bytes EmitMovqRcxFromMemRbx
//	mov rdx, payloadMask         ; 10 bytes (payloadMask=0x0000FFFFFFFFFFFF)
//	and rcx, rdx                 ; 3 bytes
//
// After the template, rcx = R(srcReg)'s GCRef payload (byte-level equivalent to
// value.GCRefOf). The caller then does mov [rax+word_idx*8], rcx to write CI
// segment word3 (no need to load an imm via EmitMovRcxImm64).
//
// **Note**: rdx is unused in the LoadCISlotAddr segment, so it is safe to use as
// the mask temp register. rax holds the CI-segment address after LoadCISlotAddr;
// this template does not touch rax.
func EmitFrameInlineLoadClosureGCRef(buf []byte, srcReg uint8) []byte {
	buf = EmitMovqRcxFromMemRbx(buf, int32(srcReg)*8)
	buf = EmitMovRdxImm64(buf, 0x0000_FFFF_FFFF_FFFF) // payloadMask
	buf = EmitAndRcxRdx(buf)
	return buf
}

// EncodedFrameInlineLoadClosureGCRefLen = 7+10+3 = 20.
const EncodedFrameInlineLoadClosureGCRefLen = 20

// EmitFrameInlineWriteCIWordFromRcx emits a single 4-byte "mov [rax + wordIdx*8],
// rcx" (counterpart to EmitFrameInlineWriteCIWord but with imm64 replaced by
// rcx, saving the 10-byte imm load).
//
// Encoding: 48 89 48 disp8 (48 = REX.W / 89 = MOV r/m64 r64 / ModRM=01_001_000,
// i.e. 0x48 + disp8 = wordIdx * 8).
//
// Use case: Spike 1 word3 = cl GCRef (loaded into rcx by EmitFrameInlineLoadClosureGCRef).
func EmitFrameInlineWriteCIWordFromRcx(buf []byte, wordIdx uint8) []byte {
	if wordIdx > 4 {
		wordIdx = 0
	}
	return append(buf, 0x48, 0x89, 0x48, byte(int8(wordIdx)*8))
}

// EncodedFrameInlineWriteCIWordFromRcxLen = 4.
const EncodedFrameInlineWriteCIWordFromRcxLen = 4

// EmitFrameInlinePopVoid0ArgSkeleton emits the amd64 Spike 1 popCallInfo
// byte-level inline skeleton (see §9.20 Option B Spike 1 BuildVoid0ArgSkeleton,
// the reverse).
//
// **Spike 1 simplified shape**: after the Run-side helper finishes executing the
// callee Lua body, this template byte-level decrements ciDepth and **emits ret
// at the segment tail to exit the mmap segment** (see §9.20.9 commit-5l fixing
// the missing ret bug). **The remaining Go-side popCallInfo work** (readCISegInto
// reloading the caller th.cur) is left to the helper-compatible path (need not
// be byte-level inline, because th.cur is a Go-side cold field that the mmap
// segment does not read).
//
// Byte sequence (11 bytes): CIDepthDec 10 bytes (`mov rax, [r15+ciDepthOff]; dec
// qword ptr [rax]`) + ret 1 byte (c3).
//
// **rax segment return value**: CIDepthDec does not set rax explicitly; it
// inherits the value of the last mov (the ciDepthAddr field value, i.e. the host
// addr of the ciDepth mirror word, a nonzero large integer). But the trampoline
// checks raxResume==ExitInlineHelper(3) — rax will not collide with 3 (because
// ciDepthAddr is a large address), so the trampoline takes the regular
// stack-unwind exit and its behavior is unchanged. runFrameInlineDispatcher
// fails when raxResume!=0, so since commit-5l the PopVoid0Arg segment tail emits
// `xor eax, eax` to explicitly clear rax = 0 (ExitNormal).
func EmitFrameInlinePopVoid0ArgSkeleton(buf []byte, ciDepthAddrOffset int32) []byte {
	buf = EmitFrameInlineCIDepthDec(buf, ciDepthAddrOffset)
	// segment tail emit xor eax, eax (2 bytes: 31 c0) = rax = 0 = ExitNormal,
	// runFrameInlineDispatcher fails when raxResume!=0, this clears rax as a fallback.
	buf = append(buf, 0x31, 0xC0)
	// emit ret (1 byte: c3) — exit the mmap segment at the tail (commit-5l fixes the missing ret bug)
	buf = append(buf, 0xC3)
	return buf
}

// EncodedFrameInlinePopVoid0ArgSkeletonLen is the byte count of the Spike 1
// popCallInfo skeleton (13 bytes: 10 CIDepthDec + 2 xor eax,eax + 1 ret, see
// §9.20.9 commit-5l fix).
const EncodedFrameInlinePopVoid0ArgSkeletonLen = EncodedFrameInlineCIDepthIncDecLen + 3

// EmitFrameInlineExitHelperRequest emits the amd64 Spike 1 trampoline
// exit-resume protocol exit-helper-request segment byte-level inline template
// (see `docs/design/p4-method-jit/implementation-progress.md` §9.20.9 (4)
// trampoline rework + (6) compileSpecSelfCall emit rework).
//
// **Protocol position**: after BuildVoid0Arg, before PopVoid0Arg, exit the mmap
// segment and return to the trampoline. The trampoline asm checks
// RAX = ExitInlineHelper and routes to the Go dispatcher, which via
// jitCtx.exitArg0 = HelperRunCallee routes to the callee to run the frame
// logic; on completion it returns resumeAddr = codePageAddr + resumeOff, and the
// trampoline re-CALLs back into PopVoid0Arg.
//
// **Byte sequence** (amd64, total 27 bytes):
//
//	; write jitCtx.exitReasonCode = ExitInlineHelper (3)
//	mov rax, imm32        ; 5 bytes (B8 imm32): rax = ExitInlineHelper
//	mov [r15+exitReason], eax ; 4 bytes (41 89 47 disp8): write the 32-bit field
//	; write jitCtx.exitArg0 = HelperRunCallee (1)
//	mov rax, imm64        ; 10 bytes (48 B8 imm64): rax = helperCode
//	mov [r15+exitArg0], rax ; 4 bytes (49 89 47 disp8): write the 64-bit field
//	; finally mov rax, ExitInlineHelper to set the return value (trampoline checks RAX)
//	mov rax, imm32        ; 5 bytes (B8 imm32): rax = ExitInlineHelper
//	ret                    ; 1 byte (c3)
//	; total: 5 + 4 + 10 + 4 + 5 + 1 = 29 (but the duplicate mov rax can be optimized to 27, see below)
//
// **Optimization** (this implementation): exitReasonCode and the final rax
// return value are the same imm32 = ExitInlineHelper, so reorder to:
//
//	mov rax, helperCode  ; 10 bytes (48 B8 imm64)
//	mov [r15+exitArg0], rax ; 4 bytes
//	mov eax, ExitInlineHelper ; 5 bytes (B8 imm32)
//	mov [r15+exitReason], eax ; 4 bytes
//	; rax is now ExitInlineHelper (3), used as the return value
//	ret                  ; 1 byte
//	; total: 10 + 4 + 5 + 4 + 1 = 24 bytes
//
// **Parameters**:
//   - exitReasonOff: offset of the jitContext.exitReasonCode field (uint32, 4 bytes)
//   - exitArg0Off: offset of the jitContext.exitArg0 field (uint64, 8 bytes)
//   - helperCode: the helper request code (HelperRunCallee=1 etc.)
//
// **In the current Spike 1 stage archSupportsFrameInline=false blocks the real
// emission**, so the mmap segment does not actually emit this template; the
// trampoline asm checks RAX != 3 and always jumps to skipDispatch. This template
// is the engineering base anchor, enabled when commit-5 opens the switch.
func EmitFrameInlineExitHelperRequest(buf []byte, exitReasonOff, exitArg0Off int32, helperCode uint64) []byte {
	// 1. mov rax, helperCode (10 bytes: 48 B8 imm64)
	buf = append(buf, 0x48, 0xB8,
		byte(helperCode), byte(helperCode>>8), byte(helperCode>>16), byte(helperCode>>24),
		byte(helperCode>>32), byte(helperCode>>40), byte(helperCode>>48), byte(helperCode>>56))
	// 2. mov [r15+exitArg0Off], rax (4 bytes: 49 89 47 disp8)
	//    49 = REX.WB / 89 = MOV r/m64 r64 / ModRM 01_000_111 = 0x47 + disp8
	if exitArg0Off < -128 || exitArg0Off > 127 {
		// fallback: disp32 (7-byte version); in the Spike 1 stage an 8-bit offset is enough, so write disp8 here.
		// future Spike 4 with multi-frame multi-offset will extend to the disp32 form.
		buf = append(buf, 0x49, 0x89, 0x87, byte(exitArg0Off), byte(exitArg0Off>>8),
			byte(exitArg0Off>>16), byte(exitArg0Off>>24))
	} else {
		buf = append(buf, 0x49, 0x89, 0x47, byte(int8(exitArg0Off)))
	}
	// 3. mov eax, ExitInlineHelper (5 bytes: B8 imm32, 32-bit implicitly clears the high bits)
	buf = append(buf, 0xB8, 0x03, 0x00, 0x00, 0x00) // ExitInlineHelper=3
	// 4. mov [r15+exitReasonOff], eax (5 bytes: 41 89 47 disp8, 32-bit field)
	//    41 = REX.B / 89 = MOV r/m32 r32 / ModRM 01_000_111 = 0x47 + disp8
	if exitReasonOff < -128 || exitReasonOff > 127 {
		buf = append(buf, 0x41, 0x89, 0x87, byte(exitReasonOff), byte(exitReasonOff>>8),
			byte(exitReasonOff>>16), byte(exitReasonOff>>24))
	} else {
		buf = append(buf, 0x41, 0x89, 0x47, byte(int8(exitReasonOff)))
	}
	// 5. ret (1 byte: c3)
	buf = append(buf, 0xC3)
	return buf
}

// EncodedFrameInlineExitHelperRequestLen = 10 + 4 + 5 + 4 + 1 = 24
// (disp8 form, jitContext offset ≤ 127). The disp32 fallback form is +6 bytes.
const EncodedFrameInlineExitHelperRequestLen = 24
