// Logger — promotion logging and diagnostics interface (`docs/design/p2-bridge/04-try-compile-fallback.md` §6.4).
//
// Interface kept minimal: P2 PB0 is a placeholder, PB5 provides the real
// implementation. This file establishes the interface shape first to lock down the contract.
package bridge

import "github.com/Liam0205/wangshu/internal/bytecode"

// Logger is the P2 promotion logging and diagnostics interface (04 §6.4).
//
// **Interface stability guarantee** (04 §6.4 + 05 §0.3): these four methods do
// **not change signatures** after P2 ships — P3/P4 may only add new interfaces
// (embedding Logger), not modify this one.
type Logger interface {
	// LogPromoted: T1 transition succeeded — promotion success log (04 §6.1).
	// Format: function <name> promoted to gibbous (entry=<E>, backedge=<B>, feedback=<F>)
	LogPromoted(proto *bytecode.Proto, pd *ProfileData)

	// LogStuck: T2 transition — not compilable, stays interpreted permanently (04 §6.2).
	// Format: function <name> stays interpreted (not compilable: F<n> <reason>)
	LogStuck(proto *bytecode.Proto, pd *ProfileData, comp Compilability)

	// LogCompileFail: T3 transition — compile failed, stays interpreted permanently (04 §6.3).
	// Format: function <name> compile failed, stays interpreted: <err>
	LogCompileFail(proto *bytecode.Proto, pd *ProfileData, err error)

	// LogPanic: T3 subclass — fallback diagnostics for a P3 backend panic (04 §5.2).
	// The promotion log emits only one line (T3 LogCompileFail); the full stack goes through this separate channel.
	LogPanic(proto *bytecode.Proto, panicValue interface{})
}
