# P2 实现进度对账(implementation-progress)

> 状态:**P2 设计阶段已交付,实现冲刺已完成 PB0-PB7 + P2 后续优化轮 #1-#4**(2026-06-13 单会话冲刺)。P1 已交付(M0-M14 全过线),P2 PB0-PB7 + P2 后续优化轮(原称 `P2+`,已全部融入 P2 主交付)全部里程碑过线;`make all` 双 build tag(default + wangshu_profile)全绿。
> 单一事实源:本文是 P2 实现现状与设计文档差异的对账表(对应 P1 [implementation-progress.md](../p1-interpreter/implementation-progress.md) 的角色)。
> 设计文档集:见 [00-overview](./00-overview.md) §0 文档地图。
>
> **术语:`P2+` = P2 后续优化轮**(P2 主体 PB0-PB7 交付之后的持续优化项),不是 P3 阶段。本文为减少术语混淆,首选写法是「P2 后续优化轮」;但与设计文档 00-06 各篇的 `P2+` 字面引用保持一致(避免跨文档术语漂移)。**本轮 2026-06-13 已把 P2 后续优化轮 #1-#4 全部融入 P2 主交付。**

---

## 0. 当前状态

**P2 实现:100% PB0-PB7 全过线 + P2 后续优化轮 #1-#4 全部融入 P2 主交付**。设计文档集已齐备(00-06 共 7310 行,含 §1-§9 完整论证 + Go 代码骨架)。

**前置条件检查**:
- ✅ P1 全卷已交付(M0-M14 + 所有收尾轮 + 长稳承诺轮 + 外部审查修复轮 + 官方测试套与性能轮)
- ✅ P1 期间的前瞻义务全部已落地(详见 §6;部分项目 P2 PB0 同批补齐——见 §6 修正对账)
- ✅ P2 设计文档完整(00 总览 + 01 计数 + 02 IC 反馈 + 03 可编译性 + 04 状态机 + 05 P3/P4 接口 + 06 测试)
- ✅ P2 PB0-PB7 全部交付(本轮 2026-06-13 单会话冲刺)
- ✅ P2 后续优化轮 #1-#4 全部融入 P2 主交付(stdlib 白名单 / 阈值校准占位 / sync.Pool (C) / megamorphic 主动识别)

---

## 1. 里程碑进度对账(对应 [00-overview §4](./00-overview.md))

| PB | 内容 | 文档 | 完成定义 | 状态 | 关键提交 |
|---|---|---|---|---|---|
| PB0 | `internal/bridge` 包骨架 + ProfileData 字段 + State 私有 profileTable + `vm.profileEnabled` 翻 true | [01-profiling §1-§3](./01-profiling.md) | bridge 包独立编译;P1-only 部署关 profileEnabled 仍 byte-equal | ✅ | 328e1a5 / 4fe6321 / 370c0bf(三件套) |
| PB1 | 回边/入口采样点接入(FORLOOP/JMP 回跳 + enterLuaFrame) | [01-profiling §2-§4](./01-profiling.md) | 三档脚本 profile 累积非零;profile 开关切换不改 byte-equal | ✅ | ade65f4 + 5325db5(钩点 + 测试) |
| PB2 | IC 反馈聚合器(读全部 IC kind + 算术双计数比例) | [02-ic-feedback §3-§4](./02-ic-feedback.md) | 一组合成脚本聚合产出对应 `FeedbackKind`;`confidence` 单测 | ✅ | 82881ea + abdd4df(双计数写入 + 聚合器) |
| PB3 | 静态可编译性分析器(F1-F7 AST visitor) | [03-compilability-analysis §3-§5](./03-compilability-analysis.md) | F1-F7 各形状测试断言;**不可编译形状的零误判注入 fuzz** | ✅ | cf0a326(visitor + 14 档单测) |
| PB4 | TierState 状态机 + considerPromotion 入口 + TierStuck 不重试 | [04-try-compile-fallback §2-§4](./04-try-compile-fallback.md) | 状态转移单测;TierStuck 不再触发(防抖断言) | ✅ | 51382d1(8 档状态机单测) |
| PB5 | 升层日志格式 + 诊断接口 | [04-try-compile-fallback §6](./04-try-compile-fallback.md) | 三类日志文本断言 | ✅ | 0aa17eb(stdLogger + 8 档日志格式单测) |
| PB6 | P3/P4 接口实现 + mock P3 编译器 | [05-p3-p4-interface §1-§3](./05-p3-p4-interface.md) | mock P3 接受 Proto+feedback 返回 dummy GibbousCode | ✅ | ce7db25(`internal/bridge/mock` 包三档变体) |
| PB7 | 端到端验收 + 测试套 | [06-testing-strategy §2-§5](./06-testing-strategy.md) | **P2 总验收**:V1-V22 全过 | ✅ | b564443 + ddf5cfc + ecffcb9(Compile 接线 + e2e 测试 + lint 修复) |

