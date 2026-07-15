//go:build wangshu_p3 && wangshu_profile

// Gibbous tier: the three baseline micro-benchmarks are promoted to wazero
// execution via force-all, compared three ways against the crescent tier
// (BenchmarkXxx_Wangshu) plus gopher.
//
// **Non-empty guarantee (lesson from PW9's empty benchmarks)**: the
// simple/arith/loop script bodies originally live in the **top-level chunk**,
// but the top level is vararg (excluded by F1) and never promotes → running
// force-all on the top level directly = still measuring crescent (an empty
// benchmark). So the gibbous tier wraps the script body in a **non-vararg
// inner function** kernel() and calls it repeatedly, so kernel promotes to
// gibbous (first call warms up crescent, from the second call on it is a
// call_indirect direct dispatch) -- the gibbous path is actually exercised.
// This differs slightly from the top-level form measured by the crescent/
// gopher tiers (one extra function call), so the ratio only serves as a
// "gibbous vs crescent, same kernel" reference.
//
// Run: go test -tags "wangshu_p3 wangshu_profile" -bench 'Gibbous' ./benchmarks/baseline/

package baseline

import (
	"testing"

	"github.com/Liam0205/wangshu"
)

// wrapKernel + the kernel body constants live in baseline_kernel_test.go
// (tag-neutral, shared with the P1 build's _GopherKernel same-shape
// counterpart benches, issue #93).

func benchGibbous(b *testing.B, body string, force bool) {
	prog, err := wangshu.Compile([]byte(wrapKernel(body)), "bench-gib")
	if err != nil {
		b.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(force)
	if _, err := prog.Run(st); err != nil { // warm-up promotion (when force=true)
		b.Fatalf("warmup: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := prog.Run(st); err != nil {
			b.Fatalf("run: %v", err)
		}
	}
}

// Minimal-body kernels (1-instruction body): isolate the boundary
// (call_indirect + return) cost. Same body strings as P4 side in
// baseline_gibbous_jit_test.go - duplicated here because that file is
// gated on wangshu_p4 and not visible to this build tag. Used by V16
// boundary roundtrip acceptance.
const constBodyP3 = `return 42`
const nilBodyP3 = `return nil`
const boolBodyP3 = `return true`

// Gibbous tier (force-all promotes to gibbous) plus same-kernel crescent tier
// (force=false, same wrapKernel wrapping, for a fair comparison -- avoids the
// apples-to-oranges of gibbous "wrapped kernel x50" vs crescent "bare top level").
func BenchmarkSimple_Gibbous(b *testing.B)       { benchGibbous(b, simpleBody, true) }
func BenchmarkSimple_WangshuKernel(b *testing.B) { benchGibbous(b, simpleBody, false) }
func BenchmarkArith_Gibbous(b *testing.B)        { benchGibbous(b, arithBody, true) }
func BenchmarkArith_WangshuKernel(b *testing.B)  { benchGibbous(b, arithBody, false) }
func BenchmarkLoop_Gibbous(b *testing.B)         { benchGibbous(b, loopBody, true) }
func BenchmarkLoop_WangshuKernel(b *testing.B)   { benchGibbous(b, loopBody, false) }

// V16 boundary roundtrip: minimal-body kernels measure the trampoline
// + entry/exit cost on the P3 side, parallel to BenchmarkGibbousJIT_Const/
// Nil/Bool on the P4 side. Comparing the two pairs (P3 vs P4) gives the
// V16 acceptance number: P4 boundary should be no more than 5% slower
// than P3 boundary on the same body.
func BenchmarkConst_Gibbous(b *testing.B)       { benchGibbous(b, constBodyP3, true) }
func BenchmarkConst_WangshuKernel(b *testing.B) { benchGibbous(b, constBodyP3, false) }
func BenchmarkNil_Gibbous(b *testing.B)         { benchGibbous(b, nilBodyP3, true) }
func BenchmarkNil_WangshuKernel(b *testing.B)   { benchGibbous(b, nilBodyP3, false) }
func BenchmarkBool_Gibbous(b *testing.B)        { benchGibbous(b, boolBodyP3, true) }
func BenchmarkBool_WangshuKernel(b *testing.B)  { benchGibbous(b, boolBodyP3, false) }
