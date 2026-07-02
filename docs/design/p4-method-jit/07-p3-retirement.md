# P4 §7:P3 Wasm 层的去留——P4 验收时的决策框架

> 状态:**架构决策深度**(对齐 [../architecture.md](../architecture.md) §2 状态表:P4 是「架构决策」,比 P2/P3 详细设计粗一档——本文定方向、定边界、给关键决策框架,**不展开退役/留中层的具体工程实施**,那是 P4 上线时的实操或后续 implementation-progress 收口的事)。本文是 P4 文档集 [./00-overview.md](./00-overview.md) §0 文档地图所定的「P3 去留决策框架」单一事实源——P4 验收通过时,P3 是退役还是作为可移植中层留下,本文给框架而非结论;**结论在 P4 验收时用数据定**。
>
> 上游契约:[../roadmap.md](../roadmap.md)(§4 P4 验收门槛 + P3 去留两个选项的字面措辞)、[../../../llmdoc/architecture/evolution-roadmap.md](../../../llmdoc/architecture/evolution-roadmap.md)(tier 映射:P3/P4 同 gibbous tier-1)、[../architecture.md](../architecture.md) §1(`internal/gibbous/wasm` 与 `internal/gibbous/jit` 并列的包布局——结构上共存零成本)。
>
> P3 承接面:[../p3-wasm-tier/00-overview.md](../p3-wasm-tier/00-overview.md)(P3 总览,边界表 + tier 映射 + 决策矩阵的对偶面)、[../p3-wasm-tier/implementation-progress.md](../p3-wasm-tier/implementation-progress.md)(P3 PW0-PW10 全卷已交付,实测基线见本文 §3.1)。
>
> 接口稳定面:[../p2-bridge/05-p3-p4-interface.md](../p2-bridge/05-p3-p4-interface.md) §0.3(接口稳定性使两后端共存零成本——本文 §5.3 的物理基础)。
>
> 跨阶段对位:[./01-launch-judgment.md](./01-launch-judgment.md)(本文是 P4 验收时对偶决策——立项判定决定要不要做 P4,本节决定 P4 上线时 P3 去/留;两个判定形态平行,共享同款「闸门级单点决策」纪律)。
>
> **本文定位一句话**:**给框架不给结论**——P3 表现 / 真实宿主需求 / 平台覆盖需求三类输入须实测后才能裁,任何在本文给「P3 退役」或「P3 留中层」最终结论的文字都判否,结论在 P4 验收时用数据定。

对应 Go 包决策:`internal/gibbous/wasm`(P3,留版本史 / 退役 / 留中层视决策)与 `internal/gibbous/jit`(P4,留)的去留二选一,详见 §6 / §7。

---

## 0. 定位:P4 验收时必须做的决策

### 0.1 一句话:验收时的二选一

[../roadmap.md](../roadmap.md) §4 P4 段对验收的字面承诺:

> **「验收**:列内核负载 ≥ LuaJ-luajc 档;Wasm 层退役,**或**留作可移植中层(未移植架构、禁 exec-mmap 环境)」

这一段给出 P4 验收通过后的两个选项,本文承担这一二选一的决策框架——P4 验收时数据进档后必须裁决:

| 选项 | 描述 | 判定时点 |
|---|---|---|
| **P3 退役** | `internal/gibbous/wasm` 代码留版本史移除主分支,wazero 依赖从 go.mod 删除,差分矩阵收窄 | P4 验收通过后 |
| **P3 留作可移植中层** | 平台 build tag 矩阵分:P4 平台(amd64/arm64 + exec-mmap)走 jit,P3 平台(其它)走 wasm | P4 验收通过后 |

任何在本文给「P3 退役」或「P3 留中层」最终结论的文字都判否——本文唯一职责是把决策输入分类清楚 + 把决策矩阵列详细 + 把缺省倾向写明确,**结论在 P4 验收时按 §3 实测口径与 §4 翻案条件裁决**。

### 0.2 为什么是「框架不是结论」

P3 去留决策的输入分三类,每类都需要在 P4 验收时实测后才能裁:

| 输入类 | 内容 | 实测要求 |
|---|---|---|
| **P3 实际表现** | P3 在 P4 上线后的「P4 不可用平台」上跑出的实际收益(对照解释器 crescent) | P4 验收时跑「P4 vs crescent vs P3」三方对照 benchmark,在 ≥1 个 P4 不可用平台上 |
| **真实宿主需求** | 首个目标宿主或后续宿主是否有「禁 exec-mmap」(iOS 嵌入 / seccomp 沙箱)等运行环境约束 | 由首个或新宿主提出明确需求才进入考量;无需求则不必为「假设需求」预留 |
| **平台覆盖需求** | wangshu 是否承诺支持 riscv64/ppc64le/s390x 等 Go 工具链支持但 P4 不支持的架构 | 由项目战略层决定承诺面;承诺面默认仅 amd64/arm64(P4 + crescent 已覆盖) |

任何把这三类输入「凭工程直觉押结论」的文字都违反 [../../../llmdoc/guides/design-claims-vs-codebase-physics.md](../../../llmdoc/guides/design-claims-vs-codebase-physics.md) 同源纪律——本文承担把判据列清楚的职责,不替项目主决策者拍板。

### 0.3 与 P4 §1 启动判定的对偶面

本文与 [./01-launch-judgment.md](./01-launch-judgment.md)(P4 §1 立项判定)在形态上对偶——两者都是 P4 阶段的闸门级单点决策,但承担不同时点的不同决策:

| 维度 | [./01-launch-judgment.md](./01-launch-judgment.md)(立项闸门) | 本文 §7(去留闸门) |
|---|---|---|
| **决策对象** | P4 该不该做 / 何时做 | P4 上线时 P3 怎么办 |
| **判定时点** | P3 收口后,P4 实施前 | P4 验收通过后 |
| **判定形态** | 综合判定(三档前置 + P3 现状 + 宿主负载证据) | 综合判定(P3 实测表现 + 真实宿主需求 + 平台覆盖) |
| **结论可逆性** | 立项后大方向不可逆(详 [./01-launch-judgment.md](./01-launch-judgment.md) §0.2) | 留中层可逆(后续可退役)/ 退役不可逆(代码删除 + 文档转遗产标记) |
| **数据归档点** | [./implementation-progress.md](./implementation-progress.md)(立项时建立) | [./implementation-progress.md](./implementation-progress.md) + [doc-gaps](../../../llmdoc/memory/doc-gaps.md)(P4 验收时归档) |
| **战略价值双向性** | 立项 / 不立项都是合理产出 | 退役 / 留中层都是合理产出,**两个选项都不丢失 P4 验收数据** |

对偶面的纪律意义:**P4 阶段从立项到验收均按「闸门级单点决策」模式推进,数据进档(§5.5)即合理产出,不押 wishful thinking**——这是 [../roadmap.md](../roadmap.md) §5 原则 3「每阶段独立交付价值,任何闸门处停下都不亏」在 P4 闸门链上的体现。

### 0.4 章节路标

本文章节走「论据拆穿 → 决策矩阵 → 实测口径 → 翻案条件 → 缺省倾向 + 决策时点 → 工程动作清单 → 跳跃路径自动消解 → 不变式 + 风险」次序:

| 章节 | 内容 | 单一事实源 |
|---|---|---|
| §1 | 「留作可移植中层」表面论据的关键拆穿(wazero 编译引擎与 P4 共享平台约束) | 本文 §1 |
| §2 | 决策矩阵(承旧 §6.2 三档环境 × 三层最优,加 wazero 平台支持矩阵考据) | 本文 §2 |
| §3 | 决策的实测口径(P4 验收时跑什么) | 本文 §3 |
| §4 | 翻案条件(留 P3 的硬条件) | 本文 §4 |
| §5 | 缺省倾向 + 决策时点 + 数据进档 | 本文 §5 |
| §6 | 退役的工程动作清单 | 本文 §6 |
| §7 | 留作可移植中层的工程动作清单 | 本文 §7 |
| §8 | 跳跃路径下本节自动消解(承 [./01-launch-judgment.md](./01-launch-judgment.md) §2.2) | 本文 §8 |
| §9 | 不变式清单 | 本文 §9 |
| §10 | 风险与开放问题 | 本文 §10 |
| §11 | 回填请求 | 本文 §11 |


---

## 1. 「留作可移植中层」的表面论据 + 关键拆穿

### 1.1 表面论据

