// Public API end-to-end tests — verify Compile / Program.Run / Value bridging (M13).
package wangshu_test

import (
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
)

func TestCompileAndRun_Simple(t *testing.T) {
	prog, err := wangshu.Compile([]byte("return 1+2"), "snippet")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	results, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results len=%d, want 1", len(results))
	}
	if !results[0].IsNumber() || results[0].Number() != 3 {
		t.Errorf("result = %v, want 3", results[0].Display())
	}
}

func TestCompileAndRun_Args(t *testing.T) {
	prog, err := wangshu.Compile([]byte(`
local a, b = ...
return a * b + 1
`), "argchunk")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	results, err := prog.Run(st, wangshu.Number(6), wangshu.Number(7))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(results) != 1 || !results[0].IsNumber() || results[0].Number() != 43 {
		t.Errorf("result = %s", results[0].Display())
	}
}

func TestCompileAndRun_StringConcat(t *testing.T) {
	prog, err := wangshu.Compile([]byte(`
local s = "hello, "
return s .. "world"
`), "concat")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	results, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(results) != 1 || !results[0].IsString() || results[0].Str() != "hello, world" {
		t.Errorf("result = %v", results[0].Display())
	}
}

func TestCompileAndRun_LoopReturn(t *testing.T) {
	prog, err := wangshu.Compile([]byte(`
local function f(n)
  local s = 0
  for i = 1, n do s = s + i*i end
  return s
end
return f(10)
`), "loop")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	results, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	want := 0.0
	for i := 1; i <= 10; i++ {
		want += float64(i * i)
	}
	if results[0].Number() != want {
		t.Errorf("result = %v, want %v", results[0].Display(), want)
	}
}

func TestCompileError(t *testing.T) {
	_, err := wangshu.Compile([]byte("function broken("), "bad")
	if err == nil {
		t.Fatalf("expected compile error")
	}
}

func TestRuntimeError(t *testing.T) {
	prog, err := wangshu.Compile([]byte(`
local x
return x + 1
`), "rt")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	_, err = prog.Run(st)
	if err == nil {
		t.Fatalf("expected runtime error")
	}
	if !strings.Contains(err.Error(), "arithmetic") {
		t.Errorf("error = %q, want substring 'arithmetic'", err.Error())
	}
}

func TestProgram_ReusableAcrossStates(t *testing.T) {
	prog, err := wangshu.Compile([]byte("return 42"), "shared")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	for i := 0; i < 3; i++ {
		st := wangshu.NewState(wangshu.Options{})
		r, err := prog.Run(st)
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		if r[0].Number() != 42 {
			t.Errorf("run %d: %v", i, r[0].Display())
		}
	}
}
