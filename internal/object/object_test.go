package object

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/value"
)

func TestHeaderRoundTrip(t *testing.T) {
	h := MakeHeader(OBJ_TABLE, ColorGray, true, 0xA, arena.GCRef(0x1234567890))
	if OTypeOf(h) != OBJ_TABLE {
		t.Errorf("otype: %d", OTypeOf(h))
	}
	if ColorOf(h) != ColorGray {
		t.Errorf("color: %d", ColorOf(h))
	}
	if !IsFixed(h) {
		t.Errorf("fixed lost")
	}
	if !HasGCNext(h) {
		t.Errorf("hasGCNext lost")
	}
	if FlagsOf(h) != 0xA {
		t.Errorf("flags: %#x", FlagsOf(h))
	}
	if GCNextOf(h) != arena.GCRef(0x1234567890) {
		t.Errorf("gcnext: %#x", uint64(GCNextOf(h)))
	}
	// 字段更新。
	h2 := SetColor(h, ColorBlack)
	if ColorOf(h2) != ColorBlack || OTypeOf(h2) != OBJ_TABLE || GCNextOf(h2) != GCNextOf(h) {
		t.Errorf("SetColor mutated other fields")
	}
	h3 := SetGCNext(h, 0)
	if HasGCNext(h3) || GCNextOf(h3) != 0 {
		t.Errorf("SetGCNext(0) failed: hasNext=%v next=%#x", HasGCNext(h3), uint64(GCNextOf(h3)))
	}
}

func TestStringLayout(t *testing.T) {
	a := arena.New(arena.Options{})
	hello := []byte("hello, world")
	ref := AllocString(a, hello, 0xCAFEBABE)
	if OTypeOf(HeaderOf(a, ref)) != OBJ_STRING {
		t.Fatalf("otype")
	}
	if StringHash(a, ref) != 0xCAFEBABE {
		t.Errorf("hash: %#x", StringHash(a, ref))
	}
	if StringLen(a, ref) != uint32(len(hello)) {
		t.Errorf("len: %d", StringLen(a, ref))
	}
	if string(StringBytes(a, ref)) != string(hello) {
		t.Errorf("content: %q", StringBytes(a, ref))
	}
	// 字数公式(06 §1.3)。
	wantWords := stringWords(uint32(len(hello)))
	if wantWords != 2+(uint32(len(hello))+1+7)/8 {
		t.Errorf("formula mismatch: %d", wantWords)
	}
	// NUL 终止(便于 C 互操作,不计 len)。
	off := uint32(ref) + strDataIdx*8 + uint32(len(hello))
	if a.Bytes()[off] != 0 {
		t.Errorf("missing NUL terminator at off %d", off)
	}

	// 比较两个内容相同的串(不同 GCRef,intern 之前的状态)
	r2 := AllocString(a, hello, 0xCAFEBABE)
	if r2 == ref {
		t.Fatalf("two allocations should produce distinct refs (intern is M5's job)")
	}
	if !StringEqual(a, ref, r2) {
		t.Errorf("StringEqual on identical content failed")
	}
	r3 := AllocString(a, []byte("hellz"), 0)
	if StringEqual(a, ref, r3) {
		t.Errorf("StringEqual mismatched on different content")
	}
}

