//go:build (wangshu_p3 || wangshu_p4) && wangshu_profile

package difftest

import (
	"testing"

	"github.com/Liam0205/wangshu"
)

// tieredStdlibNameCases are the behaviours fixed for #329 in a form that really executes in the
// P3 / P4 compiled tiers. Two things make the obvious way of writing such a test prove nothing:
//
//   - A function that calls the global pairs / ipairs itself is not promoted (the bridge classifies
//     the call as unknown). The kernels therefore take the iterator triple, or the function under
//     test, as arguments.
//   - A promoted function that is invoked BY A HOST FUNCTION still has its frame run by the
//     interpreter. pcall(kernel, ...) is such an invocation, so a kernel whose every call sits
//     directly under pcall is compiled (PromotionCount > 0) and never executed compiled. Each kernel
//     that has to raise is therefore called through a small Lua wrapper, and the wrapper is what
//     pcall gets. The wrapper must not tail-call the kernel: on P3 a tail call does not enter
//     compiled code either.
//
// The test asserts GibbousEntries, the number of times execution entered compiled code, instead of
// assuming any of this.
//
// forced says whether force-all runs the kernel compiled on this build. Every kernel does on every
// build, except the one that reads a missing table field: see missingFieldCallRunsCompiled.
//
// auto says whether the kernel is also expected to run compiled, on both tiers, when promotion comes
// from natural heat under the lowered thresholds; some kernels only run compiled under force-all.
var tieredStdlibNameCases = []struct {
	name, src    string
	forced, auto bool
}{
	// A promoted generic-for drives the iterators that pairs / ipairs hand out, after the global next
	// is gone: pairs must not look it up again.
	{"generic_for_over_private_iterators", `
next = nil
local function loop(f, s, c) local n = 0 for _, v in f, s, c do n = n + v end return n end
local t = {1, 2, 3}
local total = 0
for i = 1, 60 do total = total + loop(ipairs(t)) + loop(pairs(t)) end
return total`, true, true},
	// ipairs itself, called from a promoted function. A function that calls the global is not
	// promoted, so the kernel reaches the factory as a method of the table, and is padded with
	// arithmetic so that both P4 native translators accept it. One kernel per case: the entry count
	// then says that this kernel ran compiled, not that one of several did.
	{"ipairs_called_from_compiled_code", `
local x = {10, 20, 30, ipairs = ipairs}
local function viaIpairs(t, i)
  local a = i * 2 + 1
  local b = a - i
  local c = b * 3 - a
  local d = c + b - i
  local e = d * 2 + c
  local g = e - d + a
  local n = 0
  for _, v in t:ipairs() do n = n + v end
  return n + g - g
end
local total = 0
for i = 1, 40 do total = total + viaIpairs(x, i) end
return total`, true, true},
	// The same for pairs.
	{"pairs_called_from_compiled_code", `
local x = {10, 20, 30, pairs = pairs}
local function viaPairs(t, i)
  local a = i * 2 + 1
  local b = a - i
  local c = b * 3 - a
  local d = c + b - i
  local e = d * 2 + c
  local g = e - d + a
  local n = 0
  for k in t:pairs() do n = n + 1 end
  return n + g - g
end
local total = 0
for i = 1, 40 do total = total + viaPairs(x, i) end
return total`, true, true},
	// The ipairs iterator narrows its control value like luaL_checkint, numeric strings included.
	{"ipairs_iterator_string_control", `
local function loop(f, s, c) local n = 0 for _, v in f, s, c do n = n + v end return n end
local t = {7, 8, 9}
local total = 0
for i = 1, 60 do total = total + loop(ipairs({}), t, "1") end
return total`, true, true},
	// Calling the name 5.1 does not have, from inside a promoted function, raises the same error.
	// Only the chunk name is cut off the position prefix, because it differs between the two engines.
	// The line number is compared, and every kernel that raises spans several lines so that a line
	// taken from the wrong instruction shows. This is
	// the input of #329 as written, a read of a missing field followed by a call; P4 on arm64 does not
	// compile that form, so there the three runs only compare results and the next case is the one
	// that raises from compiled code.
	{"missing_table_unpack_error", `
local function kernel(t)
  local a, b = table.unpack(t)
  return a, b
end
local function call(t) local a, b = kernel(t) return a, b end
local seen = {}
for i = 1, 40 do
  local ok, msg = pcall(call, {1, 2})
  seen[tostring(ok) .. " " .. tostring(msg):gsub("^.-:(%d+: )", "%1")] = true
end
local out = {}
for k in pairs(seen) do out[#out + 1] = k end
return table.concat(out, "|"), type(__ipairs_iter)`, missingFieldCallRunsCompiled, false},
	// The same call with the missing function reached as an upvalue and the kernel padded with
	// arithmetic, a form P3 and both P4 native translators run compiled under force-all. Under
	// natural heat P4 runs it compiled and P3 does not, hence auto = false.
	{"missing_table_unpack_upvalue_error", `
local unpack52 = table.unpack
local function kernel(t, x)
  local a = x * 2 + 1
  local b = a - x
  local c = b * 3 - a
  local d = c + b - x
  local e = d * 2 + c
  local f = e - d + a
  local p, q = unpack52(t)
  return p, q, f
end
local function call(t, x) local p, q, f = kernel(t, x) return p, q, f end
local seen = {}
for i = 1, 40 do
  local ok, msg = pcall(call, {1, 2}, i)
  seen[tostring(ok) .. " " .. tostring(msg):gsub("^.-:(%d+: )", "%1")] = true
end
local out = {}
for k in pairs(seen) do out[#out + 1] = k end
return table.concat(out, "|")`, true, false},
	// io.lines as the generator of a promoted generic-for: an explicit nil is rejected as a file handle
	// and a table or a boolean fails the string check, both attributed to the for generator as on 5.1. Nothing
	// is read from stdin. The loop is bounded because a generator that wrongly accepts its argument
	// would otherwise be called forever.
	{"io_lines_argument_errors", `
local function loop(f, s, c)
  local n = 0
  for _ in f, s, c do
    n = n + 1
    if n >= 3 then break end
  end
  return n
end
local function call(f, s, c) local n = loop(f, s, c) return n end
local seen, order = {}, {}
for i = 1, 40 do
  for _, arg in ipairs({"nil", "table", "boolean"}) do
    local ok, msg
    if arg == "nil" then ok, msg = pcall(call, io.lines, nil, nil)
    elseif arg == "table" then ok, msg = pcall(call, io.lines, {})
    else ok, msg = pcall(call, io.lines, false) end
    local line = arg .. ": " .. tostring(ok) .. " " .. tostring(msg):gsub("^.-:(%d+: )", "%1")
    if not seen[line] then seen[line] = true order[#order + 1] = line end
  end
end
return table.concat(order, " | ")`, true, true},
	// io.lines with a filename -- a string or a number -- as the generator of a promoted generic-for.
	// wangshu raises instead of opening the file, so this case is in tieredNotComparedWithOracle.
	{"io_lines_filename_error", `
local function loop(f, s, c)
  local n = 0
  for _ in f, s, c do
    n = n + 1
    if n >= 3 then break end
  end
  return n
end
local function call(f, s, c) local n = loop(f, s, c) return n end
local seen, order = {}, {}
for i = 1, 40 do
  for _, arg in ipairs({"string", "number"}) do
    local ok, msg
    if arg == "string" then ok, msg = pcall(call, io.lines, "no-such-file")
    else ok, msg = pcall(call, io.lines, 0) end
    local line = arg .. ": " .. tostring(ok) .. " " .. tostring(msg):gsub("^.-:(%d+: )", "%1")
    if not seen[line] then seen[line] = true order[#order + 1] = line end
  end
end
return table.concat(order, " | ")`, true, true},
	// The ipairs iterator's own argument checks, reached from a promoted generic-for: the control
	// value is checked before the table, and a non-numeric string, a nil (the generic-for passes
	// one when the loop names no control value) and a boolean are not numbers.
	{"ipairs_iterator_argument_errors", `
local function loop(f, s, c)
  local n = 0
  for _, v in f, s, c do
    n = n + v
    if n > 100 then break end
  end
  return n
end
local function call(f, s, c) local n = loop(f, s, c) return n end
local it = ipairs({})
local seen, order = {}, {}
for i = 1, 40 do
  for _, arg in ipairs({"no table", "bad control", "bad both", "nil control", "boolean control"}) do
    local ok, msg
    if arg == "no table" then ok, msg = pcall(call, it, 5, 0)
    elseif arg == "bad control" then ok, msg = pcall(call, it, {1, 2}, "x")
    elseif arg == "bad both" then ok, msg = pcall(call, it, 5, {})
    elseif arg == "nil control" then ok, msg = pcall(call, it, {1, 2})
    else ok, msg = pcall(call, it, {1, 2}, true) end
    local line = arg .. ": " .. tostring(ok) .. " " .. tostring(msg):gsub("^.-:(%d+: )", "%1")
    if not seen[line] then seen[line] = true order[#order + 1] = line end
  end
end
return table.concat(order, " | ")`, true, true},
	// A file handle's close called with no argument at all, from a promoted function: the error says
	// "got nil" (io_close pushes the default output first; the other handle methods say
	// "got no value", and flush is called the same way to show it). A function is promoted only if
	// its calls are to known-safe library functions, which a call through a parameter or through
	// io.stdout is not; the methods are therefore stored in the table library and reached as
	// upvalues initialised from it, like math.mod below. Under natural heat P4 runs the kernel
	// compiled and P3 does not, hence auto = false.
	{"file_close_without_handle", `
table.close343, table.flush343 = io.stdout.close, io.stdout.flush
local hclose, hflush = table.close343, table.flush343
local function kernel(x, which)
  local a = x * 2 + 1
  local b = a - x
  local c = b * 3 - a
  local d = c + b - x
  local e = d * 2 + c
  local f = e - d + a
  local r
  if which then r = hclose() else r = hflush() end
  return r, f
end
local function call(x, which) local r, f = kernel(x, which) return r, f end
local seen, order = {}, {}
for i = 1, 40 do
  for _, which in ipairs({true, false}) do
    local ok, msg = pcall(call, i, which)
    local line = tostring(which) .. ": " .. tostring(ok) .. " " .. tostring(msg):gsub("^.-:(%d+: )", "%1")
    if not seen[line] then seen[line] = true order[#order + 1] = line end
  end
end
return table.concat(order, " | ")`, true, false},
	// collectgarbage("setpause") / ("setstepmul") return the previous value, called from a promoted
	// function. collectgarbage is not one of the library calls a promoted function may make, so it
	// is stored in the table library and reached as an upvalue, like the handle methods above. The
	// option names are upvalues too: P3 does not compile a function that loads a string constant.
	// Under natural heat P4 runs the kernel compiled and P3 does not, hence auto = false.
	{"collectgarbage_set_returns_previous", `
table.cg345 = collectgarbage
local cg, setpause, setstepmul = table.cg345, "setpause", "setstepmul"
local function kernel(x)
  local a = x * 2 + 1
  local b = a - x
  local c = b * 3 - a
  local d = c + b - x
  local e = d * 2 + c
  local f = e - d + a
  local p = cg(setpause, 100 + x)
  local m = cg(setstepmul, 300 + x)
  return p * 1000 + m + f - f
end
local out = {}
for i = 1, 40 do out[#out + 1] = kernel(i) end
cg("setpause", 200)
return table.concat(out, " ")`, true, false},
	// math.mod is fmod's 5.0 name and the same function object. A promoted function sees the two equal
	// and gets the same results from either. The kernel reaches them as upvalues and is padded with
	// arithmetic, a form both tiers promote under force-all. Under natural heat P4 runs it compiled
	// and P3 does not, hence auto = false.
	{"math_mod_is_fmod", `
local mod, fmod = math.mod, math.fmod
local function kernel(x, y)
  local a = mod(x, y)
  local b = fmod(x, y)
  local c = a * 2 + b
  local d = c - a - b
  local e = d + x - y
  local f = e * 3 - d
  local g = f + c - a
  return a + b + g, mod == fmod
end
local total, agree = 0, 0
for i = 1, 60 do
  local v, same = kernel(i + 7, 5)
  total = total + v
  if same then agree = agree + 1 end
end
return total, agree`, true, false},
}

