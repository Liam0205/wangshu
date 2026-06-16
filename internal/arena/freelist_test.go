// 尺寸入口加固回归。
//
// uint32 回绕族:nbytes 接近 0xFFFFFFFF 时 roundUp8 / bump+need / words*8
// 都会回绕成小值,超大请求被静默"成功"切走 8 字节(错误别名,静默错果)。
// 加固后必须 fail-fast panic。
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

// freelist 行为:Free 后复用、复用块清零、LARGE 首次适配。
func TestFreelist_ReuseAndZeroFill(t *testing.T) {
	a := New(Options{InitialBytes: 4096})
	r1 := a.AllocBytes(24) // 3 字 → class 2
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
	big := a.AllocBytes(200 * 8) // 200 字 > 64 → LARGE
	a.Free(big, 200*8)
	got := a.AllocBytes(200 * 8) // 精确命中
	if got != big {
		t.Fatalf("LARGE exact-fit reuse failed: want %d got %d", big, got)
	}
	a.Free(got, 200*8)
	// 200 字块对 70 字请求:剩 130 > 64 独立成 LARGE 块
	part := a.AllocBytes(70 * 8)
	if part != big {
		t.Fatalf("LARGE split reuse failed: want %d got %d", big, part)
	}
	if a.FreeBytes() == 0 {
		t.Fatal("split remainder not returned to freelist")
	}
	// 100 字块对 70 字请求:剩 30 ≤ 64 → 向下取整入定长桶(不再滞留)
	small := a.AllocBytes(100 * 8)
	a.Free(small, 100*8)
	taken := a.AllocBytes(70 * 8)
	if taken != small {
		t.Fatalf("slightly-larger LARGE block must be taken (no stranding): want %d got %d", small, taken)
	}
	// 剩余 30 字进了桶:28 字(class 14 代表)请求应命中该余块
	rem := a.AllocBytes(28 * 8)
	if rem != small+GCRef(70*8) {
		t.Fatalf("remainder not bucketed: want %d got %d", small+GCRef(70*8), rem)
	}
}

// --- LARGE multi-bucket size-class (issue #10 root fix) 直接单测 ---

// TestLargeSizeClass_Boundaries 验证 largeSizeClass 在 power-of-2 边界 + 中间值的桶号映射。
// 桶 i 对应 words ∈ ((1<<(6+i)), (1<<(7+i))]:桶 0=65..128, 桶 1=129..256, ...
func TestLargeSizeClass_Boundaries(t *testing.T) {
	cases := []struct {
		words  uint32
		bucket int
	}{
		{65, 0},       // 桶 0 起点
		{100, 0},      // 桶 0 中段
		{128, 0},      // 桶 0 终点(精确 2^7)
		{129, 1},      // 桶 1 起点
		{200, 1},      // 桶 1 中段
		{256, 1},      // 桶 1 终点(精确 2^8)
		{257, 2},      // 桶 2 起点
		{512, 2},      // 桶 2 终点
		{513, 3},      // 桶 3 起点
		{1024, 3},     // 桶 3 终点
		{1 << 20, 13}, // 1M words = 1<<20,桶 13(2^20)
		{1 << 28, 21}, // 2GB words 上限(=MaxBytes/8 量级)
	}
	for _, c := range cases {
		if got := largeSizeClass(c.words); got != c.bucket {
			t.Errorf("largeSizeClass(%d) = %d, want %d", c.words, got, c.bucket)
		}
	}
}

// TestLargeSizeClass_Clamp 验证超大 words(超 numLargeClasses 范围)clamp 到末桶。
func TestLargeSizeClass_Clamp(t *testing.T) {
	if got := largeSizeClass(1 << 31); got != numLargeClasses-1 {
		t.Errorf("largeSizeClass clamp at boundary: got %d, want %d", got, numLargeClasses-1)
	}
}

