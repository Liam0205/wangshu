# Wangshu llmdoc 文档地图

> 项目状态:**纯设计阶段**。仓库唯一实质内容是设计文档 `docs/design/roadmap.md`,无任何代码实现。本文档库忠实记录的是**设计意图与规划**,不是已交付能力。
> 启动阅读顺序请看 [[startup]](本文件不重复有序启动清单)。

## 类别用途

- **`must/`** —— 每次任务都应先读的微型启动文档。只放跨任务、稳定、几乎每次都用得上的知识。
- **`overview/`** —— 项目/大特性的身份、边界与角色。
- **`architecture/`** —— 检索地图、所有权边界、流程与不变式。
- **`guides/`** —— 一篇一个工作流。**当前为空**(设计阶段,尚无可操作工作流;有了实现/构建/测试流程再补)。
- **`reference/`** —— 稳定查阅事实:契约、schema、约定、术语。
- **`memory/`** —— 历史过程记忆。`memory/reflections/` 归 reflector 所有;`memory/decisions/` 与 `memory/doc-gaps.md` 归 recorder 所有。当前 reflections 与 decisions **均为空**。

## 现有文档与路由提示

### must/
- [[design-premises]] — **最重要的 MUST**。四组不可妥协前提:① 列内核负载形状(两个校准测量:LuaJIT 仅比 luajc 快 6%;生产端到端被稀释到 ±5-7% 噪声)② Go runtime 四项税 → 边界几十~百 ns 固定成本 ③ 五条贯穿原则 ④ 第一天 NaN-boxing 值表示承诺。**判断任何提案是否合理前先读这篇。**

### overview/
- [[project-overview]] — 项目身份与边界:望舒/Lua=月亮意象、纯 Go 嵌入式 Lua VM 定位、三层目标(Lua 5.1 核心)、三项非目标(§6)、首个目标宿主(多运行时规则引擎)、当前状态。**想知道「这是什么、不做什么」看这篇。**

### architecture/
- [[evolution-roadmap]] — 分层 VM 五阶段流水线 P1→P5(人力/倍率/验收/前置 spike)、月相 tier 命名映射。**问演进路线、阶段门槛、wazero<150ns spike、crescent/gibbous/fullmoon 看这篇。** 注意流水线图倍率与正文验收门槛不在同一坐标系。
- [[value-representation]] — 值表示与内存模型:NaN-boxing vs Go tagged struct 决策、自管 arena、自写 mark-sweep GC、同一块内存使编译层成增量。**问值/内存/GC/为什么这样选看这篇。**

### reference/
- [[embedding-contract]] — 宿主嵌入契约:`Compile→Program`、`Program.Call(arena,args)`、arena ABI(类型化扁平列 + 字符串区 + presence bitmap,零拷贝读)、per-item 简易 API、drop-in 定位。**问宿主怎么嵌入、API 形状看这篇。**
- [[glossary]] — 术语表 + prior art 借鉴点。**遇到 NaN-boxing/arena/tier/月相/deopt/列内核等术语,或问参照项目看这篇。**

### memory/
- `memory/doc-gaps.md` — 已识别的文档缺口(随实现推进收敛)。
- `memory/decisions/` — 决策记录(recorder 所有),当前为空。
- `memory/reflections/` — 反思记录(reflector 所有),当前为空。

---

源设计文档:`docs/design/roadmap.md`(引用时用 `docs/design/roadmap.md` (§N))。
