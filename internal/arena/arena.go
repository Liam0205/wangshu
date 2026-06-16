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

// 上限。bump/cap 用 uint32 ⇒ uint32 寻址理论上限 4 GiB,实际取 2 GiB
// 留一半 headroom 防 uint32 边界溢出(06 §3)。
const (
	// MaxBytes 是单 arena 容量上限。
	MaxBytes uint32 = 1 << 31 // 2 GiB
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
	// InPlaceBacking 标记 NewBacking 是「原地扩展」语义(P3 收养 wazero
	// linear memory:memory.grow 原地扩,返回的新视图已含旧数据)而非
	// 「realloc」语义(P1 默认 make:新地址,需 copy 旧内容)。
	//
	// false(P1 默认):grow 时 copy(newWords, oldWords) 迁移旧数据。
	// true(P3 收养):grow 时**不 copy**——新视图已含旧数据,且旧视图在
	//   wazero memory.grow 后已 disconnect(PW0 spike 实测),copy 源是 UB。
	//
	// 详见 docs/design/p3-wasm-tier/03-memory-model.md §1.6。
	InPlaceBacking bool
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
	inPlace bool // Options.InPlaceBacking:grow 时是否跳过 copy(P3 收养语义)

	// freelist(06 §2):20 个 small size-class 定长桶 + LARGE 多桶(power-of-2
	// 字数,桶 0=65..128 字 / 桶 1=129..256 / .../桶 23=...)。空闲块以 GCRef 偏移
	// 串链(word0 = next, word1 = words),grow 后偏移不失效。
	// **LARGE 多桶(issue #10 root fix)**:旧单链 first-fit 在反复分配单调递增
	// 尺寸(如 rehash array 段 doublings)下产生 O(N) 链长 + O(N) 扫描成本 ⟹ O(N²)
	// 整体退化;multi-bucket 后每次 alloc 扫桶内短链,典型 power-of-2 alloc O(1) 命中。
	freeHeads      [numSizeClasses]GCRef
	largeFreeHeads [numLargeClasses]GCRef
	freeBytes      uint64 // freelist 上的总空闲字节(观测/测试)

	// freeSet/freeSite(debugFreelist 排障用):当前空闲的全部字偏移与释放点。
	freeSet  map[GCRef]uint32
	freeSite map[GCRef]string
}

// New creates an Arena with the given options.
// InitialBytes > MaxBytes 直接 panic(fail-fast,与 grow 超限一致,不静默截断)。
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
		panic(fmt.Sprintf("arena: InitialBytes %d exceeds MaxBytes %d", cap, max))
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
		inPlace: opts.InPlaceBacking,
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

// Bump returns the next unallocated byte offset (includes the leading
// nullReserve; NOT net allocated bytes — subtract nullReserve for that).
func (a *Arena) Bump() uint32 { return a.bump }

// Words returns the word view of the entire backing. **The returned slice is invalidated
// by any subsequent allocation that triggers a grow** — callers must re-fetch after such ops
// (engineering.md §-2 约束:reloadFrame 纪律).
func (a *Arena) Words() []uint64 { return a.words }

// Bytes returns the byte view of the entire backing. Same invalidation rule as Words.
func (a *Arena) Bytes() []byte { return a.bytes }

// AllocBytes 分配 nbytes 字节(自动向上对齐到 8),返回起始字节偏移 GCRef。
//
// 两级:freelist 命中(size-class 定长桶 / LARGE 首次适配)→ bump 线性切分。
// 不写 GCHeader、不挂 sweep 链——那是 gc 包的责任。
//
// 注意:freelist 复用的内存是脏的(残留旧对象内容/链指针),调用方(object
// 构造函数)必须显式初始化全部字段。
//
// 容量不足 → 触发 grow(GC 由 gc 包的 MaybeCollect 在分配计费路径介入)。
// 超 maxCap(含 uint32 回绕级请求)→ panic(fail-fast,不静默错果)。
func (a *Arena) AllocBytes(nbytes uint32) GCRef {
	// 尺寸检查在 uint64 域:nbytes 接近 0xFFFFFFFF 时 roundUp8 / bump+need
	// 都会回绕成小值,4 GiB 请求被静默"成功"切走 8 字节(错误别名)。
	if uint64(nbytes) > uint64(a.maxCap) {
		panic(fmt.Sprintf("arena: allocation of %d bytes exceeds max capacity %d", nbytes, a.maxCap))
	}
	need := roundUp8(nbytes)
	if need == 0 {
		need = 8 // 至少分配一字,避免零长引用
	}
	words := need / 8
	if words <= largeThresholdWords {
		c := sizeClass(words)
		if ref := a.popSizeClass(c); !ref.IsNull() {
			a.zeroFill(ref, classWords(c))
			return ref
		}
		// 桶内尺寸统一:bump 也按桶代表字数切(保证未来 Free 回桶可复用)
		need = classWords(c) * 8
	} else if ref := a.popLarge(words); !ref.IsNull() {
		a.zeroFill(ref, words)
		return ref
	}
	if uint64(a.bump)+uint64(need) > uint64(a.cap) {
		a.grow64(uint64(a.bump) + uint64(need))
	}
	ref := GCRef(a.bump)
	a.bump += need
	return ref
}

