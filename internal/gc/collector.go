// Package gc implements the stop-the-world mark-sweep collector, shadow stack,
// safepoint discipline, string intern, and write-barrier interface for the arena
// GC objects defined in package object. See docs/design/p1-interpreter/06-memory-gc.md.
//
// Scope (P1):
//   - STW full GC, no write barrier (the write-barrier interface is a placeholder,
//     filled only by P3+ incremental GC — 06 §9.4).
//   - Two-white flipping retained (06 §4.3: isomorphic with future incremental GC).
//   - Explicit gray stack (06 §5.3) for iterative marking.
//   - shadow stack: host explicitly push/defer pop; Lua execution uses "stack as root"
//     with zero registration (06 §6).
//   - string intern (JSHash + weakly-reachable index, 06 §9.3): rawequal on strings
//     degenerates to GCRef comparison.
//   - weak-table cleartable (06 §8.4 / 07 §13) stub: semantics wired in after 07 lands.
package gc

import (
	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// Color encoding is shared with the object package (to avoid a circular
// dependency we reuse the object.ColorXXX constants here).

// Collector is the mark-sweep collector (single instance, hung off State).
type Collector struct {
	a *arena.Arena

	// The full sweep chain of all header objects (linkSweep is called on the Alloc path).
	sweepHead arena.GCRef

	// Two-white: flipped at the end of each GC round (06 §4.3).
	currentWhite uint8 // 0 or 1; the white color of newly allocated objects

	// mark work set (explicit gray stack; 06 §5.3).
	gray []arena.GCRef

	// shadow stack (host explicitly registers; 06 §6).
	shadow []value.Value

	// root set (R1..R9; 06 §5.1): injected by State.
	roots Roots

	// pacing (06 §8.3).
	threshold           uint64 // allocation-byte threshold that triggers the next GC
	gcPauseRatio        int    // GCPAUSE, default 200 (2.0x)
	bytesAllocSince     uint64 // cumulative allocation since last GC (including bytes already swept)
	liveBytesAfterSweep uint64 // live bytes at the end of this round's sweep (accumulated by sweep)
	stressMode          bool   // high-frequency stress mode: force Collect at every safepoint (12 §5)
	stopped             bool   // collectgarbage("stop"): automatic GC paused (explicit Collect unaffected)
	collecting          bool   // true during Collect (host-trigger AllocCharge re-entry guard, issue #9 direction 1)
	hostTrigger         bool   // host alloc crossing the threshold triggers collect (issue #9 direction 1, opt-in; existing stdlib/intern paths have mid-construction transient GCRefs not pinned, so default false to avoid breakage)

	// gcPending flag (P3 PW9): reflects "whether MaybeCollect will actually Collect",
	// mirrored into a fixed word of the arena (linear memory) so gibbous FORLOOP
	// back-edges can inline an i32.load check — only crossing the layer via h_safepoint
	// when GC is actually due (otherwise a hot loop crosses the layer unconditionally
	// every iteration, swallowing the gains of eliminating dispatch, 05 §3 / 08 §5.1.2).
	// **Conservatively correct**: a true flag covers "stressMode or
	// bytesAllocSince≥threshold"; when GC should fire the flag must be 1 (only a missed
	// set-to-1 is dangerous; an extra set-to-1 is merely one harmless extra layer cross).
	// When gcPendingRef=0 everything is a no-op (non-p3 build).
	gcPendingRef  arena.GCRef // GCRef (byte offset) of the flag word in the arena; 0 = not installed (gibbous does not read)
	gcPendingLast bool        // last value written to the flag word (transition-only write, avoids repeating the store every alloc)

	// weak-table registry (06 §8.4 / 07 §13).
	weakList []arena.GCRef

	// finalizer queue (06 §10).
	finalizeList    []arena.GCRef // userdata with a registered __gc (creation order)
	hasFinalizer    map[arena.GCRef]bool
	toRunFinalizers []arena.GCRef     // __gc to run this round (reversed while traversing in reverse creation order)
	runFinalizer    func(arena.GCRef) // __gc dispatch callback (injected by State, M11+)

	// host closure reclamation callback (injected by State): when sweep reclaims a
	// host closure, notify the registry to release the slot reference (hostFn slot
	// reuse, prevents unbounded growth of the long-lived State registry).
	releaseHostFn func(hostFnID uint32)

	// string intern table (06 §9.1).
	strBuckets [][]arena.GCRef
	strMask    uint32
	strCount   uint32
}

// Roots is the runtime root set, supplied by the State / VM at GC time (06 §5.1).
//
// Field names follow the R numbering (R5 = running thread, covered automatically via
// its valueStack/CallInfo, so stack slots need not be listed explicitly — registering
// RunningThread is enough, since the mark phase scans down to the stack following the
// Thread header).
type Roots struct {
	Globals           arena.GCRef                   // R1
	Registry          arena.GCRef                   // R2
	MainThread        arena.GCRef                   // R3
	RunningThread     arena.GCRef                   // R4 / R5 (following its valueStack/CallInfo scans all live registers)
	Threads           []arena.GCRef                 // R4: other live Threads (coroutine chain)
	ProgramStringRefs func(visit func(arena.GCRef)) // R6: string GCRefs of all programStringRefs in State (per 01 §5.7 / 06 §5.1 R6 revision)
	TypeMetatables    [9]arena.GCRef                // R9: per-type metatables (07 §1.2)

	// ExtraValues exposes live Values held on the Go side (used when M9/M10 thread
	// value stacks live in a Go slice; after M13 switches to an arena view, taken over
	// by RunningThread stack scanning). Any transient Value on the Go heap (values
	// stashed by callInfo during table get/set, CONCAT intermediates, etc.) also goes
	// through this root.
	ExtraValues func(visit func(value.Value))

	// ExtraRefs exposes live GCRefs held on the Go side (for objects held directly as a
	// GCRef, such as open upvalues on a thread).
	ExtraRefs func(visit func(arena.GCRef))

	// R7 shadow stack is held by the Collector itself; R8 temporary roots fall under
	// R5/R7 and need no separate field.
}

// Options configures the collector.
type Options struct {
	GCPause int // default 200; 0 = use default. 06 §8.3 LUAI_GCPAUSE.
}

// New constructs a Collector around the given arena.
func New(a *arena.Arena, opts Options) *Collector {
	pause := opts.GCPause
	if pause == 0 {
		pause = 200
	}
	c := &Collector{
		a:            a,
		gcPauseRatio: pause,
		threshold:    uint64(a.Cap()) / 4, // first time: trigger at 1/4 capacity (avoids both very-early GC and too-late GC)
		hasFinalizer: make(map[arena.GCRef]bool),
		strBuckets:   make([][]arena.GCRef, 16), // start with 16 buckets, rehash when load factor exceeds 1
		strMask:      15,
	}
	return c
}

// SetRoots installs / refreshes the root set provider (called by State during init
// and whenever RunningThread changes).
func (c *Collector) SetRoots(r Roots) { c.roots = r }

// SetFinalizerRunner injects the __gc dispatch callback (06 §10; State injects it at init).
func (c *Collector) SetFinalizerRunner(fn func(arena.GCRef)) { c.runFinalizer = fn }

// SetHostFnReleaser injects the host closure slot release callback (State injects it at init).
func (c *Collector) SetHostFnReleaser(fn func(uint32)) { c.releaseHostFn = fn }

// RegisterFinalizer registers a userdata with a __gc (called when setmetatable includes __gc).
func (c *Collector) RegisterFinalizer(ud arena.GCRef) {
	if c.hasFinalizer[ud] {
		return
	}
	c.hasFinalizer[ud] = true
	c.finalizeList = append(c.finalizeList, ud)
}

// LinkSweep links a newly allocated object at the head of the full sweep chain (06 §2.1).
//
// Must be called immediately "after writing the GCHeader" — this is the collector's only
// channel for seeing new objects.
// Allocator path: after object.allocateRaw(M3), the caller (State's Alloc helper) calls LinkSweep.
//
// Color semantics: **new objects are set to deadWhite** (next-round reclaim candidate color,
// matching Lua 5.1 `luaC_link`).
// The first mark round paints reachable objects black; sweep reclaims those still deadWhite
// (= unreachable).
// This avoids the degeneration "LinkSweep sets currentWhite ⇒ first round deadWhite mismatch ⇒
// nothing reclaimed even after one round".
func (c *Collector) LinkSweep(ref arena.GCRef) {
	h := object.HeaderOf(c.a, ref)
	h = object.SetColor(h, c.deadWhite())
	h = object.SetGCNext(h, c.sweepHead)
	object.SetHeader(c.a, ref, h)
	c.sweepHead = ref
}

// AllocCharge notifies the collector of the byte count of one allocation (used for pacing).
//
// State's Alloc helper calls this after every arena.AllocBytes to accumulate.
//
// **issue #9 direction 1**: trigger collect directly when crossing the threshold. This
// avoids the starvation of "accounting rises but sweep never fires" under
// boundary-dominated workloads (host repeatedly NewTable + short scripts, VM opcode
// safepoints not frequently crossed).
//
// **half-constructed object safety**: before calling AllocCharge, the host public API:
// ① already-pinned objects (the Table returned by NewTable is registered immediately via
// PinRef) are reachable from a GC root; ② intermediate transient GCRefs (e.g. newArr/newNode
// inside rehash), though not on the sweep chain, are not reclaimed since sweep only walks the
// chain. So a "mid-path triggered collect" on the host path is safe for all valid paths.
//
// **non-reentrancy guard**: any AllocCharge triggered inside collect (e.g. a finalizer calling
// host code that in turn calls a host public API) is intercepted by the collecting guard,
// avoiding recursive collect.
func (c *Collector) AllocCharge(nbytes uint32) {
	c.bytesAllocSince += uint64(nbytes)
	c.updateGCPending()
	if c.hostTrigger && !c.stopped && !c.collecting && c.bytesAllocSince >= c.threshold {
		c.Collect()
	}
}

// SetHostTriggeredCollect toggles whether host alloc crossing the threshold triggers
// collect directly (issue #9 direction 1).
//
// **opt-in contract**: once enabled, any AllocCharge call may Collect immediately when
// bytesAllocSince crosses the threshold. The caller (host public API / stdlib, etc.) must
// guarantee: **all transient GCRefs are reachable from a GC root** (in the pin table /
// pushed on the shadow stack / already on the sweep chain and mark-able). Otherwise a
// mid-construction GCRef gets wrongly reclaimed = UAF.
//
// **wangshu public API safety** (targeting enablement in wangshu.NewState):
//   - NewTable/NewArrayTable return values registered as a GC root immediately via PinRef ✓
//   - transient newArr/newNode inside rehash are not LinkSweep'd → sweep does not reclaim ✓
//   - on the SetGlobal path, globals is an R5 root ✓
//
// **unsafe** (default false, so current stdlib/intern passes):
//   - mid-intern (b []byte still on the Go stack, new strRef not yet in the table)
//   - metamethod callback holding a transient Lua-level Value
//   - transient before public-facing fromInnerWithPin
//
// So SetHostTriggeredCollect(true) is only safe to enable when "the caller guarantees
// full-path pinning". Recommended: the host embedding layer manually calls st.Collect() /
// st.MaybeCollectNow() each cycle as a cadence control (issue #9 direction 2, provided by #60).
func (c *Collector) SetHostTriggeredCollect(on bool) { c.hostTrigger = on }

// SetGCPendingRef installs the arena GCRef of the gcPending flag word (P3 PW9; in the
// wangshu_p3 build, State allocates an arena word at init and passes it in). Once installed,
// the collector mirrors the flag into that word at state-transition points (threshold
// crossing / Collect / stressMode toggle), and gibbous reads it inline.
func (c *Collector) SetGCPendingRef(ref arena.GCRef) {
	c.gcPendingRef = ref
	c.gcPendingLast = false
	c.updateGCPending()
}

// gcPendingNow reports whether "MaybeCollect will actually Collect" right now (always false when stopped).
func (c *Collector) gcPendingNow() bool {
	if c.stopped {
		return false
	}
	return c.stressMode || c.bytesAllocSince >= c.threshold
}

// updateGCPending mirrors gcPendingNow() into the arena flag word — writing only when the
// value changes (transition-only, avoids a store on every AllocCharge). No-op when
// gcPendingRef=0 (non-p3 build).
func (c *Collector) updateGCPending() {
	if c.gcPendingRef == 0 {
		return
	}
	now := c.gcPendingNow()
	if now == c.gcPendingLast {
		return
	}
	c.gcPendingLast = now
	v := uint64(0)
	if now {
		v = 1
	}
	c.a.SetWordAt(c.gcPendingRef, v)
}

// MaybeCollect checks the threshold at an allocation point and, if necessary, starts one
// STW full GC (06 §7.1).
//
// Caller contract: before calling, all live Values must be reachable from a root (on the
// Lua stack, or already pushed onto the shadow stack); otherwise they get wrongly reclaimed.
// This is the implementation side of the 06 §6.3 host-function discipline.
func (c *Collector) MaybeCollect() {
	if c.stopped {
		return
	}
	if c.stressMode || c.bytesAllocSince >= c.threshold {
		c.Collect()
	}
}

// SetStopped pauses/resumes automatic GC (collectgarbage("stop"/"restart"); official
// semantics: after stop only explicit collectgarbage triggers reclamation, allocation no
// longer auto-GCs).
//
// updateGCPending: same as SetStressMode / Collect — stopped is an input to gcPendingNow
// (always returns false when stopped), so the flag word must be synced after a state
// transition; otherwise after restart (bytesAllocSince≥threshold accumulated during the
// stop) the flag lingers at 0 → gibbous back-edges miss the layer cross (though self-heals
// on the next AllocCharge, symmetric handling eliminates the lag window).
func (c *Collector) SetStopped(on bool) { c.stopped = on; c.updateGCPending() }

// SetStressMode toggles high-frequency GC stress mode (06 §11 / 12 §5): force a full
// Collect at every safepoint. Used by GC-transparency fuzzing — under stress mode the
// output must be byte-equal to normal mode, otherwise it means a missed root / early reclaim.
func (c *Collector) SetStressMode(on bool) { c.stressMode = on; c.updateGCPending() }

// Collect performs one STW full GC (06 §8.2 main flow).
func (c *Collector) Collect() {
	if c.collecting {
		return // guard against recursion (host-trigger AllocCharge during finalizer/sweep alloc inside Collect)
	}
	c.collecting = true
	defer func() { c.collecting = false }()
	c.markRoots()
	c.markAll()
	c.separateFinalizers()
	c.clearWeakTables()
	c.sweep()
	// Run finalizers (06 §10): separateFinalizers has already resurrected this round's
	// dead-white userdata and moved them into toRunFinalizers; here we dispatch each via
	// the callback (injected by State, calls the __gc metamethod). Executed in reverse
	// creation order (5.1 semantics).
	if len(c.toRunFinalizers) > 0 && c.runFinalizer != nil {
		for i := len(c.toRunFinalizers) - 1; i >= 0; i-- {
			c.runFinalizer(c.toRunFinalizers[i])
		}
	}
	c.toRunFinalizers = c.toRunFinalizers[:0]
	// issue #11 direction 1: after sweep, try to shrink the backing slab back to the Go heap.
	// Compact internally only acts when it decides cap can shrink (cap > bump + slack); under
	// P3 InPlaceBacking mode and a tight-cap steady state it is a no-op O(1). Typical benefit:
	// after a transient peak triggers grow doubling, Release frees the bulk of the bump-area →
	// Compact shrinks cap to the bump scale, and the Go runtime reclaims the old large slab
	// (latched high-water released, alleviating the pineapple#105-class long-lived pool fat
	// state phenomenon).
	c.a.Compact()
	// pacing: this round's live bytes were accumulated during sweep in c.liveBytesAfterSweep.
	c.threshold = c.liveBytesAfterSweep * uint64(c.gcPauseRatio) / 100
	if c.threshold < uint64(c.a.Cap())/16 {
		c.threshold = uint64(c.a.Cap()) / 16 // lower bound, prevents threshold degenerating to 0 on a tiny heap
	}
	c.bytesAllocSince = 0
	// Flip currentWhite.
	c.currentWhite ^= 1
	// After Collect, bytesAllocSince resets to 0 → gcPending drops to 0 (unless stressMode keeps it at 1).
	c.updateGCPending()
}

// liveBytesAfterSweep field is defined on the struct (assigned by sweep() at the end of each round, read by Collect()).
