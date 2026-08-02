//go:build wangshu_p4

package regression

import (
	"testing"

	wangshu "github.com/Liam0205/wangshu"
)

// TestP4HotLoopForcePromote covers the #218 crasher: a 100-million-iteration loop whose body is
// four assignments, compared between the P1 interpreter and the P4 force-all-promote path.
//
// It lives here rather than in testdata/fuzz/FuzzP4ForceAllPromote/ on the criterion in
// llmdoc/guides/unreproducible-crasher-triage.md. The input neither crashes nor diverges -- it is
// simply the heaviest seed in that corpus by about 6x (1.8s against under 0.3s for every other
// entry), and the coordinator replays the whole corpus in parallel at startup, which is what times
// out a worker. Same resolution as #123 and #166.
//
// The step budget and arena cap MIRROR fuzz_p4_test.go's. Running the loop to completion instead
// took 87 seconds -- 50x the harness -- because the budget is what bounds it there; a regression
// test that reproduces the shape but not the bounds is testing something else.
//
// No in-test deadline on purpose: the failure mode is "never returns", and the guide's rule is that
// non-termination is judged by the package-level go test -timeout, which takes the binary down with
// every goroutine stack rather than one t.Fatalf line.
func TestP4HotLoopForcePromote(t *testing.T) {
	const src = `function k() for B=0,100001000 do A=0 cA=0 A=C A=0 end end k()`
	prog, err := wangshu.Compile([]byte(src), "r")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	st1 := wangshu.NewState(wangshu.Options{MaxArenaBytes: 64 << 20})
	st1.SetStepBudget(1 << 20)
	_, err1 := prog.Run(st1)

	st4 := wangshu.NewState(wangshu.Options{MaxArenaBytes: 64 << 20})
	st4.SetStepBudget(1 << 20)
	st4.SetForceAllPromote(true)
	_, err4 := prog.Run(st4)

	// Both sides exhaust the step budget; what matters is that they AGREE, since a divergence
	// here would be a P4 miscompilation rather than a timing difference.
	if (err1 == nil) != (err4 == nil) {
		t.Fatalf("P1/P4 error existence diverged: p1=%v p4=%v", err1, err4)
	}
	if err1 != nil && err1.Error() != err4.Error() {
		t.Errorf("P1/P4 error text diverged:\n  p1=%v\n  p4=%v", err1, err4)
	}
}
