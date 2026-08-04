---
name: 2026-08-04-issue228-229-lazy-capture-and-nested-tailcall-top
description: >
  两个 nightly 自动开的 fuzz crasher（#228 走 `FuzzOracleDiff`、#229 走 `FuzzP4ForceAllPromote`）的处理轮，
  分支 `fix/228-229-fuzz-crashers`，1 个 commit `a11334b`。**两个都在当前 master 上如实复现、都是真缺陷，
  而且互不相关**——两个 run 都在 `efa9aa6` 上，但版本核对这次不是关键，关键是两个都真复现。
  **#228：捕获的错误抬得太早。** seed 是 `print(string.gsub("","(",0))`，lua5.1 返回 `0 1`、望舒抬
  `unfinished capture`。根因是 PUC 在 `push_onecapture`（也就是捕获**真的被读出来**的时候）才报这个错，
  而 `add_value` 只有三条路径会走到那里——表替换、函数替换、`%n` 展开；纯字符串或数字替换走 `add_s`，
  只展开 `%n`、从不读捕获，所以 lua5.1 里 `gsub("abc","(","r")` 正常返回 `"rarbrcr", 4`，同一个 pattern
  给 `match` / `find` 才抬错。望舒在 `collectCaptures` 里就抬了，于是所有消费者都变成早抬。修法是
  `capResult` 加 `unfinished` 标记，由 `capsToValues`（这个文件里对应 `push_onecapture` 的函数）在**读取时**
  抬错。**我第一版修错了**：我把检查只放在 `%n` 展开那条路径，而**表替换会无条件读捕获 1**（PUC 的
  `add_value` 在 `lua_gettable` 之前就调了 `push_onecapture`），于是 `gsub("alo","(.",{})` 本该抬错却成功了
  ——**是官方测试套 `pm.lua:193` 抓到的，不是 oracle seed**，那个 seed 只覆盖「不该抬错」这一侧。
  **#229：嵌套尾调用返回后没恢复调用方的 top。** seed 最小化到
  `o2={n=function() return 0 end} function f(b) return b:n() end for A=0,70 do f(o2) A={0} end`，P1 成功、
  P4 抬 `SETLIST: not a table`。**诊断过程本身是教训**：我先后排除了三个「看着很像」的方向——
  peroptranslator 里那段注释正好写着 `NEWTABLE head + SETLIST → "SETLIST: not a table"`（探针一行没打出来，
  那条路径根本没走到）、stale base（每个副作用后都 `RefreshJitCtxAddrs`，不解决）、最后在 `doSetList` 的
  抬错点打栈迹拿到 `executeLoop -> doSetList`、**没有任何 JIT 帧**，才明白抬错的是普通解释器、破坏发生在
  更早的某个已升层调用里。真因是三步链：gibbous 的 `TailCall` helper 为了同步跑完 Lua 尾调用链，以
  `entryDepth = ciDepth-1` 开了一层**嵌套** `executeFrom`，于是「离开这一层的 entry 帧」**不等于**
  「离开最后一个 Lua 帧」——下面还压着活着的 caller 帧，它马上要继续解释执行；而 `doReturn` 在终止分支
  仍然把 top 收窄到 `dst + wantedN`，把 caller 的活寄存器留在 top 之上，GC 的栈根扫描
  （`visitThreadValues`）把 `[top, size)` 当陈旧残留清成 nil，于是 `A={0}` 的 NEWTABLE 结果被清掉、紧随的
  SETLIST 报「not a table」。`tag=65528` 就是 `value.TagNil`，这条线索直接指向 GC 清理。修法在终止分支里
  区分「真的没有 caller 了」和「还有 caller 在下面」，后者恢复成 caller 的逻辑帧顶——对照 PUC `lvm.c` 的
  `OP_RETURN`「`if (b) L->top = L->ci->top`」，PUC 从不为 Lua callee 重进一层 `luaV_execute`，所以这笔恢复
  天然归 RETURN 自己做；同一个仓库里另外**三处**兄弟路径（`doReturn` 自己的非终止分支、gibbous 的
  `DoReturn`、`callHost`）早就在做一样的恢复，这次只是补齐第四处。两个回归测试都用「把修复还原掉」实测
  确认过会变红。五条教训：把错误从早抬改成懒抬要在**每个** materialize 点各加一次检查（两侧都要有用例）/
  fuzz seed 只覆盖它自己那一侧，改参照实现的语义边界之后必须跑官方测试套 / 「有一段既有注释正好描述了这个
  症状」是最容易走错的线索，先用探针证明那条路径真的被执行 / 抬错的位置不是缺陷的位置，栈迹里**没有**
  JIT 帧本身就是关键信息 / 一个共享层的恢复动作已有 N 处兄弟路径在做，第 N+1 处漏做就是缺陷。
