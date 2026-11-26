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
// auto says whether the kernel is also expected to run compiled when promotion comes from natural
// heat under the lowered thresholds; some kernels are only promoted under force-all.
var tieredStdlibNameCases = []struct {
	name, src string
	auto      bool
}{
	// A promoted generic-for drives the iterators that pairs / ipairs hand out, after the global next
	// is gone: pairs must not look it up again.
	{"generic_for_over_private_iterators", `
next = nil
local function loop(f, s, c) local n = 0 for _, v in f, s, c do n = n + v end return n end
local t = {1, 2, 3}
local total = 0
for i = 1, 60 do total = total + loop(ipairs(t)) + loop(pairs(t)) end
return total`, true},
	// The ipairs iterator narrows its control value like luaL_checkint, numeric strings included.
	{"ipairs_iterator_string_control", `
local function loop(f, s, c) local n = 0 for _, v in f, s, c do n = n + v end return n end
local t = {7, 8, 9}
local total = 0
for i = 1, 60 do total = total + loop(ipairs({}), t, "1") end
return total`, true},
	// Calling the name 5.1 does not have, from inside a promoted function, raises the same error.
	// The position prefix is cut off because the chunk name differs between the two engines.
	{"missing_table_unpack_error", `
local function kernel(t) local a, b = table.unpack(t) return a, b end
local function call(t) local a, b = kernel(t) return a, b end
local seen = {}
for i = 1, 40 do
  local ok, msg = pcall(call, {1, 2})
  seen[tostring(ok) .. " " .. tostring(msg):gsub("^.-:%d+: ", "")] = true
end
local out = {}
for k in pairs(seen) do out[#out + 1] = k end
return table.concat(out, "|"), type(__ipairs_iter)`, false},
	// io.lines as the generator of a promoted generic-for: an explicit nil is rejected as a file handle
	// and a non-string fails the string check, both attributed to the for generator as on 5.1. Nothing
	// is read from stdin. The loop is bounded because a generator that wrongly accepts its argument
	// would otherwise be called forever.
	{"io_lines_argument_errors", `
local function loop(f, s, c) local n = 0 for _ in f, s, c do n = n + 1 if n >= 3 then break end end return n end
local function call(f, s, c) local n = loop(f, s, c) return n end
local seen, order = {}, {}
for i = 1, 40 do
  for _, arg in ipairs({"nil", "table", "boolean"}) do
    local ok, msg
    if arg == "nil" then ok, msg = pcall(call, io.lines, nil, nil)
    elseif arg == "table" then ok, msg = pcall(call, io.lines, {})
    else ok, msg = pcall(call, io.lines, false) end
    local line = arg .. ": " .. tostring(ok) .. " " .. tostring(msg):gsub("^.-:%d+: ", "")
    if not seen[line] then seen[line] = true order[#order + 1] = line end
  end
end
return table.concat(order, " | ")`, true},
	// The ipairs iterator's own argument checks, reached from a promoted generic-for: the control
	// value is checked before the table, and a non-numeric string is not a number.
	{"ipairs_iterator_argument_errors", `
local function loop(f, s, c) local n = 0 for _, v in f, s, c do n = n + v if n > 100 then break end end return n end
local function call(f, s, c) local n = loop(f, s, c) return n end
local it = ipairs({})
local seen, order = {}, {}
for i = 1, 40 do
  for _, arg in ipairs({"no table", "bad control", "bad both"}) do
    local ok, msg
    if arg == "no table" then ok, msg = pcall(call, it, 5, 0)
    elseif arg == "bad control" then ok, msg = pcall(call, it, {1, 2}, "x")
    else ok, msg = pcall(call, it, 5, {}) end
    local line = arg .. ": " .. tostring(ok) .. " " .. tostring(msg):gsub("^.-:%d+: ", "")
    if not seen[line] then seen[line] = true order[#order + 1] = line end
  end
end
return table.concat(order, " | ")`, true},
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
return total, agree`, false},
}

// TestTiered_StdlibNames runs each kernel three ways on this build -- interpreter only, force-all
// promoted, and promoted from natural heat -- and requires all three to agree with each other and
// with lua5.1.
//
// It then requires that execution actually entered compiled code in the promoted runs, and that it
// did not in the interpreter-only run. Without that, the comparison can silently become interpreter
// against interpreter: PromotionCount > 0 only says that a Proto was compiled, and a kernel whose
// every call sits directly under pcall is compiled without ever executing compiled.
func TestTiered_StdlibNames(t *testing.T) {
	oracle := findOracle()
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
			if oracle != "" {
				if want := runOracle(t, oracle, wrapForOracle(c.src)); baseline != want {
					t.Errorf("interpreter vs oracle:\n  wangshu: %q\n  oracle:  %q", baseline, want)
				}
			}

			forced := wangshu.NewState(wangshu.Options{})
			forced.SetForceAllPromote(true)
			if got := runAutoOnce(t, prog, forced, "force-all"); got != baseline {
				t.Errorf("force-all diverged from the interpreter:\n  force-all:   %q\n  interpreter: %q", got, baseline)
			}
			if forced.GibbousEntries() == 0 {
				t.Errorf("force-all never entered compiled code (%d protos promoted): this kernel does not exercise the tier",
					forced.PromotionCount())
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
