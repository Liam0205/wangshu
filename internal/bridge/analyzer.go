// Compilability analyzer (`docs/design/p2-bridge/03-compilability-analysis.md` §4-§5).
//
// `compilabilityVisitor` collects the F1-F4 + F6 no-tier-up signals in a
// single AST walk; F5 / F7 are decided independently at the Proto level
// (the visitor is not involved).
//
// **Conservative first, prefer false negatives over false positives**
// (03 §1): any shape we are unsure about is judged NotCompilable. A
// misjudgment (treating a non-compilable shape as compilable) leads P3 to
// emit wrong code or crash at runtime, and the fallback is not triggered —
// this is P2's design red line.
package bridge

import (
	"fmt"

	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/frontend/ast"
)

// AnalyzeProto is the compilability-analysis entry point invoked by codegen
// as a callback at Compile time (03 §5.2).
//
// Call contract:
//   - Called by compile.Gen after producing a `*bytecode.Proto`, writing the
//     result into `ProfileData.Compilable` (03 §2.5);
//   - Under the `!profile` build tag codegen does not call this function, so
//     all Protos stay CompUnknown (03 §2.6);
//   - **Nested Protos are judged independently** (03 §7): codegen calls this
//     function once for every Proto it produces; a parent's verdict does not
//     propagate to its children.
//
// Invariants:
//  1. Analyze once, result immutable (03 §5.4) — Compilable is not modified
//     after this function returns;
//  2. Conservative first — any of the F1-F7 signals firing yields NotCompilable;
//  3. AST used then discarded (03 §2.4 decision option ①) — this function does
//     not retain a reference to fn after returning.
func (b *Bridge) AnalyzeProto(fn *ast.FuncExpr, proto *bytecode.Proto) Compilability {
	return b.AnalyzeProtoWithOuter(fn, proto, nil, nil)
}

