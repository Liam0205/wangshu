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
// **loopFuel back-edge accounting (issue #143)**: each template's back-edge
// decrements jitCtx.loopFuel (`sub dword [r15+loopFuelOff], 1; jnz
// loop_start`); at zero the exhausted tail spills the xmm-resident loop state
// (idx/limit/step in xmm0/1/2) into the jitCtx loopSpill0/1/2 slots, sets
// RAX = loopFuelCode and RETs. The Go-side Run detects the sentinel, calls
// host.LoopPreempt (bills the spent back-edges to the step budget, refills,
// raises "instruction budget exceeded" / "context canceled" when tripped) and
// re-enters the segment at the resume entry, which reloads xmm0/1/2 and jumps
// back to the loop head. Without this, a `for i=0,inf` loop had no billing
// point (the preemptFlag safepoint has no async producer for the step budget)
// and hung forever — the 3rd instance of the "in-segment no preemption point
// -> step-budget billing gap" family (#102 P4 native emit, #135 P3 wasm,
// #143 this file).
//
// **Not included** (left for real PJ3 extension):
//   - non-positive step (jcc picks jb instead of ja)
//   - nesting / break

package amd64

// emitPJ3LoopFuelBackEdge emits the loopFuel back-edge decrement + the
// exhausted tail (issue #143, mirroring the per-op native emit's
// emitLoopFuelBackEdge in peroptranslator/emit_ops_amd64.go):
//
//	sub dword [r15+loopFuelOff], 1  ; 8 bytes
//	jnz loop_start                  ; 6 bytes (backward; this IS the back edge)
//	; exhausted tail:
//	movsd [r15+spill0], xmm0        ; 9 bytes (idx)
//	movsd [r15+spill1], xmm1        ; 9 bytes (limit)
//	movsd [r15+spill2], xmm2        ; 9 bytes (step)
//	mov rax, loopFuelCode           ; 10 bytes (RAX sentinel, same protocol
//	                                ;   as the spec deoptCode)
//	ret                             ; 1 byte
//
// Unlike the native emit's HelperLoopFuel exit-reason (which resumes fully
// in-segment via the dispatcher), the PJ3 templates keep the loop induction
// state in xmm registers, which no ABI preserves across the RET-to-Go round
// trip — hence the spill into the jitCtx loopSpill slots that
// emitPJ3LoopFuelResume reloads. No seg2seg deopt guard is needed: PJ3 spec
// templates only ever run as the top-level segment (never as a seg2seg
// callee).
func emitPJ3LoopFuelBackEdge(buf []byte, loopStart int,
	loopFuelOff, loopSpillOff int32, loopFuelCode uint64) []byte {
	buf = EmitSubDwordR15DispImm8(buf, loopFuelOff, 1)
	jnzStart := len(buf)
	buf = EmitJneRel32(buf, int32(loopStart-(jnzStart+EncodedJccRel32Len)))
	buf = EmitMovsdR15DispFromXmm(buf, 0, loopSpillOff)
	buf = EmitMovsdR15DispFromXmm(buf, 1, loopSpillOff+8)
	buf = EmitMovsdR15DispFromXmm(buf, 2, loopSpillOff+16)
	buf = EmitMovRaxImm64(buf, loopFuelCode)
	buf = EmitRet(buf)
	return buf
}

