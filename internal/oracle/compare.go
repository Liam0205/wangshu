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
var addrRe = regexp.MustCompile(`\b(table|function|thread|userdata): 0x[0-9a-fA-F]+`)

// NormalizeOutput rewrites engine-dependent reference-value addresses before
// byte comparison. Accepted platform differences belong in CompareOutput so
// callers can distinguish them from exact equality. NaN sign spellings are
// deliberately NOT normalized here (#173): the accept-known-diff path lives
// in CompareOutput and is gated by per-span rendering evidence from prelude,
// so a literal "NAN" written by the script cannot be silently folded.
func NormalizeOutput(s string) string {
	return addrRe.ReplaceAllString(s, "${1}: 0xADDR")
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
