//go:build wangshu_p3 && wangshu_profile

package crescent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// TestP3_NodeHitOnATableAtAReusedAddress hands a compiled constant-key
// access a table that sits where the table it warmed up on used to be, and
// looks at the tables to tell which of the cases the checks on the frozen
// index decide. Table A has the fields k1..k8; an accessor of k8 is warmed up
// on it and compiled, A is dropped and collected, and B is built in its place
// with n fields j1..jn of which the first d are removed again. B never has k8.
//
// A case is decided by those checks when B has A's generation and the frozen
// index names a node of B that holds a value under another key: there only the
// comparison of the node's key sends the access to the helper. Whether B comes
// out that way depends on the hash function and on how nodes are handed out,
// so the test goes through the sizes and requires that for each access at
// least one of them does. test/difftest's TestTiered_TableAtAReusedAddress runs
// the same tables against lua5.1 and cannot see this.
func TestP3_NodeHitOnATableAtAReusedAddress(t *testing.T) {
	for _, op := range []string{"read", "store", "method"} {
		decided := []string{}
		for n := 1; n <= 12; n++ {
			for d := 0; d <= n; d++ {
				if p3TableReuseCase(t, op, n, d) {
					decided = append(decided, fmt.Sprintf("keys_%d_removed_%d", n, d))
				}
			}
		}
		if len(decided) == 0 {
			t.Errorf("%s: no size of the second table put a live field of another key where the frozen index points with the generation unchanged: the layout this test needs is gone", op)
		}
		t.Logf("%s: decided by the checks on the frozen index: %v", op, decided)
	}
}

// p3TableReuseCase runs one case and reports whether it is one the checks on
// the frozen index decide. The result of the access is checked either way.
func p3TableReuseCase(t *testing.T, op string, n, d int) bool {
	t.Helper()
	where := fmt.Sprintf("%s, %d keys, %d removed", op, n, d)
	st := New()
	runChunk(t, st, `
function mk362() local t = {} for i = 1, 8 do t["k" .. i] = i end return t end
A362 = mk362()
function read_362(t) return t.k8 end
function store_362(t, v) t.k8 = v end
function method_362(t) return t:k8() end
function inherited_362() return "inherited" end
MT362 = {__index = {k8 = inherited_362}}
R362 = false
for i = 1, 40 do read_362(A362) store_362(A362, 8) end
A362.k8 = inherited_362
for i = 1, 40 do method_362(A362) end`)

	global := func(name string) value.Value {
		v, _ := st.tableGet(st.globals, st.makeStringValue(name))
		return v
	}
	a := value.GCRefOf(global("A362"))
	pid := object.ClosureProtoID(st.arena, value.GCRefOf(global(op+"_362")))
	if !promoteProto(st, pid) {
		t.Fatalf("%s: the accessor was not compiled", where)
	}

	runChunk(t, st, `A362 = nil`)
	st.Collect()
	st.Collect()
	runChunk(t, st, fmt.Sprintf(`
B362 = {}
for i = 1, %d do B362["j" .. i] = i end
for i = 1, %d do B362["j" .. i] = nil end`, n, d))
	b := value.GCRefOf(global("B362"))
	if b != a {
		t.Fatalf("%s: the second table is at %#x, the first was at %#x", where, uint32(b), uint32(a))
	}
	if op == "method" {
		st.SetMeta(b, value.GCRefOf(global("MT362")))
	}

	decided := p3FrozenIndexNamesAnotherKey(st, pid, b, st.makeStringValue("k8"))

	before := st.GibbousEntries()
	call := map[string]string{
		"read":   `R362 = read_362(B362)`,
		"store":  `store_362(B362, 777) R362 = B362.k8`,
		"method": `R362 = method_362(B362)`,
	}[op]
	// With the frozen index unchecked the method call can fetch a number and
	// raise; that is this case's failure, and the other cases still run.
	if err := tryChunk(t, st, call); err != nil {
		t.Errorf("%s: the access raised: %v", where, err)
		return decided
	}
	if got := st.GibbousEntries() - before; got != 1 {
		t.Errorf("%s: the access entered compiled code %d times, want 1", where, got)
	}

	r := global("R362")
	switch op {
	case "read":
		if r != value.Nil {
			t.Errorf("%s: read %s, want nil", where, debugVal(st, r))
		}
	case "store":
		if !value.IsNumber(r) || value.AsNumber(r) != 777 {
			t.Errorf("%s: k8 after the store is %s, want 777", where, debugVal(st, r))
		}
	case "method":
		if !keyEqual(r, st.makeStringValue("inherited")) {
			t.Errorf("%s: the call returned %s, want the inherited method's result", where, debugVal(st, r))
		}
	}
	// The other fields are what the script left: j(d+1)..jn, each its own index.
	for i := 1; i <= n; i++ {
		v, _ := st.tableGet(b, st.makeStringValue(fmt.Sprintf("j%d", i)))
		switch {
		case i <= d && v != value.Nil:
			t.Errorf("%s: the removed field j%d is %s", where, i, debugVal(st, v))
		case i > d && (!value.IsNumber(v) || value.AsNumber(v) != float64(i)):
			t.Errorf("%s: the field j%d is %s, want %d", where, i, debugVal(st, v), i)
		}
	}
	return decided
}

