//go:build wangshu_p4

package jit

import (
	"errors"

	"github.com/Liam0205/wangshu/internal/bridge"
	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/value"
)

// p4Code implements the `bridge.GibbousCode` interface (`p2-bridge/05-p3-p4-interface.md`
// §6 + p4-method-jit/00-overview.md §1 boundary-table GibbousCode implementor row).
//
// **PJ2 wired-in version** (2026-06-25): p4Code actually holds a *CodePage (mmap
// segment) + jitContext + retA (target register number); Run jumps into the mmap
// segment via callJITFull to get RAX, then writes it back to the stack at base/8 + retA.
//
// **PJ2 wired-in scope**: only the single-BB shape "LOADK A K(0); RETURN A 1" is
// supported — this is the only Lua subset with no side effects, no helper, and no
// cross-layer call across the spike-gate ⊕ trampoline ⊕ emitter trio. Other shapes
// are rejected by SupportsAllOpcodes (per 06 §3.8 incremental-whitelist discipline
// + p4Code.Run's no-stack-mutation / no-helper-call contract).
//
// Correspondence with P3 *p3Code:
//   - P3 GibbousCode wraps a wazero CompiledModule + api.Function handle;
//   - P4 GibbousCode wraps an unsafe.Pointer native code segment (via
//     jitamd64.CodePage) plus jitContext plus compile-time-fixed retA info.
type p4Code struct {
	proto *bytecode.Proto

	// codePage is the mmap'd PROT_RX segment (after the W^X flip); holds the
	// segment until Dispose. The archCodePage type alias is routed by arch_*.go
	// (each of amd64/arm64 has its own CodePage).
	codePage *archCodePage

	// jitCtx is this compilation product's JIT execution context (per-Proto singleton).
	jitCtx *JITContext

	// retA is the RETURN instruction's A register number — after p4Code.Run's mmap
	// segment returns RAX, it writes RAX to the stack[base/8 + retA] slot (= R(retA)
	// NaN-box).
	retA uint8

	// retB is the RETURN instruction's B field (B-1 = number of return values).
	//   - retB = 1: 0 return values (empty RETURN); Run writes no stack slot
	//   - retB = 2: 1 return value; Run writes RAX to stack[base/8 + retA]
	retB uint8

	// retPC is the RETURN instruction's pc (0-based) — used by DoReturn to
	// materialize ci.savedPC.
	retPC uint8

	// writeRetA marks whether RAX should be written to R(retA) after the mmap
	// segment executes:
	//   - true (LOADK/LOADBOOL/LOADNIL): the mmap segment computes a new value,
	//     the slot must be written
	//   - false (leading RETURN A B=2, e.g. `function(x) return x end`): R(retA)
	//     is already the parameter value (written by the caller), and must not be
	//     overwritten by the dummy RAX returned from the mmap segment
	writeRetA bool

	// preludeOp is the prep opcode before RETURN (if any, used in P4 PJ7's
	// simplified shapes to call a host helper to fetch a value then SetReg-write
	// R(retA), or to call host.Arith to perform arithmetic):
	//   - 0 (default): no prelude, LOADK/LOADBOOL/LOADNIL already computed the
	//     value at compile time
	//   - bytecode.GETUPVAL: Run calls host.GetUpval(retA, preludeArg) to fetch
	//     the value, then SetReg(retA, val) writes the slot. This is a simplified
	//     substitute for "mmap segment calls host" — it moves the host call out of
	//     the mmap segment into Go-side Run.
	//   - bytecode.ADD/SUB/MUL/DIV/MOD/POW: Run calls host.Arith(base, pc, op,
	//     b=preludeArg, c=preludeC, a=retA) to perform the arithmetic + write
	//     R(retA). The helper handles RK decode + double-dispatch + coercion +
	//     metamethods; on return 1 the ERR propagates (picked up by enterGibbous
	//     via pendingErr).
	preludeOp uint8

	// preludeArg is the prelude opcode's B field (GETUPVAL's upvalue index 0-255,
	// or the arithmetic family's B field with RK encoding 0-511, or GETGLOBAL/
	// SETGLOBAL's Bx field 0-262143 — hence widened to uint32).
	preludeArg uint32

	// preludeC is the arithmetic-family prelude's C field (RK encoding 0-511).
	// Unused by the GETUPVAL shape.
	preludeC uint16

	// cmpA is the A field of the comparison-folding shape (EQ/LT/LE) (0 or 1, used
	// to fold into `BoolValue(packed.bit0 == cmpA)`). Unused by other shapes.
	cmpA uint8

	// chainOp/chainB/chainC are the second segment's op + B + C for the two-stage
	// arithmetic chain shape (MUL+ADD+RETURN etc.). 0 = no chain; when non-zero
	// Run calls host.Arith twice in sequence.
	chainOp uint8
	chainB  uint16
	chainC  uint16

	// host is the injected P4HostState (copied from *Compiler): held per-p4Code,
	// no concurrent writes (written once at Compile time, read-only at Run time)
	// — V18 -race friendly.
	host P4HostState

	// useSpec marks whether this p4Code uses the PJ2 speculation template (per
	// docs/design/p4-method-jit/03-speculation-ic.md §2 IsNumber×2 speculation
	// template). Currently only enabled for the ADD A B C + RETURN A 2 shape; on
	// speculation failure RAX = deoptCode, and after detecting it Run falls back
	// to the host.Arith slow path.
	useSpec bool

	// specDeoptCode is the constant burned into the PJ2 speculation template's
	// deopt block (per 04-osr-deopt.md exit reason code). When Run detects the
	// segment returned RAX == specDeoptCode, it takes the slow path.
	// Chosen as 0xFF...FFFE000 (a NaN-box non-number pattern that is not any legal
	// Lua value) to avoid false positives.
	specDeoptCode uint64

	// PJ3 FORLOOP reg-limit deopt path flags:
	//   - forLoopDeopt = true: this p4Code is the PJ3 reg-limit FORLOOP shape;
	//     when Run detects raxSpec==deoptCode it calls host.ForPrep rather than
	//     host.Arith (byte-equal interpreter raising `'for' limit must be a number`
	//     etc.)
	//   - forLoopA: FORPREP/FORLOOP's A field (R(A)..R(A+2) = init/limit/step
	//     three slots, host.ForPrep uses this field to locate the slots)
	//   - forLoopLimitReg: the R(forLoopLimitReg) slot number expected by the
	//     reg-limit template
	//   - forLoopUpvalIdx: the upvalue-limit sub-shape's upval idx + 1 (0 = does
	//     not go through upval; >0 = Run first does host.GetUpval(idx-1) + SetReg
	//     to write the limit slot)
	forLoopDeopt    bool
	forLoopA        uint8
	forLoopLimitReg uint8
	forLoopUpvalIdx uint8

	// PJ4 IC ArrayHit path flags:
	//   - icArrayHit = true: when Run detects raxSpec==deoptCode it calls
	//     host.GetTable (byte-equal interpreter IC + hash + __index)
	icArrayHit bool

	// PJ4 IC SETTABLE ArrayHit path flags:
	//   - icSetArrayHit = true: when Run detects raxSpec==deoptCode it calls
	//     host.SetTable (byte-equal interpreter icSetTable + __newindex). The
	//     setter shape has retB=1 with no R(A) write.
	icSetArrayHit bool

	// PJ5 CALL void path flags (per docs/design/p4-method-jit/05-system-pipeline.md
	// §4.3 + 06-backends.md §3.5):
	//   - isCallVoid = true: Run's prelude path calls host.CallBaseline to perform
	//     the baseline CALL (byte-equal P1 doCall dispatch); followed by DoReturn
	//     to pop the frame.
	//   - isCallUpval = true: shape B (GETUPVAL+CALL+RETURN void), Run's prelude
	//     path calls host.GetUpval to fetch the callee function; false means shape
	//     A (MOVE+CALL+RETURN void), Run calls host.GetReg to fetch the callee
	//   - callA / callB / callC: CALL A B C three fields (passed to host.CallBaseline)
	//   - callArgCount: 0/1/2 args
	//   - callArg1IsK: for the 1-arg shape, true=LOADK / false=MOVE reg; for the
	//     2-K-arg shape always true
	//   - callArg1K: the first K for the 1-K or 2-K arg shape
	//   - callArg1RegSrc: the MOVE.B source reg number for the 1-reg-arg shape
	//   - callArg2K: the second K for the 2-K-arg shape
	//
	// Reuses the preludeArg field: for shape A = MOVE.B (source reg); for shape B
	// = GETUPVAL.B (upvalue index)
	isCallVoid     bool
	isCallUpval    bool
	callA          uint8
	callB          uint8
	callC          uint8
	callArgCount   uint8
	callMultiRet   uint8 // N=0/1 existing (setter/getter 1 ret); N>=2 means N-return-value getter — Run copies N MOVEs
	callArg1IsK    bool
	callArg1K      uint64
	callArg1RegSrc uint8
	callArg2IsK    bool
	callArg2K      uint64
	callArg2RegSrc uint8
	callArg3IsK    bool
	callArg3K      uint64
	callArg3RegSrc uint8
	callArg4IsK    bool
	callArg4K      uint64
	callArg4RegSrc uint8
	callArg5IsK    bool
	callArg5K      uint64
	callArg5RegSrc uint8
	callArg6IsK    bool
	callArg6K      uint64
	callArg6RegSrc uint8
	callArg7IsK    bool
	callArg7K      uint64
	callArg7RegSrc uint8

	// PJ5 TAILCALL path flags (per docs/design/p4-method-jit/05-system-pipeline.md §4.3):
	//   - isTailCall = true: Run's prelude path calls host.TailCall's three-state branch:
	//     0 = Lua tail complete (this frame already popped, skip DoReturn and return 0)
	//     1 = ERR
	//     2 = host tail complete (result in R(callA).., Run emits a dead RETURN B=0 to-top and goes through DoReturn)
	//   - reuses the isCallUpval / callA / callB / callC / callArgCount / callArg1* / callArg2K
	//     fields (8 sub-shapes TA0/TB0/TA1K/TB1K/TA1R/TB1R/TA2K/TB2K)
	//   - reuses preludeArg = MOVE.B (shape TA*) / GETUPVAL.B (shape TB*)
	isTailCall bool

	// PJ5 SELF method call inline path flags (per
	// docs/design/p4-method-jit/05-system-pipeline.md §4.3 + 09 §9.17):
	//   - isSelfCall = true: Run's prelude path first calls host.Self to fetch the
	//     method into R(callA) + load self into R(callA+1), then calls
	//     host.CallBaseline / TailCall to perform the byte-equal P1 doCall dispatch.
	//     SELF + CALL and SELF + TAILCALL share this field; the actual CALL/TAILCALL
	//     branch is gated by preludeOp + isCallVoid / isTailCall.
	//   - selfCallA: SELF.A = method result register (same as callA)
	//   - selfMethodRK: SELF.C field (RK method-name constant index 0-511)
	//   - selfRecvSrcReg + selfRecvIsUpval: recv source — true=upvalue idx / false=reg
	//
	// **Relationship with isCallVoid / isTailCall**: isSelfCall is an additive
	// attribute — Run's CALL/TAILCALL switch case adds one extra host.Self
	// preprocessing call; other paths are unchanged.
	isSelfCall      bool
	selfCallA       uint8
	selfMethodRK    uint16
	selfRecvSrcReg  uint8
	selfRecvIsUpval bool

	// PJ5 SELF + CALL spec template wiring (per §9.10 reusing PJ4 EmitSelfNodeHit):
	//   - useSpecSelfCall = true: the SELF segment runs the EmitSelfNodeHit
	//     byte-level template via callJITSpec (IC NodeHit guard + NodeVal store
	//     R(A)=method), skipping host.Self; on failure raxSpec==specDeoptCode it
	//     falls back to host.Self.
	//   - Run preprocessing: first host.GetReg/GetUpval + SetReg to load
	//     R(callA)=recv (simulating MOVE/GETUPVAL, since the spec segment reads the
	//     receiver from R(callA) at byte level), then callJITSpec;
	//     success → method already stored to R(callA), self already stored to R(callA+1);
	//     failure → R(callA+1) already has recv stored (same step as the P1 SELF case),
	//     fall back to host.Self which overwrites it again. Then load args +
	//     host.CallBaseline + host.DoReturn.
	//   - reuses the useSpec + specDeoptCode fields (spec segment deopt code).
	useSpecSelfCall bool

	// PJ5 Option B Spike 1 frame-build inlining (per §9.20):
	//   - useFrameInline = true: Run takes runSpecSelfCallInline instead of
	//     host.CallBaseline; the mmap segment byte-level inlines enterLuaFrame +
	//     helper call executeFrom + popCallInfo (per §9.20 Spike 1 route).
	//   - Gating (per §9.20.4): callee.NumParams=0 + !IsVararg + !NeedsArg +
	//     MaxStack≤32 + caller-callee Proto known at compile time + IC NodeHit + FBSelfMono.
	//   - Failure (callee Proto fails gating / archSupportsFrameInline=false /
	//     segment deopts) → falls back to useSpecSelfCall (SELF segment byte-level +
	//     host.CallBaseline).
	//   - **Spike 1 stage not yet wired in**: this field + Run's runSpecSelfCallInline +
	//     Compile's compileSpecSelfCallInline are still skeletons (the emit template
	//     is already byte-level implemented, amd64 120B / arm64 164B); the helper
	//     call ABI + e2e prove-the-path remain as a batch of engineering work.
	useFrameInline bool

	// frameInlineResumeOff is the byte offset within the mmap segment of the PJ5
	// Option B Spike 1 frame-build inlining resume entry (per §9.20.9 (5) Go-side
	// dispatcher protocol):
	//   - when useFrameInline=true, compileSpecSelfCall records = len(buf) after
	//     emitting BuildVoid0Arg + ExitHelperRequest, used as the segment offset of
	//     PopVoid0Arg
	//   - after the dispatcher handles the callee, it uses codePageAddr +
	//     frameInlineResumeOff to compute the absolute address of the resume entry,
	//     and the trampoline re-CALLs into the mmap segment to continue
	//   - when useFrameInline=false this field is always 0 (the useFrameInline
	//     branch is not entered)
	//
	// Injected at Run entry via jitCtx.SetCodePageAddr + SetResumeOff (per §9.20.9
	// commit-5 wired-in batch); fixed at compile time as a p4Code field, injected
	// into jitCtx at Run time.
	frameInlineResumeOff uint32
}

