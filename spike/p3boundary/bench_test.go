package p3boundary

import (
	"context"
	"testing"
)

// PW0 三档 call boundary benchmark(01-spike-gate §1.2)。
//
// 闸门:S2 < 150ns(主指标)。S1 < 80ns(下沿基线)。S3 < 150ns(慢路径助手)。
//
// 跑法(01-spike-gate §1.4):
//
//	GOFLAGS=-mod=mod go test -bench=. -benchtime=10s -count=5
//
// 锁频 + 绑核见 §1.4 纪律。ns/op 是关注指标。

// BenchmarkS1_Noop — S1 空往返:Go 调空 Wasm 函数返回。跨层固定成本下限。
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

// BenchmarkS2_ParamMemRW — S2 带参往返:传 base i32,Wasm 体内 i64.load+store,
// 返回 i32 status。**主指标**——复刻 [04-trampoline §2] gibbous 入口形状。
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

// BenchmarkS3_ImportedCall — S3 反向往返:Wasm 体内 call imported Go fn。
// 复刻 [04-trampoline §3] gibbous → host 慢路径助手形状。
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

// BenchmarkS3N_ImportedCallAmortized — S3 变体:Wasm 体内循环 call imported
// fn N 次,单次 b.N 跑一个 callout_n(N)。报告的 ns/op 是「一次 callout_n
// 调用」的成本;除以 N 得**单次 imported dispatch 的摊销成本**(摊掉 fn.Call
// 外层 ctx/stack 开销)。这是慢路径助手 T_cross 的关键输入。
//
// 用 callN=1000:外层 fn.Call 开销(~36ns 见 S2)摊到 1000 次 imported 调用,
// 可忽略;ns/op / 1000 ≈ 单次 imported dispatch 真实成本。
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
	// 报告 ns/op 是「一次 callout_n(1000)」成本;÷1000 得单次 imported dispatch
	// 摊销成本(手工换算,见函数注释)。
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

	stack := make([]uint64, 1) // 复用:入参 base=stack[0],返回值也写回 stack[0]
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		stack[0] = 0 // base = 0
		if err := fn.CallWithStack(ctx, stack); err != nil {
			b.Fatal(err)
		}
	}
}
