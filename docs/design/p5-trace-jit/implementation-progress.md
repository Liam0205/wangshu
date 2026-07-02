# P5 实现进度对账(implementation-progress)

> 状态:**未启动。P5 是一个备选方案,不是既定计划**。P5 是五个阶段里唯一的「开放式」阶段,截至本文创建时没有任何代码交付,`internal/fullmoon/trace` 包还没有新建。启动的前提条件见 [./01-launch-judgment.md](./01-launch-judgment.md);启动之后按 §1 施工分档 PT0-PT9 展开。
>
> 对应 Go 包:未启动前不新建;如果立项则是 `internal/fullmoon/trace`(见 [./00-overview.md](./00-overview.md) §1 + [../architecture.md](../architecture.md) §1 包布局)。
>
> 上游依据:[../roadmap.md](../roadmap.md)(§4 P5 定义 +2-4 人年做到可信 v1、开放式);[../../../llmdoc/architecture/evolution-roadmap.md](../../../llmdoc/architecture/evolution-roadmap.md)(P5 速查行:仅在 P4 收益不够时启动);[../../../llmdoc/must/design-premises.md](../../../llmdoc/must/design-premises.md)(前提三原则 3 递归——PT 分档每档独立可停)。
>
> 同 P5 目录依赖:[./00-overview.md](./00-overview.md)(§4 章节地图 / §5 风险与开放问题索引 / §6 施工前置条件);[./01-launch-judgment.md](./01-launch-judgment.md)(§1 三条并集立项条件、§5.4 v1-v3 阶段验收预览);[./09-acceptance-checklist.md](./09-acceptance-checklist.md)(v1-v3 阶段的具体验收项 / T1-T12 验证矩阵 / 证据台账);[./02-trace-recording.md](./02-trace-recording.md) 到 [./08-testing-strategy.md](./08-testing-strategy.md)(具体施工内容的承接章节)。
>
> P4 承接面:[../p4-method-jit/implementation-progress.md](../p4-method-jit/implementation-progress.md)(P4 PJ0-PJ11 全交付,本文模式参照);[../p4-method-jit/09-acceptance-checklist.md](../p4-method-jit/09-acceptance-checklist.md)(P4 验收数据是 P5 的 baseline)。
>
> 单一事实源:本文承载「P5 施工分档与开放问题合并台账」,不承载「P5 该不该做」(那是 [./01-launch-judgment.md](./01-launch-judgment.md))、也不承载「P5 验收怎么验」(那是 [./09-acceptance-checklist.md](./09-acceptance-checklist.md))。
>
> **术语**:`PT`(P-Trace)= P5 实现里程碑编号,对应 P1 的 M、P2 的 PB、P3 的 PW、P4 的 PJ;PT0 = spike 阶段验收,PT1-PT9 = 施工分档。

---

## 0. 当前状态

**P5 未启动**——没有任何代码,`internal/fullmoon/trace` 包不存在。

### 0.1 启动前置条件对账(见 [./00-overview.md](./00-overview.md) §6)

