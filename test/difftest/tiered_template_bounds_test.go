//go:build (wangshu_p3 || wangshu_p4) && wangshu_profile

package difftest

import (
	"testing"

	"github.com/Liam0205/wangshu"
)

// tieredShorterTableCases call a function whose whole body is one table access with a constant
// integer key on a table that is not the one the function warmed up on: it has the same
// generation (it was built the same way) and a shorter array part. P4 compiles such a function
// into a template that addresses the array slot directly; the slot it would address lies past
// the end of the shorter table's array, in whatever object the arena holds next, so each case
// allocates a second table right after the short one and reports both.
//
// The cases have the two-chunk form of tieredClearCases, for the same reason: the template is only
// emitted from an inline cache the interpreter has filled. natural says that the warm chunk calls
// the function often enough for it to be promoted under the default thresholds as well.
var tieredShorterTableCases = []struct {
	name, warm, run string
	natural         bool
}{
	{"read_from_a_shorter_table", `
A350 = {1, 2, 3, 4, 5, 6, 7, 8}
function get8_350(t) return t[8] end
local s = 0
for i = 1, 3000 do s = s + get8_350(A350) end`, `
local out = {}
for round = 1, 20 do
  local B = {1}
  local neighbour = {11, 22, 33, 44, 55, 66, 77, 88}
  out[#out + 1] = tostring(get8_350(B))
end
return table.concat(out, " "), get8_350(A350)`, true},
	// A table built from a vararg list or from a call gets its array part from SETLIST, not from
	// NEWTABLE, so how its generation depends on its length is SETLIST's business; the two forms
	// are cases of their own.
	{"read_from_a_shorter_vararg_table", `
function pack350(...) return {...} end
A350 = pack350(1, 2, 3, 4, 5, 6, 7, 8)
function get8_350(t) return t[8] end
local s = 0
for i = 1, 3000 do s = s + get8_350(A350) end`, `
local out = {}
for round = 1, 20 do
  local B = pack350(1)
  local neighbour = {11, 22, 33, 44, 55, 66, 77, 88}
  out[#out + 1] = tostring(get8_350(B))
end
return table.concat(out, " "), get8_350(A350)`, true},
	{"read_from_a_shorter_table_built_from_a_call", `
function mk350(n) local t = {} for i = 1, n do t[i] = i end return unpack(t) end
A350 = {mk350(8)}
function get8_350(t) return t[8] end
local s = 0
for i = 1, 3000 do s = s + get8_350(A350) end`, `
local out = {}
for round = 1, 20 do
  local B = {mk350(2)}
  local neighbour = {11, 22, 33, 44, 55, 66, 77, 88}
  out[#out + 1] = tostring(get8_350(B))
end
return table.concat(out, " "), get8_350(A350)`, true},
	{"read_from_an_empty_table", `
A350 = {1, 2, 3, 4, 5, 6, 7, 8}
function get1_350(t) return t[1] end
local s = 0
for i = 1, 3000 do s = s + get1_350(A350) end`, `
local out = {}
for round = 1, 20 do
  local B = {}
  local neighbour = {11, 22, 33, 44, 55, 66, 77, 88}
  out[#out + 1] = tostring(get1_350(B))
end
return table.concat(out, " "), get1_350(A350)`, true},
	// The store has to land in the short table, which grows to take it, and leave the next
	// object alone.
	{"store_to_a_shorter_table", `
A350 = {1, 2, 3, 4, 5, 6, 7, 8}
function set8_350(t, v) t[8] = v end
for i = 1, 40 do set8_350(A350, 8) end`, `
for i = 1, 5 do set8_350(A350, 8) end
local out = {}
for round = 1, 20 do
  local B = {1}
  local neighbour = {11, 22, 33, 44, 55, 66, 77, 88}
  set8_350(B, 12345)
  out[#out + 1] = tostring(B[8]) .. ":" .. tostring(neighbour[1])
end
return table.concat(out, " "), A350[8]`, false},
	{"store_to_a_shorter_vararg_table", `
function pack350(...) return {...} end
A350 = pack350(1, 2, 3, 4, 5, 6, 7, 8)
function set8_350(t, v) t[8] = v end
for i = 1, 40 do set8_350(A350, 8) end`, `
for i = 1, 5 do set8_350(A350, 8) end
local out = {}
for round = 1, 20 do
  local B = pack350(1)
  local neighbour = {11, 22, 33, 44, 55, 66, 77, 88}
  set8_350(B, 12345)
  out[#out + 1] = tostring(B[8]) .. ":" .. tostring(neighbour[1])
end
return table.concat(out, " "), A350[8]`, false},
}

// TestTiered_TemplateOnShorterTable runs each case the four ways TestTiered_ClearWhileTraversing
// does and requires them to agree with each other and with lua5.1.
func TestTiered_TemplateOnShorterTable(t *testing.T) {
	oracle := findOracle()
	for _, c := range tieredShorterTableCases {
		t.Run(c.name, func(t *testing.T) { checkWarmThenForced(t, oracle, c.warm, c.run) })
	}
}

