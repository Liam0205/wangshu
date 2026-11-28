//go:build (wangshu_p3 || wangshu_p4) && wangshu_profile

package api_test

import (
	"testing"

	"github.com/Liam0205/wangshu"
)

// TestPreallocate_CompiledAccessAfterwards: compiled code that has read and written an integer key
// of the hash part reads and writes the same key correctly after Preallocate grew the array part
// over it. The functions are promoted before the growth and their compiled code is entered again
// after it; both are asserted, so the accesses checked here are the compiled ones.
func TestPreallocate_CompiledAccessAfterwards(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(true)
	tv := st.NewTable()
	defer tv.Release()
	tt := tv.AsTable()
	_ = tt.SetIndex(5, wangshu.Number(50))
	_ = tt.Set(wangshu.String("x"), wangshu.Number(8))
	st.SetGlobal("T", tv)
	prog, err := wangshu.Compile([]byte(`
		local t = T
		function get() local a for i = 1, 500 do a = t[5] end return a end
		function set(v) for i = 1, 500 do t[5] = v end end
	`), "preallocate")
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if _, err := prog.Run(st); err != nil {
		t.Fatalf("Run: %v", err)
	}
	get, set := st.GetGlobal("get"), st.GetGlobal("set")
	call := func(fn wangshu.Value, args ...wangshu.Value) []wangshu.Value {
		t.Helper()
		r, err := st.Call(fn, args...)
		if err != nil {
			t.Fatalf("Call: %v", err)
		}
		return r
	}
	for i := 0; i < 20; i++ {
		call(set, wangshu.Number(50))
		if got := call(get)[0].Number(); got != 50 {
			t.Fatalf("t[5] reads %v before the growth, want 50", got)
		}
	}
	if st.PromotionCount() < 2 {
		t.Fatalf("%d functions were promoted before the growth, want get and set", st.PromotionCount())
	}

	if e := tt.Preallocate(10); e != nil {
		t.Fatalf("Preallocate: %v", e)
	}
	entered := st.GibbousEntries()
	if got := call(get)[0].Number(); got != 50 {
		t.Errorf("t[5] reads %v after the growth, want 50", got)
	}
	call(set, wangshu.Number(77))
	if got := call(get)[0].Number(); got != 77 {
		t.Errorf("t[5] reads %v after a store that followed the growth, want 77", got)
	}
	if n := st.GibbousEntries() - entered; n != 3 {
		t.Errorf("compiled code was entered %d times after the growth, want 3", n)
	}
	if got := tt.GetIndex(5).Number(); got != 77 {
		t.Errorf("the host reads t[5] = %v, want 77", got)
	}
	n := 0
	if e := tt.ForEach(func(_, _ wangshu.Value) bool { n++; return n < 100 }); e != nil {
		t.Fatalf("ForEach: %v", e)
	}
	if n != 2 {
		t.Errorf("ForEach visited %d entries, want 2", n)
	}
}

// TestPreallocate_DuringCompiledLoop: the array part grows while compiled code is in the middle of
// a loop that reads and writes an integer key of the hash part. The loop reaches the host through
// an __add handler (a function that calls a host function directly is not compiled), and the
// handler grows the table on one iteration. The accesses after it see the key in the array part.
func TestPreallocate_DuringCompiledLoop(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(true)
	tv := st.NewTable()
	defer tv.Release()
	tt := tv.AsTable()
	_ = tt.SetIndex(5, wangshu.Number(0))
	_ = tt.Set(wangshu.String("x"), wangshu.Number(8))
	st.SetGlobal("T", tv)
	armed, calls, grown := false, 0, 0
	st.Register("handler", func(_ *wangshu.State, args []wangshu.Value) ([]wangshu.Value, error) {
		if armed {
			if calls++; calls == 1000 {
				grown++
				if e := tt.Preallocate(10); e != nil {
					return nil, e
				}
			}
		}
		return args[:1], nil
	})
	prog, err := wangshu.Compile([]byte(`
		local t = T
		local p = setmetatable({}, {__add = handler})
		function run()
			local s = 0
			for i = 1, 2000 do
				t[5] = t[5] + 1
				s = s + t[5]
				s = s + p
			end
			return s, t[5]
		end
	`), "preallocate")
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if _, err := prog.Run(st); err != nil {
		t.Fatalf("Run: %v", err)
	}
	run := st.GetGlobal("run")
	for i := 0; i < 5; i++ {
		if _, err := st.Call(run); err != nil {
			t.Fatalf("Call: %v", err)
		}
	}
	if st.PromotionCount() < 1 {
		t.Fatal("run was not promoted before the growth")
	}

	armed = true
	entered := st.GibbousEntries()
	r, err := st.Call(run)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if n := st.GibbousEntries() - entered; n != 1 {
		t.Fatalf("compiled code was entered %d times, want 1", n)
	}
	if calls != 2000 || grown != 1 {
		t.Fatalf("the handler was called %d times and grew the table %d times, want 2000 and 1", calls, grown)
	}
	// t[5] went from 10000 to 12000 in this call; s adds up 10001..12000.
	if len(r) != 2 || r[0].Number() != 22001000 || r[1].Number() != 12000 {
		t.Errorf("run returned %v and %v, want 22001000 and 12000", r[0].Number(), r[1].Number())
	}
	if got := tt.GetIndex(5).Number(); got != 12000 {
		t.Errorf("the host reads t[5] = %v, want 12000", got)
	}
	n := 0
	if e := tt.ForEach(func(_, _ wangshu.Value) bool { n++; return n < 100 }); e != nil {
		t.Fatalf("ForEach: %v", e)
	}
	if n != 2 {
		t.Errorf("ForEach visited %d entries, want 2", n)
	}
}
