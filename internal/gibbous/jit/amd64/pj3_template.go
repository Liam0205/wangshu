//go:build wangshu_p4 && amd64

// pj3_template.go —— P4 PJ3 字节级 FORLOOP 内联模板(承
// docs/design/p4-method-jit/05-system-pipeline.md §6.3 回边 +
// 06-backends.md §3.3 数值 for)。
//
// **PJ3 最简形态**:全常量 init/limit/step + 空 body 的 FORLOOP 内联
// (`function() for i=1,100 do end end` 类)。验证 mmap 段内自循环 +
// 浮点 idx 累加 + ucomisd limit + backward jcc 字节级可工作。
//
// **不包含**(留 PJ3 真接入扩展):
//   - body inline opcodes(需 reg-K spec 模板 inline +寄存器分配)
//   - limit 是 reg(MOVE)形态(需 IsNumber guard)
//   - 安全点检查(safepoint check)— 当前模板纯计算无副作用,可后续扩
//   - 非正 step(jcc 选 jb 而非 ja)
//   - 嵌套 / break

package amd64

// EmitForLoopEmptyConst 拼接「全常量 init/limit/step + 空 body FORLOOP
// 字节级模板」。
//
// 字节布局(承上面文件头注的设计图;preemptFlagOff < 0 时省略 safepoint check):
//
//	[ 0] mov rax, K_init_imm64     ; 10 bytes
//	[10] movq xmm0, rax             ; 5 bytes
//	[15] mov rax, K_limit_imm64    ; 10 bytes
//	[25] movq xmm1, rax             ; 5 bytes
//	[30] mov rax, K_step_imm64     ; 10 bytes
//	[40] movq xmm2, rax             ; 5 bytes
//	[45] subsd xmm0, xmm2           ; 4 bytes (FORPREP 预减:idx = init - step)
//	[49] ; loop_start
//	[49] addsd xmm0, xmm2           ; 4 bytes (FORLOOP: idx += step)
//	[53] ucomisd xmm0, xmm1         ; 4 bytes (cmp idx, limit)
//	[57] ja  after_loop             ; 6 bytes (forward jcc;rel32 = +5 / +19 含 safepoint)
//	[63] ; (optional safepoint check)
//	[63] cmp byte [r15+pfOff], 0    ; 8 bytes (仅 preemptFlagOff >= 0 时)
//	[71] jne after_loop             ; 6 bytes (forward jcc)
//	[77] jmp loop_start             ; 5 bytes (backward jmp)
//	[82] ; after_loop
//	[82] ret                        ; 1 byte
//	——— 总长 = 69 字节(无 safepoint) / 83 字节(含 safepoint) ———
//
// 参数 preemptFlagOff:r15+disp32 处的 preempt 字段 byte 偏移;
//   - >= 0:启用 safepoint check(承 V18 -race 抢占纪律)
//   - <  0:跳过 safepoint(测试用 / 严格单段计算用例)
//
// **预设条件**:trampoline 装 r15(safepoint check 读 r15);R(A) idx 槽
// 不写(空 body 不需要);返回 rax 是 dummy(段返回后 host 不读)。
func EmitForLoopEmptyConst(buf []byte, kInit, kLimit, kStep uint64, preemptFlagOff int32) []byte {
	// 装 init/limit/step 到 xmm0/xmm1/xmm2
	buf = EmitMovRaxImm64(buf, kInit) // mov rax, K_init
	buf = EmitMovqXmmFromRax(buf, 0)  // movq xmm0, rax

	buf = EmitMovRaxImm64(buf, kLimit) // mov rax, K_limit
	buf = EmitMovqXmmFromRax(buf, 1)   // movq xmm1, rax

	buf = EmitMovRaxImm64(buf, kStep) // mov rax, K_step
	buf = EmitMovqXmmFromRax(buf, 2)  // movq xmm2, rax

	// FORPREP 预减:xmm0 = init - step
	buf = EmitSubsdXmmXmm(buf, 0, 2) // subsd xmm0, xmm2

	// loop_start label
	loopStart := len(buf)

	// FORLOOP idx+=step:xmm0 += xmm2
	buf = EmitAddsdXmmXmm(buf, 0, 2) // addsd xmm0, xmm2

	// cmp idx, limit
	buf = EmitUcomisdXmmXmm(buf, 0, 1) // ucomisd xmm0, xmm1

	// ja after_loop placeholder rel32=0(forward fixup)
	buf = EmitJaRel32(buf, 0)
	jaRel32Off := len(buf) - 4

	// (可选)safepoint check:cmp byte [r15+pfOff], 0;jne after_loop
	var safepointJneRel32Off int = -1
	if preemptFlagOff >= 0 {
		buf = EmitCmpByteR15DispImm8(buf, preemptFlagOff, 0)
		buf = EmitJneRel32(buf, 0)
		safepointJneRel32Off = len(buf) - 4
	}

	// jmp loop_start backward
	jmpStart := len(buf)
	backwardRel32 := int32(loopStart - (jmpStart + EncodedJmpRel32Len))
	buf = EmitJmpRel32(buf, backwardRel32)

	// after_loop label
	afterLoop := len(buf)

	// ret
	buf = EmitRet(buf)

	// patch ja forward rel32 = afterLoop - (ja rel32 起点 + 4)
	forwardRel32 := int32(afterLoop) - int32(jaRel32Off+4)
	PatchRel32(buf, jaRel32Off, forwardRel32)

	// patch safepoint jne forward(若启用)
	if safepointJneRel32Off >= 0 {
		safepointRel32 := int32(afterLoop) - int32(safepointJneRel32Off+4)
		PatchRel32(buf, safepointJneRel32Off, safepointRel32)
	}

	return buf
}

