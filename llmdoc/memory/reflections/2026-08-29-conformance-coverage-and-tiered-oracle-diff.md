---
name: 2026-08-29-conformance-coverage-and-tiered-oracle-diff
description: >
  起因是一个提问：我们对 PUC 一致性到底有多大把握。去量而不是给感觉，量出来的结果推翻了平时的说法。
  分支 `feat/conformance-suite-and-tiered-oracle-diff`（commit 数目与文件数不在这里写死，取数命令见正文
  末节）。**第一个事实**：平时说「官方 Lua 测试套件通过」，实际是 14 个文件（上游 24 个）、按行号算
  1856 / 3434 = 54.0%，只有 3 个文件从头跑到尾（`pm` / `sort` / `vararg`），其余都在某一行 `stopAt`
  截断，`events.lua` 只跑 3 行。截断都有登记的豁免理由，是刻意不实现的功能而不是隐藏分歧，但「套件通过」
  与「套件 54% 通过」给人的信心完全不同。**第二个事实**：p3/p4 的 fuzz 差分不拿 PUC 当参照，而是拿 p1
  当参照（`FuzzAutoPromote` / `FuzzP4ForceAllPromote` 断言分层结果 == p1 结果，再靠 `FuzzOracleDiff` 的
  p1 == PUC 传递过去）；传递性本身没错，但它继承 p1 的所有盲区，而且看不到「p1 与 PUC 一致、两个 tier
  各自也一致地错」这种情况。**本轮两件事**：给套件加了 4 个文件（与上游逐字节相同），以及新增
  `FuzzOracleDiffTiered`（build 约束 `wangshu_oracle_cgo && cgo && (wangshu_p3 || wangshu_p4)`），
  在强制提升的 State 上把任意变异源码与内嵌 5.1.5 比对，跳过集合与 p1 目标完全相同；顺带把
  `runFuzzInputOn` 从 `runWangshuSide` 抽出来共用，并把 nightly 的 `fuzztime` 与新步骤上限改成按层取值。
  **写这篇反思时用同一条纪律去核了这两件事，测出两个缺陷，其中第一个推翻了本轮第一件事的收益陈述**：
  ① 新加的四个文件实际执行的 `assert` 次数是 **0 / 1 / 0 / 0** —— `code.lua` 与 `checktable.lua` 开头就是
  `if T == nil then ... return end`（`T` 是官方 testC 调试库，本仓不提供，`code.lua` 打印
  "testC not active: skipping opcode tests" 后返回），`verybig.lua` 的断言全在那个模板字符串里、要等
  第 96 行 `dofile` 才执行而 `stopAt` 是 89，`db.lua` 的 `debug.getinfo` 断言从第 25 行起而 `stopAt` 是 24；
  所以「code.lua 完整通过并检查 codegen」「verybig 构造并运行超 64k 上限的程序」「db 用官方期望校
  currentline/source」三句都不成立，四个文件合计贡献 1 次断言，而按行号算的「已跑行数」从 1856 涨到 2187。
  ② `FuzzOracleDiffTiered` 的注释说「若整轮没有任何输入提升就让目标失败」，而那个
  `promotedAtLeastOnce` 变量只被 `Store`、没有任何地方 `Load`，fuzz 目标本身没有提升断言；配套的
  `TestTieredOracleDiffActuallyPromotes` 确实防住了「整个 harness 忘记提升」（把 `SetForceAllPromote`
  删掉实测变红），但它比注释声称的弱一格 —— 它的脚本调的 `__oracle_print` 在全仓只出现在那一行、
  并不存在，脚本第 2 行就抬错（能过是靠 Lua 先算实参再报 nil 调用这个巧合），而更要紧的是
  **把 payload 换成空字符串那条断言照旧成立**（实测 `PromotionCount` 9 → 10，因为 force-all 会提升
  main chunk 自己），所以它证明的是「force-all 开着」而不是「被测代码进了 tier」。四条教训：引用外部权威套件当信心来源必须同时给覆盖率 / 传递性证明继承中间项的盲区
  并且看不见两端各自一致地错 / 一个「测新路径」的测试必须断言它真的进了那条路径而不是能编译 /
  按行号算出来的覆盖量不是执行证据，换成绝对量也修不好它，要换成能被执行观测到的量。
