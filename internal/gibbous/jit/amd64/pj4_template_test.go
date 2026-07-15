//go:build wangshu_p4 && linux && amd64

package amd64

import (
	"testing"
)

// pj4_template_test.go —— PJ4 IC ArrayHit template byte-level length
// consistency + emit-does-not-panic tests. A real mmap+RX round-trip needs a
// real table constructed (arena + table head + array segment), which pulls in
// cross-internal-package dependencies; that is left to the crescent e2e
// higher-level tests.

func TestPJ4_EmitGetTableArrayHit_Length(t *testing.T) {
	var buf []byte
	buf = EmitGetTableArrayHit(buf,
		1,          // aReg = R(1)
		0,          // bReg = R(0) (table)
		7,          // stableShape (gen)
		3,          // stableIndex
		16,         // arenaBaseOff(jitContext field offset example)
		0xCAFEBABE, // deoptCode
	)

	// Do not require an exact length, only assert non-empty + no panic
	if len(buf) == 0 {
		t.Fatal("EmitGetTableArrayHit returned empty buf")
	}
	t.Logf("EmitGetTableArrayHit emitted %d bytes", len(buf))

	// Verify the template ends with ret(0xC3)
	if buf[len(buf)-1] != 0xC3 {
		t.Errorf("template should end with ret(0xC3), got 0x%02x", buf[len(buf)-1])
	}
}

func TestPJ4_EmitGetTableArrayHit_DeoptBlockPresent(t *testing.T) {
	var buf []byte
	const deopt uint64 = 0xDEADBEEF12345678
	buf = EmitGetTableArrayHit(buf, 0, 0, 0, 0, 0, deopt)

	// The deopt block is mov rax, deoptCode + ret (48 B8 deoptCode + C3 = 11
	// bytes) at the end of the template. Check the last 11 bytes.
	if len(buf) < 11 {
		t.Fatal("buf too short")
	}
	tail := buf[len(buf)-11:]
	if tail[0] != 0x48 || tail[1] != 0xB8 {
		t.Errorf("deopt block prefix = %x, want 48 B8(mov rax, imm64)", tail[0:2])
	}
	// imm64 little-endian = deoptCode bytes
	for i := 0; i < 8; i++ {
		if tail[2+i] != byte(deopt>>(8*i)) {
			t.Errorf("deopt imm64 byte %d = 0x%02x, want 0x%02x",
				i, tail[2+i], byte(deopt>>(8*i)))
		}
	}
	// Trailing ret
	if tail[10] != 0xC3 {
		t.Errorf("deopt block tail = 0x%02x, want 0xC3(ret)", tail[10])
	}
}

// TestPJ4_EmitGetTableArrayHit_StrictIsTableGuard verifies the strict IsTable
// guard byte sequence in the front of the template (bytes 7-22):
//
//	[ 0-6 ] mov rax, [rbx + bReg*8]    (7 bytes)
//	[ 7-10] shr rax, 48                (4 bytes, 48 C1 E8 30)
//	[11-15] cmp eax, 0xFFFC            (5 bytes, 3D FC FF 00 00)
//	[16-21] jne deopt (rel32 placeholder)(6 bytes, 0F 85 ...)
//
// The strict guard replaces the original simplified version mov rcx,0xFFFC<<48
// + cmp rax,rcx + jb deopt (10+3+6=19 bytes), saving 4 bytes and giving a real
// strict IsTable check.
func TestPJ4_EmitGetTableArrayHit_StrictIsTableGuard(t *testing.T) {
	var buf []byte
	buf = EmitGetTableArrayHit(buf, 1, 0, 7, 3, 16, 0xCAFEBABE)

	// Skip the first 7 bytes (mov rax, [rbx + 0*8]) = 48 8B 03 00 00 00 00.
	// EmitMovqRaxFromMemReg may use [rbx+disp8] rather than disp32, so when
	// asserting, first find where shr rax, 48 appears (48 C1 E8 30).
	const shrSig = uint32(0x48C1E830) // bytes: 48 C1 E8 30 -> as uint32 LE
	// grep the shr byte sequence directly within the first 20 bytes of buf
	found := false
	for i := 0; i <= 20-4; i++ {
		if buf[i] == 0x48 && buf[i+1] == 0xC1 && buf[i+2] == 0xE8 && buf[i+3] == 48 {
			found = true
			// Immediately after should be cmp eax, 0xFFFC = 3D FC FF 00 00
			if i+8 >= len(buf) {
				t.Fatalf("cmp 段越界")
			}
			if buf[i+4] != 0x3D ||
				buf[i+5] != 0xFC || buf[i+6] != 0xFF ||
				buf[i+7] != 0x00 || buf[i+8] != 0x00 {
				t.Errorf("cmp eax, 0xFFFC bytes wrong at %d: got %x, want 3D FC FF 00 00",
					i+4, buf[i+4:i+9])
			}
			// Immediately after should be jne rel32 = 0F 85 ... (6 bytes)
			if i+10 >= len(buf) {
				t.Fatalf("jne 段越界")
			}
			if buf[i+9] != 0x0F || buf[i+10] != 0x85 {
				t.Errorf("jne rel32 prefix wrong at %d: got %x %x, want 0F 85",
					i+9, buf[i+9], buf[i+10])
			}
			t.Logf("严密 IsTable guard 字节序列在 offset %d 找到:shr@%d / cmp@%d / jne@%d",
				i, i, i+4, i+9)
			break
		}
		_ = shrSig
	}
	if !found {
		t.Errorf("严密 IsTable guard 字节序列(shr rax 48 / cmp eax 0xFFFC / jne)未在模板前 20 字节出现 → 模板未升级到严密版")
	}
}

// TestPJ4_EmitGetTableArrayHit_NoSimplifiedGuard is a negative assertion: the
// template bytes should not contain the original simplified IsTable guard
// sequence (mov rcx, 0xFFFC<<48 = 48 B9 00 00 00 00 00 00 FC FF + cmp rax, rcx
// = 48 39 C8 + jb rel32 = 0F 82 ...).
//
// After the strict version replaces the simplified one, the template bytes
// should no longer contain the little-endian bytes 00 00 00 00 00 00 FC FF of
// 0xFFFC<<48 = 0xFFFC_0000_0000_0000. Note the nil mask (0xFFFE_...) and
// stableShape may still appear at the tail of the template (nil check + cmp eax
// stableShape), so we only look for the presence of the characteristic byte
// sequence mov rcx, imm64 + cmp rax, rcx + jb combination.
//
// Simplified-version signature: 48 B9 [..imm64..] 48 39 C8 0F 82
func TestPJ4_EmitGetTableArrayHit_NoSimplifiedGuard(t *testing.T) {
	var buf []byte
	buf = EmitGetTableArrayHit(buf, 1, 0, 7, 3, 16, 0xCAFEBABE)

	// Simplified-version signature: 48 B9 [imm64 8 bytes] 48 39 C8 0F 82 (15
	// bytes total). After the strict version replaces it, this combination
	// should not appear within the first 25 bytes of buf (a later mov rcx is
	// the nil mask 0xFFFE..., but it is followed by cmp rax rcx + je rather than
	// jb, so the signature will not false-match).
	for i := 0; i+14 < 25; i++ {
		if buf[i] == 0x48 && buf[i+1] == 0xB9 && // mov rcx, imm64
			buf[i+10] == 0x48 && buf[i+11] == 0x39 && buf[i+12] == 0xC8 && // cmp rax, rcx
			buf[i+13] == 0x0F && buf[i+14] == 0x82 { // jb rel32
			t.Errorf("发现旧简化版 IsTable guard 字节序列在 offset %d "+
				"(mov rcx imm64 + cmp rax rcx + jb)→ 模板未升级到严密版",
				i)
			break
		}
	}
}