func TestTableLayout(t *testing.T) {
	a := arena.New(arena.Options{})
	tbl := AllocTable(a, 4, 8)
	if OTypeOf(HeaderOf(a, tbl)) != OBJ_TABLE {
		t.Fatalf("otype")
	}
	if TableASize(a, tbl) != 4 {
		t.Errorf("asize: %d", TableASize(a, tbl))
	}
	if TableHMask(a, tbl) != 7 {
		t.Errorf("hmask: %d", TableHMask(a, tbl))
	}
	// 数组段初值全 Nil。
	for i := uint32(0); i < 4; i++ {
		if TableArrayAt(a, tbl, i) != value.Nil {
			t.Errorf("array[%d] not Nil", i)
		}
	}
	SetTableArrayAt(a, tbl, 0, value.NumberValue(42))
	if v := TableArrayAt(a, tbl, 0); !value.IsNumber(v) || value.AsNumber(v) != 42 {
		t.Errorf("array round trip")
	}
	// 哈希节点初值。
	for i := uint32(0); i < 8; i++ {
		if NodeKey(a, tbl, i) != value.Nil || NodeVal(a, tbl, i) != value.Nil {
			t.Errorf("node[%d] not Nil", i)
		}
		if NodeNext(a, tbl, i) != -1 {
			t.Errorf("node[%d] next %d", i, NodeNext(a, tbl, i))
		}
	}
	SetNode(a, tbl, 3, value.NumberValue(7), value.True, 5)
	if k := NodeKey(a, tbl, 3); !value.IsNumber(k) || value.AsNumber(k) != 7 {
		t.Errorf("node key")
	}
	if NodeNext(a, tbl, 3) != 5 {
		t.Errorf("node next")
	}
	// gen 与 metatable。
	if TableGen(a, tbl) != 0 {
		t.Errorf("initial gen != 0")
	}
	mt := AllocTable(a, 0, 0)
	SetTableMeta(a, tbl, mt)
	if TableMetaRef(a, tbl) != mt {
		t.Errorf("metaRef")
	}
	if !TableHasMeta(a, tbl) {
		t.Errorf("hasMeta flag")
	}
	if TableGen(a, tbl) != 1 {
		t.Errorf("setMeta should bump gen, got %d", TableGen(a, tbl))
	}
	BumpGen(a, tbl)
	if TableGen(a, tbl) != 2 {
		t.Errorf("BumpGen failed")
	}
}

func TestClosureLayout(t *testing.T) {
	a := arena.New(arena.Options{})
	lc := AllocLuaClosure(a, 17, 3)
	if IsHostClosure(a, lc) {
		t.Errorf("Lua closure misclassified as host")
	}
	if ClosureProtoID(a, lc) != 17 {
		t.Errorf("protoID: %d", ClosureProtoID(a, lc))
	}
	if ClosureNUpvals(a, lc) != 3 {
		t.Errorf("nupvals: %d", ClosureNUpvals(a, lc))
	}
	uv := AllocClosedUpvalue(a, value.NumberValue(99))
	SetClosureUpvalRef(a, lc, 1, uv)
	if ClosureUpvalRef(a, lc, 1) != uv {
		t.Errorf("upvalRef")
	}

	hc := AllocHostClosure(a, 5, 2)
	if !IsHostClosure(a, hc) {
		t.Errorf("Host closure misclassified")
	}
	SetHostClosureUpval(a, hc, 0, value.True)
	if HostClosureUpval(a, hc, 0) != value.True {
		t.Errorf("host upval")
	}
}

func TestUpvalueOpenChain(t *testing.T) {
	a := arena.New(arena.Options{})
	th := arena.GCRef(0x1000) // 假 threadRef,不需真 thread
	tail := AllocOpenUpvalue(a, th, 5, 0)
	mid := AllocOpenUpvalue(a, th, 7, tail)
	head := AllocOpenUpvalue(a, th, 9, mid) // 降序链:9 -> 7 -> 5
	if UpvalIsClosed(a, head) || UpvalIsClosed(a, mid) || UpvalIsClosed(a, tail) {
		t.Fatalf("open upvals reported closed")
	}
	if UpvalStackIdx(a, head) != 9 || UpvalStackIdx(a, mid) != 7 || UpvalStackIdx(a, tail) != 5 {
		t.Errorf("stackIdx")
	}
	if UpvalNextOpen(a, head) != mid || UpvalNextOpen(a, mid) != tail || UpvalNextOpen(a, tail) != 0 {
		t.Errorf("nextOpen chain broken")
	}
	// 关闭 mid:把当前栈值拷入,翻 flag。
	CloseUpvalue(a, mid, value.NumberValue(123))
	if !UpvalIsClosed(a, mid) {
		t.Fatalf("mid should be closed")
	}
	if v := UpvalClosedValue(a, mid); !value.IsNumber(v) || value.AsNumber(v) != 123 {
		t.Errorf("closed value lost")
	}
	// head 仍开放,nextOpen 链尚未由 caller 调整(本测试只验单元行为)。
	if UpvalIsClosed(a, head) {
		t.Errorf("head should remain open")
	}
}

