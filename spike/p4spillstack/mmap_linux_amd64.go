//go:build linux && amd64

package p4spillstack

import (
	"encoding/binary"
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"
)

// CodePage is one mmap'd executable segment (after W^X flip), held until
// Munmap. Mirrors spike/p4tramp.CodePage.
type CodePage struct {
	mem    []byte
	length int
}

// Addr returns the segment start as a uintptr (the CALL target).
func (c *CodePage) Addr() uintptr {
	if c == nil || len(c.mem) == 0 {
		return 0
	}
	return uintptr(unsafe.Pointer(&c.mem[0]))
}

// Munmap releases the segment.
func (c *CodePage) Munmap() error {
	if c == nil || c.mem == nil {
		return nil
	}
	mem := c.mem
	c.mem = nil
	c.length = 0
	return unix.Munmap(mem)
}

// MmapCode allocates RW memory, copies code, flips to RX, and returns it.
// Same protocol as spike/p4tramp.MmapCode (05 §2.1).
func MmapCode(code []byte) (*CodePage, error) {
	if len(code) == 0 {
		return nil, errors.New("p4spillstack: empty code")
	}
	pageSize := unix.Getpagesize()
	length := ((len(code) + pageSize - 1) / pageSize) * pageSize

	mem, err := unix.Mmap(
		-1, 0, length,
		unix.PROT_READ|unix.PROT_WRITE,
		unix.MAP_ANON|unix.MAP_PRIVATE,
	)
	if err != nil {
		return nil, fmt.Errorf("p4spillstack: mmap RW failed: %w", err)
	}
	copy(mem, code)
	if err := unix.Mprotect(mem, unix.PROT_READ|unix.PROT_EXEC); err != nil {
		_ = unix.Munmap(mem)
		return nil, fmt.Errorf("p4spillstack: mprotect RX failed: %w", err)
	}
	return &CodePage{mem: mem, length: length}, nil
}

// emitU32 appends a little-endian uint32.
func emitU32(buf []byte, v uint32) []byte {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	return append(buf, b[:]...)
}

// EmitDescendSegment emits a segment that models seg2seg deep recursion:
// it descends `levels` frames, at each level doing `sub rsp, 32` and
// writing a per-level canary word onto the stack (the byte cost per level
// matches the 32 B/level of the real arm64 seg2seg path; amd64's real
// path is ~24 B but 32 keeps the model conservative), then unwinds back
// up verifying each canary, and returns the number of levels whose canary
// survived in rax.
//
// Register use (leaf-style, no Go ABI): rax = surviving count, rcx = loop
// counter, rdx = canary value. The segment is entered with SP already
// pointing at the spill stack (the trampoline switched it). It never calls
// a Go helper, so it is NOSPLIT-safe by construction.
//
// The emitted body, in pseudo-asm:
//
//	xor  rax, rax                 ; surviving = 0
//	mov  rcx, levels              ; counter
//	descend:
//	  sub  rsp, 32                ; carve a frame (32 B/level)
//	  mov  rdx, rcx               ; canary = remaining level index
//	  mov  [rsp], rdx             ; store canary
//	  dec  rcx
//	  jnz  descend
//	mov  rcx, levels              ; unwind counter
//	ascend:
//	  mov  rdx, [rsp]             ; read canary back
//	  cmp  rdx, rcx               ; expected == current level index
//	  jne  skip                   ; corrupted -> don't count
//	  inc  rax                    ; survived
//	skip:
//	  add  rsp, 32                ; pop the frame
//	  dec  rcx
//	  jnz  ascend
//	ret
//
// If SP was switched onto the spill stack, the `sub rsp` descent burns
// that buffer; if the switch failed or the buffer is too small, the
// canaries get corrupted (or the process crashes) and rax != levels.
func EmitDescendSegment(levels uint32) []byte {
	var buf []byte
	// xor rax, rax
	buf = append(buf, 0x48, 0x31, 0xc0)
	// mov rcx, imm32 (zero-extended): 48 c7 c1 imm32
	buf = append(buf, 0x48, 0xc7, 0xc1)
	buf = emitU32(buf, levels)

	descend := len(buf)
	// sub rsp, 32: 48 83 ec 20
	buf = append(buf, 0x48, 0x83, 0xec, 0x20)
	// mov rdx, rcx: 48 89 ca
	buf = append(buf, 0x48, 0x89, 0xca)
	// mov [rsp], rdx: 48 89 14 24
	buf = append(buf, 0x48, 0x89, 0x14, 0x24)
	// dec rcx: 48 ff c9
	buf = append(buf, 0x48, 0xff, 0xc9)
	// jnz descend: 0f 85 rel32
	buf = append(buf, 0x0f, 0x85)
	rel := int32(descend - (len(buf) + 4))
	buf = emitU32(buf, uint32(rel))

	// mov rcx, levels again  (unwind loop counter)
	buf = append(buf, 0x48, 0xc7, 0xc1)
	buf = emitU32(buf, levels)
	// mov rsi, 1  (expected canary, ascends 1..levels as we pop)
	buf = append(buf, 0x48, 0xc7, 0xc6)
	buf = emitU32(buf, 1)

	ascend := len(buf)
	// mov rdx, [rsp]: 48 8b 14 24
	buf = append(buf, 0x48, 0x8b, 0x14, 0x24)
	// cmp rdx, rsi: 48 39 f2  (compare canary against expected)
	buf = append(buf, 0x48, 0x39, 0xf2)
	// jne skip (short): 75 03  (skip over `inc rax`, 3 bytes)
	buf = append(buf, 0x75, 0x03)
	// inc rax: 48 ff c0
	buf = append(buf, 0x48, 0xff, 0xc0)
	// skip:
	// add rsp, 32: 48 83 c4 20
	buf = append(buf, 0x48, 0x83, 0xc4, 0x20)
	// inc rsi: 48 ff c6
	buf = append(buf, 0x48, 0xff, 0xc6)
	// dec rcx: 48 ff c9
	buf = append(buf, 0x48, 0xff, 0xc9)
	// jnz ascend: 0f 85 rel32
	buf = append(buf, 0x0f, 0x85)
	rel = int32(ascend - (len(buf) + 4))
	buf = emitU32(buf, uint32(rel))

	// ret
	buf = append(buf, 0xc3)
	return buf
}
