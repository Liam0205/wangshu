// Size-entry hardening regression.
//
// uint32 wraparound family: when nbytes is near 0xFFFFFFFF, roundUp8 /
// bump+need / words*8 all wrap around to small values, so an oversized
// request is silently "succeeded" and hands back an 8-byte slice (aliased
// error, silent wrong result). After hardening these must fail-fast panic.
package arena

import "testing"

func wantPanic(t *testing.T, name string, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s: expected panic, got silent success", name)
		}
	}()
	fn()
}

func TestAllocBytes_OverflowPanics(t *testing.T) {
	a := New(Options{InitialBytes: 64})
	wantPanic(t, "AllocBytes(0xFFFFFFFF)", func() { a.AllocBytes(0xFFFFFFFF) })
	wantPanic(t, "AllocBytes(0xFFFFFFF8)", func() { a.AllocBytes(0xFFFFFFF8) })
	wantPanic(t, "AllocBytes(maxCap+1)", func() { a.AllocBytes(MaxBytes + 1) })
}

func TestAllocWords_OverflowPanics(t *testing.T) {
	a := New(Options{InitialBytes: 64})
	wantPanic(t, "AllocWords(0x20000000)", func() { a.AllocWords(0x20000000) })
	wantPanic(t, "AllocWords(0xFFFFFFFF)", func() { a.AllocWords(0xFFFFFFFF) })
}

func TestNew_InitialBytesExceedsMaxPanics(t *testing.T) {
	wantPanic(t, "New(Initial>Max)", func() {
		New(Options{InitialBytes: 1024, MaxBytes: 512})
	})
}

func TestAllocBytes_SmallStillWorksAfterGuards(t *testing.T) {
	a := New(Options{InitialBytes: 64})
	r := a.AllocBytes(16)
	if r.IsNull() {
		t.Fatal("normal alloc returned null")
	}
	a.SetWordAt(r, 42)
	if a.WordAt(r) != 42 {
		t.Fatal("write/read roundtrip failed")
	}
}

// freelist behavior: reuse after Free, zero-fill the reused block, LARGE first-fit.
func TestFreelist_ReuseAndZeroFill(t *testing.T) {
	a := New(Options{InitialBytes: 4096})
	r1 := a.AllocBytes(24) // 3 words → class 2
	a.SetWordAt(r1, 0xDEADBEEF)
	a.SetWordAt(r1+8, 0xDEADBEEF)
	a.Free(r1, 24)
	r2 := a.AllocBytes(24)
	if r2 != r1 {
		t.Fatalf("size-class reuse failed: r1=%d r2=%d", r1, r2)
	}
	if a.WordAt(r2) != 0 || a.WordAt(r2+8) != 0 {
		t.Fatal("reused block not zero-filled")
	}
}

func TestFreelist_LargeFirstFit(t *testing.T) {
	a := New(Options{InitialBytes: 64 * 1024})
	big := a.AllocBytes(200 * 8) // 200 words > 64 → LARGE
	a.Free(big, 200*8)
	got := a.AllocBytes(200 * 8) // exact hit
	if got != big {
		t.Fatalf("LARGE exact-fit reuse failed: want %d got %d", big, got)
	}
	a.Free(got, 200*8)
	// 200-word block for a 70-word request: 130 remaining > 64, becomes its own LARGE block
	part := a.AllocBytes(70 * 8)
	if part != big {
		t.Fatalf("LARGE split reuse failed: want %d got %d", big, part)
	}
	if a.FreeBytes() == 0 {
		t.Fatal("split remainder not returned to freelist")
	}
	// 100-word block for a 70-word request: 30 remaining ≤ 64 → floored into a
	// fixed-size bucket (no longer stranded)
	small := a.AllocBytes(100 * 8)
	a.Free(small, 100*8)
	taken := a.AllocBytes(70 * 8)
	if taken != small {
		t.Fatalf("slightly-larger LARGE block must be taken (no stranding): want %d got %d", small, taken)
	}
	// remaining 30 words went into a bucket: a 28-word (class-14 representative)
	// request should hit that remainder block
	rem := a.AllocBytes(28 * 8)
	if rem != small+GCRef(70*8) {
		t.Fatalf("remainder not bucketed: want %d got %d", small+GCRef(70*8), rem)
	}
}

// --- LARGE multi-bucket size-class (issue #10 root fix) direct unit tests ---