// TestPJ4_EmitGetTableNodeHit_Length verifies the NodeHit template byte-level
// length consistency. Expected length ~159 bytes (ArrayHit 132 bytes + key
// comparison 27 bytes: NodeKey load 8 + mov rdx imm64 10 + cmp rax rdx 3 + jne
// rel32 6).
func TestPJ4_EmitGetTableNodeHit_Length(t *testing.T) {
	var buf []byte
	buf = EmitGetTableNodeHit(buf,
		1,                     // aReg = R(1)
		0,                     // bReg = R(0)(table)
		7,                     // stableShape (gen)
		2,                     // stableIndex
		0xFFFB_DEAD_BEEF_CAFE, // stableKey (simulated string NaN-box)
		16,                    // arenaBaseOff
		0xFFFCDEAD_DEADBEAD,   // deoptCode
	)
	if len(buf) == 0 {
		t.Fatal("EmitGetTableNodeHit returned empty buf")
	}
	t.Logf("EmitGetTableNodeHit emitted %d bytes", len(buf))
	if buf[len(buf)-1] != 0xC3 {
		t.Errorf("NodeHit template should end with ret(0xC3), got 0x%02x",
			buf[len(buf)-1])
	}
}

// TestPJ4_EmitGetTableNodeHit_StrictIsTableGuard verifies the NodeHit template
// reuses the same strict IsTable guard byte sequence (mirroring ArrayHit
// StrictIsTableGuard).
func TestPJ4_EmitGetTableNodeHit_StrictIsTableGuard(t *testing.T) {
	var buf []byte
	buf = EmitGetTableNodeHit(buf, 1, 0, 7, 2, 0xFFFB_0000_0000_0001,
		16, 0xCAFEBABE)

	// Look for shr rax, 48 (48 C1 E8 30) within the first 20 bytes
	found := false
	for i := 0; i <= 20-4; i++ {
		if buf[i] == 0x48 && buf[i+1] == 0xC1 && buf[i+2] == 0xE8 && buf[i+3] == 48 {
			found = true
			// Immediately after: cmp eax, 0xFFFC + jne rel32
			if i+10 >= len(buf) {
				t.Fatalf("strict guard 段越界")
			}
			if buf[i+4] != 0x3D ||
				buf[i+5] != 0xFC || buf[i+6] != 0xFF ||
				buf[i+7] != 0x00 || buf[i+8] != 0x00 {
				t.Errorf("NodeHit cmp eax 0xFFFC bytes wrong at %d", i+4)
			}
			if buf[i+9] != 0x0F || buf[i+10] != 0x85 {
				t.Errorf("NodeHit jne rel32 prefix wrong at %d", i+9)
			}
			break
		}
	}
	if !found {
		t.Errorf("NodeHit 严密 IsTable guard 字节序列未在模板前 20 字节出现")
	}
}

// TestPJ4_EmitGetTableNodeHit_KeyComparison verifies the NodeHit template
// contains the key comparison segment (mov rdx, stableKey + cmp rax, rdx +
// jne deopt, a 19-byte sequence).
//
// stableKey uses 0xFFFB_1234_5678_9ABC to simulate a string NaN-box; verify
// the imm64 byte sequence BC 9A 78 56 34 12 FB FF (little-endian) appears
// after mov rdx.
func TestPJ4_EmitGetTableNodeHit_KeyComparison(t *testing.T) {
	const stableKey uint64 = 0xFFFB_1234_5678_9ABC
	var buf []byte
	buf = EmitGetTableNodeHit(buf, 1, 0, 7, 2, stableKey, 16, 0xCAFEBABE)

	// find mov rdx imm64 = 48 BA + imm64 LE bytes
	wantSig := []byte{0x48, 0xBA,
		0xBC, 0x9A, 0x78, 0x56, 0x34, 0x12, 0xFB, 0xFF}
	found := false
	for i := 0; i+9 < len(buf); i++ {
		match := true
		for j, b := range wantSig {
			if buf[i+j] != b {
				match = false
				break
			}
		}
		if match {
			found = true
			// followed by cmp rax, rdx = 48 39 D0 (3 bytes) + jne rel32 = 0F 85 ... (6 bytes)
			if i+12 >= len(buf) {
				t.Fatalf("key 比对段越界")
			}
			if buf[i+10] != 0x48 || buf[i+11] != 0x39 || buf[i+12] != 0xD0 {
				t.Errorf("cmp rax, rdx 字节错位 at %d: got %x %x %x",
					i+10, buf[i+10], buf[i+11], buf[i+12])
			}
			if buf[i+13] != 0x0F || buf[i+14] != 0x85 {
				t.Errorf("jne rel32 prefix 字节错位 at %d", i+13)
			}
			t.Logf("NodeHit key 比对段在 offset %d 找到", i)
			break
		}
	}
	if !found {
		t.Errorf("NodeHit 模板未含 stableKey=0x%x 烧入字节(mov rdx imm64 未发射)",
			stableKey)
	}
}

// TestPJ4_EmitSetTableArrayHit_Length verifies the PJ4 SETTABLE ArrayHit
// template byte-level length self-consistency. Expected ~122 bytes (getter
// ArrayHit is 132 bytes but the nil check becomes load R(C)/reverse store +
// no NodeKey comparison, dropping the nil mask load + cmp + je).
func TestPJ4_EmitSetTableArrayHit_Length(t *testing.T) {
	var buf []byte
	buf = EmitSetTableArrayHit(buf,
		0,          // aReg = R(0) (table)
		1,          // cReg = R(1) (value)
		7,          // stableShape (gen)
		3,          // stableIndex
		16,         // arenaBaseOff
		0xCAFEBABE, // deoptCode
	)
	if len(buf) == 0 {
		t.Fatal("EmitSetTableArrayHit returned empty buf")
	}
	t.Logf("EmitSetTableArrayHit emitted %d bytes", len(buf))
	if buf[len(buf)-1] != 0xC3 {
		t.Errorf("SetTable template should end with ret(0xC3), got 0x%02x",
			buf[len(buf)-1])
	}
}

