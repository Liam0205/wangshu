//go:build wangshu_p4 && wangshu_profile

// P4 gibbous-jit tier: realworld five scripts under force-all promotion,
// same shape as realworld_gibbous_test.go but on the P4 self-managed
// native codegen path (amd64/arm64 mmap+trampoline) instead of P3 wazero.
//
// Only compiled under wangshu_p4 && wangshu_profile: p4 provides the real
// gibbous JIT Compiler; profile enables OnEnter/OnBackEdge sampling
// (force-all triggers promotion through it). Default tag does not compile
// this file; wangshu_p3 does not either (p3/p4 mutually exclusive build
// tags per architecture.md section 1 layout).
//
// Non-empty guarantee (per PW9 empty-test lesson): the five scripts each
// contain hot inner functions (fib recursion / spectralnorm A/Av/Atv /
// nbody advance / etc.) which force-all promotes to P4 native code. A
// warmup Run drives the first-call promotion (first invocation stays on
// crescent, subsequent invocations go through the P4 trampoline).
//
// Run:
//   go test -tags "wangshu_p4 wangshu_profile" -bench 'Gibbous' ./benchmarks/realworld/

package realworld

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
	// Warmup: first Run drives hot inner functions to promote (first call
	// stays on crescent, promotion happens on entry).
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
// State reused across iterations (embedder-with-pool semantics).
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

func BenchmarkFib_GibbousJIT(b *testing.B)              { benchVMGibbousJITForce(b, "fib") }
func BenchmarkBinaryTrees_GibbousJIT(b *testing.B)      { benchVMGibbousJITForce(b, "binarytrees") }
func BenchmarkSpectralNorm_GibbousJIT(b *testing.B)     { benchVMGibbousJITForce(b, "spectralnorm") }
func BenchmarkFannkuch_GibbousJIT(b *testing.B)         { benchVMGibbousJITForce(b, "fannkuch") }
func BenchmarkNBody_GibbousJIT(b *testing.B)            { benchVMGibbousJITForce(b, "nbody") }
func BenchmarkFib_GibbousJITAuto(b *testing.B)          { benchVMGibbousJITAuto(b, "fib") }
func BenchmarkBinaryTrees_GibbousJITAuto(b *testing.B)  { benchVMGibbousJITAuto(b, "binarytrees") }
func BenchmarkSpectralNorm_GibbousJITAuto(b *testing.B) { benchVMGibbousJITAuto(b, "spectralnorm") }
func BenchmarkFannkuch_GibbousJITAuto(b *testing.B)     { benchVMGibbousJITAuto(b, "fannkuch") }
func BenchmarkNBody_GibbousJITAuto(b *testing.B)        { benchVMGibbousJITAuto(b, "nbody") }
