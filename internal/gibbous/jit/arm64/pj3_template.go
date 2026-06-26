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

// EncodedForLoopEmptyConstArm64Len arm64 PJ3 FORLOOP 空 body 模板字节数
// (84 字节 = mov+fmov×3 60 + fsub 4 + fadd 4 + fcmpe 4 + b.cond 4 + b 4
// + ret 4)。
const EncodedForLoopEmptyConstArm64Len = 3*(EncodedMovXdImm64Len+EncodedFmovDdFromXnLen) +
	EncodedFsubDdDnDmLen + EncodedFaddDdDnDmLen + EncodedFcmpeDnDmLen +
	EncodedBCondLen + EncodedBLen + EncodedRetLen
