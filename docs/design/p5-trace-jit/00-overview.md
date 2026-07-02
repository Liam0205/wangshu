# P5 总览:fullmoon/trace trace JIT——文档地图 / 施工计划 / 期权本质

> 状态:**未立项——期权,非计划。本目录是启动判定(01)通过后可直接施工的图纸;判定未通过前,任何章节都不产生代码**。P5 是五阶段中唯一「开放式」阶段,与 P1-P4 的「计划性阶段」结构性不同——见 [../roadmap.md](../roadmap.md) §4「+2-4 人年到可信 v1,开放式」+ [../../../llmdoc/architecture/evolution-roadmap.md](../../../llmdoc/architecture/evolution-roadmap.md) 速查表 P5 行「仅在 P4 收益不够时启动」。
>
> 对应 Go 包:`internal/fullmoon/trace`(trace 录制器 / SSA IR / 优化 pass / 寄存器分配器 / snapshot + deopt 机器;详见 [../architecture.md](../architecture.md) §1 包布局)。
>
> 上游契约:[../roadmap.md](../roadmap.md)(§4 P5 定义、§0 终局目标、§7 prior art:LuaJIT = trace JIT 架构范本)、[../../../llmdoc/architecture/evolution-roadmap.md](../../../llmdoc/architecture/evolution-roadmap.md)(P5 = fullmoon tier-2;仅在 P4 收益不够时启动)、[../../../llmdoc/must/design-premises.md](../../../llmdoc/must/design-premises.md)(前提一 6% 校准 —— P5 边际收益的达摩克利斯之剑 / 前提三五条贯穿原则,尤其原则 3 递归到 P5 内部各闸)。
>
> 依赖面:[../p4-method-jit/00-overview.md](../p4-method-jit/00-overview.md)(P5 的全部系统基建来自 P4——mmap / W^X / codebuf label resolver / amd64+arm64 encoders / exit-reason 协议 / P4HostState / trampoline)、[../p2-bridge/00-overview.md](../p2-bridge/00-overview.md)(热度 / feedback 前端;P5 复用回边采样与 IC 反馈)、[../p1-interpreter/05-interpreter-loop.md](../p1-interpreter/05-interpreter-loop.md)(§1 CallInfo / §7 调用协议——trace 录制宿主 + deopt 着陆点)、[../p1-interpreter/12-testing-difftest.md](../p1-interpreter/12-testing-difftest.md)(§3.8 Runner 抽象 / §6.1 列内核基准硬约束 / §8 fuzz 长跑)。
>
> P5 定位一句话:**只在 P4 收益不够时启动的期权;若行权,则复用 P4 全套基建把「实际走过的那一条路径」做成 IR + 优化 + regalloc + snapshot deopt 的 trace JIT,兑现 [../roadmap.md](../roadmap.md) §0 终局目标(列内核 10-30x over gopher-lua)**。

---

## 1. 定位:终局档位的最后一段

### 1.1 P5 的战略位置

```
P1 解释器 ──► P2 分层桥 ──► P3 Wasm 编译层 ──► P4 method JIT ──► P5 trace JIT
(2-4x)        (基建)        (4-8x)             (luajc 档,     (10-30x,开放式)
                                                 trace 收益 ~70%)  终局目标
```

P5 = **fullmoon(tier-2)**,月相命名的终点(满月),也是 [../roadmap.md](../roadmap.md) §0 终局目标(「列内核负载 10-30x over gopher-lua,逼近 LuaJIT 档」)的承载者。

流水线图标注 P4 已拿走「trace 收益 ~70%」——这是关键:**P5 的理论空间从一开始就只有剩余的 ~30%**。加上 +2-4 人年的开放式投入,决定了 P5 与 P1-P4 结构性不同的立项姿态:

