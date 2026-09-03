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
  「今天看不见」的依据是 SETGLOBAL/SETUPVAL/MOVE 不会 raise,而 GETTABLE 会。**第一版修法只顾了 #252,把 #248 自己的常驻语料
  `8dff36b8bd115962`(`(0\n)(A.A)`,索引作为**调用参数**)改红了**,而 `test-all`/`conformance-all`/
  `difftest-all` 四道门全绿、只有 `make fuzz-oracle` 逮到——真实规则比第一版更细:**每个表达式在它
  后面那个分隔符被消费后各自 discharge**(`f(A.A\n,1\n)` 给出 GETTABLE:2 与 LOADK:3),所以最终修法
  落在被五处共用的 `parseExprList` 上,新增 `parseExprListEnds` 返回 per-expression 物化行,末元素的
  行由调用方(`)` / 语句末尾)填,并补了 `calleeEndLine` 处理括号包住的被调用者(`(0\n)(A.A)` 的
  LOADK 归第 2 行)。四条教训:近似口径要标注成近似并写下反例形状 / 一次修复要同时改「谁来传」和「谁
  来用」两侧 / 修语料驱动的 issue 要把同族旧语料一起当验收门 / 预先记录的缺口要附带「它何时会变得
  可见」的判据。
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

### 4. 插桩把「谁传错」与「谁用错」分开

插桩打印 `dischargeVars` 的调用栈与三个行号,把每个形状的成因分开看:

| 形状 | callerLine | opLine | 结论 |
|---|---|---|---|
| `A.x\n\n+1` | **3(对)** | 1 | 传入的行本来就对,被 `opLine` 覆盖 |
| `(A.x\n\n)` | 1(错) | 1 | 走 `ParenExpr` 分支,传的是**左**括号行 |
| `local v = A\n.x`(#248) | 1(错) | 2(对) | `stmtLocal` 传语句起始行,全靠 `opLine` 兜 |

于是第一版修法是三处一起:删掉 `opLine` 覆盖(修 `A.x\n\n+1`)、`ParenExpr` 加 `EndLine`(修两个括号
形状)、`LocalStmt` 加 `EndLine`(保住 #248 的形状)。只做第一步时 `local v = A\n.x` 退化成 1,这个
实验结果直接证明了第三步的必要性。`registerLocal` 仍用 `s.Line`——那是作用域边界,不是指令行。嵌套
括号必须用**最内层** `ParenExpr` 的 `EndLine`(`((A.A\n)\n)()` 的 GETTABLE 在 2 而 CALL 在 3),所以
`EndLine` 挂在每个 `ParenExpr` 节点上,不能改成「整个表达式的结束行」。

### 5. 第一版修法只顾了 #252,把 #248 自己的语料改红了

第一版改动(删 `opLine` + `ParenExpr.EndLine` + `LocalStmt.EndLine`)让 #252 通过、`test-all` /
`conformance-all` / `difftest-all` 全绿,看起来可以收工。**`make fuzz-oracle` 抓到它把 #248 自己的
常驻语料 `8dff36b8bd115962` 改红了**:

```lua
co=coroutine.create(function()(0
)(A.A)end)print(coroutine.resume(co))
```

这个形状的索引在**括号外、作为调用参数**(`(0\n)` 是被调用者,`(A.A)` 是参数),而我建标尺时只取了
「索引作为被调用者」的形状。补取真值后发现规律比第一版的模型更细:

```
f(A.A\n,1\n)  ->  GETTABLE:2  LOADK:3
```

**每个参数在它后面那个分隔符被消费后各自 discharge**,取当时的 lastline——第一个参数在 `,`(第 2
行),第二个在 `)`(第 3 行)。不是整个参数列表统一用右括号行。

这也解释了为什么第一版给 `LocalStmt` 用整条语句的 `p.lastLine` 恰好是对的:`local` 语句的最后一个
token 就是最后一个初始化式的结尾。多参数调用才暴露出「每个元素各有自己的行」。

### 6. 最终修法:per-expression end line

落点是 `parseExprList`,它被**五处**共用(调用参数 / `local` 初始化 / 赋值右侧 / `return` / 泛型
for)。新增 `parseExprListEnds` 返回每个元素的物化行,`parseExprList` 保留为薄包装:

- 每个元素的 end line = 它后面那个 `,` 被消费后的 `p.lastLine`;
- **最后一个元素的 end line 在这里未知**——取决于闭合构造的那个 token(调用参数的 `)`、泛型 for 的
  `do`、`return` 则什么都没有),留 0 由调用方填。
- `adjustExprList` / `compileArgList` 各加一个 `ends []int32` 参数,`endAt(i)` 在缺失时回退到原来的
  行,所以只有真正需要的调用点要改。

另外补了一格:**被括号包住的被调用者**。`(0\n)(A.A)` 的 LOADK 要落第 2 行,因为 `)` 是被消费的
token、把 lastline 推到了 2;而 `t.x\n{1}` 的 GETTABLE 仍是第 1 行,因为 `{` 在索引被 discharge 时
还没被扫描。为此加了 `calleeEndLine`,只对 `ParenExpr` 形状的被调用者生效。

### 7. #248 那轮已经把这个缺口写下来了

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

### 教训 3:修一个 crasher 语料时,要把同族**旧**语料一起当验收门,而不只跑那一个新的

**核心断言**:第一版修法让 #252 的语料通过,`test-all`(p1/p3/p4)、`conformance-all`、
`difftest-all` 全绿——四道门全过,读起来完全像可以收工。它其实把 **#248 自己的常驻语料**改红了,只有
`make fuzz-oracle` 会跑到 `testdata/fuzz/` 里那 170 多个常驻 seed,而它排在最后。差分测试与 fuzz 常驻
语料**覆盖的输入集合不同**:前者跑自己生成的形状,后者跑历史上真实撞出来过的形状,而「历史上撞出来
过的」恰恰是同族回归最可能落在的地方。

**判据**:修一个由语料驱动的 issue 时,先 `grep` 出**同族的旧语料**(本例同族的判据很直接——#248 的
反思文档就在 `llmdoc/memory/reflections/` 里、语料哈希写在 issue 正文里),把它们和新语料一起作为改
动前后都要跑的那一组,而不是等全量验证的最后一步顺带发现。改动触及一个**被历史 issue 修过的机制**
时,这一条尤其重要:那个机制的每个历史语料都是一条已经付过代价的断言。自查办法 = 问「我这次改的是谁
当年修的东西,他留下的输入我跑了吗」。

这与 [[prove-the-path-under-test]] §9.6(说「这个差分 crasher 已经修好了」之前要在修复前的 base 上双
向验证)相邻但不同:那条讲**单个** crasher 的因果验证,本条讲**同族语料集合**的覆盖——单个 crasher
验证得再严,也不会告诉你隔壁那个语料被你改红了。

### 教训 4:记录一个已知缺口时,要附带「它在什么条件下会变得可见」

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
- **教训 3 → [[prove-the-path-under-test]]**:§9.6 相邻的新一格——同族**旧**语料要一起当验收门;
  `difftest` 全绿不覆盖 `testdata/fuzz/` 的常驻 seed,而同族回归最可能正落在那里。
- **教训 4 → [[prove-the-path-under-test]]**:§4.2「已知差异类注释必须有执行体」的时间对偶——缺口
  记录要写触发条件,否则下次撞上时认不出是旧账。
