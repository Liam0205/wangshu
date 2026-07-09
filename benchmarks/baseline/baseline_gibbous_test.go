//go:build wangshu_p3 && wangshu_profile

// 凸月(gibbous)档:baseline 三档微基准经 force-all 升 wazero 执行,与新月
// (crescent,BenchmarkXxx_Wangshu)+ gopher 三方对比。
//
// **非空保证(承 PW9 空测教训)**:simple/arith/loop 原脚本主体在**顶层 chunk**,
// 而顶层是 vararg(F1 排除)永不升层 → 直接 force-all 跑顶层 = 测的还是新月(空测)。
// 故凸月档把脚本主体包进**非 vararg 内层函数** kernel() 反复调,使 kernel 升 gibbous
// (首调 crescent 预热,二调起 call_indirect 直调)——凸月路径真被走到。这与新月/
// gopher 档测的顶层形态略有差异(多一层函数调用),比值仅作「凸月 vs 新月同核」参考。
//
// 运行:go test -tags "wangshu_p3 wangshu_profile" -bench 'Gibbous' ./benchmarks/baseline/

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
	if _, err := prog.Run(st); err != nil { // 预热升层(force=true 时)
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

// 凸月档(force-all 升 gibbous)+ 同核新月档(force=false,同 wrapKernel 包装,
// 公平对比——避免拿凸月「包装核 ×50」对新月「裸顶层」的苹果对橘子)。
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