承 [../roadmap.md](../roadmap.md) §4 P4 段「Wasm 层退役,**或**留作可移植中层(未移植架构、禁 exec-mmap 环境)」——「留作可移植中层」选项的表面论据是:

| 表面论据点 | 内容 |
|---|---|
| **P4 平台覆盖窄** | P4 仅做 amd64 / arm64 双后端([./06-backends.md](./06-backends.md) §1)——这是 [./02-template-direction.md](./02-template-direction.md) §1.3 候选谱系否决论证下的工程合理选型,但物理上不覆盖 riscv64 / ppc64le / s390x 等其它 Go 工具链支持架构 |
| **P4 需要 exec-mmap** | P4 自付四项税 + 系统管线(详 [./05-system-pipeline.md](./05-system-pipeline.md))——必须分配可执行内存(`mmap PROT_EXEC` 或等价物);iOS / seccomp 沙箱等禁 exec-mmap 环境无法用 P4 |
| **P3 看起来兜底** | P3 的 wazero 后端「平台独立」「无 exec-mmap 依赖」是表面读印象——上述「P4 不可用」环境下,P3 看起来天然兜底,使「留 P3 作可移植中层」成为合理选项 |

### 1.2 拆穿一层:wazero 编译引擎与 P4 共享同一组平台约束

这条论据被「拆穿一层」就发现 P3 在 P4 不可用平台上的物理形态根本不是「兜底」——wazero 项目本身的物理事实:

| wazero 平台支持事实 | 内容 | 物理来源 |
|---|---|---|
| **wazero 编译引擎仅 amd64 / arm64** | wazero 的 `RuntimeConfigCompiler` 模式生成 amd64 / arm64 原生码;其它架构编译引擎不可用 | wazero 项目本身的工程现状(P3 启动期 spike 已确认 [../p3-wasm-tier/01-spike-gate.md](../p3-wasm-tier/01-spike-gate.md) §1.4) |
| **wazero 编译引擎需 exec-mmap** | 与 P4 同款约束——分配可执行内存写机器码 → mprotect 翻面跳入执行 | 系统管线物理依赖与 P4 完全同源(详 [./05-system-pipeline.md](./05-system-pipeline.md) §4.1 wazero 参考) |
| **其它平台 wazero 走解释模式** | `RuntimeConfigInterpreter` 模式逐条解释 Wasm 字节码——纯 Go 解释器跑 Wasm | wazero 设计文档 + 实测 |

这一条物理事实使「留 P3 作可移植中层」的表面论据**当场被拆穿**:**在 P4 不可用平台上,wazero 退化为解释模式**——P3 不再是「编译执行 Wasm」,而是「解释执行 Wasm」。

### 1.3 在 P4 不可用平台上,wazero 退化为解释模式

形式化承上一节:

```
[P4 可用平台]                       [P4 不可用平台]
amd64 / arm64 + exec-mmap            riscv64 / ppc64le / s390x / iOS / seccomp...
       │                                    │
       ▼                                    ▼
P4 = 自管原生码 codegen            P4 = 不可用
P3 = wazero 编译模式生成原生码     P3 = wazero 解释模式逐条解释 Wasm 字节码
       │                                    │
       ▼                                    ▼
两层共存(P3/P4 各跑各的)        P3 = 「解释 Wasm」非「编译 Wasm」
                                     ↓
                                  这才是真正比较的位置
```

P4 不可用平台上,P3 的真实形态不是「Wasm 编译执行」,而是 「Wasm 解释执行」——这一形态变换是 §1.4 真正比较的物理基础。

### 1.4 真正比较:crescent 直接解释 vs wazero 解释模式跑「Lua → Wasm 翻译产物」

承 §1.3,P4 不可用平台上,P3 留作中层的真实选型问题是:

| 选项 | 形态 | 链路长度 |
|---|---|---|
| **crescent 直接解释 Lua 字节码** | Lua 字节码 → crescent 主循环逐条 dispatch | 一层解释 |
| **P3 走 wazero 解释模式跑 Wasm** | Lua 字节码 → wangshu/wasm 翻译为 Wasm → wazero 解释模式逐条解释 Wasm | **两层翻译再解释** |

后者比前者**多了一层翻译**(Lua → Wasm 翻译产物本身比 Lua 字节码长且语义更显式)+ **执行体经更细粒度的指令集**(Wasm 字节码 ISA 比 Lua 字节码 ISA 更细,意味着同一段逻辑要跑更多 Wasm 指令)+ **解释器自身实现质量** wazero 解释模式 vs crescent 解释模式两边都未经特别针对该平台的优化。

### 1.5 「后者隔了一层翻译再解释,大概率不快反慢」是核心论证

综合 §1.4,在 P4 不可用平台上,P3 留作中层的物理预期是:

| 预期 | 论证 |
|---|---|
| **大概率不快反慢** | crescent 是手写 Lua 解释器,直接 dispatch Lua 字节码;P3 走解释模式跑「Lua → Wasm 翻译产物」隔了一层翻译,Wasm 指令数显著多于原 Lua 字节码,解释器再走 wazero 抽象层——双层 dispatch 税 + 翻译产物指令膨胀 ≫ crescent 一层 dispatch |
| **难证「翻译产物某些形态在 wazero 解释模式下显著快」** | 理论上可能存在某些 Lua 形态在 Wasm 翻译后结构更规则使 wazero 解释器跑得快(如循环展开 / dispatch 表),但这一假设需实测翻案——不是默认前提,见 §4.1 翻案条件 |
| **「留作可移植中层」需要实测翻案才能成立** | 默认推论:「留作可移植中层」表面合理,拆穿后大概率不成立——实测翻案前不预设此结论 |

**这正是「框架不给结论」的核心**:本文不替项目主决策者断言「P3 一定退役」,只是把「留中层」表面论据被拆穿的物理逻辑写清楚——P4 验收时若实测显示某类负载下 wazero 解释模式仍快于 crescent,翻案路径明确(§4),否则缺省倾向是 P3 退役(§5.1)。


---

## 2. 决策矩阵(承旧 §6.2 表展开)

### 2.1 三档环境 × 三层最优表

本节展开决策矩阵,加上 wazero 平台支持矩阵考据 + 每档「P4 可用?P3 编译模式可用?该环境最优层」逐档展开:

| 环境档 | 平台示例 | P4 可用? | P3 wazero 编译模式可用? | P3 wazero 解释模式可用? | 该档最优层 |
|---|---|---|---|---|---|
| **(A) P4 完全可用** | linux/darwin/windows × amd64/arm64,允许 exec-mmap | ✅ | ✅(被 P4 替代) | ✅(被 P4 / 编译模式替代) | **gibbous/jit**(P4) |
| **(B) Go 工具链支持但 P4 不支持** | linux × riscv64 / ppc64le / s390x / mips64 等 | ❌ | ❌(wazero 编译引擎同样不支持) | ✅(wazero 解释模式可用) | **crescent**(大概率;待实测,见 §3.2 / §4.1) |
| **(C) 禁 exec-mmap** | iOS、seccomp 沙箱、某些云 FaaS 受限沙箱 | ❌ | ❌(wazero 编译模式同样需 exec-mmap) | ✅(wazero 解释模式不需 exec-mmap) | **crescent**(同上) |

**关键观察**:
- 档 (A) P4 直接接管,P3 退役选项的最大动机来源——P3 在该档无独立价值,与 P4 100% 重叠;
- 档 (B) (C) 在 P4 不可用环境下,真正需要选型的是「crescent vs P3 解释模式」——§1 的拆穿正在此档发生;
- 任何「P3 留作可移植中层」的论据都必须给出在档 (B) (C) 下「P3 解释模式快于 crescent」的实测证据,否则缺省 crescent。

### 2.2 wazero 平台支持矩阵考据

wazero 项目本身的平台支持矩阵(P4 验收时可由 wazero 当前版本自报数据再核对一次,本文先录粗判):

| wazero 模式 | 支持的架构 | 物理依赖 |
|---|---|---|
| **`RuntimeConfigCompiler`(编译模式)** | amd64 / arm64 | exec-mmap + 各 OS 平台 codegen 后端 |
| **`RuntimeConfigInterpreter`(解释模式)** | 任何 Go 工具链可编译目标 | 仅纯 Go,无 exec-mmap 依赖 |

**与 P4 的对照**:

| 维度 | P4 | wazero 编译模式 |
|---|---|---|
| 架构 | amd64 / arm64 | amd64 / arm64 |
| exec-mmap | 必需 | 必需 |
| W^X 系统管线 | 必需 | 必需(wazero 已实装) |
| icache flush(arm64) | 必需 | 必需(wazero 已实装) |
| trampoline | 必需 | 必需 |

