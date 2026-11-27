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
	// A cache with string keys and weak values releases its keys once the values are gone: the
	// entries the collector cleared do not keep their keys alive, unlike keys the script removed.
	{"weak_valued_cache_releases_its_string_keys", `
local w = setmetatable({}, {__mode = "v"})
local hold = {}
for i = 1, 2000 do local o = {} hold[i] = o w[string.rep("k", 200) .. i] = o end
collectgarbage() collectgarbage()
local before = collectgarbage("count")
hold = nil
collectgarbage() collectgarbage() collectgarbage()
return collectgarbage("count") < before / 2`, "true"},
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
// hands next a string with the same contents. In wangshu a collection in between changes nothing
// about the node: a node emptied by a store of nil keeps its string key alive, so the rebuilt
// string finds it as long as no later insertion has taken the node over or rehashed the table
// (06 §8.4). In lua5.1 the node does not keep the string alive: nothing else refers to it here, so
// it dies and the node's key is marked dead, after which next matches that key by object address
// only, and the call fails unless the rebuilt string happens to be allocated where the old one
// was. The difference is registered in exemptions (corners_test.go).
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

// nextFromKeysOfClearedEntries fills a weak-valued table with 50 entries whose keys are strings
// built at run time, lets the values die, collects three times and counts how often next accepts
// a rebuilt key. An entry the collector cleared does not keep its string key alive (06 §8.4): the
// key survives the collection that clears the entry and goes with the next one, after which the
// node holds a key that matches nothing.
const nextFromKeysOfClearedEntries = `
local w = setmetatable({}, {__mode = "v"})
local hold = {}
for i = 1, 50 do local o = {} hold[i] = o w["wk" .. i] = o end
hold = nil
collectgarbage() collectgarbage() collectgarbage()
local ok = 0
for i = 1, 50 do if pcall(next, w, "wk" .. i) then ok = ok + 1 end end
return ok`

// TestDiff_NextFromKeysOfClearedEntries pins wangshu's side: no call succeeds, with the collector
// in its normal mode and collecting at every safepoint. lua5.1 lets these strings die too and
// then matches a dead key by address, so there a call succeeds when the rebuilt string happens to
// be allocated where the old one was; its count is logged and not asserted.
func TestDiff_NextFromKeysOfClearedEntries(t *testing.T) {
	for _, stress := range []bool{false, true} {
		if got := runClearCase(t, nextFromKeysOfClearedEntries, stress); got != "0" {
			t.Errorf("wangshu (collecting at every safepoint: %v):\n  got:  %q\n  want: %q", stress, got, "0")
		}
	}
	if oracle := findOracle(); oracle != "" {
		t.Logf("lua5.1 here: %s", strings.TrimSuffix(runOracle(t, oracle, wrapForOracle(nextFromKeysOfClearedEntries)), "\n"))
	}
}

// removedStringKeysOfWeakValuedTable removes string keys built at run time from a weak-valued
// table and from a strong one, collects (or not), and stores the keys again. A key the script
// removed is kept alive whether or not the table is weak, so it goes back into its node: the
// traversal order afterwards is the same with and without the collection, and next accepts every
// removed key after it. The values are numbers, so the collector clears no entry.
const removedStringKeysOfWeakValuedTable = `
local function key(i) return "wr" .. i .. "zz" end
local same, accepted = 0, 0
for _, mode in ipairs({"v", ""}) do
  local order = {}
  for round = 1, 2 do
    local t = setmetatable({}, {__mode = mode})
    for i = 1, 12 do t[key(i)] = i end
    for i = 1, 12, 3 do t[key(i)] = nil end
    if round == 2 then
      collectgarbage() collectgarbage()
      for i = 1, 12, 3 do if pcall(next, t, key(i)) then accepted = accepted + 1 end end
    end
    for i = 1, 12, 3 do t[key(i)] = -i end
    local out = {}
    for k in pairs(t) do out[#out + 1] = k end
    order[round] = table.concat(out, ",")
  end
  if order[1] == order[2] then same = same + 1 end
end
return same, accepted`

