//go:build wangshu_p4 && arm64

// pj3_template.go —— PJ8 arm64 PJ3 FORLOOP 空 body 字节级模板(对位
// amd64 pj3_template.go::EmitForLoopEmptyConst 69/83 字节 SSE2 版的 arm64
// 端镜像)。
//
// **真接入状态**:本批 archEmitForLoopEmptyConst stub→ 真代理已落地,
// Compile 主路径经 callJITFull 可调本模板;真 mmap+RX 端到端测试留物理
// self-hosted runner(arm64 trampoline asm 已就绪 trampoline_arm64.s)。
//
// **arm64 vs amd64 PJ3 模板对位**:
//   - amd64 EmitForLoopEmptyConst 69/83 字节(无/含 safepoint):
//     mov+movq×3(15*3=45)+ subsd+addsd+ucomisd+ja+jmp+ret(4+4+4+6+5+1=24)
//     + safepoint(cmp 8 + jne 6 = 14)
//   - arm64 84/92 字节(无/含 safepoint):
//     mov+fmov×3(20*3=60)+ fsub+fadd+fcmpe+b.cond+b+ret(4*6=24)
//     + safepoint(ldrb 4 + cbnz 4 = 8)
//
// 字节布局图(arm64,无 safepoint 形态):
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
// 含 safepoint 形态(preemptFlagOff>=0,92 字节):loop_start 后 fadd 前
// 插 ldrb w0,[x27+pfOff] 4 + cbnz w0,after_loop 4 = 8 字节,对位 amd64
// safepoint check 14 字节但节省 6 字节(RISC fixed-length 紧凑)。

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

// EmitForLoopRegLimitArm64 拼接「init/step 常量 + limit 是 reg + 空 body
// FORLOOP」arm64 模板(对位 amd64 EmitForLoopRegLimit 103/117 字节,arm64
// 因 MOV imm64 序列与 RISC fixed-length 累积长)。
//
// 字节布局(以 step>0 + safepoint 启用为例,128 字节):
//
//	[ 0-3 ] LDR x0, [x26+limitReg*8]      ; 4(load R(limitReg))
//	[ 4-19] MOV x1, qNanBoxBase imm64     ; 16
//	[20-23] CMP x0, x1                    ; 4
//	[24-27] B.HS deopt                    ; 4(若 R(limitReg) >= qNanBoxBase 非 number,deopt)
//	[28-43] MOV x0, K_init imm64           ; 16
//	[44-47] FMOV d0, x0                    ; 4
//	[48-51] LDR x0, [x26+limitReg*8]       ; 4(再 load limit,作为 f64 bits)
//	[52-55] FMOV d1, x0                    ; 4
//	[56-71] MOV x0, K_step imm64           ; 16
//	[72-75] FMOV d2, x0                    ; 4
//	[76-79] FSUB d0, d0, d2                ; 4(FORPREP 预减)
//	[80-83] ; loop_start
//	[80-83] FADD d0, d0, d2                ; 4
//	[84-87] FCMPE d0, d1                   ; 4
//	[88-91] B.GT after_loop                ; 4(forward)
//	[92-95] LDRB W0, [x27+pfOff]           ; 4(safepoint)
//	[96-99] CBNZ W0, after_loop            ; 4
//	[100-103] B loop_start backward        ; 4
//	[104-107] ; after_loop
//	[104-107] RET                          ; 4
//	[108-123] MOV x0, deoptCode imm64      ; 16(deopt block)
//	[124-127] RET                          ; 4
//	——— 含 safepoint:128 字节 ———
//	——— 无 safepoint(preemptFlagOff<0,省 8 字节):120 字节 ———
//
// **预设条件**:
//   - x26 = valueStackBase(承 06 §4.2 trampoline 装)
//   - x27 = jitContext(safepoint check 用)
//   - limitReg ∈ [0, 254]
//
// **deopt 路径**(byte-equal P1):本模板不写 R(A) idx 槽(空 body 形态),
// deopt 时直接返 deoptCode,caller 经 Run 路径降级调 host(同款 amd64)。
func EmitForLoopRegLimitArm64(buf []byte, kInit, kStep uint64,
	limitReg uint8, deoptCode uint64, preemptFlagOff int32) []byte {
	// guard:LDR R(limitReg) → MOV qNanBoxBase → CMP → B.HS deopt
	buf = EmitLdrXtFromXnDisp(buf, 0, 26, uint16(limitReg)*8)
	buf = EmitMovXdImm64(buf, 1, qNanBoxBase)
	buf = EmitCmpXnXm(buf, 0, 1)
	bHsDeoptOff := len(buf)
	buf = EmitBCond(buf, CondHS, 0) // placeholder imm19=0

	// 装 init/limit/step
	buf = EmitMovXdImm64(buf, 0, kInit)
	buf = EmitFmovDdFromXn(buf, 0, 0)

	buf = EmitLdrXtFromXnDisp(buf, 0, 26, uint16(limitReg)*8)
	buf = EmitFmovDdFromXn(buf, 1, 0)

	buf = EmitMovXdImm64(buf, 0, kStep)
	buf = EmitFmovDdFromXn(buf, 2, 0)

	// FORPREP 预减
	buf = EmitFsubDdDnDm(buf, 0, 0, 2)

	// loop_start label
	loopStart := len(buf)

	// FORLOOP idx+=step
	buf = EmitFaddDdDnDm(buf, 0, 0, 2)
	buf = EmitFcmpeDnDm(buf, 0, 1)

	// b.gt after_loop placeholder
	bGtOff := len(buf)
	buf = EmitBCond(buf, CondGT, 0)

	// (可选)safepoint check
	var safepointCbnzOff int = -1
	if preemptFlagOff >= 0 {
		buf = EmitLdrbWtFromXnDisp(buf, 0, 27, uint16(preemptFlagOff))
		safepointCbnzOff = len(buf)
		buf = EmitCbnzW(buf, 0, 0) // placeholder imm19=0
	}

	// b loop_start backward
	bLoopOff := len(buf)
	imm26 := int32(loopStart-bLoopOff) / 4
	buf = EmitB(buf, imm26)

	// after_loop label
	afterLoopOff := len(buf)

	// ret
	buf = EmitRet(buf)

	// deopt block
	deoptStart := len(buf)
	buf = EmitMovXdImm64(buf, 0, deoptCode)
	buf = EmitRet(buf)

	// patch B.GT forward(target = after_loop)
	patchBCondImm19(buf, bGtOff, int32(afterLoopOff-bGtOff)/4)

	// patch safepoint CBNZ forward(target = after_loop)
	if safepointCbnzOff >= 0 {
		patchCbnzImm19(buf, safepointCbnzOff, int32(afterLoopOff-safepointCbnzOff)/4)
	}

	// patch B.HS deopt(target = deopt_block start)
	patchBCondImm19(buf, bHsDeoptOff, int32(deoptStart-bHsDeoptOff)/4)

	return buf
}

// EncodedForLoopRegLimitArm64NoSafepointLen 无 safepoint 形态字节数(120)。
const EncodedForLoopRegLimitArm64NoSafepointLen = 120

// EncodedForLoopRegLimitArm64WithSafepointLen 含 safepoint 形态字节数(128)。
const EncodedForLoopRegLimitArm64WithSafepointLen = 128
