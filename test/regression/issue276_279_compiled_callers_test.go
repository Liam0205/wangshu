//go:build (wangshu_p3 || wangshu_p4) && wangshu_profile

package regression

import (
	"testing"

	wangshu "github.com/Liam0205/wangshu"
)

// TestHostBoundaryErrorsInCompiledCallers runs #277-#279 shapes whose caller frames the P3/P4 tiers
// actually compile. The other #276-#279 tests mostly go through debug.traceback or an unknown call,
// which keeps their frames in the interpreter even under force-all, so on their own they say nothing
// about the compiled call helpers (whose pc convention the traceback names are read from) or about
// raiseGibbous, where a compiled frame's errors get their raise-point processing. Here the frames
// under test are local functions that call only known locals or whitelisted stdlib, each run under
// force-all and under auto promotion at threshold 1, and PromotionCount must be non-zero. That is
// only a coarse guard: the count cannot say which function was promoted, so it would not notice the
// caller under test staying in the interpreter while some other function got promoted. That the
// compiled paths are covered was shown by mutation: storing the CALL pc instead of pc + 1 in the
// gibbous call helpers, or dropping raiseGibbous's raise-point processing, makes cases here fail on
// both P3 and P4. The caller of table.sort without a comparator is marked forceOnly: under P3, auto
// promotion at threshold 1 leaves it in the interpreter, so only force-all is required to compile it.
// Expectations are lua5.1's, from the same source run as a file named "x".
func TestHostBoundaryErrorsInCompiledCallers(t *testing.T) {
	for _, tc := range []struct {
		name, src, want string
		uncaught        bool // want is the error the run ends with, not OUT
		forceOnly       bool // auto mode may legitimately leave the caller in the interpreter
	}{
		{"#277: __lt error(m, 3) from table.sort's default comparison in a compiled caller",
			`local mt = {__lt = function(a, b) error("lt3", 3) end}
local function s1(t, n)
  local s = 0
  for i = 1, n do s = s + i end
  table.sort(t)
  return s
end
local out = {}
for i = 1, 5 do
  local ok, m = pcall(s1, {setmetatable({}, mt), setmetatable({}, mt), setmetatable({}, mt)}, 50)
  out[#out + 1] = tostring(m)
end
OUT = table.concat(out, ";")`,
			`x:5: lt3;x:5: lt3;x:5: lt3;x:5: lt3;x:5: lt3`, false, true},
		{"#278: nil and NaN keys through __newindex = rawset in a compiled caller",
			`local function s2(t, k, n)
  local s = 0
  for i = 1, n do s = s + i end
  t[k] = s
  return s
end
local mt = {__newindex = rawset}
local out = {}
for i = 1, 5 do
  local ok, m = pcall(s2, setmetatable({}, mt), nil, 50)
  out[#out + 1] = tostring(m)
  ok, m = pcall(s2, setmetatable({}, mt), 0/0, 50)
  out[#out + 1] = tostring(m)
end
OUT = table.concat(out, ";")`,
			`x:4: table index is nil;x:4: table index is NaN;x:4: table index is nil;x:4: table index is NaN;x:4: table index is nil;x:4: table index is NaN;x:4: table index is nil;x:4: table index is NaN;x:4: table index is nil;x:4: table index is NaN`, false, false},
		{"#278: rawset and next raising inside a C function called from a compiled caller",
			`local function s3(t, k, n)
  local s = 0
  for i = 1, n do s = s + i end
  rawset(t, k, s)
  return s
end
local function s4(t, n)
  local s = 0
  for i = 1, n do s = s + i end
  return next(t, {})
end
local out = {}
for i = 1, 5 do
  local ok, m = pcall(s3, {}, nil, 50)
  out[#out + 1] = tostring(m)
  ok, m = pcall(s4, {}, 50)
  out[#out + 1] = tostring(m)
end
OUT = table.concat(out, ";")`,
			`table index is nil;invalid key to 'next';table index is nil;invalid key to 'next';table index is nil;invalid key to 'next';table index is nil;invalid key to 'next';table index is nil;invalid key to 'next'`, false, false},
		{"#279: xpcall's handler over compiled frames",
			`local function helper(x, n)
  local s = 0
  for i = 1, n do s = s + i end
  return x + s
end
local function f(n)
  local s = 0
  for i = 1, n do s = s + i end
  return helper(nil, s) + 1
end
for i = 1, 5 do pcall(f, 3) end
local _, m = xpcall(function() return f(3) end, debug.traceback)
OUT = m`,
			`x:4: attempt to perform arithmetic on local 'x' (a nil value)
stack traceback:
	x:4: in function 'helper'
	x:9: in function <x:6>
	(tail call): ?
	[C]: in function 'xpcall'
	x:12: in main chunk
	[C]: ?`, false, false},
		{"#279: xpcall's handler over a compiled comparator inside sort",
			`local function cmp(a, b)
  local s = 0
  for i = 1, 3 do s = s + i end
  return a.v < b.v
end
local function s5(t, n)
  local s = 0
  for i = 1, n do s = s + i end
  table.sort(t, cmp)
  return s
end
for i = 1, 5 do pcall(s5, {{v = 1}, {v = 2}, {}}, 5) end
local _, m = xpcall(function() return s5({{v = 1}, {v = 2}, {}}, 5) end, debug.traceback)
OUT = m`,
			`x:4: attempt to compare nil with number
stack traceback:
	x:4: in function <x:1>
	[C]: in function 'sort'
	x:9: in function <x:6>
	(tail call): ?
	[C]: in function 'xpcall'
	x:13: in main chunk
	[C]: ?`, false, false},
		{"#279: uncaught error over compiled frames",
			`local function helper(x, n)
  local s = 0
  for i = 1, n do s = s + i end
  return x + s
end
local function f(n)
  local s = 0
  for i = 1, n do s = s + i end
  return helper(nil, s) + 1
end
for i = 1, 5 do pcall(f, 3) end
f(3)`,
			`x:4: attempt to perform arithmetic on local 'x' (a nil value)
stack traceback:
	x:4: in function 'helper'
	x:9: in function 'f'
	x:12: in main chunk
	[C]: ?`, true, false},
		{"#279: uncaught error from a compiled comparator inside sort",
			`local function cmp(a, b)
  local s = 0
  for i = 1, 3 do s = s + i end
  return a.v < b.v
end
local function s5(t, n)
  local s = 0
  for i = 1, n do s = s + i end
  table.sort(t, cmp)
  return s
end
for i = 1, 5 do pcall(s5, {{v = 1}, {v = 2}, {}}, 5) end
s5({{v = 1}, {v = 2}, {}}, 5)`,
			`x:4: attempt to compare nil with number
stack traceback:
	x:4: in function <x:1>
	[C]: in function 'sort'
	x:9: in function 's5'
	x:13: in main chunk
	[C]: ?`, true, false},
		{"#279: uncaught error in a compiled callee after its call site has succeeded",
			`local function leaf(n) local t = nil if n > 3 then return t.x end return n end
local function mid(n)
  local s = 0
  for i = 1, n do s = s + i end
  leaf(n)
  return s
end
for i = 1, 3 do mid(i) end
mid(5)`,
			`x:1: attempt to index local 't' (a nil value)
stack traceback:
	x:1: in function 'leaf'
	x:5: in function 'mid'
	x:9: in main chunk
	[C]: ?`, true, false},
		{"#279: xpcall(f, debug.traceback) over a compiled call site that has succeeded",
			`local function leaf(n) local t = nil if n > 3 then return t.x end return n end
local function mid(n)
  local s = 0
  for i = 1, n do s = s + i end
  local v = leaf(n)
  return s + v
end
for i = 1, 3 do mid(i) end
OUT = select(2, xpcall(function() local r = mid(5) return r end, debug.traceback))`,
			`x:1: attempt to index local 't' (a nil value)
stack traceback:
	x:1: in function 'leaf'
	x:5: in function 'mid'
	x:9: in function <x:9>
	[C]: in function 'xpcall'
	x:9: in main chunk
	[C]: ?`, false, false},
	} {
		for _, mode := range []string{"force", "auto"} {
			prog, err := wangshu.Compile([]byte(tc.src), "@x")
			if err != nil {
				t.Fatalf("%s: compile: %v", tc.name, err)
			}
			st := wangshu.NewState(wangshu.Options{})
			if mode == "force" {
				st.SetForceAllPromote(true)
			} else {
				st.SetHotThresholds(1, 1)
			}
			_, err = prog.Run(st)
			var got string
			switch {
			case tc.uncaught && err == nil:
				t.Errorf("%s (%s): no error", tc.name, mode)
				continue
			case tc.uncaught:
				got = err.Error()
			case err != nil:
				t.Errorf("%s (%s): %v", tc.name, mode, err)
				continue
			default:
				got = st.GetGlobal("OUT").Str()
			}
			if got != tc.want {
				t.Errorf("%s (%s):\n got %q\nwant %q", tc.name, mode, got, tc.want)
			}
			if st.PromotionCount() == 0 && (mode == "force" || !tc.forceOnly) {
				t.Errorf("%s (%s): nothing was promoted, so the compiled path went untested", tc.name, mode)
			}
		}
	}
}
