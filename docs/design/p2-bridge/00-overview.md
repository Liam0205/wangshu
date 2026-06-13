# P2 总览:bridge 分层桥(基建)——文档地图 / 实现里程碑 / 验收 / 人月分解

> 状态:**设计阶段,详细设计已齐备**。本文是 P2 文档集(00-06)的导航与施工计划:每篇文档的定位、组件依赖、构建顺序、里程碑验收门槛、人月分解、跨文档定稿决策速查、与 P1 的桥接(P1 已落地的前瞻义务对账)。
> 上游:`docs/design/roadmap.md` (§4 P2 定义、§5 五条原则)、[architecture](../architecture.md)(§3 依赖图、bridge 是基建非执行层)、[evolution-roadmap](../../llmdoc/architecture/evolution-roadmap.md)(P2 无独立量化验收、tier 映射 P2 不分配月相)。
> P1 依赖面:[02 §4 FORLOOP/JMP 回边](../p1-interpreter/02-bytecode-isa.md)、[02 §7 IC slot](../p1-interpreter/02-bytecode-isa.md)、[02 §4 编号 38..63 预留](../p1-interpreter/02-bytecode-isa.md)、[05 §6 IC 执行 / §6.4 算术 IC「P1 写不读纯供料」](../p1-interpreter/05-interpreter-loop.md)、[05 §1 CallInfo/Frame](../p1-interpreter/05-interpreter-loop.md)、[04 AST 利于可编译性分析](../p1-interpreter/04-frontend-parser-codegen.md)、[11 §1.3 Compile 可编译性探测占位](../p1-interpreter/11-embedding-arena-abi.md)。
>
> P2 目标一句话:**在 P1 解释器之上加一台「分层决策机器」,产出三样东西(热度、IC 类型 feedback、可编译性判定)喂给 P3/P4 编译层,自己不在执行热路径上**——`evolution-roadmap` 速查表写「P2 无独立量化门槛」是因为 P2 不加速;**P2 的「成功」是「决策正确」**(不漏报真热点 / IC 反馈忠实反映运行期 / 可编译性绝不误判),不是「跑得多快」。

---

## 0. 文档地图:谁定什么(单一事实源分工)

| 文档 | 定位 | 单一事实源(其它文档以它为准) |
|---|---|---|
| [01-profiling](./01-profiling.md) | 计数 | 回边/入口采样点选择论证、`ProfileData` 字段级 spec、阈值与编译预算 pacing、profile 路线 A/B 抉择(旁路计数定稿) |
| [02-ic-feedback](./02-ic-feedback.md) | 反馈 | P1 IC 写入复用契约、算术 IC 双计数挪用规约(P1 IC 字段共享方案)、`TypeFeedback` shape 与 `confidence` 计算、megamorphic 标记的 P2 落地 |
| [03-compilability-analysis](./03-compilability-analysis.md) | 安全闸门 | 不升层形状清单 F1-F7 完整定义、AST visitor 设计与 04 AST 保留协议、yield 保守近似算法、`Compilability` 缓存生命期、F7 (P3 后端能力)查询协议 |
| [04-try-compile-fallback](./04-try-compile-fallback.md) | 决策 | TierState 状态机(单向 + 吸收态)、升层决策入口 `considerPromotion`、TierStuck 不重试纪律、零 deopt 论证、fallback ≠ deopt 严格区分、升层日志格式(`promoted to gibbous`) |
| [05-p3-p4-interface](./05-p3-p4-interface.md) | 接口 | `P3Compiler`/`P4Feedback` 接口定义、共享前端契约(P3 `feedback` 可选/P4 `feedback` 核心)、跨阶段消费协议 |
| [06-testing-strategy](./06-testing-strategy.md) | 验收 | P2 验收口径总表(决策正确,非性能)、可编译性误判注入测试、升层日志断言、编译失败 fallback 对拍、跨阶段差分(crescent-only vs P2-on-crescent 同结果) |
| [implementation-progress](./implementation-progress.md) | 进度 | M0 起步前预设占位(P1 已落地的前瞻义务对账见 §6;P2 实现现状对账模板) |

