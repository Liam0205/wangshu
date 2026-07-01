//go:build wangshu_p4 && wangshu_profile

// 凸月-jit(gibbous-jit, P4)档:heavy 三脚本经 force-all 升原生 jit 执行。
//
// **覆盖率预期**(承 docs/design/p4-method-jit/implementation-progress.md
// PJ7):heavy 三脚本的内层 kernel 形态(FORLOOP body 含多算术 + 累加 / while
// 单条件 + 浮点 / 自递归 + 算术)均**不在**现 P4 PJ7 形态白名单内(单 BB 值产生
// + RETURN / FORLOOP 字节级 inline 空 body / 表 IC / CALL void),analyzeShape 返
// 不命中 → SupportsAllOpcodes 返 false → bridge TierStuck → 永久走 crescent。
//
// **预期数字 ≈ BenchmarkHeavyXxx_Wangshu**(走 P1 解释器,P4 拒升)。这是诚实
// 暴露 PJ7 形态子集对真实 Lua 热点的覆盖率,标 PJ7+ 扩 SAO 白名单的 followup,
// 不修脚本去迁就 P4 子集(见 09-acceptance-checklist.md V15 注)。
//
// 运行:go test -tags "wangshu_p4 wangshu_profile" -bench 'GibbousJIT' ./benchmarks/heavy/
package heavy

import (
	"testing"

	"github.com/Liam0205/wangshu"
)

func benchVMGibbousJIT(b *testing.B, name string) {
	src := loadScript(b, name)
	prog, err := wangshu.Compile(src, name)
	if err != nil {
		b.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(true)
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

func BenchmarkHeavyArith_GibbousJIT(b *testing.B)     { benchVMGibbousJIT(b, "heavy_arith") }
func BenchmarkHeavyRecursion_GibbousJIT(b *testing.B) { benchVMGibbousJIT(b, "heavy_recursion") }
func BenchmarkHeavyFloatloop_GibbousJIT(b *testing.B) { benchVMGibbousJIT(b, "heavy_floatloop") }