metadata:
  type: reflection
  date: 2026-08-04
---

# 一个抬得太早的错误，与一个漏掉第四处的恢复（2026-08-04，分支 `fix/228-229-fuzz-crashers`）

> 范围：#228、#229 两个 issue，1 个 commit（`a11334b`）。产品侧改动落在
> `internal/stdlib/pattern.go`（`capResult` 加 `unfinished` 标记）、`internal/stdlib/stringlib.go`
> （`capsToValues` 改成返回错误，四个消费者各自抬）与 `internal/crescent/call.go`（`doReturn` 终止分支
> 恢复 caller 的 top）；测试落在 `fuzz_228_test.go` 与 `fuzz_229_test.go`；两个 seed 分别入
> `testdata/fuzz/FuzzOracleDiff/b38e375dae54ee4b` 与
> `testdata/fuzz/FuzzP4ForceAllPromote/6e4264d640c40292`。

## 任务

nightly 在两个不同的 target 上自动开了两个 crasher issue：#228 来自 p1 腿的 `FuzzOracleDiff`，
#229 来自 p4 腿的 `FuzzP4ForceAllPromote`。

## 本轮做了什么

### 0. 版本核对给出的信号这次不重要，因为两个都真复现

两个 run 的 headSha 都是 `efa9aa6`（在 PR #226 之后、#227 之前）。按
[[unreproducible-crasher-triage]]「第一步永远是版本核对」这仍然是第一步，但那一节
「三档是关于要不要复现一遍的分类，它不回答有没有缺陷」在这一轮又一次成立：在当前 master 上逐条
重放之后，**两个都如实 FAIL**，都是真缺陷。

同一篇 guide 的「一批 crasher 先问会不会被同一个改动一起解决」这次给出的是**否**：两个 seed hash
不同，触到的子系统一个是 stdlib 的 pattern 消费侧、一个是共享调用层的 RETURN，**互不相关**，只能
分头查。这个判断本身是便宜的——把两条 reproducer 各跑一遍，看它们失败在什么形式上（一条是
oracle 差分、一条是 P1-vs-P4 层间分歧），就够了。

### 1. #228：`unfinished capture` 抬得太早（`internal/stdlib/pattern.go` + `stringlib.go`）

seed 是 `print(string.gsub("","(",0))`。lua5.1 返回 `0 1`（零次替换、原串），望舒抬
`unfinished capture`。

按 [[cross-backend-semantic-fix-sweep]]「PUC 语义由 C 实现定义」的手法读 `_lua515/lstrlib.c`：这个
错误在 PUC 里是从 `push_onecapture` 抬的，也就是**一个捕获真的被读出来的时候**。而 `str_gsub` 的
`add_value` 只有三条路径会走到 `push_onecapture`：

| repl 的类型 | 走哪条 | 读捕获吗 |
|---|---|---|
| 表 | `add_value` → `push_onecapture(ms, 0, ...)` → `lua_gettable` | **读**，而且是无条件的 |
| 函数 | `add_value` → `push_captures` → `lua_call` | **读**，全部捕获 |
| 字符串 / 数字 | `add_value` → `add_s` | 只在遇到 `%n` 时才读 |

