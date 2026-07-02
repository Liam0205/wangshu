# P5 总览:fullmoon trace JIT——文档地图、施工计划、备选方案的本质

> 状态:**未立项。P5 是一个备选方案,不是既定计划**。本目录是启动判定(01)通过后可以直接开工的图纸;判定未通过前,任何章节都不产生代码。P5 是五个阶段里唯一「开放式」的阶段,和 P1-P4 的「计划性阶段」在结构上不同——见 [../roadmap.md](../roadmap.md) §4「+2-4 人年做到可信 v1,开放式」,以及 [../../../llmdoc/architecture/evolution-roadmap.md](../../../llmdoc/architecture/evolution-roadmap.md) 速查表 P5 行「仅在 P4 收益不够时启动」。
>
> 对应 Go 包:`internal/fullmoon/trace`(trace 录制器、SSA IR、优化 pass、寄存器分配器、snapshot + deopt 机制;详见 [../architecture.md](../architecture.md) §1 包布局)。
>
> 上游依据:[../roadmap.md](../roadmap.md)(§4 P5 定义、§0 终局目标、§7 prior art:LuaJIT 是 trace JIT 架构范本);[../../../llmdoc/architecture/evolution-roadmap.md](../../../llmdoc/architecture/evolution-roadmap.md)(P5 = fullmoon tier-2,仅在 P4 收益不够时启动);[../../../llmdoc/must/design-premises.md](../../../llmdoc/must/design-premises.md)(按前提一的校准,P4 完成后与 LuaJIT 只差约 6%,这直接限制了 P5 能带来的收益上限;前提三的五条原则,尤其是原则 3——P5 内部每个阶段也各设一道独立验收)。
>
> 依赖面:[../p4-method-jit/00-overview.md](../p4-method-jit/00-overview.md)(P5 的全部系统基建都来自 P4:mmap、W^X、codebuf label resolver、amd64+arm64 encoders、exit-reason 协议、P4HostState、trampoline);[../p2-bridge/00-overview.md](../p2-bridge/00-overview.md)(热度和 feedback 前端;P5 复用循环回跳(back edge)采样与 IC 反馈);[../p1-interpreter/05-interpreter-loop.md](../p1-interpreter/05-interpreter-loop.md)(§1 CallInfo、§7 调用协议——trace 录制的宿主 + deopt 落回点);[../p1-interpreter/12-testing-difftest.md](../p1-interpreter/12-testing-difftest.md)(§3.8 Runner 抽象、§6.1 列内核基准硬约束、§8 长时间运行的 fuzz)。
>
> P5 定位一句话:**这是一个只在 P4 收益不够时才会启动的备选方案。如果决定启动,就复用 P4 全套基建,把「实际走过的那一条路径」做成 IR + 优化 + regalloc + snapshot deopt 的 trace JIT,达到 [../roadmap.md](../roadmap.md) §0 的终局目标(列内核 10-30x over gopher-lua)**。

---

## 1. 定位:终局档位的最后一段

### 1.1 P5 的战略位置

```
P1 解释器 ──► P2 分层桥 ──► P3 Wasm 编译层 ──► P4 method JIT ──► P5 trace JIT
(2-4x)        (基建)        (4-8x)             (luajc 档,     (10-30x,开放式)
                                                 trace 收益 ~70%)  终局目标
```

P5 = **fullmoon(tier-2)**,是月相命名的终点(满月),也是 [../roadmap.md](../roadmap.md) §0 终局目标(「列内核负载 10-30x over gopher-lua,逼近 LuaJIT 档」)的承载者。

流水线图上标出 P4 已经拿走「trace 收益 ~70%」——这一点很关键:**P5 的理论空间从一开始就只剩下大约 30%**。再加上 +2-4 人年的开放式投入,决定了 P5 与 P1-P4 在立项姿态上有结构性的不同:

