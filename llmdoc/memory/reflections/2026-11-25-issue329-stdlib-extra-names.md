# #329：标准库多出两个 5.1 没有的名字

nightly 的 go-fuzz 找到 `t={}table.unpack(t)`：官方 Lua 5.1 报错，望舒照常执行。`table.unpack` 是 5.2 才有的，
设计文档从一开始就写着「不提供」，而实现从 2026-06-11 起多注册了它。顺着查出另一个多出的名字 `__ipairs_iter`，
以及 `pairs` / `ipairs` 经全局表查迭代器造成的两处行为差异。改动清单见
[implementation-progress](../../../docs/design/p1-interpreter/implementation-progress.md) 的 2026-11-25 条目，
机制见 [10 §4.4 / §12.3](../../../docs/design/p1-interpreter/10-stdlib.md)。

## 为什么五个多月没发现

规则是「标准库的名字集合必须与 5.1 一致」。它的执行体只有 `exemptions` 这份手工清单，而清单只能写
「官方有、望舒没有」。「望舒有、官方没有」这个方向没有任何检查。设计文档 §12.3 在 2026-07-29 核对时已经
写明「遍历全部 5.1 函数名的存在性差分用例还没写」，并把它记成缺口，但缺口一直没补。

代码里的注释还把这件事写成了有意为之：`table.unpack alias ... provide both`。注释和设计文档相反，
没有人核对过。

## 这一轮的做法

1. 先按排查指南在当前 master 上精确重放，确认是真缺陷，不是参照实现的问题。
2. 没有只删那一个名字。把两边的名字清单各导出一份比对，一次找出全部两个多余名字。
3. 对第二个名字（`__ipairs_iter`）没有停在「删掉全局」上：它之所以是全局，是因为 `ipairs` 每次去全局表里查它；
   `pairs` 用同样的办法查 `next`。对照 `lbaselib.c` 的 `auxopen`（迭代器是 C 闭包的 upvalue），又找出
   `next = nil` 之后 `pairs` 报错、`pairs({}) == next` 为 `true` 两处差异。
4. 把比对留下来当测试（`TestDiff_StdlibNames`），补上设计文档里记了四个月的那个缺口。

## 可复用的检查方法

写进了 [cross-backend-semantic-fix-sweep](../../guides/cross-backend-semantic-fix-sweep.md)
「两个集合必须相等这类规则，比对要做成常驻测试，而且两个方向都要比」：看到「A 必须等于 B」的规则先问执行体
是什么；执行体是手工清单时，它表达不了的那个方向就是没人看的。

## 独立审查指出的三件事

合入前的独立审查没有发现阻塞问题，但有三处是我自己本该先做到的。

**一、测试写着「热函数」，实际没有升层。** 我假设「循环 3000 次的函数会被升到 P3 / P4」，于是写了一个
在循环里调用 `ipairs` / `pairs` 的用例，注释说它覆盖升层后的路径。实际上直接调用全局函数的函数会被判成
不可升层，这个用例在三种构建下执行的都是解释器。能提前发现它的检查只有一行：读 `PromotionCount()`。
[prove-the-path-under-test](../../guides/prove-the-path-under-test.md) 早就写了「测试通过不证明被测路径被执行」，
我没有照做。改成 `TestTiered_StdlibNames`，把「升层次数大于 0」写成断言。

**二、发现文档与实现不一致，只改了一处。** 核对名字集合时我确认了 `os.exit` 等十个函数没有注册，改了
§11 总表的 os 那一行就停了。同一件事在 README（两个语言版本）、§9.1、§9.3、§9.4、§10.1、§11 的 io 行、
`exemptions` 里还有七八处说法，其中 README 写的「`os.exit` 不真的退出进程」是使用者会照着写代码的。
能提前发现它的检查是在全仓搜索同一个说法（`git grep "os.exit"`），这也是已有的约定。现在由
`TestStdlibAbsentIsInREADME` 检查 README 的「不提供」表与登记表一致。

**三、存量差异没有开 issue。** 我在复盘里写了「这些函数要不要补上，没有在这一轮决定」，但没有留下任何
可以跟踪的东西。按约定，本轮不修的存量差异要开 issue。补开了 #330（os / io 缺少的函数）和 #331
（主块之下的 C 帧，栈回溯按独立解释器、C 调用深度按内嵌用法）。

审查还指出 `ipairs` 的迭代器不接受数字字符串。这是存量差异，但它就在这次改动的函数里，而且属于同一类
原因：迭代器没有公开的名字，以前按函数名逐个核对 `luaL_checkint` 的行为时轮不到它。这一轮直接修了。

## 顺带核对出的文档错误

`10-stdlib.md` 把 `os.exit` / `os.remove` / `os.rename` / `os.tmpname` / `os.setlocale` 和
`io.close` / `io.flush` / `io.input` / `io.output` / `io.type` 写成「做简化版」，README 写「os 全部提供」，
实际都没有注册。设计文档里的 `Libs` / `LibsSafe` / `Exclude` 三层开关也没有实现。这次把 README 和设计文档
各处都按现状改了（设计文档新增 §9.5），原始设计的段落保留并标明不是现状。这些函数和开关要不要做，
记在 [#330](https://github.com/Liam0205/wangshu/issues/330)。