// Proto back-pointer (trampoline validation).
func (c *p4Code) Proto() *bytecode.Proto {
	return c.proto
}

// Run is the crescent→gibbous cross-layer entry point (P4 PJ7 wired-in).
//
// **Calling contract** (per gibbous_host.go::enterGibbous):
//   - param stack: the P3 reuse stack ([]uint64, len ≥ 1); P4 neither reads nor
//     writes it — the value stack proper is operated via host.SetReg (the P3
//     wazero CallWithStack 1-slot buffer protocol is incompatible with P4, and
//     the buffer provided by gibbous_host.go::gibbousStack cannot be used as a
//     real value stack);
//   - param base: this frame's R0 byte offset (stackSegByte + base*8), passed to
//     host.DoReturn to materialize ci.savedPC + handle nresults (host internally
//     computes real slots via thread.cur.base, unrelated to the base byte offset);
//   - return status: 0=OK / 1=ERR (P4 never returns 2=DEOPT, since PJ7 has no
//     speculation guard).
//
// **Execution flow**:
//  1. callJITFull jumps into the mmap segment (the segment only runs mov rax, value; ret), gets RAX;
//  2. writeRetA=true (LOADK/LOADBOOL/LOADNIL): via host.SetReg(retA, RAX) write
//     the constant value computed by the mmap segment into R(retA);
//  3. preludeOp non-zero (GETUPVAL): fetch the value via a host helper then
//     SetReg(retA, val);
//  4. host.DoReturn(base, retPC, retA, retB): move the result to funcIdx per
//     nresults + pop this frame's CallInfo + restore caller top.
//
// **Wired-in path condition**: hostState != nil (injected by wireP4 via
// *Compiler.SetHostState). host==nil only happens on the jit-package-internal
// prove-the-path unit-test path (the unit test does not construct a full *State,
// it only verifies the value-correct path is reached). The bridge main path's
// wireP4 always injects via SetHostState, and Run always pops the frame via
// host.DoReturn.
//
// **PJ7 wired-in subset** (per analyzeShape):
//   - length-1 RETURN A B (empty function / identity shape)
//   - LOADK/LOADBOOL/LOADNIL + RETURN A 2 (constant return, writeRetA=true)
//   - leading RETURN A 2 (luac-optimized shape: identity function)
//   - MOVE A B + RETURN A 2 (retA=B skips the relay)
//   - GETUPVAL A B + RETURN A 2 (preludeOp=GETUPVAL)
//   - ADD/SUB/MUL/DIV/MOD/POW A B C + RETURN A 2 (preludeOp=arithmetic op, Run
//     calls the host.Arith helper, can propagate ERR)
//   - length-3 luac main-chunk trailing redundancy (LOADK + RETURN + RETURN dead)
func (c *p4Code) Run(stack []uint64, base uint32) int32 {
	if c.codePage == nil || c.jitCtx == nil {
		stack[0] = 1
		return 1
	}

	// Refcount acquire: keep the mmap segment alive for the duration of this
	// Run, closing the multi-State concurrent Dispose vs Run UAF window.
	// Enter fails only if the segment has already been disposed; in that case
	// the Proto is no longer eligible for JIT execution and the caller must
	// fall back (return error, upstream re-checks tier).
	if !c.codePage.Enter() {
		stack[0] = 1
		return 1
	}
	defer c.codePage.Exit()

	// **PJ2 full-wiring preparation**: at Run entry, compute the arena base +
	// valueStackBase and load them into jitContext, so PJ2+ byte-level arithmetic
	// codegen can read these two fields via r15+offset (the mmap segment addresses
	// value-stack slots directly, skipping the host-helper round-trip). In the
	// current PJ7 simplified shape the mmap segment is a dummy (mov+ret does not
	// read r15), so the loaded values are unused — but loading them is itself
	// correct with no side effects, and establishes the Go-side starting point of
	// the PJ2 full-wiring path (per 05 §5 arena base reload protocol: recomputed
	// on every Run entry, never cached, grow-safe).
	var vsBaseAddr uintptr
	if c.host != nil && c.jitCtx != nil {
		// A1 optimisation: single host call fills all five addr fields
		// with one arena.Words() lookup + one unsafe.Pointer take,
		// instead of five per-getter round-trips. Same arena-grow
		// reload protocol (per section 05 §5): populated on every
		// Run entry, values become stale once we leave the JIT world.
		c.host.RefreshJitCtxAddrs(c.jitCtx, int32(base))
		vsBaseAddr = c.jitCtx.ValueStackBase()
	}

	jitCtxAddr := jitContextAddr(c.jitCtx)

	// PJ2 speculation template path (when useSpec=true, the ADD A B C shape takes
	// callJITSpec + deopt detection; on failure it falls back to the host.Arith
	// slow path)
	//
	// **mock host fallback**: when host.ArenaBaseAddr returns 0 (the unit-test mock
	// has no real arena), skip the spec path and go straight to the host helper —
	// avoiding the segment reading [rbx+0] = reading address 0 SIGSEGV.
	if c.useSpec && c.host != nil && vsBaseAddr != 0 {
		// **PJ5 SELF + CALL spec template standalone path** (per §9.10 reusing
		// EmitSelfNodeHit + §9.17 upgrade): the SELF segment takes the byte-level
		// template (skipping host.Self) + the CALL segment takes host.CallBaseline.
		// A self-contained sub-path — not conflated with the PJ2/PJ3/PJ4 spec
		// splits below.
		if c.useSpecSelfCall {
			return c.runSpecSelfCall(int32(base), jitCtxAddr, vsBaseAddr)
		}
		// **upvalue-limit preprocessing**: the reg-limit template expects
		// R(forLoopLimitReg) to be a number. For the upval shape, Run first calls
		// host.GetUpval(idx-1) + SetReg to write the limit slot, then the template
		// byte-level inlines the reg-limit path (the IsNumber guard still applies —
		// if the upval is not a number, the guard fails → host.ForPrep raises
		// byte-equal P1).
		if c.forLoopUpvalIdx > 0 {
			upvalVal := c.host.GetUpval(int32(base), int32(c.forLoopUpvalIdx-1))
			c.host.SetReg(int32(c.forLoopLimitReg), upvalVal)
		}
		raxSpec := archCallJITSpec(c.codePage.Addr(), jitCtxAddr, vsBaseAddr)
		if raxSpec == c.specDeoptCode {
			// **PJ4 IC ArrayHit deopt** path: call host.GetTable byte-equal P1
			// (via IC + hash + __index metamethod chain; preludeOp/Arg/C already
			// hold the GETTABLE info)
			if c.icArrayHit {
				specPC := int32(c.retPC) - 1
				st := c.host.GetTable(int32(base), specPC, int32(c.retA),
					int32(c.preludeArg), int32(c.preludeC))
				if st != 0 {
					return st
				}
				_ = stack
				c.host.DoReturn(int32(base), int32(c.retPC), int32(c.retA), int32(c.retB))
				return 0
			}

			// **PJ4 IC SETTABLE ArrayHit deopt** path: call host.SetTable
			// byte-equal P1 (via icSetTable + __newindex metamethod chain). The
			// setter shape has retB=1 with no R(A) write, so DoReturn does not read R(A).
			if c.icSetArrayHit {
				specPC := int32(c.retPC) - 1
				st := c.host.SetTable(int32(base), specPC, int32(c.retA),
					int32(c.preludeArg), int32(c.preludeC))
				if st != 0 {
					return st
				}
				_ = stack
				c.host.DoReturn(int32(base), int32(c.retPC), int32(c.retA), int32(c.retB))
				return 0
			}

			// **PJ3 FORLOOP reg-limit deopt** path split: call host.ForPrep
			// rather than host.Arith (byte-equal interpreter raising non-number error)
			if c.forLoopDeopt {
				st := c.host.ForPrep(int32(base), int32(c.retPC)-2 /* FORPREP pc=3 */, int32(c.forLoopA))
				if st != 0 {
					return st
				}
				// host.ForPrep has set the three number slots + pre-decrement, but
				// P4 does not wire host.ForLoop (it does not implement an in-segment
				// iterator to run the remaining loop — that would need a full
				// doForLoop helper).
				// Simplification: after host.ForPrep succeeds, DoReturn directly
				// (equivalent to the P4 frame running only FORPREP coercion, with
				// the actual loop in the P1 interpreter — TODO full host.ForLoop).
				// **This simplification makes reg-limit FORLOOP byte-equal but
				// performance-equivalent to P1 on the deopt path** (no more
				// speedup after deopt) — consistent with design 04 §5 deopt
				// protocol (speculation failure falls back to the interpreter).
				_ = stack
				c.host.DoReturn(int32(base), int32(c.retPC), int32(c.retA), int32(c.retB))
				return 0
			}
			// chain-shape preludePC computation: op1's real pc = retPC - 2 (two
			// cases: ordinary single op retPC = 1, chain retPC = 2)
			specPC := int32(c.retPC) - 1
			if c.chainOp != 0 {
				specPC = int32(c.retPC) - 2
			}
			// speculation failure → fall back to the host.Arith slow path (byte-equal interpreter)
			st := c.host.Arith(int32(base), specPC, int32(c.preludeOp),
				int32(c.preludeArg), int32(c.preludeC), int32(c.retA))
			if st != 0 {
				return st
			}
			// **chain spec deopt path**: on chain-template deopt only op1 ran
			// (host.Arith above); op2 must also run to be byte-equal with the
			// interpreter. chainB was fixed = retA in analyzeArithChainForm (the
			// intermediate-value link), and op2's actual pc = specPC + 1 (one past op1).
			if c.chainOp != 0 {
				st2 := c.host.Arith(int32(base), specPC+1,
					int32(c.chainOp), int32(c.chainB), int32(c.chainC),
					int32(c.retA))
				if st2 != 0 {
					return st2
				}
			}
		}
		// OK path (both fast path and deopt slow path have written R(retA))
		_ = stack
		c.host.DoReturn(int32(base), int32(c.retPC), int32(c.retA), int32(c.retB))
		return 0
	}

	rax := archCallJITFull(c.codePage.Addr(), jitCtxAddr)

	// Write R(retA) = RAX (only when writeRetA=true and retB ≥ 2).
	if c.writeRetA && c.retB >= 2 && c.host != nil {
		c.host.SetReg(int32(c.retA), rax)
	}

	// PJ7 prelude opcode handling (host-call shapes such as GETUPVAL / ADD-POW /
	// UNM / LEN / NEWTABLE / GETTABLE etc.): the mmap segment does not call host
	// (avoiding full trampoline complexity), and instead calls on the Go side
	// inside Run.
	//
	// **pc argument convention** (per gibbous_host.go::Arith/Unm/Len/NewTable/GetTable
	// each helper): pc is the "**index of the executed opcode itself**" — inside
	// the helper `ci.pc = pc + 1` replicates the interpreter main loop's "ci.pc
	// already ++ after fetch" discipline, aligning with three downstream sites:
	// errWithName's `ci.pc-1==pc (failing op)` / annotateError's `LineInfo[ci.pc-1]`
	// / IC slot `proto.IC[pc]`.
	//
	// **two-stage chain shape** (retPC=2, op1 at pc 0, op2 at pc 1): the slow-path
	// op1 pc argument = 0, op2 pc argument = 1. In the chain shape the preludePC
	// computation retPC - 1 = 1 is off (it targets op2's position rather than
	// op1's) — in the chain shape op1's real pc = retPC - 2 = 0.
	//
	// **ordinary single-op shape** (retPC=1, prelude at pc 0): preludePC = retPC - 1 = 0.
	preludePC := int32(c.retPC) - 1
	if c.chainOp != 0 {
		// chain shape retPC=2, op1's real pc = retPC - 2 = 0
		preludePC = int32(c.retPC) - 2
	}
	// **retB guard split**: the previous unified `c.retB >= 2` guard only suits the
	// getter shape (R(A) writes the return value); the setter shape
	// (SETTABLE/SETGLOBAL 0 return values) with retB=1 also needs the prelude. Each
	// case self-checks retB as needed (getter checks >=2, setter does not check).
	if c.preludeOp != 0 && c.host != nil {
		switch c.preludeOp {
		case uint8(bytecode.GETUPVAL):
			if c.retB < 2 {
				break
			}
			val := c.host.GetUpval(int32(base), int32(c.preludeArg))
			c.host.SetReg(int32(c.retA), val)
		case uint8(bytecode.GETGLOBAL):
			if c.retB < 2 {
				break
			}
			// Global read: host.DoGetGlobal(base, pc, a, bx) — via icGetTable it
			// goes through IC + hash + __index metamethod chain (the `_G` table),
			// can raise.
			// Note: bx (the Bx field, 18-bit) is passed through via preludeArg (uint32).
			st := c.host.DoGetGlobal(int32(base), preludePC, int32(c.retA),
				int32(c.preludeArg))
			if st != 0 {
				return st
			}
		case uint8(bytecode.ADD), uint8(bytecode.SUB), uint8(bytecode.MUL),
			uint8(bytecode.DIV), uint8(bytecode.MOD), uint8(bytecode.POW):
			if c.retB < 2 {
				break
			}
			// Arithmetic family: call the host.Arith slow-path helper (byte-for-byte
			// isomorphic to the interpreter's doArith). The helper uses reg/RK to
			// fetch B/C + setReg to write R(A) + ci.pc=pc+1 to materialize the
			// failing-op index (used for errWithName line numbers + IC feedback slot
			// alignment).
			st := c.host.Arith(int32(base), preludePC, int32(c.preludeOp),
				int32(c.preludeArg), int32(c.preludeC), int32(c.retA))
			if st != 0 {
				// raise pending: the host side has set gibbousPendingErr,
				// enterGibbous picks it up and propagates (does not call DoReturn to
				// pop the frame; the ERR path does not go through RETURN).
				return st
			}
			// Two-stage arithmetic chain shape (MUL+ADD+RETURN etc.): call
			// host.Arith a second time, pc=preludePC+1 (chainOp comes after op1).
			// chainB is already retA (the chain input, written to R(retA) by op1).
			if c.chainOp != 0 {
				st2 := c.host.Arith(int32(base), preludePC+1, int32(c.chainOp),
					int32(c.chainB), int32(c.chainC), int32(c.retA))
				if st2 != 0 {
					return st2
				}
			}
		case uint8(bytecode.UNM):
			if c.retB < 2 {
				break
			}
			// Unary minus: host.Unm is byte-for-byte isomorphic to the interpreter's
			// UNM slow path (string coercion + __unm metamethod, can raise).
			st := c.host.Unm(int32(base), preludePC,
				int32(c.preludeArg), int32(c.retA))
			if st != 0 {
				return st
			}
		case uint8(bytecode.LEN):
			if c.retB < 2 {
				break
			}
			// Length operation: host.Len is byte-for-byte isomorphic to the
			// interpreter's LEN (string byte length / table border / table __len /
			// error on other types, can raise).
			st := c.host.Len(int32(base), preludePC,
				int32(c.preludeArg), int32(c.retA))
			if st != 0 {
				return st
			}
		case uint8(bytecode.NEWTABLE):
			if c.retB < 2 {
				break
			}
			// Table construction: host.NewTable(base, pc, a, b, c) — alloc +
			// safepoint all inside the helper; B/C are the Fb-encoded initial
			// array/hash segment size hints. Never raises (only Go runtime OOM can
			// crash, decoupled from this error protocol).
			c.host.NewTable(int32(base), preludePC, int32(c.retA),
				int32(c.preludeArg), int32(c.preludeC))
		case uint8(bytecode.GETTABLE):
			if c.retB < 2 {
				break
			}
			// Table read: host.GetTable(base, pc, a, b, c) — via IC + hash + __index
			// metamethod chain, can raise (attempt to index nil/string with key/...).
			// Note: pc is also used as the IC slot index (proto.IC[pc]) — it must be
			// preludePC to hit GETTABLE's private IC slot, not RETURN's empty slot
			// (semantic correctness).
			st := c.host.GetTable(int32(base), preludePC, int32(c.retA),
				int32(c.preludeArg), int32(c.preludeC))
			if st != 0 {
				return st
			}
		case uint8(bytecode.SETTABLE):
			// Table write: host.SetTable(base, pc, a, b, c) — via IC + hash +
			// __newindex metamethod chain, can raise. **The setter shape has retB=1
			// (0 return values)**, does not write R(A), needs no retB guard.
			st := c.host.SetTable(int32(base), preludePC, int32(c.retA),
				int32(c.preludeArg), int32(c.preludeC))
			if st != 0 {
				return st
			}
		case uint8(bytecode.SETGLOBAL):
			// Global write: host.DoSetGlobal(base, pc, a, bx) — via icSetTable it
			// goes through IC + the `_G` table + __newindex, can raise. Setter shape.
			st := c.host.DoSetGlobal(int32(base), preludePC, int32(c.retA),
				int32(c.preludeArg))
			if st != 0 {
				return st
			}
		case uint8(bytecode.SETUPVAL):
			// Upvalue write: host.SetUpvalFromReg(base, a, b) — reads R(a) + upvalSet
			// writes the upvalue. Never raises. Setter shape.
			c.host.SetUpvalFromReg(int32(base), int32(c.retA), int32(c.preludeArg))
		case uint8(bytecode.NOT):
			if c.retB < 2 {
				break
			}
			// Logical not: pure Truthy (no metamethod, no raise). Run directly reads
			// R(B) via host.GetReg + SetReg(A, BoolValue(!Truthy(...))).
			v := value.Value(c.host.GetReg(int32(c.preludeArg)))
			c.host.SetReg(int32(c.retA), uint64(value.BoolValue(!value.Truthy(v))))
		case uint8(bytecode.CALL):
			// PJ5 CALL void shape: `function(g) g() end` (shape A, isCallUpval=false)
			// or `local function noop()...end; function() noop() end` (shape B,
			// isCallUpval=true) — MOVE+CALL+RETURN void / GETUPVAL+CALL+RETURN void
			// (0-arg shape, callArgCount=0) or MOVE/GETUPVAL+LOADK+CALL+RETURN void
			// (1-K-arg shape, callArgCount=1).
			//
			// **pc argument**: CALL's own pc is computed from retPC-1 (0-arg shape
			// retPC=2, 1-arg shape retPC=3, CALL is always the instruction before
			// RETURN).
			//
			// **preprocessing — load the callee function into the R(callA) slot**:
			// the MOVE/GETUPVAL that luac emitted is a dummy in the mmap segment and
			// does not execute, so Run manually calls a host helper to do it.
			//   - shape A: host.GetReg(preludeArg=MOVE.B) + SetReg(callA)
			//   - shape B: host.GetUpval(base, preludeArg=GETUPVAL.B) + SetReg(callA)
			//
			// **1-arg-shape LOADK loading**: when callArgCount=1, Run does
			// host.SetReg(callA+1, callArg1K) to load the compile-time-burned K
			// constant into the argument slot.
			//
			// **SELF inline shape** (isSelfCall=true): after loading recv into
			// R(callA), first call host.Self to complete R(callA)=R(callA)[K_method]
			// + R(callA+1)=self; then load arguments starting at R(callA+2) (skipping
			// the self slot) — byte-equal to the interpreter's SELF + CALL inline
			// subset.
			callPC := int32(c.retPC) - 1
			var srcVal uint64
			if c.isCallUpval {
				srcVal = c.host.GetUpval(int32(base), int32(c.preludeArg))
			} else {
				srcVal = c.host.GetReg(int32(c.preludeArg))
			}
			c.host.SetReg(int32(c.callA), srcVal)
			// SELF inline preprocessing: host.Self completes method fetch + self loading
			if c.isSelfCall {
				// pc argument: SELF's own pc. CALL is at retPC-1, SELF is one
				// instruction before CALL + args (callArgCount LOADK/MOVE instructions).
				// So SELF.pc = callPC - 1 - callArgCount.
				selfPC := callPC - 1 - int32(c.callArgCount)
				st := c.host.Self(int32(base), selfPC, int32(c.selfCallA),
					int32(c.selfCallA), int32(c.selfMethodRK))
				if st != 0 {
					return st
				}
			}
			argOffset := int32(1)
			if c.isSelfCall {
				argOffset = 2 // self occupies R(callA+1), args start at R(callA+2)
			}
			c.loadCallArgs(argOffset)
			// baseline doCall: bypasses the R3 indirect sentinel (this simplified
			// shape does not support in-segment call_indirect); host/crescent/__call/gibbous
			// all shapes run to completion synchronously.
			st := c.host.CallBaseline(int32(base), callPC,
				int32(c.callA), int32(c.callB), int32(c.callC))
			if st != 0 {
				return st
			}
			// N>=2 return-value getter shape: Run does N MOVE copies to preserve
			// byte-equality (luac emits R(callA+nret+k) ← R(callA+k), then
			// RETURN A=callA+nret B=nret+1). The trailing DoReturn already has
			// retA/retB set (retA=callA+nret, retB=nret+1) and reads R(callA+nret..)
			// to copy into the caller slots.
			if c.callMultiRet >= 2 {
				nret := int32(c.callMultiRet)
				for k := int32(0); k < nret; k++ {
					c.host.SetReg(int32(c.callA)+nret+k,
						c.host.GetReg(int32(c.callA)+k))
				}
			}
		case uint8(bytecode.TAILCALL):
			// PJ5 TAILCALL shape (per docs/design/p4-method-jit/05-system-pipeline.md
			// §4.3 + analyzeTailCallForm): `function() return f() end`-type single
			// CallExpr as the sole return expression is translated by luac into
			// TAILCALL + trailing dead RETURN B=0 (to-top) + implicit RETURN B=1.
			//
			// **pc argument**: TAILCALL's own pc = retPC-1 (retPC points to the dead RETURN).
			//
			// **preprocessing — load the callee function into the R(callA) slot**
			// (same as CALL void; after loading, call host.TailCall to complete the
			// tail call):
			//   - shape TA*: host.GetReg(MOVE.B) + SetReg(callA)
			//   - shape TB*: host.GetUpval(base, GETUPVAL.B) + SetReg(callA)
			//
			// **SELF inline shape** (isSelfCall=true): same as the CALL path, after
			// loading recv into R(callA) call host.Self, args start at R(callA+2);
			// then call host.TailCall to complete the tail-call three-state branch.
			//
			// **Three-state branch** (same as crescent.State.TailCall + jit/host.go::TailCall):
			//   - 0 = Lua tail complete: the caller frame has been replaced by the
			//     callee frame + executeFrom synchronously drives the callee chain to
			//     completion + nresults written back to funcIdx. Run **skips
			//     DoReturn** (this frame already popped) and returns 0 directly (the
			//     DoReturn call at the end of this function must be skipped by the
			//     isTailCall guard).
			//   - 1 = ERR: raise pending → upper layer propagates ERR.
			//   - 2 = host tail complete: result already at R(callA..), the G frame
			//     not popped. Run **calls DoReturn normally** (matching the dead
			//     RETURN A=callA B=0 to-top, nret = top - (base + callA), DoReturn's
			//     internal B=0 multi-value path).
			tailPC := int32(c.retPC) - 1
			var srcVal uint64
			if c.isCallUpval {
				srcVal = c.host.GetUpval(int32(base), int32(c.preludeArg))
			} else {
				srcVal = c.host.GetReg(int32(c.preludeArg))
			}
			c.host.SetReg(int32(c.callA), srcVal)
			// SELF inline preprocessing: host.Self completes method fetch + self loading
			if c.isSelfCall {
				selfPC := tailPC - 1 - int32(c.callArgCount)
				st := c.host.Self(int32(base), selfPC, int32(c.selfCallA),
					int32(c.selfCallA), int32(c.selfMethodRK))
				if st != 0 {
					return st
				}
			}
			argOffset := int32(1)
			if c.isSelfCall {
				argOffset = 2
			}
			c.loadCallArgs(argOffset)
			st := c.host.TailCall(int32(base), tailPC,
				int32(c.callA), int32(c.callB), int32(c.callC))
			switch st {
			case 0:
				// Lua tail complete: this frame has been replaced by the callee
				// frame + executeFrom synchronously drove the callee chain to
				// completion. Return 0 directly, skip the trailing DoReturn (this
				// frame already popped).
				_ = stack
				return 0
			case 1:
				return 1
			case 2:
				// host tail complete: result at R(callA..), G frame not popped. Fall
				// through to the trailing DoReturn (retB=0 multi-value to-top path).
			default:
				// Future extension: this interface currently defines only the three
				// states 0/1/2. Treat other values as ERR fallback.
				return 1
			}
		case uint8(bytecode.EQ), uint8(bytecode.LT), uint8(bytecode.LE):
			if c.retB < 2 {
				break
			}
			// Comparison-folding shape: the 6-op template EQ/LT/LE + JMP + LOADBOOL
			// × 2 + RETURN (+ dead RETURN) is equivalent to
			// `R(retA) = BoolValue(cmp == (cmpA==1))`. host.Compare returns packed:
			// bit0=result / bit1=error flag.
			//
			// **pc argument**: this shape has retPC=4 (RETURN at pc 4), so
			// preludePC=3 is wrong — the comparison op is actually at pc 0. Pass 0
			// directly as preludePC (line number / IC slot anchored to the
			// comparison op itself).
			packed := c.host.Compare(int32(base), 0, int32(c.preludeOp),
				int32(c.preludeArg), int32(c.preludeC))
			if packed&2 != 0 {
				// bit1 = ERR pending (raiseGibbous already set it)
				return 1
			}
			cmpResult := packed & 1 // bit0
			// Folding: true if the result matches cmpA (equivalent to luac's LOADBOOL sequence).
			boolVal := value.False
			if cmpResult == int32(c.cmpA) {
				boolVal = value.True
			}
			c.host.SetReg(int32(c.retA), uint64(boolVal))
		}
	}

	_ = stack // P3 protocol parameter, P4 neither reads nor writes

	if c.host != nil {
		c.host.DoReturn(int32(base), int32(c.retPC), int32(c.retA), int32(c.retB))
	}

	return 0
}