// emitPJ3LoopFuelResume emits the loopFuel resume entry (issue #143) and
// returns its byte offset within the segment. Run re-enters here (codePage
// base + offset) after host.LoopPreempt refilled the fuel:
//
//	movsd xmm0, [r15+spill0]        ; 9 bytes (idx)
//	movsd xmm1, [r15+spill1]        ; 9 bytes (limit)
//	movsd xmm2, [r15+spill2]        ; 9 bytes (step)
//	jmp loop_start                  ; 5 bytes (backward)
//
// Jumping to loop_start runs the next `addsd` (idx += step) exactly as the
// normal back-edge would have — the drained iteration completed its body
// before the exhausted tail ran, so there is no double-step.
func emitPJ3LoopFuelResume(buf []byte, loopStart int, loopSpillOff int32) ([]byte, int) {
	resumeOff := len(buf)
	buf = EmitMovsdXmmFromR15Disp(buf, 0, loopSpillOff)
	buf = EmitMovsdXmmFromR15Disp(buf, 1, loopSpillOff+8)
	buf = EmitMovsdXmmFromR15Disp(buf, 2, loopSpillOff+16)
	jmpStart := len(buf)
	buf = EmitJmpRel32(buf, int32(loopStart-(jmpStart+EncodedJmpRel32Len)))
	return buf, resumeOff
}

// EncodedPJ3LoopFuelBackEdgeLen is the byte count of the back-edge decrement
// pair (8 sub + 6 jnz = 14). It replaces the 5-byte `jmp loop_start`.
const EncodedPJ3LoopFuelBackEdgeLen = EncodedSubDwordR15DispImm8Len + EncodedJccRel32Len

// EncodedPJ3LoopFuelTailLen is the byte count of the exhausted tail
// (3 spills 9*3 + mov rax imm64 10 + ret 1 = 38).
const EncodedPJ3LoopFuelTailLen = EncodedMovsdR15DispLen*3 + EncodedMovRaxImm64Len + EncodedRetLen

// EncodedPJ3LoopFuelResumeLen is the byte count of the resume entry
// (3 reloads 9*3 + jmp 5 = 32).
const EncodedPJ3LoopFuelResumeLen = EncodedMovsdR15DispLen*3 + EncodedJmpRel32Len

// EncodedPJ3LoopFuelExtraLen is the total the loopFuel machinery adds to a
// template versus the fuel-less version: back-edge pair + tail + resume,
// minus the 5-byte plain jmp it replaces (14 + 38 + 32 - 5 = 79).
const EncodedPJ3LoopFuelExtraLen = EncodedPJ3LoopFuelBackEdgeLen +
	EncodedPJ3LoopFuelTailLen + EncodedPJ3LoopFuelResumeLen - EncodedJmpRel32Len

