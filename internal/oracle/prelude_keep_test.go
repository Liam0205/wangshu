package oracle

import (
	"strings"
	"testing"
)

// TestSortedIterUsesThePreWrapTostring pins how preludeSortedIter reaches the un-normalized tostring.
//
// Three mechanisms were tried. Reading the global finds the width-normalizing WRAPPER, and truncating to
// 8 hex digits makes more keys tie, changing the order this comparator exists to stabilize. Publishing the
// raw function as a GLOBAL failed twice over: the whitelist trim runs before this section and erased it,
// so the comparator called a nil value identically on both engines and the harness reported PASS while
// masking everything after it; and once kept past the trim, the global handed scripts an un-normalized PUC
// renderer, reopening the very divergence #233 closes.
//
// The answer needed no mechanism at all: Prelude() concatenates every section into ONE Lua chunk, so
// preludeGuards' local is still in scope. This test pins that, because the property is invisible at the
// call site -- someone splitting Prelude() into separate chunks would break sorting silently.
func TestSortedIterUsesThePreWrapTostring(t *testing.T) {
	p := Prelude(GlobalSet{})

	guards := strings.Index(p, "local __rawtostring = __tostring")
	if guards < 0 {
		t.Fatal("preludeGuards no longer captures the pre-wrap tostring as a local")
	}
	sorted := strings.Index(p, "return __rawtostring(a) < __rawtostring(b)")
	if sorted < 0 {
		t.Fatal("the key comparator no longer uses the pre-wrap tostring")
	}
	if sorted < guards {
		t.Error("the comparator appears before the local that defines it: sections were reordered, and " +
			"the local would be nil at the comparator")
	}
	// No global may carry it: a script could then call PUC's un-normalized renderer directly.
	if strings.Contains(p, "__ORACLE_RAW_TOSTRING") {
		t.Error("the raw tostring is published as a global again; a script reaching it (walking the " +
			"globals suffices) gets PUC's un-normalized address rendering, reopening #233")
	}
}
