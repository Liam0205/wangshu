package p3boundary

import (
	"context"
	"testing"
)

// PW0 three-tier call boundary benchmarks (01-spike-gate §1.2).
//
// Gates: S2 < 150ns (primary metric). S1 < 80ns (lower-bound baseline). S3 < 150ns (slow-path helper).
//
// How to run (01-spike-gate §1.4):
//
//	GOFLAGS=-mod=mod go test -bench=. -benchtime=10s -count=5
//
// See §1.4 discipline for frequency locking + core pinning. ns/op is the metric of interest.

// BenchmarkS1_Noop — S1 empty round trip: Go calls an empty Wasm function that returns. The lower bound on fixed cross-layer cost.
func BenchmarkS1_Noop(b *testing.B) {
	ctx := context.Background()
	rt := newCompilerRuntime(ctx)
	defer rt.Close(ctx)

	mod, err := rt.Instantiate(ctx, s1Wasm)
	if err != nil {
		b.Fatalf("instantiate: %v", err)
	}
	fn := mod.ExportedFunction("noop")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := fn.Call(ctx); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkS2_ParamMemRW — S2 round trip with args: pass base i32, do i64.load+store
// inside the Wasm body, return i32 status. **Primary metric** -- reproduces the
// gibbous entry shape from [04-trampoline §2].
func BenchmarkS2_ParamMemRW(b *testing.B) {
	ctx := context.Background()
	rt := newCompilerRuntime(ctx)
	defer rt.Close(ctx)

	mod, err := rt.Instantiate(ctx, s2Wasm)
	if err != nil {
		b.Fatalf("instantiate: %v", err)
	}
	fn := mod.ExportedFunction("rw")
	mem := mod.ExportedMemory("mem")
	mem.WriteUint64Le(0, 0xDEADBEEF)
	mem.WriteUint64Le(8, 0xC0FFEE)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := fn.Call(ctx, 0); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkS3_ImportedCall — S3 reverse round trip: call an imported Go fn inside the Wasm body.
// Reproduces the gibbous → host slow-path helper shape from [04-trampoline §3].
func BenchmarkS3_ImportedCall(b *testing.B) {
	ctx := context.Background()
	rt := newCompilerRuntime(ctx)
	defer rt.Close(ctx)

	if _, err := rt.NewHostModuleBuilder("env").
		NewFunctionBuilder().
		WithFunc(func() {}).
		Export("h_noop").
		Instantiate(ctx); err != nil {
		b.Fatalf("host module: %v", err)
	}
	mod, err := rt.Instantiate(ctx, s3Wasm)
	if err != nil {
		b.Fatalf("instantiate: %v", err)
	}
	fn := mod.ExportedFunction("callout")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := fn.Call(ctx); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkS3N_ImportedCallAmortized — S3 variant: call an imported fn N times in
// a loop inside the Wasm body; each b.N runs one callout_n(N). The reported ns/op
// is the cost of a single callout_n call; divide by N to get the **amortized cost of
// a single imported dispatch** (amortizing away the outer fn.Call ctx/stack overhead).
// This is the key input for the slow-path helper T_cross.
//
// Using callN=1000: the outer fn.Call overhead (~36ns, see S2) is amortized over
// 1000 imported calls and becomes negligible; ns/op / 1000 ≈ the true cost of a
// single imported dispatch.
func BenchmarkS3N_ImportedCallAmortized(b *testing.B) {
	const callN = 1000
	ctx := context.Background()
	rt := newCompilerRuntime(ctx)
	defer rt.Close(ctx)

	if _, err := rt.NewHostModuleBuilder("env").
		NewFunctionBuilder().
		WithFunc(func() {}).
		Export("h_noop").
		Instantiate(ctx); err != nil {
		b.Fatalf("host module: %v", err)
	}
	mod, err := rt.Instantiate(ctx, s3NWasm)
	if err != nil {
		b.Fatalf("instantiate: %v", err)
	}
	fn := mod.ExportedFunction("callout_n")

	stack := make([]uint64, 1)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		stack[0] = callN
		if err := fn.CallWithStack(ctx, stack); err != nil {
			b.Fatal(err)
		}
	}
	// The reported ns/op is the cost of one callout_n(1000); ÷1000 gives the amortized
	// cost of a single imported dispatch (compute by hand, see function comment).
}

func BenchmarkS2_CallWithStack(b *testing.B) {
	ctx := context.Background()
	rt := newCompilerRuntime(ctx)
	defer rt.Close(ctx)

	mod, err := rt.Instantiate(ctx, s2Wasm)
	if err != nil {
		b.Fatalf("instantiate: %v", err)
	}
	fn := mod.ExportedFunction("rw")
	mem := mod.ExportedMemory("mem")
	mem.WriteUint64Le(0, 0xDEADBEEF)
	mem.WriteUint64Le(8, 0xC0FFEE)

	stack := make([]uint64, 1) // reused: input base=stack[0], return value also written back to stack[0]
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		stack[0] = 0 // base = 0
		if err := fn.CallWithStack(ctx, stack); err != nil {
			b.Fatal(err)
		}
	}
}
