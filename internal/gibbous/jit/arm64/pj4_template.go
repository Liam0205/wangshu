//go:build wangshu_p4 && arm64

// pj4_template.go —— PJ8 arm64 PJ4 table IC six-path byte-level templates
// (mirrors amd64 pj4_template.go, the complete set of six paths).
//
// **Not wired in yet** (per §9.12 remaining engineering work): the arm64
// trampoline asm + mmap+RX end-to-end path awaits a physical self-hosted
// runner; this batch only assembles the byte-level templates and validates
// the layout via byte-level unit tests, laying the groundwork for the next
// stage that actually wires it in.
//
// **arm64 register protocol** (per 06-backends.md §4.2; this batch keeps the
// templates in inline form that can only run once the trampoline is connected):
//   - x26 = valueStackBase (mirrors amd64 rbx)
//   - x27 = jitContext (mirrors amd64 r15)
//   - x28 = Go G (reserved by the Go runtime)
//   - x14 = arena base (loaded at template entry, mirrors amd64 r14)
//   - x0/x1/x2/x3 = scratch
//
// **Six paths**:
//   - EmitGetTableArrayHitArm64 (168 bytes)
//   - EmitGetTableNodeHitArm64 (mirrors amd64 159B)
//   - EmitSetTableArrayHitArm64 (mirrors amd64 113B)
//   - EmitSetTableNodeHitArm64 (mirrors amd64 140B)
//   - EmitSelfArrayHitArm64 (mirrors amd64 139B)
//   - EmitSelfNodeHitArm64 (mirrors amd64 166B)

package arm64

// qNanBoxTableTagShiftedArm64 is the arm64 high-16-bit value of the NaN-box
// table tag (mirrors the amd64 constant of the same name, but the arm64
// template uses LSR + CMP for a strict guard, comparing the 16-bit value
// directly).
const qNanBoxTableTagShiftedArm64 uint64 = 0xFFFC

// qNanBoxNilImmArm64 is the arm64 full NaN-box Nil value (mirrors amd64
// qNanBoxNilImm).
const qNanBoxNilImmArm64 uint64 = 0xFFFE_0000_0000_0000

// payloadMaskArm64 is the GCRef payload extraction mask (clears the high
// 16-bit NaN-box tag).
const payloadMaskArm64 uint64 = 0x0000_FFFF_FFFF_FFFF

// EmitGetTableArrayHitArm64 assembles the arm64 PJ4 IC ArrayHit byte-level
// direct-slot template (the arm64 mirror of the amd64 132-byte
// EmitGetTableArrayHit).
//
// **Not wired in yet** (the arm64 trampoline asm + mmap+RX end-to-end path
// awaits a physical self-hosted runner); this batch is pure byte-level
// template assembly plus byte-level unit tests to validate the layout.
//
// **Byte layout** (168 bytes, strict IsTable guard version):
//
//	[ 0-3 ] LDR x0, [x26 + B*8]          ; 4 (load R(B) NaN-box)
//	[ 4-7 ] LSR x0, x0, #48               ; 4 (IsTable shift)
//	[ 8-23] MOV x1, 0xFFFC imm64          ; 16 (load TagTable)
//	[24-27] CMP x0, x1                    ; 4
//	[28-31] B.NE deopt                    ; 4
//	[32-35] LDR x0, [x26 + B*8]           ; 4 (re-load R(B))
//	[36-51] MOV x1, payloadMask imm64     ; 16
//	[52-55] AND x0, x0, x1                ; 4 (GCRef extract)
//	[56-59] MOV x1, x0 (ORR x1, XZR, x0)  ; 4 (rcx = GCRef offset)
//	[60-63] LDR x14, [x27 + arenaBaseOff] ; 4 (load arena base → x14)
//	[64-67] ADD x2, x14, x1               ; 4 (SIB substitute: x2 = base + GCRef)
//	[68-71] LDR x0, [x2, #40]             ; 4 (table.word5 → x0)
//	[72-75] LSR x0, x0, #32               ; 4 (gen is in the high 32 bits)
//	[76-91] MOV x3, stableShape imm64     ; 16
//	[92-95] CMP x0, x3                    ; 4
//	[96-99] B.NE deopt                    ; 4
//	[100-103] LDR x0, [x2, #16]           ; 4 (table.arrayRef → x0)
//	[104-107] MOV x1, x0                   ; 4 (rcx = arrayRef offset)
//	[108-111] ADD x2, x14, x1              ; 4 (SIB substitute: x2 = base + arrayRef)
//	[112-115] LDR x0, [x2, #stableIndex*8] ; 4 (array[stableIndex])
//	[116-131] MOV x3, qNanBoxNil imm64    ; 16
//	[132-135] CMP x0, x3                    ; 4
//	[136-139] B.EQ deopt                   ; 4
//	[140-143] STR x0, [x26 + A*8]         ; 4 (store R(A))
//	[144-147] RET                          ; 4
//	[148-163] MOV x0, deoptCode imm64     ; 16 (deopt block)
//	[164-167] RET                          ; 4
//	——— total 168 bytes (amd64 132 + arm64 SIB substitute + MOV imm64 length delta) ———
//
// **36-byte delta vs amd64's 132 bytes**:
//   - arm64 SIB substitute: 2x ADD + LDR (8 bytes) replaces amd64's single
//     SIB ldr (10 bytes) → saves 2 bytes but adds 2 instructions
//   - arm64 MOV imm64 sequence is 16 bytes (movz+3*movk) vs amd64's 10 bytes
//     (mov rax imm64) → +6 bytes each, 4 MOV imm64 uses = +24 bytes
//   - total delta ~36 bytes, as expected
//
// **Preconditions** (per 06 §4.2; the arm64 trampoline is deferred to PJ8+):
//   - x26 = valueStackBase
//   - x27 = jitContext
//   - x28 = Go G (reserved by the Go runtime)
//   - x14 = arena base (loaded at this template's entry)
//   - x0/x1/x2/x3 = scratch
//
// **deopt path**: when the Run side sees x0==deoptCode it calls host.GetTable
// byte-equal to P1 (P1 icGetTable handles both ArrayHit and NodeHit; the arm64
// side reuses it).
func EmitGetTableArrayHitArm64(buf []byte, aReg, bReg uint8, stableShape, stableIndex uint32,
	arenaBaseOff uint16, deoptCode uint64) []byte {
	// 1. LDR x0, [x26 + B*8] (load R(B))
	buf = EmitLdrXtFromXnDisp(buf, 0, 26, uint16(bReg)*8)

	// 2. LSR x0, x0, #48 (IsTable shift)
	buf = EmitLsrXdImm6(buf, 0, 0, 48)

	// 3. MOV x1, 0xFFFC imm64
	buf = EmitMovXdImm64(buf, 1, qNanBoxTableTagShiftedArm64)

	// 4. CMP x0, x1
	buf = EmitCmpXnXm(buf, 0, 1)

	// 5. B.NE deopt (placeholder, patched afterwards)
	bNeTagOff := len(buf)
	buf = EmitBCond(buf, CondNE, 0)

	// 6. re-load R(B)
	buf = EmitLdrXtFromXnDisp(buf, 0, 26, uint16(bReg)*8)

	// 7. MOV x1, payloadMask imm64
	buf = EmitMovXdImm64(buf, 1, payloadMaskArm64)

	// 8. AND x0, x0, x1
	buf = EmitAndXdXnXm(buf, 0, 0, 1)

	// 9. MOV x1, x0 (rcx = GCRef offset)
	buf = EmitMovXdFromXn(buf, 1, 0)

	// 10. LDR x14, [x27 + arenaBaseOff] (load arena base → x14)
	buf = EmitLdrXtFromXnDisp(buf, 14, 27, arenaBaseOff)

	// 11. ADD x2, x14, x1 (SIB substitute: base + GCRef)
	buf = EmitAddXdXnXm(buf, 2, 14, 1)

	// 12. LDR x0, [x2, #40] (table.word5 → x0)
	buf = EmitLdrXtFromXnDisp(buf, 0, 2, 40)

	// 13. LSR x0, x0, #32 (gen is in the high 32 bits)
	buf = EmitLsrXdImm6(buf, 0, 0, 32)

	// 14. MOV x3, stableShape imm64
	buf = EmitMovXdImm64(buf, 3, uint64(stableShape))

	// 15. CMP x0, x3
	buf = EmitCmpXnXm(buf, 0, 3)

	// 16. B.NE deopt
	bNeShapeOff := len(buf)
	buf = EmitBCond(buf, CondNE, 0)

	// 17. LDR x0, [x2, #16] (table.arrayRef → x0)
	buf = EmitLdrXtFromXnDisp(buf, 0, 2, 16)

	// 18. MOV x1, x0 (arrayRef offset)
	buf = EmitMovXdFromXn(buf, 1, 0)

	// 19. ADD x2, x14, x1 (SIB substitute: base + arrayRef)
	buf = EmitAddXdXnXm(buf, 2, 14, 1)

	// 20. LDR x0, [x2, #stableIndex*8] (array[stableIndex])
	buf = EmitLdrXtFromXnDisp(buf, 0, 2, uint16(stableIndex)*8)

	// 21. MOV x3, qNanBoxNil imm64
	buf = EmitMovXdImm64(buf, 3, qNanBoxNilImmArm64)

	// 22. CMP x0, x3
	buf = EmitCmpXnXm(buf, 0, 3)

	// 23. B.EQ deopt (nil → deopt)
	bEqNilOff := len(buf)
	buf = EmitBCond(buf, CondEQ, 0)

	// 24. STR x0, [x26 + A*8] (store R(A))
	buf = EmitStrXtToXnDisp(buf, 0, 26, uint16(aReg)*8)

	// 25. RET
	buf = EmitRet(buf)

	// 26. deopt block
	deoptStart := len(buf)
	buf = EmitMovXdImm64(buf, 0, deoptCode)
	buf = EmitRet(buf)

	// 27. patch B.cond imm19 = (deoptStart - this B.cond itself) / 4
	patchBCondImm19(buf, bNeTagOff, int32(deoptStart-bNeTagOff)/4)
	patchBCondImm19(buf, bNeShapeOff, int32(deoptStart-bNeShapeOff)/4)
	patchBCondImm19(buf, bEqNilOff, int32(deoptStart-bEqNilOff)/4)

	return buf
}

