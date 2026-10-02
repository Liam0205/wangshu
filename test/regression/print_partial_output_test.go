package regression

import (
	"io"
	"os"
	"testing"

	wangshu "github.com/Liam0205/wangshu"
)

// TestPrintWritesEachArgumentAsConverted covers luaB_print's order of work: it calls tostring on one
// argument and writes the result before converting the next, so output from a __tostring handler
// lands between the arguments, and when a later conversion fails the arguments before it are
// already written. print used to convert everything first and write once at the end, which put a
// handler's output before the whole line and wrote nothing at all when a conversion failed.
// print now buffers and writes out before any conversion that can run Lua code, so the cases
// cover each way that can happen: a __tostring on a table, a redefined global tostring (including
// one that fails part way), a __tostring on the shared string metatable, and tostring removed.
// Expectations are lua5.1's stdout.
func TestPrintWritesEachArgumentAsConverted(t *testing.T) {
	const src = `local o = setmetatable({}, {__tostring = function() io.write("[X]") return "obj" end})
print("a", o, "b")
local bad = setmetatable({}, {__tostring = function() return {} end})
io.write(tostring(select(2, pcall(print, "a", "b", bad, "c"))), "\n")
local boom = setmetatable({}, {__tostring = function() error("boom") end})
io.write(tostring(select(2, pcall(print, 1, boom))), "\n")
print()
print(1, nil, true)
local real = tostring
tostring = function(v) io.write("<") return real(v) end
print("p", 2)
tostring = function(v) if v == 3 then return {} end return real(v) end
io.write(tostring(select(2, pcall(print, 1, 2, 3, 4))), "\n")
tostring = real
getmetatable("").__tostring = function(s) io.write("{s}") return s end
print("m", 5)
getmetatable("").__tostring = nil
tostring = nil
io.write(tostring == nil and select(2, pcall(print, 1)) or "?", "\n")`
	const want = "a[X]\tobj\tb\n" +
		"a\tb'tostring' must return a string to 'print'\n" +
		"1x:5: boom\n" +
		"\n" +
		"1\tnil\ttrue\n" +
		"<p<\t2\n" +
		"1\t2'tostring' must return a string to 'print'\n" +
		"{s}m\t5\n" +
		"attempt to call a nil value\n"
	got := captureStdout(t, func() {
		st := wangshu.NewState(wangshu.Options{})
		prog, err := wangshu.Compile([]byte(src), "@x")
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		if _, err := prog.Run(st); err != nil {
			t.Fatalf("run: %v", err)
		}
	})
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

// TestPrintKeepsTheChecksOfItsToStringCalls covers print's shortcut for values the builtin tostring
// would only format: it skips the call, but not what the call checks. In lua5.1 each argument is a
// lua_call, so print at the deepest C level raises "C stack overflow" before writing anything; the
// shortcut wrote the line and returned. The step-budget charge for a string argument is wangshu's
// own, so its expectation is the text print gave before the shortcut (frozen, no position).
func TestPrintKeepsTheChecksOfItsToStringCalls(t *testing.T) {
	const src = `local hit, msg
local function probe(n)
  local ok, err = pcall(probe, n + 1)
  if not ok and not hit then
    hit = n
    print("x")
    io.write("after print\n")
  elseif not ok and not msg then
    msg = err
  end
end
pcall(probe, 0)
io.write(tostring(msg), "\n")`
	got := captureStdout(t, func() {
		st := wangshu.NewState(wangshu.Options{})
		prog, err := wangshu.Compile([]byte(src), "@x")
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		if _, err := prog.Run(st); err != nil {
			t.Fatalf("run: %v", err)
		}
	})
	if want := "C stack overflow\n"; got != want {
		t.Errorf("at the C limit: got %q, want %q (lua5.1)", got, want)
	}

	const budget = `local s = string.rep("a", 200000)
local ok, e = pcall(function() print(s) end)
OUT = e`
	_ = captureStdout(t, func() {
		st := wangshu.NewState(wangshu.Options{})
		st.SetStepBudget(5000)
		prog, err := wangshu.Compile([]byte(budget), "@budget.lua")
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		if _, err := prog.Run(st); err != nil {
			t.Fatalf("run: %v", err)
		}
		if got, want := st.GetGlobal("OUT").Str(), "instruction budget exceeded"; got != want {
			t.Errorf("budget: got %q, want %q", got, want)
		}
	})
}

// captureStdout runs f with os.Stdout redirected to a pipe and returns what was written. print and
// io.write write to os.Stdout directly, so this is the only way to observe their interleaving.
func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	saved := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	defer func() { os.Stdout = saved }()
	f()
	os.Stdout = saved
	_ = w.Close()
	return <-done
}
