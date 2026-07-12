//go:build wangshu_p4 && linux && amd64

package amd64

import (
	"math"
	"runtime"
	"testing"
)

// pj3_template_amd64_test.go —— PJ3 FORLOOP 模板真 mmap+RX round-trip
// 验证。承 docs/design/p4-method-jit/05-system-pipeline.md §6.3。

// TestPJ3_ForLoopEmptyConst_RoundTrip:`for i=1,100 do end` 全常量空 body,
// 段执行后 rax 不被读(模板末 ret 时 rax 状态不重要,主要验证段 RET 正常)。
//
// 实际验证项:模板**真在 mmap+RX 跑通 99 次回边 + 末次 ja 退出**——
// 若 backward jmp / ucomisd / ja 有任一字节错,会 SIGSEGV / SIGILL /
// 死循环(测试 timeout)。本测试通过 = 物理可行性硬证据。
func TestPJ3_ForLoopEmptyConst_RoundTrip(t *testing.T) {
	cases := []struct {
		init, limit, step float64
		name              string
	}{
		{1, 100, 1, "1..100 step 1"},
		{1, 10, 1, "1..10 step 1"},
		{0, 0, 1, "0..0 step 1(单次)"},
		{1, 1, 1, "1..1 step 1(单次)"},
		{1, 1000, 1, "1..1000 step 1"},
		{1, 10, 2, "1..10 step 2"},
		{1, 100, 0.5, "1..100 step 0.5(200 次)"},
		// #117/#118 unordered-exit pins: a NaN in limit or init must
		// exit on the FIRST compare. Current emitter form:
		// `ucomisd limit, idx; jb after_loop` -- ucomisd sets
		// CF=ZF=PF=1 on unordered, so jb (CF=1) TAKES the exit jump.
		// (The original bug was the inverted `ucomisd idx, limit;
		// ja exit`: ja needs CF=0&&ZF=0, false on unordered, so the
		// segment spun forever.) Source-level carriers no longer
		// exist -- PUC-parity constant folding refuses to fold 0%0,
		// so a NaN can never reach a Proto const slot from source --
		// but the emitters still accept arbitrary bits and must stay
		// unordered-safe.
		{1, math.NaN(), 1, "NaN limit(unordered 立即退出)"},
		{math.NaN(), 10, 1, "NaN init(unordered 立即退出)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf []byte
			buf = EmitForLoopEmptyConst(buf,
				math.Float64bits(tc.init),
				math.Float64bits(tc.limit),
				math.Float64bits(tc.step),
				-1 /* no safepoint check */)

			if len(buf) != EncodedForLoopEmptyConstLen {
				t.Fatalf("buf len=%d, want %d", len(buf), EncodedForLoopEmptyConstLen)
			}

			page, err := MmapCode(buf)
			if err != nil {
				t.Fatalf("MmapCode: %v", err)
			}
			defer func() { _ = page.Munmap() }()

			// CallJITSpec 不读 vsBase(模板不寻址 rbx);用 0 即可,但保险传
			// 个 dummy 非 0 地址避免万一段读 rbx
			dummyStack := make([]uint64, 4)
			vsBase := uintptr(0)
			_ = vsBase
			rax := CallJITSpec(page.Addr(), 0, uintptr(0))
			runtime.KeepAlive(dummyStack)

			// rax 是段返回值,本模板末 ret 时 rax 不是有意义值(可能是 ja
			// 之前 ucomisd 的 flag 影响后的 rax,或上层 trampoline 保留)。
			// 我们只验段正常 ret——若 backward jmp 死循环 → testing
			// timeout;若字节错 → SIGSEGV。
			t.Logf("%s: rax=0x%x(段正常退出,backward jmp + ja 字节级跑通)",
				tc.name, rax)
		})
	}
}
