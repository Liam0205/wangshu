---
name: 2026-09-18-issue262-operator-line-is-posfix-lastline
description: >
  nightly 开出的 issue #262(p4 `FuzzOracleDiffTiered`,seed
  `print(pcall(function() error("boom"%<CR>0) end))`,分支 `fix/issue262-operator-lastline`)。**标题写
  「go-fuzz crash」但不是崩溃**:两边都正确抛 `attempt to perform arithmetic on a string value`,只有
  行号不同(PUC `:2:` / 望舒 `:1:`)。seed 里的换行是**裸 `\r`**,先怀疑词法器,探针一跑发现 `\n`
  同样复现——词法器四种换行都算对了,缺陷在 codegen。**版本核对干净**:失败 run 的 headSha 就是当前
  master `6e1ce70`。根因是 #252 那套「行号 = 发射那一刻的 lastline」的口径在**运算符**这一格没有落
  到位:PUC 对一个二元运算有**两个**发射点——`luaK_infix` 在运算符 token 刚被消费时跑(左操作数在
  运算符行物化),`luaK_posfix` 在右操作数解析完之后跑(右操作数物化、运算指令本身、比较的 JMP、
  CONCAT 全部落在**右操作数最后一个 token 的行**);一元运算只有 `luaK_prefix` 这一个点,同样在操作数
  之后。望舒的 `exprBin`/`exprCompare`/`exprConcat`/`exprUn` 全用运算符行 `e.Line`,只在「右操作数与
  运算符同行」时碰巧正确——而 #252 那张表里恰好只有这种形状(`A.x\n\n+1` 的 `+` 与 `1` 同在第 3 行),
  所以当时全绿。修法:`BinExpr`/`UnExpr` 加 `EndLine`(解析完操作数后取 `p.lastLine`),posfix/prefix
  时发射的一律用它,infix 时发射的保留 `Line`。**用 `luac5.1 -p -l` 对 90 多个形状扫一遍**又扫出两格
  同族残留,都是 doc-gaps 里挂着的「不会 raise 所以不可见」项:① `storeVar` 的 store 行——SETTABLE
  **会** raise(对 nil 对象索引、或 `__newindex` 里 `error(msg, 2)`),`x.y = 1\n+\n1` PUC 报 3 我们报 1;
  ② 数值 for 的表头——FORPREP 在 `do` 之后发射,`'for' initial value must be a number` 在 `do` 那行报,
  `for i = "x", 2\ndo end` PUC 报 2 我们报 1。两格一并修掉。**独立审阅用 1875 个组合枚举形状又扫出两处我判为
  「不可见」其实会 raise 的**:泛型 for 的 TFORLOOP(`for k in\nnil` PUC 2 / 我们 1)与构造器 `[k]=v` 的
  SETTABLE(`{\n[nil]\n=\n1}` PUC 4 / 我们 1);于是把剩余 DIFF 全部修掉(构造器各字段行、泛型 for 三条
  跳转、方法调用接收者、RETURN、VARARG、条件 TEST/JMP、`if`/`while` 跳转、LOADNIL 补位、糖式参数、构造器
  lookahead 时的 lastline),最终 1875 形状逐指令行号 0 差异。三条教训:「同一条口径」在每个语法位置都要问一遍「PUC 在哪一刻发射」
  (→ [[prove-the-path-under-test]] §2.1b)/ 「不会 raise 所以不可见」这个判断要按指令逐条核,SETTABLE
  与 FORPREP 都会 raise(→ [[prove-the-path-under-test]] §4.2a)/ 有参照实现的行为对齐,用 dump 表
  批量扫全部语法位置比按 issue 逐格补便宜得多(→ [[cross-backend-semantic-fix-sweep]])。
metadata:
  type: reflection
  date: 2026-09-18
---

# 运算指令的行是 posfix 那一刻的 lastline,不是运算符的行(2026-09-18,issue #262)

