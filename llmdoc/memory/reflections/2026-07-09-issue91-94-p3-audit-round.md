---
name: 2026-07-09-issue91-94-p3-audit-round
description: >
  P3 摸底四连修复轮(2026-07-09,PR #100/#101)。一场 P3 生产路径 probe 一次产 4 个带实证的 issue,同类根因是
  P4 上线后注意力集中在 P4、P3 auto 模式生产路径长期缺 probe。#91:relooper computeScopes 只处理单向部分
  交叠,漏对称方向,顶层 if/else diamond 永远升不了 P3(连 force-all 也不行),normalizeScopes 定点迭代修复
  + 白盒 PromotionCount>0 用例(byte-equal 分不清升层与静默 CFAIL 回退)。#92:WorthPromoting 缺 back edge
  维度,直线小体升层每次付 wasm 边界往返却无循环摊薄,加 straightLineMinCodeLen=32 拒(仅 auto)。#93:README
  P3 列分母错配(kernel×50 vs 裸顶层×1)低估 ~50 倍,加 _GopherKernel 同形状分母 + 缺分母渲染 —。#94:
  Bridge.OnEnter 每帧查 map,加 OnEnterID/OnBackEdgeID 走 ProtoID 索引 slice。核心教训:能力修复≠收益改变
  (fannkuch 能编但不赚,反引 backend-capability-vs-profitability);互补 PR 合入顺序(收益门先于能力修复,
  防回归窗口);fixture 随升层判据多维化演进(第 2 实例连 issue #21);跨机器 bench 接力用 PR 评论当交接文档
  (第 2 实例连 issue #89)。
metadata:
  type: reflection
  date: 2026-07-09
---

# P3 摸底四连修复轮反思(2026-07-09,PR #100 / #101)

> 范围:一场 P3 auto 模式生产路径 probe(挂 bridge Logger 逐 proto 看升层决策 + cpuprofile + 控制变量
> kernel 扫描)一次产出 issue #91/#92/#93/#94,两个 PR 两天内全闭环。#92/#94 归 PR #100(已合入,
> CI 39/39 绿 + bot 两轮 APPROVE 零问题),#91/#93 归 PR #101(rebase + README 刷新已 push,CI 因 GitHub
> Actions 服务中断未跑完)。

## 任务

给 P3(wasm 后端)auto 模式的生产升层路径做一次系统 probe,把长期没人看的决策链摸一遍,并修掉摸出来的问题:

- **#91**:P3 relooper 拒绝顶层 if/else diamond。`computeScopes` 的嵌套修复只处理单向部分交叠(已有
  scope 的 begin 落进新 scope 区间),漏了对称方向(先前 scope 的 end 落进后来 scope 区间)——正是 diamond
  两个汇合 block 产生的形式。后果:Simple kernel 和 fannkuch 主函数(93 条指令)**永远升不了 P3,连
  force-all 也不行**。
- **#92**:P3 `WorthPromoting` 缺 back edge 维度。直线体(无循环)零 helper op 无条件放行,升层后每次调用
  付 wasm 边界往返(~130ns vs 解释器 ~73ns),体内没循环摊薄,Arith kernel 升层后反而慢 1.76x。
- **#93**:README baseline 三行的 P3 列工作负载错配。P3 列测 kernel×50(顶层 vararg 不升层,必须包 kernel),
  其它列测裸顶层×1,formatter 直接相除把 P3 低估 ~50 倍。
- **#94**:`Bridge.OnEnter` 每次进帧跑,被拒升层的调用密集负载上 ~94% 调用只做「查 map → 已决策 → return」,
  map 查找吃 ~6% 总时间。

## 期望与实际

- 期望:P4 稳定后回头体检 P3,预期找一两处小回归,顺手修掉。
- 实际:一场 probe 一次产 4 个 issue,其中 #91 是能力级 bug(顶层 diamond **永远**升不了、连 force-all 都
  拒),藏了很久没人发现。根因不是某次改动引入,而是 **P4 上线后注意力全在 P4,P3 auto 模式生产路径长期缺
  probe**——没有观察就没有发现。

## 修复要点

- **#91 `normalizeScopes`**:所有 scope 建完后跑定点迭代,对每对 improper overlap,end 更大的一方(必须是
  block;若是 loop 则报错防御)把 begin 前移到包含另一个。终止性证明:每次修复严格减小某个 begin,下界 0。
  白盒用例 `TestP3_TopLevelDiamondPromotes` 断言 `PromotionCount > 0`。
- **#92 `straightLineMinCodeLen=32`**:无 back edge(FORLOOP/TFORLOOP/负向 JMP)且 `len(Code) < 32` 拒;仅
  auto 生效,forceAll 绕过(收益门,不动能力面)。实测 Arith auto 10211→5894 ns/op。
- **#93 方案 1**:加 `_GopherKernel` 同形状分母基准,formatter baseline P3 格用它当分母 + `[^p3-kernel]`
  脚注 + 不参与该行最快加粗;旧日志缺分母时渲染 `—` 而非错误倍率。
