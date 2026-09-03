---
name: 2026-08-27-issue248-index-line-across-newline
description: >
  nightly 开出的 issue #248（p1 `FuzzOracleDiff`），seed 是
  `co=coroutine.create(function()(0\n)(A.A)end)print(coroutine.resume(co))`，分支
  `fix/248-index-error-line`，1 个 commit `1693a69`。**版本核对干净**：失败 run 的 headSha 正好就是当时的
  master（`13a9311`），当前 HEAD 重放确实复现。**分歧是错误消息里的行号**：oracle 报 `:2:`，望舒报
  `:1:`，消息本体一致（`attempt to index global 'A' (a nil value)`），真 `lua5.1` 二进制同样报 `:2:`。
  **最小化剥掉协程之后**问题不变：`print(pcall(function() return A\n.A end))` 一样分歧，协程只是噪声，
  关键是**索引表达式跨了换行**——PUC 把错误归到**索引运算符所在行**（那条 GETTABLE 出错），望舒归到
  表达式起始行。**根因是两处独立错误，缺任一处修复行号仍然不对**：① `exprIndex`
  （`internal/frontend/compile/codegen.go`）把**对象**交给 `exp2AnyReg(e.Line, &obj)`，而 `e.Line` 是
  运算符的行，延迟加载的 GETGLOBAL 被盖上运算符的行，改成 `e.Obj.Pos()`（key 用 `e.Key.Pos()`）；②
  `eIndexed` 这个 expDesc 不带行号，它的 GETTABLE 用「之后由谁 discharge 就取谁的行」——对
  `local v = ...` 就是 LocalStmt 的行，给 expDesc 加了 `opLine` 字段，GETTABLE 用它。**仓里早就有**
  `TestIndexExprLineIsOperatorLine`，而它一直是绿的：它用局部变量做对象，局部变量已经在寄存器里，
  `exprIndex` 对它不发射任何指令、GETTABLE 立即 discharge，两个 bug 在这条路径上都不可能出现；新测试
  因此特意用全局，并核对整张 LineInfo 表而不是单个条目。三条教训：位置类测试必须用会触发延迟加载的被测
  对象（判据是「这个写法会让编译器发射那条指令吗」，不发射就测不到）/ 一处症状可能由多处独立错误叠加，
  少修一处仍然错（判据是改完先逐条核对整张表，不是只看错误消息变没变）/ 最小化要先剥掉「看起来相关」的
  外壳（协程是噪声，逐个删外层构造、每删一次重跑，别对着 seed 的原始形式找因果）。
metadata:
  type: reflection
  date: 2026-08-27
---

# 索引表达式跨行时,两处独立错误各自把行号钉错了一次(2026-08-27,commit `1693a69`)

> 范围:issue #248(nightly `FuzzOracleDiff` 开出),分支 `fix/248-index-error-line`,1 个 commit
> `1693a69`。改动落在 `internal/frontend/compile/codegen.go`(`exprIndex` 改用 `e.Obj.Pos()`/
> `e.Key.Pos()`)、`internal/frontend/compile/expdesc.go`(`expDesc` 加 `opLine` 字段,`dischargeVars`
> 的 GETTABLE 改用它)与新测试 `internal/frontend/compile/issue248_index_line_test.go`。

## 任务

nightly 报的 seed:

```lua
co=coroutine.create(function()(0
)(A.A)end)print(coroutine.resume(co))
```

## 本轮做了什么

### 0. 版本核对:干净,而且没有第二种解释

按 [[unreproducible-crasher-triage]]「第一步永远是版本核对」核了一遍:失败 run 的 headSha 正好**就是
当时的 master**(`13a9311`),不可能过期;当前 HEAD 上重放确实复现。这一档不需要更多分诊,直接进分歧
分析。

### 1. 分歧是行号,不是消息本体

oracle 报 `:2:`,望舒报 `:1:`,消息本体一致(`attempt to index global 'A' (a nil value)`)。拿真的
`lua5.1` 二进制单独跑同一段也报 `:2:`——这确认了参照实现没有分歧,分歧在望舒自己的行号记账。

### 2. 最小化:协程是噪声,一路删到「索引表达式跨行」这一个维度

seed 里带协程、`pcall`(经 `coroutine.resume`)、括号包裹的调用。逐个删外层构造重跑:

- 去掉协程壳、直接 `pcall`:`print(pcall(function() return A\n.A end))` 一样分歧——**协程无关**。
- 继续剥,剩下的核心写法是**索引表达式跨了换行**:`A\n.x` 这种写法,不管外面裹了什么控制结构,分歧都
  在。

