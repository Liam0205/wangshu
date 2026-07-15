//go:build wangshu_p4 && arm64 && linux

package jit

import "testing"

// TestArm64ArenaBaseOff_Valid checks that valid offsets return normally.
func TestArm64ArenaBaseOff_Valid(t *testing.T) {
	cases := []struct {
		in   int32
		want uint16
	}{
		{0, 0},
		{8, 8},
		{40, 40},
		{32760, 32760}, // upper bound (arm64 LDR pimm12=4095 → byteOff=32760)
	}
	for _, tc := range cases {
		got := arenaBaseOffArm64(tc.in)
		if got != tc.want {
			t.Errorf("arenaBaseOffArm64(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestArm64ArenaBaseOff_Negative checks that a negative value panics (arm64 LDR pimm12 is unsigned).
func TestArm64ArenaBaseOff_Negative(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for negative arenaBaseOff")
		}
	}()
	arenaBaseOffArm64(-1)
}

// TestArm64ArenaBaseOff_TooLarge checks that exceeding 32760 panics (guards against
// a future JITContext field reordering pushing arenaBase to ≥32760 and silently
// degrading to reading [x27+0]).
func TestArm64ArenaBaseOff_TooLarge(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for arenaBaseOff > 32760")
		}
	}()
	arenaBaseOffArm64(32768)
}

// TestArm64ArenaBaseOff_NotAligned checks that a non-8-byte-aligned value panics
// (guards against arenaBase losing 8-byte alignment and silently falling back after
// a field type is changed to a smaller one).
func TestArm64ArenaBaseOff_NotAligned(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for arenaBaseOff not 8-byte aligned")
		}
	}()
	arenaBaseOffArm64(4)
}
