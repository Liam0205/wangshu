package difftest

import (
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
)

// clearWhileTraversingCases cover #338: assigning nil to the field a traversal has just been given
// must not break the traversal ("the behavior of next is undefined if, during the traversal, you
// assign any value to a non-existent field ... you may however modify existing fields. In
// particular, you may clear existing fields", Lua 5.1 manual). The differential fuzz cannot see
// any of this, because its prelude replaces pairs and next with a sorted walk over a key snapshot;
// these cases run the real next and are compared with lua5.1 directly.
//
// Every case is written so that its result does not depend on the order of the traversal, which
// 5.1 leaves unspecified. want is lua5.1's output; it is also what the test compares with where no
// lua5.1 is installed.
var clearWhileTraversingCases = []struct{ name, src, want string }{
	{"string_keys_with_pairs", `
local t = {a = 1, b = 2, c = 3}
local n = 0
for k in pairs(t) do t[k] = nil n = n + 1 end
return n, next(t)`, "3\tnil"},
	{"next_after_clearing_its_key", `
local u = {x = 1, y = 2}
local k = next(u)
u[k] = nil
local k2, v2 = next(u, k)
return k ~= k2, k2 == "x" or k2 == "y", v2 == u[k2], next(u, k2)`, "true\ttrue\ttrue\tnil"},
	{"array_part_keys", `
local a = {10, 20, 30}
local n = 0
for k in pairs(a) do a[k] = nil n = n + 1 end
return n, #a, next(a)`, "3\t0\tnil"},
	{"float_boolean_and_table_keys", `
local m = {[1.5] = 1, [true] = 2, [{}] = 3, [false] = 4, [-0.25] = 5}
local n, sum = 0, 0
for k, v in pairs(m) do m[k] = nil n = n + 1 sum = sum + v end
return n, sum, next(m)`, "5\t15\tnil"},
	// nextvar.lua's "erasing values" block; the official suite only runs that file up to a line
	// before it.
	{"nextvar_erasing_values", `
local t = {[{1}] = 1, [{2}] = 2, [string.rep("x ", 4)] = 3, [100.3] = 4, [4] = 5}
local n = 0
for k, v in pairs(t) do
  n = n + 1
  assert(t[k] == v)
  t[k] = nil
  collectgarbage()
  assert(t[k] == nil)
end
return n`, "5"},
	{"explicit_next_loop", `
local t = {}
for i = 1, 200 do t["k" .. i] = i end
local n, sum = 0, 0
local k, v = next(t)
while k do
  t[k] = nil
  n = n + 1
  sum = sum + v
  k, v = next(t, k)
end
return n, sum, next(t)`, "200\t20100\tnil"},
	{"clear_some_and_change_the_rest", `
local t = {}
for i = 1, 40 do t["k" .. i] = i end
for k, v in pairs(t) do
  if v % 2 == 0 then t[k] = nil else t[k] = v * 10 end
end
local n, sum = 0, 0
for _, v in pairs(t) do n = n + 1 sum = sum + v end
return n, sum`, "20\t4000"},
	{"refill_after_clearing", `
local t = {a = 1, b = 2, c = 3, d = 4}
for k in pairs(t) do t[k] = nil end
t.b = 20 t.e = 50 t.a = 10 t.f = 60
local keys = {}
for k, v in pairs(t) do keys[#keys + 1] = k .. "=" .. v end
table.sort(keys)
return table.concat(keys, " ")`, "a=10 b=20 e=50 f=60"},
	{"clearing_repeatedly", `
local t = {}
local total = 0
for round = 1, 30 do
  for i = 1, 12 do t["r" .. ((round * 7 + i) % 40)] = i end
  for k in pairs(t) do t[k] = nil total = total + 1 end
  if next(t) ~= nil then return "not empty after round " .. round end
end
return total`, "360"},
	// The callback drops its own reference to the key before collecting: table.foreach still holds
	// the key it continues from (lua_next keeps it on the C stack).
	{"table_foreach_clearing_object_keys", `
local t = {[{}] = 1, [{}] = 2, [{}] = 3, x = 4}
local n = 0
table.foreach(t, function(k) t[k] = nil k = nil collectgarbage() n = n + 1 end)
return n, next(t)`, "4\tnil"},
	{"globals_table", `
for i = 1, 30 do _G["zz338_" .. i] = i end
local n = 0
for k in pairs(_G) do
  if type(k) == "string" and string.sub(k, 1, 6) == "zz338_" then _G[k] = nil n = n + 1 end
end
return n, zz338_1, zz338_30`, "30\tnil\tnil"},
	// A collection in the middle of the traversal empties every entry of a weak-valued table,
	// the one the traversal stands on included, and the traversal ends normally. keep holds the
	// values until the first step, so that no earlier collection can remove them.
	{"weak_values_cleared_by_a_collection", `
local w = setmetatable({}, {__mode = "v"})
local keep = {{}, {}, {}}
w.a = keep[1] w.b = keep[2] w.c = keep[3]
local n = 0
for k, v in pairs(w) do v = nil keep = nil collectgarbage() n = n + 1 end
return n, next(w)`, "1\tnil"},
	// next accepts a key that was removed before the traversal reached it, and still rejects one
	// that never was in the table.
	{"next_from_a_removed_key", `
local t = {a = 1}
t.a = nil
local ok, k = pcall(next, t, "a")
return ok, k, pcall(next, t, "never")`, "true\tnil\tfalse\tinvalid key to 'next'"},
	// The same with a key that is not a constant of the chunk: the string is built at run time, and
	// built again for the call to next.
	{"next_from_a_removed_key_built_at_run_time", `
local t = {}
for i = 1, 5 do t["dyn" .. i] = i end
t["dyn" .. 3] = nil
local ok = pcall(next, t, "dyn" .. 3)
return ok, pcall(next, t, "dyn" .. 9)`, "true\tfalse\tinvalid key to 'next'"},
	// A removed key does not keep its object alive.
	{"removed_key_is_collectable", `
local probe = setmetatable({}, {__mode = "v"})
local holder = {}
do local k = {} probe[1] = k holder[k] = 1 holder[k] = nil end
collectgarbage() collectgarbage()
local n = 0
for i = 1, 50 do holder["s" .. i] = i end
for _ in pairs(holder) do n = n + 1 end
return probe[1], n`, "nil\t50"},
	{"removed_key_stored_again_between_collections", `
local t = {}
local keys = {}
for i = 1, 20 do keys[i] = {} t[keys[i]] = i end
for round = 1, 5 do
  for i = 1, 20, 2 do t[keys[i]] = nil end
  collectgarbage()
  for i = 1, 20, 2 do t[keys[i]] = i + round end
end
local n, sum = 0, 0
for _, v in pairs(t) do n = n + 1 sum = sum + v end
return n, sum`, "20\t260"},
}

