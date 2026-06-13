# P2-05 P3/P4 共享前端契约:接口定义 / 跨阶段消费协议 / mock P3

> 状态:**设计阶段,详细设计已齐备**。本文是 P2「分层桥」的**接口单一事实源**(承 [00-overview](./00-overview.md) §0 文档地图):
> ① `P3Compiler` / `P4Feedback` 接口的字段级定义;
> ② P3 与 P4 共享 P2 前端的**根本差异**(P3 `feedback` 可选 / P4 `feedback` 核心);
> ③ 跨阶段消费协议(P2 产料 → P3/P4 消费,接口语义稳定);
> ④ `GibbousCode` 抽象类型(P3 = wazero 模块,P4 = 原生机器码,共同接口);
> ⑤ **mock P3 实现**(供 PB6 引入,PB7 端到端验收时让 P2 不依赖真 P3 完成);
> ⑥ 共享前端的版本演进协议(`TypeFeedback` shape 调整 / `FeedbackKind` 枚举追加的兼容策略)。
>
> 上游契约:[00-overview](./00-overview.md)(§1 边界、§4 PB6 mock 引入、§6 决策表 P3/P4 用 feedback 行、§9 风险);
> [01-profiling](./01-profiling.md)(P2 不直接给 P3/P4 接口,但 ProfileData 是 considerPromotion 的状态机字段持有者);
> [02-ic-feedback](./02-ic-feedback.md)(`TypeFeedback` shape 是 P3/P4 的核心契约,本文是消费侧对接);
> [03-compilability-analysis](./03-compilability-analysis.md) §3.7(F7 P3 后端能力 / `SupportsAllOpcodes` 接口本文给出完整定义);
> [04-try-compile-fallback](./04-try-compile-fallback.md)(状态机调本文 `P3Compiler.Compile` 是升层入口);
> 下游:[../p3-wasm-tier/02-translation](../p3-wasm-tier/02-translation.md)(P3Compiler 的 wazero 实现);[../p4-method-jit](../p4-method-jit.md)(P4 投机消费 P4Feedback,deopt 自管)。
> P1 依赖面:[01 §5.7](../p1-interpreter/01-value-object-model.md)(`Proto` 字段——P3/P4 接到的 Proto 是什么);
> [05 §1.2](../p1-interpreter/05-interpreter-loop.md)(CallInfo bit50 `callStatus_gibbous`——P3 trampoline 的入口判定,P2 在 [04](./04-try-compile-fallback.md) `installGibbous` 时写 1);
> [11 §1.3](../p1-interpreter/11-embedding-arena-abi.md)(`Compile` 占位与可编译性探测——P3/P4 编译触发口径)。

对应 Go 包:`internal/bridge`(本文接口宿主)、`internal/gibbous/wasm`(P3 实现)、`internal/gibbous/jit`(P4 实现)、`internal/bridge/mock`(本文 §7 mock P3)。

---

## 0. 定位:P2 是编译层共享前端

### 0.1 一句话总结

P2 是**编译层的共享前端**——它把「该编译什么、什么类型、多热」算好,P3/P4/P5 共享这个前端,**只换发射后端**。这正是 [../roadmap.md](../roadmap.md) §4 P4 阶段的字面承诺:

> 「P4:**继承 P3 的全部分层结构,只换发射后端**(Wasm 发射→原生发射)」

而「分层结构」的核心(热度计数 + IC feedback + 可编译性闸门 + 升降决策状态机)正是 P2 在 [01-profiling](./01-profiling.md) / [02-ic-feedback](./02-ic-feedback.md) / [03-compilability-analysis](./03-compilability-analysis.md) / [04-try-compile-fallback](./04-try-compile-fallback.md) 四篇里建好的。**P2 一次建好,P3/P4/P5 三阶段复用**——这是分层 VM 在演进维度的最大经济性。

### 0.2 「共享前端」的物理体现

承 [../p2-bridge/00-overview](./00-overview.md) §7.3 末:**「P2 是所有编译层的共享前端」**。本文的存在理由就是把这条承诺落到接口层面——任何让 P3 / P4 各自重新设计「热度采样 / 类型反馈 / 升层判断」的实现都直接判否,**P2 一份接口,P3/P4 都吃**。

P2 与 P3/P4 的接口分两份:

| 接口 | P2 端角色 | 实现方 | 文档单一事实源 |
|---|---|---|---|
| `P3Compiler` | P2 调下去:把 Proto 喂给后端编译 | P3(wazero)/ P4(原生) / mock(本文 §7) | 本文 §2 |
| `P4Feedback` | P4 反向读上来:从 P2 取 TypeFeedback 做投机 | P4 自身实现(读 P2 的 ProfileData.feedback) | 本文 §4 |

> **为什么 P3 不需要对偶的 `P3Feedback`**?P3 是 try-compile 非投机,**不依赖** feedback 正确性([../p3-wasm-tier/06-ic-feedback-consume](../p3-wasm-tier/06-ic-feedback-consume.md) §1)。它在 `P3Compiler.Compile` 调用入参里**顺带**接到 feedback(可选用),不需要再开一条反向通道。P4 不同——P4 是投机 JIT,deopt 失败 + 重编译时需要重新读 feedback,所以 P4 主动开「反向读」接口(本文 §4)。**两条接口形态不对称,反映 P3/P4 用法不对称**(详见 §1)。

### 0.3 接口稳定性即「共享前端」的硬约束

「共享前端」的工程本质是 **接口稳定性** —— P2 实现端定稳之后,P3/P4/P5 三阶段都按同一份接口来对接,**P2 实现零修改**。这条不变式在三个时间点上兑现:

1. **P3 上线(PB6/PB7 → 真 P3 PR)**:`P3Compiler` 接口已稳,从 mock(§7)切到真 P3(`internal/gibbous/wasm`),P2 端**不动**;
2. **P4 上线(P4 阶段)**:同样的 `P3Compiler` 接口被 P4 实现复用(P4 也是「拿到 Proto + feedback,产出可执行码」语义),P2 端**不动**;
3. **TypeFeedback shape 演进**(P4 实测后可能加字段):向后兼容追加,P3 接到不认识的字段忽略即可,P2 端**不动**(详见 §9)。

**P2 实现一旦定稳,P3/P4/P5 三阶段共用,接口零修改**——这是本文设计的最高优先级原则,所有具体接口形状都在兑现这一条。

### 0.4 与下游(P3 / P4)文档的边界

| 关注点 | 本文(05)拥有 | 不属于本文 |
|---|---|---|
| **`P3Compiler` 接口形状** | ✅ 字段级签名 + 错误返回语义 + `SupportsAllOpcodes` 的契约规范 | 真 P3 实现细节(wazero 集成,字节码→Wasm)→ [../p3-wasm-tier/02-translation](../p3-wasm-tier/02-translation.md) |
| **`P4Feedback` 接口形状** | ✅ `FeedbackFor` 取 feedback 的契约 + confidence 消费协议 | 真 P4 实现(模板编译 / OSR exit / 自管栈)→ [../p4-method-jit](../p4-method-jit.md) |
| **`GibbousCode` 抽象类型** | ✅ 共同接口(`Run` / `Dispose` / `GetTrampoline`) | P3 / P4 各自的具体类型(wazero `api.Function` / 原生码段)→ 各自后端文档 |
| **mock P3 实现** | ✅ 完整代码骨架,PB6 引入 | — |
| **共享前端的版本演进** | ✅ shape 字段追加 / `FeedbackKind` 枚举追加的兼容策略 | shape 字段实测后调整的具体内容 → [02-ic-feedback](./02-ic-feedback.md) §9 缺口 |
| **P3 编译失败回 P2** | ✅ 错误返回语义(`error != nil ⇒ TierStuck`) | TierStuck 转移逻辑 → [04-try-compile-fallback](./04-try-compile-fallback.md) §3 |
| **P4 deopt 回 P2** | ✅ 接口形状(P4 自己有 deopt,P2 是接受重聚合请求的一方) | deopt 实装 → [../p4-method-jit](../p4-method-jit.md) §3 |

本文回答的核心问题是:**P2 与 P3/P4 之间,接口是什么形状,各自承担什么职责,接口如何稳定到三阶段共用**。具体的「P3 怎么生成 Wasm」「P4 怎么发原生码」「P4 怎么做 OSR exit」都不在本文,本文只关心「接口面」。

---

## 1. P3/P4 用 P2 的根本差异

理解 P3 与 P4 用 feedback 的语义差异,是本文一切接口设计的出发点。承 [../p2-bridge/00-overview](./00-overview.md) §7.2 末尾的对比表,本文展开论证。

### 1.1 一行差异

| | **P3 用 feedback** | **P4 用 feedback** |
|---|---|---|
| 用途定位 | **可选的紧凑翻译**(锦上添花) | **核心的类型投机供料**(必需) |
| 依赖 feedback 正确性 | **不依赖**(try-compile 对所有合法输入正确) | **依赖**(投机基于 feedback,但有 guard 兜底 + deopt) |
| feedback 错的后果 | **无**(P3 不赌类型) | guard 失败 → deopt 回解释器(慢但不错) |
| `confidence` 字段作用 | 弱(P3 不投机) | 强(高 confidence 才投机) |
| feedback 缺失(nil)的后果 | 仍正确编译,只是发通用 Wasm(语义完备的快路径分发) | 仍正确编译,只是 P4 退化为「无投机模板」(失去倍率,但仍省 dispatch 税) |
| P2 的责任 | 产 feedback 即足,无 deopt 通道 | 产 feedback + 接受 P4 重聚合请求(deopt 后 feedback 可能更新) |

### 1.2 P3 不依赖 feedback 正确性的硬证据

[../p3-wasm-tier/06-ic-feedback-consume](../p3-wasm-tier/06-ic-feedback-consume.md) §1 原话:

> **「快路径检查 = 语义分发,不是投机 guard」**——P3 的 ADD 翻译里 `IsNumber×2`、GETTABLE 的「同表同代次」检查与解释器快路径([05](../p1-interpreter/05-interpreter-loop.md) §4.1/§6.3)是**同一组判定**——失败走慢路径助手得到正确结果,**不存在 deopt**。feedback 只影响「内联哪条快路径、固化哪份 IC 快照」,即代码形状,不影响语义覆盖面。

这意味着:

- 即使 feedback 说「这个算术点恒为 number」(`FBArithStableNumber`,confidence=1.0),P3 的翻译**仍含 `IsNumber×2` 检查**——只是因为 feedback 说稳定,P3 知道「检查通过的概率高」,可以把快路径直接内联(`f64.add` 紧跟检查后),慢路径分支内联到边角(更友好的代码布局,icache 友好);
- **若实际跑起来非 number** 来了(feedback 错或运行期改变),P3 的翻译走慢路径助手,**正确返回 number / metamethod 结果**,不出错;
- 所以 P3 即便接到完全错误的 feedback(如把所有点都标 `FBArithStableNumber` 但实际全是 string),**仍正确**——只是性能不优(代码布局不利于热点形态)。

### 1.3 P4 必须依赖 feedback 正确性

