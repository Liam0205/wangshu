//go:build wangshu_p3

// Package memadapter lets the arena adopt a wazero linear memory as its backing
// (docs/design/p3-wasm-tier/03-memory-model.md §1).
//
// Compiled only under the wangshu_p3 build tag: in the default / wangshu_profile
// builds the arena uses DefaultBacking (Go-heap make) and does not pull in wazero.
//
// **Deviation from the design skeleton (per the PW0 spike measurements, SP-A)**:
// 03-memory-model §1.5's skeleton uses `mem.UnsafeUnderlyingBuffer()`, but the
// PW0 spike (spike/p3boundary) confirmed empirically that `mem.Read(0, size)`
// yields a more robust write-through zero-copy view (a public wazero v1.12.0 API
// with well-defined semantics: it returns a direct view of the underlying buffer,
// which disconnects after a grow and must be re-fetched). This subpackage follows
// that finding and uses Read.
package memadapter

import (
	"context"
	"fmt"
	"unsafe"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"

	"github.com/Liam0205/wangshu/internal/arena"
)

const wasmPageBytes = 65536 // 64 KiB

// MemoryHolder holds the wazero Memory used as the backing for some State's main arena.
//
// One MemoryHolder serves one State's main arena (an arena is private to a single
// State, 03 §1.5). The holder module only declares and exports the memory, carrying
// no functions; the modules produced by gibbous translation share this memory via
// import memory (04-trampoline §2 / §3).
type MemoryHolder struct {
	ctx     context.Context
	runtime wazero.Runtime
	module  api.Module
	memory  api.Memory
	maxPage uint32
}

// TableSlots is the capacity of the shared funcref table (the slot cap of the
// PW10 Arch-2 tiered-up function registry).
//
// The table is declared once in the env holder module (fixed capacity, no max);
// each tiered-up Proto occupies one slot (monotonic allocation, with the Compiler
// maintaining the Proto→slot registry). 8192 far exceeds the realistic count of
// hot Protos within a single State (a typical Program has ≪ hundreds of tiered-up
// functions); overflow is detected by the Compiler at allocation time (if exceeded,
// that Proto is not called directly via call_indirect and falls back to h_call,
// without breaking correctness).
const TableSlots = 8192

// New constructs a MemoryHolder, allocating a linear memory of initialBytes
// (rounded up to a 64 KiB page boundary) with an upper limit of maxBytes.
func New(ctx context.Context, runtime wazero.Runtime, initialBytes, maxBytes uint32) (*MemoryHolder, error) {
	initPage := ceilDivPage(initialBytes)
	maxPage := ceilDivPage(maxBytes)
	if initPage == 0 {
		initPage = 1
	}
	if maxPage < initPage {
		maxPage = initPage
	}
	if maxPage > wazero32MaxPages {
		return nil, fmt.Errorf("memadapter: maxBytes %d exceeds wasm32 4 GiB", maxBytes)
	}

	bin := buildMemoryHolderModuleBinary(initPage, maxPage, TableSlots)
	compiled, err := runtime.CompileModule(ctx, bin)
	if err != nil {
		return nil, fmt.Errorf("memadapter: compile holder module: %w", err)
	}
	// Named "env": the gibbous module shares this memory via
	// `import "env" "memory"` (PW2 cross-module memory sharing, 03 §3.1).
	mod, err := runtime.InstantiateModule(ctx, compiled,
		wazero.NewModuleConfig().WithName("env"))
	if err != nil {
		return nil, fmt.Errorf("memadapter: instantiate holder: %w", err)
	}
	mem := mod.ExportedMemory("memory")
	if mem == nil {
		return nil, fmt.Errorf("memadapter: holder module exports no memory")
	}
	return &MemoryHolder{
		ctx:     ctx,
		runtime: runtime,
		module:  mod,
		memory:  mem,
		maxPage: maxPage,
	}, nil
}

// Memory exposes the underlying wazero Memory (needed when the gibbous module
// imports memory).
func (h *MemoryHolder) Memory() api.Memory { return h.memory }

// Backing returns an arena.BackingFn adapter. On each arena backing request (the
// initial one plus every grow) it ensures the wazero memory has enough capacity,
// then takes a write-through view aliased as []uint64 and returns it.
//
// **Companion to arena.grow64** (03 §1.6 + arena GrowInPlace flag): under the
// adoption model, memory.grow is an in-place expansion (old contents preserved),
// so the returned new view already contains the old data — the arena must use
// InPlaceBacking semantics (no copy), otherwise the copy source (the old view) has
// already disconnected and reading it is UB. See arena.Options.InPlaceBacking.
func (h *MemoryHolder) Backing() arena.BackingFn {
	return func(words uint32) []uint64 {
		needBytes := uint64(words) * 8
		h.ensureBytes(needBytes)
		return h.viewWords(words)
	}
}

// ensureBytes grows the wazero memory when its capacity falls short of needBytes.
// After a grow the old view is invalidated (per the spike measurements); the
// Backing closure then re-fetches a fresh view to replace it.
func (h *MemoryHolder) ensureBytes(needBytes uint64) {
	cur := uint64(h.memory.Size())
	if cur >= needBytes {
		return
	}
	needPages := uint64(ceilDivPage64(needBytes))
	curPages := cur / wasmPageBytes
	delta := needPages - curPages
	if _, ok := h.memory.Grow(uint32(delta)); !ok {
		panic(fmt.Sprintf("memadapter: memory.grow(%d pages) failed (cur=%d need=%d bytes)",
			delta, cur, needBytes))
	}
}

// viewWords takes a write-through view of the first words*8 bytes of the wazero
// memory, unsafely aliased as []uint64.
//
// Safety: ① mem.Read returns a direct slice of the underlying buffer (write-through,
// per the spike measurements); ② the wazero memory starts 64 KiB page-aligned, far
// exceeding uint64's 8-byte alignment requirement; ③ the view length is exactly
// words*8 (arena injection-point contract 1).
func (h *MemoryHolder) viewWords(words uint32) []uint64 {
	byteLen := uint64(words) * 8
	buf, ok := h.memory.Read(0, uint32(byteLen))
	if !ok {
		panic(fmt.Sprintf("memadapter: memory.Read(0, %d) failed (size=%d)", byteLen, h.memory.Size()))
	}
	if len(buf) == 0 {
		return nil
	}
	return unsafe.Slice((*uint64)(unsafe.Pointer(&buf[0])), words)
}

// Close releases the resources held by the holder module and runtime (called when
// the State is destroyed).
func (h *MemoryHolder) Close() error {
	return h.module.Close(h.ctx)
}

const wazero32MaxPages = 65536 // wasm32: 4 GiB / 64 KiB

func ceilDivPage(bytes uint32) uint32 {
	return uint32((uint64(bytes) + wasmPageBytes - 1) / wasmPageBytes)
}

func ceilDivPage64(bytes uint64) uint32 {
	return uint32((bytes + wasmPageBytes - 1) / wasmPageBytes)
}
