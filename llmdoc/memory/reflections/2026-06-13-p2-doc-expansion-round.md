---
name: p2-doc-expansion-round
description: P2 单文件设计稿扩展为子目录详细设计的过程教训(multi-doc-drafting 二轮验证 + 子代理工具卡死的 heredoc 兜底)
metadata:
  type: reflection
  date: 2026-06-13
---

# P2 文档集扩展轮反思(P2 单文件 → 子目录 8 文件)

> 范围:`docs/design/p2-bridge.md`(703 行单文件)扩展为 `docs/design/p2-bridge/`(子目录 8 文件 7453 行)。仿 P1 13 篇详细设计 + 00-overview 形式,补 PB0-PB7 里程碑、人月分解、实现级代码骨架、F1-F7 visitor 设计、跨文档回填请求收口表。驱动:用户 goal「针对 P2,仿照 P1 的做法,将 P2 设计稿扩展为详细的施工指南」。
>
> 工作流:multi-doc-drafting guide 第二次实战验证(P1 首次 = `2026-06-11-design-doc-completion`)。

## 核心教训

### 1. multi-doc-drafting guide 三大核心协议二轮验证有效

P1 19 篇文档起草轮总结的「并行起草、单点收口 / 回填请求节协议 / 指定唯一验收口径收口点」三条核心协议,本轮在 P2 8 篇扩展中**再次零失败兑现**:

- **并行起草**:第一批 3 篇(01/02/03)+ 第二批 3 篇(04/05/06)分两批并行,主助理收口。每篇独立子代理 prompt 含必读上游清单 + 风格基线 + 回填请求约定 + 章节大纲 + 不变式约束——子代理首次产出即合规率 ~95%,主助理只做头尾核验与决策研判。
- **回填请求节**:子代理共提 30 余条回填请求(03 RB-1~8、05 RB-1~12、06 GAP-T 7 条等),主助理在收口阶段统一审视:已兑现 4 条(RB-3/4/5/6)、PB 实施期兑现 8 条、跨阶段(P3/P4)兑现 12 条——全部记录在 `implementation-progress.md §2` 收口表。
- **唯一验收口径收口点**:06-testing-strategy 的 V1-V22 22 条对应 P1 12-testing-difftest §10 角色,00-overview §8 指向它;子代理们把各自的不变式翻成 V<n> 验收口径,无打架。

**Why**:P1 首次验证时还有「子代理中断恢复」教训(原反思 §1),本轮更稳——一样的工作流第二次跑零阻塞,验证机制级稳定,而非首轮巧合。

**How to apply**:任何后续大型文档起草任务(如 P3/P4 详细设计扩展)**直接复用本工作流**,不再需要预演——但每次任务前**仍要主助理亲写 00-overview**(纲领必须主助理定下来,不能并行外包,因为它定下后续所有子文档的派生约束)。

### 2. 子代理工具卡死的 Bash heredoc 兜底是救命招

本轮发现一个**新教训**:子代理 04-try-compile-fallback 跑到 §6.6 时(851 行),Write/Edit 工具卡住不响应,但 Bash 工具可用。子代理在自报告里写「Bash works. The Write/Edit tools seem stuck. I'll write the document via Bash heredoc in chunks.」——这是它**自救成功**的关键决策。

**Why 工具卡死**:子代理工具调用次数累积到一定量后,Write/Edit 可能进入异常状态(具体根因未深究,可能是 token / 锁 / 超时类),但 Bash heredoc(`cat >> file <<'EOF' ... EOF`)走不同的代码路径,避开了卡点。

**How to apply**:
- 子代理的 prompt 应**预留 Bash heredoc 兜底协议**——「若 Write/Edit 工具卡住,改用 `cat >> file <<'EOF' ... EOF` 续写,EOF 标记不要被内容里的 EOF 干扰」。本轮 04 的续写 prompt 已加了这条。
- 主助理在「子代理停滞但可能仍在写」时,**先 Read 文件实际状态**,而非依赖子代理报告——这次 04 报告 438 行卡住,实际已落盘 851 行(子代理偷偷续写但没 ack)。
- **「停子代理 + 派新子代理续写」是合法救援动作**:用户提示「把子代理停掉,然后启动一个新的子代理续写」是教训级正确——multi-doc-drafting guide §2「子代理失败恢复纪律」里「同一篇连续失败 2-3 次改主助理亲写」是上限,但**先尝试新子代理续写一次**(不重写、只追加)是更优的中间档,避免过早接管。

### 3. 拆子目录前必须做引用面普查

P2 单文件 `p2-bridge.md` 被 14 个外部文档引用(P1 04/02/05/architecture/llmdoc/p3-p5/doc-gaps),且很多带具体章节号(如 `[p2-bridge](./p2-bridge.md) §3.4`)。删除原文件后:

- **简单引用**(无章节号):`[p2-bridge](./p2-bridge.md)` → `[p2-bridge](./p2-bridge/00-overview.md)` 直接 sed 批量。
- **章节级引用**:`§3.4` 的语义是单文件原稿的 §3.4 节,不能直接指向 `00-overview.md` 的 §3.4(00-overview 没那些 §)——必须**按章节映射表**改成对应子文档。本轮做了完整映射:§2 热度→01-profiling、§3 IC→02-ic-feedback、§4 可编译性→03-compilability-analysis、§5/§6 状态机→04-try-compile-fallback、§7 接口→05-p3-p4-interface、§8/§9 不变式/缺口→00-overview §9。

**Why**:章节号引用错位是典型的「文档碎片化」陷阱——拆分时如果只改链接 URL 不改章节号,读者点进去找不到对应章节,等于断链。

**How to apply**:任何「单文件 → 子目录」拆分前,**先 grep 全部带 §X 的引用**,做章节映射表;sed 时按映射逐条替换,而非「全部指向总览」。本轮的章节映射表见 `docs/design/p3-wasm-tier.md` / `p4-method-jit.md` / `p5-trace-jit.md` 修改后的引用。

### 4. AST 保留协议三选一是子代理定下来 + 用户裁决的典型案例

03-compilability-analysis §2.4 给了 AST 保留协议三选一(① Compile 同步分析 / ② AST 留存接口 / ③ 重 parse),子代理倾向 ①(接口稳定 / AST 短命 / 零开销 / 错误一致性)。主助理收口时把它列为 **AskUserQuestion 的「高影响 × 中不确定度」** 决策,用户裁决采纳 ①。

但**多 State profile 归属**子代理倾向 (B) 私有,用户裁决改成了 **(B+C) 嵌套**——预设 (C) 接口避免事后改接口。这是一次「子代理保守 → 用户偏激进 → 接口预设」的正向调整。

**Why**:子代理的定下来倾向是「按当前需求选最简方案」(YAGNI),用户作为长期持有者更看重「未来扩展接口零成本」(预设接口比事后扩接口便宜 100 倍)。这两种视角在大型设计期都需要,**子代理定下来 + 用户裁决是健康的协作模式**。

**How to apply**:大型起草任务收尾时,主助理用 multi-doc-drafting guide §3「主动盘点不确定决策」纪律,把子代理定下来但「影响 × 不确定度」高的决策列出来,用 `AskUserQuestion` **自包含地**问用户(每个选项的具体后果要写清,不留指代——P1 设计评审轮反思教训)。本轮 3 条决策(AST 协议 / profile 归属 / P1 04 改时机)用一次 AskUserQuestion 收口,用户裁决完成无歧义。

## 促成的稳定文档更新

- `docs/design/p2-bridge/`(新建子目录,8 文件 7453 行——00-overview / 01-profiling / 02-ic-feedback / 03-compilability-analysis / 04-try-compile-fallback / 05-p3-p4-interface / 06-testing-strategy / implementation-progress)
- `docs/design/p2-bridge.md` 删除(被子目录替代)
- `docs/design/architecture.md` / `p3-wasm-tier.md` / `p4-method-jit.md` / `p5-trace-jit.md` 引用面更新(章节映射到子文档)
- `llmdoc/index.md` / `startup.md` / `architecture/evolution-roadmap.md` P2 状态从「单文件设计稿」→「详细设计齐备(子目录)」
- `llmdoc/memory/doc-gaps.md` p2-bridge.md §0 引用更新到 p2-bridge/00-overview.md

## promotion 候选(暂留观察)

- 「子代理工具卡死的 Bash heredoc 兜底协议」(教训 2)是新发现,候选进 [[multi-doc-drafting]] guide 的子代理失败恢复纪律节。本轮首次样本,暂留 memory 观察;若 P3/P4 详细设计扩展时再次遇到 Write/Edit 卡死,可正式 promote。
- 「拆子目录前必须做章节映射表」(教训 3)是新纪律,候选进 [[multi-doc-drafting]] guide 的「文档拆分纪律」节(目前 guide 只覆盖起草不覆盖拆分)。同样首次样本,暂留观察。

## 关联

- [[multi-doc-drafting]](本轮工作流来源,二轮验证)
- [[design-doc-completion]](P1 19 篇起草轮反思,本轮的工作流先例)
- [[design-review-round]](P1 设计评审轮反思,「主动盘点不确定决策」纪律的来源)
- `docs/design/p2-bridge/00-overview.md`(本轮主助理亲写的 P2 纲领)
- `docs/design/p2-bridge/implementation-progress.md`(P2 实施期对账表 + §2 回填收口表 + §3 决策盘点)
