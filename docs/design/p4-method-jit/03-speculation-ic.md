# P4 §3:类型投机机制——IC 反馈消费 + f64 快路径 + guard

> 状态:**详细设计**(P4 整体仍是「架构决策深度」,但类型投机是 P4 与 P2/P3 零 deopt 的根本分野——guard 形式、状态机 deopt 边、再训练协议是验收前必须钉死的口径,本文按详细设计深度展开,与 [./04-osr-deopt.md](./04-osr-deopt.md) 同档)。本文是 P4 子文档集的「类型投机机制」单一事实源——IC 反馈如何消费、guard 如何发、状态机如何加 deopt 边一次定稳。
>
> **本文定位一句话**:P4 把 P2 反馈消费成「快路径 + guard,失败 OSR exit 回解释器」——以 P3「快路径 + 慢路径 helper,不离开函数」的非投机翻译为对偶基线,落到代码层是三条可机械验证的不变式:① 快路径检查 = 投机 guard,不是语义分发;② guard 物理形式 = 显式比较 + 条件跳,无信号陷阱;③ 失败着陆面 = OSR exit + 再训练,不在编译码内就地补救。
>
> 上游契约:
> [./02-template-direction.md](./02-template-direction.md)(方向裁决——本文 §2 投机模板叠在「per-function 模板编译」基底上、§4 子集内投机的承诺由本文落具体)、
> [../roadmap](../roadmap.md)(§2 四项税——「runtime 所有权」决定 Go 下信号陷阱不可用,本文 §3 guard 显式化的硬约束源头;§4 P4 验收 = 列内核负载达 luajc 档)、
> [../../../llmdoc/must/design-premises](../../../llmdoc/must/design-premises.md)(前提一负载形状 / 前提二四项税 / 前提三五原则——投机错果是 JIT 第一危险源 / **前提四第一天 NaN-box 承诺——guard 物理形式是单次 u64 比较的现金兑现处**)。
>
> P3 对位(本文核心镜像章节):
> [../p3-wasm-tier/06-ic-feedback-consume](../p3-wasm-tier/06-ic-feedback-consume.md)(P3 IC 非投机消费,882 行,**直接对偶面**——P3 全篇论证「快路径检查 = 语义分发,不是投机 guard」,本文 §1 / §2 / §5 系统翻面给出 P4 = 投机 guard 的镜像论证;P3 §3 各 FeedbackKind 形式与本文 §2 是镜像章节,引用明确指向)、
> [../p3-wasm-tier/02-translation](../p3-wasm-tier/02-translation.md)(P3 翻译器主体——本文 §5 给出 P4 amd64 伪汇编与之物理同形 / 语义异化的对照)。
>
> P2 依赖面(P4 反向读 feedback 接口的单一事实源):
> [../p2-bridge/02-ic-feedback](../p2-bridge/02-ic-feedback.md)(P2 IC 反馈聚合,823 行——上游 TypeFeedback shape 与 confidence 计算单一事实源;§4 PointFeedback 字段定义、§5.1 confidence 物理含义、§6 megamorphic 主动识别)、
> [../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md)(**P4 反向读 feedback 接口的单一事实源**;§1.4 P4 = confidence 强消费方、§4 `P4Feedback.FeedbackFor` 反向读 / `RequestRefresh` 重聚合协议、§5 P4 投机供料语义、§5.6 P4 不依赖 P2 状态机的硬纪律)、
> [../p2-bridge/04-try-compile-fallback](../p2-bridge/04-try-compile-fallback.md)(P2/P3 零 deopt 状态机基线——本文 §4 加 deopt 边的对照参照;§7 不重试纪律——本文 §7.2 拉黑投机的对位)。
>
> P1 已完成的料(投机直接复用的现成基础设施):
> [../p1-interpreter/01-value-object-model](../p1-interpreter/01-value-object-model.md)(§3.2 NaN-box `IsNumber` 单次 u64 无符号比较——本文 §3.4 guard 物理形式、§7 值表示不变式 1「跨 tier 拷贝是 memmove」)、
> [../p1-interpreter/02-bytecode-isa](../p1-interpreter/02-bytecode-isa.md)(§7 ICSlot 结构——P4 投机模板的输入面)、
> [../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md)(§6.2 表 IC、§6.3 表/全局 IC 命中流程、§6.4 算术 IC「写而不读」纯供料——本文 §1 供料链下游)。
>
> 下游协作(同子目录):
> [./04-osr-deopt.md](./04-osr-deopt.md)(OSR exit 物化协议、deopt 状态机、再训练防风暴——本文 §7 只提对位,具体落点写「详 04-osr-deopt §X」,本文不重展开物化协议)、
> [./08-testing-strategy.md](./08-testing-strategy.md)(差分接入「投机错果」最危险 bug 类——本文 §3.5 提名差分主防线,具体口径在 08)。

对应 Go 包:`internal/gibbous/jit`(P4 后端的投机模板与 guard 发射,与 P3 `internal/gibbous/wasm` 同层);上游契约方 `internal/bridge`(`P4Feedback` / `TypeFeedback` 产料);P4 不直接修改 P2 状态字段(§8)。

---

## 0. 定位

### 0.1 一句话:P4 把 P2 反馈消费成「快路径 + guard」

P4 的投机不自己采样,**直接消费 P2 的 `TypeFeedback`**——把 IC 反馈变成两件事:① 在被反馈点贴出 f64 / 直达槽 / 内联方法等**快路径**机器码;② 在快路径前贴出**显式 guard**(比较 + 条件跳)验证投机前提,失败即 OSR exit 回解释器(详 [./04-osr-deopt.md](./04-osr-deopt.md))。这是把「P4 = dispatch 消除器 + IC 投机注入器」的「投机注入器」一面拆开:**反馈消费 + guard 形式 + 失败着陆面** 三件事一次定稳。

P4 投机的边界由 [./02-template-direction.md](./02-template-direction.md) §4 边界表给:做「IC 反馈类型投机:f64 快路径 + guard」、做「函数级 OSR exit 回解释器」、不做「投机失败的细粒度恢复」(那是 P5 snapshot)、不做「跨函数内联」(那是 P5 trace 内联)。本文是这条「做」的展开,以及它如何与 P2/P3 共享前端协作而不破坏其零 deopt 不变式。

### 0.2 与 P3 06-ic-feedback-consume 的对偶面

[../p3-wasm-tier/06-ic-feedback-consume](../p3-wasm-tier/06-ic-feedback-consume.md)(下称 P3 06)全篇论证 P3「快路径检查 = 语义分发,不是投机 guard」(P3 06 §1)——**本文是该论证在 P4 维度的整体翻面**。两文之间的镜像关系是本文的重要骨架:

| 维度 | P3 06(非投机) | 本文(投机) |
|---|---|---|
| 章节定位 | P3 是兑现机器,不是投机机器(P3 06 §0) | P4 是投机机器,deopt 兜底(本文 §0) |
| 快路径检查的语义 | 语义分发(失败也是合法路径,落到慢路径助手得正确结果)(P3 06 §1) | 投机 guard(失败 ⇒ 投机前提不成立,OSR exit)(本文 §1 / §3) |
| 失败后续路径 | 调慢路径 imported helper(同函数内仍跑 Wasm)(P3 06 §1.1) | OSR exit 出 JIT 世界,crescent 接管该帧剩余执行(本文 §7.1) |
| 函数边界 | 不离开 Wasm 函数(P3 06 §1.1) | 离开 JIT 函数(本文 §7.1) |
| feedback 错的代价 | 仅性能(慢路径仍正确)(P3 06 §1.5) | 性能(deopt 开销 ~µs)+ 反复时拉黑投机(本文 §7.2) |
| confidence 字段 | 弱消费(可只看 Kind 不读 confidence)(P3 06 §3.1)| 强消费(≥0.99 才发投机模板)(本文 §2.7) |
| 是否需要 OSR/deopt 机器 | **不需要**(P3 06 §0.2 与 P2 零 deopt 字面一致) | **需要**(本文 §4 状态机加边 + 04-osr-deopt 全篇) |
| 状态机角色 | 单向无环(TierInterp → TierGibbous 吸收态)| 加一条 TierGibbous → TierInterp 的 deopt 边(本文 §4.2) |

**关键观察(承 P3 06 §1.1)**:**两边发的可能都是「比较 + 条件跳」的物理形式,但语义/后续路径完全不同**。P3 的「IsNumber×2 + 内联 f64.add」是**完整的 ADD 翻译**(含所有合法 Lua 语义路径,快+慢);P4 的「IsNumber×2(guard)+ f64.add」是**裁剪版 ADD 投机**(只覆盖 number 路径,其他路径靠 OSR 把执行整体转给 crescent)。**物理同形 ≠ 语义同义**——这是 P3 06 给本文的最核心遗产,本文 §1 / §5 反复回援。

### 0.3 与 OSR exit 的边界

OSR exit 是投机失败的着陆面,本文与 [./04-osr-deopt.md](./04-osr-deopt.md) 分工:

| 内容 | 本文(03)拥有 | 不属于本文(在 04) |
|---|---|---|
| guard 形式硬约束(显式 vs 信号陷阱)| ✅ §3 全节 | — |
| 状态机 deopt 边(P2 零 deopt → P4 加一条)| ✅ §4 全节 | 状态机各转移点的物化协议(04 §3) |
| feedback 各种 Kind 的投机模板 | ✅ §2 全节(快路径 + guard 形式)| — |
| guard 失败发生时的运行期物化序列 | ❌(只提对位,详 04 §3 / §5) | ✅ 04 §3「物化 = memmove」全展开 |
| OSR exit stub 的代码生成 | ❌ | ✅ 04 §6 |
| deopt 计数 + P4StuckSpeculation 状态机 | 提对位 §4.2 / §7.2 | ✅ 04 §5 |
| 重训练协议(`RequestRefresh`)| ✅ §7.3(从 P4 的 IC 重训需求侧)| 04 §5.5 / P2 [05-p3-p4-interface §4.2.2](../p2-bridge/05-p3-p4-interface.md) |
| 与 wazero 边界检查的 prior art 对照 | ✅ §3.2 | — |

简言之:**本文管「guard 长什么样、何时发」,04 管「guard 失败之后怎么完成」**;两文协同覆盖 P4 投机机制的完整闭环。

### 0.4 章节路标

§1 供料链(P1 写 / P2 聚合 / P4 经 P4Feedback 反向读)→ §2 投机模板按五种 FeedbackKind 分档(P3 06 §3 镜像章节)→ §3 guard 硬约束(显式比较,无信号陷阱;承前提四 NaN-box 单 u64 比较)→ §4 状态机加 deopt 边(P2/P3 零 deopt 基线 + F1-F7 继续生效)→ §5 ADD 投机模板对照(P3 wat vs P4 amd64,物理同形 / 语义异化)→ §6 stableShape/stableIndex 直达槽(P2 字段语义 + P4 内联 guard)→ §7 deopt 兜底与重训练(OSR exit + 拉黑投机 + RequestRefresh)→ §8 P4 不依赖 P2 状态机的硬纪律 → §9 不变式清单 → §10 风险与开放问题 → §11 回填请求。

---

## 1. 供料链:P1 写 / P2 聚合 / P4 消费

### 1.1 三棒接力图

P4 的投机不自己采样,**直接消费 P2 的 `TypeFeedback`**——三棒接力在 P1/P2/P3 文档已铺好,本文 §1 只在 P4 视角重述并明确 P4 端的反向读接口与不对称消费原则。承 [../p2-bridge/02-ic-feedback](../p2-bridge/02-ic-feedback.md) §0 三阶段供料链 + P3 06 §1 同源:

```
P1 解释器(crescent,已完成):
  算术 IC(05 §6.4)写 numHits / metaHits 双计数(「写而不读」纯供料)
  表 / 全局 / SELF IC(05 §6.3 / §6.4)写 kind + shape + index + tableRef
   ↓
P2 bridge(已完成 PB0-PB7):
  aggregator.Aggregate(proto) 读 ICSlot(race-tolerant)→
    产 TypeFeedback{ Points[pc] = PointFeedback{kind, confidence, stableShape, stableIndex, ...} }
  CAS 装到 ProfileData.feedback(02-ic-feedback §5.5)
   ↓
P3 (gibbous/wasm):
  P3Compiler.Compile(proto, feedback) — 入参一次性传(05-p3-p4-interface §2.1)
  非投机消费(P3 06 全篇)——失败走助手仍正确,feedback 错只损性能
   ↓
P4 (gibbous/jit,本文):
  P4Feedback.FeedbackFor(proto) 反向读(05-p3-p4-interface §4)
  投机消费——按 Kind + Confidence 决定发投机模板还是通用模板
  guard 失败 ⇒ OSR exit + deopt 计数(本文 §7)
  反复 deopt ⇒ RequestRefresh 触发 P2 重聚合 + 重编译降级投机点(本文 §7.3)
```

