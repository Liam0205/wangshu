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
			define, err := wangshu.Compile([]byte(`function budget339(t, u)`+c.pad+`
  local s = 0
  for i = 1,
    u do
    s = s + i
  end
  return s
end`), "budget")
			if err != nil {
				t.Fatalf("wangshu compile: %v", err)
			}
			call, err := wangshu.Compile([]byte(`local r = budget339({1, 2}, BOUND339) return r`), "call")
			if err != nil {
				t.Fatalf("wangshu compile: %v", err)
			}
			run := func(force bool) (string, int64) {
				st := newInterpBaselineState()
				if _, err := define.Run(st); err != nil {
					t.Fatalf("define: %v", err)
				}
				if force {
					st.SetForceAllPromote(true)
				}
				st.SetGlobal("BOUND339", wangshu.Number(3))
				for i := 0; i < 4; i++ {
					if _, err := call.Run(st); err != nil {
						t.Fatalf("warm call %d: %v", i, err)
					}
				}
				st.SetGlobal("BOUND339", wangshu.Number(1e12))
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
		})
	}
}