阅读顺序建议:实现者先读 00→04(状态机)→01(计数,易着手)→03(安全闸门,关乎正确性)→02(反馈,有 P1 字段挪用回填)→05(接口,与 P3/P4 对接)→06(验收口径,每步收口查)。

---

## 1. P2 与 P1/P3 的边界(谁拥有什么)

| 关注点 | P1(crescent)拥有 | **P2(bridge)拥有** | P3+(gibbous)拥有 |
|---|---|---|---|
| IC slot 的**写入** | ✅ 解释器命中/失效时写(05 §6) | — | — |
| IC slot 的**读取消费** | P1 读表/全局 IC 加速取值;**算术 IC 写而不读**(05 §6.4) | ✅ 读全部 IC 聚合成类型 feedback([02-ic-feedback](./02-ic-feedback.md)) | 读 P2 聚合的 feedback 做投机(P4) |
| **热度计数** | 提供采样点(FORLOOP 回边、函数入口,02 §4) | ✅ 计数器存储 + 阈值判定([01-profiling](./01-profiling.md)) | — |
| **可编译性判定** | 提供 AST(04)与 Proto(02);`Compile` 占位恒「可解释」(11 §1.3) | ✅ 静态分析 pass + 判定结果([03-compilability-analysis](./03-compilability-analysis.md)) | 读判定决定编译哪些 Proto |
| **升降层决策** | — | ✅ 状态机([04-try-compile-fallback](./04-try-compile-fallback.md)) | 接受「编译此 Proto」请求,产出 gibbous 代码 |
| **实际编译/执行加速** | 解释执行(永不退役,fallback 落点) | ❌ **不加速** | ✅ 字节码→Wasm,兑现倍率 |

**一句话**:P1 产料(IC 写、采样点、AST),P2 加工成决策(热度、feedback、可编译性),P3 消费决策去加速。**P2 是中间的「决策加工厂」,自己不进热路径**——这一条贯穿所有子文档,任何让 `internal/bridge` 出现「跑 Proto」逻辑的设计都直接判否。

> **tier 坐标系警告**([evolution-roadmap](../../llmdoc/architecture/evolution-roadmap.md)):月相 tier 比阶段粗一层。P1=tier-0(crescent),P3/P4=tier-1(gibbous),P5=tier-2(fullmoon)。**P2 不分配月相**——它是基建,不是一个能执行字节码的层。代码包名也据此:执行层用月相(`crescent`/`gibbous`/`fullmoon`),P2 用功能名 `internal/bridge`([architecture](../architecture.md) §1)。日志里**不会**有 `function promoted to bridge`;P2 触发的升层日志是 `function promoted to gibbous`(落点是 gibbous,见 [04-try-compile-fallback](./04-try-compile-fallback.md))。

---

## 2. 总数据流(承 P1,产料给 P3/P4)

```
                P1 解释器执行期(crescent,已落地)
                 │            │              │
       FORLOOP 回边 /      IC slot 命中      Compile 时 AST
       函数入口采样         /失效写入         (04 产出)
       (02 §4 已落地)      (05 §6.4 已落地    (04 §1 已落地)
                          含算术 IC 双计数)
                 │            │              │
                 ▼            ▼              ▼
        ┌──────────────┐ ┌──────────┐ ┌──────────────────┐
        │ 热度计数器     │ │ IC 反馈   │ │ 可编译性分析 pass │   ← internal/bridge
        │ 01-profiling  │ │ 02-ic-fb │ │ 03-compilability │     (P2 本体)
        └──────────────┘ └──────────┘ └──────────────────┘
                 │            │              │
                 └────────────┴──────┬───────┘
                                     ▼
                        ┌──────────────────────────┐
                        │ 04-try-compile-fallback   │
                        │ 升降层决策状态机            │
                        │ 单向 + 吸收态(零 deopt)   │
                        └──────────────────────────┘
                                     │
                      ┌──────────────┴──────────────┐
                      ▼ (可编译 + 热)                ▼ (不可编译 / 冷 / 失败)
              请 P3 编译该 Proto              留在 P1 解释器
              (05-p3-p4-interface)            (永不退役,原则 1)
```

