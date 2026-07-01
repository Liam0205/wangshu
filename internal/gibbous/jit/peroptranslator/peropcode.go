//go:build wangshu_p4 && amd64

// peropcode.go — production-facing bridge.GibbousCode implementation for
// PJ10's per-opcode translator. This is the artefact bridge.installGibbous
// receives when a Proto matches AnalyzeShape (constant-tuple shape that
// PJ7's analyzeShape rejects).
//
// Lifecycle:
//   - Compile (in jit/compiler.go) calls AnalyzeShape; on match it calls
//     TranslateProto, which returns a *PerOpCode wrapped as GibbousCode.
//   - bridge.installGibbous stores it in the gibbousCodes map; crescent's
//     doCall path calls Run on it when it sees a TierGibbous Proto.
//   - On shutdown, Dispose releases the mmap segment.
//
// The emitted machine code is a one-instruction stub:
//
//   xor eax, eax
//   ret
//
// All the actual R(A+i) writes live in PerOpCode.Run (Go side), which
// pulls the cached imm64 values out of the PerOpCode struct and calls
// host.SetReg for each, then host.DoReturn to pop the frame. This matches
// PJ7's "simplified" pattern (mmap stub returns RAX, Go does SetReg +
// DoReturn) but generalised to N returns: we don't need an mmap segment
// that materialises N values, since they're already baked into the
// struct at compile time.
//
// Why the stub still runs:
//   - Preserves the round-trip through the trampoline so callers see the
//     same "Go calls into mmap memory" lifecycle PJ0-PJ9 has, including
//     callee-saved restore and r15 = jitCtx wiring. Later sub-stages
//     (PJ10b arithmetic, PJ10c control flow) will replace the trivial
//     "xor eax,eax; ret" with real translated code; keeping the stub now
//     means the rest of the wiring doesn't have to change later.
//   - Exposes PJ10 to the same "the mmap segment is actually executed"
//     invariants real PJ7 templates rely on (W^X has actually been
//     flipped, icache has actually been flushed). Without the stub, a
//     bug in MmapCode / mprotect would not be caught by PJ10 tests.

package peroptranslator

import (
	"errors"
	"unsafe"

	"github.com/Liam0205/wangshu/internal/bridge"
	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/gibbous/jit"
	jitamd64 "github.com/Liam0205/wangshu/internal/gibbous/jit/amd64"
	"github.com/Liam0205/wangshu/internal/value"
)

// errSpikeNilHost is returned by TranslateProto when the caller fails to
// supply a P4HostState. The bridge always wires one via SetHostState, so
// this only fires in tests that forget the injection.
var errSpikeNilHost = errors.New("peroptranslator: nil P4HostState (caller must inject host)")