// TestPJ4_EmitSetTableArrayHit_StrictIsTableGuard verifies the SETTABLE
// template reuses the same strict IsTable guard (shr@7 / cmp@11 / jne@16).
func TestPJ4_EmitSetTableArrayHit_StrictIsTableGuard(t *testing.T) {
	var buf []byte
	buf = EmitSetTableArrayHit(buf, 0, 1, 7, 3, 16, 0xCAFEBABE)

	found := false
	for i := 0; i <= 20-4; i++ {
		if buf[i] == 0x48 && buf[i+1] == 0xC1 && buf[i+2] == 0xE8 && buf[i+3] == 48 {
			found = true
			if i+10 >= len(buf) {
				t.Fatalf("strict guard 段越界")
			}
			if buf[i+4] != 0x3D ||
				buf[i+5] != 0xFC || buf[i+6] != 0xFF ||
				buf[i+7] != 0x00 || buf[i+8] != 0x00 {
				t.Errorf("SetTable cmp eax 0xFFFC bytes wrong at %d", i+4)
			}
			if buf[i+9] != 0x0F || buf[i+10] != 0x85 {
				t.Errorf("SetTable jne rel32 prefix wrong at %d", i+9)
			}
			break
		}
	}
	if !found {
		t.Errorf("SetTable 严密 IsTable guard 字节序列未在模板前 20 字节出现")
	}
}

// TestPJ4_EmitSetTableArrayHit_ReverseStore verifies the SETTABLE template
// contains the reverse store byte sequence (load rdx from [rbx+cReg*8] =
// 48 8B 93 disp32 + mov [r14+rcx+...] from rdx = 49 89 94 0E disp32).
func TestPJ4_EmitSetTableArrayHit_ReverseStore(t *testing.T) {
	var buf []byte
	buf = EmitSetTableArrayHit(buf, 0, 1, 7, 3, 16, 0xCAFEBABE) // cReg=1 → disp=8

	// find mov rdx, [rbx + 8] = 48 8B 93 08 00 00 00 (7 bytes)
	wantLoadRdx := []byte{0x48, 0x8B, 0x93, 0x08, 0x00, 0x00, 0x00}
	found := -1
	for i := 0; i+6 < len(buf); i++ {
		match := true
		for j, b := range wantLoadRdx {
			if buf[i+j] != b {
				match = false
				break
			}
		}
		if match {
			found = i
			break
		}
	}
	if found < 0 {
		t.Fatal("load rdx from [rbx+cReg*8] 字节序列未在模板出现")
	}
	t.Logf("load rdx 在 offset %d 找到", found)

	// what follows: reverse store mov [r14+rcx+stableIndex*8], rdx
	// stableIndex=3, *8 = 24 → 49 89 94 0E 18 00 00 00 (8 bytes)
	wantStoreRdx := []byte{0x49, 0x89, 0x94, 0x0E, 0x18, 0x00, 0x00, 0x00}
	if found+7+7 >= len(buf) {
		t.Fatalf("反向 store 段越界")
	}
	for j, b := range wantStoreRdx {
		if buf[found+7+j] != b {
			t.Errorf("反向 store 字节[%d] = 0x%02x, want 0x%02x",
				j, buf[found+7+j], b)
		}
	}
}

// TestPJ4_EmitSelfArrayHit_Length verifies the PJ4 SELF ArrayHit template
// byte-level length self-consistency. Expected ~141 bytes (getter ArrayHit
// 132 + R(A+1) copy segment 7 bytes; the measured value depends on the
// EmitMovqMemRegFromRax disp encoding).
func TestPJ4_EmitSelfArrayHit_Length(t *testing.T) {
	var buf []byte
	buf = EmitSelfArrayHit(buf,
		2,          // aReg = R(2) (method result) → R(A+1)=R(3)
		0,          // bReg = R(0) (obj)
		7,          // stableShape
		1,          // stableIndex
		16,         // arenaBaseOff
		0xCAFEBABE, // deoptCode
	)
	if len(buf) == 0 {
		t.Fatal("EmitSelfArrayHit returned empty buf")
	}
	t.Logf("EmitSelfArrayHit emitted %d bytes", len(buf))
	if buf[len(buf)-1] != 0xC3 {
		t.Errorf("SELF template should end with ret(0xC3), got 0x%02x",
			buf[len(buf)-1])
	}
}

// TestPJ4_EmitSelfArrayHit_SelfStore verifies the SELF template contains the
// "R(A+1) := R(B)" copy segment (load rax from [rbx+bReg*8] + store
// [rbx+(aReg+1)*8] from rax, in the front of the template).
func TestPJ4_EmitSelfArrayHit_SelfStore(t *testing.T) {
	var buf []byte
	// aReg=2 → R(A+1) = R(3) slot offset 24
	// bReg=0 → R(B) slot offset 0
	buf = EmitSelfArrayHit(buf, 2, 0, 7, 1, 16, 0xCAFEBABE)

	// The front of the template should have:
	// [0-6] load rax from [rbx + 0]  = 48 8B 03 00 00 00 00 (7 bytes)
	// [7-13] store [rbx + 24], rax  = 48 89 83 18 00 00 00 (7 bytes)
	// followed by shr rax, 48 = 48 C1 E8 30 (4 bytes)
	if len(buf) < 18 {
		t.Fatal("模板太短")
	}
	// second segment store [rbx+24], rax bytes
	// EmitMovqMemRegFromRax encoding: 48 89 83 (rm=011=rbx) disp32 (or disp8 short)
	// with disp32: ModRM=83=mod10 reg=000 rm=011; with disp8: ModRM=43=mod01 reg=000 rm=011
	// measure to see which
	storeStart := 7
	if buf[storeStart] != 0x48 || buf[storeStart+1] != 0x89 {
		t.Errorf("store R(A+1) 前缀错位 at %d: got %x %x, want 48 89",
			storeStart, buf[storeStart], buf[storeStart+1])
	}
	t.Logf("SELF 模板前 18 字节 = %x", buf[:18])
}

