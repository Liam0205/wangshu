---
name: 2026-09-13-issue260-nil-immediate-and-delete-gen
description: >
  nightly 开出的 issue #260(p4 `FuzzP4ForceAllPromote`,seed `96aaf5cc29cb7f3c`,分支
  `fix/issue260-p4-nil-guard`):`function add(x)s=A s=0 local function add()B=s%0 end add()add()end
  add(0)add(0)` 在 P1 正常、P4 报 `attempt to perform arithmetic on global 's' (a nil value)`。版本核对
  干净(run 跑在当前 master `a40e7ee`),当前 HEAD 重放 0.00 秒复现。最小形式
  `function f() s=nil s=0 local function g() B=s+0 end g() g() end f() f()`。**一个症状下面是两处独立缺陷
  叠加,单独撤掉任何一处修复,seed 都仍然通过**——所以「seed 过了」不能当归因证据,得给每一处缺陷
  各造一个只有它能让变红的输入。缺陷一:P4 native 手写的 NaN-box Nil 立即数写成 `0xFFFE<<48`
  (那是 `TagUserdata`),`value.Nil` 实际是 `0xFFF8<<48`;inline GETGLOBAL / SETGLOBAL / GETTABLE /
  SETTABLE 的「slot != Nil」守卫因此**从不**对真 Nil 触发,Nil 槽位直接被当值返回。这个常量从
  2026-06-26 PJ4 模板起就错,八处复制(`jit/amd64`、`jit/arm64` 模板包各一个 const,
  `peroptranslator/emit_ops_amd64.go` 六处字面量),所有 `-race` / difftest / conformance 都没抓到,因为
  它只在「IC 快照命中的槽位后来变成 Nil」时可见。缺陷二:`rawSet` 删键(val=Nil)与 weak 表 sweep 清项
  都不 BumpGen,而删掉的槽位 `next>=0` 仍留在链上,同键再插会落到**另一个**槽位、别的键也可能落进
  原槽位——key→slot 映射变了而 gen 不变,gen-only 的 inline 消费者继续读旧槽。这是
  [[2026-07-02-p4-beat-p3-opset-round]] 教训 2 预告的「已修一处不代表全表安全」的第二实例,
  doc-gaps 里挂了两个月的「BumpGen 路径清单未落」缺口由本轮收口:约定写进 `rawtable.go` 头注,
  删键两处补 bump,单元测试直接断言「槽位换主必伴随 gen 变化」。孤立缺陷一的 e2e 用数组部
  (`t[2]=nil` 后 `__index`,数组部没有 key→slot 间接所以 gen 从不变,只剩 Nil 守卫);孤立缺陷二的
  e2e 靠脚本枚举 5000 个全局名找到 `v4927` 删掉后 `k2621` 恰好落进同一槽位(只依赖字符串哈希,确定)。
  三条教训:手写常量必须有编译期或测试期的锚(→ [[prove-the-path-under-test]]);一个症状两处缺陷时
  每处都要有自己的判别输入(→ [[cross-backend-semantic-fix-sweep]]);invariant 的 producer 清单要
  在约定写下时一并盘点,不能只修 fuzz 撞到的那一格(→ [[design-claims-vs-codebase-physics]])。
metadata:
  type: reflection
  date: 2026-09-13
---

# Nil 立即数写错 + 删键不 BumpGen:一个 seed 底下两处缺陷(2026-09-13,issue #260)

> 范围:issue #260(nightly `FuzzP4ForceAllPromote` 开出),分支 `fix/issue260-p4-nil-guard`。改动落在
> `internal/gibbous/jit/amd64/pj4_template.go` 与 `internal/gibbous/jit/arm64/pj4_template.go`
> (Nil 常量改为 `0xFFF8<<48`)、`internal/gibbous/jit/peroptranslator/emit_ops_amd64.go`(六处字面量
> 改用 `uint64(value.Nil)`)、`internal/crescent/rawtable.go`(删键 BumpGen + 头注写 gen 约定)、
> `internal/gc/sweep.go`(weak 表清项 BumpGen);新测试 `jit/{amd64,arm64}/nanbox_consts_test.go`、
> `internal/crescent/rawtable_gen_test.go`、`test/regression/fuzz_260_test.go`;crasher 语料入
> `test/fuzz/testdata/fuzz/FuzzP4ForceAllPromote/96aaf5cc29cb7f3c`。

## 任务

nightly 报的 seed:

```lua
function add(x)s=A s=0 local function add()B=s%0 end add()add()end add(0)add(0)
```

