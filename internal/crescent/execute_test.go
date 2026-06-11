// 端到端解释器测试 — 算术 / 循环 / 调用三档(05 M9 验收口径)。
package crescent

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/frontend/compile"
	"github.com/Liam0205/wangshu/internal/frontend/lex"
	"github.com/Liam0205/wangshu/internal/frontend/parse"
	"github.com/Liam0205/wangshu/internal/value"
)

// runLua 编译 src,加载,执行 main chunk 并返回结果。
func runLua(t *testing.T, src string) *State {
	t.Helper()
	lx := lex.New([]byte(src), "test")
	block, err := parse.Parse(lx, "test")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	mainID, protos, err := compile.Compile(block, "test")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := New()
	cl := st.LoadProgram(mainID, protos)
	if _, err := st.Call(cl, nil, 0); err != nil {
		t.Fatalf("call: %v", err)
	}
	return st
}

// TestExec_LocalArith — 简单算术 + 局部:验证 NaN-box 直算与 R(A) 写入。
func TestExec_LocalArith(t *testing.T) {
	src := `
local function add(a, b) return a + b end
result = add(3, 4)
`
	st := runLua(t, src)
	v, _ := st.tableGet(st.globals, st.makeStringValue("result"))
	if !value.IsNumber(v) || value.AsNumber(v) != 7 {
		t.Errorf("result = %v, want number 7", debugVal(st, v))
	}
}

// TestExec_NumericForLoop — 02 §8 求和函数 sum(n) = sum_{i=1}^{n} i*i,验证 FORPREP/FORLOOP。
func TestExec_NumericForLoop(t *testing.T) {
	src := `
local function f(n)
  local s = 0
  for i=1,n do s = s + i*i end
  return s
end
result = f(10)
`
	st := runLua(t, src)
	v, _ := st.tableGet(st.globals, st.makeStringValue("result"))
	want := 0.0
	for i := 1; i <= 10; i++ {
		want += float64(i * i)
	}
	if !value.IsNumber(v) || value.AsNumber(v) != want {
		t.Errorf("result = %v, want %v", debugVal(st, v), want)
	}
}

// TestExec_RecursiveCall — 递归 fib(10),验证 reentry 不爆 Go 栈与多层 RETURN。
func TestExec_RecursiveCall(t *testing.T) {
	src := `
local function fib(n)
  if n < 2 then return n end
  return fib(n-1) + fib(n-2)
end
result = fib(10)
`
	st := runLua(t, src)
	v, _ := st.tableGet(st.globals, st.makeStringValue("result"))
	if !value.IsNumber(v) || value.AsNumber(v) != 55 {
		t.Errorf("fib(10) = %v, want 55", debugVal(st, v))
	}
}

// TestExec_TailCall — 尾递归 1e3 次不爆栈(验证 TAILCALL 复用帧)。
func TestExec_TailCall(t *testing.T) {
	src := `
local function loop(n, acc)
  if n == 0 then return acc end
  return loop(n-1, acc+1)
end
result = loop(1000, 0)
`
	st := runLua(t, src)
	v, _ := st.tableGet(st.globals, st.makeStringValue("result"))
	if !value.IsNumber(v) || value.AsNumber(v) != 1000 {
		t.Errorf("loop(1000,0) = %v, want 1000", debugVal(st, v))
	}
}

// TestExec_WhileLoop — 验证 LT + JMP 回边。
func TestExec_WhileLoop(t *testing.T) {
	src := `
local function count(n)
  local i = 0
  while i < n do i = i + 1 end
  return i
end
result = count(100)
`
	st := runLua(t, src)
	v, _ := st.tableGet(st.globals, st.makeStringValue("result"))
	if !value.IsNumber(v) || value.AsNumber(v) != 100 {
		t.Errorf("count(100) = %v, want 100", debugVal(st, v))
	}
}

// TestExec_ClosureUpvalue — 验证 CLOSURE + 后随伪指令 + GETUPVAL/SETUPVAL。
func TestExec_ClosureUpvalue(t *testing.T) {
	src := `
local function make()
  local x = 10
  local function inc() x = x + 1; return x end
  return inc()
end
result = make()
`
	st := runLua(t, src)
	v, _ := st.tableGet(st.globals, st.makeStringValue("result"))
	if !value.IsNumber(v) || value.AsNumber(v) != 11 {
		t.Errorf("make() = %v, want 11", debugVal(st, v))
	}
}

// TestExec_IfElse — if/elseif/else 的多分支跳转。
func TestExec_IfElse(t *testing.T) {
	src := `
local function pick(a, b)
  if a < b then return -1 end
  if a > b then return 1 end
  return 0
end
r1 = pick(1, 2)
r2 = pick(2, 1)
r3 = pick(3, 3)
`
	st := runLua(t, src)
	for k, want := range map[string]float64{"r1": -1, "r2": 1, "r3": 0} {
		v, _ := st.tableGet(st.globals, st.makeStringValue(k))
		if !value.IsNumber(v) || value.AsNumber(v) != want {
			t.Errorf("%s = %v, want %v", k, debugVal(st, v), want)
		}
	}
}

// makeStringValue intern 一个字符串字面量,返回对应 Value(便于测试查 globals)。
func (st *State) makeStringValue(s string) value.Value {
	ref := st.gc.Intern([]byte(s))
	return value.MakeGC(value.TagString, ref)
}

// debugVal 返回一个易读字符串(测试失败时打印)。
func debugVal(st *State, v value.Value) string {
	if value.IsNumber(v) {
		return formatLuaNumber(value.AsNumber(v))
	}
	switch value.Tag(v) {
	case value.TagNil:
		return "nil"
	case value.TagString:
		return "\"" + string(toStringDebug(st, v)) + "\""
	}
	return typeName(v)
}

func toStringDebug(st *State, v value.Value) []byte {
	if value.Tag(v) == value.TagString {
		b, _ := st.toStringBytes(v)
		return b
	}
	return nil
}
