//go:build wangshu_p4 && linux && amd64

package amd64

import "testing"

// TestPJ1_Emitter_MovRaxRet 端到端实证:Emitter 发射「mov rax, imm; ret」
// 序列 → MmapCode 翻面 → CallJIT 拿到 imm。对位 spike/p4tramp::TestSpike_S1,
// 但本测试在主库 internal/gibbous/jit/amd64 内,意义是「spike 闸门 ✅ → 主库
// emitter 工作」。
//
// 这是 PJ1 验收口径(00 §4 PJ1 行 + 06 §6.1)的最小实证:LOADK + RETURN
// 直线 Proto 经 P4 amd64 后端编译产物 → mmap 段 → callJIT → byte-equal
// crescent 解释结果(本测试只验前半段:「imm 经发射→执行→返回」管线工作)。
func TestPJ1_Emitter_MovRaxRet(t *testing.T) {
	cases := []uint64{
		0,
		1,
		0xdeadbeef,
		0xcafebabedeadbeef,
		^uint64(0),
	}
	for _, imm := range cases {
		t.Run("", func(t *testing.T) {
			// 发射:[mov rax, imm; ret]
			var buf []byte
			buf = EmitMovRaxImm64(buf, imm)
			buf = EmitRet(buf)
			if len(buf) != EncodedMovRaxImm64Len+EncodedRetLen {
				t.Fatalf("encoded length should be %d bytes, got %d",
					EncodedMovRaxImm64Len+EncodedRetLen, len(buf))
			}

			// W^X 翻面 + mmap
			page, err := MmapCode(buf)
			if err != nil {
				t.Fatalf("MmapCode failed: %v", err)
			}
			defer func() {
				if err := page.Munmap(); err != nil {
					t.Errorf("Munmap failed: %v", err)
				}
			}()

			// trampoline: Go → mmap → ret
			got := CallJIT(page.Addr())
			if got != imm {
				t.Errorf("CallJIT returned 0x%x, expected 0x%x", got, imm)
			}
		})
	}
}

// TestPJ1_RepeatedCalls 同段反复调返回值稳定(承 spike S2 同款形态,主库
// 镜像)。
func TestPJ1_RepeatedCalls(t *testing.T) {
	imm := uint64(0xfeedface00c0ffee)
	var buf []byte
	buf = EmitMovRaxImm64(buf, imm)
	buf = EmitRet(buf)

	page, err := MmapCode(buf)
	if err != nil {
		t.Fatalf("MmapCode failed: %v", err)
	}
	defer func() { _ = page.Munmap() }()

	const N = 1000
	for i := 0; i < N; i++ {
		got := CallJIT(page.Addr())
		if got != imm {
			t.Fatalf("call #%d: got 0x%x, want 0x%x", i, got, imm)
		}
	}
}

// BenchmarkPJ1_CallJIT 单 CALL 成本基线(主库版本,对位 spike Bench 的
// 1.95ns/op 实测)。
//
// 本 bench 是 PJ1 阶段的性能基线起点;PJ2-PJ5 渐进扩 trampoline(切 SP +
// callee-saved 等)时本 bench 数字会上浮——「PJ1 简化形态 vs 完整 trampoline」
// 的成本差正是 PJ1+ 工程取舍的实证依据。
func BenchmarkPJ1_CallJIT(b *testing.B) {
	imm := uint64(0xdeadbeef)
	var buf []byte
	buf = EmitMovRaxImm64(buf, imm)
	buf = EmitRet(buf)

	page, err := MmapCode(buf)
	if err != nil {
		b.Fatalf("MmapCode failed: %v", err)
	}
	defer func() { _ = page.Munmap() }()

	addr := page.Addr()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		got := CallJIT(addr)
		if got != imm {
			b.Fatalf("call #%d: got 0x%x, want 0x%x", i, got, imm)
		}
	}
}
