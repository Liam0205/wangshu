---
name: 2026-07-20-concat-storm-root-cause-round
description: >
  2026-07-20 concat storm crasher 家族(issues #123-#167,横跨数周,累计 11+ 例)
  的根因定性突破轮(PR #168,修复 #166/#167 两个 nightly p3 crasher,rebase merge
  前写这篇反思)。破局全靠 PR #165 上线的取证设施:#166/#167 的 headSha 都是当时
  master HEAD 494a137——取证设施上线后家族第一次复发。两个 run 各有恰好一个长出
  header 的 worker stderr 日志,抓到完整栈迹:panic: deadlocked! + 一个 runnable
  goroutine 卡在 gc.(*Collector).stringMatches -> Intern -> crescent doConcat ->
  executeLoop。关键定性:panic: deadlocked! 是 Go fuzz 的 per-input 看门狗
  (internal/fuzz/worker.go 的 time.AfterFunc(10*time.Second, panic))——单个 fuzz
  输入跑过 10 秒就被打死。此前所有轮次误判为内存 OOM,GOMEMLIMIT 从未奏效、最小化
  corpus 本地重放永远干净,都因为死因根本不是内存而是 CPU wall-clock 撞 10 秒看门狗。
  飞行记录拿到真凶输入:for i=1,777777776 do glob = cat(i) end,cat 内
  return "<~15KB 字面量>"..i。根因链:preempt() 每指令边界只把 stepUsed 加 1,不计
  CONCAT/Intern 的字节工作量;1<<20 步预算允许约 50 万次迭代,单次 prog.Run 约 2.7s
  字节工作,FuzzAutoPromote harness 每输入跑 4 次 Run(2 State × 2 轮),4×2.7s≈11s
  > 10 秒看门狗。修复:共享 doConcat(call.go)调 chargeBulkWork(len)(state.go 新增),
  按 len>>6(1 步/64 字节)记账,使 step budget 成为字节工作量的度量;三层
  (P1 executeLoop / P3 wasm h_concat / P4 native host.Concat)全部路由同一 doConcat,
  单点记账覆盖所有 backend。比率调优曲折:初版 >>10(1 步/KiB)本地过但 CI 上
  TestIssue166_HarnessWatchdogMargin 跑到 13.5s(CI runner 比本地慢约 10×),收紧到
  >>6(1 步/64B)后本地约 0.08s、CI 约 0.9s。六条教训:取证投资兑现且兑现方式正是
  设计意图 / 静默死亡两步分类直接给答案 / 指令预算必须度量工作量而非条数 / 家族级
  误分类可持续数周(真值来自第三方证据非表象反推)/ 限流比率按最慢目标环境定且 CI
  实测 / in-test wall-clock 断言合法性看它守的是任意秒数还是真实固定阈值。
metadata:
  type: reflection
  date: 2026-07-20
---

# 2026-07-20 concat storm 根因定性突破轮反思(PR #168)

> 范围:PR #168,修复 nightly p3 crasher #166/#167。两个 commit
> `88e724f`(CONCAT 字节工作量记账)+ `ab27936`(收紧比率到 64B/step +
> 稳健测试守卫)。这是 concat storm crasher 家族(issues #123-#167,横跨
> 数周,累计 11+ 例)从「查不出死因」到「根因清晰」的定性突破轮,rebase
> merge 前写这篇反思。

## 任务

concat storm 家族此前累计 11+ 例,横跨数周,每次都是「minimized 输入本地
重放干净 + worker 无声死亡」,历轮均按 [[unreproducible-crasher-triage]]
处置(corpus 入库 + 诊断硬化),死因始终没定性,曾被反复当作内存 OOM
处理(加 GOMEMLIMIT、降 arena cap),但都不奏效。本轮 nightly 报出
#166/#167 两个 p3 crasher,任务是定性死因并修复。

## 期望与实际

- 期望:延续历轮经验,大概率又是一次「不可复现 + corpus 入库站岗」的
  止损轮。
- 实际:PR #165 上线的 worker 取证设施在这一轮第一次复发时一步给出死因,
  把横跨数周的家族从「查不出死因」变成「根因清晰」——死因不是内存,是
  单个 fuzz 输入的 CPU wall-clock 撞上 Go fuzz 的 10 秒 per-input 看门狗。

### 破局经过

1. #166/#167 两个 crasher 的 headSha 都是当时的 master HEAD `494a137`
   ——正是 PR #165 worker 取证设施上线之后,这个家族第一次复发。
2. 从 run artifact 的 `fuzz-forensics/FuzzAutoPromote/worker-<pid>-stderr.log`
   里,两个 run 各有**恰好一个**长出 header 的 worker stderr 日志,抓到了
   完整栈迹:`panic: deadlocked!` + 一个 runnable goroutine 卡在
   `gc.(*Collector).stringMatches -> gc.(*Collector).Intern ->
   crescent.(*State).doConcat -> crescent.(*State).executeLoop`。
3. 关键定性:`panic: deadlocked!` 来自 Go fuzz 的 per-input 看门狗——
   `internal/fuzz/worker.go` 里 `time.AfterFunc(10*time.Second, func(){
   panic("deadlocked!") })`,即**单个 fuzz 输入跑过 10 秒**就被打死。此前
   所有轮次误判为内存 OOM——GOMEMLIMIT 从未奏效、最小化 corpus 本地重放
   永远干净,都因为死因根本不是内存,是 CPU wall-clock 撞上 10 秒看门狗。
4. 飞行记录(`worker-<pid>-input.log`)拿到真凶输入:
   `local function cat(i) return "<大字面量>"..i end; local out=""; for
   i=1,777777776 do glob = cat(i) end`。cat 内部 `return "<字面量>"..i`
   就是 doConcat 的来源。

### 根因链

- `preempt()`(state.go)在每个指令边界(循环 back-edge + 帧进入 +
  TFORLOOP)只把 stepUsed 加 1,**不计 CONCAT / Intern 的字节工作量**。
- 一个 `for i=1,N do glob = cat(i) end`、cat 内 `return "<~15KB 字面量>"..i`,
  每次迭代只扣约 2 步,却拷贝 + intern 约 15KB。
- 1<<20 步预算允许约 50 万次迭代,单次 prog.Run 就要约 2.7 秒字节工作;
  FuzzAutoPromote harness **每个输入跑 4 次** prog.Run(2 个 State × 2 轮),
  4×2.7s≈11s > 10 秒看门狗 → worker panic → CI 报 "hung or terminated
  unexpectedly: exit status 2" → 自动开 crasher issue,落盘的是最小化后的
  **轻**输入(单次 Run < 10 秒),所以本地重放永远干净。

### 修复

- 在共享的 `doConcat`(internal/crescent/call.go,fast path + slow-path
  plain fold)里调 `chargeBulkWork(len)`(state.go 新增),按 `len >> 6`
  (1 步 / 64 字节)记账,使 step budget 成为字节工作量的度量而非仅迭代数。
- 三层(P1 executeLoop / P3 wasm h_concat / P4 native host.Concat)全部
  路由到同一个 doConcat,单点记账覆盖所有 backend,差分 fuzz 对称性不破。
- #166/#167 corpus 作为轻量 budget-bounded 回归种子入
  `testdata/fuzz/FuzzAutoPromote/`。回归测试 `issue166_concat_storm_test.go`
  (默认 build)+ `issue166_concat_storm_tiered_test.go`(p3/p4)。

### 比率调优的曲折

- 初始选 `>>10`(1 步/KiB),本地 4×run 1.33s 通过。但 PR #168 首次 CI
  失败:`TestIssue166_HarnessWatchdogMargin` 在 CI runner 上跑到 13.5s
  ——**CI runner 比本地慢约 10×**,1<<20 预算在 `>>10` 下允许约 1GiB/run
  字节工作,慢速 CI 上 4×run 就越过 10 秒看门狗。
- 收紧到 `>>6`(1 步/64B,16× 更激进):1<<20 预算约束总 concat 到约
  64MiB/run,4×run 本地约 0.08s、CI 约 0.9s。1MiB 合法 concat 记约 16K 步
  (约 1.5% 预算),<64B concat 记 0 步,正常程序不受影响。

## 踩坑与教训

### 教训 1:取证设施在这一轮兑现,且兑现方式正是当初的设计意图

PR #165 的赌注是「让复发时信息一次性够用」,这一轮第一次复发就凭 worker
stderr 栈迹一步定性,把横跨数周的家族从「查不出死因」变成「根因清晰」。

**Why**:低频罕见事件的调查成本大头不是「修」,是「等下一次复发」;等的
时候免费,但复发时若信息不够又要再等。这轮实证了 [[unreproducible-crasher-triage]]
已记录的「投资应投在让复发时信息够用」这条纪律——取证设施上线到复发之间
隔了不到两天,而家族此前空转了数周。

**How to apply**:对本地无法复现的低频事件,除了调查根因,并行投一个便宜
的诊断改动让下次复发信息够用;不要把资源全投在「这次尽力挖」。这轮是该
纪律的正面结算样本。

### 教训 2:静默死亡的两步分类这次直接给出答案

`exit status 2` = Go runtime fatal,worker 打了完整栈迹,只是此前被
/dev/null 丢弃;取证设施接住 fd 2 后,`panic: deadlocked!` 一行就把死因
从「疑似内存 OOM」翻成「fuzz 10 秒看门狗」。

**Why**:[[2026-07-19-fuzz-worker-forensics-round]] 沉淀的两步分类
(退出码语义 + 子进程 stdio 接线)本轮直接消费:exit status 2 语义说明
「有一份完整尸检报告」,取证设施保证「这份报告不再被扔进 /dev/null」,
两者合起来使这一轮的定性从数周缩到一行日志。

**How to apply**:fuzz / CI 报「进程静默死亡」时,先看退出方式语义再看
栈迹里的 panic 首行——本轮 `panic: deadlocked!` 这一行直接锁定死因是
per-input 看门狗而非资源耗尽,与历轮的内存假说完全不同类。

### 教训 3:指令预算必须是「工作量」的度量,不能只数指令条数

单条 CONCAT 能做与字节数成正比的无界工作,只扣 1 步会让 step budget 对
byte-heavy 循环完全失效。凡是单条指令能做 O(n) 工作的算子(CONCAT,以及
潜在的 string.rep / string.format / table.concat)都有这个风险。

**Why**:`preempt()` 的 stepUsed 原本是「指令条数」的计数,隐含假设是
「每条指令的工作量有常数上界」。CONCAT / Intern 打破了这个假设——单条
指令的工作量随字符串长度线性增长,于是「50 万次迭代」这个看似受预算约束
的循环,实际做了约 2.7 秒的字节工作,预算完全没管住它。

**How to apply**:设计 / 审计 budget / fuel 类限流机制时,问一句「有没有
单步能做无界工作的操作」,有就要按工作量记账(本轮 `chargeBulkWork(len)`
把 CONCAT 的字节数折算成步数)。列出全部 O(n) 单指令算子逐个检查
(string.rep / string.format / table.concat 尚未记账,是已知的下一批候选)。

### 教训 4:家族级误分类可以持续数周,真值来自第三方证据而非表象反推

concat storm 此前一直被当内存问题(加 GOMEMLIMIT、降 arena cap),因为
表象(RSS 高、minimized 重放干净)都与内存假说不矛盾。真值来自第三方
证据(取证栈迹),不是从表象反推。

**Why**:内存假说能「解释得通」每一个表象——RSS 确实高(大字符串反复
intern)、minimized 输入确实重放干净(轻输入单次 Run < 10 秒)——但它
从来修不好(GOMEMLIMIT 软限制没接住,见 [[2026-07-18-issue155-158-nightly-crasher-round]]
教训 4 的观察)。一个假说反复「解释得通但修不好」,往往整体就是错的。

**How to apply**:一个假说反复「解释得通却修不好」时,优先投资能给出
**独立第三方证据**的手段(取证 / oracle),而不是在原假说里继续调参。本轮
的第三方证据是取证栈迹,它一步把内存假说整体推翻。

### 教训 5:限流比率要按最慢的目标环境定,且 CI 上验证不能只看本地

CI runner 比本地慢约 10×,本地通过的 wall-clock 余量在 CI 上可能翻车
——初版 `>>10` 本地 4×run 1.33s 通过,CI 上
`TestIssue166_HarnessWatchdogMargin` 跑到 13.5s > 10 秒看门狗。

**Why**:byte-charge 比率把「字节工作量」换算成「步数预算」,而步数预算
最终对应的是 wall-clock 时间;同一比率在不同速度的机器上对应的秒数不同。
用本地时间做判据会系统性低估 CI 上的真实耗时。这与 [[design-claims-vs-codebase-physics]]
§6「空间维度」(同一时刻跨物理环境的差异)同族——本地与 CI runner 是两
个物理环境,速度差一个数量级。

**How to apply**:选 byte-charge / timeout 类常量时留一个数量级以上的
余量,并在 CI 实测确认,不要用本地时间做判据。收紧到 `>>6` 后本地约
0.08s、CI 约 0.9s,对 10 秒看门狗留了约 10× 余量。

### 教训 6:in-test wall-clock 断言的合法性,看它守的是任意选的秒数还是真实固定的外部阈值

承 [[unreproducible-crasher-triage]] 的「量纲错配」教训——针对「永不返回」
的回归测试不要自建 in-test deadline(与共享 runner 速度对赌),交给包级
`go test -timeout`;但本轮 `TestIssue166_HarnessWatchdogMargin` 的 8s
guard 守的是 Go fuzz **真实固定的 10 秒外部看门狗**(而非任意 in-test 界),
这是合法的——它测的正是决定 worker 存活的生产条件。

**Why**:in-test wall-clock 断言被诟病是因为它常常在断言一个「任意选的
秒数」,那个秒数与共享 runner 的速度对赌,在慢机器上会假阳性。但当断言
守的是一个**真实存在的固定阈值**(这里是 Go fuzz 硬编在 worker.go 里的
10 秒)时,断言测的就是生产条件本身,不是主观选的界——它的合法性来自
被守的阈值是客观固定的。

**How to apply**:判断一个 wall-clock 断言是否合法,看它守的是「任意选
的秒数」还是「真实存在的固定阈值」。守固定外部阈值(且留足余量)合法;
守任意选的秒数(与 runner 速度对赌)不合法,交给包级 timeout。

## 流程

取证 artifact 拿栈迹 → `panic: deadlocked!` 定位 per-input 看门狗 →
internal/fuzz/worker.go 源码核对 `time.AfterFunc(10s)` → 飞行记录取真凶
输入 → 根因链推导(每指令 1 步 vs CONCAT 字节工作)→ `chargeBulkWork`
单点记账(doConcat)→ 初版 `>>10` → PR #168 首次 CI 失败
(`TestIssue166_HarnessWatchdogMargin` 13.5s)→ 收紧 `>>6` → CI 绿 →
rebase merge 前写反思。

## Promotion 候选

- **教训 1 + 教训 2 + 教训 4**(取证兑现 + 两步分类给答案 + 家族误分类
  数周):建议并入 [[unreproducible-crasher-triage]] 作为 concat storm 家族
  的**结案数据点**——该 guide 此前记录了 #123 起的止损流程与 PR #165 的
  诊断硬化设施,本轮是这条投资的正面结算,应补一句「取证设施上线后家族
  第一次复发即定性,死因是 fuzz 10 秒 per-input 看门狗而非内存 OOM,
  历轮 GOMEMLIMIT / arena cap 都因假说整体错误而无效」。由 recorder 决定
  措辞与落点。
- **教训 3**(指令预算须度量工作量而非条数):建议提升——它是一条与
  crasher 家族无关的、可复用的限流机制设计原则,适合进
  [[design-claims-vs-codebase-physics]](budget/fuel 类机制的物理不变式)
  或独立条目。判据「有没有单步能做无界工作的操作」可操作性强,且已给出
  下一批候选算子(string.rep / string.format / table.concat)。首次成文,
  由 recorder 判断是否本轮提升。
- **教训 5**(限流比率按最慢环境定 + CI 实测):属 [[design-claims-vs-codebase-physics]]
  §6「空间维度」的一个新实例(本地 vs CI runner 速度差一个数量级),
  可在该节下次修订时作补充。
- **教训 6**(wall-clock 断言合法性判据):承 [[unreproducible-crasher-triage]]
  的「量纲错配」教训并给出补充判据(守固定外部阈值合法 / 守任意秒数不合法),
  建议并入该 guide 对应小节。

## 触发场景

- concat storm 家族(或同类)再报不可复现 crasher 时(先看取证 artifact
  的栈迹 panic 首行:`panic: deadlocked!` = fuzz 10 秒 per-input 看门狗,
  死因是单输入 CPU wall-clock 而非内存;历轮内存假说已被证伪);
- 设计 / 审计 budget / fuel / step-budget 类限流机制时(教训 3:问「有没有
  单步能做无界工作的操作」,CONCAT 已记账,string.rep / string.format /
  table.concat 尚未记账);
- 选 byte-charge / timeout 类常量时(教训 5:按最慢目标环境定 + CI 实测,
  留一个数量级余量;CI runner 比本地慢约 10×);
- 给「永不返回 / 长时间运行」类测试加 wall-clock 断言时(教训 6:守真实
  固定外部阈值合法,守任意选的秒数不合法交给包级 timeout)。

## 关联

[[2026-07-19-fuzz-worker-forensics-round]](本轮是该轮取证设施投资的正面
结算:机制 A 的 worker stderr 栈迹 + 机制 B 的飞行记录合起来一步定性,
证明该轮教训 1「两步分类」+ 机制 A/B 的设计意图完全兑现)·
[[2026-07-18-issue155-158-nightly-crasher-round]](该轮教训 4 观察到
GOMEMLIMIT 软限制没接住 41.5M execs 的静默死亡,本轮解释了原因——死因
不是 Go 堆压力而是 CPU wall-clock 撞看门狗,内存类硬化本就接不住)·
[[2026-07-11-issue123-unreproducible-crasher-round]](家族起点,首次把
concat storm 判为进程级资源耗尽 + corpus 入库,本轮修正了「资源耗尽」
的具体性质:不是内存 / mmap,是单输入 CPU 时间)·
[[unreproducible-crasher-triage]](本轮是该 guide 的家族结案数据点)·
[[design-claims-vs-codebase-physics]](教训 3/5 的提升候选落点)·
PR #168 · issue #166 · issue #167 · commit 88e724f(CONCAT 字节记账)·
commit ab27936(收紧 64B/step + 测试守卫)· PR #165(取证设施前序)
