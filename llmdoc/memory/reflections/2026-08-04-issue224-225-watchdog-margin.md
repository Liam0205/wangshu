---
name: 2026-08-04-issue224-225-watchdog-margin
description: >
  两个 nightly 自动开的 p4 fuzz crasher（#224、#225）的处理轮，分支 `fix/224-225-fuzz-crashers`，
  1 个 commit（`4c89799`），**产品代码零改动，这一轮的价值全在诊断上**。两个 seed 都是 concat storm
  家族（#123–#167）的写法——一个约 90 字节的字面量在 `for i=1,777777776` 循环里拼接，而且赋值目标被
  写错（`qut` 而不是 `out`），所以累加器根本不增长，最小化之后的 seed 天然是轻的。两个都**既不崩也不
  分歧**，本地重放各约 1 秒就正常触发预算。**关键发现是「重放干净」这次不是全部答案**：我照惯例先做
  版本核对（两个 run 都在 `093f7d1`，早于 #222 那一轮的合并），但接着做了一件之前没做的事——**把 seed
  也拿到 `093f7d1` 上跑**，结果它们在**那里也已经被界住**，所以 #222 那一轮并不是修好它们的原因，
  「过期、已修复」这个惯常结论**不成立**；run 时间确实早于合并，但那个事实与本轮无关。**真正的问题是
  余量**：`1<<20` 的 step budget 允许约 64 MiB 的 concat，本地每个 fuzz 子测试花 0.7–1.3 秒，而 CI
  运行器约慢 10 倍（这一点**仓库自己早已记在** `internal/crescent/state.go` 的 `chargeBulkWork` 注释
  里），于是这一族最慢的 seed 投射到 CI 是 12–13 秒，撞上 go-fuzz 的 **10 秒 per-input 看门狗**——六个
  家族 seed 里**两个已经超过**、四个余量不到 1.4 倍；nightly 日志印证了机制而不是靠推断（`panic:
  deadlocked`，正是那个看门狗）。修法：harness 的 step budget 从 `1<<20` 减半到 `1<<19`，新增
  `fuzz_budget_test.go` 里的 `fuzzStepBudget` 常量（带与消费方相同的 build tag），`fuzz_auto_test.go`
  与 `fuzz_p4_test.go` 四处 `SetStepBudget` 改用它。**不损失覆盖**：这一族每种写法在两个预算下都会
  触发，fuzzer 走同样的路径、只是更早停下，缩小的是「单个输入能消耗多少 wall-clock」，而那正是看门狗
  量的东西。效果：最慢子测试 1.34 秒 → 0.56 秒，六个 seed 全部拿到 ≥1.8 倍余量，corpus 全量重放
  5.5 秒 → 2.9 秒。**验证过检测能力没被削弱**：注入一个真实的 P1-vs-P4 分歧（把升层侧的返回值截断），
  在新预算下仍然 FAIL，恢复后通过；另外 60 秒引导式 fuzz 干净。四条教训：版本核对之后还要把 seed
  拿到那个旧 commit 上跑一遍以验证归因 / 一个上限只要「界住」还不够，它允许的量必须与外部看门狗差一个
  数量级 / 降低 fuzz 预算不等于降低覆盖，前提用注入缺陷来证明 / 常量要与它的消费方共享 build tag。
metadata:
  type: reflection
  date: 2026-08-04
---

# 两个既不崩也不分歧的 crasher，与一个只差 1.4 倍的余量（2026-08-04，分支 `fix/224-225-fuzz-crashers`）

> 范围：#224、#225 两个 issue，1 个 commit（`4c89799`）。**产品代码零改动**。改动落在
> `fuzz_budget_test.go`（新增 `fuzzStepBudget` 常量）、`fuzz_auto_test.go` 与 `fuzz_p4_test.go`
> （四处 `SetStepBudget` 改用它）；回归 `test/regression/issue224_watchdog_margin_test.go`；
> 两个 seed 入 `testdata/fuzz/FuzzAutoPromote/6ed94d7f9fe6248a` 与 `841bbefecf338b0d`。

## 任务

nightly 在两个夜晚自动开了两个 p4 fuzz crasher issue（#224、#225）。

## 本轮做了什么

