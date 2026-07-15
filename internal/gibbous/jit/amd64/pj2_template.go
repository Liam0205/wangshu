//go:build wangshu_p4 && amd64

// pj2_template.go — byte-level assembly of the P4 PJ2 speculative ADD
// two-number template (per docs/design/p4-method-jit/03-speculation-ic.md
// §2 IsNumber×2 speculation template).
//
// **Scope**: this file is only byte-level template assembly + unit tests
// verifying the encoding — it does **not** wire into the SupportsAllOpcodes
// whitelist. Full integration requires:
//   1. trampoline switches SP to jitContext.spillBase (est. +0.5 person-month)
//   2. valueStackBase loaded into a callee-saved register (rbx) + correct
//      value setup at the Run entry
//   3. NaN-box constant (qNanBoxBase = 0xFFF8_0000_0000_0000) burned into the
//      segment as imm64
//   4. OSR exit path: on guard failure, jump out of the segment + trampoline
//      detects exitReasonCode and falls back to the host.Arith slow path
//
// Each step above requires real on-machine amd64 mmap+RX+execute debugging +
// gdb tracing; this session only lands byte assembly + unit tests against the
// ISA docs. **Real full integration est. +1-2 person-months** (per
// implementation-progress.md PJ11 gap analysis).

package amd64

// SSE binop opcode bytes — F2 0F <op> C1 form (ADDSD/SUBSD/MULSD/DIVSD
// xmm0, xmm1). Per Intel SDM Vol 2:
//   - ADDSD = F2 0F 58
//   - MULSD = F2 0F 59
//   - SUBSD = F2 0F 5C
//   - DIVSD = F2 0F 5E
const (
	SseOpAddsd byte = 0x58
	SseOpMulsd byte = 0x59
	SseOpSubsd byte = 0x5C
	SseOpDivsd byte = 0x5E
)

// EmitArithSpeculativeBinop assembles the byte-level sequence for the
// "two-number speculative BINOP A B C template".
//
// Form (ADD as example):
//
//	movsd xmm0, [rbx + B*8]    ; load R(B) into xmm0 (8 bytes)
//	movsd xmm1, [rbx + C*8]    ; load R(C) into xmm1 (8 bytes)
//	<sseOp> xmm0, xmm1         ; xmm0 OP xmm1 (4 bytes; OP = add/sub/mul/div)
//	movsd [rbx + A*8], xmm0    ; store back to R(A) (8 bytes)
//	ret                        ; segment return (1 byte)
//	——— 29 bytes total ———
//
// **Preconditions**:
//   - rbx = valueStackBase (loaded in the callJITSpec trampoline)
//   - R(B), R(C) are both number (IsNumber guard already passed — this
//     template assumes the two-number fast path; the deopt jcc for the
//     failure path is emitted in the WithGuard version)
//
// Parameters: sseOp is the SSE opcode byte (SseOpAddsd/Subsd/Mulsd/Divsd);
// a/b/c are register numbers [0,254]; buf is the target to append bytes to.
//
// Returns buf after appending.
func EmitArithSpeculativeBinop(buf []byte, sseOp byte, a, b, c uint8) []byte {
	// Assumes valueStackBase is in rbx, reg*8 is the disp32
	buf = EmitMovsdXmmFromMem(buf, 0, 3 /* rbx */, int32(b)*8) // movsd xmm0, [rbx + B*8]
	buf = EmitMovsdXmmFromMem(buf, 1, 3 /* rbx */, int32(c)*8) // movsd xmm1, [rbx + C*8]
	// SSE binop xmm0, xmm1: F2 0F <op> C0|(0<<3)|1 = F2 0F <op> C1
	buf = append(buf, 0xF2, 0x0F, sseOp, 0xC1)
	buf = EmitMovsdMemFromXmm(buf, 0, 3 /* rbx */, int32(a)*8) // movsd [rbx + A*8], xmm0
	buf = EmitRet(buf)                                         // ret
	return buf
}