### 1.1 P2 后续优化轮(原称 `P2+`)— 本轮全部融入 P2 主交付

| 项 | 内容 | 文档 | 完成定义 | 状态 | 关键提交 |
|---|---|---|---|---|---|
| #1 | 精确 yield 分析(stdlib 白名单 + known local 调用图传递) | [03 §3.2 + §9 GAP-1](./03-compilability-analysis.md) | type/tostring/math.*/string.*/table.* 等不再标 unknown;known local 含 yield 父也判 F2 | ✅ | a20b8a7(白名单 + 12 档单测);PB3 cf0a326 已落地 known local 真实现 |
| #2 | 阈值实测校准 | [01 §5 + 03 §3.5/§3.6](./01-profiling.md) | 阈值常量保留设计建议值,P3 真落地后实测校准 | ✅(占位) | a5ebef1(threshold 占位测试 + 漂移防御断言) |
| #3 | sync.Pool (C) 双表混合方案 | [01 §6.4](./01-profiling.md) | Proto 旁全局聚合表 + Flush + (C) 模式 considerPromotion | ✅ | 2fc7f5a(AggregateProfile + 4 档单测含多 State race-free 累积) |
| #4 | megamorphic 主动识别 | [02 §6.2](./02-ic-feedback.md) | ICSlot.Refill 计数 + 阈值翻 FBTableMega | ✅ | d70dfcd(Refill + 阈值 3 + 2 档单测) |
| #5 | F2-c ReasonSelfCall 占位位拆分(P4 PJ5 SELF inline 真接入前置)| [03 §4.2 visitMethodCallExpr](./03-compilability-analysis.md) | visitMethodCallExpr 不再硬叠 callsUnknownFn,改标 sawSelfCall + ReasonSelfCall 占位位;recheckCompilabilityRuntime 占位位扩到 (ReasonBackendUnsupp \| ReasonSelfCall);std_logger formatReasons F2 多位合并加 selfCall;analyzer_test 加 TestAnalyze_F2c_SelfCall 验占位位置位 + ReasonUnknownCall 不叠加 | ✅ | ee17319(2026-06-28,P4 PJ5 SELF method call inline 真接入前置)|
| #6 | forceAll retry window per-entry recheck dedup(issue #40 止血)| [04 §3.2 addendum 第 3 条](./04-try-compile-fallback.md) | retry window 内被后端拒收的 proto 每条回边重跑 recheckCompilabilityRuntime 全量分析(实测 22% CPU + 1.5 GB/op @ HeavyArith force);修复:pd.recheckedAtEntry 每次进入至多 recheck 一次,OnBackEdge 在每 pc count==1 / count==HotBackEdgeThreshold 两个升温里程碑再武装;state_machine_test 加 3 档(dedup 上限 / warm-IC 首回边升层不变 / entry-4 吸收不变)| ✅ | 本轮(2026-07-02,darwin/arm64 M5 Pro 实测 HeavyArith force 1125ms→49ms、fannkuch force 5.18ms→3.55ms、分配 1.5GB/op→124B/op)|

---

## 2. 跨文档回填请求收口表

P2 设计期各子文档对其他文档发起的回填请求,按收口阶段分类:

### 2.1 已兑现(设计期主助理直接收口)

| # | 来源 | 内容 | 兑现位置 |
|---|---|---|---|
| RB-3 | [03-compilability-analysis §10](./03-compilability-analysis.md) | 把 04 的「P2 是否需要保留 AST」缺口标关闭 | [00-overview §7 末尾](./00-overview.md) AST 协议定稿 ✅ |
| RB-4 | [03-compilability-analysis §10](./03-compilability-analysis.md) | 00 的「AST 保留协议悬而未决」标定稿 | [00-overview §7 + §9](./00-overview.md) ✅ |
| RB-5 | [03-compilability-analysis §10](./03-compilability-analysis.md) | `ProfileData` 加 `Compilable` / `Reasons` 字段 | [01-profiling §2.2](./01-profiling.md) ✅ |
| RB-6 | [03-compilability-analysis §10](./03-compilability-analysis.md) | 04 的 `considerPromotion` 入口检查 `Compilable` | [04-try-compile-fallback §3.2](./04-try-compile-fallback.md) ✅ |

### 2.2 P2 PB 实施期已兑现(2026-06-13 冲刺)

