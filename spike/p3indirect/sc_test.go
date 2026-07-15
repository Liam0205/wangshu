package p3indirect

import (
	"context"
	"testing"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// instantiateEnvTable instantiates env (exports table + memory), explicitly named "env".
func instantiateEnvTable(ctx context.Context, t *testing.T, rt wazero.Runtime, numSlots int) api.Module {
	t.Helper()
	m, err := rt.InstantiateWithConfig(ctx, envTableMemModule(numSlots),
		wazero.NewModuleConfig().WithName("env"))
	if err != nil {
		t.Fatalf("env table+mem instantiate: %v", err)
	}
	return m
}

// TestSC_CrossModuleIndirectViaSharedTable — Arch-2 make-or-break validation.
//
// The provider module self-registers leaf into an env shared-table slot via an
// active element; the caller module call_indirect's that slot to cross-module
// call provider.leaf. Passing ⇒ re-compile-free incremental promotion is feasible.
func TestSC_CrossModuleIndirectViaSharedTable(t *testing.T) {
	ctx := context.Background()
	rt := newCompilerRuntime(ctx)
	defer rt.Close(ctx)

	instantiateEnvTable(ctx, t, rt, 8)

	// provider: leaf(x)=x*3+1 self-registers into table[0].
	if _, err := rt.InstantiateWithConfig(ctx, providerModule(0, 3),
		wazero.NewModuleConfig().WithName("provider0")); err != nil {
		t.Fatalf("provider0 instantiate: %v", err)
	}
	// caller: driver call_indirect table[0].
	caller, err := rt.InstantiateWithConfig(ctx, callerModule(0),
		wazero.NewModuleConfig().WithName("caller0"))
	if err != nil {
		t.Fatalf("caller0 instantiate: %v", err)
	}

	const n = int32(10)
	res, err := caller.ExportedFunction("driver").Call(ctx, api.EncodeI32(n))
	if err != nil {
		t.Fatalf("driver call_indirect cross-module: %v", err)
	}
	// driver(n) = Σ_{k=1..n} leaf(k) = Σ (k*3+1).
	want := int32(0)
	for k := n; k > 0; k-- {
		want += k*3 + 1
	}
	if got := api.DecodeI32(res[0]); got != want {
		t.Errorf("cross-module call_indirect: driver(%d) = %d, want %d", n, got, want)
	}
}

// TestSC_IncrementalRegisterAfterCaller — incremental promotion simulation:
// after the caller is instantiated, register a new provider into another slot,
// and the caller (a different driver pointing at that other slot) can call it.
// This is exactly the core scenario of "re-compile-free existing modules, a new
// promotion only fills a table slot".
func TestSC_IncrementalRegisterAfterCaller(t *testing.T) {
	ctx := context.Background()
	rt := newCompilerRuntime(ctx)
	defer rt.Close(ctx)

	instantiateEnvTable(ctx, t, rt, 8)

	// First register a provider into slot 0 + a caller calling slot 0.
	if _, err := rt.InstantiateWithConfig(ctx, providerModule(0, 3),
		wazero.NewModuleConfig().WithName("p0")); err != nil {
		t.Fatalf("p0: %v", err)
	}
	caller0, err := rt.InstantiateWithConfig(ctx, callerModule(0),
		wazero.NewModuleConfig().WithName("c0"))
	if err != nil {
		t.Fatalf("c0: %v", err)
	}
	// c0 runs once first (at this point the table has only slot 0).
	if _, err := caller0.ExportedFunction("driver").Call(ctx, api.EncodeI32(5)); err != nil {
		t.Fatalf("c0 driver before incremental: %v", err)
	}

	// Incremental "promotion": register a new provider into slot 1
	// (leaf(x)=x*5+1), without re-compiling c0/p0.
	if _, err := rt.InstantiateWithConfig(ctx, providerModule(1, 5),
		wazero.NewModuleConfig().WithName("p1")); err != nil {
		t.Fatalf("p1 incremental register: %v", err)
	}
	// New caller calling slot 1.
	caller1, err := rt.InstantiateWithConfig(ctx, callerModule(1),
		wazero.NewModuleConfig().WithName("c1"))
	if err != nil {
		t.Fatalf("c1: %v", err)
	}
	const n = int32(10)
	res, err := caller1.ExportedFunction("driver").Call(ctx, api.EncodeI32(n))
	if err != nil {
		t.Fatalf("c1 driver call_indirect slot1: %v", err)
	}
	want := int32(0)
	for k := n; k > 0; k-- {
		want += k*5 + 1 // p1 leaf = x*5+1
	}
	if got := api.DecodeI32(res[0]); got != want {
		t.Errorf("incremental slot1: driver(%d) = %d, want %d", n, got, want)
	}

	// Old c0 (slot 0) still works normally (unaffected by re-compile-free).
	res0, err := caller0.ExportedFunction("driver").Call(ctx, api.EncodeI32(n))
	if err != nil {
		t.Fatalf("c0 driver after incremental: %v", err)
	}
	want0 := int32(0)
	for k := n; k > 0; k-- {
		want0 += k*3 + 1
	}
	if got := api.DecodeI32(res0[0]); got != want0 {
		t.Errorf("c0 after incremental: driver(%d) = %d, want %d", n, got, want0)
	}
}
