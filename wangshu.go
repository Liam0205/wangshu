// Package wangshu is the public API facade for the Wangshu Lua VM.
//
// Design: docs/design/p1-interpreter/11-embedding-arena-abi.md §1.
// P1/M13 scope: implement the minimal subset of Compile / Program / State / Value;
// the arena ABI column-data interface and the lightuserdata handle table are
// deferred to later in P1 (done at M14 / P2 when the columnar-kernel host is wired in).
//
// Usage example:
//
//	prog, err := wangshu.Compile([]byte("return 1+2"), "snippet")
//	if err != nil { ... }
//	st := wangshu.NewState(wangshu.Options{})
//	results, err := prog.Run(st)
//	// results[0].Number() == 3
package wangshu

import (
	"context"
	"fmt"

	"github.com/Liam0205/wangshu/internal/arena"
	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/crescent"
	"github.com/Liam0205/wangshu/internal/frontend/compile"
	"github.com/Liam0205/wangshu/internal/frontend/lex"
	"github.com/Liam0205/wangshu/internal/frontend/parse"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/stdlib"
	"github.com/Liam0205/wangshu/internal/value"
)

// Options configures a State (11 §1.2).
//
// Fields implemented in P1: GCPause (passed to the collector), AllowFileLoad
// (gate on loadfile/dofile reading the host filesystem, off by default),
// HideFileLoaders (strip the loader quartet out of globals, matching gopher-lua).
// The remaining fields keep the interface shape and get wired in at later milestones.
type Options struct {
	InitialArenaBytes uint32
	MaxArenaBytes     uint32
	MaxCallDepth      int
	MaxCCalls         int
	GCPause           int
	// AllowFileLoad enables loadfile/dofile to read the host filesystem.
	// Default false: when an embedded VM runs untrusted scripts, file reads
	// are an out-of-bounds probing surface.
	AllowFileLoad bool
	// HideFileLoaders is strict sandbox mode: after NewState loads stdlib, it
	// strips the loadfile / dofile / loadstring / load quartet out of the
	// globals table (setting them to Nil). A script calling them gets an
	// "attempt to call a nil value (global 'X')" fatal error, matching the
	// gopher-lua embedded-sandbox tradition (issue #3) — there is no graceful
	// (nil, errmsg) degradation.
	//
	// **Field name vs. actual strip scope**: the name says "File" but the
	// quartet stripped includes loadstring and load — these two do not read
	// files, but as **same-origin dynamic code-loading risk surface** (compiling
	// / loading code at runtime, an out-of-sandbox capability of equivalent
	// weight), stripping them alongside loadfile/dofile is what covers the full
	// "load arbitrary code at runtime" attack surface. To forbid only file
	// reads while keeping loadstring/load, use `AllowFileLoad=false` (the
	// default) rather than this field.
	//
	// Default false: preserves PUC Lua 5.1.5-matching behavior (with
	// AllowFileLoad=false, loadfile returns (nil, errmsg)), so the official
	// 5.1.5 oracle differential test does not regress.
	//
	// Setting this true together with AllowFileLoad=true is self-contradictory
	// (allow file reads yet strip the entry functions); NewState detects it and
	// panics fail-fast.
	HideFileLoaders bool
}

// State is a VM instance (11 §1.2). It holds globals/registry/arena/host
// registry/handle table/string intern/GC collector. State carries mutable
// state, **one per goroutine**.
type State struct {
	core *crescent.State
	// loaded caches "Programs already loaded on this State" (11 §1.3: string
	// constants are lazily interned on the first Call and the same closure is
	// reused thereafter; calling LoadProgram again on every Run would re-copy
	// the Proto and re-allocate ICs).
	loaded map[*Program]loadedProg
	// mounted caches "host Arenas already mounted" → column count at mount time:
	// repeated Calls with the same *Arena do not re-run RegisterHostFn / rebuild
	// proxy tables (the columnar-kernel workload is "one Call per batch",
	// otherwise each batch leaks 2×columns closures + tables); a change in
	// column count (AddColumn after mount) triggers a remount.
	mounted map[*Arena]int
	// innerArgsBuf is CallInto's reusable argument buffer (the zero-allocation
	// boundary path, issue #8): each CallInto resets it to [:0] then fills it,
	// avoiding a make([]value.Value, len(args)) per call. State is
	// single-goroutine and CallInto is non-reentrant, so reuse is safe.
	innerArgsBuf []value.Value
}

type loadedProg struct {
	cl arena.GCRef
}

// NewState creates a fresh VM with the P1 minimal stdlib loaded.
func NewState(opts Options) *State {
	if opts.AllowFileLoad && opts.HideFileLoaders {
		// Semantically self-contradictory: allow file reads yet strip the entry
		// functions — this combination is always a misconfiguration.
		panic("wangshu: NewState: AllowFileLoad and HideFileLoaders are mutually exclusive")
	}
	st := &State{core: crescent.NewWithOptions(arena.Options{
		InitialBytes: opts.InitialArenaBytes,
		MaxBytes:     opts.MaxArenaBytes,
	})}
	st.core.SetAllowFileLoad(opts.AllowFileLoad)
	// loadstring's compile callback (injected via the facade to avoid a
	// crescent → frontend reverse dependency)
	st.core.SetCompileFn(func(src []byte, chunkname string) (uint32, []*bytecode.Proto, error) {
		lx := lex.New(src, chunkname)
		block, err := parse.Parse(lx, chunkname)
		if err != nil {
			return 0, nil, err
		}
		return compile.Compile(block, chunkname)
	})
	stdlib.OpenAll(st.core)
	if opts.HideFileLoaders {
		// gopher-lua match: strip the loader quartet out of globals (set to
		// Nil), so a script call → "attempt to call a nil value". loadstring is
		// stripped as same-origin risk surface (dynamic compilation is also a
		// common embedded-sandbox concern).
		st.core.SetGlobal("loadfile", value.Nil)
		st.core.SetGlobal("dofile", value.Nil)
		st.core.SetGlobal("loadstring", value.Nil)
		st.core.SetGlobal("load", value.Nil)
	}
	return st
}

