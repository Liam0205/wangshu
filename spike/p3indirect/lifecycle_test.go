package p3indirect

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// S-B: feasibility of incremental-promotion module lifecycle (a PW10 life-or-death unknown).
//
// Problem: the main library promotes each Proto independently by hotness at
// different times, but "single module + call_indirect" requires all gibbous
// functions to live in one module. wazero does not support appending functions
// to an already-instantiated module (core Wasm has no such mechanism), so
// incremental promotion = "recompile the entire module including the new
// function + instantiate as a new instance". This group of tests verifies the
// approach is feasible + safe:
//
//   - SB1: cross-instance shared memory visibility — multiple module instances
//     import the same env.memory, one instance writes and another reads it.
//     This is the physical basis for "new and old instances coexist, sharing
//     the same arena substrate".
//   - SB2: incremental recompile + dual-instance coexistence — compile
//     module{1 leaf} and instantiate → then compile module{N leaf} and
//     instantiate as a new instance; both instances are callable at the same
//     time (the old instance need not be Closed). Tests how the CompileModule
//     cost scales with the number of functions (a one-off event per promotion,
//     whether ms-level is acceptable).
//   - SB3: re-entrant promotion (the hardest lifecycle risk point) — while the
//     old instance's driver is executing **midway** (its frame is on the Go
//     stack), it returns to Go via imported h_promote, at which point a new
//     module is compiled + instantiated and called, then returns to the old
//     driver to continue. Verifies "promote B while frame A is in flight" does
//     not crash, the old instance is not mistakenly Closed, and the new
//     instance executes normally.

// instantiateEnv instantiates the env.memory singleton (explicitly named "env"
// for import resolution — hand-written binaries have no name section, so a name
// must be assigned via ModuleConfig.WithName).
func instantiateEnv(ctx context.Context, t *testing.T, rt wazero.Runtime) api.Module {
	t.Helper()
	envMod, err := rt.InstantiateWithConfig(ctx, envMemModule(),
		wazero.NewModuleConfig().WithName("env"))
	if err != nil {
		t.Fatalf("env instantiate: %v", err)
	}
	return envMod
}

// SB1: cross-instance shared memory visibility.
func TestSB1_SharedMemoryCrossInstance(t *testing.T) {
	ctx := context.Background()
	rt := newCompilerRuntime(ctx)
	defer rt.Close(ctx)

	// env.memory singleton (holder equivalent).
	envMod := instantiateEnv(ctx, t, rt)
	mem := envMod.ExportedMemory("memory")

	// Two independent gibbous instances, both importing the same env.memory.
	instA, err := rt.InstantiateWithConfig(ctx, buildMemModule(1, false),
		wazero.NewModuleConfig().WithName("gibA"))
	if err != nil {
		t.Fatalf("instA: %v", err)
	}
	instB, err := rt.InstantiateWithConfig(ctx, buildMemModule(1, false),
		wazero.NewModuleConfig().WithName("gibB"))
	if err != nil {
		t.Fatalf("instB: %v", err)
	}

	// A writes memory[base=0] (driver writes leaf(1)=4 to [0]).
	if _, err := instA.ExportedFunction("driver").Call(ctx, api.EncodeI32(0)); err != nil {
		t.Fatalf("instA driver: %v", err)
	}
	gotA, _ := mem.ReadUint64Le(0)
	if gotA != 4 { // leaf(1)=1*3+1=4
		t.Errorf("after A: memory[0] = %d, want 4", gotA)
	}

	// B writes memory[base=8]; A's write to [0] is still visible (shared same memory).
	if _, err := instB.ExportedFunction("driver").Call(ctx, api.EncodeI32(8)); err != nil {
		t.Fatalf("instB driver: %v", err)
	}
	gotB, _ := mem.ReadUint64Le(8)
	gotA2, _ := mem.ReadUint64Le(0)
	if gotB != 4 {
		t.Errorf("after B: memory[8] = %d, want 4", gotB)
	}
	if gotA2 != 4 {
		t.Errorf("A's write clobbered: memory[0] = %d, want 4 (两实例共见同一 memory)", gotA2)
	}
}

