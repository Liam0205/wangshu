//go:build wangshu_p4 && arm64

// pj2_template_arm64.go —— PJ8 arm64 PJ2 投机 ADD 字节级模板(对位
// amd64 pj2_template.go::EmitArithSpeculativeBinopWithGuard 92 字节 SSE2
// 版的 arm64 端镜像)。
//
// **不真接入**(承 §9.12 剩余工程量明示):arm64 trampoline asm(`x28=Go G
// 不动 / x27=jitContext / x26=valueStackBase`)留物理 self-hosted runner;
// 本批仅做字节级模板拼接 + 字节级单测验布局,为下一阶段真接入提供基础。
//
// **arm64 vs amd64 PJ2 模板对位**:
//   - amd64:guard×2(26 字节×2)+ binop(29 字节)+ deopt(11 字节)= 92 字节
//   - arm64:guard×2(28 字节×2)+ binop(32 字节)+ deopt(20 字节)= 108 字节
//
// arm64 指令固定 4 字节,但 MOV imm64 序列(4×movz/movk = 16 字节)比 amd64
// MOV rax, imm64(10 字节)更长,故总长 +16 字节。
//
// **预设寄存器协议**(承 06-backends.md §4.2 + arm64 trampoline asm 留 PJ8+):
//   - x26 = valueStackBase(对位 amd64 rbx)
//   - x27 = jitContext(对位 amd64 r15)
//   - x28 = Go G(Go runtime 保留,不动)
//   - x0/x1 = scratch 通用寄存器
//   - d0/d1 = 浮点 scratch

package arm64

// qNanBoxBase 是 NaN-box number 上限(承 internal/value/value.go::qNanBoxBase
// = 0xFFF8 << 48)。number raw bits < qNanBoxBase 即合法 number。
const qNanBoxBase uint64 = 0xFFF8_0000_0000_0000

// EmitIsNumberGuardArm64 拼接 arm64「IsNumber guard」字节级序列(对位
// amd64 EmitIsNumberGuard)。验证 R(reg) NaN-box 是 number(< qNanBoxBase),
// 失败跳 deopt(imm19 字偏移)。
//
// 序列(28 字节):
//
//	LDR x0, [x26 + reg*8]    ; 4 字节,load R(reg)
//	MOVZ x1, qNanBoxBase[15:0]  ; 4
//	MOVK x1, qNanBoxBase[31:16] LSL 16  ; 4
//	MOVK x1, qNanBoxBase[47:32] LSL 32  ; 4
//	MOVK x1, qNanBoxBase[63:48] LSL 48  ; 4(= mov x1, qNanBoxBase imm64,共 16 字节)
//	CMP x0, x1               ; 4
//	B.HS deopt (imm19)       ; 4(unsigned >= 跳 deopt,等价 amd64 jae)
//	——— 总计 28 字节 ———
//
// **imm19 字偏移**(arm64 B.cond 是 19-bit 字偏移,对位 EmitBCond):
// imm19 = (deopt 起点 - 本 B.cond 指令地址) / 4。caller 计算后直接写入,
// 无单独 patch 阶段(本模板 deopt 位置编译期已知)。
//
// 用例:PJ2 投机 ADD/SUB/MUL/DIV 双 guard 的每一道。
func EmitIsNumberGuardArm64(buf []byte, reg uint8, imm19 int32) []byte {
	if reg > 254 {
		reg = 0
	}
	// LDR x0, [x26 + reg*8](byteOff <= 32760)
	buf = EmitLdrXtFromXnDisp(buf, 0 /*x0*/, 26 /*x26 vsBase*/, uint16(reg)*8)
	// MOV x1, qNanBoxBase imm64(16 字节)
	buf = EmitMovXdImm64(buf, 1 /*x1*/, qNanBoxBase)
	// CMP x0, x1
	buf = EmitCmpXnXm(buf, 0, 1)
	// B.HS deopt (imm19)
	buf = EmitBCond(buf, CondHS, imm19)
	return buf
}

// EncodedIsNumberGuardArm64Len arm64 IsNumber guard 字节数(4+16+4+4 = 28)。
const EncodedIsNumberGuardArm64Len = EncodedLdrXtFromXnDispLen +
	EncodedMovXdImm64Len + EncodedCmpXnXmLen + EncodedBCondLen