// PerOpCode is peroptranslator's bridge.GibbousCode implementation. It
// owns the mmap'd code page, holds the per-slot source descriptors baked
// at Compile time, and replays them via host.SetReg on every Run.
type PerOpCode struct {
	proto    *bytecode.Proto
	codePage *jitamd64.CodePage
	jitCtx   *jit.JITContext
	host     jit.P4HostState

	// sources[i] describes the value PerOpCode.Run writes into
	// R(retA + i): either a baked imm64 (constant) or a runtime copy
	// from another register / upvalue.
	sources []slotSource

	// sideEffects are pre-return ops (today: SETUPVAL) that produce no
	// return-slot output but mutate host-visible state. Run executes them
	// before the head-op replay so the setter form
	// `function(v) upval = v end` works.
	sideEffects []sideEffect

	// retA / retB / retPC are baked at Compile time so Run can hand them
	// to host.DoReturn without re-parsing the Proto.
	retA  uint8
	retB  uint8
	retPC uint8

	// Tail-call shape (`function() return f() end` etc.): when true, the
	// final side effect is a sideEffectTailCall whose return code
	// determines whether DoReturn runs. The retB used for the (possibly
	// skipped) DoReturn is set to 0 (multret to-top) per the dead-RETURN
	// the frontend emits after TAILCALL.
	isTailCall bool

	// FORLOOP block: when forLoopValid is true, the side-effects list
	// is split into [preamble : forLoopAfter] + the loop body + [forLoopAfter :]
	// post-loop ops. The Run path inserts an iteration loop at the split:
	//   1. Run preamble side effects.
	//   2. Call host.ForPrep(forLoopA).
	//   3. Loop: step R(A), check condition, run body, repeat.
	//   4. Run post-loop side effects.
	//
	// **Note**: `forLoopAfter` is also reused by the TFORLOOP path below
	// (see AnalyzeShape at translator.go:946). AnalyzeShape guarantees
	// forLoopValid and tforLoopValid are mutually exclusive, so the
	// shared field is safe today. If that invariant ever relaxes, split
	// into `forLoopAfter` and `tforLoopAfter` before adding the new
	// case — the field name misleadingly implies FORLOOP-only.
	forLoopValid bool
	forLoopA     uint8
	forLoopPC    uint8
	forLoopAfter int
	bodyEffects  []sideEffect

	// TFORLOOP block (generic for): when tforLoopValid is true, the
	// dispatcher calls host.TForLoop each iteration until it returns -2.
	tforLoopValid bool
	tforLoopA     uint8
	tforLoopC     uint8
	tforLoopPC    uint8
}

// Proto returns the source Proto.
func (c *PerOpCode) Proto() *bytecode.Proto { return c.proto }

