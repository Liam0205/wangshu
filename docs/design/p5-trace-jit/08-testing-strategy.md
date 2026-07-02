# P5 §8:测试策略——三层 byte-equal 差分 + deopt 注入 + pass 开关差分矩阵 + 常驻 fuzz

> 状态:**未立项图纸(启动判定见 [01](./01-launch-judgment.md))**。本文是 P5「测试策略」的单一
> 事实源:三层同 Proto byte-equal 差分、每 guard 强制失败的 deopt 注入、per-pass 开关的差分矩阵、
> FuzzP5ForceTrace 常驻 fuzz、单测 / IR golden / regalloc verifier / snapshot roundtrip、CI build tag
> 矩阵。**性能验收在 [09](./09-acceptance-checklist.md);本文只讲差分方法学 + 测试架构**。
>
> **本文定位一句话**:**P5 是望舒投机最重的层——录制假设 + 每条 pass 语义保持 + snapshot 状态重建都是
> 静默错误结果的候选,组合空间远超 P4。差分主防线在 P5 是最重要的一层(依据 09-acceptance-checklist §1)**。测试策略的一切设计都从
> 「差分测试给出无歧义的对错判定」推出:三层 byte-equal 强制检查(§2)、per-guard 强制失败注入(§3)、per-pass
> 开关差分矩阵(§4)、常驻 fuzz(§5)。四项同时到位才算 P5 测试合格。
>
> 上游依据:
> [./09-acceptance-checklist.md](./09-acceptance-checklist.md)(§1 验收与差分四项)+ [./06-snapshot-deopt.md](./06-snapshot-deopt.md)(§4「正确性收敛不可计划,由 fuzz
> 时长决定」);
> [../../../llmdoc/must/design-premises.md](../../../llmdoc/must/design-premises.md)(原则 2 「投机错误结果
> 是 JIT 最危险 bug 类,层间差分是主防线」);
> [../p1-interpreter/12-testing-difftest.md](../p1-interpreter/12-testing-difftest.md)(P1 差分测试矩阵
> 单一事实源;§3.8 Runner 抽象 `WangshuFullmoon` slot 已注释预留 + §7 P5 行预留);
> [../p4-method-jit/08-testing-strategy.md](../p4-method-jit/08-testing-strategy.md)(**P4 测试策略,本文
> 直接对位继承**——V-numbered checklist 风格 + §5 deopt 注入设计 + `FuzzP4ForceAllPromote` 模式 + `-race`
> 与 mmap 不兼容的教训);
> [../../../llmdoc/guides/prove-the-path-under-test.md](../../../llmdoc/guides/prove-the-path-under-test.md)
> (测试通过 ≠ 在测的路径被走到——P5 是该 guide 的最大新用户,超越 P4)。
>
> 下游对齐:
> [07-system-integration.md](./07-system-integration.md)(测试架构基于 §5 host 接口扩展);
> [09-acceptance-checklist.md](./09-acceptance-checklist.md)(**验收 T-item 编号在此章;本文只声明 T1..Tn
> 的类目与差分性质,具体验收数字与 PT 映射由 09 拥有**);
> [04-optimization-passes.md](./04-optimization-passes.md)(pass 开关支持,依据 §4 差分矩阵);
> [05-register-allocation.md](./05-register-allocation.md)(regalloc verifier 单测,依据 §6);
> [06-snapshot-deopt.md](./06-snapshot-deopt.md)(deopt 注入 hook 是 PT5 必交付,依据 §3.5)。

---

## 0. 定位:P5 是差分主防线最重要的一层

### 0.1 一句话:投机最重 ⇒ 差分最关键

依据 [09-acceptance-checklist §1](./09-acceptance-checklist.md) 第一段字面:

> P5 的每一项机制——录制的类型假设、每个优化 pass 的语义保持、snapshot 的状态重建——都是「静默错误结果」
> 候选,且组合空间远超 P4。

四层错误结果的来源:

| 错误结果的类型 | P5 特有形式 | 抓的手段 |
|---|---|---|
| 录制期 IC 假设错 | 「这条循环里 R(x) 恒 number」被假设,罕见输入打破假设 | 三层 byte-equal(§2)+ deopt 注入(§3)|
| 优化 pass 语义偏移 | FOLD/CSE/LICM 某规则漏考虑元方法副作用 / NaN / GC 移动语义 | pass 开关差分矩阵(§4)|
| snapshot 物化错 | 某槽映射错、某 sink 配方 unsink 时缺字段 | deopt 注入把每条 exit 变确定性触发(§3)|
| regalloc 与 snapshot 耦合错 | exit 时某 IR 值预期在寄存器实际已被覆写 | regalloc verifier(§6)+ deopt 注入联合验证 |

### 0.2 与 P4 08 的关系

[P4 08](../p4-method-jit/08-testing-strategy.md) 是 P5 08 的直接前身:

| 维度 | P4 08(已交付轨道) | 本文(P5 增项 + 强化) |
|---|---|---|
| 验收编号 | V1-V22(V1-V18 P3 接续 + V19-V22 P4 增) | **T1..Tn**(依据 §1.5;09 拥有具体编号)|
| 主防线 | crescent vs gibbous-jit byte-equal | **crescent vs gibbous vs fullmoon 三方 byte-equal**(§2)|
| force-all 模式 | `SetForceAllPromoteJIT` | **`SetForceAllTraceRecord`**(§2.2)+ 保留 P4 force-all 一起跑 |
| deopt 注入 | P4 spec 过但**从未实现**(依据 P4 08 §5.2 2026-07-02 addendum) | **PT5 必交付**(§3.5);P5 侧没有替代路径 |
| pass 开关差分 | 无(P4 模板编译,无 pass) | **§4 全新增**——与 [04](./04-optimization-passes.md) §8 的开关约定配套 |
| fuzz 常驻 | nightly 2h | **常驻 fuzz 集群**(09-acceptance-checklist §1「升级为专用集群」)|
| `-race` 兼容 | P4 fuzz `-race` skip(依据 P4 08 §3.4)| **一样 skip**——P5 mmap 段一样与 Go race detector 不兼容(§8.2)|

