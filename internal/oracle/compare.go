// compare.go -- shared (no build tag) helpers for the oracle diff:
// output normalization and wangshu-side error classification. Kept
// buildable without cgo so corpus replay tooling and the default
// build's vet/lint see them.
package oracle

import (
	"regexp"
	"strings"
)

// addrRe matches ONLY the reference-value spellings tostring emits
// ("table: 0x...", "function: 0x...", "thread: 0x...",
// "userdata: 0x..."): the two engines print real, necessarily
// different addresses there. The type-prefix anchor keeps a script's
// own hex output (string.format("0x%x", n), plain "0x1" literals)
// fully comparable -- an unanchored 0x[0-9a-f]+ rule would normalize
// 0x1 and 0x2 to the same token and mask genuine value divergences
// (PR review finding).
// file (0x...) is included: PUC's file handles carry a __tostring rendering that form, and
// leaving it out made print(io.stdout) a guaranteed divergence once the standard streams
// existed.
// addrRe matches the address BODY only; the type prefix is validated inside NormalizeOutput.
//
// Three anchoring attempts failed before this one, and the reason is worth keeping. \b does not match
// between a word char and a letter, so `io.write(0) print(print)` produced "0function: 0x..." whose
// address escaped normalization entirely (#232). Replacing \b with a consumed [^A-Za-z] fixed only that
// digit case -- a LETTER still blocked it (`io.write("x")`), and consuming the character meant two
// adjacent addresses shared one anchor, so the second escaped.
//
// RE2 has no lookbehind, so the prefix cannot be asserted zero-width in the pattern. Matching the
// address alone and checking what precedes it in Go code has neither problem: nothing is consumed, so
// adjacent matches are independent, and the check can be exactly "is this one of tostring's reference
// spellings" rather than an approximation of it.
var addrRe = regexp.MustCompile(`0x[0-9a-fA-F]+`)

// refPrefixes are the spellings tostring emits for reference values. A script's own hex
// (string.format("0x%x", n), a plain 0x1 literal) matches none of them and stays comparable -- the
// reason an anchor exists at all.
var refPrefixes = []string{"table: ", "function: ", "thread: ", "userdata: ", "file ("}

// NormalizeOutput rewrites engine-dependent reference-value addresses before
// byte comparison. Accepted platform differences belong in CompareOutput so
// callers can distinguish them from exact equality. NaN sign spellings are
// deliberately NOT normalized here (#173): the accept-known-diff path lives
// NaN needs no handling here at all: the oracle normalizes its own NaN
// rendering (lua515.c), so both engines emit the same bytes.
func NormalizeOutput(s string) string {
	locs := addrRe.FindAllStringIndex(s, -1)
	if len(locs) == 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	prev := 0
	for _, loc := range locs {
		start, end := loc[0], loc[1]
		b.WriteString(s[prev:start])
		// Decide per occurrence from the text immediately before it: FindAllStringIndex gives real
		// offsets, so repeated and adjacent addresses are each judged on their own.
		head := s[:start]
		isRef := false
		for _, p := range refPrefixes {
			if !strings.HasSuffix(head, p) {
				continue
			}
			// No further test on what precedes the prefix, deliberately.
			//
			// "xfunction: 0x..." (io.write("x") then print(print)) and "myfunction: 0x1" (a script's
			// own text) are textually identical in shape, so no local context can separate them --
			// the information is not in the string. The two failure modes are not symmetric:
			// refusing to normalize makes every io.write-then-print-a-reference script a GUARANTEED
			// divergence, while normalizing a script's own look-alike is harmless, because a script
			// computes its hex identically on both engines and both sides therefore collapse to the
			// same token. A mask could only bite if the two engines computed DIFFERENT hex for the
			// same expression, which is a divergence we would want surfaced by its own value, not by
			// its rendering.
			isRef = true
			break
		}
		if isRef {
			b.WriteString("0xADDR")
		} else {
			b.WriteString(s[start:end])
		}
		prev = end
	}
	b.WriteString(s[prev:])
	return b.String()
}

// OutputComparison classifies a captured-output comparison.
type OutputComparison int

const (
	// OutputEqual means the two sides are byte-identical after address
	// normalization.
	OutputEqual OutputComparison = iota
	// OutputDifferent means they are not.
	OutputDifferent
)

// CompareOutput compares the two engines' captured output.
//
// There is no accepted-difference path. The one platform difference that used to
// need one -- glibc rendering a NaN's sign bit, so 0/0 printed "-nan" on the
// oracle side and "nan" on wangshu's -- is now removed where it is produced, by
// the oracle's own number formatting (internal/oracle/lua515.c). Comparing is
// therefore exact again, up to reference-value addresses.
//
// That matters beyond simplicity. A sign byte inside a string is ordinary data:
// '#', '==', string.sub, '..' and arithmetic move it anywhere, turning
// string.len(0/0) into 4 against 3 and string.len(0/0)*100 into 400 against 300
// -- differences with no NaN token left in the output for an exemption to anchor
// to. Every rule that tried to recognise them downstream had to read some
// quantity the two engines disagreed about, which is why none of them could be
// made symmetric.
func CompareOutput(oracleOutput, wangshuOutput string) OutputComparison {
	if oracleOutput == wangshuOutput {
		return OutputEqual
	}
	if NormalizeOutput(oracleOutput) == NormalizeOutput(wangshuOutput) {
		return OutputEqual
	}
	return OutputDifferent
}

func WangshuLimitError(msg string) bool {
	return strings.Contains(msg, "instruction budget exceeded") ||
		strings.Contains(msg, LimitSentinel) ||
		strings.Contains(msg, "not enough memory") ||
		strings.Contains(msg, "internal VM panic: arena:")
}

// SkipClassError reports whether an error message (either side) is a
// guard whose trip point is an implementation constant rather than a
// Lua-5.1 semantic: recursion/nesting depth (Go segmented stacks vs C
// stack; counting granularity differs), codegen complexity ceilings
// (register allocation differences shift the exact trip input), and
// resource limits (WangshuLimitError). Class comparisons must skip
// when either side hits one -- near the shared nominal thresholds
// (200 syntax levels, 200 C calls, ...) the engines legitimately trip
// a few inputs apart.
func SkipClassError(msg string) bool {
	return WangshuLimitError(msg) ||
		strings.Contains(msg, "too many syntax levels") ||
		strings.Contains(msg, "stack overflow") || // covers "C stack overflow"
		strings.Contains(msg, "too complex") || // pattern + "function or expression too complex"
		strings.Contains(msg, "constant table overflow") ||
		strings.Contains(msg, "too many local variables") ||
		strings.Contains(msg, "too many upvalues") ||
		strings.Contains(msg, "has more than") // "...has more than 200 local variables"-family
}
