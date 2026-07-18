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
	//
	// genChanged: budget OWNERSHIP changed since the last refresh
	// (st.budgetGen is bumped ONLY by SetStepBudget). Any partial fuel
	// drain accrued under the previous budget configuration is
	// discarded UNBILLED — an aggregate armed-boolean cannot see "ctx
	// already armed, budget added later" (both are armed=true), which
	// billed up to a full quantum of ctx-phase back-edges/dispatches to
	// the brand-new budget (code-review increment-3). Ctx-only changes
	// deliberately do NOT reach this flag: while a budget stays armed
	// the drain still belongs to that same live budget, and resetting
	// per ctx toggle handed out a fresh unbilled quantum each time
	// (code-review increment-4); while no budget is armed, the armed
	// branches below already resize the window from the live state.
	genChanged := ctx.SyncBudgetGen(st.budgetGen.Load())
	armed := st.stepBudget > 0 || st.ctx.Load() != nil
	if armed {
		if !genChanged {
			st.stepUsed += int64(ctx.SegCallFuelSpent())
		}
		ctx.SetSegCallFuel(jit.SegCallFuelBudgeted)
	} else {
		ctx.SetSegCallFuel(jit.SegCallFuelUnlimited)
	}
	// Loop back-edge fuel (issue #102): refill ONLY on a budget
	// ownership change or an armed-state transition. This refresh runs
	// after every dispatcher resume, and a loop whose body round-trips
	// through an exit-reason helper each iteration (cold CALL,
	// GETTABLE...) would have its back-edge drain erased every
	// iteration by an unconditional refill — the guard would never fire
	// and nothing else checks the budget on that path (host-closure
	// CALLs never reach st.preempt()). Between changes,
	// host.LoopPreempt is the only refiller — with one exception below.
	if armed {
		if genChanged {
			// Budget ownership change: drop the previous window's
			// drain unbilled (it accrued under a configuration the new
			// budget does not own) and start a fresh budgeted window.
			ctx.SetLoopFuel(jit.SegCallFuelBudgeted)
		} else if !ctx.LoopFuelArmedBudgeted() {
			// Arming transition without a budget change (ctx installed
			// while no budget armed): the pre-arming drain reports 0
			// spent (Unlimited-mode rule), so nothing is billed here.
			ctx.SetLoopFuel(jit.SegCallFuelBudgeted)
		} else if ctx.LoopFuel() == 0 {
			// Deopt stranding repair (PR #105 review): a seg2seg
			// callee's loop that drains the fuel at segCallDepth>0
			// deopts (set flag + ret) instead of exiting via
			// HelperLoopFuel, so LoopPreempt never runs and the
			// counter parks at 0 — where the segment's sub+jnz would
			// wrap to 2^32 before the next billing point. A LEGIT
			// drain always refills through LoopPreempt before the
			// segment resumes, so observing 0 here means a strand.
			// Refill WITHOUT billing: the deopt-redo re-runs those
			// same iterations on the baseline, which bills them via
			// st.preempt — billing the strand would double-charge.
			ctx.SetLoopFuel(jit.SegCallFuelBudgeted)
		}
	} else if genChanged || ctx.LoopFuelArmedBudgeted() {
		// Disarming transition (or a stale budgeted quantum from a
		// pre-generation refill): drop the budgeted quantum, go
		// unlimited (the partial drain has no budget to bill).
		ctx.SetLoopFuel(jit.SegCallFuelUnlimited)
	}
}

// LoopPreempt implements the HelperLoopFuel dispatcher target (issue
// #102): an in-segment loop back-edge (FORLOOP / negative-sBx JMP)
// drained loopFuel to zero. Bill the spent back-edges to the step
// budget, refill, and run the same preemption check st.preempt()
// performs on interpreter back-edges — raising "instruction budget
// exceeded" / "context canceled" as a recoverable error when tripped.
//
// The check must happen HERE, not deferred to st.preempt(): a loop
// whose body never enters a Lua frame (inline arithmetic, math
// intrinsics, host-closure CALLs) has no other preemption point, so
// billing without checking would let stepUsed sail past the budget
// unenforced (probe-verified: 7.7M helper round trips, zero raises).
func (st *State) LoopPreempt(ctx *jit.JITContext, base, pc int32) int32 {
	_ = base
	if st.stepBudget > 0 || st.ctx.Load() != nil {
		st.stepUsed += int64(ctx.LoopFuelSpent())
		ctx.SetLoopFuel(jit.SegCallFuelBudgeted)
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
	ctx.SetLoopFuel(jit.SegCallFuelUnlimited)
	return 0
}
