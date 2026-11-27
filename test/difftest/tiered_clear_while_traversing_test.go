//go:build (wangshu_p3 || wangshu_p4) && wangshu_profile

package difftest

import (
	"testing"

	"github.com/Liam0205/wangshu"
)

// tieredClearCases are the #338 behaviours in a form that executes in the P3 / P4 compiled tiers:
// a deleted key stays in its node, so compiled code meets nodes that hold a key and no value.
//
// A case has two chunks. warm defines the kernels as globals and calls them; run calls them
// again and returns the result. The tiered run executes warm in the interpreter and run with
// force-all on, on the same State: the kernels are then compiled from inline caches the
// interpreter has just filled, and as long as nothing changes the generation of the table in
// between, their inline table paths are the ones that execute. Compiling a kernel at its first
// call instead (force-all on a fresh State) finds the caches empty and goes through the helpers;
// running one script twice leaves the caches filled with a generation that is no longer current.
//
// A function is compiled at its first call with force-all on and runs compiled from the second, so
// run calls each kernel a few times before the calls that matter.
//
// What the kernels look like follows from that and from what the tiers accept: a kernel does not
// call the global pairs or next itself (such a function is not promoted), is padded with
// arithmetic so that both P4 native translators accept it, and stores nil through a parameter
// (the interpreter fills the cache of a store only when the stored value is not nil, and P4
// compiles a table access only where the cache is filled).
var tieredClearCases = []struct{ name, warm, run string }{
	// A promoted generic-for clears the table it walks.
	{"clear_in_generic_for", `
function clear338(f, s, c)
  local n = 0
  for k in f, s, c do
    s[k] = nil
    n = n + 1
  end
  return n
end
clear338(next, {a = 1, [1.5] = 2})`, `
local total = 0
for i = 1, 60 do
  local t = {a = 1, b = 2, c = 3, [1.5] = 4, [true] = 5, [{}] = 6}
  total = total + clear338(next, t)
  if next(t) ~= nil then return "not empty in round " .. i end
end
return total`},
	// A global is deleted, read and stored again by promoted code, forty times. P3 does the nil
	// store inline, without going through the table code: the node keeps its key, the inline read
	// finds no value and falls back, the next store puts the value back into the same node, and
	// the inline read hits again.
	{"delete_read_store_global", `
gv338 = 0
function churn338(i, z)
  local p = i * 2 + 1
  local q = p - i
  local r = q * 3 - p
  local u = r + q - i
  local w = u * 2 + r
  local g = w - u + p
  gv338 = z
  local a = gv338
  gv338 = i
  local b = gv338
  if a ~= nil then return 1000 + b + g - g end
  return b + g - g
end
for i = 1, 20 do churn338(i, i) end`, `
local total = 0
for i = 21, 60 do total = total + churn338(i, nil) end
return total, gv338`},
	// A global is deleted by promoted code and a different global then takes its node over, while
	// promoted code keeps reading the deleted name: it must read nil, never the other global's
	// value. On P3 the nil store is inline and changes no generation, so the takeover is the only
	// moment at which the reader's cached node can be invalidated; if insertNewKey did not bump
	// there, the second result would be 7.
	//
	// The two names are the pair of TestReusedGlobalSlotDoesNotAliasDeletedKey (test/regression):
	// k2621's main position is the node v4927 sits in. That depends on the string hashes and on the
	// size of the globals table; internal/stdlib's TestIssue260SlotReusePairStillHolds fails if it
	// stops holding.
	{"deleted_global_node_taken_over", `
v4927 = 1
function put338(i, z)
  local p = i * 2 + 1
  local q = p - i
  local r = q * 3 - p
  local u = r + q - i
  local w = u * 2 + r
  local g = w - u + p
  v4927 = z
  return g - g
end
function read338(i)
  local p = i * 2 + 1
  local q = p - i
  local r = q * 3 - p
  local u = r + q - i
  local w = u * 2 + r
  local g = w - u + p
  local q = v4927
  if q == nil then return -1 + g - g end
  return q + g - g
end
for i = 1, 20 do put338(i, i) read338(i) end`, `
for i = 1, 4 do put338(i, i) read338(i) end
local a = read338(1)
put338(1, nil)
k2621 = 7
local b = read338(1)
return a, b, k2621`},
	// The same takeover with the nil stored by a function that is nothing but one field store, run
	// on the globals table. P4 compiles such a function into a dedicated template that stores into
	// the node directly.
	{"deleted_global_node_taken_over_single_store", `
v4927 = 1
function set338(t, v) t["v4927"] = v end
function read338(i)
  local p = i * 2 + 1
  local q = p - i
  local r = q * 3 - p
  local u = r + q - i
  local w = u * 2 + r
  local g = w - u + p
  local q = v4927
  if q == nil then return -1 + g - g end
  return q + g - g
end
for i = 1, 20 do set338(_G, i) read338(i) end`, `
for i = 1, 4 do set338(_G, i) read338(i) end
local a = read338(1)
set338(_G, nil)
k2621 = 7
local b = read338(1)
return a, b, k2621`},
	// A field is deleted by other code while a single-store function is compiled for it: the next
	// store is a store to an absent field and must reach __newindex. The P4 template compares the
	// node's key but not its value, so it relies on the deletion changing the generation.
	{"single_store_function_after_delete_elsewhere", `
log338 = 0
S338 = setmetatable({x = 0, y = 0}, {__newindex = function(t, k, v) log338 = log338 + 1 rawset(t, k, v) end})
function setter338(t, v) t["x"] = v end
for i = 1, 40 do setter338(S338, i) end`, `
for i = 1, 10 do
  rawset(S338, "x", nil)
  setter338(S338, i)
end
return log338, S338.x`},
	// A field is deleted, read and stored again by promoted code; a deleted field is absent, so
	// the read goes to __index and the store goes to __newindex, every time.
	{"deleted_field_reaches_metamethods", `
log338 = 0
M338 = setmetatable({x = 0}, {
  __index = function() return -1 end,
  __newindex = function(t, k, v) log338 = log338 + 1 rawset(t, k, v) end,
})
function churn338(t, i, z)
  local p = i * 2 + 1
  local q = p - i
  local r = q * 3 - p
  local u = r + q - i
  local w = u * 2 + r
  local g = w - u + p
  t.x = z
  local a = t.x
  t.x = i
  local b = t.x
  return a + b + g - g
end
for i = 1, 20 do churn338(M338, i, i) end`, `
local total = 0
for i = 21, 60 do total = total + churn338(M338, i, nil) end
return total, log338, M338.x`},
}

