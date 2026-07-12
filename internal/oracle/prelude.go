// Package oracle: the Lua-text prelude shared by BOTH sides of the
// differential fuzz (official 5.1.5 via the cgo shim, and wangshu).
//
// Symmetry is the core invariant: every stub, cap, wrapper, and
// rewrite below runs identically on both interpreters, so harness
// behavior can never be attributed to one side only. The single
// deliberate exception is documented at preludeGuards ("pattern too
// complex" conversion -- a wangshu-only guard rerouted to a skip).
//
// This file carries no build tag: prelude construction is pure Go and
// stays visible to the default (zero-cgo) build for fmt/vet/lint and
// for deterministic corpus replay tooling.
package oracle

import (
	"sort"
	"strconv"
	"strings"
)

// OutputCapBytes caps accumulated print/io.write output. Both sides
// raise the LimitSentinel at exactly the same accumulated length, so
// the cap itself can never create a divergence.
const OutputCapBytes = 1 << 20

// LimitSentinel marks shim/harness-imposed limit errors. It matches
// ORACLE_LIMIT_SENTINEL in shim.c; the fuzz target classifies any
// error carrying it as "not comparable, skip". Scripts CAN fake it
// via error("ORACLE_LIMIT..."), but both sides then skip
// symmetrically -- a lost input, never a false verdict.
const LimitSentinel = "ORACLE_LIMIT"

// GlobalSet describes the allowed global surface: top-level names,
// plus per-module key sets for whitelisted table globals. Callers
// build it from a live wangshu State so the whitelist tracks stdlib
// growth automatically instead of being hand-copied.
type GlobalSet struct {
	// Top lists allowed top-level global names.
	Top []string
	// Nested maps a table-global name to its allowed keys
	// (e.g. "string" -> {"sub", "rep", ...}).
	Nested map[string][]string
}

// Prelude returns the Lua chunk to run before the fuzz input, layered:
//
//  1. capture: print/io.write append to a local accumulator (output
//     cap raises LimitSentinel); readout exposed as __oracle_readout.
//  2. determinism stubs: os.time/clock/date/getenv, collectgarbage/
//     gcinfo, math.random/randomseed.
//  3. guards: loadfile/dofile disabled; loadstring/load reject binary
//     chunks (PUC would otherwise undump attacker-controlled bytecode
//     -- 5.1's verifier is known-unsafe); string pattern functions get
//     size/quantifier caps (bounds PUC's un-hookable C backtracking)
//     and a "pattern too complex" -> LimitSentinel conversion (wangshu
//     bounds backtracking, PUC does not -- an engine-capability gap
//     rerouted to skip); string.rep product cap; pcall/xpcall/
//     coroutine.resume re-raise limit errors so budget/memory hits are
//     never swallowed by a catch (the two engines' budgets are not
//     comparable, so a caught budget error would silently fork
//     execution paths).
//  4. whitelist trim: erase every global (and nested key of the
//     whitelisted table globals) not present in keep.
//  5. sorted iteration: pairs/next/table.foreach iterate keys in a
//     total order (Lua 5.1 leaves table iteration order unspecified,
//     so raw order would be pure false-positive noise).
func Prelude(keep GlobalSet) string {
	var b strings.Builder
	b.Grow(1 << 13)
	b.WriteString(preludeCapture)
	b.WriteString(preludeStubs)
	b.WriteString(preludeGuards)
	writeTrim(&b, keep)
	b.WriteString(preludeSortedIter)
	return b.String()
}

var preludeCapture = `
local __acc, __n, __len = {}, 0, 0
local __tostring, __type, __select = tostring, type, select
local __concat, __error, __format = table.concat, error, string.format
local function __emit(s)
  __len = __len + #s
  if __len > ` + strconv.Itoa(OutputCapBytes) + ` then
    __error("` + LimitSentinel + `: output cap", 0)
  end
  __n = __n + 1
  __acc[__n] = s
end
function print(...)
  local n = __select("#", ...)
  for i = 1, n do
    local s = __tostring((__select(i, ...)))
    if __type(s) ~= "string" then
      __error("'tostring' must return a string to 'print'")
    end
    if i > 1 then __emit("\t") end
    __emit(s)
  end
  __emit("\n")
end
io.write = function(...)
  local n = __select("#", ...)
  for i = 1, n do
    local v = (__select(i, ...))
    local tv = __type(v)
    if tv == "number" then
      __emit(__format("%.14g", v))
    elseif tv == "string" then
      __emit(v)
    else
      __error("bad argument #" .. i .. " to 'write' (string expected, got " .. tv .. ")")
    end
  end
  return true
end
function __oracle_readout()
  return __concat(__acc)
end
`

const preludeStubs = `
os.time = function() return 0 end
os.clock = function() return 0 end
os.date = function() return "(date)" end
os.getenv = function() return nil end
os.difftime = function(t2, t1) return t2 - (t1 or 0) end
collectgarbage = function() return 0 end
gcinfo = function() return 0 end
math.random = function() return 0.5 end
math.randomseed = function() end
loadfile = function() return nil, "oracle-harness: loadfile disabled" end
dofile = function() __error("oracle-harness: dofile disabled") end
`

