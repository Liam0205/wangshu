---
name: 2026-10-01-issue272-273-yield-base-and-promotion-replay
description: >
  #271 一轮审查顺带开出的两个存量问题，一起修(分支 `fix/272-273-yield-boundary-and-p4-tailcall`)。
  #272:元方法处理函数直接是宿主函数 `coroutine.yield` 时，协程在比较中途挂起(lua5.1 报
  `attempt to yield across metamethod/C-call boundary`)。设计稿 08 §5.2 写了 nCcalls 基线比对，实现从没做，
  唯一的拦截在 `callLuaFromHost`,只认「从 Lua 函数冒出来的哨兵」。照 `ldo.c` 补 `baseCcalls`,在 `State.Yield`
  里先比较；主线程措辞随之改成 5.1 唯一的那一句。查语义面时顺带发现 `return coroutine.yield(x)` 根本不能恢复
  (宿主尾调用没记 resume 点),官方 closure.lua 正好测这个，但在 setfenv 豁免的截断点之后。
  #273:P4 下函数升层的那一次调用(auto 下第 `hotEntry` 次、force-all 下第一次)仍由解释器执行；它以宿主尾调用收尾时,TAILCALL 分支的
  「尾调用 gibbous 分发」没判断是否进入了新帧，把调用者自己刚装好的原生码从 pc 0 再跑一遍,TAILCALL 前的副作用
  执行两次。修法是分发条件加 `next != nil`。教训：
  **一个「在刚进入的帧上做 X」的分支，要先证明确实进入了新帧——同一变量在另一条路径上仍指向旧帧**;
  **升层的那一次调用由解释器跑完，而 proto 已经有原生码——每个函数只经过一次，测试要在这一次里放可计数的副作用**;
  **测试豁免截掉的那一段，等于没有测试，豁免点之后的用例要另找地方跑**。
metadata:
  type: reflection
  date: 2026-10-01
---

# yield 的基线检查与升层那一次调用的重跑(2026-10-01,issue #272 / #273)

> 范围：分支 `fix/272-273-yield-boundary-and-p4-tailcall`。改动在 `internal/crescent/coroutine.go`(`baseCcalls`、
> `State.Yield`)、`internal/crescent/call.go`(`doTailCall` 宿主尾调用的 resume 点)、`internal/crescent/execute.go`
> (TAILCALL 分支的 gibbous 分发条件);新测试 `test/regression/issue272_yield_boundary_test.go`、
> `test/regression/issue273_tailcall_promotion_p4_test.go`;设计稿 08 / P3 04、07 / P1 与 P4 implementation-progress。

## 过程

1. **#273 先定位路径**。debug runner 打开 `peroptranslator` 的计数器：调用 200 次时 `PromotionCount=1`、
   `NativeRunCount=1`、`TailCallRunCount=1`——第 200 次调用里原生码跑了一次。199 次全对、200 次起多 1、只多一次，
   说明多出的执行只发生在升层那一次。`OnEnterID` 在 `enterLuaFrame` 末尾，升层时这一帧已经建好、由解释器执行；
   解释器跑到 TAILCALL,`doTailCall` 调宿主函数返回 `next == nil`,随后的分发查 `GibbousCodeOf(proto)`——这时
   `proto` 还是调用者自己，刚刚装好了原生码。一行 `next != nil` 修好，两种表现(计数多 1、GC stress 下索引已回收的表)
   同时消失。
2. **#272 先测绘语义面再动手**。写了覆盖 pcall、元方法、比较器、gsub、for-in 迭代器、`__call`、嵌套协程、主线程的
   探针，与 `lua5.1` 对照，发现三类差异：元方法 / 比较器直接用 `coroutine.yield` 会挂起；主线程措辞不同；以及意料之外的
   **`return coroutine.yield(x)` 第二次 resume 失败**。最后这一类比 issue 本身影响大得多。
3. **对照 `ldo.c` 实现**:`lua_resume` 里 `L->baseCcalls = ++L->nCcalls`,`lua_yield` 判 `nCcalls > baseCcalls`。
   望舒 `Resume` 的 `nCcalls++` 之后记 `co.baseCcalls`,`Yield` 先比较。`__call = coroutine.yield` 是普通调用、合法，
   嵌套在元方法里的新协程有自己的基线，两者都按 lua5.1 继续工作。
