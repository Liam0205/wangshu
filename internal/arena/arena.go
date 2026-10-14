// Package arena provides the self-managed linear memory backing the value world.
//
// Design: docs/design/p1-interpreter/06-memory-gc.md §1-§3.
//
// Core contract:
//   - Dual-view backing: the same underlying storage is aliased via unsafe into
//     []uint64 (word view) and []byte (byte view).
//   - Offset addressing: GCRef is a 48-bit byte offset (a plain integer to the Go
//     GC, sidestepping the write-barrier tax).
//   - 8-byte alignment: all allocations are word-aligned; the low 3 bits of a
//     GCRef are always 0.
//   - Offset 0 is reserved as the null GCRef ("no object" semantically); bump
//     starts at 8.
//   - grow doubles the capacity; the internal reference relationships expressed
//     via GCRef stay unchanged (the payoff of offset addressing).
//   - The backing comes from an injection point (NewBacking), replaced in P3 by
//     the wazero linear memory adapter.
//
// This stage has no GCHeader / freelist / sweep chain: those are wired in by the
// gc package (M5). This package only exposes the AllocBytes byte-allocation
// primitive plus Words/Bytes dual-view access.
package arena

import (
	"fmt"
	"unsafe"
)

// GCRef is a byte-offset reference to an object within the arena (48-bit
// effective, carried in a uint64 for easy NaN-box embedding).
// 0 = null (no object); the low 3 bits of a non-zero value are always 0
// (8-byte alignment).
type GCRef uint64

// IsNull reports whether ref is the null reference.
func (r GCRef) IsNull() bool { return r == 0 }

// Limits. bump/cap use uint32 ⇒ the theoretical uint32 addressing ceiling is
// 4 GiB; in practice we cap at 2 GiB, leaving half as headroom against uint32
// boundary overflow (06 §3).
const (
	// MaxBytes is the per-arena capacity ceiling.
	MaxBytes uint32 = 1 << 31 // 2 GiB
	// nullReserve is the reserved region at offset 0 (bytes). bump starts at 8.
	nullReserve uint32 = 8
)

// BackingFn is the factory for backing memory. The P1 default is make([]uint64, n);
// P3 replaces it with the wazero linear memory adapter (per the backfill request in
// 06 §1.1 and p3-wasm-tier §4.2).
type BackingFn func(words uint32) []uint64

// DefaultBacking is the P1 default backing factory (pure Go heap allocation).
func DefaultBacking(words uint32) []uint64 { return make([]uint64, words) }

// Options configures Arena behavior.
type Options struct {
	// InitialBytes is the initial capacity (bytes, rounded up to a multiple of 8).
	// Zero value = 64 KiB.
	InitialBytes uint32
	// MaxBytes is the ceiling (bytes). Zero value = arena.MaxBytes.
	MaxBytes uint32
	// NewBacking is the backing factory; nil uses DefaultBacking.
	NewBacking BackingFn
	// InPlaceBacking marks NewBacking as having "in-place grow" semantics (P3
	// adopting wazero linear memory: memory.grow extends in place, and the new
	// view returned already contains the old data) rather than "realloc"
	// semantics (the P1 default make: new address, requiring a copy of the old
	// contents).
	//
	// false (P1 default): grow copies via copy(newWords, oldWords) to migrate old data.
	// true (P3 adoption): grow does **not** copy — the new view already contains
	//   the old data, and the old view is disconnected after wazero memory.grow
	//   (verified by the PW0 spike), so copying from it is UB.
	//
	// See docs/design/p3-wasm-tier/03-memory-model.md §1.6 for details.
	InPlaceBacking bool
}

