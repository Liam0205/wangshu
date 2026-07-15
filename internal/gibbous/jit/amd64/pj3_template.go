//go:build wangshu_p4 && amd64

// pj3_template.go —— P4 PJ3 byte-level FORLOOP inline templates (implements
// docs/design/p4-method-jit/05-system-pipeline.md §6.3 back-edge +
// 06-backends.md §3.3 numeric for).
//
// **PJ3 minimal form**: all-constant init/limit/step + empty-body FORLOOP
// inline (the `function() for i=1,100 do end end` shape). Verifies that an
// in-mmap self-loop + float idx accumulation + ucomisd limit + backward jcc
// work at the byte level.
//
// **Not included** (left for real PJ3 extension):
//   - body inline opcodes (need reg-K spec template inline + register allocation)
//   - limit-is-reg (MOVE) form (needs IsNumber guard)
//   - safepoint check — the current template is pure computation with no side
//     effects, so this can be added later
//   - non-positive step (jcc picks jb instead of ja)
//   - nesting / break

package amd64

// EmitForLoopEmptyConst assembles the "all-constant init/limit/step + empty-body
// FORLOOP byte-level template".
//
// Byte layout (per the design diagram in the file header above; the safepoint
// check is omitted when preemptFlagOff < 0):
//
//	[ 0] mov rax, K_init_imm64     ; 10 bytes
//	[10] movq xmm0, rax             ; 5 bytes
//	[15] mov rax, K_limit_imm64    ; 10 bytes
//	[25] movq xmm1, rax             ; 5 bytes
//	[30] mov rax, K_step_imm64     ; 10 bytes
//	[40] movq xmm2, rax             ; 5 bytes
//	[45] subsd xmm0, xmm2           ; 4 bytes (FORPREP pre-subtract: idx = init - step)
//	[49] ; loop_start
//	[49] addsd xmm0, xmm2           ; 4 bytes (FORLOOP: idx += step)
//	[53] ucomisd xmm1, xmm0         ; 4 bytes (cmp limit, idx; operand order exits on NaN, #117/#118)
//	[57] jb  after_loop             ; 6 bytes (forward jcc; CF=1 covers unordered; rel32 = +5 / +19 with safepoint)
//	[63] ; (optional safepoint check)
//	[63] cmp byte [r15+pfOff], 0    ; 8 bytes (only when preemptFlagOff >= 0)
//	[71] jne after_loop             ; 6 bytes (forward jcc)
//	[77] jmp loop_start             ; 5 bytes (backward jmp)
//	[82] ; after_loop
//	[82] ret                        ; 1 byte
//	——— total len = 69 bytes (no safepoint) / 83 bytes (with safepoint) ———
//
// Parameter preemptFlagOff: byte offset of the preempt field at r15+disp32.
//   - >= 0: enable safepoint check (per V18 -race preemption discipline)
//   - <  0: skip safepoint (for tests / strictly single-segment compute cases)
//
// **Preconditions**: the trampoline loads r15 (the safepoint check reads r15);
// the R(A) idx slot is not written (an empty body does not need it); the
// returned rax is a dummy (the host does not read it after the segment returns).
func EmitForLoopEmptyConst(buf []byte, kInit, kLimit, kStep uint64, preemptFlagOff int32) []byte {
	// load init/limit/step into xmm0/xmm1/xmm2
	buf = EmitMovRaxImm64(buf, kInit) // mov rax, K_init
	buf = EmitMovqXmmFromRax(buf, 0)  // movq xmm0, rax

	buf = EmitMovRaxImm64(buf, kLimit) // mov rax, K_limit
	buf = EmitMovqXmmFromRax(buf, 1)   // movq xmm1, rax

	buf = EmitMovRaxImm64(buf, kStep) // mov rax, K_step
	buf = EmitMovqXmmFromRax(buf, 2)  // movq xmm2, rax

	// FORPREP pre-subtract: xmm0 = init - step
	buf = EmitSubsdXmmXmm(buf, 0, 2) // subsd xmm0, xmm2

	// loop_start label
	loopStart := len(buf)

	// FORLOOP idx+=step: xmm0 += xmm2
	buf = EmitAddsdXmmXmm(buf, 0, 2) // addsd xmm0, xmm2

	// cmp limit, idx — operand order matters for NaN (issues #117/#118):
	// ucomisd sets CF=ZF=1 on unordered, so with `ucomisd idx, limit; ja`
	// a NaN limit/init never took the exit branch and the segment looped
	// forever. Comparing limit-vs-idx and exiting on `jb` (CF=1) exits on
	// BOTH limit < idx AND unordered — matching the interpreter's
	// zero-iteration semantics for NaN (same shape as the per-op
	// emitFORLOOP's jae-on-swapped-operands).
	buf = EmitUcomisdXmmXmm(buf, 1, 0) // ucomisd xmm1, xmm0

	// jb after_loop placeholder rel32=0 (forward fixup)
	buf = EmitJbRel32(buf, 0)
	jbRel32Off := len(buf) - 4

	// (optional) safepoint check: cmp byte [r15+pfOff], 0; jne after_loop
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

	// patch jb forward rel32 = afterLoop - (ja rel32 start + 4)
	forwardRel32 := int32(afterLoop) - int32(jbRel32Off+4)
	PatchRel32(buf, jbRel32Off, forwardRel32)

	// patch safepoint jne forward (if enabled)
	if safepointJneRel32Off >= 0 {
		safepointRel32 := int32(afterLoop) - int32(safepointJneRel32Off+4)
		PatchRel32(buf, safepointJneRel32Off, safepointRel32)
	}

	return buf
}