// EmitArithSpeculativeAdd is a backward-compatible wrapper for
// EmitArithSpeculativeBinop(SseOpAddsd, ...). New code should call
// EmitArithSpeculativeBinop and pass the op directly.
func EmitArithSpeculativeAdd(buf []byte, a, b, c uint8) []byte {
	return EmitArithSpeculativeBinop(buf, SseOpAddsd, a, b, c)
}

// EncodedArithSpecAddLen is the EmitArithSpeculativeAdd byte count:
// 8 + 8 + 4 + 8 + 1 = 29 bytes.
const EncodedArithSpecAddLen = EncodedMovsdMemLen + EncodedMovsdMemLen +
	EncodedSseBinopLen + EncodedMovsdMemLen + EncodedRetLen

// qNanBoxBaseConst is the lower bound of the NaN-box non-number range (per
// internal/value/value.go::qNanBoxBase = 0xFFF8_0000_0000_0000). A value less
// than this is a number.
const qNanBoxBaseConst uint64 = 0xFFF8_0000_0000_0000

// EmitIsNumberGuard emits an IsNumber guard: load [rbx+regOff*8] into rax,
// cmp against qNanBoxBase (loaded via rcx); if greater-or-equal (unsigned >=)
// it is not a number, so jump to deoptRel32 (a rel32 offset relative to the PC
// right after this jcc).
//
// Byte sequence:
//
//	mov rax, [rbx + reg*8]    ; 7 bytes (EncodedMovqFromMemRegLen)
//	mov rcx, qNanBoxBase      ; 10 bytes (EncodedMovRcxImm64Len)
//	cmp rax, rcx              ; 3 bytes (EncodedCmpRaxRcxLen)
//	jae deopt_rel32           ; 6 bytes (EncodedJccRel32Len)
//	—— 26 bytes total ——
//
// **Note**: this template only emits bytes and does not compute the deopt
// rel32 offset — the caller (the full PJ2 codegen) must patch back
// deoptRel32 = (relative offset of the deopt label) once the whole segment
// length is known. The current stub uses 0; patch it during real integration.
func EmitIsNumberGuard(buf []byte, reg uint8, deoptRel32 int32) []byte {
	buf = EmitMovqRaxFromMemReg(buf, 3 /* rbx */, int32(reg)*8) // mov rax, [rbx+reg*8]
	buf = EmitMovRcxImm64(buf, qNanBoxBaseConst)                // mov rcx, qNanBoxBase
	buf = EmitCmpRaxRcx(buf)                                    // cmp rax, rcx
	buf = EmitJaeRel32(buf, deoptRel32)                         // jae deopt
	return buf
}

// EncodedIsNumberGuardLen is the EmitIsNumberGuard byte count (7+10+3+6 = 26).
const EncodedIsNumberGuardLen = EncodedMovqFromMemRegLen + EncodedMovRcxImm64Len +
	EncodedCmpRaxRcxLen + EncodedJccRel32Len