// TestLargeSizeClass_Boundaries verifies largeSizeClass bucket mapping at
// power-of-2 boundaries plus in-between values.
// Bucket i covers words ∈ ((1<<(6+i)), (1<<(7+i))]: bucket 0=65..128, bucket 1=129..256, ...
func TestLargeSizeClass_Boundaries(t *testing.T) {
	cases := []struct {
		words  uint32
		bucket int
	}{
		{65, 0},       // bucket 0 start
		{100, 0},      // bucket 0 middle
		{128, 0},      // bucket 0 end (exactly 2^7)
		{129, 1},      // bucket 1 start
		{200, 1},      // bucket 1 middle
		{256, 1},      // bucket 1 end (exactly 2^8)
		{257, 2},      // bucket 2 start
		{512, 2},      // bucket 2 end
		{513, 3},      // bucket 3 start
		{1024, 3},     // bucket 3 end
		{1 << 20, 13}, // 1M words = 1<<20, bucket 13 (2^20)
		{1 << 28, 21}, // 2GB words upper limit (= MaxBytes/8 magnitude)
	}
	for _, c := range cases {
		if got := largeSizeClass(c.words); got != c.bucket {
			t.Errorf("largeSizeClass(%d) = %d, want %d", c.words, got, c.bucket)
		}
	}
}

// TestLargeSizeClass_Clamp verifies that oversized words (beyond the
// numLargeClasses range) clamp to the last bucket.
func TestLargeSizeClass_Clamp(t *testing.T) {
	if got := largeSizeClass(1 << 31); got != numLargeClasses-1 {
		t.Errorf("largeSizeClass clamp at boundary: got %d, want %d", got, numLargeClasses-1)
	}
}

// TestLargeFreelist_BucketSegregation verifies that blocks of different sizes go
// into different buckets. After Free'ing several sizes, repeatedly Alloc'ing the
// same size should hit the matching bucket directly, without scanning across buckets.
func TestLargeFreelist_BucketSegregation(t *testing.T) {
	a := New(Options{InitialBytes: 1 << 16})
	// Allocate 5 different sizes: 128, 256, 512, 1024, 2048 words (each in its own bucket)
	sizes := []uint32{128, 256, 512, 1024, 2048}
	refs := make([]GCRef, len(sizes))
	for i, s := range sizes {
		refs[i] = a.AllocBytes(s * 8)
	}
	// Free them all
	for i, s := range sizes {
		a.Free(refs[i], s*8)
	}
	// Repeatedly Alloc'ing the same size should hit the head of its own bucket (LIFO)
	for i, s := range sizes {
		got := a.AllocBytes(s * 8)
		if got != refs[i] {
			t.Errorf("size %d: bucket-segregated alloc failed: want %d got %d", s, refs[i], got)
		}
	}
}

// TestLargeFreelist_CrossBucketUpgrade verifies upgrading to bucket c0+1 when bucket c0 is empty.
func TestLargeFreelist_CrossBucketUpgrade(t *testing.T) {
	a := New(Options{InitialBytes: 1 << 16})
	// Free a single 200-word block into bucket 1 (129..256 words) only
	big := a.AllocBytes(200 * 8)
	a.Free(big, 200*8)
	// Request 100 words (bucket 0, 65..128) — bucket 0 is empty, upgrade to bucket 1 and find the 200-word block
	got := a.AllocBytes(100 * 8)
	if got != big {
		t.Fatalf("cross-bucket upgrade failed: want %d got %d", big, got)
	}
}

// TestLargeFreelist_PopLarge_EmptyReturnsZero verifies popLarge returns 0 when all buckets are empty.
func TestLargeFreelist_PopLarge_EmptyReturnsZero(t *testing.T) {
	a := New(Options{InitialBytes: 1 << 16})
	// No Free at all → all LARGE buckets empty
	if ref := a.popLarge(200); !ref.IsNull() {
		t.Errorf("popLarge with empty buckets returned non-null: %d", ref)
	}
}

// TestLargeFreelist_ExactHitO1 verifies that an exact same-size hit at the head of bucket c0 is O(1).
// It repeatedly Alloc+Free's the same size to verify LIFO head hits.
func TestLargeFreelist_ExactHitO1(t *testing.T) {
	a := New(Options{InitialBytes: 1 << 18})
	const words = uint32(512)
	r0 := a.AllocBytes(words * 8)
	for i := 0; i < 100; i++ {
		a.Free(r0, words*8)
		r1 := a.AllocBytes(words * 8)
		if r1 != r0 {
			t.Fatalf("iteration %d: exact-hit LIFO broken: want %d got %d", i, r0, r1)
		}
		r0 = r1
	}
}