注意:P3 与 P4 共享 P2 同一份 `TypeFeedback`,只是接收方式不同——P3 经 `Compile` 入参一次性接收,P4 经 `FeedbackFor` 反向读多次(P4 deopt 后须读到更新后的 feedback,详 §1.4)。**这正是 [../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md) §4.5 接口形式对比表的物理体现**:P3 是顺向一次性,P4 是反向反复。

### 1.2 P1 IC 写入是什么形式

承 [../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md) §6.2/§6.3/§6.4 与 [../p2-bridge/02-ic-feedback](../p2-bridge/02-ic-feedback.md) §1 已完成形式:

| 指令族 | P1 写入字段 | 写入条件 | P4 投机的钩子 |
|---|---|---|---|
| GETTABLE / SETTABLE / SELF | `kind`={1,2,3,4} + `shape`/`index`/`tableRef` | 表查找命中或多次换表降级 mega | `kind∈{1,2,3}` + 同表同代次 ⇒ 直达槽投机(本文 §2.3 / §6) |
| GETGLOBAL / SETGLOBAL | `kind`=2(globals 恒 node hit)+ `shape` + `index` | globals 表查找命中 | 直达 globals 槽投机(本文 §2.4) |
| SELF(`obj:m()`) | 与 GETTABLE 同构 | 方法常驻 metatable → 命中率极高 | 内联方法查找 + metatable 代次 guard(本文 §2.5) |
| ADD / SUB / MUL / DIV / MOD / POW / UNM | 算术 IC 双计数:`shape`(numHits) / `index`(metaHits)挪用 | 快路径双 number 命中 → numHits++;走元方法 → metaHits++ | 比例 ≥0.99 ⇒ f64 投机模板(本文 §2.2) |
| 比较 LT / LE | 算术 IC 同源(双计数挪用)| 双 number 或双 string 直比 → numHits;`__lt`/`__le` → metaHits | 比例 ≥0.99 ⇒ 直比投机(粒度损失见 §10) |

**关键性质**(承 [../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md) §6.4「P1 写不读纯供料」原话):算术 IC 是**纯旁路写**,P1 解释器靠 `IsNumber` 现场判定走快慢路径,不读 IC slot——这意味着 IC slot 写不写、写成什么样都不影响 P1 的取值正确性。这条铁律给 P4 的红利是:P4 投机可以激进地依赖 IC 反馈,**P1 的正确性兜底独立于 IC 反馈本身**——投机失败 OSR exit 回 P1,P1 的语义路径与 IC 反馈解耦。

### 1.3 P2 聚合产物 TypeFeedback

承 [../p2-bridge/02-ic-feedback](../p2-bridge/02-ic-feedback.md) §4 PointFeedback 完整定义,本节只列 P4 投机消费要用到的字段(完整 Go 结构定义见上游 §4.2;**字段名以 P2 02 §4.2 实际定义为准——包内私有 `stableShape` / `stableIndex` 等小写,P4 端经 internal package 共享访问**):

```go
// 字段大小写以 ../p2-bridge/02-ic-feedback §4.2 实际定义为准(包内私有)。
type PointFeedback struct {
    pc           int32         // 对应字节码 pc
    kind         FeedbackKind  // FBUnstable / FBArithStableNumber / FBTableMono / FBTableMega / FBGlobalStable / FBSelfMono
    confidence   float32       // [0, 1]:Kind 可信度,P4 用作投机激进度旋钮(§2.7)
    stableShape  uint32        // 表 IC:gen 代次 / 算术 IC:零
    stableIndex  uint32        // 表 IC:命中槽位下标 / 算术 IC:零
    observations uint32        // 该点累积观测次数(诊断用)
}
```

**P4 消费的核心字段**:**kind**(决定走哪条投机模板,§2 总表)+ **confidence**(决定是否投机的旋钮,§2.7 阈值消费)+ **stableShape / stableIndex**(作为投机模板的编译期立即数,§6 直达槽)。**P4 不消费 observations**(诊断用,confidence 已是更精细的同源信号);**pc** 由 Points 索引隐含。

`stableShape` / `stableIndex` 是 P4 与 P3 共用的字段,只是 P4 把它包成 guard 比对值,P3 把它包成 if-then 直达槽分发(P3 06 §3.2.1)。

### 1.4 P4 经 P4Feedback.FeedbackFor 反向读

承 [../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md) §4 接口签名,P4 通过 `P4Feedback.FeedbackFor(proto)` 反向读 feedback。**这与 P3 经 `Compile` 入参一次性接收不同**——P4 需要在两个时刻读 feedback:

| 时刻 | 读 feedback 的目的 |
|---|---|
| (1) **首次 Compile** | 决定每个 IC 点投机激进度(等价 P3 的 `Compile(proto, fb)` 入参) |
| (2) **deopt 后重训练完成,再次 Compile** | 读到的是更新后的 feedback(可能比上次更准),把失效的投机点降级为通用模板(§7.3) |

为什么 P4 需要反向读而 P3 不需要:**P3 不依赖 feedback 正确性(P3 06 §1.5),feedback 错只损性能;P4 依赖 feedback 正确性,feedback 错会触发 deopt 风暴,需要在重训练后重读**。这条不对称在 [../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md) §4.5 接口形式对比表已立。

接口的关键性质(承 [../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md) §4.2.1):

- **返回的 `*TypeFeedback` 是只读快照**——P2 端的写入用 CAS 装新指针,P4 拿到的旧指针仍指原来的不可变快照,**无锁同步**。
- **`RequestRefresh`** 是 P4 → P2 的反向通道——deopt 风暴时 P4 请求 P2「这份 feedback 已陈旧,请重聚合」(详 §7.3 + [../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md) §4.2.2)。

### 1.5 「不对称消费」原则:P3 弱依赖 / P4 强依赖

承 [../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md) §1.5 总纲:**P2 产出一份对称的 feedback,P3/P4 不对称消费**——P2 不为 P3/P4 各产一份(浪费),也不在 P3 编译时省略 feedback(让 P3 有机会做紧凑翻译)。

落到 P4 视角:Kind 决定走哪条投机模板(§2);Confidence 强消费(≥0.99 才投机,§2.7,而 P3 弱消费可只看 Kind 不读 confidence,P3 06 §3.1);stableShape/stableIndex 用作 guard 比对值与直达槽 offset 立即数(§6,而 P3 用作 if-then 直达槽分发的立即数,P3 06 §3.2.1);feedback nil 容忍(退化为「无投机模板」,失去倍率,但仍省 dispatch 税,05-p3-p4-interface §1.1);feedback 错的代价 = 性能(deopt 开销)+ 反复时拉黑投机(本文 §7.2,而 P3 仅性能损失)。

**这就是「不对称消费」原则在 P4 视角下的兑现**:P4 强依赖 feedback 正确性,但 guard + deopt 兜底确保正确性不损——投机失败不出错,只是触发 OSR exit 回解释器(05-p3-p4-interface §1.3)。

---

## 2. 按 feedback 种类的投机模板

### 2.1 五种 FeedbackKind 的投机动作总表

本节展开 FeedbackKind → 投机模板的映射表(加 confidence 阈值列 + 失败着陆面列 + 与 P3 镜像章节列):

| feedback Kind | 投机动作 | guard 检查什么 | 失败着陆面 | confidence 阈值 | P3 镜像章节(对偶) |
|---|---|---|---|---|---|
| `FBArithStableNumber` | f64 直接运算指令(`mulsd`/`fmul`/`addsd`)+ NaN 规范化 | 两操作数 `IsNumber`(NaN-box 单 u64 比较)| OSR exit | ≥0.99 | P3 06 §3.1(P3 此处发完整 if-else,本文裁掉 else)|
| `FBTableMono` | 代次比对 + 直达槽 load/store(把 IC 命中路径内联成机器码)| 同表 + 同代次(NaN-box tag + tableRef + gen)| OSR exit | =1.0(P1 mono IC 无降级历史)| P3 06 §3.2.1(P3 在 if-then 直达,本文在 guard 通过后直达)|
| `FBGlobalStable` | 常量化 / 直达 globals 槽 | globals 代次(身份恒等省 tableRef 校验)| OSR exit | =1.0 | P3 06 §3.2.2 |
| `FBSelfMono` | 内联方法查找结果(self 传递 + 直达槽)| metatable 代次(同 FBTableMono 三层)| OSR exit | =1.0 | P3 06 §3.2.3 |
| `FBUnstable` / `FBTableMega` | **不投机**:发通用模板(等价解释器一样的语义路径) | 无 guard(语义完备)| 无(直接走通用)| n/a(本就不投机)| P3 06 §3.3 / §3.4 |
| `nil`(feedback 缺失) | 同 FBUnstable(全用通用模板)| 无 guard | 无 | n/a | P3 06 §3.5(一样的 nil 容忍)|

**观察**:**五种 Kind 的投机动作两分**:可投机的(前四种,有 guard + 失败 OSR exit)与不可投机的(后两种,无 guard 直接通用模板)。这是 P4 的「投机叠在可编译子集之内」(原则 4)在 feedback 维度的具体体现——P4 不是「凡可编译就投机」,是「可编译且 confidence 达标才投机」。

下面 §2.2-§2.6 各档详细给:快路径形式 + guard 形式 + 失败语义边界 + 与 P3 模板的差分。

### 2.2 FBArithStableNumber:f64 快路径 + IsNumber×2 guard + NaN 规范化

**触发条件**(承 [../p2-bridge/02-ic-feedback](../p2-bridge/02-ic-feedback.md) §3 / §5.2):算术 IC 双计数比例 `numHits / (numHits+metaHits) ≥ 0.99` 且 `numHits+metaHits ≥ 100`(样本量阈值)。

**P4 投机模板形式**(amd64 风格伪汇编,详细对照见 §5):

```asm
;; emit_add(pc, A, B, C, fb=FBArithStableNumber, conf>=0.99):
;; 注:本文伪汇编中 r15/r14 等寄存器仅为示例占位,具体寄存器约定以 [05 §3.3 / 06 §4.1] 为单一事实源
;; 假设值栈 base 寄存器已是 r15(实际形式见 05 §3.3 jitContext + §4.1 寄存器约定)
  mov rax, [r15 + 8*B]                  ; 加载 vb(NaN-box u64)
  mov rcx, [r15 + 8*C]                  ; 加载 vc

  ;; ★ guard:IsNumber×2(NaN-box 单 u64 无符号比较,详 §3.4)★
  cmp rax, NAN_THRESHOLD                ; vb < threshold ⇒ 是 number
  jae .deopt_pc                         ; 不是 number → OSR exit
  cmp rcx, NAN_THRESHOLD
  jae .deopt_pc                         ; 不是 number → OSR exit

  ;; 双 number 快路径:f64.add
  movq xmm0, rax
  movq xmm1, rcx
  addsd xmm0, xmm1
  ucomisd xmm0, xmm0                    ; NaN check
  jp .canon_nan                         ; canonicalize:01 §3.4
.write_back:
  movq [r15 + 8*A], xmm0                ; 写回栈槽(NaN-box 编码不变,01 §7 不变式 1)
  jmp .next_pc

.canon_nan:
  ;; canonicalizeNaN:把 f64 NaN 全部规范化为 0x7FF8000000000000
  mov rax, 0x7FF8000000000000
  movq xmm0, rax
  jmp .write_back

.deopt_pc:
  call $osr_exit_pc_<n>                 ; OSR exit:详 04-osr-deopt §3 / §6
```

**guard 失败语义**:guard 失败**不是错误**,是「投机前提不成立」——OSR exit 把该帧剩余执行整体交还 crescent,crescent 走 [../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md) §4.1/§4.2 完整 doArith(coercion + `__add` 元方法 + 错误抛出),**正确返回**。这与 P3 06 §3.1 的「else 分支调 `$h_arith` 走 metamethod」语义等价但**机制不同**:P3 在 Wasm 函数内同步调 helper 后继续直线 Wasm,P4 离开 JIT 函数让解释器接管。

**与 P3 ADD 翻译的根本区别**(承 P3 06 §1.2 三档具体形式):

