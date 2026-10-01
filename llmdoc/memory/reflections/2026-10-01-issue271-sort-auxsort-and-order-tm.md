---
name: 2026-10-01-issue271-sort-auxsort-and-order-tm
description: >
  巡检处理 nightly 开出的 #271(p1 `FuzzOracleDiff`,seed `t={0,0,0%0,0X0}table.sort(t)print(unpack(t))`,
  分支 `fix/271-table-sort-auxsort`)。标题写 crash,实际是输出不同:oracle `0 nan 0 0`、望舒 `0 0 nan 0`。
  根因是 `table.sort` 用的是 `sort.SliceStable`,设计稿 10 §7.3 写的「移植 5.1 `auxsort`」从没做过;两种算法
  只在严格弱序下给出相同结果,NaN 不满足。改为逐步移植 `auxsort`,直接读写表。改的过程中顺带查出共用的
  `LessThan` 措辞错误,以及 VM 的 `<`/`<=` 元方法规则与 `lvm.c` 不一致(一侧有 `__lt` 时 `A < 1` 也返回
  处理函数的结果)。独立审查又找出三处:跨比较持有的元素没有 GC 根(存量,纯 Lua 可触发 VM panic)、
  默认比较器报错多了位置前缀(存量)、快排在对抗排列上平方级而不查取消(本轮引入);第二轮复核又找出
  全局 yield 哨兵被并发写的数据竞争(存量 + 本轮新增一处写入点),处理函数直接是 `coroutine.yield` 时的
  越界挂起开了 #272。教训:
  **设计稿写了「照抄参照实现」不等于实现照抄了,文档声称的对齐要用一个只在对齐时才成立的输入核对**;
  **把参照实现的 C 栈局部搬成 Go 局部时,先问 C 栈在那里还顺带提供了什么(GC 根)**;
  **换算法时把旧算法顺带提供的复杂度保证列出来,逐条确认新算法是否还有**。
metadata:
  type: reflection
  date: 2026-10-01
---

# table.sort 移植 auxsort,以及顺带查出的比较规则偏差(2026-10-01,issue #271)

> 范围:issue #271,分支 `fix/271-table-sort-auxsort`。改动在 `internal/stdlib/tablelib.go`(`tableSorter`)、
> `internal/crescent/execute.go`(`lessThan` / `lessEqual` / `callOrderTM` / `orderError`)、
> `internal/crescent/meta.go`(`LessThan` 改为委托)、`internal/crescent/state.go`(`CheckCancel`);
> 新测试 `test/regression/issue271_sort_order_test.go`;设计稿 07 §9.2-§9.4、10 §7.3-§7.4 与 implementation-progress。
> 本轮巡检在中途中断过一次,接续时语料已入库(`b5cbdd5`),修复从头做。

## 过程

1. **复现**:失败 run 的 headSha 就是 master,本地重放立刻复现。读 `tableFnSort`:数组拷到 Go 切片,
   `sort.SliceStable`,写回。设计稿 10 §7.3 明写「P1 移植 5.1 `auxsort` 的快排结构」并把「相等元素最终顺序」
   列为差分敏感项,12 §13 也把 sort 稳定性列为「严格」——文档说的是一回事,代码是另一回事。
2. **移植**:按 `ltablib.c` 逐行搬,保留哪些值重新从表里读、哪些沿用栈上读到的值,每次比较的操作数顺序,
   `set2` 的写入顺序,`i > u` / `j < l` 的检查在比较之后。17 条手写用例(NaN 组合、比较序列、恒真比较器、
   `<=` 比较器越界读到 nil、比较器中途报错、比较器观察和修改表、相等键)与 `lua5.1` 逐条一致,旧实现 13 条不一致。
3. **顺带查出的比较规则**:混合类型用例里 `table.sort({1, "a"})` 报 `two string values`,追到 `LessThan`
   不论类型一律报 `two X values`;再对照 `lvm.c` 发现 VM 自己的 `doCompare` 也不对:类型不同时没有先报错,
   处理函数「先查左、没有再查右」,不比较两边是否相同。改成照抄 `luaV_lessthan` / `lessequal` /
   `call_orderTM` / `luaG_ordererror`(后者只比较类型名第三个字母,`string` 与 `thread` 撞在一起),
   `LessThan` 委托过去。P3/P4 的比较慢路径都经 `doCompare`,一处修好三层。
4. **对抗排列**:旧的归并排序在任何输入上都是 O(n log n),快排不是。用 McIlroy 的对抗构造实测 3000 个元素
   225 万次比较(正常排列 3.5 万),预付的 `n*log2(n)` 管不住,加了超出四倍后逐次计费的后备。
