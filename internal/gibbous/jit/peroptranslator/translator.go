//go:build wangshu_p4 && amd64

// translator.go — spike v0/v1 of the per-opcode translator (PJ10).
//
// Takes a *bytecode.Proto whose Code is the single-BB single-result shape
//
//	<head op produces a single Value in R(A=0)>
//	RETURN A=0 B=2
//
// and emits the equivalent amd64 byte sequence into a code buffer. The
// buffer is then mmap'd via amd64.MmapCode and called via amd64.CallJIT.
//
// Spike v1 supported head ops (R(A) := compile-time constant Value):
//   - LOADK    A=0 Bx=<num const idx>   (number constants only)
//   - LOADBOOL A=0 B=<0|1> C=0          (no skip — C=0 stays single-BB)
//   - LOADNIL  A=0 B=0                  (single-slot fill, R(A) := nil)
//
// All three are isomorphic in the spike's slice of the world: the head op
// names a NaN-boxed u64 Value, and the RETURN forwards it as the single
// result via RAX. The whole translation degenerates to "mov rax, imm64;
// ret", and Spike v1 only changes how `imm` is computed per head op.
//
// Out of scope (spike v0+v1):
//   - MOVE / GETUPVAL — those need R14=vsBase ABI and host helpers; v2.
//   - LOADBOOL C != 0 — splits the BB (skip semantics); v2 once we have
//     a label resolver.
//   - String LOADK   — proto.Consts[Bx] for strings is a nil-tagged
//     placeholder until the State lazily interns it; v2 will route those
//     through a host helper, same as P3 wasm does (see internal/gibbous/
//     wasm/compiler.go SupportsAllOpcodes string-const reject).
//   - value-stack write-back — real RETURN writes R(A)..R(A+B-2) back to
//     the host's value stack via R14=vsBase + DoReturn; the spike still
//     returns the single Value bit pattern in RAX.
//   - SupportsAllOpcodes wiring — the spike is invoked from its own tests.
//
// What this validates:
//   - The translator accepts an actual *bytecode.Proto produced by the
//     wangshu frontend (not hand-written bytes).
//   - Multiple head op encodings (number / bool / nil) all round-trip
//     bit-for-bit through mmap + CALL.
//   - The existing W^X plumbing (PJ1's amd64.MmapCode + CallJIT) is
//     reusable from PJ10 without modification.
package peroptranslator

import (
	"fmt"

	"github.com/Liam0205/wangshu/internal/bytecode"
	jitamd64 "github.com/Liam0205/wangshu/internal/gibbous/jit/amd64"
	"github.com/Liam0205/wangshu/internal/value"
)

// CompiledSpike is what TranslateSpike returns: an mmap'd code page that
// Call() can invoke.
type CompiledSpike struct {
	page *jitamd64.CodePage
}

// Addr exposes the entry point (mostly for diagnostics; Call uses it
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

// Call invokes the compiled stub via the PJ1 CallJIT trampoline. Returns
// the raw uint64 from RAX (the NaN-boxed Value of the single return).
func (c *CompiledSpike) Call() uint64 {
	return jitamd64.CallJIT(c.page.Addr())
}

// TranslateSpike walks proto.Code, recognises the supported head + RETURN
// shape, computes the compile-time imm64, and emits the two-instruction
// stub `mov rax, imm64; ret`. Returns a CompiledSpike whose Call returns
// the Value bit pattern verbatim.
//
// Returns an error if the Proto shape is outside the spike v1 supported
// set (see package doc for the list). SupportsAllOpcodes wiring lands in
// spike v2; here the caller has to know it's supplying the supported shape.
func TranslateSpike(proto *bytecode.Proto) (*CompiledSpike, error) {
	if proto == nil {
		return nil, fmt.Errorf("peroptranslator: nil proto")
	}
	if len(proto.Code) != 2 {
		return nil, fmt.Errorf("peroptranslator: spike requires Code length 2, got %d", len(proto.Code))
	}

	imm, err := headOpImm64(proto, proto.Code[0])
	if err != nil {
		return nil, err
	}
	if err := checkSingleReturn(proto.Code[1]); err != nil {
		return nil, err
	}

	// Two-instruction stub: `mov rax, imm64; ret`. No frame, no callee-
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

// headOpImm64 recognises the supported single-Value-producer head ops and
// computes the NaN-boxed u64 that R(A=0) would hold after the op runs.
//
// Returns an error if the op is unsupported or its operands fall outside
// the spike's supported subset.
func headOpImm64(proto *bytecode.Proto, ins bytecode.Instruction) (uint64, error) {
	op := bytecode.Op(ins)
	if a := bytecode.A(ins); a != 0 {
		return 0, fmt.Errorf("peroptranslator: spike expects head op A=0, got %s A=%d", op, a)
	}
	switch op {
	case bytecode.LOADK:
		bx := bytecode.Bx(ins)
		if bx < 0 || bx >= len(proto.Consts) {
			return 0, fmt.Errorf("peroptranslator: LOADK Bx=%d out of Consts range [0,%d)", bx, len(proto.Consts))
		}
		if proto.IsStringConst(bx) {
			return 0, fmt.Errorf("peroptranslator: spike does not support string LOADK (Bx=%d)", bx)
		}
		return uint64(proto.Consts[bx]), nil

	case bytecode.LOADBOOL:
		if c := bytecode.C(ins); c != 0 {
			return 0, fmt.Errorf("peroptranslator: spike does not support LOADBOOL C!=0 (skip semantics splits the BB), got C=%d", c)
		}
		b := bytecode.B(ins)
		return uint64(value.BoolValue(b != 0)), nil

	case bytecode.LOADNIL:
		// LOADNIL A B fills R(A..B) with nil. In single-BB / single-slot
		// shape we want B == A, i.e. exactly one slot. Frontend emits
		// LOADNIL A=0 B=0 for `local x; x = nil` / `return nil` in the
		// kernel — we enforce B == 0 (== A) here.
		if b := bytecode.B(ins); b != 0 {
			return 0, fmt.Errorf("peroptranslator: spike supports LOADNIL B=0 only (single-slot), got B=%d", b)
		}
		return uint64(value.Nil), nil

	default:
		return 0, fmt.Errorf("peroptranslator: unsupported head op %s", op)
	}
}

// checkSingleReturn enforces the spike's RETURN shape: A=0, B=2 (one
// return value, R(0)).
func checkSingleReturn(ins bytecode.Instruction) error {
	if op := bytecode.Op(ins); op != bytecode.RETURN {
		return fmt.Errorf("peroptranslator: spike expects RETURN at pc=1, got %s", op)
	}
	if a := bytecode.A(ins); a != 0 {
		return fmt.Errorf("peroptranslator: spike expects RETURN A=0, got A=%d", a)
	}
	if b := bytecode.B(ins); b != 2 {
		return fmt.Errorf("peroptranslator: spike expects RETURN B=2 (one retval), got B=%d", b)
	}
	return nil
}