**结论**:wazero 编译引擎与 P4 共享同一组平台约束——「P4 不可用」与「wazero 编译模式不可用」是同一组条件;在 P4 不可用平台上,wazero 自动退化为解释模式,这是 §1.3 物理事实的工程根源。

### 2.3 每档逐档展开

**档 (A) P4 完全可用——gibbous/jit 最优**

- P4 在档 (A) 兑现 luajc 档([./01-launch-judgment.md](./01-launch-judgment.md) §4.1);
- P3 在档 (A) 与 P4 重叠 → 退役动机最大;
- 档 (A) 是 §5.1「缺省倾向」覆盖的环境。

**档 (B) Go 工具链支持但 P4 不支持——crescent 最优(大概率)**

- 该档代表用户:服务端 Linux 跑非 amd64/arm64 服务器(如 IBM mainframe + s390x、华为 / 阿里云的 ARM 之外的 RISC 类硬件如 riscv64);
- crescent 在该档跑 Lua 字节码原速;
- P3 在该档退化为「wazero 解释模式跑翻译产物」,§1.4 / §1.5 论证后大概率不快反慢;
- 档 (B) 是 §4.1 翻案条件的主要发生场景。

**档 (C) 禁 exec-mmap——crescent 最优(大概率)**

- 该档代表用户:iOS 嵌入(App Store 审核要求禁 exec-mmap)、seccomp 沙箱(某些 SaaS 平台限制 jit)、某些 FaaS 受限沙箱;
- crescent 在该档同样原速;
- P3 在该档同档 (B);
- 档 (C) 是 §4.2 翻案条件的主要发生场景。

### 2.4 决策矩阵的纪律

| 纪律点 | 内容 |
|---|---|
| **不预设结论** | 决策矩阵把每档最优层标「大概率」而非「绝对」——§3 实测可翻档 (B) (C) 的判定 |
| **可移植中层主张需对应档实测证据** | 任何「P3 留作中层」论据都须落到具体档 (B) (C) 下的实测;不允许笼统说「P3 在 P4 不支持平台上有用」 |
| **数据进档** | P4 验收时若做了档 (B) (C) 的实测,数据进 [./implementation-progress.md](./implementation-progress.md);未做实测则 §5.1 缺省倾向生效 |
| **覆盖面承诺与决策矩阵的关系** | 项目战略层若决定承诺支持档 (B) (C) → §3.2 实测必跑;若未承诺 → 决策矩阵自动收窄到档 (A),§5.1 缺省倾向自动生效 |


---

## 3. 决策的实测口径(P4 验收时跑什么)

### 3.1 列内核基准:amd64 P4 vs amd64 crescent vs amd64 P3(档 (A) 的对照)

承 [./01-launch-judgment.md](./01-launch-judgment.md) §4.2 列内核基准硬约束([../p1-interpreter/12-testing-difftest.md](../p1-interpreter/12-testing-difftest.md) §6.1),P4 验收时档 (A) 的三方对照基准:

| 对照项 | 内容 | 用途 |
|---|---|---|
| **amd64 P4(gibbous/jit)** | 列内核负载在 P4 下的 ns/op | P4 验收主指标(luajc 档) |
| **amd64 crescent(P1)** | 同负载在 crescent 解释器下的 ns/op | 基线对照(P4 / crescent 应 ≥3.5x,详 [./01-launch-judgment.md](./01-launch-judgment.md) §4.5) |
| **amd64 P3(gibbous/wasm)** | 同负载在 P3 wazero 编译模式下的 ns/op(2026-06-16 实测基线 loop 2.95x / table 0.88x / call 0.52x / mixed 0.99x,详 [../p3-wasm-tier/implementation-progress.md](../p3-wasm-tier/implementation-progress.md) §0) | P4 / P3 收益差判定;若 P4 / P3 ≤ 1.0(P4 不快于 P3),P4 立项前提失败 |

档 (A) 三方对照的口径已在 P3 PW10 收口时部分铺好(P3 vs crescent 对照已有 2026-06-16 基线);P4 验收时只需追加 P4 列即可。

**档 (A) 的对照本身不直接产出去留决策**——档 (A) 下 P3 与 P4 重叠,缺省 P3 退役;但若档 (A) 实测显示 P3 在某些 kernel 上意外比 P4 还快(罕见且暗示 P4 实施不彻底),需先回 P4 验收闸门重新评估,而非把 P3 留下做兜底(否则两层共存会掩盖 P4 实施问题)。

### 3.2 平台覆盖检验:在 ≥1 个 P4 不可用环境上跑三方对照

档 (B) (C) 的实测口径——在至少 1 个 P4 不可用环境(如 riscv64 Linux,或 seccomp 沙箱)上跑「crescent vs P3 解释模式 vs P3 编译模式」三方对照:

| 对照项 | 该环境下可用性 | 期望结果 |
|---|---|---|
| **crescent** | ✅ | 列内核 ns/op 基线 |
| **P3 解释模式(wazero `RuntimeConfigInterpreter`)** | ✅ | 与 crescent 对照,验证 §1.4 / §1.5 论证 |
| **P3 编译模式(wazero `RuntimeConfigCompiler`)** | ❌(预期不可用) | 不可用本身是档 (B) (C) 物理事实的正面证据 |

**实测纪律**:

| 纪律点 | 内容 |
|---|---|
| **环境数 ≥ 1** | 至少 1 个 P4 不可用环境实测;若项目战略层承诺多档(如同时承诺 riscv64 + iOS),需多档分别实测 |
| **基准与档 (A) 同形** | 用同一组列内核负载 + 同一份 Lua 脚本,跨档可比 |
| **数据进档** | 三方对照实测数据写入 [./implementation-progress.md](./implementation-progress.md) 的 P3 去留决策节;未跑实测则 §5.1 缺省倾向生效 |
| **不强求** | 若项目战略层未承诺档 (B) (C),实测可不跑(覆盖面收窄到档 (A));但留中层选项随之失去判据,自动归到 §5.1 缺省倾向 |

### 3.3 真实宿主需求:由首个宿主提出明确需求才进入考量

承 §0.2 输入分类「真实宿主需求」,实测口径:

| 需求类 | 触发条件 | 数据来源 |
|---|---|---|
| **iOS 嵌入需求** | 首个或后续宿主侧提出「需要在 iOS App 里嵌入 wangshu」明确诉求 | 宿主侧 issue / 设计需求文档 |
| **seccomp 沙箱嵌入需求** | 宿主侧提出「需要在某 SaaS 平台 seccomp 沙箱内运行 wangshu」诉求 | 同上 |
| **多架构覆盖需求** | 项目战略层提出「需要承诺 riscv64 / ppc64le / s390x 等架构支持」 | 项目 roadmap / overview 决议 |

**纪律**:

| 纪律点 | 内容 |
|---|---|
| **「假设需求」不进入考量** | 不为「假设的未来 iOS 用户」预留 P3 留中层路径——这是 [../../../llmdoc/guides/design-claims-vs-codebase-physics.md](../../../llmdoc/guides/design-claims-vs-codebase-physics.md) 同源纪律「主张须证据,不能凭直觉」在去留决策上的对位 |
| **明确需求触发实测** | 真实宿主需求一旦明确,进 §3.2 实测该宿主目标平台,数据进档 |
| **需求时点** | 需求时点不可控——本文 §10.2 风险已写明:若需求只在 P4 验收后才浮现,需要双后端的应急保留方案 |

### 3.4 实测数据的归档与裁决纪律

承 [./01-launch-judgment.md](./01-launch-judgment.md) §5.3「数据进档协议」同源:

| 数据 | 归档点 | 用途 |
|---|---|---|
| §3.1 档 (A) 三方对照实测 | [./implementation-progress.md](./implementation-progress.md) | P4 验收主指标 + P3 去留决策档 (A) 输入 |
| §3.2 档 (B) (C) 实测(若跑) | [./implementation-progress.md](./implementation-progress.md) | P3 去留决策档 (B) (C) 输入 |
| §3.3 真实宿主需求(若有) | [./implementation-progress.md](./implementation-progress.md) + [doc-gaps](../../../llmdoc/memory/doc-gaps.md) | 留中层路径的合法性凭据 |
| **去留决策档位决议** | [./implementation-progress.md](./implementation-progress.md)(P4 验收时建立) | P3 去留二选一拍板 |

