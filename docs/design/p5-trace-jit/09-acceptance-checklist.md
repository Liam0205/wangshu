# P5 §9:验收清单——v1/v2/v3 内部阶段验收 + T1-T? 验证矩阵 + 证据台账

> 状态:**未立项,验收清单形式占位**。本文与 [../p4-method-jit/09-acceptance-checklist.md](../p4-method-jit/09-acceptance-checklist.md) 在形式上平行,但 P5 未启动之前所有验收项停留在「验收准则 + 编号占位 + 空证据表」状态;立项通过并按 [./implementation-progress.md](./implementation-progress.md) §1 PT 分档施工之后,证据表逐档填入实测数据。
>
> 对应 Go 包:未立项前不新建;如果立项则是 `internal/fullmoon/trace`(见 [./00-overview.md](./00-overview.md) §1)。
>
> 上游依据:[../roadmap.md](../roadmap.md)(§4 P5 定义、§0 终局目标「列内核 10-30x over gopher-lua」);[../../../llmdoc/must/design-premises.md](../../../llmdoc/must/design-premises.md)(前提一列内核形式 + 前提三五条原则,尤其是原则 3 递归);[../p1-interpreter/12-testing-difftest.md](../p1-interpreter/12-testing-difftest.md)(§6.1 列内核基准硬约束、§7 P5 行预留、§8 长时间 fuzz)。
>
> 同 P5 目录依赖:[./00-overview.md](./00-overview.md)(章节地图 + 复用清单);[./01-launch-judgment.md](./01-launch-judgment.md)(§5.4 v1-v3 阶段验收预览 + §6.1 P4 验收数据 baseline);[./08-testing-strategy.md](./08-testing-strategy.md)(测试机制;本文引用其 T1-T? 编号,不做定义);[./implementation-progress.md](./implementation-progress.md)(PT0-PT9 施工分档与本文 v1-v3 阶段验收的映射;§1 定义)。
>
> P4 承接面:[../p4-method-jit/09-acceptance-checklist.md](../p4-method-jit/09-acceptance-checklist.md)(V1-V22 编号已经被 P4 占用,P5 用 T1-T? 避免冲突,见 §3 编号约定);[../p4-method-jit/08-testing-strategy.md](../p4-method-jit/08-testing-strategy.md)(V-编号来源)。
>
> 本文定位一句话:**P5 的验收由「外部终局目标(10-30x over gopher-lua)+ 内部立项承诺(§1.2 打败 P4 的目标负载)」双锚定,分 v1/v2/v3 三档独立可停,T1-T? 验证矩阵覆盖每档实际达标的证据**。

---

## 1. 验收准则

### 1.1 外部锚:终局目标(见 [../roadmap.md](../roadmap.md) §4)

**列内核负载 10-30x over gopher-lua**([../roadmap.md](../roadmap.md) §4 P5 验收行),基准标准依据 [../p1-interpreter/12-testing-difftest.md](../p1-interpreter/12-testing-difftest.md) §6.1 列内核硬约束(一次 Call 进 VM 整批迭代;N ≥ 1000)。

- **下沿 10x**——P5 v1 最低验收线,承接 [./00-overview.md](./00-overview.md) §5.1 风险 3「纯 Go 全部显式 guard 对 trace 收益的折损」,使得 10x 可能是实际上沿而不是下沿;
- **上沿 30x**——按 [../roadmap.md](../roadmap.md) §4 的目标,只有分配下沉 / 逃逸分析 + side trace 树全套(v2 + v3)交付之后才有物理可能达到。

10-30x 是**宽带区间,不是精确锚**(见 [./01-launch-judgment.md](./01-launch-judgment.md) §8.3 不变式)——判定「验收 pass」的下沿是 10x,并不是「必须到 30x 上沿」。

### 1.2 内部锚:实现立项理由(见 [./01-launch-judgment.md](./01-launch-judgment.md) §2)

P5 立项判定 §2.1 列出了 P4 结构上吃不下的四类负载,P5 立项 = 承诺实现这些负载上的显著加速。所以 P5 除了外部锚之外,**必须**在这四类负载上**显著**优于 P4,否则 P5 就失败了自己的立项理由,即使 10-30x 达标也不能视为验收 pass:

| P5 目标负载类别 | 内部验收要求 |
|---|---|
| **跨函数热循环** | P5/P4 加速比 **≥ 2x** 在选定的宿主真实负载子集上——trace 内联跨函数边界的核心收益 |
| **循环内的冗余** | P5/P4 加速比 **≥ 1.5x**——CSE / LICM 应该能吃下 P4 每轮重算的冗余 |
| **分配密集的循环** | P5/P4 加速比 **≥ 2x**(只在 v2 阶段内计算)——分配下沉是 v2 的实现载体 |
| **megamorphic 稳定子集** | P5/P4 加速比 **≥ 1.5x**——trace 按实际路径特化,把 P2 判为「多态不投机」的调用点拆成多条单态 trace |

**具体阈值(1.5x / 2x)是占位**,预登记时按 P4 verified 基线校准(见 [./01-launch-judgment.md](./01-launch-judgment.md) §3.2)。如果 P5 在这四类负载上没有显著优于 P4,则 P5 立项理由 §2.1 没有实现,判 fail。

### 1.3 两个锚的关系

**外部锚是必要条件,内部锚是充分条件**:

- 外部锚未达 = 项目终局目标没有实现——直接 fail;
- 外部锚达 + 内部锚未达 = P5 达到了列内核标准形式,但没有实现立项理由(P4 目标负载还是 P4 更快)——**仍然判 fail**,因为如果列内核标准形式 P5/P4 比拼下 P4 已经够了,P5 立项本身就不应该通过;
- 外部锚达 + 内部锚达 = pass。

---

## 2. 分档验收:v1 / v2 / v3 内部阶段

### 2.1 三档定义(见 [./01-launch-judgment.md](./01-launch-judgment.md) §5.4)

原则 3 的递归运用——P5 立项之后仍然分三档,每档独立可停(见 [./00-overview.md](./00-overview.md) §5.1 风险 2):

| 档 | 内容 | 主要交付物 | 独立可停条件 |
|---|---|---|---|
| **v1** | 录制 + 基础优化(FOLD / CSE / DCE / guard-dedup)+ regalloc + snapshot(不含 sink)+ loop peeling / LICM | 完整跑通 loop trace 端到端;§1.2 前两类负载实现;§1.1 外部锚下沿 10x 实现 | 如果 v1 达标后 §2.1 类别 3 分配密集负载份额不足以支撑 v2 投入,可以停 |
| **v2** | 分配下沉 / 逃逸分析 | §1.2 类别 3 分配密集负载实现 | v2 是类别 3 的对应手段;如果真实负载中该类份额小(§1.2 阈值不成立),可以停 |
| **v3** | side trace 树(热 side exit 继续录 side trace) | 复杂控制流路径的覆盖 + trace 树生长与回收 | v3 处理已录 trace 的热 side exit;如果 v1 / v2 之后 side exit 分布均匀没有热点,可以停 |

### 2.2 每档的验收项(独立勾选)

**v1 阶段验收项**:

- [ ] **v1-A**:PT0 spike 通过——最小 loop trace 端到端跑通(见 [./implementation-progress.md](./implementation-progress.md) §1 PT0),与解释器 byte-equal + 单次 guard-fail deopt 完成;
- [ ] **v1-B**:§1.1 外部锚下沿达标——列内核基准 P5 ≥ 10x over gopher-lua;
- [ ] **v1-C**:§1.2 类别 1「跨函数热循环」内部锚达标——P5/P4 ≥ 2x;
- [ ] **v1-D**:§1.2 类别 2「循环内的冗余」内部锚达标——P5/P4 ≥ 1.5x;
- [ ] **v1-E**:§3 T1-T? 验证矩阵全部通过(每 T 项独立勾选);
- [ ] **v1-F**:V17-V18 无回归——P4 build 下 V1-V22 全部通过(见 [../p4-method-jit/09-acceptance-checklist.md](../p4-method-jit/09-acceptance-checklist.md) §1);
- [ ] **v1-G**:双 arch(amd64 + arm64)v1-A..v1-F 全部通过。

**v2 阶段验收项**(v1 达标之后独立判定启动):

- [ ] **v2-A**:PT7 分配下沉交付(见 [./implementation-progress.md](./implementation-progress.md) §1 PT7),unsink 路径 byte-equal 解释器;
- [ ] **v2-B**:§1.2 类别 3「分配密集的循环」内部锚达标——P5/P4 ≥ 2x;
- [ ] **v2-C**:sunk 对象与自管 GC 的交互 fuzz 无 UAF、无根泄漏(见 [./08-testing-strategy.md](./08-testing-strategy.md) T? 编号);
- [ ] **v2-D**:v1 各项无回归(v2 pass 集加入后 v1 test suite 仍然全绿);
- [ ] **v2-E**:双 arch v2-A..v2-D 全部通过。

