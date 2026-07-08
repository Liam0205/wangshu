---
name: issue67-amd64-nodehit-crossrun-round
description: >
  issue #67 n-body 后半的 amd64 端补齐(PR #82,分支 feat/issue67-amd64-nodehit):把 arm64 上已修正的
  identity-free NodeHit inline(hmask 边界 + nodeRef guard)移植到 amd64。头条教训:PR #74 当年把
  GETTABLE/SETTABLE 常量字符串 key 的段内 NodeHit inline 定为 arm64-only(理由 amd64 实测 inline 反而
  ~3% 回归)这个结论被推翻——那 ~3% 不是 inline 的成本,而是一道烤进段里的 TableRef 身份 guard 每次都不通过
  造成的白费开销:n-body 的 bodies[i] 这类局部表每个 Run 都在新 arena 偏移重建,promotion 只烤一次快照,
  身份 guard 跨 Run 100% 落空,段里每次都发射 guard、每次都不通过、每次都照样 exit-reason 往返到 host。
  之前看到的「inline 命中 17 万次」是单个 Run 内的假信号,跨 Run 稳态从没真 inline 过。度量教训:任何投机/
  inline 快路径的收益必须测跨 Run(稳态)的 exit-reason dispatch 计数,而不是单个 Run 内命中数。修正 commit
  6a10721(原在 arm64):TableRef 身份 guard 对正确性并不必要——一个 node 自己的 key 字段就能唯一标识这条
  entry,换成两道与表地址无关的 guard(hmask 边界 + nodeRef != 0)。本任务把修正后的 guard 移植到 amd64,
  共用 emitTableNodeHitPrelude,amd64 实测 n-body auto 43.5ms→7.0ms(~6.2x),跨 Run exit-reason dispatch
  875k→50k,fib/binary-trees/spectral/fannkuch 无回归;arm64 M5 Pro n-body auto 0.98x→5.7x。新增
  TestPJ10_TableNodeHit_CrossRunInline 断言每 Run dispatch < 500(身份 guard 时代 ~18000),补上 arm64
  那一轮缺的跨 Run prove-the-path。
metadata:
  type: reflection
  date: 2026-07-08
---

# issue #67 amd64 NodeHit 跨 Run inline 补齐轮反思(2026-07-08,PR #82)

> 范围:分支 `feat/issue67-amd64-nodehit`(3 个功能/文档提交 + 2 处 review nit 修正)。把 arm64 上已修正的 identity-free NodeHit inline(hmask 边界 + nodeRef guard)移植到 amd64,并推翻 PR #74「amd64 NodeHit inline 反而回归、故 arm64-only」的历史结论。

## 任务

PR #74 当年把 GETTABLE/SETTABLE 常量字符串 key 的段内 NodeHit inline 定为 **arm64-only**,理由是 amd64 实测 inline 反而 ~3% 回归(n-body 43.1→44.4ms,binary-trees 26.8→27.8ms)。本轮把 arm64 上已经修正的 identity-free NodeHit inline(hmask 边界 + nodeRef guard,原 commit 6a10721 落在 arm64)移植到 amd64,共用新的 `emitTableNodeHitPrelude` 发射 guard 链,并补上 arm64 那一轮缺的跨 Run prove-the-path 测试。

## 期望与实际

- 期望:amd64 补齐 NodeHit inline 后与 arm64 接受面同构,n-body 后半得到与 arm64 一致的加速。
- 实际:**达成且推翻了历史结论**。amd64 实测 n-body auto 43.5ms → 7.0ms(~6.2×),跨 Run exit-reason dispatch 875k → 50k;fib / binary-trees / spectral-norm / fannkuch 无回归。arm64 M5 Pro 同步实测 n-body auto 从 0.98× 变 5.7×。两个架构现在都命中。PR #74「amd64 inline 反而 ~3% 回归」的结论被证伪——那 ~3% 从来不是 inline 本身的成本。

## 头条教训:度量单位/时间窗口选错,会把一个从未生效的优化误判为「生效但太贵」

