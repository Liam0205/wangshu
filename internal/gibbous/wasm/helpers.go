//go:build wangshu_p3

package wasm

// Go-side implementation of the imported helpers (02-translation §6.4 + 04-trampoline §3).
//
// **Dependency cycle break**: helpers need to operate on runtime state (CallInfo,
// value stack, upvalues, return-value write-back), all owned by crescent.State. But
// crescent must import gibbous/wasm to get p3Code.Run (trampoline); if gibbous/wasm
// imports crescent back, that forms a cycle. Resolution (same interface-injection
// technique the P2 bridge uses to avoid depending on crescent): gibbous/wasm defines
// the minimal abstract interface HostState that the helpers need, and crescent
// implements it and injects it during the tier-up install.
//
// PW2 helper set: h_getupval / h_setupval / h_return / h_safepoint.

import (
	"context"

	"github.com/tetratelabs/wazero/api"
)

// HostState is the minimal abstraction the gibbous helpers use to operate on runtime
// state (implemented by crescent.State).
//
// All methods are coordinated by base (the byte offset of the current frame's R0) ——
// gibbous frames and interpreter frames share the same value stack (03-memory-model),
// and base uniquely locates the current frame.
//
// Method semantics directly reuse the existing interpreter-side implementation (the
// corresponding execute.go lines), guaranteeing byte-equal behavior.
type HostState interface {
	// GetUpval reads the value of upvalue B of the current closure (execute.go GETUPVAL section).
	GetUpval(base int32, b int32) uint64
	// SetUpval writes upvalue B of the current closure (execute.go SETUPVAL section).
	SetUpval(base int32, b int32, val uint64)
	// DoReturn handles RETURN A B: writes return values back to the caller's expected
	// slots + records savedPC. Returns status (0=OK / 1=ERR); the gibbous function
	// returns accordingly.
	DoReturn(base int32, pc int32, a int32, b int32) int32
	// Safepoint is the back-edge checkpoint (GC + loop step-budget accounting). Returns
	// status (0=OK / 1=raise: budget exceeded or ctx canceled); the gibbous section
	// returns 1 to bubble up accordingly.
	Safepoint(base int32, pc int32) int32
	// LoopBudgetAddr returns the linear-memory byte address of the loop-budget fuel word
	// (P3 loop step-budget fix): the back edge decrements it inline, only crossing the
	// layer to h_safepoint once it hits zero.
	LoopBudgetAddr() uint32
	// SetSavedPC writes back CallInfo.savedPC (pc materialization, 02 §4.2).
	SetSavedPC(base int32, pc int32)

	// --- PW3 arithmetic slow-path helpers (the fast path emits f64 directly inside Wasm for two numbers, falling back to Go on failure) ---

	// Arith handles the arithmetic-opcode slow path (ADD/SUB/MUL/DIV/MOD/POW): coercion +
	// metamethod chain (reuses execute.go doArithSlow). op is a bytecode.OpCode value.
	// Returns status (0=OK / 1=ERR).
	Arith(base, pc, op, b, c, a int32) int32
	// Unm handles the UNM slow path (string coercion + __unm).
	Unm(base, pc, b, a int32) int32
	// Len handles LEN (string length / table border / error on other types; reuses execute.go LEN section).
	Len(base, pc, b, a int32) int32
	// Concat handles CONCAT (reuses the full execute.go doConcat logic + safepoint).
	Concat(base, pc, a, b, c int32) int32

	// --- PW4 control-flow slow-path helpers ---

	// Compare handles the LT/LE slow path (string comparison / __lt/__le metamethods). op is
	// a bytecode.OpCode. Returns packed: bit0=comparison result (0/1), bit1=error flag.
	Compare(base, pc, op, b, c int32) int32
	// Eq handles the __eq metamethod path of EQ (when raw values are unequal; reuses rawEqual + __eq).
	// Returns packed: bit0=result, bit1=error.
	Eq(base, pc, b, c int32) int32
	// ForPrep handles FORPREP: three-slot validation + coercion + pre-decrement (reuses
	// execute.go FORPREP section, byte-equal error messages). Returns status (0=OK / 1=ERR).
	ForPrep(base, pc, a int32) int32

	// --- PW5 table IC slow-path helpers (the fast path inlines the hash probe, falling back to Go on miss/complex shapes) ---

	// GetTable handles the GETTABLE A B C slow path (full icGetTable lookup + __index).
	// Returns status (0=OK / 1=ERR).
	GetTable(base, pc, a, b, c int32) int32
	// SetTable handles the SETTABLE A B C slow path (full icSetTable write + __newindex + safepoint).
	SetTable(base, pc, a, b, c int32) int32
	// GetGlobal handles the GETGLOBAL A Bx slow path (icGetTable on the globals table).
	// The Do prefix avoids a clash with the State public API GetGlobal(name string).
	DoGetGlobal(base, pc, a, bx int32) int32
	// SetGlobal handles the SETGLOBAL A Bx slow path (icSetTable on the globals table + safepoint).
	DoSetGlobal(base, pc, a, bx int32) int32
	// Self handles SELF A B C (R(A+1):=R(B) + icGetTable; self forwarding is contained inside the helper, idempotent).
	Self(base, pc, a, b, c int32) int32
	// NewTable handles NEWTABLE A B C (allocTable + setReg + safepoint, all allocation inside the helper).
	NewTable(base, pc, a, b, c int32) int32
	// SetList handles SETLIST A B C (doSetList batch write + possible rehash + safepoint).
	SetList(base, pc, a, b, c int32) int32

	// Call handles the CALL A B C three-way dispatch (crescent/gibbous/host, 04-trampoline §3).
	// Runs the callee frame to completion, leaving return values in the shared stack slots R(A..).
	// Returns: success = **the refreshed byte offset of this frame's base** (a nested call may
	// relocate the segment via growStack, and gibbous must use this new base to continue address
	// computation, otherwise a stale $base points at an already-Freed old segment = UAF);
	// error = negative sentinel -1 (pendingErr already set, status chain bubbles up).
	DoCall(base, pc, a, b, c int32) int64

	// TailCall handles TAILCALL A B C (tail call reusing the frame, 04-trampoline §2.5).
	// Reuses doTailCall to close upvalues + shift arguments down + rewrite the current
	// CallInfo, then synchronously drives the reused frame to completion (executeFrom,
	// preserving the proper-tail-call O(1) stack). Return values already land in the
	// caller's expected slots. Returns status (0=OK, the gibbous function should return 0
	// directly / 1=ERR).
	TailCall(base, pc, a, b, c int32) int32

	// Closure handles CLOSURE A Bx (makeClosure + setReg + safepoint, allocation inside the helper).
	// The trailing pseudo-instructions are consumed by makeClosure reading ci.pc (the emit side already skipped their translation). Returns status (0/1).
	Closure(base, pc, a, bx int32) int32
	// Close handles CLOSE A (closes the open upvalues ≥ base+A, a pure state operation). Returns status (always 0).
	Close(base, pc, a int32) int32
	// TForLoop handles TFORLOOP A C (calls the iterator R(A)(R(A+1),R(A+2)), results land in R(A+3..)).
	// The iterator call goes through callLuaFromHost and may relocate the segment via growStack → returns this frame's refreshed base:
	//   ≥0 = new base byte offset (first value non-nil, keep looping); -1 = ERR; -2 = exit (first value nil).
	TForLoop(base, pc, a, c int32) int64

	// GlobalsRaw returns the NaN-boxed u64 of the globals table (baked as an immediate at
	// compile time, used inline by GETGLOBAL/SETGLOBAL). The globals identity stays constant
	// and does not move over the State's lifetime. The Raw suffix avoids a clash with the
	// State public API Globals() arena.GCRef.
	GlobalsRaw() uint64

	// GCPendingAddr returns the linear-memory byte address of the gcPending flag word (arena GCRef,
	// P3 PW9). The gibbous FORLOOP back edge inlines `i32.load(GCPendingAddr)` —— only crossing the
	// layer to h_safepoint when it is non-zero (otherwise a hot loop crosses the layer unconditionally
	// every iteration and eats the gain, 05 §3). Constant over the State's lifetime.
	GCPendingAddr() uint32

	// CITransferAddr returns the linear-memory byte address of the ci-transfer relay word (arena GCRef,
	// P3 PW10 R3). A gibbous→gibbous call_indirect direct call passes the callee frame's base through
	// this word (DoCall writes it, the caller wasm reads it as the call_indirect argument) and the
	// refreshed caller base (DoReturn writes it, the caller reads it after call_indirect returns to
	// continue). Constant over the State's lifetime.
	CITransferAddr() uint32

	// CIDepthAddr returns the linear-memory byte address of the ci-depth cursor word (arena GCRef,
	// P3 PW10 zero-crossing Stage 1a). The Wasm-side frame build/teardown (Stage 2/3) increments/decrements
	// this i32 word without returning to Go to change th.ciDepth. Constant over the State's lifetime.
	CIDepthAddr() uint32

	// CISegBaseAddr returns the linear-memory byte address of the ci-seg-base word (arena GCRef,
	// P3 PW10 zero-crossing Stage 2). This word holds the current byte base of the CI segment (growCISeg
	// may relocate it); the Wasm-side frame build/teardown reads it to compute frame addresses on the fly.
	// **The word address is constant**, but the word content (segment base) changes as the segment relocates.
	CISegBaseAddr() uint32

	// OpenGuardAddr returns the linear-memory byte address of the open-upvalue guard word (arena GCRef,
	// P3 PW10 zero-crossing Stage 2). Word value = maxOpenIdx+1 (there are open upvalues) / 0 (none); the Wasm
	// RETURN fast path guards frameBase ≥ this value ⟺ this frame has no open upvalues to close.
	OpenGuardAddr() uint32

	// TopAddr returns the linear-memory byte address of the top mirror word (arena GCRef, P3 PW10 zero-crossing
	// ①). Word value = th.top (slot index); the Wasm frame build sets the callee frame top / the caller writes
	// it when restoring top itself, and the Go-side GC stack-root scan reads it to bound the upper limit of [0,top).
	// Slot-index coordinate (grow-safe). Constant over the State's lifetime.
	TopAddr() uint32

	// ProtoCacheBaseAddr returns the byte address of the mirror word for the base of the proto-field cache segment
	// (arena GCRef, P3 PW10 zero-crossing infra-b). The Wasm ④ emitCall guard fast path reads this base + protoID*8
	// on the fly to fetch the callee Proto's MaxStack/NumParams/IsVararg/NeedsArg, avoiding the Go map.
	ProtoCacheBaseAddr() uint32

	// FastCallHitsAddr returns the byte address of the hit-count word for the ④ emitCall guard fast path (PW10
	// zero-crossing ④ verification). The Wasm does i64 ++ on a hit; the Go test reads the word and asserts it
	// together with indirectCalls.
	FastCallHitsAddr() uint32

	// PopErrFrame pops the leftover gibbous callee frame when a call_indirect direct call fails (PW10 R3).
	// A failing callee returns 1 without popping its own frame, and the caller wasm calls this helper to pop it
	// based on status≠0 —— precisely replicating the frame-popping condition of the baseline enterGibbous ERR
	// path (only pops when currentCI is a gibbous frame).
	PopErrFrame()
}

