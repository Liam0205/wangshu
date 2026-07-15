// Package p4tramp is the P4 PJ1 spike -- a minimal round-trip demonstration of
// Go → mmap'd amd64 → ret back to Go (follows
// docs/design/p4-method-jit/06-backends.md §1.7 gate discipline).
//
// Does not pollute the main library: an independent go module (analogous to
// spike/p3boundary / spike/p3indirect).
//
// Demonstration goals (follows 06 §6.1 PJ1 acceptance):
//
//   - Gate ① exec mmap + W^X flip works: syscall.Mmap PROT_READ|PROT_WRITE →
//     unix.Mprotect PROT_READ|PROT_EXEC, then jump in from Go and execute;
//   - Gate ② trampoline entry/exit symmetry: Go → asm stub → self-managed
//     stack → mmap segment → ret → restore Go stack; don't step on the Go
//     runtime (GS/FS segments untouched, callee-saved registers saved and
//     restored);
//   - Gate ③ a single straight-line template can be emitted + executed: emit
//     the eight-byte sequence "mov rax, IMM64; ret", Go calls in and gets the
//     IMM64 value back -- this is the physical basis of the LOADK template;
//   - Gate ④ demonstrate the single-shot cost upper bound of exec mmap: same
//     form as the P3 spike S1/S2/S3, producing an ns/op number filed as the
//     PJ1+ performance baseline.
//
// **Gate red/green decision** (failure means rework):
//   - Green (all of ①②③④ pass): land the PJ1 emitter trait signature + amd64
//     trampoline asm;
//   - Red (any failure): the measured details of this section feed back into
//     the landed form of 06 §2.4 / §4.1, possibly changing register
//     conventions / entry protocol / W^X timing; the emitter trait must not
//     be pushed while the spike is red.
//
// Platform coverage (PJ1 spike scope):
//   - linux/amd64: the basic gates (the main body of this spike);
//   - darwin/arm64: the W^X-form spike (MAP_JIT + pthread_jit_write_protect_np
//     vs RW→RX seal, pick one of two) is deferred to before PJ8 starts; PJ1
//     main-assistant ruling item ③ is registered.
//
// Upstream contracts:
//   - docs/design/p4-method-jit/06-backends.md §1.7 spike gate discipline
//   - docs/design/p4-method-jit/05-system-pipeline.md §2 the system four-piece
//     set (exec mmap / W^X / icache / trampoline)
//   - spike/p3boundary, spike/p3indirect: the same P3 spike template
//     (independent module / three-tier samples / archived decision report)
package p4tramp
