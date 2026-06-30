//go:build wangshu_p4 && amd64

// translator.go — per-opcode translator (PJ10).
//
// Takes a *bytecode.Proto whose Code is the single-BB "N constants + one
// RETURN" shape and emits the equivalent amd64 byte sequence. The emitted
// stub does no real work itself — it just hands a "no exit" status back
// in RAX; the actual R(A)..R(A+N-1) writes are done by PerOpCode.Run via
// host.SetReg, which loads the imm64 values cached at Compile time.
//
// Supported shape (call this the "constant tuple" shape):
//
//	<N head ops, each LOADK/LOADBOOL/LOADNIL writing R(A+i)>  ; N >= 1
//	RETURN A B                                                 ; B-1 == N
//	[optional trailing RETURN A=0 B=1]                         ; dead code
//
// Per head op (R(A+i) := compile-time constant Value):
//   - LOADK    A=startA+i Bx=<num const idx>   (number constants only)
//   - LOADBOOL A=startA+i B=<0|1> C=0          (no skip — C=0 stays single-BB)
//   - LOADNIL  A=startA+i B=startA+i           (single-slot fill, R(A+i) := nil)
//
// This is what PJ10 buys over PJ0-PJ9: PJ7's analyzeShape only matches
// "head op + RETURN A 1" (single return value). The peroptranslator
// happily accepts N constants returned together (N >= 1), so e.g.
//
//	local function k() return 42, 43, 44 end
//
// promotes through PJ10 even though PJ7's analyzeShape says "unsupported
// shape" (verified by the V15b heavy-bench / promotion-probe scaffolding).
//
// Out of scope (yet):
//   - MOVE / GETUPVAL — those need R14=vsBase ABI and host helpers.
//   - LOADBOOL C != 0 — splits the BB (skip semantics); needs a label
//     resolver.
//   - String LOADK   — proto.Consts[Bx] for strings is a nil-tagged
//     placeholder until the State lazily interns it; would need a host
//     helper to fetch the real GCRef.
//   - Arithmetic / control flow / table ops / CALL — these are the
//     PJ10b/c/d sub-stages.
//
// What this validates:
//   - The translator accepts an actual *bytecode.Proto produced by the
//     wangshu frontend and produces a bridge.GibbousCode the bridge can
//     install.
//   - Multiple return values round-trip bit-for-bit through SetReg +
//     DoReturn — a path PJ7 cannot exercise.
//   - The existing W^X plumbing (PJ1's amd64.MmapCode + CallJITFull) is
//     reusable from PJ10 without modification.
package peroptranslator

import (
	"fmt"
	"unsafe"

	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/gibbous/jit"
	jitamd64 "github.com/Liam0205/wangshu/internal/gibbous/jit/amd64"
	"github.com/Liam0205/wangshu/internal/value"
)

// shapeInfo is what AnalyzeShape returns when it recognises a supported
// Proto. The PerOpCode builder uses these fields to bake in the per-slot
// source descriptor list, optional pre-return side-effect ops, and the
// RETURN's A/B fields.
type shapeInfo struct {
	ok          bool
	sources     []slotSource // one entry per return slot, in slot order
	sideEffects []sideEffect // ops with no return-slot output (SETUPVAL); run before head ops
	startA      uint8        // R(startA) is the first slot written
	retA        uint8        // RETURN A — matches startA in the typical shape
	retB        uint8        // RETURN B — len(sources)+1 (or 1 for the setter form)
	retPC       uint8        // RETURN's pc index in proto.Code
}

// sideEffect describes a pre-return op that has no associated return slot.
// Today the supported kinds are SETUPVAL (U(b) := R(a)) and LOADNIL (fill
// scratch regs); future PJ10a extensions may add others (e.g. SETGLOBAL
// once PJ10d arrives).
type sideEffect struct {
	kind sideEffectKind
	a    uint8 // op's A field (source register for SETUPVAL; first slot for LOADNIL)
	b    uint8 // op's B field (upvalue index for SETUPVAL; last slot for LOADNIL)
}

// sideEffectKind discriminates the pre-return op kinds.
type sideEffectKind uint8

const (
	sideEffectSetUpval sideEffectKind = iota // SETUPVAL A B: U(B) := R(A)
	sideEffectLoadNil                        // LOADNIL A B: R(A..B) := nil (scratch slots only)
)