> **P5 不是计划,是期权**(承 [../roadmap.md](../roadmap.md) §4 + 速查表)。启动条件唯一:「只在 P4 的收益不够时启动」。本目录的首要职责不是设计 P5,而是把「P4 收益不够」定义成可判定的框架([./01-launch-judgment.md](./01-launch-judgment.md)),防止 P5 因技术浪漫主义而非真实需求被启动。

### 1.2 期权与计划的差别

|  | 计划性阶段(P1-P4) | 期权性阶段(P5) |
|---|---|---|
| **是否必做** | 是,里程碑排期确定 | 否,行权条件不成立就不做 |
| **人力估算意义** | 排期基础,资源分配依据 | 判定输入,行权后才生效 |
| **未行权代价** | 无未行权概念 | 零——启动判定的过程产出本身有独立价值 |
| **验收依据** | 上阶段产出 + 本阶段人力到位 | P4 验收数据 + 宿主真实负载证据(01) |
| **失败模式** | 未完成里程碑 | 「不该做而做了」(01 §5 头号风险) |

期权模式的核心纪律是原则 3 的递归套用:**P4 停下,项目已达近期目标**——[../roadmap.md](../roadmap.md) §0 明确「近期目标 = 逼近 LuaJIT 档」,P4 兑现 luajc 档即完成近期目标,P5 是超出「近期」范围的开放式投资。

### 1.3 本目录相对于 [../p4-method-jit/](../p4-method-jit/) 的深度差

P4 目录是详细设计深度——每个 opcode 模板、每条 guard 形态、每条 exit 序列、每个寄存器约定都写到可施工级别。P5 目录**比 P4 目录粗一档但比原单文件设计稿细一档**:

- 比 P4 目录粗:因为 P5 未立项,IR 具体形式 / snapshot 编码 / regalloc 与 snapshot 耦合协议等实现级细节推迟到立项后再展开——立项前锁死这些细节等于「用工程野心替代需求证据」。
- 比原单文件细:因为 P5 需要「立项一通过就可以直接施工」,故构造决策(录制机制、IR 骨架、pass 分级、regalloc 策略、snapshot 概念方案、系统集成、测试策略、验收清单、施工分档)必须在本目录中就绪。

一句话:**本目录是一份施工图纸的骨架 —— 结构完备到可以按章节展开实施,但不锁死会被实测推翻的具体形式**。

---

## 2. 原理概览:trace JIT 五步流水线

### 2.1 pipeline(LuaJIT 范本)

P5 若启动,采用 LuaJIT 的总体架构([../roadmap.md](../roadmap.md) §7 prior art):

```
   crescent 解释执行(P2 热度计数照常)
        │ 热回边越 trace 阈值(比 P4 升层阈值更高)
        ▼
   ① trace 录制:解释器切入录制模式,逐条执行的同时把「实际执行的指令 +
      实际观察的类型 + 实际选中的分支」记成线性 IR;CALL 不分界——
      跟进被调函数继续录(= 天然内联);回到起点回边 ⇒ 闭环成 loop trace
        │ 录制中断(NYI 形状 / 太长 / 异常)⇒ 弃 trace,回纯解释,记黑名单
        ▼
   ② IR 优化:SSA 形式线性 IR;CSE、循环不变量外提(LuaJIT 式 loop
      peeling:首轮 + 优化后的后续轮)、guard 去重、死代码消除、
      分配下沉 / 逃逸分析(P5 最深的优化,可后置到 v2)
        ▼
   ③ 寄存器分配:线性 trace 上的逆序扫描分配(LuaJIT 单遍风格)——
      IR 值驻留机器寄存器,栈槽往返消失(P4 结构税在此终于卸掉)
        ▼
   ④ 发射 + 安装:复用 P4 全套系统管线(§3);trace 入口 patch 到热回边;
      每个 guard 挂 side exit + snapshot
        │ 运行期 guard 失败
        ▼
   ⑤ side exit:按 snapshot 物化解释器状态(可能多帧)→ crescent 续跑;
      某 exit 自身变热 ⇒ 从该 exit 续录 side trace(trace 树生长)
```

