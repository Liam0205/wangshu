//go:build wangshu_p4 && arm64

// pj3_template.go —— PJ8 arm64 PJ3 FORLOOP 空 body 字节级模板(对位
// amd64 pj3_template.go::EmitForLoopEmptyConst 69 字节 SSE2 版的 arm64
// 端镜像)。
//
// **不真接入**(承 §9.12 剩余工程量明示):arm64 trampoline asm + mmap+RX
// 端到端 留物理 self-hosted runner;本批仅做字节级模板拼接 + 字节级单测
// 验布局,为下一阶段真接入提供基础。
//
// **arm64 vs amd64 PJ3 模板对位**:
//   - amd64 EmitForLoopEmptyConst 69 字节(无 safepoint):
//     mov+movq×3(15*3=45)+ subsd+addsd+ucomisd+ja+jmp+ret(4+4+4+6+5+1=24)
//   - arm64 84 字节(无 safepoint):
//     mov+fmov×3(20*3=60)+ fsub+fadd+fcmpe+b.cond+b+ret(4*6=24)
//
// 字节布局图(arm64):
//
//	[ 0-15]  mov x0, K_init imm64       ; 16
//	[16-19]  fmov d0, x0                 ; 4
//	[20-35]  mov x0, K_limit imm64       ; 16
//	[36-39]  fmov d1, x0                 ; 4
//	[40-55]  mov x0, K_step imm64        ; 16
//	[56-59]  fmov d2, x0                 ; 4
//	[60-63]  fsub d0, d0, d2             ; 4(FORPREP 预减)
//	[64-67]  ; loop_start
//	[64-67]  fadd d0, d0, d2             ; 4(idx += step)
//	[68-71]  fcmpe d0, d1                ; 4(cmp idx, limit)
//	[72-75]  b.gt after_loop             ; 4(forward,idx > limit → exit)
//	[76-79]  b loop_start                ; 4(backward jmp)
//	[80-83]  ; after_loop
//	[80-83]  ret                          ; 4
//	——— 总长 84 字节(无 safepoint)———
//
// **不含 safepoint check**(arm64 端 safepoint 留 PJ8+ 与 arm64 trampoline
// 协议同批接入)。

package arm64

// EmitForLoopEmptyConstArm64 拼接 arm64「全常量 init/limit/step + 空 body
// FORLOOP 字节级模板」(对位 amd64 EmitForLoopEmptyConst,但本批不含
// safepoint check 段——arm64 safepoint 字节级形态留 PJ8+)。
//
// 参数:
//   - kInit / kLimit / kStep:三个常量的 NaN-box raw bits(由 caller 经
//     value.NumberValue(K).Bits() 算好,与 amd64 同款)
//
// 返回追加后的 buf。
//
// **字节布局**(84 字节,无 safepoint 版):承文件头注详细图。
//
// **预设条件**:
//   - arm64 trampoline 协议(承 06 §4.2,留 PJ8+):x27=jitContext /
//     x26=valueStackBase;但本模板纯浮点循环无值栈寻址,不读 x26/x27
//   - R(A) idx 槽不写(空 body 不需要)
//   - 返回 x0 是 dummy(段返回后 host 不读)
//
// 用例:PJ3 FORLOOP 空 body 形态 arm64 端字节级实证(对位 amd64 同款形态
// 的 7.15-25.41x 加速比留 arm64 物理 runner 实测)。
func EmitForLoopEmptyConstArm64(buf []byte, kInit, kLimit, kStep uint64) []byte {
	// 装 init/limit/step 到 d0/d1/d2(各 20 字节:mov x0 imm64 16 + fmov 4)
	buf = EmitMovXdImm64(buf, 0, kInit) // mov x0, kInit
	buf = EmitFmovDdFromXn(buf, 0, 0)   // fmov d0, x0

	buf = EmitMovXdImm64(buf, 0, kLimit) // mov x0, kLimit
	buf = EmitFmovDdFromXn(buf, 1, 0)    // fmov d1, x0

	buf = EmitMovXdImm64(buf, 0, kStep) // mov x0, kStep
	buf = EmitFmovDdFromXn(buf, 2, 0)   // fmov d2, x0

	// FORPREP 预减:d0 = init - step(4 字节)
	buf = EmitFsubDdDnDm(buf, 0, 0, 2)

	// loop_start label
	loopStart := len(buf)

	// FORLOOP idx+=step:d0 += d2(4 字节)
	buf = EmitFaddDdDnDm(buf, 0, 0, 2)

	// cmp idx, limit:fcmpe d0, d1(4 字节,signaling ordered)
	buf = EmitFcmpeDnDm(buf, 0, 1)

	// b.gt after_loop placeholder(forward,fixup 后)
	// imm19 = (after_loop - 本 b.gt 指令) / 4 = 距离 4 字节后(b 指令 + ret)
	// = 2 字节字偏移 (b + ret 共 8 字节 = 2 字)
	bGtOff := len(buf)
	buf = EmitBCond(buf, CondGT, 0) // placeholder imm19=0

	// b loop_start backward(4 字节)
	// imm26 = (loop_start - 本 b 指令) / 4 = (loopStart - len(buf)) / 4
	// = (loopStart - currentB) / 4(负值)
	bLoopOff := len(buf)
	imm26 := int32(loopStart-bLoopOff) / 4
	buf = EmitB(buf, imm26)

	// after_loop label
	afterLoopOff := len(buf)

	// ret(4 字节)
	buf = EmitRet(buf)

	// patch b.gt imm19 = (after_loop - b.gt 自身位置) / 4 字数偏移
	// (arm64 B.cond imm19 是相对本指令地址的字偏移)
	imm19BGt := int32(afterLoopOff-bGtOff) / 4
	patchBCondImm19(buf, bGtOff, imm19BGt)

	return buf
}

