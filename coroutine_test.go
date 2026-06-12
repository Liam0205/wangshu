// Coroutine end-to-end tests(08 路线 B 验收)。
package wangshu_test

import (
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
)

func runMulti(t *testing.T, src string) []wangshu.Value {
	t.Helper()
	prog, err := wangshu.Compile([]byte(src), "cotest")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	results, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return results
}

func TestCo_BasicYieldResume(t *testing.T) {
	r := runMulti(t, `
local co = coroutine.create(function(a, b)
  local c = coroutine.yield(a + b)
  return c * 2
end)
local ok1, sum = coroutine.resume(co, 3, 4)
local ok2, double = coroutine.resume(co, 10)
return tostring(ok1), sum, tostring(ok2), double, coroutine.status(co)`)
	want := []string{"true", "7", "true", "20", "dead"}
	for i, w := range want {
		if r[i].Display() != w {
			t.Errorf("r[%d] = %q, want %q", i, r[i].Display(), w)
		}
	}
}

func TestCo_MultiYieldLoop(t *testing.T) {
	r := runMulti(t, `
local co = coroutine.create(function()
  for i = 1, 3 do coroutine.yield(i) end
  return "done"
end)
local out = ""
for i = 1, 4 do
  local ok, v = coroutine.resume(co)
  out = out .. tostring(v) .. ";"
end
return out, coroutine.status(co)`)
	if r[0].Str() != "1;2;3;done;" || r[1].Str() != "dead" {
		t.Errorf("got %q %q", r[0].Display(), r[1].Display())
	}
}

func TestCo_Wrap(t *testing.T) {
	r := runMulti(t, `
local gen = coroutine.wrap(function()
  coroutine.yield(1)
  coroutine.yield(2)
  return 3
end)
return gen() + gen() + gen()`)
	if r[0].Number() != 6 {
		t.Errorf("got %v, want 6", r[0].Display())
	}
}

func TestCo_ErrorInsideBecomesFalse(t *testing.T) {
	r := runMulti(t, `
local co = coroutine.create(function() error("inside") end)
local ok, err = coroutine.resume(co)
return tostring(ok), err, coroutine.status(co)`)
	// error(string) 自动加 chunkname:line: 前缀(5.1)
	if r[0].Str() != "false" || !strings.HasSuffix(r[1].Str(), ": inside") || r[2].Str() != "dead" {
		t.Errorf("got %q %q %q", r[0].Display(), r[1].Display(), r[2].Display())
	}
}

func TestCo_ResumeDead(t *testing.T) {
	r := runMulti(t, `
local co = coroutine.create(function() return 1 end)
coroutine.resume(co)
local ok, err = coroutine.resume(co)
return tostring(ok), err`)
	if r[0].Str() != "false" {
		t.Errorf("ok = %q, want false", r[0].Display())
	}
}

func TestCo_TypeIsThread(t *testing.T) {
	r := runMulti(t, `
local co = coroutine.create(function() end)
return type(co)`)
	if r[0].Str() != "thread" {
		t.Errorf("type = %q, want thread", r[0].Display())
	}
}

func TestCo_NestedCallYield(t *testing.T) {
	r := runMulti(t, `
local function inner() coroutine.yield("deep") end
local co = coroutine.create(function() inner(); return "after" end)
local ok1, v1 = coroutine.resume(co)
local ok2, v2 = coroutine.resume(co)
return v1, v2`)
	if r[0].Str() != "deep" || r[1].Str() != "after" {
		t.Errorf("got %q %q", r[0].Display(), r[1].Display())
	}
}

func TestCo_CrossThreadUpvalue(t *testing.T) {
	r := runMulti(t, `
local x = 100
local co = coroutine.create(function()
  x = x + 1
  coroutine.yield(x)
  x = x + 1
  return x
end)
local _, a = coroutine.resume(co)
local _, b = coroutine.resume(co)
return a, b, x`)
	if r[0].Number() != 101 || r[1].Number() != 102 || r[2].Number() != 102 {
		t.Errorf("got %v %v %v", r[0].Display(), r[1].Display(), r[2].Display())
	}
}

func TestCo_YieldOutsideCoroutine(t *testing.T) {
	prog, err := wangshu.Compile([]byte(`coroutine.yield(1)`), "bad")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	_, err = prog.Run(st)
	if err == nil {
		t.Fatalf("expected error for yield outside coroutine")
	}
}

func TestCo_StatusRunningInside(t *testing.T) {
	r := runMulti(t, `
local co
co = coroutine.create(function()
  return coroutine.status(co)
end)
local _, s = coroutine.resume(co)
return s`)
	if r[0].Str() != "running" {
		t.Errorf("status inside = %q, want running", r[0].Display())
	}
}
