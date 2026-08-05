package oracle

import "testing"

// TestNormalizeAddrPrefixValidation covers #232 and the three anchoring attempts that failed before it.
//
// The type prefix cannot be asserted zero-width in RE2, so the address body is matched alone and the
// preceding text is checked in Go. That removes both failure modes of the earlier tries: nothing is
// consumed, so adjacent addresses are independent, and the check is exactly "is this one of tostring's
// reference spellings" rather than \b's approximation of it.
func TestNormalizeAddrPrefixValidation(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"plain", "function: 0xabc123\n", "function: 0xADDR\n"},
		// #232: io.write emits with no newline, gluing a digit onto the prefix.
		{"digit abuts", "0function: 0xabc123\n", "0function: 0xADDR\n"},
		// The second failed attempt only handled digits; a letter still blocked it.
		{"letter abuts", "xfunction: 0xabc123\n", "xfunction: 0xADDR\n"},
		{"table", "0table: 0xdead\n", "0table: 0xADDR\n"},
		{"thread", "thread: 0x99\n", "thread: 0xADDR\n"},
		{"userdata", "userdata: 0x99\n", "userdata: 0xADDR\n"},
		{"file handle", "file (0x1234)\n", "file (0xADDR)\n"},
		{"file handle abutted", "0file (0x1234)\n", "0file (0xADDR)\n"},
		// The third failure: consuming the leading char let one anchor serve two matches.
		{"adjacent addresses", "file (0x11)file (0x22)\n", "file (0xADDR)file (0xADDR)\n"},
		{"two on one line", "table: 0x11 function: 0x22\n", "table: 0xADDR function: 0xADDR\n"},
		{"same address twice", "table: 0x11 table: 0x11\n", "table: 0xADDR table: 0xADDR\n"},
		{"at start of output", "table: 0x11", "table: 0xADDR"},
		// A script's own hex must stay comparable -- the reason the anchor exists.
		{"bare hex", "0x1\n0x2\n", "0x1\n0x2\n"},
		// "myfunction: 0x1" and "xfunction: 0x..." are the same shape, so this one normalizes too.
		// Harmless: a script computes its hex identically on both engines, so both sides collapse to
		// the same token; see the comment in compare.go.
		{"longer word tail also normalizes", "myfunction: 0x1\n", "myfunction: 0xADDR\n"},
		{"formatted hex", "value 0xff\n", "value 0xff\n"},
		{"hex after colon-space", "n: 0x5\n", "n: 0x5\n"},
	} {
		if got := NormalizeOutput(tc.in); got != tc.want {
			t.Errorf("%s: NormalizeOutput(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}
