// Package p3indirect is the PW10 spike harness for eliminating the
// gibbous→gibbous cross-layer call tax (docs/design/p3-wasm-tier/04-trampoline.md
// §9 gap + 02-translation.md §1.2).
//
// Background: P3 PW9 measurements found that a gibbous→gibbous call goes through
// h_call with a double cross-layer hop (Wasm→Go→Wasm, PW0 measured ~143ns/call),
// which degrades call-intensive kernels (call kernel 7x slower than crescent).
// The real fix is "single module + internal function table + call_indirect direct
// call" — but this is a milestone-level architectural change, blocked by two
// codebase physics constraints (each Proto is an independent module + Lua frames
// live in Go), and with one make-or-break unknown: whether wazero can
// incrementally compile / re-instantiate a module (incremental promotion needs
// it).
//
// This spike is PW10's kickoff gate (following the PW0 precedent), validating two
// make-or-break assumptions:
//
//   - S-A: the per-call cost of call_indirect within a single module — must be
//     ≪143ns (host round trip), target <30ns (near-native indirect call). If it
//     misses, the whole overhaul is pointless.
//   - S-B: the module re-compile cost of incremental promotion + the
//     instance-hot-swap lifecycle — validate that "re-compile module{A} →
//     module{A,B} and safely switch instances" is feasible (cannot Close the old
//     instance while it has an in-flight gibbous frame).
//
// The baseline S-Host replicates PW0 S3N's per-call amortized cost of an imported
// call (~143ns), for S-A to directly compare the speedup factor.
//
// **Independent go module**: does not pollute the main library's zero-external-
// dependency discipline (same as spike/p3boundary + benchmarks/). After the data
// goes into docs/design/p3-wasm-tier/implementation-progress.md §11 PW10
// decision, this directory is kept as a regression.
package p3indirect