// EmitArithSpeculativeBinopArm64 拼接 arm64 PJ2 投机 BINOP 快路径核心
// (无 guard 段,对位 amd64 EmitArithSpeculativeBinop 29 字节 SSE2 版)。
//
// 序列(32 字节):
//
//	LDR x0, [x26 + B*8]      ; 4(load R(B))
//	FMOV d0, x0              ; 4(GP→FP)
//	LDR x0, [x26 + C*8]      ; 4(load R(C))
//	FMOV d1, x0              ; 4(GP→FP)
//	FADD/FSUB/FMUL/FDIV d0, d0, d1  ; 4(双精度 binop,sseOp 选 add/sub/mul/div)
//	FMOV x0, d0              ; 4(FP→GP)
//	STR x0, [x26 + A*8]      ; 4(store R(A))
//	RET                       ; 4
//	——— 总计 32 字节 ———
//
// **arithOp** 参数:用上 EmitFadd/Fsub/Fmul/Fdiv 函数指针无法字节级精确编
// 码 base,改用「op base 字节」选择:
//   - 0x28 → FADD(0x1E602800)
//   - 0x38 → FSUB(0x1E603800)
//   - 0x08 → FMUL(0x1E600800)
//   - 0x18 → FDIV(0x1E601800)
//
// 实际本批用 emitArithOpArm64 helper 根据 opSel 派发(承下面定义)。
func EmitArithSpeculativeBinopArm64(buf []byte, opSel uint8, a, b, c uint8) []byte {
	if a > 254 {
		a = 0
	}
	if b > 254 {
		b = 0
	}
	if c > 254 {
		c = 0
	}
	// LDR x0, [x26 + B*8] + FMOV d0, x0
	buf = EmitLdrXtFromXnDisp(buf, 0, 26, uint16(b)*8)
	buf = EmitFmovDdFromXn(buf, 0, 0) // fmov d0, x0
	// LDR x0, [x26 + C*8] + FMOV d1, x0
	buf = EmitLdrXtFromXnDisp(buf, 0, 26, uint16(c)*8)
	buf = EmitFmovDdFromXn(buf, 1, 0) // fmov d1, x0
	// op d0, d0, d1
	buf = emitArithOpArm64(buf, opSel, 0, 0, 1)
	// FMOV x0, d0
	buf = EmitFmovXdFromDn(buf, 0, 0) // fmov x0, d0
	// STR x0, [x26 + A*8]
	buf = EmitStrXtToXnDisp(buf, 0, 26, uint16(a)*8)
	buf = EmitRet(buf)
	return buf
}

// EncodedArithSpecBinopArm64Len arm64 PJ2 binop 快路径字节数(8 条 × 4 = 32)。
const EncodedArithSpecBinopArm64Len = 32

// emitArithOpArm64 按 opSel 字节派发到 Fadd/Fsub/Fmul/Fdiv。
//
// opSel 字节(承 EmitArithSpeculativeBinopArm64 godoc):
//
//	0x28 → FADD
//	0x38 → FSUB
//	0x08 → FMUL
//	0x18 → FDIV
//
// 未知 opSel 兜底 FADD。
func emitArithOpArm64(buf []byte, opSel uint8, dd, dn, dm uint8) []byte {
	switch opSel {
	case 0x28:
		return EmitFaddDdDnDm(buf, dd, dn, dm)
	case 0x38:
		return EmitFsubDdDnDm(buf, dd, dn, dm)
	case 0x08:
		return EmitFmulDdDnDm(buf, dd, dn, dm)
	case 0x18:
		return EmitFdivDdDnDm(buf, dd, dn, dm)
	default:
		return EmitFaddDdDnDm(buf, dd, dn, dm)
	}
}

// arm64 PJ2 投机算术 op 选择字节(对位 amd64 SseOpAddsd/Subsd/Mulsd/Divsd)。
// **注**:这是 opcode 判别字节(arm64 浮点 binop 指令格式 bits[15:8] 区分
// FADD/FSUB/FMUL/FDIV),不是 base 的最低字节(base 最低字节恒 0x00)。
const (
	ArithOpAddArm64 uint8 = 0x28 // FADD opcode 判别字节(0x1E60_2800)
	ArithOpSubArm64 uint8 = 0x38 // FSUB opcode 判别字节(0x1E60_3800)
	ArithOpMulArm64 uint8 = 0x08 // FMUL opcode 判别字节(0x1E60_0800)
	ArithOpDivArm64 uint8 = 0x18 // FDIV opcode 判别字节(0x1E60_1800)
)

