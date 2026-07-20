---
name: 2026-07-18-review-p3-loopfuel-rearm-round
description: >
  2026-07-18 外部增量 code review 修复轮(分支 fix/review-p3-loopfuel-rearm,
  PR #161)。review 报告 `.code-review/from-6a55d1e/increment-1-to-d90641a.md`
  指出两个问题,均先写定向验证确认属实、再经用户 /goal 指令修复。① P3 loop fuel
  在预算开启后不重新武装:Safepoint 在无预算时把 wasm 线性内存的 fuel 字填成
  loopFuelUnlimited(1<<30),SetStepBudget/SetCancelHook 只改 Go 侧字段,
  「先无预算跑热 P3 循环、再开预算」的顺序下新预算要等约十亿次 back-edge(回边)
  后才被查询;修法在 enterGibbous 镜像 P4 RefreshJitCtxAddrs 的 armed-state
  transition 处理。② scripts/go-fuzz.sh 与 triage guide 声称 GOMEMLIMIT 会触发
  带栈的 Go OOM fatal——核对官方文档为纯软限制,运行时任何情况下不会因它主动
  fatal;该错误认知从 #123 轮起传播三周、跨四个载体。三条教训:A 复现「机制没
  生效」的指控时,复现测试自己也要 prove-the-path(第一版 FORLOOP 复现走了解释
  器路径差点误判问题不存在);B host-API 调用时序类 bug 用定向回归测试而非硬造
  fuzz target;C 机制性声称写进注释/文档时须附官方文档或实验佐证,引用时值得
  花一分钟 go doc 核对原文。
metadata:
  type: reflection
  date: 2026-07-18
---

# 外部 review 修复轮:P3 loop fuel 重新武装 + GOMEMLIMIT 文档订正(2026-07-18,PR #161)

> 范围:分支 `fix/review-p3-loopfuel-rearm`,PR #161。外部增量 code review
> `.code-review/from-6a55d1e/increment-1-to-d90641a.md` 指出两个问题,先验证
> 后修复,拆三个 commit(`2945b57` P3 fix / `bc29cd5` go-fuzz.sh 注释 /
> `40979a1` llmdoc guide 订正)。

## 任务

处置外部增量 code review 的两条指控:

- **问题 1(重要)**:P3 loop fuel 在预算开启后不重新武装,「先无预算跑热
  P3 循环、再开预算」的调用顺序下预算长期失效;
- **问题 2(重要)**:`scripts/go-fuzz.sh` 注释与
  `llmdoc/guides/unreproducible-crasher-triage.md` 对 GOMEMLIMIT 的机制性
  描述与官方文档不符。

## 期望与实际

### 问题 1(P3 loop fuel 不重新武装)

- 期望:`SetStepBudget` / `SetCancelHook` 开启后,已升 P3 的热循环在下一个
  quantum 内就受预算约束。
- 实际:`Safepoint` 在无预算/无取消回调时把 wasm 线性内存里的 fuel 字填成
  `loopFuelUnlimited`(1<<30),而 `SetStepBudget` / `SetCancelHook` 只改
  Go 侧字段、不碰 fuel 字。全仓只有 State 初始化和 `Safepoint` 两处写这个
  字,所以「先无预算跑热 P3 循环、再开预算」的顺序下,新预算要等约十亿次
  back-edge(回边)之后才会被查询。定向测试复现:warm 两轮
  (promotions=1,safepoints=1)后 `SetStepBudget(10)`,100 万次段内 while
  循环照常跑完、err=nil、safepoints-delta=0。
- 修法(commit `2945b57`):在 `enterGibbous` 镜像 P4
  `RefreshJitCtxAddrs` 的 armed-state transition 处理——武装方向把
  refill 与 fuel 字重置为 `loopFuelQuantum`,且不把武装前的消耗计入新预算
  (那段消耗发生在无预算期间);解除方向恢复 `loopFuelUnlimited` 使快路径
  回来。回归测试覆盖四个方向:arm 后必须报错 / cancel-hook 后必须中断 /
  disarm 恢复快路径(Safepoint 次数接近零)/ arm 不把 pre-arm 消耗误计入
  新预算。其中 arm 与 cancel 两个测试在无修复的代码上确实失败
  (prove-the-path)。

### 问题 2(GOMEMLIMIT 机制性描述错误)

- 期望:文档里对 runtime 机制的声称与官方语义一致。
- 实际:`go-fuzz.sh` 注释声称 GOMEMLIMIT 会在 live heap 压不下去时触发带
  goroutine 栈的 Go OOM fatal。核对 `runtime/debug.SetMemoryLimit` 官方
  文档:纯软限制,「the application may still make progress」,运行时任何
  情况下不会因它主动 fatal。这与 #156/#157/#159 带着限制仍静默死亡的观测
  一致——之前把「没接住」解读成「硬化还差一层」,其实是这一层从来就没有
  「转换成 in-process fatal」的能力。
- 修法(commit `bc29cd5` / `40979a1`):两处改写为「只压 RSS 峰值、提早
  GC,不保证把 OS kill 转换成带栈 fatal,硬上限需要进程级隔离」,并指向
  下一层诊断方向(harness 按 seed 记 wall-clock)。

## 踩坑与教训

### 教训 A:复现「某机制没生效」的指控时,复现测试自己也要 prove-the-path

复现问题 1 时,第一版测试用了 `local function spin()` + FORLOOP 循环 +
`SetForceAllPromote`,结果 budget 居然生效了(报错)——差点据此把真问题
误判成「不存在」。原因是那个循环的写法没有在段内跑 back-edge,预算走的是
解释器路径,测的根本不是被指控的那条路径。换成既有
`test/regression/loop_stepbudget_p3_test.go` 已证明「back-edge 在段内跑」的写法
(`function sum(n) ... while ... X=0*i ... end` + `SetHotThresholds(2,4)` +
顶层 `sum(12)%sum(N)`)后才稳定复现。

**判据**:验证「某机制没生效」的指控时,必须先用白盒探针
(`SafepointCalls` / `PromotionCount`)确认测试用的执行路径就是被指控的
那条路径,否则「没复现」不等于「问题不存在」。这是
[[prove-the-path-under-test]] 的验证侧应用:该 guide 既有实例都在「修复要
证明路径」和「诊断归因要证明路径」两侧,本条补上「**复现也要证明路径**」
——一次走错路径的复现失败,产出的是伪证据,会直接把一个真 bug 关掉。

### 教训 B:host-API 调用时序类 bug,定向回归测试是正确载体,不必硬造 fuzz target

问题 1 属于 host-API 调用顺序类 bug(同一 State 上 arm-after-warm),不是
脚本内容类——现有 fuzz harness 都在每次 Run 前就把预算设好,任何语料都
到不了这个状态转换。commit message 里也显式写明了这一点。对这类 bug,
定向回归测试(四个方向各一)是正确载体。

**判据**:bug 的触发条件在 host API 的调用时序里、不在被解释的脚本里,
就写定向回归测试;fuzz 的探索空间是脚本内容(加上
[[prove-the-path-under-test]] §5 列的接受面/硬件/参数维度),调用时序不在
其中,硬造 fuzz target 是无效投资。

### 教训 C:机制性声称的传播链——写下时就要附佐证,引用时花一分钟核对原文

GOMEMLIMIT 的错误认知从 #123 轮(2026-07-11 反思)开始,先进了
`go-fuzz.sh` 脚本注释,又进了 [[unreproducible-crasher-triage]] guide,
昨天(2026-07-18 巡检轮)还被原样引用进飞书报告和新反思。传播了三周、
跨了四个载体(反思 → 脚本注释 → guide → 报告),直到外部 review 逐字
核对官方文档才纠正。期间它还参与了误导:#156/#157/#159 的「软限制没接住」
被解读成「硬化还差一层」,而正确解读是「这一层从来就没有这个能力」。

**判据**:声称「某 runtime 机制会做 X」的注释/文档,写下时就应该贴官方
文档原文或可复现的实验佐证;引用既有文档里的机制性声称时,若它是推理链
的关键环节(比如据此设计下一层硬化),值得花一分钟 `go doc` 核对原文。
文档库的自引用会让错误认知越滚越可信——载体越多、越像共识。

## 流程

- 外部 review 的两条指控,都是**先写定向验证(不修复)、向用户确认属实后
  才动手修**;用户随后用 /goal 下达修复指令。验证与修复分离让「指控是否
  成立」这个判断有独立证据支撑,不与修复方案捆绑。
- 修复严格按「一个逻辑修复一个 commit」拆了三个 commit(P3 fix /
  go-fuzz.sh 注释 / llmdoc guide 订正)。
- commit-msg hook 拦了一次 subject 里的 em-dash(English-only ASCII
  政策),用 `-` 替换后通过。

## Promotion 候选

- **教训 A**(复现「机制没生效」的指控时,复现测试自己也要
  prove-the-path):建议由 recorder 并入 [[prove-the-path-under-test]],
  作为验证侧/复现侧的新维度——该 guide 已覆盖测试侧、诊断侧(§7)、
  度量侧(§8),本条是「复现侧」:走错路径的复现失败会把真 bug 误判为
  不存在。落点与措辞由 recorder 决定。
- **教训 B**(host-API 调用时序类 bug 用定向回归测试,不硬造 fuzz
  target):首次样本暂留 memory。
- **教训 C**(机制性声称写下时附佐证、引用时核对原文):首次样本暂留
  memory;若后续再出现「文档间互相引用放大错误认知」的实例,可考虑升
  guide 或并入文档写作纪律。

## 触发场景

- 收到「某机制没生效」的指控(外部 review / issue / 用户报告)要写复现
  测试时(教训 A:先用白盒探针确认复现测试走的就是被指控的路径,「没复现」
  不等于「问题不存在」);
- 判断一个 bug 该配定向回归测试还是 fuzz target 时(教训 B:触发条件在
  host API 调用时序里就写定向测试,fuzz 语料到不了那个状态转换);
- 在注释/文档里写「某 runtime/工具链机制会做 X」时,或把既有文档里的
  机制性声称用作推理链关键环节时(教训 C:贴官方原文或实验佐证;引用前
  `go doc` 核对一分钟);
- 给 State 级开关(budget / cancel-hook / tier 开关类)加运行期切换语义
  时(问题 1 的机制模式:凡是「加速层把某个 Go 侧配置镜像成段内/线性内存
  快照」的地方,配置变更点都必须有对应的镜像刷新,P4 RefreshJitCtxAddrs
  与本轮 enterGibbous 是两个既有先例);
- 给「多个独立配置项汇成一个观察量」的机制写配置变更检测时(附记教训 D:
  用代际计数,不要用从配置推导的聚合布尔——布尔看不到 armed 状态内部的
  变更);
- 给触发条件对某个周期性窗口的相位敏感的 bug 写回归测试时(附记教训 E:
  在整个窗口宽度上扫描,单点 warm 长度会静默错过坏相位);
- 给「丢弃 vs 结算」类判定设计检测信号时(附记二教训 F:信号的编码
  范围必须与被丢弃之物的所有权精确对齐——过窄漏检真变更,过宽把
  非所有权变化也当变更,两个方向都出漏洞);
- 修复配对状态机 bug 补回归测试时(附记二教训 G:状态转换矩阵的两个
  方向都要测,只测触发本次 bug 的那条边会让反方向的矫枉过正无人发觉)。

## 关联

[[prove-the-path-under-test]](教训 A 的落点候选:复现侧新维度)·
[[unreproducible-crasher-triage]](问题 2 订正对象;GOMEMLIMIT 层级描述
已改写)· [[2026-07-14-p3-loop-stepbudget-round]](P3 loopBudget 机制的
上一轮:本轮修的是它留下的 armed-state transition 缺口)·
[[2026-07-15-issue143-pj3-loopfuel-round]] /
[[2026-07-10-issue102-loop-fuel-round]](P4 loopFuel 家族前序,armed-state
transition 处理的镜像来源)· [[2026-07-18-issue155-158-nightly-crasher-round]]
(教训 4 曾原样引用 GOMEMLIMIT 错误声称,本轮订正)·
[[2026-07-11-issue123-unreproducible-crasher-round]](错误认知源头轮)·
PR #161 · commit 2945b57 · commit bc29cd5 · commit 40979a1 ·
commit 961f665 / 000e7fb / a171457(附记:第二轮增量 review 修复)·
commit 16b88d6(附记二:第三轮增量 review 修复)

## 附记(合入前第二轮增量 review:第一轮修复自身的盲区与假绿测试)

llmdoc 进 PR 后,第二轮增量 review
(`.code-review/from-6a55d1e/increment-3-to-40979a1.md`)在第一轮修复
自身找到两个缺陷,均已修复(commits `961f665` / `000e7fb` / `a171457`)。

### 缺陷 1:聚合布尔检测不到「已武装状态内部的配置变更」

第一轮修复用 `stepBudget > 0 || ctx != nil` 这个聚合布尔检测武装状态
转换,它只能看到聚合值的 false↔true 边沿。「ctx 已武装、随后再开
budget」这条时序两头都是 armed=true,转换检测对这次变更失明:ctx-only
阶段的 partial drain(最多一个完整 quantum)被下一个计费点算进全新
预算。定向复现在两个 tier 都成立:P3 上武装后 1 次新 back-edge 就触发
budget=10 报错;P4 探针确认同样存在(4096 宽度窗口的陈旧排空后,
~2500 次新 back-edge 触发 budget=3000)。

修复(commit `961f665`):`State.budgetGen` 代际计数——`SetStepBudget`
/ `SetCancelHook` 各自 bump;P3 `enterGibbous` 与 P4
`RefreshJitCtxAddrs`(经 `JITContext.SyncBudgetGen`)比对各自缓存的
代际,任何变更都先丢弃旧窗口的 partial drain 且不计费(那段消耗发生在
旧配置下,绝不计入后武装的预算),再按新武装状态开新窗口;P4 的
seg-call fuel 在同一变更点跳过它的 `Spent()` 计费,理由相同。

**教训 D**:「配置发生过变更」的检测要用代际/世代计数,不要用从配置
推导出来的聚合布尔——布尔只能编码「当前状态是什么」,编码不了「中途
发生过变化」。凡是多个独立配置项(此处 budget 与 ctx)汇成一个观察量
的地方,都有这个盲区。

### 缺陷 2:第一轮的 cancel 回归测试是假绿

第一轮写的 cancel 回归测试,warm 用了一个 Program、被测 Run 用另一个
独立编译的 Program;`PromotionCount()` 是 State 级累计值,harness 的
「已升层」检查因此空过。被测 Run 在解释器 frame-entry preempt 处就
返回 context canceled,从没进过 P3(`SafepointCalls` delta = 0)——
把整个修复删掉,这个测试照样通过。这恰好违反了本反思正文教训 A 与
guide §7.1 里自己刚写下的规则:写下教训与执行教训之间仍有距离,同一
轮里前脚写进 guide 的判据,后脚就没套用到自己新写的测试上。

重写后的测试:同一个 Program,循环长度经全局 knob 切换;mid-run 从
另一 goroutine 触发 cancel;断言 `SafepointCalls` delta > 0(段内路径
确实被走到)+ 及时性 < 5s(陈旧 unlimited 窗口实测要 ~9s 才排空,5s
能把「及时中断」与「排空后才中断」干净分开)。三个新/改测试都分别对
pre-fix 版本与聚合布尔版本验证过失败。

### 教训 E:相位敏感的 bug 用全窗口扫描的回归测试

stale billing 是否触发,取决于 warm 阶段停在 64(P3)/ 4096(P4)宽度
fuel 窗口的哪个相位,单一 warm 长度可能静默错过坏相位(实测 10 个相位
采样里只有 2 个触发)。测试改为在整个窗口宽度上扫描 warm 长度,任何
相位分布下至少有一个采样点必然暴露问题。**判据**:触发条件对某个
周期性窗口的相位敏感时,回归测试要扫过整个窗口宽度,不要赌单点。

### 小项与流程

- `go-fuzz.sh` GOMEMLIMIT 注释后半段的确定性口吻(bound / never slow
  down)改为概率性表述(commit `000e7fb`)——延续正文问题 2 的同一条
  纪律:机制性声称不许超出官方语义的担保范围;
- `state.go` 注释里 `BudgetGen`→`SyncBudgetGen` 的方法名笔误由 bot
  增量审查抓到,已订正(commit `a171457`);
- gofmt hook 在 commit 时拦截了一个未格式化的测试文件(防线正常工作);
- 一次 `git stash pop` 弹出了历史遗留的旧 stash 造成冲突:工作树里有
  历史 stash 条目时,`git stash push` 指定文件之后要留意 pop 出来的是
  哪一条,或改用临时文件复制来做 A/B 验证(本轮后来改用 /tmp 文件
  复制法做删修复验证)。

### 附记的 promotion 候选

- **教训 D**(配置变更检测用代际计数而非聚合布尔):首次样本暂留
  memory;它与正文「加速层把 Go 侧配置镜像成段内快照,变更点必须刷新
  镜像」是同一机制的两层——正文管「有没有刷新点」,本条管「刷新点的
  变更检测本身怎么写才不漏」。若后续再出现聚合观察量吞掉配置变更的
  实例,可与正文条目一起升 guide。
- **教训 E**(相位敏感用全窗口扫描):首次样本暂留 memory,可作
  [[prove-the-path-under-test]] 的测试设计侧候补维度。
- 缺陷 2 作为「guide 刚写完就被自己违反」的实例,值得在
  [[prove-the-path-under-test]] §7.1 下次修订时反引:新写的复现/回归
  测试本身也要过一遍 §7.1 的白盒探针检查,包括修复轮自己写的那些。

## 附记二(合入前第三轮增量 review:increment-3 的代际方案矫枉过正)

第三轮增量 review
(`.code-review/from-6a55d1e/increment-4-to-4348a44.md`)在 increment-3
修复自身找到一个真实的配额绕过漏洞,已修复(commit `16b88d6`)。

### 缺陷:代际编码范围过宽,ctx 变化被误当成预算所有权变更

increment-3 把 `SetStepBudget` 和 `SetCancelHook` 都接进同一个
`budgetGen`,消费方把任何代际变化都解释为「预算所有权变更」,并无计费
丢弃旧 fuel 窗口。但「budget 持续武装、只是 ctx 被设置/替换/移除」时,
drain 前后属于同一个活跃预算,`SetContext` 也不会重置 `stepUsed`——
每次 toggle 却白送一个未计费的 quantum 窗口。定向复现:budget=100 下
交替 `SetContext`/`RemoveContext` 十轮、每轮约 40 次 back-edge,共约
400 次 back-edge 从不报错(P3 上 `Safepoint` delta=0——每个新 64
quantum 都大于单轮的 40,计费点永远不被穿越);修复后第 3 轮就正确报
instruction budget exceeded。

### 修复(commit `16b88d6`)

- `budgetGen` 收窄为只由 `SetStepBudget` bump——语义就是「预算所有权
  变更」,与它同时 reset `stepUsed` 对齐;
- ctx 变化只影响 fuel 窗口大小,消费方从 live armed 状态推导:P3
  `enterGibbous` 在「无 budget」时按 quantum↔unlimited 差异调整窗口
  (zero-value 初态留给 `Safepoint` 首次穿越去初始化,避免抹掉
  `SafepointCalls` 探针信号);P4 `RefreshJitCtxAddrs` 恢复
  `!LoopFuelArmedBudgeted()` 的 arming-transition 分支,处理「无
  budget 时 ctx 装上」(Unlimited 模式 `Spent` 返回 0,不会误计费);
- budget 武装期间 ctx toggle 时,三个分支全都不触发——窗口保留、
  drain 继续累积进同一预算。

### 教训 F(本轮核心,教训 D 的修正):代际的编码范围必须与「谁拥有被丢弃的东西」精确对齐

「用代际计数检测配置变更」的方向没错,但**代际编码什么必须精确**。
fuel drain 的所有者是 step budget,不是「budget+ctx 的联合配置」——
把 ctx 变化也编进同一代际,等于把「所有者没变」的场景也当成「所有者
换了」处理,丢掉了本该结算的消耗。一般化:决定「丢弃 vs 结算」的检测
信号,粒度必须恰好是所有权本身;过窄(聚合布尔,increment-3 抓到的
教训 D)漏检真变更,过宽(联合代际,本轮)把非所有权变化也当成变更,
两个方向都出漏洞。这是一对对偶失误:同一个机制连续两轮分别踩了两头。

### 教训 G(测试盲区的对偶):状态转换矩阵要双向都测

increment-3 补的测试只覆盖了「ctx 先武装、budget 后加」这一个方向;
「budget 先武装、ctx 后变化」这个反方向没有测试,于是矫枉过正无人
发觉。**判据**:修复配对状态机 bug 时,回归测试要把状态转换矩阵的
两个方向都列出来测,不能只测触发本次 bug 的那一条边。

### 新回归测试与验证

- 双层新增 `TestP3LoopBudget_CtxToggleDoesNotExtendBudget` /
  `TestP4LoopBudget_CtxToggleDoesNotExtendBudget`:10 轮 toggle,断言
  必须 raise + P3 校验 `Safepoint` 真穿越 + rounds<=5 防部分延长;对
  increment-3 版本的代码验证过失败(P3 零穿越、P4 十轮跑满)。
- 全套 budget 测试(9 P3 + 7 P4)+ 三 build 全量 + race + arm64
  交叉编译 + difftest + 60s FuzzAutoPromote 全绿。
- bot 增量审查 APPROVE,静态核对确认 P3/P4 镜像一致、值域守卫穷尽;
  它建议合入前本地跑 5 个 budget 测试——提交前已跑过(含 `-race`)。

### 附记二的 promotion 候选

- **教训 F**(检测信号粒度与所有权精确对齐)与附记教训 D 构成同一
  机制上的一对对偶失误——「同一个检测信号两轮分别踩过窄与过宽两头」
  已是完整模式;若后续再出现「丢弃 vs 结算」判定信号设计失误的实例,
  建议 D 与 F 一起升 guide。
- **教训 G**(状态转换矩阵双向测试)首次样本暂留 memory,可与附记
  教训 E 相邻,同作 [[prove-the-path-under-test]] 测试设计侧的候补
  维度。
