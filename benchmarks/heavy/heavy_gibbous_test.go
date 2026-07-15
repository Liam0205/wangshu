//go:build wangshu_p3 && wangshu_profile

// Gibbous (P3) tier: the three heavy scripts run under force-all-lifted wazero
// execution, compared against crescent (the default tier, BenchmarkXxx_Wangshu)
// plus gopher as a third-party baseline.
//
// Compiled only under the wangshu_p3 && wangshu_profile build: p3 supplies the
// real gibbous Compiler; profile enables OnEnter/OnBackEdge sampling (force-all
// triggers promotion through it).
//
// **Non-empty guarantee**: all three heavy scripts contain an inner non-vararg
// kernel (probes confirm each lifts one Proto). Under force-all the first call
// drives promotion, and from the second call on the gibbous path is reached
// directly via call_indirect — so the gibbous path is genuinely exercised.
//
// Run: go test -tags "wangshu_p3 wangshu_profile" -bench 'Gibbous' ./benchmarks/heavy/
package heavy

import (
	"testing"

	"github.com/Liam0205/wangshu"
)

// benchVMGibbousForce: force-all promotion + warmup, measures the P3
// steady-state (all reachable Protos already lifted). This is the "upper
// bound" of what P3 can do on this script.
func benchVMGibbousForce(b *testing.B, name string) {
	src := loadScript(b, name)
	prog, err := wangshu.Compile(src, name)
	if err != nil {
		b.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(true)
	if _, err := prog.Run(st); err != nil { // warmup: drive promotion
		b.Fatalf("warmup: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := prog.Run(st); err != nil {
			b.Fatalf("run: %v", err)
		}
	}
}

// benchVMGibbousAuto: production heat-threshold promotion, single long-lived
// State reused across iterations (mirrors an embedder that keeps its State
// warm in a pool). NO force-all, NO warmup: the first few iterations
// stay on crescent until the natural threshold trips, then subsequent
// iterations run on gibbous. The bench average includes that warmup tail.
func benchVMGibbousAuto(b *testing.B, name string) {
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

func BenchmarkHeavyArith_Gibbous(b *testing.B)         { benchVMGibbousForce(b, "heavy_arith") }
func BenchmarkHeavyRecursion_Gibbous(b *testing.B)     { benchVMGibbousForce(b, "heavy_recursion") }
func BenchmarkHeavyFloatloop_Gibbous(b *testing.B)     { benchVMGibbousForce(b, "heavy_floatloop") }
func BenchmarkHeavyArith_GibbousAuto(b *testing.B)     { benchVMGibbousAuto(b, "heavy_arith") }
func BenchmarkHeavyRecursion_GibbousAuto(b *testing.B) { benchVMGibbousAuto(b, "heavy_recursion") }
func BenchmarkHeavyFloatloop_GibbousAuto(b *testing.B) { benchVMGibbousAuto(b, "heavy_floatloop") }
