//go:build wangshu_p4

package jit

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/bridge"
	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/value"
)

// compiler_pj4_self_test.go —— synthetic-driver test for the PJ4 SELF IC
// ArrayHit main path.
//
// **Background** (per external review feedback): in luac 5.1 the SELF opcode's
// method key is always an ident → string constant → compile-time NodeHit (not
// ArrayHit). Real-world code never emits the "SELF + RETURN + IC ArrayHit"
// form, so the crescent e2e tier-up tests cannot reach the analyzeSelfArrayHit
// + compileIcSelfArrayHit main path.
//
// This file builds a **synthetic proto + IC slot + feedback** to drive the
// SELF main path directly, verifying:
//   1. analyzeSelfArrayHit form recognition returns true (SELF + RETURN A 2 +
//      IC ArrayHit + feedback FBTableMono + Confidence>=0.99 + matching
//      stableShape/Index)
//   2. compileIcSelfArrayHit emits the 139-byte template + incSpecTableHits()
//   3. the returned GibbousCode.Proto() points to the input proto, Slot()
//      returns (0, false)
//
// **No real Run**: Run needs a real arena + table addressing; this test uses a
// mock host and does not actually execute the code (host.ArenaBaseAddr=0 makes
// Run take the fallback rather than callJITSpec). Byte-level emit correctness
// of the template is already covered by the jit/amd64 package byte-level unit
// tests (TestPJ4_EmitSelfArrayHit_*). This test verifies the main-path Compile
// flow + SpecTableHits probe, filling the "SELF main path has no
// execution-level coverage" gap reported by the bot.