**裁决主体**:由用户(项目主决策者)与主助理共同裁决,数据进档,本文不替用户拍板某档。这与 [./01-launch-judgment.md](./01-launch-judgment.md) §3.4 立项判定纪律对位。


---

## 4. 翻案条件(留 P3 的硬条件)

### 4.1 「wazero 解释模式跑翻译产物」在某类真实负载上仍显著快于 crescent

§1.5 论证「P3 留作中层」的表面论据被拆穿后,真正能让「留中层」成立的硬条件:

> **在档 (B) (C) 至少一个平台上,实测「wazero 解释模式跑 Lua → Wasm 翻译产物」在某类真实负载上的 ns/op 显著快于「crescent 直接解释 Lua 字节码」**(显著 = ≥ 2x 加速,具体阈值由项目战略层定)。

可能性评估:

| 评估维度 | 评估 |
|---|---|
| **物理可能性** | 极低——隔了一层翻译再解释,wazero 解释器的实现质量不在 Lua 形态特化路径上,crescent 是手写 Lua 解释器有诸多 Lua 特化优化(如 PW10 R1-R2-R3-R3.5 优化 / NaN-box 直接寻址),P3 解释模式不具备这些 |
| **在某些 Lua 形态下可能** | 理论上若 Lua 程序极规则(纯算术循环、无 metatable、无 IC),Wasm 翻译产物可能在 wazero 解释器下有更紧凑的 dispatch loop;但这一假设只能实测确认,不可凭直觉押 |
| **实测翻案的工程动作** | §3.2 档 (B) (C) 实测必须跑,数据进档;若数据真的显示 P3 解释模式 ≥ 2x 快于 crescent,翻案条件 4.1 成立,留中层选项进入候选 |

### 4.2 真实宿主明确需求(iOS 嵌入 / seccomp 沙箱)且实测验证 P3 加速

承 §3.3,若有真实宿主提出明确需求(如 iOS 嵌入 / seccomp 沙箱),且 §3.2 实测显示 P3 解释模式在该宿主目标负载上加速:

| 翻案触发链 | 内容 |
|---|---|
| **触发** | 真实宿主提出明确需求 + §3.2 实测验证 P3 加速 |
| **结论** | 留中层选项进入候选;此时进 §7 工程动作清单 |
| **风险** | 需求时点滞后(详 §10.2)——若需求在 P4 验收后才浮现,P3 已退役,需考虑应急保留方案(见 §10.2) |

### 4.3 翻案条件的合并

§4.1 + §4.2 形成「OR」关系——任一成立即留中层选项可候选;两者都不成立即缺省 P3 退役(§5.1):

```
[P4 验收数据进档]
       │
       ▼
[§4.1 实测 P3 解释模式快于 crescent ≥ 2x?]
   ┌─────┴─────┐
   ▼           ▼
   是          否
   │           │
   │           ▼
   │      [§4.2 真实宿主明确需求 + 实测 P3 加速?]
   │      ┌─────┴─────┐
   │      ▼           ▼
   │      是          否
   │      │           │
   ▼      ▼           ▼
留中层选项可候选    退役选项缺省(§5.1)
   │
   ▼
进 §7 工程动作清单
```

**纪律**:翻案条件是 OR 关系而非 AND——任一成立即可留中层;两者都需要实测数据支撑,不接受「凭直觉论证」的留中层路径。

### 4.4 翻案条件的工程意义

承 §3.3,若 P4 验收时 §4.1 / §4.2 翻案条件均不成立,但项目战略层主动决定保留 P3 作未来扩展的预留——这是合法但有维护成本的形态(详 §10.3 风险),需在 [./implementation-progress.md](./implementation-progress.md) 显式记录「保留 P3 作为预留,未来若 §4.1 / §4.2 触发再实施 §7 工程动作」。

但此形态不是 §4.1 / §4.2 翻案,而是「主动保留」——决策权归项目战略层,本文不替决策者拍板,只列工程意义。


---

## 5. 缺省倾向 + 决策时点

### 5.1 缺省倾向:P4 验收通过后,P3 退役

承 §1.5 + §2.4 + §4.3,本文给出的缺省倾向:

> **P4 验收通过后,P3 退役**——`internal/gibbous/wasm` 代码留版本史移除主分支,difftest 移除 wasm runner,降低差分矩阵 / 降低维护面 / 降低单一外部依赖(wazero)。

缺省倾向的论证基础:

| 论证点 | 内容 | 单一事实源 |
|---|---|---|
| **档 (A) P3 与 P4 重叠** | 档 (A) 下 P3 无独立价值,与 P4 100% 覆盖重叠 | §2.3 档 (A) 行 |
| **档 (B) (C) P3 解释模式不是兜底** | §1.4 / §1.5 论证「wazero 解释模式跑翻译产物」大概率不快反慢于 crescent | §1.5 |
| **维护成本** | 双后端 long tail bug + wazero 版本升级 + 差分矩阵扩大 | §10.3 风险 |
| **外部依赖最小化** | wangshu 主库 zero 外部依赖纪律(详 [../../../llmdoc/memory/reflections/2026-06-12-issue1-api-gap-round.md](../../../llmdoc/memory/reflections/2026-06-12-issue1-api-gap-round.md) 短评:`d1ff096` 主库零外部依赖)——退役 P3 即从 go.mod 删除 wazero 依赖 |

**缺省倾向的纪律意义**:在 §3 实测口径未跑或 §4 翻案条件不满足时,缺省 P3 退役;留中层路径必须有实测数据 + 真实宿主需求 / 项目战略承诺支撑。

### 5.2 决策时点:P4 验收

承 §0.3 双向决策的下半场,P4 §1 是上半场:

| 决策点 | 时点 | 决策内容 |
|---|---|---|
| **P4 立项判定**([./01-launch-judgment.md](./01-launch-judgment.md)) | P3 收口后,P4 实施前 | P4 该不该做 / 何时做 |
| **P4 实施期** | P4 立项后 | per-function 模板编译 + 投机 + OSR + 双后端实装 |
| **P4 验收** | P4 实施完成后 | luajc 档达标判定 + V1-V18 差分套全过 |
| **P3 去留决议**(本文) | P4 验收通过后 | P3 退役 / 留中层 |

**P3 去留决议不早于也不晚于 P4 验收**:

| 时点偏移 | 后果 |
|---|---|
| **早于 P4 验收**(P4 验收前预设 P3 去留) | 把决策建立在「P4 一定通过」的乐观假设上,违反 [../../../llmdoc/guides/design-claims-vs-codebase-physics.md](../../../llmdoc/guides/design-claims-vs-codebase-physics.md) 同源纪律 |
| **晚于 P4 验收 + 数据归档**(P4 验收后长期不裁) | 双后端共存的维护税持续,差分矩阵双倍跑;不利于团队聚焦 |
| **正好在 P4 验收时**(本文承诺) | 数据齐 + 决策即时,工程节奏对齐 |

### 5.3 结构上不预设结论:gibbous/wasm 与 gibbous/jit 同 tier 同接口

承 [../architecture.md](../architecture.md) §1 包布局:

```
internal/gibbous/wasm  ← P3 包
internal/gibbous/jit   ← P4 包
```

两包并列同 tier,共享 P2 前端([../p2-bridge/05-p3-p4-interface.md](../p2-bridge/05-p3-p4-interface.md) §0.3 接口稳定性)——`P3Compiler` 接口已稳,P3/P4 都按同一份接口对接,P2 实现端零修改。

**接口稳定性使两后端共存零成本**:

| 维度 | 内容 |
|---|---|
| **P2 端零修改** | 不论 P3 退役还是留中层,P2 接口不动 |
| **P3 与 P4 共用 GibbousCode 抽象** | [../p2-bridge/05-p3-p4-interface.md](../p2-bridge/05-p3-p4-interface.md) §0.4 GibbousCode 共同接口(Run / Dispose / GetTrampoline),两后端各自实现 |
| **共存的结构自由度零成本保留** | 即便缺省倾向 P3 退役,结构上保留共存自由度;翻案条件成立后切到留中层路径无需重做接口设计 |

**这正是架构决策与策略决策的分离**:

| 层 | 决策内容 | 自由度 |
|---|---|---|
| **架构层**(P2 接口 / GibbousCode 抽象) | 允许 P3/P4 共存(结构自由度) | 零成本保留 |
| **策略层**(本文 P3 去留) | 按 P4 验收数据裁剪(P3 退役 vs 留中层) | 数据进档后裁决 |