// EmitArithSpeculativeBinopWithGuard assembles the complete speculation
// template "IsNumber guard ×2 + two-number BINOP fast path + ret" (generic
// version; sseOp picks add/sub/mul/div).
//
// Sequence (ADD as example):
//
//	[guard-B] mov rax, [rbx+B*8] ; mov rcx, qNanBoxBase ; cmp ; jae deopt
//	[guard-C] mov rax, [rbx+C*8] ; mov rcx, qNanBoxBase ; cmp ; jae deopt
//	movsd xmm0, [rbx+B*8]
//	movsd xmm1, [rbx+C*8]
//	<sseOp> xmm0, xmm1
//	movsd [rbx+A*8], xmm0
//	ret
//	[deopt:] mov rax, deoptCode ; ret
//
// Total length = 26*2 + 29 + 11 (deopt block) = 92 bytes (independent of
// sseOp — every SSE binop is F2 0F <op> C1 = 4 bytes, so the template byte
// layout is unchanged).
//
// **Note**: the guard rel32 is patched during assembly to the relative offset
// that "jumps to the start of the deopt block". deoptCode is the OSR exit
// reason (per 04-osr-deopt.md); after leaving the segment, the caller detects
// != 0 and takes the host helper slow path.
func EmitArithSpeculativeBinopWithGuard(buf []byte, sseOp byte, a, b, c uint8, deoptCode uint64) []byte {
	startLen := len(buf)

	// Compute the deopt block offset: guard×2 + ADD template (dropping the
	// trailing single ret byte, since deopt follows right after the segment tail).
	// Actual algorithm: first write guard×2 + ADD template + final ret, then the
	// deopt block follows right after.
	// rel32 = (deopt start) - (PC after jcc) = total - jcc_end_offset
	guardLen := EncodedIsNumberGuardLen
	addLen := EncodedArithSpecAddLen
	// guard1 jcc end = startLen + guardLen
	// guard2 jcc end = startLen + 2*guardLen
	// end of add segment = startLen + 2*guardLen + addLen
	// deopt start = startLen + 2*guardLen + addLen
	// PC after jcc1 = startLen + guardLen
	// PC after jcc2 = startLen + 2*guardLen
	rel1 := int32(2*guardLen + addLen - guardLen) // = guardLen + addLen
	rel2 := int32(addLen)                         // = addLen (jcc2 jumps to deopt start)

	buf = EmitIsNumberGuard(buf, b, rel1)
	// rel2 for the second guard: from the guard2 jcc to the deopt start = addLen
	buf = EmitIsNumberGuard(buf, c, rel2)

	// BINOP fast path (29 bytes)
	buf = EmitArithSpeculativeBinop(buf, sseOp, a, b, c)

	// deopt block: mov rax, deoptCode; ret (11 bytes)
	buf = EmitMovRaxImm64(buf, deoptCode)
	buf = EmitRet(buf)

	_ = startLen
	return buf
}

// EmitArithSpeculativeAddWithGuard is the ADD-form backward-compatible wrapper
// for EmitArithSpeculativeBinopWithGuard (per the existing PJ2 ADD integration
// path). New code should call EmitArithSpeculativeBinopWithGuard and pass the
// op directly.
func EmitArithSpeculativeAddWithGuard(buf []byte, a, b, c uint8, deoptCode uint64) []byte {
	return EmitArithSpeculativeBinopWithGuard(buf, SseOpAddsd, a, b, c, deoptCode)
}

// EmitArithSpeculativeBinopRegK assembles the byte-level sequence for the
// "reg + constant speculative BINOP A B kvalue template" (B is a reg ∈ [0,254],
// kvalue is the NaN-box raw bits burned in at compile time).
//
// Form (ADD as example, kvalue = NumberValue(K).bits()):
//
//	movsd xmm0, [rbx + B*8]        ; load R(B) into xmm0 (8 bytes)
//	mov rax, imm64=kvalue           ; burn K constant raw bits into rax (10 bytes)
//	movq xmm1, rax                  ; rax → xmm1 (5 bytes)
//	<sseOp> xmm0, xmm1              ; xmm0 OP xmm1 (4 bytes)
//	movsd [rbx + A*8], xmm0         ; store back to R(A) (8 bytes)
//	ret                             ; segment return (1 byte)
//	——— 36 bytes total ———
//
// **Preconditions**:
//   - rbx = valueStackBase (loaded in the callJITSpec trampoline)
//   - R(B) must be a number (IsNumber guard already passed — this template
//     assumes the reg side is a number, and the constant K has been validated
//     as a number at compile time; the deopt jcc for the failure path is
//     emitted in the WithGuard version)
//
// Parameters: sseOp is the SSE opcode byte; a/b are register numbers [0,254];
// kvalue is the raw NaN-box bits of K[c] (computed by the caller via
// value.NumberValue(K).bits() and burned directly via mov rax,imm64).
//
// Returns buf after appending.
func EmitArithSpeculativeBinopRegK(buf []byte, sseOp byte, a, b uint8, kvalue uint64) []byte {
	buf = EmitMovsdXmmFromMem(buf, 0, 3 /* rbx */, int32(b)*8) // movsd xmm0, [rbx + B*8]
	buf = EmitMovRaxImm64(buf, kvalue)                         // mov rax, imm64=kvalue (10)
	buf = EmitMovqXmmFromRax(buf, 1)                           // movq xmm1, rax (5)
	buf = append(buf, 0xF2, 0x0F, sseOp, 0xC1)                 // <sseOp> xmm0, xmm1
	buf = EmitMovsdMemFromXmm(buf, 0, 3 /* rbx */, int32(a)*8) // movsd [rbx + A*8], xmm0
	buf = EmitRet(buf)                                         // ret
	return buf
}