// TestLargeFreelist_RemainderToCorrectBucket verifies the remainder is split into
// the right bucket (>64 words LARGE / ≤64 words small).
func TestLargeFreelist_RemainderToCorrectBucket(t *testing.T) {
	a := New(Options{InitialBytes: 1 << 16})
	// 200-word block — Free + Alloc 70 words → 130 words remaining > 64 → into LARGE bucket 1 (129..256)
	big := a.AllocBytes(200 * 8)
	a.Free(big, 200*8)
	taken := a.AllocBytes(70 * 8)
	if taken != big {
		t.Fatalf("alloc 70w from 200w block failed: want %d got %d", big, taken)
	}
	// The remaining 130 words should be hit by popLarge for an Alloc of 130 words
	rem := a.AllocBytes(130 * 8)
	if rem != big+GCRef(70*8) {
		t.Fatalf("remainder 130 not in correct bucket: want %d got %d", big+GCRef(70*8), rem)
	}
}

// TestLargeFreelist_RemainderToSmallBucket verifies a remainder ≤ 64 words goes into a small bucket.
func TestLargeFreelist_RemainderToSmallBucket(t *testing.T) {
	a := New(Options{InitialBytes: 1 << 16})
	// 130-word block — Free + Alloc 100 words → 30 words remaining ≤ 64 → floored into a small bucket
	big := a.AllocBytes(130 * 8)
	a.Free(big, 130*8)
	taken := a.AllocBytes(100 * 8)
	if taken != big {
		t.Fatalf("alloc 100w from 130w LARGE block failed: want %d got %d", big, taken)
	}
	// remaining 30 words into a small bucket: a 28-word (class-14 representative) request should hit
	rem := a.AllocBytes(28 * 8)
	if rem != big+GCRef(100*8) {
		t.Fatalf("remainder ≤64 not in small bucket: want %d got %d", big+GCRef(100*8), rem)
	}
}

// TestLargeFreelist_FragmentationAvoidance simulates the repeated-doublings workload
// from issue #10, verifying that scan depth stays bounded under multi-bucket layout
// (no O(N) single chain). Indirect verification: after 1000 rounds of building many
// doublings, Alloc is still O(1)-ish.
func TestLargeFreelist_FragmentationAvoidance(t *testing.T) {
	a := New(Options{InitialBytes: 1 << 20})
	// Simulate #10 usage: repeatedly allocate array segs of different sizes + Free
	sizes := []uint32{128, 256, 512, 1024, 2048, 4096}
	for round := 0; round < 100; round++ {
		refs := make([]GCRef, len(sizes))
		for i, s := range sizes {
			refs[i] = a.AllocBytes(s * 8)
		}
		for i := range sizes {
			a.Free(refs[i], sizes[i]*8)
		}
	}
	// After 100 rounds × 6 sizes = 600 free + alloc, one more Alloc of each size
	// should still hit the bucket head O(1)
	// (guaranteed by both LIFO + multi-bucket)
	for _, s := range sizes {
		ref := a.AllocBytes(s * 8)
		if ref.IsNull() {
			t.Fatalf("size %d: alloc returned null after 100-round fragmentation test", s)
		}
		a.Free(ref, s*8)
	}
}

// TestLargeFreelist_ZeroFillOnReuse verifies zeroFill clears old stale data when a LARGE block is reused.
func TestLargeFreelist_ZeroFillOnReuse(t *testing.T) {
	a := New(Options{InitialBytes: 1 << 16})
	big := a.AllocBytes(200 * 8)
	for i := uint32(0); i < 200; i++ {
		a.SetWordAt(big+GCRef(i*8), 0xDEADBEEFCAFEBABE)
	}
	a.Free(big, 200*8)
	r2 := a.AllocBytes(200 * 8)
	for i := uint32(0); i < 200; i++ {
		if w := a.WordAt(r2 + GCRef(i*8)); w != 0 {
			t.Fatalf("LARGE reuse not zero-filled at offset %d: got %#x", i*8, w)
		}
	}
}

// --- arena Compact (issue #11 direction 1) direct unit tests ---

// TestCompact_NoOpAtMinCap verifies Compact is a no-op when cap ≤ max(bump, 64 KiB).
func TestCompact_NoOpAtMinCap(t *testing.T) {
	a := New(Options{InitialBytes: 64 * 1024})
	cap0 := a.Cap()
	a.Compact()
	if a.Cap() != cap0 {
		t.Errorf("Compact at min cap changed Cap: before=%d after=%d", cap0, a.Cap())
	}
}