### 5.4 架构决策与策略决策分离的纪律

承 §5.3,这一分离是 wangshu 项目的工程哲学——结构允许的形态不必锁死,具体形态由实测数据裁:

| 纪律点 | 内容 |
|---|---|
| **架构层不预设策略层结论** | 包布局 / 接口设计不预设「P3 一定会退役」或「P3 一定会留中层」 |
| **策略层不动架构层** | P3 去留决议不需要改 P2 接口 / GibbousCode 抽象 |
| **数据驱动策略** | P3 去留按 §3 实测口径 + §4 翻案条件裁决,不按工程直觉 |
| **策略层产出不可逆性分级** | 退役不可逆(代码删除);留中层可逆(后续可退役)——这一不对称使「先留中层,后续若 §4.1 / §4.2 不再触发再退役」是合法路径 |

### 5.5 决策推迟到 P4 验收时用数据定,记入 doc-gaps

承 §0.2 + §3.4,本文不强求 P4 验收前给出去留结论——决策推迟到 P4 验收时,基于实测数据裁。在此期间:

| 状态 | 处理 |
|---|---|
| **决策未做** | 状态记入 [doc-gaps](../../../llmdoc/memory/doc-gaps.md):「P3 去留决议(本文 §5.2):P4 验收时用数据定」 |
| **P4 验收完成 + 决策做出** | 决策档位决议 + 数据进 [./implementation-progress.md](./implementation-progress.md);doc-gaps 该项移除 |
| **P4 立项被否决**(走 [./01-launch-judgment.md](./01-launch-judgment.md) §3.4 跳过档) | P3 去留决议本身保留;P3 PW0-PW10 现状作为 wangshu 当前面 |

### 5.6 D2 决议(2026-07-01 用户拍板):主动保留 P3

承 [./09-acceptance-checklist.md](./09-acceptance-checklist.md) §2 D2 行,用户于 P4 验收后拍板 P3 去留:**主动保留形态**(本文 §10.2),既不走 §6 退役也不走 §7 留中层。

**拍板依据**:

| 输入类 | 现状 |
|---|---|
| **§4.1 实测翻案** | 未成立——档 (B)(C) 未跑 §3.2 三方对照实测,用户未承诺覆盖 riscv64/ppc64le/s390x |
| **§4.2 真实宿主需求** | 未成立——首个宿主定位 gopher-lua alternative,未见 iOS/seccomp 明确需求 |
| **档 (A) 事实** | P4 在 amd64/arm64 三平台 CI 全绿(V15b heavy 5.53x/5.45x/4.00x over gopher,V16 边界快 P3 1.4-2.0x),P3 与 P4 完全重叠 |
| **决策选型** | 缺省倾向本应退役(§5.1);用户主动选择主动保留形态,是低成本对冲 §10.2 「需求时点滞后」风险的应急保留方案 |

**主动保留形态与 §6 退役 / §7 留中层的三方对比**:

| 维度 | §6 退役 | §7 留中层 | **§10.2 主动保留** (本决议) |
|---|---|---|---|
| **代码** | `git rm -r internal/gibbous/wasm` | 主分支活维护 | 保留主分支 |
| **go.mod wazero** | 删除 | 持续升版本配对验证 | 版本锁死不升 |
| **build tag** | 整体删 | 平台矩阵化 (P4 平台走 jit,其它走 wasm) | `//go:build wangshu_p3` deprecated 标记 |
| **CI 差分矩阵** | crescent vs P4 双方 | crescent vs P3 vs P4 三方 | 保留 P4 build 独立跑 (三平台 CI 已在),P3 build 不承诺持续绿 |
| **wazero 升级** | 一次性移除 | 长期配对验证 | 锁死当前版本,break 风险接受 |
| **承诺面** | 只 amd64/arm64 exec-mmap | 承诺 riscv64/ppc64le/s390x/iOS/seccomp | 只 amd64/arm64 exec-mmap (与退役同,不承诺 P3 场景) |
| **维护成本** | 收窄 | 长期双份 | 接近 zero (代码留但不主动维护) |
| **未来「捡回」代价** | 高 (从 git log 恢复 + 对齐当前主分支) | 无 (已在主分支) | 中 (代码在,但 wazero break API 时需 spike 修) |
| **性质** | 决策不可逆 | 承诺面扩大 | 应急保留,可逆 |

**工程实施(2026-07-01 起,点到名)**:

| 动作 | 内容 |
|---|---|
| **代码留** | `internal/gibbous/wasm` 保留;不做主动改动,不引入新功能 |
| **build tag** | `wangshu_p3` tag 继续在 CI test-p3 job 里跑 (确保当前 wazero 版本下 byte-equal 不退化) |
| **wazero 版本** | `go.mod` 锁死当前 wazero 版本,不主动升级;wazero 若有 security patch 才升 |
| **文档** | P3 子目录文档头注加「P4 已上线 + D2 主动保留;P3 是设计资产不是产品能力,若未来 §4.1/§4.2 翻案条件浮现再评估升级到 §7 留中层」;不转「遗产」标记 (§6.4 是退役形态动作,本决议不适用) |
| **CI 双后端** | 当前 tri-platform CI 已跑 p1/p3/p4 三 build,保持现状;不因决议改动 CI 结构 |
| **触发升级到 §7 留中层的条件** | 未来若 §4.1 实测证据出现,或 §4.2 真实宿主需求明确,评估是否升级到 §7 留中层;条件不满足前维持主动保留 |

**触发退到 §6 退役的条件**:若未来 wazero break API 且没有低成本 patch 路径,重新评估退到 §6 退役 (代价:重跑 §3.2 实测确认无 §4 翻案条件成立)。

**决议归档**:本节 + [./09-acceptance-checklist.md](./09-acceptance-checklist.md) §2 D2 行 + [./implementation-progress.md](./implementation-progress.md) 三处同步。


---

## 6. 退役的工程动作清单(点到名,不展开实施)

承 §5.1 缺省倾向,本节列出「P3 退役」分支的工程动作清单——本文点到名,不展开具体工程实施(具体落 [./implementation-progress.md](./implementation-progress.md) 或 P4 上线时实操):

| 动作类 | 内容 | 单一事实源 / 落点 |
|---|---|---|
| **6.1 difftest 移除 P3 runner** | 移除 [../p1-interpreter/12-testing-difftest.md](../p1-interpreter/12-testing-difftest.md) §3.8 Runner 抽象的 `WangshuGibbous`(wasm)runner;P3 与 P4 的差分由 P4 runner 接续 | [../p1-interpreter/12-testing-difftest.md](../p1-interpreter/12-testing-difftest.md) §3.8 |
| **6.2 包路径** | `internal/gibbous/wasm` 留版本史(git log 可查),从主分支移除;主分支只剩 `internal/gibbous/jit` | [../architecture.md](../architecture.md) §1 |
| **6.3 wazero 依赖** | 从 `go.mod` 删除 wazero 依赖;主库 zero 外部依赖纪律强化 | [../../../llmdoc/memory/reflections/2026-06-12-issue1-api-gap-round.md](../../../llmdoc/memory/reflections/2026-06-12-issue1-api-gap-round.md) 短评:`d1ff096` |
| **6.4 文档:P3 子目录转「P4 继承的分层协议遗产」标记** | `docs/design/p3-wasm-tier/` 子目录文档不删,转为「P4 继承的分层协议遗产」状态标记;子文档头注加状态:「P3 已退役,本文档作为 P4 继承的分层协议遗产保留;P4 验收时点标记于 [./implementation-progress.md](./implementation-progress.md)」;链给 P4 同章 | 该子目录每个文档头部 |
| **6.5 build tag 迁移** | 若 P4 期间 P3/P4 共存(发布过渡期):p3 build tag 标 deprecated → 后续 release 删 | go build tag 工程惯例 |

### 6.1 difftest 移除 P3 runner(细化)

P3 退役后,层间差分由 P4 runner 接续:

| 阶段 | 差分形态 |
|---|---|
| **P3 上线时** | crescent vs P3-wasm byte-equal([../p3-wasm-tier/08-testing-strategy.md](../p3-wasm-tier/08-testing-strategy.md) V1-V13) |
| **P4 上线时(P3 共存)** | crescent vs P3-wasm vs P4-jit 三方 byte-equal([./08-testing-strategy.md](./08-testing-strategy.md) 接续) |
| **P3 退役后** | crescent vs P4-jit byte-equal(差分矩阵收窄) |

### 6.2 包路径(细化)

`internal/gibbous/wasm` 退役的物理形态:

