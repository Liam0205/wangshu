//go:build wangshu_p4 && amd64

// translator_native.go - CFG-based native code emit path.
//
// This is the "real" PJ10 translator: takes any reducible Proto whose
// opcodes are all in the supported set, walks the CFG, emits per-op
// native amd64 code, and returns a bridge.GibbousCode whose Run just
// jumps into the mmap segment.
//
// vs translator.go (Go-side replay): that path only handles single-BB
// linear head-op shapes (via AnalyzeShape) and dispatches via
// PerOpCode.Run running the mmap `xor eax,eax; ret` stub then replaying
// side effects in Go. This new path handles arbitrary CFG shapes and
// actually executes the native emit bytes.
package peroptranslator

import (
	"errors"
	"fmt"
	"os"
	"unsafe"

	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/gibbous/jit"
	jitamd64 "github.com/Liam0205/wangshu/internal/gibbous/jit/amd64"
	"github.com/Liam0205/wangshu/internal/value"
)

// debugMinimalNative, when set, makes TranslateProtoNative emit only
// `xor eax, eax; ret` (3 bytes) instead of the full op sequence. Used
// to isolate whether a suspected crash is in the emitted bytes or in
// the trampoline entry/exit protocol.
var debugMinimalNative = os.Getenv("PJ10_NATIVE_MINIMAL") != ""

// nativeCode is a bridge.GibbousCode implementation for the native emit
// path. Owns the mmap page and calls into it via CallJITSpec.
type nativeCode struct {
	proto    *bytecode.Proto
	codePage *jitamd64.CodePage
	jitCtx   *jit.JITContext
	host     jit.P4HostState

	// retA / retB / retPC are baked at compile time from the sole
	// RETURN instruction in the Proto's CFG. When the native mmap
	// segment RETs with status 0, Run calls host.DoReturn(retPC, retA,
	// retB) to perform the frame teardown. This avoids emitting a shim
	// call from inside the mmap for RETURN, dodging the morestack /
	// stack unwinder incompatibility (see project memory).
	retA  int32
	retB  int32
	retPC int32
}

func (c *nativeCode) Proto() *bytecode.Proto { return c.proto }

