//go:build wangshu_p3

package memadapter

import (
	"context"
	"testing"

	"github.com/tetratelabs/wazero"

	"github.com/Liam0205/wangshu/internal/arena"
)

// PW1 acceptance (03-memory-model §1 + 00-overview §4 PW1 completion criteria):
// after the arena adopts the wazero memory, value read/write and grow behavior
// match a Go-heap backing; a GCRef (byte offset) stays valid across grow.

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

// TestPW1_AdoptedArena_BasicAllocRead after the arena adopts the wazero memory,
// basic allocation plus read/write is correct (adoption is transparent to the
// arena's upper layers).
func TestPW1_AdoptedArena_BasicAllocRead(t *testing.T) {
	h, cleanup := newHolder(t)
	defer cleanup()

	a := arena.New(arena.Options{
		InitialBytes:   64 * 1024,
		MaxBytes:       1 << 31,
		NewBacking:     h.Backing(),
		InPlaceBacking: true,
	})

	// Allocate a block, write a NaN-box value, read it back unchanged
	ref := a.AllocBytes(16)
	if ref.IsNull() {
		t.Fatal("AllocBytes returned null")
	}
	words := a.Words()
	idx := uint32(ref) / 8
	words[idx] = 0xC0FFEE_DEADBEEF
	words[idx+1] = 0x1234_5678

	// Re-fetch the view and read back (simulates a cross-operation access)
	w2 := a.Words()
	if w2[idx] != 0xC0FFEE_DEADBEEF || w2[idx+1] != 0x1234_5678 {
		t.Errorf("adopted arena read mismatch: %#x %#x", w2[idx], w2[idx+1])
	}
}

// TestPW1_AdoptedArena_GrowPreservesData under adoption, after a grow
// (memory.grow extends in place, no copy) data written before the grow is still
// readable via the GCRef offset — verifying InPlaceBacking semantics are correct
// (03-memory-model §1.6 + arena grow64 adaptation).
func TestPW1_AdoptedArena_GrowPreservesData(t *testing.T) {
	h, cleanup := newHolder(t)
	defer cleanup()

	a := arena.New(arena.Options{
		InitialBytes:   64 * 1024, // 1 page
		MaxBytes:       1 << 31,
		NewBacking:     h.Backing(),
		InPlaceBacking: true,
	})

	// Allocate within the initial capacity and write a sentinel
	ref := a.AllocBytes(16)
	idx := uint32(ref) / 8
	const sentinel = uint64(0xABCD_1234_5678_9EF0)
	a.Words()[idx] = sentinel

	// Allocate beyond 1 page to trigger a grow (memory.grow extends in place)
	bigRef := a.AllocBytes(128 * 1024) // 128 KiB > 1 page, must grow
	if bigRef.IsNull() {
		t.Fatal("big alloc returned null")
	}

	// After grow: the sentinel still reads back via the GCRef offset (offset
	// addressing is unchanged + in-place grow preserves old data)
	w := a.Words()
	if w[idx] != sentinel {
		t.Errorf("grow lost data at ref %#x: got %#x, want %#x", uint32(ref), w[idx], sentinel)
	}

	// After grow, the new region is writable
	bigIdx := uint32(bigRef) / 8
	w[bigIdx] = 0x9999
	if a.Words()[bigIdx] != 0x9999 {
		t.Error("grown region not writable")
	}
}

// TestPW1_AdoptedArena_ManyAllocsGrow many allocations trigger repeated grows,
// with GCRefs staying valid throughout (an adopted version of the longevity
// scenario).
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
		r := a.AllocBytes(64) // 64 B per block, 5000 blocks = 320 KiB triggers several grows
		refs[i] = r
		a.Words()[uint32(r)/8] = uint64(i) // write the index
	}
	// Read all back and verify (after several grows, every old GCRef still points at the right data)
	for i := 0; i < n; i++ {
		got := a.Words()[uint32(refs[i])/8]
		if got != uint64(i) {
			t.Fatalf("ref[%d]=%#x lost data: got %d want %d", i, uint32(refs[i]), got, i)
		}
	}
}