// PendingErr defaults to nil (the P4 PJ2 simplified shape holds no error state —
// Run returns the status directly).
func (c *p4Code) PendingErr() error {
	return nil
}

// runSpecSelfCall handles the PJ5 SELF + CALL spec template path (per
// compileSpecSelfCall + §9.10 reusing EmitSelfNodeHit).
//
// **Flow**:
//  1. First load R(callA) = recv (simulating luac MOVE/GETUPVAL, since the spec
//     segment reads the receiver from R(callA) at byte level).
//  2. callJITSpec runs the EmitSelfNodeHit template:
//     - success → R(callA) = method (stored) + R(callA+1) = self (stored)
//     - failure (raxSpec == specDeoptCode) → fall back to host.Self (R(callA+1)
//     has already been stored recv by the template, same step as the P1 SELF case;
//     host.Self overwrites it again byte-equal)
//  3. callArgCount=0 (this batch is only the 0-arg shape), no args to load.
//  4. host.CallBaseline completes the CALL segment.
//  5. host.DoReturn pops the frame.
//
// byte-equal P1: the success path = byte-level NodeHit direct-to-slot (skipping the
// hash), matching the result of a P1 icGetTable NodeHit; the failure path = the
// full P1 SELF segment via host.Self.
func (c *p4Code) runSpecSelfCall(base int32, jitCtxAddr uintptr, vsBaseAddr uintptr) int32 {
	// 1. load R(callA) = recv (simulating MOVE/GETUPVAL)
	// MOVE form (form M*): recv loading is already byte-level emitted at the head
	// of the spec segment (per §9.19 args-inline amortization), skipping the
	// host.GetReg+SetReg 2 crossings.
	// GETUPVAL form (form U*): the upvalue is not on the vsBase stack, still loaded
	// via host on the Run side.
	if c.selfRecvIsUpval {
		upvalVal := c.host.GetUpval(base, int32(c.selfRecvSrcReg))
		c.host.SetReg(int32(c.callA), upvalVal)
	}

	// 2. callJITSpec runs the SELF segment
	raxSpec := archCallJITSpec(c.codePage.Addr(), jitCtxAddr, vsBaseAddr)
	if raxSpec == c.specDeoptCode {
		// deopt failure: the SELF NodeHit guard does not hold (table shape changed /
		// key degraded / NodeVal=nil) = a genuine speculation failure → OSR exit.
		// Feed p4SpecState counting (per §9.18 + 04 §5.1 the three things on a
		// single failure: count +1, switch to P4Deoptimized on reaching threshold).
		onOSRExit(c.proto)
		// fall back to host.Self (byte-equal P1 SELF segment)
		// SELF's own pc = callPC - 1 (CALL is at retPC-1, SELF is one before CALL, 0-arg shape)
		callPC := int32(c.retPC) - 1
		selfPC := callPC - 1 - int32(c.callArgCount)
		st := c.host.Self(base, selfPC, int32(c.selfCallA),
			int32(c.selfCallA), int32(c.selfMethodRK))
		if st != 0 {
			return st
		}
	} else if c.useFrameInline && raxSpec == uint64(ExitInlineHelper) {
		// **§9.20.9 Run-end dispatcher implementation** (commit-5b/5l):
		st := c.runFrameInlineDispatcher(base)
		if st != 0 {
			return st
		}
		// **caller frame's own RETURN** (commit-5l): on the caller proto's
		// useFrameInline path the mmap segment did not emit the caller's RETURN
		// instruction (only the SELF+CALL inline segment), so Run must DoReturn
		// manually (matching the non-useFrameInline path's host.CallBaseline +
		// DoReturn caller-RETURN frame pop).
		c.host.DoReturn(base, int32(c.retPC), int32(c.retA), int32(c.retB))
		return 0
	}

	// 3. args loading is already byte-level emitted in the spec segment (per §9.19
	// amortization optimization, skipping the host round-trip). After the spec
	// segment executes, args have landed at R(callA+2..callA+1+N).
	// **On the deopt path** the spec segment returned deoptCode mid-way, but the
	// args-loading segment (before SELF) already executed, so R(callA+2..) is
	// already loaded — after falling back to host.Self the args are still usable,
	// no reload needed.

	// 4. CALL segment / TAILCALL segment (byte-equal P1)
	callPC := int32(c.retPC) - 1
	if c.isTailCall {
		// TAILCALL three-state branch (same semantics as host.TailCall):
		//   0 = Lua tail complete (this frame already popped, skip DoReturn and return)
		//   1 = ERR
		//   2 = host tail complete (result at R(callA..), G frame not popped, falls to DoReturn dead RETURN B=0)
		st := c.host.TailCall(base, callPC, int32(c.callA), int32(c.callB), int32(c.callC))
		switch st {
		case 0:
			return 0
		case 1:
			return 1
		case 2:
			// fall through to DoReturn
		default:
			return 1
		}
	} else {
		// CALL void: byte-equal P1 doCall
		st := c.host.CallBaseline(base, callPC, int32(c.callA), int32(c.callB), int32(c.callC))
		if st != 0 {
			return st
		}
	}

	// 5. DoReturn pops the frame (setter shape retB=1, 0 return values)
	c.host.DoReturn(base, int32(c.retPC), int32(c.retA), int32(c.retB))
	return 0
}

