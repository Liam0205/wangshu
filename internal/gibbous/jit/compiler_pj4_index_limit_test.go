//go:build wangshu_p4

package jit

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/bridge"
	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/value"
)

// TestPJ4_Analyze_SlotIndexLimit verifies that each of the seven analyzers
// that hand an inline-cache index to a single-access table template accepts
// maxTemplateSlotIndex and rejects the index after it: the amd64 templates
// address the slot with a 32-bit displacement, and index*24+8 has to fit.
func TestPJ4_Analyze_SlotIndexLimit(t *testing.T) {
	if d := int64(maxTemplateSlotIndex)*24 + 8; d > 1<<31-1 {
		t.Fatalf("maxTemplateSlotIndex*24+8 = %d does not fit a 32-bit displacement", d)
	}
	if d := int64(maxTemplateSlotIndex+1)*24 + 8; d <= 1<<31-1 {
		t.Fatalf("maxTemplateSlotIndex is not the largest index that fits: (max+1)*24+8 = %d", d)
	}

	const key = value.Value(0xFFFB_0000_0000_0042)
	two := func(op0, ret bytecode.Instruction) *bytecode.Proto {
		return &bytecode.Proto{
			Code:   []bytecode.Instruction{op0, ret},
			IC:     make([]bytecode.ICSlot, 2),
			Consts: []value.Value{0, key},
		}
	}
	cases := []struct {
		name    string
		proto   *bytecode.Proto
		pc      int // the instruction whose inline cache the analyzer reads
		kind    uint8
		fb      bridge.FeedbackKind
		analyze func(*bytecode.Proto, *bridge.TypeFeedback) (shapeInfo, bool)
	}{
		{"analyzeGetTableArrayHit",
			two(bytecode.EncodeABC(bytecode.GETTABLE, 1, 0, 257), bytecode.EncodeABC(bytecode.RETURN, 1, 2, 0)),
			0, bytecode.ICKindArrayHit, bridge.FBTableMono, analyzeGetTableArrayHit},
		{"analyzeGetTableNodeHit",
			two(bytecode.EncodeABC(bytecode.GETTABLE, 1, 0, 257), bytecode.EncodeABC(bytecode.RETURN, 1, 2, 0)),
			0, bytecode.ICKindNodeHit, bridge.FBTableMono, analyzeGetTableNodeHit},
		{"analyzeSetTableArrayHit",
			two(bytecode.EncodeABC(bytecode.SETTABLE, 0, 257, 1), bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0)),
			0, bytecode.ICKindArrayHit, bridge.FBTableMono, analyzeSetTableArrayHit},
		{"analyzeSetTableNodeHit",
			two(bytecode.EncodeABC(bytecode.SETTABLE, 0, 257, 1), bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0)),
			0, bytecode.ICKindNodeHit, bridge.FBTableMono, analyzeSetTableNodeHit},
		{"analyzeSelfArrayHit",
			two(bytecode.EncodeABC(bytecode.SELF, 2, 0, 257), bytecode.EncodeABC(bytecode.RETURN, 2, 2, 0)),
			0, bytecode.ICKindArrayHit, bridge.FBSelfMono, analyzeSelfArrayHit},
		{"analyzeSelfNodeHit",
			two(bytecode.EncodeABC(bytecode.SELF, 2, 0, 257), bytecode.EncodeABC(bytecode.RETURN, 2, 2, 0)),
			0, bytecode.ICKindNodeHit, bridge.FBSelfMono, analyzeSelfNodeHit},
		{"analyzeSelfCallSpecForm",
			&bytecode.Proto{
				Code: []bytecode.Instruction{
					bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0),
					bytecode.EncodeABC(bytecode.SELF, 1, 1, 256),
					bytecode.EncodeABC(bytecode.CALL, 1, 2, 1),
					bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
				},
				IC:     make([]bytecode.ICSlot, 4),
				Consts: []value.Value{key},
			},
			1, bytecode.ICKindNodeHit, bridge.FBSelfMono, analyzeSelfCallSpecForm},
	}
	for _, c := range cases {
		for _, tc := range []struct {
			index uint32
			want  bool
		}{{maxTemplateSlotIndex, true}, {maxTemplateSlotIndex + 1, false}} {
			c.proto.IC[c.pc] = bytecode.ICSlot{Kind: c.kind, Shape: 7, Index: tc.index}
			points := make([]bridge.PointFeedback, c.pc+1)
			points[c.pc] = bridge.PointFeedback{Kind: c.fb, Confidence: 1.0, StableShape: 7, StableIndex: tc.index}
			info, ok := c.analyze(c.proto, &bridge.TypeFeedback{Points: points})
			if ok != tc.want {
				t.Errorf("%s with index %d: accepted = %v, want %v", c.name, tc.index, ok, tc.want)
			}
			if ok && info.icStableIndex != tc.index {
				t.Errorf("%s with index %d: icStableIndex = %d", c.name, tc.index, info.icStableIndex)
			}
		}
	}
}