// SetGCStressMode toggles high-frequency GC stress mode (forces a full Collect
// at every safepoint). Used by GC-transparency tests: under stress mode the
// output must be byte-equal to normal mode (12 §5).
func (st *State) SetGCStressMode(on bool) { st.core.SetGCStressMode(on) }

// SetForceAllPromote toggles force-promote-all mode (the P3 cross-tier
// differential-test entry point, p3-testing-strategy 08 §2.2). Once set, every
// compilable Proto is promoted to gibbous on its first execution (bypassing the
// heat threshold, but **not bypassing the compilability gate**) — making
// crescent vs gibbous cross-tier diffs reproducible and maximizing coverage.
//
// **testing-only**: exists only to remove the timing nondeterminism of "which
// Protos are hot enough" for cross-tier differential testing; it is not a
// supported production run mode. On non-wangshu_p3 builds / when P3 is not
// injected, compilability gate F7 permanently judges nothing compilable → all
// stay in crescent, and this switch is a no-op.
func (st *State) SetForceAllPromote(on bool) { st.core.SetForceAllPromote(on) }

// SetTierEnabled is the runtime master switch for tiered execution
// (**production admin API**, on by default).
//
// After disabling (enabled=false):
//   - no new promotions occur (entry/back-edge sampling short-circuits directly,
//     and heat stops accumulating);
//   - **already-promoted Protos also fall back to interpreter execution** —
//     effective from the next dispatch decision (a call already executing inside
//     a native/wasm segment runs to completion normally);
//   - compiled artifacts stay in the cache; re-enabling (enabled=true) restores
//     tiered execution without recompilation.
//
// Typical use: a "one-click fall back to interpreter" lever during production
// canary of P3/P4 — when a promotion path is suspected in production, downgrade
// without rebuilding the State / restarting the process, and use TierStats to
// observe and localize. On non-wangshu_p3 / wangshu_p4 builds the interpreter is
// the only execution tier, and this switch is a no-op.
func (st *State) SetTierEnabled(enabled bool) { st.core.SetTierEnabled(enabled) }

// TierEnabled returns the current state of the tiered-execution runtime switch
// (see SetTierEnabled). On non-P3/P4 builds it always returns true (the
// interpreter is the only execution tier, nothing to turn off).
func (st *State) TierEnabled() bool { return st.core.TierEnabled() }

// TierStats is a State-level tiered-execution observability snapshot (production
// admin API, paired with the SetTierEnabled switch). Each field is counted from
// this State's own profile table / promotion cache.
type TierStats struct {
	// Promoted: number of Protos already promoted (loaded with P3 wasm / P4
	// native compilation artifacts).
	Promoted int
	// StuckNotCompilable: number of Protos permanently kept in the interpreter
	// because the compilability check excludes their shape (vararg / coroutine /
	// unsupported opcode etc.). Expected, no attention needed.
	StuckNotCompilable int
	// StuckDeclined: number of Protos the backend could compile but declined to
	// promote as not worthwhile (profitability gate). Also expected.
	StuckDeclined int
	// StuckCompileFailed: number of Protos that entered real compilation but
	// failed (backend error / panic). **A nonzero value is worth investigating**
	// — unlike the two expected Stuck categories above.
	StuckCompileFailed int
	// Profiled: number of Protos with any profile data (having passed at least
	// one sampling hook point).
	Profiled int
	// TierEnabled: current state of the runtime switch (mirrors
	// State.TierEnabled()).
	TierEnabled bool
}

// TierStatsSnapshot returns a snapshot of the current State's tiered-execution
// distribution.
//
// Typical use: localizing a performance anomaly during production canary of
// P3/P4 — whether Promoted matches expectations, whether StuckCompileFailed is
// nonzero; paired with SetTierEnabled for a "turn it off and look again"
// controlled experiment. A diagnostic path, very cheap (a few counter reads),
// but polling it per-frame is not recommended. On non-P3/P4 builds it returns a
// zero distribution + TierEnabled=true.
func (st *State) TierStatsSnapshot() TierStats {
	s := st.core.TierStatsSnapshot()
	return TierStats{
		Promoted:           s.Promoted,
		StuckNotCompilable: s.StuckNotCompilable,
		StuckDeclined:      s.StuckDeclined,
		StuckCompileFailed: s.StuckCompileFailed,
		Profiled:           s.Profiled,
		TierEnabled:        s.TierEnabled,
	}
}

// SetHotThresholds overrides the natural-heat promotion thresholds
// (entry maps to HotEntryThreshold, backEdge to HotBackEdgeThreshold;
// 0 keeps that threshold unchanged).
//
// **testing-only**: the auto-mode coverage entry point — the production
// thresholds (200/1000) are unreachable for a single short script, so
// tests lower them to drive the auto decision chain (runtime
// compilability recheck / profitability gate / short-proto floor and
// its exemption) with small cases. Changes only WHEN the promotion
// decision runs, never WHETHER/HOW it decides. No-op on non-P3/P4
// builds (same boundary as SetForceAllPromote).
func (st *State) SetHotThresholds(entry, backEdge uint32) {
	st.core.SetHotThresholds(entry, backEdge)
}