// loadCallArgs loads callArg1..7 into R(callA+offset), R(callA+offset+1), ...
// offset = 1 (ordinary CALL/TAILCALL, args from R(callA+1)); 2 (SELF inline, args
// from R(callA+2), since R(callA+1)=self).
func (c *p4Code) loadCallArgs(offset int32) {
	if c.callArgCount >= 1 {
		var argVal uint64
		if c.callArg1IsK {
			argVal = c.callArg1K
		} else {
			argVal = c.host.GetReg(int32(c.callArg1RegSrc))
		}
		c.host.SetReg(int32(c.callA)+offset+0, argVal)
	}
	if c.callArgCount >= 2 {
		var argVal uint64
		if c.callArg2IsK {
			argVal = c.callArg2K
		} else {
			argVal = c.host.GetReg(int32(c.callArg2RegSrc))
		}
		c.host.SetReg(int32(c.callA)+offset+1, argVal)
	}
	if c.callArgCount >= 3 {
		var argVal uint64
		if c.callArg3IsK {
			argVal = c.callArg3K
		} else {
			argVal = c.host.GetReg(int32(c.callArg3RegSrc))
		}
		c.host.SetReg(int32(c.callA)+offset+2, argVal)
	}
	if c.callArgCount >= 4 {
		var argVal uint64
		if c.callArg4IsK {
			argVal = c.callArg4K
		} else {
			argVal = c.host.GetReg(int32(c.callArg4RegSrc))
		}
		c.host.SetReg(int32(c.callA)+offset+3, argVal)
	}
	if c.callArgCount >= 5 {
		var argVal uint64
		if c.callArg5IsK {
			argVal = c.callArg5K
		} else {
			argVal = c.host.GetReg(int32(c.callArg5RegSrc))
		}
		c.host.SetReg(int32(c.callA)+offset+4, argVal)
	}
	if c.callArgCount >= 6 {
		var argVal uint64
		if c.callArg6IsK {
			argVal = c.callArg6K
		} else {
			argVal = c.host.GetReg(int32(c.callArg6RegSrc))
		}
		c.host.SetReg(int32(c.callA)+offset+5, argVal)
	}
	if c.callArgCount >= 7 {
		var argVal uint64
		if c.callArg7IsK {
			argVal = c.callArg7K
		} else {
			argVal = c.host.GetReg(int32(c.callArg7RegSrc))
		}
		c.host.SetReg(int32(c.callA)+offset+6, argVal)
	}
}