// TestPJ4_EmitSetTableNodeHit_Length verifies the SETTABLE NodeHit template
// byte-level length self-consistency. Expected 140 bytes (GetTable NodeHit
// 159 - getter segment 34 + setter segment 15).
//
// **Exact length assertion** (per external review 🟢 feedback): locks down the
// layout contract, preventing a future accidental change to the primitive that
// drifts the length in a way the length test can catch.
func TestPJ4_EmitSetTableNodeHit_Length(t *testing.T) {
	const stableKey uint64 = 0xFFFB_1234_5678_9ABC
	var buf []byte
	buf = EmitSetTableNodeHit(buf,
		0, // aReg = R(0) (table)
		1, // cReg = R(1) (value)
		7, // stableShape
		2, // stableIndex
		stableKey,
		16,         // arenaBaseOff
		0xCAFEBABE, // deoptCode
	)
	const wantLen = 140
	if len(buf) != wantLen {
		t.Errorf("EmitSetTableNodeHit emitted %d bytes, want %d(精确长度契约)",
			len(buf), wantLen)
	}
	t.Logf("EmitSetTableNodeHit emitted %d bytes(=%d ✓)", len(buf), wantLen)
	if buf[len(buf)-1] != 0xC3 {
		t.Errorf("SetTable NodeHit template should end with ret(0xC3), got 0x%02x",
			buf[len(buf)-1])
	}
}

// TestPJ4_EmitSetTableNodeHit_StrictIsTableGuard verifies the SETTABLE NodeHit
// template reuses the same strict IsTable guard.
func TestPJ4_EmitSetTableNodeHit_StrictIsTableGuard(t *testing.T) {
	var buf []byte
	buf = EmitSetTableNodeHit(buf, 0, 1, 7, 2, 0xFFFB_0000_0000_0001,
		16, 0xCAFEBABE)

	found := false
	for i := 0; i <= 20-4; i++ {
		if buf[i] == 0x48 && buf[i+1] == 0xC1 && buf[i+2] == 0xE8 && buf[i+3] == 48 {
			found = true
			if i+10 >= len(buf) {
				t.Fatalf("strict guard 段越界")
			}
			if buf[i+4] != 0x3D ||
				buf[i+5] != 0xFC || buf[i+6] != 0xFF ||
				buf[i+7] != 0x00 || buf[i+8] != 0x00 {
				t.Errorf("SetTable NodeHit cmp eax 0xFFFC bytes wrong at %d", i+4)
			}
			if buf[i+9] != 0x0F || buf[i+10] != 0x85 {
				t.Errorf("SetTable NodeHit jne rel32 prefix wrong at %d", i+9)
			}
			break
		}
	}
	if !found {
		t.Errorf("SetTable NodeHit 严密 IsTable guard 字节序列未在模板前 20 字节出现")
	}
}

// TestPJ4_EmitSetTableNodeHit_KeyCompareAndReverseStore verifies the NodeHit
// setter contains the key comparison segment + reverse store NodeVal segment.
func TestPJ4_EmitSetTableNodeHit_KeyCompareAndReverseStore(t *testing.T) {
	const stableKey uint64 = 0xFFFB_DEAD_BEEF_CAFE
	var buf []byte
	// aReg=0 → R(A)=[rbx+0]
	// cReg=1 → R(C)=[rbx+8] (value)
	// stableIndex=2 → NodeKey @ rcx+48 / NodeVal @ rcx+56
	buf = EmitSetTableNodeHit(buf, 0, 1, 7, 2, stableKey, 16, 0xCAFEBABE)

	// find mov rdx, stableKey = 48 BA + imm64 LE
	wantMovRdx := []byte{0x48, 0xBA,
		0xFE, 0xCA, 0xEF, 0xBE, 0xAD, 0xDE, 0xFB, 0xFF}
	movRdxOff := -1
	for i := 0; i+9 < len(buf); i++ {
		match := true
		for j, b := range wantMovRdx {
			if buf[i+j] != b {
				match = false
				break
			}
		}
		if match {
			movRdxOff = i
			break
		}
	}
	if movRdxOff < 0 {
		t.Fatal("mov rdx, stableKey 字节序列未在模板出现")
	}
	t.Logf("mov rdx stableKey 在 offset %d 找到", movRdxOff)

	// what follows: cmp rax, rdx (3 bytes) + jne rel32 (6 bytes) = 9-byte key comparison segment
	if movRdxOff+12 >= len(buf) {
		t.Fatal("key 比对段越界")
	}
	if buf[movRdxOff+10] != 0x48 || buf[movRdxOff+11] != 0x39 || buf[movRdxOff+12] != 0xD0 {
		t.Errorf("cmp rax, rdx 字节错位")
	}
	if buf[movRdxOff+13] != 0x0F || buf[movRdxOff+14] != 0x85 {
		t.Errorf("jne rel32 prefix 字节错位")
	}

	// right after the key comparison (9 bytes): load R(C) → rdx (7 bytes) + reverse store
	// (8 bytes)
	// load rdx segment: 48 8B 93 disp32 (cReg=1 → disp=8)
	loadRdxOff := movRdxOff + 9 + 9 - 1 // starts after movRdx(10) + cmp(3) + jne(6) = 19 bytes
	// actual: movRdx starts at movRdxOff, takes 10 bytes; cmp 3 bytes, jne 6 bytes
	// the load R(C) → rdx segment is 19 bytes later
	loadRdxStart := movRdxOff + 19
	if loadRdxStart+6 >= len(buf) {
		t.Fatal("load R(C) 段越界")
	}
	wantLoadRdx := []byte{0x48, 0x8B, 0x93, 0x08, 0x00, 0x00, 0x00}
	for j, b := range wantLoadRdx {
		if buf[loadRdxStart+j] != b {
			t.Errorf("load R(C)→rdx 字节[%d] = 0x%02x, want 0x%02x at offset %d",
				j, buf[loadRdxStart+j], b, loadRdxStart+j)
		}
	}

	// right after load rdx (7 bytes): reverse store mov [r14+rcx+stableIndex*24+8], rdx
	// stableIndex=2, 24*2+8=56 → 49 89 94 0E 38 00 00 00 (8 bytes)
	storeStart := loadRdxStart + 7
	wantStore := []byte{0x49, 0x89, 0x94, 0x0E, 0x38, 0x00, 0x00, 0x00}
	if storeStart+7 >= len(buf) {
		t.Fatal("反向 store 段越界")
	}
	for j, b := range wantStore {
		if buf[storeStart+j] != b {
			t.Errorf("反向 store 字节[%d] = 0x%02x, want 0x%02x at offset %d",
				j, buf[storeStart+j], b, storeStart+j)
		}
	}
	_ = loadRdxOff // kept for future extension, not verified currently
}

// TestPJ4_EmitSelfNodeHit_Length verifies the SELF NodeHit template byte-level
// length self-consistency. Expected 166 bytes (SELF ArrayHit 139 + key
// comparison segment 27).
//
// **Exact length assertion** (per external review 🟢 feedback): locks down the
// layout contract, preventing a future accidental change to the primitive that
// drifts the length in a way the length test can catch.
func TestPJ4_EmitSelfNodeHit_Length(t *testing.T) {
	const stableKey uint64 = 0xFFFB_1234_5678_9ABC
	var buf []byte
	buf = EmitSelfNodeHit(buf,
		2, // aReg = R(2) (method result) → R(A+1)=R(3)
		0, // bReg = R(0) (obj)
		7, // stableShape
		1, // stableIndex
		stableKey,
		16,         // arenaBaseOff
		0xCAFEBABE, // deoptCode
	)
	const wantLen = 166
	if len(buf) != wantLen {
		t.Errorf("EmitSelfNodeHit emitted %d bytes, want %d(精确长度契约)",
			len(buf), wantLen)
	}
	t.Logf("EmitSelfNodeHit emitted %d bytes(=%d ✓)", len(buf), wantLen)
	if buf[len(buf)-1] != 0xC3 {
		t.Errorf("SELF NodeHit template should end with ret(0xC3), got 0x%02x",
			buf[len(buf)-1])
	}
}

