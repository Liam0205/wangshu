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
	// The callback drops its own reference to the value and every other one before collecting:
	// table.foreach still holds the value, so the entry of the weak-valued table is not cleared
	// while its callback runs.
	{"table_foreach_keeps_the_current_value", `
local w = setmetatable({}, {__mode = "v"})
local hold = {}
for i = 1, 5 do hold[i] = {} w["k" .. i] = hold[i] end
local n, seen = 0, 0
table.foreach(w, function(k, v)
  n = n + 1
  hold, v = nil, nil
  collectgarbage()
  if rawget(w, k) ~= nil then seen = seen + 1 end
  return true
end)
return n, seen`, "1\t1"},
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

// nextFromFixedKeysOfClearedEntries is the same with keys lua5.1 never collects: it fixes the
// reserved words, the metamethod names listed in ltm.c and the "not enough memory" message
// (luaS_fix in llex.c, ltm.c and lstate.c), so there the dead key of a cleared entry goes on
// pointing at a live string and next accepts it every time. wangshu fixes no string: such a key
// goes like any other, and next raises unless something else refers to the string. "__index" is
// one that something else refers to (it is a key of the string metatable), so there the two
// agree. The script counts the calls that succeed in 30 rounds, for "__call", "while",
// "not enough memory" and "__index".
const nextFromFixedKeysOfClearedEntries = `
local function count(build)
  local ok = 0
  for round = 1, 30 do
    local w = setmetatable({}, {__mode = "v"})
    for i = 1, 4 do w["pad" .. i .. "_" .. round] = true end
    w[build()] = {}
    collectgarbage() collectgarbage() collectgarbage()
    if pcall(next, w, build()) then ok = ok + 1 end
  end
  return ok
end
return count(function() return "__ca" .. "ll" end), count(function() return "whi" .. "le" end),
  count(function() return "not enough" .. " memory" end), count(function() return "__ind" .. "ex" end)`

// TestDiff_NextFromFixedKeysOfClearedEntries pins both sides of that: neither depends on where
// the allocator puts anything.
func TestDiff_NextFromFixedKeysOfClearedEntries(t *testing.T) {
	const wantWangshu, wantOracle = "0\t0\t0\t30", "30\t30\t30\t30"
	for _, stress := range []bool{false, true} {
		if got := runClearCase(t, nextFromFixedKeysOfClearedEntries, stress); got != wantWangshu {
			t.Errorf("wangshu (collecting at every safepoint: %v):\n  got:  %q\n  want: %q", stress, got, wantWangshu)
		}
	}
	if oracle := findOracle(); oracle != "" {
		if got := strings.TrimSuffix(runOracle(t, oracle, wrapForOracle(nextFromFixedKeysOfClearedEntries)), "\n"); got != wantOracle {
			t.Errorf("lua5.1 no longer answers as recorded:\n  got:      %q\n  recorded: %q", got, wantOracle)
		}
	}
}

// nextFromRuntimeHeldKeysOfClearedEntries is the same for strings the runtime itself refers to.
// lua5.1's os table has the keys "execute" and "getenv" and its registry the key "_LOADED", so
// those strings stay alive and next accepts them. wangshu provides os.getenv, and neither
// os.execute nor a package library, so only "getenv" is alive there.
const nextFromRuntimeHeldKeysOfClearedEntries = `
local function count(build)
  local ok = 0
  for round = 1, 30 do
    local w = setmetatable({}, {__mode = "v"})
    for i = 1, 4 do w["pad" .. i .. "_" .. round] = true end
    w[build()] = {}
    collectgarbage() collectgarbage() collectgarbage()
    if pcall(next, w, build()) then ok = ok + 1 end
  end
  return ok
end
return count(function() return "exec" .. "ute" end), count(function() return "_LOAD" .. "ED" end),
  count(function() return "get" .. "env" end)`

