//go:build wangshu_p4 && amd64

// pj4_template.go —— PJ4 表 IC ArrayHit 字节级 inline 直达槽模板(承
// docs/design/p4-method-jit/03-speculation-ic.md §6 stableShape/stable
// Index 直达槽)。
//
// **形态**:`function(t) return t[K] end`(GETTABLE A B C 常量索引)
// IC slot kind=ArrayHit + stableShape/stableIndex 命中时,字节级 inline
// 直达 array 段读跳过哈希。
//
// **字节级流程**(amd64,~125 字节,严密 IsTable guard 版):
//
//	1. load R(B) 到 rax(候选 table NaN-box)
//	2. **严密 IsTable guard**:shr rax,48 + cmp eax,0xFFFC + jne deopt
//	   (4+5+6=15 字节,精确验高 16 位 tag = TagTable)
//	3. 由于 shr 已破坏 rax,需要重新 load R(B) → rax,然后 GCRef extract
//	4. load arena base 到 r14(从 jitContext 经 EmitMovqR14FromR15Disp)
//	5. mov rcx, rax(rcx = GCRef byte offset)
//	6. load table.word5 = [r14+rcx+40] 到 rax → shr 32 → 与 stableShape 比
//	7. load table.arrayRef = [r14+rcx+16] 到 rax
//	8. load array[stableIndex] = [r14+rcx+stableIndex*8] 到 rax
//	9. nil check:cmp rax, qNanBoxNil → je deopt
//	10. store R(A) = rax
//	11. ret
//	12. [deopt:] mov rax, deoptCode; ret
//
// **预设条件**:
//   - rbx = valueStackBase(callJITSpec 装)
//   - r15 = jitContext(callJITSpec 装)
//   - r14 = scratch(模板入口装 arena base)
//
// **deopt 路径**:Run 端 raxSpec==deoptCode 时调 host.GetTable(byte-equal
// P1 解释器,经 IC + 哈希 + __index 元方法链)。
//
// **严密 IsTable guard 实装**(承外部审查 increment-9/10 ICTable guard
// 假阳建议):用 `shr rax, 48 + cmp eax, 0xFFFC + jne deopt` 精确验高 16 位
// = TagTable(0xFFFC)。string(0xFFFB)/ function(0xFFFD)/ userdata
// (0xFFFE)/ thread(0xFFFF)所有非 table NaN-box 都立即触发 deopt,不再
// fall through 到 gen check(原简化版用 `rax < 0xFFFC<<48` 单边 jb,
// 对 function/userdata/thread 高 tag 假阳,后续 gen check 几乎必触发 deopt
// 但**多走一段 mmap 段指令**;严密版直接 IsTable 失败立即 deopt 省指令)。

package amd64

// qNanBoxTableTagShifted 是 table tag NaN-box 高位:0xFFFC << 48 = 0xFFFC_0000_0000_0000
const qNanBoxTableTagShifted uint64 = 0xFFFC_0000_0000_0000

// qNanBoxNilImm 是 Nil 的 NaN-box raw bits(value.Nil = 0xFFFE_0000_0000_0000)
// 承 internal/value/value.go::Nil。
const qNanBoxNilImm uint64 = 0xFFFE_0000_0000_0000

