// globals baseline 测试——MarkGlobalsBaseline + ResetGlobalsToBaseline
// (issue #6:sync.Pool 复用 State 时脚本级状态隔离,对位 gopher-lua statePool)。
package wangshu_test

import (
	"testing"

	"github.com/Liam0205/wangshu"
)

func TestBaseline_HijackedStdlibRestored(t *testing.T) {
	// 验收用例:脚本 hijack tostring;Reset 后 tostring 仍是 base 库函数。
	st := wangshu.NewState(wangshu.Options{})
	st.MarkGlobalsBaseline()
	// hijack
	prog, _ := wangshu.Compile([]byte(`tostring = "pwned"`), "h")
	if _, err := prog.Run(st); err != nil {
		t.Fatalf("hijack run: %v", err)
	}
	// 验证 hijack 生效
	if v := st.GetGlobal("tostring"); !v.IsString() || v.Str() != "pwned" {
		t.Fatalf("hijack didn't take effect: %s", v.Display())
	}
	// Reset 恢复
	st.ResetGlobalsToBaseline()
	// tostring 应该回到 function(host closure)
	v := st.GetGlobal("tostring")
	defer v.Release()
	if !v.IsFunction() {
		t.Errorf("post-reset tostring = %s, want function", v.Display())
	}
	// 验证是真函数:Lua 端调用,确认 tostring(42) == "42"
	verify, _ := wangshu.Compile([]byte(`return tostring(42)`), "v")
	r, err := verify.Run(st)
	if err != nil {
		t.Fatalf("verify run: %v", err)
	}
	if r[0].Str() != "42" {
		t.Errorf("tostring(42) = %s, want '42'", r[0].Display())
	}
}

func TestBaseline_NewGlobalDeleted(t *testing.T) {
	// new_global = 123 类 leak:Reset 后 globals 中不应剩 new_global
	st := wangshu.NewState(wangshu.Options{})
	st.MarkGlobalsBaseline()
	prog, _ := wangshu.Compile([]byte(`x = 123; y = "leak"`), "l")
	if _, err := prog.Run(st); err != nil {
		t.Fatalf("run: %v", err)
	}
	if v := st.GetGlobal("x"); !v.IsNumber() || v.Number() != 123 {
		t.Fatalf("x pre-reset = %s", v.Display())
	}
	st.ResetGlobalsToBaseline()
	if v := st.GetGlobal("x"); !v.IsNil() {
		t.Errorf("x post-reset = %s, want nil", v.Display())
	}
	if v := st.GetGlobal("y"); !v.IsNil() {
		t.Errorf("y post-reset = %s, want nil", v.Display())
	}
}

func TestBaseline_MultipleBorrowCycles(t *testing.T) {
	// pineapple statePool 形态:100 次 Borrow + hijack + Return 循环,
	// 每次 Borrow 看到的 tostring 都是干净的 baseline。
	st := wangshu.NewState(wangshu.Options{})
	st.MarkGlobalsBaseline()
	hijack, _ := wangshu.Compile([]byte(`tostring = "pwn" .. tostring(0)`), "h")
	for i := 0; i < 100; i++ {
		// Borrow:验证 tostring 是干净的
		v := st.GetGlobal("tostring")
		if !v.IsFunction() {
			t.Fatalf("iter %d Borrow: tostring = %s, want function", i, v.Display())
		}
		v.Release()
		// 用脚本 hijack
		if _, err := hijack.Run(st); err != nil {
			t.Fatalf("iter %d hijack: %v", i, err)
		}
		// Return:Reset
		st.ResetGlobalsToBaseline()
	}
}

func TestBaseline_BaselineSurvivesGC(t *testing.T) {
	// baseline 复合值(table/function)在 globals 覆盖 + GC 压力下不应被回收
	st := wangshu.NewState(wangshu.Options{})
	st.MarkGlobalsBaseline()
	// hijack tostring 多次 + 压力 GC
	st.SetGCStressMode(true)
	defer st.SetGCStressMode(false)
	for i := 0; i < 50; i++ {
		st.SetGlobal("tostring", wangshu.String("pwned"))
	}
	st.ResetGlobalsToBaseline()
	v := st.GetGlobal("tostring")
	defer v.Release()
	if !v.IsFunction() {
		t.Fatalf("post-reset tostring = %s, want function (baseline GC'd?)", v.Display())
	}
	verify, _ := wangshu.Compile([]byte(`return tostring(7)`), "v")
	r, err := verify.Run(st)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if r[0].Str() != "7" {
		t.Errorf("tostring(7) = %s", r[0].Display())
	}
}

func TestBaseline_RepeatedMarkOverridesOld(t *testing.T) {
	// Mark 重复调用:第二次 Mark 用新 baseline,旧 baseline 失效
	st := wangshu.NewState(wangshu.Options{})
	st.MarkGlobalsBaseline()
	st.SetGlobal("custom", wangshu.Number(42))
	st.MarkGlobalsBaseline() // 第二次:把 custom 也纳入基线
	// 脚本删除 custom
	prog, _ := wangshu.Compile([]byte(`custom = nil`), "d")
	if _, err := prog.Run(st); err != nil {
		t.Fatalf("run: %v", err)
	}
	st.ResetGlobalsToBaseline()
	if v := st.GetGlobal("custom"); !v.IsNumber() || v.Number() != 42 {
		t.Errorf("custom = %s, want 42", v.Display())
	}
}

func TestBaseline_ResetWithoutMarkClearsAll(t *testing.T) {
	// 未 Mark 直接 Reset:基线空 → 所有字符串 key globals 被清(慎用,
	// godoc 已警告;此测试只验证行为一致)
	st := wangshu.NewState(wangshu.Options{})
	st.SetGlobal("user", wangshu.Number(1))
	st.ResetGlobalsToBaseline()
	if v := st.GetGlobal("user"); !v.IsNil() {
		t.Errorf("user = %s, want nil", v.Display())
	}
	// stdlib 也会被清——验证 tostring 已不存在
	if v := st.GetGlobal("tostring"); !v.IsNil() {
		t.Errorf("tostring = %s, want nil (no baseline → all cleared)", v.Display())
	}
}

func TestBaseline_TableHijackRestored(t *testing.T) {
	// stdlib 的 table 库 hijack 后能复原
	st := wangshu.NewState(wangshu.Options{})
	st.MarkGlobalsBaseline()
	prog, _ := wangshu.Compile([]byte(`table = "pwned"`), "h")
	if _, err := prog.Run(st); err != nil {
		t.Fatalf("run: %v", err)
	}
	st.ResetGlobalsToBaseline()
	tv := st.GetGlobal("table")
	defer tv.Release()
	if !tv.IsTable() {
		t.Fatalf("table = %s, want table", tv.Display())
	}
	// 验证还能用:table.insert
	prog2, _ := wangshu.Compile([]byte(`local t = {}; table.insert(t, 7); return t[1]`), "u")
	r, err := prog2.Run(st)
	if err != nil {
		t.Fatalf("use table: %v", err)
	}
	if r[0].Number() != 7 {
		t.Errorf("table.insert result = %s", r[0].Display())
	}
}
