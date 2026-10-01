// Targeted tests for the remaining 0%-coverage functions (coverage audit, third pass).
package crescent

import (
	"strings"
	"testing"

	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// TestErrName_GlobalDescription: mis-calling a global fn → "global 'f'" description
// (covers describeReg's GETGLOBAL branch and constStringAt).
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

// TestErrName_FieldDescription: mis-calling a table field → "field 'k'" description.
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

// TestErrName_MethodDescription: mis-calling a method → "method 'm'" description.
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

// TestTraceback_Exposed: State.Traceback in both forms — carried in an error and as a standalone call.
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
	// Standalone call (no running thread)
	if tb := st.Traceback(); !strings.HasPrefix(tb, "stack traceback:") {
		t.Errorf("standalone Traceback() = %q", tb)
	}
}

// TestRawTable_DeleteExistingKey: setting nil deletes an existing hash key (covers nodeSetKV).
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
	// Deleting an absent key = no-op
	other := value.MakeGC(value.TagString, st.gc.Intern([]byte("nokey")))
	if e := st.rawSet(tbl, other, value.Nil); e != nil {
		t.Errorf("deleting absent key should be no-op, got %v", e)
	}
}

// TestFinalizer_GCRunsUserDataGC: __gc is invoked after the userdata dies
// (covers RegisterFinalizer + separateFinalizers resurrection + runFinalizer callback).
func TestFinalizer_GCRunsUserDataGC(t *testing.T) {
	st := New()
	th := st.newThread()
	st.runningThread = th
	defer func() { st.runningThread = nil }()

	// A host fn as the __gc handler: sets a package-level flag
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

	// The meta table stays reachable (stack reference); the userdata is unreachable → dies white this round → resurrect + __gc
	th.push(value.MakeGC(value.TagTable, meta))
	st.gc.Collect()

	if !called {
		t.Errorf("__gc finalizer was not invoked after collect")
	}
}

// TestTraceback_NotBuiltForCaughtErrors pins 09 §7.3's cost rule after #279 moved the uncaught-error
// traceback to the raise point: an error that a pcall or coroutine.resume will catch must not pay for
// one. protectDepth is what tells the raise point which case it is in; without it every error raised
// under pcall would walk and render the whole stack only for the string to be dropped.
func TestTraceback_NotBuiltForCaughtErrors(t *testing.T) {
	st := New()
	var seen []*LuaError
	reg := func(name string, fn HostFn) {
		st.SetGlobal(name, value.MakeGC(value.TagFunction, st.MakeHostClosure(st.RegisterHostFn(fn))))
	}
	reg("probe", func(st *State, args []value.Value) ([]value.Value, *LuaError) {
		e := errf("probe")
		seen = append(seen, e)
		return nil, e
	})
	// The two catching boundaries, reduced to what pcall and coroutine.resume do (crescent's own tests
	// run without the stdlib).
	reg("mypcall", func(st *State, args []value.Value) ([]value.Value, *LuaError) {
		_, e := st.ProtectedCall(args[0], nil)
		return []value.Value{value.BoolValue(e == nil)}, nil
	})
	reg("myresume", func(st *State, args []value.Value) ([]value.Value, *LuaError) {
		id, e := st.NewCoroutine(args[0])
		if e != nil {
			return nil, e
		}
		_, ok, _ := st.Resume(id, nil)
		return []value.Value{value.BoolValue(ok)}, nil
	})
	prog := mustCompile(t, []byte(`
local function f() probe() end
mypcall(f)
myresume(f)
myresume(function() mypcall(f) end)
f()`))
	cl := st.LoadProgram(prog.mainID, prog.protos)
	if _, err := st.Call(cl, nil, 0); err == nil {
		t.Fatalf("expected the last, uncaught call to fail")
	}
	if len(seen) != 4 {
		t.Fatalf("probe ran %d times, want 4", len(seen))
	}
	for i, e := range seen[:3] {
		if e.Traceback != "" {
			t.Errorf("caught error %d carries a traceback: %q", i, e.Traceback)
		}
	}
	if !strings.HasPrefix(seen[3].Traceback, "stack traceback:\n\t[C]: in function 'probe'") {
		t.Errorf("uncaught error traceback = %q", seen[3].Traceback)
	}
}