// TestPJ4_SelfArrayHit_MainPath_Synthesized synthesizes a SELF proto + IC slot
// + feedback and really drives the analyzeSelfArrayHit → compileIcSelfArrayHit
// chain.
//
// Form: SELF A=2 B=0 C=257(K[1]) + RETURN A=2 B=2, satisfying all
// analyzeSelfArrayHit trigger conditions (A<=253 / B<=254 / C>=256 /
// RETURN.A=SELF.A / retB=2 / IC kind=ArrayHit / feedback mono / matching
// shape/index).
//
// SELF semantics: R(A+1)=R(B); R(A)=R(B)[K] —— i.e. R(3)=R(0); R(2)=R(0)[K[1]].
// The compile-time path needs the ArrayHit signal: proto.IC[0].Kind=ArrayHit +
// feedback.Points[0].Kind=FBTableMono with matching shape/index.
func TestPJ4_SelfArrayHit_MainPath_Synthesized(t *testing.T) {
	ResetSpecHits()

	// Synthetic proto: SELF + RETURN (2-op form)
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.SELF, 2, 0, 257), // SELF A=2 B=0 C=K[1]
			bytecode.EncodeABC(bytecode.RETURN, 2, 2, 0), // RETURN A=2 B=2
		},
		IC: make([]bytecode.ICSlot, 2),
		Consts: []value.Value{
			0, // K[0] dummy (SELF.C=257 → KIdx=1)
			0, // K[1] dummy method key NaN-box (any value works; SELF ArrayHit
			//     does not verify stableKey)
		},
	}
	// Fill IC slot: Kind=ArrayHit + Shape=7 + Index=1 (simulating the P1
	// interpreter recording the hit state at pc=0 SELF)
	proto.IC[0] = bytecode.ICSlot{
		Kind:  bytecode.ICKindArrayHit,
		Shape: 7,
		Index: 1,
	}

	// Synthetic feedback: FBTableMono + Confidence=1.0 + matching shape/index
	feedback := &bridge.TypeFeedback{
		Points: []bridge.PointFeedback{
			{
				Kind:        bridge.FBSelfMono,
				Confidence:  1.0,
				StableShape: 7,
				StableIndex: 1,
			},
		},
	}

	// Verify form recognition really returns true
	info, ok := analyzeSelfArrayHit(proto, feedback)
	if !ok {
		t.Fatal("analyzeSelfArrayHit 应返 true(SELF + RETURN A 2 + IC ArrayHit + feedback mono)")
	}
	if !info.icSelfArrayHit {
		t.Error("info.icSelfArrayHit 应为 true")
	}
	if info.icAReg != 2 || info.icBReg != 0 {
		t.Errorf("icAReg/icBReg = %d/%d, want 2/0", info.icAReg, info.icBReg)
	}
	if info.icStableShape != 7 || info.icStableIndex != 1 {
		t.Errorf("stableShape/Index = %d/%d, want 7/1",
			info.icStableShape, info.icStableIndex)
	}
	if info.preludeOp != uint8(bytecode.SELF) {
		t.Errorf("preludeOp = %d, want SELF=%d", info.preludeOp, bytecode.SELF)
	}

	// Drive compileIcSelfArrayHit: build a Compiler + inject a mock host
	// (non-zero arenaBase enables the useSpec path) + Compile
	c := New()
	host := newMockP4Host()
	host.arenaBase = 0x1000 // non-zero makes the Compile path take archSupportsSpec + ArenaBaseAddr check
	c.SetHostState(host)

	hitsBefore := SpecTableHits()
	gc, err := c.Compile(proto, feedback)
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}
	if gc == nil {
		t.Fatal("Compile 返 nil GibbousCode")
	}

	// Verify SpecTableHits really increments (SELF template really compiled,
	// via archEmitSelfArrayHit emitting 139 bytes + archMmapCode mmap+RX)
	hitsAfter := SpecTableHits()
	if hitsAfter <= hitsBefore {
		t.Errorf("SpecTableHits 未增长(%d → %d) → SELF compileIc 路径未真触达",
			hitsBefore, hitsAfter)
	}
	t.Logf("SELF 主路径合成驱动:SpecTableHits %d → %d(增量 = %d)",
		hitsBefore, hitsAfter, hitsAfter-hitsBefore)

	// Verify GibbousCode.Proto() points back to the input proto
	if gc.Proto() != proto {
		t.Error("GibbousCode.Proto() 应指向输入 proto")
	}
	// Verify Slot() returns (0, false) (P4 native code has no wasm-table concept)
	if idx, ok := gc.Slot(); idx != 0 || ok {
		t.Errorf("Slot() = (%d, %v), want (0, false)", idx, ok)
	}
}