// TestPW1_MemoryShared_GoWasmView verifies the holder's Memory() and the arena
// backing view share the same source (the same wazero linear memory) — the
// physical basis for "both layers see the same memory".
func TestPW1_MemoryShared_GoWasmView(t *testing.T) {
	h, cleanup := newHolder(t)
	defer cleanup()

	a := arena.New(arena.Options{
		InitialBytes:   64 * 1024,
		MaxBytes:       1 << 31,
		NewBacking:     h.Backing(),
		InPlaceBacking: true,
	})

	// Write through the arena view
	ref := a.AllocBytes(8)
	a.Words()[uint32(ref)/8] = 0x7777_8888

	// Read the same offset through the wazero Memory interface (byte offset = ref)
	got, ok := h.Memory().ReadUint64Le(uint32(ref))
	if !ok {
		t.Fatal("Memory.ReadUint64Le failed")
	}
	if got != 0x7777_8888 {
		t.Errorf("two-layer view mismatch: arena wrote 0x77778888, wazero read %#x", got)
	}
}

// TestPW10_EnvTableExported verifies the env holder module exports a shared
// funcref table (the physical foundation of the PW10 Arch-2 upper-layer function
// registry) — the table declaration must keep the holder binary instantiable, and
// the table must be shareable by a later gibbous module via `import env.table`
// plus active-element self-registration.
func TestPW10_EnvTableExported(t *testing.T) {
	ctx := context.Background()
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfigCompiler())
	defer rt.Close(ctx)
	h, err := New(ctx, rt, 64*1024, 1<<31)
	if err != nil {
		t.Fatalf("memadapter.New: %v", err)
	}
	defer h.Close()

	// Real test: one module that imports env.table registers a func into table[0]
	// via an active element, and another module that imports env.table reaches it
	// with call_indirect table[0].
	// This reproduces spike S-C, confirming the holder's table really is one that
	// "can be shared across modules + written by an element".
	provider := []byte{
		0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00,
		// type: (i32)->(i32)
		0x01, 0x06, 0x01, 0x60, 0x01, 0x7f, 0x01, 0x7f,
		// import env.table (funcref, flags=0 min=0)
		0x02, 0x0f, 0x01, 0x03, 'e', 'n', 'v', 0x05, 't', 'a', 'b', 'l', 'e', 0x01, 0x70, 0x00, 0x00,
		// func: 1 func type0
		0x03, 0x02, 0x01, 0x00,
		// element: active table0 offset=(i32.const 0) func[0]
		0x09, 0x07, 0x01, 0x00, 0x41, 0x00, 0x0b, 0x01, 0x00,
		// code: leaf(x)=x+1
		0x0a, 0x09, 0x01, 0x07, 0x00, 0x20, 0x00, 0x41, 0x01, 0x6a, 0x0b,
	}
	if _, err := rt.InstantiateWithConfig(ctx, provider,
		wazero.NewModuleConfig().WithName("p")); err != nil {
		t.Fatalf("provider import env.table + element register failed: %v", err)
	}
	caller := []byte{
		0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00,
		0x01, 0x06, 0x01, 0x60, 0x01, 0x7f, 0x01, 0x7f,
		0x02, 0x0f, 0x01, 0x03, 'e', 'n', 'v', 0x05, 't', 'a', 'b', 'l', 'e', 0x01, 0x70, 0x00, 0x00,
		0x03, 0x02, 0x01, 0x00,
		0x07, 0x07, 0x01, 0x03, 'r', 'u', 'n', 0x00, 0x00,
		// run(x) = call_indirect[table0,type0]( x, (i32.const 0) )
		0x0a, 0x0b, 0x01, 0x09, 0x00, 0x20, 0x00, 0x41, 0x00, 0x11, 0x00, 0x00, 0x0b,
	}
	cmod, err := rt.InstantiateWithConfig(ctx, caller,
		wazero.NewModuleConfig().WithName("c"))
	if err != nil {
		t.Fatalf("caller import env.table failed: %v", err)
	}
	res, err := cmod.ExportedFunction("run").Call(ctx, 41)
	if err != nil {
		t.Fatalf("cross-module call_indirect via env.table: %v", err)
	}
	if res[0] != 42 { // leaf(41)=42
		t.Errorf("env.table call_indirect = %d, want 42", res[0])
	}
}