// Run is the cross-tier entry point: crescent's doCall invokes it when
// it sees this Proto promoted to TierGibbous.
//
// Execution:
//  1. Refresh JITContext addresses (arena base, value-stack base, etc.).
//     PJ7's p4Code does the same — the arena can grow between calls so
//     these have to be recomputed every Run.
//  2. Call into the mmap segment via CallJITFull (sets r15 = jitCtx).
//     The segment runs `xor eax,eax; ret`, returning 0 in RAX.
//  3. Replay the cached imms into R(retA + i) via host.SetReg.
//  4. host.DoReturn pops the frame and routes the N return values up.
//
// Returns 0 on success, 1 on host-side error (none in PJ10a; head ops
// here are pure value materialisation, no error paths). The status byte
// matches the GibbousCode.Run contract on bridge/p3compiler.go.
func (c *PerOpCode) Run(stack []uint64, base uint32) int32 {
	if c.codePage == nil || c.jitCtx == nil || c.host == nil {
		if len(stack) > 0 {
			stack[0] = 1
		}
		return 1
	}

	// Refcount acquire for the duration of this Run, same protocol as
	// p4Code.Run in ../code.go. See internal/gibbous/jit/amd64/codepage_linux.go
	// for the refcount + deferred munmap rationale.
	if !c.codePage.Enter() {
		if len(stack) > 0 {
			stack[0] = 1
		}
		return 1
	}
	defer c.codePage.Exit()

	c.jitCtx.SetArenaBase(c.host.ArenaBaseAddr())
	c.jitCtx.SetValueStackBase(c.host.ValueStackBaseAddr(int32(base)))
	c.jitCtx.SetCIDepthAddr(c.host.CIDepthHostAddr())
	c.jitCtx.SetCISegBaseAddr(c.host.CISegBaseHostAddr())
	c.jitCtx.SetTopAddr(c.host.TopHostAddr())

	jitCtxAddr := uintptr(unsafe.Pointer(c.jitCtx))
	_ = jitamd64.CallJITFull(c.codePage.Addr(), jitCtxAddr) // returns 0

	// Run pre-return side effects + (optional) FORLOOP body. The loop,
	// when present, splits the outer side-effects slice at forLoopAfter:
	// effects before that index are the preamble (init/limit/step setup
	// + any pre-loop scratch fills), then host.ForPrep is called, then
	// the body runs once per iteration via runEffect, then post-loop
	// outer effects run.
	for i, se := range c.sideEffects {
		if c.forLoopValid && i == c.forLoopAfter {
			if st := c.runForLoop(base); st != 0 {
				return st
			}
		}
		if c.tforLoopValid && i == c.forLoopAfter {
			if st := c.runTForLoop(base); st != 0 {
				return st
			}
		}
		if st := c.runEffect(base, se, stack); st != 0 {
			if st == -1 { // sentinel: tail-call done, frame popped
				return 0
			}
			return st
		}
	}
	// Loop inserted at the very end (no post-loop side effects).
	if c.forLoopValid && c.forLoopAfter == len(c.sideEffects) {
		if st := c.runForLoop(base); st != 0 {
			return st
		}
	}
	if c.tforLoopValid && c.forLoopAfter == len(c.sideEffects) {
		if st := c.runTForLoop(base); st != 0 {
			return st
		}
	}

	// Materialise the N return values into R(retA + i). Each slot is one
	// of four kinds: a baked immediate (LOADK / LOADBOOL / LOADNIL), a
	// copy from another register (MOVE), an upvalue read (GETUPVAL), or
	// an arithmetic operation routed through host.Arith (ADD/SUB/MUL/
	// DIV/MOD/POW).
	//
	// host.Arith writes its result directly into R(A) via the host's
	// SetReg-equivalent inside the helper — see gibbous_host.go::Arith.
	// On a slow path failure (non-coercible operand / __add metamethod
	// raise), Arith returns 1 and sets the host's pendingErr; we
	// propagate that through Run's status code so the bridge tier-stuck
	// machinery / DoReturn doesn't run on a half-formed frame.
	//
	// Read ordering: GETUPVAL / GetReg reads happen before any SetReg
	// for the corresponding slot, so chains like `return x, x+1` (if we
	// ever extend AnalyzeShape to accept them) would see consistent
	// inputs.
	for i, src := range c.sources {
		var val uint64
		switch src.kind {
		case slotKindConst:
			val = src.imm
		case slotKindReg:
			val = c.host.GetReg(int32(src.reg))
		case slotKindUpval:
			val = c.host.GetUpval(int32(base), int32(src.upval))
		case slotKindArith:
			// host.Arith writes R(c.retA+i) directly + may raise.
			// pc is the index of the arithmetic op in proto.Code.
			if st := c.host.Arith(
				int32(base),
				int32(src.arithPC),
				int32(src.arithOp),
				int32(src.arithB),
				int32(src.arithC),
				int32(c.retA)+int32(i),
			); st != 0 {
				return st
			}
			continue // skip the SetReg below; Arith already wrote R(A)
		case slotKindUnm:
			// host.Unm writes R(c.retA+i) directly + may raise on
			// non-coercible operand / __unm metamethod failure.
			if st := c.host.Unm(
				int32(base),
				int32(src.arithPC),
				int32(src.reg),
				int32(c.retA)+int32(i),
			); st != 0 {
				return st
			}
			continue
		case slotKindLen:
			// host.Len writes R(c.retA+i) directly + may raise on
			// non-string / non-table operands lacking __len.
			if st := c.host.Len(
				int32(base),
				int32(src.arithPC),
				int32(src.reg),
				int32(c.retA)+int32(i),
			); st != 0 {
				return st
			}
			continue
		case slotKindConcat:
			// host.Concat writes R(c.retA+i) directly + may raise on
			// non-concatable operands (no __concat, not string/number).
			// arithB/arithC carry the CONCAT range R(B..C) inclusive.
			if st := c.host.Concat(
				int32(base),
				int32(src.arithPC),
				int32(c.retA)+int32(i),
				int32(src.arithB),
				int32(src.arithC),
			); st != 0 {
				return st
			}
			continue
		case slotKindCmp:
			// Comparison diamond: net effect is
			//   R(dst) := (R(B) op R(C)) iff (negate != 0)
			// where negate is the EQ/LT/LE op's A field (the "if not
			// matching A then pc++" bit). host.Eq / host.Compare return
			// packed bits (bit0 = result, bit1 = error). On error we
			// propagate the status code so DoReturn doesn't run on a
			// half-formed frame.
			var packed int32
			cmpOp := bytecode.OpCode(src.arithOp)
			if cmpOp == bytecode.EQ {
				packed = c.host.Eq(
					int32(base),
					int32(src.arithPC),
					int32(src.arithB),
					int32(src.arithC),
				)
			} else {
				packed = c.host.Compare(
					int32(base),
					int32(src.arithPC),
					int32(cmpOp),
					int32(src.arithB),
					int32(src.arithC),
				)
			}
			if packed&2 != 0 {
				return 1 // error
			}
			result := packed&1 != 0
			if src.reg == 0 {
				result = !result // EQ/LT/LE A=0 negates the runtime result
			}
			val = uint64(value.BoolValue(result))
		case slotKindGetTable:
			// host.GetTable writes R(c.retA+i) directly + may raise on
			// attempt-to-index-nil / __index raise.
			if st := c.host.GetTable(
				int32(base),
				int32(src.arithPC),
				int32(c.retA)+int32(i),
				int32(src.arithB),
				int32(src.arithC),
			); st != 0 {
				return st
			}
			continue
		case slotKindGetGlobal:
			// host.DoGetGlobal writes R(c.retA+i) directly + may raise.
			if st := c.host.DoGetGlobal(
				int32(base),
				int32(src.arithPC),
				int32(c.retA)+int32(i),
				int32(src.imm),
			); st != 0 {
				return st
			}
			continue
		case slotKindNewTable:
			// host.NewTable writes R(c.retA+i) directly; never raises
			// (only Go OOM, which the signature does not surface).
			if st := c.host.NewTable(
				int32(base),
				int32(src.arithPC),
				int32(c.retA)+int32(i),
				int32(src.arithB),
				int32(src.arithC),
			); st != 0 {
				return st
			}
			continue
		case slotKindAndOr:
			// TESTSET diamond: result = (Truthy(R(B)) == bool(C)) ? R(B) : <else>
			// where <else> is either a register copy (arithOp==0,
			// arithB=src reg) or a constant (arithOp==1, imm=baked).
			testVal := c.host.GetReg(int32(src.reg))
			match := value.Truthy(value.Value(testVal)) == (src.upval != 0)
			if match {
				val = testVal
			} else {
				if src.arithOp == 0 {
					val = c.host.GetReg(int32(src.arithB))
				} else {
					val = src.imm
				}
			}
		case slotKindNot:
			// Pure Go: never raises, no host helper round-trip.
			operand := value.Value(c.host.GetReg(int32(src.reg)))
			val = uint64(value.BoolValue(!value.Truthy(operand)))
		}
		c.host.SetReg(int32(c.retA)+int32(i), val)
	}

	if st := c.host.DoReturn(int32(base), int32(c.retPC), int32(c.retA), int32(c.retB)); st != 0 {
		return st
	}
	_ = stack
	return 0
}

