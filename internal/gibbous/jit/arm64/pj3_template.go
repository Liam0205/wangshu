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
// FORLOOP 字节级模板」(对位 amd64 EmitForLoopEmptyConst,含可选 safepoint
// check 段)。
//
// 参数:
//   - kInit / kLimit / kStep:三个常量的 NaN-box raw bits(由 caller 经
//     value.NumberValue(K).Bits() 算好,与 amd64 同款)
//   - preemptFlagOff:r27+disp 处的 preempt 字段 byte 偏移
//   - >= 0:启用 safepoint check(承 V18 -race 抢占纪律)
//   - <  0:跳过 safepoint(测试用 / 严格单段计算用例)
//
// 返回追加后的 buf。
//
// **字节布局**(84 字节无 safepoint / 92 字节含 safepoint):
//   - 无 safepoint:mov+fmov×3 60 + fsub 4 + fadd 4 + fcmpe 4 + b.gt 4
//   - b 4 + ret 4 = 84
//   - 含 safepoint:84 + ldrb 4 + cbnz 4 = 92(safepoint check 插在
//     loop_start 后、fadd 前,对位 amd64 14B safepoint;arm64 仅 8B 因
//     ldrb+cbnz 等价 cmp+jne 但 RISC 紧凑)
//
// **预设条件**:
//   - arm64 trampoline 协议(承 06 §4.2):x27=jitContext / x26=valueStackBase;
//     启用 safepoint 时读 [x27+preemptFlagOff] 一字节
//   - R(A) idx 槽不写(空 body 不需要)
//   - 返回 x0 是 dummy(段返回后 host 不读)
//
// 用例:PJ3 FORLOOP 空 body 形态 arm64 端字节级实证(对位 amd64 同款形态
// 的 7.15-25.41x 加速比留 arm64 物理 runner 实测)。
func EmitForLoopEmptyConstArm64(buf []byte, kInit, kLimit, kStep uint64, preemptFlagOff int32) []byte {
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

	// (可选)safepoint check:ldrb w0, [x27+pfOff]; cbnz w0, after_loop
	// (对位 amd64 cmp byte [r15+pfOff],0 + jne after_loop 14B;
	//  arm64 端 ldrb+cbnz 共 8B)
	var safepointCbnzOff int = -1
	if preemptFlagOff >= 0 {
		buf = EmitLdrbWtFromXnDisp(buf, 0, 27, uint16(preemptFlagOff))
		safepointCbnzOff = len(buf)
		buf = EmitCbnzW(buf, 0, 0) // placeholder imm19=0
	}

	// FORLOOP idx+=step:d0 += d2(4 字节)
	buf = EmitFaddDdDnDm(buf, 0, 0, 2)

	// cmp idx, limit:fcmpe d0, d1(4 字节,signaling ordered)
	buf = EmitFcmpeDnDm(buf, 0, 1)

	// b.gt after_loop placeholder(forward,fixup 后)
	bGtOff := len(buf)
	buf = EmitBCond(buf, CondGT, 0) // placeholder imm19=0

	// b loop_start backward(4 字节)
	bLoopOff := len(buf)
	imm26 := int32(loopStart-bLoopOff) / 4
	buf = EmitB(buf, imm26)

	// after_loop label
	afterLoopOff := len(buf)

	// ret(4 字节)
	buf = EmitRet(buf)

	// patch b.gt imm19 = (after_loop - b.gt 自身位置) / 4 字数偏移
	imm19BGt := int32(afterLoopOff-bGtOff) / 4
	patchBCondImm19(buf, bGtOff, imm19BGt)

	// patch safepoint cbnz forward(若启用)
	if safepointCbnzOff >= 0 {
		safepointImm19 := int32(afterLoopOff-safepointCbnzOff) / 4
		patchCbnzImm19(buf, safepointCbnzOff, safepointImm19)
	}

	return buf
}

// EncodedForLoopEmptyConstArm64Len arm64 PJ3 FORLOOP 空 body 模板字节数
// (84 字节无 safepoint / 92 字节含 safepoint;指本批 caller 关注的
// 无 safepoint 上限,含 safepoint 由 caller 经 EncodedSafepointCheckLen 加)。
const EncodedForLoopEmptyConstArm64Len = 3*(EncodedMovXdImm64Len+EncodedFmovDdFromXnLen) +
	EncodedFsubDdDnDmLen + EncodedFaddDdDnDmLen + EncodedFcmpeDnDmLen +
	EncodedBCondLen + EncodedBLen + EncodedRetLen
