// table / os / io sub-libraries + base library completions (unpack/xpcall)
// (mandatory columns from the 10 trimmed-set table).
package stdlib

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/crescent"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// ----- table sub-library -----

var tableFns = []entry{
	{"insert", tableFnInsert},
	{"remove", tableFnRemove},
	{"concat", tableFnConcat},
	{"sort", tableFnSort},
	{"getn", tableFnGetn},
	{"maxn", tableFnMaxn},
	{"setn", tableFnSetn},
	{"foreach", tableFnForeach},   // LUA_COMPAT 5.0 legacy (bundled by default in official 5.1.5)
	{"foreachi", tableFnForeachi}, // same as above
}

// tableFnForeach: table.foreach(t, f) -- calls f(k, v) for each key/value
// pair; if f returns non-nil, stop and return that value (official ltablib
// foreach).
func tableFnForeach(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	tv, e := tblArg(args, 0, "foreach")
	if e != nil {
		return nil, e
	}
	if len(args) < 2 || value.Tag(args[1]) != value.TagFunction {
		return nil, crescent.NewArgError(2, "function expected")
	}
	t := value.GCRefOf(tv)
	key := value.Nil
	for {
		k, v, ok, err := st.RawNext(t, key)
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		rs, e := st.ProtectedCallDirect(args[1], []value.Value{k, v})
		if e != nil {
			return nil, e
		}
		if len(rs) > 0 && rs[0] != value.Nil {
			return []value.Value{rs[0]}, nil
		}
		key = k
	}
	return nil, nil
}

// tableFnForeachi: table.foreachi(t, f) -- calls f(i, t[i]) for 1..#t; the
// return-value semantics are the same as foreach.
func tableFnForeachi(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	tv, e := tblArg(args, 0, "foreachi")
	if e != nil {
		return nil, e
	}
	if len(args) < 2 || value.Tag(args[1]) != value.TagFunction {
		return nil, crescent.NewArgError(2, "function expected")
	}
	t := value.GCRefOf(tv)
	n := int(st.RawBorder(t))
	for i := 1; i <= n; i++ {
		v, _ := st.RawGet(t, value.NumberValue(float64(i)))
		rs, e := st.ProtectedCallDirect(args[1], []value.Value{value.NumberValue(float64(i)), v})
		if e != nil {
			return nil, e
		}
		if len(rs) > 0 && rs[0] != value.Nil {
			return []value.Value{rs[0]}, nil
		}
	}
	return nil, nil
}

// tableFnSetn: table.setn -- 5.1.5 empirically raises "'setn' is obsolete"
// directly (the 10 §11 △ column says "no-op", but oracle behavior takes
// priority: match the error wording to preserve the diff).
func tableFnSetn(_ *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	// PUC's setn runs luaL_checktype(L, 1, LUA_TTABLE) BEFORE reaching the
	// obsolete error, so a non-table first argument reports the type error
	// instead. Raising the obsolete message unconditionally diverged for every
	// such call, including table.setn() with no arguments at all.
	if len(args) == 0 || value.Tag(args[0]) != value.TagTable {
		return nil, crescent.NewArgError(1, "table expected, got "+argTypeName(args, 0))
	}
	return nil, crescent.NewError("'setn' is obsolete") // with position prefix (executeFrom annotation)
}

func tblArg(args []value.Value, n int, fname string) (value.Value, *crescent.LuaError) {
	if n >= len(args) || value.Tag(args[n]) != value.TagTable {
		// PUC's luaL_typerror is "%s expected, got %s" with luaL_typename of
		// the argument, and lua_typename maps LUA_TNONE -- an argument that was
		// not passed at all -- to the literal "no value". Omitting the ", got X"
		// clause diverged from the oracle for every such call, and
		// table.insert() with no arguments is one mutation away in fuzzing.
		got := "no value"
		if n < len(args) {
			got = crescent.TypeNameOf(args[n])
		}
		return value.Nil, crescent.NewArgError(n+1, "table expected, got "+got)
	}
	return args[n], nil
}

// tableInsertShiftCap bounds how far BELOW index 1 an insert position may sit.
//
// 5.1 places no bound on it, so a far-negative position turns into a
// multi-billion-iteration loop inside a builtin, where the step budget cannot
// reach. It deliberately does NOT bound the number of elements in the table:
// shifting a large real array is ordinary work both engines do quickly, and
// capping that rejected valid inserts.
// Sized from measurement, not guessed: at 2^22 real lua5.1 completes the shift in
// 56ms, at 2^24 in 0.21s, at 2^28 in 3.4s. 12 section 4.9's bar is an
// uninterruptible hang, so a cap that rejects 56ms of work is too tight -- it
// refuses inserts PUC finishes promptly. 2^22 and then 2^26 were both still too
// tight: lua5.1 completes the 2^26 shift in 5.5s, which is slow but not a hang,
// and the boundary case was being rejected. 2^27 keeps the accepted range to what
// the reference actually finishes while still cutting off the 2-billion-iteration
// shift that motivated the cap (INT32_MIN is 16x beyond it).
const tableInsertShiftCap = 1 << 27

