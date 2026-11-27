package crescent

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// The tests in this file pin what the collector does with DEAD nodes (a deleted key that stays in
// its node, see the rawtable.go header): the node does not keep the key's object alive, a key
// whose object dies is replaced by object.DeadKey before the sweep frees the object, and the
// node stays DEAD either way.

// gcTestState returns a State with a running thread whose stack roots whatever the test pushes.
func gcTestState(t *testing.T) (*State, *thread) {
	t.Helper()
	st := New()
	th := st.newThread()
	st.runningThread = th
	t.Cleanup(func() { st.runningThread = nil })
	return st, th
}

func nodeOf(t *testing.T, st *State, tbl arena.GCRef, key value.Value) uint32 {
	t.Helper()
	_, where, idx := st.rawGetWithLoc(tbl, key)
	if where != locNode {
		t.Fatalf("key %x is not a live key of the hash part", uint64(key))
	}
	return idx
}

func TestDeadKey_ObjectDiesAndKeyIsReplaced(t *testing.T) {
	st, th := gcTestState(t)
	holder := newHashTable(st, 8)
	th.push(value.MakeGC(value.TagTable, holder))

	key := value.MakeGC(value.TagTable, st.allocTable(0, 0))
	if e := st.rawSet(holder, key, value.NumberValue(1)); e != nil {
		t.Fatal(e)
	}
	idx := nodeOf(t, st, holder, key)
	if e := st.rawSet(holder, key, value.Nil); e != nil {
		t.Fatal(e)
	}
	// A weak-valued probe tells whether the key's object survives the collection.
	probe := st.allocTable(0, 8)
	meta := st.allocTable(0, 8)
	_ = st.tableSet(meta, st.makeStringValue("__mode"), st.makeStringValue("v"))
	st.SetMeta(probe, meta)
	_ = st.rawSet(probe, value.NumberValue(1), key)
	th.push(value.MakeGC(value.TagTable, probe))
	gen0 := object.TableGen(st.arena, holder)

	st.gc.Collect()

	if v := st.rawGet(probe, value.NumberValue(1)); v != value.Nil {
		t.Fatalf("the deleted key's object survived: a DEAD node kept it alive")
	}
	if k := object.NodeKey(st.arena, holder, idx); k != object.DeadKey {
		t.Fatalf("node %d still holds %x after its key's object died, want DeadKey", idx, uint64(k))
	}
	if v := object.NodeVal(st.arena, holder, idx); v != value.Nil {
		t.Fatalf("node %d has value %x, want nil", idx, uint64(v))
	}
	if object.TableGen(st.arena, holder) == gen0 {
		t.Fatalf("node %d changed its key but gen stayed %d", idx, gen0)
	}
	// The node is still DEAD, not free: a colliding key elsewhere must not be placed in it.
	top := object.TableHSize(st.arena, holder) - 1
	other := uint32(0)
	if idx == 0 {
		other = 1
	}
	a := keyWithMainPosition(t, st, holder, other, "a")
	_ = st.rawSet(holder, a, value.NumberValue(2))
	b := keyWithMainPosition(t, st, holder, other, "b")
	_ = st.rawSet(holder, b, value.NumberValue(3))
	if object.TableHSize(st.arena, holder) != top+1 {
		t.Fatal("the table rehashed; the free-node search was not exercised")
	}
	if k := object.NodeKey(st.arena, holder, idx); k != object.DeadKey {
		t.Fatalf("node %d was reused although it is DEAD: it holds %x", idx, uint64(k))
	}
	// A second collection leaves the placeholder alone and does not bump again.
	gen1 := object.TableGen(st.arena, holder)
	st.gc.Collect()
	if k := object.NodeKey(st.arena, holder, idx); k != object.DeadKey {
		t.Fatalf("node %d holds %x after a second collection, want DeadKey", idx, uint64(k))
	}
	if g := object.TableGen(st.arena, holder); g != gen1 {
		t.Fatalf("a collection that changed no key bumped gen: %d -> %d", gen1, g)
	}
}

func TestDeadKey_ReachableKeyStaysAndNextContinues(t *testing.T) {
	st, th := gcTestState(t)
	holder := newHashTable(st, 8)
	th.push(value.MakeGC(value.TagTable, holder))

	key := value.MakeGC(value.TagTable, st.allocTable(0, 0))
	th.push(key) // the traversal still holds the key
	_ = st.rawSet(holder, key, value.NumberValue(1))
	_ = st.rawSet(holder, st.makeStringValue("x"), value.NumberValue(2))
	_ = st.rawSet(holder, st.makeStringValue("y"), value.NumberValue(3))
	idx := nodeOf(t, st, holder, key)
	_ = st.rawSet(holder, key, value.Nil)
	gen0 := object.TableGen(st.arena, holder)

	st.gc.Collect()

	if k := object.NodeKey(st.arena, holder, idx); k != key {
		t.Fatalf("node %d holds %x; a deleted key that is still reachable must stay", idx, uint64(k))
	}
	if g := object.TableGen(st.arena, holder); g != gen0 {
		t.Fatalf("a collection that changed no key bumped gen: %d -> %d", gen0, g)
	}
	if _, _, _, e := st.rawNext(holder, key); e != nil {
		t.Fatalf("next from a deleted, still reachable key after a collection: %v", e.Msg)
	}
}

