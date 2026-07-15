// Package jit implements the P4 method JIT (gibbous tier-1 second emit backend).
//
// Status: delivered (PJ0-PJ11, 2026-07-01; per-op native emit + seg2seg).
// amd64 and arm64 both emit native code with a spec-template path, an
// exit-reason / frame-inline protocol, and seg2seg segment-to-segment CALL
// dispatch (issue #50). See docs/design/p4-method-jit/implementation-progress.md.
//
// Relationship to P3 (`internal/gibbous/wasm`): same tier-1, different emit
// backend, sharing the `bridge.P3Compiler` interface surface (`p2-bridge/05-p3-p4-interface.md`
// §0.3 interface stability) — P3 emits wasm handed to wazero to run, P4 emits
// native code and manages codegen itself. Build tags are mutually exclusive:
// `wangshu_p3` and `wangshu_p4` are not enabled at the same time (main-assistant
// decision, aligned with P3's single-field `b.p3` injection; when P3 is retired
// in PJ11, P4 fully takes over — see 07-p3-retirement.md §5).
//
// **Approach A: speculation lifecycle self-managed by P4** (`03-speculation-ic.md`
// §4 / §8 + user decision d0e57f4): the P2 three-state enum
// `TierInterp/TierGibbous/TierStuck` stays unchanged; within this package P4
// maintains a `p4SpecState[proto]` sub-state-machine (`P4Speculative /
// P4Deoptimized / P4StuckSpeculation`). OSR exit / retraining / blacklisting
// speculation are all self-managed by P4, transparent to P2.
//
// Package structure:
//   - compiler.go     P3Compiler implementation + progressive whitelist
//   - code.go         GibbousCode implementation (p4Code struct)
//   - p4state.go      P4 speculation lifecycle state machine (p4SpecState; spec-template deopt)
//   - amd64/          amd64 backend (emitter + trampoline asm)
//   - arm64/          arm64 backend (same structure)
//
// Upstream contracts:
//   - `docs/design/p4-method-jit/00-overview.md` §4 PJ table / §6 quick-reference / §9 invariants
//   - `docs/design/p4-method-jit/02-template-direction.md` direction decision
//   - `docs/design/p4-method-jit/05-system-pipeline.md` the system-pipeline four-piece set
//   - `docs/design/p4-method-jit/06-backends.md` dual-backend split
package jit