| 维度 | P3 ADD(P3 06 §3.1)| P4 ADD(本节)|
|---|---|---|
| else 分支 | **永远存在且必发**(`$h_arith` 调用)| **不存在**——失败直接 OSR exit(else 被裁掉)|
| 慢路径在哪 | 同 Wasm 函数(if-else 分支)| 解释器(出 JIT 函数边界)|
| 是否离开编译码 | 否 | 是 |
| feedback 错的代价 | 100% 走 helper(慢但正确)| OSR 风暴 → 拉黑投机(§7.2)|

**关键不变式**:**P4 投机模板的 else 分支被裁掉,这正是「投机」的字面定义——「省略某些合法语义分支,赌它不发生」**(P3 06 §1.1 末尾对投机的定义)。P3 不省略所以不是投机;P4 省略所以是投机,需要 guard + deopt 兜底。

### 2.3 FBTableMono:代次比对 + 直达槽 + 同表+同代次 guard

**触发条件**(承 [../p2-bridge/02-ic-feedback](../p2-bridge/02-ic-feedback.md) §2.1):GETTABLE/SETTABLE 上 ICSlot.kind ∈ {1 array hit, 2 node hit, 3 mono-metamethod} 且 Refill 计数 < 阈值(未升级到 mega)。

**P4 投机模板形式**(amd64 风格伪汇编,详 §6):

```asm
;; emit_gettable(pc, A, B, C, fb=FBTableMono):
;; 编译期立即数(从 PointFeedback.stableShape/stableIndex + ICSlot 双源选取,详 §6 与 P3 06 §4.4):
;;   SNAP_TABLEREF = uint64(slot.tableRef)
;;   SNAP_GEN      = uint32(slot.shape)
;;   SNAP_KIND     = uint8(slot.kind)
;;   SNAP_INDEX    = uint32(slot.index)

  mov rax, [r15 + 8*B]                  ; 加载 t(NaN-box u64)

  ;; ★ guard 第 1 层:类型 = table(NaN-box tag 比对)★
  mov rdx, rax
  shr rdx, 47                           ; 取高位 tag bits(01 §3.2)
  cmp dl, TABLE_TAG
  jne .deopt_pc

  ;; ★ guard 第 2 层:同表(arena 偏移低 32 位身份)★
  mov edx, eax                          ; 截 tableRef 低 32 位
  cmp edx, SNAP_TABLEREF
  jne .deopt_pc

  ;; ★ guard 第 3 层:同代次(t.gen() == SNAP_GEN)★
  mov rdx, rax
  and rdx, 0x0000FFFFFFFFFFFF           ; 提取 GCRef 偏移
  cmp dword [rdx + GEN_OFF], SNAP_GEN
  jne .deopt_pc

  ;; ★ 三层 guard 通过 ⇒ 直达槽 ★
  ;; SNAP_KIND 编译期已知,switch 静态展开为单分支:
  ;; 当 SNAP_KIND=1: array_at;当 SNAP_KIND=2: node_val_at;当 SNAP_KIND=3: __index 直调
  mov rcx, [rdx + ARRAY_OFF + 8*SNAP_INDEX]   ; 当 SNAP_KIND=1
  mov [r15 + 8*A], rcx
  jmp .next_pc

.deopt_pc:
  call $osr_exit_pc_<n>                 ; 任一 guard 失败 → OSR exit
```

**与 P3 GETTABLE 翻译的差分**(承 P3 06 §3.2.1 + §1.3):

| 维度 | P3 GETTABLE(P3 06 §3.2.1)| P4 GETTABLE(本节)|
|---|---|---|
| 三层校验位置 | if-then-else 的判定层 | guard:三层失败都跳同一个 `.deopt_pc` |
| 失败动作 | call `$h_gettable`(同函数内走完整查找,P1 §6.3)| OSR exit |
| 校验顺序 | IsTable → tableRef → gen(P3 06 §3.2.1)| 完全相同(本节)|
| 字段来源 | SNAP_* 来自编译期 ICSlot 直接读(P3 06 §4.4)| 相同——P4 也从 ICSlot 直接读 SNAP_*(详 §6)|

**关键观察**:**三层校验的物理形式、字段来源、判定顺序完全相同,只有「失败着陆面」不同**——P3 落同函数 helper,P4 落 OSR exit。这是 P3 06 §1.3 「同表同代次校验是语义分发,不是投机 guard」的对偶兑现:同样三层比较,P3 是「快路径前置检查」(失败也合法),P4 是「投机 guard」(失败 ⇒ 出函数)。