> 范围:issue #262(nightly `FuzzOracleDiffTiered` 开出),分支 `fix/issue262-operator-lastline`。改动落在
> `internal/frontend/ast/ast.go`(`BinExpr`/`UnExpr` 加 `EndLine`,`NumForStmt` 加 `ExprEndLines`/`DoLine`)、
> `internal/frontend/parse/expr.go` 与 `stmt.go`(填 `p.lastLine`)、
> `internal/frontend/compile/codegen.go`(`exprBin`/`exprCompare`/`exprConcat`/`exprUn` 改用 `EndLine`)、
> `internal/frontend/compile/stmt.go`(`storeVar` 的 store 行改为语句末行;`stmtNumFor` 表头逐项取行)。
> 新测试 `internal/frontend/compile/issue262_operator_line_test.go`(三张 LineInfo 表,期望值取自
> `luac5.1 -p -l`)、`test/regression/fuzz_262_test.go`(12 条 pcall 消息含行号,逐条与 `lua5.1` 二进制
> 比对过),语料入 `test/fuzz/testdata/fuzz/FuzzOracleDiffTiered/6508df401c98220d`。

## 任务

nightly 报的 seed(标题 `go-fuzz crash (p4)`),原文里 `%` 和 `0` 之间是一个裸回车符:

```lua
print(pcall(function() error("boom"%\r0) end))
```

```
oracle: "false\t[string \"fuzz\"]:2: attempt to perform arithmetic on a string value\n"
tiered: "false\t[string \"fuzz\"]:1: attempt to perform arithmetic on a string value\n"
```

## 过程

### 1. 版本核对与分诊

失败 run 的 headSha 就是当前 master,没有「已修复代码」这条便宜解释;当前 HEAD 精确重放 0.01 秒复现。
只有这一个新 seed,不需要去重分组。

### 2. 先排除词法器

seed 的换行是 `\r`,第一反应是词法器把 `\r` 数错了。写了一个把 PUC 与望舒并排跑的探针,喂进 `\n`、`\r`、
`\r\n`、`\n\r`、`\r\r` 五种变体——**全都**报 `:1:`,与换行形式无关;而 `local x = 1\rlocal y = "a" + 1`
两边都报 2,说明词法器本身把 `\r` 算作一行没错。缺陷在 codegen:`%` 后面换行时,MOD 指令的行取的是 `%`
那一行,PUC 取的是 `0` 那一行。

顺带发现一处词法器差异(长字符串里的裸 `\r` 我们保留 13、PUC 归一化成 10),但那是另一个问题(它让
`s:byte()` 的输出不同,fuzz 早晚会开一个独立 issue),没有混进本轮。

### 3. 根因:一个二元运算有两个发射点

`lparser.c` 的 `subexpr`:

```c
luaX_next(ls);                       /* 消费运算符 */
luaK_infix(ls->fs, op, v);           /* 左操作数在这里物化:lastline = 运算符行 */
nextop = subexpr(ls, &v2, priority[op].right);
luaK_posfix(ls->fs, op, v, &v2);     /* 运算指令在这里发射:lastline = 右操作数末 token 行 */
```

`luaK_infix` 对算术运算只在左操作数**不是**数字字面量时把它 `exp2RK`(数字字面量推迟到 posfix 里的
`codearith` 才物化);对比较运算无条件 `exp2RK`;对 `and`/`or` 发 TEST+JMP;对 `..` 把左操作数
`exp2nextreg`。`luaK_posfix` 里 `codearith`/`codecomp` 物化右操作数并发射运算指令(比较还发一条 JMP),
CONCAT 也在这里。一元运算只有 `luaK_prefix`,在操作数解析完之后。

望舒 `exprBin`/`exprCompare`/`exprConcat`/`exprUn` 里所有 `emitABC`/`exp2RK` 都传 `e.Line`——运算符行。
两者只在右操作数与运算符同行时一致。**#252 那张表里的 `local v = A.x\n\n+1` 恰好就是这种形状**(`+`
与 `1` 同在第 3 行),所以它作为「算术运算是 discharge 点」的证据是对的,却没有把运算符行与 posfix 行
区分开。

### 4. 修法:`EndLine`,infix 用 `Line`、posfix 用 `EndLine`