四条输入(热度、IC 反馈、AST、Proto)汇聚到一台状态机,输出二元决策:**「请 gibbous 编译」或「留在 crescent 解释」**。P2 自己不消费 feedback——它只是供料。这与算术 IC 的「P1 写不读」一脉相承:**信息在每一层被生产,在下一层被消费**——P1 写 IC,P2 读 IC 写 feedback,P4 读 feedback 投机。每层只做自己那一棒。

---

## 3. 组件依赖(回顾)与关键耦合点

依赖图见 [architecture](../architecture.md) §3:`internal/bridge` 依赖 `bytecode`(读 Proto) + `value`(读 IC slot kind) + `ast`(可编译性 AST visitor) + `gc`(Proto 旁 ProfileData 不是 GC 对象,但分配在 Go 堆与 Proto 同生命期,无 GC 干扰)。**`internal/bridge` 不依赖 `crescent`**——这是「基建非执行层」的物理体现:bridge 不调解释器,而是被解释器在采样点回调(FORLOOP 执行侧 `vm.bridge.onBackEdge(...)`)。

设计定稿后新增的跨组件**关键耦合点**(实现时最易出错处):

1. **算术 IC 双计数挪用**([02-ic-feedback](./02-ic-feedback.md) §3 / 02 §7 / 05 §6.4):算术点的 ICSlot 闲置字段(`shape`/`index`/`tableRef`,算术不存表)挪用为 `numHits`/`metaHits` —— **P1 已落地此挪用**(M10 IC 接入时同批写入,P1 写不读),P2 直接读。**回填请求已兑现** ✅。
2. **回边采样点的 profile 启用开关**([01-profiling](./01-profiling.md) §2 / 05 §10.1 FORLOOP):`vm.profileEnabled` 编译期常量(或 build tag),P1-only 部署时关掉零开销。这是「每阶段独立交付」(原则 3)在 P1↔P2 边界的物理兑现 —— **P1 当前实现是「关」**,P2 启动时翻成「开」并实现 `bridge.onBackEdge`/`onEnter` 回调。
3. **`Proto.compilable` 字段的初值约定**([03-compilability-analysis](./03-compilability-analysis.md) §4 / 11 §1.3):P1 的 `Compile` 把所有 Proto 标 `CompUnknown`;P2 上线后 `Compile` 时同步跑 `analyzeProto` 写真值进 `Proto` 旁 `ProfileData` —— **P1 公共 API 不变**,wangshu.go 门面零修改(原则 3「接口稳定」)。
4. **AST 保留协议**([03-compilability-analysis](./03-compilability-analysis.md) §2 / 04 §1):04 已标「P1 顺手产出 P2 复用」,但 P1 当前实现是 codegen 后 AST 被 GC —— P2 启动时定夺**「Compile 时同步分析、结果缓存,AST 用完即弃」**而非保留 AST。这是 P2 的最早决策点(影响 04 是否需要给 P2 加 AST 留存接口)。
5. **CallInfo bit50 gibbous 位**(05 §1.2 已留):P1 恒 0,P2 在 [04-try-compile-fallback](./04-try-compile-fallback.md) `installGibbous` 时写 1,标识跨层帧。**P1 已落地预留位**,P2 写入语义是 P3 trampoline 的依赖(P3 §5)。
6. **多 State 共享 Program 的 profile 归属**(11 §1.4 / §8):一个 `Program` 可被多个 `State` 并发 Call(11 §8 已落地 `-race` 通过)。Profile 计数挂哪是 P2 的核心并发缺口——倾向**挂 State 私有的 `profileTable`** 避免并发写 Proto 旁计数;但这把累积速度均分到各 State,需实测。详见 [01-profiling](./01-profiling.md) §6。

---

## 4. 实现里程碑(细化 architecture §5 的施工顺序)

每步可独立编译 + 单测通过再进下一步。「验收」列是该步的完成定义;PB 编号(P-Bridge)供排期引用。**P2 不加速,所以里程碑验收全部是「决策正确性」类**,无性能验收门(对比 P1 M10/M14 是性能门)。

