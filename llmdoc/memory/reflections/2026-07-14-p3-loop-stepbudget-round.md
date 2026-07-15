---
name: 2026-07-14-p3-loop-stepbudget-round
description: >
  2026-07-14 定时巡检补跑轮的第 4 个 crasher(归入 PR #140)。PR #140 自己的
  CI p3 fuzz-smoke 撞出 FuzzAutoPromote/bb525447c652d8d9(`for i=0,5/0 do X=0*i
  end` 无限循环),本地重放确认是真实 bug 且在 master 上就存在:P3 wasm 升层后的
  循环回边不计 step budget,全 inline 的死循环在解释器立即抛 "instruction budget
  exceeded",升 P3 却永远挂住。根因:P3 回边只 inline 检 gcPending(纯 GC 用),
  step budget 无 async producer;host.Safepoint 只做 GC、完全不计费。这是 issue
  #102(P4 native loop fuel)的 P3 对偶。修法照抄 P4 loopFuel:加 loopBudget
  linear-memory fuel 字,回边 inline 自减 + 归零(或 gcPending 置位)才跨层
  Safepoint 计费/重填/超额 raise;无 budget 时重填大额(loopFuelUnlimited)使稳态
  循环几乎不跨层(实测 1e6 迭代 0 次跨层)。顺带发现原判为「不可复现资源耗尽」的
  #135(`sum(555555520)` 有限但巨大)其实是同一 bug 的温和实例(有限 vs 无限),
  本修一并真修好。核心可复用教训:① 同一物理事实(段内无抢占点 → 计费缺口)在
  P4 native 修过后必须横向查 P3 wasm(跨 tier 对称修复义务);② 判「不可复现」前
  要跑到真正暴露 bug 的 tier/路径(#135 单 Run 解释器干净,auto/P3 才挂),别只
  在最容易的路径上重放就下不可复现结论。
metadata:
  type: reflection
  date: 2026-07-14
---

# P3 gibbous 循环回边 step-budget 缺口(2026-07-14,PR #140 第 4 个 crasher)

> 范围:PR #140(2026-07-14 巡检补跑轮)。前三个 crasher(#137 前端常量顺序、
> #136 P4 call-void 误接受、#135 原判不可复现)已在同 PR 修复;本轮 PR 自己的
> CI p3 fuzz-smoke 又撞出第 4 个 `FuzzAutoPromote/bb525447c652d8d9`,一并修入。

## 任务

PR #140 的 CI(p3 / ubuntu-latest fuzz-smoke)撞出新 crasher:

```lua
function sum(n)local s=0 for i=0,n do X=0*i end return s end return sum(12)%sum(5/0)
```

`sum(5/0)` = `sum(inf)` → `for i=0,inf do X=0*i end` 无限循环。本地重放确认:
**在干净 master 上就存在**(worktree 验证),不是本 PR 引入。

## 根因

解释器的循环回边走 `st.preempt()`,计 step budget 并在超额时抛 `instruction
budget exceeded`(实测 0.03s 触发)。P3 wasm 升层后的回边**不计 budget**:

- P3 回边只 inline 检 `gcPending` 标志(collector 在 GC due 时置位),非 0 才跨层
  调 `h_safepoint`——这是**纯 GC 用途**的优化(热循环无 GC due 时零跨层);
- `host.Safepoint` 只做 `st.gc.MaybeCollect()`,**完全不碰 step budget**;
- step budget 没有 async producer(不像抢占 flag 有外部写者),全 inline 的循环体
  (纯算术、无 Lua 帧)在段内没有任何别的抢占点。

⟹ 全 inline 的死循环升 P3 后永远挂住。这是 **issue #102**(P4 native 段内循环
fuel 缺口)的 **P3 对偶**:那轮修的是 P4 native 的 `loopFuel`,这轮是 P3 wasm 的
回边。

## 修法(照抄 P4 loopFuel,保住速度)

用户明确关心「每次迭代都跨层计费岂不是很慢」——答案是**不能**每迭代跨层(~143ns
会吃光 P3 消灭 dispatch 的收益)。P4 的 `loopFuel` 正是解法:

- 加一个 per-State `loopBudget` linear-memory fuel 字(与 `gcPendingRef` 一同分配);
- 回边 inline **自减** `loopBudget` + 检查 `loopBudget<=0 || gcPending`——纯段内
  几条指令(load/sub/store + 比较),几乎零成本;
- **只有** fuel 归零(或 GC due)才跨层 `h_safepoint`。`Safepoint` 把消耗的 quantum
  计入 `stepBudget`、重填、超额时 `raiseGibbousAtPC`(返回 status 1,段 `return 1`
  冒泡);
- **无 budget/ctx armed 时重填大额** `loopFuelUnlimited`(1<<30)⟹ 稳态循环几乎
  永不跨层(镜像 P4 `SegCallFuelUnlimited`);armed(fuzz / 脚本配额)时重填小额
  `loopFuelQuantum`(64),每 64 次迭代跨层一次,~143ns 摊到 64 次 ≈ 每迭代 2ns。

`Safepoint` 签名从 `(base,pc)->()` 改为 `->i32`(wasm type 3 加返回值),`h_safepoint`
调用点检返回值 == 1 则 `return 1`。

## review 抓出的漏洞:第一版只覆盖 FORLOOP,漏了 while/repeat 的 JMP 回边

第一版把 `emitBackEdgeSafepoint` **只挂在 `emitForLoopTerm`(数值 FORLOOP)**——review
bot(REQUEST_CHANGES)指出 while/repeat 循环的回边是**负位移 JMP**,走 `emitJmpTerm`
→ `emitEdge`,对回边只发**裸 `br`、没有任何 safepoint**。P3 里 step budget 只在回边
safepoint 计(host helper 不计),所以 while/repeat 的全 inline 死循环升 P3 后仍会挂
——正是要修的同一类 bug,只是换了个循环 opcode。P4(#102)的实现覆盖了 JMP 回边,
第一版 P3「自称是 #102 的 P3 对偶却漏了这一半」。

**反事实验证 bot 判断为真**:把回边 safepoint 换回 gc-only(第一版对 while 的等价
行为),crasher 结构的无限 while(`while i<=n do X=0*i i=i+1 end`,n=inf,顶层调
`sum` 两次 auto 升层)**挂 5s**;装回 budget-aware safepoint 后**立即 raise**。

**修法:把 safepoint 挪到唯一的 back-edge choke point**——`emitEdge` 的 `scLoop`
分支。所有循环形态(FORLOOP / while / repeat / 比较驱动的 JMP 回边)都经此发 `br`
回 loop 头,挂在这里一次覆盖全部,并删掉 `emitForLoopTerm` 里的单独调用(避免
FORLOOP 双发)。`emitEdge` 加 `cfg` 参数取 back-edge 指令 pc(源 BB 末指令)锚错误行。
prove-the-path:3M 迭代的 budgeted while 跨 Safepoint 46876 次(≈3M/64,证 JMP 回边
真计费),1e6 无 budget 循环跨 0 次(快路径不变)。

## 验证

- crasher `bb525447c652d8d9` 重放从「挂 70s」变「0.24s 干净」;
- **byte-equal**:P3 升层的死循环 raise 的错误消息 + stack traceback 与解释器逐字节
  一致(有限完成 / budget 超额两种都比对过);
- **prove-the-path 性能证据**:1e6 迭代的无 budget 热循环跨层 `Safepoint`
  **0 次**(unlimited 重填使快路径完好,不是每迭代跨层)——白盒 `SafepointCalls()`
  计数器断言 crossings ≤ 1000(bug 会是 ~1e6);
- default / p3 / p4 / difftest 全绿,lint 0 issue,p3 GC-stress(V5/V13)不变,
  p3 fuzz-smoke 90s(179 万 execs)干净。

## 教训

### 教训 1:同一物理事实的计费缺口,P4 修过后必须横向查 P3

「段内无抢占点 → step budget 计费缺口」是一个物理事实,在 P4 native(issue #102)
和 P3 wasm(本轮)两个独立加速层里各撞一次。issue #102 修 P4 loopFuel 时**没有
横向查 P3 wasm 有没有同样的缺口**——P3 的回边只做 GC、从来不计 budget,这个洞
一直开着直到本轮 fuzz 撞出。这与 [[cross-backend-semantic-fix-sweep]] 的「修语义
类 bug 要枚举所有绕过 host 语义的站点」完全同构,但对象是**抢占/计费**而非语义:
step budget 是一个「解释器在回边维护、每个加速层都必须各自复刻」的不变量,修一个
tier 的计费缺口时必须同 PR 横向查所有 tier(P3 wasm / P4 amd64 / P4 arm64)的回边
是否都计费。**候选并入 [[cross-backend-semantic-fix-sweep]]** 作「抢占/计费不变量
跨 tier 对称」新实例(与既有的语义类实例并列,首次样本暂留)。

### 教训 2:判「不可复现」前要跑到真正暴露 bug 的 tier/路径

本轮开头把 #135(`sum(555555520)`,有限但巨大)判为「不可复现资源耗尽型」,依据
是**单 Run 解释器重放干净**。但那是在最容易的路径上重放——bug 在 auto/P3 tier 才
暴露(有限 555M 迭代升 P3 后不计 budget 会跑很久 / 无限 `5/0` 直接挂)。第 4 个
crasher `5/0` 撞出后回头才看清 #135 是同一 bug 的温和实例。教训:crasher 落盘 input
在「解释器单 Run」重放干净**不等于**不可复现——差分 fuzz 的 crasher 往往在特定
tier(auto / force-all / P3 / P4)才暴露,判不可复现前必须在**报告它的那个 tier /
模式**下重放(harness 用 auto 就用 auto 跑,别只用解释器)。这是 [[unreproducible-
crasher-triage]] 的补充:该 guide 的「精确重放」步骤要明确「用报告 crasher 的 tier
重放」,不是默认解释器。**候选补入 [[unreproducible-crasher-triage]]**(首次样本暂留)。

### 教训 3:「同一语义装在多个 opcode / 多条终结边」时,修在唯一 choke point 而非逐个终结符

第一版把回边 safepoint 只挂在 FORLOOP 终结符,漏了 while/repeat 的 JMP 回边(review
bot 抓出)。根因是把「回边计费」这件事绑在**一个具体的循环 opcode**(FORLOOP)上,
而真实边界是「所有回边」——FORLOOP / while 的负位移 JMP / 比较驱动的 JMP 回边是
同一语义(跳回 loop 头)的不同 opcode 表达。正解是找到它们汇合的**唯一 choke
point**(`emitEdge` 的 `scLoop` 分支——所有回边都经此发 `br`),把计费挂在那里一次
覆盖全部,而不是给每个终结符分别补。判据:给「某类控制流边」加统一处理(计费 /
safepoint / 插桩)时,先问「这类边在代码里有没有一个汇合点」,有就挂在汇合点;若
分散在多个 opcode 的终结逻辑里各挂一次,一定会漏掉某个 opcode(这正是 P3 PW4b
TFORLOOP / while JMP 各走独立终结函数留下的坑)。这与教训 1(跨 tier 对称)是**同
一 PR 内的 tier 内对偶**:教训 1 说「同一不变量跨 tier 都要复刻」,本条说「同一
tier 内同一语义的多个 opcode 表达都要覆盖,找 choke point」。与 [[cross-backend-
semantic-fix-sweep]] 的「per-op / spec-模板多通道」同族(那里是修语义 bug 要扫所有
通道,这里是加计费要挂在通道汇合点)。首次样本暂留观察。

## promotion 决策

- 教训 1(抢占/计费不变量跨 tier 对称)候选并入 [[cross-backend-semantic-fix-sweep]]
  作新实例族(语义扫描的姊妹:计费/抢占扫描),首次样本暂留;
- 教训 2(判不可复现前跑到暴露 tier)候选补入 [[unreproducible-crasher-triage]]
  的重放步骤,首次样本暂留。

## 触发场景

- 给某个加速层(P4 native / P3 wasm)修「段内绕过 host 的计费/抢占缺口」时(教训 1:
  同 PR 横向查所有 tier 的回边 / 段内派发是否都复刻了解释器的抢占点);
- 给加速层的循环 / 段内派发加抢占计费时(照 P4 loopFuel / 本轮 P3 loopBudget:
  inline fuel 自减 + 归零才跨层,无 budget 时 unlimited 重填保住零跨层快路径);
- 判一个差分 fuzz crasher 为「不可复现」前(教训 2:先用报告它的 tier / 模式重放,
  别只在解释器单 Run 上重放就下结论)。
