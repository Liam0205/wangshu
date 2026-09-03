---
name: 2026-09-03-issue252-discharge-line-is-lastline
description: >
  nightly 开出的 issue #252(p1 `FuzzOracleDiff`),seed 是
  `co=coroutine.create(function()(A.A\n)()end)print(coroutine.resume(co))`,分支
  `fix/252-paren-discharge-line`。**标题写「go-fuzz crash」但根本不是崩溃**:两边都正确抛
  `attempt to index global 'A' (a nil value)`,verdict class 一致,只有错误消息里的**行号**不同
  (PUC `:2:`,望舒 `:1:`),经 `print(coroutine.resume(co))` 打印出来才被 output 比对逮到。
  **版本核对干净**:失败 run 的 headSha 就是当前 HEAD(`e945a78`)。**本轮推翻了 #248 定下的口径**:
  #248 认为「PUC 把 GETTABLE 归到索引运算符所在行」并为此加了 `expDesc.opLine`,而真实规则是
  `luaK_codeABC`/`luaK_codeABx` **不接收行号参数**、一律用发射那一刻的 `fs->ls->lastline`——所以
  GETTABLE 的行取决于**谁在哪一行 discharge 它**。两者只在「索引写完就地被消费」时相等。决定性反例是
  `local v = A.x\n\n+1`:**完全没有括号**,运算符在第 1 行,PUC 报第 3 行(`+` 才是 discharge 点),
  这排除了「括号特例」和「运算符行」两种模型。修法是删掉 `opLine`、让调用方传 lastline 语义的行,为此
  给 `ParenExpr` 和 `LocalStmt` 各加 `EndLine`(取 `p.lastLine`)。三处缺一不可:单删 `opLine` 会让
  #248 的 `local v = A\n.x` 退化成 1(`stmtLocal` 传的是语句起始行)。**这个缺口 #248 那轮已经预先
  记录过**(commit `e598660` 明确写下 PUC 用 `ls->lastline`、匹配它需要给 AST 加结束行、判定为
  wider than #248 而撤回了半成品修复),#252 是它第一次以**会抬错的指令**的形式浮出水面——当时判断
  「今天看不见」的依据是 SETGLOBAL/SETUPVAL/MOVE 不会 raise,而 GETTABLE 会。三条教训:近似口径要标注
  成近似并写下反例形状 / 一次修复要同时改「谁来传」和「谁来用」两侧 / 预先记录的缺口要附带「它何时会
  变得可见」的判据。
metadata:
  type: reflection
  date: 2026-09-03
---

# GETTABLE 的行是 discharge 点的 lastline,不是索引运算符的行(2026-09-03,issue #252)

> 范围:issue #252(nightly `FuzzOracleDiff` 开出),分支 `fix/252-paren-discharge-line`。改动落在
> `internal/frontend/ast/ast.go`(`ParenExpr`/`LocalStmt` 各加 `EndLine`)、
> `internal/frontend/parse/expr.go` 与 `stmt.go`(填 `p.lastLine`)、
> `internal/frontend/compile/expdesc.go`(删 `opLine`,GETTABLE 用传入的行)、
> `internal/frontend/compile/codegen.go`(`ParenExpr` 用 `EndLine` discharge)、
> `internal/frontend/compile/stmt.go`(`stmtLocal` 的初始化列表用 `EndLine`),新测试
> `internal/frontend/compile/issue252_discharge_line_test.go`,crasher 语料入
> `testdata/fuzz/FuzzOracleDiff/612767150dcb06d5`。

## 任务

nightly 报的 seed,标题写作 `go-fuzz crash (p1)`:

```lua
co=coroutine.create(function()(A.A
)()end)print(coroutine.resume(co))
```

## 本轮做了什么

### 0. 版本核对干净,但「crash」这个词不成立

按 [[unreproducible-crasher-triage]] 先核版本:失败 run 的 headSha(`e945a78`)**就是当前 HEAD**,
重放确实复现。但重放结果不是子进程崩溃:

```
oracle:  "false\t[string \"fuzz\"]:2: attempt to index global 'A' (a nil value)\n"
wangshu: "false\t[string \"fuzz\"]:1: attempt to index global 'A' (a nil value)\n"
```

两边都正确抛出同一条错误,verdict class 一致,分歧只在**行号**。nightly 的 issue 模板对所有
`FuzzOracleDiff` 失败统一写「crash(子进程异常退出/死循环/panic)」,而差分失败其实是 `t.Fatalf`。
与 [[2026-08-27-issue248-index-line-across-newline]] 完全同一族——那次也是这个标题、也是协程壳、也是
索引跨行的行号分歧。

### 1. 用 luac 建标尺,而不是只看那一个 seed

`luac5.1 -p -l` 逐个形状取真值,一开始只是想确认 PUC 把 GETTABLE 放第 2 行:

| 源码 | PUC GETTABLE |
|---|---|
| `(A.A)()` 全在一行 | 1 |
| `(A.A\n)()` | 2 |
| `(A.A\n\n)()` | 3 |
| `((A.A\n)\n)()` | **2**(内层右括号),CALL 在 3 |

前三行看起来像「跟着右括号走」,于是第一版修法是给 `ParenExpr` 加 `EndLine`。**这一版是错的**,dump
出整张 `LineInfo` 逐条核对时露了馅:`local v=(A.x\n\n)` 变成 0、`((A.A\n)\n)()` 变成 3,而
`A.x\n\n+1` 根本没被修。按 [[2026-08-27-issue248-index-line-across-newline]] 教训 2 的判据(改完先
dump 整张表、不要只看错误消息变没变)当场撤回,没有留下半成品。

### 2. 决定性反例:没有括号也分歧

继续扩标尺时找到那个把模型钉死的形状:

```lua
local v = A.x

+1
```

**没有任何括号**,索引运算符在第 1 行,`luac5.1` 把 GETTABLE 放在第 **3** 行——`+` 所在行。望舒放第
1 行。这一个反例同时排除了两种模型:不是「括号特例」(没有括号),也不是「运算符行」(运算符在 1)。

### 3. 根因:PUC 压根不给指令传行号

读 `internal/oracle/_lua515/src/lcode.c`:

```c
int luaK_codeABC (FuncState *fs, OpCode o, int a, int b, int c) {
  return luaK_code(fs, CREATE_ABC(o, a, b, c), fs->ls->lastline);
}
```

**每条指令一律用 `fs->ls->lastline`** ——词法器逐 token 推进的「最后消费的 token 的行」,只有
`luaK_fixline` 会事后改写个别指令(函数糖、CALL)。而望舒是**每个 emit 点显式传一个行参数**、行号从
AST 节点上就近取。两套记账模型不同,`opLine`(#248)、`ArgsLine`、`AssignStmt.EndLine` 都是在望舒
这套模型下对 PUC 逐点打的补丁,#252 是补丁没覆盖到的又一格。

所以「运算符行」是「discharge 点的 lastline」的**近似**:索引写完就地被消费时两者相等,消费点被推迟
过换行就分叉。括号只是推迟消费的一种方式,算术运算符是另一种。

### 4. 三处配合,缺一不可

插桩打印 `dischargeVars` 的调用栈与三个行号,把每个形状的成因分开看:

| 形状 | callerLine | opLine | 结论 |
|---|---|---|---|
| `A.x\n\n+1` | **3(对)** | 1 | 传入的行本来就对,被 `opLine` 覆盖 |
| `(A.x\n\n)` | 1(错) | 1 | 走 `ParenExpr` 分支,传的是**左**括号行 |
| `local v = A\n.x`(#248) | 1(错) | 2(对) | `stmtLocal` 传语句起始行,全靠 `opLine` 兜 |

于是修法必须三处一起:

1. 删掉 `opLine` 覆盖,GETTABLE 用传入的行 → 修 `A.x\n\n+1`;
2. `ParenExpr` 加 `EndLine`(右括号行,取 `expect(RPAREN)` 之后的 `p.lastLine`),discharge 用它 →
   修两个括号形状;
3. `LocalStmt` 加 `EndLine`,`stmtLocal` 的初始化列表用它 → 保住 #248 的形状。

只做第 1 步时 `local v = A\n.x` 退化成 1,这一步的实验结果直接证明了第 3 步的必要性。
`registerLocal` 仍用 `s.Line`——那是作用域边界,不是指令行。嵌套括号必须用**最内层** `ParenExpr` 的
`EndLine`(`((A.A\n)\n)()` 的 GETTABLE 在 2 而 CALL 在 3),所以 `EndLine` 挂在每个 `ParenExpr` 节点
上,不能改成「整个表达式的结束行」。

### 5. #248 那轮已经把这个缺口写下来了

commit `e598660` 的正文:

> The third is storeVar's single-target store with a multi-line RHS. PUC emits it at ls->lastline, which
> its LEXER advances token by token... Matching this needs an end-line on the AST, which is wider than
> #248, so the comment now states the gap and why it is invisible today -- SETGLOBAL/SETUPVAL/MOVE cannot
> raise.

当时还写了一版 `max(Pos())` 的修复,发现 `Pos()` 返回调用的**起始**行、AST 没有结束位置,于是撤回、
只记缺口。判断「今天看不见」的依据是**那几条指令不会 raise**,所以行号错了也没人看得见。#252 正是
这个缺口第一次落在**会 raise 的指令**(GETTABLE)上——依据本身没错,只是它没有回答「什么时候会变得
可见」。

## 期望与实际

| 期望 | 实际 |
|---|---|
| issue 标题说 crash,先按崩溃分诊 | 不是崩溃,是错误消息行号差分;nightly 模板对所有 `FuzzOracleDiff` 失败统一写 crash |
| #248 已经把索引行号修对了,#252 是个新的独立形状 | #248 的口径本身是近似,`opLine` 在本轮被删除 |
| 括号推迟了 discharge,是个括号特例 | `A.x\n\n+1` 没有括号也分歧,规则与括号无关 |
| 给 `ParenExpr` 加 `EndLine` 就够 | 三处配合缺一不可;单改 `ParenExpr` 会把另两个形状改坏、单删 `opLine` 会让 #248 退化 |
| 这个缺口是新发现的 | #248 那轮已明确记录并撤回过半成品修复,只是没写「何时会变可见」 |

## 教训

### 教训 1:把近似当成规则写进代码时,要标注它是近似并写下已知反例的形状

**核心断言**:#248 从若干个都满足的样本里读出「GETTABLE 归到索引运算符所在行」,并把它作为**规则**写
进字段名(`opLine`)、注释和测试。规则与近似的差别不在当时是否全绿——两者都全绿——而在于**近似有失
真区间,而写下来的规则不带这个信息**。下一个读者(以及下一个 fuzz seed)拿到的是一条看起来无条件的
规则。

**判据**:从参照实现的若干个输出里归纳出一条口径时,先问「参照实现**内部**是怎么算这个量的,我的口
径和它是同一个式子,还是在我采的样本上恰好相等」。能读到源码就去读(本例 `luaK_codeABC` 五行就给出
了答案);读不到就构造「让两个模型给出不同答案」的输入去区分——本例 `A.x\n\n+1` 就是这样一个判别
输入,它在 #248 那轮同样可以构造出来。归纳出的口径若无法证明与参照实现同式,注释里要写明它是近似、
以及**已知会失真的形状**,而不是只写它在做什么。

这与 [[design-claims-vs-codebase-physics]] §5.1「判断外部依赖现状时判据是那份源码,不是关于它的陈
述」同构,落在一个新的具体一格:那条讲**别人对源码的陈述**不可靠,本条讲**自己从源码行为归纳的口
径**同样不可靠,而且更危险——它由自己写下,读起来像一手事实。

### 教训 2:一个行号/位置错误可能同时需要改「谁来传」和「谁来用」两侧

**核心断言**:望舒把行号从 AST 就近取、逐点传给 `emit`,于是「某条指令行号错了」有两个独立成因:
**用的人**取错了(读了 `opLine` 而不是传入的行),**传的人**给错了(`stmtLocal` 传语句起始行、
`ParenExpr` 传左括号行)。只修一侧的表现是「部分形状修好、另一部分改坏」,很容易被误读成「修法方向
错了」而整体撤回。

**判据**:改行号类缺陷前,先插桩打印**每个失败形状**的调用栈加上「传入的行 / 用到的行」两个值,按
「传入的对不对」把形状分组。传入的对而结果错 → 改用的一侧;传入的就错 → 改传的一侧。本例分组后
三类形状分别落在两侧,一次就把三处都定位了,不需要试错。这是
[[2026-08-27-issue248-index-line-across-newline]] 教训 2(一处症状可能由多处独立错误叠加)在**同一
条数据流的上下游**这个形式上的具体化。

### 教训 3:记录一个已知缺口时,要附带「它在什么条件下会变得可见」

**核心断言**:#248 记缺口的方式是完备的——写了 PUC 的真实机制、写了为什么匹配不了、写了为什么今天
看不见(那几条指令不会 raise)、并且撤回了不完整的修复而不是留一句假装修好的注释。**唯一缺的是**
「什么条件下它会变得可见」。有了那一句(比如「一旦同样的 lastline 规则落在会 raise 的指令上,例如
GETTABLE,这个缺口就会变成用户可见的错误行号」),#252 从 nightly 报上来时就能被立刻认出是旧账,而
不是当成新问题重新分诊一轮。

**判据**:写「已知缺口 / 已接受偏离」时,除了「是什么」「为什么现在不修」,再补一行**触发条件**:
哪一类新代码、哪一类新输入会让它变得可见。自查办法是问「如果这个缺口明天被 fuzz 撞出来,我这段记录
能让读者认出是它吗」。这与 [[prove-the-path-under-test]] §4.2(「已知差异」类注释必须指出哪一行代码
或哪个测试执行了它)是同一条纪律的两个方向:§4.2 要求豁免声明有**执行体**,本条要求缺口记录有**触发
条件**——前者防「声明了但没人执行」,后者防「记录了但认不出来」。

## Promotion 决策

- **教训 1 → [[design-claims-vs-codebase-physics]]**:§5.1 那一族的新一格——自己从参照实现行为归纳
  出的口径,与别人对源码的陈述一样需要回到源码验证;附「构造判别输入」这个具体手法。
- **教训 2 → [[cross-backend-semantic-fix-sweep]]**:接
  [[2026-08-27-issue248-index-line-across-newline]] 教训 2 的「多处独立错误叠加」那一族,补「同一条
  数据流上下游两侧」这个形式与「按传入值是否正确分组」的判据。
- **教训 3 → [[prove-the-path-under-test]]**:§4.2「已知差异类注释必须有执行体」的时间对偶——缺口
  记录要写触发条件,否则下次撞上时认不出是旧账。