// tableFnInsert: table.insert(t, [pos,] v).
func tableFnInsert(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	tv, e := tblArg(args, 0, "insert")
	if e != nil {
		return nil, e
	}
	t := value.GCRefOf(tv)
	n := int(st.RawBorder(t))
	switch len(args) {
	case 2:
		// append
		if e := st.RawSet(t, value.NumberValue(float64(n+1)), args[1]); e != nil {
			return nil, e
		}
	case 3:
		posF, ok := toNumberStr(st, args[1])
		if !ok {
			return nil, crescent.NewArgError(2, "number expected, got "+st.TypeName(args[1]))
		}
		// luaL_checkint is (int)luaL_checkinteger: double -> lua_Integer
		// (ptrdiff_t, 64-bit) -> int, so the position is narrowed to 32 bits.
		// Taking Go's 64-bit int instead had two consequences once the bounds
		// check came off: a position of 1/0 became INT64_MIN and the shift loop
		// ran ~2^63 iterations (PUC returns promptly; wangshu hung), and a large
		// finite position landed on the un-narrowed key, so
		// insert({1,2,3}, 2^32+2, "X") wrote t[4294967298] where PUC writes
		// t[4]. Reuse the same helper string.char uses for that cast.
		pos := int(cCharCast(posF))
		// NO bounds check: PUC 5.1's tinsert has none. The
		// "position out of bounds" error belongs to 5.2+, and rejecting
		// out-of-range positions here diverged from the oracle on every one of
		// them -- position 0, negatives, and anything past the end.
		//
		// 5.1's ltablib.c tinsert computes e = #t + 1, raises e to pos when pos
		// is larger ("grow the array if necessary"), shifts [pos, e-1] up by
		// one, then writes at pos. A pos at or below 0 shifts nothing, because
		// the loop runs from e down to pos+1 and e is already <= pos, so it just
		// writes. Verified against the oracle: t={} with pos 0 yields
		// t[0]=1 and #t==0; t={"a","b"} with pos 99 yields t[99]="z" and #t==2.
		e2 := n + 1
		if pos > e2 {
			e2 = pos
		}
		// Fail fast rather than shifting an unbounded span.
		//
		// 5.1 shifts [pos, e-1] up one with no bound on the distance, so a
		// position far below 1 makes the loop run |pos| times: pos = 2^31
		// narrows to INT32_MIN and the shift is ~2.1 billion rawget/rawset
		// pairs. The official build genuinely does this work -- measured at 2m21s
		// -- and it is not a semantic divergence, both engines compute the same
		// table. But it runs inside a builtin, so the VM's step budget cannot
		// interrupt it, and an embedded host would simply hang on a one-line
		// script. 12 section 4.9's hardening rule applies: cap the work and
		// raise, rather than inherit an unbounded loop from the reference.
		//
		// The cap is well above any real use (a shift that large cannot produce
		// a table anyone reads) and below the point where the loop stops being
		// interruptible in practice.
		// Bound only the part of the span that lies BELOW index 1.
		//
		// Measuring e2-pos, the whole element count, was wrong: it rejected
		// ordinary inserts into large tables -- insert(t, 1, "X") on a 4.3M
		// element table raised, where both PUC and wangshu complete the shift in
		// well under a second. The hazard is not "many elements", it is a
		// position far below the array, which 5.1 turns into |pos| iterations
		// over keys that hold nothing.
		if pos < 1 && 1-pos > tableInsertShiftCap {
			return nil, crescent.NewArgError(2, "position out of bounds")
		}
		for i := e2; i > pos; i-- {
			v, _ := st.RawGet(t, value.NumberValue(float64(i-1)))
			if e := st.RawSet(t, value.NumberValue(float64(i)), v); e != nil {
				return nil, e
			}
		}
		if e := st.RawSet(t, value.NumberValue(float64(pos)), args[2]); e != nil {
			return nil, e
		}
	default:
		return nil, crescent.NewError("wrong number of arguments to 'insert'")
	}
	return nil, nil
}

// tableFnRemove: table.remove(t [, pos]) → the removed value.
func tableFnRemove(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	tv, e := tblArg(args, 0, "remove")
	if e != nil {
		return nil, e
	}
	t := value.GCRefOf(tv)
	n := int(st.RawBorder(t))
	pos := n
	// An explicit nil means "absent", because PUC reads the position with
	// luaL_optint: table.remove(t, nil) removes the last element rather than
	// raising. Treating a present nil as a bad argument diverged.
	if len(args) >= 2 && args[1] != value.Nil {
		posF, ok := toNumberStr(st, args[1])
		if !ok {
			return nil, crescent.NewArgError(2, "number expected, got "+st.TypeName(args[1]))
		}
		pos = int(cCharCastInt32(posF)) // luaL_checkint narrowing, as in insert
	}
	// Out-of-range pos (including an empty table): return 0 values and leave
	// the table untouched (official tremove `!(1 <= pos && pos <= e)
	// return 0`). The old implementation lacked this check: the shift loop
	// did not run but t[n]=nil still executed -- silently deleting the last
	// element (data corruption).
	if pos < 1 || pos > n {
		return nil, nil
	}
	removed, _ := st.RawGet(t, value.NumberValue(float64(pos)))
	// shift left [pos+1, n] → [pos, n-1]
	for i := pos; i < n; i++ {
		v, _ := st.RawGet(t, value.NumberValue(float64(i+1)))
		if e := st.RawSet(t, value.NumberValue(float64(i)), v); e != nil {
			return nil, e
		}
	}
	if e := st.RawSet(t, value.NumberValue(float64(n)), value.Nil); e != nil {
		return nil, e
	}
	return []value.Value{removed}, nil
}

