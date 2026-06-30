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
	retB        uint8        // RETURN B — len(sources)+1 (or 1 for the setter form, 0 for multret tail call)
	retPC       uint8        // RETURN's pc index in proto.Code

	// tail-call form: TAILCALL + RETURN A B=0 (to-top). At Run time the
	// final side effect must be a sideEffectTailCall whose status code
	// drives a tri-state branch (0 = frame already popped, 2 = multret
	// DoReturn). When isTailCall is true, the head-op replay produces no
	// values; DoReturn is called only when the tail-call helper returns
	// status 2 (host tail call).
	isTailCall bool
	tailCallA  uint8
	tailCallB  uint8
	tailCallPC uint8

	// FORLOOP block: at most one numeric for-loop is recognized per
	// Proto today. When forLoopValid is true, sideEffects contains only
	// the preamble + post-loop ops; bodyEffects has the body ops that
	// run once per iteration.
	forLoopValid bool
	forLoopA     uint8        // R(forLoopA..+2) holds init/limit/step
	forLoopPC    uint8        // pc of the FORPREP instruction (for error pc anchoring)
	forLoopAfter int          // index into sideEffects after which the FORLOOP block runs (0-based)
	bodyEffects  []sideEffect // side effects executed once per iteration
}

// sideEffect describes a pre-return op that has no associated return slot.
// Today the supported kinds are SETUPVAL (U(b) := R(a)), LOADNIL (fill
// scratch regs), scratch-register fills (MOVE/LOADK writing a reg that's
// either outside the return window or overwritten before RETURN), the
// table/global setters (SETTABLE, SETGLOBAL), CALL (writes results to
// R(A..A+C-2) via host.CallBaseline), TAILCALL/SELF/SETLIST, and
// sideEffectArith (arithmetic ops re-emitted into loop bodies — same
// semantics as the slotKindArith head op but written through SetReg
// rather than into the return-slot replay).
type sideEffect struct {
	kind sideEffectKind
	a    uint8  // op's A field (source register for SETUPVAL; first slot for LOADNIL; dest reg for MOVE/LOADK/SETTABLE/CALL/Arith; source reg for SETGLOBAL)
	b    uint8  // op's B field (upvalue index for SETUPVAL; last slot for LOADNIL; source reg for MOVE; low 8 bits of RK key for SETTABLE; nargs+1 for CALL)
	c    uint8  // op's C field (low 8 bits of RK value for SETTABLE; nresults+1 for CALL; Arith op tag)
	imm  uint64 // baked NaN-boxed value (LOADK) or Bx (SETGLOBAL) or pc (CALL/TAILCALL) or packed RK fields (SETTABLE) or packed (op<<32|B<<16|C|pc) (Arith)
}

// sideEffectKind discriminates the pre-return op kinds.
type sideEffectKind uint8