// p3FrozenIndexNamesAnotherKey reports whether an inline cache of proto pid
// describes table b by address and generation while the node it names, inside
// b's hash part, holds a value under a key other than key.
func p3FrozenIndexNamesAnotherKey(st *State, pid uint32, b arena.GCRef, key value.Value) bool {
	p := st.protos[pid]
	for i := range p.IC {
		slot := &p.IC[i]
		if slot.Kind != bytecode.ICKindNodeHit || slot.TableRef != uint32(b) ||
			slot.Shape != object.TableGen(st.arena, b) {
			continue
		}
		k, v, ok := object.NodeIfInside(st.arena, b, slot.Index)
		if ok && v != value.Nil && !keyEqual(k, key) {
			return true
		}
	}
	return false
}

// TestP3_ArrayHitOnATableAtAReusedAddress is the same for the array part.
// Table A has eight array elements; an accessor of element 8, with the key a
// constant or a register, is warmed up on it and compiled, A is dropped and
// collected, and B, with n elements, is the table built afterwards that lands
// in its place.
//
// Here every B shorter than A has to be a case the check on the frozen index
// decides: B must be at A's address with A's generation, so that the guards
// on the table pass, and the frozen index must lie past B's array part. A B
// with a different generation would be turned away by the generation guard,
// the access would go to the helper, and the result would be right without
// the index check having been reached; so the test requires all of this for
// each case instead of asking for one.
func TestP3_ArrayHitOnATableAtAReusedAddress(t *testing.T) {
	for _, op := range []string{"read_constant_key", "read_register_key", "store_constant_key", "store_register_key"} {
		for n := 0; n <= 8; n++ {
			p3ArrayReuseCase(t, op, n)
		}
	}
}

func p3ArrayReuseCase(t *testing.T, op string, n int) {
	t.Helper()
	// Which freed block the next table head gets is the allocator's business:
	// it hands out the block of that size freed last. Here that is not A's
	// head. The State keeps one word per loaded proto in a segment it
	// reallocates whenever a chunk is loaded; six protos are loaded by the
	// time B's chunk is, so the segment freed then is as large as a table
	// head, and the first head allocated afterwards takes it. Each attempt
	// starts over and puts one more empty table in front of B, until B's head
	// is where A's was.
	for pad := 0; pad <= 16; pad++ {
		if p3ArrayReuseAttempt(t, op, n, pad) {
			return
		}
	}
	t.Errorf("%s, %d elements: with up to 16 tables allocated in front of it the second table never landed where the first was", op, n)
}