| 时点 | 主分支状态 |
|---|---|
| **P4 验收前** | `internal/gibbous/wasm` + `internal/gibbous/jit` 两包共存 |
| **P4 验收通过 + 决策 P3 退役** | `git rm -r internal/gibbous/wasm`;主分支只剩 `internal/gibbous/jit`;历史代码 git log / git tag 可查 |
| **后续若需要回看** | `git log --all` 找最后保留 P3 的 commit / tag;无需保留主分支冗余代码 |

### 6.3 wazero 依赖(细化)

主库 `go.mod` 的 zero 外部依赖纪律是 wangshu 项目的核心工程纪律之一:

| 时点 | go.mod 状态 |
|---|---|
| **P3 上线后** | wazero 是主库唯一外部依赖 |
| **P4 上线时(P3 共存)** | wazero 仍是主库依赖;P4 不引入新外部依赖 |
| **P3 退役后** | wazero 从 go.mod 删除;主库回归 zero 外部依赖纪律 |

### 6.4 文档转遗产标记(细化)

P3 子目录文档的转标:

| 文档 | 转标后状态 |
|---|---|
| `p3-wasm-tier/00-overview.md` | 头注加「P3 已退役于 [日期] / [P4 验收 commit];本文档保留作为 P4 继承的分层协议遗产」+ 退役决策依据指向本文 §5 + [./implementation-progress.md](./implementation-progress.md) 决策档位决议 |
| 其它 P3 子文档 | 同上头注 + 「具体实施细节已退役,但分层协议设计资产被 P4 继承」标记 |

### 6.5 build tag 迁移(细化)

若 P4 上线时 P3/P4 短期共存(发布节奏需要):

| 阶段 | build tag 形态 |
|---|---|
| **P4 上线 v1.x** | `//go:build p3` 包内文件保留,默认 build 不带 p3 tag → P3 不参与编译;仍可用 `go build -tags p3` 跑 P3 用作过渡期对比 |
| **P4 稳定运行 v1.y(y > x)** | p3 build tag 整体删除;`internal/gibbous/wasm` 整体 git rm |

build tag 阶段是退役的过渡期,不是常态;主分支总归是 P3 整体移除。


---

## 7. 留作可移植中层的工程动作清单(点到名,不展开实施)

承 §4 翻案条件成立分支,本节列出「P3 留中层」分支的工程动作清单——同款「点到名,不展开实施」纪律:

| 动作类 | 内容 | 落点 |
|---|---|---|
| **7.1 平台 build tag 矩阵** | P4 平台(amd64/arm64 + exec-mmap)→ `internal/gibbous/jit`;P3 平台(其它)→ `internal/gibbous/wasm` | go build tag 工程实施 |
| **7.2 升层判定加平台维度** | considerPromotion 接口扩展:增加平台维度,P4 平台走 jit promote,P3 平台走 wasm promote | [../p2-bridge/04-try-compile-fallback.md](../p2-bridge/04-try-compile-fallback.md) considerPromotion 接口扩展回填请求 |
| **7.3 维护成本估算** | 双后端 long tail bug + wazero 版本升级 + 差分矩阵扩大 | §10.3 风险 |
| **7.4 退路** | 发现「P3 解释模式比 crescent 慢」的硬证据后退役 | §4.1 翻案条件失效后回 §6 |

### 7.1 平台 build tag 矩阵(细化)

| build 形态 | tag | 含义 |
|---|---|---|
| **默认 build(amd64/arm64 + exec)** | 无特殊 tag | 走 jit;wasm 包不参与编译 |
| **archived 平台(riscv64 / ppc64le / s390x)** | go build tag 自动选 wasm | 走 wasm;jit 包不参与编译 |
| **沙箱受限**(seccomp / iOS) | 显式 build tag(如 `noexec`) | 走 wasm;jit 包不参与编译 |

build tag 形态需在 P4 实施期具体实装(不展开 here)——本文只点到名:留中层路径需要 build tag 矩阵化处理。

### 7.2 升层判定加平台维度(细化)

考虑到 P2 决策机的形态:

| 形态 | 内容 |
|---|---|
| **P3 / P4 共存时** | considerPromotion 不知道走哪个后端,需要根据当前 build tag 选择;接口形态可能是 `P3Compiler`(wasm)+ `P4Compiler`(jit)各一个,或单一 `GibbousCompiler` 接口在不同 build tag 下注入不同实现 |
| **接口扩展回填请求** | 本文 §11 列出对 [../p2-bridge/04-try-compile-fallback.md](../p2-bridge/04-try-compile-fallback.md) 的回填请求,本文不主动改 P2 现稿 |

### 7.3 维护成本估算(细化)

留中层路径的长期维护成本:

| 成本项 | 内容 |
|---|---|
| **double bug surface** | wasm 后端 + jit 后端 long tail bug 各一份;每个 issue 需在双后端核对 |
| **wazero 版本升级** | wazero 升版本需在 wasm tier 配对验证;P3 退役后此成本消除 |
| **差分矩阵扩大** | crescent vs wasm vs jit 三方 byte-equal;CI 时长扩大;尤其 GC 压力 fuzz 在双后端跑两遍 |
| **文档维护** | P3 子目录文档需持续维护(不能转遗产标记);P4 子目录文档同步;两组都活在主分支 |
| **依赖维护** | wazero 在 go.mod 持续保留;主库 zero 外部依赖纪律破例 |

### 7.4 退路(细化)

留中层后,后续若 §4.1 翻案条件失效(实测显示 P3 解释模式不快于 crescent),退路:

```
[留中层 v1.x]
       │
       ▼
[后续 release 期实测复测 §3.2]
       │
       ▼
   [P3 解释模式 vs crescent 数据]
       │
   ┌───┴───┐
   ▼       ▼
   仍快    不快
   │       │
   │       ▼
   │   退到 §6 退役工程动作清单
   ▼
   留中层维持
```

退路存在使「先留中层,后续实测复测后退役」是合法路径;退役不可逆(代码删除)+ 留中层可逆 的不对称使决策风险偏向「先留后退」——但这一不对称的代价是双后端长期维护(§7.3)。

### 7.5 留中层路径的二阶段实施

承 §7.1-§7.4,留中层路径分二阶段:

| 阶段 | 内容 |
|---|---|
| **阶段 1:P4 验收时立即实施** | build tag 矩阵 + considerPromotion 接口扩展 + 双后端 CI 配置 |
| **阶段 2:后续维护期** | wazero 升版本配对验证 + 差分矩阵跑 + 文档同步;复测期间若 §4.1 失效则退到 §6 |


---

## 8. 跳跃路径(P3 从未存在)的本节自动消解

### 8.1 跳跃路径的形态

承 [./01-launch-judgment.md](./01-launch-judgment.md) §2.2,「跳跃路径」是 P3 PW0 spike 不达标时跳过 P3 直接做 P4 的备用形态。该形态下:

| 形态 | 内容 |
|---|---|
| **P3 状态** | spike 否决,从未上线;`internal/gibbous/wasm` 包从未存在 |
| **P4 状态** | 自建分层骨架(原 P3 战略价值移入 P4) |
| **去留决策** | 不存在——P3 不存在,无可去留 |

### 8.2 本节自动消解

若跳跃路径成立(P3 从未存在),本节(P3 去留决策框架)自动消解:

| 消解动作 | 内容 |
|---|---|
| **本文不再适用** | 无 P3 可去留,本文 §1-§7 的决策矩阵 / 实测口径 / 退役工程动作 / 留中层工程动作均不发生 |
| **P3 文档作 P4 继承遗产** | P3 子目录文档(若仍存在,即设计文档但代码未实施)作为 P4 自建分层骨架的设计参考——这是 [../p3-wasm-tier/01-spike-gate.md](../p3-wasm-tier/01-spike-gate.md) §5.4「跳跃路径下设计资产复用」的具体延伸 |
| **本文章节路标自动失效** | §1-§7 的决策框架不发生;§9 不变式 + §10 风险节按「P3 从未存在」形态简化 |

### 8.3 现实:P3 PW0-PW10 已交付,跳跃路径不复存在

承 [./01-launch-judgment.md](./01-launch-judgment.md) §2.3 现实形态:

| 现实事实 | 内容 |
|---|---|
| **P3 PW0 spike 已通过** | wazero call boundary 实测 36.7ns(≪ 150ns 闸门),详 [../p3-wasm-tier/implementation-progress.md](../p3-wasm-tier/implementation-progress.md) §0.1 spike 报告 |
| **P3 PW1-PW10 全卷交付** | 包骨架 + 翻译器 + 跨层 + 端到端验收(V1-V13 byte-equal + V14 loop 核 2.95x ≥2x)+ PW10 跨层税优化 |
| **跳跃路径在事实层面被消解** | P3 已存在,不存在「跳过 P3 直接做 P4」的现实选项 |

**结论**:本文 §8 写实——**跳跃路径不复存在,本文 §1-§7 决策框架是 P4 上线时实际发生的形态**。本节作为概念完备性记录保留(对偶 [./01-launch-judgment.md](./01-launch-judgment.md) §2.2 同款写实),但不构成现实决策路径。

### 8.4 与 P3 根本性退役决议的关系

承 §0.3 + §5.3,本文 P3 去留决议是 P4 上线时的**初次裁决**——若初次裁决为「留中层」,后续若 §4.1 翻案条件失效可退到「退役」(§7.4 退路);若初次裁决为「退役」,代码已删除,后续若需要「重新启用 P3」需走「P3 重启」流程(本文不展开,属于另一阶段决议)。

「P3 重启」与本文 §8 跳跃路径形态在物理上有部分重叠(都需要从无到有实施 P3),但触发条件不同:

| 形态 | 触发条件 | 实施代价 |
|---|---|---|
| **本文 §8 跳跃路径** | P3 从未存在(PW0 spike 否决) | 高(P4 + P3 同时啃) |
| **P3 重启** | P3 退役后某时点真实需求浮现需重新启用 | 中(代码版本史可查,但需对齐到当前主分支 + wazero 当前版本) |

P3 重启不在本文范围,留 future doc-gaps。


---

## 9. 不变式清单(本文承担)

承 [./00-overview.md](./00-overview.md) §9 的全 P4 不变式(待 00 写时聚合呈现),本子文档承担以下三条 P3 去留决议级不变式:

### 9.1 「框架不给结论」

承 §0.2,P3 去留决议本身不是本文产出物——本文只承担:

- 决策输入分类(§0.2);
- 表面论据拆穿(§1);
- 决策矩阵(§2);
- 实测口径(§3);
- 翻案条件(§4);
- 缺省倾向(§5);
- 工程动作清单(§6 / §7)。

任何在本文给「P3 一定退役」或「P3 一定留中层」最终结论的文字都判否——结论在 P4 验收时按 §3 实测口径与 §4 翻案条件裁决。

### 9.2 「数据定结论,不押 wishful thinking」

承 §3 实测口径:

| 决策类 | 数据来源 |
|---|---|
| **档 (A) 决策** | §3.1 amd64 P4 vs amd64 crescent vs amd64 P3 实测 |
| **档 (B) (C) 决策** | §3.2 P4 不可用平台上 crescent vs P3 解释模式实测 |
| **真实宿主需求** | §3.3 由首个或后续宿主提出明确需求才进入考量 |

任何「凭工程直觉押 P3 解释模式快」「凭假设需求预留 P3 留中层路径」的论据都违反 [../../../llmdoc/guides/design-claims-vs-codebase-physics.md](../../../llmdoc/guides/design-claims-vs-codebase-physics.md) 同源纪律「主张须证据,不能凭直觉」。

### 9.3 「结构共存零成本」

承 §5.3 + §5.4,P2 接口稳定性([../p2-bridge/05-p3-p4-interface.md](../p2-bridge/05-p3-p4-interface.md) §0.3)使两后端共存零成本——结构层允许共存,策略层按数据裁:

| 结构层 | 策略层 |
|---|---|
| 包布局并列(`internal/gibbous/wasm` + `internal/gibbous/jit`) | 不预设结论 |
| `P3Compiler` / `GibbousCode` 接口同形 | 数据进档后裁决 |
| `P2` 决策机零修改 | 退役 / 留中层都不动 P2 |

任何「为了简化结构提前删 P3 包」「为了预设结论锁死接口形态」的论据都违反本条不变式——架构决策与策略决策的分离是 wangshu 项目的工程哲学。

### 9.4 「决策双向产出」

承 §0.3 + [../roadmap.md](../roadmap.md) §5 原则 3,P3 去留决议双向都是合理产出:

| 方向 | 价值 |
|---|---|
| **退役** | 维护面收窄 + zero 外部依赖纪律强化 + 差分矩阵收窄 + 单一发布形态 |
| **留中层** | 平台覆盖扩大 + 真实宿主需求兜底 + 结构自由度兑现 |

不存在「不做决策最好」的形态——闸门级单点决策不可绕过(同款 [./01-launch-judgment.md](./01-launch-judgment.md) §0.2 立项闸门纪律)。


---

## 10. 风险与开放问题

### 10.1 P4 验收数据的代表性

**风险**:P4 验收用的列内核基准([./01-launch-judgment.md](./01-launch-judgment.md) §4.2)若不代表首个目标宿主的实际热路径,P3 去留决策基于不代表性的数据,会得出错决策。

具体场景:

| 场景 | 后果 |
|---|---|
| **验收 Horner 多项式 ≥ luajc 档** | 但宿主真实负载是 call 密集 / table 密集形态 → P4 在该形态下 ≥ luajc 档判定不成立 |
| **决策依据 Horner 数据而非宿主真实负载数据** | P3 退役后若宿主真实负载需要 P3 兜底,代价大 |

**缓解**:P4 验收基准必须包含宿主真实负载形态测试——若宿主是 call 密集形态,验收基准也应含 call 密集核;[./08-testing-strategy.md](./08-testing-strategy.md) 验收口径需把宿主负载形态作为分档(同 [../p3-wasm-tier/implementation-progress.md](../p3-wasm-tier/implementation-progress.md) §14.10 四档分桶)。

**残留**:首个目标宿主的真实负载形态 vs 验收基准形态之间的差距是工程判断题,无法事前完全弥合——只能 P4 验收时尽量贴近真实形态实测。

### 10.2 真实宿主 iOS/seccomp 需求的及时性

**风险**:承 §3.3,真实宿主的 iOS / seccomp 需求若在 P4 验收时未明确(§4.2 翻案条件不触发),P3 退役;若需求只在 P4 验收后才浮现,需要双后端的应急保留方案。

具体场景:

| 时点 | 需求状态 | 后果 |
|---|---|---|
| **P4 验收时** | 无 iOS / seccomp 需求 | 缺省退役 P3 |
| **P4 验收后 6 月内** | 某宿主提出 iOS 嵌入需求 | 需重启 P3(代价见 §8.4 P3 重启) |

**缓解**:

| 缓解项 | 内容 |
|---|---|
| **应急保留方案** | P4 验收时若无明确需求但存在「可能浮现」的概率,可走「主动保留 P3 但不持续维护」的过渡形态——保留代码 + build tag(deprecated 标记)+ wazero 不持续升版本;若需求浮现再「捡回」 |
| **决策记录** | 若选「主动保留」需在 [./implementation-progress.md](./implementation-progress.md) 显式记录,与「§4 翻案条件触发的留中层」区分 |

**残留**:「主动保留」与「留中层」是不同形态——前者不强约束当前面工程纪律,后者是当前 release 的一部分;两者维护成本不同(主动保留维护成本低但被 wazero 升版本 break 风险高)。

### 10.3 wazero 长期演进对 P3 维护成本的影响

**风险**:承 §7.3 维护成本估算,wazero 是活跃维护的开源项目,API 变更 / 性能演进对 P3 留中层路径有持续影响:

| 影响维度 | 内容 |
|---|---|
| **API 变更** | wazero v2.x / v3.x 若有 break 改动,P3 needs 跟进升级;wangshu 需配对验证 |
| **性能演进** | wazero 解释模式若大幅优化,§4.1 翻案条件可能从「不成立」转为「成立」;反之亦然 |
| **平台支持矩阵演进** | wazero 若新增更多架构编译引擎(如 riscv64 编译模式),档 (B) 部分平台从「P4 不可用 + P3 解释模式」转为「P4 不可用 + P3 编译模式」,决策矩阵需重新评估 |

**缓解**:

| 缓解项 | 内容 |
|---|---|
| **版本锁定纪律** | go.mod 锁定 wazero 版本,升版本前重跑 §3.2 实测确认无退化 |
| **持续监测** | wazero 项目动态进 doc-gaps 或长期 watch 列表;wazero 重大变更触发 P3 留中层路径的重新评估 |
| **退役不可逆性的对冲** | 若选退役,主分支移除 wasm 包 → wazero 长期演进对主分支无影响;留中层路径下需持续 watch wazero |

