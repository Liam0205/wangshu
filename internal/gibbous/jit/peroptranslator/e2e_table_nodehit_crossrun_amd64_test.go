//go:build wangshu_p4 && wangshu_profile && amd64 && linux

// e2e_table_nodehit_crossrun_amd64_test.go — issue #67: the amd64 GETTABLE/
// SETTABLE NodeHit inline must fire ACROSS Runs, not just within a single
// Run. The original inline used a baked TableRef identity guard that
// missed 100% across Runs (a table rebuilt each Run lands at a fresh arena
// offset), so it produced zero benefit despite emitting — a within-Run
// hit count looked fine but steady-state (b.N loops / production
// re-execution) never inlined. After replacing TableRef with hmask-bounds
// + nodeRef guards (identity-free, key-field identifies the entry), the
// inline hits for same-shaped tables across Runs. This test pins that: it
// runs the same promoted program multiple times and asserts the
// exit-reason dispatch count stays LOW on later Runs (the field accesses
// inline instead of round-tripping to host.GetTable/SetTable).

package peroptranslator_test

import (
	"testing"

	"github.com/Liam0205/wangshu"
	"github.com/Liam0205/wangshu/internal/gibbous/jit/peroptranslator"
)

// TestPJ10_TableNodeHit_CrossRunInline: a kernel that reads string-keyed
// fields off a table rebuilt inside a function (so a fresh arena offset
// each call, defeating any identity guard), run repeatedly. Once promoted,
// later Runs must inline the NodeHit field reads — the per-Run dispatch
// count stays far below the field-access count. Before the guard fix this
// would stay pinned at the full field-access count on every Run.
func TestPJ10_TableNodeHit_CrossRunInline(t *testing.T) {
	// kernel builds a fresh table each call and reads 3 string-keyed
	// fields in a loop. The table is same-shaped every time (same keys,
	// same insertion order -> same node Index), so the identity-free
	// guards hit regardless of the table's arena offset.
	src := `
local function kernel(n)
  local p = { x = 3, y = 5, z = 7 }
  local acc = 0
  for i = 1, n do acc = acc + p.x + p.y + p.z end
  return acc
end
local r
for j = 1, 3 do r = kernel(2000) end   -- warm + promote
return r
`
	prog, err := wangshu.Compile([]byte(src), "pj10crossrun")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(true)

	// Warm-up Run: promotes the kernel.
	if _, err := prog.Run(st); err != nil {
		t.Fatalf("warmup run: %v", err)
	}

	// Steady-state Runs: measure dispatch per Run. Each Run does the
	// kernel's warmup loop (3 calls of kernel(2000)) again with a fresh
	// table per call. If the NodeHit reads inline, dispatch stays tiny;
	// if the identity guard were still in place (or no inline at all),
	// every one of the ~18000 field reads would exit-reason.
	for run := 0; run < 3; run++ {
		before := peroptranslator.DispatchHelperCount.Load()
		res, err := prog.Run(st)
		if err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		dispatch := peroptranslator.DispatchHelperCount.Load() - before
		t.Logf("run %d: dispatch=%d", run, dispatch)
		// Each Run: 3 kernel(2000) calls * 2000 iters * 3 field reads =
		// 18000 field reads. With cross-Run inline, dispatch must be a
		// tiny constant (cold-IC re-warm on the first promoted Run at
		// most). Assert well below the field-read count — a value near
		// 18000 means the reads are still exit-reasoning.
		if dispatch > 500 {
			t.Errorf("run %d: dispatch=%d too high — NodeHit reads not inlining "+
				"across Runs (identity-guard regression?)", run, dispatch)
		}
		// x+y+z = 15, * 2000 = 30000.
		if len(res) != 1 || res[0].Display() != "30000" {
			t.Fatalf("run %d: kernel = %v, want [30000]", run, res)
		}
	}
}