// TestPJ4_EmitSelfNodeHit_SelfStoreAndKeyCompare verifies the SELF NodeHit
// template contains:
//   - front R(A+1) := R(B) copy segment (mirroring SELF ArrayHit)
//   - trailing NodeKey comparison segment (mov rdx stableKey + cmp rax rdx + jne)
func TestPJ4_EmitSelfNodeHit_SelfStoreAndKeyCompare(t *testing.T) {
	const stableKey uint64 = 0xFFFB_AAAA_BBBB_CCCC
	var buf []byte
	// aReg=2 → R(A+1)=R(3) slot offset 24
	// bReg=0 → R(B) slot offset 0
	buf = EmitSelfNodeHit(buf, 2, 0, 7, 1, stableKey, 16, 0xCAFEBABE)

	// front: load R(B) + store R(A+1) (14 bytes)
	if buf[0] != 0x48 || buf[1] != 0x8B {
		t.Errorf("SELF NodeHit 前部 load R(B) 字节错位")
	}
	if buf[7] != 0x48 || buf[8] != 0x89 {
		t.Errorf("SELF NodeHit 前部 store R(A+1) 字节错位")
	}

	// find mov rdx, stableKey (middle key comparison segment)
	wantMovRdx := []byte{0x48, 0xBA,
		0xCC, 0xCC, 0xBB, 0xBB, 0xAA, 0xAA, 0xFB, 0xFF}
	found := false
	for i := 0; i+9 < len(buf); i++ {
		match := true
		for j, b := range wantMovRdx {
			if buf[i+j] != b {
				match = false
				break
			}
		}
		if match {
			found = true
			// what follows: cmp rax, rdx + jne rel32
			if buf[i+10] != 0x48 || buf[i+11] != 0x39 || buf[i+12] != 0xD0 {
				t.Errorf("cmp rax, rdx 字节错位 at %d", i+10)
			}
			if buf[i+13] != 0x0F || buf[i+14] != 0x85 {
				t.Errorf("jne rel32 prefix 字节错位 at %d", i+13)
			}
			t.Logf("SELF NodeHit key 比对段在 offset %d 找到", i)
			break
		}
	}
	if !found {
		t.Errorf("SELF NodeHit 模板未含 stableKey=0x%x 烧入字节", stableKey)
	}
}

// TestPJ5_EmitSpecArgLoadK_Length verifies the PJ5 SELF spec args load-K
// byte-level template length (10-byte mov rax imm64 + 7-byte mov [rbx+dst*8] rax = 17 bytes).
func TestPJ5_EmitSpecArgLoadK_Length(t *testing.T) {
	var buf []byte
	buf = EmitSpecArgLoadK(buf, 5, 0xDEADBEEF12345678)
	if len(buf) != 17 {
		t.Errorf("EmitSpecArgLoadK 长度 = %d, want 17", len(buf))
	}
}

// TestPJ5_EmitSpecArgLoadReg_Length verifies the PJ5 SELF spec args load-reg
// byte-level template length (7-byte mov rax [rbx+src*8] + 7-byte mov [rbx+dst*8] rax = 14 bytes).
func TestPJ5_EmitSpecArgLoadReg_Length(t *testing.T) {
	var buf []byte
	buf = EmitSpecArgLoadReg(buf, 5, 3)
	if len(buf) != 14 {
		t.Errorf("EmitSpecArgLoadReg 长度 = %d, want 14", len(buf))
	}
}

// TestPJ5_EmitFrameInlineCIDepthInc_Length verifies the ciDepth++ byte-level
// inline template length (7-byte mov rax [r15+disp32] + 3-byte inc qword ptr [rax] = 10 bytes).
// Per §9.20 Option B Spike 1 starting building block.
func TestPJ5_EmitFrameInlineCIDepthInc_Length(t *testing.T) {
	var buf []byte
	buf = EmitFrameInlineCIDepthInc(buf, 0x40)
	if len(buf) != EncodedFrameInlineCIDepthIncDecLen {
		t.Errorf("EmitFrameInlineCIDepthInc 长度 = %d, want %d", len(buf), EncodedFrameInlineCIDepthIncDecLen)
	}
}

// TestPJ5_EmitFrameInlineCIDepthInc_Encoding verifies the inc byte-level encoding.
//
// Expected: [4C 8B 87 40 00 00 00 | 48 FF 00] = mov rax,[r15+0x40] + inc [rax]
func TestPJ5_EmitFrameInlineCIDepthInc_Encoding(t *testing.T) {
	var buf []byte
	buf = EmitFrameInlineCIDepthInc(buf, 0x40)

	want := []byte{
		0x49, 0x8B, 0x87, 0x40, 0x00, 0x00, 0x00, // mov rax, [r15+0x40]
		0x48, 0xFF, 0x00, // inc qword ptr [rax]
	}
	if len(buf) != len(want) {
		t.Fatalf("长度不一致:got %d want %d", len(buf), len(want))
	}
	for i, b := range want {
		if buf[i] != b {
			t.Errorf("buf[%d] = 0x%02X, want 0x%02X", i, buf[i], b)
		}
	}
}

// TestPJ5_EmitFrameInlineCIDepthDec_Length verifies the ciDepth-- byte-level
// inline template length, same as Inc (10 bytes). Per §9.20 popCallInfo reverse.
func TestPJ5_EmitFrameInlineCIDepthDec_Length(t *testing.T) {
	var buf []byte
	buf = EmitFrameInlineCIDepthDec(buf, 0x40)
	if len(buf) != EncodedFrameInlineCIDepthIncDecLen {
		t.Errorf("EmitFrameInlineCIDepthDec 长度 = %d, want %d", len(buf), EncodedFrameInlineCIDepthIncDecLen)
	}
}

// TestPJ5_EmitFrameInlineCIDepthDec_Encoding verifies the dec byte-level encoding (trailing inc becomes dec).
func TestPJ5_EmitFrameInlineCIDepthDec_Encoding(t *testing.T) {
	var buf []byte
	buf = EmitFrameInlineCIDepthDec(buf, 0x40)

	want := []byte{
		0x49, 0x8B, 0x87, 0x40, 0x00, 0x00, 0x00, // mov rax, [r15+0x40]
		0x48, 0xFF, 0x08, // dec qword ptr [rax]
	}
	for i, b := range want {
		if buf[i] != b {
			t.Errorf("buf[%d] = 0x%02X, want 0x%02X", i, buf[i], b)
		}
	}
}