const (
	sideEffectSetUpval  sideEffectKind = iota // SETUPVAL A B: U(B) := R(A)
	sideEffectLoadNil                         // LOADNIL A B: R(A..B) := nil
	sideEffectMove                            // MOVE A B: R(A) := R(B) (scratch fill)
	sideEffectLoadK                           // LOADK A Bx: R(A) := <imm> (scratch fill)
	sideEffectSetTable                        // SETTABLE A B C: R(A)[RK(B)] := RK(C)
	sideEffectSetGlobal                       // SETGLOBAL A Bx: Globals[K(Bx)] := R(A)
	sideEffectCall                            // CALL A B C: R(A..A+C-2) := R(A)(R(A+1..A+B-1))
	sideEffectTailCall                        // TAILCALL A B C: tri-state — see PerOpCode.Run
	sideEffectSelf                            // SELF A B C: R(A+1) := R(B); R(A) := R(B)[RK(C)]
	sideEffectSetList                         // SETLIST A B C: R(A)[(C-1)*FPF+i] := R(A+i) for i=1..B
	sideEffectArith                           // arithmetic R(A) := RK(B) <op> RK(C) (used inside loop bodies)
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
	slotKindConst     slotKind = iota // immediate (LOADK/LOADBOOL/LOADNIL)
	slotKindReg                       // copy from R(reg) (MOVE)
	slotKindUpval                     // read upvalue B (GETUPVAL)
	slotKindArith                     // arithmetic via host.Arith (ADD/SUB/MUL/DIV/MOD/POW)
	slotKindUnm                       // unary minus via host.Unm (UNM)
	slotKindLen                       // length op via host.Len (LEN)
	slotKindNot                       // logical not via Go-side Truthy/BoolValue (NOT)
	slotKindConcat                    // string concat via host.Concat (CONCAT A B C)
	slotKindCmp                       // EQ/LT/LE diamond → bool via host.Eq/Compare
	slotKindGetTable                  // R(A) := R(B)[RK(C)] via host.GetTable
	slotKindGetGlobal                 // R(A) := Globals[K(Bx)] via host.DoGetGlobal
	slotKindNewTable                  // R(A) := new table via host.NewTable
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
	// We accept all. Pre-RETURN ops are classified into:
	//   - head ops:    the *last* op before RETURN that writes R(retA+i), one per return slot
	//   - side effects: everything else (scratch fills, SETUPVAL, dead writes
	//                   that another op overwrites)
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

	// Tail-call shape detection: RETURN with B=0 (multret) is normally
	// rejected, but it's exactly the shape used after a TAILCALL — the
	// frontend pairs them. Recognize this case so PJ10 can promote tail
	// calls.
	isTailCall := false
	var tailCallA, tailCallB uint8
	if retB == 0 {
		if retPC < 1 {
			return shapeInfo{}
		}
		prev := proto.Code[retPC-1]
		if bytecode.Op(prev) != bytecode.TAILCALL {
			return shapeInfo{} // not a recognizable multret form
		}
		ta := bytecode.A(prev)
		tb := bytecode.B(prev)
		if ta < 0 || ta > 255 || tb < 0 || tb > 255 {
			return shapeInfo{}
		}
		isTailCall = true
		tailCallA = uint8(ta)
		tailCallB = uint8(tb)
		// Treat tail-call shape as producing zero head-op slots; head-op
		// replay is skipped. retB stays 0 in shapeInfo (multret-to-top
		// for the DoReturn that runs on status=2).
	}

	if retB == 0 && !isTailCall {
		return shapeInfo{}
	}
	n := 0
	if retB >= 1 {
		n = retB - 1 // number of return values (0 for setter form)
	}

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

	// Pre-pass: detect numeric for-loop blocks. The wangshu frontend emits
	//
	//   [k]   FORPREP A sBx=fwd   ; jump to FORLOOP at k+1+fwd
	//   <body>
	//   [k+1+fwd] FORLOOP A sBx=-fwd  ; back-edges to k+1
	//
	// Find FORPREP at any pc < retPC, check the matching FORLOOP exists,
	// and record (forPrepPC, forLoopPC, forLoopA, bodyStart, bodyEnd).
	// At most one loop block is supported today; if more than one
	// FORPREP is found the entire shape is rejected.
	type loopInfo struct {
		forPrepPC int
		forLoopPC int
		forLoopA  uint8
		bodyStart int // first body pc (inclusive)
		bodyEnd   int // last body pc (exclusive — = forLoopPC)
	}
	var loop *loopInfo
	for pc := 0; pc < retPC; pc++ {
		ins := proto.Code[pc]
		if bytecode.Op(ins) != bytecode.FORPREP {
			continue
		}
		if loop != nil {
			return shapeInfo{} // multiple loops out of scope
		}
		fwd := int(bytecode.SBx(ins))
		a := bytecode.A(ins)
		if a < 0 || a > 252 { // need A..A+3 in range
			return shapeInfo{}
		}
		flPC := pc + 1 + fwd
		if flPC <= pc || flPC >= retPC {
			return shapeInfo{}
		}
		flIns := proto.Code[flPC]
		if bytecode.Op(flIns) != bytecode.FORLOOP {
			return shapeInfo{}
		}
		if bytecode.A(flIns) != a {
			return shapeInfo{}
		}
		// FORLOOP sBx must back-edge to the body start (pc+1).
		flBack := flPC + 1 + int(bytecode.SBx(flIns))
		if flBack != pc+1 {
			return shapeInfo{}
		}
		loop = &loopInfo{
			forPrepPC: pc,
			forLoopPC: flPC,
			forLoopA:  uint8(a),
			bodyStart: pc + 1,
			bodyEnd:   flPC,
		}
	}

	// Helper: is pc inside the (one) for-loop body? Used by both passes
	// to route ops to bodyEffects vs outer sideEffects.
	inBody := func(pc int) bool {
		if loop == nil {
			return false
		}
		return pc >= loop.bodyStart && pc < loop.bodyEnd
	}
	_ = inBody

	// Pre-pass: detect comparison diamonds. The wangshu frontend emits a
	// fixed 4-op diamond for `R(Adst) := (R(B) op R(C)) == bool(A)`:
	//
	//   [pc+0] EQ/LT/LE A B C
	//   [pc+1] JMP sBx=1                    ; skip the false-arm LOADBOOL
	//   [pc+2] LOADBOOL Adst 0 1            ; R(Adst) := false, pc++
	//   [pc+3] LOADBOOL Adst 1 0            ; R(Adst) := true
	//
	// We collapse this into one synthetic head op writing R(Adst), placed
	// at the diamond's *last* pc (pc+3) so the last-writer pass treats it
	// as the slot's head.
	//
	// diamondAt[pc] = (Adst, true) when pc is the start of a diamond.
	// diamondMember[pc] = true for any pc in pc+1..pc+3 (so the normal
	// per-op classifier skips them).
	diamondStart := make(map[int]uint8, 0) // pc → Adst
	diamondMember := make(map[int]bool, 0)
	for pc := 0; pc+3 < retPC; pc++ {
		if dst, ok := matchCmpDiamond(proto, pc); ok {
			diamondStart[pc] = dst
			diamondMember[pc+1] = true
			diamondMember[pc+2] = true
			diamondMember[pc+3] = true
			pc += 3 // skip the rest of the diamond
		}
	}

	// First pass: find the last writer pc for each return slot. We need
	// the *last* writer so MOVEs into a slot that later gets CONCAT'd
	// over are recognised as side-effects, not head ops.
	headPC := make([]int, n)
	for i := range headPC {
		headPC[i] = -1
	}
	for pc := 0; pc < retPC; pc++ {
		// Skip the JMP / LOADBOOL members of a diamond — only the
		// diamond's start pc is the writer.
		if diamondMember[pc] {
			continue
		}
		// Diamond start: the dst is written.
		if dst, ok := diamondStart[pc]; ok {
			idx := int(dst) - retA
			if idx >= 0 && idx < n {
				headPC[idx] = pc
			}
			continue
		}

		ins := proto.Code[pc]
		op := bytecode.Op(ins)
		// Side-effect-only ops have no return-slot dest.
		if op == bytecode.SETUPVAL || op == bytecode.SETTABLE || op == bytecode.SETGLOBAL || op == bytecode.SETLIST {
			continue
		}
		// SELF writes R(A) and R(A+1). They're almost always
		// overwritten by a subsequent CALL — record as last-writer for
		// completeness, but in practice the CALL will overwrite them.
		if op == bytecode.SELF {
			a := bytecode.A(ins)
			for _, r := range [2]int{a, a + 1} {
				idx := r - retA
				if idx >= 0 && idx < n {
					headPC[idx] = pc
				}
			}
			continue
		}
		// CALL writes R(A..A+C-2) when C>=2. We mark CALL as the writer
		// for each affected return slot, but at the second pass it
		// becomes a sideEffectCall and the head op for the slot is a
		// post-CALL register read (slotKindReg).
		if op == bytecode.CALL {
			a := bytecode.A(ins)
			c := bytecode.C(ins)
			if c >= 2 {
				for k := 0; k < c-1; k++ {
					idx := a + k - retA
					if idx >= 0 && idx < n {
						headPC[idx] = pc
					}
				}
			}
			continue
		}
		// LOADNIL writes a closed range R(A..B); each slot in that
		// range gets this pc as its last writer (so far).
		if op == bytecode.LOADNIL {
			a, b := bytecode.A(ins), bytecode.B(ins)
			for r := a; r <= b; r++ {
				idx := r - retA
				if idx >= 0 && idx < n {
					headPC[idx] = pc
				}
			}
			continue
		}
		// All other ops have R(A) as dest.
		a := bytecode.A(ins)
		idx := a - retA
		if idx >= 0 && idx < n {
			headPC[idx] = pc
		}
	}
	// Every return slot must have a writer.
	for _, pc := range headPC {
		if pc < 0 {
			return shapeInfo{}
		}
	}

	// Second pass: walk ops in order. An op is a head op for slot i iff
	// pc == headPC[i] (and the op writes only one slot — LOADNIL handled
	// separately). Anything else is a side effect.
	var (
		sources     []slotSource
		sideEffects []sideEffect
		bodyEffects []sideEffect
	)
	if n > 0 {
		sources = make([]slotSource, n)
	}
	// effectsRef points at the current append target — either the outer
	// sideEffects slice or the body's bodyEffects slice. Body ops between
	// FORPREP and FORLOOP route through bodyEffects.
	effectsRef := &sideEffects
	// Build pc → slotIdx map for O(1) lookup.
	pcToSlot := make(map[int]int, n)
	for i, pc := range headPC {
		pcToSlot[pc] = i
	}
	// forLoopAfter records the outer-effects index at which the loop is
	// inserted (so PerOpCode.Run knows where to switch from preamble to
	// loop iteration to post-loop effects).
	forLoopAfter := -1
	for pc := 0; pc < retPC; pc++ {
		// FORPREP marker: finish appending preamble side effects, then
		// switch to body mode. The FORPREP itself is handled by the
		// Run-time loop dispatch (host.ForPrep + step loop), not as a
		// side effect.
		if loop != nil && pc == loop.forPrepPC {
			forLoopAfter = len(sideEffects)
			effectsRef = &bodyEffects
			continue
		}
		// FORLOOP marker: end body mode, switch back to outer side
		// effects (post-loop ops).
		if loop != nil && pc == loop.forLoopPC {
			effectsRef = &sideEffects
			continue
		}

		// Inside the body, restrict to the body-op subset.
		if loop != nil && pc > loop.forPrepPC && pc < loop.forLoopPC {
			ins := proto.Code[pc]
			op := bytecode.Op(ins)
			se, ok := bodyEffectFromIns(proto, op, ins, pc)
			if !ok {
				return shapeInfo{}
			}
			*effectsRef = append(*effectsRef, se)
			continue
		}

		// Skip diamond member ops — they're folded into the diamond start.
		if diamondMember[pc] {
			continue
		}

		// Diamond start: build slotKindCmp head op.
		if dst, ok := diamondStart[pc]; ok {
			if slotIdx, hit := pcToSlot[pc]; hit {
				cmpIns := proto.Code[pc]
				cmpOp := bytecode.Op(cmpIns)
				cmpA := bytecode.A(cmpIns)
				cmpB := bytecode.B(cmpIns)
				cmpC := bytecode.C(cmpIns)
				sources[slotIdx] = slotSource{
					kind:    slotKindCmp,
					arithOp: uint8(cmpOp),
					arithB:  uint16(cmpB),
					arithC:  uint16(cmpC),
					reg:     uint8(cmpA), // negate bit
					arithPC: uint8(pc),
				}
				_ = dst
				continue
			}
			// Diamond writes a register that's never read by RETURN —
			// shouldn't happen given diamondStart is only set when the
			// dst lies in the return window. Treat as unsupported.
			return shapeInfo{}
		}

		ins := proto.Code[pc]
		op := bytecode.Op(ins)

		// Pure side-effect ops (no reg dest).
		if se, ok := sideEffectFromIns(op, ins); ok {
			*effectsRef = append(*effectsRef, se)
			continue
		}

		// CALL: emit a sideEffectCall + for each return slot it writes,
		// install a slotKindReg head op that reads the slot back after
		// the call returns. (A side-effect-only call — C=1, no results —
		// affects no slots and just emits the side effect.)
		if op == bytecode.CALL {
			a := bytecode.A(ins)
			b := bytecode.B(ins)
			cc := bytecode.C(ins)
			if a < 0 || a > 255 || b < 0 || b > 255 || cc < 0 || cc > 255 {
				return shapeInfo{}
			}
			*effectsRef = append(*effectsRef, sideEffect{
				kind: sideEffectCall,
				a:    uint8(a),
				b:    uint8(b),
				c:    uint8(cc),
				imm:  uint64(pc),
			})
			if cc >= 2 {
				for k := 0; k < cc-1; k++ {
					reg := a + k
					idx := reg - retA
					if idx >= 0 && idx < n && headPC[idx] == pc {
						sources[idx] = slotSource{
							kind:    slotKindReg,
							reg:     uint8(reg),
							arithPC: uint8(pc),
						}
					}
				}
			}
			continue
		}

		// SELF: emit as side effect. SELF.A is the dest (method goes to
		// R(A), self to R(A+1)). After SELF, the typical pattern is
		// CALL A B C with method at R(A) and self at R(A+1). Note
		// SELF can raise (attempt to index nil).
		if op == bytecode.SELF {
			a := bytecode.A(ins)
			b := bytecode.B(ins)
			cc := bytecode.C(ins)
			if a < 0 || a > 255 || b < 0 || b > 255 || cc < 0 || cc > 511 {
				return shapeInfo{}
			}
			*effectsRef = append(*effectsRef, sideEffect{
				kind: sideEffectSelf,
				a:    uint8(a),
				b:    uint8(b),
				c:    uint8(cc & 0xff),
				imm:  uint64(pc)<<32 | uint64(cc),
			})
			// For any slot that maps to this SELF as last writer, set
			// up a slotKindReg to read back R(retA+slot).
			for _, r := range [2]int{a, a + 1} {
				idx := r - retA
				if idx >= 0 && idx < n && headPC[idx] == pc {
					sources[idx] = slotSource{
						kind:    slotKindReg,
						reg:     uint8(r),
						arithPC: uint8(pc),
					}
				}
			}
			continue
		}
		// TAILCALL: pre-RETURN, dispatched at Run time via host.TailCall
		// with the captured (a, b, c, pc). The shapeInfo records the A/B
		// fields separately so the Run path can decide whether to skip
		// DoReturn (status 0 = frame already popped) or fall through to
		// DoReturn with multret (status 2 = host tail call).
		if op == bytecode.TAILCALL && isTailCall && pc == retPC-1 {
			*effectsRef = append(*effectsRef, sideEffect{
				kind: sideEffectTailCall,
				a:    uint8(bytecode.A(ins)),
				b:    uint8(bytecode.B(ins)),
				c:    uint8(bytecode.C(ins)),
				imm:  uint64(pc),
			})
			continue
		}

		// LOADNIL multi-slot: special-case because it may serve as head
		// op for multiple return slots simultaneously.
		if op == bytecode.LOADNIL {
			a, b := bytecode.A(ins), bytecode.B(ins)
			if b < a || a < 0 || b > 255 {
				return shapeInfo{}
			}
			// Decompose into per-register writes; for each, decide head vs scratch.
			scratchA, scratchB := -1, -1
			for r := a; r <= b; r++ {
				idx := r - retA
				if idx >= 0 && idx < n && headPC[idx] == pc {
					sources[idx] = slotSource{
						kind:    slotKindConst,
						imm:     uint64(value.Nil),
						arithPC: uint8(pc),
					}
					continue
				}
				// Scratch register (not a return slot, or overwritten later).
				if scratchA < 0 {
					scratchA = int(r)
				}
				scratchB = int(r)
			}
			if scratchA >= 0 {
				*effectsRef = append(*effectsRef, sideEffect{
					kind: sideEffectLoadNil,
					a:    uint8(scratchA),
					b:    uint8(scratchB),
				})
			}
			continue
		}

		// All other ops have R(A) as dest. Check if this is the slot's
		// head op or a scratch fill.
		if slotIdx, ok := pcToSlot[pc]; ok {
			src, ok := headOpSource(proto, ins)
			if !ok {
				return shapeInfo{}
			}
			src.arithPC = uint8(pc)
			sources[slotIdx] = src
			continue
		}

		// Scratch fill (writes a reg that's later overwritten, or
		// outside the return window). Only MOVE / LOADK / LOADBOOL /
		// LOADNIL are supported as scratch fills today.
		if se, ok := scratchFromIns(proto, op, ins); ok {
			*effectsRef = append(*effectsRef, se)
			continue
		}
		return shapeInfo{}
	}
	// Post-pass: for any return slot whose headPC fell inside the loop
	// body, install slotKindReg{reg=retA+i} so PerOpCode.Run reads the
	// final register value after all iterations complete. The body's
	// arithmetic side effects have already updated the register on each
	// iteration.
	if loop != nil {
		for slot := 0; slot < n; slot++ {
			pc := headPC[slot]
			if pc >= loop.bodyStart && pc < loop.bodyEnd {
				sources[slot] = slotSource{
					kind:    slotKindReg,
					reg:     uint8(retA + slot),
					arithPC: uint8(pc),
				}
			}
		}
	}

	info := shapeInfo{
		ok:          true,
		sources:     sources,
		sideEffects: sideEffects,
		startA:      uint8(retA),
		retA:        uint8(retA),
		retB:        uint8(retB),
		retPC:       uint8(retPC),
		isTailCall:  isTailCall,
		tailCallA:   tailCallA,
		tailCallB:   tailCallB,
		tailCallPC:  uint8(retPC - 1),
	}
	if loop != nil {
		info.forLoopValid = true
		info.forLoopA = loop.forLoopA
		info.forLoopPC = uint8(loop.forPrepPC)
		info.forLoopAfter = forLoopAfter
		info.bodyEffects = bodyEffects
	}
	return info
}

