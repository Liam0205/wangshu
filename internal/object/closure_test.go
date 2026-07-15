package object

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/arena"
)

// TestClosureGibbousSlot verifies the slot-cache encoding round trip (PW10
// zero-cross-boundary lazy-fill IC): stores slot+1 / 0=unfilled, and the
// write-back only touches the high 16 bits of word1, without corrupting
// protoID / nupvals.
func TestClosureGibbousSlot(t *testing.T) {
	a := arena.New(arena.Options{})
	const protoID = 0x0BADF00D
	const nupvals = 3
	cl := AllocLuaClosure(a, protoID, nupvals)

	// Initially unfilled.
	if _, ok := ClosureGibbousSlot(a, cl); ok {
		t.Fatalf("fresh closure 应未填充 slot")
	}
	if got := ClosureProtoID(a, cl); got != protoID {
		t.Fatalf("protoID 初始: %#x", got)
	}
	if got := ClosureNUpvals(a, cl); got != nupvals {
		t.Fatalf("nupvals 初始: %d", got)
	}

	// Fill slot=0 (edge case: slot 0 is encoded as 1, must be distinguishable from unfilled).
	SetClosureGibbousSlot(a, cl, 0)
	if got, ok := ClosureGibbousSlot(a, cl); !ok || got != 0 {
		t.Fatalf("slot=0 往返: got=%d ok=%v", got, ok)
	}
	// Filling does not corrupt protoID / nupvals.
	if got := ClosureProtoID(a, cl); got != protoID {
		t.Fatalf("protoID 被腐蚀: %#x", got)
	}
	if got := ClosureNUpvals(a, cl); got != nupvals {
		t.Fatalf("nupvals 被腐蚀: %d", got)
	}

	// Refill with slot=8191 (maxTableSlots-1, production upper bound).
	SetClosureGibbousSlot(a, cl, 8191)
	if got, ok := ClosureGibbousSlot(a, cl); !ok || got != 8191 {
		t.Fatalf("slot=8191 往返: got=%d ok=%v", got, ok)
	}
	if got := ClosureProtoID(a, cl); got != protoID {
		t.Fatalf("protoID 被腐蚀(改填后): %#x", got)
	}

	// Beyond the 16-bit encoding domain: not cached (keeps the original value unchanged).
	SetClosureGibbousSlot(a, cl, 0xffff)
	if got, ok := ClosureGibbousSlot(a, cl); !ok || got != 8191 {
		t.Fatalf("超域写应 no-op,保持 8191: got=%d ok=%v", got, ok)
	}

	// The upvalue slots are unaffected by the slot cache (the high bits are physically isolated from upvalRef[]).
	uvRef := arena.GCRef(0xABCD8)
	SetClosureUpvalRef(a, cl, 1, uvRef)
	if got := ClosureUpvalRef(a, cl, 1); got != uvRef {
		t.Fatalf("upvalRef[1]: %#x", got)
	}
	if got, ok := ClosureGibbousSlot(a, cl); !ok || got != 8191 {
		t.Fatalf("写 upval 后 slot 应不变: got=%d ok=%v", got, ok)
	}
}
