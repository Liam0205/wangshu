# issue #13 公共面 API 缺口轮 4(typed-array + GlobalsSlot 快路径,parity-friendly)

- **日期**:2026-06-16
- **任务类型**:跨 consumer perf 调研 → 双向 issue(pineapple#112 + wangshu#13) → wangshu 侧 issue #13 落地
- **branch**:`feat/typed-array-and-globals-slot`(4 commits 在 master 上,单会话)

## 任务

用户初始问:「调研一下 pineapple 中使用 Lua 的方法,看看他们哪里用得不对、需要指导一下?以及看看我们能做什么来帮助他们提升运行效能。」pineapple 是 wangshu 首个 boundary-dominated 目标宿主(transform_by_lua operator)。调研走完后用户追问「我们自己这一侧没有什么可以优化的方式来适配 pineapple 这种用法吗」——本轮落地 wangshu 侧两件:

- **A 件** typed-array `NewFloat/Int64/Bool/StringArrayTable` 族:跳过 `[]Value` 中转直接 NaN-box 进 arena 数组段。脚本侧仍是普通 array table(`xs[i]`),**不是** arena 列轨的 `__index` 代理(`arena.xs[i]`),即 **parity-friendly**。`452ddb6`,12 个测试。
- **B 件** `GlobalsSlot` 预解析句柄:`State.GlobalsSlot(name)` + `SetBySlot/GetBySlot/Release()`,把 `gc.Intern([]byte(name))` 摊销到 Init 期。`0834647`,10 个测试。

机制走 `internal/crescent.State.SetGlobalByRef(arena.GCRef, Value)` 内部「by-ref」变体 + 公共 `GlobalsSlot` 经 `core.PinRef` 把 intern 后的 name GCRef 钉为 GC 根。`219475a`(README)+ `0d93486`(embedding-contract `§不强制 arena 的简易 API`)同步。`go test -race ./...` 全绿。

并行行动:filed pineapple#112(consumer 侧三条 unilateral 修复:per-engine cache `fn` / caller-owned `dst []any` mirror CallInto / pool `[]any` buffers)+ wangshu#13(A+B 件)。

## 预期 vs 实际

- 预期:[[public-api-incremental-delivery]] 9 条纪律的第 4 次应用(承 issue1/issue234/issue56 三轮),全机械复用 + 零阻塞,落地形态走既有模板(typed-array 仿 `NewArrayTable`,GlobalsSlot 仿 `SetGlobal/GetGlobal` + pin 表)。
- 实际:确实零阻塞,机械度高。两份 issue 一周内落地。但调研阶段揭示了一个**新的工作流维度**——「跨 consumer 的 perf 优化提议必须先读 consumer 的 llmdoc 反思 / 决策档,才能避免推荐已被它们实测且明确 deferred 的方案」;落地阶段则把 **parity-friendly 作为 wangshu 公共 API 的设计分类轴**显式入了 embedding-contract,这是 [[issue8-boundary-cost-round]]「实现浪费 vs 架构成本」框架的**消费侧对偶面**:`CallInto` 解的是「形态够不到 arena 列轨时仍要零分配」,issue #13 解的是「形态可走 arena 列轨但 consumer 受跨引擎字节对等约束不能改脚本」。

## 教训(每条首句为「下次什么场景会触发」)

### 1. 跨 consumer perf 调研先读 consumer 的反思/决策档,再起草自家侧的建议

**触发场景**:接到「调研 consumer X 用 wangshu 的方法 / 看怎么帮它提升运行效能」类调研任务,且 consumer 自己也维护 llmdoc 类工程化文档时。

本轮调研先把 pineapple 的 `llmdoc/reference/lua-backend.md` 与 `llmdoc/memory/reflections/wangshu-borrow-optimization-survey.md` 通读一遍,才发现:wangshu 端的 arena 列轨方案(`Program.Call(state, arena)`,边界成本 -46%)pineapple 不仅**评估过**,而且**测过数字**,并**已自觉 deferred**——因为采用它需要把 `lua_script` 访问形态从 `xs[i]` 改成 `arena.xs[i]`,这会破四引擎(Go/Java/C++/Python 编译器)`lua_script` 字节对等,parity-cost 比 perf 收益高。

若没先读这两篇而直接按「我们的强项是 arena 零拷贝」推荐,产出会是一份**已被 consumer 明确否决的方案的重复版**,既浪费往返也证明我们不读对方的工程档。

**修正纪律**:跨 consumer perf 调研的第一步永远是 `find consumer-repo/llmdoc -name "*.md"` + 读 reflections/decisions/doc-gaps——不是先 grep 它的 code、不是先跑 profile,是先读它「已经评估过什么、为什么 deferred」。这是把 consumer 的工程档当**真实约束的一手证据**(它告诉你哪条路 walk 过、走多远、为什么停),而非「nice-to-have 的背景资料」。

本轮的两份 issue 都把 consumer-side 反思 URL 在 issue body 里 cross-link 入档,使下一轮无论谁回看都能 5 秒找到「为什么是这个 spec 而不是 arena 列轨」。

**首次样本暂留观察**——下次再遇到「为 X consumer 调研优化机会」类任务时验证;若复发可促成 [[public-api-incremental-delivery]] 第 10 条「consumer-driven 优化先读 consumer 反思」或单独立 guide。

### 2. parity-friendly 作为 wangshu 公共 API 的设计分类轴——CallInto 的消费侧对偶面

**触发场景**:wangshu 接到「来自跨引擎 consumer 的 perf 优化提议 / drop-in 候选」类需求,且该 consumer 的 `lua_script` 在多个执行后端共用(Go/Java/C++/Python 等)时。

跨引擎 `lua_script` 字节对等是 pineapple 一类 consumer 的**硬约束**——它有 N 个执行后端,wangshu 是其中一个,任何一个后端要的 syntax wangshu 自己消化不了就破对等。这把 wangshu 公共 API 自然分成两类:

- **parity-breaking**:需要 consumer 改 `lua_script` syntax 才能用——例如 arena 列轨的 `arena.xs[i]` proxy。跨引擎 consumer **不能 unilaterally 采纳**,即使 perf 收益再大。
- **parity-friendly**:consumer 只改宿主端 Go 代码、`lua_script` 一字不动——例如 typed-array `NewXxxArrayTable`(脚本看到普通 array table `xs[i]`)、`GlobalsSlot`(脚本看到普通 `_G.k`)。跨引擎 consumer **可以 unilaterally 采纳**。

这是 [[issue8-boundary-cost-round]] 「实现浪费 vs 架构成本」框架的**消费侧对偶面**:那一轮区分的是 wangshu **自己**愿不愿优化(架构成本不优、实现浪费要优);本轮区分的是 **consumer** 在跨引擎约束下能不能采纳(parity-breaking 跨引擎拿不到、parity-friendly 跨引擎可白嫖)。两轴正交——`CallInto` 是「实现浪费消除 + parity-friendly」(consumer 改 Go 代码即可)、arena 列轨是「架构选择 + parity-breaking」(consumer 须改脚本)。

**落地形态**:embedding-contract `§不强制 arena 的简易 API` 已把 issue #13 段显式标注「parity-friendly,不破跨引擎 `lua_script` 字节对等」(`0d93486`)。

**首次样本暂留观察**——首次以 first-class 分类轴入档;P2+/P3+ 若再接「为某跨引擎 consumer 提建议」类任务可验证是否仍成立。复发后促成 [[embedding-contract]] 加显式「parity-friendly vs parity-breaking」分类节,或单独 reference 一篇。

### 3. 「ByRef internal + opaque public handle」是热循环 key 摊销的可复用 pattern

**触发场景**:公共 API 暴露的快路径会在热循环里反复 intern / hash / 解析某个固定 key(name / 字符串字面量 / 路径 / fielddesc),且 key 在循环内不变时。

B 件的机制可抽象成一个小 pattern:

1. **internal 暴露「by-ref」变体**:`crescent.State.SetGlobalByRef(nameRef arena.GCRef, v Value)`——接已预解析好的 GCRef 而非 string,跳过 intern。
2. **公共面用 opaque handle 包装**:`GlobalsSlot` struct 内部钉 GCRef + 关联 `*State`(防跨 State 误用),Release 把 GCRef 从 pin 表撤下。
3. **lifecycle**:Init 期一次性 `state.GlobalsSlot(name)` 解析 + pin 接 GC 根,热路径 `SetBySlot/GetBySlot` 走零分配 by-ref 路径,Shutdown 期 `slot.Release()`。

理论上同款 pattern 还能套到 `Table.GetSlot(key) → tableSlot` 类(把 IC slot 暴露给宿主)、`Path.Resolve("foo.bar.baz") → pathSlot` 类(把 lookup 路径预解析)等等。

**首次样本暂留观察**——单实例没到立 pattern 的阈值;若 P2+/P3+ 接到「Table 反复读同一 key」「path lookup 反复」类 issue 再次套用,可促成 [[public-api-incremental-delivery]] 加一条 pattern 节或并入 [[design-claims-vs-codebase-physics]] 「成本归类」维度。

### 4. consumer 的「评估了且自觉 deferred」反思是黄金一手证据,wangshu 应同款维护

**触发场景**:做长期项目里「已知备选方案 A 我们没采纳」的记录时(尤其当 A 已被实测有数字、有 deferred 原因)。

pineapple 的 `wangshu-borrow-optimization-survey.md` 是一篇模范——它**测了** arena 列轨的边界成本(-46%)、**列了** parity-cost、**说清** 为什么 defer。任何下一个评估「pineapple 要不要上 arena 列轨」的人,5 分钟就能拿到「数字 / 理由 / 当前阻塞 / 还开的备选路径」全部上下文,不用从零跑 benchmark。

wangshu 端类似机制是 `memory/doc-gaps.md`「**已收口(留作审计)**」节(已彻底解决的旧缺口)+ 「【...审计的四项负债】」类「评估了不收口」条目。本轮 issue #13 也属此族——arena 列轨被 consumer 评估且 deferred 是事实,wangshu 应在 embedding-contract / doc-gaps 加一条「arena 列轨方案的 consumer-side 采纳现状」备查项,而非散落在 issue/PR comments 里。

**首次样本暂留观察**——已落地形态见 embedding-contract issue #13 段对 parity-friendly 的标注 + 本反思,但 doc-gaps 侧的「arena 列轨 consumer 采纳现状」专项条目尚未入档;若 P2+/P3+ 再撞到「已评估 + 已 deferred」类决策可批量入档。

## 缺失的文档或信号

- 调研开头的「先读 consumer llmdoc 反思」无显式 guide 指针——本轮靠常识 walk 进 pineapple repo 找的;若初学者按「先读 consumer 的 code 看它怎么用 wangshu API」操作,会错过 consumer 已自决的方案选择,产出一份重复劳动建议。
- [[embedding-contract]] `§不强制 arena 的简易 API` 段已把 issue #13 标注 parity-friendly,但**整篇没有 parity-friendly vs parity-breaking 的总览定义**——本轮在 issue 段就地标注是局部解,缺概念级 anchor 让后续 API 设计者自检「我加的这条是 parity 哪一边」。
- doc-gaps 缺「arena 列轨方案在 pineapple 已 deferred」专项条目;当前只有 embedding-contract issue #13 段顺带提了一句「不破跨引擎字节对等」。

## Promotion 候选

### 留在 memory 观察(均首次样本)

- **教训 1**:「跨 consumer perf 调研先读 consumer 反思」——首次样本,工作流类纪律;若下一轮跨 consumer 调研复发,候选促成 [[public-api-incremental-delivery]] 第 10 条「consumer-driven 优化先读 consumer 工程档」或独立小 guide「cross-project workflow」。
- **教训 2**:「parity-friendly 作为 API 分类轴」——首次以 first-class 分类轴入档(顺便在 embedding-contract issue #13 段已就地标注);若 P2+/P3+ 再接跨引擎 consumer 类 issue 可促成 [[embedding-contract]] 加「parity-friendly vs parity-breaking」总览节,或独立 reference「API parity classification」。
- **教训 3**:「ByRef internal + opaque public handle」pattern——单实例,远未到立 pattern 阈值;P2+/P3+ 若 Table.GetSlot / Path.Resolve 类机制再次套用可促成 pattern 入 [[public-api-incremental-delivery]]。
- **教训 4**:wangshu 端「已评估 + 已 deferred」类条目机制——本轮局部入了 embedding-contract,doc-gaps 侧专项尚未入;P2+/P3+ 再撞同形态可批量入档。

### 不升入既有 guide

- [[public-api-incremental-delivery]] 9 条纪律全部按既有形态机械复用过线,本轮**不新增 guide 条目**(三轮验证已稳定为肌肉记忆,见 issue56 教训 4)。

## 关联

[[issue8-boundary-cost-round]](实现浪费 vs 架构成本——本轮 parity-friendly 是消费侧对偶面)·
[[issue1-api-gap-round]] / [[issue234-api-gap-round-2]] / [[issue56-api-gap-round-3]](同款工作流前三轮)·
[[public-api-incremental-delivery]](本轮第 4 次机械复用,零新增纪律,稳定肌肉记忆)·
[[embedding-contract]](`§不强制 arena 的简易 API` 已把 issue #13 段标注 parity-friendly)·
pineapple#112(consumer 侧三条 unilateral 修复)+ wangshu#13(本轮 wangshu 侧 A+B 件 落地)
