// Pattern-related functions of the string library + format/byte/char
// (10 §7-§8).
package stdlib

import (
	"bytes"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/Liam0205/wangshu/internal/crescent"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// strArg fetches the n-th argument as string bytes (numbers coerce to strings,
// per Lua 5.1 behavior).
func strArg(st *crescent.State, args []value.Value, n int, fname string) ([]byte, *crescent.LuaError) {
	if n >= len(args) {
		return nil, crescent.NewArgError(n+1, "string expected, got no value")
	}
	v := args[n]
	if value.Tag(v) == value.TagString {
		return object.StringBytes(st.Arena(), value.GCRefOf(v)), nil
	}
	if value.IsNumber(v) {
		return []byte(crescent.FormatLuaNumber(value.AsNumber(v))), nil
	}
	return nil, crescent.NewArgError(n+1, "string expected, got "+st.TypeName(v))
}

// numArg fetches the n-th argument as a number (may be absent).
func numArg(st *crescent.State, args []value.Value, n int, def float64) (float64, bool) {
	if n >= len(args) || args[n] == value.Nil {
		return def, true
	}
	return toNumberStr(st, args[n])
}

// strInitPos reduces Lua's init argument (1-based, possibly negative) to a
// 0-based index.
func strInitPos(init float64, slen int) int {
	i := int(init)
	if i < 0 {
		i = slen + i + 1
	}
	if i < 1 {
		i = 1
	}
	return i - 1
}

// capsToValues materializes captures into Lua values; with no explicit
// captures it returns the whole matched string.
func capsToValues(st *crescent.State, src []byte, s, e int, caps []capResult) []value.Value {
	if len(caps) == 0 {
		return []value.Value{intern(st, string(src[s:e]))}
	}
	out := make([]value.Value, len(caps))
	for i, c := range caps {
		if c.pos {
			out[i] = value.NumberValue(float64(c.start + 1))
		} else {
			out[i] = intern(st, string(src[c.start:c.start+c.len]))
		}
	}
	return out
}

// stringFnFind: string.find(s, pat [, init [, plain]]).
func stringFnFind(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	s, e := strArg(st, args, 0, "find")
	if e != nil {
		return nil, e
	}
	pat, e := strArg(st, args, 1, "find")
	if e != nil {
		return nil, e
	}

	initF, ok := numArg(st, args, 2, 1)
	if !ok {
		return nil, crescent.NewArgError(3, "number expected, got "+st.TypeName(args[2]))
	}
	init := strInitPos(initF, len(s))
	// The charge happens AFTER the search, from the bytes actually examined -- see the
	// chargeScan calls below.
	//
	// Two wrong versions came first. Billing len(s) per call made the idiomatic advancing loop
	// `s:find(pat, pos)` pay for the whole subject on every step, and billing len(s)-init still
	// assumed the scan runs to the end. It does not: the search stops at the FIRST match, so a
	// loop over "abab..." examines about two bytes per call and the real total is O(n), which is
	// why lua5.1 finishes 256 KiB in 2ms. Only the result reveals how far the scan went.
	chargeScan := func(examined int) *crescent.LuaError {
		return st.ChargeBulkWork(examined + len(pat))
	}
	if init > len(s) {
		return []value.Value{value.Nil}, nil
	}
	// PUC str_find_aux fast path: plain search when explicitly
	// requested OR when the pattern contains no SPECIALS ("^$*+?.([%-").
	// Note ')' is NOT special -- find("", ")") plain-searches and
	// returns nil where match/gsub raise "invalid pattern capture"
	// (oracle diff fuzz catch).
	plain := (len(args) >= 4 && value.Truthy(args[3])) ||
		!strings.ContainsAny(string(pat), "^$*+?.([%-")
	if plain {
		// bytes.Index on the VIEWS: converting to string copied the whole subject on every
		// call, so a plain find on a 1 MiB string cost a megabyte per call and 300000 of them
		// ran 43 seconds while producing two integers. lua5.1 does it in 10ms.
		idx := bytes.Index(s[init:], pat)
		if idx < 0 {
			// A miss examined the whole remainder.
			if ce := chargeScan(len(s) - init); ce != nil {
				return nil, ce
			}
			return []value.Value{value.Nil}, nil
		}
		if ce := chargeScan(idx); ce != nil {
			return nil, ce
		}
		start := init + idx
		return []value.Value{
			value.NumberValue(float64(start + 1)),
			value.NumberValue(float64(start + len(pat))),
		}, nil
	}
	start, end, caps, found, err := patternFind(s, pat, init)
	if err == nil {
		examined := len(s) - init
		if found && start >= init {
			examined = start - init
		}
		if ce := chargeScan(examined); ce != nil {
			return nil, ce
		}
	}
	if err != nil {
		return nil, crescent.NewError(err.Error())
	}
	if !found {
		return []value.Value{value.Nil}, nil
	}
	out := []value.Value{
		value.NumberValue(float64(start + 1)),
		value.NumberValue(float64(end)),
	}
	if len(caps) > 0 {
		out = append(out, capsToValues(st, s, start, end, caps)...)
	}
	return out, nil
}

// stringFnMatch: string.match(s, pat [, init]).
func stringFnMatch(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	s, e := strArg(st, args, 0, "match")
	if e != nil {
		return nil, e
	}
	pat, e := strArg(st, args, 1, "match")
	if e != nil {
		return nil, e
	}

	initF, ok := numArg(st, args, 2, 1)
	if !ok {
		return nil, crescent.NewArgError(3, "number expected, got "+st.TypeName(args[2]))
	}
	init := strInitPos(initF, len(s))
	// The charge happens AFTER the search, from the bytes actually examined -- see the
	// chargeScan calls below.
	//
	// Two wrong versions came first. Billing len(s) per call made the idiomatic advancing loop
	// `s:find(pat, pos)` pay for the whole subject on every step, and billing len(s)-init still
	// assumed the scan runs to the end. It does not: the search stops at the FIRST match, so a
	// loop over "abab..." examines about two bytes per call and the real total is O(n), which is
	// why lua5.1 finishes 256 KiB in 2ms. Only the result reveals how far the scan went.
	chargeScan := func(examined int) *crescent.LuaError {
		return st.ChargeBulkWork(examined + len(pat))
	}
	if init > len(s) {
		return []value.Value{value.Nil}, nil
	}
	start, end, caps, found, err := patternFind(s, pat, init)
	if err == nil {
		examined := len(s) - init
		if found && start >= init {
			examined = start - init
		}
		if ce := chargeScan(examined); ce != nil {
			return nil, ce
		}
	}
	if err != nil {
		return nil, crescent.NewError(err.Error())
	}
	if !found {
		return []value.Value{value.Nil}, nil
	}
	return capsToValues(st, s, start, end, caps), nil
}

// stringFnGmatch: string.gmatch(s, pat) → iterator closure.
//
// The iterator is a host closure registered through State; its state (the next
// start position) is held in a Go closure variable.
func stringFnGmatch(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	s, e := strArg(st, args, 0, "gmatch")
	if e != nil {
		return nil, e
	}
	pat, e := strArg(st, args, 1, "gmatch")
	if e != nil {
		return nil, e
	}
	// The iterator outlives this call, so it must snapshot its inputs -- but that snapshot is
	// real work proportional to the subject, and the call produces only a closure, so no
	// produced-bytes charge can see it. Constructing 60000 iterators over a 1 MiB subject
	// without iterating once ran 27 seconds untripped. (PUC stores a pointer and charges
	// nothing, but wangshu cannot hold a raw arena pointer across GC.)
	if ce := st.ChargeBulkWork(len(s) + len(pat)); ce != nil {
		return nil, ce
	}
	src := append([]byte(nil), s...)
	p := append([]byte(nil), pat...)
	pos := 0
	iter := func(ist *crescent.State, _ []value.Value) ([]value.Value, *crescent.LuaError) {
		if pos > len(src) {
			return []value.Value{value.Nil}, nil
		}
		start, end, caps, found, err := patternFindOpt(src, p, pos, false)
		if err != nil {
			return nil, crescent.NewError(err.Error())
		}
		if !found {
			pos = len(src) + 1
			return []value.Value{value.Nil}, nil
		}
		if end == start {
			// empty match: advance by +1 from the hit position (PUC
			// gmatch_aux `if (e==src) newstart++`; note the hit position
			// may be past pos — the condition is end==start, not
			// end==pos, otherwise an empty match found after the scan
			// advanced wouldn't move and the iterator would spin in place
			// repeating output).
			pos = end + 1
		} else {
			pos = end
		}
		return capsToValues(ist, src, start, end, caps), nil
	}
	id := st.RegisterHostFn(iter)
	cl := st.MakeHostClosure(id)
	return []value.Value{value.MakeGC(value.TagFunction, cl)}, nil
}

// stringFnGsub: string.gsub(s, pat, repl [, n]). repl supports
// string/function/table.
func stringFnGsub(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	s, e := strArg(st, args, 0, "gsub")
	if e != nil {
		return nil, e
	}
	pat, e := strArg(st, args, 1, "gsub")
	if e != nil {
		return nil, e
	}
	if len(args) < 3 {
		return nil, crescent.NewArgError(3, "string/function/table expected")
	}
	repl := args[2]
	// "unlimited" is tracked SEPARATELY from the count, not encoded as -1.
	//
	// Sharing the sign bit was wrong once the count started being narrowed: PUC's
	// gsub loop runs while n < max_s, so a negative max_s means ZERO replacements,
	// while -1 as a sentinel meant unlimited. gsub("aaaa","a","b",-1) is
	// "aaaa", 0 on PUC and was "bbbb", 4 here -- and every count that NARROWS to
	// a negative, such as 2^31, hit the same path.
	maxN, unlimited := 0, true
	if len(args) >= 4 && args[3] != value.Nil {
		f, ok := toNumberStr(st, args[3])
		if !ok {
			return nil, crescent.NewArgError(4, "number expected, got "+st.TypeName(args[3]))
		}
		maxN, unlimited = int(cCharCastInt32(f)), false // luaL_optint narrowing
	}
	// PUC validates the replacement's TYPE here -- AFTER reading argument 4, not before.
	//
	// str_gsub runs luaL_optint(L, 4, ...) and only then luaL_argcheck on tr, so when BOTH are
	// bad the count's error wins: gsub("", "", nil, {}) reports bad argument #4, not #3.
	// Hoisting this check above the count read got #216 right and broke that ordering.
	//
	// It still has to run BEFORE the substitution loop: checking lazily inside it meant
	// gsub("", "", nil, 0) succeeded, because a zero count left the loop unentered and the bad
	// argument unseen.
	switch value.Tag(repl) {
	case value.TagString, value.TagTable, value.TagFunction:
		// ok
	default:
		if !value.IsNumber(repl) { // a number is accepted, like a string
			return nil, crescent.NewArgError(3, "string/function/table expected")
		}
	}
	// Charge the SUBJECT up front, then each replacement as it is produced (below).
	//
	// An earlier version guessed the total as len(s) + replLen*(len(s)+1) -- the worst case where
	// every byte position matches -- and that was wrong in both directions. It over-charged
	// ordinary work: a 16 KiB template with one %BODY% token and a 4 KiB replacement was billed
	// ~67 MB and rejected outright, where lua5.1 just returns the 20 KiB result. The formula
	// ignored maxN, the anchored early break, and the pattern's own length, so a one-match gsub
	// paid for a million. And it UNDER-charged the case it could not see: for a function or table
	// replacement the length is unknowable up front, so the fallback billed only the subject and a
	// closure returning 200 KB per match ran 15 seconds without touching the budget.
	//
	// Charging inside the loop needs no estimate at all: at each match the replacement's real
	// length is in hand. Guessing a size when the exact one is available a few lines later was the
	// mistake.
	if e := st.ChargeBulkWork(len(s)); e != nil {
		return nil, e
	}
	var out []byte
	pos := 0
	count := 0
	anchored := len(pat) > 0 && pat[0] == '^'
	for (unlimited || count < maxN) && pos <= len(s) {
		start, end, caps, found, err := patternFind(s, pat, pos)
		if err != nil {
			return nil, crescent.NewError(err.Error())
		}
		// An anchored pattern only matches at pos (patternFind already
		// guarantees this); stop on either no match or a match not at pos.
		if !found || (anchored && start != pos) {
			break
		}
		out = append(out, s[pos:start]...)
		rep, le := st2gsubRepl(st, s, start, end, caps, repl)
		if le != nil {
			return nil, le
		}
		// Charge every match, whatever the replacement's kind.
		//
		// An attempt to remove double-billing gated this on function/table replacements, on the
		// grounds that st2gsubRepl already bills string ones incrementally. That was wrong: the
		// inner charge covers ONE expansion, so gating here left a loop of 200000 string-replacement
		// matches with nothing bounding the total, and the storm case hung. Billing a copied byte
		// twice is a 2x error; not billing it at all is unbounded, and the two are not comparable.
		if ce := st.ChargeBulkWork(len(rep)); ce != nil {
			return nil, ce
		}
		out = append(out, rep...)
		count++
		if end == start {
			if start < len(s) {
				out = append(out, s[start])
			}
			pos = start + 1
		} else {
			pos = end
		}
		if anchored {
			break // lstrlib: an anchored gsub replaces at most once
		}
	}
	if pos < len(s) {
		out = append(out, s[pos:]...)
	}
	return []value.Value{intern(st, string(out)), value.NumberValue(float64(count))}, nil
}

// st2gsubRepl computes the replacement text for one match.
func st2gsubRepl(st *crescent.State, src []byte, s, e int, caps []capResult, repl value.Value) ([]byte, *crescent.LuaError) {
	whole := src[s:e]
	// Materialize the captures ONCE, lazily.
	//
	// This used to call capsToValues on every %n, re-interning every capture each time: a
	// replacement with 20000 back-references to a 1 MiB capture interned 20 GiB and ran 11.5
	// seconds. The charge below bills produced bytes, which cannot see that -- the cost was in
	// bytes CONSUMED per reference, not produced.
	var capVals []value.Value
	capsDone := false
	capVal := func(i int) value.Value {
		if !capsDone {
			capVals = capsToValues(st, src, s, e, caps)
			capsDone = true
		}
		if i < len(capVals) {
			return capVals[i]
		}
		return value.Nil
	}
	switch {
	case value.Tag(repl) == value.TagString || value.IsNumber(repl):
		var rb []byte
		if value.IsNumber(repl) {
			rb = []byte(crescent.FormatLuaNumber(value.AsNumber(repl)))
		} else {
			rb = object.StringBytes(st.Arena(), value.GCRefOf(repl))
		}
		var out []byte
		charged := 0
		for i := 0; i < len(rb); i++ {
			// Charge INSIDE the expansion, not after it returns.
			//
			// A single match can amplify without bound: gsub("(a+)", string.rep("%1", 20000)) on a
			// 1 MiB subject expands 20000 back-references before the caller's per-match charge is
			// ever consulted, which ran 46 seconds -- past go-fuzz's 10s watchdog, the very
			// signature this budget exists to stop. Billing each appended chunk as it is produced
			// bounds one match as well as many, and still needs no estimate.
			if e := st.ChargeBulkWork(len(out) - charged); e != nil {
				return nil, e
			}
			charged = len(out)
			if rb[i] == '%' && i+1 < len(rb) {
				i++
				c := rb[i]
				if c == '%' {
					out = append(out, '%')
				} else if c >= '0' && c <= '9' {
					if c == '0' {
						out = append(out, whole...)
					} else {
						// %n out of range (beyond the capture count; with no
						// explicit captures only %1 is valid = whole match).
						// PUC push_onecapture raises invalid capture index.
						idx := int(c - '1')
						nCaps := len(caps)
						if nCaps == 0 {
							nCaps = 1 // no explicit captures: capture 1 = whole match
						}
						if idx >= nCaps {
							return nil, crescent.NewError(fmt.Sprintf("invalid capture index %%%c", c))
						}
						v := capVal(idx)
						b, _ := valueToBytesForGsub(st, v)
						// Charge the capture's bytes on each reference: copying it is the work,
						// whether or not the result is kept.
						if ce := st.ChargeBulkWork(len(b)); ce != nil {
							return nil, ce
						}
						out = append(out, b...)
					}
				} else {
					return nil, crescent.NewError("invalid use of '%' in replacement string")
				}
			} else {
				out = append(out, rb[i])
			}
		}
		return out, nil
	case value.Tag(repl) == value.TagFunction:
		vals := capsToValues(st, src, s, e, caps)
		results, le := st.ProtectedCallDirect(repl, vals)
		if le != nil {
			return nil, le
		}
		if len(results) == 0 || results[0] == value.Nil || results[0] == value.False {
			return whole, nil
		}
		b, ok := valueToBytesForGsub(st, results[0])
		if !ok {
			return nil, crescent.NewError("invalid replacement value (a " + st.TypeName(results[0]) + ")")
		}
		return b, nil
	case value.Tag(repl) == value.TagTable:
		key := capVal(0)
		// Through the __index chain (PUC gsub uses lua_gettable, so
		// metamethods are visible).
		v, le := st.IndexWithMeta(repl, key)
		if le != nil {
			return nil, le
		}
		if v == value.Nil || v == value.False {
			return whole, nil
		}
		b, ok := valueToBytesForGsub(st, v)
		if !ok {
			return nil, crescent.NewError("invalid replacement value (a " + st.TypeName(v) + ")")
		}
		return b, nil
	}
	return nil, crescent.NewArgError(3, "string/function/table expected")
}

func valueToBytesForGsub(st *crescent.State, v value.Value) ([]byte, bool) {
	if value.IsNumber(v) {
		return []byte(crescent.FormatLuaNumber(value.AsNumber(v))), true
	}
	if value.Tag(v) == value.TagString {
		return object.StringBytes(st.Arena(), value.GCRefOf(v)), true
	}
	return nil, false
}

// stringFnFormat: string.format(fmt, ...) — %d %i %u %f %g %e %s %q %x %X %o %c %%.
func stringFnFormat(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	f, e := strArg(st, args, 0, "format")
	if e != nil {
		return nil, e
	}
	var out []byte
	charged := 0
	argn := 1
	i := 0
	for i < len(f) {
		// Charge as the output grows, not once at the end: 7000 %s verbs with 1 MiB arguments each
		// produced 7 GiB and ran 11 seconds before a single trailing charge was consulted, which is
		// past go-fuzz's 10s watchdog. Same defect shape as gsub's, in the same round.
		if e := st.ChargeBulkWork(len(out) - charged); e != nil {
			return nil, e
		}
		charged = len(out)
		if f[i] != '%' {
			out = append(out, f[i])
			i++
			continue
		}
		i++
		if i < len(f) && f[i] == '%' {
			out = append(out, '%')
			i++
			continue
		}
		// flags/width/precision: matches PUC scanformat's hard limits —
		// at most 5 flags (sizeof(FLAGS)-1; the 6th raises repeated
		// flags), and width and precision each at most 2 digits (the 3rd
		// raises width or precision too long). This doubles as embedded
		// hardening: the 2-digit width cap fully seals off
		// `%.99999999999d`-style OOM (the old defense was a self-imposed
		// 1 GiB threshold; PUC semantics are stricter and byte-equal;
		// oracle diff fuzz turned up the %100X divergence).
		spec := []byte{'%'}
		flagStart := i
		for i < len(f) && strings.ContainsRune("-+ #0", rune(f[i])) {
			spec = append(spec, f[i])
			i++
		}
		if i-flagStart >= 6 {
			return nil, crescent.NewError("invalid format (repeated flags)")
		}
		for d := 0; d < 2 && i < len(f) && isdigit(f[i]); d++ {
			spec = append(spec, f[i])
			i++
		}
		if i < len(f) && isdigit(f[i]) {
			return nil, crescent.NewError("invalid format (width or precision too long)")
		}
		if i < len(f) && f[i] == '.' {
			spec = append(spec, f[i])
			i++
			for d := 0; d < 2 && i < len(f) && isdigit(f[i]); d++ {
				spec = append(spec, f[i])
				i++
			}
			if i < len(f) && isdigit(f[i]) {
				return nil, crescent.NewError("invalid format (width or precision too long)")
			}
		}
		if i >= len(f) {
			return nil, crescent.NewError("invalid format string to 'format'")
		}
		verb := f[i]
		i++
		if argn >= len(args) && verb != '%' {
			return nil, crescent.NewArgError(argn+1, "no value")
		}
		switch verb {
		case 'd', 'i':
			n, ok := toNumberStr(st, args[argn])
			if !ok {
				return nil, crescent.NewArgError(argn+1, "number expected, got "+st.TypeName(args[argn]))
			}
			out = append(out, cSignedFormat(spec, int64(n))...)
			argn++
		case 'u', 'x', 'X', 'o':
			// PUC casts through unsigned LUA_INTFRM_T: %u/%x/%o of -1
			// print the two's-complement value, not "-1". Non-number
			// arguments raise via luaL_checknumber (oracle diff fuzz
			// caught the silently-ignored conversion failure).
			n, ok := toNumberStr(st, args[argn])
			if !ok {
				return nil, crescent.NewArgError(argn+1, fmt.Sprintf("number expected, got %s", st.TypeName(args[argn])))
			}
			// Rendered manually: Go's fmt diverges from C printf on
			// the '#' flag ("%#X" of 0 prints "0X0" -- C omits the
			// prefix for zero; "%#08X" pads outside the prefix; "%#.0o"
			// of 0 prints "" -- C forces one octal zero) and applies
			// ' '/'+' to unsigned verbs that C ignores. Oracle diff
			// fuzz catches: format("% 00X0", 0), format("%#X", 0).
			out = append(out, cUnsignedFormat(spec, verb, cUnsignedCast(n))...)
			argn++
		case 'c':
			// PUC sprintf's the char with the full spec (width/flags
			// apply: %5c pads with spaces; the 0 flag is numeric-only
			// and ignored) and appends buff with strlen -- so a NUL
			// char truncates everything from itself on
			// (format("%002c", 0) == " "). Go's %c would encode
			// bytes >= 0x80 as multi-byte UTF-8, so pad manually and
			// mirror the strlen cut (oracle diff fuzz catch).
			n, ok := toNumberStr(st, args[argn])
			if !ok {
				return nil, crescent.NewArgError(argn+1, fmt.Sprintf("number expected, got %s", st.TypeName(args[argn])))
			}
			// %c uses (int)luaL_checkNUMBER -- a DIRECT double->int cast, not
			// the two-step double->lua_Integer->int that luaL_checkint does.
			// The distinction matters: 2^40+1 has low-32 bits of 65, so the
			// two-step gives byte 65, while the direct cast on x86-64 yields
			// INT32_MIN ("integer indefinite") and hence byte 0, which is what
			// the oracle produces. Verified against C on this host.
			out = append(out, cPadChar(spec, byte(cDirectInt32(n)))...)
			argn++
		case 'f', 'e', 'E', 'g', 'G':
			n, ok := toNumberStr(st, args[argn])
			if !ok {
				return nil, crescent.NewArgError(argn+1, "number expected, got "+st.TypeName(args[argn]))
			}
			// NaN/Inf: Go's fmt prints "NaN"/"+Inf"/"-Inf", but PUC
			// routes through C sprintf, whose glibc output is
			// verb-case dependent (lowercase verb -> nan/inf,
			// uppercase -> NAN/INF). wangshu renders NaN without a
			// sign under every verb (see cFormatSpecialFloat). Render
			// these specially, applying only width + left-justify from
			// the spec (precision and +/space are meaningless for NaN,
			// C ignores them). Oracle diff fuzz catch (#170/#171).
			if math.IsNaN(n) || math.IsInf(n, 0) {
				out = append(out, cFormatSpecialFloat(spec, verb, n)...)
				argn++
				continue
			}
			// %g/%G with NO explicit precision needs one supplied: C defaults to
			// precision 6, while Go's %g defaults to the shortest representation
			// that round-trips. string.format("%g", 1/3) is "0.333333" in C and
			// was "0.3333333333333333" here. %e and %f already agree, because Go
			// matches C's default of 6 for those.
			es := spec
			if verb == 'g' || verb == 'G' {
				if _, hasPrec := specPrecision(spec); !hasPrec {
					es = append(append([]byte{}, spec...), '.', '6')
				}
			}
			out = append(out, []byte(fmt.Sprintf(string(append(es, verb)), n))...)
			argn++
		case 's':
			// PUC 's' reads via luaL_checklstring: string/number only
			// -- nil/boolean/table raise (oracle diff fuzz catch;
			// tostring-style acceptance is a Lua 5.2+ behavior).
			svb, e2 := strArg(st, args, argn, "format")
			if e2 != nil {
				return nil, e2 // strArg's message already matches PUC
			}
			// Charge the ARGUMENT's bytes, before the copy and before any precision truncation:
			// "%.1s" against a 1 MiB argument still copies the megabyte, and "%.0s" produces
			// nothing at the same cost, so the work is in the bytes CONSUMED.
			//
			// Then advance `charged` past what this verb will emit, so the loop's produced-bytes
			// increment does not bill the SAME bytes a second time. For a copy-through verb the
			// consumed and produced bytes are one cost, not two; double-counting them made a
			// 1 MiB format need a 3x budget, contradicting the documented 1-step-per-64-bytes
			// rate.
			if ce := st.ChargeBulkWork(len(svb)); ce != nil {
				return nil, ce
			}
			sv := string(svb)
			// PUC str_format 's': strings >= 100 chars WITHOUT a
			// precision bypass sprintf (pushed whole, NULs intact);
			// everything else goes through sprintf + strlen append,
			// which truncates at the first NUL byte.
			hasPrec := bytes.ContainsRune(spec, '.')
			if !hasPrec && len(sv) >= 100 {
				out = append(out, sv...)
			} else {
				// C printf ignores the '0' flag for %s (space pad);
				// Go's %0Ns really zero-pads strings. Strip 0s from
				// the FLAG region only -- width digits may legally
				// contain zeros (%10s). Flags end at the first
				// nonzero digit or '.' (format("%02s", 0) == " 0",
				// oracle diff fuzz catch).
				sSpec := stripZeroFlag(spec)
				formatted := fmt.Sprintf(string(append(sSpec, 's')), sv)
				if i := strings.IndexByte(formatted, 0); i >= 0 {
					formatted = formatted[:i]
				}
				out = append(out, formatted...)
			}
			argn++
			// Skip re-billing the bytes the argument charge already covered.
			if charged < len(out) {
				if adv := len(svb); charged+adv <= len(out) {
					charged += adv
				} else {
					charged = len(out)
				}
			}

		case 'q':
			sb, e2 := strArg(st, args, argn, "format")
			if e2 != nil {
				return nil, e2
			}
			out = append(out, quoteLuaString(sb)...)
			argn++
		default:
			return nil, crescent.NewError(fmt.Sprintf("invalid option '%%%c' to 'format'", verb))
		}
	}
	// Charge the formatted bytes, same meter as CONCAT and string.rep: a format loop over large
	// %s arguments ran 20 seconds inside a 1<<20 budget without tripping it.
	// Only the REMAINDER: the in-loop charge already billed everything up to `charged`, and
	// re-billing the whole output made a 1 MiB format cost ~3x the honest 1-step-per-64-bytes
	// rate, contradicting the figures in 10 §3.1a and embedding-tiers §5.
	if e := st.ChargeBulkWork(len(out) - charged); e != nil {
		return nil, e
	}
	return []value.Value{intern(st, string(out))}, nil
}

// cFormatSpecialFloat renders a NaN or Inf through the same conversion PUC's
// C sprintf produces on glibc, since Go's fmt spells these differently
// ("NaN"/"+Inf"/"-Inf" vs C's nan/inf/NAN/INF). Verified against the
// embedded PUC 5.1.5 oracle (#170/#171):
//
//	verb        NaN (0/0)   +Inf (1/0)   -Inf (-1/0)
//	%f %e %g    nan         inf          -inf
//	%E %G       NAN         INF          -INF
//
// glibc's NaN sign is a quirk: the lowercase conversion prints a bare "nan"
// (no sign, and +/space flags are ignored), while the uppercase conversion
// prints "-NAN" for the same 0/0 bit pattern. Inf follows the ordinary sign
// and honors the +/space flags. Only width and the '-' (left-justify) flag
// from spec are applied here; precision is meaningless for these values and
// C ignores it.
//
// NaN renders WITHOUT a sign under every verb, upper or lower.
//
// This used to hardcode "-NAN" for the uppercase verbs, to imitate what glibc
// prints so the differential oracle would agree. That was imitation of one
// CPU/libc combination for the benefit of a test, and it was never right as
// product behaviour: IEEE 754 gives a NaN's sign bit no numeric meaning, and
// wangshu cannot honour it anyway because value.NumberValue canonicalizes every
// NaN to one bit pattern. The oracle now normalizes its own NaN rendering
// instead (internal/oracle/lua515.c), so nothing is gained by the imitation and
// the inconsistency between %e and %E is gone.
func cFormatSpecialFloat(spec []byte, verb byte, f float64) []byte {
	upper := verb == 'E' || verb == 'G'

	var core string
	if math.IsNaN(f) {
		if upper {
			core = "NAN"
		} else {
			core = "nan"
		}
	} else {
		// Inf: real sign, plus the +/space flag for a positive value.
		neg := math.IsInf(f, -1)
		word := "inf"
		if upper {
			word = "INF"
		}
		var sign string
		switch {
		case neg:
			sign = "-"
		case bytes.IndexByte(spec, '+') >= 0:
			sign = "+"
		case bytes.IndexByte(spec, ' ') >= 0:
			sign = " "
		}
		core = sign + word
	}

	// Apply width + left-justify from spec via manual space padding.
	// spec is "%" + flags + width + optional ".prec"; flags and precision
	// are already accounted for above, so only the width digits matter.
	//
	// Every value pads to the FULL declared width, NaN and Inf alike.
	//
	// This used to subtract one column for a lowercase NaN, imitating glibc,
	// which always reserves a column for a NaN's sign and so renders %5f as
	// " nan" (four visible characters). That was imitation of one libc for the
	// benefit of the differential oracle, and it made wangshu's own %5f and %5E
	// pad inconsistently. The oracle now normalizes its NaN rendering, sign and
	// reserved column together (internal/oracle/lua515.c), so the imitation buys
	// nothing. Precision is ignored for these values, as in C.
	width := 0
	left := bytes.IndexByte(spec, '-') >= 0
	for i := 1; i < len(spec); i++ {
		c := spec[i]
		if c == '.' {
			break // precision follows; ignored for NaN/Inf
		}
		if c >= '0' && c <= '9' {
			// A leading '0' here is the zero-pad flag, not a width digit;
			// C space-pads NaN/Inf regardless, so folding it into the
			// width value is harmless.
			width = width*10 + int(c-'0')
		}
	}
	if width <= len(core) {
		return []byte(core)
	}
	pad := make([]byte, width-len(core))
	for i := range pad {
		pad[i] = ' '
	}
	if left {
		return append([]byte(core), pad...)
	}
	return append(pad, []byte(core)...)
}

// quoteLuaString implements %q (byte-for-byte aligned with PUC addquoted):
// `"` and `\` get a leading backslash; `\n` is emitted as backslash + a real
// newline (not the two chars \n); `\r` → \r; NUL → \000 (three digits, to keep
// a following digit from sticking to it).
func quoteLuaString(s []byte) []byte {
	out := []byte{'"'}
	for _, c := range s {
		switch c {
		case '"', '\\':
			out = append(out, '\\', c)
		case '\n':
			out = append(out, '\\', '\n')
		case '\r':
			out = append(out, '\\', 'r')
		case 0:
			out = append(out, '\\', '0', '0', '0')
		default:
			out = append(out, c)
		}
	}
	return append(out, '"')
}

// stringFnByte: string.byte(s [, i [, j]]).
func stringFnByte(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	s, e := strArg(st, args, 0, "byte")
	if e != nil {
		return nil, e
	}
	// PUC luaL_optinteger: nil defaults, but a present non-number
	// argument raises (string.byte("abc", "y") errors; "2" coerces).
	iF, ok := numArg(st, args, 1, 1)
	if !ok {
		return nil, crescent.NewArgError(2, fmt.Sprintf("number expected, got %s", st.TypeName(args[1])))
	}
	jF, ok := numArg(st, args, 2, iF)
	if !ok {
		return nil, crescent.NewArgError(3, fmt.Sprintf("number expected, got %s", st.TypeName(args[2])))
	}
	i := normIdx(int(iF), len(s))
	j := normIdx(int(jF), len(s))
	if i < 1 {
		i = 1
	}
	if j > len(s) {
		j = len(s)
	}
	// Same lua_checkstack ceiling as unpack, and the same off-by-nargs: PUC's str_byte
	// calls luaL_checkstack(L, n, "string slice too long"), which rejects when
	// (L->top - L->base) + n exceeds LUAI_MAXCSTACK -- and for a C function that first
	// term is the argument count. So the bound is 8000 - nargs, not a flat 8000, and
	// string.byte had no bound at all: a 9000-byte string sliced 1..8000 returned 8000
	// values where PUC raises. Note the message differs from unpack's.
	if n := j - i + 1; n > 0 && n > maxCStack-len(args) {
		// luaL_checkstack WRAPS the caller's text: "stack overflow (%s)". The bare
		// message is what luaL_error would give, and str_byte uses checkstack, so the
		// wrapped form is the one PUC emits. Also note -1 and 1e6 reach here too --
		// after normIdx they still exceed the ceiling.
		return nil, crescent.NewError("stack overflow (string slice too long)")
	}
	var out []value.Value
	for k := i; k <= j; k++ {
		out = append(out, value.NumberValue(float64(s[k-1])))
	}
	return out, nil
}

// stringFnChar: string.char(...).
func stringFnChar(st *crescent.State, args []value.Value) ([]value.Value, *crescent.LuaError) {
	out := make([]byte, len(args))
	for i, a := range args {
		f, ok := toNumberStr(st, a)
		if !ok {
			return nil, crescent.NewArgError(i+1, "number expected, got "+st.TypeName(a))
		}
		// PUC str_char: luaL_checkint truncates toward zero, then
		// luaL_argcheck(uchar(c) == c) rejects anything outside
		// [0, 255] ("invalid value"): char(-1)/char(256) error,
		// char(3.7) == char(3). Oracle diff fuzz caught the old
		// silent byte() wraparound.
		//
		// Non-finite and out-of-int64-range values take PUC's two-step cast,
		// which is NOT a plain range check. luaL_checkint is
		// (int)luaL_checkinteger, so the double first becomes a lua_Integer
		// (ptrdiff_t) and is THEN narrowed to int. On x86-64 cvttsd2si maps
		// NaN and every out-of-range double to INT64_MIN, whose low 32 bits are
		// zero, so c == 0, uchar(c) == c holds, and PUC emits byte 0 rather
		// than erroring.
		//
		// This mirrors cUnsignedCast's choice for %u/%x/%o: the C cast is UB
		// outside the representable range and the two official PUC builds
		// genuinely disagree (arm64 FCVTZS saturates +inf to INT64_MAX, whose
		// low 32 bits are -1, which fails the uchar check and errors). wangshu
		// pins the x86-64 result on all arches, and the differential harness
		// skips the UB range instead of comparing it -- see the
		// "unsigned-cast UB range" sentinel in internal/oracle/prelude.go.
		n := int64(cCharCast(f))
		if n < 0 || n > 255 {
			return nil, crescent.NewArgError(i+1, "invalid value")
		}
		out[i] = byte(n)
	}
	return []value.Value{intern(st, string(out))}, nil
}

// cCharCast mirrors PUC's luaL_checkint on x86-64 for string.char: the double
// becomes a lua_Integer (ptrdiff_t, 64-bit) and is then narrowed to int.
//
// x86-64 cvttsd2si yields INT64_MIN ("integer indefinite") for NaN and for any
// double outside int64 range; narrowing that to int32 gives 0. Go's own
// float64->int64 conversion is undefined for those inputs, so the mapping is
// written out rather than relied upon. Values inside range truncate toward zero,
// which both agree on.
// cCharCastInt32 is the luaL_checkint narrowing, named for reuse outside
// string.char: double -> lua_Integer (64-bit) -> int.
func cCharCastInt32(f float64) int32 { return cCharCast(f) }

// cDirectInt32 mirrors a DIRECT C (int)(double) cast, as used by string.format's
// %c via (int)luaL_checknumber. x86-64 cvttsd2si yields INT32_MIN for NaN and for
// anything outside int32 range -- unlike the two-step luaL_checkint path, which
// truncates the low 32 bits of a 64-bit intermediate.
func cDirectInt32(f float64) int32 {
	if math.IsNaN(f) || f >= 2147483648.0 || f < -2147483648.0 {
		return math.MinInt32
	}
	return int32(f)
}

func cCharCast(f float64) int32 {
	if math.IsNaN(f) || f >= 9223372036854775808.0 || f < -9223372036854775808.0 {
		return int32(math.MinInt64 & 0xFFFFFFFF) // low 32 bits of INT64_MIN == 0
	}
	return int32(int64(f))
}

// cUnsignedCast mirrors the x86-64 C `(unsigned long long)(double)`
// conversion PUC's str_format performs for %u/%x/%X/%o (issue #158:
// format("%X", 1e19) must print 8AC7230489E80000, but Go's
// uint64(int64(f)) saturates any f >= 2^63 to the int64-overflow
// indefinite 0x8000000000000000). gcc lowers the cast as: values below
// 2^63 go through cvttsd2si directly (NaN falls into this branch too —
// comisd unordered sets CF; negatives wrap two's-complement; NaN and
// underflow produce the 0x8000000000000000 indefinite), values at or
// above 2^63 convert as (f - 2^63) + 2^63 (+inf and >= 2^64 overflow
// cvttsd2si to the indefinite, which the +2^63 then wraps to 0).
//
// Outside [0, 2^64) the C cast is UB and PUC's output genuinely
// differs by arch (arm64 FCVTZU saturates: NaN/negative -> 0,
// >= 2^64 -> 0xFFFF...). Go's own out-of-range conversion is
// arch-dependent for the same reason (amd64 CVTTSD2SI indefinite vs
// arm64 FCVTZS saturation), so every UB corner below is spelled out
// explicitly: wangshu pins the x86-64 gcc behavior (probe-verified)
// on ALL arches rather than inheriting whichever instruction the Go
// compiler picks. The oracle-diff prelude reroutes the UB range to a
// skip — an arm64 PUC build is entitled to disagree there.
func cUnsignedCast(f float64) uint64 {
	const (
		two63 = 9223372036854775808.0  // 2^63
		two64 = 18446744073709551616.0 // 2^64
	)
	switch {
	case f != f: // NaN: cvttsd2si indefinite
		return 1 << 63
	case f < two63:
		if f <= -two63 { // -inf and below-int64 range: indefinite
			return 1 << 63
		}
		return uint64(int64(f)) // int64 range: exact truncation, Go-defined
	case f < two64:
		return uint64(int64(f-two63)) + (1 << 63)
	default: // +inf or >= 2^64: indefinite + 2^63 wraps to 0
		return 0
	}
}

// cUnsignedFormat renders C sprintf's %u/%x/%X/%o. Go's fmt cannot be
// reused here: it diverges from C on the '#' flag ("%#X" of 0 prints
// "0X0" where C omits the prefix for a zero value; "%#08X" must
// zero-pad INSIDE the prefix; "%#o" forces a leading octal zero by
// widening precision, and "%#.0o" of 0 prints "0" where Go prints ""),
// and it honors ' '/'+' on unsigned verbs that C ignores.
// cSignedFormat renders %d/%i the way C printf does, which Go's fmt does not in
// one corner: when precision is 0 and the value is 0, C converts the value to NO
// digits (C99 7.19.6.1) but still emits the sign that a '+' or ' ' flag asks
// for, because that sign is not one of the converted digits. Go treats the whole
// conversion as empty and drops the sign with it:
//
//	spec      C          Go
//	%+.0d     "+"        ""
//	%+5.0d    "    +"    "     "
//	% .0d     " "        ""
//	%.0d      ""         ""        (agree: no flag, nothing to keep)
//
// Everything else delegates to fmt, which matches. This mirrors the neighbouring
// unsigned verbs, which are hand-rolled for the same family of Go-vs-C printf
// disagreements (see cUnsignedFormat).
func cSignedFormat(spec []byte, n int64) []byte {
	prec, hasPrec := specPrecision(spec)
	if !hasPrec || prec != 0 || n != 0 {
		return []byte(fmt.Sprintf(string(append(spec, 'd')), n))
	}
	// Empty digit string. Build sign + width padding by hand.
	sign := ""
	switch {
	case bytes.IndexByte(spec, '+') >= 0:
		sign = "+"
	case bytes.IndexByte(spec, ' ') >= 0:
		sign = " "
	}
	width := specWidth(spec)
	if width <= len(sign) {
		return []byte(sign)
	}
	pad := bytes.Repeat([]byte{' '}, width-len(sign))
	if bytes.IndexByte(spec, '-') >= 0 {
		return append([]byte(sign), pad...)
	}
	return append(pad, sign...)
}

// specPrecision reports the precision in a "%"+flags+width[.prec] spec.
func specPrecision(spec []byte) (int, bool) {
	dot := bytes.IndexByte(spec, '.')
	if dot < 0 {
		return 0, false
	}
	prec := 0
	for i := dot + 1; i < len(spec) && spec[i] >= '0' && spec[i] <= '9'; i++ {
		prec = prec*10 + int(spec[i]-'0')
	}
	return prec, true
}

// specWidth reports the field width in a "%"+flags+width[.prec] spec. A leading
// '0' is the zero-pad flag rather than a width digit; C space-pads an empty
// conversion regardless, so treating it as width would be harmless but reading
// it as a flag keeps the parse honest.
func specWidth(spec []byte) int {
	i := 1 // skip '%'
	for i < len(spec) && (spec[i] == '-' || spec[i] == '+' || spec[i] == ' ' ||
		spec[i] == '#' || spec[i] == '0') {
		i++
	}
	width := 0
	for ; i < len(spec) && spec[i] >= '0' && spec[i] <= '9'; i++ {
		width = width*10 + int(spec[i]-'0')
	}
	return width
}

func cUnsignedFormat(spec []byte, verb byte, v uint64) []byte {
	minus, zero, hash := false, false, false
	i := 1 // skip '%'
	for ; i < len(spec); i++ {
		switch spec[i] {
		case '-':
			minus = true
		case '0':
			zero = true
		case '#':
			hash = true
		case '+', ' ':
			// C ignores sign flags for unsigned conversions.
		default:
			goto flagsDone
		}
	}
flagsDone:
	width := 0
	for ; i < len(spec) && isdigit(spec[i]); i++ {
		width = width*10 + int(spec[i]-'0')
	}
	hasPrec := false
	prec := 0
	if i < len(spec) && spec[i] == '.' {
		hasPrec = true
		for i++; i < len(spec) && isdigit(spec[i]); i++ {
			prec = prec*10 + int(spec[i]-'0')
		}
	}

	var digits string
	switch verb {
	case 'x':
		digits = strconv.FormatUint(v, 16)
	case 'X':
		digits = strings.ToUpper(strconv.FormatUint(v, 16))
	case 'o':
		digits = strconv.FormatUint(v, 8)
	default: // 'u'
		digits = strconv.FormatUint(v, 10)
	}
	// C: a zero value with an explicit zero precision converts to no
	// characters.
	if v == 0 && hasPrec && prec == 0 {
		digits = ""
	}
	if hasPrec && len(digits) < prec {
		digits = strings.Repeat("0", prec-len(digits)) + digits
	}
	prefix := ""
	if hash {
		switch verb {
		case 'x':
			if v != 0 {
				prefix = "0x"
			}
		case 'X':
			if v != 0 {
				prefix = "0X"
			}
		case 'o':
			// Alternate octal form: force the first digit to be zero
			// (this also resurrects "%#.0o" of 0 as "0").
			if len(digits) == 0 || digits[0] != '0' {
				digits = "0" + digits
			}
		}
	}
	body := prefix + digits
	if pad := width - len(body); pad > 0 {
		switch {
		case minus: // '-' beats '0' in C
			body += strings.Repeat(" ", pad)
		case zero && !hasPrec: // '0' is ignored when a precision is given
			body = prefix + strings.Repeat("0", pad) + digits
		default:
			body = strings.Repeat(" ", pad) + body
		}
	}
	return []byte(body)
}

// stripZeroFlag returns spec without '0' FLAG characters (the leading
// zeros before any width digits). Width digits are untouched: in
// "%010s" the first 0 is a flag, the "10" is width.
func stripZeroFlag(spec []byte) []byte {
	out := make([]byte, 0, len(spec))
	out = append(out, spec[0]) // '%'
	i := 1
	// flag region: "-+ #0" repeated
	for i < len(spec) && bytes.IndexByte([]byte("-+ #0"), spec[i]) >= 0 {
		if spec[i] != '0' {
			out = append(out, spec[i])
		}
		i++
	}
	out = append(out, spec[i:]...)
	return out
}

// cPadChar renders C sprintf's %c: one byte, space-padded to the spec
// width ('-' left-aligns; '0' is numeric-only, spaces regardless),
// then truncated at the first NUL like PUC's strlen-append.
// C99: precision has no effect on %c, so everything after '.' is ignored.
func cPadChar(spec []byte, c byte) []byte {
	width := 0
	left := false
	for _, f := range spec[1:] { // skip '%'
		if f == '.' {
			break // precision follows; C99 ignores it for %c
		}
		switch {
		case f == '-':
			left = true
		case f >= '0' && f <= '9':
			// A leading 0 is the (ignored-for-%c) zero flag only when
			// no width digits were seen; C parses "00" flags then "2"
			// width for %002c. Treating every leading 0 as flag and
			// later digits as width matches: width = width*10 only
			// after a nonzero digit or a prior width digit.
			if width == 0 && f == '0' {
				continue // zero flag (repeatable), no width yet
			}
			width = width*10 + int(f-'0')
		}
	}
	var body []byte
	if c != 0 {
		body = []byte{c}
	} // NUL: strlen(buff) cuts before any right-padding is visible...
	// ...but LEFT padding (right-aligned, the default) precedes the
	// char in buff, so spaces survive: sprintf("% 2c", 0) -> " \0",
	// strlen -> " ".
	pad := width - 1
	if pad < 0 {
		pad = 0
	}
	if left {
		// left-aligned: char first, then padding; a NUL char cuts
		// everything (strlen == 0).
		if c == 0 {
			return nil
		}
		return append(body, bytes.Repeat([]byte{' '}, pad)...)
	}
	outb := append(bytes.Repeat([]byte{' '}, pad), body...)
	return outb
}

// Keep the strconv reference alive (for future extensions like strInitPos).
var _ = strconv.Itoa