metadata:
  type: reflection
  date: 2026-08-29
---

# 「官方套件通过」到底通过了多少,以及给 p3/p4 补一条直接对 PUC 的差分轴(2026-08-29)

> 范围:分支 `feat/conformance-suite-and-tiered-oracle-diff`。改动集中在
> `test/luasuite/`(**四个上游套件文件已在同分支 revert**,只留 `stopAt` 表注释里的计数方法说明)、根包新增
> `fuzz_oracle_tiered_test.go`、`fuzz_oracle_test.go` 抽出 `runFuzzInputOn`,
> 以及 `.github/workflows/ci.yml` 与 `.github/workflows/nightly-diff-fuzz.yml`。
> 产品代码零改动。commit 数目与改动文件数不在文中写死,取数命令见末节。

## 1. 起因:一个提问,不是一个 issue

用户问的是「我们对 PUC 一致性有多大把握」。这不是一个可以凭印象回答的问题,
所以去量了。量出来的结果推翻了平时的说法 —— 两处。

## 2. 量出来的第一个事实:「官方套件通过」通过了 54%

平时的说法是「官方 Lua 测试套件通过」。实际情况:

- 套件里是 **14 个文件**,上游 `lua5.1-tests` 有 **24 个**。
- 按行号算,已跑行数 **1856 / 3434 = 54.0%**。
- 只有 **3 个文件**从头跑到尾:`pm.lua` / `sort.lua` / `vararg.lua`。
- 其余每个都在某一行被 `test/luasuite/luasuite_test.go` 的 `stopAt` 表截断。
  最极端的是 `events.lua`,只跑 **3 行(0.8%)** —— 它第 5 行就用 `setfenv`。

先确认了一件重要的事:现有 14 个文件与上游**逐字节相同**,这一条是真的。
截断也都有登记的豁免理由(`setfenv`/`getfenv`、`debug` 高级面、io 对象模型、
`string.dump`、`require`、真正的增量 GC),都是**刻意不实现的功能**,不是藏起来的分歧。

所以问题不在于有隐瞒,而在于**同一件事的两种说法给人的信心差得很远**:
「套件通过」听起来像全套跑过了,「套件 54% 通过、只有 3 个文件完整跑完」是另一回事。

## 3. 量出来的第二个事实:p3/p4 的参照是 p1,不是 PUC

`FuzzAutoPromote` 与 `FuzzP4ForceAllPromote` 断言的是「分层结果 == p1 结果」。
「分层结果 == PUC」是靠 `FuzzOracleDiff` 的「p1 == PUC」**传递**过来的。

传递性本身没有错。问题有两条:

1. 它**继承 p1 比较的所有盲区** —— p1 看不见的,传递之后仍然看不见。
2. 它**看不到「p1 与 PUC 一致,而两个 tier 各自也一致地错」** 这种情况。
   tier == p1 这个断言在这种情况下是假的,所以理论上会被抓到;但两个 tier
   互相之间的一致性不由这条链保证,而分层结果与 p1 的比较只在 p1 侧有参照。

`test/difftest` 确实按 tier 对真 `lua5.1` 二进制跑,但它的生成器
(`test/difftest/generator.go`)只有 451 行,而且 `vararg` / `goto` / `pcall` /
`error` 的生成计数都是 **0** —— `grep -c 'vararg\|goto\|pcall\|error('` 在那个文件上返回 0。
所以它覆盖的写法比 go-fuzz 的变异窄得多。

## 4. 本轮做的两件事

