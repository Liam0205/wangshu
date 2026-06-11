package arena

import (
	"testing"
	"unsafe"
)

func TestNullReserved(t *testing.T) {
	a := New(Options{})
	// 第一次分配的 GCRef 必须 ≥ 8(offset 0 保留为 null)。
	r := a.AllocBytes(8)
	if r == 0 {
		t.Fatalf("first allocation returned null GCRef")
	}
	if r < GCRef(nullReserve) {
		t.Fatalf("first allocation %d below nullReserve %d", r, nullReserve)
	}
	// null GCRef 的语义。
	if !GCRef(0).IsNull() {
		t.Fatalf("GCRef(0).IsNull() = false, want true")
	}
	if r.IsNull() {
		t.Fatalf("non-zero GCRef reports IsNull")
	}
}

func TestAlignment(t *testing.T) {
	a := New(Options{})
	// 任意大小请求都必须返回 8 对齐的 GCRef(low 3 bit = 0)。
	for _, n := range []uint32{1, 3, 7, 8, 9, 15, 16, 17, 100, 1023} {
		r := a.AllocBytes(n)
		if r&7 != 0 {
			t.Fatalf("AllocBytes(%d) returned misaligned %#x", n, uint64(r))
		}
	}
	// AllocWords 同理。
	for _, w := range []uint32{1, 3, 9} {
		r := a.AllocWords(w)
		if r&7 != 0 {
			t.Fatalf("AllocWords(%d) returned misaligned %#x", w, uint64(r))
		}
	}
}

func TestRoundUp8(t *testing.T) {
	cases := []struct{ in, want uint32 }{
		{0, 0}, {1, 8}, {7, 8}, {8, 8}, {9, 16}, {15, 16}, {16, 16}, {17, 24},
	}
	for _, c := range cases {
		if got := roundUp8(c.in); got != c.want {
			t.Errorf("roundUp8(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestBumpProgression(t *testing.T) {
	a := New(Options{})
	prev := uint32(nullReserve)
	for i := 0; i < 100; i++ {
		r := a.AllocBytes(13) // 向上对齐到 16
		if uint32(r) != prev {
			t.Fatalf("alloc #%d: ref=%d, expected bump=%d", i, r, prev)
		}
		prev += 16
	}
	if a.Bump() != prev {
		t.Fatalf("bump=%d, want %d", a.Bump(), prev)
	}
}

func TestDualViewAlias(t *testing.T) {
	a := New(Options{})
	// 在某偏移写一个 uint64,通过字节视图读出 8 个字节,反之亦然——同一段内存的两个视图。
	r := a.AllocWords(1)
	a.SetWordAt(r, 0x0123456789ABCDEF)
	off := uint32(r)
	bytes := a.Bytes()
	// 小端
	for i, want := range []byte{0xEF, 0xCD, 0xAB, 0x89, 0x67, 0x45, 0x23, 0x01} {
		if got := bytes[off+uint32(i)]; got != want {
			t.Fatalf("byte view[%d] = %#x, want %#x", off+uint32(i), got, want)
		}
	}
	// 改字节视图,字视图同步可见
	bytes[off] = 0xFF
	if got := a.WordAt(r); got != 0x0123456789ABCDFF {
		t.Fatalf("word after byte mutation = %#x", got)
	}
}

func TestDualViewBacking(t *testing.T) {
	a := New(Options{InitialBytes: 64})
	// words 与 bytes 必须共享同一段底层内存(unsafe alias)。
	if len(a.Bytes()) != len(a.Words())*8 {
		t.Fatalf("bytes len %d != words len %d * 8", len(a.Bytes()), len(a.Words())*8)
	}
	if &a.Bytes()[0] != (*byte)(unsafe.Pointer(&a.Words()[0])) {
		t.Fatalf("byte view base != word view base (alias broken)")
	}
}

func TestGrowPreservesContent(t *testing.T) {
	a := New(Options{InitialBytes: 64})
	// 装满 64 字节(分配 7 个 8 字节,加上 nullReserve 占 8)。
	refs := make([]GCRef, 7)
	for i := range refs {
		refs[i] = a.AllocWords(1)
		a.SetWordAt(refs[i], uint64(0xCAFE0000+i))
	}
	beforeCap := a.Cap()
	// 下一次分配触发 grow:cap 翻倍。
	r := a.AllocWords(1)
	a.SetWordAt(r, 0xDEADBEEF)
	if a.Cap() <= beforeCap {
		t.Fatalf("cap did not grow: %d -> %d", beforeCap, a.Cap())
	}
	// 旧 GCRef 必须仍可读出原值——这是偏移寻址的红利。
	for i, ref := range refs {
		if got := a.WordAt(ref); got != uint64(0xCAFE0000+i) {
			t.Fatalf("after grow: ref %d = %#x, want %#x", ref, got, uint64(0xCAFE0000+i))
		}
	}
	if got := a.WordAt(r); got != 0xDEADBEEF {
		t.Fatalf("after grow: new ref %d = %#x", r, got)
	}
}

func TestGrowMultiple(t *testing.T) {
	a := New(Options{InitialBytes: 32})
	refs := make([]GCRef, 0, 1000)
	for i := 0; i < 1000; i++ {
		r := a.AllocWords(1)
		a.SetWordAt(r, uint64(i))
		refs = append(refs, r)
	}
	for i, r := range refs {
		if got := a.WordAt(r); got != uint64(i) {
			t.Fatalf("ref %d = %d, want %d", r, got, i)
		}
	}
}

func TestCustomBacking(t *testing.T) {
	called := 0
	bf := func(words uint32) []uint64 {
		called++
		return make([]uint64, words)
	}
	a := New(Options{InitialBytes: 32, NewBacking: bf})
	if called != 1 {
		t.Fatalf("backing called %d times after New, want 1", called)
	}
	// 触发 grow → backing 工厂再被调一次。
	for a.Cap() == 32 {
		a.AllocWords(1)
	}
	if called < 2 {
		t.Fatalf("backing called %d times after grow, want >=2", called)
	}
}

func TestMisalignedRefPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on misaligned WordAt")
		}
	}()
	a := New(Options{})
	a.WordAt(GCRef(7))
}

func TestAllocBytesZero(t *testing.T) {
	a := New(Options{})
	// 零长度分配 → 至少 8 字节,且 GCRef 仍唯一(防退化为 null)。
	r1 := a.AllocBytes(0)
	r2 := a.AllocBytes(0)
	if r1 == 0 || r2 == 0 || r1 == r2 {
		t.Fatalf("zero-len allocs collided or null: r1=%d r2=%d", r1, r2)
	}
}