// PromotionCount returns the number of Protos already promoted (crescent →
// gibbous) on the current State (**testing-only**).
//
// Use: for benchmarks / e2e tests to white-box assert "promotion really
// happened" under auto-lifting — if HotEntryThreshold never fires, the numbers a
// p3 build measures are interpreter-path numbers, nearly indistinguishable from
// p1 and unreadable (see the prove-the-path-under-test guide). Taking this value
// as 0-before / >0-after proves promotion occurred; under p3 force-all it
// usually equals the number of compilable Protos after the run.
//
// Shape: non-decreasing (promotion only grows, monotonically increasing over
// the State's lifetime); on non-wangshu_p3 builds / when P3 is not injected it
// always returns 0 (equivalent to a no-op).
func (st *State) PromotionCount() int { return st.core.PromotionCount() }

// SafepointCalls returns the cumulative count of gibbous back-edges crossing
// tiers into host.Safepoint (a white-box probe, for tests: to prove that a hot
// loop with no budget barely crosses tiers).
func (st *State) SafepointCalls() int64 { return st.core.SafepointCalls() }

// GCCountKB returns the arena's currently-used KB (= the bump pointer; includes
// free blocks on the freelist awaiting reuse). For long-run stability
// observation: in steady state the freelist recycles blocks and this value
// should be bounded.
func (st *State) GCCountKB() float64 { return st.core.GCCountKB() }

// ArenaCapKB returns the arena backing's current **capacity** in KB (issue #11
// direction 3).
//
// Difference from GCCountKB: GCCountKB measures "live bytes" and falls back with
// Collect; ArenaCapKB measures "backing slab capacity", reflecting the real Go
// heap residency — in grow-only mode it increases monotonically and is not
// shrunk by Collect (arena copy-compact pending issue #11 direction 1).
//
// Typical use: a long-running State pool layer uses this to judge a fat-state
// threshold, dropping the state rather than caching it when exceeded. More
// accurate than GCCountKB — the latter hides the latched high-water behind
// sweep.
func (st *State) ArenaCapKB() float64 { return st.core.ArenaCapKB() }

// Collect forces one full GC sweep (corresponds to Lua collectgarbage("collect")).
//
// issue #9 direction 2: the host embedding layer explicitly drives the GC
// cadence, avoiding the roundabout path of a collectgarbage script call.
// **Typical scenario**: under a boundary-dominated workload (the host
// repeatedly builds large tables + short scripts), the VM opcode safepoint does
// not fire often enough for GC to keep up with the host-driven allocation
// cadence → the arena's internal accounting grows monotonically. The host
// calling this method periodically at pool-return / batch-completion points
// keeps GCCountKB bounded. **Cost**: ~microseconds to milliseconds each
// (depending on live-object scale). Does not shrink backing capacity (see
// ArenaCapKB / issue #11 direction 1).
func (st *State) Collect() { st.core.Collect() }

// MaybeCollectNow triggers a collect conditionally on the GC threshold (may
// collect or may be a no-op). Equivalent to letting the host trigger one
// safepoint check.
//
// issue #9 direction 2's minimal safe surface: suited to the shape of "wanting
// to periodically let GC manage itself but the VM opcode safepoint doesn't fire"
// (short scripts + high-frequency host-driven allocation). Cheaper than
// Collect() (only really collects when the threshold is hit), but does not
// guarantee a sweep when it doesn't fire. Shapes with a hard requirement for a
// sweep should call Collect directly.
func (st *State) MaybeCollectNow() { st.core.MaybeCollectNow() }

// SetHostTriggeredCollect toggles host-alloc crossing the threshold triggering
// GC directly (issue #9 direction 1, **experimental opt-in**).
//
// **Off by default** — once enabled, any alloc (NewTable/SetIndex/rehash/
// intern/stdlib alloc...) that crosses the GC threshold sweeps immediately.
// **Safety contract**: the caller guarantees all transient GCRefs are reachable
// from a GC root (pin table / shadow stack / already on a sweep chain).
//
// **Not recommended for production before mid-construction pin safety is
// audited**: the current stdlib (string.gsub etc.) + string intern's transient
// GCRefs are not all explicitly registered via pin/shadow stack, so enabling it
// introduces UAF risk. Known breaks: luasuite gc.lua / literals.lua /
// nextvar.lua / pm.lua / strings.lua etc. (measured 2026-06-16).
//
// **Recommended alternative**: use Collect() / MaybeCollectNow() for explicit
// cadence control (issue #9 direction 2, production-safe). The host embedding
// layer just calls it periodically at pool-return / batch-completion points.
// This method should only be considered for enabling "once the future
// mid-construction pin audit is complete + the caller is sufficiently confident
// about transient safety", as a convenience path with zero extra
// cadence-control code.
func (st *State) SetHostTriggeredCollect(on bool) { st.core.SetHostTriggeredCollect(on) }

// SetStepBudget sets the back-edge instruction budget (<=0 disables): on
// overrun the script terminates with the recoverable error "instruction budget
// exceeded". A host's execution quota for untrusted scripts.
func (st *State) SetStepBudget(n int64) { st.core.SetStepBudget(n) }