### 4.1 接入 4 个官方套件文件

拉了上游 `lua5.1-tests`,按**实际依赖**而不是按文件名分诊 10 个缺失文件,加了 4 个
(`code.lua` / `checktable.lua` / `verybig.lua` / `db.lua`,与上游逐字节相同),
并在 `stopAt` 表里登记各自的截断行与理由。

两个明确不加并记录了理由:`files.lua` 第 4 行就到 `io.input`、`attrib.lua` 第 5 行就到
`require`,各只能贡献三四行。

**这一件事的收益陈述在写反思时被推翻了,见 §5。**

### 4.2 给 p3/p4 加直接对 PUC 的 fuzz 差分

新增 `fuzz_oracle_tiered_test.go` 里的 `FuzzOracleDiffTiered`,build 约束
`wangshu_oracle_cgo && cgo && (wangshu_p3 || wangshu_p4)`:任意变异源码在**强制提升**的
State 上跑,与内嵌 5.1.5 比对。

**跳过集合与 p1 目标完全相同**,这一条是刻意的:加一个 tier 专属的跳过是让这个目标变绿
最容易的办法,也是让它变得毫无价值的办法 —— 「tier 与 PUC 分歧而 p1 不分歧」正是这个
目标存在的理由,不能可跳过。

最需要小心的一点写在文件头注里,而且它是对的:`FuzzOracleDiff` **本来就能在 tier tag 下
编译并通过** —— 它构造的是普通 State,tier 代码被链接进来但从未进入。一个忘记提升的
tiered harness **看起来与一个通过的测试一模一样**,而实际把解释器测了两遍。

顺带把 `runFuzzInputOn` 从 `runWangshuSide` 抽出来共用,避免两个目标各持一份比较约定而漂移
(预算值、limit 分类、readout 覆盖处理必须是同一份实现)。

实测:两个 tier 都能观察到提升(p4 上 9 → 11),25 秒引导式 fuzz 5522 execs,**没有发现分歧**。
这是**第一个结果,不是一张清白证明** —— 它说明这条轴是活的、显然的情形一致。

### 4.3 nightly 预算重新推导

p3/p4 现在带两个 fuzz 步骤而 p1 只有一个,单一的 `gofuzztime` 要么在 p1 上浪费、
要么在 p4 上超链。于是 `fuzztime` 与新步骤的上限也变成**按层取值**(与既有的
`native_fuzz_cap` 一致):p1 32m、p3 28m、p4 26m。预算校验步骤在三层上都过。

## 5. 写这篇反思时,用本仓自己的纪律去核这两件事,测出两个缺陷

这一节是本轮最有价值的部分。按 [[prove-the-path-under-test]] §2(b) 的要求 ——
一个「测 X」的东西要有**正交于「它没报错」**的证据 —— 去核了上面两件事,两件都有问题。

### 5.1 新加的四个套件文件合计执行了 1 次断言

手法:给套件文件包一层计数器(把 `assert` 换成一个自增再转发的版本),
按 `stopAt` 截断后跑一遍,读实际执行的 `assert` 次数。这个量是**执行证据**,
而「已跑行数」只是一个按行号算出来的数,不携带任何执行信息。

结果(截断后实际执行的 `assert` 次数):

| 文件 | stopAt | 实际执行的 assert 次数 | 机制 |
|---|---|---|---|
| `code.lua` | 0(整跑) | **0** | 第 1 行 `if T==nil then ... return end`,`T` 是官方 testC 调试库,本仓不提供;它打印 `>>> testC not active: skipping opcode tests <<<` 然后返回 |
| `checktable.lua` | 0(整跑) | **1** | 同样第 3 行 `if T == nil then ... return end`;那 1 次是文件第 1 行的 `assert(rawget(_G,"stat")==nil)` |
| `verybig.lua` | 89 | **0** | 断言全在 `prog` 那个长括号**字符串**里,要等第 96 行 `dofile(file)` 才执行;而 `stopAt=89` 恰好切在第 89 行 `os.tmpname()` 之前,`dofile` 永远到不了 |
| `db.lua` | 24 | **0** | `debug.getinfo` 的断言从第 25 行起(`do local a = debug.getinfo(print) assert(...)`),`stopAt=24` 切在它之前;前 23 行只有函数定义与一句 `print` |

