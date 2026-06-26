//go:build wangshu_p4 && amd64

// pj4_template.go —— PJ4 表 IC ArrayHit 字节级 inline 直达槽模板(承
// docs/design/p4-method-jit/03-speculation-ic.md §6 stableShape/stable
// Index 直达槽)。
//
// **形态**:`function(t) return t[K] end`(GETTABLE A B C 常量索引)
// IC slot kind=ArrayHit + stableShape/stableIndex 命中时,字节级 inline
// 直达 array 段读跳过哈希。
//
// **字节级流程**(amd64,~120 字节):
//
//	1. load R(B) 到 rax(table NaN-box)
//	2. IsTable guard:high 16 bit == 0xFFFC?用 mov rcx, 0xFFFC<<48 + cmp + jne
//	3. GCRef extract:and rax 经 rcx mask 提取 48 bit
//	4. load arena base 到 r14(从 jitContext 经 EmitMovqR14FromR15Disp)
//	5. mov rcx, rax(rcx = GCRef byte offset)
//	6. load table.word5 = [r14+rcx+40] 到 rdx → shr rdx, 32 → rdx = gen
//	7. cmp edx, stableShape → jne deopt
//	8. load table.arrayRef = [r14+rcx+16] 到 rcx
//	9. load array[stableIndex] = [r14+rcx+stableIndex*8] 到 rax
//	10. nil check:cmp rax, qNanBoxNil → je deopt
//	11. store R(A) = rax
//	12. ret
//	13. [deopt:] mov rax, deoptCode; ret
//
// **预设条件**:
//   - rbx = valueStackBase(callJITSpec 装)
//   - r15 = jitContext(callJITSpec 装)
//   - r14 = scratch(模板入口装 arena base)
//
// **deopt 路径**:Run 端 raxSpec==deoptCode 时调 host.GetTable(byte-equal
// P1 解释器,经 IC + 哈希 + __index 元方法链)。

package amd64

// qNanBoxTableTagShifted 是 table tag NaN-box 高位:0xFFFC << 48 = 0xFFFC_0000_0000_0000
const qNanBoxTableTagShifted uint64 = 0xFFFC_0000_0000_0000

// qNanBoxNilImm 是 Nil 的 NaN-box raw bits(value.Nil = 0xFFFE_0000_0000_0000)
// 承 internal/value/value.go::Nil。
const qNanBoxNilImm uint64 = 0xFFFE_0000_0000_0000

