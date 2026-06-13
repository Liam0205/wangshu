# P2 实现进度对账(implementation-progress)

> 状态:**P2 设计阶段,实现未启动**。P1 已交付(M0-M14 全过线),P2 PB0 待启动。
> 单一事实源:本文是 P2 实现现状与设计文档差异的对账表(对应 P1 [implementation-progress.md](../p1-interpreter/implementation-progress.md) 的角色)。
> 设计文档集:见 [00-overview](./00-overview.md) §0 文档地图。

---

## 0. 当前状态

**P2 实现:0%,所有 PB(P-Bridge 里程碑)未启动。** 设计文档集已齐备(00-06 共 7310 行,含 §1-§9 完整论证 + Go 代码骨架)。

**前置条件检查**:
- ✅ P1 全卷已交付(M0-M14 + 所有收尾轮 + 长稳承诺轮 + 外部审查修复轮 + 官方测试套与性能轮)
- ✅ P1 期间的前瞻义务全部已落地(详见 [00-overview §7](./00-overview.md))
- ✅ P2 设计文档完整(00 总览 + 01 计数 + 02 IC 反馈 + 03 可编译性 + 04 状态机 + 05 P3/P4 接口 + 06 测试)
- ⏳ P2 PB0(`internal/bridge` 包骨架)待启动

---

## 1. 里程碑进度对账(对应 [00-overview §4](./00-overview.md))

| PB | 内容 | 文档 | 完成定义 | 状态 |
|---|---|---|---|---|
| PB0 | `internal/bridge` 包骨架 + ProfileData 字段 + State 私有 profileTable + `vm.profileEnabled` 翻 true | [01-profiling §1-§3](./01-profiling.md) | bridge 包独立编译;P1-only 部署关 profileEnabled 仍 byte-equal | ⏳ 待启动 |
| PB1 | 回边/入口采样点接入(FORLOOP/JMP 回跳 + enterLuaFrame) | [01-profiling §2-§4](./01-profiling.md) | 三档脚本 profile 累积非零;profile 开关切换不改 byte-equal | ⏳ |
| PB2 | IC 反馈聚合器(读全部 IC kind + 算术双计数比例) | [02-ic-feedback §3-§4](./02-ic-feedback.md) | 一组合成脚本聚合产出对应 `FeedbackKind`;`confidence` 单测 | ⏳ |
| PB3 | 静态可编译性分析器(F1-F7 AST visitor) | [03-compilability-analysis §3-§5](./03-compilability-analysis.md) | F1-F7 各形状测试断言;**不可编译形状的零误判注入 fuzz** | ⏳ |
| PB4 | TierState 状态机 + considerPromotion 入口 + TierStuck 不重试 | [04-try-compile-fallback §2-§4](./04-try-compile-fallback.md) | 状态转移单测;TierStuck 不再触发(防抖断言) | ⏳ |
| PB5 | 升层日志格式 + 诊断接口 | [04-try-compile-fallback §6](./04-try-compile-fallback.md) | 三类日志文本断言 | ⏳ |
| PB6 | P3/P4 接口实现 + mock P3 编译器 | [05-p3-p4-interface §1-§3](./05-p3-p4-interface.md) | mock P3 接受 Proto+feedback 返回 dummy GibbousCode | ⏳ |
| PB7 | 端到端验收 + 测试套 | [06-testing-strategy §2-§5](./06-testing-strategy.md) | **P2 总验收**:V1-V22 全过 | ⏳ |

---

## 2. 跨文档回填请求收口表

P2 设计期各子文档对其他文档发起的回填请求,按收口阶段分类:

### 2.1 已兑现(设计期主助理直接收口)

| # | 来源 | 内容 | 兑现位置 |
|---|---|---|---|
| RB-3 | [03-compilability-analysis §10](./03-compilability-analysis.md) | 把 04 的「P2 是否需要保留 AST」缺口标关闭 | [00-overview §7 末尾](./00-overview.md) AST 协议定稿 ✅ |
| RB-4 | [03-compilability-analysis §10](./03-compilability-analysis.md) | 00 的「AST 保留协议悬而未决」标定稿 | [00-overview §7 + §9](./00-overview.md) ✅ |
| RB-5 | [03-compilability-analysis §10](./03-compilability-analysis.md) | `ProfileData` 加 `Compilable` / `Reasons` 字段 | [01-profiling §2.2](./01-profiling.md) ✅ |
| RB-6 | [03-compilability-analysis §10](./03-compilability-analysis.md) | 04 的 `considerPromotion` 入口检查 `Compilable` | [04-try-compile-fallback §3.2](./04-try-compile-fallback.md) ✅(子代理起草时已实现) |