// TestLargeFreelist_BucketSegregation 验证不同 size 块入不同桶。Free 多个 size 后
// 反复 Alloc 同 size 应直接命中对应桶,不跨桶扫描。
func TestLargeFreelist_BucketSegregation(t *testing.T) {
	a := New(Options{InitialBytes: 1 << 16})
	// 分配 5 个不同 size:128, 256, 512, 1024, 2048 字(各占独立桶)
	sizes := []uint32{128, 256, 512, 1024, 2048}
	refs := make([]GCRef, len(sizes))
	for i, s := range sizes {
		refs[i] = a.AllocBytes(s * 8)
	}
	// Free 全部
	for i, s := range sizes {
		a.Free(refs[i], s*8)
	}
	// 反复 Alloc 同 size 应命中各自桶的头部(LIFO)
	for i, s := range sizes {
		got := a.AllocBytes(s * 8)
		if got != refs[i] {
			t.Errorf("size %d: bucket-segregated alloc failed: want %d got %d", s, refs[i], got)
		}
	}
}

// TestLargeFreelist_CrossBucketUpgrade 验证桶 c0 空时升桶 c0+1 查找。
func TestLargeFreelist_CrossBucketUpgrade(t *testing.T) {
	a := New(Options{InitialBytes: 1 << 16})
	// 只在 桶 1(129..256 字)Free 一块 200 字
	big := a.AllocBytes(200 * 8)
	a.Free(big, 200*8)
	// 请求 100 字(桶 0,65..128)— 桶 0 空,升到 桶 1 找到 200 字
	got := a.AllocBytes(100 * 8)
	if got != big {
		t.Fatalf("cross-bucket upgrade failed: want %d got %d", big, got)
	}
}

// TestLargeFreelist_PopLarge_EmptyReturnsZero 验证全桶空时 popLarge 返 0。
func TestLargeFreelist_PopLarge_EmptyReturnsZero(t *testing.T) {
	a := New(Options{InitialBytes: 1 << 16})
	// 没有任何 Free → 所有 LARGE 桶空
	if ref := a.popLarge(200); !ref.IsNull() {
		t.Errorf("popLarge with empty buckets returned non-null: %d", ref)
	}
}

// TestLargeFreelist_ExactHitO1 验证桶 c0 头部精确同 size 命中 O(1)。
// 通过反复 Alloc+Free 同 size 验证 LIFO 头部命中。
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

// TestLargeFreelist_RemainderToCorrectBucket 验证剩余切到对应桶(>64 字 LARGE / ≤64 字 small)。
func TestLargeFreelist_RemainderToCorrectBucket(t *testing.T) {
	a := New(Options{InitialBytes: 1 << 16})
	// 200 字块 — Free + Alloc 70 字 → 剩 130 字 > 64 → 进 LARGE 桶 1(129..256)
	big := a.AllocBytes(200 * 8)
	a.Free(big, 200*8)
	taken := a.AllocBytes(70 * 8)
	if taken != big {
		t.Fatalf("alloc 70w from 200w block failed: want %d got %d", big, taken)
	}
	// 剩 130 字应该能被 Alloc 130 字 popLarge 命中
	rem := a.AllocBytes(130 * 8)
	if rem != big+GCRef(70*8) {
		t.Fatalf("remainder 130 not in correct bucket: want %d got %d", big+GCRef(70*8), rem)
	}
}

// TestLargeFreelist_RemainderToSmallBucket 验证剩余 ≤ 64 字进 small 桶。
func TestLargeFreelist_RemainderToSmallBucket(t *testing.T) {
	a := New(Options{InitialBytes: 1 << 16})
	// 130 字块 — Free + Alloc 100 字 → 剩 30 字 ≤ 64 → 向下取整到 small 桶
	big := a.AllocBytes(130 * 8)
	a.Free(big, 130*8)
	taken := a.AllocBytes(100 * 8)
	if taken != big {
		t.Fatalf("alloc 100w from 130w LARGE block failed: want %d got %d", big, taken)
	}
	// 剩 30 字进 small 桶:28 字(class 14 代表)请求应命中
	rem := a.AllocBytes(28 * 8)
	if rem != big+GCRef(100*8) {
		t.Fatalf("remainder ≤64 not in small bucket: want %d got %d", big+GCRef(100*8), rem)
	}
}