// TestPJ4_SelfArrayHit_FormGuards verifies the SELF form guards —— any
// unmet condition returns false.
func TestPJ4_SelfArrayHit_FormGuards(t *testing.T) {
	mkValid := func() (*bytecode.Proto, *bridge.TypeFeedback) {
		p := &bytecode.Proto{
			Code: []bytecode.Instruction{
				bytecode.EncodeABC(bytecode.SELF, 2, 0, 257),
				bytecode.EncodeABC(bytecode.RETURN, 2, 2, 0),
			},
			IC:     make([]bytecode.ICSlot, 2),
			Consts: []value.Value{0, 0},
		}
		p.IC[0] = bytecode.ICSlot{Kind: bytecode.ICKindArrayHit, Shape: 7, Index: 1}
		f := &bridge.TypeFeedback{
			Points: []bridge.PointFeedback{
				{Kind: bridge.FBTableMono, Confidence: 1.0, StableShape: 7, StableIndex: 1},
			},
		}
		return p, f
	}

	cases := []struct {
		name string
		mut  func(*bytecode.Proto, *bridge.TypeFeedback)
	}{
		{
			"A 超界 254",
			func(p *bytecode.Proto, _ *bridge.TypeFeedback) {
				p.Code[0] = bytecode.EncodeABC(bytecode.SELF, 254, 0, 257)
				p.Code[1] = bytecode.EncodeABC(bytecode.RETURN, 254, 2, 0)
			},
		},
		{
			"C 非常量(<256)",
			func(p *bytecode.Proto, _ *bridge.TypeFeedback) {
				p.Code[0] = bytecode.EncodeABC(bytecode.SELF, 2, 0, 100) // C=100 is a reg
			},
		},
		{
			"RETURN.A != SELF.A",
			func(p *bytecode.Proto, _ *bridge.TypeFeedback) {
				p.Code[1] = bytecode.EncodeABC(bytecode.RETURN, 5, 2, 0)
			},
		},
		{
			"RETURN.B != 2",
			func(p *bytecode.Proto, _ *bridge.TypeFeedback) {
				p.Code[1] = bytecode.EncodeABC(bytecode.RETURN, 2, 1, 0)
			},
		},
		{
			"IC kind=None(P1 未跑过)",
			func(p *bytecode.Proto, _ *bridge.TypeFeedback) {
				p.IC[0].Kind = bytecode.ICKindNone
			},
		},
		{
			"feedback Confidence 低",
			func(_ *bytecode.Proto, f *bridge.TypeFeedback) {
				f.Points[0].Confidence = 0.5
			},
		},
		{
			"stableShape 不一致(IC vs feedback race)",
			func(_ *bytecode.Proto, f *bridge.TypeFeedback) {
				f.Points[0].StableShape = 99 // differs from IC.Shape=7
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, f := mkValid()
			tc.mut(p, f)
			if _, ok := analyzeSelfArrayHit(p, f); ok {
				t.Errorf("形态守卫失效:%s 应返 false 但返 true", tc.name)
			}
		})
	}
}

// TestPJ4_SelfNodeHit_MainPath_Synthesized synthesizes a SELF NodeHit-form
// proto + IC slot + feedback and really drives the analyzeSelfNodeHit →
// compileIcSelfNodeHit chain.
//
// **luac form boundary** (same as SELF ArrayHit): a real-world `obj:method`
// must have parentheses `()` to be valid Lua syntax, but `obj:method()`
// compiles to SELF + CALL + RETURN (3+ ops) rather than the SELF + RETURN
// 2-op form. So luac never emits this IC form —— wiring up the SELF NodeHit
// main path is engineering groundwork, and e2e tier-up can only reach it after
// the PJ5 CALL wiring lands. This synthetic-driver test is currently the only
// coverage means.
//
// Form: SELF A=2 B=0 C=257(K[1] string) + RETURN A=2 B=2, satisfying all
// analyzeSelfNodeHit trigger conditions + IC[0].Kind=NodeHit + stableKey
// fixed at compile time from proto.Consts[1].
func TestPJ4_SelfNodeHit_MainPath_Synthesized(t *testing.T) {
	ResetSpecHits()

	// stableKey simulated with a string NaN-box (0xFFFB + GCRef offset)
	const stableKey uint64 = 0xFFFB_0000_0000_0042

	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.SELF, 2, 0, 257), // SELF A=2 B=0 C=K[1]
			bytecode.EncodeABC(bytecode.RETURN, 2, 2, 0), // RETURN A=2 B=2
		},
		IC: make([]bytecode.ICSlot, 2),
		Consts: []value.Value{
			0,                      // K[0] dummy
			value.Value(stableKey), // K[1] real stableKey (string NaN-box)
		},
	}
	proto.IC[0] = bytecode.ICSlot{
		Kind:  bytecode.ICKindNodeHit,
		Shape: 7,
		Index: 1,
	}

	feedback := &bridge.TypeFeedback{
		Points: []bridge.PointFeedback{
			{
				Kind:        bridge.FBSelfMono,
				Confidence:  1.0,
				StableShape: 7,
				StableIndex: 1,
			},
		},
	}

	// Verify form recognition
	info, ok := analyzeSelfNodeHit(proto, feedback)
	if !ok {
		t.Fatal("analyzeSelfNodeHit 应返 true(SELF + RETURN A 2 + IC NodeHit + feedback mono)")
	}
	if !info.icSelfNodeHit {
		t.Error("info.icSelfNodeHit 应为 true")
	}
	if info.icAReg != 2 || info.icBReg != 0 {
		t.Errorf("icAReg/icBReg = %d/%d, want 2/0", info.icAReg, info.icBReg)
	}
	if info.icStableKey != stableKey {
		t.Errorf("icStableKey = 0x%x, want 0x%x", info.icStableKey, stableKey)
	}
	if info.preludeOp != uint8(bytecode.SELF) {
		t.Errorf("preludeOp = %d, want SELF=%d", info.preludeOp, bytecode.SELF)
	}

	// Drive compileIcSelfNodeHit
	c := New()
	host := newMockP4Host()
	host.arenaBase = 0x1000
	c.SetHostState(host)

	hitsBefore := SpecTableHits()
	gc, err := c.Compile(proto, feedback)
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}
	if gc == nil {
		t.Fatal("Compile 返 nil GibbousCode")
	}

	hitsAfter := SpecTableHits()
	if hitsAfter <= hitsBefore {
		t.Errorf("SpecTableHits 未增长(%d → %d) → SELF NodeHit compileIc 路径未真触达",
			hitsBefore, hitsAfter)
	}
	t.Logf("SELF NodeHit 主路径合成驱动:SpecTableHits %d → %d",
		hitsBefore, hitsAfter)

	if gc.Proto() != proto {
		t.Error("GibbousCode.Proto() 应指向输入 proto")
	}
}