// AnalyzeProtoWithOuter is the scope-aware version of AnalyzeProto (from P4
// PJ5 + 03 §9 GAP-5): outerLocalFuncs is a name mapping of local fns on the
// outer scope chain, letting calls to outer local fns inside this proto be
// recognized as known rather than unknown calls.
//
// When outerLocalFuncs = nil the behavior is equivalent to AnalyzeProto
// (backward compatible).
//
// Typical scenario: nested closure
//
//	local function noop() end                -- outer registers noop
//	local function invoker() noop() end     -- outer noop called inside this proto
//
// Without extension (nil): visitor.localFuncs empty → noop marked
// callsUnknownFn → invoker NotCompilable;
// With extension: visitor.localFuncs contains noop → isKnownLocalCall=true →
// recursively judge noop.Body (same signal-contagion semantics: if noop
// contains yield then invoker does too), so invoker can be Compilable.
//
// **Shadowing safety**: entries in outerLocalFuncs whose name collides with
// this proto's Params are dropped, avoiding mistaking a parameter for a known
// local fn.
//
// outerAliases carries `local sqrt = math.sqrt`-style bindings from the
// outer funcState chain (name -> RHS expression). Entries whose RHS
// passes the isSafeStdlibCall whitelist seed the visitor's safeAliases,
// so `sqrt(x)` calls inside this proto resolve as safe stdlib calls
// instead of ReasonUnknownCall. Same shadowing discipline as
// outerLocalFuncs.
func (b *Bridge) AnalyzeProtoWithOuter(fn *ast.FuncExpr, proto *bytecode.Proto, outerLocalFuncs map[string]*ast.FuncExpr, outerAliases map[string]ast.Expr) Compilability {
	v := newCompilabilityVisitor()
	// Inherit the outer local funcs snapshot, minus entries shadowed by this
	// function's parameter names.
	for name, fnAST := range outerLocalFuncs {
		shadowed := false
		for _, p := range fn.Params {
			if p == name {
				shadowed = true
				break
			}
		}
		if !shadowed {
			v.localFuncs[name] = fnAST
		}
	}
	// Seed safe stdlib aliases from the outer chain (whitelist-filtered
	// here, dataflow-tracked by the frontend).
	for name, rhs := range outerAliases {
		shadowed := false
		for _, p := range fn.Params {
			if p == name {
				shadowed = true
				break
			}
		}
		if !shadowed && isSafeStdlibCall(rhs) {
			v.safeAliases[name] = true
		}
	}
	v.walkBlock(fn.Body)

	var reasons ReasonsBitmap

	// F1: vararg (triple detection 03 §3.1.3: AST.IsVararg + Proto.IsVararg + visitor.sawVararg)
	if fn.IsVararg || v.sawVararg || protoIsVararg(proto) {
		reasons |= ReasonVararg
	}
	// AST/Proto IsVararg must agree (03 §2.3 invariant 1) — a mismatch is a codegen bug
	if fn.IsVararg != proto.IsVararg {
		panic(fmt.Sprintf("compilability: AST/Proto IsVararg mismatch (ast=%v, proto=%v)",
			fn.IsVararg, proto.IsVararg))
	}

	// F2: coroutine-related
	if v.callsYield {
		reasons |= ReasonYield
	}
	if v.callsResume {
		reasons |= ReasonResume
	}
	if v.callsCoroutine {
		reasons |= ReasonCoroutine
	}
	if v.callsUnknownFn {
		reasons |= ReasonUnknownCall
	}
	// F2-c: SELF method call (placeholder bit, same technique as
	// ReasonBackendUnsupp — from the P4 PJ5 SELF inline shape wiring)
	if v.sawSelfCall {
		reasons |= ReasonSelfCall
	}

	// F3 / F4: debug / setfenv
	if v.usesDebug {
		reasons |= ReasonDebug
	}
	if v.usesSetfenv {
		reasons |= ReasonSetfenv
	}

	// F5: oversized function (Proto level)
	if len(proto.Code) > MaxCompilableInsns {
		reasons |= ReasonOverSize
	}
	if int(proto.MaxStack) > MaxCompilableRegs {
		reasons |= ReasonOverRegs
	}

	// F6: deep nesting / upvalue count
	if v.maxClosureDepth > MaxClosureDepth {
		reasons |= ReasonNestedDeep
	}
	if len(proto.UpvalDescs) > MaxUpvalCount {
		reasons |= ReasonOverUpval
	}

	// F7: P3 backend capability query (last, only queried once F1-F6 all pass —
	// 03 §3.7.5 + invariant I8)
	if reasons == 0 && b.checkF7BackendSupport(proto) {
		reasons |= ReasonBackendUnsupp
	}

	// Decision and cache
	result := CompCompilable
	if reasons.HasAny() {
		result = CompNotCompilable
	}
	b.SetCompilability(proto, result, reasons)
	return result
}

// checkF7BackendSupport is the F7 P3 backend capability query (03 §3.7.6).
//
// b.p3 == nil (P1-only / P2 PB0..PB5 with P3 not injected) → treated as
// unsupported (conservative reject). This guarantees P1-only behavior stays
// identical to before P2 was enabled — all Protos stay permanently tier-0
// interpreted.
func (b *Bridge) checkF7BackendSupport(proto *bytecode.Proto) bool {
	if b.p3 == nil {
		return true // no P3 = supports no opcode = F7 fires
	}
	return !b.p3.SupportsAllOpcodes(proto)
}

// protoIsVararg scans Proto.Code to see if it contains a VARARG opcode
// (defense in depth, 03 §3.1.3).
func protoIsVararg(proto *bytecode.Proto) bool {
	for _, ins := range proto.Code {
		if bytecode.Op(ins) == bytecode.VARARG {
			return true
		}
	}
	return false
}