harness 判定 `error 存在性真分叉(疑似 P4 误编译)`:P1 无错,P4 报
`attempt to perform arithmetic on global 's' (a nil value)`。

## 本轮做了什么

1. **版本核对**:失败 run 的 headSha 是 `a40e7ee`,就是当前 master,没有「已修复」这条便宜解释;
   当前 HEAD 重放 0.00 秒复现。
2. **最小化**(按 [[unreproducible-crasher-triage]] 逐层剥壳):`s=A`(A 为 nil,即 `s=nil`)、
   两次外层调用、两次内层调用都是必要的;`%0` 换成 `+0` 仍复现;把 `s` 换成局部表字段 `t.s` 就不复现
   (所以是 GETGLOBAL 通道);把 `B=s+0` 换成 `B=s`(不做算术)也不复现。最小形式:
   `function f() s=nil s=0 local function g() B=s+0 end g() g() end f() f()`。
3. **第一处缺陷**:读 `emit_ops_amd64.go` 的 GETGLOBAL NodeHit inline,守卫写的是
   `cmp rax, 0xFFFE_0000_0000_0000`,而 `internal/value/value.go` 里 `TagNil = 0xFFF8`,
   `0xFFFE` 是 `TagUserdata`。同一个错值在 `jit/amd64/pj4_template.go` 的 `qNanBoxNilImm`、
   `jit/arm64/pj4_template.go` 的 `qNanBoxNilImmArm64` 和 `emit_ops_amd64.go` 另外五处都有。
   `git log -S` 追到 2026-06-26 PJ4 模板首次引入,注释还写着「following value.go::Nil」。
4. **第二处缺陷**:修完常量后想给「删键后槽位换主」写个单元测试,结果它在 `rawtable.go` 上直接红:
   `rawSet` 的 `val == Nil` 分支把槽位置成 `(Nil, Nil, next)` 就返回,不 BumpGen;`insertNewKey` 判
   「main position 空」要求 `next < 0`,所以被删的链上槽位不算空,同键再插落到 `findFreeNode` 给的
   另一个槽位。`gc/sweep.go` 的 weak 表清项是同一形状。
5. **归因验证**:分别只撤掉一处修复重跑 seed,**两种情况 seed 都通过**。这说明 seed 需要两处缺陷同时
   存在(删键让 IC 快照指向的槽位变 Nil,Nil 守卫失效让它被当成值),任何一处单独修好都能让 seed 变
   绿——但另一处仍然是缺陷。于是分头构造判别输入:
   - 缺陷一:数组部没有 key→slot 间接,`t[2]=nil` 不会改 gen,只有 Nil 守卫挡着。
     `local t=setmetatable({1,2,3},{__index=function() return 9 end}) ... t[2]=nil return g()`
     在只撤 Nil 修复时 P4 返回 `-1`(把 Nil 槽当值)而 P1 走 `__index` 得 12。
   - 缺陷二:要让别的键落进被删键的槽位。全局表哈希只依赖字符串,写脚本枚举 `v<i>=1 ... v<i>=nil
     k<j>=7` 5000 对名字,`v4927` / `k2621` 命中:只撤 gen 修复时 P4 读到 `k2621` 的 7 算出 10,P1 得 -1。
   三条 pin 加上两处修复的 2×2 撤回矩阵全部按预期变红/变绿。
6. **P3 侧只是推断**:P3 wasm `emitGetGlobal` 与 P4 一样是 gen-only,但本轮三条 pin 在 p3 tag 下、撤掉
   gen 修复也全绿——force-all 在首次执行就升层,那时 IC 还没回填,wasm 走的是纯 helper 分支,inline
   路径没被触达。「P3 也有这个缺陷」是从代码结构读出来的,没有实测复现;要证实需要一个先把 IC 烤热再
   升层(按热度阈值而非 force-all)的输入。
7. **fuzz 重探**:修后 `FuzzP4ForceAllPromote` 跑 60 秒无新发现;全套 p1/p3/p4 测试、lint、
   `GOARCH=arm64 go vet`、conformance-p4、difftest-p4 通过。

## 期望与实际

- 期望:一个 fuzz seed 对应一个根因,修好后 seed 变绿即闭环。
- 实际:seed 变绿是两处缺陷的**合取**被打破,任何一处修复都能做到;若只修先看到的那处(Nil 常量),
  删键不 bump gen 会继续潜伏,等下一个恰好让别的键落进旧槽位的 seed。

## 教训