// tieredFarSlotCases access slots far from the start of a large table through the same
// single-access functions. The template reaches the slot at a byte offset from the start of the
// array or node block, and on arm64 an offset of 32768 or more does not fit the load and store
// instructions that take it as an immediate: that is array index 4096, and the value of node
// 1365. The node cases compile one function per key, so that whichever keys the hash puts in
// the nodes around 1365 are among them.
var tieredFarSlotCases = []struct {
	name, warm, run string
	natural         bool
}{
	{"read_far_into_the_array_part", `
A351 = {}
for i = 1, 9000 do A351[i] = i end
function get5000_351(t) return t[5000] end
function get8193_351(t) return t[8193] end
local s = 0
for i = 1, 3000 do s = s + get5000_351(A351) + get8193_351(A351) end`, `
local a, b
for i = 1, 5 do a, b = get5000_351(A351), get8193_351(A351) end
return a, b, A351[1]`, true},
	{"store_far_into_the_array_part", `
A351 = {}
for i = 1, 9000 do A351[i] = i end
function set5000_351(t, v) t[5000] = v end
function set8193_351(t, v) t[8193] = v end
for i = 1, 40 do set5000_351(A351, i) set8193_351(A351, i) end`, `
for i = 1, 5 do set5000_351(A351, i) set8193_351(A351, i) end
set5000_351(A351, -5000) set8193_351(A351, -8193)
local changed = {}
for i = 1, 9000 do
  if A351[i] ~= i then changed[#changed + 1] = i .. "=" .. tostring(A351[i]) end
end
return table.concat(changed, " ")`, false},
	{"read_every_field_of_a_large_table", `
T351 = {}
T351[0] = -1
for i = 1, 1500 do T351["k" .. i] = i end
getters351 = {}
for i = 1, 1500 do
  getters351[i] = loadstring("return function(t) return t.k" .. i .. " end")()
  for j = 1, 40 do getters351[i](T351) end
end`, `
local wrong = {}
for round = 1, 3 do
  for i = 1, 1500 do
    local v = getters351[i](T351)
    if v ~= i then wrong[#wrong + 1] = "k" .. i .. "=" .. tostring(v) end
  end
end
return #wrong, table.concat(wrong, " ", 1, math.min(#wrong, 5))`, false},
	{"store_every_field_of_a_large_table", `
T351 = {}
T351[0] = -1
for i = 1, 1500 do T351["k" .. i] = i end
setters351 = {}
for i = 1, 1500 do
  setters351[i] = loadstring("return function(t, v) t.k" .. i .. " = v end")()
  for j = 1, 40 do setters351[i](T351, i) end
end`, `
for round = 1, 3 do
  for i = 1, 1500 do setters351[i](T351, i + round) end
end
local wrong, n = {}, 0
for i = 1, 1500 do
  local v = rawget(T351, "k" .. i)
  if v ~= i + 3 then wrong[#wrong + 1] = "k" .. i .. "=" .. tostring(v) end
end
local ok, err = pcall(function() for k in pairs(T351) do n = n + 1 end end)
return #wrong, table.concat(wrong, " ", 1, math.min(#wrong, 5)), rawget(T351, 0), ok, ok and n or err`, false},
}

// TestTiered_TemplateFarSlot runs each case the four ways TestTiered_ClearWhileTraversing does
// and requires them to agree with each other and with lua5.1.
func TestTiered_TemplateFarSlot(t *testing.T) {
	oracle := findOracle()
	for _, c := range tieredFarSlotCases {
		t.Run(c.name, func(t *testing.T) { checkWarmThenForced(t, oracle, c.warm, c.run) })
	}
}

// runShorterTableCaseDefault runs the two chunks of a case on a State with the default
// thresholds and returns the result with the number of entries into compiled code.
func runShorterTableCaseDefault(t *testing.T, warmSrc, runSrc string) (string, int64) {
	t.Helper()
	warm, err := wangshu.Compile([]byte(warmSrc), "shorter-warm")
	if err != nil {
		t.Fatalf("wangshu compile (warm): %v", err)
	}
	run, err := wangshu.Compile([]byte(runSrc), "shorter-run")
	if err != nil {
		t.Fatalf("wangshu compile (run): %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	got := runTieredClear(t, st, warm, run, false, "default thresholds")
	return got, st.GibbousEntries()
}

// TestTiered_Template_DefaultThresholds runs the natural cases of both lists with nothing
// forced and nothing lowered, which is how an embedder runs them, and compares with the
// interpreter-only run of the same build.
func TestTiered_Template_DefaultThresholds(t *testing.T) {
	for _, c := range append(append([]struct {
		name, warm, run string
		natural         bool
	}(nil), tieredShorterTableCases...), tieredFarSlotCases...) {
		if !c.natural {
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			warm, err := wangshu.Compile([]byte(c.warm), "shorter-warm")
			if err != nil {
				t.Fatalf("wangshu compile (warm): %v", err)
			}
			run, err := wangshu.Compile([]byte(c.run), "shorter-run")
			if err != nil {
				t.Fatalf("wangshu compile (run): %v", err)
			}
			want := runTieredClear(t, newInterpBaselineState(), warm, run, false, "baseline")
			got, entries := runShorterTableCaseDefault(t, c.warm, c.run)
			if got != want {
				t.Errorf("default thresholds diverged from the interpreter:\n  default:     %q\n  interpreter: %q", got, want)
			}
			if shorterTableCasesPromoteNaturally && entries == 0 {
				t.Errorf("the function was never entered as compiled code under the default thresholds: this case does not exercise the tier")
			}
			t.Logf("result %q; entries into compiled code under the default thresholds: %d", got, entries)
		})
	}
}
