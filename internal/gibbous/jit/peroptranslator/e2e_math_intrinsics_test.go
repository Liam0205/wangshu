//go:build wangshu_p4 && wangshu_profile && ((amd64 && linux) || (arm64 && (linux || (darwin && cgo)) && !wangshu_qemu))

// e2e_math_intrinsics_test.go — issue #77: math.* intrinsic emission.
// When a CALL site's IC observes the callee is a recognized pure-numeric
// host closure (math.sqrt/floor/ceil/abs/max/min), the native segment
// emits the op inline (SQRTSD / ROUNDSD / FSQRT / ...) instead of exiting
// to run the Go closure. These assertions are ARCH-NEUTRAL: they check
// the correctness contract (result byte-equal to the interpreter, incl.
// negatives / NaN / Inf edge cases) which must hold on both arches. The
// amd64 dispatch-drop measurement lives in the cross-run amd64 test.

package peroptranslator_test

import (
	"testing"

	"github.com/Liam0205/wangshu"
	"github.com/Liam0205/wangshu/internal/gibbous/jit/peroptranslator"
)

// runIntrinsic compiles + force-promotes src and returns the single
// display result.
func runIntrinsic(t *testing.T, name, src string) string {
	t.Helper()
	prog, err := wangshu.Compile([]byte(src), name)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(true)
	res, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("got %d results, want 1: %v", len(res), res)
	}
	return res[0].Display()
}