func (c *nativeCode) Run(stack []uint64, base uint32) (status int32) {
	NativeRunCount.Add(1)
	// Defense in depth: if the mmap segment corrupts the Go runtime state
	// enough to trigger a fault on RET, catch it and report an error
	// instead of taking down the host process. This is a stopgap while
	// the root cause of the nested + concurrent crash is being tracked
	// (see [[project-pj10-native-longtask]]).
	defer func() {
		if r := recover(); r != nil {
			if len(stack) > 0 {
				stack[0] = 1
			}
			status = 1
		}
	}()
	if c.codePage == nil || c.jitCtx == nil || c.host == nil {
		if len(stack) > 0 {
			stack[0] = 1
		}
		return 1
	}
	// Refcount acquire, same protocol as p4Code.Run / PerOpCode.Run. See
	// internal/gibbous/jit/amd64/codepage_linux.go for the refcount +
	// deferred munmap rationale. This is the PJ10 native emit main
	// execution path, so the refcount protection is load-bearing for the
	// multi-State Dispose vs Run UAF closure.
	if !c.codePage.Enter() {
		if len(stack) > 0 {
			stack[0] = 1
		}
		return 1
	}
	defer c.codePage.Exit()
	// A1: single batched host call replaces five per-field getters.
	c.host.RefreshJitCtxAddrs(c.jitCtx, int32(base))
	// Snapshot Go G for helper-call safety.
	saveGoG(c.jitCtx.SavedGoGSlot())
	// Install host interface header so shims can reconstruct P4HostState.
	c.jitCtx.SetHostRef(hostIfaceHeader(c.host))

	jitCtxAddr := uintptr(unsafe.Pointer(c.jitCtx))
	vsBaseAddr := c.jitCtx.ValueStackBase()
	rawStatus := jitamd64.CallJITSpec(c.codePage.Addr(), jitCtxAddr, vsBaseAddr)
	// Exit-reason dispatcher loop (issue #38): the mmap segment cannot
	// safely call Go shims for ops like GETTABLE / NEWTABLE etc. under
	// concurrent load, so the emit path lowers those ops to `mov rax,
	// ExitInlineHelper; mov [r15+exitArg0], packed; mov [r15+resumeOff],
	// nextOpOff; ret`. We handle the request Go-side and reenter via
	// codePage + resumeOff. Repeat until the segment returns a
	// non-helper status.
	for uint32(rawStatus) == jit.ExitInlineHelper {
		// HelperReturn terminates the run: multi-return Protos lower
		// each RETURN to this exit-reason so every site carries its
		// own (a, b, pc). DoReturn here replaces both the loop
		// reentry and the single-return Go-side teardown below.
		if arg0 := c.jitCtx.ExitArg0(); arg0&jit.HelperCodeMask == jit.HelperReturn {
			a := int32((arg0 >> 16) & 0xFF)
			b := int32((arg0 >> 24) & 0x1FF)
			pc := int32((arg0 >> 42) & 0x3FFFFF)
			return c.host.DoReturn(int32(base), pc, a, b)
		}
		// Snapshot resumeOff BEFORE dispatching: HelperCall drives the
		// callee synchronously, and a recursive call into this same
		// Proto reenters this same nativeCode and clobbers the shared
		// per-Proto jitCtx (resumeOff / exitArg0 / addr fields).
		resumeOff := c.jitCtx.ResumeOff()
		if !c.dispatchHelper(int32(base)) {
			return 1
		}
		// Arena may have grown during the host call; refresh addr
		// fields before reentering the mmap segment. This also repairs
		// any jitCtx fields a recursive inner Run overwrote.
		c.host.RefreshJitCtxAddrs(c.jitCtx, int32(base))
		saveGoG(c.jitCtx.SavedGoGSlot())
		c.jitCtx.SetHostRef(hostIfaceHeader(c.host))
		vsBaseAddr = c.jitCtx.ValueStackBase()
		resumeAddr := c.codePage.Addr() + uintptr(resumeOff)
		rawStatus = jitamd64.CallJITSpec(resumeAddr, jitCtxAddr, vsBaseAddr)
	}
	status = int32(rawStatus)
	// Perform Go-side frame teardown via host.DoReturn on success.
	// Emitting host.DoReturn as a shim call from inside the mmap segment
	// crashes the Go stack unwinder under nested + concurrent load;
	// doing it here avoids the mmap-to-Go shim call entirely. Only the
	// single-return path reaches here with status 0 — multi-return
	// Protos exit through the HelperReturn branch above.
	if status == 0 {
		if drStatus := c.host.DoReturn(int32(base), c.retPC, c.retA, c.retB); drStatus != 0 {
			status = drStatus
		}
	}
	return status
}

// dispatchHelper (arch-shared) lives in translator_native_dispatch.go.

func (c *nativeCode) PendingErr() error    { return nil }
func (c *nativeCode) Slot() (uint32, bool) { return 0, false }

// IsPJ10Native is a public marker method identifying this GibbousCode
// as the CFG-based PJ10 native emit path (as opposed to PerOpCode
// head-op replay or PJ0-PJ9 shape-spec templates). Callers use it to
// gate behavior that is only safe on the native path — most notably
// the tail-call gibbous dispatch in crescent/execute.go, which
// requires the callee to honor DoReturn's standard frame-teardown
// contract on a reused tail-call frame. PerOpCode's head-op replay
// assumes a fresh frame and doesn't compose with that lifecycle.
func (c *nativeCode) IsPJ10Native() bool { return true }

// Dispose releases the mmap'd code page. Safe to call multiple times
// and safe under concurrent Run in multi-State setups: CodePage.Dispose
// flips a disposed flag (blocking further Enter) and the refcount
// protocol defers the actual unix.Munmap until the last active Run's
// Exit. See internal/gibbous/jit/amd64/codepage_linux.go for the full
// protocol.
//
// Callers (bridge Proto teardown / recompile paths) should invoke this
// when they no longer need the compiled code — otherwise mmap pages
// accumulate for every recompile until process exit.
func (c *nativeCode) Dispose() {
	if c == nil || c.codePage == nil {
		return
	}
	_ = c.codePage.Dispose()
	c.codePage = nil
}

// hostIfaceHeader (arch-shared) lives in translator_native_dispatch.go.