// preludeGuards: input-bounding wrappers. All error texts are either
// the shared LimitSentinel (both sides skip) or the fixed
// "oracle-harness:" prefix (identical on both sides, so the resulting
// error is comparable, not skipped).
const preludeGuards = `
local __pcall, __xpcall, __sfind, __ssub, __sgsub = pcall, xpcall, string.find, string.sub, string.gsub
local function __is_limit(m)
  return __type(m) == "string" and (
    __sfind(m, "` + LimitSentinel + `", 1, true) or
    __sfind(m, "instruction budget exceeded", 1, true) or
    __sfind(m, "not enough memory", 1, true))
end
local __handler_limit = nil
local function __refilter(ok, ...)
  if not ok then
    local m = (__select(1, ...))
    if __is_limit(m) then __error(m, 0) end
    if __handler_limit ~= nil then
      local mm = __handler_limit
      __handler_limit = nil
      __error(mm, 0)
    end
  end
  return ok, ...
end
pcall = function(f, ...) return __refilter(__pcall(f, ...)) end
xpcall = function(f, h)
  return __refilter(__xpcall(f, function(m)
    if __is_limit(m) then
      __handler_limit = m
      return m
    end
    return h(m)
  end))
end
local __resume = coroutine.resume
coroutine.resume = function(co, ...) return __refilter(__resume(co, ...)) end

local __loadstring, __load = loadstring, load
loadstring = function(s, c)
  if __type(s) == "string" and __ssub(s, 1, 1) == "\27" then
    return nil, "oracle-harness: binary chunk rejected"
  end
  return __loadstring(s, c)
end
load = function(f, c)
  if __type(f) == "string" then return loadstring(f, c) end
  if __type(f) ~= "function" then return __load(f, c) end
  local first = true
  return __load(function()
    local piece = f()
    if first and __type(piece) == "string" and __ssub(piece, 1, 1) == "\27" then
      __error("oracle-harness: binary chunk rejected", 0)
    end
    first = false
    return piece
  end, c)
end

local __rep = string.rep
string.rep = function(s, n)
  if __type(s) == "number" then s = __tostring(s) end
  if __type(s) == "string" and __type(n) == "number" and n > 0 and #s * n > 4194304 then
    __error("oracle-harness: rep too large", 0)
  end
  return __rep(s, n)
end

-- Pattern-function guards. PUC 5.1's matcher is unbounded C-side
-- backtracking that the instruction hook cannot interrupt (and deep
-- %b/quantifier recursion can overflow the C stack); wangshu bounds
-- both (its own "pattern too complex"). Caps below keep PUC's worst
-- case around 256^3 steps (~tens of ms) and keep wangshu's step
-- budget from firing on inputs PUC would still chew through; when
-- wangshu's guard fires anyway, the conversion below turns it into
-- the shared limit sentinel (skip, not divergence).
local function __patcheck(s, p)
  if __type(s) == "number" then s = __tostring(s) end
  if __type(p) == "number" then p = __tostring(p) end
  if __type(s) == "string" and #s > 256 then
    __error("oracle-harness: pattern subject too long", 0)
  end
  if __type(p) == "string" then
    if #p > 48 then __error("oracle-harness: pattern too long", 0) end
    local _, q = __sgsub(p, "[%*%+%-%?]", "%0")
    if q > 2 then __error("oracle-harness: pattern too branchy", 0) end
  end
  return s, p
end
local function __patres(ok, ...)
  if not ok then
    local m = (__select(1, ...))
    if __type(m) == "string" and __sfind(m, "pattern too complex", 1, true) then
      __error("` + LimitSentinel + `: pattern", 0)
    end
    __error(m, 0)
  end
  return ...
end
local __find, __match, __gmatch, __gsub2 = string.find, string.match, string.gmatch, string.gsub
string.find = function(s, p, i, plain)
  s, p = __patcheck(s, p)
  return __patres(__pcall(__find, s, p, i, plain))
end
string.match = function(s, p, i)
  s, p = __patcheck(s, p)
  return __patres(__pcall(__match, s, p, i))
end
string.gmatch = function(s, p)
  s, p = __patcheck(s, p)
  local it = __gmatch(s, p)
  return function() return __patres(__pcall(it)) end
end
string.gfind = string.gmatch
string.gsub = function(s, p, r, n)
  s, p = __patcheck(s, p)
  return __patres(__pcall(__gsub2, s, p, r, n))
end
`

