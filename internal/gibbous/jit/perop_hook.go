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
	"github.com/Liam0205/wangshu/internal/bridge"
	"github.com/Liam0205/wangshu/internal/bytecode"
)

// perOpTranslator is the hook for compiling a Proto through the
// per-opcode translator. nil = PJ10 disabled (PJ7 paths only).
var perOpTranslator func(proto *bytecode.Proto, host P4HostState) (bridge.GibbousCode, error)

// perOpAnalyzer is the hook for "does PJ10 know how to compile this
// Proto?". Returns true iff TranslateProto would succeed. Called from
// SupportsAllOpcodes as a fall-through after analyzeShape.
var perOpAnalyzer func(proto *bytecode.Proto) bool

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