### 2.2 P2 PB 实施期兑现

| # | 来源 | 内容 | 实施 PB |
|---|---|---|---|
| RB-1 | [03-compilability-analysis §10](./03-compilability-analysis.md) | 04 §1.X 加「P2 启动时落地」AST 接口约定:`compile.Gen` 在产出 Proto 后调 `bridge.AnalyzeProto`,`!profile` build tag 跳过 | PB0 启动时同批改 [P1 04](../p1-interpreter/04-frontend-parser-codegen.md) |
| RB-2 | [03-compilability-analysis §10](./03-compilability-analysis.md) | 04 §3.X 暴露 `ast.Walker`/`ast.Visitor` 接口供 P2 visitor 实现 | PB3 启动时验证 P1 04 的 visitor 接口形态 |
| RB-8 | [03-compilability-analysis §10](./03-compilability-analysis.md) | 06 §3 可编译性误判注入 fuzz 覆盖 F1-F7 | PB7 验收时实施 |
| 06-T1 | [06-testing-strategy §11.1](./06-testing-strategy.md) | `bridge.Bridge` 暴露 `ConsiderPromotion` 公开方法供测试调 | PB6/PB7 实施 |
| 06-T2 | [06-testing-strategy §11.1](./06-testing-strategy.md) | `bridge.ProfileDataOf(state, proto)` 测试 helper | PB6/PB7 |
| 06-T3 | [06-testing-strategy §11.1](./06-testing-strategy.md) | `bridge.TrySetTierState` 测试入口(状态机不变量直接验) | PB4/PB7 |
| 06-T4 | [06-testing-strategy §11.1](./06-testing-strategy.md) | `mockP3RejectAll` / `mockP3DummyCompile` mock 工厂 | PB6 |
| 06-T5 | [06-testing-strategy §11.1](./06-testing-strategy.md) | `wangshu.NewStateWithBridge(cfg)` 测试入口 | PB7 |
| 06-T6 | [06-testing-strategy §11.1](./06-testing-strategy.md) | `bridge.SetGlobalDiag(diag)` 测试钩 | PB5/PB7 |

### 2.3 跨阶段(P3/P4 实施期)

| # | 来源 | 内容 | 实施阶段 |
|---|---|---|---|
| RB-7 | [03-compilability-analysis §10](./03-compilability-analysis.md) | 把 `SupportsAllOpcodes(proto) bool` 加进 `P3Compiler` 接口 | P3 PR 上线时 |
| 05-RB-1~12 | [05-p3-p4-interface §12](./05-p3-p4-interface.md) | 12 条对 P3/P4 文档的回填请求(P3Compiler 接口 / trampoline 入口 / mock 永久并存 / P4Feedback / RequestRefresh / P3DeoptNotifier 等) | task #12「P3/P4 接口面对齐」收口 |

---

## 3. 设计期决策盘点(影响 × 不确定度)

按 [multi-doc-drafting guide](../../../llmdoc/guides/multi-doc-drafting.md) 「主动盘点不确定决策」纪律,设计期主助理与子代理拍板但需用户复核或后续实测验证的关键决策:

### 3.1 影响 PB 开工形态(高影响 / 中不确定度)