### 教训 1:手写进 codegen 的常量必须有一个锚,不能只有一条「following X」的注释

**核心断言**:`qNanBoxNilImm` 的注释写「following internal/value/value.go::Nil」,值却是另一个 tag。
注释不会被编译器检查,而 `jit/amd64` 是叶子 byte emitter、不能 import `internal/value`,于是这个数字
两个月里被复制了八次、没有任何东西能发现它错。Nil 守卫失效的效果是「守卫永不触发」,而不是「守卫
乱触发」——所有正常路径都更快、结果都对,只有槽位真变 Nil 那一刻才错,常规测试自然全绿。

**判据**:任何以字面量形式写进 emitter 的编码常量(tag 位、Nil/True/False 位、掩码、结构体偏移),
要么直接引用定义它的包,要么在 test 里与定义包比对(本轮 `TestNilImmMatchesValueNil` 就是这个锚,
在旧常量上确认会红)。自查办法:grep emitter 目录里的 `0x` 字面量,每一个都问「它的真值在哪个包
定义、这里有什么在盯着两者一致」。

### 教训 2:一个 seed 变绿不等于归因完成——两处缺陷合取时,每处都要有只属于它的判别输入

**核心断言**:撤掉 Nil 修复 seed 过、撤掉 gen 修复 seed 也过,说明 seed 只证明「两处缺陷至少修了
一处」。先修的那处天然会被当成「根因」,第二处就成了下次 nightly 的 issue。

**判据**:发现第二处可疑缺陷时,先做撤回矩阵(每处单独撤、全撤);只要有「单撤仍绿」,就必须为那
一处另造判别输入,直到每处修复都有一个只因它变红的测试。构造思路是**去掉另一处缺陷的参与条件**:
本轮用数组部消掉 gen 维度(只剩 Nil 守卫),用「别的键落进旧槽位」消掉 Nil 维度(旧槽位不是 Nil,
只剩 gen)。这与 [[cross-backend-semantic-fix-sweep]] 讲的「多处独立错误叠加,少修一处仍然错」是
同族,但形状相反:那里是**析取**(任一处错就错,修一处症状不消),这里是**合取**(要都错才错,修一
处症状就消),合取更危险,因为症状消失会被读成「修好了」。

### 教训 3:写下一条 不变量约定时,要同时盘点它的全部 producer,而不是只修 fuzz 撞到的那一格

**核心断言**:2026-07-02 `insertNewKey` Brent 重定位漏 bump 被修时,反思已经写了「已修一处不代表全表
安全」,doc-gaps 也记了「producer 侧 BumpGen 路径清单未落」;两个月后 fuzz 撞到的正是同一清单上
的另一格(删键)。缺口被准确预告却没有被执行,因为它当时被排在「P4 arm64 port / P5 前」这种远期
里程碑之后,而一格 grep + 三处判断只要半小时。

**判据**:约定成文的那一刻就把 producer 列成表(本轮:rehash / Brent 重定位 / rawSet 删键 / weak
sweep 清项),逐条标「已 bump / 补 bump / 不需要(为什么)」,并写一个直接断言约定的单元测试
(本轮 `TestRawTable_SlotReuseAfterDeleteBumpsGen` 断言「槽位换主必伴随 gen 变化」,不依赖任何
consumer)。而且要**每个 producer 一条**:首轮盲审发现 weak sweep 那一格补了 bump 却没有测试盯着,
单独撤掉它全部测试仍绿——正是本条教训在同一轮里被自己违反了一次,`TestWeak_SweepClearBumpsGen` 补上。**invariant 强度由最严 consumer 定义**这句话的操作含义就是:consumer 一旦选了 gen-only,
producer 清单就必须完整,清单不完整时缺的每一格都是一个待发的 fuzz issue。

## Promotion 决策

- **教训 1 → [[prove-the-path-under-test]]**:新增 §4.11「emitter 里的手写编码常量要有锚」,并在 §5 的
  inline 快路径条目后追加本例(第四个「inline 省校验 → fuzz 才抓到」实例,但这次省掉的不是校验而是
  校验用的真值)。
- **教训 2 → [[cross-backend-semantic-fix-sweep]]**:补「合取型叠加」这一格与撤回矩阵手法。
- **教训 3 → [[design-claims-vs-codebase-physics]]**:§2 arena 重定位一节旁新增 §2.1「gen 约定的
  producer 清单」,把 rawtable 的四个 producer 写成表;同时关闭 `memory/doc-gaps.md` 里的对应缺口。
