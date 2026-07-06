//go:build linux && amd64

// fib_seg.go — issue #50 Spike 5 feasibility probe: a self-recursive
// mmap segment computing fib(n) entirely in-segment, with the recursive
// calls going segment-to-segment (baked absolute address, no host round
// trip). This validates the physical floor of EmitCallInline's
// segment-to-segment dispatch + native recursion depth before the
// production translator is rewired.
//
// The segment ABI here is deliberately minimal (n in RCX, result in RAX)
// to isolate the CALL+RET recursion cost from the Lua frame-management
// details. Production will carry the Lua value stack + CI frame in the
// segment, but the recursion-depth / cross-page-call physics are the
// same.
package p4callinline

import (
	"encoding/binary"
)

// EmitFibSegment emits a self-recursive fib segment. selfAddr is the
// segment's own start address (baked into the two recursive `call`
// sites). Because MmapCode needs the bytes before it knows the address,
// callers emit twice: once with selfAddr=0 to size it, then mmap, then
// re-emit with the real address and copy over — OR use the two-pass
// helper BuildFibSegment below which patches the addresses in place.
//
// Layout (n in RCX, result in RAX):
//
//	fib:
//	  cmp rcx, 2
//	  jge rec              ; n >= 2 → recursive case
//	  mov rax, rcx         ; base: return n
//	  ret
//	rec:
//	  push rcx             ; save n
//	  dec rcx              ; n-1
//	  mov rax, selfAddr
//	  call rax             ; fib(n-1) → rax
//	  pop rcx              ; restore n
//	  push rax             ; save fib(n-1)
//	  sub rcx, 2           ; n-2
//	  mov rax, selfAddr
//	  call rax             ; fib(n-2) → rax
//	  pop rdx              ; fib(n-1)
//	  add rax, rdx
//	  ret
func EmitFibSegment(selfAddr uintptr) []byte {
	buf := make([]byte, 0, 64)
	// cmp rcx, 2  (48 83 F9 02)
	buf = append(buf, 0x48, 0x83, 0xF9, 0x02)
	// jge rec (7D rel8) — target is the byte after the base-case block.
	// base case = mov rax, rcx (48 89 C8) + ret (C3) = 4 bytes.
	buf = append(buf, 0x7D, 0x04)
	// base: mov rax, rcx (48 89 C8); ret (C3)
	buf = append(buf, 0x48, 0x89, 0xC8, 0xC3)
	// rec:
	// push rcx (51)
	buf = append(buf, 0x51)
	// dec rcx (48 FF C9)
	buf = append(buf, 0x48, 0xFF, 0xC9)
	// mov rax, selfAddr (48 B8 imm64)
	buf = append(buf, 0x48, 0xB8)
	buf = binary.LittleEndian.AppendUint64(buf, uint64(selfAddr))
	// call rax (FF D0)
	buf = append(buf, 0xFF, 0xD0)
	// pop rcx (59)
	buf = append(buf, 0x59)
	// push rax (50)
	buf = append(buf, 0x50)
	// sub rcx, 2 (48 83 E9 02)
	buf = append(buf, 0x48, 0x83, 0xE9, 0x02)
	// mov rax, selfAddr (48 B8 imm64)
	buf = append(buf, 0x48, 0xB8)
	buf = binary.LittleEndian.AppendUint64(buf, uint64(selfAddr))
	// call rax (FF D0)
	buf = append(buf, 0xFF, 0xD0)
	// pop rdx (5A)
	buf = append(buf, 0x5A)
	// add rax, rdx (48 01 D0)
	buf = append(buf, 0x48, 0x01, 0xD0)
	// ret (C3)
	buf = append(buf, 0xC3)
	return buf
}

// BuildFibSegment mmaps a self-recursive fib segment. Two-pass: emit
// with a zero self-address to learn the byte length + the two imm64
// offsets, mmap RW, learn the real address, patch both imm64s, flip RX.
func BuildFibSegment() (*CodePage, error) {
	// Pass 1: emit with placeholder to find imm64 offsets.
	tmpl := EmitFibSegment(0)
	// The two imm64 fields follow the two `48 B8` (mov rax, imm64)
	// markers. Find them.
	var immOffs []int
	for i := 0; i+1 < len(tmpl); i++ {
		if tmpl[i] == 0x48 && tmpl[i+1] == 0xB8 {
			immOffs = append(immOffs, i+2)
			i += 9 // skip past the imm64
		}
	}
	page, err := MmapCodeRW(tmpl)
	if err != nil {
		return nil, err
	}
	addr := page.Addr()
	for _, off := range immOffs {
		binary.LittleEndian.PutUint64(page.mem[off:off+8], uint64(addr))
	}
	if err := page.FlipRX(); err != nil {
		_ = page.Munmap()
		return nil, err
	}
	return page, nil
}
