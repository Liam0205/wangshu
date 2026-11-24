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

## 顺带核对出的文档错误

`10-stdlib.md` §11 总表把 `os.exit` / `os.remove` / `os.rename` / `os.tmpname` / `os.setlocale` 和
`io.close` / `io.input` / `io.output` / `io.type` 列在「简化/部分」一栏，实际都没有注册。这次按实际情况改了
os 那一行，并在 §12.3 里写明；`stdlibAbsent` 把它们登记为缺少。这些函数要不要补上，没有在这一轮决定。
