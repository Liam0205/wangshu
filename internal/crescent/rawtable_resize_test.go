package crescent

import (
	"fmt"
	"testing"

	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// traverse walks tbl with rawNext from start (Nil for the beginning) and returns how often each
// key was returned. It fails the test when the traversal raises or does not end.
func traverse(t *testing.T, st *State, tbl arena.GCRef, start value.Value) map[value.Value]int {
	t.Helper()
	seen := map[value.Value]int{}
	key := start
	for steps := 0; ; steps++ {
		k, _, ok, e := st.rawNext(tbl, key)
		if e != nil {
			t.Fatalf("next: %s", e.Msg)
		}
		if !ok {
			return seen
		}
		if steps > 1000 {
			t.Fatal("the traversal does not end")
		}
		seen[k]++
		key = k
	}
}

// TestGrowArray_MovesHashIntegerKeys: growing the array part moves the integer keys it now covers
// out of the hash part. Left behind, such a key could not be read any more -- lookups of 1..asize
// only look at the array part -- while next would still return it and, continuing from it, go back
// into the array part and come round to it again.
func TestGrowArray_MovesHashIntegerKeys(t *testing.T) {
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
	node0 := object.TableNodeRef(st.arena, tbl)
	hsize0 := object.TableHSize(st.arena, tbl)

	st.growArray(tbl, 200)

	if got := object.TableASize(st.arena, tbl); got != 200 {
		t.Fatalf("array part has %d slots, want 200", got)
	}
	if object.TableGen(st.arena, tbl) == gen0 {
		t.Fatal("the array block changed and keys moved, but gen did not change")
	}
	if object.TableNodeRef(st.arena, tbl) != node0 || object.TableHSize(st.arena, tbl) != hsize0 {
		t.Error("the hash part was replaced; it has to stay where it is")
	}
	for _, k := range []int{100, 150, 300} {
		if v := st.rawGet(tbl, num(k)); v != num(k*10) {
			t.Errorf("t[%d] reads %x after the growth, want %d", k, uint64(v), k*10)
		}
	}
	for _, k := range []int{100, 150} {
		if v := object.TableArrayAt(st.arena, tbl, uint32(k-1)); v != num(k*10) {
			t.Errorf("t[%d] is not in the array part after the growth", k)
		}
	}
	if v := st.rawGet(tbl, str); v != num(7) {
		t.Errorf("the string key reads %x, want 7", uint64(v))
	}
	if v := st.rawGet(tbl, value.NumberValue(1.5)); v != num(8) {
		t.Errorf("the non-integer key reads %x, want 8", uint64(v))
	}
	// No hash node still holds a value under a key that the array part covers.
	for i := uint32(0); i < object.TableHSize(st.arena, tbl); i++ {
		if _, in := arrayIndex(object.NodeKey(st.arena, tbl, i), 200); in && object.NodeVal(st.arena, tbl, i) != value.Nil {
			t.Errorf("hash node %d still holds a value under a key that belongs to the array part", i)
		}
	}
	// A traversal sees every entry exactly once and ends.
	seen := traverse(t, st, tbl, value.Nil)
	if len(seen) != 5 {
		t.Errorf("the traversal saw %d entries, want 5", len(seen))
	}
	for k, n := range seen {
		if n != 1 {
			t.Errorf("the traversal returned key %x %d times", uint64(k), n)
		}
	}
	// A moved key is stored and removed in the array part from now on.
	_ = st.rawSet(tbl, num(100), num(1))
	if v := object.TableArrayAt(st.arena, tbl, 99); v != num(1) {
		t.Errorf("a store to the moved key 100 did not go to the array part")
	}
	_ = st.rawSet(tbl, num(100), value.Nil)
	if v := st.rawGet(tbl, num(100)); v != value.Nil {
		t.Errorf("the moved key 100 reads %x after it was removed, want nil", uint64(v))
	}
}

// TestGrowArray_KeepsArrayAndNeverShrinks: the old array slots keep their values, and a size at or
// below the current one changes nothing (gen included).
func TestGrowArray_KeepsArrayAndNeverShrinks(t *testing.T) {
	st := New()
	tbl := st.allocTable(4, 0)
	for i := uint32(0); i < 4; i++ {
		object.SetTableArrayAt(st.arena, tbl, i, value.NumberValue(float64(i+1)))
	}
	st.growArray(tbl, 10)
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
	arr := object.TableArrayRef(st.arena, tbl)
	for _, n := range []uint32{0, 5, 10} {
		st.growArray(tbl, n)
		if got := object.TableASize(st.arena, tbl); got != 10 {
			t.Errorf("growArray(%d) changed the array part to %d slots", n, got)
		}
	}
	if object.TableGen(st.arena, tbl) != gen || object.TableArrayRef(st.arena, tbl) != arr {
		t.Error("a growth that changed nothing bumped gen or replaced the array block")
	}
}

// TestGrowArray_TraversalGoesOn: a traversal in progress goes on after the array part grows. The
// table has string keys only, some of them removed, so no key has to move: from whichever key the
// traversal stands on, removed or not, it returns exactly the entries it would have returned
// without the growth.
func TestGrowArray_TraversalGoesOn(t *testing.T) {
	build := func() (*State, arena.GCRef, []value.Value) {
		st := New()
		tbl := st.allocTable(0, 0)
		keys := make([]value.Value, 20)
		for i := range keys {
			keys[i] = st.makeStringValue(fmt.Sprintf("k%d", i))
			if e := st.rawSet(tbl, keys[i], value.NumberValue(float64(i))); e != nil {
				t.Fatal(e)
			}
		}
		for _, i := range []int{3, 7, 11} {
			_ = st.rawSet(tbl, keys[i], value.Nil)
		}
		return st, tbl, keys
	}
	for at := 0; at < 20; at++ {
		st, tbl, keys := build()
		want := traverse(t, st, tbl, keys[at])
		st.growArray(tbl, 64)
		got := traverse(t, st, tbl, keys[at])
		if len(got) != len(want) {
			t.Errorf("standing on k%d: %d entries follow after the growth, %d without it", at, len(got), len(want))
		}
		for k := range want {
			if got[k] != 1 {
				t.Errorf("standing on k%d: an entry that followed is returned %d times after the growth", at, got[k])
			}
		}
	}
}

// TestGrowArray_TraversalEndsWhenKeysMove: when integer keys move, a traversal standing anywhere
// still ends, and it returns no key more than once unless it stood on a moved key (from there it
// continues in the array part and meets the hash part again).
func TestGrowArray_TraversalEndsWhenKeysMove(t *testing.T) {
	num := func(n int) value.Value { return value.NumberValue(float64(n)) }
	build := func() (*State, arena.GCRef, []value.Value) {
		st := New()
		tbl := st.allocTable(0, 0)
		var keys []value.Value
		for _, k := range []int{40, 50, 60, 900} {
			keys = append(keys, num(k))
		}
		for i := 0; i < 6; i++ {
			keys = append(keys, st.makeStringValue(fmt.Sprintf("k%d", i)))
		}
		for i, k := range keys {
			if e := st.rawSet(tbl, k, num(i)); e != nil {
				t.Fatal(e)
			}
		}
		if object.TableASize(st.arena, tbl) != 0 {
			t.Fatalf("setup: array part has %d slots, want 0", object.TableASize(st.arena, tbl))
		}
		return st, tbl, keys
	}
	_, _, keys := build()
	for at := range keys {
		st, tbl, keys := build()
		st.growArray(tbl, 100)
		got := traverse(t, st, tbl, keys[at])
		_, moved := arrayIndex(keys[at], 100)
		for k, n := range got {
			if n != 1 {
				t.Errorf("standing on key %d: key %x is returned %d times", at, uint64(k), n)
			}
		}
		if !moved && len(got) > len(keys)-1 {
			t.Errorf("standing on key %d, which did not move: %d entries follow, more than the table has left", at, len(got))
		}
	}
}

// TestGrowArray_TraversalFromARemovedIntegerKey: a traversal that stands on an integer key of the
// hash part whose entry was removed, and that the grown array part covers, continues in the array
// part and goes through the whole hash part again, although no key had to move: next takes a key
// in 1..asize for an array index. It ends, and it returns every entry once.
func TestGrowArray_TraversalFromARemovedIntegerKey(t *testing.T) {
	num := func(n int) value.Value { return value.NumberValue(float64(n)) }
	st := New()
	tbl := st.allocTable(0, 0)
	for i := 0; i < 10; i++ {
		_ = st.rawSet(tbl, st.makeStringValue(fmt.Sprintf("k%d", i)), num(i))
	}
	_ = st.rawSet(tbl, num(500), num(1))
	_ = st.rawSet(tbl, num(5), num(2))
	if object.TableASize(st.arena, tbl) != 0 {
		t.Fatalf("setup: array part has %d slots, want 0", object.TableASize(st.arena, tbl))
	}
	_ = st.rawSet(tbl, num(5), value.Nil)
	rest := traverse(t, st, tbl, num(5))
	if len(rest) == 0 || len(rest) >= 11 {
		t.Fatalf("setup: %d entries follow key 5, want some but not all of the 11 others", len(rest))
	}

	st.growArray(tbl, 10)

	got := traverse(t, st, tbl, num(5))
	if len(got) != 11 {
		t.Errorf("%d entries are returned from the removed key 5 after the growth, want all 11", len(got))
	}
	for k, n := range got {
		if n != 1 {
			t.Errorf("key %x is returned %d times", uint64(k), n)
		}
	}
}