// TestPJ5_EmitFrameInlineLoadCISlotAddr_Length verifies the amd64 CI-segment
// depth-th frame address load template length (30 bytes, per §9.20 Option B Spike 1).
func TestPJ5_EmitFrameInlineLoadCISlotAddr_Length(t *testing.T) {
	var buf []byte
	buf = EmitFrameInlineLoadCISlotAddr(buf, 0x40, 0x48)
	if len(buf) != EncodedFrameInlineLoadCISlotAddrLen {
		t.Errorf("EmitFrameInlineLoadCISlotAddr 长度 = %d, want %d",
			len(buf), EncodedFrameInlineLoadCISlotAddrLen)
	}
}

// TestPJ5_EmitFrameInlineLoadCISlotAddr_Encoding verifies the key bytes (imul rcx, rcx, 40).
//
// Verify: the sequence contains [48 6B C9 28] = imul rcx, rcx, 40 (40 = ciSlotBytes = ciWords*8)
func TestPJ5_EmitFrameInlineLoadCISlotAddr_Encoding(t *testing.T) {
	var buf []byte
	buf = EmitFrameInlineLoadCISlotAddr(buf, 0x40, 0x48)
	// sequence layout:
	// [0..6]   mov rax, [r15+ciDepthOff]   (7 bytes)
	// [7..9]   mov rcx, rax                (3 bytes)
	// [10..12] mov rcx, [rcx]              (3 bytes)
	// [13..19] mov rax, [r15+ciSegBaseOff] (7 bytes)
	// [20..22] mov rax, [rax]              (3 bytes)
	// [23..26] imul rcx, rcx, 40           (4 bytes)
	// [27..29] add rax, rcx                (3 bytes)
	wantImul := []byte{0x48, 0x6B, 0xC9, 0x28}
	for i, b := range wantImul {
		if buf[23+i] != b {
			t.Errorf("imul rcx, rcx, 40 字节[%d]=0x%02X, want 0x%02X(off %d)",
				i, buf[23+i], b, 23+i)
		}
	}
	// add rax, rcx should be at offset 27-29
	wantAdd := []byte{0x48, 0x01, 0xC8}
	for i, b := range wantAdd {
		if buf[27+i] != b {
			t.Errorf("add rax, rcx 字节[%d]=0x%02X, want 0x%02X(off %d)",
				i, buf[27+i], b, 27+i)
		}
	}
}

// TestPJ5_EmitFrameInlineWriteCIWord_Length verifies the amd64 CI-frame word
// write template length (10-byte mov rcx imm64 + 4-byte mov [rax+disp8] rcx = 14 bytes).
// Per §9.20 Option B Spike 1.
func TestPJ5_EmitFrameInlineWriteCIWord_Length(t *testing.T) {
	var buf []byte
	buf = EmitFrameInlineWriteCIWord(buf, 0, 0xDEADBEEF)
	if len(buf) != EncodedFrameInlineWriteCIWordLen {
		t.Errorf("EmitFrameInlineWriteCIWord 长度 = %d, want %d",
			len(buf), EncodedFrameInlineWriteCIWordLen)
	}
}

// TestPJ5_EmitFrameInlineBuildVoid0ArgSkeleton_Length verifies the amd64 Spike 1
// enterLuaFrame byte-level inline skeleton v2 total length (30 + 42 + 20 + 4 + 14 + 10 =
// 120 bytes; word3 switches to runtime GCRef load, adding 10 bytes).
// Per §9.20 Option B Spike 1.
func TestPJ5_EmitFrameInlineBuildVoid0ArgSkeleton_Length(t *testing.T) {
	var buf []byte
	words := FrameInlineCISlotWords{
		Word0: 0x0000000100000010, // funcIdx=1, base=0x10
		Word1: 0x0000000000000020, // top=0x20
		Word2: 0x0000000000000005, // protoID=5
		Word3: 0,                  // ignored in v2, switched to callARecv load
		Word4: 0,
	}
	buf = EmitFrameInlineBuildVoid0ArgSkeleton(buf, 0x40, 0x48, 5 /*callARecv*/, words)
	if len(buf) != EncodedFrameInlineBuildVoid0ArgSkeletonLen {
		t.Errorf("EmitFrameInlineBuildVoid0ArgSkeleton 长度 = %d, want %d",
			len(buf), EncodedFrameInlineBuildVoid0ArgSkeletonLen)
	}
}

// TestPJ5_EmitFrameInlinePopVoid0ArgSkeleton_CIDepthDecPlusRet verifies the amd64
// Spike 1 popCallInfo skeleton byte-level = CIDepthDec 10 byte + xor eax,eax 2 byte +
// ret 1 byte = 13 byte (per commit-5l fixing the missing ret bug).
func TestPJ5_EmitFrameInlinePopVoid0ArgSkeleton_CIDepthDecPlusRet(t *testing.T) {
	var bufA []byte
	bufA = EmitFrameInlinePopVoid0ArgSkeleton(bufA, 0x40)
	if len(bufA) != EncodedFrameInlinePopVoid0ArgSkeletonLen {
		t.Errorf("PopVoid0ArgSkeleton 长度 = %d, want %d",
			len(bufA), EncodedFrameInlinePopVoid0ArgSkeletonLen)
	}
	// first 10 byte = CIDepthDec (EmitMovqRaxFromR15Disp + EmitDecQwordPtrAtRax)
	var bufB []byte
	bufB = EmitFrameInlineCIDepthDec(bufB, 0x40)
	for i := range bufB {
		if bufA[i] != bufB[i] {
			t.Errorf("字节[%d] 差异:Pop=0x%02X, CIDepthDec=0x%02X",
				i, bufA[i], bufB[i])
		}
	}
	// last 3 byte = 0x31 0xc0 0xc3 (xor eax,eax + ret)
	if bufA[10] != 0x31 || bufA[11] != 0xC0 || bufA[12] != 0xC3 {
		t.Errorf("PopVoid0Arg 末 3 byte = 0x%02X%02X%02X, want 0x31C0C3(xor eax,eax + ret)",
			bufA[10], bufA[11], bufA[12])
	}
}

// TestPJ5_EmitFrameInlineLoadClosureGCRef_Length verifies the amd64 Spike 1 closure
// GCRef NaN-box decode template length (7+10+3 = 20 bytes).
func TestPJ5_EmitFrameInlineLoadClosureGCRef_Length(t *testing.T) {
	var buf []byte
	buf = EmitFrameInlineLoadClosureGCRef(buf, 5)
	if len(buf) != EncodedFrameInlineLoadClosureGCRefLen {
		t.Errorf("EmitFrameInlineLoadClosureGCRef 长度 = %d, want %d",
			len(buf), EncodedFrameInlineLoadClosureGCRefLen)
	}
}

