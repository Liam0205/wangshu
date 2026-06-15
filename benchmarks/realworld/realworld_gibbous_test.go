//go:build wangshu_p3 && wangshu_profile

// 凸月(gibbous)档:realworld 五脚本经 force-all 升 wazero 执行,与新月
// (crescent,默认档 BenchmarkXxx_Wangshu)+ gopher 三方对比(PW10 R5 口径)。
//
// 仅 wangshu_p3 && wangshu_profile build 编译:p3 提供真 gibbous Compiler +
// 收养 wazero memory;profile 启用 OnEnter/OnBackEdge 采样(force-all 经它触发
// 升层)。默认 tag 下本文件不编译,不污染 gopher/新月两路 bench 列。
//
// **非空保证(承 PW9 空测教训)**:五脚本均含热内层函数(fib 递归 / spectralnorm
// 的 A/Av/Atv / nbody 的 advance 等),force-all 下这些 Proto 升 gibbous;预热一次
// 驱动升层(首调跑 crescent,二调起经 call_indirect 直调 gibbous)。顶层 chunk 是
// vararg(F1 排除)不升层,但脚本主体工作量在被反复调的内层函数 → 凸月路径真被走到。
//
// 运行:go test -tags "wangshu_p3 wangshu_profile" -bench 'Gibbous' ./benchmarks/realworld/

package realworld

import (
	"testing"

	"github.com/Liam0205/wangshu"
)

func benchVMGibbous(b *testing.B, name string) {
	src := loadScript(b, name)
	prog, err := wangshu.Compile(src, name)
	if err != nil {
		b.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(true)
	// 预热:首次 Run 驱动内层热函数升 gibbous(首调 crescent,升层发生在帧入口)。
	if _, err := prog.Run(st); err != nil {
		b.Fatalf("warmup: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := prog.Run(st); err != nil {
			b.Fatalf("run: %v", err)
		}
	}
}

func BenchmarkFib_Gibbous(b *testing.B)          { benchVMGibbous(b, "fib") }
func BenchmarkBinaryTrees_Gibbous(b *testing.B)  { benchVMGibbous(b, "binarytrees") }
func BenchmarkSpectralNorm_Gibbous(b *testing.B) { benchVMGibbous(b, "spectralnorm") }
func BenchmarkFannkuch_Gibbous(b *testing.B)     { benchVMGibbous(b, "fannkuch") }
func BenchmarkNBody_Gibbous(b *testing.B)        { benchVMGibbous(b, "nbody") }