对照组(现有文件确实在跑):`sort.lua` 150030 次、`nextvar.lua` 59262 次、
`gc.lua` 5005 次、`pm.lua` 147 次、`vararg.lua` 83 次。所以这个手法本身是灵敏的。
顺带发现现有的 `events.lua` 也是 **0** 次(它只跑 3 行,与 §2 说的一致)。

**同一条纪律顺手推翻了一处历史引用,而这一处是本轮判据最好的反例。**
`llmdoc/guides/prove-the-path-under-test.md` §4 举过一个例子:VS0-e 子步 ⑥ 本来计划写 11 条
vararg 语料,结论是「不写冗余,因为 `luasuite/closure.lua` 已含
`{coroutine.yield(unpack(arg[i]))}` 这个 5.1 作者本人写的最复杂组合」,
证据写的是「luasuite 14 文件全 PASS」。实测:`closure.lua` **是被截断的文件之一**
(`stopAt=163`,截断理由是 `setfenv`),而**被引用的那一行是第 195 行 —— 它从来没有被执行过。**
`vararg.lua` 整文件确实在跑(它是完整跑完的少数文件之一),所以那一半证据是真的;
而「`closure.lua` 里那个最复杂的组合已经覆盖了」这一半是假的。
这一处正是教训 1 与教训 4 叠起来的样子:**「那个文件在套件里」被当成了「那一行在跑」**,
而套件层面「全 PASS」这个说法让这个替换看起来天经地义 —— 不需要任何人做错判断,
只要引用一个不带覆盖率的结论就够了。判据因此要多一格:`grep -l` 命中之后
**还要确认命中的那一行在 `stopAt` 之前**。

**所以本轮第一件事的三句收益陈述都不成立**:

- 「`code.lua` 143 行完整通过,断言生成的字节码,是套件里唯一检查 codegen 的文件」——
  它一条 codegen 断言都没执行。它检查 codegen 靠的是 `T.listcode`,而 `T` 就是它头一行
  判空之后放弃的那个库。设计文档 `docs/design/p1-interpreter/12-testing-difftest.md` §2.1
  当年把 `code.lua` 预判为「结构性 SKIP」,理由是望舒自定义 opcode 编号 —— 那个预判虽然
  理由不同(真实原因是 `T` 不存在,而不是 opcode 编号),结论方向是对的。
- 「`verybig.lua` 构造并运行超过 >64k 常量/跳转上限的程序,本仓其他地方都没测过」——
  它连一个字节都没构造,`stopAt=89` 把构造与运行整段切掉了。
- 「`db.lua` 用官方期望校 `debug.getinfo` 的 currentline/source」—— 那些断言在第 25 行起,
  被 `stopAt=24` 切掉了。

**「已跑行数 1856 → 2187」这个数字本身没算错**(按 `stopAt` 与文件行数算就是这个结果),
它错在**测的不是执行**。四个文件把分母抬高了 819 行、分子抬高了 331 行,而实际执行的
断言只多了 1 条。原来的记录说「占比从 54.0% 掉到 51.4% 是因为新加的文件豁免点后有大段
跑不到的尾巴」—— 这个解释对分母那一半是对的,但它默认了分子那 331 行**在做事**,而实测是没有。

