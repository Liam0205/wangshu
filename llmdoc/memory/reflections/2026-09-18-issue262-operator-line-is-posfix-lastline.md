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
  lookahead 时的 lastline)。**第二轮独立审阅用 170 个按 `lparser.c` 非终结符列的模板、19538 个形状、
  连同嵌套 proto 一起比对,又推翻一次**:我的构造器修法在「位置项后跟 `k=v`」时把物化行覆盖成 `}` 行
  (引入的新缺陷),方括号键 `A[B\n.\nc]` 的 discharge 行仍偏(会 raise),CLOSURE / 块退出 CLOSE /
  repeat CLOSE+JMP 三族不可见残留;全部修掉,并在对齐 while 体的 CLOSE 时**发现并修了一个语义 bug**:
  while 只有一层 block、CLOSE 发在回边之后从未执行,循环体内每次迭代建的闭包共享同一个 upvalue。全范围
  终审又抓出多目标赋值局部目标的 MOVE 一格——藏在比对脚本「opcode 序列不同就不比行」的桶里,经 activelines
  可见;最终 21019 形状行号 0 差异。三条教训:「同一条口径」在每个语法位置都要问一遍「PUC 在哪一刻发射」
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
JMP(`IterLine`/`DoLine`/`Body.EndLine`,第三轮把各语句上重复的 `BodyEndLine` 并进了 `Block.EndLine`)、方法调用接收者在方法名行 discharge(PUC 的 `:` 分支先读名字
再 `luaK_self`,与 `.` 分支先 discharge 再读名字**相反**,#248 当年把两者写成同形是错的)、`return` 的
RETURN 取列表末行、VARARG 在 `...` 被消费**之前**发射、`if`/`while`/`repeat` 条件的 TEST+JMP 取条件末行、
`if` 逃逸 JMP 与 `while` 回边取 block 末行、`local` 的 LOADNIL 补位取末初始化式行、`f{...}`/`f"..."` 糖式
参数取参数末行,以及一处解析器层面的差异:构造器里 NAME/`=` 的 lookahead 已经扫过下一个 token 时,PUC 的
lastline 跟着扫描器走(`{\nA\n.x}` 里 A 的 GETGLOBAL 落在 `.x` 那一行),`Parser.next` 在消费 lookahead
时也照此推进。修完再用审阅者那 1875 个形状扫一遍:逐指令行号 0 差异,只剩 235 个 opcode 序列不同的
形状(我们对 `not (a<b)` 多发一条 NOT、对 `local a` 多发 LOADNIL,与本轮无关的既有差异)。

**④ 第二轮独立审阅又把 0 差异推翻了**:它用**约 170 个模板、按 `lparser.c` 非终结符列**(含函数体、
块退出、方括号键、repeat 带 upvalue……)、间隙插换行或注释,共 19538 个形状,而且**连同嵌套 proto 一起**
比对(我和第一轮审阅者的脚本都只看主 proto)。结果:一处阻塞——我给 R1-I1 写的 `TableItem.EndLine`
在「最后一个位置项后面还跟着 `k=v` 字段」时被无条件覆盖成 `}` 行,`{A.x\n,\ny=2\n}` PUC 报 2、我
报 4(基线报 1),是修复**引入**的新缺陷;一处会 raise 的残留——方括号键 `A[B\n.\nc]` 里键的 discharge
行(`yindex` 在 `]` 之前 `exp2val`,PUC 3 / 我 2),六个语法位置都复现;三族不会 raise 的残留——CLOSURE
及其 MOVE/GETUPVAL 伪指令(`pushclosure` 在 `end` 之后)、块退出 CLOSE(`leaveblock` 在闭合关键字之前)、
repeat 带 upvalue 的 CLOSE/JMP(在 `until` 条件之后)。这一轮把它们**全部**修掉,顺手修了 `local\na`
的 LOADNIL 行,并且在对齐 while 循环体 CLOSE 的位置时发现了一个**语义 bug**:望舒的 while 只有一层
block,CLOSE 发在回边 JMP **之后**、永远执行不到,循环体里每次迭代建的闭包共享同一个 open upvalue,
`while i<3 do local x=i fs[#fs+1]=function() return x end i=i+1 end` 三个闭包都返回同一个值——luac
是两层 block(外层可 break、内层作用域),CLOSE 在回边之前。改成两层后行为与 PUC 一致(0,1,2)。
修完用审阅者的 19538 形状(含嵌套 proto)重扫:**行号差异 0**,opcode 序列差异 513 个全部是基线上就有
的(与基线的差异集合逐条比对,HEAD 没有新增一个),另有 71 个形状基线判 ambiguous syntax、HEAD 与
luac 一致地接受(构造器里 lookahead 之后的 `f\n(...)`)。

**⑤ 第三轮独立审阅在方括号键上又抓出一格**:我给 `yindex` 那一步写的是 `dischargeVars`,而 PUC 调的是
`luaK_exp2val`——两者只在键**带 t/f 跳转链**时不同(`A[B or true\n]`、`A[B and 1\n]`):`exp2val` 会把它
放进寄存器,LOADBOOL/LOADK 落在键末行;`dischargeVars` 什么都不发,推到 `]`/`=` 行。基线反而是对的
(一次 `exp2RK` 在键起始行,恰好等于键末行),是我把发射点拆成两段后引入的。改用本仓已有的 `exp2Val`
(语义与 PUC 逐字对应),加四行 pin,并把扫描模板里方括号键从裸比较扩到 `and`/`or` 收尾,20154 形状
重扫仍 0 差异。值得记的是**这一格 19538 个形状都没扫到**:模板里方括号键只有 `B . c`、`B == C`、
`f ( )` 三种,没有短路链——扫描面「按非终结符列」还要加一句「每个非终结符的**每个产生式分支**都要有
模板」,`exp` 的 and/or 分支就是这里漏的。

**⑥ 全范围终审再抓一格,而且暴露了比对脚本本身的一个盲桶**:多目标赋值里**局部变量目标**的 MOVE 还用
语句首行(`local a, b\na, b = 1,\n2` PUC 两条 MOVE 都在 3,我们在 2)。漏的原因有两层:三套模板里没有
一个「局部变量作为多目标赋值目标」的形状(`local a , b = 1 , 2` 是声明,走 `stmtLocal`,不走这条路径);
更要紧的是**比对脚本把 opcode 序列不同的形状直接归入 OPSEQ 桶、不再比行号**——这类形状我们多发一条
LOADNIL,所以即使模板里有它也会掉进那个桶。终审者把两边按 opcode 名对齐后再比行,加上「每个 token 独占
一行」的第四遍扫描,20 个形状全是这一个根因。MOVE 不会 raise,但 `debug.getinfo(f, "L").activelines`
把它暴露给脚本(lua5.1 `2,4,5` / 我们 `2,3,4,5`)——正是教训 2 里写的第二条可见通道,这次是它第一次
真的抓到东西。修法一行(`s.Line` → `storeLine`),加三行 LineInfo pin 与一条 activelines e2e;比对脚本
改为「对两边都发出、次数相同的每种 opcode 比较行号多重集」(difflib 对齐在 luac 把常量折进 SETTABLE 的
RK 时会错位,所以按 opcode 分桶比多重集),模板补七条局部目标赋值。带这两处改动重扫:21019 形状行号
0 差异、按 opcode 比行 0 差异;opcode 序列差异比基线模板集多出的部分全部来自新模板,望舒自身的 opcode
序列相对基线只在 while 两层 block 与 `{ f<nl>(3) }` 接受两族上不同,都已在本轮记录。

这里最值得记的不是又修了几格,而是**扫描面的定义**:第一轮我手挑 90 个,第一轮审阅者 31 个模板 1875
个形状,第二轮审阅者 170 个模板 19538 个形状 + 嵌套 proto,终审者再加「每 token 独占一行」与「OPSEQ 桶
也比行」——每放大一次都有新发现,而且第二轮发现的包括一个真实的运行时语义 bug。「按非终结符列模板」
这句话在我自己写反思时已经写下,却没有在自己的扫描里做到;审阅者做到了。扫描面除了「模板够不够」还有
「脚本有没有哪个桶是不比的」这一维。

### 6. 验证

- 四张编译期 LineInfo 表(运算符 / store / 数值 for 表头 / 其余语句发射点,共 64 行)与 21 条 e2e 消息
  + 1 条 activelines 用例 + 3 条 while 闭包语义用例,全部在 master 上失败、在分支上通过(对照用
  `git worktree` 建的 master 检出,不用 stash);e2e 期望值逐条与 `lua5.1` 二进制输出比对过。第二轮
  审阅者的模板加上短路键与局部目标赋值共 21019 个形状(含嵌套 proto)在最终树上逐指令行号 0 差异,
  opcode 序列不同的形状按 opcode 比行号多重集也 0 差异;望舒自身的 opcode 序列相对基线只在 while 两层
  block 与 `{ f<nl>(3) }` 接受两族上不同。
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
第一轮独立审阅用**模板 × 换行位置组合**生成 1875 个形状,又扫出泛型 for 生成器与构造器键两个我没挑到的
位置;第二轮独立审阅把模板按 `lparser.c` 非终结符补到 170 个、**连嵌套 proto 一起比**,19538 个形状,
又扫出我修复引入的一处、一处会 raise 的残留、三族不可见残留,以及一个 while 闭包捕获的运行时语义 bug。
扫描面每放大一次都有新发现,直到按语法覆盖为止。判据见下。

**判据**:对齐工作的对象是一张**表**(所有语法位置 × 所有发射点)而不是一个 seed。当参照实现能被本地
调用(`luac5.1`、`lua5.1`、cgo oracle)时,先写 dump 比对脚本、**用「每个非终结符一个模板 × 在每个 token
间隙插换行或注释(取到两处)」的组合枚举**生成形状(两万个也只要几分钟),**嵌套 proto 一起比**,**opcode
序列不同的形状也要比行号**(对两边都发出、次数相同的每种 opcode 比行号多重集;终审那格就藏在「序列不同、
不比行」的桶里),再按 DIFF 分类修;seed 只是告诉你「这张表里有红格」。模板集合按 `lparser.c` 的非终结符逐个列(statement 各
种、`body`、`simpleexp` 各分支、`primaryexp` 各后缀、`constructor` 各字段形式、`funcargs` 三种、带 upvalue
的块退出),不按「我想到了哪些」列——本轮我写下这条判据的同时自己没做到,第二轮审阅者做到了,差距是
1875 对 19538、以及一个运行时语义 bug。自查办法:修完一个 seed 后问「同一条口径还作用于哪些语法位置,我
扫过了吗」——答案是一份枚举生成的清单加每格红绿,而不是「应该没了」;清单如果是手写的,再问「有哪个
非终结符没有模板」。这是 [[cross-backend-semantic-fix-sweep]] 同族扫描纪律在「参照实现可本地调用」这个
条件下的加强版:能批量比对时就不要逐格猜,能枚举时就不要手挑,能比嵌套 proto 就不要只看主 proto。

## Promotion 决策

- **教训 1 → [[prove-the-path-under-test]] §2.1b**:§2.1a「用例要区分两个模型」的推广——候选模型可能
  有三个以上(起始行 / 运算符行 / posfix 行),每个语法位置的每条指令各自一格。
- **教训 2 → [[prove-the-path-under-test]] §4.2a**:「不可见」判断的作用域是指令不是路径;activelines
  是第二个可见通道。
- **教训 3 → [[cross-backend-semantic-fix-sweep]]**:参照实现可本地调用时,用 dump 比对表批量扫全部
  语法位置,seed 只是入口。
- doc-gaps 里「前端行号记账模型」那条:全部残留修掉,21019 形状(含嵌套 proto、含局部目标赋值模板)dump
  比对逐指令 0 差异、OPSEQ 桶按 opcode 比行也 0 差异;条目改写为「已对齐」,并写明扫描面的定义(模板按
  非终结符列、含嵌套 proto、OPSEQ 桶也比行),供下次前端行号改动当验收门。
