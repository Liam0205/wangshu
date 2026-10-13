package api_test

import (
	"strconv"
	"testing"

	wangshu "github.com/Liam0205/wangshu"
)

// TestHostTriggeredCollect_KeepsObjectsBeingBuilt: with SetHostTriggeredCollect on, a
// collection used to run inside the allocation's own accounting, after the new object was
// linked on the sweep chain but before anything rooted it. NewTable's table was swept before
// PinRef (20000 tables left a few handles aliasing the next table's block), and SetGlobal's
// interned name could be swept before it was stored. Collections now run when the public
// call returns, so every handle keeps its own table and every global its own key.
func TestHostTriggeredCollect_KeepsObjectsBeingBuilt(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	st.SetHostTriggeredCollect(true)
	ts := make([]wangshu.Value, 20000)
	for i := range ts {
		ts[i] = st.NewTable()
	}
	for i, tv := range ts {
		if err := tv.AsTable().SetIndex(1, wangshu.Number(float64(i))); err != nil {
			t.Fatalf("SetIndex: %v", err)
		}
	}
	bad := 0
	for i, tv := range ts {
		if v := tv.AsTable().GetIndex(1); v.Number() != float64(i) {
			bad++
		}
	}
	if bad != 0 {
		t.Errorf("%d of %d tables read back another table's value", bad, len(ts))
	}
	for i := 0; i < 5000; i++ {
		st.SetGlobal("g"+strconv.Itoa(i), wangshu.Number(float64(i)))
	}
	for i := 0; i < 5000; i++ {
		if v := st.GetGlobal("g" + strconv.Itoa(i)); v.Number() != float64(i) {
			t.Fatalf("global g%d = %v, want %d", i, v.Display(), i)
		}
	}
}

// TestHostTriggeredCollect_CollectsAtAPIBoundary: the switch still does its job — host-only
// churn (tables built and released between calls, no script running) is collected without
// an explicit Collect, while with the switch off it piles up.
func TestHostTriggeredCollect_CollectsAtAPIBoundary(t *testing.T) {
	churn := func(st *wangshu.State) float64 {
		for r := 0; r < 200; r++ {
			tv := st.NewTable()
			for i := 0; i < 200; i++ {
				_ = tv.AsTable().SetIndex(i+1, wangshu.Number(float64(i)))
			}
			tv.Release()
		}
		return st.GCCountKB()
	}
	off := wangshu.NewState(wangshu.Options{})
	on := wangshu.NewState(wangshu.Options{})
	on.SetHostTriggeredCollect(true)
	kbOff, kbOn := churn(off), churn(on)
	t.Logf("GCCountKB after churn: off=%.0f on=%.0f", kbOff, kbOn)
	if kbOn*4 > kbOff {
		t.Errorf("switch on did not keep host churn down: off=%.0f on=%.0f", kbOff, kbOn)
	}
}