// EmitArithSpeculativeBinopWithGuardArm64 拼接 PJ2 投机模板完整版(IsNumber
// guard×2 + 双 number 快路径 + deopt block)字节级序列,对位 amd64
// EmitArithSpeculativeBinopWithGuard 92 字节 SSE2 版。
//
// 序列(108 字节):
//
//	[guard-B] 28 字节:LDR R(B) + CMP qNanBoxBase + B.HS deopt
//	[guard-C] 28 字节:LDR R(C) + CMP qNanBoxBase + B.HS deopt
//	[fast]    32 字节:LDR + FMOV + LDR + FMOV + op + FMOV + STR + RET
//	[deopt]   20 字节:MOV x0, deoptCode + RET
//	——— 总计 108 字节 ———
//
// **imm19 计算**(arm64 B.cond 是 19-bit 字偏移,LSL 2 → byte 偏移):
//   - guard1 B.cond 之后 PC = 28
//   - guard2 B.cond 之后 PC = 56
//   - fast 段结束 PC = 88
//   - deopt 起点 PC = 88
//   - imm19_1(guard1→deopt 字偏移)= (88 - 24)/4 = 16(B.cond 在 guard1 末偏移 24)
//   - imm19_2(guard2→deopt 字偏移)= (88 - 52)/4 = 9
//
// 实际计算:imm19 是 B.cond 相对自身指令地址的字偏移(arm64 PC-relative
// 计算 PC = 本条 B.cond 地址,与 amd64 rel32 是 jmp 后 PC 不同)。
//
// 实际 imm19 = (deopt_offset - b_cond_offset) / 4(B.cond 自身字偏移到
// 目标)。
//
//	guard1 B.cond 字偏移 = 24/4 = 6,deopt 字偏移 = 88/4 = 22 → imm19_1 = 22-6 = 16
//	guard2 B.cond 字偏移 = 52/4 = 13 → imm19_2 = 22-13 = 9
//
// 本函数发 placeholder imm19,在拼接时直接写入计算值(无单独 PatchImm19
// 阶段,因 deopt 位置 emit 时已知)。
func EmitArithSpeculativeBinopWithGuardArm64(buf []byte, opSel uint8, a, b, c uint8, deoptCode uint64) []byte {
	// guard 段单段 28 字节,fast 段 32 字节,deopt 起点 = 28*2 + 32 = 88
	// guard1 B.cond 自身位置 = 24(guard1 内 LDR 4 + MOV imm 16 + CMP 4 = 24)
	// imm19_1 = (88 - 24)/4 = 16(字偏移)
	// guard2 B.cond 自身位置 = 28 + 24 = 52
	// imm19_2 = (88 - 52)/4 = 9
	imm19Guard1 := int32(16)
	imm19Guard2 := int32(9)

	buf = EmitIsNumberGuardArm64(buf, b, imm19Guard1)
	buf = EmitIsNumberGuardArm64(buf, c, imm19Guard2)

	// fast 段(32 字节)
	buf = EmitArithSpeculativeBinopArm64(buf, opSel, a, b, c)

	// deopt block(20 字节):MOV x0, deoptCode imm64(16)+ RET(4)
	buf = EmitMovXdImm64(buf, 0, deoptCode)
	buf = EmitRet(buf)

	return buf
}

// EncodedArithSpecBinopWithGuardArm64Len arm64 PJ2 完整投机模板字节数
// (28×2 + 32 + 20 = 108)。
const EncodedArithSpecBinopWithGuardArm64Len = 2*EncodedIsNumberGuardArm64Len +
	EncodedArithSpecBinopArm64Len + EncodedMovXdImm64Len + EncodedRetLen

