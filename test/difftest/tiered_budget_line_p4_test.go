//go:build wangshu_p4 && wangshu_profile

package difftest

import "testing"

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
