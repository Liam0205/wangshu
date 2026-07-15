// globals baseline tests -- MarkGlobalsBaseline + ResetGlobalsToBaseline
// (issue #6: script-level state isolation when reusing State via sync.Pool,
// matching gopher-lua statePool).
package wangshu_test

import (
	"testing"

	"github.com/Liam0205/wangshu"
)

func TestBaseline_HijackedStdlibRestored(t *testing.T) {
	// Acceptance case: script hijacks tostring; after Reset, tostring is still the base library function.
	st := wangshu.NewState(wangshu.Options{})
	st.MarkGlobalsBaseline()
	// hijack
	prog, _ := wangshu.Compile([]byte(`tostring = "pwned"`), "h")
	if _, err := prog.Run(st); err != nil {
		t.Fatalf("hijack run: %v", err)
	}
	// verify hijack took effect
	if v := st.GetGlobal("tostring"); !v.IsString() || v.Str() != "pwned" {
		t.Fatalf("hijack didn't take effect: %s", v.Display())
	}
	// Reset restores
	st.ResetGlobalsToBaseline()
	// tostring should return to a function (host closure)
	v := st.GetGlobal("tostring")
	defer v.Release()
	if !v.IsFunction() {
		t.Errorf("post-reset tostring = %s, want function", v.Display())
	}
	// Verify it's a real function: call it from Lua, confirm tostring(42) == "42".
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
	// new_global = 123 style leak: after Reset, new_global must not remain in globals
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
	// pineapple statePool pattern: 100 cycles of Borrow + hijack + Return;
	// each Borrow sees a clean baseline tostring.
	st := wangshu.NewState(wangshu.Options{})
	st.MarkGlobalsBaseline()
	hijack, _ := wangshu.Compile([]byte(`tostring = "pwn" .. tostring(0)`), "h")
	for i := 0; i < 100; i++ {
		// Borrow: verify tostring is clean
		v := st.GetGlobal("tostring")
		if !v.IsFunction() {
			t.Fatalf("iter %d Borrow: tostring = %s, want function", i, v.Display())
		}
		v.Release()
		// hijack via script
		if _, err := hijack.Run(st); err != nil {
			t.Fatalf("iter %d hijack: %v", i, err)
		}
		// Return: Reset
		st.ResetGlobalsToBaseline()
	}
}

func TestBaseline_BaselineSurvivesGC(t *testing.T) {
	// baseline compound values (table/function) must not be reclaimed under globals overwrite + GC pressure
	st := wangshu.NewState(wangshu.Options{})
	st.MarkGlobalsBaseline()
	// hijack tostring repeatedly + stress GC
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
	// Repeated Mark calls: the second Mark uses the new baseline, invalidating the old one
	st := wangshu.NewState(wangshu.Options{})
	st.MarkGlobalsBaseline()
	st.SetGlobal("custom", wangshu.Number(42))
	st.MarkGlobalsBaseline() // second call: fold custom into the baseline too
	// script deletes custom
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
	// Reset without prior Mark: baseline is empty → all string-key globals are cleared
	// (use with care, godoc already warns; this test only verifies behavioral consistency)
	st := wangshu.NewState(wangshu.Options{})
	st.SetGlobal("user", wangshu.Number(1))
	st.ResetGlobalsToBaseline()
	if v := st.GetGlobal("user"); !v.IsNil() {
		t.Errorf("user = %s, want nil", v.Display())
	}
	// stdlib is cleared too -- verify tostring no longer exists
	if v := st.GetGlobal("tostring"); !v.IsNil() {
		t.Errorf("tostring = %s, want nil (no baseline → all cleared)", v.Display())
	}
}

func TestBaseline_TableHijackRestored(t *testing.T) {
	// the stdlib table library can be restored after hijack
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
	// verify it still works: table.insert
	prog2, _ := wangshu.Compile([]byte(`local t = {}; table.insert(t, 7); return t[1]`), "u")
	r, err := prog2.Run(st)
	if err != nil {
		t.Fatalf("use table: %v", err)
	}
	if r[0].Number() != 7 {
		t.Errorf("table.insert result = %s", r[0].Display())
	}
}
