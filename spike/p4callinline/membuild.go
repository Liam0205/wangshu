//go:build linux && amd64

// mmap + emit helpers for the issue #50 spike. Mirrors
// spike/p4tramp/mmap_linux_amd64.go (same MmapCode protocol) plus the
// cross-segment emit shapes this spike needs.
package p4callinline

import (
	"encoding/binary"
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// CodePage is one W^X-flipped executable mmap segment.
type CodePage struct {
	mem    []byte
	length int
}

// Addr returns the segment start (the CALL target).
func (c *CodePage) Addr() uintptr {
	if c == nil || len(c.mem) == 0 {
		return 0
	}
	return memAddr(c.mem)
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

// MmapCodeRW allocates an RW page and copies code, leaving it writable
// so callers can patch baked addresses before flipping to RX via
// FlipRX. Used by the two-pass self-recursive segment builder.
func MmapCodeRW(code []byte) (*CodePage, error) {
	if len(code) == 0 {
		return nil, errors.New("p4callinline: empty code")
	}
	pageSize := unix.Getpagesize()
	length := ((len(code) + pageSize - 1) / pageSize) * pageSize
	mem, err := unix.Mmap(
		-1, 0, length,
		unix.PROT_READ|unix.PROT_WRITE,
		unix.MAP_ANON|unix.MAP_PRIVATE,
	)
	if err != nil {
		return nil, fmt.Errorf("p4callinline: mmap RW failed: %w", err)
	}
	copy(mem, code)
	return &CodePage{mem: mem, length: length}, nil
}

// FlipRX flips a page allocated by MmapCodeRW from RW to RX.
func (c *CodePage) FlipRX() error {
	if c == nil || c.mem == nil {
		return errors.New("p4callinline: nil page")
	}
	return unix.Mprotect(c.mem, unix.PROT_READ|unix.PROT_EXEC)
}

// MmapCode allocates RW, copies code, flips to RX (never RWX).
func MmapCode(code []byte) (*CodePage, error) {
	if len(code) == 0 {
		return nil, errors.New("p4callinline: empty code")
	}
	pageSize := unix.Getpagesize()
	length := ((len(code) + pageSize - 1) / pageSize) * pageSize
	mem, err := unix.Mmap(
		-1, 0, length,
		unix.PROT_READ|unix.PROT_WRITE,
		unix.MAP_ANON|unix.MAP_PRIVATE,
	)
	if err != nil {
		return nil, fmt.Errorf("p4callinline: mmap RW failed: %w", err)
	}
	copy(mem, code)
	if err := unix.Mprotect(mem, unix.PROT_READ|unix.PROT_EXEC); err != nil {
		_ = unix.Munmap(mem)
		return nil, fmt.Errorf("p4callinline: mprotect RX failed: %w", err)
	}
	return &CodePage{mem: mem, length: length}, nil
}

// EmitLeafAddImm emits a callee segment:
//
//	48 b8 <imm64>   mov rax, imm64
//	48 01 c8        add rax, rcx      ; rax = imm + rcx (caller passes rcx)
//	c3              ret
//
// The callee both computes (proves execution) and returns via plain
// ret — the return address was pushed by the caller segment's CALL,
// exactly the cross-segment shape EmitCallInline will emit.
func EmitLeafAddImm(imm uint64) []byte {
	buf := make([]byte, 0, 16)
	buf = append(buf, 0x48, 0xb8)
	buf = binary.LittleEndian.AppendUint64(buf, imm)
	buf = append(buf, 0x48, 0x01, 0xC8) // add rax, rcx
	buf = append(buf, 0xC3)
	return buf
}

// EmitCallerToCallee emits a caller segment that CALLs calleeAddr
// (baked imm64) and returns rax+1:
//
//	48 b9 <imm64>   mov rcx, 7           ; arg via rcx
//	48 b8 <addr>    mov rax, calleeAddr
//	ff d0           call rax             ; cross-page CALL
//	48 ff c0        inc rax              ; prove control returned here
//	c3              ret
func EmitCallerToCallee(calleeAddr uintptr, arg uint64) []byte {
	buf := make([]byte, 0, 32)
	buf = append(buf, 0x48, 0xb9)
	buf = binary.LittleEndian.AppendUint64(buf, arg)
	buf = append(buf, 0x48, 0xb8)
	buf = binary.LittleEndian.AppendUint64(buf, uint64(calleeAddr))
	buf = append(buf, 0xFF, 0xD0) // call rax
	buf = append(buf, 0x48, 0xFF, 0xC0)
	buf = append(buf, 0xC3)
	return buf
}

// EmitCallerLoop emits a caller that CALLs calleeAddr n times in a
// tight loop (per-call cost measurement, amortizing the trampoline):
//
//	48 b9 <n>        mov rcx, n
//	48 b8 <addr>     mov rax, calleeAddr
//	; loop:
//	50               push rax             ; save callee addr
//	51               push rcx             ; save counter
//	ff d0            call rax
//	59               pop rcx
//	58               pop rax
//	48 ff c9         dec rcx
//	75 f4            jnz loop (-12)
//	c3               ret
func EmitCallerLoop(calleeAddr uintptr, n uint64) []byte {
	buf := make([]byte, 0, 40)
	buf = append(buf, 0x48, 0xb9)
	buf = binary.LittleEndian.AppendUint64(buf, n)
	buf = append(buf, 0x48, 0xb8)
	buf = binary.LittleEndian.AppendUint64(buf, uint64(calleeAddr))
	loopStart := len(buf)
	buf = append(buf, 0x50)             // push rax
	buf = append(buf, 0x51)             // push rcx
	buf = append(buf, 0xFF, 0xD0)       // call rax
	buf = append(buf, 0x59)             // pop rcx
	buf = append(buf, 0x58)             // pop rax
	buf = append(buf, 0x48, 0xFF, 0xC9) // dec rcx
	rel := loopStart - (len(buf) + 2)
	buf = append(buf, 0x75, byte(int8(rel))) // jnz loop
	buf = append(buf, 0xC3)
	return buf
}

// EmitLeafRet emits the minimal callee: just `ret` (1 byte). Baseline
// for the pure CALL+RET cost.
func EmitLeafRet() []byte { return []byte{0xC3} }

// EmitFrameWriteCallee emits a callee that emulates the enterLuaFrame
// hot core as raw stores (the S-B shape). rcx = CI slot byte address,
// rdx = ciDepth word byte address, r8 = top word byte address:
//
//	; write 5 CI words (base|funcIdx, top|pc, word2, cl, nVarargs)
//	48 b8 <w0>      mov rax, w0
//	48 89 01        mov [rcx], rax
//	48 b8 <w1>      mov rax, w1
//	48 89 41 08     mov [rcx+8], rax
//	48 b8 <w2>      mov rax, w2
//	48 89 41 10     mov [rcx+16], rax
//	48 b8 <w3>      mov rax, w3
//	48 89 41 18     mov [rcx+24], rax
//	48 b8 <w4>      mov rax, w4
//	48 89 41 20     mov [rcx+32], rax
//	48 ff 02        inc qword [rdx]     ; ciDepth++
//	49 c7 00 <top>  mov qword [r8], top ; set top (imm32)
//	48 ff 0a        dec qword [rdx]     ; ciDepth-- (teardown half)
//	c3              ret
func EmitFrameWriteCallee(w [5]uint64, top uint32) []byte {
	buf := make([]byte, 0, 96)
	disp := []byte{0x00, 0x08, 0x10, 0x18, 0x20}
	for i, word := range w {
		buf = append(buf, 0x48, 0xb8)
		buf = binary.LittleEndian.AppendUint64(buf, word)
		if i == 0 {
			buf = append(buf, 0x48, 0x89, 0x01) // mov [rcx], rax
		} else {
			buf = append(buf, 0x48, 0x89, 0x41, disp[i]) // mov [rcx+disp8], rax
		}
	}
	buf = append(buf, 0x48, 0xFF, 0x02) // inc qword [rdx]
	// mov qword [r8], imm32 (sign-extended): 49 C7 00 imm32
	buf = append(buf, 0x49, 0xC7, 0x00)
	buf = binary.LittleEndian.AppendUint32(buf, top)
	buf = append(buf, 0x48, 0xFF, 0x0A) // dec qword [rdx]
	buf = append(buf, 0xC3)
	return buf
}