5. **独立审查**(子代理,逐行对照 `ltablib.c` / `lvm.c` 并做差分):移植与比较规则本身无误,另找出三处,
   见下面教训 2、3 和「其余」。修完再送同一审查者复核。

## 教训

### 1. 设计稿写了「照抄参照实现」不等于实现照抄了

10 §7.3 从 P1 起就写着移植 `auxsort`,还专门讨论了比较器重入与 GC 纪律;实际代码是 `sort.SliceStable`。
差分测试(`difftest`、官方 `sort.lua`)长期不报,是因为它们的输入全是严格弱序,两种算法在严格弱序下
结果相同(除了相等元素的相对顺序,而 `sort.lua` 只检查有序性)。**文档声称的对齐,要用一个只有在真的
对齐时才成立的输入去核对**:这里是 NaN 或不一致的比较器,它们让两种算法给出不同结果。自查办法:读到
「对齐 X 的实现」这类说法时,问一句「哪个输入能区分照抄了和没照抄」,写进测试。

### 2. 把 C 栈局部搬成 Go 局部时,问 C 栈还顺带提供了什么

`auxsort` 把 pivot、`a[i]`、`a[j]` 用 `lua_rawgeti` 推到 C 函数 `sort` 自己的 Lua 栈上,跨多次比较持有;
那个栈是 GC 根。移植成 Go 局部变量后,这个「顺带的 GC 根」没了:比较器把表清空、把参数置 nil、
`collectgarbage()`,下一次比较就读到已释放的对象,纯 Lua 就能让 VM panic。旧实现同样有这个洞(Go 切片也不是根),
设计稿 10 §7.4 还专门论证过「比较时元素都在表槽里,无需 Pin」——论证前提是比较器不动表,而 §7.4 第 3 条
自己就承认比较器可以改表,只是把它归成「与 5.1 一致即可」的语义问题,没看出它也是内存安全问题。
修法是 `tableSorter.hold` 对跨比较持有的可回收值逐个 `PinRef`。**判据**:把参照实现里压栈 / 存进 registry
的值改存到宿主语言的局部变量时,逐个问「在这段时间里会不会运行用户代码(比较器、元方法)」,会就要固定。

### 3. 换算法时,把旧算法顺带提供的保证列出来

归并排序换成快排,换来了逐字节一致,也丢了「任何输入都是 O(n log n)」。第一版只想到了步数预算
(加了逐次计费),审查实测出另一半:排数字时不运行 Lua 代码,没有指令边界替它查 `SetContext`,20000 个元素
的对抗排列在 500ms 超时下跑满 1.55s。补了每 4096 次比较查一次取消。**判据**:替换一个算法时,把旧算法的
复杂度、稳定性、是否分配、是否会运行用户代码逐项列出,每一项问「新算法还成立吗,不成立时谁替它兜底」;
步数预算和取消是两个独立的兜底,只补一个不够。

### 其余

- 默认比较器的报错在 5.1 里是 C 函数内部抛的,不带位置;望舒的宿主调用边界给它补了 Lua 调用方的行号
  (存量)。第一版回归测试用正则去掉了位置前缀再比较,恰好把这个差异藏了起来——**测试里为了「不依赖
  chunk 名」去掉前缀时,要单独留一条断言前缀有无的用例**。
- 第二轮复核找出 `MarkAnnotated` 会写全局共享的 yield 哨兵(`__lt = coroutine.yield` 时 `LessThan` 返回它),
  `-race` 报数据竞争。追下去发现 master 上元方法调用边界的 `e.argNarg = 0` 也写同一个对象,普通的 `A < B` 就能触发,
  一并修掉(两处都跳过哨兵),补了两个 State 并发跑的 `-race` 回归。**判据**:一个哨兵 / 单例错误对象一旦是包级
  变量,任何「给错误对象打标记」的代码都要先排除它——`annotateError` 和 `resolveArgError` 早就排除了,
  后加的写入点没跟上;加新的写入点时 grep 一下 `errYieldSentinel` 看已有的写入点是怎么防的。
- 同一处还暴露出语义问题:处理函数直接是 `coroutine.yield` 时望舒会让协程越过元方法边界挂起,lua5.1 报错——开了 #272。
- master 上就有、与本轮无关的其他差异(比较器里 yield 的报错位置、`__lt` 里 `error(m, 2)` 的层级、`next({}, {})`
  的位置前缀、traceback 少一行 `[C]: in function 'sort'`)登记进 doc-gaps,没在本轮修。

## Promotion 决策

- 教训 1、2 → [[cross-backend-semantic-fix-sweep]]「PUC 语义由 C 实现定义」一节加第九个刻度:照抄参照实现时,
  参照实现里「值放在哪」本身带着语义(C 栈 = GC 根),搬到宿主局部变量要补上;文档声称的对齐要用区分输入核对。
- 教训 3 留在本篇。
