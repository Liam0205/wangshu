// Package bridge is the P2 layered-bridge subsystem (basecraft, not an
// execution layer).
//
// Design docs: docs/design/p2-bridge/ (00-overview..06-testing-strategy).
//
// P2 in one sentence: on top of the P1 interpreter (crescent), add a "layered
// decision machine" that produces three things (hotness, IC type feedback,
// compilability judgment) to feed the P3/P4 compile layer, while itself
// staying off the execution hot path (`docs/design/p2-bridge/00-overview.md`
// §1).
//
// Package layout (each file holds one group of tightly-coupled types; does not
// mutually depend on the execution layer internal/crescent -- bridge is
// infrastructure, not an execution layer):
//
//   - profile.go        hotness counters (back edge / entry) and ProfileData fields
//   - feedback.go       TypeFeedback / PointFeedback / FeedbackKind enums
//   - compilability.go  the Compilability three-state enum and reasonsBitmap
//   - tier.go           the TierState state-machine three-state enum
//   - p3compiler.go     the P3Compiler interface (shared P3/P4 front end) and the GibbousCode abstraction
//   - bridge.go         the Bridge main struct + onBackEdge / onEnter hook points +
//     the considerPromotion state-machine entry
//
// **Important invariants** (span the whole package; breaking them is a design
// failure):
//
//   - bridge does not depend on internal/crescent -- the reverse hook points
//     are injected from the crescent side (interface + setter) to avoid a
//     circular dependency.
//   - bridge itself does not emit code and does not run Protos; once "run
//     Proto" logic appears in this package it violates P2's iron rule of
//     "staying off the execution hot path".
//   - all P2 counter increments and compilability queries must be
//     compile-time eliminable when ProfileEnabled() is false (zero overhead
//     for P1-only deployments).
//
// State-machine invariant: TierState is one-directional + absorbing (no
// reverse TierGibbous→TierInterp edge), which is the formalization of the
// P2/P3 "zero deopt" property (`04-try-compile-fallback.md` §2.4).
package bridge
