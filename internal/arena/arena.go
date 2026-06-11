// Package arena provides the self-managed linear memory backing the value world.
//
// 设计:docs/design/p1-interpreter/06-memory-gc.md §1-§3。
//
// 核心契约:
//   - 双视图 backing:同一底层经 unsafe 别名出 []uint64 (字视图) 与 []byte (字节视图)。
//   - 偏移寻址:GCRef 是 48-bit 字节偏移(对 Go GC 是普通整数,绕开写屏障税)。
//   - 8 字节对齐:所有分配按字对齐;GCRef 低 3 bit 恒为 0。
//   - offset 0 保留为 null GCRef(语义上「无对象」),bump 初值 = 8。
//   - grow 翻倍扩容,内部用 GCRef 引用关系不变(偏移寻址的红利)。
//   - backing 来源经注入点(NewBacking),P3 替换为 wazero linear memory adapter。
//
// 本阶段不含 GCHeader / freelist / sweep 链:它们由 gc 包(M5)接入,本包只暴露 AllocBytes
// 字节分配原语 + Words/Bytes 双视图访问。
package arena

import (
	"fmt"
	"unsafe"
)

// GCRef 是 arena 内对象的字节偏移引用(48-bit 有效,uint64 承载方便嵌入 NaN-box)。
// 0 = null(无对象);非零值低 3 bit 恒为 0(8 字节对齐)。
type GCRef uint64

// IsNull reports whether ref is the null reference.
func (r GCRef) IsNull() bool { return r == 0 }

// 上限。bump/cap 用 uint32 ⇒ 单 arena 最大 4 GiB(06 §3)。
const (
	// MaxBytes 是单 arena 容量上限(uint32 寻址)。
	MaxBytes uint32 = 1 << 31 // 2 GiB,留一半 headroom 防 uint32 边界溢出
	// nullReserve 是 offset 0 保留区(字节)。bump 初值 = 8。
	nullReserve uint32 = 8
)

// BackingFn 是 backing 内存的工厂。P1 默认实现为 make([]uint64, n);P3 替换为 wazero
// linear memory adapter(承 06 §1.1 与 p3-wasm-tier §4.2 回填请求)。
type BackingFn func(words uint32) []uint64

// DefaultBacking 是 P1 默认 backing 工厂(纯 Go 堆分配)。
func DefaultBacking(words uint32) []uint64 { return make([]uint64, words) }

// Options 配置 Arena 行为。
type Options struct {
	// InitialBytes 初始容量(字节,自动向上对齐到 8 的倍数)。零值 = 64 KiB。
	InitialBytes uint32
	// MaxBytes 上限(字节)。零值 = arena.MaxBytes。
	MaxBytes uint32
	// NewBacking backing 工厂,nil 用 DefaultBacking。
	NewBacking BackingFn
}

// Arena 是自管线性内存。包含 backing 双视图与 bump 指针。
//
// 注意:Arena 内部对象互引用经 GCRef(整数),Go GC 看不到内部图——这是孤立 Go 写屏障
// 税的物理手段(roadmap §2)。
type Arena struct {
	words   []uint64 // 字视图(真实 backing)
	bytes   []byte   // 字节视图(unsafe 别名,与 words 共享同一段内存)
	bump    uint32   // 下一个未分配字节偏移(始终 8 对齐)
	cap     uint32   // 当前容量(字节,= len(words)*8)
	maxCap  uint32   // 上限(字节)
	backing BackingFn
}

// New creates an Arena with the given options.
func New(opts Options) *Arena {
	cap := opts.InitialBytes
	if cap == 0 {
		cap = 64 * 1024
	}
	cap = roundUp8(cap)
	max := opts.MaxBytes
	if max == 0 {
		max = MaxBytes
	}
	if cap > max {
		cap = max
	}
	bf := opts.NewBacking
	if bf == nil {
		bf = DefaultBacking
	}
	a := &Arena{
		bump:    nullReserve,
		cap:     cap,
		maxCap:  max,
		backing: bf,
	}
	a.setBacking(bf(cap / 8))
	return a
}

