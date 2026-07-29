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

// TestConcatBudget pins that table.concat is charged to the same budget.
//
// It is the fourth member of this family, and the one I found by sweeping rather than by waiting
// for an audit: every stdlib call that can move or build O(n) data inside one uninterruptible C
// call is a candidate, since the instruction hook cannot interrupt any of them. 2000 concats over
// a 200000-element table cost 28 seconds across the two engines and compared normally.
//
// Also swept and found cheap at scale: string.rep, string.gsub, string.upper, string.sub,
// table.sort, table constructors and coroutine churn -- all under 300ms at sizes where concat
// took tens of seconds, because they either allocate once or the instruction hook does see them.
func TestConcatBudget(t *testing.T) {
	pre := Prelude(testKeep)

	loop := Exec(`t={} for i=1,200000 do t[i]="xxxxxxxx" end for k=1,2000 do table.concat(t) end print(1)`, pre, Limits{})
	if !strings.Contains(loop.Err, LimitSentinel) {
		t.Errorf("a loop of large concats was COMPARED (err=%q)", loop.Err)
	}

	// Ordinary concats must still be compared, including the empty table, an explicit range and
	// the error case for a non-string element.
	for _, src := range []string{
		`print(table.concat({1,2,3},","))`,
		`print(table.concat({},","))`,
		`print(table.concat({1,2,3},",",2,3))`,
		`local ok,e=pcall(table.concat,{1,{},3},",") print(ok,e)`,
		`t={} for i=1,1000 do t[i]="x" end for k=1,100 do table.concat(t) end print(1)`,
	} {
		r := Exec(src, pre, Limits{})
		if strings.Contains(r.Err, LimitSentinel) {
			t.Errorf("an ordinary concat was SKIPPED (err=%q) for %s", r.Err, src)
		}
	}
}

// TestBulkBudgetCoversTheFamily pins the whole bulk-work family in one place, in BYTES.
//
// Four audit rounds and one self-sweep found members one at a time, and a fifth round found three
// more: my sweep had timed SINGLE calls, where every one of these is milliseconds, so it concluded
// they were cheap. Loop form is the shape that matters -- a script can repeat one C call thousands
// of times inside the instruction budget, and the hook cannot interrupt any of them.
//
// The unit is bytes because counting elements undercharges anything holding large strings: 64
// concat pieces of 64 KiB each cost 9 seconds while charging 64. table.sort is charged n*log2(n),
// since charging n left 100 sorts of 200000 elements burning 4 seconds.
//
// string.gsub is deliberately absent: __patcheck already caps pattern subjects at 256 bytes, so
// every loop form of it is excluded at 2ms by that guard and a bulk charge there would be dead
// code. An audit reported gsub as a gap; measuring showed the existing guard already covers it.
func TestBulkBudgetCoversTheFamily(t *testing.T) {
	pre := Prelude(testKeep)
	for _, tc := range []struct{ name, src string }{
		{"string.rep in a loop",
			`for i=1,1000 do local x=#string.rep("x",4194304) end print(1)`},
		{"string.upper in a loop",
			`local s=string.rep("a",4194304) for i=1,1000 do s:upper() end print(1)`},
		{"table.sort in a loop",
			`t={} for i=1,200000 do t[i]=i%7 end for k=1,100 do table.sort(t) end print(1)`},
		{"concat of large pieces",
			`t={} for i=1,64 do t[i]=string.rep("y",65536) end for k=1,2000 do table.concat(t) end print(1)`},
		{"string.sub with a wrapped index in a loop",
			`local s=string.rep("a",1048576) for k=1,20000 do s:sub(4294967297) end print(1)`},
		{"string.sub extracting a large slice in a loop",
			`local s=string.rep("a",1048576) for k=1,20000 do s:sub(1,1048576) end print(1)`},
		{"concat with a large separator",
			`t={} for i=1,1000 do t[i]="" end local d=string.rep("z",65536) for k=1,4000 do table.concat(t,d) end print(1)`},
	} {
		r := Exec(tc.src, pre, Limits{})
		if !strings.Contains(r.Err, LimitSentinel) {
			t.Errorf("%s was COMPARED (err=%q)", tc.name, r.Err)
		}
	}
	// A cheap range concat over a HUGE table must still be compared: the charge reads the
	// requested [i,j], not #t. Reading #t made this skip, which is the same mistake the insert
	// shim's own comment warns about.
	huge := Exec(`t={} for i=1,2000000 do t[i]="a" end print(table.concat(t,",",1,3))`, pre, Limits{})
	if strings.Contains(huge.Err, LimitSentinel) {
		t.Errorf("a 3-element range concat over a large table was SKIPPED (err=%q)", huge.Err)
	}
	// And ordinary uses of every shimmed function must still be compared.
	for _, src := range []string{
		`print(string.rep("ab",3))`,
		`print(("hello"):sub(2),("hello"):sub(-3,-2),("hello"):sub(3,1))`,
		// Both sub indices narrow to int32: sub(4294967297) is index 1 in lua5.1, giving "".
		`print(#("hello"):sub(4294967297),#("hello"):sub(1,4294967297))`,
		`print(("hello"):sub("2"),("hello"):sub(2.9))`,
		`local s="abcdef" for i=1,#s do io.write(s:sub(i,i)) end print("")`,
		`print(("hello"):upper(),("X"):lower(),("abc"):reverse())`,
		`print(("hello world"):gsub("o","0"))`,
		`t={3,1,2} table.sort(t) print(table.concat(t,","))`,
		`t={} for i=1,1000 do t[i]=i end table.sort(t) print(t[1],t[1000])`,
	} {
		r := Exec(src, pre, Limits{})
		if strings.Contains(r.Err, LimitSentinel) {
			t.Errorf("ordinary use was SKIPPED (err=%q) for %s", r.Err, src)
		}
	}
}