`db.lua` 那一格另外量了一次「能不能救」:把 `stopAt` 从 24 往上抬,25–34 之间都编译失败
(切在 `do`/`function` 块中间,报 `'end' expected`),抬到 40 时第一条断言执行了、
并且**失败**(`db:4: assertion failed!`,那是 `dostring` 那一层)。所以这个文件不是「调一下
`stopAt` 就有收益」,它真正需要的是 `debug.sethook` / `getlocal`,也就是它被截断的原因。

### 5.2 tiered fuzz 目标的「整轮至少提升一次」断言不存在,而守护测试的脚本调了一个不存在的函数

两个独立的问题。

**其一,`promotedAtLeastOnce` 只被写、从不被读。** 文件头注写着「所以提升是断言出来的
而不是假设的:……然后要求 `PromotionCount()` 增长……**如果整轮什么都没提升,这个目标会失败
而不是报绿**」。实测 `grep -rn promotedAtLeastOnce --include=*.go .` 只有三处:声明、
一句注释、以及 `runTieredSide` 里的 `.Store(true)`。**没有任何 `.Load()`**,
根包也没有能读它的 `TestMain`(唯一的 `TestMain` 在 `fuzz_forensics_test.go`,与它无关)。
所以 fuzz 目标本身对提升没有断言 —— 注释描述的机制没有实现。

这一处的性质与 [[prove-the-path-under-test]] §4.2 是同一族:**一句没有执行体的声明,
比一个已知缺口更糟,因为它读起来像已经处理过了**。而且这一处更容易骗过读者,因为那个变量
真的存在、真的被写、类型也真的是 `atomic.Bool`,看起来完全像在工作。

**其二,守护测试 `TestTieredOracleDiffActuallyPromotes` 的脚本第 2 行就抬错。**
它跑的是:

```lua
local function work(n) local s = 0 for i = 1, n do s = s + i % 5 end return s end
__oracle_print(work(64))
```

`__oracle_print` 在全仓只出现在这一行(`grep -rn __oracle_print --include=*.go .`
只命中它自己);prelude 提供的是 `__oracle_readout`。所以实测日志里是
`input errored (not fatal for this probe): [string "fuzz"]:2: attempt to call global '__oracle_print' (a nil value)`。

它仍然通过,原因是 Lua **先算实参再发现被调者是 nil**:`work(64)` 在报错之前执行了。

**而这个巧合掩盖了一件更要紧的事:那条断言比注释声称的弱得多。**
在守护测试自己的环境里(同一个 keep 集合、同一条 `runFuzzInputOn` 路径)只换 payload 实测:

| payload | `PromotionCount` | 断言 `after > before` |
|---|---|---|
| 现在这个(带循环的函数 + 调用) | 9 → 11 | 成立 |
| `local x = 1` | 9 → 10 | 成立 |
| **空字符串** | 9 → 10 | **成立** |

**空输入也满足这条断言。** 原因是 `PromotionCount` 是**编译侧**的量,而 force-all 会提升
被编译出来的那个 **main chunk** 自己 —— 只要还有一个可编译的 Proto 存在这个计数就增长,
不需要任何被测函数存在,更不需要它被执行。所以这条断言实际证明的是「force-all 开着」,
不是「被测的那段代码进了 tier」。用一个执行侧的计数器(`peroptranslator.NativeRunCount`)
对照可以看到差别:payload 修好(把 `__oracle_print` 换成真的存在的名字)之后
`NativeRunCount` 增量从 1 变成 2,而 `PromotionCount` 两种情况都是 +2。

**这一格与 §5.2 其一是同一件事的两个层次**:其一是注释描述的机制根本没写,
其二是写了的那个机制比注释描述的弱一格 —— 而两者叠起来的效果是,
「整轮至少提升一次」这句话在代码里没有任何等价物。

**该记在正面的部分**:「把 `SetForceAllPromote` 去掉后它变红」这条实测是**真的**,
本轮复核时按同样的手法做了变异实测,确认删掉那一行之后
`PromotionCount did not grow (0 -> 0)` 直接挂掉。所以这个测试确实防住了
**「整个 harness 忘记提升」这一种退化** —— 这一种恰好也是文件头注最担心的那一种,
所以它不是白写的。它没防住的是「提升开着但被测代码没进 tier」,而那正是空输入也能过的原因。

