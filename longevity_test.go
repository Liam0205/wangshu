// Longevity tests — 长稳承诺验证(06 §2 freelist 复用 + 05 §7.4 调用深度上限)。
//
// 三类断言:
//  1. arena 占用有界:同一 State 反复 Run 分配密集脚本,稳态下 freelist 循环
//     复用,arena cap 不随轮次无限 grow。
//  2. Lua 深递归报 "stack overflow"(可被 pcall 捕获),不打爆 Go 栈。
//  3. host→Lua 交替重入报 "C stack overflow",同样可恢复。
package wangshu_test

import (
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
)

// TestLongevity_ArenaBounded 同一 State 重复跑分配密集脚本,断言内存有界。
//
// 脚本每轮制造 ~2000 个临时对象(表/字符串/闭包)且全部不外逃;若 sweep
// 没把死对象归还 freelist(或归还后分配不复用),arena 会随轮次线性涨。
func TestLongevity_ArenaBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("longevity test skipped in -short")
	}
	src := `
local acc = 0
for i = 1, 200 do
  local t = { i, i * 2, s = "key" .. i }
  local f = function() return t[1] end
  acc = acc + f() + #("payload" .. i)
end
return acc`
	prog, err := wangshu.Compile([]byte(src), "longevity")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})

	// 预热 50 轮让 arena/threshold 到稳态,再记基线。
	for i := 0; i < 50; i++ {
		if _, err := prog.Run(st); err != nil {
			t.Fatalf("warmup run %d: %v", i, err)
		}
	}
	base := st.GCCountKB()
	for i := 0; i < 2000; i++ {
		if _, err := prog.Run(st); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	after := st.GCCountKB()
	// bump 指针只在 freelist 落空时前进;稳态下应基本不动。给 4x 余量
	// (rehash 尺寸阶梯、intern 表增长等良性余地)。
	if after > base*4 {
		t.Errorf("arena usage not bounded: base=%.1fKB after 2000 runs=%.1fKB", base, after)
	}
}

// TestLongevity_DeepRecursionOverflow 深递归必须报 Lua 语义的 stack overflow。
func TestLongevity_DeepRecursionOverflow(t *testing.T) {
	src := `
local function f(n) return 1 + f(n + 1) end
local ok, err = pcall(f, 1)
return tostring(ok), tostring(err)`
	prog, err := wangshu.Compile([]byte(src), "deeprec")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	results, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if results[0].String_() != "false" {
		t.Errorf("pcall ok = %s, want false", results[0].GoString())
	}
	if !strings.Contains(results[1].String_(), "stack overflow") {
		t.Errorf("err = %q, want contains 'stack overflow'", results[1].String_())
	}
}

// TestLongevity_DeepRecursionWithinLimit 上限内的深递归(含尾调用消除)不受影响。
func TestLongevity_DeepRecursionWithinLimit(t *testing.T) {
	src := `
local function f(n) if n <= 0 then return 0 end return 1 + f(n - 1) end
local function loop(n, acc) if n == 0 then return acc end return loop(n - 1, acc + 1) end
return f(19000), loop(1000000, 0)`
	prog, err := wangshu.Compile([]byte(src), "deepok")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	results, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if results[0].Number() != 19000 {
		t.Errorf("f(19000) = %s, want 19000", results[0].GoString())
	}
	if results[1].Number() != 1000000 {
		t.Errorf("tailcall loop = %s, want 1000000 (proper tail call must not consume depth)", results[1].GoString())
	}
}

// TestLongevity_CStackOverflow host→Lua 交替重入(pcall 自递归)必须被
// "C stack overflow" 拦下而非 Go 栈 fatal。
func TestLongevity_CStackOverflow(t *testing.T) {
	src := `
local f
f = function() return pcall(f) end
local ok, e = f()
return tostring(ok), tostring(e)`
	prog, err := wangshu.Compile([]byte(src), "cstack")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	results, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// 最深一层 pcall 返回 (false, "C stack overflow"),其外层 pcall 全部
	// 成功返回 true —— 顶层观察 ok=true(与官方 5.1 行为一致)。
	if results[0].String_() != "true" {
		t.Errorf("top ok = %s, want true", results[0].GoString())
	}
}

// TestLongevity_StateReuse 同一 State 上多 Program 反复装载执行,loaded 缓存
// 生效(IC/intern 跨 Run 保留)且互不污染。
func TestLongevity_StateReuse(t *testing.T) {
	progA, err := wangshu.Compile([]byte(`return 1 + 1`), "a")
	if err != nil {
		t.Fatal(err)
	}
	progB, err := wangshu.Compile([]byte(`local t = { x = 7 } return t.x`), "b")
	if err != nil {
		t.Fatal(err)
	}
	st := wangshu.NewState(wangshu.Options{})
	for i := 0; i < 500; i++ {
		ra, err := progA.Run(st)
		if err != nil {
			t.Fatalf("progA run %d: %v", i, err)
		}
		if ra[0].Number() != 2 {
			t.Fatalf("progA = %s", ra[0].GoString())
		}
		rb, err := progB.Run(st)
		if err != nil {
			t.Fatalf("progB run %d: %v", i, err)
		}
		if rb[0].Number() != 7 {
			t.Fatalf("progB = %s", rb[0].GoString())
		}
	}
}
