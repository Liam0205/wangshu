package language_test

import (
	"testing"

	"github.com/Liam0205/wangshu/test/testutil"
)

// TestTableInsert_NoBoundsCheck51 pins that table.insert accepts ANY position,
// which is what PUC 5.1's ltablib.c tinsert does -- it has no bounds check at
// all. The "position out of bounds" error belongs to Lua 5.2+, and raising it
// diverged from the 5.1 oracle on position 0, negatives, and anything past the
// end.
//
// 5.1 computes e = #t + 1, raises e to pos when pos is larger ("grow the array
// if necessary"), shifts [pos, e-1] up one, then writes at pos. A pos at or
// below 0 shifts nothing, since the loop runs from e down to pos+1.
func TestTableInsert_NoBoundsCheck51(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"pos 0 on empty",
			`local t={} table.insert(t,0,1) return #t..","..tostring(t[0])`, "0,1"},
		{"pos past end",
			`local t={} table.insert(t,5,1) return #t..","..tostring(t[5])`, "0,1"},
		{"pos 0 shifts nothing",
			`local t={"a","b"} table.insert(t,0,"z") return #t..","..tostring(t[0])..","..tostring(t[1])`, "0,z,nil"},
		{"negative pos",
			`local t={"a","b"} table.insert(t,-1,"z") return #t..","..tostring(t[-1])`, "0,z"},
		{"far past end keeps n",
			`local t={"a","b"} table.insert(t,99,"z") return #t..","..tostring(t[99])`, "2,z"},
		{"in-range still shifts",
			`local t={"a","b"} table.insert(t,2,"z") return #t..","..t[1]..","..t[2]..","..t[3]`, "3,a,z,b"},
		{"pos 1 on empty",
			`local t={} table.insert(t,1,"x") return #t..","..t[1]`, "1,x"},
	} {
		got := testutil.RunOne(t, tc.src)
		if !got.IsString() || got.Str() != tc.want {
			t.Errorf("%s: %s = %v, want %q", tc.name, tc.src, got.Display(), tc.want)
		}
	}
}

// TestTableInsert_PositionNarrowedToInt32 pins that the position goes through
// luaL_checkint's cast, double -> lua_Integer -> int, so it is narrowed to 32
// bits. Taking Go's 64-bit int instead put a large finite position on the
// un-narrowed key, and a nonfinite one made the shift loop run for billions of
// iterations.
//
// These are the two-axis cases the first version of the table above missed: it
// stopped at position 99, inside the range the old bounds check used to reject.
func TestTableInsert_PositionNarrowedToInt32(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		// 2^32+2 narrows to 2, so it inserts at 2 like any in-range position
		{"2^32+2 narrows to 2",
			`local t={1,2,3} table.insert(t,4294967298,"X") return #t..","..tostring(t[4])..","..tostring(t[4294967298])`,
			"4,3,nil"},
		// nonfinite narrows to 0 and just writes, no shift
		{"inf narrows to 0",
			`local t={"a"} table.insert(t,1/0,"X") return #t..","..tostring(t[0])`, "0,X"},
		{"nan narrows to 0",
			`local t={"a"} table.insert(t,0/0,"X") return #t..","..tostring(t[0])`, "0,X"},
	} {
		got := testutil.RunOne(t, tc.src)
		if !got.IsString() || got.Str() != tc.want {
			t.Errorf("%s: %s = %v, want %q", tc.name, tc.src, got.Display(), tc.want)
		}
	}
}

// TestTableInsert_ShiftSpanCapped pins the fail-fast bound on the shift.
//
// 5.1 places no bound on the distance, so a position that narrows far below 1
// makes the loop run |pos| times -- 2^31 narrows to INT32_MIN and the official
// build performs ~2.1 billion rawget/rawset pairs, measured at 2m21s. That runs
// inside a builtin where the VM's step budget cannot interrupt it, so an embedded
// host would hang on a one-line script. Capped and raised instead.
func TestTableInsert_ShiftSpanCapped(t *testing.T) {
	for _, src := range []string{
		`local ok,e = pcall(table.insert,{"a"},2147483648,"X") return tostring(ok)..","..tostring(e)`,
		`local ok,e = pcall(table.insert,{"a"},-2147483648,"X") return tostring(ok)..","..tostring(e)`,
	} {
		got := testutil.RunOne(t, src)
		if !got.IsString() || got.Str() == "true,nil" {
			t.Errorf("%s = %v, want a raised error rather than an unbounded shift", src, got.Display())
		}
	}
	// An ordinary insert into a LARGE table must still work: the cap bounds the
	// distance below index 1, not the number of elements. Measuring the element
	// count rejected this, which both engines complete quickly.
	if got := testutil.RunOne(t, `local t={} for i=1,100000 do t[i]=1 end table.insert(t,1,"X") return #t..","..t[1]`); !got.IsString() || got.Str() != "100001,X" {
		t.Errorf("insert at 1 into a 100k table should succeed, got %v", got.Display())
	}
	// a span just under the cap still works
	if got := testutil.RunOne(t, `local t={} table.insert(t,-1000,"X") return tostring(t[-1000])`); !got.IsString() || got.Str() != "X" {
		t.Errorf("small negative position should still insert, got %v", got.Display())
	}
}

// TestTableConcat_ErrorTextMatchesPUC pins PUC addfield's exact message:
// "invalid value (%s) at index %d in table for 'concat'", carrying
// luaL_typename of the offending element. wangshu had the parenthesis around the
// wrong span and omitted the type name, so the text differed from the oracle's
// for every non-string element.
func TestTableConcat_ErrorTextMatchesPUC(t *testing.T) {
	for _, tc := range []struct{ src, want string }{
		{`local ok,e = pcall(table.concat,{1},",",0) return e`,
			"invalid value (nil) at index 0 in table for 'concat'"},
		{`local ok,e = pcall(table.concat,{1,{},3},",") return e`,
			"invalid value (table) at index 2 in table for 'concat'"},
		{`local ok,e = pcall(table.concat,{1,true},",") return e`,
			"invalid value (boolean) at index 2 in table for 'concat'"},
	} {
		got := testutil.RunOne(t, tc.src)
		if !got.IsString() || got.Str() != tc.want {
			t.Errorf("%s\n  got  %v\n  want %q", tc.src, got.Display(), tc.want)
		}
	}
}
