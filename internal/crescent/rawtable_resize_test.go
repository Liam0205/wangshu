package crescent

import (
	"fmt"
	"testing"

	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// follow returns the keys that rawNext returns from start on (Nil for the beginning), in order.
// It fails the test when the traversal raises or does not end.
func follow(t *testing.T, st *State, tbl arena.GCRef, start value.Value) []value.Value {
	t.Helper()
	var keys []value.Value
	key := start
	for {
		k, _, ok, e := st.rawNext(tbl, key)
		if e != nil {
			t.Fatalf("next: %s", e.Msg)
		}
		if !ok {
			return keys
		}
		if len(keys) > 1000 {
			t.Fatal("the traversal does not end")
		}
		keys = append(keys, k)
		key = k
	}
}

// traverse walks tbl like follow and returns how often each key was returned.
func traverse(t *testing.T, st *State, tbl arena.GCRef, start value.Value) map[value.Value]int {
	t.Helper()
	seen := map[value.Value]int{}
	for _, k := range follow(t, st, tbl, start) {
		seen[k]++
	}
	return seen
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
	// The block a growth replaces goes back to the allocator: the next block of its size is it.
	st.growArray(tbl, 40)
	if got := object.AllocTableArray(st.arena, 10); got != arr {
		t.Errorf("the replaced array block %#x was not freed: a new block of its size is at %#x", arr, got)
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

// growUnderTraversal grows the array part of tbl to newSize while a traversal stands on here, and
// checks what the traversal returns from there against what it would have returned without the
// growth:
//
//   - here in the array part: the same entries, the moved ones from the array part;
//   - here in the hash part and not covered by the growth: the same entries in the same order,
//     without the moved ones;
//   - here in the hash part and covered by the growth: the entries that moved to a slot behind
//     here, then every entry left in the hash part, and nothing else.
func growUnderTraversal(t *testing.T, st *State, tbl arena.GCRef, here value.Value, newSize uint32) {
	t.Helper()
	oldSize := object.TableASize(st.arena, tbl)
	all := follow(t, st, tbl, value.Nil)
	without := follow(t, st, tbl, here)
	covered := func(k value.Value) bool {
		_, in := arrayIndex(k, newSize)
		return in
	}
	_, hereInArray := arrayIndex(here, oldSize)
	hereIdx, hereCovered := arrayIndex(here, newSize)

	st.growArray(tbl, newSize)

	var want []value.Value
	if hereCovered {
		// The array slots behind here, in index order, then the hash part from its start.
		for idx := hereIdx + 1; idx < newSize; idx++ {
			k := value.NumberValue(float64(idx + 1))
			for _, e := range all {
				if e == k {
					want = append(want, k)
				}
			}
		}
		for _, k := range all {
			if !covered(k) {
				want = append(want, k)
			}
		}
	} else {
		for _, k := range without {
			if !covered(k) {
				want = append(want, k)
			}
		}
	}
	got := follow(t, st, tbl, here)
	name := func(keys []value.Value) string {
		out := ""
		for _, k := range keys {
			out += debugVal(st, k) + " "
		}
		return out
	}
	if name(got) != name(want) {
		t.Errorf("growing to %d under a traversal standing on %s returns\n  %s\nwant\n  %s\n(without the growth: %s)",
			newSize, debugVal(st, here), name(got), name(want), name(without))
	}
	if hereInArray {
		// Stated in terms of the traversal without the growth: the same entries, once each.
		seen := map[value.Value]int{}
		for _, k := range got {
			seen[k]++
		}
		for _, k := range without {
			if seen[k] != 1 {
				t.Errorf("standing on %s in the array part: %s, which would have followed, is returned %d times", debugVal(st, here), debugVal(st, k), seen[k])
			}
		}
		if len(got) != len(without) {
			t.Errorf("standing on %s in the array part: %d entries follow, %d without the growth", debugVal(st, here), len(got), len(without))
		}
	}
}

// TestGrowArray_TraversalWhenKeysMove: what a traversal returns after a growth that moved integer
// keys, from every key it can stand on (growUnderTraversal states the rule). The table has an
// array part, integer keys of the hash part that the growth covers (9, 20, 40) and one it does
// not (900), and string keys.
func TestGrowArray_TraversalWhenKeysMove(t *testing.T) {
	num := func(n int) value.Value { return value.NumberValue(float64(n)) }
	// build returns the table and its keys in the order of a traversal. Every build lays the
	// table out the same way, so position i names the same entry in each of them.
	build := func() (*State, arena.GCRef, []value.Value) {
		st := New()
		tbl := st.allocTable(0, 0)
		for _, k := range []int{1, 2, 3, 4, 40, 20, 900, 9} {
			if e := st.rawSet(tbl, num(k), num(k)); e != nil {
				t.Fatal(e)
			}
		}
		for i := 0; i < 6; i++ {
			_ = st.rawSet(tbl, st.makeStringValue(fmt.Sprintf("k%d", i)), num(i))
		}
		if got := object.TableASize(st.arena, tbl); got != 4 {
			t.Fatalf("setup: array part has %d slots, want 4", got)
		}
		keys := follow(t, st, tbl, value.Nil)
		if len(keys) != 14 {
			t.Fatalf("setup: the traversal returns %d entries, want 14", len(keys))
		}
		return st, tbl, keys
	}

	// One growth, standing on each key; the second pass removes that key's entry first, as a
	// traversal may.
	for at := 0; at < 28; at++ {
		st, tbl, keys := build()
		here := keys[at%14]
		if at >= 14 {
			_ = st.rawSet(tbl, here, value.Nil)
		}
		growUnderTraversal(t, st, tbl, here, 64)
	}

	// A second growth later in the same traversal. The first one, to 16 slots while standing on
	// key 9, sends the traversal through the hash part again, so entries it has returned are
	// among those that follow; the rule holds from wherever it then stands.
	first := func() (*State, arena.GCRef, []value.Value) {
		st, tbl, _ := build()
		growUnderTraversal(t, st, tbl, num(9), 16)
		return st, tbl, follow(t, st, tbl, num(9))
	}
	_, _, rest := first()
	if len(rest) != 9 {
		t.Fatalf("setup: %d entries follow the first growth, want the 9 left in the hash part", len(rest))
	}
	for j := range rest {
		st, tbl, rest := first()
		growUnderTraversal(t, st, tbl, rest[j], 64)
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

// TestGrowArray_SlotsAreKeptUntilTheNextRehash: the slots growArray adds stay while new keys find
// room in the hash part, and a rehash sizes the array part anew from the integer keys the table
// holds. The table is the one the public NewTable makes: 8 hash nodes, no array part.
func TestGrowArray_SlotsAreKeptUntilTheNextRehash(t *testing.T) {
	num := func(n int) value.Value { return value.NumberValue(float64(n)) }
	asize := func(st *State, tbl arena.GCRef) uint32 { return object.TableASize(st.arena, tbl) }
	addStrings := func(st *State, tbl arena.GCRef, from, to int) {
		for i := from; i < to; i++ {
			if e := st.rawSet(tbl, st.makeStringValue(fmt.Sprintf("name%d", i)), num(i)); e != nil {
				t.Fatal(e)
			}
		}
	}

	// Empty slots: kept while the hash part has room, given up by the rehash.
	st := New()
	tbl := st.NewLibTable(0)
	st.growArray(tbl, 1000)
	rehashes := 0
	for i := 0; i < 40; i++ {
		before := object.TableNodeRef(st.arena, tbl)
		addStrings(st, tbl, i, i+1)
		if object.TableNodeRef(st.arena, tbl) == before {
			if got := asize(st, tbl); rehashes == 0 && got != 1000 {
				t.Fatalf("after %d string keys and no rehash the array part has %d slots, want 1000", i+1, got)
			}
			continue
		}
		rehashes++
		if got := asize(st, tbl); got != 0 {
			t.Fatalf("the rehash at string key %d left %d array slots for a table with no integer key", i+1, got)
		}
	}
	if rehashes == 0 {
		t.Fatal("setup: 40 string keys did not make the table rehash")
	}
	if object.TableHSize(st.arena, st.NewLibTable(0)) != 8 {
		t.Fatal("setup: NewLibTable(0) no longer makes 8 hash nodes")
	}
	// The first five of these keys fit without a rehash (how many do depends on their hashes: a
	// free node is only looked for when a key's main position is taken).
	st = New()
	tbl = st.NewLibTable(0)
	st.growArray(tbl, 1000)
	addStrings(st, tbl, 0, 5)
	if got := asize(st, tbl); got != 1000 {
		t.Errorf("after 5 string keys the array part has %d slots, want 1000", got)
	}

	// Sparsely filled slots: the rehash keeps 1..16, of which 10 are in use, and the entry at 600
	// goes to the hash part.
	st = New()
	tbl = st.NewLibTable(0)
	st.growArray(tbl, 1000)
	for i := 1; i <= 10; i++ {
		_ = st.rawSet(tbl, num(i), num(i))
	}
	_ = st.rawSet(tbl, num(600), num(600))
	addStrings(st, tbl, 0, 40)
	if got := asize(st, tbl); got != 16 {
		t.Errorf("after 1..10, 600 and 40 string keys the array part has %d slots, want 16", got)
	}
	for _, i := range []int{1, 10, 600} {
		if v := st.rawGet(tbl, num(i)); v != num(i) {
			t.Errorf("t[%d] reads %x after the rehash", i, uint64(v))
		}
	}

	// The key whose store makes the table rehash counts: with 1, 2, 5 and 6 in a table without a
	// hash part, storing 7 leaves 8 slots (five of 1..8 are in use), where the four keys alone
	// would leave 2.
	st = New()
	tbl = st.allocTable(0, 0)
	st.growArray(tbl, 6)
	for _, i := range []int{1, 2, 5, 6} {
		_ = st.rawSet(tbl, num(i), num(i))
	}
	if got := asize(st, tbl); got != 6 {
		t.Fatalf("setup: the array part has %d slots after stores inside it, want 6", got)
	}
	_ = st.rawSet(tbl, num(7), num(7))
	if got := asize(st, tbl); got != 8 {
		t.Errorf("after 1, 2, 5, 6 and the store of 7 the array part has %d slots, want 8", got)
	}

	// Filled slots: a rehash keeps an array part that the integer keys fill more than half.
	st = New()
	tbl = st.NewLibTable(0)
	st.growArray(tbl, 1000)
	for i := 1; i <= 1000; i++ {
		_ = st.rawSet(tbl, num(i), num(i))
	}
	addStrings(st, tbl, 0, 40)
	if got := asize(st, tbl); got < 1000 {
		t.Errorf("after the fill and 40 string keys the array part has %d slots, want at least 1000", got)
	}
	for _, i := range []int{1, 500, 1000} {
		if v := st.rawGet(tbl, num(i)); v != num(i) {
			t.Errorf("t[%d] reads %x after the fill and 40 string keys", i, uint64(v))
		}
	}
}