// MarkGlobalsBaseline snapshots the current _G as a baseline (issue #6, the
// gopher-lua-matching script-isolation mechanism when reusing a State via
// sync.Pool).
//
// Typical timing: call once right after `NewState` loads stdlib, fixing the
// stdlib-provided surface as the baseline. Thereafter `ResetGlobalsToBaseline`
// can reset _G between each Borrow / Return — both script hijack
// (`tostring = "pwned"`) and leak (`new_global = 123`) are covered, and the next
// Borrow sees a _G as clean as right after stdlib load.
//
// Repeated calls overwrite the old baseline (no side effect, but the composite
// values rooted by the previous baseline may be GC'd after their root is
// released).
//
// Restriction: snapshots string keys only — stdlib and the host's own globals
// are all string keys; number/table/function etc. keys are skipped (atypical,
// nonexistent in practice). Composite values in the baseline (table/function
// GCRef) enter the GC roots via visitExtraValues, so what Reset writes back to
// _G is not a dangling ref.
func (st *State) MarkGlobalsBaseline() { st.core.MarkGlobalsBaseline() }

// ResetGlobalsToBaseline restores _G to the state snapshotted by the last
// `MarkGlobalsBaseline` (issue #6):
//
//   - non-baseline string keys are deleted (writing Nil, equivalent to Lua table
//     semantics `_G[k] = nil`)
//   - baseline string keys are written back to their value at baseline time
//
// When `MarkGlobalsBaseline` was never called (empty baseline), this is
// equivalent to "clear all string-key globals" — use with care, it clears stdlib
// too.
//
// The pineapple sync.Pool usage mirrors the gopher-lua statePool.Return path:
//
//	st := wangshu.NewState(opts)
//	st.MarkGlobalsBaseline()           // set the baseline right after stdlib load
//	// repeated Borrow / Return cycles:
//	for i := 0; i < N; i++ {
//	    prog.Run(st)                   // script may hijack or leak
//	    st.ResetGlobalsToBaseline()    // next Borrow sees a clean _G
//	}
//
// Performance tier: each Reset walks all string keys of the current _G twice
// (collect + delete-non-baseline + write-baseline), O(N) for N=number of
// globals. Typical N ~ 100-200 (stdlib + host helpers), microseconds per Reset —
// the cost is amortizable over the sync.Pool Borrow/Return boundary. Never call
// it on the script hot path.
func (st *State) ResetGlobalsToBaseline() { st.core.ResetGlobalsToBaseline() }

// SetContext binds a context.Context to the State — at every preemption point
// (back-edge / function frame entry / TFORLOOP etc.) the VM checks ctx.Err();
// when non-nil it aborts the current Run/Call and returns a Go error wrapping
// ctx.Err() (catchable by Lua-side pcall, with err.Error() containing
// "context canceled: <original ctx text>"). Matches gopher-lua `L.SetContext`
// (issue #4).
//
// Shape: precise to a wall-clock interrupt (both `ctx.WithTimeout` and upstream
// `Cancel` take effect); coexists with SetStepBudget, whichever fires first
// terminates — the latter counts instructions, the former is event-driven.
//
// Cross-goroutine: the typical context-cancel usage is goroutine A running the
// VM and goroutine B calling `cancel()` — internally wrapped with
// atomic.Pointer, cross-goroutine safe. The VM State itself remains
// single-goroutine.
//
// Return-to-pool: before reusing a State (if doing State pooling) call
// RemoveContext to clear it, otherwise the next Borrow reuses a stale ctx.
//
// Performance: the cost is at the same preemption point as chargeStep, adding
// one atomic.Pointer.Load + nil check; when no ctx is injected it's a fast-path
// nil compare, near-zero cost (no impact observed in the performance-round
// benchmarks).
func (st *State) SetContext(ctx context.Context) {
	if ctx == nil {
		st.core.SetCancelHook(nil)
		return
	}
	st.core.SetCancelHook(ctx.Err)
}

// RemoveContext clears the Context currently bound to the State (pairs with
// SetContext). Repeated calls, or Remove without a prior SetContext, have no
// side effect.
func (st *State) RemoveContext() { st.core.SetCancelHook(nil) }

// Program is an immutable compilation product (11 §1.4). Shareable across
// goroutines; string constants are lazily interned into a State's arena the
// first time that State Runs it.
type Program struct {
	mainID uint32
	protos []*bytecode.Proto
}

// Compile turns Lua 5.1 source into a Program (11 §1.3).
//
// lexer → parser → codegen; compile errors are returned as Go errors.
func Compile(source []byte, chunkname string) (*Program, error) {
	lx := lex.New(source, chunkname)
	block, err := parse.Parse(lx, chunkname)
	if err != nil {
		return nil, err
	}
	mainID, protos, err := compile.Compile(block, chunkname)
	if err != nil {
		return nil, err
	}
	return &Program{mainID: mainID, protos: protos}, nil
}

// Run executes prog's main chunk on state, optionally passing arguments (args is
// a Value slice).
//
// Returns all of the main chunk's return values. Lua runtime errors are
// converted to a Go error. Repeated Runs of the same Program on the same State
// reuse the closure loaded on the first Run (lazy interning happens only once,
// and ICs stay in effect across Runs).
//
// Return-value lifetime: when the script returns a table / function, the
// returned Value is registered as a GC root via the State pin table — the caller
// should pair it with v.Release(), otherwise pin slots accumulate under a
// long-lived State. **v0.1.1 → v0.1.2 behavior change**: previously table /
// function return values were silently mapped to Nil; now they can be read on
// the Go side (table via v.AsTable(), function via state.Call(v, ...)). Hosts
// consuming only scalars (nil/bool/number/string) need no change.
func (prog *Program) Run(state *State, args ...Value) ([]Value, error) {
	return prog.call(state, nil, args)
}

