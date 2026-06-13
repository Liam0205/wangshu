# P3 实现进度对账(implementation-progress)

> 状态:**P3 详细设计已齐备(2026-06-13 单文件 380 行 → 子目录 9 文件约 8800 行扩展轮),实现未启动**。P1 全卷已交付(M0-M14)+ P2 PB0-PB7 + 后续优化轮 #1-#4 全过线;P3 PW0(开工前置 spike)待启动——spike 是 P3 生死闸门,先于一切翻译工作。
> 单一事实源:本文是 P3 实现现状与设计文档差异的对账表(对应 P1/P2 [implementation-progress.md](../p1-interpreter/implementation-progress.md) 的角色)。
> 设计文档集:见 [00-overview](./00-overview.md) §0 文档地图。
>
> **术语:`P-Wasm`(PW)= P3 实现里程碑编号**(对应 P1 的 M、P2 的 PB);PW0 是 spike 闸门,PW1-PW9 是翻译器到端到端验收。

---

## 0. 当前状态

**P3 实现:PW0 spike 闸门已通过(2026-06-13),PW1-PW9 未启动。** 设计文档集已齐备(00-08 共约 8800 行,含全 opcode 翻译表 + WAT 伪码 + Go 实代码骨架 + V1-V18 验收口径)。

**前置条件检查**:
- ✅ P1 全卷已交付(M0-M14 + 所有收尾轮 + 长稳承诺轮 + 外部审查修复轮 + 官方测试套与性能轮)
- ✅ P2 PB0-PB7 + 后续优化轮 #1-#4 全过线(2026-06-13)
- ✅ P3 设计文档完整(00 总览 + 01 spike 闸门 + 02 翻译器 + 03 内存模型 + 04 trampoline + 05 safepoint-gc + 06 feedback 消费 + 07 协程线程规则 + 08 测试验收)
- ✅ **P3 PW0(wazero call boundary spike < 150ns)闸门通过**——S2 主指标 36.7ns ≪ 150ns(详见 §0.1 spike 数据存档);**P3 开工放行**
- ⏳ **P3 开工前置确认(待外部)**:向首个宿主确认「列内核是否跑在协程里」,决定线程级 tier 规则是否成立([07 §3.2](./07-coroutine-thread-rule.md))

---

## 0.1 PW0 spike 实测数据存档(承 [01-spike-gate §6](./01-spike-gate.md) 决策报告模板)

> **本节是 P3 是否启动的依据,永久存档不删**(承 §5 维护协议第 1 条)。spike 代码:`spike/p3boundary/`(独立 go module,不污染主库零依赖)。

**测量环境**:Intel Xeon 6982P-C / go1.26.2 / **wazero v1.12.0** / 编译模式(`NewRuntimeConfigCompiler`)/ `-benchtime=10s -count=5` 取中位数(噪声 < 1%)。

**三档 + 变体实测**:

| 样本 | ns/op | 闸门 | 判定 |
|---|---|---|---|
| **S1 空往返** | 18.9 | < 80 | ✅ |
| **S2 带参 + memRW(`Call`)——主指标** | **36.7** | **< 150** | ✅ 远超 |
| S2 零分配(`CallWithStack` 复用栈) | **14.8** | < 150 | ✅✅ 极优 |
| S3 反向 imported(单次完整 `fn.Call`) | 201.7 | < 150 | ⚠️ 超 |
| S3-N 摊销(Wasm 内循环调 imported × 1000) | **~143/单次 dispatch** | < 150 | ⚠️ 边缘 |

