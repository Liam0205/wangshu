package regression

import (
	"testing"

	wangshu "github.com/Liam0205/wangshu"
)

// TestTracebackMatchesDbErrorfb covers #279: tracebacks are laid out as ldblib.c's db_errorfb prints them,
// with names from getfuncname (the CALLER's current CALL/TAILCALL/TFORLOOP instruction), "<source:line>"
// for an unnamed function, "[C]: in function 'name'" / "[C]: ?" for host frames, "(tail call): ?" for
// a tail call's vanished caller, the closing "[C]: ?" for the host that ran the chunk on the main thread,
// and the LEVELS1/LEVELS2 "..." elision. The old renderer printed every Lua frame as "in function", host
// frames as "[C]: in ?", tail calls in 5.2's "(...tail calls...)" form, and never elided.
//
// Each case runs as-is and with every function force-promoted (a no-op in the P1 build), since the P3/P4
// call helpers record the caller's pc that the names are read from. Expectations are lua5.1's, from the
// same source run as a file named "x".
func TestTracebackMatchesDbErrorfb(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"named local frames and the closing [C]",
			`local function leaf() return debug.traceback("here") end
local function f() return (leaf()) end
OUT = f()`,
			`here
stack traceback:
	x:1: in function 'leaf'
	x:2: in function 'f'
	x:3: in main chunk
	[C]: ?`},
		{"C frame named from its call site; callback unnamed",
			`local out
table.sort({2, 1}, function(a, b) out = out or debug.traceback("cmp") return a < b end)
OUT = out`,
			`cmp
stack traceback:
	x:2: in function <x:2>
	[C]: in function 'sort'
	x:2: in main chunk
	[C]: ?`},
		{"tail call pseudo-frames",
			`local out
local function t4() out = debug.traceback("chain") end
local function t3() return t4() end
local function t2() return t3() end
local function t1() t2() end
t1()
OUT = out`,
			`chain
stack traceback:
	x:2: in function <x:2>
	(tail call): ?
	(tail call): ?
	x:5: in function 't1'
	x:6: in main chunk
	[C]: ?`},
		{"inside a coroutine: no C frame below the body",
			`local out
local co = coroutine.create(function() out = debug.traceback("co") end)
coroutine.resume(co)
OUT = out`,
			`co
stack traceback:
	x:2: in function <x:2>`},
		{"a coroutine resumed through pcall still has no C frame below its body",
			`local out
local co = coroutine.create(function() out = debug.traceback("co") end)
pcall(coroutine.resume, co)
OUT = out`,
			`co
stack traceback:
	x:2: in function <x:2>`},
		{"getinfo past a pcall-resumed coroutine's body is nil",
			`local co = coroutine.create(function() OUT = tostring(debug.getinfo(2, "S")) end)
pcall(coroutine.resume, co)`,
			`nil`},
		{"metamethod handler is unnamed",
			`local t = setmetatable({}, {__index = function() return debug.traceback("idx") end})
OUT = t.foo`,
			`idx
stack traceback:
	x:1: in function <x:1>
	x:2: in main chunk
	[C]: ?`},
		{"for generator",
			`local out
for k in function() out = debug.traceback("iter") return nil end do end
OUT = out`,
			`iter
stack traceback:
	x:2: in function '(for generator)'
	x:2: in main chunk
	[C]: ?`},
		{"method and field names",
			`local obj = {m = function(self) return debug.traceback("meth") end}
OUT = obj:m()`,
			`meth
stack traceback:
	x:1: in function 'm'
	x:2: in main chunk
	[C]: ?`},
		{"non-constant key is '?'",
			`local t = {function() return debug.traceback("k") end}
local i = 1
OUT = t[i]()`,
			`k
stack traceback:
	x:1: in function '?'
	x:3: in main chunk
	[C]: ?`},
		{"level 0 is traceback itself",
			`OUT = debug.traceback("zero", 0)`,
			`zero
stack traceback:
	[C]: in function 'traceback'
	x:1: in main chunk
	[C]: ?`},
		{"through pcall",
			`local ok, s = pcall(debug.traceback, "p")
OUT = s`,
			`p
stack traceback:
	[C]: in function 'pcall'
	x:1: in main chunk
	[C]: ?`},
		{"negative level",
			`OUT = debug.traceback("neg", -1)`,
			`neg
stack traceback:
	(tail call): ?
	[C]: in function 'traceback'
	x:1: in main chunk
	[C]: ?`},
		{"level past the stack",
			`OUT = debug.traceback("far", 50)`,
			`far
stack traceback:`},
		{"deep stack is elided",
			`local function deep(n) if n == 0 then return debug.traceback("deep") end local r = deep(n - 1) return r end
OUT = deep(30)`,
			`deep
stack traceback:
	x:1: in function 'deep'
	x:1: in function 'deep'
	x:1: in function 'deep'
	x:1: in function 'deep'
	x:1: in function 'deep'
	x:1: in function 'deep'
	x:1: in function 'deep'
	x:1: in function 'deep'
	x:1: in function 'deep'
	x:1: in function 'deep'
	x:1: in function 'deep'
	...
	x:1: in function 'deep'
	x:1: in function 'deep'
	x:1: in function 'deep'
	x:1: in function 'deep'
	x:1: in function 'deep'
	x:1: in function 'deep'
	x:1: in function 'deep'
	x:1: in function 'deep'
	x:2: in main chunk
	[C]: ?`},
	} {
		for _, force := range []bool{false, true} {
			st := runTracebackCase(t, tc.src, force)
			if st == nil {
				continue
			}
			if got := st.GetGlobal("OUT").Str(); got != tc.want {
				t.Errorf("%s (force=%v):\n got %q\nwant %q", tc.name, force, got, tc.want)
			}
		}
	}
}

