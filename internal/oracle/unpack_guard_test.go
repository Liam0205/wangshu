//go:build wangshu_oracle_cgo && cgo

package oracle

import (
	"strings"
	"testing"
)

// TestUnpackGuardWidth pins the ORACLE-side skip guard for #244, in both directions.
//
// The wangshu-side test cannot reach this: the guard lives in the prelude, so only a real oracle Exec
// exercises it. That gap is why two wrong versions shipped before this one -- the first keyed on i alone
// (the window shifts with e), and the second re-derived luaL_checkint's narrowing by hand instead of
// reusing __ckint0, leaving three crash shapes live.
//
// Too WIDE matters as much as too narrow: a skipped input that both engines handle identically is silent
// coverage loss, and unlike a crash it produces no signal at all.
func TestUnpackGuardWidth(t *testing.T) {
	const sentinel = "unpack element count overflows int"

	// The trim erases every global not listed, INCLUDING unpack -- with an empty set the wrapper never
	// runs and every case looks unguarded. The fuzz target derives this list from wangshu's own globals;
	// here it is spelled out, which is enough for these shapes.
	keep := GlobalSet{
		Top: []string{"unpack", "select", "print", "table", "string", "math", "pcall", "tostring",
			"tonumber", "type", "error", "setmetatable", "getmetatable", "ipairs", "pairs", "next"},
		Nested: map[string][]string{
			"table":  {"concat", "insert", "remove", "sort", "maxn"},
			"string": {"rep", "format", "byte", "char", "sub", "gsub", "find", "match", "len", "upper", "lower", "reverse"},
			"math":   {"floor", "huge"},
		},
	}

	// MUST skip: PUC segfaults on these. Running them without the guard takes the test binary down,
	// which is exactly why they are listed rather than left to the fuzzer to rediscover.
	for _, src := range []string{
		`A(unpack({},0X80000000))`,       // the filed seed
		`A(unpack({},-2147483647))`,      // INT_MIN+1
		`A(unpack({1,2,3},-2147483646))`, // window shifted by #t
		`A(unpack({},-1,2147483647))`,    // explicit huge end index
		`A(unpack({},1/0,2147483647))`,   // nonfinite narrows to 0
		`A(unpack({},-1/0,2147483647))`,  // and the negative one
		`A(unpack({},"-2147483648"))`,    // luaL_optint coerces numeric strings
		`A(unpack({},1e20,2147483647))`,  // beyond int64: the UB cast yields 0
	} {
		r := Exec(src, Prelude(keep), Limits{})
		if !strings.Contains(r.Err, sentinel) {
			t.Errorf("must be guarded but was not: %s\n  verdict=%v err=%q", src, r.Verdict, r.Err)
		}
	}

	// MUST NOT skip: both engines handle these, so skipping them loses comparison.
	for _, src := range []string{
		`print(select("#",unpack({1,2,3})))`,
		`print(select("#",unpack({1,2,3},2)))`,
		`print(table.concat({unpack({1,2,3},2,3)},","))`,
		`print(select("#",unpack({},-2)))`,
		`print(select("#",unpack({},0X7FFFFFFF)))`,
		`print(select("#",unpack({1,2,3},-2147483643)))`, // just outside the shifted window
		`print(select("#",unpack({1,2,3},4294967297)))`,  // wraps into range: answers 3
		`print(select("#",unpack({1,2,3},1e20)))`,        // narrows to 0: answers 4
		`print(select("#",unpack({},-2147483646.5)))`,    // truncates toward zero to INT_MAX
	} {
		r := Exec(src, Prelude(keep), Limits{})
		if strings.Contains(r.Err, sentinel) {
			t.Errorf("must stay comparable but was skipped: %s\n  err=%q", src, r.Err)
		}
	}
}