// EncodedGetTableArrayHitArm64Len is the byte count of the arm64 PJ4 IC
// ArrayHit template (168).
const EncodedGetTableArrayHitArm64Len = 168

// EmitGetTableNodeHitArm64 assembles the arm64 PJ4 IC NodeHit byte-level
// direct-slot template (the arm64 mirror of the amd64 159-byte
// EmitGetTableNodeHit).
//
// **Not wired in yet** (same as the file header note): the arm64 trampoline
// asm + mmap+RX end-to-end path awaits a physical self-hosted runner; this
// batch is pure byte-level template assembly plus byte-level unit tests to
// validate the layout.
//
// **NodeHit vs ArrayHit differences** (same as amd64 NodeHit):
//   - reads word3=nodeRef (offset 24) instead of word2=arrayRef (offset 16)
//   - node[idx] stride is 24 bytes (nodeWords=3: key/val/next) instead of
//     array[idx]'s 8 bytes
//   - one extra key comparison (NodeKey == stableKey verifies against key
//     degeneration / the __index chain)
//
// **Byte layout** (196 bytes, ArrayHit 168 + key comparison segment 28):
//
//	[ 0-31] IsTable guard (LDR + LSR + MOV imm64 + CMP + B.NE, 32 bytes)
//	[32-67] re-load + payloadMask AND + MOV x1,x0 + LDR x14 + ADD x2 (36 bytes)
//	[68-71] LDR x0, [x2, #40]                  ; 4 (word5)
//	[72-75] LSR x0, x0, #32                    ; 4
//	[76-91] MOV x3, stableShape                ; 16
//	[92-95] CMP x0, x3                         ; 4
//	[96-99] B.NE deopt                         ; 4
//	[100-103] LDR x0, [x2, #24]                ; 4 (**nodeRef** word3, NodeHit branch)
//	[104-107] MOV x1, x0                       ; 4 (rcx = nodeRef offset)
//	[108-111] ADD x2, x14, x1                  ; 4 (SIB substitute: new base for node)
//	[112-115] LDR x0, [x2, #stableIndex*24]    ; 4 (NodeKey)
//	[116-131] MOV x3, stableKey imm64          ; 16
//	[132-135] CMP x0, x3                       ; 4
//	[136-139] B.NE deopt                       ; 4 (NodeKey != stableKey)
//	[140-143] LDR x0, [x2, #stableIndex*24+8]  ; 4 (NodeVal)
//	[144-159] MOV x3, qNanBoxNil imm64         ; 16
//	[160-163] CMP x0, x3                       ; 4
//	[164-167] B.EQ deopt                       ; 4 (NodeVal == Nil)
//	[168-171] STR x0, [x26 + A*8]              ; 4
//	[172-175] RET                              ; 4
//	[176-191] MOV x0, deoptCode imm64          ; 16 (deopt block)
//	[192-195] RET                              ; 4
//	——— total 196 bytes ———
//
// **stableKey frozen at compile time** (same as amd64 NodeHit):
//   - numeric key: value.NumberValue(K) raw bits (IEEE 754 NaN-box)
//   - string key: value.MakeGC(TagString, ref) NaN-box, with ref already
//     interned at compile time and immutable
//   - comparing the whole NaN-box is equivalent to keyEqual (same source as
//     ic.go::keyEqual)
//
// **deopt path**: when the Run side sees x0==deoptCode it calls host.GetTable
// byte-equal to P1 (P1 icGetTable handles NodeHit; via IC + hash + __index
// metamethod chain).
func EmitGetTableNodeHitArm64(buf []byte, aReg, bReg uint8,
	stableShape, stableIndex uint32, stableKey uint64,
	arenaBaseOff uint16, deoptCode uint64) []byte {
	// 1-5. strict IsTable guard (LDR + LSR + MOV imm64 + CMP + B.NE, 32 bytes)
	buf = EmitLdrXtFromXnDisp(buf, 0, 26, uint16(bReg)*8)
	buf = EmitLsrXdImm6(buf, 0, 0, 48)
	buf = EmitMovXdImm64(buf, 1, qNanBoxTableTagShiftedArm64)
	buf = EmitCmpXnXm(buf, 0, 1)
	bNeTagOff := len(buf)
	buf = EmitBCond(buf, CondNE, 0)

	// 6-11. re-load + GCRef extract + arena base + ADD x2 SIB (36 bytes)
	buf = EmitLdrXtFromXnDisp(buf, 0, 26, uint16(bReg)*8)
	buf = EmitMovXdImm64(buf, 1, payloadMaskArm64)
	buf = EmitAndXdXnXm(buf, 0, 0, 1)
	buf = EmitMovXdFromXn(buf, 1, 0)
	buf = EmitLdrXtFromXnDisp(buf, 14, 27, arenaBaseOff)
	buf = EmitAddXdXnXm(buf, 2, 14, 1)

	// 12-13. load word5 + LSR 32 (gen is in the high 32 bits)
	buf = EmitLdrXtFromXnDisp(buf, 0, 2, 40)
	buf = EmitLsrXdImm6(buf, 0, 0, 32)

	// 14-16. gen check + B.NE deopt
	buf = EmitMovXdImm64(buf, 3, uint64(stableShape))
	buf = EmitCmpXnXm(buf, 0, 3)
	bNeShapeOff := len(buf)
	buf = EmitBCond(buf, CondNE, 0)

	// 17-19. **NodeHit branch**: load nodeRef (word3, offset 24) + new SIB base
	buf = EmitLdrXtFromXnDisp(buf, 0, 2, 24)
	buf = EmitMovXdFromXn(buf, 1, 0)
	buf = EmitAddXdXnXm(buf, 2, 14, 1)

	// 20-23. NodeKey load + stableKey comparison + B.NE deopt
	buf = EmitLdrXtFromXnDisp(buf, 0, 2, uint16(stableIndex*24))
	buf = EmitMovXdImm64(buf, 3, stableKey)
	buf = EmitCmpXnXm(buf, 0, 3)
	bNeKeyOff := len(buf)
	buf = EmitBCond(buf, CondNE, 0)

	// 24-27. NodeVal load + nil check + B.EQ deopt
	buf = EmitLdrXtFromXnDisp(buf, 0, 2, uint16(stableIndex*24+8))
	buf = EmitMovXdImm64(buf, 3, qNanBoxNilImmArm64)
	buf = EmitCmpXnXm(buf, 0, 3)
	bEqNilOff := len(buf)
	buf = EmitBCond(buf, CondEQ, 0)

	// 28-29. store R(A) + RET
	buf = EmitStrXtToXnDisp(buf, 0, 26, uint16(aReg)*8)
	buf = EmitRet(buf)

	// 30. deopt block
	deoptStart := len(buf)
	buf = EmitMovXdImm64(buf, 0, deoptCode)
	buf = EmitRet(buf)

	// 31. patch B.cond imm19 = (deoptStart - this B.cond itself) / 4
	patchBCondImm19(buf, bNeTagOff, int32(deoptStart-bNeTagOff)/4)
	patchBCondImm19(buf, bNeShapeOff, int32(deoptStart-bNeShapeOff)/4)
	patchBCondImm19(buf, bNeKeyOff, int32(deoptStart-bNeKeyOff)/4)
	patchBCondImm19(buf, bEqNilOff, int32(deoptStart-bEqNilOff)/4)

	return buf
}

// EncodedGetTableNodeHitArm64Len is the byte count of the arm64 PJ4 IC NodeHit
// template (196).
const EncodedGetTableNodeHitArm64Len = 196