// EmitForLoopEmptyConst assembles the "all-constant init/limit/step + empty-body
// FORLOOP byte-level template".
//
// Byte layout (per the design diagram in the file header above; the safepoint
// check is omitted when preemptFlagOff < 0, the loopFuel back-edge accounting
// when loopFuelOff < 0):
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
//	[57] jb  after_loop             ; 6 bytes (forward jcc; CF=1 covers unordered)
//	[63] ; (optional safepoint check)
//	[63] cmp byte [r15+pfOff], 0    ; 8 bytes (only when preemptFlagOff >= 0)
//	[71] jne after_loop             ; 6 bytes (forward jcc)
//	[77] ; back edge: loopFuel pair (loopFuelOff >= 0) or plain jmp
//	[77] sub dword [r15+fuelOff], 1 ; 8 bytes \  issue #143 fuel accounting
//	[85] jnz loop_start             ; 6 bytes  | (replaces jmp loop_start)
//	[91] <exhausted tail>           ; 38 bytes/
//	; after_loop
//	ret                             ; 1 byte
//	; resume entry (loopFuelOff >= 0)  <- returned resumeOff
//	<reload xmm0/1/2 + jmp loop_start> ; 32 bytes
//	——— no safepoint, no fuel: 69 bytes / with both: 162 bytes ———
//
// Parameter preemptFlagOff: byte offset of the preempt field at r15+disp32.
//   - >= 0: enable safepoint check (per V18 -race preemption discipline)
//   - <  0: skip safepoint (for tests / strictly single-segment compute cases)
//
// Parameters loopFuelOff / loopSpillOff / loopFuelCode: the jitCtx loopFuel
// counter offset, the loopSpill0 slot offset (spill1/2 are assumed contiguous
// at +8/+16 — asserted by the jit package's layout test), and the RAX
// sentinel Run uses to detect a fuel exit. loopFuelOff < 0 disables the fuel
// machinery (byte-level unit tests that call with jitCtx = 0).
//
// Returns the assembled buffer and the resume-entry byte offset (0 when the
// fuel machinery is disabled).
//
// **Preconditions**: the trampoline loads r15 (the safepoint check, fuel
// counter and spill slots all read/write r15); the R(A) idx slot is not
// written (an empty body does not need it); the returned rax on the normal
// exit is a dummy (the host does not read it after the segment returns) but
// carries loopFuelCode on a fuel exit.
func EmitForLoopEmptyConst(buf []byte, kInit, kLimit, kStep uint64,
	preemptFlagOff, loopFuelOff, loopSpillOff int32, loopFuelCode uint64) ([]byte, int) {
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

	// back edge: loopFuel dec + jnz (issue #143) or the plain backward jmp
	if loopFuelOff >= 0 {
		buf = emitPJ3LoopFuelBackEdge(buf, loopStart, loopFuelOff, loopSpillOff, loopFuelCode)
	} else {
		jmpStart := len(buf)
		buf = EmitJmpRel32(buf, int32(loopStart-(jmpStart+EncodedJmpRel32Len)))
	}

	// after_loop label
	afterLoop := len(buf)

	// ret
	buf = EmitRet(buf)

	// resume entry (issue #143): reload xmm0/1/2 + jmp loop_start
	resumeOff := 0
	if loopFuelOff >= 0 {
		buf, resumeOff = emitPJ3LoopFuelResume(buf, loopStart, loopSpillOff)
	}

	// patch jb forward rel32 = afterLoop - (ja rel32 start + 4)
	forwardRel32 := int32(afterLoop) - int32(jbRel32Off+4)
	PatchRel32(buf, jbRel32Off, forwardRel32)

	// patch safepoint jne forward (if enabled)
	if safepointJneRel32Off >= 0 {
		safepointRel32 := int32(afterLoop) - int32(safepointJneRel32Off+4)
		PatchRel32(buf, safepointJneRel32Off, safepointRel32)
	}

	return buf, resumeOff
}

// EncodedForLoopEmptyConstLen is the byte count of the "all-constant
// init/limit/step + empty-body FORLOOP" no-safepoint no-fuel version:
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
// that includes the safepoint check (still no fuel):
// the 69 above + 8 (cmp byte [r15+disp], 0) + 6 (jne) = 83 bytes.
const EncodedForLoopEmptyConstWithSafepointLen = EncodedForLoopEmptyConstLen +
	EncodedCmpByteR15DispImm8Len + EncodedJccRel32Len

// EncodedForLoopEmptyConstFullLen is the byte count with both the safepoint
// and the loopFuel machinery enabled (83 + 79 = 162 bytes).
const EncodedForLoopEmptyConstFullLen = EncodedForLoopEmptyConstWithSafepointLen +
	EncodedPJ3LoopFuelExtraLen

