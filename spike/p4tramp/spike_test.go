//go:build linux && amd64

package p4tramp

import (
	"testing"
)

// TestSpike_S1_RoundTrip 闸门 ①②③:exec mmap + W^X 翻面 + Go → mmap → ret
// 最小 round-trip,验证 mmap 段返回值 == EmitMovRaxImm64Ret 烧入的 imm64。
//
// 这是 PJ1 spike 的根基测试——若失败,P4 amd64 后端的物理基础(JIT 段执行)
// 不成立,emitter trait 不应落地(承 06 §1.7)。
func TestSpike_S1_RoundTrip(t *testing.T) {
	cases := []uint64{
		0,
		1,
		0xdeadbeef,
		0xcafebabedeadbeef,
		^uint64(0),
	}
	for _, imm := range cases {
		t.Run("", func(t *testing.T) {
			code := EmitMovRaxImm64Ret(imm)
			if len(code) != 11 {
				t.Fatalf("encoded length should be 11 bytes, got %d", len(code))
			}
			page, err := MmapCode(code)
			if err != nil {
				t.Fatalf("MmapCode failed: %v", err)
			}
			defer func() {
				if err := page.Munmap(); err != nil {
					t.Errorf("Munmap failed: %v", err)
				}
			}()

			got := CallJIT(page.Addr())
			if got != imm {
				t.Errorf("CallJIT returned 0x%x, expected 0x%x", got, imm)
			}
		})
	}
}

// TestSpike_S2_RepeatedCalls 闸门 ②对称性:同一 mmap 段多次 CALL 不污染状态。
//
// 模拟「同一 Proto 编译产物被多次调」场景——PJ1 完整 trampoline 要保证多次
// 进出不踩 runtime / 不泄漏栈 / 不动 Go 调度。本 spike 极简版不切 SP,但仍
// 需验证「多次 round-trip 返回值稳定」是 trampoline 设计无副作用的最低要求。
func TestSpike_S2_RepeatedCalls(t *testing.T) {
	imm := uint64(0xfeedface00c0ffee)
	code := EmitMovRaxImm64Ret(imm)
	page, err := MmapCode(code)
	if err != nil {
		t.Fatalf("MmapCode failed: %v", err)
	}
	defer func() { _ = page.Munmap() }()

	const N = 10000
	for i := 0; i < N; i++ {
		got := CallJIT(page.Addr())
		if got != imm {
			t.Fatalf("call #%d: got 0x%x, want 0x%x", i, got, imm)
		}
	}
}

// TestSpike_S3_MultiplePages 闸门 ②隔离性:同时持多段 mmap,各自独立返回值。
//
// 模拟「多 Proto 各自编译产物」场景。
func TestSpike_S3_MultiplePages(t *testing.T) {
	pages := make([]*CodePage, 8)
	imms := make([]uint64, 8)
	for i := range pages {
		imms[i] = uint64(0x10000+i) << 32 // 高位差异,易区分
		code := EmitMovRaxImm64Ret(imms[i])
		var err error
		pages[i], err = MmapCode(code)
		if err != nil {
			t.Fatalf("page %d MmapCode failed: %v", i, err)
		}
	}
	defer func() {
		for _, p := range pages {
			_ = p.Munmap()
		}
	}()

	// 交叉调用:不顺序、不只调一次,验证段间无串扰
	for round := 0; round < 100; round++ {
		for i := range pages {
			j := (i + round) % len(pages)
			got := CallJIT(pages[j].Addr())
			if got != imms[j] {
				t.Errorf("round %d page %d: got 0x%x, want 0x%x", round, j, got, imms[j])
			}
		}
	}
}

// BenchmarkSpike_CallJIT 闸门 ④:Go → mmap 段 → ret 单次成本测量(承 spike 范本)。
//
// 对位 P3 spike S1 空往返 18.9ns(承 docs/design/p3-wasm-tier/implementation-progress.md
// §0.1)——P4 mmap 段单 CALL 比 wazero 跨界更便宜的物理基础是「不经
// wazero/Wasm 中介」。本 bench 给 PJ1 完整 trampoline 的成本基线。
//
// 形态:发射「mov rax, IMM; ret」9 字节段(直线;P4 真实 LOADK 模板形态),Go
// 经 CallJIT 反复调,b.N 循环内均摊。
func BenchmarkSpike_CallJIT(b *testing.B) {
	imm := uint64(0xdeadbeef)
	code := EmitMovRaxImm64Ret(imm)
	page, err := MmapCode(code)
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