### 0.3 章节路标

| § | 主题 | 关键产物 |
|---|---|---|
| §1 | 测试哲学 + T-item 类目 | 差分四项手段 + 三层同 Proto oracle |
| §2 | 三层 byte-equal 差分(主防线)| Runner 扩展 + force-all-trace + IC 预热 + CI 强制检查 |
| §3 | Deopt 注入(P5 主动触发每条 exit)| PT5 必交付 hook + 每 guard 强制失败 + 联合验证 |
| §4 | Pass 开关差分矩阵 | 与 04 §8 约定 + 首个 divergent config 定位 pass |
| §5 | FuzzP5ForceTrace(常驻 fuzz)| P4 fuzz 模式扩展 + 语料 + 崩溃回收进语料库的纪律 |
| §6 | 单元级测试 | IR printer golden + FOLD 规则表测试 + regalloc verifier + snapshot roundtrip |
| §7 | 性能验证(引用 09)| 无回归 gate + 常驻 workload 加速 gate |
| §8 | CI wiring | build tag + PR job / nightly job + `-race` 兼容子集 |
| §9 | 开放问题 | 差分覆盖度量 + 崩溃 corpus 归属 |

---

## 1. 测试哲学与 T-item 类目

### 1.1 差分测试给出无歧义的对错判定

依据 [architecture §4 不变式 2](../architecture.md)「层间逐字节差分是 CI 必过检查」+ [design-premises 原则
2](../../../llmdoc/must/design-premises.md)「JIT 最危险 bug 类 = 投机错误结果 → 层间差分是主防线」。

P5 的差分不是「多做一层」,而是**四项手段同时到位**:

| 差分维度 | 抓什么 | 强度 |
|---|---|---|
| **三方**(crescent vs gibbous vs fullmoon) | 同 Proto 三层执行 | ★★★★★(无实现差异噪声,任一分叉必是 P5 bug)|
| **同层跨 pass 组合**(record-only / +FOLD / +CSE / …) | 定位单条 pass 引入的错误结果 | ★★★★(直接指认是哪个 pass 出错)|
| **同 pass 组合跨输入种子**(FuzzP5ForceTrace) | 覆盖未想到的输入 | ★★★★(时间维度累积)|
| **同输入跨 guard 强制失败位置** | 每条 exit 物化无损 | ★★★(把稀疏的 deopt 变确定性)|

四项同时到位才是 P5 测试合格。缺任一项的等价影响见下表:

| 缺失项 | 后果 |
|---|---|
| 无三层 byte-equal | 录制期假设错 / 优化 pass 错误结果无法可靠抓住 |
| 无 pass 差分矩阵 | 发现错误结果时无法定位是哪 pass 引入,回归修复困难 |
| 无常驻 fuzz | 只跑「想到的」用例,罕见输入静默通过 |
| 无 deopt 注入 | snapshot 物化路径基本没被真正测过(自然 deopt 稀疏) |

### 1.2 P1 是 oracle,永远

依据 [P1 12 §9 不变式 10](../p1-interpreter/12-testing-difftest.md)「JIT 主防线复用 P1 harness——解释器永
不退役,是所有上层的 oracle」+ [00-overview §3](./00-overview.md)「fullmoon 的 deopt 落点是 crescent 而非
gibbous——deopt 语义以解释器为 oracle」。

P5 的 oracle 链:

```
   官方 lua5.1(P1 顶级 oracle,可通过 P1 12 golden 比对)
        ▲
        │  P1 差分已验证
        ▼
   crescent(P5 侧 oracle)
        ▲
        │  ★ P5 主防线:crescent vs fullmoon 必 byte-equal
        ▼
   fullmoon
```

**P4 gibbous 是三方差分的独立参照物,不是 oracle**——P4 与 fullmoon 发现的 bug 类型不同,若两者不一致而
crescent 与其中一方一致,以 crescent 为准判另一方 bug。

### 1.3 06-snapshot-deopt §4 「正确性收敛不可计划」的工程翻译

06-snapshot-deopt §4 字面:「+2-4 人年」的上界开放,因为 snapshot 机器正确性收敛时间**本质上不可计划**——由 fuzz
发现的 bug 衰减曲线决定,而非里程碑排期决定。

翻译到测试策略层:

- **常驻 fuzz 是必须品,不是奢侈品**(§5);
- **PT-N 里程碑不写「fuzz 无 bug」**,而写「fuzz N 天无 bug 衰减到零」;
- **测试基础设施本身要跑常驻监控**(fuzz 累积 hours + 崩溃 corpus 增长曲线);
- **不允许「性价比不划算」的退缩**(项目纪律:正确性防线不做成本折衷)——差分覆盖不足时不能因为「跑更长 fuzz 太贵」而不跑,这是正确性防线,不是收益优化。

### 1.4 -race 与 mmap 的物理不兼容

依据 [P4 08 §3.4 2026-07-02 addendum](../p4-method-jit/08-testing-strategy.md):P4 fuzz 在 `-race` 构建下
自动 skip——mmap+morestack 与 Go race detector 的 stack unwinder 物理不兼容(反思
[2026-07-01-p4-pj10-native-round](../../../llmdoc/memory/reflections/) lesson 1)。

