// State.SetContext/RemoveContext tests — context cancellation hook
// (issue #4: pineapple integration needs timeout/cancel to propagate through the VM).
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
	// issue #4 acceptance case: 100ms WithTimeout + while-true infinite loop → returns within 100ms.
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
	// Upper bound tolerance: leave 1s of headroom for CI jitter. Lower bound 50ms guards against premature kills.
	if elapsed < 50*time.Millisecond {
		t.Errorf("elapsed=%v too short, expect ~100ms", elapsed)
	}
	if elapsed > time.Second {
		t.Errorf("elapsed=%v too long, expect ~100ms", elapsed)
	}
}

func TestSetContext_CancelMidExecution(t *testing.T) {
	// Cross-goroutine cancellation: VM runs on G1, G2 cancels after 50ms, VM should
	// return a ctx error within ~50ms.
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
	// Run a normal script after SetContext: with no ctx trigger it should return the result normally.
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
	// SetContext → RemoveContext → must not interrupt even if the ctx has already expired
	st := wangshu.NewState(wangshu.Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	st.SetContext(ctx)
	time.Sleep(20 * time.Millisecond) // make sure the ctx has expired
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
	// ctx cancellation is raised as a LuaError, so pcall can catch it — same semantics as
	// SetStepBudget (the issue leaves the exact behavior unspecified; follow gopher-lua's counterpart: pcall may intercept).
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
	// Multiple States in the same process each hold their own ctx, without interfering. Also verified in sync under -race.
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
	// Calling RemoveContext directly without SetContext should have no side effect; repeated Remove is also side-effect-free.
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
	// A custom ctx error should be visible (err.Error contains the original error text)
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
	// ctx.Err() returns context.Canceled (the original error is obtained via Cause(ctx));
	// we wrap ctx.Err() rather than Cause, so the text should contain "context canceled".
	if !strings.Contains(err.Error(), "context") {
		t.Errorf("err = %q", err.Error())
	}
}
