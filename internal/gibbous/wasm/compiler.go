//go:build wangshu_p3

// Package wasm is the P3 gibbous tier: bytecode → Wasm compiler + wazero execution environment
// (docs/design/p3-wasm-tier/).
//
// Compiled only under the wangshu_p3 build — under the default / wangshu_profile builds this
// package is not in the import graph, and the main library does not link wazero runtime code.
//
// PW progress (02-translation §1.3 incremental whitelist):
//   - PW1 (this round): package skeleton + Compiler implements bridge.P3Compiler;
//     SupportsAllOpcodes always false (supported all false) → no Proto promotion →
//     byte-equal with P1-only. The arena adopts wazero memory via the memadapter subpackage.
//   - PW2+: expand the supported whitelist tier by tier + emit translation (see 02-translation §3).
package wasm

import (
	"context"
	"fmt"
	"runtime/debug"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"

	"github.com/Liam0205/wangshu/internal/bridge"
	"github.com/Liam0205/wangshu/internal/bytecode"
)

// Compiler implements bridge.P3Compiler (02-translation §5).
//
// One Compiler serves one State (holding that State's wazero Runtime, sharing the same
// source as the memadapter MemoryHolder). Multiple States each hold an independent
// Compiler / Runtime / Memory (the arena is private to a single State, 03 §1.5).
type Compiler struct {
	ctx     context.Context
	runtime wazero.Runtime

	// host is the injected runtime state abstraction (crescent.State implements HostState) —
	// helper callbacks operate on CallInfo / value stack / upvalues through it, breaking the crescent⇄gibbous cycle.
	host HostState

	// hostModInstantiated marks whether the env host module (with memory + helpers) has
	// been registered to the runtime. Registered once on the first Compile, reused by later Protos.
	hostModReady bool

	// supported[op] = whether this opcode already has a Wasm translation.
	// PW1: all false (conservative default, 02-translation §1.3 + §5.2).
	// PW2: straight-line opcodes (MOVE/LOADK/LOADBOOL/LOADNIL/GETUPVAL/SETUPVAL/JMP).
	supported [numOpcodes]bool

	// slotOf: Proto → shared env.table slot number (PW10 Arch-2). Each promoted Proto is
	// assigned a monotonically increasing slot at Compile time; its module registers run into
	// that slot via the element segment; a gibbous→gibbous CALL reaches across modules directly
	// via call_indirect using the callee Proto's slot (R3 wiring). -1 (not in table) = not
	// promoted / over capacity → fall back to h_call.
	slotOf   map[*bytecode.Proto]uint32
	nextSlot uint32
}

// maxTableSlots is the upper bound of slots the Compiler can allocate (= memadapter.TableSlots,
// the env shared table capacity). Beyond it a Proto gets no slot (falls back to h_call, correctness intact).
// Hardcoded to avoid the wasm package (non-test code) reverse-importing memadapter and forming an
// inverted import cycle; the two values must agree (TestPW10_SlotCapAligned asserts alignment).
const maxTableSlots = 8192

// numOpcodes is the count of P1 active opcodes (0..37) + a reserved-region upper-bound guard.
// bytecode.OpCode is 6-bit (0..63); the supported array is built at length 64, so
// out-of-range opcodes (38..63 reserved) naturally land on false.
const numOpcodes = 64

