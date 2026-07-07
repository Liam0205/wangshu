//go:build wangshu_p4 && wangshu_profile

// seg2seg_deopt_redo_test.go — issue #66 subtask 3: targeted injection
// tests for the seg2seg deopt-redo path. The deopt-idempotency property
// (admission guarantees: no param write, no observable side effect before
// the deopt point) is the correctness pillar of issue #50 seg2seg, but it
// was only covered indirectly (difftest / fuzz). These tests inject a
// run-time guard miss into an already-promoted seg2seg callee and assert:
//
//   - the result stays byte-equal to the interpreter (deopt-redo produces
//     the correct value), and
//   - SegToSegDeoptCount moves (a white-box probe proving the run actually
//     took the top-level deopt-redo branch, not "never promoted").
//
// Arch-neutral: the assertions only depend on promote + deopt-redo +
// byte-equal, so this runs on amd64 and arm64 alike (the arm64 emitter
// bumps the same counter on its top-level redo branch).

package peroptranslator_test

import (
	"testing"

	"github.com/Liam0205/wangshu"
	"github.com/Liam0205/wangshu/internal/gibbous/jit/peroptranslator"
)

// TestSeg2SegDeoptRedo_ArithGuardMiss: a recursive seg2seg callee whose
// arith guard misses at the TOP frame (shallow), complementing the nested
// (deep) case below. seg2seg is a property of the RECURSIVE self-call
// (the promoted proto `call`s directly into its own segment), so a plain
// leaf `add(a,b)` never engages it — the callee must itself contain the
// seg2seg CALL. `poly(n, x)` recurses and adds x*n each level; warmed with
// numbers it goes seg2seg-recursive, then a string x makes the arith guard
// miss immediately at the top frame, driving a top-level deopt-redo. Lua
// coerces the numeric string, so the result stays byte-equal.
func TestSeg2SegDeoptRedo_ArithGuardMiss(t *testing.T) {
	// Must stay sequential: SegToSegHitCount / SegToSegDeoptCount are
	// process-global counters read via before/after deltas, so a
	// t.Parallel() here (or on another test touching these counters)
	// would let a concurrent run's increments leak into the delta. Do
	// NOT add t.Parallel().
	src := `
local function poly(n, x)
  if n <= 0 then return 0 else return x * n + poly(n - 1, x) end
end
local acc = 0
for i = 1, 30 do acc = acc + poly(4, 2) end  -- warm + promote (all-number)
local coerced = poly(4, "2")                 -- string x -> top-frame deopt
return acc, coerced
`
	prog, err := wangshu.Compile([]byte(src), "seg2segdeopt")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(true)
	beforeHit := peroptranslator.SegToSegHitCount.Load()
	beforeDeopt := peroptranslator.SegToSegDeoptCount.Load()
	res, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// Hard gate: byte-equal to the interpreter. poly(4,2) = 2*(4+3+2+1)
	// = 20; acc = 30 * 20 = 600; coerced = poly(4,"2") = 20 (Lua coerces).
	if len(res) != 2 || res[0].Display() != "600" || res[1].Display() != "20" {
		t.Fatalf("got %v, want [600 20]", res)
	}
	seg := peroptranslator.SegToSegHitCount.Load() - beforeHit
	deopt := peroptranslator.SegToSegDeoptCount.Load() - beforeDeopt
	t.Logf("seg2seg hits = %d, deopt-redo = %d", seg, deopt)
	// The warm loop must have engaged the native seg2seg path...
	if seg == 0 {
		t.Errorf("SegToSegHitCount stalled — seg2seg dispatch never engaged " +
			"on this platform, so the deopt injection is not meaningful")
	}
	// ...and the string operand must have driven a top-level deopt-redo.
	if deopt == 0 {
		t.Errorf("SegToSegDeoptCount stalled — the string operand did not " +
			"take the deopt-redo branch (guard miss not exercised?)")
	}
}

// TestSeg2SegDeoptRedo_NestedPropagation: a recursive seg2seg callee whose
// guard misses several native frames DEEP must propagate the deopt up the
// chain (each level rets) and redo only at the TOP — a middle frame must
// not consume the deopt. `sumdown(n, x)` recurses on n and adds x each
// level; warmed with numbers it goes seg2seg-recursive, then a string x
// injected at the deepest level makes an arith guard miss well below the
// top frame. Correctness (byte-equal) proves the deopt was consumed at the
// right place, not mid-chain.
func TestSeg2SegDeoptRedo_NestedPropagation(t *testing.T) {
	// Sequential-only: see the note in TestSeg2SegDeoptRedo_ArithGuardMiss
	// — these two tests share the process-global SegToSeg* counters and
	// must not run in parallel. Do NOT add t.Parallel().
	// sumdown(n, x): returns x + sumdown(n-1, x) for n>0, else 0. With
	// x=1 the result is n. The recursion is seg2seg (arith + compare +
	// self-call via upvalue, no param write). Warm it, then call once
	// with a string x so the arith guard misses at every recursion level
	// — the deepest miss must propagate up to the top and redo there.
	src := `
local function sumdown(n, x)
  if n <= 0 then return 0 else return x + sumdown(n - 1, x) end
end
local acc = 0
for i = 1, 30 do acc = acc + sumdown(5, 1) end  -- warm + promote
local coerced = sumdown(5, "2")                 -- string x -> nested deopt
return acc, coerced
`
	prog, err := wangshu.Compile([]byte(src), "seg2segnested")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(true)
	beforeHit := peroptranslator.SegToSegHitCount.Load()
	beforeDeopt := peroptranslator.SegToSegDeoptCount.Load()
	res, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// Hard gate: byte-equal. acc = 30 * sumdown(5,1) = 30 * 5 = 150;
	// coerced = sumdown(5, "2") = 5 * 2 = 10 (Lua coerces "2").
	if len(res) != 2 || res[0].Display() != "150" || res[1].Display() != "10" {
		t.Fatalf("got %v, want [150 10]", res)
	}
	seg := peroptranslator.SegToSegHitCount.Load() - beforeHit
	deopt := peroptranslator.SegToSegDeoptCount.Load() - beforeDeopt
	t.Logf("seg2seg hits = %d, deopt-redo = %d", seg, deopt)
	if seg == 0 {
		t.Errorf("SegToSegHitCount stalled — recursive seg2seg never engaged")
	}
	if deopt == 0 {
		t.Errorf("SegToSegDeoptCount stalled — nested guard miss did not " +
			"reach the top-level deopt-redo branch")
	}
}
