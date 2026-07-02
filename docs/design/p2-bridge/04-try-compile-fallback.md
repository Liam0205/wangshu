# P2 §4:升降层决策子系统(TierState 状态机 + considerPromotion + 零 deopt)

> 状态:**设计阶段,详细设计已齐备**。本文是 [00-overview](./00-overview.md) §0 文档地图列出的 **P2 决策中枢单一事实源**——TierState 状态机(单向 + 吸收态)、升层决策入口 `considerPromotion`、TierStuck 不重试纪律、零 deopt 论证、`fallback ≠ deopt` 严格区分、升层日志格式(`promoted to gibbous`)。
> 上游种子:[../p2-bridge/00-overview](./00-overview.md) §5(try-compile-fallback) + §6(状态机),本文大量扩展。
> 上游契约:[00-overview](./00-overview.md) §1(P2 不在执行热路径)、§2(总数据流)、§3(关键耦合 5 CallInfo bit50)、§6(决策表)、§9(P3 后端跨版本升级 stuck 重评估缺口);
> [01-profiling](./01-profiling.md) §0.2(`considerPromotion` 入口契约)、§4.3(调用契约)、§6.5(profileTable 持有 ProfileData);
> [02-ic-feedback](./02-ic-feedback.md) §0(P2 写 feedback 不消费,仅供 P3/P4)、§4.5(ProfileData.feedback 槽 / `installFeedback` CAS)、§6.4(`aggregator.aggregate(proto)` 入口);
> [03-compilability-analysis](./03-compilability-analysis.md) §1(保守第一,宁漏勿误)、§5.1(`Compilability` 三态)、§5.2(`AnalyzeProto` 入口)、§5.5(P1 占位 `CompUnknown` 视同 `CompNotCompilable`);
> P1 依赖面:[../p1-interpreter/05-interpreter-loop.md](../p1-interpreter/05-interpreter-loop.md) §1.2(CallInfo word2 bit50 `callStatus_gibbous`,P1 恒 0,P2 升层时写 1);
> [../p1-interpreter/00-overview.md](../p1-interpreter/00-overview.md) §5(P1 前瞻义务对账,bit50 已落地);
> 下游契约:[05-p3-p4-interface](./05-p3-p4-interface.md)(`P3Compiler.Compile` 接口本文 §4 调用,05 是其单一事实源);
> [../p3-wasm-tier/04-trampoline.md](../p3-wasm-tier/04-trampoline.md)(跨层 trampoline 消费 CallInfo bit50);
> 上游原则面:[../roadmap.md](../roadmap.md) §5 原则 1(解释器永不退役 / fallback 着陆点)、原则 3(每阶段独立交付不亏)、原则 4(不可编译形状走 fallback)、[../../llmdoc/must/design-premises.md](../../llmdoc/must/design-premises.md) 前提三原则 1 / 4。
>
> **本文定位一句话**:**P2 是「分层决策中枢」,这台决策机的状态空间由本文定稿——三个状态、两条边、零回边**。状态机的「单向 + 吸收态」是 P2/P3 「零 deopt」的形式化体现:任何 Proto 一旦升层(`TierGibbous`)或卡死(`TierStuck`),**都不会回到 `TierInterp`**——P2 不存在「降层」概念。这把分层 VM 最危险的 bug 类(投机错误静默错果,roadmap §5 原则 2)在 P2/P3 阶段从根上消除——**没有投机就没有投机错误**。

---

## 0. 定位:P2 决策中枢——状态机的吸收态形式化

### 0.1 P2 信息流的最后一棒

承 [00-overview](./00-overview.md) §1 / §2,P2 的总流水线:

```
   P1 解释器执行期(crescent,已落地)
   │            │              │
   FORLOOP/JMP  IC slot 写       Compile 时 AST
   回边采样      (P1 写不读)      (04 codegen 产出)
   (01-profiling) (02-ic-feedback) (03-compilability-analysis)
   │            │              │
   ▼            ▼              ▼
   热度计数      IC 反馈聚合      Compilability 缓存
   (热不热)      (类型稳不稳)     (能不能编)
   │            │              │
   └────────────┴──────┬───────┘
                       ▼
            ┌──────────────────────┐
            │ ★ 本文 ★               │
            │ TierState 状态机     │
            │ considerPromotion 入口│
            │ try-compile + fallback│
            └──────────────────────┘
                       │
        ┌──────────────┴──────────────┐
        ▼ (热 && 可编译 && 编译成功)     ▼ (其它三条)
    TierGibbous(升层)              TierStuck(永久解释)
        │                               │
        ▼                               ▼
   走 P3 Wasm 层(P3 §5)        走 P1 解释(原则 1 着陆点)
```

**本文是 P2 的「最后一棒」**——前三个子系统(01/02/03)各自产出热度信号、类型反馈、可编译性缓存,本文把三者汇聚成一台状态机,产出二元决策(升 gibbous / 永久解释)。

### 0.2 三个不变式(贯穿全文)

承 [../p2-bridge/00-overview §8](./00-overview.md) §8 的同源不变式与 [00-overview](./00-overview.md) §6 决策速查表,本子系统的三条铁律:

1. **P2 只有 fallback,没有 deopt**(§1 详):fallback 是「编译前静态决定永久解释」,deopt 是「编译后运行期假设失败退回解释器」(P4 才有)。**P2 阶段一个 Proto 从不「升完又回」**——这是「零 deopt」的字面体现。
2. **TierState 是单向 + 吸收**(§2 详):状态转移图无环、无回边;`TierGibbous` 与 `TierStuck` 都是吸收态(无出边);唯一起点 `TierInterp` 有两条出边(`→TierGibbous` 升层成功 / `→TierStuck` 不可编译或编译失败)。
3. **TierStuck 是永久终态**(§7 详):一旦标 Stuck(无论是不可编译还是编译失败),**单次运行内永不再尝试**——`considerPromotion` 入口直接 no-op。这条纪律防住「不可编译热函数每次越阈值都重试」的抖动(§7.1)。

这三条不变式共同形式化了 P2 与 P3 的「零 deopt」:**没有运行期事件能让一个 Proto 从 gibbous 回到 interp**(§8 形式化论证)。

### 0.3 与 P3 / P4 的边界(谁拥有什么)

| 关注点 | P2(本文)拥有 | P3([p3-wasm-tier](../p3-wasm-tier/00-overview.md))拥有 | P4(p4-method-jit)拥有 |
|---|---|---|---|
| **状态机** | ✅ 单向 + 吸收(无 deopt 边) | 接受 P2 的升层请求,不维护状态 | **加 deopt 边**(`Gibbous → Interp`) |
| **升层决策** | ✅ `considerPromotion`(§3) | 被动响应 `Compile` 调用 | 同 P3 |
| **降层决策** | ❌ **不存在** | ❌ 不存在 | ✅ 投机失败 OSR exit(P4 独有) |
| **CallInfo bit50 写** | ✅ `installGibbous` 写 1(§4.4) | — | — |
| **CallInfo bit50 读** | — | ✅ trampoline 据此判帧形态(P3 §5) | ✅ 同 P3(P4 trampoline 复用 P3 接口) |
| **try-compile 调用** | ✅ 调 `P3Compiler.Compile`(§4.1) | 实现 `Compile` 接口(05 §1) | 同 P3,只换发射后端 |
| **编译失败后处理** | ✅ 标 `TierStuck`,不重试(§7) | 返回 err,不持有任何状态 | 同 P3 |

**关键边界**:**P2 是消费者-决策者-写状态者,P3 是被动编译器,P4 才是首个引入「降层」概念的层**。本文的状态机干净、单向——一旦 P4 上线,会扩展状态机加 `Gibbous → Interp` 的 deopt 边(届时另起一篇「P4 状态机扩展」),但**不修改本文定稿的 P2/P3 单向部分**(原则 3「每阶段独立交付」)。

### 0.4 与 [./00-overview.md] §5 / §6 的关系

[../p2-bridge/00-overview](./00-overview.md) §5(try-compile-fallback)+ §6(状态机)是 P2 单文件原稿里的种子,本文是它的**详细设计扩展**(承 [00-overview](./00-overview.md) §0 的「决策中枢单一事实源」分工)。两文档关系:

| 内容 | 单文件原稿 §5/§6 | 本详细设计 |
|---|---|---|
| fallback ≠ deopt 区分 | §5.1 一段简版定义 | §1 全展开(对比表 + 三层论证 + 与 P4 的根本分野) |
| 零 deopt 论证 | §5.1 / §6.1 形象描述 | §8 形式化论证(状态机无环 + 单向证明) |
| TierState 枚举 | §6.1 给三状态 | §2 状态机定稿(完整 Go 枚举 + ASCII 图 + 转移条件表) |
| considerPromotion 代码 | §6.2 给一段骨架 | §3 完整(并发 CAS 守卫 + 错误分类 + 4 路径处理) |
| try-compile 协议 | §5 暗含 | §4 全展开(`P3Compiler.Compile` 调用 + `installGibbous` 协议 + bit50 写 + recover 兜底) |
| TierStuck 不重试 | §6.3 一段 | §7 完整(防抖论证 + 编译失败确定性 + P3 跨版本 stuck 重评估缺口) |
| 升层日志 | §6.4 给三类格式 | §6 全展开(诊断接口 + 日志 helper + 失败 stuck 信息表) |
| 接口承诺 | (无) | §9 P3 trampoline 共享前端契约 + 接口稳定保证 |

**单文件原稿 §5/§6 = 决策方向**;**本文 = 字段级 + 代码骨架 + 形式化论证 + 接口约定**。两者并存,后续维护以本详细设计为准。

---

## 1. fallback ≠ deopt(关键概念辨析)

### 1.1 一句话定义

- **fallback**:**编译前**就静态决定「这个 Proto 永久走解释」。一次性、不可逆、不可观察(没有运行期事件标记它)。
- **deopt**:**编译后**运行期投机假设失败,从已经在执行的编译码退回解释器。可反复、可观察(每次 deopt 都有日志/指标)、需要 OSR 着陆机制。

### 1.2 对比表

| 维度 | **fallback**(P2/P3) | **deopt**(P4/P5) |
|---|---|---|
| 决定时机 | **编译前**(静态分析,03 §0.2) | **运行期**(投机 guard 失败) |
| 触发原因 | ① 不可编译形状(F1-F7,03 §3) ② try-compile 失败(F7 边角 + 后端 panic,§5) | guard 失败:类型偏离(IC feedback 错)、metamethod 触发、栈溢出等 |
| 着陆点 | crescent 解释器(原则 1) | crescent 解释器(原则 1,同) |
| 重试 | **永不重试**(TierStuck,§7) | **可反复**(每次 deopt 后可继续投机) |
| 决策频率 | **一次性**(单次运行内一次决定永久) | **多次**(同一 Proto 可 deopt 多次) |
| 可观察性 | 编译失败日志一次(§6) | 每次 deopt 都有日志/指标(P4) |
| 安全机制 | 静态分析保证「编了的都能正确跑」 | guard + OSR exit 机器(投机错就退回) |
| 投机错误风险 | **不存在**(没有投机) | 存在(roadmap §5 原则 2 主防线) |
| 状态机表现 | `TierInterp → TierStuck` 单向 | `TierGibbous → TierInterp` 反向边 |

### 1.3 三层论证:为什么 P2 只有 fallback 没有 deopt

#### 1.3.1 静态保证 vs 运行期假设

承 [03-compilability-analysis](./03-compilability-analysis.md) §1.2 与 [../p2-bridge/00-overview](./00-overview.md) §5.1:

- **P2/P3 走 try-compile**:[03](./03-compilability-analysis.md) 的 F1-F7 形状判定是**静态保证**——编译的子集对**任何运行期输入**都正确。编译出的 gibbous 代码不依赖任何运行期类型假设(P3 是「锦上添花的紧凑翻译」非「赌类型」,02 §0)。**所以根本不需要 deopt**——没有「假设被打破」的可能。
- **P4/P5 走投机 JIT**:P4 假设 IC feedback 反映的类型是稳定的(如「这个点恒为 number」),据此发激进快路径(`f64.add` 直接发,不查 metamethod)。运行期若来了非 number 操作数,**guard 失败 → 必须 OSR exit 回解释器**——这就是 deopt。

**关键差异**:**有没有「投机假设」**。P2/P3 编译可编译子集 = 不投机;P4 投机类型分布 = 必须有 deopt 兜底。

#### 1.3.2 安全责任的位置

| 阶段 | 安全责任在哪 |
|---|---|
| P2 fallback | **静态分析**([03](./03-compilability-analysis.md) §1 「保守第一,宁漏勿误」)——任何不可编译形状都判 `CompNotCompilable`,从源头排除编译 |
| P4 deopt | **运行期 guard**(P4 §IC 投机)——发激进快路径但加 guard,失败即退回 |

P2 的安全责任前移到「编译前」,P4 的安全责任后置到「编译后运行期」。**P2 阶段没有「编译后运行期失败」可能**(03 静态保证),所以也没有「编译后退回」需求。

#### 1.3.3 与「投机错误静默错果」的关系

