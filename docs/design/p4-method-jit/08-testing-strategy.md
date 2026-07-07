# P4 §8:测试策略与验收——luajc 档 + 差分主防线 + deopt 注入 + 双架构 CI

> 状态:**详细设计**(P4 整体仍是「架构决策深度」,但「投机错果是 JIT 最危险 bug 类」需要在文档阶段把测试与验收口径钉死,本文按详细设计深度展开,与 [./03-speculation-ic.md](./03-speculation-ic.md) / [./04-osr-deopt.md](./04-osr-deopt.md) 同档)。本文是 P4 子文档集的「测试与验收」单一事实源——luajc 档量化锚点 + 同 Proto 差分主防线 + OSR 状态等价 + deopt 注入 + GC 压力 fuzz + 双架构双跑 + 风险与开放问题,一次定稳。
>
> **本文定位一句话**:**P4 是望舒第一个会说谎的层**——投机模板有 guard 漏判可能,产出错果不崩溃不报错,有限用例测不出。差分主防线把「同 Proto 走 crescent vs gibbous-jit byte-equal」从口号变成机制;deopt 注入把「每条 exit 路径」从「天然稀疏」变成「主动踩热」;双架构双跑把 amd64/arm64 都纳入 CI 硬门禁。三件事同时存在才算 P4 测试合格,任一缺失即 P4 验收不收单。
>
> 上游契约:
> [../roadmap.md](../roadmap.md)(§1 校准测量 luajc 164μs 锚点 / §4 P4 验收 / §5 原则 2 投机错果是 JIT 最危险 bug 类 / 原则 3 任何检查停下不亏)、
> [../../../llmdoc/must/design-premises.md](../../../llmdoc/must/design-premises.md)(前提一负载形状 / 前提三五原则——原则 2 是本文 §3 / §11.1 主防线的最高上游)。
>
> P3 对位(本文核心镜像章节):
> [../p3-wasm-tier/08-testing-strategy.md](../p3-wasm-tier/08-testing-strategy.md)(P3 验收单一事实源,1056 行,**直接对位文档**——P3 V1-V18 是 P4 接续轨道:正确性轴 V1-V13 + 性能轴 V14-V16 + 工程轴 V17-V18 的形式本文 §2 原样接续,**P4 增项 V19-V22 是其延伸**;P3 08 §2 force-all 模式与本文 §3 / §5 deopt 注入并存;P3 08 §4.3「P3 验证面比 P4 窄,先 P3 后 P4 的风险阶梯」在本文 §1 / §11.1 兑现)、
> [../p1-interpreter/12-testing-difftest.md](../p1-interpreter/12-testing-difftest.md)(P1 差分测试矩阵单一事实源;§3.8 Runner 抽象是 P4 接入的轨道,§7 P4 行预留是本文 §4 接入的预设位,§8 CI 门禁是本文 §6 双架构双跑的上游;26 条验收口径总表对 P4 原样适用)。
>
> 同子目录对位:
> [./03-speculation-ic.md](./03-speculation-ic.md)(P4 §3 类型投机机制——§3.5「guard 多判 vs 漏判」语义边界是本文 §3.4 差分主防线的物理基础)、
> [./04-osr-deopt.md](./04-osr-deopt.md)(P4 §4 OSR exit 协议——§5 deopt 风暴的物理学是本文 §5 deopt 注入工作模型的来源,§3.7 物化序列编译期静态生成是本文 §4 端到端校验的目标)、
> [./06-backends.md](./06-backends.md)(P4 §6 双后端——§5 双架构测试纪律是本文 §6 接续的具体口径,§6.1 PJ0-PJ11 里程碑是本文 §2.5 各 V 对应 PJ 映射的来源)。
>
> guide 应用:
> [../../../llmdoc/guides/prove-the-path-under-test.md](../../../llmdoc/guides/prove-the-path-under-test.md)(**P4 是该 guide 的最大用户**——「测试通过 ≠ 在测的路径被走到」在 P4 投机静默错果维度反复出现,本文 §3.7 / §4.3 / §5.5 / §11.6 三处反向引用)、
> [../../../llmdoc/guides/perf-optimization-workflow.md](../../../llmdoc/guides/perf-optimization-workflow.md)(perf 判定纪律四件套:§1 profile 先行 + §3 benchmark 否决门 + §5 跨机器基线对照 + §7 立项数字 vs profile 实测瓶颈——本文 §8.4 / §8.5 / §11.7 直接引用)。
>
> P3 现状作镜子:
> [../p3-wasm-tier/implementation-progress.md](../p3-wasm-tier/implementation-progress.md) §11(PW9 端到端验收的方法论修正——空测陷阱 + 已验证非空 force-all 路径)、
> [../../../llmdoc/memory/reflections/2026-06-15-p3-pw9-acceptance-perf-round.md](../../../llmdoc/memory/reflections/2026-06-15-p3-pw9-acceptance-perf-round.md)(空测陷阱教训——PW9 早期把「不升层 vararg 顶层 chunk」测出 ≈1.0x 据此立错的后续里程碑,修正后真实 2.58x;**P4 验收必须从第一天起防止该家族陷阱**)。

对应 Go 包:`internal/gibbous/jit`(P4 后端,与 `internal/gibbous/wasm` 同层并列,承 [../architecture.md](../architecture.md) §1);测试入口位置 `test/difftest/p4_test.go` / `test/conformance/p4_test.go` / `internal/gibbous/jit/*_test.go` / `benchmarks/realworld`(luajc 档基准)。

---

## 0. 定位

### 0.1 一句话:P4 是望舒第一个会说谎的层

P1/P2/P3 是「不投机」的——P1 解释器按 Lua 5.1 语义现场判定走快慢路径;P2 决策机静态分析可编译性;P3 翻译器把可编译子集翻成 Wasm,所有「快路径检查」是**语义分发**(失败也是合法路径,落到 helper 得正确结果,承 [../p3-wasm-tier/06-ic-feedback-consume.md](../p3-wasm-tier/06-ic-feedback-consume.md) §1)。它们不存在「假设错」——「层间 byte-equal 差分」防的是**实现 bug**(翻译错码 / IC 失效降级出错 / GC 漏根),不是**投机错果**。

P4 不一样。P4 第一次引入「运行期假设可能被打破」(承 [./03-speculation-ic.md](./03-speculation-ic.md) §0 / §4):

- **投机模板把 else 分支裁掉**:P3 的「IsNumber×2 + f64.add + else 调 helper」三件套,P4 只发「IsNumber×2(guard)+ f64.add」,失败 OSR exit。
- **guard 漏判 = 静默错果**:guard 多判(查得太严)只损性能,可观测;**guard 漏判**(该查没查)直接产出错误结果**不崩溃、不报错**——是 [../roadmap.md](../roadmap.md) §5 原则 2 字面点名的「JIT 最危险 bug 类」。
- **有限用例覆盖不足**:guard 漏判的触发条件天然稀疏(只在投机假设不成立的罕见输入命中),普通 e2e 跑万千用例可能一条不撞;**只有「同 Proto 走两层逐字节差分」+ 持续 fuzz + 错误路径专门构造**才能撞出。

**这是 P4 测试与验收的第一性原理**——一切口径设计都从「P4 会说谎」推出:差分主防线优先级最高(§3)、OSR 状态等价专项必加(§4)、deopt 注入必做(§5)、双架构双跑必跑(§6)、风险阶梯设计必守(§11.1)。P3 验收 V1-V18 已经把「层间 byte-equal 差分 + 强制全升 + GC 压力 + 双 build + race」铺成轨道,**P4 的测试套是在这条轨道上加 V19-V22 增项,不是另起炉灶**。

### 0.2 与 P3 08-testing-strategy 的关系:验收套接续轨道

[../p3-wasm-tier/08-testing-strategy.md](../p3-wasm-tier/08-testing-strategy.md)(P3 08,1056 行)是本文最直接对位的文档。两文关系:

| 维度 | P3 08(已交付的轨道)| 本文(P4 增项)|
|---|---|---|
| 验收编号 | V1-V18(13+3+2 = 18 条)| **V1-V18 全部继承 + V19-V22 增项**(承 §2.4)|
| 主防线 | crescent vs gibbous(P3 翻译)byte-equal | crescent vs gibbous-jit byte-equal(承 §3)|
| 强制全升模式 | force-all-promote 绕热度阈值,所有 Compilable Proto 编译(P3 08 §2.2)| **force-all + deopt 注入双重模式**(承 §10.3 / §5.2)|
| Runner 抽象 | `WangshuGibbous` runner(P3 08 §2.1)| **新增 `WangshuGibbousJIT` runner**(承 §3.1)|
| 性能门 | 循环密集 ≥2x over P1(P3 08 V14)| **替换为 ≥luajc 档(164μs 水位)**(承 §1.2 / §8.2)|
| 双架构 CI | 不强制(P3 wazero 跨架构由 wazero 自行处理)| **硬门禁**(amd64 + arm64 各跑全套,承 §6)|
| GC 压力 fuzz | 上 gibbous(P3 08 §3)| **上 gibbous-jit + 物化期 GC 触发**(承 §7.4)|
| 风险阶梯 | P3 是确定性 bug(翻译错则任何输入必现,P3 08 §4.3)| **P4 是条件性 bug(投机错果只在打破假设的罕见输入触发,差几个数量级难撞)**(承 §11.1)|

**关键纪律**:**P3 08 全部 V1-V18 在 P4 build 下不豁免,继续跑**——P4 引入投机不是把 P3 的验收套替换掉,是在其上叠加 V19+ 增项。这与 P3 上线时 P1 三方差分仍跑(P3 08 §1.5)一样的递归——**每个上层都接续下层验收套,各自只新增自己的增项**,确保「下层正确性是上层正确性的前提条件」始终被持续验证。

### 0.3 与 P1 12-testing-difftest 的关系:CI 门禁的最终接入点

[../p1-interpreter/12-testing-difftest.md](../p1-interpreter/12-testing-difftest.md)(P1 12,850 行)是 P1/P3/P4 共享的 CI 门禁基础。P4 接入点:

- **§3.8 Runner 抽象**:P1 时跑「望舒解释器 / gopher-lua / 官方」三方,P3 加 `WangshuGibbous`,P4 加 **`WangshuGibbousJIT`**——同一比对框架,加 runner 不改 harness(承 §3.1)。
- **§7 P4 行预留**:P1 12 §7 表的「P4 method JIT」行已预留——「deopt 正确性:IC 投机失败时 OSR exit 回解释器,exit 后状态必须与『一路解释』一致」——本文 §4 把这条 P4 行的承诺翻成具体 V19 验收口径。
- **§8 CI 门禁**:P1 12 §8 把「层间逐字节差分是 CI 必过门禁」字面化([../architecture.md](../architecture.md) §4 不变式 2),本文 §6 把 amd64 与 arm64 双跑追加进 CI 矩阵。
- **§10 验收口径总表 26 条**:P1 12 §10 总表对 P4 build 原样适用——任何「为 P4 加新豁免」提案要回到 P1 12 重新论证(承 §0.4)。

**关键观察**:P1 时把 harness 建对(Runner 抽象 + 三方差分 + CI 硬门禁)是「为 P3+ 投机错误主防线提前铺轨道」(P1 12 §3.8 末)。**P4 是这条轨道的最重要用户**——P3 的主防线是「翻译正确性」(确定性 bug),P4 的主防线是「投机正确性 + deopt 着陆点严格性」(条件性 bug),后者是 P1 当年立项「为什么差分 fuzz 必须是 CI 硬门禁」的真正靶标。P4 测试套交付即兑现 P1 当年的前瞻性承诺。

### 0.4 章节路标

| 章节 | 主题 | 关键产物 |
|---|---|---|
| §1 | 验收口径(luajc 档锚点)| 列内核负载 ≥ 164μs / 第二检查 / 量化口径汇总 |
| §2 | P4 验收口径总表 V1-V22 | V1-V18 接续 + V19-V22 增项 + PJ 映射 |
| §3 | 同 Proto 差分(主防线)| Runner + force-all + 持续 fuzz + CI 硬门禁 |
| §4 | OSR 状态等价(V19,P4 独有)| 每 guard 强制失败 + 续跑 byte-equal + 物化序列端到端校验 |
| §5 | deopt 注入(主动测每条 exit)| V8 `--deopt-every-n` 一样的 / 风暴防抖 |
| §6 | 双架构双跑(V21)| amd64 + arm64 物理 runner / CI 资源 / arm64 滞后应急 |
| §7 | GC 压力 fuzz 延伸 | P1/P3 套 + 物化期 GC + 双架构差分 |
| §8 | 性能基准(luajc 档实测)| 列内核 + 多核形式分桶 + 跨机器对照 + profile vs 立项 |
| §9 | 工程轴 V17-V18 | 多 build tag + race + 双架构 CI 资源 + nightly |
| §10 | P3 → P4 验收套迁移(若 P3 退役)| Runner 表更替 / V1-V18 接续 / force-all-jit |
| §11 | 风险与开放问题 | 投机错果 + guard 天花板 + Go 调度 + icache + arm64 + 资源 + luajc 代表性 |
| §12 | 不变式清单 | 投机错果 / 双架构 / luajc 档 / OSR 等价 |
| §13 | 回填请求 | 对 P1 12 / P2 / P3 现稿的字段补登记 |

---

## 1. 验收口径(luajc 档锚点)

### 1.1 量化锚点:列内核负载 ≥ 164μs(校准测量 1)

承 [../roadmap.md](../roadmap.md) §1 校准测量 1(Horner 5 次多项式循环,1000 items,同机同日 A/B,16 核 Intel Xeon 6982P-C):

| 嵌入栈 | 绝对值 | 技术 |
|---|---|---|
| gopher-lua(Go) | 729μs | 纯解释,interface 装箱,switch dispatch |
| LuaJ-luac(Java) | 259μs | JVM 解释器,本体被 C2 编译热 |
| **LuaJ-luajc(Java)** | **164μs** | Lua→JVM bytecode,C2 全套优化(P4 验收锚点)|
| LuaJIT(C++) | 154μs | trace JIT,NaN-boxing |

**P4 验收 = 列内核负载达到 164μs 那一档的水位**(同等工作量基准上 ≥ 该档,即 ≥ 4.4x over gopher-lua、≥ 2x over P3 loop 核 2.95x baseline)。这是 [./00-overview.md](./00-overview.md) §0.3 / 本文 §7.1 字面承诺。

**为什么是 luajc 档而非 LuaJIT**:LuaJIT 仅比 luajc 快 6%(154 vs 164μs)——在 per-item 跨界形式下边界跨越主导成本,脚本本体再快也被钉死(前提一)。**达到 luajc 档即「逼近 LuaJIT 档」**(项目近期目标,[../roadmap.md](../roadmap.md) §0)。luajc 与 P4 同属「纯字节码/机器码 + 显式检查」家族(luajc 是 Lua→JVM bytecode + C2 全套优化,P4 是模板编译 + IC 投机 + 显式 guard),无信号陷阱可用——**6% 那档差距正是 P4 显式 guard 的成本天花板**(承 [./03-speculation-ic.md](./03-speculation-ic.md) §3.2 / §10.1)。

### 1.2 LuaJIT vs luajc 仅 6%——「逼近 LuaJIT 档」的近期含义在 P4 兑现

承 [../../../llmdoc/must/design-premises.md](../../../llmdoc/must/design-premises.md) 前提一第二段+前提三原则 5(第一天 NaN-box 承诺):

> 真 LuaJIT 只比 luajc 快 6%(154 vs 164μs)。在 per-item 跨界形式下,**边界跨越 + 值装箱主导成本**,脚本本体再快也被钉死。这是「纯 Go 不必复刻 trace JIT 也能逼近顶档」的核心论据。

