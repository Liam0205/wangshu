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
	"fmt"
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

// wrapKernelJITWithArg 包带参 kernel(用于 reg-limit 形态:`for i=1,n do
// end`,n 是参数 reg R(0)= luac 编 MOVE R(A+1) R(0))。kernel 接收一个
// 参数 N 并以 N 跑循环;外层 wrap × 50 调 kernel(N) 摊薄 boundary。
func wrapKernelJITWithArg(body string, argName string, argValue int) string {
	return "local function kernel(" + argName + ")\n" + body + "\nend\nlocal t = 0\nfor _ = 1, 50 do t = kernel(" +
		fmt.Sprint(argValue) + ") end\nreturn t"
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

// benchGibbousJITWithArg 同 benchGibbousJIT 但 wrap 带参 kernel。
func benchGibbousJITWithArg(b *testing.B, body, argName string, argValue int, force bool) {
	src := wrapKernelJITWithArg(body, argName, argValue)
	prog, err := wangshu.Compile([]byte(src), "bench-jit-arg")
	if err != nil {
		b.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(force)
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

// PJ3 reg-limit hot path benchmark:`function(n) for i=1,n do end end`
// (luac 编 [1] MOVE A_init+1 0,即 limit = R(0) = 参数 n)。117 字节
// 模板含 IsNumber guard + 浮点 loop + safepoint + deopt block,
// fast path 与全常量空 body 同款字节级 inline,差别仅多一次 guard。
func BenchmarkGibbousJIT_PJ3RegLimit1000(b *testing.B) {
	benchGibbousJITWithArg(b, "for i = 1, n do end", "n", 1000, true)
}
func BenchmarkGibbousJIT_PJ3RegLimit1000Cresc(b *testing.B) {
	benchGibbousJITWithArg(b, "for i = 1, n do end", "n", 1000, false)
}
func BenchmarkGibbousJIT_PJ3RegLimit10000(b *testing.B) {
	benchGibbousJITWithArg(b, "for i = 1, n do end", "n", 10000, true)
}
func BenchmarkGibbousJIT_PJ3RegLimit10000Cresc(b *testing.B) {
	benchGibbousJITWithArg(b, "for i = 1, n do end", "n", 10000, false)
}

// PJ3 body inline benchmark:`local s=0; for i=1,K do s=s+1 end; return s`
// 形态(135 字节模板含 safepoint)— 真实生产 hot path,带状态累加循环。
const pj3BodyAdd1000Body = `local s=0; for i=1,1000 do s=s+1 end; return s`
const pj3BodyAdd10000Body = `local s=0; for i=1,10000 do s=s+1 end; return s`

func BenchmarkGibbousJIT_PJ3BodyAdd1000(b *testing.B) {
	benchGibbousJIT(b, pj3BodyAdd1000Body, true)
}
func BenchmarkGibbousJIT_PJ3BodyAdd1000Cresc(b *testing.B) {
	benchGibbousJIT(b, pj3BodyAdd1000Body, false)
}
func BenchmarkGibbousJIT_PJ3BodyAdd10000(b *testing.B) {
	benchGibbousJIT(b, pj3BodyAdd10000Body, true)
}
func BenchmarkGibbousJIT_PJ3BodyAdd10000Cresc(b *testing.B) {
	benchGibbousJIT(b, pj3BodyAdd10000Body, false)
}

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

// PJ4 IC ArrayHit benchmark —— `function(t) return t[K] end` 形态字节级 inline。
//
// **wrap 形态选择**:PJ4 IC inline 触发需要 P1 解释器先填 IC[0]=ArrayHit,
// 然后升层时 analyzeGetTableArrayHit 命中。本 wrap 用「内层 inner kernel
// 函数 + outer 多次调」形态:
//
//   - inner kernel 是 PJ4 形态(2 op:GETTABLE + RETURN);
//   - outer 先经 P1 解释器跑 50 次 warmup 让 IC[0] 填上(force=true 时
//     warmup 在 benchmark 入口前的两轮 prog.Run 里跑;第一轮 P1 填 IC,
//     第二轮 force-all 升 inner 让 analyzeGetTableArrayHit 命中);
//   - benchmark loop 里 inner 已是字节级 inline 模板。
//
// **诚实数据**(Xeon 6982P,200 iter):
//
//	PJ4IcArrayHit1     4654 ns/op   PJ4IcArrayHit1Cresc  4214 ns/op (slower 10%)
//	PJ4IcArrayHit2     4906 ns/op   PJ4IcArrayHit2Cresc  4248 ns/op (slower 15%)
//
// **加速为负是预期**:P1 解释器 icGetTable 已是「IC 命中即 array 段直达」
// 的几条 Go 指令快路径(不走完整哈希),与 P4 字节级 IC inline 模板做的事
// 完全等价。差异点在「P4 多付 callJITSpec trampoline 入出 + 寄存器保存恢复」
// (~50ns 开销),P1 反而没这开销。
//
// **真加速场景留 PJ5 CALL inline**:把 outer 也升 P4 后,outer 内多次
// GETTABLE 不付 doCall boundary,IC inline 在「无 doCall 跨界 + 字节级
// 直达 array 段」组合下才显出加速。本档保留作 SpecTableHits prove-the-path
// 命中证据(SpecTableHits 经 PJ4 e2e test 已断言 > 0)+ 同形态 P1 baseline
// 对照。
//
// 对位 gopher-lua:本档不加 gopher 对位(单 inner kernel 形态对位口径见
// baseline_test.go 同款 wrap 路径)。

// wrapKernelJITForPJ4IcArray:outer 持表 + 跑 50 次 inner kernel(tbl) 调用。
// kernel 是 PJ4 IC ArrayHit 升层目标(`function(tb) return tb[idx] end`)。
// 第一次 P1 跑 outer 时 kernel 内 GETTABLE → icGetTable 填 IC[0]=ArrayHit;
// 第二次 force-all 升 kernel 时 analyzeGetTableArrayHit 命中 IC slot →
// 字节级 inline 编译;后续 prog.Run 每次 outer × 50 调直发字节级。
func wrapKernelJITForPJ4IcArray(idx int) string {
	return "local t = {42, 43, 44}\n" +
		"local function kernel(tb) return tb[" + fmt.Sprint(idx) + "] end\n" +
		"local r = 0\nfor _ = 1, 50 do r = kernel(t) end\nreturn r"
}

// benchGibbousJITPJ4Table 跑 PJ4 IC ArrayHit benchmark 形态。
//
// **关键 phase 顺序**:先关 force-all 跑一次让 P1 解释器填 IC[0],再开
// force-all + 跑一次升 inner kernel(IC[0] 已填 → analyzeGetTableArrayHit
// 命中 → IC inline 编译)。第三次起 b.N 循环直接命中已升层的字节级 inline 模板。
//
// force=false 时,两次 warmup 都不开 force-all,inner kernel 永远不升层
// (长度 2 < MinPromotableCodeLen=10,short-proto 守卫拒);b.N 路径仍走
// P1 解释器 icGetTable host helper,作 crescent 档对照基线。
func benchGibbousJITPJ4Table(b *testing.B, idx int, force bool) {
	src := wrapKernelJITForPJ4IcArray(idx)
	prog, err := wangshu.Compile([]byte(src), "bench-jit-pj4-table")
	if err != nil {
		b.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	if _, err := prog.Run(st); err != nil { // P1 预热填 IC slot
		b.Fatalf("warmup-phase1: %v", err)
	}
	st.SetForceAllPromote(force)
	if _, err := prog.Run(st); err != nil { // force-all 升 inner kernel
		b.Fatalf("warmup-phase2: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := prog.Run(st); err != nil {
			b.Fatalf("run: %v", err)
		}
	}
}

// PJ4 IC ArrayHit benchmark P4 vs crescent(force=true: P4 字节级 inline;
// force=false: 同 wrapper 形态下 P1 解释器 icGetTable host helper 路径)。
//
// idx=1(t[1])是 hot path 最常见形态;idx=2 验数字键 stableIndex 差异。
func BenchmarkGibbousJIT_PJ4IcArrayHit1(b *testing.B) {
	benchGibbousJITPJ4Table(b, 1, true)
}
func BenchmarkGibbousJIT_PJ4IcArrayHit1Cresc(b *testing.B) {
	benchGibbousJITPJ4Table(b, 1, false)
}
func BenchmarkGibbousJIT_PJ4IcArrayHit2(b *testing.B) {
	benchGibbousJITPJ4Table(b, 2, true)
}
func BenchmarkGibbousJIT_PJ4IcArrayHit2Cresc(b *testing.B) {
	benchGibbousJITPJ4Table(b, 2, false)
}

// PJ5 SELF method call inline benchmark P4 vs crescent。
// force=true: PJ5 SELF inline 主路径(host.Self + host.CallBaseline byte-equal P1);
// force=false: crescent 解释器 SELF + CALL bytecode 路径。
//
// **当前性能预期**(承 §9.17):PJ5 SELF inline 路径走 host.Self / host.CallBaseline
// 完整 P1 byte-equal — 与 crescent 解释器同款 helper 链,**性能差异主要来自 P4
// 升层 + DoReturn 弹帧 vs 解释器主循环 + setReg/reg 同款操作**,无显著加速
// (PJ5 SELF inline 当前是「正确性接入」而非「性能加速」)。段内 EmitSelfCallInline
// 模板真接入后(留多会话),通过 IC NodeHit / ArrayHit guard + 跳过 host
// round-trip 可获 ≥2x 加速。
//
// 本 benchmark 用于 baseline 数据采集 — 后续 spec template 真接入时作对比基线。
func benchGibbousJITSelfCall(b *testing.B, force bool) {
	src := `
local sum = 0
local o = { m = function(self, x) sum = sum + x end }
local function caller(t, v) t:m(v) end
local function kernel()
  caller(o, 42)
end
local t = 0
for _ = 1, 50 do kernel() end
return t`
	prog, err := wangshu.Compile([]byte(src), "bench-jit-self")
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

func BenchmarkGibbousJIT_PJ5SelfCall(b *testing.B)      { benchGibbousJITSelfCall(b, true) }
func BenchmarkGibbousJIT_PJ5SelfCallCresc(b *testing.B) { benchGibbousJITSelfCall(b, false) }