所以 lua5.1 里 `gsub("abc","(","r")` 正常返回 `"rarbrcr", 4`，而同一个 pattern 给 `match` / `find` /
`gmatch` 就抬错——**同一个 pattern、同一次匹配，抬不抬错取决于消费者要不要读那个捕获**。

望舒在 `collectCaptures` 里就抬了，于是所有消费者都变成早抬，`gsub` 那一条与 PUC 分歧。修法是把
判断和抬错分开：`capResult` 加一个 `unfinished bool` 字段，`collectCaptures` 只打标记；
`capsToValues`（`stringlib.go` 里对应 PUC `push_onecapture` 的那个函数）在**读取时**返回错误，
`find` / `match` / `gmatch` / gsub 的三条 repl 路径各自把它转成 Lua 错误。

### 2. 我第一版把检查放错了位置，官方测试套抓到了它

第一版我把错误一路推迟到 `%n` 展开那条路径去检查——理由是「字符串替换只在 `%n` 时读捕获」，这句话
本身是对的。漏掉的是**表替换那条路径会无条件读捕获 1**：PUC 的 `add_value` 在 `lua_gettable` 之前
就调了 `push_onecapture`，所以

```lua
gsub("alo", "(.", {})    -- lua5.1: 抬错（表替换读了捕获 1）
```

本该抬错，我的第一版让它成功了。**抓到它的是官方测试套 `pm.lua:193`**：

```lua
assert(not pcall(string.gsub, "alo", "(.", print))
assert(not pcall(string.gsub, "alo", ".)", print))
assert(not pcall(string.gsub, "alo", "(.", {}))
```

oracle 的那个 seed 只覆盖「不该抬错」这一侧，它天然不会告诉我「表替换必须抬错」。修法是给表替换
那条路径单独加一次检查（`key := capVal(0)` 之后立刻判 `capsErr`），`%n` 那条仍在自己的 return 前判。

### 3. #229：嵌套尾调用返回后没恢复调用方的 top（`internal/crescent/call.go` 的 `doReturn`）

seed 最小化到：

```lua
o2={n=function() return 0 end} function f(b) return b:n() end for A=0,70 do f(o2) A={0} end
```

P1 成功、P4 抬 `SETLIST: not a table`。

**这一条的诊断过程本身是教训。** 我先后排除了三个方向，每一个都「看着很像」：

1. **既有注释正好描述了我的症状。** `internal/gibbous/jit/peroptranslator/translator.go` 里那段
   `lastReplayPC` 降级机制的注释就写着 `NEWTABLE head + SETLIST → "SETLIST: not a table"`，
   是「deferred head 顺序危险」这个已有记录。我在那一层查了很久，**而探针一行没打出来**——
   那条路径根本没走到。
2. **stale base / 地址失效。** 在每个副作用之后都调 `RefreshJitCtxAddrs`，**不解决**。
3. **在 `doSetList` 的抬错点打栈迹**，拿到的是 `executeLoop -> doSetList`，**没有任何 JIT 帧**——
   于是才明白：抬错的地方是普通解释器，破坏发生在**更早**的某个已升层调用里。

真因是三步链：

- gibbous 的 `TailCall` helper（`internal/crescent/gibbous_host.go`）为了同步跑完 Lua 尾调用链，
  以 `entryDepth = th.ciDepth - 1` 开了一层**嵌套** `executeFrom`。于是「离开这一层的 entry 帧」
  **不等于**「离开最后一个 Lua 帧」——下面还压着活着的 caller 帧，它马上要继续解释执行。
- `doReturn` 在终止分支（`th.ciDepth <= entryDepth`）仍然把 top 收窄到 `dst + wantedN`，把 caller
  的活寄存器留在了 top 之上。
- GC 的栈根扫描 `visitThreadValues`（`internal/crescent/state.go`）把 `[top, size)` 当陈旧残留
  **清成 nil**（这一步本身是对的，对齐官方 `lgc.c` 的 `traversestack`，防的是死引用被后来升高的 top
  覆盖后又被当活根扫描）。于是 `A={0}` 的 NEWTABLE 结果被清掉，紧随其后的 SETLIST 报「not a table」。

