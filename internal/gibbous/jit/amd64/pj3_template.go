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
//	[53] ucomisd xmm1, xmm0         ; 4 bytes (cmp limit, idx;操作数序为 NaN 退出,#117/#118)
//	[57] jb  after_loop             ; 6 bytes (forward jcc;CF=1 含 unordered;rel32 = +5 / +19 含 safepoint)
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

	// cmp limit, idx — operand order matters for NaN (issues #117/#118):
	// ucomisd sets CF=ZF=1 on unordered, so with `ucomisd idx, limit; ja`
	// a NaN limit/init never took the exit branch and the segment looped
	// forever. Comparing limit-vs-idx and exiting on `jb` (CF=1) exits on
	// BOTH limit < idx AND unordered — matching the interpreter's
	// zero-iteration semantics for NaN (same shape as the per-op
	// emitFORLOOP's jae-on-swapped-operands).
	buf = EmitUcomisdXmmXmm(buf, 1, 0) // ucomisd xmm1, xmm0

	// jb after_loop placeholder rel32=0(forward fixup)
	buf = EmitJbRel32(buf, 0)
	jbRel32Off := len(buf) - 4

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

	// patch jb forward rel32 = afterLoop - (ja rel32 起点 + 4)
	forwardRel32 := int32(afterLoop) - int32(jbRel32Off+4)
	PatchRel32(buf, jbRel32Off, forwardRel32)

	// patch safepoint jne forward(若启用)
	if safepointJneRel32Off >= 0 {
		safepointRel32 := int32(afterLoop) - int32(safepointJneRel32Off+4)
		PatchRel32(buf, safepointJneRel32Off, safepointRel32)
	}

	return buf
}

// EncodedForLoopEmptyConstLen 是「全常量 init/limit/step + 空 body FORLOOP」
// 无 safepoint 版本字节数:10*3(mov×3) + 5*3(movq×3) + 4(subsd) + 4(addsd) + 4(ucomisd)
// + 6(jb) + 5(jmp) + 1(ret) = 69 字节。
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
//	[76] ucomisd xmm1, xmm0                   ; 4 bytes (cmp limit, idx;NaN 退出,#117/#118)
//	[80] jb  after_loop                       ; 6 bytes (forward fixup;CF=1 含 unordered)
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

	// cmp limit, idx + jb: exit on limit < idx OR unordered (NaN limit
	// from the guarded reg slot is a genuine number — issues #117/#118,
	// see EmitForLoopEmptyConst).
	buf = EmitUcomisdXmmXmm(buf, 1, 0)

	// jb after_loop placeholder
	buf = EmitJbRel32(buf, 0)
	jbRel32Off := len(buf) - 4

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

	// patch jb forward rel32 (target = after_loop)
	forwardRel32 := int32(afterLoop) - int32(jbRel32Off+4)
	PatchRel32(buf, jbRel32Off, forwardRel32)

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