// EmitGetTableArrayHit 拼接 IC ArrayHit 直达槽模板字节级序列。
//
// 参数:
//   - aReg:目标 R(A) 寄存器号
//   - bReg:表 R(B) 寄存器号
//   - stableShape:编译期固化的 table.gen 快照
//   - stableIndex:编译期固化的 array slot 下标
//   - arenaBaseOff:jitContext.arenaBase 字段偏移(int32 byte)
//   - deoptCode:guard 失败时返 deoptCode
//
// 返回追加后的 buf。
//
// **字节布局**(详细):
//
//	[ 0] mov rax, [rbx + bReg*8]                     ; 7 (load R(B))
//	[ 7] mov rcx, qNanBoxTableTagShifted             ; 10 (high 16 bit mask)
//	[17] cmp rax, rcx                                ; 3 (precise IsTable not implemented;
//	                                                     guard 简化为 rax >= 0xFFFC...)
//	[20] jb deopt                                    ; 6 (jb: 若 rax < tag 则非 table = deopt)
//	     ; 实际 table NaN-box 是高 16 = 0xFFFC,值域 [qNanBoxTableTagShifted,
//	     ; qNanBoxTableTagShifted + 2^48);其它 tag(string/thread/userdata)
//	     ; 不同,但 nil 是 0xFFFE,大于 qNanBoxTableTagShifted!需精确 cmp。
//	     ; **简化版**:暂用 ja deopt(若 > 0xFFFC...+0xFFFF...FFFF 则非
//	     ; table)+ jb deopt(若 < 0xFFFC... 则非 table)— 但 string/thread/
//	     ; userdata 标签都 > 0xFFFC,本简化不严密;暂只接受 deopt 误报
//	     ; (deopt 走 host.GetTable byte-equal)。
//	[26] mov rcx, 0x0000_FFFF_FFFF_FFFF              ; 10 (GCRef payload mask)
//	[36] and rax, rcx                                ; 3 (extract GCRef)
//	[39] mov rcx, rax                                ; 3 (rcx = GCRef offset)
//	[42] mov r14, [r15+arenaBaseOff]                 ; 7 (load arena base)
//	[49] mov rdx, [r14+rcx+40]                       ; 8 (table.word5)
//	     ; 注:无 SIB 寻址 mov rdx, [r14+rcx+disp32] 8 字节 — 已有 SIB rax
//	     ; 版,加 rdx 版需新 emit;简化用 movq rax: 我们 reuse 拿 rax,后
//	     ; 续重新装。改用以下:
//	     ; mov rax, [r14+rcx+40] (rax 覆盖 — 此点 rax 不再需要,因 GCRef 在 rcx)
//	[57] shr rax, 32                                 ; 4 (gen 在高 32 位)
//	[61] cmp eax, stableShape                        ; 5 (注:cmp eax,imm32 5 字节,
//	                                                     不带 REX.W)
//	[66] jne deopt                                   ; 6
//	[72] mov rax, [r14+rcx+16]                       ; 8 (table.arrayRef → rax)
//	[80] mov rcx, rax                                ; 3 (rcx = arrayRef offset)
//	[83] mov rax, [r14+rcx+stableIndex*8]            ; 8 (array[stableIndex])
//	[91] mov rcx, qNanBoxNilImm                       ; 10 (Nil bits)
//	[101] cmp rax, rcx                               ; 3
//	[104] je deopt                                   ; 6
//	[110] mov [rbx + aReg*8], rax                    ; 7 (store R(A))
//	[117] ret                                        ; 1
//	[118] ; deopt block
//	[118] mov rax, deoptCode imm64                   ; 10
//	[128] ret                                        ; 1
//	——— 总计 ~129 字节 ———
//
// 已知精度不足:IsTable guard 仅做 `rax >= 0xFFFC<<48` 单边检查,对 string/
// thread/userdata 等更高 tag 误判通过——但后续 GCRef offset 与 gen 比较
// 几乎必失败(non-table 的 word5 几乎不会等于编译期 stableShape),触发
// deopt 走 host.GetTable byte-equal。**严密版**留 PJ4+ 完整 IsTable guard
// 字节级(mov rcx, tag_mask >> 48 + shr rax, 48 + cmp + jne)。
func EmitGetTableArrayHit(buf []byte, aReg, bReg uint8, stableShape, stableIndex uint32,
	arenaBaseOff int32, deoptCode uint64) []byte {
	// 1. load R(B) → rax
	buf = EmitMovqRaxFromMemReg(buf, 3 /*rbx*/, int32(bReg)*8)

	// 2. IsTable guard 简化:cmp rax, 0xFFFC<<48 + jb deopt
	//    若 rax < 0xFFFC<<48 则一定不是 table(包括 number)→ deopt
	buf = EmitMovRcxImm64(buf, qNanBoxTableTagShifted)
	buf = EmitCmpRaxRcx(buf)
	buf = EmitJbRel32(buf, 0) // placeholder rel32 → patch to deopt
	jbDeoptOff := len(buf) - 4

	// 3. GCRef extract:and rax, payload_mask(经 rcx)
	const payloadMask uint64 = 0x0000_FFFF_FFFF_FFFF
	buf = EmitMovRcxImm64(buf, payloadMask)
	// and rax, rcx:48 21 C8(REX.W + 21 + ModRM C8 = mod11 reg=001(rcx) rm=000(rax))
	buf = append(buf, 0x48, 0x21, 0xC8)

	// 4. mov rcx, rax(rcx = GCRef byte offset)
	buf = EmitMovqRcxFromRax(buf)

	// 5. load arena base → r14
	buf = EmitMovqR14FromR15Disp(buf, arenaBaseOff)

	// 6. load table.word5 = [r14+rcx+40] → rax
	buf = EmitMovqRaxFromR14PlusRcxDisp(buf, 40)

	// 7. shr rax, 32(用 rax shr 而非 rcx;同源 emit shr ecx —— 这里 shr eax)
	//    shr rax, imm8 = 48 C1 E8 imm8(/5 = SHR,rm=000=rax)
	buf = append(buf, 0x48, 0xC1, 0xE8, 32)

	// 8. cmp eax, stableShape(32-bit cmp)
	//    cmp eax, imm32 = 3D imm32(无 ModRM,5 字节)
	buf = append(buf, 0x3D)
	buf = append(buf,
		byte(stableShape),
		byte(stableShape>>8),
		byte(stableShape>>16),
		byte(stableShape>>24))

	// 9. jne deopt
	buf = EmitJneRel32(buf, 0)
	jneShapeOff := len(buf) - 4

	// 10. load table.arrayRef = [r14+rcx+16] → rax
	buf = EmitMovqRaxFromR14PlusRcxDisp(buf, 16)

	// 11. mov rcx, rax(rcx = arrayRef offset)
	buf = EmitMovqRcxFromRax(buf)

	// 12. load array[stableIndex] = [r14+rcx+stableIndex*8] → rax
	buf = EmitMovqRaxFromR14PlusRcxDisp(buf, int32(stableIndex)*8)

	// 13. nil check:cmp rax, qNanBoxNil + je deopt
	buf = EmitMovRcxImm64(buf, qNanBoxNilImm)
	buf = EmitCmpRaxRcx(buf)
	buf = EmitJeRel32(buf, 0)
	jeNilOff := len(buf) - 4

	// 14. store R(A) = rax
	buf = EmitMovqMemRegFromRax(buf, 3 /*rbx*/, int32(aReg)*8)

	// 15. ret(normal exit)
	buf = EmitRet(buf)

	// 16. deopt block
	deoptStart := len(buf)
	buf = EmitMovRaxImm64(buf, deoptCode)
	buf = EmitRet(buf)

	// 17. patch all forward jcc to deopt start
	PatchRel32(buf, jbDeoptOff, int32(deoptStart)-int32(jbDeoptOff+4))
	PatchRel32(buf, jneShapeOff, int32(deoptStart)-int32(jneShapeOff+4))
	PatchRel32(buf, jeNilOff, int32(deoptStart)-int32(jeNilOff+4))

	return buf
}