// AnalyzeNative reports whether the native emit path can handle a Proto:
// PreferNative reports whether Compiler should skip shape-spec fast
// paths and route this Proto directly to the native emitter.
//
// Native wins over shape-spec when the shape-spec fast paths can't
// optimize the body: the FORLOOP-with-body spec template only inlines
// 1- or 2-op reg-K bodies (see shapeInfo.hasBody / hasBody2), so a
// FORLOOP kernel with a 3+ op body falls back to per-op replay in
// shape-spec while native emits full inline SSE.
//
// Heuristic: there must be a non-entry reachable BB with >= 4 opcodes.
// "Non-entry" (BB.id != 0) excludes the FORPREP setup block whose ops
// are LOADK init + FORPREP; that block hits 4 ops even for empty for
// loops. Only counting non-entry BBs isolates the loop body, which is
// the shape shape-spec's body-inline template can't beat.
//
// Also require multi-BB CFG: single-BB Protos are the historical
// shape-spec spec-template use case (getter/setter/return-constant
// forms). Routing them to native breaks pre-existing tests that
// assert which spec fast path fires.
func PreferNative(proto *bytecode.Proto) bool {
	if !AnalyzeNative(proto) {
		return false
	}
	c := buildCFG(proto)
	reach := c.reachableBlocks()
	live := 0
	hasBigBodyBB := false
	for id, bb := range c.blocks {
		if !reach[id] {
			continue
		}
		live++
		// Skip entry BB (id 0): it's typically the FORPREP setup with
		// LOADK init + FORPREP terminator, ~4 ops even for empty loops.
		// The loop body BB (id >= 1) is what shape-spec's body-inline
		// template struggles with.
		if id > 0 && bb.endPC-bb.startPC >= 4 {
			hasBigBodyBB = true
		}
	}
	return live >= 2 && hasBigBodyBB
}