// PendingErr satisfies bridge.GibbousCode. PJ10a has no error paths
// (head ops are pure value materialisation), so the answer is always nil.
func (c *PerOpCode) PendingErr() error { return nil }

// runEffect dispatches a single sideEffect record. Returns:
//   - 0 on success
//   - 1 on host-side error
//   - -1 as a sentinel for "Lua tail call done; frame already popped".
//     The caller (Run) translates -1 → return 0 without DoReturn.
func (c *PerOpCode) runEffect(base uint32, se sideEffect, stack []uint64) int32 {
	switch se.kind {
	case sideEffectSetUpval:
		c.host.SetUpvalFromReg(int32(base), int32(se.a), int32(se.b))
	case sideEffectLoadNil:
		for r := int32(se.a); r <= int32(se.b); r++ {
			c.host.SetReg(r, uint64(value.Nil))
		}
	case sideEffectMove:
		c.host.SetReg(int32(se.a), c.host.GetReg(int32(se.b)))
	case sideEffectLoadK:
		c.host.SetReg(int32(se.a), se.imm)
	case sideEffectSetTable:
		b := int32(se.imm >> 16)
		cc := int32(se.imm & 0xffff)
		if st := c.host.SetTable(int32(base), 0, int32(se.a), b, cc); st != 0 {
			return st
		}
	case sideEffectSetGlobal:
		if st := c.host.DoSetGlobal(int32(base), 0, int32(se.a), int32(se.imm)); st != 0 {
			return st
		}
	case sideEffectCall:
		if st := c.host.CallBaseline(
			int32(base),
			int32(se.imm),
			int32(se.a),
			int32(se.b),
			int32(se.c),
		); st != 0 {
			return st
		}
	case sideEffectTailCall:
		st := c.host.TailCall(
			int32(base),
			int32(se.imm),
			int32(se.a),
			int32(se.b),
			int32(se.c),
		)
		switch st {
		case 0:
			_ = stack
			return -1 // sentinel: Lua tail call complete, frame popped
		case 2:
			// Host tail call: results in R(callA..). Fall through.
		default:
			return 1
		}
	case sideEffectSelf:
		selfPC := int32(se.imm >> 32)
		rkC := int32(se.imm & 0xffffffff)
		if st := c.host.Self(int32(base), selfPC, int32(se.a), int32(se.b), rkC); st != 0 {
			return st
		}
	case sideEffectSetList:
		if st := c.host.SetList(int32(base), 0, int32(se.a), int32(se.b), int32(se.c)); st != 0 {
			return st
		}
	case sideEffectArith:
		// Imm layout: op<<48 | B<<32 | C<<16 | pc.
		op := uint8(se.imm >> 48)
		b := uint16(se.imm >> 32)
		cc := uint16(se.imm >> 16)
		pc := uint16(se.imm)
		if st := c.host.Arith(
			int32(base),
			int32(pc),
			int32(op),
			int32(b),
			int32(cc),
			int32(se.a),
		); st != 0 {
			return st
		}
	case sideEffectClosure:
		// CLOSURE A Bx: imm = pc<<32 | bx. host.Closure reads the
		// inner Proto's pseudo upvalue-bind instructions via ci.pc,
		// makes the closure value, and stores into R(A).
		closPC := int32(se.imm >> 32)
		bx := int32(se.imm & 0xffffffff)
		if st := c.host.Closure(int32(base), closPC, int32(se.a), bx); st != 0 {
			return st
		}
	case sideEffectClose:
		// CLOSE A: close all upvalues with stack index >= base+A.
		if st := c.host.Close(int32(base), 0, int32(se.a)); st != 0 {
			return st
		}
	case sideEffectGetUpvalScratch:
		// R(A) := U(B); never raises.
		c.host.SetReg(int32(se.a), c.host.GetUpval(int32(base), int32(se.b)))
	case sideEffectGetGlobalScratch:
		// R(A) := Globals[K(Bx)] via host.DoGetGlobal; may raise.
		if st := c.host.DoGetGlobal(int32(base), 0, int32(se.a), int32(se.imm)); st != 0 {
			return st
		}
	case sideEffectGetTableScratch:
		// R(A) := R(B)[RK(C)] via host.GetTable; may raise.
		if st := c.host.GetTable(int32(base), 0, int32(se.a), int32(se.b), int32(se.imm)); st != 0 {
			return st
		}
	case sideEffectNewTableScratch:
		// R(A) := new table via host.NewTable; never raises.
		if st := c.host.NewTable(int32(base), 0, int32(se.a), int32(se.b), int32(se.c)); st != 0 {
			return st
		}
	}
	return 0
}