// Call executes prog on state and exposes the host columnar-data arena to the
// script (11 §1.5).
//
// The arena is injected under the global name `arena`: `arena.<col>[i]` reads
// row i (1-based, Lua convention), zero-copy boxed on the fly; a null row reads
// out as nil; `arena.rows` is the row count. Columns are read-only (11 §5.3). A
// nil arena is equivalent to Run.
//
// Return-value lifetime as in Run: for composite values (table/function) the
// caller should pair with Release().
func (prog *Program) Call(state *State, arena *Arena, args ...Value) ([]Value, error) {
	return prog.call(state, arena, args)
}

func (prog *Program) call(state *State, ar *Arena, args []Value) (results []Value, err error) {
	// Defense in depth: a Go panic from an internal VM defect is caught here and
	// converted to an error — an embedded VM's "the host process must not crash"
	// is a bottom-line promise, so even a future compiler/interpreter bug does
	// not bring the host down.
	defer func() {
		if r := recover(); r != nil {
			results, err = nil, fmt.Errorf("wangshu: internal VM panic: %v", r)
		}
	}()
	lp, ok := state.loaded[prog]
	if !ok {
		cl := state.core.LoadProgram(prog.mainID, prog.protos)
		lp = loadedProg{cl: cl}
		if state.loaded == nil {
			state.loaded = map[*Program]loadedProg{}
		}
		state.loaded[prog] = lp
	}
	if ar != nil {
		// A given Arena is mounted only once: the mount artifacts (proxy tables +
		// HostFn) are held by the `arena` global, so remounting means re-register
		// leaks. When the Arena data is updated in place (the host reuses the same
		// *Arena to fill the next batch), the proxy closures already read the new
		// data, so no remount is needed; an AddColumn after mount (column count
		// changes) does trigger a remount to expose the new columns.
		if state.mounted == nil {
			state.mounted = map[*Arena]int{}
		}
		if n, ok := state.mounted[ar]; !ok || n != len(ar.cols) {
			state.mountArena(ar)
			state.mounted[ar] = len(ar.cols)
		}
	}
	innerArgs := make([]value.Value, len(args))
	for i, a := range args {
		innerArgs[i] = a.toInner(state)
	}
	inner, err := state.core.Call(lp.cl, innerArgs, -1)
	if err != nil {
		return nil, err
	}
	out := make([]Value, len(inner))
	for i, v := range inner {
		out[i] = fromInnerWithPin(state, v)
	}
	return out, nil
}

// SetGlobal binds a value to key name in the globals table (mirrors gopher-lua
// `L.SetGlobal`).
//
// Shape: the "write global by name" item of the per-item stack-style API (11
// §7.1 / §9.1). Together with GetGlobal/Call it completes the minimal
// call loop in gopher-lua drop-in form.
//
// Performance tier: gopher-lua-style per-item boundary crossing, in the tier
// dominated by boundary cost (design-premises premise one). Usable for low-
// frequency / prototype / migration periods; for the high-frequency hot path use
// the arena column track instead.
func (st *State) SetGlobal(name string, v Value) {
	st.core.SetGlobal(name, v.toInner(st))
}

// GetGlobal reads key name from the globals table (mirrors gopher-lua
// `L.GetGlobal`).
//
// A missing key returns Nil. If the value read is a function, its underlying
// reference is registered as a GC root via the State pin table — before the
// Value is discarded, pair with v.Release() to explicitly free the slot
// (optional; not releasing only accumulates a small amount of memory when a
// long-lived State repeatedly GetGlobals different-named fns). Since v0.1.2
// tables are exposed via v.AsTable() (issue #2); userdata is still not exposed
// and maps to Nil.
//
// Performance tier: same as SetGlobal — per-item boundary crossing
// (design-premises premise one). Usable for low-frequency / prototype /
// migration periods; for the high-frequency hot path use the arena column track
// instead ([[embedding-contract]] arena ABI section).
func (st *State) GetGlobal(name string) Value {
	v := st.core.GetGlobal(name)
	return fromInnerWithPin(st, v)
}

// GlobalsSlot is a pre-resolved globals-key handle (issue #13 part B).
//
// When an item-mode embedder writes M ItemInput fields per record, each
// SetGlobal(name, v) call does a `gc.Intern([]byte(name))` — the `[]byte(name)`
// is an extra Go-side allocation plus an intern-table hash lookup. For fixed
// field names (typical: bound at `LuaOp.Init` and unchanged over the whole LuaOp
// lifetime), this cost can be amortized once at Init time:
//
//	slot := st.GlobalsSlot("item_price")  // resolve once at Init, holds one pin slot
//	for _, item := range items {
//	    st.SetBySlot(slot, wangshu.Number(item.price))  // skip intern in the hot loop
//	    st.Call(fn, ...)
//	}
//
// Internally holds a pin-table index, keeping the interned name-string GCRef
// alive; not calling Release does not affect correctness (StateGC reclaiming the
// State reclaims the whole pin table at once), but a long-lived State that
// repeatedly creates many slots of distinct names should pair with Release.
//
// Eliminates only the host-side intern hash cost; does not touch the globals
// rawtable's own lookup cost (that is per-key irreducible). The script-side
// `GETGLOBAL name` already goes fast via IC and is not in this mechanism's
// scope. Misusing SetBySlot/GetBySlot across States panics fail-fast.
type GlobalsSlot struct {
	st     *State
	pinIdx uint32
}

// GlobalsSlot pre-resolves a globals key name, returning a reusable slot handle
// (issue #13 part B, the string-key dual of SetGlobal/GetGlobal).
//
// Internally interns name and then pins the reference, so that SetBySlot/
// GetBySlot in the hot loop can skip the `[]byte(name)` allocation and the
// intern-table lookup. An empty name is legal, equivalent to SetGlobal("", v),
// i.e. the globals[""] slot.
//
// Performance tier: resolve once at Init time, fixing one pin slot; the cost of
// calling SetBySlot in the subsequent hot loop = `tableSet(globals, key, v)`,
// saving one alloc + one intern hash lookup compared to the per-Set
// string-intern amortized path.
func (st *State) GlobalsSlot(name string) GlobalsSlot {
	ref := st.core.InternForEmbed([]byte(name))
	pinIdx := st.core.PinRef(ref)
	return GlobalsSlot{st: st, pinIdx: pinIdx}
}