| # | 来源 | 内容 | 兑现 PB |
|---|---|---|---|
| RB-1 | [03 §10](./03-compilability-analysis.md) | `compile.Gen` 在产 Proto 后调 `bridge.AnalyzeProto`,`!profile` build tag 跳过 | ✅ PB7-1(`b564443` analyze_on.go / analyze_off.go) |
| RB-2 | [03 §10](./03-compilability-analysis.md) | 暴露 `ast.Walker`/`ast.Visitor` 接口供 P2 visitor 实现 | 不需要——visitor 直接在 bridge 包内手工递归 walk(`ce7db25` analyzer.go) |
| RB-7 | [03 §10](./03-compilability-analysis.md) | 把 `SupportsAllOpcodes(proto) bool` 加进 `P3Compiler` 接口 | ✅ PB0(`internal/bridge/p3compiler.go`) |
| RB-8 | [03 §10](./03-compilability-analysis.md) | 06 §3 可编译性误判注入 fuzz 覆盖 F1-F7 | 部分 ✅ PB3 单测覆盖 14 档形状(`cf0a326`);完整注入 fuzz 留 P2 后续优化轮(原 `P2+`)细化 |
| 06-T1 | [06 §11.1](./06-testing-strategy.md) | `bridge.Bridge` 暴露 `ConsiderPromotion` 公开方法 | 不暴露——通过 `OnEnter`/`OnBackEdge` 越阈值间接驱动(测试用 `for HotEntryThreshold` 循环);测试入口足够 |
| 06-T2 | [06 §11.1](./06-testing-strategy.md) | `bridge.ProfileDataOf(state, proto)` 测试 helper | ✅ `Bridge.ProfileOf(proto)` 已暴露(同语义) |
| 06-T3 | [06 §11.1](./06-testing-strategy.md) | `bridge.TrySetTierState` 测试入口 | 不需要——测试直接 `pd.TierState = ...` 操作 ProfileData 字段(同包测试可访问) |
| 06-T4 | [06 §11.1](./06-testing-strategy.md) | `mockP3RejectAll` / `mockP3DummyCompile` mock 工厂 | ✅ PB6(`internal/bridge/mock` 包三档变体) |
| 06-T5 | [06 §11.1](./06-testing-strategy.md) | `wangshu.NewStateWithBridge(cfg)` 测试入口 | 不需要——`wangshu.NewState` 内部已建 Bridge,wangshu_profile build 时自动生效 |
| 06-T6 | [06 §11.1](./06-testing-strategy.md) | `bridge.SetGlobalDiag(diag)` 测试钩 | ✅ `Bridge.SetLogger(l)` 已暴露(同语义) |

### 2.3 跨阶段(P3/P4 实施期)

| # | 来源 | 内容 | 实施阶段 |
|---|---|---|---|
| RB-7 | [03 §10](./03-compilability-analysis.md) | 把 `SupportsAllOpcodes(proto) bool` 加进 `P3Compiler` 接口 | ✅ 已落地(本期 PB0)——P3 PR 实现该方法即可 |
| 05-RB-1~12 | [05 §12](./05-p3-p4-interface.md) | 12 条对 P3/P4 文档的回填请求 | 留 P3/P4 实施期 |

---

## 3. 设计文档 §7 P1 前瞻义务的 PB0 同批补齐(实现现状对账修正)

设计文档 [00-overview §7](./00-overview.md) 声称 P1 已落地的若干前瞻义务,实代码扫描发现部分项目实际未落地——P2 PB0 同批补齐:

| 前瞻义务 | 文档原状态 | 实代码状态 | PB0 同批补齐 |
|---|---|---|---|
| 算术 IC 双计数(numHits/metaHits 挪用 shape/index/tableRef) | ✅ 已落地 | ❌ ICSlot 字段已建,但 doArith / UNM / LT/LE 路径**未写入** | ✅ PB2 commit `82881ea` 落地 |
| FORLOOP / 循环体 JMP 回跳的回边采样点 | ✅ 字节码层已暴露 | ❌ `vm.profileEnabled` 编译期常量不存在;`bridge.onBackEdge` 钩点不存在 | ✅ PB0 commit `4fe6321` 建 profileEnabled,PB1 commit `ade65f4` 接钩 |
| `enterLuaFrame` 函数入口采样钩 | ✅ 已落地 | ⚠️ enterLuaFrame 函数本身存在,但**钩点不存在** | ✅ PB1 commit `ade65f4` 接钩 |
| `Compile` 可编译性探测占位 | ✅ 已落地(恒「可解释」) | ✅ 真——`wangshu.Program` 公共 API 不变 | 无需补 |
| CallInfo bit50 gibbous 位 | ✅ 已落地(P1 恒 0) | ❌ callInfo 是普通 Go struct,无 word2 位打包 | ⚠️ PB0 commit-3 加占位 bool,但 lint 抓未使用,`ecffcb9` 删除——P3 trampoline 真落地时再加更合理(语义记在 installGibbous 注释里) |