### 1. 两个 seed 都是这个家族的写法，而且都是轻的

两个 seed 的脚本形式一样：

```lua
local function cat(i) return "<约 90 字节的字面量>" .. i end
local out = ""
for i = 1, 777777776 do qut = out .. cat(i) end
return out
```

这是 concat storm 家族（#123–#167）的第七、第八次被开成 issue。注意 `qut`——赋值目标被 fuzzer
写错了，所以累加器 `out` 从头到尾是空串，每次迭代拼的都是约 90 字节而不是越来越长的串。这正是
**最小化之后的 seed 天然是轻的**那个机制：真正把 worker 弄死的输入更重，落盘的这一个是最小化过程
自己活得下来的那个。

两个都**既不崩也不分歧**，本地重放各约 1 秒，字节记账正常把它们界住、正确抬「instruction budget
exceeded」。

### 2.「重放干净」这次不是全部答案

上一轮（#221/#222）的教训 4 已经写过：这个家族「本地重放干净」是**必然属性**，不含任何缺陷信息。
所以我没有用它结案。按 [[unreproducible-crasher-triage]]「第一步永远是版本核对」核了版本：两个 run
的 headSha 都是 `093f7d1`，**早于**昨天 #222 那一轮的合并提交。按前几轮的流程，这里就该判第一档、
在当前 HEAD 上重放确认之后结案。

**但我这一轮多做了一步：把 seed 也拿到 `093f7d1` 上跑了一遍。** 结果是它们在**那里也已经被界住**
——`093f7d1` 上跑一样是约 1 秒正常触发预算，没有任何异常。

这一步把结论整个推翻了：**#222 那一轮并不是修好它们的原因**，所以「过期、已经被上一轮修好了」这个
惯常结论**不成立**。run 时间确实早于合并，但那个事实与这两个 issue 为什么被开出来无关——它们在那个
commit 上就已经不是「无界」了。

### 3. 真正的问题是余量，而仓库自己早就把关键数字写下来了

`1<<20` 的 step budget 在 1 步 / 64 字节的比率下允许约 **64 MiB** 的 concat。这个数是 #168 那轮定的，
当时的判据是「让 4 次 `prog.Run` 在 CI 上也舒服地待在 1 秒以内」。实测本地每个 fuzz 子测试
**0.7–1.3 秒**。

而 **CI 运行器比本地慢约 10 倍**——这一点不需要我去测，`internal/crescent/state.go` 的
`chargeBulkWork` 注释里早就写着（那一轮正是按最慢的 CI runner 把比率从 1 步/KiB 收紧到 1 步/64 字节
的）。把本地耗时乘上这个倍率：

| 量 | 值 |
|---|---|
| 本地最慢的家族子测试 | 1.34 秒 |
| 投射到 CI（×10） | 12–13 秒 |
| go-fuzz 的 per-input 看门狗 | **10 秒** |
| 六个家族 seed 的余量 | **两个已经超过**，另外四个不到 **1.4 倍** |

所以这两个 issue 不是产品缺陷，是 **harness 自己的余量不够**：输入已经从「无界」变成了「只是太慢」，
而看门狗分不出这两者。

**机制是日志印证的，不是推断的**：nightly 日志里就是 `panic: deadlocked`，那正是
`internal/fuzz/worker.go` 里 `time.AfterFunc(10*time.Second, panic)` 的输出（家族根因轮 #166/#167
已经定性过这一行的含义）。

### 4. 修法：把 harness 的 step budget 减半

`fuzz_budget_test.go` 新增一个常量：

```go
//go:build (wangshu_p3 || wangshu_p4) && wangshu_profile

const fuzzStepBudget = 1 << 19
```

`fuzz_auto_test.go`（两处，`st1` 与 `stA`）与 `fuzz_p4_test.go`（两处，`st1` 与 `st4`）四处
`SetStepBudget(1 << 20)` 改用它。

**不损失覆盖，理由要说清楚**：这一族**每一种写法在两个预算下都会触发**——fuzzer 走的是同样的路径，
只是更早停下。缩小的是「单个输入能消耗多少 wall-clock」，而那恰好是看门狗量的那个量。换句话说，
减半减掉的是余量问题本身，不是探索面。

效果：