**v3 阶段验收项**(v2 达标之后独立判定启动):

- [ ] **v3-A**:PT8 side trace 树交付(见 [./implementation-progress.md](./implementation-progress.md) §1 PT8),side trace 独立热度追踪 + 从 side exit 起点继续录闭环;
- [ ] **v3-B**:§1.1 外部锚上沿逼近——列内核基准 P5 加速比逼近 30x(或达到 v3 独立预登记的上沿目标);
- [ ] **v3-C**:trace 树回收正确性(黑名单 + 冷 trace 释放 mmap 页)fuzz 无内存泄漏;
- [ ] **v3-D**:v1 + v2 各项无回归;
- [ ] **v3-E**:双 arch v3-A..v3-D 全部通过。

### 2.3 每档停下的合法产出

原则 3 的字面实现——每档停下不亏,需要给出:

- 停止时点的**归档报告**(落到 [../../../llmdoc/memory/decisions/](../../../llmdoc/memory/decisions/));
- 已交付子档的**永久保留**——v1 停在 v1 意味着 P5 v1 就是 wangshu 的当前形式(仍然是 P4 之上的加速层,只是不含 sink 和 side trace);
- 剩余档位的**再启动判定条件**(什么条件下重新评估 v2 / v3 立项)。

---

## 3. 验证矩阵:T1-T? 编号约定

### 3.1 为什么用 T 编号(避免与 P4 V 编号冲突)

[../p4-method-jit/09-acceptance-checklist.md](../p4-method-jit/09-acceptance-checklist.md) 已经用 V1-V22 编号覆盖 P4 全套验收;P5 如果沿用 V 编号需要从 V23 开始,但会造成:

- P5 验收项被理解为「P4 增项」,与 P5 是独立 tier 的语义不符;
- 跨文档 grep 时 V 编号池扩到 P4+P5 混杂。

所以 P5 用 **T1-T?** 编号(T = Trace),与 P4 V1-V22 平行,独立池。具体 T 编号在 [./08-testing-strategy.md](./08-testing-strategy.md) 定义,本文只引用,不重复定义。

### 3.2 T 编号预定义(占位,细节由 [./08-testing-strategy.md](./08-testing-strategy.md) 承接)

以下 T 编号占位在本文的验收矩阵中出现,具体机制细节看 [./08-testing-strategy.md](./08-testing-strategy.md):

| T # | 描述 | v1 | v2 | v3 |
|---|---|---|---|---|
| **T1** | 三层差分测试:crescent vs gibbous(P3 或 P4)vs fullmoon 两两 byte-equal | ✅ | ✅ | ✅ |
| **T2** | 四层差分测试(如果 P3 仍保留):crescent vs gibbous-wasm vs gibbous-jit vs fullmoon 两两 byte-equal;见 [./00-overview.md](./00-overview.md) §3.3 P3 主动保留 | ✅ | ✅ | ✅ |
| **T3** | deopt 注入:每个 guard 强制失败模式,side exit + snapshot 恢复路径全部逐条真正执行到;恢复后输出与一路解释 byte-equal | ✅ | ✅ | ✅ |
| **T4** | pass-toggle 分级差分:按 pass 开关组合跑(只录不优化 / +FOLD / +CSE / +LICM / +sink / +side-trace),把「哪个 pass 引入错误结果」定位成一阶问题 | ✅ | ✅ | ✅ |
| **T5** | fuzz 时长下限(v1 目标 nightly ≥ 4 CPU-hour;v2 ≥ 8;v3 ≥ 16)——见 [./00-overview.md](./00-overview.md) §5.1 风险 2「正确性置信度是 fuzz 时长的函数」 | ≥4h | ≥8h | ≥16h |
| **T6** | perf 检查:每阶段内部锚 §1.2 阈值 + 外部锚 §1.1 下沿(v1)/ 逼近上沿(v3) | 见 §2.2 | 见 §2.2 | 见 §2.2 |
| **T7** | 无回归:P1 V1-V18 + P2 verified + P3 / P4 各自 V-编号全套在 P5 build 下不豁免 | ✅ | ✅ | ✅ |
| **T8** | GC 压力差分:自管 mark-sweep 高频触发下 P5 输出与解释器 byte-equal(见 [../p1-interpreter/12-testing-difftest.md](../p1-interpreter/12-testing-difftest.md) §? gcstress);v2 特别加码 sunk 对象根扫描 | ✅ | +sunk | +sunk |
| **T9** | trace 录制稳定性 fuzz:随机输入触发 NYI / abort / 黑名单,不崩溃,并且 fallback 到解释器 byte-equal | ✅ | ✅ | ✅ |
| **T10** | 双 arch 跨平台 byte-equal:amd64 与 arm64 输出相同,和 P4 双架构 CI 双跑同样的纪律(见 [../p4-method-jit/06-backends.md](../p4-method-jit/06-backends.md) §5) | ✅ | ✅ | ✅ |
| **T11** | 协程不 trace 强制检查:trace 触及 coroutine yield / resume 时录制 abort,进入黑名单(见 [./00-overview.md](./00-overview.md) §5.2 开放问题) | ✅ | ✅ | ✅ |
| **T12** | trace 树生长与回收(仅 v3):trace 数量上限达到时冷 trace 释放策略正确——fuzz 长时间运行无内存泄漏 | — | — | ✅ |

