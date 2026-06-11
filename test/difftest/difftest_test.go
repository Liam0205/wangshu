// Package difftest implements the cross-implementation byte-equal harness:
// Wangshu vs official Lua 5.1.5 (12 §3 的 M14 最小可用版).
//
// 当前覆盖:固定脚本集(seed corpus)的输出对拍。随机脚本生成器(真 fuzz)
// 后续接入(12 §3.2 generator)。无 lua5.1 时跳过(CI 的 oracle 供给见
// engineering.md;本地 `make difftest` 前先跑 scripts/check-oracle.sh)。
package difftest

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
)

// findOracle 返回 lua5.1 可执行文件路径;不存在返回空。
func findOracle() string {
	for _, name := range []string{"lua5.1", "lua51"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

// runOracle 用官方 lua5.1 跑脚本,返回 stdout。
func runOracle(t *testing.T, oracle, src string) string {
	t.Helper()
	cmd := exec.Command(oracle, "-")
	cmd.Stdin = strings.NewReader(src)
	var out bytes.Buffer
	cmd.Stdout = &out
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("oracle run: %v\nstderr: %s", err, stderr.String())
	}
	return out.String()
}

// runWangshu 用 Wangshu 跑脚本,捕获 print 输出。
//
// M14 简化:print 输出直接进 Go 进程 stdout 不可捕获,因此 difftest 用
// `return` 值对拍而非 stdout——脚本统一以 `return tostring(expr)` 形态构造,
// oracle 侧由 wrapper 把返回值 print 出来。
func runWangshu(t *testing.T, src string) string {
	t.Helper()
	prog, err := wangshu.Compile([]byte(src), "difftest")
	if err != nil {
		t.Fatalf("wangshu compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	results, err := prog.Run(st)
	if err != nil {
		t.Fatalf("wangshu run: %v", err)
	}
	parts := make([]string, len(results))
	for i, r := range results {
		parts[i] = r.GoString()
	}
	return strings.Join(parts, "\t") + "\n"
}

// diffCase 是一个对拍用例:脚本必须以 return 结尾(单值或多值)。
type diffCase struct {
	name string
	src  string
}

// seedCorpus 是首批固定脚本(12 §2 思路:每个语义角落一个用例)。
var seedCorpus = []diffCase{
	{"arith_basic", `return 1 + 2 * 3 - 4 / 2`},
	{"arith_mod", `return 7 % 3`},
	{"arith_mod_neg", `return -7 % 3`},
	{"arith_pow", `return 2 ^ 10`},
	{"arith_div_float", `return 7 / 2`},
	{"concat_str_num", `return "v=" .. 42`},
	{"compare_lt", `return tostring(1 < 2)`},
	{"compare_le", `return tostring(2 <= 2)`},
	{"compare_strings", `return tostring("abc" < "abd")`},
	{"logic_and_or", `return (nil and 1) or "fallback"`},
	{"numeric_for", `
local s = 0
for i = 1, 100 do s = s + i end
return s`},
	{"numeric_for_step", `
local s = 0
for i = 10, 1, -2 do s = s + i end
return s`},
	{"while_loop", `
local i, n = 0, 0
while i < 50 do i = i + 1; n = n + i end
return n`},
	{"repeat_until", `
local i = 0
repeat i = i + 1 until i >= 5
return i`},
	{"recursive_fib", `
local function fib(n)
  if n < 2 then return n end
  return fib(n-1) + fib(n-2)
end
return fib(15)`},
	{"tail_call", `
local function loop(n, acc)
  if n == 0 then return acc end
  return loop(n - 1, acc + n)
end
return loop(100, 0)`},
	{"closure_counter", `
local function counter()
  local n = 0
  return function() n = n + 1; return n end
end
local c = counter()
c(); c()
return c()`},
	{"table_array", `
local t = { 10, 20, 30 }
return t[1] + t[2] + t[3]`},
	{"table_hash", `
local t = { x = 1, y = 2 }
t.z = t.x + t.y
return t.z`},
	{"string_len", `return #"hello"`},
	{"string_lib", `return string.upper("abc") .. string.sub("hello", 2, 4)`},
	{"math_lib", `return math.max(3, 7) + math.min(2, 5) + math.abs(-4)`},
	{"tostring_num", `return tostring(3.14)`},
	{"tostring_int", `return tostring(100)`},
	{"tonumber_str", `return tonumber("42") + 1`},
	{"multi_assign", `
local a, b, c = 1, 2
return tostring(a) .. tostring(b) .. tostring(c)`},
	{"vararg_pass", `
local function f(...)
  local a, b = ...
  return a + b
end
return f(3, 4)`},
	{"metatable_index", `
local base = { v = 7 }
local t = setmetatable({}, { __index = base })
return t.v`},
	{"pcall_ok", `
local ok, v = pcall(function() return 9 end)
return tostring(ok) .. tostring(v)`},
	{"pcall_err", `
local ok = pcall(function() error("x") end)
return tostring(ok)`},
	{"nan_compare", `return tostring(0/0 == 0/0)`},
	{"inf_div", `return tostring(1/0)`},
	{"neg_zero", `return tostring(-0)`},
	// —— 覆盖率审计补充(2026-06-12) ——
	{"method_call", `
local t = { v = 10 }
function t.get(self) return self.v end
return t:get()`},
	{"method_def_colon", `
local obj = { n = 5 }
function obj:bump() self.n = self.n + 1; return self.n end
obj:bump()
return obj:bump()`},
	{"func_stmt_global", `
function double(x) return x * 2 end
return double(21)`},
	{"table_len", `return #{1, 2, 3}`},
	{"return_vararg", `
local function f(...) return ... end
return f(1, 2, 3)`},
	{"vararg_forward", `
local function sum3(a, b, c) return a + b + c end
local function fwd(...) return sum3(...) end
return fwd(1, 2, 3)`},
	{"select_hash", `return select("#", 1, 2, 3)`},
	{"break_in_for", `
local n = 0
for i = 1, 10 do
  n = i
  if i >= 4 then break end
end
return n`},
	{"break_in_while", `
local i = 0
while true do
  i = i + 1
  if i >= 3 then break end
end
return i`},
	{"string_sub_neg", `return string.sub("hello", -3)`},
	{"string_rep", `return string.rep("ab", 3)`},
}

// TestDiff_SeedCorpus 对拍固定脚本集(Wangshu vs 官方 5.1.5)。
func TestDiff_SeedCorpus(t *testing.T) {
	oracle := findOracle()
	if oracle == "" {
		t.Skip("lua5.1 oracle not found on PATH; skipping difftest")
	}
	for _, c := range seedCorpus {
		t.Run(c.name, func(t *testing.T) {
			// oracle wrapper:把 return 值 print 出来(tostring 规约)
			oracleSrc := wrapForOracle(c.src)
			want := runOracle(t, oracle, oracleSrc)
			got := runWangshu(t, c.src)
			if got != want {
				t.Errorf("byte-diff:\n  wangshu: %q\n  oracle:  %q", got, want)
			}
		})
	}
}

// wrapForOracle 把 `return expr` 脚本变成 oracle 侧的 print 形态:
// 把 chunk 当函数调用,返回值逐个 tostring 后用 \t join print。
func wrapForOracle(src string) string {
	return `
local function __chunk()
` + src + `
end
local __r = { __chunk() }
local __parts = {}
for i = 1, #__r do __parts[i] = tostring(__r[i]) end
print(table.concat(__parts, "\t"))
`
}
