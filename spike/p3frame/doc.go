// Package p3frame is the PW10 "zero-crossing" milestone Stage 0 spike (a
// standalone go module that keeps the main library dependency-free) — it
// validates the life-or-death hypothesis: is doing the frame build/teardown for
// a gibbous→gibbous call inside Wasm (segment-word writes + ciDepth increment/
// decrement + the maxOpenIdx guard) actually faster than the current two host
// crossings via h_call + h_return.
//
// See DECISION.md for the decision report (permanent archive). Verdict 🟢 GREEN:
// in-Wasm ~8.5ns/call vs twocross ~90ns/call (10.5x faster), enough to pull the
// call core up to ≥1x ⟹ green-light the Stage 1+ rewrite.
//
// Follows the precedent of the PW0 spike/p3boundary + PW10-Phase0 spike/p3indirect
// (milestone-level architecture changes come with a spike gate; never blindly
// kick off a multi-session rewrite).
package p3frame