4. **尾调用 yield**:`doTailCall` 的宿主分支照 `doCall` 记 `pendingResume`,nresults 为 multret,由紧随的
   `RETURN A 0` 从 top 返回(对应 `ldo.c` resume 时 `luaD_poscall(LUA_MULTRET)`)。

## 教训

### 1. 「在刚进入的帧上做 X」之前，先证明进入了新帧

TAILCALL 分支的注释写的是「run it on the tail-call frame we just entered」,代码却把 `ci = currentCI(th)` 的结果
无条件当成新帧。`doTailCall` 有两种返回：Lua 尾调用返回新帧，宿主尾调用返回 nil、`ci` 留在调用者。分发代码只看
「这个 proto 有没有 gibbous 码」,而在升层那一次调用里，调用者自己的 proto 恰好刚有了。**判据**:一个分支的动作
以「刚发生了某个状态转换」为前提时，条件里要直接检查那个转换的结果(这里是 `next != nil`),不能用一个在转换
没发生时也可能为真的派生量(「proto 有 gibbous 码」)代替。

### 2. 升层的那一次调用，每个函数只经过一次

升层检查在帧建好之后，所以升层那一次调用剩下的部分由解释器执行，而 proto 已经有原生码。force-all 下这是每个
函数的第一次调用,auto 模式下是第 `hotEntry` 次——两种模式都有这个窗口(修复前 force-all 跑 issue 里的脚本同样
得到 `1001`)。起初我以为 force-all 没有这个窗口，写进了设计稿和 guide;审查指出测试的断言是空的之后，按变异
重跑时 force-all + GC stress 两格也失败，才更正。这个窗口每个函数只经过一次，要求那次调用恰好以宿主尾调用收尾、
之前有可见副作用，重跑才看得出来；已有差分测试与 nightly fuzz 为什么没有生成过这种写法，没有追查。

同一处还有一个测试自身的问题：第一版测试用 `got[0].Str()` 比较返回值，而 `Value.Str()` 对数字返回空串，返回数字
的两个用例比较永远相等——issue 原文的复现脚本恰好就是其中之一。审查者把测试放到未修的代码上跑，auto 格照样通过。
改为 `Display()`,并让 P1 的结果也对固定期望值断言，变异实测 4 格失败。

**判据**:为 tier 切换写测试时，要有一格让被测写法在升层那一次调用里就有可计数的副作用，断言次数，两种模式
各一格并断言 `PromotionCount > 0`;断言写好后，在去掉修复的代码上看它确实变红，比较函数本身也算在被验证的范围里。

### 3. 测试豁免截掉的那一段，等于没有测试

官方 closure.lua 的「yields in tail calls」(第 220 行)在 `stopAt` 豁免的截断点(第 163 行，setfenv)之后，望舒从未
跑过；被截掉的不只是 setfenv 那十几行，而是第 178–407 行整段协程测试。`return coroutine.yield(x)` 这种常见写法因此
一直不能恢复，没有任何测试报出来。修好后把这一段单独拿出来跑,P1、P4 auto、P4 force-all 都能跑完，修复前的 P4
在「yields in tail calls」那里报 `cannot resume: no pending yield point`。旧文档 [[prove-the-path-under-test]] §1 还拿「closure.lua
含协程多值 yield/resume 组合且字节级一致通过」当覆盖度充分的例子——那一段其实从没跑过。**判据**:给官方套件加截断豁免时，
扫一眼截断点之后还有哪些与豁免原因无关的用例，把它们单独放进 regression;豁免注释里写清截掉了什么。

### 其余

- 设计稿 08 §5.2 早就给出了正确的检测方法(nCcalls 基线比对),实现走了另一条只覆盖部分路径的捷径。与 #271
  「设计稿写了照抄 auxsort、代码没照抄」是同一类：**设计稿描述的机制，要用一个只有机制真的存在时才成立的输入核对**。
- 主线程 yield 的措辞，设计稿把 5.2+ 的 `attempt to yield from outside a coroutine` 当成 5.1 的，标了「待核对」一直
  没核对。`lua_yield` 只有一个检查，所以 5.1 只有一种措辞。

## Promotion 决策

- 教训 1、2 → [[prove-the-path-under-test]] §2.2b:「在刚进入的帧上做 X」要直接检查进入了新帧;tier 切换测试要在
  升层那一次调用里放可计数的副作用，并在去掉修复的代码上确认断言变红。
- 教训 3 → [[test-layout]]「新增测试往哪放」第 6 条：官方套件截断豁免之后的无关用例要单独补进 regression。
