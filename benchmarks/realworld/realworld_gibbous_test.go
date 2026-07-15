//go:build wangshu_p3 && wangshu_profile

// Gibbous tier: the five realworld scripts run through force-all promotion to
// wazero execution, compared three ways against crescent (the default tier,
// BenchmarkXxx_Wangshu) and gopher (PW10 R5 convention).
//
// Compiled only under the wangshu_p3 && wangshu_profile build: p3 provides the
// real gibbous Compiler plus adopted wazero memory; profile enables
// OnEnter/OnBackEdge sampling (which force-all triggers promotion through).
// Under the default tags this file is not compiled, so it does not pollute the
// gopher/crescent bench lists.
//
// **Non-empty guarantee (learned from PW9's empty-benchmark lesson)**: all five
// scripts contain hot inner functions (fib recursion / spectralnorm's A/Av/Atv
// / nbody's advance, etc.); under force-all these Protos are promoted to
// gibbous. A single warmup drives the promotion (the first call runs crescent,
// from the second call on it dispatches straight to gibbous via call_indirect).
// The top-level chunk is a vararg (excluded by F1) and is not promoted, but the
// bulk of each script's work lives in the repeatedly-called inner functions ⟹
// the gibbous path is genuinely exercised.
//
// Run: go test -tags "wangshu_p3 wangshu_profile" -bench 'Gibbous' ./benchmarks/realworld/

package realworld

import (
	"testing"

	"github.com/Liam0205/wangshu"
)

// benchVMGibbousForce: force-all + warmup, P3 steady-state (all reachable
// Protos already lifted). Upper bound of what P3 can do here.
func benchVMGibbousForce(b *testing.B, name string) {
	src := loadScript(b, name)
	prog, err := wangshu.Compile(src, name)
	if err != nil {
		b.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(true)
	// warmup: first Run drives inner hot functions to promote.
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

// benchVMGibbousAuto: production heat-threshold promotion, single
// long-lived State reused across iterations (embedder-with-pool
// semantics). No force-all, no warmup.
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

func BenchmarkFib_Gibbous(b *testing.B)              { benchVMGibbousForce(b, "fib") }
func BenchmarkBinaryTrees_Gibbous(b *testing.B)      { benchVMGibbousForce(b, "binarytrees") }
func BenchmarkSpectralNorm_Gibbous(b *testing.B)     { benchVMGibbousForce(b, "spectralnorm") }
func BenchmarkFannkuch_Gibbous(b *testing.B)         { benchVMGibbousForce(b, "fannkuch") }
func BenchmarkNBody_Gibbous(b *testing.B)            { benchVMGibbousForce(b, "nbody") }
func BenchmarkFib_GibbousAuto(b *testing.B)          { benchVMGibbousAuto(b, "fib") }
func BenchmarkBinaryTrees_GibbousAuto(b *testing.B)  { benchVMGibbousAuto(b, "binarytrees") }
func BenchmarkSpectralNorm_GibbousAuto(b *testing.B) { benchVMGibbousAuto(b, "spectralnorm") }
func BenchmarkFannkuch_GibbousAuto(b *testing.B)     { benchVMGibbousAuto(b, "fannkuch") }
func BenchmarkNBody_GibbousAuto(b *testing.B)        { benchVMGibbousAuto(b, "nbody") }