> **P5 不是既定计划,而是一个备选方案**(依据 [../roadmap.md](../roadmap.md) §4 和速查表)。启动条件只有一个:「只在 P4 的收益不够时启动」。本目录的首要职责不是设计 P5,而是把「P4 收益不够」定义成一个可以判定的框架([./01-launch-judgment.md](./01-launch-judgment.md)),防止 P5 因为技术上的浪漫想法(而不是真实需求)被启动。

### 1.2 备选方案与既定计划的差别

|  | 计划性阶段(P1-P4) | 备选方案性质的阶段(P5) |
|---|---|---|
| **是否必做** | 是,里程碑排期确定 | 否,启动条件不成立就不做 |
| **人力估算的意义** | 排期基础,资源分配依据 | 判定输入,决定启动后才生效 |
| **不启动的代价** | 没有「不启动」这个概念 | 零——启动判定过程本身的产出就有独立价值 |
| **验收依据** | 上一阶段的产出 + 本阶段人力到位 | P4 验收数据 + 宿主真实负载证据(见 01) |
| **失败模式** | 未完成里程碑 | 「不该做而做了」(见 01 §5 头号风险) |

备选方案模式的核心纪律是原则 3 的递归运用:**P4 停下,项目就已经达到近期目标**——[../roadmap.md](../roadmap.md) §0 明确写了「近期目标 = 逼近 LuaJIT 档」,P4 达到 luajc 档就完成了近期目标,P5 属于超出「近期」范围的开放式投资。

### 1.3 本目录相对于 [../p4-method-jit/](../p4-method-jit/) 的深度差

P4 目录写到了详细设计的深度——每个 opcode 模板、每种 guard 形式、每条 exit 序列、每个寄存器约定都写到了可以直接开工的级别。P5 目录**比 P4 目录粗一档,但比原来的单文件设计稿细一档**:

- 比 P4 目录粗:因为 P5 还没立项,IR 的具体形式、snapshot 编码、regalloc 与 snapshot 的耦合协议等实现级细节推迟到立项后再展开——立项前就把这些细节锁死,等于用工程上的想法替代真实的需求证据。
- 比原单文件细:因为 P5 需要做到「立项一通过就能直接开工」,所以架构上的决策(录制机制、IR 骨架、pass 分级、regalloc 策略、snapshot 概念方案、系统集成、测试策略、验收清单、施工分档)必须在本目录里就位。

一句话:**本目录是一份施工图纸的骨架——结构完整到可以按章节展开实施,但不把那些会被实测推翻的具体形式锁死**。

---

## 2. 原理概览:trace JIT 五步流水线

### 2.1 pipeline(以 LuaJIT 为范本)

P5 如果启动,采用 LuaJIT 的总体架构([../roadmap.md](../roadmap.md) §7 prior art):

```
   crescent 解释执行(P2 热度计数照常)
        │ 热的循环回跳(back edge)超过 trace 阈值(比 P4 升层阈值更高)
        ▼
   ① trace 录制:解释器切入录制模式,逐条执行的同时把「实际执行的指令 +
      实际观察到的类型 + 实际选中的分支」记成线性 IR;CALL 不作为边界——
      跟进被调函数继续记录(= 天然内联);回到起点回跳时闭环成 loop trace
        │ 录制中断(NYI 形式 / trace 太长 / 异常)时,丢弃 trace、回到纯解释、
        │ 把该起点记入黑名单
        ▼
   ② IR 优化:SSA 形式的线性 IR;CSE、循环不变量外提(LuaJIT 式 loop
      peeling:首轮 + 优化后的后续轮)、guard 去重、死代码消除、
      分配下沉 / 逃逸分析(P5 里最深的一层优化,可以放到 v2 再做)
        ▼
   ③ 寄存器分配:在线性 trace 上做逆序扫描分配(LuaJIT 单遍风格)——
      IR 值直接驻留在机器寄存器里,栈槽往返消失(P4 的结构开销在这里终于卸掉)
        ▼
   ④ 发射 + 安装:复用 P4 全套系统管线(见 §3);trace 入口 patch 到热
      back edge 上;每个 guard 都挂一个 side exit + 一份 snapshot
        │ 运行期间某个 guard 失败
        ▼
   ⑤ side exit:按 snapshot 物化解释器状态(可能涉及多帧)→ 由 crescent
      继续跑;如果某个 exit 自己变热了,就从这个 exit 起点继续录 side trace
      (trace 树逐步生长)
```