// runTForLoop dispatches the generic-for (TFORLOOP) iteration. Each
// iteration calls host.TForLoop(A, C) which:
//   - Invokes R(A)(R(A+1), R(A+2)).
//   - On result[0] != nil: writes R(A+3..A+2+C) := results; R(A+2) :=
//     result[0]; returns the (possibly refreshed) base offset.
//   - On result[0] == nil: returns -2 (exit).
//   - On error: returns -1.
//
// After a successful iteration we replay c.bodyEffects, which read
// R(A+3..A+2+C) as the visible loop vars.
func (c *PerOpCode) runTForLoop(base uint32) int32 {
	const maxIter = 1 << 28
	for iter := 0; iter < maxIter; iter++ {
		ret := c.host.TForLoop(int32(base), int32(c.tforLoopPC), int32(c.tforLoopA), int32(c.tforLoopC))
		switch {
		case ret == -1:
			return 1
		case ret == -2:
			return 0
		case ret < -2:
			return 1 // defensive
		}
		// ret >= 0: base may have been refreshed (growStack). For the
		// Go-side replay we don't actually move arenaBase pointers — we
		// always call host.GetReg/SetReg which re-derive addresses each
		// time, so the only thing we need is to keep going.
		for _, be := range c.bodyEffects {
			if st := c.runEffect(base, be, nil); st != 0 {
				if st == -1 {
					return 1
				}
				return st
			}
		}
	}
	return 1
}

