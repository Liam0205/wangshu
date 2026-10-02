---
name: 2026-10-02-issue276-279-host-boundary-positions-and-traceback
description: >
  #271、#272 两轮登记在 doc-gaps 的存量差异转成 issue 后一起修(分支 `fix/276-279-host-boundary-error-positions`)。
  四个问题都出在宿主函数参与的调用链上:`coroutine.wrap` 少一层调用方位置(#276)、宿主函数触发的元方法处理函数里
  `error(m, 2)` 差一层(#277)、C 函数内部抛出的错误多了位置前缀(#278)、traceback 格式与生成时机(#279)。教训:
  **位置和层级取决于调用方是 Lua 帧还是 C 帧，判断「与 lua5.1 一致」必须两种调用方都测**(#202 把 wrap 一项判为不成立，
  只测了经 pcall 的写法);**延迟消费「当前栈」的状态会被中途的截栈改掉，traceback 要在出错点取**(R3c-fix 之后第二个样本);
  **force-all 用例不等于升层路径被测到，要确认被测的帧真的升了层**;
  **把某类错误统一冻结之前，先查有没有别的路径正好借这个错误拿到正确的位置**(本地审查发现 `__newindex = rawset` 因此回归)。
metadata:
  type: reflection
  date: 2026-10-02
---

# 宿主调用边界上的错误位置、层级与 traceback(2026-10-02,issue #276 / #277 / #278 / #279)

> 范围：分支 `fix/276-279-host-boundary-error-positions`。改动在 `internal/crescent/meta.go`、`errors.go`、
> `objname.go`、`call.go`、`execute.go`、`coroutine.go`、`state.go`、`gibbous_host.go` 与
> `internal/stdlib/coroutinelib.go`;新测试 `test/regression/issue27{6,7,8,9}_*_test.go` 与
> `internal/crescent/residual_test.go::TestTraceback_NotBuiltForCaughtErrors`;设计稿 08 / 09、P1 与 P3
> implementation-progress。对账表在 P1 implementation-progress 的 #276-#279 条目，这里只记过程和教训。

## 过程

1. **来源**。这几项是 #271 独立审查在 master 上复现、与那一轮无关的差异，当时决定登记 doc-gaps、本轮不修。
   用户随后要求把只记在文档里的差异都开成 issue,于是有了 #277-#279(#276 先开)。
2. **先扫再修**。doc-gaps 原条目提示「可能不止这几处」。用探针把会回调 Lua 的宿主函数(sort、gsub、foreach、
   pcall、wrap、for-in)和会在 C 函数内部抛错的入口(next、rawset、表索引)两两组合跑 lua5.1 对照，范围比 issue 原文大:
   #278 还包括 `gsub("a", "a", error)`、`sort` 的比较器是 `rawset` 这类宿主调宿主;#277 还包括 gsub 表替换值的 `__index`。
3. **#279 牵出两处顺带的问题**。traceback 从调用方当前指令推断函数名后，P3 的 `TestPW10R3_IndirectErrorByteEqual`
   失败：gibbous 的三个调用 helper 把 `ci.pc` 存成 CALL 本身，比解释器约定少一条(R3c-fix 当时只改了算术族)。
   另一处是 `pcall(coroutine.resume, co)` 留下的 `pendingHostFrames` 被协程第一帧吸收，协程栈底多出一行 `[C]: ?`。
4. **变异检查**。每项修复去掉后对应用例都失败，只有 gibbous `pc + 1` 例外：去掉它，新加的 #279 用例在 force-all 下
   仍然全过，是已有的 P3/P4 测试抓到的。没有逐个确认 #279 用例里哪些帧真的升了层。
5. **本地审查第一轮**(阻塞 1、重要 4、小问题 3)。阻塞项是本轮引入的回归:`RawSet` 一律冻结为不带位置后,
   `__newindex = rawset` 配 nil / NaN 键从 `x:N: table index is nil` 变成裸消息。根因是 `setIndexWithMeta` 先找
   `__newindex` 再检查键，基线上 rawset 的错误冒到赋值所在的 Lua 帧，凑巧补对了位置。重要项里，扫描清单漏了
   `print`(它不经全局 `tostring`、少一层 C 帧);`xpcall(f, debug.traceback)` 与 `debug.traceback(co)` 属于 #279
   范围却仍然不同，而 doc-gaps 已经整条标成完成;09 §8 的订正写了代码并没有的错误后缀;编译层回归测试实际没有升层。
   这些都在同一轮补修：键检查前移、`print` 走全局 `tostring`、`describeRegDepth` 用 `kname`、xpcall 的 handler
   改在出错点调用(`atRaisePoint`)、`debug.traceback` 支持协程参数(死于错误的协程保留出错时的栈)、新增
   `issue276_279_compiled_callers_test.go` 并断言 `PromotionCount`。
6. **本地审查第二轮**(阻塞 1、重要 1、小问题 2)。阻塞项又是本轮引入的：handler 挪到出错点调用后,`C stack overflow`
   发生时 C 调用深度正好在上限，调 handler 的入口检查立刻失败。5.1 的 `luaD_call` 为此留了八分之一的余量。重要项是
   load 的 reader 经 `ProtectedCallDirect` 调用、在 Go 侧吞掉错误，「协程将死」的判断却以为没有边界接住它。两项的共同点是
   **把一段代码挪到更早的时刻执行，它所处的状态(深度计数、有哪些边界)也跟着变了**,要按新时刻重新核对每个前提。
7. **本地审查第三轮**(重要 2、小问题 3)。第二轮把 C 深度检查统一成一个函数时，把 `Resume` 也接了进去，而
   `lua_resume` 用的不是 `luaD_call` 那套规则;新造的 `LUA_ERRERR` 错误对象忘了冻结位置。教训是**把多个检查点合并成
   一个 helper 之前，逐个对照 5.1 里各自的那一行**:同一个错误消息，在 `ldo.c` 里可能出自规则不同的几处。
8. **本地审查第四轮**(重要 1、小问题 3)。handler 能越过 C 上限之后，又发现一处读同一个深度的地方:`lparser.c` 的
   `enterlevel` 把语法层数记在 `nCcalls` 上。望舒的解析器自己数层数，与调用深度无关，所以在深处或 handler 里
   `loadstring` 能嵌套的层数和 lua5.1 不同。这说明**改变一个计数器的取值范围(handler 可以越过上限)时，要找出所有
   读这个计数器的地方**,不只是写它的地方。
9. **本地审查第五轮**(重要 2、小问题 1)。新的语法层数照搬了 `enterlevel` 的绝对边界，暴露出 `cCallCheck` 本身比
   `luaD_call` 宽一层(先比较再加一，而 5.1 先加一再比较),最深一层的 `loadstring("")` 因此失败。修的是检查本身。
   同轮还发现第四轮登记已知限制时的归因写错了:把 3 层差距整个算成 lua.c 的启动开销，实际启动开销是 2 层，另 1 层
   是这个边界。**登记「不打算修」的差异之前，先把差距逐层拆开、每一层都找到来源**;一个数字凑得上不等于解释对了。
10. **本地审查第六轮**(盲审小问题 3,补充新增重要 1)。补充用内嵌 oracle 查出第五轮的归因还是错了一半:lua.c 进入
    主 chunk 前的 2 层里，只有 `lua_cpcall(pmain)` 是 lua.c 独有的，另一层是宿主用 `lua_pcall` 运行 chunk 的那次调用，
    嵌入式 PUC 也有，望舒没计。于是让 `callOnStack` 计入这一层，并加了与 oracle 逐字节比较的测试。**判断「对方的差距
    来自参照实现的外壳」时，拿同一个参照实现去掉外壳的形式(这里是内嵌 oracle)再量一次**,别只和带外壳的二进制比。
11. **本地审查第七轮**(重要 1、小问题 1)。新加的 oracle 深度测试在 CI 里根本不会执行:oracle-smoke 只跑 fuzz 目标和
    点名的测试，而这是个普通测试。给它补 force-all 时又发现 P3 编译帧调 Lua 被调方时把 Go 重入记成了 C 层(master
    上就有),于是拆出 `luaReentry`。**新增一个只在特定 build tag 下编译的测试时，要确认 CI 里有哪一步会点名跑它**;
    能编译、本地能过，不等于有人在跑。

## 教训

### 1. 「与 lua5.1 一致」要按调用方种类分开测

5.1 加位置的规则只有两条:`luaG_runerror` 在当前帧是 Lua 函数时加;`luaL_where(L, 1)` 取调用方，调用方是 C 函数时给空串。
所以同一个错误，在 Lua 代码里直接触发和经 `pcall`、比较器、`gsub` 触发，位置可以不同。#202 把「`coroutine.wrap` 缺前缀」
判为不成立，用的探针是 `pcall(f)`,调用方正好是 C 函数，两边都只有一层；直接调用 `f()` 才看得出差别。

**检查方法**:判断位置、层级、函数名相关的差异是否成立时，至少跑两种写法——调用方是 Lua 帧(直接调用、尾调用、元方法)
和调用方是 C 帧(pcall、sort 比较器、gsub 替换函数)。只测一种得出的「一致」不能用来关 issue。

### 2. traceback 要在出错点取，不能等错误冒到顶层

原来未捕获错误的 traceback 在 `execute` 返回之后才生成，这时错误穿过的每个宿主边界都已经 `truncateCI`,比较器和
`sort` 都不在栈上了。这与 P3 R3c-fix 的教训 3 是同一种问题：一个机制延迟读取「当前栈」,另一个机制在这期间改了栈。
修法也一样，在出错点、栈还完整时取，用 `protectDepth` 判断有没有会捕获它的边界，避免给被 pcall 接住的错误白白生成。

这是第二个样本，可以考虑把它写进 [[design-claims-vs-codebase-physics]] §2:**延迟消费上下文相关的状态，要先列出
从产生到消费之间有谁会改这个上下文**。

### 3. force-all 用例要确认被测帧真的升了层(本地审查第一轮也指出了这一点)

#279 的用例在 P3/P4 下各跑 force-all 和不升层两遍，但 gibbous 调用 helper 的 pc 改动去掉后它们照样通过。force-all
只是打开升层开关，函数能不能升层还要过可编译性检查;traceback 用例的帧大多调用 `debug.traceback` 等宿主函数，
没有确认它们是否升层。以后给编译层加的用例，要么断言升层计数，要么做一次变异确认用例对编译层的改动敏感。这属于 [[prove-the-path-under-test]] 说的「绿色不等于在测你以为在测的路径」。

### 4. 冻结一类错误之前，先查谁在依赖它原来的位置

#278 的修法是在 `RawSet` 等入口把错误冻结为不带位置，这对「C 函数直接调用 rawset」是对的。但 `__newindex = rawset`
时，原来的正确结果其实来自一个凑巧:rawset 的错误冒到赋值所在的 Lua 帧，被补上了该行位置，而 5.1 里这个错误本来
就该在那一帧由 `luaH_set` 报出，根本不进 `__newindex`。冻结让凑巧失效，暴露出键检查的顺序错了。

**检查方法**:改变某个入口的错误标注方式时，列出所有会把这个入口当回调的写法(元方法处理函数直接是这个库函数、
`sort` / `gsub` / `foreach` 的回调是它),逐个和 lua5.1 比一遍，而不只看直接调用。

### 5. 扫描「会回调 Lua 的宿主函数」要按 5.1 的实现列，不按望舒的实现列

扫描清单是按望舒里谁调用了 `ProtectedCallDirect` 列的。`print` 在望舒里直接调内部转换函数，不像会多出一层;
5.1 的 `luaB_print` 却是 `lua_call` 全局 `tostring`。同类问题要从 `lbaselib.c` / `lstrlib.c` / `ltablib.c` 里所有
`lua_call` / `lua_pcall` 的调用点列清单。

## 触发场景

- 判断错误位置、层级、traceback 函数名的差异是否成立，或者写「已与 lua5.1 核对」之前。
- 修改宿主调用边界(`callLuaFromHost`、`ProtectedCall*`、`Resume`、元方法入口)上的错误标注或层级计数时。
- 一个机制在事后读取栈、帧或 pc,而中间可能经过截栈、弹帧时。
- 给 P3/P4 写 force-all 用例并据此声称编译层也覆盖到了时。
- 把某个入口的错误改成「冻结、不带位置」之前，或者扫描「会回调 Lua 的宿主函数」时。
