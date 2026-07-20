---
name: 2026-07-12-i123-deadline-to-package-timeout-round
description: >
  i123 回归测试内建 wall-clock deadline 从 5s 一路加到 30s 后又在 p4/macos-latest
  上失败一轮(PR #129,分支 fix/i123-deadline-headroom,单 commit 706ba26)。
  该测试的失败画像是「#123 类段内无限循环永不返回」,任何有限 in-test 上限都能抓到
  它,同时任何有限上限都在跟共享 runner 的可变速度对赌——5s→30s→120s 三档常量演进
  本身就证明这类失败对常量修改免疫。第一反应「再加大到 120s」被用户一句「解决本质
  问题吗?以后就一定不可能再因此出错了吗?」打回。结构性修法:整段删掉自建的
  runWithDeadlineErr wall-clock,把「永不返回」的判定交给 go test 包级
  -timeout(默认 10min,CI 未覆写),因为它本身就是 harness 内建的兜底,触发时
  还带全体 goroutine 栈 dump 比一行 t.Fatalf 严格更优;误报余量从约 6× 拉到约 28×,
  唯一代价是本该失败的用例失败得更慢。核心可复用教训:「测试想抓的是『永不返回』、
  裁判量的是『有限秒数』」是量纲错配,任何有限 in-test deadline 都无法把「真挂了」
  和「合法地慢」区分开;测试若失败模式是非终止,应优先经 harness 自带 timeout
  兜底(package -timeout)而非自建 per-run deadline,后者只在测试需要在超时后继续
  执行(清理 / 对比 / 累积)时才有正当理由,i123 不需要。附:forloop_nan_limit_test.go
  一样的 mustFinish 10s 模式(3 调用点)本轮刻意不动,因为空循环体形状排空只要毫秒,
  10s 比 i123 的约 1.4× 余量宽约 1000×,余量差异使其暂不需要同种修法,若日后同样闪
  失就套一样的药;这是升级候选的观察项。过程侧一件事:本轮用户当场抓到我在
  force push 上用了 `git push ... | grep | head` 管道,违反了刚在 memory 里落定的
  push 纪律——纪律涵盖所有 push 含 force push,已同日回补入 memory 文件。PR #129
  CI 全绿 + bot APPROVE,应用户要求停在合入前等 review。
metadata:
  type: reflection
  date: 2026-07-12
---

# i123 从 wall-clock deadline 到 package timeout 的一轮(2026-07-12,PR #129)

> 范围:分支 `fix/i123-deadline-headroom`,PR #129,单 commit 706ba26。承
> [[2026-07-11-issue123-unreproducible-crasher-round]] 把 corpus `326b508e` 入库
> 常驻回归的后续——corpus 是同一个,测试是同一个,这轮修的是这个测试的裁判机制
> 本身。

## 任务

master ci run 在 PR #128(cgo oracle fuzz)rebase merge 之后第一次跑就在 test (p4 /
macos-latest) 上失败:`TestI123_NightlyCorporaMirrorFuzzHarness/i123_326b508e`
报「P1 run 1 did not terminate within 30s」。PR #128 完全没碰解释器热路径,失败
纯粹来自 runner 慢。任务是把这个每次都可能因 runner 快慢触发的假失败结构性解决
掉。

## 期望与实际

- 期望:调 deadline 常量把误报余量做够,以后不再因 runner 慢触发。
- 实际:意识到「调常量」这条路本身就是一直在跑输的赛道——deadline 从最初的 5s
  加到 30s(commit 6b48f39)之后又在 p4/macos-latest 上失败;`-race` build 本机
  跑一次要约 21s,共享 runner 的慢没有上限,任何有限常量都是在跟 runner 速度对赌。
  修法转向删掉自建的 `runWithDeadlineErr`,把「永不返回」的判定交给 go test
  包级 `-timeout`(默认 10min)。

## 常量演进史(证明这个类别的失败对常量修改免疫)

- 初版:每次 run 的 wall-clock 上限 5s——在 ubuntu-latest P1 leg 失败;
- commit 6b48f39「widen #123 regression deadline to 30s for shared runners」:
  5s → 30s;
- 本轮之前:30s 在 p4/macos-latest 上失败;
- 我的第一反应:30s → 120s(还是常量);
- 用户反问:「解决本质问题吗?以后就一定不可能再因此出错了吗?」
- 定下来的修法:整段删掉 wall-clock,交给 go test 包级 `-timeout`。