// EmitSetTableArrayHitArm64 assembles the arm64 PJ4 IC SETTABLE ArrayHit
// byte-level inline write-back template (the arm64 mirror of the amd64
// 113-byte EmitSetTableArrayHit).
//
// **Form**: `function(t, v) t[K] = v end`, where K is a numeric constant that
// hits the array segment (luac emits SETTABLE A B C with A=R(t) / B=K idx>=256
// / C=R(v)). When IC[0].Kind = ArrayHit and Shape/Index hit, it does a
// byte-level inline write-back to array[stableIndex].
//
// **Byte layout** (144 bytes: GETTABLE ArrayHit 168, minus the 24-byte nil
// check segment, plus a 0-byte write-back segment; in practice it drops the
// LDR/MOV/CMP/B.EQ nil segment (24 bytes) but adds the load-R(C)-value +
// write-back (8 bytes), net -16 → 24 bytes shorter than GETTABLE ArrayHit, so
// 168 - 24 = 144):
//
//	[ 0-31] IsTable guard                          ; 32 (same as GETTABLE ArrayHit)
//	[32-67] re-load + payloadMask + AND + SIB base ; 36
//	[68-99] word5 + LSR + stableShape + CMP + B.NE ; 32 (gen check)
//	[100-103] LDR x0, [x2, #16]                    ; 4 (table.arrayRef)
//	[104-107] MOV x1, x0                           ; 4 (rcx = arrayRef offset)
//	[108-111] ADD x2, x14, x1                      ; 4 (SIB substitute: base for array)
//	[112-115] LDR x3, [x26 + C*8]                  ; 4 (load R(C) value → x3)
//	[116-119] STR x3, [x2, #stableIndex*8]         ; 4 (write-back store array[idx])
//	[120-123] RET                                  ; 4 (setter has no R(A) write)
//	[124-139] MOV x0, deoptCode imm64              ; 16 (deopt block)
//	[140-143] RET                                  ; 4
//	——— total 144 bytes (amd64 113 + arm64 MOV imm64/SIB delta ~31 bytes) ———
//
// **Design simplifications** (same engineering boundary as amd64 SETTABLE
// ArrayHit):
//   - **does not verify the existing array[stableIndex] != nil** (the
//     new-key path) — the P1 interpreter IC hit protocol itself requires
//     that slot to be non-nil
//   - **assumes no __newindex metatable** (meta-freeze assumption)
//
// The strict version (adding ~13 bytes to verify the existing nil + ~13 bytes
// to verify __newindex) is deferred to PJ4+.
//
// **Preconditions** (per 06 §4.2; the arm64 trampoline is deferred to PJ8+):
//   - x26 = valueStackBase / x27 = jitContext / x14 = arena base
//   - x0/x1/x2/x3 = scratch
//
// **deopt path**: when the Run side sees x0==deoptCode it calls host.SetTable
// byte-equal to P1 (P1 icSetTable handles both ArrayHit and NodeHit). The
// setter form returns RETURN A 1, and the Run side does not read R(A) when
// retB=1.
func EmitSetTableArrayHitArm64(buf []byte, aReg, cReg uint8,
	stableShape, stableIndex uint32,
	arenaBaseOff uint16, deoptCode uint64) []byte {
	// 1-5. strict IsTable guard (32 bytes)
	buf = EmitLdrXtFromXnDisp(buf, 0, 26, uint16(aReg)*8)
	buf = EmitLsrXdImm6(buf, 0, 0, 48)
	buf = EmitMovXdImm64(buf, 1, qNanBoxTableTagShiftedArm64)
	buf = EmitCmpXnXm(buf, 0, 1)
	bNeTagOff := len(buf)
	buf = EmitBCond(buf, CondNE, 0)

	// 6-11. re-load + GCRef extract + arena base + ADD x2 SIB (36 bytes)
	buf = EmitLdrXtFromXnDisp(buf, 0, 26, uint16(aReg)*8)
	buf = EmitMovXdImm64(buf, 1, payloadMaskArm64)
	buf = EmitAndXdXnXm(buf, 0, 0, 1)
	buf = EmitMovXdFromXn(buf, 1, 0)
	buf = EmitLdrXtFromXnDisp(buf, 14, 27, arenaBaseOff)
	buf = EmitAddXdXnXm(buf, 2, 14, 1)

	// 12-13. word5 load + LSR 32
	buf = EmitLdrXtFromXnDisp(buf, 0, 2, 40)
	buf = EmitLsrXdImm6(buf, 0, 0, 32)

	// 14-16. gen check + B.NE deopt
	buf = EmitMovXdImm64(buf, 3, uint64(stableShape))
	buf = EmitCmpXnXm(buf, 0, 3)
	bNeShapeOff := len(buf)
	buf = EmitBCond(buf, CondNE, 0)

	// 17-19. load arrayRef (word2, offset 16) + new SIB base for array
	buf = EmitLdrXtFromXnDisp(buf, 0, 2, 16)
	buf = EmitMovXdFromXn(buf, 1, 0)
	buf = EmitAddXdXnXm(buf, 2, 14, 1)

	// 20. **setter branch**: load R(C) value → x3 (use x3 to avoid reusing x0)
	buf = EmitLdrXtFromXnDisp(buf, 3, 26, uint16(cReg)*8)

	// 21. write-back store: STR x3, [x2, #stableIndex*8]
	buf = EmitStrXtToXnDisp(buf, 3, 2, uint16(stableIndex*8))

	// 22. RET (setter has no R(A) write)
	buf = EmitRet(buf)

	// 23. deopt block
	deoptStart := len(buf)
	buf = EmitMovXdImm64(buf, 0, deoptCode)
	buf = EmitRet(buf)

	// 24. patch B.cond imm19
	patchBCondImm19(buf, bNeTagOff, int32(deoptStart-bNeTagOff)/4)
	patchBCondImm19(buf, bNeShapeOff, int32(deoptStart-bNeShapeOff)/4)

	return buf
}

// EncodedSetTableArrayHitArm64Len is the byte count of the arm64 PJ4 SETTABLE
// ArrayHit template (144).
const EncodedSetTableArrayHitArm64Len = 144

// EmitSetTableNodeHitArm64 assembles the arm64 PJ4 IC SETTABLE NodeHit
// byte-level inline write-back template (the arm64 mirror of the amd64
// 140-byte EmitSetTableNodeHit).
//
// **Form**: `function(t, v) t[K] = v end`, where K is a string/arbitrary key
// in the hash segment. When IC[0].Kind=NodeHit and Shape/Index/Key hit, it
// does a byte-level inline write-back to node[stableIndex].val.
//
// **Combined differences vs SETTABLE ArrayHit / GETTABLE NodeHit**:
//   - vs SETTABLE ArrayHit: reads word3=nodeRef (offset 24) instead of
//     word2=arrayRef (offset 16), the node stride is 24 bytes, and there is
//     an extra key comparison segment
//   - vs GETTABLE NodeHit: drops NodeVal load + nil check + STR R(A), replaced
//     by LDR R(C) value + write-back STR NodeVal
//
// **Byte layout** (172 bytes: GETTABLE NodeHit 196 - NodeVal/nil/storeRA 24 +
// store value 0; in practice GETTABLE NodeHit's 24 + STR R(A) 4 = 28, the SET
// segment's LDR + STR = 8, net -20 → 196 - 24 = 172):
//
//	[ 0-139] same as GETTABLE NodeHit up to B.NE key (IsTable + GCRef + SIB +
//	         gen check + nodeRef + NodeKey + stableKey + CMP + B.NE) = 140
//	[140-143] LDR x3, [x26 + C*8]              ; 4 (setter: load R(C) value → x3)
//	[144-147] STR x3, [x2, #stableIndex*24+8]  ; 4 (write-back store NodeVal)
//	[148-151] RET                              ; 4 (setter has no R(A) write)
//	[152-167] MOV x0, deoptCode imm64          ; 16 (deopt block)
//	[168-171] RET                              ; 4
//	——— total 172 bytes ———
//
// **Design simplifications** (same engineering boundary as amd64 SETTABLE
// NodeHit / ArrayHit):
//   - does not verify the existing NodeVal != nil (the new-key path)
//   - assumes no __newindex metatable
//
// **deopt path**: when the Run side sees x0==deoptCode it calls host.SetTable
// byte-equal to P1 (P1 icSetTable handles NodeHit; via IC + hash + __newindex
// metamethod chain). The setter form has retB=1, and the Run side's DoReturn
// does not read R(A).
//
// **Preconditions** (per 06 §4.2): x26/x27/x14/x0-x3 same as GETTABLE NodeHit.
func EmitSetTableNodeHitArm64(buf []byte, aReg, cReg uint8,
	stableShape, stableIndex uint32, stableKey uint64,
	arenaBaseOff uint16, deoptCode uint64) []byte {
	// 1-5. strict IsTable guard
	buf = EmitLdrXtFromXnDisp(buf, 0, 26, uint16(aReg)*8)
	buf = EmitLsrXdImm6(buf, 0, 0, 48)
	buf = EmitMovXdImm64(buf, 1, qNanBoxTableTagShiftedArm64)
	buf = EmitCmpXnXm(buf, 0, 1)
	bNeTagOff := len(buf)
	buf = EmitBCond(buf, CondNE, 0)

	// 6-11. re-load + GCRef extract + arena base + ADD x2 SIB
	buf = EmitLdrXtFromXnDisp(buf, 0, 26, uint16(aReg)*8)
	buf = EmitMovXdImm64(buf, 1, payloadMaskArm64)
	buf = EmitAndXdXnXm(buf, 0, 0, 1)
	buf = EmitMovXdFromXn(buf, 1, 0)
	buf = EmitLdrXtFromXnDisp(buf, 14, 27, arenaBaseOff)
	buf = EmitAddXdXnXm(buf, 2, 14, 1)

	// 12-16. gen check
	buf = EmitLdrXtFromXnDisp(buf, 0, 2, 40)
	buf = EmitLsrXdImm6(buf, 0, 0, 32)
	buf = EmitMovXdImm64(buf, 3, uint64(stableShape))
	buf = EmitCmpXnXm(buf, 0, 3)
	bNeShapeOff := len(buf)
	buf = EmitBCond(buf, CondNE, 0)

	// 17-19. **NodeHit branch**: load nodeRef + new SIB base for node
	buf = EmitLdrXtFromXnDisp(buf, 0, 2, 24)
	buf = EmitMovXdFromXn(buf, 1, 0)
	buf = EmitAddXdXnXm(buf, 2, 14, 1)

	// 20-23. NodeKey load + stableKey comparison + B.NE deopt
	buf = EmitLdrXtFromXnDisp(buf, 0, 2, uint16(stableIndex*24))
	buf = EmitMovXdImm64(buf, 3, stableKey)
	buf = EmitCmpXnXm(buf, 0, 3)
	bNeKeyOff := len(buf)
	buf = EmitBCond(buf, CondNE, 0)

	// 24-25. **setter branch**: LDR R(C) value → x3 + write-back STR NodeVal
	buf = EmitLdrXtFromXnDisp(buf, 3, 26, uint16(cReg)*8)
	buf = EmitStrXtToXnDisp(buf, 3, 2, uint16(stableIndex*24+8))

	// 26. RET (setter has no R(A) write)
	buf = EmitRet(buf)

	// 27. deopt block
	deoptStart := len(buf)
	buf = EmitMovXdImm64(buf, 0, deoptCode)
	buf = EmitRet(buf)

	// 28. patch B.cond imm19
	patchBCondImm19(buf, bNeTagOff, int32(deoptStart-bNeTagOff)/4)
	patchBCondImm19(buf, bNeShapeOff, int32(deoptStart-bNeShapeOff)/4)
	patchBCondImm19(buf, bNeKeyOff, int32(deoptStart-bNeKeyOff)/4)

	return buf
}

// EncodedSetTableNodeHitArm64Len is the byte count of the arm64 PJ4 SETTABLE
// NodeHit template (172).
const EncodedSetTableNodeHitArm64Len = 172