| 量 | `1<<20` | `1<<19` |
|---|---|---|
| 最慢子测试 | 1.34 秒 | **0.56 秒** |
| 六个 seed 在 CI 速度下的余量 | 两个已超、四个 < 1.4 倍 | 全部 **≥ 1.8 倍** |
| corpus 全量重放 | 5.5 秒 | **2.9 秒** |

### 5. 验证检测能力没被削弱

减小 fuzz harness 的资源上限，直觉上的风险是「它现在还能不能发现真问题」。所以我**注入了一个真实的
分歧**：把升层侧的返回值截断，制造一个 P1-vs-P4 的真差异。在新预算 `1<<19` 下 harness 仍然 FAIL；
把注入撤掉之后通过。另外跑了 60 秒引导式 fuzz，干净。

回归 `test/regression/issue224_watchdog_margin_test.go` 覆盖两种写法（累加器不增长的那一版就是两个
seed 的写法，以及累加器真的二次增长的那一版），并且**在 harness 自己的预算设定下量代价**而不是量
corpus 的耗时——实测把预算调回 8 倍会让它变红，所以它是有效的执行体。

### 6. 一个过程细节：常量的 build tag

我第一版把 `fuzzStepBudget` 写成一个**没有 build tag** 的文件，golangci-lint 报 unused。原因是消费
它的两个文件都在 `(wangshu_p3 || wangshu_p4) && wangshu_profile` 之后，**默认构建看不见它们**，于是
默认构建里这个常量确实没有任何使用者。加上相同的 tag 之后，五种构建组合都干净。

## 期望与实际

- 期望：又是两个 nightly crasher，按上一轮的流程核版本 → 在当前 HEAD 重放 → 大概是过期的，结案。
- 实际：版本核对与重放都如期，而**多做的那一步（在旧 commit 上也重放）把「已被上一轮修好」这个归因
  证伪了**。真实原因在另一个维度上——不是「有没有被界住」，而是「界住之后允许的量与外部看门狗之间还
  剩多少余量」。产品代码一行没改，改的是 harness 的一个数字。

## 教训

### 教训 1（版本核对之后，还要把 seed 拿到那个「旧 commit」上跑一遍）

我此前几轮的流程是「run 的 sha 早于修复 → 在当前 HEAD 上重放确认已修 → 结案」。这一轮加了一步：
**也在旧 commit 上重放**，结果发现它们在那里就已经被界住，于是「已被某轮修好」这个因果是错的，真实
原因在别处。

**Why**：前一轮（#212–#219）已经把「三档是成本档不是缺陷档」写进 guide，那一条修的是「不要因为 sha
旧就跳过重放」。但它只要求在**当前 HEAD** 上重放，而当前 HEAD 上通过这件事，与「是哪个改动让它通过
的」是两个问题。只在 HEAD 上重放能得到「现在没问题」，却得不到「为什么现在没问题」——而后者决定还要
不要继续查。这一轮如果停在第一步，我会写一句「已被 #222 那轮修好」，然后六个 seed 的余量问题继续留在
仓库里，下一个夜晚再开一个 issue。

**How to apply**：判据 = 确认一个 issue「已被某个改动修好」时，要同时确认**修法与现象之间真的有
因果**——把 seed 也拿到那个「修复之前」的 commit 上跑一遍，在那里也不复现就说明归因错了，得继续查。
自查办法：说「这是上一轮修好的」之前，先问「我在那个修复之前的 commit 上量过吗」；没量过，这句话就
只是时间顺序的巧合，不是因果。这与 [[prove-the-path-under-test]] §9.6「双向验证」是同一条纪律换了
时点——那条讲验证一个**修复**要在 base 上确认 FAIL，本条讲判定一个 issue **过期**要在 base 上确认它
也不复现（如果在 base 上就不复现，那个修复不是原因）。

### 教训 2（一个上限只要「界住」还不够，它允许的量必须与外部看门狗差一个数量级）

`1<<20` 确实把 concat 界住了，但它允许的 64 MiB 在 CI 上要跑 12–13 秒，而看门狗是 10 秒。输入从
「无界」变成了「只是太慢」，而看门狗分不出这两者。