// NewCompiler constructs a Compiler (PW1: supported all false).
//
// runtime is created and injected by the facade layer (crescent under the wangshu_p3 build),
// sharing the same Runtime as memadapter.MemoryHolder — ensuring the gibbous module can share,
// via imported memory, the linear memory adopted by the arena. host is the runtime state
// abstraction (crescent.State implements HostState); helper callbacks operate on runtime state through it.
//
// PW2 supported whitelist: straight-line opcodes (02-translation §1.3 PW2 tier).
func NewCompiler(ctx context.Context, runtime wazero.Runtime, host HostState) *Compiler {
	c := &Compiler{
		ctx:     ctx,
		runtime: runtime,
		host:    host,
		slotOf:  make(map[*bytecode.Proto]uint32),
	}
	c.supported[bytecode.MOVE] = true
	c.supported[bytecode.LOADK] = true
	c.supported[bytecode.LOADBOOL] = true
	c.supported[bytecode.LOADNIL] = true
	c.supported[bytecode.GETUPVAL] = true
	c.supported[bytecode.SETUPVAL] = true
	c.supported[bytecode.JMP] = true
	c.supported[bytecode.RETURN] = true // required as the exit of a single-BB Proto
	// PW3: straight-line arithmetic (no BB split). Comparisons EQ/LT/LE/TEST/TESTSET split BBs, left to PW4.
	c.supported[bytecode.ADD] = true
	c.supported[bytecode.SUB] = true
	c.supported[bytecode.MUL] = true
	c.supported[bytecode.DIV] = true
	c.supported[bytecode.MOD] = true
	c.supported[bytecode.POW] = true
	c.supported[bytecode.UNM] = true
	c.supported[bytecode.NOT] = true
	c.supported[bytecode.LEN] = true
	c.supported[bytecode.CONCAT] = true
	// PW4: control flow + comparisons (relooper unlocks multi-BB).
	c.supported[bytecode.EQ] = true
	c.supported[bytecode.LT] = true
	c.supported[bytecode.LE] = true
	c.supported[bytecode.TEST] = true
	c.supported[bytecode.TESTSET] = true
	c.supported[bytecode.FORPREP] = true
	c.supported[bytecode.FORLOOP] = true
	// PW5: table IC opcodes (inline snapshot freezing + invalidation downgrade helpers).
	c.supported[bytecode.GETGLOBAL] = true
	c.supported[bytecode.SETGLOBAL] = true
	c.supported[bytecode.GETTABLE] = true
	c.supported[bytecode.SETTABLE] = true
	c.supported[bytecode.SELF] = true
	c.supported[bytecode.NEWTABLE] = true
	c.supported[bytecode.SETLIST] = true
	// PW6: CALL three-way dispatch + base refresh (cross-tier mutual calls).
	c.supported[bytecode.CALL] = true
	c.supported[bytecode.TAILCALL] = true
	// PW7: closure construction + scoped upvalue closing (all via helpers).
	c.supported[bytecode.CLOSURE] = true
	c.supported[bytecode.CLOSE] = true
	// PW4b: TFORLOOP generic for (calls the iterator via h_tforloop + base refresh).
	c.supported[bytecode.TFORLOOP] = true
	// PW5+ unlocked tier by tier (02-translation §1.3). VARARG is never added.
	return c
}

// SlotOf returns the Proto's slot number in the shared env.table + whether it is registered (PW10 Arch-2).
//
// Used by R3 CALL translation: if the callee Proto is already promoted to gibbous and has a valid slot
// (< maxTableSlots) ⟹ reach it directly across modules via call_indirect <slot>; otherwise
// (not registered / table-full sentinel) fall back to h_call.
// ok=false means the Proto has not been compiled yet (no slot); returning maxTableSlots means the table
// was full and it did not enter the table.
func (c *Compiler) SlotOf(proto *bytecode.Proto) (uint32, bool) {
	s, ok := c.slotOf[proto]
	return s, ok
}

