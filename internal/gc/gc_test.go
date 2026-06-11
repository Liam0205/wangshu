package gc

import (
	"strings"
	"testing"

	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// 构造一个最小 setup:arena + collector + 一个空表当 globals。
func newTestVM(t *testing.T) (*arena.Arena, *Collector) {
	t.Helper()
	a := arena.New(arena.Options{InitialBytes: 4096})
	c := New(a, Options{})
	g := object.AllocTable(a, 0, 0)
	c.LinkSweep(g)
	c.SetRoots(Roots{Globals: g})
	return a, c
}

func TestHashStringDeterministic(t *testing.T) {
	// JSHash 是确定性函数(无 seed,06 §9.3 末注:沿用 5.1 无 seed 哈希,差分一致优先)。
	a, b := HashString([]byte("hello")), HashString([]byte("hello"))
	if a != b {
		t.Fatalf("hash not deterministic: %x vs %x", a, b)
	}
	// 不同串 → 不同哈希(高概率;此用例不依赖具体值,仅保证非平凡)。
	if HashString([]byte("hello")) == HashString([]byte("world")) {
		t.Fatalf("hash collision on trivial inputs")
	}
}

func TestInternBasics(t *testing.T) {
	_, c := newTestVM(t)
	r1 := c.Intern([]byte("abc"))
	r2 := c.Intern([]byte("abc"))
	if r1 != r2 {
		t.Errorf("intern same content gave different refs: %d vs %d", r1, r2)
	}
	r3 := c.Intern([]byte("abd"))
	if r3 == r1 {
		t.Errorf("intern different content shared ref")
	}
	if c.StringTableSize() != 2 {
		t.Errorf("size after 2 distinct interns = %d, want 2", c.StringTableSize())
	}
}

func TestInternRehash(t *testing.T) {
	_, c := newTestVM(t)
	for i := 0; i < 200; i++ {
		c.Intern([]byte(strings.Repeat("x", i+1))) // 200 个不同长度的串
	}
	if c.StringTableSize() != 200 {
		t.Errorf("size = %d, want 200", c.StringTableSize())
	}
	// 全部仍可命中(rehash 后引用未失效——arena 偏移寻址)。
	for i := 0; i < 200; i++ {
		if c.Intern([]byte(strings.Repeat("x", i+1))) == 0 {
			t.Errorf("intern miss after rehash")
		}
	}
}

func TestSweepReclaimsUnreachable(t *testing.T) {
	a, c := newTestVM(t)
	g := c.roots.Globals

	// 把 "alive" 串挂到 globals 数组段(确保根可达)。
	tbl := object.AllocTable(a, 4, 0)
	c.LinkSweep(tbl)
	// globals 是个空表,简化起见把 tbl 直接放进它的元数据节点不便;改用:Registry 字段挂 tbl。
	c.SetRoots(Roots{Globals: g, Registry: tbl})
	aliveStr := c.Intern([]byte("alive"))
	object.SetTableArrayAt(a, tbl, 0, value.MakeGC(value.TagString, aliveStr))

	// 创建一个不可达串。
	deadStr := c.Intern([]byte("dead"))
	if deadStr == 0 || aliveStr == 0 {
		t.Fatal("intern returned null")
	}

	// 一轮 GC,deadStr 应被从 string 表摘除;aliveStr 仍在。
	c.Collect()
	if !c.internContains([]byte("alive")) {
		t.Errorf("alive string lost after GC")
	}
	if c.internContains([]byte("dead")) {
		t.Errorf("dead string still in intern table after GC")
	}
}

func TestShadowStackProtects(t *testing.T) {
	_, c := newTestVM(t)
	tmp := c.Intern([]byte("temp"))
	if tmp == 0 {
		t.Fatal("intern returned null")
	}
	// 在没有任何 Lua 栈引用时,把它登记到 shadow stack:GC 应保留它。
	h := c.Push(value.MakeGC(value.TagString, tmp))
	defer c.Pop(h)

	c.Collect()
	if !c.internContains([]byte("temp")) {
		t.Errorf("shadow-stack-protected string lost after GC")
	}
}

func TestShadowStackPopAfterGCDropsString(t *testing.T) {
	_, c := newTestVM(t)
	tmp := c.Intern([]byte("temp2"))
	h := c.Push(value.MakeGC(value.TagString, tmp))
	c.Pop(h)
	c.Collect()
	if c.internContains([]byte("temp2")) {
		t.Errorf("string survived after pop+GC (no other root)")
	}
}

func TestPacingFlipsWhite(t *testing.T) {
	_, c := newTestVM(t)
	white0 := c.currentWhite
	c.Collect()
	if c.currentWhite == white0 {
		t.Errorf("currentWhite did not flip")
	}
}

func TestThresholdAdjustsAfterCollect(t *testing.T) {
	_, c := newTestVM(t)
	beforeT := c.threshold
	c.bytesAllocSince = uint64(c.threshold) + 100
	c.Collect()
	if c.bytesAllocSince != 0 {
		t.Errorf("bytesAllocSince not reset, = %d", c.bytesAllocSince)
	}
	if c.threshold == 0 {
		t.Errorf("threshold = 0 after Collect (clamp failed)")
	}
	_ = beforeT
}

// 高频 GC 模式压力(06 §11 / 12 §10 第 11 条):每 16 字节分配就强制 Collect,验证
// ① 不崩溃 ② 输出仍可读(string intern 表正确性 / sweep 链不损坏)。
func TestStressHighFrequencyGC(t *testing.T) {
	a, c := newTestVM(t)
	g := c.roots.Globals
	// 用一张表把"已注册"的串都挂上,作为强引用。
	keep := object.AllocTable(a, 64, 0)
	c.LinkSweep(keep)
	c.SetRoots(Roots{Globals: g, Registry: keep})

	for i := 0; i < 64; i++ {
		s := c.Intern([]byte{byte('a' + i%26), byte('0' + i/26)})
		object.SetTableArrayAt(a, keep, uint32(i), value.MakeGC(value.TagString, s))
		// 每分配若干串就强制 GC。
		c.Collect()
	}
	// 全部 64 个串应仍可达。
	if c.StringTableSize() != 64 {
		t.Errorf("after stress: intern size = %d, want 64", c.StringTableSize())
	}
}

// internContains is a test helper that scans the string intern table.
func (c *Collector) internContains(b []byte) bool {
	h := HashString(b)
	for _, ref := range c.strBuckets[h&c.strMask] {
		if c.stringMatches(ref, h, b) {
			return true
		}
	}
	return false
}