func TestThreadLayout(t *testing.T) {
	a := arena.New(arena.Options{})
	th := AllocThread(a, 16, 8)
	if OTypeOf(HeaderOf(a, th)) != OBJ_THREAD {
		t.Fatalf("otype")
	}
	if ThreadStatusOf(a, th) != StatusSuspended {
		t.Errorf("initial status")
	}
	if ThreadStackCap(a, th) != 16 {
		t.Errorf("stackCap: %d", ThreadStackCap(a, th))
	}
	if ThreadCICap(a, th) != 8 {
		t.Errorf("ciCap: %d", ThreadCICap(a, th))
	}
	if ThreadTop(a, th) != 0 {
		t.Errorf("initial top")
	}
	SetThreadTop(a, th, 5)
	if ThreadTop(a, th) != 5 || ThreadStackCap(a, th) != 16 {
		t.Errorf("SetThreadTop corrupted cap")
	}
	SetThreadStatus(a, th, StatusRunning)
	if ThreadStatusOf(a, th) != StatusRunning {
		t.Errorf("status update")
	}
	SetThreadValueStackAt(a, th, 3, value.NumberValue(42))
	if v := ThreadValueStackAt(a, th, 3); !value.IsNumber(v) || value.AsNumber(v) != 42 {
		t.Errorf("stack slot")
	}
}

func TestUserdataLayout(t *testing.T) {
	a := arena.New(arena.Options{})
	ud := AllocUserdata(a, 13)
	if OTypeOf(HeaderOf(a, ud)) != OBJ_USERDATA {
		t.Fatalf("otype")
	}
	if UserdataLen(a, ud) != 13 {
		t.Errorf("len: %d", UserdataLen(a, ud))
	}
	payload := UserdataPayload(a, ud)
	for i := range payload {
		payload[i] = byte(i + 1)
	}
	got := UserdataPayload(a, ud)
	for i, b := range got {
		if b != byte(i+1) {
			t.Errorf("payload[%d] = %d", i, b)
		}
	}
	mt := AllocTable(a, 0, 0)
	SetUserdataMeta(a, ud, mt)
	if UserdataMetaRef(a, ud) != mt {
		t.Errorf("metaRef")
	}
	if FlagsOf(HeaderOf(a, ud))&udFlagHasMeta == 0 {
		t.Errorf("hasMeta flag")
	}
}

// TestObjectSizeFormulas:对照 06 §1.3 的字数公式(最关键的工程契约)。
func TestObjectSizeFormulas(t *testing.T) {
	cases := []struct {
		name string
		got  uint32
		want uint32
	}{
		{"String len=0", stringWords(0), 2 + 1}, // 2 + ceil((0+1)/8) = 3
		{"String len=7", stringWords(7), 2 + 1}, // 2 + ceil(8/8) = 3
		{"String len=8", stringWords(8), 2 + 2}, // 2 + ceil(9/8) = 4
		{"String len=15", stringWords(15), 2 + 2},
		{"String len=16", stringWords(16), 2 + 3},
		{"Userdata len=0", userdataWords(0), 4},
		{"Userdata len=7", userdataWords(7), 5},
		{"Userdata len=8", userdataWords(8), 5},
		{"Userdata len=9", userdataWords(9), 6},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, c.got, c.want)
		}
	}
}
