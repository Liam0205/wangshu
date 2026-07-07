# issue #5 / #6 公共面 API 缺口轮 3(Table.ForEach + globals baseline 状态隔离)

- **日期**:2026-07-14
- **任务类型**:issue 驱动的公共面 API 增量交付(pineapple wangshu backend 接入触发,
  一样的工作流第三轮)

## 任务

issue #5/#6 来自 pineapple wangshu backend 深度接入,两件 gap:
- **#5** `Table.ForEach`:`func (t *Table) ForEach(fn func(key, val Value) bool) error`,
  转发 internal RawNext 循环(与 stdlib next/pairs 同源),fn 返 false 提前终止。
  动机:issue #2 只交付了 SetIndex/GetIndex/Len,缺「脚本返回 string-key map 后
  宿主端读出」的对称面——「宿主构造投喂」已有,「脚本返回后宿主遍历」缺失。
- **#6** `State.MarkGlobalsBaseline / ResetGlobalsToBaseline`:pineapple sync.Pool
  复用 State 时脚本级状态隔离。Mark 拍当前 `_G` 字符串 key 快照为基线,Reset
  把非 baseline key 清空 + baseline key 复原。对位 gopher-lua statePool
  `snapshotBaselineValues + resetToBaseline`。baseline 复合值经 `visitExtraValues`
  入 GC 根。

提交区间:`bb1e9a8..755d5ce`(3 个 commit):`4f855d2` ForEach / `3d34839`
MarkGlobalsBaseline+ResetGlobalsToBaseline / `755d5ce` doc 对账。

## 预期 vs 实际

- 预期:一样的工作流第三轮——[[public-api-incremental-delivery]] guide 的 8 条纪律
  已在前两轮全验证可复用,本轮应零阻塞零意外直接完成。