[roadmap §5 原则 2](../roadmap.md):**「层间逐字节差分测试是防『投机错误静默错果』(JIT 最危险 bug 类别)的主防线」**。

「投机错误静默错果」指投机 JIT 因类型假设错误产出错误结果但**系统不知道是错的**(没 deopt、没崩溃,只是结果不对)。这是 P4 必须严防的——主防线是层间差分(每个 deopt 着陆都对拍解释器)。

**P2/P3 阶段不存在这个风险**:
- P2 不发射代码(00 §1),只决策,无「错误代码」概念;
- P3 发射的代码是 try-compile 子集,语义层面与解释器等价,无投机假设可错;
- 如果 [03](./03-compilability-analysis.md) 误判(把不可编译形状判可编译)→ P3 编译错误代码 → 静默错果 —— **这是 [03 §1.1](./03-compilability-analysis.md) 把误判判为「灾难性后果」的本质**,03 用「保守第一」从源头消除。

**结论**:**fallback(本文)与可编译性分析(03)联手,把 P2/P3 阶段的「投机错误静默错果」从根上消除**。这是 P2/P3 与 P4 的根本分野,也是 [roadmap §4 P3 战略价值](../roadmap.md) 「在不用调试机器码的后端上,先把分层机器整体跑通」的物理基础——P3 不操心 deopt,只把翻译 + trampoline 跑通即可。

### 1.4 fallback 的两种触发与 deopt 的区分

承 [../p2-bridge/00-overview](./00-overview.md) §5.3,P2 阶段 fallback 有**两种触发**——都不是 deopt:

| 触发 | 时机 | 状态机转移 |
|---|---|---|
| **静态 fallback** | `Compile` 时 [03](./03-compilability-analysis.md) `analyzeProto` 判 `CompNotCompilable`(F1-F7 任一);`considerPromotion` 入口直接看 cached 结果 | `TierInterp → TierStuck`(不进 try-compile) |
| **try-compile fallback** | `considerPromotion` 调 P3 `Compile` 编译失败(F7 边角漏判 / P3 后端 panic / 资源耗尽,§5) | `TierInterp → TierStuck`(进 try-compile 但失败) |

两种 fallback **本质相同**:都是「Proto 永久走解释,P2 不再触发升层」,**没有运行期反向边**——这是与 deopt 的根本区别(deopt 是从 gibbous 反向回 interp 的运行期边)。

### 1.5 不会被混淆的边角:解释器作为「fallback 着陆点」也是「deopt 着陆点」

[roadmap §5 原则 1](../roadmap.md):**「解释器永不退役——它是所有编译层的 deopt 着陆点和语义 oracle」**。crescent 是**多角色着陆点**——P2/P3 阶段的 fallback、P4 阶段的 deopt **都落到同一个 crescent**。但「**着陆**」的语义不同:

- **fallback 落到 crescent**:Proto 自始至终在 crescent 解释,**没有「先在 gibbous 跑,再回 crescent」**——是「从未离开」的状态。
- **deopt 落到 crescent**(P4):Proto **先**在 gibbous 跑了一段 → guard 失败 → **退回** crescent —— 是「离开后重返」的状态,需要保留 PC/locals/CallInfo 让 crescent 能在 deopt 点续跑(P4 OSR exit 机器)。

**P2 没有 OSR exit、没有 deopt 着陆机器、没有 PC/locals 翻译**——它只有「永远没离开」的 crescent。这就是 [00-overview §1](./00-overview.md) 「P2 自己不在执行热路径」的工程意义之一:**P2 不操心着陆问题,因为它从未离开**。

---

## 2. TierState 状态机定稿

### 2.1 完整 Go 枚举

```go
// internal/bridge —— 升降层状态机(挂 ProfileData.tierState,见 [01-profiling] §2.2)
//
// 三态枚举:
//   - TierInterp   起点;所有 Proto 起步;升层未发生
//   - TierGibbous  升层成功的吸收态;Proto 已编译,P3 trampoline 接管
//   - TierStuck    永久解释的吸收态;不可编译 / 编译失败 / 后端不支持
//
// **不变式**:状态机是单向 + 吸收态,无 TierGibbous→TierInterp / TierStuck→TierInterp
// 反向边——即「零 deopt」的形式化表达(§8 详)。
type TierState uint8

const (
    TierInterp TierState = iota // 0:解释执行中(默认起点)
    TierGibbous                 // 1:已升 gibbous(P3/P4 共享落点)
    TierStuck                   // 2:永久解释(不可编译 / 编译失败)
)

func (t TierState) String() string {
    switch t {
    case TierInterp:
        return "interp"
    case TierGibbous:
        return "gibbous"
    case TierStuck:
        return "stuck"
    default:
        return "unknown"
    }
}
```

要点:

- **`TierInterp = 0`** 是 Go 零值,与 `ProfileData` 零初始化一致——首次 onBackEdge/onEnter 命中 Proto 时自动得到 `TierInterp`,无需显式设置。这是 [01-profiling §6.5](./01-profiling.md) `profileTable` 惰性建表的延伸:`pd := &ProfileData{}` 即等价于 `pd.tierState == TierInterp`。
- **三态名字直白对应分层语义**:`Interp` = crescent 解释、`Gibbous` = P3/P4 编译落点、`Stuck` = 永久卡在解释。**与执行层月相术语对齐**(00 §1 末尾警告:不会有 `bridge` 层,P2 是基建)。
- **不引入 `TierUnknown`**:[03 §5.1](./03-compilability-analysis.md) 已有 `CompUnknown` 处理「未分析」语义;状态机这边不重复——P2 启动时所有 Proto 的 `tierState` 都是 `TierInterp`(零值即起点),与 `Compilable` 字段的 `CompUnknown`(未分析)是正交语义。

### 2.2 状态转移 ASCII 图

```
                 ┌────────────────────────────────────┐
                 │                                    │
                 │     升层成功(§3.4 + §4.1):       │
                 │     可编译 + try-compile 成功      │
                 │     installGibbous + 写 bit50      │
                 │     log: promoted to gibbous       │
                 ▼                                    │
        ┌──────────────────┐                  ┌─────────────┐
        │   TierInterp     │                  │ TierGibbous │ ← 吸收态
        │ (起点 / 默认零值)  │                  │             │
        │                  │                  │ 走 P3 trampoline
        │ 走 crescent 解释  │                  │ (P3 §5)
        └──────────────────┘                  └─────────────┘
                 │                                    ▲
                 │                                    │
                 │  不可编译(§3.3)                     │
                 │  OR try-compile 失败(§5)           │
                 │  log: stays interpreted (not       │
                 │       compilable: F<n>) | failed   │
                 │                                    │
                 ▼                                    │
        ┌──────────────────┐                          │
        │   TierStuck      │ ← 吸收态                  │
        │                  │                          │
        │ 永久 crescent 解释 │                          │
        │ 不再触发 considerPromotion                    │
        └──────────────────┘                          │
                 │                                    │
                 └────无任何反向边(§8 形式化论证)──────┘
```

**两条出边、零回边、两个吸收态**——这就是状态机的几何形状。下文逐项论证。

### 2.3 转移条件表

| # | 起态 | 终态 | 触发条件 | 处理动作 | 日志 |
|---|---|---|---|---|---|
| T1 | `TierInterp` | `TierGibbous` | 热度越阈值(01)+ `CompCompilable`(03)+ `P3.Compile()` 返回 nil err | `installGibbous(proto, code)`(写 bit50)+ 设 `tierState = TierGibbous` | `function <name> promoted to gibbous` |
| T2 | `TierInterp` | `TierStuck` | 热度越阈值 + `CompNotCompilable`(03)| 设 `tierState = TierStuck` | `function <name> stays interpreted (not compilable: <reason>)` |
| T3 | `TierInterp` | `TierStuck` | 热度越阈值 + `CompCompilable` 但 `P3.Compile()` 返回 err | 设 `tierState = TierStuck` | `function <name> compile failed, stays interpreted: <err>` |
| T4 | `TierGibbous` | — | (无任何转移) | — | — |
| T5 | `TierStuck` | — | (无任何转移) | — | — |

**T1/T2/T3 是仅有的三条转移**;T4/T5 表示两个吸收态无出边。

### 2.4 无 `Gibbous → Interp` 边的形式化论证

**断言**:状态机不存在 `TierGibbous → TierInterp` 边——一旦升 Gibbous,Proto **永久**走 P3 编译码,**没有任何运行期事件**能让它回到 crescent 解释。

证明(承 §1.3 三层论证):

1. **不存在「投机假设失败」事件**:P3 编译的 gibbous 代码不带投机 guard(P3 §3,03 §1 静态保证),所以没有「guard 失败→OSR exit」的运行期事件类。
2. **不存在「资源耗尽要回退」事件**:P3 编译产物住在 wazero module(P3 §2),与 crescent 共享 linear memory(P3 §4),不会出现「内存不够」「指令缓存不够」之类需要回退的资源事件——这是 P2/P3 借助 wazero 成熟基建的红利([roadmap §4 P3 战略价值](../roadmap.md))。
3. **不存在「跨版本兼容」事件**:本文 §7.4 论证 P3 后端跨版本升级(支持新 opcode)的 stuck 重评估留 P2+,在单次运行内**没有**「P3 后端能力变化」事件——所以单次运行内 Gibbous 永远是 Gibbous。
4. **CallInfo bit50 写入的不可逆性**(§4.4):`installGibbous` 把 bit50 置 1 后,P3 trampoline 据此判该 Proto 走 gibbous 路径(P3 §5)。**bit50 不会被反向清零**——P2 没有任何路径写它回 0。

**结论**:`TierGibbous` 是几何形状上的吸收态,也是物理上的不可逆态——**单次运行内不存在让它回 `TierInterp` 的机制**。

### 2.5 无 `Stuck → Interp` 边、无 `Stuck → Gibbous` 边的形式化论证

**断言**:`TierStuck` 一旦标记,无任何转移——既不回 `Interp`(没必要,本来就解释),也不去 `Gibbous`(`Stuck` 的语义就是「不能升」)。

证明(详 §7 不重试纪律):

1. **回 `Interp` 无意义**:`Stuck` 自身的物理含义就是「解释执行」(走 crescent),与 `Interp` 行为等价。但**保留区分**有诊断价值——`Stuck` 表示「曾经评估过升层,失败」,`Interp` 表示「冷或还没评估」。日志/调试工具能据此区分两类「在解释器跑」的 Proto(§7.3)。
2. **去 `Gibbous` 在单次运行内不可能**:P3 后端能力在单次运行内不变(§7.4),编译失败原因在单次运行内不消失;`compileTried` 在转 `Stuck` 时已置 true,`considerPromotion` 入口的守卫直接 no-op(§3.2)。
3. **不冲突 `compileTried` 字段**:`Stuck` 与 `compileTried=true` 同步设置——任何 `Stuck` 状态都意味着「编译曾被尝试过(可能没尝试,if `CompNotCompilable` 直接 Stuck)」或「不需要尝试(已知不可编)」。两种情况下都不重试。

**结论**:`TierStuck` 是单次运行内的最终态,无后续转移。

### 2.6 状态机是有限确定自动机(DFA)

形式化:状态机 M = (Q, Σ, δ, q₀, F),其中:

- Q = {TierInterp, TierGibbous, TierStuck}(状态集)
- Σ = {hot+compilable+success, hot+notCompilable, hot+compileFail, gibbous-running}(输入字母表,即触发事件)
- δ:
  - δ(TierInterp, hot+compilable+success) = TierGibbous
  - δ(TierInterp, hot+notCompilable) = TierStuck
  - δ(TierInterp, hot+compileFail) = TierStuck
  - δ(TierGibbous, gibbous-running) = TierGibbous(自环,gibbous 跑着不离开)
  - δ(TierStuck, *) = TierStuck(任何事件不动)
- q₀ = TierInterp
- F = {TierGibbous, TierStuck}(终态/吸收态集)

性质:

- **无环**(忽略自环):没有 q1, q2 满足 q1 → q2 → q1。
- **吸收态**:F 中状态无出边到 Q\F。
- **可达性**:从 q₀ 可达 F 全部状态(任一 Proto 在足够长运行内都会到达 `Gibbous` 或 `Stuck`,前提是它会越阈值;否则永远停留 `Interp`,这也是合法终止)。

**这个 DFA 干净到可以人工证明 P2/P3 的语义不变量**——任何 Proto 的执行轨迹,要么始终 crescent(从未越阈值或越阈值后判 Stuck),要么从某点起永远 gibbous。**没有「半 gibbous 半 crescent」的混合状态**(那是 P4 的事)。

---

## 3. 升层决策入口 considerPromotion

### 3.1 调用契约(承 [01-profiling] §0.2 / §4.3)

`considerPromotion` 是 P2 状态机的**唯一入口**——onBackEdge / onEnter 越阈值时调用本函数,内部完成「检查可编译性 → try-compile → 安装 gibbous」全套流程。

调用契约(承 [01-profiling §4.3](./01-profiling.md)):

