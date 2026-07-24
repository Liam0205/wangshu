// compare.go -- shared (no build tag) helpers for the oracle diff:
// output normalization and wangshu-side error classification. Kept
// buildable without cgo so corpus replay tooling and the default
// build's vet/lint see them.
package oracle

import (
	"regexp"
	"strconv"
	"strings"
)

// spanCountHardMax caps the number of NaN spans DecodeOutput will
// accept from a readout header, regardless of body length. The output
// cap is 1 MiB (OutputCapBytes), so a legitimate fuzz input has at
// most ~O(output size / (min NaN token width)) spans -- 128k gives an
// order of magnitude headroom over that while still bounding hostile
// input allocations to a few MB rather than GB.
const spanCountHardMax = 1 << 17

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

// NaNSpan records a byte range [Start, End) inside the accumulated
// print/io.write output that was produced by a NaN rendering path
// (print/io.write of a NaN number, or the nan/NAN tokens inside a
// string.format output fed at least one NaN argument). Offsets are
// 0-based byte offsets into the output body returned by DecodeOutput.
// Prelude emits one span per rendering event; CompareOutput uses the
// spans to allow NaN-sign spelling divergence ONLY within these ranges,
// so a script's own literal "NAN"/"-NAN" output outside every span
// stays under strict byte-equality.
type NaNSpan struct {
	Start int
	End   int
}

// DecodeOutput removes the private readout header emitted by Prelude and
// returns the raw output body plus the NaN-rendering byte spans within it.
// Header format:
//
//	"<count>\n" +
//	  <count> lines of "<off>-<end>\n" +
//	  <body>
//
// The decoder is intentionally strict: a malformed header (non-numeric
// count, malformed offset line, offsets out of range, non-monotonic
// spans, or bytes still unread once <count> spans are consumed
// arithmetically fine) returns ok=false so the caller can classify the
// run as VerdictLimit rather than silently falling back to "no
// evidence".
func DecodeOutput(readout string) (output string, spans []NaNSpan, ok bool) {
	nl := strings.IndexByte(readout, '\n')
	if nl < 0 {
		return "", nil, false
	}
	count, err := strconv.Atoi(readout[:nl])
	if err != nil || count < 0 {
		return "", nil, false
	}
	rest := readout[nl+1:]
	if count == 0 {
		return rest, nil, true
	}
	// Bound count by an upper limit derived from the remaining bytes
	// so a hostile readout (a fuzz script CAN overwrite the
	// __oracle_readout global) cannot request a giant allocation
	// before offset-line validation runs. The minimum viable span
	// header line is 3 bytes ("0-0\n"), so a valid encoding fits at
	// most len(rest)/3 spans; anything larger is definitely
	// malformed. Additionally hard-cap at spanCountHardMax to prevent
	// pathological but valid-looking readouts from OOM-ing the
	// worker even on a very long body.
	if count > spanCountHardMax || count > len(rest)/3+1 {
		return "", nil, false
	}
	spans = make([]NaNSpan, 0, count)
	prevEnd := 0
	for i := 0; i < count; i++ {
		nl := strings.IndexByte(rest, '\n')
		if nl < 0 {
			return "", nil, false
		}
		line := rest[:nl]
		dash := strings.IndexByte(line, '-')
		if dash < 0 {
			return "", nil, false
		}
		start, err := strconv.Atoi(line[:dash])
		if err != nil || start < prevEnd {
			return "", nil, false
		}
		end, err := strconv.Atoi(line[dash+1:])
		if err != nil || end < start {
			return "", nil, false
		}
		spans = append(spans, NaNSpan{Start: start, End: end})
		prevEnd = end
		rest = rest[nl+1:]
	}
	if len(spans) > 0 && spans[len(spans)-1].End > len(rest) {
		return "", nil, false
	}
	return rest, spans, true
}

// OutputComparison classifies an oracle output comparison.
type OutputComparison uint8

const (
	OutputEqual OutputComparison = iota
	OutputKnownNaNSign
	OutputDifferent
)

