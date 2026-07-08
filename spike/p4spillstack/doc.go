// Package p4spillstack is the issue #89 spike: prove that a P4 native
// trampoline can switch SP onto a self-managed spill stack (a Go-heap
// []byte) so that deep seg2seg recursion consumes that buffer instead of
// the goroutine stack's NOSPLIT allowance.
//
// Does not pollute the main module: standalone go module (mirrors
// spike/p4tramp / spike/p3boundary / spike/p3indirect).
//
// Background (see docs/design/p4-method-jit/05-system-pipeline.md §3.4):
// PR #86 dropped segToSegDepthCap from 128 to 16 because each seg2seg
// level does a `sub sp` inside the NOSPLIT trampoline window, invisible
// to Go's linker nosplit accounting. Go only reserves StackNosplitBase =
// 800 bytes below a checkpoint, so a ~4KB descent (cap=128) can punch
// through the stack allocation and corrupt a neighboring heap object
// (GC reports "found pointer to free object"). The fix is to switch SP
// onto a self-managed 64 KiB spill stack in the trampoline, so the
// descent burns that buffer instead of the goroutine stack.
//
// Gates (verdict written to DECISION.md):
//
//   - G1 SP switch works: a trampoline that moves SP to a []byte buffer,
//     CALLs a segment, and restores SP returns correctly and does not
//     crash Go.
//   - G2 deep descent lives on the spill stack: a segment that recurses
//     N levels (each `sub sp` + write a canary) at a depth that would
//     blow the goroutine stack runs fine on the spill stack, and the
//     canaries read back correctly (no corruption).
//   - G3 nested trampoline re-entry hands off SP without clobber: an
//     outer CallJIT that (via a host callback) makes an inner CallJIT
//     must not let the inner descent overwrite the outer's live spill
//     region. Verified by an SP-cursor handoff (continue descending from
//     the current SP when already inside the spill buffer).
//   - G4 GOGC=1 stress: repeated deep runs under debug.SetGCPercent(1)
//     do not trigger "found pointer to free object" — the spike form of
//     the main-module TestI86_DeepRecursionGCStress regression.
//
// Red/green: all four green => the SP-switch design is physically sound
// on amd64; wire it into internal/gibbous/jit/amd64/trampoline_spec and
// raise segToSegDepthCap. arm64 SP switch is NOT covered here (this host
// is x86_64, no qemu) — it rides the CI arm64 matrix on PR / a separate
// arm64 machine spike.
//
// Upstream contracts:
//   - docs/design/p4-method-jit/05-system-pipeline.md §3.4 (spill layout)
//   - docs/design/p4-method-jit/06-backends.md §4.1.5 / §4.2.5 (trampoline)
//   - internal/gibbous/jit/peroptranslator/call_ic.go (segToSegDepthCap)
package p4spillstack