**T 编号池目前预留 T1-T12**,具体机制、触发方式、与 CI job 的对应关系见 [./08-testing-strategy.md](./08-testing-strategy.md)。如果 [./08-testing-strategy.md](./08-testing-strategy.md) 定稿时需要更细的颗粒度分档(比如 T3a / T3b),按其定稿为准,本文 §3.2 表随后同步。

### 3.3 无回归纪律(见 [../p4-method-jit/08-testing-strategy.md](../p4-method-jit/08-testing-strategy.md) §0.2 相同)

P5 build 下 P1 / P2 / P3 / P4 各自 V-编号全套 test 不豁免——按照 P4 build 下 P2 V1-V22 不豁免同样的纪律(见 [../p4-method-jit/08-testing-strategy.md](../p4-method-jit/08-testing-strategy.md) §0.2 + RJ-13 实现)。P5 加入后:

- 项目 CI 矩阵扩到 `{p1, p3, p4, p5} × {ubuntu-latest, ubuntu-24.04-arm, macos-latest}` 共 12 job;
- 每个 P5 阶段(v1/v2/v3)通过条件必须包含 P1/P2/P3/P4 全套无回归;
- 无回归 = 逐字节 byte-equal + 性能不劣化(P5 build 加入之后不能让 P1-P4 测试跑得更慢)。

---

## 4. 证据台账(等运行时填入)

### 4.1 台账形式

镜像 [../p4-method-jit/09-acceptance-checklist.md](../p4-method-jit/09-acceptance-checklist.md) §3 「性能数字归档」的形式,证据表**在 P5 立项 + 施工 + 验收运行时才逐行填入**。P5 未立项前所有表都是空的——空表本身就是「未立项」这一事实的显性化,不是缺陷。

### 4.2 v1 阶段证据台账(占位)

**v1 性能证据(§1.1 外部锚)——等运行时填入**:

| 日期 | run 编号 | 平台 | 基准脚本 | gopher-lua (μs/op) | P4 (μs/op) | **P5 v1 (μs/op)** | P5/gopher | P5/P4 | 达标 ≥10x? |
|---|---|---|---|---|---|---|---|---|---|
| — | — | ubuntu-latest | horner_1000 | — | — | — | — | — | — |
| — | — | ubuntu-24.04-arm | horner_1000 | — | — | — | — | — | — |
| — | — | macos-latest | horner_1000 | — | — | — | — | — | — |

**v1 内部锚证据(§1.2 类别 1 + 2)——等运行时填入**:

| 日期 | run | 平台 | 类别 1 负载(跨函数热循环) | P4 (μs/op) | P5 (μs/op) | P5/P4 | 达标 ≥2x? |
|---|---|---|---|---|---|---|---|
| — | — | — | — | — | — | — | — |

| 日期 | run | 平台 | 类别 2 负载(循环内的冗余) | P4 (μs/op) | P5 (μs/op) | P5/P4 | 达标 ≥1.5x? |
|---|---|---|---|---|---|---|---|
| — | — | — | — | — | — | — | — |

**v1 T-项证据(T1-T11)——等运行时填入**:

| T # | 平台 amd64 | 平台 linux/arm64 | 平台 darwin/arm64 | 证据 CI run / test path |
|---|---|---|---|---|
| T1 | ⬜ | ⬜ | ⬜ | — |
| T2 | ⬜ | ⬜ | ⬜ | — |
| T3 | ⬜ | ⬜ | ⬜ | — |
| T4 | ⬜ | ⬜ | ⬜ | — |
| T5 | ⬜ | ⬜ | ⬜ | — |
| T6 | ⬜ | ⬜ | ⬜ | — |
| T7 | ⬜ | ⬜ | ⬜ | — |
| T8 | ⬜ | ⬜ | ⬜ | — |
| T9 | ⬜ | ⬜ | ⬜ | — |
| T10 | ⬜ | ⬜ | ⬜ | — |
| T11 | ⬜ | ⬜ | ⬜ | — |