// slotSource describes how PerOpCode.Run materialises one return slot:
// either a compile-time constant baked at Compile time, a runtime
// copy from another register, an upvalue read, or an arithmetic op
// run through host.Arith.
type slotSource struct {
	kind slotKind
	imm  uint64 // valid when kind == slotKindConst

	// reg/upval/upval are repurposed across kinds for compactness; see
	// each slotKind branch in PerOpCode.Run for the actual semantics.
	reg   uint8 // slotKindReg: source register
	upval uint8 // slotKindUpval: upvalue index B

	// Arithmetic ops (slotKindArith): op + B + C carry RK-encoded
	// operand identifiers, exactly as in the source bytecode.
	arithOp uint8  // bytecode opcode value (ADD/SUB/MUL/DIV/MOD/POW)
	arithB  uint16 // RK-encoded operand 1 (0-511; >=256 means K(B-256))
	arithC  uint16 // RK-encoded operand 2
	arithPC uint8  // pc index of the arithmetic instruction (for error reporting)
}

// slotKind discriminates how the source value is obtained.
type slotKind uint8

const (
	slotKindConst slotKind = iota // immediate (LOADK/LOADBOOL/LOADNIL)
	slotKindReg                   // copy from R(reg) (MOVE)
	slotKindUpval                 // read upvalue B (GETUPVAL)
	slotKindArith                 // arithmetic via host.Arith (ADD/SUB/MUL/DIV/MOD/POW)
	slotKindUnm                   // unary minus via host.Unm (UNM)
	slotKindLen                   // length op via host.Len (LEN)
	slotKindNot                   // logical not via Go-side Truthy/BoolValue (NOT)
)

// AnalyzeShape reports whether the given Proto matches the constant-tuple
// shape this spike supports. Returns shapeInfo{ok: true, ...} on a match.
//
// Naming: capitalised so the bridge wiring in jit/compiler.go can call it
// from outside the package while keeping the rest of the spike internal.
func AnalyzeShape(proto *bytecode.Proto) shapeInfo {
	if proto == nil || len(proto.Code) < 2 {
		return shapeInfo{}
	}
	// Find the RETURN instruction. The frontend emits either:
	//   [head1, head2, ..., headN, RETURN A B]            (N >= 1, B-1 == N)
	//   [head1, head2, ..., headN, RETURN A B, RETURN A=0 B=1]  (dead trailing)
	//   [side_effect..., RETURN A=0 B=1]                  (setter form, no returns)
	// We accept all. Pre-RETURN ops are split into return-slot writers
	// (head ops) and pre-return side-effect ops (SETUPVAL etc.).
	retPC := -1
	for pc, ins := range proto.Code {
		if bytecode.Op(ins) == bytecode.RETURN {
			retPC = pc
			break
		}
	}
	if retPC < 1 {
		return shapeInfo{}
	}
	retIns := proto.Code[retPC]
	retA := bytecode.A(retIns)
	retB := bytecode.B(retIns)
	if retB < 1 {
		return shapeInfo{} // B=0 (multret) out of scope for PJ10a.
	}
	n := retB - 1 // number of return values (0 for setter form)

	// Optional trailing RETURN A=0 B=1 must be the last instruction if
	// present; nothing else after retPC.
	if retPC+1 < len(proto.Code) {
		if retPC+1 != len(proto.Code)-1 {
			return shapeInfo{}
		}
		trailing := proto.Code[retPC+1]
		if bytecode.Op(trailing) != bytecode.RETURN || bytecode.A(trailing) != 0 || bytecode.B(trailing) != 1 {
			return shapeInfo{}
		}
	}

	// Walk the ops before RETURN. Each one is either a head op writing
	// R(retA + headIdx) for the next expected slot, or a side-effect op
	// that doesn't touch any return slot.
	var (
		sources     []slotSource
		sideEffects []sideEffect
		headIdx     = 0
	)
	if n > 0 {
		sources = make([]slotSource, n)
	}
	for pc := 0; pc < retPC; pc++ {
		ins := proto.Code[pc]
		op := bytecode.Op(ins)

		// Side-effect ops first — these never touch a return slot.
		if se, ok := sideEffectFromIns(op, ins); ok {
			sideEffects = append(sideEffects, se)
			continue
		}

		// LOADNIL A B fills R(A..B) inclusive. If A matches the next
		// expected return slot (retA + headIdx), expand it into per-slot
		// head sources; otherwise treat it as a scratch-register side
		// effect (the MOVEs that follow will copy the nil into the
		// return window).
		if op == bytecode.LOADNIL {
			a, b := bytecode.A(ins), bytecode.B(ins)
			if b < a || a < 0 || b > 255 {
				return shapeInfo{}
			}
			if headIdx < n && a == retA+headIdx {
				fillCount := b - a + 1
				if headIdx+fillCount > n {
					return shapeInfo{}
				}
				for j := 0; j < fillCount; j++ {
					sources[headIdx] = slotSource{
						kind:    slotKindConst,
						imm:     uint64(value.Nil),
						arithPC: uint8(pc),
					}
					headIdx++
				}
			} else {
				// Scratch fill — write R(a..b) := nil before head-op replay.
				sideEffects = append(sideEffects, sideEffect{
					kind: sideEffectLoadNil,
					a:    uint8(a),
					b:    uint8(b),
				})
			}
			continue
		}

		// Otherwise it must be a single-slot head op writing R(retA + headIdx).
		if headIdx >= n {
			return shapeInfo{}
		}
		expectedA := retA + headIdx
		if a := bytecode.A(ins); a != expectedA {
			return shapeInfo{}
		}
		src, ok := headOpSource(proto, ins)
		if !ok {
			return shapeInfo{}
		}
		src.arithPC = uint8(pc) // record pc for arith error reporting
		sources[headIdx] = src
		headIdx++
	}
	if headIdx != n {
		return shapeInfo{} // not enough head ops for the declared return count
	}
	return shapeInfo{
		ok:          true,
		sources:     sources,
		sideEffects: sideEffects,
		startA:      uint8(retA),
		retA:        uint8(retA),
		retB:        uint8(retB),
		retPC:       uint8(retPC),
	}
}