CI 里那两条断言(required-target 存在性检查、p3 与 p4 两个 tag 下都跑守护测试)
本身是对的,而且 required-target 那条防的是真实风险:这个目标的 build 约束是
`wangshu_oracle_cgo && cgo && (wangshu_p3 || wangshu_p4)`,tag 打错它会静默消失
而其余目标照旧让 job 变绿。

## 6. 四条教训

### 教训 1:引用一个外部权威套件当信心来源,必须同时给出覆盖率

「官方 Lua 测试套件通过」这句话传达的信心远超它的证据。实际是 14 / 24 个文件、
按行号 54%、只有 3 个完整跑完,而平时只说「通过」。

**判据**:凡引用一个**外部权威套件**作为信心来源,要同时给出「跑了多少 / 总共多少 /
有多少被截断以及为什么」。只说「通过」就是在暗示全跑了。

落点:[[design-claims-vs-codebase-physics]](主张 vs 实际那一族)。

### 教训 2:传递性证明继承中间项的所有盲区,并且看不见「两端各自一致地错」

`tiered == p1` 且 `p1 == PUC` 推出 `tiered == PUC`,但这条链继承 p1 比较的每一个盲区,
而且对「p1 与 PUC 一致、两个 tier 一致地错」这种情况没有直接的观察能力。

**判据**:当 A 与 C 的一致性是通过 B 传递得来的,要问「有没有一条**直接**比 A 与 C 的路」。
没有的话,那个盲区要如实写进文档,而不是让传递结论看起来与直接结论等价。

落点:[[prove-the-path-under-test]](差分参照链那一族)。

### 教训 3:一个「测新路径」的测试,必须断言它真的进了那条路径,而不是能编译

`FuzzOracleDiff` 在 tier tag 下能编译、能通过,但走的是解释器 —— 一个忘记提升的
tiered harness 与它**从输出上无从区分**。

**判据**:新增一个「测 X 路径」的目标时,配一条**能观测到进入 X 的断言**,并且用变异实测它:
把进入 X 的那一行删掉,测试必须变红。本轮做到了这一步(`PromotionCount()` 增长 +
删掉 `SetForceAllPromote` 变红),但 §5.2 暴露出还差**两格**:

- **那条断言要有执行体。** 注释描述的「整轮至少提升一次否则失败」没有实现
  (`promotedAtLeastOnce` 只被 `Store`),而它读起来完全像在工作。
- **变异实测要包含 payload 本身,不只是包含那个开关。** 删掉 `SetForceAllPromote`
  会变红,但把 payload 换成**空字符串**照样通过 —— 所以这条断言证明的是「force-all 开着」,
  不是「那段代码在 tier 上跑过」。`PromotionCount` 是编译侧的量,force-all 会提升 main chunk
  自己;要断言执行就得读执行侧的计数器(如 `peroptranslator.NativeRunCount`)。
  自查办法很便宜:**把 payload 换成空的,断言必须变红**。

落点:[[prove-the-path-under-test]] 新的一格 —— **能编译不等于进了那条路径**,
以及它的两个下一格:断言要有执行体、变异实测要作用在 payload 上。

### 教训 4:按行号算出来的覆盖量不是执行证据,换成绝对量也修不好它

本轮原来的结论是:「占比从 54.0% 掉到 51.4%,但那是因为占比本来就是错的度量 ——
它汇总的缺口不是均匀散在未测行为里,而是集中在 codegen 与 >64k 上限,恰好是这四个文件
覆盖的;报告时给绝对量与缺口位置,不要只给比例。」

