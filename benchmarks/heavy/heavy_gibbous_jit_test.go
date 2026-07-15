//go:build wangshu_p4 && wangshu_profile

// gibbous-jit (P4) tier: the three heavy scripts run tiered up to native jit via
// force-all.
//
// **Coverage expectation** (per docs/design/p4-method-jit/implementation-progress.md
// PJ7): the inner-kernel forms of the three heavy scripts (FORLOOP body with
// multiple arithmetic ops + accumulation / while single condition + float /
// self-recursion + arithmetic) are all **not** in the current P4 PJ7 form
// whitelist (single-BB value production + RETURN / FORLOOP byte-level inline
// empty body / table IC / CALL void), so analyzeShape returns a miss →
// SupportsAllOpcodes returns false → bridge TierStuck → permanently runs on
// crescent.
//
// **Expected numbers ≈ BenchmarkHeavyXxx_Wangshu** (on the P1 interpreter, P4
// refuses to tier up). This honestly exposes the coverage of the PJ7 form subset
// against real Lua hot spots, marking a followup to expand the SAO whitelist in
// PJ7+, rather than rewriting the scripts to accommodate the P4 subset (see the
// V15 note in 09-acceptance-checklist.md).
//
// Run: go test -tags "wangshu_p4 wangshu_profile" -bench 'GibbousJIT' ./benchmarks/heavy/
package heavy

import (
	"testing"

	"github.com/Liam0205/wangshu"
)

// benchVMGibbousJITForce: force-all + warmup, P4 steady-state.
func benchVMGibbousJITForce(b *testing.B, name string) {
	src := loadScript(b, name)
	prog, err := wangshu.Compile(src, name)
	if err != nil {
		b.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(true)
	if _, err := prog.Run(st); err != nil {
		b.Fatalf("warmup: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := prog.Run(st); err != nil {
			b.Fatalf("run: %v", err)
		}
	}
}

// benchVMGibbousJITAuto: production heat threshold, single long-lived
// State reused across iterations (embedder-with-pool semantics). No
// force-all, no warmup; the first few iterations stay on crescent until
// the threshold trips.
func benchVMGibbousJITAuto(b *testing.B, name string) {
	src := loadScript(b, name)
	prog, err := wangshu.Compile(src, name)
	if err != nil {
		b.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := prog.Run(st); err != nil {
			b.Fatalf("run: %v", err)
		}
	}
}

func BenchmarkHeavyArith_GibbousJIT(b *testing.B)     { benchVMGibbousJITForce(b, "heavy_arith") }
func BenchmarkHeavyRecursion_GibbousJIT(b *testing.B) { benchVMGibbousJITForce(b, "heavy_recursion") }
func BenchmarkHeavyFloatloop_GibbousJIT(b *testing.B) { benchVMGibbousJITForce(b, "heavy_floatloop") }
func BenchmarkHeavyArith_GibbousJITAuto(b *testing.B) { benchVMGibbousJITAuto(b, "heavy_arith") }
func BenchmarkHeavyRecursion_GibbousJITAuto(b *testing.B) {
	benchVMGibbousJITAuto(b, "heavy_recursion")
}
func BenchmarkHeavyFloatloop_GibbousJITAuto(b *testing.B) {
	benchVMGibbousJITAuto(b, "heavy_floatloop")
}
