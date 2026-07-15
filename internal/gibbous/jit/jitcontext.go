//go:build wangshu_p4

// jitcontext.go —— the P4 JIT execution-context struct (per
// docs/design/p4-method-jit/05-system-pipeline.md §3.3 jitContext field table).
//
// **Option A** (user decision, 03 §4 + §8): **P4 owns the speculation
// lifecycle itself**. jitContext is P4's "key coupling point #4" across the
// Go ↔ JIT world boundary (00 §3 key coupling point 4) — it carries "all the
// Go-side capabilities the JIT code needs (arena base / helper table /
// preemptFlag / exit reason code)" packed into a jitContext struct on the Go
// heap, passed in via a fixed register (amd64 r15).
//
// **Simplified PJ1+2-stage form**: this file first builds the struct field
// skeleton + constructor + test hooks, but does **not** fully wire up the
// trampoline asm in PJ1 (the simplified PJ1 form does not switch SP and does
// not load jitContext; a single CALL+RET trampoline suffices). Full wiring
// (the amd64 trampoline doing MOVQ jitContext, R15 when it switches SP) lands
// as one batch at PJ2+ startup.
//
// Design basis:
//   - 05 §3.3: jitContext struct fields (arena base / value-stack base /
//     preemptFlag / helper table / exit reason code / spill-area start);
//     per-arch fixed register (amd64 r15); Go-heap allocation discipline.
//   - 06 §4.1: amd64 register convention (r15 = jitContext); 06 §4.2: arm64
//     register convention (x28 = jitContext).
//   - 03 §8: P4 self-managed speculation lifecycle — do the p4SpecState
//     substate fields hang off jitContext or a separate map? The current
//     decision is a separate map (per-Proto state, persistent across calls,
//     whereas jitContext is per-call scratch), which reduces the jitContext
//     field count (per 03 §8.4 ✅ self-managed deopt count and
//     P4StuckSpeculation state).
package jit

import (
	"sync/atomic"
	"unsafe"
)

// JITContextPreemptFlagOffset is the byte offset of the preemptFlag field
// relative to *JITContext — the JIT template's byte-level codegen reads this
// offset to do the safepoint check
// (cmp byte ptr [r15 + JITContextPreemptFlagOffset], 0; jne deopt).
//
// **byte-comparison discipline**: preemptFlag is an atomic.Uint32 (4 bytes)
// but effectively only takes the two values 0/1; the crescent side sets the
// low byte=1 (little-endian) when raising it, so `cmpb 0` correctly detects
// !=0 under the current protocol — this avoids emitting a dword cmp, which
// wastes a byte + ModRM SIB. Should preemptFlag be extended to use a high bit
// in the future, it must switch to a `cmpd` form (the matching EmitCmpDword
// is left as PJ3+ engineering groundwork).
//
// Computed via unsafe.Offsetof rather than hardcoded: the Go runtime does not
// guarantee struct field order is consistent across versions (though on
// 64-bit systems with sequential alignment it is usually stable), and
// Offsetof pins it down once as a compile-time constant. The PJ3+ FORLOOP
// byte-level inline back-edge checkpoint emits directly through this offset.
const (
	JITContextArenaBaseOffset      = unsafe.Offsetof(JITContext{}.arenaBase)
	JITContextValueStackBaseOffset = unsafe.Offsetof(JITContext{}.valueStackBase)
	JITContextPreemptFlagOffset    = unsafe.Offsetof(JITContext{}.preemptFlag)
	JITContextExitReasonOffset     = unsafe.Offsetof(JITContext{}.exitReasonCode)
	// PJ5 Option B Spike 1+ frame-establishment inlining: exposes the host
	// byte addresses of ciDepth / ciSegBase / top (per §9.20); after
	// dereferencing the mmap segment's `r15+offset` to a uintptr, the CI
	// segment is inc/dec'd at the byte level. Reuses the P3 PW10 Stage 1a/2
	// mirror words (crescent.State.ciDepthRef / ciSegBaseRef / topRef), but
	// uses host addrs (uintptr) rather than wasm linear-memory offsets (the
	// P3 wasm segment vs P4 mmap segment use different addressing protocols
	// on each end).
	JITContextCIDepthAddrOffset   = unsafe.Offsetof(JITContext{}.ciDepthAddr)
	JITContextCISegBaseAddrOffset = unsafe.Offsetof(JITContext{}.ciSegBaseAddr)
	JITContextTopAddrOffset       = unsafe.Offsetof(JITContext{}.topAddr)

	// **§9.20.9 trampoline exit-resume protocol fields** (Spike 1 wired up in
	// commit-1):
	JITContextExitArg0Offset  = unsafe.Offsetof(JITContext{}.exitArg0)
	JITContextResumeOffOffset = unsafe.Offsetof(JITContext{}.resumeOff)

	// **§9.20.9 trampoline exit-resume protocol codePageAddr field** (Spike 1
	// wired up in commit-3b): the dispatcher computes the resume entry via
	// codePageAddr + resumeOff; the Run side records the codePage start when
	// emitting. Per design draft (5).
	JITContextCodePageAddrOffset = unsafe.Offsetof(JITContext{}.codePageAddr)

	// **PJ10-native addition**: savedGoG is used by mmap-segment helper
	// calls to preserve the Go ABIInternal invariant that R14 = G. The
	// Go-side Run wrapper writes G into it via saveGoG; the mmap segment
	// emits `mov r14, [r15+savedGoGOff]` before each Go helper call to
	// restore R14 = G, otherwise Go's function prologue crashes.
	// See [[project-pj10-native-longtask]] R14-save-wrapper.
	JITContextSavedGoGOffset = unsafe.Offsetof(JITContext{}.savedGoG)

	// JITContextSegCallDepthOffset is the byte offset of segCallDepth
	// (issue #50 Spike 5). The callee RETURN emit reads it via
	// `cmp dword [r15+off], 0` to branch between the segment-exit and
	// in-segment-teardown paths; the caller CALL fast body increments /
	// decrements it around the segment-to-segment `call`.
	JITContextSegCallDepthOffset = unsafe.Offsetof(JITContext{}.segCallDepth)

	// JITContextSegCallDeoptOffset is the byte offset of segCallDeopt
	// (issue #50 Spike 5). arith / compare / CALL slow blocks test
	// `cmp dword [r15+segCallDepthOff], 0` and, when nonzero, set
	// `mov dword [r15+segCallDeoptOff], 1` + ret to deopt the seg2seg
	// call chain.
	JITContextSegCallDeoptOffset = unsafe.Offsetof(JITContext{}.segCallDeopt)

	// Inline GETUPVAL ABI (issue #50 Spike 5): the running frame's
	// closure GCRef, the running thread's stack-slot-0 host address, and
	// the single-thread inline-safety flag. The inline GETUPVAL emit
	// reads all three via r15+offset.
	JITContextCurrentClosureRefOffset = unsafe.Offsetof(JITContext{}.currentClosureRef)
	JITContextThreadStackBase0Offset  = unsafe.Offsetof(JITContext{}.threadStackBase0)
	JITContextInlineUpvalSafeOffset   = unsafe.Offsetof(JITContext{}.inlineUpvalSafe)

	// JITContextValueStackEndOffset is the byte offset of valueStackEnd
	// (issue #80): the seg2seg CALL fast body's stack-bound guard reads
	// it to verify the callee frame fits in the current stack segment.
	JITContextValueStackEndOffset = unsafe.Offsetof(JITContext{}.valueStackEnd)

	// JITContextSpillBaseOffset / JITContextSpillTopOffset are the byte
	// offsets of the self-managed spill stack bounds (issue #89). The
	// trampoline reads spillBase to switch SP onto the spill stack before
	// entering the segment (MOVQ [r15+spillBaseOff], SP), so deep seg2seg
	// recursion descends on the spill buffer instead of the goroutine
	// stack's NOSPLIT allowance. spillBase is the high-address end (SP
	// entry point for a down-growing stack); spillTop is the low-address
	// growth limit. Both 0 => no switch (baseline, goroutine stack).
	JITContextSpillBaseOffset = unsafe.Offsetof(JITContext{}.spillBase)
	JITContextSpillTopOffset  = unsafe.Offsetof(JITContext{}.spillTop)

	// JITContextSavedGoSPOffset is the byte offset of savedGoSP (issue
	// #89): the trampoline stashes the goroutine SP here before switching
	// SP onto the spill stack, and reloads it on the way out.
	JITContextSavedGoSPOffset = unsafe.Offsetof(JITContext{}.savedGoSP)

	// JITContextSegCallFuelOffset is the byte offset of segCallFuel: the
	// seg2seg CALL fast body decrements it once per in-segment dispatch
	// and skips to the host path when it reaches zero, so the step
	// budget / cancel context can preempt execution that would otherwise
	// never leave the mmap segment (fuzz crasher f2165a93dd62892d:
	// fib(5510) under a step budget hung forever once segToSegDepthCap
	// made depth-128 subtrees fully in-segment — ~phi^128 calls with no
	// billing point).
	JITContextSegCallFuelOffset = unsafe.Offsetof(JITContext{}.segCallFuel)

	// JITContextLoopFuelOffset is the byte offset of loopFuel: the loop
	// back-edge budget counter (issue #102). The FORLOOP / negative-sBx
	// JMP back-edge emit decrements it (amd64 `sub dword [r15+off],1`,
	// arm64 ldr-sub-str) and exits via HelperLoopFuel at zero, giving
	// budgeted States a billing point on loops whose bodies never leave
	// the mmap segment (issue #102: a 277M-iteration loop with an
	// inline math-intrinsic body ran to completion under a 1M budget).
	JITContextLoopFuelOffset = unsafe.Offsetof(JITContext{}.loopFuel)
)

