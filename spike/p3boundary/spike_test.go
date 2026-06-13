package p3boundary

import (
	"context"
	"testing"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// newCompilerRuntime 建一个**编译模式** wazero runtime(非解释器)。
// P3 生产形态是编译模式;解释模式数据无效(01-spike-gate §1.4)。
func newCompilerRuntime(ctx context.Context) wazero.Runtime {
	return wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfigCompiler())
}

// TestModulesInstantiate 验证四个手写 wasm 二进制能被 wazero 编译 + 实例化。
// 这是 spike 的前置:手写字节正确才能跑 benchmark。
func TestModulesInstantiate(t *testing.T) {
	ctx := context.Background()
	rt := newCompilerRuntime(ctx)
	defer rt.Close(ctx)

	// S1
	s1, err := rt.Instantiate(ctx, s1Wasm)
	if err != nil {
		t.Fatalf("S1 instantiate: %v", err)
	}
	if s1.ExportedFunction("noop") == nil {
		t.Error("S1: noop not exported")
	}

	// S2
	s2, err := rt.Instantiate(ctx, s2Wasm)
	if err != nil {
		t.Fatalf("S2 instantiate: %v", err)
	}
	if s2.ExportedFunction("rw") == nil {
		t.Error("S2: rw not exported")
	}
	if s2.ExportedMemory("mem") == nil {
		t.Error("S2: mem not exported")
	}

	// S3 需要先注册 host module "env"
	_, err = rt.NewHostModuleBuilder("env").
		NewFunctionBuilder().
		WithFunc(func() {}).
		Export("h_noop").
		Instantiate(ctx)
	if err != nil {
		t.Fatalf("S3 host module: %v", err)
	}
	s3, err := rt.Instantiate(ctx, s3Wasm)
	if err != nil {
		t.Fatalf("S3 instantiate: %v", err)
	}
	if s3.ExportedFunction("callout") == nil {
		t.Error("S3: callout not exported")
	}

	// S4 long loop
	s4, err := rt.Instantiate(ctx, s4LongLoopWasm)
	if err != nil {
		t.Fatalf("S4 instantiate: %v", err)
	}
	if s4.ExportedFunction("loop") == nil {
		t.Error("S4: loop not exported")
	}
}

// TestS2Semantics 验证 S2 模块语义正确:i64.load offset=8 → i64.store offset=0。
// 这同时是 memory 共见的最小验证(Go 写 → Wasm 读 → Wasm 写 → Go 读)。
func TestS2Semantics(t *testing.T) {
	ctx := context.Background()
	rt := newCompilerRuntime(ctx)
	defer rt.Close(ctx)

	mod, err := rt.Instantiate(ctx, s2Wasm)
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	fn := mod.ExportedFunction("rw")
	mem := mod.ExportedMemory("mem")

	// Go 侧写 offset=8 处一个值
	const want = uint64(0xC0FFEE_DEADBEEF)
	if !mem.WriteUint64Le(8, want) {
		t.Fatal("WriteUint64Le failed")
	}
	// 先把 offset=0 清成别的值,确认 store 真的写了
	mem.WriteUint64Le(0, 0xAAAA)

	// 调 Wasm:它把 [8] load 出来 store 到 [0]
	res, err := fn.Call(ctx, 0)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if len(res) != 1 || api.DecodeI32(res[0]) != 0 {
		t.Errorf("status = %v, want [0]", res)
	}

	// Go 侧读 offset=0,应等于 want(Wasm 写的值,Go 直接读到 — 共见)
	got, ok := mem.ReadUint64Le(0)
	if !ok {
		t.Fatal("ReadUint64Le failed")
	}
	if got != want {
		t.Errorf("memory shared mismatch: got %#x, want %#x", got, want)
	}
}
