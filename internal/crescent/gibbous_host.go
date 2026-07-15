// gibbous trampoline + HostState implementation (VS0-d / PW2-d).
//
// crescent <-> gibbous (P3 wasm) cross-tier bridge (docs/design/p3-wasm-tier/04-trampoline.md):
//   - enterGibbous: when crescent doCall detects a Proto has been promoted to
//     gibbous, it jumps into wazero execution via the bridge's GibbousCode.Run
//     (§2.2). The trampoline logic lives in crescent across all builds and
//     calls Run through the bridge.GibbousCode interface — it does not import
//     the p3-build-only gibbous package (P3/P4 share the same trampoline, §0.4).
//   - HostState methods: callback entry points for gibbous wasm's imported
//     helpers (h_getupval/h_setupval/h_return/h_safepoint) (§3). The method
//     signatures use primitive types (int32/uint64) and live across all builds;
//     the p3 build's gibbous.NewCompiler takes *State as the injected HostState
//     (binding is done in the wangshu_p3 injection file).
package crescent

import (
	"unsafe"

	"github.com/Liam0205/wangshu/internal/bridge"
	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// enterGibbous is the crescent -> gibbous tier-up entry (04 §2.2).
//
// Caller: doCall's gibbous branch (only th==mainTh, §5 thread-level tier rule).
// Precondition: arguments are already moved to funcIdx+1.. (same as host/Lua
// calls; doCall has prepared them).
//
// Three steps: (1) enterLuaFrame pushes the frame (reuses the interpreter's
// stack-reserve/vararg logic, marks gibbous=true) (2) compute the base byte
// offset (value-stack segment base + frame base) (3) code.Run enters wazero.
// Result write-back + frame pop are done by gibbous RETURN via h_return
// (DoReturn) inside Run, so after this function returns the stack state is
// identical to the interpreter having run that frame — doCall returns
// (nil, nil), and the execute main loop reloads ci=currentCI (same as the host
// call path, call.go doCall).
func (st *State) enterGibbous(th *thread, code bridge.GibbousCode, funcIdx, nargs, nresults int) *LuaError {
	if e := st.enterLuaFrame(th, funcIdx, nargs, nresults, false); e != nil {
		return e
	}
	ci := st.gibCI(th)
	ci.SetGibbous(true) // bit50 callStatus_gibbous (04 §1.2): this frame takes the Wasm path
	th.reMirrorTop()    // PW10 R2b-1: re-mirror the ci segment after a cold field (gibbous) change

	// base byte offset: R0's byte address in the shared linear memory =
	//   (value-stack segment word offset stackBaseW + frame base slot) * 8
	//   (each slot is an 8-byte NaN-box u64).
	// This refines 04 §2.2's baseBytes: the stack segment has a non-zero start,
	// so the base = segment base + frame offset.
	baseByte := (th.stackBaseW + uint32(ci.base)) * 8

	status := code.Run(st.gibbousStack(), baseByte)
	// The segment is the authoritative reverse-sync source (PW10 zero-cross
	// Stage 1b): Wasm execution may increment/decrement the ciDepth word + write
	// segment frames (Stage 2/3), so here we reload th.ciDepth/th.cur from the
	// segment as the source of truth. In Stage 1b there is no Wasm write, so the
	// word stays equal to ciDepth -> a verifying no-op.
	th.syncCurFromSeg()
	if status != 0 {
		// ERR: DoReturn/h_raise has already set pendingErr, or a wazero-internal
		// error (PendingErr).
		if st.gibbousPendingErr == nil {
			if e := code.PendingErr(); e != nil {
				st.gibbousPendingErr = &LuaError{Msg: "gibbous: " + e.Error()}
			} else {
				st.gibbousPendingErr = errf("gibbous: run failed (status=%d)", status)
			}
		}
		e := st.gibbousPendingErr
		st.gibbousPendingErr = nil
		// Pop this frame's CallInfo (if DoReturn did not — the ERR path does not
		// go through RETURN).
		if th.ciDepth > 0 && currentCI(th).Gibbous() {
			st.popCallInfo(th)
		}
		return e
	}
	// OK: the result has been written back to funcIdx.. and the frame popped by
	// h_return (DoReturn).
	return nil
}

// gibbousStack returns the reused cross-tier stack buffer (CallWithStack
// zero-alloc path, 04 §2.2 step3 note: PW0 spike measured 14.8ns). len>=1:
// stack[0]=base input arg, and on return stack[0]=status.
func (st *State) gibbousStack() []uint64 {
	if st.gibStack == nil {
		st.gibStack = make([]uint64, 1)
	}
	return st.gibStack
}

// --- HostState implementation (gibbous imported-helper callbacks, 04 §3) ---
//
// The method signatures match the gibbous/wasm HostState interface (primitive
// types); the p3 build injects *State as the HostState. All methods use base
// (this frame's R0 byte offset) or the runningThread's current frame as
// coordinates — gibbous frames and interpreter frames share the same value
// stack (03-memory-model).

// gibCI gets the current frame at the gibbous boundary (PW10 zero-cross (3)b).
// It first runs syncCurFromSeg to reverse-sync with the **segment as the source
// of truth** — the Wasm RETURN fast path (emitReturnFast) decrements the ciDepth
// word + tears down the frame during wasm execution, never returning to Go, so
// Go's th.cur/th.ciDepth are frozen while the segment is live. All HostState
// helper entries get ci through here, ensuring that after Wasm modifies the
// segment the Go side reads the latest frame (not a stale dead frame ->
// register-addressing misalignment corruption, Option A risk #1). syncCurFromSeg
// is idempotent and cheap (a no-op when word==Go), and the slow-path helper has
// already crossed the tier boundary anyway, so the cost is negligible.
func (st *State) gibCI(th *thread) *callInfo {
	th.syncCurFromSeg()
	return &th.cur
}

// SetReg writes val directly into the current frame's R(idx) slot (NaN-box u64;
// dedicated to the gibbous-jit P4 PJ7 simplified form).
//
// **Dependency untangling** (P4HostState interface, jit/host.go): after
// p4Code.Run executes in the mmap segment, it needs to write RAX (a NaN-box
// value) into the R(retA) slot (the arena value-stack proper) — bypassing the
// P3 CallWithStack 1-slot buffer protocol (the P3 stack protocol is
// incompatible with P4).
//
// Parameters:
//   - idx: register number (R(idx), = ci.base + idx, i.e. the thread slot index)
//   - val: NaN-box u64 value
//
// Implementation: computes the slot via ci.base + idx and writes directly with
// setSlot. This method's semantics match `execute.go::SETREG`-class operations
// (direct arena value-stack write, no GC barrier — since a NaN-box u64 write is
// an atomic single word).
func (st *State) SetReg(idx int32, val uint64) {
	th := st.runningThread
	ci := st.gibCI(th)
	th.setSlot(ci.base+int(idx), value.Value(val))
}

// GetReg reads the current frame's R(idx) (P4HostState interface, dual of
// SetReg).
func (st *State) GetReg(idx int32) uint64 {
	th := st.runningThread
	ci := st.gibCI(th)
	return uint64(th.slot(ci.base + int(idx)))
}

// SetUpvalFromReg writes R(a) into the current closure's upvalue b (same as the
// execute.go SETUPVAL section; a P4HostState-dedicated "read reg + write
// upvalue" atomic helper that avoids introducing the two round-trips of a
// generic GetReg+SetUpval).
func (st *State) SetUpvalFromReg(base int32, a int32, b int32) {
	th := st.runningThread
	ci := st.gibCI(th)
	uv := object.ClosureUpvalRef(st.arena, ci.Cl(), uint16(b))
	st.upvalSet(th, uv, reg(th, ci, int(a)))
}

// ArenaBaseAddr returns the uintptr of the arena `[]byte`'s start (per 05 §3.3
// P4HostState interface). Prepared for the PJ2 full speculative template — the
// mmap segment reads this field via r15+offset and then byte-addresses value
// stack slots. The current PJ7 simplified form does not call it.
//
// **arena relocation**: Words() returns a new slice on grow, and this field
// returns the current Words start. The caller (jit.Compile) computes it live at
// the Run entry and does not cache it (per 05 §5 arena base reload protocol —
// the arena-view aliasing grow hazard, see [[feedback-arena-view-aliasing]]).
func (st *State) ArenaBaseAddr() uintptr {
	words := st.arena.Words()
	if len(words) == 0 {
		return 0
	}
	return uintptr(unsafe.Pointer(&words[0]))
}

// ValueStackBaseAddr returns the byte address of the current frame's R0 (per 05
// §3.3 + 06 §4.1 rbx = valueStackBase).
//
// The base parameter is the byte offset computed by enterGibbous
// (`(stackBaseW + ci.base) * 8`), and this function returns the arena.Words
// start uintptr + base. **arena grow hazard**: same as ArenaBaseAddr, must be
// computed live at the Run entry, not cached.
func (st *State) ValueStackBaseAddr(base int32) uintptr {
	words := st.arena.Words()
	if len(words) == 0 {
		return 0
	}
	return uintptr(unsafe.Pointer(&words[0])) + uintptr(base)
}

// CIDepthHostAddr returns the host byte address of the thread.ciDepth mirror
// word (per docs/design/p4-method-jit/implementation-progress.md §9.20 Option B
// Spike 1 + P4HostState interface).
//
// **Reuses the P3 PW10 Stage 1a mirror word** (st.ciDepthRef): the same arena
// mirror word, written on the crescent side via thread.setCIDepth
// (`a.SetWordAt(st.ciDepthRef, ...)`), and byte-level inc/dec'd by the P4 mmap
// segment through the host addr this returns (enterLuaFrame + popCallInfo
// byte-level inline).
//
// Returns = arena.Words().bytePtr + (ciDepthRef bytes). **arena relocation**:
// same as ArenaBaseAddr, reloaded from jitContext on returning to the JIT world
// after a grow; in the Spike 1 phase it is computed live and injected at each
// Run entry (per 05 §5 arena base reload protocol).
func (st *State) CIDepthHostAddr() uintptr {
	words := st.arena.Words()
	if len(words) == 0 {
		return 0
	}
	return uintptr(unsafe.Pointer(&words[0])) + uintptr(st.ciDepthRef)
}

// CISegBaseHostAddr returns the host byte address of the CI segment's current
// byte-base mirror word (per §9.20 Option B Spike 1).
//
// **Reuses the P3 PW10 Stage 2 mirror word** (st.ciSegBaseRef): the CI segment
// is relocatable (growCISeg / newThread update ciBaseW), and syncCISegBase
// mirrors ciBaseW*8 into this arena word. The P4 mmap segment dereferences the
// host addr this returns to get the current CI segment base, then computes the
// CallInfo[depth] frame address (base + depth*40).
func (st *State) CISegBaseHostAddr() uintptr {
	words := st.arena.Words()
	if len(words) == 0 {
		return 0
	}
	return uintptr(unsafe.Pointer(&words[0])) + uintptr(st.ciSegBaseRef)
}

// TopHostAddr returns the host byte address of the thread.top mirror word (per
// §9.20 Option B Spike 1).
//
// **Reuses the P3 PW10 Stage 1a mirror word** (st.topRef): top is a stack slot
// index, written by the P4 mmap segment when enterLuaFrame sets the callee
// frame top (top = base + MaxStack).
func (st *State) TopHostAddr() uintptr {
	words := st.arena.Words()
	if len(words) == 0 {
		return 0
	}
	return uintptr(unsafe.Pointer(&words[0])) + uintptr(st.topRef)
}

// RefreshJitCtxAddrs lives in gibbous_host_p4.go (needs the jit package
// which is wangshu_p4-tagged; keeping it out of this untagged file avoids
// pulling jit into non-P4 builds).

// GetUpval gets the current closure's upvalue b (same as the execute.go
// GETUPVAL section).
func (st *State) GetUpval(base int32, b int32) uint64 {
	th := st.runningThread
	ci := st.gibCI(th)
	uv := object.ClosureUpvalRef(st.arena, ci.Cl(), uint16(b))
	return uint64(st.upvalGet(th, uv))
}

// SetUpval writes the current closure's upvalue b (same as the execute.go
// SETUPVAL section).
func (st *State) SetUpval(base int32, b int32, val uint64) {
	th := st.runningThread
	ci := st.gibCI(th)
	uv := object.ClosureUpvalRef(st.arena, ci.Cl(), uint16(b))
	st.upvalSet(th, uv, value.Value(val))
}

// DoReturn handles gibbous RETURN A B (h_return, 04 §4.7).
//
// It mirrors doReturn's non-terminal path (call.go doReturn): moveResults
// writes R(A..A+nret-1) back starting at funcIdx, adjusts to the caller's
// nresults (truncating or padding), pops this frame's CallInfo, and restores
// the caller top. The gibbous frame is popped here by the trampoline
// (symmetric to enterLuaFrame's push); returns status=0.
func (st *State) DoReturn(base int32, pc int32, a int32, b int32) int32 {
	th := st.runningThread
	st.doReturnHits++ // PW10 zero-cross (3)b verification: the fast path does not go through here (a stalled count proves the fast path is active)
	ci := st.gibCI(th)
	ci.pc = pc // materialize pc (savedPC, 04 §4.5; used by traceback)
	var nret int
	if b == 0 {
		nret = th.top - (ci.base + int(a))
	} else {
		nret = int(b) - 1
	}
	st.closeUpvals(th, ci.base)
	dst := ci.FuncIdx()
	src := ci.base + int(a)
	for k := 0; k < nret; k++ {
		th.setSlot(dst+k, th.slot(src+k))
	}
	wantedN := ci.NResults()
	st.popCallInfo(th)
	if wantedN < 0 {
		th.setTop(dst + nret)
	} else {
		for k := nret; k < wantedN; k++ {
			th.setSlot(dst+k, value.Nil)
		}
		if th.ciDepth > 0 {
			caller := st.gibCI(th)
			th.setTop(caller.base + int(st.protoOf(caller).MaxStack))
		} else {
			th.setTop(dst + wantedN)
		}
	}
	// PW10 R3: write the post-pop caller base byte offset into the transfer
	// word, for the caller's call_indirect to read and resume after returning
	// (needed to refresh base when the caller is gibbous-via-call_indirect — the
	// callee may have growStack-relocated the segment). Ignored (harmless) when
	// the caller is non-gibbous or takes the baseline Run path.
	if th.ciDepth > 0 {
		caller := st.gibCI(th)
		st.arena.SetWordAt(st.ciTransferRef, uint64((th.stackBaseW+uint32(caller.base))*8))
	}
	return 0 // OK
}

// raiseGibbous anchors and stashes the error thrown by a gibbous frame (PW10
// R3c-fix).
//
// **Why anchor at the throw site**: as a gibbous error bubbles up the status
// chain, the frames along the way (the gibbous callee via PopErrFrame, the
// gibbous caller via its enterGibbous launcher) get popped -> by the time it
// bubbles to the top-level executeFrom, currentCI is no longer the throwing
// frame, so annotateError reads the wrong frame -> line-number/traceback drift
// (R3c known regression). The fix: at the throw site (where currentCI is still
// the throwing frame), immediately annotateError to anchor the
// "chunkname:line:" prefix + materialize the traceback, and mark annotated; the
// top-level executeFrom sees annotated and skips re-annotating, so the line
// number is no longer affected by subsequent frame pops.
//
// Consistent with the interpreter: when the interpreter annotates at the
// top-level executeFrom, currentCI is exactly the throwing frame (the
// interpreter does not pop frames on the error path), so the annotated line =
// throwing-frame line. gibbous anchors at the throw site to reach the same line
// number, making gibbous error messages match the interpreter (byte-equal
// across tiers).
//
// Idempotent: when e is already annotated (from a deeper metamethod
// sub-call's interpreter annotation), annotateError returns directly without
// re-adding the prefix. Returns 1 (the status-chain ERR code); call sites do
// `return st.raiseGibbous(e)`.
func (st *State) raiseGibbous(e *LuaError) int32 {
	th := st.runningThread
	if th.ciDepth > 0 {
		e = st.annotateError(e, currentCI(th))
		if e != nil && e.Traceback == "" {
			e.Traceback = st.buildTraceback(th)
		}
	}
	st.gibbousPendingErr = e
	return 1
}

// PopErrFrame pops the leftover gibbous callee frame when a call_indirect
// direct call fails (PW10 R3).
//
// When a gibbous callee errors, its own `return 1` does not pop the frame
// (symmetric to baseline: it is the callee's enterGibbous launcher that pops).
// R3 call_indirect direct calls skip the intermediate launcher, so when
// call_indirect returns non-zero the caller wasm calls this helper to pop —
// precisely replicating the baseline enterGibbous error path's "pop only if
// currentCI is a gibbous frame" condition (gibbous_host.go enterGibbous ERR
// branch), keeping the ciDepth/currentCI trajectory frame-for-frame identical.
// Otherwise the top-level executeFrom's annotateError reads the wrong currentCI
// -> the error line-number prefix changes -> breaking the cross-tier byte-equal
// diff (V1-V13).
//
// When a callee errors but currentCI is not gibbous (the callee took the
// fallback sync path running a crescent sub-frame, and that sub-frame errored
// and was left behind), do not pop — same condition as baseline enterGibbous
// (left to the protected boundary's truncateCI).
func (st *State) PopErrFrame() {
	th := st.runningThread
	if th.ciDepth > 0 && st.gibCI(th).Gibbous() {
		st.popCallInfo(th)
	}
}

// loopFuelQuantum is the refill amount for gibbous back-edge fuel when a
// budget/ctx is armed: every this-many back-edges cross the tier boundary into
// h_safepoint once, charging this batch to stepBudget. The ~143ns cross-tier
// cost is amortized over quantum iterations (64 -> about 2ns per iteration).
// Mirrors the intent of P4's HelperLoopFuel=32 (P3 cross-tier is slightly more
// expensive, so a slightly larger value).
const loopFuelQuantum = 64

// loopFuelUnlimited is the refill amount when there is no budget/ctx: a large
// value so a steady-state loop almost never crosses the tier boundary (mirrors
// P4's SegCallFuelUnlimited). Stored as i32, well below MaxInt32 to leave
// self-decrement headroom. About 1 billion back-edges per cross, so a plain
// embedding never notices.
const loopFuelUnlimited = 1 << 30

// Safepoint is the back-edge checkpoint (h_safepoint, 04 §3.3): GC + loop
// step-budget accounting. Returns 0 for normal / 1 for raise (budget exceeded
// or ctx canceled, same semantics as the interpreter's preempt).
//
// A gibbous back-edge inline-decrements the loopBudget word, and only crosses
// the tier boundary here when it hits zero (or gcPending is set). Dual of P4's
// host.LoopPreempt (the P3 version of issue #102): a fully inlined loop body
// (pure arithmetic / no Lua frame) has no other preemption point, so it is only
// here that the consumed quantum is charged to stepBudget and checked —
// otherwise an infinite loop (for i=0,1/0 do X=0 end) would hang forever after
// tier-up to P3.
func (st *State) Safepoint(base int32, pc int32) int32 {
	st.safepointCalls++
	st.gc.MaybeCollect()
	// Refill loop fuel + charge. When a budget/ctx is armed, use a small quantum
	// (periodically returning here to check); otherwise use a large amount
	// (steady-state, unnoticed). The current word value = refill - spent; the
	// back-edge only crosses when the word <= 0, so spent is roughly the last
	// refill. Charge precisely using "last refill - current value".
	cur := int64(int32(st.arena.WordAt(st.loopBudgetRef)))
	if st.stepBudget > 0 || st.ctx.Load() != nil {
		spent := int64(st.loopFuelRefill) - cur
		if spent < 0 {
			spent = 0
		}
		st.stepUsed += spent
		st.loopFuelRefill = loopFuelQuantum
		st.arena.SetWordAt(st.loopBudgetRef, uint64(uint32(loopFuelQuantum)))
		if st.stepBudget > 0 && st.stepUsed > st.stepBudget {
			return st.raiseGibbousAtPC(pc, errf("instruction budget exceeded"))
		}
		if h := st.ctx.Load(); h != nil {
			if err := h.err(); err != nil {
				return st.raiseGibbousAtPC(pc, errf("context canceled: %s", err.Error()))
			}
		}
		return 0
	}
	st.loopFuelRefill = loopFuelUnlimited
	st.arena.SetWordAt(st.loopBudgetRef, uint64(uint32(loopFuelUnlimited)))
	return 0
}

// raiseGibbousAtPC anchors the error line number (pc), then stashes via
// raiseGibbous and returns 1. A back-edge budget-exceeded error's line falls on
// the back-edge instruction's pc (consistent with the interpreter's preempt
// behavior).
func (st *State) raiseGibbousAtPC(pc int32, e *LuaError) int32 {
	if th := st.runningThread; th != nil && th.ciDepth > 0 {
		st.gibCI(th).pc = pc + 1 // errWithName reads ci.pc-1 == pc
	}
	return st.raiseGibbous(e)
}

// SetSavedPC writes back the current frame's savedPC (materialize pc, 04 §4.5).
func (st *State) SetSavedPC(base int32, pc int32) {
	st.gibCI(st.runningThread).pc = pc
}

// --- PW3 arithmetic slow-path helpers (fast path for two numbers is emitted
// inline in Wasm; on failure it falls back to Go) ---
//
// These reconstruct a bytecode.Instruction to reuse the interpreter's
// doArith/doArithSlow/doConcat/LEN logic, ensuring the gibbous slow path is
// byte-for-byte isomorphic with crescent. Inside the helper, the ci obtained
// via gibCI is the gibbous frame (enterGibbous already pushed it), and register
// addressing goes through reg/setReg (already arena-ized in VS0-c).

// Arith is the arithmetic slow path (ADD/SUB/MUL/DIV/MOD/POW). op is the
// bytecode.OpCode value; it calls doArith directly (which re-checks the fast
// path + slow-path coercion/metamethods), isomorphic with the interpreter.
func (st *State) Arith(base, pc, op, b, c, a int32) int32 {
	th := st.runningThread
	ci := st.gibCI(th)
	ci.pc = pc + 1 // materialize pc: when the interpreter executes this op ci.pc has already ++'d, so errWithName's ci.pc-1==pc (R3c-fix)
	ins := bytecode.EncodeABC(bytecode.OpCode(op), int(a), int(b), int(c))
	if e := st.doArith(th, ci, ins); e != nil {
		return st.raiseGibbous(e)
	}
	return 0
}

// Unm is the UNM slow path (string coercion + __unm). It reconstructs the UNM
// instruction to reuse the execute.go UNM section logic (here directly re-runs
// that section's slow-path branch).
func (st *State) Unm(base, pc, b, a int32) int32 {
	th := st.runningThread
	ci := st.gibCI(th)
	ci.pc = pc + 1 // R3c-fix: errWithName's ci.pc-1==pc (the failing op index)
	bv := reg(th, ci, int(b))
	if f, ok := st.toNumberCoerce(bv); ok {
		setReg(th, ci, int(a), value.NumberValue(-f))
		return 0
	}
	h := st.metaFieldOfValue(bv, "__unm")
	if h == value.Nil {
		return st.raiseGibbous(st.errWithName(ci, "perform arithmetic on", int(b), bv))
	}
	res, e := st.callMetaHandler(th, h, []value.Value{bv, bv}, 1)
	if e != nil {
		return st.raiseGibbous(e)
	}
	setReg(th, ci, int(a), res)
	return 0
}

// Len handles LEN (string length / table border / error on other types; reuses
// the execute.go LEN section).
func (st *State) Len(base, pc, b, a int32) int32 {
	th := st.runningThread
	ci := st.gibCI(th)
	ci.pc = pc + 1 // R3c-fix
	bv := reg(th, ci, int(b))
	switch value.Tag(bv) {
	case value.TagString:
		n := object.StringLen(st.arena, value.GCRefOf(bv))
		setReg(th, ci, int(a), value.NumberValue(float64(n)))
		return 0
	case value.TagTable:
		border := st.rawBorder(value.GCRefOf(bv))
		setReg(th, ci, int(a), value.NumberValue(float64(border)))
		return 0
	default:
		return st.raiseGibbous(st.errWithName(ci, "get length of", int(b), bv))
	}
}

// Concat handles CONCAT (reuses the full execute.go doConcat logic +
// safepoint).
func (st *State) Concat(base, pc, a, b, c int32) int32 {
	th := st.runningThread
	ci := st.gibCI(th)
	ci.pc = pc + 1 // R3c-fix
	ins := bytecode.EncodeABC(bytecode.CONCAT, int(a), int(b), int(c))
	if e := st.doConcat(th, ci, ins); e != nil {
		return st.raiseGibbous(e)
	}
	st.safepoint(th, ci)
	return 0
}

// Compare is the LT/LE slow path (string comparison / __lt/__le metamethods;
// reuses doCompare). Returns packed: bit0=comparison result, bit1=error flag.
func (st *State) Compare(base, pc, op, b, c int32) int32 {
	th := st.runningThread
	ci := st.gibCI(th)
	ci.pc = pc + 1 // R3c-fix
	ins := bytecode.EncodeABC(bytecode.OpCode(op), 0, int(b), int(c))
	res, e := st.doCompare(th, ci, ins)
	if e != nil {
		st.raiseGibbous(e)
		return 2 // bit1 = error (raiseGibbous has set pendingErr + anchored the line number)
	}
	if res {
		return 1 // bit0 = true
	}
	return 0
}

// Eq handles EQ's __eq metamethod path (when raw not-equal; reuses the
// doCompare EQ branch). Returns packed: bit0=result, bit1=error.
func (st *State) Eq(base, pc, b, c int32) int32 {
	th := st.runningThread
	ci := st.gibCI(th)
	ci.pc = pc + 1 // R3c-fix
	ins := bytecode.EncodeABC(bytecode.EQ, 0, int(b), int(c))
	res, e := st.doCompare(th, ci, ins)
	if e != nil {
		st.raiseGibbous(e)
		return 2
	}
	if res {
		return 1
	}
	return 0
}

// ForPrep handles FORPREP three-slot validation + coercion + pre-decrement
// (reuses the execute.go FORPREP section logic). Returns status (0=OK / 1=ERR).
func (st *State) ForPrep(base, pc, a int32) int32 {
	th := st.runningThread
	ci := st.gibCI(th)
	ci.pc = pc + 1 // R3c-fix
	ra := int(a)
	init, ok1 := st.toNumberCoerce(reg(th, ci, ra))
	limit, ok2 := st.toNumberCoerce(reg(th, ci, ra+1))
	step, ok3 := st.toNumberCoerce(reg(th, ci, ra+2))
	if !ok1 {
		return st.raiseGibbous(errf("'for' initial value must be a number"))
	}
	if !ok2 {
		return st.raiseGibbous(errf("'for' limit must be a number"))
	}
	if !ok3 {
		return st.raiseGibbous(errf("'for' step must be a number"))
	}
	// Pre-decrement + normalize the three slots to numbers (after entering
	// FORLOOP the fast path no longer needs to re-check types).
	setReg(th, ci, ra, value.NumberValue(init-step))
	setReg(th, ci, ra+1, value.NumberValue(limit))
	setReg(th, ci, ra+2, value.NumberValue(step))
	return 0
}

// --- PW5 table IC slow-path helpers (fast path inlines a hash probe; on
// invalidation/complex forms it falls back to Go) ---
//
// materialize pc: gibbous passes the opcode index pc; when the interpreter
// executes this opcode ci.pc has already ++'d (pointing at the next one), so we
// set ci.pc=pc+1 to make enhanceIndexErr's ci.pc-1 == pc (describeReg picks the
// current instruction). icGetTable/icSetTable's pc parameter = IC slot index =
// opcode index. icGetTable may re-enter execute via the __index metamethod
// (appending cis) -> refresh ci after it returns.

// GetTable handles the GETTABLE A B C slow path (same as execute.go :101-112).
func (st *State) GetTable(base, pc, a, b, c int32) int32 {
	th := st.runningThread
	ci := st.gibCI(th)
	ci.pc = pc + 1
	proto := st.protoOf(ci)
	tbl := reg(th, ci, int(b))
	key := rk(th, ci, proto, int(c))
	v, e := st.icGetTable(th, ci, pc, tbl, key)
	if e != nil {
		return st.raiseGibbous(st.enhanceIndexErr(e, ci, int(b), tbl))
	}
	ci = st.gibCI(th)
	setReg(th, ci, int(a), v)
	return 0
}

// SetTable handles the SETTABLE A B C slow path (same as execute.go :114-124 +
// safepoint).
func (st *State) SetTable(base, pc, a, b, c int32) int32 {
	th := st.runningThread
	ci := st.gibCI(th)
	ci.pc = pc + 1
	proto := st.protoOf(ci)
	tbl := reg(th, ci, int(a))
	key := rk(th, ci, proto, int(b))
	val := rk(th, ci, proto, int(c))
	if e := st.icSetTable(th, ci, pc, tbl, key, val); e != nil {
		return st.raiseGibbous(st.enhanceIndexErr(e, ci, int(a), tbl))
	}
	ci = st.gibCI(th)
	st.safepoint(th, ci)
	return 0
}

// DoGetGlobal handles the GETGLOBAL A Bx slow path (same as execute.go :78-88).
func (st *State) DoGetGlobal(base, pc, a, bx int32) int32 {
	th := st.runningThread
	ci := st.gibCI(th)
	ci.pc = pc + 1
	proto := st.protoOf(ci)
	key := proto.Consts[bx]
	gv := value.MakeGC(value.TagTable, st.globals)
	v, e := st.icGetTable(th, ci, pc, gv, key)
	if e != nil {
		return st.raiseGibbous(e)
	}
	ci = st.gibCI(th)
	setReg(th, ci, int(a), v)
	return 0
}

// DoSetGlobal handles the SETGLOBAL A Bx slow path (same as execute.go :90-99 +
// safepoint).
func (st *State) DoSetGlobal(base, pc, a, bx int32) int32 {
	th := st.runningThread
	ci := st.gibCI(th)
	ci.pc = pc + 1
	proto := st.protoOf(ci)
	key := proto.Consts[bx]
	gv := value.MakeGC(value.TagTable, st.globals)
	if e := st.icSetTable(th, ci, pc, gv, key, reg(th, ci, int(a))); e != nil {
		return st.raiseGibbous(e)
	}
	ci = st.gibCI(th)
	st.safepoint(th, ci)
	return 0
}

// Self handles SELF A B C (same as execute.go :134-144). The helper includes
// the self-pass R(A+1):=R(B), which is idempotent with the inline fast path's
// store (on an inline miss the store already happened, and the helper redoing
// it has no side effect).
func (st *State) Self(base, pc, a, b, c int32) int32 {
	th := st.runningThread
	ci := st.gibCI(th)
	ci.pc = pc + 1
	proto := st.protoOf(ci)
	tbl := reg(th, ci, int(b))
	setReg(th, ci, int(a)+1, tbl)
	key := rk(th, ci, proto, int(c))
	v, e := st.icGetTable(th, ci, pc, tbl, key)
	if e != nil {
		return st.raiseGibbous(st.enhanceIndexErr(e, ci, int(b), tbl))
	}
	ci = st.gibCI(th)
	setReg(th, ci, int(a), v)
	return 0
}

// NewTable handles NEWTABLE A B C (same as execute.go :126-132; allocation + GC
// all inside the helper).
func (st *State) NewTable(base, pc, a, b, c int32) int32 {
	th := st.runningThread
	ci := st.gibCI(th)
	ci.pc = pc + 1
	asz := bytecode.Fb2Int(uint32(b))
	hsz := bytecode.Fb2Int(uint32(c))
	t := st.allocTable(asz, roundUpPow2(hsz))
	setReg(th, ci, int(a), value.MakeGC(value.TagTable, t))
	st.safepoint(th, ci)
	return 0
}

// SetList handles SETLIST A B C (same as execute.go :385-386 / doSetList +
// safepoint). doSetList may consume the C=0 "next instruction holds the large
// batch number" -> reading Proto.Code[ci.pc] and ci.pc++, so we must first set
// ci.pc to just past the opcode (pc+1), consistent with the interpreter's
// post-fetch state.
func (st *State) SetList(base, pc, a, b, c int32) int32 {
	th := st.runningThread
	ci := st.gibCI(th)
	ci.pc = pc + 1
	ins := bytecode.EncodeABC(bytecode.SETLIST, int(a), int(b), int(c))
	if e := st.doSetList(th, ci, ins); e != nil {
		return st.raiseGibbous(e)
	}
	ci = st.gibCI(th)
	st.safepoint(th, ci)
	return 0
}

// GlobalsRaw returns the globals table's NaN-box u64 (burned as an immediate at
// compile time; used by the GETGLOBAL/SETGLOBAL inline fast path). globals has a
// constant identity that never moves during the State's lifetime (arena objects
// do not relocate).
func (st *State) GlobalsRaw() uint64 {
	return uint64(value.MakeGC(value.TagTable, st.globals))
}

// GCPendingAddr returns the linear-memory byte address of the gcPending flag
// word (P3 PW9). The gibbous FORLOOP back-edge inline-reads it (i32.load) and
// only crosses the tier boundary to call h_safepoint when non-zero.
func (st *State) GCPendingAddr() uint32 {
	return uint32(st.gcPendingRef)
}

// LoopBudgetAddr returns the linear-memory byte address of the loop-budget fuel
// word (P3 loop step-budget fix). The gibbous back-edge inline-decrements it and
// only crosses the tier boundary to h_safepoint when it hits zero.
func (st *State) LoopBudgetAddr() uint32 {
	return uint32(st.loopBudgetRef)
}

// CITransferAddr returns the linear-memory byte address of the ci-transfer
// relay word (P3 PW10 R3). A gibbous->gibbous call_indirect direct call passes
// the callee/refreshed base byte offset through this word.
func (st *State) CITransferAddr() uint32 {
	return uint32(st.ciTransferRef)
}

// CIDepthAddr returns the linear-memory byte address of the ci-depth cursor
// word (P3 PW10 zero-cross Stage 1a). The Wasm side increments/decrements this
// i32 word during frame build/teardown, avoiding a return to Go to modify
// th.ciDepth.
func (st *State) CIDepthAddr() uint32 {
	return uint32(st.ciDepthRef)
}

// CISegBaseAddr returns the linear-memory byte address of the ci-seg-base word
// (P3 PW10 zero-cross Stage 2). This word holds the CI segment's current byte
// base (relocatable); the Wasm-side frame build/teardown reads it to compute
// the frame address live (segment base + depth*ciWords*8 + word*8).
func (st *State) CISegBaseAddr() uint32 {
	return uint32(st.ciSegBaseRef)
}

// OpenGuardAddr returns the linear-memory byte address of the open-upvalue guard
// word (P3 PW10 zero-cross Stage 2). Word value = maxOpenIdx+1 (has open
// upvalues) / 0 (none); the Wasm RETURN fast-path guard frameBase >= this value
// <=> this frame has no open upvalues to close (closeUpvals is a no-op).
func (st *State) OpenGuardAddr() uint32 {
	return uint32(st.openGuardRef)
}

// TopAddr returns the linear-memory byte address of the top mirror word (P3
// PW10 zero-cross (1)). Word value = th.top (slot index); the Wasm frame build
// writes it when setting the callee frame top / the caller restores top itself,
// and the GC stack-root scan reads it to bound [0,top). A slot-index coordinate
// (grow-safe). The address is constant during the State's lifetime.
func (st *State) TopAddr() uint32 {
	return uint32(st.topRef)
}

// ProtoCacheBaseAddr returns the byte address of the proto-field-cache segment
// base mirror word (PW10 zero-cross infra-b). The Wasm (4) emitCall guard fast
// path reads this base + protoID*8 live to get the callee Proto's
// MaxStack/NumParams/IsVararg/NeedsArg cache, avoiding a Go map lookup of Proto
// fields. This mirror word's address is constant during the State's lifetime;
// the segment itself is relocatable (LoadProgram reallocates) and is read live
// through this word.
func (st *State) ProtoCacheBaseAddr() uint32 {
	return uint32(st.protoCacheBaseRef)
}

// FastCallHitsAddr returns the byte address of the (4) emitCall guard fast-path
// hit-count word (PW10 zero-cross (4) verification). On a Wasm hit it i64++'s;
// the Go test reads the word together with indirectCalls to assert the path was
// hit.
func (st *State) FastCallHitsAddr() uint32 {
	return uint32(st.fastCallHitsRef)
}

// --- PW7 closure construction + scope upvalue closing (all via helpers,
// reusing the interpreter) ---

// Closure handles CLOSURE A Bx (same as execute.go:394-397). After makeClosure
// reads it, the following pseudo-instructions (MOVE/GETUPVAL at ci.pc) consume
// the upvalue captures — so we must first set ci.pc to just past CLOSURE (pc+1),
// consistent with the interpreter's post-fetch state. No base refresh needed
// (does not enter a nested frame, does not growStack).
func (st *State) Closure(base, pc, a, bx int32) int32 {
	th := st.runningThread
	ci := st.gibCI(th)
	ci.pc = pc + 1
	ins := bytecode.EncodeABx(bytecode.CLOSURE, int(a), int(bx))
	cl := st.makeClosure(th, ci, ins)
	setReg(th, ci, int(a), value.MakeGC(value.TagFunction, cl))
	st.safepoint(th, ci)
	return 0
}

// Close handles CLOSE A (same as execute.go:391-392): closes all open upvalues
// >= base+A.
func (st *State) Close(base, pc, a int32) int32 {
	th := st.runningThread
	ci := st.gibCI(th)
	st.closeUpvals(th, ci.base+int(a))
	return 0
}

// TForLoop handles TFORLOOP A C (same as execute.go:355-383): calls the
// iterator R(A)(R(A+1),R(A+2)) and lands the results in R(A+3..A+2+C). First
// value non-nil -> control variable R(A+2):=first value, continue; first value
// nil -> exit.
//
// **base refresh (PW4b core)**: the iterator call via callLuaFromHost may
// growStack, relocating the value-stack segment in the arena (stackBaseW
// changes), making a stale base invalid = UAF (same as PW6 h_call, see
// design-claims-vs-codebase-physics §2). Returns i64:
//
//	>=0 = the refreshed base byte offset for this frame (continue the loop) /
//	-1 = ERR / -2 = exit (first value nil).
func (st *State) TForLoop(base, pc, a, c int32) int64 {
	th := st.runningThread
	ci := st.gibCI(th)
	ci.pc = pc + 1
	ra := int(a)
	iter := reg(th, ci, ra)
	state := reg(th, ci, ra+1)
	ctrl := reg(th, ci, ra+2)
	results, e := st.callLuaFromHostNamed(th, iter, []value.Value{state, ctrl})
	if e != nil {
		// PUC getfuncname treats OP_TFORLOOP as a named call site (issue #133);
		// pc is this TFORLOOP instruction's index; resolveArgError rewrites the
		// function name before raiseGibbous anchors the line number.
		st.raiseGibbous(st.resolveArgError(e, ci, pc, ra))
		return -1
	}
	ci = st.gibCI(th)
	for k := 0; k < int(c); k++ {
		v := value.Nil
		if k < len(results) {
			v = results[k]
		}
		setReg(th, ci, ra+3+k, v)
	}
	if c >= 1 && len(results) >= 1 && results[0] != value.Nil {
		setReg(th, ci, ra+2, results[0]) // control variable = first return value, continue the loop
		return int64((th.stackBaseW + uint32(ci.base)) * 8)
	}
	return -2 // first value nil: exit the loop
}

// ExecutePlainCallInlineFrame is the plain-CALL variant of
// ExecuteCalleeFromInlineFrame (issue #50 Spike 2). Mirrors the SELF
// path but does NOT add an implicit self to nargs — the mmap segment
// already wrote the callee CI slot with the correct base (= caller
// base + A + 1, no self shift) and passes the raw nargs = CALL.B - 1.
//
// Segment protocol (Spike 2 minimal form):
//
//  1. segment wrote CI[ciDepth-1] with 5 words:
//     word0 = base|funcIdx (caller base + callA is funcIdx; callee's
//     base = funcIdx + 1 for the non-vararg fixed-arity form)
//     word1 = top | pc     (top will be set to caller.base + callArgCount
//     + callee.MaxStack when we know callee's MaxStack — segment
//     leaves top at 0; helper sets it below via th.setTop)
//     word2 = protoID | (nresults & 0xFFFF)<<32
//     word3 = closure GCRef (payload only)
//     word4 = nVarargs = 0 (Spike 2 rejects vararg callees)
//  2. segment incremented the ciDepth mirror word by 1.
//  3. segment RETs with jitCtx.exitReason = ExitInlineHelper +
//     exitArg0 = HelperExecutePlainCall packed with (callA, nargs, nresults).
//
// Helper flow (mirror of ExecuteCalleeFromInlineFrame with the +1
// difference removed):
//
//  1. Sync Go th.ciDepth from mirror (segment bumped it).
//  2. Read CI[ciDepth-1].cl for callee validation.
//  3. Decrement th.ciDepth to undo the segment bump (enterLuaFrame
//     will re-bump).
//  4. funcIdx = th.cur.base + callA.
//  5. If callee is P4-promoted and on mainTh → enterGibbous
//     (zero-cross); else enterLuaFrame + executeFrom.
//  6. Bump th.ciDepth back by 1 to leave the segment's PopFrame
//     symmetric.
//
// Return: 0=OK / 1=ERR (raiseGibbous already set state.pendingErr).
func (st *State) ExecutePlainCallInlineFrame(base, callA, nargs, nresults int32) int32 {
	_ = base
	th := st.runningThread
	// The segment guard already validated R(callA) is a mono Lua
	// closure matching the IC protoID; it did NOT push a CI frame or
	// bump ciDepth (Spike 2 keeps frame management Go-side — segment
	// only guards + exits). So th.cur is still the caller frame and
	// ciDepth is unchanged. funcIdx addresses the callee closure in
	// the caller's frame exactly like host.CallBaseline / doCall.
	ci := st.gibCI(th)
	funcIdx := ci.base + int(callA)
	callee := th.slot(funcIdx)
	if value.Tag(callee) != value.TagFunction {
		// Guard should have prevented this; fall back to the raise.
		return st.raiseGibbous(st.errWithName(ci, "call", int(callA), callee))
	}
	cl := value.GCRefOf(callee)
	calleePID := object.ClosureProtoID(st.arena, cl)
	if int(calleePID) >= len(st.protos) || st.protos[calleePID] == nil {
		return st.raiseGibbous(errf("ExecutePlainCallInlineFrame: invalid callee protoID %d", calleePID))
	}
	if st.nCcalls >= maxCCallDepth {
		return st.raiseGibbous(errf("C stack overflow"))
	}
	// nCcalls watermark (gibbousReentryCCallCap): each zero-cross level
	// is a real Go re-entry (enterGibbous → Run → dispatcher → here),
	// so deep Lua recursion would exhaust maxCCallDepth long before
	// maxLuaCallDepth. Past the watermark, fall through to the
	// interpreter path below — its executeFrom drives the remaining
	// recursion flat (doCall's gibbous branch is gated by the same
	// watermark), costing exactly one more nCcalls total. Sampled
	// BEFORE the increment so all four gates (here, doCall,
	// ExecuteCalleeFromInlineFrame, executeFrom's TAILCALL dispatch)
	// switch at the same depth (PR #86 review).
	underWatermark := st.nCcalls < gibbousReentryCCallCap
	st.nCcalls++
	// Zero-cross fast path: callee is also P4-promoted → skip
	// executeFrom's interpreter loop entirely.
	if profileEnabled && th == st.mainTh && underWatermark {
		calleeCode := st.bridge.GibbousCodeOf(st.protos[calleePID])
		if calleeCode != nil {
			err := st.enterGibbous(th, calleeCode, funcIdx, int(nargs), int(nresults))
			st.nCcalls--
			if err != nil {
				return st.raiseGibbous(err)
			}
			st.frameInlineZeroCrossHits++
			return 0
		}
	}
	// Interpreter fallback: enterLuaFrame + executeFrom, exactly like
	// host.CallBaseline's doCall path, then callee RETURN pops itself.
	if e := st.enterLuaFrame(th, funcIdx, int(nargs), int(nresults), false); e != nil {
		st.nCcalls--
		return st.raiseGibbous(e)
	}
	entryDepth := th.ciDepth - 1
	err := st.executeFrom(th, entryDepth)
	st.nCcalls--
	if err != nil {
		return st.raiseGibbous(err)
	}
	return 0
}

// NativeCalleeSegAddr returns the PJ10 native segment entry address for
// the callee Proto, or 0 if the callee isn't native-compiled (issue #50
// Spike 5). Looks up the callee's GibbousCode and type-asserts to
// bridge.NativeSegAddrer (only peroptranslator.nativeCode implements it).
func (st *State) NativeCalleeSegAddr(protoID uint32) uint64 {
	if int(protoID) >= len(st.protos) || st.protos[protoID] == nil {
		return 0
	}
	code := st.bridge.GibbousCodeOf(st.protos[protoID])
	if code == nil {
		return 0
	}
	seg, ok := code.(bridge.NativeSegAddrer)
	if !ok {
		return 0
	}
	return seg.NativeSegEntryAddr()
}

// CalleeNeverExitsSegment reports whether the callee Proto's native
// segment never exits to a Go helper mid-execution (issue #50 Spike 5).
func (st *State) CalleeNeverExitsSegment(protoID uint32) bool {
	if int(protoID) >= len(st.protos) || st.protos[protoID] == nil {
		return false
	}
	code := st.bridge.GibbousCodeOf(st.protos[protoID])
	if code == nil {
		return false
	}
	seg, ok := code.(bridge.NativeSegAddrer)
	if !ok {
		return false
	}
	return seg.NativeNeverExitsSegment()
}

// ObserveCallCallee snapshots the callee shape at R(A) for the issue
// #50 Spike 1 per-CALL-site inline cache. Returns a packed uint64 the
// PJ10 native dispatcher uses to populate the IC after
// host.CallBaseline succeeds.
//
// Packing (matches jit.P4HostState.ObserveCallCallee):
//
//	bits  0..31 : protoID (0 for host closure or non-function)
//	            : for a math-intrinsic host closure, bits 0..47 instead
//	              carry the closure GCRef (issue #77)
//	bits 32..39 : proto.NumParams (0 for host / non-function)
//	bits 40..47 : proto.MaxStack (0 for host / non-function)
//	bits 48..55 : flags — bit0=IsVararg, bit1=NeedsArg, bit2=IsHost
//	bits 56..63 : math intrinsic kind (jit.Intrinsic*, 0 = none); set
//	              only when bit2 (IsHost) is set and the host fn is a
//	              recognized intrinsic
//
// The observation is racy w.r.t. concurrent GC / proto rewrites but is
// benign: the mmap-segment guard the observation feeds re-validates
// protoID at each hit via an atomic load; a stale meta byte only turns
// into an over-cautious slow-path fall through, never into a memory
// error. Never raises.
func (st *State) ObserveCallCallee(base int32, a int32) uint64 {
	th := st.runningThread
	ci := st.gibCI(th)
	callee := reg(th, ci, int(a))
	if value.Tag(callee) != value.TagFunction {
		return 0 // non-function: leave IC untouched; CallBaseline will raise.
	}
	cl := value.GCRefOf(callee)
	if object.IsHostClosure(st.arena, cl) {
		// Host closure. If it is a recognized math intrinsic (issue #77),
		// pack the kind (bits 56..63) and the closure GCRef (bits 0..47)
		// so populateCallIC can cache an inline fast path + an identity
		// guard value; otherwise record "stuck host" (flag bit 2 set) as
		// before. The GCRef fits in 48 bits (arena byte offset); the full
		// callee value is reconstructed host-side as
		// MakeGC(TagFunction, gcref).
		if kind := st.IntrinsicKindOf(object.ClosureProtoID(st.arena, cl)); kind != 0 {
			return uint64(cl) |
				uint64(observeCallFlagIsHost)<<48 |
				uint64(kind)<<56
		}
		return uint64(observeCallFlagIsHost) << 48
	}
	pid := object.ClosureProtoID(st.arena, cl)
	if int(pid) >= len(st.protos) || st.protos[pid] == nil {
		return 0 // unknown proto id: treat as unobservable.
	}
	proto := st.protos[pid]
	var flags uint8
	if proto.IsVararg {
		flags |= observeCallFlagIsVararg
	}
	if proto.NeedsArg {
		flags |= observeCallFlagNeedsArg
	}
	return uint64(pid) |
		uint64(uint8(proto.NumParams))<<32 |
		uint64(uint8(proto.MaxStack))<<40 |
		uint64(flags)<<48
}

// Flag bits for ObserveCallCallee packing (mirror of
// peroptranslator.CallICFlag*).
const (
	observeCallFlagIsVararg uint8 = 1 << 0
	observeCallFlagNeedsArg uint8 = 1 << 1
	observeCallFlagIsHost   uint8 = 1 << 2
)

// tryIndirectCallee decides whether the callee is a "gibbous-with-slot Lua
// closure (main thread)" — if so, it pushes the frame itself + sets the gibbous
// bit + writes the callee frame base to the transfer word, returning
// (sentinel, true) to let the caller wasm reach it directly via call_indirect
// (avoiding code.Run's double tier-cross); otherwise it returns (0, false) to
// take the fallback.
//
// It is strictly isomorphic with doCall's callee resolution (nargs/nresults
// decode, funcIdx locate), but **only** intercepts the one case of "plain Lua
// closure + already gibbous-promoted + has slot"; host / __call / not-promoted /
// table-full gibbous are all not intercepted (handled=false) and dispatched
// uniformly by DoCall's fallback-path doCall, preserving correctness.
//
// **Frame push equivalent to enterGibbous**: enterLuaFrame (marks fresh=false) +
// SetGibbous(true) + reMirrorTop — field-for-field identical to the
// crescent->gibbous entry's enterGibbous (gibbous_host.go), differing only in
// "not doing code.Run here, running via the caller's call_indirect instead". The
// callee RETURN pops this frame + writes the refreshed caller base to the
// transfer word via DoReturn (symmetric to enterGibbous where the trampoline
// pops the frame).
func (st *State) tryIndirectCallee(th *thread, ci *callInfo, a, b, c int32) (int64, bool) {
	if !profileEnabled || th != st.mainTh {
		return 0, false // coroutine threads do not tier up (§5); a non-profile build has no gibbous
	}
	funcIdx := ci.base + int(a)
	callee := th.slot(funcIdx)
	if value.Tag(callee) != value.TagFunction {
		return 0, false // __call metamethod / non-callable -> fallback (handled by doCall)
	}
	cl := value.GCRefOf(callee)
	if object.IsHostClosure(st.arena, cl) {
		return 0, false // host fn -> fallback
	}
	pid := object.ClosureProtoID(st.arena, cl)
	code := st.bridge.GibbousCodeOf(st.protos[pid])
	if code == nil {
		return 0, false // not promoted (crescent) -> fallback
	}
	slot, ok := code.Slot()
	if !ok {
		return 0, false // table-full sentinel (no slot) -> fallback via code.Run (baseline)
	}
	// PW10 zero-cross lazy IC fill: cache slot into the closure's word1 high 16
	// bits, so subsequent Wasm-side emitCall reads it directly, avoiding this
	// path's Go map lookup (GibbousCodeOf + Slot). Idempotent (writes the same
	// value each time).
	object.SetClosureGibbousSlot(st.arena, cl, slot)
	// nargs/nresults decode (same as doCall: B=0 takes to top, C=0 leaves to top).
	var nargs int
	if b == 0 {
		nargs = th.top - funcIdx - 1
	} else {
		nargs = int(b) - 1
	}
	nresults := int(c) - 1
	// Push the frame (equivalent to enterGibbous: enterLuaFrame + set gibbous
	// bit + re-mirror).
	if e := st.enterLuaFrame(th, funcIdx, nargs, nresults, false); e != nil {
		st.raiseGibbous(e) // anchor the line number (currentCI is still the caller frame)
		return -1, true
	}
	cci := st.gibCI(th)
	cci.SetGibbous(true)
	th.reMirrorTop()
	// Write the callee frame base byte offset to the transfer word, for the
	// caller wasm to read as the call_indirect argument.
	calleeBaseByte := (th.stackBaseW + uint32(cci.base)) * 8
	st.arena.SetWordAt(st.ciTransferRef, uint64(calleeBaseByte))
	st.indirectCalls++              // count direct-call hits (R3 verification)
	return int64(slot)<<1 | 1, true // indirect sentinel (odd)
}

// Call handles CALL A B C inside a gibbous frame (04-trampoline §3 + PW10 R3
// direct call).
//
// **R3 fast path**: the callee is a gibbous-with-slot Lua closure ==>
// tryIndirectCallee pushes the frame itself + sets the gibbous bit + writes the
// callee frame base to the transfer word, returning an indirect sentinel — the
// caller wasm uses it to `call_indirect <slot>` across modules directly
// (avoiding code.Run's ~143ns double tier-cross re-entry).
//
// **Fallback**: host / crescent (not promoted) / __call metamethod / no-slot
// gibbous (table full) ==> reuses doCall's uniform dispatch to run to
// completion synchronously (baseline; the result is already in R(A..), next==nil
// or starts a layer of executeFrom to drive the un-promoted Lua frame).
//
// **base refresh (PW6 core, fallback path)**: the callee frame may growStack,
// relocating the value-stack segment in the arena, invalidating this frame's
// $base. On return the fallback path recomputes the new base byte offset from
// the current stackBaseW + ci.base (even); the R3 direct-call path's base
// refresh is done by the callee RETURN via DoReturn writing the transfer word.
//
// Returns (i64 tri-state, dispatched on the Wasm side; the negative check must
// come before the odd/even check):
//   - < 0 (-1): error, pendingErr is set, bubbles up the status chain;
//   - odd (slot<<1)|1: indirect direct call, callee frame base already written
//     to the transfer word;
//   - even (multiple of 8): done, value = the refreshed base byte offset for
//     this frame (fallback path ran to completion synchronously).
func (st *State) DoCall(base, pc, a, b, c int32) int64 {
	th := st.runningThread
	ci := st.gibCI(th)
	ci.pc = pc
	// R3 fast path: callee is gibbous-with-slot ==> push the frame + return the indirect sentinel (caller call_indirect).
	if ret, handled := st.tryIndirectCallee(th, ci, a, b, c); handled {
		return ret
	}
	// Fallback: host / crescent / __call / no-slot gibbous — run to completion synchronously (baseline).
	ins := bytecode.EncodeABC(bytecode.CALL, int(a), int(b), int(c))
	next, e := st.doCall(th, ci, ins)
	if e != nil {
		st.raiseGibbous(e)
		return -1
	}
	if next != nil {
		// Entering a new Lua frame (the callee is an un-promoted closure) —
		// drive it to completion synchronously. nCcalls accounting: executeFrom
		// is a new Go stack re-entry boundary, preventing alternating
		// gibbous<->crescent recursion from blowing the Go stack (same guard as
		// meta.go callLuaFromHost).
		if st.nCcalls >= maxCCallDepth {
			st.raiseGibbous(errf("C stack overflow"))
			return -1
		}
		st.nCcalls++
		entryDepth := th.ciDepth - 1
		e2 := st.executeFrom(th, entryDepth)
		st.nCcalls--
		if e2 != nil {
			st.raiseGibbous(e2)
			return -1
		}
	}
	// Refresh base (a nested frame may have growStack-relocated the segment; a
	// stale base points at a Free'd segment = UAF).
	ci = st.gibCI(th)
	return int64((th.stackBaseW + uint32(ci.base)) * 8)
}

// CallBaseline handles the P4 PJ5 simplified-form CALL A B C (per
// internal/gibbous/jit/host.go::P4HostState.CallBaseline).
//
// **Difference from DoCall**: DoCall takes the P3 R3 indirect fast path via
// tryIndirectCallee (returning the (slot<<1)|1 sentinel to let the caller wasm
// jump to the callee run via call_indirect); the P4 PJ5 simplified form has no
// wasm-level in-segment indirect channel, so CallBaseline goes straight to the
// baseline doCall dispatch (host/crescent/__call/all gibbous forms run to
// completion synchronously), avoiding a pushed-but-never-executed dangling
// callee frame.
//
// Returns: 0=OK / 1=ERR (pendingErr set, raiseGibbous). After this path
// completes the callee frame is settled + results landed in R(A..A+C-2), the
// caller frame is still live, awaiting the Run side's DoReturn to pop the frame.
//
// **base refresh**: this simplified form's mmap segment does not read
// valueStackBase (callBaseline goes straight to the host sync path), so a
// baseline-internal growStack relocation is invisible to the P4 segment — only
// the host side sees the stale-base risk, and doCall already correctly refreshes
// ci.base to resume.
func (st *State) CallBaseline(base, pc, a, b, c int32) int32 {
	th := st.runningThread
	ci := st.gibCI(th)
	ci.pc = pc
	ins := bytecode.EncodeABC(bytecode.CALL, int(a), int(b), int(c))
	next, e := st.doCall(th, ci, ins)
	if e != nil {
		st.raiseGibbous(e)
		return 1
	}
	if next != nil {
		// Entering a new Lua frame (the callee is an un-promoted closure or a
		// no-slot gibbous) — drive it to completion synchronously. nCcalls
		// accounting same as DoCall (same guard as meta.go callLuaFromHost).
		if st.nCcalls >= maxCCallDepth {
			st.raiseGibbous(errf("C stack overflow"))
			return 1
		}
		st.nCcalls++
		entryDepth := th.ciDepth - 1
		e2 := st.executeFrom(th, entryDepth)
		st.nCcalls--
		if e2 != nil {
			st.raiseGibbous(e2)
			return 1
		}
	}
	return 0
}

// TailCall handles TAILCALL A B C inside a gibbous frame (tail call reuses the
// frame, 04-trampoline §2.5).
//
// Reuses doTailCall:
//   - plain Lua closure / __call: doTailCall closes upvalues, shifts args down,
//     pops this frame (G), and pushes the callee frame (reusing G's funcIdx,
//     nresults inherits G's nresults). This function then drives the callee
//     chain to completion via executeFrom — **tail recursion iterates at O(1)
//     stack/CallInfo depth inside the interpreter** (when the callee itself
//     TAILCALLs again, doTailCall pops+pushes at the same depth, continuing in
//     the same execute loop), returning 0; the gibbous function then directly
//     returns 0 (this frame has been replaced, skipping the trailing RETURN).
//   - host fn: doTailCall internally callHosts (result lands at base+a), and
//     the G frame is not popped -> returns 2; the gibbous falls through to the
//     trailing RETURN, with DoReturn doing the final return (mirroring the
//     interpreter).
//
// Returns: 0=Lua tail call complete (gibbous return 0) / 1=ERR / 2=host (falls
// through to the trailing RETURN).
func (st *State) TailCall(base, pc, a, b, c int32) int32 {
	th := st.runningThread
	ci := st.gibCI(th)
	ci.pc = pc
	ins := bytecode.EncodeABC(bytecode.TAILCALL, int(a), int(b), int(c))
	next, e := st.doTailCall(th, ci, ins)
	if e != nil {
		st.raiseGibbous(e)
		return 1
	}
	if next == nil {
		// Host tail call: the result already landed at base+a, and the G frame
		// was not popped -> fall back to the trailing RETURN (DoReturn).
		return 2
	}
	// Lua tail call: G has been replaced by the callee frame. Drive the callee
	// chain to completion synchronously.
	if st.nCcalls >= maxCCallDepth {
		st.raiseGibbous(errf("C stack overflow"))
		return 1
	}
	st.nCcalls++
	entryDepth := th.ciDepth - 1
	e2 := st.executeFrom(th, entryDepth)
	st.nCcalls--
	if e2 != nil {
		st.raiseGibbous(e2)
		return 1
	}
	return 0
}

// ExecuteCalleeFromInlineFrame is the Spike 1 Step C-1 helper (per
// `docs/design/p4-method-jit/implementation-progress.md` §9.20.9 trampoline
// exit-resume protocol commit-2 interface + commit-5d real implementation +
// commit-5f real-integration strategy redirect: from the data written by the
// mmap segment's BuildVoid0Arg, look up the closure GCRef -> callee Proto ->
// call enterLuaFrame + executeFrom to fully redo the frame build).
//
// **Design clarification** (per commit-5e integration verification + §9.20.5
// P3 PW10 same-source reference implementation differences): the original P3
// PW10 §14.8 (4)-i design expected the mmap segment to fully emit the frame
// build (arena proto cache segment + 5-word real computation), with the helper
// only running executeFrom; the P4 Spike 1 real integration takes a more
// conservative "frame-build-data lookup + in-helper enterLuaFrame redo"
// strategy, giving up zero-cross but guaranteeing correctness + engineering
// feasibility.
//
// **Flow** (per §9.20.9 (1) protocol + commit-5f redirect + commit-5j
// self-check fix):
//  1. the mmap segment's BuildVoid0Arg has already ciDepth++'d + written
//     CallInfo[ciDepth-1]'s 5 words (of which word3 = closure GCRef payload, the
//     only trustworthy field in Spike 1)
//  2. look up the callee Proto: read word3 -> closure GCRef ->
//     object.ClosureProtoID -> st.protos[pid]
//  3. ciDepth-- to cancel out the BuildVoid0Arg side effect (enterLuaFrame will
//     ciDepth++ again)
//  4. funcIdx = calleeCI.funcIdx (written by the mmap segment's BuildVoid0Arg
//     word0; at the current commit-4b emit it is a 0 placeholder — Spike 1
//     commit-5k engineering needs the P2 Bridge analyzer to pass the callee
//     FuncExpr -> bytecode.Proto lookup + compile-time hardcode
//     word0=base|funcIdx<<32)
//  5. nargs = 0 (Spike 1 simplified 0-arg form; callee.NumParams=0 guards)
//  6. nresults = 0 (Spike 1 0-return setter form; per L976 `const nresults=0`
//     below)
//  7. enterLuaFrame + executeFrom + popCallInfo (automatically by callee RETURN)
//  8. **exit ciDepth++ balance**: lets the mmap segment's subsequent
//     PopVoid0Arg dec to the correct caller depth
//
// **Current Spike 1 real integration not yet complete** (commit-5j self-check):
// analyzeSelfCallSpecForm revokes the useFrameInline guard -> info.useFrameInline
// is not really set -> the Compile-side useFrameInline branch is dead code ->
// this helper is not reached during Run. Remaining commit-5k engineering: callee
// Proto metadata integration + word0/1/2/4 real computation + guard re-enable.
//
//   - 0=OK (callee complete + returns landed in R(callA..callA+nresults-1))
//   - 1=ERR (state.pendingErr set, the Run-side dispatcher returns 1 and the
//     error bubbles up)
func (st *State) ExecuteCalleeFromInlineFrame(base, callA, callArgCount, nresults int32) int32 {
	_ = base // the base arg is the R0 byte offset computed by jitContext.valueStackBase, unread by the Spike 1 helper
	th := st.runningThread
	// **commit-5m fixes the ciDepth Go-vs-mirror desync bug**
	if th.ciDepthWordRef != 0 {
		th.ciDepth = int(uint32(th.arena.WordAt(th.ciDepthWordRef)))
	}
	// 1. Look up the callee Proto: read CI[ciDepth-1].word3 -> closure GCRef
	depth := th.ciDepth - 1
	var calleeCI callInfo
	th.readCISegInto(depth, &calleeCI)
	cl := calleeCI.cl
	if cl == 0 {
		return st.raiseGibbous(errf("ExecuteCalleeFromInlineFrame: nil closure GCRef in CI[%d]", depth))
	}
	calleePID := object.ClosureProtoID(st.arena, cl)
	if int(calleePID) >= len(st.protos) || st.protos[calleePID] == nil {
		return st.raiseGibbous(errf("ExecuteCalleeFromInlineFrame: invalid callee protoID %d", calleePID))
	}
	// 2. ciDepth-- to cancel out the BuildVoid0Arg side effect
	th.setCIDepth(th.ciDepth - 1)
	// 3. funcIdx = th.cur.base + callA (under the SELF + CALL form the method is in R(callA))
	funcIdx := th.cur.base + int(callA)
	// 4. nargs = 1 + callArgCount (self + N user args, Spike 2); nresults is
	//    computed from the caller's CALL.C (Spike 4 multi-return multi-form):
	//    callC=1=0 returns / 2=1 return / 3..16=N=2..15 returns, dropping
	//    multi-ret.
	nargs := 1 + int(callArgCount)
	// 5. C stack depth check + nCcalls++
	if st.nCcalls >= maxCCallDepth {
		return st.raiseGibbous(errf("C stack overflow"))
	}
	// nCcalls watermark mirrors ExecutePlainCallInlineFrame (see the
	// gibbousReentryCCallCap doc in frame.go): past the watermark, take
	// the interpreter fallback so deep recursion cannot exhaust the C
	// stack budget. Sampled before the increment so all four gates
	// switch at the same depth (PR #86 review).
	underWatermark := st.nCcalls < gibbousReentryCCallCap
	st.nCcalls++
	// 6. **commit-5u real zero-cross optimization** (per §9.20.12 remaining
	//    zero-cross engineering): if the callee Proto is also P4-promoted
	//    (GibbousCodeOf non-nil and main thread), call enterGibbous directly
	//    (which internally does enterLuaFrame + code.Run + DoReturn frame pop),
	//    skipping the executeFrom interpreter main loop + entering the P4 mmap
	//    segment directly, achieving the zero-cross path. When the callee is not
	//    P4-promoted, fall back to enterLuaFrame + executeFrom (the existing
	//    Spike 1-4 path).
	if profileEnabled && th == st.mainTh && underWatermark {
		calleeCode := st.bridge.GibbousCodeOf(st.protos[calleePID])
		if calleeCode != nil {
			err := st.enterGibbous(th, calleeCode, funcIdx, nargs, int(nresults))
			st.nCcalls--
			if err != nil {
				return st.raiseGibbous(err)
			}
			st.frameInlineZeroCrossHits++ // zero-cross path hit (State-level, read by tests)
			// exit ciDepth++ balances PopVoid0Arg (per Spike 1 commit-5d/5m)
			th.setCIDepth(th.ciDepth + 1)
			return 0
		}
	}
	// 6.b enterLuaFrame + executeFrom (the existing Spike 1-4 non-zero-cross fallback path)
	if e := st.enterLuaFrame(th, funcIdx, nargs, int(nresults), false); e != nil {
		st.nCcalls--
		return st.raiseGibbous(e)
	}
	// 7. Drive the callee Lua body to RETURN synchronously (with an embedded popCallInfo to pop the callee frame)
	entryDepth := th.ciDepth - 1
	err := st.executeFrom(th, entryDepth)
	st.nCcalls--
	if err != nil {
		return st.raiseGibbous(err)
	}
	// 8. exit ciDepth++ balances the mmap segment's PopVoid0Arg: after
	//    executeFrom pops the callee frame ciDepth = caller_depth; the mmap
	//    segment's PopVoid0Arg (EmitFrameInlineCIDepthDec) will dec again -> the
	//    exit manual ciDepth++ cancels it out, letting PopVoid0Arg dec to the
	//    correct caller_depth. No writeCISeg(&th.cur) needed: PopVoid0Arg only
	//    dec's the mirror word + rets, without readCISegInto reload, so the
	//    transient ciDepth <-> th.cur inconsistency is restored by p4Code.Run's
	//    exit + syncCurFromSeg at the next Go control-flow point.
	th.setCIDepth(th.ciDepth + 1)
	return 0
}
