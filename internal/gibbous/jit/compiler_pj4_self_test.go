//go:build wangshu_p4

package jit

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/bridge"
	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/value"
)

// compiler_pj4_self_test.go —— PJ4 SELF IC ArrayHit 主路径合成驱动测试。
//
// **背景**(承外部审查反馈):luac 5.1 中 SELF opcode 的 method key 必是
// ident → 字符串常量 → 编译期 NodeHit（非 ArrayHit）。real-world 代码不会
// 编出「SELF + RETURN + IC ArrayHit」形态,所以 crescent e2e 真升层测试
// 无法触达 analyzeSelfArrayHit + compileIcSelfArrayHit 主路径。
//
// 本文件构造**合成 proto + IC slot + feedback**,直接驱动 SELF 主路径,
// 验证:
//   1. analyzeSelfArrayHit 形态识别返 true(SELF + RETURN A 2 + IC ArrayHit
//      + feedback FBTableMono + Confidence>=0.99 + stableShape/Index 一致)
//   2. compileIcSelfArrayHit emit 139 字节模板 + incSpecTableHits()
//   3. 返回的 GibbousCode.Proto() 指向输入 proto,Slot() 返 (0, false)
//
// **不真 Run**:Run 需要真 arena + table 寻址,本测用 mock host 不真执行
// 段(用 host.ArenaBaseAddr=0 让 Run 走 fallback 而非 callJITSpec)——
// 模板字节级 emit 正确性已由 jit/amd64 包字节级单测(TestPJ4_EmitSelfArrayHit_*)
// 覆盖。本测验主路径 Compile 流程 + SpecTableHits 探针,补 bot 反馈的
// 「SELF 主路径无执行级覆盖」缺口。

// TestPJ4_SelfArrayHit_MainPath_Synthesized 合成 SELF proto + IC slot +
// feedback,真驱动 analyzeSelfArrayHit → compileIcSelfArrayHit 链路。
//
// 形态:SELF A=2 B=0 C=257(K[1])+ RETURN A=2 B=2,符合 analyzeSelfArrayHit
// 全部触发条件(A<=253 / B<=254 / C>=256 / RETURN.A=SELF.A / retB=2 / IC
// kind=ArrayHit / feedback mono / shape/index 一致)。
//
// SELF semantics: R(A+1)=R(B); R(A)=R(B)[K] —— 即 R(3)=R(0); R(2)=R(0)[K[1]]。
// 编译期需要 ArrayHit 信号:proto.IC[0].Kind=ArrayHit + feedback.Points[0].
// Kind=FBTableMono 一致 shape/index。
func TestPJ4_SelfArrayHit_MainPath_Synthesized(t *testing.T) {
	ResetSpecHits()

	// 合成 proto:SELF + RETURN(2 op 形态)
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.SELF, 2, 0, 257), // SELF A=2 B=0 C=K[1]
			bytecode.EncodeABC(bytecode.RETURN, 2, 2, 0), // RETURN A=2 B=2
		},
		IC: make([]bytecode.ICSlot, 2),
		Consts: []value.Value{
			0, // K[0] dummy(SELF.C=257 → KIdx=1)
			0, // K[1] dummy method key NaN-box(任意值即可,SELF ArrayHit
			//     不验 stableKey)
		},
	}
	// 填 IC slot:Kind=ArrayHit + Shape=7 + Index=1(模拟 P1 解释器在
	// pc=0 SELF 处填的命中状态)
	proto.IC[0] = bytecode.ICSlot{
		Kind:  bytecode.ICKindArrayHit,
		Shape: 7,
		Index: 1,
	}

	// 合成 feedback:FBTableMono + Confidence=1.0 + 同 shape/index
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

	// 验形态识别真返 true
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

	// 驱动 compileIcSelfArrayHit:构造 Compiler + 注入 mock host(arenaBase
	// 非 0 启用 useSpec 路径)+ Compile
	c := New()
	host := newMockP4Host()
	host.arenaBase = 0x1000 // 非 0 让 Compile 路径走 archSupportsSpec + ArenaBaseAddr check
	c.SetHostState(host)

	hitsBefore := SpecTableHits()
	gc, err := c.Compile(proto, feedback)
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}
	if gc == nil {
		t.Fatal("Compile 返 nil GibbousCode")
	}

	// 验 SpecTableHits 真增(SELF 模板真编译,经 archEmitSelfArrayHit emit
	// 139 字节 + archMmapCode mmap+RX)
	hitsAfter := SpecTableHits()
	if hitsAfter <= hitsBefore {
		t.Errorf("SpecTableHits 未增长(%d → %d) → SELF compileIc 路径未真触达",
			hitsBefore, hitsAfter)
	}
	t.Logf("SELF 主路径合成驱动:SpecTableHits %d → %d(增量 = %d)",
		hitsBefore, hitsAfter, hitsAfter-hitsBefore)

	// 验 GibbousCode.Proto() 指回输入 proto
	if gc.Proto() != proto {
		t.Error("GibbousCode.Proto() 应指向输入 proto")
	}
	// 验 Slot() 返 (0, false)(P4 原生码无 wasm 表概念)
	if idx, ok := gc.Slot(); idx != 0 || ok {
		t.Errorf("Slot() = (%d, %v), want (0, false)", idx, ok)
	}
}