**这个结论的方向对,但它自己踩了同一个坑。** 提出的替代品是「绝对量」(已跑行数
1856 → 2187),而已跑行数与占比是**同一个量的两种写法**,都是按 `stopAt` 与文件行数算出来的,
都不携带执行信息。实测那 331 行新增「已跑行数」合计执行了 1 条断言,两个写法都读不出这件事。

**判据**:一个覆盖类指标要能回答「这些行**做了什么**」,不只是「这些行**被送进了解释器**」。
按行号算的量(占比、已跑行数)属于后者,换分子分母的写法救不了它。可执行的替代是一个
**执行侧的计数**(本轮用的是实际执行的 `assert` 次数;别的场合可以是命中计数器、
dispatch 计数、覆盖率工具的实测行),它对「一行代码存在但那条路径没走到」是敏感的。
自查办法:给这个指标构造一个「数字明显变好而实际什么都没多测」的输入 ——
构造得出来就说明这个指标测的不是执行(本轮的构造现成:加一个开头就
`if T == nil then return end` 的文件)。

这一条与原来的表述还有一处不同。原来说「一个指标在明确改善之后变差,说明它汇总掉了
你真正关心的分布」;实测之后,这一轮**没有**「明确改善」这个前提 —— 指标变差的同时
实际收益也接近零,所以那不是指标失真,是指标在**如实反映**一件收益很小的事,
只不过它反映得太粗、看不出到底是「加了很多跑不到的尾巴」还是「加的东西整体没跑」。

落点:[[design-claims-vs-codebase-physics]](主张 vs 实际那一族),与教训 1 同族。

## 7. 遗留

四项,都不在本轮改(本轮不动代码 / 测试 / workflow):

1. `test/luasuite/testdata/` 里 `code.lua` 与 `checktable.lua` 依赖官方 testC 库(`T`),
   本仓不提供,两个文件在开头判空后返回。要么提供 `T` 的等价面(`listcode` / `querytab`
   这类内省钩子),要么把它们从套件里撤掉并在 `stopAt` 表里写清「整文件不产生断言」;
   现在的 `"code.lua": 0` 与 `"checktable.lua": 0` 登记的是「整跑」,而实际是「整跳」,
   这两个含义在表里长得一样。
2. `verybig.lua` 的 `stopAt=89` 把构造与 `dofile` 整段切掉,`db.lua` 的 `stopAt=24`
   切在第一条断言之前。前者要能跑需要 `os.tmpname` + `io.output` + `dofile`,
   后者需要 `debug.sethook` / `getlocal`。
3. `fuzz_oracle_tiered_test.go` 的 `promotedAtLeastOnce` 没有读取点,文件头注描述的
   「整轮无提升则目标失败」没有实现;`TestTieredOracleDiffActuallyPromotes` 的脚本调
   `__oracle_print`(不存在,应为 prelude 提供的名字),而且那条断言对 payload 不敏感。
4. `test/luasuite/luasuite_test.go` 的包注释写「The 13 files in testdata/」,与实际文件数
   不一致(这类会变的量本来就不该写死在注释里)。

## 写完这篇之后又发生的两件事(补记)

本篇写在两个 commit 之前,补上以免它描述的是中间状态:

**一、四个套件文件已 revert。** 既然实测只贡献 1 次断言,留着就是「覆盖率数字变好而实际什么都没多测」,正是本篇教训 4 说的那件事。计数方法(把 `assert` 包一层计数器、按 `stopAt` 截断后跑)写进了 `luasuite_test.go` 的 `stopAt` 表注释,连同各文件的实测数值,免得下一个人重复。

