package p3indirect

import (
	"context"
	"testing"

	"github.com/tetratelabs/wazero/api"
)

// S-A:dispatch 单次成本对比(call_indirect vs direct call vs imported host call)。
//
// driver 在紧循环里调 leaf callN 次,ns/op ÷ callN = 单次 dispatch 摊销成本
// (摊掉外层 fn.Call 的 ctx/stack 开销 ~36ns)。三形态 leaf 体相同、循环骨架
// 相同,差异全在调用机制 → 直接对比。
//
// 闸门:indirect 必须 ≪ host(~143ns,跨层税);目标 <30ns(近原生 indirect)。
//
// 跑法:GOFLAGS=-mod=mod go test -bench BenchmarkSA -benchtime=2s -count=3
const callN = 10000

func benchDispatch(b *testing.B, kind callKind) {
	ctx := context.Background()
	rt := newCompilerRuntime(ctx)
	defer rt.Close(ctx)
	if kind == kindHost {
		registerHost(ctx, b, rt)
	}
	mod, err := rt.Instantiate(ctx, buildModule(1, kind))
	if err != nil {
		b.Fatalf("instantiate: %v", err)
	}
	fn := mod.ExportedFunction("driver")

	stack := make([]uint64, 1)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		stack[0] = api.EncodeI32(callN)
		if err := fn.CallWithStack(ctx, stack); err != nil {
			b.Fatal(err)
		}
	}
	// ns/op 是「一次 driver(callN)」成本;÷callN 得单次 dispatch 摊销(手工换算)。
	b.ReportMetric(float64(callN), "calls/op")
}

// BenchmarkSA_Indirect — 单 module 内 call_indirect 调 leaf(本方案目标形态)。
func BenchmarkSA_Indirect(b *testing.B) { benchDispatch(b, kindIndirect) }

// BenchmarkSA_Direct — 单 module 内 call 直调 leaf(地板基线,无表查)。
func BenchmarkSA_Direct(b *testing.B) { benchDispatch(b, kindDirect) }

// BenchmarkSA_Host — call imported Go leaf(= PW0 S3N 跨层税基线 ~143ns)。
func BenchmarkSA_Host(b *testing.B) { benchDispatch(b, kindHost) }