1. **幂等**:多次调用不出错——本函数自身用 `pd.tierState != TierInterp` 守卫(§3.2),第二次进入直接 no-op。
2. **不重载 frame**:本函数是 P2 内部决策机,**不动当前 frame 的 stk/k/ic**——onBackEdge/onEnter 调用方无需 reloadFrame。
3. **不在最热路径**:即便慢(数百 µs:AnalyzeProto 一次 + P3 Compile 一次),也只在阈值临界点发生——摊薄到每回边几十 ns(实测后调阈值)。
4. **无返回值**:升层成功/失败/不可编译都通过修改 `pd.tierState` 表达,onBackEdge/onEnter 调用方不读返回值。

### 3.2 完整 Go 代码骨架

```go
// internal/bridge —— 升层决策入口(本文 §3,P2 状态机的唯一进入路径)
//
// 调用方:
//   - bridge.onBackEdge(...)  在回边阈值越过时调(01 §4.1)
//   - bridge.onEnter(...)     在入口阈值越过时调(01 §4.2)
//
// 处理路径(四条):
//   (P1) 已经在吸收态(TierGibbous / TierStuck) → 直接 return(防抖)
//   (P2) 可编译性未分析(CompUnknown,P1 占位) → 视同不可编译,转 Stuck(03 §5.5)
//   (P3) 不可编译(CompNotCompilable,F1-F7) → 转 Stuck,记日志,return
//   (P4) 可编译 → try-compile;成功转 Gibbous,失败转 Stuck
func (b *Bridge) considerPromotion(proto *bytecode.Proto, pd *ProfileData) {
    // ┌─ P1 路径:已转吸收态 ─────────────────────────────────────
    // 守卫:已升 Gibbous / 已卡 Stuck → 直接 no-op(§7 防抖纪律)
    // 这一行也保证了 considerPromotion 的幂等性(§3.1 契约 1)
    if pd.tierState != TierInterp {
        return
    }

    // ┌─ P2/P3 路径:可编译性查询 ───────────────────────────────
    // 03 §5.5 占位约定:P1-only build 下 Compilable=CompUnknown,
    // 视同 CompNotCompilable(再保守一层),直接转 Stuck
    comp := b.CompilabilityOf(proto) // 03 §5.3 入口
    if comp != CompCompilable {
        // 既包含 CompNotCompilable(显式不可编)
        // 也包含 CompUnknown(P1 占位 / 03 还没分析)
        pd.tierState = TierStuck
        pd.compileTried = true // §7.2 字段:防御性记账
        b.diag.LogStuck(proto, pd, comp)
        return
    }

    // ┌─ P4 路径:try-compile ────────────────────────────────
    // 取 IC 反馈(02 §6.4 入口),P3 可选消费(02 §0)
    fb := b.aggregator.Aggregate(proto)
    pd.feedback = fb // CAS 安装,02 §5.5 installFeedback 内部处理并发
    pd.compileTried = true

    // 调 P3 编译(§4.1 try-compile 协议;05 §1 是 Compile 接口单一事实源)
    code, err := b.tryCompile(proto, fb)
    if err != nil {
        pd.tierState = TierStuck // 编译失败 → fallback 永久解释(§5)
        b.diag.LogCompileFail(proto, pd, err)
        return
    }

    // ┌─ 升层成功:安装 gibbous + 写 CallInfo bit50 ─────────────
    b.installGibbous(proto, code) // §4.4 写 P3 trampoline 入口标记
    pd.tierState = TierGibbous
    b.diag.LogPromoted(proto, pd) // §6 升层日志
}
```

