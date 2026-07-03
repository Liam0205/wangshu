// Package p4callinline is the issue #50 spike: can a P4 native mmap
// segment CALL directly into another mmap segment (segment-to-segment
// dispatch), and does in-segment CallInfo frame build/teardown hold up?
//
// Physics questions (mirrors spike/p3indirect for P3 PW10):
//
//	S-A: cross-page CALL — an indirect `call rax` from one PROT_RX mmap
//	     page into another, returning via plain `ret`. Both pages live
//	     in the same address space; the return address lands on
//	     whatever stack the trampoline runs (Go stack, NOSPLIT window).
//	     Measures per-call cost vs the exit-reason round trip (~600ns).
//
//	S-B: in-segment CI frame write — emulate enterLuaFrame's hot core
//	     (write 5 CI words + inc ciDepth word + set top word) as raw
//	     stores from inside the segment, and the popCallInfo mirror
//	     (dec ciDepth). Verifies the mirror-word protocol from Go's
//	     side observes the writes (syncCurFromSeg contract).
//
// Decision gate: S-A per-call cost < 20ns AND S-B round-trips
// byte-identical -> proceed with EmitCallInline; otherwise fall back
// to "in-segment frame build + one boundary hop" (red-light path).
package p4callinline
