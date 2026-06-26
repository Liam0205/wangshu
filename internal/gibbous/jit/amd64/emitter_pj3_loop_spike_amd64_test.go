//go:build wangshu_p4 && linux && amd64

package amd64

import (
	"testing"
	"unsafe"
)

// emitter_pj3_loop_spike_test.go —— PJ3 字节级 backward jmp + safepoint
// check 物理可行性 spike(承 docs/design/p4-method-jit/05-system-pipeline.md
// §1.2.2 抢占检查点 + §6.3 回边 + 06-backends.md §3.3 数值 for 内联)。
//
// **spike 目标**:在真 mmap+RX 段内 emit 一个简单 counter loop:
//
//   mov rax, 0                 ; 累加器
//   mov rcx, N                 ; 计数
//   loop_start:
//     inc rax                  ; 累加 ++
//     cmp byte [r15+pfOff], 0  ; 读 jitContext.preemptFlag
//     jne after_loop           ; pf!=0 早退出
//     dec rcx                  ; 计数 --
//     jne loop_start           ; 回边 backward jmp
//   after_loop:
//     ret                      ; rax = 累加值
//
// 验证项:
//   ① backward jmp(negative rel32)真在 mmap+RX 跑通
//   ② cmp byte [r15+disp] 真读 jitContext 字段(经 trampoline 装 r15)
//   ③ 中途置 preemptFlag=1 → loop 早退出 → rax < N
//
// **prove-the-path**:本测既验编码字节级正确,又验 backward jmp + r15
// load 的物理行为——这是 PJ3 FORLOOP 字节级 inline 真接入的最核心物理
// 证据。

// 用全局 jitContext heap-allocated(承 05 §1.3.4 + 测试 §3.6 必须 heap)
// 避免 morestack 搬走 *JITContext 指针。

// 用包内 JITContext struct + Offsetof 算 preemptFlag 偏移——但 amd64
// 包不能 import jit 包(否则循环)。手算 offset:JITContext struct 字段
// 顺序 arenaBase / valueStackBase / preemptFlag / exitReasonCode /
// spillBase / spillTop。三 uintptr(24)后是 atomic.Uint32(本质 uint32
// 4 字节,但 padding 对齐 8)→ preemptFlag 偏移 = 24 字节(在 amd64 64-bit
// 平台稳定)。
//
// 为不依赖 jit 包内部布局,本 spike 用一个**本地 mini ctx struct**(同
// 字段布局但定义在测试包内),offset 用 unsafe.Offsetof 算。

type spikeCtx struct {
	_           [3]uintptr // arenaBase + valueStackBase + ...(占位与 JITContext 前几字段对齐)
	preemptFlag uint32     // 实际只取 0/1,byte cmp 即可
}

var spikePreemptOff = int32(unsafe.Offsetof(spikeCtx{}.preemptFlag))

// 全局 heap-allocated ctx——承 prove-the-path「JIT 不持 Go 栈指针」纪律。
var spikeCtxInstance = &spikeCtx{}

// TestPJ3_BackwardJmpLoop_Normal:preemptFlag=0,loop 跑满 N 次,rax=N.
func TestPJ3_BackwardJmpLoop_Normal(t *testing.T) {
	spikeCtxInstance.preemptFlag = 0
	const N = int32(100)

	buf := buildSpikeLoop(t, N, spikePreemptOff)
	page, err := MmapCode(buf)
	if err != nil {
		t.Fatalf("MmapCode: %v", err)
	}
	defer func() { _ = page.Munmap() }()

	ctxAddr := uintptr(unsafe.Pointer(spikeCtxInstance))
	rax := CallJITFull(page.Addr(), ctxAddr)

	if int32(rax) != N {
		t.Errorf("rax=%d, want N=%d(loop 应跑满 N 次)", int32(rax), N)
	}
}

// TestPJ3_BackwardJmpLoop_EarlyExit:preemptFlag=1,loop 第一次 safepoint
// check 即退出,rax=1(累加器只跑一次 inc).
func TestPJ3_BackwardJmpLoop_EarlyExit(t *testing.T) {
	spikeCtxInstance.preemptFlag = 1
	defer func() { spikeCtxInstance.preemptFlag = 0 }()

	const N = int32(100)
	buf := buildSpikeLoop(t, N, spikePreemptOff)
	page, err := MmapCode(buf)
	if err != nil {
		t.Fatalf("MmapCode: %v", err)
	}
	defer func() { _ = page.Munmap() }()

	ctxAddr := uintptr(unsafe.Pointer(spikeCtxInstance))
	rax := CallJITFull(page.Addr(), ctxAddr)

	// preemptFlag=1 → 第一次 inc rax 后 cmp ≠ 0 即跳 after_loop,rax=1
	if int32(rax) != 1 {
		t.Errorf("rax=%d, want 1(preemptFlag=1 应让第一次循环就退出)", int32(rax))
	}
}

// buildSpikeLoop 字节级拼装 counter loop(emit-then-patch 流程实证)。
func buildSpikeLoop(t *testing.T, n int32, pfOff int32) []byte {
	t.Helper()
	var buf []byte

	// 1. mov rax, 0(7 字节,EmitMovReg64Imm32SignExt)
	buf = EmitMovReg64Imm32SignExt(buf, 0 /*rax*/, 0)

	// 2. mov rcx, N(7 字节)
	buf = EmitMovReg64Imm32SignExt(buf, 1 /*rcx*/, n)

	// 3. loop_start label,记录段内偏移
	loopStart := len(buf)

	// 4. inc rax(3 字节)
	buf = EmitIncReg64(buf, 0 /*rax*/)

	// 5. cmp byte [r15 + pfOff], 0(8 字节)
	buf = EmitCmpByteR15DispImm8(buf, pfOff, 0)

	// 6. jne after_loop placeholder rel32=0(6 字节)—— forward fixup
	buf = EmitJneRel32(buf, 0)
	jneForwardRel32Off := len(buf) - 4

	// 7. dec rcx(3 字节)
	buf = EmitDecReg64(buf, 1 /*rcx*/)

	// 8. jne loop_start(6 字节)—— backward jmp,rel32 在此处即可算
	//    backward jcc end = len(buf) + 6
	//    rel32 = loopStart - (jccEnd) = loopStart - (len(buf) + 6)
	backwardRel32 := int32(loopStart - (len(buf) + EncodedJccRel32Len))
	buf = EmitJneRel32(buf, backwardRel32)

	// 9. after_loop label
	afterLoop := len(buf)

	// 10. ret(1 字节)
	buf = EmitRet(buf)

	// 11. patch forward jne(step 6)的 rel32 = afterLoop - (jne_end)
	//     jne_end = jne_start + EncodedJccRel32Len;jne_start = jneForwardRel32Off - 2;
	//     jne_end = jneForwardRel32Off + 4(rel32 起点 + 4)
	forwardRel32 := int32(afterLoop) - int32(jneForwardRel32Off+4)
	PatchRel32(buf, jneForwardRel32Off, forwardRel32)

	return buf
}