// TestDiff_NextFromRuntimeHeldKeysOfClearedEntries pins both sides of that.
func TestDiff_NextFromRuntimeHeldKeysOfClearedEntries(t *testing.T) {
	const wantWangshu, wantOracle = "0\t0\t30", "30\t30\t30"
	for _, stress := range []bool{false, true} {
		if got := runClearCase(t, nextFromRuntimeHeldKeysOfClearedEntries, stress); got != wantWangshu {
			t.Errorf("wangshu (collecting at every safepoint: %v):\n  got:  %q\n  want: %q", stress, got, wantWangshu)
		}
	}
	if oracle := findOracle(); oracle != "" {
		if got := strings.TrimSuffix(runOracle(t, oracle, wrapForOracle(nextFromRuntimeHeldKeysOfClearedEntries)), "\n"); got != wantOracle {
			t.Errorf("lua5.1 no longer answers as recorded:\n  got:      %q\n  recorded: %q", got, wantOracle)
		}
	}
}

// nextFromDebugNamesOfClearedEntries is the same for the names in debug information. lua5.1's
// function prototypes refer to their chunk name and to the names of their locals and upvalues as
// strings, so those of any function that is still alive stay alive; wangshu keeps them outside
// the arena. The chunk name is built at run time, or it would be a constant of this script. The
// fourth key has the same form and names nothing: it shows that lua5.1's answers to the first
// three are not address reuse.
const nextFromDebugNamesOfClearedEntries = `
local function keepme(a) local zzqqlocal = a + 1 local function inner() return zzqqlocal end return inner end
local held = keepme(1)
local heldchunk = loadstring("return 1", "zzqq" .. "chunk")
local function count(build)
  local ok = 0
  for round = 1, 30 do
    local w = setmetatable({}, {__mode = "v"})
    for i = 1, 4 do w["pad" .. i .. "_" .. round] = true end
    w[build()] = {}
    collectgarbage() collectgarbage() collectgarbage()
    local junk = {}
    for j = 1, 64 do junk[j] = ("#"):rep(7) .. j end
    if pcall(next, w, build()) then ok = ok + 1 end
  end
  return ok
end
return count(function() return "zzqq" .. "local" end), count(function() return "kee" .. "pme" end),
  count(function() return "zzqq" .. "chunk" end), count(function() return "zzqq" .. "other" end),
  held(), heldchunk()`

// checkOracleHeldKeys compares lua5.1's counts with the recorded ones. The count at index control
// belongs to a key that nothing holds: lua5.1 accepts it only in the rounds where the rebuilt
// string lands on the address of the collected one, and that depends on the C allocator. It is
// logged and only required to stay below the 30 rounds, since a control key accepted every time
// would say nothing about the other answers.
func checkOracleHeldKeys(t *testing.T, got, recorded string, control int) {
	t.Helper()
	g, r := strings.Split(got, "\t"), strings.Split(recorded, "\t")
	if len(g) != len(r) {
		t.Errorf("lua5.1 no longer answers as recorded:\n  got:      %q\n  recorded: %q", got, recorded)
		return
	}
	for i := range r {
		switch {
		case i == control && g[i] == "30":
			t.Errorf("lua5.1 accepted the control key in every round, so its other answers may be address reuse: %q", got)
		case i != control && g[i] != r[i]:
			t.Errorf("lua5.1 no longer answers as recorded:\n  got:      %q\n  recorded: %q", got, recorded)
			return
		}
	}
	t.Logf("lua5.1 here: %s", got)
}

