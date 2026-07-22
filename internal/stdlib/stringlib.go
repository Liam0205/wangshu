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
		idx := strings.Index(string(s[init:]), string(pat))
		if idx < 0 {
			return []value.Value{value.Nil}, nil
		}
		start := init + idx
		return []value.Value{
			value.NumberValue(float64(start + 1)),
			value.NumberValue(float64(start + len(pat))),
		}, nil
	}
	start, end, caps, found, err := patternFind(s, pat, init)
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
	if init > len(s) {
		return []value.Value{value.Nil}, nil
	}
	start, end, caps, found, err := patternFind(s, pat, init)
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
	src := append([]byte(nil), s...)
	p := append([]byte(nil), pat...)
	pos := 0
	iter := func(ist *crescent.State, _ []value.Value) ([]value.Value, *crescent.LuaError) {
		if pos > len(src) {
			return []value.Value{value.Nil}, nil
		}
		start, end, caps, found, err := patternFind(src, p, pos)
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
	maxN := -1
	if len(args) >= 4 && args[3] != value.Nil {
		f, ok := toNumberStr(st, args[3])
		if !ok {
			return nil, crescent.NewArgError(4, "number expected, got "+st.TypeName(args[3]))
		}
		maxN = int(f)
	}
	var out []byte
	pos := 0
	count := 0
	anchored := len(pat) > 0 && pat[0] == '^'
	for (maxN < 0 || count < maxN) && pos <= len(s) {
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
	capVal := func(i int) value.Value {
		vals := capsToValues(st, src, s, e, caps)
		if i < len(vals) {
			return vals[i]
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
		for i := 0; i < len(rb); i++ {
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
	argn := 1
	i := 0
	for i < len(f) {
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
			out = append(out, []byte(fmt.Sprintf(string(append(spec, 'd')), int64(n)))...)
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
			out = append(out, cPadChar(spec, byte(int64(n)))...)
			argn++
		case 'f', 'e', 'E', 'g', 'G':
			n, ok := toNumberStr(st, args[argn])
			if !ok {
				return nil, crescent.NewArgError(argn+1, "number expected, got "+st.TypeName(args[argn]))
			}
			// NaN/Inf: Go's fmt prints "NaN"/"+Inf"/"-Inf", but PUC
			// routes through C sprintf, whose glibc output is
			// verb-case dependent (lowercase verb -> nan/inf,
			// uppercase -> NAN/INF) and treats the NaN sign quirkily
			// (%f of 0/0 is "nan" but %E of 0/0 is "-NAN"). Render
			// these specially, applying only width + left-justify from
			// the spec (precision and +/space are meaningless for NaN,
			// C ignores them). Oracle diff fuzz catch (#170/#171).
			if math.IsNaN(n) || math.IsInf(n, 0) {
				out = append(out, cFormatSpecialFloat(spec, verb, n)...)
				argn++
				continue
			}
			out = append(out, []byte(fmt.Sprintf(string(append(spec, verb)), n))...)
			argn++
		case 's':
			// PUC 's' reads via luaL_checklstring: string/number only
			// -- nil/boolean/table raise (oracle diff fuzz catch;
			// tostring-style acceptance is a Lua 5.2+ behavior).
			svb, e2 := strArg(st, args, argn, "format")
			if e2 != nil {
				return nil, e2 // strArg's message already matches PUC
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
	return []value.Value{intern(st, string(out))}, nil
}

// cFormatSpecialFloat renders a NaN or Inf through the same conversion PUC's
// C sprintf produces on glibc, since Go's fmt spells these differently
// ("NaN"/"+Inf"/"-Inf" vs C's nan/inf/NAN/INF). Verified against the
// embedded PUC 5.1.5 oracle (#170/#171):
//
//	verb        NaN (0/0)   +Inf (1/0)   -Inf (-1/0)
//	%f %e %g    nan         inf          -inf
//	%E %G       -NAN        INF          -INF
//
// glibc's NaN sign is a quirk: the lowercase conversion prints a bare "nan"
// (no sign, and +/space flags are ignored), while the uppercase conversion
// prints "-NAN" for the same 0/0 bit pattern. Inf follows the ordinary sign
// and honors the +/space flags. Only width and the '-' (left-justify) flag
// from spec are applied here; precision is meaningless for these values and
// C ignores it.
//
// This deliberately hardcodes the NaN sign as negative rather than reading
// f's sign bit: wangshu's arithmetic NaN carries the OPPOSITE sign bit from
// PUC/x86 (wangshu 0/0 is +NaN, PUC is -NaN), so reading the real bit would
// diverge from the oracle on exactly the 0%0 inputs #170/#171 reported.
// Hardcoding matches the fuzz-hit negative case; fully sign-correct NaN
// rendering needs the VM value layer aligned to x86 first — tracked in #173.
func cFormatSpecialFloat(spec []byte, verb byte, f float64) []byte {
	upper := verb == 'E' || verb == 'G'

	var core string
	if math.IsNaN(f) {
		if upper {
			// glibc prints the 0/0 NaN as "-NAN" under an uppercase
			// conversion; the lowercase form is a bare "nan".
			core = "-NAN"
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
	// PUC 5.1.5 / glibc quirk: glibc always reserves one column for a NaN's
	// sign. Under a lowercase verb the sign is invisible (core is a bare
	// "nan"), so that reserved column shows up as an effective field width
	// of declared-width MINUS ONE (%5f->" nan" [width 4], %10.3f->
	// "      nan" [width 9], %8.2f->"    nan" [width 7]). Under an uppercase
	// verb the sign is the visible '-' already in core ("-NAN"), so the
	// column is spent and the field pads to the FULL declared width
	// (%5E->" -NAN" [width 5]). Inf carries its own sign in core either way
	// and also pads to the full width (%5f->"  inf"). Precision is ignored.
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
	if math.IsNaN(f) && !upper && width > 0 {
		width-- // lowercase NaN: glibc's reserved sign column, unshown
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
		n := int64(f)
		if n < 0 || n > 255 {
			return nil, crescent.NewArgError(i+1, "invalid value")
		}
		out[i] = byte(n)
	}
	return []value.Value{intern(st, string(out))}, nil
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
