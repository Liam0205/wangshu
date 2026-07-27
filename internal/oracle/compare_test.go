// compare_test.go -- pins for the output normalizer. No build tag:
// compare.go is buildable in every configuration.
package oracle

import (
	"testing"
)

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

func TestCompareOutput(t *testing.T) {
	for _, tc := range []struct {
		name      string
		oracle    string
		wangshu   string
		wantEqual bool
	}{
		{"identical", "nan\n", "nan\n", true},
		{"addresses normalize", "table: 0x1\n", "table: 0xdeadbeef\n", true},
		{"script hex stays exact", "0x1\n", "0x2\n", false},
		{"genuine divergence", "300\n", "400\n", false},
		// The oracle normalizes its own NaN rendering, so no NaN spelling
		// difference should ever reach this function. If one does, it is a real
		// divergence and must be reported: an accepted-difference path is what
		// made the leaked forms (string.len(0/0) -> 4 vs 3) impossible to handle.
		{"nan sign is not excused", "-nan\n", "nan\n", false},
		{"NAN sign is not excused", "-NAN\n", "NAN\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := CompareOutput(tc.oracle, tc.wangshu)
			if (got == OutputEqual) != tc.wantEqual {
				t.Errorf("CompareOutput(%q, %q) = %v, wantEqual=%v",
					tc.oracle, tc.wangshu, got, tc.wantEqual)
			}
		})
	}
}
