---
name: 2026-07-10-issue102-loop-fuel-round
description: >
  issue #102 修复轮(2026-07-10,PR #105,分支 fix/issue102-forloop-fuel):P4 native FORLOOP 回边不计
  step budget,一个 277M 次迭代循环 + SetStepBudget(1<<20) 在 P4 force-all 上 9s 跑完,解释器 40ms 就报
  "instruction budget exceeded"。根因:st.preempt() 只在解释器 call/回边点执行,内联到段内的循环体(算术或
  #77 math intrinsic)一次也回不到 Go 侧计费点。修法镜像 #89 segCallFuel 模式,新增 jitCtx.loopFuel 计数器
  + HelperLoopFuel exit-reason + host.LoopPreempt 计费/重灌/触发 st.preempt() 等价检查。核心教训:第一版
  复用 segCallFuel 表面像行(build 绿、测试路径存在),但白盒 probe 揭穿其实一次未触发——每次 Run resume
  重灌抹平了段内递减;更深的是这条路径没有检查点,billing 与 checking 是两件独立的事,没检查点的计费点仍是
  漏洞。review bot 抓出遗漏的 seg2seg deopt stranding bug——RefreshJitCtxAddrs 需按 armed && loopFuel==0
  的滞留特征识别并重灌但不计费(重放会经 baseline st.preempt() 重计,重复计费会双记)。
metadata:
  type: reflection
  date: 2026-07-10
---

# issue #102 loop fuel 修复轮反思(2026-07-10,PR #105)

> 范围:分支 `fix/issue102-forloop-fuel`。P4 native FORLOOP 回边(以及负向 sBx JMP 组成的回边)绕过 step
> budget 计费点,把一个耗尽预算的循环从「40ms 报错」拖成「9s 跑完」。修法新增 loopFuel 计数器 + LoopPreempt
> host 通道,双架构一致。

## 任务

给 P4 native FORLOOP 段内回边接上 step budget 计费与检查,让 `SetStepBudget(1<<20)` 下的死循环/深循环能像
解释器一样及时报 "instruction budget exceeded",而不是把整段循环体在段内跑到硬件极限。触发形式:循环体完全
inline(纯算术或 issue #77 引入的 math intrinsic)后,段内不再有任何 CALL 回到 Go 端,`st.preempt()` 一次
也执行不到。

## 期望与实际

- 期望:一次到位——按 #89 `segCallFuel` 现成模板加一个循环计数器,双架构 emit inline dec+jnz,溢出走
  exit-reason,host 计费 + 重灌,收工。
- 实际:第一版把 dec+jnz 挂到 `segCallFuel` 上就通过了 build 与既有测试,但**新写的复现测试挂了 15.8s 无
  raise**——白盒 probe(`LoopFuelExitCount` / `DispatchHelperCount`)一看:7.7M 次 dispatcher 往返,零次
  预算触发。根因两条叠加:(a) dispatcher 每次 Run resume 都重灌 `segCallFuel`,而这个循环体每次迭代经冷
  CALL 走 exit-reason 出段一次,重灌把段内递减全抹平;(b) 更根本的是段内派发路径从头到尾没有一个「检查
  stepUsed 是否超预算」的点——host CALL 分支只 bill 不 check,而 st.preempt() 只在 baseline 解释器的 call
  和回边点跑。**「计费点」和「检查点」是两件独立的事,只做前者留下的仍是漏洞**。

## 修复要点

- **数据结构**:`jitCtx.loopFuel` 独立计数器(不复用 `segCallFuel`);`RefreshJitCtxAddrs` 在从解释器进段的
  「armed 转变」时刻负责初始化,LoopPreempt 是唯一的重灌来源。
- **emit 侧**:amd64 FORLOOP condTrue 分支内联 `sub dword [r15+loopFuelOff], 1; jnz continue;` +
  exit-reason `HelperLoopFuel` 出段;arm64 一样的语义走 `ldr-sub-str-cbnz`;负 sBx JMP(loop-forming back
  edge,如 while/repeat 生成的下跳汇编)一样的 dec+分支模板。
- **host 侧**:`host.LoopPreempt` 计入已消耗的燃料到 `stepUsed`,跑等价于 `st.preempt()` 的预算检查(该抛
  就抛 "instruction budget exceeded"),抛不掉则重灌并让段继续。
- **PerOpCode 回放**:`runForLoop` / `runTForLoop` 通过新增的 `LoopFuelTick` 递减同一计数器,回放路径与
  native emit 路径同源计费。

## 核心教训

### 教训 1(计费与检查是两件事,复用错的通道会造出「有计费点但无检查点」的洞)

第一版把 loop back edge dec 挂到 `segCallFuel` 上,build 绿、既有测试全绿、277M 迭代跑到底也没崩——一切
看着都对。白盒 probe 才揭穿:该循环体每次迭代经一次冷 CALL 走 exit-reason 出段(dispatcher 往返 7.7M 次),
dispatcher 的 resume 路径**无条件重灌** `segCallFuel`,段内的 dec 每次都被抹回满值。更微妙的问题即使解决
了重灌(比如把重灌挪到入口而非每次 resume),仍然不够:段内派发路径本就没有一个「读 stepUsed 与 budget 比
较」的地方,只有 `st.preempt()`(baseline 解释器专属)才做这个比较——**没有检查点的计费点等于没记账**。

由此得两条独立的设计约束:

1. **loopFuel 必须是独立计数器**,重灌来源只有 LoopPreempt 与「armed 状态转变」——因为 loop 段内没有另一
   种自然回 host 的路径,一旦被别的路径的重灌规则牵连就等于没计费。
2. **检查必须发生在 LoopPreempt 内部**,不能延迟到后面某个可能永远走不到的抢占点。

**判据**:给绕过 host 的段内快路径接抢占/计费时,不只要问「这条路径上有计费吗」,还要问「这条路径上或它
必然汇入的下游点上有检查吗」;两者缺一则是漏洞。写下这一版的 fuel counter 时,先列一张三列表:**谁 bill /
谁 check / drain 到 0 时能不能出段**。这是 [[design-claims-vs-codebase-physics]] 的做法在设计时刻落到 fuel
counter 家族上——白盒探针(LoopFuelExitCount / DispatchHelperCount)在会话中几分钟就能把 build 绿隐藏的
问题看穿,是 [[prove-the-path-under-test]] 的直接应用。

### 教训 2(review bot 抓到测试漏掉的 seg2seg deopt 滞留 bug)

Review bot 指出一个我没设想到的路径:seg2seg 被调方在 `segCallDepth > 0` 状态跑完循环把 loopFuel 消耗到 0
后,若接着走 deopt(设 flag + ret 回主段)而非 exit-reason 出段,LoopPreempt 就永远不会运行,计数器**在段
边界外滞留在 0**;主段之后的循环 sub+jnz 从 0 减 1 就 wrap 到 2^32,再来 40 亿次段内迭代零计费——issue
#102 的窗口原样重开。

修复分两半:

- **RefreshJitCtxAddrs 加特征识别**:`armed && loopFuel == 0` 是一个不可能状态(合法 drain 一定先经过
  LoopPreempt 重灌再 resume,而 LoopPreempt 只重灌不置零),因此这个组合出现只能是 deopt 遗留的滞留状态。
  识别到就重灌,但**不计费**——deopt 会让 baseline 解释器重跑滞留的那批迭代,而 baseline 的 st.preempt() 会
  正常计费;这里再计费一次就是双记。
- **`LoopFuelTick` 在 0 处饱和**:PerOpCode 回放路径同样不能 wrap 到 2^32。

回归测试 `TestI102_DeoptStrandRepaired` 需要循环体至少 3 条指令——1 条 op 的 body 会走 PJ3 spec template 而
不是 seg2seg,`SegToSegDeoptCount` 保持 0 让测试悄悄不测想测的路径;prove-the-path 的 delta 断言当即揪出了
这点。反向 sanity:把 refresh 里的修复回退,测试挂;把 LoopFuelTick 的饱和逻辑回退,测试挂。两个负面对照都
成立,修复才可信。

### 教训 3(fuel counter 家族的设计规则)

到本轮为止 fuel counter 已有两个成员:`segCallFuel`(#89)重灌自每次 Run resume,host CALL 路径 bill 且
check(host 的 enterLuaFrame 会走 st.preempt);`loopFuel`(本轮)只由 LoopPreempt 与 armed 状态转变重灌,
LoopPreempt 自己 bill 与 check。两者的重灌规则**必须**不同——它们段内 drain 完时能出段的路径不一样:CALL
派发本就要出段,循环回边只有溢出 exit-reason 一条路。

**规则**:新增任何 fuel counter 时,先把三个问题写下来再动 emit:

1. 谁 bill(在哪个 host 边界把消耗记进 stepUsed)?
2. 谁 check(在哪里比对 budget 抛错)?
3. drain 归零时段内有没有一条路径不能出段(比如深循环体全 inline)?若有,check 必须发生在重灌那个 host
   helper 内部,不能延迟。

后续如果给 TFORLOOP native 或段内字符串操作加一个类似 counter,直接套这张表。

### 教训 4(perf 纪律)

A/B 对比 master(共享机器,先 `uptime` 看 load,`-benchtime=2s -count=3+ -cpu=1` 严格串行,在两个 worktree
之间交替跑控机器漂移):HeavyArith 15.0→15.4ms(+2.9%),HeavyFloatloop 26.0→26.5ms(+2.0%),n-body / fib
基本不动。这 +2~3% 是每次回边多一条 sub 在 jitCtx 槽上的必付成本,issue 分析里也说过不能编译期 gate
掉(promotion 早于 SetStepBudget)。中间尝试过一版把 dec 放共享块用 jmp 跳过去,多一条静态 jmp 每次迭代
反而更慢;把 dec+jnz 直接内联到 FORLOOP 的 condTrue 分支就消掉了这一条。数字在两个 worktree 交叉复测。

### 教训 5(process 观察)

- 用户中途要求 rebase 到 origin/master(PR #103 IEEE compare 修复已合入),两个 commit step 之间做了一次
  干净 rebase。
- GitHub Actions 的 lint job 挂了一次 504(golangci-lint-action HTTP 504),与本次修改无关;本地
  `golangci-lint run ./...` 0 issues。infra 抖动识别特征:conclusion 是 failure 但错误消息含 "HTTP 504
  Gateway Timeout" / golangci action 上游拉不下来,处置就是 rerun。

## Promotion 判断

- **教训 1(计费点 vs 检查点是两件事,fuel counter 设计三问表)** → **强建议升入
  [[backend-capability-vs-profitability]] 或新开一节**:与该 guide 「host 通道固定成本假设不适用段内通道」
  是同一二分轴(段内 vs host 的隐式保障差异)的另一具体面。首个明确样本但推论清晰,fuel counter 家族已两
  个成员,规则可提前定死避免第三个再撞。若下次触碰 P4 native emit 相关 guide 修订时,把三问表落进去。
- **教训 2(review bot 抓到 deopt stranding + 测试语料结构盲区)** → **memory 反引**:是 [[prove-the-path-
  under-test]] 家族的又一具体形式(测试语料的循环体大小决定了它是否真触发 seg2seg 路径),但轴较窄,首次
  样本暂留观察。
- **教训 3(fuel counter 家族三问表)** → **候选并入教训 1 的 guide 章节**,不单独立项。两成员 + 明确规则
  已足,但作为教训 1 的操作面推论更自然。
- **教训 4(perf 纪律)** → **不升,已入 [[perf-optimization-workflow]] §5 跨机器基线对照与 §7 profile 才
  是合同的现有实践,本轮是常规应用而非新维度**。
- **教训 5(process 观察)** → **不升,memory 留档**。

## Follow-up

- **PR #105 CI**:lint 504 后 rerun;若稳定则合入。
- **guide 修订**:下次触碰 P4 native emit / bridge 相关 guide 时,把教训 1 的「bill / check / drain 归零可
  否出段」三问表加进 [[backend-capability-vs-profitability]] 或新开一小节,并在 fuel counter 家族的实例列
  表补 segCallFuel(#89) + loopFuel(#102)两条。
- **潜在下一步**:若将来给 TFORLOOP native 或段内字符串操作加 fuel counter,套三问表评估,并在 memory 里把
  第三个实例挂上来判断是否达升 guide 阈值。

## 关联

[[2026-07-08-pr95-spill-stack-fuel-round]](fuel counter 家族第 1 实例 segCallFuel,#89) ·
[[2026-07-08-pr86-deep-recursion-nosplit-round]](#85→#89 深递归链,fuel counter 出现的上下文) ·
[[2026-07-08-issue77-math-intrinsics-round]](issue #77 math intrinsic 内联消掉最后一次跨 Go 机会,直接
促成 #102 的窗口密封) · [[prove-the-path-under-test]](白盒探针 LoopFuelExitCount / DispatchHelperCount
揭穿 build 绿的假象) · [[backend-capability-vs-profitability]](段内 vs host 通道的隐式保障差异,fuel
counter 三问表候选归宿) · issue #102 · PR #105 · `internal/gibbous/jit/peroptranslator/emit_ops_amd64.go`
(FORLOOP condTrue 内联 dec+jnz) · `internal/gibbous/jit/peroptranslator/translator_native_dispatch.go`
(RefreshJitCtxAddrs 滞留识别 + 重灌不计费)