// tieredNotComparedWithOracle lists the cases whose result is a registered difference from lua5.1.
// Their three runs are compared with each other and with the text recorded here.
var tieredNotComparedWithOracle = map[string]string{
	// lua5.1 opens the file and reports that it does not exist; see the exemption on io.open.
	"io_lines_filename_error": "string: false 4: io.lines with a filename is not supported | " +
		"number: false 4: io.lines with a filename is not supported\n",
}

// TestTiered_StdlibNames runs each kernel three ways on this build -- interpreter only, force-all
// promoted, and promoted from natural heat -- and requires all three to agree with each other and
// with lua5.1 (or, for the cases in tieredNotComparedWithOracle, with the recorded text).
//
// It then requires that execution actually entered compiled code in the promoted runs where the case
// says so, and that it did not in the interpreter-only run. Without that, the comparison can silently become interpreter
// against interpreter: PromotionCount > 0 only says that a Proto was compiled, and a kernel whose
// every call sits directly under pcall is compiled without ever executing compiled.
func TestTiered_StdlibNames(t *testing.T) {
	oracle := findOracle()
	known := map[string]bool{}
	for _, c := range tieredStdlibNameCases {
		known[c.name] = true
	}
	for name := range tieredNotComparedWithOracle {
		if !known[name] {
			t.Errorf("tieredNotComparedWithOracle names %q, which is not a case", name)
		}
	}
	for _, c := range tieredStdlibNameCases {
		t.Run(c.name, func(t *testing.T) {
			prog, err := wangshu.Compile([]byte(c.src), "tiernames")
			if err != nil {
				t.Fatalf("wangshu compile: %v", err)
			}
			interp := newInterpBaselineState()
			baseline := runAutoOnce(t, prog, interp, "baseline")
			if n := interp.GibbousEntries(); n != 0 {
				t.Errorf("the interpreter-only baseline entered compiled code %d times", n)
			}
			if want, recorded := tieredNotComparedWithOracle[c.name]; recorded {
				if baseline != want {
					t.Errorf("interpreter vs the recorded result:\n  wangshu:  %q\n  recorded: %q", baseline, want)
				}
			} else if oracle != "" {
				if want := runOracle(t, oracle, wrapForOracle(c.src)); baseline != want {
					t.Errorf("interpreter vs oracle:\n  wangshu: %q\n  oracle:  %q", baseline, want)
				}
			}

			forced := wangshu.NewState(wangshu.Options{})
			forced.SetForceAllPromote(true)
			if got := runAutoOnce(t, prog, forced, "force-all"); got != baseline {
				t.Errorf("force-all diverged from the interpreter:\n  force-all:   %q\n  interpreter: %q", got, baseline)
			}
			switch n := forced.GibbousEntries(); {
			case c.forced && n == 0:
				t.Errorf("force-all never entered compiled code (%d protos promoted): this kernel does not exercise the tier",
					forced.PromotionCount())
			case !c.forced && n != 0:
				t.Errorf("force-all entered compiled code %d times on a build recorded as not compiling this kernel: "+
					"missingFieldCallRunsCompiled is out of date", n)
			}

			auto := newAutoState()
			if got := runAutoOnce(t, prog, auto, "auto"); got != baseline {
				t.Errorf("auto diverged from the interpreter:\n  auto:        %q\n  interpreter: %q", got, baseline)
			}
			if c.auto && auto.GibbousEntries() == 0 {
				t.Errorf("natural heat never entered compiled code (%d protos promoted): this kernel does not exercise the tier",
					auto.PromotionCount())
			}
			t.Logf("entries into compiled code: force-all %d, auto %d", forced.GibbousEntries(), auto.GibbousEntries())
		})
	}
}