五步流水线分别对应本目录五个施工章节:

| 步骤 | 施工章节 |
|---|---|
| ① trace 录制 | [./02-trace-recording.md](./02-trace-recording.md) |
| ② IR + 优化 | [./03-ir-design.md](./03-ir-design.md) + [./04-optimization-passes.md](./04-optimization-passes.md) |
| ③ 寄存器分配 | [./05-register-allocation.md](./05-register-allocation.md) |
| ④ 发射 + 安装 | [./07-system-integration.md](./07-system-integration.md) |
| ⑤ side exit / snapshot | [./06-snapshot-deopt.md](./06-snapshot-deopt.md) |

### 2.2 P4 vs P5 一句话对比

**P4 编译「函数的全部可能路径」,投机只在类型维度;P5 编译「实际走过的那一条路径」,类型与控制流双维度投机**——投机更重 ⇒ 快路径更纯(无分支、全寄存器、跨函数);也 ⇒ 假设更脆,deopt 机器必须精细到指令级(承 [./06-snapshot-deopt.md](./06-snapshot-deopt.md))。

### 2.3 trace 起点优先级

录制起点选择沿用 LuaJIT 经验(具体机制见 [./02-trace-recording.md](./02-trace-recording.md) §2):

1. **热循环回边** —— 首要起点。loop trace 是列内核收益主体;P2 [../p2-bridge/01-profiling.md](../p2-bridge/01-profiling.md) §2 回边采样直接复用,只是阈值另设(P5 阈值 > P4)。
2. **热 side exit** —— 次要起点。已录制 trace 的某 exit 自身变热时,从该 exit 起点续录 side trace,组成 trace 树(留 v3 gate,承 [./09-acceptance-checklist.md](./09-acceptance-checklist.md) §2)。
3. **函数入口** —— 最后手段。up-recursion 等首次进入即热的形态需要函数入口 trace;规模最小、优先级最低。

---

## 3. 与 P4 的关系:基建全复用,新增四件套

### 3.1 复用清单(更新到当前 P4 现实)

P5 **不推倒任何已有层**,fullmoon 是叠在 gibbous 之上的第三执行层。相较 P4 复用清单本节按当前 P4 交付现实更新([../p4-method-jit/implementation-progress.md](../p4-method-jit/implementation-progress.md) §0 状态截至 2026-07-02):

