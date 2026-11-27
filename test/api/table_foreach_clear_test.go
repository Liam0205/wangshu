package api_test

import (
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