### 4.3 v2 阶段证据台账(占位)

**v2 内部锚证据(§1.2 类别 3)——等 v1 达标之后启动**:

| 日期 | run | 平台 | 类别 3 负载(分配密集的循环) | P4 (μs/op) | P5 v2 (μs/op) | P5/P4 | 达标 ≥2x? |
|---|---|---|---|---|---|---|---|
| — | — | — | — | — | — | — | — |

**v2 T-项证据(T3 sunk / T8 sunk / T4 +sink pass 分档 / 其他 T)——等 v1 达标之后启动**:

| T # | 平台 amd64 | 平台 linux/arm64 | 平台 darwin/arm64 | 证据 CI run |
|---|---|---|---|---|
| T3 (+sunk) | ⬜ | ⬜ | ⬜ | — |
| T8 (+sunk) | ⬜ | ⬜ | ⬜ | — |
| T4 (+sink) | ⬜ | ⬜ | ⬜ | — |

### 4.4 v3 阶段证据台账(占位)

**v3 外部锚上沿证据(§1.1 逼近 30x)——等 v2 达标之后启动**:

| 日期 | run | 平台 | 基准 | gopher (μs/op) | P5 v3 (μs/op) | P5/gopher | 逼近 30x? |
|---|---|---|---|---|---|---|---|
| — | — | — | — | — | — | — | — |

**v3 类别 4 内部锚证据(§1.2 megamorphic 稳定子集)——等 v2 达标之后启动**:

| 日期 | run | 平台 | 类别 4 负载 | P4 (μs/op) | P5 v3 (μs/op) | P5/P4 | 达标 ≥1.5x? |
|---|---|---|---|---|---|---|---|
| — | — | — | — | — | — | — | — |

**v3 T-项证据(T12 trace 树 + 其他)——等 v2 达标之后启动**:

| T # | 平台 amd64 | 平台 linux/arm64 | 平台 darwin/arm64 | 证据 CI run |
|---|---|---|---|---|
| T12 | ⬜ | ⬜ | ⬜ | — |

### 4.5 硬件、参数、日期标注纪律

按 [../../../llmdoc/guides/perf-optimization-workflow.md](../../../llmdoc/guides/perf-optimization-workflow.md) §5「跨机器基线对照」+ [../p4-method-jit/01-launch-judgment.md](../p4-method-jit/01-launch-judgment.md) §5.4「写在前面」的纪律,证据表填入时**必须**标注:

- **硬件型号** + 内核版本(CPU、机器类型、RAM 决定 bench 波动);
- **基准参数**(`-benchtime`、`-count`、锁频 / 绑核状态 / go version);
- **测量日期**(CI run 或本机复测的具体时点);
- **build tag 组合**(`wangshu_p5`、`wangshu_profile`、`wangshu_p4` 是否共存)。

**跨机器跨日的数据不可比**——如果 v1 amd64 阶段数据在 X 硬件上填入,v2 amd64 阶段数据在 Y 硬件上填入,需要在 v2 表加脚注说明「与 v1 hardware 不同」,避免以后误比 v1/v2 数字。

---

## 5. 验收流程

### 5.1 v1 阶段流程

1. **PT 施工完成**([./implementation-progress.md](./implementation-progress.md) §1 PT0..PT6)——承认 v1 code base 就绪;
2. **CI 全绿**——T1-T11 在 CI 三平台矩阵全过,证据自动填入 §4.2 T-项证据表;
3. **perf 基准运行**——bench-acceptance workflow 跑 v1 完整套(§1.1 外部 + §1.1 内部),数字填入 §4.2 表;
4. **判定会**——主助理 + 用户对 §2.2 v1-A..v1-G 逐项勾选;有未勾的项按原则 3 决定「继续调优 v1」or「v1 停止」or「回补数据再判」;
5. **归档**——v1 阶段通过或停止,决策报告落到 [../../../llmdoc/memory/decisions/](../../../llmdoc/memory/decisions/),证据表冻结版本。

v1 通过之后 v2 立项启动判定重新走一次(v2 是「续期方案」,不因 v1 通过而自动启动);v1 停止则 P5 进入「v1-only」形式永久保留。

