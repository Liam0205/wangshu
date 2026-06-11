# Wangshu llmdoc 文档地图

> 项目状态:**设计文档集全卷齐备,无代码实现**。`docs/design/` 共 19 篇约 1.37 万行(P1 全卷 00-12 可实现深度、P2/P3 详细设计、P4/P5 架构决策)。本文档库是设计文档之上的**知识压缩层**,记录设计意图与路由,不是已交付能力。
> 启动阅读顺序请看 [[startup]](本文件不重复有序启动清单)。

## 类别用途

- **`must/`** —— 每次任务都应先读的微型启动文档。只放跨任务、稳定、几乎每次都用得上的知识。
- **`overview/`** —— 项目/大特性的身份、边界与角色。
- **`architecture/`** —— 检索地图、所有权边界、流程与不变式。
- **`guides/`** —— 一篇一个工作流。
- **`reference/`** —— 稳定查阅事实:契约、schema、约定、术语。
- **`memory/`** —— 历史过程记忆。`memory/reflections/` 归 reflector 所有;`memory/decisions/` 与 `memory/doc-gaps.md` 归 recorder 所有。

## 现有文档与路由提示

### must/
- [[design-premises]] — **最重要的 MUST**。四组不可妥协前提:① 列内核负载形状(两个校准测量:LuaJIT 仅比 luajc 快 6%;生产端到端被稀释到 ±5-7% 噪声)② Go runtime 四项税 → 边界几十~百 ns 固定成本 ③ 五条贯穿原则 ④ 第一天 NaN-boxing 值表示承诺。**判断任何提案是否合理前先读这篇。**

### overview/
- [[project-overview]] — 项目身份与边界:望舒/Lua=月亮意象、纯 Go 嵌入式 Lua VM 定位、三层目标(Lua 5.1 核心)、三项非目标(§6)、首个目标宿主(多运行时规则引擎)、当前状态。**想知道「这是什么、不做什么」看这篇。**

### architecture/
- [[evolution-roadmap]] — 分层 VM 五阶段流水线 P1→P5(人力/倍率/验收/前置 spike)、月相 tier 命名映射。**问演进路线、阶段门槛、wazero<150ns spike、crescent/gibbous/fullmoon 看这篇。** 注意流水线图倍率与正文验收门槛不在同一坐标系。
- [[value-representation]] — 值表示与内存模型:NaN-boxing vs Go tagged struct 决策、自管 arena、自写 mark-sweep GC、同一块内存使编译层成增量。**问值/内存/GC/为什么这样选看这篇。**

### reference/
- [[embedding-contract]] — 宿主嵌入契约:`Compile→Program`、`Program.Call(arena,args)`、arena ABI(类型化扁平列 + 字符串区 + presence bitmap,零拷贝读)、per-item 简易 API、drop-in 定位。字段级 spec 在 `docs/design/p1-interpreter/11-embedding-arena-abi.md`。**问宿主怎么嵌入、API 形状看这篇。**
- [[glossary]] — 术语表 + prior art 借鉴点。**遇到 NaN-boxing/arena/tier/月相/deopt/列内核等术语,或问参照项目看这篇。**

### guides/
- [[multi-doc-drafting]] — 多文档并行起草工作流:回填请求节协议、单点收口、验收口径收口点指定、子代理失败恢复纪律、收尾主动盘点不确定决策、向用户提问自包含契约。**要一次起草多篇互引文档、或大型设计任务收尾时看这篇。**

### memory/
- `memory/doc-gaps.md` — 已识别的文档缺口与待外部确认事项(随实现推进收敛;含已收口审计记录)。
- `memory/decisions/2026-06-11-design-review-decisions.md` — 设计评审轮 7 项裁决归档(验收 oracle 改官方 5.1.5、stdlib 反转对齐 gopher 提供面+三层禁用、ColInt64 超界报错、luac 同构软承诺等),每项含设计文档落点指针。**查「某决策为什么这样定、落在哪」先看这篇。**
- `memory/reflections/2026-06-11-design-doc-completion.md` — 设计文档集补齐(P1 全卷 + P2-P5)的过程反思:并行起草+单点收口模式、子代理中断恢复教训。
- `memory/reflections/2026-06-11-design-review-round.md` — 设计评审决策轮过程反思:主动盘点不确定决策的收益、裁决后即时 grep 同步、AskUserQuestion 自包含教训。

---

## 设计文档集路由(llmdoc 之外的源文档)

- **入口**:`docs/design/architecture.md` §0 是文档集地图(包布局/组件图/tier 映射);`docs/design/p1-interpreter/00-overview.md` 是 P1 施工计划与**跨文档定稿决策速查**(§4)。
- **战略层**:`docs/design/roadmap.md`(引用时用 `docs/design/roadmap.md` (§N))。
- **验收口径**:`docs/design/p1-interpreter/12-testing-difftest.md` §10 是 26 条验收口径总表(所有「待定口径」的收口点;评审轮新增第 26 条 ColInt64,勿引用旧版 25 条说法)。
- llmdoc 不搬运设计文档内容,只做压缩与路由;深入实现细节一律回源文档。