**Why**：上一轮（#222）的结论是「预算必须度量工作量而非指令条数」，那一条修的是**有没有界**。本条是
它的下半句：**界住之后，那个界允许的最坏耗时还得与真正会杀掉进程的那个超时差得够远。** 一个资源上限
不是孤立生效的，它上面还叠着若干外部超时（fuzz 的 per-input 看门狗、CI job timeout、`go test
-timeout`），而这些超时量的是 wall-clock，不是步数。上限与超时之间是一个隐含的换算：步数 × 每步的
真实代价 × 目标机器的慢速倍率 = wall-clock。这三个因子里只有第一个写在代码里，另外两个要自己去量或者
去翻文档——而这一轮那个倍率**仓库自己早已记下来了**（`chargeBulkWork` 的注释），我只需要读它。

**How to apply**：判据 = 定一个资源上限时，把「这个上限允许的最坏耗时」乘上目标机器的慢速倍率，再与
那台机器上所有外部超时（看门狗、job timeout、测试 timeout）比一遍；**余量小于一个数量级就等于没有
余量**。自查办法：写下一个三项乘法——上限允许的量 × 单位量的本地耗时 × 慢速倍率，得到投射耗时，然后
列出这个投射耗时要躲开的每一个超时；任何一项余量不到 10 倍，那个上限就还没定完。反过来，`test/`
里已有的 in-test deadline 分档纪律（余量 < 10× 立刻换成包级 timeout）是同一条判据在**测试侧**的形式，
见 [[unreproducible-crasher-triage]]「重 workload regression 测试的裁判机制」。

### 教训 3（降低 fuzz 预算不等于降低覆盖，前提是那一类输入在两个预算下都会触发）

我一开始担心减半会削弱检测能力，于是注入了一个真实的 P1-vs-P4 分歧来验证——在新预算下仍然 FAIL。

**Why**：调紧 harness 的资源限制时，「会不会漏掉东西」这个担心是对的，但**它不能靠推理消除**。
推理能给的只是「这一族每种写法在两个预算下都触发，所以路径一样」，而这句话本身也是一个需要检验的
断言：预算变小之后，某些输入可能在到达被测代码之前就退出，于是测试变绿的原因从「两侧一致」悄悄换成
「这个输入根本没跑完」——这正是 [[prove-the-path-under-test]] §9.2 那一类「判据自身消耗的资源也是它
的输入」的失效方式，而且它的表现是**测试照旧通过**。唯一能区分二者的办法是**让 harness 面对一个它
必须发现的缺陷**：如果它还能发现，检测能力就还在。

**How to apply**：判据 = 调紧 fuzz harness 的资源限制（step budget、arena cap、输入长度、超时）之后，
**注入一个必须被发现的缺陷**证明检测能力未受影响，而不是只看现有用例是否仍然通过。自查办法：注入之后
harness 必须 FAIL、撤掉注入必须 PASS，两个方向都跑一遍；只跑「仍然通过」这一半，等于用一个恒真的观测
支撑一个具体结论。

### 教训 4（常量要与它的消费方共享 build tag）

`fuzzStepBudget` 第一版写在一个没有 build tag 的文件里，而消费它的两个文件都在
`(wangshu_p3 || wangshu_p4) && wangshu_profile` 之后，于是默认构建里它没有任何使用者，golangci-lint
判它 unused。

**Why**：一个标识符的「有没有被用到」不是全仓库范围的事实，而是**每一种构建组合各自的事实**。tag 化
的文件在某些组合里整个不存在，所以只要常量的可见范围比消费方**宽**，就一定存在一种组合让它孤立。本仓
的构建组合不少（默认 / p3+profile / p4+profile / oracle cgo / trace），一次只跑其中一种 vet 不足以
覆盖。

**How to apply**：判据 = 新增一个只被 tag 化文件使用的标识符时，给它**同样的** tag；并且用**消费方
的每一种 tag 组合**各 vet 一次。自查办法：写完之后问「哪一种构建组合里这个标识符会没有使用者」，答得
出来就说明 tag 还不对；答不出来就把组合列全跑一遍。

## Promotion 决策

- **教训 1** 已补进 [[unreproducible-crasher-triage]]「第一步永远是版本核对」一节，作为版本核对的
  第四步（在旧 commit 上也重放以验证归因）。
