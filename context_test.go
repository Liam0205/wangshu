// State.SetContext/RemoveContext 测试——context cancellation 钩子
// (issue #4:pineapple 接入需要 timeout/cancel 穿透 VM)。
package wangshu_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Liam0205/wangshu"
)

func TestSetContext_TimeoutCancelsLoop(t *testing.T) {
	// issue #4 验收用例:100ms WithTimeout + while-true 死循环 → 100ms 内返回。
	st := wangshu.NewState(wangshu.Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	st.SetContext(ctx)
	defer st.RemoveContext()

	prog, _ := wangshu.Compile([]byte(`while true do end`), "loop")
	start := time.Now()
	_, err := prog.Run(st)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("Run: want error, got nil")
	}
	if !strings.Contains(err.Error(), "context") {
		t.Errorf("err = %q, want contain 'context'", err.Error())
	}
	// 容忍上限:CI 抖动留 1s 余量。下限 50ms 防止过早误杀。
	if elapsed < 50*time.Millisecond {
		t.Errorf("elapsed=%v too short, expect ~100ms", elapsed)
	}
	if elapsed > time.Second {
		t.Errorf("elapsed=%v too long, expect ~100ms", elapsed)
	}
}

func TestSetContext_CancelMidExecution(t *testing.T) {
	// 跨 goroutine 取消:VM 在 G1 跑,G2 50ms 后 cancel,VM 应在
	// ~50ms 内返回 ctx 错误。
	st := wangshu.NewState(wangshu.Options{})
	ctx, cancel := context.WithCancel(context.Background())
	st.SetContext(ctx)
	defer st.RemoveContext()

	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	prog, _ := wangshu.Compile([]byte(`while true do end`), "loop")
	start := time.Now()
	_, err := prog.Run(st)
	elapsed := time.Since(start)

	if err == nil || !strings.Contains(err.Error(), "context") {
		t.Errorf("err = %v", err)
	}
	if elapsed > time.Second {
		t.Errorf("elapsed=%v too long", elapsed)
	}
}

func TestSetContext_NormalScriptUnaffected(t *testing.T) {
	// SetContext 后跑正常脚本:无 ctx 触发应正常返回结果。
	st := wangshu.NewState(wangshu.Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st.SetContext(ctx)
	defer st.RemoveContext()

	prog, _ := wangshu.Compile([]byte(`
local s = 0
for i = 1, 1000 do s = s + i end
return s
`), "sum")
	r, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if r[0].Number() != 500500 {
		t.Errorf("sum = %s", r[0].Display())
	}
}

func TestRemoveContext_TimeoutNoLongerTriggers(t *testing.T) {
	// SetContext → RemoveContext → 即使 ctx 已过期也不应中断
	st := wangshu.NewState(wangshu.Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	st.SetContext(ctx)
	time.Sleep(20 * time.Millisecond) // 确保 ctx 已过期
	st.RemoveContext()

	prog, _ := wangshu.Compile([]byte(`
local s = 0
for i = 1, 100 do s = s + i end
return s
`), "x")
	r, err := prog.Run(st)
	if err != nil {
		t.Fatalf("after Remove: %v", err)
	}
	if r[0].Number() != 5050 {
		t.Errorf("sum = %s", r[0].Display())
	}
}

func TestSetContext_PcallCanCatch(t *testing.T) {
	// ctx cancellation 作为 LuaError 抛出,pcall 能兜住——与 SetStepBudget
	// 同款语义(行为细则 issue 未明确,按对位 gopher-lua 处理:pcall 可拦)。
	st := wangshu.NewState(wangshu.Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	st.SetContext(ctx)
	defer st.RemoveContext()

	prog, _ := wangshu.Compile([]byte(`
local ok, err = pcall(function() while true do end end)
return ok, err
`), "pc")
	r, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if r[0].Bool() != false {
		t.Errorf("pcall ok=%s, want false", r[0].Display())
	}
	if !strings.Contains(r[1].Str(), "context") {
		t.Errorf("pcall err = %q", r[1].Str())
	}
}

func TestSetContext_ConcurrentStates(t *testing.T) {
	// 同一进程多个 State 各持自己的 ctx,互不干扰。-race 同步验证。
	const N = 4
	var wg sync.WaitGroup
	errs := make([]error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			st := wangshu.NewState(wangshu.Options{})
			ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
			defer cancel()
			st.SetContext(ctx)
			prog, _ := wangshu.Compile([]byte(`while true do end`), "loop")
			_, errs[idx] = prog.Run(st)
		}(i)
	}
	wg.Wait()
	for i, e := range errs {
		if e == nil || !strings.Contains(e.Error(), "context") {
			t.Errorf("worker %d err = %v", i, e)
		}
	}
}

func TestRemoveContext_NoopWithoutSet(t *testing.T) {
	// 未 SetContext 直接 RemoveContext 应无副作用;反复 Remove 也无副作用。
	st := wangshu.NewState(wangshu.Options{})
	st.RemoveContext()
	st.RemoveContext()
	prog, _ := wangshu.Compile([]byte(`return 1+1`), "x")
	r, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if r[0].Number() != 2 {
		t.Errorf("got %s", r[0].Display())
	}
}

func TestSetContext_ErrPropagatesUnderlying(t *testing.T) {
	// 自定义 ctx 错误应可见(err.Error 包含原始错误文本)
	ctx, cancel := context.WithCancelCause(context.Background())
	st := wangshu.NewState(wangshu.Options{})
	st.SetContext(ctx)
	defer st.RemoveContext()
	customErr := errors.New("upstream said stop")
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel(customErr)
	}()
	prog, _ := wangshu.Compile([]byte(`while true do end`), "loop")
	_, err := prog.Run(st)
	if err == nil {
		t.Fatalf("want err")
	}
	// ctx.Err() 返回的是 context.Canceled(原始错误经 Cause(ctx) 取);
	// 我们包装的是 ctx.Err() 而非 Cause,所以文本应含 "context canceled"。
	if !strings.Contains(err.Error(), "context") {
		t.Errorf("err = %q", err.Error())
	}
}