// setBacking installs a fresh backing slice and re-derives the byte view.
//
// 注意双视图建立顺序:从 words 派生 bytes(06 §1.1:[]byte 起始地址不保证 8 对齐,
// 反向派生在某些平台读 uint64 会触发非对齐访问)。
func (a *Arena) setBacking(words []uint64) {
	if len(words)*8 > int(a.maxCap) || len(words)*8 < int(a.cap) {
		// 防御:backing 必须能装下 a.cap 字节。
		panic(fmt.Sprintf("arena: backing size mismatch: want >=%d bytes, got %d", a.cap, len(words)*8))
	}
	a.words = words
	if len(words) == 0 {
		a.bytes = nil
		return
	}
	a.bytes = unsafe.Slice((*byte)(unsafe.Pointer(&words[0])), len(words)*8)
}

// Cap returns the current capacity in bytes.
func (a *Arena) Cap() uint32 { return a.cap }

// Bump returns the next unallocated byte offset (also = total allocated bytes from offset 0,
// minus the nullReserve at the start).
func (a *Arena) Bump() uint32 { return a.bump }

// Words returns the word view of the entire backing. **The returned slice is invalidated
// by any subsequent allocation that triggers a grow** — callers must re-fetch after such ops
// (engineering.md §-2 约束:reloadFrame 纪律).
func (a *Arena) Words() []uint64 { return a.words }

// Bytes returns the byte view of the entire backing. Same invalidation rule as Words.
func (a *Arena) Bytes() []byte { return a.bytes }

// AllocBytes 分配 nbytes 字节(自动向上对齐到 8),返回起始字节偏移 GCRef。
//
// 不写 GCHeader、不挂 sweep 链——那是 gc 包(M5)的责任。本函数只负责"切走 N 字节"。
//
// 容量不足 → 触发 grow(P1 不在此触发 GC,GC 在 gc 包接入时由其 maybeCollect 在 grow 前介入,
// 见 06 §2.1 / §7;M1 阶段无 GC,直接 grow)。超 maxCap → panic(后续可改返回 error)。
func (a *Arena) AllocBytes(nbytes uint32) GCRef {
	need := roundUp8(nbytes)
	if need == 0 {
		need = 8 // 至少分配一字,避免零长引用
	}
	if a.bump+need > a.cap {
		a.grow(a.bump + need)
	}
	ref := GCRef(a.bump)
	a.bump += need
	return ref
}

// AllocWords is a convenience wrapper for AllocBytes(words*8).
func (a *Arena) AllocWords(words uint32) GCRef { return a.AllocBytes(words * 8) }

// WordAt returns the uint64 at the given GCRef (interpreted as a word offset by ref/8).
// Panics on misaligned ref.
func (a *Arena) WordAt(ref GCRef) uint64 {
	if ref&7 != 0 {
		panic(fmt.Sprintf("arena: misaligned GCRef %#x", uint64(ref)))
	}
	return a.words[ref>>3]
}

// SetWordAt writes v at the given GCRef.
func (a *Arena) SetWordAt(ref GCRef, v uint64) {
	if ref&7 != 0 {
		panic(fmt.Sprintf("arena: misaligned GCRef %#x", uint64(ref)))
	}
	a.words[ref>>3] = v
}

// grow doubles the capacity until at least minBytes is available, then copies the old
// backing into the new one. GCRefs/bump remain valid (offset addressing's payoff).
func (a *Arena) grow(minBytes uint32) {
	newCap := a.cap
	if newCap == 0 {
		newCap = nullReserve
	}
	for newCap < minBytes {
		// 翻倍直到够用。注意 uint32 溢出。
		if newCap > a.maxCap/2 {
			newCap = a.maxCap
			break
		}
		newCap *= 2
	}
	if newCap < minBytes {
		panic(fmt.Sprintf("arena: cannot grow to %d bytes (max %d)", minBytes, a.maxCap))
	}
	newWords := a.backing(newCap / 8)
	copy(newWords, a.words)
	a.cap = newCap
	a.setBacking(newWords)
}

func roundUp8(n uint32) uint32 { return (n + 7) &^ 7 }
