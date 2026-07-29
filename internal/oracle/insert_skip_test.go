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
