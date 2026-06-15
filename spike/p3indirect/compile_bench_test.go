package p3indirect

import (
	"context"
	"testing"

	"github.com/tetratelabs/wazero"
)

// S-B 成本档:CompileModule 随函数数的伸缩。增量升层 = 重编含 N 个函数的整个
// module(每次升层一次性事件),故关注「N 个 leaf 的 module 编译成本」是否
// ms 级可接受(升层不在热路径,02 §1.2)。
//
// 跑法:GOFLAGS=-mod=mod go test -run '^$' -bench BenchmarkSB_Compile -benchtime=1s

func benchCompile(b *testing.B, numLeaves int) {
	ctx := context.Background()
	rt := newCompilerRuntime(ctx)
	defer rt.Close(ctx)
	bin := buildModule(numLeaves, kindIndirect)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cm, err := rt.CompileModule(ctx, bin)
		if err != nil {
			b.Fatalf("compile: %v", err)
		}
		_ = cm.Close(ctx)
	}
}

// 几档函数数(模拟一个 Program 内 1 / 16 / 64 / 256 个升层 Proto 的单 module)。
func BenchmarkSB_Compile1(b *testing.B)   { benchCompile(b, 1) }
func BenchmarkSB_Compile16(b *testing.B)  { benchCompile(b, 16) }
func BenchmarkSB_Compile64(b *testing.B)  { benchCompile(b, 64) }
func BenchmarkSB_Compile256(b *testing.B) { benchCompile(b, 256) }

// BenchmarkSB_Instantiate 实例化成本(重编后实例化为新实例的那一步)。
func BenchmarkSB_Instantiate(b *testing.B) {
	ctx := context.Background()
	rt := newCompilerRuntime(ctx)
	defer rt.Close(ctx)
	cm, err := rt.CompileModule(ctx, buildModule(16, kindIndirect))
	if err != nil {
		b.Fatalf("compile: %v", err)
	}
	defer cm.Close(ctx)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		inst, err := rt.InstantiateModule(ctx, cm, wazero.NewModuleConfig().WithName("x"))
		if err != nil {
			b.Fatalf("instantiate: %v", err)
		}
		_ = inst.Close(ctx)
	}
}