// SetBySlot writes globals using a pre-resolved slot as key (dual of SetGlobal).
//
// Behaviorally equivalent to SetGlobal("name", v), differing only in "the key
// need not be interned each time".
//
// Same-State check: the slot must be produced by this State's GlobalsSlot();
// a cross-State call panics — the same fail-fast style as State.Call for
// cross-State function arguments.
func (st *State) SetBySlot(s GlobalsSlot, v Value) {
	if s.st != st {
		panic("wangshu: SetBySlot: slot belongs to a different State")
	}
	ref := st.core.PinnedRefAt(s.pinIdx)
	if ref.IsNull() {
		panic("wangshu: SetBySlot: slot has been released")
	}
	st.core.SetGlobalByRef(ref, v.toInner(st))
}

// GetBySlot reads globals using a pre-resolved slot as key (dual of GetGlobal).
//
// A missing key returns Nil; function/table returns a Value registered as a GC
// root via the pin table (needs Release) — same semantics as GetGlobal.
//
// Same-State check as SetBySlot.
func (st *State) GetBySlot(s GlobalsSlot) Value {
	if s.st != st {
		panic("wangshu: GetBySlot: slot belongs to a different State")
	}
	ref := st.core.PinnedRefAt(s.pinIdx)
	if ref.IsNull() {
		panic("wangshu: GetBySlot: slot has been released")
	}
	v := st.core.GetGlobalByRef(ref)
	return fromInnerWithPin(st, v)
}

// Release frees the pin slot held by the slot. A slot not Released under a
// long-lived State is still correct, but repeatedly creating many slots of
// distinct names should pair with it — the same pin-hygiene discipline as
// Value.Release. Using SetBySlot/GetBySlot after release triggers
// panic("slot has been released"). Repeated Release is safe (the underlying
// UnpinRef is tolerant).
func (s *GlobalsSlot) Release() {
	if s.st == nil {
		return
	}
	s.st.core.UnpinRef(s.pinIdx)
	s.st = nil
}

// Call invokes a function Value on state (mirrors gopher-lua
// `L.CallByParam(P{Fn: fn, NRet: -1, Protect: true}, args...)`).
//
// Typical usage (mirroring the pineapple transform_by_lua shape):
//
//	prog, _ := wangshu.Compile([]byte(`function f(x) return x*2 end`), "rules")
//	prog.Run(st)                                // top-level definition puts f into globals
//	fn := st.GetGlobal("f")
//	defer fn.Release()
//	for _, x := range items {
//	    r, _ := st.Call(fn, wangshu.Number(x))
//	    use(r[0])
//	}
//
// Shape (11 §7.1 / §9.1): per-item boundary crossing, in the tier dominated by
// boundary cost (design-premises premise one). Use it for low-frequency /
// prototype / migration periods; for the high-frequency hot path use the arena
// column track instead.
//
// Boundary cost (issue #8): each Call round-trip is a fixed 2 allocs (VM stack →
// inner slice → public slice double copy), independent of the number of return
// values / script complexity. A boundary-dominated embedding (per-item short
// calls) is dominated by this floor cost. When a zero-allocation hot path is
// needed, use CallInto instead — it reuses the caller's dst, and scalar returns
// (bool/number) are 0-alloc end to end.
//
// Constraints:
//   - fn must be IsFunction() and come from the same State (taken via
//     GetGlobal); cross-State errors out
//   - only Lua functions are supported (defined in the script as `function f() end`).
//     A host closure registered via Register cannot yet be Called from the Go side
//     (only callable from within Lua).
//
// Returns: all values RETURNed by the callee; a runtime error is converted to a
// Go error (with traceback). A Go panic is caught and converted to an error
// (defense in depth, same as Program.Call).
//
// Return-value lifetime as in Program.Run/Call: composite values (table/function)
// are registered via the pin table, and the caller should pair with Release().
func (st *State) Call(fn Value, args ...Value) (results []Value, err error) {
	if !fn.IsFunction() {
		return nil, fmt.Errorf("wangshu: Call: value is not a function (kind=%s)", fn.Display())
	}
	if fn.fnState != st {
		return nil, fmt.Errorf("wangshu: Call: function belongs to a different State")
	}
	defer func() {
		if r := recover(); r != nil {
			results, err = nil, fmt.Errorf("wangshu: internal VM panic: %v", r)
		}
	}()
	ref := st.core.PinnedRefAt(fn.pinIdx)
	if ref.IsNull() {
		return nil, fmt.Errorf("wangshu: Call: function has been released")
	}
	innerArgs := make([]value.Value, len(args))
	for i, a := range args {
		innerArgs[i] = a.toInner(st)
	}
	inner, callErr := st.core.Call(ref, innerArgs, -1)
	if callErr != nil {
		return nil, callErr
	}
	out := make([]Value, len(inner))
	for i, v := range inner {
		out[i] = fromInnerWithPin(st, v)
	}
	return out, nil
}

