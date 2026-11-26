//go:build (wangshu_p3 || wangshu_p4) && wangshu_profile

package difftest

import (
	"testing"

	"github.com/Liam0205/wangshu"
)

// tieredStdlibNameCases are the #329 behaviours in a form the P3 / P4 tiers really promote.
//
// A function that calls the global pairs / ipairs itself is not promoted (the bridge classifies the
// call as unknown), so a "hot loop over ipairs" written the obvious way runs in the interpreter on
// every build and proves nothing about the tiers. The kernels below therefore take the iterator
// triple as arguments, or reach the missing name through a field call, and the test asserts
// PromotionCount > 0 instead of assuming it.
//
// auto says whether the kernel is also expected to promote from natural heat under the lowered
// thresholds; the error-path kernel only promotes under force-all.
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
local function f(t) return table.unpack(t) end
local seen = {}
for i = 1, 40 do
  local ok, msg = pcall(f, {1, 2})
  seen[tostring(ok) .. " " .. tostring(msg):gsub("^.-:%d+: ", "")] = true
end
local out = {}
for k in pairs(seen) do out[#out + 1] = k end
return table.concat(out, "|"), type(__ipairs_iter)`, false},
	// io.lines as the generator of a promoted generic-for: an explicit nil is rejected as a file handle
	// and a non-string fails the string check, both attributed to the for generator as on 5.1. Nothing
	// is read from stdin. The loop is bounded because a generator that wrongly accepts its argument
	// (io.lines(nil) used to return an iterator) would otherwise be called forever. The position prefix
	// is cut off because the chunk name differs between the two engines.
	{"io_lines_argument_errors", `
local function loop(f, s, c) local n = 0 for _ in f, s, c do n = n + 1 if n >= 3 then break end end return n end
local seen, order = {}, {}
for i = 1, 40 do
  for _, arg in ipairs({"nil", "table", "boolean"}) do
    local ok, msg
    if arg == "nil" then ok, msg = pcall(loop, io.lines, nil, nil)
    elseif arg == "table" then ok, msg = pcall(loop, io.lines, {})
    else ok, msg = pcall(loop, io.lines, false) end
    local line = arg .. ": " .. tostring(ok) .. " " .. tostring(msg):gsub("^.-:%d+: ", "")
    if not seen[line] then seen[line] = true order[#order + 1] = line end
  end
end
return table.concat(order, " | ")`, true},
}

// TestTiered_StdlibNames runs each kernel three ways on this build -- interpreter only, force-all
// promoted, and promoted from natural heat -- and requires all three to agree with each other and
// with lua5.1. It fails if a kernel that is supposed to promote did not, so the comparison cannot
// silently turn into interpreter against interpreter.
func TestTiered_StdlibNames(t *testing.T) {
	oracle := findOracle()
	for _, c := range tieredStdlibNameCases {
		t.Run(c.name, func(t *testing.T) {
			prog, err := wangshu.Compile([]byte(c.src), "tiernames")
			if err != nil {
				t.Fatalf("wangshu compile: %v", err)
			}
			baseline := runAutoOnce(t, prog, newInterpBaselineState(), "baseline")
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
			if forced.PromotionCount() == 0 {
				t.Error("nothing was promoted under force-all: this kernel no longer exercises the tier")
			}

			auto := newAutoState()
			if got := runAutoOnce(t, prog, auto, "auto"); got != baseline {
				t.Errorf("auto diverged from the interpreter:\n  auto:        %q\n  interpreter: %q", got, baseline)
			}
			if c.auto && auto.PromotionCount() == 0 {
				t.Error("nothing was promoted from natural heat: this kernel no longer exercises the tier")
			}
		})
	}
}
