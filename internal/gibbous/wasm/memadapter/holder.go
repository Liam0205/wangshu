//go:build wangshu_p3

// Package memadapter 让 arena 收养 wazero linear memory 作 backing
// (docs/design/p3-wasm-tier/03-memory-model.md §1)。
//
// 仅 wangshu_p3 build 编译——默认 / wangshu_profile build 下 arena 走
// DefaultBacking(Go 堆 make),不引入 wazero。
//
// **实装与设计骨架的偏差(承 PW0 spike 实测,SP-A)**:03-memory-model §1.5
// 骨架用 `mem.UnsafeUnderlyingBuffer()`,但 PW0 spike(spike/p3boundary)
// 实测确认用 `mem.Read(0, size)` 取 write-through 零拷贝视图更稳(wazero
// v1.12.0 公开 API,语义明确:返回底层 buffer 直接视图,grow 后 disconnect
// 需重取)。本子包按实测结论用 Read。
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

// MemoryHolder 持有为某个 State 主 arena 当 backing 用的 wazero Memory。
//
// 一份 MemoryHolder 服务一个 State 的主 arena(arena 单 State 私有,03 §1.5)。
// holder module 仅声明 + 导出 memory,不带函数;gibbous 翻译产物经独立 module
// 经 import memory 共享这块 memory(04-trampoline §2 / §3)。
type MemoryHolder struct {
	ctx     context.Context
	runtime wazero.Runtime
	module  api.Module
	memory  api.Memory
	maxPage uint32
}

// New 构造 MemoryHolder,分配 initialBytes(向上对齐 64 KiB 页)的 linear
// memory,上限 maxBytes。
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

	bin := buildMemoryHolderModuleBinary(initPage, maxPage)
	compiled, err := runtime.CompileModule(ctx, bin)
	if err != nil {
		return nil, fmt.Errorf("memadapter: compile holder module: %w", err)
	}
	// 命名 "env":gibbous module 经 `import "env" "memory"` 共享这块 memory
	// (PW2 跨 module memory 共享,03 §3.1)。
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

// Memory 暴露底层 wazero Memory(gibbous module import memory 时需要)。
func (h *MemoryHolder) Memory() api.Memory { return h.memory }

// Backing 返回 arena.BackingFn 适配器。每次 arena 请求 backing(初始 +
// 每次 grow)时,确保 wazero memory 容量足够,再取 write-through 视图别名
// 成 []uint64 返回。
//
// **arena.grow64 配套**(03 §1.6 + arena GrowInPlace 标志):收养模式下
// memory.grow 是原地扩展(旧内容保留),返回的新视图已含旧数据 —— arena
// 必须用 InPlaceBacking 语义(不 copy),否则 copy 源(旧视图)已 disconnect
// 是 UB。见 arena.Options.InPlaceBacking。
func (h *MemoryHolder) Backing() arena.BackingFn {
	return func(words uint32) []uint64 {
		needBytes := uint64(words) * 8
		h.ensureBytes(needBytes)
		return h.viewWords(words)
	}
}

// ensureBytes 在 wazero memory 容量不足 needBytes 时 grow。grow 后旧视图
// 失效(spike 实测),由 Backing 闭包随后重取新视图覆盖。
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

// viewWords 取 wazero memory 前 words*8 字节的 write-through 视图,unsafe
// 别名成 []uint64。
//
// 安全性:① mem.Read 返回底层 buffer 直接切片(write-through,spike 实测);
// ② wazero memory 起始 64 KiB 页对齐,远超 uint64 的 8 字节对齐要求;
// ③ 视图长度恰为 words*8(arena 注入点契约 1)。
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

// Close 释放 holder module 与 runtime 持有的资源(State 销毁时调)。
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