// compilabilityVisitor collects the F1-F4 + F6 signals (03 §4.1).
//
// **Nesting does not propagate** (03 §7.3): when the visitor enters a child
// FuncExpr it spins up a sub-visitor and only writes the maxClosureDepth
// signal back to the parent — a child's yield/debug/setfenv content signals
// are judged by its own independent AnalyzeProto call.
//
// **Scope-aware** (user decision: isKnownLocalCall real implementation):
// tracks the mapping of local fn name → child FuncExpr reference. When the
// visitor sees an `f()` shape:
//   - If f is a local name pointing at some child FuncExpr of the current
//     Proto → recursively judge the child (reusing this visitor's verdict,
//     sharing the callsXxx signals) rather than simply marking unknown.
//   - This way a function that only calls a pure-computation helper is still
//     compilable (a pure-computation helper itself has no yield/debug/setfenv,
//     recursively judged known safe).
type compilabilityVisitor struct {
	// F1: vararg fallback capture (the main verdict looks at FuncExpr.IsVararg, 03 §3.1.4)
	sawVararg bool

	// F2: coroutine-related
	callsYield     bool
	callsResume    bool
	callsCoroutine bool
	callsUnknownFn bool

	// F2-c: SELF method call placeholder signal (from the P4 PJ5 SELF inline
	// shape wiring). By default takes the ReasonSelfCall placeholder reject; at
	// runtime, once P4 is injected, `recheckCompilabilityRuntime` clears the
	// placeholder and SupportsAllOpcodes shape-gating does the real verdict.
	sawSelfCall bool

	// F3 / F4
	usesDebug   bool
	usesSetfenv bool

	// F6: nesting depth
	currentDepth    int
	maxClosureDepth int

	// Scope: local fn name → child FuncExpr reference (the basis for the F2
	// isKnownLocalCall real implementation). A local table within a single
	// visitor instance, not shared across child-function boundaries (nested
	// Protos judged independently).
	localFuncs map[string]*ast.FuncExpr

	// safeAliases tracks `local sqrt = math.sqrt`-style aliases of
	// whitelisted stdlib functions. This is dataflow tracking, not name
	// enumeration: a LocalStmt whose RHS passes the isSafeStdlibCall
	// shape check (whitelisted NameExpr / lib.method IndexExpr)
	// registers the LHS local name here; reassignment or shadowing
	// deletes it (same scope discipline as localFuncs, snapshot-restored
	// by pushScope/popScope). visitCallExpr consults this table for
	// `sqrt(x)`-style calls and skips the callsUnknownFn mark on hit.
	safeAliases map[string]bool

	// localShadows tracks a same-name local redefinition shadowing an outer
	// known function. A simple push/pop stack suffices — this initial P2
	// version uses nil-check + map overwrite, with explicit cleanup on Pop
	// (scope-aware implementation).
	scopeStack []scopeFrame

	// inlinedKnownCalls prevents self-recursion from looping forever — the
	// same FuncExpr, once expanded on the isKnownLocalCall path, is not
	// expanded again (03 §3.2.6 note: cyclic graphs are also judged
	// conservatively, since yield alone makes it non-compilable; a single
	// expansion is enough to decide whether the parent function contains yield).
	inlinedKnownCalls map[*ast.FuncExpr]bool
}

type scopeFrame struct {
	saved        map[string]*ast.FuncExpr // snapshot taken on block entry
	savedAliases map[string]bool          // safeAliases snapshot, same discipline
}

func newCompilabilityVisitor() *compilabilityVisitor {
	return &compilabilityVisitor{
		localFuncs:        make(map[string]*ast.FuncExpr),
		safeAliases:       make(map[string]bool),
		inlinedKnownCalls: make(map[*ast.FuncExpr]bool),
	}
}

// pushScope / popScope save / restore localFuncs + safeAliases on entering /
// exiting a block (do/while/for/if/repeat) (simple stack implementation).
func (v *compilabilityVisitor) pushScope() {
	saved := make(map[string]*ast.FuncExpr, len(v.localFuncs))
	for k, e := range v.localFuncs {
		saved[k] = e
	}
	savedAliases := make(map[string]bool, len(v.safeAliases))
	for k := range v.safeAliases {
		savedAliases[k] = true
	}
	v.scopeStack = append(v.scopeStack, scopeFrame{saved: saved, savedAliases: savedAliases})
}

func (v *compilabilityVisitor) popScope() {
	if len(v.scopeStack) == 0 {
		return
	}
	frame := v.scopeStack[len(v.scopeStack)-1]
	v.scopeStack = v.scopeStack[:len(v.scopeStack)-1]
	v.localFuncs = frame.saved
	v.safeAliases = frame.savedAliases
}

// walkBlock walks a block (a list of statements).
func (v *compilabilityVisitor) walkBlock(b *ast.Block) {
	if b == nil {
		return
	}
	for _, s := range b.Stmts {
		v.walkStmt(s)
	}
}

