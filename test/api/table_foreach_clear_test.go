package api_test

import (
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
)

// TestTableForEach_ClearingEntries: fn may remove the entry it is given, release the key and
// collect; the iteration continues from that key and visits every entry once (#338).
func TestTableForEach_ClearingEntries(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	prog, err := wangshu.Compile([]byte(`
tbl338 = {[{}] = 1, [{}] = 2, [{}] = 3, [1.5] = 4, x = 5, [true] = 6}`), "setup")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := prog.Run(st); err != nil {
		t.Fatal(err)
	}
	tv := st.GetGlobal("tbl338")
	defer tv.Release()
	tbl := tv.AsTable()

	n, sum := 0, 0.0
	err = tbl.ForEach(func(key, val wangshu.Value) bool {
		n++
		sum += val.Number()
		if e := tbl.Set(key, wangshu.Nil()); e != nil {
			t.Errorf("Set(key, nil): %v", e)
		}
		key.Release()
		st.Collect()
		return true
	})
	if err != nil {
		t.Fatalf("ForEach: %v", err)
	}
	if n != 6 || sum != 21 {
		t.Fatalf("visited %d entries with sum %v, want 6 and 21", n, sum)
	}
	left := 0
	if err := tbl.ForEach(func(_, _ wangshu.Value) bool { left++; return true }); err != nil {
		t.Fatalf("second ForEach: %v", err)
	}
	if left != 0 {
		t.Fatalf("%d entries left after clearing", left)
	}
}

// TestTableForeach_PanicInCallbackReleasesTheValue: table.foreach keeps the current value rooted
// while its callback runs. When the callback ends in a Go panic that the embedding API turns into
// an error (here the arena reaching MaxArenaBytes), that root is released too: the State stays
// usable, and the entry of the weak-valued table is cleared by the next collection.
func TestTableForeach_PanicInCallbackReleasesTheValue(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{MaxArenaBytes: 8 << 20})
	overflow, err := wangshu.Compile([]byte(`
W338 = setmetatable({}, {__mode = "v"})
W338[1] = {}
table.foreach(W338, function(k, v)
  local t = {}
  while true do t[#t + 1] = string.rep("x", 1000) .. #t end
end)`), "overflow")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := overflow.Run(st); err == nil || !strings.Contains(err.Error(), "arena") {
		t.Fatalf("the callback should have exhausted the arena, got %v", err)
	}
	check, err := wangshu.Compile([]byte(`collectgarbage() collectgarbage() return W338[1] ~= nil`), "check")
	if err != nil {
		t.Fatal(err)
	}
	rs, err := check.Run(st)
	if err != nil {
		t.Fatalf("the State is not usable after the overflow: %v", err)
	}
	if len(rs) != 1 || rs[0].Bool() {
		t.Errorf("the value of the weak entry is still rooted after the callback panicked")
	}
}