// CallInto is the zero-allocation variant of Call: return values are written
// into the caller-owned dst, and the number written n is returned (dst[:n] is
// valid). Return values beyond dst's capacity are discarded (only len(dst) are
// written).
//
// Boundary-cost optimization (issue #8): each Call round-trip is a fixed 2
// allocs (VM stack → inner slice → public slice double copy), and a
// boundary-dominated embedding (per-item short calls) is dominated by this floor
// cost. CallInto lets the caller reuse dst (e.g. pineapple's [1]Value), and
// scalar returns (bool/number) are 0-alloc end to end.
//
// ⚠️ string return values still copy the arena bytes into the dst element (the
// public Value holds an independent []byte); composite values (table/function)
// are still registered via the pin table (need Release). Scalars are the pure
// zero-allocation path.
func (st *State) CallInto(dst []Value, fn Value, args ...Value) (n int, err error) {
	if !fn.IsFunction() {
		return 0, fmt.Errorf("wangshu: CallInto: value is not a function (kind=%s)", fn.Display())
	}
	if fn.fnState != st {
		return 0, fmt.Errorf("wangshu: CallInto: function belongs to a different State")
	}
	defer func() {
		if r := recover(); r != nil {
			n, err = 0, fmt.Errorf("wangshu: internal VM panic: %v", r)
		}
	}()
	ref := st.core.PinnedRefAt(fn.pinIdx)
	if ref.IsNull() {
		return 0, fmt.Errorf("wangshu: CallInto: function has been released")
	}
	st.innerArgsBuf = st.innerArgsBuf[:0]
	for _, a := range args {
		st.innerArgsBuf = append(st.innerArgsBuf, a.toInner(st))
	}
	inner, callErr := st.core.CallOnStack(ref, st.innerArgsBuf, -1)
	if callErr != nil {
		return 0, callErr
	}
	n = len(inner)
	if n > len(dst) {
		n = len(dst)
	}
	for i := 0; i < n; i++ {
		dst[i] = fromInnerWithPin(st, inner[i])
	}
	return n, nil
}

// mountArena maps the host Arena into a VM-readable view (11 §5.1-§5.3).
//
// P1 shape: arena = Lua table { rows = n, <col> = column proxy }; a column proxy
// = an empty table + metatable{__index = ReadCell closure, __newindex =
// read-only error}. A whole column is never copied; proxy[i] NaN-boxes on the
// fly on each read (11 §4.1 zero-copy read).
func (st *State) mountArena(ar *Arena) {
	core := st.core
	arenaTbl := core.NewLibTable(uint32(len(ar.cols) + 1))
	core.SetTableField(arenaTbl, "rows", value.NumberValue(float64(ar.nrows)))
	for ci := range ar.cols {
		col := &ar.cols[ci]
		proxy := core.NewLibTable(0)
		meta := core.NewLibTable(2)
		colRef := col // closure captures the column pointer
		nrows := ar.nrows
		strBytes := ar.strBytes
		readCell := func(ist *crescent.State, cargs []value.Value) ([]value.Value, *crescent.LuaError) {
			// __index(proxy, i)
			if len(cargs) < 2 || !value.IsNumber(cargs[1]) {
				return []value.Value{value.Nil}, nil
			}
			i := int64(value.AsNumber(cargs[1]))
			if i < 1 || uint32(i) > nrows {
				return []value.Value{value.Nil}, nil
			}
			row := uint32(i - 1)
			if !colRef.present(row) {
				return []value.Value{value.Nil}, nil
			}
			switch colRef.tag {
			case colFloat64:
				return []value.Value{value.NumberValue(colRef.f64[row])}, nil
			case colInt64:
				v := colRef.i64[row]
				if v > 1<<53 || v < -(1<<53) {
					return nil, crescent.NewError("arena int64 value exceeds 2^53 precision range")
				}
				return []value.Value{value.NumberValue(float64(v))}, nil
			case colBool:
				bit := colRef.boolBits[row/64]&(1<<(row%64)) != 0
				return []value.Value{value.BoolValue(bit)}, nil
			case colString:
				slot := colRef.strSlots[row]
				b := strBytes[slot.off : slot.off+slot.len]
				ref := ist.InternForEmbed(b)
				return []value.Value{value.MakeGC(value.TagString, ref)}, nil
			}
			return []value.Value{value.Nil}, nil
		}
		readonly := func(_ *crescent.State, _ []value.Value) ([]value.Value, *crescent.LuaError) {
			return nil, crescent.NewError("arena column is read-only")
		}
		idxID := core.RegisterHostFn(readCell)
		nwID := core.RegisterHostFn(readonly)
		core.SetTableField(meta, "__index", value.MakeGC(value.TagFunction, core.MakeHostClosure(idxID)))
		core.SetTableField(meta, "__newindex", value.MakeGC(value.TagFunction, core.MakeHostClosure(nwID)))
		core.SetMeta(proxy, meta)
		core.SetTableField(arenaTbl, col.name, value.MakeGC(value.TagTable, proxy))
	}
	core.SetGlobal("arena", value.MakeGC(value.TagTable, arenaTbl))
}

// Value is the multi-type value of the public API (11 §4.5).
//
// P1/M13 simplified version: represented by a sum-type Go struct. GC decoupling:
// the string content held by Value has already been copied out of the VM arena
// (string), and function / table values are held indirectly via the owning
// State's pin table (kFunction / kTable: the outside cannot directly construct a
// GCRef, only take one from NewTable / GetGlobal / Call return values,
// registered as a GC root via the pin table). userdata is still not exposed.
type Value struct {
	kind kind
	// number field
	num float64
	// string field (already copied out of arena bytes)
	str []byte
	// bool field
	b bool
	// function / table field: fnState is the owning State, pinIdx is its pin
	// table index. fnState != nil means valid; set to nil after Release.
	fnState *State
	pinIdx  uint32
}

type kind uint8