// **§9.20.9 protocol status-code constants** (Spike 1 wired up + future
// helper-request routing):

// JIT exit reason codes (per §9.20.9 (3) protocol status codes + the
// exitReasonCode field):
const (
	ExitNormal       uint32 = 0 // normal RET exit from segment
	ExitError        uint32 = 1 // ERR propagation (state.pendingErr already set)
	ExitOSR          uint32 = 2 // reserved: spec-template deopt uses a RAX sentinel, not this code (see p4state.go); kept so ExitInlineHelper stays 3
	ExitInlineHelper uint32 = 3 // Spike 1 helper request (jitCtx.exitArg0 = helper code)
)

// JIT inline helper request codes (per §9.20.9 (3) protocol status codes):
const (
	HelperRunCallee uint64 = 1 // Spike 1 Step C-1: run the callee Lua body
	HelperGrowStack uint64 = 2 // future: arena grow trigger
	HelperGCBarrier uint64 = 3 // future: GC write barrier (only when writing the Go heap)

	// PJ10 native emit exit-reason codes for shim-based ops that can't
	// be shim-called from inside the mmap segment (issue #38). The
	// segment writes the helper code into low bits of exitArg0 along
	// with packed op args, sets resumeOff to the next-op offset, and
	// RETs; nativeCode.Run's dispatcher reads exitArg0, invokes the
	// corresponding host method, then reenters the mmap segment via
	// codePage + resumeOff.
	HelperGetTable    uint64 = 10
	HelperSetTable    uint64 = 11
	HelperGetGlobal   uint64 = 12
	HelperSetGlobal   uint64 = 13
	HelperGetUpval    uint64 = 14
	HelperSetUpval    uint64 = 15
	HelperNewTable    uint64 = 16
	HelperSelf        uint64 = 17
	HelperUnm         uint64 = 18
	HelperLen         uint64 = 19
	HelperConcat      uint64 = 20
	HelperSetList     uint64 = 21
	HelperArithSlow   uint64 = 22
	HelperCompareSlow uint64 = 23
	HelperCall        uint64 = 24

	// HelperReturn is the exit-reason code for RETURN in multi-return
	// Protos. Unlike other helpers it terminates the segment run: the
	// dispatcher calls host.DoReturn with the packed (a, b, pc) and
	// does NOT reenter the mmap segment. Single-return Protos keep the
	// legacy `xor eax, eax; ret` + Go-side DoReturn(retA/retB/retPC)
	// path, which saves the exitArg0 packing on the hot exit.
	HelperReturn uint64 = 25

	// HelperExecutePlainCall is the exit-reason code the PJ10 native
	// EmitCallInline fast path emits after the segment has written the
	// callee CI slot and incremented ciDepth (issue #50 Spike 2). The
	// dispatcher's HelperExecutePlainCall case calls
	// host.ExecutePlainCallInlineFrame, which drives executeFrom (or
	// the zero-cross P4-callee path) and rebalances ciDepth for the
	// segment's PopFrame sequence.
	//
	// exitArg0 packing for HelperExecutePlainCall follows the standard
	// emitExitReason layout (bits 0..15 = helper code):
	//
	//	bits 16..23 : callA (CALL.A field, 0-255; the standard a slot)
	//	bits 24..32 : nargs (CALL.B - 1; the standard 9-bit b slot)
	//	bits 33..41 : nresults (CALL.C - 1; the standard 9-bit c slot —
	//	              multret rejected by the shape gate)
	//
	// The pc slot in the standard exit-reason packing isn't consumed
	// (the helper doesn't materialise a pc), so it can stay at zero
	// or reuse the standard packing without affecting behavior.
	HelperExecutePlainCall uint64 = 26

	// HelperForPrep is the exit-reason code for the FORPREP slow path
	// (issue #78): the inline FORPREP fast body assumes the three loop
	// slots (init at A, limit at A+1, step at A+2) are numbers; when
	// any slot's IsNumber guard misses, the segment exits here and the
	// dispatcher calls host.ForPrep, which performs the PUC 5.1
	// coercion-then-error semantics ("'for' initial value/limit/step
	// must be a number") and, on success, normalizes all three slots
	// to numbers and pre-decrements the index — so the resumed FORLOOP
	// can keep assuming numbers.
	HelperForPrep uint64 = 27

	// HelperTailCall is the exit-reason code for TAILCALL (issue #52).
	// Like HelperReturn it ALWAYS terminates the run: host.TailCall
	// returns a tri-state — 0 = Lua tail call completed (frame already
	// replaced and driven to completion; Run returns 0 WITHOUT
	// DoReturn), 1 = error, 2 = host tail call (results are at
	// R(A..top); Run finishes via DoReturn on the trailing dead RETURN
	// luac always emits at pc+1, whose B=0 multret form reads the live
	// top). No arm reenters the segment.
	HelperTailCall uint64 = 28

	// HelperTForLoop is the exit-reason code for TFORLOOP (issue #52).
	// The dispatcher calls host.TForLoop (invoking the iterator and
	// writing R(A+3..A+2+C)) and passes the continue/exit verdict back
	// through exitArg0 (1 = continue: first result non-nil, control
	// vars updated; 0 = exit loop), mirroring HelperCompareSlow's
	// packed-result protocol: the branch decision must happen inside
	// the segment, which alone knows the successor BB offsets.
	HelperTForLoop uint64 = 29

	// HelperClosure is the exit-reason code for CLOSURE A Bx (issue
	// #52). The dispatcher calls host.Closure, which materialises the
	// closure into R(A) and consumes the pseudo-instructions following
	// CLOSURE (one MOVE/GETUPVAL per upvalue) on the host side. The
	// segment's resumeOff points past the pseudos (the translator never
	// emits them). Bx exceeds the 9-bit b/c slots, so it is packed like
	// GETGLOBAL's 18-bit split: b = Bx & 0x1FF, c = Bx >> 9.
	HelperClosure uint64 = 30

	// HelperClose is the exit-reason code for CLOSE A (issue #52): the
	// dispatcher calls host.Close, closing all open upvalues at or
	// above R(A). Never raises.
	HelperClose uint64 = 31

	// HelperLoopFuel is the exit-reason code for an in-segment loop
	// back-edge whose loopFuel decrement hit zero (issue #102). A
	// fully-inline loop body (arithmetic, or a #77 math intrinsic)
	// otherwise never reaches a Go-side billing point, so a budgeted
	// State could run a 277M-iteration loop to completion while the
	// interpreter raised "instruction budget exceeded" in ms. The
	// dispatcher calls host.LoopPreempt, which bills the spent fuel to
	// the step budget, refills, and runs the standard preemption check
	// (budget + cancel context); on success the segment resumes at the
	// back-edge continuation. Emitted on FORLOOP back-edges and
	// negative-sBx JMP terminators (while/repeat back-edges — same
	// airtight-loop class). Unconditional: promotion generally happens
	// before SetStepBudget, so the check cannot be compile-time gated;
	// the SegCallFuelUnlimited refill keeps unbudgeted workloads at one
	// dec+jnz per iteration.
	HelperLoopFuel uint64 = 32
)

