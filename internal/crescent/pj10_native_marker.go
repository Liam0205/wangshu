// PJ10 native path marker check.
//
// The tail-call gibbous dispatch in execute()'s TAILCALL case is only
// safe when the callee GibbousCode honors DoReturn's standard
// frame-teardown contract on a reused tail-call frame. PJ10's
// CFG-based native emit (translator_native.go) meets this contract;
// PerOpCode head-op replay (translator.go) does not — it assumes a
// fresh frame and doesn't compose with the tail-call frame lifecycle
// (SetTailcall, funcIdx = parent.FuncIdx, Fresh flag inheritance).
//
// We can't import peroptranslator here (would create a cycle), so use
// a duck-typed marker method.
package crescent

import "github.com/Liam0205/wangshu/internal/bridge"

// pj10NativeMarker is duck-typed on the peroptranslator.nativeCode
// type's IsPJ10Native() method (see translator_native.go +
// translator_native_arm64.go).
type pj10NativeMarker interface {
	IsPJ10Native() bool
}

func isPJ10NativeCode(code bridge.GibbousCode) bool {
	m, ok := code.(pj10NativeMarker)
	return ok && m.IsPJ10Native()
}