// EncodedForLoopEmptyConstLen 是「全常量 init/limit/step + 空 body FORLOOP」
// 无 safepoint 版本字节数:10*3(mov×3) + 5*3(movq×3) + 4(subsd) + 4(addsd) + 4(ucomisd)
// + 6(ja) + 5(jmp) + 1(ret) = 69 字节。
const EncodedForLoopEmptyConstLen = EncodedMovRaxImm64Len*3 +
	EncodedMovqXmmFromRaxLen*3 +
	EncodedSseBinopLen + // subsd
	EncodedSseBinopLen + // addsd
	EncodedUcomisdLen + // ucomisd
	EncodedJccRel32Len + // ja
	EncodedJmpRel32Len + // jmp
	EncodedRetLen

// EncodedForLoopEmptyConstWithSafepointLen 含 safepoint check 版本字节数:
// 上面 69 + 8(cmp byte [r15+disp], 0)+ 6(jne)= 83 字节。
const EncodedForLoopEmptyConstWithSafepointLen = EncodedForLoopEmptyConstLen +
	EncodedCmpByteR15DispImm8Len + EncodedJccRel32Len

// EmitForLoopRegLimit 拼接「init/step 常量 + limit 是 reg + 空 body FORLOOP」
// 模板(承 reg-limit hot path 真实生产负载:`for i=1, n do end`)。
//
// 字节布局(以 step>0 + safepoint 启用为例):
//
//	[ 0] mov rax, [rbx + limitReg*8]        ; 7 bytes (load R(limitReg))
//	[ 7] mov rcx, qNanBoxBase imm64         ; 10 bytes
//	[17] cmp rax, rcx                        ; 3 bytes
//	[20] jae deopt                           ; 6 bytes (forward fixup)
//	[26] mov rax, K_init imm64; movq xmm0, rax  ; 15 bytes
//	[41] mov rax, [rbx + limitReg*8]         ; 7 bytes (load limit again as f64 bits)
//	[48] movq xmm1, rax                       ; 5 bytes
//	[53] mov rax, K_step imm64; movq xmm2, rax  ; 15 bytes
//	[68] subsd xmm0, xmm2                     ; 4 bytes
//	[72] ; loop_start
//	[72] addsd xmm0, xmm2                     ; 4 bytes
//	[76] ucomisd xmm0, xmm1                   ; 4 bytes
//	[80] ja  after_loop                       ; 6 bytes (forward fixup)
//	[86] ; (optional safepoint)
//	[86] cmp byte [r15+pfOff], 0              ; 8 bytes (若 pfOff>=0)
//	[94] jne after_loop                       ; 6 bytes
//	[100] jmp loop_start                      ; 5 bytes (backward;rel32=-(72-(100+5))=-33)
//	[105] ; after_loop
//	[105] ret                                 ; 1 byte
//	[106] ; deopt_block
//	[106] mov rax, deoptCode imm64            ; 10 bytes
//	[116] ret                                 ; 1 byte
//	——— 含 safepoint:117 字节 ———
//	——— 无 safepoint:117 - 14 = 103 字节 ———
//
// **预设条件**:
//   - rbx = valueStackBase(callJITSpec trampoline 装)
//   - limitReg = 寄存器号 ∈ [0, 254]
//   - guard 失败 → deopt 返 deoptCode → caller 走 host helper 慢路径
//
// **deopt 路径**:本模板不写 R(A) idx 槽(空 body 形态),deopt 时直接
// 返 deoptCode,caller 经 Run 路径降级调 host(经 doForPrep / doForLoop
// 同步语义 byte-equal 解释器)。
func EmitForLoopRegLimit(buf []byte, kInit, kStep uint64,
	limitReg uint8, deoptCode uint64, preemptFlagOff int32) []byte {
	// guard:load R(limitReg) → IsNumber check
	buf = EmitMovqRaxFromMemReg(buf, 3 /* rbx */, int32(limitReg)*8)
	buf = EmitMovRcxImm64(buf, qNanBoxBaseConst)
	buf = EmitCmpRaxRcx(buf)
	buf = EmitJaeRel32(buf, 0) // placeholder rel32 to deopt
	deoptJaeRel32Off := len(buf) - 4

	// 装 init/limit/step 到 xmm0/xmm1/xmm2(limit 经第二次 load 从 stack 取)
	buf = EmitMovRaxImm64(buf, kInit) // mov rax, K_init
	buf = EmitMovqXmmFromRax(buf, 0)  // movq xmm0, rax

	buf = EmitMovqRaxFromMemReg(buf, 3 /* rbx */, int32(limitReg)*8) // load limit reg
	buf = EmitMovqXmmFromRax(buf, 1)                                 // movq xmm1, rax

	buf = EmitMovRaxImm64(buf, kStep) // mov rax, K_step
	buf = EmitMovqXmmFromRax(buf, 2)  // movq xmm2, rax

	// FORPREP 预减
	buf = EmitSubsdXmmXmm(buf, 0, 2)

	// loop_start label
	loopStart := len(buf)

	// FORLOOP idx+=step
	buf = EmitAddsdXmmXmm(buf, 0, 2)

	// cmp idx, limit
	buf = EmitUcomisdXmmXmm(buf, 0, 1)

	// ja after_loop placeholder
	buf = EmitJaRel32(buf, 0)
	jaRel32Off := len(buf) - 4

	// (可选)safepoint check
	var safepointJneRel32Off int = -1
	if preemptFlagOff >= 0 {
		buf = EmitCmpByteR15DispImm8(buf, preemptFlagOff, 0)
		buf = EmitJneRel32(buf, 0)
		safepointJneRel32Off = len(buf) - 4
	}

	// jmp loop_start backward
	jmpStart := len(buf)
	backwardRel32 := int32(loopStart - (jmpStart + EncodedJmpRel32Len))
	buf = EmitJmpRel32(buf, backwardRel32)

	// after_loop label
	afterLoop := len(buf)

	// ret(正常退出)
	buf = EmitRet(buf)

	// deopt block:mov rax, deoptCode;ret
	deoptStart := len(buf)
	buf = EmitMovRaxImm64(buf, deoptCode)
	buf = EmitRet(buf)

	// patch ja forward rel32 (target = after_loop)
	forwardRel32 := int32(afterLoop) - int32(jaRel32Off+4)
	PatchRel32(buf, jaRel32Off, forwardRel32)

	// patch safepoint jne (target = after_loop)
	if safepointJneRel32Off >= 0 {
		safepointRel32 := int32(afterLoop) - int32(safepointJneRel32Off+4)
		PatchRel32(buf, safepointJneRel32Off, safepointRel32)
	}

	// patch deopt jae rel32 (target = deopt_block start)
	deoptRel32 := int32(deoptStart) - int32(deoptJaeRel32Off+4)
	PatchRel32(buf, deoptJaeRel32Off, deoptRel32)

	return buf
}

