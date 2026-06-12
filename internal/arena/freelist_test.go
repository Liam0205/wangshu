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
	// 200 字块对 70 字请求:剩 130 > 64 可切分
	part := a.AllocBytes(70 * 8)
	if part != big {
		t.Fatalf("LARGE split reuse failed: want %d got %d", big, part)
	}
	if a.FreeBytes() == 0 {
		t.Fatal("split remainder not returned to freelist")
	}
	// 100 字块对 70 字请求:剩 30 ≤ 64 不可独立成块 → 保守跳过,走 bump
	small := a.AllocBytes(100 * 8)
	a.Free(small, 100*8)
	skip := a.AllocBytes(70 * 8)
	if skip == small {
		t.Fatal("LARGE must not split when remainder <= 64 words (would strand a non-class fragment)")
	}
}