// patchBCondImm19 在 buf[off..off+4] 处的 B.cond 指令字内 patch imm19
// 字段(bit 5-23)。原指令字 cond 字段(bit 0-3)和 0x54 base(bit 24-31)
// 保留,只修改 imm19 19 位。
func patchBCondImm19(buf []byte, off int, imm19 int32) {
	if off+4 > len(buf) {
		return
	}
	// 读原指令字
	insn := uint32(buf[off]) | uint32(buf[off+1])<<8 |
		uint32(buf[off+2])<<16 | uint32(buf[off+3])<<24
	// 清掉 imm19 字段(bit 5-23,共 19 位 = 0x7FFFF<<5 = 0x00FFFFE0)
	insn &= 0xFF00001F
	// 写入新 imm19
	insn |= (uint32(imm19) & 0x7FFFF) << 5
	// 写回 buf(LE)
	buf[off] = byte(insn)
	buf[off+1] = byte(insn >> 8)
	buf[off+2] = byte(insn >> 16)
	buf[off+3] = byte(insn >> 24)
}

// EncodedForLoopEmptyConstArm64Len arm64 PJ3 FORLOOP 空 body 模板字节数
// (84 字节 = mov+fmov×3 60 + fsub 4 + fadd 4 + fcmpe 4 + b.cond 4 + b 4
// + ret 4)。
const EncodedForLoopEmptyConstArm64Len = 3*(EncodedMovXdImm64Len+EncodedFmovDdFromXnLen) +
	EncodedFsubDdDnDmLen + EncodedFaddDdDnDmLen + EncodedFcmpeDnDmLen +
	EncodedBCondLen + EncodedBLen + EncodedRetLen

// =============================================================================
// PJ4 IC ArrayHit arm64 字节级模板(对位 amd64 EmitGetTableArrayHit 132 字节)
// =============================================================================

// qNanBoxTableTagShifted arm64 端 NaN-box table tag 高 16 位值(对位 amd64
// 同名常量但 arm64 模板用 LSR + CMP 严密 guard,直接比较 16-bit 值)。
const qNanBoxTableTagShiftedArm64 uint64 = 0xFFFC

// qNanBoxNilImmArm64 arm64 端 NaN-box Nil 完整值(对位 amd64 qNanBoxNilImm)。
const qNanBoxNilImmArm64 uint64 = 0xFFFE_0000_0000_0000

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
//	[132-135] CMP x0, x3                  ; 4
//	[136-139] B.EQ deopt                  ; 4
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
	const payloadMask uint64 = 0x0000_FFFF_FFFF_FFFF
	buf = EmitMovXdImm64(buf, 1, payloadMask)

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