**P5 一样的约束**:fullmoon trace 也在 mmap 段跑,`-race` 不兼容原样继承。含义:

- `FuzzP5ForceTrace` 在 `-race` 下 `t.Skip`(§5.4);
- `-race` job 只覆盖 P5 的**非 mmap 子集**(anchor 表 + P2 桥扩展的 Go 侧逻辑 + host 接口 Go 端);
- P5 mmap 段的并发正确性(共享 anchor 表、共享 trace 段的多 State 访问)靠**多 State 并发 non-race 差分**
  + `-count=N` 反复跑发现问题。

### 1.5 T-item 类目声明

**本文声明 T-item 的类目,不定编号**——具体 T1..Tn 编号由 [09-acceptance-checklist.md](./09-acceptance-checklist.md) 拥有:

| 类目 | 内容 | 本文对应 § |
|---|---|---|
| **T-DIFF**(差分主防线) | 三层 byte-equal / force-all-trace / IC 预热 | §2 |
| **T-OSR**(OSR 状态等价 P5 版) | deopt 注入 + 每 guard 强制失败 + 输出 byte-equal | §3 |
| **T-PASS**(pass 开关差分矩阵) | 每 pass 单独关 / 组合关 / 首个 divergent 定位 | §4 |
| **T-FUZZ**(常驻 fuzz) | FuzzP5ForceTrace / GC 压力联合 / 长时间运行天数 | §5 |
| **T-UNIT**(单元级) | IR printer / FOLD 表 / regalloc verifier / snapshot roundtrip | §6 |
| **T-PERF**(性能) | 无回归 + 常驻 workload 加速 gate | §7(引用 09)|
| **T-BUILD**(build tag / CI 矩阵) | wangshu_p5 + PR/nightly 分级 | §8 |

---

## 2. 三层 byte-equal 差分(主防线)

### 2.1 P1 12 §3.8 Runner 抽象:填 WangshuFullmoon slot

依据 [P1 12 §3.8](../p1-interpreter/12-testing-difftest.md) 的 Runner 抽象——P5 实现时**填 slot,不改
harness**:

```go
// test/difftest/runner.go —— P1 已建抽象;P3 已加 WangshuGibbous;P4 已加 WangshuGibbousJIT;
// P5 实现时新增 WangshuFullmoon(P1 12 §3.8 已注释预留):

type WangshuFullmoon struct {
    forceAllTraceRecord bool  // §2.2:强制录制模式
    deoptInjectMode     bool  // §3.2:deopt 注入模式
    passDisableMask     uint32 // §4.2:按位屏蔽 pass(0=禁 FOLD,1=禁 CSE,...)
}
```

Runner 接入不改比对框架——P1 12 §3.8 已抽象为「Runner.Run(src, arena) → capture」+ N 方比对。P5 只是让
比对从三方(oracle / crescent / gibbous-jit)扩到**四方**(oracle / crescent / gibbous-jit / fullmoon),
差异性质由 §2.3 判定矩阵决定。

### 2.2 force-all-trace 模式

**为什么 P5 也需要 force-all**:依据 [P4 08 §3.7](../p4-method-jit/08-testing-strategy.md) + [prove-the-path-under-test §2(b)](../../../llmdoc/guides/prove-the-path-under-test.md)——正常热度阈值下 fullmoon 覆盖率与
时序耦合,可复现性差、覆盖率不可控。force-all-trace 绕过 anchor 热度阈值,让所有满足以下条件的循环强制
录制:

- proto 在 P2 已升 TierGibbous(P4 段已装);
- 循环 pc 有 back edge(是 FORLOOP / TFORLOOP / 循环体内向后 JMP);
- 循环 pc 未被 anchor 表标 Blacklisted。

### 2.3 IC 预热的关键

**警告**(依据 SEED §2「trace 录制的起点是热 back edge」+ P4 08 §3.7 RW-10 教训):**fullmoon force 录制模式需
要 IC 预热,不能开机即录制**:

- P5 录制依赖 IC 反馈(依据 [02](./02-trace-recording.md) + [P4 03 §3](../p4-method-jit/03-speculation-ic.md)
  同源 IC feedback)决定投机类型 —— 循环里 `x + y` 若 IC 未热,不知道 x、y 是 number 还是 string,录制器
  没依据下 guard;
- P4 的 `SetForceAllPromoteJIT` 也吃过这个亏(依据 [bridge.go 争取的 warmed-IC retry window](../p2-bridge/04-try-compile-fallback.md)):升 P4 前必须等 IC 计数够,否则投机模板生成质量差、大量 deopt;
- P5 更严——trace 录制期只有一次机会拿到类型信息,若 IC 冷则录制器要么弃 trace 要么下极保守 guard。

**推荐语义**:

```go
// force-all-trace 的两阶段语义
func (r *WangshuFullmoon) Run(src string, arena []byte) (capture, error) {
    st := wangshu.NewState(...)
    // 阶段 1:关 force,正常跑「warm-up 轮」让 IC + P4 段先建立
    st.SetForceAllTraceRecord(false)
    _ = warmUpRun(st, src, /*iters=*/100)  // 或统计 IC hitcount 达阈值
    // 阶段 2:开 force,再跑被测轮
    st.SetForceAllTraceRecord(true)
    return capturedRun(st, src)
}
```

