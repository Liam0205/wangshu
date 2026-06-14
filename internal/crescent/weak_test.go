// Weak table GC tests(06 §8.4 / 07 §13)。
package crescent

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// TestWeak_ValueModeCleared:__mode="v" 的表,GC 后不可达的值条目被清。
func TestWeak_ValueModeCleared(t *testing.T) {
	st := New()
	th := st.newThread()
	st.runningThread = th
	defer func() { st.runningThread = nil }()

	// weak 表 + 元表
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

	// 放一个"不可达"的表为值(只有弱表引用它)
	dead := st.allocTable(0, 8)
	k1 := value.NumberValue(1)
	if e := st.tableSet(weak, k1, value.MakeGC(value.TagTable, dead)); e != nil {
		t.Fatalf("set weak[1]: %v", e)
	}
	// 放一个"可达"的值(栈上引用)
	live := st.allocTable(0, 8)
	th.push(value.MakeGC(value.TagTable, live))
	k2 := value.NumberValue(2)
	if e := st.tableSet(weak, k2, value.MakeGC(value.TagTable, live)); e != nil {
		t.Fatalf("set weak[2]: %v", e)
	}
	// 弱表自身从栈可达
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

// TestWeak_KeyModeCleared:__mode="k" 的表,键死则条目清。
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

	// 死键条目应被清:rawNext 应该一个都遍历不到
	_, _, ok, _ := st.rawNext(weak, value.Nil)
	if ok {
		t.Errorf("weak-key table should be empty after GC (key died)")
	}
}

// TestWeak_StrongTableKeepsAll:无 __mode 的表完全不受影响。
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
