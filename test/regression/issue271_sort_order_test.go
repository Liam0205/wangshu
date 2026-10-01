package regression

import (
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
	"github.com/Liam0205/wangshu/test/testutil"
)

// sortShow renders t[1..n] with NaN as "N", so the expectations do not depend on how the host libc
// spells a NaN.
const sortShow = `local function show(t, n) local o = {} for i = 1, n do local v = t[i] o[i] = v ~= v and "N" or tostring(v) end return table.concat(o, " ") end
local function msg(e) return (string.gsub(e, "^[^:]*:%d+: ", "")) end
`

// TestTableSortFollowsAuxsort covers #271: table.sort ran sort.SliceStable over a copy of the array,
// which agrees with lua5.1's quicksort (ltablib.c auxsort) only for a strict weak ordering. A NaN element
// or an inconsistent comparator placed elements differently, and the copy hid what a comparator can
// observe: the table mid-sort, the partial order left when it raises, and "invalid order function for
// sorting". Every expectation is the lua5.1 answer.
func TestTableSortFollowsAuxsort(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"#271 seed", `local t = {0, 0, 0%0, 0} table.sort(t) return show(t, 4)`, "0 N 0 0"},
		{"several NaNs", `local n = 0/0 local t = {3, n, 1, n, 2, 0, n, 5, 4} table.sort(t) return show(t, 9)`,
			"0 1 N 2 3 N 5 N 4"},
		{"comparison sequence",
			`local t = {5, 3, 8, 1, 9, 2, 7, 4, 6, 0} local log = {}
			table.sort(t, function(a, b) log[#log+1] = a .. "<" .. b return a < b end)
			return table.concat(log, ",") .. "|" .. show(t, 10)`,
			"0<5,9<0,5<9,3<5,8<5,5<4,1<5,6<5,5<7,5<2,6<5,5<2,9<7,8<7,9<8,6<8,8<8,8<6,6<7,2<0,4<0,2<4,3<2,2<1,3<2,2<1,4<3,1<0|0 1 2 3 4 5 6 7 8 9"},
		{"always-true comparator",
			`local t = {3, 1, 4, 1, 5, 9, 2, 6} local ok, e = pcall(table.sort, t, function() return true end)
			return tostring(ok) .. "|" .. e .. "|" .. show(t, 8)`,
			"false|invalid order function for sorting|1 1 4 2 5 9 6 3"},
		{"<= comparator walks off the end",
			`local t = {} for i = 1, 6 do t[i] = 1 end local ok, e = pcall(table.sort, t, function(a, b) return a <= b end)
			return tostring(ok) .. "|" .. msg(e) .. "|" .. show(t, 6)`,
			"false|attempt to compare nil with number|1 1 1 1 1 1"},
		{"~= comparator",
			`local ok, e = pcall(table.sort, {1, 2, 3, 4, 5, 6, 7, 8, 9}, function(a, b) return a ~= b end)
			return tostring(ok) .. "|" .. e`,
			"false|invalid order function for sorting"},
		{"comparator raises: partial order stays",
			`local t = {9, 8, 7, 6, 5, 4, 3, 2, 1, 0} local c = 0
			local ok, e = pcall(table.sort, t, function(a, b) c = c + 1 if c == 8 then error("stop", 0) end return a < b end)
			return tostring(ok) .. "|" .. e .. "|" .. show(t, 10)`,
			"false|stop|0 2 3 6 1 4 7 8 5 9"},
		{"comparator sees the table mid-sort",
			`local t = {4, 2, 6, 1, 5, 3, 8, 7} local snap = {}
			table.sort(t, function(a, b) snap[#snap+1] = show(t, 8) return a < b end)
			return snap[4] .. "|" .. snap[9] .. "|" .. show(t, 8)`,
			"1 2 6 8 5 3 4 7|1 2 3 8 5 6 4 7|1 2 3 4 5 6 7 8"},
		{"equal keys are not kept stable",
			`local t = {} for i = 1, 12 do t[i] = {k = i % 3, id = i} end
			table.sort(t, function(a, b) return a.k < b.k end)
			local o = {} for i = 1, 12 do o[i] = t[i].id end return table.concat(o, " ")`,
			"12 9 3 6 1 4 7 10 2 8 11 5"},
		{"string with number", `return select(2, pcall(table.sort, {1, "a", 2}))`, "attempt to compare string with number"},
		{"nil hole", `return select(2, pcall(table.sort, {1, 2, nil, 3}))`, "attempt to compare nil with number"},
		{"boolean with number", `return select(2, pcall(table.sort, {1, 2, 3, 4, true}))`, "attempt to compare boolean with number"},
		{"string with thread", `return select(2, pcall(table.sort, {"s", coroutine.create(function() end)}))`,
			"attempt to compare two thread values"},
		{"__lt on one side only",
			`local A = setmetatable({}, {__lt = function() return true end}) return select(2, pcall(table.sort, {A, {}}))`,
			"attempt to compare two table values"},
	} {
		if got := testutil.RunOne(t, sortShow+tc.src).Str(); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestOrderComparisonMetamethodRules covers the VM half found while fixing #271: OP_LT/OP_LE took __lt
// from either operand and ignored the operand types, so `A < {}` and even `A < 1` returned the handler's
// answer. lua5.1 (lvm.c luaV_lessthan / lessequal, call_orderTM) raises on different types before looking
// at metamethods and calls the handler only when both operands carry the same one. Expectations are the
// lua5.1 answers.
func TestOrderComparisonMetamethodRules(t *testing.T) {
	const pre = `local function try(f) local ok, e = pcall(f) if ok then return tostring(e) end return (string.gsub(e, "^[^:]*:%d+: ", "")) end
local f = function() return true end
local A, B = setmetatable({}, {__lt = f}), setmetatable({}, {__lt = f})
`
	for _, tc := range []struct{ name, src, want string }{
		{"handler on the left only", `return try(function() return A < {} end)`, "attempt to compare two table values"},
		{"handler on the right only", `return try(function() return {} < A end)`, "attempt to compare two table values"},
		{"same handler on both", `return try(function() return A < B end)`, "true"},
		{"different types", `return try(function() return A < 1 end)`, "attempt to compare table with number"},
		{"string with thread", `local co = coroutine.create(f) return try(function() return "s" < co end)`,
			"attempt to compare two string values"},
		{"thread with string", `local co = coroutine.create(f) return try(function() return co < "s" end)`,
			"attempt to compare two thread values"},
		{"callable-table handler",
			`local C = setmetatable({}, {__call = function() return "yes" end}) local m = {__lt = C, __le = C}
			local X, Y = setmetatable({}, m), setmetatable({}, m)
			return try(function() return tostring(X < Y) .. tostring(X <= Y) .. tostring(X > Y) .. tostring(X >= Y) end)`,
			"truetruetruetrue"},
		{"__le only: <= works", `local m = {__le = function() return 1 end}
			return try(function() return setmetatable({}, m) <= setmetatable({}, m) end)`, "true"},
		{"__le only: < raises", `local m = {__le = function() return 1 end}
			return try(function() return setmetatable({}, m) < setmetatable({}, m) end)`, "attempt to compare two table values"},
		{"<= falls back to not __lt with swapped operands", `local m = {__lt = function(a, b) return a.v < b.v end}
			local P, Q = setmetatable({v = 1}, m), setmetatable({v = 2}, m)
			return tostring(P <= Q) .. tostring(Q <= P) .. tostring(P >= Q)`, "truefalsefalse"},
		{"different __le handlers", `local X = setmetatable({}, {__le = function() return true end})
			local Y = setmetatable({}, {__le = function() return true end})
			return try(function() return X <= Y end)`, "attempt to compare two table values"},
		{"<= without metamethods", `return try(function() return {} <= {} end)`, "attempt to compare two table values"},
		{"strings", `local a, b = "a", "b" return tostring(a < b) .. tostring(b <= a) .. tostring(a <= a)`, "truefalsetrue"},
		{"non-callable handler", `local m = {__lt = 5} return try(function() return setmetatable({}, m) < setmetatable({}, m) end)`,
			"attempt to call a number value"},
	} {
		if got := testutil.RunOne(t, pre+tc.src).Str(); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestTableSortAdversarialInputIsMetered pins the budget backstop that came with the auxsort port. The
// stable merge sort it replaced was O(n log n) on every input; quicksort is quadratic on a permutation
// built by McIlroy's adversary, and only the prepaid n*log2(n) was charged, so such a sort ran unmetered.
// Here a 3000-element killer needs about 2.25M comparisons against roughly 35k for an honest permutation
// of the same size: the honest sort must fit the budget and the killer must trip it.
func TestTableSortAdversarialInputIsMetered(t *testing.T) {
	const setup = `
local function killer(n)
  local val, ptr, gas, nsolid, candidate = {}, {}, n + 1, 0, 1
  for i = 1, n do ptr[i] = i val[i] = gas end
  table.sort(ptr, function(x, y)
    if val[x] == gas and val[y] == gas then
      if x == candidate then val[x] = nsolid else val[y] = nsolid end
      nsolid = nsolid + 1
    end
    if val[x] == gas then candidate = x elseif val[y] == gas then candidate = y end
    return val[x] < val[y]
  end)
  local t = {} for i = 1, n do t[i] = val[i] end
  return t
end
K = killer(3000)
R = {} for i = 1, 3000 do R[i] = (i * 7919) % 3000 end
`
	st := wangshu.NewState(wangshu.Options{})
	run := func(src string) error {
		prog, err := wangshu.Compile([]byte(src), "i271")
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		_, err = prog.Run(st)
		return err
	}
	if err := run(setup); err != nil {
		t.Fatalf("setup: %v", err)
	}
	st.SetStepBudget(8000)
	if err := run(`table.sort(R)`); err != nil {
		t.Fatalf("honest permutation should fit the budget: %v", err)
	}
	st.SetStepBudget(8000)
	if err := run(`table.sort(K)`); err == nil || !strings.Contains(err.Error(), "instruction budget exceeded") {
		t.Fatalf("adversarial permutation should trip the budget, got %v", err)
	}
}