- **#94 `OnEnterID`/`OnBackEdgeID`**:用解释器现成的 ProtoID 走 State 私有 pid 索引 slice,越界回退 map,旧
  入口保留;`LoadProgram` 后 `GrowProfileIndex`。fib auto 的 mapaccess 从 profile 消失。

## 核心教训

### 教训 1(能力修复 ≠ 收益改变,fannkuch 实证 —— [[backend-capability-vs-profitability]] 正面实例)

#91 让 fannkuch 主函数第一次**能**升 P3(能力层放行),但它 helper 密集,升了也就解释器水平(force 6.20ms
vs P1 5.91ms),auto 仍被 CALL 密度门正确拒掉(收益层拦)。**「能编」和「编了赚不赚」是两条独立的线,要分开
修、分开量**:#91 修的是 relooper 能力面(diamond 从「永久拒」变「能编」),fannkuch 升层后的净亏是收益层的
事,由既有密度门接管,不是 #91 的失败。这正是 guide 断言「塞进同一张表把 shape-independent 能编和
shape-dependent 净胜强绑,一旦净胜性依赖 shape 表就不够表达」的又一正面确认——放行 diamond 不等于承诺
diamond 都该 auto 升。判断一个能力修复成不成,看的是「原本编不了的现在能编且编对」,**不能**用「升层后跑得更
快」当验收——后者是收益层的责任。

### 教训 2(收益门先于能力修复合入,防回归窗口 —— 互补 PR 的合入顺序分析)

#91(relooper 放行 diamond)与 #92(收益门拒直线小体)是互补的一对:**只合 #91 会打开一个回归窗口**——
Simple 这类顶层 diamond 一旦能过 relooper,若此时收益门还没上,它是直线小体、会被 auto 升层然后每次付边界
往返反噬。正确顺序是**先合 #100(#92 的收益门)再合 #91**:#91 修好、Simple 能过 relooper 时,恰好被
#92 的门接住,不进 auto 反噬。用户问「#92/#94 会被 #91/#93 block 吗」,分析结论是不 block,且先合 #100 更
安全,实际也按此顺序执行。

**可复用判据**:两个 PR 一个「放开某类形式的可达性」、一个「给这类形式加收益/安全过滤」时,过滤门(收益/安全
兜底)必须先合。合入顺序不是随意的——**能力放开在前而过滤在后,中间那段 master 上存在一个「能达但没兜底」的
回归窗口**,任何人在窗口期 rebase 或跑基准都会撞见反噬。这与 [[backend-capability-vs-profitability]] 的分层
是一体两面:能力层放开时,对应的收益层过滤要么已在、要么同批先行。

### 教训 3(fixture 随升层判据多维化而演进 —— 第 2 实例,连 issue #21)

#92 的收益门加了 back edge 维度后,PR #100 第一轮 CI 立刻抓到
`TestPromotionCount_P3_NoForce_HotEntry_Lifts` 的 fixture `f` 被新门正确拒掉——`f` 是直线体(里面的 if 是
分支不是循环),auto 模式下新门把它拒了,测试挂。**这不是测试错,是门在工作**:一个「验证热入口会升层」的
fixture,在升层判据里新增一个维度后,自己必须满足这个新维度,否则它验证的前提(能升层)就不成立了。修法是给
`f` 加内层 `for k=1,4`(补上 back edge),而不是放松门。

这与 issue #21 加尺寸地板(fixture 必须 ≥10 opcodes)是同一模式的**第二个实例**:**升层判据每加一个维度,
所有依赖「会升层」的自然热度类 fixture 必须跟着满足全部维度**。可操作纪律:改升层门(收益/尺寸/结构任一维度)
后,预期热度类 fixture 会连锁失败,修法是让 fixture 满足新维度、而非放宽门;CI 挂在这类 fixture 上通常是门
生效的正信号,不是回归。两个实例已够成模式,建议下次再遇同类时直接升 guide(见 Promotion)。

### 教训 4(跨机器 bench 接力用 PR 评论当交接文档 —— 第 2 实例,连 issue #89)