// TestBulkBudgetAppliesLuaCoercions pins that the budget normalises arguments the way the C
// functions do before charging.
//
// The shims first checked raw Lua types, which leaked in BOTH directions. Undercharged:
// string.rep(1, 4194304) builds 4 MiB per call and was charged nothing because the subject was a
// number, so a loop of it cost 4m45s while comparing normally; table.remove(t, "1") bypassed the
// shift budget for the same reason. Overcharged: string.sub(s, "1048576") was charged from index 1,
// and table.concat(t, ",", "1", "3") was charged the whole table, so cheap comparable inputs were
// silently excluded.
//
// luaL_checkinteger accepts a numeric string and luaL_checklstring accepts a number, so the charge
// has to apply the same coercions or it is measuring a different call than the one that runs.
func TestBulkBudgetAppliesLuaCoercions(t *testing.T) {
	pre := Prelude(testKeep)

	// Coerced arguments must still be CHARGED.
	for _, tc := range []struct{ name, src string }{
		{"numeric subject to string.rep",
			`for k=1,20000 do local x=#string.rep(1,4194304) end print(1)`},
		{"string position to table.remove",
			`t={} for i=1,40000 do t[i]=i end for k=1,20000 do table.remove(t,"1") end print(#t)`},
	} {
		r := Exec(tc.src, pre, Limits{})
		if !strings.Contains(r.Err, LimitSentinel) {
			t.Errorf("%s was COMPARED (err=%q)", tc.name, r.Err)
		}
	}

	// And coerced arguments must not cause a FALSE skip of cheap work.
	for _, src := range []string{
		`print(string.rep(1,3))`,
		`print(string.rep("a","3"))`,
		`print(("hello"):sub("2"),("hello"):sub("2","3"))`,
		`t={1,2,3} print(table.remove(t,"1"),#t)`,
		`print(table.concat({1,2,3},",","1","2"))`,
		`print(table.concat({1,2,3},2))`,
		`print(string.upper(123))`,
		// A 3-element range over a 2M-element table, with the range given as strings.
		`t={} for i=1,2000000 do t[i]="a" end print(table.concat(t,",","1","3"))`,
		// luaL_checkint narrows to int32, so this end index becomes 1 and lua5.1 answers
		// instantly. Without the narrowing the charge scanned ~4 billion indices until the
		// instruction budget tripped, turning a comparable input into a skip.
		`print(table.concat({"x"},"",1,4294967297))`,
		// And it truncates toward zero rather than rounding.
		`print(table.concat({"a","b","c"},"",1.9,2.9))`,
		// string.rep's count narrows the same way: 4294967297 repetitions is ONE in lua5.1.
		// Both the budget shim and the pre-existing "rep too large" guard needed it.
		`print(string.rep("x",4294967297))`,
		`print(#string.rep("ab",2.9))`,
		`print(string.rep("x",-4294967295))`,
	} {
		r := Exec(src, pre, Limits{})
		if strings.Contains(r.Err, LimitSentinel) {
			t.Errorf("a coerced-argument form was SKIPPED (err=%q) for %s", r.Err, src)
		}
	}
}

