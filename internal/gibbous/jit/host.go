//go:build wangshu_p4

package jit

// P4HostState is the minimal abstract interface that the P4 simplified
// form needs to call back into the host (crescent).
//
// **Dependency-cycle break** (per docs/design/p4-method-jit/05-system-pipeline.md
// §4.3 + the same technique as gibbous/wasm/helpers.go::HostState):
// p4Code.Run needs to call the host's DoReturn to pop the frame (because
// the P4 simplified form's mmap segment does not call host helpers
// internally), but the jit package cannot import crescent (that would
// form a cycle). Solution: this interface is implemented by
// crescent.State and injected during wireP4.
//
// **Used by PJ7 wiring**: p4Code holds this interface; after the Run
// caller has finished "the value is written back to R(A)", it calls this
// interface's DoReturn to let the host finish "move results to funcIdx by
// nresults + pop frame + restore caller top" (per the same semantics as
// gibbous_host.go::DoReturn).
//
// **Relation to P3 HostState**: P3 HostState is the wasm helper set
// (GetUpval / SetUpval / DoReturn / Safepoint / Arith / GetTable, ~25
// methods); the P4 simplified form currently needs only the four
// DoReturn / SetReg / GetUpval / Arith (leaving room to extend when the
// arithmetic family / table IC are actually wired in PJ8+).
type P4HostState interface {
	// DoReturn handles P4 frame RETURN A B: return values are written
	// back into the caller's expected slots + pop the frame.
	DoReturn(base int32, pc int32, a int32, b int32) int32

	// SetReg directly writes the current frame's R(idx) slot to val
	// (NaN-boxed u64).
	SetReg(idx int32, val uint64)

	// GetUpval reads upvalue B of the current closure (same semantics as
	// the execute.go GETUPVAL case).
	//
	// Use case: P4 GETUPVAL form — after executing the mmap segment, Run
	// calls this interface to fetch the upvalue value and writes it to
	// R(retA) via SetReg.
	GetUpval(base int32, b int32) uint64

	// SetUpvalFromReg writes the current closure's upvalue b to R(a)
	// (same semantics as the execute.go SETUPVAL case). This interface
	// packs "read R(a) + write upvalue" into an atomic operation, avoiding
	// the need to introduce a general GetReg interface (it equally covers
	// scenarios like NOT/SETUPVAL that need to read a register then write
	// to the host).
	//
	// Differs from gibbous_host.go::State.SetUpval(base, b, val): that
	// signature requires the caller to already hold val, but
	// jit/P4HostState has no GetReg interface to read R(a) — hence this
	// "read-reg-included" dual helper wraps a layer around it. Never raises.
	//
	// Use case: the P4 SETUPVAL A B + RETURN A 1 form
	// (`function(v) upval = v end`).
	SetUpvalFromReg(base int32, a int32, b int32)

	// GetReg reads the NaN-boxed u64 in the current frame's R(idx) slot
	// (for scenarios inside P4 PJ7 where Run needs to read a register to
	// complete a host-dependent operation). Dual to SetReg.
	//
	// Use case: NOT A B + RETURN A 2 (`function(x) return not x end`) —
	// Run needs to read the truthiness of R(B) + SetReg(A,
	// BoolValue(!Truthy(...))).
	GetReg(idx int32) uint64

	// Arith is the arithmetic slow-path (ADD/SUB/MUL/DIV/MOD/POW) helper
	// (same signature as gibbous_host.go::Arith, reused with the P3
	// helper, byte-for-byte isomorphic to the interpreter's doArith).
	//
	// Params:
	//   - base/pc: current frame base byte offset + current pc (materializes
	//     ci.savedPC, same as P3)
	//   - op: bytecode.OpCode value (ADD=12 / SUB=13 / MUL=14 / DIV=15 /
	//     MOD=16 / POW=17 etc.)
	//   - b/c: RK register / constant index (B/C fields passed through)
	//   - a: target R(A) register number (helper writes it via setReg)
	//
	// Returns: 0=OK / 1=ERR (raise pending, picked up and bubbled by
	// enterGibbous).
	//
	// Use case: P4 ADD/SUB/MUL/... form — after executing the mmap
	// segment, Run calls this interface to complete the arithmetic +
	// write R(A), then calls DoReturn to pop the frame.
	Arith(base int32, pc int32, op int32, b int32, c int32, a int32) int32

	// Unm is the unary-minus UNM slow-path helper (same signature as
	// gibbous_host.go::Unm, byte-for-byte isomorphic to the interpreter's
	// UNM slow path: string coercion + __unm metamethod).
	//
	// Params: base/pc as in Arith; b = source register number; a = target
	// register number (R(A)).
	//
	// Returns: 0=OK / 1=ERR. Use case: the P4 UNM A B form.
	Unm(base int32, pc int32, b int32, a int32) int32

	// Len is the length-operation LEN slow-path helper (same signature as
	// gibbous_host.go::Len, byte-for-byte isomorphic to the interpreter's
	// LEN case: string byte length / table border / table __len / error on
	// other types).
	//
	// Params: base/pc as in Arith; b = source register number; a = target
	// register number (R(A)).
	//
	// Returns: 0=OK / 1=ERR. Use case: the P4 LEN A B form.
	Len(base int32, pc int32, b int32, a int32) int32

	// Concat is the string-concatenation CONCAT A B C slow-path helper
	// (same signature as gibbous_host.go::Concat, byte-for-byte isomorphic
	// to the interpreter's CONCAT case: R(A) := R(B) .. R(B+1) .. .. R(C),
	// including the __concat metamethod + mixed number / string concat;
	// may raise).
	//
	// Params: base/pc as in Arith; a = target register number; b/c =
	// CONCAT range endpoints (R(B..C) closed interval); the helper
	// completes the concatenation + setReg via doConcat.
	//
	// Returns: 0=OK / 1=ERR. Use case: the P4 CONCAT A B C form
	// (`function(x, y) return x .. y end` kind).
	Concat(base int32, pc int32, a int32, b int32, c int32) int32

	// Eq is the EQ equality-comparison slow path (same signature as
	// gibbous_host.go::Eq, via the doCompare EQ branch: raw-equality check
	// + __eq metamethod; may raise).
	//
	// Params: base/pc as in Arith; b/c = operands (EQ B C, RK encoding
	// selects register or constant).
	//
	// Returns: packed, bit0 = comparison result (0/1), bit1 = error flag (2).
	Eq(base int32, pc int32, b int32, c int32) int32

	// SetList handles SETLIST A B C (same signature as
	// gibbous_host.go::SetList, byte-for-byte isomorphic to the
	// interpreter's SETLIST case: place R(A+1..A+B) into the array segment
	// of table R(A) starting at position (C-1)*FPF+1; when C=0 the next
	// instruction is the batch number).
	//
	// Params: base/pc as in Arith; a = table register; b = element count;
	// c = batch number (0 means next pc is batch number).
	//
	// Returns: 0=OK / 1=ERR. Use case: P4 `return {1, 2, 3, 4, ...}` and
	// other array literals.
	SetList(base int32, pc int32, a int32, b int32, c int32) int32

	// NewTable handles the NEWTABLE A B C helper (same signature as
	// gibbous_host.go::NewTable; allocation + safepoint entirely inside
	// the helper, never raises — only a Go-side OOM is possible).
	//
	// Params: base/pc as in Arith; a = target register number; b/c = Fb
	// encoding of the initial sizes of the array segment / hash segment
	// (luac hints).
	//
	// Returns: 0=OK / 1=ERR (does not happen in theory, the signature is
	// kept aligned with the other helpers). Use case: the P4 NEWTABLE A B C
	// form (`function() return {} end` kind).
	NewTable(base int32, pc int32, a int32, b int32, c int32) int32

	// GetTable is the GETTABLE A B C slow-path helper (same signature as
	// gibbous_host.go::GetTable, byte-for-byte isomorphic to the
	// interpreter's GETTABLE case: via the icGetTable IC cache hit / hash
	// lookup / __index metamethod chain, may raise: attempt to index nil
	// etc.).
	//
	// Params: base/pc as in Arith; a = target register number; b = register
	// holding the table; c = RK key (register number or constant index).
	//
	// Returns: 0=OK / 1=ERR. Use case: the P4 GETTABLE A B C form
	// (`function(t, k) return t[k] end` / `function(t) return t.x end` kind).
	GetTable(base int32, pc int32, a int32, b int32, c int32) int32

	// SetTable is the SETTABLE A B C slow-path helper (same signature as
	// gibbous_host.go::SetTable, via the icSetTable IC + hash +
	// __newindex metamethod chain, may raise).
	//
	// Params: base/pc as in Arith; a = register holding the table; b/c =
	// RK key / value (register number or constant index).
	//
	// Returns: 0=OK / 1=ERR. Use case: the P4 SETTABLE A B C form
	// (`function(t, k, v) t[k] = v end` / `function(t) t.x = 1 end` kind —
	// the setter form has retB=1).
	SetTable(base int32, pc int32, a int32, b int32, c int32) int32

	// DoGetGlobal is the GETGLOBAL A Bx slow-path helper (same signature
	// as gibbous_host.go::DoGetGlobal, looks up Consts[bx] on the `_G`
	// table via icGetTable, may raise).
	//
	// Params: base/pc as in Arith; a = target register number; bx =
	// constant index (global variable name).
	//
	// Returns: 0=OK / 1=ERR. Use case: the P4 GETGLOBAL A Bx form
	// (`function() return print end` etc.).
	DoGetGlobal(base int32, pc int32, a int32, bx int32) int32

	// DoSetGlobal is the SETGLOBAL A Bx slow-path helper (same signature
	// as gibbous_host.go::DoSetGlobal, writes Consts[bx] = R(a) on the
	// `_G` table via icSetTable, may raise).
	//
	// Params: base/pc as in Arith; a = source register number; bx =
	// constant index (global variable name).
	//
	// Returns: 0=OK / 1=ERR. Use case: the P4 SETGLOBAL A Bx form (setter
	// retB=1).
	DoSetGlobal(base int32, pc int32, a int32, bx int32) int32

	// Compare handles the EQ/LT/LE comparison helper (same signature as
	// gibbous_host.go::Compare, replicates the interpreter's EQ/LT/LE
	// cases via doCompare: string comparison / __lt/__le metamethods).
	//
	// Params: base/pc as in Arith; op = bytecode.OpCode (EQ=23/LT=24/LE=25);
	// b/c = RK register / constant index (B/C fields passed through).
	//
	// Returns: packed - bit0=comparison result (0=false / 1=true),
	// bit1=error flag (2=ERR pending, picked up and bubbled by
	// enterGibbous).
	//
	// Use case: the P4 EQ/LT/LE + JMP + LOADBOOL×2 + RETURN folded form
	// (`function(x) return x == 1 end` kind — folded into a BoolValue
	// written directly to R(A) via packed bit0 vs cmpA).
	Compare(base int32, pc int32, op int32, b int32, c int32) int32

	// ForPrep handles the FORPREP A sBx helper (same signature as
	// gibbous_host.go::ForPrep, three-slot validation + coercion +
	// pre-decrement, reusing the P1 execute.go FORPREP case).
	//
	// Use case: the P4 PJ3 reg-limit FORLOOP form — when the IsNumber
	// guard fails, fall back to host.ForPrep + host.ForLoop (the template
	// deopt path, byte-equal with the interpreter).
	//
	// Params: base/pc as in Arith; a = the A field of FORPREP (R(A)..R(A+2)
	// = init/limit/step three slots).
	//
	// Returns: 0=OK / 1=ERR (raise pending, 'for' init/limit/step must be
	// a number).
	ForPrep(base int32, pc int32, a int32) int32

	// ForLoop runs the empty-body FORLOOP shape to completion (issue #177
	// deopt path). ForPrep has already normalized R(A)/R(A+1)/R(A+2) and
	// pre-decremented R(A); this helper then loops:
	//   idx += step; if !(idx <= limit) break;
	//   preempt(); R(A) = R(A+3) = idx;
	// exactly as the interpreter's execute.go FORLOOP case does. It
	// preserves the observable side effects of the loop iteration count
	// — step budget billing and cancel-context probing — which a direct
	// DoReturn after ForPrep would skip.
	//
	// pc is the FORLOOP opcode's pc (FORPREP pc + 1), used for error
	// line anchoring when preempt raises. a is the FORLOOP A field.
	//
	// Returns: 0=OK / 1=ERR (instruction budget exceeded / context
	// canceled, raised at pc). The shape is empty-body so no reg-K
	// arithmetic runs here; the body-inline variant lives in the
	// analyzeForLoopBodyForm path, not this one.
	ForLoop(base int32, pc int32, a int32) int32

	// LoopPreempt is the HelperLoopFuel dispatcher target (issue #102):
	// an in-segment loop back-edge (FORLOOP / negative-sBx JMP) drained
	// loopFuel to zero. The host bills the spent fuel to the step
	// budget, refills (SegCallFuelBudgeted when a budget/context is
	// armed, SegCallFuelUnlimited otherwise), and runs the standard
	// preemption check — exactly what st.preempt() does on interpreter
	// back-edges. The check must run here (not deferred): a loop whose
	// body never enters a Lua frame has no other preemption point.
	//
	// Returns 0=OK (segment resumes at the back-edge continuation) /
	// 1=ERR ("instruction budget exceeded" or "context canceled"
	// raised, pending on the host).
	LoopPreempt(ctx *JITContext, base int32, pc int32) int32

	// CallBaseline handles the baseline synchronous path of CALL A B C
	// (per docs/design/p4-method-jit/05-system-pipeline.md §4.3,
	// **bypassing the P3 R3 indirect direct-call sentinel protocol** — the
	// simplified version only goes through the baseline doCall dispatch +
	// synchronously drives the callee frame to completion, avoiding the
	// need to introduce an in-segment call_indirect channel.
	//
	// Params base/pc as in Arith; a/b/c are the three CALL A B C fields:
	//   - a = callee function register number (R(A)); arguments from
	//     R(A+1..A+B-1)
	//   - b = argument count + 1 (B=0 means "up to top", B=1 means 0
	//     arguments, B=N means N-1 arguments)
	//   - c = return value count + 1 (C=0 means "up to top", C=1 means 0
	//     return values, C=N means N-1 return values)
	//
	// Returns: 0=OK (callee frame completed + results landed in
	// R(A..A+C-2), caller frame still alive) /
	//      1=ERR (pendingErr set → ERR bubbles up to the caller).
	//
	// **Difference from the P3 wasm-side DoCall**: DoCall returns a
	// tri-state i64 (<0/odd/even) used for wasm-side call_indirect
	// direct-call dispatch; the P4 PJ5 simplified form has no wasm-level
	// in-segment indirect channel, so the host side **must** go through
	// baseline doCall (host/crescent/__call/all-form gibbous run
	// synchronously to completion), not entering the tryIndirectCallee
	// fast path.
	//
	// **Simplified-form use case** (`function(g) g() end` kind): the Run
	// prelude path calls this interface to complete the call + a subsequent
	// DoReturn to pop the frame. byte-equal with the P1 interpreter doCall
	// path.
	CallBaseline(base int32, pc int32, a int32, b int32, c int32) int32

	// TailCall handles the baseline synchronous path of TAILCALL A B C
	// (per docs/design/p4-method-jit/05-system-pipeline.md §4.3 +
	// p1-interpreter/05-interpreter-loop.md §8.4 — tail call reuses the
	// frame + executeFrom synchronously drives the callee chain).
	//
	// Params base/pc as in CallBaseline; a/b/c are the three TAILCALL A B C
	// fields (luac emits C=0):
	//   - a = callee function register number (R(A)); arguments from
	//     R(A+1..A+B-1)
	//   - b = argument count + 1 (B=0 means "up to top", B=1 means 0
	//     arguments, B=N means N-1 arguments)
	//   - c = return value count + 1 (TAILCALL is always C=0, luac forces
	//     writing 0 in stmtReturn to mean "up to top", dovetailing with the
	//     trailing RETURN B=0)
	//
	// Returns (tri-state branch, same as crescent.State.TailCall):
	//   - 0 = Lua tail call completed. The caller frame has been replaced
	//     by the callee frame + executeFrom synchronously drove the callee
	//     chain to completion + nresults written back to the parent funcIdx.
	//     The Run side **skips DoReturn** (this frame is already popped) and
	//     returns 0 directly.
	//   - 1 = ERR (raise pending → ERR bubbles up to the caller).
	//   - 2 = host tail call. Results landed in R(A..A+nrets-1), the G frame
	//     is not popped. The Run side **normally calls DoReturn** (matching
	//     the trailing dead RETURN A B=0 emitted by luac, nret up to top).
	//
	// **Simplified-form use case** (`function(g) return g() end` /
	// `function() return f() end` etc.): the Run prelude path calls this
	// interface to complete + tri-state branch (byte-equal with the P1
	// interpreter doTailCall path).
	TailCall(base int32, pc int32, a int32, b int32, c int32) int32

	// Self handles the SELF A B C helper (same signature as
	// gibbous_host.go::Self, byte-for-byte isomorphic to the interpreter's
	// SELF case: R(A+1)=R(B) self + R(A)=R(B)[RK(C)] method, via the
	// icGetTable IC + hash + __index metamethod chain, may raise: attempt
	// to index nil etc.).
	//
	// Params:
	//   - base/pc: current frame base byte offset + current pc (materializes
	//     ci.savedPC, same as the interpreter)
	//   - a: SELF.A (target registers: method result to R(A), self to R(A+1))
	//   - b: SELF.B (receiver register number 0-255)
	//   - c: SELF.C (RK encoding 0-511, constant offset by 256)
	//
	// Returns: 0=OK / 1=ERR (raise pending, picked up and bubbled by
	// enterGibbous).
	//
	// Use case: the P4 PJ5 SELF + CALL/TAILCALL inline form
	// (`obj:method(args)` kind). The Run prelude path calls host.Self to
	// load method/self, then calls CallBaseline / TailCall to complete the
	// byte-equal P1 doCall dispatch.
	Self(base int32, pc int32, a int32, b int32, c int32) int32

	// Closure handles CLOSURE A Bx (same as gibbous_host.go::Closure).
	// makeClosure consumes upvalue captures from the following pseudo-
	// instructions (MOVE/GETUPVAL at ci.pc), so the helper first sets ci.pc
	// to just after CLOSURE (pc+1).
	//
	// Params: base/pc as in Arith; a = target register number; bx = inner
	// Proto index.
	//
	// Returns: 0=OK / 1=ERR. Use case: P4 `local f = function() ... end`
	// kind.
	Closure(base int32, pc int32, a int32, bx int32) int32

	// Close handles CLOSE A (same as gibbous_host.go::Close): closes all
	// open upvalues ≥ base+A. Never raises.
	//
	// Params: base/pc as in Arith; a = starting register number.
	//
	// Returns: 0=OK (invariant preserved, signature aligned with Arith
	// etc.). Use case: closing upvalues when leaving a block in
	// `do local x = ... end`.
	Close(base int32, pc int32, a int32) int32

	// TForLoop handles TFORLOOP A C (same as gibbous_host.go::TForLoop).
	// Calls the iterator R(A)(R(A+1), R(A+2)) → results R(A+3..A+2+C);
	// continues if the first value is non-nil, exits if nil.
	//
	// Params: base/pc as in Arith; a = iterator register number; c =
	// return value count.
	//
	// Returns (i64, tri-state):
	//   - ≥0 = refreshed byte offset of this frame's base (continue loop)
	//   - -1 = ERR (raise pending)
	//   - -2 = exit (first value nil)
	TForLoop(base int32, pc int32, a int32, c int32) int64

	// GlobalsRaw returns the globals table as a NaN-boxed u64 (same
	// contract as the P3 wasm compiler's use in translate_table.go:
	// the globals table identity is fixed for the State lifetime and
	// arena objects never move, so the GCRef byte offset can be baked
	// into emitted code at compile time). The PJ10 native GETGLOBAL /
	// SETGLOBAL NodeHit inline fast path bakes it as an imm64.
	GlobalsRaw() uint64

	// ArenaBaseAddr returns the uintptr of the start of the arena
	// `[]byte` (per 05 §3.3).	//
	// Use case: the PJ2 full speculative template — the mmap segment reads
	// the arenaBase field via r15+offset, then reads/writes value-stack
	// slots directly with byte-level movsd, skipping the host-interface
	// round trip. The PJ7 simplified form does not call this interface (its
	// mmap segment is a dummy).
	ArenaBaseAddr() uintptr

	// RefreshJitCtxAddrs is a batched setter that populates all five
	// arena-relative address fields on the JIT context in one call:
	// arenaBase, valueStackBase (using the caller's frame R0 byte
	// offset `base`), ciDepthAddr, ciSegBaseAddr, topAddr. This exists
	// because the individual per-field getters each recompute
	// arena.Words() and take unsafe.Pointer of the same []byte, which
	// costs a noticeable ~5-15 ns per boundary-heavy call. Batching
	// them into one host call eliminates the redundant work.
	//
	// Callers (p4Code.Run / PerOpCode.Run / nativeCode.Run) should
	// prefer this over the individual setters. The individual getters
	// stay in the interface for legacy callers and for cases where a
	// caller genuinely needs only one field (rare).
	RefreshJitCtxAddrs(ctx *JITContext, base int32)

	// ValueStackBaseAddr returns the byte address of the current frame's
	// R0 (per 05 §3.3 + 06 §4.1 rbx = valueStackBase).
	//
	// The base param is the current frame's R0 byte offset (per the
	// baseByte = (stackBaseW + ci.base) * 8 computed in enterGibbous, same
	// semantics as the base passed to DoReturn).
	//
	// Returns: arena.Words().bytePtr + base — this is the true byte address
	// of R0 in the Go process's virtual address space. The mmap segment
	// reads r15+offset to get this field, then addresses R(reg) via movsd
	// [valueStackBase + reg*8].
	//
	// **Arena relocation risk**: on an arena grow, Words() reallocates and
	// this field goes stale. Per the 05 §5 arena-base reload protocol: a
	// grow only happens on the allocation slow path (leaving the JIT
	// world); the JIT-inline bump goes out on overflow — after returning,
	// base is reloaded from jitContext. The PJ2 full version wires up this
	// protocol; the PJ7 simplified form does not yet call this interface.
	ValueStackBaseAddr(base int32) uintptr

	// CIDepthHostAddr returns the host byte address of the thread.ciDepth
	// mirror word (per §9.20 Option B Spike 1).
	//
	// **Reuses the P3 PW10 Stage 1a mirror word** (crescent.State.ciDepthRef):
	// the same mirror word is written on the crescent side via setCIDepth,
	// and read / inc / dec on the P4 mmap segment via the host addr
	// (uintptr). Returns = arena.Words().bytePtr + (st.ciDepthRef bytes).
	//
	// **Arena relocation risk**: same as ArenaBaseAddr — after an arena
	// grow leaves the JIT world, base is reloaded from jitContext on
	// return; in the Spike 1 stage it is injected at each Run entry.
	CIDepthHostAddr() uintptr

	// CISegBaseHostAddr returns the host byte address of the mirror word
	// for the CI segment's current byte base (per §9.20).
	//
	// **Reuses the P3 PW10 Stage 2 mirror word** (crescent.State.ciSegBaseRef):
	// the CI segment is relocatable; the mmap segment dereferences this
	// mirror word to get the current CI segment base, then computes the
	// CallInfo[depth] frame address (base + depth*40).
	CISegBaseHostAddr() uintptr

	// TopHostAddr returns the host byte address of the thread.top mirror
	// word (per §9.20).
	//
	// **Reuses the P3 PW10 Stage 1a mirror word** (crescent.State.topRef):
	// top is a stack-slot index, written by the mmap segment when
	// enterLuaFrame sets the callee frame top (top = base + MaxStack).
	TopHostAddr() uintptr

	// ExecuteCalleeFromInlineFrame is the Spike 1 Step C-1 helper API (per
	// §9.20.7 real-wiring breakdown + §9.20.9 trampoline exit-resume
	// protocol commit-2 interface + commit-5l signature fix: callA
	// replaces retA, in the SELF + CALL form the method is at R(callA), and
	// callA is the correct field for identifying the callee slot).
	//
	// **Preconditions** (the caller mmap segment must guarantee):
	//   - the mmap segment's BuildVoid0ArgSkeleton has finished writing the
	//     CallInfo[depth] 5-word fields (word0 is a compile-time placeholder
	//     0, ignored inside the helper which instead reads calleeCI.cl word3
	//     to reverse-look-up the callee Proto; funcIdx is computed from
	//     caller.base + callA)
	//   - the mmap segment's EmitFrameInlineCIDepthInc has done ciDepth++
	//   - the thread.cur field has not been updated by the mmap segment (a
	//     Go-side cold field)
	//
	// **Flow** (corresponding to the crescent.State implementation):
	//   1. read CI[ciDepth-1].cl (the callee closure GCRef loaded by
	//      BuildVoid0Arg LoadClosureGCRef)
	//   2. reverse-look-up the callee Proto: object.ClosureProtoID(cl) →
	//      st.protos[pid]
	//   3. ciDepth-- to cancel the BuildVoid0Arg side effect
	//   4. funcIdx = th.cur.base + callA (caller frame R(callA) = method slot)
	//   5. nargs=1 + nresults=0 (the Spike 1 SELF + CALL 0-user-arg setter
	//      form: SELF already wrote R(callA+1)=self, caller CALL.B=2 = 1
	//      nargs (self only), enterLuaFrame expects nargs=1)
	//   6. nCcalls++/enterLuaFrame/executeFrom/popCallInfo
	//   7. on exit ciDepth++ to balance PopVoid0Arg (commit-5m syncs the Go
	//      field ciDepth from the mirror at entry first, to avoid the mmap
	//      CIDepthInc being out of sync with the Go field)
	//
	// **Returns**: 0=OK (callee completed + return values landed in
	// R(callA..callA+nresults-1)) / 1=ERR (state.pendingErr set, the Run
	// dispatcher takes the error path).
	//
	// **commit-5l signature fix** (per PR review + self-check): the
	// original retA was RETURN.A (always 0 in the setter form), which
	// cannot correctly compute funcIdx; changed to callA which is CALL.A
	// (the method slot position in the SELF + CALL form), aligned with the
	// same semantics as host.CallBaseline.
	//
	// **commit-5p Spike 2 signature extension**: adds the callArgCount
	// param, allowing the N-arg SELF + CALL form (callArgCount=0..7; inside
	// the helper enterLuaFrame nargs = 1+callArgCount = self + N user args).
	//
	// **commit-5q Spike 4 signature extension**: adds the nresults param,
	// allowing the multi-return form (callC=1 → 0-return setter / callC=2 →
	// 1-return getter / callC=3..16 → N=2..15 return, dropping multi-ret;
	// inside the helper enterLuaFrame sets nresults + the callee RETURN
	// doReturn automatically lands R(callA..callA+nresults-1)).
	ExecuteCalleeFromInlineFrame(base int32, callA int32, callArgCount int32, nresults int32) int32

	// ExecutePlainCallInlineFrame is the PJ10 native CALL variant of
	// ExecuteCalleeFromInlineFrame — same shape (mmap segment builds
	// the callee CI slot + increments ciDepth, then exits via
	// HelperExecutePlainCall; helper drives executeFrom + rebalances
	// ciDepth for the segment-side PopFrame), differing only in the
	// caller-side arg convention:
	//
	//   - SELF variant assumes callArgCount = user-arg count and the
	//     helper computes nargs = 1 + callArgCount (self is implicit).
	//   - plain-CALL variant takes nargs directly (matches CALL.B - 1;
	//     B=1 → 0 args, B=N → N-1 args). No implicit self.
	//
	// The helper interprets the segment-written CI slot as-is: word3
	// carries the callee closure GCRef, word0 carries base|funcIdx,
	// word2 carries protoID|nresults. Zero raises are propagated back
	// to Run's Go-side error path via jitCtx.exitReason.
	//
	// Params:
	//
	//   - base:  jitCtx.valueStackBase (caller frame R(0) byte offset).
	//   - callA: CALL.A field — R(callA) held the closure that the
	//     segment reflected into CI[depth].cl.
	//   - nargs: CALL.B - 1 (0..255).
	//   - nresults: CALL.C - 1 (-1 for multret, but the segment guard
	//     rejects multret in the Spike 2 minimal form).
	//
	// Return: 0=OK / 1=ERR (state.pendingErr already set).
	ExecutePlainCallInlineFrame(base int32, callA int32, nargs int32, nresults int32) int32

	// NativeCalleeSegAddr returns the PJ10 native mmap segment entry
	// address for the callee Proto with the given protoID, or 0 if the
	// callee is not native-compiled (issue #50 Spike 5). Used by the
	// CALL IC populate path to record CalleeSegAddr so a future
	// segment-to-segment dispatch can `call` into the callee directly.
	//
	// Only main-thread native callees are eligible (coroutines don't
	// promote); a non-native / disposed callee returns 0 and the fast
	// path stays on the host round trip.
	NativeCalleeSegAddr(protoID uint32) uint64

	// CalleeNeverExitsSegment reports whether the callee Proto's native
	// segment runs start-to-finish without exiting to a Go helper
	// (issue #50 Spike 5). Only such callees are eligible for
	// segment-to-segment dispatch. Returns false for non-native /
	// disposed callees.
	CalleeNeverExitsSegment(protoID uint32) bool

	// CalleeSeg2SegRetCount returns the callee's uniform RETURN value
	// count (every reachable RETURN carries the same B-1), or -1 when
	// sites disagree / multret / non-native (issue #155). The seg2seg
	// populate compares it against the CALL's C-1 before flagging the
	// slot NeverExits: the in-segment teardown moves exactly B-1
	// values with no nil-fill, so a callee that can return fewer
	// values than the caller consumes must stay on the exit-reason
	// path (host.DoReturn nil-fills like the interpreter).
	CalleeSeg2SegRetCount(protoID uint32) int32

	// ObserveCallCallee inspects R(A) at a CALL site and returns a
	// packed observation of the callee's shape. Called by the exit-
	// reason dispatcher just before host.CallBaseline to populate the
	// per-CALL-site inline cache (issue #50 Spike 1). The observation
	// snapshots the callee before CallBaseline overwrites R(A) with
	// return values, so callers can populate the IC after the call
	// succeeds without a second reg read.
	//
	// The returned uint64 packs:
	//
	//	bits  0..31 : protoID (0 if host closure); for a math-intrinsic
	//	              host closure, bits 0..47 carry the closure GCRef
	//	bits 32..39 : numParams
	//	bits 40..47 : maxStack
	//	bits 48..55 : flags — bit0=IsVararg, bit1=NeedsArg, bit2=IsHost
	//	bits 56..63 : math intrinsic kind (Intrinsic*, 0 = none), set only
	//	              alongside IsHost for a recognized intrinsic (issue #77)
	//
	// When R(A) is not a function value the observation returns zero
	// packed (protoID=0 + flags=0); the dispatcher path will hit the
	// host.CallBaseline raise anyway, and the IC populate short-circuits
	// on the same signal. Never raises.
	ObserveCallCallee(base int32, a int32) uint64
}

// SetHostState injects the host (crescent) abstraction into this Compiler.
//
// **per-Compiler singleton** (per the wireP4 call contract): one
// *Compiler per State; this method is called once inside the single wireP4
// goroutine; when a subsequent Compile produces a p4Code, the Compiler's
// hostState is copied into the p4Code field; p4Code.Run uses the hostState
// it holds itself (per-p4Code single writer-then-reader, no concurrent
// write).
//
// This avoids the multi-State concurrent-write race of a package-level
// global hostState (V18 -race friendly, per the
// design-claims-vs-codebase-physics discipline — fix a race each time one
// is found).
func (c *Compiler) SetHostState(h P4HostState) {
	c.hostState = h
}