// zeroFill 清零一个复用块(freelist 内存是脏的;bump 区新内存天然是零,
// 统一在此清使两路分配对调用方等价)。
func (a *Arena) zeroFill(ref GCRef, words uint32) {
	base := ref >> 3
	for i := uint32(0); i < words; i++ {
		a.words[base+GCRef(i)] = 0
	}
}

// AllocWords is a convenience wrapper for AllocBytes(words*8).
// words > MaxBytes/8 级请求在乘法回绕前拦截(fail-fast)。
func (a *Arena) AllocWords(words uint32) GCRef {
	if uint64(words)*8 > uint64(a.maxCap) {
		panic(fmt.Sprintf("arena: allocation of %d words exceeds max capacity %d bytes", words, a.maxCap))
	}
	return a.AllocBytes(words * 8)
}

// WordAt returns the uint64 at the given GCRef (interpreted as a word offset by ref/8).
// Panics on misaligned ref.
func (a *Arena) WordAt(ref GCRef) uint64 {
	if ref&7 != 0 {
		panic(fmt.Sprintf("arena: misaligned GCRef %#x", uint64(ref)))
	}
	if debugFreelist && a.freeSet[ref] != 0 {
		panic(fmt.Sprintf("arena: read of freed word %#x freed at %s", uint64(ref), a.FreeSiteOf(ref)))
	}
	return a.words[ref>>3]
}

// SetWordAt writes v at the given GCRef.
func (a *Arena) SetWordAt(ref GCRef, v uint64) {
	if ref&7 != 0 {
		panic(fmt.Sprintf("arena: misaligned GCRef %#x", uint64(ref)))
	}
	if debugFreelist && a.freeSet[ref] != 0 {
		panic(fmt.Sprintf("arena: write to freed word %#x", uint64(ref)))
	}
	a.words[ref>>3] = v
}

// grow64 doubles the capacity until at least minBytes is available, then copies the old
// backing into the new one. GCRefs/bump remain valid (offset addressing's payoff).
// minBytes 在 uint64 域(调用方 bump+need 可能超 uint32)。
func (a *Arena) grow64(minBytes uint64) {
	if minBytes > uint64(a.maxCap) {
		panic(fmt.Sprintf("arena: cannot grow to %d bytes (max %d)", minBytes, a.maxCap))
	}
	newCap := a.cap
	if newCap == 0 {
		newCap = nullReserve
	}
	for uint64(newCap) < minBytes {
		// 翻倍直到够用。注意 uint32 溢出。
		if newCap > a.maxCap/2 {
			newCap = a.maxCap
			break
		}
		newCap *= 2
	}
	if uint64(newCap) < minBytes {
		panic(fmt.Sprintf("arena: cannot grow to %d bytes (max %d)", minBytes, a.maxCap))
	}
	newWords := a.backing(newCap / 8)
	// P1 默认(realloc 语义):新 backing 是新地址,copy 迁移旧数据。
	// P3 收养(InPlaceBacking):memory.grow 原地扩,newWords 已含旧数据,
	// 且旧 a.words 视图在 grow 后已 disconnect(PW0 spike 实测),不能 copy。
	if !a.inPlace {
		copy(newWords, a.words)
	}
	a.cap = newCap
	a.setBacking(newWords)
}

func roundUp8(n uint32) uint32 { return (n + 7) &^ 7 }
