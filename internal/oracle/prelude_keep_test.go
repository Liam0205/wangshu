package oracle

import "strings"
import "testing"

// TestSortedIterHelperSurvivesTrim pins the whitelist-trim interaction that made a broken oracle look
// like a passing test.
//
// preludeSortedIter needs the PRE-WRAP tostring, which preludeGuards publishes as a global. The trim
// (step 4) runs BEFORE sorted iteration is installed (step 5), so a global missing from the keep list is
// erased and the comparator calls a nil value on every reference-keyed table. Both engines raise
// identically, so the harness reports PASS -- while masking every comparison after it. That is the same
// shape of masking an earlier round had already found in this area, which is why it is pinned rather than
// left to the suites.
func TestSortedIterHelperSurvivesTrim(t *testing.T) {
	p := Prelude(GlobalSet{})
	if !strings.Contains(p, "__ORACLE_RAW_TOSTRING = ") {
		t.Fatal("preludeGuards no longer publishes the pre-wrap tostring")
	}
	if !strings.Contains(p, "local __rawtostring = __ORACLE_RAW_TOSTRING") {
		t.Fatal("preludeSortedIter no longer reads the pre-wrap tostring")
	}
	// The trim must keep it. Its keep list is emitted into the prelude text, so look for it there.
	trimIdx := strings.Index(p, "__oracle_readout")
	if trimIdx < 0 {
		t.Fatal("keep list not found in prelude")
	}
	if !strings.Contains(p, `"__ORACLE_RAW_TOSTRING"`) &&
		!strings.Contains(p, "__ORACLE_RAW_TOSTRING']=true") &&
		!strings.Contains(p, "__ORACLE_RAW_TOSTRING\"]=true") {
		t.Error("__ORACLE_RAW_TOSTRING is not in the trim's keep list: sorted iteration will call a " +
			"nil value on every reference-keyed table, symmetrically on both engines, so the harness " +
			"reports PASS while masking everything after it")
	}
}
