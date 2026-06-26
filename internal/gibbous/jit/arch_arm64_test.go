//go:build wangshu_p4 && arm64 && linux

package jit

import "testing"

// TestArm64ArenaBaseOff_Valid 验合法偏移正常返回。
func TestArm64ArenaBaseOff_Valid(t *testing.T) {
	cases := []struct {
		in   int32
		want uint16
	}{
		{0, 0},
		{8, 8},
		{40, 40},
		{32760, 32760}, // 上限(arm64 LDR pimm12=4095 → byteOff=32760)
	}
	for _, tc := range cases {
		got := arenaBaseOffArm64(tc.in)
		if got != tc.want {
			t.Errorf("arenaBaseOffArm64(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestArm64ArenaBaseOff_Negative 验负值 panic(arm64 LDR pimm12 unsigned)。
func TestArm64ArenaBaseOff_Negative(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for negative arenaBaseOff")
		}
	}()
	arenaBaseOffArm64(-1)
}

// TestArm64ArenaBaseOff_TooLarge 验超 32760 panic(防 JITContext 字段
// 未来重排把 arenaBase 推到 ≥32760 时静默退化为读 [x27+0])。
func TestArm64ArenaBaseOff_TooLarge(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for arenaBaseOff > 32760")
		}
	}()
	arenaBaseOffArm64(32768)
}

// TestArm64ArenaBaseOff_NotAligned 验非 8 字节对齐 panic(防字段类型改成
// 更小字段后 arenaBase 偏移失去 8 字节对齐时静默兜底)。
func TestArm64ArenaBaseOff_NotAligned(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for arenaBaseOff not 8-byte aligned")
		}
	}()
	arenaBaseOffArm64(4)
}
