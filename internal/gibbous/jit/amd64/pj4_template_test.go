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
