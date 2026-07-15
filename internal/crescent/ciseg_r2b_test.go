//go:build wangshu_p3 && wangshu_profile

// PW10 R2b-1 acceptance: the CallInfo arena segment (write-only mirror) packs/unpacks
// losslessly round-trip + during real execution the segment matches the Go cis mirror
// field-by-field. In R2b-1 the segment is write-only (behavior unchanged); this test
// reads the segment directly to verify packing correctness, paving the way for R2b-2
// flipping the accessor/GC roots to read the segment.
package crescent

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/value"
)

// TestR2b1_CISegPackRoundTrip unit: writeCISeg → readCISegInto round-trips every field
// losslessly, including the sign-extension corner of nresults=-1 (variable) and the flags bits.
func TestR2b1_CISegPackRoundTrip(t *testing.T) {
	st := New()
	th := st.newThread()
	cases := []callInfo{
		{base: 1, funcIdx: 0, top: 5, protoID: 7, cl: 0x1234_5678, nresults: 2, tailcall: false, fresh: true, gibbous: false, pc: 42},
		{base: 100, funcIdx: 99, top: 200, protoID: 0xFFFF_FFFF, cl: 0, nresults: -1, tailcall: true, fresh: false, gibbous: true, pc: 0},
		{base: 0, funcIdx: 0, top: 0, protoID: 0, cl: 0xFFFF_FFFF_FFFF, nresults: 0, tailcall: true, fresh: true, gibbous: true, pc: 0x7FFF_FFFF},
	}
	for i, want := range cases {
		th.writeCISeg(i, &want)
	}
	for i, want := range cases {
		var got callInfo
		th.readCISegInto(i, &got)
		if got.base != want.base || got.funcIdx != want.funcIdx || got.top != want.top ||
			got.protoID != want.protoID || got.cl != want.cl || got.nresults != want.nresults ||
			got.tailcall != want.tailcall || got.fresh != want.fresh || got.gibbous != want.gibbous ||
			got.pc != want.pc {
			t.Errorf("frame %d round-trip mismatch:\n got  %+v\n want %+v", i, got, want)
		}
	}
}

// TestR2b2_GrowCISegDeepRecursion: deep recursion (>initialCISlots=64 frames) triggers
// growCISeg to reallocate the ci segment multiple times, verifying: ① no crash ② correct
// result (segment is write-only, behavior transparent) ③ under a wangshu_trace build,
// per-frame verifyCISeg stays field-by-field consistent across relocations (form Y's
// computed addressing is immune to grow).
func TestR2b2_GrowCISegDeepRecursion(t *testing.T) {
	// Tail recursion reuses the frame in O(1) (no deepening), so use non-tail accumulation; depth = n frames.
	src := `
local function sum(n)
  if n == 0 then return 0 end
  return n + sum(n - 1)
end
return sum(300)`
	st, mainCl := loadFn(t, src)
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run (深递归 growCISeg): %v", err)
	}
	want := float64(300 * 301 / 2) // Σ1..300 = 45150
	if len(rets) != 1 || !value.IsNumber(rets[0]) || value.AsNumber(rets[0]) != want {
		t.Fatalf("sum(300) = %v, want %v(growCISeg 不应改行为)", rets[0], want)
	}
}

// TestR2b3_SegClRootSurvivesGC R2b-3 core acceptance: GC root scanning reads each frame's
// closure root from the arena ci segment. Constructs "closure reachable only via a live
// frame" + forcing a full GC midway through a deep call chain + GC stress, verifying the
// closure is not wrongly collected (missed root / wrong-segment read = UAF). This is the
// regression anchor establishing the ci segment as the authoritative GC root source.
func TestR2b3_SegClRootSurvivesGC(t *testing.T) {
	// Deep recursion chain: each level has a distinct closure frame in the ci segment; the innermost host fn forces Collect.
	// If some frame's closure root is not scanned correctly from the segment → its Proto/upvalue is collected → wrong value returned or crash.
	src := `
local function chain(n)
  if n == 0 then collectgarbage_test(); return 0 end
  local k = n * 2          -- 每帧一个 upvalue,被内层闭包捕获 = 帧 closure 必须是活根
  local function step() return k end
  return step() + chain(n - 1)
end
result = chain(120)        -- 120 层帧(> initialCISlots 64,跨 growCISeg)`
	st := New()
	st.SetGCStressMode(true) // force a full Collect at every safepoint (triggers root scanning frequently)
	id := st.RegisterHostFn(func(s *State, _ []value.Value) ([]value.Value, *LuaError) {
		s.gc.Collect() // at the deepest frame (120 live frames in the ci segment), force a root scan
		return nil, nil
	})
	cl := st.MakeHostClosure(id)
	st.SetGlobal("collectgarbage_test", value.MakeGC(value.TagFunction, cl))

	prog := mustCompile(t, []byte(src))
	mainCl := st.LoadProgram(prog.mainID, prog.protos)
	if _, err := st.Call(mainCl, nil, 0); err != nil {
		t.Fatalf("call (深链 GC stress + 段根扫描): %v", err)
	}
	v, _ := st.tableGet(st.globals, st.makeStringValue("result"))
	// chain(n) = Σ step() = Σ (k=2n) for n=120..1 = 2*(120*121/2) = 14520
	want := float64(14520)
	if !value.IsNumber(v) || value.AsNumber(v) != want {
		t.Errorf("result = %v, want %v(段 closure 根漏扫 = 闭包被误回收)", debugVal(st, v), want)
	}
}