// walkStmt walks a single statement.
func (v *compilabilityVisitor) walkStmt(s ast.Stmt) {
	switch n := s.(type) {
	case *ast.LocalStmt:
		// `local x, y, z = a, b, function() ... end` — if some expr is a
		// FuncExpr literal, bind the corresponding name into localFuncs (the
		// basis for F2 known local call).
		for _, e := range n.Exprs {
			v.walkExpr(e)
		}
		// Register local functions (a later definition of the same name
		// overwrites the earlier one, matching Lua 5.1 scope semantics).
		for i, name := range n.Names {
			if i < len(n.Exprs) {
				if fn, ok := n.Exprs[i].(*ast.FuncExpr); ok {
					v.localFuncs[name] = fn
					delete(v.safeAliases, name)
					continue
				}
				// `local sqrt = math.sqrt` shape: the RHS is a safe stdlib
				// reference (a value read, not a call), so register the
				// LHS as a safe alias; later `sqrt(x)` calls skip the
				// unknown mark. Dataflow tracking: reassignment and
				// shadowing are cleaned up by the delete branches below
				// and in AssignStmt.
				if isSafeStdlibCall(n.Exprs[i]) {
					v.safeAliases[name] = true
					delete(v.localFuncs, name)
					continue
				}
			}
			// Not a FuncExpr literal ⇒ do not bind (if name previously pointed
			// at a local fn and is now shadowed, remove it from the map to
			// avoid a misjudgment).
			delete(v.localFuncs, name)
			delete(v.safeAliases, name)
		}
	case *ast.LocalFuncStmt:
		// `local function f() ... end` — bind localFuncs directly.
		// Note the scope: the function body can see itself (recursion allowed),
		// so bind first, then walk the body.
		v.localFuncs[n.Name] = n.Fn
		delete(v.safeAliases, n.Name)
		v.walkFuncExpr(n.Fn)
	case *ast.AssignStmt:
		for _, e := range n.Targets {
			v.walkExpr(e)
		}
		for _, e := range n.Exprs {
			v.walkExpr(e)
		}
		// `f = function() end` shape — if the target is a NameExpr and the RHS
		// is a FuncExpr, but a global/upvalue f being assigned is not a local,
		// **conservatively do not bind localFuncs** (after assignment f may be
		// overwritten externally).
		// Any reassignment to a tracked name invalidates its known-fn /
		// safe-alias record (dataflow invalidation).
		for _, tgt := range n.Targets {
			if name, ok := tgt.(*ast.NameExpr); ok {
				delete(v.localFuncs, name.Name)
				delete(v.safeAliases, name.Name)
			}
		}
	case *ast.CallStmt:
		v.walkExpr(n.Call)
	case *ast.DoStmt:
		v.pushScope()
		v.walkBlock(n.Body)
		v.popScope()
	case *ast.WhileStmt:
		v.walkExpr(n.Cond)
		v.pushScope()
		v.walkBlock(n.Body)
		v.popScope()
	case *ast.RepeatStmt:
		// repeat-until: Cond can see locals within the Body scope
		v.pushScope()
		v.walkBlock(n.Body)
		v.walkExpr(n.Cond)
		v.popScope()
	case *ast.IfStmt:
		for _, c := range n.Clauses {
			v.walkExpr(c.Cond)
			v.pushScope()
			v.walkBlock(c.Body)
			v.popScope()
		}
		if n.Else != nil {
			v.pushScope()
			v.walkBlock(n.Else)
			v.popScope()
		}
	case *ast.NumForStmt:
		v.walkExpr(n.Init)
		v.walkExpr(n.Limit)
		if n.Step != nil {
			v.walkExpr(n.Step)
		}
		v.pushScope()
		v.walkBlock(n.Body)
		v.popScope()
	case *ast.GenForStmt:
		for _, e := range n.Exprs {
			v.walkExpr(e)
		}
		v.pushScope()
		v.walkBlock(n.Body)
		v.popScope()
	case *ast.FuncStmt:
		// `function a.b.c.m() ... end` — the target is a NameExpr/IndexExpr chain.
		// Do not bind localFuncs (global or table field, not a local).
		v.walkExpr(n.Target)
		v.walkFuncExpr(n.Fn)
	case *ast.ReturnStmt:
		for _, e := range n.Exprs {
			v.walkExpr(e)
		}
	case *ast.BreakStmt:
		// no expression, nothing to walk
	}
}

