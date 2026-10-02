package regression

import (
	"strings"
	"testing"

	wangshu "github.com/Liam0205/wangshu"
	"github.com/Liam0205/wangshu/test/testutil"
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
		{"xpcall's handler sees the raiser's frames",
			`local function lvl3() error("deep") end
local function lvl2() lvl3() end
local _, m = xpcall(function() lvl2() end, debug.traceback)
OUT = m`,
			`x:1: deep
stack traceback:
	[C]: in function 'error'
	x:1: in function 'lvl3'
	x:2: in function 'lvl2'
	x:3: in function <x:3>
	[C]: in function 'xpcall'
	x:3: in main chunk
	[C]: ?`},
		{"xpcall's handler on a runtime error",
			`local _, m = xpcall(function() local x = nil; return x.y end, debug.traceback)
OUT = m`,
			`x:1: attempt to index local 'x' (a nil value)
stack traceback:
	x:1: in function <x:1>
	[C]: in function 'xpcall'
	x:1: in main chunk
	[C]: ?`},
		{"xpcall's handler inside a sort comparator",
			`local _, m = xpcall(function() table.sort({3, 2, 1}, function(a, b) error("cmp") end) end, debug.traceback)
OUT = m`,
			`x:1: cmp
stack traceback:
	[C]: in function 'error'
	x:1: in function <x:1>
	[C]: in function 'sort'
	x:1: in function <x:1>
	[C]: in function 'xpcall'
	x:1: in main chunk
	[C]: ?`},
		{"xpcall's handler for an error re-raised by a wrap function",
			`local _, m = xpcall(function() local co = coroutine.wrap(function() error("inco") end) co() end, debug.traceback)
OUT = m`,
			`x:1: x:1: inco
stack traceback:
	[C]: in function 'co'
	x:1: in function <x:1>
	[C]: in function 'xpcall'
	x:1: in main chunk
	[C]: ?`},
		{"xpcall's handler inside __tostring under print",
			`local _, m = xpcall(function() print(setmetatable({}, {__tostring = function() error("ts") end})) end, debug.traceback)
OUT = m`,
			`x:1: ts
stack traceback:
	[C]: in function 'error'
	x:1: in function <x:1>
	[C]: ?
	[C]: in function 'print'
	x:1: in function <x:1>
	[C]: in function 'xpcall'
	x:1: in main chunk
	[C]: ?`},
		{"xpcall keeps one handler result",
			`OUT = tostring(select("#", xpcall(error, function(m) return 1, 2 end)))`,
			`2`},
		{"a handler that is not a function",
			`local _, m = xpcall(function() error("x") end, setmetatable({}, {__call = function() return "called" end}))
OUT = m`,
			`error in error handling`},
		{"a handler that always fails",
			`local _, m = xpcall(function() error("x") end, function(m) error("again") end)
OUT = m`,
			`error in error handling`},
		{"a handler that fails once runs again for its own error",
			`local n = 0
local _, m = xpcall(function() error("x") end, function(m) n = n + 1 if n == 1 then error("again") end return n .. ":" .. m end)
OUT = m`,
			`2:x:2: again`},
		{"an inner pcall shields its errors from the handler",
			`local _, m = xpcall(function() local ok, m = pcall(error, "inner") error("outer:" .. m) end, function(m) return "H " .. m end)
OUT = m`,
			`H x:1: outer:inner`},
		{"a resumed coroutine's errors do not reach the handler",
			`local _, a, b = xpcall(function() return coroutine.resume(coroutine.create(function() error("inco") end)) end, function(m) return "H" end)
OUT = tostring(a) .. " " .. b`,
			`false x:1: inco`},
		{"the handler runs for a stack overflow",
			`local _, m = xpcall(function() local function r() return r() + 1 end return r() end, function(m) return (m:gsub("\n.*", "")) end)
OUT = m`,
			`x:1: stack overflow`},
		{"suspended coroutine: yield on top, level 0 by default",
			`local co = coroutine.create(function() local function g() coroutine.yield() end g() end)
coroutine.resume(co)
OUT = debug.traceback(co, "co")`,
			`co
stack traceback:
	[C]: in function 'yield'
	x:1: in function 'g'
	x:1: in function <x:1>`},
		{"coroutine levels count from 0",
			`local co = coroutine.create(function() local function g() coroutine.yield() end g() end)
coroutine.resume(co)
OUT = debug.traceback(co, "co", 1)`,
			`co
stack traceback:
	x:1: in function 'g'
	x:1: in function <x:1>`},
		{"coroutine negative level",
			`local co = coroutine.create(function() coroutine.yield() end)
coroutine.resume(co)
OUT = debug.traceback(co, "co", -1)`,
			`co
stack traceback:
	(tail call): ?
	[C]: in function 'yield'
	x:1: in function <x:1>`},
		{"coroutine not started",
			`OUT = debug.traceback(coroutine.create(function() end), "new")`,
			`new
stack traceback:`},
		{"coroutine finished",
			`local co = coroutine.create(function() end)
coroutine.resume(co)
OUT = debug.traceback(co, "done")`,
			`done
stack traceback:`},
		{"coroutine that died by error keeps its stack",
			`local co = coroutine.create(function() table.sort({3, 2, 1}, function(a, b) error("cmp") end) end)
coroutine.resume(co)
OUT = debug.traceback(co, "dead")`,
			`dead
stack traceback:
	[C]: in function 'error'
	x:1: in function <x:1>
	[C]: in function 'sort'
	x:1: in function <x:1>`},
		{"coroutine that died by a non-string error keeps its stack",
			`local co = coroutine.create(function() error({}) end)
coroutine.resume(co)
OUT = debug.traceback(co)`,
			`stack traceback:
	[C]: in function 'error'
	x:1: in function <x:1>`},
		{"normal coroutine waits in resume",
			`local outer
local inner = coroutine.create(function() OUT = debug.traceback(outer, "normal") end)
outer = coroutine.create(function() local r = coroutine.resume r(inner) end)
coroutine.resume(outer)`,
			`normal
stack traceback:
	[C]: in function 'r'
	x:3: in function <x:3>`},
		{"the running coroutine's own handle",
			`local w
w = coroutine.create(function() OUT = debug.traceback(w, "self") end)
coroutine.resume(w)`,
			`self
stack traceback:
	x:2: in function <x:2>`},
		{"a numeric string level",
			`local function f() return debug.traceback("s", "2") end
OUT = f()`,
			`s
stack traceback:
	x:2: in main chunk
	[C]: ?`},
		{"traceback under print's tostring",
			`local T = setmetatable({}, {__tostring = function() OUT = debug.traceback("ts") return "" end})
print(T)`,
			`ts
stack traceback:
	x:1: in function <x:1>
	[C]: ?
	[C]: in function 'print'
	x:2: in main chunk
	[C]: ?`},
		{"C stack overflow under xpcall still reaches the handler",
			`local t = setmetatable({}, {})
getmetatable(t).__index = function(t, k) return t[k] end
local _, m = xpcall(function() return t.x end, function(m) return "H:" .. tostring(m) end)
OUT = m`,
			`H:x:2: C stack overflow`},
		{"C stack overflow through __tostring under xpcall",
			`local function deep(n) return tostring(setmetatable({}, {__tostring = function() return deep(n + 1) end})) end
local _, m = xpcall(function() return deep(0) end, function(m) return "H:" .. tostring(m) end)
OUT = m`,
			`H:C stack overflow`},
		{"load's reader runs the enclosing xpcall's handler",
			`local n = 0
local _, m = xpcall(function() local f, m = load(function() error("rd") end) return m end, function(m) n = n + 1 return "H:" .. m end)
OUT = tostring(m) .. " " .. n`,
			`H:x:2: rd 1`},
		{"a reader error does not count as the coroutine dying",
			`local co = coroutine.create(function() local f, m = load(function() error("rd") end) return m end)
coroutine.resume(co)
OUT = debug.traceback(co, "done")`,
			`done
stack traceback:`},
		{"a reader's non-string piece names load's caller",
			`local _, f, m = pcall(function() return load(function() return {} end) end)
OUT = tostring(f) .. " " .. m`,
			`nil x:1: reader function must return a string`},
		{"a reader's non-string piece runs the enclosing xpcall's handler",
			`local _, f, m = xpcall(function() return load(function() return {} end) end, function(m) return "H:" .. m end)
OUT = tostring(f) .. " " .. m`,
			`nil H:x:1: reader function must return a string`},
		{"a reader's non-string piece with a C caller is bare",
			`local _, f, m = pcall(load, function() return {} end)
OUT = tostring(f) .. " " .. m`,
			`nil reader function must return a string`},
		{"a reader's non-string piece at top level carries the traceback",
			`local f, m = load(function() return {} end)
OUT = m`,
			`x:1: reader function must return a string
stack traceback:
	[C]: in function 'load'
	x:1: in main chunk
	[C]: ?`},
		{"a reader may return a number piece",
			`local parts, i = {"return ", 42}, 0
OUT = tostring(load(function() i = i + 1 return parts[i] end)())`,
			`42`},
		{"resume stays at the C limit while a handler runs past it",
			`local t = setmetatable({}, {})
getmetatable(t).__index = function(t, k) return t[k] end
local co = coroutine.create(function() return "ran" end)
local _, m = xpcall(function() return t.x end, function(m) return tostring(m) .. " | " .. tostring(select(2, coroutine.resume(co))) end)
OUT = m`,
			`x:2: C stack overflow | C stack overflow`},
		{"LUA_ERRERR caught inside a handler has no position",
			`local t = setmetatable({}, {})
getmetatable(t).__index = function(t, k) return t[k] end
local _, m = xpcall(function() return t.x end, function(m)
  local ok, m2 = pcall(function() return t.y end)
  return tostring(m) .. " | " .. tostring(ok) .. " " .. tostring(m2)
end)
OUT = m`,
			`x:2: C stack overflow | false error in error handling`},
		{"calling the handler at the C limit: a runtime error one level short",
			`local n, limit = 0, nil
local mt = {}
mt.__index = function(t, k)
  n = n + 1
  if limit and n >= limit then return n + {} end
  return t[k]
end
local t = setmetatable({}, mt)
pcall(function() return t.x end)
limit = n
n = 0
local _, m = xpcall(function() return t.x end, function(m) return "H:" .. tostring(m) end)
OUT = m`,
			`H:x:5: C stack overflow`},
		{"calling the handler at the C limit: a host function raising one level short",
			`local n, limit = 0, nil
local mt = {}
mt.__index = function(t, k)
  n = n + 1
  if limit and n >= limit then error("e") end
  return t[k]
end
local t = setmetatable({}, mt)
pcall(function() return t.x end)
limit = n
n = 0
local _, m = xpcall(function() return t.x end, function(m) return "H:" .. tostring(m) end)
OUT = m`,
			`H:C stack overflow`},
		{"a handler past the C limit cannot compile a chunk",
			`local t = setmetatable({}, {})
getmetatable(t).__index = function(t, k) return t[k] end
local _, m = xpcall(function() return t.x end, function(m) local f, e = loadstring("return 1") return tostring(f) .. " " .. tostring(e) end)
OUT = m`,
			`nil [string "return 1"]:1: chunk has too many syntax levels`},
		{"syntax levels count the C calls already in use",
			`local function maxlevels()
  local lo, hi = 0, 400
  while lo < hi do
    local mid = math.floor((lo + hi + 1) / 2)
    if loadstring("return " .. string.rep("(", mid) .. "1" .. string.rep(")", mid)) then lo = mid else hi = mid - 1 end
  end
  return lo
end
local top = maxlevels()
local function nest(n) if n == 0 then return maxlevels() end local ok, r = pcall(nest, n - 1) return r end
OUT = tostring(top - nest(50))`,
			`50`},
		{"a resume refused at the C limit kills a coroutine that never started",
			`local t = setmetatable({}, {})
getmetatable(t).__index = function(t, k) return t[k] end
local fresh = coroutine.create(function() return "ran" end)
local y = coroutine.create(function() coroutine.yield(1) return "ran2" end)
coroutine.resume(y)
local _, m = xpcall(function() return t.x end, function(m)
  local a = tostring(select(2, coroutine.resume(fresh)))
  local b = tostring(select(2, coroutine.resume(y)))
  return a .. " " .. coroutine.status(fresh) .. " | " .. b .. " " .. coroutine.status(y)
end)
OUT = m .. " | " .. tostring(select(2, coroutine.resume(fresh))) .. " | " .. tostring(select(2, coroutine.resume(y)))`,
			`C stack overflow dead | C stack overflow suspended | cannot resume dead coroutine | ran2`},
		{"the deepest reachable level can still compile a chunk and resume a coroutine",
			`local last, rs
local t = setmetatable({}, {})
getmetatable(t).__index = function(t, k)
  last = loadstring("") ~= nil
  rs = coroutine.resume(coroutine.create(function() end))
  return t[k]
end
pcall(function() return t.x end)
OUT = tostring(last) .. " " .. tostring(rs)`,
			`true true`},
		{"a table error rethrown by a wrap function: [C] lines above the wrap call",
			`local w2 = coroutine.wrap(function() table.foreach({[{}] = 1}, error) end)
local function caller() w2() end
OUT = select(2, xpcall(caller, function(m) return debug.traceback("T") end))`,
			`T
stack traceback:
	x:3: in function <x:3>
	[C]: in function 'w2'
	x:2: in function <x:2>
	[C]: in function 'xpcall'
	x:3: in main chunk
	[C]: ?`},
		{"a table error rethrown by a wrap function called by xpcall itself",
			`OUT = select(2, xpcall(coroutine.wrap(function() error({}) end), function() return debug.traceback("T") end))`,
			`T
stack traceback:
	x:1: in function <x:1>
	[C]: ?
	[C]: in function 'xpcall'
	x:1: in main chunk
	[C]: ?`},
		{"an outer coroutine killed by a table error from a wrap function keeps its frames",
			`local outer = coroutine.create(function()
  local w = coroutine.wrap(function() error({}) end)
  w()
end)
local ok = coroutine.resume(outer)
OUT = tostring(ok) .. "\n" .. debug.traceback(outer)`,
			`false
stack traceback:
	[C]: in function 'w'
	x:3: in function <x:1>`},
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
		{"__newindex = rawset nil key",
			`local t = setmetatable({}, {__newindex = rawset})
t[nil] = 1`,
			`x:2: table index is nil
stack traceback:
	x:2: in main chunk
	[C]: ?`},
		{"__tostring raising under print",
			`local T = setmetatable({}, {__tostring = function() error("ts") end})
print(T)`,
			`x:1: ts
stack traceback:
	[C]: in function 'error'
	x:1: in function <x:1>
	[C]: ?
	[C]: in function 'print'
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

// TestNonConstantKeyIsNamedFieldQuestionMark covers getobjname's GETTABLE / SELF case: a table read is
// "field" whatever its key, and kname gives '?' for a key that is not a string constant, so the error
// suffix is "field '?'" -- the same name the traceback prints for such a callee. Expectations are
// lua5.1's.
func TestNonConstantKeyIsNamedFieldQuestionMark(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"call through a register key",
			`local function e(f, ...) local ok, m = pcall(f, ...) return tostring(ok) .. " " .. tostring(m) end
local t, i = {}, 1
return e(function()
  t[i]()
end)`,
			`false [string "test"]:4: attempt to call field '?' (a nil value)`},
		{"index through a number constant key",
			`local function e(f, ...) local ok, m = pcall(f, ...) return tostring(ok) .. " " .. tostring(m) end
local t = {}
return e(function()
  return t[1].x
end)`,
			`false [string "test"]:4: attempt to index field '?' (a nil value)`},
		{"arithmetic on a boolean constant key",
			`local function e(f, ...) local ok, m = pcall(f, ...) return tostring(ok) .. " " .. tostring(m) end
local t = {}
return e(function()
  return t[true] + 1
end)`,
			`false [string "test"]:4: attempt to perform arithmetic on field '?' (a nil value)`},
		{"length of a register key",
			`local function e(f, ...) local ok, m = pcall(f, ...) return tostring(ok) .. " " .. tostring(m) end
local t, k = {}, "s"
return e(function()
  return #t[k]
end)`,
			`false [string "test"]:4: attempt to get length of field '?' (a nil value)`},
		{"concatenation of a register key",
			`local function e(f, ...) local ok, m = pcall(f, ...) return tostring(ok) .. " " .. tostring(m) end
local t, i = {}, 1
return e(function()
  return t[i] .. "x"
end)`,
			`false [string "test"]:4: attempt to concatenate field '?' (a nil value)`},
		{"method call through a register key",
			`local function e(f, ...) local ok, m = pcall(f, ...) return tostring(ok) .. " " .. tostring(m) end
local o, i = {}, 1
return e(function()
  o[i](o)
end)`,
			`false [string "test"]:4: attempt to call field '?' (a nil value)`},
		{"string constant keys keep their name",
			`local function e(f, ...) local ok, m = pcall(f, ...) return tostring(ok) .. " " .. tostring(m) end
local t = {}
return e(function()
  t.x()
end)`,
			`false [string "test"]:4: attempt to call field 'x' (a nil value)`},
	} {
		if got := testutil.RunOne(t, tc.src).Str(); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestUncaughtNonStringErrorHasNoTraceback: lua.c's handler hands a non-string error object back
// untouched, so an uncaught error({}) gets no traceback.
func TestUncaughtNonStringErrorHasNoTraceback(t *testing.T) {
	for _, src := range []string{`error({})`, `error()`, `error(setmetatable({}, {__tostring = function() return "T" end}))`} {
		prog, err := wangshu.Compile([]byte(src), "@x")
		if err != nil {
			t.Fatalf("%s: compile: %v", src, err)
		}
		_, err = prog.Run(wangshu.NewState(wangshu.Options{}))
		if err == nil {
			t.Errorf("%s: no error", src)
		} else if strings.Contains(err.Error(), "stack traceback") {
			t.Errorf("%s: got a traceback: %q", src, err.Error())
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