### 10.4 决策时点的工程节奏

**风险**:P4 验收时点本身依赖 P4 立项 + 实施完成——若 P4 验收期长(1-2 人年),P3 状态长期不裁(双后端共存 + 双倍维护),团队聚焦受影响。

**缓解**:

| 缓解项 | 内容 |
|---|---|
| **P4 实施期 P3 维护策略** | P4 实施期 P3 不引入新功能,只做 bug 修复 + 关键漏洞补丁;减少长期共存维护成本 |
| **P4 内部第二闸门**([./01-launch-judgment.md](./01-launch-judgment.md) §4.3) | minimal P4 跑通后即可初步评估「P4 / crescent」收益;若不达 luajc 档预期,P3 去留决议可提前到「P4 重评估时点」而非完整 P4 验收时点 |

### 10.5 开放问题(记入 [doc-gaps](../../../llmdoc/memory/doc-gaps.md))

| 问题 | 待解时点 |
|---|---|
| **P4 验收基准是否覆盖宿主真实负载形态** | P4 验收时;[./08-testing-strategy.md](./08-testing-strategy.md) 配合 |
| **§3.2 档 (B) (C) 实测在哪个具体平台跑** | P4 验收时;依赖项目战略层承诺面 |
| **真实宿主 iOS / seccomp 需求的真实性** | 首个或后续宿主侧确认;P4 验收时之前理想 |
| **wazero 长期演进的具体影响** | P4 验收后持续 watch;退役选择下影响为零,留中层选择下需持续监测 |
| **P3 退役后若需重启的工程实施细节** | 不在本文范围;P3 重启另立文档 |


---

## 11. 回填请求(对 P1/P2/P3 现稿)

本文不主动改 P1/P2/P3 现稿,以下回填请求由主助理收口在 [./implementation-progress.md](./implementation-progress.md):

### 11.1 对 [../p3-wasm-tier/00-overview.md](../p3-wasm-tier/00-overview.md) 的回填请求

- §1 P3/P4 边界表(P1/P2/P3/P4 谁拥有什么)可补一行指向本文,作为 P3 去留决议的指针——0 总览只列 P3 实施层(同 tier 不同后端),去留决议层未点到。

### 11.2 对 [../p3-wasm-tier/01-spike-gate.md](../p3-wasm-tier/01-spike-gate.md) 的回填请求

- §0.2「闸门是单点决策不可绕过」可补一个对位指针指向本文 §0.3——P3 spike 闸门(开工)与本文(去留)是 P3 生命周期上的两个闸门,形态平行。

### 11.3 对 [../p2-bridge/04-try-compile-fallback.md](../p2-bridge/04-try-compile-fallback.md) 的回填请求(仅留中层路径触发)

- considerPromotion 接口扩展加平台维度——本文 §7.2 已点到名,但若决策为「留中层」需在该文档 §2 状态机展开实施细节。本回填**仅在 P4 验收 + 决策为留中层时触发**;若决策为退役,本回填无需执行。

### 11.4 对 [./08-testing-strategy.md](./08-testing-strategy.md) 的回填请求

- 验收基准分档加宿主真实负载形态(§10.1 风险缓解);本文 §10.1 已识别该风险,但具体验收口径展开在 [./08-testing-strategy.md](./08-testing-strategy.md)。

### 11.5 对 [../roadmap.md](../roadmap.md) 的回填请求

- §4 P4 段「Wasm 层退役,或留作可移植中层」措辞可补一句指针指向本文,使该决策框架的单一事实源显式化。

### 11.6 主动回填请求兑现纪律

承 [../../../llmdoc/guides/multi-doc-drafting.md](../../../llmdoc/guides/multi-doc-drafting.md) 「单点收口」纪律,本文回填请求**不主动兑现**——主助理在写完 P4 文档集后统一收口:

| 兑现时机 | 内容 |
|---|---|
| **本子文档完成时** | 各回填请求列在本节,不主动改 P1/P2/P3 文档 |
| **主助理收尾时** | 统一兑现 §11.1-§11.5 各项;不兑现的项保留在 [./implementation-progress.md](./implementation-progress.md) 的「待回填」节 |
| **决策时**(P4 验收时) | 决策档位决议进档时,顺手再扫一遍回填请求是否仍有效——若决策为退役则 §11.3 自动不触发 |

这一纪律保证 P1/P2/P3 现稿不被多个 P4 子文档并行修改造成冲突——回填集中在主助理收口点,主助理负责最终一致性。

---

## 相关

- [../roadmap.md](../roadmap.md)(§4 P4 验收 + P3 去留两个选项的字面措辞 / §5 五条贯穿原则——本文 §0.1 / §5.1 / §9.4 同源)
- [../architecture.md](../architecture.md)(§1 包布局 `internal/gibbous/wasm` 与 `internal/gibbous/jit` 并列——本文 §5.3 物理基础)
- [../../../llmdoc/architecture/evolution-roadmap.md](../../../llmdoc/architecture/evolution-roadmap.md)(tier 映射:P3/P4 同 gibbous tier-1)
- [../../../llmdoc/overview/project-overview.md](../../../llmdoc/overview/project-overview.md)(项目身份 + 首个目标宿主——本文 §3.3 真实宿主需求来源)
- [../../../llmdoc/guides/design-claims-vs-codebase-physics.md](../../../llmdoc/guides/design-claims-vs-codebase-physics.md)(主张须证据,不能凭直觉——本文 §0.2 / §3.3 / §9.2 同源纪律)
- [../../../llmdoc/guides/multi-doc-drafting.md](../../../llmdoc/guides/multi-doc-drafting.md)(单点收口 + 用户裁决——本文 §11.6 同源)
- [../../../llmdoc/memory/reflections/2026-06-12-issue1-api-gap-round.md](../../../llmdoc/memory/reflections/2026-06-12-issue1-api-gap-round.md)(主库 zero 外部依赖纪律 `d1ff096`——本文 §6.3 论据)
- [../../../llmdoc/memory/doc-gaps.md](../../../llmdoc/memory/doc-gaps.md)(P3 去留决议长期登记点;本文 §5.5 / §10.5 同源)
- [../p3-wasm-tier/00-overview.md](../p3-wasm-tier/00-overview.md)(P3 总览,边界表 + tier 映射——本文 §11.1 回填请求目标)
- [../p3-wasm-tier/01-spike-gate.md](../p3-wasm-tier/01-spike-gate.md)(P3 闸门同款定位的对位文档——本文 §11.2 回填请求目标)
- [../p3-wasm-tier/implementation-progress.md](../p3-wasm-tier/implementation-progress.md)(P3 PW0-PW10 实测基线——本文 §3.1 数据来源)
- [../p2-bridge/04-try-compile-fallback.md](../p2-bridge/04-try-compile-fallback.md)(considerPromotion 状态机——本文 §11.3 回填请求目标,仅留中层时触发)
- [../p2-bridge/05-p3-p4-interface.md](../p2-bridge/05-p3-p4-interface.md)(§0.3 接口稳定性——本文 §5.3 物理基础)
- [./00-overview.md](./00-overview.md)(P4 文档集总览,本文是其 §0 文档地图所定的「P3 去留决策框架」单一事实源)
- [./01-launch-judgment.md](./01-launch-judgment.md)(立项闸门同款定位的对位文档,P4 §1 是上半场,本文 §7 是下半场——本文 §0.3 / §5.2 同源)
- [./02-template-direction.md](./02-template-direction.md)(P4 方向裁决,本文 §1.1 / §2.3 提对位)
- [./05-system-pipeline.md](./05-system-pipeline.md)(四项税兑现 + trampoline,本文 §1.1 wazero 平台共享约束论证基础)
- [./06-backends.md](./06-backends.md)(amd64/arm64 双后端,本文 §1.1 / §2.2 P4 平台覆盖论据)
- [./08-testing-strategy.md](./08-testing-strategy.md)(差分接入,本文 §10.1 / §11.4 提对位)
- [./implementation-progress.md](./implementation-progress.md)(P4 验收数据 + P3 去留决策档位决议归档点)
- [../p1-interpreter/12-testing-difftest.md](../p1-interpreter/12-testing-difftest.md)(§3.8 Runner 抽象——本文 §6.1 退役工程动作目标)
- [../p5-trace-jit/00-overview.md](../p5-trace-jit/00-overview.md)(下一站:与 P3 去留决议同 tier 不同形态,但本文不展开)

