//go:build wangshu_oracle_cgo && cgo && amd64

package oracle

import (
	"strings"
	"testing"
)

// TestExec_ModProductIsRounded pins the oracle's '%' to the reading in which
// the product in a - floor(a/b)*b is rounded before the subtraction.
//
// A compiler that may contract the expression and targets a CPU with FMA fuses
// the multiply and the subtract, and the result changes whenever the product is
// not exactly representable. The nightly runner image began producing such a
// build on 2026-11-08 (#309 .. #320), so internal/oracle/lua515.c routes
// luai_nummod through a volatile intermediate on x86-64.
//
// A default amd64 build never fuses, so there this test passes with or without
// the override. It only has teeth when the compiler is allowed to fuse; CI and
// `make fuzz-oracle` run it a second time with CGO_CFLAGS="-O2 -mfma" for that
// reason.
//
// amd64 only: on arm64 the oracle is left fused, matching wangshu there.
func TestExec_ModProductIsRounded(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		// lcode.c constfolding: both operands are numeric literals.
		{"constant folded, huge dividend", `print(84572069678023671808 % 89)`, "0\n"},
		{"constant folded, inexact divisor", `print(3 % .1)`, "0\n"},
		// lvm.c OP_MOD fast path: operands are only known at run time.
		{"runtime, huge dividend", `local a, b = 84572069678023671808, 89 print(a % b)`, "0\n"},
		{"runtime, inexact divisor", `local a, b = 3, .1 print(a % b)`, "0\n"},
		// lvm.c Arith: a string operand takes the coercion path.
		{"string coerced", `local a, b = "84572069678023671808", 89 print(a % b)`, "0\n"},
		// The input the tiered go-fuzz step reported in #315.
		{"loop from #315", `local s = "" for i = 1, 50 do s = s .. i % .1 .. " " end print(s)`, strings.Repeat("0 ", 50) + "\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := execT(t, tc.src)
			if r.Verdict != VerdictOK {
				t.Fatalf("verdict = %v, err = %q", r.Verdict, r.Err)
			}
			if r.Output != tc.want {
				t.Fatalf("output = %q, want %q", r.Output, tc.want)
			}
		})
	}
}
