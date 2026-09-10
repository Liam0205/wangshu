package api_test

import (
	"strings"
	"testing"

	"github.com/Liam0205/wangshu/test/testutil"
)

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
		if got := testutil.RunOne(t, tc.src).Str(); got != tc.want {
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
		if got := testutil.RunOne(t, tc.src).Str(); got != tc.want {
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
		if got := testutil.RunOne(t, tc.src).Str(); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestIOHandles_StreamKindRespected pins that the methods check WHICH stream they are on.
//
// Checking only "is this a handle" let io.stdin:write fall through to stdout -- which both
// gave the wrong answer and escaped the differential harness's capture wrapper, since that
// only intercepts the stdout/stderr handles. io.stdout:lines() was worse: it returned an
// iterator that read STDIN, handing back someone else's input rather than failing.
func TestIOHandles_StreamKindRespected(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"write to stdin fails",
			`local a, b, c = io.stdin:write("W") return tostring(a) .. "," .. tostring(b) .. "," .. tostring(c)`,
			"nil,Bad file descriptor,9"},
		{"lines on stdout fails",
			`local ok, e = pcall(io.stdout:lines()) return tostring(ok) .. "," .. tostring(e)`,
			"false,Bad file descriptor"},
		// PUC's file metatable has __tostring and NO __metatable.
		{"tostring form", `return tostring(io.stdout):sub(1, 6)`, "file ("},
		{"no __metatable", `return tostring(getmetatable(io.stdout).__metatable)`, "nil"},
	} {
		if got := testutil.RunOne(t, tc.src).Str(); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestDebugGetInfo_NoFabricatedFields pins that getinfo never FABRICATES a field.
//
// It used to also pin that the function form OMITS what/source, which was the fix for an early version
// that hardcoded what="Lua" with source="=[C]" -- mislabelling both kinds. Omitting was the right response
// to fabricating, but it overshot: those fields are DERIVABLE (a host closure is distinguishable from a Lua
// one at the value level, and a Lua function carries its proto's source), and PUC answers them. The official
// db.lua asserts exactly that.
//
// So the rule this test guards is "derive or omit, never invent", and the derived cases now live in
// TestGetInfoFunctionForm. What remains here is that the values are CORRECT per kind, which is what the
// original defect got wrong.
func TestDebugGetInfo_NoFabricatedFields(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		// A host function must not be labelled Lua, which is the mislabelling that started this.
		{"function form labels C correctly", `return tostring(debug.getinfo(print).what)`, "C"},
		{"function form sources C correctly", `return tostring(debug.getinfo(print).source)`, "=[C]"},
		{"function form labels Lua correctly",
			"local function f() end return tostring(debug.getinfo(f).what)", "Lua"},
		{"function form has func", `return type(debug.getinfo(print).func)`, "function"},
		{"level form has func", `return type(debug.getinfo(1).func)`, "function"},
		// Level 0 is getinfo itself, a C function -- 0 is a valid level, not past the top.
		{"level zero is a table", `return type(debug.getinfo(0))`, "table"},
		{"level zero is C", `return tostring(debug.getinfo(0).what)`, "C"},
	} {
		if got := testutil.RunOne(t, tc.src).Str(); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestIOOmissions_HaveAnEnforcer pins what the io and debug libraries deliberately do NOT
// provide, so the exemption entries are not prose alone.
//
// Without this the omissions live only as text in corners_test.go, and nothing stops them being
// half-implemented later or refiled by the fuzzer as a divergence.
func TestIOOmissions_HaveAnEnforcer(t *testing.T) {
	for _, name := range []string{"open", "popen", "tmpfile", "close", "input", "output"} {
		src := `return tostring(io.` + name + `)`
		if got := testutil.RunOne(t, src).Str(); got != "nil" {
			t.Errorf("io.%s = %q, want nil (registered as a gap; provide it deliberately or update the exemption)", name, got)
		}
	}
	// The file methods that need a real file are absent from the handle metatable too.
	for _, name := range []string{"seek", "setvbuf"} {
		src := `return tostring(io.stdout.` + name + `)`
		if got := testutil.RunOne(t, src).Str(); got != "nil" {
			t.Errorf("file:%s = %q, want nil (registered as a gap)", name, got)
		}
	}
	for _, name := range []string{"sethook", "gethook", "getlocal", "setlocal", "getupvalue", "setupvalue", "getregistry"} {
		src := `return tostring(debug.` + name + `)`
		if got := testutil.RunOne(t, src).Str(); got != "nil" {
			t.Errorf("debug.%s = %q, want nil (registered as a gap)", name, got)
		}
	}
}

// TestDebugGetInfo_FieldsMatchPUC pins the fields that a differential comparison can see.
//
// getinfo entered the compared surface as soon as it existed, because the fuzz target
// enumerates its whitelist from wangshu's live globals -- so a field nobody had checked against
// lua5.1 was being diffed. what and source were both wrong and neither had a test.
func TestDebugGetInfo_FieldsMatchPUC(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		// The main chunk is "main", not "Lua".
		{"main chunk what", `return debug.getinfo(1).what`, "main"},
		{"function what", `local f = function() return debug.getinfo(1).what end return f()`, "Lua"},
		// source is the chunkname exactly as the loader stored it -- PUC does NOT synthesize a
		// marker, so a bare name stays bare. short_src is the display form, so they differ.
		{"source is the raw chunkname", `return debug.getinfo(1).source`, "test"},
		{"source differs from short_src",
			`local i = debug.getinfo(1) return tostring(i.source ~= i.short_src)`, "true"},
		// linedefined is 0 for a chunk, including a loadstring chunk called as a function.
		{"main chunk linedefined", `return tostring(debug.getinfo(1).linedefined)`, "0"},
		{"loadstring chunk is main",
			`local f = loadstring("return debug.getinfo(1).what") return f()`, "main"},
		{"function linedefined is its line",
			`local g = function() return debug.getinfo(1).linedefined end local r = g() return tostring(r)`, "1"},
		// The what selector filters the fields, and an unknown letter raises.
		{"selector filters", `local i = debug.getinfo(1, "l") local n = 0 for _ in pairs(i) do n = n + 1 end return tostring(n)`, "1"},
		{"bad selector raises",
			`local ok, e = pcall(debug.getinfo, 1, "Z") return tostring(e)`,
			"bad argument #2 to '?' (invalid option)"},
		// The level goes through luaL_checkint narrowing, so a fraction truncates.
		{"fractional level", `return type(debug.getinfo(0.5))`, "table"},
		{"short_src is display form", `return debug.getinfo(1).short_src`, `[string "test"]`},
	} {
		if got := testutil.RunOne(t, tc.src).Str(); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestDebugGetInfo_HostBoundaryLevel pins that the level one past the outermost Lua frame
// reports what="C", and that anything beyond it is nil.
//
// wangshu does not push host frames onto cis, so that level had no frame and returned nil where
// lua5.1 reports the C function that called the chunk. Reporting it is not a fabricated field:
// the entry frame is marked, so "one past it" is a known host boundary.
func TestDebugGetInfo_HostBoundaryLevel(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"host boundary is a table", `return type(debug.getinfo(2))`, "table"},
		{"host boundary is C", `return debug.getinfo(2).what`, "C"},
		{"beyond it is nil", `return tostring(debug.getinfo(3))`, "nil"},
	} {
		if got := testutil.RunOne(t, tc.src).Str(); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestDebugGetInfo_LevelsCountCAndTailFrames pins that a debug level names the same frame PUC
// names, which means counting the frames wangshu does not push onto cis.
//
// Indexing cis directly by ciDepth-level skips them, and that reorders everything above the
// first one: a Lua callback invoked from table.foreach gave "Lua,Lua,main,C" where PUC gives
// "Lua,Lua,C,main", so what/func/source/currentline all named the wrong function from level 3 up.
// callInfo.hostFrames and tailDepth already record both counts -- they were added for error()'s
// level walk -- so this is a walk over existing bookkeeping rather than new state.
func TestDebugGetInfo_LevelsCountCAndTailFrames(t *testing.T) {
	const chain = `local function chain(n)
  local r = {}
  for i = 1, n do
    local info = debug.getinfo(i)
    r[i] = info and tostring(info.what) or "nil"
  end
  return table.concat(r, ",")
end
`
	for _, tc := range []struct{ name, src, want string }{
		// A C frame between two Lua frames must appear in its real position.
		{"host callback", chain + `local out
table.foreach({1}, function() out = chain(4) end)
return out`, "Lua,Lua,C,main"},
		{"sort comparator", chain + `local out
table.sort({3, 1, 2}, function(a, b) if not out then out = chain(5) end return a < b end)
return out`, "Lua,Lua,C,main,C"},
		// A tail call leaves a pseudo-frame PUC reports as what="tail". The harness's own
		// `return` is itself a tail call, so this chunk has two of them -- verified against
		// lua5.1 with the same source shape rather than a similar-looking one.
		{"tail pseudo-frame", chain + `local f = function() return chain(4) end
local out = f()
return out`, "Lua,tail,main,C"},
		// A coroutine's stack ends at its own bottom: resume is on another thread, so there
		// is no host frame to report.
		{"coroutine stack ends", chain + `local co = coroutine.wrap(function() return chain(3) end)
return co()`, "Lua,tail,nil"},
	} {
		if got := testutil.RunOne(t, tc.src).Str(); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestDebugTraceback_LevelAndHostFrames pins that traceback uses the SAME frame model as getinfo.
//
// The two were fixed separately: getinfo went through resolveLevel while traceback still
// subtracted from ciDepth, so a level taken from inside table.foreach or a sort comparator landed
// on the wrong frame, and buildTraceback omitted the C frame entirely. Three readers of this stack
// now share one model -- error()'s level walk, resolveLevel, and buildTraceback.
func TestDebugTraceback_LevelAndHostFrames(t *testing.T) {
	// A traceback taken inside a host callback must SHOW the C frame, in position.
	src := `local function inner() return (debug.traceback("M", 1):gsub("\n", " | ")) end
local out
table.foreach({1}, function() out = inner() end)
return out`
	got := testutil.RunOne(t, src).Str()
	for _, want := range []string{"stack traceback:", "[C]: in ?", "in main chunk"} {
		if !strings.Contains(got, want) {
			t.Errorf("traceback missing %q; got %q", want, got)
		}
	}
	// The C frame must sit BETWEEN the Lua frames and the main chunk, as PUC has it.
	ci, mi := strings.Index(got, "[C]: in ?"), strings.Index(got, "in main chunk")
	if ci < 0 || mi < 0 || ci > mi {
		t.Errorf("C frame is not above the main chunk: %q", got)
	}
	// A non-number level is IGNORED, matching lua_isnumber rather than luaL_optint.
	if r := testutil.RunOne(t, `local ok = pcall(debug.traceback, "m", {}) return tostring(ok)`).Str(); r != "true" {
		t.Errorf("non-number level should be ignored, got pcall=%s", r)
	}
}
