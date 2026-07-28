package wangshu_test

import "testing"

// TestIOHandles_SurviveGC is the regression this feature needs most.
//
// A first attempt at the standard streams called object.AllocUserdata directly and skipped
// the collector's LinkSweep, so the handles' headers carried no colour and no sweep link:
// a collectgarbage() after creating them panicked with an out-of-range arena index while
// they were still reachable from the io table. Every allocator in crescent/alloc.go pairs
// AllocX with LinkSweep + AllocCharge, and userdata is no exception -- State.NewUserdata
// exists so this cannot be skipped again.
func TestIOHandles_SurviveGC(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"type after gc", `collectgarbage("collect") return type(io.stdout)`, "userdata"},
		{"write after two gcs",
			`collectgarbage("collect") collectgarbage("collect") return tostring(io.stdout:write(""))`, "true"},
		{"metatable after gc",
			`collectgarbage("collect") return type(getmetatable(io.stdout))`, "table"},
		{"survives repeated gc",
			`local n=0 for i=1,50 do collectgarbage("collect") if io.stdout then n=n+1 end end return tostring(n)`, "50"},
		{"survives allocation pressure",
			`local t={} for i=1,10000 do t[i]={i} end collectgarbage("collect") return type(io.stdout)`, "userdata"},
	} {
		if got := runOne(t, tc.src).Str(); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestIOHandles_MatchPUC pins the shapes verified against lua5.1 5.1.5.
func TestIOHandles_MatchPUC(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		// PUC reports the streams as userdata, so a table would be visible via type().
		{"stdout type", `return type(io.stdout)`, "userdata"},
		{"stdin type", `return type(io.stdin)`, "userdata"},
		{"distinct handles", `return tostring(io.stdout == io.stderr)`, "false"},
		// 5.1's f_write returns a boolean status; returning the handle is 5.2+.
		{"write returns boolean", `return type(io.stdout:write(""))`, "boolean"},
		// Closing a standard stream RETURNS the failure rather than raising.
		{"close returns nil,msg",
			`local a, b = io.stdout:close() return tostring(a) .. "," .. b`,
			"nil,cannot close standard file"},
		// A non-handle receiver gets PUC's "FILE* expected" wording.
		{"bad receiver",
			`local ok, e = pcall(io.stdout.write, {}, "x") return tostring(ok) .. "," .. tostring(e)`,
			"false,bad argument #1 to '?' (FILE* expected, got table)"},
		// Reading a write-only handle gives PUC's errno triple.
		{"read on stdout",
			`local a, b, c = io.stdout:read() return tostring(a) .. "," .. tostring(b) .. "," .. tostring(c)`,
			"nil,Bad file descriptor,9"},
		{"lines is a function", `return type(io.stdin:lines())`, "function"},
	} {
		if got := runOne(t, tc.src).Str(); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestDebugLibrary_MatchPUC pins debug.traceback and getinfo against lua5.1.
func TestDebugLibrary_MatchPUC(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"traceback bare", `return debug.traceback():sub(1, 16)`, "stack traceback:"},
		// The message is joined to the traceback with a newline (byte 10).
		{"traceback separator", `return tostring(debug.traceback("m"):byte(2))`, "10"},
		// An EXPLICIT nil is a value and comes back unchanged -- not treated as absent.
		{"traceback nil", `return tostring(debug.traceback(nil))`, "nil"},
		{"traceback passes tables through",
			`local x = {} return tostring(debug.traceback(x) == x)`, "true"},
		{"getinfo level", `return type(debug.getinfo(1))`, "table"},
		{"getinfo past top", `return tostring(debug.getinfo(99))`, "nil"},
		{"getinfo needs an argument",
			`local ok, e = pcall(debug.getinfo) return tostring(e)`,
			"bad argument #1 to '?' (function or level expected)"},
		// Deliberately absent: they need introspection hooks the interpreter lacks.
		{"sethook absent", `return type(debug.sethook)`, "nil"},
	} {
		if got := runOne(t, tc.src).Str(); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
