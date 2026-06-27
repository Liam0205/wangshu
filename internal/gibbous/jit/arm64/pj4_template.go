//go:build wangshu_p4 && arm64

// pj4_template.go —— PJ8 arm64 PJ4 表 IC 六路径字节级模板(对位 amd64
// pj4_template.go,六路径完整集合)。
//
// **不真接入**(承 §9.12 剩余工程量明示):arm64 trampoline asm + mmap+RX
// 端到端 留物理 self-hosted runner;本批仅做字节级模板拼接 + 字节级单测
// 验布局,为下一阶段真接入提供基础。
//
// **arm64 寄存器协议**(承 06-backends.md §4.2,本批留模板 inline 形式
// 在 trampoline 接通后才能跑):
//   - x26 = valueStackBase(对位 amd64 rbx)
//   - x27 = jitContext(对位 amd64 r15)
//   - x28 = Go G(Go runtime 保留)
//   - x14 = arena base(模板入口装入,对位 amd64 r14)
//   - x0/x1/x2/x3 = scratch
//
// **六路径**:
//   - EmitGetTableArrayHitArm64(168 字节)
//   - EmitGetTableNodeHitArm64(对位 amd64 159B)
//   - EmitSetTableArrayHitArm64(对位 amd64 113B)
//   - EmitSetTableNodeHitArm64(对位 amd64 140B)
//   - EmitSelfArrayHitArm64(对位 amd64 139B)
//   - EmitSelfNodeHitArm64(对位 amd64 166B)

package arm64

// qNanBoxTableTagShiftedArm64 arm64 端 NaN-box table tag 高 16 位值(对位 amd64
// 同名常量但 arm64 模板用 LSR + CMP 严密 guard,直接比较 16-bit 值)。
const qNanBoxTableTagShiftedArm64 uint64 = 0xFFFC

// qNanBoxNilImmArm64 arm64 端 NaN-box Nil 完整值(对位 amd64 qNanBoxNilImm)。
const qNanBoxNilImmArm64 uint64 = 0xFFFE_0000_0000_0000

// payloadMaskArm64 GCRef payload 提取掩码(高 16 位 NaN-box tag 清零)。
const payloadMaskArm64 uint64 = 0x0000_FFFF_FFFF_FFFF