// TestPJ5_EmitFrameInlineLoadClosureGCRef_Encoding verifies the key bytes (mov rcx [rbx]
// + mov rdx payloadMask + and rcx rdx).
func TestPJ5_EmitFrameInlineLoadClosureGCRef_Encoding(t *testing.T) {
	var buf []byte
	buf = EmitFrameInlineLoadClosureGCRef(buf, 5)

	// mov rcx, [rbx + 40] at offset 0-6 (48 8B 8B disp32 + 5*8=40)
	if buf[0] != 0x48 || buf[1] != 0x8B || buf[2] != 0x8B {
		t.Errorf("mov rcx,[rbx+disp32] 前缀 = 0x%02X%02X%02X, want 0x488B8B",
			buf[0], buf[1], buf[2])
	}
	if buf[3] != 40 {
		t.Errorf("disp32 = %d, want 40(=5*8)", buf[3])
	}
	// mov rdx, payloadMask at offset 7-16 (48 BA + 8-byte imm)
	if buf[7] != 0x48 || buf[8] != 0xBA {
		t.Errorf("mov rdx imm64 前缀 = 0x%02X%02X, want 0x48BA",
			buf[7], buf[8])
	}
	// imm64 = 0x0000_FFFF_FFFF_FFFF (LE byte order: FF FF FF FF FF FF 00 00)
	wantMask := []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0x00, 0x00}
	for i, b := range wantMask {
		if buf[9+i] != b {
			t.Errorf("payloadMask imm64 字节[%d]=0x%02X, want 0x%02X", i, buf[9+i], b)
		}
	}
	// and rcx, rdx at offset 17-19 (48 21 D1)
	if buf[17] != 0x48 || buf[18] != 0x21 || buf[19] != 0xD1 {
		t.Errorf("and rcx,rdx 字节[17-19]=0x%02X%02X%02X, want 0x4821D1",
			buf[17], buf[18], buf[19])
	}
}

// TestPJ5_EmitFrameInlineWriteCIWordFromRcx_Length verifies the amd64 word
// write-from-rcx template length (4 bytes).
func TestPJ5_EmitFrameInlineWriteCIWordFromRcx_Length(t *testing.T) {
	var buf []byte
	buf = EmitFrameInlineWriteCIWordFromRcx(buf, 3)
	if len(buf) != EncodedFrameInlineWriteCIWordFromRcxLen {
		t.Errorf("EmitFrameInlineWriteCIWordFromRcx 长度 = %d, want %d",
			len(buf), EncodedFrameInlineWriteCIWordFromRcxLen)
	}
}

// TestPJ5_EmitFrameInlineWriteCIWordFromRcx_Encoding verifies the disp8 for each wordIdx.
func TestPJ5_EmitFrameInlineWriteCIWordFromRcx_Encoding(t *testing.T) {
	for _, wordIdx := range []uint8{0, 1, 2, 3, 4} {
		var buf []byte
		buf = EmitFrameInlineWriteCIWordFromRcx(buf, wordIdx)

		// mov [rax + wordIdx*8], rcx — 48 89 48 disp8
		if buf[0] != 0x48 || buf[1] != 0x89 || buf[2] != 0x48 {
			t.Errorf("word_idx=%d: mov [rax+disp8] rcx 前缀 = 0x%02X%02X%02X, want 0x488948",
				wordIdx, buf[0], buf[1], buf[2])
		}
		wantDisp := int8(wordIdx) * 8
		if int8(buf[3]) != wantDisp {
			t.Errorf("word_idx=%d: disp8 = %d, want %d", wordIdx, int8(buf[3]), wantDisp)
		}
	}
}

// TestPJ5_EmitFrameInlineBuildVoid0ArgSkeleton_StructuralBoundaries verifies the
// skeleton v2 inter-segment boundaries (per v2, word3 switches to runtime GCRef load):
//
//	[0..29]   LoadCISlotAddr            (30 bytes)
//	[30..43]  WriteCIWord(0) imm64       (14 bytes)
//	[44..57]  WriteCIWord(1) imm64       (14 bytes)
//	[58..71]  WriteCIWord(2) imm64       (14 bytes)
//	[72..91]  LoadClosureGCRef(callARecv)(20 bytes)— rcx = R(callARecv) GCRef
//	[92..95]  WriteCIWordFromRcx(3)      (4 bytes) — word3 = rcx
//	[96..109] WriteCIWord(4) imm64       (14 bytes)
//	[110..119] CIDepthInc                 (10 bytes)
//
// Verifies correct segment stacking by checking the positions of signature bytes.
func TestPJ5_EmitFrameInlineBuildVoid0ArgSkeleton_StructuralBoundaries(t *testing.T) {
	var buf []byte
	words := FrameInlineCISlotWords{Word0: 1, Word1: 2, Word2: 3, Word4: 5}
	buf = EmitFrameInlineBuildVoid0ArgSkeleton(buf, 0x40, 0x48, 5 /*callARecv*/, words)

	// LoadCISlotAddr segment tail: add rax, rcx at offset 27-29 (0x48 0x01 0xC8)
	if buf[27] != 0x48 || buf[28] != 0x01 || buf[29] != 0xC8 {
		t.Errorf("LoadCISlotAddr 段末 add rax,rcx 字节[27-29]=0x%02X%02X%02X, want 0x4801C8",
			buf[27], buf[28], buf[29])
	}
	// WriteCIWord(0) segment head: offset 30 mov rcx, imm64 (0x48 0xB9)
	if buf[30] != 0x48 || buf[31] != 0xB9 {
		t.Errorf("WriteCIWord(0) 段头 mov rcx imm64 字节[30-31]=0x%02X%02X, want 0x48B9",
			buf[30], buf[31])
	}
	// LoadClosureGCRef segment head: offset 72 mov rcx, [rbx + 5*8=40] (0x48 0x8B 0x8B)
	if buf[72] != 0x48 || buf[73] != 0x8B || buf[74] != 0x8B {
		t.Errorf("LoadClosureGCRef 段头 mov rcx [rbx+disp32] 字节[72-74]=0x%02X%02X%02X, want 0x488B8B",
			buf[72], buf[73], buf[74])
	}
	// WriteCIWordFromRcx segment: offset 92 mov [rax+24] rcx (0x48 0x89 0x48 0x18)
	if buf[92] != 0x48 || buf[93] != 0x89 || buf[94] != 0x48 || buf[95] != 0x18 {
		t.Errorf("WriteCIWordFromRcx(3) 字节[92-95]=0x%02X%02X%02X%02X, want 0x48894818",
			buf[92], buf[93], buf[94], buf[95])
	}
	// WriteCIWord(4) segment head: offset 96 mov rcx imm64 (0x48 0xB9)
	if buf[96] != 0x48 || buf[97] != 0xB9 {
		t.Errorf("WriteCIWord(4) 段头字节[96-97]=0x%02X%02X, want 0x48B9",
			buf[96], buf[97])
	}
	// CIDepthInc segment head: offset 110 mov rax, [r15+0x40] (0x49 0x8B 0x87)
	if buf[110] != 0x49 || buf[111] != 0x8B || buf[112] != 0x87 {
		t.Errorf("CIDepthInc 段头 mov rax,[r15+disp32] 字节[110-112]=0x%02X%02X%02X, want 0x498B87",
			buf[110], buf[111], buf[112])
	}
	// CIDepthInc segment tail: offset 117-119 inc qword ptr [rax] (0x48 0xFF 0x00)
	if buf[117] != 0x48 || buf[118] != 0xFF || buf[119] != 0x00 {
		t.Errorf("CIDepthInc 段末 inc qword ptr [rax] 字节[117-119]=0x%02X%02X%02X, want 0x48FF00",
			buf[117], buf[118], buf[119])
	}
}

