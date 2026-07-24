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
		// NaN sign is classified explicitly by CompareOutput, not hidden here.
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
		in       string
		output   string
		evidence bool
		ok       bool
	}{
		{"0hello", "hello", false, true},
		{"1-NAN", "-NAN", true, true},
		{"", "", false, false},
		{"xdata", "", false, false},
	}
	for _, tt := range tests {
		output, evidence, ok := DecodeOutput(tt.in)
		if output != tt.output || evidence != tt.evidence || ok != tt.ok {
			t.Errorf("DecodeOutput(%q) = (%q, %v, %v), want (%q, %v, %v)",
				tt.in, output, evidence, ok, tt.output, tt.evidence, tt.ok)
		}
	}
}

func TestCompareOutput(t *testing.T) {
	tests := []struct {
		name          string
		oracleOutput  string
		wangshuOutput string
		oracleNaN     bool
		wangshuNaN    bool
		want          OutputComparison
	}{
		{"equal", "ok\n", "ok\n", false, false, OutputEqual},
		{"addresses", "table: 0x1\n", "table: 0x2\n", false, false, OutputEqual},
		{"lowercase", "-nan\n", "nan\n", true, true, OutputKnownNaNSign},
		{"uppercase", "NAN\n", "-NAN\n", true, true, OutputKnownNaNSign},
		{"right width", "value=[       NAN]\n", "value=[      -NAN]\n", true, true, OutputKnownNaNSign},
		{"left width", "value=[NAN       ]\n", "value=[-NAN      ]\n", true, true, OutputKnownNaNSign},
		{"multiple", "NAN\t      -nan\n", "-NAN\t       nan\n", true, true, OutputKnownNaNSign},
		{"suffix literal", "NAN0\n", "-NAN0\n", true, true, OutputKnownNaNSign},
		{"prefix literal", "0NAN\n", "0-NAN\n", true, true, OutputKnownNaNSign},
		{"format word adjacency", "BANANA\n", "BA-NANA\n", true, true, OutputKnownNaNSign},
		{"script literal padding both sides", " -nan \n", " nan \n", true, true, OutputKnownNaNSign},
		{"script literal padding embedded", "prefix -nan suffix\n", "prefix nan suffix\n", true, true, OutputKnownNaNSign},
		{"symmetric leading + printf trailing", " -nan  \n", " nan   \n", true, true, OutputKnownNaNSign},
		{"symmetric trailing + printf leading", "  -nan \n", "   nan \n", true, true, OutputKnownNaNSign},
		{"plain text lacks evidence", "BANANA\n", "BA-NANA\n", false, false, OutputDifferent},
		{"one-sided evidence", "NAN\n", "-NAN\n", true, false, OutputDifferent},
		{"case differs", "NAN\n", "nan\n", true, true, OutputDifferent},
		{"alignment differs", " NAN\n", "-NAN \n", true, true, OutputDifferent},
		{"width differs", "  NAN\n", "-NAN\n", true, true, OutputDifferent},
		{"other byte differs", "NAN ok\n", "-NAN no\n", true, true, OutputDifferent},
		{"non-sign insertion", "BANANA\n", "BAXNANA\n", true, true, OutputDifferent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CompareOutput(tt.oracleOutput, tt.wangshuOutput, tt.oracleNaN, tt.wangshuNaN); got != tt.want {
				t.Fatalf("CompareOutput(%q, %q) = %v, want %v",
					tt.oracleOutput, tt.wangshuOutput, got, tt.want)
			}
		})
	}
}