// AnalyzeNative reports whether the native emit path can handle a Proto:
func AnalyzeNative(proto *bytecode.Proto) bool {
	if proto == nil || len(proto.Code) == 0 {
		return false
	}
	// F7-a: LOADK for string constants isn't inlined (that would need
	// an arena-relative bake). String constants used by GETGLOBAL /
	// SETGLOBAL / GETTABLE / SETTABLE / SELF go through host shims that
	// read proto.Consts by index — those never touch the mmap segment's
	// LOADK path, so they're fine. Only reject a proto if any live
	// LOADK actually references a string-tagged const.
	stringConst := func(bx int) bool {
		if bx < 0 || bx >= len(proto.StringLitIdx) {
			return false
		}
		return proto.StringLitIdx[bx] >= 0
	}
	// Vararg functions aren't supported (permanent VARARG gate).
	if proto.IsVararg {
		return false
	}
	c := buildCFG(proto)
	if !c.isReducible() {
		return false
	}
	reach := c.reachableBlocks()
	for id, bb := range c.blocks {
		if !reach[id] {
			continue
		}
		for pc := bb.startPC; pc < bb.endPC; pc++ {
			ins := proto.Code[pc]
			op := bytecode.Op(ins)
			if !opSupported(op) {
				return false
			}
			// Arithmetic ops with RK-encoded B or C fall through to a
			// shim call in the current inline fast path; reject Protos
			// that have any such shape until inline RK is supported.
			switch op {
			case bytecode.LOADK:
				// LOADK writes proto.Consts[Bx] into R(A). String consts
				// can't be baked as a raw uint64 immediate (they're
				// arena-relative GCRefs); reject the whole proto if any
				// live LOADK references one.
				if stringConst(bytecode.Bx(ins)) {
					return false
				}
			case bytecode.ADD, bytecode.SUB, bytecode.MUL, bytecode.DIV,
				bytecode.LT, bytecode.LE:
				// Inline arith/compare fast paths require numeric K
				// operands. Reject Protos whose K would fall through.
				if bytecode.B(ins) >= 256 {
					kidx := bytecode.B(ins) - 256
					if kidx < 0 || kidx >= len(proto.Consts) {
						return false
					}
					if !value.IsNumber(value.Value(proto.Consts[kidx])) {
						return false
					}
				}
				if bytecode.C(ins) >= 256 {
					kidx := bytecode.C(ins) - 256
					if kidx < 0 || kidx >= len(proto.Consts) {
						return false
					}
					if !value.IsNumber(value.Value(proto.Consts[kidx])) {
						return false
					}
				}
			case bytecode.EQ:
				// inlineRawEq handles any 64-bit-comparable K (numeric,
				// nil, bool, or interned string). Lua 5.1 EQ on strings
				// is pointer-equal because the frontend interns all
				// string literals; __eq metamethods for tables/userdata
				// are only invoked when types match, so raw ptr-equal
				// masks a metatable dispatch only for those two — which
				// don't appear as K operands. Accept all K here.
				for _, rk := range [2]int{int(bytecode.B(ins)), int(bytecode.C(ins))} {
					if rk >= 256 {
						kidx := rk - 256
						if kidx < 0 || kidx >= len(proto.Consts) {
							return false
						}
					}
				}
			case bytecode.GETTABLE:
				// GETTABLE enters native emit when the IC snapshot says
				// ArrayHit (inline runtime-index fast path) or NodeHit
				// (exit-reason slow path; host.GetTable is byte-equal to
				// the interpreter's IC path). Un-warmed sites (Kind ==
				// None) or meta/megamorphic sites stay on the P1
				// interpreter — those would exit on every access with no
				// inline-arith payoff to amortize the round trip.
				if int(pc) >= len(proto.IC) {
					return false
				}
				if k := proto.IC[pc].Kind; k != bytecode.ICKindArrayHit &&
					k != bytecode.ICKindNodeHit {
					return false
				}
			case bytecode.SETTABLE:
				// SETTABLE mirrors GETTABLE: ArrayHit gets the inline
				// fast path, NodeHit rides the exit-reason slow path
				// (host.SetTable). Other kinds reject.
				if int(pc) >= len(proto.IC) {
					return false
				}
				if k := proto.IC[pc].Kind; k != bytecode.ICKindArrayHit &&
					k != bytecode.ICKindNodeHit {
					return false
				}
			case bytecode.NEWTABLE:
				// NEWTABLE goes through the exit-reason path (host
				// allocates). The emit signature carries B/C as uint8;
				// larger presize hints would truncate, so reject them
				// (semantically harmless but keeps args faithful).
				if bytecode.B(ins) >= 256 || bytecode.C(ins) >= 256 {
					return false
				}
			case bytecode.GETGLOBAL, bytecode.SETGLOBAL:
				// GETGLOBAL/SETGLOBAL only enter native emit when the
				// IC snapshot says NodeHit — the inline fast path is a
				// gen check + fixed node slot access (globals table
				// identity and key are compile-time constants). An
				// un-warmed site would pay a mmap<->Go exit-reason
				// round trip per access, which loses to shape-spec /
				// interpreter (the earlier acceptance without this
				// gate regressed Transform CallInto by ~14%).
				if int(pc) >= len(proto.IC) {
					return false
				}
				if proto.IC[pc].Kind != bytecode.ICKindNodeHit {
					return false
				}
			case bytecode.CALL:
				// CALL goes through the exit-reason path; the dispatcher
				// runs host.CallBaseline which drives the callee to
				// completion synchronously. B=0 (args to top) and C=0
				// (multret) depend on a live `top` the native segment
				// doesn't maintain per-op — reject those forms.
				if bytecode.B(ins) == 0 || bytecode.C(ins) == 0 {
					return false
				}
			}
		}
	}
	// Count reachable RETURNs: single-return Protos use the fast
	// `xor eax, eax; ret` exit + Go-side DoReturn(retA/retB/retPC);
	// multi-return Protos lower each RETURN to a HelperReturn
	// exit-reason (TranslateProtoNative sets codeBufProto.MultiReturn).
	// Zero reachable RETURNs would leave Run without a teardown path.
	//
	// CALL density gate: every CALL lowers to an exit-reason round trip
	// (mmap RET -> Go dispatch -> host.CallBaseline -> mmap reentry),
	// which costs roughly 15-25 interpreted ops. Protos whose bodies
	// are dominated by CALLs (recursive fib, tree builders) run slower
	// on the native path than on the interpreter — measured: fib 11ms
	// interp vs 18ms native. Require enough non-CALL work per CALL to
	// amortize the round trip.
	returnCount := 0
	callCount := 0
	totalOps := 0
	for id, bb := range c.blocks {
		if !reach[id] {
			continue
		}
		for pc := bb.startPC; pc < bb.endPC; pc++ {
			totalOps++
			switch bytecode.Op(proto.Code[pc]) {
			case bytecode.RETURN:
				returnCount++
			case bytecode.CALL:
				callCount++
			}
		}
	}
	if returnCount == 0 {
		return false
	}
	if callCount > 0 && totalOps/callCount < 16 {
		return false
	}
	return true
}