// HelperCodeMask masks off the low 16 bits of exitArg0 that hold the
// helper discriminator; the high 48 bits carry op-specific packed args.
const HelperCodeMask uint64 = 0xFFFF

// JITContext is P4's cross-boundary execution context (05 §3.3).
//
// **Lifecycle**: one jitContext per State (held by crescent.State)? Or one
// per-call (built afresh each time a P4 up-tier call happens)? — the PJ1+2
// stage settles on **per-State singleton** (per 05 §3.4 self-managed stack
// layout: reused within the State to reduce GC pressure). The specific State
// field mount point is added as a batch during PJ2 wireP4.
//
// **Go-heap allocation discipline** (per 05 §1.3.4):
//   - jitContext must be Go-heap allocated (`new(JITContext)`), never on the
//     stack;
//   - the Go GC does not move Go-heap objects, but it does move stack objects
//     — stack allocation would leave the jitContext pointer stale after a
//     morestack, violating the 05 §1.3 "JIT holds no Go stack pointer"
//     discipline;
//   - passed into the JIT segment via amd64 r15; inside the segment only
//     load/store r15+offset happens, never a dereference-as-Go-pointer (a
//     free pass on write barriers, per 05 §1.4).
//
// **per-arch consistency** (per 06 §4.1/§4.2): amd64 r15 = JITContext, arm64
// x28 = JITContext. The Go side converts *JITContext to a uintptr via
// unsafe.Pointer and packs it into the trampoline (wiring left to PJ2).
type JITContext struct {
	// arenaBase is the uintptr of the arena `[]byte` start (per 05 §1.3.3 /
	// §3.3).
	//
	// The JIT segment addresses via r14 = arenaBase, turning a GCRef offset
	// into a byte address. This field is refreshed when the arena grows (per
	// 05 §5 arena base reload protocol).
	//
	// **Not wired into the trampoline in the PJ1+2 stage** (the simplified
	// form does not need an arena base); enabled once the PJ2 arithmetic
	// templates involve a NaN-box load/store.
	arenaBase uintptr

	// valueStackBase is the uintptr of the current frame's R0 (per 05 §3.3 +
	// 06 §4.1 amd64 rbx).
	//
	// When crescent calls GibbousCode.Run it passes a base offset (uint32);
	// on entry the trampoline computes valueStackBase = arenaBase + base*8
	// and loads it into rbx. This field is "the stack-slot start computed
	// before each JIT entry".
	valueStackBase uintptr

	// preemptFlag is the preemption signal (per 05 §1.2.2 + §6.3).
	//
	// Async preemption is unavailable under Go (roadmap §2 runtime
	// ownership); P4 terminates cooperatively via back-edge checkpoints +
	// preemptFlag — the back-edge template inlines `cmpb [r15+offset], 0` +
	// `jne exit_stub`. Any nonzero value triggers an OSR exit out of the JIT
	// world.
	//
	// The crescent side sets this field to 1 on a scheduling yield / GC
	// trigger; the trampoline clears it to 0 on exit.
	//
	// uint32 + atomic Load (crescent writes, JIT reads) avoids a data race
	// (passing `-race` is the V18 acceptance criterion).
	preemptFlag atomic.Uint32

	// helperTable is the slow-path helper function table (05 §4.3).
	//
	// **Empty table in the PJ1 stage**: the LOADK/RETURN straight-line
	// templates call no helper. The table is filled once PJ2 arithmetic +
	// slow paths are enabled (one Go function pointer per slow path), and the
	// JIT segment does an indirect CALL via r15+offset. The specific helper
	// list uses the same mapping as P3 04-trampoline §3.3 (arith / gettable /
	// call / safepoint).
	//
	// The field type [N]uintptr (N = helper count, filled from PJ2 on) is
	// left to be extended as a batch in PJ2. Currently left empty (unused)
	// until PJ2 startup adds fields to the struct.

	// exitReasonCode carries the segment's exit reason (05 §3.3). ACTIVE:
	// the issue #50 frame-inline exit-helper-request protocol writes
	// ExitInlineHelper (3) here from the mmap segment; JITContextExitReasonOffset
	// is baked into the amd64/arm64 emitters (compiler.go emits it). Do not
	// remove this field or reorder it ahead of confirming those baked offsets.
	// The ExitOSR (2) value below is reserved but the spec-template deopt path
	// uses a RAX sentinel (specDeoptCode), not this field, to signal a miss.
	exitReasonCode uint32

	// spillBase is the start of the self-managed machine-stack spill area
	// (05 §3.4).
	//
	// **PJ1 stage does not switch SP** — this field is 0; it is filled once
	// the full PJ2 trampoline enables SP switching (each P4 compilation
	// product allocates a Go-heap []byte as its self-managed stack, spillBase
	// points at its end, and the trampoline does MOVQ spillBase, SP on
	// entry).
	spillBase uintptr

	// spillTop is the upper bound of the self-managed machine-stack spill
	// area (per 05 §3.4).
	//
	// 0 in the PJ1 stage; once PJ2 enables SP switching = spillBase +
	// self-managed stack size (typically 64 KiB, per the
	// implementation-progress §3.1 "self-managed machine-stack size" open
	// question, to be fixed by PJ0/PJ1 measurement).
	spillTop uintptr

	// ciDepthAddr is the host byte address of the thread.ciDepth mirror word
	// (per §9.20 Option B Spike 1).
	//
	// **Reuses the P3 PW10 Stage 1a mirror word** (crescent.State.ciDepthRef):
	// the crescent side injects `&arena.Words()[ciDepthRef].byte` during
	// wireP4. The mmap segment emits `mov rax, [r15+ciDepthAddr]; inc qword
	// ptr [rax]` for a byte-level ciDepth++ (enterLuaFrame inline) / `dec ...`
	// (popCallInfo inline).
	//
	// **0 = not wired** (the Spike 0 stage); the crescent setter writes it
	// when Spike 1 is wired up.
	ciDepthAddr uintptr

	// ciSegBaseAddr is the host byte address of the mirror word holding the
	// CI segment's current byte base (per §9.20 Option B Spike 1).
	//
	// **Reuses the P3 PW10 Stage 2 mirror word** (crescent.State.ciSegBaseRef):
	// the CI segment is relocatable (its byte base changes after a grow), so
	// the crescent side injects `&arena.Words()[ciSegBaseRef].byte` during
	// wireP4. The mmap segment emits `mov rax, [r15+ciSegBaseAddr]; mov rbx,
	// [rax]` to first dereference the current CI segment base, then computes
	// `rbx + depth*ciSlotSize + word*8` to address a CallInfo[depth] field.
	ciSegBaseAddr uintptr

	// topAddr is the host byte address of the thread.top mirror word (per
	// §9.20 Option B Spike 1).
	//
	// **Reuses the P3 PW10 Stage 1a mirror word** (crescent.State.topRef):
	// thread.top is a stack-slot index (a grow-safe coordinate); enterLuaFrame
	// writes this word when setting the callee frame top (top = base +
	// MaxStack). The mmap segment emits `mov rax, [r15+topAddr]; mov qword
	// ptr [rax], topVal` for a byte-level top set.
	topAddr uintptr

	// exitArg0 is the helper request code the mmap segment carries when
	// returning to the trampoline via the exit-helper-request protocol (per
	// the §9.20.9 trampoline exit-resume protocol detailed design draft +
	// §9.20.6 helper call ABI protocol).
	//
	// **Protocol flow**: the mmap segment emits `mov rax, HELPER_RUN_CALLEE;
	// mov [r15+exitArg0Off], rax`, then `ret` out of the segment. The
	// trampoline dispatcher reads jitCtx.exitArg0 to decide helper routing:
	// HELPER_RUN_CALLEE → executeFrom callee / HELPER_GROW_STACK → arena grow
	// / HELPER_GC_BARRIER → write barrier (future).
	//
	// ACTIVE on amd64/arm64 (archSupportsFrameInline() returns true); only
	// the arch_other fallback leaves it dormant. Wired up by the issue #50
	// frame-inline path (commit chain §9.20.9).
	exitArg0 uint64

	// resumeOff is the byte offset of the resume entry within the mmap
	// segment (per §9.20.9 (2)): after BuildVoid0Arg the exit-helper-request
	// segment returns to the trampoline → the Go dispatcher runs the callee
	// to completion → the trampoline computes the resume entry address via
	// `codePageAddr + resumeOff` → it CALLs back into the mmap segment again
	// to resume running PopVoid0Arg (popCallInfo) + ret.
	//
	// **codePage does not relocate** (the mmap PROT_RX segment is allocated
	// once), so resumeOff can be determined at compile time. The
	// compileSpecSelfCall useFrameInline branch records this field when it
	// emits.
	resumeOff uint32

	_ [4]byte // 8-byte alignment padding (after uint32 resumeOff)

	// codePageAddr is the host byte address of the PROT_RX mmap segment start
	// (per §9.20.9 (5) resume entry computation): dispatchInlineHelper uses
	// `codePageAddr + resumeOff` to compute the absolute resume entry address,
	// then re-enters the mmap segment via a second CALL from the Go wrapper.
	//
	// ACTIVE on amd64/arm64. Injected at wireP4 / installGibbous time.
	codePageAddr uintptr

	// segCallDepth is the native segment-to-segment call nesting depth
	// (issue #50 Spike 5). Zero means "entered from Go" (the top-level
	// nativeCode.Run via CallJITSpec); a caller segment increments it
	// before `call`ing into a callee segment and decrements after the
	// callee returns. The callee's RETURN emit branches on it:
	//
	//   - segCallDepth == 0: RETURN exits the segment (`xor eax,eax;ret`
	//     single-return, or HelperReturn exit-reason multi-return) and
	//     the Go-side Run does host.DoReturn — the historical path.
	//   - segCallDepth > 0: RETURN moves the results into the caller's
	//     target registers in-segment and `ret`s back into the caller
	//     segment, no Go round trip. (The virtual-frame model means the
	//     caller never bumped ciDepth, so there is nothing to unwind —
	//     see emitReturnDualSemantics.)
	//
	// It also bounds native recursion: past a conservative cap the
	// caller segment falls back to the exit-reason path so the Go
	// goroutine stack can't overflow inside the NOSPLIT window where
	// morestack can't fire (see spike DECISION.md option b).
	segCallDepth uint32

	// segCallDeopt is the segment-to-segment deopt flag (issue #50 Spike
	// 5). When a segment running as a seg2seg callee (segCallDepth > 0)
	// hits an operation that would normally exit to a Go helper (arith /
	// compare guard miss, a CALL that can't itself go seg2seg, or any
	// exit-reason op), it sets this to 1 and `ret`s instead of exiting.
	// Each caller fast body checks it after the `call`: if set and still
	// nested (segCallDepth > 0), it propagates by ret'ing; at the top
	// (segCallDepth == 0) it clears the flag and redoes the whole call
	// via the exit-reason host path (host.CallBaseline rebuilds a fresh
	// frame — idempotent because a seg2seg-eligible callee has no
	// observable side effect before the deopt point).
	segCallDeopt uint32

	// savedGoG is a snapshot of the Go G register (amd64 R14 / arm64 X28)
	// at Run entry. PJ10 native emit that calls Go helpers must first do
	// `mov r14, [r15+savedGoGOff]` inside the mmap segment to restore
	// R14=G, otherwise Go's ABIInternal prelude (morestack / stack-guard /
	// getg) reads garbage from R14 and crashes.
	//
	// Written by saveGoG at Run entry (see peroptranslator/save_g_amd64.s).
	// PJ0-PJ9 do not use this field - their mmap segments never call Go
	// functions (PR #26 R14 ABI fix relies on that invariant).
	savedGoG uintptr

	// hostRef holds the peroptranslator P4HostState value's interface
	// header as [2]uintptr (itab + data). The PJ10 native helper shims
	// reconstruct the P4HostState from this pair and then dispatch to
	// the appropriate method.
	//
	// **Not using interface{} field**: the JITContext package does not
	// import peroptranslator, so it can't declare a P4HostState-typed
	// field. Using an opaque [2]uintptr keeps the type dependency
	// one-way; peroptranslator restores the interface via unsafe.
	hostRef [2]uintptr

	// currentClosureRef is the GCRef (arena byte offset) of the Lua
	// closure whose Proto the running segment executes (issue #50 Spike 5
	// inline GETUPVAL). RefreshJitCtxAddrs sets it for the top-level
	// (Go-entered) frame; the segment-to-segment caller sets it to the
	// callee closure before `call [seg]` and restores it after. Inline
	// GETUPVAL reads closure word2+b through this to reach the upvalue
	// object, avoiding a HelperGetUpval exit-reason each recursive call.
	currentClosureRef uintptr

	// threadStackBase0 is the absolute host byte address of the running
	// thread's value-stack slot 0 (arenaBase + stackBaseW*8). Inline
	// GETUPVAL's open-upvalue path reads owner.slot(stackIdx) as
	// [threadStackBase0 + stackIdx*8]. Refreshed every Run entry from the
	// live arenaBase, so it survives arena grow.
	threadStackBase0 uintptr

	// inlineUpvalSafe is 1 when the running State has no coroutines and no
	// suspended resume chain, so every open upvalue is owned by the
	// running thread. The inline GETUPVAL open path is only valid then
	// (owner resolution otherwise needs the Go-side st.uvOwner map). When
	// 0, the open path falls back to HelperGetUpval (or deopts when
	// segCallDepth>0).
	inlineUpvalSafe uint32
	_               uint32

	// valueStackEnd is the absolute host byte address ONE PAST the
	// running thread's value-stack segment (arenaBase + (stackBaseW +
	// stackCap)*8). The seg2seg CALL fast body checks that the callee
	// frame (callee vsBase + CalleeMaxStack*8) fits below this before
	// dispatching in-segment (issue #80): the interpreter's
	// enterLuaFrame grows the stack via ensureStack, but a seg2seg
	// call never re-enters Go — without this bound, deep native
	// recursion silently reads/writes past the stack segment into
	// neighboring arena objects (wrong results, corrupted closures).
	// Refreshed on every Run entry / dispatcher re-entry alongside the
	// other addr fields, so it survives arena grow and stack
	// relocation.
	valueStackEnd uintptr

	// spillBacking holds the Go-heap []byte that backs the self-managed
	// spill stack (issue #89). spillBase / spillTop point into it. Kept as
	// a field so the GC never frees the buffer while the trampoline has SP
	// switched onto it (the trampoline holds only a raw uintptr, which the
	// GC does not treat as a reference). Nil until AllocSpillStack runs.
	spillBacking []byte

	// savedGoSP holds the goroutine stack pointer that the trampoline
	// stashes before switching SP onto the spill stack (issue #89), so it
	// can restore SP on the way out. It lives in the jitCtx (not a
	// register) because the segment CALL clobbers caller-saved registers
	// and the trampoline's own callee-saved slots are on the goroutine
	// stack we are switching away from. Nested trampoline entries (a fresh
	// CallJITSpec from the goroutine stack during exit-reason resume) each
	// overwrite and restore it in LIFO order, which is correct because the
	// outer entry has already restored SP to the goroutine stack before
	// the inner entry runs (see spike/p4spillstack DECISION.md G3).
	savedGoSP uintptr

	// segCallFuel bounds how many seg2seg in-segment CALL dispatches may
	// run between Go-side preemption points. The seg2seg fast body
	// decrements it before each in-segment dispatch; at zero the caller
	// falls back to the exit-reason host path, whose
	// ExecutePlainCallInlineFrame -> enterLuaFrame runs st.preempt()
	// (step budget + cancel context) and the host refills the fuel on
	// the next Run entry / dispatcher resume. Loop back-edges use the
	// separate loopFuel counter below (issue #102) — see its doc for
	// why the two cannot share.
	//
	// Why fuel and not the preemptFlag: the step budget has no async
	// producer — nothing ever sets preemptFlag when a budget is armed;
	// billing happens synchronously in st.preempt() at call/back-edge
	// points. In-segment dispatch bypasses all of those, so a promoted
	// call-tree can run ~phi^depthCap calls with zero billing (fuzz
	// crasher f2165a93dd62892d: fib(5510) under SetStepBudget hung
	// "forever" while the interpreter erred in 50ms). A decrementing
	// fuel counter needs no async signal and costs one dec+jz pair per
	// dispatch.
	//
	// When no budget/context is armed the host refills with
	// SegCallFuelUnlimited, so steady-state workloads keep the full
	// in-segment speed and only budget-armed States pay periodic host
	// round trips.
	segCallFuel uint32

	// segCallFuelRefill records the value of the last SetSegCallFuel so
	// the host can bill (refill - segCallFuel) in-segment dispatches to
	// the step budget on the next refresh. Go-side only; the segment
	// never reads it.
	segCallFuelRefill uint32

	// loopFuel bounds how many in-segment loop back-edges (FORLOOP /
	// negative-sBx JMP) may run between Go-side preemption points
	// (issue #102). The back-edge emit decrements it; at zero the
	// segment exits via HelperLoopFuel and host.LoopPreempt bills the
	// spent back-edges to the step budget, refills, and runs the
	// standard preemption check.
	//
	// Deliberately SEPARATE from segCallFuel: the dispatcher refills
	// segCallFuel on every Run entry / resume (the host CALL path is
	// itself a billing point, so erasing the drain there is fine), but
	// a loop whose body round-trips through an exit-reason helper each
	// iteration (e.g. a not-yet-warmed CALL) would then never
	// accumulate enough back-edge decrements to trip the guard — and
	// host-closure CALLs never reach st.preempt(), so nothing would
	// ever CHECK the billed budget (probe: 7.7M helper round trips,
	// stepUsed billed past the budget, zero raises). loopFuel is
	// refilled ONLY by LoopPreempt and by RefreshJitCtxAddrs on an
	// armed-state TRANSITION (budget armed <-> disarmed), so the drain
	// survives dispatcher resumes.
	//
	// Initialized to SegCallFuelBudgeted (with an Unlimited refill
	// marker so the pre-arming drain never bills): an unbudgeted State
	// pays one HelperLoopFuel round trip after the first 4096
	// back-edges, gets an Unlimited refill, and stays in-segment for
	// 2^31 back-edges thereafter.
	loopFuel uint32

	// loopFuelRefill mirrors segCallFuelRefill for the loop back-edge
	// counter: records the last SetLoopFuel value so LoopFuelSpent can
	// bill (refill - loopFuel), returning 0 for Unlimited-mode drains
	// (same rationale as SegCallFuelSpent — those back-edges ran while
	// no budget was armed).
	loopFuelRefill uint32
}