// EmitSelfArrayHitArm64 assembles the arm64 PJ4 IC SELF ArrayHit byte-level
// inline template (the arm64 mirror of the amd64 139-byte EmitSelfArrayHit).
//
// **SELF opcode semantics** (per bytecode/opcode.go::SELF):
//
//	R(A+1) := R(B)
//	R(A)   := R(B)[RK(C)]
//
// That is, the `obj:method()` form: first copy obj into R(A+1) (the self/this
// argument), then R(A) = R(B).method loads the method function. Followed by a
// CALL R(A) R(A+1) ... invocation.
//
// IC ArrayHit hit condition: the method key is a numeric constant and hits the
// array segment (rare but a valid form); the common case is NodeHit (a string
// key method name).
//
// **Byte layout** (172 bytes, ArrayHit 168 + R(A+1) copy segment 4):
//
//	[ 0-3 ] LDR x0, [x26 + B*8]          ; 4 (load R(B) obj NaN-box)
//	[ 4-7 ] STR x0, [x26 + (A+1)*8]      ; 4 (**SELF extra**: store R(A+1) = obj)
//	[ 8-35] IsTable guard (LSR + MOV imm64 + CMP + B.NE)  ; 28 (entry LDR already used)
//	[36-71] re-load + payloadMask + AND + SIB ; 36
//	[72-103] word5 + LSR + stableShape + CMP + B.NE ; 32 (gen check)
//	[104-107] LDR x0, [x2, #16]          ; 4 (table.arrayRef)
//	[108-111] MOV x1, x0                  ; 4
//	[112-115] ADD x2, x14, x1             ; 4 (SIB base for array)
//	[116-119] LDR x0, [x2, #stableIndex*8]; 4 (array[stableIndex])
//	[120-135] MOV x3, qNanBoxNil          ; 16
//	[136-139] CMP x0, x3                  ; 4
//	[140-143] B.EQ deopt                  ; 4
//	[144-147] STR x0, [x26 + A*8]         ; 4 (store R(A) method)
//	[148-151] RET                         ; 4
//	[152-171] MOV x0, deoptCode + RET     ; 20 (deopt block)
//	——— total 172 bytes ———
//
// **SELF design points** (same as amd64 SELF ArrayHit):
//   - R(A+1) is written **before** the IsTable guard, because the deopt path
//     into host.GetTable also needs R(A+1) already set (same steps as the P1
//     SELF case: setReg(A+1, B) → icGetTable → setReg(A)); if R(A+1) were not
//     set at deopt time it would break the byte-equal P1 invariant
//   - the entry LDR x0 ordering is preserved: load first → immediately STR
//     R(A+1) → then LSR (LSR clobbers rax but R(A+1) has already been stored)
//
// **deopt path**: when the Run side sees x0==deoptCode it calls host.GetTable
// byte-equal to P1 (R(A+1)=R(B) already stored; P1 icGetTable handles both
// ArrayHit and NodeHit).
//
// **Preconditions** (per 06 §4.2): x26/x27/x14/x0-x3 same as GETTABLE ArrayHit.
func EmitSelfArrayHitArm64(buf []byte, aReg, bReg uint8,
	stableShape, stableIndex uint32,
	arenaBaseOff uint16, deoptCode uint64) []byte {
	// 1. LDR x0, [x26+B*8] (load R(B) obj)
	buf = EmitLdrXtFromXnDisp(buf, 0, 26, uint16(bReg)*8)

	// 2. **SELF extra**: STR x0, [x26+(A+1)*8] (self/this argument)
	buf = EmitStrXtToXnDisp(buf, 0, 26, uint16(aReg+1)*8)

	// 3-7. strict IsTable guard
	buf = EmitLsrXdImm6(buf, 0, 0, 48)
	buf = EmitMovXdImm64(buf, 1, qNanBoxTableTagShiftedArm64)
	buf = EmitCmpXnXm(buf, 0, 1)
	bNeTagOff := len(buf)
	buf = EmitBCond(buf, CondNE, 0)

	// 8-13. re-load + GCRef extract + arena base + ADD x2 SIB
	buf = EmitLdrXtFromXnDisp(buf, 0, 26, uint16(bReg)*8)
	buf = EmitMovXdImm64(buf, 1, payloadMaskArm64)
	buf = EmitAndXdXnXm(buf, 0, 0, 1)
	buf = EmitMovXdFromXn(buf, 1, 0)
	buf = EmitLdrXtFromXnDisp(buf, 14, 27, arenaBaseOff)
	buf = EmitAddXdXnXm(buf, 2, 14, 1)

	// 14-18. gen check
	buf = EmitLdrXtFromXnDisp(buf, 0, 2, 40)
	buf = EmitLsrXdImm6(buf, 0, 0, 32)
	buf = EmitMovXdImm64(buf, 3, uint64(stableShape))
	buf = EmitCmpXnXm(buf, 0, 3)
	bNeShapeOff := len(buf)
	buf = EmitBCond(buf, CondNE, 0)

	// 19-21. arrayRef + new SIB base for array
	buf = EmitLdrXtFromXnDisp(buf, 0, 2, 16)
	buf = EmitMovXdFromXn(buf, 1, 0)
	buf = EmitAddXdXnXm(buf, 2, 14, 1)

	// 22. LDR array[stableIndex]
	buf = EmitLdrXtFromXnDisp(buf, 0, 2, uint16(stableIndex*8))

	// 23-25. nil check + B.EQ deopt
	buf = EmitMovXdImm64(buf, 3, qNanBoxNilImmArm64)
	buf = EmitCmpXnXm(buf, 0, 3)
	bEqNilOff := len(buf)
	buf = EmitBCond(buf, CondEQ, 0)

	// 26. STR x0, [x26+A*8] (store R(A) = method function)
	buf = EmitStrXtToXnDisp(buf, 0, 26, uint16(aReg)*8)

	// 27. RET
	buf = EmitRet(buf)

	// 28. deopt block
	deoptStart := len(buf)
	buf = EmitMovXdImm64(buf, 0, deoptCode)
	buf = EmitRet(buf)

	// 29. patch B.cond imm19
	patchBCondImm19(buf, bNeTagOff, int32(deoptStart-bNeTagOff)/4)
	patchBCondImm19(buf, bNeShapeOff, int32(deoptStart-bNeShapeOff)/4)
	patchBCondImm19(buf, bEqNilOff, int32(deoptStart-bEqNilOff)/4)

	return buf
}

// EncodedSelfArrayHitArm64Len is the byte count of the arm64 PJ4 SELF ArrayHit
// template (172).
const EncodedSelfArrayHitArm64Len = 172

// EmitSelfNodeHitArm64 assembles the arm64 PJ4 IC SELF NodeHit byte-level
// inline template (the arm64 mirror of the amd64 166-byte EmitSelfNodeHit).
//
// **SELF opcode semantics** (per bytecode/opcode.go::SELF):
//
//	R(A+1) := R(B)
//	R(A)   := R(B)[RK(C)]
//
// **NodeHit vs ArrayHit**: NodeHit walks the node chain (a string key method
// name is the common luac form; a numeric key obj:1() also goes through
// NodeHit if it hits the hash segment), with one extra hit condition of
// NodeKey == stableKey.
//
// **Byte layout** (200 bytes, NodeHit 196 + R(A+1) copy segment 4):
//
//	[ 0-3 ] LDR x0, [x26 + B*8]              ; 4 (load R(B) obj)
//	[ 4-7 ] STR x0, [x26 + (A+1)*8]          ; 4 (**SELF first step**: R(A+1)=obj)
//	[ 8-11] LSR x0, x0, #48                  ; 4 (IsTable shift)
//	[12-27] MOV x1, 0xFFFC imm64              ; 16
//	[28-31] CMP x0, x1                        ; 4
//	[32-35] B.NE deopt                        ; 4
//	[36-71] re-load + payloadMask + AND + SIB ; 36
//	[72-103] word5 + LSR + stableShape + CMP + B.NE ; 32 (gen check)
//	[104-107] LDR x0, [x2, #24]              ; 4 (**nodeRef** word3)
//	[108-111] MOV x1, x0                      ; 4
//	[112-115] ADD x2, x14, x1                 ; 4 (SIB base for node)
//	[116-119] LDR x0, [x2, #stableIndex*24]   ; 4 (NodeKey)
//	[120-135] MOV x3, stableKey imm64         ; 16
//	[136-139] CMP x0, x3                      ; 4
//	[140-143] B.NE deopt                      ; 4 (NodeKey != stableKey)
//	[144-147] LDR x0, [x2, #stableIndex*24+8] ; 4 (NodeVal)
//	[148-163] MOV x3, qNanBoxNil imm64        ; 16
//	[164-167] CMP x0, x3                      ; 4
//	[168-171] B.EQ deopt                      ; 4 (NodeVal == Nil)
//	[172-175] STR x0, [x26 + A*8]             ; 4 (store R(A) = method)
//	[176-179] RET                             ; 4
//	[180-195] MOV x0, deoptCode imm64         ; 16 (deopt block)
//	[196-199] RET                             ; 4
//	——— total 200 bytes ———
//
// **SELF design points** (same as SELF ArrayHit + amd64 SELF NodeHit):
//   - R(A+1) is written **before** the IsTable guard (same steps as the
//     byte-equal P1 SELF case)
//   - the rest reuses the EmitGetTableNodeHitArm64 NodeHit flow entirely, but
//     the leading LDR is already merged into the SELF entry and not repeated
//
// **deopt path**: when the Run side sees x0==deoptCode it calls host.GetTable
// byte-equal to P1 (R(A+1)=R(B) already stored; P1 icGetTable handles NodeHit
// + the __index chain).
//
// **Preconditions** (per 06 §4.2): x26/x27/x14/x0-x3 same as NodeHit.
func EmitSelfNodeHitArm64(buf []byte, aReg, bReg uint8,
	stableShape, stableIndex uint32, stableKey uint64,
	arenaBaseOff uint16, deoptCode uint64) []byte {
	// 1. LDR x0, [x26+B*8] (load R(B) obj)
	buf = EmitLdrXtFromXnDisp(buf, 0, 26, uint16(bReg)*8)

	// 2. **SELF first step**: STR x0, [x26+(A+1)*8] (self/this argument)
	buf = EmitStrXtToXnDisp(buf, 0, 26, uint16(aReg+1)*8)

	// 3-6. IsTable guard (LSR + MOV + CMP + B.NE, 28 bytes; the entry LDR is
	//      already merged into SELF step 1 and not repeated)
	buf = EmitLsrXdImm6(buf, 0, 0, 48)
	buf = EmitMovXdImm64(buf, 1, qNanBoxTableTagShiftedArm64)
	buf = EmitCmpXnXm(buf, 0, 1)
	bNeTagOff := len(buf)
	buf = EmitBCond(buf, CondNE, 0)

	// 7-12. re-load + GCRef extract + arena base + ADD x2 SIB (36 bytes)
	buf = EmitLdrXtFromXnDisp(buf, 0, 26, uint16(bReg)*8)
	buf = EmitMovXdImm64(buf, 1, payloadMaskArm64)
	buf = EmitAndXdXnXm(buf, 0, 0, 1)
	buf = EmitMovXdFromXn(buf, 1, 0)
	buf = EmitLdrXtFromXnDisp(buf, 14, 27, arenaBaseOff)
	buf = EmitAddXdXnXm(buf, 2, 14, 1)

	// 13-17. word5 + LSR + stableShape + CMP + B.NE (32 bytes)
	buf = EmitLdrXtFromXnDisp(buf, 0, 2, 40)
	buf = EmitLsrXdImm6(buf, 0, 0, 32)
	buf = EmitMovXdImm64(buf, 3, uint64(stableShape))
	buf = EmitCmpXnXm(buf, 0, 3)
	bNeShapeOff := len(buf)
	buf = EmitBCond(buf, CondNE, 0)

	// 18-20. **NodeHit branch**: load nodeRef (word3, offset 24) + new SIB base
	buf = EmitLdrXtFromXnDisp(buf, 0, 2, 24)
	buf = EmitMovXdFromXn(buf, 1, 0)
	buf = EmitAddXdXnXm(buf, 2, 14, 1)

	// 21-24. NodeKey load + stableKey comparison + B.NE deopt
	buf = EmitLdrXtFromXnDisp(buf, 0, 2, uint16(stableIndex*24))
	buf = EmitMovXdImm64(buf, 3, stableKey)
	buf = EmitCmpXnXm(buf, 0, 3)
	bNeKeyOff := len(buf)
	buf = EmitBCond(buf, CondNE, 0)

	// 25-28. NodeVal load + nil check + B.EQ deopt
	buf = EmitLdrXtFromXnDisp(buf, 0, 2, uint16(stableIndex*24+8))
	buf = EmitMovXdImm64(buf, 3, qNanBoxNilImmArm64)
	buf = EmitCmpXnXm(buf, 0, 3)
	bEqNilOff := len(buf)
	buf = EmitBCond(buf, CondEQ, 0)

	// 29-30. store R(A) = method + RET
	buf = EmitStrXtToXnDisp(buf, 0, 26, uint16(aReg)*8)
	buf = EmitRet(buf)

	// 31. deopt block
	deoptStart := len(buf)
	buf = EmitMovXdImm64(buf, 0, deoptCode)
	buf = EmitRet(buf)

	// 32. patch B.cond imm19 = (deoptStart - this B.cond itself) / 4
	patchBCondImm19(buf, bNeTagOff, int32(deoptStart-bNeTagOff)/4)
	patchBCondImm19(buf, bNeShapeOff, int32(deoptStart-bNeShapeOff)/4)
	patchBCondImm19(buf, bNeKeyOff, int32(deoptStart-bNeKeyOff)/4)
	patchBCondImm19(buf, bEqNilOff, int32(deoptStart-bEqNilOff)/4)

	return buf
}