// EmitGetTableArrayHitArm64 拼接 arm64 PJ4 IC ArrayHit 字节级直达槽模板
// (对位 amd64 EmitGetTableArrayHit 132 字节版的 arm64 端镜像)。
//
// **不真接入**(arm64 trampoline asm + mmap+RX 端到端 留物理 self-hosted
// runner);本批纯字节级模板拼接 + 字节级单测验布局。
//
// **字节布局**(168 字节,严密 IsTable guard 版):
//
//	[ 0-3 ] LDR x0, [x26 + B*8]          ; 4(load R(B) NaN-box)
//	[ 4-7 ] LSR x0, x0, #48               ; 4(IsTable shift)
//	[ 8-23] MOV x1, 0xFFFC imm64          ; 16(load TagTable)
//	[24-27] CMP x0, x1                    ; 4
//	[28-31] B.NE deopt                    ; 4
//	[32-35] LDR x0, [x26 + B*8]           ; 4(re-load R(B))
//	[36-51] MOV x1, payloadMask imm64     ; 16
//	[52-55] AND x0, x0, x1                ; 4(GCRef extract)
//	[56-59] MOV x1, x0(ORR x1, XZR, x0)   ; 4(rcx = GCRef offset)
//	[60-63] LDR x14, [x27 + arenaBaseOff] ; 4(load arena base → x14)
//	[64-67] ADD x2, x14, x1               ; 4(SIB 替代:x2 = base + GCRef)
//	[68-71] LDR x0, [x2, #40]             ; 4(table.word5 → x0)
//	[72-75] LSR x0, x0, #32               ; 4(gen 在高 32 位)
//	[76-91] MOV x3, stableShape imm64     ; 16
//	[92-95] CMP x0, x3                    ; 4
//	[96-99] B.NE deopt                    ; 4
//	[100-103] LDR x0, [x2, #16]           ; 4(table.arrayRef → x0)
//	[104-107] MOV x1, x0                   ; 4(rcx = arrayRef offset)
//	[108-111] ADD x2, x14, x1              ; 4(SIB 替代:x2 = base + arrayRef)
//	[112-115] LDR x0, [x2, #stableIndex*8] ; 4(array[stableIndex])
//	[116-131] MOV x3, qNanBoxNil imm64    ; 16
//	[132-135] CMP x0, x3                    ; 4
//	[136-139] B.EQ deopt                   ; 4
//	[140-143] STR x0, [x26 + A*8]         ; 4(store R(A))
//	[144-147] RET                          ; 4
//	[148-163] MOV x0, deoptCode imm64     ; 16(deopt block)
//	[164-167] RET                          ; 4
//	——— 总计 168 字节(amd64 132 + arm64 SIB 替代 + MOV imm64 长度差)———
//
// **vs amd64 132 字节差 36 字节**:
//   - arm64 SIB 替代:2 次 ADD + LDR(8 字节)替代 amd64 单条 SIB ldr
//     (10 字节)→ 实际省 2 字节但多 2 条指令
//   - arm64 MOV imm64 序列 16 字节(movz+3*movk)vs amd64 10 字节
//     (mov rax imm64)→ 多 6 字节/次,4 次 MOV imm64 多 24 字节
//   - 总差 ~36 字节,符合预期
//
// **预设条件**(承 06 §4.2 arm64 trampoline 留 PJ8+):
//   - x26 = valueStackBase
//   - x27 = jitContext
//   - x28 = Go G(Go runtime 保留)
//   - x14 = arena base(本模板入口装入)
//   - x0/x1/x2/x3 = scratch
//
// **deopt 路径**:Run 端 x0==deoptCode 时调 host.GetTable byte-equal P1
// (P1 icGetTable 兼容 ArrayHit + NodeHit;arm64 端复用)。
func EmitGetTableArrayHitArm64(buf []byte, aReg, bReg uint8, stableShape, stableIndex uint32,
	arenaBaseOff uint16, deoptCode uint64) []byte {
	// 1. LDR x0, [x26 + B*8](load R(B))
	buf = EmitLdrXtFromXnDisp(buf, 0, 26, uint16(bReg)*8)

	// 2. LSR x0, x0, #48(IsTable shift)
	buf = EmitLsrXdImm6(buf, 0, 0, 48)

	// 3. MOV x1, 0xFFFC imm64
	buf = EmitMovXdImm64(buf, 1, qNanBoxTableTagShiftedArm64)

	// 4. CMP x0, x1
	buf = EmitCmpXnXm(buf, 0, 1)

	// 5. B.NE deopt(placeholder,patch 后)
	bNeTagOff := len(buf)
	buf = EmitBCond(buf, CondNE, 0)

	// 6. re-load R(B)
	buf = EmitLdrXtFromXnDisp(buf, 0, 26, uint16(bReg)*8)

	// 7. MOV x1, payloadMask imm64
	buf = EmitMovXdImm64(buf, 1, payloadMaskArm64)

	// 8. AND x0, x0, x1
	buf = EmitAndXdXnXm(buf, 0, 0, 1)

	// 9. MOV x1, x0(rcx = GCRef offset)
	buf = EmitMovXdFromXn(buf, 1, 0)

	// 10. LDR x14, [x27 + arenaBaseOff](load arena base → x14)
	buf = EmitLdrXtFromXnDisp(buf, 14, 27, arenaBaseOff)

	// 11. ADD x2, x14, x1(SIB 替代:base + GCRef)
	buf = EmitAddXdXnXm(buf, 2, 14, 1)

	// 12. LDR x0, [x2, #40](table.word5 → x0)
	buf = EmitLdrXtFromXnDisp(buf, 0, 2, 40)

	// 13. LSR x0, x0, #32(gen 在高 32 位)
	buf = EmitLsrXdImm6(buf, 0, 0, 32)

	// 14. MOV x3, stableShape imm64
	buf = EmitMovXdImm64(buf, 3, uint64(stableShape))

	// 15. CMP x0, x3
	buf = EmitCmpXnXm(buf, 0, 3)

	// 16. B.NE deopt
	bNeShapeOff := len(buf)
	buf = EmitBCond(buf, CondNE, 0)

	// 17. LDR x0, [x2, #16](table.arrayRef → x0)
	buf = EmitLdrXtFromXnDisp(buf, 0, 2, 16)

	// 18. MOV x1, x0(arrayRef offset)
	buf = EmitMovXdFromXn(buf, 1, 0)

	// 19. ADD x2, x14, x1(SIB 替代:base + arrayRef)
	buf = EmitAddXdXnXm(buf, 2, 14, 1)

	// 20. LDR x0, [x2, #stableIndex*8](array[stableIndex])
	buf = EmitLdrXtFromXnDisp(buf, 0, 2, uint16(stableIndex)*8)

	// 21. MOV x3, qNanBoxNil imm64
	buf = EmitMovXdImm64(buf, 3, qNanBoxNilImmArm64)

	// 22. CMP x0, x3
	buf = EmitCmpXnXm(buf, 0, 3)

	// 23. B.EQ deopt(nil → deopt)
	bEqNilOff := len(buf)
	buf = EmitBCond(buf, CondEQ, 0)

	// 24. STR x0, [x26 + A*8](store R(A))
	buf = EmitStrXtToXnDisp(buf, 0, 26, uint16(aReg)*8)

	// 25. RET
	buf = EmitRet(buf)

	// 26. deopt block
	deoptStart := len(buf)
	buf = EmitMovXdImm64(buf, 0, deoptCode)
	buf = EmitRet(buf)

	// 27. patch B.cond imm19 = (deoptStart - 本 B.cond 自身) / 4
	patchBCondImm19(buf, bNeTagOff, int32(deoptStart-bNeTagOff)/4)
	patchBCondImm19(buf, bNeShapeOff, int32(deoptStart-bNeShapeOff)/4)
	patchBCondImm19(buf, bEqNilOff, int32(deoptStart-bEqNilOff)/4)

	return buf
}

// EncodedGetTableArrayHitArm64Len arm64 PJ4 IC ArrayHit 模板字节数(168)。
const EncodedGetTableArrayHitArm64Len = 168