// tableFnConcat: table.concat(t [, sep [, i [, j]]]).
func tableFnConcat(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	// PUC's tconcat reads the separator with luaL_optlstring(L, 2, ...) BEFORE
	// luaL_checktype(L, 1, LUA_TTABLE), so when both arguments are wrong the
	// error names #2, not #1: table.concat("", {}) reports
	// "bad argument #2 (string expected, got table)". Checking the table first
	// reported #1 instead. A fuzz smoke found this on its own.
	sep := ""
	if len(args) >= 2 && args[1] != value.Nil {
		sb, e := strArg(st, args, 1, "concat")
		if e != nil {
			return nil, e
		}
		sep = string(sb)
	}
	tv, e := tblArg(args, 0, "concat")
	if e != nil {
		return nil, e
	}
	t := value.GCRefOf(tv)
	// The ok flags must be checked: a non-numeric i or j is an argument error in
	// PUC (luaL_optint), not a silent fall back to the default. Discarding them
	// made table.concat({1,2,3}, ",", {}) report a bogus index error, and a bad j
	// succeed outright. The sibling callers in stringlib and unpack do check.
	iF, iOK := numArg(st, args, 2, 1)
	if !iOK {
		return nil, crescent.NewArgError(3, "number expected, got "+st.TypeName(args[2]))
	}
	jF, jOK := numArg(st, args, 3, float64(st.RawBorder(t)))
	if !jOK {
		return nil, crescent.NewArgError(4, "number expected, got "+st.TypeName(args[3]))
	}
	// NaN normalization: NaN-X=NaN and NaN>x is always false would bypass
	// the range check below; also Go int(NaN)=MIN_INT64 disagrees with PUC
	// 5.1.5 int(NaN)=0 (a diff divergence). Uniformly treat NaN as 0 (match
	// PUC luaL_checkint's NaN→0) so an out-of-range index takes the normal
	// "invalid value (nil) at index" path rather than implementation-defined
	// behavior.
	if iF != iF {
		iF = 0
	}
	if jF != jF {
		jF = 0
	}
	// Embedded hardening: j is script-controlled, so extreme values like
	// 1e14 make the parts-append loop exhaust host memory. table.concat's
	// real engineering semantics only make sense within the table's # range;
	// 1<<24 (~16M) is the hardening cap -- anything beyond the table length
	// is meaningless (indexes nil), so cut the loop off at the start while
	// the actual work loop is still bounded by the number of table elements.
	// The bound is on the WALK, applied inside the loop, not on j up front.
	//
	// The walk stops at the first element that is not a string or number, and a
	// table has nil at border+1, so the cost is min(j, border+1) - i -- never
	// j - i. A cap on j therefore rejected shapes PUC finishes instantly:
	// concat({"a","b"}, ",", 1, 1e14) reports "invalid value (nil) at index 3"
	// on both engines in microseconds, while the cap turned that into "range too
	// large". Two earlier attempts at keeping the cap failed for the same reason
	// -- comparing raw floats, then requiring only that t[i] be concatenable --
	// because neither bounded the WALK, which was already bounded by the data.
	//
	// There is no bound here at all, and none is needed: parts grows only while
	// elements are concatenable, so its length cannot exceed the table's own
	// contiguous run. An earlier revision of this comment claimed a bound "is
	// kept" -- it was removed with the cap, and the comment was left behind.
	iN := int(cCharCastInt32(iF))
	jN := int(cCharCastInt32(jF))
	var parts []string
	// luaL_checkint narrowing on both bounds: PUC reads them with
	// luaL_optint, so 2^32+2 is 2, not an out-of-range index.
	for k := iN; k <= jN; k++ {
		v, _ := st.RawGet(t, value.NumberValue(float64(k)))
		if value.IsNumber(v) {
			parts = append(parts, crescent.FormatLuaNumber(value.AsNumber(v)))
		} else if value.Tag(v) == value.TagString {
			parts = append(parts, string(object.StringBytes(st.Arena(), value.GCRefOf(v))))
		} else {
			// PUC's addfield: "invalid value (%s) at index %d in table for
			// 'concat'", where %s is luaL_typename of the offending element.
			// wangshu had the parenthesis around the wrong span and omitted the
			// type name entirely, so the message differed from the oracle's on
			// every non-string element.
			return nil, crescent.NewError(fmt.Sprintf(
				"invalid value (%s) at index %d in table for 'concat'", st.TypeName(v), k))
		}
	}
	return []value.Value{intern(st, strings.Join(parts, sep))}, nil
}

// tableFnSort: table.sort(t [, comp]). comp is a Lua function (called back via ProtectedCallDirect).
func tableFnSort(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	tv, e := tblArg(args, 0, "sort")
	if e != nil {
		return nil, e
	}
	t := value.GCRefOf(tv)
	// PUC sort: a present non-nil comparator must be a function
	// (luaL_checktype after !lua_isnoneornil); a non-function comp was
	// silently ignored here (oracle diff fuzz catch: table:sort(0)).
	if len(args) >= 2 && args[1] != value.Nil && value.Tag(args[1]) != value.TagFunction {
		return nil, crescent.NewArgError(2, fmt.Sprintf("function expected, got %s", st.TypeName(args[1])))
	}
	n := int(st.RawBorder(t))
	vals := make([]value.Value, n)
	for i := 0; i < n; i++ {
		vals[i], _ = st.RawGet(t, value.NumberValue(float64(i+1)))
	}
	var sortErr *crescent.LuaError
	less := func(a, b value.Value) bool {
		if sortErr != nil {
			return false
		}
		if len(args) >= 2 && value.Tag(args[1]) == value.TagFunction {
			rs, e := st.ProtectedCallDirect(args[1], []value.Value{a, b})
			if e != nil {
				sortErr = e
				return false
			}
			return len(rs) > 0 && value.Truthy(rs[0])
		}
		// Default comparison uses the full `<` semantics (number/string fast
		// path + __lt metamethod; official sort_comp goes through
		// lua_lessthan, so object tables with __lt can be sorted directly)
		r, e := st.LessThan(a, b)
		if e != nil {
			sortErr = e
			return false
		}
		return r
	}
	sort.SliceStable(vals, func(i, j int) bool { return less(vals[i], vals[j]) })
	if sortErr != nil {
		return nil, sortErr
	}
	for i := 0; i < n; i++ {
		if e := st.RawSet(t, value.NumberValue(float64(i+1)), vals[i]); e != nil {
			return nil, e
		}
	}
	return nil, nil
}