// NewJITContext constructs a P4 JIT execution context.
//
// Caller: crescent.State builds one singleton after wireP4 (wiring left to
// PJ2).
//
// The self-managed spill stack (issue #89) is allocated here so every
// translation product gets one; the trampoline switches SP onto it before
// entering a segment, so deep seg2seg recursion descends on this buffer
// instead of the goroutine stack's NOSPLIT allowance.
func NewJITContext() *JITContext {
	c := &JITContext{}
	c.AllocSpillStack()
	// Loop back-edge fuel (issue #102): start with the budgeted quantum
	// but the Unlimited refill marker, so (a) a State that arms a budget
	// mid-life reaches a billing point within the first 4096 back-edges
	// even if RefreshJitCtxAddrs hasn't seen the transition yet, and
	// (b) LoopFuelSpent reports 0 for the pre-arming drain (never bill
	// unbudgeted back-edges to a later-armed budget — PR #95 review
	// note, same rule as segCallFuel).
	c.loopFuel = SegCallFuelBudgeted
	c.loopFuelRefill = SegCallFuelUnlimited
	return c
}

// SpillStackSize is the size of the self-managed spill stack (issue #89).
// 64 KiB per the 05 §3.4 design; at 32 B/level (the conservative seg2seg
// per-level cost) that is ~2048 levels of headroom, far above any depth
// cap we set. The buffer holds only register spills / return addresses,
// never Lua values or Go heap pointers, so it is invisible to the GC.
const SpillStackSize = 64 * 1024

