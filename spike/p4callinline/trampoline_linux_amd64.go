//go:build linux && amd64

package p4callinline

// CallSeg jumps into an mmap segment; the segment ends with `ret`
// (return value in RAX). Args are passed in RCX / RDX / R8 — the
// spike segments read them directly (mirrors the production spec
// trampoline's "registers pre-loaded before CALL" protocol).
func CallSeg(codeAddr, rcx, rdx, r8 uintptr) uint64
