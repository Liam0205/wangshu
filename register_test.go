// Register / RegisterModule 测试——Go 函数注册到 Lua 全局/模块表(11 §10)。
package wangshu_test

import (
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
)

type hostErr struct{ msg string }

func (e *hostErr) Error() string { return e.msg }

func TestRegister_HostFnCalledByLua(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	st.Register("greet", func(_ *wangshu.State, args []wangshu.Value) ([]wangshu.Value, error) {
		if len(args) != 1 || !args[0].IsString() {
			return nil, nil
		}
		return []wangshu.Value{wangshu.String("hello, " + args[0].Str())}, nil
	})
	prog, _ := wangshu.Compile([]byte(`return greet("world")`), "x")
	r, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(r) != 1 || r[0].Str() != "hello, world" {
		t.Errorf("r = %s", r[0].Display())
	}
}

func TestRegister_HostFnErrorCapturedByPCall(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	st.Register("boom", func(_ *wangshu.State, _ []wangshu.Value) ([]wangshu.Value, error) {
		return nil, &hostErr{"explode"}
	})
	prog, _ := wangshu.Compile([]byte(`
local ok, err = pcall(boom)
return ok, err
`), "x")
	r, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(r) != 2 || r[0].Bool() != false || !strings.Contains(r[1].Str(), "explode") {
		t.Errorf("ok=%s err=%s", r[0].Display(), r[1].Display())
	}
}

func TestCall_HostClosureRejected(t *testing.T) {
	// 当前裁口:Register 注册的 host fn 只能 Lua 内调,Go 端经 GetGlobal
	// 取出 + state.Call 直接调用应被清晰拒绝(internal callHost 入口
	// 未对接 Go 端临时栈帧)。本测试保护这条边界 — 未来真做了支持时,
	// 错误措辞同步移除即可。
	st := wangshu.NewState(wangshu.Options{})
	st.Register("noop", func(_ *wangshu.State, _ []wangshu.Value) ([]wangshu.Value, error) {
		return nil, nil
	})
	fn := st.GetGlobal("noop")
	defer fn.Release()
	if !fn.IsFunction() {
		t.Fatalf("noop not a function: %s", fn.Display())
	}
	_, err := st.Call(fn)
	if err == nil {
		t.Fatalf("Call(host closure): want error, got nil")
	}
	if !strings.Contains(err.Error(), "host closure") {
		t.Errorf("err = %q, want contain 'host closure'", err.Error())
	}
}

func TestRegisterModule_NamespaceLookup(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	st.RegisterModule("math2", map[string]wangshu.HostFn{
		"sq": func(_ *wangshu.State, args []wangshu.Value) ([]wangshu.Value, error) {
			x := args[0].Number()
			return []wangshu.Value{wangshu.Number(x * x)}, nil
		},
		"id": func(_ *wangshu.State, args []wangshu.Value) ([]wangshu.Value, error) {
			return args, nil
		},
	})
	prog, _ := wangshu.Compile([]byte(`return math2.sq(7), math2.id("ok")`), "x")
	r, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(r) != 2 || r[0].Number() != 49 || r[1].Str() != "ok" {
		t.Errorf("r = %s / %s", r[0].Display(), r[1].Display())
	}
}
