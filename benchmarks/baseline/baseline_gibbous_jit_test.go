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

// kernel body —— P4 PJ7 真接入子集(单 BB「值产生 + RETURN」+ 二段算术
// 链式 + 比较折叠等)。

// 常量 number 返回(LOADK + RETURN)
const constBody = `return 42`

// 常量 nil 返回(空 RETURN 长度 1 形态——`function() end` 等价)
const nilBody = `return nil`

// 常量 bool 返回(LOADBOOL + RETURN)
const boolBody = `return true`

// 算术族(单 op):MUL + RETURN
const mulBody = `local x=7; return x*2`

// 二段算术链式:MUL + ADD + RETURN(`x*2+1` 形态)
const chainBody = `local x=7; return x*2+1`

// 比较折叠:EQ + JMP + LOADBOOL×2 + RETURN
const cmpBody = `local x=7; return x == 7`

// PJ2 投机 ADD 形态:R(B) + R(C) 双寄存器(B/C 都 ≤ 254)→ 命中 spec 模板
// (mmap 段直发 IsNumber guard×2 + movsd+addsd+movsd+ret 字节级)。
const specAddBody = `local x=7; local y=11; return x+y`

// PJ2 投机 SUB/MUL/DIV 形态:reg+reg 双 number 投机模板,字节级 SSE op =
// F2 0F 5C/59/5E C1(分别 SUBSD/MULSD/DIVSD),与 ADD 同 92 字节模板布局。
const specSubBody = `local x=11; local y=7; return x-y`
const specMulBody = `local x=6; local y=7; return x*y`
const specDivBody = `local x=42; local y=6; return x/y`

// PJ2 投机 reg-K 形态:`R(B) op K` 中 K 编译期烧 imm64 直发段,只 guard
// reg 端(73 字节模板,比 reg-reg 少 19 字节)。常见 hot path 常量化形态,
// luac 编 `x+5` 等为 ADD A B(reg) C(>=256 = K idx)。
const specRegKAddBody = `local x=7; return x+5`
const specRegKSubBody = `local x=10; return x-3`
const specRegKMulBody = `local x=7; return x*2`
const specRegKDivBody = `local x=42; return x/6`

// PJ2 投机 chain-KK 二段链式形态:`R(B) op1 K1 op2 K2`,luac 编 `x*2+1`
// 为 MUL+ADD 链式(op1.C=K1 / op2.C=K2 / op2.B=retA 中间值衔接)。chain
// 模板复用 xmm0 跨两段 SSE binop,一次 mmap 段调用完成两次算术,省一次
// boundary 跨界 + reg-stack 中转。
const specChainMulAddBody = `local x=7; return x*2+1`
const specChainAddMulBody = `local x=7; return (x+1)*2`

// PJ3 FORLOOP 字节级 inline:`function() for i=1,K do end end` 形态。
// P4 首次在 mmap 段内字节级跑控制流(循环),不经任何 host helper round-trip。
// 模板 69 字节(浮点 idx+step / ucomisd limit / backward jmp)。
//
// **注**:wrapKernelJIT 把 body 包进 kernel,然后 `t = kernel()` 调 50 次。
// 要匹配 analyzeForLoopForm 的「三 LOADK + FORPREP + FORLOOP + RETURN(空)」
// 6-7 op 闭门形态,kernel body 必须是「for i=1,K do end」无任何返回值/
// 其它语句——`for ... end`(空 return implied)而非 `for ... end; return 0`。
const pj3ForLoop100Body = `for i = 1, 100 do end`
const pj3ForLoop1000Body = `for i = 1, 1000 do end`
const pj3ForLoop10000Body = `for i = 1, 10000 do end`

func BenchmarkGibbousJIT_Const(b *testing.B)      { benchGibbousJIT(b, constBody, true) }
func BenchmarkGibbousJIT_ConstCresc(b *testing.B) { benchGibbousJIT(b, constBody, false) }
func BenchmarkGibbousJIT_Nil(b *testing.B)        { benchGibbousJIT(b, nilBody, true) }
func BenchmarkGibbousJIT_NilCresc(b *testing.B)   { benchGibbousJIT(b, nilBody, false) }
func BenchmarkGibbousJIT_Bool(b *testing.B)       { benchGibbousJIT(b, boolBody, true) }
func BenchmarkGibbousJIT_BoolCresc(b *testing.B)  { benchGibbousJIT(b, boolBody, false) }
func BenchmarkGibbousJIT_Mul(b *testing.B)        { benchGibbousJIT(b, mulBody, true) }
func BenchmarkGibbousJIT_MulCresc(b *testing.B)   { benchGibbousJIT(b, mulBody, false) }
func BenchmarkGibbousJIT_Chain(b *testing.B)      { benchGibbousJIT(b, chainBody, true) }
func BenchmarkGibbousJIT_ChainCresc(b *testing.B) { benchGibbousJIT(b, chainBody, false) }
func BenchmarkGibbousJIT_Cmp(b *testing.B)        { benchGibbousJIT(b, cmpBody, true) }
func BenchmarkGibbousJIT_CmpCresc(b *testing.B)   { benchGibbousJIT(b, cmpBody, false) }