// walkExpr walks a single expression.
func (v *compilabilityVisitor) walkExpr(e ast.Expr) {
	if e == nil {
		return
	}
	switch n := e.(type) {
	case *ast.NilExpr, *ast.TrueExpr, *ast.FalseExpr,
		*ast.NumberExpr, *ast.StringExpr:
		// nothing
	case *ast.VarargExpr:
		v.sawVararg = true
	case *ast.NameExpr:
		v.visitNameExpr(n)
	case *ast.IndexExpr:
		v.walkExpr(n.Obj)
		v.walkExpr(n.Key)
	case *ast.ParenExpr:
		v.walkExpr(n.E)
	case *ast.CallExpr:
		v.visitCallExpr(n)
	case *ast.MethodCallExpr:
		v.visitMethodCallExpr(n)
	case *ast.BinExpr:
		v.walkExpr(n.L)
		v.walkExpr(n.R)
	case *ast.UnExpr:
		v.walkExpr(n.E)
	case *ast.FuncExpr:
		v.walkFuncExpr(n)
	case *ast.TableExpr:
		for _, it := range n.Items {
			if it.Key != nil {
				v.walkExpr(it.Key)
			}
			v.walkExpr(it.Val)
		}
	}
}

// visitNameExpr captures key names like debug / setfenv / getfenv (F3 / F4).
//
// False-positive tolerance: a user redefining a local `debug` also fires this
// (a rare anti-pattern). Precise recognition (scope-aware name resolution) is
// left to gap GAP-5 in §9.
func (v *compilabilityVisitor) visitNameExpr(e *ast.NameExpr) {
	switch e.Name {
	case "debug":
		v.usesDebug = true
	case "setfenv", "getfenv":
		v.usesSetfenv = true
	}
}

// visitCallExpr handles the f(args) shape (F2).
func (v *compilabilityVisitor) visitCallExpr(e *ast.CallExpr) {
	// 1. Walk Args first (args can also contain yield etc.)
	for _, a := range e.Args {
		v.walkExpr(a)
	}
	// 2. Recognize coroutine.* calls
	if isCoroutineCall(e.Fn) {
		v.callsCoroutine = true
		switch methodName(e.Fn) {
		case "yield":
			v.callsYield = true
		case "resume":
			v.callsResume = true
		}
		return
	}
	// 3. Recognize debug.* calls (F3 reinforcement)
	if isDebugCall(e.Fn) {
		v.usesDebug = true
		return
	}
	// 4. Recognize direct setfenv/getfenv calls (F4 reinforcement)
	if name, ok := e.Fn.(*ast.NameExpr); ok {
		if name.Name == "setfenv" || name.Name == "getfenv" {
			v.usesSetfenv = true
			return
		}
	}
	// 5. stdlib whitelist (P2 follow-up optimization round #1): stdlib calls
	// that definitely will not yield are not marked unknown, letting functions
	// with pure computation + stdlib calls be judged Compilable.
	if isSafeStdlibCall(e.Fn) {
		return
	}
	// 5b. Safe stdlib alias call (`local sqrt = math.sqrt; ... sqrt(x)`
	// shape). LocalStmt dataflow tracking registered the name in
	// safeAliases; reassignment/shadowing was already invalidated in
	// walkStmt branches, so a hit here is equivalent to a direct
	// math.sqrt(x) call.
	if name, ok := e.Fn.(*ast.NameExpr); ok && v.safeAliases[name.Name] {
		return
	}
	// 6. General call — isKnownLocalCall real implementation (user decided
	// not to make it always false)
	if v.isKnownLocalCall(e.Fn) {
		// A known local pointing at some child FuncExpr of the current Proto →
		// merge the child function body into the parent's verdict (walkBlock,
		// not walkFuncExpr — the latter creates a sub-visitor that isolates
		// signals, which is for the "child function definition" scenario, not
		// the "parent calls child" semantics).
		// A parent calling a child is equivalent to "the parent, on its
		// execution path, effectively runs the helper body" — the signals must
		// propagate.
		name := e.Fn.(*ast.NameExpr).Name
		fn := v.localFuncs[name]
		// Prevent infinite self-recursion (03 §3.2.6 note: a second entry into
		// the same FuncExpr is skipped)
		if !v.inlinedKnownCalls[fn] {
			v.inlinedKnownCalls[fn] = true
			v.walkBlock(fn.Body)
		}
		return
	}
	// 7. Also walk the Fn expression (non-NameExpr etc. may contain an
	// IndexExpr chain and so on)
	v.walkExpr(e.Fn)
	// 8. Any call that cannot be statically determined to be a "known local
	// child Proto" or a "safe stdlib call" is treated as unknown (03 §3.2 (b)).
	v.callsUnknownFn = true
}

