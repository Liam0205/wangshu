//go:build wangshu_p4 && linux && amd64

package amd64

import (
	"testing"
)

// pj4_template_test.go —— PJ4 IC ArrayHit 模板字节级长度自洽 + emit 不
// panic 测试。真 mmap+RX round-trip 需要构造真 table(arena + table head
// + array 段),涉及跨 internal package 依赖,留 crescent e2e 真升层测试。

func TestPJ4_EmitGetTableArrayHit_Length(t *testing.T) {
	var buf []byte
	buf = EmitGetTableArrayHit(buf,
		1,          // aReg = R(1)
		0,          // bReg = R(0) (table)
		7,          // stableShape (gen)
		3,          // stableIndex
		16,         // arenaBaseOff(jitContext field offset 示例)
		0xCAFEBABE, // deoptCode
	)

	// 不强制具体长度,只验非空 + 不 panic
	if len(buf) == 0 {
		t.Fatal("EmitGetTableArrayHit returned empty buf")
	}
	t.Logf("EmitGetTableArrayHit emitted %d bytes", len(buf))

	// 验证模板末尾有 ret(0xC3)
	if buf[len(buf)-1] != 0xC3 {
		t.Errorf("template should end with ret(0xC3), got 0x%02x", buf[len(buf)-1])
	}
}

func TestPJ4_EmitGetTableArrayHit_DeoptBlockPresent(t *testing.T) {
	var buf []byte
	const deopt uint64 = 0xDEADBEEF12345678
	buf = EmitGetTableArrayHit(buf, 0, 0, 0, 0, 0, deopt)

	// deopt block 是 mov rax, deoptCode + ret(48 B8 deoptCode + C3 = 11 字节)
	// 在模板末尾。检查最后 11 字节
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
	// 末尾 ret
	if tail[10] != 0xC3 {
		t.Errorf("deopt block tail = 0x%02x, want 0xC3(ret)", tail[10])
	}
}