一个常量已经涨了 24× 还是能被打穿,这说明失败不是「常量还不够大」,是「用有限
常量当裁判」这件事本身不对。

## 结构性修法

删掉自建的 `runWithDeadlineErr`(goroutine + channel + `time.After` 实现的每次
run wall-clock)。两条腿恢复为内联的 `prog.Run(st1)`(P1 baseline)与
`prog.Run(stA)`(auto-promote),一旦命中 #123 类段内无限循环,go test 包级
`-timeout`(默认 10min,CI 未覆写)会把整个 test binary 拿下,同时 dump 全部
goroutine 栈——严格优于旧版本一行 `t.Fatalf("... did not terminate within Ns")`
提供的信息量。

关键论证(接下来复用):

- i123 这个测试要固定住的是 #123 类段内无限循环——从字节码语义看它是**永不返回**
  的,不是「很慢的合法路径」;
- 任何**有限** in-test 上限都能抓到「永不返回」,所以选具体值本身没有正确性意义;
- 唯一的选值考虑是「误报余量」——`-race` build 一次合法 run 约 21s,go test
  默认 timeout 10min,余量约 28×,而原来的 30s 只有约 1.4×;
- 代价:本该失败的用例失败得更慢(从 30s 到 10min),但本该失败的用例本来就要
  失败,慢一点无所谓;
- 副产物:go test 的 `-timeout` 触发时自带 goroutine dump,比自建的一行 fatal
  信息量高一个数量级。

## 核心教训

### 教训 1(测试要抓的是「非终止」、裁判量的是「有限秒数」——量纲错配)

「真的挂了」和「合法地慢」这两件事**没有任何有限的 in-test deadline 能区分开**,
这是量纲错配,不是常量选得不够大。5s → 30s → 120s 的常量演进史本身就是这个错
配的证据:每次常量涨了 N× 之后另一档 runner 还能把它打穿,因为共享 runner 的
慢没有物理上限,而合法 run 的时间是一个跟 runner 速度线性挂钩的量。

**Why**:in-test 自建 deadline 的形式是「过了 N 秒还没返回就报错」,它把「非终止」
近似成「非常慢」。近似只在「合法 run 的实际时间」有一个稳定上界时才成立,共享
runner 不提供这个上界。语言 harness 提供的包级 `-timeout` 也是有限的,但它的默认
值(10min)比任何合法 run 都高一到两个数量级,把误报余量拉到 in-test deadline
做不到的量级,同时它触发时的信息量(全体 goroutine 栈)也严格更高。

**How to apply**:一个测试的失败模式如果是「被测代码永不返回」,优先用 harness
自带的包级 timeout(Go 里是 `go test -timeout`),不要在测试内部自建 wall-clock
deadline。自建 deadline 只在这两种情况才有正当理由——(a) 测试需要在超时后**继续
执行**(跑清理 / 对比结果 / 累积数据),或者 (b) 一个用例的失败模式是「慢过某个
明确阈值」而非「永不返回」。i123 两条都不属于:它是端到端 run,超时后没有清理
要跑,而且它要抓的就是无限循环不是「慢过阈值」。

### 教训 2(余量差异是决定要不要现在同修一样模式的判据)

`test/regression/forloop_nan_limit_test.go` 有一样的 `mustFinish` 10s deadline 模式(三个调用点),
本轮**刻意不改**。原因是它的形状排空只要毫秒——空循环体、tiny budget——10s 相对
它约 1000× 余量,i123 的 30s 是约 1.4×;一样模式在 i123 上是每次都可能触发的
定时炸弹,在 forloop_nan_limit 上是天体尺度的余量。修法应不应该同步扩到全仓,
不由「模式一样」定,由「误报余量」定。

**Why**:in-test deadline 是一个「跟 runner 速度对赌」的模式,余量决定了这个赌
在实践中输不输。对同一模式在不同用例上的处置差异,应基于对每个用例的具体余量
估算,不基于「模式统一性」的洁癖。

**How to apply**:遇到一样的可疑模式(自建 wall-clock deadline)在多处使用时,
按余量分档处置——余量 < 10× 的立刻替换成包级 timeout;余量 10×~100× 的作观察
项,记进 promotion watch,一旦某档 runner 打穿了就同样修;余量 > 100× 的暂不动。

