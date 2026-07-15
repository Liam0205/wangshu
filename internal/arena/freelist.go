// Freelist — size-class fixed-length buckets + LARGE first-fit (06 §2).
//
// Allocation has two levels: freelist hit (O(1) pop reuse) → bump linear split.
// Reclamation (gc sweep / rehash segment swap) returns via Free:
//   - ≤64 words: enter the fixed-length bucket by size-class (allocation already
//     rounds up to the bucket's representative word count, so sizes within a
//     bucket are uniform);
//   - >64 words: enter the LARGE intrusive chain (word0=next, word1=block words),
//     first-fit, only taken on an "exact hit" or when "the remainder >64 words
//     can form an independent block" (to avoid out-of-bucket fragmentation waste).
//
// No coalescing, no cross-class splitting (06 §2.2 P1 simplification).
// Reused memory is dirty: the constructor must explicitly initialize all fields
// and must not rely on fresh-zero (AllocString's NUL fill was changed to explicit
// zeroing).
package arena

import (
	"math/bits"
	"runtime"
)

// size-class partition (06 §2.2): 20 small buckets + LARGE multi-bucket
// (power-of-2 word counts).
const (
	numSizeClasses      = 20
	largeThresholdWords = 64
	// LARGE multi-bucket (issue #10 root fix): bucket i corresponds to words ∈
	// ((1<<(6+i)), (1<<(7+i))], covering 65..maxAlloc. numLargeClasses=24 →
	// bucket 23 = (1<<29)..(1<<30) words = 4..8 GB, far above MaxBytes=2 GiB (no
	// overflow risk).
	numLargeClasses = 24
)

// largeSizeClass maps words > 64 to a power-of-2 bucket number (0..23).
// bucket 0 = 65..128 words, bucket 1 = 129..256, bucket 2 = 257..512, ...
// out-of-range values clamp to the last bucket (over maxCap is already rejected
// by AllocBytes).
//
// Implementation: bucket = ceil(log2(words)) - 7 = bits.Len32(words-1) - 7.
// (bits.Len32(x-1) == ceil(log2(x)) holds for x>=2 — for x=2^n it takes that power)
// The call contract words > 64 ⟹ words-1 ≥ 64 ⟹ Len32 ≥ 7 ⟹ c ≥ 0, so no lower
// bound guard is needed.
func largeSizeClass(words uint32) int {
	c := int(bits.Len32(words-1)) - 7
	if c >= numLargeClasses {
		return numLargeClasses - 1
	}
	return c
}

// sizeClass maps a word count (1..64) to a bucket number.
func sizeClass(words uint32) int {
	switch {
	case words <= 8:
		return int(words - 1)
	case words <= 16:
		return int(8 + (words-9)/2)
	case words <= 32:
		return int(12 + (words-17)/4)
	default:
		return int(16 + (words-33)/8)
	}
}

// classWords returns the bucket's representative word count (blocks in a bucket
// are all allocated at this size).
func classWords(c int) uint32 {
	switch {
	case c < 8:
		return uint32(c + 1)
	case c < 12:
		return uint32(10 + 2*(c-8))
	case c < 16:
		return uint32(20 + 4*(c-12))
	default:
		return uint32(40 + 8*(c-16))
	}
}

// debugFreelist turns on Free/Alloc pairing assertions and use-after-free
// detection (double free / overlapping free interval / read-write of freed
// words). Normally off; set to true and recompile when debugging.
const debugFreelist = false

// FreeSiteOf returns the recorded free site in debug mode (for debugging).
func (a *Arena) FreeSiteOf(ref GCRef) string {
	if a.freeSite == nil {
		return "?"
	}
	return a.freeSite[ref]
}

// Free returns a block previously allocated by AllocBytes to the freelist.
//
// nbytes must equal the request byte count at allocation time (Free internally
// rounds up to the actual block size by the same rules). The caller (gc sweep /
// rehash) guarantees no double return and no access via the old GCRef after
// returning.
func (a *Arena) Free(ref GCRef, nbytes uint32) {
	if ref.IsNull() {
		return
	}
	need := roundUp8(nbytes)
	if need == 0 {
		need = 8
	}
	words := need / 8
	if debugFreelist {
		if words <= largeThresholdWords {
			words = classWords(sizeClass(words))
		}
		if a.freeSet == nil {
			a.freeSet = map[GCRef]uint32{}
			a.freeSite = map[GCRef]string{}
		}
		for w := uint32(0); w < words; w++ {
			at := ref + GCRef(w*8)
			if _, dup := a.freeSet[at]; dup {
				panic("arena: double free / overlapping free at " + itoa(uint64(at)) + " base " + itoa(uint64(ref)))
			}
		}
		site := callerSite()
		for w := uint32(0); w < words; w++ {
			a.freeSet[ref+GCRef(w*8)] = 1
			a.freeSite[ref+GCRef(w*8)] = site
		}
		words = need / 8
	}
	if words <= largeThresholdWords {
		c := sizeClass(words)
		words = classWords(c)
		a.words[ref>>3] = uint64(a.freeHeads[c])
		a.freeHeads[c] = ref
		a.freeBytes += uint64(words) * 8
		return
	}
	a.pushLarge(ref, words)
}

func itoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

// callerSite returns a summary of Free's call stack (for debug).
func callerSite() string {
	var pcs [8]uintptr
	n := runtime.Callers(3, pcs[:])
	frames := runtime.CallersFrames(pcs[:n])
	out := ""
	for {
		f, more := frames.Next()
		out += f.Function + ":" + itoa(uint64(f.Line)) + " <- "
		if !more {
			break
		}
	}
	return out
}

// pushLarge hooks a >64-word block onto the head of its LARGE bucket
// (word0=next, word1=words). Bucketed by largeSizeClass(words); a typical
// power-of-2-word alloc hits a single bucket O(1).
func (a *Arena) pushLarge(ref GCRef, words uint32) {
	c := largeSizeClass(words)
	a.words[ref>>3] = uint64(a.largeFreeHeads[c])
	a.words[(ref>>3)+1] = uint64(words)
	a.largeFreeHeads[c] = ref
	a.freeBytes += uint64(words) * 8
}

// popSizeClass tries to pop a block from a fixed-length bucket (block size =
// classWords(c)).
func (a *Arena) popSizeClass(c int) GCRef {
	ref := a.freeHeads[c]
	if ref.IsNull() {
		return 0
	}
	a.freeHeads[c] = GCRef(a.words[ref>>3])
	a.freeBytes -= uint64(classWords(c)) * 8
	if debugFreelist {
		for w := uint32(0); w < classWords(c); w++ {
			delete(a.freeSet, ref+GCRef(w*8))
		}
	}
	return ref
}

// popLarge does multi-bucket first-fit (issue #10 root fix). Flow:
//
//	① compute the needed bucket c0 = largeSizeClass(needWords)
//	② first-fit scan within bucket c0 (use if bw ≥ needWords)
//	③ miss → climb buckets c0+1, c0+2, ...
//	④ on hit, split the remainder: >64 words go to the matching bucket, ≤64 words
//	   round down into a small bucket, and a tail ≤7 words is discarded
//
// Compared with the old single-chain first-fit: the scan range is limited to a
// short chain within a bucket; a typical power-of-2 alloc hits bucket c0's head
// exactly O(1). N=1000 rehash repeated doublings no longer blow up the LARGE
// chain length.
func (a *Arena) popLarge(needWords uint32) GCRef {
	c0 := largeSizeClass(needWords)
	for cc := c0; cc < numLargeClasses; cc++ {
		var prev GCRef
		ref := a.largeFreeHeads[cc]
		for !ref.IsNull() {
			next := GCRef(a.words[ref>>3])
			bw := uint32(a.words[(ref>>3)+1])
			if bw >= needWords {
				if prev.IsNull() {
					a.largeFreeHeads[cc] = next
				} else {
					a.words[prev>>3] = uint64(next)
				}
				a.freeBytes -= uint64(bw) * 8
				if debugFreelist {
					for w := uint32(0); w < bw; w++ {
						delete(a.freeSet, ref+GCRef(w*8))
					}
				}
				if rem := bw - needWords; rem > 0 {
					remRef := ref + GCRef(needWords*8)
					if rem > largeThresholdWords {
						if debugFreelist {
							for w := uint32(0); w < rem; w++ {
								a.freeSet[remRef+GCRef(w*8)] = 1
							}
						}
						a.pushLarge(remRef, rem)
					} else {
						if c := floorClass(rem); c >= 0 {
							a.Free(remRef, classWords(c)*8)
						}
					}
				}
				return ref
			}
			prev = ref
			ref = next
		}
	}
	return 0
}

// floorClass returns the largest size-class whose representative word count ≤
// words (-1 if none). An argument >64 words clamps to 64 (sizeClass's argument
// contract is 1..64; an out-of-range index panics).
func floorClass(words uint32) int {
	if words == 0 {
		return -1
	}
	if words > largeThresholdWords {
		words = largeThresholdWords
	}
	c := sizeClass(words)
	for c >= 0 && classWords(c) > words {
		c--
	}
	return c
}

// FreeBytes returns the total free bytes currently on the freelist (for
// test/observation).
func (a *Arena) FreeBytes() uint64 { return a.freeBytes }