// tableFnGetn: table.getn(t) (5.1 legacy, = #t).
func tableFnGetn(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	tv, e := tblArg(args, 0, "getn")
	if e != nil {
		return nil, e
	}
	return []value.Value{value.NumberValue(float64(st.RawBorder(value.GCRefOf(tv))))}, nil
}

// tableFnMaxn: table.maxn(t) = the largest positive numeric key (scans the whole table, 5.1).
func tableFnMaxn(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	tv, e := tblArg(args, 0, "maxn")
	if e != nil {
		return nil, e
	}
	t := value.GCRefOf(tv)
	maxn := 0.0
	key := value.Nil
	for {
		k, _, ok, err := st.RawNext(t, key)
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		if value.IsNumber(k) {
			if f := value.AsNumber(k); f > maxn {
				maxn = f
			}
		}
		key = k
	}
	return []value.Value{value.NumberValue(maxn)}, nil
}

// ----- os / io minimal set -----

var osFns = []entry{
	{"time", osFnTime},
	{"clock", osFnClock},
	{"date", osFnDate},
	{"getenv", osFnGetenv},
	{"difftime", osFnDifftime},
}

// osFnDifftime: os.difftime(t2, t1) = t2 - t1 (POSIX seconds, 5.1).
func osFnDifftime(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) < 1 {
		return nil, crescent.NewArgError(1, "number expected, got no value")
	}
	t2, ok := toNumberStr(st, args[0])
	if !ok {
		return nil, crescent.NewArgError(1, "number expected, got "+st.TypeName(args[0]))
	}
	t1 := 0.0
	if len(args) >= 2 && args[1] != value.Nil {
		var ok2 bool
		t1, ok2 = toNumberStr(st, args[1])
		if !ok2 {
			return nil, crescent.NewArgError(2, "number expected, got "+st.TypeName(args[1]))
		}
	}
	return []value.Value{value.NumberValue(t2 - t1)}, nil
}

// zoneDSTOffsets reports the standard and daylight offsets in effect around t,
// found by scanning the surrounding year for an instant whose DST state differs.
//
// ok is false when the zone has only one state there, in which case os.time leaves
// isdst alone -- see the comment at the call site for why no default shift is
// applied. Keyed on IsDST rather than on offset magnitude so that a permanently-DST
// zone and a permanent offset change are both classified correctly.
func zoneDSTOffsets(t time.Time) (std, dst int, ok bool) {
	_, base := t.Zone()
	baseDST := t.IsDST()
	// Probe OUTWARD from t, nearest first, because that is the direction glibc's
	// mktime searches. Scanning forward from January 1 instead picked the wrong
	// neighbour whenever the offsets changed within the year: America/Vancouver 2026
	// switches to MST (-25200) on Nov 1, the same magnitude as PDT, so glibc's
	// nearest non-DST neighbour is -25200 while a January-first scan found PST
	// (-28800). A full-tzdata sweep put that class at 65 cases across 55 zones.
	// glibc's mktime searches in 601200-second strides (about 6.96 days), probing
	// backward then forward at each stride, so it can step PAST a nearer transition
	// and settle on a further zone entry. A one-day scan finds the nearest one
	// instead, which is a different offset whenever a transition sits inside one
	// stride: Asia/Anadyr 2010 was an hour off in a genuinely two-state year. The
	// stride and probe order are copied rather than approximated.
	const strideSec = 601200
	// The bound is 381 strides, bracketed empirically against a C mktime reference.
	//
	// This constant and the equal-offset delta below are coupled, which is why earlier
	// rounds could not settle either alone: with the delta wrong, narrowing the bound
	// measured as neutral-to-worse and looked like a bad idea; with the delta right, it
	// takes the last 4 mismatches to 0. Do not change one without re-measuring the full
	// sweep.
	const maxStrides = 381
	for i := int64(1); i <= maxStrides; i++ {
		for _, off := range [2]int64{-i * strideSec, i * strideSec} {
			cand := t.Add(time.Duration(off) * time.Second)
			if cand.IsDST() != baseDST {
				_, other := cand.Zone()
				// A neighbour whose offset EQUALS the base one is still accepted: it
				// yields std == dst, which the fallback below turns into the default
				// hour. Rejecting it and continuing the search looked right in
				// isolation but broke Asia/Anadyr, America/Vancouver and
				// America/Miquelon, which rely on the first such neighbour.
				if other == base {
					// Usable neighbour with the same offset: glibc's delta for it is
					// ZERO, so isdst has no effect. Returning the default hour here was
					// the sole cause of the 1% residual.
					return base, base, true
				}
				if baseDST {
					return other, base, true
				}
				return base, other, true
			}
		}
	}
	// No usable neighbour: glibc falls back to a DEFAULT one-hour delta rather than
	// treating the field as a no-op. Returning ok=false here left isdst=true unshifted,
	// which a 4400-case sweep against a C mktime reference showed was the DOMINANT
	// failure -- 1108 of 1120 mismatches, every one of them isdst=true and off by
	// exactly one hour.
	//
	// Round 2 of this work tried an unconditional +3600 and regressed six zones, but
	// that was with the neighbour detection still wrong; with the search correct the
	// fallback is what the reference does.
	// The fallback must know WHICH of the two the base offset is. When the requested
	// instant is itself DST, base is the DAYLIGHT offset, so standard is an hour below
	// it -- returning (base, base+3600) put both an hour late. Go's tzdata extend
	// string collapses Africa/Casablanca to permanent +01/isdst=true past ~2088, which
	// is the shape that exposes it, and no sampled year reached that far.
	if baseDST {
		return base - 3600, base, true
	}
	return base, base + 3600, true
}