// qNanBoxTableTagHigh16 是 TagTable 在 NaN-box 高 16 位的纯值(0xFFFC),
// 严密 IsTable guard 字节级 `cmp eax, 0xFFFC` 的立即数。
const qNanBoxTableTagHigh16 int32 = 0xFFFC

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
// **字节布局**(严密 IsTable guard 版,~125 字节):
//
//	[ 0] mov rax, [rbx + bReg*8]                     ; 7 (load R(B))
//	[ 7] shr rax, 48                                 ; 4 (提取高 16 位 tag)
//	[11] cmp eax, 0xFFFC                             ; 5 (TagTable 精确比较)
//	[16] jne deopt                                   ; 6 (非 table → deopt)
//	     ; shr 已破坏 rax,需重新 load R(B)
//	[22] mov rax, [rbx + bReg*8]                     ; 7 (re-load R(B))
//	[29] mov rcx, 0x0000_FFFF_FFFF_FFFF              ; 10 (GCRef payload mask)
//	[39] and rax, rcx                                ; 3 (extract GCRef)
//	[42] mov rcx, rax                                ; 3 (rcx = GCRef offset)
//	[45] mov r14, [r15+arenaBaseOff]                 ; 7 (load arena base)
//	[52] mov rax, [r14+rcx+40]                       ; 8 (table.word5 → rax)
//	[60] shr rax, 32                                 ; 4 (gen 在高 32 位)
//	[64] cmp eax, stableShape                        ; 5 (gen 比较)
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
//	——— 总计 132 字节(原简化版 129 字节;严密版 +3 字节因 re-load R(B))———
//
// **严密 IsTable guard**:`shr rax,48 + cmp eax,0xFFFC + jne deopt`
// (15 字节)精确验高 16 位 = TagTable(0xFFFC)。string(0xFFFB)/
// function(0xFFFD)/ userdata(0xFFFE)/ thread(0xFFFF)所有非 table
// NaN-box 立即触发 deopt——不再像简化版那样 fall through 到 gen check
// (假阳后再 deopt,但多走一段 mmap 段指令)。
func EmitGetTableArrayHit(buf []byte, aReg, bReg uint8, stableShape, stableIndex uint32,
	arenaBaseOff int32, deoptCode uint64) []byte {
	// 1. load R(B) → rax
	buf = EmitMovqRaxFromMemReg(buf, 3 /*rbx*/, int32(bReg)*8)

	// 2. **严密 IsTable guard**:shr rax,48 + cmp eax,0xFFFC + jne deopt
	//    (15 字节)精确验高 16 位 tag = TagTable,排除所有非 table NaN-box。
	buf = EmitShrRaxImm8(buf, 48)
	buf = EmitCmpEaxImm32(buf, qNanBoxTableTagHigh16)
	buf = EmitJneRel32(buf, 0) // placeholder rel32 → patch to deopt
	jneTagOff := len(buf) - 4

	// 3. shr 已破坏 rax,重新 load R(B) → rax
	buf = EmitMovqRaxFromMemReg(buf, 3 /*rbx*/, int32(bReg)*8)

	// 4. GCRef extract:and rax, payload_mask(经 rcx)
	const payloadMask uint64 = 0x0000_FFFF_FFFF_FFFF
	buf = EmitMovRcxImm64(buf, payloadMask)
	// and rax, rcx:48 21 C8(REX.W + 21 + ModRM C8 = mod11 reg=001(rcx) rm=000(rax))
	buf = append(buf, 0x48, 0x21, 0xC8)

	// 5. mov rcx, rax(rcx = GCRef byte offset)
	buf = EmitMovqRcxFromRax(buf)

	// 6. load arena base → r14
	buf = EmitMovqR14FromR15Disp(buf, arenaBaseOff)

	// 7. load table.word5 = [r14+rcx+40] → rax
	buf = EmitMovqRaxFromR14PlusRcxDisp(buf, 40)

	// 8. shr rax, 32(gen 在高 32 位)
	//    shr rax, imm8 = 48 C1 E8 imm8(/5 = SHR,rm=000=rax)
	buf = append(buf, 0x48, 0xC1, 0xE8, 32)

	// 9. cmp eax, stableShape(32-bit cmp)
	//    cmp eax, imm32 = 3D imm32(无 ModRM,5 字节,EAX 隐式)
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

	// 12. mov rcx, rax(rcx = arrayRef offset)
	buf = EmitMovqRcxFromRax(buf)

	// 13. load array[stableIndex] = [r14+rcx+stableIndex*8] → rax
	buf = EmitMovqRaxFromR14PlusRcxDisp(buf, int32(stableIndex)*8)

	// 14. nil check:cmp rax, qNanBoxNil + je deopt
	buf = EmitMovRcxImm64(buf, qNanBoxNilImm)
	buf = EmitCmpRaxRcx(buf)
	buf = EmitJeRel32(buf, 0)
	jeNilOff := len(buf) - 4

	// 15. store R(A) = rax
	buf = EmitMovqMemRegFromRax(buf, 3 /*rbx*/, int32(aReg)*8)

	// 16. ret(normal exit)
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

// EmitGetTableNodeHit 拼接 IC NodeHit 直达槽模板字节级序列(承
// docs/design/p4-method-jit/03-speculation-ic.md §6 NodeHit)。
//
// **形态**:`function(t) return t[K] end`(GETTABLE A B C 常量索引)
// IC slot kind=NodeHit + stableShape/stableIndex 命中时,字节级 inline
// 直达 node 段读跳过哈希。NodeHit 比 ArrayHit 多一次 key 比对(NodeKey
// vs stableKey)。
//
// 参数:
//   - aReg:目标 R(A) 寄存器号
//   - bReg:表 R(B) 寄存器号
//   - stableShape:编译期固化的 table.gen 快照
//   - stableIndex:编译期固化的 node slot 下标
//   - stableKey:编译期固化的 key NaN-box(string ref / number bits 等)
//   - arenaBaseOff:jitContext.arenaBase 字段偏移(int32 byte)
//   - deoptCode:guard 失败时返 deoptCode
//
// 返回追加后的 buf。
//
// **字节布局**(严密 IsTable guard + key 比对版,~159 字节):
//
//	[ 0-6 ] mov rax, [rbx + bReg*8]                ; 7 (load R(B))
//	[ 7-10] shr rax, 48                            ; 4
//	[11-15] cmp eax, 0xFFFC                        ; 5 (严密 IsTable)
//	[16-21] jne deopt                              ; 6
//	[22-28] mov rax, [rbx + bReg*8]                ; 7 (re-load R(B))
//	[29-38] mov rcx, payloadMask                   ; 10
//	[39-41] and rax, rcx                           ; 3 (GCRef extract)
//	[42-44] mov rcx, rax                           ; 3 (rcx = GCRef offset)
//	[45-51] mov r14, [r15+arenaBaseOff]            ; 7
//	[52-59] mov rax, [r14+rcx+40]                  ; 8 (table.word5)
//	[60-63] shr rax, 32                            ; 4 (gen 在高 32 位)
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
//	——— 总计 ~159 字节(ArrayHit 132 字节 + key 比对 27 字节)———
//
// **NodeHit vs ArrayHit 差异**:
//   - 取 word3=nodeRef(offset 24)而非 word2=arrayRef(offset 16)
//   - node[idx] 步长 24 字节(nodeWords=3)而非 array[idx] 8 字节
//   - 多 key 比对(NodeKey == stableKey 验证防键退化 / __index 链)
//   - 模板长度 +27 字节(key load + mov rdx + cmp + jne 共 27 字节)
//
// **stableKey 编译期固化**:
//   - 数字键:value.NumberValue(K) raw bits(IEEE 754 NaN-box)
//   - 字符串键:value.MakeGC(TagString, ref) NaN-box,ref 编译期已 intern 不变
//   - 用 NaN-box 整体比较等价于 keyEqual(承 ic.go::keyEqual 同源)
//
// **deopt 路径**:Run 端 raxSpec==deoptCode 时调 host.GetTable(byte-equal
// P1 解释器,经 IC + 哈希 + __index 元方法链)。
func EmitGetTableNodeHit(buf []byte, aReg, bReg uint8,
	stableShape, stableIndex uint32, stableKey uint64,
	arenaBaseOff int32, deoptCode uint64) []byte {
	// 1. load R(B) → rax
	buf = EmitMovqRaxFromMemReg(buf, 3 /*rbx*/, int32(bReg)*8)

	// 2. 严密 IsTable guard:shr rax,48 + cmp eax,0xFFFC + jne deopt
	buf = EmitShrRaxImm8(buf, 48)
	buf = EmitCmpEaxImm32(buf, qNanBoxTableTagHigh16)
	buf = EmitJneRel32(buf, 0)
	jneTagOff := len(buf) - 4

	// 3. shr 已破坏 rax,重新 load R(B) → rax
	buf = EmitMovqRaxFromMemReg(buf, 3 /*rbx*/, int32(bReg)*8)

	// 4. GCRef extract:and rax, payload_mask(经 rcx)
	const payloadMask uint64 = 0x0000_FFFF_FFFF_FFFF
	buf = EmitMovRcxImm64(buf, payloadMask)
	buf = append(buf, 0x48, 0x21, 0xC8) // and rax, rcx

	// 5. mov rcx, rax(rcx = GCRef byte offset)
	buf = EmitMovqRcxFromRax(buf)

	// 6. load arena base → r14
	buf = EmitMovqR14FromR15Disp(buf, arenaBaseOff)

	// 7. load table.word5 = [r14+rcx+40] → rax
	buf = EmitMovqRaxFromR14PlusRcxDisp(buf, 40)

	// 8. shr rax, 32(gen 在高 32 位)
	buf = append(buf, 0x48, 0xC1, 0xE8, 32)

	// 9. cmp eax, stableShape(32-bit cmp)
	buf = append(buf, 0x3D)
	buf = append(buf,
		byte(stableShape),
		byte(stableShape>>8),
		byte(stableShape>>16),
		byte(stableShape>>24))

	// 10. jne deopt
	buf = EmitJneRel32(buf, 0)
	jneShapeOff := len(buf) - 4

	// 11. **NodeHit 分流**:load table.nodeRef = [r14+rcx+24] → rax
	//     (word3=tableNodeIdx=3,3*8=24 字节;ArrayHit 用 word2=16 即 arrayRef)
	buf = EmitMovqRaxFromR14PlusRcxDisp(buf, 24)

	// 12. mov rcx, rax(rcx = nodeRef offset)
	buf = EmitMovqRcxFromRax(buf)

	// 13. load NodeKey = [r14+rcx+stableIndex*24] → rax
	//     (node[idx] 步长 24 字节,nodeWords=3,word0=key/word1=val/word2=next)
	buf = EmitMovqRaxFromR14PlusRcxDisp(buf, int32(stableIndex)*24)

	// 14. mov rdx, stableKey(编译期固化 key NaN-box)
	buf = EmitMovRdxImm64(buf, stableKey)

	// 15. cmp rax, rdx + jne deopt(NodeKey != stableKey → deopt)
	buf = EmitCmpRaxRdx(buf)
	buf = EmitJneRel32(buf, 0)
	jneKeyOff := len(buf) - 4

	// 16. load NodeVal = [r14+rcx+stableIndex*24+8] → rax
	buf = EmitMovqRaxFromR14PlusRcxDisp(buf, int32(stableIndex)*24+8)

	// 17. nil check:cmp rax, qNanBoxNil + je deopt
	buf = EmitMovRcxImm64(buf, qNanBoxNilImm)
	buf = EmitCmpRaxRcx(buf)
	buf = EmitJeRel32(buf, 0)
	jeNilOff := len(buf) - 4

	// 18. store R(A) = rax
	buf = EmitMovqMemRegFromRax(buf, 3 /*rbx*/, int32(aReg)*8)

	// 19. ret(normal exit)
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
