// Package crescent is the tier-0 interpreter (P1 main loop) — the single
// execution layer of P1 and the deopt landing point for all future tiers
// (roadmap §5 principle 1).
//
// Design: docs/design/p1-interpreter/05-interpreter-loop.md. Within the M9
// scope it only runs the three tiers arithmetic / loop / call; IC, metatables,
// coroutines and GC are left to M10/M11.
package crescent

import (
	"fmt"
	"sync/atomic"
	"unsafe"

	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/bridge"
	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/gc"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// ctxHolder wraps context.Context and is swapped safely across goroutines via
// atomic.Pointer. It strips the interface signature to avoid making
// internal/crescent depend directly on the standard-library context.
type ctxHolder struct {
	err func() error
}

// LuaError carries a Lua-level error value (05 §9.2).
type LuaError struct {
	Value value.Value
	// HasValue: whether the Value field carries a real error value. We cannot
	// use Value's zero value to decide "unset" — under NaN-boxing, bits 0
	// happen to be the legal number +0.0, so the error value of error(0, 0)
	// would be misjudged and replaced by the Msg string.
	HasValue  bool
	Msg       string // cached for the Go error interface
	Traceback string // built when the error bubbles to the top level (09; errors caught by pcall carry none)
	Level     int    // the level of error(msg, level) (09); 0 = no position prefix
	annotated bool   // chunkname:line: prefix already added (added only once)
	// PUC luaL_argerror mirror (issue #133): the function name in
	// "bad argument #N to 'name'" comes from the CALLER's call site
	// (getobjname on the CALL/TAILCALL/TFORLOOP operand), not from the
	// callee's own identity. Host functions raise the structured pair
	// below via NewArgError (Msg pre-filled with the C-caller fallback
	// "'?'"); the interpreter's host-call sites rewrite Msg through
	// resolveArgError before annotation. argNarg == 0 means "not an
	// arg error / already resolved".
	argNarg  int
	argExtra string
}

func (e *LuaError) Error() string {
	if e.Traceback != "" {
		return e.Msg + "\n" + e.Traceback
	}
	return e.Msg
}

// State is the embedding-facing VM state.
//
// M9-scope simplification: the value stack uses a Go slice, later switched in
// M13 to a view over the arena (arena backing injection point; 05 §1.3 / 06
// §1.1 leaves the hook).
type State struct {
	arena         *arena.Arena
	gc            *gc.Collector
	protos        []*bytecode.Proto // ProtoID → Proto (injected by Compile, see LoadProgram)
	strRefs       [][]arena.GCRef   // literals in protos[id] → interned GCRef (R6 root, see 11 §1.4)
	globals       arena.GCRef       // _G (globals table)
	runningThread *thread           // the thread currently executing (source of GC ExtraValues)
	hostFns       hostFnRegistry    // host function registry (M12)
	stringLib     arena.GCRef       // string library table (per-type __index for string values, 07 §1.2)
	stringMeta    arena.GCRef       // shared metatable for string values {__index = string} (PUC parity; getmetatable("") returns it)
	cos           coRegistry        // coroutine registry (08; coID = lightuserdata handle)

	// uvOwner records which thread's stack each [open] upvalue belongs to (the
	// Go-side form of the (threadRef, stackIdx) pair in 01 §5.4; once the value
	// stack is arena-ized it stores threadRef instead).
	// Removed from this table on close (the self-held value no longer depends on the thread).
	uvOwner map[arena.GCRef]*thread

	// compileFn is the compile callback for loadstring/load (injected by the
	// wangshu facade to avoid a reverse crescent → frontend dependency).
	// Returns (mainID, protos, err).
	compileFn func(src []byte, chunkname string) (uint32, []*bytecode.Proto, error)

	// nCcalls is the host→Lua re-entry depth (real Go stack consumption;
	// equivalent to 05 §7.4 LUAI_MAXCCALLS). callLuaFromHost does +1 on entry
	// / -1 on return; exceeding maxCCallDepth raises "C stack overflow".
	nCcalls int

	// threadChain is the suspended caller threads on the resume chain (06 §5.1
	// R4/R5: runningThread only covers the current thread, but the stacks of the
	// other threads on the chain must also be roots — after the freelist reuses
	// their memory, a missed root is a use-after-free). Pushed on Resume entry, popped on return.
	threadChain []*thread

	// loadedCls is the main-chunk closure returned by LoadProgram (R8-class
	// resident root: held only by the Go-side loaded cache; without a root it is
	// collected, and after the freelist reuses the block it cross-executes
	// another Program). Never cleared during the State's lifetime (11 §1.3 loaded is not evicted).
	loadedCls []arena.GCRef

	// When stepBudget > 0 the instruction budget is enabled: back edges
	// (JMP/FORLOOP negative displacement) check once every stepQuantum
	// instructions crossed, and raise "instruction budget exceeded" on overrun.
	// The seed for a host-side script quota feature; fuzzing uses it instead of
	// fragile source-substring filtering.
	stepBudget int64
	stepUsed   int64

	// loopFuelRefill records the last refill amount written to the loopBudget
	// word, so Safepoint can bill this batch of back edges precisely with
	// "refill - current value" (P3 loop step-budget fix; Go-side private, the
	// gibbous segment does not read it).
	loopFuelRefill int64

	// budgetGen counts budget OWNERSHIP changes: bumped ONLY by
	// SetStepBudget (which also resets stepUsed). Fuel-window owners
	// (P3's enterGibbous via loopFuelGen, P4's RefreshJitCtxAddrs via
	// JITContext.SyncBudgetGen) compare against their cached generation
	// and, on mismatch, discard the partial fuel drain WITHOUT billing —
	// back edges that ran under a previous budget configuration must
	// never bill a later-armed budget. An aggregate armed-boolean cannot
	// see e.g. "ctx already armed, budget added later" (both states are
	// armed=true), which billed up to a full quantum of ctx-phase back
	// edges to the brand-new budget (code-review increment-3).
	//
	// Deliberately NOT bumped by SetCancelHook: a ctx set/replace/remove
	// while a budget stays armed does not change who owns the drain —
	// treating it as a change handed out a fresh unbilled quantum per
	// toggle, letting repeated SetContext/RemoveContext extend the quota
	// indefinitely (code-review increment-4). Ctx changes while NO
	// budget is armed only affect the fuel WINDOW SIZE, which the
	// consumers derive from the live armed state, not from this counter.
	// Atomic for the same cross-goroutine reason as ctx itself.
	budgetGen atomic.Uint32

	// loopFuelGen is enterGibbous's cached budgetGen (VM-goroutine only).
	loopFuelGen uint32

	// safepointCalls counts how many times a gibbous back edge crosses tiers into
	// host.Safepoint (white-box probe, proving that with no budget the unlimited
	// loopFuel refill keeps a hot loop from crossing tiers almost entirely). Read only by tests.
	safepointCalls int64

	// ctx is the cancellation signal injected by SetContext (issue #4): the same
	// preempt points (back edge / frame entry / CALL) do a ctx.Err() check. The
	// atomic wrapper is because of the common cross-goroutine cancellation
	// pattern: the VM runs in goroutine A while the timer/ctx calls cancel() in
	// goroutine B — the context implementation is itself race-safe, but our reads
	// and writes of the ctx field cross goroutines, so an atomic wrapper is needed to avoid a data race.
	ctx atomic.Pointer[ctxHolder]

	// gcSeen/gcSeenRefs cache the root-scan dedup maps (used by the multi-thread
	// slow path; each Collect round reuses them with clear, avoiding root scanning
	// itself creating Go-heap garbage).
	gcSeen     map[*thread]bool
	gcSeenRefs map[*thread]bool

	// argsPool is the callHost argument buffer pool (LIFO; host→Lua→host nesting
	// deepens naturally). Idle slices in the pool hold no live Value (return means
	// logically invalid), so they are not GC roots.
	argsPool [][]value.Value

	// mainTh is the main-thread cache (State.Call reuses it across Runs, avoiding a newThread each time).
	mainTh *thread

	// pinnedRefs is the table of GCRef handles held by public-facing Values
	// (R8-class resident root: after the facade GetGlobal hands out a
	// function/table the user holds it long-term; without a root, overwriting the
	// old value in globals collects it → freelist reuse → calling the Value is a UAF).
	// Slot recycling: UnpinRef sets the slot to Null and pushes onto freePins;
	// PinRef reuses a free slot first (the public-facing Value.Release explicitly
	// frees via this path; not calling Release only accumulates slots + GCRefs,
	// a minor harm, see the wangshu.go godoc note).
	pinnedRefs []arena.GCRef
	freePins   []uint32

	// baseline is a globals snapshot (issue #6): MarkGlobalsBaseline takes it,
	// ResetGlobalsToBaseline restores from it. The compound values in baseline
	// (table/function GCRef) enter the GC roots via visitExtraValues so that
	// overwriting globals does not collect them (the dual of the pin table: the
	// pin table manages "long-held GCRefs exposed by the public API", baseline
	// manages "long-held GCRefs needed to restore internal state").
	baseline []baselineEntry

	// allowFileLoad: whether loadfile/dofile may read the host filesystem.
	// Default false (an embedded VM taking untrusted scripts, where file reads
	// are an over-privilege probe surface; the minimal landing of the 10 §12.1
	// LibsSafe idea). The host enables it explicitly via Options.AllowFileLoad.
	allowFileLoad bool

	// bridge: the P2 tiering bridge (`docs/design/p2-bridge/`). State-private,
	// hung here so the back-edge / entry sampling hook points (crescent main loop
	// + enterLuaFrame) can call into `bridge.OnBackEdge` / `OnEnter` (when
	// profileEnabled=true; otherwise the whole segment is compiled away, zero
	// overhead). The bridge package does not depend on crescent (infrastructure,
	// not the execution layer), so the reverse hook points are injected as an interface through this field.
	//
	// Lifetime: born with State (constructed at New; destroying State releases
	// Bridge together, including its profileTable / gibbousCodes references).
	// Not shared under multi-goroutine concurrency: the 01 §6.3 (B) scheme —
	// profileTable is hung State-private; -race passes naturally (00 §4 PB7 acceptance (d)).
	bridge *bridge.Bridge

	// arenaCleanup: releases the external resources held by the arena backing when
	// State is destroyed (under the wangshu_p3 build = close the adopted wazero
	// memory holder + runtime; under the default build = nil). Returned by
	// newStateArena, called by Close.
	arenaCleanup func()

	// gibStack is the crescent→gibbous cross-tier reused stack buffer (the
	// zero-alloc path of CallWithStack, 04-trampoline §2.2). Built lazily, reused single-goroutine.
	gibStack []uint64

	// gibbousPendingErr is the error staging bubbled by gibbous helpers
	// (DoReturn/h_raise) (04 §4 status chain): the helper stashes pendingErr, and the trampoline takes it on ERR.
	gibbousPendingErr *LuaError

	// p3env holds the wazero Runtime + memadapter holder under the wangshu_p3
	// build (built by newStateArena, taken by wireP3 to construct the gibbous
	// Compiler sharing the same runtime/memory). Always nil in the default build.
	// Type-erased to any to avoid depending on wazero across all builds.
	p3env any

	// gcPendingRef is the arena GCRef of the gcPending flag word (P3 PW9): at GC
	// state-transition points the collector mirrors "is it due" into this word
	// (linear memory), and the gibbous FORLOOP back edge inline-i32.loads it,
	// crossing tiers to h_safepoint only when due. 0 = not allocated.
	gcPendingRef arena.GCRef

	// loopBudgetRef is the arena GCRef of the gibbous back-edge fuel counter word
	// (issue: the P3 loop step-budget gap, the P3 dual of #102). P4 native counts
	// via JITContext.loopFuel, and the back edge inline dec+jz crosses tiers to
	// host.LoopPreempt to bill only on hitting zero; P3 wasm has no equivalent
	// mechanism — the back edge only inline-checks gcPending (pure GC use), so with
	// no async producer for the step budget ⟹ a fully-inlined infinite loop
	// (for i=0,1/0 do X=0 end) that the interpreter preempts and immediately raises
	// "instruction budget exceeded" would hang forever once lifted to P3. This word
	// is the P3 version of loopFuel: the back edge inline-decrements it, and only on
	// hitting zero (or gcPending being set) crosses tiers to h_safepoint; Safepoint
	// bills the consumed quantum into stepBudget, refills, and raises on overrun.
	// When budget/ctx is armed it refills a small amount (loopFuelQuantum),
	// otherwise a large amount ⟹ a steady-state loop crosses tiers almost never
	// (mirrors P4 SegCallFuelUnlimited). 0 = not allocated (also allocated in
	// non-p3 builds, so the offset logic is unified).
	loopBudgetRef arena.GCRef

	// ciTransferRef is the base transfer word for the gibbous→gibbous
	// call_indirect direct call (PW10 R3). When DoCall finds the callee already
	// lifted to gibbous, it writes the callee's frame base byte offset into this
	// word + returns an indirect sentinel; the caller wasm reads it as the
	// call_indirect argument. After call_indirect returns, DoReturn has already
	// written the refreshed caller base back into this word, and the caller reads
	// it to continue addressing. LIFO-safe (each write is immediately followed by
	// the sole reader, no interleaving). 0 = not allocated (also allocated in
	// non-p3 builds, so the offset logic is unified).
	ciTransferRef arena.GCRef

	// ciDepthRef is the linear-memory mirror word of the main thread's frame depth
	// th.ciDepth (PW10 zero-cross Stage 1a). The Wasm-side frame build/teardown
	// (Stage 2/3) increments/decrements it without going back to Go to change
	// ciDepth. Only mainTh writes it (coroutines do not lift, gibbous only runs on
	// mainTh) — wired through mainTh.ciDepthWordRef. 0 = not allocated (also
	// allocated in non-p3 builds, so the offset logic is unified).
	ciDepthRef arena.GCRef

	// ciSegBaseRef is the linear-memory mirror word of the main thread's CI
	// segment current byte base (ciBaseW*8) (PW10 zero-cross Stage 2). The CI
	// segment can be relocated by growCISeg, so the Wasm-side frame build/teardown
	// must read this word to compute the frame address on the fly (segment base +
	// depth*ciWords*8 + word*8) rather than burning a compile-time immediate. Only
	// mainTh writes it (via ciSegBaseWordRef), updated by growCISeg/newThread. 0 = not allocated.
	ciSegBaseRef arena.GCRef

	// openGuardRef is the linear-memory mirror word of the "open upvalue guard
	// value" (PW10 zero-cross Stage 2). Value = maxOpenIdx+1 (openUvs non-empty) /
	// 0 (empty). The Wasm-side RETURN fast-path guard "this frame has no open
	// upvalue to close" ⟺ frameBase ≥ openGuard (always true when empty since ≥0;
	// when non-empty ≥maxOpenIdx+1 ⟺ >maxOpenIdx = the no-op condition of
	// closeUpvals). Only mainTh writes it.
	openGuardRef arena.GCRef

	// topRef is the linear-memory mirror word of the main thread's stack top
	// th.top (PW10 zero-cross ①). The Wasm-side frame build/teardown (④ set the
	// callee frame top when building / ③ the caller self-restores when tearing
	// down) writes it without going back to Go to change th.top.
	// **Stores the slot index (not the byte address)**: th.top is a slot index
	// relative to stackBaseW; growStack changes stackBaseW but the slot index is
	// unchanged → storing the slot index makes grow safe with zero re-sync (the
	// arena view-aliasing hazard cashed in: do not cache derived absolute
	// addresses). Only mainTh writes it (via topWordRef). 0 = not allocated.
	topRef arena.GCRef

	// protoCacheRef is the proto-field cache segment (PW10 zero-cross infra-b).
	// Length = len(st.protos) words, one word per proto, layout: [15:0]MaxStack |
	// [23:16]NumParams | [24]IsVararg | [25]NeedsArg | [63:26]reserved. ④ emitCall
	// guard fast path reads this to avoid a Go map lookup of Proto fields.
	// (Re)allocated + all cache words written at LoadProgram; never changed at
	// runtime (proto fields are fixed at compile time). The segment can be
	// relocated → the base is mirrored to Wasm for on-the-fly reads via protoCacheBaseRef.
	protoCacheRef     arena.GCRef
	protoCacheBaseRef arena.GCRef
	protoCacheLen     uint32 // current word count of the protoCacheRef segment (for freeing the old one)

	// fastCallHitsRef is the mirror word of the ④ emitCall guard fast-path hit
	// count (for PW10 zero-cross ④ verification). i64 ++ inside Wasm (single-thread
	// mainTh, no race), read by the Go test side. When ④ hits it does not call
	// helperCall, so indirectCalls does not grow — this word supplies "fast-path
	// hit visibility", letting R3/PW10 test assertions relax to "indirectCalls +
	// fastCallHits ≥ before+1". No production function.
	fastCallHitsRef arena.GCRef

	// indirectCalls counts gibbous→gibbous call_indirect direct-call hits (for PW10
	// R3 verification, ++ when tryIndirectCallee returns the indirect sentinel).
	// Read only by tests, confirming the direct-call path is actually taken (not a
	// silent fallback to code.Run). No production meaning; the one int overhead is negligible.
	indirectCalls uint64

	// doReturnHits counts how many times h_return (DoReturn) is called (for PW10
	// zero-cross ③b verification). When the Wasm-side RETURN guard fast path hits
	// it does **not** call DoReturn, so this counter stalling proves the fast path
	// really took effect (not a fake-green from falling back to helperReturn
	// everywhere). Read only by tests. No production meaning.
	doReturnHits uint64

	// frameInlineZeroCrossHits counts zero-cross path hits (following §9.20.12
	// commit-5u): the number of times the helper ExecuteCalleeFromInlineFrame,
	// finding the reverse-looked-up callee is also P4-lifted, calls enterGibbous
	// directly, skipping the executeFrom interpreter main loop.
	frameInlineZeroCrossHits uint64
}

// SetCompileFn injects the compile callback (assembled at wangshu.NewState;
// used by loadstring).
func (st *State) SetCompileFn(fn func(src []byte, chunkname string) (uint32, []*bytecode.Proto, error)) {
	st.compileFn = fn
}

// Bridge exposes the P2 tiering bridge (`docs/design/p2-bridge/`) for use
// inside the internal packages (main-loop sampling hook points / AnalyzeProto at Compile time).
//
// **Not exposed to the public API** — the facade layer wangshu.go injects via
// setters like SetBridgeP3Compiler / SetBridgeLogger, not taking a
// *bridge.Bridge directly, to avoid the public face forming a dependency on the
// internal/bridge type.
func (st *State) Bridge() *bridge.Bridge { return st.bridge }

// CompileAndLoad compiles a piece of source and loads it as a closure (the core of loadstring).
func (st *State) CompileAndLoad(src []byte, chunkname string) (value.Value, error) {
	if st.compileFn == nil {
		return value.Nil, errf("loadstring: compiler not available")
	}
	mainID, protos, err := st.compileFn(src, chunkname)
	if err != nil {
		return value.Nil, err
	}
	cl := st.LoadProgram(mainID, protos)
	return value.MakeGC(value.TagFunction, cl), nil
}

// SetStringLib registers the string library table as the per-type metatable
// __index for string values (backs the `("x"):upper()` syntax, 07 §1.2).
func (st *State) SetStringLib(t arena.GCRef) { st.stringLib = t }

// SetStringMeta registers the REAL shared string metatable (PUC shape:
// {__index = string}). getmetatable("") returns it, and the string
// __index path reads its __index field live, so scripts mutating the
// table (getmetatable("").__index = ...) behave like official 5.1.5.
func (st *State) SetStringMeta(t arena.GCRef) { st.stringMeta = t }

// StringMeta returns the shared string metatable (0 if unset).
func (st *State) StringMeta() arena.GCRef { return st.stringMeta }

// New constructs a fresh State (arena + collector + empty globals).
// New builds a State with the arena at default capacity (64 KiB initial / 2 GiB cap).
// Use NewWithOptions when arena capacity customization is needed.
func New() *State { return NewWithOptions(arena.Options{}) }

// NewWithOptions builds a State, passing arenaOpts (wangshu.Options.{InitialArenaBytes,
// MaxArenaBytes}) through to arena.New (zero values fall back inside arena.New
// to the default 64 KiB / 2 GiB, issue #11 direction 2).
func NewWithOptions(arenaOpts arena.Options) *State {
	a, cleanup, p3env := newStateArena(arenaOpts)
	c := gc.New(a, gc.Options{})
	st := &State{arena: a, gc: c, bridge: bridge.NewBridge(), arenaCleanup: cleanup, p3env: p3env}
	st.globals = object.AllocTable(a, 0, 8)
	c.LinkSweep(st.globals)
	// gcPending flag word (P3 PW9): allocate one arena word; the collector
	// mirrors the GC-due state, and the gibbous FORLOOP back edge inline-reads it
	// (avoiding an unconditional cross-tier h_safepoint every iteration).
	// Allocated early → stable offset; also allocated in non-p3 builds (the 1-word
	// overhead is negligible, so the offset logic is unified).
	st.gcPendingRef = a.AllocWords(1)
	a.SetWordAt(st.gcPendingRef, 0)
	c.SetGCPendingRef(st.gcPendingRef)
	// loop-budget fuel word (P3 loop step-budget gap fix): the gibbous back edge
	// inline-decrements this word, crossing tiers to h_safepoint to bill only on
	// hitting zero. Allocated early → stable offset; the initial value 0 makes the
	// first back edge after lifting cross tiers once (Safepoint refills the correct
	// quantum per the armed state).
	st.loopBudgetRef = a.AllocWords(1)
	a.SetWordAt(st.loopBudgetRef, 0)
	// ci-transfer relay word (P3 PW10 R3): the gibbous→gibbous call_indirect direct
	// call passes the callee/refreshed base byte offset through this word (see the
	// field comment). Allocated early → stable offset.
	st.ciTransferRef = a.AllocWords(1)
	a.SetWordAt(st.ciTransferRef, 0)
	// ci-depth cursor word (P3 PW10 zero-cross Stage 1a): the linear-memory mirror
	// of the main thread's frame depth th.ciDepth; the Wasm-side frame
	// build/teardown (Stage 2/3) increments/decrements it without going back to Go.
	// Allocated early → stable offset; also allocated in non-p3 builds (the 1-word
	// overhead is negligible). Only mainTh writes it (coroutines do not lift).
	st.ciDepthRef = a.AllocWords(1)
	a.SetWordAt(st.ciDepthRef, 0)
	// ci-seg-base word (PW10 zero-cross Stage 2): the main thread's CI segment
	// current byte base, updated after growCISeg relocates, so the Wasm-side frame
	// build/teardown reads it to compute the frame address on the fly (the segment
	// can be relocated, cannot burn an immediate).
	st.ciSegBaseRef = a.AllocWords(1)
	a.SetWordAt(st.ciSegBaseRef, 0)
	// open-upvalue guard word (PW10 zero-cross Stage 2): maxOpenIdx+1 / 0, used by
	// the Wasm RETURN fast-path guard "this frame has no open upvalue".
	st.openGuardRef = a.AllocWords(1)
	a.SetWordAt(st.openGuardRef, 0)
	// top mirror word (PW10 zero-cross ①): the linear-memory mirror of the main
	// thread's stack top th.top (slot index); the Wasm-side frame build/teardown
	// writes it without going back to Go to change th.top (the GC stack-root scan
	// upper bound). Stores the slot index → grow-safe.
	st.topRef = a.AllocWords(1)
	a.SetWordAt(st.topRef, 0)
	// proto cache base mirror word (PW10 zero-cross infra-b): the byte base of the
	// protoCacheRef segment, updated after LoadProgram (re)allocates the segment.
	// The Wasm ④ emitCall guard fast path reads this base + protoID*8 on the fly to fetch the cache word.
	st.protoCacheBaseRef = a.AllocWords(1)
	a.SetWordAt(st.protoCacheBaseRef, 0)
	// fast-call hits counter word (PW10 zero-cross ④ verification): ++ inside Wasm
	// on ④ hit, asserted by tests together with R3 indirectCalls for R3/④ path hit visibility.
	st.fastCallHitsRef = a.AllocWords(1)
	a.SetWordAt(st.fastCallHitsRef, 0)
	st.installRoots()
	st.wireP3() // wangshu_p3 build: construct the gibbous wasm Compiler and inject into bridge; default build / p4 build no-op
	st.wireP4() // wangshu_p4 build: construct the gibbous jit Compiler and inject into bridge; default build / p3 build no-op
	// host closure slot recycling (dynamically registered HostFns like the gmatch
	// iterator, mountArena column proxies, etc. release their slot after their
	// closure is GC'd, keeping the registry bounded).
	c.SetHostFnReleaser(st.releaseHostFn)
	// __gc finalizer scheduling (06 §10): call the __gc metamethod of a userdata after it dies and is resurrected.
	c.SetFinalizerRunner(func(ud arena.GCRef) {
		meta := object.UserdataMetaRef(st.arena, ud)
		if meta.IsNull() {
			return
		}
		key := value.MakeGC(value.TagString, st.gc.Intern([]byte("__gc")))
		h, _ := st.tableGet(meta, key)
		if value.Tag(h) != value.TagFunction || st.runningThread == nil {
			return
		}
		udv := value.MakeGC(value.TagUserdata, ud)
		// a finalizer error is swallowed (5.1: the error does not propagate, the GC process continues)
		_, _ = st.callLuaFromHost(st.runningThread, h, []value.Value{udv})
	})
	return st
}

// installRoots injects the current State's root set into the collector.
//
// While the value stack lives in a Go slice it is exposed via ExtraValues; the
// table data already lives in the arena native layout, and all Values also go
// through ExtraValues (revoked after M11 switches to the arena hash).
func (st *State) installRoots() {
	st.gc.SetRoots(gc.Roots{
		Globals:           st.globals,
		ProgramStringRefs: st.visitProgramStringRefs,
		ExtraValues:       st.visitExtraValues,
		ExtraRefs:         st.visitExtraRefs,
	})
}

// visitProgramStringRefs exposes R6: the interned GCRef of each string literal within each Proto.
func (st *State) visitProgramStringRefs(visit func(arena.GCRef)) {
	for _, refs := range st.strRefs {
		for _, r := range refs {
			if !r.IsNull() {
				visit(r)
			}
		}
	}
}

// visitExtraValues exposes the Values held by all live thread stacks (the
// bypass root while the value stack lives in a Go slice).
//
// After the freelist reuses memory, a missed root is a use-after-free: besides
// runningThread, the suspended caller threads on the resume chain
// (threadChain), the stacks of all non-dead coroutines, the coroutine main
// function (held only by the Go struct before the first resume) and the xfer
// transfer area must all be reachable. The compound values of the globals
// baseline (the root for issue #6 ResetGlobalsToBaseline) are scanned here too.
func (st *State) visitExtraValues(visit func(value.Value)) {
	// globals baseline: compound values without a root → the next Reset writes an already-dead GCRef into _G
	for i := range st.baseline {
		visit(st.baseline[i].val)
	}
	// fast path (the vast majority of loads): no coroutines, no resume chain →
	// scan runningThread directly, zero map allocation (every Collect round does a
	// root scan, and the slow path's seen map is GC self-harm).
	if len(st.cos.cos) == 0 && len(st.threadChain) == 0 {
		st.visitThreadValues(st.runningThread, nil, visit)
		return
	}
	if st.gcSeen == nil {
		st.gcSeen = map[*thread]bool{}
	}
	clear(st.gcSeen)
	seen := st.visitThreadValues(st.runningThread, st.gcSeen, visit)
	for _, th := range st.threadChain {
		seen = st.visitThreadValues(th, seen, visit)
	}
	for _, co := range st.cos.cos {
		if co.status == CoDead {
			continue
		}
		seen = st.visitThreadValues(co.th, seen, visit)
		visit(co.fn)
		for _, v := range co.xfer {
			visit(v)
		}
	}
}

// visitThreadValues scans one thread's stack/varargs; seen=nil means the
// single-thread fast path (no dedup, no allocation).
func (st *State) visitThreadValues(th *thread, seen map[*thread]bool, visit func(value.Value)) map[*thread]bool {
	if th == nil || (seen != nil && seen[th]) {
		return seen
	}
	if seen != nil {
		seen[th] = true
	}
	// PW10 zero-cross ①: the GC stack-root scan upper bound reads liveTop() (after
	// the Wasm frame build/teardown changes the top word, the segment is
	// authoritative); when ① lands the Wasm has not yet written the word, so
	// liveTop equals th.top and the flip is a zero behavior change.
	top := th.liveTop()
	for i := 0; i < top; i++ {
		visit(th.slot(i))
	}
	// Clear stale residue above top to nil (aligning with the official lgc.c
	// traversestack): otherwise a dead reference stays in the slot, and after top
	// rises and overwrites it the next GC scans it as a live root — a
	// use-after-free under freelist memory reuse (marking a freed block corrupts the freelist chain).
	for i := top; i < th.size(); i++ {
		th.setSlot(i, value.Nil)
	}
	// After VS0-e the vararg area lives in the lower stack region
	// stack[base-nVarargs..base), in the same segment as the registers. Scanning
	// the stack [0, top) naturally covers every active frame's varargs (vararg slot
	// < base < top), so there is no separate ciVarargs root scan; the old ciVarargs scan is retired.
	return seen
}

// visitExtraRefs exposes the objects that all live threads hold directly as
// GCRefs on ci/openUvs, plus the LoadProgram-produced closures (loaded-cache
// resident roots) and the public-facing Value pin table.
func (st *State) visitExtraRefs(visit func(arena.GCRef)) {
	for _, cl := range st.loadedCls {
		visit(cl)
	}
	// stringMeta is held only by this Go-side field (unlike stringLib,
	// which is also reachable via the "string" global): without a root
	// here the collector frees it and the next string index is a
	// use-after-free (caught by TestGCStress_AllocHeavy on the first
	// build that added the table).
	if !st.stringMeta.IsNull() {
		visit(st.stringMeta)
	}
	for _, r := range st.pinnedRefs {
		if !r.IsNull() {
			visit(r)
		}
	}
	if len(st.cos.cos) == 0 && len(st.threadChain) == 0 {
		st.visitThreadRefs(st.runningThread, nil, visit)
		return
	}
	if st.gcSeenRefs == nil {
		st.gcSeenRefs = map[*thread]bool{}
	}
	clear(st.gcSeenRefs)
	seen := st.visitThreadRefs(st.runningThread, st.gcSeenRefs, visit)
	for _, th := range st.threadChain {
		seen = st.visitThreadRefs(th, seen, visit)
	}
	for _, co := range st.cos.cos {
		if co.status == CoDead {
			continue
		}
		seen = st.visitThreadRefs(co.th, seen, visit)
	}
}

func (st *State) visitThreadRefs(th *thread, seen map[*thread]bool, visit func(arena.GCRef)) map[*thread]bool {
	if th == nil || (seen != nil && seen[th]) {
		return seen
	}
	if seen != nil {
		seen[th] = true
	}
	// PW10 R2b-3: each frame's closure root is read from the arena ci segment
	// (word3), the segment being the authoritative root source. cl is written to
	// the segment at push and never changes afterward, so the segment holds the
	// correct cl for all active frames [0, ciDepth) (including the current frame —
	// th.cur is the hot mirror, but cl is not changed on the hot path). Form Y computes the address on the fly.
	// PW10 Stage 1c: the depth reads liveCIDepth (after the Wasm-side frame
	// build/teardown changes the depth, the segment is authoritative; otherwise the
	// closure of a Wasm-newly-pushed frame is missed = UAF; the immediate word
	// decrement after a Wasm frame pop = no over-scan). GC is single-goroutine + Wasm
	// is not on the stack, so live does not change during the scan → hoist as a loop
	// invariant, same style as visitThreadValues above (state.go:406).
	live := th.liveCIDepth()
	for depth := 0; depth < live; depth++ {
		if cl := th.ciSegCl(depth); !cl.IsNull() {
			visit(cl)
		}
	}
	for _, uv := range th.openUvs {
		if !uv.IsNull() {
			visit(uv)
		}
	}
	return seen
}

// Arena exposes the underlying arena (for tests / embedding APIs).
func (st *State) Arena() *arena.Arena { return st.arena }

// Globals returns the GCRef of the globals table.
func (st *State) Globals() arena.GCRef { return st.globals }

// InternForEmbed exposes the collector's string intern path for the embedding
// API (11 §1.3 lazy interning of string constants; needed for Value bridging).
func (st *State) InternForEmbed(b []byte) arena.GCRef {
	return st.gc.Intern(b)
}

// SetGCStressMode toggles the high-frequency GC stress mode (used by the 12 §5 GC-transparency fuzz).
func (st *State) SetGCStressMode(on bool) { st.gc.SetStressMode(on) }

// SetForceAllPromote toggles the force-all-promote mode (P3 PW9 inter-tier differential-test entry, 08 §2.2).
//
// Forwards to Bridge.SetForceAllPromote: once set, every compilable Proto is
// lifted to gibbous on first execution (bypassing the heat threshold, **not**
// bypassing the compilability gate), removing the timing nondeterminism of
// "which Protos are hot enough", making crescent vs gibbous inter-tier
// differences reproducible + maximizing coverage. No-op when bridge is nil
// (P1-only build). **Testing-only** — exposed via the facade-layer testing entry, not a supported run mode.
func (st *State) SetForceAllPromote(on bool) {
	if st.bridge != nil {
		st.bridge.SetForceAllPromote(on)
	}
}

// SetTierEnabled flips the runtime tier kill switch (production admin
// API). enabled=false stops new promotions and routes already-promoted
// protos back to the interpreter at the next dispatch decision
// (Bridge.GibbousCodeOf returns nil while off); enabled=true restores
// tiered execution reusing installed code. No-op when bridge is nil
// (P1-only build — there is no tier to disable).
func (st *State) SetTierEnabled(enabled bool) {
	if st.bridge != nil {
		st.bridge.SetTierEnabled(enabled)
	}
}

// TierEnabled reports the runtime tier kill switch state. Always true
// on a P1-only build (nil bridge): the interpreter is the only tier.
func (st *State) TierEnabled() bool {
	if st.bridge != nil {
		return st.bridge.TierEnabled()
	}
	return true
}

// TierStatsSnapshot returns the per-State tier distribution (forwards
// to Bridge.TierStatsSnapshot; production admin API). On a P1-only
// build (nil bridge) it returns the zero distribution with
// TierEnabled=true — the interpreter is the only tier.
func (st *State) TierStatsSnapshot() bridge.TierStats {
	if st.bridge != nil {
		return st.bridge.TierStatsSnapshot()
	}
	return bridge.TierStats{TierEnabled: true}
}

// SetHotThresholds overrides the natural-heat promotion thresholds
// (**testing-only**; forwards to Bridge.SetHotThresholds; 0 keeps that
// threshold unchanged). Lowering them lets short scripts / fuzz inputs
// actually reach the auto-mode promotion decision chain
// (recheckCompilabilityRuntime / PromotionGater / short-proto floor +
// FloorExempter — all of which forceAll bypasses or never consults).
// Changes only WHEN the decision runs, never WHAT it decides. No-op
// when bridge is nil.
func (st *State) SetHotThresholds(entry, backEdge uint32) {
	if st.bridge != nil {
		st.bridge.SetHotThresholds(entry, backEdge)
	}
}

// PromotionCount returns the number of Protos already lifted on the current
// State (testing-only, forwards to Bridge.PromotionCount). Returns 0 when bridge
// is nil (P1-only build / P3 not injected).
//
// See the bridge.go PromotionCount godoc for usage: assert "really promoted" under the auto-lifting form.
func (st *State) PromotionCount() int {
	if st.bridge != nil {
		return st.bridge.PromotionCount()
	}
	return 0
}

// SafepointCalls returns the cumulative number of times a gibbous back edge
// crosses tiers into host.Safepoint (white-box probe, for tests: proving that
// with no budget the unlimited loopFuel refill keeps a hot loop from crossing tiers almost entirely).
func (st *State) SafepointCalls() int64 { return st.safepointCalls }

// SetStepBudget sets the back-edge instruction budget (<=0 disables). On
// overrun the script terminates with a recoverable "instruction budget
// exceeded" error — a host-side script quota feature, used by fuzzing instead of
// fragile source-substring filtering to catch infinite/overlong loops.
func (st *State) SetStepBudget(n int64) {
	st.stepBudget = n
	st.stepUsed = 0
	st.budgetGen.Add(1)
}

// SetAllowFileLoad toggles the filesystem-read capability of loadfile/dofile (default off).
func (st *State) SetAllowFileLoad(on bool) { st.allowFileLoad = on }

// AllowFileLoad queries the file-read switch (used by stdlib loadfile/dofile).
func (st *State) AllowFileLoad() bool { return st.allowFileLoad }

// preempt is the VM preempt point entry (called once each by back edge / frame
// entry / TFORLOOP). It aligns with Go runtime preemption semantics: a preempt
// point is "check at an instruction boundary whether to yield execution". This
// implementation's yield conditions:
//   - stepBudget > 0 and stepUsed overrun → "instruction budget exceeded"
//   - ctx injected and ctx.Err() non-nil → "context canceled: <err>"
//
// Caller contract: must be called at an instruction boundary (between
// opcodes/frames), not mid-opcode. The three preempt points (execute.go JMP back
// edge / execute.go TFORLOOP continuation / frame.go enterLuaFrame frame entry)
// share this single entry — the same preempt point in P3+ JIT-generated code
// also goes through this logic (can be copied directly or inlined as machine code).
//
// Performance: the fast path with no budget and no ctx is "check two fields for
// 0/nil → return nil", already inlined; enabling either adds one atomic.Load or
// int comparison, with no visible effect observed on the perf benchmark round.
//
// History: before v0.1.2 named chargeStep (only billed budget); when SetContext
// landed on issue #4 the ctx check was merged in, and after audit it was renamed
// preempt to reflect the "preempt point" rather than "billing" semantics.
func (st *State) preempt() *LuaError {
	if st.stepBudget > 0 {
		st.stepUsed++
		if st.stepUsed > st.stepBudget {
			return errf("instruction budget exceeded")
		}
	}
	if h := st.ctx.Load(); h != nil {
		if err := h.err(); err != nil {
			return errf("context canceled: %s", err.Error())
		}
	}
	return nil
}

// SetCancelHook injects a cancellation callback (the internal bridge of the
// issue #4 public SetContext). When fn returns a non-nil error, the VM aborts the
// current Call/Run at the next preempt point. Atomically replaced, cross-goroutine
// safe; passing nil is equivalent to RemoveCancelHook.
func (st *State) SetCancelHook(fn func() error) {
	if fn == nil {
		st.ctx.Store(nil)
		return
	}
	st.ctx.Store(&ctxHolder{err: fn})
}

// CancelHookActive reports whether a cancellation callback is currently installed (diagnostics/tests).
func (st *State) CancelHookActive() bool { return st.ctx.Load() != nil }

// GCCollect triggers one full GC (used by collectgarbage("collect")).
func (st *State) GCCollect() { st.gc.Collect() }

// GCSetStopped pauses/resumes automatic GC (collectgarbage("stop"/"restart")).
func (st *State) GCSetStopped(on bool) { st.gc.SetStopped(on) }

// getArgsBuf / putArgsBuf: the callHost argument buffer pool.
func (st *State) getArgsBuf(n int) []value.Value {
	if last := len(st.argsPool) - 1; last >= 0 {
		buf := st.argsPool[last]
		st.argsPool = st.argsPool[:last]
		if cap(buf) >= n {
			return buf[:n]
		}
	}
	if n < 8 {
		return make([]value.Value, n, 8)
	}
	return make([]value.Value, n)
}

func (st *State) putArgsBuf(buf []value.Value) {
	// Clear references so a pooled slice does not extend the Go-side visibility of
	// arena objects (not a GC root, purely hygiene).
	// Under the trace build fill a poison value: if a HostFn violates the contract
	// by retaining args, reading the poison shows up immediately, rather than the
	// remote symptom of "being silently overwritten by a later call".
	poison := value.Nil
	if traceExec {
		poison = value.NumberValue(-6.66e66)
	}
	for i := range buf {
		buf[i] = poison
	}
	st.argsPool = append(st.argsPool, buf)
}

// GCCountKB returns the arena's current live KB (collectgarbage("count") /
// gcinfo): bump - freelist free bytes, approximating the official totalbytes
// semantics (falls back after a GC collection; the official test suite gc.lua's
// "run until gc" loop depends on that fall-back). Still an observable-but-not-
// byte-comparable item (10 §13: the freelist granularity / auxiliary-block
// accounting differs from the official allocator).
func (st *State) GCCountKB() float64 {
	used := uint64(st.arena.Bump()) - st.arena.FreeBytes()
	return float64(used) / 1024.0
}

// ArenaCapKB returns the arena backing's current **capacity** in KB (issue #11
// direction 3). The difference from GCCountKB: GCCountKB measures "live bytes"
// and shrinks on Collect; ArenaCapKB measures "backing slab capacity", reflecting
// the real Go-heap residency (monotonically rising when grow-only). The pool
// layer thresholds on this to decide whether to drop a fat state.
func (st *State) ArenaCapKB() float64 {
	return float64(st.arena.Cap()) / 1024.0
}

// Collect forcibly triggers one full GC sweep (corresponds to Lua
// collectgarbage("collect")). Issue #9 direction 2: let the host embedding layer
// explicitly drive the GC cadence, avoiding the roundabout path of a
// collectgarbage script call. Under short scripts / host-driven allocation,
// calling it periodically keeps the arena's internal accounting bounded (but the
// backing capacity does not shrink, see ArenaCapKB / issue #11).
func (st *State) Collect() {
	st.gc.Collect()
}

// MaybeCollectNow triggers conditionally on the GC threshold (may collect or may
// no-op). Equivalent to the host triggering one safepoint check — the minimal safe
// surface of issue #9 direction 2. Under short-script embedding, calling it
// periodically substitutes for the starvation from insufficient VM-opcode
// safepoint trigger frequency.
func (st *State) MaybeCollectNow() {
	st.gc.MaybeCollect()
}

// SetHostTriggeredCollect toggles host-alloc crossing-threshold directly
// triggering collect (issue #9 direction 1).
// **Opt-in, off by default** — enabling requires the caller to guarantee all
// transient GCRefs are pin/shadow-stack reachable. See the
// gc.Collector.SetHostTriggeredCollect godoc for the detailed safety contract.
// The current stdlib/intern paths have mid-construction transients, **so enabling
// in production is not recommended before audit**; the recommended approach is
// explicit cadence control via Collect() / MaybeCollectNow() (issue #9 direction 2).
func (st *State) SetHostTriggeredCollect(on bool) {
	st.gc.SetHostTriggeredCollect(on)
}

// NewError constructs a LuaError with a message (for use by stdlib and other host functions).
func NewError(msg string) *LuaError {
	return &LuaError{Msg: msg}
}

// NewArgError mirrors PUC luaL_argerror for host functions (issue
// #133): narg is 1-based, extra is the parenthesized detail ("string
// expected, got nil"). Msg starts with the C-caller fallback "'?'"
// (what PUC prints when no Lua frame can name the callee — pcall(f,
// ...), sort comparators, metamethod handlers); the interpreter's
// Lua-side call boundaries rewrite it with the caller-derived name via
// resolveArgError before position annotation.
func NewArgError(narg int, extra string) *LuaError {
	return &LuaError{
		Msg:      fmt.Sprintf("bad argument #%d to '?' (%s)", narg, extra),
		argNarg:  narg,
		argExtra: extra,
	}
}

// NewErrorVal constructs an error carrying a Lua Value (corresponds to the error(v) builtin).
func NewErrorVal(v value.Value, msg string) *LuaError {
	return &LuaError{Value: v, HasValue: true, Msg: msg, Level: 1}
}

// MarkAnnotated blocks the position-prefix annotation (error(v, 0) / non-string error value).
func (e *LuaError) MarkAnnotated() { e.annotated = true }

// TypeNameOf exposes the internal typeName for stdlib to implement the type() builtin.
func TypeNameOf(v value.Value) string { return typeName(v) }

// TypeName is the State-aware type name: unlike package-level
// TypeNameOf it recognizes coroutine handles (lightuserdata present
// in the registry -> "thread", matching PUC). Error messages and
// type() should both go through here; the package-level form only
// serves contexts without a State.
func (st *State) TypeName(v value.Value) string { return st.typeNameOf(v) }

// typeNameOf is the State-aware internal form of typeName (the single
// entry point for error-message paths).
func (st *State) typeNameOf(v value.Value) string {
	if st.IsCoroutineHandle(v) {
		return "thread"
	}
	return typeName(v)
}

// NewLibTable provides a new table to stdlib (for hanging the stdlib namespace).
func (st *State) NewLibTable(approxFields uint32) arena.GCRef {
	hsz := uint32(8)
	for hsz < approxFields {
		hsz *= 2
	}
	t := st.allocTable(0, hsz)
	return t
}

// SetTableField gives stdlib a convenient interface to "write a table field by string key".
func (st *State) SetTableField(t arena.GCRef, name string, v value.Value) {
	ref := st.gc.Intern([]byte(name))
	key := value.MakeGC(value.TagString, ref)
	_ = st.tableSet(t, key, v)
}

// LoadProgram registers the compiled Protos and lazy-interns their string
// literals (Proto §lazy literal interning; 06 §5.1 R6 rewrite). Returns the
// closure GCRef corresponding to mainID (0 upvalues; main chunk).
//
// A Program is immutable and shareable across States (11 §1.4): here each Proto
// is shallow-copied State-private — sharing the read-only Code/StringLits/LineInfo,
// privatizing Consts (interned into this State's arena), IC (runtime-writable) and
// Protos (relative index → this State's absolute ProtoID).
func (st *State) LoadProgram(mainID uint32, protos []*bytecode.Proto) arena.GCRef {
	base := uint32(len(st.protos))
	for _, p := range protos {
		cp := *p // shallow copy: Code/StringLits/LineInfo/UpvalDescs/LocVars share the read-only underlying arrays
		// private Protos: relative index fixed up to absolute ProtoID
		cp.Protos = make([]uint32, len(p.Protos))
		for i, id := range p.Protos {
			cp.Protos[i] = base + id
		}
		// private Consts: string literals interned into this State's arena
		cp.Consts = make([]value.Value, len(p.Consts))
		copy(cp.Consts, p.Consts)
		refs := make([]arena.GCRef, len(p.Consts))
		for i := range cp.Consts {
			if p.IsStringConst(i) {
				lit := p.StringLits[p.StringLitIdx[i]]
				refs[i] = st.gc.Intern([]byte(lit))
				cp.Consts[i] = value.MakeGC(value.TagString, refs[i])
			}
		}
		// private IC (runtime-writable; cannot be shared across States)
		cp.IC = make([]bytecode.ICSlot, len(p.Code))
		st.protos = append(st.protos, &cp)
		st.strRefs = append(st.strRefs, refs)
	}
	cl := st.allocLuaClosure(base+mainID, 0)
	st.loadedCls = append(st.loadedCls, cl)
	st.rebuildProtoCache()
	// Size the bridge's ProtoID-indexed ProfileData fast path (issue #94)
	// to the new proto count, so the OnEnterID/OnBackEdgeID hot hooks run
	// a slice index instead of a map lookup.
	st.bridge.GrowProfileIndex(len(st.protos))
	return cl
}

// rebuildProtoCache (re)allocates the protoCacheRef segment and fills in the
// field cache of all protos (PW10 zero-cross infra-b). On a repeated LoadProgram
// the old segment is Free'd + the new segment is fully rewritten, and the
// protoCacheBaseRef mirror word is updated in sync (the segment can be relocated,
// and Wasm ④ reads the base + protoID*8 on the fly to address).
//
// Word layout (one u64 word per proto):
//
//	[15:0]  MaxStack (uint8, but 16 bits of headroom left)
//	[23:16] NumParams (uint8)
//	[24]    IsVararg
//	[25]    NeedsArg
//	[63:26] reserved
//
// Not called during Wasm execution (LoadProgram is a top-level API boundary), so
// Free'ing the old segment + writing the new base is safe.
func (st *State) rebuildProtoCache() {
	a := st.arena
	if st.protoCacheRef != 0 {
		a.Free(st.protoCacheRef, st.protoCacheLen*8)
		st.protoCacheRef = 0
		st.protoCacheLen = 0
	}
	n := uint32(len(st.protos))
	if n == 0 {
		a.SetWordAt(st.protoCacheBaseRef, 0)
		return
	}
	st.protoCacheRef = a.AllocWords(n)
	st.protoCacheLen = n
	a.SetWordAt(st.protoCacheBaseRef, uint64(uint32(st.protoCacheRef)))
	for pid := uint32(0); pid < n; pid++ {
		p := st.protos[pid]
		w := uint64(p.MaxStack) | uint64(p.NumParams)<<16
		if p.IsVararg {
			w |= 1 << 24
		}
		if p.NeedsArg {
			w |= 1 << 25
		}
		ref := arena.GCRef(uint32(st.protoCacheRef) + pid*8)
		a.SetWordAt(ref, w)
	}
}

// Call executes a Lua closure with the given args, returning all results.
//
// args are the arguments passed by value; the number of return values is
// controlled by the callee (an explicit RETURN returns as many as it gives).
// nresults < 0 means "return all"; otherwise trim/pad with nil to the count.
func (st *State) Call(cl arena.GCRef, args []value.Value, nresults int) ([]value.Value, error) {
	rets, err := st.callOnStack(cl, args, nresults)
	if err != nil {
		return nil, err
	}
	// Copy out an independent slice: under the old contract the return values are
	// still readable after the next Call (the caller may hold them long-term).
	return append([]value.Value(nil), rets...), nil
}

// callOnStack executes the closure, returning the active slice directly on the
// main thread's stack (zero copy).
//
// ⚠️ The underlying slice is the reused th.stack: it is overwritten after the
// next Call/Run resets top. The caller must consume it before next entering the
// VM (read out scalars / copy / register compound values via the pin table).
// After runningThread is reset to nil, mainTh is still a resident root at the
// same level as loadedCls → the return values stay reachable under GC (the stack
// is not shrunk, and the slot values are still referenced by mainTh.stack).
func (st *State) callOnStack(cl arena.GCRef, args []value.Value, nresults int) ([]value.Value, error) {
	if object.IsHostClosure(st.arena, cl) {
		// Calling a host closure directly from the Go side needs a temporary stack
		// frame scaffold, not done this cycle; a Register'd host fn works in a closed
		// loop when called from within Lua (the callHost path).
		return nil, fmt.Errorf("Call: host closure cannot be called from Go end; invoke it from Lua side instead")
	}
	// Reuse the main thread (rule-engine shape = a long-resident State running
	// short scripts at high frequency; a newThread's stack slice/growth each time
	// is pointless Go-heap churn). State is single-goroutine, and the main thread
	// does not re-enter (host→Lua re-entry stacks up on the same th's execute,
	// coroutines each have their own th). Reset: top zeroed + frame depth rewound
	// to 0 (truncateCI), openUvs reused (the last Run's terminal closeUpvals already emptied it).
	th := st.mainTh
	if th == nil {
		th = st.newThread()
		st.mainTh = th
		// mirror the main thread's frame depth to ciDepthRef (PW10 zero-cross Stage 1a):
		// only mainTh is wired, coroutine th is never mirrored (coroutines do not lift).
		// setCIDepth decides whether to write the word based on this field.
		th.ciDepthWordRef = st.ciDepthRef
		th.topWordRef = st.topRef             // ①: stack top th.top mirror (Wasm frame build/teardown writes / GC reads)
		th.setTop(0)                          // initialize the top mirror word (newThread already top=0)
		th.ciSegBaseWordRef = st.ciSegBaseRef // Stage 2: CI segment base mirror (Wasm computes frame address on the fly)
		th.openGuardWordRef = st.openGuardRef // Stage 2: open-upvalue guard word
		th.setCIDepth(0)                      // initialize word = 0 (in sync with the new thread's ciDepth=0)
		th.syncCISegBase()                    // initialize the segment base word (newThread already built the segment)
		th.syncOpenGuard()                    // initialize the guard word (new thread has no open upvalue → 0)
	} else {
		th.setTop(0)
		th.truncateCI(0)
		th.pendingResume = nil
		// If the last Run exited via error, unwind did not go through closeUpvals,
		// so openUvs may still hold open uvs pointing to now-invalid stack positions —
		// close them (self-held snapshot values), clear uvOwner.
		if len(th.openUvs) > 0 {
			st.closeUpvals(th, 0)
		}
	}
	st.runningThread = th
	defer func() { st.runningThread = nil }()
	// push callee + args to the stack bottom
	th.push(value.MakeGC(value.TagFunction, cl))
	for _, v := range args {
		th.push(v)
	}
	// PW10 zero-cross top-level lift + P4 real hookup:
	// the previous `code.Slot() == ok` check was a requirement of the P3 wasm
	// `call_indirect` internal direct call, but the top-level enterGibbous does not
	// depend on slot — any GibbousCode (P3 / P4) can go through it.
	// P4 `Slot()` always returns (0, false) (native code has no wasm-table concept),
	// so the previous ok check made the P4 top-level lift path always skip (when the
	// host directly calls a P4-lifted closure the mmap segment is not taken, and the
	// profile numbers measure the interpreter, violating prove-the-path).
	//
	// Now fixed to "go through enterGibbous if there is GibbousCode" — P3 / P4 share
	// this path; the P3-internal gibbous→gibbous CALL still goes through wasm
	// `call_indirect` (R3 protocol, requires slot ok), but that is a detail of the
	// wasm segment execution within enterGibbous, not affecting the top-level entry.
	if !object.IsHostClosure(st.arena, cl) && profileEnabled && th == st.mainTh && st.bridge != nil {
		pid := object.ClosureProtoID(st.arena, cl)
		if code := st.bridge.GibbousCodeOf(st.protos[pid]); code != nil {
			// pass nresults -1 (callee returns all); at the end callOnStack trims/pads
			// with nil per the user's nresults, same as the interpreter path. Inside
			// enterGibbous entry=false (fresh=false), but the wasm path does not enter the
			// execute main loop and does not depend on fresh; DoReturn ignores fresh and just processes per nresults.
			if err := st.enterGibbous(th, code, 0 /*funcIdx*/, len(args), -1); err != nil {
				if err.Traceback == "" {
					err.Traceback = st.buildTraceback(th)
				}
				return nil, err
			}
			rets := th.activeSlice(th.top)
			if nresults >= 0 {
				if len(rets) > nresults {
					rets = rets[:nresults]
				} else {
					for len(rets) < nresults {
						th.push(value.Nil)
					}
					rets = th.activeSlice(nresults)
				}
			}
			return rets, nil
		}
	}

	// enter the main frame (interpreter path: cl not lifted / host / vararg main chunk / coroutine thread)
	if err := st.enterLuaFrame(th, 0 /*funcIdx in stack*/, len(args), -1 /*caller wants all*/, true /*entry*/); err != nil {
		return nil, err
	}
	if err := st.execute(th); err != nil {
		if err.Traceback == "" {
			err.Traceback = st.buildTraceback(th)
		}
		return nil, err
	}
	// After top-level execution ends, the return values are a few slots starting at
	// the stack bottom (determined by the RETURN landing point dst=funcIdx).
	// Zero copy: slice th.stack's active region directly (contract in the callOnStack doc).
	rets := th.activeSlice(th.top)
	if nresults >= 0 {
		if len(rets) > nresults {
			rets = rets[:nresults]
		} else {
			for len(rets) < nresults {
				th.push(value.Nil)
			}
			rets = th.activeSlice(nresults)
		}
	}
	return rets, nil
}

// CallOnStack is the exported form of callOnStack, for the facade layer's
// zero-alloc CallInto. The return value is the active slice on the main thread's
// stack (zero copy, valid until next entering the VM); see the callOnStack doc for the contract.
func (st *State) CallOnStack(cl arena.GCRef, args []value.Value, nresults int) ([]value.Value, error) {
	return st.callOnStack(cl, args, nresults)
}

// thread is an execution thread: the value stack lives in an arena segment
// (VS0-c form Y), the CallInfo still lives in a Go slice.
//
// **Value stack arena-ization (VS0-c)**: stack is no longer a Go slice but a
// stretch of Value[stackCap] inside the arena (01 §5.6 valueStackRef). Register
// R(i) = the i-th slot of the arena segment (stackBaseW word offset + i).
// Physically it reads/writes the same block of linear memory as gibbous wasm's
// i64.load offset=8*i (under the P3 build) — this is the foundation of the
// end-to-end shared view.
//
// Form Y: slot/setSlot addresses each time via the current view of arena.Words()
// (does not cache a derived slice), immune to arena grow (on grow the arena.words
// field is updated by setBacking, and the next Words() takes the new view). When
// the segment is full, growStack reallocates a larger segment inside the arena +
// copies the old + Free's the old; open upvalues address via owner.slot(idx), and
// after stackBaseW is updated they automatically point to the same position in the
// new segment (no extra relocation needed).
type thread struct {
	arena      *arena.Arena           // the arena where the value stack segment lives (for segment addressing + grow)
	stackBaseW uint32                 // the value stack segment's word offset in the arena (= valueStackRef>>3)
	stackCap   int                    // segment capacity (slot count; = size())
	top        int                    // current stack top (the temporary region above ci.top)
	openUvs    map[uint32]arena.GCRef // stackIdx → open Upvalue ref (M9 simplification, M10 switches to a descending-order chain)

	// --- CallInfo state (PW10 R2b-4: retire the Go []callInfo, the arena segment is authoritative) ---
	//
	// cur is the working copy of the current top frame (hot mirror). currentCI
	// returns &cur — **address-stable**, so a hot loop holding a ci pointer never
	// dangles (eliminating the old &th.cis[len-1] append-relocation hazard,
	// design-claims-vs-codebase-physics §2 constructive resolution). push/pop sync
	// cur ↔ segment: push first flushes cur back to segment[ciDepth-1] (preserving
	// caller pc/base/top) then loads the callee; pop pops cur then reloads the caller from the segment.
	cur     callInfo
	ciDepth int // logical frame depth (= old len(th.cis)); segment[0..ciDepth-1] are the active frames
	ciBaseW uint32
	ciCap   int
	// When ciDepthWordRef is non-zero, setCIDepth mirrors ciDepth to this arena
	// word (PW10 zero-cross Stage 1a). Only mainTh is wired (= st.ciDepthRef after
	// State.New); coroutine th is always 0 (not mirrored).
	ciDepthWordRef arena.GCRef
	// When ciSegBaseWordRef is non-zero, syncCISegBase mirrors the CI segment's
	// current byte base (ciBaseW*8) to this arena word (PW10 zero-cross Stage 2),
	// for the Wasm-side frame build/teardown to compute the frame address on the fly. Only mainTh is wired.
	ciSegBaseWordRef arena.GCRef
	// When openGuardWordRef is non-zero, syncOpenGuard mirrors the open-upvalue
	// guard value (maxOpenIdx+1 / 0) to this arena word (PW10 zero-cross Stage 2),
	// for the Wasm RETURN fast-path guard. Only mainTh.
	openGuardWordRef arena.GCRef
	// When topWordRef is non-zero, setTop mirrors th.top (slot index) to this arena
	// word (PW10 zero-cross ①). The Wasm-side frame build/teardown writes it, the GC
	// stack-root scan reads liveTop(). Only mainTh is wired; coroutine th is always 0.
	topWordRef arena.GCRef
	// maxOpenIdx is the largest stackIdx in openUvs (the equivalent of the official
	// descending-chain head): closeUpvals(level) returns in O(1) when level >
	// maxOpenIdx — RETURN calls closeUpvals every frame, and without this fast path
	// it would fully iterate the map each time (once took 20% CPU).
	// Invariant: openUvs non-empty ⇒ maxOpenIdx = max(keys); empty ⇒ the value is meaningless.
	maxOpenIdx uint32

	// pendingResume records the resume info when a yield bubbles out of execute
	// (the P1 form of the 08 §3.3 saveFrame); consumed by executeResume on resume.
	pendingResume *pendingResumeInfo
}

// initialStackSlots is the initial slot count of a thread's value stack segment
// (aligned with the old Go slice cap 64).
const initialStackSlots = 64

// ciWords is the word count each CallInfo occupies in the arena ci segment
// (VS0-e substep ②: 4 → 5).
//
// **Physical layout (following 04-trampoline §1.2 word2 packing; VS0-e substep ② extends word4)**:
//
//	word0 [31:0]base   [63:32]funcIdx
//	word1 [31:0]top    [63:32]pc(savedPC)
//	word2 [31:0]protoID [47:32]nresults [48]tailcall [49]fresh [50]gibbous
//	word3 cl(GCRef)
//	word4 [15:0]nVarargs (VS0-e substep ②; after substep ③ the lower stack region [base-nVarargs..base) is the authoritative vararg area)
//
// The ci segment is the authoritative source of cold fields + GC roots (since
// R2b-3). **The Wasm-side segment frame stride**:
// internal/gibbous/wasm/helpers_index.go ciFrameBytes must be strictly equal to
// ciWords*8 (40 this round). The working copy of the current top frame is in
// th.cur (hot mirror, currentCI returns &cur).
const ciWords = 5

// initialCISlots is the ci segment's initial frame count (a typical program's
// call depth ≪ this value; deeper triggers growCISeg).
const initialCISlots = 64

// newThread builds a thread whose value stack lives in an arena segment (VS0-c).
// The main thread and coroutines go through this single entry, so the main
// thread's stack and coroutine stacks are arena-ized together.
func (st *State) newThread() *thread {
	th := &thread{arena: st.arena}
	ref := st.arena.AllocWords(initialStackSlots)
	th.stackBaseW = uint32(ref) >> 3
	th.stackCap = initialStackSlots
	// CallInfo arena segment (PW10 R2b): ciWords words per frame, initially initialCISlots frames.
	ciRef := st.arena.AllocWords(initialCISlots * ciWords)
	th.ciBaseW = uint32(ciRef) >> 3
	th.ciCap = initialCISlots
	return th
}

// --- CallInfo arena segment writes (PW10 R2b-1: cold fields write only the mirror) ---
//
// writeCISeg packs a callInfo's fields into the depth-th frame of the ci segment
// (5 words, layout see thread.ciBaseW). R2b-1 calls it after enterLuaFrame pushes
// a frame + after any cold-field change, keeping the segment in sync with the Go
// cis mirror; since R2b-2 the cold accessors read this segment.
//
// Form Y: computes the address on the fly via SetWordAt (reading arena.words's
// current value), does not cache a derived slice, immune to grow (growCISeg / any
// alloc-triggered physical grow).
func (th *thread) writeCISeg(depth int, ci *callInfo) {
	a := th.arena
	wordRef := func(w int) arena.GCRef {
		return arena.GCRef(th.ciBaseW+uint32(depth*ciWords+w)) << 3
	}
	a.SetWordAt(wordRef(0), uint64(uint32(ci.base))|uint64(uint32(ci.funcIdx))<<32)
	a.SetWordAt(wordRef(1), uint64(uint32(ci.top))|uint64(uint32(ci.pc))<<32)
	a.SetWordAt(wordRef(2), packCIWord2(ci))
	a.SetWordAt(wordRef(3), uint64(ci.cl))
	a.SetWordAt(wordRef(4), uint64(ci.nVarargs)) // VS0-e substep ②: nVarargs mirror (other bits reserved)
}

// packCIWord2 packs protoID/nresults/flags into word2 (04-trampoline §1.2
// layout). nresults is an int (-1 means variable), storing the low 16 bits (C-1
// semantics ≤ 0xFFFF; -1 → 0xFFFF).
func packCIWord2(ci *callInfo) uint64 {
	w := uint64(ci.protoID)
	w |= uint64(uint16(ci.nresults)) << 32
	if ci.tailcall {
		w |= 1 << 48
	}
	if ci.fresh {
		w |= 1 << 49
	}
	if ci.gibbous {
		w |= 1 << 50
	}
	return w
}

// readCISegInto unpacks the depth-th frame of the ci segment into out (R2b-1
// round-trip self-check + used by accessors since R2b-2).
func (th *thread) readCISegInto(depth int, out *callInfo) {
	a := th.arena
	wordRef := func(w int) arena.GCRef {
		return arena.GCRef(th.ciBaseW+uint32(depth*ciWords+w)) << 3
	}
	w0 := a.WordAt(wordRef(0))
	w1 := a.WordAt(wordRef(1))
	w2 := a.WordAt(wordRef(2))
	out.base = int(int32(uint32(w0)))
	out.funcIdx = int(int32(uint32(w0 >> 32)))
	out.top = int(int32(uint32(w1)))
	out.pc = int32(uint32(w1 >> 32))
	out.protoID = uint32(w2)
	out.nresults = int(int16(uint16(w2 >> 32))) // sign extension (-1 → 0xFFFF → -1)
	out.tailcall = w2&(1<<48) != 0
	out.fresh = w2&(1<<49) != 0
	out.gibbous = w2&(1<<50) != 0
	out.cl = arena.GCRef(a.WordAt(wordRef(3)))
	out.nVarargs = uint16(a.WordAt(wordRef(4))) // VS0-e substep ②: unpack nVarargs from word4
}

// syncCurFromSeg closes off the "Go-side reverse sync" of the segment-is-
// authoritative flip (PW10 zero-cross Stage 1b).
//
// **Segment is authoritative**: since Stage 2/3 the Wasm-side frame
// build/teardown increments/decrements the ciDepth word + writes segment frames,
// all without going back to Go, so Go's th.ciDepth / th.cur are frozen while Wasm
// executes and the segment is the live one. When control crosses back to Go
// (code.Run returns / fallback helper entry), Go must reverse-sync from the
// segment as the source of truth: read the ciDepth word, and if it differs from
// th.ciDepth (Wasm changed it), adopt the word depth + reload th.cur from the segment.
//
// **Stage 1b is a verification no-op**: at this stage there is no Wasm write, the
// word always equals th.ciDepth, so the sync does not trigger (under wangshu_trace
// the consistency is asserted). Its purpose is to first close off the "segment is
// authoritative" reverse-sync discipline, so that after Stage 2/3 lands Wasm
// writes this takes effect automatically. th.cur's pc/top live in a Wasm local
// (not th.cur) during gibbous frame execution, so reloading th.cur does not lose
// information "newer than the segment" — a gibbous frame never continues from th.cur.
func (th *thread) syncCurFromSeg() {
	if th.ciDepthWordRef == 0 {
		return // not mainTh (coroutines do not lift, no word mirror)
	}
	wd := int(uint32(th.arena.WordAt(th.ciDepthWordRef)))
	if wd == th.ciDepth {
		return // the word matches Go (Stage 1b always takes this path; Stage 2/3 also takes it when Wasm has not changed the depth)
	}
	// Wasm changed the depth (Stage 2/3): adopt the word depth + reload the top
	// frame from the segment. After VS0-e the vararg area lives in the lower stack
	// region (stack[base-nVarargs..base)), so syncCurFromSeg no longer restores the
	// cur.varargs Go slice (retired); nVarargs is unpacked from word4 via
	// readCISegInto + the data is fetched via stack access.
	th.ciDepth = wd
	if wd > 0 {
		th.readCISegInto(wd-1, &th.cur)
	}
}

// ciAt reads a value copy of the depth-th frame's callInfo (read-only access to a
// non-current frame: traceback / coroutine resume).
// The current top frame (depth==ciDepth-1) returns the hot mirror th.cur directly
// (its pc/top may be newer than the segment); the rest are unpacked from the
// segment (after VS0-e nVarargs is unpacked from word4, and the vararg area is
// read on the fly from the lower stack region).
// **Returns a value copy** — the caller must not cache the pointer across an
// allocation (form-Y: read on the fly by depth each time, eliminating *callInfo dangling).
func (th *thread) ciAt(depth int) callInfo {
	if depth == th.ciDepth-1 {
		return th.cur
	}
	var ci callInfo
	th.readCISegInto(depth, &ci)
	return ci
}

// liveCIDepth returns the authoritative frame depth the GC root scan should adopt
// (PW10 zero-cross Stage 1c).
//
// **Why read the word rather than th.ciDepth**: since Stage 2/3 the Wasm-side
// frame build/teardown increments/decrements the ciDepth word + writes segment
// frames during Wasm execution, **all without going back to Go**, so at this
// moment th.ciDepth (the Go field) lags the real depth. A gibbous frame body can
// hit a safepoint (alloc/back edge) triggering GC — at which point GC must scan
// segment-frame closure roots by the **word** depth, otherwise the Wasm-newly-
// pushed active frame is missed = the closure is wrongly collected = UAF.
// mainTh reads the word (ciDepthWordRef≠0); coroutine th has no word mirror (does
// not lift), falling back to th.ciDepth.
//
// Safety: the Stage 3 Wasm frame-build emission order "first write the 4 words of
// the segment frame, then ciDepth++" guarantees that when the word increments the
// segment frame's closure is already in place (no miss); the Stage 2 frame
// teardown "first decrement ciDepth" makes the popped frame no longer scanned (no
// over-scan). When Stage 1c lands, Wasm has not yet written the word (Stage 2/3
// does), so liveCIDepth always equals th.ciDepth and the flip is a zero behavior
// change; the wangshu_trace self-check (syncCurFromSeg/setCIDepth) guards the word being consistent with Go.
func (th *thread) liveCIDepth() int {
	if th.ciDepthWordRef != 0 {
		return int(uint32(th.arena.WordAt(th.ciDepthWordRef)))
	}
	return th.ciDepth
}

// syncCISegBase mirrors the CI segment's current byte base (ciBaseW*8) to the
// linear-memory word (PW10 zero-cross Stage 2). Called after newThread (mainTh) +
// growCISeg relocation, so the Wasm-side frame build/teardown reads the latest
// segment base to compute the frame address on the fly. Only mainTh is wired
// (ciSegBaseWordRef≠0).
func (th *thread) syncCISegBase() {
	if th.ciSegBaseWordRef != 0 {
		th.arena.SetWordAt(th.ciSegBaseWordRef, uint64(th.ciBaseW*8))
	}
}

// syncOpenGuard mirrors the open-upvalue guard value to the linear-memory word
// (PW10 zero-cross Stage 2). Value = maxOpenIdx+1 (openUvs non-empty) / 0 (empty).
// The Wasm RETURN fast-path guard frameBase ≥ this value ⟺ this frame has no open
// upvalue ≥base to close (= closeUpvals no-op). Called after any change to openUvs
// / maxOpenIdx (findOrCreateUpval / closeUpvals). Only mainTh is wired.
func (th *thread) syncOpenGuard() {
	if th.openGuardWordRef == 0 {
		return
	}
	var g uint32
	if len(th.openUvs) != 0 {
		g = th.maxOpenIdx + 1
	}
	th.arena.SetWordAt(th.openGuardWordRef, uint64(g))
}

// setCIDepth sets the frame depth + mirrors it to the linear-memory word (PW10
// zero-cross Stage 1a).
// All writes of th.ciDepth are closed off through here (enterLuaFrame ++ /
// popCallInfo -- / truncateCI =), keeping ciDepthWordRef (mainTh) in sync with Go
// ciDepth. Stage 1a only writes the shadow (Go still reads ciDepth); in Stage 2/3
// the Wasm side increments/decrements the word and the Go side reads it back via
// syncCurFromSeg (the segment-is-authoritative flip).
func (th *thread) setCIDepth(n int) {
	th.ciDepth = n
	if th.ciDepthWordRef != 0 {
		th.arena.SetWordAt(th.ciDepthWordRef, uint64(uint32(n)))
		if ciMirrorCheck {
			// wangshu_trace safety net: read the word back to self-check the mirror is
			// consistent with Go ciDepth (Stage 1a), catching wrong-ref / encoding bugs;
			// after the Stage 1c flip to reading the word, this self-check guards against regression.
			if got := uint32(th.arena.WordAt(th.ciDepthWordRef)); got != uint32(n) {
				panic(fmt.Sprintf("crescent: ciDepth 字镜像不一致 got=%d want=%d", got, n))
			}
		}
	}
}

// setTop sets the stack top + mirrors it to the linear-memory word (PW10
// zero-cross ①). All writes of th.top are closed off through here (call / return /
// meta / host paths), keeping topWordRef (mainTh) in sync with Go th.top.
// ① only writes the shadow (GC reads liveTop, Wasm does not yet write the word);
// starting from ④ Wasm frame build / ③ caller self-restore of top, the Wasm side
// writes this word and the Go boundary reads it back adjacently via
// syncCurFromSeg. **Stores the slot index** (grow-safe).
func (th *thread) setTop(n int) {
	th.top = n
	if th.topWordRef != 0 {
		th.arena.SetWordAt(th.topWordRef, uint64(uint32(n)))
		if ciMirrorCheck {
			if got := uint32(th.arena.WordAt(th.topWordRef)); got != uint32(n) {
				panic(fmt.Sprintf("crescent: top 字镜像不一致 got=%d want=%d", got, n))
			}
		}
	}
}

// liveTop returns the stack top (slot index) the GC stack-root scan should use.
// Starting from Stage ④ the Wasm-side frame build/teardown writes the top word
// during Wasm execution (setting the frame top when building a callee frame /
// caller self-restore), all without going back to Go, so th.top (the Go field)
// lags the real value; a safepoint in a gibbous frame body triggering GC must scan
// the [0,top) stack roots by the **word** top + nil-clear [top,size), otherwise a
// Wasm-newly-pushed active slot is missed / wrongly cleared = UAF.
// mainTh reads the word (topWordRef≠0); coroutine th has no word mirror (does not
// lift), falling back to th.top. When ① lands, Wasm has not yet written the word,
// so liveTop always equals th.top and the flip is a zero behavior change.
func (th *thread) liveTop() int {
	if th.topWordRef != 0 {
		return int(uint32(th.arena.WordAt(th.topWordRef)))
	}
	return th.top
}

// truncateCI rewinds the frame depth to newDepth (pcall/metamethod/yield boundary
// cleanup, replacing the old th.cis = th.cis[:newDepth]). After the rewind it
// reloads the new top frame from the segment into th.cur. After VS0-e the vararg
// area lives in the lower stack region, with no separate ciVarargs cleanup (the
// truncated frames' stack region is covered by visitThreadValues's [top, size)
// nil-clear after setTop shrinks top). R2b-4 + VS0-e.
func (th *thread) truncateCI(newDepth int) {
	th.setCIDepth(newDepth)
	if newDepth > 0 {
		th.readCISegInto(newDepth-1, &th.cur)
	}
}

// reMirrorTop flushes the current top frame's hot mirror (th.cur) back to the ci
// segment (called after a cold field changes th.cur via currentCI, e.g.
// SetTailcall/SetGibbous, R2b-4).
func (th *thread) reMirrorTop() {
	if th.ciDepth > 0 {
		th.writeCISeg(th.ciDepth-1, &th.cur)
	}
}

// growCISeg reallocates a larger segment when the ci segment lacks capacity for
// need frames (mimicking growStack, PW10 R2b-2).
//
// Form Y: copies old frames via WordAt/SetWordAt computing the address on the fly
// (reading arena.words's current value, free of a cached derived slice, immune to
// the physical grow triggered by AllocWords). The ci segment addresses only via
// ciBaseW + depth computed on the fly (no derived pointer surviving across grow),
// and after relocation the next writeCISeg/readCISegInto automatically points to
// the new segment; the top frame's hot mirror is in th.cur (address-stable,
// unaffected by segment relocation).
func (th *thread) growCISeg(need int) {
	newCap := need + 8
	if d := th.ciCap * 2; d > newCap {
		newCap = d
	}
	a := th.arena
	newRef := a.AllocWords(uint32(newCap * ciWords))
	oldRef := arena.GCRef(th.ciBaseW) << 3
	// copy the existing active frames [0,ciDepth) (already mirrored); new frames are writeCISeg'd by the caller afterward.
	copyFrames := th.ciDepth
	if copyFrames > th.ciCap {
		copyFrames = th.ciCap
	}
	for w := 0; w < copyFrames*ciWords; w++ {
		a.SetWordAt(newRef+arena.GCRef(w*8), a.WordAt(oldRef+arena.GCRef(w*8)))
	}
	oldCap := th.ciCap
	th.ciBaseW = uint32(newRef) >> 3
	th.ciCap = newCap
	th.syncCISegBase() // PW10 Stage 2: update the Wasm-visible segment base word after segment relocation
	if !oldRef.IsNull() {
		a.Free(oldRef, uint32(oldCap*ciWords)*8)
	}
}

// ciSegCl reads cl (word3, GCRef) from the depth-th frame of the ci segment. The
// R2b-3 GC root scan reads each frame's closure root from the arena segment
// through here (rather than Go cis[depth].cl) — proving the segment is the correct
// GC root source (a missed root = UAF). Form Y: WordAt computes the address on the
// fly, free of caching.
func (th *thread) ciSegCl(depth int) arena.GCRef {
	ref := arena.GCRef(th.ciBaseW+uint32(depth*ciWords+3)) << 3
	return arena.GCRef(th.arena.WordAt(ref))
}

// (PW10 R2b-1 safety net, enabled only under ciMirrorCheck=wangshu_trace build).
// A pack/unpack bug panics here immediately, rather than the symptom being far
// from the root cause after the R2b-2 flip to reading the segment.
func (th *thread) verifyCISeg(depth int, want *callInfo) {
	var got callInfo
	th.readCISegInto(depth, &got)
	if got.base != want.base || got.funcIdx != want.funcIdx || got.top != want.top ||
		got.protoID != want.protoID || got.cl != want.cl || got.nresults != want.nresults ||
		got.tailcall != want.tailcall || got.fresh != want.fresh || got.gibbous != want.gibbous ||
		got.pc != want.pc || got.nVarargs != want.nVarargs {
		panic(fmt.Sprintf("crescent: ci 段镜像不一致 depth=%d\n got  %+v\n want %+v", depth, got, *want))
	}
}

// --- Value stack access closure (VS0 form Y) ---
//
// slot/setSlot are the single scalar read/write closure of a value stack slot;
// size/copyOut/copyIn/activeSlice cover capacity queries and bulk moves. Form Y:
// each time addresses via the current view of arena.Words() by offset, without
// caching a derived slice (immune to arena grow); after the VS0-a closure, the
// opcode and calling-convention code stay unchanged.

// slot reads the value at absolute stack position i (the stackBaseW+i-th word of the arena segment).
func (th *thread) slot(i int) value.Value {
	return value.Value(th.arena.Words()[th.stackBaseW+uint32(i)])
}

// setSlot writes absolute stack position i.
func (th *thread) setSlot(i int, v value.Value) {
	th.arena.Words()[th.stackBaseW+uint32(i)] = uint64(v)
}

// size returns the value stack segment capacity (= stackCap; for capacity boundary checks).
func (th *thread) size() int { return th.stackCap }

// copyOut copies slots [lo,hi) into the caller's Go slice dst (return value move).
func (th *thread) copyOut(dst []value.Value, lo, hi int) {
	w := th.arena.Words()
	for i := lo; i < hi; i++ {
		dst[i-lo] = value.Value(w[th.stackBaseW+uint32(i)])
	}
}

// copyIn copies src into slots [lo,...) (resume writes values).
func (th *thread) copyIn(lo int, src []value.Value) {
	w := th.arena.Words()
	for i, v := range src {
		w[th.stackBaseW+uint32(lo+i)] = uint64(v)
	}
}

// activeSlice returns the zero-copy active slice of [0,hi) (the callOnStack return
// value; see the callOnStack doc for the contract: consume it before next entering
// the VM — grow only happens on entering the VM, so this slice is valid within the
// consumption window). Under form Y it aliases the arena segment via unsafe (Value
// underlying = uint64).
func (th *thread) activeSlice(hi int) []value.Value {
	if hi == 0 {
		return nil
	}
	w := th.arena.Words()
	return unsafe.Slice((*value.Value)(unsafe.Pointer(&w[th.stackBaseW])), hi)
}

// push pushes a value onto the stack top (growStack if the segment is full).
func (th *thread) push(v value.Value) {
	if th.top >= th.stackCap {
		th.growStack(th.top + 1)
	}
	th.setSlot(th.top, v)
	th.setTop(th.top + 1)
}

// ensureStack ensures the segment capacity ≥ n (growStack if the segment is full).
func (th *thread) ensureStack(n int) {
	if n > th.stackCap {
		th.growStack(n)
	}
}

// growStack reallocates a larger segment inside the arena, copies old slots,
// changes stackBaseW, Free's the old segment (lua_realloc stack style).
//
// **The grow view trap**: AllocWords may trigger an arena physical grow64 that
// invalidates the old view; the copy computes the address on the fly via
// WordAt/SetWordAt (reading arena.words's current value, the offset unchanged after
// grow), without caching any derived slice (cashing in form Y immunity).
//
// **Automatic upvalue relocation**: open upvalues address via owner.slot(idx), and
// after stackBaseW is updated they automatically point to the same position in the
// new segment, with no need to change openUvs's stackIdx keys.
func (th *thread) growStack(need int) {
	newCap := need + 8
	if d := th.stackCap * 2; d > newCap {
		newCap = d
	}
	a := th.arena
	newRef := a.AllocWords(uint32(newCap))
	oldRef := arena.GCRef(th.stackBaseW) << 3
	for i := 0; i < th.top; i++ {
		a.SetWordAt(newRef+arena.GCRef(i*8), a.WordAt(oldRef+arena.GCRef(i*8)))
	}
	oldCap := th.stackCap
	th.stackBaseW = uint32(newRef) >> 3
	th.stackCap = newCap
	if !oldRef.IsNull() {
		a.Free(oldRef, uint32(oldCap)*8)
	}
}

// callInfo persists the state of each active Lua call (05 §1.2). M9-simplified fields.
//
// The pc field in M9 is "the position of the instruction currently executing"
// (the main loop reads/writes it directly, unlike the design doc's savedPC which
// is "the pc restored on return"). When coroutines hook up in M11, pc/top are
// aligned back to ci and the saveFrame abstraction (05 §1.3 reloadFrame/saveFrame symmetric convention).
//
// **proto referenced by protoID (VS0-b)**: a Go pointer *bytecode.Proto cannot
// enter linear memory (03-memory-model §5 Go-heap-side asset demarcation);
// instead store protoID (uint32), looked up via st.protos[id] when used. This
// aligns with the P3 trampoline's ci.protoID interface (04 §2.2).
//
// **gibbous flag bit** (p2-bridge/04 §4.4 word2 bit50 callStatus_gibbous):
// set to 1 when the gibbous frame entry trampoline pushes a new CallInfo,
// marking this frame as going through the Wasm path (04 §1.2). The P1 interpreter
// main loop does not read it (transparent to it, 04 §1.3); the trampoline reads
// it to decide the flow when cross-tier scheduling / error bubbling. The form-b
// simplified version (bool, same as tailcall/fresh; the word-bit packing is deferred to VS0-e).
type callInfo struct {
	base     int         // the absolute index of R0 in stack
	funcIdx  int         // the callee closure slot (funcIdx = base-1)
	top      int         // this frame's logical top
	protoID  uint32      // the current Proto's ID (st.protos[protoID]; VS0-b replaces *Proto)
	cl       arena.GCRef // the current closure
	nresults int         // the number of returns the caller expects; -1 = variable
	tailcall bool
	fresh    bool // execute re-entry boundary
	gibbous  bool // this frame executes in gibbous (Wasm) compiled code (04 §1.2; always false in P1)

	pc int32

	// nVarargs is this frame's vararg-area length (VS0-e substep ④: M14 hook
	// landed). The vararg area is in the lower stack region
	// stack[base-nVarargs..base); doVararg reads th.slot(base-nVarargs+k) directly;
	// the GC stack scan [0, top) naturally covers it (vararg < base < top). Mirrored in segment word4.
	nVarargs uint16
}

// protoOf takes the callInfo's Proto (VS0-b: protoID → *Proto closure).
func (st *State) protoOf(ci *callInfo) *bytecode.Proto { return st.protos[ci.protoID] }

// --- CallInfo field access closure (R2a, PW10 VS0-e prerequisite) ---
//
// All callInfo field reads/writes go through the accessors below, so that when
// R2b physically migrates ci into linear memory, only the method bodies + struct
// change, not the ~171 call sites (same discipline as the VS0-a value-stack addressing closure).
//
// **Hot/cold split (basis for the R2b physical layout)**: Base/SetBase and
// Pc/SetPc are hot registers (read/written every instruction via
// reg/setReg/main loop); R2b plans to keep them as the current frame's Go mirror,
// syncing with the arena ci segment only at push/pop/tier boundaries; the rest
// (cl/nresults/funcIdx/protoID/top/flags/nVarargs) are cold fields (touched only
// at call boundaries), which R2b reads/writes directly on the arena segment. This
// round the accessors are all pass-through (return/write the Go field), a zero behavior change.
func (ci *callInfo) Base() int            { return ci.base }
func (ci *callInfo) FuncIdx() int         { return ci.funcIdx }
func (ci *callInfo) Top() int             { return ci.top }
func (ci *callInfo) SetTop(v int)         { ci.top = v }
func (ci *callInfo) ProtoID() uint32      { return ci.protoID }
func (ci *callInfo) Cl() arena.GCRef      { return ci.cl }
func (ci *callInfo) NResults() int        { return ci.nresults }
func (ci *callInfo) Tailcall() bool       { return ci.tailcall }
func (ci *callInfo) SetTailcall(v bool)   { ci.tailcall = v }
func (ci *callInfo) Fresh() bool          { return ci.fresh }
func (ci *callInfo) Gibbous() bool        { return ci.gibbous }
func (ci *callInfo) SetGibbous(v bool)    { ci.gibbous = v }
func (ci *callInfo) Pc() int32            { return ci.pc }
func (ci *callInfo) SetPc(v int32)        { ci.pc = v }
func (ci *callInfo) NVarargs() uint16     { return ci.nVarargs }
func (ci *callInfo) SetNVarargs(v uint16) { ci.nVarargs = v }
