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

// TestPJ2_SSE_Encoding 验 PJ2 字节级算术 SSE 指令编码符合 Intel x86-64 ISA。
//
// 不真执行(完整 mmap+RX 执行需 jitContext 切 SP + 寄存器分配 codegen,
// 留 PJ2-PJ5 完整版),只断言字节编码与 ISA 文档一致。
func TestPJ2_SSE_Encoding(t *testing.T) {
	// MOVSD xmm0, [rax+0]:F2 0F 10 00 + disp32=0(4 字节)= 8 字节
	t.Run("MovsdXmmFromMem_xmm0_rax_0", func(t *testing.T) {
		var buf []byte
		buf = EmitMovsdXmmFromMem(buf, 0, 0, 0)
		want := []byte{0xF2, 0x0F, 0x10, 0x80, 0x00, 0x00, 0x00, 0x00}
		if !bytesEqual(buf, want) {
			t.Errorf("MOVSD xmm0,[rax+0] = %x, want %x", buf, want)
		}
	})

	// MOVSD xmm1, [rcx+8]:F2 0F 10 89 08 00 00 00
	t.Run("MovsdXmmFromMem_xmm1_rcx_8", func(t *testing.T) {
		var buf []byte
		buf = EmitMovsdXmmFromMem(buf, 1, 1, 8)
		want := []byte{0xF2, 0x0F, 0x10, 0x89, 0x08, 0x00, 0x00, 0x00}
		if !bytesEqual(buf, want) {
			t.Errorf("MOVSD xmm1,[rcx+8] = %x, want %x", buf, want)
		}
	})

	// MOVSD [rax+0], xmm0:F2 0F 11 80 + disp32=0
	t.Run("MovsdMemFromXmm_xmm0_rax_0", func(t *testing.T) {
		var buf []byte
		buf = EmitMovsdMemFromXmm(buf, 0, 0, 0)
		want := []byte{0xF2, 0x0F, 0x11, 0x80, 0x00, 0x00, 0x00, 0x00}
		if !bytesEqual(buf, want) {
			t.Errorf("MOVSD [rax+0],xmm0 = %x, want %x", buf, want)
		}
	})

	// ADDSD xmm0, xmm1:F2 0F 58 C1
	t.Run("AddsdXmmXmm_xmm0_xmm1", func(t *testing.T) {
		var buf []byte
		buf = EmitAddsdXmmXmm(buf, 0, 1)
		want := []byte{0xF2, 0x0F, 0x58, 0xC1}
		if !bytesEqual(buf, want) {
			t.Errorf("ADDSD xmm0,xmm1 = %x, want %x", buf, want)
		}
	})

	// SUBSD xmm0, xmm1:F2 0F 5C C1
	t.Run("SubsdXmmXmm_xmm0_xmm1", func(t *testing.T) {
		var buf []byte
		buf = EmitSubsdXmmXmm(buf, 0, 1)
		want := []byte{0xF2, 0x0F, 0x5C, 0xC1}
		if !bytesEqual(buf, want) {
			t.Errorf("SUBSD xmm0,xmm1 = %x, want %x", buf, want)
		}
	})

	// MULSD xmm0, xmm1:F2 0F 59 C1
	t.Run("MulsdXmmXmm_xmm0_xmm1", func(t *testing.T) {
		var buf []byte
		buf = EmitMulsdXmmXmm(buf, 0, 1)
		want := []byte{0xF2, 0x0F, 0x59, 0xC1}
		if !bytesEqual(buf, want) {
			t.Errorf("MULSD xmm0,xmm1 = %x, want %x", buf, want)
		}
	})

	// DIVSD xmm0, xmm1:F2 0F 5E C1
	t.Run("DivsdXmmXmm_xmm0_xmm1", func(t *testing.T) {
		var buf []byte
		buf = EmitDivsdXmmXmm(buf, 0, 1)
		want := []byte{0xF2, 0x0F, 0x5E, 0xC1}
		if !bytesEqual(buf, want) {
			t.Errorf("DIVSD xmm0,xmm1 = %x, want %x", buf, want)
		}
	})

	// disp32 范围测试:-128 应负数 LE 编码
	t.Run("MovsdXmmFromMem_disp32_negative", func(t *testing.T) {
		var buf []byte
		buf = EmitMovsdXmmFromMem(buf, 0, 0, -128)
		// disp32 = -128 = 0xFFFFFF80(LE: 80 FF FF FF)
		want := []byte{0xF2, 0x0F, 0x10, 0x80, 0x80, 0xFF, 0xFF, 0xFF}
		if !bytesEqual(buf, want) {
			t.Errorf("MOVSD xmm0,[rax-128] = %x, want %x", buf, want)
		}
	})

	// 字节数常量
	t.Run("Constants", func(t *testing.T) {
		if EncodedMovsdMemLen != 8 {
			t.Errorf("EncodedMovsdMemLen = %d, want 8", EncodedMovsdMemLen)
		}
		if EncodedSseBinopLen != 4 {
			t.Errorf("EncodedSseBinopLen = %d, want 4", EncodedSseBinopLen)
		}
	})
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestPJ2_MovqMemEncoding 验「mov rax, [r15+disp32]」+「mov rax, [reg+disp32]」
// 字节级 ISA 编码。
func TestPJ2_MovqMemEncoding(t *testing.T) {
	// mov rax, [r15+0]:49 8B 87 00 00 00 00
	t.Run("MovqRaxFromR15Disp_0", func(t *testing.T) {
		var buf []byte
		buf = EmitMovqRaxFromR15Disp(buf, 0)
		want := []byte{0x49, 0x8B, 0x87, 0x00, 0x00, 0x00, 0x00}
		if !bytesEqual(buf, want) {
			t.Errorf("MOV rax,[r15+0] = %x, want %x", buf, want)
		}
	})

	// mov rax, [r15+16]:49 8B 87 10 00 00 00
	t.Run("MovqRaxFromR15Disp_16", func(t *testing.T) {
		var buf []byte
		buf = EmitMovqRaxFromR15Disp(buf, 16)
		want := []byte{0x49, 0x8B, 0x87, 0x10, 0x00, 0x00, 0x00}
		if !bytesEqual(buf, want) {
			t.Errorf("MOV rax,[r15+16] = %x, want %x", buf, want)
		}
	})

	// mov rax, [rax+0]:48 8B 80 00 00 00 00(reg=0=rax)
	t.Run("MovqRaxFromMemReg_rax_0", func(t *testing.T) {
		var buf []byte
		buf = EmitMovqRaxFromMemReg(buf, 0, 0)
		want := []byte{0x48, 0x8B, 0x80, 0x00, 0x00, 0x00, 0x00}
		if !bytesEqual(buf, want) {
			t.Errorf("MOV rax,[rax+0] = %x, want %x", buf, want)
		}
	})

	// mov rax, [rcx+24]:48 8B 81 18 00 00 00
	t.Run("MovqRaxFromMemReg_rcx_24", func(t *testing.T) {
		var buf []byte
		buf = EmitMovqRaxFromMemReg(buf, 1, 24)
		want := []byte{0x48, 0x8B, 0x81, 0x18, 0x00, 0x00, 0x00}
		if !bytesEqual(buf, want) {
			t.Errorf("MOV rax,[rcx+24] = %x, want %x", buf, want)
		}
	})

	t.Run("Constants", func(t *testing.T) {
		if EncodedMovqFromR15DispLen != 7 {
			t.Errorf("EncodedMovqFromR15DispLen = %d, want 7", EncodedMovqFromR15DispLen)
		}
		if EncodedMovqFromMemRegLen != 7 {
			t.Errorf("EncodedMovqFromMemRegLen = %d, want 7", EncodedMovqFromMemRegLen)
		}
	})
}
