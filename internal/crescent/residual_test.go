// 残余 0% 函数的定向测试(覆盖率审计第三轮)。
package crescent

import (
	"strings"
	"testing"

	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// TestErrName_GlobalDescription:全局函数误调用 → "global 'f'" 描述
// (覆盖 describeReg 的 GETGLOBAL 分支与 constStringAt)。
func TestErrName_GlobalDescription(t *testing.T) {
	st := New()
	prog := mustCompile(t, []byte(`undefined_global_fn()`))
	cl := st.LoadProgram(prog.mainID, prog.protos)
	_, err := st.Call(cl, nil, 0)
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "global 'undefined_global_fn'") {
		t.Errorf("error %q should contain global name description", err.Error())
	}
}

// TestErrName_FieldDescription:表字段误调用 → "field 'k'" 描述。
func TestErrName_FieldDescription(t *testing.T) {
	st := New()
	prog := mustCompile(t, []byte(`local t = {}; t.missing_fn()`))
	cl := st.LoadProgram(prog.mainID, prog.protos)
	_, err := st.Call(cl, nil, 0)
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "field 'missing_fn'") {
		t.Errorf("error %q should contain field name description", err.Error())
	}
}

// TestErrName_MethodDescription:方法误调用 → "method 'm'" 描述。
func TestErrName_MethodDescription(t *testing.T) {
	st := New()
	prog := mustCompile(t, []byte(`local t = {}; t:missing_method()`))
	cl := st.LoadProgram(prog.mainID, prog.protos)
	_, err := st.Call(cl, nil, 0)
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "method 'missing_method'") {
		t.Errorf("error %q should contain method name description", err.Error())
	}
}

// TestTraceback_Exposed:State.Traceback 在错误中与独立调用两种形态。
func TestTraceback_Exposed(t *testing.T) {
	st := New()
	prog := mustCompile(t, []byte(`
local function deep() error("boom", 0) end
local function mid() deep() end
mid()`))
	cl := st.LoadProgram(prog.mainID, prog.protos)
	_, err := st.Call(cl, nil, 0)
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "stack traceback:") {
		t.Errorf("top-level error should carry traceback, got %q", err.Error())
	}
	// 静态调用(无运行线程)
	if tb := st.Traceback(); !strings.HasPrefix(tb, "stack traceback:") {
		t.Errorf("standalone Traceback() = %q", tb)
	}
}

// TestRawTable_DeleteExistingKey:置 nil 删除已有哈希键(覆盖 nodeSetKV)。
func TestRawTable_DeleteExistingKey(t *testing.T) {
	st := New()
	tbl := st.allocTable(0, 8)
	key := value.MakeGC(value.TagString, st.gc.Intern([]byte("k")))
	if e := st.rawSet(tbl, key, value.NumberValue(1)); e != nil {
		t.Fatalf("set: %v", e)
	}
	if v := st.rawGet(tbl, key); v == value.Nil {
		t.Fatalf("key should exist before delete")
	}
	if e := st.rawSet(tbl, key, value.Nil); e != nil {
		t.Fatalf("delete: %v", e)
	}
	if v := st.rawGet(tbl, key); v != value.Nil {
		t.Errorf("key should be gone after setting nil")
	}
	// 删除不存在的键 = no-op
	other := value.MakeGC(value.TagString, st.gc.Intern([]byte("nokey")))
	if e := st.rawSet(tbl, other, value.Nil); e != nil {
		t.Errorf("deleting absent key should be no-op, got %v", e)
	}
}

// TestFinalizer_GCRunsUserDataGC:__gc 在 userdata 死亡后被调用
// (覆盖 RegisterFinalizer + separateFinalizers 复活 + runFinalizer 回调)。
func TestFinalizer_GCRunsUserDataGC(t *testing.T) {
	st := New()
	th := st.newThread()
	st.runningThread = th
	defer func() { st.runningThread = nil }()

	// host fn 作为 __gc handler:置全局标记
	called := false
	id := st.RegisterHostFn(func(_ *State, args []value.Value) ([]value.Value, *LuaError) {
		called = true
		return nil, nil
	})
	gcFn := st.MakeHostClosure(id)

	// userdata + metatable{__gc}
	ud := object.AllocUserdata(st.arena, 8)
	st.gc.LinkSweep(ud)
	meta := st.allocTable(0, 8)
	gcKey := value.MakeGC(value.TagString, st.gc.Intern([]byte("__gc")))
	if e := st.tableSet(meta, gcKey, value.MakeGC(value.TagFunction, gcFn)); e != nil {
		t.Fatalf("set __gc: %v", e)
	}
	object.SetUserdataMeta(st.arena, ud, meta)
	st.gc.RegisterFinalizer(ud)

	// meta 表保持可达(栈引用);userdata 不可达 → 本轮死白 → 复活 + __gc
	th.push(value.MakeGC(value.TagTable, meta))
	st.gc.Collect()

	if !called {
		t.Errorf("__gc finalizer was not invoked after collect")
	}
}