我抓到的 `tag=65528` 就是 `value.TagNil`（`internal/value/value.go`：`TagNil = 0xFFF8`），这条线索
直接指向 GC 清理——不是「SETLIST 读错了寄存器」，是「那个寄存器被写成了 nil」。

**修法**：终止分支里区分「真的没有 caller 了」（`th.ciDepth == 0`，保持 `dst + wantedN`）和「还有
caller 在下面」（恢复成 `caller.base + MaxStack`，即 caller 的逻辑帧顶）。对照 PUC `lvm.c` 的
`OP_RETURN`：`if (b) L->top = L->ci->top`——PUC 从不为 Lua callee 重进一层 `luaV_execute`（Lua→Lua
是 `goto reentry`），所以这笔恢复天然归 RETURN 自己做；望舒因为嵌套了 `executeFrom`，这笔在终止
分支上漏了。

**同一个仓库里另外三处兄弟路径早就在做一样的恢复**：

| 站点 | 文件 | 什么时候恢复 |
|---|---|---|
| `doReturn` 的非终止分支 | `internal/crescent/call.go` | 定长 nresults，退到 caller 继续解释 |
| gibbous 的 `DoReturn` | `internal/crescent/gibbous_host.go` | 段内 RETURN 走 host helper |
| `callHost` | `internal/crescent/host.go` | 定长结果的 host 返回路径 |
| **`doReturn` 的终止分支** | `internal/crescent/call.go` | **本轮补上的第四处** |

`callHost` 那一处的注释里还写着它当年的症状（多值 CALL 留下低 top → `callLuaFromHost` 脚手架
覆写 TFORLOOP 三槽 → `pairs` 收到 number），来自 2026-06-12 测试加固轮
（[[2026-06-12-test-hardening-round]] 教训 2）。也就是说这条纪律在本仓已经有过一次实证，本轮是
**第四处**。

**是既有缺陷、与 #228 无关**：把 #228 的改动 stash 掉之后同样复现，从 `origin/master` 就在。

**只有定长 nresults 会走到那个分支**这一点要写清，否则修法会误伤真正的 host→Lua 边界：
`callLuaFromHostNamed` / `execute` / 协程 resume 都是 `enterLuaFrame(entry=true)` 且传
`nresults=-1`，走的是上面 `wantedN < 0` 那一支，它的 `n := th.top - funcIdx` 结果窗口不受影响。

### 4. 验证

两个回归测试都用「把修复还原掉」实测确认过会变红——这是
[[unreproducible-crasher-triage]]「一个防住某个数值的回归测试必须用变异实测确认」在**防住某个行为**
上的同一条纪律。

- `fuzz_228_test.go`：四条「不该抬错」（seed 本体、字符串替换、数字替换、空主串）+ 六条「必须抬错」
  （函数替换、表替换、`%n` 展开、`match`、`find`、`gmatch`）。**两侧都有用例**是这一条的关键。
- `fuzz_229_test.go`：P1 与 P4 forceAll 两路跑同一段脚本比结果。注释里写清了三个都不能省的形状
  要素——多返回值的尾调用（`return b:n()` = SELF + TAILCALL + RETURN B=0，才会走 `TailCall` helper
  的嵌套 `executeFrom`）、caller 要热到会升层、循环体要构造**带元素**的表（才有 NEWTABLE 结果落在
  收窄后的 top 之上、后面才有 SETLIST 去读它）。
- 官方测试套 `test/luasuite`（`pm.lua` 整文件在内）全绿——这一步是第 2 节那个错误的直接产物。

## 期望与实际

| 期望 | 实际 |
|---|---|
| 两个 crasher 大概有一个是过期的 | 两个都在当前 master 上如实复现，都是真缺陷，而且互不相关 |
| #228 是「少了一个检查」 | 是「检查放在了错误的时机」——早抬 vs 懒抬，而懒抬需要在每个读取点各放一次 |
| 修好 oracle 报的那条就收工 | 第一版修法让官方套的另一侧变红；seed 只覆盖单侧 |
| #229 是 JIT 编译 SETLIST 编错了 | 抬错的是普通解释器，破坏在更早的已升层调用里；栈迹里没有 JIT 帧是关键信息 |
| 那段既有注释精确匹配症状，应该就是它 | 探针证明那条路径根本没被走到 |
| #229 是一个新机制的新缺陷 | 是一个已有纪律的第四处漏做，前三处早就在做 |