| PB | 内容 | 对应文档 | 验收(完成定义) |
|---|---|---|---|
| PB0 | `internal/bridge` 包骨架 + ProfileData 字段 + State 私有 profileTable + `vm.profileEnabled` 翻 true | [01-profiling](./01-profiling.md) §1-§3 | bridge 包独立编译;P1-only 部署关 profileEnabled 仍 byte-equal(差分回归) |
| PB1 | 回边/入口采样点接入(FORLOOP/JMP 回跳 + enterLuaFrame) | [01-profiling](./01-profiling.md) §2-§4 | 三档脚本(realworld benchmark game)profile 累积非零;profile 开关切换不改 byte-equal |
| PB2 | IC 反馈聚合器(读全部 IC kind + 算术双计数比例) | [02-ic-feedback](./02-ic-feedback.md) §3-§4 | 一组合成脚本(纯 number 算术 / 多态表 / 单态全局)聚合产出对应 `FeedbackKind`;`confidence` 计算单测 |
| PB3 | 静态可编译性分析器(F1-F7 AST visitor) | [03-compilability-analysis](./03-compilability-analysis.md) §3-§5 | F1-F7 各形状对应一组测试脚本,断言 `analyzeProto` 判 `CompNotCompilable`;**不可编译形状的零误判注入 fuzz**(主防线) |
| PB4 | TierState 状态机 + considerPromotion 入口 + TierStuck 不重试 | [04-try-compile-fallback](./04-try-compile-fallback.md) §2-§4 | 状态转移单测(单向 / 吸收);热度越阈值后冷函数仍冷;TierStuck 不再触发 considerPromotion(防抖动断言) |
| PB5 | 升层日志格式 + 诊断接口 | [04-try-compile-fallback](./04-try-compile-fallback.md) §6 | 三类日志(`promoted to gibbous` / `stays interpreted (not compilable)` / `compile failed`)文本断言 |
| PB6 | P3/P4 接口实现 + mock P3 编译器(给 PB7 用) | [05-p3-p4-interface](./05-p3-p4-interface.md) §1-§3 | mock P3 接受 Proto+feedback 返回 dummy GibbousCode;接口稳定性单测 |
| PB7 | 端到端验收 + 测试套 | [06-testing-strategy](./06-testing-strategy.md) §2-§5 | **P2 总验收**:(a) 可编译性零误判 fuzz 通过 (b) crescent-only 与 P2-on-crescent 跑 realworld 五脚本结果 byte-equal (c) 升层日志匹配预期 (d) 多 State 并发 profileTable `-race` 通过 |

> **PB0 启动条件**:P1 全卷已交付(M0-M14 完成);P2 前瞻义务对账见 §6,关键三项(IC 双计数挪用 / FORLOOP 采样点 / `vm.profileEnabled` 开关)在 P1 已落地。
>
> **mock P3 在 PB6 引入**:让 PB7 端到端验收不依赖 P3 实现完成。真 P3 上线时换掉 mock,P2 接口零修改(`P3Compiler` 接口稳定)。

---

## 5. 人月分解(roadmap §4:1-2 人月,基建定位)

按单人全职折算;区间下沿=顺利,上沿=含返工与 P1 字段挪用回填实测:

| 里程碑段 | 内容 | 估算 |
|---|---|---|
| PB0-PB1 | 包骨架 + 采样点接入 | 0.25 人月 |
| PB2 | IC 反馈聚合 | 0.25 人月 |
| PB3 | 可编译性分析器(F1-F7,主重点) | 0.5 - 0.75 人月 |
| PB4-PB5 | 状态机 + 日志 | 0.25 人月 |
| PB6 | P3/P4 接口 + mock | 0.1 人月 |
| PB7 | 测试套 + 验收 | 0.25 - 0.5 人月 |
| 合计 | | **1.5 - 2 人月** |

与 [evolution-roadmap](../../llmdoc/architecture/evolution-roadmap.md) 的 1-2 人月吻合(上沿略超出自 PB3 可编译性分析的保守判定细化与 PB7 端到端 fuzz 调试)。**PB3 是大头**——不是因为代码量(F1-F7 visitor 不复杂),而是**保守正确性的反复验证**:可编译性误判后果是灾难性的(投机错误静默错果一类),宁可漏判不可误判,这条铁律的兑现需要充足的注入 fuzz 与 review。

