package p3frame

import (
	"context"
	"testing"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// Stage 0 spike 基准:对比两形态单次 leaf 调用的「建拆帧」摊销成本。
//
//   - inwasm  :建拆帧全 Wasm 内(段字写 + ciDepth 增减 + maxOpenIdx 守卫),热路径零 Go 跨界。
//   - twocross:建拆帧经 h_call + h_return 两次 host 跨界(Go 做等价段字写 + ciDepth 调整)。
//
// driver 紧循环调 leaf leafN(=100000)次,ns/op ÷ leafN = 单次「dispatch + 建拆帧」摊销。
// 两形态差值 = 「两次 host 跨界」vs「Wasm 内做同样工作」的净成本。
//
// 闸门:inwasm 必须显著快过 twocross(标尺:足以把 call 核从 0.49x 拉到 ≥1x ⟹
// 每次调用至少省下一个数量级可观的时间)。同时 allocs/op 不回退(两者都应 ≈0)。
//
// 运行:cd spike/p3frame && go test -bench . -benchmem -benchtime=2s -count=3

// hostState 持共享 memory 句柄,h_call/h_return 经它做等价 Go 侧帧工作。
type hostState struct{ mem api.Memory }

// hCall 模拟 twocross 建帧:写 4 段字 @ segBase+depth*32 + ciDepth++。
// 用 stack-based 零分配 API(对齐 R3.5 生产 WithGoModuleFunction)。
func (h *hostState) hCall(_ context.Context, _ api.Module, stack []uint64) {
	depth, _ := h.mem.ReadUint32Le(ciDepthOff)
	addr := uint32(segBase) + depth*ciWords*8
	h.mem.WriteUint64Le(addr+0, 0x11)
	h.mem.WriteUint64Le(addr+8, 0x22)
	h.mem.WriteUint64Le(addr+16, 0x33)
	h.mem.WriteUint64Le(addr+24, 0x44)
	h.mem.WriteUint32Le(ciDepthOff, depth+1)
	stack[0] = 0 // 模拟返回刷新后 base(drop)
}

// hReturn 模拟 twocross 拆帧:读 maxOpenIdx 守卫 + ciDepth--。
func (h *hostState) hReturn(_ context.Context, _ api.Module, stack []uint64) {
	_, _ = h.mem.ReadUint32Le(maxOpenOff) // 守卫读取(恒过)
	depth, _ := h.mem.ReadUint32Le(ciDepthOff)
	h.mem.WriteUint32Le(ciDepthOff, depth-1)
}

// setup 建 runtime + env.memory holder + spike module,返回两 driver 入口 + 清理。
func setup(b *testing.B) (inwasm, twocross api.Function, mem api.Memory, closer func()) {
	b.Helper()
	ctx := context.Background()
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfigCompiler())

	// env.memory holder。
	envMod, err := rt.InstantiateWithConfig(ctx, buildEnvModule(),
		wazero.NewModuleConfig().WithName("env"))
	if err != nil {
		b.Fatalf("env instantiate: %v", err)
	}
	mem = envMod.Memory()
	hs := &hostState{mem: mem}

	// host module:h_call(type0 i32->i32)+ h_return(type1 i32->())。
	_, err = rt.NewHostModuleBuilder("host").
		NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(hs.hCall),
		[]api.ValueType{api.ValueTypeI32}, []api.ValueType{api.ValueTypeI32}).Export("h_call").
		NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(hs.hReturn),
		[]api.ValueType{api.ValueTypeI32}, nil).Export("h_return").
		Instantiate(ctx)
	if err != nil {
		b.Fatalf("host instantiate: %v", err)
	}

	// spike module(import env.memory + host.h_call/h_return)。
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
	// 初始化 ciDepth=0, maxOpenIdx=0(守卫恒过)。
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

// BenchmarkFrame_Guarded:建拆帧全 Wasm 内 + 真实运行期守卫(段基址现读字 +
// maxOpenIdx 守卫分支 + caller gibbous 位检查)。量「带完整守卫的内联帧建拆」是否
// 仍显著快过 2 跨界——守卫开销会不会吞掉收益。
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
	mem.WriteUint32Le(segBaseWordOff, segBase) // 段基址镜像字(guarded 现读)
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

// TestFrame_BothComputeSame 正确性:两 driver 都算 Σ leaf(n) = Σ(3n+1),
// 确认建拆帧逻辑不破坏 leaf 调用结果(spike 计算自洽)。
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
		// 收尾 ciDepth 应回 0(建拆帧配平)。
		if d, _ := mem.ReadUint32Le(ciDepthOff); d != 0 {
			t.Errorf("%s 收尾 ciDepth = %d, want 0(建拆帧未配平)", name, d)
		}
	}
}
