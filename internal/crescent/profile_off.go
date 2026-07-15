//go:build !wangshu_profile

package crescent

// profileEnabled is the compile-time constant of the P2 counting switch
// (`docs/design/p2-bridge/01-profiling.md` §3.5 route B bypass counting + §0.1
// invariant 1):
//
//   - Default false: the back-edge / entry sampling hook points are entirely
//     eliminated by the Go compiler — a P1-only deployment pays no counting tax
//     at all (zero overhead), byte-for-byte identical to P1 behavior.
//   - `go build -tags wangshu_profile` enables it as true, activating the P2
//     decision machine.
//
// This is the physical realization of "independent per-stage delivery"
// (principle 3) at the P1 ↔ P2 boundary: **enabling P2 is an internal behavior
// switch, the public API is unchanged**.
const profileEnabled = false