// PJ2 投机 ADD reg+reg 形态:命中 spec 模板的真 luajc 档相关数据。
func BenchmarkGibbousJIT_SpecAdd(b *testing.B)      { benchGibbousJIT(b, specAddBody, true) }
func BenchmarkGibbousJIT_SpecAddCresc(b *testing.B) { benchGibbousJIT(b, specAddBody, false) }

// PJ2 投机 SUB/MUL/DIV 同款 P4 vs crescent 对比(命中 92 字节 spec 模板)。
func BenchmarkGibbousJIT_SpecSub(b *testing.B)      { benchGibbousJIT(b, specSubBody, true) }
func BenchmarkGibbousJIT_SpecSubCresc(b *testing.B) { benchGibbousJIT(b, specSubBody, false) }
func BenchmarkGibbousJIT_SpecMul(b *testing.B)      { benchGibbousJIT(b, specMulBody, true) }
func BenchmarkGibbousJIT_SpecMulCresc(b *testing.B) { benchGibbousJIT(b, specMulBody, false) }
func BenchmarkGibbousJIT_SpecDiv(b *testing.B)      { benchGibbousJIT(b, specDivBody, true) }
func BenchmarkGibbousJIT_SpecDivCresc(b *testing.B) { benchGibbousJIT(b, specDivBody, false) }

// PJ2 投机 reg-K 四档(73 字节模板,单 guard,K 烧 imm64)P4 vs crescent。
func BenchmarkGibbousJIT_SpecRegKAdd(b *testing.B)      { benchGibbousJIT(b, specRegKAddBody, true) }
func BenchmarkGibbousJIT_SpecRegKAddCresc(b *testing.B) { benchGibbousJIT(b, specRegKAddBody, false) }
func BenchmarkGibbousJIT_SpecRegKSub(b *testing.B)      { benchGibbousJIT(b, specRegKSubBody, true) }
func BenchmarkGibbousJIT_SpecRegKSubCresc(b *testing.B) { benchGibbousJIT(b, specRegKSubBody, false) }
func BenchmarkGibbousJIT_SpecRegKMul(b *testing.B)      { benchGibbousJIT(b, specRegKMulBody, true) }
func BenchmarkGibbousJIT_SpecRegKMulCresc(b *testing.B) { benchGibbousJIT(b, specRegKMulBody, false) }
func BenchmarkGibbousJIT_SpecRegKDiv(b *testing.B)      { benchGibbousJIT(b, specRegKDivBody, true) }
func BenchmarkGibbousJIT_SpecRegKDivCresc(b *testing.B) { benchGibbousJIT(b, specRegKDivBody, false) }

// PJ2 投机 chain-KK 二段链式(92 字节模板,单 guard,双 K imm64,一段段
// 跑省一次 boundary)P4 vs crescent。
func BenchmarkGibbousJIT_SpecChainMulAdd(b *testing.B) {
	benchGibbousJIT(b, specChainMulAddBody, true)
}
func BenchmarkGibbousJIT_SpecChainMulAddCresc(b *testing.B) {
	benchGibbousJIT(b, specChainMulAddBody, false)
}
func BenchmarkGibbousJIT_SpecChainAddMul(b *testing.B) {
	benchGibbousJIT(b, specChainAddMulBody, true)
}
func BenchmarkGibbousJIT_SpecChainAddMulCresc(b *testing.B) {
	benchGibbousJIT(b, specChainAddMulBody, false)
}

// PJ3 FORLOOP 字节级 inline(69 字节模板,mmap 段内自循环)P4 vs crescent。
// 这是 P4 首次「字节级跑控制流不经 host helper」的真大幅加速验证档。
// 不同 K(100/1000/10000)验证 backward jmp 摊薄 boundary 的真实收益。
func BenchmarkGibbousJIT_PJ3For100(b *testing.B) {
	benchGibbousJIT(b, pj3ForLoop100Body, true)
}
func BenchmarkGibbousJIT_PJ3For100Cresc(b *testing.B) {
	benchGibbousJIT(b, pj3ForLoop100Body, false)
}
func BenchmarkGibbousJIT_PJ3For1000(b *testing.B) {
	benchGibbousJIT(b, pj3ForLoop1000Body, true)
}
func BenchmarkGibbousJIT_PJ3For1000Cresc(b *testing.B) {
	benchGibbousJIT(b, pj3ForLoop1000Body, false)
}
func BenchmarkGibbousJIT_PJ3For10000(b *testing.B) {
	benchGibbousJIT(b, pj3ForLoop10000Body, true)
}
func BenchmarkGibbousJIT_PJ3For10000Cresc(b *testing.B) {
	benchGibbousJIT(b, pj3ForLoop10000Body, false)
}