// TestDiff_NextFromDebugNamesOfClearedEntries pins both sides of that; of lua5.1's answers the
// one for the fourth key is the control (see checkOracleHeldKeys).
func TestDiff_NextFromDebugNamesOfClearedEntries(t *testing.T) {
	const wantWangshu, wantOracle = "0\t0\t0\t0\t2\t1", "30\t30\t30\t0\t2\t1"
	for _, stress := range []bool{false, true} {
		if got := runClearCase(t, nextFromDebugNamesOfClearedEntries, stress); got != wantWangshu {
			t.Errorf("wangshu (collecting at every safepoint: %v):\n  got:  %q\n  want: %q", stress, got, wantWangshu)
		}
	}
	if oracle := findOracle(); oracle != "" {
		checkOracleHeldKeys(t, strings.TrimSuffix(runOracle(t, oracle, wrapForOracle(nextFromDebugNamesOfClearedEntries)), "\n"), wantOracle, 3)
	}
}

// nextFromKeysHeldByWeakTables is the same for strings that only another weak table refers to,
// as the key of an entry of a weak-keyed table or as the value of an entry of a weak-valued one.
// lua5.1 never clears a string from a weak table (iscleared marks it), so those strings stay
// alive; wangshu's weak tables clear strings like other objects (issue #348). The next two keys
// are held from the strong side of a weak table (the key of an entry of a weak-valued table, the
// value of an entry of a weak-keyed one) and stay alive on both sides. The last key is held by
// nothing and shows that lua5.1's answers to the others are not address reuse.
const nextFromKeysHeldByWeakTables = `
local wk = setmetatable({}, {__mode = "k"})
local wv = setmetatable({}, {__mode = "v"})
local keep = {}
local function count(prefix, hold)
  local ok = 0
  for round = 1, 30 do
    local c = setmetatable({}, {__mode = "v"})
    for i = 1, 4 do c["pad" .. i .. "_" .. round] = true end
    c[prefix .. round] = {}
    if hold == "k" then wk[prefix .. round] = true
    elseif hold == "v" then wv[round] = prefix .. round
    elseif hold == "vkey" then wv[prefix .. round] = keep
    elseif hold == "kval" then local t = {} keep[#keep + 1] = t wk[t] = prefix .. round end
    collectgarbage() collectgarbage() collectgarbage()
    local junk = {}
    for j = 1, 64 do junk[j] = ("#"):rep(7) .. j end
    if pcall(next, c, prefix .. round) then ok = ok + 1 end
  end
  return ok
end
return count("zzwk", "k"), count("zzwv", "v"), count("zzvk", "vkey"), count("zzkv", "kval"), count("zzno")`

// TestDiff_NextFromKeysHeldByWeakTables pins both sides of that; of lua5.1's answers the last one
// is the control (see checkOracleHeldKeys).
func TestDiff_NextFromKeysHeldByWeakTables(t *testing.T) {
	const wantWangshu, wantOracle = "0\t0\t30\t30\t0", "30\t30\t30\t30\t0"
	for _, stress := range []bool{false, true} {
		if got := runClearCase(t, nextFromKeysHeldByWeakTables, stress); got != wantWangshu {
			t.Errorf("wangshu (collecting at every safepoint: %v):\n  got:  %q\n  want: %q", stress, got, wantWangshu)
		}
	}
	if oracle := findOracle(); oracle != "" {
		checkOracleHeldKeys(t, strings.TrimSuffix(runOracle(t, oracle, wrapForOracle(nextFromKeysHeldByWeakTables)), "\n"), wantOracle, 4)
	}
}

// nextFromConstantsOfDroppedChunks goes the other way: wangshu holds a string that lua5.1 does
// not. A State keeps the string constants of every chunk it has loaded alive, also after the
// chunk's function is gone (issue #359); lua5.1 collects them with the function. So a cleared
// entry whose key is such a constant is accepted by wangshu's next every time. lua5.1 tells the
// key by its address, and its answer depends on what was allocated since; only wangshu's side
// is pinned.
const nextFromConstantsOfDroppedChunks = `
do local f = loadstring("return 'zzk' .. 'onst', 'zzkonst2'") f = nil end
collectgarbage()
local ok = 0
for round = 1, 30 do
  local w = setmetatable({}, {__mode = "v"})
  for i = 1, 4 do w["pad" .. i .. "_" .. round] = true end
  w["zzk" .. "onst2"] = {}
  collectgarbage() collectgarbage() collectgarbage()
  if pcall(next, w, "zzk" .. "onst2") then ok = ok + 1 end
end
return ok`

