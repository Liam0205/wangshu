// GlobalsSlot pre-resolved handle tests (issue #13 item B). Verify that
// SetBySlot/GetBySlot are semantically equivalent to SetGlobal/GetGlobal,
// that cross-State misuse panics, and that Release is well-behaved.
package wangshu_test

import (
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
)

// TestGlobalsSlot_RoundTripScalar verifies that scalars (number/bool/string/nil)
// written and read via slot are semantically equivalent to SetGlobal/GetGlobal.
func TestGlobalsSlot_RoundTripScalar(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	cases := []struct {
		name string
		val  wangshu.Value
		read func(wangshu.Value) any
		want any
	}{
		{"num", wangshu.Number(42.5), func(v wangshu.Value) any { return v.Number() }, 42.5},
		{"flag", wangshu.Bool(true), func(v wangshu.Value) any { return v.Bool() }, true},
		{"name", wangshu.String("alice"), func(v wangshu.Value) any { return v.Str() }, "alice"},
		{"empty", wangshu.Nil(), func(v wangshu.Value) any { return v.IsNil() }, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			slot := st.GlobalsSlot(c.name)
			defer slot.Release()
			st.SetBySlot(slot, c.val)
			got := c.read(st.GetBySlot(slot))
			if got != c.want {
				t.Errorf("%s round-trip: got %v want %v", c.name, got, c.want)
			}
		})
	}
}

// TestGlobalsSlot_ScriptVisible verifies that a global written via slot is
// readable from the script side through `GETGLOBAL name`—the slot path writes
// to the very same slot of the same globals table that SetGlobal writes to.
func TestGlobalsSlot_ScriptVisible(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	priceSlot := st.GlobalsSlot("item_price")
	defer priceSlot.Release()
	rateSlot := st.GlobalsSlot("rate")
	defer rateSlot.Release()
	st.SetBySlot(priceSlot, wangshu.Number(100))
	st.SetBySlot(rateSlot, wangshu.Number(0.85))

	prog, _ := wangshu.Compile([]byte(`return item_price * rate + 10`), "calc")
	r, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if r[0].Number() != 100*0.85+10 {
		t.Errorf("calc = %v, want %v", r[0].Number(), 100*0.85+10)
	}
}

// TestGlobalsSlot_OverwriteAcrossBorrowSimulation simulates how pineapple's
// LuaOp reuses a state: acquire the slot once during Init, then repeatedly
// overwrite and read it within the Execute loop, with writes consistent with
// SetGlobal(name, v).
func TestGlobalsSlot_OverwriteAcrossBorrowSimulation(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	slot := st.GlobalsSlot("x")
	defer slot.Release()

	// Repeatedly overwrite via slot; each read should reflect the latest value
	for i := 0; i < 100; i++ {
		st.SetBySlot(slot, wangshu.Number(float64(i)))
		v := st.GetBySlot(slot)
		if got := v.Number(); got != float64(i) {
			t.Fatalf("iter %d: got %v want %v", i, got, float64(i))
		}
	}

	// slot writes and SetGlobal writes should reach the same globals slot
	st.SetBySlot(slot, wangshu.Number(7))
	via := st.GetGlobal("x")
	if got := via.Number(); got != 7 {
		t.Errorf("slot write not visible via GetGlobal: got %v want 7", got)
	}

	st.SetGlobal("x", wangshu.Number(11))
	viaSlot := st.GetBySlot(slot)
	if got := viaSlot.Number(); got != 11 {
		t.Errorf("SetGlobal write not visible via GetBySlot: got %v want 11", got)
	}
}

// TestGlobalsSlot_TableValueWritePersists verifies that writing a table-typed
// Value via slot lands in globals, that a later script can read it, and that
// after Release the table contents are kept alive by the globals holding them.
func TestGlobalsSlot_TableValueWritePersists(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	slot := st.GlobalsSlot("xs")
	defer slot.Release()

	tv := st.NewFloatArrayTable([]float64{1, 2, 3, 4, 5})
	st.SetBySlot(slot, tv)
	tv.Release() // held by the global table, the table itself stays reachable

	prog, _ := wangshu.Compile([]byte(`
		local s = 0
		for i = 1, #xs do s = s + xs[i] end
		return s
	`), "sum")
	r, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if r[0].Number() != 15 {
		t.Errorf("sum = %v, want 15", r[0].Number())
	}
}