// p3ArrayReuseAttempt runs the case with pad empty tables allocated before B
// and reports whether B landed at A's address; if it did not, nothing else is
// checked.
func p3ArrayReuseAttempt(t *testing.T, op string, n, pad int) (landed bool) {
	t.Helper()
	where := fmt.Sprintf("%s, %d elements", op, n)
	// A store through an unchecked index lands in whatever follows B's array
	// part, which can be the head of the next table; reading that table back
	// then fails inside the arena. That is this case's failure, on its own
	// State, and the other cases still run.
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("%s: reading the tables back panicked: %v", where, r)
			landed = true
		}
	}()
	st := New()
	runChunk(t, st, `
A362 = {1, 2, 3, 4, 5, 6, 7, 8}
function read_constant_key_362(t) return t[8] end
function store_constant_key_362(t, v) t[8] = v end
function read_register_key_362(t, i) return t[i] end
function store_register_key_362(t, i, v) t[i] = v end
B362, N362, R362, K362 = false, false, false, {}
for i = 1, 40 do
  read_constant_key_362(A362) store_constant_key_362(A362, 8)
  read_register_key_362(A362, 8) store_register_key_362(A362, 8, 8)
end`)

	global := func(name string) value.Value {
		v, _ := st.tableGet(st.globals, st.makeStringValue(name))
		return v
	}
	a := value.GCRefOf(global("A362"))
	pid := object.ClosureProtoID(st.arena, value.GCRefOf(global(op+"_362")))
	if !promoteProto(st, pid) {
		t.Fatalf("%s: the accessor was not compiled", where)
	}

	runChunk(t, st, `A362 = nil`)
	st.Collect()
	st.Collect()
	items := make([]string, n)
	for i := range items {
		items[i] = fmt.Sprint((i + 1) * 11)
	}
	runChunk(t, st, fmt.Sprintf(`
for i = 1, %d do K362[i] = {} end
B362 = {%s}
N362 = {901, 902, 903, 904, 905, 906, 907, 908}`, pad, strings.Join(items, ", ")))
	b := value.GCRefOf(global("B362"))
	if b != a {
		return false
	}

	// The cache the accessor was compiled with: B by address and generation,
	// and an index that is inside B only when B has all eight elements.
	asize := object.TableASize(st.arena, b)
	if asize != uint32(n) {
		t.Fatalf("%s: the array part of the second table has %d slots", where, asize)
	}
	described := false
	for i := range st.protos[pid].IC {
		slot := &st.protos[pid].IC[i]
		if slot.Kind == bytecode.ICKindArrayHit && slot.TableRef == uint32(b) &&
			slot.Shape == object.TableGen(st.arena, b) && slot.Index == 7 {
			described = true
		}
	}
	if !described {
		t.Errorf("%s: no inline cache of the accessor names the second table's address and generation with index 7: the access is turned away before the index check", where)
	}

	before := st.GibbousEntries()
	call := map[string]string{
		"read_constant_key":  `R362 = read_constant_key_362(B362)`,
		"read_register_key":  `R362 = read_register_key_362(B362, 8)`,
		"store_constant_key": `store_constant_key_362(B362, 777) R362 = B362[8]`,
		"store_register_key": `store_register_key_362(B362, 8, 777) R362 = B362[8]`,
	}[op]
	if err := tryChunk(t, st, call); err != nil {
		t.Errorf("%s: the access raised: %v", where, err)
		return true
	}
	if got := st.GibbousEntries() - before; got != 1 {
		t.Errorf("%s: the access entered compiled code %d times, want 1", where, got)
	}

	r := global("R362")
	switch {
	case strings.HasPrefix(op, "store"):
		if !value.IsNumber(r) || value.AsNumber(r) != 777 {
			t.Errorf("%s: element 8 after the store is %s, want 777", where, debugVal(st, r))
		}
	case n == 8:
		if !value.IsNumber(r) || value.AsNumber(r) != 88 {
			t.Errorf("%s: read %s, want 88", where, debugVal(st, r))
		}
	case r != value.Nil:
		t.Errorf("%s: read %s, want nil", where, debugVal(st, r))
	}
	// The elements of B the script wrote, and the table allocated after it.
	for i := 1; i <= n && i < 8; i++ {
		v, _ := st.tableGet(b, value.NumberValue(float64(i)))
		if !value.IsNumber(v) || value.AsNumber(v) != float64(i*11) {
			t.Errorf("%s: element %d of the second table is %s, want %d", where, i, debugVal(st, v), i*11)
		}
	}
	nb := value.GCRefOf(global("N362"))
	for i := 1; i <= 8; i++ {
		v, _ := st.tableGet(nb, value.NumberValue(float64(i)))
		if !value.IsNumber(v) || value.AsNumber(v) != float64(900+i) {
			t.Errorf("%s: element %d of the table allocated after it is %s, want %d", where, i, debugVal(st, v), 900+i)
		}
	}
	return true
}