P4 兑现这条核心论据的方式:

- **dispatch 税消除**:per-opcode 模板 + 直线机器码,消解释器取指/译码/dispatch 的恒定税(承 [./02-template-direction.md](./02-template-direction.md) §1.1);
- **f64 直算**:NaN-box u64 直接 `movq xmm` + `addsd`,无装箱解箱(承 [./03-speculation-ic.md](./03-speculation-ic.md) §2.2 + §3.4);
- **直达槽 IC**:表/全局/SELF 的命中走「同表+同代次 guard + 编译期立即数 offset」直达槽 load(承 [./03-speculation-ic.md](./03-speculation-ic.md) §6);
- **Go runtime 四项税自付**:trampoline 切自管栈 + 回边检查点 + 自管 arena 值世界——边界成本自付而不假手 wazero(承 [./05-system-pipeline.md](./05-system-pipeline.md) 全节)。

**P4 = dispatch 消除器 + IC 投机注入器**(承 [./00-overview.md](./00-overview.md) §0.1),做到 luajc 档即兑现「纯 Go 逼近 LuaJIT」承诺。这是项目立项核心论据的物理验收时刻。

### 1.3 与 P5「~30% 剩余空间」的坐标系

承本文 §1.1 + [../../../llmdoc/architecture/evolution-roadmap.md](../../../llmdoc/architecture/evolution-roadmap.md)(流水线图坐标系):

```
P1 解释器 ──► P2 桥 ──► P3 Wasm ──► P4 method JIT ──► P5 trace JIT
(2-4x)        (基建)    (4-8x)       (trace 收益 ~70%)   (10-30x,开放式)
```

「P4 拿到 trace JIT 理论收益的约 70%,剩余 ~30% 是 P5 的理论边际」——**这是另一个坐标系,不与 luajc 档混用**:

| 数字 | 基线 | 含义 | 出处 |
|---|---|---|---|
| **luajc 档(P4 验收锚点)** | gopher-lua(基线 1x)| Horner 列内核绝对水位 164μs(≈4.4x)| 校准测量 1 / 本节 §1.1 |
| **「~70% trace 收益」** | trace JIT 理论上限(LuaJIT 顶档)| P4 vs P5 各自相对 trace 上限的占比 | 流水线图 |
| **「P5 ~30% 剩余空间」** | 同上 | P5 相对 P4 的边际 | 同上 |

**坐标系警告**(承 [../p3-wasm-tier/08-testing-strategy.md](../p3-wasm-tier/08-testing-strategy.md) §5.2 一样的):**任何性能讨论引用倍率时必须标注基线**——「P4 达 luajc 档」与「P4 拿 70% trace 收益」是两个独立陈述,不能互推。本文 §8 性能基准全部以 luajc 档为锚,不涉及「70%」坐标系(P5 文档自己说)。

### 1.4 基准必须是列内核形状(承 12 §6.1 硬约束)

承 [../p1-interpreter/12-testing-difftest.md](../p1-interpreter/12-testing-difftest.md) §6.1(基准列内核形状硬约束)+ [../../../llmdoc/must/design-premises.md](../../../llmdoc/must/design-premises.md) 前提一(校准测量 2 端到端 ±5-7% 噪声):

> 基准必须用列内核形状:**循环写在 Lua 内,一次调用进一次 VM,整批数据在 VM 内迭代**。per-item 跨界形式下边界成本主导,测不出 P4(前提一/前提二)。

**列内核形状的具体要求**(对 P4 与 P3 同样适用):

- **一次 CallFn 进 VM**:Go 侧调一次 `program.Call(state, arena, args)`,而非 Go 侧 for 循环反复调进 VM。
- **整批数据在 VM 内迭代**:Lua 脚本顶层是 `for i=1, 1000 do … end` 类循环,每次迭代处理一个 item。
- **kernel 必须能升层**:含可升层的内层函数(F1-F7 可编译,且非顶层 vararg chunk),不是顶层 chunk 直接写循环——这是 PW9 空测陷阱教训(承 [../../../llmdoc/memory/reflections/2026-06-15-p3-pw9-acceptance-perf-round.md](../../../llmdoc/memory/reflections/2026-06-15-p3-pw9-acceptance-perf-round.md)),P4 第一天就要避开。

### 1.5 per-item 形状下边界成本主导,测不出 P4

承 [../../../llmdoc/must/design-premises.md](../../../llmdoc/must/design-premises.md) 前提一校准测量 2:

> 某生产规则引擎,启用 luajc:隔离脚本级 -37%(脚本本身明显加速);宿主端到端 benchmark:**前后对照全部落在 ±5-7% 噪声带内**(加速端到端不可见)。原因:绝大多数脚本是单行判断,**边界成本主导**,VM 层加速被稀释到看不见。

**P4 验收必须避开这个误区**:

| 反模式 | 后果 |
|---|---|
| Go 侧 for 循环 + 内层 `state.Call(stmt)` per-item 跨界 | P4 加速被 ~143ns/次跨层税(P3 实测,[../p3-wasm-tier/01-spike-gate.md](../p3-wasm-tier/01-spike-gate.md))稀释,164μs 测不出来 |
| 顶层 vararg chunk 直接写 for 循环 | F1 排除该 chunk 永不升层,实测 crescent vs crescent ≈1.0x(空测,承 §3.7)|
| 用 boundary mini 基准当 P4 验收锚 | 测的是边界成本,不是 P4 加速;P4 在 boundary mini 上理应与 P3 持平(都付边界税)|

**P4 验收唯一锚**:**列内核(自包含热循环)+ 已验证可升层的 kernel + Horner 校准对位** —— 三件齐才是合格的 P4 验收基准。

### 1.6 P4 内部第二检查(单架构 amd64 + 仅算术投机的最小 P4)中途校验

承 [./01-launch-judgment.md](./01-launch-judgment.md) §4.3 + [./06-backends.md](./06-backends.md) §6.3 末:

> 单架构(amd64)+ 仅算术投机的最小 P4 先打通全管线并测 Horner 档位,若距 luajc 档仍远(说明瓶颈不在 dispatch/投机),立即停下重评——这是「每阶段独立交付价值,任何检查停下不亏」原则在 P4 内部的套用。

**第二检查口径**(对应 [./06-backends.md](./06-backends.md) §6.1 PJ7「amd64 端到端验收 + 性能基准」):

| 检查项 | 验证内容 | 通过标准 |
|---|---|---|
| **机制全管线** | trampoline + 直线模板 + 算术投机 + OSR exit + GC 压力跑通 | V1-V5 + V19 + V20 全部 byte-equal,无 OSR 风暴 |
| **Horner 校准首测** | amd64 单架构跑 Horner 1000 items | **>= luajc 档 1.5x 余量(为后续 PJ + arm64 + 真实负载留余地)**,即 ≥ 246μs 反向 = 跑得更快(实测 ns/op 须低于 luajc 档对应值) |
| **若达不到 1.5x 余量** | 立即停下重评 | 瓶颈不在 dispatch/投机:可能是值表示边界(已被前提四锁死)/ Go 调度毛刺(承 §11.3)/ 投机模板生成质量(per-arch emitter 实现质量)|

第二检查的工程意义:**+1-2 人年的中途校验**——若 amd64 + 算术投机最小 P4 已跑到 luajc 档 1.5x 余量,后续 PJ8-PJ11(arm64 + 表 IC + CALL 跨层 + 真实负载)有合理空间;若已到 PJ7 距 luajc 档仍远,继续投人月只是把成本推高不解决根本瓶颈。

### 1.7 量化口径汇总表(P1/P2/P3/P4 各阶段验收)

承 [../p3-wasm-tier/08-testing-strategy.md](../p3-wasm-tier/08-testing-strategy.md) §0.2 四阶段对照表 + 本文新增 P4 行:

| 阶段 | 性能验收 | 正确性验收 | 主防线 |
|---|---|---|---|
| P1 | ≥2x over gopher-lua 三档 + benchmark game 五项 + boundary mini([12 §6](../p1-interpreter/12-testing-difftest.md))| 三方差分逐字节一致([12 §3](../p1-interpreter/12-testing-difftest.md))| **byte-equal 差分 fuzz**(防 codegen / IC / GC 透明性偏差)|
| P2 | **❌ 无**(基建定位)| 决策正确([../p2-bridge/06-testing-strategy](../p2-bridge/06-testing-strategy.md))| 可编译性零误判注入 fuzz(防安全检查失守)|
| P3 | 循环密集 ≥2x over P1(价值兑现门)| crescent vs gibbous 层间差分逐字节一致 | **层间 byte-equal 差分 fuzz**(crescent 与 gibbous 同输入同输出)|
| **P4(本文)** | **列内核 ≥ luajc 档(164μs)** | **投机正确性 + OSR 状态等价**(crescent vs gibbous-jit byte-equal + deopt 着陆点严格性)| **投机错果差分主防线 + deopt 注入 + 双架构双跑**(条件性 bug,差几个数量级难撞)|

**P4 与 P3 在主防线上的根本不同**(承 [../p3-wasm-tier/08-testing-strategy.md](../p3-wasm-tier/08-testing-strategy.md) §0.2 + §4.4):

- **P3 是确定性 bug**——某 opcode 翻译错了,任何走到它的输入都会暴露(强制全升 + 充分 fuzz 必撞)。
- **P4 是条件性 bug**——guard 漏了「x 可能不是 number」这个条件,只有当 x 真的不是 number 的罕见输入才暴露。**条件性 bug 比确定性 bug 难撞几个数量级**——这是 JIT「投机错误静默错果」之所以是「最危险 bug 类」(原则 2)的根源,也是本文 §5 deopt 注入与 §11.1 风险阶梯设计的来源。

---

## 2. P4 验收口径总表(P1 V1-V18 + P3 V1-V18 + P4 增项)

承 [../p3-wasm-tier/08-testing-strategy.md](../p3-wasm-tier/08-testing-strategy.md) §1 全 V 编号总表风格,P4 验收套是 P3 V1-V18 的接续 + P4 增项 V19-V22。**全表 22 条,每条配可执行检查路径**(本文核心铁律「不留无法验证的承诺」,承 P3 08 §0.3 + P1 12 §0)。

### 2.1 V1-V13(正确性轴,层间差分逐字节,承 P3 08 §1)

P3 08 §1.1 定的 V1-V13 是**正确性轴主防线**——crescent vs gibbous(P3 翻译)逐字节一致,覆盖 PW2 直线 opcode → PW8 协程不升层全部形状 + 强制全升 + GC 压力 fuzz。**P4 接续这 13 条**:

| # | 类目 | P3 视角(已交付)| **P4 视角(本文新增叠加)**|
|---|---|---|---|
| V1 | 直线 opcode | crescent vs gibbous(wasm)byte-equal | **+** crescent vs gibbous-jit byte-equal(直线模板正确,无投机)|
| V2 | 算术快路径 | f64 直发 + NaN 规范化 byte-equal | **+** P4 投机模板 IsNumber×2 guard + f64.add byte-equal(承 §3.4)|
| V3 | 算术慢路径 | 走 helper byte-equal | **+** P4 失败 OSR exit 后解释续跑 byte-equal(承 §4)|
| V4 | 数值 for | FORPREP/FORLOOP byte-equal | **+** P4 数值 for 投机模板 byte-equal(若做循环变量寄存器驻留,需 V19 物化序列)|
| V5 | 回边 GC | gcPending inline byte-equal | **+** P4 回边检查点(自管栈 + preemptFlag,承 [./05-system-pipeline.md](./05-system-pipeline.md) §1)+ GC 压力 byte-equal |
| V6 | 表 IC 命中 | 单态命中跳哈希 byte-equal | **+** P4 直达槽 + 同表+同代次 guard byte-equal(承 [./03-speculation-ic.md](./03-speculation-ic.md) §2.3 / §6)|
| V7 | 表 IC 失效 | gen bump 走 helper byte-equal | **+** P4 guard 失败 OSR exit + 解释续跑 byte-equal |
| V8 | 跨层 CALL 链 | 错误冒泡 byte-equal | **+** P4 jit→jit / jit→crescent / jit→host 三向互调 byte-equal(承 [./05-system-pipeline.md](./05-system-pipeline.md) §3 世界边界)|
| V9 | gibbous traceback | 帧 pc 物化 byte-equal | **+** P4 帧 pc 物化(经 OSR exit 写 savedPC,承 [./04-osr-deopt.md](./04-osr-deopt.md) §4)byte-equal |
| V10 | 闭包 upvalue | CLOSURE/CLOSE byte-equal | **+** P4 闭包模板 byte-equal(承 [./06-backends.md](./06-backends.md) §3.6)|
| V11 | 协程不升层 | 协程内 tier 恒 Interp/Stuck | **+** P4 协程内 P2 tierState 恒非 TierGibbous(承 [../p3-wasm-tier/07-coroutine-thread-rule.md](../p3-wasm-tier/07-coroutine-thread-rule.md) 一样的规则,P4 原样继承)|
| V12 | 强制全升 byte-equal | force-all 跑全脚本 byte-equal | **+** P4 force-all-jit byte-equal(承 §10.3)|
| V13 | GC 压力逼写回遗漏 | locals 缓存 A/B 对照 | **+** P4 寄存器→栈槽写回 + 物化期 GC 触发 A/B 对照(承 §7.4)|

**纪律**:V1-V13 在 P4 build 下**全部继续跑,不豁免**——`WangshuGibbous`(P3 wasm)与 `WangshuGibbousJIT`(P4)同时跑全 13 条,差任一条立即停 PR(承 §3.5)。

### 2.2 V14-V16(性能轴,P3 已设;P4 替换为 luajc 档)

P3 08 §1.2 定的性能轴 V14-V16(loop ≥2x over P1 / realworld geomean ≥1.5x / boundary 无退化)在 P4 维度替换/扩展:

| # | P3 口径(已交付)| **P4 口径(替换/新增)**|
|---|---|---|
| **V14** | 循环密集 ≥2x over P1 crescent | **替换为「列内核负载 ≥ luajc 档 164μs」**(承 §1.1)。同机同日 A/B,以同等 Horner 工作量基准对照,P4 ≥ 164μs 水位(实测 ns/op ≤ luajc 对应值)|
| **V15** | realworld 五脚本 geomean ≥1.5x | **保留 + 强化**:realworld 五脚本(fib/binarytrees/spectralnorm/nbody/fannkuch)P4 geomean ≥ P3 geomean ≥ 1.5x(P4 应在 P3 基础上再涨)|
| **V16** | 边界往返 ≥ spike S2 × 0.95 | **保留**:P4 edge boundary 往返 ≥ P3 实测 S2 × 0.95(P4 自管 trampoline 的边界成本不应比 P3 wazero 边界劣化超过 5%)|

**V14 替换的物理意义**(承 [./00-overview.md](./00-overview.md) §0.3 + 本文 §1.1):**P4 与 P3 在「>= 2x over P1」上无显著差**(P3 已 2.95x loop 核,P4 dispatch 消除是一样的收益)——P4 真正要兑现的是**绝对水位**(luajc 档 164μs),这是 P3 力所不及的(P3 受 wazero 中介 + Wasm 表达力限制,生成码质量上限低于 P4 直发原生)。

**V14/V15 的 P4 验收时序**(承 [./06-backends.md](./06-backends.md) §6.1 PJ7 / PJ11):

- **PJ7 amd64 端到端**:V14 单架构 amd64 ≥ luajc 档(本文 §1.6 第二检查)
- **PJ11 luajc 档全验收**:V14 amd64 + arm64 双架构 ≥ luajc 档 + V15 realworld ≥ P3 geomean(在双架构上)

### 2.3 V17-V18(工程轴,四 build + -race)

P3 08 §1.3 定的工程轴(三套 build 零回归 + -race)在 P4 维度扩展为**四 build + 双架构 -race**:

