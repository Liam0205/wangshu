package oracle

import "testing"

// TestNormalizeAddrIsNotWordAnchored covers #232: io.write emits without a newline, so
// `io.write(0) print(print)` produces "0function: 0x...". The old \b anchor did not match between the
// word char "0" and "function", so the address escaped normalization and every such script was a
// guaranteed divergence. The anchor is now "not a letter", which still keeps a script own hex
// comparable -- the reason the anchor exists at all.
func TestNormalizeAddrIsNotWordAnchored(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"function: 0xabc123\n", "function: 0xADDR\n"},
		{"0function: 0xabc123\n", "0function: 0xADDR\n"},
		{"table: 0xdead\n", "table: 0xADDR\n"},
		{"0table: 0xdead\n", "0table: 0xADDR\n"},
		{"file (0x1234)\n", "file (0xADDR)\n"},
		{"0file (0x1234)\n", "0file (0xADDR)\n"},
		// a script's own hex must stay comparable
		{"0x1\n", "0x1\n"},
		{"0x2\n", "0x2\n"},
		{"myfunction: 0x1\n", "myfunction: 0x1\n"},
		{"tostring gives 0xff\n", "tostring gives 0xff\n"},
	} {
		if got := NormalizeOutput(tc.in); got != tc.want {
			t.Errorf("NormalizeOutput(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
