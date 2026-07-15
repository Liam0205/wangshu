//go:build linux && amd64

// Gates ①②③: exec mmap + W^X flip + Go → mmap segment → ret back to Go, the
// minimal round-trip.
//
// Design:
//   - No switch to a self-managed stack, no jitContext setup (that belongs to
//     the full PJ1 trampoline; this spike only verifies "the mmap segment can
//     run + ret returns to Go without stepping on the runtime");
//   - Emit the "mov rax, IMM64; ret" 9-byte sequence (48 b8 IMM64-LE c3); Go
//     jumps in via a `runtime/cgo`-free trampoline to fetch the IMM64 value;
//   - trampoline asm stub: `callJIT(codeAddr, scratch *[8]uint64) uint64`
//     — treats codeAddr solely as a CALL target (equivalent to the `JMP AX`
//     pattern inside the go runtime asm); scratch is reserved for PJ1 to set up
//     jitContext later.
//
// Gate red/green: ① mmap RW + mprotect RX without error; ② callJIT does not
// SEGV; ③ return value == IMM64 input — all three passing means the spike is
// green, clearing the way to commit to the PJ1 emitter trait.
package p4tramp

import (
	"encoding/binary"
	"errors"
	"fmt"
	"runtime"

	"golang.org/x/sys/unix"
)

// CodePage is a mmap'd executable segment (after the W^X flip), held until Munmap.
//
// Corresponds to docs/design/p4-method-jit/05-system-pipeline.md §2.1 exec mmap
// protocol; the full PJ1 version will introduce codePagePool (per-Proto segment +
// release policy), this spike only does a single segment.
type CodePage struct {
	// addr is the mmap segment start (unsafe-equivalent uintptr, but the spike
	// uses a []byte view instead — avoiding an unsafe dependency keeps the spike
	// gate simpler).
	mem []byte
	// length is the number of bytes allocated (>= len(code)).
	length int
}

// Addr returns the segment start as a uintptr — the CALL target address.
func (c *CodePage) Addr() uintptr {
	if c == nil || len(c.mem) == 0 {
		return 0
	}
	// &c.mem[0] is the first address of the []byte backing array, a mmap'd
	// page-aligned stable pointer — the physical source of gate ① (after
	// mprotect, this block is the executable segment start).
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

// MmapCode allocates a W+X segment, writes code, and returns it after the W^X
// flip (per the 05 §2.1 protocol).
//
// Flow (matching wazero internal/engine/wazevo, but self-implemented for P4):
//  1. unix.Mmap MAP_ANON|MAP_PRIVATE PROT_READ|PROT_WRITE alloc;
//  2. copy code into the segment;
//  3. unix.Mprotect PROT_READ|PROT_EXEC flip (W^X, never holds RWX at any point);
//  4. (linux/amd64) hardware guarantees icache coherency, no explicit flush;
//
// On failure, cleans up the already-allocated segment (Munmap).
func MmapCode(code []byte) (*CodePage, error) {
	if len(code) == 0 {
		return nil, errors.New("p4tramp: empty code")
	}
	// page alignment (linux 4 KiB, independent of the actual code segment length
	// — mmap only accepts page granularity)
	pageSize := unix.Getpagesize()
	length := ((len(code) + pageSize - 1) / pageSize) * pageSize

	// step 1: mmap RW
	mem, err := unix.Mmap(
		-1, 0, length,
		unix.PROT_READ|unix.PROT_WRITE,
		unix.MAP_ANON|unix.MAP_PRIVATE,
	)
	if err != nil {
		return nil, fmt.Errorf("p4tramp: mmap RW failed: %w", err)
	}

	// step 2: write code
	copy(mem, code)

	// step 3: mprotect RX (W^X flip; never holds RWX)
	if err := unix.Mprotect(mem, unix.PROT_READ|unix.PROT_EXEC); err != nil {
		_ = unix.Munmap(mem)
		return nil, fmt.Errorf("p4tramp: mprotect RX failed: %w", err)
	}

	// step 4: linux/amd64 hardware icache coherency (no-op); on arm64 insert the
	// IC IVAU/DC CVAU sequence here (deferred to the PJ8 darwin/arm64 spike + the
	// 06 §2.3 protocol).
	_ = runtime.GOOS

	return &CodePage{mem: mem, length: length}, nil
}

// EmitMovRaxImm64Ret emits the "mov rax, imm64; ret" 9-byte sequence.
//
// amd64 encoding (intel manual §B.1):
//
//	48 b8 ii ii ii ii ii ii ii ii   ; REX.W + B8+rd  → mov rax, imm64
//	c3                                ; ret
//
// Fixed 9 bytes (no variable-length prefix) — minimizing the physical form of
// the LOADK template.
func EmitMovRaxImm64Ret(imm uint64) []byte {
	buf := make([]byte, 11)
	buf[0] = 0x48 // REX.W
	buf[1] = 0xb8 // mov rax, imm64
	binary.LittleEndian.PutUint64(buf[2:10], imm)
	buf[10] = 0xc3 // ret
	return buf
}