// matchCmpDiamond returns (Adst, true) if the 4-op comparison diamond
// pattern is rooted at proto.Code[pc]. The pattern is:
//
//	[pc+0] EQ/LT/LE A B C
//	[pc+1] JMP sBx=1                    ; skip false-arm LOADBOOL
//	[pc+2] LOADBOOL Adst 0 1            ; R(Adst) := false; pc++ skips next
//	[pc+3] LOADBOOL Adst 1 0            ; R(Adst) := true
//
// Net effect: R(Adst) := (R(B) cmp R(C)) iff (A != 0). This is what the
// wangshu frontend emits for boolean expressions like `return a == b`,
// `return a < b`, `return a <= b` and their negations.
func matchCmpDiamond(proto *bytecode.Proto, pc int) (uint8, bool) {
	code := proto.Code
	if pc+3 >= len(code) {
		return 0, false
	}
	cmp := code[pc]
	cmpOp := bytecode.Op(cmp)
	if cmpOp != bytecode.EQ && cmpOp != bytecode.LT && cmpOp != bytecode.LE {
		return 0, false
	}
	if bytecode.Op(code[pc+1]) != bytecode.JMP {
		return 0, false
	}
	if bytecode.SBx(code[pc+1]) != 1 {
		return 0, false
	}
	lb1 := code[pc+2]
	if bytecode.Op(lb1) != bytecode.LOADBOOL {
		return 0, false
	}
	if bytecode.B(lb1) != 0 || bytecode.C(lb1) != 1 {
		return 0, false
	}
	lb2 := code[pc+3]
	if bytecode.Op(lb2) != bytecode.LOADBOOL {
		return 0, false
	}
	if bytecode.B(lb2) != 1 || bytecode.C(lb2) != 0 {
		return 0, false
	}
	if bytecode.A(lb1) != bytecode.A(lb2) {
		return 0, false
	}
	dst := bytecode.A(lb1)
	if dst < 0 || dst > 255 {
		return 0, false
	}
	return uint8(dst), true
}

