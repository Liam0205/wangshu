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
	// Two positions, both just under the cap, both found by the fuzzer on separate
	// days and both moved here for the same reason -- around 100M shifted elements
	// costs seconds, and the coordinator replays the corpus in parallel.
	for _, src := range []string{
		`t={0} table.insert(t,4194967278,"") return tostring(t[4])`,
		`t={0} table.insert(t,4194967288,(t)) return tostring(t[4])`,
	} {
		runInsertShift(t, src)
	}
}

func runInsertShift(t *testing.T, src string) {
	t.Helper()
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

// TestInsertShiftThresholdsStayDistinct pins the two thresholds that four separate nightly
// crashers came from, and pins that they are DIFFERENT numbers on purpose.
//
// The product cap (2^27) is a correctness boundary: below it wangshu must perform the shift
// because lua5.1 does, and rejecting it would be a divergence. The harness skip (2^20) is
// about cost only -- the fuzz coordinator replays the whole corpus in parallel at startup, so
// a seed costing seconds takes the worker down even though both engines agree on it.
//
// #203, #208 and #209 were all the same shape, filed on three separate nights, because for a
// long time one constant answered both questions. Nothing pinned the split afterwards, so
// this test does: a span in the band between the two must still be performed by the product
// (not capped) while being cheap enough that the corpus stays fast.
func TestInsertShiftThresholdsStayDistinct(t *testing.T) {
	// Both constants are unexported and in different packages (tableInsertShiftCap in
	// internal/stdlib, the skip in internal/oracle/prelude.go), so this asserts the SPLIT
	// behaviourally rather than comparing literals that could drift out of sync with either.
	//
	// A span inside the band -- above the harness skip (2^20), below the product cap (2^27) --
	// must be PERFORMED by the product. That catches the CAP being lowered onto the skip.
	//
	// It does NOT catch the skip being raised onto the cap: this package never touches
	// internal/oracle, so it cannot observe where the skip sits, and an audit confirmed the
	// test stays green while the corpus goes back to 3s per seed. That direction is pinned by
	// TestInsertShiftSkipThreshold in internal/oracle, which is where it can be seen. Two
	// thresholds in two packages need two enforcers.
	src := `t={0} table.insert(t,-2097151,"") return tostring(t[4])`
	st := wangshu.NewState(wangshu.Options{MaxArenaBytes: 512 << 20})
	prog, err := wangshu.Compile([]byte(src), "r")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	start := time.Now()
	res, rerr := prog.Run(st)
	if rerr != nil {
		t.Fatalf("run: %v -- a span between the two thresholds must be PERFORMED, not capped", rerr)
	}
	if len(res) == 0 || res[0].Str() != "nil" {
		t.Errorf("t[4] = %v, want nil", res[0].Display())
	}
	elapsed := time.Since(start)
	t.Logf("span ~2M (between skip and cap) shifted in %v", elapsed.Round(time.Millisecond))
	// And it must stay CHEAP: the reason the skip exists is that a corpus seed costing seconds
	// kills the fuzz worker, so a span just above the skip has to be affordable. A regression
	// that made this band slow would put the skip back in the position that caused #203/#208/#209.
	if elapsed > 5*time.Second {
		t.Errorf("a span just above the harness skip took %v; the skip is sized on the assumption "+
			"that this band is cheap", elapsed.Round(time.Millisecond))
	}
}
