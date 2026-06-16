package crescent

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/arena"
)

// TestNew_EquivalentToZeroValueOptions 验证 New() 与 NewWithOptions(arena.Options{})
// 行为一致——保证向后兼容性(crescent.New() 是公共 API 在 wangshu 包之前的入口)。
func TestNew_EquivalentToZeroValueOptions(t *testing.T) {
	st1 := New()
	st2 := NewWithOptions(arena.Options{})
	// 验证两个 State 的初始 arena 容量一致(都是 64 KiB 默认)
	if st1.arena.Cap() != st2.arena.Cap() {
		t.Errorf("New() vs NewWithOptions({}) cap mismatch: %d vs %d",
			st1.arena.Cap(), st2.arena.Cap())
	}
}

// TestNewWithOptions_InitialBytesRespected 验证 InitialBytes 字段真传到 arena.New。
func TestNewWithOptions_InitialBytesRespected(t *testing.T) {
	const want = uint32(1 << 20) // 1 MiB
	st := NewWithOptions(arena.Options{InitialBytes: want})
	if got := st.arena.Cap(); got < want {
		t.Errorf("InitialBytes=%d not respected: arena.Cap()=%d", want, got)
	}
}

// TestNewWithOptions_MaxBytesRespected 验证 MaxBytes 字段真传到 arena.New
// (超 MaxBytes 时 grow64 panic)。
func TestNewWithOptions_MaxBytesRespected(t *testing.T) {
	const want = uint32(8 * 1024)
	st := NewWithOptions(arena.Options{InitialBytes: want, MaxBytes: want})
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on alloc exceeding MaxBytes, got none")
		}
	}()
	// 触发 grow:必然 panic
	_ = st.arena.AllocBytes(want + 1000)
}