// sideEffectFromIns recognises a pre-return op that has no return-slot
// output. Returns (se, true) on a match; otherwise (zero, false) so the
// caller can try the head-op interpretation.
func sideEffectFromIns(op bytecode.OpCode, ins bytecode.Instruction) (sideEffect, bool) {
	switch op {
	case bytecode.SETUPVAL:
		// SETUPVAL A B: U(B) := R(A).
		a := bytecode.A(ins)
		b := bytecode.B(ins)
		if a < 0 || a > 255 || b < 0 || b > 255 {
			return sideEffect{}, false
		}
		return sideEffect{kind: sideEffectSetUpval, a: uint8(a), b: uint8(b)}, true
	default:
		return sideEffect{}, false
	}
}

// headOpSource recognises the supported head ops and returns a
// slotSource describing how PerOpCode.Run will materialise the value at
// the corresponding return slot. Returns ok=false for any unsupported op
// or operand configuration.
func headOpSource(proto *bytecode.Proto, ins bytecode.Instruction) (slotSource, bool) {
	op := bytecode.Op(ins)
	switch op {
	case bytecode.LOADK:
		bx := bytecode.Bx(ins)
		if bx < 0 || bx >= len(proto.Consts) {
			return slotSource{}, false
		}
		if proto.IsStringConst(bx) {
			return slotSource{}, false
		}
		return slotSource{kind: slotKindConst, imm: uint64(proto.Consts[bx])}, true

	case bytecode.LOADBOOL:
		if bytecode.C(ins) != 0 {
			return slotSource{}, false
		}
		return slotSource{kind: slotKindConst, imm: uint64(value.BoolValue(bytecode.B(ins) != 0))}, true

	case bytecode.LOADNIL:
		// Single-slot fill (B == A) only.
		if bytecode.B(ins) != bytecode.A(ins) {
			return slotSource{}, false
		}
		return slotSource{kind: slotKindConst, imm: uint64(value.Nil)}, true

	case bytecode.MOVE:
		// R(A) := R(B). Copy at Run time via host.GetReg(B) + SetReg(A).
		// B is uint8 in the encoding; check the cast.
		b := bytecode.B(ins)
		if b < 0 || b > 255 {
			return slotSource{}, false
		}
		return slotSource{kind: slotKindReg, reg: uint8(b)}, true

	case bytecode.GETUPVAL:
		// R(A) := U(B). Read at Run time via host.GetUpval(base, B).
		b := bytecode.B(ins)
		if b < 0 || b > 255 {
			return slotSource{}, false
		}
		return slotSource{kind: slotKindUpval, upval: uint8(b)}, true

	case bytecode.UNM:
		// R(A) := -R(B). Routed through host.Unm — string coercion +
		// __unm metamethod live there; can raise on non-numeric input.
		b := bytecode.B(ins)
		if b < 0 || b > 255 {
			return slotSource{}, false
		}
		return slotSource{kind: slotKindUnm, reg: uint8(b)}, true

	case bytecode.LEN:
		// R(A) := #R(B). Routed through host.Len — string byte-length /
		// table border / raise-on-other-types live there.
		b := bytecode.B(ins)
		if b < 0 || b > 255 {
			return slotSource{}, false
		}
		return slotSource{kind: slotKindLen, reg: uint8(b)}, true

	case bytecode.NOT:
		// R(A) := not R(B). Pure Go computation: BoolValue(!Truthy(...)).
		// No host helper needed — never raises, never allocates.
		b := bytecode.B(ins)
		if b < 0 || b > 255 {
			return slotSource{}, false
		}
		return slotSource{kind: slotKindNot, reg: uint8(b)}, true

	case bytecode.ADD, bytecode.SUB, bytecode.MUL, bytecode.DIV, bytecode.MOD, bytecode.POW:
		// R(A) := RK(B) <op> RK(C). Run uses host.Arith to compute the
		// result and write it into R(A); the slow path subsumes the
		// per-Run check for "both number? do SSE add" / "string coerce"
		// / "__add metamethod" / "not addable -> raise" lattice. This
		// matches PJ7's slow-path lane bit-for-bit (gibbous_host.go::
		// Arith is the same helper PJ7 uses on a deopt).
		b := bytecode.B(ins)
		c := bytecode.C(ins)
		if b < 0 || b > 511 || c < 0 || c > 511 {
			return slotSource{}, false
		}
		return slotSource{
			kind:    slotKindArith,
			arithOp: uint8(bytecode.Op(ins)),
			arithB:  uint16(b),
			arithC:  uint16(c),
		}, true

	default:
		return slotSource{}, false
	}
}

