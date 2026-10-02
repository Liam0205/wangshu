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
// Expectations are lua5.1's stdout.
func TestPrintWritesEachArgumentAsConverted(t *testing.T) {
	const src = `local o = setmetatable({}, {__tostring = function() io.write("[X]") return "obj" end})
print("a", o, "b")
local bad = setmetatable({}, {__tostring = function() return {} end})
io.write(tostring(select(2, pcall(print, "a", "b", bad, "c"))), "\n")
local boom = setmetatable({}, {__tostring = function() error("boom") end})
io.write(tostring(select(2, pcall(print, 1, boom))), "\n")
print()
print(1, nil, true)`
	const want = "a[X]\tobj\tb\n" +
		"a\tb'tostring' must return a string to 'print'\n" +
		"1x:5: boom\n" +
		"\n" +
		"1\tnil\ttrue\n"
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