// EncodedSelfNodeHitArm64Len is the byte count of the arm64 PJ4 SELF NodeHit
// template (200).
const EncodedSelfNodeHitArm64Len = 200

// EmitSelfNodeHitNoRetArm64 is the same as EmitSelfNodeHitArm64, except the
// **success path does not emit ret** — it falls through to the subsequent
// segment emitted by the caller (the arm64 mirror of §9.20.9 Spike 1's
// useFrameInline path, same form as amd64 EmitSelfNodeHitNoRet).
//
// Key differences vs EmitSelfNodeHitArm64 (success path):
//   - step 30 does not RET; it emits B instead (jumping past the deopt block
//     to the segment's tail, falling through to the caller)
//   - the deopt block is unchanged (writes x0=deoptCode + RET)
//
// Total byte count: 200 (same as NodeHit) — the RET (4B) is replaced by a B
// (4B), so the length is identical.
//
// **deopt path**: when the Run side sees x0==deoptCode it calls host.GetTable
// byte-equal to P1.
//
// **Wiring path**: under the archEmitFrameInlineExitHelperRequest +
// archCallJITSpec form, after the SELF segment succeeds it falls through to
// BuildVoid0Arg + ExitHelperRequest + PopVoid0Arg + ret; enabled once
// archSupportsFrameInline is flipped to true (per the C5 commit's
// placeholder-backfill lesson: archEmitSelfNodeHitNoRet used to be a panic
// placeholder → now a real implementation, to prevent the NoRet path's panic
// from killing the whole useFrameInline path if triggered by mistake).
func EmitSelfNodeHitNoRetArm64(buf []byte, aReg, bReg uint8,
	stableShape, stableIndex uint32, stableKey uint64,
	arenaBaseOff uint16, deoptCode uint64) []byte {
	// 1. LDR x0, [x26+B*8] (load R(B) obj)
	buf = EmitLdrXtFromXnDisp(buf, 0, 26, uint16(bReg)*8)

	// 2. **SELF first step**: STR x0, [x26+(A+1)*8] (self/this argument)
	buf = EmitStrXtToXnDisp(buf, 0, 26, uint16(aReg+1)*8)

	// 3-6. IsTable guard (LSR + MOV + CMP + B.NE, 28 bytes; the entry LDR is
	//      already merged into SELF step 1 and not repeated)
	buf = EmitLsrXdImm6(buf, 0, 0, 48)
	buf = EmitMovXdImm64(buf, 1, qNanBoxTableTagShiftedArm64)
	buf = EmitCmpXnXm(buf, 0, 1)
	bNeTagOff := len(buf)
	buf = EmitBCond(buf, CondNE, 0)

	// 7-12. re-load + GCRef extract + arena base + ADD x2 SIB (36 bytes)
	buf = EmitLdrXtFromXnDisp(buf, 0, 26, uint16(bReg)*8)
	buf = EmitMovXdImm64(buf, 1, payloadMaskArm64)
	buf = EmitAndXdXnXm(buf, 0, 0, 1)
	buf = EmitMovXdFromXn(buf, 1, 0)
	buf = EmitLdrXtFromXnDisp(buf, 14, 27, arenaBaseOff)
	buf = EmitAddXdXnXm(buf, 2, 14, 1)

	// 13-17. word5 + LSR + stableShape + CMP + B.NE (32 bytes)
	buf = EmitLdrXtFromXnDisp(buf, 0, 2, 40)
	buf = EmitLsrXdImm6(buf, 0, 0, 32)
	buf = EmitMovXdImm64(buf, 3, uint64(stableShape))
	buf = EmitCmpXnXm(buf, 0, 3)
	bNeShapeOff := len(buf)
	buf = EmitBCond(buf, CondNE, 0)

	// 18-20. **NodeHit branch**: load nodeRef (word3, offset 24) + new SIB base
	buf = EmitLdrXtFromXnDisp(buf, 0, 2, 24)
	buf = EmitMovXdFromXn(buf, 1, 0)
	buf = EmitAddXdXnXm(buf, 2, 14, 1)

	// 21-24. NodeKey load + stableKey comparison + B.NE deopt
	buf = EmitLdrXtFromXnDisp(buf, 0, 2, uint16(stableIndex*24))
	buf = EmitMovXdImm64(buf, 3, stableKey)
	buf = EmitCmpXnXm(buf, 0, 3)
	bNeKeyOff := len(buf)
	buf = EmitBCond(buf, CondNE, 0)

	// 25-28. NodeVal load + nil check + B.EQ deopt
	buf = EmitLdrXtFromXnDisp(buf, 0, 2, uint16(stableIndex*24+8))
	buf = EmitMovXdImm64(buf, 3, qNanBoxNilImmArm64)
	buf = EmitCmpXnXm(buf, 0, 3)
	bEqNilOff := len(buf)
	buf = EmitBCond(buf, CondEQ, 0)

	// 29. store R(A) = method
	buf = EmitStrXtToXnDisp(buf, 0, 26, uint16(aReg)*8)

	// 30. **NO RET** — emit B instead (forward, jumping past the deopt block to
	//     the segment tail, falling through to the caller-emitted BuildVoid0Arg
	//     segment; same technique as amd64 EmitJmpRel32)
	bSuccessOff := len(buf)
	buf = EmitB(buf, 0) // placeholder, patched afterwards

	// 31. deopt block (writes x0=deoptCode + RET, 12 bytes = MovXdImm64 8 + Ret 4)
	deoptStart := len(buf)
	buf = EmitMovXdImm64(buf, 0, deoptCode)
	buf = EmitRet(buf)

	// 32. patch B.cond imm19 = (deoptStart - this B.cond itself) / 4
	patchBCondImm19(buf, bNeTagOff, int32(deoptStart-bNeTagOff)/4)
	patchBCondImm19(buf, bNeShapeOff, int32(deoptStart-bNeShapeOff)/4)
	patchBCondImm19(buf, bNeKeyOff, int32(deoptStart-bNeKeyOff)/4)
	patchBCondImm19(buf, bEqNilOff, int32(deoptStart-bEqNilOff)/4)

	// 33. patch success B imm26 = (len(buf) - bSuccessOff) / 4
	//     jump to just past the deopt block (i.e. the segment tail, the start
	//     of the subsequent BuildVoid0Arg segment)
	patchBImm26(buf, bSuccessOff, int32(len(buf)-bSuccessOff)/4)

	return buf
}