// TestDiff_NextFromConstantsOfDroppedChunks pins wangshu's side of that.
func TestDiff_NextFromConstantsOfDroppedChunks(t *testing.T) {
	for _, stress := range []bool{false, true} {
		if got := runClearCase(t, nextFromConstantsOfDroppedChunks, stress); got != "30" {
			t.Errorf("wangshu (collecting at every safepoint: %v): got %q, want %q", stress, got, "30")
		}
	}
}

// nextFromKeysRemovedFromAnotherTable is a second way for wangshu to hold a string that lua5.1
// does not. A key removed with an assignment of nil stays alive in its node, in whatever table
// that node is; lua5.1 does not mark the key of a removed node. So when the key of a cleared
// entry was also stored in and removed from another table, wangshu's next accepts it every
// time, and when it was not, never. lua5.1 tells the key by its address: with the strings
// allocated before the call it answered 0 and 1 here, without them 30 and 30. Only wangshu's
// side is pinned.
const nextFromKeysRemovedFromAnotherTable = `
local other = {}
local function count(build, remove)
  local ok = 0
  for round = 1, 30 do
    local w = setmetatable({}, {__mode = "v"})
    for i = 1, 4 do w["pad" .. i .. "_" .. round] = true end
    w[build()] = {}
    if remove then other[build()] = 1 other[build()] = nil end
    collectgarbage() collectgarbage() collectgarbage()
    local junk = {}
    for j = 1, 64 do junk[j] = ("#"):rep(7) .. j end
    if pcall(next, w, build()) then ok = ok + 1 end
  end
  return ok
end
return count(function() return "zzqq" .. "kept" end, false), count(function() return "zzqq" .. "removed" end, true)`

// TestDiff_NextFromKeysRemovedFromAnotherTable pins wangshu's side of that.
func TestDiff_NextFromKeysRemovedFromAnotherTable(t *testing.T) {
	for _, stress := range []bool{false, true} {
		if got := runClearCase(t, nextFromKeysRemovedFromAnotherTable, stress); got != "0\t30" {
			t.Errorf("wangshu (collecting at every safepoint: %v): got %q, want %q", stress, got, "0\t30")
		}
	}
}

// nextFromRemovedFixedKeys is the case of nextFromCollectedStringKey with keys lua5.1 never
// collects: a reserved word, a metamethod name and "not enough memory", each built at run time,
// stored in a table that is not weak and removed by a store of nil. lua5.1 does not collect such
// a string and wangshu's node keeps it alive, so next accepts the rebuilt key on both sides
// whatever was allocated and collected in between. The script counts the calls that succeed in
// 30 rounds for each key.
const nextFromRemovedFixedKeys = `
local function count(build)
  local ok = 0
  for round = 1, 30 do
    local t = {}
    for i = 1, 4 do t["pad" .. i .. "_" .. round] = true end
    t[build()] = true
    t[build()] = nil
    local junk = {}
    for i = 1, round do junk[i] = string.rep("x", i) end
    junk = nil
    collectgarbage() collectgarbage()
    if pcall(next, t, build()) then ok = ok + 1 end
  end
  return ok
end
return count(function() return "wh" .. "ile" end), count(function() return "__g" .. "c" end),
  count(function() return "not enough mem" .. "ory" end)`

