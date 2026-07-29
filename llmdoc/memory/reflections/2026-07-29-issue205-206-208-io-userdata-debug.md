---
name: 2026-07-29-issue205-206-208-io-userdata-debug
description: >
  三个 issue(#205 / #206 / #208)的处理轮,分支 `fix/205-206-208-io-stack`,3 个 commit。
  **#206**:`string.byte` 完全没有 `lua_checkstack` 上限,`string.byte(string.rep("a",9000),1,8000)`
  返回 8000 个值而 PUC 抬错;上限与 `unpack` 一样是 `8000 - nargs`,但**消息要包一层**——
  `luaL_checkstack` 把调用者给的文本套成 `stack overflow (%s)`,所以 PUC 输出的是
  `stack overflow (string slice too long)` 而不是裸字符串,我第一版上限对了、文本错了,65 个
  oracle 用例里仍有 12 个分歧。**#208** 已经被上一轮修掉了:它的 fuzz run 跑在 `cbd0512` 上,
  早于把 harness skip 降到 2^20 的 `c07ba58`,现在这个 seed 0.00 秒就跳过,作为回归防线留在
  corpus 里。**#205** 是上一轮撤回的那一块:根因很简单——原型直接调 `object.AllocUserdata`、
  **跳过了 collector 的 `LinkSweep`**,对象 header 里既没有颜色也没有 sweep 链,收集器根本看不见
  它;`crescent/alloc.go` 里每个分配器都是 `AllocX` + `LinkSweep` + `AllocCharge` 三件一起,
  现在加了 `State.NewUserdata` 把这一步固定下来。必须是真 userdata 而不是表,因为 PUC 报的是
  `userdata`、`type()` 会露馅,为此补了 `metaFieldOfValue` / `indexWithMeta` 两处 VM 缺口,
  `getmetatable` 对 userdata 也开始遵守 `__metatable`。56 个探针与 `lua5.1` 比对抓到四条细节,
  每一条我第一版都写错了(`FILE* expected, got X` / 只写句柄读出 errno 三元组
  `(nil,"Bad file descriptor",9)` / `debug.traceback` 传显式 nil 返回 nil / `getinfo` 的
  `function or level expected` 词序)。`io.read` 必须用真实二进制验证,因为 `go test` 不把 stdin
  转给测试进程,最初的探针全部返回 nil、量到的是 harness 而不是代码。harness 侧:加了
  `io.stdout` 之后脚本可以经一条**没有被捕获**的路径输出,两侧捕获里都缺那段文本、于是仍然
  「一致」——绿是因为错误的原因;而我第一版 wrapper 调了一个 prelude 里并不存在的 helper,
  两侧以完全相同的方式失败、比较照样报一致。五条教训:一个 `AllocX` 必须与本仓的注册步骤配对 /
  `go test` 不转发 stdin 故进程级 IO 要用真实二进制验证 / 新增输出路径必须同时接进捕获 /
  两侧在同一个错误上一致不等于行为一致 / 参照实现的错误消息可能被 `luaL_*` 包了一层。
metadata:
  type: reflection
  date: 2026-07-29
---

# file-handle userdata、`debug` 子集与一条没被捕获的输出路径(2026-07-29,分支 `fix/205-206-208-io-stack`)

> 范围:#205 / #206 / #208 三个 issue,3 个 commit。改动落在
> `internal/stdlib/stringlib.go`(`string.byte` 的上限与包一层的消息)、
> `internal/crescent/alloc.go`(`State.NewUserdata` + payload / meta 访问器)、
> `internal/crescent/meta.go`(`metaFieldOfValue` 与 `indexWithMeta` 的 userdata 分支)、
> `internal/crescent/errors.go`(`FrameInfo`,给 `debug.getinfo` 供料)、
> `internal/stdlib/tablelib.go`(三个标准流 + `io.read` / `io.lines` + `debug` 子集)、
> `internal/stdlib/stdlib.go`(`getmetatable` 认 userdata + 注册 `debug`)、
> `internal/oracle/prelude.go`(file-handle 的 `:write` 接进捕获累加器)、
> `test/difftest/corners_test.go`(三条豁免条目改写)、`io_handles_test.go`。

## 任务

三个 issue 性质不同:#206 是我顺着 vendored 源码查 `lua_checkstack` 调用点时发现的,#208 是
nightly 自动开的 go-fuzz crasher,#205 是上一轮(`fix/201-203-unpack-and-skip`)把 `io.stdout`
整段撤回时自己开的那一个。

## 本轮做了什么

### 1. #206 `string.byte` 缺 `lua_checkstack` 上限,而且消息被包了一层

`string.byte(string.rep("a",9000),1,8000)` 返回了 8000 个值,而 PUC 抬错。PUC 的 `str_byte` 调
`luaL_checkstack(L, n, "string slice too long")`,所以上限与 `unpack` 一样是 `8000 - nargs`
(三参数调用 7997 成功、7998 抬错)——这一条是上一轮 #201 已经量清楚的机制在另一个函数上的复用。

**新东西在消息上**:`luaL_checkstack` 会把调用者给的文本**包一层**成 `"stack overflow (%s)"`,
所以 PUC 输出的是 `stack overflow (string slice too long)`,不是裸的 `string slice too long`。
我第一版把上限写对了、文本照抄了裸串,65 个 oracle 用例里仍有 12 个分歧。`unpack` 那边不会
暴露这件事,因为 `luaB_unpack` 用的是 `luaL_error(L, "too many results to unpack")`——`luaL_error`
直出,不套格式。两个函数在这个维度上不一样,而它们的上限公式一样,所以照着 `unpack` 抄很容易
只抄对一半。

**顺着 vendored 源码把其余 `lua_checkstack` 调用点都查了**(这本来就是 #206 被发现的方式):
`string.find` / `string.match` 的 `too many captures` 阈值已经正确,`coroutine.resume` 的参数
路径也一致,没有别的要补。

### 2. #208 已经被上一轮修掉了

#208 是 `table.insert` 移位那一类的又一个 nightly crasher。它的 fuzz run 跑在 `cbd0512` 上,
**早于**上一轮把 harness skip 降到 2^20 的 `c07ba58`。现在这个 seed 0.00 秒就跳过,作为回归
防线留在 corpus 里。

这是上一轮教训 1(同一写法第 N 次被 fuzzer 开成 issue 时该改的是被接受的区间,不是再挪一个
seed)的一个**正向结算**:区间改窄之后,同族的下一个 crasher 不需要任何新动作就消失了。

### 3. #205 三个标准流 + `io.read` + `debug` 子集

上一轮我把这一块撤回了,因为原型会破坏 arena。**根因很简单**:原型直接调
`object.AllocUserdata`,**跳过了 collector 的 `LinkSweep`**,所以对象 header 里既没有颜色也
没有 sweep 链,收集器根本看不见它;创建句柄之后一个 `collectgarbage()` 就以 arena 索引越界
panic,而那些句柄仍然从 `io` 表可达。

`internal/crescent/alloc.go` 里每个分配器都是 `AllocX` + `LinkSweep` + `AllocCharge` **三件
一起**(`allocLuaClosure` / `allocOpenUpvalue` / `allocTable` 三个现成样本都写着这三步),
userdata 也不例外。现在加了 `State.NewUserdata` 把这一步固定下来,`TestIOHandles_SurviveGC`
在反复收集与分配压力下防住它(收集后 `type(io.stdout)` 仍是 userdata、50 轮收集句柄仍在、
一万个表的分配压力之后仍在)。

**必须是真 userdata 而不是表**,因为 PUC 报的是 `userdata`、`type()` 会露馅。为此补了两处 VM
缺口:`metaFieldOfValue` 不认 userdata(所以 userdata 的 `__index` 从来没被查过),
`indexWithMeta` 压根没有 userdata 分支(userdata 没有自己的裸字段,索引应当直接走 `__index`,
没有 metatable 时报 `attempt to index a userdata value`);另外 `getmetatable` 对 userdata
也是无条件返回 nil,现在还会遵守 `__metatable`。

**56 个探针与 `lua5.1` 比对抓到的细节,每一条我第一版都写错了**:

| 细节 | 我写的 | PUC 实际 |
|---|---|---|
| 非句柄 receiver | 通用的参数类型错误 | `bad argument #1 to '?' (FILE* expected, got table)` |
| 读一个只写句柄 | 单个 nil | errno 三元组 `(nil, "Bad file descriptor", 9)` |
| `debug.traceback(nil)` | 裸 traceback | `nil`——**显式 nil 是一个值、不是「参数缺失」** |
| `debug.getinfo()` 无参 | 我凭印象的词序 | `function or level expected`(这个词序) |

现在 54/56 一致,剩下 2 个是故意不提供的 `debug.sethook` / `debug.getlocal`。

**`io.read` 必须用真实二进制验证**:`go test` 不会把 stdin 转发给测试进程,我最初的探针全部
返回 nil,量到的是 harness 而不是代码。用一个真实 binary 之后,八种写法(`*l`、`*n`、`*a`、
字节数、EOF、默认、`io.stdin:read`、`io.lines`)全部与 `lua5.1` 一致。

`getinfo` 只填 P1 能诚实回答的字段(`currentline` / `source` / `short_src` / `what` / `func`),
`nups` / `activelines` / `namewhat` 宁缺不假造——对一个会去测这些字段的调用方来说,错的字段比
没有更糟。`FrameInfo` 那里还有一个 off-by-one:`getinfo` 自己是 host 函数、host 帧不进 `cis`,
所以 level 1 是**最内层** cis 帧,直接减 level 会多跳一帧、让最常见的 `getinfo(1)` 返回 nil。

仍然缺的是需要真实文件的部分:`io.open` / `io.popen` / `io.tmpfile` / `f:seek`。`io.lines(filename)`
因此会抬错而不是静默返回一个空迭代器。

### 4. harness 侧:新增的写路径必须接进捕获累加器

加了 `io.stdout` 之后,脚本可以经一条 harness **没有捕获**的路径输出:`io.stdout:write("A")`
直接漏到真实 stdout,而且那段文本在**两侧**的捕获输出里都不存在——于是两侧仍然一致、不报分歧,
但比较实际上已经不覆盖这条路径写出的任何东西了。**这是「因为错误的原因而变绿」,与一条掩盖了
整类输入的 skip 是同一种失效方式。** 修法是在 `internal/oracle/prelude.go` 里把 file-handle 的
`:write` 也接到 `io.write` 用的那个累加器上(`io.stderr` 的写入丢弃而不累积,与 harness 别处
只比 stdout 的口径一致)。

我第一版这个 wrapper 里调了一个 prelude 中**并不存在**的 helper `__ts`,于是每个碰到 wrapper
的脚本都死在 `attempt to call global '__ts'`、捕获输出变成**空**——而比较报的是「一致」,因为
两侧以完全相同的方式失败了。**两侧在同一个错误上一致,不等于两侧在行为上一致。** 我是靠打印
verdict 与实际输出、而不是看 diff 是否为空才发现的。

## 期望与实际

- 期望:#206 是一条小口径上限修复;#208 要分诊;#205 是上一轮撤回后接着做。
- 实际:#206 的上限公式一次就对,**卡住的是消息文本**(参照实现把它包了一层);#208 一行代码
  没改——它已经被上一轮的区间改动一起解决了;#205 的根因只有一行(漏了 `LinkSweep`),真正
  花时间的是 56 个探针逐条纠正我凭印象写下的四个细节,以及发现 `go test` 根本不给测试进程 stdin。

## 教训

### 教训 1(一个 `AllocX` 必须与它所在仓库的注册步骤配对,先去读同族分配器再写新的)

userdata 那次不是 GC 有 bug,是我漏了 `LinkSweep`——而 `crescent/alloc.go` 里三个现成分配器
每一个都写着 `AllocX` + `LinkSweep` + `AllocCharge` 这三步。

**Why**:一个对象类型的「分配」在本仓不是一个函数调用,是一组**必须一起做完**的登记动作:写头
(颜色)、挂 sweep 链、记账。少任何一步的症状都出现在**离原因很远的地方**——越界 panic 出现在
GC 里,而错误在分配处;而且它只在收集器真的跑起来时才暴露,所以「功能全对」和「会破坏 arena」
可以同时成立(上一轮就是这么撤回的)。这与 [[design-claims-vs-codebase-physics]] §4「GC 根
可达性不能靠推理下结论」是同一物理基础的**另一侧**:那条讲「已有对象在优化后还可不可达」,
本条讲「新对象有没有进入收集器的视野」。

**How to apply**:给一个新对象类型加分配路径时,先读同一文件里已有的分配器,把它们**共有的
每一步**都照做,并把新入口做成唯一入口(本轮 `State.NewUserdata`),让下一个人无法再跳过。
判据:同族分配器里出现 N 次的动作就是契约,不是那几个函数各自的选择。

### 教训 2(`go test` 不转发 stdin,凡是读标准输入的功能都得用真实二进制验证)

我最初为 `io.read` 写的八个探针**全部返回 nil**,当时的读法是「实现不工作」。真实原因是
`go test` 不把 stdin 转给测试进程,量到的是 harness 的行为。

**Why**:测试驱动本身会提供或不提供一部分**进程级环境**(stdin、tty、信号、工作目录、环境
变量),而被测功能恰好依赖它时,探针测的是「驱动给了没有」而不是「代码对不对」。这一条与
[[prove-the-path-under-test]] §2 是同一个命题在**输入侧**的形式:那边讲「输出相同不代表走了
被测路径」,这边讲「输入根本没到,所以路径压根没跑」。危险的地方在于失败方向——它产出的是
「不工作」这个**否定**结论,而否定结论会直接改变修法(我差点去改 `readFormats` 的实现)。

**How to apply**:功能依赖进程级输入 / 输出 / 环境时,先确认测试驱动是否提供了它;不提供就
换真实二进制 + 喂真实 stdin 来验。判据:一个探针的结果全是同一个「什么都没有」的值(全 nil、
全空串、全 0)时,先怀疑输入没到,再怀疑实现。

### 教训 3(新增一条输出路径时,必须同时把它接进差分 harness 的捕获)

加了 `io.stdout:write` 之后,脚本有了一条 harness 不捕获的输出通道。两侧都不捕获、都一致、
都不报错,而那条路径实际上退出了比较范围。

**Why**:差分比较的对象不是「程序做了什么」,是「harness 捕获到了什么」。新增一个写外部世界的
API 就是新增一条绕过捕获的通道,而它的失效表现是**最安静的那一种**:不是误报(会亮红灯)、
也不是单侧 skip(至少这条输入被丢掉),而是**这类输出在所有输入上都不再被比较**,却没有任何
迹象。这是 [[prove-the-path-under-test]] §4.2/§4.6「一句无执行体的豁免 / 一条还活着的 skip
读起来像已处理」那一族的新成员——前两者是「声明说这里不用看」,本条是「捕获面本身有个洞」。

**How to apply**:加了任何写外部世界的 API 之后,写一个「经新路径输出 + 经老路径输出」的用例,
确认捕获输出里**两段都在**。判据不是「测试绿了」,是能在捕获结果里逐字节看到新路径写出的那
几个字节。

### 教训 4(两侧在同一个错误上一致,不等于两侧在行为上一致)

我那个 wrapper 引用了 prelude 里不存在的 `__ts`,于是所有相关脚本都抛同一个错、捕获输出都是
空串,比较报「一致」。

**Why**:比较器只回答「两串字节相等吗」。两侧共享同一层 harness 代码时,那层代码自己的 bug
会**对称地**打坏两侧,于是「相等」这个结论仍然成立,只是它已经不再是关于被测语义的结论了。
[[prove-the-path-under-test]] §3 已经写过这条的一般形式(harness 自身失效的默认表现是假绿),
本轮是它的又一个样本,并给出一个**廉价的具体动作**:看 verdict 与实际输出,不只看 diff 是否
为空。空输出与错误文本是两个红旗——正常脚本的捕获输出很少恰好是空的。

**How to apply**:差分比较报一致时,如果输出是空的、或者是错误文本,要额外确认那不是「两侧
同样地坏掉了」。判据:harness 层的新代码上线时,至少跑一个**已知应当有输出**的用例并肉眼确认
那段输出真的在捕获结果里。

### 教训 5(参照实现的错误消息可能被包了一层)

`luaL_checkstack` 把调用者给的文本包成 `stack overflow (%s)`。我照抄了裸文本,上限判断完全
正确,12 个用例还在分歧。

**Why**:C 侧抬错有两类入口:`luaL_error` 把给它的文本**直出**,而 `luaL_checkstack` /
`luaL_argerror` / `luaL_typerror` 这类会**再套一层格式**。源码里那一行看起来都是「一个字符串
字面量」,差别在被谁消费。这是 [[cross-backend-semantic-fix-sweep]]「PUC 语义由 C 实现定义」
的又一个刻度:前面几个刻度讲**值**要顺着 C 的转换链推(`luaL_checkint` 的隐式两步)、讲**上限**
的条件项可能读栈状态(`unpack` 减参数个数),本条讲**消息文本**也有一条包装链。

**How to apply**:抄一条 PUC 错误消息时,去看它是经哪个 `luaL_*` 抬出来的,把那一层的格式串
一起抄;同族函数的公式一样不代表消息一样(`unpack` 与 `string.byte` 的上限公式相同、消息一个
直出一个包一层)。

## Promotion 决策

- **教训 1** 已补进 [[design-claims-vs-codebase-physics]] §4 末尾(新对象类型的分配路径必须
  照抄同族分配器的每一步,缺一步的症状离原因很远)。
- **教训 2** 已补进 [[prove-the-path-under-test]] **§4.10**(测试驱动没提供的进程级输入,
  测出来的「不工作」是假的),紧接 §4.7 那几节「覆盖维度」之后。
- **教训 3** 已补进 [[prove-the-path-under-test]] **§9.7**(新增输出路径必须同时接进捕获,
  否则它退出比较范围),作为 §4.2/§4.6「无执行体的豁免 / 活着的 skip」那一族在**捕获面**上的
  新成员。
- **教训 4** 已补进 [[prove-the-path-under-test]] **§9.8**(两侧在同一个错误上一致 ≠ 行为
  一致),它是 §3 与 §9 结尾那句「harness 自身失效的默认表现是假绿」的第二个样本,贡献的是
  「看 verdict 与实际输出,空输出与错误文本是红旗」这个可执行动作。
- **教训 5** 已补进 [[cross-backend-semantic-fix-sweep]]「PUC 语义由 C 实现定义」节,作为
  该节第四个刻度(值的转换链 → 上限的条件项 → **消息的包装链**)。
- 另外两处**正向结算**已就地补进对应 guide,不算新教训:① #208 是
  [[unreproducible-crasher-triage]]「第一步永远是版本核对」第一档的又一次命中,并且是上一轮
  「该改的是被接受的区间」的事后确认(区间改窄后同族的下一个 crasher 不需要任何新动作就消失,
  这本身就是那次改动改对了位置的信号);② #205 的根因只有一行,验证了
  [[prove-the-path-under-test]] §4.3b「卡在哪要具体到哪个约定」是可执行的——撤回时写下的那句
  「非 finalizer 路径创建 userdata 的分配与 rooting 约定」正好把下一轮的第一个动作指向了同族
  分配器,已在该节补一段正向结算。

## 触发场景

- **给一个新对象类型加分配路径时**:先读同一文件里已有的分配器,把它们共有的每一步都照做,
  并把新入口做成唯一入口(教训 1)。
- **被测功能依赖 stdin / tty / 信号 / 工作目录这类进程级环境时**:先确认测试驱动提供了它,
  否则用真实二进制验;探针结果全是同一个「什么都没有」的值时先怀疑输入没到(教训 2)。
- **给运行时加任何写外部世界的 API 之后**:同一轮把它接进差分 harness 的捕获,并用一个
  「新路径 + 老路径各写一段」的用例确认两段都在捕获结果里(教训 3)。
- **差分比较报一致、而输出是空的或是错误文本时**:确认那不是两侧同样地坏掉(教训 4)。
- **抄一条参照实现的错误消息时**:先看它是经哪个 `luaL_*` 抬出来的,`luaL_checkstack` /
  `luaL_argerror` 这类会再套一层格式(教训 5)。
- **修一个与已修函数「公式相同」的兄弟函数时**:公式可以照抄,消息、默认值、参数校验各自
  独立核对一遍(#206 相对 #201 的关系)。

## 关联

[[prove-the-path-under-test]](教训 2 落 §4.10、教训 3 落 §9.7、教训 4 落 §9.8;§3 harness
自身失效的假绿、§4.2 无执行体的豁免、§4.6 skip 随 bug 撤、§4.5 量参照实现的边界) ·
[[design-claims-vs-codebase-physics]](教训 1 落 §4;GC 根可达性那一节的另一侧) ·
[[cross-backend-semantic-fix-sweep]](教训 5 落「PUC 语义由 C 实现定义」节第四刻度) ·
[[2026-07-28-issue201-203-unpack-skip-thresholds]](上一轮:`io.stdout` 在那里被撤回并开成
#205、`unpack` 的 `8000 - nargs` 上限、移位区间的 skip 降到 2^20 使 #208 不需新动作) ·
[[unreproducible-crasher-triage]](#208 的分诊:先核对 fuzz run 的 commit 与当前 HEAD) ·
`internal/crescent/alloc.go`(`State.NewUserdata` 与三件套约定) ·
`internal/crescent/meta.go`(userdata 的 `__index` 通道) ·
`internal/crescent/errors.go`(`FrameInfo` 与 level 的 off-by-one) ·
`internal/stdlib/tablelib.go`(标准流、`io.read`、`debug` 子集) ·
`internal/stdlib/stringlib.go`(`string.byte` 的上限与包一层的消息) ·
`internal/oracle/prelude.go`(file-handle `:write` 接进捕获累加器) ·
`io_handles_test.go`(`TestIOHandles_SurviveGC` / `TestIOHandles_MatchPUC` /
`TestDebugLibrary_MatchPUC`)

## 收尾：六轮审计，36 处缺陷，全部集中在 io.read 与 debug.getinfo

三个 issue，12 个 commit。六轮独立审计共 36 处缺陷，而分布很清楚：
**userdata / GC / arena 那部分六轮全清**（漏 `LinkSweep` 那一条修对之后就再没出过问题），
`string.byte` 的上限一次就对，而 `io.read` 的格式解析与 `debug.getinfo` 的字段占了几乎全部。

原因不难看：这两处我都是**先按印象实现、再靠审计逐条纠正**，而不是先把参照实现的规则量出来。
`io.read` 的每一条规则（`*` 必需、只看 `*` 后一个字符、`*L` 不存在、count 0 与负数、
列表在首次失败处停下、`*n` 的 scanf 提交策略）都是被审计指出来之后我去量才确定的；
`debug.getinfo` 的 `what` / `source` / `linedefined` / `what` 选择器 / level 窄化同理。

### 两次「修法本身走反方向」

- stdin reader 从**包级全局**（跨 State 竞争）改成**每 State 一个**，结果换来**静默丢输入**：
  一个进程里两个 State 顺序各读一行得到 `line1` / `nil`，因为第一个 State 的缓冲预读吞掉了后面
  的内容并随它一起消失。`os.Stdin` 是一个 fd、一个 offset，两个 reader 不可能各自拥有它——
  共享是对的，只有竞争需要修，所以最终是共享 + 锁。
- `debug.getinfo` 判定 main chunk 试了**四次**：帧下标（尾调用会把普通函数放到 0）、
  「vararg 且无固定参数」（两个方向都错：把协程栈底的 `function(...)` 说成 main，又漏掉
  loadstring chunk）、`callInfo.fresh`（漏掉从 Lua 里调用的 loadstring chunk），
  最后用 PUC 的判据 `LineDefined == 0`——为此让编译器给 chunk 记 0 而不是它起始的那一行。

### 我自己写的回归测试挂住了构建

第三轮最重的一条发现在**我自己的测试里**：它依赖环境里的 stdin，于是编译出的二进制在终端下
**永久阻塞**、在管道下**断言失败**——而且断言的正好与它声称要固定的性质相反。它在 `go test`
下通过，只因为 `go test` 恰好把 `/dev/null` 接了上去，而 `make test` 是直接跑二进制的。

追加四条教训：

10. **两处相同的规则就是一处太多。** 「EOF 时迭代器不产出任何值」这条我先只改了两份拷贝中的一份
    （`io.lines` 与 `file:lines` 各有一个闭包）。判据：同一条语义出现在两个地方时，先合并再修，
    否则修完的那一份会让人以为整条规则都对了。
11. **测试自己不能依赖环境里的 stdin / tty / 工作目录。** 判据：写一个会读进程级输入的测试时，
    自己把它重定向掉（`os.Stdin = devnull` 并在 cleanup 里还原），并且**用编译出的二进制在有
    真实 stdin 的情况下跑一遍**——`go test` 提供的环境与 `make test` 直接跑二进制不同，只在前者
    下验证会漏掉阻塞与断言反向这两种失效。
12. **从别处搬来的判据要重新验证它在新位置成立。** 「下标 0 就是 main chunk」在 `buildTraceback`
    里是对的（它只给栈底贴标签），搬到 `getinfo` 就错了（它要指名函数，而尾调用会把普通函数放到
    下标 0）。判据：复用一个条件时，问「原来的调用方问的是同一个问题吗」；两个调用方对同一份数据
    问不同的问题时，判据不能共用。
13. **给一个函数加了兄弟函数的检查之后，要回头检查自己同时新增的那个函数。** 这一轮我给
    `string.byte` 补了 `lua_checkstack` 上限、还顺着源码扫了其余调用点，却漏了**同一个 commit 里
    我自己新写的** `io.read`——它有完全一样的缺口。判据：做「同族缺陷扫描」时，把本次改动**新增**
    的函数也算进同族，它们不在旧代码里、grep 不到，但同样属于那一族。