// TestPJ5_EmitFrameInlineWriteCIWord_Encoding verifies the key bytes for each word_idx
// (mov rcx imm64 + mov [rax+wordIdx*8] rcx).
func TestPJ5_EmitFrameInlineWriteCIWord_Encoding(t *testing.T) {
	for _, wordIdx := range []uint8{0, 1, 2, 3, 4} {
		var buf []byte
		buf = EmitFrameInlineWriteCIWord(buf, wordIdx, 0xCAFEBABE12345678)

		// mov rcx, imm64 at offset 0 (REX.W=0x48 + 0xB9=mov rcx, imm64 + 8-byte imm)
		if buf[0] != 0x48 || buf[1] != 0xB9 {
			t.Errorf("word_idx=%d: mov rcx, imm64 前缀 = 0x%02X%02X, want 0x48B9",
				wordIdx, buf[0], buf[1])
		}
		// mov [rax + wordIdx*8], rcx at offset 10 (48 89 48 disp8)
		if buf[10] != 0x48 || buf[11] != 0x89 || buf[12] != 0x48 {
			t.Errorf("word_idx=%d: mov [rax+disp8] rcx 前缀 = 0x%02X%02X%02X, want 0x488948",
				wordIdx, buf[10], buf[11], buf[12])
		}
		// disp8 = wordIdx * 8
		wantDisp := int8(wordIdx) * 8
		if int8(buf[13]) != wantDisp {
			t.Errorf("word_idx=%d: disp8 = %d, want %d", wordIdx, int8(buf[13]), wantDisp)
		}
	}
}

// TestPJ5_EmitFrameInlineExitHelperRequest_Length verifies the amd64 Spike 1 trampoline
// exit-resume protocol exit-helper-request segment total length (24 bytes, per §9.20.9 (4)
// optimized version: 10 + 4 + 5 + 4 + 1 = 24; reorder rax to reuse ExitInlineHelper as the return value).
func TestPJ5_EmitFrameInlineExitHelperRequest_Length(t *testing.T) {
	var buf []byte
	// use small disp8 offset (exitReason=20, exitArg0=64, equivalent to the actual jitContext
	// offset, disp8 form).
	buf = EmitFrameInlineExitHelperRequest(buf, 20 /*exitReasonOff*/, 64 /*exitArg0Off*/, 1 /*HelperRunCallee*/)
	if len(buf) != EncodedFrameInlineExitHelperRequestLen {
		t.Errorf("EmitFrameInlineExitHelperRequest 长度 = %d, want %d (disp8 form)",
			len(buf), EncodedFrameInlineExitHelperRequestLen)
	}
}

// TestPJ5_EmitFrameInlineExitHelperRequest_Encoding verifies the key byte structure (per
// §9.20.9 (4) protocol spec):
//
//	[0..1]   48 B8        ; mov rax, imm64
//	[2..9]   helperCode imm64 (HelperRunCallee=1 → 01 00 00 00 00 00 00 00)
//	[10..12] 49 89 47     ; mov [r15+disp8], rax (REX.WB + opcode + ModRM)
//	[13]     disp8        ; exitArg0Off
//	[14]     B8           ; mov eax, imm32
//	[15..18] 03 00 00 00  ; ExitInlineHelper=3
//	[19..21] 41 89 47     ; mov [r15+disp8], eax (REX.B + opcode + ModRM 32-bit)
//	[22]     disp8        ; exitReasonOff
//	[23]     C3           ; ret
func TestPJ5_EmitFrameInlineExitHelperRequest_Encoding(t *testing.T) {
	var buf []byte
	buf = EmitFrameInlineExitHelperRequest(buf, 20, 64, 1)

	// mov rax, helperCode(48 B8 imm64)
	if buf[0] != 0x48 || buf[1] != 0xB8 {
		t.Errorf("[0-1] mov rax, imm64 = 0x%02X%02X, want 0x48B8", buf[0], buf[1])
	}
	if buf[2] != 0x01 || buf[3] != 0x00 || buf[4] != 0x00 || buf[5] != 0x00 {
		t.Errorf("[2-5] helperCode imm64 lo = 0x%02X%02X%02X%02X, want 0x01000000 (HelperRunCallee=1)",
			buf[2], buf[3], buf[4], buf[5])
	}
	// mov [r15+exitArg0Off], rax(49 89 47 disp8)
	if buf[10] != 0x49 || buf[11] != 0x89 || buf[12] != 0x47 {
		t.Errorf("[10-12] mov [r15+disp8], rax 前缀 = 0x%02X%02X%02X, want 0x498947",
			buf[10], buf[11], buf[12])
	}
	if buf[13] != 64 {
		t.Errorf("[13] exitArg0Off disp8 = %d, want 64", buf[13])
	}
	// mov eax, ExitInlineHelper(B8 03 00 00 00)
	if buf[14] != 0xB8 || buf[15] != 0x03 {
		t.Errorf("[14-15] mov eax, imm32 = 0x%02X%02X, want 0xB803", buf[14], buf[15])
	}
	// mov [r15+exitReasonOff], eax(41 89 47 disp8,32-bit)
	if buf[19] != 0x41 || buf[20] != 0x89 || buf[21] != 0x47 {
		t.Errorf("[19-21] mov [r15+disp8], eax 前缀 = 0x%02X%02X%02X, want 0x418947",
			buf[19], buf[20], buf[21])
	}
	if buf[22] != 20 {
		t.Errorf("[22] exitReasonOff disp8 = %d, want 20", buf[22])
	}
	// ret(C3)
	if buf[23] != 0xC3 {
		t.Errorf("[23] ret = 0x%02X, want 0xC3", buf[23])
	}
}