// runForLoop dispatches the FORPREP + FORLOOP iteration block. The loop
// regs R(forLoopA..+2) hold init/limit/step; host.ForPrep validates and
// pre-decrements init. Each iteration:
//   - Step:   R(forLoopA) += R(forLoopA+2)
//   - Check:  if step > 0 then R(A) <= limit else R(A) >= limit
//   - Visible: R(forLoopA+3) := R(forLoopA) (the user's `i`)
//   - Body:   replay c.bodyEffects (arith ops accumulate into scratch regs)
//
// On exit, scratch accumulators carry their final value into the head-op
// replay (which has been redirected to slotKindReg for any slot whose
// last writer was inside the body).
func (c *PerOpCode) runForLoop(base uint32) int32 {
	if st := c.host.ForPrep(int32(base), int32(c.forLoopPC), int32(c.forLoopA)); st != 0 {
		return st
	}
	// Cap iterations as a safety net against runaway loops in unexpected
	// shapes (limit/step combinations that don't terminate). The official
	// Lua interpreter handles this via plain float comparison; we mirror
	// that with the same arithmetic — but a 256M iteration cap keeps
	// pathological cases bounded.
	const maxIter = 1 << 28
	for iter := 0; iter < maxIter; iter++ {
		idx := value.AsNumber(value.Value(c.host.GetReg(int32(c.forLoopA))))
		step := value.AsNumber(value.Value(c.host.GetReg(int32(c.forLoopA) + 2)))
		idx += step
		c.host.SetReg(int32(c.forLoopA), uint64(value.NumberValue(idx)))
		limit := value.AsNumber(value.Value(c.host.GetReg(int32(c.forLoopA) + 1)))
		var cont bool
		if step > 0 {
			cont = idx <= limit
		} else {
			cont = idx >= limit
		}
		if !cont {
			return 0
		}
		c.host.SetReg(int32(c.forLoopA)+3, uint64(value.NumberValue(idx)))
		for _, be := range c.bodyEffects {
			if st := c.runEffect(base, be, nil); st != 0 {
				if st == -1 {
					return 1 // tail call inside loop body — unexpected
				}
				return st
			}
		}
	}
	return 1 // runaway loop — treat as error
}

// Slot satisfies bridge.GibbousCode. PerOpCode is amd64-native, not a
// wasm module, so there's no shared env.table slot — always (0, false).
func (c *PerOpCode) Slot() (uint32, bool) { return 0, false }