// EncodedSelfNodeHitNoRetArm64Len is the byte count of the arm64 PJ4 SELF
// NodeHit NoRet variant (200, same as NodeHit; RET 4B swapped for B 4B).
const EncodedSelfNodeHitNoRetArm64Len = 200

// EmitSpecArgLoadKArm64 writes R(dstReg) = K (a NaN-box u64) — byte-level
// inline loading of args/recv for the PJ5 SELF spec template (the arm64
// counterpart of amd64 EmitSpecArgLoadK).
//
// **vsBase register**: the arm64 spec template uses x26 (per
// trampoline_arm64.s::callJITSpec).
//
// Byte sequence (movz/movk×4 + str = 5 instructions = 20 bytes; K is loaded
// into x0 then stored):
//
//	movz/movk x0, K_imm64  ; 4 instructions (16 bits each)
//	str x0, [x26 + dstReg*8] ; 1 instruction
func EmitSpecArgLoadKArm64(buf []byte, dstReg uint8, k uint64) []byte {
	// use x0 as a scratch temporary register (the spec template does not hold
	// a long-lived x0)
	buf = EmitMovXdImm64(buf, 0, k)
	// vsBase is in x26 → STR x0, [x26 + dstReg*8]
	buf = EmitStrXtToXnDisp(buf, 0, 26, uint16(dstReg)*8)
	return buf
}

// EmitSpecArgLoadRegArm64 writes R(dstReg) = R(srcReg).
//
// Byte sequence (LDR + STR = 2 instructions = 8 bytes):
//
//	ldr x0, [x26 + srcReg*8]
//	str x0, [x26 + dstReg*8]
func EmitSpecArgLoadRegArm64(buf []byte, dstReg uint8, srcReg uint8) []byte {
	buf = EmitLdrXtFromXnDisp(buf, 0, 26, uint16(srcReg)*8)
	buf = EmitStrXtToXnDisp(buf, 0, 26, uint16(dstReg)*8)
	return buf
}

// EmitFrameInlineCIDepthIncArm64 emits the arm64 ciDepth++ byte-level inline
// template (per §9.20 Option B Spike 1, the amd64 counterpart).
//
// Unlike amd64, arm64 has no single `inc [mem]` instruction, so it needs three
// steps: LDR + ADD + STR.
//
// Byte sequence (4 instructions = 16 bytes):
//
//	ldr  x16, [x27 + ciDepthAddrOffset]  ; x16 = ciDepthAddr (host byte address)
//	ldr  x17, [x16]                       ; x17 = *ciDepthAddr (current ciDepth)
//	add  x17, x17, #1                     ; x17++
//	str  x17, [x16]                       ; *ciDepthAddr = x17
//
// x16/x17 are the ARMv8 IP0/IP1 scratch registers (intra-procedure-call
// scratch, freely clobberable by the callee); the caller need not save them.
// x27 = jitContext (per 06 §4.2).
//
// +6 bytes vs amd64 (10 bytes): RISC fixed-length requires 3 separate
// instructions + LDR pimm12, vs amd64's `inc [rax]` with compound addressing.
func EmitFrameInlineCIDepthIncArm64(buf []byte, ciDepthAddrOffset uint16) []byte {
	// LDR x16, [x27 + ciDepthAddrOffset]
	buf = EmitLdrXtFromXnDisp(buf, 16, 27, ciDepthAddrOffset)
	// LDR x17, [x16]
	buf = EmitLdrXtFromXnDisp(buf, 17, 16, 0)
	// ADD x17, x17, #1
	buf = EmitAddXdImm12(buf, 17, 17, 1)
	// STR x17, [x16]
	buf = EmitStrXtToXnDisp(buf, 17, 16, 0)
	return buf
}

// EmitFrameInlineCIDepthDecArm64 emits the arm64 ciDepth-- byte-level inline
// template (per §9.20 popCallInfo's reverse, the amd64 counterpart).
//
// Same as Inc: 4 instructions, 16 bytes (LDR + LDR + SUB + STR).
func EmitFrameInlineCIDepthDecArm64(buf []byte, ciDepthAddrOffset uint16) []byte {
	buf = EmitLdrXtFromXnDisp(buf, 16, 27, ciDepthAddrOffset)
	buf = EmitLdrXtFromXnDisp(buf, 17, 16, 0)
	// SUB x17, x17, #1 — switched to the generic EmitSubXdImm12 macro (per the
	// PR #26 external review hygiene correction 2026-06-28: previously wrote
	// the raw bytes 0xD1000631 directly, which was hard to read and
	// inconsistent with the emitter's generic macro system).
	buf = EmitSubXdImm12(buf, 17, 17, 1)
	buf = EmitStrXtToXnDisp(buf, 17, 16, 0)
	return buf
}

// EncodedFrameInlineCIDepthIncDecArm64Len is the byte count of the arm64
// ciDepth++/-- byte-level inline template (16, vs amd64 = 10). arm64 is +6
// bytes because RISC fixed-length requires 3 separate instructions + LDR
// pimm12, vs amd64's compound addressing.
const EncodedFrameInlineCIDepthIncDecArm64Len = 16

// EmitFrameInlineLoadCISlotAddrArm64 emits the arm64 template that loads the
// address of the depth-th frame in the CI segment into X0 (per §9.20 Option B
// Spike 1, the amd64 counterpart).
//
// Byte sequence (7 instructions = 40 bytes, per the PR #26 review fix bd73625):
//
//	ldr  x16, [x27 + ciDepthAddrOffset]    ; 4 bytes: x16 = ciDepthAddr
//	ldr  x17, [x16]                         ; 4 bytes: x17 = depth (current)
//	ldr  x16, [x27 + ciSegBaseAddrOffset]   ; 4 bytes: x16 = ciSegBaseAddr
//	ldr  x16, [x16]                         ; 4 bytes: x16 = ciSegBase
//	mov  x9, #40                            ; 16 bytes (EmitMovXdImm64 movz+movk*3
//	                                        ;           takes 4 segments even for imm=40)
//	mul  x17, x17, x9                       ; 4 bytes: x17 = depth * 40
//	add  x0, x16, x17                       ; 4 bytes: x0 = ciSegBase + depth*40
//	                                        ; total: 4+4+4+4+16+4+4 = 40 bytes
//
// After the template, x0 = the byte address of CallInfo[depth] (equivalent to
// amd64 rax).
//
// **Scratch registers** (per the PR #26 external review correction 2026-06-28):
//   - x16/x17 = IP0/IP1 (intra-procedure-call scratch, AAPCS standard, freely
//     clobberable)
//   - x9 = caller-saved scratch (per AAPCS x0-x18 are caller-saved, x19-x28
//     are callee-saved + x18 is platform reserved; **previously wrongly used
//     x18, an AAPCS violation** — this batch changes it to x9).
//
// **arm64 40 bytes vs amd64 30 bytes** (per the bd73625 review fix): arm64 is
// +10 bytes because EmitMovXdImm64 takes 4 16-bit segments (movz+movk*3) even
// for #40, wasting 12 bytes; the single-instruction 4-byte EmitMovXdImm12
// optimization is deferred to PJ8+ (extended after arm64 physical-runner
// end-to-end acceptance).
func EmitFrameInlineLoadCISlotAddrArm64(buf []byte, ciDepthAddrOffset, ciSegBaseAddrOffset uint16) []byte {
	// 1. x16 = ciDepthAddr → x17 = depth
	buf = EmitLdrXtFromXnDisp(buf, 16, 27, ciDepthAddrOffset)
	buf = EmitLdrXtFromXnDisp(buf, 17, 16, 0)
	// 2. x16 = ciSegBaseAddr → x16 = ciSegBase (overwrites x16)
	buf = EmitLdrXtFromXnDisp(buf, 16, 27, ciSegBaseAddrOffset)
	buf = EmitLdrXtFromXnDisp(buf, 16, 16, 0)
	// 3. mov x9, #40 (scratch; per AAPCS x9 is caller-saved and usable)
	buf = EmitMovXdImm64(buf, 9, 40)
	// Note: EmitMovXdImm64 is 16 bytes, movz+movk*3. We only need a single movz,
	// but EmitMovXdImm64 does not distinguish imm size, so small imms use 4
	// movz/movk and waste 12 bytes.
	// Spike 1 simplification: accept the 16-byte imm load; a later PJ8+
	// optimization will use a single EmitMovzXd.
	// 4. mul x17, x17, x9
	buf = EmitMulXdXnXm(buf, 17, 17, 9)
	// 5. add x0, x16, x17
	buf = EmitAddXdXnXm(buf, 0, 16, 17)
	return buf
}

// EncodedFrameInlineLoadCISlotAddrArm64Len is the byte count of the arm64
// template that loads the address of the depth-th CI-segment frame
// (4*4 + 16 + 4 + 4 = 40 bytes, vs amd64 = 30).
//
// arm64 is +10 bytes vs amd64's 30 bytes because EmitMovXdImm64 (16 bytes,
// movz+movk*3) takes 4 16-bit segments even to load the small constant #40 —
// a future optimization will use a single 4-byte EmitMovzXd (deferred to the
// PJ8+ generic optimization batch).
const EncodedFrameInlineLoadCISlotAddrArm64Len = 40

