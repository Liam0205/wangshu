package wangshu_test

import (
	"testing"

	"github.com/Liam0205/wangshu/test/testutil"
)

// TestGsubReplacementEscapeMatchesPUC covers #234 and a pre-existing sibling.
//
// PUC's add_s emits ANY non-digit after '%' raw:
//
//	if (news[i] != L_ESC) luaL_addchar(b, news[i]);
//	else { i++; if (!isdigit(uchar(news[i]))) luaL_addchar(b, news[i]); ... }
//
// Two consequences we did not match. A trailing '%' makes add_s do i++ and read news[i] with NO bounds
// check, so it reads the NUL that lua_tolstring guarantees and emits a NUL byte per match -- verified by
// hexdump: gsub("aaa","a","x%") is "x\0x\0x\0". And any other non-digit is emitted literally, so
// gsub("a","a","%z") is "z" rather than an error; that half was pre-existing and diverged on the base
// tree too.
//
// This mirrors a quirk (a read one past the length) rather than a documented rule, but the oracle
// compares bytes, so matching it is what keeps every replacement ending in '%' comparable.
func TestGsubReplacementEscapeMatchesPUC(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"trailing percent emits NUL",
			`local r,n=string.gsub("aaa","a","%",1) return string.format("%q",r)..","..tostring(n)`,
			`"\000aa",1`},
		{"trailing percent after text",
			`local r,n=string.gsub("aaa","a","x%") return string.format("%q",r)..","..tostring(n)`,
			`"x\000x\000x\000",3`},
		{"percent after a capture",
			`local r,n=string.gsub("ab","(a)","%1%") return string.format("%q",r)..","..tostring(n)`,
			`"a\000b",1`},
		{"non-digit after percent is literal",
			`local r,n=string.gsub("a","a","%z") return string.format("%q",r)..","..tostring(n)`,
			`"z",1`},
		// Unchanged: %% is an escaped percent, %n is a capture, an out-of-range index still raises.
		{"escaped percent",
			`local r,n=string.gsub("aaa","a","%%") return string.format("%q",r)..","..tostring(n)`,
			`"%%%",3`},
		{"capture reference",
			`local r,n=string.gsub("ab","(a)","[%1]") return string.format("%q",r)..","..tostring(n)`,
			`"[a]b",1`},
		{"out-of-range index still raises",
			`local ok,e=pcall(string.gsub,"a","a","%2") return tostring(e)`, "invalid capture index"},
	} {
		if got := testutil.RunOne(t, tc.src).Str(); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestAddressLengthIsComparable covers #233, which normalization alone cannot reach.
//
// NormalizeOutput rewrites "table: 0x..." to a token, making printed forms comparable -- but a script
// that MEASURES one sees the length before any normalization runs: #tostring({}) is 21 on PUC (%p gives
// 12 hex digits on this platform) and 17 here (0x%08x gives 8). The oracle prelude now renders its own
// addresses at wangshu's width, which is the same "eliminate the difference at the rendering site"
// choice the NaN sign handling makes.
//
// This test pins our side of that contract: the width must stay 8, or the prelude's normalization stops
// lining up.
func TestAddressLengthIsComparable(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"table address width", `return tostring(#tostring({}))`, "17"},
		{"function address width", `return tostring(#tostring(print))`, "20"},
	} {
		if got := testutil.RunOne(t, tc.src).Str(); got != tc.want {
			t.Errorf("%s: got %q, want %q -- prelude renders the oracle side at this width", tc.name, got, tc.want)
		}
	}
}