// Dispose releases the mmap segment. Safe under concurrent Run in
// multi-State setups: CodePage.Dispose flips a disposed flag (blocking
// further Enter) and the refcount protocol defers the actual unix.Munmap
// until the last active Run's Exit. See amd64/codepage_linux.go.
func (c *PerOpCode) Dispose() error {
	if c == nil || c.codePage == nil {
		return nil
	}
	err := c.codePage.Dispose()
	c.codePage = nil
	return err
}

// TranslateProto is the production entry: takes a Proto and a P4HostState,
// returns a bridge.GibbousCode the bridge can install. Errors when the
// Proto is outside the supported constant-tuple shape; the caller should
// have run AnalyzeShape first so a non-nil error here is genuinely
// unexpected (and bridge will surface it as
// bridge.CompileErrUnsupportedOpcodeShape).
func TranslateProto(proto *bytecode.Proto, host jit.P4HostState) (bridge.GibbousCode, error) {
	if host == nil {
		return nil, errSpikeNilHost
	}
	info := AnalyzeShape(proto)
	if !info.ok {
		return nil, errors.New("peroptranslator: Proto shape not supported by AnalyzeShape")
	}

	// Emit a tiny stub: `xor eax,eax; ret` (3 bytes total). The Run
	// path does the actual R(A+i) writes via host.SetReg, so the stub
	// itself produces no value — it only validates that the mmap +
	// trampoline path is exercised on every Run.
	buf := []byte{0x31, 0xC0} // xor eax, eax
	buf = jitamd64.EmitRet(buf)

	page, err := jitamd64.MmapCode(buf)
	if err != nil {
		return nil, err
	}
	return &PerOpCode{
		proto:         proto,
		codePage:      page,
		jitCtx:        jit.NewJITContext(),
		host:          host,
		sources:       info.sources,
		sideEffects:   info.sideEffects,
		retA:          info.retA,
		retB:          info.retB,
		retPC:         info.retPC,
		isTailCall:    info.isTailCall,
		forLoopValid:  info.forLoopValid,
		forLoopA:      info.forLoopA,
		forLoopPC:     info.forLoopPC,
		forLoopAfter:  info.forLoopAfter,
		bodyEffects:   info.bodyEffects,
		tforLoopValid: info.tforLoopValid,
		tforLoopA:     info.tforLoopA,
		tforLoopC:     info.tforLoopC,
		tforLoopPC:    info.tforLoopPC,
	}, nil
}

// init registers TranslateProto + a Shape analyser into the jit main
// package. This is the "PJ10 enabled" switch: import this sub-package
// (e.g. with `import _ ".../peroptranslator"`) and the hooks become
// non-nil, the jit Compiler's SupportsAllOpcodes / Compile fall-through
// gain the PJ10 supported subset.
//
// Native path (as of the 2026-07-01 round in
// [[2026-07-01-p4-pj10-native-round]]): TranslateProtoNative is wired
// as the first preference here — when AnalyzeNative accepts a Proto
// its emit output is used directly; on any failure we fall back to
// TranslateProto (head-op replay) so behaviour stays identical for
// unsupported shapes. See `opSupported` godoc in translator_native.go
// for the exact mmap-safe inline op subset.
func init() {
	jit.RegisterPerOpTranslator(
		func(proto *bytecode.Proto, host jit.P4HostState) (bridge.GibbousCode, error) {
			if AnalyzeNative(proto) {
				code, err := TranslateProtoNative(proto, host)
				if err == nil {
					return code, nil
				}
			}
			return TranslateProto(proto, host)
		},
		func(proto *bytecode.Proto) bool {
			return AnalyzeShape(proto).ok || AnalyzeNative(proto)
		},
	)
	jit.RegisterPerOpNativeAnalyzer(PreferNative)
}
