//go:build wangshu_p4

// Package peroptranslator is the P4 PJ10 per-opcode translator subpackage
// (see docs/design/p4-method-jit/10-per-op-translator.md).
//
// PJ10 reworks P4 from "shape-recognize + hand-written byte-level template"
// (PJ0-PJ9) to "per-opcode translation, any reducible Proto can promote".
// This subpackage is the spike landing zone: build out the new translator
// here from scratch without touching the existing PJ0-PJ9 paths, prove the
// end-to-end pipeline on the smallest possible Proto, then graduate the
// integration into internal/gibbous/jit/compiler.go.
//
// Build tag wangshu_p4: the subpackage only compiles under the P4 build,
// same as the rest of internal/gibbous/jit. Default build is unaffected.
//
// Scope as of spike v0:
//   - emitter for `LOADK imm + RETURN A 1` shape — produces "mov rax,
//     imm64; ret" byte sequence
//   - reuses amd64.MmapCode + amd64.CallJIT for the mmap+W^X+CALL plumbing
//   - test asserts CallJIT returns the imm64 verbatim
//
// Out of scope (spike v0): CFG construction, label resolver, real Proto
// input, integration with bridge.P3Compiler. Those land in spike v1+.
package peroptranslator