// AllocSpillStack allocates the self-managed spill stack and fills
// spillBase / spillTop (issue #89). spillBase is the high-address end
// (the SP entry point for a down-growing stack), 16-byte aligned as the
// amd64/arm64 ABI requires; spillTop is the low-address growth limit. The
// trampoline reads spillBase via [r15+JITContextSpillBaseOffset] to switch
// SP before entering a segment. Idempotent: a second call keeps the
// existing buffer.
func (c *JITContext) AllocSpillStack() {
	if c.spillBacking != nil {
		return
	}
	c.spillBacking = make([]byte, SpillStackSize)
	lo := uintptr(unsafe.Pointer(&c.spillBacking[0]))
	c.spillTop = lo
	c.spillBase = (lo + uintptr(len(c.spillBacking))) &^ 0xf // 16-byte aligned
}

// SpillBase returns the spill-stack SP entry point (testing hook).
func (c *JITContext) SpillBase() uintptr { return c.spillBase }

// SetPreemptFlag sets the preemption flag (called by the crescent side, read
// atomically by the JIT side).
//
// Caller: on a GC trigger / scheduling yield (crescent.State holds a
// reference to this ctx).
func (c *JITContext) SetPreemptFlag() {
	c.preemptFlag.Store(1)
}

// ClearPreemptFlag clears the preemption flag (called at the trampoline exit).
func (c *JITContext) ClearPreemptFlag() {
	c.preemptFlag.Store(0)
}