// getBoolField reads a boolean field, reporting whether it was present at all --
// PUC's getboolfield treats an absent field as false, but os.time needs to
// distinguish "absent" from "explicitly false".
func getBoolField(st *crescent.State, t arena.GCRef, key string) (bool, bool) {
	v, _ := st.RawGet(t, intern(st, key))
	if v == value.Nil {
		return false, false
	}
	return value.Truthy(v), true
}

func osFnTime(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	// PUC os_time: no arg / nil -> current time; anything else must be
	// a table (luaL_checktype) whose day/month/year fields are
	// mandatory (getfield with d<0 raises "field 'X' missing in date
	// table"); sec/min default 0, hour defaults 12.
	if len(args) == 0 || args[0] == value.Nil {
		return []value.Value{value.NumberValue(float64(time.Now().Unix()))}, nil
	}
	if value.Tag(args[0]) != value.TagTable {
		return nil, crescent.NewArgError(1, "table expected, got "+st.TypeName(args[0]))
	}
	t := value.GCRefOf(args[0])
	getfield := func(key string, def int) (int, *crescent.LuaError) {
		v, _ := st.RawGet(t, intern(st, key))
		if value.IsNumber(v) {
			// os.time's getfield uses (int)lua_tointeger, same two-step narrowing.
			return int(cCharCastInt32(value.AsNumber(v))), nil
		}
		if def < 0 {
			return 0, crescent.NewError("field '" + key + "' missing in date table")
		}
		return def, nil
	}
	sec, e := getfield("sec", 0)
	if e != nil {
		return nil, e
	}
	minute, e := getfield("min", 0)
	if e != nil {
		return nil, e
	}
	hour, e := getfield("hour", 12)
	if e != nil {
		return nil, e
	}
	day, e := getfield("day", -1)
	if e != nil {
		return nil, e
	}
	month, e := getfield("month", -1)
	if e != nil {
		return nil, e
	}
	year, e := getfield("year", -1)
	if e != nil {
		return nil, e
	}
	// mktime semantics: local time, out-of-range fields normalize.
	tt := time.Date(year, time.Month(month), day, hour, minute, sec, 0, time.Local)
	// isdst selects WHICH offset to interpret the fields with, as mktime's tm_isdst
	// does, and the epoch second is computed from that offset rather than by adjusting
	// Go's own answer -- adjusting was wrong in the spring-forward gap, where the local
	// time does not exist and Go normalizes it forward while mktime resolves it the
	// other way.
	//
	// The two offsets come from zoneDSTOffsets, which copies glibc's search. Verified
	// against a C mktime reference at zero mismatches over 598 zones x nine years from
	// 1850 to 2199 (21528 cases), plus a 6600-case independent holdout. There is no
	// exemption for this any more; an earlier revision had one and it was deleted when
	// the difference went away.
	if want, present := getBoolField(st, t, "isdst"); present {
		if stdOff, dstOff, ok := zoneDSTOffsets(tt); ok {
			off := stdOff
			if want {
				off = dstOff
			}
			// The same field values interpreted at a FIXED offset: read them as UTC
			// and subtract it. Adjusting time.Date's own answer instead depended on
			// how Go resolved a local time that may not exist (the spring-forward
			// gap, where Go normalizes forward and mktime resolves the other way).
			utc := time.Date(year, time.Month(month), day, hour, minute, sec, 0, time.UTC)
			tt = time.Unix(utc.Unix()-int64(off), 0)
		}
	}
	return []value.Value{value.NumberValue(float64(tt.Unix()))}, nil
}

var processStart = time.Now()

func osFnClock(_ *crescent.State, _ []value.Value) ([]value.Value, *crescent.LuaError) {
	return []value.Value{value.NumberValue(time.Since(processStart).Seconds())}, nil
}

func osFnDate(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	format := "%c"
	if len(args) >= 1 && args[0] != value.Nil {
		fb, e := strArg(st, args, 0, "date")
		if e != nil {
			return nil, e
		}
		format = string(fb)
	}
	// PUC os_date: the second argument goes through luaL_checknumber
	// when present -- non-numbers raise (oracle diff arg sweep catch).
	now := time.Now()
	if len(args) >= 2 && args[1] != value.Nil {
		f, ok := toNumberStr(st, args[1])
		if !ok {
			return nil, crescent.NewArgError(2, "number expected, got "+st.TypeName(args[1]))
		}
		now = time.Unix(int64(f), 0)
	}
	// A leading '!' selects UTC (PUC checks for it before anything else).
	utc := false
	if strings.HasPrefix(format, "!") {
		format = format[1:]
		now = now.UTC()
		utc = true
	}
	// "*t" (and "!*t") returns a TABLE rather than a formatted string.
	if format == "*t" {
		t := st.NewLibTable(9)
		set := func(k string, v value.Value) { st.SetTableField(t, k, v) }
		set("year", value.NumberValue(float64(now.Year())))
		set("month", value.NumberValue(float64(int(now.Month()))))
		set("day", value.NumberValue(float64(now.Day())))
		set("hour", value.NumberValue(float64(now.Hour())))
		set("min", value.NumberValue(float64(now.Minute())))
		set("sec", value.NumberValue(float64(now.Second())))
		// wday is 1-based with Sunday=1; yday is 1-based.
		set("wday", value.NumberValue(float64(int(now.Weekday())+1)))
		set("yday", value.NumberValue(float64(now.YearDay())))
		// isdst from the zone offset: a time whose local offset differs from its
		// zone's standard offset is in DST. Hardcoding false was wrong in any zone
		// that observes it.
		if isDST(now) {
			set("isdst", value.True)
		} else {
			set("isdst", value.False)
		}
		return []value.Value{value.MakeGC(value.TagTable, t)}, nil
	}
	return []value.Value{intern(st, strftime(format, now, utc))}, nil
}