// CompiledSpike is what TranslateSpike returns: an mmap'd code page that
// Call() can invoke. Kept around for the spike-v0/v1 unit tests.
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

// CompiledSpikeV2 emits the value-stack-aware variant (writes via rbx).
// Kept for spike v0/v1/v2 unit tests; production wiring lives in
// PerOpCode (peropcode.go).
type CompiledSpikeV2 struct {
	page   *jitamd64.CodePage
	slotA  uint8
	jitCtx *jit.JITContext
}

// Dispose releases the mmap segment.
func (c *CompiledSpikeV2) Dispose() error {
	if c == nil || c.page == nil {
		return nil
	}
	err := c.page.Munmap()
	c.page = nil
	return err
}

// Run invokes the stub. The caller supplies a value-stack slice and the
// stub writes R(slotA) := <translated head op value> into it.
func (c *CompiledSpikeV2) Run(valueStack []uint64) uint64 {
	if c == nil || c.page == nil || len(valueStack) == 0 {
		panic("peroptranslator: CompiledSpikeV2.Run with empty stack")
	}
	vsBase := uintptr(unsafe.Pointer(&valueStack[0]))
	jitCtx := uintptr(unsafe.Pointer(c.jitCtx))
	return jitamd64.CallJITSpec(c.page.Addr(), jitCtx, vsBase)
}

// SlotA exposes the slot the stub writes to.
func (c *CompiledSpikeV2) SlotA() uint8 { return c.slotA }

// TranslateSpikeV2 — kept as the value-stack-aware spike artefact for the
// v2 unit test. The bridge-integration path uses TranslateProto, not this.
func TranslateSpikeV2(proto *bytecode.Proto) (*CompiledSpikeV2, error) {
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
	slotA := uint8(bytecode.A(proto.Code[0]))

	var buf []byte
	buf = jitamd64.EmitMovRaxImm64(buf, imm)
	buf = jitamd64.EmitMovqMemRegFromRax(buf, 3 /*rbx*/, int32(slotA)*8)
	buf = append(buf, 0x31, 0xC0) // xor eax, eax
	buf = jitamd64.EmitRet(buf)

	page, err := jitamd64.MmapCode(buf)
	if err != nil {
		return nil, fmt.Errorf("peroptranslator: mmap %d bytes: %w", len(buf), err)
	}
	ctx := jit.NewJITContext()
	return &CompiledSpikeV2{page: page, slotA: slotA, jitCtx: ctx}, nil
}

// TranslateSpike — kept as the RAX-return spike artefact for v0/v1 unit
// tests. The bridge-integration path uses TranslateProto, not this.
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
// computes the NaN-boxed u64 that R(A) would hold after the op runs.
//
// Returns an error if the op is unsupported or its operands fall outside
// the supported subset.
func headOpImm64(proto *bytecode.Proto, ins bytecode.Instruction) (uint64, error) {
	op := bytecode.Op(ins)
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
		// LOADNIL A B fills R(A..B) with nil. Single-slot shape requires
		// B == A; the wangshu frontend emits LOADNIL A B with B == A for
		// "local x = nil" / "return nil" in the inner kernel.
		if a, b := bytecode.A(ins), bytecode.B(ins); b != a {
			return 0, fmt.Errorf("peroptranslator: spike supports LOADNIL A==B only (single-slot), got A=%d B=%d", a, b)
		}
		return uint64(value.Nil), nil

	default:
		return 0, fmt.Errorf("peroptranslator: unsupported head op %s", op)
	}
}

// checkSingleReturn enforces the legacy spike's RETURN shape: A=0, B=2
// (one return value, R(0)). Used by TranslateSpike / TranslateSpikeV2;
// production path (TranslateProto via AnalyzeShape) handles N returns.
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