这是本轮最重要、最可复用的一条,与以下两条独立事实纠缠在一起。

### 度量侧:收益必须测跨 Run 稳态的 exit-reason dispatch,不是单 Run 命中数

PR #74 当年观察到 inline「命中 17 万次」并据此认定 inline 机制在工作,却同时看到 ~3% 回归,于是推出「inline 生效但太贵」的结论,进而定下 arm64-only 的架构裁决。真相是:那「命中 17 万次」是**单个 Run 内**的假信号;跨 Run 的**稳态** exit-reason dispatch 计数才是真相,而它从来没有下降——段里每次都发射 guard、每次都不通过、每次都照样 exit-reason 往返到 `host.GetTable`/`host.SetTable`。inline 快路径**跨 Run 从没真正生效过**,那 ~3% 是「每次都白发一遍身份 guard 又白退一次 host」叠出来的净开销。

**度量教训**:任何投机 / inline 快路径的收益,度量单位必须是**跨 Run(稳态)的 exit-reason dispatch 计数**,不是单个 Run 内的命中数。单 Run 命中数看着正常(甚至很漂亮的 17 万次),但跨 Run 稳态可能从没真 inline 过——因为 guard 在第一个 Run 里可能碰巧通过(表还是 promotion 那一刻的那张),后续 Run 全落空。选错度量单位会把「从未生效的优化」读成「生效但太贵」,再叠加一个碰巧同向的噪声(~3%),就能推出一个完全错误的架构结论(arm64-only)。

### 物理侧:烤进段的身份快照指针,在对象跨 Run 重建后失效

为什么身份 guard 跨 Run 100% 落空:最初的 inline 用一道**身份比较**——把编译期 IC 记录的表指针烤进段,运行期比对当前表指针是否一致。n-body 的 `bodies[i]` 这类局部表**每个 Run 都在新的 arena 偏移重建**,而 promotion 只在提升那一刻烤一次快照。于是段里烤死的身份指针指向的是第一个 Run 的那张表,后续每个 Run 的表都在新偏移,身份比较必然不通过。这是「promotion 快照 vs 运行期重建」的时间维度失效——烤进段的身份快照在对象跨 Run 重建后指向陈旧对象,与 [[design-claims-vs-codebase-physics]] §5「时间维度」已收录的四个形式(难点过期 / 调研先于实现 / 外部依赖现状过期 / sentinel 注释承诺与检查状态解耦)同源,但维度是新的:**前序快照失效发生在「运行期对象跨 Run 重建」这条时间轴上**,而不是「前序里程碑/前序调研/外部依赖/注释承诺」那几条。

### 修正:身份 guard 对正确性并不必要,换成两道与表地址无关的 guard

TableRef 身份 guard 对正确性并不必要——一个 node 自己的 key 字段就能唯一标识这条 entry(同一 key 在整张表至多出现在一个 node)。修正(commit 6a10721,原在 arm64)换成两道与表地址无关的 guard:

- **hmask 边界**(`Index <= hmask`,保证访问在界内);
- **nodeRef != 0**(排除 hmask==0 歧义:hash 大小 1 vs 无 hash 段会把 node[0] 错指到 arena 偏移 0)。

同 shape 的表(同 key 同插入顺序 → 同 node Index)照样命中;不同 shape 的表 guard 落空、退到 host(慢但绝不会错)。这样跨 Run 重建的同 shape 表也能真命中 inline,收益从「假信号」变成真收益。

## 本轮改动

把修正后的 guard 移植到 amd64,共用新的 `emitTableNodeHitPrelude` 发射 guard 链(IsTable / GCRef 提取 / arena base / hmask 边界 / gen(Shape) / nodeRef != 0),`emitInlineGetTableNodeHit` / `emitInlineSetTableNodeHit` 加 NodeKey + NodeVal guard 和 store。这是接受面/emit 面的双 arch 同构补齐,不改正确性语义(不同 shape 仍退 host)。