`BinExpr`/`UnExpr` 加 `EndLine`,解析器在操作数解析完、下一个 token 还没被消费时取 `p.lastLine`(与
`luaX_next` 的顺序一致:lookahead 已扫描但未消费,不推进 lastline)。codegen 里:

- 算术:左操作数(非数字字面量)在 `Line` 物化;右操作数、推迟的数字字面量左操作数、运算指令在 `EndLine`;
- 比较:左操作数在 `Line`;右操作数、比较指令、JMP 在 `EndLine`;
- `and`/`or`:TEST+JMP 在 `Line`;右操作数 discharge 在 `EndLine`;
- `..`:链上每个操作数由**跟在它后面**的那个 `..` 的 infix 推入,取那个运算符的行;最后一个操作数和
  CONCAT 在最内层 posfix,取 `EndLine`(`1\n..\n2\n..\nA.x` 给 LOADK 2、LOADK 4、GETTABLE 5、CONCAT 5);
- 一元:操作数物化与 UNM/NOT/LEN 都在 `EndLine`。

### 5. 用 dump 表批量扫,扫出两格同族残留

写了一个把望舒 LineInfo 与 `luac5.1 -p -l` 并排打印、逐指令比对的脚本,喂了 90 多个跨行形状(运算符
全家、语句各位置、控制流)。修完运算符之后剩下的 DIFF 分三类:

**① 单目标赋值的 store 行**(`x.y = 1\n+\n1` SETTABLE luac 3 / 我们 1)。这是 #248 记、#252 又记的
「已知缺口」,两次都以「SETGLOBAL/SETUPVAL/MOVE 不会 raise」判为不可见。**这个判断漏了 SETTABLE**:
对 nil 对象做 SETTABLE 会 raise,`__newindex` 里 `error(msg, 2)` 也会把行号指到 store 那条指令。
oracle 探针证实 PUC 报 3、我们报 1,是用户可见差分。多目标路径早就用 `AssignStmt.EndLine`,只是快速
路径没跟上;改 `storeVar` 的 `line` 参数为 `storeLine`,#248 的 `A\n.x = 1` 仍是 2(语句也在 2 结束)。

**② 数值 for 表头**(`for i = "x", 2\ndo end` FORPREP luac 2 / 我们 1)。doc-gaps 里也挂着,写的是
「数值 for 的 FORPREP/LOADK 行」,没写可见条件。FORPREP 会 raise(`'for' initial value must be a
number` 三连),而它在 `checknext(TK_DO)` **之后**发射,所以报 `do` 那一行。`NumForStmt` 加
`ExprEndLines[3]`(每个表达式解析完立刻取 lastline,`exp1` 就地物化)与 `DoLine`;默认 step 的 LOADK
跟 limit 同行;FORLOOP 保留 `for` 行(`luaK_fixline`)。

**③ 我第一版判为「确认不可见」的那一堆,被独立审阅推翻了两处**:第一版把剩下的 DIFF(`{}` 的
NEWTABLE、`while`/`if`/泛型 for 的 JMP、`local a, b\n= 1` 的 LOADNIL)一并归为「不会 raise、只有
activelines 能看见」,就收工了。blind reviewer 用同一套 dump 比对法自己扫了 31 个模板 × 最多两处换行
共 1875 个形状,在**我没扫到的模板**上找到两处会 raise 的:泛型 for 的 **TFORLOOP**(`forlist` 在
`checknext(TK_IN)` 之后读 `line`,`forbody` 用 `luaK_fixline` 盖上去;生成器不可调用时在它上报错——
`for k in\nnil do end` PUC 2 / 我们 1)和表构造器 `[k]=v` 字段的 **SETTABLE**(`recfield` 在值解析完
之后发射;键为 nil/NaN 时在它上报 `table index is nil`——`{\n[nil]\n=\n1}` PUC 4 / 我们 1)。也就是
说我自己的「扫过了」是假的:我的清单只有 60 多个手挑形状,漏掉了泛型 for 生成器和构造器键这两个位置。