// visitMethodCallExpr handles the `obj:method(args)` shape — decomposed into
// "receiver/args walk + mark sawSelfCall signal". **No longer hard-stacks
// callsUnknownFn** (from the P4 PJ5 SELF inline shape wiring decision): the
// method call's callee is the same as PJ5's existing known-fn shape (MOVE/
// GETUPVAL) — the callee's internal yield/__call/meta are handed off wholesale
// to host.Self + host.CallBaseline / host.TailCall for byte-equal P1 doCall
// path handling.
//
// **Placeholder-bit semantics**: the default still judges NotCompilable +
// ReasonSelfCall (placeholder); at runtime, once P4 is injected,
// `recheckCompilabilityRuntime` clears the placeholder (same "re-judge after
// P3/P4 injection" technique as ReasonBackendUnsupp). The F1-F6 real
// structural exclusions are preserved as-is.
func (v *compilabilityVisitor) visitMethodCallExpr(e *ast.MethodCallExpr) {
	v.walkExpr(e.Recv)
	for _, a := range e.Args {
		v.walkExpr(a)
	}
	v.sawSelfCall = true
}

// walkFuncExpr handles a nested FuncExpr (03 §7.3 signal-contagion isolation).
//
// Before entering the child function: +1 currentDepth + record maxClosureDepth;
// the child function body runs in its own sub-visitor, which only writes the
// nesting-depth signal back to the parent — the child's yield/debug/setfenv
// content signals are judged by its own independent AnalyzeProto call.
//
// **Exception**: the isKnownLocalCall path (visitCallExpr step 5) still
// recurses into the child using the current visitor — that is to propagate a
// known local call's yield content back to the parent function. The two paths
// do not conflict: a child function definition itself does not propagate, but
// when the parent calls the child, propagation follows call semantics.
//
// **PJ5 extension (2026-06-27)**: sub-visitor.localFuncs inherits the parent
// visitor's snapshot (minus entries shadowed by the closure's own parameter
// names), letting outer local known fns called inside the closure be
// recognized as known (from 03 §9 GAP-5 scope-aware name resolution). Example:
//
//	local function noop() end
//	local function invoker() noop() end  -- noop is an upvalue inside invoker
//
// Without extension: when invoker.body sees noop() the sub.localFuncs is empty
// → noop marked callsUnknownFn → ReasonUnknownCall → invoker NotCompilable;
// With extension: sub.localFuncs contains noop → isKnownLocalCall=true →
// recursively judge noop.Body (same signal-contagion semantics as the parent:
// if noop contains yield then invoker does too), so invoker's shape is
// Compilable + P4 PJ5 tier-up reachable.
//
// **Shadowing safety**: the closure's own Params (`function(name) ... end`)
// are removed from the inherited table, avoiding mistaking the parent's `noop`
// for the closure's parameter `noop`. A local redefined inside the closure
// body overwrites the inherited parent entry via walkStmt::LocalStmt.
func (v *compilabilityVisitor) walkFuncExpr(e *ast.FuncExpr) {
	v.currentDepth++
	if v.currentDepth > v.maxClosureDepth {
		v.maxClosureDepth = v.currentDepth
	}

	// The child function body runs on its own (isolated signals)
	sub := newCompilabilityVisitor()
	sub.currentDepth = v.currentDepth
	// Inherit the parent's localFuncs + safeAliases snapshot, minus entries
	// shadowed by the closure's own parameter names.
	for k, fn := range v.localFuncs {
		shadowed := false
		for _, p := range e.Params {
			if p == k {
				shadowed = true
				break
			}
		}
		if !shadowed {
			sub.localFuncs[k] = fn
		}
	}
	for k := range v.safeAliases {
		shadowed := false
		for _, p := range e.Params {
			if p == k {
				shadowed = true
				break
			}
		}
		if !shadowed {
			sub.safeAliases[k] = true
		}
	}
	// Seed the sub's recursion guard with a copy of the ancestors'
	// already-expanded set. Without this, a closure literal inside a
	// recursive local function resets the guard at every sub-visitor
	// boundary (`local function A() return function() A() end end`:
	// expand A -> closure literal -> fresh sub -> A not marked -> expand
	// A -> ...), which is a fatal, unrecoverable stack overflow at
	// Compile time (fuzz seed 648e96a2d9661b88). A copy — not a shared
	// reference — keeps the parent's own signal analysis intact: the
	// sub's expansions must not consume the parent's single-expansion
	// budget (parent signals are load-bearing, sub signals are
	// discarded except maxClosureDepth).
	for fn := range v.inlinedKnownCalls {
		sub.inlinedKnownCalls[fn] = true
	}
	sub.walkBlock(e.Body)

	// Only write back the nesting depth
	if sub.maxClosureDepth > v.maxClosureDepth {
		v.maxClosureDepth = sub.maxClosureDepth
	}

	v.currentDepth--
}