### 5.2 v2 / v3 阶段流程

v2 / v3 分别镜像 §5.1 的流程,每档独立判定。**v3 达标即 P5 全套完成**,项目终局目标实现,可以考虑对外发布「wangshu v1.0」型号(具体版本策略立项时决定)。

---

## 6. 与 P4 验收数据的兼容读法

见 [./01-launch-judgment.md](./01-launch-judgment.md) §6.1,P4 验收数据是 P5 立项的 baseline;本节说明 P5 验收数据出台之后如何与 P4 数据兼容对读:

- **P5 数字不撤 P4 数字**——项目 README perf table 应该展示 P4 与 P5 并列(见 [../p4-method-jit/09-acceptance-checklist.md](../p4-method-jit/09-acceptance-checklist.md) §3.7 addendum「权威现值 vs 验收时点快照」同样的纪律);
- **P4 已知损失(§6.2 issue #39/#40)在 P5 上是否复现**——如果 P5 v1 阶段运行时 issue #40 arm64 P4 回归还没闭合,P5 arm64 数据与 P4 arm64 数据的对比读法需要显式声明基线;
- **P3 主动保留(D2 决议)对 P5 的影响**——如果首个宿主选 P3 wasm build(iOS / seccomp 场景),P5 无法接管;P5 验收只在 P4 + P5 build 上进行,P3-only build 保留 P4 之前的状态。

---

## 7. 开放问题

- v1 / v2 / v3 每档「独立可停」的量化触发条件(§2.1 表右列的「份额小 / 不足以支撑投入」如何定量)——立项通过之后设计定稿时决定;
- T 编号最终清单(§3.2 表现 T1-T12 是占位,实际的 T # 由 [./08-testing-strategy.md](./08-testing-strategy.md) 定稿时可能拆分或合并)——[./08-testing-strategy.md](./08-testing-strategy.md) 定稿时同步;
- T5 fuzz 时长下限的具体数字(4h / 8h / 16h 是占位)——立项后按机器成本预算校准;
- 「10x 下沿的验收 pass」是否可以对外声明「P5 v1 stable」——需要与项目版本策略协调,立项通过之后决定;
- **coroutine 与 trace 的关系**——见 [./00-overview.md](./00-overview.md) §5.2 开放问题「LuaJIT 选择 trace 不跨 yield,望舒大概率也一样」,T11 就是这条决策的验收落点;如果立项之后决定放开 coroutine trace,T11 内容需要重新定义。

---

## 相关

- [./00-overview.md](./00-overview.md)(P5 总览,§4 章节地图 + §6 施工前置条件)
- [./01-launch-judgment.md](./01-launch-judgment.md)(§5.4 v1-v3 阶段验收预览 + §6.1 P4 验收数据 baseline)
- [./08-testing-strategy.md](./08-testing-strategy.md)(T1-T12 编号 + 差分套 + deopt 注入 + pass 分级 + 持续 fuzz 机制)
- [./implementation-progress.md](./implementation-progress.md)(PT0-PT9 施工分档;每 PT 交付时对应本文哪个 v-阶段)
- [../roadmap.md](../roadmap.md)(§4 P5 定义、§0 终局目标「10-30x over gopher-lua」)
- [../../../llmdoc/must/design-premises.md](../../../llmdoc/must/design-premises.md)(前提一列内核形式 / 前提三五条原则 / 原则 3 递归)
- [../../../llmdoc/architecture/evolution-roadmap.md](../../../llmdoc/architecture/evolution-roadmap.md)(P5 速查行、启动条件)
- [../../../llmdoc/guides/perf-optimization-workflow.md](../../../llmdoc/guides/perf-optimization-workflow.md)(§5 跨机器基线对照——§4.5 硬件标注纪律同源)
- [../p4-method-jit/09-acceptance-checklist.md](../p4-method-jit/09-acceptance-checklist.md)(V1-V22 P4 验收清单,§3 归档模式参照)
- [../p4-method-jit/08-testing-strategy.md](../p4-method-jit/08-testing-strategy.md)(V-编号定义,§0.2「build 下不豁免」同样的纪律)
- [../p4-method-jit/06-backends.md](../p4-method-jit/06-backends.md)(§5 双 arch CI 双跑——T10 同源)
- [../p1-interpreter/12-testing-difftest.md](../p1-interpreter/12-testing-difftest.md)(§3.8 fullmoon runner 预留 / §6.1 列内核硬约束 / §7 P5 行 / §8 长时间 fuzz)
