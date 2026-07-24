// compare_test.go -- pins for the output normalizer. No build tag:
// compare.go is buildable in every configuration.
package oracle

import "testing"

func TestNormalizeOutput_AddressesOnly(t *testing.T) {
	cases := []struct{ in, want string }{
		// reference-value spellings normalize
		{"table: 0x00002f70\n", "table: 0xADDR\n"},
		{"function: 0x5f1db60d7e40\n", "function: 0xADDR\n"},
		{"thread: 0x1\tuserdata: 0xdeadbeef\n", "thread: 0xADDR\tuserdata: 0xADDR\n"},
		// a script's OWN hex output must stay comparable (PR review:
		// an unanchored rule made 0x1 == 0x2)
		{"0x1\n", "0x1\n"},
		{"0x2\n", "0x2\n"},
		{"value=0xff\n", "value=0xff\n"},
		// NaN sign is classified explicitly by CompareOutput via spans,
		// never hidden here.
		{"-nan\tnan\n", "-nan\tnan\n"},
	}
	for _, c := range cases {
		if got := NormalizeOutput(c.in); got != c.want {
			t.Errorf("NormalizeOutput(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if NormalizeOutput("0x1") == NormalizeOutput("0x2") {
		t.Fatal("plain hex tokens must not be normalized into the same value")
	}
}

func TestDecodeOutput(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		output string
		spans  []NaNSpan
		ok     bool
	}{
		{"empty spans", "0\nhello", "hello", nil, true},
		{"one span", "1\n0-4\n-NAN", "-NAN", []NaNSpan{{0, 4}}, true},
		{"two spans", "2\n0-3\n8-12\nnan\ttext-NAN", "nan\ttext-NAN", []NaNSpan{{0, 3}, {8, 12}}, true},
		{"missing header", "", "", nil, false},
		{"garbage count", "x\nabc", "", nil, false},
		{"negative count", "-1\nabc", "", nil, false},
		{"malformed offset line", "1\n0-\nabc", "", nil, false},
		{"span past body", "1\n0-100\nshort", "", nil, false},
		{"non-monotonic spans", "2\n5-10\n0-4\nsome text here", "", nil, false},
		{"start after end", "1\n5-3\nabc", "", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, spans, ok := DecodeOutput(tt.in)
			if output != tt.output || ok != tt.ok {
				t.Errorf("DecodeOutput(%q) = (%q, _, %v), want (%q, _, %v)",
					tt.in, output, ok, tt.output, tt.ok)
			}
			if len(spans) != len(tt.spans) {
				t.Fatalf("DecodeOutput(%q) spans len = %d, want %d", tt.in, len(spans), len(tt.spans))
			}
			for i := range spans {
				if spans[i] != tt.spans[i] {
					t.Errorf("DecodeOutput(%q) span[%d] = %v, want %v", tt.in, i, spans[i], tt.spans[i])
				}
			}
		})
	}
}

// nanTokenSpans returns spans as prelude's string.format wrapper would
// record them: each "nan"/"NAN" run greedy-absorbs a leading '-' and
// the ASCII spaces on either side, bounded by the previous span's end.
// Test helper only.
func nanTokenSpans(s string) []NaNSpan {
	var out []NaNSpan
	scanStart := 0
	for scanStart < len(s) {
		hit := -1
		for j := scanStart; j+3 <= len(s); j++ {
			if s[j:j+3] == "nan" || s[j:j+3] == "NAN" {
				hit = j
				break
			}
		}
		if hit < 0 {
			break
		}
		tokStart := hit
		if hit > scanStart && s[hit-1] == '-' {
			tokStart = hit - 1
		}
		spanStart := tokStart
		for spanStart > scanStart && s[spanStart-1] == ' ' {
			spanStart--
		}
		spanEnd := hit + 3
		for spanEnd < len(s) && s[spanEnd] == ' ' {
			spanEnd++
		}
		out = append(out, NaNSpan{Start: spanStart, End: spanEnd})
		scanStart = spanEnd
	}
	return out
}

