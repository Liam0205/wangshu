//go:build wangshu_oracle_cgo && cgo

package oracle

import (
	"strings"
	"testing"
	"time"
)

// TestInsertShiftSkipThreshold pins WHERE the harness's insert-shift skip sits.
//
// This is the half of the threshold split that a test outside this package cannot see.
// test/regression's TestInsertShiftThresholdsStayDistinct asserts that a span in the band is
// PERFORMED by the product, which catches the product cap being lowered onto the skip -- but
// it never touches this package, so raising the skip instead leaves it green while the corpus
// goes back to costing seconds per seed. That is exactly the state #203, #208 and #209 were
// filed from, so it needs its own enforcer here.
//
// The two thresholds answer different questions and must stay different numbers: the product
// cap (2^27) is a correctness boundary, since lua5.1 performs the shift below it, while this
// skip (2^20) is about what can sit in a corpus the fuzz coordinator replays in parallel.
func TestInsertShiftSkipThreshold(t *testing.T) {
	pre := Prelude(testKeep)

	// Just ABOVE the skip: must raise the limit sentinel, i.e. be excluded from comparison.
	// A span of 2^20+1 is the smallest one the skip is meant to cover.
	above := Exec(`t={0} table.insert(t,-1048576,"") print(#t)`, pre, Limits{})
	if !strings.Contains(above.Err, LimitSentinel) {
		t.Errorf("a span just above the skip was COMPARED rather than skipped (err=%q); "+
			"the corpus will go back to costing seconds per seed", above.Err)
	}

	// Just BELOW the skip: must still be compared, so the skip does not swallow cheap work.
	// Both engines perform this in milliseconds.
	start := time.Now()
	below := Exec(`t={0} table.insert(t,-1048570,"") print(#t)`, pre, Limits{})
	elapsed := time.Since(start)
	if strings.Contains(below.Err, LimitSentinel) {
		t.Errorf("a span just below the skip was SKIPPED (err=%q); the skip is meant to exclude "+
			"only the expensive band, not ordinary work", below.Err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("a span just below the skip took %v; the skip is sized on the assumption that "+
			"everything below it is cheap", elapsed.Round(time.Millisecond))
	}
	t.Logf("skip boundary holds: above=skipped, below=compared in %v", elapsed.Round(time.Millisecond))
}

// TestInsertShiftCumulativeBudget pins the OTHER half of the skip: the total shifted-element
// count across a script, not just the largest single call.
//
// Keying only on one call's span left a loop of just-below-threshold inserts fully compared --
// 99 iterations at 2^20-6 elements cost about 11 seconds across the two engines, the same
// corpus hazard in the same family, merely split across calls. The instruction-count hook
// cannot interrupt it either, because each shift happens inside one C call, so the per-call
// span check was the only thing standing there and it does not see accumulation.
//
// An audit found this after the per-call threshold already had enforcers in both packages,
// which is why it gets its own: "the family is covered" was true for single calls and false
// once the same work was spread over several.
func TestInsertShiftCumulativeBudget(t *testing.T) {
	pre := Prelude(testKeep)

	// A loop whose per-call spans are each UNDER the single-call threshold but whose total is
	// far over it must be excluded.
	loop := Exec(`t={0} for k=1,99 do table.insert(t,-1048570,"") end print(#t)`, pre, Limits{})
	if !strings.Contains(loop.Err, LimitSentinel) {
		t.Errorf("a loop of sub-threshold inserts was COMPARED (err=%q); each call passes the "+
			"per-call span check, so only the cumulative budget can catch this", loop.Err)
	}

	// Ordinary repeated inserts must still be compared: the budget excludes the expensive band,
	// not loops in general.
	for _, src := range []string{
		`t={0} for k=1,50 do table.insert(t,-100,"") end print(#t)`,
		`t={} for k=1,100 do table.insert(t,k) end print(#t)`,
	} {
		r := Exec(src, pre, Limits{})
		if strings.Contains(r.Err, LimitSentinel) {
			t.Errorf("ordinary repeated inserts were SKIPPED (err=%q) for %s", r.Err, src)
		}
	}
}

// TestShiftBudgetCoversPositionSign pins that shift work is charged regardless of the position's
// SIGN, and for table.remove as well as table.insert.
//
// Both checks originally sat inside the negative-position branch, so a positive position was
// neither span-checked nor charged: inserting at position 1 in a 40000-iteration loop shifts the
// whole array every call and cost 15 seconds across the two engines while comparing normally.
// Removing at position 1 is the same shape and had no shim at all.
//
// This is the third gap of one family, each found by a separate audit round: the per-call span
// missed accumulation, the cumulative budget missed positive positions, and the insert shim
// missed remove. What they have in common is that the COST is the number of elements moved,
// which is what the budget now keys on rather than any property of the position.
func TestShiftBudgetCoversPositionSign(t *testing.T) {
	pre := Prelude(testKeep)
	for _, tc := range []struct{ name, src string }{
		{"insert at a positive position in a loop",
			`t={} for i=1,40000 do table.insert(t,1,0) end print(#t)`},
		{"remove at a positive position in a loop",
			`t={} for i=1,40000 do t[i]=i end for i=1,20000 do table.remove(t,1) end print(#t)`},
	} {
		r := Exec(tc.src, pre, Limits{})
		if !strings.Contains(r.Err, LimitSentinel) {
			t.Errorf("%s was COMPARED (err=%q); the budget must charge moved elements whatever "+
				"the position's sign", tc.name, r.Err)
		}
	}
	// Ordinary uses of both must still be compared, including the low-position forms -- the
	// budget excludes the expensive band, not shifting as such.
	for _, src := range []string{
		`t={} for i=1,500 do table.insert(t,1,i) end print(#t,t[1])`,
		`t={1,2,3,4,5} for i=1,3 do table.remove(t,2) end print(#t,t[2])`,
		`t={1,2,3} print(table.remove(t,1),#t)`,
	} {
		r := Exec(src, pre, Limits{})
		if strings.Contains(r.Err, LimitSentinel) {
			t.Errorf("ordinary shifting was SKIPPED (err=%q) for %s", r.Err, src)
		}
	}
}