// PreemptFlagPending reports whether the preemption flag is set (test hook,
// prove-the-path hit counter).
func (c *JITContext) PreemptFlagPending() bool {
	return c.preemptFlag.Load() != 0
}

// SetArenaBase sets the uintptr of the arena `[]byte` start (per 05 §1.3.3 /
// §3.3 arena base reload protocol). The crescent side computes the current
// arena.Words() start byte address via the host interface at wireP4 + Run
// entry and injects it via this setter.
//
// **PJ2 full-wiring preparation**: together with valueStackBase, this field
// is read by the mmap segment via r15+offset during PJ2 byte-level arithmetic
// codegen — the `movsd xmm0, [r15+arenaBase+vsbase+reg*8]` byte-level
// template. The current simplified PJ7 form does not yet read this field (the
// mmap segment is a dummy mov+ret, and the prelude fetches values via the
// host helper interface).
func (c *JITContext) SetArenaBase(addr uintptr) {
	c.arenaBase = addr
}

// SetValueStackBase sets the uintptr of the current frame's R0 (per 05 §3.3 +
// 06 §4.1).
//
// Calling contract: the Run entry computes valueStackBase = arena.Words().
// bytePtr + (stackBaseW + ci.base) * 8, loaded so the mmap segment addresses
// R(idx) indirectly via r15+offset. This setter is called at the Run entry to
// ensure valueStackBase reflects the current frame's real slot start before
// each P4 frame execution.
func (c *JITContext) SetValueStackBase(addr uintptr) {
	c.valueStackBase = addr
}