| # | P3 口径 | **P4 口径(扩展)**|
|---|---|---|
| V17 | 三套 build:default / wangshu_profile / +wangshu_p3 全绿 | **扩为四套 build**:+ `wangshu_p4` 第四套(承 §9.1)。`wangshu_p4` 依赖 `wangshu_profile` + `wangshu_p3` 共存;四 build 全绿 |
| V18 | -race 多 State 并发 gibbous 通过 | **扩为双架构 -race**:amd64 与 arm64 各跑 -race × `-count=10`(承 §6 + §9.2)|

### 2.4 P4 增项 V19+(待编号)

P4 引入投机带来的新验收口径,在 P3 V1-V18 之外新增:

| # | 类目 | 断言(P4 不变式)| 单测对应 | 自动化检查 |
|---|---|---|---|---|
| **V19** | OSR 状态等价(P4 独有)| **兑现方式(issue #66,2026-07-07 校准）**：本条原本设想验证「函数级 OSR 状态等价」，但函数级 OSR 物化从未实现（承 [./04-osr-deopt.md](./04-osr-deopt.md) 头部裁决），现在等价由 **deopt-redo 等价**兑现——① seg2seg deopt-redo 注入测试 `TestSeg2SegDeoptRedo_ArithGuardMiss` / `_NestedPropagation`（+ `SegToSegDeoptCount` 探针，实跑增长 3 / 4）验证「callee 段内 guard 失败 → 逐层传播 → 顶层重跑整个 top-level call 后结果不变」；② spec-template deopt 测试 `TestPJ5_SelfCall_E2E_SpecTemplate_OSRExitToDeopt`（+ `SpecP4DeoptHits` 探针，实跑增长 6）验证 spec 段 guard 失败 → 降级 host.Self 后仍 byte-equal | seg2seg：`TestSeg2SegDeoptRedo_ArithGuardMiss` / `_NestedPropagation`;spec-template：`TestPJ5_SelfCall_E2E_SpecTemplate_OSRExitToDeopt` | `SegToSegDeoptCount` / `SpecP4DeoptHits` 探针真实增长 + 重跑/降级后输出 byte-equal crescent |
| **V20** | deopt 风暴下不死锁 | 同 Proto 反复 deopt 触阈 → 进入 `P4StuckSpeculation`(P4 内吸收态;P2 tierState 仍 TierGibbous,承方案 A)防抖,后续不再投机但仍**正确**(走通用模板,与 crescent byte-equal)。**兑现方式(issue #66,2026-07-07 校准）**：由 spec-template deopt 风暴测试 `TestPJ5_SelfCall_E2E_SpecTemplate_DeoptStorm`（5 caller 独立累积 `SpecP4DeoptHits`，实跑增长 15）+ `internal/gibbous/jit/p4state_test.go` 7 个 P4 状态机单测（含 `TestP4SpecState_MaxRecompileTriesReachedStuck` 验 P4StuckSpeculation 吸收态）兑现 | `TestPJ5_SelfCall_E2E_SpecTemplate_DeoptStorm` + `p4state_test.go` 7 单测 | 进入 `P4StuckSpeculation` 后:① 后续输出仍 byte-equal crescent ② deopt 计数停止增长 ③ 不重试投机(承 [./04-osr-deopt.md](./04-osr-deopt.md) §5.3-§5.5)|
| **V21** | 双架构双跑(amd64 + arm64)| amd64 与 arm64 物理 runner 各跑全套 V1-V18 + V19 + V20,且 amd64 输出 == arm64 输出 == crescent 输出(三方 byte-equal)| `difftest/p4_test.go::TestDualArchByteEqual`(同 Proto 三 runner)+ CI 矩阵两架构 | crescent vs gibbous-jit/amd64 vs gibbous-jit/arm64 三方 byte-equal(承 §6.2)|
| **V22** | guard 漏判 fuzz(**当前实现形式**:错误存在性差分,承 §2.4 addendum)| **当前**:force-all P4 vs P1 差分 fuzz,验证「错误存在性」(errP1==nil ⇔ errP4==nil,预算/时机类分叉 Skip)+ 结果 byte-equal——若 guard 漏判导致 P4 静默错果,fuzz 会在差分先抓到。**归 followup**:「模板生成器每条 guard 强制不查一次」的原始 spec 变体(FuzzGuardOmission)未实现 | `fuzz_p4_test.go::FuzzP4ForceAllPromote`(build tag `wangshu_p4 && wangshu_profile`,5 corpus seed + 27 内嵌 f.Add seed,`-race` 下自动 Skip,见本 §)| 任一 seed 触发 error 存在性真分叉(非 budget 类)或 result 字面不 byte-equal ⇒ 硬 fail;spec 变体「per-guard 禁用」归 §2.4 addendum 与 09 §4 已登记 followup |

> **V22 spec 变体状态**(2026-07-02 与实现对账):原设计 `FuzzGuardOmission` + build tag `wangshu_p4_guardfuzz` + 「每条 guard 一次性禁用」的形式在 codebase 中**未实现**——完成的是 `FuzzP4ForceAllPromote`(承本表 V22 行 + fuzz_p4_test.go)。等价性论证:P4 与 P1 force-all 差分能抓「静默错果」这一 V22 的**核心断言**(guard 漏判 → P4 输出偏离 P1 → byte-equal 破 → 硬 fail);「per-guard 禁用」的额外深度作为 followup 保留(承 09 §4),不阻塞 PJ11。

**V19-V22 的核心价值**:

- V19 直接验「deopt-redo 等价无损」——原设想的函数级 OSR 物化已由 #50 deopt-redo 取代（承 [./04-osr-deopt.md](./04-osr-deopt.md) 头部裁决），现在验的是 seg2seg deopt-redo 重跑 + spec-template 降级后结果不变;
- V20 直接验「deopt 风暴防抖收敛」——`P4StuckSpeculation` 吸收态可达;
- V21 把双架构提升为 CI 硬门禁,与单架构差分独立验;
- V22 把 guard 必要性变成可验证 —— 是 [../../../llmdoc/guides/prove-the-path-under-test.md](../../../llmdoc/guides/prove-the-path-under-test.md) 「毒化哨兵」反向侧解药在 P4 投机维度的兑现。

### 2.5 各 V 对应的 PJ 里程碑映射(承 06-backends §6.1)

承 [./06-backends.md](./06-backends.md) §6.1 PJ0-PJ11 + §6.2 V-J 映射(本文具体化为 V1-V22 在 PJ 维度的对账):

| PJ | 名称 | 本文 V 口径(累积验收)|
|---|---|---|
| PJ0 | 架构选定 + 包骨架 | 编译通过(P4 build tag 下)+ V17 四 build 启动验证 |
| PJ1 | amd64 trampoline + 直线模板 | V1(直线 opcode byte-equal)|
| PJ2 | amd64 算术 + 比较模板 | V2(算术快路径)+ V3(算术慢路径)+ V19 启动(每 guard 强制失败)|
| PJ3 | amd64 控制流 + FORLOOP + 回边 safepoint | V4(数值 for)+ V5(回边 GC)+ V13(GC 压力)|
| PJ4 | amd64 表 IC 模板(投机版)| V6(表 IC 命中)+ V7(表 IC 失效)+ V19 扩(表 IC guard 失败)|
| PJ5 | amd64 CALL/TAILCALL + 跨层互调 + OSR exit | V8(跨层 CALL)+ V9(traceback)+ V19 扩(CALL 路径 OSR exit)+ V20(deopt 风暴防抖启动)|
| PJ6 | amd64 CLOSURE/CLOSE + upvalue | V10(闭包 upvalue)|
| **PJ7** | amd64 端到端验收 + 性能基准 | **V11**(协程不升层)+ **V12**(force-all-jit)+ **V14**(amd64 单架构 ≥ luajc 档,§1.6 第二检查)+ **V18**(单架构 -race)+ **V22**(guard 漏判 fuzz 启动)|
| PJ8 | arm64 后端启动 + 同框架渐进 | arm64 单架构 V1-V11 对位 amd64(各 PJ 重跑 V 验收)|
| PJ9 | arm64 端到端验收 + 双架构差分套 | **V21**(双架构双跑 byte-equal)+ V18 arm64 -race + **V17 四 build × 双架构** |
| **PJ11** | luajc 档验收 | **V14 双架构** + **V15**(realworld 双架构 geomean)+ **V16**(boundary 无退化)+ **V20 全验**(双架构 deopt 风暴)+ **V22 全验**(双架构 guard 漏判 fuzz nightly ≥30 天无差异)|

**关键纪律**:**每 PJ 验收都是累积**——PJ4 验收时 V1-V5 + V19 都要在 PJ4 build 下重跑通过(不只是 PJ4 自己的 V6/V7 通过)。这与 P3 PW 验收一样的递归([../p3-wasm-tier/08-testing-strategy.md](../p3-wasm-tier/08-testing-strategy.md) §6.2.1):「翻译 bug 在引入它的 PJ 就被抓住,不堆到端到端阶段才暴露一堆混在一起的 bug」。

---

## 3. 同 Proto 差分(主防线,V1-V13 P4 接续)

承本文 §7.2 主防线五机制 + [../p3-wasm-tier/08-testing-strategy.md](../p3-wasm-tier/08-testing-strategy.md) §2 全节(crescent vs gibbous 逐字节差分)+ [../p1-interpreter/12-testing-difftest.md](../p1-interpreter/12-testing-difftest.md) §3.8 Runner 抽象。**P3 已交付的差分轨道是 P4 接续的物理底座**(承 §0.2 / §3.6),本节定 P4 在该轨道上的具体落点。

### 3.1 [12 §3.8] Runner 抽象新增 WangshuGibbousJIT

[../p1-interpreter/12-testing-difftest.md](../p1-interpreter/12-testing-difftest.md) §3.8 已建抽象,P3 已加 `WangshuGibbous`,P4 加 `WangshuGibbousJIT`:

```go
// test/difftest/runner.go —— P1 已建抽象,P3 已加,P4 接续
type Runner interface { Name() string; Run(src string, arena []byte) (capture, error) }
type WangshuInterp   struct{}                         // P1:被测 internal/crescent
type GopherLua       struct{}                         // P1:基准 gopher-lua
type OfficialLua     struct{}                         // P1:oracle 官方 5.1.5
type WangshuGibbous  struct{ forceAllPromote bool }   // P3:同 Proto 走 gibbous Wasm 层
type WangshuGibbousJIT struct{                         // ★ P4(本文完成)
    forceAllPromote  bool   // 强制全升模式(§3.7)
    deoptInjectMode  bool   // deopt 注入模式(§5.2)
    deoptInjectEvery int    // 每 N 次 guard 失败一次(默认 0=禁用)
}
```

**P4 与 P3 runner 的关键差异**:

- **deopt 注入开关**:`deoptInjectEvery` 是 P4 独有(P3 无 deopt,无注入概念,承 §5.2);
- **强制全升模式**:`forceAllPromote` 与 P3 一样的语义,**P4 还多一层意义**——投机模板的 confidence 已被 P2 阈值滤过,但 force-all 把覆盖率拉满后,**deopt 路径的覆盖也跟着拉满**(承 [../p3-wasm-tier/08-testing-strategy.md](../p3-wasm-tier/08-testing-strategy.md) §2.3 隐性红利的 P4 对偶兑现);
- **多 runner 并跑**:P4 build 下,差分套同时跑 `WangshuInterp` / `WangshuGibbous`(P3)/ `WangshuGibbousJIT`(P4)三个望舒 runner——三方互校,**P3 与 P4 翻译的同 Proto 在同输入下输出 byte-equal**(双 backend 差分,把 P3 当 oracle 验 P4,把 P4 当 oracle 验 P3,互为对照,承 [./06-backends.md](./06-backends.md) §5.2 末同源)。

### 3.2 同一 Proto 走 crescent vs gibbous-jit byte-equal

承 [../p3-wasm-tier/08-testing-strategy.md](../p3-wasm-tier/08-testing-strategy.md) §2.1.1 二方判定矩阵在 P4 维度的复刻:

| crescent(oracle) vs gibbous-jit(被测)| 结论 |
|---|---|
| == | **通过**(P4 投机正确)|
| ≠ | **gibbous-jit bug**(crescent 是 P1 已验证 oracle,gibbous-jit 孤立错——确定性信号,无实现噪声)|

**关键性质**(承 [../p3-wasm-tier/08-testing-strategy.md](../p3-wasm-tier/08-testing-strategy.md) §2.1):**P4 与 crescent 共享同一份 Proto、同一份 arena 值世界、同一套 NaN-box 编码**——任何输出差异必是 P4 后端 bug,不存在「实现本来就不同」的噪声。这与 P3 一样的,**比 P1 三方差分判据更锐利**。

**P4 视角下「bug 类」拆分**:

| bug 类 | 触发性 | 差分如何抓 |
|---|---|---|
| 翻译 bug(直线模板发射错)| 确定性 | 任何走到该模板的输入必撞,V1-V11 形状级单测兜底 |
| 投机 bug(guard 漏判 / 多判)| **条件性**(只在投机假设不成立的罕见输入触发)| **持续 fuzz 撒网 + deopt 注入主动踩热**(承 §5)|
| 物化 bug(寄存器→栈槽写回遗漏)| 物化点稀疏出现 | V19 强制每 guard 失败 + GC 压力 fuzz(承 §7.4)|
| 双架构 bug(amd64 vs arm64 emitter 不一致)| 单架构跑必失明 | V21 双架构双跑(承 §6)|

### 3.3 输出 O1..O5(05-interpreter-loop 既有口径)逐项 byte-equal

承 [../p1-interpreter/12-testing-difftest.md](../p1-interpreter/12-testing-difftest.md) §3.1 五项可观察输出在 P4 层间差分的特化(承 [../p3-wasm-tier/08-testing-strategy.md](../p3-wasm-tier/08-testing-strategy.md) §2.1.2):

| 可观察项 | P4 层间特有关注 | 对应 V 口径 |
|---|---|---|
| O1 print/io.write 字节流 | P4 的 print 经 helper 经 trampoline 出 JIT 世界回 Go 调同一份 stdout sink——字节流必逐字节同 crescent | V1-V13 通用 |
| O2 顶层返回值 | P4 RETURN 经 trampoline 弹帧 + nresults 处理(承 [./05-system-pipeline.md](./05-system-pipeline.md) §4 调用协议)——返回值序列必同 crescent | V8(CALL/RETURN)|
| O3 错误信息(值 + 位置 + traceback)| **P4 帧 pc 物化**——错误位置 `chunk:line:` 由 OSR exit 写 savedPC 后 crescent 接管时取(承 [./04-osr-deopt.md](./04-osr-deopt.md) §4 + §7),必同 crescent | **V9**(traceback 主靶)|
| O4 副作用顺序 | P4 直线模板的副作用(print/表写)顺序必同 crescent 解释序——**模板编译不重排可观察副作用**(承 [./02-template-direction.md](./02-template-direction.md) §1.1) | V1-V13 通用 |
| O5 退出状态 | P4 错误冒泡到顶层的退出态必同 crescent(status=1 ERR 或正常返回 status=0;**status=2 DEOPT 是 P4 独有但只在中间出现,不冒泡到顶层**——承 [./04-osr-deopt.md](./04-osr-deopt.md) §0.2)| V8 |

**O4 副作用顺序在 P4 维度的特别关注**:模板编译比 Wasm 翻译更接近裸机,理论上 per-arch emitter 可能为优化重排指令(指令调度)。但 **P4 基线不做任何重排可观察副作用的优化**(承 [./02-template-direction.md](./02-template-direction.md) §4 边界表「不做循环不变量外提 / CSE」)——副作用顺序与字节码顺序严格一致。**若未来引入指令调度**(P5 IR 优化器领地),O4 是第一道防线。

### 3.4 持续 fuzz(survives nightly)

承 [../p1-interpreter/12-testing-difftest.md](../p1-interpreter/12-testing-difftest.md) §11(PR 门禁固定时长 + nightly 长跑)+ [../p3-wasm-tier/08-testing-strategy.md](../p3-wasm-tier/08-testing-strategy.md) §2.5 / §4.2(已扩 gibbous 轴)在 P4 维度的具体接入(真实现见 [`.github/workflows/nightly-diff-fuzz.yml`](../../../.github/workflows/nightly-diff-fuzz.yml),2026-07-01 起 matrix 覆盖 p1/p3/p4 三 variant):

```yaml
# .github/workflows/nightly-diff-fuzz.yml —— P4 变体挂钩后当前形式
jobs:
  diff-fuzz:
    strategy:
      fail-fast: false
      matrix:
        include:
          - variant: p1
            tags: ''
          - variant: p3
            tags: 'wangshu_p3 wangshu_profile'
          - variant: p4                                   # ★ 2026-07-01 挂钩
            tags: 'wangshu_p4 wangshu_profile'
    runs-on: ubuntu-latest
    steps:
      # 1. rolling-seed differential fuzz(每晚 200 万脚本 vs lua5.1.5 oracle)
      - run: WANGSHU_FUZZ_SEED_BASE=$(($(date -u +%s)/86400*10000000)) \
             WANGSHU_FUZZ_N=2000000 \
             go test -tags "${{ matrix.tags }}" ./test/difftest/ \
               -run TestDiff_RandomScripts -timeout 150m
      # 2. GC-stress transparency fuzz(20 万脚本双模式对照)
      - run: WANGSHU_GCSTRESS_N=200000 \
             go test -tags "${{ matrix.tags }}" ./test/difftest/ \
               -run TestGCStress -timeout 60m
      # 3. go-fuzz native long run(各 fuzz target 45m)
      - run: ./scripts/go-fuzz.sh 45m "${{ matrix.tags }}"
```

**空间 × 时间协同覆盖**:PR 门禁 [`.github/workflows/ci.yml`](../../../.github/workflows/ci.yml) 用 tri-platform matrix(ubuntu-latest amd64 + ubuntu-24.04-arm 原生 GHA runner + macos-latest M1)× P1/P3/P4 三 variant × test/fuzz-smoke/conformance/difftest 四 job 覆盖**空间维度**(3 平台 × 3 build);nightly-diff-fuzz.yml 用 ubuntu-latest amd64 单平台 × P1/P3/P4 三 variant × rolling-seed diff + GC-stress + go-fuzz 三项覆盖**时间维度**(每晚积累)。V21 双架构 byte-equal 已由 tri-platform ci.yml 覆盖(承 §6),nightly 单平台累积用来抓 30 天时间维度的 rare divergence,不重复跑三平台以省 CI 资源。crash 与 mismatch 报告分别 triage(nightly-diff-fuzz.yml 已内置 `DIVERGENCE seed=X kind=Y` 结构化标记 + go-fuzz crash 分流 + infra failure 分流,自动开 issue)。

**P4 fuzz 与 `-race` 的物理约束**(承 fuzz_p4_test.go 与 `race_on_test.go` / `race_off_test.go` 中的 `raceEnabled` 常量,以及反思 [[2026-07-01-p4-pj10-native-round]] lesson 1):P4 mmap 段 shim 调 Go helper 的路径与 Go race detector 的 stack unwinder(mmap+morestack)物理不兼容,**`FuzzP4ForceAllPromote` 在 `-race` 构建下自动 `t.Skip`**。这意味着:

- **`-race` CI job**(V18,承 §9.2)只覆盖 P4 的**非 mmap 子集**——mmap 段真机 execute 的正确性依赖 non-race 差分 / difftest / conformance 三条独立防线;
- **fuzz 的 30 天累积覆盖仅在 non-race 走**——V22 spec 的核心断言(guard 漏判 → byte-equal 破)由 non-race fuzz 累积承担,`-race` 不重复;
- **原描述「`-race` + fuzz 30s」**(旧稿 §3.5 表 PR check 行)应理解为:`-race` job 与 fuzz-smoke job **并列覆盖**,而非「一个 job 内既开 `-race` 也跑 P4 fuzz」——后者会被 `raceEnabled` 常量 skip,达不到覆盖。

### 3.5 CI 硬门禁(architecture §4 不变式 2)

承 [../architecture.md](../architecture.md) §4 不变式 2(层间逐字节差分 CI 必过)+ [../p3-wasm-tier/08-testing-strategy.md](../p3-wasm-tier/08-testing-strategy.md) §4.1。P4 上线后 CI 硬门禁:

| 阶段 | 跑什么 | 时间预算 | build tag |
|---|---|---|---|
| **PR check** | V1-V11 形状级单测 + V12 强制全升 + V19-V20 OSR/deopt 风暴 + V18 -race(非 mmap 子集,`FuzzP4ForceAllPromote` 在 `-race` 下自动 skip,承 §3.4)+ 翻译器/emitter 内测 + 层间差分**短** fuzz(`-fuzztime=30s`,non-race build)| < 8 min | `wangshu_profile wangshu_p3 wangshu_p4` |
| **PR check(P1/P2/P3 不豁免)**| P1 三方差分一轮 + P2 V1-V22 + P3 V1-V18(承 §0.2)| < 5 min | 同上 |
| **nightly** | 层间差分**长** fuzz(2h)+ guard 漏判 fuzz + deopt 注入 fuzz + GC 压力上 gibbous-jit(`-count=20`)+ longevity + 性能基准全档(V14-V16)| 数小时 | 同上,**双架构** |
| **release 前** | 全套 + benchmark 矩阵双架构实测产出 + V20 长跑(deopt 风暴防抖收敛验证 30+ 天等价物)| 不限 | 四套 build × 双架构 全跑 |

**关键纪律**(承 [../p3-wasm-tier/08-testing-strategy.md](../p3-wasm-tier/08-testing-strategy.md) §4.2.1):

- **层间 mismatch = 阻断级 bug**:gibbous-jit vs crescent 任一脚本输出不同,PR 不合入(承 §3.5);层间 mismatch 单列一档 triage(必是 P4 后端 bug,无实现噪声)。
- **任何为 P4 加新豁免的提案要回到 P1 12 §4 重新论证**——P4 与 crescent 共享同一份 arena / NaN-box / globals,**不引入任何私有不确定性源**,P1 §4 豁免表对 P4 原样适用。

### 3.6 P3 已交付的差分轨道作为 P4 模板:V1-V13 各形状测试套结构 P4 直接复用

承 [../p3-wasm-tier/08-testing-strategy.md](../p3-wasm-tier/08-testing-strategy.md) §2.6 + §6.2(test/difftest/p3_test.go + test/conformance/p3_test.go)。**P4 直接复用 P3 测试套结构**:

```go
//go:build wangshu_p4
// test/difftest/p4_test.go —— crescent vs gibbous-jit 逐字节差分主体(V1-V13 P4 接续 + V19-V22)

// TestForceAllPromoteByteEqualP4 —— V12 P4 接续:force-all-jit 下,差分套全脚本 byte-equal
func TestForceAllPromoteByteEqualP4(t *testing.T) {
    for _, sc := range loadAllDifftestScripts() {       // 复用 P3 已建语料
        t.Run(sc.Name, func(t *testing.T) {
            outC  := normalize(runCrescentOnly(sc.Source), sc.normOpts)         // oracle
            outP4 := normalize(runForceAllGibbousJIT(sc.Source), sc.normOpts)   // 被测
            if !bytes.Equal(outC, outP4) { t.Fatalf("P4 LAYER mismatch in %s", sc.Name) }
        })
    }
}

// FuzzCrescentVsGibbousJIT —— P4 层间差分 fuzz(承 §3.4)
func FuzzCrescentVsGibbousJIT(f *testing.F) {
    f.Fuzz(func(t *testing.T, seed uint32) {
        src := genCompilableScript(seed)
        if !isValidP1(src) { return }
        outC  := normalize(runCrescentOnly([]byte(src)), defaultNorm)
        outP4 := normalize(runForceAllGibbousJIT([]byte(src)), defaultNorm)
        if !bytes.Equal(outC, outP4) { t.Errorf("[P4 LAYER] %s", src) }
    })
}
```

**复用度**:test/difftest/runner.go 加 `WangshuGibbousJIT`(§3.1)+ test/conformance/p4_test.go 仿 P3 conformance 形状 + benchmarks/realworld 加 jit 档对照——**结构 P4 直接复用,不另起 harness**。

### 3.7 force-all 强制全升模式(非空保证,承 P3 08 §2.2)

承 [../p3-wasm-tier/08-testing-strategy.md](../p3-wasm-tier/08-testing-strategy.md) §2.2 + §2.3 + [../../../llmdoc/guides/prove-the-path-under-test.md](../../../llmdoc/guides/prove-the-path-under-test.md):

**为什么 P4 也必须强制全升**(同 P3 一样的理由):正常运行下 P2 决策机按热度阈值决定升层时机,引入时序不确定性——同一脚本跑两次因调度/GC 时机不同被编译的 Proto 集合可能不同;差分若依赖「自然热度」会出现「这次升那次没升」不可复现。**force-all-jit** 绕过热度判定,所有 `CompCompilable` Proto 在首次调用前直接编译,保证可复现 + 覆盖最大化。

**测试入口**(承 §10.3 + §13 RB-2,P4 完成时实现,testing-only):

```go
// internal/bridge —— 强制全升模式 P4 扩展(testing-only)
// **不绕可编译性检查**(承 P3 08 §2.3.1):F1-F7 排除形状即便 force-all-jit 也走 crescent
func SetForceAllPromoteJIT(state *State, on bool)
```

**非空保证**(承 [../p3-wasm-tier/implementation-progress.md](../p3-wasm-tier/implementation-progress.md) §11.2 RW-10 教训 + [../../../llmdoc/guides/prove-the-path-under-test.md](../../../llmdoc/guides/prove-the-path-under-test.md) §2(b)正向 tier 断言):**P4 force-all 后必须白盒断言「真有 Proto 到达 P4 投机版 GibbousCode 安装位」**——方案 A 下 P2 `tierState` 仍 `TierGibbous`,P4 端额外断言 `p4SpecState[proto] == P4Speculative`(P4 实现内部读),锁死「force-all 没升任何 Proto → crescent==gibbous-jit 退化为 crescent==crescent」假阳性:

```go
// V12 邻接非空保证(承 P3 08 §2.3.1 + RW-10):kernel 包内层函数避开 PW9 顶层 vararg 空测陷阱
func TestForceAllPromoteRealP4(t *testing.T) {
    state := wangshu.NewState(nil)
    bridge.SetForceAllPromoteJIT(state, true)
    src := `local function kernel(n) local s=0; for i=1,n do s=s+i*i end; return s end; return kernel(1000)`
    prog, _ := wangshu.Compile([]byte(src), "p4")
    _ = prog.Call(state, nil)
    pd := bridge.ProfileDataOf(state, prog.HotProto())
    // ① P2 三态断言:TierGibbous(承方案 A,P4 不写 P2 枚举)
    if pd.TierState != bridge.TierGibbous {
        t.Fatalf("force-all-jit failed to promote: P2 state=%v(空测陷阱守卫)", pd.TierState)
    }
    // ② P4 内部子状态断言:P4Speculative(P4 实现内 internal API)
    if jit.SpecStateOf(prog.HotProto()) != jit.P4Speculative {
        t.Fatalf("force-all-jit promoted but P4 substate=%v(投机版 GibbousCode 未装)", jit.SpecStateOf(prog.HotProto()))
    }
}
```

**这是 [../../../llmdoc/guides/prove-the-path-under-test.md](../../../llmdoc/guides/prove-the-path-under-test.md) §2(b)正向 tier 断言在 P4 维度的兑现**——P3 已是该家族第 5+ 实例(PW9 vararg 空测 / R3 错误路径 / PW10 ③b 命中盲区 等),P4 第一天起就把这条纪律内建进 force-all 测试结构。

---

## 4. OSR 状态等价专项(V19,P4 独有)

### 4.1 [12 §7] P4 行预留:guard 失败 exit 后的最终输出与「同输入一路解释」byte-equal

承 [../p1-interpreter/12-testing-difftest.md](../p1-interpreter/12-testing-difftest.md) §7 P4 行预留:

> **P4 method JIT** 的「deopt 正确性」:IC 投机失败时 OSR exit 回解释器,exit 后状态必须与「一路解释」一致。**「投机路径 vs 解释器」逐字节差分是唯一能抓住它的手段**。

V19 把这条 P4 行的承诺翻成具体口径:

| V19 子项 | 断言 | 测试构造 |
|---|---|---|
| **每 guard 强制失败一次** | 每个 P4 投机点的 guard 强制失败一次,该帧剩余执行交还 crescent,最终输出与「全程 crescent 解释」byte-equal | `conformance/p4_test.go::TestEveryGuardForceFailOnce`(table-driven 每个投机模板形状)|
| **物化序列无损**(承 [./04-osr-deopt.md](./04-osr-deopt.md) §3.7)| OSR exit 物化序列写回栈槽后,栈槽内容 == 解释器在该 pc 的栈槽内容(per-slot 语义等价)| 白盒探针:在物化点埋断点,dump 栈槽 vs crescent 同 pc dump 比对 |
| **着陆面 reloadFrame 正确**(承 [./04-osr-deopt.md](./04-osr-deopt.md) §1.1 + [../p1-interpreter/05-interpreter-loop.md](../p1-interpreter/05-interpreter-loop.md) §1.3)| crescent 接管后 reloadFrame 从 exitPC 续跑,fetch-decode-execute 链与「全程解释」从同 pc 起跑等价 | 黄金对比:exit 后 N 条 opcode 解释序的 trace,与「全程解释」从 exit pc 起的 N 条 trace byte-equal |

### 4.2 验证物化(04-osr-deopt §3.3)与着陆(04 §1.1)无损

承 [./04-osr-deopt.md](./04-osr-deopt.md) §3.3「OSR exit 退化为三步」+ §1.1「Exit 的单位 = 当前函数帧」:

```
OSR exit 三步(承 04 §3.3):
  1. 寄存器→栈槽写回(若有局部缓存,§3.6 编译期静态烧入)
  2. 写 exitPC 到 CallInfo.savedPC(机器地址→字节码 pc 映射)
  3. 经 trampoline 出 JIT 世界,crescent reloadFrame 续跑同帧

V19 端到端校验:
  - 步 1 无损 = 物化 store 序列覆盖该 exit 点全部活跃 Lua 值
  - 步 2 无损 = exitPC 立即数与编译期记录的 (机器地址 → 字节码 pc) 对一致
  - 步 3 无损 = trampoline 不丢/不改任何寄存器(jitContext 入参 + Go 帧 callee-saved)
```

V19 不验各步实现细节(那是 [./04-osr-deopt.md](./04-osr-deopt.md) §3-§6 的事),只验**端到端等价**——「输出 byte-equal」是「三步全无损」的充要条件(差分是无歧义的统一法庭)。

### 4.3 测试构造:每 guard 强制失败一次(deopt 注入模式) → 解释续跑 → 与全程解释结果 byte-equal

承 [../../../llmdoc/guides/prove-the-path-under-test.md](../../../llmdoc/guides/prove-the-path-under-test.md) §2(a)毒化哨兵思路 + 本文 §5.2 deopt 注入入口。table-driven 每个投机点形状一例,**每条 fb Kind × 每层 guard 各一例**:

```go
// V19 测试构造(table-driven,IsNumberB / IsNumberC / tableRef / gen / globalsGen / MetaGen ...)
func TestEveryGuardForceFailOnce(t *testing.T) {
    cases := []struct{ name, src, guardKind string }{
        {"FBArithStableNumber-IsNumberB", `local function f(a,b) return a+b end; return f(1.5,2.5)`, "IsNumber"},
        {"FBTableMono-tableRef", `…`, "tableRef"},
        {"FBTableMono-gen", `…`, "gen"},
        {"FBGlobalStable-globalsGen", `…`, "globalsGen"},
        {"FBSelfMono-MetaGen", `…`, "MetaGen"},
        // ... 每条 fb Kind × 每层 guard 各一例 ...
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            outOracle := runCrescentOnly([]byte(tc.src))                    // 全程 crescent
            state := wangshu.NewState(nil)
            bridge.SetForceAllPromoteJIT(state, true)
            jit.SetGuardForceFailFirst(state, tc.guardKind)                  // §5.2 测试入口
            outDeopt := runScript(state, []byte(tc.src))
            // 物化无损 + 着陆无损 = 端到端 byte-equal
            if !bytes.Equal(outOracle, outDeopt) { t.Fatalf("V19 broken on %s", tc.name) }
        })
    }
}
```

**关键纪律**(承 [../../../llmdoc/guides/prove-the-path-under-test.md](../../../llmdoc/guides/prove-the-path-under-test.md) §2):**V19 必须用强制失败构造,不能依赖偶发 deopt**——偶发 deopt 在普通用例下天然稀疏,差分跑过万千用例可能一条不撞;**强制失败把每条 exit 路径变成确定性触发**,把条件性 bug 转成确定性 bug(差几个数量级难度)。

### 4.4 这是「OSR 物化序列编译期静态生成」(04 §3.7)的端到端校验

承 [./04-osr-deopt.md](./04-osr-deopt.md) §3.7 不变式:**「OSR exit 的物化序列必须编译期静态生成,杜绝运行期映射查询、杜绝运行期分配」**。

V19 是这条不变式的端到端校验:

- **若运行期查 snapshot 配方**(P5 trace JIT 形式):V19 仍可能 byte-equal(若 snapshot 写得对),但 V19 通过不能反推 P4 没用 snapshot ——V19 只是必要条件不是充分条件;**P4 实现时 review 必须额外检查物化序列是编译期烧入的固定 store 序列**(`internal/gibbous/jit/osr.go` 的 emit 函数审查)。
- **若 exit 路径上做分配**:可能触发 GC + 违反 [../p3-wasm-tier/05-safepoint-gc.md](../p3-wasm-tier/05-safepoint-gc.md) 「分配只在受控点」哲学——V19 在 GC 压力 + V13 locals 写回 A/B 对照下能撞出脏读,但需要 §7 GC 压力联合验。

**V19 + GC 压力联合验**(承 §7.4):**deopt 注入 + 高频 GC 双开**——每 guard 强制失败时若 exit 路径有未上根的临时分配,高频 GC 期内必被回收,后续读栈槽必脏读 → 差分破。这是 V19 与 V13 的交叉,也是 P4 比 P3 多的「物化期 GC 触发」专门验证靶标。

---

## 5. deopt 注入模式(主动测每条 exit 路径)

### 5.1 deopt 路径天然触发稀疏,被动 fuzz 覆盖不足

承本文 §7.2 第三机制 + [./04-osr-deopt.md](./04-osr-deopt.md) §5.5 deopt 风暴的物理学。

**deopt 路径的天然稀疏性**:每个投机点 confidence 已被 P2 阈值滤过(≥0.99,承 [./03-speculation-ic.md](./03-speculation-ic.md) §2.7)——P2 标 stable 的点真实负载下 99% 走快路径,1% 才走 deopt;1000 次执行只有约 10 次 deopt,实际 deopt 触发**稀疏**;普通 e2e fuzz 跑万千脚本可能一条 deopt 都不撞——「deopt 着陆点是否物化无损」结构性失明;即便撞到 deopt 也只覆盖「这次刚好撞到的那条 exit 路径」,**其他 N-1 条 exit 路径的物化序列仍未验**。

**这是 [../../../llmdoc/guides/prove-the-path-under-test.md](../../../llmdoc/guides/prove-the-path-under-test.md) 反模式三档之 ①「空测/不公平基准」在 deopt 维度的具体形式**——「测试通过」(普通差分全绿)≠「在测的路径(deopt 着陆面)被走到」。

### 5.2 deopt 注入:「每个 guard 强制失败一次 / 每 N 次失败一次」

承本文 §7.2 第三机制字面承诺。**两个注入模式**(分别覆盖不同靶标):

| 模式 | 注入参数 | 靶标 | 配套 V |
|---|---|---|---|
| **每个 guard 强制失败一次** | `SetGuardForceFailFirst(state, kind)` | 验证每条 exit 路径的物化无损 | V19 |
| **每 N 次失败一次** | `SetDeoptInjectEvery(state, n)`(n=1 即每次,n=10 即 10% 触发率)| 验证 deopt 风暴防抖收敛 + P4StuckSpeculation 吸收态 | V20 |

**测试入口**(**当前状态**:2026-07-02 复核,`SetGuardForceFailFirst` 与 `SetDeoptInjectEvery` 两条 helper 均**未实现**——设计稿中登记的这两个 API 在 `internal/gibbous/jit/` 与全仓 grep 无匹配。V19 / V20 的端到端断言由业务路径的 e2e 与合成状态机单测承担):

```go
// internal/gibbous/jit —— deopt 注入测试入口(**设计稿承诺,当前未实现**;归 followup)
// kind ∈ { "IsNumberB", "IsNumberC", "tableRef", "gen", "globalsGen", "MetaGen", ... }
func SetGuardForceFailFirst(state *State, guardKind string)   // 第 1 次执行强制失败(未实现)
func SetDeoptInjectEvery(state *State, n int)                  // 每 n 次失败一次(未实现)
```

**未实现的等效覆盖**(承 09 §4「V19-V22 引用已统一到 codebase 里已有的等效测试」):

- **V19 端到端**:`internal/crescent/gibbous_pj5_self_e2e_test.go::TestPJ5_SelfCall_E2E_SpecTemplate_OSRExitToDeopt`——真业务路径构造 spec-template caller 打 mismatched-shape,触发 onOSRExit → SpecP4DeoptHits 累积 + P4Deoptimized 转移实证;
- **V20 端到端**:同文件 `TestPJ5_SelfCall_E2E_SpecTemplate_DeoptStorm`——5 caller 独立累积 SpecP4DeoptHits 互不串扰,配合 `internal/gibbous/jit/p4state_test.go` 7 个 P4 状态机单测(含 `TestP4SpecState_MaxRecompileTriesReachedStuck` 验 P4StuckSpeculation 吸收态);
- **per-guard 强制失败拆分口径**(V19 的更细粒度形式):归 followup,不阻塞 PJ11。

### 5.3 V8 `--deopt-every-n` 一样的思路

本节展开「V8 `--deopt-every-n` 一样的思路」。

V8 的 `--deopt-every-n=N` 是 V8 调试期 / fuzz 期的 flag——把 TurboFan 编译产物的每 N 次 guard 检查强制 deopt 一次,使 deopt 路径被持续踩热。这条 flag 是 V8 fuzz 套(ClusterFuzz)的标准武器,撞出过大量「deopt 着陆点 snapshot 重建错」类 bug。

**P4 借鉴的具体形式**:

| V8 形式 | P4 对位 |
|---|---|
| `--deopt-every-n=N`(全局 flag)| `SetDeoptInjectEvery(state, n)`(per-State testing-only)|
| `--always-opt`(强制全升)| `bridge.SetForceAllPromoteJIT`(承 §3.7)|
| `--allow-natives-syntax`(测试用语义注入)| 不需要(P4 通过 testing helper 注入,不污染 Lua 语法)|

**为什么 P4 不用「flag」而用「per-State helper」**:Go 测试不像 V8 是在独立进程跑(每个测试 fork V8),是同进程多 testing.T 串/并行——全局 flag 会跨测试污染。**per-State helper** 是 Go 测试模式的天然适配,与 P3 的 `SetForceAllPromote`(承 [../p3-wasm-tier/08-testing-strategy.md](../p3-wasm-tier/08-testing-strategy.md) §2.2)一样的。

### 5.4 让 fuzz 把每条 exit 路径都踩热

承本文 §7.2 + [../../../llmdoc/guides/prove-the-path-under-test.md](../../../llmdoc/guides/prove-the-path-under-test.md) §2(c)正向命中证据。**联合 fuzz 撒网设计**:`every` 取小值(1~10)频繁 deopt → 验 V20(防抖收敛);取大值(20~100)偶发 deopt → 验 V19(物化无损):

```go
//go:build wangshu_p4
// FuzzDeoptInjection —— V19/V20 联合 fuzz(承 §3.4 nightly 运行)
func FuzzDeoptInjection(f *testing.F) {
    f.Add(uint32(0xc0ffee), uint32(7))   // (seed, deoptInjectEvery)
    f.Fuzz(func(t *testing.T, seed uint32, every uint32) {
        if every == 0 || every > 100 { return }
        src := genCompilableScript(seed)
        if !isValidP1(src) { return }
        outC := normalize(runCrescentOnly([]byte(src)), defaultNorm)               // oracle
        state := wangshu.NewState(nil)
        bridge.SetForceAllPromoteJIT(state, true)
        jit.SetDeoptInjectEvery(state, int(every))
        outP4 := normalize(runScript(state, []byte(src)), defaultNorm)
        // 注入下仍 byte-equal ⇒ 物化无损(V19) + deopt 风暴下不死锁(V20)
        if !bytes.Equal(outC, outP4) { t.Errorf("[V19/V20] every=%d: %s", every, src) }
    })
}
```

fuzz 引擎自动探索 every 维度,每条 fuzz 用例同时验两条 V。

### 5.5 与 prove-the-path-under-test guide 一致(空测/未走加速路径反模式的对偶面)

承 [../../../llmdoc/guides/prove-the-path-under-test.md](../../../llmdoc/guides/prove-the-path-under-test.md) §2(a)毒化哨兵 + §2(c)正向证据 + §3 错误路径覆盖。

**deopt 注入是该 guide 在 P4 维度的具体兑现**——「测试通过 ≠ 在测的路径被走到」在 P4 投机维度对应「差分全绿 ≠ deopt 着陆面被走到」:

| guide 反模式 | P4 deopt 维度对偶 | 解药 |
|---|---|---|
| ① 空测/不公平基准 | 普通 fuzz 不撞 deopt(天然稀疏)| **deopt 注入主动踩热**(本节)|
| ② 静默替身 | OSR exit 与「全程解释」输出等价,普通 e2e 不分辨 | **V19 强制每 guard 失败 + 物化序列编译期静态生成的 review**(承 §4.4)|
| ③ 覆盖度自欺 | 手写 N 条 deopt 用例 ≠ 真覆盖每条 exit 路径 | **fuzz + 注入 + V22 guard 漏判 fuzz 联合**——单测 + fuzz 双轴(承 §6.2 P3 一样的)|

**这是 [../../../llmdoc/guides/prove-the-path-under-test.md](../../../llmdoc/guides/prove-the-path-under-test.md) 升级为 first-class guide 的最大用户**——guide 列出的 7 个独立实例都是 P3 PW5/PW6/PW9/PW10 + VS0-e,P4 在投机错果维度第一天起就把整套解药内建进测试结构。

### 5.6 deopt 风暴防抖测试:同 Proto 反复 deopt → 计数过阈值 → P4StuckSpeculation,后续不再投机但仍正确

承 [./04-osr-deopt.md](./04-osr-deopt.md) §5.2-§5.5 + §5.6 阈值校准。V20 三断言:

```go
// V20 deopt 风暴防抖收敛测试(高频 deopt 输入打破投机假设)
func TestDeoptStormToStuck(t *testing.T) {
    src := `local function f(x) return x*2 end
            for i=1,1000 do f(i) end          -- 训练:IC 标 FBArithStableNumber, conf=0.999
            for i=1,1000 do f(tostring(i)) end -- 风暴:每次 deopt
            return "done"`
    state := wangshu.NewState(nil)
    bridge.SetForceAllPromoteJIT(state, true)
    outC := runCrescentOnly([]byte(src))                  // oracle
    prog, _ := wangshu.Compile([]byte(src), "v20")
    _ = prog.Call(state, nil)
    pd := bridge.ProfileDataOf(state, prog.HotProto())

    // 断言 1:进入 P4StuckSpeculation 吸收态(P4 内部状态;P2 tierState 仍 TierGibbous,承方案 A)
    if jit.SpecStateOf(prog.HotProto()) != jit.P4StuckSpeculation { t.Fatalf("V20 防抖未收敛:p4state=%v", jit.SpecStateOf(prog.HotProto())) }
    // 断言 2:吸收态后输出仍 byte-equal crescent(走通用模板,不再投机)
    state2 := wangshu.NewState(nil); bridge.SetForceAllPromoteJIT(state2, true)
    if !bytes.Equal(runScript(state2, []byte(src)), outC) { t.Fatalf("V20 吸收态正确性破") }
    // 断言 3:不重试投机(deopt 计数停止增长)
    before := bridge.DeoptCountOf(state, prog.HotProto())
    _ = prog.Call(state, nil)
    if bridge.DeoptCountOf(state, prog.HotProto()) > before { t.Fatalf("V20 不重试纪律破") }
}
```

**V20 三断言的意义**(承 [./04-osr-deopt.md](./04-osr-deopt.md) §5.3-§5.5):**吸收态可达**(deopt 计数过阈值必转 `P4StuckSpeculation`)+ **正确性不损**(吸收态后通用模板输出仍 byte-equal crescent,「拉黑投机不损正确性」字面兑现)+ **不重试纪律**(吸收态后不再尝试投机重编,与 P2 不重试纪律对位)。

---

## 6. 双架构双跑(V21,承 06-backends §5)

### 6.1 [12 §8] CI 门禁在 amd64 与 arm64 物理 runner 各跑全套

承 [../p1-interpreter/12-testing-difftest.md](../p1-interpreter/12-testing-difftest.md) §8 CI 门禁 + [./06-backends.md](./06-backends.md) §5.1 / §5.3 双架构纪律:

> CI 门禁要求:① 每次 PR 必跑 difftest 全套;② amd64 与 arm64 物理 runner 各跑全套;③ nightly fuzz 叠加 P4 runner——P4 是望舒第一个会说谎的层(投机错误静默产错果),fuzz 是覆盖 deopt 路径的主要手段。

**双架构双跑的 CI 落点**(示意,P4 完成时按实际 GitHub Actions / 自托管 runner 形式调):

```yaml
# .github/workflows/ci.yml —— P4 接入双架构(示意)
jobs:
  difftest-amd64:    # GitHub Actions amd64 默认
    runs-on: ubuntu-latest
    steps: { run: make test-p4 && make test-p4-race }
  difftest-arm64:    # ★ 物理 arm64 机(自托管 runner / GitHub arm64)
    runs-on: arm64-physical-runner
    steps: { run: make test-p4 && make test-p4-race }
  cross-arch-byte-equal:  # ★ V21 双架构 byte-equal 验收
    needs: [difftest-amd64, difftest-arm64]
    runs-on: ubuntu-latest
    steps: { run: make verify-cross-arch-equal }
```

### 6.2 同 Proto crescent vs gibbous-jit byte-equal,amd64 与 arm64 互为对照

承 [./06-backends.md](./06-backends.md) §5.2 字面:`crescent (Go) ←─ byte-equal ─→ gibbous-jit/amd64 (native) ↕ byte-equal ←─ byte-equal ─→ gibbous-jit/arm64 (native)`。

**双架构差分把两层 bug 都暴露**(承 [./06-backends.md](./06-backends.md) §5.2 末):**投机模板 bug**(architecture-independent)→ 两架构同时 fail,定位在 `internal/gibbous/jit/template.go` 类共享骨架;**per-arch 编码 bug(amd64)**→ 仅 amd64 fail,立即定位到 `internal/gibbous/jit/amd64/emitter.go`;**per-arch 编码 bug(arm64)**→ 仅 arm64 fail,立即定位到 `internal/gibbous/jit/arm64/emitter.go`。

**V21 实现形式**:`difftest/p4_test.go::TestDualArchByteEqual` 用 `runtime.GOARCH` 决定当前架构跑 gibbous-jit,与 `runCrescentOnly` 比对;跨架构 byte-equal 由 **CI matrix 把两架构 artifact 拉下来比对**——不能在单个 CI job 内验(单 job 只能跑一个架构),必须靠 CI orchestration。

### 6.3 交叉编译只能保证能构建,不能代跑差分——CI 必须有真 arm64 机器

承 [./06-backends.md](./06-backends.md) §5.3 字面:**CI 必须有真 arm64 物理 runner**——交叉编译(`GOARCH=arm64 go build`)只验证「代码能编」。理由:① arm64 icache flush 序列、内存模型差异(LDR/STR 弱有序、需 dmb 屏障)、原子操作语义,在 amd64 上模拟不了——只有真硬件能跑出 race condition;② wazero 项目一样的 CI 矩阵(linux/amd64 + linux/arm64 + darwin/amd64 + darwin/arm64 全跑差分);③ **CI 不跑 = 实质未测**(承 [`prove-the-path-under-test`](../../../llmdoc/guides/prove-the-path-under-test.md) §1)。

**配套口径**(承 [./06-backends.md](./06-backends.md) §5.3 表):linux amd64 / arm64 全 difftest + nightly fuzz + benchmark;darwin amd64 / arm64 至少 difftest 主套(MAP_JIT 平台特异性);windows amd64 至少 build + 基本测试(VirtualProtect 平台特异性)。

**「CI 不跑 = 实质未测」的 P4 维度具体形式**:arm64 emitter 漏 dmb 屏障,只在多核并发某窗口触发(测试无并发不撞);只在多 State 并发跑 P4 编译产物时撞——只有 -race + 真 arm64 物理机才暴露。CI 不跑 arm64 = arm64 部分发布失守。

### 6.4 双架构 CI 资源开销(承 06-backends §5.7)

承 [./06-backends.md](./06-backends.md) §5.7 + 本文 §11 风险 5:**双后端 + 双架构 CI 是长期固定成本;arm64 滞后交付不阻塞 P4 验收**(验收平台定 amd64),但发布口径须如实标注。

**资源预算**(参考 wazero 项目一样的规模):arm64 物理 runner CI 永久托管 1-2 台(至少 1 台跑 PR,1 台跑 nightly,避免阻塞 PR fuzz);单次 PR check 时长 8-12 min(amd64 + arm64 串行关键路径 + difftest 主套);nightly 时长 6-12 h(双架构 fuzz 各 2-4 h + GC 压力 + longevity);月度 CI cost(自托管 arm64)中等量级(物理机 + 电力 + 监控 + 备份)。

**降本策略**(若资源吃紧):① **PR 短 fuzz 单架构**(PR 只跑 amd64 短 fuzz,arm64 短 fuzz 移到 nightly);② **nightly 长跑跨架构错峰**(周一/三/五跑 amd64 长 fuzz,周二/四/六跑 arm64,平摊 runner 占用);③ **release 前再跑全集**(平日只跑差分主套 + 短 fuzz,release 前跑 V20 deopt 风暴长跑天数级 + V22 guard 漏判 fuzz 全集)。

### 6.5 arm64 滞后交付时的 CI 矩阵应急方案(amd64 全跑 + arm64 子集)

承 [./06-backends.md](./06-backends.md) §5.7 + 本文 §11 风险 5:**验收平台定 amd64;发布口径标注;不退化 difftest 矩阵**(arm64 build 仍要过,只是「每 PJ 的 arm64 验收」推后);**正式 GA 条件**:arm64 全 PJ 通过 + 双架构 nightly fuzz 跑 ≥30 天无差异。

**应急 CI 矩阵分级**:

| 阶段 | amd64 | arm64 |
|---|---|---|
| **PJ7 amd64 端到端验收** | 全 V1-V22 | build only(交叉编译验「能编」)|
| **PJ8 arm64 启动** | 全套 | V1-V11 渐进对位(每 opcode 同步)|
| **PJ9 双架构差分** | 全套 | 全套 + V21 跨架构验,nightly ≥30 天无差异 |
| **PJ11 luajc 档全验收** | 全套 + V14/V15 luajc 档 | 全套 + V14/V15 luajc 档(GA)|

**arm64 滞后交付时的发布口径**:`gibbous/jit` 发布如实标 Linux amd64 GA / Linux arm64 Beta(滞后)/ Darwin arm64 Beta(MAP_JIT 平台特异性,V20 长跑验证中)/ Windows amd64 Build only。

**严格不变式**:**arm64 build 即便不跑 V21/V22,仍要 build 通过 + V1-V18 跑过**——「能编不破 + 主防线不退」是 arm64 滞后交付的最低底线。

---

## 7. GC 压力 fuzz 延伸(承 P1 12 §5 + P3 08 V5/V13)

### 7.1 P1/P3 既有 GC 压力 fuzz 模式 + P4 叠加

承 [../p1-interpreter/12-testing-difftest.md](../p1-interpreter/12-testing-difftest.md) §5 + [../p3-wasm-tier/08-testing-strategy.md](../p3-wasm-tier/08-testing-strategy.md) §3 全节(GC 压力 fuzz 上 gibbous):

| 阶段 | GC 压力靶标 |
|---|---|
| P1 | mark 漏扫某字段 / shadow stack 漏 Pin / freelist 复用语义破裂 |
| P3 | + locals 缓存写回遗漏(若启用)/ imported 助手内 Pin 漏 |
| **P4** | + JIT 持 arena base 跨 safepoint / 回边检查点漏布 / 物化期 GC 触发 + 物化序列内的临时分配漏根 / 寄存器→栈槽写回遗漏 |

高频 GC 把概率拉到 1,漏根对象必被回收,后续用必崩/脏读(承 P1 12 §5)。**P4 GC 压力 fuzz 把上述靶标全覆盖**——既继承 P1/P3 套也叠加 P4 独有的「JIT 世界 GC 协议」靶标。

### 7.2 专打「JIT 持 arena base 跨 safepoint」(05-system-pipeline §5)

承 [./05-system-pipeline.md](./05-system-pipeline.md) §5 「arena base 在两个 safepoint 之间稳定」纪律:arena 搬迁(扩容)只发生在分配慢路径(出 JIT 世界);JIT 内联 bump 分配快路径越界即出去,回来从 context 重载 base。这是 [05](../p1-interpreter/05-interpreter-loop.md) §1.3「重载 stk」纪律在机器码层的同构。

**JIT 持 arena base 跨 safepoint 的潜在 bug**:① arena base 缓存进 jitContext 寄存器(如 r15),JIT 内某操作触发 arena 扩容(helper 内分配),返回后 r15 仍是旧 base 再 load/store 是 UAF;② 回边检查点 + GC 触发,gcPending 被设但 r15 未刷新,GC 后续读 r15 看到旧 arena 是脏读;③ trampoline 出 JIT 调 helper 期间 arena 可能 grow,trampoline 回来未从 jitContext 重载 r15。

**P4 GC 压力 fuzz 必须验**:强制 arena 频繁扩容(SetArenaCap 设小值 + 高频分配脚本)+ 高频 GC(GCPAUSE=1)+ force-all-jit;期望每次 JIT 帧返回后 arena base 重载,无 UAF,正常 pacing 与压力下输出 byte-equal。

### 7.3 专打「回边检查点漏布」(05 §6)

承 [./05-system-pipeline.md](./05-system-pipeline.md) §1.2 「异步抢占」纪律:生成码在循环回边插抢占检查点,load `jitContext.preemptFlag`,置位则经 exit stub 退到边界(GC safepoint / 调度让出共用此点);直线段长度有界 ⇒ 不可抢占窗口有界。

**回边检查点漏布的 bug**:① 某循环模板未在回边发射 `cmp [jitContext + preemptFlagOff], 0; jne preempt_exit`——长循环 JIT 帧不响应抢占,Go 调度器无法切换 goroutine,严重时整个进程卡死;② 检查点发射了但 preempt_exit stub 内未先物化栈槽——抢占退出后 GC 扫到陈旧栈槽 → 误回收。

**P4 GC 压力 fuzz 必须验**:长循环脚本(100 万次回边)+ 多 goroutine 并发跑(逼出 Go 调度抢占)+ 高频 GC,期望无 deadlock + 输出仍 byte-equal crescent。

### 7.4 GC 压力下 deopt 状态等价(物化期 GC 触发)

承 §4.4(V19 + GC 压力联合验)+ [./04-osr-deopt.md](./04-osr-deopt.md) §3.7 物化期 GC 触发的禁止项。

**物化期 GC 触发的 bug 形式**:① OSR exit 物化序列内若有「未上根的临时分配」(违反 §3.7 不变式)——高频 GC 期内必撞,临时分配被回收 → 物化写到陈旧栈槽 → exit 后 crescent 读栈槽脏值;② 物化序列内寄存器写回到栈槽期间触发 GC——若写回未完成 GC 扫描看到部分新部分旧的栈槽,根可见性不一致。

**联合验形式**:每条 guard kind ∈ {IsNumberB, tableRef, gen, globalsGen} × 每条 alloc-heavy OSR 脚本 × 正常 pacing 与 GCPAUSE=1 双档对照——任一档 != crescent oracle 即 V19+V13 物化期 GC 触发破透明性 / 物化序列 unsafe。

### 7.5 GC 压力下双架构差分(amd64/arm64 各跑)

承 §6 + §7.2-§7.4 GC 压力靶标 + arm64 内存模型差异。**双架构 GC 压力的特异性**:amd64 内存模型 TSO(强一致),写到栈槽 → GC 读默认可见;arm64 LDR/STR 弱有序需 dmb 屏障——若 emitter 漏 dmb,GC 可能读到陈旧栈槽,只在 arm64 + 多核并发 + 高频 GC 才撞。**这就是 §6.3「CI 必须有真 arm64 物理机」的具体兑现**——交叉编译模拟不出 arm64 弱内存模型。

**测试形式**(承 §3.4 nightly fuzz 双架构):GC 压力 fuzz 在双架构 nightly job 各跑 ≥20 count,任一架构破透明性即阻断。

### 7.6 GC 压力 fuzz 与 nightly 长跑的接续

承 [../p1-interpreter/12-testing-difftest.md](../p1-interpreter/12-testing-difftest.md) §11 + [../p3-wasm-tier/08-testing-strategy.md](../p3-wasm-tier/08-testing-strategy.md) §3.3。GC 压力 fuzz 在 P4 nightly 接入:**GC 压力上 P4** `make test-p4-fuzz` 跑 `TestGCStressGibbousJIT`(承 §3.4)`-count=20`;**longevity + 双架构 + 累积态稳定**(数百万迭代,双架构 nightly 各跑 ≥30 天,验「JIT 帧 + 跨层往返 + arena live set 累积」无泄漏);**物化期 GC 触发长跑**(V19+V13 联合 fuzz,本节 §7.4)在 nightly ≥10h 跑,撞物化序列内的稀疏 bug。

---

## 8. 性能基准(luajc 档 + 实测口径)

### 8.1 列内核基准(承 P3 08 §V14)

承 [../p3-wasm-tier/08-testing-strategy.md](../p3-wasm-tier/08-testing-strategy.md) §5.1 + §5.1.1 同机 A/B 方法论。`benchmarks/realworld/realworld_test.go::BenchmarkLoopHeavy` 加 `gibbous-jit` 子档对照(crescent / gibbous-wasm / gibbous-jit 三档同机同进程内顺序执行,固定 CPU 频率,足量 `-benchtime`)。验收脚本读三档 ns/op,**断言 P4 ≤ luajc 档 164μs(校准测量 1 锚点)**——基准必须用 Horner 多项式 1000 items(列内核硬约束,承 §1.4)。

### 8.2 P4 应替换 P3 V14 ≥2x P1 为 ≥164μs 一档

承 §1.1 + §2.2:**V14 在 P4 维度的精确口径是绝对水位 ≥ luajc 档**,而非相对 P1 的倍率。

物理理由:

| 指标 | 倍率(P1 基线 ≥2x)| 绝对水位(luajc 档 ≥164μs)|
|---|---|---|
| **跨机器可比性** | 弱(不同机器主频/缓存差异)| 强(同硬件同基准对位 luajc 校准)|
| **跨阶段可比性** | 弱(P3 已 2.95x,P4 也 2.95x ⇒ P4 = P3?)| 强(P3 wasm 中介限制上限,P4 直发原生有更高上限)|
| **「逼近 LuaJIT」语义** | 不直接体现 | 直接体现(luajc 仅比 LuaJIT 慢 6%)|
| **承前提一锚点** | 间接 | 直接(Horner 校准是前提一的物理基础)|

**V14 P4 与 V14 P3 不冲突**:P3 的「≥2x over P1」是 P3 价值兑现门(P3 在 wasm 中介下能拿多少加速),P4 的「≥164μs 一档」是 P4 价值兑现门(直发原生能不能逼近 LuaJIT 档)。**两者在 P4 build 下都跑,P3 维度的 V14 在 P4 build 下应自动通过(P4 通常比 P3 还快)**。

### 8.3 多核形式分桶(loop / table / call / mixed,承 P3 11.3)

承 [../p3-wasm-tier/implementation-progress.md](../p3-wasm-tier/implementation-progress.md) §11.3 PW9 性能轴实测对账。**P3 PW10 实测基线**(本机 Xeon 6982P 2s×3 count, 2026-06-16):loop 2.95x / table 0.88x / call 0.52x(bench kernel 结构性架构边界 F2-b unknown call)/ mixed 0.99x / geomean 0.79x(V15 未达,P3 收口为「已完成子里程碑 + 架构边界文档化」)。

**P4 在四桶上的预期与靶标**:

| 桶 | P3 实测 | **P4 预期** | P4 物理优势 |
|---|---|---|---|
| **loop** | 2.95x | **luajc 档 1.5x 余量**(承 §1.6)| 直发原生消 wazero 中介 / 内联 IC 直达槽 / f64 直算 |
| **table** | 0.88x | **≥1.5x** | 同上 + 编译期立即数寻址 |
| **call** | 0.52x(F2-b 不可升)| **≥0.8x**(改善但不消除架构边界)| jit→jit 直跳 + 自管栈减少边界税 |
| **mixed** | 0.99x | **≥1.5x** | 各项优势叠加 |

**纪律**(承 [../../../llmdoc/guides/perf-optimization-workflow.md](../../../llmdoc/guides/perf-optimization-workflow.md) §7):**P4 验收按「分桶 + 主桶达标」**——loop/table/mixed 至少各达预期,call 桶单独标注「F2-b 架构边界」(若 P4 也无法显著消除,如实文档化,**不为追原数字硬上 UAF**)。

### 8.4 跨机器基线对照纪律(承 perf-optimization-workflow §5)

承 [../../../llmdoc/guides/perf-optimization-workflow.md](../../../llmdoc/guides/perf-optimization-workflow.md) §5 跨机器/跨参数 perf 基线对照:

> 任何性能数字判定回归 / 收益前,必须**同 commit 同硬件同参数复测对照**——绝不拿 memory/reflection/implementation-progress 里的历史数字直接下结论。

**P4 验收对该纪律的应用**:

- **luajc 档 164μs 是 Xeon 6982P-C 上的实测值**(校准测量 1)——P4 验收复测必须**同硬件**,不能拿其他机器的 ns/op 直接下结论;
- **跨机器换算时**:用 gopher-lua 同机比例换算(如 P4 实测机上 gopher-lua 跑 X μs / 校准测量 X' μs 的比例,把 luajc 档 164 / X' × X 算成本机的等价水位);
- **memory/reflection 写 perf 数字必标硬件 + 参数 + 日期**(承 [../../../llmdoc/guides/perf-optimization-workflow.md](../../../llmdoc/guides/perf-optimization-workflow.md) §5 末次纪律)——P4 验收数字进 implementation-progress 时必标。

### 8.5 立项数字目标 vs profile 实测瓶颈(承 perf-optimization-workflow §7)

承 [../../../llmdoc/guides/perf-optimization-workflow.md](../../../llmdoc/guides/perf-optimization-workflow.md) §7 立项数字目标 vs profile 实测瓶颈(profile 才是合同):

> 立项数字目标(0.X x → ≥Y x)是「方向陈述」,**不是合同**。完成中跑 cpuprofile / -benchmem 揭示「真实瓶颈所在 + 各块占比」才是合同。

**P4 验收对该纪律的应用**:

- **PJ7 第二检查时若 amd64 单架构 Horner 距 luajc 档仍远**(承 §1.6),先跑 cpuprofile 定位主导项,**不为追原数字硬上**:
  - 若主导项是 dispatch 残余(应该被 P4 消解但实测仍占 30%)⇒ 模板编译质量有问题,回 §3 模板分级表逐 opcode review;
  - 若主导项是 GC pacing(高频 ns/op 抖动)⇒ 调 GCPAUSE / arena cap;
  - 若主导项是边界税(jit→helper 跨层调用占 50%)⇒ 那就是 P3 一样的 F2-b 架构边界,文档化为已知边界,不硬上;
- **PJ11 luajc 档全验收时 V15 realworld geomean 不达预期** ⇒ 同上 profile 优先,不强行刷数字。

**P4 收口形式**(若 PJ11 实测 luajc 档某桶不可达):「**已完成子里程碑 + 架构边界文档化**」是合法收口形式(承 PW10 P3 一样的,[../../../llmdoc/memory/reflections/2026-06-16-p3-pw10-architectural-ceiling-round.md](../../../llmdoc/memory/reflections/2026-06-16-p3-pw10-architectural-ceiling-round.md))。

### 8.6 与 LuaJIT 直接对照基准(P4 直接对位)

承 [../p1-interpreter/12-testing-difftest.md](../p1-interpreter/12-testing-difftest.md) §6.5 calibration 基准 + 入库可复现立项数据:

```
benchmarks/calibration/horner_5_1000.lua    # 校准测量 1 主基准(承 12 §6.5)
results/
  measurement1_horner.md                     # 测量 1 原始数据表(729 / 259 / 164 / 154μs)
  luaj/                                       # (可选)LuaJ luac/luajc 复现脚本
  luajit/                                     # (可选)LuaJIT 复现脚本
```

**P4 直接对位 LuaJIT**(立项核心论据兑现):

- P4 验收时**重跑 calibration/horner_5_1000.lua** 在与校准测量 1 同硬件(Xeon 6982P-C)上,记录 ns/op;
- 与 LuaJIT 154μs 直接对照(允许 ±5%),验「P4 逼近 LuaJIT 档」字面成立。

**Note**:LuaJIT 复现需 C 工具链,不进 P4 CI 主流程(同 P1 12 §6.5 纪律);LuaJIT 数字作为参照点,**不阻塞 P4 验收**(P4 验收锚是 luajc 档 164μs,LuaJIT 154μs 是「逼近顶档」叙事的参照)。

---

## 9. 工程轴(V17-V18)

### 9.1 多 build tag(default / wangshu_p3 / wangshu_p4 / wangshu_profile / 各组合)

承 [../p3-wasm-tier/08-testing-strategy.md](../p3-wasm-tier/08-testing-strategy.md) §6.4(三套 build tag)+ P4 新增第四套 `wangshu_p4`:

| build 组合 | P2 bridge | P3 gibbous-wasm | P4 gibbous-jit | 等价于 |
|---|---|---|---|---|
| `default`(无 tag)| mock | 不存在 | 不存在 | P1-only |
| `wangshu_profile` | 真 bridge | mock P3 | mock P4 | P2(无升层)|
| `wangshu_profile,wangshu_p3` | 真 bridge | **真 P3** | mock P4 | P3 完整 |
| `wangshu_profile,wangshu_p3,wangshu_p4` | 真 bridge | **真 P3** | **真 P4** | **P4 完整** |
| `wangshu_profile,wangshu_p4`(P3 退役场景)| 真 bridge | 不存在 | **真 P4** | P4 自承担分层骨架(承 [./01-launch-judgment.md](./01-launch-judgment.md) §2.2 跳跃路径)|

**为什么 `wangshu_p4` 默认依赖 `wangshu_p3`**(承 [./01-launch-judgment.md](./01-launch-judgment.md) §2):**常规路径 P3 之后**——P3 已上线但收益不够,P4 是纯后端替换;P4 与 P3 共享同一份 bridge 决策机 + 升层状态机 + 跨层调用协议,P4 在 P3 已建好的分层骨架上只换发射后端。**特殊场景**(P3 退役)由 [./07-p3-retirement.md](./07-p3-retirement.md) 决策框架确定。

**V17 四套 build 零回归**(P3 在 P4 build 下不豁免):`make all-p4` 依次跑 default(P1-only)/ wangshu_profile(P2)/ wangshu_profile+wangshu_p3(P3)/ wangshu_profile+wangshu_p3+wangshu_p4(P4 完整)四档全绿。

### 9.2 -race 通过

承 [../p3-wasm-tier/08-testing-strategy.md](../p3-wasm-tier/08-testing-strategy.md) §V18 + 双架构扩展(承 §6):`make test-p4-race` 在 amd64 与 arm64 物理 runner 各跑 `go test -tags 'wangshu_profile wangshu_p3 wangshu_p4' -race -count=10 ./test/difftest/...` + `./internal/gibbous/jit/...`。

**V18 在 P4 维度的具体靶标**:**多 State 并发跑 gibbous-jit**(P4 比 P3 多一层风险——共享 P4 codeSeg、共享 osrExits 数组、共享 P4 端 `p4SpecState[proto]` map)+ **跨架构内存模型不一致(arm64)**(emitter 漏 dmb 屏障在 -race 下浮现,承 §7.5)+ **deopt 状态字段并发写**(多 State 同时触发同 Proto 的 deopt → `p4SpecState[proto].deoptCount` atomic 加 + P4StuckSpeculation 转移的并发原子性,承 [./04-osr-deopt.md](./04-osr-deopt.md) §6.2 lock inc)。

### 9.3 双架构 CI 资源(承 §6.4 + 06-backends §5)

承 §6.4 资源开销 + [./06-backends.md](./06-backends.md) §5.7。本节不重复,只点出与工程轴的交互:

- **V17 四 build × 双架构 = 8 个独立 CI 矩阵格**——若资源紧张,按 §6.5 应急方案分级;
- **`make all-p4` 在双架构都跑过才算 V17 全过**——单架构通过的 PR 不合入(arm64 滞后场景按 §6.5 标 Beta)。

### 9.4 nightly 长跑(承 12 §8)

承 [../p1-interpreter/12-testing-difftest.md](../p1-interpreter/12-testing-difftest.md) §8 + §11 + [../p3-wasm-tier/08-testing-strategy.md](../p3-wasm-tier/08-testing-strategy.md) §3.3。P4 nightly 长跑套(承 §3.4 nightly job 矩阵):层间差分长 fuzz `FuzzCrescentVsGibbousJIT` 2h × 双架构 / guard 漏判 fuzz `FuzzGuardOmission`(V22)2h × 双架构 / deopt 注入 fuzz `FuzzDeoptInjection`(V19+V20)2h × 双架构 / GC 压力 fuzz 上 gibbous-jit 1h × 双架构 / longevity 长跑(数百万迭代,V20 风暴防抖)4h × 双架构 / 性能基准全档(V14-V16)30 min × 双架构 / Horner 校准重跑(校准测量 1 锚点)10 min(amd64 only,参考点)。

**长跑发现 bug 的处理**:承 P3 一样的,最小化复现 + 分类(翻译 bug / 投机 bug / 物化 bug / 双架构 bug)+ 固化为 conformance 用例回流(承 P1 12 §3.6)。

---

## 10. P3 → P4 验收套迁移协议(若 P3 退役)

承 [./07-p3-retirement.md](./07-p3-retirement.md)(P3 Wasm 层的去留)+ [../p3-wasm-tier/08-testing-strategy.md](../p3-wasm-tier/08-testing-strategy.md) §0.4 末「P3 退役协议」预设位:

**前提**:[./07-p3-retirement.md](./07-p3-retirement.md) §3 缺省倾向是「P4 验收通过后,P3 退役」(代码留在版本史,降低差分矩阵与维护面);留下的唯一翻案条件是「实测证明 wazero 解释模式跑翻译后的 Wasm 在某类真实负载上仍显著快于 crescent」(可能性低)。

若 P3 退役完成,本节定迁移协议。

### 10.1 [12 §3.8] Runner 表移除 WangshuGibbous(P3),新增 WangshuGibbousJIT(P4)

P3 退役完成后:`test/difftest/runner.go` 保留 P1 三 runner(WangshuInterp / GopherLua / OfficialLua)+ P4 主 runner `WangshuGibbousJIT`;`WangshuGibbous` 删除(若需保留作历史 bench 参照,放 benchmarks/legacy/ 而非 difftest 主套)。**注意**:**Runner 表删除 WangshuGibbous 不等于删 P3 代码** —— 删的是 difftest 接入,P3 代码 `internal/gibbous/wasm` 留在版本史(便于「未来某场景需要回退到 wasm 中间层」时还原)。

### 10.2 V1-V18 名义不变,P4 接续

承 §2.1-§2.3 + P3 退役场景:V1-V13(正确性轴)P4 接续承担(不再有 crescent vs gibbous-wasm,只剩 crescent vs gibbous-jit);V14-V16(性能轴)P4 替换为 luajc 档(承 §1.1 + §8.2);V17-V18(工程轴)P4 接续承担(三套 build 删 wangshu_p3,变成 default / wangshu_profile / wangshu_p4 三套);V19-V22(P4 增项)P4 主(本就是 P4 引入)。

**纪律**:P3 退役后,V1-V18 编号**保留**(不要重新编号 V1-V8 等)——保留编号是承诺连续性,新合入的 PR 仍可引用历史 V14 验收数字而不混淆。

### 10.3 force-all 入口在 P4 期改为 force-all-jit(承 P3 implementation-progress §11.2 force-all 模式)

承 §3.7 + [../p3-wasm-tier/implementation-progress.md](../p3-wasm-tier/implementation-progress.md) §11.2。两个测试入口的命名:**P3 与 P4 共存场景**(常规路径,§9.1):`SetForceAllPromote` 升 P3 wasm,`SetForceAllPromoteJIT` 升 P4 jit,两入口并存;**P3 退役场景**:只剩 `SetForceAllPromoteJIT`,旧入口 deprecated 保留兼容性 N 个版本后删除。

### 10.4 deopt 注入与 force-all 在 P4 期共存

承 §3.7 + §5.2:**P4 期 force-all 模式与 deopt 注入模式同时存在**——两者目标不同,但可同时启用:

| 启用模式 | 测试目标 |
|---|---|
| 仅 force-all-jit | V12 强制全升下 byte-equal(覆盖最大化所有 Compilable Proto)|
| 仅 deopt 注入 `SetGuardForceFailFirst` | V19 每条 exit 路径物化无损 |
| 仅 deopt 注入 `SetDeoptInjectEvery` | V20 deopt 风暴防抖收敛 |
| force-all-jit **+** deopt 注入 | **V19/V20/V22 联合 fuzz 主形式**(承 §5.4)——把所有 Proto 升 P4 + 主动注入 deopt → 把每条 exit 路径从「天然稀疏」变成「确定性踩热」 |

**双重模式工程意义**:**force-all 把覆盖率拉满,deopt 注入把每条 exit 路径踩热**——两者正交,联合启用差分覆盖度最大化,这是 P4 持续 fuzz 标准配置(nightly 默认开启)。

---

## 11. 风险与开放问题

### 11.1 投机错果的根本性威胁(承旧 §8 风险 2 + roadmap §5 原则 2)

**风险**:**P4 是望舒第一个会说谎的层** —— guard 漏判产出错果不崩溃不报错,有限用例测不出。这是 [../roadmap.md](../roadmap.md) §5 原则 2 字面点名的「JIT 最危险 bug 类」,也是 P4 测试套整套设计的根本驱动。

**触发条件的稀疏性**:guard 漏判常常是「某条 if 分支没考虑到 NaN-box 边界编码」/「假设 R(B) 是 number 但忘了中途被 MOVE 改成 string」/「同表+同代次 guard 但忘了元方法 chain 也要重核对」——这些**只在罕见输入触发**(99% 走快路径不撞,投机错果在生产端默默累积错果不报警);一旦撞上**追溯极难**(没崩溃、没 stack trace、只有「某个业务结果错了」)。

**P4 测试套的全套防线**:同 Proto 差分(§3,V1-V13)防翻译 bug + 直接 guard 漏判;OSR 状态等价(§4,V19)防物化无损 / 着陆面正确;deopt 注入(§5,V19/V20)主动踩热每条 exit 路径,把条件性 bug 转成确定性 bug;guard 漏判 fuzz(§2.4,V22)直接禁用每条 guard 验「这条 guard 真在防东西」;GC 压力 fuzz 联合(§7,V13/V19+V13)防物化期 GC 触发 / arena base 跨 safepoint;双架构双跑(§6,V21)防 emitter 不一致;nightly 长跑(§9.4)防累积态稳定 + 罕见角落。**任一防线缺失即 P4 测试不合格**——P4 验收设双轴(正确性 + 性能)硬门,正确性轴里这七层防线全部到位才算守得住「投机错果」。

### 11.2 全显式 guard 的密度天花板(承 03-speculation-ic §3.3 + 旧 §8 风险 2)

**风险**:全显式 guard 的成本若实测吃掉投机收益的大头,f64 快路径的净收益要重新核算。

**测试维度的应对**:微基准分桶 profile(承 §8 + [../../../llmdoc/guides/perf-optimization-workflow.md](../../../llmdoc/guides/perf-optimization-workflow.md) §1)——若 PJ7 amd64 单架构 Horner 距 luajc 档仍远,profile 主导项是 guard 检查指令累积占 30+%,立项 guard 合并(承 [./03-speculation-ic.md](./03-speculation-ic.md) §3.6);**guard 合并测试纪律**:guard 合并必须保持差分 byte-equal(`make test-p4` 全套 + V19 / V22 fuzz 不变)——任何「为优化合并 guard」的提案都要过 byte-equal 门;**若合并后仍达不到 luajc 档**:文档化为已知架构边界(承 §8.5 PW10 P3 一样的收口形式),不强制刷数字。

### 11.3 Go 调度交互的未知数(承 05-system-pipeline §6.3 + 旧 §8 风险 3)

**风险**:长直线段 + 回边检查点粒度不当 ⇒ GC STW 延迟毛刺;检查点过密 ⇒ 吃性能。**Go 调度行为随 Go 版本演进**(非公开契约,wazero 跟进历史是预警源)。

**测试维度的应对**:**回边检查点漏布的差分压力**(§7.3)——多 goroutine 并发 + 长循环脚本 + GC 压力,逼出 Go 异步抢占信号;**跨 Go 版本 nightly 矩阵**——CI 跑 Go 1.25 / 1.26 / latest 各版本的差分套(N-1 + N + tip;Go 官方支持窗口仅最新两个 minor;Go 版本演进时矩阵同步顺移),若 Go 版本升级触发回归立即 issue 跟进;**检查点密度 A/B 实测**——PJ7 端到端验收时按几种检查点密度跑性能基准,**A/B 必过 byte-equal + 性能不更慢双门**。

### 11.4 代码体积与 icache(承旧 §8 风险 4)

**风险**:模板展开比 Wasm/解释器字节码膨胀一个量级,热函数过大时 icache 压力反噬。

**测试维度的应对**:**P2 F5 大函数检查已天然限制编译单元尺寸**(承 [../p2-bridge/03-compilability-analysis.md](../p2-bridge/03-compilability-analysis.md) F5)——P4 沿用,F5 排除的大函数不进 P4;**测试**:大函数边界 fuzz 生成(脚本接近 F5 上限),验差分 byte-equal + 性能 not worse;**icache 监测**(可选,nightly 长跑):用 perf 工具采样 icache miss 率,异常时报警(参考 wazero 一样的实践)。

### 11.5 arm64 维护矩阵(承旧 §8 风险 5 + 06-backends §5.7)

**风险**:双后端 + 双架构 CI 是长期固定成本;arm64 滞后交付不阻塞 P4 验收(验收平台定 amd64),但发布口径须如实标注。

**测试维度的应对**:**应急 CI 矩阵分级**(§6.5)——arm64 build 不破 + V1-V18 不退是最低底线;**arm64 滞后期间的发布口径**(§6.5 表)——如实标注 Beta / Tech Preview;**arm64 GA 条件**——全 PJ 通过 + 双架构 nightly fuzz ≥30 天无差异(承 [./06-backends.md](./06-backends.md) §5.7),这是 V21 在时间维度的兑现。

### 11.6 deopt 注入与 fuzz 资源开销(双架构双跑 + 注入率多档)

**风险**:deopt 注入 fuzz 在双架构都要跑,注入率多档(every=1/3/7/N),fuzz 资源开销翻倍。

**应对**:**PR check 用最严档**(every=1 即每次 deopt,最易撞 V20 风暴防抖)+ short fuzztime(30s);**nightly 跑全档**(every=1/3/7/100,长 fuzztime);**资源紧时按 §6.4 降本策略**——错峰跑 / 周轮换 / release 前再跑全集。

**承 [../../../llmdoc/guides/prove-the-path-under-test.md](../../../llmdoc/guides/prove-the-path-under-test.md) §1**:deopt 注入是必跑的,**不能因为「资源吃紧」就跳过**——跳过等于「P4 测试套有窟窿,投机错果有可能漏过 CI」(P4 测试不合格)。资源调整只能在「跑多久 / 多频繁」上调,不能在「跑不跑」上调。

### 11.7 luajc 档基准的代表性(承 §1.4)

**风险**:luajc 档 164μs 是单一基准(Horner 5 次多项式 1000 items)的实测值,代表性受质疑——若真实负载形式与 Horner 显著不同,luajc 档可能不是合理锚点。

**应对**:**多基准分桶**(§8.3)——loop / table / call / mixed 四桶各自有期望,Horner 是 loop 桶代表不是唯一;**realworld geomean 兜底**(V15)——五脚本几何均值 ≥ P3 geomean ≥ 1.5x,代表性扩到「benchmark game」家族;**真实生产负载抽样**(PJ11 验收阶段,承 [./06-backends.md](./06-backends.md) §6.1 PJ11)——接首个目标宿主真实脚本抽样,实测 P4 加速比;若 luajc 档在该宿主不能兑现端到端可见加速(参考校准测量 2「±5-7% 噪声」),如实记入 implementation-progress + 评估「列内核形状」假设是否对该宿主成立。

---

## 12. 不变式清单

P4 测试与验收的实现期硬性约束,违反即测试不合格:

### 12.1 「投机错果是 JIT 最危险 bug 类,差分主防线优先级最高」

承 §0.1 + §3 + §11.1 + [../roadmap.md](../roadmap.md) §5 原则 2 字面。**任何 PR 让 V1-V13 / V19-V22 任一项破 → 阻断合并**(不可豁免,承 §3.5);**任何「为 P4 加新豁免」的提案 → 回 P1 12 §4 重新论证**(P4 与 crescent 共享 arena/NaN-box/globals,不引入私有不确定性源);**测试覆盖任何缺失(空测 / 静默替身 / 覆盖度自欺)→ 视为 P4 测试不合格**(承 [../../../llmdoc/guides/prove-the-path-under-test.md](../../../llmdoc/guides/prove-the-path-under-test.md) 反模式三档)。

### 12.2 「双架构双跑 = 真 runner CI 硬门禁」

承 §6 + §11.5 + [./06-backends.md](./06-backends.md) §5.3 字面。**CI 必须有真 arm64 物理 runner**——交叉编译只验证「能编」不替代真机;**V21 双架构 byte-equal 是 CI 必过门禁**(amd64 输出 == arm64 输出 == crescent 输出);**arm64 滞后期间发布口径必须如实标注**(Beta / Tech Preview);**arm64 GA 条件:全 PJ 通过 + nightly ≥30 天无差异**(承 §6.5)。

### 12.3 「luajc 档 = 列内核硬约束基准上的水位」

承 §1.1 + §1.4 + §8.2 + [../roadmap.md](../roadmap.md) §1 校准测量 1 字面。**V14 P4 验收锚是绝对水位 ≥164μs**,不是相对 P1 倍率;**基准必须是列内核形状**(自包含热循环 + 已验证可升层 kernel + Horner 校准对位);**per-item 形状下边界成本主导,测不出 P4**——任何「Go 侧 for 循环 + per-item 跨界」基准提案直接判否(承 PW9 空测陷阱教训);**跨机器复测必须同硬件,memory/reflection 写 perf 数字必标硬件 + 参数 + 日期**(承 §8.4)。

### 12.4 「OSR 状态等价 = 每 guard 强制失败 + 续跑 byte-equal」

承 §4 + §5 + §11.6 + [./04-osr-deopt.md](./04-osr-deopt.md) §3.7 + [../../../llmdoc/guides/prove-the-path-under-test.md](../../../llmdoc/guides/prove-the-path-under-test.md) §2(a)/§2(c)。**V19 必须用强制失败构造,不能依赖偶发 deopt**——偶发 deopt 天然稀疏,普通 fuzz 撞不出;**deopt 注入测试入口必须存在**(`SetGuardForceFailFirst` + `SetDeoptInjectEvery`,承 §5.2);**物化序列必须编译期静态生成**(承 [./04-osr-deopt.md](./04-osr-deopt.md) §3.7),review 必须额外检查(V19 通过不能反推无 snapshot);**物化期 GC 触发联合验**(V19 + V13,承 §7.4)是必跑项,不可省略。

### 12.5 「P4 测试套接续 P3 V1-V18,不豁免不替换」

承 §0.2 + §2.1 + §10。**P4 build 下 V1-V13 / V14 / V17-V18 全部继续跑**(P3 翻译 = `WangshuGibbous` runner 与 P4 = `WangshuGibbousJIT` runner 在 P4 build 下同时跑);**P3 V1-V18 在 P4 维度叠加 V19-V22 共 22 条**——P4 验收套是 P3 验收套的延伸;**若 P3 退役**(承 §10),V1-V18 编号保留(承诺连续性),P4 接续承担。

---

## 13. 回填请求(若有)

本节按 [multi-doc-drafting](../../../llmdoc/guides/multi-doc-drafting.md) 协议登记本文起草中发现的、需要 P3 / P2 / P1 现稿增字段或调整的请求。**主助理任务收尾阶段统一兑现,本文先列明细**。

### 13.1 P1 现稿(12-testing-difftest.md)

| # | 文档 | 节 | 内容 | 优先级 |
|---|---|---|---|---|
| RB-1 | [../p1-interpreter/12-testing-difftest.md](../p1-interpreter/12-testing-difftest.md) | §3.8 Runner 抽象表 | 落实 `WangshuGibbousJIT` runner 注释从 `// P3+ 新增,本文预留,不实现` 拆为 P3 已实现 + P4 待实现两行,避免 P4 实现时该注释跨度过广 | 中 |
| RB-2 | [../p1-interpreter/12-testing-difftest.md](../p1-interpreter/12-testing-difftest.md) | §7 P4 行 | 现稿已预留 P4 行(确认 P1 12 §7 表已含 P4 行),建议在 P4 行加链接指向本文 §4 / §5,使 P4 字段从「文字承诺」升级为「具体口径接入」 | 低 |

### 13.2 P3 现稿(08-testing-strategy.md / implementation-progress.md)

| # | 文档 | 节 | 内容 | 优先级 |
|---|---|---|---|---|
| RB-3 | [../p3-wasm-tier/08-testing-strategy.md](../p3-wasm-tier/08-testing-strategy.md) | §0.4 末「P3 退役协议预设位」 | 落实「若 P3 退役,V1-V18 编号保留,P4 接续承担」,本文 §10 已字面化,可在 P3 08 加引用「具体迁移协议见 P4 §8 §10」 | 中 |
| RB-4 | [../p3-wasm-tier/08-testing-strategy.md](../p3-wasm-tier/08-testing-strategy.md) | §4.4 P3 是 P4 投机正确性验证的预演 | 现稿已字面承诺,加引用「P4 视角具体验证形式见 P4 §8 §4 / §5」 | 低 |
| RB-5 | [../p3-wasm-tier/implementation-progress.md](../p3-wasm-tier/implementation-progress.md) | §11 PW9 验收对账 | 加 P4 视角延伸:本文 §3.7 force-all 非空保证援引 RW-10 教训,可在 P3 implementation-progress 加引用「P4 force-all-jit 一样的纪律见 P4 §8 §3.7」 | 低 |

### 13.3 P2 现稿(05-p3-p4-interface.md / 06-testing-strategy.md)

| # | 文档 | 节 | 内容 | 优先级 |
|---|---|---|---|---|
| RB-6 | [../p2-bridge/06-testing-strategy.md](../p2-bridge/06-testing-strategy.md) | §1 P2 验收口径总表 | P2 V1-V22 在 P4 build 下不豁免——本文 §0.2 字面承诺,P2 06 加「P4 build 下 P2 V1-V22 仍跑」纪律 | 中 |
| RB-7 | [../p2-bridge/05-p3-p4-interface.md](../p2-bridge/05-p3-p4-interface.md) | §6 GibbousCode `Run` 返回 status=2 DEOPT | 现稿已预留(`P3 永远不返回 2,P4 才返回 2`),加引用「P4 测试如何验 status=2 路径见 P4 §8 §4(V19 OSR 状态等价)」 | 低 |

### 13.4 P4 子目录其他文档(本子目录内回填)

| # | 文档 | 节 | 内容 | 优先级 |
|---|---|---|---|---|
| RB-8 | [./03-speculation-ic.md](./03-speculation-ic.md) | §3.5「guard 多判 vs 漏判」语义边界 | 现稿已点名「差分主防线见 08-testing-strategy」,本文 §3.4 / §11.1 字面化,可加双向引用 | 已对接 |
| RB-9 | [./04-osr-deopt.md](./04-osr-deopt.md) | §5 deopt 风暴 + §6 exit stub | 现稿 §5.5 给出 deopt 风暴物理学,本文 §5.6 V20 把它翻成具体测试构造,加双向引用 | 已对接 |
| RB-10 | [./06-backends.md](./06-backends.md) | §5 双架构测试纪律 + §6 PJ 里程碑 | 现稿 §5.2 / §6.2 已立 V-J 编号,本文 §2.5 落实 V1-V22 与 PJ 的具体映射;06 加引用「具体口径与 PJ 映射见 P4 §8 §2.5」 | 已对接 |

承 [multi-doc-drafting](../../../llmdoc/guides/multi-doc-drafting.md) 协议:**本文起草仅在 §13 登记回填请求,不主动修改 P3 / P2 / P1 / 其他子目录文档**;所有跨文档同步由主助理收尾时统一处理。

---

## 相关

- [./03-speculation-ic.md](./03-speculation-ic.md)(P4 §3 类型投机机制——§3.5「guard 多判 vs 漏判」是本文 §3.4 / §11.1 主防线物理基础)
- [./04-osr-deopt.md](./04-osr-deopt.md)(P4 §4 OSR exit 协议——§3.7 物化序列编译期静态生成 / §5 deopt 风暴的物理学是本文 §4 / §5 / §12.4 不变式来源)
- [./06-backends.md](./06-backends.md)(P4 §6 双后端——§5 双架构测试纪律 / §6.1 PJ 里程碑是本文 §6 / §2.5 接续上游)
- [../p3-wasm-tier/08-testing-strategy.md](../p3-wasm-tier/08-testing-strategy.md)(**P3 验收单一事实源,本文核心镜像章节**——V1-V18 接续 + force-all 模式 + Runner 抽象 / 风险阶梯设计 / 双轴硬门 全部对偶面)
- [../p1-interpreter/12-testing-difftest.md](../p1-interpreter/12-testing-difftest.md)(P1 差分测试矩阵单一事实源——§3.8 Runner 抽象 / §7 P4 行预留 / §8 CI 门禁)
- [../p2-bridge/06-testing-strategy.md](../p2-bridge/06-testing-strategy.md)(P2 验收单一事实源——P4 build 下 V1-V22 不豁免)
- [../p2-bridge/05-p3-p4-interface.md](../p2-bridge/05-p3-p4-interface.md)(P3/P4 共享前端接口——P4 反向读 feedback 接口的单一事实源 + status=2 DEOPT 出口)
- [../p3-wasm-tier/00-overview.md](../p3-wasm-tier/00-overview.md)(P3 总览,本文 V1-V18 P4 接续的根)
- [../p3-wasm-tier/implementation-progress.md](../p3-wasm-tier/implementation-progress.md)(P3 PW9 验收对账,本文 §3.7 / §1.5 / §8.3 援引)
- [../../../llmdoc/guides/prove-the-path-under-test.md](../../../llmdoc/guides/prove-the-path-under-test.md)(**本文 §3.7 / §4.3 / §5.5 / §11.6 三处反向引用**——P4 是该 guide 的最大用户)
- [../../../llmdoc/guides/perf-optimization-workflow.md](../../../llmdoc/guides/perf-optimization-workflow.md)(perf 判定纪律四件套——本文 §8.4 / §8.5 / §11.7 直接引用)
- [../../../llmdoc/must/design-premises.md](../../../llmdoc/must/design-premises.md)(前提一负载形状 / 前提三五原则——原则 2 是本文 §0 / §11.1 主防线最高上游)
- [../../../llmdoc/architecture/evolution-roadmap.md](../../../llmdoc/architecture/evolution-roadmap.md)(坐标系警告——流水线图倍率 vs 阶段验收门槛不同坐标系,本文 §1.3)
- [../roadmap.md](../roadmap.md)(§1 校准测量 luajc 164μs / §4 P4 验收 / §5 原则 2 投机错果是 JIT 最危险 bug 类)
- [../architecture.md](../architecture.md)(§4 不变式 2「层间逐字节差分是 CI 必过门禁」是本文 §3.5 / §6 / §12 的最高纲领)
- [../../../Makefile](../../../Makefile)(make all-p4 入口,§9.1 四套 build tag)
- [../../../benchmarks/realworld](../../../benchmarks/realworld/)(P4 性能门实际脚本,§8.1 / §8.6 luajc 档基准)
- [../../../benchmarks/calibration](../../../benchmarks/calibration/)(校准测量基准,§8.6 LuaJIT 直接对位)
