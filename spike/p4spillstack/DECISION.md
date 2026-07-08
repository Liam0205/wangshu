# Spike decision — issue #89 self-managed spill stack (2026-07-08)

## Question

Can a P4 native trampoline switch SP onto a self-managed spill stack (a
Go-heap `[]byte`) so that deep seg2seg recursion consumes that buffer
instead of the goroutine stack's NOSPLIT allowance — letting
`segToSegDepthCap` rise past 16 (PR #86's safe value) back toward 128+?

## Background

PR #86 dropped `segToSegDepthCap` from 128 to 16 because each seg2seg
level does a `sub sp` inside the NOSPLIT trampoline window, invisible to
Go's linker nosplit accounting. Go reserves only `StackNosplitBase = 800`
bytes below a checkpoint, so a ~4 KB descent (cap=128) can punch through
the stack allocation and corrupt a neighboring heap object — GC then
reports "found pointer to free object". The design fix (05 §3.4, fields
`spillBase`/`spillTop` reserved but never wired) is to switch SP onto a
self-managed 64 KiB stack in the trampoline.

## Scope

**amd64 only.** This host is linux/amd64 (no qemu-aarch64), so the arm64
SP switch — whose highest risk is the Go stack walker faulting on a
manually-moved SP (see `trampoline_arm64.s` header) — is NOT covered
here. It rides the CI arm64 matrix on the PR and a separate arm64-machine
spike (relay/handoff of the arm64 leg).

## Measurements (Intel Xeon Platinum, linux/amd64, go1.26.2, -cpu=1)

| gate | probe | result | verdict |
|---|---|---|---|
| G1 | SP switch onto spill stack, shallow segment round-trips | canaries survive, no crash | ✅ |
| G2 | descend 1024 levels (32 KiB, >> cap=128) on a 64 KiB spill stack | all 1024 canaries survive | ✅ |
| G2 | depth sweep 16 / 128 / 512 / 1024 / 1800 levels | all survive | ✅ |
| G3 | 500 repeated relay entries, range-checked SP handoff | no cross-entry clobber | ✅ |
| G3 | Go-side SP asserted outside the spill buffer between calls | confirmed | ✅ |
| G4 | GOGC=1, 200 deep runs + heap churn | no "found pointer to free object" | ✅ |
| cost | SP-switch trampoline vs baseline (no switch) | 19.6 ns vs 19.0 ns (+0.6 ns) | ✅ negligible |

## Physics validated

1. **The SP switch is safe on amd64.** A NOSPLIT trampoline that saves the
   goroutine SP (in R15), moves SP to the spill base, `CALL`s the segment,
   and restores SP before returning does not disturb the Go runtime: the
   window makes no Go call, no stack growth, no back-edge check, so the
   runtime never reads g / grows / preempts while SP is off the goroutine
   stack. R14 (Go G) is never touched because the segment is pure machine
   code.

2. **Deep descent lives entirely on the spill stack.** 1024 levels ×
   32 B = 32 KiB descends cleanly inside a 64 KiB buffer — 8× past the
   cap=128 that PR #86 had to forbid on the goroutine stack. The buffer is
   a plain `[]byte`, invisible to the GC (holds only register spills /
   return addresses, never Lua values or Go heap pointers), so GOGC=1
   stress finds no corruption.

3. **The exit-reason resume path has NO nested-clobber hazard.** The outer
   trampoline restores SP to the goroutine stack as it returns, so each
   fresh `CallJITSpec` (exit-reason resume, HelperCall-driven callee Run)
   enters from the goroutine stack and switches cleanly to `spillBase`.
   Only seg2seg's in-segment `call rax` (which never exits the trampoline)
   keeps descending on the spill stack — and that is exactly the shared
   descent we want. A range-checked handoff ("switch to spillBase only if
   SP is not already inside the buffer") is a cheap belt-and-suspenders
   guard but, given the restore-on-return, is not strictly required on the
   resume path.

## Verdict: 🟢 green (amd64)

The SP-switch design is physically sound on amd64. Wire it into
`internal/gibbous/jit/amd64/trampoline_spec_amd64.s` (save SP, switch to
`spillBase`, restore on return), allocate the spill stack per-(State,Proto)
on the jitCtx (using the reserved `spillBase`/`spillTop` fields), and raise
`segToSegDepthCap`. The main-module regression is
`TestI86_DeepRecursionGCStress` — it must pass 3/3 at the raised cap under
GOGC=1.

**arm64 is not decided here.** The arm64 trampoline's framesize/LR-slot
protocol makes a manual SP move higher-risk (stack walker unwind); that
leg is verified on the CI arm64 matrix (correctness) and a separate arm64
machine (the relay spike + perf), per the user's relay plan.

## What this spike does NOT cover

- arm64 SP switch (no local arm64 execution env; CI + arm64-machine relay).
- Spill-stack overflow → deopt (05 §3.4 bounds the depth; a real overflow
  falls back to the interpreter — the emit path already routes past-cap
  seg2seg to exit-reason, so cap simply moves up).
- Segment-internal Go helper calls on the switched SP: still forbidden
  (same mmap + morestack incompatibility as today; the exit-reason
  protocol already keeps host calls Go-side, after the segment ret's).
