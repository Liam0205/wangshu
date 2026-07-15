package p3boundary

import (
	"context"
	"testing"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// newCompilerRuntime builds a wazero runtime in **compiler mode** (not the interpreter).
// The P3 production form is compiler mode; interpreter-mode data is invalid (01-spike-gate §1.4).
func newCompilerRuntime(ctx context.Context) wazero.Runtime {
	return wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfigCompiler())
}

// TestModulesInstantiate verifies that the four hand-written wasm binaries can be compiled + instantiated by wazero.
// This is a prerequisite for the spike: only correct hand-written bytes can run the benchmark.
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

	// S3 requires registering the host module "env" first
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

// TestS2Semantics verifies the S2 module has correct semantics: i64.load offset=8 → i64.store offset=0.
// This is also the minimal verification that memory is shared (Go writes → Wasm reads → Wasm writes → Go reads).
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

	// On the Go side, write a value at offset=8
	const want = uint64(0xC0FFEE_DEADBEEF)
	if !mem.WriteUint64Le(8, want) {
		t.Fatal("WriteUint64Le failed")
	}
	// First set offset=0 to a different value, to confirm the store actually wrote
	mem.WriteUint64Le(0, 0xAAAA)

	// Call Wasm: it loads [8] and stores it to [0]
	res, err := fn.Call(ctx, 0)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if len(res) != 1 || api.DecodeI32(res[0]) != 0 {
		t.Errorf("status = %v, want [0]", res)
	}

	// On the Go side, read offset=0; it should equal want (the value Wasm wrote, read directly by Go — shared)
	got, ok := mem.ReadUint64Le(0)
	if !ok {
		t.Fatal("ReadUint64Le failed")
	}
	if got != want {
		t.Errorf("memory shared mismatch: got %#x, want %#x", got, want)
	}
}