**结论**:实现现状对账后,P2 PB0 不仅是「翻开关」,而是同批建立了上述钩点机制。设计文档 §7 的「✅ 已落地」声明在某些项目上是设计期的 wishful thinking,实际代码层面的 P2 PB0 = 「钩点首次建立 + 把 considerPromotion 占位」。文档 §7 已在本轮收尾保持原状,因为对未来读者来说「已落地」是当前状态(P2 PB0 之后)的事实。

---

## 4. 设计期决策盘点(影响 × 不确定度)

### 4.1 PB 实施期已验证

| 决策 | 设计稿 | PB 实施期落地形态 | 复核结果 |
|---|---|---|---|
| AST 保留协议 | 方案 ① Compile 时同步分析、缓存、AST 用完即弃 | PB7-1 `analyze_on.go`:wangshu_profile build 下 compile.Gen 调 bridge.AnalyzeProto,AST 在 Compile 函数返回后自然 GC | ✅ 验证可行 |
| 计数路线 | 路线 B 旁路计数 | PB1 实代码:`if profileEnabled { st.bridge.OnBackEdge(...) }` 编译期消去 | ✅ 默认 build 性能不退化(全套测试套通过) |
| 多 State profile 归属 | (B+C) 嵌套——(B) 默认 + (C) 接口预设 | PB0 实代码:profileTable 挂 State 私有(B);(C) 接口暂未预设(留 sync.Pool 实测后启用) | ✅ -race 通过(TestP2_ConcurrentStates_Race) |
| TierState 三态 | Interp / Gibbous / Stuck(单向 + 吸收) | PB4 实代码:`internal/bridge/tier.go`,无反向边 | ✅ 8 档状态机单测验证 |
| TierStuck 不重试 | 单次运行内永久 stuck | PB4 实代码:`considerPromotion` 入口守卫 `pd.TierState != TierInterp`;TestStateMachine_Idempotent_Stuck 验证后续永不调 Compile | ✅ |

### 4.2 实测后定标(留 P2 后续优化轮)

| 决策 | 当前 | 校准条件 |
|---|---|---|
| `HotBackEdgeThreshold = 1000` | 建议值,bridge 包常量 | P3 真落地后用 pineapple 真实负载校准 |
| `HotEntryThreshold = 200` | 同上 | 同上 |
| `MaxCompilableInsns = 2000` | 已采用,bridge 包常量 | P3 实测真实负载 90% 分位调整 |
| `MaxClosureDepth = 3`(F6) | 已采用,严保守 | P3 upvalue 编译协议成熟后放宽 |
| 算术 IC `confidence` 稳定阈值 = 0.99 | 已采用 | P4 实测 deopt 率反推 |
| 编译预算 pacing | 初版不做(STW) | 若实测 cold-start 长尾再加 |

---

## 5. P2 与 P1 implementation-progress 的差异

| 维度 | P1 implementation-progress | 本文(P2) |
|---|---|---|
| 当前状态 | 全卷已交付,持续维护后续轮次对账 | PB0-PB7 全过线,持续维护 P2 后续优化轮(原 `P2+`)对账 |
| 表格主体 | 实际落地的 PR / 提交哈希 / 时间线 | 同款 |
| 与设计文档的差异 | 已落地形态与设计文档的差异(如简化 / 留口) | 同款(§3 P1 前瞻义务对账修正、§4.2 阈值待校准) |
| 后续维护 | 每轮里程碑落地后追加对账行 | 每轮 P2 后续优化或 P3 接入时追加对账行 |

---

## 6. 后续维护协议

PB7 已落地,本文按以下协议更新:

1. P2 后续优化轮(精确 yield 分析 / 阈值校准 / (C) sync.Pool 双表混合启用 / fuzz 误判注入扩展 / megamorphic 主动识别;设计文档原称 `P2+`)落地时追加对账行;
2. P3 接入时把 mock P3 替换为真 wazero 后端,本文 §1 PB6 行追加「P3 真后端落地」对账;
3. 跨文档回填请求(§2)逐项实施,实施时把对应行从「⚠️ 待实施」改「✅ 已落地」。

---

相关:
- [00-overview](./00-overview.md)(P2 总览,本文是其 §4 PB 表的运行期对账)
- [01-profiling](./01-profiling.md)~[06-testing-strategy](./06-testing-strategy.md)(各子系统设计文档)
- [../p1-interpreter/implementation-progress](../p1-interpreter/implementation-progress.md)(P1 同款,作维护协议参考)
- [../../llmdoc/guides/multi-doc-drafting](../../../llmdoc/guides/multi-doc-drafting.md)(主动盘点不确定决策的纪律来源)