// CompareOutput compares captured oracle output after deterministic
// address normalization. The comparison partitions each side into
// interleaved segments driven by its NaN-rendering spans: bytes OUTSIDE
// every span must match byte-for-byte between engines (a script's own
// literal "NAN"/"-NAN" output ends up here, so genuine differences
// there still fail); bytes INSIDE a matching span pair may additionally
// differ by "known NaN sign spelling" per knownNaNSignDifference.
// IEEE 754 assigns no numeric meaning to a NaN's sign, while PUC's
// visible spelling depends on the host libc and the NaN bit pattern
// produced by the host floating-point implementation; wangshu picks a
// consistent negative rendering and the harness classifies the residual
// per-rendering divergence here.
//
// A mismatched span shape (different count, non-matching gap bytes,
// or intra-span content diverging beyond sign spelling) demotes the
// verdict to OutputDifferent so the harness fails.
func CompareOutput(oracleOutput, wangshuOutput string, oracleSpans, wangshuSpans []NaNSpan) OutputComparison {
	// Fast path: raw byte equality before touching addresses or spans.
	if oracleOutput == wangshuOutput {
		return OutputEqual
	}
	// Slow path with address normalization; NaN spans stay off this path
	// because addresses cannot appear inside a NaN token (spans are
	// print/io.write of a numeric NaN or NaN tokens produced by
	// string.format on NaN args, none of which produce reference-value
	// spellings).
	oNorm := NormalizeOutput(oracleOutput)
	wNorm := NormalizeOutput(wangshuOutput)
	if oNorm == wNorm {
		return OutputEqual
	}
	if len(oracleSpans) == 0 || len(wangshuSpans) == 0 || len(oracleSpans) != len(wangshuSpans) {
		return OutputDifferent
	}
	// Walk both raw outputs together, span by span. Gap bytes go through
	// NormalizeOutput before comparison (addresses may live there);
	// intra-span bytes compare raw so span offsets remain accurate.
	oPos, wPos := 0, 0
	signDiff := false
	for i := range oracleSpans {
		o, w := oracleSpans[i], wangshuSpans[i]
		if o.Start < oPos || o.End > len(oracleOutput) || w.Start < wPos || w.End > len(wangshuOutput) {
			return OutputDifferent
		}
		if NormalizeOutput(oracleOutput[oPos:o.Start]) != NormalizeOutput(wangshuOutput[wPos:w.Start]) {
			return OutputDifferent
		}
		oTok := oracleOutput[o.Start:o.End]
		wTok := wangshuOutput[w.Start:w.End]
		if oTok == wTok {
			oPos, wPos = o.End, w.End
			continue
		}
		if !knownNaNSignDifference(oTok, wTok) {
			return OutputDifferent
		}
		signDiff = true
		oPos, wPos = o.End, w.End
	}
	if NormalizeOutput(oracleOutput[oPos:]) != NormalizeOutput(wangshuOutput[wPos:]) {
		return OutputDifferent
	}
	if signDiff {
		return OutputKnownNaNSign
	}
	return OutputEqual
}

type nanOutputPart struct {
	literal string
	upper   bool
	// signRun is the byte sequence of '-'/'+' characters immediately
	// preceding the nan/NAN token. Empty means no sign column at
	// all. A single '-' is the common libc-emitted NaN sign; a
	// single '+' is printf's forced-sign flag on a NaN result;
	// multi-byte runs (e.g. "+-", "--") show up when a script
	// literal '-'/'+' sits adjacent to a rendered sign column. Sign
	// spelling divergence lets any signRun combination interchange
	// as long as either side's tail literal and shape line up.
	signRun  string
	leading  int
	trailing int
}