## 教训

### 教训 1（把一个错误从「早抬」改成「懒抬」，要在每个 materialize 点各加一次检查，而不是在恰好被报告的那条路径上加一次）

我把 #228 的检查放在 `%n` 展开那条路径，而表替换会**无条件**读捕获 1，于是
`gsub("alo","(.",{})` 本该抬错却成功了——官方套 `pm.lua:193` 抓到了它，而 oracle 的 seed 只覆盖
「不该抬错」那一侧。

**为什么容易错**：「懒抬错」这个改动的心智模型是「把 `return err` 往后挪」，而它实际上是
「把一个点变成一个集合」——错误从一个抬出点变成 N 个读取点，N 由**消费者**决定而不是由被改的那个
函数决定。fuzz 报上来的是其中一个消费者，改完它就觉得改完了。

**判据**：把一个错误从早抬改成懒抬时，先列出**所有会 materialize 那个值的路径**（这里是
`capsToValues` 的四个调用点，其中 gsub 内部又分三条 repl 路径），逐个加检查；并且**两侧都要有
用例**——该抬的、不该抬的。自查办法：问「除了 fuzz 报的这条，还有谁会读这个值」，答不出全部名字
就还没有列完。

参照实现的位置本身就是清单：PUC 把这个错误放在 `push_onecapture` 里，那么「谁调
`push_onecapture`」就是清单，`grep` 一遍 `_lua515/lstrlib.c` 比凭记忆枚举可靠。

### 教训 2（fuzz 的 seed 只覆盖它自己那一侧；改参照实现的语义边界之后要跑官方测试套）

oracle 差分只告诉我「`gsub("","(",0)` 不该抬错」，它天然不会告诉我「`gsub("alo","(.",{})` 必须
抬错」——那条在 `pm.lua` 里。

**为什么容易错**：差分 fuzz 是**单向**的证据源——它报的是「望舒抬了而 lua5.1 没抬」或者反过来的
某**一**个具体输入，而一次语义边界的移动同时改变边界两侧的行为。官方测试套是**双向**的：它同时
断言哪些必须成功、哪些必须失败，因为它就是为「这个边界在哪」写的。

**判据**：改动参照实现的语义边界（接受面、抬错时机、默认值、上限）之后，除了 seed 与差分测试，
必须跑 `test/luasuite`。自查办法：写下「我把这个边界从 X 挪到了 Y」，然后问「Y 那一侧有测试吗」
——如果这一侧的用例全来自 fuzz seed，那就只有一侧。

这与 [[prove-the-path-under-test]] §4 「覆盖度先 grep 既有 oracle 再决定是否补语料」是同一件事的
另一个入口：那条讲补语料前先看仓里有没有，本条讲改边界后必须去跑那个已有的。

### 教训 3（「有一段既有注释正好描述了这个症状」是最容易走错的线索）

peroptranslator 里那段 `NEWTABLE head + SETLIST → SETLIST: not a table` 的注释精确匹配我的症状，
我因此在错误的层查了很久；而探针证明那条路径没被走到。

**为什么容易错**：一段既有注释同时提供了「症状描述」和「解释」，而且是仓库自己写的、可信度高，
读到它的瞬间搜索就停了。但注释描述的是**那个机制**下的症状，同一句错误消息可以由任何写坏那个
寄存器的东西产生——`SETLIST: not a table` 只说明 SETLIST 读到的不是表，不说明是谁让它不是表。

**判据**：看到一段既有注释匹配症状时，**先用探针证明那条路径真的被执行**，再开始在那里查；
症状相同不等于路径相同。自查办法：在动手读那段代码之前先加一行打印或计数器，跑一次 reproducer
看它是否被打到。