// EncodedForLoopRegLimitWithSafepointLen 含 safepoint 版字节数:
// 7(load1)+10(movrcx)+3(cmp)+6(jae)+10(mov init)+5(movq)+7(load2)
// +5(movq)+10(mov step)+5(movq)+4(subsd)+4(addsd)+4(ucomisd)+6(ja)
// +8(cmp byte)+6(jne)+5(jmp)+1(ret)+10(mov deopt)+1(ret) = 117 字节。
const EncodedForLoopRegLimitWithSafepointLen = EncodedMovqFromMemRegLen + // load1
	EncodedMovRcxImm64Len + EncodedCmpRaxRcxLen + EncodedJccRel32Len + // guard
	EncodedMovRaxImm64Len + EncodedMovqXmmFromRaxLen + // K_init
	EncodedMovqFromMemRegLen + EncodedMovqXmmFromRaxLen + // load limit twice
	EncodedMovRaxImm64Len + EncodedMovqXmmFromRaxLen + // K_step
	EncodedSseBinopLen + // subsd
	EncodedSseBinopLen + // addsd
	EncodedUcomisdLen + // ucomisd
	EncodedJccRel32Len + // ja
	EncodedCmpByteR15DispImm8Len + EncodedJccRel32Len + // safepoint
	EncodedJmpRel32Len + // jmp
	EncodedRetLen + // ret normal
	EncodedMovRaxImm64Len + EncodedRetLen // deopt block
