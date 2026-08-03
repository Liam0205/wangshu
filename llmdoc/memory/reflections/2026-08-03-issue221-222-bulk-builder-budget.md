---
name: 2026-08-03-issue221-222-bulk-builder-budget
description: >
  两个 nightly 自动开的 fuzz crasher（#221、#222）的处理轮，分支 `fix/221-222-fuzz-crashers`，
  1 个 commit（`7119391`）。**#221 过期**：seed 是 `print(pcall(math.mod))`，正是上一轮
  （PR #220 / `ac21b91`）给 `__wrapArgOrder` 补上 `math.mod` 别名之后修掉的那个，它的 run 跑在
  `947fbda` 上、早于那个修复；但没有因为 sha 旧就下结论，实际重放确认 PASS。**#222 是真的**，
  而且是 concat storm 家族（#123–#167）的下一批：seed 是一个 777777776 次迭代的字符串拼接循环，
  target `FuzzAutoPromote`，run 跑在上一轮的合并提交 `093f7d1` 上。**我先走错了两次方向**——
  seed 本地重放只要 0.77 秒、在自己 corpus 里只是第三重的（1.22s / 1.21s / 0.77s），所以「太重」
  解释不了；又量了内存，单 seed 峰值 RSS 只有 106 MB 而 CI 用 `GOMEMLIMIT=512MiB`、整个 corpus
  并行重放峰值 525 MB，也不构成死因。**真正定下来的方式是去读 guide 里这个家族自己的结论**：
  #166 那轮已经查清死因是 CPU wall-clock 撞 go-fuzz 的 10 秒 per-input 看门狗（不是内存 OOM），
  而那份 guide **点名列出了剩下的候选**：`string.rep` / `string.format` / `table.concat`「尚未按
  工作量记账，是同类风险的候选」。实测证实了这个预测：在 1<<20 step budget 内、且完全没有触发
  预算的情况下，三者各自的紧循环分别跑了 **21 秒 / 20 秒 / 53 秒**，单次 `prog.Run` 就已经超过
  10 秒看门狗，而 `FuzzAutoPromote` 每个输入要跑**四次** Run。修法：`internal/crescent/state.go`
  导出 `ChargeBulkWork`（包装既有的 `chargeBulkWork`），三个 stdlib 函数各自按**产出字节数**记账，
  21/20/53 秒变成 46/90/70 毫秒且预算正确触发，八种普通写法（含 1 MiB 的 `string.rep`、10000 元素
  的 `table.concat`）与 lua5.1 逐字节一致。`table.concat` 有个细节：记账必须按**字节**而不是元素
  个数——它的遍历本来就被表的长度界住，所以「256 个 2KB 的元素」按个数看很便宜、实际要 53 秒。
  四条教训：一个家族的第 N 次复发先去读那个家族自己的结论 / 记账的计量单位要和真实成本同量纲 /
  新增的资源判据要复用既有的计量器而不是自建第二个阈值 / 「本地重放干净」对这个家族天然无效。
metadata:
  type: reflection
  date: 2026-08-03
---

# 一个过期的 crasher，与 guide 早就写好的那份答案（2026-08-03，分支 `fix/221-222-fuzz-crashers`）

> 范围：#221、#222 两个 issue，1 个 commit（`7119391`）。产品侧改动落在
> `internal/crescent/state.go`（导出 `ChargeBulkWork`）、`internal/stdlib/stdlib.go`
> （`string.rep`）、`internal/stdlib/stringlib.go`（`string.format`）、
> `internal/stdlib/tablelib.go`（`table.concat`）；测试落在
> `test/regression/issue222_bulk_builder_test.go`；#222 的 seed 入
> `testdata/fuzz/FuzzAutoPromote/ad96f441153507ff`。

## 任务

nightly 在两个夜晚自动开了两个 crasher issue（#221、#222）。

## 本轮做了什么

### 1. #221 过期，但仍然实际重放了一遍

按 [[unreproducible-crasher-triage]]「第一步永远是版本核对」先核版本。#221 的 seed 是
`print(pcall(math.mod))`，正是上一轮（PR #220，commit `ac21b91`）给 `__wrapArgOrder` 补上
`math.mod` 别名之后修掉的那一个；它的 fuzz run 跑在 `947fbda` 上，`git merge-base --is-ancestor`
确认那个 commit 早于修复。

**但我没有因为 sha 旧就下结论。** 上一轮（#212–#219）的教训正是「三档是关于要不要复现一遍的
分类，它不回答有没有缺陷」，所以把 seed 在当前 HEAD 上实际重放了一遍确认 PASS。这一次结论与
版本核对一致，属于第一档，一行代码没改。

### 2. #222 是 concat storm 家族的下一批，而答案早就写在 guide 里