到这一步「协程」「函数调用」「pcall」全部证明是噪声,真正相关的维度只有一个:索引运算符与它的对象/键
之间隔着换行。

### 3. 根因:两处独立错误,而且方向刚好相反

dump 修前的 `LineInfo` 发现:`pc0 GETGLOBAL line=2、pc1 GETTABLE line=1`——**反了**,GETGLOBAL(对象
的加载)本该是行 1(`A` 所在行),GETTABLE(索引运算)本该是行 2(`.x` 所在行)。

1. **`exprIndex` 把对象交给 `exp2AnyReg(e.Line, &obj)`**,而 `e.Line` 是**运算符**的行。延迟加载的
   GETGLOBAL 因此被盖上运算符的行,而不是对象自己的行。改成 `e.Obj.Pos()`(key 对应用
   `e.Key.Pos()`)。
2. **`eIndexed` 这个 expDesc 不带行号**,它的 GETTABLE 在「之后由谁 discharge」时才拿到行号——
   discharge 发生在任意一个后续点,对 `local v = A\n.x` 这种写法就是 `LocalStmt` 所在的行(行 1)。给
   `expDesc` 加 `opLine int32` 字段,GETTABLE 改用它而不是 discharge 调用方传入的行。

两处任意单独修复,行号都仍然是错的——第一处修好只是让 GETGLOBAL 落到对的行,GETTABLE 仍然跟着
discharge 点走;第二处修好只是让 GETTABLE 落到对的行,GETGLOBAL 仍然被运算符的行覆盖。两处一起修才
让整张 LineInfo 表对。

### 4. 既有测试为什么长期假绿

仓库里早就有 `TestIndexExprLineIsOperatorLine`(`internal/frontend/parse/parser_test.go`),而它在
两个 bug 都存在的整段期间**一直是绿的**。原因:它用的对象是**局部变量**(`local v = t\n .x`)。局部
变量已经在寄存器里,`exprIndex` 对它**不发射任何指令**——没有 GETGLOBAL 那一步可以被盖错行,也没有
「延迟到之后 discharge」这件事,GETTABLE 立即以调用方给的行发射。两个 bug 都依赖「对象的加载被延迟」
这件事才会出现,而局部变量这条路径从设计上就不延迟——所以现有测试测的从来不是会暴露 bug 的那条路径。

新测试(`TestIndexLinesAcrossNewline`)因此特意用**全局**做对象(会延迟加载,走 GETGLOBAL),并且核对
**整张** `LineInfo` 表(逐条 pc→line)而不是单个字段——只看最终报错的那一行看不出「反了」这件事,只有
把每条指令的行号都摆出来才能看出 GETGLOBAL 和 GETTABLE 互相戴错了对方的帽子。

## 期望与实际

| 期望 | 实际 |
|---|---|
| seed 里的协程与 `pcall` 是问题的一部分 | 都是噪声,最小化到 `A\n.x` 就已经稳定复现 |
| 修一处(比如只改 `exprIndex` 的行参数)就够 | 两处独立错误,任一处单独修复行号仍然错(方向相反,一个偏运算符行、一个偏 discharge 点行) |
| 既有的 `TestIndexExprLineIsOperatorLine` 应该早就抓到这个 | 它用局部变量,局部变量不触发延迟加载,两个 bug 在这条路径上都不可能出现,所以它测的从来不是相关路径 |

## 教训

### 教训 1:测「位置/行号」类的性质,必须用会触发延迟加载的被测对象

**核心断言**:位置信息类的 bug 往往只在「值被延迟物化」的路径上出现——已经在寄存器里的值不需要
`exprIndex`/`dischargeVars` 发射任何新指令,所以无论行号参数传得对不对,都没有一条指令可以被钉错。
局部变量恰好是最省事、最容易被拿来写测试的对象,而它恰好是这条路径上**不会触发**延迟加载的那一种。

**判据**:写位置/行号类的测试之前,先问「这个写法会让编译器**发射**我关心的那条指令吗」——发射了
才有一条指令的「行号」这个属性可以被测。判定方法:如果对象/操作数已经在寄存器/已知位置(局部变量、
已经算出来的常量),编译器很可能就地复用、不发射新指令;要触发发射,用需要**加载**的对象(全局、
另一层索引、函数调用结果)。

这与 [[prove-the-path-under-test]] §2 的核心命题同构(测试通过 ≠ 在测的路径被走到),但落在一个新的
具体一格:**被测对象的写法本身决定了「那条指令是否被发射」**,这一格此前的实例(inline vs helper、
force-all vs auto、错误路径盲区)全部是「同一条指令走了哪条实现」,本例是「这条指令有没有被发射」——
后者更前置,如果指令都没发射,连「走了哪条实现」这个问题都不存在。