// isDST reports whether t's zone is observing daylight saving at that instant.
//
// Delegates to time.Time.IsDST rather than comparing the January and July offsets:
// that min-of-two heuristic reports false for a permanently-DST zone such as
// Africa/Casablanca, where glibc reports true.
func isDST(t time.Time) bool { return t.IsDST() }

// strftime renders the C strftime directives PUC's os.date forwards, matching
// glibc's output for each.
//
// The set was previously six directives applied with a strings.Replacer, so
// anything else passed through literally -- os.date("%j") returned "%j". These are
// the directives a Lua script realistically uses; an unknown one is left as-is,
// which is what glibc does for an undefined conversion.
func strftime(format string, t time.Time, utc bool) string {
	var b strings.Builder
	for i := 0; i < len(format); i++ {
		if format[i] != '%' || i+1 >= len(format) {
			b.WriteByte(format[i])
			continue
		}
		i++
		switch format[i] {
		case 'Y':
			fmt.Fprintf(&b, "%04d", t.Year())
		case 'y':
			fmt.Fprintf(&b, "%02d", t.Year()%100)
		case 'm':
			fmt.Fprintf(&b, "%02d", int(t.Month()))
		case 'd':
			fmt.Fprintf(&b, "%02d", t.Day())
		case 'e':
			fmt.Fprintf(&b, "%2d", t.Day())
		case 'H':
			fmt.Fprintf(&b, "%02d", t.Hour())
		case 'M':
			fmt.Fprintf(&b, "%02d", t.Minute())
		case 'S':
			fmt.Fprintf(&b, "%02d", t.Second())
		case 'I':
			h := t.Hour() % 12
			if h == 0 {
				h = 12
			}
			fmt.Fprintf(&b, "%02d", h)
		case 'p':
			if t.Hour() < 12 {
				b.WriteString("AM")
			} else {
				b.WriteString("PM")
			}
		case 'j':
			fmt.Fprintf(&b, "%03d", t.YearDay())
		case 'a':
			b.WriteString(t.Format("Mon"))
		case 'A':
			b.WriteString(t.Weekday().String())
		case 'b', 'h':
			b.WriteString(t.Format("Jan"))
		case 'B':
			b.WriteString(t.Month().String())
		case 'c':
			b.WriteString(t.Format("Mon Jan _2 15:04:05 2006"))
		case 'x':
			b.WriteString(t.Format("01/02/06"))
		case 'X':
			b.WriteString(t.Format("15:04:05"))
		case 'Z':
			// glibc names the zone "GMT" only when it was reached through a '!'
			// format; under TZ=UTC the LOCAL zone is still "UTC". Both an
			// unconditional rewrite and no rewrite at all get one of the two cases
			// wrong, so the '!' is threaded in rather than inferred from the zone.
			if utc {
				b.WriteString("GMT")
			} else {
				b.WriteString(t.Format("MST"))
			}
		case 'F':
			fmt.Fprintf(&b, "%04d-%02d-%02d", t.Year(), int(t.Month()), t.Day())
		case 'T':
			fmt.Fprintf(&b, "%02d:%02d:%02d", t.Hour(), t.Minute(), t.Second())
		case 'D':
			b.WriteString(t.Format("01/02/06"))
		case 'R':
			fmt.Fprintf(&b, "%02d:%02d", t.Hour(), t.Minute())
		case 'r':
			h := t.Hour() % 12
			if h == 0 {
				h = 12
			}
			ap := "AM"
			if t.Hour() >= 12 {
				ap = "PM"
			}
			fmt.Fprintf(&b, "%02d:%02d:%02d %s", h, t.Minute(), t.Second(), ap)
		case 'C':
			fmt.Fprintf(&b, "%02d", t.Year()/100)
		case 'u':
			// ISO weekday, Monday=1..Sunday=7 (unlike %w's Sunday=0).
			wd := int(t.Weekday())
			if wd == 0 {
				wd = 7
			}
			fmt.Fprintf(&b, "%d", wd)
		case 'z':
			b.WriteString(t.Format("-0700"))
		case 's':
			// The real epoch second, deliberately NOT glibc's value under a '!'
			// format. glibc's %s calls mktime on the already-UTC-converted tm, so
			// it re-reads those fields as LOCAL time: os.date("!%s", 0) is -28800
			// under TZ=+8 and 0 under TZ=UTC, i.e. the answer depends on the zone
			// for a value that has none. Same category as print's NUL truncation --
			// an artifact of the C call rather than a semantic worth copying.
			fmt.Fprintf(&b, "%d", t.Unix())
		case 'g':
			// ISO week-based year, two digits (pairs with %V, not with %y).
			yr, _ := t.ISOWeek()
			fmt.Fprintf(&b, "%02d", yr%100)
		case 'G':
			yr, _ := t.ISOWeek()
			fmt.Fprintf(&b, "%d", yr)
		case 'k':
			fmt.Fprintf(&b, "%2d", t.Hour())
		case 'l':
			h := t.Hour() % 12
			if h == 0 {
				h = 12
			}
			fmt.Fprintf(&b, "%2d", h)
		case 'P':
			if t.Hour() < 12 {
				b.WriteString("am")
			} else {
				b.WriteString("pm")
			}
		case 'V':
			_, wk := t.ISOWeek()
			fmt.Fprintf(&b, "%02d", wk)
		case 'U':
			// Week of year, Sunday as the first day.
			fmt.Fprintf(&b, "%02d", (t.YearDay()+6-int(t.Weekday()))/7)
		case 'W':
			// Week of year, Monday as the first day.
			wd := (int(t.Weekday()) + 6) % 7
			fmt.Fprintf(&b, "%02d", (t.YearDay()+6-wd)/7)
		case 'w':
			fmt.Fprintf(&b, "%d", int(t.Weekday()))
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case '%':
			b.WriteByte('%')
		default:
			// Undefined conversion: emit it verbatim, as glibc does.
			b.WriteByte('%')
			b.WriteByte(format[i])
		}
	}
	return b.String()
}