// EmitFrameInlineLoadCISlotAddrAbsoluteArm64 is the same as
// EmitFrameInlineLoadCISlotAddrArm64 but its result x0 is an **arena absolute
// address** (the arm64 mirror of §9.20.9 commit-5l's fix for the ciSegBase
// mirror-word semantic bug; the counterpart of amd64
// EmitFrameInlineLoadCISlotAddrAbsolute).
//
// **Bug origin** (the P3 PW10 Stage 2 ciSegBase mirror-word protocol):
// `*ciSegBaseAddrPtr` stores `ciBaseW * 8` (a byte offset into the arena), not
// an absolute address. The x0 that LoadCISlotAddrArm64 computes,
// ciBaseW*8 + depth*40, is a byte offset and cannot be dereferenced directly
// (verified on real hardware: darwin/arm64 macos-latest CI showed SIGSEGV at
// spec segment 0x108, addr=ciBaseW*8+depth*40 a small value; per F3-#3b real
// M1 debugging, lldb-verified).
//
// **This function**: after LoadCISlotAddr, it appends
// `ldr x14, [x27+arenaBaseOff]; add x0, x0, x14` (adds the arena base to x0),
// making x0 an absolute address. **Precondition**: the caller has already set
// up jitContext.arenaBase (per SetArenaBase injection); x14 is used as a
// scratch register (arm64 AAPCS x14 is a caller-saved temporary, safe to
// overwrite, in the same slot as amd64 r14).
//
// **Byte sequence**: LoadCISlotAddrArm64 (40B) + `ldr x14, [x27+arenaBaseOff]`
// (4B) + `add x0, x0, x14` (4B) = 48B.
//
// **Usage** (the useFrameInline path):
//  1. call this function: x0 = the absolute address of CI[depth] (48B)
//  2. subsequent WriteCIWordArm64 / CIDepthIncArm64 / LoadClosureGCRefArm64 +
//     WriteCIWordFromXArm64 use it the same way
func EmitFrameInlineLoadCISlotAddrAbsoluteArm64(buf []byte,
	ciDepthAddrOffset, ciSegBaseAddrOffset, arenaBaseOffset uint16) []byte {
	buf = EmitFrameInlineLoadCISlotAddrArm64(buf, ciDepthAddrOffset, ciSegBaseAddrOffset)
	// load x14 = arena base
	buf = EmitLdrXtFromXnDisp(buf, 14, 27, arenaBaseOffset)
	// add x0, x0, x14
	buf = EmitAddXdXnXm(buf, 0, 0, 14)
	return buf
}

// EncodedFrameInlineLoadCISlotAddrAbsoluteArm64Len = 40 + 4 + 4 = 48.
const EncodedFrameInlineLoadCISlotAddrAbsoluteArm64Len = EncodedFrameInlineLoadCISlotAddrArm64Len + 8

// EmitFrameInlineWriteCIWordArm64 emits the arm64 template that writes imm64
// into the word_idx of a CI frame (per §9.20 Option B Spike 1, the amd64
// counterpart).
//
// Calling contract: x0 must already hold the byte address of the
// CallInfo[depth] frame start (per EmitFrameInlineLoadCISlotAddrArm64);
// word_idx range is [0,4] (per ciWords=5).
//
// Byte sequence (16 + 4 = 20 bytes):
//
//	mov  x16, imm64                      ; 16 bytes (EmitMovXdImm64 movz+movk*3)
//	str  x16, [x0 + word_idx*8]          ; 4 bytes (EmitStrXtToXnDisp pimm12)
//
// arm64 20 bytes vs amd64 14 bytes, +6 bytes because EmitMovXdImm64 always
// takes 4 16-bit segments (regardless of imm size); amd64 mov rcx imm64 is a
// single 10-byte instruction.
func EmitFrameInlineWriteCIWordArm64(buf []byte, wordIdx uint8, imm64 uint64) []byte {
	if wordIdx > 4 {
		wordIdx = 0
	}
	buf = EmitMovXdImm64(buf, 16, imm64)
	buf = EmitStrXtToXnDisp(buf, 16, 0, uint16(wordIdx)*8)
	return buf
}

// EncodedFrameInlineWriteCIWordArm64Len = 20.
const EncodedFrameInlineWriteCIWordArm64Len = 20

// FrameInlineCISlotWordsArm64 is the arm64-side 5-word CI-frame argument group
// used by Spike 1 (the counterpart of amd64 FrameInlineCISlotWords; same
// layout, a local type only to avoid a cross-package reference).
type FrameInlineCISlotWordsArm64 struct {
	Word0 uint64
	Word1 uint64
	Word2 uint64
	Word3 uint64
	Word4 uint64
}

// EmitFrameInlineBuildVoid0ArgSkeletonArm64 emits the arm64 Spike 1
// enterLuaFrame byte-level inline skeleton v2 (per §9.20 Option B Spike 1, the
// amd64 counterpart, with word3 switched to loading the runtime closure
// GCRef).
//
// Segment stacking:
//  1. LoadCISlotAddrArm64: x0 = CallInfo[depth] frame start (40 bytes)
//  2. WriteCIWordArm64 (0/1/2): write word0/1/2 imm (20*3 = 60 bytes)
//  3. LoadClosureGCRefArm64 (callARecv): x16 = R(callARecv) GCRef (24 bytes)
//  4. WriteCIWordFromXArm64 (3, 16): word3 = x16 (4 bytes)
//  5. WriteCIWordArm64 (4): write word4 imm (20 bytes)
//  6. CIDepthIncArm64: ciDepth++ (16 bytes)
//
// **Total length**: 40 + 60 + 24 + 4 + 20 + 16 = 164 bytes (vs amd64 v2 = 120;
// arm64 is +44 bytes because of RISC fixed-length + MovXdImm64 16 bytes vs
// amd64 10 bytes).
//
// arm64 register convention: x0 holds the segment address (mirrors amd64 rax)
// / x16/17/18 scratch.
func EmitFrameInlineBuildVoid0ArgSkeletonArm64(buf []byte,
	ciDepthAddrOffset, ciSegBaseAddrOffset uint16,
	callARecv uint8,
	words FrameInlineCISlotWordsArm64) []byte {
	// 1. x0 = CallInfo[depth] frame start
	buf = EmitFrameInlineLoadCISlotAddrArm64(buf, ciDepthAddrOffset, ciSegBaseAddrOffset)
	// 2. write word0/1/2 imm
	buf = EmitFrameInlineWriteCIWordArm64(buf, 0, words.Word0)
	buf = EmitFrameInlineWriteCIWordArm64(buf, 1, words.Word1)
	buf = EmitFrameInlineWriteCIWordArm64(buf, 2, words.Word2)
	// 3. x16 = R(callARecv) GCRef
	buf = EmitFrameInlineLoadClosureGCRefArm64(buf, callARecv)
	// 4. word3 = x16
	buf = EmitFrameInlineWriteCIWordFromXArm64(buf, 3, 16)
	// 5. write word4 imm
	buf = EmitFrameInlineWriteCIWordArm64(buf, 4, words.Word4)
	// 6. ciDepth++
	buf = EmitFrameInlineCIDepthIncArm64(buf, ciDepthAddrOffset)
	return buf
}

// EncodedFrameInlineBuildVoid0ArgSkeletonArm64Len = 40 + 20*3 + 24 + 4 + 20 + 16 = 164.
const EncodedFrameInlineBuildVoid0ArgSkeletonArm64Len = 164

// EmitFrameInlineBuildVoid0ArgSkeletonAbsoluteArm64 is the same as
// EmitFrameInlineBuildVoid0ArgSkeletonArm64 but uses
// LoadCISlotAddrAbsoluteArm64 (x0 = arena absolute address, per §9.20.9
// commit-5l's fix for the ciSegBase mirror-word semantic bug; the counterpart
// of amd64 EmitFrameInlineBuildVoid0ArgSkeletonAbsolute).
//
// **Design difference**: the original BuildVoid0ArgSkeletonArm64's
// LoadCISlotAddrArm64 computes x0 = ciBaseW*8 + depth*40, a byte offset into
// the arena, not an absolute address; the subsequent WriteCIWord's
// `STR x16, [x0]` then SIGSEGVs when executed on real hardware (addr=that
// small byte offset, landing on a low protected page on macOS). This function
// uses the Absolute version, so x0 = absolute address and the subsequent STR
// writes to a valid location.
//
// **Byte sequence** (total length 172 bytes = original 164 + Absolute's extra
// 8 bytes for the arena base load):
//  1. LoadCISlotAddrAbsoluteArm64 (48B, 40 + 4 ldr x14 + 4 add x0,x0,x14)
//  2. WriteCIWordArm64 (0/1/2) imm 3 * 20 = 60B
//  3. LoadClosureGCRefArm64 (callARecv, 24B): x16 = R(callARecv) GCRef payload
//  4. WriteCIWordFromXArm64 (3,16) (4B): CI[depth].word3 = x16
//  5. WriteCIWordArm64 (4) imm (20B)
//  6. CIDepthIncArm64 (16B)
//
// **Total**: 48 + 60 + 24 + 4 + 20 + 16 = 172 bytes (original 164 + 8 for the
// absolute load's extra 8).
func EmitFrameInlineBuildVoid0ArgSkeletonAbsoluteArm64(buf []byte,
	ciDepthAddrOffset, ciSegBaseAddrOffset, arenaBaseOffset uint16,
	callARecv uint8,
	words FrameInlineCISlotWordsArm64) []byte {
	// 1. x0 = CI[depth] absolute address (Absolute version, 48B)
	buf = EmitFrameInlineLoadCISlotAddrAbsoluteArm64(buf, ciDepthAddrOffset, ciSegBaseAddrOffset, arenaBaseOffset)
	// 2. write word0/1/2 imm
	buf = EmitFrameInlineWriteCIWordArm64(buf, 0, words.Word0)
	buf = EmitFrameInlineWriteCIWordArm64(buf, 1, words.Word1)
	buf = EmitFrameInlineWriteCIWordArm64(buf, 2, words.Word2)
	// 3. x16 = R(callARecv) GCRef
	buf = EmitFrameInlineLoadClosureGCRefArm64(buf, callARecv)
	// 4. word3 = x16
	buf = EmitFrameInlineWriteCIWordFromXArm64(buf, 3, 16)
	// 5. write word4 imm
	buf = EmitFrameInlineWriteCIWordArm64(buf, 4, words.Word4)
	// 6. ciDepth++
	buf = EmitFrameInlineCIDepthIncArm64(buf, ciDepthAddrOffset)
	return buf
}

// EncodedFrameInlineBuildVoid0ArgSkeletonAbsoluteArm64Len = 48 + 60 + 24 + 4 + 20 + 16 = 172.
const EncodedFrameInlineBuildVoid0ArgSkeletonAbsoluteArm64Len = EncodedFrameInlineBuildVoid0ArgSkeletonArm64Len + 8