warm-up iters 数字待 PT7 校准。**空测风险**:若 warm-up 不够,force 时录制器全弃 trace,capture 与
crescent 一致但 fullmoon 覆盖率为零 —— 依据 [prove-the-path-under-test §2(b)](../../../llmdoc/guides/prove-the-path-under-test.md) 正向 tier 断言:

```go
// 非空守卫:force 后必须真录出 trace
if TraceCountAfter(st) == 0 {
    t.Fatalf("force-all-trace 空测:0 trace recorded, may need more warm-up iters")
}
```

### 2.4 四方判定矩阵

依据 [P1 12 §3.3](../p1-interpreter/12-testing-difftest.md) 三方矩阵扩展:

| oracle | crescent | gibbous-jit | fullmoon | 结论 |
|---|---|---|---|---|
| == | == | == | == | 通过 |
| == | == | == | ≠ | **fullmoon 孤立错**(最强信号,P5 bug)|
| == | == | ≠ | == | gibbous-jit 独立错(P4 bug,不阻塞 P5) |
| == | == | ≠ | ≠ | 双 backend 同样出错——可能是共享路径 bug(如 P2 IC feedback 错误结果传下去)|
| == | ≠ | any | any | crescent 独立错(P1/P3 已建防线应先抓)|
| ≠ | == | == | == | 三层皆偏离 oracle——如 gopher/官方偏差(依据 P1 12 §4.7)|

**层间差分是强制检查**:fullmoon 孤立错(第 2 行)PR 直接不合入。

### 2.5 CI 强制检查

依据 [architecture §4 不变式 2](../architecture.md) + [P4 08 §3.5](../p4-method-jit/08-testing-strategy.md):

| 阶段 | 跑什么 | 时长 | build tag |
|---|---|---|---|
| **PR** | T-DIFF 形式级单测 + force-all-trace 小语料 byte-equal + T-OSR 每 guard 强制失败一次 + T-PASS 小矩阵 | < 15 min | `wangshu_p5,wangshu_p4,wangshu_p3,wangshu_profile` |
| **PR(P1-P4 不豁免)** | P1 三方 + P2 决策 + P3 V1-V18 + P4 V1-V22 全跑 | < 8 min | 同上 |
| **nightly** | 大语料 fuzz + 全 pass 矩阵 + 双架构 + longevity | 6-12 h | 同上 |
| **release** | nightly 全跑 + 常驻 fuzz cluster 输出总结 | 不限 | 同上 |

**关键纪律**:P5 上线不豁免 P1-P4——四层验收套并存跑,这是 [P4 08 §0.2](../p4-method-jit/08-testing-strategy.md) 「每上层接续下层验收套」纪律的递归应用。

---

## 3. Deopt 注入(P5 主动触发每条 exit)

### 3.1 P5 侧「不注入 = 结构性失明」

依据 [P4 08 §5.1](../p4-method-jit/08-testing-strategy.md):deopt 路径天然稀疏(投机 confidence ≥0.99 时
99% 走快路径,1% 才 deopt),普通 fuzz 发现不出来。

P5 侧更严:

- fullmoon 的 snapshot 比 P4 复杂(依据 [06-snapshot-deopt §3](./06-snapshot-deopt.md) 表:P4 = 空;P5 = frames[1..N] +
  slots{稀疏} + sinks[]);
- P4 的 OSR exit「栈槽即真相」,exit 序列几乎空——正常 e2e 撞到 deopt 也不测什么;
- **P5 每次 deopt 都是「按 snapshot 逐帧物化 + unsink」,是一整套编译期烧入的 store 序列**——每条 exit
  的物化配方都不同,必须每条独立执行一次才验证得到。

### 3.2 每 guard 强制失败一次

依据 [P4 08 §5.2](../p4-method-jit/08-testing-strategy.md) + [prove-the-path-under-test §2(a)](../../../llmdoc/guides/prove-the-path-under-test.md) 毒化哨兵思路。

**核心 API 建议(待 PT5 实现)**:

```go
// internal/fullmoon —— deopt 注入 hook(build tag wangshu_p5,testing-only)
// 类比 P4 spec 的 SetGuardForceFailFirst(P4 未实现),但 P5 侧不是 optional
func SetGuardForceFailFirst(state *State, traceID uint32, guardIndex uint32)

// 一次性打开:某 trace 的第 k 号 guard 下次 hit 时强制失败
// 目的:验证该 guard 对应的 snapshot 物化路径无损
```

### 3.3 联合验证形式:每 (trace, guard) 组合遍历

```go
//go:build wangshu_p5
func TestEveryGuardForceFailOnce(t *testing.T) {
    for _, sc := range guardCoverageCorpus() {  // 覆盖每种 guard kind
        t.Run(sc.name, func(t *testing.T) {
            oracleOut := runCrescentOnly(sc.src)
            st := wangshu.NewState(...)
            st.SetForceAllTraceRecord(true)
            _ = warmUpRun(st, sc.src, 100)
            traces := collectTraces(st)
            if len(traces) == 0 { t.Fatalf("no trace after warm-up") }  // 非空守卫
            for _, tr := range traces {
                for k := uint32(0); k < tr.NumGuards(); k++ {
                    st2 := clone(st)
                    fullmoon.SetGuardForceFailFirst(st2, tr.ID, k)
                    injOut := capturedRun(st2, sc.src)
                    if !bytes.Equal(oracleOut, injOut) {
                        t.Fatalf("trace=%d guard=%d: deopt output mismatch", tr.ID, k)
                    }
                }
            }
        })
    }
}
```

### 3.4 与 §2.4 判定矩阵联动