// WorthPromoting implements bridge.PromotionGater (issue #39):
// profitability judgment, consulted in auto mode after capability
// (SupportsAllOpcodes) passes. Returns false for protos whose op mix
// is dominated by helper-round-trip ops — every GETTABLE / SETTABLE /
// SELF miss, every CALL, and every LEN / CONCAT / NEWTABLE / SETLIST
// crosses the wasm→Go host boundary (~tens of ns each, plus the IC
// inline fast path only covers warmed mono sites). On helper-dense
// float kernels (nbody's advance / energy: ~1 table op per 3 opcodes)
// the promoted code runs ~2x SLOWER than the interpreter — measured
// 43.5ms → 89.7ms after 45b8b53 unblocked their promotion (issue #39).
//
// Density judgment mirrors P4's CALL density gate (AnalyzeNative,
// bebbd44) but over the wasm helper set: require enough total ops per
// helper-bound op to amortize the boundary crossings. Pure-arithmetic
// loop kernels (heavy_arith / heavy_floatloop: zero helper-bound ops)
// promote and keep their measured P3 wins; helper-dense kernels
// (nbody advance ~1/3, fannkuch shuffles ~1/3, fib call bodies 1/6,
// spectral-norm accessor loops ~1/7) stay on the interpreter.
func (c *Compiler) WorthPromoting(proto *bytecode.Proto) bool {
	if len(proto.Code) == 0 {
		return true
	}
	// Back-edge dimension (issue #92): P3's win comes from dispatch saved
	// INSIDE loops; the price is a per-call wasm boundary round trip
	// (~130ns/call vs ~73ns/call interpreted, measured on the Arith
	// kernel). A straight-line body (no back edge) has nothing to
	// amortize that tax — a small one loses on every call (Arith kernel:
	// promoted 6450ns vs interpreted 3666ns, 1.76x SLOWER). Reject small
	// straight-line bodies; large ones (>= straightLineMinCodeLen) carry
	// enough per-call dispatch savings to cover the boundary.
	hasBackEdge := false
	total := 0
	helperBound := 0
	for _, ins := range proto.Code {
		total++
		switch op := bytecode.Op(ins); op {
		case bytecode.GETTABLE, bytecode.SETTABLE, bytecode.SELF,
			bytecode.GETGLOBAL, bytecode.SETGLOBAL,
			bytecode.CALL, bytecode.TAILCALL,
			bytecode.LEN, bytecode.CONCAT,
			bytecode.NEWTABLE, bytecode.SETLIST,
			bytecode.CLOSURE, bytecode.CLOSE:
			helperBound++
		case bytecode.FORLOOP, bytecode.TFORLOOP:
			hasBackEdge = true
		case bytecode.JMP:
			if bytecode.SBx(ins) < 0 {
				hasBackEdge = true
			}
		}
	}
	if !hasBackEdge && len(proto.Code) < straightLineMinCodeLen {
		return false
	}
	if helperBound == 0 {
		return true
	}
	// Floor 7, measured on the realworld + heavy suites (Xeon
	// Platinum, benchtime=2s count=3, 2026-07-03): every kernel that
	// measured promoted-slower-than-interpreter falls under it (nbody
	// advance 74/26 ≈ 2.8, fib 12/2 = 6, spectral-norm accessors
	// 20/3 ≈ 6.7 — at floor 5 fib still promoted and lost ~2.3x), and
	// every kernel that wins on P3 has no helper-bound ops at all
	// (heavy_arith / heavy_floatloop return early above). Auto-mode
	// results with this floor: nbody 89.7→43.2ms, fib 24.9→10.9ms,
	// binary-trees 104→38.4ms, spectral-norm 40→20.7ms; heavy three
	// unchanged (86.3 / 5.68 / 50.9ms).
	return total/helperBound >= wasmHelperDensityFloor
}

// wasmHelperDensityFloor is the minimum total-ops-per-helper-bound-op
// ratio for promotion to be predicted profitable (see WorthPromoting).
const wasmHelperDensityFloor = 7

// straightLineMinCodeLen is the minimum Code length for a proto with NO
// back edge to be predicted profitable (issue #92). Balance point: the
// per-call boundary tax (call_indirect entry/exit, ~57ns measured on
// the Arith kernel: 130ns promoted vs 73ns interpreted per call) over
// the per-instruction dispatch saving (~2-4ns). At 32+ straight-line
// instructions the saved dispatch covers the boundary; below it the
// call is a net loss on every entry. Calibrated on darwin/arm64 (issue
// #92 PoC) and re-checked on amd64 Xeon: Arith kernel (10 insns)
// 6450 -> 3551 ns/op back at interpreter level, Loop kernel (back edge)
// keeps promoting, heavy suite unchanged (all have back edges).
const straightLineMinCodeLen = 32

// SupportsAllOpcodes implements the F7 backend capability query (03 §3.7 + 02 §5.2).
//
// Pure read-only, does not modify the Proto, does not panic (out-of-range opcode numbers naturally land on false).
//
// Constraints:
//   - all opcodes in reachable BBs are in the supported whitelist;
//   - for multi-BB, the CFG must be reducible (the relooper only handles reducible CFGs, PW4);
//   - LOADK with a string constant: Consts is a State-private lazy intern (Nil placeholder at
//     compile time), so no real GCRef can be baked out → reject (left to PW5 to fetch via a helper).
//
// Note this method only answers "can it be compiled"; "is compiling it worth it" is WorthPromoting's job.
func (c *Compiler) SupportsAllOpcodes(proto *bytecode.Proto) bool {
	if len(proto.Code) == 0 {
		return true // empty Proto vacuously supported (in practice never judged hot by P2)
	}
	cfg := buildCFG(proto)
	reach := cfg.reachableBlocks()
	// Multi-BB: must be reducible (the relooper only handles reducible CFGs).
	if len(reach) > 1 {
		if !analyzeRelooper(cfg).isReducible() {
			return false
		}
	}
	// Scan the opcodes of all **reachable** BBs (dead-code blocks never execute, so they do not affect the support decision).
	for _, blk := range cfg.blocks {
		if !reach[blk.id] {
			continue
		}
		for pc := blk.startPC; pc < blk.endPC; pc++ {
			ins := proto.Code[pc]
			op := bytecode.Op(ins)
			if int(op) >= numOpcodes || !c.supported[op] {
				return false
			}
			// LOADK string constant: the real value cannot be baked out at compile time
			if op == bytecode.LOADK {
				bx := bytecode.Bx(ins)
				if proto.IsStringConst(bx) {
					return false
				}
			}
			// SETLIST C=0: the next instruction word is a large batch number (data, not an opcode) — the
			// linear emitter would mistakenly translate it as an opcode → reject. B=0 (fill to top) depends on
			// gibbous frame top maintenance (not wired before PW7) → reject. The common {1,2,3} (B≥1, C≥1) is allowed.
			if op == bytecode.SETLIST {
				if bytecode.C(ins) == 0 || bytecode.B(ins) == 0 {
					return false
				}
			}
			// CALL B=0 (args to top) / C=0 (returns to top) are multi-value windows, depending on th.top
			// maintained across opcodes — gibbous straight-line code does not maintain top → reject. Fixed args
			// and fixed returns (B≥1, C≥1) are allowed (the common local x = f(a,b) form). Multi-value propagation left for later.
			if op == bytecode.CALL {
				if bytecode.C(ins) == 0 || bytecode.B(ins) == 0 {
					return false
				}
			}
			// TAILCALL B=0 (args to top) depends on th.top like CALL → reject (fixed args B≥1 allowed).
			if op == bytecode.TAILCALL {
				if bytecode.B(ins) == 0 {
					return false
				}
			}
		}
	}
	return true
}