// TestRemovedStringKeysOfWeakValuedTable pins that, in both collection modes. Tables with weak
// keys are left out: there the collector also clears the entries whose string keys nothing else
// refers to (#348), so whether such a key is still in the table when the script removes it
// already depends on when collections ran.
func TestRemovedStringKeysOfWeakValuedTable(t *testing.T) {
	for _, stress := range []bool{false, true} {
		if got := runClearCase(t, removedStringKeysOfWeakValuedTable, stress); got != "2\t8" {
			t.Errorf("wangshu (collecting at every safepoint: %v):\n  got:  %q\n  want: %q", stress, got, "2\t8")
		}
	}
}

// nextOnRemovedKeyAfterStores are calls of next on a removed key after further stores to the table.
// Whether the table still has a node for such a key is internal state, and it differs from lua5.1
// in either direction. Five causes are known:
//
//   - the layout of the hash part: a new key whose main position is that node takes it over, and an
//     insertion that finds no free node rehashes, which drops every removed key. wangshu's hash
//     part is not laid out like lua5.1's (#349);
//   - collections: at each collection lua5.1 turns the key of every removed node into a dead key
//     if it is collectable (a string, a table, a function; not a number or a boolean), whether or
//     not the key's object is alive. It does so while marking the table (traversetable), and a
//     table whose keys and values are both weak is not traversed, so there a key the script
//     removed stays as it is. next still finds a dead key's node, a store does not, so the same
//     key stored again is inserted as a new key and can cause a rehash. wangshu puts a removed
//     key back into its node as long as the key's object is alive, however many collections ran
//     in between, and a string the script removed stays alive (06 §8.4);
//   - stores that leave no value behind: lua5.1's luaH_set creates a node for a key before it
//     knows that the value is nil or that __newindex will take the store, so such a store to an
//     absent key leaves a removed key behind, uses up a free node and can cause a rehash. In
//     wangshu it does not touch the table;
//   - keys that are objects: a table, function, userdata or thread used as a key is hashed by its
//     address, in both implementations, and the addresses the two allocators hand out have nothing
//     to do with each other. Which node such a key lands on, and so whether it takes a removed
//     key's node over, also varies within one implementation with what was allocated and
//     collected before (TestNextOnRemovedKey_ObjectKeysInsertedAfterwards);
//   - dead keys matched by address: after a collection lua5.1's next recognises a removed key
//     whose object is gone by the address it had, so it also accepts an object or string that
//     never was in the table and happens to be allocated there. wangshu's next raises for every
//     key the table has no node for (TestDiff_NextFromKeysNeverInTheTable).
//
// The manual leaves the behaviour undefined once a traversal has assigned to a field that did not
// exist; the cases are here so that a change in wangshu's answers is noticed. want is wangshu's
// result, oracle is lua5.1's.
var nextOnRemovedKeyAfterStores = []struct{ name, src, want, oracle string }{
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
	// The keys stay alive in K. After the collection lua5.1 inserts the five of them as new keys,
	// finds no free node and rehashes, which drops the three that were not stored again.
	{"same_keys_stored_again_after_a_collection", `
local K = {} for i = 1, 8 do K[i] = "key" .. i end
local t = {}
for i = 1, 8 do t[K[i]] = i end
for i = 1, 8 do t[K[i]] = nil end
collectgarbage()
for i = 1, 5 do t[K[i]] = i end
return (pcall(next, t, K[6]))`, "true", "false"},
	// The same in a table whose keys and values are both weak: lua5.1 does not turn the removed
	// keys into dead keys there, so the five go back into their nodes and the two agree.
	{"same_keys_stored_again_after_a_collection_weak_keys_and_values", `
local K = {} for i = 1, 8 do K[i] = "key" .. i end
local t = setmetatable({}, {__mode = "kv"})
for i = 1, 8 do t[K[i]] = i end
for i = 1, 8 do t[K[i]] = nil end
collectgarbage()
for i = 1, 5 do t[K[i]] = i end
return (pcall(next, t, K[6]))`, "true", "true"},
	// A nil stored to a key that was never there: lua5.1 has a node for it afterwards.
	{"nil_stored_to_an_absent_key", `
local t = {a = 1} t.x = nil
local u = {a = 1} rawset(u, "y", nil)
local w = setmetatable({}, {__newindex = function() end}) w.z = 5
return (pcall(next, t, "x")), (pcall(next, u, "y")), (pcall(next, w, "z"))`,
		"false\tfalse\tfalse", "true\ttrue\ttrue"},
	// The three nil stores take lua5.1's free nodes and the last one rehashes, dropping "a".
	{"nil_stores_to_absent_keys_after_a_removal", `
local e = {} e.a = 1 e.b = 2 e.a = nil
for i = 1, 3 do e["q" .. i] = nil end
return (pcall(next, e, "a"))`, "true", "false"},
	{"clearing_traversal_that_also_stores_nil_to_absent_keys", `
local s = {} for i = 1, 8 do s["k" .. i] = i end
local n = 0
return pcall(function() for k in pairs(s) do s[k] = nil s["absent" .. k] = nil n = n + 1 end return n end)`,
		"true\t8", "false\tinvalid key to 'next'"},
}