// EncodedArithSpecBinopRegKLen is the reg-K template byte count:
// 8 (movsd xmm0,mem) + 10 (mov rax,imm64 = REX.W 48 B8 + 8 bytes)
// + 5 (movq xmm1,rax) + 4 (sse binop F2 0F op C1) + 8 (movsd mem,xmm0)
// + 1 (ret) = 36 bytes.
const EncodedArithSpecBinopRegKLen = EncodedMovsdMemLen + EncodedMovRaxImm64Len +
	EncodedMovqXmmFromRaxLen + EncodedSseBinopLen + EncodedMovsdMemLen + EncodedRetLen

// EmitArithSpeculativeBinopRegKWithGuard assembles the reg-K speculation
// template with a guard (only the reg side needs a number guard; K was already
// validated at compile time and needs no runtime guard).
//
// Sequence:
//
//	[guard-B] mov rax, [rbx+B*8] ; mov rcx, qNanBoxBase ; cmp ; jae deopt
//	movsd xmm0, [rbx+B*8]
//	mov rax, imm64=kvalue
//	movq xmm1, rax
//	<sseOp> xmm0, xmm1
//	movsd [rbx+A*8], xmm0
//	ret
//	[deopt:] mov rax, deoptCode ; ret
//
// Total length = 26 (guard×1) + 36 (fast path) + 11 (deopt) = 73 bytes.
//
// 19 bytes shorter than reg-reg WithGuard (92 bytes) — only guards one reg side.
func EmitArithSpeculativeBinopRegKWithGuard(buf []byte, sseOp byte, a, b uint8, kvalue uint64, deoptCode uint64) []byte {
	fastLen := EncodedArithSpecBinopRegKLen
	// guard1 jcc end = startLen + guardLen
	// end of fast segment = startLen + guardLen + fastLen
	// deopt start = startLen + guardLen + fastLen
	// PC after jcc1 = startLen + guardLen
	// rel1 = (deopt start) - (PC after jcc1) = fastLen
	rel1 := int32(fastLen)

	buf = EmitIsNumberGuard(buf, b, rel1)

	// fast path (reg-K form, 36 bytes)
	buf = EmitArithSpeculativeBinopRegK(buf, sseOp, a, b, kvalue)

	// deopt block: mov rax, deoptCode; ret (11 bytes)
	buf = EmitMovRaxImm64(buf, deoptCode)
	buf = EmitRet(buf)

	return buf
}

// EncodedArithSpecBinopRegKWithGuardLen is the reg-K WithGuard byte count:
// 26 + 36 + 11 = 73 bytes.
const EncodedArithSpecBinopRegKWithGuardLen = EncodedIsNumberGuardLen +
	EncodedArithSpecBinopRegKLen + EncodedMovRaxImm64Len + EncodedRetLen

