package crescent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/frontend/compile"
	"github.com/Liam0205/wangshu/internal/frontend/lex"
	"github.com/Liam0205/wangshu/internal/frontend/parse"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// runChunk compiles src and runs it on st.
func runChunk(t *testing.T, st *State, src string) {
	t.Helper()
	if err := tryChunk(t, st, src); err != nil {
		t.Fatalf("call: %v", err)
	}
}

// tryChunk compiles src and runs it on st, returning the error the run raised.
func tryChunk(t *testing.T, st *State, src string) error {
	t.Helper()
	lx := lex.New([]byte(src), "test")
	block, err := parse.Parse(lx, "test")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	mainID, protos, err := compile.Compile(block, "test")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	_, err = st.Call(st.LoadProgram(mainID, protos), nil, 0)
	return err
}

// fields returns "p1 = 1, p2 = 2, ..." with n fields.
func fields(prefix string, n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = fmt.Sprintf("%s%d = %d", prefix, i+1, i+1)
	}
	return strings.Join(parts, ", ")
}

// TestIC_NodeHitOnATableAtAReusedAddress: an inline cache filled on a table
// that has since been collected must not be used on the table allocated in its
// place. The chunk fills the caches of a read and a store of one field on a
// table A and drops A; after a collection build() makes B, with fewer fields,
// and C, which has the field. B lands at A's address and has A's generation;
// when C's node block follows B's, the node index the caches hold, counted
// from B's node block, addresses C's field.
//
// Whether the blocks fall that way depends on the sizes, so the test goes
// through a few and requires that at least one produces the situation.
func TestIC_NodeHitOnATableAtAReusedAddress(t *testing.T) {
	produced := 0
	for na := 2; na <= 8; na++ {
		for nb := 0; nb <= 4; nb++ {
			for nc := 0; nc <= 4; nc++ {
				if icReuseCase(t, na, nb, nc) {
					produced++
				}
			}
		}
	}
	if produced == 0 {
		t.Fatal("no combination of sizes put another table's node where a stale cache points: the layout this test needs is gone")
	}
	t.Logf("%d combinations of sizes produced the situation", produced)
}

// icReuseCase runs one combination: A has na fields, B nb, and C the field
// plus nc more. It reports whether the stale caches pointed at C's field, and
// checks the accesses either way.
func icReuseCase(t *testing.T, na, nb, nc int) bool {
	t.Helper()
	field := fmt.Sprintf("a%d", na)
	st := New()
	runChunk(t, st, fmt.Sprintf(`
B, C, r1, r2, r3 = false, false, false, false, false
function get_r(t) return t.%[1]s end
function set_r(t, v) t.%[1]s = v end
function build() B = {%[3]s} C = {%[1]s = "other table", %[4]s} end
function check()
  r1 = get_r(B)
  set_r(B, 777)
  r2 = B.%[1]s
  r3 = C.%[1]s
end
do
  local A = {%[2]s}
  get_r(A) set_r(A, %[5]d)
end
`, field, fields("a", na), fields("b", nb), fields("c", nc), na))
	st.Collect()
	st.Collect()

	global := func(name string) value.Value {
		v, _ := st.tableGet(st.globals, st.makeStringValue(name))
		return v
	}
	call := func(name string) {
		t.Helper()
		if _, err := st.Call(value.GCRefOf(global(name)), nil, 0); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	call("build")
	b := value.GCRefOf(global("B"))
	key := st.makeStringValue(field)

	// The situation: caches name B's address and generation with a node
	// index outside B, where a read that does not look at B's size finds the
	// field with a value.
	stale := 0
	for _, p := range st.protos {
		for i := range p.IC {
			slot := &p.IC[i]
			if slot.Kind == bytecode.ICKindNodeHit && slot.TableRef == uint32(b) &&
				slot.Shape == object.TableGen(st.arena, b) &&
				slot.Index >= object.TableHSize(st.arena, b) &&
				keyEqual(object.NodeKey(st.arena, b, slot.Index), key) &&
				object.NodeVal(st.arena, b, slot.Index) != value.Nil {
				stale++
			}
		}
	}

	call("check")
	where := fmt.Sprintf("A with %d fields, B with %d, C with %d more (stale caches: %d)", na, nb, nc, stale)
	if v := global("r1"); v != value.Nil {
		t.Errorf("%s: B.%s read %s, want nil", where, field, debugVal(st, v))
	}
	if v := global("r2"); !value.IsNumber(v) || value.AsNumber(v) != 777 {
		t.Errorf("%s: B.%s after the store is %s, want 777", where, field, debugVal(st, v))
	}
	if v := global("r3"); !keyEqual(v, st.makeStringValue("other table")) {
		t.Errorf("%s: C.%s after the store to B is %s, want its own value", where, field, debugVal(st, v))
	}
	return stale >= 2 // those of get_r and set_r; the constructor of A leaves a third on the same field
}
