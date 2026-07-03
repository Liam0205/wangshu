# Spike decision — issue #50 EmitCallInline (2026-07-04)

## Question

Can a P4 native mmap segment CALL directly into another mmap segment
(segment-to-segment dispatch), and does in-segment CallInfo frame
build/teardown hold up physically?

## Measurements (Xeon Platinum, linux/amd64, go1.26.2, -benchtime=2s -cpu=1)

| probe | result | gate | verdict |
|---|---|---|---|
| S-A cross-segment CALL+RET (amortized over 1024 inner calls) | **1.1 ns/call** | < 20 ns | ✅ 18x under gate |
| S-A repeated stability (1e6 iterations) | zero corruption | byte-stable | ✅ |
| S-B in-segment frame write (5 CI words + inc/dec ciDepth word + top word) | **2.8 ns** | Go-side observes all writes | ✅ |

Reference point: the exit-reason CALL round trip measured ~600 ns/call
on the same machine (issue #50 amd64 profile comment), with ~84% of
samples on the eliminable chain.

## Physics validated

1. **Cross-page CALL just works**: both pages are PROT_RX in the same
   address space; `call rax` pushes the return address on the current
   stack (Go stack under the NOSPLIT trampoline window) and the callee's
   `ret` returns into the caller segment. No icache, no fence, no
   relocation issue on linux/amd64.
2. **Raw-store frame build is Go-visible**: stores from the segment to
   arena-resident words (CI slot, ciDepth mirror, top mirror) are
   ordinary cache-coherent writes; Go reads observe them immediately
   (single-threaded VM execution — no fence needed).
3. **The return-address discipline matches production**: the production
   spec trampoline already runs segments on the Go stack in a NOSPLIT
   window; nested seg-to-seg CALLs only deepen that native stack, which
   is fine as long as recursion depth × frame bytes stays within the
   goroutine stack the trampoline entered with. Production must bound
   native nesting depth (maxLuaCallDepth already bounds Lua depth;
   native-to-native nesting adds ~16 bytes/level of return addresses —
   20000 × 16 = 320 KB worst case, above the default 8 KB goroutine
   stack but morestack CANNOT trigger inside the segment).
   **⚠ Implication**: production EmitCallInline must either (a) run on
   the self-managed spill stack (jitCtx.spillBase — designed for this in
   05 §3.4, currently unused), or (b) bound native nesting depth low
   enough for the goroutine stack. This is the main engineering risk the
   spike surfaces — not dispatch cost.

## Decision

**GREEN — proceed with EmitCallInline** on the plan:

- In-segment frame build: write 5 CI words + inc ciDepth + set top
  (S-B shape, ~3ns) replacing host enterLuaFrame (~150ns+).
- Segment-to-segment dispatch for native callees via a callee-address
  table (analogous to P3 PW10 R1's shared funcref table; P4 version:
  per-State Go-side slice indexed by protoID, patched at promote time).
- Native stack depth: bound it (option b) for the first cut — a
  conservative native-nesting cap (e.g. 64 levels, ~1 KB return
  addresses + zero locals) with fallback to the exit-reason path
  beyond the cap. Self-managed stack (option a) only if profiling
  shows deep native recursion matters (fib recurses 24 levels).

## Red-light fallback (not needed, kept for the record)

If segment-to-segment dispatch had failed: in-segment frame build +
single boundary hop per call (exit-reason with pre-built frame) —
would have kept the S-B win (~150ns of the ~600ns) and lost the
dispatch win.
