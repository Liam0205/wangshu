//go:build wangshu_p4

// perop_hook.go — registration hook so the PJ10 per-opcode translator
// can install itself into Compiler.Compile / .SupportsAllOpcodes without
// jit (main package) importing peroptranslator (sub-package). The sub-
// package imports jit for the P4HostState + JITContext types; the hook
// is the inverse direction, registered at init time.
//
// Wiring:
//   - jit (this file): declares the two hook variables, defaults nil.
//   - peroptranslator (sub-package init): registers its TranslateProto +
//     AnalyzeShape into the hooks via the public Register* functions.
//   - crescent.wireP4 (or any other entry that wants PJ10 enabled): blank
//     imports peroptranslator so its init runs.
//
// When the hooks are nil (peroptranslator not imported), Compile and
// SupportsAllOpcodes behave exactly like PJ0-PJ9, so this is a strict
// extension. The PJ7 byte-equal test suite is unaffected if the hooks
// stay nil; turning on PJ10 means importing peroptranslator somewhere.

package jit

import (
	"sync/atomic"

	"github.com/Liam0205/wangshu/internal/bridge"
	"github.com/Liam0205/wangshu/internal/bytecode"
)

// CompilePreferNativeCount counts how many times Compile took the
// "prefer native path" fast-track. Used by benchmarks/tests to confirm
// the native compile path is actually being selected.
var CompilePreferNativeCount atomic.Int64

// perOpTranslator is the hook for compiling a Proto through the
// per-opcode translator. nil = PJ10 disabled (PJ7 paths only).
var perOpTranslator func(proto *bytecode.Proto, host P4HostState) (bridge.GibbousCode, error)

// perOpAnalyzer is the hook for "does PJ10 know how to compile this
// Proto?". Returns true iff TranslateProto would succeed. Called from
// SupportsAllOpcodes as a fall-through after analyzeShape.
var perOpAnalyzer func(proto *bytecode.Proto) bool

// perOpNativeAnalyzer is the narrower hook for "does PJ10's *native*
// CFG-based emit path accept this Proto?". When true, Compile prefers
// the native path over the historical shape-spec fast paths, because
// native emits full inline SSE for multi-BB numeric loops that the
// shape spec would fall back to a mostly-interpreted head-op replay
// on. nil when peroptranslator is not imported (or does not expose
// AnalyzeNative), leaving the historical Compile order intact.
var perOpNativeAnalyzer func(proto *bytecode.Proto) bool

// RegisterPerOpNativeAnalyzer installs the "native accepts?" analyzer.
// Optional companion to RegisterPerOpTranslator: without it, Compile
// stays on the historical order and only reaches the perOpTranslator
// as a fallback when analyzeShape rejects.
func RegisterPerOpNativeAnalyzer(analyzer func(*bytecode.Proto) bool) {
	perOpNativeAnalyzer = analyzer
}

// RegisterPerOpTranslator installs the PJ10 translator entry. Called from
// peroptranslator.init. Panics if called twice (only one registration
// expected per process).
func RegisterPerOpTranslator(
	translate func(*bytecode.Proto, P4HostState) (bridge.GibbousCode, error),
	analyzer func(*bytecode.Proto) bool,
) {
	if perOpTranslator != nil {
		panic("jit: RegisterPerOpTranslator called twice")
	}
	perOpTranslator = translate
	perOpAnalyzer = analyzer
}

// HasPerOpTranslator reports whether PJ10 is wired in. Test scaffolding
// uses this to skip / opt in to PJ10-specific assertions.
func HasPerOpTranslator() bool { return perOpTranslator != nil }