func osFnGetenv(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	nb, e := strArg(st, args, 0, "getenv")
	if e != nil {
		return nil, e
	}
	v, ok := os.LookupEnv(string(nb))
	if !ok {
		return []value.Value{value.Nil}, nil
	}
	return []value.Value{intern(st, v)}, nil
}

var ioFns = []entry{
	{"write", ioFnWrite},
}

func ioFnWrite(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	for i := range args {
		b, e := strArg(st, args, i, "write")
		if e != nil {
			return nil, e
		}
		if _, werr := os.Stdout.Write(b); werr != nil {
			// PUC's g_write pushes false on a write failure rather than raising.
			return []value.Value{value.False}, nil
		}
	}
	// 5.1's g_write returns a BOOLEAN success flag -- `lua_pushboolean(L, status)`
	// -- not the file handle (that is 5.2+). Returning nothing at all made
	// type(io.write("")) raise "value expected".
	return []value.Value{value.True}, nil
}

// ----- math completions -----

var mathExtraFns = []entry{
	{"fmod", mathFnFmod},
	{"mod", mathFnFmod}, // LUA_COMPAT_MOD: 5.0 alias (bundled by default in official 5.1.5)
	{"modf", mathFnModf},
	{"atan2", mathFn2("atan2", atan2)},
	{"sinh", mathFn1("sinh", sinh)},
	{"cosh", mathFn1("cosh", cosh)},
	{"tanh", mathFn1("tanh", tanh)},
	{"frexp", mathFnFrexp},
	// ldexp reads the exponent with luaL_checkint, so it narrows to int32.
	{"ldexp", mathFn2("ldexp", func(m, e float64) float64 { return ldexp(m, int(cCharCastInt32(e))) })},
	{"pow", mathFn2("pow", func(a, b float64) float64 { return pow(a, b) })},
	{"random", mathFnRandom},
	{"randomseed", mathFnRandomSeed},
	{"atan", mathFn1("atan", atan)},
	{"asin", mathFn1("asin", asin)},
	{"acos", mathFn1("acos", acos)},
	{"deg", mathFn1("deg", deg)},
	{"rad", mathFn1("rad", rad)},
	{"log10", mathFn1("log10", log10)},
}

func mathFn2(name string, f func(a, b float64) float64) crescent.HostFn {
	return func(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
		if len(args) < 2 {
			// Left to right, i.e. the FIRST missing argument.
			//
			// This deliberately does NOT match the x86-64 oracle, which reports #2
			// because PUC writes f(luaL_checknumber(L,1), luaL_checknumber(L,2))
			// and gcc evaluates those arguments right to left there. C leaves that
			// order unspecified, and the arm64 build reports #1 -- the two official
			// builds disagree with each other, so there is nothing to align to.
			// An earlier revision of this branch did align to x86 and the arm64
			// oracle-smoke job rejected it.
			//
			// Reporting the first missing argument is the defensible choice, and the
			// harness skips the shape (see the arg-order guard in prelude.go).
			return nil, crescent.NewArgError(len(args)+1, "number expected, got no value")
		}
		// Left to right, for the same reason as the arity check above: the
		// order the oracle reports is its compiler's, not Lua's.
		a, ok1 := toNumberStr(st, args[0])
		if !ok1 {
			return nil, crescent.NewArgError(1, fmt.Sprintf("number expected, got %s", st.TypeName(args[0])))
		}
		b, ok2 := toNumberStr(st, args[1])
		if !ok2 {
			return nil, crescent.NewArgError(2, fmt.Sprintf("number expected, got %s", st.TypeName(args[1])))
		}
		return []value.Value{value.NumberValue(f(a, b))}, nil
	}
}

func mathFnFmod(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	return mathFn2("fmod", fmod)(st, args)
}

func mathFnFrexp(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) < 1 {
		return nil, crescent.NewArgError(1, "number expected, got no value")
	}
	x, ok := toNumberStr(st, args[0])
	if !ok {
		return nil, crescent.NewArgError(1, "number expected, got "+st.TypeName(args[0]))
	}
	m, e := frexp(x)
	return []value.Value{value.NumberValue(m), value.NumberValue(float64(e))}, nil
}

func mathFnModf(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) < 1 {
		return nil, crescent.NewArgError(1, "number expected, got no value")
	}
	x, ok := toNumberStr(st, args[0])
	if !ok {
		return nil, crescent.NewArgError(1, "number expected, got "+st.TypeName(args[0]))
	}
	ip, fp := modf(x)
	return []value.Value{value.NumberValue(ip), value.NumberValue(fp)}, nil
}