// TestP3_InlineAccessStaysInlineOnItsOwnTable is the other side of the checks
// on the frozen index: on the table an accessor was warmed up on they have to
// pass, or every access would quietly go to the helper and still return the
// right value. For a string key that rests on the constant in the State's own
// copy of the Proto being the very reference the table holds as the key.
//
// A helper that is reached does its lookup through the accessor's inline cache
// and fills it again when it is empty; the compiled code carries its own copy
// of the cache and never touches the Proto's. So the test empties the Proto's
// caches after the accessor is compiled, calls it a hundred times, and requires
// them to be empty still.
func TestP3_InlineAccessStaysInlineOnItsOwnTable(t *testing.T) {
	cases := []struct{ name, fn, call string }{
		{"string_key_read", `function f362(t) return t.k8 end`, `f362(A362)`},
		{"string_key_store", `function f362(t) t.k8 = 5 end`, `f362(A362)`},
		{"method_lookup", `function f362(t) return t:m8() end`, `f362(A362)`},
		{"long_string_key_read", `function f362(t) return t.a_key_that_is_longer_than_forty_bytes_so_that_it_is_not_short end`, `f362(A362)`},
		{"number_key_read", `function f362(t) return t[1.5] end`, `f362(A362)`},
		{"negative_zero_key_read", `function f362(t) return t[-0] end`, `f362(A362)`},
		{"array_constant_key_read", `function f362(t) return t[3] end`, `f362(A362)`},
		{"array_constant_key_store", `function f362(t) t[3] = 5 end`, `f362(A362)`},
		{"array_register_key_read", `function f362(t, i) return t[i] end`, `f362(A362, 3)`},
		{"array_register_key_store", `function f362(t, i) t[i] = 5 end`, `f362(A362, 3)`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := New()
			runChunk(t, st, c.fn+`
A362 = {11, 22, 33, 44, k8 = 8, m8 = function() return 8 end,
  a_key_that_is_longer_than_forty_bytes_so_that_it_is_not_short = 1, [1.5] = 15, [0] = 100}
for i = 1, 40 do `+c.call+` end`)
			v, _ := st.tableGet(st.globals, st.makeStringValue("f362"))
			pid := object.ClosureProtoID(st.arena, value.GCRefOf(v))
			filled := 0
			for i := range st.protos[pid].IC {
				if k := st.protos[pid].IC[i].Kind; k == bytecode.ICKindArrayHit || k == bytecode.ICKindNodeHit {
					filled++
				}
			}
			if filled != 1 {
				t.Fatalf("the accessor has %d filled table caches after the warm-up, want 1", filled)
			}
			if !promoteProto(st, pid) {
				t.Fatal("the accessor was not compiled")
			}
			for i := range st.protos[pid].IC {
				st.protos[pid].IC[i] = bytecode.ICSlot{}
			}

			before := st.GibbousEntries()
			runChunk(t, st, `for i = 1, 100 do `+c.call+` end`)
			if got := st.GibbousEntries() - before; got != 100 {
				t.Errorf("the accessor entered compiled code %d times, want 100", got)
			}
			for i := range st.protos[pid].IC {
				if slot := st.protos[pid].IC[i]; slot != (bytecode.ICSlot{}) {
					t.Errorf("the inline cache at pc %d was filled again (%+v): the access went to the helper", i, slot)
				}
			}
		})
	}
}