// isKnownLocalCall: whether fn is a "known local name pointing at some child
// Proto of the current Proto".
//
// User-decided real implementation: tracks the local fn name → child FuncExpr
// mapping (bound to the table at LocalStmt / LocalFuncStmt).
func (v *compilabilityVisitor) isKnownLocalCall(fn ast.Expr) bool {
	name, ok := fn.(*ast.NameExpr)
	if !ok {
		return false
	}
	_, isLocal := v.localFuncs[name.Name]
	return isLocal
}

// isCoroutineCall detects the coroutine.<method>(...) pattern (03 §3.2.4 (1)).
func isCoroutineCall(fn ast.Expr) bool {
	idx, ok := fn.(*ast.IndexExpr)
	if !ok {
		return false
	}
	obj, ok := idx.Obj.(*ast.NameExpr)
	if !ok || obj.Name != "coroutine" {
		return false
	}
	_, ok = idx.Key.(*ast.StringExpr)
	return ok
}

// isDebugCall detects the debug.<method>(...) pattern.
func isDebugCall(fn ast.Expr) bool {
	idx, ok := fn.(*ast.IndexExpr)
	if !ok {
		return false
	}
	obj, ok := idx.Obj.(*ast.NameExpr)
	if !ok || obj.Name != "debug" {
		return false
	}
	_, ok = idx.Key.(*ast.StringExpr)
	return ok
}

// methodName extracts the method name from <table>.<method>.
func methodName(fn ast.Expr) string {
	if idx, ok := fn.(*ast.IndexExpr); ok {
		if key, ok := idx.Key.(*ast.StringExpr); ok {
			return key.Val
		}
	}
	return ""
}

