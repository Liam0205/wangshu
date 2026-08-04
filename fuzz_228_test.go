package wangshu_test

import "testing"

// TestUnfinishedCaptureIsReportedLazily covers #228: an unclosed capture like "(" is reported when a
// capture is MATERIALIZED, not when the match succeeds.
//
// PUC raises this from push_onecapture, and add_value only reaches it for a table replacement, a
// function replacement, or a %n expansion -- a plain string or number replacement goes to add_s,
// which copies the replacement and expands only %n. So gsub("abc", "(", "r") returns "rarbrcr", 4 on
// lua5.1 while match/find on the same pattern raise. Collecting captures eagerly made every consumer
// raise, and the fuzzer found it through gsub("", "(", 0).
//
// The table branch is separated out deliberately: it reads capture 1 unconditionally, so it must
// raise even though no %n appears. pm.lua:193 asserts exactly that, and the first version of this fix
// deferred the error past it -- the official suite caught what the oracle seed did not.
func TestUnfinishedCaptureIsReportedLazily(t *testing.T) {
	// Succeeds: no capture is ever materialized.
	for _, tc := range []struct{ name, src, want string }{
		{"count zero", `return tostring(string.gsub("","(",0))`, "0"},
		{"string replacement", `return tostring(string.gsub("abc","(","r"))`, "rarbrcr"},
		{"number replacement", `return tostring(string.gsub("ab","(",7))`, "7a7b7"},
		{"empty subject", `return tostring(string.gsub("","(","r"))`, "r"},
	} {
		if got := runOne(t, tc.src).Str(); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
	// MIXED patterns: a closed capture followed by an unclosed one. PUC's push_onecapture is
	// per-INDEX, so a %n or table-key reference to a closed capture must not see the unfinished one.
	// An audit caught this: the first fix moved the raise into the right function but kept
	// collectCaptures' whole-list granularity, so all five of these wrongly raised.
	for _, tc := range []struct{ name, src, want string }{
		{"percent-1 with trailing open", `return tostring(string.gsub("alo","(.)(","<%1>"))`, "<a><l><o>"},
		{"percent-2 with leading open", `return tostring(string.gsub("alo","((.)","<%2>"))`, "<a><l><o>"},
		{"two closed then open", `return tostring(string.gsub("ab","(.)(.)(","<%1%2>"))`, "<ab>"},
		{"table key with trailing open",
			`return tostring(string.gsub("alo","(.)(",{a="A",l="L",o="O"}))`, "ALO"},
		{"position capture then open",
			`return tostring(string.gsub("ab","()(",{[1]="X",[2]="Y",[3]="Z"}))`, "XaYbZ"},
	} {
		if got := runOne(t, tc.src).Str(); got != tc.want {
			t.Errorf("%s: got %q, want %q -- push_onecapture is per-index", tc.name, got, tc.want)
		}
	}
	// Raises: each of these materializes a capture.
	for _, tc := range []struct{ name, src string }{
		{"function replacement", `local ok=pcall(string.gsub,"alo","(.",print) return tostring(ok)`},
		{"table replacement", `local ok=pcall(string.gsub,"alo","(.",{}) return tostring(ok)`},
		{"percent-n expansion", `local ok=pcall(string.gsub,"abc","(","%1") return tostring(ok)`},
		{"match", `local ok=pcall(string.match,"abc","(") return tostring(ok)`},
		{"find", `local ok=pcall(string.find,"abc","(") return tostring(ok)`},
		{"gmatch", `local ok=pcall(function() for _ in ("abc"):gmatch("(") do end end) return tostring(ok)`},
		// A function replacement reads ALL captures, so a mixed pattern DOES raise for it -- the
		// per-index rule applies to the paths that read one index, not to every path.
		{"function replacement, mixed", `local ok=pcall(string.gsub,"alo","(.)(",print) return tostring(ok)`},
		{"out-of-range index", `local ok=pcall(string.gsub,"alo","(.)","%2") return tostring(ok)`},
	} {
		if got := runOne(t, tc.src).Str(); got != "false" {
			t.Errorf("%s: got %q, want \"false\" -- materializing a capture must raise", tc.name, got)
		}
	}
}

// TestGsubRaisesAtTheFirstOffendingReference pins WHICH error a multi-%n template reports.
//
// PUC's add_s scans the replacement left to right and raises on the first offending reference, so
// gsub("ab", "(", "%1%2") is "unfinished capture" -- %1 is reached first -- and not the
// "invalid capture index %2" that the later reference would give. Recording capsErr and testing it only
// after the whole template was scanned inverted that, and no suite caught it: none compares gsub error
// CLASSES across multi-%n templates, so the check has to be here.
func TestGsubRaisesAtTheFirstOffendingReference(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"unfinished before out-of-range",
			`local ok,e=pcall(string.gsub,"ab","(","%1%2") return tostring(e)`, "unfinished capture"},
		{"unfinished before out-of-range, mixed pattern",
			`local ok,e=pcall(string.gsub,"ab","(.)(","%2%3") return tostring(e)`, "unfinished capture"},
		// The reverse order must still report the index error, so this is not just "always report
		// unfinished".
		{"out-of-range alone",
			`local ok,e=pcall(string.gsub,"ab","(.)","%2") return tostring(e)`, "invalid capture index %2"},
		{"out-of-range with no captures",
			`local ok,e=pcall(string.gsub,"ab","%a","%2") return tostring(e)`, "invalid capture index %2"},
	} {
		if got := runOne(t, tc.src).Str(); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
	// Whole-match capture 1 is memoized like any other index: many references must intern once and
	// still produce the right text.
	for _, tc := range []struct{ name, src, want string }{
		{"repeated whole-match reference", `return tostring(string.gsub("ab","%a","%1%1%1"))`, "aaabbb"},
		{"repeated explicit capture", `return tostring(string.gsub("ab","(%a)","%1%1"))`, "aabb"},
		{"percent-0 twice", `return tostring(string.gsub("ab","%a","%0%0"))`, "aabb"},
	} {
		if got := runOne(t, tc.src).Str(); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