---

## 6. 跨文档定稿决策速查(实现前必读)

设计期在多篇文档间协商定稿的关键决策,集中列出防止实现时只读单篇而漏掉:

| 决策 | 定稿 | 出处 |
|---|---|---|
| 计数路线 | 路线 B 旁路计数(不改 P1 字节码 0..37);路线 A profile 伪指令保留给 P3 | [01-profiling](./01-profiling.md) §3 |
| 计数器存储 | Proto 旁 ProfileData(主)+ State 私有 profileTable(并发隔离);CallInfo 帧级不做 | [01-profiling](./01-profiling.md) §4 / §6 |
| 算术 IC 双计数 | numHits/metaHits 挪用闲置字段(shape/index/tableRef),不增 ICSlot 尺寸 | [02-ic-feedback](./02-ic-feedback.md) §3 |
| feedback shape | TypeFeedback 按 pc 索引 PointFeedback(kind+confidence+稳定 shape/index);P3 可选用 / P4 核心用 | [02-ic-feedback](./02-ic-feedback.md) §4 |
| 可编译性分析层 | 主分析 AST(04 复用),Proto 层做交叉校验;Compile 时同步分析、结果缓存进 Proto 旁 | [03-compilability-analysis](./03-compilability-analysis.md) §2 |
| 不升层形状 | F1-F7:vararg/coroutine/debug/setfenv/过大/深嵌套闭包/F7 后端能力——保守第一,宁漏勿误 | [03-compilability-analysis](./03-compilability-analysis.md) §3 |
| yield 保守 | 直接 `coroutine.yield` 或调用未知函数即判不可编译;精确分析留 P2+ | [03-compilability-analysis](./03-compilability-analysis.md) §3 F2 |
| TierState | 三状态(Interp/Gibbous/Stuck),单向 + 吸收;无回边(零 deopt) | [04-try-compile-fallback](./04-try-compile-fallback.md) §2 |
| TierStuck 不重试 | 编译失败永久 stuck 不重试;P3 后端跨版本升级的 stuck 重评估留 P2+ | [04-try-compile-fallback](./04-try-compile-fallback.md) §3 |
| fallback ≠ deopt | fallback 静态决定永久解释,deopt 运行期退回(P4 才有);P2 只有 fallback | [04-try-compile-fallback](./04-try-compile-fallback.md) §1 |
| P3 用 feedback | 可选(锦上添花的紧凑翻译,不依赖正确性) | [05-p3-p4-interface](./05-p3-p4-interface.md) §2 |
| P4 用 feedback | 核心(类型投机的输入,依赖 confidence 决定激进度) | [05-p3-p4-interface](./05-p3-p4-interface.md) §3 |
| P2 验收口径 | 决策正确(零误判 / 累积合理 / 状态机不变量),非性能 | [06-testing-strategy](./06-testing-strategy.md) §1 |

---

## 7. P1 已落地的前瞻义务对账(P2 启动前置)

P1 全卷已交付。P2 依赖的「P1 期间留口」全部已落地,P2 启动时无需 P1 端再开发:

| 前瞻义务 | P1 落地状态 | 出处 | P2 消费方式 |
|---|---|---|---|
| 算术 IC 双计数(numHits/metaHits 挪用 shape/index/tableRef) | ✅ 已落地(M10 IC 接入时实装,P1 写不读) | 02 §7 / 05 §6.4 / [implementation-progress](../p1-interpreter/implementation-progress.md) | [02-ic-feedback](./02-ic-feedback.md) §3 直接读 |
| FORLOOP / 循环体 JMP 回跳的回边采样点 | ✅ 字节码层已暴露;P1 解释器执行侧已可挂钩(`vm.profileEnabled` 当前 false) | 02 §4 / 05 §10.1 | [01-profiling](./01-profiling.md) §2 翻开关 + 实现 onBackEdge |
| `enterLuaFrame` 函数入口采样钩 | ✅ 已落地;P1 内未实际计数(占位) | 05 §1.4 / §7.1 | [01-profiling](./01-profiling.md) §2 实现 onEnter |
| opcode 编号 38..63 预留 | ✅ 全空(02 §4 编号表 0..37 用满,38..63 不分配) | 02 §4 | P2 不占用(路线 B);留 P3 视需要用 |
| AST 是否保留 | P1 现状是 codegen 后丢弃;**待 P2 启动时定夺**「Compile 时同步分析+缓存,AST 用完即弃」(倾向方案,无需 04 改造) | 04 §1 | [03-compilability-analysis](./03-compilability-analysis.md) §2 主决策点 |
| `Compile` 可编译性探测占位 | ✅ 已落地(恒「可解释」,所有 Proto tier-0,11 §1.3) | 11 §1.3 | [03-compilability-analysis](./03-compilability-analysis.md) §5 填实 |
| arena backing 注入点 | ✅ 已落地(arena.Options.NewBacking,P3 用,P2 不直接消费) | 06 §1.1 / [implementation-progress](../p1-interpreter/implementation-progress.md) | P2 不依赖(P3 才用) |
| CallInfo bit50 gibbous 位 | ✅ 已落地(P1 恒 0) | 05 §1.2 | [04-try-compile-fallback](./04-try-compile-fallback.md) installGibbous 时写 1 |

**结论**:P2 启动是纯增量,P1 全部前瞻义务已交付,P2 PB0 可直接开工。**AST 保留协议已定稿**([03-compilability-analysis §2.4](./03-compilability-analysis.md)):**方案 ① Compile 时同步分析、缓存结果、AST 用完即弃**(三选一中选 ①——接口稳定 / AST 短命现状 / 运行期零开销 / 错误一致性)。落地协议:`compile.Gen` 在产出 `*bytecode.Proto` 后调一次 `bridge.AnalyzeProto(funcBody, proto)` 把可编译性结果写进 `proto.ProfileData.Compilable`;`!profile` build tag 下跳过该调用,所有 Proto 留 `CompUnknown`(零值,与 P1-only 行为完全一致)。**P1 公共 API 不变**——这是「填占位不改 API」原则 3 的物理兑现。

---

## 8. P2 的「成功标准」是「决策正确」(承 §0,验收口径预设)

P2 不加速、不在热路径(§1),所以**没有性能门**——这是 P2 与 P1/P3/P4 验收的**根本不同**:

| 阶段 | 性能验收 | 决策正确性验收 |
|---|---|---|
| P1 | ≥2x over gopher-lua 三档 + benchmark game 五项 + boundary mini | + 差分逐字节一致(主要在性能验收外) |
| **P2** | **❌ 无**(基建定位) | ✅ **唯一验收维度**(详见 [06-testing-strategy](./06-testing-strategy.md)) |
| P3 | 循环密集 ≥2x over P1 | + crescent/gibbous 层间差分 |
| P4 | 列内核 ≥ luajc 档 | + 投机正确性(deopt 着陆点严格性) |

P2 验收三条主轴:

1. **可编译性零误判**——F1-F7 形状的零误判注入 fuzz 是 P2 的主防线,与 P1 的差分 fuzz 同地位。
2. **累积合理 + 阈值生效**——回边/入口计数累积非零、越阈值才触发 considerPromotion、单调上升不丢失。
3. **状态机不变量**——TierStuck 不再触发(防抖)、升层单向(无 Gibbous→Interp 边)、profileTable 跨 State `-race` 通过。

详细口径表见 [06-testing-strategy](./06-testing-strategy.md) §1,对应 P1 [12 §10](../p1-interpreter/12-testing-difftest.md) 的角色。

---

## 9. 风险与未决缺口汇总(详见各文档缺口节与 [doc-gaps](../../../llmdoc/memory/doc-gaps.md))