[../p4-method-jit](../p4-method-jit.md) §2.1 表里 `FBArithStableNumber` 的投机模板:

> 投机模板发什么:**直接 f64 运算指令**(如 `mulsd` / `fmul`)+ NaN 规范化;
> guard 检查什么:两操作数 `IsNumber`(NaN-box 单次 u64 无符号比较)。

注意 P4 的 ADD 投机模板与 P3 的 ADD 翻译的根本区别:

- **P3**:`IsNumber×2` 是**语义分发**(快路径前置检查,失败也是合法路径,落到慢路径助手得到正确结果);
- **P4**:`IsNumber×2` 是**投机 guard**(失败即「投机前提不成立」,触发 OSR exit 回解释器,**该帧剩余执行整体交还解释器**——[../p4-method-jit](../p4-method-jit.md) §3.1)。

物理上两者发的都可能是「比较 + 条件跳」,但**语义/机器码后续路径完全不同**:

| | P3 的 IsNumber×2 失败后 | P4 的 IsNumber×2 失败后 |
|---|---|---|
| 后续路径 | 调慢路径 imported helper(同函数内仍跑 Wasm),走 metamethod / coercion → 正确返回 → 直线继续 | OSR exit:写回栈槽真相 + 跳出 JIT 世界 + crescent 接管该帧剩余执行 |
| 函数边界 | 不离开 Wasm 函数 | 离开 JIT 函数,转给解释器 |
| 是否「错」 | 不是错(只是慢路径) | 不是错(投机失败 ≠ 错误,只是回到全语义路径) |
| feedback 错的后果 | 无可观察后果(同样得正确结果) | 频繁 deopt → 性能塌陷,**最终被 P4 拉黑投机**([../p4-method-jit](../p4-method-jit.md) §3.4) |

所以 P4 **必须**依赖 feedback 正确性—— feedback 反映的是「这个点过去 N 次执行的类型分布」,P4 据此判断是否值得发投机模板;若 feedback 严重失真(P2 聚合 bug 或 race-tolerant 读读到坏值),P4 投机失败率高、deopt 风暴、最终 `TierStuck-speculation`。

> **但 P4 仍不会出错**——只是性能塌陷。这正是 deopt 机器的价值:**guard 是兜底**,feedback 错只损性能不损正确性([../p4-method-jit](../p4-method-jit.md) §2.1 末尾「guard 多判损性能,漏判出错」是另一维度的危险性)。

### 1.4 confidence 字段在两阶段的角色

[02-ic-feedback](./02-ic-feedback.md) §5.1 给出 confidence 的物理含义:

| FeedbackKind | confidence 物理含义 | P3 怎么用 | P4 怎么用 |
|---|---|---|---|
| `FBArithStableNumber` | numHits / (numHits+metaHits) 的真比例 | 不读 confidence,只用 kind 决定「是否内联快路径」 | 读 confidence,**≥0.99 才发投机模板**;<0.99 退化为通用模板(同 P3 形态) |
| `FBTableMono` | 恒 1.0(P1 mono IC 无降级历史) | 用 stableShape/stableIndex 直接内联 IC 命中路径 | 同 P3 用 stableShape/stableIndex,但 guard 失败 ⇒ OSR exit(P3 是退助手) |
| `FBTableMega` | 0.0(明确「别投机」标记) | 发通用 GETTABLE 翻译(完整哈希查找) | **不发投机直达槽,老实查哈希**([02-ic-feedback](./02-ic-feedback.md) §6) |
| `FBUnstable` | 0.0 或诊断比例 | 发通用翻译 | 不投机,发通用模板 |

**关键观察**:**confidence 的强消费方是 P4**;P3 把 confidence 当作「锦上添花的提示」,即便不读 confidence 只看 kind 也能正确编译。**这是 P3 / P4 共用同一份 `TypeFeedback` 但语义不对称消费的具体体现**——`confidence` 字段对 P3 是 noise(无害的可忽略字段),对 P4 是核心(投机激进度的旋钮)。

### 1.5 共享前端的「不对称消费」原则

总结 §1.1-§1.4 得出本文的总纲:

> **P2 产出一份对称的 feedback,P3/P4 不对称消费**。
>
> - **对称产出**:P2 产 `TypeFeedback` 时不知道下游是 P3 还是 P4,统一聚合所有 IC 观测、按 pc 索引、含 kind+confidence+stableShape/stableIndex(详见 [02-ic-feedback](./02-ic-feedback.md) §4);
> - **不对称消费**:P3 用作紧凑翻译提示(可选,kind 主用,confidence 忽略),P4 用作投机供料(必需,kind+confidence 都用,stableShape/stableIndex 直达槽);
> - **P2 不为 P3/P4 各产一份**(浪费),也不在 P3 编译时省略 feedback(让 P3 有机会做紧凑翻译)。

这与 [02-ic-feedback](./02-ic-feedback.md) §7.1 「三层信息分层生产消费」一脉相承——信息在 P2 一次产出,P3/P4 按各自策略消费,P2 不预设消费方策略。

---

## 2. `P3Compiler` 接口定义

`P3Compiler` 是 P2 调下游的核心接口——P2 在 [04-try-compile-fallback](./04-try-compile-fallback.md) §3 状态机决定升层时,调 `P3Compiler.Compile` 触发实际编译。本节给字段级定义。

### 2.1 完整接口签名

```go
// internal/bridge —— P3/P4 编译器与 P2 之间的核心接口。
// P3(wazero 后端,internal/gibbous/wasm)、P4(原生后端,internal/gibbous/jit)、
// mock(internal/bridge/mock)三方实现。
//
// P2 实现端持 `p3 P3Compiler`(注入式),不耦合具体后端类型——
// 这是「共享前端」的物理体现:P3 → 真 P3 / P4 → 真 P4 / mock 三种实现可热替换,
// P2 的 considerPromotion / installGibbous 代码完全不动(§0.3 接口稳定性)。
type P3Compiler interface {
    // SupportsAllOpcodes 检查 Proto 中的所有 opcode 是否都在后端的支持集内。
    //
    // 调用方:[03-compilability-analysis] §3.7 F7 闸门(analyzeProto 中调用)。
    // 实现方契约:
    //   - 实现者扫一遍 proto.Code,对每条 instruction 取 op,查后端的支持表;
    //   - 任一未支持(或 op 在 38..63 预留区间但后端尚未实现该伪指令)即返回 false;
    //   - 性能要求:O(N) 单遍,N=proto.Code 长度;通常每个 Proto 几百 ns;
    //     不应在此函数内分配(每个 Proto 在 Compile 时调一次,频率不高但仍是 codegen 阶段)。
    //   - 调用纯只读:不修改 Proto、不持久化任何状态。
    //
    // 错误处理:实现方不应 panic;遇到无法识别的 opcode 编号也返回 false(保守拒)。
    SupportsAllOpcodes(proto *bytecode.Proto) bool

    // Compile 把 Proto 编译成 GibbousCode(可执行产物)。
    //
    // 调用方:[04-try-compile-fallback] §3 considerPromotion 中,在确认
    //   ① TierState == TierInterp ② Compilable == CompCompilable ③ 热度越阈值
    //   后调用本函数。
    //
    // 入参:
    //   - proto:目标 Proto(已通过 F1-F7 闸门,可编译);
    //   - feedback:类型反馈快照(P2 [02-ic-feedback] §4 聚合产物);可能为 nil
    //     (实现方必须容忍 nil ⇒ 退化为「无 feedback 提示」编译,仍正确)。
    //
    // 返回:
    //   - GibbousCode:编译产物,封装可执行入口 + 资源句柄(详见 §6);
    //   - error:编译失败原因。
    //
    // 错误返回的语义(关键契约):
    //   - error != nil ⇒ P2 把该 Proto 标 TierStuck(永久解释,不重试,
    //     [04] §3 转移规则);本调用是「该 Proto 升层尝试」的唯一一次,
    //     失败即 fallback,不再调 Compile。
    //   - 实现方应区分错误类型:F7 边角(应在 SupportsAllOpcodes 已拦,
    //     这里再 panic 报 codegen bug);资源耗尽(wazero OOM、原生码段
    //     mmap 失败)→ 错误传 P2;后端 panic(实现 bug)→ 实现方 recover
    //     转 error,**不让 panic 穿越本接口**(P2 不能因后端 bug 崩溃,
    //     只能 fallback 该 Proto)。
    //
    // 性能要求:Compile 不在热路径(只在升层时一次性调用),
    //   预算数毫秒级可接受;实现方应避免在 Compile 内做不必要分配。
    //
    // 并发要求:Compile 可被多 State 并发调用同一 Proto(若启用未来的
    //   全局 profile 聚合 [01-profiling] §6.4 (C) 方案);实现方需保证
    //   线程安全(典型实现:wazero runtime 自身线程安全;原生码段写入
    //   到独立内存区,无共享可变状态)。
    Compile(proto *bytecode.Proto, feedback *TypeFeedback) (GibbousCode, error)
}
```

### 2.2 接口字段语义详解

#### 2.2.1 `SupportsAllOpcodes` 的契约

承 [03-compilability-analysis](./03-compilability-analysis.md) §3.7 的 F7 兜底闸门——`SupportsAllOpcodes` 是 P2 询问后端「这个 Proto 你都能编吗」的入口。

**实现方典型骨架**(P3 wazero 后端):

```go
// internal/gibbous/wasm —— P3Compiler.SupportsAllOpcodes 实现
func (c *Compiler) SupportsAllOpcodes(proto *bytecode.Proto) bool {
    for _, ins := range proto.Code {
        op := bytecode.OpcodeOf(ins)
        if !c.supported[op] {
            return false                        // 任一未支持即拒
        }
    }
    return true
}

// supported 表在 P3 后端初始化时建立——只有「明确实现 Wasm 翻译」的 opcode
// 才进表(详见 [../p3-wasm-tier/02-translation](../p3-wasm-tier/02-translation.md) §3.4)。
// P3 开发期渐进扩充此表,自然反映「后端能力成长」到 P2 的可编译性判定上。
```

**「保守缺省」原则**(承 [03-compilability-analysis](./03-compilability-analysis.md) §3.7.4):

- supported 表默认严格——只把**明确实现的 opcode** 加入,其他全部返 false;
- 任何**未识别**的 opcode 编号(如 38..63 预留区间被未来 opcode 占用但本后端版本不识别)→ 返 false(保守拒);
- 实现方**不应 panic**——见到未知 op 就当不支持,让 P2 顺理成章 fallback 到解释。

#### 2.2.2 `Compile` 的错误返回语义

`Compile` 的错误返回是 P2 ↔ P3 的关键契约——它决定「编译失败如何 fallback」。错误分三类:

| 错误类型 | 触发场景 | 实现方应返回 | P2 应对 |
|---|---|---|---|
| **F7 边角**(SupportsAllOpcodes 漏拦) | 实现 bug;不应发生 | `panic(...)` 报 bug | 不应到达——若到达,实现方 panic 是 codegen 一致性的最后一道闸 |
| **资源耗尽** | wazero 编译时 OOM、原生码段 mmap 失败、wazero module 实例化超限 | `error`(`bridge.ErrCompileResource`,详见 §2.4) | TierStuck,该 Proto 永久解释 |
| **后端 panic 兜底** | wazero 内部 panic、generator panic、未预期 bug | `error`(`bridge.ErrCompilePanic` 包装),**不让 panic 穿透本接口** | TierStuck,该 Proto 永久解释,可记诊断日志报 bug |