// opSupported reports whether a given op is emit-covered by the native
// path.
//
// **Ultra-conservative gate for production wiring**: only ops that have
// NO shim call in their emitted sequence. Calling Go helpers from mmap
// crashes Go's stack unwinder under nested/concurrent load, so we avoid
// it entirely by:
//   - RETURN: emit `xor eax, eax; ret` inline; host.DoReturn is invoked
//     from Go side after CallJITSpec returns.
//   - Arithmetic: inline SSE fast path (inlineArithSSE) supports
//     reg-reg / reg-K / K-reg / K-K when the K operand is numeric.
//   - Compare: inline UCOMISd fast path for LT/LE with numeric operands;
//     inline raw 64-bit bit-equal for EQ (reg-reg only — AnalyzeNative
//     rejects K operands to dodge the arena-relative string const path).
//   - TEST / TESTSET: inline compare against Nil / False imm64 constants
//     with rel8 forward branches to a notTruthy label.
//   - FORPREP: inline `R(A) -= R(A+2); jmp FORLOOP` (assumes three slots
//     are numbers, matching the FORLOOP inline SSE fast path).
//
// Currently enabled with NO shim call in the emit output:
//
//	MOVE, LOADK (numeric consts only), LOADBOOL, LOADNIL,
//	ADD/SUB/MUL/DIV (inline SSE + IsNumber guards + NaN result guard),
//	NOT, UNM (inline sign-flip with IsNumber guard),
//	EQ (raw 64-bit cmp), LT/LE (inline UCOMISd + IsNumber guards),
//	TEST, TESTSET (inline Nil/False bit-compare),
//	JMP, FORPREP, FORLOOP,
//	GETTABLE/SETTABLE (IC ArrayHit inline / NodeHit exit-reason),
//	NEWTABLE, GETUPVAL, SETUPVAL, CALL (exit-reason dispatch),
//	RETURN (Go-side DoReturn after segment RET)
//
// **Excluded**:
//
//	LEN, CONCAT, SELF, SETLIST, TAILCALL, CLOSURE, CLOSE, TFORLOOP,
//	MOD, POW (no inline emit yet), and
//	GETGLOBAL/SETGLOBAL (exit-reason emit exists but acceptance routed
//	shape-spec-friendly protos into slower per-access round trips; a
//	per-site heuristic is needed before re-enabling)
//
// AnalyzeNative additionally rejects Protos with non-numeric K operands
// on inline arithmetic / compare ops, CALL with B=0/C=0, NEWTABLE with
// B/C >= 256, and GETTABLE/SETTABLE sites without ArrayHit/NodeHit IC.
func opSupported(op bytecode.OpCode) bool {
	switch op {
	case bytecode.MOVE, bytecode.LOADK, bytecode.LOADBOOL, bytecode.LOADNIL,
		bytecode.ADD, bytecode.SUB, bytecode.MUL, bytecode.DIV,
		bytecode.NOT, bytecode.UNM,
		bytecode.EQ, bytecode.LT, bytecode.LE,
		bytecode.TEST, bytecode.TESTSET,
		bytecode.JMP, bytecode.FORPREP, bytecode.FORLOOP,
		bytecode.GETTABLE, bytecode.SETTABLE, bytecode.NEWTABLE,
		bytecode.GETUPVAL, bytecode.SETUPVAL,
		bytecode.GETGLOBAL, bytecode.SETGLOBAL,
		bytecode.CALL,
		bytecode.RETURN:
		return true
	default:
		return false
	}
}