// TestDiff_NextOnRemovedKeyAfterStores pins both sides of nextOnRemovedKeyAfterStores. Unlike the
// collected-string case, lua5.1's results here follow from its table layout and from which objects
// are alive, not from where the allocator puts them, so they are asserted.
func TestDiff_NextOnRemovedKeyAfterStores(t *testing.T) {
	oracle := findOracle()
	for _, c := range nextOnRemovedKeyAfterStores {
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

// nextFromKeysNeverInTheTable removes a table used as a key, lets it be collected, and then asks
// next about 200 fresh tables that never were keys of t, in 30 rounds. The result is the number of
// calls that did not raise.
const nextFromKeysNeverInTheTable = `
local ok = 0
for i = 1, 30 do
  local t = {}
  for j = 1, 4 do t["s" .. j] = j end
  do local k = {} t[k] = 1 t[k] = nil end
  collectgarbage() collectgarbage()
  local objs = {}
  for m = 1, 200 do objs[m] = {} end
  for m = 1, 200 do if pcall(next, t, objs[m]) then ok = ok + 1 end end
end
return ok`

// TestDiff_NextFromKeysNeverInTheTable pins wangshu's side: every call raises, with the collector
// in its normal mode and collecting at every safepoint. lua5.1 accepts the fresh table that is
// allocated where the collected key was; its count depends on the C allocator and is logged.
func TestDiff_NextFromKeysNeverInTheTable(t *testing.T) {
	for _, stress := range []bool{false, true} {
		if got := runClearCase(t, nextFromKeysNeverInTheTable, stress); got != "0" {
			t.Errorf("wangshu (collecting at every safepoint: %v):\n  got:  %q\n  want: %q", stress, got, "0")
		}
	}
	if oracle := findOracle(); oracle != "" {
		t.Logf("lua5.1 here: %s", strings.TrimSuffix(runOracle(t, oracle, wrapForOracle(nextFromKeysNeverInTheTable)), "\n"))
	}
}

// nextAfterInsertingObjectKeys removes a string key from each of 40 tables, inserts two fresh
// tables as keys and asks next for the removed key. A key that is an object is hashed by its
// address, so whether one of the two lands on the removed key's node, or the insertion rehashes,
// depends on where the allocator put them, and that depends on which collections ran before. The
// result is a string of 40 marks, o where next accepted the removed key and x where it raised.
const nextAfterInsertingObjectKeys = `
local keep, out = {}, {}
for j = 1, 40 do
  local t = {}
  for i = 1, 20 do t["s" .. i] = i end
  t["del" .. j] = 1
  t["del" .. j] = nil
  for i = 1, 2 do local k = {} keep[#keep + 1] = k t[k] = i end
  out[j] = pcall(next, t, "del" .. j) and "o" or "x"
end
return table.concat(out)`

// TestNextOnRemovedKey_ObjectKeysInsertedAfterwards runs nextAfterInsertingObjectKeys with the
// collector in its normal mode and collecting at every safepoint. The two results are allowed to
// differ, and are logged: with object keys the layout of a table follows the addresses of the keys,
// so the traversal order of such a table has always varied with the timing of collections, and
// since a removed key keeps its node, so does the answer of next for that key. What is asserted is
// that each run completes and that every call either succeeded or raised.
func TestNextOnRemovedKey_ObjectKeysInsertedAfterwards(t *testing.T) {
	for _, stress := range []bool{false, true} {
		got := runClearCase(t, nextAfterInsertingObjectKeys, stress)
		if len(got) != 40 || strings.Trim(got, "ox") != "" {
			t.Errorf("collecting at every safepoint: %v: got %q, want 40 marks of o or x", stress, got)
		}
		t.Logf("collecting at every safepoint: %v: %s", stress, got)
	}
}
