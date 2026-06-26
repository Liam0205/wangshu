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
