// M11 metatable + pcall end-to-end tests.
package wangshu_test

import (
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
)

func TestMeta_IndexTable(t *testing.T) {
	got := runOne(t, `
local base = { greeting = "hello" }
local derived = setmetatable({}, { __index = base })
return derived.greeting
`)
	if !got.IsString() || got.String_() != "hello" {
		t.Errorf("got %v, want 'hello'", got.GoString())
	}
}

func TestMeta_IndexFunction(t *testing.T) {
	got := runOne(t, `
local t = setmetatable({}, { __index = function(tbl, key) return key .. "!" end })
return t.boom
`)
	if !got.IsString() || got.String_() != "boom!" {
		t.Errorf("got %v, want 'boom!'", got.GoString())
	}
}

func TestMeta_NewIndex(t *testing.T) {
	got := runOne(t, `
local log = {}
local t = setmetatable({}, { __newindex = function(tbl, k, v) rawset(log, k, v) end })
t.x = 42
return rawget(log, "x")
`)
	if !got.IsNumber() || got.Number() != 42 {
		t.Errorf("got %v, want 42", got.GoString())
	}
}

func TestMeta_Add(t *testing.T) {
	got := runOne(t, `
local mt = { __add = function(a, b) return a.v + b.v end }
local x = setmetatable({ v = 3 }, mt)
local y = setmetatable({ v = 4 }, mt)
return x + y
`)
	if !got.IsNumber() || got.Number() != 7 {
		t.Errorf("got %v, want 7", got.GoString())
	}
}

func TestMeta_GetMetatable(t *testing.T) {
	got := runOne(t, `
local mt = {}
local t = setmetatable({}, mt)
return getmetatable(t) == mt
`)
	if !got.IsBool() || !got.Bool() {
		t.Errorf("got %v, want true", got.GoString())
	}
}

func TestPcall_Success(t *testing.T) {
	got := runOne(t, `
local ok, v = pcall(function() return 99 end)
if ok then return v end
return -1
`)
	if !got.IsNumber() || got.Number() != 99 {
		t.Errorf("got %v, want 99", got.GoString())
	}
}

func TestPcall_CatchError(t *testing.T) {
	got := runOne(t, `
local ok, err = pcall(function() error("kaboom") end)
if ok then return "no-error" end
return err
`)
	// 5.1 语义:error(string) 自动加 chunkname:line: 前缀
	if !got.IsString() || !strings.HasSuffix(got.String_(), ": kaboom") {
		t.Errorf("got %v, want suffix ': kaboom'", got.GoString())
	}
}

func TestPcall_CatchRuntimeError(t *testing.T) {
	got := runOne(t, `
local ok, err = pcall(function()
  local x
  return x + 1
end)
return ok
`)
	if !got.IsBool() || got.Bool() {
		t.Errorf("got %v, want false", got.GoString())
	}
}

func TestPcall_StateContinues(t *testing.T) {
	prog, err := wangshu.Compile([]byte(`
local ok = pcall(function() error("x") end)
return 7 + 1
`), "cont")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	results, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if results[0].Number() != 8 {
		t.Errorf("got %v, want 8", results[0].GoString())
	}
}