func runClearCase(t *testing.T, src string, stress bool) string {
	t.Helper()
	prog, err := wangshu.Compile([]byte(src), "clear")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetGCStressMode(stress)
	results, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run (stress=%v): %v", stress, err)
	}
	parts := make([]string, len(results))
	for i, r := range results {
		parts[i] = r.Display()
	}
	return strings.Join(parts, "\t")
}

// TestDiff_ClearWhileTraversing runs each case with the collector in its normal mode and collecting
// at every safepoint, and compares both with the recorded lua5.1 output and, where lua5.1 is
// installed, with lua5.1 itself.
func TestDiff_ClearWhileTraversing(t *testing.T) {
	oracle := findOracle()
	for _, c := range clearWhileTraversingCases {
		t.Run(c.name, func(t *testing.T) {
			if oracle != "" {
				if got := strings.TrimSuffix(runOracle(t, oracle, wrapForOracle(c.src)), "\n"); got != c.want {
					t.Errorf("the recorded result is not lua5.1's:\n  recorded: %q\n  lua5.1:   %q", c.want, got)
				}
			}
			if got := runClearCase(t, c.src, false); got != c.want {
				t.Errorf("wangshu:\n  got:  %q\n  want: %q", got, c.want)
			}
			if got := runClearCase(t, c.src, true); got != c.want {
				t.Errorf("wangshu, collecting at every safepoint:\n  got:  %q\n  want: %q", got, c.want)
			}
		})
	}
}

