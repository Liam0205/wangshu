//go:build wangshu_p3

package memadapter

import (
	"context"
	"testing"

	"github.com/tetratelabs/wazero"

	"github.com/Liam0205/wangshu/internal/arena"
)

// PW1 验收(03-memory-model §1 + 00-overview §4 PW1 完成定义):
// arena 收养 wazero memory 后,值读写 / grow 行为与 Go 堆 backing 一致;
// grow 后 GCRef(字节偏移)不失效。

func newHolder(t *testing.T) (*MemoryHolder, func()) {
	t.Helper()
	ctx := context.Background()
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfigCompiler())
	h, err := New(ctx, rt, 64*1024, 1<<31)
	if err != nil {
		t.Fatalf("memadapter.New: %v", err)
	}
	return h, func() { _ = h.Close(); _ = rt.Close(ctx) }
}

// TestPW1_AdoptedArena_BasicAllocRead arena 收养 wazero memory 后,基本
// 分配 + 读写正确(收养对 arena 上层透明)。
func TestPW1_AdoptedArena_BasicAllocRead(t *testing.T) {
	h, cleanup := newHolder(t)
	defer cleanup()

	a := arena.New(arena.Options{
		InitialBytes:   64 * 1024,
		MaxBytes:       1 << 31,
		NewBacking:     h.Backing(),
		InPlaceBacking: true,
	})

	// 分配一块,写 NaN-box 值,读回一致
	ref := a.AllocBytes(16)
	if ref.IsNull() {
		t.Fatal("AllocBytes returned null")
	}
	words := a.Words()
	idx := uint32(ref) / 8
	words[idx] = 0xC0FFEE_DEADBEEF
	words[idx+1] = 0x1234_5678

	// 重取视图读回(模拟跨操作)
	w2 := a.Words()
	if w2[idx] != 0xC0FFEE_DEADBEEF || w2[idx+1] != 0x1234_5678 {
		t.Errorf("adopted arena read mismatch: %#x %#x", w2[idx], w2[idx+1])
	}
}

// TestPW1_AdoptedArena_GrowPreservesData 收养模式下 grow(memory.grow 原地
// 扩,不 copy)后,grow 前写入的数据仍可经 GCRef 偏移读回——验证
// InPlaceBacking 语义正确(03-memory-model §1.6 + arena grow64 适配)。
func TestPW1_AdoptedArena_GrowPreservesData(t *testing.T) {
	h, cleanup := newHolder(t)
	defer cleanup()

	a := arena.New(arena.Options{
		InitialBytes:   64 * 1024, // 1 page
		MaxBytes:       1 << 31,
		NewBacking:     h.Backing(),
		InPlaceBacking: true,
	})

	// 在初始容量内分配并写一个哨兵
	ref := a.AllocBytes(16)
	idx := uint32(ref) / 8
	const sentinel = uint64(0xABCD_1234_5678_9EF0)
	a.Words()[idx] = sentinel

	// 分配超过 1 page 触发 grow(memory.grow 原地扩)
	bigRef := a.AllocBytes(128 * 1024) // 128 KiB > 1 page,必 grow
	if bigRef.IsNull() {
		t.Fatal("big alloc returned null")
	}

	// grow 后:哨兵经 GCRef 偏移仍读回原值(偏移寻址不变 + 原地 grow 保留旧数据)
	w := a.Words()
	if w[idx] != sentinel {
		t.Errorf("grow lost data at ref %#x: got %#x, want %#x", uint32(ref), w[idx], sentinel)
	}

	// grow 后能写新区域
	bigIdx := uint32(bigRef) / 8
	w[bigIdx] = 0x9999
	if a.Words()[bigIdx] != 0x9999 {
		t.Error("grown region not writable")
	}
}

// TestPW1_AdoptedArena_ManyAllocsGrow 大量分配触发多次 grow,GCRef 全程
// 有效(模拟 longevity 形态的收养版本)。
func TestPW1_AdoptedArena_ManyAllocsGrow(t *testing.T) {
	h, cleanup := newHolder(t)
	defer cleanup()

	a := arena.New(arena.Options{
		InitialBytes:   64 * 1024,
		MaxBytes:       1 << 31,
		NewBacking:     h.Backing(),
		InPlaceBacking: true,
	})

	const n = 5000
	refs := make([]arena.GCRef, n)
	for i := 0; i < n; i++ {
		r := a.AllocBytes(64) // 64 B 各块,5000 块 = 320 KiB 触发多次 grow
		refs[i] = r
		a.Words()[uint32(r)/8] = uint64(i) // 写入序号
	}
	// 全部读回校验(grow 多次后所有旧 GCRef 仍指向正确数据)
	for i := 0; i < n; i++ {
		got := a.Words()[uint32(refs[i])/8]
		if got != uint64(i) {
			t.Fatalf("ref[%d]=%#x lost data: got %d want %d", i, uint32(refs[i]), got, i)
		}
	}
}

// TestPW1_MemoryShared_GoWasmView 验证 holder 的 Memory() 与 arena backing
// 视图同源(同一块 wazero linear memory)——这是「两层共见」的物理基础。
func TestPW1_MemoryShared_GoWasmView(t *testing.T) {
	h, cleanup := newHolder(t)
	defer cleanup()

	a := arena.New(arena.Options{
		InitialBytes:   64 * 1024,
		MaxBytes:       1 << 31,
		NewBacking:     h.Backing(),
		InPlaceBacking: true,
	})

	// 经 arena 视图写
	ref := a.AllocBytes(8)
	a.Words()[uint32(ref)/8] = 0x7777_8888

	// 经 wazero Memory 接口读同一偏移(字节偏移 = ref)
	got, ok := h.Memory().ReadUint64Le(uint32(ref))
	if !ok {
		t.Fatal("Memory.ReadUint64Le failed")
	}
	if got != 0x7777_8888 {
		t.Errorf("two-layer view mismatch: arena wrote 0x77778888, wazero read %#x", got)
	}
}