// ArenaBase returns the arena base (test hook).
func (c *JITContext) ArenaBase() uintptr { return c.arenaBase }

// ValueStackBase returns the current frame's R0 byte address (test hook).
func (c *JITContext) ValueStackBase() uintptr { return c.valueStackBase }

// SetCIDepthAddr sets the host byte address of the thread.ciDepth mirror word
// (per §9.20 Option B Spike 1). Injected by crescent.State.wireP4.
func (c *JITContext) SetCIDepthAddr(addr uintptr) {
	c.ciDepthAddr = addr
}

// CIDepthAddr returns the host byte address of the thread.ciDepth mirror word
// (test hook).
func (c *JITContext) CIDepthAddr() uintptr { return c.ciDepthAddr }

// SetCISegBaseAddr sets the host byte address of the mirror word holding the
// CI segment's current byte base (per §9.20).
func (c *JITContext) SetCISegBaseAddr(addr uintptr) {
	c.ciSegBaseAddr = addr
}

// CISegBaseAddr returns the host byte address of the CI segment mirror word
// (test hook).
func (c *JITContext) CISegBaseAddr() uintptr { return c.ciSegBaseAddr }

// SetTopAddr sets the host byte address of the thread.top mirror word (per
// §9.20).
func (c *JITContext) SetTopAddr(addr uintptr) {
	c.topAddr = addr
}

// TopAddr returns the host byte address of the thread.top mirror word (test
// hook).
func (c *JITContext) TopAddr() uintptr { return c.topAddr }

// SetAllAddrs writes all five arena-relative address fields at once.
// Called by P4HostState.RefreshJitCtxAddrs so the host implementation
// can compute unsafe.Pointer(&arena.Words()[0]) exactly once and derive
// the four dependent fields from a single base + offset, instead of
// paying the slice-header walk five separate times.
//
// Individual setters (SetArenaBase / SetValueStackBase / SetCIDepthAddr
// / SetCISegBaseAddr / SetTopAddr) remain for callers that legitimately
// need one field.
func (c *JITContext) SetAllAddrs(arenaBase, valueStackBase, ciDepth, ciSegBase, top uintptr) {
	c.arenaBase = arenaBase
	c.valueStackBase = valueStackBase
	c.ciDepthAddr = ciDepth
	c.ciSegBaseAddr = ciSegBase
	c.topAddr = top
}

// SetExitArg0 sets the helper request code (§9.20.9 protocol). ACTIVE on
// amd64/arm64.
func (c *JITContext) SetExitArg0(arg uint64) {
	c.exitArg0 = arg
}

// ExitArg0 returns the helper request code (test hook, per §9.20.9 wiring
// preparation).
func (c *JITContext) ExitArg0() uint64 { return c.exitArg0 }

// SetResumeOff sets the byte offset of the resume entry within the mmap
// segment (per §9.20.9 (2)). Recorded when the compileSpecSelfCall
// useFrameInline branch emits.
func (c *JITContext) SetResumeOff(off uint32) {
	c.resumeOff = off
}

// ResumeOff returns the resume entry byte offset (test hook).
func (c *JITContext) ResumeOff() uint32 { return c.resumeOff }

// SetCodePageAddr sets the mmap segment start (per §9.20.9 (5) resume entry
// computation). The PROT_RX segment start byte address is injected at
// installGibbous time.
func (c *JITContext) SetCodePageAddr(addr uintptr) {
	c.codePageAddr = addr
}

// CodePageAddr returns the mmap segment start (test hook).
func (c *JITContext) CodePageAddr() uintptr { return c.codePageAddr }

// SavedGoGSlot returns &c.savedGoG for the saveGoG asm helper to write.
// PJ10 native usage: at Run entry `saveGoG(ctx.SavedGoGSlot())` snapshots
// the current R14=G into jitCtx; the mmap segment emits
// `mov r14, [r15+savedGoGOff]` before each Go helper call to restore G.
func (c *JITContext) SavedGoGSlot() *uintptr { return &c.savedGoG }