// TestCompact_ShrinkAfterGrowDoubling verifies Compact actually shrinks back to bump after grow doubling.
func TestCompact_ShrinkAfterGrowDoubling(t *testing.T) {
	a := New(Options{InitialBytes: 64 * 1024})
	// Allocate 200 KB → triggers grow doubling to 256 KB
	r := a.AllocBytes(200 * 1024)
	if r.IsNull() {
		t.Fatal("alloc 200KB failed")
	}
	capPeak := a.Cap()
	if capPeak < 256*1024 {
		t.Fatalf("grow doubling did not occur as expected: peak cap=%d", capPeak)
	}
	a.Compact()
	capAfter := a.Cap()
	if capAfter >= capPeak {
		t.Errorf("Compact did not shrink Cap: peak=%d after=%d", capPeak, capAfter)
	}
}

// TestCompact_Idempotent verifies a second Compact is a no-op.
func TestCompact_Idempotent(t *testing.T) {
	a := New(Options{InitialBytes: 64 * 1024})
	_ = a.AllocBytes(200 * 1024)
	a.Compact()
	capFirst := a.Cap()
	a.Compact()
	capSecond := a.Cap()
	if capSecond != capFirst {
		t.Errorf("Compact non-idempotent: first=%d second=%d", capFirst, capSecond)
	}
}

// TestCompact_GCRefValidAfterCompact verifies GCRefs already Alloc'd are still valid for read/write after Compact.
func TestCompact_GCRefValidAfterCompact(t *testing.T) {
	a := New(Options{InitialBytes: 64 * 1024})
	// Allocate several blocks, write known values, and after Compact reads should match
	type record struct {
		ref   GCRef
		words uint32
		vals  []uint64
	}
	var records []record
	for _, w := range []uint32{1, 8, 65, 200, 1024} {
		ref := a.AllocBytes(w * 8)
		vals := make([]uint64, w)
		for i := uint32(0); i < w; i++ {
			vals[i] = uint64(0xCAFE0000) | uint64(i)
			a.SetWordAt(ref+GCRef(i*8), vals[i])
		}
		records = append(records, record{ref, w, vals})
	}
	// Trigger grow doubling so Compact has room to shrink
	_ = a.AllocBytes(200 * 1024)
	a.Compact()
	// After Compact all GCRefs should read back their original values
	for _, r := range records {
		for i := uint32(0); i < r.words; i++ {
			got := a.WordAt(r.ref + GCRef(i*8))
			if got != r.vals[i] {
				t.Errorf("GCRef invalid after Compact: ref %d off %d: got %#x want %#x",
					r.ref, i*8, got, r.vals[i])
			}
		}
	}
}

// TestCompact_FreelistValidAfterCompact verifies freelist reuse is still correct after Compact.
func TestCompact_FreelistValidAfterCompact(t *testing.T) {
	a := New(Options{InitialBytes: 64 * 1024})
	// Trigger grow + some Frees into the freelist
	r1 := a.AllocBytes(200 * 8)
	r2 := a.AllocBytes(400 * 8)
	a.Free(r1, 200*8)
	a.Free(r2, 400*8)
	// Trigger grow doubling
	_ = a.AllocBytes(150 * 1024)
	a.Compact()
	// After Compact, Free + Alloc should still go through the freelist
	r3 := a.AllocBytes(200 * 8) // should hit the r1 remnant
	if r3 != r1 {
		t.Errorf("post-Compact freelist reuse broke: want %d got %d", r1, r3)
	}
}

// TestCompact_InPlaceBackingNoOp verifies Compact is a no-op under InPlaceBacking mode
// (P3 adopts wazero linear memory which cannot shrink; the default BackingFn leaves
// InPlaceBacking at its default false, so here we manually set Options.InPlaceBacking=true
// to test the P3 path).
func TestCompact_InPlaceBackingNoOp(t *testing.T) {
	a := New(Options{InitialBytes: 64 * 1024, InPlaceBacking: true})
	_ = a.AllocBytes(200 * 1024)
	capPeak := a.Cap()
	a.Compact()
	if a.Cap() != capPeak {
		t.Errorf("Compact under InPlaceBacking should be no-op: peak=%d after=%d", capPeak, a.Cap())
	}
}

// floorClass bounds: the input contract is 1..64; >64 must clamp rather than panic out of bounds.
func TestFloorClassBounds(t *testing.T) {
	if c := floorClass(0); c != -1 {
		t.Errorf("floorClass(0) = %d, want -1", c)
	}
	if c := floorClass(1); c != 0 || classWords(c) != 1 {
		t.Errorf("floorClass(1) = %d (rep %d), want class 0 rep 1", c, classWords(c))
	}
	if c := floorClass(64); classWords(c) > 64 {
		t.Errorf("floorClass(64) rep %d exceeds 64", classWords(c))
	}
	// >64 clamps to 64, no panic
	if c := floorClass(100); classWords(c) > 64 {
		t.Errorf("floorClass(100) rep %d exceeds 64 (clamp failed)", classWords(c))
	}
}