// helperSet holds the injected HostState and provides the Go callbacks registered with wazero.
//
// One per State (private to the single State of the arena/Runtime). The wazero host
// module's callback closures capture helperSet and forward to HostState when invoked.
type helperSet struct {
	host HostState
}

// --- wazero host function callbacks (zero-allocation stack-based, PW10 R3.5) ---
//
// Uses wazero `api.GoFunc` (registered via WithGoFunction) rather than `WithFunc`
// (reflection): the reflection path does make([]reflect.Value) + a per-argument
// reflect.New box on every cross-layer callGoFunc (measured ~14 allocs/call on a
// call-heavy kernel, dominating and degrading the call kernel); the stack-based path
// decodes arguments / writes back results directly from the []uint64, zero reflection
// and zero allocation.
//
// Convention: stack[i] is the raw u64 of the i-th argument (i32 taken via api.DecodeI32
// as the low 32 bits; i64 taken raw); the result is written back to stack[0] (i32 via
// api.EncodeI32; i64 written raw). Argument order = the type parameter order of the
// import declaration in module.go.

// goGetUpval: (base i32, b i32) -> (i64)  type 0 / h_getupval.
func (h *helperSet) goGetUpval(_ context.Context, stack []uint64) {
	base := api.DecodeI32(stack[0])
	b := api.DecodeI32(stack[1])
	stack[0] = h.host.GetUpval(base, b)
}

