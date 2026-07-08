//go:build wangshu_p4

package peroptranslator

import (
	"testing"
	"unsafe"
)

// TestCallICLayout guards the byte offsets the segment-to-segment
// dispatch emit bakes as disp8 constants (issue #50 Spike 5). A struct
// reorder that moved Flags or CalleeSegAddr would silently corrupt the
// emitted `test byte [icSlot+6]` / `mov rax, [icSlot+16]` — this test
// catches it at build time instead of at SIGSEGV time.
func TestCallICLayout(t *testing.T) {
	var ic CallIC
	if got := unsafe.Offsetof(ic.Flags); got != callICFlagsByteOffset {
		t.Errorf("CallIC.Flags offset = %d, emit assumes %d", got, callICFlagsByteOffset)
	}
	if got := unsafe.Offsetof(ic.CalleeSegAddr); got != callICSegAddrByteOffset {
		t.Errorf("CallIC.CalleeSegAddr offset = %d, emit assumes %d", got, callICSegAddrByteOffset)
	}
	// ProtoID must be at offset 0 — the guard emits `cmp eax, [rdx+0]`.
	if got := unsafe.Offsetof(ic.CalleeProtoID); got != 0 {
		t.Errorf("CallIC.CalleeProtoID offset = %d, want 0", got)
	}
	// Intrinsic fields (issue #77): the segment reads IntrinsicID at
	// [icSlot+7] and IntrinsicCalleeVal at [icSlot+24].
	if got := unsafe.Offsetof(ic.IntrinsicID); got != callICIntrinsicIDByteOffset {
		t.Errorf("CallIC.IntrinsicID offset = %d, emit assumes %d", got, callICIntrinsicIDByteOffset)
	}
	if got := unsafe.Offsetof(ic.IntrinsicCalleeVal); got != callICIntrinsicValByteOffset {
		t.Errorf("CallIC.IntrinsicCalleeVal offset = %d, emit assumes %d", got, callICIntrinsicValByteOffset)
	}
}