- **可编译性误判风险**:F2(协程 yield)的精确分析 P2 初版用保守近似,可能漏判一批可编译函数(损失加速,不损失正确性)。详见 [03-compilability-analysis](./03-compilability-analysis.md) §3 F2 的 §9 缺口;若实测发现漏判面太大(真实负载里大量函数被保守判不可编译),再开「精确 yield 调用图分析」工作。
- **多 State profile 归属风险**:Profile 挂 State 私有 vs Proto 共享是核心并发决策。挂 State 私有避免 `-race`,但累积速度均分;挂 Proto 共享累积更快但需 atomic 计数。**当前定稿 (B+C) 嵌套**:(B) 默认运行(profileTable 挂 State 私有)+ (C) 接口预设占位实现(Proto 旁聚合表 nop,详见 [01-profiling §6](./01-profiling.md))。pineapple sync.Pool 形态实测后若发现累积速度均分严重影响热阈值生效,替换 (C) nop 为真聚合,接口无需改动。
- **AST 保留协议**:已定稿(见 §7 末尾)——方案 ① Compile 时同步分析、缓存结果、AST 用完即弃。详见 [03-compilability-analysis §2.4](./03-compilability-analysis.md)。
- **阈值数值定标**:`HotBackEdgeThreshold=1000` / `HotEntryThreshold=200` 是建议值,实现后用 pineapple 真实负载校准(详见 [01-profiling](./01-profiling.md) §5);**阈值不影响正确性**(只影响何时编译),与原则 3 一致(晚编译只是少赚不出错)。
- **编译预算 pacing**:防编译风暴(一次性大量函数越阈值)。P2 初版不做(STW 式「越阈值即尝试」);若实测有 cold-start 长尾再加。详见 [01-profiling](./01-profiling.md) §5。
- **过大函数 / 嵌套闭包阈值**:`MaxCompilableInsns` / `MaxClosureDepth` 实测定标(详见 [03-compilability-analysis](./03-compilability-analysis.md) §3 F5/F6)。
- **P3 后端跨版本升级的 stuck 重评估**:见 §6 决策表,留 P2+ 实现。
- **CallInfo 帧级计数**:P2 主决策不需要(详见 [01-profiling](./01-profiling.md) §4),若未来「分析单次长循环」场景需要再补。

---

相关:[architecture](../architecture.md)(包布局:bridge 是基建非执行层) ·
[01-profiling](./01-profiling.md)(回边采样 + ProfileData) ·
[02-ic-feedback](./02-ic-feedback.md)(IC 双计数 + TypeFeedback) ·
[03-compilability-analysis](./03-compilability-analysis.md)(F1-F7 安全闸门) ·
[04-try-compile-fallback](./04-try-compile-fallback.md)(状态机 + 零 deopt) ·
[05-p3-p4-interface](./05-p3-p4-interface.md)(P3/P4 共享前端) ·
[06-testing-strategy](./06-testing-strategy.md)(决策正确验收) ·
[../p1-interpreter/02-bytecode-isa](../p1-interpreter/02-bytecode-isa.md)(回边 §4 / IC slot §7 / 38..63 预留) ·
[../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md)(IC 执行 §6 / 算术 IC 写不读 §6.4 / CallInfo §1) ·
[../p1-interpreter/04-frontend-parser-codegen](../p1-interpreter/04-frontend-parser-codegen.md)(AST 利于可编译性) ·
[../p1-interpreter/11-embedding-arena-abi](../p1-interpreter/11-embedding-arena-abi.md)(Compile 占位 §1.3) ·
[../p1-interpreter/implementation-progress](../p1-interpreter/implementation-progress.md)(P1 已落地的前瞻义务对账) ·
[../p3-wasm-tier](../p3-wasm-tier.md)(P2 喂 P3:编译哪些 + feedback) ·
[../p4-method-jit](../p4-method-jit.md)(P2 喂 P4:类型投机供料) ·
[../roadmap.md](../roadmap.md) (§4 P2 定义 / §5 五条原则) ·
[evolution-roadmap](../../llmdoc/architecture/evolution-roadmap.md)(P2 无独立量化验收 / tier 映射 / P2 无月相) ·
[design-premises](../../llmdoc/must/design-premises.md)(原则 1 解释器永不退役 / 原则 4 走 fallback 不做完备性)