// SB2: incremental recompile + dual-instance coexistence (old instance need not be Closed).
func TestSB2_IncrementalRecompileCoexist(t *testing.T) {
	ctx := context.Background()
	rt := newCompilerRuntime(ctx)
	defer rt.Close(ctx)
	envMod := instantiateEnv(ctx, t, rt)
	mem := envMod.ExportedMemory("memory")

	// "promotion version 1": module with 1 leaf.
	v1, err := rt.InstantiateWithConfig(ctx, buildMemModule(1, false),
		wazero.NewModuleConfig().WithName("gibV1"))
	if err != nil {
		t.Fatalf("v1: %v", err)
	}
	// "promotion version 2": recompile the entire module with 4 leaves, instantiate as a new instance.
	v2, err := rt.InstantiateWithConfig(ctx, buildMemModule(4, false),
		wazero.NewModuleConfig().WithName("gibV2"))
	if err != nil {
		t.Fatalf("v2: %v", err)
	}

	// Both instances are still callable (v1 was not Closed).
	if _, err := v1.ExportedFunction("driver").Call(ctx, api.EncodeI32(0)); err != nil {
		t.Fatalf("v1 driver after v2 instantiate: %v", err)
	}
	got1, _ := mem.ReadUint64Le(0)
	if got1 != 4 { // 1 leaf: Σ=leaf(1)=4
		t.Errorf("v1 driver = %d, want 4", got1)
	}
	if _, err := v2.ExportedFunction("driver").Call(ctx, api.EncodeI32(16)); err != nil {
		t.Fatalf("v2 driver: %v", err)
	}
	got2, _ := mem.ReadUint64Le(16)
	want2 := int32(wantSumLeaves(4)) // 4 leaf: Σ_{i=1..4}(i*3+1)
	if got2 != uint64(uint32(want2)) {
		t.Errorf("v2 driver = %d, want %d", got2, want2)
	}
}

// wantSumLeaves Σ_{i=1..n}(i*3+1) (the expected value of memDriverBody's unrolled accumulation).
func wantSumLeaves(n int) int {
	acc := 0
	for i := 1; i <= n; i++ {
		acc += i*3 + 1
	}
	return acc
}

// SB3: re-entrant promotion — the old driver compiles+instantiates+calls a new module while in flight.
func TestSB3_ReentrantPromotion(t *testing.T) {
	ctx := context.Background()
	rt := newCompilerRuntime(ctx)
	defer rt.Close(ctx)
	envMod := instantiateEnv(ctx, t, rt)
	mem := envMod.ExportedMemory("memory")

	var innerRan bool
	// h_promote: simulates "promote B while frame A is on the Go stack" —
	// compile + instantiate a new gibbous module and call it immediately. This
	// is the hardest lifecycle risk point (old instance A's frame is in flight now).
	_, err := rt.NewHostModuleBuilder("host").
		NewFunctionBuilder().
		WithFunc(func(ctx context.Context) {
			inner, e := rt.InstantiateWithConfig(ctx, buildMemModule(2, false),
				wazero.NewModuleConfig().WithName(fmt.Sprintf("gibInner_%d", time.Now().UnixNano())))
			if e != nil {
				t.Errorf("re-entrant instantiate failed: %v", e)
				return
			}
			defer inner.Close(ctx)
			if _, e := inner.ExportedFunction("driver").Call(ctx, api.EncodeI32(64)); e != nil {
				t.Errorf("re-entrant inner driver: %v", e)
				return
			}
			innerRan = true
		}).
		Export("h_promote").
		Instantiate(ctx)
	if err != nil {
		t.Fatalf("host module: %v", err)
	}

	// Outer module with the h_promote hook (driver returns to Go midway to promote).
	outer, err := rt.InstantiateWithConfig(ctx, buildMemModule(1, true),
		wazero.NewModuleConfig().WithName("gibOuter"))
	if err != nil {
		t.Fatalf("outer: %v", err)
	}
	res, err := outer.ExportedFunction("driver").Call(ctx, api.EncodeI32(0))
	if err != nil {
		t.Fatalf("outer driver (re-entrant promote): %v", err)
	}
	if !innerRan {
		t.Error("re-entrant inner module did not run")
	}
	// Outer driver returns normally (leaf(1)=4), proving the old frame continues without crashing after re-entrant promotion.
	if got := api.DecodeI32(res[0]); got != 4 {
		t.Errorf("outer driver = %d, want 4 (re-entrant 后旧帧续跑)", got)
	}
	// Inner writes memory[64]; outer writes memory[0]; both visible.
	innerVal, _ := mem.ReadUint64Le(64)
	if innerVal != uint64(uint32(wantSumLeaves(2))) {
		t.Errorf("inner wrote memory[64] = %d, want %d", innerVal, wantSumLeaves(2))
	}
}