// nextFromCollectedStringKey removes a string key that nothing else refers to, collects, and then
// hands next a string with the same contents. wangshu's answer does not depend on whether a
// collection happened in between: a removed node keeps its string key alive, so the rebuilt string
// finds the node (06 §8.4). lua5.1 lets the string die and marks the node's key dead, after which
// next matches that key by object address only: the call then fails unless the rebuilt string
// happens to be allocated where the old one was. The difference is registered in exemptions
// (corners_test.go).
const nextFromCollectedStringKey = `
local t = {}
for i = 1, 5 do t["dyn" .. i] = i end
t["dyn" .. 3] = nil
collectgarbage() collectgarbage()
return (pcall(next, t, "dyn" .. 3))`

// TestDiff_NextFromCollectedStringKey pins wangshu's side of that difference, with the collector in
// its normal mode and collecting at every safepoint. lua5.1's result is logged and not asserted: it
// depends on the C allocator.
func TestDiff_NextFromCollectedStringKey(t *testing.T) {
	for _, stress := range []bool{false, true} {
		if got := runClearCase(t, nextFromCollectedStringKey, stress); got != "true" {
			t.Errorf("wangshu (collecting at every safepoint: %v):\n  got:  %q\n  want: %q", stress, got, "true")
		}
	}
	if oracle := findOracle(); oracle != "" {
		t.Logf("lua5.1 here: %s", strings.TrimSuffix(runOracle(t, oracle, wrapForOracle(nextFromCollectedStringKey)), "\n"))
	}
}

// nextAfterInsertingOtherKeys are calls of next on a removed key after other keys have been
// inserted. Whether the removed key's node is still there is decided by the layout of the hash
// part: a new key whose main position is that node takes it over, and an insertion that finds no
// free node rehashes. wangshu's hash part is not laid out like lua5.1's (#349), so the two disagree
// on some of these in either direction. The manual leaves the behaviour undefined once a traversal
// has assigned to a field that did not exist; the cases are here so that a change in wangshu's
// answers is noticed. want is wangshu's result, oracle is lua5.1's.
var nextAfterInsertingOtherKeys = []struct{ name, src, want, oracle string }{
	// lua5.1's hash part has one node here and 1.5 takes it over; wangshu's has two.
	{"one_key_removed_then_one_inserted", `
local t = {} t.x1 = 1 t.x1 = nil t[1.5] = 1
return (pcall(next, t, "x1"))`, "true", "false"},
	{"one_of_three_removed_then_one_inserted", `
local u = {} u.s1 = 1 u.s2 = 2 u.s3 = 3 u.s1 = nil u[1.5] = 1
return (pcall(next, u, "s1"))`, "false", "true"},
	{"clearing_traversal_that_also_inserts", `
local w = {} w.x1 = 1
local n = 0
return pcall(function() for k in pairs(w) do w[k] = nil w[1.5] = 1 n = n + 1 end return n end)`,
		"true\t1", "false\tinvalid key to 'next'"},
}

// TestDiff_NextAfterInsertingOtherKeys pins both sides of nextAfterInsertingOtherKeys. Unlike the
// collected-string case, lua5.1's results here follow from its table layout alone, so they are
// asserted: when #349 is settled these cases move into clearWhileTraversingCases.
func TestDiff_NextAfterInsertingOtherKeys(t *testing.T) {
	oracle := findOracle()
	for _, c := range nextAfterInsertingOtherKeys {
		t.Run(c.name, func(t *testing.T) {
			for _, stress := range []bool{false, true} {
				if got := runClearCase(t, c.src, stress); got != c.want {
					t.Errorf("wangshu (collecting at every safepoint: %v):\n  got:  %q\n  want: %q", stress, got, c.want)
				}
			}
			if oracle != "" {
				if got := strings.TrimSuffix(runOracle(t, oracle, wrapForOracle(c.src)), "\n"); got != c.oracle {
					t.Errorf("lua5.1 no longer answers as recorded:\n  got:      %q\n  recorded: %q", got, c.oracle)
				}
			}
		})
	}
}
