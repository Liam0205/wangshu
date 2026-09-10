package wangshu_test

import (
	"strings"
	"testing"

	"github.com/Liam0205/wangshu/test/testutil"
)

// TestGetInfoFunctionForm pins debug.getinfo(f)'s "S" fields against PUC.
//
// Found by adding the official db.lua to the suite and watching it fail on line 26 -- the first line of real
// work in that file. The previous behaviour returned a table with no `what` at all, on the reasoning that
// omitting a field beats fabricating one. That rule is right, but it had been applied to fields the
// interpreter can in fact DERIVE: a host closure is distinguishable from a Lua one at the value level, and a
// Lua function carries its proto's source and linedefined. Omitting a derivable field is not honesty, it is
// a gap -- and truncating the suite file just above the assertion is what kept it invisible.
//
// Expected values were read off a real lua5.1: getinfo(print) gives what="C", short_src="[C]",
// linedefined=-1; a Lua function gives what="Lua" with its own chunk name and first line.
func TestGetInfoFunctionForm(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{
			"host function",
			`local a = debug.getinfo(print) return a.what .. "|" .. a.short_src .. "|" .. a.linedefined`,
			"C|[C]|-1",
		},
		{
			"lua function",
			"local function f() end\nlocal a = debug.getinfo(f) return a.what .. \"|\" .. a.linedefined",
			"Lua|1",
		},
		{
			// what="main" is PUC's label for a chunk whose linedefined is 0.
			"main chunk",
			`local a = debug.getinfo(loadstring("return 1")) return a.what .. "|" .. a.linedefined`,
			"main|0",
		},
		{
			// "S" must remain selectable: asking only for "l" must not drag what/source along.
			"selector honoured",
			`local a = debug.getinfo(print, "l") return tostring(a.what) .. "|" .. tostring(a.currentline)`,
			"nil|-1",
		},
	} {
		if got := testutil.RunOne(t, tc.src).Str(); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
	// lastlinedefined and activelines: both were filed under "hook-dependent, omit rather than fabricate"
	// alongside nups/namewhat, but unlike those they need nothing beyond the proto -- LineEnd is already
	// tracked for every function, and LineInfo already maps each instruction to its line. The official
	// db.lua asserts on both, which is what surfaced the misfiling.
	if got := testutil.RunOne(t, "local function f()\n  local a = 1\n  return a\nend\n"+
		`local i = debug.getinfo(f, "S") return i.linedefined .. "|" .. i.lastlinedefined`).Str(); got != "1|4" {
		t.Errorf("linedefined|lastlinedefined: got %q, want %q", got, "1|4")
	}
	if got := testutil.RunOne(t, "local function f()\n  local a = 1\n  return a\nend\n"+
		`local i = debug.getinfo(f, "L")
return type(i.activelines) .. "|" .. tostring(i.activelines[2] ~= nil)`).Str(); got != "table|true" {
		t.Errorf("activelines: got %q, want %q", got, "table|true")
	}
	// "L" is selectable: without it activelines must be absent, as PUC does.
	if got := testutil.RunOne(t, `local function f() end local i = debug.getinfo(f, "S") return tostring(i.activelines)`).Str(); got != "nil" {
		t.Errorf("activelines leaked without the L selector: %q", got)
	}
	// A C function has no activelines even when "L" is asked for.
	if got := testutil.RunOne(t, `local i = debug.getinfo(print, "L") return tostring(i.activelines)`).Str(); got != "nil" {
		t.Errorf("activelines set for a C function: %q", got)
	}

	// short_src must be the chunk name as ChunkID renders it, not the raw "=..."/"@..." form.
	if s := testutil.RunOne(t, `local function f() end local a = debug.getinfo(f) return a.short_src`).Str(); strings.HasPrefix(s, "=") || strings.HasPrefix(s, "@") {
		t.Errorf("short_src kept the raw chunkname prefix: %q", s)
	}
}
