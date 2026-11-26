//go:build wangshu_oracle_cgo && cgo

package oracle

import (
	"strings"
	"testing"
)

// TestIpairsIteratorGuard pins the prelude's guard on the ipairs iterator, in both directions.
//
// The iterator narrows its control value with luaL_checkint, whose result for a NaN, an infinity or
// anything beyond int64 is undefined behaviour in C and differs between amd64 and arm64. Such a call
// must be excluded from comparison. The guard lives in the prelude, so only a real Exec runs it; this
// test is what fails when the check is deleted.
//
// Too wide matters as much as too narrow: a finite control value that both engines handle the same way
// must still be compared, or the guard silently removes coverage.
func TestIpairsIteratorGuard(t *testing.T) {
	const sentinel = "luaL_checkint UB range"

	for _, src := range []string{
		`print(pcall(ipairs({}), {1}, 0/0))`,
		`print(pcall(ipairs({}), {1}, 1/0))`,
		`print(pcall(ipairs({}), {1}, -1/0))`,
		`print(pcall(ipairs({}), {1}, 1e30))`,
		`print(pcall(ipairs({}), {1}, "1e30"))`, // luaL_checkint coerces numeric strings
		`for i, v in ipairs({}), {1, 2}, 0/0 do print(i, v) end`,
		// The script must not be able to steer the check by rebinding what it reads.
		`tonumber = function() return 0 end print(pcall(ipairs({}), {1}, 0/0))`,
		`tonumber = nil print(pcall(ipairs({}), {1}, 1/0))`,
	} {
		r := execT(t, src)
		if r.Verdict != VerdictLimit || !strings.Contains(r.Err, sentinel) {
			t.Errorf("must be skipped but was compared: %s\n  verdict=%v err=%q out=%q", src, r.Verdict, r.Err, r.Output)
		}
	}

	for _, tc := range []struct{ src, want string }{
		{`for i, v in ipairs({4, 5}) do print(i, v) end`, "1\t4\n2\t5\n"},
		{`print(ipairs({})({7, 8, 9}, 1))`, "2\t8\n"},
		{`print(ipairs({})({7, 8, 9}, "1"))`, "2\t8\n"},
		{`print(ipairs({})({7, 8, 9}, 4294967297.5))`, "2\t8\n"},
		{`print(select("#", ipairs({})({7}, 1)))`, "0\n"},
		// One guarded iterator, so identity compares the way it does without the wrapper.
		{`print(ipairs({}) == ipairs({}))`, "true\n"},
	} {
		r := execT(t, tc.src)
		if r.Verdict != VerdictOK || r.Output != tc.want {
			t.Errorf("%s\n  verdict=%v err=%q\n  got  %q\n  want %q", tc.src, r.Verdict, r.Err, r.Output, tc.want)
		}
	}

	// Argument errors still come from the real iterator and the real factory, and the wrapper forwards
	// the arguments as received: a missing control value is "no value", not the nil a fixed parameter
	// list would turn it into. The position prefix and the iterator's name are the prelude's own, the
	// same on both engines, so only the part after them is pinned.
	for _, tc := range []struct{ src, want string }{
		{`local f = ipairs({}) print(pcall(f))`, "bad argument #2 to 'raw' (number expected, got no value)\n"},
		{`local f = ipairs({}) print(pcall(f, {}))`, "bad argument #2 to 'raw' (number expected, got no value)\n"},
		{`local f = ipairs({}) print(pcall(f, {}, "x"))`, "bad argument #2 to 'raw' (number expected, got string)\n"},
		{`local f = ipairs({}) print(pcall(f, 1, 1))`, "bad argument #1 to 'raw' (table expected, got number)\n"},
		{`print(pcall(ipairs))`, "bad argument #1 to 'ipairs' (table expected, got no value)\n"},
	} {
		r := execT(t, tc.src)
		if r.Verdict != VerdictOK || !strings.HasPrefix(r.Output, "false\t") || !strings.HasSuffix(r.Output, tc.want) {
			t.Errorf("%s\n  verdict=%v err=%q\n  got       %q\n  want tail %q", tc.src, r.Verdict, r.Err, r.Output, tc.want)
		}
	}
}