// goSetUpval: (base i32, b i32, val i64) -> ()  type 1 / h_setupval.
func (h *helperSet) goSetUpval(_ context.Context, stack []uint64) {
	h.host.SetUpval(api.DecodeI32(stack[0]), api.DecodeI32(stack[1]), stack[2])
}

// goReturn: (base i32, pc i32, a i32, b i32) -> (i32)  type 2 / h_return.
func (h *helperSet) goReturn(_ context.Context, stack []uint64) {
	st := h.host.DoReturn(api.DecodeI32(stack[0]), api.DecodeI32(stack[1]), api.DecodeI32(stack[2]), api.DecodeI32(stack[3]))
	stack[0] = api.EncodeI32(st)
}

// goSafepoint: (base i32, pc i32) -> (i32)  type 4 / h_safepoint.
func (h *helperSet) goSafepoint(_ context.Context, stack []uint64) {
	st := h.host.Safepoint(api.DecodeI32(stack[0]), api.DecodeI32(stack[1]))
	stack[0] = api.EncodeI32(st)
}

// goArith: (base,pc,op,b,c,a i32) -> (i32)  type 5 / h_arith.
func (h *helperSet) goArith(_ context.Context, stack []uint64) {
	st := h.host.Arith(api.DecodeI32(stack[0]), api.DecodeI32(stack[1]), api.DecodeI32(stack[2]),
		api.DecodeI32(stack[3]), api.DecodeI32(stack[4]), api.DecodeI32(stack[5]))
	stack[0] = api.EncodeI32(st)
}

