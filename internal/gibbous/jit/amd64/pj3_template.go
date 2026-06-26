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
// 字节布局(承上面文件头注的设计图):
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
//	[57] ja  after_loop             ; 6 bytes (forward jcc;rel32 = +5)
//	[63] jmp loop_start             ; 5 bytes (backward jmp;rel32 = -(49-(63+5)) = -19)
//	[68] ; after_loop
//	[68] ret                        ; 1 byte
//	——— 总长 = 69 字节 ———
//
// **预设条件**:trampoline 装 r15(本模板不读 r15);R(A) idx 槽不写
// (空 body 不需要);返回 rax 是 dummy(段返回后 host 不读)。
func EmitForLoopEmptyConst(buf []byte, kInit, kLimit, kStep uint64) []byte {
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

	return buf
}

// EncodedForLoopEmptyConstLen 是「全常量 init/limit/step + 空 body FORLOOP」
// 字节数:10*3(mov×3) + 5*3(movq×3) + 4(subsd) + 4(addsd) + 4(ucomisd)
// + 6(ja) + 5(jmp) + 1(ret) = 69 字节。
const EncodedForLoopEmptyConstLen = EncodedMovRaxImm64Len*3 +
	EncodedMovqXmmFromRaxLen*3 +
	EncodedSseBinopLen + // subsd
	EncodedSseBinopLen + // addsd
	EncodedUcomisdLen + // ucomisd
	EncodedJccRel32Len + // ja
	EncodedJmpRel32Len + // jmp
	EncodedRetLen