// Compile compiles a Proto into GibbousCode (02 §5.1 + §5.5 panic fallback).
//
// Flow: ① ensure the host module (helpers) is registered ② translate produces the function body
// ③ buildGibbousModuleBinary assembles the complete module ④ wazero CompileModule +
// InstantiateModule (import env.memory + host.h_*) ⑤ wrap in p3Code.
//
// The defer recover fallback: any panic from translation / wazero calls is turned into a
// *CompileError (Kind=BackendPanic), which does not cross this interface (02 §1.4).
func (c *Compiler) Compile(proto *bytecode.Proto, fb *bridge.TypeFeedback) (gc bridge.GibbousCode, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = &bridge.CompileError{
				Kind:   bridge.CompileErrBackendPanic,
				Proto:  proto,
				Reason: fmt.Sprintf("p3 backend panic: %v\nstack: %s", r, debug.Stack()),
			}
			gc = nil
		}
	}()

	// ① register the host module (helpers) once
	if err := c.ensureHostModule(); err != nil {
		return nil, &bridge.CompileError{
			Kind: bridge.CompileErrOutOfResources, Proto: proto,
			Reason: "p3: register host module: " + err.Error(),
		}
	}

	// ② translate
	body, terr := c.translate(proto)
	if terr != nil {
		return nil, &bridge.CompileError{
			Kind: bridge.CompileErrUnsupportedOpcodeShape, Proto: proto,
			Reason: terr.Error(),
		}
	}

	// Allocate a shared env.table slot (PW10 Arch-2): this module's run is registered into
	// table[slot] via the element segment, and gibbous→gibbous reaches across modules directly via
	// call_indirect on this slot (R3).
	// Idempotent: recompiling the same Proto (multiple States share the same Proto; each Compiler is
	// independent in theory) reuses the existing slot.
	slot, ok := c.slotOf[proto]
	if !ok {
		if c.nextSlot >= maxTableSlots {
			// Table full: no slot allocated, this Proto's run does not enter the table (R3 falls back to
			// h_call based on "no slot"). It can still compile and execute, only gibbous→it takes the slow
			// path. Marked with the sentinel maxTableSlots.
			slot = maxTableSlots
		} else {
			slot = c.nextSlot
			c.nextSlot++
		}
		c.slotOf[proto] = slot
	}

	// ③ assemble the module binary (slot registered into the element segment; on the table-full
	// sentinel, still emitting an element to write table[maxTableSlots] would go out of bounds — so on
	// table full no element is emitted, see buildGibbousModuleBinary).
	bin := buildGibbousModuleBinary(body, slot)

	// ④ wazero compile + instantiate
	compiled, cerr := c.runtime.CompileModule(c.ctx, bin)
	if cerr != nil {
		return nil, &bridge.CompileError{
			Kind: bridge.CompileErrOutOfResources, Proto: proto,
			Reason: "p3: wazero compile: " + cerr.Error(),
		}
	}
	mod, ierr := c.runtime.InstantiateModule(c.ctx, compiled,
		wazero.NewModuleConfig().WithName(gibbousModuleName(proto)))
	if ierr != nil {
		_ = compiled.Close(c.ctx)
		return nil, &bridge.CompileError{
			Kind: bridge.CompileErrOutOfResources, Proto: proto,
			Reason: "p3: wazero instantiate: " + ierr.Error(),
		}
	}
	fn := mod.ExportedFunction("run")
	if fn == nil {
		_ = mod.Close(c.ctx)
		return nil, &bridge.CompileError{
			Kind: bridge.CompileErrBackendPanic, Proto: proto,
			Reason: "p3: gibbous module exports no 'run'",
		}
	}

	return &p3Code{
		compiled: compiled,
		module:   mod,
		fn:       fn,
		proto:    proto,
		ctx:      c.ctx,
		slot:     slot,
		hasSlot:  slot < maxTableSlots,
	}, nil
}

