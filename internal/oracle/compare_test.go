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
		// NaN sign strip
		{"-nan\tnan\n", "nan\tnan\n"},
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
