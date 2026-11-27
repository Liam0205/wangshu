//go:build (wangshu_p3 || wangshu_p4) && wangshu_profile

package difftest

import (
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
)

// TestTiered_StepBudgetLineInGenericFor covers the pc a compiled generic for over a host iterator
// hands the host when the step budget runs out on its back edge, the JMP after the TFORLOOP: the
// error and its traceback carry that JMP's line, as in the interpreter. luac gives the TFORLOOP
// the line of "in ... do" and the JMP the line of the body's last statement, so a pc that is the
// TFORLOOP's reports the other line.
//
// Two kernels, one short and one behind errorNameFarPad. On amd64 P4 runs the loop of either
// through head-op replay: handing the host the TFORLOOP's pc there fails both. P4 on arm64 has no
// replay and compiles both through its native translator. P3 compiles both on every architecture.
func TestTiered_StepBudgetLineInGenericFor(t *testing.T) {
	for _, c := range []struct{ name, pad string }{
		{"short_kernel", ""},
		{"beyond_pc_255", errorNameFarPad},
	} {
		t.Run(c.name, func(t *testing.T) {
			checkStepBudgetLine(t, `SMALL339, BIG339 = {1, 2, 3}, {}
for i = 1, 300000 do BIG339[i] = i end`, `function budget339(t, u)`+c.pad+`
  local nx = next
  local s = 0
  for k
    in nx, u do
    s = s + 1
  end
  return s
end`)
		})
	}
}

// checkStepBudgetLine defines the globals of setup and the kernel budget339(t, u), calls the
// kernel a few times with SMALL339 so that it is compiled, and then with BIG339 under a step
// budget the loop cannot finish in. The error text, traceback included, has to be the same with
// the interpreter alone and under force-all, and force-all has to enter compiled code.
func checkStepBudgetLine(t *testing.T, setup, kernel string) {
	t.Helper()
	prepare, err := wangshu.Compile([]byte(setup), "setup")
	if err != nil {
		t.Fatalf("wangshu compile: %v", err)
	}
	define, err := wangshu.Compile([]byte(kernel), "budget")
	if err != nil {
		t.Fatalf("wangshu compile: %v", err)
	}
	call, err := wangshu.Compile([]byte(`local r = budget339({1, 2}, USEBIG339 and BIG339 or SMALL339) return r`), "call")
	if err != nil {
		t.Fatalf("wangshu compile: %v", err)
	}
	run := func(force bool) (string, int64) {
		st := newInterpBaselineState()
		if _, err := prepare.Run(st); err != nil {
			t.Fatalf("setup: %v", err)
		}
		if _, err := define.Run(st); err != nil {
			t.Fatalf("define: %v", err)
		}
		if force {
			st.SetForceAllPromote(true)
		}
		for i := 0; i < 4; i++ {
			if _, err := call.Run(st); err != nil {
				t.Fatalf("warm call %d: %v", i, err)
			}
		}
		st.SetGlobal("USEBIG339", wangshu.Bool(true))
		st.SetStepBudget(200000)
		_, err := call.Run(st)
		if err == nil {
			t.Fatalf("force-all %v: the loop finished within the budget", force)
		}
		return err.Error(), st.GibbousEntries()
	}
	want, n := run(false)
	if n != 0 {
		t.Errorf("the interpreter-only run entered compiled code %d times", n)
	}
	if first, _, _ := strings.Cut(want, "\n"); !strings.HasSuffix(first, "instruction budget exceeded") {
		t.Fatalf("interpreter: %q, want the budget error", want)
	}
	got, n := run(true)
	if n == 0 {
		t.Errorf("force-all never entered compiled code: this kernel does not exercise the tier")
	}
	if got != want {
		t.Errorf("force-all diverged from the interpreter:\n  force-all:   %q\n  interpreter: %q", got, want)
	}
	t.Logf("%q; entries into compiled code under force-all: %d", want, n)
}