// A string key of a DEAD node in a strong table is the one kind the node does keep alive: an equal
// string could be created again later, and it must find its old node whether or not a collection
// ran in between.
func TestDeadKey_StringKeyIsKept(t *testing.T) {
	st, th := gcTestState(t)
	holder := newHashTable(st, 8)
	th.push(value.MakeGC(value.TagTable, holder))

	ref := st.gc.Intern([]byte("only-referenced-by-the-dead-node"))
	key := value.MakeGC(value.TagString, ref)
	_ = st.rawSet(holder, key, value.NumberValue(1))
	idx := nodeOf(t, st, holder, key)
	_ = st.rawSet(holder, key, value.Nil)

	st.gc.Collect()
	st.gc.Collect()

	if k := object.NodeKey(st.arena, holder, idx); k != key {
		t.Fatalf("node %d holds %x, want the deleted string key", idx, uint64(k))
	}
	if again := st.gc.Intern([]byte("only-referenced-by-the-dead-node")); again != ref {
		t.Fatalf("the string was collected and interned again at %x (was %x)", uint64(again), uint64(ref))
	}
	_ = st.rawSet(holder, key, value.NumberValue(2))
	if got := nodeOf(t, st, holder, key); got != idx {
		t.Fatalf("stored again, the string key is in node %d, want its old node %d", got, idx)
	}
}

func weakTable(st *State, th *thread, mode string) arena.GCRef {
	weak := newHashTable(st, 8)
	meta := st.allocTable(0, 8)
	_ = st.tableSet(meta, st.makeStringValue("__mode"), st.makeStringValue(mode))
	st.SetMeta(weak, meta)
	th.push(value.MakeGC(value.TagTable, weak))
	return weak
}

// Clearing a weak-valued entry removes the value and keeps the key, so next(t, key) still works:
// a traversal of a weak table survives a collection that clears the entry it stands on.
func TestWeak_ClearedValueKeepsKeyForNext(t *testing.T) {
	st, th := gcTestState(t)
	weak := weakTable(st, th, "v")
	key := value.NumberValue(1.5)
	_ = st.rawSet(weak, key, value.MakeGC(value.TagTable, st.allocTable(0, 0)))
	_ = st.rawSet(weak, value.NumberValue(2.5), value.NumberValue(7))
	idx := nodeOf(t, st, weak, key)
	gen0 := object.TableGen(st.arena, weak)

	st.gc.Collect()

	if v := st.rawGet(weak, key); v != value.Nil {
		t.Fatalf("the dead value was not cleared: %x", uint64(v))
	}
	if k := object.NodeKey(st.arena, weak, idx); k != key {
		t.Fatalf("node %d holds %x, want the key of the cleared entry", idx, uint64(k))
	}
	if object.TableGen(st.arena, weak) == gen0 {
		t.Fatalf("an entry was cleared but gen stayed %d", gen0)
	}
	if _, _, _, e := st.rawNext(weak, key); e != nil {
		t.Fatalf("next from the key of a cleared entry: %v", e.Msg)
	}
	if v := st.rawGet(weak, value.NumberValue(2.5)); v != value.NumberValue(7) {
		t.Fatalf("the other entry reads %x, want 7", uint64(v))
	}
}

// A weak-keyed entry whose key dies loses the key as well: nothing may refer to the freed object.
func TestWeak_ClearedKeyIsReplaced(t *testing.T) {
	st, th := gcTestState(t)
	weak := weakTable(st, th, "k")
	key := value.MakeGC(value.TagTable, st.allocTable(0, 0))
	_ = st.rawSet(weak, key, value.NumberValue(1))
	idx := nodeOf(t, st, weak, key)

	st.gc.Collect()

	if k := object.NodeKey(st.arena, weak, idx); k != object.DeadKey {
		t.Fatalf("node %d holds %x after its weak key died, want DeadKey", idx, uint64(k))
	}
	if v := object.NodeVal(st.arena, weak, idx); v != value.Nil {
		t.Fatalf("node %d has value %x, want nil", idx, uint64(v))
	}
}