// goUnm: (base,pc,b,a i32) -> (i32)  type 6 / h_unm.
func (h *helperSet) goUnm(_ context.Context, stack []uint64) {
	st := h.host.Unm(api.DecodeI32(stack[0]), api.DecodeI32(stack[1]), api.DecodeI32(stack[2]), api.DecodeI32(stack[3]))
	stack[0] = api.EncodeI32(st)
}

// goLen: (base,pc,b,a i32) -> (i32)  type 7 / h_len.
func (h *helperSet) goLen(_ context.Context, stack []uint64) {
	st := h.host.Len(api.DecodeI32(stack[0]), api.DecodeI32(stack[1]), api.DecodeI32(stack[2]), api.DecodeI32(stack[3]))
	stack[0] = api.EncodeI32(st)
}

// goConcat: (base,pc,a,b,c i32) -> (i32)  type 8 / h_concat.
func (h *helperSet) goConcat(_ context.Context, stack []uint64) {
	st := h.host.Concat(api.DecodeI32(stack[0]), api.DecodeI32(stack[1]), api.DecodeI32(stack[2]), api.DecodeI32(stack[3]), api.DecodeI32(stack[4]))
	stack[0] = api.EncodeI32(st)
}

// goCompare: (base,pc,op,b,c i32) -> (i32 packed)  type 8 / h_compare.
func (h *helperSet) goCompare(_ context.Context, stack []uint64) {
	st := h.host.Compare(api.DecodeI32(stack[0]), api.DecodeI32(stack[1]), api.DecodeI32(stack[2]), api.DecodeI32(stack[3]), api.DecodeI32(stack[4]))
	stack[0] = api.EncodeI32(st)
}

// goEq: (base,pc,b,c i32) -> (i32 packed)  type 6 / h_eq.
func (h *helperSet) goEq(_ context.Context, stack []uint64) {
	st := h.host.Eq(api.DecodeI32(stack[0]), api.DecodeI32(stack[1]), api.DecodeI32(stack[2]), api.DecodeI32(stack[3]))
	stack[0] = api.EncodeI32(st)
}

// goForPrep: (base,pc,a i32) -> (i32 status)  type 9 / h_forprep.
func (h *helperSet) goForPrep(_ context.Context, stack []uint64) {
	st := h.host.ForPrep(api.DecodeI32(stack[0]), api.DecodeI32(stack[1]), api.DecodeI32(stack[2]))
	stack[0] = api.EncodeI32(st)
}

// --- PW5 table IC helper callbacks ---

func (h *helperSet) goGetTable(_ context.Context, stack []uint64) {
	st := h.host.GetTable(api.DecodeI32(stack[0]), api.DecodeI32(stack[1]), api.DecodeI32(stack[2]), api.DecodeI32(stack[3]), api.DecodeI32(stack[4]))
	stack[0] = api.EncodeI32(st)
}