// EmitGetTableNodeHitArm64 拼接 arm64 PJ4 IC NodeHit 字节级直达槽模板
// (对位 amd64 EmitGetTableNodeHit 159 字节版的 arm64 端镜像)。
//
// **不真接入**(承文件头注同款):arm64 trampoline asm + mmap+RX 端到端
// 留物理 self-hosted runner;本批纯字节级模板拼接 + 字节级单测验布局。
//
// **NodeHit vs ArrayHit 差异**(承 amd64 NodeHit 同款):
//   - 取 word3=nodeRef(offset 24)而非 word2=arrayRef(offset 16)
//   - node[idx] 步长 24 字节(nodeWords=3:key/val/next)而非 array[idx] 8 字节
//   - 多 key 比对(NodeKey == stableKey 验证防键退化 / __index 链)
//
// **字节布局**(196 字节,ArrayHit 168 + key 比对段 28):
//
//	[ 0-31] IsTable guard(LDR + LSR + MOV imm64 + CMP + B.NE,32 字节)
//	[32-67] re-load + payloadMask AND + MOV x1,x0 + LDR x14 + ADD x2(36 字节)
//	[68-71] LDR x0, [x2, #40]                  ; 4(word5)
//	[72-75] LSR x0, x0, #32                    ; 4
//	[76-91] MOV x3, stableShape                ; 16
//	[92-95] CMP x0, x3                         ; 4
//	[96-99] B.NE deopt                         ; 4
//	[100-103] LDR x0, [x2, #24]                ; 4(**nodeRef** word3,NodeHit 分流)
//	[104-107] MOV x1, x0                       ; 4(rcx = nodeRef offset)
//	[108-111] ADD x2, x14, x1                  ; 4(SIB 替代:新 base for node)
//	[112-115] LDR x0, [x2, #stableIndex*24]    ; 4(NodeKey)
//	[116-131] MOV x3, stableKey imm64          ; 16
//	[132-135] CMP x0, x3                       ; 4
//	[136-139] B.NE deopt                       ; 4(NodeKey != stableKey)
//	[140-143] LDR x0, [x2, #stableIndex*24+8]  ; 4(NodeVal)
//	[144-159] MOV x3, qNanBoxNil imm64         ; 16
//	[160-163] CMP x0, x3                       ; 4
//	[164-167] B.EQ deopt                       ; 4(NodeVal == Nil)
//	[168-171] STR x0, [x26 + A*8]              ; 4
//	[172-175] RET                              ; 4
//	[176-191] MOV x0, deoptCode imm64          ; 16(deopt block)
//	[192-195] RET                              ; 4
//	——— 总计 196 字节 ———
//
// **stableKey 编译期固化**(承 amd64 NodeHit 同款):
//   - 数字键:value.NumberValue(K) raw bits(IEEE 754 NaN-box)
//   - 字符串键:value.MakeGC(TagString, ref) NaN-box,ref 编译期已 intern 不变
//   - 用 NaN-box 整体比较等价于 keyEqual(承 ic.go::keyEqual 同源)
//
// **deopt 路径**:Run 端 x0==deoptCode 时调 host.GetTable byte-equal P1
// (P1 icGetTable 兼容 NodeHit;经 IC + 哈希 + __index 元方法链)。
func EmitGetTableNodeHitArm64(buf []byte, aReg, bReg uint8,
	stableShape, stableIndex uint32, stableKey uint64,
	arenaBaseOff uint16, deoptCode uint64) []byte {
	// 1-5. 严密 IsTable guard(LDR + LSR + MOV imm64 + CMP + B.NE 共 32 字节)
	buf = EmitLdrXtFromXnDisp(buf, 0, 26, uint16(bReg)*8)
	buf = EmitLsrXdImm6(buf, 0, 0, 48)
	buf = EmitMovXdImm64(buf, 1, qNanBoxTableTagShiftedArm64)
	buf = EmitCmpXnXm(buf, 0, 1)
	bNeTagOff := len(buf)
	buf = EmitBCond(buf, CondNE, 0)

	// 6-11. re-load + GCRef extract + arena base + ADD x2 SIB(36 字节)
	buf = EmitLdrXtFromXnDisp(buf, 0, 26, uint16(bReg)*8)
	buf = EmitMovXdImm64(buf, 1, payloadMaskArm64)
	buf = EmitAndXdXnXm(buf, 0, 0, 1)
	buf = EmitMovXdFromXn(buf, 1, 0)
	buf = EmitLdrXtFromXnDisp(buf, 14, 27, arenaBaseOff)
	buf = EmitAddXdXnXm(buf, 2, 14, 1)

	// 12-13. load word5 + LSR 32(gen 在高 32 位)
	buf = EmitLdrXtFromXnDisp(buf, 0, 2, 40)
	buf = EmitLsrXdImm6(buf, 0, 0, 32)

	// 14-16. gen check + B.NE deopt
	buf = EmitMovXdImm64(buf, 3, uint64(stableShape))
	buf = EmitCmpXnXm(buf, 0, 3)
	bNeShapeOff := len(buf)
	buf = EmitBCond(buf, CondNE, 0)

	// 17-19. **NodeHit 分流**:load nodeRef(word3, offset 24)+ 新 SIB base
	buf = EmitLdrXtFromXnDisp(buf, 0, 2, 24)
	buf = EmitMovXdFromXn(buf, 1, 0)
	buf = EmitAddXdXnXm(buf, 2, 14, 1)

	// 20-23. NodeKey load + stableKey 比对 + B.NE deopt
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

	// 31. patch B.cond imm19 = (deoptStart - 本 B.cond 自身) / 4
	patchBCondImm19(buf, bNeTagOff, int32(deoptStart-bNeTagOff)/4)
	patchBCondImm19(buf, bNeShapeOff, int32(deoptStart-bNeShapeOff)/4)
	patchBCondImm19(buf, bNeKeyOff, int32(deoptStart-bNeKeyOff)/4)
	patchBCondImm19(buf, bEqNilOff, int32(deoptStart-bEqNilOff)/4)

	return buf
}

// EncodedGetTableNodeHitArm64Len arm64 PJ4 IC NodeHit 模板字节数(196)。
const EncodedGetTableNodeHitArm64Len = 196

