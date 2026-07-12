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

// NormalizeOutput rewrites engine-dependent-but-semantically-equal
// spellings before byte comparison:
//   - reference-value addresses -> "<type>: 0xADDR" (see addrRe);
//   - "-nan" -> "nan": C's %g prints the sign bit of a NaN, and the
//     default quiet NaN of x86 arithmetic is negative, so PUC on
//     glibc prints "-nan" where wangshu (and PUC on other libcs)
//     prints "nan". IEEE 754 assigns no meaning to a NaN's sign.
func NormalizeOutput(s string) string {
	s = addrRe.ReplaceAllString(s, "${1}: 0xADDR")
	s = strings.ReplaceAll(s, "-nan", "nan")
	return s
}

// WangshuLimitError reports whether a wangshu-side error message is a
// resource limit rather than a semantic result -- the wangshu mirror
// of VerdictLimit. Budget/allocation limits are deliberately NOT
// aligned between the engines (back-edge budget vs instruction hook,
// arena cap vs byte-accounted allocator), so any of them firing makes
// the run non-comparable.
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