deopt 注入不改判定矩阵——注入模式下的 capture 仍参加四方比对。**特殊语义**:注入模式下 fullmoon 段
guard 强制失败退 crescent,fullmoon 「执行路径」变成「fullmoon 前半 + crescent 后半」;这条混合路径与
「全程 crescent」byte-equal 才通过,是 T-OSR 的核心标准。

### 3.5 PT5 必交付,不是 optional(P4 教训)

**关键警示**(依据 [P4 08 §5.2 2026-07-02 addendum](../p4-method-jit/08-testing-strategy.md)):P4 侧的 spec
过 `SetGuardForceFailFirst` + `SetDeoptInjectEvery`——**两个 hook 均未实现**,P4 08 §5.2 复核标注为
followup。P4 靠 e2e 自然覆盖侥幸没出事,但 spec 的深度验证从未跑通。

**P5 侧不能重复这个错误**:

- P5 snapshot 物化路径的复杂度远超 P4(P4 = 空 vs P5 = frames+slots+sinks),自然 e2e 覆盖不到每条 exit;
- **PT5 必须交付 deopt 注入 hook**,并在 PT5 验收时跑通完整 T-OSR;
- 若 PT5 只出「hook 未实现」就往后推,PT9 端到端发现 snapshot bug 时定位成本会指数级增加(依据 06-snapshot-deopt §4
  「正确性收敛不可计划」的最坏情况)。

**PT5 里程碑必须包含**:①`SetGuardForceFailFirst` 完成 ② `TestEveryGuardForceFailOnce` 全绿 ③每 fb kind
× 每层 guard 至少 1 例(依据 [P4 08 §4.3](../p4-method-jit/08-testing-strategy.md) 覆盖矩阵)。

### 3.6 GC 压力联合验证(依据 P4 08 §7.4)

deopt 期间 unsink 会分配(§5 P5 host 扩展 `UnsinkAlloc`)——若配合高频 GC(GCPAUSE=1),unsink 中途触
发 GC:

```go
func TestDeoptGCStress(t *testing.T) {
    for _, sc := range allocHeavyDeoptCorpus() {
        for _, pause := range []int{1, 2, 5, 200} {
            base := runWithPauseAndInjection(sc.src, /*inject=*/false, pause)
            inj  := runWithPauseAndInjection(sc.src, /*inject=*/true, pause)
            if !bytes.Equal(base, inj) {
                t.Fatalf("GC + deopt breaks transparency @pause=%d %s", pause, sc.name)
            }
        }
    }
}
```

如果 `UnsinkAlloc` 内部有未上 shadow stack 的中间对象(违反 [P1 06 §6.3](../p1-interpreter/06-memory-gc.md)
纪律),高频 GC 会必然发现——把偶发变必现(依据 [P1 12 §5.2](../p1-interpreter/12-testing-difftest.md))。

---

## 4. Pass 开关差分矩阵

### 4.1 与 04 §8 的开关约定

依据 [04-optimization-passes.md §8](./04-optimization-passes.md)(未来章):每条 pass 必须提供独立关闭开关,
接口约定:

```go
// internal/fullmoon —— pass 开关(testing-only,build tag wangshu_p5)
type PassMask uint32
const (
    PassFOLD PassMask = 1 << iota
    PassCSE
    PassDCE
    PassGuardDedup
    PassPeel
    PassSink
)

func SetPassDisabled(state *State, disabled PassMask)
```

### 4.2 配置矩阵

按累积增益方向:record-only 基线 + 逐步加 pass。**首个出现差异的 config 直接指认是哪个 pass 出错**(依据 SEED
§5 第三点字面):

| 配置 | 禁用集 | 期望 |
|---|---|---|
| C0(基线) | 全禁,只录制 | byte-equal crescent |
| C1 | 禁 CSE/DCE/dedup/peel/sink,只开 FOLD | byte-equal crescent |
| C2 | 禁 DCE/dedup/peel/sink,开 FOLD/CSE | byte-equal crescent |
| C3 | +DCE | byte-equal crescent |
| C4 | +guard-dedup | byte-equal crescent |
| C5 | +peel | byte-equal crescent |
| C6(全开) | 无 | byte-equal crescent |

**首个 divergent config 定位规则**:若 C4 出现 mismatch 而 C3 未出现 → **guard-dedup 引入错误结果**,定位到
`internal/fullmoon/opt/guardDedup.go` 类文件。这是差分的一阶价值。

### 4.3 与 §2 的关系

pass 差分是三层差分的**分层版本**:三层差分回答「fullmoon 是否与解释器语义一致」,pass 差分回答「不一致
是哪个 pass 引入的」。两者互补,前者强制检查,后者是定位工具 + 回归防线。

### 4.4 CI 分级

| 语料 | PR | nightly |
|---|---|---|
| 小语料(30 脚本) | 全 pass 矩阵 C0-C6 都跑 | 同 |
| 大语料(rolling seed 200 万)| 只跑 C6(全开)+ 用 C0 抽样对比 | 全矩阵 |

小语料在 PR 阶段跑全矩阵——CI 时间预算许可(每 config 30 脚本 × 6 config × <100ms/脚本 = ~20 秒);大
语料只在 nightly 全矩阵。

---

## 5. FuzzP5ForceTrace(常驻 fuzz)

### 5.1 P4 fuzz 模式扩展

依据 [P4 08 §2.4 V22 表](../p4-method-jit/08-testing-strategy.md):P4 实现形式是 `FuzzP4ForceAllPromote`
(force-all promote + error-existence 差分 + byte-equal 差分)。P5 侧延伸:

```go
//go:build wangshu_p5 && wangshu_profile && !race
// (race build tag exclusion 依据 §1.4)

// FuzzP5ForceTrace —— 常驻 fuzz(build tag wangshu_p5,-race 下自动 skip)
// 依据 P4 08 §2.4 V22 表 + 09-acceptance-checklist §1 「fuzz 时长决定正确性置信度」
func FuzzP5ForceTrace(f *testing.F) {
    // 语料 seed:P4 已建 corpus + P5 独有形式(§5.2)
    for _, s := range seedCorpus() { f.Add(s) }
    f.Fuzz(func(t *testing.T, seed []byte) {
        if !isValidP1(seed) { return }
        oracleOut, oracleErr := runCrescentOnly(seed)
        st := wangshu.NewState(...)
        st.SetForceAllPromoteJIT(true)
        st.SetForceAllTraceRecord(true)
        _ = warmUpRun(st, seed, 100)
        p5Out, p5Err := capturedRun(st, seed)
        // ① error-existence 差分(依据 V22 addendum)
        if (oracleErr == nil) != (p5Err == nil) {
            // 排除 budget / GC 时机类分叉
            if !isBudgetSkew(oracleErr, p5Err) {
                t.Errorf("error existence divergence: oracle=%v p5=%v seed=%x", oracleErr, p5Err, seed)
            }
            return
        }
        // ② byte-equal 差分
        if oracleErr == nil && !bytes.Equal(oracleOut, p5Out) {
            t.Errorf("output divergence seed=%x", seed)
        }
    })
}
```

### 5.2 语料扩展:P5 独有形式

P4 fuzz corpus 覆盖 F1-F7 边界与算术投机形式——对 P5 不够。**P5 独有关注的形式**(与
[02 §NYI](./02-trace-recording.md) 协调):

- **循环内跨函数调用**(trace 内联主场,依据 [01-launch-judgment §2 第一类负载](./01-launch-judgment.md));
- **循环内类型不稳定**(loop 里 var 时 number 时 string,触发多变态 guard);
- **循环内 metamethod 触发**(force 走 helper vs sink 后合并);
- **循环内分配密集**(触发 sink 优化的物理形式);
- **循环内表增删**(触发 gen bump + trace IC 失效);
- **多层嵌套循环**(不同 anchor 交互);
- **深递归 + 循环**(trace 内联导致 snapshot frames 深度 > 3)。

seed corpus 位置:`testdata/fuzz/FuzzP5ForceTrace/`(与 P1/P4 fuzz corpus 相同的目录规范)。

### 5.3 常驻 fuzz cluster(09-acceptance-checklist §1 承诺)

09-acceptance-checklist §1「持续 fuzz 作为常驻基础设施——P5 的正确性置信度 = fuzz 时长的函数,§8 的 nightly 长时间运行任务在 P5 期间
应升级为专用 fuzz 集群」。

**建议实现方式**(共享机资源约束下的现实版本:fuzz 限并发、bench 串行、单个重任务):

- **`-parallel=4`**(shared 机上限,不能打满);
- **`-cpu=1`**(与共用户不打架);
- 分片按 seed base 时间戳(与 [.github/workflows/nightly-diff-fuzz.yml](../../../.github/workflows/nightly-diff-fuzz.yml) 每日 rolling 方式一致)
- **累积统计**入 `results/p5-fuzz-hours.md`(与 [09](./09-acceptance-checklist.md) 的 T-item 数字对齐);
- **fuzz cluster 是逻辑概念,不是物理独立机器**——共享机上按时段调度 + 记账。

### 5.4 崩溃 corpus 回收进语料库的纪律

依据 P4 08 §3.4 + issue #36 nightly crash corpus 先例:fuzz 发现崩溃 →
最小化(依据 [P1 12 §3.6](../p1-interpreter/12-testing-difftest.md))→ 入 `testdata/fuzz/FuzzP5ForceTrace/`
seed → 未来运行时自动重跑(fuzz 引擎语义)。

**关键**:seed 入库是**回归防线**,而非「fuzz 白发现白算」。P5 侧因组合空间超 P4,corpus 增长会更快,预
留 `testdata/fuzz/FuzzP5ForceTrace/` 目录容量与 CI 时长预算(每 seed 重跑 ~100ms,1000 seeds ≈ 100 秒
CI 时长)。

### 5.5 -race 明确 skip 与替代覆盖

依据 §1.4:P5 fuzz `-race` 下 `t.Skip("mmap+morestack incompatible with race detector")`。**替代覆盖**:

- non-race build 下的 fuzz(时间维度累积);
- non-race build 下 `go test ./test/difftest/... -count=10`(多次执行发现 race condition);
- non-race build 下 nightly 长时间运行(数百万迭代累积);
- Go 侧 anchor 表 / P2 桥扩展 / host 接口 Go 端**在 `-race` 下正常跑**(它们没进 mmap 段)。

---

## 6. 单元级测试

### 6.1 IR printer golden

依据 [03-ir-design.md](./03-ir-design.md)(未来章)的 IR 文本形式:每条 pass 前后 dump IR,golden 比对。

```go
// internal/fullmoon/ir_golden_test.go
func TestIRAfterFOLD(t *testing.T) {
    tests := []struct {
        name, luaSrc, wantIR string
    }{
        {"const_fold_add", "return 1 + 2", "  v1 = k(3)\n  ret v1"},
        // ... FOLD 规则表每条一例
    }
    for _, tc := range tests {
        got := recordAndFOLD(tc.luaSrc)
        if got != tc.wantIR { t.Errorf("%s: want\n%s\ngot\n%s", tc.name, tc.wantIR, got) }
    }
}
```

