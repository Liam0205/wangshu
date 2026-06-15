package p3indirect

import (
	"context"
	"testing"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// instantiateEnvTable 实例化 env(导出 table + memory),显式命名 "env"。
func instantiateEnvTable(ctx context.Context, t *testing.T, rt wazero.Runtime, numSlots int) api.Module {
	t.Helper()
	m, err := rt.InstantiateWithConfig(ctx, envTableMemModule(numSlots),
		wazero.NewModuleConfig().WithName("env"))
	if err != nil {
		t.Fatalf("env table+mem instantiate: %v", err)
	}
	return m
}

// TestSC_CrossModuleIndirectViaSharedTable — Arch-2 生死验证。
//
// provider module 经 active element 把 leaf 自注册进 env 共享表 slot;caller module
// call_indirect 该 slot 跨 module 调到 provider.leaf。通过 ⇒ 免重编的增量升层可行。
func TestSC_CrossModuleIndirectViaSharedTable(t *testing.T) {
	ctx := context.Background()
	rt := newCompilerRuntime(ctx)
	defer rt.Close(ctx)

	instantiateEnvTable(ctx, t, rt, 8)

	// provider:leaf(x)=x*3+1 自注册进 table[0]。
	if _, err := rt.InstantiateWithConfig(ctx, providerModule(0, 3),
		wazero.NewModuleConfig().WithName("provider0")); err != nil {
		t.Fatalf("provider0 instantiate: %v", err)
	}
	// caller:driver call_indirect table[0]。
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
	// driver(n) = Σ_{k=1..n} leaf(k) = Σ (k*3+1)。
	want := int32(0)
	for k := n; k > 0; k-- {
		want += k*3 + 1
	}
	if got := api.DecodeI32(res[0]); got != want {
		t.Errorf("cross-module call_indirect: driver(%d) = %d, want %d", n, got, want)
	}
}

// TestSC_IncrementalRegisterAfterCaller — 增量升层模拟:caller 已实例化后,再注册
// 一个新 provider 到另一个 slot,caller(指向另一 slot 的另一个 driver)能调到它。
// 这正是「免重编已有 module、新升层只填表槽」的核心场景。
func TestSC_IncrementalRegisterAfterCaller(t *testing.T) {
	ctx := context.Background()
	rt := newCompilerRuntime(ctx)
	defer rt.Close(ctx)

	instantiateEnvTable(ctx, t, rt, 8)

	// 先注册 provider 到 slot 0 + 一个调 slot 0 的 caller。
	if _, err := rt.InstantiateWithConfig(ctx, providerModule(0, 3),
		wazero.NewModuleConfig().WithName("p0")); err != nil {
		t.Fatalf("p0: %v", err)
	}
	caller0, err := rt.InstantiateWithConfig(ctx, callerModule(0),
		wazero.NewModuleConfig().WithName("c0"))
	if err != nil {
		t.Fatalf("c0: %v", err)
	}
	// c0 先跑一次(此时 table 只有 slot 0)。
	if _, err := caller0.ExportedFunction("driver").Call(ctx, api.EncodeI32(5)); err != nil {
		t.Fatalf("c0 driver before incremental: %v", err)
	}

	// 增量「升层」:注册新 provider 到 slot 1(leaf(x)=x*5+1),免重编 c0/p0。
	if _, err := rt.InstantiateWithConfig(ctx, providerModule(1, 5),
		wazero.NewModuleConfig().WithName("p1")); err != nil {
		t.Fatalf("p1 incremental register: %v", err)
	}
	// 新 caller 调 slot 1。
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

	// 旧 c0(slot 0)仍正常(免重编未受影响)。
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