// TestPJ4_SetTableNodeHit_MainPath_Synthesized synthesizes the SETTABLE
// NodeHit form and really drives the main path (mirroring the SELF NodeHit
// form).
//
// Same e2e tier-up path as SETTABLE ArrayHit (`function(t,v) t["x"]=v end` is
// a common SETTABLE NodeHit form and is already e2e-covered); the
// synthetic-driver unit test ensures the analyzer/compileIc are still caught
// when accidentally modified.
func TestPJ4_SetTableNodeHit_MainPath_Synthesized(t *testing.T) {
	ResetSpecHits()

	const stableKey uint64 = 0xFFFB_0000_0000_0042

	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.SETTABLE, 0, 257, 1), // SETTABLE A=0 B=K[1] C=R(1)
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),     // RETURN A=0 B=1(setter)
		},
		IC: make([]bytecode.ICSlot, 2),
		Consts: []value.Value{
			0,
			value.Value(stableKey),
		},
	}
	proto.IC[0] = bytecode.ICSlot{
		Kind:  bytecode.ICKindNodeHit,
		Shape: 7,
		Index: 1,
	}

	feedback := &bridge.TypeFeedback{
		Points: []bridge.PointFeedback{
			{
				Kind:        bridge.FBTableMono,
				Confidence:  1.0,
				StableShape: 7,
				StableIndex: 1,
			},
		},
	}

	info, ok := analyzeSetTableNodeHit(proto, feedback)
	if !ok {
		t.Fatal("analyzeSetTableNodeHit 应返 true")
	}
	if !info.icSetNodeHit {
		t.Error("info.icSetNodeHit 应为 true")
	}
	if info.icStableKey != stableKey {
		t.Errorf("icStableKey = 0x%x, want 0x%x", info.icStableKey, stableKey)
	}

	c := New()
	host := newMockP4Host()
	host.arenaBase = 0x1000
	c.SetHostState(host)

	hitsBefore := SpecTableHits()
	gc, err := c.Compile(proto, feedback)
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}
	if gc == nil {
		t.Fatal("Compile 返 nil GibbousCode")
	}
	if SpecTableHits() <= hitsBefore {
		t.Errorf("SETTABLE NodeHit compileIc 路径未真触达")
	}
	t.Logf("SETTABLE NodeHit 主路径合成驱动:SpecTableHits %d → %d",
		hitsBefore, SpecTableHits())
}