#93 的 README 表刷新在 darwin/arm64 机器上做,amd64 表的 baseline P3 格先标 `—`(旧数字是错的不能留,而
amd64 数字必须同机重测才符合口径),PR 评论里留了完整的 amd64 接力清单(跑前查 load、步骤、预期量级、贴回哪
几行)。我在 amd64 机器上按清单接力:rebase onto master → 跑全表 → 刷两个 README(整轮同口径,不只 baseline
三行)→ 更新脚注「待重测」句子。**跨机器 bench 接力用 PR 评论当交接文档,这是第二次跑通**(第一次是 issue
#89 的 arm64 接力,见 [[2026-07-08-pr95-spill-stack-fuel-round]] 教训 3)。

可复用要点:一台机器只能出本架构的诚实数字,另一架构的格先标 `—` 占位(不留错数字),把接力清单写进 PR 评论
——查 load、精确步骤、预期量级、回贴位置四要素齐全,接力方照做即可。两个实例都验证了:交接信息落在 PR 评论
里比落在临时文件里可靠(评论随 PR 走、reviewer 也能看)。

### 教训 5(一场集中 probe 摸底产一批带实证的 issue —— process 观察)

这 4 个 issue 出自同一场 P3 摸底,每个都带 probe 实证数字(具体 ns/op、CPU 占比、倍率)和明确的修复方向,所以
修起来快——4 个 issue 两个 PR 两天内全闭环。**集中一次 probe(挂 Logger 看逐 proto 决策 + cpuprofile + 控制
变量扫描),比零散撞一个修一个高效得多**:probe 一次性把决策链摸清,产出的 issue 自带定位和量化,后续实现阶段
不用重复搭观测。触发场景:某条生产路径长期没人看(本轮是 P4 上线后被冷落的 P3 auto),值得专门排一场 probe 而
非等它出问题——「没有观察就没有发现」在这里得到反证。

### 教训 6(缺数据宁可渲染 `—`,不给错误倍率 —— 小教训)

#93 的 formatter:旧日志没有 `_GopherKernel` 分母时,宁可显示 `—` 也不算一个错误倍率。**表格 formatter 缺
必要输入时,渲染占位符比用可得但错配的数据凑一个数字诚实**——错配的分母(kernel×50 除以裸顶层×1)恰恰是 #93
低估 50 倍的根因,给错数字比给空更有害,因为它看起来是对的。

## Promotion 判断

- **教训 1(能力≠收益,fannkuch)** → **guide 反引即可,暂不改正文**。理由:
  [[backend-capability-vs-profitability]] 已收四个实例并总结出接口族,本轮是断言的又一正面确认(能力放行
  diamond ≠ 承诺 auto 升),没有引入新接口或新维度,补一句「#91 relooper 放行 vs fannkuch 收益门拒」到 guide
  的实例列表即可,不改分层模型。
- **教训 2(收益门先于能力修复合入)** → **升 guide 一节(建议)**。理由:这是
  [[backend-capability-vs-profitability]] 的操作面推论(能力层放开时收益层过滤须先行或同批),属于该 guide
  缺的「合入顺序/回归窗口」维度,不是新原则,适合作为该 guide 的一个小节而非独立 guide。首个明确样本,但推论
  性强、可复用清晰,达升层阈值。
- **教训 3(fixture 随判据多维化演进)** → **升 guide(建议,达阈值)**。理由:第二个实例(连 issue #21 尺寸
  地板),两个实例同结构且纪律明确(改门后热度类 fixture 连锁失败是正信号、修 fixture 不放门),对齐项目「第二
  实例接近/达阈值即升」惯例。建议并入 [[backend-capability-vs-profitability]] 或 prove-the-path 家族的一个小
  节,而非新开 guide。
- **教训 4(跨机器 bench 接力用 PR 评论)** → **memory 反引(第 2 实例,接近阈值,暂留观察)**。理由:两个实例
  (issue #89 arm64、本轮 amd64)已成模式,但两次都是同一人接力、场景仍窄(bench 表刷新),再攒一个跨人或跨场景
  实例更稳。暂在本篇与 [[2026-07-08-pr95-spill-stack-fuel-round]] 互相反引,下次再遇升 guide。
- **教训 5(集中 probe 产批量 issue)** → **暂留观察(process 观察)**。理由:单次样本,是好的工作方式但还不到
  可复用纪律的密度,记在 memory 供日后对照。
- **教训 6(缺数据渲染 `—`)** → **不升,memory 留档即可**。小教训,formatter 局部惯例,复用面窄。

## Follow-up

- **PR #101 收尾**:等 GitHub Actions 服务恢复后确认 CI 跑完全绿再合入。合入顺序遵教训 2——PR #100(收益门)
  已先合,#101 的 #91 relooper 放行合入时收益门已在位,无回归窗口。
- **guide 修订**:下次触碰 [[backend-capability-vs-profitability]] 时,把教训 2(合入顺序/回归窗口)与教训 3
  (fixture 多维化,连 issue #21)各作一小节并入,并在实例列表补 #91/#92。
- **P3 probe 常态化**:考虑把这场 probe 的观测手法(bridge Logger 逐 proto 决策 + cpuprofile + 控制变量
  kernel 扫描)写成一个可复跑的 P3 体检脚本,避免下次又靠零散撞见。

## 关联

[[backend-capability-vs-profitability]](教训 1/2/3 归宿:能力 vs 收益分层、`WorthPromoting` 收益门、
`MinPromotableLener` 尺寸地板同族)· [[2026-07-08-pr95-spill-stack-fuel-round]](教训 4 前序:issue #89
跨机器接力第 1 实例)· [[2026-06-17-issue21-short-proto-guard-round]](教训 3 前序:尺寸地板 fixture ≥10
opcodes 第 1 实例)· issue #91/#92/#93/#94 · PR #100(#92/#94,已合入)· PR #101(#91/#93,CI 待恢复)·
`internal/gibbous/jit`(P3 relooper `normalizeScopes` / `WorthPromoting` `straightLineMinCodeLen`)·
Bridge `OnEnterID`/`OnBackEdgeID` · README baseline `_GopherKernel` 分母