golden 更新 diff 在 PR review 显眼(依据 [P1 12 §4.8](../p1-interpreter/12-testing-difftest.md) 豁免清单
的可审计性原则)。

### 6.2 FOLD 规则表测试

每条 FOLD 规则(依据 [04 §3](./04-optimization-passes.md))单测:

- 输入 IR(手构造,不经录制);
- 期望输出 IR(手写);
- 边界 case:NaN、-0、GC 移动语义、元方法可见性(某些「常量」不能 FOLD 因为可能有 metatable)。

### 6.3 regalloc verifier(依据 [05 §8](./05-register-allocation.md))

regalloc 后的 IR 逐条断言:

- 每条 IR 用到的 vreg 都有物理寄存器或 spill 槽;
- exit 时活着的 IR 值都可被 snapshot 引用(依据 [06 §4.2](./06-snapshot-deopt.md) regalloc 与 snapshot 的
  耦合约定);
- 无未定义使用(use before def);
- 无死写(def without use);
- callee-saved 语义正确(依据 P4 06 §4.1.5 Go ABIInternal 兼容)。

verifier 是 dev-only assertion(build tag `wangshu_p5_verify`),不进 release build。

### 6.4 snapshot encode/decode roundtrip

依据 [06 §3](./06-snapshot-deopt.md):snapshot 表按压缩形式编码。roundtrip test:

```go
func TestSnapshotRoundtrip(t *testing.T) {
    for i := 0; i < 1000; i++ {
        orig := randSnapshot(rng)  // 随机生成 frames + slots + sinks
        encoded := orig.Encode()
        decoded := DecodeSnapshot(encoded)
        if !orig.Equal(decoded) { t.Errorf("snapshot mismatch #%d", i) }
    }
}
```

编码错误会直接把 deopt 物化搞错,是最容易发现的 bug 类之一,单元层就应该覆盖。

### 6.5 exit stub metadata consistency

每 trace emit 后的元数据(exit stub 地址 ↔ guard index ↔ snapshot index 三方映射)交叉验证:

```go
func TestTraceExitMetadataConsistent(t *testing.T) {
    tr := compileTrace(sampleIR())
    for k := 0; k < tr.NumGuards(); k++ {
        stubAddr := tr.ExitStubAddr(k)
        snap := tr.GuardSnapshot(k)
        if stubAddr == 0 || snap == nil { t.Errorf("guard %d incomplete metadata", k) }
    }
}
```

---

## 7. 性能验证(引用 09)

**范围声明**:本文只列性能验证的**方法学**,具体数字与 PT 映射由 [09-acceptance-checklist.md](./09-acceptance-checklist.md) 拥有——本文不重复。

**方法学要点**(依据 [P4 08 §8](../p4-method-jit/08-testing-strategy.md) + [perf-optimization-workflow](../../../llmdoc/guides/perf-optimization-workflow.md)):

- **基准形式的硬约束**([P1 12 §6.1](../p1-interpreter/12-testing-difftest.md) 列内核):循环在 Lua 内、
  一次 CallFn 进 VM;
- **P4 无 regression gate**(P4 build 关或开-but-cold 都不能比 P4 单跑慢);
- **适合 P5 的负载加速 gate**(SEED §1.2 四类「P4 结构性吃不下的负载」,PT9 端到端时给具体数字);
- **profile 优先于目标数字**(依据 [perf-optimization-workflow §7](../../../llmdoc/guides/perf-optimization-workflow.md)):PT9 若不达预期,先跑 cpuprofile 定位瓶颈,不强刷数字;
- **跨机器复测同硬件**([P4 08 §8.4](../p4-method-jit/08-testing-strategy.md));
- **常驻 workload 累积**:与 §5 常驻 fuzz 一样,常驻性能监控入 `results/p5-perf-timeline.md`。

具体 T-PERF 编号与验收数字见 [09](./09-acceptance-checklist.md)。

---

## 8. CI wiring

### 8.1 build tag 建议