### 教训 2:一处症状可能由多处独立错误叠加,少修一处仍然错

**核心断言**:「消息报错了」这个单一症状不能唯一确定「有一个 bug」——它同样可以是两个方向相反的
bug 叠加而成的净效果,而且这种叠加有一个陷阱:**单独修复其中一处,症状可能看起来「变了但还是错的」,
容易被误判为「修法方向错了」而回头怀疑刚做的那一处**。

本例修前 dump 出来的 `pc0 GETGLOBAL line=2、pc1 GETTABLE line=1` 已经把两处错误的存在写在了数据里
(两条指令的行号都不对,而且方向相反),只是当时还没意识到这是两件独立的事。

**判据**:改完一处怀疑的行号错误后,**先 dump 整张 LineInfo 表逐条核对,而不是只看最终报出来的那条
错误消息变没变**。错误消息只反映「报错点」那一条指令的行号,而报错点往往不是唯一被写错的那条——本例
真正被写错的是 GETGLOBAL 和 GETTABLE 两条,而报错消息在两处都错的情况下也可能凑巧「看起来」只差一
行,容易被误判成只有一处错。自查办法:改完一处修复后,如果错误消息仍然不对,先问「还有没有第二条
指令的行号也不对」,再决定要不要撤回刚做的修复。

### 教训 3:最小化要先剥掉「看起来相关」的外壳,逐层删、每删一次重跑

**核心断言**:seed 里显眼的构造(本例是协程)会天然地把注意力吸引过去,而 fuzzer minimize 出来的
seed 本身不保证「每一层外壳都是必要的」——它只保证「删了会不再复现」这件事此前**没有被验证过**。
一开始盯着协程去查,是把 seed 的表面结构当成了因果结构。

**判据**:最小化时逐个删掉外层构造(协程壳、`pcall`、括号包裹),每删一次都重跑,直到删不动为止,
不要对着 seed 的原始形式去找因果。本例把协程去掉、把 `pcall` 保留时分歧还在,这一步就足够确认协程是
噪声;继续删剩下的构造,最终稳定在「索引表达式跨了换行」这一个维度上。

这与 [[unreproducible-crasher-triage]]「一批 crasher 先问会不会被同一个改动一起解决」是同一族纪律
的另一面:那条讲**多个** issue 之间要先判断是不是同一件事;本条讲**单个** issue 内部的 seed 结构里,
哪些部分是那个根因的必要条件、哪些只是偶然裹在外面的壳,同样需要逐一验证而不能从字面结构直接读出。

## 后续订正(2026-09-03,issue #252)

本轮定的口径「PUC 把错误归到**索引运算符所在行**」**不成立**,它只是真实规则的一个近似。#252
(`(A.A\n)()`)证明了这一点,详见 [[2026-09-03-issue252-discharge-line-is-lastline]]。

真实规则是:PUC 的 `luaK_codeABC`/`luaK_codeABx` **根本不接收行号参数**,一律用发射那一刻的
`fs->ls->lastline`。所以 GETTABLE 的行是「**谁 discharge 它、在哪一行 discharge**」,而不是运算符
写在哪。两者只在「索引写完就地被消费」时相等,而 `A.x\n\n+1`(**没有括号**)就已经分叉:运算符在
第 1 行,PUC 报第 3 行(`+` 才是 discharge 点)。

因此本轮加的 `expDesc.opLine` 字段在 #252 中被删除,改由调用方传入 lastline 语义的行
(`ParenExpr.EndLine`、`LocalStmt.EndLine`)。本轮那句「`e.Line` 是运算符的行,这才是 faulting
GETTABLE 必须带的行」是错的,已在代码注释中改正。

本轮**正确且保留**的部分:对象与键各自按 `e.Obj.Pos()` / `e.Key.Pos()` discharge(GETGLOBAL 归对象
自己的行),以及「位置类测试必须用会触发延迟加载的对象」这条教训——它在 #252 里再次生效。

## Promotion 决策

- **教训 1 → [[prove-the-path-under-test]]**:「测试没走到被测路径」那一族的新一格——被测对象的写法
  决定了那条指令是否被发射,这一格排在「走了哪条实现」之前,是更前置的检查。
- **教训 2 → [[cross-backend-semantic-fix-sweep]]**:「同一症状可能由多处独立错误叠加」那一族——本例
  是同一子系统(前端 codegen)内两处独立错误的叠加,而不是跨后端/跨通道的叠加,补一个新的具体形式。
- **教训 3 → [[unreproducible-crasher-triage]]**:最小化那一族,「一批 crasher 是否同一件事」判据在
  单个 seed 内部结构上的应用。