func TestCompareOutput(t *testing.T) {
	tests := []struct {
		name          string
		oracleOutput  string
		wangshuOutput string
		oracleSpans   []NaNSpan
		wangshuSpans  []NaNSpan
		want          OutputComparison
	}{
		// --- equal / non-NaN paths ---
		{"equal", "ok\n", "ok\n", nil, nil, OutputEqual},
		{"addresses only", "table: 0x1\n", "table: 0x2\n", nil, nil, OutputEqual},

		// --- span-anchored known NaN sign spelling (positive) ---
		{"lowercase", "-nan\n", "nan\n", []NaNSpan{{0, 4}}, []NaNSpan{{0, 3}}, OutputKnownNaNSign},
		{"uppercase", "NAN\n", "-NAN\n", []NaNSpan{{0, 3}}, []NaNSpan{{0, 4}}, OutputKnownNaNSign},
		{"right width", "value=[       NAN]\n", "value=[      -NAN]\n", []NaNSpan{{7, 17}}, []NaNSpan{{7, 17}}, OutputKnownNaNSign},
		{"left width", "value=[NAN       ]\n", "value=[-NAN      ]\n", []NaNSpan{{7, 17}}, []NaNSpan{{7, 17}}, OutputKnownNaNSign},
		{"multiple", "NAN\t      -nan\n", "-NAN\t       nan\n", []NaNSpan{{0, 3}, {4, 14}}, []NaNSpan{{0, 4}, {5, 15}}, OutputKnownNaNSign},
		{"suffix literal", "NAN0\n", "-NAN0\n", []NaNSpan{{0, 3}}, []NaNSpan{{0, 4}}, OutputKnownNaNSign},
		{"prefix literal", "0NAN\n", "0-NAN\n", []NaNSpan{{1, 4}}, []NaNSpan{{1, 5}}, OutputKnownNaNSign},
		{"format word adjacency covered by span", "BAN AN NAN\n", "BAN AN -NAN\n", []NaNSpan{{7, 10}}, []NaNSpan{{7, 11}}, OutputKnownNaNSign},
		{"script literal padding both sides", " -nan \n", " nan \n", []NaNSpan{{1, 5}}, []NaNSpan{{1, 4}}, OutputKnownNaNSign},
		{"script literal padding embedded", "prefix -nan suffix\n", "prefix nan suffix\n", []NaNSpan{{7, 11}}, []NaNSpan{{7, 10}}, OutputKnownNaNSign},
		{"symmetric leading + printf trailing", " -nan  \n", " nan   \n", []NaNSpan{{1, 7}}, []NaNSpan{{1, 7}}, OutputKnownNaNSign},
		{"symmetric trailing + printf leading", "  -nan \n", "   nan \n", []NaNSpan{{1, 7}}, []NaNSpan{{1, 7}}, OutputKnownNaNSign},

		// --- must still fail: any script-side / non-span byte differs ---
		{"plain text lacks spans", "BANANA\n", "BA-NANA\n", nil, nil, OutputDifferent},
		{"one-sided spans", "NAN\n", "-NAN\n", []NaNSpan{{0, 3}}, nil, OutputDifferent},
		{"case differs", "NAN\n", "nan\n", []NaNSpan{{0, 3}}, []NaNSpan{{0, 3}}, OutputDifferent},
		{"alignment differs", " NAN\n", "-NAN \n", []NaNSpan{{0, 4}}, []NaNSpan{{0, 5}}, OutputDifferent},
		{"asymmetric non-zero padding", "  NAN \n", " -NAN  \n", []NaNSpan{{0, 6}}, []NaNSpan{{0, 7}}, OutputDifferent},
		{"width differs", "  NAN\n", "-NAN\n", []NaNSpan{{0, 5}}, []NaNSpan{{0, 4}}, OutputDifferent},
		{"other byte differs", "NAN ok\n", "-NAN no\n", []NaNSpan{{0, 3}}, []NaNSpan{{0, 4}}, OutputDifferent},
		{"non-sign insertion", "BANANA\n", "BAXNANA\n", []NaNSpan{{3, 6}}, []NaNSpan{{4, 7}}, OutputDifferent},

		// --- reviewer-authored regressions: same run has a real NaN
		//     AND a plain-text NAN/-NAN difference the harness must
		//     NOT swallow (the sign-spelling accept-list only fires
		//     inside a matching span). Fails on the pre-span design
		//     that used an execution-level boolean flag.
		{"nan + plain-text NAN diff outside span", "nan\tBANANA\n", "-nan\tBA-NANA\n", []NaNSpan{{0, 3}}, []NaNSpan{{0, 4}}, OutputDifferent},
		{"nan + plain-text NAN diff at tail", "nan foo NAN\n", "-nan foo -NAN\n", []NaNSpan{{0, 3}}, []NaNSpan{{0, 4}}, OutputDifferent},
		{"nan gap byte differs", "-nan X\n", "nan Y\n", []NaNSpan{{0, 4}}, []NaNSpan{{0, 3}}, OutputDifferent},

		// --- sign flags and reserved-sign-column padding
		{"unsigned vs plus sign", "NAN\n", "+NAN\n", []NaNSpan{{0, 3}}, []NaNSpan{{0, 4}}, OutputKnownNaNSign},
		{"plus vs minus sign", "+NAN0\n", "-NAN0\n", []NaNSpan{{0, 4}}, []NaNSpan{{0, 4}}, OutputKnownNaNSign},
		{"reserved sign column leading", "       nan\n", "      nan\n", []NaNSpan{{0, 10}}, []NaNSpan{{0, 9}}, OutputKnownNaNSign},
		{"reserved sign column trailing", "nan       \n", "nan      \n", []NaNSpan{{0, 10}}, []NaNSpan{{0, 9}}, OutputKnownNaNSign},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CompareOutput(tt.oracleOutput, tt.wangshuOutput, tt.oracleSpans, tt.wangshuSpans); got != tt.want {
				t.Fatalf("CompareOutput(%q, %q) = %v, want %v",
					tt.oracleOutput, tt.wangshuOutput, got, tt.want)
			}
		})
	}
}

// TestCompareOutput_HelperSpans double-checks nanTokenSpans doesn't
// diverge from the intent of the tests above by re-running the "multiple"
// case with helper-generated spans.
func TestCompareOutput_HelperSpans(t *testing.T) {
	oOut, wOut := "NAN\t      -nan\n", "-NAN\t       nan\n"
	if got := CompareOutput(oOut, wOut, nanTokenSpans(oOut), nanTokenSpans(wOut)); got != OutputKnownNaNSign {
		t.Fatalf("helper spans: got %v", got)
	}
}
