---
name: 2026-08-02-issue212-219-fuzz-crasher-batch
description: >
  八个 nightly 自动开的 fuzz crasher issue(#212–#219)的处理轮,分支
  `fix/212-219-fuzz-crashers`,1 个 commit。**先做版本核对**:八个 run 全在 `5383aec` 上,但与上一轮
  (#209)不同——这次**不是过期 issue**,五个在当前 master 上如实复现,是真缺陷。八个 issue 只有
  **六个不同的 seed**(#212/#215 同一个 hash `1d42242f157c9754`,#217/#219 同一个
  `0150a245c776b8ed`)。三个真缺陷:① `error()` 的 level 参数没做类型检查——代码写成
  「转换成功才用,失败静默取默认值 1」,而 PUC 的 `luaL_optint` 对**显式传了一个转不动的值**是抬错的,
  只有缺省或显式 nil 才取默认值,数字字符串仍强制转换(#212/#213/#215);② 调用的行号取的是**被调用
  表达式**的起始行而不是**参数列表**那一行,`(0\n)()` lua5.1 报第 2 行、望舒报第 1 行,只有跨行的被
  调用表达式才有差别,所以一直没被发现(#214);③ `string.gsub` 的 repl 类型检查写在替换循环**里面**,
  而第 4 个参数把循环次数压成 0,于是那个非法参数从来没被看到、`gsub("", "", nil, .0)` 成功了,PUC 是
  在循环**之前**用 `luaL_argcheck` 校验的(#216)。两个非缺陷:④ `math.mod` 是 harness 的**别名漏网**
  ——`mathFn2` 报第一个缺失参数是刻意决定(两个官方构建互相不一致,C 不规定实参求值顺序),但
  `__wrapArgOrder` 只包了 `math.fmod`、没包它的 `LUA_COMPAT_MOD` 别名 `math.mod`,于是同一写法被报了
  两次,`math.atan2` 也从来没被包过(#217/#219);⑤ #218 是重 workload 进了 corpus 的问题(一亿次迭代、
  1.8 秒,是 p4 corpus 里最重的一个、约 6 倍),按 triage guide 走显式回归测试。四条教训:版本核对只
  回答「要不要在这个 commit 上复现一遍」不回答「有没有缺陷」/ 一个刻意的豁免判据必须覆盖被豁免函数的
  所有别名 / 参照实现在循环之前做的校验不能挪到循环里面 / 把 fuzz seed 转成回归测试时要连同 harness
  的边界条件一起复现(我第一版没抄 `SetStepBudget`,1.8 秒变成 87 秒)。
metadata:
  type: reflection
  date: 2026-08-02
---

# 八个 crasher、六个 seed、三个真缺陷(2026-08-02,分支 `fix/212-219-fuzz-crashers`)

> 范围:#212–#219 八个 issue,1 个 commit(`cdb09ce`)。产品侧改动落在
> `internal/stdlib/stdlib.go`(`error` 的 level)、`internal/frontend/parse/expr.go`(调用的行号)、
> `internal/stdlib/stringlib.go`(gsub 的 repl 前置校验);harness 侧落在
> `internal/oracle/prelude.go`(`__wrapArgOrder` 扩到 math 表里所有取两个数的入口);测试落在
> `fuzz_212_219_test.go` 与 `test/regression/p4_hot_loop_promote_test.go`;五个 seed 入
> `testdata/fuzz/FuzzOracleDiff/`。

## 任务

nightly 在八个不同的夜晚自动开了八个 crasher issue(#212–#219)。

## 本轮做了什么

### 0. 先做版本核对,但这一次的结论与上一轮相反

按 [[unreproducible-crasher-triage]]「第一步永远是版本核对(不是复现矩阵)」先核版本:八个 run 的
headSha 全部是 `5383aec`。上一轮(#209)的八个字面事实几乎一样——同样全在一个旧 commit 上——而那一轮
的结论是第一档:修法已经在仓库里,issue 只是慢了一步,一行代码没改。

**这一轮不是。** 在当前 master 上逐条重放之后,五个 seed 如实 FAIL:三个是真产品缺陷,一个是 harness
的豁免判据漏了别名,一个是 corpus 里的重 workload。也就是说版本核对给出的那个「三档」是关于**要不要
在这个 commit 上复现一遍**的分类,它本身不回答**有没有缺陷**——同一个 commit 上的八个 run 既可以全部
是过期的,也可以全部是活的。

**另一个便宜的检查同样有收益**:按同一篇 guide「一批 crasher 先问会不会被同一个改动一起解决」,八个
issue 只有**六个不同的 seed**——#212 与 #215 是同一个 hash `1d42242f157c9754`,#217 与 #219 是同一个
`0150a245c776b8ed`。nightly 按 run 开 issue,同一个输入在两个夜晚各被最小化到同一个 hash,就会开出
两个 issue。先算 hash 再分组,比逐条查根因便宜得多。

### 1. `error()` 的 level 参数没做类型检查(#212 / #213 / #215)

`error("", 0>0)` 在望舒里报的是**空消息**,lua5.1 报
`bad argument #2 to 'error' (number expected, got boolean)`。

根因在 `internal/stdlib/stdlib.go::baseFnError`:

```go
if len(args) >= 2 {
    if f, ok := toNumberStr(st, args[1]); ok {
        level = int(cCharCastInt32(f)) // luaL_optint narrowing
    }
}
```

`ok == false` 时**静默保留默认值 1**,而 PUC 的 `luaL_optint` 在这种情况下是**抬错**的。`luaL_opt*`
的规则要拆成两件事:

- **缺省 / 显式 nil** → 取默认值(这一条原来是对的,而且必须保住:`error("m")` 与 `error("m", nil)`
  都该按 level 1 加位置前缀);
- **显式传了一个转不动的值** → 立刻抬 `bad argument #2 (number expected, got X)`。

改法是把这两件事分开:`len(args) >= 2 && args[1] != value.Nil` 才走检查,检查失败返回
`crescent.NewArgError(2, ...)`。数字字符串(`"2"`)仍然被强制转换,因为 `luaL_optint` 内部走
`luaL_checkinteger`,它接受数字字符串。

**一个细节我第一版写错了**:同一个参数错误在 Lua 函数**内部**抬出时带位置前缀
(`[string "test"]:1: bad argument #2 to 'error' ...`),而经 `pcall(error, "m", {})` 直接调用时
**不带**、函数名也退化成 `'?'`。我原以为两种写法都不带前缀,是去查 lua5.1 才改对的。这两条现在都在
`fuzz_212_219_test.go::TestErrorLevelMustBeANumber` 里各有一个用例。

### 2. 调用的行号取的是被调用表达式那一行,不是参数列表那一行(#214)

```lua
(0
)()
```

lua5.1 报第 2 行(`()` 所在的那一行),望舒报第 1 行。`internal/frontend/parse/expr.go` 里写的是
`ast.CallExpr{Line: e.Pos()}`——用**被调用表达式的起始行**,而 PUC 记录的是**参数列表开始的那一行**。
改成在 `parseArgs` 之前取 `p.tok.Line`。

**只有跨行的被调用表达式才有差别**,单行时两者相同,所以这个偏差一直没被发现——这也解释了为什么它得
靠 fuzz 才撞出来:手写代码几乎不会把被调用表达式跨行写。方法调用(`MethodCallExpr`)本来就用方法名
那一行,与 PUC 一致,一并在测试里固定下来防回退。

### 3. `string.gsub` 的 repl 类型是惰性校验的(#216)

`gsub("", "", nil, .0)` 在望舒里**成功了**,lua5.1 抬
`bad argument #3 (string/function/table expected)`。

根因不是「少了一个检查」,而是**检查放错了位置**:repl 的类型检查写在替换循环**里面**(每次要替换时
才判断怎么用 repl),而第 4 个参数把循环次数压成 0,于是循环一次都没跑、那个非法参数从来没有被看到。
PUC 的 `str_gsub` 是在循环**之前**用 `luaL_argcheck(tr)` 校验的。

**是第 4 个参数让这条路径可达的。** 没有它,`gsub("", "", nil)` 会走进循环、在第一次替换时报错,行为
碰巧正确;有了它,同一段代码就静默成功。改法是在 `internal/stdlib/stringlib.go::stringFnGsub` 里把
类型检查提到循环之前(string / table / function 三种,加上「number 按 string 收」)。四种合法 repl
(串 / 函数 / 表 / 数字)也一并写了用例,防止提前校验把某一种合法写法挡在外面。

### 4. `math.mod` 不是产品缺陷,是 harness 的别名漏网(#217 / #219)

`mathFn2` 报「第一个缺失的参数」是**刻意的、有注释记录的**决定:两个官方构建**互相不一致**——x86-64
报 `#2`、arm64 报 `#1`,因为 C 不规定
`f(luaL_checknumber(L,1), luaL_checknumber(L,2))` 里两个实参的求值顺序。既然两个参照实现自己都不一致,
就没有可对齐的对象,所以望舒取「报第一个缺失 / 出错的参数」这个可辩护的行为,harness 侧有一个
`__wrapArgOrder` 判据把「多于一个坏参数」那种编号取决于顺序的写法跳掉(这一格记在
[[cross-backend-semantic-fix-sweep]]「先分清有定义还是 UB」表的第三格)。

**但那个判据只包了 `math.fmod`,没包 `math.mod`。** `math.mod` 是 `LUA_COMPAT_MOD` 下**同一个 C 函数**
的 5.0 别名,所以完全相同的写法仍然可报,而且被 fuzzer 报了**两次**(#217、#219 是同一个 seed hash)。
`math.atan2` 也从来没被包过。现在按「math 表里所有取两个数的入口」全部包上:`fmod` / `mod` / `pow` /
`ldexp` / `atan2`。

### 5. #218 是 p4 层,也不是缺陷,是重 workload 进了 corpus

seed 是 `function k()for B=0,100001000 do A=0 cA=0 A=C A=0 end end k()`——一亿次迭代。它**既不崩也不
分歧**,只是 p4 corpus 里最重的一个 seed(1.8 秒,而其余每个都在 0.3 秒以内,约 6 倍),而 coordinator
启动时会并行重放整个 corpus。按 [[unreproducible-crasher-triage]]「入库位置的取舍」:轻量、budget 有界
的写法入 `testdata/fuzz/` 常驻;**重 workload 走显式回归测试**。这与 #123、#166 是同一个处置。

**这里有个我第一版做错的地方。** 我写的回归测试**跑到循环结束**,耗时 **87 秒**——是 harness 的 50 倍。
原因是 harness 用 `SetStepBudget(1 << 20)` 把它界住了(所以那 1.8 秒并不是一亿次迭代的代价,而是
一百万步预算的代价),而我只抄了 seed 的源码、没抄那个 budget。改成镜像 `fuzz_p4_test.go` 的 step
budget 与 arena cap 之后是 1.82 秒,与 harness 一致。

另外按 guide 的规定**没有自建 in-test deadline**:失败模式是「永不返回」,交给包级 `go test -timeout`
(它触发时会把整个 test binary 拿下并 dump 全部 goroutine 栈,信息量比一行 `t.Fatalf` 高一个数量级)。

### 6. 验证

- 全套测试 + `-race`、`test/`、p3 / p4 两个 build tag 全部 0 失败;
- oracle 单测、corpus 全量重放全绿;
- 45 秒引导式 fuzz 干净;
- **五个 seed 全部从 FAIL 变 PASS**。

## 期望与实际

- 期望:又是一批 nightly crasher,而上一轮八个字面事实几乎一样的 issue 全是过期的,核一次版本大概就能
  收工。
- 实际:版本核对的结论确实是「全在一个旧 commit 上」,但在当前 master 上实际跑一遍之后,五个如实复现、
  三个是真缺陷。**版本核对与「有没有缺陷」是两个正交的问题**,而上一轮的结论很容易让人把它们混成一个。

## 教训

### 教训 1(版本核对的三种结论要都记住,不要因为上一轮是「过期」就默认这一轮也是)

上一轮 #209 是第一档(修法已在仓库里,一行代码没改),这一轮八个 issue 同样全在一个旧 commit 上,但
五个如实复现、三个是真产品缺陷。

**Why**:版本核对回答的是「**要不要在这个 commit 上复现一遍**」这个成本问题——它排除的是「撞的是已
修复代码」这条最便宜的解释,而不是「有没有缺陷」这个事实问题。八个 run 落在同一个旧 commit 上这件事
本身不含任何信息量:nightly 每晚跑的就是当时的 master,所以「全在一个旧 commit 上」是常态,不是线索。
把上一轮的结论当成这一轮的先验,会让人跳过唯一能分档的动作——**实际跑一遍**。这与教训 4 是一对:那条
讲复现要连边界条件一起复现,本条讲复现这一步本身不能省。

**How to apply**:判据 = 版本核对完成之后,无论 headSha 落在哪一档,都要在当前 HEAD 上把每一条 seed
实际重放一遍再分档;「与上一轮同一个 commit」「与上一轮同一类写法」都不是可以省掉重放的理由。自查
办法:说「这一批是过期的」之前,先看有没有一条命令的输出支持这句话——如果支持它的只是上一轮的结论,
那就还没有分档。

### 教训 2(一个刻意的豁免判据,必须覆盖被豁免函数的所有别名)

`math.mod` 是 `math.fmod` 的 `LUA_COMPAT_MOD` 别名、同一个 C 函数,而 `__wrapArgOrder` 只写了
`fmod`,于是同一个写法被 fuzzer 报了两次(#217、#219)。`math.atan2` 也从来没被包过。

**Why**:豁免判据的正确边界是「**具备被豁免那条性质的全部入口**」,而人写清单时枚举的是「我记得的
那几个名字」。别名尤其危险,因为它在 C 侧根本不是另一个函数——`LUA_COMPAT_MOD` 只是给同一个
`math_fmod` 多绑了一个 Lua 名字,所以那条性质(两个官方构建对实参求值顺序不一致)对它逐字成立,而
按名字写的判据看不见它。这与 [[cross-backend-semantic-fix-sweep]] 的核心纪律「枚举全部后端 × 通道,
不凭记忆」是同一条判据换了对象:那条枚举的是**实现站点**,本条枚举的是**入口名**。

**How to apply**:判据 = 给某个函数加豁免 / 包装 / skip 时,先查它有没有别名或兼容名(Lua 5.1 里就是
`LUA_COMPAT_*` 那一族:`math.mod` = `fmod`、`table.getn`/`setn`、`string.gfind` = `gmatch`),并且用
「所有具备该性质的入口」而不是「我记得的那几个名字」来枚举——本轮的写法是按 math 表里「取两个数经
`luaL_checknumber` 的入口」这句话去枚举,而不是往清单里再补一行。自查办法:同一个豁免被 fuzzer 报了
第二次而产品没有缺陷时,先怀疑判据的覆盖面,不要怀疑判据本身。

### 教训 3(参照实现在循环之前做的校验,不能挪到循环里面做)

gsub 的 repl 类型检查从「循环之前」挪进了循环内部,于是一个把循环次数压成 0 的参数就让它整个失效——
`gsub("", "", nil, .0)` 成功返回,而 lua5.1 抬 `bad argument #3`。

**Why**:一个校验**在控制流的哪个位置**也是语义的一部分,不只是实现细节。「进入循环前校验一次」与
「每次迭代校验」在参数合法时完全等价、在参数非法且循环体执行 ≥ 1 次时也等价——两者只在
**参数非法且循环零次**这一种输入上分岔,而这正是 fuzzer 擅长构造的:它只要找到另一个参数能把次数压
到 0。这是 [[cross-backend-semantic-fix-sweep]]「PUC 语义由 C 实现定义」的又一个刻度:前面几个刻度分别
讲**值**要顺着 C 的转换链推、**上限**的条件项可能读调用当时的栈状态、**消息**可能被 `luaL_*` 包一层,
本条讲**校验的位置**同样是要照抄的东西。

**How to apply**:判据 = 把参照实现的校验搬过来时,记下它在 C 源码里位于哪个控制流位置(函数入口 /
循环之前 / 循环之内 / 分支之内),搬过来之后位置要一致。自查办法:对每一个「懒校验」问一句「有没有
一个输入能让这段循环 / 分支执行零次」——`n = 0`、空串、空表、空区间、提前 return 都是候选;有的话
就为那个输入写一个用例。

### 教训 4(重 workload 的回归测试必须连同 harness 的边界条件一起复现,不只是复现那段脚本)

我照抄了 #218 的 seed 但没照抄 `SetStepBudget(1 << 20)`,于是 harness 里 1.8 秒的输入在回归测试里跑
了 **87 秒**——比它要替代的那个 seed 还重 50 倍。

**Why**:一个 fuzz seed 的**代价**由「脚本 + 该 fuzz target 设置的全部限制」共同决定,而 seed 文件里
只有前一半。#218 那 1.8 秒根本不是一亿次迭代的代价,是**一百万步预算**的代价;把 budget 丢掉之后,
那段脚本测的是「一亿次迭代跑完要多久」这件另外的事。更糟的是动机反过来了:这个 seed 之所以被搬出
corpus,理由就是它太重,而不抄边界条件等于把它原封不动搬进了常规测试套件——常规套件每次 `make` 都跑,
比 corpus 重放更频繁。

**How to apply**:判据 = 把一个 fuzz seed 转成显式回归测试时,打开那个 fuzz target,把它设置的**每
一个** limit 一起抄过来(step budget、arena cap、force-promote 开关、输入长度检查),而不只抄脚本。
自查办法:新写的回归测试跑完之后量一次耗时,与那个 seed 在 harness 里的耗时对一下——量级不同就说明
漏抄了某个限制,那时测的是另一件事。

## Promotion 决策

- **教训 1** 已补进 [[unreproducible-crasher-triage]]「第一步永远是版本核对」那节末尾(三档是关于
  「要不要复现一遍」的分类,不回答「有没有缺陷」;#212–#219 是与 #209 字面事实几乎一样但结论相反的
  实例)。
- **教训 4** 已补进 [[unreproducible-crasher-triage]]「corpus 入库常驻回归——但要挑对入库位置」那节,
  紧接「重 workload 走 `test/regression/`」之后(挪过去的时候要连 harness 的限制一起抄)。
- **教训 2** 已补进 [[cross-backend-semantic-fix-sweep]]「对齐 PUC 之前先分清有定义还是 UB」那节的
  **执行体纪律**段(豁免的执行体必须覆盖被豁免函数的所有别名与兼容名)——那段原本讲豁免要**有**执行
  体,本条讲那个执行体的**覆盖面**。
- **教训 3** 已补进 [[cross-backend-semantic-fix-sweep]]「PUC 语义由 C 实现定义」那节,作为第五个
  刻度(校验在控制流里的位置也要照抄)。

## 触发场景

- **拿到一批 nightly crasher 时**:先算 seed hash 分组(本轮八个 issue 只有六个不同 seed),再核版本,
  然后无论落在哪一档都在当前 HEAD 上实际重放一遍再分档(教训 1)。
- **给某个函数加豁免 / skip / 包装时**:先查它有没有别名或 `LUA_COMPAT_*` 兼容名,用「所有具备该性质
  的入口」枚举,不要往清单里补名字(教训 2)。
- **把参照实现的一个校验搬进望舒时**:记下它在 C 里的控制流位置并保持一致;对每个懒校验问「有没有
  输入能让这段执行零次」(教训 3)。
- **把一个 fuzz seed 转成显式回归测试时**:连 fuzz target 里的每一个 limit 一起抄,写完量一次耗时与
  harness 对齐(教训 4)。
- **实现 `luaL_opt*` 语义时**:「缺省 / 显式 nil 取默认值」与「显式传了转不动的值就抬错」是两条规则,
  写成「转换成功才用」会把后者静默吞掉(本轮 #212 的根因)。

## 关联

[[unreproducible-crasher-triage]](第一步永远是版本核对——本轮是「同一个 commit、结论相反」的实例,
教训 1 的落点;一批 crasher 先算分组——本轮八个 issue 六个 seed;corpus 入库位置那节是教训 4 的落点)·
[[cross-backend-semantic-fix-sweep]](枚举不凭记忆是教训 2 的同族;「PUC 语义由 C 实现定义」是教训 3
的落点;UB / 未指定四格判据第三格是 `mathFn2` 那个决定的依据)·
[[2026-07-29-issue209-stale-crasher-threshold-pin]](上一轮:字面事实几乎一样而结论是第一档,教训 1
的对照)· [[2026-07-29-issue205-206-208-io-userdata-debug]](教训 5 错误消息被包一层,与教训 3 同属
「照抄参照实现要抄到哪一层」)· [[2026-07-28-issue201-203-unpack-skip-thresholds]](重 workload 与
被接受区间的处置史)·
`internal/stdlib/stdlib.go::baseFnError` · `internal/frontend/parse/expr.go::parsePrefixExpr` ·
`internal/stdlib/stringlib.go::stringFnGsub` · `internal/oracle/prelude.go::__wrapArgOrder` ·
`fuzz_212_219_test.go` · `test/regression/p4_hot_loop_promote_test.go` ·
`docs/design/p1-interpreter/09-errors-pcall.md` §3.1a(level 的两条规则)/ §3.5.1(CALL 记参数列表那一行)·
`docs/design/p1-interpreter/10-stdlib.md` §6.5.1(gsub 的 repl 前置校验)/ §8.6(取两个数的 math 入口)·
`docs/design/p1-interpreter/12-testing-difftest.md` §4.9e