这五步流水线分别对应本目录的五个施工章节:

| 步骤 | 施工章节 |
|---|---|
| ① trace 录制 | [./02-trace-recording.md](./02-trace-recording.md) |
| ② IR + 优化 | [./03-ir-design.md](./03-ir-design.md) + [./04-optimization-passes.md](./04-optimization-passes.md) |
| ③ 寄存器分配 | [./05-register-allocation.md](./05-register-allocation.md) |
| ④ 发射 + 安装 | [./07-system-integration.md](./07-system-integration.md) |
| ⑤ side exit / snapshot | [./06-snapshot-deopt.md](./06-snapshot-deopt.md) |

### 2.2 P4 vs P5 一句话对比

**P4 编译「函数的全部可能路径」,投机只发生在类型维度;P5 编译「实际走过的那一条路径」,类型和控制流两个维度都投机**——投机更重意味着快路径更纯(没有分支、全在寄存器里、跨函数);同时也意味着假设更脆弱,deopt 机制必须精细到指令级别(见 [./06-snapshot-deopt.md](./06-snapshot-deopt.md))。

### 2.3 trace 起点优先级

录制起点的选择沿用 LuaJIT 的经验(具体机制见 [./02-trace-recording.md](./02-trace-recording.md) §2):

1. **热的循环 back edge**——首要起点。loop trace 是列内核收益的主体;P2 [../p2-bridge/01-profiling.md](../p2-bridge/01-profiling.md) §2 的 back edge 采样可以直接复用,只是阈值另设(P5 阈值 > P4)。
2. **热的 side exit**——次要起点。已录制 trace 的某个 exit 自身变热的时候,从该 exit 起点继续录 side trace,组成 trace 树(留到 v3 阶段,见 [./09-acceptance-checklist.md](./09-acceptance-checklist.md) §2)。
3. **函数入口**——最后手段。up-recursion 这类首次进入就热的情况需要函数入口 trace;规模最小,优先级最低。

---

## 3. 与 P4 的关系:基建全部复用,新增四件

### 3.1 复用清单(更新到当前 P4 现状)

P5 **不推倒任何已有层**,fullmoon 是叠在 gibbous 之上的第三个执行层。相较 P4 的复用清单,本节按当前 P4 的交付现状更新([../p4-method-jit/implementation-progress.md](../p4-method-jit/implementation-progress.md) §0 状态截至 2026-07-02):