// preludeSortedIter replaces the unordered iteration primitives with
// sort-then-replay versions. Keys of types lacking a total order sort
// by type rank then tostring -- tostring embeds addresses that differ
// across sides, so reference-keyed tables stay rank-grouped only; the
// fuzz target's address normalizer handles the printed form.
const preludeSortedIter = `
local __rawnext, __sort = next, table.sort
local __rank = { number = 1, string = 2, boolean = 3, table = 4,
                 ["function"] = 5, userdata = 6, thread = 7 }
local __keyorder = function(a, b)
  local ta, tb = __rank[__type(a)] or 8, __rank[__type(b)] or 8
  if ta ~= tb then return ta < tb end
  if ta <= 2 then return a < b end
  if ta == 3 then return (a and 1 or 0) < (b and 1 or 0) end
  return __tostring(a) < __tostring(b)
end
local function __sortedkeys(t)
  local ks, n = {}, 0
  local k = __rawnext(t)
  while k ~= nil do
    n = n + 1
    ks[n] = k
    k = __rawnext(t, k)
  end
  __sort(ks, __keyorder)
  return ks, n
end
function pairs(t)
  if __type(t) ~= "table" then
    __error("bad argument #1 to 'pairs' (table expected, got " .. __type(t) .. ")")
  end
  local ks, n = __sortedkeys(t)
  local i = 0
  return function()
    while true do
      i = i + 1
      if i > n then return nil end
      local k = ks[i]
      local v = t[k]
      if v ~= nil then return k, v end
    end
  end, t, nil
end
next = function(t, k)
  if __type(t) ~= "table" then
    __error("bad argument #1 to 'next' (table expected, got " .. __type(t) .. ")")
  end
  local ks, n = __sortedkeys(t)
  if k == nil then
    if n == 0 then return nil end
    return ks[1], t[ks[1]]
  end
  for i = 1, n do
    if ks[i] == k then
      if i == n then return nil end
      return ks[i+1], t[ks[i+1]]
    end
  end
  __error("invalid key to 'next'")
end
table.foreach = function(t, f)
  local ks, n = __sortedkeys(t)
  for i = 1, n do
    local v = t[ks[i]]
    if v ~= nil then
      local r = f(ks[i], v)
      if r ~= nil then return r end
    end
  end
end
`

// writeTrim emits the whitelist-erase layer: every top-level global
// not in keep.Top is erased; every key of a whitelisted TABLE global
// not in keep.Nested[name] likewise. Erasure is in-place, so the
// string metatable's __index (the string library table itself, on
// both engines) sees the trimmed set too. The prelude's own readout
// global survives automatically.
//
// Iteration inside the trim uses the still-native next (the sorted
// replacement is installed after this layer), captured locally so the
// fuzz script cannot interfere.
func writeTrim(b *strings.Builder, keep GlobalSet) {
	top := make([]string, 0, len(keep.Top)+1)
	top = append(top, keep.Top...)
	top = append(top, "__oracle_readout")
	sort.Strings(top)

	b.WriteString("do\n  local __next, __rawget, __rawset = next, rawget, rawset\n")
	b.WriteString("  local __keep = {")
	for _, name := range top {
		b.WriteString("[")
		b.WriteString(luaStrLit(name))
		b.WriteString("] = true, ")
	}
	b.WriteString("}\n")
	b.WriteString(`  local __g = _G
  local kill = {}
  local k = __next(__g)
  while k ~= nil do
    if __type(k) ~= "string" or not __keep[k] then kill[#kill+1] = k end
    k = __next(__g, k)
  end
  for i = 1, #kill do __rawset(__g, kill[i], nil) end
`)
	nested := make([]string, 0, len(keep.Nested))
	for name := range keep.Nested {
		nested = append(nested, name)
	}
	sort.Strings(nested)
	for _, name := range nested {
		keys := append([]string(nil), keep.Nested[name]...)
		sort.Strings(keys)
		b.WriteString("  do local __t = __rawget(__g, ")
		b.WriteString(luaStrLit(name))
		b.WriteString(")\n  if __type(__t) == \"table\" then\n    local __nk = {")
		for _, k := range keys {
			b.WriteString("[")
			b.WriteString(luaStrLit(k))
			b.WriteString("] = true, ")
		}
		b.WriteString(`}
    local kill2 = {}
    local k2 = __next(__t)
    while k2 ~= nil do
      if __type(k2) ~= "string" or not __nk[k2] then kill2[#kill2+1] = k2 end
      k2 = __next(__t, k2)
    end
    for i = 1, #kill2 do __rawset(__t, kill2[i], nil) end
  end end
`)
	}
	b.WriteString("end\n")
}

// luaStrLit renders s as a quoted Lua string literal (safe for any
// byte content the enumeration yields). Table-constructor keys wrap
// it in brackets at the call sites.
func luaStrLit(s string) string {
	var b strings.Builder
	b.WriteString(`"`)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"' || c == '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case c == '\n':
			b.WriteString(`\n`)
		case c < 0x20 || c == 0x7f:
			b.WriteString(`\`)
			b.WriteString(strconv.Itoa(int(c)))
		default:
			b.WriteByte(c)
		}
	}
	b.WriteString(`"`)
	return b.String()
}
