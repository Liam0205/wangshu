//go:build wangshu_p4 && wangshu_profile

package crescent

import (
	"runtime"
	"runtime/debug"
	"sync"
	"testing"

	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/value"
)

// gibbous_p4_r14_abi_test.go —— PJ4/PJ5 mmap 段 R14 ABI 违约修复后验
// (承 PR #26 外部审查 5b28c8a + trampoline_spec_amd64.s PUSH/POP R14)。
//
// 外部审查警告:R14 是 Go amd64 ABIInternal g 寄存器,PJ4 IC 模板 + PJ5
// SELF spec template 字节级 emit 把 arena base 装 R14 后段尾 RET 直接污染
// Go G,生产负载 morestack/抢占/同步取 g 时 SEGV。修复方案 trampoline
// PUSH/POP R14 救济:段瞬时覆写,段尾 POP 恢复 Go G,Go runtime 后续操作
// 见正确 G 值。
//
// 本测试**直接触发** Go runtime 取 g 的路径(morestack / GC / 抢占)与
// spec template 路径混跑,显性化任何 R14 残留导致的 G 损坏:
//   1. TestPJ4PJ5_R14ABI_GCStress:spec template 路径反复跑 + 强制 GC,
//      验 GC mark/sweep 期间取 g 正确(若 R14 残留 GC 即 SEGV)
//   2. TestPJ4PJ5_R14ABI_ConcurrentGC:多 goroutine 并发跑 spec template
//      + GC stress,验 Go runtime stop-the-world 取所有 g 时 g 正确
//   3. TestPJ4PJ5_R14ABI_DeepStack:深递归触发 morestack 拷栈,验 morestack
//      期间取 g 正确

// TestPJ4PJ5_R14ABI_GCStress mmap 段反复跑 + 强制 GC + finalize,验
// 修复后 R14 救济成功(R14 残留则 GC 取 g SEGV)。
func TestPJ4PJ5_R14ABI_GCStress(t *testing.T) {
	src := `
local o = { m = function(self) return 42 end }
local function caller(t) return t:m() end
local sum = 0
for i = 1, 100 do sum = sum + caller(o) end  -- warmup
sum = sum + caller(o)
return sum`
	st, mainCl := loadFnP4(t, src)

	// warmup phase 1(填 IC NodeHit + FBSelfMono feedback)
	if _, err := st.Call(value.GCRefOf(mainCl), nil, 1); err != nil {
		t.Fatalf("warmup: %v", err)
	}
	st.bridge.SetForceAllPromote(true)

	// 跑 spec template + 强制 GC 交替(50 轮)
	for i := 0; i < 50; i++ {
		rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
		if err != nil {
			t.Fatalf("iter %d run: %v", i, err)
		}
		if got := value.AsNumber(value.Value(rets[0])); got != 101*42 {
			t.Errorf("iter %d result = %v, want %d", i, got, 101*42)
		}
		// 强制 GC + finalize(GC mark/sweep 期间取所有 g,若 R14 残留即 SEGV)
		runtime.GC()
		debug.FreeOSMemory()
	}
}

// TestPJ4PJ5_R14ABI_ConcurrentGC 多 goroutine 并发跑 spec template + GC
// stress,验 Go runtime stop-the-world 取所有 g 时 g 正确。
//
// **修正(PR #26 评论 d8a5899..83f0b2e 重要建议)**:
//   - callersWG 单独追 caller goroutine 完成,GC stresser 在 caller 全程
//     运行,显著扩大「并发 GC + stop-the-world 取 g」重叠窗口
//   - loadFnP4 内调 t.Fatalf 仅安全在主 goroutine,所有 State 预先在主
//     线程构造,goroutine 内只跑 st.Call(返 error 路径)
func TestPJ4PJ5_R14ABI_ConcurrentGC(t *testing.T) {
	const goroutines = 8
	const itersPerGoroutine = 30

	src := `
local mt = { m = function(self, k) return k+1 end }
local function caller(_, t, v) return t:m(v) end
local sum = 0
for i = 1, 50 do sum = sum + caller(nil, mt, i) end  -- warmup
sum = sum + caller(nil, mt, 100)
return sum`

	// 主线程预编译 N 个 State(loadFnP4 内含 t.Fatalf,只在主 goroutine 安全)
	type stateBundle struct {
		st     *State
		mainCl arena.GCRef
	}
	bundles := make([]stateBundle, goroutines)
	for g := 0; g < goroutines; g++ {
		st, mainCl := loadFnP4(t, src)
		// warmup phase 1 + force-all(主线程顺序跑,避免 goroutine 内 t.Fatalf)
		if _, err := st.Call(value.GCRefOf(mainCl), nil, 1); err != nil {
			t.Fatalf("bundle %d warmup: %v", g, err)
		}
		st.bridge.SetForceAllPromote(true)
		bundles[g] = stateBundle{st: st, mainCl: value.GCRefOf(mainCl)}
	}

	// callers wg + GC stresser 独立 goroutine
	var callersWG sync.WaitGroup
	callersWG.Add(goroutines)

	stopGC := make(chan struct{})
	var gcWG sync.WaitGroup
	gcWG.Add(1)
	go func() {
		defer gcWG.Done()
		for {
			select {
			case <-stopGC:
				return
			default:
				runtime.GC()
				runtime.Gosched()
			}
		}
	}()

	// callers:并发跑 spec template
	for g := 0; g < goroutines; g++ {
		go func(idx int) {
			defer callersWG.Done()
			b := bundles[idx]
			for i := 0; i < itersPerGoroutine; i++ {
				if _, err := b.st.Call(b.mainCl, nil, 1); err != nil {
					t.Errorf("goroutine %d iter %d: %v", idx, i, err)
					return
				}
			}
		}(g)
	}

	// 等所有 caller 完成,然后停 GC stresser(承评论修正:
	// caller 全程 GC stress 显性化「并发 GC + stop-the-world 取 g」)
	callersWG.Wait()
	close(stopGC)
	gcWG.Wait()
}

// TestPJ4PJ5_R14ABI_DeepStack 深递归(20 层)触发 morestack 拷栈,验
// morestack 期间取 g 正确。
//
// **morestack 触发条件**:Go goroutine 默认栈 8KB,函数 frame 占用足够大
// 时栈不足触发 morestack 拷大栈;morestack 路径用 r14=g 寻址 g 字段。若
// spec template 段后 R14 残留垃圾值,morestack 取 g 时 SEGV / 拷错栈。
//
// 本测试用 Lua 递归调用 + 大栈帧 caller 触发 Go 端 morestack:
//   - lua function `recurse(n)` 自调 20 层,每层调 `t:m()` spec template
//   - 20 层 Lua 调用栈 + 各层 host helper / executeFrom 栈,触发 Go morestack
func TestPJ4PJ5_R14ABI_DeepStack(t *testing.T) {
	src := `
local o = { m = function(self) return 1 end }
local function recurse(n)
  if n <= 0 then return 0 end
  return o:m() + recurse(n - 1)
end
local result = recurse(20)  -- 20 层递归 + 每层 SELF spec template
return result`
	st, mainCl := loadFnP4(t, src)

	if _, err := st.Call(value.GCRefOf(mainCl), nil, 1); err != nil {
		t.Fatalf("warmup: %v", err)
	}
	st.bridge.SetForceAllPromote(true)

	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("deep recursion: %v", err)
	}
	if got := value.AsNumber(value.Value(rets[0])); got != 20 {
		t.Errorf("recurse(20) = %v, want 20", got)
	}
}