> **关键纪律**:**panic 不能穿透 `P3Compiler.Compile`**——P2 的状态机是单线程同步语义([04] §3),让一个 Proto 的编译 panic 把整个 VM 拽倒是工程灾难。实现方在 `Compile` 入口套 `defer recover()` 转 error,P2 拿到 error 后走 fallback 路径,VM 整体仍跑(只是该 Proto 永久解释)。

#### 2.2.3 `Compile` 入参 feedback 的可空性

`feedback *TypeFeedback` 可能为 nil:

- **场景 1**:P1 IC 还未充分聚合(罕见——considerPromotion 触发时 [04] 必先调 aggregator,fb 通常非 nil)。
- **场景 2**:测试场景下 P2 显式传 nil(给 P3/P4 跑「无 feedback 提示」的对照基线)。
- **场景 3**:Aggregator panic 兜底 → 传 nil 而非中断升层流程(实现自由度,P2 实装时再定)。

**实现方必须容忍 nil**——退化为「全用通用翻译」(无紧凑翻译提示),仍正确编译。这与 §1.1 「P3 不依赖 feedback 正确性」一脉相承——nil 是「极端情况下的无信息」,P3/P4 都应该能在无信息下正确工作(只是失去 feedback 带来的代码紧凑度优化)。

### 2.3 错误类型常量

```go
// internal/bridge —— P3Compiler.Compile 的错误返回常量
var (
    // ErrCompileResource —— 资源耗尽(wazero OOM / 原生码段 mmap 失败 / module 实例化超限)。
    // P2 应对:TierStuck,该 Proto 永久解释。
    ErrCompileResource = errors.New("bridge: compile resource exhausted")

    // ErrCompilePanic —— 后端 panic 兜底(实现方应 recover 后 wrap 为本错误)。
    // P2 应对:TierStuck + 诊断日志(报 bug,但 VM 整体不崩)。
    ErrCompilePanic = errors.New("bridge: compile panic recovered")

    // ErrCompileUnsupported —— SupportsAllOpcodes 漏拦的边角(理论不应到达)。
    // 实现方可选用此错误,但更倾向于 panic(显式暴露 codegen bug)。
    ErrCompileUnsupported = errors.New("bridge: unsupported shape (codegen bug)")
)
```

实现方用 `fmt.Errorf("...: %w", ErrCompileResource)` 包装具体细节,P2 用 `errors.Is(err, ErrCompileResource)` 判类型分支处理(诊断日志格式略不同,详见 [04-try-compile-fallback](./04-try-compile-fallback.md) §6)。

### 2.4 调用契约的不变式清单

P2 调 `P3Compiler.Compile` 时守的契约:

| # | 不变式 | 兑现处 |
|---|---|---|
| C1 | 只在 `Compilable == CompCompilable` 通过后调用 | [04] §3 considerPromotion 入口 |
| C2 | 只在 `TierState == TierInterp` 时调用(防重复升层) | [04] §3 状态机守卫 |
| C3 | 调用同一 Proto 不会两次成功(后续状态转走 TierGibbous/TierStuck) | [04] §3 单向状态机 |
| C4 | feedback 入参可空,实现方容忍 nil | §2.2.3 |
| C5 | 错误返回 ⇒ P2 标 TierStuck,不重试 | [04] §3 失败转移 + §3.3 不重试纪律 |
| C6 | panic 不穿透接口 | §2.2.2(实现方 recover) |

实现方守的契约:

| # | 不变式 | 兑现处 |
|---|---|---|
| I1 | SupportsAllOpcodes 是纯函数(只读 Proto,无副作用) | §2.2.1 |
| I2 | Compile 是同步语义(返回时编译完成,GibbousCode 立即可用) | §2.1 接口签名 |
| I3 | 不修改 Proto / feedback(都是只读消费) | §2.1 |
| I4 | panic 不穿透(recover 转 error) | §2.2.2 |
| I5 | nil feedback 不导致正确性变化(只影响代码形状) | §1.1 共享前端原则 |

---

## 3. P3 用 feedback 的语义:可选,锦上添花

### 3.1 「可选」的物理含义

P3 用 feedback 的「可选」是说:**P3 在 feedback 存在时可以发更紧凑的 Wasm 代码,在 feedback 不存在(nil)或 feedback 错时仍发正确的 Wasm 代码**。

这里「正确」的标准来自 [../p3-wasm-tier/06-ic-feedback-consume](../p3-wasm-tier/06-ic-feedback-consume.md) §1:Wasm 代码对**所有合法 Lua 输入**给出与解释器逐字节一致的结果。feedback 不影响这个标准的兑现,只影响:

1. **代码形状**(同样的语义,不同的字节布局);
2. **icache 友好度**(快路径前置/后置 vs 通用分发);
3. **module 体积**(紧凑翻译可省一些「通用 fallback 助手调用」字节)。

### 3.2 用法形态:稳定算术点的紧凑翻译

[../p3-wasm-tier/02-translation](../p3-wasm-tier/02-translation.md) §3.2 ADD 翻译是 feedback 在 P3 的典型用法:

```wat
;; 不带 feedback 时:P3 发完整快慢分发
(local.set $vb (i64.load offset=8*B (local.get $base)))
(local.set $vc (i64.load offset=8*C (local.get $base)))
(if (i32.and (call $is_number (local.get $vb))
             (call $is_number (local.get $vc)))
  (then ;; 双 number 快路径:f64.add + canonicalizeNaN
        ...)
  (else ;; 慢路径助手:metamethod / coercion
        (br_if $err (call $h_arith ...))))

;; 带 feedback (FBArithStableNumber, conf=1.0) 时:P3 仍发同样的快慢分发,
;; 但**编译期决定快路径在前**(代码块紧跟 if-then,慢路径塞到 if-else 末端)
;; ——快路径的 i-cache 局部性更好。
;; 注:如果 feedback 是 FBUnstable,P3 倾向把慢路径前置(降低分支预测失败开销)。
```

**关键观察**:**P3 的翻译在「带 feedback 」与「不带 feedback」两种形态下,语义完全相同**——两者都有 IsNumber×2 的快路径检查、都有慢路径助手 fallback、都对所有合法输入正确。feedback 只影响**字节顺序**与**模板选择**,不影响语义覆盖面。

### 3.3 不投机的纪律

**P3 不存在「投机 guard」**——所有「类型检查」都是 `语义分发`(检查通过 → 快路径,失败 → 慢路径,**两路都正确**)。承 [../p3-wasm-tier/06-ic-feedback-consume](../p3-wasm-tier/06-ic-feedback-consume.md) §1:

> 「快路径检查 = 语义分发,不是投机 guard。失败走慢路径助手得到正确结果,不存在 deopt。」

物理体现:**P3 编译产物里没有任何「OSR exit 出 JIT 世界」的代码路径**——失败永远在 Wasm 函数内部走慢路径,该函数仍跑到底,RETURN 才退出。这与 P4 的「guard 失败 ⇒ 出 JIT」根本不同(§1.3)。

> **P3 没有 deopt 通道是 P3 战略价值的核心**(承 [../p3-wasm-tier/00-overview](../p3-wasm-tier/00-overview.md) §0):P3 不需要实现 OSR exit、snapshot、guard 失败恢复——这些复杂机器全留给 P4。**P3 享受 P2 的零 deopt 简化**(单向状态机,[04] §2)与 wazero 的成熟后端(§9 [../p3-wasm-tier/00-overview](../p3-wasm-tier/00-overview.md) §8 四项税外包),专心把分层骨架跑通。

### 3.4 例子:GETTABLE 单态点的 IC 快照固化

[../p3-wasm-tier/02-translation](../p3-wasm-tier/02-translation.md) §3.4 GETTABLE 翻译:

```wat
;; 编译期固化 IC 快照 = (tableRef, gen, kind, index)
;;   ↑ 来源:feedback.points[pc].stableShape (= ICSlot.shape, 即 gen)
;;          feedback.points[pc].stableIndex (= ICSlot.index, 即槽位)
(local.set $t (i64.load offset=8*B (local.get $base)))
(if (i32.and (call $is_table (local.get $t))
             (i32.and (i64.eq (call $gcref (local.get $t)) (i64.const SNAP_TABLEREF))
                      (i32.eq (call $gen (local.get $t))   (i32.const SNAP_GEN))))
  (then ;; 同表同代次 ⇒ 直达槽(array/node 按 SNAP_KIND 静态选)
    (i64.store offset=8*A (local.get $base) (call $slot_load ...)))
  (else ;; miss/形状变了 ⇒ 完整查找+元方法,正确性不依赖快照
    (br_if $err (call $h_gettable (local.get $base) (i32.const PC)))))
```

**P3 用 feedback 的方式**:

- 读 `feedback.points[pc].kind == FBTableMono` → 知道这点单态稳定;
- 把 `stableShape` (= gen) 与 `stableIndex` (= 槽位) 烧进 Wasm 代码作为常量;
- 失败时(表 gen 已变 / 不是同一表)走 helper 完整查找,**仍正确**。

**若 feedback 是 nil 或 `FBTableMega`**:P3 跳过 IC 快照固化,直接发 helper 查找(完整哈希)——慢一些但仍正确。**这是「可选」的体现**:有 feedback 时锦上添花,无 feedback 时退化到通用形态。

### 3.5 confidence 在 P3 上的弱消费

§1.4 表已说 P3 不强依赖 confidence。具体:

- **P3 通常只看 kind**:`FBArithStableNumber` ⇒ 紧凑算术翻译;`FBTableMono` ⇒ IC 快照固化;`FBUnstable`/`FBTableMega` ⇒ 通用翻译。
- **若实现方愿意精细化**:可读 confidence,< 某阈值时退化为通用翻译(避免对低 confidence 的点也固化 IC 快照——失效 miss 多)。但这是优化项,P3 实装不强求。
- **不读 confidence 也对**:即便对低 confidence 的稳定点固化 IC 快照,运行期不命中走 helper,正确性不损。

> **P2 仍坚持产 confidence 给 P3**:虽然 P3 弱用,但**接口统一**(P4 必须用)是「共享前端」的硬约束(§0.3)。P2 不为 P3 单产一份精简 feedback——这会让接口分叉,违反「P3/P4 共用一份接口」的总纲。

### 3.6 P3 不依赖 feedback 正确性的「极端测试」

为了在测试套里验证「P3 对 feedback 错容忍」,[06-testing-strategy](./06-testing-strategy.md) §X(待补)会引入这样的测试:

- **故意发坏 feedback**:把所有 IC 点都标 `FBArithStableNumber, conf=1.0` —— 跑实际混合形态脚本(含 string + table + number),P3 编译出来的 Wasm 仍应给正确结果(只是性能不优,因为快路径检查频繁失败走慢路径)。
- **空 feedback**:传 nil → P3 编译出的 Wasm 与「带正确 feedback」的版本**逐字节差分一致**(语义层面;实际字节布局可能略有差别)。

**这两组测试是 P3「不依赖 feedback 正确性」的工程兑现**——任何因 feedback 错或缺失导致 P3 输出错果的情况都直接判为 P3 实现 bug。

---

## 4. `P4Feedback` 接口定义

P4 是投机 JIT,核心消费 feedback。本节给 P4 反向读 feedback 的接口形状。

### 4.1 完整接口签名

```go
// internal/bridge —— P4 的反向读 feedback 接口。
// 实现方:internal/bridge(P2 自身——P4 通过持 *Bridge 实现 P4Feedback);
// P3 不消费此接口(P3 通过 Compile 调用入参接收 feedback)。
//
// 设计理由:P4 在 deopt 后需要重新拉 feedback(投机失败 ⇒ 解释执行 ⇒
// IC 重新累计 ⇒ feedback 更新)再决定重编译,所以需要「随时可读」通道——
// 与 P3 一次性入参不同。
type P4Feedback interface {
    // FeedbackFor 取某 Proto 的当前 TypeFeedback 快照。
    //
    // 调用方:P4 在 ① 首次 Compile 时(等价 P3 入参)② deopt 后重训练完成
    //   再次 Compile 时(读最新 feedback,可能比上次更准)。
    //
    // 返回:
    //   - *TypeFeedback:可能为 nil(若该 Proto 还未聚合过 feedback);
    //     非 nil 即为 [02-ic-feedback] §4 的 TypeFeedback 结构。
    //   - 返回的指针**只读**:实现方保证返回的 TypeFeedback 不会被后续 P2
    //     聚合修改(P2 [02-ic-feedback] §4.5 初版「只聚合一次」,后续重聚合
    //     用 CAS 装新指针,旧指针仍指向不可变快照——见 §4.4 重聚合协议)。
    FeedbackFor(proto *bytecode.Proto) *TypeFeedback

    // RequestRefresh 请求 P2 为某 Proto 重新聚合 feedback。
    //
    // 调用方:P4 在 deopt 风暴后判断「当前 feedback 已陈旧,需重训练」时调。
    // 实现方:P2 把该 Proto 的 ProfileData.feedback 字段标记为 stale,
    //   下次有 IC 观测累积时触发重聚合;或立即重新 aggregate 并 CAS 安装。
    //   P2 实装策略详见 §4.4。
    //
    // 注意:本方法仅是「请求」,P2 自由决定何时实际重聚合;
    //   返回值是「请求是否被接受」(P2 端可拒绝,如该 Proto 已 TierStuck)。
    //
    // P2 初版可不实装本方法(P4 阶段才需要)——接口预留,初版返 false 即可。
    RequestRefresh(proto *bytecode.Proto) bool
}
```

### 4.2 接口字段语义详解

#### 4.2.1 `FeedbackFor` 的契约

`FeedbackFor` 是 P4 的「随时读」通道——P4 在编译某 Proto 时调一次,deopt 后想重编译再调一次。返回的 `*TypeFeedback` 是**只读快照**:

- P2 内部用 atomic.LoadPointer 取 `ProfileData.feedback`,返回给 P4;
- 一旦 P4 拿到指针,**该指针指向的 TypeFeedback 不变**(即便 P2 重聚合,新 feedback 通过 CAS 装到 ProfileData.feedback 字段,旧指针仍指原来的不可变快照);
- P4 不需要保护(无锁读),也不需要复制(快照本就不可变)。

这与 [02-ic-feedback](./02-ic-feedback.md) §5.5 「CAS 安装 feedback」配套——P2 端的写入用 CAS,P4 端的读取用普通 LoadPointer,**无锁同步**。

#### 4.2.2 `RequestRefresh` 的契约

`RequestRefresh` 是 P4 → P2 的反向通道——deopt 风暴时 P4 想说「这份 feedback 已陈旧,请重新聚合」。**P2 初版可不实装**(P4 阶段才有意义,P2 PB6 mock + 真 P3 都不需要),接口预留。

**实装策略**(P4 阶段):

| 策略 | 实装难度 | 代价 | 选用 |
|---|---|---|---|
| (a) 立即重聚合 | 低 | RequestRefresh 阻塞数百 µs(同步 aggregate) | 简单形态,P4 初版可选 |
| (b) 异步标 stale | 中 | 需要 stale 标记字段 + 下次 IC 观测累积时触发 | 更解耦,推荐 |
| (c) 后台 worker 周期重聚合 | 高 | 需要后台 goroutine + 调度策略 | 过度设计,不推荐 |

**P2 实装时定**(本文不预设),接口形状不动。

### 4.3 P4 的典型调用流程

P4 阶段(未来)的典型调用流程:

```go
// internal/gibbous/jit —— P4 编译器的简化骨架
type Jit struct {
    fb P4Feedback // 注入 P2 的 *Bridge(*Bridge 实现 P4Feedback 接口)
    // ... 其它字段(自管栈 / trampoline / 后端发射器,详见 [../p4-method-jit])
}

// Compile:首次或 deopt 重编译
func (j *Jit) Compile(proto *bytecode.Proto) (GibbousCode, error) {
    // 1. 拉取最新 feedback(对 deopt 重编译,这是关键——读到的是更新后的 feedback)
    fb := j.fb.FeedbackFor(proto)

    // 2. 按 confidence 决定每个 IC 点的投机激进度
    //    - FBArithStableNumber + conf>=0.99 ⇒ 发 f64 投机模板 + guard
    //    - FBTableMono + conf=1.0 ⇒ 发直达槽投机 + guard
    //    - 其它 ⇒ 发通用模板(与 P3 形态等价,但跑在原生码段)
    code, err := j.emit(proto, fb)
    if err != nil {
        return nil, err
    }
    return code, nil
}

// OnDeopt:guard 失败回 P2,P4 自管 deopt 计数
func (j *Jit) OnDeopt(proto *bytecode.Proto, exitPC int32) {
    j.deoptCount[proto]++
    if j.deoptCount[proto] >= deoptThreshold {
        // 反复 deopt ⇒ 请求 P2 重聚合 feedback,然后重编译
        if j.fb.RequestRefresh(proto) {
            // P2 接受 ⇒ 等下次 considerPromotion 时按新 feedback 重编译
        }
        // 标 TierStuck-speculation,该 Proto 永久走通用模板
        j.markStuckSpeculation(proto)
    }
}
```

**关键点**:**P4 自管 deopt 计数与 TierStuck-speculation 标记**——P2 不参与这些 P4 内部状态。P2 提供的是**纯供料 + 重聚合通道**,P4 在此之上自管投机生命周期。

### 4.4 重聚合协议(P4 deopt 后)

P4 deopt 后请求重聚合的工作流:

```
P4 投机失败:                                  P2 重聚合:
┌──────────────────────┐                      ┌─────────────────────────┐
│ guard 失败            │                      │ P2 收到 RequestRefresh   │
│   ↓                  │                      │   ↓                     │
│ OSR exit 出 JIT 世界 │                      │ 标记 ProfileData.feedback│
│   ↓                  │                      │   为 stale(或立即重聚合)│
│ crescent 接管该帧    │                      │   ↓                     │
│   ↓                  │                      │ 下次 IC 写入累积一段后   │
│ 解释器继续跑         │                      │ aggregator 重新聚合     │
│   ↓                  │                      │   ↓                     │
│ IC 重新累积新观测    │ ──── 信号传递 ────►  │ CAS 装新 *TypeFeedback   │
│   ↓                  │                      │   到 ProfileData.feedback│
│ deopt 计数 +1        │                      │   ↓                     │
│   ↓                  │                      │ P4 下次 FeedbackFor      │
│ 计数过阈值           │                      │   读到更新后的 feedback   │
│   ↓                  │                      └─────────────────────────┘
│ 调 RequestRefresh    │
│   ↓                  │
│ 等下次升层时机重编译 │
└──────────────────────┘
```

**关键纪律**:

1. **P2 不主动重聚合**——只在 P4 显式 RequestRefresh 后才做(避免开销 + 简化时序);
2. **重聚合用 CAS 装新 feedback 指针**——旧指针仍可读(P3/P4 已拿到的快照不会被破坏);
3. **P4 重编译用最新 feedback**——经 FeedbackFor 取,读到的是 CAS 装好的新版本;
4. **P3 可读到陈旧 feedback 仍正确**——P3 不依赖 feedback 正确性(§1.1),无重训练需求。

> **P2 PB6 不实装重聚合**——本节接口形状只为 P4 阶段铺路,PB6 mock 与真 P3 都不需要。`RequestRefresh` 在 P2 初版返 `false` 占位,P3 不调,P4 阶段再填实。

### 4.5 P4Feedback 与 P3Compiler 的接口形态对比

| 维度 | `P3Compiler` | `P4Feedback` |
|---|---|---|
| 调用方向 | P2 调下游 | 下游(P4)反向读 P2 |
| 调用频率 | 每 Proto 升层时一次 | 每 Proto 编译时 + deopt 重编译时 |
| feedback 传递 | 入参一次性传 | 反向读多次 |
| 实现方 | P3(wazero)/ P4(原生)/ mock | P2 自己(*Bridge 实现) |
| 是否影响 P3 | 是(P3 必须实现 SupportsAllOpcodes / Compile) | 否(P3 不消费 P4Feedback) |
| 是否影响 P4 | 是(P4 实现 P3Compiler——P4 也是「拿 Proto 产可执行码」语义) | 是(P4 用此接口反向读) |
| 接口形态 | 顺向 + 一次性 | 反向 + 反复 |

**两条接口共同支持「共享前端」**:`P3Compiler` 让 P2 把 Proto 推下去(P3/P4 共用此接口);`P4Feedback` 让 P4 把 deopt 信号推上来(P3 不需要)。**接口形态不对称,反映 P3/P4 用法不对称**(§1.5)。

---

## 5. P4 用 feedback 的语义:核心,投机供料

### 5.1 「核心」的物理含义

P4 用 feedback 的「核心」是说:**P4 的投机机会完全由 feedback 决定**——feedback 说哪些点稳定,P4 就在那些点发投机模板;feedback 说不稳定的点,P4 老老实实发通用模板。

承 §1.3 的对比:**P4 必须依赖 feedback 正确性,但 guard + deopt 兜底确保正确性不损**。物理体现:

1. **feedback 决定模板选择**:`FBArithStableNumber, conf>=0.99` → f64 投机模板 + IsNumber guard;否则通用模板。
2. **guard 是兜底**:投机失败不出错,触发 OSR exit 回 P1 解释执行。
3. **deopt 后重训练**:解释执行期间 IC 重新积累 → P4 调 RequestRefresh → P2 重聚合 → P4 用新 feedback 再编译。

### 5.2 confidence 阈值的工程化设计

`confidence` 是 P4 决定「是否投机」的旋钮。承 [02-ic-feedback](./02-ic-feedback.md) §5.2 的 P2 端阈值(`stableArithThreshold = 0.99`),P4 端需要自己再设投机阈值:

| P4 投机阈值 | 物理含义 | 选用 |
|---|---|---|
| `confidence >= 0.99` | 极保守:只对最稳定点投机,失败率极低,但投机面窄 | P4 初版基线 |
| `confidence >= 0.95` | 中等:覆盖更多点,但 5% 失败率 ⇒ 偶尔 deopt | P4 实测调优 |
| `confidence >= 0.90` | 激进:覆盖大部分点,10% 失败率 ⇒ deopt 频率高 | 不推荐(deopt 风暴风险) |

> **P2 端阈值与 P4 端阈值的关系**:P2 端阈值(`>=0.99` 才标 `FBArithStableNumber`,`<0.99` 标 `FBUnstable`,见 [02-ic-feedback](./02-ic-feedback.md) §5.2)是「P2 视角下何为稳定」;P4 端阈值是「P4 视角下何为值得投机」。两者是串联的——P2 已经在 0.99 处把 unstable 滤掉了,P4 拿到的所有 `FBArithStableNumber` 都已经 ≥0.99,P4 自己再设阈值意义不大(除非 P4 想再卡严一些)。**P4 初版直接信 P2 的 kind,不再读 confidence** 是合理的简化——但接口仍允许 P4 读 confidence(详见 §1.4)。

### 5.3 例子:算术 IC 的 f64 投机模板

[../p4-method-jit](../p4-method-jit.md) §2.1 ADD 投机模板(简化):

```asm
;; P4 看到 feedback.points[pc].kind == FBArithStableNumber, conf=1.0
;; 发以下机器码模板(amd64 风格伪码):

  ; 加载操作数
  mov rax, [base + 8*B]            ; vb
  mov rcx, [base + 8*C]            ; vc

  ; IsNumber×2 guard(NaN-box 单比较,详见 [01] §3.2)
  cmp rax, NAN_THRESHOLD            ; vb < threshold?
  jae .deopt_pc                     ; 不是 number → OSR exit
  cmp rcx, NAN_THRESHOLD
  jae .deopt_pc                     ; 不是 number → OSR exit

  ; 双 number 快路径:f64.add
  movq xmm0, rax
  movq xmm1, rcx
  addsd xmm0, xmm1
  ucomisd xmm0, xmm0                ; NaN check
  jp .canon_nan                     ; canonicalize 路径
.continue:
  movq [base + 8*A], xmm0           ; 写回栈槽
  jmp .next_pc

.deopt_pc:
  ; OSR exit 回解释器:
  ; 1. 写 exitPC 到 CallInfo.savedPC(编译期常量)
  ; 2. 经 trampoline 出 JIT 世界
  ; 3. crescent 从 exitPC 续跑
  call $osr_exit_pc_<n>
```

**与 P3 的 ADD 翻译对比**:

| | P3 | P4 |
|---|---|---|
| IsNumber×2 失败后 | 内联调慢路径 helper(同函数内仍跑) | OSR exit 出 JIT 世界 |
| 慢路径在哪 | 同 Wasm 函数(if-else 分支) | 解释器(出函数边界) |
| 是否离开编译码 | 否 | 是 |
| feedback 错的代价 | 仅性能(慢路径仍正确) | 性能(deopt 开销 ~µs)+ 反复时拉黑投机 |

### 5.4 stableShape / stableIndex 直达槽投机

对 `FBTableMono` / `FBGlobalStable` / `FBSelfMono`,P4 用 stableShape / stableIndex 发直达槽投机:

```asm
;; P4 看到 feedback.points[pc].kind == FBTableMono
;; stableShape = SNAP_GEN, stableIndex = SNAP_SLOT

  mov rax, [base + 8*B]             ; t
  cmp gen(rax), SNAP_GEN            ; guard:同代次?
  jne .deopt_pc                     ; 失败 → OSR exit

  mov rcx, [t.array + 8*SNAP_SLOT]  ; 直达槽
  mov [base + 8*A], rcx
  jmp .next_pc

.deopt_pc:
  call $osr_exit_pc_<n>
```

**guard 失败的语义边界**(承 [../p4-method-jit](../p4-method-jit.md) §2.1 末):

> guard 只验证「投机前提」,验证失败**不是错误**,是回到全语义路径——guard **多判**(过于保守)只损性能,**漏判**(该查没查)直接产出错误结果且不崩溃,是 JIT 第一危险源。

P4 投机的 guard 是 P4 自己生成的,不在 P2 接口范围内——但 P2 提供的 stableShape/stableIndex 是 guard 检查的输入。**P2 必须保证这两个字段反映「实际命中过的快照」**,而非凭空数字——否则 P4 投机的 guard 永远失败,不符合「conf=1.0 的稳定点」预期。这条由 [02-ic-feedback](./02-ic-feedback.md) §2.1 的 P2 提取规则保证(stableShape/stableIndex 直接复制 ICSlot.shape/index,P1 写入时是真实命中)。

### 5.5 deopt 兜底与重训练

承 §4.4 重聚合协议,P4 投机失败后的工作流:

1. **guard 失败 → OSR exit**:[../p4-method-jit](../p4-method-jit.md) §3 物化 = memmove(栈槽真相不变式)。
2. **解释器接管 → IC 重新累积**:解释器跑该帧剩余指令,沿途的 IC 写入更新观测。
3. **deopt 计数 +1**:P4 自管,本 Proto 的 deopt 计数累积。
4. **过阈值 → 拉黑投机**:[../p4-method-jit](../p4-method-jit.md) §3.4「TierStuck-speculation」——该 Proto 永久只发无投机的通用模板(仍比解释快——dispatch 税照省)。
5. **可选:RequestRefresh + 重编译**:若 P4 判断「feedback 已陈旧」,调 P2 RequestRefresh,等下次 considerPromotion 时按新 feedback 重编译(失效的投机点降级为通用模板)。

**P2 在这个流程中的角色**:

- **接收 RequestRefresh 信号**(§4.2.2),按策略重聚合;
- **不参与 P4 的 deopt 计数 / 拉黑判定**——这些是 P4 内部状态;
- **不接收 OSR exit 通知**——OSR exit 是 P4 ↔ 解释器的事,P2 不卷入。

> **P2 的「投机生命周期」是空的**:P2 状态机([04] §2)只有 TierInterp/TierGibbous/TierStuck 三态,**TierGibbous 不区分「P3 编译的」与「P4 编译的」,也不区分「投机版本」与「通用版本」**。P4 在 TierGibbous 内部自己管投机状态(deopt 计数、拉黑、重训练),P2 只看「这 Proto 升过没」。这是**P2 是共享前端**的另一个体现——P2 不为 P4 单独建状态字段,P4 用自己的状态机叠在 P2 状态机之上。

### 5.6 P4 不依赖 P2 状态机的硬纪律

承 §0.3 接口稳定性,P4 实装时**不应**:

- ❌ 修改 P2 的 ProfileData.tierState 字段(P4 自管投机状态机,与 P2 状态机正交);
- ❌ 假设 P2 在 deopt 后自动降回 TierInterp(P2 单向状态机,P4 deopt 不触发降层);
- ❌ 通过 P2 接口拿到 P3 编译产物再修改(P4 自己生成原生码,与 P3 产物隔离)。

P4 实装时**应**:

- ✅ 通过 `P4Feedback.FeedbackFor` 反向读 feedback(本文 §4);
- ✅ 通过 `P3Compiler.Compile` 上交编译产物(P4 实现 P3Compiler,产 P4 版 GibbousCode);
- ✅ 自管 deopt 计数与 TierStuck-speculation 状态(P2 不参与);
- ✅ 通过 `RequestRefresh` 请求 P2 重聚合(本文 §4.2.2)。

> **P2 与 P4 解耦**:P2 的状态机只关心「升过没/卡死没」,P4 的投机状态完全自包含。这让 P2 PB6 mock 与真 P3 / P4 三阶段切换零修改 P2 实装(§0.3)。

---

## 6. `GibbousCode` 抽象类型

`GibbousCode` 是 P3/P4 编译产物的共同抽象——P3 = wazero 模块,P4 = 原生机器码,但 P2 看到的是统一接口。

### 6.1 接口定义

```go
// internal/bridge —— 编译产物的共同抽象。
// P3 实现:wazero compiled module + 入口函数句柄(internal/gibbous/wasm/code.go)
// P4 实现:原生机器码段 + trampoline(internal/gibbous/jit/code.go)
// mock 实现:dummy 占位(internal/bridge/mock/code.go,§7)
type GibbousCode interface {
    // Run 进入编译码执行该帧。
    //
    // 调用方:[04] installGibbous 后,crescent 的 doCall(05 §7.1)在
    //   detect 到 callee 的 tierState == TierGibbous 时调用([../p3-wasm-tier/04-trampoline]
    //   §5.2 trampoline 协议)。
    //
    // 入参:
    //   - state:当前 State,持 thread/arena/CallInfo 等运行期上下文;
    //   - base:本帧 R0 在 thread.valueStack 的字节偏移
    //     (P3 的 trampoline 入参,[../p3-wasm-tier/04-trampoline] §2)。
    //
    // 返回:
    //   - status:0=OK(返回值已回填 R(A..)),1=ERR(state.pendingErr 已置);
    //     2=DEOPT(P4 OSR exit;P3 永远不返回 2)。
    //
    // 实现方契约:
    //   - 同步调用语义(返回时该帧已执行完);
    //   - 调用前 P2 已压 CallInfo 标 bit50 gibbous(05 §1.2);
    //   - 调用方负责弹 CallInfo(本接口不弹)。
    Run(state *State, base int32) int32

    // Dispose 释放编译码占用的资源。
    //
    // 调用方:Program 销毁 / GC、Proto 不再被引用、State 关闭等。
    // 实现方契约:
    //   - 幂等:多次调用安全;
    //   - 释放后 Run 不应被调用(由调用方守);
    //   - 释放 wazero compiled module / 原生码段 mmap / trampoline 等。
    Dispose() error

    // GetTrampoline 返回该编译码的入口 trampoline(crescent → gibbous 进入点)。
    //
    // 用法:installGibbous 时把 trampoline 装到 Proto 旁的 gibbous 索引表,
    //   crescent 的 doCall 直接 call trampoline,不需要在每次调用时重查 GibbousCode。
    //
    // P3 实现:wazero api.Function(经其 Call 方法即可触发执行);
    // P4 实现:原生 trampoline 入口地址(asm stub,详见 [../p4-method-jit] §4.2)。
    //
    // 返回:统一抽象为 unsafe.Pointer——P2 不解读其内容,只是透传给
    //   crescent 的 doCall 路径。
    GetTrampoline() unsafe.Pointer
}
```

### 6.2 P3 实现:wazero compiled module

P3 端的 `GibbousCode` 实现:

```go
// internal/gibbous/wasm —— P3 的 GibbousCode 实现
type p3Code struct {
    module wazero.CompiledModule    // wazero 编译产物
    fn     api.Function              // 入口函数句柄
    proto  *bytecode.Proto           // 反查用(诊断/日志)
    icSnap []icSnapshot              // §3.4 提到的固化 IC 快照(仅诊断用)
}

func (c *p3Code) Run(state *State, base int32) int32 {
    // 进入 wazero 执行(一次跨层,目标 <150ns,详见 [../p3-wasm-tier/01-spike-gate] §1)
    results, err := c.fn.Call(state.ctx, uint64(base))
    if err != nil {
        // wazero 内部错误(罕见)→ 设 pendingErr 返 ERR
        state.pendingErr = err
        return 1
    }
    return int32(results[0]) // status 由 Wasm 函数返回
}

func (c *p3Code) Dispose() error {
    return c.module.Close(context.Background())
}

func (c *p3Code) GetTrampoline() unsafe.Pointer {
    // P3 的 trampoline 是 wazero api.Function 句柄(由 wazero 自管入口 stub)
    return unsafe.Pointer(&c.fn)
}
```

### 6.3 P4 实现:原生机器码段

P4 端的 `GibbousCode` 实现(简化骨架,详见 [../p4-method-jit](../p4-method-jit.md) §4):

```go
// internal/gibbous/jit —— P4 的 GibbousCode 实现
type p4Code struct {
    codeSeg    []byte                // mmap 出来的可执行段(W^X 切完态)
    entry      uintptr                // 入口地址(asm trampoline stub)
    osrExits   []osrExitInfo          // OSR exit 着陆点表(deopt 重建栈状态用)
    proto      *bytecode.Proto
}

func (c *p4Code) Run(state *State, base int32) int32 {
    // 经 asm trampoline 进入 JIT 世界(详见 [../p4-method-jit] §4.2)
    return jitTrampolineEnter(c.entry, state, base)
}

func (c *p4Code) Dispose() error {
    // munmap + 释放 OSR exit 表
    return munmap(c.codeSeg)
}

func (c *p4Code) GetTrampoline() unsafe.Pointer {
    return unsafe.Pointer(c.entry) // 原生入口地址
}
```

### 6.4 GibbousCode 的释放协议

`Dispose` 的契约要点:

1. **幂等**:多次调用不出错(实现方用 atomic.CompareAndSwap 或 sync.Once 守);
2. **释放后 Run 不可调**:由调用方(P2 / crescent doCall)守纪律,接口不强制运行期检测;
3. **多 State 共享 Proto 时的释放时机**:Proto 的 GibbousCode 由 Program 持有(GibbousCode 与 Proto 同生命期);Program 销毁时 GibbousCode 自动 Dispose。

> **多 State 共享 Proto 下的细节**(承 §0.4 边界表):若 Program 同时被多个 State 并发使用,GibbousCode 是只读共享对象——任何 State 都可调 Run,但 Dispose 只在 Program 销毁时调一次。这要求实现方的 Run 是**线程安全**的(P3 wazero Module Call 已经线程安全;P4 原生码段是只读 + per-call 状态隔离,也是线程安全)。**这是已知设计点,留 §11 缺口跟踪**。

### 6.5 GibbousCode 与 Proto 的关联

P2 在 [04] `installGibbous` 时把 `GibbousCode` 关联到 Proto:

```go
// internal/bridge —— installGibbous 实装(由 [04] §3 调用)
func (b *Bridge) installGibbous(proto *bytecode.Proto, code GibbousCode) {
    // 1. 装入 Program 的 gibbousIndex(per-Proto 编译码索引表)
    b.program.gibbousIndex[proto.ID] = code

    // 2. 标 ProfileData.tierState = TierGibbous([04] §2 状态转移)
    b.profileOf(proto).tierState = TierGibbous

    // 3. 写 CallInfo bit50 标记(05 §1.2 callStatus_gibbous)——
    //    在 doCall 检测 callee 已升 gibbous 时压 CallInfo 时设;
    //    本函数不直接写 CallInfo(它由 doCall 的下一次调用触发)。

    // 4. 诊断日志([04] §6 格式)
    b.diag.Logf("function %s promoted to gibbous", proto.Name)
}
```

**关键纪律**:

- **GibbousCode 一旦装入,P2 不再 dispose**——直到 Program 销毁;
- **多 State 共享 GibbousCode**——每 State 都能调同一个 Run;
- **TierStuck 的 Proto 不持 GibbousCode**——installGibbous 不被调,gibbousIndex[proto.ID] 留 nil。

---

## 7. mock P3 实现(PB6 引入)

承 [00-overview](./00-overview.md) §4 PB6 验收:**「mock P3 接受 Proto+feedback 返回 dummy GibbousCode;接口稳定性单测」**。本节给完整代码骨架。

### 7.1 mock 的目的:让 PB7 端到端验收不依赖真 P3

P2 PB7([00-overview](./00-overview.md) §4)是「P2 总验收」——可编译性零误判 fuzz / crescent-only vs P2-on-crescent byte-equal / 升层日志匹配 / 多 State 并发 `-race`。**这些验收都不需要真 P3 编译产出加速**——只需要「能升层 + 接口走通 + Proto 跑了一次假编译」即可。

**mock P3 就是这个「假编译」实现**:

- 输入:Proto + feedback;
- 输出:dummy GibbousCode(Run 内部直接调 crescent 解释器跑该帧,不真编译);
- 错误返回路径可控制(测试用例可让某些 Proto 编译失败);
- `SupportsAllOpcodes` 可控制(测试用例可让某些 opcode 「不支持」走 F7)。

**这让 PB7 验收可在真 P3 实现完成前完成**——P2 PB6/PB7 与 P3 实现解耦,各自独立交付(原则 3「每阶段独立交付」)。

### 7.2 mock 完整代码骨架

```go
// internal/bridge/mock —— mock P3 实现,供 PB6/PB7 端到端验收用。
//
// 设计原则(§7.4):语义中性——mock 不引入与真 P3 不同的隐式行为,
// 只是把「编译」这一步替换为「装个空壳,Run 时调解释器」。
package mock

import (
    "errors"
    "sync/atomic"
    "unsafe"

    "github.com/<...>/wangshu/internal/bridge"
    "github.com/<...>/wangshu/internal/bytecode"
)

// Compiler 实现 bridge.P3Compiler 接口。
type Compiler struct {
    // —— 测试用控制旋钮(可选,默认全允) ——
    supportFn func(op uint8) bool          // 控制 SupportsAllOpcodes;nil ⇒ 全允
    failFn    func(p *bytecode.Proto) error // 控制 Compile 失败;nil ⇒ 全成
    // —— 诊断 ——
    compiledCount atomic.Uint64             // 已编译 Proto 数(测试断言用)
}

// New 构造一个全允的 mock 编译器(默认形态)。
func New() *Compiler { return &Compiler{} }

// NewWithSupport 构造按 op 过滤的 mock(测试 F7 闸门用)。
func NewWithSupport(supportFn func(op uint8) bool) *Compiler {
    return &Compiler{supportFn: supportFn}
}

// NewWithFail 构造按 Proto 注入失败的 mock(测试编译失败 fallback 用)。
func NewWithFail(failFn func(p *bytecode.Proto) error) *Compiler {
    return &Compiler{failFn: failFn}
}

// SupportsAllOpcodes 实现 bridge.P3Compiler。
//
// 默认行为:全允(返回 true);
// 注入 supportFn 后:扫一遍 Proto.Code,任一 op 不支持即 false。
func (c *Compiler) SupportsAllOpcodes(p *bytecode.Proto) bool {
    if c.supportFn == nil {
        return true                              // 默认全允
    }
    for _, ins := range p.Code {
        op := bytecode.OpcodeOf(ins)
        if !c.supportFn(op) {
            return false
        }
    }
    return true
}

// Compile 实现 bridge.P3Compiler。
//
// 默认行为:产 dummyCode(Run 调解释器);
// 注入 failFn 后:按其返回值决定是否失败。
func (c *Compiler) Compile(p *bytecode.Proto, fb *bridge.TypeFeedback) (bridge.GibbousCode, error) {
    // 1. 注入失败路径(测试 fallback)
    if c.failFn != nil {
        if err := c.failFn(p); err != nil {
            return nil, err                       // 透传 ErrCompileResource 之类
        }
    }
    // 2. 默认成功:产 dummy 编译产物
    c.compiledCount.Add(1)
    return &dummyCode{
        proto:    p,
        feedback: fb,                             // 留存供测试断言
    }, nil
}

// CompiledCount 返回已 mock 编译的 Proto 数(测试断言:升层次数符合预期)。
func (c *Compiler) CompiledCount() uint64 {
    return c.compiledCount.Load()
}

// dummyCode 实现 bridge.GibbousCode。
//
// 「假编译」语义:Run 内部不跑真 Wasm,而是回调 P2 的 fallback path
// 让解释器跑该帧——这保证 mock 模式下结果与「不升层」完全一致(§7.4 语义中性)。
type dummyCode struct {
    proto    *bytecode.Proto
    feedback *bridge.TypeFeedback                // 留存供 §7.5 断言:接到了正确的 feedback
    disposed atomic.Bool                          // 防 use-after-dispose
}

// Run 调解释器跑该帧——mock 的核心:不真编译,转给 crescent。
//
// 注意:本接口的入参 (state, base) 与真 P3 / P4 一致(§6.1),
// mock 通过 state.bridge.RunInterpreted 路由回解释器(详见 §7.3 注入点)。
func (c *dummyCode) Run(state *bridge.State, base int32) int32 {
    if c.disposed.Load() {
        panic("mock: GibbousCode used after Dispose")
    }
    // 转给解释器执行(等价 fallback 路径,但走 gibbous 接口)
    return state.RunInterpretedAt(c.proto, base)
}

// Dispose 实现 bridge.GibbousCode。
func (c *dummyCode) Dispose() error {
    c.disposed.Store(true)
    return nil
}

// GetTrampoline 返回 dummy 入口指针(测试不解读,只比对非 nil)。
func (c *dummyCode) GetTrampoline() unsafe.Pointer {
    return unsafe.Pointer(c)                     // 自身指针即可,非 nil 标识「已装」
}
```

### 7.3 mock 的注入点

P2 在测试模式下通过依赖注入装入 mock:

```go
// internal/bridge —— Bridge 构造支持注入 P3Compiler
func NewBridge(opts BridgeOpts) *Bridge {
    return &Bridge{
        p3: opts.P3,                              // 默认 nil → P1-only 部署
        // ... 其它字段
    }
}

// 测试启动时:
//   mockP3 := mock.New()
//   bridge := bridge.NewBridge(BridgeOpts{P3: mockP3})
//   ... 跑测试脚本,断言 mockP3.CompiledCount() == expected
```

`Bridge.p3` 字段类型是 `P3Compiler` 接口——测试装 mock,生产装真 P3,**P2 实装代码完全不区分二者**。这是「共享前端,接口稳定」(§0.3)在测试维度的兑现。

### 7.4 「语义中性」纪律

mock 的最高纪律:**不引入与真 P3 不同的隐式行为**。具体:

| 维度 | mock 行为 | 真 P3 行为 | 是否一致 |
|---|---|---|---|
| SupportsAllOpcodes 的过滤集 | 默认全允 / 注入按 op 过滤 | 按 P3 后端实际支持集 | 行为同构(注入 supportFn 模拟真 P3 的支持集) |
| Compile 入参 feedback | 容忍 nil(透传给 dummyCode) | 容忍 nil(退化通用翻译) | 一致 |
| Compile 返回错误 | 默认成功 / 注入按 Proto 失败 | 按资源/panic 失败 | 行为同构 |
| Run 的语义 | 转给解释器跑(即「假编译」) | 跑 wazero 编译产物 | **结果一致**(对所有合法输入产相同结果——这是 P3 的逐字节差分承诺,mock 直接复用解释器结果保证) |
| Dispose 的语义 | 标记 disposed | 释放 wazero compiled module | 行为同构 |

**关键不变式**:**mock 的 Run 永远不引入与解释器不同的副作用**——它就是解释器的一层薄包装。这让 PB7 端到端验收里「crescent-only vs P2-on-crescent (mock)」差分**自然 byte-equal**(因为 P2-on-crescent 的「升层」最后还是跑解释器,只是经过了 P2 的状态机)。

> **mock 不能做的事**:① 修改 Proto 字段(只读);② 修改 feedback 字段(只读);③ 在 Run 中跑除解释器外的任何执行路径(防止引入新行为);④ 在 Compile 中分配大量内存或耗时(mock 应是轻量的)。**这些纪律由代码 review 守**,不在接口运行期强制。

### 7.5 mock 的测试断言场景

mock 在 PB7 测试套里的典型用法([06-testing-strategy](./06-testing-strategy.md) 待补):

| 测试场景 | mock 配置 | 断言 |
|---|---|---|
| 升层时机正确 | 默认 mock | 跑某热脚本 → `mockP3.CompiledCount()` 等于预期(只有可编译且热的 Proto 升层) |
| F7 闸门生效 | `NewWithSupport(op != OpVararg)` 等 | 含 vararg 的 Proto **不**调 Compile(F1 已拦);含某「不支持」op 的 Proto 经 F7 拦下 |
| 编译失败 fallback | `NewWithFail(...)` 注入资源耗尽 | 该 Proto 标 TierStuck;后续不再调 Compile |
| feedback 正确传递 | 默认 mock | dummyCode 留存的 feedback 与 P2 聚合产物逐字段一致 |
| 多 State 并发 | 默认 mock + 多 goroutine | `-race` 通过;CompiledCount 累积合理 |

---

## 8. P3 上线时换 mock 的协议

### 8.1 接口零修改保证

PB6/PB7 完成后(mock 落地、P2 验收通过),真 P3 实现上线时:

1. **`internal/gibbous/wasm.Compiler` 实现 `bridge.P3Compiler` 接口**——同 mock 一样的接口签名;
2. **`Bridge` 构造时注入真 P3**(替换 mock):`bridge.NewBridge(BridgeOpts{P3: wasm.NewCompiler(rt)})`;
3. **P2 实装代码完全不动**——`considerPromotion` / `installGibbous` / 状态机转移、所有逻辑都基于 `P3Compiler` 接口而非具体类型。

这是「共享前端」在 P3 上线时的物理兑现:**P2 端零修改,只是 P3Compiler 实现从 mock 变成真编译器**。

### 8.2 P2 测试套对真 P3 的复用

mock 写好的测试套(§7.5)对真 P3 也适用:

| 测试 | mock 模式 | 真 P3 模式 |
|---|---|---|
| 升层时机 | mockP3.CompiledCount() | 真 P3 内部计数 + Bridge.diag 升层日志 |
| F7 闸门 | NewWithSupport 注入过滤 | 真 P3 的 supported 表(初期渐进扩充) |
| 编译失败 | NewWithFail 注入失败 | 真 P3 的资源耗尽测试(用极小 wazero memory limit) |
| feedback 传递 | dummyCode 字段比对 | 真 P3 编译产物的 IC 快照与 feedback 一致 |
| byte-equal 差分 | mock Run 调解释器,自然一致 | crescent vs gibbous 逐字节差分([../p3-wasm-tier/08-testing-strategy](../p3-wasm-tier/08-testing-strategy.md) §2) |

**Bridge 测试 + mock P3 → Bridge 测试 + 真 P3,测试代码不变**,只是 P3Compiler 实现切换。这种「测试可移植性」是接口稳定的副产品——同一份测试通过两种实现都能跑。

### 8.3 切换时机与并行存活

真 P3 上线初期,mock 不立即移除:

- **mock 留作测试用**:即便有真 P3,某些 P2 单元测试用 mock 更快(避免 wazero 启动开销);
- **mock 留作 fallback build**:`!gibbous` build tag 下编译,允许「P2-only」嵌入式部署(没有真 P3,但 P2 仍能验证可编译性 + 永久解释 fallback 路径)。

**mock 与真 P3 永久并存**——前者是测试 + 极简部署,后者是生产加速。两者实现同一个 `P3Compiler` 接口,P2 端透明。

---

## 9. 共享前端的版本演进协议

接口稳定不等于接口冻结——P2 实装期可能发现 `TypeFeedback` shape 需要扩展,本节给版本演进的兼容策略。

### 9.1 `TypeFeedback` 字段追加(向后兼容)

承 [02-ic-feedback](./02-ic-feedback.md) §9.1-§9.4 的潜在扩展:表 IC 命中计数 / LT/LE 子分支区分 / megamorphic 主动识别。这些扩展若启用,会给 `PointFeedback` 加字段:

```go
// 可能的演进方向(参考 [02-ic-feedback] §9):
type PointFeedback struct {
    pc           int32
    kind         FeedbackKind
    confidence   float32
    stableShape  uint32
    stableIndex  uint32
    observations uint32
    // —— 演进字段(可能在 P2+ 加) ——
    tableHits    uint32  // §9.1:表 IC 命中计数(若加,P3/P4 可读)
    cmpKind      uint8   // §9.2:LT/LE 子分支(0=number, 1=string)
    // ... 其它演进字段
}
```

**兼容策略**:

1. **新字段追加在末尾**:不动现有字段顺序与语义,P3/P4 旧版本读到不识别字段直接忽略(Go 结构体读字段是按名字,新字段对旧消费方不可见,**自动兼容**)。
2. **零值即「无信息」**:新字段未填时取零值,P3/P4 对零值与「字段不存在」一视同仁(`tableHits == 0` 当作 P1 没记命中数处理)。
3. **不删除字段**:即便某字段实测无用,也保留(避免 P3/P4 老版本编译失败)。

### 9.2 `FeedbackKind` 枚举追加(向后兼容)

`FeedbackKind` 是 uint8 枚举([02-ic-feedback](./02-ic-feedback.md) §4.3),可能追加:

```go
const (
    FBUnstable          FeedbackKind = iota  // 0
    FBArithStableNumber                       // 1
    FBTableMono                               // 2
    FBTableMega                               // 3
    FBGlobalStable                            // 4
    FBSelfMono                                // 5

    // —— 演进追加(零或多) ——
    FBArithStableString                       // 6:LT/LE 字符串稳定([02] §9.2)
    FBTableMonoWithChain                      // 7:深 __index 链稳定(P5 用)
    // ...
)
```

**兼容策略**:

1. **追加只增不改**:已有枚举值的数值与语义永不变(否则破坏旧 P3 / P4 二进制);
2. **`Unknown` 兜底**:P3/P4 见到不识别的 FeedbackKind(数值超出已知范围),**视同 `FBUnstable`**(保守:不投机、不紧凑翻译)。
3. **代码里用 switch 处理**:消费方用 `switch fb.kind` 处理已知值,default 落到 `FBUnstable` 处理逻辑——**这条由 review 守,不在接口强制**。

```go
// P3/P4 消费方典型骨架(向后兼容版本演进)
switch fb.kind {
case bridge.FBArithStableNumber: emitArithFastPath(...)
case bridge.FBTableMono:         emitTableMonoFastPath(...)
case bridge.FBTableMega:         emitTableMega(...)
case bridge.FBGlobalStable:      emitGlobalStable(...)
case bridge.FBSelfMono:          emitSelfMono(...)
default:                          emitGeneric(...)  // FBUnstable + 任何未识别值
}
```

**这种「default 兜底」机制保证旧 P3/P4 二进制可以在新 P2 上正确运行**——只是失去新 FeedbackKind 带来的优化机会。

### 9.3 `P3Compiler` 接口形状的演进

`P3Compiler` 接口本身的演进策略:

1. **方法签名不变**——已有方法的入参 / 返回值类型永不变;
2. **新方法可加**——若 P4/P5 阶段需要新协议(如「请求 deopt 通知」),通过**接口嵌入**或**可选接口**实现:

```go
// 例:未来若 P5 需要 deopt 通知,P3Compiler 自身不变,新增可选接口
type P3DeoptNotifier interface {
    OnDeopt(proto *bytecode.Proto, exitPC int32)
}

// P2 端:
if notifier, ok := b.p3.(P3DeoptNotifier); ok {
    notifier.OnDeopt(proto, exitPC)
}
```

3. **错误常量可加,不可删**——旧错误类型永久保留,新增类型作为新错误码并存。

### 9.4 版本演进的反模式

**禁止**:

- ❌ 修改已有字段的语义(如把 `confidence` 从 [0, 1] 改成 [0, 100]);
- ❌ 删除已有 FeedbackKind 数值(`FBSelfMono = 5` 永远是 5,即便不再用);
- ❌ 改 `P3Compiler.Compile` 入参签名(加新参用 `Options struct` 兼容);
- ❌ 在已有方法上加返回值(同上,用 struct);
- ❌ 跨字段交叉语义(新字段语义依赖旧字段值,导致旧消费方误读)。

**允许**:

- ✅ `PointFeedback` / `TypeFeedback` 末尾追加字段;
- ✅ `FeedbackKind` 枚举末尾追加新值;
- ✅ `P3Compiler` 通过可选接口加新协议;
- ✅ 错误常量追加(旧消费方用 `errors.Is` 兼容);
- ✅ 文档级别的语义澄清(不改代码)。

> **演进的代价是接口稳定性**——每次追加字段 / 枚举值都增加心智负担(P3/P4 实现方需要决定是否消费新字段)。**演进只在实测明确收益后启动**(典型触发条件:某负载类的 P4 投机失败率高,需要表 IC 命中计数判稳定度)。**当前 P2 PB6 不预设任何演进,接口冻结到 §2 / §4 / §6 的形状**——演进留 P2+ / P4 阶段。

---

## 10. 不变式清单

本文涉及的接口约束,实现者与 review 时自检:

| # | 不变式 | 兑现处 |
|---|---|---|
| **N1** 接口稳定 | `P3Compiler` / `P4Feedback` / `GibbousCode` 接口签名定稿后,P2 实装零修改;P3/P4 切换 mock ↔ 真实现仅替换实现,P2 代码不动 | §0.3 / §8.1 |
| **N2** P3 不依赖 feedback | P3 编译产物对所有合法输入正确,即便 feedback 为 nil 或错误;feedback 只影响代码形状,不影响语义覆盖面 | §1.1 / §3.3 |
| **N3** P4 依赖 feedback 但 guard 兜底 | P4 投机基于 feedback,投机失败 ⇒ OSR exit 回解释器(慢但不错);feedback 错最坏只是反复 deopt 拉黑投机,不损正确性 | §1.3 / §5.5 |
| **N4** P4 不依赖 P2 状态机 | P4 自管 deopt 计数与 TierStuck-speculation;P2 状态机([04] §2)只看「升过没/卡死没」,不区分 P3 / P4 编译产物 | §5.6 |
| **N5** mock 语义中性 | mock 不引入与真 P3 不同的隐式行为;Run 转给解释器,无新副作用 | §7.4 |
| **N6** 共享前端不为单一阶段定制 | P2 产 feedback 时不知道下游是 P3 / P4 / P5,统一对称产出;消费方按各自策略不对称消费 | §1.5 |
| **N7** panic 不穿透接口 | `P3Compiler.Compile` 实现方 recover 转 error,P2 状态机不被实现 bug 拽倒 | §2.2.2 |
| **N8** GibbousCode 是只读共享 | 多 State 并发调 Run 安全;Dispose 由 Program 销毁触发,只一次 | §6.4 |
| **N9** 版本演进向后兼容 | TypeFeedback / FeedbackKind / P3Compiler 接口的演进只追加,不修改不删除;旧消费方读到新字段 / 新枚举值 default 落 unstable / generic | §9.1-§9.4 |
| **N10** 接口零修改保证测试可移植 | 同一份测试套对 mock 与真 P3 都跑通(切实现不切代码);PB7 验收通过后 P3 上线零回归 | §8.2 |

---

## 11. 文档缺口

承 [00-overview](./00-overview.md) §9 风险清单,本文涉及的二阶细节可能在实装期浮现,记入 [doc-gaps](../../../llmdoc/memory/doc-gaps.md) 待 P2/P3/P4 真实开发时校准。

| # | 缺口 | 触发条件 | 计划处理 |
|---|---|---|---|
| GAP-1 | **P4 投机激进度的实测调标** | P4 阶段实装后用真实负载实测 | confidence 阈值(§5.2)、deopt 阈值、TierStuck-speculation 标准——P4 阶段定;不影响正确性 |
| GAP-2 | **GibbousCode 释放协议在多 State 共享下的细节** | 多 State 并发使用同一 Program | Dispose 时机、Run 的并发安全契约的形式化测试;参见 §6.4 末尾 |
| GAP-3 | **`RequestRefresh` 的 P2 实装策略**(§4.2.2) | P4 阶段才实装;P2 PB6 不需要 | (a) 立即重聚合 / (b) 异步标 stale / (c) 后台 worker 三选一,P4 阶段定 |
| GAP-4 | **TypeFeedback 字段追加触发条件**(§9.1) | P4 实测发现表 IC 命中计数粒度不够 / LT/LE 区分缺失 | 演进 PR 上线时一并补,本文 §9 提供兼容策略 |
| GAP-5 | **P3 / P4 同时存活时的优先级**([架构演进:P3 与 P4 并存] | P4 上线、P3 退役 / 留作中层未定 | 见 [../roadmap.md](../roadmap.md) §4 P4 验收末尾「Wasm 层退役,或留作可移植中层」;若并存,需要决定 considerPromotion 优先调谁——本文不预设,留 P4 上线时决策 |
| GAP-6 | **mock P3 在 PB7 之后的归属** | 真 P3 上线后 | mock 留作测试 + `!gibbous` build fallback,**永久并存**(§8.3);若实测 mock 无用再删 |
| GAP-7 | **跨 P3 版本升级的 stuck 重评估**(承 [00-overview](./00-overview.md) §6 决策表) | P3 后端能力跨版本扩展(实现新 opcode) | TierStuck 的 Proto 是否在 P3 升级后重新评估?当前不重评估(§9.3 接口稳定不破坏 P2 状态机);留 P2+ |
| GAP-8 | **interface 选择 vs 具体类型的测试可观察性** | mock 与真 P3 切换时,测试断言粒度可能不一致 | mock 暴露 CompiledCount 等钩子是侵入式;真 P3 通过 diag.Logf 暴露——两套测试断言可能需要分别写。**接受双轨测试**,接口本身不变 |

---

## 12. 对上游(P3 / P4)文档的回填请求

### 12.1 回填请求节(P3-wasm-tier 与 P4-method-jit)

**这些回填请求在主助理任务 #12 阶段统一兑现**,本文先列出明细。

| # | 文档 | 节 | 内容 |
|---|---|---|---|
| RB-1 | `../p3-wasm-tier/02-translation.md` | §5(P3Compiler 接口形状) | 把 P3Compiler 实装的「实现方契约」(本文 §2.2)与本文同源化;`SupportsAllOpcodes` / `Compile` 的错误返回语义、并发语义、panic 兜底纪律以本文 §2 为准 |
| RB-2 | `../p3-wasm-tier/06-ic-feedback-consume.md` | 全文(IC 与 TypeFeedback) | 与本文 §3「P3 用 feedback 的语义」对齐;明确 P3 不读 confidence 是常态,即便读 confidence 也只用于代码形状决策;feedback 错的容忍性来自本文 §1.1 |
| RB-3 | `../p3-wasm-tier/04-trampoline.md` | §2(crescent → gibbous 入口) | 与本文 §6.2 P3 GibbousCode 实现对齐;`fn.Call` 入参与 Run(state, base) 一致,status 0/1 返回值语义同本文 §6.1 |
| RB-4 | `../p3-wasm-tier/implementation-progress.md` | §2 缺口 | 把「mock P3 与真 P3 并存」(本文 §8.3)记入 P3 文档缺口;mock 留作测试 + fallback build |
| RB-5 | `../p4-method-jit.md` | §2.1(供料链) | 与本文 §1.3 / §5 对齐:P4 通过 `P4Feedback.FeedbackFor`(本文 §4)反向读 feedback,而非 Compile 入参;deopt 后调 RequestRefresh |
| RB-6 | `../p4-method-jit.md` | §3.4(deopt 后再训练) | 与本文 §5.5 对齐:P4 自管 deopt 计数,过阈值后调 P2 RequestRefresh 触发重聚合;P2 状态机不参与 P4 投机生命周期 |
| RB-7 | `../p4-method-jit.md` | §4(系统管线) | P4 也实现 `bridge.P3Compiler` 接口(本文 §0.4 边界表「实现方」一行)——这是「共享前端」的物理体现:P4 也接受 Proto + feedback,产出 GibbousCode;只是 GibbousCode 的实现是原生码段而非 wazero module(§6.3) |
| RB-8 | `../p4-method-jit.md` | §X(deopt 通知协议) | 若未来需要 P2 收到 P4 deopt 通知(用于诊断/重聚合策略),通过**可选接口** `P3DeoptNotifier`(本文 §9.3)添加,不动 P3Compiler 主接口 |

### 12.2 P2 内部协调请求(本卷其他文档)

| # | 文档 | 节 | 内容 |
|---|---|---|---|
| RB-9 | [00-overview](./00-overview.md) | §6 决策表「P3 用 feedback / P4 用 feedback」两行 | 引用本文 §1 / §3 / §5 作为单一事实源 |
| RB-10 | [04-try-compile-fallback](./04-try-compile-fallback.md) | §3 considerPromotion 入口 | considerPromotion 调用本文 §2 P3Compiler.Compile,错误返回时按本文 §2.3 错误常量分类记诊断日志 |
| RB-11 | [02-ic-feedback](./02-ic-feedback.md) | §4.5 ProfileData.feedback 槽 | 本文 §4 P4Feedback 接口是该字段的反向读契约;`installFeedback` 用 atomic.CAS 与本文 §4.2.1 配套 |
| RB-12 | [06-testing-strategy](./06-testing-strategy.md) | §X mock P3 测试套 | 本文 §7.5 mock 测试场景全部进入 PB7 验收单测 |

### 12.3 接口稳定性的兑现承诺

承 §0.3 「P2 实现一旦定稳,P3/P4/P5 三阶段共用,接口零修改」,本文给 P3/P4 的回填请求**全部不要求改动 P3 / P4 文档的核心接口形状**——只是要求**把现有 P3/P4 文档中关于接口形状的零散描述指向本文作为单一事实源**。这是接口稳定性的硬兑现:

- P3 的 `Compiler` 类型继续实现 `bridge.P3Compiler`(无需改 P3 文档的接口签名);
- P4 的 `Jit` 类型继续实现 `bridge.P3Compiler` + 反向读 `bridge.P4Feedback`(无需改 P4 文档的接口签名);
- 演进字段追加(§9)只在实测后启动,不预先在 P3/P4 文档中预留。

> **回填请求的实质是「文档单一事实源化」**,不是接口改动——这是「共享前端」在文档维度的体现:接口形状统一定义在本文,P3/P4 文档引用本文,**避免接口形状在多篇文档间漂移**。

---

## 13. 相关

- [00-overview](./00-overview.md)(P2 总览,§0 文档地图 / §6 决策表 P3 P4 用 feedback / §9 风险)
- [01-profiling](./01-profiling.md)(profile 计数子系统;ProfileData.feedback 字段挂载点)
- [02-ic-feedback](./02-ic-feedback.md)(TypeFeedback shape 与 confidence 计算的单一事实源,本文是消费侧对接)
- [03-compilability-analysis](./03-compilability-analysis.md)(F7 P3 后端能力 / `SupportsAllOpcodes` 接口本文给出完整定义)
- [04-try-compile-fallback](./04-try-compile-fallback.md)(状态机调本文 P3Compiler.Compile;升层日志格式)
- [06-testing-strategy](./06-testing-strategy.md)(PB6/PB7 mock P3 测试套验收口径)
- [../p2-bridge/00-overview](./00-overview.md) §7(本文种子,P2 单文件原稿)
- [../p3-wasm-tier/00-overview](../p3-wasm-tier/00-overview.md)(P3 总览;02-translation P3Compiler 实现 + 04-trampoline GibbousCode P3 形态)
- [../p4-method-jit](../p4-method-jit.md)(P4 投机消费 P4Feedback;P4 也实现 P3Compiler;deopt 与重训练)
- [../p1-interpreter/01-value-object-model.md](../p1-interpreter/01-value-object-model.md) §5.7(Proto 字段——P3/P4 接到的 Proto 是什么)
- [../p1-interpreter/05-interpreter-loop.md](../p1-interpreter/05-interpreter-loop.md) §1.2(CallInfo bit50 callStatus_gibbous,P3 trampoline 入口判定)
- [../p1-interpreter/11-embedding-arena-abi.md](../p1-interpreter/11-embedding-arena-abi.md) §1.3(Compile 占位填实)
- [../roadmap.md](../roadmap.md) §4 P4「继承 P3 全部分层结构,只换发射后端」(共享前端的关键论据)
- [../../../llmdoc/must/design-premises.md](../../../llmdoc/must/design-premises.md)(原则 4 fallback 不做完备性 / 原则 3 每阶段独立交付)

