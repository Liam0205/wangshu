//go:build wangshu_p4 && wangshu_profile && ((amd64 && linux) || (arm64 && (linux || (darwin && cgo)) && !wangshu_qemu))

// e2e_forprep_stackgrow_test.go — regression tests for the two native-
// tier miscompiles the FuzzAutoPromote harness caught in its first days
// (issues #78 and #80). Both fuzz crashers are also kept as corpus
// seeds under testdata/fuzz/FuzzAutoPromote/.

package peroptranslator_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
)

// runForced compiles src and runs it once under force-all promotion,
// returning the Display-joined results or the error text.
func runForced(t *testing.T, src string) (string, error) {
	t.Helper()
	prog, err := wangshu.Compile([]byte(src), "e2e78")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(true)
	res, rerr := prog.Run(st)
	if rerr != nil {
		return "", rerr
	}
	parts := make([]string, len(res))
	for i, r := range res {
		parts[i] = r.Display()
	}
	return strings.Join(parts, "\t"), nil
}

// TestPJ10_ForPrep_NonNumberLimitRaises (issue #78): a promoted proto
// whose FORPREP receives a non-number limit must raise exactly like the
// interpreter ("'for' limit must be a number"), not silently exit the
// loop. The kernel is called with a nil limit only AFTER promotion, so
// the native FORPREP guard (not the interpreter) is what raises.
func TestPJ10_ForPrep_NonNumberLimitRaises(t *testing.T) {
	src := `
local function f(n) for i = 0, n do end return 0 end
for i = 1, 5 do f(3) end -- promote with numeric limits
return f(nil)
`
	_, err := runForced(t, src)
	if err == nil {
		t.Fatal("promoted FORPREP with nil limit returned instead of raising")
	}
	if !strings.Contains(err.Error(), "'for' limit must be a number") {
		t.Fatalf("wrong error: %v", err)
	}
}

// TestPJ10_ForPrep_NonNumberInitAndStepRaise: the other two slots.
func TestPJ10_ForPrep_NonNumberInitAndStepRaise(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{"init", `
local function f(x) for i = x, 3 do end return 0 end
for i = 1, 5 do f(1) end
return f({})`, "'for' initial value must be a number"},
		{"step", `
local function f(s) for i = 1, 3, s do end return 0 end
for i = 1, 5 do f(1) end
return f("x")`, "'for' step must be a number"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := runForced(t, c.src)
			if err == nil {
				t.Fatalf("promoted FORPREP with bad %s returned instead of raising", c.name)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("wrong error: %v (want %q)", err, c.want)
			}
		})
	}
}

// TestPJ10_ForPrep_CoercibleStringLimit: a string limit that COERCES
// ("10") must run the loop like the interpreter does — the guard-miss
// path routes through host.ForPrep's coercion, not straight to a raise.
func TestPJ10_ForPrep_CoercibleStringLimit(t *testing.T) {
	src := `
local function f(n) local s = 0 for i = 1, n do s = s + i end return s end
for i = 1, 5 do f(4) end
return f("10")
`
	got, err := runForced(t, src)
	if err != nil {
		t.Fatalf("coercible string limit raised: %v", err)
	}
	if got != "55" {
		t.Fatalf("f(\"10\") = %s, want 55", got)
	}
}

// TestPJ10_ForPrep_NaNLimitZeroIter: a NaN limit is a genuine number —
// it must stay on the fast path and match the interpreter's
// zero-iteration semantics (the original #78 fuzz crasher's `0%0`).
func TestPJ10_ForPrep_NaNLimitZeroIter(t *testing.T) {
	src := `
local function f(n) local c = 0 for i = 0, n do c = c + 1 end return c end
for i = 1, 5 do f(2) end
return f(0/0)
`
	got, err := runForced(t, src)
	if err != nil {
		t.Fatalf("NaN limit raised: %v", err)
	}
	if got != "0" {
		t.Fatalf("f(0/0) = %s, want 0 (zero iterations)", got)
	}
}

// TestPJ10_SegCall_DeepRecursionStackGrow (issue #80): two-call
// recursion deep enough that the callee frames overrun the initial
// 64-slot value-stack segment. Before the fix this silently corrupted
// neighboring arena objects (wrong sums, "attempt to call a number
// value"): the interpreter path relocated the stack in enterLuaFrame
// but the segment re-entered at the STALE base, and the seg2seg fast
// body never bounds-checked the callee frame at all. Sweep depths
// around the onset (was depth >= 20 with 64 slots) plus a deep tail.
func TestPJ10_SegCall_DeepRecursionStackGrow(t *testing.T) {
	for _, d := range []int{12, 18, 20, 22, 30, 70} {
		t.Run(fmt.Sprintf("depth%d", d), func(t *testing.T) {
			src := fmt.Sprintf(`
local function g(n) if n == 0 then return 0 end local z = 0 return g(n-1) + g(z) end
return g(%d)`, d)
			got, err := runForced(t, src)
			if err != nil {
				t.Fatalf("depth %d raised: %v", d, err)
			}
			if got != "0" {
				t.Fatalf("g(%d) = %s, want 0", d, got)
			}
		})
	}
}

// TestPJ10_SegCall_DeepRecursionAccumulates: same shape but the result
// actually accumulates, so any frame corruption shows up as a wrong sum
// rather than only as a tag error.
func TestPJ10_SegCall_DeepRecursionAccumulates(t *testing.T) {
	// sum(n) = n + sum(n-1) + sum(0), sum(0)=0 -> n*(n+1)/2.
	src := `
local function s(n) if n == 0 then return 0 end local z = 0 return n + s(n-1) + s(z) end
return s(40)
`
	got, err := runForced(t, src)
	if err != nil {
		t.Fatalf("raised: %v", err)
	}
	if got != "820" {
		t.Fatalf("s(40) = %s, want 820", got)
	}
}
