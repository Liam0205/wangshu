package p3frame

import (
	"context"
	"testing"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// Stage 0 spike benchmark: compare the amortized frame build/teardown cost of
// a single leaf call across two forms.
//
//   - inwasm  : frame build/teardown fully inside Wasm (segment word writes +
//     ciDepth inc/dec + maxOpenIdx guard), zero Go crossings on the hot path.
//   - twocross: frame build/teardown via two host crossings, h_call + h_return
//     (Go does the equivalent segment word writes + ciDepth adjustment).
//
// The driver tightly loops the leaf leafN(=100000) times; ns/op ÷ leafN = the
// per-call amortized "dispatch + frame build/teardown". The delta between the
// two forms = the net cost of "two host crossings" vs "doing the same work
// inside Wasm".
//
// Gate: inwasm must be markedly faster than twocross (yardstick: enough to pull
// the call kernel from 0.49x up to ≥1x ⟹ each call saves at least an order of
// magnitude of meaningful time). allocs/op must not regress either (both should
// be ≈0).
//
// Run: cd spike/p3frame && go test -bench . -benchmem -benchtime=2s -count=3

// hostState holds the shared memory handle; h_call/h_return do the equivalent
// Go-side frame work through it.
type hostState struct{ mem api.Memory }

// hCall simulates the twocross frame build: write 4 segment words @
// segBase+depth*32 + ciDepth++.
// Uses the stack-based zero-alloc API (matching R3.5 production
// WithGoModuleFunction).
func (h *hostState) hCall(_ context.Context, _ api.Module, stack []uint64) {
	depth, _ := h.mem.ReadUint32Le(ciDepthOff)
	addr := uint32(segBase) + depth*ciWords*8
	h.mem.WriteUint64Le(addr+0, 0x11)
	h.mem.WriteUint64Le(addr+8, 0x22)
	h.mem.WriteUint64Le(addr+16, 0x33)
	h.mem.WriteUint64Le(addr+24, 0x44)
	h.mem.WriteUint32Le(ciDepthOff, depth+1)
	stack[0] = 0 // simulate base after return refresh (drop)
}

// hReturn simulates the twocross frame teardown: read maxOpenIdx guard +
// ciDepth--.
func (h *hostState) hReturn(_ context.Context, _ api.Module, stack []uint64) {
	_, _ = h.mem.ReadUint32Le(maxOpenOff) // guard read (always passes)
	depth, _ := h.mem.ReadUint32Le(ciDepthOff)
	h.mem.WriteUint32Le(ciDepthOff, depth-1)
}

// setup builds runtime + env.memory holder + spike module, returning the two
// driver entries + cleanup.
func setup(b *testing.B) (inwasm, twocross api.Function, mem api.Memory, closer func()) {
	b.Helper()
	ctx := context.Background()
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfigCompiler())

	// env.memory holder.
	envMod, err := rt.InstantiateWithConfig(ctx, buildEnvModule(),
		wazero.NewModuleConfig().WithName("env"))
	if err != nil {
		b.Fatalf("env instantiate: %v", err)
	}
	mem = envMod.Memory()
	hs := &hostState{mem: mem}

	// host module: h_call (type0 i32->i32) + h_return (type1 i32->()).
	_, err = rt.NewHostModuleBuilder("host").
		NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(hs.hCall),
		[]api.ValueType{api.ValueTypeI32}, []api.ValueType{api.ValueTypeI32}).Export("h_call").
		NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(hs.hReturn),
		[]api.ValueType{api.ValueTypeI32}, nil).Export("h_return").
		Instantiate(ctx)
	if err != nil {
		b.Fatalf("host instantiate: %v", err)
	}

	// spike module (import env.memory + host.h_call/h_return).
	mod, err := rt.InstantiateWithConfig(ctx, buildFrameModule(),
		wazero.NewModuleConfig().WithName("spike"))
	if err != nil {
		b.Fatalf("spike instantiate: %v", err)
	}
	inwasm = mod.ExportedFunction("driver_inwasm")
	twocross = mod.ExportedFunction("driver_twocross")
	if inwasm == nil || twocross == nil {
		b.Fatal("driver export missing")
	}
	return inwasm, twocross, mem, func() { _ = rt.Close(ctx) }
}

func BenchmarkFrame_Inwasm(b *testing.B) {
	inwasm, _, mem, closer := setup(b)
	defer closer()
	ctx := context.Background()
	// Initialize ciDepth=0, maxOpenIdx=0 (guard always passes).
	mem.WriteUint32Le(ciDepthOff, 0)
	mem.WriteUint32Le(maxOpenOff, 0)
	stack := make([]uint64, 1)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		stack[0] = 0 // base
		if err := inwasm.CallWithStack(ctx, stack); err != nil {
			b.Fatalf("inwasm: %v", err)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/leafN, "ns/call")
}