// EmitForLoopWithRegKBody 拼接「全常量 init/limit/step + reg-K body FORLOOP」
// 模板(承 hot path 真实生产负载:`local s=K_s; for i=K1,K2 do s=s op K3 end;
// return s`)。
//
// 字节布局(含 safepoint):
//
//	[ 0] mov rax, K_s_imm64;  mov [rbx+aS*8], rax     ; 17 (init R(aS)=s)
//	[17] mov rax, K_init imm64;movq xmm0,rax           ; 15
//	[32] mov rax, K_limit imm64; movq xmm1,rax         ; 15
//	[47] mov rax, K_step imm64; movq xmm2,rax          ; 15
//	[62] subsd xmm0, xmm2                              ; 4
//	[66] ; loop_start
//	[66] addsd xmm0, xmm2                              ; 4
//	[70] ucomisd xmm0, xmm1                            ; 4
//	[74] ja  after_loop                                ; 6
//	[80] movsd xmm3, [rbx+aS*8]                        ; 8 (load s)
//	[88] mov rax, K_body imm64; movq xmm4,rax          ; 15
//	[103] <sseOp> xmm3, xmm4                           ; 4 (s op K)
//	[107] movsd [rbx+aS*8], xmm3                       ; 8 (store s)
//	[115] cmp byte [r15+pfOff], 0                      ; 8 (可选 safepoint)
//	[123] jne after_loop                               ; 6
//	[129] jmp loop_start                               ; 5 (backward;rel32=-(66-(129+5))=-68)
//	[134] ; after_loop
//	[134] ret                                          ; 1
//	——— 含 safepoint:135 字节 ———
//	——— 无 safepoint:135 - 14 = 121 字节 ———
//
// **预设条件**:
//   - rbx = valueStackBase
//   - aS:s 的寄存器号(R(aS),与 init/limit/step 的 A_init 独立,
//     由 caller 校验 aS != A_init/+1/+2/+3 避免覆盖)
//   - sseOp:F2 0F <op> C0 形式 SSE binop(ADD/SUB/MUL/DIV)
//   - 完全无 guard(K_s/K_body 都是 number 编译期烧 imm,init/limit/step
//     也是 K;reg-limit + body inline 形态留 PJ3+ 扩)
//
// **deopt 路径**:本最简 body 形态无 guard,无 deopt block。
func EmitForLoopWithRegKBody(buf []byte, kS, kInit, kLimit, kStep, kBody uint64,
	aS uint8, sseOp byte, preemptFlagOff int32) []byte {
	// 1. Init R(aS) = K_s
	buf = EmitMovRaxImm64(buf, kS)
	buf = EmitMovqMemRegFromRax(buf, 3 /*rbx*/, int32(aS)*8)

	// 2. FORLOOP setup
	buf = EmitMovRaxImm64(buf, kInit)
	buf = EmitMovqXmmFromRax(buf, 0)
	buf = EmitMovRaxImm64(buf, kLimit)
	buf = EmitMovqXmmFromRax(buf, 1)
	buf = EmitMovRaxImm64(buf, kStep)
	buf = EmitMovqXmmFromRax(buf, 2)
	buf = EmitSubsdXmmXmm(buf, 0, 2)

	// loop_start
	loopStart := len(buf)
	buf = EmitAddsdXmmXmm(buf, 0, 2)
	// cmp limit, idx + jb: exit on limit < idx OR unordered (NaN in a
	// const slot — issues #117/#118, see EmitForLoopEmptyConst).
	buf = EmitUcomisdXmmXmm(buf, 1, 0)
	buf = EmitJbRel32(buf, 0)
	jbOff := len(buf) - 4

	// body: R(aS) = R(aS) sseOp K (用 xmm3/xmm4,避开 idx/limit/step xmm0/1/2)
	buf = EmitMovsdXmmFromMem(buf, 3, 3 /*rbx*/, int32(aS)*8)
	buf = EmitMovRaxImm64(buf, kBody)
	buf = EmitMovqXmmFromRax(buf, 4)
	// sseOp xmm3, xmm4:F2 0F <op> ModRM
	// ModRM = 0xC0 | (3<<3) | 4 = 0xDC
	buf = append(buf, 0xF2, 0x0F, sseOp, 0xDC)
	buf = EmitMovsdMemFromXmm(buf, 3, 3 /*rbx*/, int32(aS)*8)

	// safepoint check(可选)
	var safepointJneOff int = -1
	if preemptFlagOff >= 0 {
		buf = EmitCmpByteR15DispImm8(buf, preemptFlagOff, 0)
		buf = EmitJneRel32(buf, 0)
		safepointJneOff = len(buf) - 4
	}

	// backward jmp
	jmpStart := len(buf)
	buf = EmitJmpRel32(buf, int32(loopStart-(jmpStart+EncodedJmpRel32Len)))

	// after_loop label
	afterLoop := len(buf)
	buf = EmitRet(buf)

	// patch forward fixups
	PatchRel32(buf, jbOff, int32(afterLoop)-int32(jbOff+4))
	if safepointJneOff >= 0 {
		PatchRel32(buf, safepointJneOff, int32(afterLoop)-int32(safepointJneOff+4))
	}

	return buf
}

// EncodedForLoopWithRegKBodyWithSafepointLen 含 safepoint 版字节数:
// 10+7(init R(aS))+10+5+10+5+10+5(setup) + 4(subsd) + 4(addsd)+4(ucomisd)
// +6(ja) + 8+10+5+4+8(body) + 8+6(safepoint) + 5(jmp) + 1(ret) = 135.
const EncodedForLoopWithRegKBodyWithSafepointLen = EncodedMovRaxImm64Len + EncodedMovqMemFromRaxLen + // init R(aS)
	EncodedMovRaxImm64Len + EncodedMovqXmmFromRaxLen + // K_init
	EncodedMovRaxImm64Len + EncodedMovqXmmFromRaxLen + // K_limit
	EncodedMovRaxImm64Len + EncodedMovqXmmFromRaxLen + // K_step
	EncodedSseBinopLen + // subsd
	EncodedSseBinopLen + // addsd
	EncodedUcomisdLen + // ucomisd
	EncodedJccRel32Len + // ja
	EncodedMovsdMemLen + EncodedMovRaxImm64Len + EncodedMovqXmmFromRaxLen + EncodedSseBinopLen + EncodedMovsdMemLen + // body
	EncodedCmpByteR15DispImm8Len + EncodedJccRel32Len + // safepoint
	EncodedJmpRel32Len + // backward jmp
	EncodedRetLen // ret