## 解药:跨 Run prove-the-path 测试(arm64 那一轮缺的正是这一条)

新增 `TestPJ10_TableNodeHit_CrossRunInline`(`e2e_table_nodehit_crossrun_amd64_test.go`):提升后的 kernel 每次调用重建一张同 shape 的表(新 arena 偏移,专门打身份 guard),断言每 Run 的 exit-reason dispatch < 500(身份 guard 时代是 ~18000)。

这正是 arm64 那一轮缺的**跨 Run** prove-the-path。之前 arm64 的 e2e 只有 arch-neutral 的正确性断言 + 单 Run 观察,没有跨 Run dispatch 断言,所以「命中 17 万次是单 Run 假信号」这个陷阱当初没被抓到——正确性 e2e 全绿(inline 走没走都产出正确值,身份 guard 落空只是退 host 变慢不变错),而单 Run 命中计数器看着漂亮。缺的正交证据是「跨 Run 稳态的 dispatch 不下降」这一条:身份 guard 时代它稳在 ~18000,identity-free guard 时代降到 < 500,两条路径在这个断言上才可分辨。

## promotion 分析

### 头条教训 → [[prove-the-path-under-test]]:强烈建议提升,新增「度量单位/时间窗口」维度

这条头条教训是 [[prove-the-path-under-test]] 家族的又一独立实例,家族早已跨过提升阈值(现为十三实例)。但本轮的**维度是新的**:

- 以往实例多是「绿色 ≠ 在测你以为在测的」(路径是否被走到,§1-§4)或「诊断侧退化归因」(§7)或「探索空间维度」(§5);
- 本轮是**度量侧**——「性能收益的度量单位选错(单 Run 命中数 vs 跨 Run 稳态 dispatch),会把一个从未生效的优化误判为『生效但太贵』,进而推出错误的架构结论(arm64-only)」。

它与家族核心断言同源(输出/表面数字不携带路径信息,必须用正交证据反推),但补的是一个此前未被显式记录的解药维度:**读加速收益时,度量单位必须是稳态窗口(跨 Run)的 dispatch 计数,不是单个 Run/单个窗口内的命中数**。单 Run 命中数是「机制被触发过」的证据,不是「机制在稳态生效」的证据——两者的差就是本轮那 17 万次假信号。

**建议提升,并入 [[prove-the-path-under-test]]**,两种落法皆可(留 recorder 定):

- 并入 **§7 诊断侧对偶**作对偶补充——§7 已讲「退化归因前先证被怪罪的路径存在」,本条讲「收益归因前先证收益来自稳态生效而非单 Run 假信号」,两者都是「表面数字不携带路径信息」的对偶面(一个管退化、一个管收益);
- 或**新增一节「度量单位 / 时间窗口」**——单独讲「命中数 vs 稳态 dispatch」「单 Run 窗口 vs 跨 Run 窗口」这个度量单位陷阱,并把「白盒命中计数器要按稳态窗口统计,不按单 Run 统计」写进 §2(b) 命中计数器解药的补充。

理由:家族已过阈值,且本轮贡献了明确、可复用的解药形式(跨 Run dispatch 断言 + 「命中数不等于稳态生效」的判据),不需要再等第二次样本验证——这与 issue #67 auto-mode 轮教训 1 直接提升的处理一致(家族过阈值 + 贡献明确解药 = 直接升)。

### 与 [[design-claims-vs-codebase-physics]] §5「时间维度」相关:建议作补充,归到 recorder 判

烤进段的身份快照指针在对象跨 Run 重建后失效,是 §5「时间维度」的又一形式(promotion 快照 vs 运行期重建)。§5 现有四个形式的失效都在「前序事实变更」时间轴上(前序里程碑 / 前序调研 / 外部依赖 / 注释承诺),本轮补的是「运行期对象跨 Run 重建」这条更快、更贴近热路径的时间轴——同一份烤进段的快照,不用等跨里程碑,只要下一个 Run 就失效了。

