//go:build wangshu_p4 && wangshu_profile && ((amd64 && linux) || (arm64 && (linux || (darwin && cgo)) && !wangshu_qemu))

// e2e_compare_nan_test.go — issue #103: the P4 inline compare fast
// paths must honor IEEE semantics for NaN and negative zero.
//
// Two bug classes, both caught after promotion (the interpreter and
// the exit-reason slow path were always correct):
//
//   - amd64 LT/LE: UCOMISD + a naive jcc resolved unordered (NaN) to
//     the WRONG successor in all four op/A combinations (fuzz seed
//     765ba4598e721c69: `NaN < 0` judged true turned a non-terminating
//     recursion into a terminating one). arm64 was already correct
//     (MI/PL/LS/HI family, issue #37 step 7); the fix mirrors it with
//     a jp (parity = unordered) pre-branch.
//   - both arches, EQ: inline raw-bit equality broke on the two IEEE
//     exceptions — canonNaN bits are identical but NaN ~= NaN, and
//     +/-0 bits differ but compare equal.
//
// Each kernel is loop-driven so the proto promotes, and every case is
// asserted byte-equal against the never-promoting interpreter.
package peroptranslator_test

import (
	"testing"

	"github.com/Liam0205/wangshu"
	"github.com/Liam0205/wangshu/internal/gibbous/jit/peroptranslator"
)

// runBothTiers runs src on a force-all State and an interpreter State,
// requiring promotion to actually happen and both results to match.
func runBothTiers(t *testing.T, name, src string) {
	t.Helper()
	prog, err := wangshu.Compile([]byte(src), name)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	run := func(force bool) string {
		st := wangshu.NewState(wangshu.Options{})
		st.SetForceAllPromote(force)
		beforeRun := peroptranslator.NativeRunCount.Load()
		res, err := prog.Run(st)
		if err != nil {
			t.Fatalf("run(force=%v): %v", force, err)
		}
		if force {
			if st.PromotionCount() == 0 {
				t.Fatal("PromotionCount = 0; kernel did not promote")
			}
			if peroptranslator.NativeRunCount.Load() == beforeRun {
				t.Fatal("NativeRunCount unchanged; promoted proto never ran natively")
			}
		}
		out := ""
		for i, r := range res {
			if i > 0 {
				out += "\t"
			}
			out += r.Display()
		}
		return out
	}
	interp := run(false)
	native := run(true)
	if interp != native {
		t.Fatalf("tier divergence: interp=%q native=%q", interp, native)
	}
}

// TestPJ10_Compare_NaN_LTLE — all four inline LT/LE op/A combinations
// with a NaN operand must pick the PUC successor (ordered comparison
// with NaN is false). The un-fixed amd64 emit inverted every one.
func TestPJ10_Compare_NaN_LTLE(t *testing.T) {
	cases := map[string]string{
		// LT, A=1 (`a < 0` exec-on-true)
		"lt_true_arm": `
local function k(a) if a < 0 then return 1 end return 0 end
local nan = 0/0
local s = 0
for i = 1, 20 do s = s + k(nan) end
return s`,
		// LT, A=0 (`not (a < 0)`)
		"lt_negated": `
local function k(a) if not (a < 0) then return 1 end return 0 end
local nan = 0/0
local s = 0
for i = 1, 20 do s = s + k(nan) end
return s`,
		// LE, A=1
		"le_true_arm": `
local function k(a) if a <= 0 then return 1 end return 0 end
local nan = 0/0
local s = 0
for i = 1, 20 do s = s + k(nan) end
return s`,
		// LE, A=0
		"le_negated": `
local function k(a) if not (a <= 0) then return 1 end return 0 end
local nan = 0/0
local s = 0
for i = 1, 20 do s = s + k(nan) end
return s`,
		// GT/GE lower to LT/LE with swapped operands (NaN on the K-free
		// side exercises the reg-reg shape from the other operand slot).
		"gt_arm": `
local function k(a) if a > 0 then return 1 end return 0 end
local nan = 0/0
local s = 0
for i = 1, 20 do s = s + k(nan) end
return s`,
		"ge_arm": `
local function k(a) if a >= 0 then return 1 end return 0 end
local nan = 0/0
local s = 0
for i = 1, 20 do s = s + k(nan) end
return s`,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) { runBothTiers(t, "pj10nan"+name, src) })
	}
}

// TestPJ10_Compare_NaN_Recursion — the exact fuzz shape from seed
// 765ba4598e721c69, bounded: NaN < 0 must be false so the guard never
// fires and the recursion runs to its depth cap on BOTH tiers.
func TestPJ10_Compare_NaN_Recursion(t *testing.T) {
	src := `
local function walk(n, depth)
  if n < 0 then return depth end
  if depth >= 50 then return -1 end
  return walk(n, depth + 1)
end
local nan = 0/0
local r
for i = 1, 5 do r = walk(nan, 0) end
return r`
	runBothTiers(t, "pj10nanrec", src)
}

// TestPJ10_Compare_NaN_EQ — inline raw-bit EQ vs the two IEEE
// exceptions: NaN == NaN must be false (canonNaN makes the bits
// identical) and -0.0 == 0.0 must be true (bits differ). Covers both
// A polarities (== and ~=) and the runtime-negative-zero shape that
// dodges the constant folder.
func TestPJ10_Compare_NaN_EQ(t *testing.T) {
	cases := map[string]string{
		"nan_eq": `
local function k(a, b) if a == b then return 1 end return 0 end
local nan = 0/0
local s = 0
for i = 1, 20 do s = s + k(nan, nan) end
return s`,
		"nan_neq": `
local function k(a, b) if a ~= b then return 1 end return 0 end
local nan = 0/0
local s = 0
for i = 1, 20 do s = s + k(nan, nan) end
return s`,
		"negzero_eq": `
local function k(a, b) if a == b then return 1 end return 0 end
local m = -1
local nz = 0.0 * m
local s = 0
for i = 1, 20 do s = s + k(nz, 0) end
return s`,
		"negzero_neq": `
local function k(a, b) if a ~= b then return 1 end return 0 end
local m = -1
local nz = 0.0 * m
local s = 0
for i = 1, 20 do s = s + k(nz, 0) end
return s`,
		// K-side zero: `x == 0` with x = -0.0 exercises the reg-K shape
		// (the K pins NaN away but not the +/-0 pair).
		"negzero_eq_k": `
local function k(a) if a == 0 then return 1 end return 0 end
local m = -1
local nz = 0.0 * m
local s = 0
for i = 1, 20 do s = s + k(nz) end
return s`,
		// Plain numbers must still work through the IEEE-aware form.
		"plain_eq": `
local function k(a, b) if a == b then return 1 end return 0 end
local s = 0
for i = 1, 20 do s = s + k(i, i) + k(i, i + 1) end
return s`,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) { runBothTiers(t, "pj10naneq"+name, src) })
	}
}
