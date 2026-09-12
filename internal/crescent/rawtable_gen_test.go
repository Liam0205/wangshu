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
// re-verify gen, never NodeKey. If a deleted key's slot is later handed to a
// different key while gen stays put, such a consumer reads the other key's
// value as if it were its own. Every path that changes which key a slot holds
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

// TestRawTable_DeleteReinsertMovesSlotBumpsGen is the exact #260 mechanism:
// deleting a chained key leaves its slot with key=Nil but next>=0, so
// re-inserting the same key does not find the main position "empty" and
// lands in a fresh slot from findFreeNode. The key->slot mapping changed,
// yet before the fix gen did not, so a gen-only consumer kept reading the
// dead slot (Nil) for a key that is present again.
func TestRawTable_DeleteReinsertMovesSlotBumpsGen(t *testing.T) {
	st := New()
	key := st.makeStringValue("s")
	// Fill so that "s" ends up chained behind another key at its main
	// position; placement depends on string hashes, so try growing filler
	// sets until one produces the shape. Not finding one is a failure, not
	// a skip: this is the direct unit pin for the #260 mechanism and must
	// not silently degrade if hashing or placement changes.
	var tbl arena.GCRef
	var idx0 uint32
	var placed bool
	for i := 0; i < 32 && !placed; i++ {
		tbl = st.allocTable(0, 4)
		for j := 0; j <= i; j++ {
			_ = st.rawSet(tbl, st.makeStringValue(fmt.Sprintf("f%d", j)), value.NumberValue(1))
		}
		if e := st.rawSet(tbl, key, value.NumberValue(1)); e != nil {
			t.Fatal(e)
		}
		_, where, idx := st.rawGetWithLoc(tbl, key)
		if where == locNode && object.NodeNext(st.arena, tbl, idx) >= 0 {
			idx0, placed = idx, true
		}
	}
	if !placed {
		t.Fatal("could not construct a chained slot for the key; rebuild the filler set for the current hash/placement")
	}
	gen0 := object.TableGen(st.arena, tbl)
	if e := st.rawSet(tbl, key, value.Nil); e != nil {
		t.Fatal(e)
	}
	if e := st.rawSet(tbl, key, value.NumberValue(2)); e != nil {
		t.Fatal(e)
	}
	_, where, idx1 := st.rawGetWithLoc(tbl, key)
	if where != locNode {
		t.Fatalf("key not in hash part after re-insert: %v", where)
	}
	if idx1 != idx0 && object.TableGen(st.arena, tbl) == gen0 {
		t.Fatalf("key moved from slot %d to %d but gen stayed %d", idx0, idx1, gen0)
	}
	if object.TableGen(st.arena, tbl) == gen0 {
		t.Fatalf("delete + re-insert left gen at %d", gen0)
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
