//go:build wangshu_oracle_cgo && cgo && (wangshu_p3 || wangshu_p4) && (amd64 || !wangshu_p4)

package fuzz_test

// topPromotes reports whether this build compiles top in TestCDepthOfAPromotedFunctionCalledFromGo.
// P3 does on every architecture, and P4 does on amd64, where a function the native translator
// rejects still compiles through head-op replay.
const topPromotes = true