// EncodedForLoopEmptyConstLen is the byte count of the "all-constant
// init/limit/step + empty-body FORLOOP" no-safepoint version:
// 10*3 (mov×3) + 5*3 (movq×3) + 4 (subsd) + 4 (addsd) + 4 (ucomisd)
// + 6 (jb) + 5 (jmp) + 1 (ret) = 69 bytes.
const EncodedForLoopEmptyConstLen = EncodedMovRaxImm64Len*3 +
	EncodedMovqXmmFromRaxLen*3 +
	EncodedSseBinopLen + // subsd
	EncodedSseBinopLen + // addsd
	EncodedUcomisdLen + // ucomisd
	EncodedJccRel32Len + // ja
	EncodedJmpRel32Len + // jmp
	EncodedRetLen

// EncodedForLoopEmptyConstWithSafepointLen is the byte count of the version
// that includes the safepoint check:
// the 69 above + 8 (cmp byte [r15+disp], 0) + 6 (jne) = 83 bytes.
const EncodedForLoopEmptyConstWithSafepointLen = EncodedForLoopEmptyConstLen +
	EncodedCmpByteR15DispImm8Len + EncodedJccRel32Len

// EmitForLoopRegLimit assembles the "init/step constant + limit-is-reg +
// empty-body FORLOOP" template (for the reg-limit hot path's real production
// load: `for i=1, n do end`).
//
// Byte layout (example with step>0 + safepoint enabled):
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
//	[76] ucomisd xmm1, xmm0                   ; 4 bytes (cmp limit, idx; exits on NaN, #117/#118)
//	[80] jb  after_loop                       ; 6 bytes (forward fixup; CF=1 covers unordered)
//	[86] ; (optional safepoint)
//	[86] cmp byte [r15+pfOff], 0              ; 8 bytes (if pfOff>=0)
//	[94] jne after_loop                       ; 6 bytes
//	[100] jmp loop_start                      ; 5 bytes (backward; rel32=-(72-(100+5))=-33)
//	[105] ; after_loop
//	[105] ret                                 ; 1 byte
//	[106] ; deopt_block
//	[106] mov rax, deoptCode imm64            ; 10 bytes
//	[116] ret                                 ; 1 byte
//	——— with safepoint: 117 bytes ———
//	——— without safepoint: 117 - 14 = 103 bytes ———
//
// **Preconditions**:
//   - rbx = valueStackBase (loaded by the callJITSpec trampoline)
//   - limitReg = register number ∈ [0, 254]
//   - guard failure → deopt returns deoptCode → caller takes the host-helper slow path
//
// **deopt path**: this template does not write the R(A) idx slot (empty-body
// form); on deopt it directly returns deoptCode, and the caller falls back via
// the Run path to the host (via doForPrep / doForLoop, byte-equal to the
// interpreter's synchronized semantics).
func EmitForLoopRegLimit(buf []byte, kInit, kStep uint64,
	limitReg uint8, deoptCode uint64, preemptFlagOff int32) []byte {
	// guard: load R(limitReg) → IsNumber check
	buf = EmitMovqRaxFromMemReg(buf, 3 /* rbx */, int32(limitReg)*8)
	buf = EmitMovRcxImm64(buf, qNanBoxBaseConst)
	buf = EmitCmpRaxRcx(buf)
	buf = EmitJaeRel32(buf, 0) // placeholder rel32 to deopt
	deoptJaeRel32Off := len(buf) - 4

	// load init/limit/step into xmm0/xmm1/xmm2 (limit fetched from the stack via a second load)
	buf = EmitMovRaxImm64(buf, kInit) // mov rax, K_init
	buf = EmitMovqXmmFromRax(buf, 0)  // movq xmm0, rax

	buf = EmitMovqRaxFromMemReg(buf, 3 /* rbx */, int32(limitReg)*8) // load limit reg
	buf = EmitMovqXmmFromRax(buf, 1)                                 // movq xmm1, rax

	buf = EmitMovRaxImm64(buf, kStep) // mov rax, K_step
	buf = EmitMovqXmmFromRax(buf, 2)  // movq xmm2, rax

	// FORPREP pre-subtract
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

	// (optional) safepoint check
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

	// ret (normal exit)
	buf = EmitRet(buf)

	// deopt block: mov rax, deoptCode; ret
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

// EncodedForLoopRegLimitWithSafepointLen is the byte count of the with-safepoint version:
// 7 (load1)+10 (movrcx)+3 (cmp)+6 (jae)+10 (mov init)+5 (movq)+7 (load2)
// +5 (movq)+10 (mov step)+5 (movq)+4 (subsd)+4 (addsd)+4 (ucomisd)+6 (ja)
// +8 (cmp byte)+6 (jne)+5 (jmp)+1 (ret)+10 (mov deopt)+1 (ret) = 117 bytes.
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

// EmitForLoopWithRegKBody assembles the "all-constant init/limit/step + reg-K
// body FORLOOP" template (for the hot path's real production load:
// `local s=K_s; for i=K1,K2 do s=s op K3 end; return s`).
//
// Byte layout (with safepoint):
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
//	[115] cmp byte [r15+pfOff], 0                      ; 8 (optional safepoint)
//	[123] jne after_loop                               ; 6
//	[129] jmp loop_start                               ; 5 (backward; rel32=-(66-(129+5))=-68)
//	[134] ; after_loop
//	[134] ret                                          ; 1
//	——— with safepoint: 135 bytes ———
//	——— without safepoint: 135 - 14 = 121 bytes ———
//
// **Preconditions**:
//   - rbx = valueStackBase
//   - aS: register number of s (R(aS), independent of init/limit/step's A_init;
//     the caller checks aS != A_init/+1/+2/+3 to avoid clobbering)
//   - sseOp: SSE binop in the F2 0F <op> C0 form (ADD/SUB/MUL/DIV)
//   - fully guard-free (K_s/K_body are both numbers burned as imm at compile
//     time, init/limit/step are also K; the reg-limit + body inline form is left
//     for PJ3+ extension)
//
// **deopt path**: this minimal body form is guard-free, with no deopt block.
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

	// body: R(aS) = R(aS) sseOp K (uses xmm3/xmm4, avoiding idx/limit/step xmm0/1/2)
	buf = EmitMovsdXmmFromMem(buf, 3, 3 /*rbx*/, int32(aS)*8)
	buf = EmitMovRaxImm64(buf, kBody)
	buf = EmitMovqXmmFromRax(buf, 4)
	// sseOp xmm3, xmm4: F2 0F <op> ModRM
	// ModRM = 0xC0 | (3<<3) | 4 = 0xDC
	buf = append(buf, 0xF2, 0x0F, sseOp, 0xDC)
	buf = EmitMovsdMemFromXmm(buf, 3, 3 /*rbx*/, int32(aS)*8)

	// safepoint check (optional)
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

// EncodedForLoopWithRegKBodyWithSafepointLen is the byte count of the with-safepoint version:
// 10+7 (init R(aS))+10+5+10+5+10+5 (setup) + 4 (subsd) + 4 (addsd)+4 (ucomisd)
// +6 (ja) + 8+10+5+4+8 (body) + 8+6 (safepoint) + 5 (jmp) + 1 (ret) = 135.
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

// EmitForLoopWithRegKBody2 assembles a two-op body template: `local s; for
// i=K1,K2 do s = s op1 K3; s = s op2 K4 end; return s`. The body has two
// consecutive reg-K ops sharing the R(aS) register (the intermediate value
// stays in R(aS)), using the xmm3 register across both ops (avoiding a
// load/store round-trip).
//
// Byte layout (with safepoint):
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
//	——— with safepoint: 154 bytes ———
//
// Saves one load+store versus the single-body template (sharing xmm3 across 2
// ops): saves 16 bytes of memory access per iteration.
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

	// body: load s once, then two SSE ops share xmm3
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
