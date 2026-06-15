package p3indirect

import (
	"context"
	"testing"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// newCompilerRuntime 建编译模式 wazero runtime(P3 生产形态;解释模式数据无效)。
func newCompilerRuntime(ctx context.Context) wazero.Runtime {
	return wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfigCompiler())
}

// hostLeaf 是 kindHost 形态 import 的 Go leaf:leaf(x)=x*3+1(与 wasm leafBody 同)。
func hostLeaf(_ context.Context, stack []uint64) {
	x := api.DecodeI32(stack[0])
	stack[0] = api.EncodeI32(x*3 + 1)
}

// wantDriver 计算 driver(n) = Σ_{k=1..n}(k*3+1) 的期望值(对拍 wasm 执行)。
func wantDriver(n int32) int32 {
	var acc int32
	for k := n; k > 0; k-- {
		acc += k*3 + 1
	}
	return acc
}

// registerHost 注册 kindHost 形态需要的 env.h_leaf。
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

// TestModulesSemantics 验证三形态 module 都能编译/实例化且 driver(n) 语义一致。
// 这是 spike 前置:手写字节正确 + 三形态等价才能公平 benchmark dispatch 成本。
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