// bodyEffectFromIns recognises ops that may appear inside a FORLOOP body
// and lowers them into sideEffect records. The accepted subset is the
// side-effect-style ops we already handle (MOVE/LOADK/LOADBOOL/SETTABLE/
// SETGLOBAL) plus the arithmetic ops (ADD/SUB/MUL/DIV/MOD/POW) — which
// don't normally appear as side effects in the outer shape, but inside a
// loop body their results land in scratch accumulators that are then read
// after the loop ends.
//
// Returns ok=false for any op not in the supported body subset.
func bodyEffectFromIns(proto *bytecode.Proto, op bytecode.OpCode, ins bytecode.Instruction, pc int) (sideEffect, bool) {
	// Reuse the existing recognisers for the side-effect-style ops.
	if se, ok := sideEffectFromIns(op, ins); ok {
		return se, true
	}
	if se, ok := scratchFromIns(proto, op, ins); ok {
		return se, true
	}
	// Arithmetic ops: re-emit as sideEffectArith. RK B/C fit in 9 bits;
	// pack them with the opcode and pc into imm so Run can dispatch
	// host.Arith correctly.
	switch op {
	case bytecode.ADD, bytecode.SUB, bytecode.MUL, bytecode.DIV, bytecode.MOD, bytecode.POW:
		a := bytecode.A(ins)
		b := bytecode.B(ins)
		c := bytecode.C(ins)
		if a < 0 || a > 255 || b < 0 || b > 511 || c < 0 || c > 511 {
			return sideEffect{}, false
		}
		return sideEffect{
			kind: sideEffectArith,
			a:    uint8(a),
			imm: uint64(uint8(op))<<48 |
				uint64(uint16(b))<<32 |
				uint64(uint16(c))<<16 |
				uint64(uint16(pc)),
		}, true
	}
	return sideEffect{}, false
}