// TranslateProtoNative is the entry called from init() to route native
// compile requests through the CFG + emit pipeline.
func TranslateProtoNative(proto *bytecode.Proto, host jit.P4HostState) (*nativeCode, error) {
	if host == nil {
		return nil, errors.New("peroptranslator: nil P4HostState")
	}
	if !AnalyzeNative(proto) {
		return nil, errors.New("peroptranslator: proto not supported by native path")
	}
	c := buildCFG(proto)
	buf := newCodeBuf(len(c.blocks))
	// Build a raw uint64 constant table so LOADK can bake the immediate
	// directly. Non-string constants (numbers, bool, nil) are already
	// nan-boxed in proto.Consts. AnalyzeNative rejected string consts.
	consts := make([]uint64, len(proto.Consts))
	for i, v := range proto.Consts {
		consts[i] = uint64(v)
	}
	// Snapshot Proto.IC into codeBufProto for the GETTABLE inline
	// fast path (B4). P1 may still be writing IC concurrently, so we
	// read each field with atomic loads (same protocol as P3 wasm's
	// snapshotICSlot) to keep `go test -race` quiet. Stale reads fall
	// through the runtime guards to the shim, byte-equal to P1.
	icSnap := snapshotProtoIC(proto)
	buf.proto = &codeBufProto{Consts: consts, IC: icSnap}
	// Bake the globals table byte offset for the GETGLOBAL / SETGLOBAL
	// NodeHit inline fast path (same identity-is-stable contract as P3
	// wasm's emitGetGlobal).
	buf.proto.GlobalsTaddr = uint32(host.GlobalsRaw() & 0x0000_FFFF_FFFF_FFFF)
	// Multi-return detection: count reachable RETURNs; more than one
	// switches emitTerminator to the HelperReturn exit-reason lowering
	// (each site carries its own a/b/pc instead of the single stashed
	// retA/retB/retPC).
	{
		reach := c.reachableBlocks()
		returns := 0
		for id, bb := range c.blocks {
			if !reach[id] {
				continue
			}
			for pc := bb.startPC; pc < bb.endPC; pc++ {
				if bytecode.Op(proto.Code[pc]) == bytecode.RETURN {
					returns++
				}
			}
		}
		buf.proto.MultiReturn = returns > 1
	}

	// DEBUG: emit just `xor eax, eax; ret` to isolate whether the crash
	// is in the mmap segment content or the trampoline entry/exit.
	if debugMinimalNative {
		buf.emit([]byte{0x31, 0xC0, 0xC3})
		page, err := jitamd64.MmapCode(buf.bytes)
		if err != nil {
			return nil, err
		}
		return &nativeCode{
			proto:    proto,
			codePage: page,
			jitCtx:   jit.NewJITContext(),
			host:     host,
		}, nil
	}

	// Emit a prologue that initializes RBX = vsBase from jitCtx. This
	// makes the mmap segment self-contained: it can be called by any
	// trampoline (CallJITSpec or CallJITFull) as long as R15 = jitCtx.
	// The prologue is the first instructions before BB 0.
	//
	// mov rbx, [r15 + valueStackBaseOff]  (7 bytes)
	buf.emit([]byte{0x49, 0x8B, 0x9F,
		byte(jit.JITContextValueStackBaseOffset),
		byte(jit.JITContextValueStackBaseOffset >> 8),
		byte(jit.JITContextValueStackBaseOffset >> 16),
		byte(jit.JITContextValueStackBaseOffset >> 24)})

	// Emit each BB in id order (which happens to be startPC order thanks
	// to buildCFG's leader sort). This is not the same as rPostOrder but
	// works for reducible CFGs where the entry is BB 0.
	reach := c.reachableBlocks()
	for id, bb := range c.blocks {
		if !reach[id] {
			continue
		}
		if err := buf.bindLabel(id); err != nil {
			return nil, fmt.Errorf("bindLabel BB %d: %w", id, err)
		}
		if err := emitBB(buf, c, bb, id); err != nil {
			return nil, fmt.Errorf("emit BB %d: %w", id, err)
		}
	}

	if err := buf.resolveLabels(); err != nil {
		return nil, fmt.Errorf("resolveLabels: %w", err)
	}

	page, err := jitamd64.MmapCode(buf.bytes)
	if err != nil {
		return nil, err
	}
	NativeCompileCount.Add(1)
	return &nativeCode{
		proto:    proto,
		codePage: page,
		jitCtx:   jit.NewJITContext(),
		host:     host,
		retA:     buf.proto.RetA,
		retB:     buf.proto.RetB,
		retPC:    buf.proto.RetPC,
	}, nil
}

// emitBB emits one basic block: linear ops via emit_amd64.go /
// emit_ops_amd64.go, then the terminator with successor BB fixups.
func emitBB(buf *codeBuf, c *cfg, bb *basicBlock, bbID int) error {
	code := c.proto.Code
	lastPC := bb.endPC - 1
	if lastPC < bb.startPC {
		return nil
	}
	// Emit straight-line prefix (all instructions except the terminator).
	for pc := bb.startPC; pc < lastPC; pc++ {
		if err := emitLinearOp(buf, code[pc], pc); err != nil {
			return err
		}
	}
	// Emit terminator with successor edges.
	termIns := code[lastPC]
	return emitTerminator(buf, c, bb, bbID, termIns, lastPC)
}