// TestPJ4_SelfArrayHit_FormGuards 验 SELF 形态守卫——任一不满足返 false。
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
				p.Code[0] = bytecode.EncodeABC(bytecode.SELF, 2, 0, 100) // C=100 是 reg
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
				f.Points[0].StableShape = 99 // 不同于 IC.Shape=7
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

// TestPJ4_SelfNodeHit_MainPath_Synthesized 合成 SELF NodeHit 形态 proto +
// IC slot + feedback,真驱动 analyzeSelfNodeHit → compileIcSelfNodeHit 链路。
//
// **luac 形态边界**(同 SELF ArrayHit):real-world `obj:method` 必须有
// 括号 `()` 才是 Lua 合法语法,但 `obj:method()` 编 SELF + CALL + RETURN
// (3+ op)而非 SELF + RETURN 2-op 形态。即 luac 不真编出此 IC 形态——
// SELF NodeHit 主路径接入是工程基础,e2e 真升层需要 PJ5 CALL 接入后才能
// 触达。本测试合成驱动是当前唯一覆盖手段。
//
// 形态:SELF A=2 B=0 C=257(K[1] string)+ RETURN A=2 B=2,符合
// analyzeSelfNodeHit 全部触发条件 + IC[0].Kind=NodeHit + stableKey 从
// proto.Consts[1] 编译期固化。
func TestPJ4_SelfNodeHit_MainPath_Synthesized(t *testing.T) {
	ResetSpecHits()

	// stableKey 用 string NaN-box 模拟(0xFFFB + GCRef offset)
	const stableKey uint64 = 0xFFFB_0000_0000_0042

	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.SELF, 2, 0, 257), // SELF A=2 B=0 C=K[1]
			bytecode.EncodeABC(bytecode.RETURN, 2, 2, 0), // RETURN A=2 B=2
		},
		IC: make([]bytecode.ICSlot, 2),
		Consts: []value.Value{
			0,                      // K[0] dummy
			value.Value(stableKey), // K[1] 真 stableKey(string NaN-box)
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

	// 验形态识别
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

	// 驱动 compileIcSelfNodeHit
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

// TestPJ4_SetTableNodeHit_MainPath_Synthesized 合成 SETTABLE NodeHit 形态
// 真驱动主路径(对位 SELF NodeHit 同款形态)。
//
// 与 SETTABLE ArrayHit 同款 e2e 真升层路径(`function(t,v) t["x"]=v end`
// 是 SETTABLE NodeHit 真常见形态,e2e 已覆盖);但合成驱动单测确保
// analyzer/compileIc 不被误改时仍能抓出。
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