// A key deleted from a weak table by the script is a DEAD node like any other; the weak-table
// pass replaces it once its object dies, whatever the table's mode.
func TestWeak_DeletedKeyIsReplacedWhenItsObjectDies(t *testing.T) {
	for _, mode := range []string{"k", "v", "kv"} {
		st, th := gcTestState(t)
		weak := weakTable(st, th, mode)
		key := value.MakeGC(value.TagTable, st.allocTable(0, 0))
		_ = st.rawSet(weak, key, value.NumberValue(1))
		idx := nodeOf(t, st, weak, key)
		_ = st.rawSet(weak, key, value.Nil)
		gen0 := object.TableGen(st.arena, weak)

		st.gc.Collect()

		if k := object.NodeKey(st.arena, weak, idx); k != object.DeadKey {
			t.Errorf("mode %q: node %d holds %x after the deleted key's object died, want DeadKey", mode, idx, uint64(k))
		}
		if object.TableGen(st.arena, weak) == gen0 {
			t.Errorf("mode %q: node %d changed its key but gen stayed %d", mode, idx, gen0)
		}
	}
}

// A weak table does not keep the string key of a DEAD node alive, as a strong table does
// (TestDeadKey_StringKeyIsKept): the string goes with the first collection that finds it
// unreferenced, and the key is replaced like that of any other dead object.
func TestWeak_DeletedStringKeyIsNotKept(t *testing.T) {
	for _, mode := range []string{"k", "v", "kv"} {
		st, th := gcTestState(t)
		weak := weakTable(st, th, mode)
		key := value.MakeGC(value.TagString, st.gc.Intern([]byte("only-referenced-by-the-dead-node")))
		_ = st.rawSet(weak, key, value.NumberValue(1))
		idx := nodeOf(t, st, weak, key)
		_ = st.rawSet(weak, key, value.Nil)
		gen0 := object.TableGen(st.arena, weak)

		st.gc.Collect()

		if k := object.NodeKey(st.arena, weak, idx); k != object.DeadKey {
			t.Errorf("mode %q: node %d holds %x after the deleted string died, want DeadKey", mode, idx, uint64(k))
		}
		if object.TableGen(st.arena, weak) == gen0 {
			t.Errorf("mode %q: node %d changed its key but gen stayed %d", mode, idx, gen0)
		}
	}
}

// The entries the collector clears are the DEAD nodes a weak-valued table mostly has. The string
// key of such an entry is a strong reference while the entry has its value, so it survives the
// collection that clears the entry; from then on only its other references keep it. A cache with
// string keys and weak values must not hold on to every key it ever had.
func TestWeak_ClearedEntryDoesNotKeepItsStringKey(t *testing.T) {
	for _, held := range []bool{false, true} {
		st, th := gcTestState(t)
		weak := weakTable(st, th, "v")
		key := value.MakeGC(value.TagString, st.gc.Intern([]byte("key-of-a-cleared-entry")))
		if held {
			th.push(key)
		}
		_ = st.rawSet(weak, key, value.MakeGC(value.TagTable, st.allocTable(0, 0)))
		idx := nodeOf(t, st, weak, key)

		st.gc.Collect()
		if v := object.NodeVal(st.arena, weak, idx); v != value.Nil {
			t.Fatalf("held %v: the dead value was not cleared: %x", held, uint64(v))
		}
		if k := object.NodeKey(st.arena, weak, idx); k != key {
			t.Fatalf("held %v: node %d holds %x right after its entry was cleared, want the key", held, idx, uint64(k))
		}

		st.gc.Collect()
		k := object.NodeKey(st.arena, weak, idx)
		if held {
			if k != key {
				t.Errorf("node %d holds %x; the key is still referenced and must stay", idx, uint64(k))
			}
			if _, _, _, e := st.rawNext(weak, key); e != nil {
				t.Errorf("next from the still referenced key of a cleared entry: %v", e.Msg)
			}
		} else if k != object.DeadKey {
			t.Errorf("node %d holds %x one collection after its entry was cleared, want DeadKey", idx, uint64(k))
		}
	}
}

// DeadKey must stay a value no lookup can match and no script can store.
func TestDeadKey_IsNotAKey(t *testing.T) {
	if keyEqual(object.DeadKey, object.DeadKey) {
		t.Fatal("DeadKey compares equal to itself: a lookup could match a DEAD node")
	}
	if value.IsCollectable(object.DeadKey) || object.DeadKey == value.Nil {
		t.Fatal("DeadKey must be neither collectable nor nil")
	}
	if e := checkKey(object.DeadKey); e == nil {
		t.Fatal("a script can store DeadKey as a table key")
	}
	if object.DeadKey != value.NumberValue(0/zero()) {
		t.Fatalf("DeadKey %x is not the canonical NaN %x", uint64(object.DeadKey), uint64(value.NumberValue(0/zero())))
	}
}

func zero() float64 { return 0 }