// TestPJ4_EmitGetTableArrayHit_StrictIsTableGuard 验严密 IsTable guard 字节
// 序列在模板前段(7-22 字节):
//
//	[ 0-6 ] mov rax, [rbx + bReg*8]    (7 字节)
//	[ 7-10] shr rax, 48                (4 字节,48 C1 E8 30)
//	[11-15] cmp eax, 0xFFFC            (5 字节,3D FC FF 00 00)
//	[16-21] jne deopt (rel32 placeholder)(6 字节,0F 85 ...)
//
// 严密 guard 替换原简化版 mov rcx,0xFFFC<<48 + cmp rax,rcx + jb deopt
// (10+3+6=19 字节),省 4 字节 + 真严密 IsTable check。
func TestPJ4_EmitGetTableArrayHit_StrictIsTableGuard(t *testing.T) {
	var buf []byte
	buf = EmitGetTableArrayHit(buf, 1, 0, 7, 3, 16, 0xCAFEBABE)

	// 跳过前 7 字节(mov rax, [rbx + 0*8])= 48 8B 03 00 00 00 00
	// 实际 EmitMovqRaxFromMemReg 可能用 [rbx+disp8] 而非 disp32,断言时
	// 先看 shr rax, 48 出现位置(48 C1 E8 30)
	const shrSig = uint32(0x48C1E830) // bytes: 48 C1 E8 30 -> as uint32 LE
	// 直接在 buf 前 20 字节内 grep shr 字节序列
	found := false
	for i := 0; i <= 20-4; i++ {
		if buf[i] == 0x48 && buf[i+1] == 0xC1 && buf[i+2] == 0xE8 && buf[i+3] == 48 {
			found = true
			// 紧随其后应是 cmp eax, 0xFFFC = 3D FC FF 00 00
			if i+8 >= len(buf) {
				t.Fatalf("cmp 段越界")
			}
			if buf[i+4] != 0x3D ||
				buf[i+5] != 0xFC || buf[i+6] != 0xFF ||
				buf[i+7] != 0x00 || buf[i+8] != 0x00 {
				t.Errorf("cmp eax, 0xFFFC bytes wrong at %d: got %x, want 3D FC FF 00 00",
					i+4, buf[i+4:i+9])
			}
			// 紧随其后应是 jne rel32 = 0F 85 ...(6 字节)
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

// TestPJ4_EmitGetTableArrayHit_NoSimplifiedGuard 反向断言:模板字节不应包含
// 原简化版 IsTable guard 序列(mov rcx, 0xFFFC<<48 = 48 B9 00 00 00 00 00 00
// FC FF + cmp rax, rcx = 48 39 C8 + jb rel32 = 0F 82 ...)。
//
// 严密版替换简化版后,模板字节序列里不应再出现 0xFFFC<<48 = 0xFFFC_0000_0000_0000
// 的小端字节 00 00 00 00 00 00 FC FF。注意 nil mask(0xFFFE_...)和
// stableShape 仍可能出现在模板末尾(nil check + cmp eax stableShape),所以
// 我们只查特征字节序列 mov rcx, imm64 + cmp rax, rcx + jb 组合的存在。
//
// 简化版组合特征:48 B9 [..imm64..] 48 39 C8 0F 82
func TestPJ4_EmitGetTableArrayHit_NoSimplifiedGuard(t *testing.T) {
	var buf []byte
	buf = EmitGetTableArrayHit(buf, 1, 0, 7, 3, 16, 0xCAFEBABE)

	// 简化版特征:48 B9 [imm64 8字节] 48 39 C8 0F 82(共 15 字节)
	// 严密版替换后,buf 前 25 字节内不应出现这个组合(后续 mov rcx 是 nil
	// mask 0xFFFE...,但后跟的是 cmp rax rcx + je 而非 jb,所以特征不会
	// 误匹配)。
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

// TestPJ4_EmitGetTableNodeHit_Length 验 NodeHit 模板字节级长度自洽。
// 预期长度 ~159 字节(ArrayHit 132 字节 + key 比对 27 字节:NodeKey load
// 8 + mov rdx imm64 10 + cmp rax rdx 3 + jne rel32 6)。
func TestPJ4_EmitGetTableNodeHit_Length(t *testing.T) {
	var buf []byte
	buf = EmitGetTableNodeHit(buf,
		1,                     // aReg = R(1)
		0,                     // bReg = R(0)(table)
		7,                     // stableShape (gen)
		2,                     // stableIndex
		0xFFFB_DEAD_BEEF_CAFE, // stableKey(模拟 string NaN-box)
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

// TestPJ4_EmitGetTableNodeHit_StrictIsTableGuard 验 NodeHit 模板复用同款
// 严密 IsTable guard 字节序列(对位 ArrayHit StrictIsTableGuard)。
func TestPJ4_EmitGetTableNodeHit_StrictIsTableGuard(t *testing.T) {
	var buf []byte
	buf = EmitGetTableNodeHit(buf, 1, 0, 7, 2, 0xFFFB_0000_0000_0001,
		16, 0xCAFEBABE)

	// 查 shr rax, 48(48 C1 E8 30)在模板前 20 字节
	found := false
	for i := 0; i <= 20-4; i++ {
		if buf[i] == 0x48 && buf[i+1] == 0xC1 && buf[i+2] == 0xE8 && buf[i+3] == 48 {
			found = true
			// 紧随其后 cmp eax, 0xFFFC + jne rel32
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

// TestPJ4_EmitGetTableNodeHit_KeyComparison 验 NodeHit 模板含 key 比对段
// (mov rdx, stableKey + cmp rax, rdx + jne deopt 19 字节序列)。
//
// stableKey 用 0xFFFB_1234_5678_9ABC 模拟 string NaN-box,验 imm64 字节
// 序列 BC 9A 78 56 34 12 FB FF(小端)出现在 mov rdx 后。
func TestPJ4_EmitGetTableNodeHit_KeyComparison(t *testing.T) {
	const stableKey uint64 = 0xFFFB_1234_5678_9ABC
	var buf []byte
	buf = EmitGetTableNodeHit(buf, 1, 0, 7, 2, stableKey, 16, 0xCAFEBABE)

	// 查 mov rdx imm64 = 48 BA + imm64 LE 字节
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
			// 后跟 cmp rax, rdx = 48 39 D0(3 字节)+ jne rel32 = 0F 85 ...(6 字节)
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

// TestPJ4_EmitSetTableArrayHit_Length 验 PJ4 SETTABLE ArrayHit 模板字节
// 级长度自洽。预期 ~122 字节(getter ArrayHit 132 字节但 nil check 改 load
// R(C)/反向 store + 无 NodeKey 比对,简化掉 nil mask 加载 + cmp + je)。
func TestPJ4_EmitSetTableArrayHit_Length(t *testing.T) {
	var buf []byte
	buf = EmitSetTableArrayHit(buf,
		0,          // aReg = R(0)(table)
		1,          // cReg = R(1)(value)
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

// TestPJ4_EmitSetTableArrayHit_StrictIsTableGuard 验 SETTABLE 模板复用同款
// 严密 IsTable guard(shr@7 / cmp@11 / jne@16)。
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

// TestPJ4_EmitSetTableArrayHit_ReverseStore 验 SETTABLE 模板含反向 store
// 字节序列(load rdx from [rbx+cReg*8] = 48 8B 93 disp32 + mov [r14+rcx+...]
// from rdx = 49 89 94 0E disp32)。
func TestPJ4_EmitSetTableArrayHit_ReverseStore(t *testing.T) {
	var buf []byte
	buf = EmitSetTableArrayHit(buf, 0, 1, 7, 3, 16, 0xCAFEBABE) // cReg=1 → disp=8

	// 查 mov rdx, [rbx + 8] = 48 8B 93 08 00 00 00(7 字节)
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

	// 紧随其后:反向 store mov [r14+rcx+stableIndex*8], rdx
	// stableIndex=3, *8 = 24 → 49 89 94 0E 18 00 00 00(8 字节)
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

// TestPJ4_EmitSelfArrayHit_Length 验 PJ4 SELF ArrayHit 模板字节级长度自洽。
// 预期 ~141 字节(getter ArrayHit 132 + R(A+1) 拷段 7 字节,实测取决于
// EmitMovqMemRegFromRax disp 编码)。
func TestPJ4_EmitSelfArrayHit_Length(t *testing.T) {
	var buf []byte
	buf = EmitSelfArrayHit(buf,
		2,          // aReg = R(2)(method 结果)→ R(A+1)=R(3)
		0,          // bReg = R(0)(obj)
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

// TestPJ4_EmitSelfArrayHit_SelfStore 验 SELF 模板含「R(A+1) := R(B)」拷段
// (load rax from [rbx+bReg*8] + store [rbx+(aReg+1)*8] from rax 在模板前部)。
func TestPJ4_EmitSelfArrayHit_SelfStore(t *testing.T) {
	var buf []byte
	// aReg=2 → R(A+1) = R(3) 槽偏移 24
	// bReg=0 → R(B) 槽偏移 0
	buf = EmitSelfArrayHit(buf, 2, 0, 7, 1, 16, 0xCAFEBABE)

	// 模板前部应有:
	// [0-6] load rax from [rbx + 0]  = 48 8B 03 00 00 00 00(7 字节)
	// [7-13] store [rbx + 24], rax  = 48 89 83 18 00 00 00(7 字节)
	// 紧跟 shr rax, 48 = 48 C1 E8 30(4 字节)
	if len(buf) < 18 {
		t.Fatal("模板太短")
	}
	// 第二段 store [rbx+24], rax 字节
	// EmitMovqMemRegFromRax 编码:48 89 83 (rm=011=rbx) disp32 (or disp8 short)
	// 用 disp32 时 ModRM=83=mod10 reg=000 rm=011;disp8 时 ModRM=43=mod01 reg=000 rm=011
	// 实测看哪个
	storeStart := 7
	if buf[storeStart] != 0x48 || buf[storeStart+1] != 0x89 {
		t.Errorf("store R(A+1) 前缀错位 at %d: got %x %x, want 48 89",
			storeStart, buf[storeStart], buf[storeStart+1])
	}
	t.Logf("SELF 模板前 18 字节 = %x", buf[:18])
}

// TestPJ4_EmitSetTableNodeHit_Length 验 SETTABLE NodeHit 模板字节长度自洽。
// 预期 140 字节(GetTable NodeHit 159 - getter 段 34 + setter 段 15)。
//
// **精确长度断言**(承外部审查 🟢 反馈):锁死布局契约,防未来误改原语导致
// 长度漂移而 length 测试不能抓出。
func TestPJ4_EmitSetTableNodeHit_Length(t *testing.T) {
	const stableKey uint64 = 0xFFFB_1234_5678_9ABC
	var buf []byte
	buf = EmitSetTableNodeHit(buf,
		0, // aReg = R(0)(table)
		1, // cReg = R(1)(value)
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

// TestPJ4_EmitSetTableNodeHit_StrictIsTableGuard 验 SETTABLE NodeHit 模板
// 复用同款严密 IsTable guard。
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

// TestPJ4_EmitSetTableNodeHit_KeyCompareAndReverseStore 验 NodeHit setter
// 含 key 比对段 + 反向 store NodeVal 段。
func TestPJ4_EmitSetTableNodeHit_KeyCompareAndReverseStore(t *testing.T) {
	const stableKey uint64 = 0xFFFB_DEAD_BEEF_CAFE
	var buf []byte
	// aReg=0 → R(A)=[rbx+0]
	// cReg=1 → R(C)=[rbx+8](value)
	// stableIndex=2 → NodeKey @ rcx+48 / NodeVal @ rcx+56
	buf = EmitSetTableNodeHit(buf, 0, 1, 7, 2, stableKey, 16, 0xCAFEBABE)

	// 查 mov rdx, stableKey = 48 BA + imm64 LE
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

	// 紧随其后:cmp rax, rdx(3 字节)+ jne rel32(6 字节)= 9 字节 key 比对段
	if movRdxOff+12 >= len(buf) {
		t.Fatal("key 比对段越界")
	}
	if buf[movRdxOff+10] != 0x48 || buf[movRdxOff+11] != 0x39 || buf[movRdxOff+12] != 0xD0 {
		t.Errorf("cmp rax, rdx 字节错位")
	}
	if buf[movRdxOff+13] != 0x0F || buf[movRdxOff+14] != 0x85 {
		t.Errorf("jne rel32 prefix 字节错位")
	}

	// 紧接 key 比对(9 字节)后:load R(C) → rdx(7 字节)+ 反向 store
	// (8 字节)
	// load rdx 段:48 8B 93 disp32(cReg=1 → disp=8)
	loadRdxOff := movRdxOff + 9 + 9 - 1 // movRdx(10) + cmp(3) + jne(6) = 19 字节后开始
	// 实际:movRdx 起点 movRdxOff,占 10 字节;cmp 3 字节,jne 6 字节
	// 总 19 字节后是 load R(C) → rdx 段
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

	// 紧随 load rdx(7 字节)后:反向 store mov [r14+rcx+stableIndex*24+8], rdx
	// stableIndex=2,24*2+8=56 → 49 89 94 0E 38 00 00 00(8 字节)
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
	_ = loadRdxOff // 保留作未来扩展用,当前不验证
}

// TestPJ4_EmitSelfNodeHit_Length 验 SELF NodeHit 模板字节长度自洽。
// 预期 166 字节(SELF ArrayHit 139 + key 比对段 27)。
//
// **精确长度断言**(承外部审查 🟢 反馈):锁死布局契约,防未来误改原语导致
// 长度漂移而 length 测试不能抓出。
func TestPJ4_EmitSelfNodeHit_Length(t *testing.T) {
	const stableKey uint64 = 0xFFFB_1234_5678_9ABC
	var buf []byte
	buf = EmitSelfNodeHit(buf,
		2, // aReg = R(2)(method 结果)→ R(A+1)=R(3)
		0, // bReg = R(0)(obj)
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

// TestPJ4_EmitSelfNodeHit_SelfStoreAndKeyCompare 验 SELF NodeHit 模板含:
//   - 前部 R(A+1) := R(B) 拷段(对位 SELF ArrayHit)
//   - 后部 NodeKey 比对段(mov rdx stableKey + cmp rax rdx + jne)
func TestPJ4_EmitSelfNodeHit_SelfStoreAndKeyCompare(t *testing.T) {
	const stableKey uint64 = 0xFFFB_AAAA_BBBB_CCCC
	var buf []byte
	// aReg=2 → R(A+1)=R(3) 槽偏移 24
	// bReg=0 → R(B) 槽偏移 0
	buf = EmitSelfNodeHit(buf, 2, 0, 7, 1, stableKey, 16, 0xCAFEBABE)

	// 前部:load R(B) + store R(A+1) (14 字节)
	if buf[0] != 0x48 || buf[1] != 0x8B {
		t.Errorf("SELF NodeHit 前部 load R(B) 字节错位")
	}
	if buf[7] != 0x48 || buf[8] != 0x89 {
		t.Errorf("SELF NodeHit 前部 store R(A+1) 字节错位")
	}

	// 查 mov rdx, stableKey(中部 key 比对段)
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
			// 紧随其后 cmp rax, rdx + jne rel32
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

// TestPJ5_EmitSpecArgLoadK_Length 验 PJ5 SELF spec args 装载 K 字节级模板长度
// (10 字节 mov rax imm64 + 7 字节 mov [rbx+dst*8] rax = 17 字节)。
func TestPJ5_EmitSpecArgLoadK_Length(t *testing.T) {
	var buf []byte
	buf = EmitSpecArgLoadK(buf, 5, 0xDEADBEEF12345678)
	if len(buf) != 17 {
		t.Errorf("EmitSpecArgLoadK 长度 = %d, want 17", len(buf))
	}
}

// TestPJ5_EmitSpecArgLoadReg_Length 验 PJ5 SELF spec args 装载 reg 字节级模板长度
// (7 字节 mov rax [rbx+src*8] + 7 字节 mov [rbx+dst*8] rax = 14 字节)。
func TestPJ5_EmitSpecArgLoadReg_Length(t *testing.T) {
	var buf []byte
	buf = EmitSpecArgLoadReg(buf, 5, 3)
	if len(buf) != 14 {
		t.Errorf("EmitSpecArgLoadReg 长度 = %d, want 14", len(buf))
	}
}

// TestPJ5_EmitFrameInlineCIDepthInc_Length 验 ciDepth++ 字节级 inline 模板
// 长度(7 字节 mov rax [r15+disp32] + 3 字节 inc qword ptr [rax] = 10 字节)。
// 承 §9.20 Option B Spike 1 起手积木。
func TestPJ5_EmitFrameInlineCIDepthInc_Length(t *testing.T) {
	var buf []byte
	buf = EmitFrameInlineCIDepthInc(buf, 0x40)
	if len(buf) != EncodedFrameInlineCIDepthIncDecLen {
		t.Errorf("EmitFrameInlineCIDepthInc 长度 = %d, want %d", len(buf), EncodedFrameInlineCIDepthIncDecLen)
	}
}

// TestPJ5_EmitFrameInlineCIDepthInc_Encoding 验 inc 字节级编码。
//
// 期望:[4C 8B 87 40 00 00 00 | 48 FF 00] = mov rax,[r15+0x40] + inc [rax]
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

// TestPJ5_EmitFrameInlineCIDepthDec_Length 验 ciDepth-- 字节级 inline 模板
// 长度同 Inc(10 字节)。承 §9.20 popCallInfo 反向。
func TestPJ5_EmitFrameInlineCIDepthDec_Length(t *testing.T) {
	var buf []byte
	buf = EmitFrameInlineCIDepthDec(buf, 0x40)
	if len(buf) != EncodedFrameInlineCIDepthIncDecLen {
		t.Errorf("EmitFrameInlineCIDepthDec 长度 = %d, want %d", len(buf), EncodedFrameInlineCIDepthIncDecLen)
	}
}

// TestPJ5_EmitFrameInlineCIDepthDec_Encoding 验 dec 字节级编码(末尾 inc 改 dec)。
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

// TestPJ5_EmitFrameInlineLoadCISlotAddr_Length 验 amd64 CI 段第 depth 帧
// 地址加载模板长度(30 字节,承 §9.20 Option B Spike 1)。
func TestPJ5_EmitFrameInlineLoadCISlotAddr_Length(t *testing.T) {
	var buf []byte
	buf = EmitFrameInlineLoadCISlotAddr(buf, 0x40, 0x48)
	if len(buf) != EncodedFrameInlineLoadCISlotAddrLen {
		t.Errorf("EmitFrameInlineLoadCISlotAddr 长度 = %d, want %d",
			len(buf), EncodedFrameInlineLoadCISlotAddrLen)
	}
}

// TestPJ5_EmitFrameInlineLoadCISlotAddr_Encoding 验关键字节(imul rcx, rcx, 40)。
//
// 验:序列含 [48 6B C9 28]= imul rcx, rcx, 40(40 = ciSlotBytes = ciWords*8)
func TestPJ5_EmitFrameInlineLoadCISlotAddr_Encoding(t *testing.T) {
	var buf []byte
	buf = EmitFrameInlineLoadCISlotAddr(buf, 0x40, 0x48)
	// 序列布局:
	// [0..6]   mov rax, [r15+ciDepthOff]   (7字节)
	// [7..9]   mov rcx, rax                (3字节)
	// [10..12] mov rcx, [rcx]              (3字节)
	// [13..19] mov rax, [r15+ciSegBaseOff] (7字节)
	// [20..22] mov rax, [rax]              (3字节)
	// [23..26] imul rcx, rcx, 40           (4字节)
	// [27..29] add rax, rcx                (3字节)
	wantImul := []byte{0x48, 0x6B, 0xC9, 0x28}
	for i, b := range wantImul {
		if buf[23+i] != b {
			t.Errorf("imul rcx, rcx, 40 字节[%d]=0x%02X, want 0x%02X(off %d)",
				i, buf[23+i], b, 23+i)
		}
	}
	// add rax, rcx 应在 offset 27-29
	wantAdd := []byte{0x48, 0x01, 0xC8}
	for i, b := range wantAdd {
		if buf[27+i] != b {
			t.Errorf("add rax, rcx 字节[%d]=0x%02X, want 0x%02X(off %d)",
				i, buf[27+i], b, 27+i)
		}
	}
}

// TestPJ5_EmitFrameInlineWriteCIWord_Length 验 amd64 CI 帧 word 写入模板
// 长度(10 字节 mov rcx imm64 + 4 字节 mov [rax+disp8] rcx = 14 字节)。
// 承 §9.20 Option B Spike 1。
func TestPJ5_EmitFrameInlineWriteCIWord_Length(t *testing.T) {
	var buf []byte
	buf = EmitFrameInlineWriteCIWord(buf, 0, 0xDEADBEEF)
	if len(buf) != EncodedFrameInlineWriteCIWordLen {
		t.Errorf("EmitFrameInlineWriteCIWord 长度 = %d, want %d",
			len(buf), EncodedFrameInlineWriteCIWordLen)
	}
}

// TestPJ5_EmitFrameInlineBuildVoid0ArgSkeleton_Length 验 amd64 Spike 1
// enterLuaFrame 字节级 inline 骨架总长度(30 + 70 + 10 = 110 字节)。
// 承 §9.20 Option B Spike 1。
func TestPJ5_EmitFrameInlineBuildVoid0ArgSkeleton_Length(t *testing.T) {
	var buf []byte
	words := FrameInlineCISlotWords{
		Word0: 0x0000000100000010, // funcIdx=1, base=0x10
		Word1: 0x0000000000000020, // top=0x20
		Word2: 0x0000000000000005, // protoID=5
		Word3: 0xDEADBEEFCAFEBABE, // cl
		Word4: 0,
	}
	buf = EmitFrameInlineBuildVoid0ArgSkeleton(buf, 0x40, 0x48, words)
	if len(buf) != EncodedFrameInlineBuildVoid0ArgSkeletonLen {
		t.Errorf("EmitFrameInlineBuildVoid0ArgSkeleton 长度 = %d, want %d",
			len(buf), EncodedFrameInlineBuildVoid0ArgSkeletonLen)
	}
}

// TestPJ5_EmitFrameInlineBuildVoid0ArgSkeleton_StructuralBoundaries 验骨架
// 段间边界(LoadCISlotAddr 0-29 | WriteCIWord×5 30-99 | CIDepthInc 100-109)。
// 通过查特征字节位置验证段堆叠正确。
func TestPJ5_EmitFrameInlineBuildVoid0ArgSkeleton_StructuralBoundaries(t *testing.T) {
	var buf []byte
	words := FrameInlineCISlotWords{Word0: 1, Word1: 2, Word2: 3, Word3: 4, Word4: 5}
	buf = EmitFrameInlineBuildVoid0ArgSkeleton(buf, 0x40, 0x48, words)

	// LoadCISlotAddr 段末:add rax, rcx 在 offset 27-29(0x48 0x01 0xC8)
	if buf[27] != 0x48 || buf[28] != 0x01 || buf[29] != 0xC8 {
		t.Errorf("LoadCISlotAddr 段末 add rax,rcx 字节[27-29]=0x%02X%02X%02X, want 0x4801C8",
			buf[27], buf[28], buf[29])
	}
	// WriteCIWord 段 word0 起:offset 30 mov rcx, imm64(0x48 0xB9)
	if buf[30] != 0x48 || buf[31] != 0xB9 {
		t.Errorf("WriteCIWord 段头 mov rcx imm64 字节[30-31]=0x%02X%02X, want 0x48B9",
			buf[30], buf[31])
	}
	// CIDepthInc 段头:offset 100 mov rax, [r15+0x40](0x49 0x8B 0x87)
	if buf[100] != 0x49 || buf[101] != 0x8B || buf[102] != 0x87 {
		t.Errorf("CIDepthInc 段头 mov rax,[r15+disp32] 字节[100-102]=0x%02X%02X%02X, want 0x498B87",
			buf[100], buf[101], buf[102])
	}
	// CIDepthInc 段末:offset 107-109 inc qword ptr [rax](0x48 0xFF 0x00)
	if buf[107] != 0x48 || buf[108] != 0xFF || buf[109] != 0x00 {
		t.Errorf("CIDepthInc 段末 inc qword ptr [rax] 字节[107-109]=0x%02X%02X%02X, want 0x48FF00",
			buf[107], buf[108], buf[109])
	}
}

// TestPJ5_EmitFrameInlineWriteCIWord_Encoding 验各 word_idx 关键字节
// (mov rcx imm64 + mov [rax+wordIdx*8] rcx)。
func TestPJ5_EmitFrameInlineWriteCIWord_Encoding(t *testing.T) {
	for _, wordIdx := range []uint8{0, 1, 2, 3, 4} {
		var buf []byte
		buf = EmitFrameInlineWriteCIWord(buf, wordIdx, 0xCAFEBABE12345678)

		// mov rcx, imm64 在 offset 0(REX.W=0x48 + 0xB9=mov rcx, imm64 + 8 字节 imm)
		if buf[0] != 0x48 || buf[1] != 0xB9 {
			t.Errorf("word_idx=%d: mov rcx, imm64 前缀 = 0x%02X%02X, want 0x48B9",
				wordIdx, buf[0], buf[1])
		}
		// mov [rax + wordIdx*8], rcx 在 offset 10(48 89 48 disp8)
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