**2026-07-02 实现勘误(承 [implementation-progress §14.5](./implementation-progress.md) PR #34 amd64 native 扩接)**:上文「IsTable → tableRef → gen 三层校验顺序与 P3 完全相同」的设计描述在 P4 amd64 native path 的 GETTABLE / SETTABLE ArrayHit inline 上**不再字面成立**——本轮工程 amd64 native ArrayHit inline 路径**完全去掉了 TableRef + gen identity guards**,只留一层 IsTable 位 tag 检查。理由:

1. **物理无需**:ArrayHit inline 直接从当下 table 的 `asize` / `arrayRef` 字段读取——对**任意** table 都正确。非 nil 的 array 槽读永远不会触发 `__index` 元方法链;非 nil-over-non-nil 的 array 槽 store 永远不会触发 rehash / 键退化。这两个属性是 array 段本身的物理不变量,与 「同表 + 同代次」这个身份约束无关。
2. **IC snapshot 只作 emit-time gate**:P4 用 IC feedback 只是决定「哪个 pc 位点允许 emit ArrayHit inline 序列」,不是运行期身份匹配。运行期读的是活表的字段。
3. **副作用**:因为 identity guards 撤掉了,「多次 dispatch 同一 pc 会看到不同 table」这条曾经的失败面在 P4 amd64 上就自然不 deopt 了——这不是漏判,是这条 op 序列对不同 table 都是正确的。

对应地,GETGLOBAL / SETGLOBAL NodeHit inline(§2.4)也做了类似简化:只留 gen-only guard + 编译期烧入的 node index。这**强化了对生产端的要求**:任何 key → slot 的重定位都必须 `BumpGen`,否则 inline 就会读到错的 slot。这个 producer-side 不变量已在 crescent `rawtable.go::insertNewKey` 上补齐(2026-07-02,fuzz seed 4b3d10ff 回归)。

本 addendum 记录 addendum 时点的物理事实,不删除上文原设计文本——上文对 P2/P3 端的对偶描述,以及 P4 spec template(§9.19 SELF NodeHit 六模板)仍字面成立;只有 amd64 native path 的 ArrayHit / NodeHit inline 走了简化路线。arm64 端因 native 接受门尚未接 GETTABLE/SETTABLE(承 §14.5 分岔),此 addendum 对 arm64 无影响。

### 2.4 FBGlobalStable:常量化 / 直达 globals 槽 + globals 代次 guard

**触发条件**(承 [../p2-bridge/02-ic-feedback](../p2-bridge/02-ic-feedback.md) §2.2):GETGLOBAL/SETGLOBAL 上 ICSlot.kind = 2 node hit。

**P4 投机模板形式**:

```asm
;; emit_getglobal(pc, A, Bx, fb=FBGlobalStable):
;; 编译期立即数:
;;   SNAP_GEN_GLOBALS = slot.shape
;;   SNAP_INDEX       = slot.index
;; 注:GETGLOBAL 不需要 SNAP_TABLEREF(目标恒为 globals,身份恒等)
;;     也不需要 SNAP_KIND(globals 恒为 node hit)

  ;; 取 globals 表(从 closure 的 _ENV upvalue,常量化)
  mov rdx, [r14 + ENV_GLOBALS_OFF]      ; r14 = closure base,编译期可知

  ;; ★ guard:globals 同代次 ★
  cmp dword [rdx + GEN_OFF], SNAP_GEN_GLOBALS
  jne .deopt_pc

  ;; 命中 ⇒ 直达 globals node 槽
  mov rcx, [rdx + NODE_OFF + 16*SNAP_INDEX]   ; node 是 (key,val) 对,16 字节
  mov [r15 + 8*A], rcx
  jmp .next_pc

.deopt_pc:
  call $osr_exit_pc_<n>
```

**简化处**:相对 §2.3 GETTABLE,GETGLOBAL 省了 IsTable + tableRef 两层 guard——globals 身份恒等,无需运行期校验(承 P3 06 §3.2.2)。这是 globals IC 的物理优势(命中代码更短、单 guard 更友好),P3 与 P4 同享此红利。

**进一步优化方向**(留 P4 实测后定):若 globals 表整生命周期都未发生 rehash(SNAP_GEN_GLOBALS 永不变),理论上 P4 可在编译期把整个值常量化(skip globals 槽 load,直接发 `mov [r15 + 8*A], CONST_VALUE`)——但这需要值本身不变(string 还可,table/closure 等引用类型不行)。当前定稿保守:P4 始终发 globals 槽 load,不做值常量化。

### 2.5 FBSelfMono:内联方法查找 + metatable 代次 guard

**触发条件**(承 [../p2-bridge/02-ic-feedback](../p2-bridge/02-ic-feedback.md) §2.3):SELF 上 ICSlot.kind ∈ {1, 2, 3}。

**P4 投机模板形式**:与 §2.3 GETTABLE FBTableMono 三层 guard 同构——前置加 self 传递 `R(A+1) := R(B)`,后续 R(A) := R(B)[RK(C)] 走与 §2.3 完全相同的三层 guard + 直达方法槽。承 P3 06 §3.2.3 同结构,改 helper 为 OSR exit。

**SELF IC 命中率极高**(方法常驻 metatable,实际负载里几乎不变),所以 FBSelfMono 是 **P4 内联方法调用的核心入口**——`obj:m()` 形式的 SELF + CALL 序列,经此投机内联消除「方法查找 + 调用」的两次 dispatch 税。这是「内联方法查找结果」的具体形式,与「内联函数体」(那是 P5 trace 内联)严格区分:**P4 只内联「方法地址」,不内联「方法函数体」**。

### 2.6 FBUnstable / FBTableMega:不投机,发通用模板

**触发条件**(承 [../p2-bridge/02-ic-feedback](../p2-bridge/02-ic-feedback.md) §3.3 / §3.4):

- `FBUnstable`:① IC 未观测(slot.kind=0);② 算术比例不达标(<0.99);③ 算术样本量不足(<100);
- `FBTableMega`:Refill 计数 ≥ 阈值(P2+ #4 主动识别已完成,默认 3 次重填即标 mega)。

**P4 翻译形式**:**等价解释器一样的语义路径**——发通用模板,无 guard,无 OSR exit;直接调通用 helper(等价解释器无 IC 形式,helper 内部走完整 doGetTable + IC)。

**与 P3 处理一致**(承 P3 06 §3.3 / §3.4):P3 与 P4 对 mega/unstable 的处理完全一样的——不内联快路径,直接发 helper。这是「不投机」原则在两层共同的兑现:**多态点投机收益为负**(命中率低 < Refill 阈值,内联 guard 浪费 icache + 频繁 deopt)。物理形式上 P3 走 imported 调用(跨 Wasm/Go 边界),P4 走 jitContext 间接 + trampoline 出 JIT 世界,**收益放弃相同(都放弃 IC 加速),失败兜底都不需要(本就是慢路径)**。

### 2.7 confidence 阈值消费(≥0.99 才投机)

承 [../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md) §1.4 / §5.2:

| 阈值 | 物理含义 | 选用 |
|---|---|---|
| `confidence >= 0.99` | 极保守:只对最稳定点投机,失败率极低,投机面窄 | **P4 初版基线** |
| `confidence >= 0.95` | 中等:覆盖更多点,但 5% 失败率 ⇒ 偶尔 deopt | P4 实测调优 |
| `confidence >= 0.90` | 激进:覆盖大部分点,10% 失败率 ⇒ deopt 频率高 | 不推荐(deopt 风暴风险)|

**P2 端阈值与 P4 端阈值的关系**:P2 端阈值(`>=0.99` 才标 `FBArithStableNumber`,详 [../p2-bridge/02-ic-feedback](../p2-bridge/02-ic-feedback.md) §5.2)是「P2 视角下何为稳定」;P4 端阈值是「P4 视角下何为值得投机」。两者是串联的——P2 已经在 0.99 处把 unstable 滤掉了,P4 拿到的所有 `FBArithStableNumber` 都已经 ≥0.99,**P4 自己再设阈值意义不大**(除非 P4 想再卡严)。

**P4 初版定稿**:**直接信 P2 的 Kind,不再独立读 confidence**——这与 P3 06 §3.1 的「P3 不读 confidence」一致,但理由相反:P3 是因为不投机所以不需要,P4 是因为 P2 已用阈值滤过所以再读冗余。接口仍允许 P4 读 confidence 留扩展空间(05-p3-p4-interface §1.4)。

**对 FBTableMono / FBGlobalStable / FBSelfMono**:Confidence = 1.0 是 P2 给的(P1 mono IC 无降级历史,02-ic-feedback §2.1 表的「单态命中即稳定」)——P4 这三档不需要 confidence 阈值,直接看 Kind 即可。

---

## 3. guard 的硬约束:显式比较,无信号陷阱

### 3.1 LuaJIT/V8 信号陷阱 guard 在 Go 下不可用

LuaJIT / V8 等 C++ 系 JIT 大量使用**硬件陷阱**实现零成本 guard——典型形式是让非法访问触发 `SIGSEGV`,信号处理器接管恢复:

```
LuaJIT 风格(C 下可用):
  ; 假设 t 是 number,直接走 f64 算术,不发显式 guard
  movq xmm0, rax                  ; rax 假设是 number
  addsd xmm0, xmm1
  ; 若 rax 实际是非 number(NaN-box tag 不是 number),
  ; movq/addsd 后的某处取地址访问会落到非法页 → SIGSEGV
  ; 信号处理器接管 → 找出对应 PC → snapshot 重建 → 回解释器
```

**纯 Go 下此路不通**——Go runtime 拥有信号处理(SIGSEGV/SIGBUS/SIGFPE),落在非 Go PC 上的 fault 无法恢复,直接 fatal。这是 [../roadmap](../roadmap.md) §2 四项税同族的「runtime 所有权」约束:Go runtime 把信号处理握死,**第三方代码无法插入「我自己处理这个 fault」的钩子**——任何在 JIT 代码里依赖陷阱的 guard 形式都直接判否。

### 3.2 wazero 也走显式边界检查的同源约束

wazero 是 Apache 2.0 纯 Go Wasm 引擎,P3 把四项税全套外包给它([../p3-wasm-tier/00-overview §8](../p3-wasm-tier/00-overview.md))。wazero 的边界检查实现也是**显式比较**而非信号陷阱——这与 P4 同源,理由也同源(纯 Go runtime 所有权约束)。

| 引擎 | guard / 边界检查 形式 | 理由 |
|---|---|---|
| LuaJIT(C++) | 信号陷阱 | C 下 runtime 可改信号处理 |
| V8(C++) | 信号陷阱 + 显式 guard 混用 | 同上 |
| wazero(纯 Go) | 全显式边界检查 | Go runtime 所有权 |
| **P4(纯 Go)** | **全显式 guard** | **同上** |

**这是「纯 Go 逼近而非追平 LuaJIT」的微观注脚之一**——量化上已被前提一的 6% 校准吸收(LuaJIT 仅比 luajc 快 6%,[../roadmap](../roadmap.md) §1)。luajc 也是「纯 JVM 字节码 + 显式检查」,不依赖陷阱 guard,所以 P4 「显式 guard」的成本对标 luajc 而非真 LuaJIT,**6% 那一档的差距正是这条约束的一部分**。

### 3.3 P4 定稿:所有 guard = 显式「比较 + 条件跳」

P4 总览的字面承诺:

> P4(及 P5)所有 guard 都是显式「比较 + 条件跳」,每 guard 2-3 条指令的恒定成本。

**物理形式约束**:每个 guard 都形如 `cmp + jcc`(amd64)或 `cmp + b.cond`(arm64),2-3 条机器指令的恒定成本。这与 §2.2 / §2.3 等节给出的 amd64 伪汇编一致——**所有 `.deopt_pc` 跳转都是显式 jcc,没有 movq/load 暗藏陷阱**。

**guard 成本结构**:

| guard 类型 | 物理形式 | 指令数 | 成本量级 |
|---|---|---|---|
| IsNumber(NaN-box 单 u64 比较)| `cmp rax, NAN_THRESHOLD; jae .deopt` | 2 | 1 cycle 量级(分支预测命中)|
| 同表(tableRef)| `cmp eax, SNAP_TABLEREF; jne .deopt` | 2 | 1 cycle 量级 |
| 同代次(gen)| `cmp [rdx + GEN_OFF], SNAP_GEN; jne .deopt` | 2(load+cmp 合并)| 2 cycle 量级(含 load)|
| 类型 = table(tag bits)| `mov tmp, rax; shr tmp, 47; cmp ...; jne` | 3-4 | 2 cycle 量级 |

**guard 总成本天花板**(承本文 §10 风险):全显式 guard 的成本在密集投机点上压低了生成码密度的天花板——**LuaJIT 同等 guard 免费,P4 须付 2-3 cycle**。这条天花板在 §10 风险节展开。

### 3.4 NaN-box guard 的物理形式:单次 u64 无符号比较

承 [../p1-interpreter/01-value-object-model](../p1-interpreter/01-value-object-model.md) §3.2 + [../../../llmdoc/must/design-premises](../../../llmdoc/must/design-premises.md) 前提四第一天承诺:

> **NaN-boxed u64 + 自管 arena**(线性内存),数字识别 = 单次 u64 无符号比较与 NAN_THRESHOLD。

P4 的 IsNumber guard 直接用此识别:

```
NAN_THRESHOLD = 0xFFF8_0000_0000_0000(NaN-box 编码下,所有 number 都 < 此阈值)

cmp rax, NAN_THRESHOLD
jae .deopt_pc                     ; 不是 number(>= 阈值)→ OSR exit
```

**这是前提四「第一天值表示承诺」在 P4 兑付的现金**:

- ① **GC 不卷入**:NaN-box u64 是值类型,guard 比较读寄存器即可,不涉及 Go GC 写屏障([../roadmap](../roadmap.md) §2 四项税之四「写屏障」白赚);
- ② **无装箱解箱**:与 Go interface 装箱相比,NaN-box 数字直接是 8 字节 u64,guard 与 f64 算术读同一份,无格式转换;
- ③ **跨 tier 拷贝是 memmove**:guard 失败 OSR exit 时,栈槽里就是 NaN-box u64,crescent 接管直接读相同编码,无重建([./04-osr-deopt.md](./04-osr-deopt.md) §3 「物化 = memmove」全展开,本文不重写)。

**反例反证(若第一天选了 Go tagged struct,住 Go 堆)**:IsNumber guard 退化为「读 tag 字段 + cmp(至少 3 cycle)+ 保证 GC 不搬走 v」,guard 失败 OSR exit 退化为「机器表示 → Go 对象」重建,deopt 机器复杂度上一个量级。P4 的 deopt 之所以能薄到几乎没有([./04-osr-deopt.md](./04-osr-deopt.md) §3.3「栈槽真相」不变式),**靠的就是这条值表示承诺**——前提四不是空话,是 P4 deopt 简单性的物理基础。

### 3.5 guard 「多判 vs 漏判」语义边界

P4 投机的多判 vs 漏判原则:

> guard 只验证「投机前提」,验证失败**不是错误**,是回到全语义路径——guard **多判**(过于保守)只损性能,**漏判**(该查没查)直接产出错误结果且不崩溃,是 JIT 第一危险源。

| guard 行为 | 后果 | 防线 |
|---|---|---|
| **多判**(查得太严)| 投机命中率下降,频繁 deopt,性能塌陷 | 可观测(deopt 计数,§7),实测可调阈值缓解 |
| **漏判**(查得不够)| **静默错果**——产出错误结果不崩溃、不报错 | **差分主防线见 [./08-testing-strategy.md](./08-testing-strategy.md)**|

**这是 [../../../llmdoc/must/design-premises](../../../llmdoc/must/design-premises.md) 前提三原则 2「投机错误静默错果是 JIT 最危险 bug 类别」的字面对齐**:guard 漏判 = 投机错误。差分主防线在 [./08-testing-strategy.md](./08-testing-strategy.md) 全面展开,本文 §3.5 只点名,不重展开口径。

**对 P4 实现的具体纪律**:

- guard 必须发在快路径**之前**(操作数检查先于任何副作用)——这条与 [./04-osr-deopt.md](./04-osr-deopt.md) §3.3「栈槽真相」不变式协同:guard 失败时栈槽里全是合法值,OSR exit 物化集合为空。
- guard 不可省略(即便 confidence=1.0)——P2 的 confidence 是统计性的,不是数学上 100%,任何 guard 漏判都是 bug,review 与差分双双兜底。
- guard 失败的着陆点必须落到 OSR exit,不可就地补救为通用模板继续跑(承 [./04-osr-deopt.md](./04-osr-deopt.md) §0.1)——补救路径会破坏「不优化跨指令」的简单性,把 P4 从模板编译升级到 stitching 编译。

### 3.6 guard 合并(同操作数直线段内只查一次)

guard 密度天花板的缓解策略:

> 缓解:guard 合并——同一操作数在直线段内只查一次,这是不引入 IR 前提下可做的窥孔级优化。

**形式**:假设 R(B) 在 pc=10..15 直线段内被多次用作 ADD/SUB/MUL 操作数,且未被任何写指令覆盖——第一条 ADD 前发 IsNumber(R(B)) guard,后续 ADD/SUB 直接走 f64 不重发 R(B) guard(R(C)/R(D) 等仍按需 guard)。

**关键约束**(此优化可做但仍非 IR 优化):

- ① **直线段内**:遇到任何 BB 边界(JMP/分支/CALL/CLOSURE 等)guard 必须重发——直线段外 R(B) 可能被写覆盖;
- ② **未被写覆盖**:即便在直线段内,R(B) 若被任何 MOVE / 算术结果 store 等写过,guard 必须重发;
- ③ **不引入 IR**:窥孔级优化(发射器线性扫描时维护「最近 guard 过的寄存器集合」),不需要 SSA / regalloc / 数据流分析。

**它仍非 IR 优化的硬纪律**:guard 合并不能演变成「跨 BB 数据流分析的冗余 guard 消除」(后者要 SSA + use-def 链,直接进入 P5 IR 优化器领地)。**P4 实现时必须把这条优化锁在「直线段窥孔」范围内,不可借势扩到跨 BB**——这是「P4 简单性」对 guard 合并的边界约束,留 §10 风险节追踪。

---

## 4. P4 与 P2/P3 零 deopt 的分野:状态机加一条边

### 4.1 P2/P3 零 deopt 状态机回顾

承 [../p2-bridge/04-try-compile-fallback](../p2-bridge/04-try-compile-fallback.md) §2 + §1.3「P2/P3 静态保证 vs P4 投机」:

```
P2/P3 状态机(零 deopt,单向无环):
  TierInterp ──► TierGibbous(吸收态:升层成功后永驻)
  TierInterp ──► TierStuck   (吸收态:F1-F7 不可编译或编译失败)

  没有任何「Gibbous → Interp」边——升层一旦成功,gibbous 代码对所有合法输入正确,
  「假设被打破」的可能不存在,所以根本不需要 deopt。
```

**P2/P3 零 deopt 的字面理由**(承 [../p2-bridge/04-try-compile-fallback](../p2-bridge/04-try-compile-fallback.md) §1):

> P2/P3 走 try-compile,**编译的子集对任何运行期输入都正确**。编译出的 gibbous 代码不依赖任何运行期类型假设,**所以根本不需要 deopt**——没有「假设被打破」的可能。

P3 06 §0.2 已把这条具体兑现到代码层:P3 的所有「IsNumber×2 / 同表同代次 / globals gen 校验」都是**语义分发**——失败也是合法语义路径,落到 helper 得正确结果。

### 4.2 P4 状态机:P2 三态不变 + P4 内部 p4SpecState 子状态机叠加(方案 A)

**方案 A 决议**(承 [./04-osr-deopt.md](./04-osr-deopt.md) §5 + 本文 §8 硬纪律):**P2 `tierState` 枚举不变,仍是 `TierInterp / TierGibbous / TierStuck` 三态(单向无环)**;**P4 在 `internal/gibbous/jit` 内部维护独立子状态字段 `p4SpecState[proto]`**——枚举 `P4Speculative / P4Deoptimized / P4StuckSpeculation`——**叠加**在 P2 `TierGibbous` 之上,**P2 不感知**(承 §8.1 / §8.2)。

```
                P2 状态机(不变,承 ../p2-bridge/04-try-compile-fallback §2):
                  TierInterp ──► TierGibbous(吸收态)
                  TierInterp ──► TierStuck   (吸收态)

                  P4 升层成功后,P2 看仍是 TierGibbous(从 P2 视角永驻)。

  ┌─────────────────────────────────────────────────────────────────┐
  │  P4 内部 p4SpecState 子状态机(叠加在 TierGibbous 内):           │
  │                                                                 │
  │   (P4 编译成功)──►  P4Speculative                              │
  │                          │                                      │
  │                          │ (a) guard 失败 → OSR exit(本文 §7.1)│
  │                          ▼                                      │
  │                       P4Deoptimized                             │
  │                  (P4 内部「降层」语义:                          │
  │                   该帧由 crescent 续跑剩余字节码 + 计数 +1)     │
  │                          │                                      │
  │            ┌─────────────┴─────────────┐                        │
  │            │ deopt 计数 < 阈值          │ deopt 计数 ≥ 阈值       │
  │            ▼                           ▼                        │
  │  (b) RequestRefresh 重训练 +    (c) P4StuckSpeculation          │
  │      重编译失效投机点 ────►          (P4 内吸收态:不再投机,   │
  │      回 P4Speculative                  仍发通用模板;P2 看仍   │
  │                                        是 TierGibbous)         │
  └─────────────────────────────────────────────────────────────────┘
```

**新增的子状态与转移**(均在 P4 端,P2 实现零修改):

| # | 子状态 / 转移 | 触发条件 | 动作 |
|---|---|---|---|
| (a) | `P4Speculative → P4Deoptimized` | guard 失败 | 该帧剩余执行交还 crescent;P4 端 `deoptCount[proto]` +1(本文 §7.1)|
| (b) | `P4Deoptimized → P4Speculative`(重训练后重编译)| 解释执行期间 IC 重新积累,P4 调 RequestRefresh 后再编 | 失效的投机点降级为通用模板;P4 端覆写 GibbousCode(仍住 P2 `Bridge.gibbousIndex`,P2 视角不动)|
| (c) | `P4Deoptimized → P4StuckSpeculation`(吸收态)| deopt 计数 ≥ 阈值,反复 deopt 风暴 | 该 Proto 永久只发无投机的通用模板(仍比解释快——dispatch 税照省);**P2 仍看 TierGibbous,不重试投机**(同 [../p2-bridge/04-try-compile-fallback](../p2-bridge/04-try-compile-fallback.md) §7 不重试纪律的对偶)|

**关键观察**:`P4StuckSpeculation` 与 P2 的 `TierStuck` 是**两个完全不同的状态机里的两个吸收态**——前者是「P4 投机失败拉黑」(P4 内部状态字段,挂 `p4SpecState[proto]`),后者是「P2 编译失败拉黑」(P2 `tierState` 枚举值)。两者都是「不重试纪律」家族,但**归属不同 + 影响范围不同**(详 §8 P4 不依赖 P2 状态机)。**P2 三态枚举永远不被 P4 写**——这是方案 A 的核心承诺,使「P2 PB6 mock → 真 P3/P4 三阶段切换零修改 P2 实现」(承 [../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md) §0.3)成立。

### 4.3 与 try-compile fallback 的对照表

承 [../p2-bridge/04-try-compile-fallback](../p2-bridge/04-try-compile-fallback.md) §1 「fallback ≠ deopt」表,本节再延一列(P5 trace JIT 的预告):

| | try-compile(P2/P3)| **投机 JIT(P4)** | trace JIT(P5)|
|---|---|---|---|
| 运行期假设 | 无 | 有(类型稳定)| 有且更重(路径稳定)|
| deopt 机器 | 不需要 | **函数级 OSR exit(详 04-osr-deopt §3)** | 精细 snapshot(详 P5 §4)|
| 假设错的代价 | 不存在 | 慢(exit + 再训练),不错 | 慢,不错 |
| 状态机 | P2 三态单向无环 | **P2 三态不变 + P4 内部 p4SpecState 子状态机叠加**(本文 §4.2) | 同 P4 + trace 黑名单 |
| 吸收态 | TierStuck(P2 管,F1-F7 失败)| **P4StuckSpeculation**(P4 内部,投机失败超阈值)| 同 P4 + 黑名单更细粒度(per-trace)|
| 重编译 | 不重试([../p2-bridge/04-try-compile-fallback](../p2-bridge/04-try-compile-fallback.md) §7)| 经 RequestRefresh 重训练后允许(本文 §7.3)| 同 P4 + side trace 在线扩张 |

**关键观察**:P4 与 try-compile 共享 F1-F7 检查(原则 4 不松动,§4.4),但**新增**「重训练后重编译」机制——这与 P2/P3 的「不重试」纪律是**两条正交线**:

- `TierStuck`(P2 `tierState` 值,P2 管)= **编译能力上的不可达**(F1 排除 / F7 后端不支持),重试无意义,**永久不重编**;
- `P4StuckSpeculation`(P4 内部 `p4SpecState[proto]` 值,P4 管)= **投机决策上的不收敛**(反复 deopt),**计数未超阈值前 P4 自己重编译**(降级失效投机点),超阈值才转吸收态。

两者都是「不重试纪律」家族,但触发条件、归属、决策权不同。

### 4.4 F1-F7 检查继续生效,投机叠加在可编译子集之内

原则 4 与 P4 的承诺:

> P2 的可编译性检查(F1..F7)**原样沿用**:P4 仍只编译静态可编译子集,投机叠加在这个子集**之内**——「不可编译形状走 fallback」与「可编译形状内做类型投机」是**正交的两层**(原则 4 不因 P4 而松动)。

**两层正交的物理体现**:

| 维度 | F1-F7 检查 | 投机决策 |
|---|---|---|
| 决策时点 | 编译期(`analyzeCompilability`)| 编译期(每条 IC opcode 按 fb.kind 决定)|
| 决策依据 | Proto 静态特征(varargs / 元方法形式 / 后端能力等)| 运行期 feedback(numHits/metaHits 比例 / IC kind)|
| 决策粒度 | 整 Proto(全部可编译 or 全部不可编译)| per-pc(单 IC 点投机 or 不投机)|
| 失败动作 | 整 Proto 标 TierStuck,永久解释 | 单点 OSR exit,该帧剩余解释,其他点不受影响 |
| 重试 | 不重试([../p2-bridge/04-try-compile-fallback](../p2-bridge/04-try-compile-fallback.md) §7)| 重训练后允许重编(§7.3)|

**关键不变式(承 [./02-template-direction.md](./02-template-direction.md) §4 边界表)**:**F1-F7 检查继续生效,投机叠在可编译子集之内**——P4 不会因为投机机制存在就放宽 F1(不为 varargs 补编译路径)、F2(不为 unknown call 补)、F5(不为大函数补)等检查。原因:这些检查排除的是**结构性不可编译**形式,投机解决不了结构性问题(varargs 不是「类型不稳定」,是「编译器没有投机维度可挖」)。

---

## 5. P3/P4 的 ADD 投机模板对照(具体例)

本节给两段并列伪码,把 §0.2 与 §1.5 的「不对称消费」与「物理同形 / 语义异化」论证落到具体形式。**只示意不细化**——详细 opcode 模板留 [./06-backends.md](./06-backends.md) 完成。

### 5.1 P3 的 ADD 翻译(wat 伪码)+ 失败走 helper 路径

承 P3 06 §1.2 + §3.1 全展开,本节只摘要(完整 wat 伪码见 P3 06 §3.1):

```wat
;; emit_add(P3,fb=FBArithStableNumber): 把 IsNumber×2 包进 if/else 完整覆盖语义
(if (i32.and (IsNumber vb) (IsNumber vc))
  (then (f64.add ...) ; 主流路径(快路径)放 if-then,fb 提示稳定 → 命中率高
        (i64.store offset=8*A ...))
  (else                ; ★ else 永远存在,失败走同函数内 helper ★
    (br_if $err (call $h_arith ...))))   ; helper 走 P1 §4.1/§4.2 慢路径,正确返回
```

**关键性质**(承 P3 06 §1.2):

1. **else 分支永远存在**——helper 内部走慢路径(coercion + `__add` 元方法),正确返回。
2. **不离开 Wasm 函数**——helper 是 imported 调用,Go 助手返回后 Wasm 直线继续。
3. **不存在「P3 算错」的可能**——慢路径就是 P1 慢路径同源(P3 验收门 V1-V13 三方逐字节差分)。

### 5.2 P4 的 ADD 投机模板(amd64 风格伪码)+ 失败 OSR exit

P4 同 fb 输入下的投机模板(完整版见 §2.2,本节只摘对偶面):

```asm
;; emit_add(P4,fb=FBArithStableNumber, conf>=0.99): else 被裁掉
  mov rax, [r15 + 8*B]                  ; vb
  mov rcx, [r15 + 8*C]                  ; vc

  ;; ★ 投机 guard:IsNumber×2(NaN-box 单 u64 比较,§3.4)★
  cmp rax, NAN_THRESHOLD
  jae .deopt_pc                         ; ★ 不是 number → OSR exit(else 在这里被裁) ★
  cmp rcx, NAN_THRESHOLD
  jae .deopt_pc

  ;; ★ 双 number 快路径 ★
  movq xmm0, rax;  movq xmm1, rcx;  addsd xmm0, xmm1
  ;; ... NaN canonicalize ...
  movq [r15 + 8*A], xmm0
  jmp .next_pc

.deopt_pc:
  call $osr_exit_pc_<n>                 ; 物化协议详 04-osr-deopt §3
```

### 5.3 物理同形,语义/后续路径完全不同

承 P3 06 §1.1 「物理同形 ≠ 语义同义」:

| 维度 | P3(§5.1)| P4(§5.2)|
|---|---|---|
| **比较 + 条件跳的物理形式** | `i64.lt_u + br_if` | `cmp + jae` |
| **比较的对象与阈值** | NAN_THRESHOLD = 0xFFF8000000000000 | 同(NaN-box 编码同一份)|
| **失败后续路径** | `(call $h_arith ...)` —— Wasm 函数内同步调 helper | `call $osr_exit_pc_<n>` —— 离开 JIT 函数 |
| **慢路径在哪** | 同 Wasm 函数(if-else)| 解释器(出 JIT 边界后 crescent 接管)|
| **else 分支** | **永远存在且必发** | **被裁掉**(投机的字面体现)|
| **feedback 错的代价** | 100% 走 helper(慢但正确)| OSR 风暴(一次 ~µs)→ 反复时拉黑投机 |
| **是否需要 OSR/deopt 机器** | 否 | **是**(详 [./04-osr-deopt.md](./04-osr-deopt.md))|

**关键观察**:① 比较指令物理形式可完全相同(都是 `cmp + jcc` 与同一阈值,字节级几乎对齐);② 后续路径语义完全不同(P3 jcc 跳 helper,P4 jcc 跳 OSR exit);③「投机」的字面定义在这里物化——P4 把 P3 的 else 分支裁掉,等于「省略某些合法语义分支,赌它不发生」(P3 06 §1.1 末尾对投机的定义)。

### 5.4 feedback 错的代价对比表

承 P3 06 §1.5 与 [../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md) §1.3 + §1.5:

| feedback 实情 | P3 表现 | P4 表现 |
|---|---|---|
| feedback 完全准确 | 100% 命中快路径 | 100% 命中投机模板,无 deopt |
| feedback 偏差(99/1)| 99% 快路径 + 1% helper(同函数内)| 99% 投机命中 + 1% deopt(每次 ~µs)|
| feedback 严重错(50/50)| 50% 快路径 + 50% helper(慢但正确)| 50% 命中 + 50% deopt(性能塌陷)→ 计数累积 → 拉黑投机 |
| feedback 完全错(mega 标 mono)| 100% 走 helper(等同解释器无 IC)| 100% deopt → 立即拉黑 → 永久走通用模板 |
| feedback nil | 退化通用翻译 | 退化「无投机模板」(失去倍率但仍省 dispatch 税)|

**关键观察**:① **两层都不出错**——「投机错果只损性能不损正确性」字面兑现;② **代价可控性不同**——P3 的「错 feedback 性能损失」只到「等同解释器无 IC」,无加速但无回退;**P4 的「错 feedback deopt 风暴」可能比解释器还慢**(deopt 自身 ~µs 开销 vs 解释器单条 opcode ~10ns),需要拉黑投机机制兜底(§7.2);③ **P3 不需要拉黑,P4 必须有**——这是 [../p3-wasm-tier/00-overview](../p3-wasm-tier/00-overview.md) §0「P3 战略价值」的对偶兑现:P3 享受 wazero 红利零 deopt,P4 自付投机生命周期管理代价。

---

## 6. stableShape / stableIndex 直达槽投机

### 6.1 P2 的 stableShape/stableIndex 字段语义

承 [../p2-bridge/02-ic-feedback](../p2-bridge/02-ic-feedback.md) §2.1 表 IC 提取规则:

| ICSlot.kind | P2 提取的 FeedbackKind | stableShape | stableIndex |
|---|---|---|---|
| 1 array hit | `FBTableMono` | shape = ICSlot.shape(表 gen 代次)| index = ICSlot.index(array 段下标)|
| 2 node hit | `FBTableMono` | 同上 | index = ICSlot.index(node 段槽位)|
| 3 mono-metamethod | `FBTableMono` | shape = metatable gen | index = 元方法槽位 |
| 4 megamorphic | `FBTableMega` | 不填(P4 应忽略)| 不填 |

**关键性质**(承 [../p2-bridge/02-ic-feedback](../p2-bridge/02-ic-feedback.md) §2.1 末尾):

- ① **stableShape / stableIndex 反映「实际命中过的快照」**——P2 直接复制 ICSlot.shape / ICSlot.index,而 ICSlot 是 P1 解释器命中时写入的真实数据;
- ② **不是凭空构造的数字**——若 P1 从未命中过(kind=0),P2 标 FBUnstable 不填这两个字段;
- ③ **统计性快照,不是强一致**——P2 聚合时 P1 仍在跑,read-tolerant 读到的 shape/index 可能是「半新半旧」,但仍是「真实命中过的瞬时态之一」(详 [../p2-bridge/02-ic-feedback](../p2-bridge/02-ic-feedback.md) §5.4)。

**P2 字段不含 tableRef**(承 P3 06 §4.4.1):

- PointFeedback 字段定义里**没有 tableRef**——P2 聚合器没把 tableRef 编入 fb(为节省 fb 体积);
- P3 / P4 若需 tableRef 作 SNAP 立即数,必须**从 ICSlot 直接读**——这是 P3 06 §4.4 「双源选取」的根源:fb 决定路径(Kind),ICSlot 填立即数(包含 tableRef)。

### 6.2 P4 内联 guard 的物理形式

承 §2.3 GETTABLE 投机模板,本节聚焦「stableShape / stableIndex 怎么变成机器码立即数」:

```asm
;; 编译期固化的 SNAP_* 立即数:
;;   SNAP_TABLEREF = uint64(ICSlot[pc].tableRef)        ;; 从 ICSlot 直接读(P2 字段不含)
;;   SNAP_GEN      = uint32(PointFeedback.stableShape)  ;; 从 fb 读,等价 ICSlot.shape
;;   SNAP_KIND     = uint8(ICSlot[pc].kind)             ;; 从 ICSlot 直接读(细粒度,fb.kind 是粗粒度)
;;   SNAP_INDEX    = uint32(PointFeedback.stableIndex)  ;; 从 fb 读,等价 ICSlot.index

  mov rax, [r15 + 8*B]                  ; 加载 t

  ;; ★ guard 第 2 层:同表(tableRef 比对)★
  mov edx, eax
  cmp edx, SNAP_TABLEREF                ; ★ SNAP_TABLEREF 是机器码立即数(2-byte/4-byte imm)★
  jne .deopt_pc

  ;; ★ guard 第 3 层:同代次 ★
  mov rdx, rax
  and rdx, 0x0000FFFFFFFFFFFF
  cmp dword [rdx + GEN_OFF], SNAP_GEN   ; ★ SNAP_GEN 是机器码立即数 ★
  jne .deopt_pc

  ;; ★ 直达槽:SNAP_INDEX 也是立即数,offset 编译期固化 ★
  mov rcx, [rdx + ARRAY_OFF + 8*SNAP_INDEX]  ; 当 SNAP_KIND=1
  mov [r15 + 8*A], rcx
```

**关键性质**:

- ① **SNAP_* 是机器码立即数**——amd64 / arm64 都支持 cmp-immediate 与 base+offset 寻址,SNAP_TABLEREF / SNAP_GEN 直接编入指令字节,运行期不需要 load(单条指令完成比较);
- ② **SNAP_INDEX 编入 base+offset 寻址**——`[rdx + ARRAY_OFF + 8*SNAP_INDEX]` 在编译期把 `ARRAY_OFF + 8*SNAP_INDEX` 算成单个常量 offset,运行期 `[rdx + CONST]` 单条 load;
- ③ **零间接寻址**——运行期不读 ICSlot 字段,所有 SNAP 都已物化为机器码立即数,与 P3 06 §2.2 同源(P3 把 SNAP 编为 WAT 立即数,P4 编为机器码立即数)。

**与 P3 的物理形式对照**:

| 形式 | P3(WAT 立即数,经 wazero 后端编为机器码)| P4(直接发机器码立即数)|
|---|---|---|
| SNAP_TABLEREF | `(i64.const SNAP_TABLEREF)` | `cmp edx, SNAP_TABLEREF`(amd64 32-bit imm)|
| SNAP_GEN | `(i32.const SNAP_GEN)` | `cmp dword [rdx + GEN_OFF], SNAP_GEN` |
| SNAP_INDEX | `(i32.const SNAP_INDEX)` | base+offset 编进寻址常量 |

P3 经 wazero 编出来的机器码与 P4 直发的机器码在最优情况下**字节级几乎一样**——这是 [../p3-wasm-tier/00-overview](../p3-wasm-tier/00-overview.md) §8 「四项税外包给 wazero」红利的部分兑现:wazero 的 codegen 质量是 P4 的下限,P4 的优势在于**少一层 Wasm 中介**(无 wasm verification 启动税、无跨层 trampoline 进入开销)。

### 6.3 P4 投机的 guard 是 P4 自己生成,P2 提供检查输入

承 [../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md) §5.4 末:

> P4 投机的 guard 是 P4 自己生成的,不在 P2 接口范围内——但 P2 提供的 stableShape/stableIndex 是 guard 检查的输入。

**职责切分**:

| 职责 | 归属 |
|---|---|
| 决定哪些 IC 点值得投机 | **P4**(读 fb.kind + Confidence,对 conf<阈值的点不投机)|
| 生成 guard 机器码 | **P4**(amd64/arm64 各发一段)|
| 选取 guard 比对值(SNAP_*)| **P4**(从 fb 读 stableShape/stableIndex,从 ICSlot 直接读 tableRef/kind 细粒度)|
| 保证 SNAP_* 反映「真实命中过的快照」 | **P2**([../p2-bridge/02-ic-feedback](../p2-bridge/02-ic-feedback.md) §2.1 字段提取规则)|
| 在 IC slot 失效时通知 P4 | **不存在该协议**——P4 自管 deopt 计数,不依赖 P2 通知(详 §8)|

**关键不变式**:**P4 的 guard 是 P4 单方面发的代码,P2 是 guard 输入数据的供应方**。这与 P3 06 §4.4 双源选取协议同构(fb 决定路径,ICSlot 填立即数),只是 P4 把「直达槽分发」翻面成「guard 比对值」。

### 6.4 P2 必须保证字段反映「实际命中过的快照」,否则 guard 永远失败

承 [../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md) §5.4 末尾:

> P2 必须保证这两个字段反映「实际命中过的快照」,而非凭空数字——否则 P4 投机的 guard 永远失败,不符合「conf=1.0 的稳定点」预期。这条由 [../p2-bridge/02-ic-feedback](../p2-bridge/02-ic-feedback.md) §2.1 的 P2 提取规则保证。

**P2 字段保证的具体形式**:

| 字段 | P2 保证 | 不保证则 P4 后果 |
|---|---|---|
| stableShape | 来自 ICSlot.shape(P1 命中时写入)| 永久 guard 失败,deopt 风暴,投机被拉黑(§7.2)|
| stableIndex | 来自 ICSlot.index(同上)| 同上 |
| Kind ∈ {Mono, Mega, ...} | 按 ICSlot.kind 字段精确翻译 | P4 选错模板(对 mega 点发 mono guard)→ 永久 guard 失败 |

**P2 端的保护**(承 [../p2-bridge/02-ic-feedback](../p2-bridge/02-ic-feedback.md) §1.3 「P1 写不读纯供料」+ §3.4 提取规则):

- ① P1 IC 写入是真实命中(05 §6.3 doGetTable 命中后才写)——「凭空命中」不存在;
- ② P2 聚合时直接复制 ICSlot 字段,无变换、无插值;
- ③ P2 race-tolerant 读读到「半新半旧」组合时,读到的字段仍是 P1 某次真实命中的瞬时态(只是不一定是最新一次)——不会读到任何「P1 从未写入」的数字。

**这是 P2 → P4 的「输入合同」**:P2 给 P4 的 SNAP_* 必须是「P1 真实命中过的某次快照」,P4 据此发的 guard 在该快照仍有效时命中。若 P1 从此再也不命中(表 rehash 后 gen bump),guard 永久失败,**触发 deopt 风暴拉黑投机**——这是合理工作流程,不是 P2 失约。

---

## 7. deopt 兜底与重训练

本节给 deopt 工作流的「触发 → 着陆 → 计数 → 拉黑 / 重训练」全闭环。**物化协议本身留 [./04-osr-deopt.md](./04-osr-deopt.md) §3-§6 展开**,本文只把 P4 投机视角下的关键决策点钉死。

### 7.1 工作流:guard 失败 → OSR exit → 解释器接管 → IC 重新累积

承 [./04-osr-deopt.md](./04-osr-deopt.md) 全节 + [../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md) §5.5,P4 投机失败的工作流:

```
P4 投机模板执行:
   ↓
guard 失败(IsNumber 不通过 / tableRef 不匹配 / gen 不一致 / 等)
   ↓
跳到 .deopt_pc 标签 → call $osr_exit_pc_<n>
   ↓
[OSR exit 物化序列,详 04-osr-deopt §3]
   - 写 exitPC 到当前 CallInfo.savedPC(机器地址→字节码 pc 映射,编译期已记)
   - 物化 = memmove(栈槽真相不变式,01 §7 + 04-osr-deopt §3.3)
   - 经 trampoline 退出 JIT 世界(04-osr-deopt §6)
   ↓
crescent reloadFrame(05 §1.3),从 exitPC 续跑该帧
   ↓
解释器执行该帧剩余字节码:
   - 沿途 IC 写入更新观测(05 §6.4 算术 IC「写不读」纯供料)
   - 失败那条 ADD/GETTABLE 经 P1 慢路径(coercion / 元方法)正确返回
   ↓
该帧 RETURN 后,继续上层调用链;deopt 计数 +1(挂在 ProfileData 上,详 04-osr-deopt §5.1)
```

**关键性质**:

- ① **「函数级 OSR exit」是单位**(承 [./04-osr-deopt.md](./04-osr-deopt.md) §3.1)——guard 失败时**整帧剩余执行交还解释器**,不做「编译码内恢复后继续跑编译码」、不做跨帧恢复;
- ② **物化是 memmove**——栈槽里就是 NaN-box u64,crescent 直接读相同编码,无重建([./04-osr-deopt.md](./04-osr-deopt.md) §3.2);
- ③ **上层帧不受影响**——调用链上层帧各自维持自身 tier,P4 deopt 不向上传递(P4 deopt 不是 P5 trace 的「unwind 多帧 snapshot」,是单帧着陆;详 [./04-osr-deopt.md](./04-osr-deopt.md) §3.1)。

### 7.2 过阈值 → 拉黑投机(P4StuckSpeculation)

承本文 §4.2 状态机 + [./04-osr-deopt.md](./04-osr-deopt.md) §3.4:

| 事件 | 动作 |
|---|---|
| 单次 guard 失败 | OSR exit 回解释;deopt 计数 +1;解释执行继续写 IC |
| deopt 计数超低阈值(本文不预设具体数值)| 丢弃当前 gibbous 编译产物,回 `TierInterp` 重新积累 feedback;再热后**重编译**(§7.3 重训练协议)|
| 重编译后仍反复 deopt | 标 `P4StuckSpeculation`:该 Proto 永久只发无投机的通用模板(仍比解释快——dispatch 税照省),或干脆永久解释。**吸收态,防抖** |

**与 [../p2-bridge/04-try-compile-fallback](../p2-bridge/04-try-compile-fallback.md) §7 不重试纪律的对偶**:

| 维度 | P2 TierStuck(§7 不重试)| P4 P4StuckSpeculation(本节)|
|---|---|---|
| 触发条件 | F1-F7 检查拒 / 编译失败 | 投机反复 deopt 超阈值 |
| 决策权 | P2 状态机管 | **P4 自管**(不修改 P2 tierState,详 §8)|
| 是否仍能升 gibbous | 否(永久解释)| **是**——只是「不投机」,仍发通用模板叠在 dispatch 消除上 |
| 是否需要差分门兜底 | 是([../p2-bridge/04-try-compile-fallback](../p2-bridge/04-try-compile-fallback.md) §7)| 是([./08-testing-strategy.md](./08-testing-strategy.md))|

**关键决策(承 [./04-osr-deopt.md](./04-osr-deopt.md) §3.4)**:`P4StuckSpeculation` 不是「永久解释」,是「永久不投机 + 仍发 dispatch 消除模板」——这与 P2 TierStuck 的「永久解释」不同,理由是:**dispatch 税消除是 P4 的非投机收益,与投机正交**(承 [./02-template-direction.md](./02-template-direction.md) §1.1)。即便所有 IC 点都 mega、所有投机都拉黑,P4 仍有「直线机器码 + 编译期立即数操作数」相对解释器的恒定加速——保留这部分收益是合理的。

具体阈值数值与 [../p2-bridge/01-profiling §5](../p2-bridge/01-profiling.md) 一样的待定:依赖真实负载校准,只影响时机不影响正确性,留 [doc-gaps](../../../llmdoc/memory/doc-gaps.md) 跟踪;实现协议详 [./04-osr-deopt.md](./04-osr-deopt.md) §5.4。

### 7.3 RequestRefresh + 重编译协议

承 [../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md) §4.2.2 + §4.4 重聚合协议(完整工作流图见上游 §4.4,本节只摘 P4 视角的关键时序):

```
P4 投机失败 → OSR exit → crescent 接管 → IC 重积累(P1 写) → deopt 计数 +1
   ↓ 计数过阈值
P4 调 RequestRefresh(proto) → P2 标 fb 为 stale 或立即重聚合
   ↓
P2 aggregator 用最新 IC 数据产新 TypeFeedback → CAS 装到 ProfileData.feedback
   ↓
等下次升层时机,P4 调 FeedbackFor(proto) 读到更新后的 feedback → 重编译
   ↓
失效的投机点降级为通用模板;deopt 计数清零(或保留,留 04-osr-deopt §5.4 决策)
```

**关键纪律**(承 [../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md) §4.4):

- ① **P2 不主动重聚合**——只在 P4 显式 RequestRefresh 后才做(避免开销 + 简化时序);
- ② **重聚合用 CAS 装新 feedback 指针**——旧指针仍可读(P3/P4 已拿到的快照不会被破坏);
- ③ **P4 重编译用最新 feedback**——经 FeedbackFor 取,**失效的投机点降级为通用模板**(去投机重编);
- ④ **P3 可读到陈旧 feedback 仍正确**——P3 不依赖 feedback 正确性(§1.5),无重训练需求。

### 7.4 P2 在该流程的角色

承 [../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md) §5.5 末尾:

> **P2 在这个流程中的角色**:
> - **接收 RequestRefresh 信号**(§4.2.2),按策略重聚合;
> - **不参与 P4 的 deopt 计数 / 拉黑判定**——这些是 P4 内部状态;
> - **不接收 OSR exit 通知**——OSR exit 是 P4 ↔ 解释器的事,P2 不卷入。

**关键不变式**(承 [../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md) §5.5 末「P2 状态机不区分 P3 / P4」):

> P2 状态机([../p2-bridge/04-try-compile-fallback §2](../p2-bridge/04-try-compile-fallback.md))只有 TierInterp/TierGibbous/TierStuck 三态,**TierGibbous 不区分「P3 编译的」与「P4 编译的」,也不区分「投机版本」与「通用版本」**。P4 在 TierGibbous 内部自己管投机状态(deopt 计数、拉黑、重训练),P2 只看「这 Proto 升过没」。

**P2 视角的物理形式**:

```
P2 ProfileData.tierState:
   TierInterp   ←──── (升层) ────  TierGibbous  ←──── (永驻吸收态) ────  TierStuck
                                        │
                                        │ ★ TierGibbous 内部 P4 自管 ★
                                        ▼
                              ┌─────────────────────┐
                              │ P4 投机生命周期管理:  │
                              │   deopt 计数        │
                              │   投机版本 vs 通用版本│
                              │   P4StuckSpeculation │
                              │   (P2 不感知此细分)   │
                              └─────────────────────┘
```

**这是「共享前端」原则在 P4 deopt 视角的兑现**:P2 一份接口,P3/P4 共用,但各自的特殊状态(P3 无,P4 有 deopt 生命周期)由各自后端在 TierGibbous 这把伞下私管,**P2 实现代码不为 P4 单独建状态字段**——这条是 §8 P4 不依赖 P2 状态机硬纪律的核心。

---

## 8. P4 不依赖 P2 状态机的硬纪律

承 [../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md) §5.6 直接复刻,本节为 P4 实现 review 时自检清单。

### 8.1 ❌ 修改 P2 ProfileData.tierState

**禁止**:P4 实现时直接写 `bridge.ProfileData.tierState`(把它从 TierGibbous 改回 TierInterp 用作「降层」)。

**理由**:

- ① P2 状态机([../p2-bridge/04-try-compile-fallback](../p2-bridge/04-try-compile-fallback.md) §2)是单向无环——加任何反向边违反 P2/P3 零 deopt 设计;
- ② P4 自管投机生命周期(§7.2 / §7.3),不需要把 deopt 信号传到 P2 状态机;
- ③ 多 State 共享 Proto 时,一个 State 的 P4 deopt 不应影响其他 State 看到的 tierState。

**正确做法**:P4 用**自己的状态字段**(`p4SpecState[proto]`)管 deopt 计数与投机版本,P2 的 tierState 始终保持 TierGibbous(从 P2 视角看「升过了」)。

### 8.2 ❌ 假设 P2 deopt 后自动降回 TierInterp

**禁止**:P4 实现时假设「我触发 OSR exit 后,P2 会自动把 tierState 改回 TierInterp」。

**理由**:

- ① P2 单向状态机不会自动降层(同 §8.1);
- ② P2 不接收 OSR exit 通知(§7.4)——P2 根本不知道 P4 deopt 发生;
- ③ 假设 P2 自动降层会导致 P4 实现与 P2 状态机产生隐式耦合,P3/P4 切换时 P2 实现零修改的承诺破裂([../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md) §0.3)。

**正确做法**:P4 deopt 后,**该帧返回上层**;后续如何处理(继续走通用模板 / 拉黑 / 重训练)由 P4 自管,从 P2 视角看「Proto 仍是 TierGibbous」,只是 P4 本帧选择了不投机或返回解释。

### 8.3 ❌ 通过 P2 接口拿 P3 编译产物再修改

**禁止**:P4 实现时通过 P2 接口拿到 P3 的 GibbousCode,然后修改其内部数据(企图「在 P3 产物上加 guard」)。

**理由**:

- ① GibbousCode 是只读共享对象(承 [../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md) §6.4),多 State 并发使用,修改会破坏其他 State;
- ② P3 与 P4 是两个独立的发射后端(`internal/gibbous/wasm` vs `internal/gibbous/jit`),各自产物隔离——P4 自己生成原生码,不在 P3 的 wazero module 上叠加;
- ③ P3 已编译好的 wazero compiled module 是不可变的(wazero API 无写回口),企图修改要么失败要么破坏 wazero 内部状态。

**正确做法**:P4 自己实现 `bridge.P3Compiler` 接口(产 P4 版 GibbousCode,内部是原生码段),与 P3 的 GibbousCode(wazero module)在 `Bridge.gibbousIndex` 中**择一安装**——同一个 Proto 在某 Program 实例下要么由 P3 编要么由 P4 编,不并存。

### 8.4 ✅ 正确做法清单

承 [../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md) §5.6 末尾:

| 行为 | 实现方法 |
|---|---|
| ✅ 通过 `P4Feedback.FeedbackFor` 反向读 feedback | 持 `*Bridge`(实现 P4Feedback),按需调 FeedbackFor(本文 §1.4)|
| ✅ 通过 `P3Compiler.Compile` 上交编译产物 | P4 实现 P3Compiler 接口,Compile 返回 P4 版 GibbousCode([../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md) §6.3)|
| ✅ 自管 deopt 计数与 P4StuckSpeculation 状态 | P4 在 `internal/gibbous/jit` 内部维护 `deoptCount[proto]`,P2 不参与(§7.2)|
| ✅ 通过 `RequestRefresh` 请求 P2 重聚合 | P4 deopt 计数过阈值时调 P2 RequestRefresh(本文 §7.3)|
| ✅ 在 GibbousCode.Run 内自管「投机版本 vs 通用版本」分流 | P4 的 dummyCode-style 实现可持「投机版 entry」+「通用版 entry」双入口,Run 选哪个由 P4 内部状态决定 |

**P2 与 P4 解耦的工程红利**(承 [../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md) §5.6 末尾):

> P2 的状态机只关心「升过没/卡死没」,P4 的投机状态完全自包含。这让 P2 PB6 mock 与真 P3 / P4 三阶段切换零修改 P2 实现。

---

## 9. 不变式清单

P4 IC feedback 投机消费的实现期硬性约束,违反即设计失败:

### 9.1 「快路径 + guard,失败 OSR exit,不出错」

承 §1 / §2 / §5 / §7。物理表现:每个投机模板形如 `[guard] → [fast path] → [.deopt_pc → call $osr_exit_pc_<n>]`。**guard 失败时栈槽里就是合法 NaN-box u64**(物化集合为空,详 [./04-osr-deopt.md](./04-osr-deopt.md) §3.3),OSR exit 把该帧剩余执行整体交还 crescent,crescent 走完整 Lua 5.1 语义路径(coercion / 元方法 / 错误抛出)正确返回。

**与 P3 的镜像论证**:P3 06 §1.5 「即便 feedback 完全错误,P3 仍正确」是 P3 视角的一样的承诺,机制不同(P3 失败走同函数 helper,P4 失败走 OSR exit)但结论同——投机错果只损性能不损正确性,差分门兜底(§3.5 + [./08-testing-strategy.md](./08-testing-strategy.md))。

### 9.2 「guard 物理形式 = 比较 + 条件跳,无信号陷阱」

承 §3 全节。物理表现:每个 guard 都形如 `cmp + jcc`(amd64)或 `cmp + b.cond`(arm64),2-3 条机器指令的恒定成本——**没有任何依赖 SIGSEGV / SIGBUS / SIGFPE 的隐式 guard**。

**与 LuaJIT 的对照**:LuaJIT 的「movq xmm0, rax 后 fault → 信号处理器接管」形式在纯 Go 下不可用([../roadmap](../roadmap.md) §2 runtime 所有权),P4 显式化是 [../../../llmdoc/must/design-premises](../../../llmdoc/must/design-premises.md) 前提二的同源约束。**6% 那一档差距(LuaJIT vs luajc)正是这条约束的微观注脚**(§3.2 末)。

### 9.3 「投机叠在 F1-F7 子集之内,不松 P2 检查」

承 §4.4 + 原则 4。物理表现:P4 不会因为投机机制存在就放宽 F1(varargs)/ F2(unknown call)/ F5(大函数)等检查——**这些检查排除的是结构性不可编译,投机解决不了结构性问题**。

**两层正交的工程兑现**(承 §4.4 表):

- F1-F7 在编译期决定整 Proto「能不能编」(per-Proto 粒度);
- 投机决策在编译期决定每个 IC 点「能不能投机」(per-pc 粒度);
- 两层 AND 逻辑:Proto 必须 F1-F7 通过 + IC 点必须 confidence 达标 才发投机模板。

### 9.4 「P4 自管投机生命周期,P2 状态机不感知」

承 §7.4 + §8 全节。物理表现:

- P2 ProfileData.tierState 只有 TierInterp / TierGibbous / TierStuck 三态,**P4 投机 deopt 不修改 tierState**;
- P4 的 deopt 计数 / P4StuckSpeculation / 重训练触发 全部在 `internal/gibbous/jit` 内部状态字段管;
- P2 接收 RequestRefresh 信号(§7.3)是反向通道,与 P2 状态机正交。

**这条不变式确保 P3 / P4 切换零修改 P2 实现**——P3 上线时 mock → 真 P3 切换零修改([../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md) §8.1),P4 上线时同样零修改 P2(§8 P4 不依赖 P2 状态机的硬纪律)。

### 9.5 「stableShape/stableIndex 反映真实命中,P2 输入决定 P4 guard 命中率」

承 §6.1 / §6.4。物理表现:

- P2 的 PointFeedback.stableShape / stableIndex 必须来自 ICSlot.shape / ICSlot.index(P1 命中时写入的真实数据),**不是凭空构造**;
- P4 据此发的 guard 在该快照仍有效时命中(投机收益兑现),失效时 deopt(进入 §7 重训练流程);
- P2 没把 tableRef 编入 fb,P4 必须从 ICSlot 直接读 tableRef 作 SNAP_TABLEREF——这是 P3 06 §4.4 双源选取协议的 P4 一样的应用。

**反例反证**:若 P2 把 stableShape 写成「期望值」(如「希望表 gen=42」)而非真实命中值,P4 的 guard 在 P1 真实命中 gen=43 时永久失败,deopt 风暴拉黑投机——这是 P2 失约,不是 P4 失败。**P2 提取规则的「直接复制 ICSlot 字段」纪律是这条不变式的物理基础**([../p2-bridge/02-ic-feedback](../p2-bridge/02-ic-feedback.md) §2.1)。

---

## 10. 风险与开放问题

### 10.1 guard 密度天花板对 f64 快路径净收益的影响

P4 投机的核心风险——guard 密度天花板:

> **无信号陷阱的密度天花板**:全显式 guard 的成本若实测吃掉投机收益的大头,f64 快路径的净收益要重新核算。

**风险展开**:

- 算术 IC 的「IsNumber×2 guard」每次 2-3 cycle,加上 f64 算术本身 1-3 cycle(addsd/mulsd),**guard 成本占快路径总成本 30-60%**;
- 与 LuaJIT 同位 guard 免费(信号陷阱)对比,P4 的 guard 是恒定税——guard 密集的代码段(连续算术 + 频繁 IC opcode)收益打折;
- 列内核负载的算术热路径正是 P4 验收锚点(luajc 档,Horner 多项式 1000 items),guard 密度在此场景的实测净收益是 P4 验收成败的关键。

**缓解(本文 §3.6)**:guard 合并(同操作数直线段内只查一次)是窥孔级优化,不引入 IR;但合并的范围必须锁在「直线段」内,不可借势扩到跨 BB(§3.6 末「不可借势扩到跨 BB」纪律)。

**实测路径**:[./01-launch-judgment.md](./01-launch-judgment.md) §4.3 已立的「中途检查」——单架构(amd64)+ 仅算术投机的最小 P4 先打通全管线并测 Horner 档位,**若 guard 密度天花板让净收益不够,本文 §3.6 guard 合并的窥孔范围可能扩张**(留 §11 回填请求空间)。

### 10.2 guard 合并的窥孔优化范围

承 §3.6 末「不可借势扩到跨 BB」纪律,guard 合并的工程边界:

| 形式 | 是否允许 | 理由 |
|---|---|---|
| 同 BB 内同操作数同投机的合并 | ✅ 允许(§3.6 形式)| 窥孔级,不引入 IR |
| 跨 BB 但简单线性 CFG(单后继单前继)的合并 | ⚠️ 待评估 | 边缘 case,可能仍属窥孔范围 |
| 跨 BB 数据流分析的冗余 guard 消除 | ❌ 禁止 | 需要 SSA + use-def,直接进入 P5 IR 优化器领地 |
| 跨 Proto 的 guard 提升(caller 提前 guard 后传给 callee)| ❌ 禁止 | 需要内联,P5 trace JIT 才做 |

**开放点**:跨 BB 简单线性 CFG 是否纳入 P4 窥孔范围,留 P4 实现期实测决策——若 guard 密度天花板(§10.1)实测显著伤收益,且实现跨 BB 简单线性合并的工程成本低,可考虑微扩;反之保守只做同 BB。

### 10.3 confidence 阈值定标依赖真实负载

承 §2.7 + [../p2-bridge/01-profiling §5](../p2-bridge/01-profiling.md):

- P2 端的 `stableArithThreshold = 0.99` 是占位(02-ic-feedback §5.2);
- P4 端是否再加 confidence 阈值(§2.7 表)是开放;
- 阈值的最优值依赖真实负载(列内核 vs 混合 vs per-item),无法在文档阶段定稿。

**留 P4 实测后定**——开放问题之一,不影响正确性。

### 10.4 ADD/SUB 之外算术 op 的子分流粒度损失

承 P3 06 §6.5 + [../p2-bridge/02-ic-feedback](../p2-bridge/02-ic-feedback.md) §9.2:P1 算术 IC 双计数对 ADD/SUB/MUL/DIV/MOD/POW/UNM/LT/LE 一视同仁,P2 不区分具体算术 op,P4 emitArith 对所有算术 op 都按 FBArithStableNumber 内联一样的 IsNumber×2 guard——**算术 op 之间无粒度损失**(快路径形式相同,仅 f64 指令不同)。

**LT/LE 子分流损失**:LT/LE 的 numHits 不区分「双 number 快路径」与「双 string 快路径」,P4 拿到 FBArithStableNumber 在 LT/LE 上是「快路径稳定」(可能 number 也可能 string)——若 P4 投机时按双 number 发 `f64 cmp` guard,运行期来双 string 时 guard 失败 deopt。解决路径留 P2+ 实测后补:P1 比较 IC 写入加分流字段 → P2 提取为 `FBArithStableString` 新枚举值 → P4 emitCompare 据此发对应内联快路径。**记入 [doc-gaps](../../../llmdoc/memory/doc-gaps.md) 与 [../p2-bridge/02-ic-feedback](../p2-bridge/02-ic-feedback.md) §9.2 共享**。

### 10.5 多 State 并发下 P4 deopt 状态的语义

承 [../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md) §6.4 + [../p2-bridge/00-overview §9](../p2-bridge/00-overview.md):多 State 共享 Proto 时,GibbousCode 是只读共享对象;**P4 的 deopt 计数挂哪**(全局 `p4SpecState[proto]` vs per-State)需实现期决策;反复 deopt 拉黑 P4StuckSpeculation 后,**所有 State 共享该 Proto 的「不投机」决策**(投机决策是统计性的,所有 State 命中数据共同贡献到 deopt 计数)。**记入 [doc-gaps](../../../llmdoc/memory/doc-gaps.md)**——不影响正确性,只影响投机决策粒度。

---

## 11. 回填请求

本节按 [multi-doc-drafting](../../../llmdoc/guides/multi-doc-drafting.md) 协议登记本文起草中发现的、需要 P3 / P2 现稿增字段或调整的请求。**主助理任务收尾阶段统一兑现,本文先列明细**。

### 11.1 P2 现稿(02-ic-feedback / 05-p3-p4-interface)

| # | 文档 | 节 | 内容 | 优先级 |
|---|---|---|---|---|
| RB-1 | [../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md) | §5.5(P4 deopt 兜底与重训练)| 与本文 §7.3 / §7.4 字面同源化;明确 P2 接受 RequestRefresh 后 CAS 装新 feedback,P3 旧指针仍可读 | 中 |
| RB-2 | [../p2-bridge/02-ic-feedback](../p2-bridge/02-ic-feedback.md) | §9.2(LT/LE 子分流缺口)| 本文 §10.4 是其 P4 视角的对偶兑现,引用本文作 P4 端实证 | 低 |
| RB-3 | [../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md) | §5.6(P4 不依赖 P2 状态机硬纪律)| 本文 §8 直接复刻该节,可在 P2 文档侧加引用「本节具体形式见 P4 §3 §8」 | 低 |

### 11.2 P3 现稿(06-ic-feedback-consume)

| # | 文档 | 节 | 内容 | 优先级 |
|---|---|---|---|---|
| RB-4 | [../p3-wasm-tier/06-ic-feedback-consume](../p3-wasm-tier/06-ic-feedback-consume.md) | §1.1 「P3 / P4 物理同形 ≠ 语义同义」 | 本文 §0.2 / §5.3 把这条对偶面在 P4 视角全展开,可在 P3 06 加引用「P4 视角对偶兑现见 P4 §3 §5」 | 低 |
| RB-5 | [../p3-wasm-tier/06-ic-feedback-consume](../p3-wasm-tier/06-ic-feedback-consume.md) | §6.1(IC 失效是否触发重编译,留 P4 评估)| 本文 §7.3 给出 P4 端的重训练协议——P3 IC 失效永久 miss 与 P4 投机 deopt 反复失败统一在 P4 RequestRefresh + 重编译协议处理(详见 [./04-osr-deopt.md](./04-osr-deopt.md) §5)| 中 |

### 11.3 P4 子目录其他文档(本子目录内回填)

| # | 文档 | 节 | 内容 | 优先级 |
|---|---|---|---|---|
| RB-6 | [./04-osr-deopt.md](./04-osr-deopt.md) | §5(deopt 计数 + P4StuckSpeculation)| 本文 §7.2 给 P4 视角,04 §5 给具体物化协议;两文协同覆盖完整闭环 | 高 |
| RB-7 | [./08-testing-strategy.md](./08-testing-strategy.md) | (差分接入「投机错果」)| 本文 §3.5 提名差分主防线,08 起草时引用本文 §3.5 / §9.1 | 高 |
| RB-8 | [./06-backends.md](./06-backends.md) | (per-arch 发射函数)| 本文 §2 / §5 给伪汇编示意,06 起草时本文作 amd64 端母版 | 中 |
| RB-9 | [./02-template-direction.md](./02-template-direction.md) | §2.4 / §4.1 / §4.4「子集内投机」承诺 | 本文 §4.4 落具体形式;02 引用本文作具体兑现处 | 已对接 |

承 [multi-doc-drafting](../../../llmdoc/guides/multi-doc-drafting.md) 协议:**本文起草仅在 §11 登记回填请求,不主动修改 P3 / P2 / 其他子目录文档**;所有跨文档同步由主助理收尾时统一处理。

---

## 12. 相关

- [./02-template-direction.md](./02-template-direction.md)(方向裁决——本文 §2 / §4 / §5 是其「子集内投机」承诺的具体兑现处)
- [./04-osr-deopt.md](./04-osr-deopt.md)(OSR exit 物化协议、deopt 状态机、再训练防风暴——本文 §7 / §9.1 落对位,具体物化协议在 04)
- [./05-system-pipeline.md](./05-system-pipeline.md)(jitContext 注入 / 自管栈 / trampoline——本文 §2 amd64 伪汇编里的 r15/r14 等寄存器约定的 single source)
- [./06-backends.md](./06-backends.md)(amd64/arm64 双后端 per-arch 发射函数——本文 §2 / §5 是其 amd64 端的母版)
- [./08-testing-strategy.md](./08-testing-strategy.md)(差分接入「投机错果」最危险 bug 类——本文 §3.5 / §9.1 提名,具体口径在 08)
- [../p3-wasm-tier/06-ic-feedback-consume](../p3-wasm-tier/06-ic-feedback-consume.md)(P3 IC 非投机消费,**本文核心镜像章节**——P3 06 §1 / §3 与本文 §1 / §2 是镜像章节)
- [../p3-wasm-tier/02-translation](../p3-wasm-tier/02-translation.md)(P3 翻译器主体——本文 §5 给出 P4 amd64 伪汇编与之物理同形 / 语义异化的对照)
- [../p2-bridge/02-ic-feedback](../p2-bridge/02-ic-feedback.md)(TypeFeedback shape 与 confidence 计算单一事实源,本文是 P4 投机消费侧)
- [../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md)(**P4 反向读 feedback 接口的单一事实源**;§4 P4Feedback 接口、§5 P4 投机供料语义、§5.6 P4 不依赖 P2 状态机的硬纪律)
- [../p2-bridge/04-try-compile-fallback](../p2-bridge/04-try-compile-fallback.md)(P2/P3 零 deopt 状态机基线——本文 §4 加 deopt 边的对照参照)
- [../p1-interpreter/01-value-object-model](../p1-interpreter/01-value-object-model.md) §3.2 / §7(NaN-box 编码 + 值表示不变式 1「跨 tier 拷贝是 memmove」——本文 §3.4 guard 物理形式)
- [../p1-interpreter/02-bytecode-isa](../p1-interpreter/02-bytecode-isa.md) §7(ICSlot 结构;算术 IC 双计数挪用)
- [../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md) §6(IC 执行机制——本文 §1 供料链下游)
- [../roadmap](../roadmap.md)(§2 四项税 / §4 P4 验收 luajc 档 / §7 prior art:V8 Sparkplug / JSC Baseline JIT)
- [../../../llmdoc/must/design-premises](../../../llmdoc/must/design-premises.md)(前提一负载形状 / 前提二四项税 / 前提三五原则——投机错果是 JIT 第一危险源 / 前提四第一天 NaN-box 承诺)
- [../../../llmdoc/architecture/evolution-roadmap](../../../llmdoc/architecture/evolution-roadmap.md)(tier 映射:P4 = gibbous tier-1)