// TestPJ10_MathIntrinsic_Correctness checks each intrinsic promotes and
// stays byte-equal to the interpreter across ordinary + edge-case inputs.
// The kernel keeps the call in an inner function called in a loop so it
// promotes and the intrinsic fast path warms.
func TestPJ10_MathIntrinsic_Correctness(t *testing.T) {
	// Each body aliases the math fn to a local (matching real JIT-friendly
	// code like benchmark n-body — a direct `math.sqrt(x)` trips the F2-b
	// unknown-call gate and the proto never promotes to native, so the
	// intrinsic path is only reached via an alias) and calls it in a loop
	// inside a function so the proto promotes and the fast path warms.
	cases := []struct {
		name string
		body string
		want string
	}{
		{"sqrt", `local f=math.sqrt local function k(x) local s=0.0 for i=1,20 do s=s+f(x) end return s end return k(16.0)`, "80"},
		{"sqrt_neg", `local f=math.sqrt local function k(x) local s=0.0 for i=1,20 do s=f(x) end return s end return k(-4.0)`, "nan"},
		{"floor", `local f=math.floor local function k(x) local s=0.0 for i=1,20 do s=s+f(x) end return s end return k(3.7)`, "60"},
		{"floor_neg", `local f=math.floor local function k(x) local s=0.0 for i=1,20 do s=s+f(x) end return s end return k(-3.2)`, "-80"},
		{"ceil", `local f=math.ceil local function k(x) local s=0.0 for i=1,20 do s=s+f(x) end return s end return k(3.2)`, "80"},
		{"ceil_neg", `local f=math.ceil local function k(x) local s=0.0 for i=1,20 do s=s+f(x) end return s end return k(-3.7)`, "-60"},
		{"abs", `local f=math.abs local function k(x) local s=0.0 for i=1,20 do s=s+f(x) end return s end return k(-5.0)`, "100"},
		{"abs_pos", `local f=math.abs local function k(x) local s=0.0 for i=1,20 do s=s+f(x) end return s end return k(5.0)`, "100"},
		{"max", `local f=math.max local function k(x,y) local s=0.0 for i=1,20 do s=s+f(x,y) end return s end return k(3.0,7.0)`, "140"},
		{"max_first", `local f=math.max local function k(x,y) local s=0.0 for i=1,20 do s=s+f(x,y) end return s end return k(9.0,2.0)`, "180"},
		{"min", `local f=math.min local function k(x,y) local s=0.0 for i=1,20 do s=s+f(x,y) end return s end return k(3.0,7.0)`, "60"},
		{"min_first", `local f=math.min local function k(x,y) local s=0.0 for i=1,20 do s=s+f(x,y) end return s end return k(2.0,9.0)`, "40"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := runIntrinsic(t, "pj10intr_"+tc.name, tc.body); got != tc.want {
				t.Fatalf("%s = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

// TestPJ10_MathIntrinsic_ByteEqualSweep is the correctness spine: for
// each intrinsic it runs the SAME script interpreted (no promotion) and
// force-promoted (intrinsic inline), over a sweep of edge-case inputs
// (negatives, fractions, ±0, Inf, NaN, huge integral values), and asserts
// the JIT result is byte-equal to the interpreter. The interpreter is
// itself difftested byte-equal to Lua 5.1.5, so this pins the inline path
// to the reference semantics without depending on random difftest happening
// to alias a math fn.
func TestPJ10_MathIntrinsic_ByteEqualSweep(t *testing.T) {
	// inputs cover: normal, negative, fraction, zero, -0, +Inf, -Inf, NaN,
	// large integral, tiny. Written as Lua expressions.
	inputs := []string{
		"3.0", "-3.0", "3.7", "-3.7", "0.0", "(-1.0)*0.0",
		"1.0/0.0", "-1.0/0.0", "0.0/0.0", "1e15", "-1e15",
		"2.5", "-2.5", "0.5", "123456.789",
	}
	unary := []string{"sqrt", "floor", "ceil", "abs"}
	for _, fn := range unary {
		for _, in := range inputs {
			src := "local f=math." + fn +
				"\nlocal function k(x) local s for i=1,15 do s=f(x) end return s end" +
				"\nreturn k(" + in + ")"
			interp := runNoPromote(t, "sweep_"+fn, src)
			jit := runIntrinsic(t, "sweep_"+fn, src)
			if interp != jit {
				t.Errorf("math.%s(%s): interp=%q jit=%q", fn, in, interp, jit)
			}
		}
	}
	// max/min: sweep pairs (order matters for NaN).
	pairs := [][2]string{
		{"3.0", "7.0"}, {"7.0", "3.0"}, {"3.0", "3.0"},
		{"0.0/0.0", "5.0"}, {"5.0", "0.0/0.0"},
		{"1.0/0.0", "5.0"}, {"-1.0/0.0", "5.0"},
		{"-0.0", "0.0"}, {"0.0", "-0.0"},
	}
	for _, fn := range []string{"max", "min"} {
		for _, p := range pairs {
			src := "local f=math." + fn +
				"\nlocal function k(x,y) local s for i=1,15 do s=f(x,y) end return s end" +
				"\nreturn k(" + p[0] + "," + p[1] + ")"
			interp := runNoPromote(t, "sweep_"+fn, src)
			jit := runIntrinsic(t, "sweep_"+fn, src)
			if interp != jit {
				t.Errorf("math.%s(%s,%s): interp=%q jit=%q", fn, p[0], p[1], interp, jit)
			}
		}
	}
}

// runNoPromote runs src WITHOUT promotion (pure interpreter) as the oracle.
func runNoPromote(t *testing.T, name, src string) string {
	t.Helper()
	prog, err := wangshu.Compile([]byte(src), name)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	res, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("got %d results, want 1: %v", len(res), res)
	}
	return res[0].Display()
}

// TestPJ10_MathIntrinsic_HitPath proves the inline path (not the
// exit-reason CALL) actually executes: IntrinsicHitCount must move once a
// sqrt kernel is promoted and re-run. A plain byte-equal assertion can't
// tell the inline path from the host round trip — both are correct.
func TestPJ10_MathIntrinsic_HitPath(t *testing.T) {
	src := `
local sqrt = math.sqrt
local function kernel(n)
  local acc = 0.0
  for i = 1, n do acc = acc + sqrt(i) end
  return acc
end
local r
for j = 1, 3 do r = kernel(500) end
return r
`
	prog, err := wangshu.Compile([]byte(src), "pj10intrhit")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(true)
	if _, err := prog.Run(st); err != nil { // warm + promote
		t.Fatalf("warmup: %v", err)
	}
	before := peroptranslator.IntrinsicHitCount.Load()
	if _, err := prog.Run(st); err != nil {
		t.Fatalf("run: %v", err)
	}
	hits := peroptranslator.IntrinsicHitCount.Load() - before
	t.Logf("IntrinsicHitCount delta = %d", hits)
	if hits == 0 {
		t.Fatal("IntrinsicHitCount didn't move — math.sqrt not inlining " +
			"(still exit-reasoning to the host closure)")
	}
}
