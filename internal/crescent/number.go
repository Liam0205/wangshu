// parseLuaNumber — the single string→number entry point (07 §5.2: arithmetic
// coercion, numeric for, and tonumber all share one implementation, so a single
// implementation guarantees consistent behavior).
//
// Rules (aligned with 5.1 lobject.c luaO_str2d): C99 strtod semantics + hex
// integer fallback at an 'x' stop point + trailing-whitespace tolerance. strtod
// accepts more than a decimal ParseFloat:
//   - hex float: `0x.8` = 0.5, `0X.0` = 0, `0x1p4` = 16 (p exponent optional);
//   - inf / infinity / nan words (case-insensitive);
//   - overflow returns ±inf (luaO_str2d does not check errno: `1e999` = inf).
//
// A cgo-oracle differential fuzz uncovered a divergence where the old
// implementation (decimal ParseFloat + a dedicated 0x integer path) returned
// nil for `tonumber("0X.0")`.
package crescent

import (
	"errors"
	"strconv"
	"strings"
)

// parseLuaNumberBytes parses a byte slice; on success returns (f, true).
func parseLuaNumberBytes(b []byte) (float64, bool) {
	return ParseLuaNumber(string(b))
}

// ParseLuaNumber parses a Lua number string (shared with stdlib's tonumber).
func ParseLuaNumber(s string) (float64, bool) {
	// PUC 5.1 luaO_str2d receives a C string, which is NUL-terminated.
	// strtod stops at the first NUL byte (C string end), so embedded NUL
	// characters effectively truncate the input: tonumber("0\0junk") == 0.
	// Go strings can carry embedded NULs; truncate to mirror C semantics.
	if idx := strings.IndexByte(s, 0); idx >= 0 {
		s = s[:idx]
	}
	i := 0
	for i < len(s) && isLuaSpace(s[i]) {
		i++
	}
	rest := s[i:]
	f, n, ok := strtodPrefix(rest)
	if !ok {
		return 0, false
	}
	// luaO_str2d: when strtod stops at 'x'/'X' ⟹ reparse from the start via
	// strtoul(s, 16), then still follow the same endptr contract (skip trailing
	// whitespace, any remaining character ⟹ failure).
	// strtoul semantics: optional sign + optional "0x" prefix + longest hex digit run;
	// overflow saturates to ULONG_MAX. Under C99 this fallback almost always fails
	// (a fully valid hex string has already been consumed by strtod's hex float;
	// stop-point shapes like "1X0"/"0x" leave strtoul with an incompletely
	// consumed string) — an oracle diff fuzz uncovered a divergence where the old
	// implementation (blindly taking [2:] as the hex digit run) parsed
	// tonumber("1X0") as 0.
	if n < len(rest) && (rest[n] == 'x' || rest[n] == 'X') {
		u, n2, ok2 := strtoulHexPrefix(rest)
		if !ok2 {
			return 0, false
		}
		for n2 < len(rest) && isLuaSpace(rest[n2]) {
			n2++
		}
		if n2 != len(rest) {
			return 0, false
		}
		return u, true
	}
	for n < len(rest) && isLuaSpace(rest[n]) {
		n++
	}
	if n != len(rest) {
		return 0, false
	}
	return f, true
}

// strtoulHexPrefix emulates the prefix parsing of C strtoul(s, &end, 16):
// optional sign + optional "0x"/"0X" prefix + longest hex digit run. Returns
// (value, bytes consumed, success); overflow saturates to ULONG_MAX (strtoul
// semantics, no errno check, matching luaO_str2d); a leading minus is applied
// via C unsigned wraparound before the cast to double.
func strtoulHexPrefix(s string) (float64, int, bool) {
	i := 0
	neg := false
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		neg = s[i] == '-'
		i++
	}
	digitStart := i
	if i+1 < len(s) && s[i] == '0' && (s[i+1] == 'x' || s[i+1] == 'X') && i+2 < len(s) && isHexDigitByte(s[i+2]) {
		i += 2
		digitStart = i
	}
	u := uint64(0)
	overflow := false
	for i < len(s) && isHexDigitByte(s[i]) {
		d := hexVal(s[i])
		if u > (^uint64(0)-uint64(d))/16 {
			overflow = true
		}
		u = u*16 + uint64(d)
		i++
	}
	if i == digitStart {
		return 0, 0, false
	}
	if overflow {
		u = ^uint64(0)
	}
	f := float64(u)
	if neg {
		f = float64(-u) // C unsigned negation wraps, then cast_num
	}
	return f, i, true
}