// scratchFromIns recognises an op writing a scratch register (a register
// outside the return window, or one that's overwritten before RETURN).
// Returns a sideEffect that PerOpCode.Run will replay before head-op
// materialisation. Only side-effect-free, never-raise ops are supported:
// MOVE / LOADK / LOADBOOL (C=0) / LOADNIL.
func scratchFromIns(proto *bytecode.Proto, op bytecode.OpCode, ins bytecode.Instruction) (sideEffect, bool) {
	a := bytecode.A(ins)
	if a < 0 || a > 255 {
		return sideEffect{}, false
	}
	switch op {
	case bytecode.MOVE:
		b := bytecode.B(ins)
		if b < 0 || b > 255 {
			return sideEffect{}, false
		}
		return sideEffect{kind: sideEffectMove, a: uint8(a), b: uint8(b)}, true

	case bytecode.LOADK:
		bx := bytecode.Bx(ins)
		if bx < 0 || bx >= len(proto.Consts) {
			return sideEffect{}, false
		}
		if proto.IsStringConst(bx) {
			return sideEffect{}, false
		}
		return sideEffect{kind: sideEffectLoadK, a: uint8(a), imm: uint64(proto.Consts[bx])}, true

	case bytecode.LOADBOOL:
		if bytecode.C(ins) != 0 {
			return sideEffect{}, false
		}
		return sideEffect{
			kind: sideEffectLoadK,
			a:    uint8(a),
			imm:  uint64(value.BoolValue(bytecode.B(ins) != 0)),
		}, true

	case bytecode.LOADNIL:
		b := bytecode.B(ins)
		if b < a || b > 255 {
			return sideEffect{}, false
		}
		return sideEffect{kind: sideEffectLoadNil, a: uint8(a), b: uint8(b)}, true

	default:
		return sideEffect{}, false
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

	case bytecode.SETTABLE:
		// SETTABLE A B C: R(A)[RK(B)] := RK(C). A is the table register
		// (0..255); B/C are RK-encoded (0..511, top bit means K-pool).
		a := bytecode.A(ins)
		b := bytecode.B(ins)
		c := bytecode.C(ins)
		if a < 0 || a > 255 || b < 0 || b > 511 || c < 0 || c > 511 {
			return sideEffect{}, false
		}
		return sideEffect{
			kind: sideEffectSetTable,
			a:    uint8(a),
			b:    uint8(b & 0xff),
			c:    uint8(c & 0xff),
			imm:  uint64(b)<<16 | uint64(c), // pack full 9-bit B/C
		}, true

	case bytecode.SETGLOBAL:
		// SETGLOBAL A Bx: Globals[K(Bx)] := R(A).
		a := bytecode.A(ins)
		bx := bytecode.Bx(ins)
		if a < 0 || a > 255 || bx < 0 {
			return sideEffect{}, false
		}
		return sideEffect{
			kind: sideEffectSetGlobal,
			a:    uint8(a),
			imm:  uint64(bx),
		}, true

	case bytecode.SETLIST:
		// SETLIST A B C: R(A)[(C-1)*FPF + i] := R(A+i) for i=1..B.
		// C=0 form (batch num in next instruction) is unsupported in
		// this single-BB shape.
		a := bytecode.A(ins)
		b := bytecode.B(ins)
		c := bytecode.C(ins)
		if a < 0 || a > 255 || b < 0 || b > 255 || c <= 0 || c > 255 {
			return sideEffect{}, false
		}
		return sideEffect{
			kind: sideEffectSetList,
			a:    uint8(a),
			b:    uint8(b),
			c:    uint8(c),
		}, true

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

	case bytecode.CONCAT:
		// R(A) := R(B) .. .. R(C). B/C are register indices (not RK),
		// always in 0..255 range. Routed through host.Concat which
		// handles __concat metamethod + raise on non-concatable types.
		b := bytecode.B(ins)
		c := bytecode.C(ins)
		if b < 0 || b > 255 || c < 0 || c > 255 || c < b {
			return slotSource{}, false
		}
		return slotSource{
			kind:   slotKindConcat,
			arithB: uint16(b),
			arithC: uint16(c),
		}, true

	case bytecode.GETTABLE:
		// R(A) := R(B)[RK(C)]. Routed through host.GetTable — IC lookup
		// / hash / __index metamethod / raise on attempt-to-index-nil.
		b := bytecode.B(ins)
		c := bytecode.C(ins)
		if b < 0 || b > 255 || c < 0 || c > 511 {
			return slotSource{}, false
		}
		return slotSource{
			kind:   slotKindGetTable,
			arithB: uint16(b),
			arithC: uint16(c),
		}, true

	case bytecode.GETGLOBAL:
		// R(A) := Globals[K(Bx)] via host.DoGetGlobal. Bx is a constant
		// index (the global variable name). Up to 18-bit, stored in imm.
		bx := bytecode.Bx(ins)
		if bx < 0 {
			return slotSource{}, false
		}
		return slotSource{
			kind: slotKindGetGlobal,
			imm:  uint64(bx),
		}, true

	case bytecode.NEWTABLE:
		// R(A) := new table with hint sizes (Fb-encoded B array hint,
		// C hash hint). Routed through host.NewTable — pure allocation,
		// never raises (only Go OOM).
		b := bytecode.B(ins)
		c := bytecode.C(ins)
		if b < 0 || b > 255 || c < 0 || c > 255 {
			return slotSource{}, false
		}
		return slotSource{
			kind:   slotKindNewTable,
			arithB: uint16(b),
			arithC: uint16(c),
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