**决策(01-spike-gate §5 决策树)**:**S2 主指标 36.7ns ≪ 150ns → 闸门主路径通过,P3 开工放行(PW1 可启动)**。`CallWithStack` 零分配路径 14.8ns 更优,PW2 trampoline 入口应直接采用(类比 P1 issue #8 的 `CallInto`)。

**spike 揭示的两项重要认知修正**(直接影响 P3 设计文档,记入 §3.4):

1. **慢路径助手(gibbous → host imported)单次 dispatch ≈ 143ns,贴 150ns 边缘**——不是设计文档摊销模型假设的「同 S2 档 ~36ns」。摊销模型 [01-spike-gate §2 / 02-translation 摊销模型] 的 `k·T_cross` 中 **`T_cross`(慢路径)应取 ~143ns**。含义:列内核形态(k≈0,助手罕见)收益完整;但若热循环每迭代调一次 helper(k≥1),143ns 会显著吃收益——**这强化了 [02-translation §1] 「翻译单位覆盖整个热闭包、IC 快路径内联避免跨层」的动机**。

2. **wazero 生成码不被 Go 异步抢占**(与 [00-overview §8] / roadmap §2 原称「回边已有抢占检查点」**相悖**)——wazero RATIONALE.md「Why it's safe to execute runtime-generated machine codes against async Goroutine preemption」明确:生成码被 Go 运行时标为 async-preemption-**unsafe**,运行期间 Go 调度器无法抢占;wazero 靠 **context cancellation(`WithCloseOnContextDone`)** 协作式终止长循环。实测坐实:纯计算长循环阻塞 STW GC 达 144ms;`WithCloseOnContextDone` + 50ms context 超时精确终止(返回 `context deadline exceeded`)。**含义**:① gibbous 跑长循环时其他 goroutine 的 GC 会被阻塞到该循环经 helper / 函数返回让出——[05-safepoint-gc §1.3] 的回边 gcPending 检查恰好是让出点,**但纯计算无 helper 的死循环是已知边角**;② P3 长循环终止靠 `WithCloseOnContextDone` + context(P1 issue #4 已落地 context 取消,P3 复用),**不是异步抢占**——这是对 roadmap §2「异步抢占税 wazero 已验证」表述的精确化(税被解决的**机制**是 context 协作式终止而非回边抢占)。

**arena 收养可行性确认**(顺带项 A,[01-spike-gate §4.2] 三项验证):

| 验证点 | 结果 |
|---|---|
| `Memory.Read` 返回 write-through 零拷贝视图 | ✅ 确认——P3 arena.backing 可 unsafe 别名此视图作底层(03-memory-model §1 物理基础成立) |
| `memory.grow` 后旧视图失效需重取 | ✅ 坐实(旧视图 cap=64KiB,grow 后新视图 cap=128KiB,底层换了)——[03-memory-model §1.7] 「grow 后 Go 视图重取」的「待验证」项**有答案:必须重取** |
| 固定容量避免 grow 重取 | ✅ `WithMemoryCapacityFromMax(true)` + memory min<max(预分配 max)下,grow 不换 buffer,旧视图稳定——P3 可选「预分配 max 避免重取」策略 |

**四项税最小验证**(顺带项 B):

| 税 | 结果 |
|---|---|
| GC 精确栈扫描 | ✅ 1 万次 wasm 调用 + 中间 `runtime.GC()` × 2,memory 无损坏 |
| 异步抢占 | ⚠️ **机制修正**(见上「认知修正 2」)——wazero 不靠异步抢占,靠 context 协作终止;`WithCloseOnContextDone` 实测有效 |
| 栈移动 | ✅ 隐式覆盖(GC 测试期间 morestack 触发,无数据损坏) |
| 写屏障 | ✅ 设计层规避(值世界全在 linear memory,生成码不写 Go 堆指针,S2 形态已涵盖) |

**版本绑定提醒**:数据随 wazero **v1.12.0** 记录;未来 wazero 升级需重跑 spike(`spike/p3boundary/` 保留作回归)。

---

## 1. 里程碑进度对账(对应 [00-overview §4](./00-overview.md))

| PW | 内容 | 文档 | 完成定义 | 状态 |
|---|---|---|---|---|
| PW0 | 开工前置 spike(wazero call boundary S1/S2/S3 实测) | [01](./01-spike-gate.md) | S2 < 150ns 且 S3 同档 ⇒ 开工;否则跳 P4 或边缘混合 | ✅ **通过**(S2=36.7ns;S3=202ns 走边缘修正,见 §0.1) |
| PW1 | `internal/gibbous/wasm` 包骨架 + arena 收养 wazero memory + 零 opcode 翻译器 | [02 §1](./02-translation.md) + [03 §1](./03-memory-model.md) | bridge 注入真 P3 后所有 Proto 仍走 crescent(F7 拦下);arena/wazero memory 共见验证 | ⏳ |
| PW2 | 翻译器骨架 + 5 直线 opcode(MOVE/LOADK/LOADBOOL/LOADNIL/JMP)+ trampoline 入口 | [02 §3.1](./02-translation.md) + [04 §2](./04-trampoline.md) | 5-op Proto 升层后 byte-equal;升层日志 `function promoted to gibbous` 触发 | ⏳ |
| PW3 | 算术 + 比较 opcode + NaN 规范化 + 慢路径助手回 Go | [02 §3.2-§3.3](./02-translation.md) + [04 §3](./04-trampoline.md) | 双 number 快路径直发 f64;混合类型走助手且 byte-equal | ⏳ |
| PW4 | 控制流(FORPREP/FORLOOP/TFORLOOP)+ 回边 safepoint | [02 §3.5](./02-translation.md) + [05 §1.3](./05-safepoint-gc.md) | 数值 for 编译后 ≥2x 解释器;回边 GC 触发 byte-equal | ⏳ |
| PW5 | 表 IC opcode + feedback 消费(IC 快照固化)+ 失效降级 | [02 §3.4](./02-translation.md) + [06](./06-ic-feedback-consume.md) | 单态表访问跳过哈希;gen bump 后走助手仍正确 | ⏳ |
| PW6 | CALL/TAILCALL/RETURN + 跨层互调 + status 链 + **CallInfo bit50 落地** | [04 §2-§4](./04-trampoline.md) | gibbous 内调未编译 Proto 经 trampoline;错误从 Wasm 帧穿越冒泡到 pcall | ⏳ |
| PW7 | CLOSURE/CLOSE/VARARG + 闭包/upvalue 编译协议 | [02 §3.7](./02-translation.md) | 闭包构造 + 开放/关闭 upvalue byte-equal | ⏳ |
| PW8 | 线程级 tier 规则 + 协程不升层 + **P2 04 considerPromotion 加线程上下文** | [07](./07-coroutine-thread-rule.md) | 协程内即便 hot + Compilable 也保持 TierInterp;主线程同 Proto 正常升层 | ⏳ |
| PW9 | 端到端验收 + 测试套(差分 + 强制全升 + GC 压力 fuzz + 性能 ≥2x) | [08](./08-testing-strategy.md) | **P3 总验收**:V1-V18 全过 | ⏳ |

---

## 2. 跨文档回填请求收口表

P3 设计期各子文档对 P1/P2 现有文档发起的回填请求。**承用户裁决「本期只记录不主动改 P1/P2 现稿」** —— 全部标「⏳ P3 PWx 落地时同批补」,不在文档扩展轮兑现。

### 2.1 对 P1 文档的回填请求

| # | 来源 | 内容 | 兑现 PW |
|---|---|---|---|
| RW-1 | [04 §1](./04-trampoline.md) | P1 [05 §1.2](../p1-interpreter/05-interpreter-loop.md) CallInfo word2 bit50 `callStatus_gibbous` 语义登记(从「P1 恒 0 预留」升级为「P3 trampoline 写,P1 不读」)+ crescent callInfo struct 加 gibbous 标识字段 | PW6(与跨层协议同批) |
| RW-2 | [05 §2.4](./05-safepoint-gc.md) | P1 [06 §12](../p1-interpreter/06-memory-gc.md) 「层边界 safepoint 的具体形态」缺口标关闭(P3 §1 三类布点已收口) | PW4(回边 safepoint 落地时) |
| RW-3 | [07 §5.2](./07-coroutine-thread-rule.md) | P1 [08](../p1-interpreter/08-coroutines.md) 增「gibbous 帧 = 不可穿越 yield 边界」节(P1 08 §6 已留前瞻引用,替换为正文) | PW8 |
| RW-4 | [02 §4.2](./02-translation.md) | P1 [05](../p1-interpreter/05-interpreter-loop.md) `CallInfo.savedPC` 物化语义(pc 立即数经助手写回)登记 | PW2/PW3(助手落地时) |
| RW-5 | [03 §8.1](./03-memory-model.md) | P1 [06 §1.1/§3](../p1-interpreter/06-memory-gc.md) `arena.Options.NewBacking` 注入点 P3 build 下的 wazero adapter 替换确认(P1 已留口,P3 验证) | PW1 |

### 2.2 对 P2 文档的回填请求

| # | 来源 | 内容 | 兑现 PW |
|---|---|---|---|
| RW-6 | [07 §2.4 / §8.1](./07-coroutine-thread-rule.md) | P2 [04 §3](../p2-bridge/04-try-compile-fallback.md) considerPromotion 入口加线程上下文输入(`th *Thread`);协程内即便 hot + Compilable 也不升层 | PW8 |
| RW-7 | [07 §6.3](./07-coroutine-thread-rule.md) | P2 [01 §4](../p2-bridge/01-profiling.md) onBackEdge / onEnter 入口签名连带扩展(加 `th` 参数,与 considerPromotion 同步) | PW8 |
| RW-8 | [04 §6.4 / §9](./04-trampoline.md) | P2 [04 §4.4](../p2-bridge/04-try-compile-fallback.md) installGibbous 增「multi-State 共享 Proto trampoline 注册幂等保证」 | PW6 |
| RW-9 | [01 §5.3 / §8.4](./01-spike-gate.md) | P2 [03](../p2-bridge/03-compilability-analysis.md) 加 `callDensity` 启发(边缘混合策略)**——仅在 spike 数据落边缘区(150±30ns)时触发**;主路径通过则无需 | PW0 后(仅边缘出路) |
| RW-10 | [08 §2.5 / §7.2](./08-testing-strategy.md) | P1 [12 §3.7](../p1-interpreter/12-testing-difftest.md) 差分生成器偏向「产生 Compilable Proto」(避开 F1-F7 排除形状,否则强制全升下差分退化为 crescent vs crescent) | PW9 |
| RW-11 | [08 §7.2](./08-testing-strategy.md) | P3 新增测试入口(SetForceAllPromote / wazero memory 健康检查 / gibbous panic 注入 / 跨层错误注入),复用 P2 [06 §11.1](../p2-bridge/06-testing-strategy.md) 的 internal-only 暴露纪律 | PW9 |

---

## 3. 设计期决策盘点(影响 × 不确定度)

按 [multi-doc-drafting guide](../../../llmdoc/guides/multi-doc-drafting.md) 「主动盘点不确定决策」纪律。

### 3.1 影响 PW 开工形态(高影响 / 高不确定度)

| 决策 | 定稿 | 出处 | 复核点 |
|---|---|---|---|
| **wazero call boundary < 150ns** | spike 闸门;不达标跳 P4 | [01 §5](./01-spike-gate.md) | **PW0 实测,P3 生死攸关** |
| **协程是否跑列内核** | 线程级 tier 规则(协程不升层)成立的前提 | [07 §3.2](./07-coroutine-thread-rule.md) | **开工前向首个宿主确认**;若列内核真在协程里 → 退路线 A goroutine 化兜底 |
| 翻译单位 | 每 Proto 一 module(基线) | [02 §1](./02-translation.md) | PW1 实测实例化开销;批量 module 留优化 |
| 寄存器映射 | 全 memory-resident(基线) | [02 §2](./02-translation.md) | PW9 前 locals 缓存不启用 |
| arena 收养 wazero memory | P3 起替换 backing 来源 | [03 §1](./03-memory-model.md) | PW1 验证 NewBacking 注入点 + grow 跨边界一致 |

### 3.2 依赖外部数据 / wazero API(中影响 / 高不确定度)

| 决策 | 当前 | 校准条件 |
|---|---|---|
| wazero memory 共享 API(import vs 读 module memory) | 倾向 import memory(基线 A) | [01 §4](./01-spike-gate.md) spike 顺带验证 |
| `memory.grow` 后 Go 视图稳定性 | 倾向「grow 后重取 slice」 | [03 §1.7](./03-memory-model.md) spike 验证 |
| `memory.grow` 并发约束 | 待定 | [03 §3](./03-memory-model.md) spike 验证 |
| IC 快照失效 → 重编译 | P3 不做(正确但慢) | 与 P4 deopt 基建统一评估([06 §6](./06-ic-feedback-consume.md)) |
| locals 缓存槽选择 | FORLOOP 三槽起步 | PW9 实测后定标([02 §2.2](./02-translation.md)) |

### 3.3 低风险已记录(低影响 / 已记缺口)

各子文档 §文档缺口节 + [00-overview §10](./00-overview.md) 风险汇总,约 20 余条次要缺口,均不阻塞 PW0 启动(PW0 spike 本身是唯一硬阻塞)。

### 3.4 PW0 spike 推翻/修正的设计文档表述(PW1 启动时同批修正)

spike 实测(§0.1)推翻了设计文档的两处表述,**PW1 启动时同批修正对应子文档**(本期只记录,承「先记录后修正」纪律):

| # | 设计文档原表述 | spike 实测修正 | 待修正落点 |
|---|---|---|---|
| SP-1 | [00-overview §8] / roadmap §2:「wazero 生成码循环回边已有抢占检查点」(异步抢占税 wazero 已验证) | wazero 生成码 **async-preemption-unsafe**,不被 Go 异步抢占;靠 `WithCloseOnContextDone` + context **协作式终止**(非回边抢占)。纯计算长循环阻塞 STW GC 144ms 实测坐实 | [00-overview §8] + [05-safepoint-gc §1.5] 把「异步抢占税解法」措辞从「回边抢占检查点」改为「context 协作终止 + 望舒 gcPending 回边让出」;长循环终止机制明确为 `WithCloseOnContextDone`(复用 P1 issue #4 context) |
| SP-2 | [01-spike-gate §2 / 02-translation 摊销模型]:`T_cross` 隐含同 S2 档(~36ns) | 慢路径助手(gibbous→host imported)单次 dispatch ≈ **143ns**,贴 150ns 边缘;`T_cross`(慢路径)≫ `T_cross`(入口) | [02-translation §1 / 06-ic-feedback-consume] 强化「翻译单位覆盖热闭包 + IC 快路径内联避免跨层」论证;摊销模型用 `T_cross_slow ≈ 143ns` 重算 k≥1 形态的收益边界 |

**正面确认**(spike 验证设计假设成立,无需修正):arena 收养 wazero memory 物理可行([03-memory-model §1] 成立)、grow 后重取协议([03-memory-model §1.7] 待验证项有答案)、GC 精确栈扫描/栈移动/写屏障三项税兑现。

---

## 4. P3 与 P1/P2 implementation-progress 的差异

| 维度 | P1/P2 implementation-progress | 本文(P3) |
|---|---|---|
| 当前状态 | 全卷已交付,持续维护后续轮次对账 | 设计阶段,实现未启动(PW0 闸门待跑) |
| 表格主体 | 实际落地的 PR / 提交哈希 / 时间线 | 设计阶段决策对账 + 待实施回填请求 |
| 与设计文档的差异 | 已落地形态与设计文档的差异 | (无差异——尚未实施) |
| 核心阻塞 | 无(已交付) | **PW0 spike 闸门 + 外部宿主确认** —— 两项均可能改变 P3 是否启动 / 如何启动 |
| 后续维护 | 每轮里程碑落地后追加对账行 | PW0 spike 跑出数据后,要么追加 PW 进度行,要么记录「跳 P4」决策 |

---

## 5. 后续维护协议

PW0 启动后,本文按以下协议更新:

1. **PW0 spike 数据进档**:三档(S1/S2/S3)实测 ns 值 + 决策(开工 / 跳 P4 / 边缘混合)永久记录在本文,无论结果如何——这是 P3 是否启动的依据,必须可追溯([01 §6 决策报告模板](./01-spike-gate.md));
2. 每个 PW 完成时,把对应行的 `⏳` 改 `✅`,加完成提交哈希;
3. 实际落地与设计文档有差异时,加「实现现状与设计文档差异对账表」;
4. 跨文档回填请求(§2)逐项实施,把对应行从「⏳ P3 PWx 落地时同批补」改「✅ 已落地」+ 提交哈希;
5. PW9 总验收过线后,本文头部状态改「P3 已交付」+ 验收数字汇总(性能 ≥2x over P1 + V1-V18 全过);
6. **若 PW0 spike 不达标**:本文记录「P3 跳过,直做 P4」决策 + spike 数据,P3 设计文档集转为「P4 继承的分层协议参考」(§2-§7 被 P4 继承,只换发射后端)。

---

相关:
- [00-overview](./00-overview.md)(P3 总览,本文是其 §4 PW 表的运行期对账)
- [01-spike-gate](./01-spike-gate.md)~[08-testing-strategy](./08-testing-strategy.md)(各子系统设计文档)
- [../p1-interpreter/implementation-progress](../p1-interpreter/implementation-progress.md)(P1 同款)
- [../p2-bridge/implementation-progress](../p2-bridge/implementation-progress.md)(P2 同款,作维护协议参考)
- [../../../llmdoc/guides/multi-doc-drafting](../../../llmdoc/guides/multi-doc-drafting.md)(主动盘点不确定决策的纪律来源)
- [../../../llmdoc/memory/doc-gaps](../../../llmdoc/memory/doc-gaps.md)(P3 开工前置确认 / P3 迁移留口)