**建议**:作 §5「时间维度」的补充实例登记(promotion 快照 vs 运行期重建),但**不单独立项**——它与 §2「arena 段重定位」也邻接(段重定位是空间维度的 held-pointer 失效,本轮是时间维度的 held-snapshot 失效),但轴不同不合并。首次以「promotion 烤死快照 vs 跨 Run 重建」形式出现,若 P5 trace JIT / OSR 再撞同族(烤进 trace 的对象身份被跨 trace 重建打穿),可强化。倾向**暂留观察或作 §5 补充登记二选一,留 recorder 定**——头条 promotion 应集中在 [[prove-the-path-under-test]] 的度量维度上,design-claims 这条是次要邻接。

### 其余

- **推翻历史架构裁决要留可复现的证据链**:PR #74 的 arm64-only 裁决在仓库躺了若干轮没人复查,直到本轮拿跨 Run dispatch 计数把它证伪。教训与「留 X 收口 godoc 承诺当 issue 信号」([[2026-06-17-issue18-p3-autolift-fix-round]] 教训 3)邻接但不同——那条管「未兑现的承诺」,本条管「基于错误度量下的裁决」。首次样本,暂留观察;若后续再有「历史 perf 裁决被证系度量单位选错」的独立实例,可考虑并入 [[perf-optimization-workflow]] 作「历史裁决复查」纪律。

## 验证

- amd64 实测 n-body auto 43.5ms → 7.0ms(~6.2×),跨 Run exit-reason dispatch 875k → 50k;fib / binary-trees / spectral-norm / fannkuch 无回归。
- arm64 M5 Pro 实测 n-body auto 0.98× → 5.7×。
- `TestPJ10_TableNodeHit_CrossRunInline` 断言每 Run dispatch < 500(身份 guard 时代 ~18000)通过。

## 触发场景

- 读一个投机 / inline 快路径的「收益」数字时(头条):度量单位必须是**跨 Run 稳态的 exit-reason dispatch 计数**,不是单个 Run 内的命中数;看到「命中 N 次」漂亮但整体数字反而回归,默认怀疑「机制被触发过但跨 Run 稳态从没真生效」,加跨 Run dispatch 断言而不是信单 Run 命中计数器。
- 给 IC / inline 快路径写「把编译期身份快照烤进段」的 guard 前:问「这个身份在段的存活窗口内会不会被跨 Run 重建打穿」(promotion 快照 vs 运行期重建,[[design-claims-vs-codebase-physics]] §5);若被测对象每 Run 在新 arena 偏移重建,身份 guard 会跨 Run 100% 落空——优先换成与对象地址无关的 guard(key/shape 类内容 guard,如本轮 hmask 边界 + nodeRef != 0)。
- 遇到一个历史 perf 裁决(某优化「实测反而回归、故某平台 only / 故放弃」)时:先复查它当年的度量单位是不是单 Run 命中数 / 单窗口数,用跨 Run 稳态计数重测再决定要不要推翻。

## 关联

[[prove-the-path-under-test]](头条落点,度量单位/时间窗口新维度,建议直接提升)· [[design-claims-vs-codebase-physics]] §5(时间维度:promotion 快照 vs 运行期重建,建议作补充登记)· [[backend-capability-vs-profitability]](NodeHit inline 是接受面/emit 能力,双 arch 同构补齐)· [[2026-07-07-issue67-auto-mode-coverage-round]](同 issue #67 前序,auto 覆盖轮)· issue #67 · PR #74(被推翻的 arm64-only 裁决)· PR #82 · commit 6a10721(arm64 identity-free guard 修正)· `internal/gibbous/jit/peroptranslator/emit_ops_amd64.go`(`emitTableNodeHitPrelude` / `emitInlineGetTableNodeHit` / `emitInlineSetTableNodeHit`)· `internal/gibbous/jit/peroptranslator/e2e_table_nodehit_crossrun_amd64_test.go`(`TestPJ10_TableNodeHit_CrossRunInline`)