func mathFnRandom(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	switch len(args) {
	case 0:
		return []value.Value{value.NumberValue(rngFloat())}, nil
	case 1:
		mF, ok := toNumberStr(st, args[0])
		// luaL_checkint narrowing: math.random(2^32) is an EMPTY interval on PUC,
		// because the bound narrows to 0.
		m := float64(cCharCastInt32(mF))
		if !ok || m < 1 {
			return nil, crescent.NewArgError(1, "interval is empty")
		}
		return []value.Value{value.NumberValue(float64(rngInt(1, int64(m))))}, nil
	default:
		loF, ok1 := toNumberStr(st, args[0])
		hiF, ok2 := toNumberStr(st, args[1])
		// Both bounds go through luaL_checkint.
		lo, hi := float64(cCharCastInt32(loF)), float64(cCharCastInt32(hiF))
		if !ok1 || !ok2 || lo > hi {
			return nil, crescent.NewArgError(2, "interval is empty")
		}
		return []value.Value{value.NumberValue(float64(rngInt(int64(lo), int64(hi))))}, nil
	}
}

func mathFnRandomSeed(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	// PUC math_randomseed: luaL_checknumber -- the seed is mandatory
	// and non-numbers raise (oracle diff arg sweep catch).
	if len(args) == 0 {
		return nil, crescent.NewArgError(1, "number expected, got no value")
	}
	f, ok := toNumberStr(st, args[0])
	if !ok {
		return nil, crescent.NewArgError(1, "number expected, got "+st.TypeName(args[0]))
	}
	rngSeed(int64(f))
	return nil, nil
}

// ----- base completions: unpack / xpcall -----

// baseFnUnpackImpl: unpack(t [, i [, j]]).
// maxCStack mirrors official LUAI_MAXCSTACK (luaconf.h).
const maxCStack = 8000

func baseFnUnpackImpl(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	tv, e := tblArg(args, 0, "unpack")
	if e != nil {
		return nil, e
	}
	t := value.GCRefOf(tv)
	// PUC luaB_unpack: i/j go through luaL_optint/luaL_checkint --
	// nil defaults, but a present non-number argument raises (oracle
	// diff fuzz catch: unpack({}, false) errors in 5.1.5).
	iF, ok := numArg(st, args, 1, 1)
	if !ok {
		return nil, crescent.NewArgError(2, fmt.Sprintf("number expected, got %s", st.TypeName(args[1])))
	}
	jF, ok := numArg(st, args, 2, float64(st.RawBorder(t)))
	if !ok {
		return nil, crescent.NewArgError(3, fmt.Sprintf("number expected, got %s", st.TypeName(args[2])))
	}
	// PUC luaL_checkint is (int)luaL_checkinteger: a 64-bit hardware
	// float->int conversion truncated to 32 bits. NaN converts to
	// INT64_MIN on x86 (cvttsd2si), whose low 32 bits are 0 -- so
	// unpack({}, 0, 0/0) reads t[0] (one nil) instead of seeing an
	// empty range. Mirror the exact double-narrowing (oracle diff
	// fuzz catch: unpack({}, 0, 7%00)).
	i, j := int(int32(int64(iF))), int(int32(int64(jF)))
	if i > j {
		return nil, nil // empty range
	}
	// The bound is LUAI_MAXCSTACK MINUS the arguments already on the stack, not a
	// flat 8000.
	//
	// PUC's luaB_unpack asks lua_checkstack(L, n), which rejects when
	// `size > LUAI_MAXCSTACK || (L->top - L->base + size) > LUAI_MAXCSTACK`. For a C
	// function `L->top - L->base` is the argument count, so the real ceiling is
	// 8000 - nargs: unpack({0},1,7997) succeeds and 7998 raises, while a two-argument
	// call gets one more. Comparing against a flat 8000 accepted the 7998..8000 band
	// that PUC rejects.
	//
	// It also still prevents a 2^30-scale range from allocating a giant slice.
	n := j - i + 1
	if n <= 0 || n > maxCStack-len(args) {
		return nil, crescent.NewError("too many results to unpack")
	}
	out := make([]value.Value, 0, n)
	for k := i; k <= j; k++ {
		v, _ := st.RawGet(t, value.NumberValue(float64(k)))
		out = append(out, v)
	}
	return out, nil
}

// baseFnXpcall: xpcall(f, handler) → (true, results...) | (false, handler(err)).
//
// 09 semantics: the handler is called before the stack unwinds -- P1
// implements this as "call the handler immediately after catching" (the
// stack has already been rolled back by the protected boundary; P1 does not
// support inspecting the erroring stack frame inside the handler, which is a
// documented simplification, see implementation-progress).
func baseFnXpcall(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	if len(args) < 2 {
		return nil, crescent.NewArgError(2, "value expected")
	}
	fn, handler := args[0], args[1]
	results, e := st.ProtectedCall(fn, nil)
	if e == nil {
		out := make([]value.Value, 0, len(results)+1)
		out = append(out, value.True)
		out = append(out, results...)
		return out, nil
	}
	errVal := e.Value
	if !e.HasValue {
		errVal = intern(st, e.Msg)
	}
	hres, he := st.ProtectedCall(handler, []value.Value{errVal})
	if he != nil {
		return []value.Value{value.False, intern(st, "error in error handling")}, nil
	}
	out := make([]value.Value, 0, len(hres)+1)
	out = append(out, value.False)
	out = append(out, hres...)
	return out, nil
}
