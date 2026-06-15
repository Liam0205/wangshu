package p3indirect

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// S-B:增量升层 module 生命周期可行性(PW10 生死未知数)。
//
// 问题:主库每 Proto 在不同时刻按热度独立升层,但「单 module + call_indirect」
// 要求所有 gibbous 函数住一个 module。wazero 不支持向已实例化 module 追加函数
// (core Wasm 无此机制),故增量升层 = 「重编含新函数的整个 module + 实例化为
// 新实例」。本组测验证该方案可行 + 安全:
//
//   - SB1:跨实例共享 memory 可见性——多个 module 实例 import 同一 env.memory,
//     一个实例写、另一个读到。这是「新旧实例共存、共用同一 arena 底座」的物理基础。
//   - SB2:增量重编 + 双实例共存——编 module{1 leaf} 实例化 → 再编 module{N leaf}
//     实例化为新实例,两实例同时可调用(旧实例不必 Close)。测 CompileModule 随
//     函数数的成本伸缩(每次升层一次性事件,ms 级是否可接受)。
//   - SB3:re-entrant 升层(最硬的生命周期险点)——旧实例的 driver 执行**中途**
//     (Go 栈上有它的帧)经 imported h_promote 回 Go,此时编译 + 实例化一个新
//     module 并调用它,再返回旧 driver 续跑。验证「A 帧在飞时升层 B」不崩、
//     旧实例不被误 Close、新实例正常执行。

// instantiateEnv 实例化 env.memory 单例(显式命名 "env" 供 import 解析——
// 手写二进制无 name section,须经 ModuleConfig.WithName 赋名)。
func instantiateEnv(ctx context.Context, t *testing.T, rt wazero.Runtime) api.Module {
	t.Helper()
	envMod, err := rt.InstantiateWithConfig(ctx, envMemModule(),
		wazero.NewModuleConfig().WithName("env"))
	if err != nil {
		t.Fatalf("env instantiate: %v", err)
	}
	return envMod
}

// SB1:跨实例共享 memory 可见性。
func TestSB1_SharedMemoryCrossInstance(t *testing.T) {
	ctx := context.Background()
	rt := newCompilerRuntime(ctx)
	defer rt.Close(ctx)

	// env.memory 单例(holder 等价)。
	envMod := instantiateEnv(ctx, t, rt)
	mem := envMod.ExportedMemory("memory")

	// 两个独立 gibbous 实例,都 import 同一 env.memory。
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

	// A 写 memory[base=0](driver 把 leaf(1)=4 写到 [0])。
	if _, err := instA.ExportedFunction("driver").Call(ctx, api.EncodeI32(0)); err != nil {
		t.Fatalf("instA driver: %v", err)
	}
	gotA, _ := mem.ReadUint64Le(0)
	if gotA != 4 { // leaf(1)=1*3+1=4
		t.Errorf("after A: memory[0] = %d, want 4", gotA)
	}

	// B 写 memory[base=8];A 写的 [0] 仍可见(共享同一块内存)。
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

// SB2:增量重编 + 双实例共存(旧实例不必 Close)。
func TestSB2_IncrementalRecompileCoexist(t *testing.T) {
	ctx := context.Background()
	rt := newCompilerRuntime(ctx)
	defer rt.Close(ctx)
	envMod := instantiateEnv(ctx, t, rt)
	mem := envMod.ExportedMemory("memory")

	// 「升层版本 1」:module 含 1 个 leaf。
	v1, err := rt.InstantiateWithConfig(ctx, buildMemModule(1, false),
		wazero.NewModuleConfig().WithName("gibV1"))
	if err != nil {
		t.Fatalf("v1: %v", err)
	}
	// 「升层版本 2」:重编含 4 个 leaf 的整个 module,实例化为新实例。
	v2, err := rt.InstantiateWithConfig(ctx, buildMemModule(4, false),
		wazero.NewModuleConfig().WithName("gibV2"))
	if err != nil {
		t.Fatalf("v2: %v", err)
	}

	// 两实例都仍可调用(v1 未被 Close)。
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

// wantSumLeaves Σ_{i=1..n}(i*3+1)(memDriverBody 展开累加的期望)。
func wantSumLeaves(n int) int {
	acc := 0
	for i := 1; i <= n; i++ {
		acc += i*3 + 1
	}
	return acc
}

// SB3:re-entrant 升层——旧 driver 在飞时编译+实例化+调用新 module。
func TestSB3_ReentrantPromotion(t *testing.T) {
	ctx := context.Background()
	rt := newCompilerRuntime(ctx)
	defer rt.Close(ctx)
	envMod := instantiateEnv(ctx, t, rt)
	mem := envMod.ExportedMemory("memory")

	var innerRan bool
	// h_promote:模拟「A 帧在 Go 栈上时升层 B」——编译 + 实例化一个新 gibbous
	// module 并立即调用它。这是最硬的生命周期险点(旧实例 A 的帧此刻在飞)。
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

	// 外层 module 带 h_promote 钩(driver 中途回 Go 升层）。
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
	// 外层 driver 正常返回(leaf(1)=4),证明 re-entrant 升层后旧帧续跑不崩。
	if got := api.DecodeI32(res[0]); got != 4 {
		t.Errorf("outer driver = %d, want 4 (re-entrant 后旧帧续跑)", got)
	}
	// 内层写 memory[64];外层写 memory[0];都可见。
	innerVal, _ := mem.ReadUint64Le(64)
	if innerVal != uint64(uint32(wantSumLeaves(2))) {
		t.Errorf("inner wrote memory[64] = %d, want %d", innerVal, wantSumLeaves(2))
	}
}
