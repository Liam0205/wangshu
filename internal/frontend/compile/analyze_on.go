//go:build wangshu_profile

// When the wangshu_profile build tag is enabled, Compile calls
// bridge.AnalyzeProto after finishing each FuncExpr, writing the
// compilability-analysis result into Proto.Compilability +
// Proto.CompReasons (`docs/design/p2-bridge/03-compilability-analysis.md`
// §6.3 wiring).
package compile

import (
	"github.com/Liam0205/wangshu/internal/bridge"
	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/frontend/ast"
)

// analyzeCompilabilityWithOuter is called by compileFunc after producing the Proto —
// the AST is discarded once used (03 §2.4 decision option ①): after this function
// returns, the fn reference can be GC'd.
//
// It holds no Bridge instance: it runs AnalyzeProto/AnalyzeProtoWithOuter with a
// temporary Bridge, only to borrow its visitor + reasonsBitmap → Proto-field write
// logic. Internally AnalyzeProto writes both the Bridge's profileTable (GC'd once
// this function exits) and Proto.Compilability / Proto.CompReasons (shared read-only
// across States).
//
// **PJ5 scope-aware extension (2026-06-27)**: accepts outerFS, collecting the merged
// view of all localFnAsts up the outerFS chain (nearer layers shadow farther ones)
// and passing it to AnalyzeProtoWithOuter as the outerLocalFuncs context — so a nested
// closure can see outer local fns and still recognize them as known calls, opening the
// P4 PJ5 real promotion path. When outerFS=nil (main chunk), it degrades to the old
// AnalyzeProto behavior.
//
// **F7 behavior**: this temporary Bridge has no P3 compiler injected (b.p3 == nil) →
// F7 always fires → every Proto analyzed at Compile time is marked CompNotCompilable +
// ReasonBackendUnsupp. This reflects the reality that "at compile time we do not yet
// know which P3 will be injected at runtime". **At runtime, considerPromotion, seeing
// this placeholder bit while b.p3 is already injected, calls
// bridge.recheckCompilabilityRuntime to re-judge** (issue #18 fix), clearing the F7
// placeholder burned at compile time and re-querying SupportsAllOpcodes against the
// real backend; the F1-F6 structural exclusions are preserved as-is.
//
// **Impact on PB7 acceptance**: byte-equal differential still holds — a P1-only build
// (without wangshu_profile) is a no-op across the whole chain; under a p3 build,
// structural NotCompilable (F1-F6) is still permanently interpreted, only the subset of
// "compile-time F7 placeholder + runtime-P3-handleable" takes the promotion path,
// consistent with the original byte-equal expectation (F1-F6 subset behavior unchanged).
func analyzeCompilabilityWithOuter(fn *ast.FuncExpr, proto *bytecode.Proto, outerFS *funcState) {
	tmp := bridge.NewBridge()
	if outerFS == nil {
		tmp.AnalyzeProto(fn, proto)
		return
	}
	// Collect the merged view of localFnAsts across all funcStates up the outer chain (nearer layers shadow farther ones).
	outerLocals := map[string]*ast.FuncExpr{}
	outerAliases := map[string]ast.Expr{}
	// Merge from the farthest layer to the nearest in reverse, so inner shadows outer (local scope shadowing semantics).
	var chain []*funcState
	for cur := outerFS; cur != nil; cur = cur.prev {
		chain = append(chain, cur)
	}
	// chain is now innermost-to-outermost; iterate in reverse, outermost-to-innermost
	for i := len(chain) - 1; i >= 0; i-- {
		for name, fnAST := range chain[i].localFnAsts {
			outerLocals[name] = fnAST
			delete(outerAliases, name)
		}
		for name, rhs := range chain[i].localAliasAsts {
			outerAliases[name] = rhs
			delete(outerLocals, name)
		}
	}
	tmp.AnalyzeProtoWithOuter(fn, proto, outerLocals, outerAliases)
}
