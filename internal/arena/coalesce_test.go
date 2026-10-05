package arena

import (
	"math/rand"
	"sort"
	"testing"
)

// checkLargeIndex verifies the LARGE freelist invariants: every bucket chain
// is a well-formed doubly linked list whose blocks belong to that bucket, the
// chains and the address index hold the same blocks, no two free blocks are
// adjacent (they would have been merged), none ends at bump, and the free
// byte count matches.
func checkLargeIndex(t *testing.T, a *Arena) {
	t.Helper()
	seen := map[GCRef]uint32{}
	var largeBytes uint64
	for c := 0; c < numLargeClasses; c++ {
		var prev GCRef
		for ref := a.largeFreeHeads[c]; !ref.IsNull(); ref = GCRef(a.words[ref>>3]) {
			w := uint32(a.words[(ref>>3)+1])
			if largeSizeClass(w) != c {
				t.Fatalf("block %d (%d words) in bucket %d, want %d", ref, w, c, largeSizeClass(w))
			}
			if back := GCRef(a.words[(ref>>3)+2]); back != prev {
				t.Fatalf("block %d prev link %d, want %d", ref, back, prev)
			}
			if _, dup := seen[ref]; dup {
				t.Fatalf("block %d on two chains", ref)
			}
			seen[ref] = w
			largeBytes += uint64(w) * 8
			prev = ref
		}
	}
	if len(seen) != len(a.largeByStart) || len(seen) != len(a.largeByEnd) {
		t.Fatalf("index size mismatch: chains %d byStart %d byEnd %d", len(seen), len(a.largeByStart), len(a.largeByEnd))
	}
	starts := make([]GCRef, 0, len(seen))
	for ref, w := range seen {
		if a.largeByStart[ref] != w {
			t.Fatalf("byStart[%d]=%d, chain says %d", ref, a.largeByStart[ref], w)
		}
		end := ref + GCRef(w)*8
		if a.largeByEnd[end] != ref {
			t.Fatalf("byEnd[%d]=%d, want %d", end, a.largeByEnd[end], ref)
		}
		if end == GCRef(a.bump) {
			t.Fatalf("free block %d ends at bump %d (bump should have retreated)", ref, a.bump)
		}
		if uint64(end) > uint64(a.bump) {
			t.Fatalf("free block %d..%d past bump %d", ref, end, a.bump)
		}
		starts = append(starts, ref)
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i] < starts[j] })
	for i := 1; i < len(starts); i++ {
		prevEnd := starts[i-1] + GCRef(seen[starts[i-1]])*8
		if prevEnd > starts[i] {
			t.Fatalf("free blocks overlap: %d..%d and %d", starts[i-1], prevEnd, starts[i])
		}
		if prevEnd == starts[i] {
			t.Fatalf("adjacent free blocks %d and %d were not coalesced", starts[i-1], starts[i])
		}
	}
	var smallBytes uint64
	for c := 0; c < numSizeClasses; c++ {
		for ref := a.freeHeads[c]; !ref.IsNull(); ref = GCRef(a.words[ref>>3]) {
			smallBytes += uint64(classWords(c)) * 8
		}
	}
	if a.freeBytes != largeBytes+smallBytes {
		t.Fatalf("freeBytes %d, chains hold %d (large %d + small %d)", a.freeBytes, largeBytes+smallBytes, largeBytes, smallBytes)
	}
}

// TestCoalesce_ForwardBackwardBoth frees three adjacent LARGE blocks in an
// order that exercises merging with the next block, the previous block, and
// both at once; the result is one block covering all three.
func TestCoalesce_ForwardBackwardBoth(t *testing.T) {
	a := New(Options{InitialBytes: 1 << 16})
	x := a.AllocBytes(100 * 8)
	y := a.AllocBytes(200 * 8)
	z := a.AllocBytes(300 * 8)
	_ = a.AllocBytes(8) // guard: keeps the run off the top
	a.Free(z, 300*8)
	a.Free(x, 100*8)
	checkLargeIndex(t, a)
	if len(a.largeByStart) != 2 {
		t.Fatalf("x and z are not adjacent; want 2 free blocks, got %d", len(a.largeByStart))
	}
	a.Free(y, 200*8) // merges with x before and z after
	checkLargeIndex(t, a)
	if w, ok := a.largeByStart[x]; !ok || w != 600 {
		t.Fatalf("want one 600-word block at %d, got index %v", x, a.largeByStart)
	}
	// The merged block serves a request none of the parts could.
	if got := a.AllocBytes(550 * 8); got != x {
		t.Fatalf("merged block not reused: want %d got %d", x, got)
	}
	checkLargeIndex(t, a)
}