// EmitArithSpeculativeChainKKWithGuard assembles the byte-level sequence for
// the "two-stage arithmetic chained reg-K-K speculation template" — form
// `R(A) = R(B) op1 K1 op2 K2` (what luac compiles for `x*2+1` etc.).
//
// Byte layout (MUL+ADD as example, i.e. x*K1+K2):
//
//	[guard-B]   mov rax,[rbx+B*8]; mov rcx,qNanBox; cmp; jae deopt  (26)
//	movsd xmm0, [rbx+B*8]                                          (8)
//	mov rax, K1_value; movq xmm1, rax; <sseOp1> xmm0, xmm1         (10+5+4)
//	mov rax, K2_value; movq xmm1, rax; <sseOp2> xmm0, xmm1         (10+5+4)
//	movsd [rbx+A*8], xmm0                                          (8)
//	ret                                                            (1)
//	[deopt:] mov rax, deoptCode; ret                               (11)
//	——— 26 + 8 + 19 + 19 + 8 + 1 + 11 = 92 bytes total ———
//
// Same length as reg-reg WithGuard (92 bytes) but different semantics — here it
// performs two SSE binops, equivalent to host.Arith × 2, saving one boundary
// crossing + reg-stack round trip.
//
// **Preconditions**: K1/K2 are already validated as number at compile time and
// are not guarded at runtime; only R(B) is guarded to be a number.
// chainB == retA (the intermediate value is reused via xmm0 and not written
// back to the stack).
func EmitArithSpeculativeChainKKWithGuard(buf []byte, sseOp1, sseOp2 byte, a, b uint8, k1value, k2value, deoptCode uint64) []byte {
	// Fast path layout length: 8 (movsd load) + 19 (K1 + sseOp1) + 19 (K2 + sseOp2)
	//                        + 8 (movsd store) + 1 (ret) = 55 bytes
	fastLen := EncodedMovsdMemLen + // movsd xmm0, [rbx+B*8]
		(EncodedMovRaxImm64Len + EncodedMovqXmmFromRaxLen + EncodedSseBinopLen) + // K1 + sseOp1
		(EncodedMovRaxImm64Len + EncodedMovqXmmFromRaxLen + EncodedSseBinopLen) + // K2 + sseOp2
		EncodedMovsdMemLen + // movsd [rbx+A*8], xmm0
		EncodedRetLen

	// rel1 = (deopt start) - (PC after jcc1) = fastLen
	// (deopt start = guardEnd + fastLen, PC after jcc1 = guardEnd)
	rel1 := int32(fastLen)

	buf = EmitIsNumberGuard(buf, b, rel1)

	// fast path
	buf = EmitMovsdXmmFromMem(buf, 0, 3 /* rbx */, int32(b)*8) // movsd xmm0, [rbx+B*8]
	// stage 1: xmm0 = xmm0 op1 K1
	buf = EmitMovRaxImm64(buf, k1value)
	buf = EmitMovqXmmFromRax(buf, 1)
	buf = append(buf, 0xF2, 0x0F, sseOp1, 0xC1) // <sseOp1> xmm0, xmm1
	// stage 2: xmm0 = xmm0 op2 K2
	buf = EmitMovRaxImm64(buf, k2value)
	buf = EmitMovqXmmFromRax(buf, 1)
	buf = append(buf, 0xF2, 0x0F, sseOp2, 0xC1) // <sseOp2> xmm0, xmm1
	// store back
	buf = EmitMovsdMemFromXmm(buf, 0, 3 /* rbx */, int32(a)*8) // movsd [rbx+A*8], xmm0
	buf = EmitRet(buf)

	// deopt block
	buf = EmitMovRaxImm64(buf, deoptCode)
	buf = EmitRet(buf)

	return buf
}

// EncodedArithSpecChainKKWithGuardLen is the two-stage chained reg-K-K template
// byte count: 26 (guard) + 55 (fast) + 11 (deopt) = 92 bytes.
const EncodedArithSpecChainKKWithGuardLen = EncodedIsNumberGuardLen +
	EncodedMovsdMemLen +
	(EncodedMovRaxImm64Len + EncodedMovqXmmFromRaxLen + EncodedSseBinopLen) +
	(EncodedMovRaxImm64Len + EncodedMovqXmmFromRaxLen + EncodedSseBinopLen) +
	EncodedMovsdMemLen +
	EncodedRetLen +
	EncodedMovRaxImm64Len + EncodedRetLen

// EncodedArithSpecAddWithGuardLen is the full speculative ADD template byte
// count (including IsNumber×2 guard + fast path + deopt block):
// 26*2 + 29 + 11 = 92.
const EncodedArithSpecAddWithGuardLen = 2*EncodedIsNumberGuardLen +
	EncodedArithSpecAddLen + EncodedMovRaxImm64Len + EncodedRetLen