// SavedGoG returns the mirrored Go G value (test hook).
func (c *JITContext) SavedGoG() uintptr { return c.savedGoG }

// SegCallDepth returns the native segment-to-segment nesting depth
// (issue #50 Spike 5 test hook). Nonzero mid-Run would indicate a
// segment-to-segment call is in flight; a leaked nonzero after Run is
// a teardown bug.
func (c *JITContext) SegCallDepth() uint32 { return c.segCallDepth }

// SegCallDeopt returns the segment-to-segment deopt flag (issue #50
// Spike 5 test hook). A leaked nonzero after Run indicates a deopt
// propagation bug.
func (c *JITContext) SegCallDeopt() uint32 { return c.segCallDeopt }

// SetUpvalInlineFields wires the inline-GETUPVAL ABI (issue #50 Spike 5):
// the running frame's closure GCRef, the running thread's stack-slot-0
// host address, and whether it is safe to inline open-upvalue reads
// (single-thread, no coroutine-owned upvalues). Called from
// RefreshJitCtxAddrs on every Run entry / resume.
func (c *JITContext) SetUpvalInlineFields(closureRef, threadStackBase0 uintptr, safe bool) {
	c.currentClosureRef = closureRef
	c.threadStackBase0 = threadStackBase0
	if safe {
		c.inlineUpvalSafe = 1
	} else {
		c.inlineUpvalSafe = 0
	}
}

// SetValueStackEnd sets the running thread's value-stack end address
// (issue #80; see the valueStackEnd field doc). Refreshed alongside
// SetUpvalInlineFields on every Run entry / dispatcher re-entry.
func (c *JITContext) SetValueStackEnd(end uintptr) { c.valueStackEnd = end }

// SegCallFuelUnlimited is the fuel refill for States with no step budget
// and no cancel context: large enough that the dec+jz guard practically
// never fires (a segment call takes >=ns, so 2^31 dispatches is minutes
// of pure in-segment work — and any host exit refills anyway).
const SegCallFuelUnlimited = 1 << 31

// SegCallFuelBudgeted is the fuel refill while a step budget or cancel
// context is armed: the host regains control (and bills st.preempt())
// at least once per this many in-segment CALL dispatches. 4096 keeps
// the budget error latency low (~us at ns-scale dispatches) while
// amortizing the exit-reason round trip to noise.
const SegCallFuelBudgeted = 4096

// SetSegCallFuel refills the seg2seg dispatch fuel (see the segCallFuel
// field doc). Called by the host on every Run entry / dispatcher resume.
func (c *JITContext) SetSegCallFuel(n uint32) {
	c.segCallFuel = n
	c.segCallFuelRefill = n
}

// SegCallFuel returns the remaining fuel (test hook).
func (c *JITContext) SegCallFuel() uint32 { return c.segCallFuel }

// SegCallFuelSpent returns how many in-segment dispatches ran since the
// last SetSegCallFuel, for step-budget billing on the host side. Returns
// 0 when the last refill was SegCallFuelUnlimited: those dispatches ran
// while no budget was armed, and charging them to a budget armed later
// (between Runs on the same State) would spuriously exhaust it on entry.
func (c *JITContext) SegCallFuelSpent() uint32 {
	if c.segCallFuelRefill != SegCallFuelBudgeted {
		return 0
	}
	return c.segCallFuelRefill - c.segCallFuel
}

// SetLoopFuel refills the loop back-edge fuel (issue #102; see the
// loopFuel field doc). Called by host.LoopPreempt on every
// HelperLoopFuel exit, and by RefreshJitCtxAddrs only on an armed-state
// transition — NOT on every refresh, or a loop body that round-trips
// through the dispatcher each iteration would keep resetting the
// counter and never reach a billing point.
func (c *JITContext) SetLoopFuel(n uint32) {
	c.loopFuel = n
	c.loopFuelRefill = n
}

// LoopFuel returns the remaining loop back-edge fuel (test hook).
func (c *JITContext) LoopFuel() uint32 { return c.loopFuel }

// LoopFuelArmedBudgeted reports whether the last loop-fuel refill was
// the budgeted quantum (host-side transition detection).
func (c *JITContext) LoopFuelArmedBudgeted() bool {
	return c.loopFuelRefill == SegCallFuelBudgeted
}

// LoopFuelSpent returns how many in-segment loop back-edges ran since
// the last SetLoopFuel, for step-budget billing. Returns 0 when the
// last refill was Unlimited (mirror of SegCallFuelSpent: unbudgeted
// back-edges must not bill a later-armed budget).
func (c *JITContext) LoopFuelSpent() uint32 {
	if c.loopFuelRefill != SegCallFuelBudgeted {
		return 0
	}
	return c.loopFuelRefill - c.loopFuel
}

// LoopFuelTick decrements the loop back-edge fuel by one and reports
// whether it is exhausted (issue #102). Go-side counterpart of the
// segment's dec+jnz for replay paths (PerOpCode.runForLoop) that
// iterate loops in Go instead of native code: the caller invokes
// host.LoopPreempt when this returns true, exactly like the segment's
// HelperLoopFuel exit. Saturates at 0 instead of wrapping (a stranded
// counter — see the deopt repair in RefreshJitCtxAddrs — then reports
// exhausted every tick until LoopPreempt refills, rather than running
// 2^32 unbilled iterations).
func (c *JITContext) LoopFuelTick() bool {
	if c.loopFuel > 0 {
		c.loopFuel--
	}
	return c.loopFuel == 0
}

// CurrentClosureRef returns the running frame's closure GCRef (test hook
// + used by the segment-to-segment caller emit to bake the restore).
func (c *JITContext) CurrentClosureRef() uintptr { return c.currentClosureRef }

// SetCurrentClosureRef overrides the running frame's closure GCRef (test
// hook; the emit path writes it in-segment for seg2seg callees).
func (c *JITContext) SetCurrentClosureRef(ref uintptr) { c.currentClosureRef = ref }

// SetHostRef stores the opaque host interface header ([2]uintptr:
// itab + data). PJ10 native shims read this to reconstruct the
// P4HostState interface and dispatch to methods.
func (c *JITContext) SetHostRef(h [2]uintptr) { c.hostRef = h }

// HostRef returns the opaque host interface header.
func (c *JITContext) HostRef() [2]uintptr { return c.hostRef }