// TestCoalesce_UnlinkFromMiddleOfBucket merges with a neighbour that is not
// at the head of its bucket chain (the doubly linked chain must splice it out).
func TestCoalesce_UnlinkFromMiddleOfBucket(t *testing.T) {
	a := New(Options{InitialBytes: 1 << 16})
	p := a.AllocBytes(200 * 8)
	q := a.AllocBytes(200 * 8)
	_ = a.AllocBytes(8)
	r := a.AllocBytes(200 * 8)
	_ = a.AllocBytes(8)
	s := a.AllocBytes(200 * 8)
	_ = a.AllocBytes(8)
	a.Free(p, 200*8) // bucket 1: p
	a.Free(r, 200*8) // bucket 1: r, p
	a.Free(s, 200*8) // bucket 1: s, r, p
	a.Free(q, 200*8) // merges with p (tail of the chain) → 400 words in bucket 2
	checkLargeIndex(t, a)
	if w := a.largeByStart[p]; w != 400 {
		t.Fatalf("p+q not merged: %v", a.largeByStart)
	}
}

// TestCoalesce_BumpRetreat frees the block at the top: bump moves back to its
// start, and freeing the block below it then moves bump further (the merged
// block reaches bump again).
func TestCoalesce_BumpRetreat(t *testing.T) {
	a := New(Options{InitialBytes: 1 << 16})
	g := a.AllocBytes(8)
	x := a.AllocBytes(100 * 8)
	y := a.AllocBytes(200 * 8)
	for i := uint32(0); i < 200; i++ {
		a.SetWordAt(y+GCRef(i*8), 0xDEADBEEF)
	}
	a.Free(y, 200*8)
	if a.Bump() != uint32(y) {
		t.Fatalf("bump %d, want %d after freeing the top block", a.Bump(), y)
	}
	if a.FreeBytes() != 0 {
		t.Fatalf("a retreated block must not stay on the freelist: freeBytes=%d", a.FreeBytes())
	}
	a.Free(x, 100*8)
	if a.Bump() != uint32(g)+8 {
		t.Fatalf("bump %d, want %d after freeing the next block down", a.Bump(), uint32(g)+8)
	}
	checkLargeIndex(t, a)
	// Bump-carved memory is handed out without zero-fill, so the retreat must
	// have cleared the old contents.
	r := a.AllocBytes(400 * 8)
	for i := uint32(0); i < 400; i++ {
		if w := a.WordAt(r + GCRef(i*8)); w != 0 {
			t.Fatalf("word %d of a block carved above a retreated bump is %#x", i, w)
		}
	}
}

// TestCoalesce_RetreatAbsorbsFreeRun frees a run of LARGE blocks bottom-up:
// each stays on the freelist (merged into one block) until the topmost is
// freed, which takes the whole run back into bump.
func TestCoalesce_RetreatAbsorbsFreeRun(t *testing.T) {
	a := New(Options{InitialBytes: 1 << 16})
	_ = a.AllocBytes(8)
	base := a.Bump()
	var refs []GCRef
	for i := 0; i < 8; i++ {
		refs = append(refs, a.AllocBytes(128*8))
	}
	for _, r := range refs[:7] {
		a.Free(r, 128*8)
	}
	checkLargeIndex(t, a)
	if len(a.largeByStart) != 1 || a.largeByStart[GCRef(base)] != 7*128 {
		t.Fatalf("run not merged into one block: %v", a.largeByStart)
	}
	a.Free(refs[7], 128*8)
	if a.Bump() != base || a.FreeBytes() != 0 || len(a.largeByStart) != 0 {
		t.Fatalf("bump %d (want %d), freeBytes %d, index %v", a.Bump(), base, a.FreeBytes(), a.largeByStart)
	}
}