**二、`db.lua` 的截断藏了一个真分歧,已修。** 它第 26 行断言 `debug.getinfo(print).what == "C"`,而 wangshu 那张表里连 `what` 都没有。原因是早期版本硬编码 `what="Lua"`、`source="=[C]"`(两种函数都标错),当时的修法是按「宁缺勿造」把字段整个省掉 —— 方向对,但**用错了对象**:这些字段是**可以推导的**(host closure 在值层面就与 Lua closure 可区分,Lua 函数带着自己 proto 的 source 与 linedefined)。**省掉一个可推导的字段不是诚实,是缺口**,而截断恰好把断言切在它上面一行。新增 `State.FunctionIsHost` / `State.FunctionInfo`,逐字段与真 `lua5.1` 比对一致。

**三、tiered harness 的提升断言被审计判定过弱,已加强。** `PromotionCount()` 在 force-all 下对**任何**输入都 +1(输入自己的 main chunk 就是可编译 Proto)—— 实测空字符串、空白、`local x = 1` 全是 9 → 10。所以我原先写的 `after > before` 只证明了「force-all 开着」。现在界是 `after > before+1`,并且**测试自己检查空 payload 落在界下**,否则那个界只是我挑的一个数。另外实测确认这个界是**执行侧**而不是编译侧:一个编译了但从未被调用的嵌套函数只有 +1。
判据补一条:**一个守卫要在两侧都做变异** —— 既要在「被保护的东西」上(删掉修复,守卫必须红),也要在「它声称在量的东西」上(换成空 payload,守卫也必须红)。我当时只做了前者。

## 再补一次:revert 之后又把 db.lua 真正接进来了

上面「四个文件已 revert」那段是当时的状态,但**我在那里停错了地方**。「这些文件不执行断言」是关于**文件**的事实;「所以它们接不进来」是另一个主张,只有当它们需要的东西**拿不到**时才成立。对 `db.lua` 来说拿得到。

它卡在 `stopAt=24`,是因为第 28 行要 `lastlinedefined` 与 `activelines`。这两个字段此前和 `nups`/`namewhat` 一起被归进「依赖 hook,宁缺勿造」—— **而这个归类是错的**:两者都不需要 hook。`LineEnd` 每个 Proto 本来就在记,`LineInfo` 本来就把每条指令映到行号,PUC 的 `activelines` 就是照这张表建的。所以它们是**可推导**的,而「宁缺勿造」从来不该盖住可推导的字段。

补上之后 `db.lua` 跑到 `stopAt=40`、**执行 4 条断言(此前 0 条)**,校的是 getinfo 的 what/source/short_src/linedefined/lastlinedefined/activelines,而且校的是**官方的期望值**而不是我自己写的 probe —— 这正是想要上游文件的全部理由。两个方向都验了牙:去掉 `lastlinedefined`,单元测试与 `db.lua` 本身都会红。

另外三个留在外面,现在是**有明确理由**而不是「实测为空」:`verybig.lua` 要 `os.tmpname`/`io.output`/`io.close`/`os.remove` 才能到它的 >64k 载荷 —— 四个都在 io 对象模型豁免里,即它卡在一个**刻意的不实现**上;`code.lua` 与 `checktable.lua` 要官方的 `T`(testC),那是 C 侧调试外挂,不是 Lua 可见特性。

**教训 4 要补一句**:我把「这个测试有没有在做事」的测量结果,当成了**文件的属性**,而它其实是**(文件,我们实现了什么)这一对**的属性。「这个测试有没有在做事」与「要让它做事需要什么」是两个问题,我只答了第一个。

## 8. 取数命令

会变的量不写死。要准确数目用:

```bash
# 本分支的 commit 数
git log --oneline origin/master..HEAD | wc -l
# 改动文件数
git diff --stat origin/master..HEAD | tail -1
# 套件文件数 / 完整跑完的文件数
ls test/luasuite/testdata/*.lua | wc -l
grep -c ': *0,' test/luasuite/luasuite_test.go   # 近似,以 stopAt 表为准
# 按行号算的覆盖量(分子/分母/占比),读 stopAt 表与文件行数
# 实际执行的 assert 次数:给套件文件包一层自增转发的 assert 再按 stopAt 截断跑
```

