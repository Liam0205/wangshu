//go:build wangshu_p4 && linux && amd64

package peroptranslator

import "testing"

// TestPJ10Native_SaveGoG_NonZero verifies saveGoG writes a non-zero value
// (the current G pointer). Runs inside a goroutine so G is guaranteed
// live. This is just a smoke test for the asm helper; correctness of the
// value is verified by the e2e test that calls a Go helper from mmap.
func TestPJ10Native_SaveGoG_NonZero(t *testing.T) {
	var slot uintptr
	saveGoG(&slot)
	if slot == 0 {
		t.Error("saveGoG wrote 0; expected a non-zero G pointer")
	}
	// Idempotent: calling again should write the same G (this goroutine
	// hasn't switched between calls).
	var slot2 uintptr
	saveGoG(&slot2)
	if slot != slot2 {
		t.Errorf("saveGoG non-idempotent within same goroutine: %x vs %x", slot, slot2)
	}
}