| 资产 | 来源(P4 落点) | P5 怎么用 |
|---|---|---|
| 自管机器栈 / mmap+PROT_RW→PROT_RX / W^X / icache flush / trampoline | [../p4-method-jit/05-system-pipeline.md](../p4-method-jit/05-system-pipeline.md)(见 [./07-system-integration.md](./07-system-integration.md) §2) | 原样复用——这四项系统开销的解法和世界边界不会因为 trace 而改变 |
| **codebuf 两遍 label resolver** | `internal/gibbous/jit/peroptranslator/codebuf.go`(PJ10 native emit 交付,见 [../p4-method-jit/10-per-op-translator.md](../p4-method-jit/10-per-op-translator.md) §14.2) | 复用——trace 线性 IR 发射天然适合两遍 label resolver 来消除 forward branch fixup |
| **amd64 + arm64 instruction encoders** | `internal/gibbous/jit/peroptranslator/emit_ops_amd64.go` + `emit_ops_arm64.go`(PJ10 交付) | 复用编码器;regalloc 之后的发射逻辑是新写的(trace 的代码形式和模板不一样) |
| **exit-reason 协议**(mmap 段内不 call Go) | `jitCtx.exitArg0` 打包(helperCode, a, b, c, pc)+ `ExitInlineHelper` RET + Go 端 dispatcher 分派 + `RefreshJitCtxAddrs`(arena grow 之后)+ `codePage + resumeOff` 重入,详见 [../p4-method-jit/05-system-pipeline.md](../p4-method-jit/05-system-pipeline.md) §4.3 | 复用——P5 mmap 段内的所有 helper 通道(unsink 分配 / IC miss / arena 越界)都走这个协议;P5 不需要自建 |
| **P4HostState 接口 + Go 端 dispatcher** | P4 shim path + R14=Go-G restore(PJ10 native emit 的 legacy 路径) | 复用——P5 全新的 host 通道仍然走同一个 host 接口签名(unsink 分配、snapshot 物化辅助、慢路径 metamethod 等) |
| amd64 native op 已接入的清单(当前 26 op) | [../p4-method-jit/implementation-progress.md](../p4-method-jit/implementation-progress.md) §14.5(2026-07-02 PR #34 amd64 端扩到 26 op,arm64 仍是 18 op 线性子集,issue #37/#40 未闭) | P5 不共用「已接入的清单」——trace 覆盖的是「实际执行过的 opcode 组合」,不受 method-JIT 的接入限制;但 P5 参考其中「哪些 op 已经能 mmap-safe inline」的经验,作为 IR lowering 的参考 |
| 热度计数 / TypeFeedback / F1-F7 检查点 | [../p2-bridge/00-overview.md](../p2-bridge/00-overview.md) | 复用:trace 阈值另设;feedback 辅助录制期的类型决策;**NYI 黑名单沿用原则 4**——录制中遇到不可处理的形式(varargs / coroutine / debug 相同清单 + trace 特有的 NYI)就丢弃 trace 回到下层,不追求完备 |
| NaN-box 值表示 / arena / GC 不变式 | [../p1-interpreter/01-value-object-model.md](../p1-interpreter/01-value-object-model.md) + [../architecture.md](../architecture.md) §4 | 值表示一次定死,IR 值的装箱拆箱就是 NaN-box 位操作;GC safepoint 纪律与 P4 相同 |
| 差分测试 harness | [../p1-interpreter/12-testing-difftest.md](../p1-interpreter/12-testing-difftest.md) §3.8 已预留 `WangshuFullmoon` runner 注释槽 | 新增 P5 runner,接入同一个槽位,详见 [./08-testing-strategy.md](./08-testing-strategy.md) |
| **新增(P5 独有)** | — | ① trace 录制器(内嵌在解释器里的录制模式)② SSA IR + 优化 pass 集 ③ trace 寄存器分配器(逆序线性扫描)④ snapshot + 多帧 unsink deopt 机制 |

### 3.2 三层升降关系

crescent(录制宿主 + 所有 deopt 的落回点)→ gibbous(P3 wasm / P4 native,函数级稳态层——trace 覆盖不到或者进了黑名单的热函数停在这一层)→ fullmoon(trace 覆盖的最热路径)。层间调用沿用 P3 / P4 的统一 CallInfo 协议和 trampoline。

**fullmoon 的 deopt 落回点是 crescent,而不是 gibbous**——deopt 语义以解释器为标准(原则 1),落到 gibbous 反而要再做一次状态映射,得不偿失;deopt 之后热度再升回 gibbous / fullmoon 由 P2 状态机自然完成(见 [./06-snapshot-deopt.md](./06-snapshot-deopt.md) §5)。

### 3.3 与 P4 当前边界(arm64 分岔 + P3 保留)的关系

有两件当前的事实会影响 P5 的施工路径:

1. **amd64 和 arm64 现在存在能力差距**([../p4-method-jit/implementation-progress.md](../p4-method-jit/implementation-progress.md) §0):amd64 native 接了 26 op(经 exit-reason 协议),arm64 仍然只有 18 op 的线性子集(issues #37/#40 未闭)。P5 施工前 arm64 的 exit-reason 端口应该先补齐——否则 P5 在 arm64 上会踩到相同的 helper 通道缺口。
2. **P3 主动保留而不是退役**([../p4-method-jit/09-acceptance-checklist.md](../p4-method-jit/09-acceptance-checklist.md) §2 D2:2026-07-01 用户定下来):P3 是设计资产,不是产品能力;如果 iOS 或 seccomp 有需求再「捡回」。P5 立项时不需要与 P3 交互,但需要知道 P3 保留意味着「同一个 proto 有 crescent、gibbous-wasm、gibbous-jit、fullmoon-trace 四种执行形式」——差分矩阵会扩到四方,详见 [./08-testing-strategy.md](./08-testing-strategy.md) §3。

---

## 4. 文档地图:章节分工与读者路径

### 4.1 章节列表

| 章节 | 单一事实源(其它章节以此为准) |
|---|---|
| [./00-overview.md](./00-overview.md) 本文 | 备选方案本质、复用清单、章节地图、风险与开放问题索引 |
| [./01-launch-judgment.md](./01-launch-judgment.md) | 启动判定框架、负载类别表、量化预登记、cheaper alternatives、v1-v3 内部阶段验收预览 |
| [./02-trace-recording.md](./02-trace-recording.md) | 内嵌在解释器里的录制模式、录制起点优先级、NYI 与黑名单、trace 长度与深度上限 |
| [./03-ir-design.md](./03-ir-design.md) | SSA IR 骨架、类型、opcode 分层、元方法可观察副作用协议、GC 移动语义 |
| [./04-optimization-passes.md](./04-optimization-passes.md) | 折叠、CSE、DCE、guard 去重、LICM、loop peeling、v1 pass 集 |
| [./05-register-allocation.md](./05-register-allocation.md) | 逆序线性扫描、snapshot 耦合协议、spill 策略、callee-save 与 P4 encoders 的 ABI 对接 |
| [./06-snapshot-deopt.md](./06-snapshot-deopt.md) | snapshot 数据结构、稀疏映射、多帧 unsink、与 GC 的交互、exit stub |
| [./07-system-integration.md](./07-system-integration.md) | mmap+W^X+icache+trampoline 的复用、exit-reason 协议扩展、入口 patch、arena base 重载 |
| [./08-testing-strategy.md](./08-testing-strategy.md) | 三层 / 四层差分套、deopt 注入、pass 分级差分、持续 fuzz 集群、T1-T? 编号约定 |
| [./09-acceptance-checklist.md](./09-acceptance-checklist.md) | 验收准则、v1-v3 内部阶段验收、验证矩阵 T1-T?、证据台账(空表,等运行时填入) |
| [./implementation-progress.md](./implementation-progress.md) | 施工分档 PT0-PT9、开放问题合并台账、change log |

### 4.2 三类读者的阅读顺序

- **评审者(判断 P5 该不该做、什么时候做)**:读 [./00-overview.md](./00-overview.md) → [./01-launch-judgment.md](./01-launch-judgment.md)。判 P5 立项之前只需要看前两篇——后续章节都以「已经决定启动」为前提。
- **施工者(立项通过后的实施者)**:按顺序读 [./02-trace-recording.md](./02-trace-recording.md) → [./03-ir-design.md](./03-ir-design.md) → [./04-optimization-passes.md](./04-optimization-passes.md) → [./05-register-allocation.md](./05-register-allocation.md) → [./06-snapshot-deopt.md](./06-snapshot-deopt.md) → [./07-system-integration.md](./07-system-integration.md)。每章末尾的开放问题以及所依赖的前置章节的结论,施工前必须对照 [./implementation-progress.md](./implementation-progress.md) 的 PT 分档,看是否已经进到该章对应的 PT。
- **测试者、验收者**:读 [./08-testing-strategy.md](./08-testing-strategy.md) + [./09-acceptance-checklist.md](./09-acceptance-checklist.md)。08 定测试机制,09 定验收清单;两篇合起来锁定「P5 到底达标了没」。

### 4.3 与 P4 目录的映射关系(方便熟悉 P4 的人上手)

| P4 章节 | P5 对应章节 | 关键差异 |
|---|---|---|
| [../p4-method-jit/00-overview.md](../p4-method-jit/00-overview.md) | [./00-overview.md](./00-overview.md) 本文 | P4 是既定计划,P5 是备选方案;文档头的状态字段 P4 是「实现阶段」,P5 是「未立项」 |
| [../p4-method-jit/01-launch-judgment.md](../p4-method-jit/01-launch-judgment.md)(luajc 档立项) | [./01-launch-judgment.md](./01-launch-judgment.md) | P4 立项锚定的是硬数字(164μs);P5 立项锚定的是「P4 结构上吃不下的负载类别 + 端到端占比 + 更便宜方案已经耗尽」三项 |
| [../p4-method-jit/02-template-direction.md](../p4-method-jit/02-template-direction.md)(方向裁决) | [./03-ir-design.md](./03-ir-design.md)(方向 = 上 SSA IR) | 恰好方向相反:P4 否决了 IR 路线走模板;P5 立 IR 路线走优化 pass |
| [../p4-method-jit/03-speculation-ic.md](../p4-method-jit/03-speculation-ic.md)(投机 IC) | [./02-trace-recording.md](./02-trace-recording.md) §? 录制期的类型观察 + 每章的 guard 部分 | P4 只在类型维度投机;P5 在类型和控制流两个维度都投机 |
| [../p4-method-jit/04-osr-deopt.md](../p4-method-jit/04-osr-deopt.md)(函数级 OSR) | [./06-snapshot-deopt.md](./06-snapshot-deopt.md)(指令级 snapshot deopt) | P4 exit 的单位是函数,栈槽就是真相;P5 exit 的单位是指令,snapshot 重建多帧真相 |
| [../p4-method-jit/05-system-pipeline.md](../p4-method-jit/05-system-pipeline.md)(四项系统开销) | [./07-system-integration.md](./07-system-integration.md) | 全部复用;07 只写「怎么复用 + 增量新增的部分」 |
| [../p4-method-jit/06-backends.md](../p4-method-jit/06-backends.md)(双 arch) | [./05-register-allocation.md](./05-register-allocation.md) 尾节 + [./07-system-integration.md](./07-system-integration.md) | P5 复用 amd64 / arm64 encoders,不新写 backend |
| [../p4-method-jit/07-p3-retirement.md](../p4-method-jit/07-p3-retirement.md) | 不存在——D2 已经决定 P3 主动保留 | P5 不重复这个决策 |
| [../p4-method-jit/08-testing-strategy.md](../p4-method-jit/08-testing-strategy.md) V1-V22 | [./08-testing-strategy.md](./08-testing-strategy.md) 用 T1-T? 编号避免冲突 | 差分套的形式一致,深度加大 |
| [../p4-method-jit/09-acceptance-checklist.md](../p4-method-jit/09-acceptance-checklist.md) PJ11 | [./09-acceptance-checklist.md](./09-acceptance-checklist.md) v1-v3 内部阶段验收 | P5 分三档独立可停,比 P4 单点验收更具递归性 |
| [../p4-method-jit/implementation-progress.md](../p4-method-jit/implementation-progress.md) PJ0-PJ11 | [./implementation-progress.md](./implementation-progress.md) PT0-PT9 | PT = P-Trace,与 PJ 平行 |

---

## 5. 风险与开放问题索引

### 5.1 风险总表(施工阶段的四大风险)

原单文件 §6 的四条风险原样保留,是立项后施工阶段需要持续监测的项目(立项前的元风险落在 [./01-launch-judgment.md](./01-launch-judgment.md) §7):

1. **最大的风险是「不该做而做了」**——P4 达到 luajc 档之后,标量内核上距 LuaJIT 只差约 6%(前提一);如果宿主的真实负载不落在 [./01-launch-judgment.md](./01-launch-judgment.md) §2 的四类里,P5 的 +2-4 人年买不到端到端可见的收益(前提一校准测量 2 的稀释教训)。[./01-launch-judgment.md](./01-launch-judgment.md) 的判定框架就是针对这个风险的全部对策:**让负载证据、而不是工程上的想法来做决定**。这也是 [../roadmap.md](../roadmap.md) §4「只在 P4 收益不够时启动」的深意。

2. **人年开放式的失控面**——snapshot 机制的正确性收敛不能排期(见 [./06-snapshot-deopt.md](./06-snapshot-deopt.md) §? 复杂度评估)。对策:P5 内部分阶段(录制 + 基础优化 + regalloc + snapshot 为 v1;sink 和逃逸为 v2;side trace 树为 v3),每个阶段独立可停——这是原则 3 在 P5 内部的递归运用。详见 [./09-acceptance-checklist.md](./09-acceptance-checklist.md) §2 v1-v3 定义。

3. **纯 Go 约束对 trace 收益的折损**——全部走显式 guard(没有信号陷阱,见 [../p4-method-jit/03-speculation-ic.md](../p4-method-jit/03-speculation-ic.md) §3)在 guard 密集的 trace 代码里成本占比比 method JIT 更高;trace 越长 guard 越多,10-30x 区间的上沿可能因此够不到——验收区间本身已经用「10-30x」这个宽带表达了这层不确定(见 [./09-acceptance-checklist.md](./09-acceptance-checklist.md) §1)。

4. **维护性风险**——trace JIT 的复杂度是永久性的负债(LuaJIT 社区的维护困境是前车之鉴),即便做成了,团队是否长期养得起这台机器,应该作为启动评审的显性议题([./01-launch-judgment.md](./01-launch-judgment.md) §5 议程项)。

### 5.2 开放问题索引(分派到各章)

原单文件 §6 的开放问题按主题分派到对应章节的「开放问题」节承接;本节只作索引,避免同一个问题在多处不同步。

| 开放问题 | 承接章节 |
|---|---|
| §1.3 判定标准的具体阈值与负载集选定——P4 验收时预登记 | [./01-launch-judgment.md](./01-launch-judgment.md) §3 + [../../../llmdoc/memory/doc-gaps.md](../../../llmdoc/memory/doc-gaps.md) |
| IR 具体形式(LuaJIT 式双数组 vs 常规 SSA 结构) | [./03-ir-design.md](./03-ir-design.md) 开放问题节 |
| snapshot 编码方案(压缩策略、增量共享编码) | [./06-snapshot-deopt.md](./06-snapshot-deopt.md) 开放问题节 |
| regalloc 与 snapshot 的耦合协议 | [./05-register-allocation.md](./05-register-allocation.md) + [./06-snapshot-deopt.md](./06-snapshot-deopt.md) 联合 |
| trace 阈值、长度、深度上限,side trace 树的生长与回收,黑名单与 NYI 清单 | [./02-trace-recording.md](./02-trace-recording.md) 开放问题节 |
| 分配下沉与自管 GC 的交互(unsink 过程中的 GC、sunk 对象的根可见性) | [./06-snapshot-deopt.md](./06-snapshot-deopt.md) + [./04-optimization-passes.md](./04-optimization-passes.md)(sink pass) |
| coroutine 与 trace 的关系(LuaJIT 选择 trace 不跨 yield,望舒大概率也一样) | [./02-trace-recording.md](./02-trace-recording.md) 开放问题节 |
| fullmoon 与 gibbous 的热度交接细节(从 gibbous 的 back edge 直接热到 trace,还是必须经过解释器录制) | [./02-trace-recording.md](./02-trace-recording.md) + [./implementation-progress.md](./implementation-progress.md) §2 |

统一台账见 [./implementation-progress.md](./implementation-progress.md) §2,承接章节的「开放问题」节是详细讨论的落点。

---

## 6. 施工前置条件小结

P5 施工之前必须**同时**满足以下条件——任何一条不满足都不启动施工:

1. [./01-launch-judgment.md](./01-launch-judgment.md) §1 三条总条件同时满足,并且具体的档位决议已经在 [../../../llmdoc/memory/decisions/](../../../llmdoc/memory/decisions/) 归档;
2. P4 amd64 + arm64 双 arch 都已达到 luajc 档,并且性能数据在 [../p4-method-jit/09-acceptance-checklist.md](../p4-method-jit/09-acceptance-checklist.md) §3 归档(作为 P5 的 baseline);
3. 首个目标宿主已经给出真实热脚本的 profile 数据,能证明 [./01-launch-judgment.md](./01-launch-judgment.md) §2 的四类负载在宿主端到端时间里占比显著(具体阈值的预登记见 §3);
4. cheaper alternatives 已经系统评估过([./01-launch-judgment.md](./01-launch-judgment.md) §4)——stdlib 内建化、P4 peephole 扩展、宿主侧 arena 形状、P4 op-set 扩展,这四条至少已经尝试过三条并且被证明不足以关闭差距;
5. +2-4 人年的人力预算和 fuzz 集群资源到位。

条件成熟之后按 [./implementation-progress.md](./implementation-progress.md) §1 PT0 spike 阶段验收开工。

---

相关:
- [./01-launch-judgment.md](./01-launch-judgment.md)(启动判定框架 + 负载类别 + 量化预登记 + cheaper alternatives + v1-v3 阶段验收预览)
- [./02-trace-recording.md](./02-trace-recording.md)(录制机制、NYI、黑名单)
- [./03-ir-design.md](./03-ir-design.md)(SSA IR 骨架、类型、opcode)
- [./04-optimization-passes.md](./04-optimization-passes.md)(优化 pass 集)
- [./05-register-allocation.md](./05-register-allocation.md)(逆序线性扫描 regalloc)
- [./06-snapshot-deopt.md](./06-snapshot-deopt.md)(snapshot 数据结构 + 多帧 unsink)
- [./07-system-integration.md](./07-system-integration.md)(P4 基建复用 + exit-reason 协议扩展)
- [./08-testing-strategy.md](./08-testing-strategy.md)(差分套 + deopt 注入 + pass 分级)
- [./09-acceptance-checklist.md](./09-acceptance-checklist.md)(v1-v3 阶段验收 + T1-T? 验证矩阵 + 证据台账)
- [./implementation-progress.md](./implementation-progress.md)(PT0-PT9 施工分档 + 开放问题合并台账)
- [../roadmap.md](../roadmap.md)(§4 P5 定义、§0 终局目标、§7 prior art)
- [../architecture.md](../architecture.md)(§1 包布局 `internal/fullmoon/trace`、§2 tier 映射、§4 三不变式)
- [../p4-method-jit/00-overview.md](../p4-method-jit/00-overview.md)(基建来源 + 与 P4 章节的映射基线)
- [../p4-method-jit/05-system-pipeline.md](../p4-method-jit/05-system-pipeline.md)(mmap+W^X+trampoline、exit-reason 协议原点)
- [../p4-method-jit/implementation-progress.md](../p4-method-jit/implementation-progress.md)(P4 交付现状——P5 的 baseline)
- [../p2-bridge/00-overview.md](../p2-bridge/00-overview.md)(热度 + feedback 前端)
- [../p1-interpreter/05-interpreter-loop.md](../p1-interpreter/05-interpreter-loop.md)(录制宿主、CallInfo——snapshot 重建的目标)
- [../p1-interpreter/06-memory-gc.md](../p1-interpreter/06-memory-gc.md)(分配下沉与 GC 的交互)
- [../p1-interpreter/12-testing-difftest.md](../p1-interpreter/12-testing-difftest.md)(§3.8 fullmoon runner 预留、§6.1 列内核基准硬约束、§8 持续 fuzz)
- [../../../llmdoc/architecture/evolution-roadmap.md](../../../llmdoc/architecture/evolution-roadmap.md)(P5 速查行、启动条件)
- [../../../llmdoc/must/design-premises.md](../../../llmdoc/must/design-premises.md)(前提一 6% 校准、五条贯穿原则)