// EmitArithSpeculativeBinopRegKArm64 拼接 arm64 PJ2 投机 reg-K 快路径
// 核心(无 guard 段,对位 amd64 EmitArithSpeculativeBinopRegK 36 字节)。
//
// 序列(44 字节):
//
//	LDR x0, [x26 + B*8]      ; 4(load R(B))
//	FMOV d0, x0              ; 4(GP→FP)
//	MOV x0, kvalue imm64     ; 16(K NaN-box bits 装入)
//	FMOV d1, x0              ; 4(GP→FP)
//	FADD/FSUB/FMUL/FDIV d0, d0, d1  ; 4(双精度 binop)
//	FMOV x0, d0              ; 4(FP→GP)
//	STR x0, [x26 + A*8]      ; 4(store R(A))
//	RET                       ; 4
//	——— 总计 44 字节 ———
//
// **vs amd64 reg-K 36 字节**:arm64 多 8 字节(MOV imm64 序列 16 vs
// amd64 movq 10 累积 + LDR-then-FMOV vs amd64 movsd 单条多 2 步,
// 但 RISC fixed-length 抵消部分)。
func EmitArithSpeculativeBinopRegKArm64(buf []byte, opSel uint8, a, b uint8, kvalue uint64) []byte {
	if a > 254 {
		a = 0
	}
	if b > 254 {
		b = 0
	}
	// LDR x0, [x26 + B*8] + FMOV d0, x0
	buf = EmitLdrXtFromXnDisp(buf, 0, 26, uint16(b)*8)
	buf = EmitFmovDdFromXn(buf, 0, 0)
	// MOV x0, kvalue + FMOV d1, x0
	buf = EmitMovXdImm64(buf, 0, kvalue)
	buf = EmitFmovDdFromXn(buf, 1, 0)
	// op d0, d0, d1
	buf = emitArithOpArm64(buf, opSel, 0, 0, 1)
	// FMOV x0, d0 + STR R(A)
	buf = EmitFmovXdFromDn(buf, 0, 0)
	buf = EmitStrXtToXnDisp(buf, 0, 26, uint16(a)*8)
	buf = EmitRet(buf)
	return buf
}

// EncodedArithSpecBinopRegKArm64Len arm64 PJ2 reg-K 快路径字节数(44 =
// LDR 4 + FMOV 4 + MOV imm64 16 + FMOV 4 + FOP 4 + FMOV 4 + STR 4 + RET 4)。
const EncodedArithSpecBinopRegKArm64Len = 44

// EmitArithSpeculativeBinopRegKWithGuardArm64 拼接 arm64 PJ2 投机 reg-K
// 完整模板(含 IsNumber guard + deopt 块,对位 amd64
// EmitArithSpeculativeBinopRegKWithGuard 73 字节)。
//
// 字节布局(92 字节):
//
//	[ 0-27]  IsNumber guard R(B)   ; 28(LDR + MOV qNanBoxBase + CMP + B.HS deopt)
//	[28-71]  reg-K 快路径           ; 44(LDR/FMOV + MOV K/FMOV + FOP + FMOV/STR + RET)
//	[72-91]  deopt block            ; 20(MOV x0, deoptCode + RET)
//	——— 总计 92 字节 ———
//
// **vs amd64 reg-K WithGuard 73 字节**:arm64 多 19 字节(guard 28 vs
// amd64 26 +2、fast 44 vs amd64 36 +8、deopt 20 vs amd64 11 +9)。
//
// **预设条件**:R(B) 端 IsNumber guard 失败 → 跳 deopt 块返 deoptCode;
// K 编译期已校验 number,运行期不再 guard(对位 amd64 同款形态)。
func EmitArithSpeculativeBinopRegKWithGuardArm64(buf []byte, opSel uint8,
	a, b uint8, kvalue uint64, deoptCode uint64) []byte {
	// imm19 = (deopt 起点 - guard B.HS 自身) / 4
	// guard B.HS 在 offset 24(LDR+MOV+CMP+B.HS,B.HS 位于 24-27)
	// fast 段长 44,deopt 在 offset 28+44 = 72
	// imm19 = (72 - 24) / 4 = 12
	const guardBHSPos = 24
	const deoptStart = 28 + 44
	imm19Guard := int32(deoptStart-guardBHSPos) / 4

	buf = EmitIsNumberGuardArm64(buf, b, imm19Guard)
	buf = EmitArithSpeculativeBinopRegKArm64(buf, opSel, a, b, kvalue)
	// deopt block:MOV x0, deoptCode + RET
	buf = EmitMovXdImm64(buf, 0, deoptCode)
	buf = EmitRet(buf)
	return buf
}