func BenchmarkFrame_Twocross(b *testing.B) {
	_, twocross, mem, closer := setup(b)
	defer closer()
	ctx := context.Background()
	mem.WriteUint32Le(ciDepthOff, 0)
	mem.WriteUint32Le(maxOpenOff, 0)
	mem.WriteUint32Le(segBaseWordOff, segBase)
	stack := make([]uint64, 1)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		stack[0] = 0
		if err := twocross.CallWithStack(ctx, stack); err != nil {
			b.Fatalf("twocross: %v", err)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/leafN, "ns/call")
}

// BenchmarkFrame_Guarded: frame build/teardown fully inside Wasm + the real
// runtime guards (segment base read live as a word + maxOpenIdx guard branch +
// caller gibbous bit check). Measures whether "inlined frame build/teardown
// with full guards" is still markedly faster than 2 crossings — i.e. whether
// the guard overhead eats the gain.
func BenchmarkFrame_Guarded(b *testing.B) {
	setup3 := func() (api.Function, api.Memory, func()) {
		ctx := context.Background()
		rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfigCompiler())
		envMod, err := rt.InstantiateWithConfig(ctx, buildEnvModule(),
			wazero.NewModuleConfig().WithName("env"))
		if err != nil {
			b.Fatal(err)
		}
		mem := envMod.Memory()
		hs := &hostState{mem: mem}
		if _, err := rt.NewHostModuleBuilder("host").
			NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(hs.hCall),
			[]api.ValueType{api.ValueTypeI32}, []api.ValueType{api.ValueTypeI32}).Export("h_call").
			NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(hs.hReturn),
			[]api.ValueType{api.ValueTypeI32}, nil).Export("h_return").
			Instantiate(ctx); err != nil {
			b.Fatal(err)
		}
		mod, err := rt.InstantiateWithConfig(ctx, buildFrameModule(),
			wazero.NewModuleConfig().WithName("spike"))
		if err != nil {
			b.Fatal(err)
		}
		return mod.ExportedFunction("driver_guarded"), mem, func() { _ = rt.Close(ctx) }
	}
	guarded, mem, closer := setup3()
	defer closer()
	ctx := context.Background()
	mem.WriteUint32Le(ciDepthOff, 0)
	mem.WriteUint32Le(maxOpenOff, 0)
	mem.WriteUint32Le(segBaseWordOff, segBase) // segment base mirror word (guarded reads live)
	stack := make([]uint64, 1)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		stack[0] = 0
		if err := guarded.CallWithStack(ctx, stack); err != nil {
			b.Fatalf("guarded: %v", err)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/leafN, "ns/call")
}

// TestFrame_BothComputeSame correctness: both drivers compute Σ leaf(n) =
// Σ(3n+1), confirming the frame build/teardown logic does not corrupt the leaf
// call result (spike computation is self-consistent).
func TestFrame_BothComputeSame(t *testing.T) {
	ctx := context.Background()
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfigCompiler())
	defer rt.Close(ctx)
	envMod, err := rt.InstantiateWithConfig(ctx, buildEnvModule(),
		wazero.NewModuleConfig().WithName("env"))
	if err != nil {
		t.Fatal(err)
	}
	mem := envMod.Memory()
	hs := &hostState{mem: mem}
	if _, err := rt.NewHostModuleBuilder("host").
		NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(hs.hCall),
		[]api.ValueType{api.ValueTypeI32}, []api.ValueType{api.ValueTypeI32}).Export("h_call").
		NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(hs.hReturn),
		[]api.ValueType{api.ValueTypeI32}, nil).Export("h_return").
		Instantiate(ctx); err != nil {
		t.Fatal(err)
	}
	mod, err := rt.InstantiateWithConfig(ctx, buildFrameModule(),
		wazero.NewModuleConfig().WithName("spike"))
	if err != nil {
		t.Fatal(err)
	}
	mem.WriteUint32Le(ciDepthOff, 0)
	mem.WriteUint32Le(maxOpenOff, 0)
	mem.WriteUint32Le(segBaseWordOff, segBase)
	// Σ_{n=1..leafN} (3n+1) = 3*N(N+1)/2 + N
	var want uint32
	for n := 1; n <= leafN; n++ {
		want += uint32(3*n + 1)
	}
	for _, name := range []string{"driver_inwasm", "driver_twocross", "driver_guarded"} {
		mem.WriteUint32Le(ciDepthOff, 0)
		out, err := mod.ExportedFunction(name).Call(ctx, 0)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if uint32(out[0]) != want {
			t.Errorf("%s = %d, want %d", name, uint32(out[0]), want)
		}
		// On finish ciDepth should return to 0 (frame build/teardown balanced).
		if d, _ := mem.ReadUint32Le(ciDepthOff); d != 0 {
			t.Errorf("%s 收尾 ciDepth = %d, want 0(建拆帧未配平)", name, d)
		}
	}
}
