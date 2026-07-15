package p3indirect

import (
	"context"
	"testing"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// newCompilerRuntime builds a wazero runtime in compiler mode (P3 production form; interpreter-mode data is invalid).
func newCompilerRuntime(ctx context.Context) wazero.Runtime {
	return wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfigCompiler())
}

// hostLeaf is the Go leaf imported by the kindHost form: leaf(x)=x*3+1 (same as the wasm leafBody).
func hostLeaf(_ context.Context, stack []uint64) {
	x := api.DecodeI32(stack[0])
	stack[0] = api.EncodeI32(x*3 + 1)
}

// wantDriver computes the expected value of driver(n) = Σ_{k=1..n}(k*3+1) (differential test against wasm execution).
func wantDriver(n int32) int32 {
	var acc int32
	for k := n; k > 0; k-- {
		acc += k*3 + 1
	}
	return acc
}

// registerHost registers env.h_leaf required by the kindHost form.
func registerHost(ctx context.Context, t testing.TB, rt wazero.Runtime) {
	t.Helper()
	_, err := rt.NewHostModuleBuilder("env").
		NewFunctionBuilder().
		WithGoFunction(api.GoFunc(hostLeaf), []api.ValueType{api.ValueTypeI32}, []api.ValueType{api.ValueTypeI32}).
		Export("h_leaf").
		Instantiate(ctx)
	if err != nil {
		t.Fatalf("host module: %v", err)
	}
}

// TestModulesSemantics verifies that all three module forms compile/instantiate and share the same driver(n) semantics.
// This is a spike prerequisite: only with correct hand-written bytes plus equivalence across the three forms can dispatch cost be benchmarked fairly.
func TestModulesSemantics(t *testing.T) {
	const n = int32(20)
	want := wantDriver(n)

	cases := []struct {
		name      string
		kind      callKind
		numLeaves int
		needHost  bool
	}{
		{"indirect", kindIndirect, 1, false},
		{"direct", kindDirect, 1, false},
		{"host", kindHost, 1, true},
		{"indirect_manyleaves", kindIndirect, 64, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			rt := newCompilerRuntime(ctx)
			defer rt.Close(ctx)
			if tc.needHost {
				registerHost(ctx, t, rt)
			}
			mod, err := rt.Instantiate(ctx, buildModule(tc.numLeaves, tc.kind))
			if err != nil {
				t.Fatalf("instantiate: %v", err)
			}
			fn := mod.ExportedFunction("driver")
			if fn == nil {
				t.Fatal("driver not exported")
			}
			res, err := fn.Call(ctx, api.EncodeI32(n))
			if err != nil {
				t.Fatalf("call: %v", err)
			}
			if got := api.DecodeI32(res[0]); got != want {
				t.Errorf("driver(%d) = %d, want %d", n, got, want)
			}
		})
	}
}