// TestDiff_NextFromRemovedFixedKeys requires the two sides to agree on that, with the collector in
// its normal mode and collecting at every safepoint.
func TestDiff_NextFromRemovedFixedKeys(t *testing.T) {
	const want = "30\t30\t30"
	for _, stress := range []bool{false, true} {
		if got := runClearCase(t, nextFromRemovedFixedKeys, stress); got != want {
			t.Errorf("wangshu (collecting at every safepoint: %v):\n  got:  %q\n  want: %q", stress, got, want)
		}
	}
	if oracle := findOracle(); oracle != "" {
		if got := strings.TrimSuffix(runOracle(t, oracle, wrapForOracle(nextFromRemovedFixedKeys)), "\n"); got != want {
			t.Errorf("lua5.1 no longer answers as recorded:\n  got:      %q\n  recorded: %q", got, want)
		}
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

// TestRemovedStringKeysOfWeakValuedTable pins that, in both collection modes. The values are
// numbers, so the collector clears no entry; once it does, later insertions depend on when it
// did (TestDiff_InsertionsAfterClearedEntries). Tables with weak keys are left out: there the
// collector also clears the entries whose string keys nothing else refers to (#348), so whether
// such a key is still in the table when the script removes it already depends on when
// collections ran.
func TestRemovedStringKeysOfWeakValuedTable(t *testing.T) {
	for _, stress := range []bool{false, true} {
		if got := runClearCase(t, removedStringKeysOfWeakValuedTable, stress); got != "2\t8" {
			t.Errorf("wangshu (collecting at every safepoint: %v):\n  got:  %q\n  want: %q", stress, got, "2\t8")
		}
	}
}

// insertionsAfterClearedEntries removes a string key from a weak-valued table whose other keys are
// strings too, lets the values of three entries die, and inserts two more string keys. With
// collect true a collection runs before the insertions. The entries it clears become removed
// nodes, so the new keys land differently, and whether the table rehashes, which drops the node
// of the key the script removed, depends on it. No key here is hashed by address.
func insertionsAfterClearedEntries(collect bool) string {
	step := ""
	if collect {
		step = "collectgarbage()\n"
	}
	return `
local t = setmetatable({}, {__mode = "v"})
local hold = {}
for i = 1, 3 do hold[i] = {} t["b1_" .. i] = hold[i] end
t.A1 = 1
t.A1 = nil
hold = nil
` + step + `for i = 1, 2 do t["c1_" .. i] = i end
return (pcall(next, t, "A1"))`
}

// storesAfterTheTableWasMadeStrong lets the collector clear seven entries of a weak-valued table,
// makes the table strong, removes a key and stores the seven keys again. With collect true a
// collection runs before those stores. The cleared entries still carry the collector's mark, so
// that collection does not keep their string keys alive: the keys are replaced, the strings
// stored again are new keys, and the rehash they cause drops the node of the removed key. The
// table is strong and all its keys are strings, and the answer still depends on the collection.
// lua5.1 marked the keys dead when it cleared the entries, so it rehashes either way.
func storesAfterTheTableWasMadeStrong(collect bool) string {
	step := ""
	if collect {
		step = "collectgarbage()\n"
	}
	return `
local t = setmetatable({}, {__mode = "v"})
for i = 1, 7 do t["key" .. i] = {} end
t.keep = 1
collectgarbage()
setmetatable(t, nil)
t.keep = nil
` + step + `for i = 1, 7 do t["key" .. i] = i end
return (pcall(next, t, "keep"))`
}

// TestDiff_InsertionsAfterClearedEntries pins that a collection changes what next answers for a
// key the script removed from a table the collector has cleared entries of, in wangshu as in
// lua5.1 for the first script: it gives false without the collection and true with it, and
// collecting at every safepoint gives true for both. When the collector clears entries decides
// where later keys go. The second script shows the same for such a table after it was made
// strong.
func TestDiff_InsertionsAfterClearedEntries(t *testing.T) {
	oracle := findOracle()
	for _, c := range []struct {
		script               func(bool) string
		collect              bool
		normal, stress, lua5 string
	}{
		{insertionsAfterClearedEntries, false, "false", "true", "false"},
		{insertionsAfterClearedEntries, true, "true", "true", "true"},
		{storesAfterTheTableWasMadeStrong, false, "true", "false", "false"},
		{storesAfterTheTableWasMadeStrong, true, "false", "false", "false"},
	} {
		src := c.script(c.collect)
		if got := runClearCase(t, src, false); got != c.normal {
			t.Errorf("wangshu (explicit collection: %v):\n  got:  %q\n  want: %q", c.collect, got, c.normal)
		}
		if got := runClearCase(t, src, true); got != c.stress {
			t.Errorf("wangshu, collecting at every safepoint (explicit collection: %v):\n  got:  %q\n  want: %q", c.collect, got, c.stress)
		}
		if oracle != "" {
			if got := strings.TrimSuffix(runOracle(t, oracle, wrapForOracle(src)), "\n"); got != c.lua5 {
				t.Errorf("lua5.1 no longer answers as recorded (explicit collection: %v):\n  got:      %q\n  recorded: %q", c.collect, got, c.lua5)
			}
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
//     removed stays as it is. An entry the collector clears from a weak table has no such
//     exception: cleartable removes it the same way in all three modes, so its key becomes a dead
//     key even while the key's object is alive. next still finds a dead key's node, a store does not, so the same
//     key stored again is inserted as a new key and can cause a rehash. wangshu puts a removed
//     key back into its node as long as the key's object is alive, however many collections ran
//     in between, and a string the script removed stays alive (06 §8.4). The key of an entry the
//     collector cleared goes back into its node too while its object is alive;
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
//     whose object is gone by the address it had, so it also accepts an object that never was
//     in the table and happens to be allocated there, and a string likewise, which also has to
//     fall on the node's collision chain when its contents are not the old key's. wangshu's next
//     raises for every key the table has no node for (TestDiff_NextFromKeysNeverInTheTable).
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
	// keys into dead keys there, so the five go back into their nodes and the two agree. K keeps
	// the keys referenced; a removed string key nothing else refers to is collected by lua5.1, and
	// stored again it is a new key unless the new string lands on the old address.
	{"same_keys_stored_again_after_a_collection_weak_keys_and_values", `
local K = {} for i = 1, 8 do K[i] = "key" .. i end
local t = setmetatable({}, {__mode = "kv"})
for i = 1, 8 do t[K[i]] = i end
for i = 1, 8 do t[K[i]] = nil end
collectgarbage()
for i = 1, 5 do t[K[i]] = i end
return (pcall(next, t, K[6]))`, "true", "true"},
	// The collector clears the entries of key1, key3 and key5 once hold lets go of their values;
	// keys keeps the strings referenced. lua5.1's cleartable turns the three keys into dead keys, so stored again
	// they are new keys, and key5 goes to its main position, the node of the key2 the script
	// removed, and takes it over. wangshu writes the three back into their nodes.
	{"keys_of_cleared_entries_stored_again_weak_values", keysOfClearedEntriesStoredAgain("v"), "true", "false"},
	// A table whose keys and values are both weak is cleared the same way, though marking skips it.
	{"keys_of_cleared_entries_stored_again_weak_keys_and_values", keysOfClearedEntriesStoredAgain("kv"), "true", "false"},
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

// keysOfClearedEntriesStoredAgain is the script of the two keys_of_cleared_entries cases for a
// table with the given __mode.
func keysOfClearedEntriesStoredAgain(mode string) string {
	return `
local w = setmetatable({}, {__mode = "` + mode + `"})
local hold, keys = {}, {}
for i = 1, 5 do keys[i] = "key" .. i end
for i = 1, 5 do hold[i] = {} w[keys[i]] = hold[i] end
w[keys[2]] = nil
hold[1], hold[3], hold[5] = nil, nil, nil
collectgarbage()
for _, i in ipairs({1, 3, 5}) do hold[i] = {} w[keys[i]] = hold[i] end
return (pcall(next, w, keys[2]))`
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
