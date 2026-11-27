package crescent

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// TestRawTable_ModelCheck drives one table with random stores, deletes, traversals and
// collections and compares it with a Go map after every step. The key universe is small, so
// keys are deleted and stored again, nodes go through every state (rawtable.go header) and
// chains pass through DEAD nodes that are taken over.
//
// Object keys (tables) are rooted only while the model holds them: a key the model has deleted
// may be dropped, and its object is then collectable although a DEAD node still holds it.
func TestRawTable_ModelCheck(t *testing.T) {
	for seed := int64(1); seed <= 40; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) { modelCheck(t, seed) })
	}
}

func modelCheck(t *testing.T, seed int64) {
	rng := rand.New(rand.NewSource(seed))
	st, th := gcTestState(t)
	tbl := st.allocTable(0, 0)
	th.push(value.MakeGC(value.TagTable, tbl))

	// Scalar keys: strings, non-integer and integer numbers, booleans.
	var universe []value.Value
	for i := 0; i < 24; i++ {
		k := st.makeStringValue(fmt.Sprintf("s%d", i))
		st.PinRef(value.GCRefOf(k)) // the test keeps using the string after the table has dropped it
		universe = append(universe, k)
	}
	for i := 0; i < 12; i++ {
		universe = append(universe, value.NumberValue(float64(i)+0.5), value.NumberValue(float64(i+1)))
	}
	universe = append(universe, value.True, value.False)
	// Object keys: pins[i] roots objKeys[i] while the model wants it alive.
	const nObj = 12
	objKeys := make([]value.Value, nObj)
	pins := make([]uint32, nObj)
	alive := make([]bool, nObj)
	newObj := func(i int) {
		ref := st.allocTable(0, 0)
		objKeys[i] = value.MakeGC(value.TagTable, ref)
		pins[i] = st.PinRef(ref)
		alive[i] = true
	}
	for i := range objKeys {
		newObj(i)
	}

	model := map[value.Value]value.Value{}
	check := func(step int, what string) {
		t.Helper()
		for k, want := range model {
			if got := st.rawGet(tbl, k); got != want {
				t.Fatalf("seed %d step %d (%s): key %x reads %x, model has %x", seed, step, what, uint64(k), uint64(got), uint64(want))
			}
		}
		seen := map[value.Value]bool{}
		key := value.Nil
		for {
			k, v, ok, e := st.rawNext(tbl, key)
			if e != nil {
				t.Fatalf("seed %d step %d (%s): next: %s", seed, step, what, e.Msg)
			}
			if !ok {
				break
			}
			if seen[k] {
				t.Fatalf("seed %d step %d (%s): traversal returned key %x twice", seed, step, what, uint64(k))
			}
			seen[k] = true
			if want, in := model[k]; !in || want != v {
				t.Fatalf("seed %d step %d (%s): traversal returned %x = %x, model has %x (present: %v)",
					seed, step, what, uint64(k), uint64(v), uint64(want), in)
			}
			key = k
		}
		if len(seen) != len(model) {
			t.Fatalf("seed %d step %d (%s): traversal saw %d entries, model has %d", seed, step, what, len(seen), len(model))
		}
		checkNodeInvariants(t, st, tbl)
	}

	pick := func() value.Value {
		if rng.Intn(4) == 0 {
			i := rng.Intn(nObj)
			if !alive[i] {
				newObj(i)
			}
			return objKeys[i]
		}
		return universe[rng.Intn(len(universe))]
	}
	for step := 0; step < 3000; step++ {
		switch r := rng.Intn(100); {
		case r < 45:
			k, v := pick(), value.NumberValue(float64(step))
			if e := st.rawSet(tbl, k, v); e != nil {
				t.Fatal(e.Msg)
			}
			model[k] = v
			check(step, "store")
		case r < 80:
			k := pick()
			if e := st.rawSet(tbl, k, value.Nil); e != nil {
				t.Fatal(e.Msg)
			}
			delete(model, k)
			check(step, "delete")
		case r < 88:
			// Drop an object key the model no longer holds: only a DEAD node may still refer to it.
			i := rng.Intn(nObj)
			if _, in := model[objKeys[i]]; alive[i] && !in {
				st.UnpinRef(pins[i])
				alive[i] = false
			}
		case r < 96:
			st.gc.Collect()
			check(step, "collect")
		default:
			// Clear the table while walking it, with a collection in the middle.
			n, want := 0, len(model)
			key := value.Nil
			for {
				k, _, ok, e := st.rawNext(tbl, key)
				if e != nil {
					t.Fatalf("seed %d step %d: next while clearing: %s", seed, step, e.Msg)
				}
				if !ok {
					break
				}
				h := st.gc.Push(k) // the traversal holds the key it continues from
				if e := st.rawSet(tbl, k, value.Nil); e != nil {
					t.Fatal(e.Msg)
				}
				delete(model, k)
				if n%3 == 0 {
					st.gc.Collect()
				}
				st.gc.Pop(h)
				n++
				key = k
			}
			if n != want {
				t.Fatalf("seed %d step %d: clearing visited %d entries, the table had %d", seed, step, n, want)
			}
			check(step, "clear")
		}
	}
}

// checkNodeInvariants checks the structure of the hash part: every node holding a key is reached
// from that key's main position, no chain has a cycle, no node refers to a freed object, and a
// FREE node has no chain link.
func checkNodeInvariants(t *testing.T, st *State, tbl arena.GCRef) {
	t.Helper()
	hsize := object.TableHSize(st.arena, tbl)
	if hsize == 0 {
		return
	}
	hmask := hsize - 1
	for i := uint32(0); i < hsize; i++ {
		k := object.NodeKey(st.arena, tbl, i)
		v := object.NodeVal(st.arena, tbl, i)
		next := object.NodeNext(st.arena, tbl, i)
		if next >= int32(hsize) {
			t.Fatalf("node %d links to %d, beyond the hash part (%d nodes)", i, next, hsize)
		}
		if k == value.Nil {
			if v != value.Nil || next >= 0 {
				t.Fatalf("node %d has no key but value %x / link %d", i, uint64(v), next)
			}
			continue
		}
		if value.IsCollectable(k) {
			if ot := object.OTypeOf(object.HeaderOf(st.arena, value.GCRefOf(k))); value.Tag(k) == value.TagString && ot != object.OBJ_STRING ||
				value.Tag(k) == value.TagTable && ot != object.OBJ_TABLE {
				t.Fatalf("node %d holds key %x whose object has type %d: it refers to freed or reused memory", i, uint64(k), ot)
			}
		}
		if k == object.DeadKey {
			if v != value.Nil {
				t.Fatalf("node %d holds DeadKey with value %x", i, uint64(v))
			}
			continue
		}
		// Reached from its main position within hsize steps.
		j, steps := int32(st.hashValue(k)&hmask), uint32(0)
		for j >= 0 && uint32(j) != i {
			j = object.NodeNext(st.arena, tbl, uint32(j))
			if steps++; steps > hsize {
				t.Fatalf("the chain from the main position of node %d's key has a cycle", i)
			}
		}
		if j < 0 {
			t.Fatalf("node %d (key %x, value %x) is not on the chain of its key's main position", i, uint64(k), uint64(v))
		}
	}
}