> **addendum 2026-07-02(承 `internal/bridge/bridge.go` 与 P2 07 §2.4 / issue #18 / P4 08 §3.7 现状)**:骨架落地后 `considerPromotion` 已加入三处 non-trivial 演进,与本 §2 状态机语义 **完全兼容**——**没有引入新状态**,新增机制均是**在 TierInterp 上的延迟转移**或**入口守卫**:
>
> 1. **`onMain bool` 参数**:签名变为 `considerPromotion(proto, pd, onMain bool)`。协程线程(`onMain == false`)在函数首行直接 `return`——协程从不升层(承 [../p3-wasm-tier/07-coroutine-thread-rule.md](../p3-wasm-tier/07-coroutine-thread-rule.md)),profile 数据仍照常累积,只是决策入口 no-op;状态机看:TierInterp self-loop,不写 tierState。
> 2. **`recheckCompilabilityRuntime` 占位撤位**(issue #18,承 memory `project_p4_placeholder_reason_pattern.md`):`comp != CompCompilable` 分支不再直接 → Stuck;若 `Reason` 属于「backend 注入后需要运行期重判」的占位类别(`ReasonBackendUnsupp | ReasonSelfCall`,后端注入前保守拒),先调 `recheckCompilabilityRuntime(proto)` 对当前 P3/P4 后端的 `SupportsAllOpcodes` 再问一遍——重判为 `CompCompilable` 就走 P4 路径,否则才转 Stuck;状态机看:相当于把 T2 从「入口即判」变为「入口重判后再判」,不新增转移边。
> 3. **`forceAll` retry window**(承 P4 08 §3.7 与 fuzz_p4_test.go):force-all 模式下,IC-gated 后端(P4 native `NodeHit` 等)需要**先跑几遍解释器让 IC 预热**再升,否则冷 IC 直接升层的 proto 立刻吸收为 P4Stuck;实装为 `if b.forceAll && pd.EntryCount < 4 { return }`——EntryCount<4 时 TierInterp 自循环(**不写 tierState**),第 4+ 次入口才继续原路径;状态机看:仍是 TierInterp self-loop 的**延迟转移**,不引入新状态。
>
> **共同性质**:这三处演进都在**入口守卫层**或**T2 分支内**扩展,**从不引入 Gibbous→Interp 或 Stuck→\* 的反向边**——状态机的单向 + 吸收态承诺(§2.4/§2.5)与零 deopt 论证(§8)不受影响。任何后续在 `considerPromotion` 加入的机制,须继承本原则:「新机制 = 入口守卫 / T1|T2|T3 分支内条件细化」,不动状态图。

骨架要点(逐条对应 §2.3 转移条件表):

- **P1 守卫覆盖三种重入**:
  - 同一 onBackEdge 调用窗口内 onEnter 也越阈值,两次都进 considerPromotion → 第二次被守卫 no-op;
  - 多 State 并发(虽然 profileTable 挂 State 私有,01 §6.3,但 Proto 的 `pd` 是 State 私有所以无并发,守卫主要防同 State 内重入);
  - TierStuck 后 onBackEdge 守卫(01 §4.1)仍兜底拦下,本函数的守卫是**双重防抖**。
- **P2/P3 路径为什么不区分 `CompUnknown` 与 `CompNotCompilable`**:[03 §5.5](./03-compilability-analysis.md) 占位约定「`CompUnknown` 视同 `CompNotCompilable`」,P1-only build 下所有 Proto 都是 `CompUnknown`(没调过 `AnalyzeProto`),走 Stuck 是正确行为(P2 不工作时全部 Proto 永久解释,与 P1 行为等价)。
- **P4 路径的 feedback 聚合时机**:[02 §4.5](./02-ic-feedback.md) 「P2 初版只聚合一次(首次升层时),不重复聚合」——这一行 `b.aggregator.Aggregate(proto)` 就是「首次升层」时刻。
- **failure path 与 success path 的状态写顺序**:**先 try-compile 再写状态**——若 P3.Compile 内部 panic,recover 见 §5.2;tierState 还在 `TierInterp`,但 `compileTried=true`,需要 §7.2 的字段同步。

### 3.3 不可编译路径的处理(承 [03] §5.4 + 状态机 T2)

`CompilabilityOf(proto)` 返回 `CompNotCompilable` 路径(状态机 T2):

- **来源**:[03 §3](./03-compilability-analysis.md) 的 F1-F7 任一形状触发——vararg / 协程 / debug / setfenv / 过大函数 / 深嵌套闭包 / P3 后端不支持。
- **处理**:转 `TierStuck`,记 `compileTried = true`(防御性,虽然没真正 try-compile,但语义上「评估过」),记日志(§6.2)。
- **不调 P3**:这是「静态 fallback」(§1.4)——根本不触发 try-compile,P3 不被打扰。

代码层面,§3.2 的 P2/P3 路径分支已覆盖。

### 3.4 可编译路径的处理(状态机 T1 / T3)

`CompilabilityOf(proto)` 返回 `CompCompilable` 路径,进入 try-compile:

- **聚合 IC 反馈**:`b.aggregator.Aggregate(proto)`([02 §6.4](./02-ic-feedback.md))。这是 P2 首次也是唯一一次为该 Proto 聚合 feedback(02 §4.5 一次性策略)。
- **CAS 安装 feedback**:`pd.feedback = fb`(实际由 `installFeedback` 内部 CAS,02 §5.5)——即便多 State 并发也只有一份 feedback 胜出,后到的丢弃。
- **调 P3 编译**:`b.tryCompile(proto, fb)` 详见 §4.1 协议。
- **结果分支**:
  - err == nil → installGibbous(§4.4)+ tierState=TierGibbous + log promoted(§6.1)→ 状态机 T1
  - err != nil → tierState=TierStuck + log compile failed(§6.3)→ 状态机 T3

### 3.5 并发安全(对 ProfileData.tierState 的访问)

承 [01-profiling §6.3](./01-profiling.md) 的 (B) 方案:**profile 计数挂 State 私有 profileTable**——`ProfileData` 是 State 私有的,**单 State 内无并发写**。所以:

- `pd.tierState` 的读写**不需要 atomic / mutex**,普通读写即可。
- `pd.compileTried` 同。
- `pd.feedback` 是 `*TypeFeedback` 指针——多 State 共享 Proto 时,**Proto 旁的 feedback 字段**(02 §4.5)需要 CAS;但本函数操作的是 State 私有 `pd.feedback` 槽位,**不与 Proto 旁 feedback 冲突**。

> **多 State 共享 Proto 的 feedback 写入**:不是本节的事——[02 §5.5](./02-ic-feedback.md) `installFeedback` 已用 atomic.CompareAndSwapPointer 处理 Proto 旁 feedback 字段的并发。本文 considerPromotion 只关心 State 私有的 ProfileData 字段。

但有一个**设计上的边角**值得记:若 (C) 方案启用([01 §6.4](./01-profiling.md) 双表混合,sync.Pool 短生命期 State 形态),全局聚合表的 `tierState` 与 State 私有的 `pd.tierState` 是否需要联动?**当前 (B) 方案下不存在此问题**;(C) 启用时另议。本文记 §11 缺口。

### 3.6 错误传播:considerPromotion 永不抛 panic / err

承 §3.1 契约 4「无返回值」:本函数对调用方(onBackEdge/onEnter)永不抛 panic 或返回 error。所有内部错误都被吸收进 `tierState = TierStuck` + 日志。具体:

- **`b.tryCompile` 返回 err**:转 Stuck + 日志,正常 return。
- **`b.tryCompile` 内部 panic**:用 defer recover 捕获,转 Stuck + 日志,正常 return(§5.2 详)。
- **`b.aggregator.Aggregate(proto)` 异常**:[02 §6.4](./02-ic-feedback.md) 的实现保证不 panic(纯函数,只读 ICSlot);但若实测发现可能 panic,在本函数加 recover 兜底(目前不预设)。
- **`b.installGibbous` 失败**:这一调用本身不会失败(只是写 bit50 + 注册 gibbous code,§4.4),若失败说明实现 bug,应 panic(让上层 logger 看见)——而非吞掉。

**不变式**:`considerPromotion` 对 onBackEdge/onEnter **永远不抛错** —— 解释器主循环的稳定性优先于 P2 决策的成功率(P2 失败一个 Proto 只是该 Proto 走解释,无系统风险)。

---

## 4. try-compile 协议:P2 怎么调 P3

### 4.1 P3Compiler.Compile 接口签名(承 [05-p3-p4-interface] §1)

`P3Compiler` 是 P2 与 P3 之间的接口契约,**单一事实源是 [05-p3-p4-interface](./05-p3-p4-interface.md)**——本文只引用其形状供 §3 considerPromotion 调用:

```go
// 引用 [05-p3-p4-interface] §1(实际定义在 internal/bridge 暴露,
// internal/gibbous/wasm 实现)
type P3Compiler interface {
    // SupportsAllOpcodes:F7 后端能力查询(03 §3.7)
    // 在 Compile 前由 [03] AnalyzeProto 调用,本函数不重复调
    SupportsAllOpcodes(proto *bytecode.Proto) bool

    // Compile:把一个 Proto 编译成 GibbousCode
    //   - proto: 已通过 [03] F1-F7 全部判定的 Proto;
    //   - feedback: 02 聚合的 TypeFeedback;P3 可选消费(锦上添花的紧凑翻译,
    //     非投机依赖,02 §0)。fb 为 nil 时 P3 走通用翻译(不读 IC 反馈)。
    //   - 返回 (*GibbousCode, error):成功返回 gibbous 代码安装句柄;
    //     失败返回 err(本文 §5 错误分类)。
    //
    // 实现纪律:
    //   - Compile 是同步阻塞调用(P2 PB0 不做异步编译);
    //   - 内部不修改 proto / feedback(P2 视角 P3 是消费方);
    //   - 失败时清理任何中间产物,不污染下次调用。
    Compile(proto *bytecode.Proto, feedback *TypeFeedback) (*GibbousCode, error)
}
```

> **接口所属**:接口定义住 `internal/bridge`(P2 包),实现住 `internal/gibbous/wasm`(P3 包);P2 在 `Bridge` 结构体里持有 `p3 P3Compiler` 字段,启动时由 `wangshu.go` 公共 API 注入(P3 落地后)。P1-only build 下 `b.p3 == nil`,[03 F7](./03-compilability-analysis.md) 永远判不可编译,本函数 P4 路径不会被走到——这是 [03 §2.6](./03-compilability-analysis.md) 「P1-only fallback」的延伸。

### 4.2 GibbousCode 返回值形态(简版,详见 P3)

`GibbousCode` 是 P3 编译产物的「安装句柄」,详细定义在 [../p3-wasm-tier/04-trampoline.md](../p3-wasm-tier/04-trampoline.md) §6(待 P3 上线后定稿)。本文只声明 P2 用到的最小形态:

```go
// 引用 [../p3-wasm-tier/04-trampoline.md] §6(P3 主管定义)
type GibbousCode struct {
    proto      *bytecode.Proto      // 反向指针,trampoline 校验用
    wasmModule *wazero.CompiledModule // P3 编译产物(P3 §2)
    entryPoint uintptr               // gibbous 函数入口(P3 §5 trampoline 用)
    // 其余字段属 P3,P2 视角是不透明
}
```

P2 视角:`GibbousCode` 是不透明 token——`installGibbous(proto, code)` 把它登记到 P3 trampoline 表;P2 不解读其内部字段。

### 4.3 错误分类(P3.Compile 返回 err 的来源)

P3.Compile 返回 err 的可能原因:

| 类别 | 触发 | 处理 | 日志 |
|---|---|---|---|
| **F7 边角漏判** | [03 §3.7](./03-compilability-analysis.md) 的 SupportsAllOpcodes 没识别出某 opcode 的某子情况(如 GETTABLE 的 key 是某种特殊形态);[03] 整体判 Compilable 但 P3 实际编不了 | 转 Stuck;视作正常的 try-compile 失败 | `compile failed: unsupported opcode shape: ...` |
| **资源耗尽** | wazero module 实例化失败(内存不足 / module table 满 / linear memory 上限);P3 的内部状态分配失败 | 转 Stuck;**这一项理论上可重试**(资源恢复后),但 P2 不区分(§7.1 论证不重试) | `compile failed: out of resources: ...` |
| **后端 panic recover** | P3 编译器内部 panic(实现 bug 或边角形态);recover 在 `b.tryCompile` 内捕获(§5.2) | 转 Stuck;视作 fatal-but-non-fatal 错误(单 Proto 失败,不影响系统) | `compile failed: backend panic recovered: ...` |
| **P3 主动拒绝** | P3 决定不编译(如启发式判该 Proto 收益不够);P2 PB0 不预期此类返回(P3 应在 SupportsAllOpcodes 阶段拒) | 转 Stuck | `compile failed: backend declined: ...` |

**分类的实用价值**:**让升层日志能区分「F7 漏判」(03 的 bug)与「资源耗尽」(运行期临时态)与「后端 panic」(P3 的 bug)**。诊断工具据此分流告警(F7 漏判应记 issue 修 [03];资源耗尽不告警;后端 panic 应记 issue 修 P3)。详见 §6.3 日志格式。

### 4.4 installGibbous 安装协议(写 CallInfo bit50)

`installGibbous` 是状态转移 T1 成功路径的最后一步——把 `GibbousCode` 注册到 P3 trampoline,**写 CallInfo bit50 标志位**让 P1 解释器在下次进入该 Proto 时跳到 gibbous 路径。

```go
// internal/bridge —— 升层成功后安装 gibbous 代码
//
// 调用时机:considerPromotion 的 P4 路径成功(P3.Compile 返回 nil err)。
// 调用前提:
//   - pd.tierState 仍是 TierInterp(没被并发改;§3.5 单 State 无并发);
//   - code != nil 且 code.proto == proto(P3 返回值合法性,P3 §2.5 保证)。
//
// 操作三件事:
//   (1) 把 code 注册进 P3 trampoline 表(P3 §5):后续 P1 解释器进 Proto 时
//       trampoline 据此查到 wasm entryPoint 跳进 gibbous;
//   (2) 在 Proto 旁挂 GibbousCode 引用(让 GC 不回收 wasm module 直到 Program 销毁);
//   (3) **写 CallInfo bit50 callStatus_gibbous = 1**:
//       从此进 该 Proto 的 CallInfo 都标 gibbous 帧(P3 trampoline 据此判流向)。
//
// 不变式:
//   - 这三件事的写入是原子的(从 P1 解释器视角看,要么都没生效,要么都生效);
//   - 不可逆(没有 uninstallGibbous,§2.4)。
func (b *Bridge) installGibbous(proto *bytecode.Proto, code *GibbousCode) {
    // (1) 注册 trampoline 表
    b.trampoline.Register(proto, code) // P3 §5

    // (2) 挂 GibbousCode 引用(防 GC)
    b.gibbousCodes[proto] = code // 普通 map,Bridge 私有

    // (3) 写 CallInfo bit50 callStatus_gibbous
    //
    // 注意:bit50 是 CallInfo 字段,但它在 P1 解释器进入帧时
    //  (enterLuaFrame, [05] §1.4)由 trampoline 决定写 0 或 1。
    //  本文不直接改已存在的 CallInfo——bit50 的「物化」发生在「下次进入此 Proto」的 enterLuaFrame
    //  里:trampoline 查表见到 proto 已注册,就把新建的 CallInfo bit50 置 1。
    //  所以 installGibbous 这一步实质是「让下次进入时被 trampoline 拦下并标 bit50」,
    //  不是「立即改所有现存 CallInfo 的 bit50」(那会不一致;现存 CallInfo 还在
    //  跑 crescent,不应被改)。
    //
    // bit50 的写入语义详见 [../p1-interpreter/05-interpreter-loop.md] §1.2:
    //  - 字段位置: word2 [50] callStatus_gibbous(P1 恒 0)
    //  - 写入者:  P3 trampoline(进帧时,P3 §5)
    //  - 读取者:  P3 trampoline 自己(下次跨层判流向),也是 P4 trampoline 的依赖
    //  - P1 解释器主循环不读 bit50(P1 不感知 gibbous,bit50 对它透明)
}
```

要点:

- **bit50 写入是 trampoline 的责任,不是 considerPromotion 的责任**:本函数只把 Proto 注册进 trampoline 表;实际的 bit50 写入发生在「下次该 Proto 被作为 Lua 帧进入」时(由 trampoline 拦截 enterLuaFrame)。
- **不动现存 CallInfo**:Proto 在升层瞬间可能正被某个 State 解释跑(其 CallInfo bit50=0),不应改这一帧的 bit50——那会让 P1 解释器在执行中突然变成 gibbous,违反「同一帧执行体」的语义。**新 CallInfo 的新进帧才标 bit50=1**,旧 CallInfo 直到 RETURN 自然消亡。
- **三件事的原子性**:从「Proto 还没升 gibbous」到「Proto 已升 gibbous」的瞬间,P1 解释器视角能看到两种状态:① trampoline 表没该 Proto + bit50 不写;② trampoline 表有该 Proto + bit50 写 1。**没有「半升半不升」的中间态可被读到**——因为 P2 单 State 内同步调用,没有并发竞争(§3.5)。

### 4.5 多 State 共享 Proto 的 installGibbous 协调

[01-profiling §6](./01-profiling.md) 的 (B) 方案下,profile 挂 State 私有,**每个 State 独立判 Proto 是否热**——所以可能多个 State 同时 considerPromotion 同一个 Proto。

并发协调:

| 候选 | 协调方式 | 优劣 |
|---|---|---|
| (A) Bridge 持锁 | considerPromotion 入口加 `b.compileMu.Lock()` | 简单,但 P2 出现锁——但 P2 不在热路径,锁不是性能问题;此为本文定稿 |
| (B) trampoline 表 CAS | `b.trampoline.Register` 内部 CAS,失败则丢弃本次 code | 无锁;但需要 P3 trampoline 实现配合 |
| (C) 状态机层 atomic.Compare | `pd.tierState` 改成 atomic uint8,CAS Interp→Gibbous | 但 pd 是 State 私有,无并发写;此方案不需要 |

**当前定稿 (A)**:`Bridge` 持一个 `compileMu sync.Mutex` 锁住 try-compile + installGibbous 关键段:

```go
// 修改 §3.2 的 P4 路径(加锁版本)
b.compileMu.Lock()
defer b.compileMu.Unlock()

// 双重检查:加锁后再确认 Bridge 维度的 trampoline 表
if existing, ok := b.gibbousCodes[proto]; ok {
    // 别的 State 抢先编译并安装了 → 复用现有 GibbousCode,不重复编译
    pd.tierState = TierGibbous
    pd.feedback = existing.feedback // 复用 feedback
    b.diag.LogPromoted(proto, pd)
    return
}
// 走完整 try-compile 路径 ...
```

要点:

- **锁粒度是 Bridge 级**:全局一把锁,简单,但**多 Proto 不能并行编译**——这是 P2 不在热路径的合理代价(编译只发生在阈值临界点,频次低)。
- **双重检查**:加锁后先看 `gibbousCodes[proto]` 是否已有——别的 State 抢先编完就复用;避免重复编译同一个 Proto。
- **未加锁的 P1/P2/P3 路径**:守卫与可编译性查询不在 critical section(因为它们是只读 + 不动跨 State 数据)——**只有 P4 路径(try-compile + installGibbous)需要锁**。
- **未来优化路径**:若多 Proto 并行编译有真实需求,改成「per-Proto 锁」(`map[*Proto]*sync.Mutex` 或 `sync.Map`),让不同 Proto 并行编译。**当前不预设**(留 §11 缺口)。

> **(C) 方案下的复杂度**:[01 §6.4](./01-profiling.md) 的 sync.Pool 双表混合方案启用后,跨 State 累积的 ProfileData 与本 State 私有的 ProfileData 都可能触发 considerPromotion——锁机制需扩展。**当前 (B) 方案下不存在**,记 §11 缺口。

### 4.6 GibbousCode 的生命期

- **创建**:`P3.Compile` 返回时新建。
- **挂载**:`installGibbous` 注册进 `b.trampoline` + `b.gibbousCodes`。
- **使用**:P3 trampoline 在每次进入该 Proto 时查表跳到 gibbous(P3 §5)。
- **回收**:Program 销毁时,Bridge 析构清理 `gibbousCodes` map + trampoline 表 + wazero module 关闭。详见 [../p3-wasm-tier/04-trampoline.md](../p3-wasm-tier/04-trampoline.md) §6.6(待 P3 落地)。

P2 视角:GibbousCode 与 Program 同生命期——一旦升层不再卸载,直到 Program 整体释放。这与「单向 + 吸收态」的状态机一致:不存在「卸载 gibbous 回 interp」的运行期事件。

---

## 5. 编译失败的 fallback 机制

### 5.1 三种失败时机

承 §4.3 错误分类,但分时机看:

| 时机 | 触发 | 处理 | 状态影响 |
|---|---|---|---|
| (a) `Compile` 调用前 | [03 §3.7 F7](./03-compilability-analysis.md) 在 `AnalyzeProto` 阶段已判 `CompNotCompilable` | considerPromotion 的 P3 路径直接转 Stuck,不进 try-compile | tierState: Interp → Stuck;无 P3 调用 |
| (b) `Compile` 调用中(返回 err) | F7 漏判 / 资源耗尽 / P3 主动拒 | considerPromotion 的 P4 路径见 err 转 Stuck | tierState: Interp → Stuck;P3 已工作但失败 |
| (c) `Compile` 调用中(panic) | P3 内部实现 bug 触发 panic | recover 兜底 + 转 Stuck | tierState: Interp → Stuck;P3 状态可能不一致(下次调可能仍 panic) |

(a) (b) (c) 在 P2 状态机视角**等价**——都进 `TierStuck` 永久解释,不可恢复。区别只在日志(§6.3)与诊断(让用户/开发者识别 P3 的实现 bug)。

### 5.2 P3 后端 panic recover 兜底(防御性纪律)

P3 是新代码(P2 上线时 P3 大概率还在 PB1/PB2 阶段),**实现 bug 触发 panic 的概率不可忽略**——尤其在边角形态(F7 漏判类)。`b.tryCompile` 必须用 `defer recover` 兜底,把 P3 的 panic 转成 err,避免污染 P1 解释器栈。

```go
// internal/bridge —— try-compile 的 recover 兜底
//
// 设计纪律:
//   - P3 的 panic 不应让 P1 解释器主循环崩溃——P2 决策机要能吸收所有 P3 异常;
//   - panic 信息保留下来供诊断(§6.3 日志),但不向上抛;
//   - panic 的 Proto 永久 Stuck,单次运行内不再尝试(§7);
//   - **P3 的内部状态可能因 panic 部分污染**,后续不应复用——
//     但 P2 PB0 信任 P3 自行清理(P3 §X 的纪律,不本文管);
//     若实测发现 P3 panic 后状态不一致,在此函数额外 reset P3。
func (b *Bridge) tryCompile(proto *bytecode.Proto, fb *TypeFeedback) (code *GibbousCode, err error) {
    defer func() {
        if r := recover(); r != nil {
            // P3 panic → 兜底转 err,considerPromotion 见到后转 Stuck
            err = &CompileError{
                Kind:   CompileErrPanic,
                Proto:  proto,
                Reason: fmt.Sprintf("P3 backend panic: %v\n%s", r, debug.Stack()),
            }
            code = nil
            // 记一条诊断级日志(独立于升层日志,§6 升层日志只说「失败」一行,
            // 这里把完整 stack 留在诊断日志供 P3 实现者调试)
            b.diag.LogPanic(proto, r)
        }
    }()
    return b.p3.Compile(proto, fb)
}
```

要点:

- **recover 范围限于 `b.p3.Compile` 调用**:`tryCompile` 是 thin wrapper,内部只调一次 P3——recover 不会误抓上层(considerPromotion / aggregator)的 panic。
- **err 类型用 `*CompileError`**:让诊断日志能区分 panic 类与正常 err 类(§6.3 表)。
- **stack trace 入诊断日志**:`runtime/debug.Stack()` 把 P3 panic 时的 Go stack 留下,供 P3 实现者修 bug。**升层日志只说一行(§6.1)**,完整 stack 走独立诊断 channel。
- **不重新 panic**:`considerPromotion` 永不抛 panic 给 onBackEdge/onEnter(§3.6 不变式)。

### 5.3 在 installGibbous 之前 vs 之后的处理

§5.1 的失败时机均发生在 `installGibbous` **之前**(因为 installGibbous 是「Compile 成功后才调」)。**installGibbous 之后不会失败**:

- installGibbous 内部三步(注册 trampoline / 挂 gibbousCodes / 标 bit50 物化机制)都是 in-memory 操作,无外部资源调用,不应失败。
- 若 installGibbous 实现里某步真的失败(如 map 写入因内存耗尽),这是 fatal 系统级错误(不是 P3 编译失败级别的事件)——应直接 panic / propagate(§3.6 「installGibbous 不会失败」)。

对比 (a)(b)(c):

```
                 try-compile 路径
                       │
    ┌──────────────────┼───────────────────────┐
    ▼                  ▼                       ▼
   (a)                (b)                     (c)
   pre-Compile         Compile 返回 err          Compile panic
   (03 已判 NotComp)   (F7 漏判 / OOM / decline) (P3 实现 bug)
    │                  │                       │
    ▼                  ▼                       ▼
   不调 P3             调 P3 但失败              调 P3 但 panic
    │                  │                       │
    ▼                  ▼                       ▼
   tierState=Stuck     tierState=Stuck         recover + tierState=Stuck
   log: not compilable log: compile failed     log: backend panic
                       │
                       │   installGibbous 之前 → 全部失败都进 Stuck
                       │
        ───────────────┴──────────────────
                       │
    installGibbous 之后  │  状态已是 Gibbous
                       │  installGibbous 内部不应失败(in-memory only)
                       │  若失败 → fatal panic(系统级,P2 不吸收)
                       ▼
                  TierGibbous(终态)
```

### 5.4 fallback 的可观察性

承 §1.2 对比表「fallback 可观察性 = 编译失败日志一次」:

- **静态 fallback (a)**:有 `LogStuck` 日志 + reason 为 [03] 形状(F1-F7 哪一个,§6.2)。
- **运行期 fallback (b)(c)**:有 `LogCompileFail` 日志 + err 详情(§6.3)。
- 两者**都不写 metrics**(P2 PB0 不引入 metrics 子系统)——日志足够。

诊断接口形态详见 §6.4。

---

## 6. 升层日志格式

承 [evolution-roadmap](../../llmdoc/architecture/evolution-roadmap.md) / [../roadmap.md](../roadmap.md) §4:**「日志/诊断输出形如 `function promoted to gibbous`,比裸 tier 编号自释」**。本节给 P2 的精确日志格式 + 诊断接口设计。

### 6.1 升层成功日志(T1 转移)

格式:

```
function <name> promoted to gibbous (entry=<E>, backedge=<B>, feedback=<F>)
```

字段:
- `<name>`:`proto.Name`(若有,否则 `proto.Source:<line>`)
- `<E>`:升层时的 `pd.entryCount`
- `<B>`:升层时的 `max(pd.backEdge[])`
- `<F>`:feedback 简要(如 `arith=2 mono=5 mega=0`,或 `nil` 表示未消费)

日志级别:**INFO**(每次升层一条,频次低,不会刷屏)。

> **关键纪律**:**日志说「promoted to gibbous」,不说「promoted to bridge」**(00 §1 末尾)——bridge 不是执行层、不是升层落点。P2 触发的升层落点是 **gibbous**(P3 的 Wasm 层),日志反映这个实质。

样例:

```
function dot promoted to gibbous (entry=200, backedge=1000, feedback=arith=2 mono=1 mega=0)
function score promoted to gibbous (entry=1024, backedge=0, feedback=nil)
```

### 6.2 不可编译日志(T2 转移)

格式:

```
function <name> stays interpreted (not compilable: F<n> <reason>)
```

字段:
- `<n>`:F1-F7 的编号(03 §3 形状清单)
- `<reason>`:简要原因(如 vararg / yield / debug / setfenv / oversize / nestedDeep / backendUnsupp)

日志级别:**INFO**(冷 + 不可编译的 Proto 罕见进入 considerPromotion——只有热的才进入,所以这条日志同样频次低)。

样例:

```
function main stays interpreted (not compilable: F1 vararg)
function processBatch stays interpreted (not compilable: F2 callsCoroutine)
function bigInit stays interpreted (not compilable: F5 oversize: 2412 insns)
```

> **F<n> 编号能直接映射回 [03] 文档**——开发者看到日志后能立即查 [03 §3 F<n>] 的形状定义,自释性满足设计要求。

### 6.3 编译失败日志(T3 转移)

格式:

```
function <name> compile failed, stays interpreted: <err>
```

字段:
- `<err>`:`CompileError.Reason` 的内容,带 err Kind 前缀(`unsupported_opcode_shape:` / `out_of_resources:` / `backend_panic:` / `backend_declined:`)

日志级别:**WARN**——编译失败比静态 fallback 更稀有,且暗示实现层面的问题(F7 漏判 / 资源问题 / P3 bug),值得告警。

样例:

```
WARN function compute compile failed, stays interpreted: unsupported_opcode_shape: GETTABLE with non-string key constant
WARN function render compile failed, stays interpreted: backend_panic: nil pointer dereference at gibbous/wasm/codegen.go:142
```

panic 类的完整 stack trace 不进入主日志(避免刷屏),由 `b.diag.LogPanic` 写到独立诊断 channel(§5.2)。

### 6.4 诊断接口设计(`diag.Logger`)

```go
// internal/diag —— P2 诊断日志接口(P3/P4 可复用)
//
// 设计纪律:
//   - 接口最小化,P2 只用四个方法;
//   - 默认实现走 stdlib log,可注入自定义实现(如发到 metrics / structured log);
//   - 接口稳定,不为 P3/P4 加新方法(若需要,新接口 + 嵌入 Logger,不破本接口)。
type Logger interface {
    LogPromoted(proto *bytecode.Proto, pd *ProfileData)
    LogStuck(proto *bytecode.Proto, pd *ProfileData, comp Compilability)
    LogCompileFail(proto *bytecode.Proto, pd *ProfileData, err error)
    LogPanic(proto *bytecode.Proto, panicValue interface{})
}

// 默认实现(P2 PB0 用)
type stdLogger struct{ out *log.Logger }

func (l *stdLogger) LogPromoted(proto *bytecode.Proto, pd *ProfileData) {
    l.out.Printf("function %s promoted to gibbous (entry=%d, backedge=%d, feedback=%s)",
        protoName(proto), pd.entryCount, maxBackEdge(pd), feedbackSummary(pd.feedback))
}
func (l *stdLogger) LogStuck(proto *bytecode.Proto, pd *ProfileData, comp Compilability) {
    reason := "unknown"
    if comp == CompNotCompilable {
        reason = formatF1ToF7Reasons(pd.Reasons) // 03 §5.3 reasonsBitmap
    } else if comp == CompUnknown {
        reason = "F0 not analyzed (P1 occlusion)" // 03 §5.5 P1 占位
    }
    l.out.Printf("function %s stays interpreted (not compilable: %s)",
        protoName(proto), reason)
}
func (l *stdLogger) LogCompileFail(proto *bytecode.Proto, pd *ProfileData, err error) {
    l.out.Printf("WARN function %s compile failed, stays interpreted: %v",
        protoName(proto), err)
}
func (l *stdLogger) LogPanic(proto *bytecode.Proto, panicValue interface{}) {
    l.out.Printf("ERROR function %s P3 backend panic: %v\n%s",
        protoName(proto), panicValue, debug.Stack())
}
```

要点:

- **接口稳定承诺**:这四个方法在 P2 上线后**不修改签名**——P3/P4 只能新增接口,不能改本接口(原则 3「每阶段独立交付」)。
- **stdLogger 是 P2 PB0 唯一实装**:写 stderr 即可。生产环境的 structured log / metrics 让宿主代码注入自定义 Logger 实现。
- **`protoName` helper**:P2 共用——对没有 Name 的 anonymous Proto,降级用 `<source>:<line>`(承 [01 §5.7](../p1-interpreter/01-value-object-model.md) Proto 字段)。
- **`formatF1ToF7Reasons` helper**:把 [03 §5.1](./03-compilability-analysis.md) `reasonsBitmap` 翻成可读字符串(`F1 vararg, F5 oversize`)。

### 6.5 日志的差分一致性

[06-testing-strategy](./06-testing-strategy.md) §3 的可编译性误判注入 fuzz 与端到端验收都断言**特定脚本应产生特定升层日志**——这是 [00-overview](./00-overview.md) §4 PB5 的「日志格式断言」验收。

为支持断言,日志格式有三条工程纪律:

1. **格式确定**:`promoted to gibbous` / `stays interpreted` / `compile failed` 三个关键短语**不变化**——测试用字符串包含断言。
2. **顺序确定**:在确定输入下(单 State / 阈值确定),升层顺序是确定的——便于差分。
3. **可截断**:测试断言可只匹配关键短语,不强求所有字段——`function dot promoted to gibbous` 这一前缀就足够断言「此 Proto 升了」。

详见 [06-testing-strategy](./06-testing-strategy.md) §3 升层日志断言节。

### 6.6 与 P4 deopt 日志的区分

P4 上线后会引入 deopt 日志(`function <name> deopt: <reason>`),与本文升层日志**不冲突**——格式上「promoted to gibbous」与「deopt」是两个独立的事件类。但日志读者必须能据此**区分 P2/P3 阶段**(只有 promoted)与**P4 阶段**(可能反复 deopt):

- P2/P3 阶段的日志**不应**出现 `deopt` 字样——若出现,说明 P4 错误启用或 P2 状态机被破坏。
- P4 阶段的 `deopt` 日志详细规约见 [../p4-method-jit/04-osr-deopt](../p4-method-jit/04-osr-deopt.md)(P4 主管)。

本文只保证 P2/P3 阶段的日志清洁。

---

## 7. TierStuck 不重试纪律(防抖动)

§2.3 转移条件表 T2/T3 在「热度越阈值 + 不可编译 / 编译失败」时把 Proto 转 `TierStuck`,§2.5 已论证 `Stuck` 是吸收态。本节回答更具体的工程问题:**为什么单次运行内坚决不重试?**——这是 P2 防抖动的核心纪律。

### 7.1 不重试的物理依据(同 P3 后端、同 Proto = 同结果)

**断言**:在单次运行(同一 Program 实例)内,若 Proto P 在某次 considerPromotion 被判 `TierStuck`,**任何后续 considerPromotion(若不被守卫拦下)都会得到同一结果**——即仍判 `Stuck`。

证明(承 §2.4 / §2.5 的 P3 后端能力不变性):

1. **`AnalyzeProto` 是纯函数**:[03 §5.1](./03-compilability-analysis.md) 论证 `Compilability` 是 Proto 的静态属性——只读 Proto 的 instructions/Constants/SubProtos,不读运行期状态。同一 Proto 输入,同一 Compilability 输出。**单次运行内不变**。
2. **`SupportsAllOpcodes` 是纯函数(对当前 P3 二进制)**:[03 §3.7 F7](./03-compilability-analysis.md) 的 P3 后端能力查询,只依赖编译进二进制的 P3 实现——单次运行内 P3 不会自我升级,所以同一 Proto 同一答案。
3. **`P3.Compile` 在(几乎)同一输入下产出同一结果**:虽然 P3 内部可能用 RNG/资源分配(如 wasm module 句柄),但「编译成功 vs 失败」对同一 Proto 在 P2 视角是稳定的——除非真的资源耗尽这一类「环境抖动」类失败。F7 漏判 / 后端 panic / 主动拒绝(§4.3)都是 Proto 形状决定的,不会单次运行内反复变。
4. **资源耗尽类失败的特例**:理论上「资源恢复后能再编」是合法的——但 P2 PB0 不区分。下文 §7.2 论证为什么不区分仍是工程上正解。

**结论**:除资源耗尽这一极少数特例,Stuck 重试在确定性意义下不会得到不同结果——**重试就是浪费**。

### 7.2 不重试的工程价值(避免抖动浪费 + 诊断清晰)

承 §7.1 的物理依据,即便允许「资源恢复后重试」,工程上仍选择「单次运行内绝不重试」——因为不重试有三层独立的工程价值:

1. **防止「不可编译热函数」无限循环重试**:想象一个真实场景——脚本里有个用 `pcall` / `coroutine.wrap` 形态的热函数(F2 / F3 判不可编译,03 §3),它每次进入回边都越阈值。**若重试,每越一次阈值都要再走一遍 [03] AnalyzeProto + P3.Compile**——AnalyzeProto 数百 µs,P3.Compile 数百 ms 量级,被打到几百次/秒会打爆 CPU。**不重试**:`pd.tierState != TierInterp` 守卫直接 no-op,几个 ns 返回。
2. **诊断清晰**:Stuck 是个稳定标签——「这个 Proto 我们试过,失败了,不再试」。日志只在转 Stuck 那一刻打一条(`stays interpreted` / `compile failed`),后续不再刷屏。**重试方案下日志会循环刷同一行**,污染诊断信号。
3. **行为可预测**:**用户/调试者能根据「升层日志一条」预测「之后这个 Proto 永远走解释器」**,与 §2 状态机的 DFA 性质一致。重试方案下,Stuck 状态语义变成「也许永久,也许下次还试」,DFA 退化。

**与 P4 deopt 的对比**:P4 的 deopt 反复跳是另一回事(P4 §X 的 deopt-loop 检测是 P4 自己的责任),不影响 P2 的 Stuck 不重试纪律。**P2 状态机的洁净性,部分就靠「Stuck 不重试」这条铁律守住**。

### 7.3 与计数累积保留的协同(`pd.backEdge` 仍累加但 considerPromotion 入口守卫拦下)

一个常被混淆的细节:**Stuck 后,profile 计数(`pd.entryCount` / `pd.backEdge[]`)仍在累加吗?**

**答**:**仍在累加,但 considerPromotion 入口守卫拦下不进决策。**

理由:

- **P1 解释器对 ProfileData 的写入路径(`onBackEdge` / `onEnter`)与 `tierState` 解耦**:[01 §4](./01-profiling.md) 的回边采样不读 tierState——它只无条件累加 `backEdge[pc]`,然后调 `b.onBackEdge` 由 P2 决定怎么处理。**累加是 P1 的事,决策是 P2 的事**。
- **P2 的 `onBackEdge` 入口的简化路径**(承 [01 §4.1](./01-profiling.md)):

```go
// internal/bridge —— onBackEdge 越阈值时的处理(承 [01 §4.1])
func (b *Bridge) onBackEdge(proto *bytecode.Proto, pd *ProfileData, pc int) {
    pd.backEdge[pc]++

    // 阈值检查(每次都做,但 considerPromotion 入口 §3.2 P1 守卫拦下吸收态)
    if pd.backEdge[pc] >= HotBackEdgeThreshold {
        b.considerPromotion(proto, pd) // §3.2 P1 路径首先 return
    }
}
```

- **守卫拦下的代价**:`considerPromotion` 进入后第一行 `if pd.tierState != TierInterp { return }` 是 hot-but-cheap 的——L1 cache 命中、一次比较,几 ns。即便 Stuck 的 Proto 越了 100 万次阈值,累计开销也只有几 ms 量级。
- **保留计数的诊断价值**:Stuck 后计数仍在涨,**让诊断工具能区分「Stuck Proto 还在被频繁调用」(说明这个 fallback 影响真实性能)与「Stuck Proto 已冷下来」(失败但无所谓)**。这对 P2+ 的「stuck 重评估」(§7.4)与「stuck Proto 性能告警」未来工作是基础。

**对比**:不保留计数的方案需要在 onBackEdge 入口加 `if pd.tierState == TierStuck { return }` 跳过累加——能省一点写带宽,但失去诊断信号,且与 [01-profiling](./01-profiling.md) 的「累加无条件」纪律冲突。**当前定稿:累加无条件,决策入口守卫拦下**。

### 7.4 跨版本 stuck 重评估(留 P2+ 缺口)

**问题**:Proto 在 v1.0 因 F7(P3 后端不支持某 opcode)被判 Stuck,用户升级到 v1.1 后 P3 后端支持了——能不能让这个 Proto 在 v1.1 上重试?

**当前定稿(P2 PB0)**:**不在单次运行内重评估**——v1.0 → v1.1 的 P3 升级,通过**进程重启**自然触发重评估(新 Program 实例的 ProfileData 全新,Stuck 从未标记过)。

理由:

1. **物理依据**:P3 后端能力是「编译进二进制」的——同一进程内不会切换 P3 二进制。**进程边界天然是 stuck 重评估边界**。
2. **工程依据**:跨版本重评估需要把「Stuck 原因」(F1-F7 哪个 / err 详情)持久化到磁盘,在新版本启动时检查「这个原因在新版是否还成立」——这是个独立的子系统,**P2 PB0 不做**。
3. **用户体验**:大部分场景(pineapple / 短 lived 服务)进程重启频繁——v1.1 部署即重启,**自然重评估**。长 lived 服务(daemon 类)用户少,且这类场景的运维通常会主动重启。

**P2+ 缺口**(承 [00 §9](./00-overview.md) 与 §11 缺口):

- **跨版本 stuck 重评估**:若实测发现「跨版本升级后大批 Proto 卡 Stuck」是真实问题,设计「P3 版本号 + Stuck 原因序列化 + 启动时重评估」机制。当前不做。
- **运行期 P3 后端切换**:理论上可能(如 wazero 升级、SIMD 后端动态启用)——目前不预设。

> **语义不变量**:**单次运行内 Stuck 永远 Stuck**(§2.5 论证),跨进程重启自然重评估——这两条共同构成 P2 的「stuck 生命期」语义。本节定稿。

---

## 8. 零 deopt 的形式化论证

§2.4 / §2.5 已分别论证了「无 `Gibbous → Interp` 边」「无 `Stuck → *` 边」。本节把这两条合并提炼成一条形式化语句——「`TierGibbous` 是吸收态」——并对比 P4 状态机解释为什么这个性质是 P2/P3 的核心红利,以及如何在 V11-V13 验收中利用它。

### 8.1 形式化语句(`TierGibbous` 是吸收态的数学陈述)

**定义**(吸收态):状态机 M 中,状态 q 是吸收态 ⟺ 对任意输入 σ ∈ Σ,δ(q, σ) = q(永远停留在 q)。

**P2 状态机定理**(承 §2 DFA 形式化):

- `TierGibbous ∈ F`(终态/吸收态集)
- ∀σ ∈ Σ,δ(TierGibbous, σ) = TierGibbous

**直观陈述**:**在 P2/P3 阶段,任何 Proto 一旦升 Gibbous,任何运行期事件都不能让它离开 Gibbous——没有反向边、没有出边到其他状态、没有自毁路径。**

形式证明:

1. δ 函数定义在 §2.6,枚举出 Gibbous 出现在等号左侧的所有项:
   - δ(TierGibbous, gibbous-running) = TierGibbous(自环)
   - 其它输入 σ ∈ Σ:δ(TierGibbous, σ) 未定义 → 默认 stay(δ 是部分函数,未定义即 self-loop)
2. 状态机不变式(§0.2 / §2 全节论证)断言「Gibbous 无出边」——即 ∀ σ,δ(TierGibbous, σ) ∉ {TierInterp, TierStuck}。

合并即得:δ(TierGibbous, σ) = TierGibbous,∀ σ ∈ Σ。**Gibbous 是吸收态,QED。**

**TierStuck 同理**(§2.5):∀ σ ∈ Σ,δ(TierStuck, σ) = TierStuck;TierStuck 是吸收态。

**核心红利**:**P2/P3 的状态机有两个吸收态(Gibbous / Stuck)和一个起点(Interp),没有循环、没有反向边——这是 P2/P3 「零 deopt」的形式化体现**。

### 8.2 与 P4 状态机的对比(P2/P3 单向无环 vs P4 有 Gibbous→Interp 反向边)

P4 引入「方法 JIT + 类型投机」后,状态机会扩展。届时(P4 §X 的状态机扩展是 P4 主管,本节只引用形态):

```
P2/P3 状态机(本文定稿,单向无环):

   ┌─────────────┐   T1   ┌──────────────┐
   │ TierInterp  │───────►│ TierGibbous  │ ← 吸收态(无出边)
   │ (起点)       │   T2/T3│ ┌──────────┐ │
   └──┬──────────┘  ┌────►│ │ TierStuck│ │ ← 吸收态
      └─────────────┘     │ └──────────┘ │
                          └──────────────┘

P4 扩展状态机(P4 §X 主管,只示意):

   ┌─────────────┐   T1   ┌──────────────┐
   │ TierInterp  │───────►│ TierGibbous  │
   │ (起点)       │◄───────┤ (P4 deopt)    │ ← 不再是吸收态!
   │             │ T6 ★   │              │
   │             │   T2/T3│              │
   │             ├──────►┤ TierStuck    │ ← 仍是吸收态
   └─────────────┘        └──────────────┘
   ★ T6 = guard 失败 / OSR exit(P4 才有的反向边)
```

**关键对比**:

| 性质 | P2/P3 状态机(本文) | P4 扩展状态机 |
|---|---|---|
| 起点 | TierInterp | TierInterp |
| 吸收态 | {TierGibbous, TierStuck} | {TierStuck}(Gibbous 不再吸收) |
| 反向边 | 无 | T6 = TierGibbous → TierInterp(deopt) |
| 单次运行内有限性 | ✅ 任 Proto 至多状态转移 1 次 | ❌ 可能反复 deopt(P4 自己处理 deopt-loop 防抖) |
| 形式化推理难度 | ✅ DFA 简单,人工证明易 | ⚠ 需配合 deopt 阈值/计数器,形式化复杂 |

**所以本文的形式化论证只覆盖 P2/P3**——P4 上线后会有独立的「P4 状态机扩展」文档(P4 主管),引入新状态(如 `TierMethodJIT`)和新反向边。**但 P4 不会修改本文定稿的 T1/T2/T3/Gibbous/Stuck 语义**——它只在外延扩展。

### 8.3 形式化论证的工程价值(让 P2 不变式可数学推理 + V11-V13 验收基础)

为什么花笔墨写这段形式化?——**它直接支持 P2 的验收**。

承 [00-overview](./00-overview.md) §8 / [06-testing-strategy](./06-testing-strategy.md) §X(待补)的 PB5 验收口径:

1. **V11(P2 状态机不变式)**:写 fuzz 测试枚举所有可能的 onBackEdge / onEnter 序列,断言 `pd.tierState` 永远不会从 `Gibbous` 或 `Stuck` 回到 `Interp`。**形式化 §8.1 是这条 fuzz 的设计依据**——若 fuzz 过了,就证明实现满足了形式化语句。
2. **V12(零 deopt 端到端)**:跑 pineapple 真实负载,在所有路径上断言「升层日志只出现一次」「`promoted to gibbous` 不会被 `deopt` 跟着」。**§8.1 保证这一断言在 P2/P3 单次运行内必然成立**——即便实现有 bug,bug 只可能在「该升没升」(false negative)而不会是「升了又退」(违反不变式)。
3. **V13(P2 状态机文档与代码一致性)**:lint 测试断言 Go 代码里 `pd.tierState =` 只在三个位置(§3.2 的 P3/P4 路径 / installGibbous 后),不会出现 `pd.tierState = TierInterp`(回退赋值)。**§2 状态机定稿是 lint 规则的依据**。

**这三条验收的核心都建立在「Gibbous 是吸收态」的形式化保证上**——没有形式化,验收只能靠经验性测试,容易遗漏边角。

> **类比**:像 GC 算法证明「无 dangling pointer」、并发算法证明「无 race」一样,P2 状态机证明「无 deopt 反向边」是层级结构的正确性基石。

### 8.4 投机功能不溜进 P2(投机机制全在 P4 内部,P2 状态机不引入)

**纪律**:**任何「猜类型 + guard」的投机机制都是 P4 的事,不能溜进 P2 实现**。

具体含义:

- **P3 编译的 gibbous 代码不带 guard**([03 §1](./03-compilability-analysis.md) 静态保证):P3 的「锦上添花」是「IC 反馈用作紧凑翻译」(02 §0)——比如 IC 显示 `add` 操作 99% 是 int+int,P3 可以用更紧凑的 int+int wasm 序列;**但 wasm 序列里仍带运行期类型检查**(动态检查后再决定走哪条 wasm 子序列),**不带「假设 int+int,失败 deopt」的 guard**。
- **P2 的 try-compile 不传任何「投机假设」标志给 P3**:`P3.Compile(proto, feedback)` 接口(§4.1)的 feedback 是「锦上添花的紧凑翻译参考」,不是「投机依赖」。**P3 自己也不引入「失败回退」机制**——回退路径根本不存在。
- **P2 状态机没有「投机失败」事件类**(§2.6 输入字母表 Σ):Σ 只含 `hot+compilable+success`、`hot+notCompilable`、`hot+compileFail`、`gibbous-running`——没有 `speculation-fail`。**投机失败不是 P2/P3 状态机能识别的事件**。

**P4 怎么引入投机**:P4 的方法 JIT 在 trace 出投机 fast path 时,在 wasm 代码里**新增** guard 检查(P4 §X 主管),guard 失败时抛 deopt 事件——这个事件让 P4 状态机的 T6 边触发(§8.2 图)。**这一切完全不影响 P2/P3 状态机**——P2 视角看到的还是「Proto 升 Gibbous(由 P4 编译)」与「Proto 卡 Stuck」两种结果。

> **核心纪律落地**:Code review 要求——**P2 包(`internal/bridge`)与 P3 包(`internal/gibbous/wasm`)的代码里不应出现 `guard` / `speculate` / `deopt` 字眼**;若出现,reviewer 拒绝合并。这条纪律是 §8.4 的工程化体现,可入 [06-testing-strategy](./06-testing-strategy.md) §X 的 lint 规则。

---

## 9. 与 P3 trampoline 的接口承诺

§4.4 的 `installGibbous` 写 CallInfo bit50 的协议在 P2 完成,P3 trampoline 在跨层切换时读 bit50 决策流向。本节梳理 P2 与 P3 在 bit50 这条共享通道上的**接口承诺**——P1 / P2 / P3 / P4 各自的责任与不可逆约束。

### 9.1 CallInfo bit50 角色(P1 已预留 / P2 写 / P3 trampoline 读)

承 [../p1-interpreter/05-interpreter-loop.md §1.2](../p1-interpreter/05-interpreter-loop.md):

```
CallInfo word2 [50]  callStatus_gibbous
  - P1 阶段:恒 0(P1 不写,P1 主循环不读)
  - P2 阶段:在 installGibbous 后,新进入该 Proto 的 CallInfo 标 1(由 trampoline 拦截 enterLuaFrame 写入,§4.4)
  - P3 阶段:trampoline 据此判该帧是否走 gibbous 路径
  - P4 阶段:复用同一 bit50 语义(P4 trampoline 与 P3 trampoline 共享接口)
```

**四方协作分工**:

| 阶段 | 责任 | 是否写 bit50 | 是否读 bit50 |
|---|---|---|---|
| P1(crescent) | 预留位 | ❌(恒 0) | ❌(P1 主循环不感知 gibbous) |
| P2(本文) | 决策升层并通知 trampoline | 间接(`installGibbous` 注册 trampoline 表,trampoline 在 enterLuaFrame 写) | ❌ |
| P3(gibbous) | trampoline 拦截/写/读 | ✅(enterLuaFrame 时按 trampoline 表查到则写 1) | ✅(跨层切换时读 bit50 判帧形态) |
| P4(method-jit) | 复用 P3 接口 | ✅(同 P3) | ✅(同 P3) |

**关键洞察**:**bit50 的写入责任在 trampoline,不在 P2 决策机**——P2 的 installGibbous 只把 Proto 注册进 trampoline 表,**实际写入由 trampoline 在「下次进帧」时动态完成**。

理由(承 §4.4 末尾的工程纪律):

- 升层瞬间可能有同一 Proto 正在被解释执行(其 CallInfo bit50=0),不应改这一帧的 bit50 中途变更帧形态——**新进帧标 1,旧进帧消亡前保持 0**。
- 这对 P1 是透明的——P1 的 enterLuaFrame 由 trampoline 包裹(P3 §5),trampoline 的写入位置在 P1 解释器看不到的「P3 → P1 边界」。

### 9.2 接口稳定保证(只 P2 写、不可逆、与 P4 deopt 不冲突)

bit50 的语义在 P2 上线时定稿,**之后任何阶段不能修改**。三条稳定承诺:

1. **bit50 只在升 Gibbous 时被置 1**:在 P2/P3 阶段,**P2 是唯一的 bit50 写者**(通过 trampoline 间接)——除升层外,任何代码路径不能写 bit50。这是 §2.4 「Gibbous 不可逆」的接口层体现。
2. **bit50 不可清零**:**不存在 `uninstallGibbous`**——P2 不提供降层 API,所以 bit50 没有「写 0」的路径。这与 §2.4 / §2.5 状态机不可逆完全对应。
3. **P4 deopt 不写 bit50**:P4 的 deopt 事件(P4 §X)是「当前 frame 的 wasm 投机 guard 失败 → 重建 frame 走 crescent」——这种事件**不动 bit50**。bit50 表达的是「Proto 已升 gibbous」,而不是「当前帧走 gibbous」。即便 P4 deopt 把当前帧重建为 crescent,**Proto 整体仍是 gibbous**(下一次进帧仍走 gibbous,直到下次又 deopt)。bit50 表达的是 Proto 级状态,deopt 是 frame 级事件,两者不冲突。

**接口稳定的工程价值**:

- P3 / P4 的 trampoline 实现可以**只读 bit50 一次**判流向,不需要回过头检查 P2 状态机——bit50 是 P2 状态机的**单一物化**。
- P1 永远不需要感知 bit50,它对 P1 是「设了等于没设」的位——这让 P1 实现保持极简(承原则 1「解释器永不退役」的工程化)。

### 9.3 与 GibbousCode 注册的协同(写入顺序 / 多 State 共享时 atomic / sync.Once)

§4.4 的 installGibbous 三步操作中,**写入顺序是关键**——若顺序错了,P3 trampoline 可能读到「bit50=1 但 GibbousCode 还没挂」的不一致状态。

**正确顺序**(承 §4.4 + §4.5 多 State 协调):

```go
// internal/bridge —— installGibbous 写入顺序定稿
//
// 不变式:从 P3 trampoline 视角看,要么「Proto 未升层(无 GibbousCode + bit50 由 enterLuaFrame 写 0)」,
//          要么「Proto 已升层(GibbousCode 已挂 + bit50 由 enterLuaFrame 写 1)」。
//          没有「bit50=1 但 GibbousCode 不在」的中间态(否则 trampoline 跳到 nil entry crash)。
func (b *Bridge) installGibbous(proto *bytecode.Proto, code *GibbousCode) {
    // 第 1 步:先挂 GibbousCode 到 Bridge map(物理写入,先做)
    //   - 这一步必须在 trampoline.Register 之前,否则 trampoline 可能立即被读到
    //     但 b.gibbousCodes[proto] 还没就绪
    b.gibbousCodes[proto] = code

    // 第 2 步:再注册 trampoline 表(让下次 enterLuaFrame 拦截到)
    //   - trampoline 内部从 b.gibbousCodes 取 code(必须先就绪)
    //   - 这一步是「让 trampoline 知道这个 Proto 升了」的关键
    b.trampoline.Register(proto, code)

    // 第 3 步:bit50 由 trampoline 在「下次 enterLuaFrame」时写
    //   - trampoline 的 enterLuaFrame 包裹拦截:见 trampoline 表里有 proto → 在新建 CallInfo 时写 bit50=1
    //   - 这一步不在本函数内同步发生,只是「物化机制启用」
    //   - 现存 CallInfo(bit50=0)保持不变,自然消亡(§4.4)
}
```

**多 State 共享时的同步**(承 §4.5):

`b.compileMu` 保护整个 considerPromotion 的关键段,所以 installGibbous 在锁内执行——三步写入对其他 State 是原子可见的(锁释放前,其他 State 的 considerPromotion 阻塞在锁上;锁释放后,其他 State 进入会被 §4.5 双重检查拦下)。

**atomic / sync.Once 的备选方案**(P2+ 优化路径):

- **atomic 单独保护 b.gibbousCodes[proto] 写入**:用 `sync.Map` 替换 `map[*Proto]*GibbousCode`,免去整体锁。但 trampoline.Register 仍需自己的并发安全(P3 责任)。
- **sync.Once 包 installGibbous**:`pd.installOnce.Do(...)` 让每个 Proto 的安装只发生一次。但这要求 Bridge 维度有 once 句柄(挂在 Proto 上),**与 [01 §6](./01-profiling.md) 「ProfileData 挂 State 私有」的纪律冲突**——当前不采用。

**当前定稿**:**(A) Bridge 持锁**(§4.5)——简单、与原则 3「每阶段独立交付」一致;待实测有性能压力再优化。

### 9.4 跨层一致性的契约总结

P2 与 P3 trampoline 之间的接口契约,**当前定稿后不应再变**——任何修改触发 V13 验收(§8.3)的「文档代码一致性」 lint 失败:

| 契约项 | 提供方 | 消费方 | 不变式 |
|---|---|---|---|
| `Bridge.gibbousCodes` map | P2(`installGibbous` 写) | P3 trampoline(read-only 查) | 单调增,不删除 |
| `Bridge.trampoline.Register` | P2 调用 | P3 实现 | 同 Proto 不重复 register(`compileMu` + 双重检查保证) |
| CallInfo bit50 | P3 trampoline(`enterLuaFrame` 写) | P3 trampoline(跨层切换读) | 一旦写 1,该 frame 不可回退 0 |
| `P3Compiler.Compile` 接口签名 | P3 实现 | P2 调用 | 上线后不修改(§4.1 末尾) |

> **契约稳定的回退预案**:P2 上线后若发现 bit50 / `gibbousCodes` 接口设计有缺陷,**新接口必须并存**——不能 break P1 / P3 现有约定。这是「每阶段独立交付不亏」(原则 3)在接口层的体现。

---

## 10. 不变式清单(实现与决策须守)

承 [00-overview](./00-overview.md) §6 决策表与本文 §0.2 三条铁律,本节把全部本文定稿的不变式集中列出——**实现 P2 决策中枢的代码、reviewer 审查、未来重构,都不应破坏其中任何一条**。

> 风格参考 [00-overview](./00-overview.md) §6 速查表;本节是 §0.2 的细化版,且每条标了对应章节供回查。

| # | 不变式 | 章节出处 | 验证方式 |
|---|---|---|---|
| **I1** | **状态机单向 + 吸收态**:状态空间 = {TierInterp, TierGibbous, TierStuck};T1/T2/T3 是仅有的三条转移;TierGibbous / TierStuck 无出边到其他状态 | §2.4 / §2.5 / §2.6 / §8.1 | V11 fuzz:任意 onBackEdge / onEnter 序列后,断言 tierState 不会从吸收态退回 |
| **I2** | **TierStuck 是终态**:一旦标 Stuck,单次运行内 considerPromotion 永久 no-op;不重试任何条件;`compileTried = true` 同步设置 | §2.5 / §7.1 / §7.2 | 单元测试:在某 Proto 转 Stuck 后,模拟 100 次阈值越过,断言 P3.Compile 调用次数 = 0 |
| **I3** | **fallback ≠ deopt**:P2/P3 阶段不存在「编译后假设失败回退解释器」的事件;只有「编译前/中失败永久解释」(fallback)与 P4 才有的「运行期 OSR exit」(deopt) | §1 全节 / §8.4 | lint:P2/P3 包代码不出现 `guard` / `speculate` / `deopt` 字眼 |
| **I4** | **升层日志说 gibbous 不说 bridge**:格式 `function <name> promoted to gibbous (...)`;**bridge** 一词不出现在日志里 | §6.1 / [00 §1 末尾](./00-overview.md) | grep:升层日志硬编码字符串只含 `promoted to gibbous` / `stays interpreted` / `compile failed` |
| **I5** | **接口稳定**:`P3Compiler.Compile` 签名(§4.1)、CallInfo bit50 语义(§9.1)、`GibbousCode` 写入字段(§9.3),**P2 上线后不修改**——只能新增,不能改 | §4.1 末尾 / §9.2 / §9.4 | V13 lint:diff 触发 `internal/bridge/interface.go` 修改时强制 review |
| **I6** | **installGibbous 写入顺序**:先 `b.gibbousCodes[proto] = code` 再 `b.trampoline.Register`,bit50 由 trampoline 在 enterLuaFrame 拦截写入 | §4.4 / §9.3 | code review:installGibbous 函数体行序与文档一致 |
| **I7** | **considerPromotion 永不抛 panic / err**:任何 P3 panic 由 §5.2 recover 兜底;返回路径只有 4 条(P1/P2/P3/P4 §3.2),全部 return,无 panic | §3.6 / §5.2 | 单元测试:P3.Compile 故意 panic,断言 considerPromotion 正常返回且 tierState=Stuck |
| **I8** | **TierStuck 与 TierInterp 在 P1 视角等价**:两者都是 P1 解释跑(crescent),无可观察行为差异;区分只在诊断维度——`Stuck` 表示「评估过失败」,`Interp` 表示「冷或还没评估」 | §2.5 / §7.3 | 端到端测试:同一脚本下,Stuck Proto 与 Interp Proto 的执行结果 byte-equal(都走 crescent) |
| **I9** | **零 deopt 形式化**:TierGibbous 是吸收态(§8.1 数学陈述);投机机制全在 P4 内部(§8.4),P2 状态机不引入投机相关状态/事件 | §8 全节 | V12 端到端:升层日志只出现一次,后续不被 deopt 跟随 |
| **I10** | **profile 不丢失**:转 Stuck 后,`pd.entryCount` / `pd.backEdge[]` 仍累加(`onBackEdge` 入口路径无条件累加);只 considerPromotion 入口守卫拦下决策——保留累积数据供诊断 | §7.3 / [01 §4](./01-profiling.md) | code review:onBackEdge 无 `if pd.tierState == TierStuck` 跳过累加 |

**额外纪律(隐式/不上表)**:

- **诊断接口 stdLogger 是 PB0 唯一实装**(§6.4):生产环境的 metrics / structured log 通过 `b.diag` 接口注入,本文不实装;
- **资源耗尽类失败一视同仁**(§7.1):理论上「资源恢复后能重编」是合法语义,但 P2 PB0 不区分,统一进 Stuck 永不重试;
- **匿名 closure 的 protoName 降级**(§6.4):无 Name 字段时,日志字段降级为 `<source>:<line>`(承 [01 §5.7](../p1-interpreter/01-value-object-model.md));若 Source 也缺,**留 §11 缺口**。

> **如何使用本清单**:① 实现 P2 决策机的 PR 在 review 时对照检查;② 未来重构(P4 上线、跨版本升级、性能优化)若违反任一条,**强制回退或独立设计文档说明**——「破坏不变式」不是工程优化,是设计变更。

---

## 11. 文档缺口 / 待决

**(本节列实现前未定的、需在 PB1+ 阶段决策或实测后调整的事项;不阻塞 P2 PB0 上线。承 [00-overview](./00-overview.md) §9 风险汇总,本节是 P2 决策中枢专属缺口。)**

### 11.1 多 State 共享 Proto 的 installGibbous 同步(承 [01 §6](./01-profiling.md))

**问题**:[01-profiling §6](./01-profiling.md) 的 sync.Pool 双表混合方案启用后(pineapple 一类「每请求新 State + sync.Pool 复用」形态),跨 State 累积的 ProfileData 与本 State 私有的 ProfileData 都可能触发 considerPromotion——§4.5 (A) Bridge 持锁的方案是否仍最优?

**观察**:

- 当前定稿(单 State 私有 profile)下,锁竞争频次低(只在阈值临界点),(A) 方案足够。
- 双表混合后,跨 State 累积入聚合表的 ProfileData 是共享读写,触发 considerPromotion 频次更高,可能让 (A) 方案的全局锁成为瓶颈。

**待决**:

- 是否在 PB1+ 改成「per-Proto 锁」(`sync.Map` + `map[*Proto]*sync.Mutex`)?
- 是否合并 [01 §6](./01-profiling.md) 双表方案与本节同步策略,在双表激活时一并升级锁粒度?

**留 P2+ 实测**:在 pineapple 真实负载下测锁竞争开销,若 < 1% wall-clock 不动;若 > 5% 改 per-Proto 锁。

### 11.2 跨版本 stuck 重评估(P2+ 缺口)

承 §7.4:

**问题**:Proto 在 v1.0 因 F7(P3 后端不支持某 opcode)被判 Stuck,用户升级到 v1.1 后 P3 后端支持了——能不能让这个 Proto 在 v1.1 上重试?

**当前**:**通过进程重启自然重评估**(新 Program 实例的 ProfileData 全新),不在单次运行内重评估。

**P2+ 设计骨架**(若实测发现是真问题):

- Stuck 原因结构化(`StuckReason: F7-OpcodeUnsupported(opcode=...)` / `F2-CoroutineCallChain` / ...)
- 持久化到磁盘(可选,伴随 wangshu 的「编译缓存」机制——目前不存在)
- 启动时检查 P3 版本号 + 已知 Stuck 原因,决定是否清除 Stuck 标记重评估

**留缺口理由**:用户场景以短 lived 为主(每请求新 Program),进程重启边界够频繁——长 lived 服务再做不晚。

### 11.3 lazy install 是否拆异步(wazero 实例化抖动)

**问题**:§4 的 `installGibbous` 隐含一个假设——`wazero.CompiledModule` 的实例化是 in-memory 快操作。但实测可能 ~100ms,这会让回边触发 considerPromotion 时主循环阻塞(虽不在最热路径,但可能影响交互式场景的延迟分布)。

**观察**:

- 当前定稿(同步阻塞,§4.1):`P3.Compile` 含 wazero 实例化,considerPromotion 整体可能数百 ms。
- 频次低(仅升层临界点)——百倍/秒以下应该没问题。

**待决**:

- 实测 wazero 实例化延迟分布(P3 PB1 之后);
- 若 P99 > 100ms 且场景有交互延迟敏感性(pineapple 不太敏感,但其它宿主可能),设计「异步 install」——把 considerPromotion 拆成「同步分析 + 异步编译」两段,异步段完成后再 installGibbous(此时双重检查 §4.5 仍守住正确性)。
- 异步方案的代价:更复杂的并发协议、potentially 编译完成时 Proto 已不再热;**留 P3 PB1 实测后决策**。

### 11.4 panic recover 上抛策略(P3 panic 的可观察性升级)

承 §5.2 + §6.4:

**问题**:`b.tryCompile` recover P3 panic 后,只在 stdLogger 写一条 `LogPanic` 行——**生产环境无法据此告警** P3 实现 bug。

**待决**:

- 增加 `Logger` 接口的 metrics 钩子(`OnP3Panic(proto, info)` 让宿主接 prometheus 计数器)?
- 还是让 `Logger` 注入 SaaS 错误上报客户端(Sentry / Datadog)?
- 还是出 P2 PB1 后单独设计「P3 实现 bug 邮件告警」工作?

**当前**:`b.diag.LogPanic` 留接口,实装走 stderr;**生产场景由宿主代码替换 Logger 实现接告警**(承 §6.4 末尾的「生产场景由宿主注入」)。

### 11.5 Source 的回退(匿名 closure 的 line:col 信息)

承 §6.4:

**问题**:`protoName` helper 在 `proto.Name` 缺失时降级为 `<source>:<line>`——但若 Proto 是 `loadstring` 来的、Source 也匿名(empty),日志会变成 `:0` 这类无意义值。

**待决**:

- 在 `Proto` 结构里补 `creationStack` 字段(创建时的 Lua 调用栈摘要)?
- 还是日志层 fallback 用 Proto 的内存地址(`%p`)作为 unique id?
- 还是直接接受 `function <unknown> promoted to gibbous` 的低信号日志?

**当前**:接受低信号——`loadstring` 的匿名 chunk 在真实场景占比极低;**留 PB1 fuzz 试用后看是否需要补**。

### 11.6 诊断结构化指标(OnPromotion 接口接入 prometheus)

承 §6.4 末尾的「生产场景由宿主注入」:

**问题**:当前 `Logger` 接口设计是文本输出(`LogPromoted` / `LogStuck` / `LogCompileFail` / `LogPanic`)——生产场景需要结构化指标(每秒升层数、每秒 Stuck 数、Stuck 累计计数等)。

**待决**:

- 在 `Logger` 接口外增加 `Reporter` 接口,定义 `OnPromotion(proto, latency)` / `OnStuck(proto, reason)` / `OnCompileFail(proto, err)` 几个钩子?
- 还是让 `Logger` 实现自由格式化文本,宿主从文本解析(简单但脆)?
- 还是 P2 PB0 不实装,P2 PB1 加一个 `Reporter` 子接口?

**当前**:**P2 PB0 只 Logger,Reporter 留 PB1**——理由是「先有完整的功能流水线再做指标」(承 [00 §3](./00-overview.md) 的实现里程碑)。

### 11.7 MaxClosureDepth 阈值定标(F6 阈值实测调标)

承 [00 §9](./00-overview.md) / [03 §3.6](./03-compilability-analysis.md):

**问题**:[03 §3.6 F6](./03-compilability-analysis.md) 的「嵌套闭包过深不可编译」阈值 `MaxClosureDepth` 是建议值——本文 §7.3 的 Stuck 计数累积保留正是为这个阈值定标提供数据。

**待决**:

- 实装后跑 pineapple 真实负载,统计 Stuck Proto 中 F6 触发的占比;
- 若 F6 占主因,调高阈值释放编译;若 F6 几乎不触发,调低阈值收紧。
- **本文不预设具体值**——留 [03 §3.6](./03-compilability-analysis.md) 自己定标。

> **本节缺口的元规则**:每条都符合「不阻塞 PB0 上线」+「实测后再决策」原则——P2 PB0 先把决策机做出来,把诊断信号收集起来,**用真实数据反推参数与方案**。

---

## 12. 速查 + 相关链接

### 12.1 状态转移速查表(精简一行式)

承 §2.3,本节是面向「实现者快速查表」用的浓缩版:

```
T1: TierInterp ─[hot ∧ Compilable ∧ Compile()=ok]→ TierGibbous   + installGibbous + log "promoted to gibbous"
T2: TierInterp ─[hot ∧ NotCompilable]→            TierStuck       + log "stays interpreted (not compilable: <F1-F7>)"
T3: TierInterp ─[hot ∧ Compilable ∧ Compile()=err]→ TierStuck     + log "compile failed, stays interpreted: <err>"
T4: TierGibbous → (无,吸收态)
T5: TierStuck   → (无,吸收态)
```

### 12.2 considerPromotion 行为速查

```
considerPromotion(proto, pd):
  P1 [守卫]    pd.tierState != TierInterp     → return (no-op)         §3.2 / I2 / I7
  P2 [可编性]  CompilabilityOf(proto) != OK   → tierState=Stuck, log    §3.3 / T2
  P3 [可编]    P3.Compile(...) returns err   → tierState=Stuck, log    §3.4 / T3
  P4 [成功]    P3.Compile(...) returns code  → installGibbous + log     §3.4 / T1
  ───────────────────────────────────────────────────────────────────
  - 永不抛 panic(P3 panic 由 §5.2 recover 吸收)                        I7
  - 永不返回 err(全部状态变化通过修改 pd.tierState 表达)                I7
  - 多 State 由 b.compileMu 锁 + 双重检查协调                           §4.5
  - 守卫拦下吸收态时,profile 计数仍累加(I10)
```

### 12.3 写入责任速查(谁写谁读)

```
ProfileData.tierState:    Bridge(considerPromotion §3.2)写;Bridge 自身读;P1/P3/P4 不读
ProfileData.feedback:     Bridge(installFeedback §3.2 / 02 §5.5)写;P3.Compile 读
ProfileData.compileTried: Bridge §3.2 / §7.2 写;诊断/test 读
b.gibbousCodes[proto]:    Bridge(installGibbous §4.4 / §9.3)写;Bridge 自身读 + P3 trampoline 读
b.trampoline.Register:    Bridge(installGibbous §9.3)调;P3 trampoline 实现持有
CallInfo bit50:           P3 trampoline(enterLuaFrame §4.4)写;P3 trampoline 跨层切换读
```

---

相关:[00-overview](./00-overview.md)(P2 §0 文档地图 + §6 决策速查) ·
[01-profiling](./01-profiling.md)(P2 §1 回边采样 + ProfileData / §6 多 State profile 归属) ·
[02-ic-feedback](./02-ic-feedback.md)(P2 §2 IC 双计数 + TypeFeedback / §0 P2 写不消费) ·
[03-compilability-analysis](./03-compilability-analysis.md)(P2 §3 F1-F7 / §5 Compilability 三态 / §5.5 P1-only 占位) ·
[05-p3-p4-interface](./05-p3-p4-interface.md)(P3Compiler.Compile 单一事实源) ·
[06-testing-strategy](./06-testing-strategy.md)(P2 §6 决策正确验收 / V11-V13 状态机不变式) ·
[../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md)(CallInfo §1 / bit50 §1.2 callStatus_gibbous) ·
[../p1-interpreter/04-frontend-parser-codegen](../p1-interpreter/04-frontend-parser-codegen.md)(AST 利于可编译性分析) ·
[../p1-interpreter/00-overview](../p1-interpreter/00-overview.md)(P1 前瞻义务对账 §5) ·
[../p1-interpreter/01-value-object-model](../p1-interpreter/01-value-object-model.md)(Proto §5.7 Name/Source 字段) ·
[../p2-bridge/00-overview](./00-overview.md)(P2 §5 try-compile-fallback / §6 状态机 / §8 不变式) ·
[../p3-wasm-tier/00-overview.md](../p3-wasm-tier/00-overview.md)(P3 总览;04-trampoline GibbousCode + trampoline 跨层) ·
[../p4-method-jit](../p4-method-jit/00-overview.md)(P4 §X deopt 反向边 / 状态机扩展) ·
[../roadmap.md](../roadmap.md)(§4 P2 定义 1-2 人月 / §5 五条原则) ·
[../../llmdoc/must/design-premises.md](../../llmdoc/must/design-premises.md)(原则 1 解释器永不退役 / 原则 4 走 fallback)