| 前置 | 状态 |
|---|---|
| [./01-launch-judgment.md](./01-launch-judgment.md) §1 三条并集条件同时满足 + 档位决议归档 [../../../llmdoc/memory/decisions/](../../../llmdoc/memory/decisions/) | ⏳ 未开始判定(P4 已经在 2026-07-01 交付,判定可以启动但还没启动) |
| P4 amd64 + arm64 双 arch 都达到 luajc 档并且性能归档 | 🟡 amd64 已达标(V14 14.08x);arm64 分岔中(issue #37 exit-reason 端口 + issue #40 arm64 P4 HeavyArith 回归未闭) |
| 首个目标宿主的真实热脚本 profile 到位 | ⏳ 等宿主侧提供 |
| Cheaper alternatives 已经系统评估(§4)——stdlib 内建化、P4 peephole 扩展、宿主侧改造、P4 op-set 扩展,这四条至少已经尝试三条 | ⏳ 未开始评估 |
| +2-4 人年的人力预算 + fuzz 集群资源到位 | ⏳ 未预算 |

**结论**:目前 P5 立项判定的**前置条件本身还没齐备**(宿主侧 profile 未到 + P4 arm64 未闭 + cheaper alternatives 未评估)——所以 P5 立项判定不能启动,更不能施工。

### 0.2 P5 立项凭据归档点

如果将来 P5 立项判定启动,凭据归档采用与 P4 同样的模式(见 [../p4-method-jit/01-launch-judgment.md](../p4-method-jit/01-launch-judgment.md) §5.3):

| 凭据 | 归档点 |
|---|---|
| §3.2 A/B/C 预登记表 | [../../../llmdoc/memory/decisions/](../../../llmdoc/memory/decisions/)`p5-launch-preregistration-YYYY-MM-DD.md` |
| 判定档位决议(立项 / 推迟 / 不立项) | 本文 §3 change log + 上述 decisions 目录 |
| 目标负载集选定 + 宿主 profile 数据附件 | 上述 decisions 目录 |
| Cheaper alternatives 四条评估报告 | 上述 decisions 目录 |
| v1 / v2 / v3 每阶段通过或停止的决策报告 | 上述 decisions 目录 |

---

## 1. 施工分档(PT0-PT9)

### 1.1 PT 编号总览

**如果 P5 立项通过**,施工按以下分档进行。每 PT 都可以独立编译 + 单测通过再进下一步。**PT 分档与 [./09-acceptance-checklist.md](./09-acceptance-checklist.md) §2 v1-v3 阶段的映射**(见 [./09-acceptance-checklist.md](./09-acceptance-checklist.md) §2 定义):

- **v1 = PT0..PT6**(录制 + 基础优化 + regalloc + snapshot + loop peeling / LICM;不含 sink 和 side trace 树);
- **v2 = PT7**(分配下沉 / 逃逸分析);
- **v3 = PT8**(side trace 树)+ PT9(全套验收调优)。

**PT 顺序纪律**:PT8(side trace 树)排在 PT7(sink)之后——按 [./09-acceptance-checklist.md](./09-acceptance-checklist.md) §2.1 三档定义(v1 = 录制 + 基础优化 + regalloc + snapshot,不含 sink;v2 = sink / 逃逸;v3 = side trace 树)——PT 编号顺序遵从 v-阶段顺序,确保 PT 施工序列与阶段验收序列自然对齐。

### 1.2 PT 逐项表

| PT | 内容 | 目标 | 交付物 | 验收 | 依赖章节 | 预估规模 | v-阶段映射 |
|---|---|---|---|---|---|---|---|
| **PT0** | Spike 阶段验收:最小端到端 trace 打通 | 证明「trace 录制 + 编译 + 执行 + guard-fail deopt」全链路物理上可行(参照 [../p3-wasm-tier/01-spike-gate.md](../p3-wasm-tier/01-spike-gate.md) 的精神) | 一条最简单的数值循环 trace,在 crescent 内录制、经复用的 P4 codebuf/encoders 编译、mmap RX 执行、一次 guard-fail 走 exit-reason 协议物化 → 与解释器逐字节一致 | 端到端 byte-equal + deopt 之后续跑 byte-equal + 复核 spike 打通没有留下隐性依赖(见 [../p3-wasm-tier/01-spike-gate.md](../p3-wasm-tier/01-spike-gate.md) §0.2 spike 阶段纪律)| [./00-overview.md](./00-overview.md) §3 复用清单 / [./02-trace-recording.md](./02-trace-recording.md) 最小录制原型 / [./07-system-integration.md](./07-system-integration.md) codebuf 复用 | 0.5-1 人月(go/no-go 质量阶段验收,参照 [../p3-wasm-tier/01-spike-gate.md](../p3-wasm-tier/01-spike-gate.md) 先例) | v1 起点 |
| **PT1** | Recorder 骨架 + NYI / abort / 黑名单 | 解释器内嵌录制模式;录制过程中遇到 NYI(varargs、coroutine yield、debug、特殊 metamethod 等)就丢弃 trace 并记入黑名单 | recorder 状态机 + 录制期 IR trace 缓冲 + 三种终止路径(闭环 loop trace / 长度超限 abort / NYI abort);黑名单集持久化 | fuzz 撞随机脚本,录制不崩溃,fallback 到解释器仍然 byte-equal([./09-acceptance-checklist.md](./09-acceptance-checklist.md) T9)| [./02-trace-recording.md](./02-trace-recording.md) §? NYI 清单 + 黑名单 + 长度上限 | 1-2 人月 | v1 |
| **PT2** | IR + folding 引擎 | 定义 SSA IR 骨架:类型系统、opcode 分层(算术 / load-store / guard / call / meta)、常量折叠 pass | IR 定义包 + `builder` API + folding pass v0(f64 常量算术折叠、NaN-box tag guard 折叠)| pass-toggle 差分测试:只录 + folding 与只录无 folding byte-equal([./09-acceptance-checklist.md](./09-acceptance-checklist.md) T4)| [./03-ir-design.md](./03-ir-design.md) 全篇 | 2-3 人月(设计定案 + 实现) | v1 |
| **PT3** | 基础 pass:CSE / DCE / guard-dedup | 循环内冗余计算消除;死代码消除;同操作数 guard 沿 trace 去重 | 三个 pass 独立开关,pass 管道装配 | pass-toggle 差分测试:每个 pass 独立开 / 关组合下 byte-equal([./09-acceptance-checklist.md](./09-acceptance-checklist.md) T4);perf 微基准显示 pass 收益 | [./04-optimization-passes.md](./04-optimization-passes.md) §CSE / §DCE / §guard-dedup | 1-2 人月 | v1 |
| **PT4** | 逆序线性扫描 regalloc | 在线性 trace 上单遍逆序扫描分配 IR 值到机器寄存器;spill 策略;callee-save 对齐 P4 encoders 的 ABI | regalloc pass + spill 表 + 与 [./05-register-allocation.md](./05-register-allocation.md) §? snapshot 耦合协议对接 | v1 subset 端到端 byte-equal;寄存器分配无冲突;spill 数量合理不飙升(perf 微基准验证)| [./05-register-allocation.md](./05-register-allocation.md) 全篇 | 2-3 人月(P5 的第一个真正的硬骨头) | v1 |
| **PT5** | Snapshot v0 + deopt restore | snapshot 数据结构;每个 guard 处的稀疏映射(IR 值 → 解释器栈槽);多帧 unsink 除外的 v0 版本(仅覆盖非 sunk 分配) | snapshot 编码 + restore 路径 + exit stub + 与 P4 exit-reason 协议对接(见 [./07-system-integration.md](./07-system-integration.md)) | deopt 注入 T3 全覆盖:每个 guard 强制失败,byte-equal([./09-acceptance-checklist.md](./09-acceptance-checklist.md));fuzz 长时间运行无异常 | [./06-snapshot-deopt.md](./06-snapshot-deopt.md) §? snapshot 骨架 + [./07-system-integration.md](./07-system-integration.md) exit-reason 协议扩展 | 3-5 人月(P5 最难的一块,见 [./00-overview.md](./00-overview.md) §5.1 风险 2) | v1 |
| **PT6** | Loop peeling / LICM | LuaJIT 式 loop peeling(首轮 + 优化后的后续轮双版本);循环不变量外提 | peeling + LICM 两个 pass 集成到管道 | 类别 2「循环内的冗余」内部锚 P5/P4 ≥ 1.5x([./09-acceptance-checklist.md](./09-acceptance-checklist.md) v1-D) | [./04-optimization-passes.md](./04-optimization-passes.md) §LICM / §loop-peeling | 1-2 人月 | v1 收尾 |
| **PT7** | Sink / 逃逸分析(v2 阶段) | 分配下沉:不逃出 trace 的分配彻底消除,字段拆为 IR 值;逃逸分析辅助判定 | sink pass + unsink 分配(deopt 时按配方重建对象)+ 与 GC 的交互测试 | 类别 3 内部锚 P5/P4 ≥ 2x([./09-acceptance-checklist.md](./09-acceptance-checklist.md) v2-B);T3(+sunk) / T8(+sunk)全部通过 | [./04-optimization-passes.md](./04-optimization-passes.md) §sink + [./06-snapshot-deopt.md](./06-snapshot-deopt.md) §unsink | 3-6 人月(v2 阶段前置) | **v2** |
| **PT8** | Side trace 树(v3 阶段) | 热 side exit 追踪 + 从 side exit 起点继续录 side trace;trace 树数据结构 + 冷 trace 回收 | side trace 生长机制 + 回收策略 + trace 树 fuzz 长时间运行 | 类别 4「megamorphic 稳定子集」内部锚 P5/P4 ≥ 1.5x([./09-acceptance-checklist.md](./09-acceptance-checklist.md) v3?);T12 全部通过;逼近 30x 外部锚 | [./02-trace-recording.md](./02-trace-recording.md) §side-trace + [./06-snapshot-deopt.md](./06-snapshot-deopt.md) 生命周期 | 2-4 人月 | **v3** |
| **PT9** | 调优 + 验收 runs | 阈值 / 长度 / 上限 / 各 pass 调优;运行完整 [./09-acceptance-checklist.md](./09-acceptance-checklist.md) 验收套 | v1/v2/v3 全套证据表 fill in(§4 台账) | [./09-acceptance-checklist.md](./09-acceptance-checklist.md) v3-A..v3-E 全部通过;双 arch 通过 | [./09-acceptance-checklist.md](./09-acceptance-checklist.md) 全篇 + [./08-testing-strategy.md](./08-testing-strategy.md) 全篇 | 1-3 人月 | v3 收尾 |

### 1.3 人月估算聚合

| 阶段 | PT | 累计估算 |
|---|---|---|
| v1(PT0-PT6) | 10.5 - 17 人月 |
| v2(PT7) | 3 - 6 人月 |
| v3(PT8-PT9) | 3 - 7 人月 |
| **合计** | **16.5 - 30 人月 ≈ +1.4-2.5 人年 到 v3 完成** |

与 [../roadmap.md](../roadmap.md) §4「+2-4 人年」的估算兼容(上限包含 spike 反复、双 arch 返工、snapshot bug 收敛的开放式时间),下沿低于 roadmap 是因为 PT8/PT9 分开算,并且 snapshot 收敛不可排期部分没有计入(见 [./00-overview.md](./00-overview.md) §5.1 风险 2)。

### 1.4 独立可停条件(见 [./09-acceptance-checklist.md](./09-acceptance-checklist.md) §2.1)

每个 PT 之后都可以决定是否继续:

| 决策点 | 触发条件 |
|---|---|
| PT0 后停止 | Spike 阶段 fail——物理层不可行,停下重新评估 |
| PT2/PT3 后停止 | IR 或基础 pass 无法收敛(bug 泛滥 / 抽象基础不成立)——重新评估 IR 形式或者降级为「只录制不优化」形式 |
| PT4 后停止 | Regalloc 严重阻塞——见 [./00-overview.md](./00-overview.md) §5.1 风险 3,纯 Go 全部显式 guard 折损的假设成真 |
| PT5 后停止 | Snapshot 机制正确性收敛不可达——见 §5.1 风险 2,fuzz 发现的 bug 曲线不收敛 |
| PT6 后停止(v1 阶段之后) | 类别 3 分配密集负载份额小,v2 立项不通过 |
| PT7 后停止(v2 阶段之后) | 类别 4 megamorphic 稳定子集份额小,v3 立项不通过 |

**每个停止点都要产出归档报告**(见 [./09-acceptance-checklist.md](./09-acceptance-checklist.md) §2.3):停止时的档位就是项目 P5 的永久形式。

---

## 2. 开放问题合并台账

见 [./00-overview.md](./00-overview.md) §5.2 开放问题索引——各章节的「开放问题」节是详细讨论的落点,本节聚合成台账,标注承接章节 + 待解时点。

| # | 问题 | 承接章节 | 待解时点 |
|---|---|---|---|
| Q1 | **IR 具体形式**(LuaJIT 式双数组 SSA vs 常规 SSA 结构) | [./03-ir-design.md](./03-ir-design.md) 开放问题节 | PT2 IR 定案时 |
| Q2 | **Snapshot 编码方案**(压缩策略、增量共享编码) | [./06-snapshot-deopt.md](./06-snapshot-deopt.md) 开放问题节 | PT5 v0 snapshot 定案时 |
| Q3 | **Regalloc 与 snapshot 的耦合协议**(regalloc 的自由度受 snapshot 引用约束) | [./05-register-allocation.md](./05-register-allocation.md) + [./06-snapshot-deopt.md](./06-snapshot-deopt.md) 联合章节 | PT4 + PT5 定案时 |
| Q4 | **Trace 阈值 / 长度上限 / 深度上限 / side trace 树生长回收策略** | [./02-trace-recording.md](./02-trace-recording.md) 开放问题节 | PT1 骨架 + PT8 树成形时分两批定 |
| Q5 | **NYI 与黑名单清单**(varargs / coroutine / debug 的 P5 扩展 + trace 特有的 NYI,比如 self-modifying 表 / 深递归 / 极深内联) | [./02-trace-recording.md](./02-trace-recording.md) NYI 节 | PT1 骨架时先定 v0,后续 PT 逐步扩展 |
| Q6 | **分配下沉与自管 GC 交互**(unsink 过程中的 GC、sunk 对象根可见性) | [./06-snapshot-deopt.md](./06-snapshot-deopt.md) unsink 节 + [./04-optimization-passes.md](./04-optimization-passes.md) sink pass 节 | PT7 v2 阶段 |
| Q7 | **Coroutine 与 trace 的关系**(LuaJIT 选择 trace 不跨 yield,望舒大概率也一样;沿 P2 F2 清单)——影响 T11 强制检查内容 | [./02-trace-recording.md](./02-trace-recording.md) 开放问题节 | PT1 定基线 v0(不跨 yield),立项之后再评估是否放开 |
| Q8 | **Fullmoon 与 gibbous 的热度交接细节**(从 gibbous 的 back edge 直接热到 trace 还是必须经过解释器录制)——LuaJIT 没有这个问题(它只有一层 JIT),望舒三层结构特有 | [./02-trace-recording.md](./02-trace-recording.md) 起点优先级节 + 本文 §1(PT1 骨架) | PT1 定 v0 决策,可能在 v2 / v3 阶段重新评估 |
| Q9 | **P4 arm64 分岔(issue #37/#40)的 P5 兼容处理**——P5 arm64 支持依赖 P4 arm64 exit-reason 协议端口 | [./07-system-integration.md](./07-system-integration.md) arch 章节 + 本文 §0.1 前置 | issue #37 闭合之后,P5 立项之前 |
| Q10 | **P3 主动保留的差分矩阵影响**(P5 加入后 CI 矩阵扩到 4 build × 3 平台 = 12 job) | [./08-testing-strategy.md](./08-testing-strategy.md) CI 矩阵节 | PT9 收尾时 |
| Q11 | **§3.2 A/B/C 立项阈值的具体数字校准** | [./01-launch-judgment.md](./01-launch-judgment.md) §3 + 预登记文档 | P5 立项判定启动之前 |
| Q12 | **首个宿主真实热脚本清单与 profile 数据** | [./01-launch-judgment.md](./01-launch-judgment.md) §2.2 侦察任务 | 宿主侧提供之后 |
| Q13 | **v1/v2/v3 独立可停触发条件的量化** | [./09-acceptance-checklist.md](./09-acceptance-checklist.md) §2.1 表右列 | 立项通过之后设计定稿时 |
| Q14 | **T 编号最终清单**(§3.2 T1-T12 是占位,可能拆分或合并) | [./08-testing-strategy.md](./08-testing-strategy.md) T-编号节 | [./08-testing-strategy.md](./08-testing-strategy.md) 定稿时 |
| Q15 | **T5 fuzz 时长下限的具体数字**(v1 4h / v2 8h / v3 16h 是占位) | [./09-acceptance-checklist.md](./09-acceptance-checklist.md) §3.2 T5 | 立项后按机器成本预算校准 |
| Q16 | **fullmoon 版本策略**(v1 stable 是否对外发布 wangshu v1.0) | 项目级决策 | v3 收尾判定会 |
| Q17 | **Cheaper alternatives 四条评估执行分工**(谁做 stdlib 内建化调研 / peephole 扩展设计 / 宿主对齐 / op-set 扩展 issue port) | [./01-launch-judgment.md](./01-launch-judgment.md) §4 | 立项判定启动之前分派 |

**统一原则**:每个问题标注承接章节后,该章节的「开放问题」节是详细讨论的落点。本文台账不复制详细内容,只作索引 + 待解时点;如果章节之间引用同一个问题不同步,以本表**待解时点**列为准。

### 2.1 与 [../../../llmdoc/memory/doc-gaps.md](../../../llmdoc/memory/doc-gaps.md) 的关系

以上 Q1-Q17 中,凡涉及外部输入(宿主 profile / 阈值决定 / 项目版本策略)的项,同步登记到 [../../../llmdoc/memory/doc-gaps.md](../../../llmdoc/memory/doc-gaps.md);内部设计决策(IR 形式 / snapshot 编码 / regalloc)保留在本表,不进 doc-gaps。分工:

- **doc-gaps**:Q11、Q12、Q15、Q16、Q17(依赖外部或跨项目决策);
- **本表**:Q1-Q10、Q13、Q14(P5 内部设计决策,施工中定)。

---

## 3. Change Log

**约定**:本文任何状态变更(P5 立项判定启动 / 通过 / PT 交付 / 阶段通过或停止)都在本节按倒序追加一行,格式模板:

```
- YYYY-MM-DD (PTx 交付 / 阶段 x 通过 / 阶段 x 停止 / 判定 x): <一句话说明> + <引用 commit hash 或 decision 归档路径>
```

### 3.1 初始占位(2026-07-02)

- **2026-07-02**(文档创建):P5 目录从单文件 `p5-trace-jit.md` 扩展为子目录形式,创建 [./00-overview.md](./00-overview.md) / [./01-launch-judgment.md](./01-launch-judgment.md) / [./09-acceptance-checklist.md](./09-acceptance-checklist.md) / [./implementation-progress.md](./implementation-progress.md)(本文)四篇骨架 + [./02-trace-recording.md](./02-trace-recording.md) 到 [./08-testing-strategy.md](./08-testing-strategy.md) 由并行 agent 起草。P5 未立项状态不变。

后续行(P5 立项判定启动 / 通过 / PT 交付等)按格式追加。

---

## 相关

- [./00-overview.md](./00-overview.md)(P5 总览,备选方案本质 + §4 章节地图 + §6 施工前置条件)
- [./01-launch-judgment.md](./01-launch-judgment.md)(启动判定 + §5.4 v1-v3 阶段验收预览 + §6.1 P4 baseline)
- [./09-acceptance-checklist.md](./09-acceptance-checklist.md)(v1/v2/v3 阶段验收项 + T1-T12 验证矩阵 + 证据台账)
- [./02-trace-recording.md](./02-trace-recording.md)(PT1 骨架 / NYI / 黑名单 / trace 起点)
- [./03-ir-design.md](./03-ir-design.md)(PT2 IR)
- [./04-optimization-passes.md](./04-optimization-passes.md)(PT3 / PT6 / PT7 优化 pass)
- [./05-register-allocation.md](./05-register-allocation.md)(PT4 regalloc)
- [./06-snapshot-deopt.md](./06-snapshot-deopt.md)(PT5 snapshot / unsink)
- [./07-system-integration.md](./07-system-integration.md)(P4 基建复用 + exit-reason 协议扩展)
- [./08-testing-strategy.md](./08-testing-strategy.md)(T1-T12 差分套 / deopt 注入 / pass 分级 / 持续 fuzz)
- [../roadmap.md](../roadmap.md)(§4 P5 定义 +2-4 人年开放式)
- [../architecture.md](../architecture.md)(§1 包布局 `internal/fullmoon/trace`)
- [../../../llmdoc/architecture/evolution-roadmap.md](../../../llmdoc/architecture/evolution-roadmap.md)(P5 速查行、启动条件)
- [../../../llmdoc/must/design-premises.md](../../../llmdoc/must/design-premises.md)(前提三原则 3 递归——§1.4 每个 PT 独立可停)
- [../../../llmdoc/memory/decisions/](../../../llmdoc/memory/decisions/)(立项凭据 / 阶段决策报告归档目录)
- [../../../llmdoc/memory/doc-gaps.md](../../../llmdoc/memory/doc-gaps.md)(§2.1 分工的外部输入类问题)
- [../p4-method-jit/implementation-progress.md](../p4-method-jit/implementation-progress.md)(P4 PJ 分档模式参照)
- [../p4-method-jit/09-acceptance-checklist.md](../p4-method-jit/09-acceptance-checklist.md)(P4 验收数据——P5 baseline)
- [../p3-wasm-tier/01-spike-gate.md](../p3-wasm-tier/01-spike-gate.md)(PT0 spike 阶段验收先例)
