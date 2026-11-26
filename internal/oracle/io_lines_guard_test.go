//go:build wangshu_oracle_cgo && cgo

package oracle

import (
	"strings"
	"testing"
)

// TestIoLinesFilenameGuard pins the prelude's handling of io.lines, in both directions.
//
// With a filename, lua5.1 opens the file and wangshu raises "not supported"; the two can never agree,
// and the oracle would be reading host files. Those calls must end in the fixed harness error on this
// side (the wangshu side runs the same prelude, so it raises the same text). Everything else must still
// reach the real io.lines, or the guard would hide the argument checks that are compared.
func TestIoLinesFilenameGuard(t *testing.T) {
	// The shared test whitelist keeps only io.write; io.lines has to survive the trim to be tested.
	keep := GlobalSet{Top: testKeep.Top, Nested: map[string][]string{}}
	for lib, names := range testKeep.Nested {
		keep.Nested[lib] = names
	}
	keep.Nested["io"] = []string{"write", "lines"}
	pre := Prelude(keep)
	const harness = "oracle-harness: io.lines with a filename disabled"

	for _, src := range []string{
		`print(pcall(io.lines, "no-such-file"))`,
		`print(pcall(io.lines, "."))`, // a directory opens on Linux: the oracle would succeed
		`print(pcall(io.lines, ""))`,
		`print(pcall(io.lines, 0))`, // luaL_checkstring takes a number as the name "0"
		`print(pcall(io.lines, 1.5, "extra"))`,
	} {
		r := Exec(src, pre, Limits{})
		if r.Verdict != VerdictOK || !strings.Contains(r.Output, harness) {
			t.Errorf("a filename reached the real io.lines: %s\n  verdict=%v err=%q out=%q", src, r.Verdict, r.Err, r.Output)
		}
	}

	for _, tc := range []struct{ src, want string }{
		{`print(type(io.lines()))`, "function\n"},
		{`print(select(2, pcall(io.lines, nil)))`, "(FILE* expected, got nil)\n"},
		{`print(select(2, pcall(io.lines, nil, "x")))`, "(FILE* expected, got nil)\n"},
		{`print(select(2, pcall(io.lines, false)))`, "(string expected, got boolean)\n"},
		{`print(select(2, pcall(io.lines, {})))`, "(string expected, got table)\n"},
		{`print(select(2, pcall(io.lines, print)))`, "(string expected, got function)\n"},
	} {
		r := Exec(tc.src, pre, Limits{})
		if r.Verdict != VerdictOK || strings.Contains(r.Output, harness) || !strings.HasSuffix(r.Output, tc.want) {
			t.Errorf("%s\n  verdict=%v err=%q\n  got       %q\n  want tail %q", tc.src, r.Verdict, r.Err, r.Output, tc.want)
		}
	}
}