| 资产 | 来源(P4 落点) | P5 怎么用 |
|---|---|---|
| 自管机器栈 / mmap+PROT_RW→PROT_RX / W^X / icache flush / trampoline | [../p4-method-jit/05-system-pipeline.md](../p4-method-jit/05-system-pipeline.md)([./07-system-integration.md](./07-system-integration.md) §2) | 原样复用——四项税解法与世界边界不因 trace 而变 |
| **codebuf 两遍 label resolver** | `internal/gibbous/jit/peroptranslator/codebuf.go`(PJ10 native emit 交付,承 [../p4-method-jit/10-per-op-translator.md](../p4-method-jit/10-per-op-translator.md) §14.2) | 复用——trace 线性 IR 发射天然适合两遍 label resolver 消 forward branch fixup |
| **amd64 + arm64 instruction encoders** | `internal/gibbous/jit/peroptranslator/emit_ops_amd64.go` + `emit_ops_arm64.go`(PJ10 交付) | 复用编码器;regalloc 之后的发射逻辑新写(trace 的码形与模板不同) |
| **exit-reason 协议**(mmap 段内不 call Go) | `jitCtx.exitArg0` 打包(helperCode, a, b, c, pc)+ `ExitInlineHelper` RET + Go 端 dispatcher 分派 + `RefreshJitCtxAddrs`(arena grow 后)+ `codePage + resumeOff` 重入,详 [../p4-method-jit/05-system-pipeline.md](../p4-method-jit/05-system-pipeline.md) §4.3 | 复用——P5 mmap 段内所有 helper 通道(unsink 分配 / IC miss / arena 越界)都经此协议;P5 无需自建 |
| **P4HostState 接口 + Go 端 dispatcher** | P4 shim path + R14=Go-G restore(PJ10 native emit 的 legacy 路径) | 复用——P5 全新的 host 通道仍走同一 host 接口签名(unsink 分配 / snapshot 物化辅助 / 慢路径 metamethod 等) |
| amd64 native op 接受门(当前 26 op) | [../p4-method-jit/implementation-progress.md](../p4-method-jit/implementation-progress.md) §14.5(2026-07-02 PR #34 amd64 端扩到 26 op,arm64 仍 18 op 线性子集,issue #37/#40 未闭) | P5 不共用「接受门」——trace 覆盖的是「实际执行过的 opcode 组合」,不受 method-JIT 接受门限制;但 P5 借鉴其「哪些 op 已能 mmap-safe inline」的经验作为 IR lowering 参考 |
| 热度计数 / TypeFeedback / F1-F7 闸门 | [../p2-bridge/00-overview.md](../p2-bridge/00-overview.md) | 复用:trace 阈值另设;feedback 辅助录制期类型决策;**NYI 黑名单沿用原则 4**——录制遇到不可处理形状(varargs / coroutine / debug 同款清单 + trace 特有 NYI)弃 trace 走下层,不做完备性 |
| NaN-box 值表示 / arena / GC 不变式 | [../p1-interpreter/01-value-object-model.md](../p1-interpreter/01-value-object-model.md) + [../architecture.md](../architecture.md) §4 | 值表示一次定死,IR 值的装拆箱就是 NaN-box 位操作,GC safepoint 纪律同 P4 |
| 差分 harness | [../p1-interpreter/12-testing-difftest.md](../p1-interpreter/12-testing-difftest.md) §3.8 已预留 `WangshuFullmoon` runner 注释槽 | 新增 P5 runner,同槽位接入,详 [./08-testing-strategy.md](./08-testing-strategy.md) |
| **新增(P5 独有)** | — | ① trace 录制器(解释器内嵌录制模式)② SSA IR + 优化 pass 集 ③ trace 寄存器分配器(逆序线性扫描)④ snapshot + 多帧 unsink deopt 机器 |

### 3.2 三层升降关系

crescent(录制宿主 + 全体 deopt 着陆点)→ gibbous(P3 wasm / P4 native,函数级稳态层——trace 覆盖不到 / 被黑名单的热函数停在这里)→ fullmoon(trace 覆盖的最热路径)。层间调用沿用 P3 / P4 的统一 CallInfo 协议与 trampoline。

**fullmoon 的 deopt 着陆点是 crescent 而非 gibbous**——deopt 语义以解释器为 oracle(原则 1),落到 gibbous 反而要二次映射状态,得不偿失;deopt 后热度再升回 gibbous / fullmoon 由 P2 状态机自然完成(承 [./06-snapshot-deopt.md](./06-snapshot-deopt.md) §5)。

### 3.3 与 P4 当前边界(arm64 分岔 + P3 保留)的关系

两点当前事实影响 P5 施工路径:

1. **amd64 与 arm64 现在有能力分岔**([../p4-method-jit/implementation-progress.md](../p4-method-jit/implementation-progress.md) §0):amd64 native 接 26 op(经 exit-reason 协议)、arm64 仍 18 op 线性子集(issues #37/#40 未闭)。P5 施工前 arm64 exit-reason 端口应先闭合——否则 P5 在 arm64 上会踩相同的 helper 通道缺口。
2. **P3 主动保留而非退役**([../p4-method-jit/09-acceptance-checklist.md](../p4-method-jit/09-acceptance-checklist.md) §2 D2:2026-07-01 用户定案):P3 是设计资产不是产品能力,若 iOS / seccomp 需求浮现再「捡回」。P5 立项时不需要与 P3 交互,但需知道 P3 保留意味着「同一 proto 有 crescent / gibbous-wasm / gibbous-jit / fullmoon-trace 四种执行形式」——差分矩阵扩到四方,详 [./08-testing-strategy.md](./08-testing-strategy.md) §3。

---

## 4. 文档地图:章节分工与读者路径

### 4.1 章节列表

| 章节 | 单一事实源(其它章节以此为准) |
|---|---|
| [./00-overview.md](./00-overview.md) 本文 | 期权本质 / 复用清单 / 章节地图 / 风险与开放问题索引 |
| [./01-launch-judgment.md](./01-launch-judgment.md) | 启动判定框架 / 负载类别表 / 量化预登记 / cheaper alternatives / v1-v3 内部闸门预览 |
| [./02-trace-recording.md](./02-trace-recording.md) | 解释器内嵌录制模式 / 录制起点优先级 / NYI 与黑名单 / trace 长度 / 深度上限 |
| [./03-ir-design.md](./03-ir-design.md) | SSA IR 骨架 / 类型 / opcode 分层 / 元方法可观察副作用协议 / GC 移动语义 |
| [./04-optimization-passes.md](./04-optimization-passes.md) | 折叠 / CSE / DCE / guard 去重 / LICM / loop peeling / v1 pass 集 |
| [./05-register-allocation.md](./05-register-allocation.md) | 逆序线性扫描 / snapshot 耦合协议 / spill 策略 / callee-save 与 P4 encoders 的 ABI 对接 |
| [./06-snapshot-deopt.md](./06-snapshot-deopt.md) | snapshot 数据结构 / 稀疏映射 / 多帧 unsink / 与 GC 交互 / exit stub |
| [./07-system-integration.md](./07-system-integration.md) | mmap+W^X+icache+trampoline 复用 / exit-reason 协议扩展 / 入口 patch / arena base 重载 |
| [./08-testing-strategy.md](./08-testing-strategy.md) | 三层 / 四层差分套 / deopt 注入 / pass 分级差分 / 持续 fuzz 集群 / T1-T? 编号约定 |
| [./09-acceptance-checklist.md](./09-acceptance-checklist.md) | 验收准则 / v1-v3 内部闸门 / 验证矩阵 T1-T? / 证据台账(空表待运行填入)|
| [./implementation-progress.md](./implementation-progress.md) | 施工分档 PT0-PT9 / 开放问题合并台账 / change log |

### 4.2 三类读者的阅读顺序

- **评审者(判定 P5 该不该做 / 何时做)**:读 [./00-overview.md](./00-overview.md) → [./01-launch-judgment.md](./01-launch-judgment.md)。判 P5 立项前只需前两篇 —— 后续章节都以「已行权」为前提。
- **施工者(立项通过后的实施者)**:按序读 [./02-trace-recording.md](./02-trace-recording.md) → [./03-ir-design.md](./03-ir-design.md) → [./04-optimization-passes.md](./04-optimization-passes.md) → [./05-register-allocation.md](./05-register-allocation.md) → [./06-snapshot-deopt.md](./06-snapshot-deopt.md) → [./07-system-integration.md](./07-system-integration.md)。每章末尾的开放问题 + 依赖前置章节的哪些结论,施工前必核对 [./implementation-progress.md](./implementation-progress.md) PT 分档是否已进到该章对应 PT。
- **测试者 / 验收者**:读 [./08-testing-strategy.md](./08-testing-strategy.md) + [./09-acceptance-checklist.md](./09-acceptance-checklist.md)。08 定测试机制,09 定验收清单;两篇合起来锁「P5 达标了没」。

### 4.3 与 P4 目录的映射关系(方便熟悉 P4 者上手)

| P4 章节 | P5 对应章节 | 关键差异 |
|---|---|---|
| [../p4-method-jit/00-overview.md](../p4-method-jit/00-overview.md) | [./00-overview.md](./00-overview.md) 本文 | P4 是计划、P5 是期权;头注状态字段 P4 「实现阶段」、P5 「未立项」 |
| [../p4-method-jit/01-launch-judgment.md](../p4-method-jit/01-launch-judgment.md)(luajc 档立项) | [./01-launch-judgment.md](./01-launch-judgment.md) | P4 立项锚是硬数字(164μs);P5 立项锚是「P4 结构性吃不下的负载类别 + 端到端占比 + 更便宜方案已耗尽」三元组 |
| [../p4-method-jit/02-template-direction.md](../p4-method-jit/02-template-direction.md)(方向裁决) | [./03-ir-design.md](./03-ir-design.md)(方向 = 上 SSA IR) | 恰好反向:P4 否决 IR 走模板;P5 立 IR 走优化 pass |
| [../p4-method-jit/03-speculation-ic.md](../p4-method-jit/03-speculation-ic.md)(投机 IC) | [./02-trace-recording.md](./02-trace-recording.md) §? 录制期类型观察 + 每章的 guard 章节 | P4 投机只类型;P5 投机类型 + 控制流两维度 |
| [../p4-method-jit/04-osr-deopt.md](../p4-method-jit/04-osr-deopt.md)(函数级 OSR)| [./06-snapshot-deopt.md](./06-snapshot-deopt.md)(指令级 snapshot deopt)| P4 exit 单位 = 函数,栈槽即真相;P5 exit 单位 = 指令,snapshot 重建多帧真相 |
| [../p4-method-jit/05-system-pipeline.md](../p4-method-jit/05-system-pipeline.md)(四项税) | [./07-system-integration.md](./07-system-integration.md) | 全复用;07 只写「怎么复用 + 增量新增点」 |
| [../p4-method-jit/06-backends.md](../p4-method-jit/06-backends.md)(双 arch) | [./05-register-allocation.md](./05-register-allocation.md) 尾节 + [./07-system-integration.md](./07-system-integration.md) | P5 复用 amd64 / arm64 encoders,不新写 backend |
| [../p4-method-jit/07-p3-retirement.md](../p4-method-jit/07-p3-retirement.md) | 不存在——D2 已定 P3 主动保留 | P5 不重复该决策 |
| [../p4-method-jit/08-testing-strategy.md](../p4-method-jit/08-testing-strategy.md) V1-V22 | [./08-testing-strategy.md](./08-testing-strategy.md) 用 T1-T? 编号避免冲突 | 差分套形态一致,深度加码 |
| [../p4-method-jit/09-acceptance-checklist.md](../p4-method-jit/09-acceptance-checklist.md) PJ11 | [./09-acceptance-checklist.md](./09-acceptance-checklist.md) v1-v3 内部闸门 | P5 分三档独立可停,比 P4 单点验收更递归 |
| [../p4-method-jit/implementation-progress.md](../p4-method-jit/implementation-progress.md) PJ0-PJ11 | [./implementation-progress.md](./implementation-progress.md) PT0-PT9 | PT = P-Trace,与 PJ 平行 |

---

## 5. 风险与开放问题索引

### 5.1 风险总表(施工阶段的四大风险)

原单文件 §6 的四条风险原样保留,是立项后施工阶段的持续监测项(立项前的元风险落 [./01-launch-judgment.md](./01-launch-judgment.md) §7):

1. **最大风险是「不该做而做了」** —— P4 达 luajc 档后,标量内核上距 LuaJIT 仅 ~6%(前提一);若宿主真实负载不落在 [./01-launch-judgment.md](./01-launch-judgment.md) §2 的四类里,P5 的 +2-4 人年买不到端到端可见收益(前提一校准测量 2 稀释教训)。[./01-launch-judgment.md](./01-launch-judgment.md) 判定框架就是本风险的全部对策:**让负载证据而非工程野心做决定**。这也是 [../roadmap.md](../roadmap.md) §4「只在 P4 收益不够时启动」的深意。

2. **人年开放式的失控面** —— snapshot 机器正确性收敛不可排期(承 [./06-snapshot-deopt.md](./06-snapshot-deopt.md) §? 复杂度评估)。对策:P5 内部分闸(录制+基础优化+regalloc+snapshot 为 v1;sink/逃逸为 v2;side trace 树为 v3),每闸独立可停 —— 原则 3 在 P5 内部的递归套用。详 [./09-acceptance-checklist.md](./09-acceptance-checklist.md) §2 v1-v3 定义。

3. **纯 Go 约束对 trace 收益的折损** —— 全显式 guard(无信号陷阱,承 [../p4-method-jit/03-speculation-ic.md](../p4-method-jit/03-speculation-ic.md) §3)在 guard 密集的 trace 码里成本占比高于 method JIT;trace 越长 guard 越多,10-30x 区间的上沿可能因此够不到 —— 验收区间本身已用「10-30x」的宽带表达了这层不确定(承 [./09-acceptance-checklist.md](./09-acceptance-checklist.md) §1)。

4. **维护性风险** —— trace JIT 的复杂度是永久性负债(LuaJIT 社区维护困境是前车),即便做成,团队是否长期养得起这台机器,应作为启动评审的显性议题([./01-launch-judgment.md](./01-launch-judgment.md) §5 议程项)。

### 5.2 开放问题索引(分派到各章)

原单文件 §6 的开放问题按主题分派到对应章节的「开放问题」节承接;本节仅作索引,避免同一问题多处失同步。

| 开放问题 | 承接章节 |
|---|---|
| §1.3 判定口径的具体阈值与负载集选定 —— P4 验收时预登记 | [./01-launch-judgment.md](./01-launch-judgment.md) §3 + [../../../llmdoc/memory/doc-gaps.md](../../../llmdoc/memory/doc-gaps.md) |
| IR 具体形式(LuaJIT 式双数组 vs 常规 SSA 结构) | [./03-ir-design.md](./03-ir-design.md) 开放问题节 |
| snapshot 编码方案(压缩策略、增量共享编码) | [./06-snapshot-deopt.md](./06-snapshot-deopt.md) 开放问题节 |
| regalloc 与 snapshot 的耦合协议 | [./05-register-allocation.md](./05-register-allocation.md) + [./06-snapshot-deopt.md](./06-snapshot-deopt.md) 联合 |
| trace 阈值 / 长度 / 深度上限 / side trace 树生长与回收 / 黑名单与 NYI 清单 | [./02-trace-recording.md](./02-trace-recording.md) 开放问题节 |
| 分配下沉与自管 GC 的交互(unsink 中途 GC、sunk 对象根可见性) | [./06-snapshot-deopt.md](./06-snapshot-deopt.md) + [./04-optimization-passes.md](./04-optimization-passes.md)(sink pass) |
| coroutine 与 trace 的关系(LuaJIT 选 trace 不跨 yield,望舒大概率同款) | [./02-trace-recording.md](./02-trace-recording.md) 开放问题节 |
| fullmoon 与 gibbous 的热度交接细节(从 gibbous 回边直接热到 trace,还是必须经解释器录制) | [./02-trace-recording.md](./02-trace-recording.md) + [./implementation-progress.md](./implementation-progress.md) §2 |

统一台账见 [./implementation-progress.md](./implementation-progress.md) §2,承接章节的「开放问题」节是详细讨论落点。

---

## 6. 施工前置条件小结

P5 施工前必须**全部**满足以下条件——任一未满足则不启动施工:

1. [./01-launch-judgment.md](./01-launch-judgment.md) §1 三条总闸门条件同时满足 + 具体档位决议已在 [../../../llmdoc/memory/decisions/](../../../llmdoc/memory/decisions/) 归档;
2. P4 amd64 + arm64 双 arch 均已达 luajc 档且性能数据在 [../p4-method-jit/09-acceptance-checklist.md](../p4-method-jit/09-acceptance-checklist.md) §3 归档(P5 的 baseline);
3. 首个目标宿主已给出真实热脚本 profile 数据,证明 [./01-launch-judgment.md](./01-launch-judgment.md) §2 四类负载在宿主端到端时间占比显著(具体阈值预登记见 §3);
4. cheaper alternatives 已系统评估([./01-launch-judgment.md](./01-launch-judgment.md) §4)—— stdlib 内建化 / P4 peephole 扩展 / 宿主侧 arena 形状 / P4 op-set 扩展,四条至少三条已尝试并被证明不足以关闭差距;
5. +2-4 人年人力预算与 fuzz 集群资源到位。

条件成熟后按 [./implementation-progress.md](./implementation-progress.md) §1 PT0 spike 闸门开工。

---

相关:
- [./01-launch-judgment.md](./01-launch-judgment.md)(启动判定框架 + 负载类别 + 量化预登记 + cheaper alternatives + v1-v3 闸门预览)
- [./02-trace-recording.md](./02-trace-recording.md)(录制机制 / NYI / 黑名单)
- [./03-ir-design.md](./03-ir-design.md)(SSA IR 骨架 / 类型 / opcode)
- [./04-optimization-passes.md](./04-optimization-passes.md)(优化 pass 集)
- [./05-register-allocation.md](./05-register-allocation.md)(逆序线性扫描 regalloc)
- [./06-snapshot-deopt.md](./06-snapshot-deopt.md)(snapshot 数据结构 + 多帧 unsink)
- [./07-system-integration.md](./07-system-integration.md)(P4 基建复用 + exit-reason 协议扩展)
- [./08-testing-strategy.md](./08-testing-strategy.md)(差分套 + deopt 注入 + pass 分级)
- [./09-acceptance-checklist.md](./09-acceptance-checklist.md)(v1-v3 闸门 + T1-T? 验证矩阵 + 证据台账)
- [./implementation-progress.md](./implementation-progress.md)(PT0-PT9 施工分档 + 开放问题合并台账)
- [../roadmap.md](../roadmap.md)(§4 P5 定义 / §0 终局目标 / §7 prior art)
- [../architecture.md](../architecture.md)(§1 包布局 `internal/fullmoon/trace` / §2 tier 映射 / §4 三不变式)
- [../p4-method-jit/00-overview.md](../p4-method-jit/00-overview.md)(基建来源 / 与 P4 章节映射基线)
- [../p4-method-jit/05-system-pipeline.md](../p4-method-jit/05-system-pipeline.md)(mmap+W^X+trampoline / exit-reason 协议原点)
- [../p4-method-jit/implementation-progress.md](../p4-method-jit/implementation-progress.md)(P4 交付现状——P5 baseline)
- [../p2-bridge/00-overview.md](../p2-bridge/00-overview.md)(热度 / feedback 前端)
- [../p1-interpreter/05-interpreter-loop.md](../p1-interpreter/05-interpreter-loop.md)(录制宿主 / CallInfo——snapshot 重建目标)
- [../p1-interpreter/06-memory-gc.md](../p1-interpreter/06-memory-gc.md)(分配下沉与 GC 交互)
- [../p1-interpreter/12-testing-difftest.md](../p1-interpreter/12-testing-difftest.md)(§3.8 fullmoon runner 预留 / §6.1 列内核基准硬约束 / §8 持续 fuzz)
- [../../../llmdoc/architecture/evolution-roadmap.md](../../../llmdoc/architecture/evolution-roadmap.md)(P5 速查行 / 启动条件)
- [../../../llmdoc/must/design-premises.md](../../../llmdoc/must/design-premises.md)(前提一 6% 校准 / 五条贯穿原则)
