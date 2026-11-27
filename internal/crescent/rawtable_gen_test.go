package crescent

import (
	"fmt"
	"testing"

	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// TestRawTable_SlotReuseAfterDeleteBumpsGen: gen-only inline fast paths (P3
// wasm emitGetGlobal, P4 native GETGLOBAL NodeHit) bake a node index and only
// re-verify gen, never NodeKey. If a deleted key's node is later handed to a
// different key while gen stays put, such a consumer reads the other key's
// value as if it were its own. Every path that changes which key a node holds
// must therefore bump gen (rawtable.go header contract).
func TestRawTable_SlotReuseAfterDeleteBumpsGen(t *testing.T) {
	st := New()
	tbl := st.allocTable(0, 4)
	key := st.makeStringValue("s")
	if e := st.rawSet(tbl, key, value.NumberValue(1)); e != nil {
		t.Fatal(e)
	}
	_, where, idx := st.rawGetWithLoc(tbl, key)
	if where != locNode {
		t.Fatalf("expected node slot, got %v", where)
	}
	gen0 := object.TableGen(st.arena, tbl)
	if e := st.rawSet(tbl, key, value.Nil); e != nil {
		t.Fatal(e)
	}
	// Insert other keys until something occupies the dead slot (or the table
	// rehashes, which also bumps gen).
	for i := 0; i < 64; i++ {
		other := st.makeStringValue(fmt.Sprintf("k%d", i))
		if e := st.rawSet(tbl, other, value.NumberValue(float64(i))); e != nil {
			t.Fatal(e)
		}
		if object.TableHSize(st.arena, tbl) <= idx {
			break
		}
		k := object.NodeKey(st.arena, tbl, idx)
		if k != value.Nil && !keyEqual(k, key) {
			if object.TableGen(st.arena, tbl) == gen0 {
				t.Fatalf("slot %d now holds another key but gen is still %d (gen-only consumers would read %v as %q)",
					idx, gen0, object.NodeVal(st.arena, tbl, idx), "s")
			}
			return
		}
	}
	if object.TableGen(st.arena, tbl) == gen0 {
		t.Fatalf("gen never bumped although %d inserts followed a delete", 64)
	}
}

// newHashTable allocates a table with an hsize-node hash part whose free-node
// cursor starts at the top, the state a rehash leaves behind. (A table fresh
// from allocTable has the cursor at 0, so its first collision rehashes.)
func newHashTable(st *State, hsize uint32) arena.GCRef {
	tbl := st.allocTable(0, hsize)
	object.SetTableLastFree(st.arena, tbl, hsize)
	return tbl
}

// keyWithMainPosition returns a string key that is not in tbl and whose main
// position in tbl's current hash part is pos.
func keyWithMainPosition(t *testing.T, st *State, tbl arena.GCRef, pos uint32, prefix string) value.Value {
	t.Helper()
	hmask := object.TableHSize(st.arena, tbl) - 1
	for i := 0; i < 4096; i++ {
		k := st.makeStringValue(fmt.Sprintf("%s%d", prefix, i))
		if st.hashValue(k)&hmask == pos && st.rawGet(tbl, k) == value.Nil {
			if _, where, _ := st.rawGetWithLoc(tbl, k); where == locNone {
				return k
			}
		}
	}
	t.Fatalf("no key with main position %d found", pos)
	return value.Nil
}

// TestRawTable_DeleteKeepsKeyAndNode: deleting a key leaves it in its node
// (a DEAD node), bumps gen once, and storing the key again puts the value back
// into the same node. A second delete of the already deleted key changes
// nothing and does not bump.
func TestRawTable_DeleteKeepsKeyAndNode(t *testing.T) {
	st := New()
	tbl := st.allocTable(0, 8)
	key := st.makeStringValue("s")
	if e := st.rawSet(tbl, key, value.NumberValue(1)); e != nil {
		t.Fatal(e)
	}
	_, where, idx := st.rawGetWithLoc(tbl, key)
	if where != locNode {
		t.Fatalf("expected node slot, got %v", where)
	}
	gen0 := object.TableGen(st.arena, tbl)
	if e := st.rawSet(tbl, key, value.Nil); e != nil {
		t.Fatal(e)
	}
	if k := object.NodeKey(st.arena, tbl, idx); !keyEqual(k, key) {
		t.Fatalf("node %d lost its key on delete: %x", idx, uint64(k))
	}
	if v := object.NodeVal(st.arena, tbl, idx); v != value.Nil {
		t.Fatalf("node %d still has a value after delete: %x", idx, uint64(v))
	}
	if v := st.rawGet(tbl, key); v != value.Nil {
		t.Fatalf("rawGet of a deleted key = %x, want nil", uint64(v))
	}
	if _, where, _ := st.rawGetWithLoc(tbl, key); where != locNone {
		t.Fatalf("a deleted key must have no IC location, got %v", where)
	}
	gen1 := object.TableGen(st.arena, tbl)
	if gen1 == gen0 {
		t.Fatalf("deleting a live key did not bump gen (%d)", gen0)
	}
	if e := st.rawSet(tbl, key, value.Nil); e != nil {
		t.Fatal(e)
	}
	if g := object.TableGen(st.arena, tbl); g != gen1 {
		t.Fatalf("deleting an already deleted key bumped gen: %d -> %d", gen1, g)
	}
	if e := st.rawSet(tbl, key, value.NumberValue(2)); e != nil {
		t.Fatal(e)
	}
	_, where, idx1 := st.rawGetWithLoc(tbl, key)
	if where != locNode || idx1 != idx {
		t.Fatalf("stored again, the key is at (%v, %d); want its old node %d", where, idx1, idx)
	}
	if g := object.TableGen(st.arena, tbl); g != gen1 {
		t.Fatalf("storing into the key's own dead node bumped gen: %d -> %d", gen1, g)
	}
}

// TestRawTable_DeadNodeTakeoverBumpsGen: a new key whose main position is a
// DEAD node takes the node over, and that changes which key the node holds, so
// gen must change at that moment -- not only when the old key was deleted. The
// node is made DEAD the way P3 wasm emitSetGlobal does it, by storing Nil into
// the value word with no bump, so that the takeover is the only thing between
// a gen-only consumer that baked the node for the old key and the new key's
// value.
func TestRawTable_DeadNodeTakeoverBumpsGen(t *testing.T) {
	st := New()
	tbl := st.allocTable(0, 8)
	key := st.makeStringValue("s")
	if e := st.rawSet(tbl, key, value.NumberValue(1)); e != nil {
		t.Fatal(e)
	}
	_, where, idx := st.rawGetWithLoc(tbl, key)
	if where != locNode {
		t.Fatalf("expected node slot, got %v", where)
	}
	st.nodeSetVal(tbl, idx, value.Nil)
	gen0 := object.TableGen(st.arena, tbl)
	hsize0 := object.TableHSize(st.arena, tbl)

	intruder := keyWithMainPosition(t, st, tbl, idx, "k")
	if e := st.rawSet(tbl, intruder, value.NumberValue(7)); e != nil {
		t.Fatal(e)
	}
	if object.TableHSize(st.arena, tbl) != hsize0 {
		t.Fatal("the insert rehashed; the takeover was not exercised")
	}
	if k := object.NodeKey(st.arena, tbl, idx); !keyEqual(k, intruder) {
		t.Fatalf("node %d holds %x; the key whose main position it is should have taken it over", idx, uint64(k))
	}
	if object.TableGen(st.arena, tbl) == gen0 {
		t.Fatalf("node %d changed its key but gen stayed %d", idx, gen0)
	}
	if v := st.rawGet(tbl, key); v != value.Nil {
		t.Fatalf("the old key reads %x after its node was taken over, want nil", uint64(v))
	}
	if v := st.rawGet(tbl, intruder); v != value.NumberValue(7) {
		t.Fatalf("the new key reads %x, want 7", uint64(v))
	}
}

// TestRawTable_DeadNodeIsNotAFreeNode: a key whose main position holds a live
// entry must not be placed into a DEAD node by the free-node search. Only the
// dead key itself, or a key whose main position the node is, may use it (5.1
// getfreepos skips every node that has a key). The DEAD node is the topmost
// one, the first the search looks at.
func TestRawTable_DeadNodeIsNotAFreeNode(t *testing.T) {
	st := New()
	tbl := newHashTable(st, 16)
	top := object.TableHSize(st.arena, tbl) - 1
	dead := keyWithMainPosition(t, st, tbl, top, "x")
	if e := st.rawSet(tbl, dead, value.NumberValue(1)); e != nil {
		t.Fatal(e)
	}
	if _, _, idx := st.rawGetWithLoc(tbl, dead); idx != top {
		t.Fatalf("the key is at node %d, want its main position %d", idx, top)
	}
	if e := st.rawSet(tbl, dead, value.Nil); e != nil {
		t.Fatal(e)
	}
	// Two keys sharing main position 0: the second needs a free node.
	first := keyWithMainPosition(t, st, tbl, 0, "a")
	if e := st.rawSet(tbl, first, value.NumberValue(2)); e != nil {
		t.Fatal(e)
	}
	second := keyWithMainPosition(t, st, tbl, 0, "b")
	if e := st.rawSet(tbl, second, value.NumberValue(3)); e != nil {
		t.Fatal(e)
	}
	if k := object.NodeKey(st.arena, tbl, top); !keyEqual(k, dead) {
		t.Fatalf("DEAD node %d was handed out by the free-node search: it now holds %x", top, uint64(k))
	}
	if object.TableHSize(st.arena, tbl) != top+1 {
		t.Fatal("the table rehashed; the free-node search was not exercised")
	}
	if _, _, idx := st.rawGetWithLoc(tbl, second); idx != top-1 {
		t.Fatalf("the colliding key is at node %d, want %d (the first free node below the DEAD one)", idx, top-1)
	}
	if v := st.rawGet(tbl, first); v != value.NumberValue(2) {
		t.Fatalf("first reads %x, want 2", uint64(v))
	}
	if v := st.rawGet(tbl, second); v != value.NumberValue(3) {
		t.Fatalf("second reads %x, want 3", uint64(v))
	}
}

// TestRawTable_TakeoverKeepsChain: a DEAD node in the middle of a collision
// chain keeps its link when a new key takes it over, so the entries behind it
// stay reachable.
func TestRawTable_TakeoverKeepsChain(t *testing.T) {
	st := New()
	tbl := newHashTable(st, 16)
	// a, b, c share main position 0: a sits at node 0 and the chain is
	// 0 -> c -> b (a new colliding key is linked right behind the main position).
	a := keyWithMainPosition(t, st, tbl, 0, "a")
	_ = st.rawSet(tbl, a, value.NumberValue(1))
	b := keyWithMainPosition(t, st, tbl, 0, "b")
	_ = st.rawSet(tbl, b, value.NumberValue(2))
	c := keyWithMainPosition(t, st, tbl, 0, "c")
	_ = st.rawSet(tbl, c, value.NumberValue(3))
	if object.TableHSize(st.arena, tbl) != 16 {
		t.Fatal("the table rehashed; rebuild the test for the current placement")
	}
	_, _, cIdx := st.rawGetWithLoc(tbl, c)
	_, _, bIdx := st.rawGetWithLoc(tbl, b)
	if object.NodeNext(st.arena, tbl, 0) != int32(cIdx) || object.NodeNext(st.arena, tbl, cIdx) != int32(bIdx) {
		t.Fatalf("chain is not 0 -> %d -> %d; rebuild the test for the current placement", cIdx, bIdx)
	}
	if e := st.rawSet(tbl, c, value.Nil); e != nil {
		t.Fatal(e)
	}
	// A key whose main position is c's node takes the DEAD node over.
	d := keyWithMainPosition(t, st, tbl, cIdx, "d")
	if e := st.rawSet(tbl, d, value.NumberValue(4)); e != nil {
		t.Fatal(e)
	}
	if k := object.NodeKey(st.arena, tbl, cIdx); !keyEqual(k, d) {
		t.Fatalf("node %d holds %x, want the key whose main position it is", cIdx, uint64(k))
	}
	for k, want := range map[value.Value]float64{a: 1, b: 2, d: 4} {
		if v := st.rawGet(tbl, k); v != value.NumberValue(want) {
			t.Fatalf("a live key reads %x, want %v", uint64(v), want)
		}
	}
	if v := st.rawGet(tbl, c); v != value.Nil {
		t.Fatalf("the deleted key reads %x, want nil", uint64(v))
	}
	// Every live key is still reached by a traversal, exactly once.
	seen := 0
	key := value.Nil
	for {
		k, _, ok, e := st.rawNext(tbl, key)
		if e != nil {
			t.Fatal(e)
		}
		if !ok {
			break
		}
		seen++
		key = k
	}
	if seen != 3 {
		t.Fatalf("traversal saw %d entries, want 3", seen)
	}
}

// TestRawTable_RehashDropsDeadNodes: a rehash sizes the new hash part for the live keys only, and
// leaves no DEAD node behind.
func TestRawTable_RehashDropsDeadNodes(t *testing.T) {
	st := New()
	tbl := st.allocTable(0, 0)
	for i := 0; i < 64; i++ {
		_ = st.rawSet(tbl, st.makeStringValue(fmt.Sprintf("k%d", i)), value.NumberValue(float64(i)))
	}
	for i := 4; i < 64; i++ {
		_ = st.rawSet(tbl, st.makeStringValue(fmt.Sprintf("k%d", i)), value.Nil)
	}
	live := uint32(4)
	hsize0 := object.TableHSize(st.arena, tbl)
	for i := 0; object.TableHSize(st.arena, tbl) == hsize0; i++ {
		if i == 1000 {
			t.Fatal("no rehash after 1000 inserts")
		}
		_ = st.rawSet(tbl, st.makeStringValue(fmt.Sprintf("n%d", i)), value.True)
		live++
	}
	// rehash: the smallest power of two that holds the live keys and leaves one node free.
	want := uint32(1)
	for want < live+1 {
		want <<= 1
	}
	hsize := object.TableHSize(st.arena, tbl)
	if hsize != want {
		t.Fatalf("after the rehash the hash part has %d nodes for %d live keys, want %d: deleted keys were counted", hsize, live, want)
	}
	for i := uint32(0); i < hsize; i++ {
		if object.NodeKey(st.arena, tbl, i) != value.Nil && object.NodeVal(st.arena, tbl, i) == value.Nil {
			t.Fatalf("node %d is DEAD right after a rehash", i)
		}
	}
	for i := 0; i < 4; i++ {
		if v := st.rawGet(tbl, st.makeStringValue(fmt.Sprintf("k%d", i))); v != value.NumberValue(float64(i)) {
			t.Fatalf("k%d reads %x after the rehash", i, uint64(v))
		}
	}
}

// TestRawNext_ContinuesFromDeletedKey: next(t, k) still finds k after
// t[k] = nil, for every key of a table that is cleared while it is walked; a
// key that never was in the table is still an invalid key.
func TestRawNext_ContinuesFromDeletedKey(t *testing.T) {
	st := New()
	tbl := st.allocTable(0, 8)
	for i := 0; i < 5; i++ {
		_ = st.rawSet(tbl, st.makeStringValue(fmt.Sprintf("k%d", i)), value.NumberValue(float64(i)))
	}
	_ = st.rawSet(tbl, value.NumberValue(1.5), value.True)
	_ = st.rawSet(tbl, value.True, value.NumberValue(9))
	n := 0
	key := value.Nil
	for {
		k, _, ok, e := st.rawNext(tbl, key)
		if e != nil {
			t.Fatalf("after %d entries: %v", n, e.Msg)
		}
		if !ok {
			break
		}
		n++
		if e := st.rawSet(tbl, k, value.Nil); e != nil {
			t.Fatal(e)
		}
		key = k
	}
	if n != 7 {
		t.Fatalf("visited %d entries, want 7", n)
	}
	if _, _, ok, _ := st.rawNext(tbl, value.Nil); ok {
		t.Fatal("the table is not empty after every entry was cleared")
	}
	if _, _, _, e := st.rawNext(tbl, st.makeStringValue("never")); e == nil || e.Msg != "invalid key to 'next'" {
		t.Fatalf("a key that never was in the table: %v", e)
	}
}

// TestWeak_SweepClearBumpsGen: the weak-table sweep clearing a dead entry is
// a key deletion by another producer, so it must bump gen for the same
// reason rawSet's delete path does (a gen-only inline consumer would keep
// reading the cleared slot, or whatever key later reuses it).
func TestWeak_SweepClearBumpsGen(t *testing.T) {
	st := New()
	th := st.newThread()
	st.runningThread = th
	defer func() { st.runningThread = nil }()

	weak := st.allocTable(0, 8)
	meta := st.allocTable(0, 8)
	modeKey := value.MakeGC(value.TagString, st.gc.Intern([]byte("__mode")))
	modeVal := value.MakeGC(value.TagString, st.gc.Intern([]byte("k")))
	if e := st.tableSet(meta, modeKey, modeVal); e != nil {
		t.Fatal(e)
	}
	st.SetMeta(weak, meta)

	deadKey := st.allocTable(0, 8)
	if e := st.tableSet(weak, value.MakeGC(value.TagTable, deadKey), value.NumberValue(42)); e != nil {
		t.Fatal(e)
	}
	th.push(value.MakeGC(value.TagTable, weak))
	gen0 := object.TableGen(st.arena, weak)

	st.gc.Collect()

	if _, _, ok, _ := st.rawNext(weak, value.Nil); ok {
		t.Fatalf("dead-key entry should have been cleared by the sweep")
	}
	if object.TableGen(st.arena, weak) == gen0 {
		t.Fatalf("weak sweep cleared an entry but gen stayed %d", gen0)
	}
}
