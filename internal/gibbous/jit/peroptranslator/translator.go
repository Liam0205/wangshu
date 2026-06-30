//go:build wangshu_p4 && amd64

// translator.go — spike v0 of the per-opcode translator (PJ10).
//
// Takes a *bytecode.Proto whose Code is the single-BB straight-line shape
// `LOADK A, K(Bx); RETURN A 1` (e.g. compiled from Lua `return 42`) and
// emits the equivalent amd64 byte sequence into a code buffer. The buffer
// is then mmap'd via amd64.MmapCode and called via amd64.CallJIT.
//
// Scope limits (spike v0):
//   - only LOADK + RETURN A 1 (one constant, returned as the single result)
//   - K(Bx) must be a number constant; string constants are out of scope
//     (P3 wasm does the same — SupportsAllOpcodes rejects them, see
//     internal/gibbous/wasm/compiler.go)
//   - no host helper calls, no value stack writes — the spike only proves
//     "Proto bytecode in, RAX out". A real RETURN does much more (write
//     R(A)..R(A+B-2) back to the host's value stack and let the host run
//     DoReturn) — that arrives in spike v2.
//
// What this validates:
//   - The translator can accept an actual *bytecode.Proto produced by the
//     wangshu frontend (not hand-written bytes).
//   - The emitted byte sequence is executable as straight machine code
//     under the existing W^X plumbing (PJ1's amd64.MmapCode + CallJIT).
//   - The encoded constant survives the round trip bit-for-bit (no NaN
//     normalization drift, no endianness slip).
package peroptranslator

import (
	"fmt"

	"github.com/Liam0205/wangshu/internal/bytecode"
	jitamd64 "github.com/Liam0205/wangshu/internal/gibbous/jit/amd64"
)

// CompiledSpike is what TranslateSpike returns: an mmap'd code page that
// CallSpike can invoke.
type CompiledSpike struct {
	page *jitamd64.CodePage
}

// Addr exposes the entry point (mostly for diagnostics; CallSpike uses it
// internally).
func (c *CompiledSpike) Addr() uintptr {
	if c == nil || c.page == nil {
		return 0
	}
	return c.page.Addr()
}

// Dispose releases the mmap segment.
func (c *CompiledSpike) Dispose() error {
	if c == nil || c.page == nil {
		return nil
	}
	err := c.page.Munmap()
	c.page = nil
	return err
}

// CallSpike invokes the compiled stub via the PJ1 CallJIT trampoline.
// Returns the raw uint64 from RAX.
func (c *CompiledSpike) Call() uint64 {
	return jitamd64.CallJIT(c.page.Addr())
}

// TranslateSpike walks proto.Code one op at a time, emits the equivalent
// amd64 bytes, then mmap's the result. Returns a CompiledSpike whose Call
// returns the constant K(Bx) verbatim (NaN-boxed, bit-for-bit).
//
// Constraints enforced (return error otherwise):
//   - len(proto.Code) == 2
//   - proto.Code[0] is LOADK with A=0
//   - proto.Code[1] is RETURN with A=0 and B=2 (one return value, the
//     contents of R(0))
//   - K(Bx) is a number (Kind == bytecode.KindNumber)
//
// These constraints are exactly the spike v0 supported shape. SupportsAll-
// Opcodes wiring lands in spike v2; here the caller has to know it's
// supplying the supported shape.
func TranslateSpike(proto *bytecode.Proto) (*CompiledSpike, error) {
	if proto == nil {
		return nil, fmt.Errorf("peroptranslator: nil proto")
	}
	if len(proto.Code) != 2 {
		return nil, fmt.Errorf("peroptranslator: spike v0 requires Code length 2, got %d", len(proto.Code))
	}
	ins0 := proto.Code[0]
	if op := bytecode.Op(ins0); op != bytecode.LOADK {
		return nil, fmt.Errorf("peroptranslator: spike v0 expects LOADK at pc=0, got %s", op)
	}
	if a := bytecode.A(ins0); a != 0 {
		return nil, fmt.Errorf("peroptranslator: spike v0 expects LOADK A=0, got A=%d", a)
	}
	bx := bytecode.Bx(ins0)
	if bx < 0 || bx >= len(proto.Consts) {
		return nil, fmt.Errorf("peroptranslator: LOADK Bx=%d out of Consts range [0,%d)", bx, len(proto.Consts))
	}
	if proto.IsStringConst(bx) {
		return nil, fmt.Errorf("peroptranslator: spike v0 does not support string LOADK (Bx=%d)", bx)
	}
	imm := loadKImm64(proto, bx)

	ins1 := proto.Code[1]
	if op := bytecode.Op(ins1); op != bytecode.RETURN {
		return nil, fmt.Errorf("peroptranslator: spike v0 expects RETURN at pc=1, got %s", op)
	}
	if a := bytecode.A(ins1); a != 0 {
		return nil, fmt.Errorf("peroptranslator: spike v0 expects RETURN A=0, got A=%d", a)
	}
	if b := bytecode.B(ins1); b != 2 {
		return nil, fmt.Errorf("peroptranslator: spike v0 expects RETURN B=2 (one retval), got B=%d", b)
	}

	// Two-instruction prologue: `mov rax, imm64; ret`. No frame, no callee-
	// saved touches — matches PJ1 callJIT's NOSPLIT|NOFRAME shape.
	var buf []byte
	buf = jitamd64.EmitMovRaxImm64(buf, imm)
	buf = jitamd64.EmitRet(buf)

	page, err := jitamd64.MmapCode(buf)
	if err != nil {
		return nil, fmt.Errorf("peroptranslator: mmap %d bytes: %w", len(buf), err)
	}
	return &CompiledSpike{page: page}, nil
}

// loadKImm64 extracts the NaN-boxed u64 representation of proto.Consts[bx].
// Only number constants are supported in spike v0 (caller guards via the
// IsStringConst check above).
//
// proto.Consts stores constants as value.Value (which is a uint64 — the
// NaN-boxed bit pattern). For number constants, that bit pattern is exactly
// what we want to ship into RAX, so the conversion is a no-op cast.
func loadKImm64(proto *bytecode.Proto, bx int) uint64 {
	return uint64(proto.Consts[bx])
}