- **教训 2** 已在同一篇 guide 新增一节「上限的余量：界住不等于够快」，紧接「concat storm 家族已根因
  定性并修复」那一族之后——那一族讲**有没有界**，本节讲**界住之后余量够不够**。
- **教训 3** 已补进 [[prove-the-path-under-test]] §9.2（判据自身消耗的资源也是它的输入）作为一个
  补记：调紧 harness 资源上限时用注入缺陷证明检测能力。
- **教训 4** 已入 [[prove-the-path-under-test]] §4.10d（一个标识符的 unused 是按构建组合判定的），
  紧接 §4.10c 之后——§4.10a/b/c 都是「测试自己的前提在新位置不成立」这一族。
- 文档对齐：`docs/design/p1-interpreter/12-testing-difftest.md` 新增 §4.9a2（一个预算「界住」还不够，
  它允许的量必须与外部看门狗差一个数量级，紧接 §4.9a）、
  `docs/design/p4-method-jit/08-testing-strategy.md` §3.4 记 harness 的预算设定、
  `docs/design/engineering.md` §1.1 给那两处历史 `1<<20` 加数值补记、
  `internal/crescent/state.go` 的 `chargeBulkWork` 注释与本轮互相指引（代码本轮不改，指引写在文档侧），
  `README.md` 与 `docs/design/p1-interpreter/implementation-progress.md` 各追加本轮台账。

## 触发场景

- **判定一个 nightly crasher「已被上一轮修好」之前**：把 seed 也拿到那个修复之前的 commit 上跑一遍；
  在那里也不复现就说明归因错了（教训 1）。
- **给 fuzz harness 或任何跑在 CI 上的东西定资源上限时**：算「上限允许的量 × 单位耗时 × 慢速倍率」，
  与那台机器上每一个外部超时比余量，小于一个数量级就是没有余量（教训 2）。
- **调紧 harness 的资源限制之后**：注入一个必须被发现的缺陷，证明检测能力还在（教训 3）。
- **把一个字面量提成常量、而消费方在 build tag 之后时**：给常量同样的 tag，按消费方的每一种组合各
  vet 一次（教训 4）。
- **看到 `panic: deadlocked` 时**：那是 go-fuzz 的 10 秒 per-input 看门狗，不是内存；下一步是量
  「harness 每输入跑几次 Run × 单次耗时 × CI 慢速倍率」，不是找内存峰值。

## 关联

[[unreproducible-crasher-triage]]（「第一步永远是版本核对」本轮补第四步；新增「上限的余量」一节；
「concat storm 家族已根因定性并修复」与「那三个候选算子已结算」是本轮的上文，本轮是那一族的下半句）·
[[prove-the-path-under-test]]（§9.2 判据自身消耗的资源本轮补记教训 3；§9.6 双向验证是教训 1 的同族；
新增 §4.10d 是教训 4 的落点；§4.5d 是上一轮的落点，本轮不动）·
[[2026-08-03-issue221-222-bulk-builder-budget]]（上一轮：三个批量算子按字节记账，教训 4「本地重放
干净对这个家族天然无效」是本轮没有用重放结案的直接理由；本轮补上它的下半句——重放干净之后还要问
「是什么让它干净的」）·
[[2026-07-20-concat-storm-root-cause-round]]（家族根因定性轮：`panic: deadlocked` 的含义、10 秒
看门狗、`chargeBulkWork` 的 1 步 / 64 字节比率与「CI 比本地慢约 10 倍」这个数字的出处）·
[[2026-08-02-issue212-219-fuzz-crasher-batch]]（版本核对三档是成本档不是缺陷档——本轮教训 1 是它的
延伸）·
`fuzz_budget_test.go::fuzzStepBudget` · `fuzz_auto_test.go` · `fuzz_p4_test.go` ·
`test/regression/issue224_watchdog_margin_test.go` ·
`internal/crescent/state.go::chargeBulkWork`（CI 慢 10 倍这件事记在它的注释里）·
`docs/design/p1-interpreter/12-testing-difftest.md` §4.9a2 ·
`docs/design/p4-method-jit/08-testing-strategy.md` §3.4 ·
`docs/design/engineering.md` §1.1（那两处 `1<<20` 是历史数值，已加补记）
