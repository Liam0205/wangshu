package p3indirect

import (
	"context"
	"testing"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// S-C dispatch cost: per-call cost of a cross-module call_indirect (via a
// shared imported table), confirming there is no significant difference from
// the intra-module case (S-A ~2.5ns), i.e. crossing modules adds no extra
// dispatch penalty.
//
// Run: GOFLAGS=-mod=mod go test -run '^$' -bench BenchmarkSC -benchtime=2s -count=3
func BenchmarkSC_CrossModuleIndirect(b *testing.B) {
	ctx := context.Background()
	rt := newCompilerRuntime(ctx)
	defer rt.Close(ctx)

	if _, err := rt.InstantiateWithConfig(ctx, envTableMemModule(8),
		wazero.NewModuleConfig().WithName("env")); err != nil {
		b.Fatalf("env: %v", err)
	}
	if _, err := rt.InstantiateWithConfig(ctx, providerModule(0, 3),
		wazero.NewModuleConfig().WithName("provider0")); err != nil {
		b.Fatalf("provider0: %v", err)
	}
	caller, err := rt.InstantiateWithConfig(ctx, callerModule(0),
		wazero.NewModuleConfig().WithName("caller0"))
	if err != nil {
		b.Fatalf("caller0: %v", err)
	}
	fn := caller.ExportedFunction("driver")

	stack := make([]uint64, 1)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		stack[0] = api.EncodeI32(callN)
		if err := fn.CallWithStack(ctx, stack); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(callN), "calls/op")
}
