// Options.HideFileLoaders 测试——严格沙箱模式 gopher-lua 对位
// (issue #3:loader 三件套 + load 从 globals 刮除,脚本调用 fatal)。
package wangshu_test

import (
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
)

func TestHideFileLoaders_LoadfileNilCall(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{HideFileLoaders: true})
	// loader 三件套 + load 应都被刮除为 nil
	for _, name := range []string{"loadfile", "dofile", "loadstring", "load"} {
		if v := st.GetGlobal(name); !v.IsNil() {
			t.Errorf("global %q = %s, want nil", name, v.Display())
		}
	}
	// 脚本调用 → "attempt to call a nil value"
	prog, _ := wangshu.Compile([]byte(`return loadfile("x.lua")`), "x")
	_, err := prog.Run(st)
	if err == nil {
		t.Fatalf("loadfile call: want error, got nil")
	}
	if !strings.Contains(err.Error(), "attempt to call global 'loadfile' (a nil value)") {
		t.Errorf("err = %q, want fatal nil call", err.Error())
	}
}

func TestHideFileLoaders_DofileFatal(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{HideFileLoaders: true})
	prog, _ := wangshu.Compile([]byte(`return dofile("x.lua")`), "x")
	_, err := prog.Run(st)
	if err == nil || !strings.Contains(err.Error(), "attempt to call global 'dofile' (a nil value)") {
		t.Errorf("err = %v", err)
	}
}

func TestHideFileLoaders_LoadstringFatal(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{HideFileLoaders: true})
	prog, _ := wangshu.Compile([]byte(`return loadstring("return 1")`), "x")
	_, err := prog.Run(st)
	if err == nil || !strings.Contains(err.Error(), "attempt to call global 'loadstring' (a nil value)") {
		t.Errorf("err = %v", err)
	}
}

func TestHideFileLoaders_DefaultPreservesPucBehavior(t *testing.T) {
	// 默认 HideFileLoaders=false:loadfile 仍可调,返回 (nil, errmsg)
	// (PUC 5.1.5 对位)——pineapple 之外的宿主依赖此行为对拍 oracle。
	st := wangshu.NewState(wangshu.Options{})
	prog, _ := wangshu.Compile([]byte(`
local f, err = loadfile("x.lua")
return f, err
`), "x")
	r, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(r) != 2 || !r[0].IsNil() {
		t.Errorf("loadfile r[0] = %s, want nil", r[0].Display())
	}
	if !r[1].IsString() || !strings.Contains(r[1].Str(), "loadfile") {
		t.Errorf("loadfile r[1] = %q, want errmsg", r[1].Str())
	}
}

func TestHideFileLoaders_AllowFileLoadConflictPanics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("AllowFileLoad+HideFileLoaders: want panic, got none")
		}
		msg, ok := r.(string)
		if !ok || !strings.Contains(msg, "mutually exclusive") {
			t.Errorf("panic = %v, want 'mutually exclusive'", r)
		}
	}()
	_ = wangshu.NewState(wangshu.Options{AllowFileLoad: true, HideFileLoaders: true})
}

func TestHideFileLoaders_OtherStdlibIntact(t *testing.T) {
	// HideFileLoaders 只针对 loader 三件套 + load,不影响其它 stdlib。
	st := wangshu.NewState(wangshu.Options{HideFileLoaders: true})
	prog, _ := wangshu.Compile([]byte(`
return type(math.pi), string.upper("ab"), tonumber("42"), pcall(function() error("x") end)
`), "x")
	r, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(r) < 4 {
		t.Fatalf("results = %d, want >= 4", len(r))
	}
	if r[0].Str() != "number" {
		t.Errorf("type(math.pi) = %s", r[0].Display())
	}
	if r[1].Str() != "AB" {
		t.Errorf("string.upper = %s", r[1].Display())
	}
	if r[2].Number() != 42 {
		t.Errorf("tonumber = %s", r[2].Display())
	}
	if r[3].Bool() != false {
		t.Errorf("pcall ok = %s, want false", r[3].Display())
	}
}
