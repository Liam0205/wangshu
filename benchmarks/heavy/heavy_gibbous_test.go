//go:build wangshu_p3 && wangshu_profile

// 凸月(gibbous, P3)档:heavy 三脚本经 force-all 升 wazero 执行,与新月
// (crescent,默认档 BenchmarkXxx_Wangshu)+ gopher 三方对比。
//
// 仅 wangshu_p3 && wangshu_profile build 编译:p3 提供真 gibbous Compiler;
// profile 启用 OnEnter/OnBackEdge 采样(force-all 经它触发升层)。
//
// **非空保证**:heavy 三脚本均含内层非 vararg kernel(probe 实测均升 1 个 Proto),
// force-all 下首调驱动升层,二调起经 call_indirect 直调 gibbous → 凸月路径真被走到。
//
// 运行:go test -tags "wangshu_p3 wangshu_profile" -bench 'Gibbous' ./benchmarks/heavy/
package heavy

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
	if _, err := prog.Run(st); err != nil { // warmup: drive promotion
		b.Fatalf("warmup: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := prog.Run(st); err != nil {
			b.Fatalf("run: %v", err)
		}
	}
}

func BenchmarkHeavyArith_Gibbous(b *testing.B)     { benchVMGibbous(b, "heavy_arith") }
func BenchmarkHeavyRecursion_Gibbous(b *testing.B) { benchVMGibbous(b, "heavy_recursion") }
func BenchmarkHeavyFloatloop_Gibbous(b *testing.B) { benchVMGibbous(b, "heavy_floatloop") }
