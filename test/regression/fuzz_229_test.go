//go:build wangshu_p4 && wangshu_profile

// fuzz_229_test.go — regression pin for issue #229 (fuzz seed
// 6e4264d640c40292): a nested executeFrom returning through doReturn's
// entry-frame branch narrowed th.top below the still-live caller frame's
// logical top, and the GC's above-top residue clear then nil'd the caller's
// live registers.
package regression

import (
	"testing"

	"github.com/Liam0205/wangshu"
)

// TestCallerTopSurvivesNestedTailCallReturn pins the #229 miscompilation.
//
// Shape requirements (each one is load-bearing; drop any and the bug hides):
//   - the callee is a multi-return tail call (`return b:n()` = SELF + TAILCALL +
//     RETURN B=0), so the gibbous TailCall host helper drives the callee chain
//     through a NESTED executeFrom whose entryDepth is the tail-called frame's
//     own depth — its RETURN therefore takes doReturn's entry-frame branch while
//     a live caller frame still sits below;
//   - the caller is hot enough to promote (the loop runs it ~70 times), so the
//     gibbous host path is actually taken;
//   - the loop body builds a table WITH elements (`A={0}`), so a NEWTABLE result
//     lands in a register above the narrowed top and a SETLIST follows to read
//     it back.
//
// Before the fix the GC's stale-residue clear (visitThreadValues nil-clears
// [top, size)) wiped that register, and P4 raised "SETLIST: not a table" where
// P1 succeeded.
func TestCallerTopSurvivesNestedTailCallReturn(t *testing.T) {
	const src = `o2={n=function() return 0 end} ` +
		`function f(b) return b:n() end ` +
		`for A=0,70 do f(o2) A={0} end ` +
		`return "ok"`

	prog, err := wangshu.Compile([]byte(src), "fuzz-229")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	run := func(forceAll bool) ([]wangshu.Value, error) {
		st := wangshu.NewState(wangshu.Options{MaxArenaBytes: 64 << 20})
		st.SetStepBudget(1 << 20)
		st.SetForceAllPromote(forceAll)
		return prog.Run(st)
	}

	resP1, errP1 := run(false)
	if errP1 != nil {
		t.Fatalf("P1 interpreter must succeed, got %v", errP1)
	}
	resP4, errP4 := run(true)
	if errP4 != nil {
		t.Fatalf("P4 diverged from P1 (caller register clobbered): %v", errP4)
	}

	if len(resP1) != len(resP4) {
		t.Fatalf("result count: P1=%d P4=%d", len(resP1), len(resP4))
	}
	for i := range resP1 {
		if resP1[i].Display() != resP4[i].Display() {
			t.Errorf("result[%d]: P1=%q P4=%q",
				i, resP1[i].Display(), resP4[i].Display())
		}
	}
}