依据 [P4 09 §1](../p4-method-jit/08-testing-strategy.md#91-多-build-tag) 命名(`wangshu_p4`):

```
default                                                 —— P1-only
wangshu_profile                                         —— P2 桥启用
wangshu_profile,wangshu_p3                              —— P3 完整
wangshu_profile,wangshu_p3,wangshu_p4                   —— P4 完整
wangshu_profile,wangshu_p3,wangshu_p4,wangshu_p5        —— P5 完整(本文语境)
```

**为什么 `wangshu_p5` 依赖 `wangshu_p4`**:依据 [00-overview §3](./00-overview.md) fullmoon 叠在 gibbous 之上——
P5 的 host 接口是 `P4HostState` 扩展(§5),jitContext 是 P4 版本 +3 字段(§4.3),必然需要 P4 已编入。

### 8.2 job 矩阵

| job | build tag | 平台 | 时长预算 |
|---|---|---|---|
| PR: p5-unit | wangshu_p5 + p4/p3/profile | ubuntu-latest amd64 | 3 min |
| PR: p5-diff-small | 同 | ubuntu-latest amd64 | 5 min |
| PR: p5-pass-matrix | 同 | ubuntu-latest amd64 | 3 min |
| PR: p5-fuzz-smoke | 同 | ubuntu-latest amd64 | 2 min(`-fuzztime=30s`)|
| PR: race-non-p5 | wangshu_p5 + `-race` | ubuntu-latest amd64 | 5 min(P5 fuzz 自动 skip,依据 §1.4)|
| PR: p5-arm64 | 同 | ubuntu-24.04-arm | 5 min(小语料)|
| nightly: p5-fuzz-long | 同 | ubuntu-latest amd64 | 2 h |
| nightly: p5-pass-matrix-full | 同 | ubuntu-latest amd64 | 1 h |
| nightly: p5-longevity | 同 | ubuntu-latest amd64 | 4 h(数百万迭代)|
| nightly: p5-arm64-full | 同 | ubuntu-24.04-arm | 3 h |

PR 总时长目标 < 20 min(依据 CI 快反馈约束)。

### 8.3 与 nightly-diff-fuzz.yml 的集成

现有 [.github/workflows/nightly-diff-fuzz.yml](../../../.github/workflows/nightly-diff-fuzz.yml) 的 matrix
增加 `- variant: p5` 行(与 p1/p3/p4 并列),同样的 rolling seed + go-fuzz 45m,tags 换为 `wangshu_p5
wangshu_p4 wangshu_p3 wangshu_profile`。

### 8.4 不放这里(纪律)

**shared-machine 资源限制**(`-parallel=4` / `-cpu=1` / 不 pgrep 轮询)不属于本文——那是
共享机资源纪律(fuzz 限并发、bench 串行、单个重任务),与设计文档正交。本文只承诺 CI 工作量,具体资源调度由 llmdoc feedback 文档管。

---

## 9. 开放问题

- **差分覆盖度量**:依据 [P1 12 §11](../p1-interpreter/12-testing-difftest.md) 缺口——「如何度量 fuzz 的
  覆盖度」在 P5 更急迫(组合空间超大)。可能路径:coverage-guided fuzz(go-fuzz native 已支持)+ per-op
  的 trace 内联次数计数入 test summary。留 PT7 校准;
- **崩溃 corpus 的跨版本归属**:P5 seed corpus 发现的 seed 若因 IR 变更失效(某 IR op 被删),归档策略:
  倾向永久保留 + 标 legacy,与 P4 corpus 归属一致((corpus 必须入仓,不引用未跟踪路径));
- **pass 开关矩阵爆炸**:6 个 pass = 64 种组合。目前只跑 7 种累积 config(§4.2),漏「非累积」组合(如
  只开 CSE+sink 不开 FOLD)——PT7 校准若发现漏抓 bug,考虑随机采样 32 种组合;
- **anchor 表并发正确性的独立验证**:§5 `-race` skip 让 mmap 段跑不了 race detector,但 anchor 表 Go 端
  的并发操作(多 State 同时录制 / evict)应能在 race 下验证——需要单独一个 `TestAnchorTableRace` 只操作
  Go 侧(不进 mmap 段),PT5 完成;
- **T-item 与 PT 里程碑映射**:本文只声明类目,具体 T-N × PT-N 表在 [09](./09-acceptance-checklist.md)。
  09 完成时若 T-item 数量差距较大(如 T1..T20),需要重新审视本文类目切分粒度是否合适;
- **fullmoon 与 P4 fuzz 的相互干扰**:同一 CI job 里若 force-all P4 + force-all P5 同时开,会不会出现
  「P4 段被跑热了但 P5 anchor 检测点覆盖不到」类交互——PT7 端到端阶段实测确认;
- **首个宿主 workload 的 T-PERF 具体数字**:与 [01-launch-judgment §3](./01-launch-judgment.md) 判定标准预登记一致,数字
  待 P4 验收时预登记,PT9 端到端时实现;
- **`SetGuardForceFailFirst` 与 P4 未实现的等价路径**:P4 侧走 `internal/crescent/gibbous_pj5_self_e2e_test.go` 等 e2e 覆盖(依据 [P4 08 §5.2 addendum](../p4-method-jit/08-testing-strategy.md))——P5 若也想走这
  条路,能否找到类似 e2e 挂载点?PT5 前论证清楚。

---

相关:
[./09-acceptance-checklist.md](./09-acceptance-checklist.md)(§1 验收与差分)· [./06-snapshot-deopt.md](./06-snapshot-deopt.md)(§4 收敛不可计划)·
[04-optimization-passes.md](./04-optimization-passes.md)(pass 开关约定,见 §4)·
[05-register-allocation.md](./05-register-allocation.md)(regalloc verifier,见 §6.3)·
[06-snapshot-deopt.md](./06-snapshot-deopt.md)(deopt 注入 hook = PT5 必交付,见 §3.5)·
[07-system-integration.md](./07-system-integration.md)(测试架构基于 host 接口扩展)·
[09-acceptance-checklist.md](./09-acceptance-checklist.md)(**T-item 编号 + 数字所在**)·
[../p4-method-jit/08-testing-strategy.md](../p4-method-jit/08-testing-strategy.md)(P4 测试策略,本文直
接对位)·
[../p1-interpreter/12-testing-difftest.md](../p1-interpreter/12-testing-difftest.md)(P1 差分测试单一事
实源,`WangshuFullmoon` slot 预留)·
[../../../llmdoc/must/design-premises.md](../../../llmdoc/must/design-premises.md)(原则 2 层间差分主防
线)·
[../../../llmdoc/guides/prove-the-path-under-test.md](../../../llmdoc/guides/prove-the-path-under-test.md)
(P5 是该 guide 最大新用户 —— §2.3 IC 预热 / §3.5 PT5 deopt hook / §5.4 corpus 回收进语料库)·
[../../../llmdoc/guides/perf-optimization-workflow.md](../../../llmdoc/guides/perf-optimization-workflow.md)
(perf 验证方法学,见 §7)·
[../../../.github/workflows/nightly-diff-fuzz.yml](../../../.github/workflows/nightly-diff-fuzz.yml)(§8.3
现有 nightly matrix 集成)
