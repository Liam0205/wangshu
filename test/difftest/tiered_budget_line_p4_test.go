//go:build wangshu_p4 && wangshu_profile

package difftest

import (
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
)

// TestTiered_StepBudgetLineInNumericFor covers the pc a compiled numeric for hands the host when
// the step budget runs out on its back edge: the error carries the line of the FORLOOP, as in the
// interpreter, and so does the traceback that comes with it. The loop header spans two lines because luac gives the FORPREP the line of "do"
// and the FORLOOP the line of "for", so a pc that is the FORPREP's reports the other line.
//
// Two kernels: one short, which the native translators compile, and one behind errorNameFarPad,
// whose loop runs through head-op replay on amd64 (handing the host the FORPREP's pc there fails
// that case and not the short one). Each is called a few times with a small bound first, so that
// it is compiled, and then with a bound the budget cannot cover.
//
// P3 reports the line of the loop body's last statement here, in either kernel; that is not this
// test's subject, so the test is built for P4 only.
func TestTiered_StepBudgetLineInNumericFor(t *testing.T) {
	for _, c := range []struct{ name, pad string }{
		{"short_kernel", ""},
		{"beyond_pc_255", errorNameFarPad},
	} {
		t.Run(c.name, func(t *testing.T) {
			checkStepBudgetLine(t, `SMALL339, BIG339 = 3, 1e12`, `function budget339(t, u)`+c.pad+`
  local s = 0
  for i = 1,
    u do
    s = s + i
  end
  return s
end`)
		})
	}
}

// TestTiered_StepBudgetLineInGenericFor is the same for a generic for over a host iterator, whose
// back edge is the JMP after the TFORLOOP: luac gives the TFORLOOP the line of "in ... do" and
// that JMP the line of the body's last statement, so a pc that is the TFORLOOP's reports the
// other line. The interpreter checks the budget at the JMP.
//
// On amd64 the loop of either kernel runs through head-op replay: handing the host the TFORLOOP's
// pc there fails both. arm64 has no replay, and compiles both through its native translator.
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