const (
	kNil kind = iota
	kBool
	kNumber
	kString
	kFunction
	kTable
)

// Constructors (function/table have no public constructor: a function is taken
// via GetGlobal, a table is created via State.NewTable).
func Nil() Value             { return Value{kind: kNil} }
func Bool(b bool) Value      { return Value{kind: kBool, b: b} }
func Number(f float64) Value { return Value{kind: kNumber, num: f} }
func String(s string) Value  { return Value{kind: kString, str: []byte(s)} }

// Type predicates.
func (v Value) IsNil() bool      { return v.kind == kNil }
func (v Value) IsBool() bool     { return v.kind == kBool }
func (v Value) IsNumber() bool   { return v.kind == kNumber }
func (v Value) IsString() bool   { return v.kind == kString }
func (v Value) IsFunction() bool { return v.kind == kFunction && v.fnState != nil }
func (v Value) IsTable() bool    { return v.kind == kTable && v.fnState != nil }

// Accessors.
func (v Value) Bool() bool      { return v.b }
func (v Value) Number() float64 { return v.num }
func (v Value) Str() string     { return string(v.str) }

// Release explicitly frees the pin-table slot of a function / table Value.
// Repeated Release / calling on other kinds has no side effect. Under a
// long-lived State, if functions are repeatedly taken via NewTable / GetGlobal
// without Release, the pin table accumulates by slot — a pineapple-style "take
// once per script" shape has no such problem and can omit Release; high-
// throughput scenarios should pair it.
func (v *Value) Release() {
	if v.fnState == nil {
		return
	}
	if v.kind != kFunction && v.kind != kTable {
		return
	}
	v.fnState.core.UnpinRef(v.pinIdx)
	v.fnState = nil
}

// Display renders in Lua style (convenient for error messages).
func (v Value) Display() string {
	switch v.kind {
	case kNil:
		return "nil"
	case kBool:
		if v.b {
			return "true"
		}
		return "false"
	case kNumber:
		return crescent.FormatLuaNumber(v.num)
	case kString:
		return string(v.str)
	case kFunction:
		return "function"
	case kTable:
		return "table"
	}
	return "<unknown>"
}

// toInner / fromInner bridge the public Value and the internal value.Value.
//
// When bridging kFunction / kTable to the internal TagFunction / TagTable, the
// owning State's pin slot is reused directly (the caller already validated
// fnState == the target state at the State.Call layer); a cross-State
// function/table Value is mapped to Nil here as a fallback (to prevent a GCRef
// misbound to the arena from causing a UAF).
func (v Value) toInner(state *State) value.Value {
	switch v.kind {
	case kNil:
		return value.Nil
	case kBool:
		return value.BoolValue(v.b)
	case kNumber:
		return value.NumberValue(v.num)
	case kString:
		// interned into the state arena via the collector
		ref := state.coreInternBytes(v.str)
		return value.MakeGC(value.TagString, ref)
	case kFunction:
		if v.fnState != state {
			return value.Nil
		}
		ref := state.core.PinnedRefAt(v.pinIdx)
		if ref.IsNull() {
			return value.Nil
		}
		return value.MakeGC(value.TagFunction, ref)
	case kTable:
		if v.fnState != state {
			return value.Nil
		}
		ref := state.core.PinnedRefAt(v.pinIdx)
		if ref.IsNull() {
			return value.Nil
		}
		return value.MakeGC(value.TagTable, ref)
	}
	return value.Nil
}

func fromInner(state *State, v value.Value) Value {
	if value.IsNumber(v) {
		return Number(value.AsNumber(v))
	}
	switch value.Tag(v) {
	case value.TagNil:
		return Nil()
	case value.TagBool:
		return Bool(value.AsBool(v))
	case value.TagString:
		// copy out arena bytes
		bytes := state.coreStringBytes(value.GCRefOf(v))
		out := make([]byte, len(bytes))
		copy(out, bytes)
		return Value{kind: kString, str: out}
	}
	// table/function/userdata are not exposed by default (return nil);
	// exposing function/table is done explicitly via the pin table by
	// fromInnerWithPin, avoiding a silent side effect (pinning a GCRef on every
	// raw read would leak unboundedly).
	return Nil()
}

// fromInnerWithPin is the "can carry a function / table reference" bridge, used
// only by entry calls of the kind "the public surface takes out / creates a
// composite value for the Go side to hold long-term" — GetGlobal / NewTable /
// State.Call return values. function / table are registered into the State pin
// table via PinRef (a GC root) to isolate the globals-overwrite and freelist-
// reuse risks.
func fromInnerWithPin(state *State, v value.Value) Value {
	switch value.Tag(v) {
	case value.TagFunction:
		ref := value.GCRefOf(v)
		idx := state.core.PinRef(ref)
		return Value{kind: kFunction, fnState: state, pinIdx: idx}
	case value.TagTable:
		ref := value.GCRefOf(v)
		idx := state.core.PinRef(ref)
		return Value{kind: kTable, fnState: state, pinIdx: idx}
	}
	return fromInner(state, v)
}

// coreInternBytes / coreStringBytes are State-internal convenience bridges
// (avoiding exposing internal/gc).
func (st *State) coreInternBytes(b []byte) arena.GCRef {
	// Via the Run path, a String value is created on the inner state; goes through
	// the collector's Intern. We do not expose the collector on the public API, so
	// this is a helper (internal/crescent.State is not exposed directly), reaching
	// the helper via the arena.
	return st.core.InternForEmbed(b)
}

func (st *State) coreStringBytes(ref arena.GCRef) []byte {
	return object.StringBytes(st.core.Arena(), ref)
}