// EmitForLoopRegLimit assembles the "init/step constant + limit-is-reg +
// empty-body FORLOOP" template (for the reg-limit hot path's real production
// load: `for i=1, n do end`).
//
// Byte layout (example with step>0 + safepoint + loopFuel enabled):
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
//	[100] ; back edge (issue #143 fuel pair, or plain jmp when fuelOff<0)
//	[100] sub dword [r15+fuelOff], 1          ; 8 bytes
//	[108] jnz loop_start                      ; 6 bytes
//	[114] <exhausted tail>                    ; 38 bytes
//	[152] ; after_loop
//	[152] ret                                 ; 1 byte
//	[153] ; deopt_block
//	[153] mov rax, deoptCode imm64            ; 10 bytes
//	[163] ret                                 ; 1 byte
//	[164] ; resume entry                       <- returned resumeOff
//	[164] <reload xmm0/1/2 + jmp loop_start>  ; 32 bytes
//	——— safepoint + fuel: 196 bytes; safepoint only: 117 bytes ———
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
//
// **fuel path** (issue #143): loopFuelCode must differ from deoptCode — Run
// tells the two RAX sentinels apart to route LoopPreempt-resume vs
// ForPrep-deopt.
func EmitForLoopRegLimit(buf []byte, kInit, kStep uint64,
	limitReg uint8, deoptCode uint64,
	preemptFlagOff, loopFuelOff, loopSpillOff int32, loopFuelCode uint64) ([]byte, int) {
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

	// back edge: loopFuel dec + jnz (issue #143) or the plain backward jmp
	if loopFuelOff >= 0 {
		buf = emitPJ3LoopFuelBackEdge(buf, loopStart, loopFuelOff, loopSpillOff, loopFuelCode)
	} else {
		jmpStart := len(buf)
		buf = EmitJmpRel32(buf, int32(loopStart-(jmpStart+EncodedJmpRel32Len)))
	}

	// after_loop label
	afterLoop := len(buf)

	// ret (normal exit)
	buf = EmitRet(buf)

	// deopt block: mov rax, deoptCode; ret
	deoptStart := len(buf)
	buf = EmitMovRaxImm64(buf, deoptCode)
	buf = EmitRet(buf)

	// resume entry (issue #143)
	resumeOff := 0
	if loopFuelOff >= 0 {
		buf, resumeOff = emitPJ3LoopFuelResume(buf, loopStart, loopSpillOff)
	}

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

	return buf, resumeOff
}

// EncodedForLoopRegLimitWithSafepointLen is the byte count of the
// with-safepoint no-fuel version:
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

// EncodedForLoopRegLimitFullLen is the byte count with both the safepoint and
// the loopFuel machinery enabled (117 + 79 = 196 bytes).
const EncodedForLoopRegLimitFullLen = EncodedForLoopRegLimitWithSafepointLen +
	EncodedPJ3LoopFuelExtraLen

// EmitForLoopWithRegKBody assembles the "all-constant init/limit/step + reg-K
// body FORLOOP" template (for the hot path's real production load:
// `local s=K_s; for i=K1,K2 do s=s op K3 end; return s`).
//
// Byte layout (with safepoint, no fuel; the issue #143 fuel pair replaces the
// trailing jmp and appends the exhausted tail + resume entry exactly as in
// EmitForLoopEmptyConst):
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
//	[129] jmp loop_start                               ; 5 (or the fuel pair)
//	[134] ; after_loop
//	[134] ret                                          ; 1
//	——— with safepoint: 135 bytes; + fuel: 214 bytes ———
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
// The accumulator s lives in R(aS) (value-stack memory), so the fuel round
// trip preserves it for free; only the xmm loop state needs the spill.
func EmitForLoopWithRegKBody(buf []byte, kS, kInit, kLimit, kStep, kBody uint64,
	aS uint8, sseOp byte,
	preemptFlagOff, loopFuelOff, loopSpillOff int32, loopFuelCode uint64) ([]byte, int) {
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

	// back edge: loopFuel dec + jnz (issue #143) or the plain backward jmp
	if loopFuelOff >= 0 {
		buf = emitPJ3LoopFuelBackEdge(buf, loopStart, loopFuelOff, loopSpillOff, loopFuelCode)
	} else {
		jmpStart := len(buf)
		buf = EmitJmpRel32(buf, int32(loopStart-(jmpStart+EncodedJmpRel32Len)))
	}

	// after_loop label
	afterLoop := len(buf)
	buf = EmitRet(buf)

	// resume entry (issue #143)
	resumeOff := 0
	if loopFuelOff >= 0 {
		buf, resumeOff = emitPJ3LoopFuelResume(buf, loopStart, loopSpillOff)
	}

	// patch forward fixups
	PatchRel32(buf, jbOff, int32(afterLoop)-int32(jbOff+4))
	if safepointJneOff >= 0 {
		PatchRel32(buf, safepointJneOff, int32(afterLoop)-int32(safepointJneOff+4))
	}

	return buf, resumeOff
}