于是这一轮把剩下的 DIFF **全部**修掉而不是分类:表构造器每个字段的行(`TableItem` 加 `KeyEndLine`/
`EndLine`,NEWTABLE 用 `{` 前一个 token 的行 `NewTableLine`)、泛型 for 的 TFORLOOP / 前向 JMP / 回边
JMP(`IterLine`/`DoLine`/`BodyEndLine`)、方法调用接收者在方法名行 discharge(PUC 的 `:` 分支先读名字
再 `luaK_self`,与 `.` 分支先 discharge 再读名字**相反**,#248 当年把两者写成同形是错的)、`return` 的
RETURN 取列表末行、VARARG 在 `...` 被消费**之前**发射、`if`/`while`/`repeat` 条件的 TEST+JMP 取条件末行、
`if` 逃逸 JMP 与 `while` 回边取 block 末行、`local` 的 LOADNIL 补位取末初始化式行、`f{...}`/`f"..."` 糖式
参数取参数末行,以及一处解析器层面的差异:构造器里 NAME/`=` 的 lookahead 已经扫过下一个 token 时,PUC 的
lastline 跟着扫描器走(`{\nA\n.x}` 里 A 的 GETGLOBAL 落在 `.x` 那一行),`Parser.next` 在消费 lookahead
时也照此推进。修完再用审阅者那 1875 个形状扫一遍:**逐指令行号 0 差异**,只剩 235 个 opcode 序列不同的
形状(我们对 `not (a<b)` 多发一条 NOT、对 `local a` 多发 LOADNIL,与本轮无关的既有差异)。

### 6. 验证

- 四张编译期 LineInfo 表(运算符 / store / 数值 for 表头 / 其余语句发射点,共 48 行)与 18 条 e2e 消息
  全部在 master 上失败、在分支上通过(对照用 `git worktree` 建的 master 检出,不用 stash);e2e 期望值
  逐条与 `lua5.1` 二进制输出比对过。审阅者的 1875 形状 dump 比对在最终树上逐指令行号 0 差异。
- 171 条常驻 oracle 语料(含 #248 `8dff36b8bd115962`、#252 `612767150dcb06d5`)全绿;
  `FuzzOracleDiffTiered` 60 秒 smoke 无新发现。
- gofmt / vet / lint / p1 p3 p4 全量测试 / conformance-all / difftest-all / 官方 Lua 套件 p1 p3 p4 全绿。

## 教训

### 教训 1:同一条口径,要在每个语法位置各问一遍「PUC 在哪一刻发射」

**核心断言**:#252 把口径定为「行号 = 发射那一刻的 lastline」是对的,但「那一刻」在不同语法位置对应
不同的 token。一个二元运算有**两个**发射点(infix / posfix),左右操作数分属不同时刻;`local` 初始化
在分隔符;store 在语句末;FORPREP 在 `do`。口径正确不等于每一格都落到位——#252 那张表用的形状恰好
让「运算符行」与「posfix 行」重合,于是运算符这一格看起来已经对了。

**判据**:对齐参照实现的行号时,对每个 AST 节点类型逐个回到 `lparser.c`/`lcode.c`,找出它发射的
**每一条**指令是在哪个 `luaX_next` 之后,然后为每条指令构造一个让「运算符行」「起始行」「posfix 行」
三者两两不同的输入(至少三行)。自查办法:测试表里的每个形状,数一数它让几个候选行不同——只让两个
不同的形状,对第三个候选没有区分力(这是 [[prove-the-path-under-test]] §2.1a 的推广:候选模型可能不止
两个)。

### 教训 2:「这条指令不会 raise 所以行号错了也看不见」要逐条指令核,而且 raise 不是唯一可见通道

**核心断言**:#248 与 #252 两轮都把 store 行记为不可见缺口,依据是「SETGLOBAL/SETUPVAL/MOVE 不会
raise」。这句话对那三条指令成立,但 `storeVar` 还发 SETTABLE,它会;doc-gaps 里的「数值 for 表头」
一项则连「为什么不可见」都没写,而 FORPREP 会 raise 三种错。一个「不可见」判断的作用域是**指令**,
不是代码路径——同一个函数发的几条指令,能不能 raise 各不相同。**而且本轮我自己又犯了一次**:第一版把
泛型 for 的「JMP」判为不可见时,没有把同一语句的 TFORLOOP 单独拿出来问(它会 raise),把构造器的
SETLIST 判为不可见时也没有把 `[k]=v` 的 SETTABLE 单独拿出来问——独立审阅用更大的扫描面把这两条抓了
出来。判据在下面,但真正的教训是:「不可见」清单要按**指令**列,不是按语句列。

**判据**:写「行号偏了但不可见」之前,列出这条路径发射的**每一种**指令,逐条回答「它能不能 raise」
(`lvm.c` 里对它有没有 `luaG_*error` 调用),再问第二个可见通道:`debug.getinfo(f, "L").activelines`
会把 LineInfo 的键集合原样暴露给脚本,任何行号偏移在那里都可见,只是 fuzz 语料目前很少走到。自查
办法:「不可见」的记录必须带上「哪条指令 + 为什么这条不会 raise + activelines 为什么没覆盖」三项,
缺一项就还没判定。这是 [[prove-the-path-under-test]] §4.2 / #252 教训 5「缺口要写触发条件」的下一格:
那条讲缺口记录要写**何时**变可见,本条讲「不可见」这个前提本身要按指令逐条证。

### 教训 3:有参照实现的行为对齐,批量 dump 比对比按 issue 逐格补便宜一个量级

**核心断言**:#248、#252、#262 三轮各修一到四格,每轮都由 nightly 撞出一个 seed 才开始,每轮的独立
审计或远端评审又各补一到两格。本轮花十几分钟写了一个「`luac5.1 -p -l` 与望舒 LineInfo 并排逐指令
比对」的脚本,喂 90 多个手挑形状,一次扫出运算符全家 + store 行 + 数值 for 三族。但手挑就是手挑:
独立审阅用**模板 × 换行位置组合**生成 1875 个形状,又扫出泛型 for 生成器与构造器键两个我没挑到的
位置。扫描面要用组合枚举而不是手挑,判据见下。

**判据**:对齐工作的对象是一张**表**(所有语法位置 × 所有发射点)而不是一个 seed。当参照实现能被本地
调用(`luac5.1`、`lua5.1`、cgo oracle)时,先写 dump 比对脚本、**用「每种语句/表达式一个模板 × 在每个
token 间隙插换行(取到两处)」的组合枚举**生成形状(1875 个只要几秒),再按 DIFF 分类修;seed 只是告诉
你「这张表里有红格」。模板集合按 `lparser.c` 的非终结符列(statement / simpleexp / primaryexp / 
constructor / funcargs 各一个),不按「我想到了哪些」列。自查办法:修完一个 seed 后问「同一条口径还
作用于哪些语法位置,我扫过了吗」——答案是一份枚举生成的清单加每格红绿,而不是「应该没了」。这是
[[cross-backend-semantic-fix-sweep]] 同族扫描纪律在「参照实现可本地调用」这个条件下的加强版:能批量
比对时就不要逐格猜,能枚举时就不要手挑。

## Promotion 决策

- **教训 1 → [[prove-the-path-under-test]] §2.1b**:§2.1a「用例要区分两个模型」的推广——候选模型可能
  有三个以上(起始行 / 运算符行 / posfix 行),每个语法位置的每条指令各自一格。
- **教训 2 → [[prove-the-path-under-test]] §4.2a**:「不可见」判断的作用域是指令不是路径;activelines
  是第二个可见通道。
- **教训 3 → [[cross-backend-semantic-fix-sweep]]**:参照实现可本地调用时,用 dump 比对表批量扫全部
  语法位置,seed 只是入口。
- doc-gaps 里「前端行号记账模型」那条:三项残留全部修掉,1875 形状 dump 比对逐指令 0 差异;条目改写
  为「已对齐」,并把 dump 比对脚本的做法写进去,供下次前端行号改动当验收门。
