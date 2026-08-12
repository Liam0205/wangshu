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
-- Render reference addresses at a FIXED WIDTH on the oracle side (#233).
--
-- NormalizeOutput rewrites "table: 0x..." to a token, which makes the printed forms comparable -- but
-- it cannot help a script that measures one: #tostring({}) is 21 on PUC (%p gives 12 hex digits here)
-- and 17 on wangshu (0x%08x gives 8), so the LENGTH diverges before any normalization runs. Rewriting
-- the oracle's own rendering to wangshu's 8-digit form makes the two lengths agree, which is the same
-- "eliminate the difference at the rendering site" choice the NaN sign handling already makes.
local __sformat, __sgsub_raw, __ssub = string.format, string.gsub, string.sub
-- Renders one address at wangshu's 0x%08x width. Uses only the locals captured above: reaching
-- string.sub/string.gsub through method syntax (hex:sub(...)) would read the LIVE globals, so a fuzz
-- script could set string.sub = nil and break the oracle's own rendering, or return a value of its
-- choosing and steer it (audit finding).
local function __narrow(pre, hex)
  local lo = hex
  if #hex > 8 then lo = __ssub(hex, -8) end
  while #lo < 8 do lo = "0" .. lo end
  return pre .. "0x" .. lo
end
local __rawtostring = __tostring
-- Gate on the VALUE'S TYPE, not on the rendered text.
--
-- An earlier version matched "^(%a+: )0x(%x+)$" against every tostring result, which corrupted a
-- script's own strings: print("n: 0x1") became "n: 0x00000001" and #tostring("n: 0xff") became 13
-- instead of 7. Worse, a script printing a genuinely engine-dependent hex quantity had it funnelled
-- through the same 32-bit truncation on BOTH sides, collapsing a real divergence into equality --
-- corrupting both sides identically is exactly what hides a difference (audit finding).
--
-- No __tostring guard: PUC's file handles are userdata WITH a __tostring rendering "file (0x...)",
-- which is the #233 case itself, so excluding metamethod renderings would exclude the thing this
-- exists for. The type gate keeps script strings out; the patterns below only fire on tostring's own
-- spellings.
local __refkind = {table = true, ["function"] = true, thread = true, userdata = true}
__tostring = function(...)
  -- Forwarded verbatim so the arity error survives: PUC raises "bad argument #1 to 'tostring'
  -- (value expected)" with no argument, and an optional parameter silently dropped that error path
  -- from comparison on both engines.
  local v = ...
  local s = __rawtostring(...)
  if __type(s) ~= "string" or not __refkind[__type(v)] then return s end
  local body, n = __sgsub_raw(s, "^(%a+: )0x(%x+)$", __narrow)
  if not n or n == 0 then
    -- "file (0x...)" is in addrRe's list too; omitting it left #tostring(io.stdout) diverging.
    body, n = __sgsub_raw(s, "^(file %()0x(%x+)%)$", function(pre, hex)
      return __narrow(pre, hex) .. ")"
    end)
  end
  if n and n > 0 then return body end
  return s
end
tostring = __tostring
local __ipairs = ipairs
-- Cumulative BULK-WORK budget, shared by every shim that can move or build O(n) data inside
-- one uninterruptible C call.
--
-- The unit is BYTES, not elements. Counting elements undercharged by orders of magnitude for
-- anything holding large strings -- 64 concat pieces of 64 KiB each cost 9 seconds while
-- charging 64 -- and the whole point of the accumulator is that the instruction-count hook
-- cannot see inside these calls, so the charge has to track the actual work.
--
-- __bulkCap bounds a SINGLE call; __bulkTotal bounds the script. Four audit rounds found four
-- members of this family one at a time, each because the previous predicate keyed on something
-- correlated with cost rather than cost itself.
local __bulkCap = 4194304
local __bulkTotal = 0
local __bulkBudget = 16777216
local __chargeBulk

-- Kept as a separate name because the shift shims charge elements moved, which for a table of
-- small values is the honest cost unit there.
local __shiftTotal = 0
local __concat, __error = table.concat, error

-- __chargeBulk raises the limit sentinel when one call, or the script in total, exceeds the
-- bulk-work budget. Defined once so every shim charges the same accumulator with the same units.
-- __asNum and __asStr mirror luaL_checkinteger/luaL_checklstring's COERCION, which every one of
-- these C functions applies before doing any work.
--
-- Charging on the raw Lua type instead leaked both ways: string.rep(1, 4194304) built 4 MiB per
-- call and was charged nothing, because the subject was a number rather than a string (a loop of
-- it cost 4m45s while comparing normally), and table.remove(t, "1") bypassed the shift budget
-- entirely. In the other direction string.sub(s, "1048576") was charged from index 1 and
-- table.concat(t, ",", "1", "3") was charged the whole table, so cheap comparable inputs were
-- wrongly skipped.
local __tonum = tonumber
local __rawget = rawget
local __asNum0 = function(v)
  local tv = __type(v)
  if tv == "number" then return v end
  if tv == "string" then return __tonum(v) end
  return nil
end
local __asNum = __asNum0
local __asStr = function(v)
  local tv = __type(v)
  if tv == "string" then return v end
  if tv == "number" then return __tostring(v) end
  return nil
end

__chargeBulk = function(bytes, what)
  if bytes > __bulkCap then
    __error("` + LimitSentinel + `: " .. what .. " size", 0)
  end
  __bulkTotal = __bulkTotal + bytes
  if __bulkTotal > __bulkBudget then
    __error("` + LimitSentinel + `: " .. what .. " budget", 0)
  end
end
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
    local v = (__select(i, ...))
    if i > 1 then __emit("\t") end
    local s = __tostring(v)
    if __type(s) ~= "string" then
      __error("'tostring' must return a string to 'print'")
    end
    __emit(s)
  end
  __emit("\n")
end
-- The file-handle :write methods route through the same accumulator as io.write.
--
-- Without this a script using io.stdout:write(...) leaks straight to the real stdout and
-- the text is missing from BOTH captured outputs -- the two sides still agree, so no
-- divergence is reported, but the comparison silently stops covering whatever was written.
-- That is the same failure mode as a skip that hides a class: green for the wrong reason.
--
-- io.stderr's writes are dropped rather than accumulated, matching how the harness treats
-- stderr elsewhere (only stdout is compared).
local __wrapHandleWrite = function(h, capture)
  if h == nil then return end
  local mt = getmetatable(h)
  if mt == nil or mt.write == nil then return end
  local orig = mt.write
  mt.write = function(self, ...)
    if self ~= h then return orig(self, ...) end
    local n = __select("#", ...)
    for i = 1, n do
      local v = (__select(i, ...))
      local tv = __type(v)
      if tv ~= "string" and tv ~= "number" then
        return orig(self, ...) -- let the real method raise the argument error
      end
      if capture then __emit(__tostring(v)) end
    end
    return true
  end
end

io.write = function(...)
  local n = __select("#", ...)
  for i = 1, n do
    local v = (__select(i, ...))
    local tv = __type(v)
    if tv == "number" then
      -- tostring, not string.format("%.14g"): PUC renders numbers with
      -- LUA_NUMBER_FMT (= %.14g) for BOTH tostring and io.write, so
      -- tostring is faithful there; going through each engine's own
      -- tostring keeps nonfinite spellings (inf/nan) consistent with
      -- the engine's print output instead of forking on the format
      -- function's C-vs-Go %g behavior.
      __emit(__tostring(v))
    elseif tv == "string" then
      __emit(v)
    else
      __error("bad argument #" .. i .. " to 'write' (string expected, got " .. tv .. ")")
    end
  end
  return true
end
__wrapHandleWrite(io.stdout, true)
__wrapHandleWrite(io.stderr, false)
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
dofile = function() error("oracle-harness: dofile disabled") end
-- __ipairs_iter: wangshu implements ipairs via this internal global
-- (its enumeration puts it on the whitelist, so the trim keeps it).
-- PUC has no such global; give it PUC's own ipairs aux iterator so
-- BOTH sides expose a working function with the same (t, i) ->
-- (i+1, t[i+1]) | nil contract. Symmetric text, equivalent result.
__ipairs_iter = __ipairs_iter or select(1, ipairs({}))
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

-- __ckint0 is luaL_checkint's chain, available this early in the prelude (math.floor is not
-- localised until later). __ckint below is the same function under the name the budget shims use.
-- Captured BEFORE any fuzz script runs. Reading them live was exploitable once this helper started
-- gating a segfault (#244): assigning a fake math.floor then made the narrowing return 0, the
-- unpack guard did not fire, and the oracle took a SIGSEGV that killed the test binary. Harmless while
-- __ckint0 only fed budget shims, load-bearing now. The file already records this hazard twice, at
-- __narrow and at preludeSortedIter -- reusing __ckint0 inherited its corrections and also this gap.
local __floor0, __tonum0 = math.floor, tonumber
local __ckint0 = function(v, dflt)
  local tv = __type(v)
  local x
  if tv == "number" then x = v elseif tv == "string" then x = __tonum0(v) end
  if x == nil then return dflt end
  local i64 = x >= 0 and __floor0(x) or -__floor0(-x)
  local i32 = i64 % 4294967296
  if i32 >= 2147483648 then i32 = i32 - 4294967296 end
  return i32
end

local __rep = string.rep
string.rep = function(s, n)
  if __type(s) == "number" then s = __tostring(s) end
  -- The count narrows through luaL_checkint before any work: string.rep("x", 4294967297) is one
  -- repetition in lua5.1, not four billion. This pre-existing guard read the raw number, so it
  -- fired on that input and excluded it from comparison -- the same narrowing gap the budget
  -- shims had, in the guard that runs before them.
  local nN = __ckint0(n, nil)
  if __type(s) == "string" and nN ~= nil and nN > 0 and #s * nN > 4194304 then
    __error("oracle-harness: rep too large", 0)
  end
  return __rep(s, n)
end

-- string.format unsigned-verb UB guard: %u/%x/%X/%o cast the argument
-- through C (unsigned long long)(double), which is UB outside
-- (-1, 2^64) — x86-64 cvttsd2si (NaN/negative -> 0x8000000000000000
-- indefinite) and arm64 FCVTZU (saturate to 0 / 0xFFFF...) genuinely
-- disagree, so the two official PUC builds diverge from EACH OTHER by
-- arch. wangshu pins the x86-64 behavior on all arches; comparison in
-- the UB range is therefore meaningless against a non-x86 oracle.
-- Reroute to the limit sentinel (skip, not divergence). The verb scan
-- can false-positive on literal "%%x" — a lost input, never a false
-- verdict.
local __tonumber, __sformat = tonumber, string.format
string.format = function(f, ...)
  -- The only thing this wrapper still does is the unsigned-verb UB guard
  -- above. NaN rendering needs nothing here: the oracle normalizes its own
  -- NaN text (internal/oracle/lua515.c), so a NaN reaches the output with the
  -- same bytes on both engines and is compared exactly like any other value.
  local unsigned = false
  if __type(f) == "string" and __sfind(f, "%%[%-%+ #0-9%.]*[uxXo]") then
    unsigned = true
  end
  if unsigned then
    local n = __select("#", ...)
    for i = 1, n do
      local v = __tonumber((__select(i, ...)))
      if v ~= nil and (v ~= v or v <= -1 or v >= 18446744073709551616) then
        __error("` + LimitSentinel + `: unsigned-cast UB range", 0)
      end
    end
  end
  return __sformat(f, ...)
end

-- math two-arg argument-order guard.
--
-- PUC writes math.fmod/pow/ldexp as f(luaL_checknumber(L,1), luaL_checknumber(L,2)),
-- and C leaves the evaluation order of those arguments UNSPECIFIED. gcc on x86-64
-- evaluates right to left, so that oracle reports "bad argument #2" for a missing
-- argument; the arm64 build reports #1. The two official builds disagree with each
-- other, so which index is "correct" is not a Lua fact and there is nothing to
-- align to -- wangshu reports the first missing argument and the comparison skips
-- the shape.
--
-- Only the case where MORE THAN ONE argument is bad or missing is affected; a
-- single bad argument names the same index either way and stays compared.
local function __wrapArgOrder(tbl, name)
  local orig = tbl[name]
  if orig == nil then return end
  tbl[name] = function(...)
    local k = __select("#", ...)
    local bad = 0
    for i = 1, 2 do
      local v = i <= k and (__select(i, ...)) or nil
      if __tonumber(v) == nil then bad = bad + 1 end
    end
    if bad > 1 then
      __error("` + LimitSentinel + `: unspecified C arg evaluation order", 0)
    end
    return orig(...)
  end
end
-- EVERY two-number math entry, including the 5.0 compatibility ALIASES. Wrapping fmod but not
-- math.mod -- which is the same C function under its 5.0 name -- left the identical shape
-- reportable, and the fuzzer filed it twice (#217, #219). math.atan2 was never wrapped either.
--
-- The list is derived from the math table rather than from memory: anything taking two numbers
-- through luaL_checknumber has this property, so the wrapper is applied to all of them.
__wrapArgOrder(math, "fmod")
__wrapArgOrder(math, "mod") -- LUA_COMPAT_MOD alias for fmod
__wrapArgOrder(math, "pow")
__wrapArgOrder(math, "ldexp")
__wrapArgOrder(math, "atan2")

-- luaL_checkint UB guard, shared by every function that narrows an int argument.
--
-- PUC reads these arguments with (int)luaL_checkinteger, i.e. double -> int64 ->
-- int32. Outside int64 range (and for NaN) that first cast is UB and the two
-- official builds DISAGREE: x86-64 cvttsd2si yields INT64_MIN, whose low 32 bits
-- are 0, while arm64 FCVTZS saturates +inf to INT64_MAX, whose low 32 bits are -1.
-- wangshu pins the x86-64 result, so comparing this range against a non-x86 oracle
-- is meaningless.
--
-- One guard rather than twelve near-identical ones: the arm64 oracle-smoke job
-- caught a single table.insert seed, but probing found EVERY narrowing site
-- exposed the same way -- %c, string.char, the ipairs iterator, tonumber's base,
-- string.rep, table.remove, table.concat's bounds, select, math.ldexp,
-- math.random, gsub's count and error's level. A per-site guard would have to be
-- remembered at each new site; this one covers the class.
local __nonfinite = function(v)
  local n = __tonumber(v)
  return n ~= nil and (n ~= n or n >= 9223372036854775808 or n < -9223372036854775808)
end
local function __wrapUB(tbl, name, from)
  local orig = tbl[name]
  if orig == nil then return end
  tbl[name] = function(...)
    local k = __select("#", ...)
    for i = from, k do
      if __nonfinite((__select(i, ...))) then
        __error("` + LimitSentinel + `: luaL_checkint UB range", 0)
      end
    end
    return orig(...)
  end
end
-- Argument positions counted from the first one that is narrowed.
__wrapUB(string, "char", 1)
__wrapUB(string, "rep", 2)
__wrapUB(string, "gsub", 4)
-- string.format is NOT wrapped wholesale: only %c narrows its argument, while the
-- float verbs take the value as a double and must stay comparable (a NaN there is
-- the whole point of the rendering normalization). Guard on the fmt containing a
-- %c conversion.
string.format = (function(orig)
  return function(f, ...)
    if __type(f) == "string" and __sfind(f, "%%[%-%+ #0-9%.]*c") then
      -- %c uses (int)luaL_checkNUMBER, a DIRECT double -> int32, so its UB range
      -- starts at int32 rather than int64: 2^40+65 is inside int64 (the two-step
      -- cast keeps its low 32 bits) but outside int32, where x86 gives INT32_MIN
      -- and arm64 saturates elsewhere. Guarding only nonfinite left that case
      -- compared, which is what the arm64 job failed on next.
      local k = __select("#", ...)
      for i = 1, k do
        local n = __tonumber((__select(i, ...)))
        if n ~= nil and (n ~= n or n >= 2147483648 or n < -2147483648) then
          __error("` + LimitSentinel + `: luaL_checknumber int32 UB range", 0)
        end
      end
    end
    return orig(f, ...)
  end
end)(string.format)
__wrapUB(table, "insert", 2)
-- unpack crashes PUC when its element count overflows int (#244).
--
-- unpack of an empty table with 0x80000000 SEGFAULTS the embedded 5.1.5, and so does the real lua5.1
-- binary. luaB_unpack narrows i and e to int, returns early only when i > e, then computes
-- n = e - i + 1 in int: its n <= 0 overflow guard misses the case where that wrap lands
-- positive-and-huge, and lua_checkstack is then asked for an absurd count.
--
-- The condition needs BOTH indices. A first version guarded two fixed values of i, measured on
-- unpack({}, i) -- where e is 0 -- and was wrong in both directions: unpack({1,2,3}, -2147483646)
-- still crashed (the window shifts with e), while unpack({1,2,3}, 4294967297) was skipped even though
-- it narrows to i=1 and both sides answer 3. Dense sampling along one axis cannot reveal a missing
-- axis, so "measured rather than derived" was exactly the wrong lesson: the mechanism supplies the
-- shape, and measurement then confirms it. Predicted against measured on 10 shapes, all agreeing.
--
-- Skipped rather than compared, like the other PUC UB ranges: an oracle that dies is not a reference.
-- Narrowing goes through __ckint0, luaL_checkint's own chain, NOT a hand-rolled modulo.
--
-- A first version wrote its own __i32 doing only the modulo step, and three of the four steps it skipped
-- were each a live crash: a nonfinite index returned nil and disabled the guard (PUC narrows inf/NaN to
-- 0, and unpack({}, 1/0, 2147483647) crashes); a NUMERIC STRING was rejected by a __type check although
-- luaL_optint coerces it (unpack({}, "-2147483648") crashes); and a beyond-int64 finite value took the
-- wrong branch of the UB cast (1e20 gave 1661992960 instead of PUC's 0, so unpack({}, 1e20, 2147483647)
-- crashed). It was also too WIDE without truncation toward zero: unpack({}, -2147483646.5) got skipped
-- where PUC truncates to exactly INT_MAX and raises cleanly, which both engines agree on.
--
-- __ckint0's own comment already says this chain "drifted between shims twice ... same bug, same shape,
-- two places". This was the third. When a file already contains the reference implementation of a
-- conversion, re-deriving it is the mistake -- reuse is not merely tidier, it is the only way to inherit
-- the corrections the original absorbed.
local __unpackIdx = function(v, dflt)
  if v == nil then return dflt end
  local x = v
  -- __tonum0, not the live global: a script reassigning tonumber could otherwise steer this and defeat
  -- the guard, which is the same hole __ckint0 had one round earlier.
  if __type(x) == "string" then x = __tonum0(x) end
  if __type(x) ~= "number" then return nil end   -- not coercible: PUC raises, so let it compare
  -- Beyond int64, or nonfinite: the double->int64 cast is UB and x86-64's cvttsd2si yields INT64_MIN,
  -- whose low 32 bits are 0. Verified against the binary: unpack({1,2,3}, 1e20) answers 4, i.e. PUC
  -- used index 0 -- NOT __ckint0's modulo, which would give 1661992960. __ckint0 is right for values
  -- inside int64 and this is the one case it cannot express, so the branch lives here rather than
  -- changing a helper the other shims depend on.
  if x ~= x or x >= 9223372036854775808 or x <= -9223372036854775808 then return 0 end
  return __ckint0(x, nil)
end
_G.unpack = (function(orig)
  return function(t, i, j, ...)
    local iv = __unpackIdx(i, 1)
    local ev
    if j ~= nil then
      ev = __unpackIdx(j, nil)
    elseif __type(t) == "table" then
      ev = #t
    end
    if iv ~= nil and ev ~= nil and iv <= ev and (ev - iv + 1) > 2147483647 then
      __error("` + LimitSentinel + `: unpack element count overflows int (PUC segfaults)", 0)
    end
    return orig(t, i, j, ...)
  end
end)(unpack)
__wrapUB(table, "remove", 2)
__wrapUB(table, "concat", 3)
__wrapUB(math, "ldexp", 2)
__wrapUB(math, "random", 1)
_G.select = (function(orig)
  return function(n, ...)
    if __nonfinite(n) then
      __error("` + LimitSentinel + `: luaL_checkint UB range", 0)
    end
    return orig(n, ...)
  end
end)(select)
_G.tonumber = (function(orig)
  return function(v, b, ...)
    if b ~= nil and __nonfinite(b) then
      __error("` + LimitSentinel + `: luaL_checkint UB range", 0)
    end
    return orig(v, b, ...)
  end
end)(tonumber)
_G.__ipairs_iter = (function(orig)
  if orig == nil then return nil end
  return function(t, i, ...)
    if __nonfinite(i) then
      __error("` + LimitSentinel + `: luaL_checkint UB range", 0)
    end
    return orig(t, i, ...)
  end
end)(__ipairs_iter)

-- table.insert shift-span guard.
--
-- 5.1's tinsert shifts [pos, #t] up one with NO bound on the distance, so a
-- position far below 1 makes the loop run |pos| times: pos = 2^31 narrows to
-- INT32_MIN under luaL_checkint and the official build performs ~2.1 billion
-- rawget/rawset pairs, measured here at 2m31s. That is not a semantic difference
-- -- both engines would compute the same table -- but it runs inside a builtin
-- where wangshu's step budget cannot interrupt it, so wangshu caps the span and
-- raises instead of hanging an embedded host (12 section 4.9).
--
-- Comparing the capped range is therefore meaningless: one side raises, the other
-- grinds. Skip it. The threshold is read from the ARGUMENTS, which both engines
-- see identically, not from either engine's behaviour.
local __floor = math.floor

-- __ckint is luaL_checkint's WHOLE chain: coerce, truncate toward zero, narrow to int32.
--
-- It exists as one helper because the chain drifted between shims twice. table.concat first had
-- only the coercion, so a range end of 4294967297 sent the byte scan over ~4 billion indices; then
-- string.rep still had only the coercion, so string.rep("x", 4294967297) -- which lua5.1 narrows to
-- 1 and answers with "x" -- was charged 4 GiB and skipped. Same bug, same shape, two places.
local __ckint = function(v, dflt)
  local n = __asNum0(v)
  if n == nil then return dflt end
  local i32
  if n >= 9223372036854775808 or n < -9223372036854775808 then
    -- Outside int64 range the C cast yields INT64_MIN, whose low 32 bits are zero. This case
    -- was handled only in the insert shim's hand-rolled copy; folding it in here is what makes
    -- one helper able to replace all of them.
    i32 = 0
  else
    local i64 = n >= 0 and __floor(n) or -__floor(-n)
    i32 = i64 % 4294967296
  end
  if i32 >= 2147483648 then i32 = i32 - 4294967296 end
  return i32
end

local __tinsert0 = table.insert
table.insert = function(t, ...)
  local n = __select("#", ...)
  if n >= 2 then
    local p = __tonumber((__select(1, ...)))
    -- Nonfinite or beyond-int64 positions are the SAME UB as string.char's cast,
    -- so skip them outright rather than trying to compare the narrowed index.
    -- x86-64 cvttsd2si sends them to INT64_MIN (low 32 bits 0, so t[0]); arm64
    -- FCVTZS saturates +inf to INT64_MAX (low 32 bits -1, so t[-1]). The product
    -- pins the x86 result, so a non-x86 oracle legitimately disagrees -- this is
    -- what made the arm64 oracle-smoke job fail while x86 passed.
    if p ~= nil and (p ~= p or p >= 9223372036854775808 or p < -9223372036854775808) then
      __error("` + LimitSentinel + `: insert-position cast UB range", 0)
    end
    if p ~= nil and p == p then
      -- Narrow to int32 exactly as luaL_checkint does before measuring the span.
      -- Reasoning about the pre-narrowing value is what made the first version of
      -- this guard miss: pos = 2^31 looks huge and positive, so "span = #t+1-pos"
      -- came out negative and nothing fired -- while PUC had already turned it
      -- into INT32_MIN, giving a span of 2^31 and a two-minute shift.
      -- No 2^53 gate. Bounding the narrowing to the exactly-representable range
      -- left everything above it neither narrowed nor skipped, which put the
      -- original defect straight back one octave up: 2^54+2^31 still had the
      -- oracle grinding while wangshu returned at once, and
      -- 2^53+4286578688 was a live divergence being COMPARED rather than
      -- skipped. Values beyond int64 all land on INT64_MIN under the C cast, so
      -- they narrow to 0 and are ordinary; everything else goes through the same
      -- modular reduction as the product.
      do
        local i32
        if p >= 9223372036854775808 or p < -9223372036854775808 then
          i32 = 0 -- C cast yields INT64_MIN; its low 32 bits are zero
        else
          local i64 = p >= 0 and __floor(p) or -__floor(-p)
          i32 = i64 % 4294967296
        end
        if i32 >= 2147483648 then i32 = i32 - 4294967296 end
        -- Two thresholds, deliberately different, because they answer different
        -- questions.
        --
        -- The PRODUCT cap (2^27) is a correctness boundary: below it wangshu must
        -- perform the shift, because lua5.1 does and rejecting it would diverge.
        -- Keyed on the distance below index 1, not the table's element count --
        -- keying on the count made this skip fire for ordinary inserts into large
        -- tables and hid the product rejecting them.
        --
        -- This SKIP is set far lower (2^20), and only for the differential
        -- comparison. A span of ~100M is correct and symmetric on both engines but
        -- costs seconds, and the fuzz coordinator replays the whole corpus in
        -- parallel at startup, so such an input takes the worker down. Three
        -- separate nightly crashers (#203 and two before it) were all this shape,
        -- each hand-moved to test/regression/ afterwards. Skipping the expensive
        -- band stops the fuzzer refiling it, while test/regression keeps serial
        -- coverage of the work actually being done.
        --
        -- The budget is CUMULATIVE as well as per-call. Keying only on a single call's
        -- span left a loop of just-below-threshold inserts fully compared: 99 iterations
        -- at 2^20-6 elements each costs about 11 seconds across the two engines, which is
        -- the same corpus hazard in the same family, merely split across calls. The
        -- instruction-count hook cannot interrupt it either, because each shift happens
        -- inside one C call.
        -- The cost is the number of elements MOVED, and that does not depend on the sign
        -- of the position. Both checks used to sit inside the negative-position branch, so
        -- a positive position was neither span-checked nor charged: inserting at position 1
        -- in a 40000-iteration loop shifts the whole array every call and cost 15 seconds
        -- across the two engines while comparing normally. Removing at position 1 in a loop
        -- is the same shape.
        --
        -- NOTE: no backticks in this comment -- the prelude is a Go raw string literal, and
        -- a backtick here silently ends it, which produces a confusing syntax error far
        -- from the real cause.
        local span
        if i32 < 1 then
          span = 1 - i32
        else
          local n = #t
          span = n + 1 - i32
          if span < 0 then span = 0 end
        end
        if span > 1048576 then
          __error("` + LimitSentinel + `: table.insert shift span", 0)
        end
        __shiftTotal = __shiftTotal + span
        if __shiftTotal > 4194304 then
          __error("` + LimitSentinel + `: table.insert shift budget", 0)
        end
      end
    end
  end
  return __tinsert0(t, ...)
end

-- table.remove is charged to the same budget: removing at a low position shifts every
-- element above it down, so a loop of table.remove(t, 1) costs the same as a loop of
-- table.insert(t, 1, v) and was equally unaccounted while the budget lived only in the
-- insert shim.
local __tremove0 = table.remove
table.remove = function(t, ...)
  if __type(t) == "table" and __select("#", ...) >= 1 then
    local i32 = __ckint((__select(1, ...)), nil)
    if i32 ~= nil then
      local n = #t
      local span = n - i32
      if span < 0 then span = 0 end
      if span > 1048576 then
        __error("` + LimitSentinel + `: table.remove shift span", 0)
      end
      __shiftTotal = __shiftTotal + span
      if __shiftTotal > 4194304 then
        __error("` + LimitSentinel + `: table.remove shift budget", 0)
      end
    end
  end
  return __tremove0(t, ...)
end

-- table.concat is charged to the same budget, for the same reason: it builds an O(n) string
-- inside one uninterruptible C call, so a loop of concats over a large table is expensive and
-- the instruction hook cannot see it. Found by sweeping every stdlib call that can move or
-- build O(n) data in one call, after three audit rounds had each found one more member of this
-- family: 2000 concats over a 200000-element table cost 28 seconds across the two engines and
-- compared normally.
--
-- The charge is the number of ELEMENTS joined, matching how the shift budget charges elements
-- moved, so one accumulator bounds the whole family.
-- The rest of the family, charged to the same budget in the same units.
--
-- Each of these builds or rearranges O(n) data inside one C call, so the instruction hook
-- cannot interrupt it, and each was measured expensive ONLY in loop form -- a single call is
-- milliseconds, which is why an earlier sweep that timed single calls concluded they were all
-- cheap. Loop form is the shape that matters here: the fuzz coordinator replays a corpus, and
-- a script may repeat one call thousands of times inside the instruction budget.
local __srep0 = string.rep
string.rep = function(sv, n, ...)
  -- The count goes through luaL_checkint, so it narrows to int32 before any work happens.
  local s2 = __asStr(sv)
  local n2 = __ckint(n, nil)
  if s2 ~= nil and n2 ~= nil and n2 > 0 then
    __chargeBulk(#s2 * n2, "string.rep")
  end
  return __srep0(sv, n, ...)
end

for _, name in __ipairs({"upper", "lower", "reverse"}) do
  local orig = string[name]
  if orig ~= nil then
    string[name] = function(sv, ...)
      local s2 = __asStr(sv)
      if s2 ~= nil then
        __chargeBulk(#s2, "string." .. name)
      end
      return orig(sv, ...)
    end
  end
end

-- string.gsub deliberately gets NO bulk charge: __patcheck already caps pattern subjects at
-- 256 bytes and patterns at 48, so gsub cannot reach a size where the bulk budget would matter.
-- A shim here measured as dead code -- every loop form I tried was already excluded at 2ms by
-- that guard, which is why the enforcer below does not list gsub.
-- string.sub charges the EXTRACTED length, not the subject's: s:sub(2) on a 1 MiB string copies
-- ~1 MiB every call, and 20000 such calls cost 12.5 seconds while comparing normally. Found by
-- enumerating the family in LOOP form, which is the step my earlier sweep skipped -- it timed
-- single calls, where every member of this family is milliseconds.
local __ssub0 = string.sub
string.sub = function(sv, i, ...)
  local s2 = __asStr(sv)
  if s2 ~= nil then
    -- str_sub uses luaL_checkINTEGER, not luaL_checkint: lua_Integer is 64-bit here, so the
    -- indices are NOT narrowed to int32. Narrowing them made s:sub(4294967297) look like index 1
    -- and charged a full copy of the subject, so 17 iterations over a 1 MiB string tripped the
    -- cumulative budget -- while lua5.1 clamps the start past the end and returns "" instantly.
    --
    -- Which luaL_check* a function uses has to be read per function; the same argument position in
    -- a sibling function is not evidence.
    --
    -- An explicit but unconvertible index still means the real call raises, so charge nothing.
    local n = #s2
    local from = 1
    if i ~= nil then
      from = __asNum0(i)
      if from == nil then return __ssub0(sv, i, ...) end
      from = from >= 0 and __floor(from) or -__floor(-from)
    end
    local to = -1
    if __select("#", ...) >= 1 then
      to = __asNum0((__select(1, ...)))
      if to == nil then return __ssub0(sv, i, ...) end
      to = to >= 0 and __floor(to) or -__floor(-to)
    end
    if from < 0 then from = n + from + 1 end
    if to < 0 then to = n + to + 1 end
    if from < 1 then from = 1 end
    if to > n then to = n end
    if to >= from then
      __chargeBulk(to - from + 1, "string.sub")
    end
  end
  return __ssub0(sv, i, ...)
end

local __tsort0 = table.sort
table.sort = function(t, ...)
  -- Mirror sort's own comparator check BEFORE charging: lua5.1 validates that argument 2 is a
  -- function (or absent/nil) and raises at once otherwise, so table.sort(bigTable, {}) must report
  -- that error rather than being charged n*log2(n) and skipped.
  if __select("#", ...) >= 1 then
    local cmp = (__select(1, ...))
    if cmp ~= nil and __type(cmp) ~= "function" then
      return __tsort0(t, ...)
    end
  end
  if __type(t) == "table" then
    -- Sorting compares about n*log2(n) times, so charging n undercharged by the log factor:
    -- 100 sorts of 200000 elements still burned 4 seconds before the budget tripped. Charge
    -- the comparison count instead, approximating log2(n) by the bit length of n.
    local n = #t
    local lg = 0
    local m = n
    while m > 1 do
      m = __floor(m / 2)
      lg = lg + 1
    end
    if lg < 1 then lg = 1 end
    __chargeBulk(n * lg, "table.sort")
  end
  return __tsort0(t, ...)
end

local __tconcat0 = table.concat
table.concat = function(t, ...)
  if __type(t) == "table" then
    -- Charge BYTES over the REQUESTED range, not elements over the whole table.
    --
    -- Two bugs came from getting either half wrong. Counting elements let 64 elements of
    -- 64 KiB each cost 9 seconds while charging only 64 -- the cost of building a string is
    -- its length, not how many pieces it came in. And reading #t rather than the [i,j]
    -- arguments made table.concat(t, ",", 1, 3) on a 2M-element table skip, even though it
    -- joins three elements in microseconds; that is the same mistake the insert shim's own
    -- comment warns about, repeated here.
    -- luaL_checkint, not just tonumber: truncate TOWARD ZERO then narrow to int32, the same
    -- two-step the insert/remove shims above do. Skipping the narrowing meant
    -- table.concat({"x"}, "", 1, 4294967297) -- which lua5.1 narrows to 1 and answers instantly
    -- -- sent the scan below over ~4 billion indices until the instruction budget tripped, so a
    -- perfectly comparable input was recorded as a skip.
    -- "absent" and "present but unconvertible" are different: the real call raises a bad-argument
    -- error for the latter, immediately and cheaply. Treating an unconvertible bound as the default
    -- made the scan charge a FABRICATED range -- table.concat(t, sep, {}, 100) with a 64 KiB
    -- separator accumulated ~6.5 MiB and raised the sentinel, so an input that fails at once was
    -- recorded as a skip. When a bound cannot convert, charge nothing and let the real call report.
    local bad = false
    local i, j = 1, #t
    if __select("#", ...) >= 2 then
      i = __ckint((__select(2, ...)), nil)
      if i == nil then bad = true end
    end
    if __select("#", ...) >= 3 then
      j = __ckint((__select(3, ...)), nil)
      if j == nil then bad = true end
    end
    if bad then
      return __tconcat0(t, ...)
    end
    -- Charge only what the real call will actually touch, and STOP at the first element it
    -- would reject.
    --
    -- Charging the whole requested range's separators up front, then scanning past an invalid
    -- element, excluded inputs that lua5.1 answers immediately:
    -- table.concat({}, string.rep("x",65536), 1, 100) reports "invalid value (nil) at index 1"
    -- in microseconds, but the shim first charged ~6.5 MiB of separators and raised the limit
    -- sentinel -- and a limit error is read as a skip, so the input silently left the
    -- comparison. An empty separator with a large j walked the whole invalid range instead.
    local bytes = 0
    if j >= i then
      -- Same rule as the bounds: an EXPLICIT separator that cannot convert means the real call
      -- raises bad argument #2 at once. Treating it as length 0 and scanning on charged ~65
      -- elements of a large table before raising the sentinel, so that input left the comparison.
      local seplen = 0
      if __select("#", ...) >= 1 then
        local sep = __asStr((__select(1, ...)))
        if sep == nil then
          return __tconcat0(t, ...)
        end
        seplen = #sep
      end
      local k = i
      while k <= j do
        local v = __rawget(t, k)
        local tv = __type(v)
        if tv == "string" then
          bytes = bytes + #v
        elseif tv == "number" then
          bytes = bytes + 8
        else
          break -- the real call raises here, so nothing beyond this is ever touched
        end
        if k > i then
          bytes = bytes + seplen
        end
        if bytes > __bulkCap then break end
        k = k + 1
      end
    end
    __chargeBulk(bytes, "table.concat")
  end
  return __tconcat0(t, ...)
end

-- string.char UB guard, same reasoning as the unsigned verbs above.
--
-- PUC's str_char runs luaL_checkint, i.e. double -> lua_Integer -> int. Outside
-- int64 range (and for NaN) that first cast is UB and the two official builds
-- disagree: x86-64 cvttsd2si gives INT64_MIN, whose low 32 bits are 0, so PUC
-- accepts and emits byte 0; arm64 FCVTZS saturates +inf to INT64_MAX, whose low
-- 32 bits are -1, which fails the uchar(c) == c check and raises. wangshu pins
-- the x86-64 result, so comparing this range against a non-x86 oracle is
-- meaningless -- skip instead of reporting a divergence.
--
-- In-range values, including fractions and the [0,255] boundary, stay compared.
local __schar = string.char
string.char = function(...)
  local n = __select("#", ...)
  for i = 1, n do
    local v = __tonumber((__select(i, ...)))
    if v ~= nil and (v ~= v or v >= 9223372036854775808 or v < -9223372036854775808) then
      __error("` + LimitSentinel + `: char-cast UB range", 0)
    end
  end
  return __schar(...)
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
string.gsub = function(s, p, ...)
  -- The replacement and count are forwarded as VARARGS, not as named parameters.
  --
  -- Naming them turned a missing third argument into an explicit nil, and an
  -- explicit nil is a PASSED argument: gsub("", 0) reached the real gsub as
  -- (s, p, nil), which satisfies its "was arg 3 supplied" test and returned
  -- normally, while PUC raised "bad argument #3 (string/function/table
  -- expected)". This wrapper predates the branch but only became reachable once
  -- the gsub count handling changed which inputs get this far, and it showed up
  -- as a verdict-class divergence on arm64 first purely by fuzz ordering.
  s, p = __patcheck(s, p)
  return __patres(__pcall(__gsub2, s, p, ...))
end
`

// preludeSortedIter replaces the unordered iteration primitives with
// sort-then-replay versions. Keys of types lacking a total order sort
// by type rank then tostring -- tostring embeds addresses that differ
// across sides, so reference-keyed tables stay rank-grouped only; the
// fuzz target's address normalizer handles the printed form.
const preludeSortedIter = `
local __rawnext, __sort = next, table.sort
-- No local declaration here: preludeGuards' own local __rawtostring is STILL IN SCOPE, because Prelude()
-- concatenates every section into one Lua chunk.
--
-- Two earlier attempts failed for want of noticing that. Reading the global tostring here finds the
-- width-normalizing wrapper, and truncating to 8 hex digits makes more keys tie, changing the very order
-- this function exists to stabilize. Publishing the raw function as a GLOBAL instead was worse twice
-- over: the whitelist trim (step 4) runs before this section (step 5) and erased it, so every comparator
-- call raised "attempt to call a nil value" identically on both engines and the harness reported PASS
-- while masking everything after it; and once kept past the trim, the global handed scripts an
-- un-normalized PUC renderer, so taking the length of its io.stdout rendering was 21 against 17 -- reopening
-- exactly the divergence #233 exists to close, reachable by nothing more than walking the globals.
local __rank = { number = 1, string = 2, boolean = 3, table = 4,
                 ["function"] = 5, userdata = 6, thread = 7 }
local __keyorder = function(a, b)
  local ta, tb = __rank[__type(a)] or 8, __rank[__type(b)] or 8
  if ta ~= tb then return ta < tb end
  if ta <= 2 then return a < b end
  if ta == 3 then return (a and 1 or 0) < (b and 1 or 0) end
  return __rawtostring(a) < __rawtostring(b)
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
