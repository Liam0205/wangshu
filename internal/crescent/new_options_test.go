package crescent

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/arena"
)

// TestNew_EquivalentToZeroValueOptions verifies that New() behaves identically to
// NewWithOptions(arena.Options{}), guaranteeing backward compatibility (crescent.New()
// was the public API entry point before the wangshu package).
func TestNew_EquivalentToZeroValueOptions(t *testing.T) {
	st1 := New()
	st2 := NewWithOptions(arena.Options{})
	// Verify both States start with the same arena capacity (both default to 64 KiB)
	if st1.arena.Cap() != st2.arena.Cap() {
		t.Errorf("New() vs NewWithOptions({}) cap mismatch: %d vs %d",
			st1.arena.Cap(), st2.arena.Cap())
	}
}

// TestNewWithOptions_InitialBytesRespected verifies the InitialBytes field is actually passed to arena.New.
func TestNewWithOptions_InitialBytesRespected(t *testing.T) {
	const want = uint32(1 << 20) // 1 MiB
	st := NewWithOptions(arena.Options{InitialBytes: want})
	if got := st.arena.Cap(); got < want {
		t.Errorf("InitialBytes=%d not respected: arena.Cap()=%d", want, got)
	}
}

// TestNewWithOptions_MaxBytesRespected verifies the MaxBytes field is actually passed to arena.New
// (grow64 panics when MaxBytes is exceeded).
func TestNewWithOptions_MaxBytesRespected(t *testing.T) {
	const want = uint32(8 * 1024)
	st := NewWithOptions(arena.Options{InitialBytes: want, MaxBytes: want})
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on alloc exceeding MaxBytes, got none")
		}
	}()
	// Trigger a grow: must panic
	_ = st.arena.AllocBytes(want + 1000)
}