// knownNaNSignDifference accepts nan/NAN sign spellings even when a format
// conversion is adjacent to ordinary format text. It also accounts for the
// single sign column moving between the token and printf width padding. Literal
// text, token case, field alignment, and every other byte must remain identical.
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
		if x.signRun == y.signRun {
			if x.leading == y.leading && x.trailing == y.trailing {
				continue
			}
			// Both sides emit the identical sign-column bytes but
			// pad differently: one includes a reserved-but-invisible
			// sign column (glibc %<w>f on a lowercase NaN reserves
			// one column for the sign the host libc suppressed)
			// while the other spends width on the actual bytes. The
			// net effect is a padding column difference of exactly 1
			// on one side. Accept it and count as a sign-column
			// divergence for OutputKnownNaNSign.
			if len(x.signRun) == 0 && compatibleReservedSignPadding(x, y) {
				signDiff = true
				continue
			}
			return false
		}
		signDiff = true
		if !compatibleNaNPadding(x, y) {
			return false
		}
	}
	return signDiff
}

func compatibleNaNPadding(a, b nanOutputPart) bool {
	// Case B (script-owned padding): both sides carry the same literal
	// space padding around the NaN token, so the only residual byte
	// difference is the sign column itself. Covers no padding at all
	// (leading=trailing=0), symmetric script padding
	// (io.write(" ", 0/0, " ")), and mixed printf+script padding whose
	// width is preserved verbatim -- printf never emits padding on both
	// sides of the same conversion, so equal leading AND equal trailing
	// on both sides means neither side spent width on the sign.
	if a.leading == b.leading && a.trailing == b.trailing {
		return true
	}
	// Case A (printf width padding): a sign consumes one column, so for
	// a fixed field width printf compensates with one fewer space on the
	// alignment side. The opposite side (padding-free OR carrying equal
	// script padding on both engines) stays byte-identical, and the
	// alignment side's padding + token length must sum to the same
	// total width across engines.
	if a.trailing == b.trailing {
		return a.leading+nanTokenLen(a) == b.leading+nanTokenLen(b)
	}
	if a.leading == b.leading {
		return a.trailing+nanTokenLen(a) == b.trailing+nanTokenLen(b)
	}
	return false
}

func nanTokenLen(p nanOutputPart) int {
	return 3 + len(p.signRun)
}

// compatibleReservedSignPadding accepts a padding delta of exactly 1
// on either the leading or trailing side (and equal on the other),
// modelling glibc's reserved-but-invisible sign column on lowercase
// NaN vs an engine that spends no width on the missing sign. Both
// sides must have sign == 0 (unsigned NaN token) for this to fire.
func compatibleReservedSignPadding(a, b nanOutputPart) bool {
	if a.trailing == b.trailing && absDiff(a.leading, b.leading) == 1 {
		return true
	}
	if a.leading == b.leading && absDiff(a.trailing, b.trailing) == 1 {
		return true
	}
	return false
}

func absDiff(a, b int) int {
	if a >= b {
		return a - b
	}
	return b - a
}

// splitNaNOutput returns each NaN spelling together with the literal bytes
// before it and any adjacent ASCII-space padding. It deliberately does not
// require word boundaries: string.format("%E0", nan) and "0%E" render the
// conversion directly beside ordinary text. Pairwise comparison still requires
// every byte outside the optional sign and its width padding to match.
func splitNaNOutput(s string) ([]nanOutputPart, string) {
	var parts []nanOutputPart
	literalStart := 0
	for i := 0; i < len(s); {
		// Absorb any contiguous run of '-' / '+' preceding a
		// nan/NAN token as the sign column. This mirrors prelude's
		// span recording, so a script literal '+' adjacent to a
		// rendered '-' NaN sign ends up in the same span shape as
		// a script literal '-' adjacent to an unsigned NaN.
		signStart := i
		for signStart < len(s) && (s[signStart] == '-' || s[signStart] == '+') {
			signStart++
		}
		if signStart+3 > len(s) ||
			(s[signStart:signStart+3] != "nan" && s[signStart:signStart+3] != "NAN") {
			i++
			continue
		}
		tokenStart := i
		wordStart := signStart
		wordEnd := wordStart + 3
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
			signRun:  s[tokenStart:wordStart],
			leading:  tokenStart - spanStart,
			trailing: spanEnd - wordEnd,
		})
		literalStart = spanEnd
		i = spanEnd
	}
	return parts, s[literalStart:]
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