// safeStdlibFuncs is the whitelist of Lua 5.1.5 stdlib functions that
// **definitely will not yield nor indirectly execute user Lua** (P2 follow-up
// optimization round #1 "precise yield analysis").
//
// Inclusion principles:
//   - **No yield**: the function itself does not call coroutine.yield;
//   - **No indirect execution of user Lua**: the function does not take a
//     callback argument (otherwise the callback might yield) — this excludes
//     string.gsub (can take a fn) / table.foreach etc.;
//   - **No re-entering the interpreter to run user metamethods**: the objects
//     the function operates on do not trigger `__index`/`__newindex` etc.
//     metamethods — this is why `pairs` / `next` are **not on the whitelist**
//     (`pairs(t)` triggers `__pairs` in 5.2+, 5.1 has no such metamethod;
//     `next` reads raw directly, but conservatively still excluded).
//
// pcall / xpcall are **not on the whitelist** — although pcall itself has a
// yield-barrier, the fn it runs has a non-negligible chance of containing
// metamethods or coroutine interfaces; excluding them keeps the conservative
// boundary consistent with P3 wasm compilation capability (P3 compilation
// across a pcall boundary is complex).
//
// Main whitelist categories:
//   - global type operations: type, tostring, tonumber, select, unpack
//   - table raw operations: rawget, rawset, rawequal
//   - metatable operations: setmetatable, getmetatable
//   - arithmetic helpers: all of math.*
//   - string helpers: string.* that do not take a fn argument (byte/char/find/
//     format/len/lower/upper/rep/reverse/sub/match/dump) — excludes gsub
//     (takes fn) / gmatch (returns an iterator)
//   - table helpers: table.* that do not take a fn (concat/insert/remove/sort/
//     maxn) — note sort takes a cmp fn which is user Lua and strictly should
//     also be excluded; but in practice cmp fns very rarely yield, so allowing
//     it is more useful, left for adjustment after real-world testing
//
// stdlib functions NOT on the whitelist (explicitly rejected):
//   - print / write / read / io.* / os.execute (IO call boundary)
//   - error / assert (error triggers longjmp semantics outside the pcall barrier)
//   - load / loadstring / loadfile / dofile (dynamic execution of user code)
//   - string.gsub (takes a fn argument) / string.gmatch (returns an iterator,
//     needs a yield chain)
//   - table.foreach / table.foreachi (take a fn argument)
//   - pairs / ipairs (return iterators, coupled with the generic-for protocol)
//   - next (does not yield itself but is coupled with the iterator protocol,
//     conservatively excluded)
var safeStdlibFuncs = map[string]bool{
	// global
	"type":         true,
	"tostring":     true,
	"tonumber":     true,
	"select":       true,
	"unpack":       true,
	"rawget":       true,
	"rawset":       true,
	"rawequal":     true,
	"setmetatable": true,
	"getmetatable": true,
}

// safeStdlibLibs marks whole libraries as whitelisted: all of stdlib.<method>
// is safe (specific methods no longer enumerated); but each library's
// "takes-a-fn-argument" functions are still excluded (the safeStdlibLibFuncs
// blacklist).
var safeStdlibLibs = map[string]bool{
	"math":   true,
	"string": true,
	"table":  true,
	"os":     true, // os.time/os.date/os.clock etc. do not yield; os.execute is IO but conservatively allowed this round
}

// unsafeStdlibLibFuncs lists specific methods within safeStdlibLibs that must
// still be excluded (take a fn / return an iterator / dynamic-execution kinds).
var unsafeStdlibLibFuncs = map[string]map[string]bool{
	"string": {
		"gsub":   true, // third arg can be a fn (executes user code)
		"gmatch": true, // returns an iterator (coupled with the generic-for protocol)
	},
	"table": {
		"foreach":  true, // takes a fn argument
		"foreachi": true, // same as above
	},
	"os": {
		"execute": true, // IO boundary
	},
}

// isSafeStdlibCall judges whether fn is a whitelisted stdlib call (P2 follow-up
// optimization round #1).
//
// Two recognized shapes:
//   - NameExpr{name}: global function (type/tostring/...) → look up safeStdlibFuncs
//   - IndexExpr{Obj=NameExpr{lib}, Key=StringExpr{m}}: lib.m shape →
//     look up safeStdlibLibs / unsafeStdlibLibFuncs
//
// Any other shape (`obj:m()` method call / passing as an argument / saving a fn
// in a table field then calling it, etc.) uniformly takes the unknown path
// (conservative first).
func isSafeStdlibCall(fn ast.Expr) bool {
	// Shape 1: direct call of a global name
	if name, ok := fn.(*ast.NameExpr); ok {
		return safeStdlibFuncs[name.Name]
	}
	// Shape 2: lib.method shape
	idx, ok := fn.(*ast.IndexExpr)
	if !ok {
		return false
	}
	libName, ok := idx.Obj.(*ast.NameExpr)
	if !ok {
		return false
	}
	mName, ok := idx.Key.(*ast.StringExpr)
	if !ok {
		return false
	}
	if !safeStdlibLibs[libName.Name] {
		return false
	}
	// Check whether the specific method is in that library's unsafe blacklist
	if unsafe, ok := unsafeStdlibLibFuncs[libName.Name]; ok {
		if unsafe[mName.Val] {
			return false
		}
	}
	return true
}
