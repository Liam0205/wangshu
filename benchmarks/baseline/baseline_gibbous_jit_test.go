//go:build wangshu_p4 && wangshu_profile

// 凸月-jit(gibbous-jit, P4)档:baseline 微基准经 force-all 升原生 jit 执行,
// 与新月(crescent)+ P3(gibbous-wasm)三方对比。
//
// **启用 build tag**:wangshu_p4 + wangshu_profile(profile 钩点必须激活,
// 否则 considerPromotion 不触发,SetForceAllPromote 也无效)。
//
// **PJ7 简化形态**(承 docs/design/p4-method-jit/implementation-progress.md
// §1 PJ7 行):P4 PJ7 真接入子集 = 单 BB「LOADK/LOADBOOL/LOADNIL + RETURN A 1」
// 形态。本 baseline 只能测这类最简函数——更复杂的 loop/arith 需要 PJ8+
// 完整 opcode 族扩(MOVE/ADD/FORLOOP 等,留下一阶段)。
//
// 运行:go test -tags "wangshu_p4 wangshu_profile" -bench 'Gibbous_JIT' ./benchmarks/baseline/

package baseline

import (
	"testing"

	"github.com/Liam0205/wangshu"
)

// 把单值 return 包进非 vararg 内层 kernel 反复调(避开 vararg 顶层不升层)。
//
// kernel() 形态是 P4 PJ7 单 BB 子集(LOADK + RETURN A 1)——这是当前真接入
// 唯一支持的形态。kernel 经热度阈值或 force-all 升 P4 后,反复调 50 次走
// jit 路径,可与 crescent baseline 比 ns/op 实证 P4 物理收益。
func wrapKernelJIT(body string) string {
	return "local function kernel()\n" + body + "\nend\nlocal t = 0\nfor _ = 1, 50 do t = kernel() end\nreturn t"
}

func benchGibbousJIT(b *testing.B, body string, force bool) {
	prog, err := wangshu.Compile([]byte(wrapKernelJIT(body)), "bench-jit")
	if err != nil {
		b.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(force)
	if _, err := prog.Run(st); err != nil { // 预热升层
		b.Fatalf("warmup: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := prog.Run(st); err != nil {
			b.Fatalf("run: %v", err)
		}
	}
}

// kernel body —— P4 PJ7 真接入子集(单 BB「值产生 + RETURN」)。

// 常量 number 返回(LOADK + RETURN)
const constBody = `return 42`

// 常量 nil 返回(空 RETURN 长度 1 形态——`function() end` 等价)
const nilBody = `return nil`

// 常量 bool 返回(LOADBOOL + RETURN)
const boolBody = `return true`

func BenchmarkGibbousJIT_Const(b *testing.B)      { benchGibbousJIT(b, constBody, true) }
func BenchmarkGibbousJIT_ConstCresc(b *testing.B) { benchGibbousJIT(b, constBody, false) }
func BenchmarkGibbousJIT_Nil(b *testing.B)        { benchGibbousJIT(b, nilBody, true) }
func BenchmarkGibbousJIT_NilCresc(b *testing.B)   { benchGibbousJIT(b, nilBody, false) }
func BenchmarkGibbousJIT_Bool(b *testing.B)       { benchGibbousJIT(b, boolBody, true) }
func BenchmarkGibbousJIT_BoolCresc(b *testing.B)  { benchGibbousJIT(b, boolBody, false) }