// hexVal returns the numeric value of a hex digit (caller already verified isHexDigitByte).
func hexVal(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	default:
		return c - 'A' + 10
	}
}

// isLuaSpace matches C isspace (default locale).
func isLuaSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\v' || c == '\f' || c == '\r'
}

// strtodPrefix parses the longest C99 strtod prefix of s. Returns (value, bytes consumed, success).
func strtodPrefix(s string) (float64, int, bool) {
	i := 0
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		i++
	}
	// inf / infinity / nan (case-insensitive; infinity matched first for longest match)
	for _, w := range [...]string{"infinity", "inf", "nan"} {
		if len(s[i:]) >= len(w) && strings.EqualFold(s[i:i+len(w)], w) {
			end := i + len(w)
			f, _ := strconv.ParseFloat(canonWord(s[:i], w), 64)
			return f, end, true
		}
	}
	// hexadecimal (0x prefix)
	if i+1 < len(s) && s[i] == '0' && (s[i+1] == 'x' || s[i+1] == 'X') {
		j := i + 2
		digits := 0
		for j < len(s) && isHexDigitByte(s[j]) {
			j++
			digits++
		}
		if j < len(s) && s[j] == '.' {
			j++
			for j < len(s) && isHexDigitByte(s[j]) {
				j++
				digits++
			}
		}
		if digits == 0 {
			// "0x" with no valid digits: strtod consumes only "0", stopping at 'x'.
			return 0, i + 1, true
		}
		end := j
		hasP := false
		if j < len(s) && (s[j] == 'p' || s[j] == 'P') {
			k := j + 1
			if k < len(s) && (s[k] == '+' || s[k] == '-') {
				k++
			}
			expDigits := 0
			for k < len(s) && s[k] >= '0' && s[k] <= '9' {
				k++
				expDigits++
			}
			if expDigits > 0 {
				end = k
				hasP = true
			}
		}
		text := s[:end]
		if !hasP {
			text += "p0" // Go's hex float requires a p exponent; strtod does not
		}
		return evalFloat(text, end)
	}
	// decimal
	j := i
	digits := 0
	for j < len(s) && s[j] >= '0' && s[j] <= '9' {
		j++
		digits++
	}
	if j < len(s) && s[j] == '.' {
		j++
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
			digits++
		}
	}
	if digits == 0 {
		return 0, 0, false
	}
	end := j
	if j < len(s) && (s[j] == 'e' || s[j] == 'E') {
		k := j + 1
		if k < len(s) && (s[k] == '+' || s[k] == '-') {
			k++
		}
		expDigits := 0
		for k < len(s) && s[k] >= '0' && s[k] <= '9' {
			k++
			expDigits++
		}
		if expDigits > 0 {
			end = k
		}
	}
	return evalFloat(s[:end], end)
}

// canonWord normalizes the inf/nan word form into a spelling Go's ParseFloat
// accepts (carrying the original sign).
func canonWord(sign, w string) string {
	if strings.EqualFold(w, "nan") {
		return "nan" // Go rejects a signed nan; the sign bit carries no meaning
	}
	return sign + "inf"
}

// evalFloat evaluates a numeric text whose shape is already valid (consumed =
// bytes consumed from the original string, possibly less than len(text): a hex
// literal with no p exponent has "p0" appended internally). Overflow is accepted
// per strtod semantics, returning ±inf (on ErrRange ParseFloat already yields
// ±Inf/0).
func evalFloat(text string, consumed int) (float64, int, bool) {
	f, err := strconv.ParseFloat(text, 64)
	if err != nil {
		if errors.Is(err, strconv.ErrRange) {
			return f, consumed, true
		}
		return 0, 0, false
	}
	return f, consumed, true
}

// isHexDigitByte reports whether c is a hex digit.
func isHexDigitByte(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}
