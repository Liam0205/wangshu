---
name: issue8-boundary-cost-round
description: issue #8 边界成本轮——CallInto 零分配 + 四档真实世界 benchmark 过程教训:实现浪费 vs 架构成本辨析、零拷贝复用栈的 GC 根安全、benchmark 必须覆盖真实交互形式
metadata:
  type: reflection
  date: 2026-06-13
---

# issue #8 边界成本轮反思(CallInto 零分配 + 四档 benchmark)

> 范围:issue #8(per-call `Call` 固定 72 B / 2 allocs,boundary-dominated 嵌入被边界成本主导,在对标场景反被 gopher-lua 超过)的根因深挖 + 方案 A(内部零拷贝)+ B(`CallInto` 公共入口)实施 + 四档官方 benchmark 固化 + README 重构。驱动源:pineapple(首个目标宿主)的 `transform_by_lua` per-item 热路径。

## 核心教训

### 1. 「实现浪费」vs「架构成本」是性能 issue 的第一分类问题

issue #8 表面是「per-item 跨界慢于 gopher-lua」,而 [[design-premises]] 前提一明写「per-item 跨界被边界成本吃光收益」是**设计预期**。最危险的反应是直接援引前提一把 issue 判为「已知限制、无需修」。

**真正的判断**:把成本拆成两类——

- **架构成本**(NaN-boxing/arena/边界拷贝纪律带来的、前提二四项税决定的固定开销):这部分是设计选择,不该为它违背前提去优化;
- **实现浪费**(72 B / 2 allocs 的返回值**双拷贝**:VM 栈→inner slice→public slice,与 nret/脚本复杂度无关):这是纯粹的实现冗余,消除它不触碰任何前提。

**How to apply**:收到「在 X 形式下比对标对象慢」的性能 issue 时,先 profile 定位成本来源,再问「这笔成本是架构选择的必然,还是可消除的实现冗余?」。只有架构成本才适用「这是已知设计权衡」的回应;实现浪费无论形式是否被前提一判为非主推,都该消除——尤其当该形式是**首个目标宿主的真实热路径**且**对标场景是已承诺的 drop-in 卖点**时。判否一个优化提案要靠前提,但前提不能拿来掩护实现浪费。

### 2. 零拷贝复用栈返回值:GC 根可达性 + 覆写契约,必须用 stress 实测验证

`CallInto` 的零分配关键是**不拷贝**返回值,直接切 `th.stack[:nret]` 活动区返回。两个风险点都不能靠推理下结论:

- **GC 根可达性**:返回值切片指向复用栈,Call 返回后 `runningThread` 复位 nil,但 `mainTh` 仍是 loadedCls 同级常驻根 → 栈槽位值在 GC 下保持可达。验证手段:`SetGCStressMode(true)`(每分配点触发 GC)+ 复用 dst 循环 + string 返回值(经 arena),500 轮读出仍正确 = 无 UAF。
- **覆写契约**:返回值底层是复用栈,下次进入 VM 前会被覆写。这是 `CallInto` 与旧 `Call`(独立拷贝、可长持)的**根本契约差异**,必须在 godoc 显式标注 ⚠️,并保留旧 `Call` 给「返回值要跨下次 Call 存活」的调用方。验证手段:连续 CallInto 不串台测试 + 旧 Call 跨调用独立性测试,两条都要留。

**How to apply**:任何「返回内部缓冲区切片以省分配」的优化(P3 值栈 arena 化后会更频繁遇到),GC stress 实测 + 覆写契约测试是上线前置,不是可选。与长稳轮「内存复用类变更配套清单」([[longevity-review-fix-round]])同家族:复用前先列哪些路径会从良性变 UAF。

### 3. benchmark 必须覆盖真实交互形式——「VM 不是自 high 产品」

旧 benchmark 只有纯 VM micro(baseline)+ 真实负载纯 VM(realworld),**全部不跨 Go↔Lua 边界**。结果:README 头条 9.0× 是嵌入者**够不到的天花板**,而真实热路径(per-item 边界)的劣化完全不可见——直到 issue #8 用自带 probe 撞出来。

修复后固化为**四档**,关键是补上两档 embedded:

1. 纯 VM micro(自含,VM 内核上限)
2. 真实负载纯 VM small(benchmark-game,计算重、边界摊薄)
3. **边界 mini**(issue #8 三档:PureVM/CallOnly/Boundary,且并列 `Call` vs `CallInto` vs gopher,让劣化与修复同屏可见)
4. **真实负载 embedded**(贴近 pineapple:1000 item × 谓词/特征变换,逐 item set→call→读标量)

**How to apply**:嵌入式 VM 的 benchmark 若只测「脚本本体」就是自欺——边界成本是嵌入者的真实成本,必须有跨界档。新档要并列对标对象 + 新旧路径,让「问题」和「修复」在同一张表里可验证,而非只秀好数字。这是 benchmark 诚实性纪律,与 [[official-suite-perf-round]] 的「归因诚实」「benchmark 否决门」同源。

### 4. README 性能数字必须同机同日重测,不能跨机拼接

旧 README 写 simple 9.0×,本轮同机实测是 5.9×(机器/go 版本差异)。四档表格若混用历史数字会自相矛盾。本轮全部四档在同一台机(Xeon 6982P-C / go1.26.2 / 串行 / count 多次)重测,倍率自洽。

**How to apply**:动 README 任一性能数字时,**整节同机重测**,不要只改一行。标注机器/版本/并行度/count,让读者知道这是相对量。

## 促成的稳定文档更新

- [[embedding-contract]] 简易 API 段补 `CallInto` 条目(含实现浪费 vs 架构成本辨析、覆写契约)
- `docs/design/p1-interpreter/implementation-progress.md` 公共 API 表加 CallInto 行
- README 性能节重构为四档(同机实测)
- `benchmarks/embedded/` 新包(mini + realworld-embedded 两档)
- Makefile bench 注释更新为四档

## promotion 候选(暂留观察)

- 「实现浪费 vs 架构成本」辨析(教训 1)是性能 issue 处置的通用判断框架。**已完成** → [[design-claims-vs-codebase-physics]](成本归类维度 §3 + 零拷贝根可达性 §4),连同 PW5 边界成本 / PW6 段重定位聚合成 4 维判断框架,不再暂留观察。
- 「benchmark 覆盖真实交互形式」(教训 3)同理,候选进 perf guide。

## 关联

[[design-premises]](前提一边界成本论证)· [[embedding-contract]](CallInto 契约)· [[perf-optimization-workflow]](归因诚实/否决门同源)· [[longevity-review-fix-round]](内存复用配套清单同家族)· [[official-suite-perf-round]](benchmark 诚实性)· [[issue1-api-gap-round]](公共 API 演进 / GCRef 接根)
