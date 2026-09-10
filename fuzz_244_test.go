package wangshu_test

import (
	"strings"
	"testing"

	"github.com/Liam0205/wangshu/test/testutil"
)

// TestUnpackAtInt32BoundaryDoesNotCrash covers #244, where the CRASH is in the oracle, not in wangshu.
//
// unpack({}, -2147483648) segfaults PUC 5.1.5 -- both the embedded oracle and the real lua5.1 binary dump
// core. luaB_unpack computes n = e - i + 1 in int and guards with n <= 0, but the subtraction is
// signed-overflow UB: gcc -O2 deletes that check as unreachable and lua_checkstack is then called with a
// NEGATIVE size, which it accepts because both of its comparisons are false. The same source at -O0
// raises cleanly, so the crash is optimization-dependent, and the shim builds at -O2.
//
// The window is NOT a fixed pair of values -- it shifts with e, which defaults to #t and is overridden by
// the third argument: crash iff i32 <= e32 and (e32 - i32 + 1) > INT_MAX. The oracle-side skip guard and
// its own two-directional cases live in internal/oracle/unpack_guard_test.go; this test covers only
// wangshu's side, which must stay sane across the whole range and never crash.
func TestUnpackAtInt32BoundaryDoesNotCrash(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"INT_MIN raises cleanly",
			`local ok,e=pcall(function() return select("#",unpack({},-2147483648)) end) return tostring(e)`,
			"too many results to unpack"},
		{"INT_MIN+1 raises cleanly",
			`local ok,e=pcall(function() return select("#",unpack({},-2147483647)) end) return tostring(e)`,
			"too many results to unpack"},
		{"hex spelling of INT_MIN",
			`local ok,e=pcall(function() return select("#",unpack({},0X80000000)) end) return tostring(e)`,
			"too many results to unpack"},
		// Just outside the crash window, and ordinary uses: these must keep working.
		{"INT_MAX start is an empty range",
			`return tostring(select("#",unpack({},0X7FFFFFFF)))`, "0"},
		{"a small negative start counts up to the border",
			`return tostring(select("#",unpack({},-2)))`, "3"},
		{"explicit range", `return table.concat({unpack({1,2,3},2,3)},",")`, "2,3"},
		{"whole table", `return table.concat({unpack({1,2,3})},",")`, "1,2,3"},
		// The crash window SHIFTS WITH e, which a first guard missed by sampling only unpack({}, i):
		// with e = 3 the boundary moves by 3, and with an explicit huge j it reaches i = 0. wangshu
		// must stay sane across all of them.
		{"non-empty table shifts the boundary",
			`local ok,e=pcall(function() return select("#",unpack({1,2,3},-2147483646)) end) return tostring(e)`,
			"too many results to unpack"},
		{"explicit huge end index",
			`local ok,e=pcall(function() return select("#",unpack({},-1,2147483647)) end) return tostring(e)`,
			"too many results to unpack"},
		// Just outside the window BOTH engines raise "too many results" -- they agree there, which is
		// exactly why it must stay compared rather than skipped.
		{"just outside the shifted window agrees",
			`local ok,e=pcall(function() return select("#",unpack({1,2,3},-2147483643)) end) return tostring(e)`,
			"too many results to unpack"},
		{"a wrapped index that narrows into range",
			`return tostring(select("#",unpack({1,2,3},4294967297)))`, "3"},
	} {
		// Suffix match: a raised message carries a chunkname:line prefix that is not the point here.
		if got := testutil.RunOne(t, tc.src).Str(); !strings.HasSuffix(got, tc.want) {
			t.Errorf("%s: got %q, want it to end with %q", tc.name, got, tc.want)
		}
	}
}
