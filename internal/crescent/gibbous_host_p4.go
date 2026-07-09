//go:build wangshu_p4

package crescent

import (
	"unsafe"

	jit "github.com/Liam0205/wangshu/internal/gibbous/jit"
)

// RefreshJitCtxAddrs implements the batched P4HostState.RefreshJitCtxAddrs
// entry: compute the arena base pointer once, then derive all five
// arena-relative addresses (arenaBase / valueStackBase / ciDepthAddr /
// ciSegBaseAddr / topAddr) by simple offset arithmetic. Called on every
// JIT Run entry and replaces five separate getter calls that each
// recomputed arena.Words() + unsafe.Pointer.
//
// Arena grow protocol still holds: caller must call this on every Run
// entry; the values become stale once execution leaves the JIT world.
// Grow only happens on the slow path, so the JIT segment reloads via
// the jitContext on return.
func (st *State) RefreshJitCtxAddrs(ctx *jit.JITContext, base int32) {
	words := st.arena.Words()
	if len(words) == 0 {
		ctx.SetAllAddrs(0, 0, 0, 0, 0)
		return
	}
	arenaBase := uintptr(unsafe.Pointer(&words[0]))
	// vsBase: recompute from the LIVE thread state instead of trusting
	// the caller's base parameter (issue #80). base is an absolute
	// arena byte offset captured at Run entry; a host call that grows
	// the value stack (enterLuaFrame -> ensureStack -> growStack)
	// RELOCATES the stack segment to a new arena offset and frees the
	// old one — re-entering the segment with the stale offset reads
	// and writes the freed segment (silent corruption: wrong results,
	// "attempt to call a number value"). At the moment of any refresh,
	// runningThread.cur is the frame this Run invocation executes
	// (helpers never leave a pushed frame behind; DoReturn exits the
	// loop instead), so (stackBaseW + cur.base)*8 is the live offset —
	// equal to base at Run entry, and correct after relocation.
	vsByte := uintptr(base)
	if th := st.runningThread; th != nil {
		vsByte = (uintptr(th.stackBaseW) + uintptr(th.cur.base)) * 8
	}
	ctx.SetAllAddrs(
		arenaBase,
		arenaBase+vsByte,
		arenaBase+uintptr(st.ciDepthRef),
		arenaBase+uintptr(st.ciSegBaseRef),
		arenaBase+uintptr(st.topRef),
	)
	// Inline GETUPVAL ABI (issue #50 Spike 5): the running frame's
	// closure GCRef, the running thread's value-stack slot-0 host
	// address (for open-upvalue owner.slot reads), and whether it is safe
	// to inline open-upvalue reads (single-thread: no coroutine can own
	// an upvalue that the running thread does not). Recomputed here from
	// the live arenaBase so the segment sees grow-safe values.
	th := st.runningThread
	if th != nil {
		ctx.SetUpvalInlineFields(
			uintptr(th.cur.cl),
			arenaBase+uintptr(th.stackBaseW)*8,
			len(st.cos.cos) == 0 && len(st.threadChain) == 0,
		)
		// Value-stack end (issue #80): the seg2seg CALL fast body
		// bounds-checks the callee frame against this before an
		// in-segment dispatch (the interpreter path grows the stack in
		// enterLuaFrame; the in-segment path cannot).
		ctx.SetValueStackEnd(arenaBase +
			uintptr(th.stackBaseW)*8 + uintptr(th.stackCap)*8)
	} else {
		ctx.SetUpvalInlineFields(0, 0, false)
		ctx.SetValueStackEnd(0)
	}
	// Seg2seg dispatch fuel: with a step budget or cancel context armed,
	// cap how long execution can stay in-segment between preemption
	// points; otherwise refill to effectively-unlimited (see the
	// segCallFuel field doc — the budget has no async producer, so an
	// unmetered in-segment call tree would never observe it). Bill the
	// dispatches spent since the last refill to the step budget first,
	// so in-segment CALLs count like interpreter calls (each in-segment
	// dispatch corresponds to one enterLuaFrame the interpreter would
	// have billed via st.preempt()).
	if st.stepBudget > 0 || st.ctx.Load() != nil {
		st.stepUsed += int64(ctx.SegCallFuelSpent())
		ctx.SetSegCallFuel(jit.SegCallFuelBudgeted)
	} else {
		ctx.SetSegCallFuel(jit.SegCallFuelUnlimited)
	}
}

// LoopPreempt implements the HelperLoopFuel dispatcher target (issue
// #102): an in-segment loop back-edge (FORLOOP / negative-sBx JMP)
// drained segCallFuel to zero. Bill the spent fuel to the step budget,
// refill, and run the same preemption check st.preempt() performs on
// interpreter back-edges — raising "instruction budget exceeded" /
// "context canceled" as a recoverable error when tripped.
//
// Billing must happen here, BEFORE the check: the Run loop's
// post-dispatch RefreshJitCtxAddrs also bills spent fuel, but by then
// the verdict would already be decided on a stale stepUsed. The refill
// via SetSegCallFuel resets segCallFuelRefill so the subsequent
// RefreshJitCtxAddrs sees zero spent and never double-bills.
func (st *State) LoopPreempt(ctx *jit.JITContext, base, pc int32) int32 {
	_ = base
	if st.stepBudget > 0 || st.ctx.Load() != nil {
		st.stepUsed += int64(ctx.SegCallFuelSpent())
		ctx.SetSegCallFuel(jit.SegCallFuelBudgeted)
		if st.stepBudget > 0 && st.stepUsed > st.stepBudget {
			th := st.runningThread
			if th != nil && th.ciDepth > 0 {
				st.gibCI(th).pc = pc + 1 // anchor the error line
			}
			return st.raiseGibbous(errf("instruction budget exceeded"))
		}
		if h := st.ctx.Load(); h != nil {
			if err := h.err(); err != nil {
				th := st.runningThread
				if th != nil && th.ciDepth > 0 {
					st.gibCI(th).pc = pc + 1
				}
				return st.raiseGibbous(errf("context canceled: %s", err.Error()))
			}
		}
		return 0
	}
	ctx.SetSegCallFuel(jit.SegCallFuelUnlimited)
	return 0
}