// EmitSetTableArrayHitArm64 拼接 arm64 PJ4 IC SETTABLE ArrayHit 字节级
// inline 反向写模板(对位 amd64 EmitSetTableArrayHit 113 字节版的 arm64
// 端镜像)。
//
// **形态**:`function(t, v) t[K] = v end`,K 是 array 段命中的数字常量
// (luac 编 SETTABLE A B C 中 A=R(t) / B=K idx>=256 / C=R(v))。IC[0].Kind
// = ArrayHit + Shape/Index 命中时,字节级 inline 反向写 array[stableIndex]。
//
// **字节布局**(144 字节,GETTABLE ArrayHit 168 减去 nil check 段 24 后
// 加反向 store 段 0;实际省 LDR/MOV/CMP/B.EQ nil 段 24 字节,但补 load
// R(C) value + 反向 store 8 字节,净 -16 → 比 GETTABLE ArrayHit 短 24
// 字节最终 168 - 24 = 144):
//
//	[ 0-31] IsTable guard                          ; 32(同 GETTABLE ArrayHit)
//	[32-67] re-load + payloadMask + AND + SIB base ; 36
//	[68-99] word5 + LSR + stableShape + CMP + B.NE ; 32(gen check)
//	[100-103] LDR x0, [x2, #16]                    ; 4(table.arrayRef)
//	[104-107] MOV x1, x0                           ; 4(rcx = arrayRef offset)
//	[108-111] ADD x2, x14, x1                      ; 4(SIB 替代:base for array)
//	[112-115] LDR x3, [x26 + C*8]                  ; 4(load R(C) value → x3)
//	[116-119] STR x3, [x2, #stableIndex*8]         ; 4(反向 store array[idx])
//	[120-123] RET                                  ; 4(setter 无 R(A) 写)
//	[124-139] MOV x0, deoptCode imm64              ; 16(deopt block)
//	[140-143] RET                                  ; 4
//	——— 总计 144 字节(amd64 113 + arm64 MOV imm64/SIB 差异约 31 字节)———
//
// **设计简化**(承 amd64 SETTABLE ArrayHit 同款工程边界):
//   - **不验现有 array[stableIndex] != nil**(防新键路径)— P1 解释器 IC
//     命中协议本身要求该位非 nil
//   - **假设无 __newindex 元表**(meta freeze 假设)
//
// 严密版(再加 ~13 字节验现有 nil + 13 字节验 __newindex)留 PJ4+。
//
// **预设条件**(承 06 §4.2 arm64 trampoline 留 PJ8+):
//   - x26 = valueStackBase / x27 = jitContext / x14 = arena base
//   - x0/x1/x2/x3 = scratch
//
// **deopt 路径**:Run 端 x0==deoptCode 时调 host.SetTable byte-equal P1
// (P1 icSetTable 兼容 ArrayHit + NodeHit)。setter 形态返 RETURN A 1,
// Run 端 retB=1 不读 R(A)。
func EmitSetTableArrayHitArm64(buf []byte, aReg, cReg uint8,
	stableShape, stableIndex uint32,
	arenaBaseOff uint16, deoptCode uint64) []byte {
	// 1-5. 严密 IsTable guard(32 字节)
	buf = EmitLdrXtFromXnDisp(buf, 0, 26, uint16(aReg)*8)
	buf = EmitLsrXdImm6(buf, 0, 0, 48)
	buf = EmitMovXdImm64(buf, 1, qNanBoxTableTagShiftedArm64)
	buf = EmitCmpXnXm(buf, 0, 1)
	bNeTagOff := len(buf)
	buf = EmitBCond(buf, CondNE, 0)

	// 6-11. re-load + GCRef extract + arena base + ADD x2 SIB(36 字节)
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

	// 17-19. load arrayRef(word2, offset 16)+ 新 SIB base for array
	buf = EmitLdrXtFromXnDisp(buf, 0, 2, 16)
	buf = EmitMovXdFromXn(buf, 1, 0)
	buf = EmitAddXdXnXm(buf, 2, 14, 1)

	// 20. **setter 分流**:load R(C) value → x3(用 x3 避开 x0 复用)
	buf = EmitLdrXtFromXnDisp(buf, 3, 26, uint16(cReg)*8)

	// 21. 反向 store:STR x3, [x2, #stableIndex*8]
	buf = EmitStrXtToXnDisp(buf, 3, 2, uint16(stableIndex*8))

	// 22. RET(setter 无 R(A) 写)
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

// EncodedSetTableArrayHitArm64Len arm64 PJ4 SETTABLE ArrayHit 模板字节数(144)。
const EncodedSetTableArrayHitArm64Len = 144

// EmitSetTableNodeHitArm64 拼接 arm64 PJ4 IC SETTABLE NodeHit 字节级
// inline 反向写模板(对位 amd64 EmitSetTableNodeHit 140 字节版的 arm64
// 端镜像)。
//
// **形态**:`function(t, v) t[K] = v end`,K 是字符串/任意键 in hash 段。
// IC[0].Kind=NodeHit + Shape/Index/Key 命中时,字节级 inline 反向写
// node[stableIndex].val。
//
// **vs SETTABLE ArrayHit / GETTABLE NodeHit 复合差异**:
//   - 比 SETTABLE ArrayHit:取 word3=nodeRef(offset 24)而非 word2=arrayRef
//     (offset 16),node 步长 24 字节,多 key 比对段
//   - 比 GETTABLE NodeHit:删 NodeVal load + nil check + STR R(A),换 LDR
//     R(C) value + 反向 STR NodeVal
//
// **字节布局**(172 字节,GETTABLE NodeHit 196 - NodeVal/nil/storeRA 24 +
// store value 0;实际 GETTABLE NodeHit 24 + STR R(A) 4 = 28,SET 段 LDR + STR
// = 8,净 -20 → 196 - 24 = 172):
//
//	[ 0-139] 同 GETTABLE NodeHit 至 B.NE key(IsTable + GCRef + SIB +
//	         gen check + nodeRef + NodeKey + stableKey + CMP + B.NE)= 140
//	[140-143] LDR x3, [x26 + C*8]              ; 4(setter:load R(C) value → x3)
//	[144-147] STR x3, [x2, #stableIndex*24+8]  ; 4(反向 store NodeVal)
//	[148-151] RET                              ; 4(setter 无 R(A) 写)
//	[152-167] MOV x0, deoptCode imm64          ; 16(deopt block)
//	[168-171] RET                              ; 4
//	——— 总计 172 字节 ———
//
// **设计简化**(承 amd64 SETTABLE NodeHit / ArrayHit 同款工程边界):
//   - 不验现有 NodeVal != nil(防新键路径)
//   - 假设无 __newindex 元表
//
// **deopt 路径**:Run 端 x0==deoptCode 时调 host.SetTable byte-equal P1
// (P1 icSetTable 兼容 NodeHit;经 IC + 哈希 + __newindex 元方法链)。
// setter 形态 retB=1,Run 端 DoReturn 不读 R(A)。
//
// **预设条件**(承 06 §4.2):x26/x27/x14/x0-x3 同 GETTABLE NodeHit。
func EmitSetTableNodeHitArm64(buf []byte, aReg, cReg uint8,
	stableShape, stableIndex uint32, stableKey uint64,
	arenaBaseOff uint16, deoptCode uint64) []byte {
	// 1-5. 严密 IsTable guard
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

	// 17-19. **NodeHit 分流**:load nodeRef + 新 SIB base for node
	buf = EmitLdrXtFromXnDisp(buf, 0, 2, 24)
	buf = EmitMovXdFromXn(buf, 1, 0)
	buf = EmitAddXdXnXm(buf, 2, 14, 1)

	// 20-23. NodeKey load + stableKey 比对 + B.NE deopt
	buf = EmitLdrXtFromXnDisp(buf, 0, 2, uint16(stableIndex*24))
	buf = EmitMovXdImm64(buf, 3, stableKey)
	buf = EmitCmpXnXm(buf, 0, 3)
	bNeKeyOff := len(buf)
	buf = EmitBCond(buf, CondNE, 0)

	// 24-25. **setter 分流**:LDR R(C) value → x3 + 反向 STR NodeVal
	buf = EmitLdrXtFromXnDisp(buf, 3, 26, uint16(cReg)*8)
	buf = EmitStrXtToXnDisp(buf, 3, 2, uint16(stableIndex*24+8))

	// 26. RET(setter 无 R(A) 写)
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

// EncodedSetTableNodeHitArm64Len arm64 PJ4 SETTABLE NodeHit 模板字节数(172)。
const EncodedSetTableNodeHitArm64Len = 172

// EmitSelfArrayHitArm64 拼接 arm64 PJ4 IC SELF ArrayHit 字节级 inline 模板
// (对位 amd64 EmitSelfArrayHit 139 字节版的 arm64 端镜像)。
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
// 形态有效);常见是 NodeHit(字符串键 method name)。
//
// **字节布局**(172 字节,ArrayHit 168 + R(A+1) 拷段 4):
//
//	[ 0-3 ] LDR x0, [x26 + B*8]          ; 4(load R(B) obj NaN-box)
//	[ 4-7 ] STR x0, [x26 + (A+1)*8]      ; 4(**SELF 额外**:store R(A+1) = obj)
//	[ 8-35] IsTable guard(LSR + MOV imm64 + CMP + B.NE)  ; 28(入口 LDR 已用)
//	[36-71] re-load + payloadMask + AND + SIB ; 36
//	[72-103] word5 + LSR + stableShape + CMP + B.NE ; 32(gen check)
//	[104-107] LDR x0, [x2, #16]          ; 4(table.arrayRef)
//	[108-111] MOV x1, x0                  ; 4
//	[112-115] ADD x2, x14, x1             ; 4(SIB base for array)
//	[116-119] LDR x0, [x2, #stableIndex*8]; 4(array[stableIndex])
//	[120-135] MOV x3, qNanBoxNil          ; 16
//	[136-139] CMP x0, x3                  ; 4
//	[140-143] B.EQ deopt                  ; 4
//	[144-147] STR x0, [x26 + A*8]         ; 4(store R(A) method)
//	[148-151] RET                         ; 4
//	[152-171] MOV x0, deoptCode + RET     ; 20(deopt block)
//	——— 总计 172 字节 ———
//
// **SELF 设计要点**(承 amd64 SELF ArrayHit 同款):
//   - R(A+1) 在 IsTable guard **前** 就写,因为 deopt 走 host.GetTable 也需
//     要 R(A+1) 已设(P1 SELF case 同款步骤:setReg(A+1, B) → icGetTable →
//     setReg(A));若 deopt 时 R(A+1) 未设会破坏 byte-equal P1 约束
//   - LDR x0 入口顺序保留:先 load → 立即 STR R(A+1) → 然后 LSR(LSR
//     破坏 rax 但 R(A+1) 已 store 完毕)
//
// **deopt 路径**:Run 端 x0==deoptCode 时调 host.GetTable byte-equal P1
// (R(A+1)=R(B) 已 store;P1 icGetTable 兼容 ArrayHit + NodeHit)。
//
// **预设条件**(承 06 §4.2):x26/x27/x14/x0-x3 同 GETTABLE ArrayHit。
func EmitSelfArrayHitArm64(buf []byte, aReg, bReg uint8,
	stableShape, stableIndex uint32,
	arenaBaseOff uint16, deoptCode uint64) []byte {
	// 1. LDR x0, [x26+B*8](load R(B) obj)
	buf = EmitLdrXtFromXnDisp(buf, 0, 26, uint16(bReg)*8)

	// 2. **SELF 额外**:STR x0, [x26+(A+1)*8](self/this 实参)
	buf = EmitStrXtToXnDisp(buf, 0, 26, uint16(aReg+1)*8)

	// 3-7. 严密 IsTable guard
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

	// 26. STR x0, [x26+A*8](store R(A) = method 函数)
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

// EncodedSelfArrayHitArm64Len arm64 PJ4 SELF ArrayHit 模板字节数(172)。
const EncodedSelfArrayHitArm64Len = 172

// EmitSelfNodeHitArm64 拼接 arm64 PJ4 IC SELF NodeHit 字节级 inline 模板
// (对位 amd64 EmitSelfNodeHit 166 字节版的 arm64 端镜像)。
//
// **SELF opcode 语义**(承 bytecode/opcode.go::SELF):
//
//	R(A+1) := R(B)
//	R(A)   := R(B)[RK(C)]
//
// **NodeHit vs ArrayHit**:NodeHit 走 node 链(字符串键 method name 是
// 常见 luac 形态;数字键 obj:1() 也走 NodeHit 若 hash 段命中),命中
// 条件多一个 NodeKey == stableKey 比对。
//
// **字节布局**(200 字节,NodeHit 196 + R(A+1) 拷段 4):
//
//	[ 0-3 ] LDR x0, [x26 + B*8]              ; 4(load R(B) obj)
//	[ 4-7 ] STR x0, [x26 + (A+1)*8]          ; 4(**SELF 第一步**:R(A+1)=obj)
//	[ 8-11] LSR x0, x0, #48                  ; 4(IsTable shift)
//	[12-27] MOV x1, 0xFFFC imm64              ; 16
//	[28-31] CMP x0, x1                        ; 4
//	[32-35] B.NE deopt                        ; 4
//	[36-71] re-load + payloadMask + AND + SIB ; 36
//	[72-103] word5 + LSR + stableShape + CMP + B.NE ; 32(gen check)
//	[104-107] LDR x0, [x2, #24]              ; 4(**nodeRef** word3)
//	[108-111] MOV x1, x0                      ; 4
//	[112-115] ADD x2, x14, x1                 ; 4(SIB base for node)
//	[116-119] LDR x0, [x2, #stableIndex*24]   ; 4(NodeKey)
//	[120-135] MOV x3, stableKey imm64         ; 16
//	[136-139] CMP x0, x3                      ; 4
//	[140-143] B.NE deopt                      ; 4(NodeKey != stableKey)
//	[144-147] LDR x0, [x2, #stableIndex*24+8] ; 4(NodeVal)
//	[148-163] MOV x3, qNanBoxNil imm64        ; 16
//	[164-167] CMP x0, x3                      ; 4
//	[168-171] B.EQ deopt                      ; 4(NodeVal == Nil)
//	[172-175] STR x0, [x26 + A*8]             ; 4(store R(A) = method)
//	[176-179] RET                             ; 4
//	[180-195] MOV x0, deoptCode imm64         ; 16(deopt block)
//	[196-199] RET                             ; 4
//	——— 总计 200 字节 ———
//
// **SELF 设计要点**(承 SELF ArrayHit 同款 + amd64 SELF NodeHit):
//   - R(A+1) 在 IsTable guard **前** 就写(byte-equal P1 SELF case 同款步骤)
//   - 后续 NodeHit 流程完整复用 EmitGetTableNodeHitArm64 模式,但
//     头部 LDR 已合并到 SELF 入口,不再重复 LDR
//
// **deopt 路径**:Run 端 x0==deoptCode 时调 host.GetTable byte-equal P1
// (R(A+1)=R(B) 已 store;P1 icGetTable 兼容 NodeHit + __index 链)。
//
// **预设条件**(承 06 §4.2):x26/x27/x14/x0-x3 同 NodeHit。
func EmitSelfNodeHitArm64(buf []byte, aReg, bReg uint8,
	stableShape, stableIndex uint32, stableKey uint64,
	arenaBaseOff uint16, deoptCode uint64) []byte {
	// 1. LDR x0, [x26+B*8](load R(B) obj)
	buf = EmitLdrXtFromXnDisp(buf, 0, 26, uint16(bReg)*8)

	// 2. **SELF 第一步**:STR x0, [x26+(A+1)*8](self/this 实参)
	buf = EmitStrXtToXnDisp(buf, 0, 26, uint16(aReg+1)*8)

	// 3-6. IsTable guard(LSR + MOV + CMP + B.NE 共 28 字节;
	//      入口 LDR 已合并到 SELF 第 1 步,不重复)
	buf = EmitLsrXdImm6(buf, 0, 0, 48)
	buf = EmitMovXdImm64(buf, 1, qNanBoxTableTagShiftedArm64)
	buf = EmitCmpXnXm(buf, 0, 1)
	bNeTagOff := len(buf)
	buf = EmitBCond(buf, CondNE, 0)

	// 7-12. re-load + GCRef extract + arena base + ADD x2 SIB(36 字节)
	buf = EmitLdrXtFromXnDisp(buf, 0, 26, uint16(bReg)*8)
	buf = EmitMovXdImm64(buf, 1, payloadMaskArm64)
	buf = EmitAndXdXnXm(buf, 0, 0, 1)
	buf = EmitMovXdFromXn(buf, 1, 0)
	buf = EmitLdrXtFromXnDisp(buf, 14, 27, arenaBaseOff)
	buf = EmitAddXdXnXm(buf, 2, 14, 1)

	// 13-17. word5 + LSR + stableShape + CMP + B.NE(32 字节)
	buf = EmitLdrXtFromXnDisp(buf, 0, 2, 40)
	buf = EmitLsrXdImm6(buf, 0, 0, 32)
	buf = EmitMovXdImm64(buf, 3, uint64(stableShape))
	buf = EmitCmpXnXm(buf, 0, 3)
	bNeShapeOff := len(buf)
	buf = EmitBCond(buf, CondNE, 0)

	// 18-20. **NodeHit 分流**:load nodeRef(word3, offset 24)+ 新 SIB base
	buf = EmitLdrXtFromXnDisp(buf, 0, 2, 24)
	buf = EmitMovXdFromXn(buf, 1, 0)
	buf = EmitAddXdXnXm(buf, 2, 14, 1)

	// 21-24. NodeKey load + stableKey 比对 + B.NE deopt
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

	// 32. patch B.cond imm19 = (deoptStart - 本 B.cond 自身) / 4
	patchBCondImm19(buf, bNeTagOff, int32(deoptStart-bNeTagOff)/4)
	patchBCondImm19(buf, bNeShapeOff, int32(deoptStart-bNeShapeOff)/4)
	patchBCondImm19(buf, bNeKeyOff, int32(deoptStart-bNeKeyOff)/4)
	patchBCondImm19(buf, bEqNilOff, int32(deoptStart-bEqNilOff)/4)

	return buf
}

// EncodedSelfNodeHitArm64Len arm64 PJ4 SELF NodeHit 模板字节数(200)。
const EncodedSelfNodeHitArm64Len = 200

// EmitSpecArgLoadKArm64 写 R(dstReg) = K(NaN-box u64)— PJ5 SELF spec
// template args/recv 装载字节级 inline(arm64 端 amd64 EmitSpecArgLoadK 对位)。
//
// **vsBase 寄存器**:arm64 spec template 用 x26(承 trampoline_arm64.s::callJITSpec)。
//
// 字节序列(movz/movk×4 + str = 5 条 = 20 字节,K 装到 x0 再 str):
//
//	movz/movk x0, K_imm64  ; 4 条(每段 16 bit)
//	str x0, [x26 + dstReg*8] ; 1 条
func EmitSpecArgLoadKArm64(buf []byte, dstReg uint8, k uint64) []byte {
	// 用 x0 作 scratch 临时寄存器(spec template 内不持长效 x0)
	buf = EmitMovXdImm64(buf, 0, k)
	// vsBase 在 x26 → STR x0, [x26 + dstReg*8]
	buf = EmitStrXtToXnDisp(buf, 0, 26, uint16(dstReg)*8)
	return buf
}

// EmitSpecArgLoadRegArm64 写 R(dstReg) = R(srcReg)。
//
// 字节序列(LDR + STR = 2 条 = 8 字节):
//
//	ldr x0, [x26 + srcReg*8]
//	str x0, [x26 + dstReg*8]
func EmitSpecArgLoadRegArm64(buf []byte, dstReg uint8, srcReg uint8) []byte {
	buf = EmitLdrXtFromXnDisp(buf, 0, 26, uint16(srcReg)*8)
	buf = EmitStrXtToXnDisp(buf, 0, 26, uint16(dstReg)*8)
	return buf
}

// EmitFrameInlineCIDepthIncArm64 发射 arm64 ciDepth++ 字节级 inline 模板
// (承 §9.20 Option B Spike 1 amd64 对位)。
//
// arm64 不像 amd64 有 `inc [mem]` 单指令,需 LDR + ADD + STR 三步。
//
// 字节序列(4 条 = 16 字节):
//
//	ldr  x16, [x27 + ciDepthAddrOffset]  ; x16 = ciDepthAddr(host 字节地址)
//	ldr  x17, [x16]                       ; x17 = *ciDepthAddr(当前 ciDepth)
//	add  x17, x17, #1                     ; x17++
//	str  x17, [x16]                       ; *ciDepthAddr = x17
//
// x16/x17 是 ARMv8 IP0/IP1 scratch 寄存器(intra-procedure-call scratch,
// callee 可任意改写),caller 不需保存。x27 = jitContext(承 06 §4.2)。
//
// 比 amd64 (10 字节)多 6 字节:RISC fixed-length 必须 3 条独立指令 +
// LDR pimm12 vs amd64 `inc [rax]` 复合寻址。
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

// EmitFrameInlineCIDepthDecArm64 发射 arm64 ciDepth-- 字节级 inline 模板
// (承 §9.20 popCallInfo 反向,amd64 对位)。
//
// arm64 没有 SUB Xd, Xn, #imm12 的 byte-equal 简形态——用 EmitSubXdImm12
// 通用宏。同 Inc 4 条指令 16 字节(LDR + LDR + SUB + STR)。
func EmitFrameInlineCIDepthDecArm64(buf []byte, ciDepthAddrOffset uint16) []byte {
	buf = EmitLdrXtFromXnDisp(buf, 16, 27, ciDepthAddrOffset)
	buf = EmitLdrXtFromXnDisp(buf, 17, 16, 0)
	// SUB x17, x17, #1 — 复用 ADD 编码(SUB = 1010001 vs ADD = 1001000),
	// 但 ADD imm12 已封装,用 ADD +(-1)语义不可直接;实际 SUB imm12 编码
	// 略不同(opcode bit 30)。下方直接拼字节。
	// SUB Xd, Xn, #imm12,shift=0:0xD1000000 + (imm12<<10) + (Rn<<5) + Rd
	// = 0xD1000000 + (1<<10) + (17<<5) + 17 = 0xD1000631
	buf = append(buf,
		0x31, 0x06, 0x00, 0xD1) // SUB x17, x17, #1(little-endian arm64 encoding)
	buf = EmitStrXtToXnDisp(buf, 17, 16, 0)
	return buf
}

// EncodedFrameInlineCIDepthIncDecArm64Len 是 arm64 ciDepth++/-- 字节级
// inline 模板字节数(16,对位 amd64 = 10)。arm64 多 6 字节因 RISC fixed-
// length 必须 3 条独立指令 + LDR pimm12 vs amd64 复合寻址。
const EncodedFrameInlineCIDepthIncDecArm64Len = 16

// EmitFrameInlineLoadCISlotAddrArm64 发射 arm64 CI 段第 depth 帧地址加载
// 到 X0 模板(承 §9.20 Option B Spike 1 amd64 对位)。
//
// 字节序列(7 条 = 28 字节):
//
//	ldr  x16, [x27 + ciDepthAddrOffset]    ; x16 = ciDepthAddr
//	ldr  x17, [x16]                         ; x17 = depth(当前)
//	ldr  x16, [x27 + ciSegBaseAddrOffset]   ; x16 = ciSegBaseAddr
//	ldr  x16, [x16]                         ; x16 = ciSegBase
//	mov  x18, #40                           ; x18 = 40 (ciSlotBytes)
//	mul  x17, x17, x18                      ; x17 = depth * 40
//	add  x0, x16, x17                       ; x0 = ciSegBase + depth*40
//
// 模板结束后 x0 = CallInfo[depth] 字节地址(等价 amd64 rax)。
//
// arm64 28 字节 vs amd64 30 字节——arm64 微优因 LDR/STR pimm12 + MUL R-type
// 比 amd64 disp32 mov + imul 编码紧凑 2 字节。x16/x17/x18 是 IP0/IP1/IP2
// scratch 寄存器(intra-procedure-call scratch)。
func EmitFrameInlineLoadCISlotAddrArm64(buf []byte, ciDepthAddrOffset, ciSegBaseAddrOffset uint16) []byte {
	// 1. x16 = ciDepthAddr → x17 = depth
	buf = EmitLdrXtFromXnDisp(buf, 16, 27, ciDepthAddrOffset)
	buf = EmitLdrXtFromXnDisp(buf, 17, 16, 0)
	// 2. x16 = ciSegBaseAddr → x16 = ciSegBase(覆写 x16)
	buf = EmitLdrXtFromXnDisp(buf, 16, 27, ciSegBaseAddrOffset)
	buf = EmitLdrXtFromXnDisp(buf, 16, 16, 0)
	// 3. mov x18, #40
	buf = EmitMovXdImm64(buf, 18, 40)
	// 等等,EmitMovXdImm64 是 16 字节 movz+movk*3。我们只要 movz 单条
	// 但 EmitMovXdImm64 不区分 imm 大小,小 imm 用 4 movz/movk 浪费 12 字节。
	// Spike 1 简化:接受 16 字节 imm 装载,后续优化 PJ8+ 再用 EmitMovzXd 单条。
	// 4. mul x17, x17, x18
	buf = EmitMulXdXnXm(buf, 17, 17, 18)
	// 5. add x0, x16, x17
	buf = EmitAddXdXnXm(buf, 0, 16, 17)
	return buf
}

// EncodedFrameInlineLoadCISlotAddrArm64Len 是 arm64 CI 段第 depth 帧地址
// 加载模板字节数(4*4 + 16 + 4 + 4 = 40 字节,对位 amd64 = 30)。
//
// arm64 40 字节比 amd64 30 字节多 10 字节,因 EmitMovXdImm64(16 字节
// movz+movk*3)即使装 #40 小常量也走 4 条 16-bit 段——未来优化用
// EmitMovzXd 单条 4 字节(留 PJ8+ 通用优化批次)。
const EncodedFrameInlineLoadCISlotAddrArm64Len = 40

// EmitFrameInlineWriteCIWordArm64 发射 arm64 CI 帧 word_idx 写入 imm64
// 模板(承 §9.20 Option B Spike 1 amd64 对位)。
//
// 调用契约:x0 必须已装 CallInfo[depth] 帧起点字节地址(承
// EmitFrameInlineLoadCISlotAddrArm64);word_idx 范围 [0,4](承 ciWords=5)。
//
// 字节序列(16 + 4 = 20 字节):
//
//	mov  x16, imm64                      ; 16 字节(EmitMovXdImm64 movz+movk*3)
//	str  x16, [x0 + word_idx*8]          ; 4 字节(EmitStrXtToXnDisp pimm12)
//
// arm64 20 字节 vs amd64 14 字节,多 6 字节因 EmitMovXdImm64 总走 4 条
// 16-bit 段(无视 imm 大小);amd64 mov rcx imm64 10 字节单条。
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