// TestCoalesce_SmallBlocksDoNotMerge: small blocks stay in their buckets even
// when adjacent to a free LARGE block, and do not stop the LARGE block from
// lowering bump.
func TestCoalesce_SmallBlocksDoNotMerge(t *testing.T) {
	a := New(Options{InitialBytes: 1 << 16})
	s := a.AllocBytes(8 * 8)
	x := a.AllocBytes(100 * 8)
	a.Free(s, 8*8)
	a.Free(x, 100*8)
	if a.Bump() != uint32(x) {
		t.Fatalf("bump %d, want %d", a.Bump(), x)
	}
	if got := a.AllocBytes(8 * 8); got != s {
		t.Fatalf("small block not reused from its bucket: want %d got %d", s, got)
	}
	checkLargeIndex(t, a)
}

// TestCoalesce_RandomModel runs random alloc/free sequences against a model of
// the live blocks, checking after every step that live blocks never overlap
// each other or a free block, keep their contents, and that the LARGE
// invariants hold. Ends by freeing everything: bump returns to its start
// unless small blocks remain below.
func TestCoalesce_RandomModel(t *testing.T) {
	type blk struct {
		ref   GCRef
		bytes uint32
		words uint32
		tag   uint64
	}
	for seed := int64(1); seed <= 20; seed++ {
		rng := rand.New(rand.NewSource(seed))
		a := New(Options{InitialBytes: 1 << 16})
		var live []blk
		tag := uint64(0)
		for step := 0; step < 3000; step++ {
			if len(live) == 0 || rng.Intn(100) < 55 {
				var words uint32
				switch rng.Intn(4) {
				case 0:
					words = uint32(1 + rng.Intn(64))
				default:
					words = uint32(65 + rng.Intn(2000))
				}
				ref := a.AllocBytes(words * 8)
				real := words
				if words <= largeThresholdWords {
					real = classWords(sizeClass(words))
				}
				for i := uint32(0); i < real; i++ {
					if w := a.WordAt(ref + GCRef(i*8)); w != 0 {
						t.Fatalf("seed %d step %d: fresh block %d word %d = %#x", seed, step, ref, i, w)
					}
				}
				tag++
				for i := uint32(0); i < real; i++ {
					a.SetWordAt(ref+GCRef(i*8), tag<<20|uint64(i))
				}
				live = append(live, blk{ref, words * 8, real, tag})
			} else {
				i := rng.Intn(len(live))
				b := live[i]
				for w := uint32(0); w < b.words; w++ {
					if got := a.WordAt(b.ref + GCRef(w*8)); got != b.tag<<20|uint64(w) {
						t.Fatalf("seed %d step %d: live block %d word %d clobbered: %#x", seed, step, b.ref, w, got)
					}
				}
				a.Free(b.ref, b.bytes)
				live[i] = live[len(live)-1]
				live = live[:len(live)-1]
			}
			if step%50 == 0 {
				checkLargeIndex(t, a)
				// Live blocks and free LARGE blocks together must be disjoint
				// and below bump.
				type span struct {
					start, end GCRef
					free       bool
				}
				var spans []span
				for _, b := range live {
					spans = append(spans, span{b.ref, b.ref + GCRef(b.words)*8, false})
				}
				for ref, w := range a.largeByStart {
					spans = append(spans, span{ref, ref + GCRef(w)*8, true})
				}
				sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
				for i, sp := range spans {
					if uint64(sp.end) > uint64(a.bump) {
						t.Fatalf("seed %d step %d: block %d..%d (free=%v) past bump %d", seed, step, sp.start, sp.end, sp.free, a.bump)
					}
					if i+1 < len(spans) && sp.end > spans[i+1].start {
						t.Fatalf("seed %d step %d: %d..%d (free=%v) overlaps %d (free=%v)", seed, step, sp.start, sp.end, sp.free, spans[i+1].start, spans[i+1].free)
					}
				}
			}
		}
		for _, b := range live {
			a.Free(b.ref, b.bytes)
		}
		checkLargeIndex(t, a)
	}
}