// runTieredClear runs warm and then run on st, switching force-all on in between when forceRun is
// set, and returns run's results.
func runTieredClear(t *testing.T, st *wangshu.State, warm, run *wangshu.Program, forceRun bool, label string) string {
	t.Helper()
	if _, err := warm.Run(st); err != nil {
		t.Fatalf("wangshu warm chunk (%s): %v", label, err)
	}
	if forceRun {
		st.SetForceAllPromote(true)
	}
	return runAutoOnce(t, run, st, label)
}

// TestTiered_ClearWhileTraversing runs each case four ways on this build and requires all of them
// to agree with each other and with lua5.1:
//
//   - interpreter only;
//   - warm in the interpreter, then run with force-all on (see tieredClearCases);
//   - force-all from the start, on a fresh State;
//   - promoted from natural heat under the lowered thresholds.
//
// It requires that execution entered compiled code in the second way and did not in the first;
// PromotionCount > 0 alone would only say that a Proto was compiled.
func TestTiered_ClearWhileTraversing(t *testing.T) {
	oracle := findOracle()
	for _, c := range tieredClearCases {
		t.Run(c.name, func(t *testing.T) {
			warm, err := wangshu.Compile([]byte(c.warm), "tierclear-warm")
			if err != nil {
				t.Fatalf("wangshu compile (warm): %v", err)
			}
			run, err := wangshu.Compile([]byte(c.run), "tierclear-run")
			if err != nil {
				t.Fatalf("wangshu compile (run): %v", err)
			}
			interp := newInterpBaselineState()
			baseline := runTieredClear(t, interp, warm, run, false, "baseline")
			if n := interp.GibbousEntries(); n != 0 {
				t.Errorf("the interpreter-only baseline entered compiled code %d times", n)
			}
			if oracle != "" {
				if want := runOracle(t, oracle, wrapForOracle("do "+c.warm+"\nend\n"+c.run)); baseline != want {
					t.Errorf("interpreter vs oracle:\n  wangshu: %q\n  oracle:  %q", baseline, want)
				}
			}

			tiered := newInterpBaselineState()
			if got := runTieredClear(t, tiered, warm, run, true, "warm, then force-all"); got != baseline {
				t.Errorf("warm, then force-all diverged from the interpreter:\n  tiered:      %q\n  interpreter: %q", got, baseline)
			}
			if tiered.GibbousEntries() == 0 {
				t.Errorf("warm, then force-all never entered compiled code (%d protos promoted): this case does not exercise the tier",
					tiered.PromotionCount())
			}

			forced := wangshu.NewState(wangshu.Options{})
			forced.SetForceAllPromote(true)
			if got := runTieredClear(t, forced, warm, run, false, "force-all"); got != baseline {
				t.Errorf("force-all diverged from the interpreter:\n  force-all:   %q\n  interpreter: %q", got, baseline)
			}

			auto := newAutoState()
			if got := runTieredClear(t, auto, warm, run, false, "auto"); got != baseline {
				t.Errorf("auto diverged from the interpreter:\n  auto:        %q\n  interpreter: %q", got, baseline)
			}
			t.Logf("result %q; entries into compiled code: warm then force-all %d, force-all %d, auto %d",
				baseline, tiered.GibbousEntries(), forced.GibbousEntries(), auto.GibbousEntries())
		})
	}
}