// TestGlobalsSlot_CrossStatePanics verifies that cross-State misuse of SetBySlot
// triggers a panic (fail-fast style, same as passing a cross-State function
// argument to State.Call).
func TestGlobalsSlot_CrossStatePanics(t *testing.T) {
	st1 := wangshu.NewState(wangshu.Options{})
	st2 := wangshu.NewState(wangshu.Options{})
	slot1 := st1.GlobalsSlot("x")
	defer slot1.Release()

	t.Run("Set", func(t *testing.T) {
		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("expected panic on cross-State SetBySlot")
			}
			if msg, ok := r.(string); !ok || !strings.Contains(msg, "different State") {
				t.Errorf("panic msg = %v, want 'different State'", r)
			}
		}()
		st2.SetBySlot(slot1, wangshu.Number(1))
	})

	t.Run("Get", func(t *testing.T) {
		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("expected panic on cross-State GetBySlot")
			}
			if msg, ok := r.(string); !ok || !strings.Contains(msg, "different State") {
				t.Errorf("panic msg = %v, want 'different State'", r)
			}
		}()
		_ = st2.GetBySlot(slot1)
	})
}

// TestGlobalsSlot_UseAfterReleasePanics verifies that using a slot after Release
// panics.
func TestGlobalsSlot_UseAfterReleasePanics(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	slot := st.GlobalsSlot("zombie")
	slot.Release()

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on use-after-Release")
		}
	}()
	// After Release, slot.st is set to nil ⟹ the cross-State check trips first
	// ("different State"); this is a reasonable fail-fast (any panic serves the
	// purpose, the exact message is not required).
	st.SetBySlot(slot, wangshu.Number(1))
}

// TestGlobalsSlot_DoubleReleaseSafe verifies that a repeated Release is safe
// (same as Value.Release).
func TestGlobalsSlot_DoubleReleaseSafe(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	slot := st.GlobalsSlot("x")
	slot.Release()
	slot.Release() // should be no-op, not panic
}

// TestGlobalsSlot_EmptyName verifies that an empty-string name is valid
// (equivalent to SetGlobal("", v)).
func TestGlobalsSlot_EmptyName(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	slot := st.GlobalsSlot("")
	defer slot.Release()
	st.SetBySlot(slot, wangshu.Number(99))
	if got := st.GetGlobal("").Number(); got != 99 {
		t.Errorf("globals[''] via slot = %v, want 99", got)
	}
}

// TestGlobalsSlot_SameNameSharedKey verifies that acquiring a slot multiple
// times under the same name writes to the same globals slot—the two slots'
// internal GCRefs point to the same interned string and land in the same
// globals bucket.
func TestGlobalsSlot_SameNameSharedKey(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	a := st.GlobalsSlot("shared")
	defer a.Release()
	b := st.GlobalsSlot("shared")
	defer b.Release()

	st.SetBySlot(a, wangshu.Number(1))
	if got := st.GetBySlot(b).Number(); got != 1 {
		t.Errorf("slot b read: got %v want 1", got)
	}
	st.SetBySlot(b, wangshu.Number(2))
	if got := st.GetBySlot(a).Number(); got != 2 {
		t.Errorf("slot a read after slot b write: got %v want 2", got)
	}
}

// TestGlobalsSlot_GCStressMode performs repeated slot operations under GC
// pressure, verifying that the pinned name-string GCRef is not reclaimed
// (otherwise SetBySlot/GetBySlot would read from a mis-bound arena).
func TestGlobalsSlot_GCStressMode(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	st.SetGCStressMode(true)
	slot := st.GlobalsSlot("hot_field")
	defer slot.Release()

	for i := 0; i < 200; i++ {
		st.SetBySlot(slot, wangshu.Number(float64(i)))
		if got := st.GetBySlot(slot).Number(); got != float64(i) {
			t.Fatalf("iter %d: got %v want %v", i, got, float64(i))
		}
	}
}