| 决策 | 定稿 | 出处 | 复核点 |
|---|---|---|---|
| AST 保留协议 | 方案 ① Compile 时同步分析、缓存、AST 用完即弃 | [00 §7](./00-overview.md) / [03 §2.4](./03-compilability-analysis.md) | PB0 启动时验证 P1 04 改动可行 |
| 计数路线 | 路线 B 旁路计数(不改 P1 字节码 0..37) | [01 §3](./01-profiling.md) | PB1 实测 onBackEdge 开销;若 ~24ns 估算偏差大于 2x 重审 |
| 多 State profile 归属 | 方案 **(B+C) 嵌套**:(B) 默认运行(profileTable 挂 State 私有)+ (C) 接口预设占位实现(Proto 旁聚合表 nop)。**用户裁决采纳预设 (C) 接口,避免事后改接口** | [01 §6](./01-profiling.md) | PB7 实测 pineapple sync.Pool 形态;若累积速度均分严重影响热阈值生效 → 替换 (C) nop 为真聚合(接口零修改) |
| TierState 三态 | Interp / Gibbous / Stuck(单向 + 吸收) | [04 §2](./04-try-compile-fallback.md) | 形式化验证 + V11-V13 |
| TierStuck 不重试 | 单次运行内永久 stuck;跨版本由进程重启自然重评估 | [04 §7](./04-try-compile-fallback.md) | 实测漏判面是否大到需要 stuck reset API |

### 3.2 依赖外部数据(中影响 / 高不确定度)

| 决策 | 当前 | 校准条件 |
|---|---|---|
| `HotBackEdgeThreshold = 1000` | 建议值 | PB7 用 pineapple 真实负载(rule eval 1000 item/批次)校准 |
| `HotEntryThreshold = 200` | 建议值 | 同上 |
| `MaxCompilableInsns`(F5 过大函数阈值) | 待 PB3 设值(暂未在 03 给具体数) | 实测 |
| `MaxClosureDepth = 3`(F6) | 偏严保守 | 等 P3 upvalue 编译协议成熟后放宽 |
| 算术 IC `confidence` 稳定阈值 = 0.99 | 工程估算 | P4 实测 deopt 率反推 |
| 编译预算 pacing | 初版不做(STW 越阈值即尝试) | 若实测 cold-start 长尾再加 |
| `installGibbous` 多 State 同步 | (B) 单 State 写;(C) 启用时加 sync.Once | 同 (C) 启用条件 |
| panic recover 上抛策略 | 日志 message 带 `panic:` 前缀 + CI 抓 | 生产监控接入时定 prometheus 指标格式 |

### 3.3 低风险已记录(低影响 / 已记缺口)

[00-overview §9](./00-overview.md) 风险与未决缺口汇总 + 各文档 §11/§9 缺口节,共记录约 30 条次要缺口,均不阻塞 PB 启动。

---

## 4. P2 与 P1 implementation-progress 的差异

P1 [implementation-progress.md](../p1-interpreter/implementation-progress.md) 的形态(已落地多轮 + 后续轮次对账)与本文不同:

| 维度 | P1 implementation-progress | 本文(P2) |
|---|---|---|
| 当前状态 | 全卷已交付,持续维护后续轮次对账 | 设计阶段,实现未启动 |
| 表格主体 | 实际落地的 PR / 提交哈希 / 时间线 | 设计阶段决策对账 + 待实施回填请求 |
| 与设计文档的差异 | 已落地形态与设计文档的差异(如简化 / 留口) | (无差异——尚未实施) |
| 后续维护 | 每轮里程碑落地后追加对账行 | PB0 启动后逐 PB 落地时追加进度行 |

---

## 5. 后续维护协议

PB0 启动后,本文按以下协议更新:

1. 每个 PB 完成时,把对应行的 `⏳` 改 `✅`,加完成提交哈希;
2. 实际落地与设计文档有差异时,在 §6 加「实现现状与设计文档差异对账表」(对应 P1 同款节);
3. PB7 总验收过线后,本文头部状态改「P2 已交付」+ 验收数字汇总(虽然 P2 是基建无性能门,但记录决策正确性验收成绩);
4. 跨文档回填请求(§2)逐项实施,实施时把 `⏳ 待实施` 改 `✅ 已落地` + 对应提交哈希。

---

相关:
- [00-overview](./00-overview.md)(P2 总览,本文是其 §4 PB 表的运行期对账)
- [01-profiling](./01-profiling.md)~[06-testing-strategy](./06-testing-strategy.md)(各子系统设计文档)
- [../p1-interpreter/implementation-progress](../p1-interpreter/implementation-progress.md)(P1 同款,作维护协议参考)
- [../../llmdoc/guides/multi-doc-drafting](../../../llmdoc/guides/multi-doc-drafting.md)(主动盘点不确定决策的纪律来源)
