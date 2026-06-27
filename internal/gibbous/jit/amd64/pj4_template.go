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

// EmitSetTableArrayHit 拼接 PJ4 SETTABLE IC ArrayHit 字节级 inline 反向写
// 模板(承 docs/design/p4-method-jit/03-speculation-ic.md §6 SETTABLE)。
//
// **形态**:`function(t, v) t[K] = v end` 中 K 是 array 段命中的数字常量
// (luac 编 SETTABLE A B C 中 A=R(t) / B=K idx >=256 / C=R(v))。IC[0].Kind
// = ArrayHit + Shape/Index 命中时,字节级 inline 反向写 array[stableIndex]。
//
// 参数:
//   - aReg:表寄存器号 R(A)(SETTABLE 的 A,table NaN-box 在此寄存器)
//   - cReg:value 寄存器号 R(C)(value 是 reg,C<256)
//   - stableShape:编译期固化的 table.gen 快照
//   - stableIndex:编译期固化的 array slot 下标
//   - arenaBaseOff:jitContext.arenaBase 字段偏移(int32 byte)
//   - deoptCode:guard 失败时返 deoptCode
//
// 返回追加后的 buf。
//
// **字节布局**(113 字节,getter ArrayHit 132 字节但 setter 省 nil mask/check
// 段 19 字节,再扩 load R(C) value + 反向 store):
//
//	[0-21]   load R(A) → rax + 严密 IsTable guard(22 字节,复用)
//	[22-28]  re-load R(A) → rax(7 字节)
//	[29-44]  GCRef extract + rcx = offset(16 字节)
//	[45-51]  load arena base → r14(7 字节)
//	[52-74]  gen check(load word5 + shr + cmp eax + jne,23 字节)
//	[75-82]  load table.arrayRef = [r14+rcx+16] → rax(8 字节)
//	[83-85]  mov rcx, rax(rcx = arrayRef offset,3 字节)
//	[86-92]  load R(C) → rdx(7 字节,EmitMovqRdxFromMemRbx)
//	[93-100] mov [r14+rcx+stableIndex*8], rdx(8 字节,反向 store)
//	[101]    ret(1 字节)
//	[102-112] deopt block(mov rax deoptCode + ret,11 字节)
//	——— 总计 113 字节 ———
//
// **设计简化**(本批 SETTABLE 工程边界):
//   - **不验现有 array[stableIndex] != nil**(防新键路径)— P1 解释器
//     IC 命中协议本身要求该位非 nil,IC slot 校验(shape 一致 + slot 未
//     失效)已保证;新键路径会让 IC 重新填,本帧若误投机依赖 P1 解释器
//     在键退化场景 bump gen + RequestRefresh
//   - **假设无 __newindex 元表**(meta freeze 假设)— 元方法场景应触发
//     gen change 由 IC 失效路径处理
//
// 严密版(再加 ~13 字节验现有 nil + 13 字节验 __newindex)留 PJ4+。
//
// **deopt 路径**:Run 端 raxSpec==deoptCode 时调 host.SetTable(byte-equal
// P1 解释器,经 IC + 哈希 + __newindex 元方法链)。setter 形态返 RETURN A 1
// (无返回值),Run 端 retB=1 不读 R(A)。
func EmitSetTableArrayHit(buf []byte, aReg, cReg uint8,
	stableShape, stableIndex uint32,
	arenaBaseOff int32, deoptCode uint64) []byte {
	// 1. load R(A) → rax(SETTABLE A 是表 reg)
	buf = EmitMovqRaxFromMemReg(buf, 3 /*rbx*/, int32(aReg)*8)

	// 2. 严密 IsTable guard
	buf = EmitShrRaxImm8(buf, 48)
	buf = EmitCmpEaxImm32(buf, qNanBoxTableTagHigh16)
	buf = EmitJneRel32(buf, 0)
	jneTagOff := len(buf) - 4

	// 3. shr 已破坏 rax,重新 load R(A)
	buf = EmitMovqRaxFromMemReg(buf, 3 /*rbx*/, int32(aReg)*8)

	// 4. GCRef extract
	const payloadMask uint64 = 0x0000_FFFF_FFFF_FFFF
	buf = EmitMovRcxImm64(buf, payloadMask)
	buf = append(buf, 0x48, 0x21, 0xC8) // and rax, rcx

	// 5. mov rcx, rax(rcx = GCRef byte offset)
	buf = EmitMovqRcxFromRax(buf)

	// 6. load arena base → r14
	buf = EmitMovqR14FromR15Disp(buf, arenaBaseOff)

	// 7. load table.word5 = [r14+rcx+40] → rax(gen check)
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

	// 12. mov rcx, rax(rcx = arrayRef offset)
	buf = EmitMovqRcxFromRax(buf)

	// 13. load R(C) value → rdx(7 字节)
	buf = EmitMovqRdxFromMemRbx(buf, int32(cReg)*8)

	// 14. mov [r14+rcx+stableIndex*8], rdx(反向 store)
	buf = EmitMovqMemR14PlusRcxFromRdx(buf, int32(stableIndex)*8)

	// 15. ret(normal exit,SETTABLE 无返回值,retB=1 时 host.DoReturn 不读 R(A))
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

// EmitSelfArrayHit 拼接 PJ4 SELF IC ArrayHit 字节级 inline 模板(承
// docs/design/p4-method-jit/03-speculation-ic.md §6 + SELF opcode 语义)。
//
// **SELF opcode 语义**(承 bytecode/opcode.go::SELF):
//
//	R(A+1) := R(B)
//	R(A)   := R(B)[RK(C)]
//
// 即 `obj:method()` 形态:先把 obj 拷到 R(A+1)(self/this 实参),然后
// R(A) = R(B).method 取 method 函数。后跟 CALL R(A) R(A+1) ... 调用。
//
// IC ArrayHit 命中条件:method key 是数字常量 + array 段命中(罕见但
// 形态有效);更常见是 NodeHit(字符串键 method name)— 本批先做
// ArrayHit 作 SELF 工程基础,NodeHit SELF 留下一 commit。
//
// 参数:
//   - aReg:R(A)(method 结果)寄存器号;R(A+1)=R(B) 由模板写入
//   - bReg:R(B)(obj)寄存器号
//   - stableShape / stableIndex / arenaBaseOff / deoptCode 同 GETTABLE
//
// **字节布局**(139 字节,ArrayHit 132 字节 + R(A+1) 拷段 7 字节):
//
//	[0-6]    load R(B) → rax(7 字节,obj NaN-box)
//	[7-13]   **额外**:store R(A+1) = rax(mov [rbx+(A+1)*8], rax)
//	         (7 字节,EmitMovqMemRegFromRax with reg=rbx)
//	[14-17]  shr rax, 48(4 字节)
//	[18-22]  cmp eax, 0xFFFC(5 字节)
//	[23-28]  jne deopt(6 字节)
//	         **注**:索引接续 ArrayHit 模板,严密 IsTable guard 沿用
//	... 同 ArrayHit getter:GCRef extract / gen check / arrayRef /
//	    array[stableIndex] / nil check / 写 R(A) / ret / deopt
//
// 实测精确 139 字节(逐原语累加:7+7+4+5+6+7+10+3+3+7+8+4+5+6+8+3+8+10+3+
// 6+7+1+10+1=139)。EmitMovqMemRegFromRax 用通用 disp32 编码(7 字节),
// 不走 disp8 short form(避免模板 length 随 reg 编号波动)。
//
// **deopt 路径**:Run 端 raxSpec==deoptCode 时调 host.GetTable byte-equal P1
// (R(A+1)=R(B) 已 store 成功不需要回滚——R(A+1) 写入是 SELF 第一步,
// deopt 路径走 host.GetTable 仍需 R(A+1) 已设;P1 解释器 SELF case 同源)。
// **注**:SELF deopt 路径调 host.GetTable + R(A+1) 已设,与 P1 SELF 路径
// byte-equal(P1 execute.go SELF case 同款步骤:setReg(A+1, B) → icGetTable
// → setReg(A))。
func EmitSelfArrayHit(buf []byte, aReg, bReg uint8,
	stableShape, stableIndex uint32,
	arenaBaseOff int32, deoptCode uint64) []byte {
	// 1. load R(B) → rax(obj NaN-box)
	buf = EmitMovqRaxFromMemReg(buf, 3 /*rbx*/, int32(bReg)*8)

	// 2. **SELF 额外步骤**:store R(A+1) = rax(self/this 实参)
	//    R(A+1) 槽偏移 = (aReg+1)*8
	buf = EmitMovqMemRegFromRax(buf, 3 /*rbx*/, int32(aReg+1)*8)

	// 3. 严密 IsTable guard
	buf = EmitShrRaxImm8(buf, 48)
	buf = EmitCmpEaxImm32(buf, qNanBoxTableTagHigh16)
	buf = EmitJneRel32(buf, 0)
	jneTagOff := len(buf) - 4

	// 4. shr 已破坏 rax,重新 load R(B)
	buf = EmitMovqRaxFromMemReg(buf, 3 /*rbx*/, int32(bReg)*8)

	// 5. GCRef extract
	const payloadMask uint64 = 0x0000_FFFF_FFFF_FFFF
	buf = EmitMovRcxImm64(buf, payloadMask)
	buf = append(buf, 0x48, 0x21, 0xC8) // and rax, rcx

	// 6. mov rcx, rax(GCRef offset)
	buf = EmitMovqRcxFromRax(buf)

	// 7. load arena base → r14
	buf = EmitMovqR14FromR15Disp(buf, arenaBaseOff)

	// 8. gen check:load word5 + shr + cmp eax + jne
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

	// 12. store R(A) = rax(method 函数)
	buf = EmitMovqMemRegFromRax(buf, 3 /*rbx*/, int32(aReg)*8)

	// 13. ret(normal exit)
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

// EmitSetTableNodeHit 拼接 PJ4 SETTABLE IC NodeHit 字节级 inline 反向写
// 模板(承 03 §6 + GetTable NodeHit + SetTable ArrayHit 同款结构组合)。
//
// **形态**:`function(t, v) t[K] = v end` 中 K 是字符串/任意键 in hash 段
// (luac 编 SETTABLE A B C 中 A=R(t) / B=K idx >=256 / C=R(v))。
// IC[0].Kind=NodeHit + Shape/Index/Key 命中时,字节级 inline 反向写
// node[stableIndex].val。
//
// 参数:
//   - aReg:表寄存器号 R(A)(SETTABLE 的 A)
//   - cReg:value 寄存器号 R(C)(value 是 reg,C<256)
//   - stableShape:编译期固化的 table.gen 快照
//   - stableIndex:编译期固化的 node slot 下标
//   - stableKey:编译期固化的 key NaN-box(从 proto.Consts[KIdx])
//   - arenaBaseOff / deoptCode 同 GetTable NodeHit
//
// **字节布局**(140 字节,GetTable NodeHit 159 - NodeVal/nil/storeRA 34 + load R(C)/反向 store 15):
//
//	[0-6]    load R(A) → rax(7 字节)
//	[7-21]   严密 IsTable guard(15 字节)
//	[22-44]  re-load + GCRef extract + rcx = offset(23 字节)
//	[45-51]  load arena base → r14(7 字节)
//	[52-74]  gen check word5/shr/cmp eax/jne(23 字节)
//	[75-82]  load table.nodeRef = [r14+rcx+24] → rax(8 字节)
//	[83-85]  mov rcx, rax(rcx = nodeRef offset)(3 字节)
//	[86-93]  load NodeKey = [r14+rcx+stableIndex*24] → rax(8 字节)
//	[94-103] mov rdx, stableKey(10 字节)
//	[104-106] cmp rax, rdx(3 字节)
//	[107-112] jne deopt(6 字节,NodeKey != stableKey → deopt)
//	[113-119] load R(C) → rdx(7 字节,setter 加载 value)
//	[120-127] mov [r14+rcx+stableIndex*24+8], rdx(8 字节,反向 store NodeVal)
//	[128]     ret(1 字节)
//	[129-139] deopt block(mov rax, deoptCode + ret,11 字节)
//	——— 总计 140 字节 ———
//
// **vs SetTable ArrayHit + GetTable NodeHit 复合差异**:
//   - 比 GetTable NodeHit:删 NodeVal load(getter 用)+ nil check + 写 R(A)
//   - 比 SetTable ArrayHit:取 word3=nodeRef + 24 步长 + 多 key 比对
//   - rdx 复用:rdx 先装 stableKey(key 比对)→ 用完后被 R(C) value 覆盖
//
// **deopt 路径**:Run 端 raxSpec==deoptCode 时调 host.SetTable byte-equal
// P1(经 icSetTable + __newindex 元方法链;P1 icSetTable 兼容 NodeHit)。
// setter 形态 retB=1,Run 端 DoReturn 不读 R(A)。
//
// **设计简化**(承 SetTable ArrayHit 同款边界):
//   - 不验现有 NodeVal != nil(防新键路径)
//   - 假设无 __newindex 元表
func EmitSetTableNodeHit(buf []byte, aReg, cReg uint8,
	stableShape, stableIndex uint32, stableKey uint64,
	arenaBaseOff int32, deoptCode uint64) []byte {
	// 1. load R(A) → rax
	buf = EmitMovqRaxFromMemReg(buf, 3 /*rbx*/, int32(aReg)*8)

	// 2. 严密 IsTable guard
	buf = EmitShrRaxImm8(buf, 48)
	buf = EmitCmpEaxImm32(buf, qNanBoxTableTagHigh16)
	buf = EmitJneRel32(buf, 0)
	jneTagOff := len(buf) - 4

	// 3. shr 已破坏 rax,重新 load R(A)
	buf = EmitMovqRaxFromMemReg(buf, 3 /*rbx*/, int32(aReg)*8)

	// 4. GCRef extract
	const payloadMask uint64 = 0x0000_FFFF_FFFF_FFFF
	buf = EmitMovRcxImm64(buf, payloadMask)
	buf = append(buf, 0x48, 0x21, 0xC8) // and rax, rcx

	// 5. mov rcx, rax(rcx = GCRef offset)
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

	// 8. **NodeHit 分流**:load table.nodeRef = [r14+rcx+24] → rax
	buf = EmitMovqRaxFromR14PlusRcxDisp(buf, 24)

	// 9. mov rcx, rax(rcx = nodeRef offset)
	buf = EmitMovqRcxFromRax(buf)

	// 10. load NodeKey = [r14+rcx+stableIndex*24] → rax
	buf = EmitMovqRaxFromR14PlusRcxDisp(buf, int32(stableIndex)*24)

	// 11. mov rdx, stableKey
	buf = EmitMovRdxImm64(buf, stableKey)

	// 12. cmp rax, rdx + jne deopt
	buf = EmitCmpRaxRdx(buf)
	buf = EmitJneRel32(buf, 0)
	jneKeyOff := len(buf) - 4

	// 13. **setter 分流**:load R(C) value → rdx(覆盖 stableKey,rdx 复用)
	buf = EmitMovqRdxFromMemRbx(buf, int32(cReg)*8)

	// 14. 反向 store:mov [r14+rcx+stableIndex*24+8], rdx(写 NodeVal)
	buf = EmitMovqMemR14PlusRcxFromRdx(buf, int32(stableIndex)*24+8)

	// 15. ret(setter 无 R(A) 写)
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

// EmitSelfNodeHit 拼接 PJ4 SELF IC NodeHit 字节级 inline 模板(承
// 03 §6 + SELF ArrayHit + GetTable NodeHit 同款结构组合)。
//
// **形态**:`function(obj) obj:method(args) end` 中 method 是字符串 ident
// (luac 编 SELF A B C 中 A=R(m) / B=R(obj) / C=K(string method name))。
// 这是 real-world `obj:method()` 调用的典型形态(几乎所有 OOP 风格 Lua
// 代码都走此路径)。
//
// IC[0].Kind=NodeHit + Shape/Index/Key 命中时,字节级 inline:
//
//	R(A+1) := R(B)  ; self/this 实参
//	R(A)   := R(B)[K_string]  ; method 函数(经 hash 段 NodeHit 直达)
//
// **字节布局**(166 字节,SELF ArrayHit 139 + key 比对 27):
//
//	[0-6]    load R(B) → rax(7 字节)
//	[7-13]   store R(A+1) = rax(7 字节,SELF 第一步)
//	[14-28]  严密 IsTable guard(15 字节)
//	[29-35]  re-load R(B) → rax(7 字节)
//	[36-51]  GCRef extract + rcx = offset(16 字节)
//	[52-58]  load arena base → r14(7 字节)
//	[59-81]  gen check(23 字节)
//	[82-89]  load nodeRef = [r14+rcx+24] → rax(8 字节,word3)
//	[90-92]  mov rcx, rax(rcx = nodeRef offset)(3 字节)
//	[93-100] load NodeKey = [r14+rcx+stableIndex*24] → rax(8 字节)
//	[101-110] mov rdx, stableKey(10 字节)
//	[111-113] cmp rax, rdx(3 字节)
//	[114-119] jne deopt(6 字节)
//	[120-127] load NodeVal = [r14+rcx+stableIndex*24+8] → rax(8 字节)
//	[128-137] mov rcx, qNanBoxNil(10 字节)
//	[138-140] cmp rax, rcx(3 字节)
//	[141-146] je deopt(6 字节,NodeVal == Nil)
//	[147-153] store R(A) = rax(7 字节)
//	[154]    ret(1 字节)
//	[155-165] deopt block(11 字节)
//	——— 总计 166 字节 ———
//
// vs SELF ArrayHit 关键差异:
//   - 取 word3=nodeRef(offset 24)而非 word2=arrayRef(offset 16)
//   - node 步长 24 字节
//   - 多 key 比对段(mov rdx stableKey + cmp rax rdx + jne)
//
// **deopt 路径**:Run 端 raxSpec==deoptCode 时调 host.GetTable byte-equal P1
// (R(A+1)=R(B) 已 store,P1 SELF case 同款步骤;P1 icGetTable 兼容 NodeHit)。
func EmitSelfNodeHit(buf []byte, aReg, bReg uint8,
	stableShape, stableIndex uint32, stableKey uint64,
	arenaBaseOff int32, deoptCode uint64) []byte {
	// 1. load R(B) → rax(obj NaN-box)
	buf = EmitMovqRaxFromMemReg(buf, 3 /*rbx*/, int32(bReg)*8)

	// 2. **SELF 额外**:store R(A+1) = rax(self/this 实参)
	buf = EmitMovqMemRegFromRax(buf, 3 /*rbx*/, int32(aReg+1)*8)

	// 3. 严密 IsTable guard
	buf = EmitShrRaxImm8(buf, 48)
	buf = EmitCmpEaxImm32(buf, qNanBoxTableTagHigh16)
	buf = EmitJneRel32(buf, 0)
	jneTagOff := len(buf) - 4

	// 4. shr 已破坏 rax,重新 load R(B)
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

	// 9. **NodeHit 分流**:load nodeRef = [r14+rcx+24]
	buf = EmitMovqRaxFromR14PlusRcxDisp(buf, 24)

	// 10. mov rcx, rax(rcx = nodeRef offset)
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

	// 16. store R(A) = rax(method 函数)
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

// EmitSpecArgLoadK 写 R(dstReg) = K(NaN-box u64)— PJ5 SELF spec template
// args 装载字节级 inline 用,代替 host.SetReg(dstReg, K)round-trip。
//
// 字节序列(10+7 = 17 字节):
//
//	mov rax, K_imm64        ; 10 字节
//	mov [rbx + dstReg*8], rax  ; 7 字节(disp32 模式)
func EmitSpecArgLoadK(buf []byte, dstReg uint8, k uint64) []byte {
	buf = EmitMovRaxImm64(buf, k)
	buf = EmitMovqMemRegFromRax(buf, 3 /*rbx*/, int32(dstReg)*8)
	return buf
}

// EmitSpecArgLoadReg 写 R(dstReg) = R(srcReg)— PJ5 SELF spec template
// args 装载字节级 inline 用,代替 host.SetReg(dstReg, host.GetReg(srcReg))
// 双 round-trip。
//
// 字节序列(7+7 = 14 字节):
//
//	mov rax, [rbx + srcReg*8]
//	mov [rbx + dstReg*8], rax
func EmitSpecArgLoadReg(buf []byte, dstReg uint8, srcReg uint8) []byte {
	buf = EmitMovqRaxFromMemReg(buf, 3 /*rbx*/, int32(srcReg)*8)
	buf = EmitMovqMemRegFromRax(buf, 3 /*rbx*/, int32(dstReg)*8)
	return buf
}

// EmitFrameInlineCIDepthInc 发射字节级 ciDepth++ inline 模板(承
// `docs/design/p4-method-jit/implementation-progress.md` §9.20 Option B
// Spike 1 起手积木):mmap 段经 r15 → 解引 jitContext.ciDepthAddr 到 rax,
// 然后字节级 inc qword ptr [rax],等价 enterLuaFrame 中 `th.setCIDepth(
// th.ciDepth+1)` 的 ciDepth 字镜像写入(P3 PW10 Stage 1a 镜像字复用)。
//
// 字节序列(10 字节):
//
//	mov rax, [r15 + ciDepthAddrOffset]  ; 7 字节(承 EmitMovqRaxFromR15Disp,
//	                                    ;        实际编码 49 8B 87 disp32:
//	                                    ;        REX.W+B 让 rm 字段用 r15)
//	inc qword ptr [rax]                  ; 3 字节(承 EmitIncQwordPtrAtRax:
//	                                    ;        48 FF 00)
//
// **参数 ciDepthAddrOffset**:`JITContextCIDepthAddrOffset`(承 jitcontext.go
// const)— 调用方必须传 jit.JITContextCIDepthAddrOffset 编译期常量,本函数
// 不直接依赖 jit 包(避免循环依赖)。
//
// **承 §9.20 Spike 1 守门**:本模板仅在 callee.NumParams=0 + !IsVararg +
// !NeedsArg 形态下 emit;callee 帧建拆其余 4 个 word 写入 + popCallInfo
// 同款手法(EmitFrameInlineCIDepthDec 即将加)。
//
// **arena grow 风险**:ciDepthAddr 由 jitContext.SetCIDepthAddr 在每次
// Run 入口现算注入(承 code.go::Run line 268-271 起接入);arena grow 触发
// 段重定位时下次 Run 重载,本字段不缓存指向 host 地址。
func EmitFrameInlineCIDepthInc(buf []byte, ciDepthAddrOffset int32) []byte {
	buf = EmitMovqRaxFromR15Disp(buf, ciDepthAddrOffset)
	buf = EmitIncQwordPtrAtRax(buf)
	return buf
}

// EmitFrameInlineCIDepthDec 发射字节级 ciDepth-- inline 模板(承 §9.20
// Option B Spike 1 popCallInfo 反向)。
//
// 字节序列(10 字节):同 Inc 但末 inc 改 dec(等价 popCallInfo 中
// `th.setCIDepth(th.ciDepth-1)`)。
func EmitFrameInlineCIDepthDec(buf []byte, ciDepthAddrOffset int32) []byte {
	buf = EmitMovqRaxFromR15Disp(buf, ciDepthAddrOffset)
	buf = EmitDecQwordPtrAtRax(buf)
	return buf
}

// EncodedFrameInlineCIDepthIncDecLen 是「ciDepth++/--」字节级 inline 模板
// 字节数(7+3=10)。承 §9.20 Spike 1 caller 单测 + Compile 段长度预算。
const EncodedFrameInlineCIDepthIncDecLen = 10

// EmitFrameInlineLoadCISlotAddr 发射字节级 CI 段第 depth 帧起点字节地址
// 加载到 rax 模板(承 §9.20 Option B Spike 1 enterLuaFrame inline 第一段)。
//
// 该模板把 ciSegBaseAddr + ciDepth * 40(每帧 ciWords=5 字 = 40 字节)算出
// CallInfo[depth] 帧地址,准备后续 writeCIWordN 写各 word。
//
// 字节序列(7+3+7+3+5+3 = 28 字节):
//
//	mov rcx, [r15 + ciDepthAddrOffset]    ; 7 字节 = mov rcx, [r15+disp32]
//	                                        ;     实际 EmitMovqRcxFromR15Disp 不存在,
//	                                        ;     用 EmitMovqRaxFromR15Disp + mov rcx, rax
//	                                        ;     OR 改用 EmitMovqRaxFromR15Disp + EmitMovqRcxFromRax
//	mov rcx, [rcx]                          ; 3 字节(48 8B 09 = mov rcx, [rcx])
//	mov rax, [r15 + ciSegBaseAddrOffset]    ; 7 字节
//	mov rax, [rax]                          ; 3 字节(48 8B 00 = mov rax, [rax])
//	imul rcx, rcx, 40                       ; 4 字节(48 6B C9 28 = imul rcx, rcx, 40)
//	add rax, rcx                            ; 3 字节(48 01 C8 = add rax, rcx)
//
// 模板结束后 rax = CallInfo[depth] 字节地址。后续 writeCIWordN(rax, word_idx, val)
// 经 mov [rax + word_idx*8], rcx 写每 word。
//
// **总长度 28 字节**(amd64 端 enterLuaFrame inline 第一段)。
//
// **arena grow 注意**:ciDepthAddr / ciSegBaseAddr 每次 Run 入口现算,不缓存
// (承 §9.20 + arena base 重载协议)。
func EmitFrameInlineLoadCISlotAddr(buf []byte, ciDepthAddrOffset, ciSegBaseAddrOffset int32) []byte {
	// 1. rcx = ciDepth(深度值)
	buf = EmitMovqRaxFromR15Disp(buf, ciDepthAddrOffset)
	buf = EmitMovqRcxFromRax(buf)
	// rcx 现是 ciDepthAddr。再解引一次:mov rcx, [rcx]
	buf = append(buf, 0x48, 0x8B, 0x09) // mov rcx, [rcx]
	// 2. rax = ciSegBase(段基址)
	buf = EmitMovqRaxFromR15Disp(buf, ciSegBaseAddrOffset)
	// rax 现是 ciSegBaseAddr。解引:mov rax, [rax]
	buf = append(buf, 0x48, 0x8B, 0x00) // mov rax, [rax]
	// 3. imul rcx, rcx, 40 — depth * ciSlotBytes
	buf = append(buf, 0x48, 0x6B, 0xC9, 40) // imul rcx, rcx, 40
	// 4. add rax, rcx — rax = ciSegBase + depth * 40 = CallInfo[depth] 地址
	buf = append(buf, 0x48, 0x01, 0xC8) // add rax, rcx
	return buf
}

// EncodedFrameInlineLoadCISlotAddrLen 是「CI 段第 depth 帧地址加载到 rax」
// 模板字节数(7+3+3+7+3+4+3 = 30 — 实际,EmitMovqRaxFromR15Disp 是 7,
// EmitMovqRcxFromRax 是 3,后续 mov rcx [rcx] 是 3,mov rax [r15+...] 是 7,
// mov rax [rax] 是 3,imul 4,add 3,合计 30 字节,非 28)。
const EncodedFrameInlineLoadCISlotAddrLen = 30

// EmitFrameInlineWriteCIWord 发射字节级 CI 帧 word_idx 写入 imm64 模板
// (承 §9.20 Option B Spike 1 enterLuaFrame inline 第二段)。
//
// 调用契约:rax 必须已装 CallInfo[depth] 帧起点字节地址(承
// EmitFrameInlineLoadCISlotAddr 已 setup);word_idx 范围 [0,4](承 ciWords=5)。
//
// 字节序列(10+4 = 14 字节):
//
//	mov rcx, imm64                      ; 10 字节(EmitMovRcxImm64)
//	mov [rax + word_idx*8], rcx         ; 4 字节(48 89 48 disp8)
//
// **word layout**(承 state.go::writeCISeg / packCIWord2):
//   - word0 = uint32(base) | uint32(funcIdx) << 32
//   - word1 = uint32(top)  | uint32(pc)      << 32
//   - word2 = uint32(protoID) | uint16(nresults) << 32 | flags<<48
//     (tailcall<<48 / fresh<<49 / gibbous<<50)
//   - word3 = uint64(cl)(arena.GCRef closure 镜像)
//   - word4 = uint64(nVarargs)(其他位预留)
//
// Spike 1 调用方按 word_idx=0..4 顺序调 5 次,完成 enterLuaFrame 的 CI 段写入。
func EmitFrameInlineWriteCIWord(buf []byte, wordIdx uint8, imm64 uint64) []byte {
	if wordIdx > 4 {
		wordIdx = 0 // 兜底防越界
	}
	buf = EmitMovRcxImm64(buf, imm64)
	// mov [rax + wordIdx*8], rcx — 48 89 48 disp8
	// REX.W = 0x48 / opcode 0x89(MOV r/m64, r64)
	// ModRM = mod 01(disp8)+ reg 001(rcx)+ rm 000(rax)= 0x48
	// disp8 = wordIdx * 8
	buf = append(buf, 0x48, 0x89, 0x48, byte(int8(wordIdx)*8))
	return buf
}

// EncodedFrameInlineWriteCIWordLen 是「写 CI 帧 word_idx」字节级模板字节数
// (10+4=14)。承 §9.20 Spike 1 caller 长度预算。
const EncodedFrameInlineWriteCIWordLen = 14

// FrameInlineCISlotWords amd64 端 Spike 1 用的 CI 帧 5 word 入参组(承
// state.go::writeCISeg + packCIWord2;各 word 由 caller 编译期烧 imm64)。
type FrameInlineCISlotWords struct {
	Word0 uint64 // base | funcIdx << 32
	Word1 uint64 // top | pc << 32
	Word2 uint64 // protoID | nresults<<32 | flags<<48(tailcall<<48 / fresh<<49 / gibbous<<50)
	Word3 uint64 // cl(arena.GCRef closure 镜像)
	Word4 uint64 // nVarargs
}

// EmitFrameInlineBuildVoid0ArgSkeleton 发射 amd64 Spike 1 enterLuaFrame 字节级
// inline 骨架(承 §9.20 Option B Spike 1):
//
//  1. LoadCISlotAddr:rax = CallInfo[depth] 帧起点字节地址(30 字节)
//  2. WriteCIWord × 5:写 5 word(14*5 = 70 字节)
//  3. CIDepthInc:ciDepth++(10 字节)
//
// **总长度**:30 + 70 + 10 = 110 字节
//
// **守门**(Spike 1 阶段 caller 必须保证):
//   - callee.NumParams=0 + !IsVararg + !NeedsArg + MaxStack≤32
//   - 各 word imm64 由 caller 编译期烧入(base / funcIdx / top / pc / protoID
//     / nresults=0 / cl GCRef / nVarargs=0)
//   - rax 在模板出口处为 CallInfo[depth] 帧地址(供后续 helper call / popCallInfo
//     共用)
//
// **仍剩 Spike 1 后续工程**(本批不实装):
//   - callee closure GCRef 解析(R(callA) → 解 NaN-box → cl GCRef 现算装 word3)
//   - 跳 helper 入 callee 执行(executeFrom 或 callee P4 段)
//   - popCallInfo 反向(LoadCISlotAddr + CIDepthDec)+ 多返值处理
//   - Compile/Run 端接通 + e2e prove-the-path
func EmitFrameInlineBuildVoid0ArgSkeleton(buf []byte,
	ciDepthAddrOffset, ciSegBaseAddrOffset int32,
	words FrameInlineCISlotWords) []byte {
	// 1. rax = CallInfo[depth] 帧起点
	buf = EmitFrameInlineLoadCISlotAddr(buf, ciDepthAddrOffset, ciSegBaseAddrOffset)
	// 2. 写 5 word
	buf = EmitFrameInlineWriteCIWord(buf, 0, words.Word0)
	buf = EmitFrameInlineWriteCIWord(buf, 1, words.Word1)
	buf = EmitFrameInlineWriteCIWord(buf, 2, words.Word2)
	buf = EmitFrameInlineWriteCIWord(buf, 3, words.Word3)
	buf = EmitFrameInlineWriteCIWord(buf, 4, words.Word4)
	// 3. ciDepth++
	buf = EmitFrameInlineCIDepthInc(buf, ciDepthAddrOffset)
	return buf
}

// EncodedFrameInlineBuildVoid0ArgSkeletonLen = 30 + 14*5 + 10 = 110.
const EncodedFrameInlineBuildVoid0ArgSkeletonLen = 110