### 教训 3(用户一句反问把「加常量」路径打回,是决策方向的杠杆点)

我最初的实施是 30s → 120s,commit message 都想好了。用户的反问:「解决本质问题
吗?以后就一定不可能再因此出错了吗?」直接把决策从「调常量」逼到「换机制」。
这类反问的价值在于它挑战的不是具体数字,是整条路径的必然性——若答案是「再涨
一档也可能被下一档 runner 打穿」,那这条路径本身就是错的。

**Why**:在「调常量」这个动作里循环时,决策者容易被局部合理性吸住(30s → 120s
显然「更保险」),看不到整个路径都在跟一个没有上界的量对赌。外部反问「这条路
径能收敛吗」是打破这种局部锁定最便宜的动作。

**How to apply**:自己在做「调常量以修 flake」类修复时,主动问一遍「若这个常量
再被打穿,下一步是什么?若下一步还是调这个常量,那这条路径能收敛吗?」——不能
收敛就换机制。

## Promotion 候选

- **教训 1**(「非终止」与「有限秒数」的量纲错配,in-test deadline vs harness
  包级 timeout 的正确分工)**首次样本暂留观察**。这是 `[[prove-the-path-under-test]]`
  的邻族但不是同一家族——那篇管「绿色不等于在测你以为在测的」(路径是否被走到),
  本条管「测试机制能不能真判定它想判定的事」(裁判量纲选对没),都是「测试自身
  是否称职」的元问题。若后续再出现「自建 wall-clock deadline 被 runner 慢打穿 →
  换 harness 自带 timeout」的第二实例,可升 guide,暂名
  `test-timeout-mechanism-selection` 或并入 prove-the-path-under-test 作
  「裁判机制侧」新节。
- **教训 2**(余量差异决定是否同修同模式)首次样本暂留。
- **教训 3**(反问路径必然性)是 process 教训,不升 guide,memory 内保留。

## 触发场景

- 一个测试因 CI runner 慢触发内建 wall-clock deadline 报「did not terminate
  within Ns」时(教训 1:先问失败模式是「永不返回」还是「慢过明确阈值」,前者
  用 harness 包级 timeout,后者才需要自建 deadline;确认没有超时后继续执行的
  需要)。
- 全仓要不要同步替换一样的可疑模式时(教训 2:按每处的误报余量分档,不按模式
  统一性)。
- 发现自己在「调常量以修 flake」的第 N 轮时(教训 3:反问下一档打穿后还剩什么
  路径,不能收敛就换机制)。

## 过程侧一件事(push 纪律涵盖 force push)

本轮 push 时用了 `git push --force-with-lease ... 2>&1 | grep -E ... | head -20`
管道,被用户当场抓到——刚在同日 memory 落定的 push 纪律(push 之后等 pre-push
hook self-wrapper 自报、不主动 poll)涵盖**所有** push 含 force push,不因为是
force push 就例外。已同日回补入 memory 文件。

**How to apply**:任何 `git push` 命令(含 `--force` / `--force-with-lease`)都
不管 pipe grep / head / awk 类过滤,直接裸跑,让 hook 的完整输出在下一句自报里
呈现。

## 验证

- PR #129 CI 全部检查绿(含跨平台 test / fuzz-smoke / difftest / conformance
  矩阵);
- bot 已 APPROVE(rebase-merge 建议);
- 应用户要求停在合入前等 review,未 merge。

## 关联

[[2026-07-11-issue123-unreproducible-crasher-round]](本轮的直接前序——那轮把
corpus `326b508e` 入库常驻回归,本轮修这个回归测试的裁判机制;两轮共用同一段
corpus 与同一个测试函数)· [[unreproducible-crasher-triage]](2026-07-11 反思
promote 出的 guide,本轮修的是它落地的 harness 侧裁判机制)·
[[prove-the-path-under-test]](邻族:那篇管路径是否被走到,本轮管裁判机制能否
判定它想判定的事,都是「测试自身是否称职」的元问题)· issue #123 · PR #129 ·
commit 706ba26 · `test/regression/issue123_regression_test.go` ·
`testdata/fuzz/FuzzAutoPromote/326b508ea720a654`