// emitResumePreludeIfPending emits a `mov rbx, [r15+vsBaseOff]` reload
// and resolves all pending resume-off fixups to point at the reload
// instruction, if any exit-reason emit is waiting. Safe no-op when
// nothing pends. Called at the start of every emitLinearOp /
// emitTerminator so the resume entry always begins with rbx = vsBase
// (dispatcher may have refreshed it via arena grow).
func emitResumePreludeIfPending(buf *codeBuf) {
	if len(buf.pendingResumeOffFixups) == 0 {
		return
	}
	resumeOff := uint32(buf.pos())
	// mov rbx, [r15 + vsBaseOff] (7B: 49 8B 9F disp32)
	off := int32(jit.JITContextValueStackBaseOffset)
	buf.emit([]byte{0x49, 0x8B, 0x9F,
		byte(uint32(off)),
		byte(uint32(off) >> 8),
		byte(uint32(off) >> 16),
		byte(uint32(off) >> 24)})
	for _, po := range buf.pendingResumeOffFixups {
		buf.bytes[po] = byte(resumeOff)
		buf.bytes[po+1] = byte(resumeOff >> 8)
		buf.bytes[po+2] = byte(resumeOff >> 16)
		buf.bytes[po+3] = byte(resumeOff >> 24)
	}
	buf.pendingResumeOffFixups = buf.pendingResumeOffFixups[:0]
}

// emitLinearOp emits one non-terminator opcode.
func emitLinearOp(buf *codeBuf, ins bytecode.Instruction, pc int32) error {
	emitResumePreludeIfPending(buf)
	op := bytecode.Op(ins)
	a := uint8(bytecode.A(ins))
	// b and c may be RK-encoded (0..511 range) for arithmetic /
	// comparison / table ops - keep them as int, not uint8.
	bReg := uint8(bytecode.B(ins))
	cReg := uint8(bytecode.C(ins))
	bRK := bytecode.B(ins)
	cRK := bytecode.C(ins)
	bx := bytecode.Bx(ins)

	switch op {
	case bytecode.MOVE:
		emitMOVE(buf, a, bReg)
	case bytecode.LOADK:
		if buf.proto == nil || int(bx) >= len(buf.proto.Consts) {
			return fmt.Errorf("LOADK: const idx %d out of range", bx)
		}
		emitLOADK(buf, a, buf.proto.Consts[bx])
	case bytecode.LOADBOOL:
		emitLOADBOOL_valueOnly(buf, a, bReg)
	case bytecode.LOADNIL:
		emitLOADNIL(buf, a, bReg)
	case bytecode.GETUPVAL:
		emitGETUPVAL(buf, a, bReg)
	case bytecode.SETUPVAL:
		emitSETUPVAL(buf, a, bReg)
	case bytecode.GETGLOBAL:
		emitGETGLOBAL(buf, pc, a, uint16(bx))
	case bytecode.SETGLOBAL:
		emitSETGLOBAL(buf, pc, a, uint16(bx))
	case bytecode.GETTABLE:
		emitGETTABLE(buf, pc, a, bReg, cRK)
	case bytecode.SETTABLE:
		emitSETTABLE(buf, pc, a, bRK, cRK)
	case bytecode.NEWTABLE:
		emitNEWTABLE(buf, pc, a, bReg, cReg)
	case bytecode.SELF:
		emitSELF(buf, pc, a, bReg, cRK)
	case bytecode.ADD, bytecode.SUB, bytecode.MUL, bytecode.DIV,
		bytecode.MOD, bytecode.POW:
		emitARITH(buf, op, pc, a, bRK, cRK)
	case bytecode.UNM:
		emitUNM(buf, pc, a, bReg)
	case bytecode.NOT:
		emitNOT(buf, a, bReg)
	case bytecode.LEN:
		emitLEN(buf, pc, a, bReg)
	case bytecode.CONCAT:
		emitCONCAT(buf, pc, a, bReg, cReg)
	case bytecode.CALL:
		emitCALL(buf, pc, a, bReg, cReg)
	case bytecode.CLOSURE:
		emitCLOSURE(buf, pc, a, uint16(bx))
	case bytecode.CLOSE:
		emitCLOSE(buf, pc, a)
	case bytecode.SETLIST:
		emitSETLIST(buf, pc, a, bReg, cReg)
	default:
		return fmt.Errorf("emitLinearOp: unsupported op %v at pc %d", op, pc)
	}
	return nil
}

