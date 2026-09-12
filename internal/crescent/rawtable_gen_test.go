package crescent

import (
	"fmt"
	"testing"

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
	tbl := st.allocTable(0, 4)
	key := st.makeStringValue("s")
	// Fill so that "s" ends up chained behind another key at its main
	// position; try several fillers since placement depends on hashes.
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
		t.Skip("could not construct a chained slot for the key")
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
