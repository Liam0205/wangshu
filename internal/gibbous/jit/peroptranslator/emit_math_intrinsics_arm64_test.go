//go:build wangshu_p4 && arm64

package peroptranslator

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/gibbous/jit"
	"github.com/Liam0205/wangshu/internal/value"
)

// TestArm64MathIntrinsicInlineWellFormed emits the issue #77 math
// intrinsic fast path (unary sqrt/floor/ceil/abs shape b==2 and max/min
// shape b==3) and checks it is structurally well-formed: the output is a
// whole number of 4-byte arm64 words and no forward branch was left at
// its 0-offset placeholder (a dangling B.cond imm19==0 / B imm26==0 would
// branch to itself on real hardware). Execution correctness is covered by
// the arch-neutral e2e tests + difftest byte-equal on the CI arm64 matrix
// (this host can't run arm64 code).
func TestArm64MathIntrinsicInlineWellFormed(t *testing.T) {
	// An IC slot already warmed as a math intrinsic: IsIntrinsic set (not
	// Stuck), a plausible callee value, and the kind byte. The emit reads
	// IntrinsicCalleeVal / IntrinsicID; the exact bytes don't matter for
	// well-formedness, only that the branches resolve.
	calleeVal := uint64(value.MakeGC(value.TagFunction, 0x2345))
	mkProto := func(kind uint8) *codeBufProto {
		return &codeBufProto{
			CallICs: []CallIC{{
				Flags:              CallICFlagIsIntrinsic | CallICFlagIsHost,
				IntrinsicID:        kind,
				IntrinsicCalleeVal: calleeVal,
			}},
		}
	}

	cases := []struct {
		name string
		a, b uint8
		kind uint8
	}{
		{"sqrt", 1, 2, jit.IntrinsicSqrt},
		{"floor", 1, 2, jit.IntrinsicFloor},
		{"ceil", 1, 2, jit.IntrinsicCeil},
		{"abs", 1, 2, jit.IntrinsicAbs},
		{"max", 1, 3, jit.IntrinsicMax},
		{"min", 1, 3, jit.IntrinsicMin},
	}

	for _, tc := range cases {
		cb := newCodeBuf(1)
		cb.proto = mkProto(tc.kind)
		var doneFixups []int
		emitCallIntrinsicFastPathArm64(cb, tc.a, tc.b, 0, &doneFixups)
		// The caller normally patches the done jumps past the exit-reason
		// block; simulate that so the scan doesn't flag them.
		for _, off := range doneFixups {
			a64PatchRel26(cb, off, len(cb.bytes))
		}
		if len(doneFixups) == 0 {
			t.Errorf("%s: no done jump emitted (body never reached?)", tc.name)
		}
		if len(cb.bytes)%4 != 0 {
			t.Fatalf("%s: emitted %d bytes, not a whole number of 4-byte words", tc.name, len(cb.bytes))
		}
		for off := 0; off+4 <= len(cb.bytes); off += 4 {
			w := word(cb.bytes, off)
			// B.cond (0x54xxxxxx) with imm19 == 0.
			if (w>>24) == 0x54 && ((w>>5)&0x7FFFF) == 0 {
				t.Errorf("%s: dangling B.cond (imm19==0) at word %d", tc.name, off/4)
			}
			// Unconditional B with imm26 == 0 (placeholder 0x14000000).
			if w == 0x14000000 {
				t.Errorf("%s: dangling B (imm26==0) at word %d", tc.name, off/4)
			}
		}
	}
}