- 实际:代码审查零阻塞问题,8 条纪律已成肌肉记忆。**但**本轮暴露出两类新维度
  教训:① 对称面缺口延迟发现(issue #2 关闭时本应同步识别到迭代读出 gap);
  ② GC 根的第二条接入通道(baseline 的 `visitExtraValues` 与 pin 表的
  `visitExtraRefs` 走不同 visitor 通道,同一不变式的两面)。

## 教训(每条首句为「下次什么场景会触发」)

### 1. 对称面缺口延迟发现——「写入对称面的读出」是容易遗漏的 gap 类型

**触发场景**:关闭一个 API issue 时,issue 交付面只有「写入」类操作(Set/SetIndex/
构造)而无「读出」类操作(Get/遍历/序列化)时。

issue #2 交付了 Table 写入能力(Set/SetIndex/Get/GetIndex/Len),关闭时以为能力
充分。直到 pineapple 实际桥接 return-map 场景才发现:Get/GetIndex 只能按已知 key
读取,缺任意 key 迭代——**对 string-key map 这个极常见形式,没有 ForEach 等于
没有读出能力**。

**根因**:issue 关闭时只检查了「issue 字面要求的 API 是否全部完成」,没有检查
「完成后的 API 面是否构成写入/读出对称闭环」。

**修正纪律**:未来关闭任何 API issue 时增加一步「对称面检查」——写入面(Set/
构造/注入)已有 → 读出面(Get/遍历/序列化)是否也有?反之亦然。这是
[[public-api-incremental-delivery]] 第 1 条「设计承诺源回看」的扩展:三角验证时
不仅看 issue 字面 vs 设计承诺 vs 真实场景,还要看**写入/读出对称性**。

### 2. GC 根的两条接入通道——pin 表 vs baseline,同一不变式的两面

**触发场景**:给 State 新增内部「跨 GC 周期长持复合值」的机制(不是公共面暴露给
宿主的 GCRef,而是 VM 内部自己需要跨 GC 生存的 value)时。

pin 表管「公共 API 暴露的长持 GCRef」,走 `visitExtraRefs`(GCRef 级别);baseline
管「内部状态恢复需要的长持 GCRef」,走 `visitExtraValues`(value.Value 级别)。
两者是同一不变式「任何让对象在 VM 生命周期管理之外被持有都必须接根」的两面,
但走不同的 visitor 通道。

**关键差异**:
- **粒度不同**:pin 表是 GCRef 级(只挂 GC 对象的引用),baseline 是 value.Value
  级(需要 markValue 递归走进复合值);
- **释放纪律不同**:pin 表的 `Release()` 可选(不 release 仅累积槽),baseline 没有
  「释放」概念——`MarkGlobalsBaseline` 覆盖旧 baseline 时旧复合值自然失根(下次
  GC 回收)。这个非对称性是有意的:pin 表是 Value 级生命期(由宿主调 Release
  控制),baseline 是 State 级生命期(由 State 生存期决定);
- **文档覆盖不同**:[[embedding-contract]] 不变式条款已覆盖 pin 表,但未提 baseline。

### 3. 线性扫描够用的判断——commit message 应注明 N 的量级

**触发场景**:写了 O(N*M) 或 O(N^2) 复杂度的代码,N 是「典型规模很小」的集合时。

`ResetGlobalsToBaseline` 的 `inBaseline` 检查用 O(N) 线性扫(N=baseline 大小,
典型 ~30-50 stdlib 条目),总体 O(N*M)(M=当前 globals 大小)。审查确认 stdlib
规模下无需 map。

**教训**:这类「小 N 用线性扫」的判断应在 commit message 显式注明 N 的量级与
量级来源(如「N=baseline 大小,典型 ~30-50 条 stdlib 条目」),否则 reviewer
需要自己推断 N 是否真的小。这是第 5 条「行为变更显式标注」纪律在性能决策维度的
类比——性能决策的隐式假设同样需要显式标注。

### 4. 一样的工作流第三轮零阻塞——8 条纪律已稳定为肌肉记忆

**触发场景**:重复使用同一工作流 guide 三轮及以上时,可作为「guide 稳定」的信号。

本轮代码审查零阻塞问题。[[public-api-incremental-delivery]] 的 8 条纪律(设计承诺
源回看 / GCRef 接根 / 单域物理隔离 / 错误消息稳定 / 行为变更显式 / 范围扩张
顺手收口 / 对位测试先 grep oracle / internal 签名避免反向依赖)在第三轮已无须
逐条检查——稳定的 guide 最终消融为习惯。这验证了 guide 立项的判断正确:两轮
样本足以立项,三轮样本证明稳定。

### 5. baseline 与 pin 表的释放纪律差异——文档未显式说明

**触发场景**:用户或未来开发者问「baseline 需不需要像 pin 表一样 Release?」时。

baseline 没有 `Release` 等价物,覆盖旧 baseline(再次调 `MarkGlobalsBaseline`)
时旧复合值自然失根。这是设计有意为之(State 级生命期 vs Value 级生命期),但
`embedding-contract.md` 和 godoc 都没有显式解释这个非对称性。未来应在
`MarkGlobalsBaseline` 的 godoc 中补一句「覆盖旧 baseline 时旧快照中的复合值在
下次 GC 自动回收;无需显式释放」。

## 缺失的文档或信号

- [[embedding-contract]] 不变式条款只提 pin 表(`visitExtraRefs`),未提 baseline
  (`visitExtraValues`)——同一「任何长持引用必须接根」不变式的后半面缺失。
- [[public-api-incremental-delivery]] guide 缺第 9 条「对称面检查」——关闭写入类
  issue 时应同步检查读出面是否充分(反之亦然)。
- `MarkGlobalsBaseline` godoc 未解释 baseline 为什么没有 `Release` 等价物
  (State 级生命期 vs Value 级生命期的有意非对称)。
- commit message 性能决策显式标注(N 的量级)无规范化约定。

## Promotion 候选

### 升入 `guides/public-api-incremental-delivery.md` 的候选

- **「对称面检查」作为第 9 条纪律**:关闭 API issue 时增加一步——写入面(Set/构造/
  注入)已有 → 读出面(Get/遍历/序列化)是否也有?反之亦然。来源:教训 1,
  issue #5 是 issue #2 关闭时本应识别到的 gap。

### 升入 `reference/embedding-contract.md` 的候选

- **补 baseline GC 根机制指针**:不变式条款当前只提 pin 表(`visitExtraRefs`),
  应增补一句指向 baseline 通道(`visitExtraValues`)——与 pin 表并列为同一不变式
  的两面。来源:教训 2。

### 留在 memory 观察

- commit message 性能决策标注(教训 3):仅一个样本,观察 P2+ 是否复现。
- baseline 释放纪律 godoc 补充(教训 5):属 godoc 文案层,不升 reference;
  下次碰到再补。

## 后续行动

- recorder 把「对称面检查」纳入 [[public-api-incremental-delivery]] 作为第 9 条纪律。
- recorder 在 [[embedding-contract]] 不变式条款补 baseline GC 根通道指针(与 pin 表
  并列)。
- P2+ 任何新增「State 级内部长持复合值」机制,前置约束相同:必须接
  `visitExtraValues`(或同等 visitor 通道)入 GC 根。
- 后续关闭 API issue 时执行对称面检查:写入/读出、构造/析构、注入/读出是否成对。
