// Weak table GC tests (06 §8.4 / 07 §13).
package crescent

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// TestWeak_ValueModeCleared: for a table with __mode="v", unreachable value entries are cleared after GC.
func TestWeak_ValueModeCleared(t *testing.T) {
	st := New()
	th := st.newThread()
	st.runningThread = th
	defer func() { st.runningThread = nil }()

	// weak table + metatable
	weak := st.allocTable(0, 8)
	meta := st.allocTable(0, 8)
	modeKey := value.MakeGC(value.TagString, st.gc.Intern([]byte("__mode")))
	modeVal := value.MakeGC(value.TagString, st.gc.Intern([]byte("v")))
	if e := st.tableSet(meta, modeKey, modeVal); e != nil {
		t.Fatalf("set __mode: %v", e)
	}
	st.SetMeta(weak, meta)
	if object.TableWeakMode(st.arena, weak) != 'v' {
		t.Fatalf("weak mode = %c, want v", object.TableWeakMode(st.arena, weak))
	}

	// Put an "unreachable" table as a value (only the weak table references it)
	dead := st.allocTable(0, 8)
	k1 := value.NumberValue(1)
	if e := st.tableSet(weak, k1, value.MakeGC(value.TagTable, dead)); e != nil {
		t.Fatalf("set weak[1]: %v", e)
	}
	// Put a "reachable" value (referenced from the stack)
	live := st.allocTable(0, 8)
	th.push(value.MakeGC(value.TagTable, live))
	k2 := value.NumberValue(2)
	if e := st.tableSet(weak, k2, value.MakeGC(value.TagTable, live)); e != nil {
		t.Fatalf("set weak[2]: %v", e)
	}
	// The weak table itself is reachable from the stack
	th.push(value.MakeGC(value.TagTable, weak))

	st.gc.Collect()

	v1, _ := st.tableGet(weak, k1)
	v2, _ := st.tableGet(weak, k2)
	if v1 != value.Nil {
		t.Errorf("weak[1] should be cleared after GC (dead value), got %x", v1)
	}
	if v2 == value.Nil {
		t.Errorf("weak[2] should survive (live value on stack)")
	}
}

// TestWeak_KeyModeCleared: for a table with __mode="k", an entry is cleared when its key dies.
func TestWeak_KeyModeCleared(t *testing.T) {
	st := New()
	th := st.newThread()
	st.runningThread = th
	defer func() { st.runningThread = nil }()

	weak := st.allocTable(0, 8)
	meta := st.allocTable(0, 8)
	modeKey := value.MakeGC(value.TagString, st.gc.Intern([]byte("__mode")))
	modeVal := value.MakeGC(value.TagString, st.gc.Intern([]byte("k")))
	_ = st.tableSet(meta, modeKey, modeVal)
	st.SetMeta(weak, meta)

	deadKey := st.allocTable(0, 8)
	_ = st.tableSet(weak, value.MakeGC(value.TagTable, deadKey), value.NumberValue(42))
	th.push(value.MakeGC(value.TagTable, weak))

	st.gc.Collect()

	// The dead-key entry should be cleared: rawNext should not reach any entry
	_, _, ok, _ := st.rawNext(weak, value.Nil)
	if ok {
		t.Errorf("weak-key table should be empty after GC (key died)")
	}
}

// TestWeak_StrongTableKeepsAll: a table without __mode is completely unaffected.
func TestWeak_StrongTableKeepsAll(t *testing.T) {
	st := New()
	th := st.newThread()
	st.runningThread = th
	defer func() { st.runningThread = nil }()

	strong := st.allocTable(0, 8)
	inner := st.allocTable(0, 8)
	_ = st.tableSet(strong, value.NumberValue(1), value.MakeGC(value.TagTable, inner))
	th.push(value.MakeGC(value.TagTable, strong))

	st.gc.Collect()

	v, _ := st.tableGet(strong, value.NumberValue(1))
	if v == value.Nil {
		t.Errorf("strong table entry must survive GC")
	}
}
