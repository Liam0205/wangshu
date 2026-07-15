package p3indirect

import (
	"context"
	"testing"

	"github.com/tetratelabs/wazero/api"
)

// S-A: per-call dispatch cost comparison (call_indirect vs direct call vs
// imported host call).
//
// The driver calls leaf callN times in a tight loop; ns/op ÷ callN = the
// amortized per-call dispatch cost (amortizing away the outer fn.Call ctx/stack
// overhead of ~36ns). All three forms have the same leaf body and the same loop
// skeleton; the only difference is the call mechanism → a direct comparison.
//
// Gate: indirect must be ≪ host (~143ns, cross-layer tax); target <30ns
// (near-native indirect).
//
// How to run: GOFLAGS=-mod=mod go test -bench BenchmarkSA -benchtime=2s -count=3
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
	// ns/op is the cost of "one driver(callN)"; ÷callN gives the amortized per-call dispatch (manual conversion).
	b.ReportMetric(float64(callN), "calls/op")
}

// BenchmarkSA_Indirect — call_indirect to leaf within a single module (this scheme's target form).
func BenchmarkSA_Indirect(b *testing.B) { benchDispatch(b, kindIndirect) }

// BenchmarkSA_Direct — direct call to leaf within a single module (floor baseline, no table lookup).
func BenchmarkSA_Direct(b *testing.B) { benchDispatch(b, kindDirect) }

// BenchmarkSA_Host — call imported Go leaf (= PW0 S3N cross-layer tax baseline ~143ns).
func BenchmarkSA_Host(b *testing.B) { benchDispatch(b, kindHost) }