// Slot returns the shared funcref table slot number + whether registered.
//
// **P4 native code has no wasm-table concept** — always returns (0, false), letting
// the upper layer take the synchronous Run fallback (gibbous→gibbous calls use P4's
// own direct-jump protocol rather than going through the wasm `call_indirect`).
// Per `p2-bridge/05-p3-p4-interface.md` §6 GibbousCode.Slot note: "P4 native code
// always takes the fallback".
func (c *p4Code) Slot() (uint32, bool) {
	return 0, false
}

// Dispose releases the mmap segment. Idempotent.
//
// **Concurrency**: Dispose is safe against concurrent Run calls in
// multi-State setups. CodePage.Dispose flips a disposed flag (blocking
// further Enter) and releases the constructor's reference; the real
// unix.Munmap fires only when the refcount reaches zero, either
// synchronously here (no Run held the segment) or on the last Run's
// deferred Exit. See internal/gibbous/jit/amd64/codepage_linux.go for the
// full refcount protocol.
func (c *p4Code) Dispose() error {
	if c.codePage != nil {
		err := c.codePage.Dispose()
		c.codePage = nil
		return err
	}
	return nil
}

// ErrRunNotImplemented: placeholder error (superseded by the PJ2 wired-in version,
// but kept as the error type for wireP4's defensive fallback return — if codePage
// construction fails, Run returns ERR directly).
var ErrRunNotImplemented = errors.New("internal/gibbous/jit: p4Code Run failed: codePage / jitCtx not initialized")