// TestConcatChargeDoesNotFireIndex pins that the byte-counting scan reads RAW, so wrapping
// table.concat does not change the program being measured.
//
// PUC's table.concat uses lua_rawgeti, so scanning with t[k] fired __index: a table with a raising
// __index reported that error instead of "invalid value (nil) at index 1", and the metamethod ran
// three times when it should not have run at all.
//
// The reason that is worse than an ordinary bug: the prelude wraps BOTH engines, so they agreed on
// the wrong answer and the differential comparison passed. A harness that changes the program
// symmetrically does not report a divergence, it HIDES one -- the same failure mode as a capture gap
// that both sides miss.
func TestConcatChargeDoesNotFireIndex(t *testing.T) {
	pre := Prelude(testKeep)

	// The __index metamethod must not run at all.
	r := Exec(`local n=0 local t=setmetatable({},{__index=function() n=n+1 return "x" end}) local ok=pcall(table.concat,t,",",1,3) print(n)`, pre, Limits{})
	if !strings.Contains(r.Output, "0") {
		t.Errorf("__index ran during the budget scan; output=%q err=%q", r.Output, r.Err)
	}

	// And the error must be concat's own, not one raised from inside a metamethod.
	r2 := Exec(`local t=setmetatable({},{__index=function() error("BOOM") end}) local ok,e=pcall(table.concat,t,",",1,3) print(e)`, pre, Limits{})
	if strings.Contains(r2.Output, "BOOM") {
		t.Errorf("the scan raised through __index instead of reporting concat's error: %q", r2.Output)
	}
	if !strings.Contains(r2.Output, "invalid value") {
		t.Errorf("expected concat's own invalid-value error, got %q", r2.Output)
	}
}

// TestConcatChargeStopsAtInvalidElement pins that the charge covers only what the real call touches.
//
// Charging the whole requested range's separators up front, and scanning past an element the real
// call would reject, excluded inputs lua5.1 answers immediately:
// table.concat({}, string.rep("x",65536), 1, 100) reports "invalid value (nil) at index 1" in
// microseconds, but the shim charged ~6.5 MiB of separators first and raised the limit sentinel --
// and a limit error reads as a skip, so the input left the comparison silently. An empty separator
// with a large j walked the whole invalid range until the instruction budget stopped it.
func TestConcatChargeStopsAtInvalidElement(t *testing.T) {
	pre := Prelude(testKeep)
	for _, src := range []string{
		`print(pcall(table.concat,{},string.rep("x",65536),1,100))`,
		`print(pcall(table.concat,{},"",1,3000000))`,
		`print(pcall(table.concat,{"a"},"",1,100))`,
	} {
		r := Exec(src, pre, Limits{})
		if strings.Contains(r.Err, LimitSentinel) {
			t.Errorf("an input the real call rejects at once was SKIPPED (err=%q) for %s", r.Err, src)
		}
		if !strings.Contains(r.Output, "invalid value") {
			t.Errorf("expected concat's invalid-value error, got %q for %s", r.Output, src)
		}
	}
}
