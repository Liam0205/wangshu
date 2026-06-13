//go:build wangshu_profile

// PB1 采样钩点接入验收(`docs/design/p2-bridge/01-profiling.md` §1.4 + §4):
//   - 回边/入口计数累积非零(三档脚本:数值循环 / while / 函数入口)
//   - profileEnabled=true 切换不改 byte-equal(测试 = 默认 build 已过 ⇒
//     此处只验「profile=on 下结果与脚本预期值一致」,与 default 一致即
//     间接验「开关切换不改 byte-equal」)
//
// 仅 `-tags wangshu_profile` 时编译——profileEnabled=false 下钩点编译期
// 消去,profileTable 永远空,本组测试无意义。
package crescent

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/frontend/compile"
	"github.com/Liam0205/wangshu/internal/frontend/lex"
	"github.com/Liam0205/wangshu/internal/frontend/parse"
	"github.com/Liam0205/wangshu/internal/value"
)

// runLuaWithBridge 像 runLua 但把 bridge 暴露给测试
// (用 wangshu.NewState 一类的包装会让本测试成为 e2e,过重——直接走
// crescent.New)。
func runLuaWithBridge(t *testing.T, src string) *State {
	t.Helper()
	lx := lex.New([]byte(src), "test_profile")
	block, err := parse.Parse(lx, "test_profile")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	mainID, protos, err := compile.Compile(block, "test_profile")
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

// TestProfile_BackEdgeAccumulates 数值循环跑 N 轮 ⇒ 至少有一个回边
// pc 累计 ≈ N 次。
func TestProfile_BackEdgeAccumulates(t *testing.T) {
	src := `
local function f(n)
  local s = 0
  for i=1,n do s = s + i end
  return s
end
result = f(50)
`
	st := runLuaWithBridge(t, src)
	v, _ := st.tableGet(st.globals, st.makeStringValue("result"))
	if !value.IsNumber(v) || value.AsNumber(v) != 1275 {
		t.Fatalf("script result = %v, want 1275", debugVal(st, v))
	}

	innerProto := findProtoByParams(t, st, 1)
	pd := st.bridge.ProfileOf(innerProto)
	maxBack := pd.MaxBackEdge()
	if maxBack < 40 {
		t.Errorf("expected MaxBackEdge ≥ 40 for 50-iter loop, got %d", maxBack)
	}
	if pd.EntryCount < 1 {
		t.Errorf("inner function entered at least once, got entryCount=%d", pd.EntryCount)
	}
}

// findProtoByParams 找到 NumParams 等于 want 且非 main chunk(IsVararg=false)
// 的第一个 Proto——主 chunk 是 vararg,跳过它。
func findProtoByParams(t *testing.T, st *State, want uint8) *bytecode.Proto {
	t.Helper()
	for _, p := range st.protos {
		if p.IsVararg {
			continue
		}
		if p.NumParams == want {
			return p
		}
	}
	t.Fatalf("no Proto with NumParams=%d (vararg=false) found", want)
	return nil
}

// TestProfile_WhileLoopBackEdge 验证 while/repeat 一类 JMP 回边也被计数
// (路线 B 覆盖 §1.3 表里全部回边形态)。
func TestProfile_WhileLoopBackEdge(t *testing.T) {
	src := `
local function f(n)
  local i = 0
  while i < n do i = i + 1 end
  return i
end
result = f(30)
`
	st := runLuaWithBridge(t, src)
	v, _ := st.tableGet(st.globals, st.makeStringValue("result"))
	if !value.IsNumber(v) || value.AsNumber(v) != 30 {
		t.Fatalf("script result = %v, want 30", debugVal(st, v))
	}

	innerProto := findProtoByParams(t, st, 1)
	pd := st.bridge.ProfileOf(innerProto)
	if pd.MaxBackEdge() < 25 {
		t.Errorf("expected MaxBackEdge ≥ 25 for 30-iter while, got %d",
			pd.MaxBackEdge())
	}
}

// TestProfile_EntryCountAccumulates 反复调用同函数 ⇒ entryCount 累积。
//
// 验证 enterLuaFrame 钩点同时覆盖 doCall 与 doTailCall(05 §7.5;
// doTailCall 内部经 enterLuaFrame 因此天然覆盖,无需独立钩点)。
func TestProfile_EntryCountAccumulates(t *testing.T) {
	src := `
local function inner(x) return x*x end
local function caller(n)
  local s = 0
  for i=1,n do s = s + inner(i) end
  return s
end
result = caller(20)
`
	st := runLuaWithBridge(t, src)
	v, _ := st.tableGet(st.globals, st.makeStringValue("result"))
	want := 0.0
	for i := 1; i <= 20; i++ {
		want += float64(i * i)
	}
	if !value.IsNumber(v) || value.AsNumber(v) != want {
		t.Fatalf("script result = %v, want %v", debugVal(st, v), want)
	}

	// inner 是单参数非 vararg 的小函数(`local function inner(x) return x*x end`),
	// 应被调 20 次。
	innerProto := findProtoByParams(t, st, 1)
	pd := st.bridge.ProfileOf(innerProto)
	if pd.EntryCount != 20 {
		t.Errorf("inner entryCount = %d, want 20", pd.EntryCount)
	}
}

// TestProfile_TierGuardBlocksWhenStuck 验证 ProfileData.TierState=TierStuck
// 时 onBackEdge / onEnter 直接 return(01 §4.1 守卫,bridge 包测试已验,
// 此处验 crescent → bridge 接线层面也守住)。
func TestProfile_TierGuardBlocksWhenStuck(t *testing.T) {
	src := `
local function f(n)
  local s = 0
  for i=1,n do s = s + 1 end
  return s
end
result = f(10)
`
	// 第一次执行让计数累积
	st := runLuaWithBridge(t, src)
	innerProto := findProtoByParams(t, st, 1)
	pd := st.bridge.ProfileOf(innerProto)
	before := pd.MaxBackEdge()

	// 把 TierState 强制设到 TierStuck,再跑一遍 ⇒ 计数不应再涨
	pd.TierState = 2 // TierStuck (避免循环 import bridge 包)
	cl := st.loadedCls[0]
	if _, err := st.Call(cl, nil, 0); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	after := pd.MaxBackEdge()
	if after != before {
		t.Errorf("MaxBackEdge changed under TierStuck: before=%d after=%d",
			before, after)
	}
}
