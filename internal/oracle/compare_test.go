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

func TestCompareOutput(t *testing.T) {
	tests := []struct {
		name          string
		oracleOutput  string
		wangshuOutput string
		want          OutputComparison
	}{
		{"equal", "ok\n", "ok\n", OutputEqual},
		{"addresses", "table: 0x1\n", "table: 0x2\n", OutputEqual},
		{"lowercase", "-nan\n", "nan\n", OutputKnownNaNSign},
		{"uppercase", "NAN\n", "-NAN\n", OutputKnownNaNSign},
		{"right width", "value=[       NAN]\n", "value=[      -NAN]\n", OutputKnownNaNSign},
		{"left width", "value=[NAN       ]\n", "value=[-NAN      ]\n", OutputKnownNaNSign},
		{"multiple", "NAN\t      -nan\n", "-NAN\t       nan\n", OutputKnownNaNSign},
		{"case differs", "NAN\n", "nan\n", OutputDifferent},
		{"alignment differs", " NAN\n", "-NAN \n", OutputDifferent},
		{"width differs", "  NAN\n", "-NAN\n", OutputDifferent},
		{"other byte differs", "NAN ok\n", "-NAN no\n", OutputDifferent},
		{"word substring", "BANANA\n", "BA-NANA\n", OutputDifferent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CompareOutput(tt.oracleOutput, tt.wangshuOutput); got != tt.want {
				t.Fatalf("CompareOutput(%q, %q) = %v, want %v",
					tt.oracleOutput, tt.wangshuOutput, got, tt.want)
			}
		})
	}
}
