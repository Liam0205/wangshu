// Package p3boundary is the PW0 spike harness for the P3 Wasm tier
// (docs/design/p3-wasm-tier/01-spike-gate.md).
//
// Goal: empirically confirm the wazero call boundary < 150ns gate (the P3
// life-or-death gate).
// Three sample tiers S1/S2/S3 + shared visibility of memory + feasibility of
// arena adoption + a minimal check of the four taxes.
//
// **Standalone go module**: keeps the main library `github.com/Liam0205/wangshu`
// under its zero-external-dependency discipline (same approach as the
// benchmarks/ submodule). The spike is temporary validation; once its data
// lands in docs/design/p3-wasm-tier/implementation-progress.md this directory
// may be kept for regression.
package p3boundary
