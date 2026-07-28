package regression

import (
	"testing"
	"time"

	wangshu "github.com/Liam0205/wangshu"
)

// TestInsertShiftJustUnderCap covers a table.insert whose position narrows to a
// span just below tableInsertShiftCap, so BOTH engines perform the shift.
//
// It lives here rather than in testdata/fuzz/FuzzOracleDiff/ on the criterion in
// llmdoc/guides/unreproducible-crasher-triage.md: the fuzz coordinator replays the
// whole seed corpus in parallel at startup, so a seed costing ~10s of CPU is
// amplified by -parallel=N and kills the worker with "fuzzing process hung or
// terminated unexpectedly". That is what happened when it was added as a seed --
// the input is correct and symmetric, it is simply too heavy to sit there. Same
// resolution as the #123 round.
//
// Serial, single process, one shape: the point is that the work COMPLETES rather
// than being rejected by the cap, since the cap is sized to refuse only an
// uninterruptible hang.
func TestInsertShiftJustUnderCap(t *testing.T) {
	const src = `t={0} table.insert(t,4194967278,"") return tostring(t[4])`
	st := wangshu.NewState(wangshu.Options{MaxArenaBytes: 512 << 20})
	prog, err := wangshu.Compile([]byte(src), "r")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	start := time.Now()
	res, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res) == 0 || res[0].Str() != "nil" {
		t.Errorf("t[4] = %v, want nil", res[0].Display())
	}
	t.Logf("span ~100M shifted in %v (accepted, not capped)", time.Since(start).Round(time.Millisecond))
}
