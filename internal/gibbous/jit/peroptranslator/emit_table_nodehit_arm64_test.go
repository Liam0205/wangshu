//go:build wangshu_p4 && arm64

package peroptranslator

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/value"
)

// TestArm64TableNodeHitInlineWellFormed emits the issue #67 GETTABLE /
// SETTABLE NodeHit inline fast paths and checks they are structurally
// well-formed: they accept a NodeHit const-key snapshot (return true), the
// output is a whole number of 4-byte arm64 instruction words, and no
// forward branch was left at its 0-offset placeholder (a dangling fixup —
// which on real hardware would branch to itself / into the middle of the
// prelude). Full execution correctness is covered by the arch-neutral e2e
// tests + difftest byte-equal on the CI arm64 matrix.
func TestArm64TableNodeHitInlineWellFormed(t *testing.T) {
	// A proto with one GETTABLE and one SETTABLE at pc 0/1, both NodeHit
	// with a constant string key (Bx/C >= 256). Consts[0] is a non-Nil
	// interned-string-shaped bit pattern (any non-Nil value works for the
	// emit-time stableKey != Nil check; the bytes are baked as an imm64).
	stableKey := uint64(value.MakeGC(value.TagString, 0x1234))
	proto := &bytecode.Proto{
		Consts: []value.Value{value.Value(stableKey)},
		IC: []bytecode.ICSlot{
			{Kind: bytecode.ICKindNodeHit, Shape: 0, Index: 2, TableRef: 0xABCD},
			{Kind: bytecode.ICKindNodeHit, Shape: 0, Index: 2, TableRef: 0xABCD},
		},
	}

	cases := []struct {
		name string
		emit func(cb *codeBuf) bool
	}{
		{"get", func(cb *codeBuf) bool {
			// R(1) := R(0)[K(0)]  (c = 256 -> const idx 0)
			return emitInlineGetTableNodeHitArm64(cb, 0, 1, 0, 256)
		}},
		{"set", func(cb *codeBuf) bool {
			// R(0)[K(0)] := R(1)  (b = 256 -> const idx 0; c = 1 register)
			return emitInlineSetTableNodeHitArm64(cb, 1, 0, 256, 1)
		}},
	}

	for _, tc := range cases {
		cb := newCodeBuf(1)
		cb.proto = &codeBufProto{Consts: []uint64{uint64(proto.Consts[0])}, IC: proto.IC}
		ok := tc.emit(cb)
		if !ok {
			t.Fatalf("%s: emit returned false (should inline a NodeHit const-key site)", tc.name)
		}
		if len(cb.bytes)%4 != 0 {
			t.Fatalf("%s: emitted %d bytes, not a whole number of 4-byte words", tc.name, len(cb.bytes))
		}
		// Scan for a dangling forward branch: a B.cond (0x54xxxxxx with
		// imm19 == 0) or an unconditional B (0x14000000 exactly) left at
		// its placeholder. The final `b done` and every guard B.cond must
		// have been patched to a non-zero relative offset.
		for off := 0; off+4 <= len(cb.bytes); off += 4 {
			w := word(cb.bytes, off)
			// B.cond: bits 24..31 == 0x54, imm19 (bits 5..23) == 0.
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