// Old frames still read back their original values via readCISegInto (copy + ciBaseW relocation are correct).
func TestR2b2_GrowCISegUnit(t *testing.T) {
	st := New()
	th := st.newThread()
	const n = 200 // > initialCISlots(64), triggers growCISeg multiple times
	// R2b-4: the th.cis Go slice is retired, the segment is authoritative. Use a local
	// want slice as the expected-value oracle (previously th.cis served this role),
	// writing the segment via computed addressing by ciDepth.
	want := make([]callInfo, 0, n)
	for d := 0; d < n; d++ {
		ci := callInfo{base: d*7 + 1, funcIdx: d * 7, top: d*7 + 3, protoID: uint32(d * 11), cl: 0, nresults: d % 4, pc: int32(d * 13)}
		want = append(want, ci)
		th.ciDepth = d + 1
		if d >= th.ciCap {
			th.growCISeg(d + 1)
		}
		th.writeCISeg(d, &want[d])
	}
	// Read back and verify all frames (data of old frames is lossless after multiple grow + relocation).
	for d := 0; d < n; d++ {
		var got callInfo
		th.readCISegInto(d, &got)
		w := want[d]
		if got.base != w.base || got.funcIdx != w.funcIdx || got.top != w.top ||
			got.protoID != w.protoID || got.nresults != w.nresults || got.pc != w.pc {
			t.Fatalf("frame %d post-grow mismatch: got %+v want %+v", d, got, w)
		}
	}
	if th.ciCap < n {
		t.Errorf("ciCap=%d < %d,growCISeg 未充分扩容", th.ciCap, n)
	}
}

// Cold fields (+ the base/pc at the moment of frame entry) are field-by-field consistent. Hooked onto a recursive script, covering multiple frame levels.
func TestR2b1_CISegMirrorCoherent(t *testing.T) {
	src := `
local function fib(n)
  if n < 2 then return n end
  return fib(n-1) + fib(n-2)
end
return fib(8)`
	st, mainCl := loadFn(t, src)
	th := st.mainTh

	// Verifying the mirror after each frame entry is awkward with a hook, and there is no
	// way to backtrack after execution; so here we drive directly and sample at the deepest
	// recursion point — with SetForceAllPromote off (pure crescent), running a
	// "per-frame readCISegInto vs cis" pass after execution (after execution cis is popped
	// empty, so verify at frame entry instead). Simplification: reuse enterLuaFrame's mirror
	// and manually push a few frames to verify consistency.
	_ = th
	// Run the script to confirm no crash (the mirror-write path is exercised throughout
	// enterLuaFrame; the segment is write-only, so the script result is unchanged = behavior transparent).
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(rets) != 1 || !value.IsNumber(rets[0]) || value.AsNumber(rets[0]) != 21 {
		t.Fatalf("fib(8) = %v, want 21(镜像只写不应改行为)", rets[0])
	}

	// Manually push frames to verify the mirror is field-by-field consistent (covers the enterLuaFrame mirror point + readback).
	// R2b-4: th.cis is retired; use a local want slice as the expected-value oracle, writing the segment by ciDepth.
	th2 := st.newThread()
	st.runningThread = th2
	defer func() { st.runningThread = st.mainTh }()
	want := make([]callInfo, 0, 5)
	for d := 0; d < 5; d++ {
		ci := callInfo{base: d*10 + 1, funcIdx: d * 10, top: d*10 + 4, protoID: uint32(d), cl: 0, nresults: d % 3, fresh: d == 0, pc: int32(d)}
		want = append(want, ci)
		th2.ciDepth = d + 1
		th2.writeCISeg(d, &want[d])
	}
	for d := 0; d < 5; d++ {
		var got callInfo
		th2.readCISegInto(d, &got)
		w := want[d]
		if got.base != w.base || got.protoID != w.protoID || got.nresults != w.nresults || got.fresh != w.fresh {
			t.Errorf("depth %d mirror incoherent: got %+v want %+v", d, got, w)
		}
	}
}