// TestUncaughtErrorTracebackIsTakenAtTheRaisePoint covers the other half of #279: the traceback attached to
// an error nothing catches used to be built after execute returned, by which point every host boundary the
// error crossed had already truncated the frames above it -- a comparator raising inside table.sort showed
// neither the comparator nor sort. It is now taken where the error first appears, while no pcall/xpcall or
// coroutine.resume is active, laid out as lua.c's handler prints it (the raising host function on top, as
// "[C]: in function 'error'"). Expectations are lua5.1's stderr minus the "lua5.1: " prefix.
func TestUncaughtErrorTracebackIsTakenAtTheRaisePoint(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"host raiser: error",
			`local function f() error("boom") end
f()`,
			`x:1: boom
stack traceback:
	[C]: in function 'error'
	x:1: in function 'f'
	x:2: in main chunk
	[C]: ?`},
		{"runtime error",
			`local t = nil
local function g() return t.x end
g()`,
			`x:2: attempt to index upvalue 't' (a nil value)
stack traceback:
	x:2: in function 'g'
	x:3: in main chunk
	[C]: ?`},
		{"comparator raising inside sort",
			`local function f() table.sort({3, 2, 1}, function(a, b) error("cmp") end) end
f()`,
			`x:1: cmp
stack traceback:
	[C]: in function 'error'
	x:1: in function <x:1>
	[C]: in function 'sort'
	x:1: in function 'f'
	x:2: in main chunk
	[C]: ?`},
		{"host comparator raising: [C] over [C]",
			`table.sort({3, 2, 1}, error)`,
			`1
stack traceback:
	[C]: ?
	[C]: in function 'sort'
	x:1: in main chunk
	[C]: ?`},
		{"gsub replacement raising",
			`string.gsub("a", "a", function() error("g") end)`,
			`x:1: g
stack traceback:
	[C]: in function 'error'
	x:1: in function <x:1>
	[C]: in function 'gsub'
	x:1: in main chunk
	[C]: ?`},
		{"error from a coroutine.wrap",
			`local w = coroutine.wrap(function() error("w") end)
local function g() w() end
g()`,
			`x:2: x:1: w
stack traceback:
	[C]: in function 'w'
	x:2: in function 'g'
	x:3: in main chunk
	[C]: ?`},
		{"an error caught first does not leak a traceback",
			`pcall(error, "caught")
error("after")`,
			`x:2: after
stack traceback:
	[C]: in function 'error'
	x:2: in main chunk
	[C]: ?`},
		{"stack overflow is elided",
			`local function r() r() end
r()`,
			`x:1: stack overflow
stack traceback:
	x:1: in function 'r'
	x:1: in function 'r'
	x:1: in function 'r'
	x:1: in function 'r'
	x:1: in function 'r'
	x:1: in function 'r'
	x:1: in function 'r'
	x:1: in function 'r'
	x:1: in function 'r'
	x:1: in function 'r'
	...
	x:1: in function 'r'
	x:1: in function 'r'
	x:1: in function 'r'
	x:1: in function 'r'
	x:1: in function 'r'
	x:1: in function 'r'
	x:1: in function 'r'
	x:1: in function 'r'
	x:2: in main chunk
	[C]: ?`},
	} {
		for _, force := range []bool{false, true} {
			prog, err := wangshu.Compile([]byte(tc.src), "@x")
			if err != nil {
				t.Fatalf("%s: compile: %v", tc.name, err)
			}
			st := wangshu.NewState(wangshu.Options{})
			st.SetForceAllPromote(force)
			_, err = prog.Run(st)
			if err == nil {
				t.Errorf("%s (force=%v): no error", tc.name, force)
				continue
			}
			if got := err.Error(); got != tc.want {
				t.Errorf("%s (force=%v):\n got %q\nwant %q", tc.name, force, got, tc.want)
			}
		}
	}
}

func runTracebackCase(t *testing.T, src string, force bool) *wangshu.State {
	t.Helper()
	prog, err := wangshu.Compile([]byte(src), "@x")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(force)
	if _, err := prog.Run(st); err != nil {
		t.Errorf("run (force=%v): %v", force, err)
		return nil
	}
	return st
}