// TestLargeFreelist_FragmentationAvoidance 模拟 issue #10 反复 doublings 工作负载,
// 验证 multi-bucket 下扫描深度 bounded(不会出现 O(N) 单链)。
// 间接验证手法:反复 1000 次 build 多 doublings 后,Alloc 仍 O(1) ish。
func TestLargeFreelist_FragmentationAvoidance(t *testing.T) {
	a := New(Options{InitialBytes: 1 << 20})
	// 模拟 #10 用法:反复分配 array seg 不同尺寸 + Free
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
	// 累计 100 轮 × 6 sizes = 600 free + alloc 后,再一次 Alloc 各 size 应仍命中桶头部 O(1)
	// (LIFO + multi-bucket 双重保证)
	for _, s := range sizes {
		ref := a.AllocBytes(s * 8)
		if ref.IsNull() {
			t.Fatalf("size %d: alloc returned null after 100-round fragmentation test", s)
		}
		a.Free(ref, s*8)
	}
}

// TestLargeFreelist_ZeroFillOnReuse 验证 LARGE 块复用时 zeroFill 清旧脏数据。
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

// --- arena Compact (issue #11 方向 1) 直接单测 ---

// TestCompact_NoOpAtMinCap 验证 cap ≤ max(bump, 64 KiB) 时 Compact no-op。
func TestCompact_NoOpAtMinCap(t *testing.T) {
	a := New(Options{InitialBytes: 64 * 1024})
	cap0 := a.Cap()
	a.Compact()
	if a.Cap() != cap0 {
		t.Errorf("Compact at min cap changed Cap: before=%d after=%d", cap0, a.Cap())
	}
}

// TestCompact_ShrinkAfterGrowDoubling 验证 grow doubling 后 Compact 真缩回 bump。
func TestCompact_ShrinkAfterGrowDoubling(t *testing.T) {
	a := New(Options{InitialBytes: 64 * 1024})
	// 分配 200 KB → 触发 grow doubling 到 256 KB
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

// TestCompact_Idempotent 验证多次 Compact 第二次 no-op。
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

// TestCompact_GCRefValidAfterCompact 验证 Compact 后已 Alloc 的 GCRef 仍合法读写。
func TestCompact_GCRefValidAfterCompact(t *testing.T) {
	a := New(Options{InitialBytes: 64 * 1024})
	// 分配多个块,写已知值,Compact 后读应一致
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
	// 触发 grow doubling 让 Compact 有缩空间
	_ = a.AllocBytes(200 * 1024)
	a.Compact()
	// Compact 后所有 GCRef 应能读出原值
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

// TestCompact_FreelistValidAfterCompact 验证 Compact 后 freelist 复用仍正确。
func TestCompact_FreelistValidAfterCompact(t *testing.T) {
	a := New(Options{InitialBytes: 64 * 1024})
	// 触发 grow + 一些 Free 进 freelist
	r1 := a.AllocBytes(200 * 8)
	r2 := a.AllocBytes(400 * 8)
	a.Free(r1, 200*8)
	a.Free(r2, 400*8)
	// 触发 grow doubling
	_ = a.AllocBytes(150 * 1024)
	a.Compact()
	// Compact 后再 Free + Alloc 应仍可走 freelist
	r3 := a.AllocBytes(200 * 8) // 应能命中 r1 残留
	if r3 != r1 {
		t.Errorf("post-Compact freelist reuse broke: want %d got %d", r1, r3)
	}
}

// TestCompact_InPlaceBackingNoOp 验证 InPlaceBacking 模式下 Compact no-op
// (P3 收养 wazero linear memory 不可缩,但默认 BackingFn 没设 InPlaceBacking 默认 false;
// 这里手动 Options.InPlaceBacking=true 测 P3 路径)。
func TestCompact_InPlaceBackingNoOp(t *testing.T) {
	a := New(Options{InitialBytes: 64 * 1024, InPlaceBacking: true})
	_ = a.AllocBytes(200 * 1024)
	capPeak := a.Cap()
	a.Compact()
	if a.Cap() != capPeak {
		t.Errorf("Compact under InPlaceBacking should be no-op: peak=%d after=%d", capPeak, a.Cap())
	}
}

// floorClass 边界:入参契约 1..64,>64 须 clamp 而非越界 panic。
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
	// >64 clamp 到 64,不 panic
	if c := floorClass(100); classWords(c) > 64 {
		t.Errorf("floorClass(100) rep %d exceeds 64 (clamp failed)", classWords(c))
	}
}