// runFrameInlineDispatcher handles the PJ5 Option B Spike 1 frame-build inlining
// Run-end dispatcher path (per §9.20.9 (1)+(5) Go-side dispatcher implementation,
// commit-5b Run-end-ified version):
//
// **Protocol**: the spec segment has emitted SELF NodeHit + BuildVoid0Arg +
// ExitHelperRequest + (resume entry) PopVoid0Arg + ret; when the segment returns
// RAX=ExitInlineHelper this function takes over to complete callee Lua body
// execution + popCallInfo re-entry.
//
// Flow:
//  1. Read jitCtx.exitArg0 to route the helper request (per §9.20.9 (3) protocol
//     status codes):
//     - HelperRunCallee: call host.ExecuteCalleeFromInlineFrame(base, callA,
//     callArgCount, nresults) (commit-5l/5p/5q signature extension)
//     completes readCISegInto + nCcalls++ + executeFrom + popCallInfo
//     - HelperGrowStack: future extension (arena grow trigger)
//     - HelperGCBarrier: future extension (GC write barrier)
//  2. If the helper returns 1=ERR, set ERR return 1 (error propagation)
//  3. A second archCallJITSpec jumps to codePage + frameInlineResumeOff to continue
//     the PopVoid0Arg + ret segment; RAX must = 0 (ExitNormal)
//  4. Skips Run's host.CallBaseline + DoReturn (the callee ran to completion inside the helper)
//
// **Design clarification** (continuing commit-3b's cross-package CALL design clarification):
//   - the dispatcher routing should be dispatchInlineHelper (a jit-package-level
//     function), but cross-package + Plan 9 asm complexity is high; instead a
//     p4Code method directly accesses c.host (per the Run-end-ified approach)
//   - dispatchInlineHelper is kept as an engineering-foundation anchor, the
//     interface for a future real trampoline asm CALL path (per §9.20.9 (5))
//
// **Currently archSupportsFrameInline=false blocks the real trigger**, so this
// function is not reached; enabled after commit-5e opens the switch +
// analyzeSelfCallSpecForm sets useFrameInline=true.
func (c *p4Code) runFrameInlineDispatcher(base int32) int32 {
	incSpecFrameInlineRunHits() // per §9.20.9 commit-5i: Run-time reach probe
	// 1. route the helper request: read jitCtx.exitArg0 to decide the helper type
	helperCode := c.jitCtx.ExitArg0()
	switch helperCode {
	case HelperRunCallee:
		// run the callee Lua body (host completes readCISegInto + executeFrom + popCallInfo)
		// **commit-5l/5p/5q signature extension**: the helper accepts (callA, callArgCount, nresults)
		//   - callA: CALL.A field (in the SELF + CALL shape the method is at R(callA))
		//   - callArgCount: 0..7 user args
		//   - nresults: callC - 1 (callC=1=0-return setter/2=1-return getter/3..16=N=2..15
		//     return drop multi-ret)
		nresults := int32(c.callC) - 1
		st := c.host.ExecuteCalleeFromInlineFrame(base, int32(c.callA), int32(c.callArgCount), nresults)
		if st != 0 {
			// error propagation (host-side raise already set pendingErr)
			return 1
		}
	case HelperGrowStack, HelperGCBarrier:
		// future extension, currently not reached (the spec segment does not emit these requests)
		return 1
	default:
		// unknown helper code (protocol bug)
		return 1
	}
	// 2. **arena grow reload**: enterLuaFrame / executeFrom inside the helper may
	//    trigger ensureStack → arena.grow, invalidating arena base + ciDepthAddr +
	//    ciSegBaseAddr + topAddr. Recompute and re-inject via host (per §9.20 + §5
	//    arena base reload protocol).
	c.host.RefreshJitCtxAddrs(c.jitCtx, base)
	// 3. a second callJITSpec jumps to the resume entry to continue PopVoid0Arg + ret
	resumeAddr := c.codePage.Addr() + uintptr(c.frameInlineResumeOff)
	jitCtxAddr := jitContextAddr(c.jitCtx)
	vsBaseAddr := c.jitCtx.ValueStackBase()
	raxResume := archCallJITSpec(resumeAddr, jitCtxAddr, vsBaseAddr)
	if raxResume != 0 {
		// resume entry segment executed abnormally (in theory PopVoid0Arg + ret
		// only does ciDepth-- + ret, RAX should be ExitNormal=0; non-zero indicates a protocol bug)
		return 1
	}
	return 0
}

// Compile-time assertions: Compiler implements the bridge.P3Compiler interface;
// p4Code implements bridge.GibbousCode (any interface signature drift is exposed
// at compile time immediately, not deferred to run time).
var (
	_ bridge.P3Compiler  = (*Compiler)(nil)
	_ bridge.GibbousCode = (*p4Code)(nil)
)