func (h *helperSet) goSetTable(_ context.Context, stack []uint64) {
	st := h.host.SetTable(api.DecodeI32(stack[0]), api.DecodeI32(stack[1]), api.DecodeI32(stack[2]), api.DecodeI32(stack[3]), api.DecodeI32(stack[4]))
	stack[0] = api.EncodeI32(st)
}

func (h *helperSet) goGetGlobal(_ context.Context, stack []uint64) {
	st := h.host.DoGetGlobal(api.DecodeI32(stack[0]), api.DecodeI32(stack[1]), api.DecodeI32(stack[2]), api.DecodeI32(stack[3]))
	stack[0] = api.EncodeI32(st)
}

func (h *helperSet) goSetGlobal(_ context.Context, stack []uint64) {
	st := h.host.DoSetGlobal(api.DecodeI32(stack[0]), api.DecodeI32(stack[1]), api.DecodeI32(stack[2]), api.DecodeI32(stack[3]))
	stack[0] = api.EncodeI32(st)
}

func (h *helperSet) goSelf(_ context.Context, stack []uint64) {
	st := h.host.Self(api.DecodeI32(stack[0]), api.DecodeI32(stack[1]), api.DecodeI32(stack[2]), api.DecodeI32(stack[3]), api.DecodeI32(stack[4]))
	stack[0] = api.EncodeI32(st)
}

func (h *helperSet) goNewTable(_ context.Context, stack []uint64) {
	st := h.host.NewTable(api.DecodeI32(stack[0]), api.DecodeI32(stack[1]), api.DecodeI32(stack[2]), api.DecodeI32(stack[3]), api.DecodeI32(stack[4]))
	stack[0] = api.EncodeI32(st)
}

func (h *helperSet) goSetList(_ context.Context, stack []uint64) {
	st := h.host.SetList(api.DecodeI32(stack[0]), api.DecodeI32(stack[1]), api.DecodeI32(stack[2]), api.DecodeI32(stack[3]), api.DecodeI32(stack[4]))
	stack[0] = api.EncodeI32(st)
}

// goCall: (base,pc,a,b,c i32) -> (i64)  type 10 / h_call (returns a 3-state sentinel, raw i64).
func (h *helperSet) goCall(_ context.Context, stack []uint64) {
	ret := h.host.DoCall(api.DecodeI32(stack[0]), api.DecodeI32(stack[1]), api.DecodeI32(stack[2]), api.DecodeI32(stack[3]), api.DecodeI32(stack[4]))
	stack[0] = uint64(ret)
}

func (h *helperSet) goTailCall(_ context.Context, stack []uint64) {
	st := h.host.TailCall(api.DecodeI32(stack[0]), api.DecodeI32(stack[1]), api.DecodeI32(stack[2]), api.DecodeI32(stack[3]), api.DecodeI32(stack[4]))
	stack[0] = api.EncodeI32(st)
}

func (h *helperSet) goClosure(_ context.Context, stack []uint64) {
	st := h.host.Closure(api.DecodeI32(stack[0]), api.DecodeI32(stack[1]), api.DecodeI32(stack[2]), api.DecodeI32(stack[3]))
	stack[0] = api.EncodeI32(st)
}

func (h *helperSet) goClose(_ context.Context, stack []uint64) {
	st := h.host.Close(api.DecodeI32(stack[0]), api.DecodeI32(stack[1]), api.DecodeI32(stack[2]))
	stack[0] = api.EncodeI32(st)
}

// goTForLoop: (base,pc,a,c i32) -> (i64)  type 11 / h_tforloop (raw i64 sentinel).
func (h *helperSet) goTForLoop(_ context.Context, stack []uint64) {
	ret := h.host.TForLoop(api.DecodeI32(stack[0]), api.DecodeI32(stack[1]), api.DecodeI32(stack[2]), api.DecodeI32(stack[3]))
	stack[0] = uint64(ret)
}

// goCallErr: () -> ()  type 12 / h_callerr (PW10 R3: pops the leftover gibbous frame).
func (h *helperSet) goCallErr(_ context.Context, _ []uint64) {
	h.host.PopErrFrame()
}