// ensureHostModule registers the host module (helper Go functions) to the runtime, once.
//
// helper callbacks forward to the runtime state (crescent.State) through c.host (HostState).
// module name "host", corresponding to the gibbous module's `import "host" "h_*"`.
//
// **Zero-allocation registration (PW10 R3.5)**: use WithGoFunction (api.GoFunc, stack-based) rather than
// WithFunc (reflection) — the latter does reflect.New per-argument boxing on every cross-tier callGoFunc
// (~14 allocs/call at the call core, a dominant regression). Stack-based decodes args / writes back
// directly from []uint64, zero reflection and zero allocation.
// The params/results ValueType must align bit-for-bit with the type declaration in module.go.
func (c *Compiler) ensureHostModule() error {
	if c.hostModReady {
		return nil
	}
	hs := &helperSet{host: c.host}
	// Reused wasm value type signatures (aligned with the module.go type declarations).
	i32 := api.ValueTypeI32
	i64 := api.ValueTypeI64
	var (
		p2     = []api.ValueType{i32, i32}
		p3     = []api.ValueType{i32, i32, i32}
		p4     = []api.ValueType{i32, i32, i32, i32}
		p5     = []api.ValueType{i32, i32, i32, i32, i32}
		p6     = []api.ValueType{i32, i32, i32, i32, i32, i32}
		retI32 = []api.ValueType{i32}
		retI64 = []api.ValueType{i64}
		none   = []api.ValueType(nil)
	)
	b := c.runtime.NewHostModuleBuilder("host")
	add := func(name string, fn api.GoFunc, params, results []api.ValueType) {
		b.NewFunctionBuilder().WithGoFunction(fn, params, results).Export(name)
	}
	add("h_getupval", hs.goGetUpval, p2, retI64)
	add("h_setupval", hs.goSetUpval, []api.ValueType{i32, i32, i64}, none)
	add("h_return", hs.goReturn, p4, retI32)
	add("h_safepoint", hs.goSafepoint, p2, retI32)
	add("h_arith", hs.goArith, p6, retI32)
	add("h_unm", hs.goUnm, p4, retI32)
	add("h_len", hs.goLen, p4, retI32)
	add("h_concat", hs.goConcat, p5, retI32)
	add("h_compare", hs.goCompare, p5, retI32)
	add("h_eq", hs.goEq, p4, retI32)
	add("h_forprep", hs.goForPrep, p3, retI32)
	add("h_gettable", hs.goGetTable, p5, retI32)
	add("h_settable", hs.goSetTable, p5, retI32)
	add("h_getglobal", hs.goGetGlobal, p4, retI32)
	add("h_setglobal", hs.goSetGlobal, p4, retI32)
	add("h_self", hs.goSelf, p5, retI32)
	add("h_newtable", hs.goNewTable, p5, retI32)
	add("h_setlist", hs.goSetList, p5, retI32)
	add("h_call", hs.goCall, p5, retI64)
	add("h_tailcall", hs.goTailCall, p5, retI32)
	add("h_closure", hs.goClosure, p4, retI32)
	add("h_close", hs.goClose, p3, retI32)
	add("h_tforloop", hs.goTForLoop, p4, retI64)
	add("h_callerr", hs.goCallErr, none, none)
	if _, err := b.Instantiate(c.ctx); err != nil {
		return err
	}
	c.hostModReady = true
	return nil
}

// gibbousModuleName gives each Proto's gibbous module a unique name (wazero requires
// already-named module names to be unique). Uses the Proto pointer address — unique and stable within a single State.
func gibbousModuleName(proto *bytecode.Proto) string {
	return fmt.Sprintf("gib_%p", proto)
}