// Arena is self-managed linear memory. It holds the backing dual views and the
// bump pointer.
//
// Note: objects inside an Arena cross-reference via GCRef (integers), so the Go
// GC cannot see the internal graph — this is the physical means of isolating the
// Go write-barrier tax (roadmap §2).
type Arena struct {
	words   []uint64 // word view (the real backing)
	bytes   []byte   // byte view (unsafe alias sharing the same memory as words)
	bump    uint32   // next unallocated byte offset (always 8-aligned)
	cap     uint32   // current capacity (bytes, = len(words)*8)
	maxCap  uint32   // ceiling (bytes)
	backing BackingFn
	inPlace bool // Options.InPlaceBacking: whether grow skips the copy (P3 adoption semantics)

	// freelist (06 §2): 20 small size-class fixed-length buckets + LARGE
	// multi-bucket (power-of-2 word counts, bucket 0=65..128 words / bucket
	// 1=129..256 / ... / bucket 23=...). Free blocks are chained by GCRef offset
	// (word0 = next, word1 = words), and offsets survive a grow.
	// **LARGE multi-bucket (issue #10 root fix)**: the old single-chain first-fit,
	// under repeated allocation of monotonically increasing sizes (e.g. rehash
	// array segment doublings), produces O(N) chain length + O(N) scan cost ⟹
	// O(N²) overall degradation; with multi-bucket, each alloc scans a short
	// in-bucket chain, and a typical power-of-2 alloc hits in O(1).
	// LARGE blocks are doubly linked (word2 = prev) so coalescing can unlink a
	// neighbour from the middle of its bucket.
	freeHeads      [numSizeClasses]GCRef
	largeFreeHeads [numLargeClasses]GCRef
	freeBytes      uint64 // total free bytes on the freelist (observation/testing)

	// Address index of the free LARGE blocks (issue #296): start → words and
	// end → start, used by freeLarge to find the free neighbours of a returned
	// block. Kept on the Go side because allocated blocks carry no boundary
	// tags; offsets survive grow and Compact unchanged.
	largeByStart map[GCRef]uint32
	largeByEnd   map[GCRef]GCRef

	// freeSet/freeSite (for debugFreelist troubleshooting): all currently free
	// word offsets and their free sites.
	freeSet  map[GCRef]uint32
	freeSite map[GCRef]string
}

