package wangshu_test

import "testing"

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
		got := runOne(t, tc.src)
		if !got.IsString() || got.Str() != tc.want {
			t.Errorf("%s: %s = %v, want %q", tc.name, tc.src, got.Display(), tc.want)
		}
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
		got := runOne(t, tc.src)
		if !got.IsString() || got.Str() != tc.want {
			t.Errorf("%s\n  got  %v\n  want %q", tc.src, got.Display(), tc.want)
		}
	}
}
