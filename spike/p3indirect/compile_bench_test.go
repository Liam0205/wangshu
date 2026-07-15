package p3indirect

import (
	"context"
	"testing"

	"github.com/tetratelabs/wazero"
)

// S-B cost tier: how CompileModule scales with the number of functions.
// Incremental tier-up = recompiling the whole module containing N functions
// (a one-shot event per tier-up), so the concern is whether the "compile cost of
// a module with N leaves" is acceptable at the ms scale (tier-up is not on the
// hot path, 02 §1.2).
//
// Run: GOFLAGS=-mod=mod go test -run '^$' -bench BenchmarkSB_Compile -benchtime=1s

func benchCompile(b *testing.B, numLeaves int) {
	ctx := context.Background()
	rt := newCompilerRuntime(ctx)
	defer rt.Close(ctx)
	bin := buildModule(numLeaves, kindIndirect)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cm, err := rt.CompileModule(ctx, bin)
		if err != nil {
			b.Fatalf("compile: %v", err)
		}
		_ = cm.Close(ctx)
	}
}

// A few function counts (simulating a single module with 1 / 16 / 64 / 256
// tiered-up Protos inside one Program).
func BenchmarkSB_Compile1(b *testing.B)   { benchCompile(b, 1) }
func BenchmarkSB_Compile16(b *testing.B)  { benchCompile(b, 16) }
func BenchmarkSB_Compile64(b *testing.B)  { benchCompile(b, 64) }
func BenchmarkSB_Compile256(b *testing.B) { benchCompile(b, 256) }

// BenchmarkSB_Instantiate measures the instantiation cost (the step of
// instantiating a fresh instance after recompilation).
func BenchmarkSB_Instantiate(b *testing.B) {
	ctx := context.Background()
	rt := newCompilerRuntime(ctx)
	defer rt.Close(ctx)
	cm, err := rt.CompileModule(ctx, buildModule(16, kindIndirect))
	if err != nil {
		b.Fatalf("compile: %v", err)
	}
	defer cm.Close(ctx)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		inst, err := rt.InstantiateModule(ctx, cm, wazero.NewModuleConfig().WithName("x"))
		if err != nil {
			b.Fatalf("instantiate: %v", err)
		}
		_ = inst.Close(ctx)
	}
}