这与 [[prove-the-path-under-test]] §7「退化归因前先证被怪罪的路径存在」同源，只是那一节的归因
来自 issue 或外部 review，本条的归因来自**仓库自己的注释**——后者更难怀疑。

### 教训 4（抬错的位置不是缺陷的位置）

#229 的错误由普通解释器抬出，而破坏发生在更早的已升层调用里；栈迹里**没有** JIT 帧这件事本身
就是关键信息——它把搜索范围从「JIT 怎么编译 SETLIST」翻转成「谁在 SETLIST 之前动了那个寄存器」。

**为什么容易错**：报错点是唯一直接给到的坐标，从它开始读代码是最自然的动作；而对「读到坏数据」
这一类缺陷，报错点恰恰是**唯一确定没有 bug** 的地方——它正确地发现了数据不对。

**判据**：对「X 报错」类缺陷，先问「X 读到的坏数据是谁写的」，而不是「X 自己哪里写错了」。
`tag=65528` 这种具体坏值往往能直接指认写入者（这里是 GC 清成 nil，因为 nil 是 GC 的清理值而不是
任何一条指令的自然产物）。自查办法：把坏值打出来，问「什么东西会写出这个值」——如果答案是
「某个清理动作」，那就去找那个清理动作的边界条件。

栈迹里**缺少**某一层同样是证据。这一条与 [[prove-the-path-under-test]] §7 的分诊侧实例
（`NativeRunCount` 探针一次读数把定位面从三类 code kind 收窄到一类）是同一个手法的反面：那里靠
探针**确认**走了哪条路，这里靠栈迹**排除**了一整层。

### 教训 5（一个共享层的恢复动作，如果已有 N 处兄弟路径在做，那么第 N+1 处漏做就是缺陷）

`doReturn` 的终止分支是第四处，前三处（`doReturn` 自己的非终止分支、gibbous 的 `DoReturn`、
`callHost`）早就在恢复 caller 的 top。

**为什么容易错**：每一处看起来都是「这条路径的局部细节」，而实际上它们在实现同一条调用约定
（PUC 的 `L->top = L->ci->top`）。新增一条返回路径时，作者关心的是「返回值搬对了吗」，而 top 的
恢复不影响返回值、只在 GC 跑起来之后才暴露，所以漏做不会立刻有症状。

**判据**：改共享层时，grep 同一个恢复 / 清理动作的所有出现处，数一数是不是所有该做的路径都做了
——数量不齐就是信号。自查办法：`grep` 那个动作的特征表达式（本轮是
`th.setTop(... .base + int(st.protoOf(...).MaxStack))`），把命中的站点与「所有会退出一层 Lua 帧的
路径」两张清单对照。

这是 [[design-claims-vs-codebase-physics]] §4.1「新对象类型的分配路径必须照抄同族分配器的每一步」
的**恢复侧对偶**：那一条讲同族分配器里出现三次的动作（`AllocX` + `LinkSweep` + `AllocCharge`）
是契约，本条讲同族返回路径里出现三次的恢复动作也是契约。两条共享同一条元纪律——**同族里重复出现
的动作是契约，不是那几个函数各自的选择**，而且两者的症状都离原因很远（那边 panic 在 GC 里、错误在
分配处；这边报错在 SETLIST、错误在 RETURN）。

## Promotion 决策

- **教训 1 / 教训 2 → [[cross-backend-semantic-fix-sweep]]**：「PUC 语义由 C 实现定义」那一节的
  第六个刻度（错误抬出的**时机**也是要照抄的东西，而懒抬需要在每个读取点各加检查）+「执行体纪律」
  一侧的验证面条款（改语义边界之后必须跑官方套，因为 seed 单向、官方套双向）。前五个刻度分别讲
  值的转换链、上限的条件项、消息的包装层、有定义 / UB 四格、校验在控制流里的位置——本条讲**抬错点
  在调用链里的位置**，与第五个刻度相邻但不同：那条管「校验写在循环外还是循环内」，本条管「错误从
  哪个函数抬出来」。