#222 的 seed 是一个 777777776 次迭代的字符串拼接循环（`local out = ""; for i = 1, 777777776 do
qut = out .. cat(i) end`，`cat` 内返回一段约 100 字节的字面量拼上 `i`），target 是
`FuzzAutoPromote`（p4 tier），run 跑在 `093f7d1`——那就是上一轮的合并提交本身，所以这是真的新
问题。

**我一开始走错了两次方向。**

第一次怀疑「这个 seed 太重」：本地重放只要 **0.77 秒**，而且在自己那个 corpus 里只是**第三重**的
（1.22 秒 / 1.21 秒 / 0.77 秒）。如果 0.77 秒能把 worker 弄死，那两个更重的早就该先死。「太重」
解释不了。

第二次怀疑内存：量了单 seed 的峰值 RSS，只有 **106 MB**，而 CI 用的是 `GOMEMLIMIT=512MiB`；整个
corpus 并行重放的峰值是 **525 MB**，刚刚擦到限制而那是一个软限制（`SetMemoryLimit` 只影响 GC
节奏，不会主动 fatal）。都不构成死因。

**真正定下来的方式是去读 `llmdoc/guides/unreproducible-crasher-triage.md` 里这个家族自己的
结论。** 那份 guide 的「concat storm 家族已根因定性并修复」一节写着：该家族的死因在 #166 那轮
已经查清——**不是内存 OOM，而是 CPU wall-clock 撞 go-fuzz 的 10 秒 per-input 看门狗**
（`internal/fuzz/worker.go` 里的 `time.AfterFunc(10*time.Second, panic)`），表现为
`hung or terminated unexpectedly: exit status 2`；CONCAT 当时通过 `chargeBulkWork`（按
`len>>6`，1 步 / 64 字节）把字节工作量折进 step budget 修好了。而那一节的「范围」段**明确点名**
了剩下的候选：

> 已知的下一批无界单指令算子（string.rep / string.format / table.concat）尚未按工作量记账，
> 是同类风险的候选。

也就是说：我怀疑的两件事都被自己的测量否掉了，而答案在 issue 被开出来之前就已经写在文档里。

### 3. 实测证实了那个预测

在 1<<20 step budget 内、**并且完全没有触发预算**的情况下，三个算子各自的紧循环耗时：

| 算子 | 写法 | 记账前 | 记账后 |
|---|---|---|---|
| `string.rep` | `for i=1,1000000 do out=string.rep("abcdefgh",4096) end` | **21 秒** | 46 毫秒（预算正确触发） |
| `string.format` | 4 个 4 KiB 的 `%s`，循环一百万次 | **20 秒** | 90 毫秒（预算正确触发） |
| `table.concat` | 256 个 2 KiB 元素的表，拼十万次 | **53 秒** | 70 毫秒（预算正确触发） |

单次 `prog.Run` 就已经超过 10 秒看门狗，而 `FuzzAutoPromote` harness 每个输入要跑**四次** Run
（2 个 State × 2 轮）。这与 #166 那轮的机制逐字一致。

### 4. 修法：三个算子走同一个计量器

`internal/crescent/state.go` 导出 `ChargeBulkWork`（一行包装既有的 `chargeBulkWork`），三个
stdlib 函数各自按**产出的字节数**记账：

- `string.rep`（`internal/stdlib/stdlib.go::stringFnRep`）：`len(s) * n`，在既有的 1 GiB
  hardening 上限检查之后；
- `string.format`（`internal/stdlib/stringlib.go::stringFnFormat`）：`len(out)`，在格式解析完成、
  intern 之前；
- `table.concat`（`internal/stdlib/tablelib.go::tableFnConcat`）：分隔符字节 + 各元素字节之和。

**没有各自定一个新阈值**，三个都走 CONCAT 那一个计量器，于是「批量工作」在 step budget 里只有
一个定义。

普通写法不受影响：八种常规写法（`string.rep("ab",3)`、**1 MiB 的 `string.rep`**、
`string.format("%s-%d",...)`、`table.concat({1,2,3},",")`、1000 元素与 **10000 元素**的
`table.concat` 等）与 lua5.1 逐字节一致——比率是 1 步 / 64 字节，1 MiB 的产出约记 16K 步，占
1<<20 预算约 1.5%。

### 5. `table.concat` 必须按字节记账，不能按元素个数

这是本轮唯一一处「顺手写下来的第一版是错的」：`table.concat` 的遍历本来就被表的长度界住，所以
按元素个数看它永远便宜——**256 个元素**听起来微不足道，而每个元素 2 KiB 时实际要 **53 秒**。代价
是拼出来的字节数，不是走过的元素个数，所以记账要读的就是那个字节数。

## 期望与实际

- 期望：又是两个 nightly crasher，一个大概过期，另一个按现象顺着查根因。
- 实际：过期那个如期收工；另一个我用两次测量分别否掉了自己的两个假设，之后才想起去读那个家族
  在 guide 里的既有结论——而那份结论不但已经定性（CPU wall-clock 撞看门狗、不是内存），还把
  下一批候选算子按名字列了出来。**顺着现象重新推导的那两步，是可以整段跳过的。**

## 教训

### 教训 1（一个家族的第 N 次复发，先去读那个家族自己的结论，而不是从现象重新推）

我先怀疑「这个 seed 太重」（0.77 秒，corpus 里只是第三重的）、再怀疑内存（峰值 106 MB，CI 限制
512 MiB），两条都被自己的测量否掉；而 guide 里 #166 那轮不但已经定性，还点名列出了下一批候选
算子。

**Why**：一个跨越数周、累计十几例的 crasher 家族，它的既有结论是**别人（包括过去的自己）已经付
过调查代价**的产物，而现象是每一例各自的表面。从现象重新推导等于把那份代价再付一遍，而且推导的
起点通常就是那一例最显眼的那个量（这一次是「重」和「内存」），恰好是家族历史上已经被证伪过的
两条。这与 [[unreproducible-crasher-triage]]「第一步永远是版本核对」是同一个层级的判据：都是**先
用最便宜的检查排除掉最便宜的解释**，而「读一遍这个家族的结论」比跑任何一条测量都便宜。

**How to apply**：判据 = 处理一个明显属于已知家族的新 issue 时，第一步是 grep 那个家族在 guide /
反思里的既有结论，特别是「范围」「后续候选」「已知未覆盖」这类小节——答案可能在 issue 被开出来
之前就写好了。自查办法：动手做第一次测量之前，先说出这个家族上一次的定性结论是什么；说不出来就
说明还没读。

### 教训 2（记账的计量单位要和真实成本同量纲，别用「个数」代替「字节」）

`table.concat` 的遍历被表长界住，按元素个数看永远便宜（256 个），而成本是拼出来的字节数
（256 × 2 KiB，53 秒）。

**Why**：一个批量操作的「规模」往往有好几个可读的量（元素个数、循环次数、输入长度、产出长度），
它们只有一个与真实代价成正比，而最容易拿到的那个常常不是它。挑错量的后果不是记账偏小一点，而是
**整类输入完全绕过记账**：只要那个量被别的东西界住（这里是表的长度），预算就再也拦不住成本沿另
一个维度增长。这与 [[prove-the-path-under-test]] §8「度量单位选错，把从没生效的优化读成生效但
太贵」是同一个错误在**限流侧**的形式：那条讲读性能数字时选错单位，本条讲写预算时选错单位。

**How to apply**：判据 = 给一个批量操作定预算前，先写下一句「这次调用的代价 ≈ 什么的函数」，再
让记账直接读那个量。自查办法：构造一个让「便宜的那个量」保持很小、而「真实成本的那个量」推到很大
的输入（256 个 2 KiB 的元素就是这样构造的），看记账有没有跟着涨；不涨就说明量错了东西。

### 教训 3（一个新增的资源判据要复用既有的那个计量器，而不是自建第二个阈值）

三个函数都走 `chargeBulkWork`，于是「批量工作」在 step budget 里只有一个定义。

**Why**：如果各自定一个阈值，它们迟早会互相漂移——三个数字分别在三次不同的调优里被改动，而**哪个
先触发**会变得难以预测，于是「这个脚本为什么超预算」这句话不再有单一答案。更要紧的是预算本身是一个
**可加的量**，多个阈值表达不了「几种批量操作叠加起来超了」这件事，而这恰恰是 fuzzer 会构造的写法。
这与 [[prove-the-path-under-test]] §4.5b「一个上限必须区分它约束的是正确性还是资源」互补：那条讲
两个**目的不同**的阈值不该共用一个数，本条讲两个**目的相同**的判据不该拆成两个数。两条一起就是
「按目的分组，同组共用一个计量器」。

**How to apply**：判据 = 给一个新算子加资源限制前，先 grep 同一类资源已经有没有计量器；有就接上去
（必要时把它导出），不要在新算子里写一个自己的阈值。自查办法：问「这个新限制与既有那个，同时被
一个脚本触碰时谁先触发」——答不出来就说明这里应该只有一个计量器。

### 教训 4（「本地重放干净」对这个家族天然无效，不构成「已修复」的证据）

#222 的 seed 本地重放 0.77 秒干净，而它确实对应一个真实的产品缺口。

**Why**：这个家族的死因是**单次 Run 的 wall-clock 撞 10 秒看门狗**，而落盘的是**最小化之后的轻
输入**（单次 Run 远低于 10 秒，否则最小化过程自己就会被打死）。所以「重放干净」是这类 artifact 的
**必然属性**，它不含任何关于有没有缺陷的信息——这一点 guide 里早已记下（「minimized 输入本身往往
不是死因」）。把它读成「已修复」，等于用一个恒真的观测去支撑一个具体结论。

**How to apply**：判据 = 遇到 `hung or terminated unexpectedly` 这类死因时，不要用「单 seed 本地
重放通过」来结案；要去量「harness 每个输入跑几次 Run × 单次耗时」是否逼近看门狗，以及那个脚本的
写法里有没有单条操作能做与字节数成正比的无界工作。自查办法：把 seed 的写法抽成一个**不最小化**的
紧循环（本轮就是这样把 21/20/53 秒量出来的），在 harness 一样的 budget 下量单次 Run 的耗时；轻输入
量不出问题，重写法一量就出来。

## Promotion 决策

- **教训 1** 与 **教训 4** 已补进 [[unreproducible-crasher-triage]]「concat storm 家族已根因定性
  并修复」一节：那一节的「范围」段原本写着三个算子「尚未按工作量记账，是同类风险的候选」，本轮把
  它结算成已完成，并把「预测被验证」这件事本身作为判据写下来（先读家族结论、再从现象推）。
- **教训 2** 与 **教训 3** 已补进 [[prove-the-path-under-test]] 新增的 §4.5d（一个资源预算的计量
  单位要与真实成本同量纲，且同类资源只该有一个计量器），紧接 §4.5c 之后——§4.5/§4.5b/§4.5c 讲阈值
  的**数值**与**关系**，本节讲那个数**量的是什么**。§10 触发场景速查同步加了一条。
- VM 行为侧的对账已入 `docs/design/p1-interpreter/implementation-progress.md`（把「已知下一批候选」
  那句改成已结算，并追加本轮的 crasher 台账条目）。

## 触发场景

- **拿到一个明显属于已知 crasher 家族的新 issue 时**：先 grep 那个家族在 guide / 反思里的既有结论，
  尤其看「范围」「后续候选」小节，再决定要不要从现象查（教训 1）。
- **给一个批量操作（拼接 / 重复 / 格式化 / 序列化）定预算时**：先写清「代价 ≈ 什么的函数」，让记账
  读那个量；用「便宜的量小、真实成本的量大」的输入自查（教训 2）。
- **给一个新算子加资源限制时**：先 grep 同类资源已有的计量器，接上去而不是自建阈值；问「两个限制
  同时被触碰时谁先触发」（教训 3）。
- **看到 `hung or terminated unexpectedly` / `panic: deadlocked!` 类死因时**：别用「单 seed 本地
  重放干净」结案；把写法抽成不最小化的紧循环，在 harness 一样的 budget 下量单次 Run 耗时（教训 4）。
- **给 stdlib 加一个新的字符串构造函数时**：它的产出字节数要经 `ChargeBulkWork` 记账，否则它就是
  这个家族的下一个候选。

## 关联

[[unreproducible-crasher-triage]]（第一步永远是版本核对——#221 是第一档的又一个实例；
「concat storm 家族已根因定性并修复」一节是本轮的主场，它的「后续候选」预测在本轮被结算）·
[[prove-the-path-under-test]]（§4.5b 阈值按目的分开是教训 3 的对偶；§8 度量单位选错是教训 2 的
同族；新增 §4.5d 是教训 2/3 的落点）·
[[2026-07-20-concat-storm-root-cause-round]]（家族根因定性轮：10 秒看门狗、`chargeBulkWork` 按
`len>>6` 记账、教训 3「指令预算必须度量工作量而非条数」——本轮是那条教训的第二次消费）·
[[2026-08-02-issue212-219-fuzz-crasher-batch]]（上一轮：`math.mod` 别名漏网就是 #221 的那个修复；
「三档不回答有没有缺陷」是本轮仍然实际重放 #221 的理由）·
`internal/crescent/state.go::ChargeBulkWork` · `internal/stdlib/stdlib.go::stringFnRep` ·
`internal/stdlib/stringlib.go::stringFnFormat` · `internal/stdlib/tablelib.go::tableFnConcat` ·
`test/regression/issue222_bulk_builder_test.go` ·
`docs/design/p1-interpreter/10-stdlib.md` §3.1a（三个批量构造函数的字节记账）·
`docs/design/p1-interpreter/12-testing-difftest.md` §4.9a（hardening 上限与 step budget 记账是两件事）·
`docs/embedding-tiers.md` §5（step budget 的语义现在含三个 stdlib 构造函数）