// EmitForLoopWithRegKBody2 拼接二段 body 模板:`local s; for i=K1,K2 do
// s = s op1 K3; s = s op2 K4 end; return s`。body 内连续两个 reg-K op,
// 共享 R(aS) 寄存器(中间值落 R(aS)),用 xmm3 寄存器跨两段(避 load/store
// 中转)。
//
// 字节布局(含 safepoint):
//
//	[init]   mov rax, K_s; mov [rbx+aS*8], rax        ; 17
//	[setup]  init xmm0/1/2 + subsd                     ; 49
//	[loop_start]
//	  addsd xmm0, xmm2; ucomisd; ja after_loop         ; 14
//	  movsd xmm3, [rbx+aS*8]                           ; 8 (load s once)
//	  mov rax, K1; movq xmm4, rax; <sseOp1> xmm3, xmm4 ; 19
//	  mov rax, K2; movq xmm4, rax; <sseOp2> xmm3, xmm4 ; 19
//	  movsd [rbx+aS*8], xmm3                           ; 8 (store s once)
//	  cmp byte [r15+pfOff], 0; jne after_loop          ; 14
//	  jmp loop_start                                    ; 5
//	[after_loop]
//	  ret                                              ; 1
//	——— 含 safepoint:154 字节 ———
//
// 比单 body 模板节省一次 load+store(共享 xmm3 跨 2 op):每 iter 节省 16 字节
// 内存访问。
func EmitForLoopWithRegKBody2(buf []byte, kS, kInit, kLimit, kStep, kBody1, kBody2 uint64,
	aS uint8, sseOp1, sseOp2 byte, preemptFlagOff int32) []byte {
	// 1. Init R(aS) = K_s
	buf = EmitMovRaxImm64(buf, kS)
	buf = EmitMovqMemRegFromRax(buf, 3, int32(aS)*8)

	// 2. setup
	buf = EmitMovRaxImm64(buf, kInit)
	buf = EmitMovqXmmFromRax(buf, 0)
	buf = EmitMovRaxImm64(buf, kLimit)
	buf = EmitMovqXmmFromRax(buf, 1)
	buf = EmitMovRaxImm64(buf, kStep)
	buf = EmitMovqXmmFromRax(buf, 2)
	buf = EmitSubsdXmmXmm(buf, 0, 2)

	loopStart := len(buf)
	buf = EmitAddsdXmmXmm(buf, 0, 2)
	// cmp limit, idx + jb: exit on limit < idx OR unordered (NaN in a
	// const slot — issues #117/#118, see EmitForLoopEmptyConst).
	buf = EmitUcomisdXmmXmm(buf, 1, 0)
	buf = EmitJbRel32(buf, 0)
	jbOff := len(buf) - 4

	// body:load s 一次,然后两段 SSE op 共享 xmm3
	buf = EmitMovsdXmmFromMem(buf, 3, 3, int32(aS)*8)
	// op1
	buf = EmitMovRaxImm64(buf, kBody1)
	buf = EmitMovqXmmFromRax(buf, 4)
	buf = append(buf, 0xF2, 0x0F, sseOp1, 0xDC) // sseOp1 xmm3, xmm4
	// op2
	buf = EmitMovRaxImm64(buf, kBody2)
	buf = EmitMovqXmmFromRax(buf, 4)
	buf = append(buf, 0xF2, 0x0F, sseOp2, 0xDC)
	// store s
	buf = EmitMovsdMemFromXmm(buf, 3, 3, int32(aS)*8)

	var safepointJneOff int = -1
	if preemptFlagOff >= 0 {
		buf = EmitCmpByteR15DispImm8(buf, preemptFlagOff, 0)
		buf = EmitJneRel32(buf, 0)
		safepointJneOff = len(buf) - 4
	}

	jmpStart := len(buf)
	buf = EmitJmpRel32(buf, int32(loopStart-(jmpStart+EncodedJmpRel32Len)))

	afterLoop := len(buf)
	buf = EmitRet(buf)

	PatchRel32(buf, jbOff, int32(afterLoop)-int32(jbOff+4))
	if safepointJneOff >= 0 {
		PatchRel32(buf, safepointJneOff, int32(afterLoop)-int32(safepointJneOff+4))
	}

	return buf
}