// EncodedForLoopWithRegKBodyWithSafepointLen is the byte count of the
// with-safepoint no-fuel version:
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

// EncodedForLoopWithRegKBodyFullLen is the byte count with both the safepoint
// and the loopFuel machinery enabled (135 + 79 = 214 bytes).
const EncodedForLoopWithRegKBodyFullLen = EncodedForLoopWithRegKBodyWithSafepointLen +
	EncodedPJ3LoopFuelExtraLen

// EmitForLoopWithRegKBody2 assembles a two-op body template: `local s; for
// i=K1,K2 do s = s op1 K3; s = s op2 K4 end; return s`. The body has two
// consecutive reg-K ops sharing the R(aS) register (the intermediate value
// stays in R(aS)), using the xmm3 register across both ops (avoiding a
// load/store round-trip).
//
// Byte layout (with safepoint, no fuel; the issue #143 fuel pair replaces the
// trailing jmp and appends the exhausted tail + resume entry exactly as in
// EmitForLoopEmptyConst):
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
//	  jmp loop_start                                    ; 5 (or the fuel pair)
//	[after_loop]
//	  ret                                              ; 1
//	——— with safepoint: 154 bytes; + fuel: 233 bytes ———
//
// Saves one load+store versus the single-body template (sharing xmm3 across 2
// ops): saves 16 bytes of memory access per iteration.
func EmitForLoopWithRegKBody2(buf []byte, kS, kInit, kLimit, kStep, kBody1, kBody2 uint64,
	aS uint8, sseOp1, sseOp2 byte,
	preemptFlagOff, loopFuelOff, loopSpillOff int32, loopFuelCode uint64) ([]byte, int) {
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

	// back edge: loopFuel dec + jnz (issue #143) or the plain backward jmp
	if loopFuelOff >= 0 {
		buf = emitPJ3LoopFuelBackEdge(buf, loopStart, loopFuelOff, loopSpillOff, loopFuelCode)
	} else {
		jmpStart := len(buf)
		buf = EmitJmpRel32(buf, int32(loopStart-(jmpStart+EncodedJmpRel32Len)))
	}

	afterLoop := len(buf)
	buf = EmitRet(buf)

	// resume entry (issue #143)
	resumeOff := 0
	if loopFuelOff >= 0 {
		buf, resumeOff = emitPJ3LoopFuelResume(buf, loopStart, loopSpillOff)
	}

	PatchRel32(buf, jbOff, int32(afterLoop)-int32(jbOff+4))
	if safepointJneOff >= 0 {
		PatchRel32(buf, safepointJneOff, int32(afterLoop)-int32(safepointJneOff+4))
	}

	return buf, resumeOff
}

// EncodedForLoopWithRegKBody2WithSafepointLen is the byte count of the
// two-op-body with-safepoint no-fuel version (154 bytes): the single-body 135
// plus one more mov imm64 + movq + sseOp group (10+5+4) = 154.
const EncodedForLoopWithRegKBody2WithSafepointLen = EncodedForLoopWithRegKBodyWithSafepointLen +
	EncodedMovRaxImm64Len + EncodedMovqXmmFromRaxLen + EncodedSseBinopLen

// EncodedForLoopWithRegKBody2FullLen is the byte count with both the
// safepoint and the loopFuel machinery enabled (154 + 79 = 233 bytes).
const EncodedForLoopWithRegKBody2FullLen = EncodedForLoopWithRegKBody2WithSafepointLen +
	EncodedPJ3LoopFuelExtraLen
