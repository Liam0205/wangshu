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

	// retA / retB / retPC are baked at Compile time so Run can hand them
	// to host.DoReturn without re-parsing the Proto.
	retA  uint8
	retB  uint8
	retPC uint8
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

	c.jitCtx.SetArenaBase(c.host.ArenaBaseAddr())
	c.jitCtx.SetValueStackBase(c.host.ValueStackBaseAddr(int32(base)))
	c.jitCtx.SetCIDepthAddr(c.host.CIDepthHostAddr())
	c.jitCtx.SetCISegBaseAddr(c.host.CISegBaseHostAddr())
	c.jitCtx.SetTopAddr(c.host.TopHostAddr())

	jitCtxAddr := uintptr(unsafe.Pointer(c.jitCtx))
	_ = jitamd64.CallJITFull(c.codePage.Addr(), jitCtxAddr) // returns 0

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

// Slot satisfies bridge.GibbousCode. PerOpCode is amd64-native, not a
// wasm module, so there's no shared env.table slot — always (0, false).
func (c *PerOpCode) Slot() (uint32, bool) { return 0, false }

// Dispose releases the mmap segment.
func (c *PerOpCode) Dispose() error {
	if c == nil || c.codePage == nil {
		return nil
	}
	err := c.codePage.Munmap()
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
		proto:    proto,
		codePage: page,
		jitCtx:   jit.NewJITContext(),
		host:     host,
		sources:  info.sources,
		retA:     info.retA,
		retB:     info.retB,
		retPC:    info.retPC,
	}, nil
}

// init registers TranslateProto + a Shape analyser into the jit main
// package. This is the "PJ10 enabled" switch: import this sub-package
// (e.g. with `import _ ".../peroptranslator"`) and the hooks become
// non-nil, the jit Compiler's SupportsAllOpcodes / Compile fall-through
// gain the PJ10 supported subset.
func init() {
	jit.RegisterPerOpTranslator(TranslateProto, func(proto *bytecode.Proto) bool {
		return AnalyzeShape(proto).ok
	})
}
