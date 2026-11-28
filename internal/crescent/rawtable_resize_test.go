package crescent

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// TestResizeArray_MovesHashIntegerKeys: growing the array part moves the integer keys it now covers
// out of the hash part (5.1's resize inserts every hash entry again). Left behind, such a key could
// not be read any more -- lookups of 1..asize only look at the array part -- while next would still
// return it and, continuing from it, go back into the array part and come round to it again.
func TestResizeArray_MovesHashIntegerKeys(t *testing.T) {
	st := New()
	tbl := st.allocTable(0, 0)
	num := func(n int) value.Value { return value.NumberValue(float64(n)) }
	// 100, 150 and 300 live in the hash part; so do a string key and a non-integer key.
	for _, k := range []int{100, 150, 300} {
		if e := st.rawSet(tbl, num(k), num(k*10)); e != nil {
			t.Fatal(e)
		}
	}
	str := st.makeStringValue("x")
	_ = st.rawSet(tbl, str, num(7))
	_ = st.rawSet(tbl, value.NumberValue(1.5), num(8))
	if object.TableASize(st.arena, tbl) != 0 {
		t.Fatalf("setup: array part has %d slots, want 0", object.TableASize(st.arena, tbl))
	}
	gen0 := object.TableGen(st.arena, tbl)

	st.resizeArray(tbl, 200)

	if got := object.TableASize(st.arena, tbl); got != 200 {
		t.Fatalf("array part has %d slots, want 200", got)
	}
	if object.TableGen(st.arena, tbl) == gen0 {
		t.Fatal("keys changed slots but gen did not change")
	}
	for _, k := range []int{100, 150, 300} {
		if v := st.rawGet(tbl, num(k)); v != num(k*10) {
			t.Errorf("t[%d] reads %x after the resize, want %d", k, uint64(v), k*10)
		}
	}
	for _, k := range []int{100, 150} {
		if v := object.TableArrayAt(st.arena, tbl, uint32(k-1)); v != num(k*10) {
			t.Errorf("t[%d] is not in the array part after the resize", k)
		}
	}
	if v := st.rawGet(tbl, str); v != num(7) {
		t.Errorf("the string key reads %x, want 7", uint64(v))
	}
	if v := st.rawGet(tbl, value.NumberValue(1.5)); v != num(8) {
		t.Errorf("the non-integer key reads %x, want 8", uint64(v))
	}
	// No hash node still holds an integer key that the array part covers.
	for i := uint32(0); i < object.TableHSize(st.arena, tbl); i++ {
		if _, in := arrayIndex(object.NodeKey(st.arena, tbl, i), 200); in {
			t.Errorf("hash node %d still holds a key that belongs to the array part", i)
		}
	}
	// A traversal sees every entry exactly once and ends.
	seen := 0
	key := value.Nil
	for {
		k, _, ok, e := st.rawNext(tbl, key)
		if e != nil {
			t.Fatalf("next: %s", e.Msg)
		}
		if !ok {
			break
		}
		if seen++; seen > 10 {
			t.Fatal("the traversal does not end")
		}
		key = k
	}
	if seen != 5 {
		t.Errorf("the traversal saw %d entries, want 5", seen)
	}
}

// TestResizeArray_KeepsArrayAndNeverShrinks: the old array slots keep their values, and a size at or
// below the current one changes nothing (gen included).
func TestResizeArray_KeepsArrayAndNeverShrinks(t *testing.T) {
	st := New()
	tbl := st.allocTable(4, 0)
	for i := uint32(0); i < 4; i++ {
		object.SetTableArrayAt(st.arena, tbl, i, value.NumberValue(float64(i+1)))
	}
	st.resizeArray(tbl, 10)
	if got := object.TableASize(st.arena, tbl); got != 10 {
		t.Fatalf("array part has %d slots, want 10", got)
	}
	for i := uint32(0); i < 10; i++ {
		want := value.Nil
		if i < 4 {
			want = value.NumberValue(float64(i + 1))
		}
		if v := object.TableArrayAt(st.arena, tbl, i); v != want {
			t.Errorf("slot %d holds %x, want %x", i, uint64(v), uint64(want))
		}
	}
	gen := object.TableGen(st.arena, tbl)
	for _, n := range []uint32{0, 5, 10} {
		st.resizeArray(tbl, n)
		if got := object.TableASize(st.arena, tbl); got != 10 {
			t.Errorf("resizeArray(%d) changed the array part to %d slots", n, got)
		}
	}
	if object.TableGen(st.arena, tbl) != gen {
		t.Error("a resize that changed nothing bumped gen")
	}
}

// TestResizeArray_DropsRemovedKeys: a node whose key was removed is not carried over, and the key
// can be stored again afterwards, in the array part when the array now covers it.
func TestResizeArray_DropsRemovedKeys(t *testing.T) {
	st := New()
	tbl := st.allocTable(0, 0)
	num := func(n int) value.Value { return value.NumberValue(float64(n)) }
	for _, k := range []int{50, 60, 400} {
		if e := st.rawSet(tbl, num(k), num(k)); e != nil {
			t.Fatal(e)
		}
	}
	_ = st.rawSet(tbl, num(60), value.Nil)
	_ = st.rawSet(tbl, num(400), value.Nil)

	st.resizeArray(tbl, 100)

	for i := uint32(0); i < object.TableHSize(st.arena, tbl); i++ {
		if k := object.NodeKey(st.arena, tbl, i); k != value.Nil {
			t.Errorf("hash node %d holds key %x after the resize, want every node free", i, uint64(k))
		}
	}
	if v := st.rawGet(tbl, num(50)); v != num(50) {
		t.Errorf("t[50] reads %x, want 50", uint64(v))
	}
	for _, k := range []int{60, 400} {
		if v := st.rawGet(tbl, num(k)); v != value.Nil {
			t.Errorf("the removed t[%d] reads %x, want nil", k, uint64(v))
		}
		if _, _, _, e := st.rawNext(tbl, num(k)); k == 400 && e == nil {
			t.Errorf("next accepted the removed key %d, whose node is gone", k)
		}
		if e := st.rawSet(tbl, num(k), num(k+1)); e != nil {
			t.Fatal(e)
		}
		if v := st.rawGet(tbl, num(k)); v != num(k+1) {
			t.Errorf("t[%d] reads %x after it was stored again, want %d", k, uint64(v), k+1)
		}
	}
	if v := object.TableArrayAt(st.arena, tbl, 59); v != num(61) {
		t.Errorf("t[60] is not in the array part after it was stored again")
	}
}