- **教训 3 / 教训 4 → [[prove-the-path-under-test]]**：§7 诊断侧那一族的两个新小节。§7.2 是
  「既有注释匹配症状时先证路径被执行」（§7 主体管外部归因、本节管仓库自己的注释），§7.3 是
  「抬错的位置不是缺陷的位置」（含「栈迹里缺少某一层也是证据」与「具体坏值指认写入者」两个手法）。
- **教训 5 → [[cross-backend-semantic-fix-sweep]]** 的不对称家族（与「运行时断言接口的扩面不对称」
  「同族 harness 防护不对称」并列的第三种：共享层恢复动作的兄弟路径不对称），并与
  [[design-claims-vs-codebase-physics]] §4.1 互相指认。放在这里而不是 design-claims，是因为它的
  可操作动作是「枚举全部站点、不凭记忆」——那正是 cross-backend 那篇的主纪律，只是对象从「后端
  emit 站点」换成「返回路径」。
- **[[unreproducible-crasher-triage]]** 补一条：**两个都真复现的那一档要怎么继续**。前面各节写的
  都是「怎么便宜地判掉不需要复现的」，而这一轮两条都真复现，下一格便宜的检查是**按失败形式分诊
  入口**（oracle 差分 → 去读参照实现的语义边界；P1-vs-P4 层间分歧 → 先问谁写坏了数据，别从 JIT
  的 emit 读起）。

## 触发场景

- 想把一个错误从「早抬」改成「懒抬」（或者反过来）时 → 先列出全部 materialize 点，两侧都写用例。
- 改动了参照实现的某个语义边界（接受面 / 抬错时机 / 默认值 / 上限）之后 → 除了 seed 与差分，
  必须跑 `test/luasuite`。
- 读到一段既有注释精确描述了手头的症状时 → 先用探针证明那条路径真的被执行，再在那里查。
- 拿到「X 报错」类缺陷时 → 先问「X 读到的坏数据是谁写的」；把坏值打出来问「谁会写这个值」。
- 看栈迹时 → 缺少哪一层也是证据，不只看有哪一层。
- 给共享调用层加一条新的返回 / 退出路径时 → grep 同族路径的恢复 / 清理动作，数一数齐不齐。

## 关联

[[unreproducible-crasher-triage]]（第一步永远是版本核对——这一轮两个都真复现，是「三档不回答有没有
缺陷」的又一个实例；「一批 crasher 先问会不会被同一个改动一起解决」这次给出的是否，两个互不相关）·
[[cross-backend-semantic-fix-sweep]]（「PUC 语义由 C 实现定义」是 #228 的定位手法；教训 1/2/5 的落点）·
[[prove-the-path-under-test]]（§7 诊断侧对偶是教训 3/4 的落点；§4 覆盖度先 grep 既有 oracle 是教训 2
的另一个入口）· [[design-claims-vs-codebase-physics]]（§4.1 同族分配器每一步是教训 5 的分配侧对偶）·
[[2026-06-12-test-hardening-round]]（教训 2 的 `callHost` top 恢复就是本轮第四处的前三处之一，
症状离根因极远这一点两轮一致）· [[2026-08-02-issue212-219-fuzz-crasher-batch]]（#216 也是 gsub，
也是「检查放错位置」——那次是控制流位置、这次是抬出点位置，两条相邻）·
`internal/stdlib/pattern.go::collectCaptures` · `internal/stdlib/stringlib.go::capsToValues` ·
`internal/crescent/call.go::doReturn` · `internal/crescent/gibbous_host.go::TailCall` ·
`internal/crescent/state.go::visitThreadValues` · `test/luasuite/testdata/pm.lua` ·
`docs/design/p1-interpreter/10-stdlib.md` §6.4.1（捕获的懒抬错）·
`docs/design/p1-interpreter/05-interpreter-loop.md` §7.2.1（嵌套 executeFrom 的 top 恢复契约）·
`docs/design/p4-method-jit/implementation-progress.md` §27

## 审计发现:位置对了,粒度错了

