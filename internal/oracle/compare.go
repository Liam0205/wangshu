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
// callers can distinguish them from exact equality.
func NormalizeOutput(s string) string {
	return addrRe.ReplaceAllString(s, "${1}: 0xADDR")
}

// OutputComparison classifies an oracle output comparison.
type OutputComparison uint8

const (
	OutputEqual OutputComparison = iota
	OutputKnownNaNSign
	OutputDifferent
)

// CompareOutput compares captured oracle output after deterministic address
// normalization. A difference solely in the sign spelling of standalone NaN
// fields is classified as OutputKnownNaNSign. IEEE 754 gives a NaN sign no
// numerical meaning, while PUC's visible spelling depends on the host libc and
// the NaN bit pattern produced by the host floating-point implementation.
func CompareOutput(oracleOutput, wangshuOutput string) OutputComparison {
	oracleOutput = NormalizeOutput(oracleOutput)
	wangshuOutput = NormalizeOutput(wangshuOutput)
	if oracleOutput == wangshuOutput {
		return OutputEqual
	}
	if knownNaNSignDifference(oracleOutput, wangshuOutput) {
		return OutputKnownNaNSign
	}
	return OutputDifferent
}

type nanOutputPart struct {
	literal  string
	upper    bool
	negative bool
	leading  int
	trailing int
}

// knownNaNSignDifference accepts only token-aligned nan/NAN spellings. It also
// accounts for the single sign column moving between the token and printf
// width padding. Literal text, token case, field alignment, and every byte not
// adjacent to a NaN token must remain identical.
func knownNaNSignDifference(a, b string) bool {
	pa, sa := splitNaNOutput(a)
	pb, sb := splitNaNOutput(b)
	if len(pa) == 0 || len(pa) != len(pb) || sa != sb {
		return false
	}

	signDiff := false
	for i := range pa {
		x, y := pa[i], pb[i]
		if x.literal != y.literal || x.upper != y.upper {
			return false
		}
		if x.negative == y.negative {
			if x.leading != y.leading || x.trailing != y.trailing {
				return false
			}
			continue
		}
		signDiff = true
		if !compatibleNaNPadding(x, y) {
			return false
		}
	}
	return signDiff
}

func compatibleNaNPadding(a, b nanOutputPart) bool {
	// With no width padding, NAN and -NAN naturally differ by one byte.
	if a.leading == 0 && a.trailing == 0 && b.leading == 0 && b.trailing == 0 {
		return true
	}
	// A sign consumes one column. For a fixed width, printf compensates with
	// one fewer leading or trailing space, preserving total field width.
	if a.trailing == 0 && b.trailing == 0 {
		return a.leading+nanTokenLen(a) == b.leading+nanTokenLen(b)
	}
	if a.leading == 0 && b.leading == 0 {
		return a.trailing+nanTokenLen(a) == b.trailing+nanTokenLen(b)
	}
	return false
}

func nanTokenLen(p nanOutputPart) int {
	if p.negative {
		return 4
	}
	return 3
}

// splitNaNOutput returns each standalone NaN token together with the literal
// bytes before it and any adjacent ASCII-space padding. The final literal is
// returned separately. Word boundaries keep strings such as BANANA distinct.
func splitNaNOutput(s string) ([]nanOutputPart, string) {
	var parts []nanOutputPart
	literalStart := 0
	for i := 0; i < len(s); {
		negative := s[i] == '-'
		tokenStart := i
		wordStart := i
		if negative {
			wordStart++
		}
		if wordStart+3 > len(s) ||
			(s[wordStart:wordStart+3] != "nan" && s[wordStart:wordStart+3] != "NAN") {
			i++
			continue
		}
		wordEnd := wordStart + 3
		if (tokenStart > 0 && isOutputWordByte(s[tokenStart-1])) ||
			(wordEnd < len(s) && isOutputWordByte(s[wordEnd])) {
			i++
			continue
		}

		spanStart := tokenStart
		for spanStart > literalStart && s[spanStart-1] == ' ' {
			spanStart--
		}
		spanEnd := wordEnd
		for spanEnd < len(s) && s[spanEnd] == ' ' {
			spanEnd++
		}
		parts = append(parts, nanOutputPart{
			literal:  s[literalStart:spanStart],
			upper:    s[wordStart] == 'N',
			negative: negative,
			leading:  tokenStart - spanStart,
			trailing: spanEnd - wordEnd,
		})
		literalStart = spanEnd
		i = spanEnd
	}
	return parts, s[literalStart:]
}

func isOutputWordByte(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z'
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
