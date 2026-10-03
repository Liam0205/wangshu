package regression

import (
	"testing"

	wangshu "github.com/Liam0205/wangshu"
)

// TestUnreferencedCoroutinesAreCollected covers #291: every coroutine that was not dead was a GC root,
// so a suspended coroutine nothing referred to kept its stack (and all it reached) for the life of the
// State, and a weak table never dropped it as a key. lua5.1 collects a thread like any other object.
// Expectations are lua5.1's.
func TestUnreferencedCoroutinesAreCollected(t *testing.T) {
	const src = `collectgarbage() collectgarbage()
local base = collectgarbage("count")
for i = 1, 50 do
  local co = coroutine.create(function() local t = {} for j = 1, 20000 do t[j] = j end coroutine.yield(t) end)
  coroutine.resume(co)
end
collectgarbage() collectgarbage()
print(collectgarbage("count") - base > 1000)
local w = setmetatable({}, {__mode = "k"})
w[coroutine.create(function() coroutine.yield() end)] = true
for co in pairs(w) do coroutine.resume(co) end
collectgarbage() collectgarbage()
print(next(w) == nil)
`
	const want = `false
true
`
	if got := printedBy(t, wangshu.Options{}, src); got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

// TestCollectedCoroutinesKeepWhatIsShared covers what must survive #291's collection of coroutines: a
// closure made inside a collected coroutine keeps its (shared) upvalue, a coroutine.wrap function keeps
// its coroutine, a coroutine reached only from another suspended one stays resumable, and the
// coroutine ids freed by collection are reused safely. It runs with and without GC stress mode.
// Expectations are lua5.1's.
func TestCollectedCoroutinesKeepWhatIsShared(t *testing.T) {
	const src = `-- a closure made inside a suspended coroutine keeps its upvalue after the coroutine is collected
local get, set
do
  local co = coroutine.create(function()
    local x = {v = "kept"}
    get = function() return x.v end
    set = function(v) x = {v = v} end
    coroutine.yield()
  end)
  coroutine.resume(co)
end
collectgarbage() collectgarbage()
for i = 1, 3000 do local t = {i} end
print(get())
set("new") collectgarbage()
for i = 1, 3000 do local t = {tostring(i)} end
print(get())
-- the upvalue is shared by two closures from the dead stack
local a, b
do
  local co = coroutine.create(function()
    local n = 0
    a = function() n = n + 1 return n end
    b = function() return n end
    coroutine.yield()
  end)
  coroutine.resume(co)
end
collectgarbage()
a() a()
print(b())
-- wrap: the function keeps the coroutine; dropping it frees it
local w = setmetatable({}, {__mode = "v"})
local f = coroutine.wrap(function() local co = coroutine.running() w[1] = co for i = 1, 3 do coroutine.yield(i) end end)
print(f(), f())
collectgarbage()
print(w[1] ~= nil, type(w[1]))
print(f())
f = nil
collectgarbage() collectgarbage()
print(w[1] == nil)
-- a coroutine that refers to itself only from its own stack is still collected
local w2 = setmetatable({}, {__mode = "k"})
do
  local co
  co = coroutine.create(function() local me = co coroutine.yield() end)
  coroutine.resume(co)
  w2[co] = 1
end
collectgarbage() collectgarbage()
print(next(w2) == nil)
-- a suspended coroutine reached only from another suspended coroutine stays alive
local outer
do
  local inner = coroutine.create(function(x) local y = coroutine.yield(x .. "1") return y .. "2" end)
  outer = coroutine.create(function()
    local r = {coroutine.resume(inner, "a")}
    coroutine.yield(r[2])
    return select(2, coroutine.resume(inner, "b"))
  end)
end
print(coroutine.resume(outer))
collectgarbage() collectgarbage()
for i = 1, 3000 do local t = {i} end
print(coroutine.resume(outer))
-- many cycles of create/drop with live ones mixed in
local keep = {}
for i = 1, 200 do
  local co = coroutine.create(function(a) local t = {a} while true do a = coroutine.yield(t[1] .. a) end end)
  coroutine.resume(co, "s" .. i)
  if i % 10 == 0 then keep[#keep + 1] = co end
  if i % 50 == 0 then collectgarbage() end
end
collectgarbage()
local out = {}
for i, co in ipairs(keep) do local _, r = coroutine.resume(co, "!") out[#out + 1] = r end
print(#keep, out[1], out[#out])
print(type(keep[1]), tostring(keep[1]):match("^thread: ") ~= nil, coroutine.status(keep[1]))
-- dead coroutines keep a status and can be keys
local d = coroutine.create(function() end)
coroutine.resume(d)
collectgarbage()
print(coroutine.status(d), coroutine.resume(d))
local e = coroutine.create(function() error("x") end)
print((coroutine.resume(e)))
collectgarbage()
print(coroutine.status(e), select("#", debug.traceback(e)))
`
	const want = `kept
new
2
1	2
true	thread
3
true
true
true	a1
true	b2
20	s10!	s200!
thread	true	suspended
dead	false	cannot resume dead coroutine
false
dead	1
`
	for _, stress := range []bool{false, true} {
		prog, err := wangshu.Compile([]byte(src), "@x")
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		got := captureStdout(t, func() {
			st := wangshu.NewState(wangshu.Options{})
			st.SetGCStressMode(stress)
			if _, err := prog.Run(st); err != nil {
				t.Errorf("stress=%v: run: %v", stress, err)
			}
		})
		if got != want {
			t.Errorf("stress=%v:\n got %q\nwant %q", stress, got, want)
		}
	}
}