审计指出我的 #228 修法**还没做完**:把抬错从 `collectCaptures` 挪到 `capsToValues` 是对的,
但我保留了**整份捕获列表**的粒度,而 PUC 的 `push_onecapture` 是**按单个下标**抬的。

于是「已闭合捕获 + 尾部未闭合捕获」的混合写法,在只读单个下标的两条路径上多抬了错 ——
五种写法各自被 lua5.1 正常处理:`gsub("alo","(.)(","<%1>")` 是 `<a><l><o>`(因为 `%1` 只读捕获 1,
根本不碰未闭合的捕获 2)、`"((.)"` 配 `%2`、两个闭合捕获接一个未闭合、表替换的键、
以及位置捕获接未闭合。

修法:只读单个下标的两条路径(`%n` 展开、表替换的键)走新的 `capOneValue`,只检查那一个下标;
读全部捕获的四条(`find` / `match` / `gmatch` / 函数替换)保留整列表检查 —— 于是混合写法对它们**仍然**抬错,
与 lua5.1 两侧都一致。记忆化也从「按列表」改成「按下标」,保住了上一轮加它的理由。

追加一条教训:

6. **把一个错误挪到正确的函数里,不等于挪到了正确的粒度上。** 我照着参照实现找对了抬错的函数
   (`push_onecapture`),却没注意它的**签名**就说明了粒度——它接受一个下标、一次只处理一个捕获。
   判据:照抄参照实现的抬错位置时,连它的**参数**一起看:参照实现处理的是单个元素还是整个集合,
   决定了检查该放在循环里还是循环外。第 1 条教训说「懒抬要在每个 materialize 点各查一次」,
   这一条是它的下半句——**每个点查的范围也要与参照实现一致**。

## 后续审计:粒度之外还有顺序、消息与零捕获特例

`c278326`、`98e0cf4`、`3f29924` 的连续审计说明上一节仍不是终点:

1. `%n` 展开虽然已改成按下标检查,却把错误记到扫描完整个 replacement 之后才抬。
   `gsub("ab", "(", "%1%2")` 因此让后到的 `%2` 覆盖先到的 `%1`:望舒报 `invalid capture index`,
   PUC 的 `add_s` 从左到右扫描并在 `%1` 立刻报 `unfinished capture`。反转引用顺序又应反转错误类别,
   所以正确规则不是「某类错误优先」,而是**首个被求值的失败优先**。修法是每次 `capVal` 后立即检查,
   不把错误延迟到循环末尾。
2. 同一张 pattern × replacement oracle 矩阵又发现三个 `invalid capture index` 站点中两个附加 `%n`
   后缀,而 PUC 与第三个兄弟站点都只报裸消息。说明「兄弟路径不对称」不仅适用于恢复动作,也适用于
   错误文本;修一个站点时应把同一消息的所有生产点一起列出。
3. 按下标记忆化分配 `len(caps)` 个槽时漏了「没有显式捕获时 `%1` 代表 whole match」的虚拟槽,
   让 `%1%1%1` 重复 intern。集合长度为零不代表可读取元素为零;容器的物理长度与 API 暴露的逻辑索引
   必须分开核对。
4. 扫描还发现 `find` / `match` / `gmatch` 共用的 init 只夹紧了下界,没有像 PUC `str_find_aux` 那样
   把过大位置夹到字符串末尾,因此漏掉末尾的零宽匹配。这不是原修法的回归,而是行为面矩阵在相邻维度
   找到的既有缺口;记录时要区分 regression 与 pre-existing gap。

追加三条稳定教训:① 懒抬语义要同时对齐**站点、粒度、求值顺序**,多错误输入专门验证「谁先抬」;
② 只比较成功 / 失败抓不到错误类别与消息差异,语义审计矩阵必须读取完整结果或错误文本;
③ 零长度集合若有隐式 / 虚拟元素,所有按长度分配的缓存都要单独验证零元素特例。这三条中的前两条已
同步到 [[cross-backend-semantic-fix-sweep]] 第六个刻度。