// New creates an Arena with the given options.
// InitialBytes > MaxBytes panics outright (fail-fast, consistent with grow
// overflow, no silent truncation).
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
// Note the dual-view construction order: derive bytes from words (06 §1.1: a
// []byte start address is not guaranteed 8-aligned, and deriving in reverse
// would trigger unaligned uint64 reads on some platforms).
func (a *Arena) setBacking(words []uint64) {
	if len(words)*8 > int(a.maxCap) || len(words)*8 < int(a.cap) {
		// Defensive: the backing must be able to hold a.cap bytes.
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
// (engineering.md §-2 constraint: reloadFrame discipline).
func (a *Arena) Words() []uint64 { return a.words }

// Bytes returns the byte view of the entire backing. Same invalidation rule as Words.
func (a *Arena) Bytes() []byte { return a.bytes }

// Compact swaps the backing for a smaller slab when the current one is more
// than twice what the heap is expected to need (issue #11 direction 1, policy
// from issue #296). GCRef offsets stay unchanged; only the physical slab is
// replaced, and the Go runtime reclaims the old one (possibly large, e.g. once
// doubled to a high-water mark by a transient large allocation), easing the
// problem of a fat state permanently residing in a long-lived State pool.
//
// target is the size the heap should fit in until the next collection; the
// collector passes the live bytes plus the next GC threshold, with some
// headroom (Collector.Collect). The new capacity is max(target, bump, 64 KiB),
// and the slab is replaced only when cap exceeds twice that. The factor of two
// is what stops Compact and grow from alternating: grow doubles, so shrinking
// to exactly what is in use made the next allocation burst grow (and copy the
// whole slab) again, every cycle.
//
// **Conditions for effect**:
//   - non-InPlaceBacking mode (P3 wangshu_p3 adopting wazero linear memory
//     cannot shrink; this method is a no-op there)
//   - cap > 2 × max(target, bump, 64 KiB)
//
// **Does not touch bump, does not remap GCRef**: this method is not a
// copy-compact GC, it only swaps the slab. Dead blocks below bump stay on the
// freelist for reuse; LARGE ones are coalesced and the block at the top lowers
// bump when it is freed (freeLarge), so bump tracks the highest live block.
func (a *Arena) Compact(target uint64) {
	if a.inPlace {
		return // P3 adoption of wazero linear memory cannot shrink
	}
	const minCap = uint64(64 * 1024)
	newCap := (target + 7) &^ 7
	if b := uint64(a.bump); newCap < b {
		newCap = (b + 7) &^ 7
	}
	if newCap < minCap {
		newCap = minCap
	}
	if newCap > uint64(a.maxCap) {
		newCap = uint64(a.maxCap)
	}
	if uint64(a.cap) <= 2*newCap {
		return // not enough slack to be worth a copy
	}
	newWords := a.backing(uint32(newCap / 8))
	copy(newWords, a.words[:a.bump/8]) // copy only [0..bump); the rest is unallocated
	a.cap = uint32(newCap)
	a.setBacking(newWords)
}

// AllocBytes allocates nbytes bytes (rounded up to 8), returning the starting
// byte offset as a GCRef.
//
// Two levels: freelist hit (size-class fixed buckets / LARGE first-fit) → bump
// linear carve. Does not write a GCHeader or attach a sweep chain — that is the
// gc package's responsibility.
//
// Note: memory reused from the freelist is dirty (residual old object
// content/chain pointers); the caller (the object constructor) must explicitly
// initialize all fields.
//
// Insufficient capacity → grow. The arena never collects: allocation is charged through
// gc.Collector.AllocCharge, and collection runs at the collector's check points
// (Collector.MaybeCollect at the VM safepoints, Collector.HostSafepoint when the opt-in
// switch is on and a public API call returns).
// Exceeding maxCap (including uint32-wraparound-level requests) → panic
// (fail-fast, no silent wrong result).
func (a *Arena) AllocBytes(nbytes uint32) GCRef {
	// Do the size check in the uint64 domain: when nbytes is near 0xFFFFFFFF,
	// both roundUp8 and bump+need wrap to a small value, so a 4 GiB request would
	// be silently "succeed" by carving off 8 bytes (a wrong alias).
	if uint64(nbytes) > uint64(a.maxCap) {
		panic(fmt.Sprintf("arena: allocation of %d bytes exceeds max capacity %d", nbytes, a.maxCap))
	}
	need := roundUp8(nbytes)
	if need == 0 {
		need = 8 // allocate at least one word, avoiding a zero-length reference
	}
	words := need / 8
	if words <= largeThresholdWords {
		c := sizeClass(words)
		if ref := a.popSizeClass(c); !ref.IsNull() {
			a.zeroFill(ref, classWords(c))
			return ref
		}
		// Uniform size within a bucket: bump also carves by the bucket's
		// representative word count (ensuring a future Free back to the bucket can reuse it).
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

// zeroFill clears a reused block (freelist memory is dirty; freshly bumped
// memory is naturally zero — clearing uniformly here makes both allocation paths
// equivalent to the caller).
func (a *Arena) zeroFill(ref GCRef, words uint32) {
	base := ref >> 3
	for i := uint32(0); i < words; i++ {
		a.words[base+GCRef(i)] = 0
	}
}

// AllocWords is a convenience wrapper for AllocBytes(words*8).
// words > MaxBytes/8 level requests are intercepted before the multiplication
// wraps around (fail-fast).
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
// minBytes is in the uint64 domain (the caller's bump+need may exceed uint32).
func (a *Arena) grow64(minBytes uint64) {
	if minBytes > uint64(a.maxCap) {
		panic(fmt.Sprintf("arena: cannot grow to %d bytes (max %d)", minBytes, a.maxCap))
	}
	newCap := a.cap
	if newCap == 0 {
		newCap = nullReserve
	}
	for uint64(newCap) < minBytes {
		// Double until sufficient. Watch out for uint32 overflow.
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
	// P1 default (realloc semantics): the new backing is a new address, so copy
	// to migrate the old data.
	// P3 adoption (InPlaceBacking): memory.grow extends in place, newWords
	// already contains the old data, and the old a.words view is disconnected
	// after the grow (verified by the PW0 spike), so it must not be copied.
	if !a.inPlace {
		copy(newWords, a.words)
	}
	a.cap = newCap
	a.setBacking(newWords)
}

func roundUp8(n uint32) uint32 { return (n + 7) &^ 7 }