// EncodedArithSpecBinopRegKWithGuardArm64Len arm64 PJ2 reg-K WithGuard
// 完整字节数(28 + 44 + 20 = 92)。
const EncodedArithSpecBinopRegKWithGuardArm64Len = 92

// EmitArithSpeculativeChainKKWithGuardArm64 拼接 arm64 PJ2 投机
// chain-KK 模板(R(A) = R(B) op1 K1 op2 K2 形态,对位 amd64
// EmitArithSpeculativeChainKKWithGuard 92 字节)。
//
// 字节布局(116 字节):
//
//	[ 0-27]  IsNumber guard R(B)        ; 28
//	[28-95]  fast: LDR/FMOV + K1 段(MOV+FMOV+FOP1)+ K2 段(MOV+FMOV+FOP2)+ FMOV/STR + RET
//	[28-31]  LDR x0, [x26+B*8]          ; 4
//	[32-35]  FMOV d0, x0                ; 4
//	[36-51]  MOV x0, K1 imm64           ; 16
//	[52-55]  FMOV d1, x0                ; 4
//	[56-59]  FOP1 d0, d0, d1            ; 4
//	[60-75]  MOV x0, K2 imm64           ; 16
//	[76-79]  FMOV d1, x0                ; 4(覆盖 d1)
//	[80-83]  FOP2 d0, d0, d1            ; 4
//	[84-87]  FMOV x0, d0                ; 4
//	[88-91]  STR x0, [x26+A*8]          ; 4
//	[92-95]  RET                         ; 4
//	[96-115] deopt block(MOV deopt + RET); 20
//	——— 总计 116 字节 ———
//
// **vs amd64 chain-KK 92 字节**:arm64 多 24 字节(guard +2,fast
// MOV imm64 序列 16×2 vs amd64 movq 10×2 累积 +12,deopt +9,余 +1)。
//
// **chain 优势**:中间值 d0 复用不写回 stack,等价 host.Arith × 2
// 但省一次 boundary 跨界(承 amd64 同款)。
func EmitArithSpeculativeChainKKWithGuardArm64(buf []byte, opSel1, opSel2 uint8,
	a, b uint8, k1value, k2value, deoptCode uint64) []byte {
	// imm19 = (deopt 起点 - guard B.HS 自身) / 4
	// fast 段长 = 4(LDR) + 4(FMOV d0) + 20(MOV K1+FMOV d1) + 4(FOP1)
	//          + 20(MOV K2+FMOV d1) + 4(FOP2) + 4(FMOV x0) + 4(STR) + 4(RET) = 68
	const guardBHSPos = 24
	const fastLen = 68
	const deoptStart = 28 + fastLen
	imm19Guard := int32(deoptStart-guardBHSPos) / 4

	buf = EmitIsNumberGuardArm64(buf, b, imm19Guard)
	if a > 254 {
		a = 0
	}
	if b > 254 {
		b = 0
	}

	// fast path
	buf = EmitLdrXtFromXnDisp(buf, 0, 26, uint16(b)*8)
	buf = EmitFmovDdFromXn(buf, 0, 0)
	// K1 段
	buf = EmitMovXdImm64(buf, 0, k1value)
	buf = EmitFmovDdFromXn(buf, 1, 0)
	buf = emitArithOpArm64(buf, opSel1, 0, 0, 1)
	// K2 段(覆盖 d1)
	buf = EmitMovXdImm64(buf, 0, k2value)
	buf = EmitFmovDdFromXn(buf, 1, 0)
	buf = emitArithOpArm64(buf, opSel2, 0, 0, 1)
	// store R(A) + ret
	buf = EmitFmovXdFromDn(buf, 0, 0)
	buf = EmitStrXtToXnDisp(buf, 0, 26, uint16(a)*8)
	buf = EmitRet(buf)
	// deopt
	buf = EmitMovXdImm64(buf, 0, deoptCode)
	buf = EmitRet(buf)
	return buf
}

// EncodedArithSpecChainKKWithGuardArm64Len arm64 PJ2 chain-KK WithGuard
// 完整字节数(28 + 68 + 20 = 116)。
const EncodedArithSpecChainKKWithGuardArm64Len = 116