// emitTerminator emits the last instruction of a BB with the appropriate
// branching / return / call semantics.
func emitTerminator(buf *codeBuf, c *cfg, bb *basicBlock, bbID int, ins bytecode.Instruction, pc int32) error {
	emitResumePreludeIfPending(buf)
	op := bytecode.Op(ins)
	a := uint8(bytecode.A(ins))
	b := uint8(bytecode.B(ins))
	cc := uint8(bytecode.C(ins))
	bRK := bytecode.B(ins)
	cRK := bytecode.C(ins)

	switch op {
	case bytecode.RETURN:
		cb := buf
		if cb.proto != nil && cb.proto.MultiReturn {
			// Multi-return Proto: each RETURN site packs its own
			// (a, b, pc) into a HelperReturn exit-reason. Run's
			// dispatcher calls host.DoReturn and terminates (no
			// reentry), so no resumeOff patching is needed — but
			// emitExitReason marks one anyway; drop it right after
			// since no next op will bind it.
			emitExitReason(cb, jit.HelperReturn, pc, int32(a), int32(b), 0)
			cb.pendingResumeOffFixups = cb.pendingResumeOffFixups[:0]
			break
		}
		// Emit `xor eax, eax; ret` inline - no shim call. host.DoReturn
		// is invoked from nativeCode.Run's Go side after CallJITSpec
		// returns. This avoids all shim-from-mmap risk for the RETURN
		// path (which is at the end of every function).
		// Stash retA/retB/retPC on the codeBuf so TranslateProtoNative
		// can lift them into nativeCode fields.
		if cb.proto != nil {
			cb.proto.RetA = int32(a)
			cb.proto.RetB = int32(b)
			cb.proto.RetPC = pc
		}
		cb.emit([]byte{0x31, 0xC0, 0xC3}) // xor eax, eax; ret
	case bytecode.TAILCALL:
		emitTAILCALL(buf, pc, a, b, cc)
		emitRet(buf)
	case bytecode.JMP:
		if len(bb.succs) != 1 {
			return fmt.Errorf("JMP with %d succs at pc %d", len(bb.succs), pc)
		}
		emitJMP(buf, bb.succs[0])
	case bytecode.FORPREP:
		if len(bb.succs) != 1 {
			return fmt.Errorf("FORPREP with %d succs at pc %d", len(bb.succs), pc)
		}
		emitFORPREP(buf, pc, a, bb.succs[0])
	case bytecode.FORLOOP:
		if len(bb.succs) != 2 {
			return fmt.Errorf("FORLOOP with %d succs at pc %d", len(bb.succs), pc)
		}
		// succs[0] = back-edge target, succs[1] = fall-out.
		emitFORLOOP(buf, a, bb.succs[0], bb.succs[1])
	case bytecode.TFORLOOP:
		if len(bb.succs) != 2 {
			return fmt.Errorf("TFORLOOP with %d succs at pc %d", len(bb.succs), pc)
		}
		// succs[0] = pc+1 = fall-out; succs[1] = pc+2 = ... actually
		// for TFORLOOP: fall-through means jump-back; pc++ means exit.
		// Linksuccs adds pc+1 then pc+2, so succs[0]=back, succs[1]=out.
		emitTFORLOOP(buf, pc, a, cc, bb.succs[0], bb.succs[1])
	case bytecode.EQ, bytecode.LT, bytecode.LE:
		if len(bb.succs) != 2 {
			return fmt.Errorf("%v with %d succs at pc %d", op, len(bb.succs), pc)
		}
		// linkSuccs: succs[0] = pc+1 (execute JMP), succs[1] = pc+2 (skip).
		emitCompare(buf, op, pc, a, bRK, cRK, bb.succs[0], bb.succs[1])
	case bytecode.TEST:
		if len(bb.succs) != 2 {
			return fmt.Errorf("TEST with %d succs at pc %d", len(bb.succs), pc)
		}
		emitTEST(buf, a, cc, bb.succs[0], bb.succs[1])
	case bytecode.TESTSET:
		if len(bb.succs) != 2 {
			return fmt.Errorf("TESTSET with %d succs at pc %d", len(bb.succs), pc)
		}
		emitTESTSET(buf, a, b, cc, bb.succs[0], bb.succs[1])
	case bytecode.LOADBOOL:
		// LOADBOOL emits the value; the CFG makes it a terminator when C
		// determines the successor edge.
		emitLOADBOOL_valueOnly(buf, a, b)
		if len(bb.succs) != 1 {
			return fmt.Errorf("LOADBOOL terminator with %d succs at pc %d", len(bb.succs), pc)
		}
		emitJMP(buf, bb.succs[0])
	default:
		// Non-terminator that happens to sit at BB end (rare): just
		// emit as linear and add a jmp to fall-through if applicable.
		if err := emitLinearOp(buf, ins, pc); err != nil {
			return err
		}
		if len(bb.succs) == 1 {
			emitJMP(buf, bb.succs[0])
		}
	}
	return nil
}

// --- Constant table plumbing ---
//
// See codeBuf.proto (in codebuf.go). TranslateProtoNative wires the
// consts table in before emit; emitLinearOp for LOADK consults it.