// EmitFrameInlineLoadClosureGCRefArm64 emits the arm64 byte-level template that
// resolves R(srcReg) NaN-box → x16 48-bit GCRef (per §9.20 Option B Spike 1,
// the amd64 counterpart).
//
// Byte sequence (4 + 16 + 4 = 24 bytes):
//
//	ldr  x16, [x26 + srcReg*8]      ; 4 bytes (x26 = vsBase)
//	mov  x17, payloadMask           ; 16 bytes (movz+movk*3)
//	and  x16, x16, x17              ; 4 bytes
//
// arm64 24 bytes vs amd64 20 bytes, +4 bytes because EmitMovXdImm64 is 16 bytes
// vs amd64 mov rdx imm64 10 bytes (a 6-byte delta - 2 bytes saved by some LDR
// forms).
//
// x16/x17 are the IP0/IP1 scratch registers (intra-procedure-call scratch).
func EmitFrameInlineLoadClosureGCRefArm64(buf []byte, srcReg uint8) []byte {
	// LDR x16, [x26 + srcReg*8]
	buf = EmitLdrXtFromXnDisp(buf, 16, 26, uint16(srcReg)*8)
	// MOV x17, payloadMask
	buf = EmitMovXdImm64(buf, 17, 0x0000_FFFF_FFFF_FFFF)
	// AND x16, x16, x17
	buf = EmitAndXdXnXm(buf, 16, 16, 17)
	return buf
}

// EncodedFrameInlineLoadClosureGCRefArm64Len = 4+16+4 = 24.
const EncodedFrameInlineLoadClosureGCRefArm64Len = 24

// EmitFrameInlineWriteCIWordFromXArm64 emits the arm64 "STR Xt, [x0 + wordIdx*8]"
// in 4 bytes (the counterpart of amd64 EmitFrameInlineWriteCIWordFromRcx).
//
// Xt is specified by the caller (typically x16 = the loaded GCRef payload).
//
// Encoding: STR Xt, [Xn, #pimm12]: 0xF9000000 base + (pimm12<<10) + (Xn=0<<5) + Xt
//   - pimm12 = wordIdx (byteOff / 8)
//   - Xn = 0 (x0 holds the CallInfo[depth] frame start)
func EmitFrameInlineWriteCIWordFromXArm64(buf []byte, wordIdx uint8, srcReg uint8) []byte {
	if wordIdx > 4 {
		wordIdx = 0
	}
	return EmitStrXtToXnDisp(buf, srcReg, 0, uint16(wordIdx)*8)
}

// EncodedFrameInlineWriteCIWordFromXArm64Len = 4.
const EncodedFrameInlineWriteCIWordFromXArm64Len = 4

// EmitFrameInlinePopVoid0ArgSkeletonArm64 emits the arm64 Spike 1 popCallInfo
// byte-level inline skeleton (the counterpart of amd64
// EmitFrameInlinePopVoid0ArgSkeleton, including the `xor eax,eax + ret` tail).
//
// **Byte sequence** (24 bytes = CIDepthDecArm64 16 + movz w0 #0 4 + ret 4):
//  1. CIDepthDecArm64: ciDepth-- (16B)
//  2. movz w0, #0: x0 = 0 = ExitNormal (4B, clears the high 32 bits = 8B
//     uint64 = 0)
//  3. ret: exit the mmap segment back to the trampoline (4B)
//
// **Same missing-ret bug fix as §9.20.9 commit-5l** (the arm64 mirror of the
// same amd64 commit, F3-#3b: the original arm64 Pop segment did not emit
// movz/ret at its tail, and the segment was followed by an mmap zero-byte
// region → real hardware darwin/arm64 SIGILL at PC=0x0...0, because after the
// resume entry jumped in, the later segment did not return normally but kept
// executing into the zero-byte region). The amd64 side was fixed in commit-5l
// (2-byte xor + 1-byte ret); the arm64 side is fixed together in F3-#3b.
func EmitFrameInlinePopVoid0ArgSkeletonArm64(buf []byte, ciDepthAddrOffset uint16) []byte {
	buf = EmitFrameInlineCIDepthDecArm64(buf, ciDepthAddrOffset)
	// movz w0, #0: x0 = 0 = ExitNormal (counterpart of §9.20.9 commit-5l xor eax,eax)
	buf = EmitMovzWdImm16(buf, 0, 0)
	// ret: exit the mmap segment (counterpart of commit-5l c3 ret)
	buf = EmitRet(buf)
	return buf
}

// EncodedFrameInlinePopVoid0ArgSkeletonArm64Len = 16 + 4 + 4 = 24 (vs amd64
// = CIDepthDec 10 + xor+ret 3 = 13; arm64 is larger because movz w0 4B vs amd64
// xor 2B + ret 4B vs amd64 ret 1B).
const EncodedFrameInlinePopVoid0ArgSkeletonArm64Len = EncodedFrameInlineCIDepthIncDecArm64Len + 8

// EmitFrameInlineExitHelperRequestArm64 emits the arm64 Spike 1 trampoline
// exit-resume protocol's exit-helper-request segment (the arm64 mirror of the
// 24-byte amd64 EmitFrameInlineExitHelperRequest).
//
// **Protocol position** (same as amd64 + tmp/wangshu-p4-todo.md §2.4): after
// BuildVoid0Arg, before PopVoid0Arg, exiting the mmap segment back to the
// trampoline. The trampoline asm checks X0 = ExitInlineHelper (3) to route to
// the Go dispatcher.
//
// **Byte sequence** (arm64, total 36 bytes):
//
//	; load helperCode into x16 (IP0 scratch)
//	movz/movk x16, helperCode imm64   ; 16 bytes (movz+movk×3)
//	; write jitCtx.exitArg0 = helperCode
//	str x16, [x27 + exitArg0Off]      ; 4 bytes (64-bit STR)
//	; load ExitInlineHelper=3 into w16 (a single movz suffices, imm16 = 3)
//	movz w16, #3                       ; 4 bytes (32-bit MOVZ)
//	; write jitCtx.exitReasonCode = 3 (32-bit field)
//	str w16, [x27 + exitReasonOff]    ; 4 bytes (32-bit STR)
//	; set return value x0 = 3 (the trampoline checks X0 to route to the dispatcher)
//	movz w0, #3                        ; 4 bytes (32-bit MOVZ, clears high bits)
//	; ret
//	ret                                ; 4 bytes
//	; total: 16 + 4 + 4 + 4 + 4 + 4 = 36 bytes
//
// **12-byte delta vs amd64 24 bytes**:
//   - arm64 MOV imm64 sequence is 16 bytes (movz+movk×3) vs amd64 mov rax imm64 10 bytes
//   - arm64 movz Wd imm16 4 bytes vs amd64 mov eax imm32 5 bytes (saves 1)
//   - arm64 RET 4 bytes vs amd64 ret 1 byte (+3)
//   - arm64 must explicitly movz w0, #3 (no fall-through register reuse) vs
//     amd64 reusing eax
//   - total delta +12 bytes (arm64 fixed-length 4-byte encoding + no register reuse)
//
// **Parameters** (same as amd64):
//   - exitReasonOff: the offset of the jitContext.exitReasonCode field (uint32, 4-byte aligned)
//   - exitArg0Off: the offset of the jitContext.exitArg0 field (uint64, 8-byte aligned)
//   - helperCode: the helper request code (HelperRunCallee=1, etc.)
//
// **Preconditions** (per 06 §4.2 arm64 trampoline protocol):
//   - x27 = jitContext (loaded by callJITSpec, callee-saved)
//   - x16/x17 = IP0/IP1 scratch (intra-procedure-call scratch)
//
// **Enabled after archSupportsFrameInline is flipped to true** (per C7, per
// tmp/wangshu-p4-todo.md §2.4 flipping the switch will expose the panic
// placeholder left by commit-5n): this commit replaces the panic placeholder
// with a real implementation; wiring the Compile/Run sides together + physical
// runner end-to-end verification is deferred to C7.
func EmitFrameInlineExitHelperRequestArm64(buf []byte, exitReasonOff, exitArg0Off int32, helperCode uint64) []byte {
	// 1. movz/movk x16, helperCode imm64 (16 bytes)
	buf = EmitMovXdImm64(buf, 16, helperCode)

	// 2. str x16, [x27 + exitArg0Off] (4 bytes, 64-bit STR; offset must be 8-aligned ≤ 32760)
	if exitArg0Off < 0 || exitArg0Off > 32760 || exitArg0Off%8 != 0 {
		// fallback: on out-of-range offset, silently → byteOff=0; in the
		// production path the jitContext field offsets are within tens of
		// bytes, so this is never reached (same defensive discipline as
		// arenaBaseOffArm64).
		exitArg0Off = 0
	}
	buf = EmitStrXtToXnDisp(buf, 16, 27, uint16(exitArg0Off))

	// 3. movz w16, #ExitInlineHelper=3 (4 bytes, 32-bit MOVZ, imm16=3 single instruction suffices)
	buf = EmitMovzWdImm16(buf, 16, 3)

	// 4. str w16, [x27 + exitReasonOff] (4 bytes, 32-bit STR; offset must be 4-aligned ≤ 16380)
	if exitReasonOff < 0 || exitReasonOff > 16380 || exitReasonOff%4 != 0 {
		exitReasonOff = 0
	}
	buf = EmitStrWtToXnDisp(buf, 16, 27, uint16(exitReasonOff))

	// 5. movz w0, #ExitInlineHelper=3 (4 bytes, set return value; the trampoline checks X0)
	buf = EmitMovzWdImm16(buf, 0, 3)

	// 6. ret (4 bytes)
	buf = EmitRet(buf)

	return buf
}

// EncodedFrameInlineExitHelperRequestArm64Len = 16 + 4 + 4 + 4 + 4 + 4 = 36
// (vs amd64 EncodedFrameInlineExitHelperRequestLen = 24; arm64 is +12 bytes
// because of fixed-length encoding + no register reuse).
const EncodedFrameInlineExitHelperRequestArm64Len = 36
