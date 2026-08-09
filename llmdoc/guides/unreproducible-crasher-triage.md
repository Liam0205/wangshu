# 不可复现 crasher 的处置模式(unreproducible crasher triage)

## 适用场景

fuzz / nightly / CI 报了一个 crasher,给出了一个落盘 input,但本地精确重放该 input 无论多少次都不
复现。典型征象:

- `go test -fuzz` 在 nightly / 长时间运行里 worker 死亡,报 `fuzzing process hung or terminated
  unexpectedly: exit status 2`,artifact 里挂了一个 `testdata/fuzz/FuzzXxx/<hash>`;
- 拿到该 input 精确重放,单次 / N 次都 PASS,harness 镜像跑 hammer 也 PASS;
- 几千万到上亿 execs 之后才死一次,复现窗口远长于一次典型 fuzz smoke。

本 guide 管的是**这种「不可复现」问题怎么止损**;它不管「可复现问题怎么修对」——那属于
[[prove-the-path-under-test]](修复后证在测的路径真被走到)和 [[cross-backend-semantic-fix-sweep]]
(修同一语义时枚举全部后端 × 通道)的范围。三者构成:

- 可复现 crasher(input 决定的 VM bug)→ prove-the-path + cross-backend sweep;
- 不可复现 crasher(进程级资源耗尽 / 工具链 flake / 已修复代码 remnant)→ **本 guide**。

范围随实例扩过两次,现在还包括两类相邻场景:**读一个数字退出码 / 给一个 CI 失败定类时**
(「静默死亡先做两步分类」及其两个前置子节:先定位这个退出码是谁的表、先把时间戳减一遍),
以及**nightly 自动开 issue 这套机制自己出问题时**(「CI 自动化本身的失败信号」:依赖步骤失败
造成的红色含义是「没跑」;去重键的粒度决定一次事件会不会被拆成 N 个 issue)。后者排在分诊之前
——分诊的输入就是那批 issue。

## 第一步永远是版本核对(不是复现矩阵)

看到「落盘 input 无法复现」这个信号后,**第一件事是核对失败 run 使用的代码版本相对当前 master 是否
已含相关修复**,不是撒复现矩阵。

**判据**:「撞的是已修复代码,当时的 headSha 还没含修复」是所有解释里**最便宜**的一条,一条命令就能
排除,复现矩阵每条要几分钟到几十分钟。把最便宜的解释放到最后,是「先撒大网再省钱」的反模式。

**手法**:

```
gh run view <run-id> --json headSha,event,createdAt
git log --oneline <headSha>..HEAD -- <suspected-path>
git merge-base --is-ancestor <fix-commit> <headSha>; echo $?
```

分三档:

- 失败 headSha **早于**最近相关修复 → 判「已在 master 修好」,corpus 入库常驻回归,复现矩阵不撒;
- 失败 headSha **已含**相关修复 → 便宜解释已排除,再走下面的分诊 + 复现矩阵;
- 失败 headSha 与相关修复无明确因果 → 走分诊,顺带记「versions checked, no direct match」进 issue。

**教训来源**:#123 轮走反了顺序——先精确重放 → 镜像 hammer → 恢复路径 → mmap 探针 → 定向 fuzz →
GC 压力 → 最后才核 headSha,前六条都跑完再核版本。等排除掉「已修复代码」这条最平凡的解释后回头看,
前六条至少有一半可以不撒。反思 [[2026-07-11-issue123-unreproducible-crasher-round]] 教训 1。

**又一个第一档命中(2026-07-29,#208)**:它是 `table.insert` 移位那一类的又一个 nightly crasher,
fuzz run 跑在 `cbd0512` 上,**早于**上一轮把 harness skip 降到 2^20 的 `c07ba58`;核一次版本就
结束,**一行代码没改**,seed 现在 0.00 秒就跳过、作为回归防线留在 corpus 里。这同时是下面
「同一写法第三次被开成 issue 时该改被接受的区间」那条的**正向结算**:区间改窄之后,同族的下一个
crasher 不需要任何新动作就消失了——**「不需要新动作」本身就是那次改动改对了位置的事后确认**(与
[[prove-the-path-under-test]] §9.6 推论同构:一批 crasher 被同一个改动一起解决,是根因修在正确
位置的信号)。反思 [[2026-07-29-issue205-206-208-io-userdata-debug]]。

**三档是关于「要不要复现一遍」的分类,它不回答「有没有缺陷」(2026-08-02,#212–#219)**。上面这三档
排除的是「撞的是已修复代码」这条最便宜的**解释**,分的是成本档,**不是**缺陷档——分完档之后仍然必须
在当前 HEAD 上实际重放一遍才知道有没有缺陷。这一条要单独写出来,是因为两轮的字面事实几乎一样而结论
相反:#209 那一轮八个字面事实指向第一档(run 跑在旧 commit 上、修法已在仓库里、一行代码没改),
#212–#219 八个 run 同样全落在一个旧 commit(`5383aec`)上,而实际重放之后**五个如实复现、三个是真
产品缺陷**(`error()` 的 level 没做类型检查 / 调用行号取了被调用表达式那一行 / `string.gsub` 的 repl
惰性校验)。

**判据**:nightly 每晚跑的就是当时的 master,所以「全落在一个旧 commit 上」是**常态而不是线索**,
它本身不含信息量。所以:**无论 headSha 落在哪一档,都要在当前 HEAD 上把每一条 seed 实际重放一遍再
分缺陷档**;「与上一轮同一个 commit」「与上一轮同一类写法」「上一轮是过期的」都不是可以省掉重放的
理由。自查办法:说「这一批是过期的」之前,先看有没有**一条命令的输出**支持这句话——如果支持它的只是
上一轮的结论,那就还没有分档。反思 [[2026-08-02-issue212-219-fuzz-crasher-batch]] 教训 1。

**还要把 seed 拿到那个「旧 commit」上也跑一遍(2026-08-04,#224/#225)**。上一条要求在**当前 HEAD**
上重放,而 HEAD 上通过这件事与「**是哪个改动让它通过的**」是两个问题。只在 HEAD 上重放能得到「现在
没问题」,得不到「为什么现在没问题」——而后者决定还要不要继续查。所以版本核对有第四步:**把 seed 也
拿到那个「修复之前」的 commit 上跑一遍**。

**判据**:确认一个 issue「已被某个改动修好」时,要同时确认**修法与现象之间真的有因果**;在旧 commit
上也不复现,就说明归因错了,得继续查。自查办法:说「这是上一轮修好的」之前,先问「我在那个修复之前
的 commit 上量过吗」——没量过,这句话就只是时间顺序的巧合,不是因果。这与
[[prove-the-path-under-test]] §9.6「双向验证」是同一条纪律换了时点:那条讲验证一个**修复**要在 base
上确认 FAIL,本条讲判定一个 issue **过期**要在 base 上确认它**也**不复现(在 base 上就不复现,那个
修复不是原因)。

**#224/#225 实证**:两个 seed 的 run 都在 `093f7d1` 上、早于 #222 那一轮的合并,当前 HEAD 上重放
各约 1 秒正常触发预算——按前三步就该判第一档结案。把它们拿到 `093f7d1` 上一跑,**在那里也已经被
界住**,所以 #222 那一轮不是修好它们的原因,「过期、已修复」这个结论不成立;run 时间确实早于合并,
但那个事实与这两个 issue 为什么被开出来无关。真实原因在另一个维度上,见下面「上限的余量」一节。
反思 [[2026-08-04-issue224-225-watchdog-margin]] 教训 1。

## 一批 crasher 先问会不会被同一个改动一起解决(排在分头查根因之前)

版本核对管的是「**一条** crasher 是不是撞的已修复代码」;当手上是**一批** crasher 时,还有一格更
便宜的检查:**它们之间是不是同一件事**。

**判据**:nightly 是按 run 自动开 issue 的,同一个根因会在不同日期、以不同的最小化输入、不同的
标题与 hash 各开一个 issue,看起来是 N 件独立的事。所以**拿到一批 crasher 时,先在当前 HEAD 上
把每一条重放一遍,通过的归成一组只做一次根因确认,再对剩下的逐条查**。顺序反了就会把一个根因
查 N 遍。

**手法**:

```
# 每条 reproducer 在当前 HEAD 上跑一遍,先分「已通过 / 仍失败」两组
go test -run FuzzOracleDiff/<hash> ...
# 已通过的那组:只确认一次它们共同的根因是什么、被哪个 PR 解决的
# 仍失败的那组:才按本 guide 剩余各节逐条分诊
```

**#187–#190 实证(2026-07-26)**:四个 `FuzzOracleDiff` crasher 由 nightly 在四个不同日期自动开出,
标题与 hash 各不相同 ——
`print(string.format("%q",0%0))` / `print(string.format((0))<string.format((0%0)))` /
`print(string.format("%q",(0%0)))` / `print(string.format("+% E",-(0%0)))`。四个全部是**同一个
根因**(NaN 符号位渲染差异)的不同表现,而那个根因刚在 PR #191 里通过「在 oracle 的渲染处消除」
解决,**一行代码没改就全修好了**。

这一条与 [[prove-the-path-under-test]] §9.0 互为印证:**正是因为根因被在产生处消除了,四个表现
才一起消失**;如果当初走的是「在比较侧识别并豁免」那条路,四个 issue 会各自需要一条新的判据分支。
反过来读:**一批 crasher 能被同一个改动一起解决,本身就是「根因修在了正确位置」的一个事后确认
信号**;如果修完之后同族 crasher 还在一条一条冒出来,那是选址错误的信号(§9.0)。

**在这之前还有一格更便宜的:先算 seed hash 去重(2026-08-02,#212–#219)**。nightly 是按 run 开
issue 的,**同一个输入在两个夜晚各被最小化到同一个 hash,就会开出两个 issue**——#212 与 #215 是同一个
hash `1d42242f157c9754`,#217 与 #219 是同一个 `0150a245c776b8ed`,八个 issue 其实只有**六个不同的
seed**。这一步比重放更便宜(比一下 artifact 文件名就够),而且它与上面那条按行为分组是两个维度:hash
相同是**同一个输入**,行为分组是**不同输入同一个根因**。反思
[[2026-08-02-issue212-219-fuzz-crasher-batch]]。

**确认「修好了」的方式见 [[prove-the-path-under-test]] §9.6**:断**逐字节 equal** 不是断「测试
绿了」(差分 harness 里 skip 也是绿的,含义却相反),并且**双向验证** —— 在修复前的 base commit
上确实 FAIL、在修复后的 HEAD 上以 equal 通过。#187–#190 在 redesign 之前的 base `c982610` 上
全部 FAIL、在当前 master 上全部以真正的 equal 通过。

**已修好的 crasher 仍然值得入库,判据是它触到的写法是否为新**。这四条不用改代码,但覆盖了三个
已有 seed 到不了的维度,而每一个都是那个符号字节逃逸的不同路径:`%q` 作用于 NaN(它引用渲染结果
而不是做浮点格式化,不走 `%e`/`%f`/`%g` 那条 `sprintf` 通道)、比较两个 `string.format` 的**返回
值**(差异变成一个 boolean,输出里根本没有 NaN 文本可锚定)、多个符号 flag 同时出现(`"+% E"`,
负号去掉后 glibc 的 `+` 与空格 flag 开始作用于 NaN)。入库之后这三个方向才有回归保护。反过来,
如果一条 crasher 的写法与既有 seed 等价,入库只是增加重放成本。**判据是写法是否为新,与有没有
改代码无关。**

### 同源可能症状不同,所以分组的输入不能只是症状(2026-08-05,#232/#233)

上面几条讲的分组手法(seed hash 去重、按根因分组)在**操作上**的输入都是**症状**——nightly 给的是标题、
hash 与最小化后的 seed,三者都描述表现。两条表现差得越远,越不会被放到一起,而「差得远」恰恰可能是
同一个量的两种可见度造成的。

**#232/#233 实证**:两个 `FuzzOracleDiff` crasher 的症状差得很远——#232 是「原始输出不同」
(`io.write(0)print(print)` 让输出变成 `0function: 0x...`,`addrRe` 的 `\b` 在数字与 `function` 之间
不匹配,地址整段逃过归一化),#233 是「长度不同」(`t={0}print(#tostring(t))`,PUC 21 对望舒 17)。看起来
一个是渲染问题、一个是语义问题,实际是**同一个地址宽度差异**:前者是否分歧取决于真实地址恰好需要几位
十六进制(所以 nightly 有时报有时不报),后者稳定分歧。**间歇与稳定这两种可见度属于同一个根因**,而
「间歇」这个属性本身很容易被读成「另一类问题」。

**判据**:同一批里若有两条都涉及同一种**引擎相关的值**(地址、指针、时间、随机数、GC 数值),**先假设
同源,再去找那个共同量**。自查办法:把每条 seed 里出现的引擎相关量各列一行(#232 是 `tostring(print)`
的地址,#233 是 `tostring(t)` 的地址),列出来相同就别急着分头查。这与 [[prove-the-path-under-test]]
§9.6 的推论互补:那条讲**修完之后**「一批 crasher 被同一个改动一起解决,是根因修在正确位置的信号」,
本条讲**动手之前**「症状不同不代表根因不同」。反思
[[2026-08-05-issue232-234-address-width-and-gsub-escape]] 教训 1。

### 都真复现的那一档:下一格便宜的检查是按失败形式分诊入口(2026-08-04,#228/#229)

上面各节写的都是「怎么便宜地判掉**不需要**复现的那一部分」——版本核对、seed hash 去重、按根因分组。
一批 crasher 全部在当前 HEAD 上如实复现时,这些都用完了,而这一档也有自己的便宜检查:**按失败形式
选分诊入口**,不同的失败形式对应不同的第一个动作。

**#228/#229 实证**:两个 run 的 headSha 都是 `efa9aa6`,但版本核对这次不重要——两个都真复现,而且
**互不相关**(hash 不同、子系统不同,一个在 stdlib 的 pattern 消费侧、一个在共享调用层的 RETURN)。
「会不会被同一个改动一起解决」这一格给出的是**否**,只能分头查。分头查之前先看失败形式:

| 失败形式 | 第一个动作 | 本轮实例 |
|---|---|---|
| **oracle 差分**(与 lua5.1 输出不同) | 去 `_lua515/` 读参照实现的语义边界:接受面 / 抬错时机 / 默认值 / 上限各在哪一行、由谁抬出 | #228:`unfinished capture` 在 PUC 是从 `push_onecapture` 抬的,也就是捕获**真的被读出来**的时候;望舒在生产侧就抬了 |
| **层间分歧**(P1 成功而 P3/P4 抬错) | 先问「谁写坏了它读到的数据」,别从加速层的 emit 读起;在抬错点打栈迹看**缺哪一层** | #229:栈迹里没有任何 JIT 帧,抬错的是普通解释器,破坏在更早的已升层调用里 |

这张表的两行分别通向另外两篇:第一行走 [[cross-backend-semantic-fix-sweep]] 的「PUC 语义由 C 实现
定义」(六个刻度:值的转换链 / 上限的条件项 / 消息的包装层 / 有定义与 UB 四格 / 校验在控制流的位置 /
错误从哪个函数抬出),第二行走 [[prove-the-path-under-test]] §7.3(抬错的位置不是缺陷的位置)。
本篇只负责把入口分对。反思
[[2026-08-04-issue228-229-lazy-capture-and-nested-tailcall-top]]。

## 修完一条 fuzz 输入之后,扫它所属的维度

fuzz 给出的是一条具体输入,但它是一个家族的采样点。**「这一条不再有差异」不等于「这个家族已经
收口」**。

**判据**:确认一条 fuzz 输入零差异之后,从 reproducer 的结构里读出它所属的维度,把那些维度枚举
扫一遍再收工。维度怎么读:哪个 verb、哪些 flag、哪个运算符、值从哪来、文本被谁消费。

**#187–#190 实证**:四条输入自身零差异之后又扫了两批 —— **236 种写法**(`%q` × 宽度组合,
6 个比较运算符 × format 结果与 tostring 结果,10 种 flag 组合 × 5 个浮点 verb)+ **140 种写法**
(7 种产生 NaN 的方式,含 `math.huge-math.huge` 与 `tonumber("nan")`,× 20 种消费其文本的方式:
concat、长度、byte、sub、reverse、gsub、find、match、upper、rep、`table.concat`、作为 table
key)。零差异。多花几分钟,换来的是知道这个家族的边界在哪,而不是只知道那四条过了。反思
[[2026-07-26-oracle-nan-render-redesign]] §9 与教训 6/7/8。

## 真假 crasher 分界(承 2026-07-03 分诊纪律)

go-fuzz 的失败模型是「worker 挂掉时把当前 input 落盘」,但 worker 挂掉的原因可以是 input 触发
panic(input 决定),也可以是 worker 进程被 OS 因资源耗尽 kill(与 input 无关,input 只是刚好在跑
而已)。两种原因需要完全不同的处置路径,**「落盘 input 能否精确重放」是区分二者的最直接判据**。

三种典型征象与对应判定:

| 征象 | 判定 | 处置方向 |
|---|---|---|
| `context deadline exceeded` + **无** failing input 文件 | fuzz 引擎 30s 窗口收尾时的工具链 flake(golang/go#75804 一类) | 单独复跑即过,不入 corpus,不开 issue(反复出现再入 doc-gaps) |
| `Failing input written to testdata/...` + input **可精确重放** | 真 crasher,input 决定的 VM bug | 走常规调查,定位 VM 侧根因 |
| `Failing input written to testdata/...` + input 精确重放**多次干净** + 几千万 execs 后才死一次 | 进程级资源耗尽嫌疑(内存 / mmap 数 / OS OOM killer / **CPU wall-clock 撞 fuzz 10 秒 per-input 看门狗**) | **本 guide 剩余各节** |

> **补记(2026-07-20)**:concat storm 家族此前落在这一行、被具体判为「内存」资源耗尽,后经取证栈迹
> 证伪——真因是 CPU wall-clock 撞 Go fuzz 的 10 秒 per-input 看门狗(见下文「concat storm 家族已
> 根因定性并修复」一节)。教训:这一行的「资源耗尽」是一类候选而非单指内存;拿到取证栈迹后,看
> panic 首行区分——`panic: deadlocked!` = CPU 撞 10 秒看门狗(单输入 CPU 时间),`signal: killed`
> 才是 OS 层内存 / OOM。

**Why**:分诊纪律的价值就是把「进程级资源耗尽」和「input 决定的 VM bug」两类原因显式分开,让「无法
复现」这个信号有明确的下一步(不是继续挖根因,而是转向进程级观测手段)。若没有这条纪律,自然反应是
把 input 当作 VM bug 触发点去追,可能会围绕这个 corpus 硬编一个「防御性修复」——但那个修复其实什么
也没修,因为 VM 侧根本没 bug。

**教训来源**:2026-07-03 issue #40 分诊沉淀 + #123 轮直接消费。反思
[[2026-07-11-issue123-unreproducible-crasher-round]] 教训 2。

### 真 crasher 但失败形式是层间分歧:先问 oracle 拿第三方真值

判据表把「真 crasher(落盘 input 能复现)」判到「走常规调查定位 VM 侧根因」,但**真 crasher 的失败形式若是层间分歧**(P1-vs-auto 结果不一致 / P1-vs-force 不一致 / 后端 A vs 后端 B 不一致),归因的第一步不是查升层路径 / 后端路径,而是**问 PUC luac oracle 拿第三方真值**。

- 若 oracle 与 P1 一致、只与被测 tier 不一致 → 归因到被测 tier(升层 / 后端 / IC / 快路径);
- 若 oracle 与**两层都不一致** → bug 在共享层(前端 codegen / stdlib / VM 共享语义),**不在升层路径**——差分 fuzz 的 P1 参照系本身可能是错的,只是两层错法不同才让分歧显性化,若两层巧合错到同一份历史残留,这个 bug 会完全静默通过差分。

判据成立的物理前提:差分 fuzz 的参照系是「另一层」不是「已知正确的实现」,「差分 = 参照系正确 + 被测有 bug」的隐含前提天然被忽视,归因会偏向「被测层」放过共享层。PUC luac 是最便宜的 oracle(同 corpus 直接跑 + `luac -l` 出参考字节码,操作数级 diff 直接指出发射分支)。

**#125 实证**:corpus `function sum() for A=0,0 do end end return sum() or (sum())` P1 返 `0`、auto 返 `nil`、oracle 说 `nil`——表象是「层间分歧指向升层路径」,真相是共享前端 `stmtReturn` 的 codegen bug 让两个 tier 各自读到不同的栈垃圾:`base := fs.freereg` 在 `exp2NextReg` 之前捕获,而 `exp2NextReg` 内部先 `freeExp`(freereg 回落一格)再把值物化到低一格,`RETURN A = base` 因此指向值的后一格,读到栈上的未定义数据。反思
[[2026-07-11-issue125-return-freereg-round]] 教训 1(层间分歧归因先问 oracle) + 教训 2(前端 codegen freereg capture-use 间距审计判据:`base := fs.freereg` 与 use 之间夹了任何会移动 freereg 的调用如 `freeExp` / `exp2NextReg` 就红旗,改用 `e.info` 消费物化后的权威位置或后移 capture)。

## 静默死亡先做两步分类(排在复现矩阵之前)

worker「无声消失 / hung or terminated unexpectedly」类失败,在撒七角度复现矩阵之前,先做两个
几分钟量级的分类检查——与「版本核对先行」同一量级,都是先用最便宜的检查排除最便宜的解释:

1. **先分类退出方式**:失败消息里是 exit code 还是 signal?`exit status 2` 是 Go runtime 自身
   fatal(panic / fatal error)的退出码,worker 死时几乎必然打印了完整栈迹——死因不是「无声」,
   是有一份完整的尸检报告没被看到;`signal: killed` 才是 OS 层的 SIGKILL(如 OOM killer),
   那才是真的没有输出。这一步直接决定「有没有栈迹可找」。
2. **再查子进程的 stdio 接线**:若第 1 步判定有输出,查它被父进程接到了哪里。internal/fuzz 的
   coordinator 用 `exec.Command` 起 worker 时 `cmd.Stderr` 留 nil,os/exec 把它接到
   /dev/null——尸检报告每次都写了,每次都被系统性丢弃。

**教训来源**:concat storm 家族 10 例横跨 8 天,此前各轮全部在复现矩阵与内存限制上打转,没有
一轮查过 `exit status 2` 的语义;这两步各花几分钟,合起来把问题性质从「查不到死因」翻译成
「输出没接住」,一步解开僵局。反思
`memory/reflections/2026-07-19-fuzz-worker-forensics-round.md` 教训 1。

### 第 1 步之前还有半步:这个退出码是**谁**的退出码(2026-08-09,#236–#241)

上面第 1 步已经在做「按退出方式分类」,但它默认那个数字的归属是已知的。**先要定位主语**:
一个数字退出码看起来像全局命名空间,而它根本不是 —— 每个程序有自己的退出码表,shell 报的是
**最后一条命令**的退出码,不是 errno。

**实例(#236–#241)**:nightly 的 oracle 安装步骤报 `exit code 28`,我读成 **ENOSPC**(磁盘写满)
并据此写了报告、提了「腾磁盘空间」的建议。**28 是 `curl` 的 `CURLE_OPERATION_TIMEDOUT`** ——
那一步是 `curl`,退出码来自 curl 自己的表:

| 表 | 28 的含义 | 谁产生它 |
|---|---|---|
| POSIX `errno.h` | `ENOSPC`(No space left on device) | 一个失败的**系统调用** |
| curl 退出码 | `CURLE_OPERATION_TIMEDOUT` | `curl` 进程自己的 `exit()`,shell 读到的就是它 |

两张表恰好在 28 这个数字上都有条目,而且都在讲一种资源类失败(空间 / 时间),所以错误那个解释
读起来完全自然。errno 表因为用得最多、最先被想起来,会变成默认解释来源,而「先被想起来」与
「正确」没有关系。

**判据**:看到一个数字退出码,**先定位是哪个命令退出的,再去查那个命令自己的退出码表**。
自查办法:说出「28 是 X」之前,先说出「28 是**谁的** 28」——说不出主语就说明还没定位到命令,
这时任何解释都是猜。这一步与上面第 1 步是同一族的两格:那一步分「Go runtime fatal 还是 OS
SIGKILL」(回答有没有栈迹可找),本步分「这个数字属于谁的表」(回答这个数字是什么意思)。

### 时间形式先于错误码:它是免费的,而且往往定类更准(2026-08-09,#236–#241)

同一轮的第二条免费证据,与退出码正交:**失败前有没有一段沉默**。

**实例**:`apt` 在 16:55:03 成功结束,然后**沉默 2 分 15 秒**才报 exit 28。这一条直接排除磁盘
写满 —— 写入撞上没有空间是**当次**返回错误,不会先卡两分钟;会先卡的只有等待类失败。配套的
反向检查同样免费且同样被忽略:那两个 run 的日志里 `no space left` / `ENOSPC` / `disk full`
出现次数都是 **0**,真是磁盘满的话 `apt` / `tar` / `make` 里总有一个会把这句话打出来。

| 时间形式 | 排除掉的 | 剩下的 |
|---|---|---|
| **立刻**失败(<1s) | 超时、锁等待、重试耗尽 | 资源耗尽(空间 / 配额 / 权限)、参数错误、文件不存在 |
| 失败前**长时间沉默**(分钟级) | 资源耗尽、参数错误 | 网络超时、锁等待、上游挂住、看门狗 |
| 失败前有**周期性**输出 | 挂住 | 重试循环、轮询 |

**为什么容易漏**:错误码是**显式的**(它就打在那里、看起来像答案),时间戳是**隐式的**(要自己
去减),注意力天然落在码上。但错误码只说「是这一类」,时间形式说的是「失败发生在等待之后还是
尝试的瞬间」,而后者能一刀切开好几类。

**判据**:分类一个 CI / worker 失败时**先把时间戳减一遍**,再读错误码。自查办法:写下「这是 X 类
失败」之前,先说出「这一步花了多久」以及「X 类失败应该花多久」,量级不符就是分类错了;并且反向
搜一次那一类的特征字符串,出现零次就该重新想。与下文「上限的余量」是同一个量的两个方向:那一节
用时间做**前瞻**判据(预测上限允许的量在 CI 上要跑多久、与看门狗比余量),本条是**回溯**用法
(从已发生的耗时反推故障类别);#224/#225 那一轮的 `panic: deadlocked` 判对,靠的也是把时间量
出来。反思 `memory/reflections/2026-08-09-issue236-241-curl-timeout-misread-as-enospc.md`
教训 1 / 2。

## 复现矩阵检查单(七角度)

退出方式与 stdio 接线分类完毕、版本核对已排除便宜解释、精确重放确认干净后,若还要继续调查,
以下七角度是「crasher 落盘 input 无法复现时,还能穷举什么」的实用检查单。逐条对照,不用现场想角度。

1. **精确重放**:corpus 原样跑 N 次(N ≥ 10),观察是否真的 100% 干净;
2. **harness 镜像 hammer**:阈值 / budget 参数与真实 fuzz worker 对齐后 hammer(N ≥ 300);形状对
   不上会静默落进别的路径,把 harness 里所有与 worker 一样的调参列清楚再跑;
3. **错误恢复路径**:`bare` / `pcall` / `coroutine` 三种包裹分别试,验证异常路径可正确恢复(不是
   死循环、不是 State 污染无法复用);
4. **资源泄漏探针**:重复触发升层 / 分配 / mmap 的操作跑 N 轮(N ≥ 300),观察 `/proc/self/maps`
   段数 + RSS 曲线是否单调增(mmap 段数不回收是常见嫌疑);
5. **定向 fuzz**:以 corpus 为种子跑短时(6~10min)定向 fuzz,让 mutation 探索附近形状;若邻域内
   有真 crasher 应短时抓到;
6. **GC 压力**:`GOGC=1` + `SetGCStressMode`(或等价),让 GC 时序变化暴露顺序依赖 bug;
7. **版本核对**:见上一节——**实际做的时候排最前**,写在这里是为了做检查单时不漏。

**七角度全部干净 + 几千万 execs 后无声死 + input 精确重放干净**这一组合的画像与「input 决定的 VM
bug」不匹配,更像 fuzz worker 进程级资源耗尽(内存 / mmap 数 / OS OOM killer)。此时**转处置模式**,
不继续无限挖根因。

**#123 轮实例**:七角度全干净,同 headSha `d6e05bd` 的 p1 腿 45 分钟 / 9100 万 execs 也没崩,特征
指向 fuzz worker 进程级资源耗尽,处置转向 corpus 入库 + 诊断硬化。

## 处置模式(判定为不可复现后)

不硬编修复,不无限挖根因。做两件事:

### 1. corpus 入库常驻回归——但要挑对入库位置

即使这个 input 是**合法通过用例**(#123 那个 `326b508ea720a654` 是无界非尾递归 + 每层 60 次全局写
循环,行为正确),也保留常驻回归。理由:

- 入库无成本;
- 站岗有价值——若这段代码将来因某处改动真的变成 VM bug 触发点,这个种子会立刻抓到;
- 常驻回归是「已经付过一次调查代价」的最便宜产出。

**入库位置的取舍**(#123 轮踩过一次):默认把 corpus 放进 `testdata/fuzz/FuzzXxx/<hash>`,但要先
判断 workload 本身的资源密度。fuzz coordinator 启动时在 `-parallel=N` 下**并行重放全部 seed
corpus** 作为 baseline coverage sweep;若 corpus 触发的 workload 本身很重(深递归 / 长循环 / 高
分配),并行重放瞬间放大资源压力,恰恰命中「不可复现 crasher」判定为进程级资源耗尽时怀疑的根因。
#123 轮的实测:corpus 入 `testdata/fuzz/` 后 fuzz-smoke 三条腿(mac / p3 / p4 ubuntu)在 30s 内
连挂——不是 corpus 有语义 bug,是 fuzz coordinator 的并发放大导致 worker 死。

判据:input 单独跑消耗几百 ms 以上 CPU、或触发深递归 / 大分配的,**改走 Go 回归测试**——写一个
显式测试(串行、单进程、逐 seed 跑一遍)覆盖同一形状,而不是入 `testdata/fuzz/`。功能等价,不搅
动 fuzz coordinator。#123 轮就是这样处理的:两个 corpus(`326b508e` / `8c132ff5`)从
`testdata/fuzz/FuzzAutoPromote/` 撤回,改成 `test/regression/issue123_regression_test.go` 里的显式测试。

**挪过去的时候要连 harness 的边界条件一起抄,不只抄那段脚本(2026-08-02,#218)**。一个 fuzz seed 的
**代价**由「脚本 + 该 fuzz target 设置的全部限制」共同决定,而 seed 文件里只有前一半。#218 那一轮:
seed 是 `for B=0,100001000 do ... end`(一亿次迭代),在 `FuzzP4ForceAllPromote` 里跑 1.8 秒——而那
1.8 秒**不是一亿次迭代的代价,是 `SetStepBudget(1 << 20)` 一百万步预算的代价**。第一版回归测试只抄了
源码、没抄 budget,于是跑到循环结束、耗时 **87 秒**,是它要替代的那个 seed 的 50 倍。动机在这里正好
反过来:这个 seed 被搬出 corpus 的理由就是它太重,而不抄限制等于把它原封不动搬进常规测试套件,
**而常规套件每次 `make` 都跑,比 corpus 重放更频繁**。

判据:把一个 fuzz seed 转成显式回归测试时,打开那个 fuzz target,把它设置的**每一个** limit 一起抄
过来——step budget、arena cap、force-promote 开关、输入长度检查,而不只抄脚本。自查办法:新写的回归
测试跑完之后量一次耗时,与那个 seed 在 harness 里的耗时对一下,**量级不同就说明漏抄了某个限制**,
那时测的是另一件事。镜像 budget 之后 #218 那条是 1.82 秒,与 harness 一致
(`test/regression/p4_hot_loop_promote_test.go`)。反思
[[2026-08-02-issue212-219-fuzz-crasher-batch]] 教训 4。

> **数值补记(2026-08-04)**:上面那个 `1 << 20` 是 #218 当时的 harness 取值。p4 的两个 fuzz target
> 现在共用常量 `fuzzbudget.Steps`(`internal/fuzzbudget`,值 `1<<16`,理由见下面「上限的余量」一节),
> 所以抄限制的时候要去读**那个常量当下的值**,不要照抄本节的字面量。`test/regression` 里的镜像测试是
> 两个包(常量不导出),按行为写死数值时要在注释里说清它镜像的是谁,写法参见
> [[prove-the-path-under-test]] §4.5c。

**判断两个 seed 是否重复,要算它们实际走的分支,不能看文件名或源码模式(2026-07-29,#209)**。
corpus 长期积累之后会出现一批**看起来同类**的 seed,合并掉多余的看着是划算的清理。但**seed 的
等价性由它实际走到的分支定义,而源码文本与那个分支之间往往隔着一层变换**(窄化 / 归一化 / 常量
折叠)。#209 那一轮:corpus 里四个 `table.insert(t,4...` 看起来是同一类,逐个算窄化之后的值才
发现 `4294967298 = 2^32 + 2` 模 2^32 之后是 **+2**——一个普通的正位置插入,根本不走那条昂贵跨度
的 skip,而其他三个都是约 -100M。按模式合并会删掉一个唯一的用例。判据:合并或删除 seed 之前,
对**每一个**算出它最终落在哪个分支(跳过 / 比对 / 哪一侧的哪个区间),按分支去重而不是按文本
去重;算不出来就先留着——corpus 重放的代价可以量(那一轮全量 0.62 秒),删错一个用例的代价量
不出来。反思 [[2026-07-29-issue209-stale-crasher-threshold-pin]] 教训 3。

**判据的首次正向消费**:#125 轮的 corpus `function sum() for A=0,0 do end end return sum() or (sum())` 是简单的 `function` + `for` 循环 + `return`,budget 天然有界,直接入 `testdata/fuzz/FuzzAutoPromote/b03a5a1dd9e56fbf` 常驻(fuzz worker 会把它当种子 mutation 探索周边形状),与 #123 的重 workload 走 `test/regression/` 形成对照。判据在这一轮首次被正向使用,证明可执行。反思 [[2026-07-11-issue125-return-freereg-round]] 教训 4。

**重 workload regression 测试的裁判机制:交给 `go test -timeout` 不要自建 per-run deadline**。走 `test/regression/` 路线的显式测试(如 `test/regression/issue123_regression_test.go`)针对的失败模式是**永不返回**(#123 类段内无限循环),不是「合法路径慢过某阈值」。（2026-07-20 起 issueNNN 回归测试实际已迁入 `test/regression/`,与本节早先声明的规范一致。）此时**不要**在测试内部用 `time.AfterFunc` / channel 类手法自建 per-run wall-clock deadline——那是量纲错配:测试想抓的是「非终止」,任何有限秒数都在跟共享 runner 的可变速度对赌,5s→30s→120s 的常量演进史本身就证明这类失败对常量修改免疫。正解是让测试直接跑裸的 `ProgramCall`,「永不返回」的判定交给 harness 自带的包级 `go test -timeout`(默认 10min,CI 未覆写)——它触发时严格更优:整个 test binary 被拿下 + 全部 goroutine 栈 dump,信息量比一行 `t.Fatalf("did not terminate within Ns")` 高一个数量级,同时 10min 相对合法 run(`-race` build 约 21s)的误报余量约 28×,比 in-test deadline 做得到的量级高得多。

自建 in-test deadline 只在这两种情况才有正当理由:(a) 测试**需要在超时后继续执行**(跑清理 / 对比结果 / 累积数据),或者 (b) 失败模式明确是「慢过某个具体阈值」而非「永不返回」。i123 两条都不属于,`test/regression/issue123_regression_test.go` 因此在 PR #129(commit 706ba26)整段删掉了自建的 `runWithDeadlineErr`。同理:全仓其它已存在的 `mustFinish` / `runWithDeadline` 类模式(例如 `test/regression/forloop_nan_limit_test.go` 的 10s deadline)按每处的误报余量分档处置——余量 < 10× 立刻换成包级 timeout,余量 10×~100× 作观察项,余量 > 100× 暂不动。反思 [[2026-07-12-i123-deadline-to-package-timeout-round]] 教训 1 + 教训 2。

#### 同一写法第三次被开成 issue 时,该调的是被接受的区间(2026-07-28,#203)

上面这条「重 workload 挪进 `test/regression/`」是**单条**输入的正确处置,但它对**同一写法反复
出现**这件事无效。`table.insert` 的重移位跨度这一写法被 nightly 开了**三个** issue:位置窄化成
约 100M 的移位跨度、刚好落在产品侧 2^27 上限之下,三个都**正确且对称**(两侧引擎都做这个移位、
结果一致),只是耗数秒,而 coordinator 启动时并行重放整个 corpus 会被这样的 seed 弄死。前两个都
按上面那条挪进了 `test/regression/insert_shift_cost_test.go`,每一次单看都对——三次之后它显然
不是在解决问题。

**判据**:**同一写法第 N 次(N >= 3)被 fuzzer 开成 issue 时,停下来问「为什么 fuzzer 还能生成
它」,而不是继续处理单个实例。** 挪 seed 处理的是「这一条输入现在不在并行重放里了」,它不改变
fuzzer 下一次还能生成一条同样的输入;成本是每次一轮人工分诊加一个 issue,收益只覆盖那一个采样
点。这与 [[prove-the-path-under-test]] §4.1「一个 reported case 是接受面的一个采样,不是那个
接受面本身」是同一判据在**处置侧**的形式:那条讲修完 reported case 要枚举整个接受面,本条讲
反复分诊同一采样点时该动的是接受面。

**#203 的答案**:产品上限与 harness skip 被绑成了**同一个数**,而它们回答的是不同的问题。产品侧
`tableInsertShiftCap` 留在 2^27,那是**正确性**边界——在它之下 wangshu 必须做这个移位,因为
lua5.1 会做(上一轮四轮审计都在反对拒绝参照实现能完成的工作,数值本身也是实测代价定下来的,见
[[prove-the-path-under-test]] §4.5);harness 侧的 skip(`internal/oracle/prelude.go`)降到 2^20,
那纯粹是「什么样的输入能待在并行 corpus 重放里」的资源问题。拆开之后 2^20-1 以下照旧比对、
2^20 及以上两侧对称跳过,而 `test/regression` 仍然串行跑一次真实的 100M 元素移位,所以「这个
工作确实被完成而不是被上限拒绝」这件事没有丢覆盖。两个阈值的**形状**仍然逐字对应(都按「低于
index 1 的距离」算),只是数值分开——形状不一致会让 skip 遮住产品的错,见
[[2026-07-28-four-diff-divergence-issues]] 教训 7;数值该不该分开见
[[prove-the-path-under-test]] §4.5b。反思 [[2026-07-28-issue201-203-unpack-skip-thresholds]]
教训 1/3。

#### 改完区间之后要问「有什么东西固定住这个改动吗」(2026-07-29,#209)

上一小节是**改**被接受的区间,本节是它的**下一步**:那个改动本身也需要一个执行体。

**判据**:**回归 seed 表达的是「这个输入不崩」,它表达不了「那个结构性决定还在」。** 修完一个
反复出现的问题之后,写一个断言「这次的结构性决定仍然成立」的测试。自查办法:假设本次改动被整段
revert,现有的测试会不会红?不会红就说明那个决定还没有执行体,只存在于代码与 commit message 里。
这是 [[prove-the-path-under-test]] §4.2「已登记为豁免这类声明必须能指向执行它的代码」换了个
时点——那条讲**声明**要有执行体,本条讲一个**已经做出的结构性决定**要有执行体。

**#209 实证**:#203 的两个阈值拆分是对的,但拆完之后没有任何测试表达「这两个阈值是两个数」。
把它们合回一个的话,现有 seed 全都照旧通过——合到低的那个(2^20),产品开始拒绝一段 lua5.1 能
完成的移位,而昂贵区的 seed 只是被 skip;合到高的那个(2^27),昂贵的那一段重新进入并行重放,
而现有 seed 恰好都在跳过区,`test/regression` 里原有的两条又只覆盖 2^27 之下的位置。所以这一轮
加了 `test/regression/insert_shift_cost_test.go::TestInsertShiftThresholdsStayDistinct`,断两个
阈值**之间**那一段的行为:必须由产品执行(不被上限拒绝)+ 必须便宜(skip 的取值就建立在这一段
便宜这个假设上)。断言的写法见 [[prove-the-path-under-test]] §4.5c(两个常数不导出且跨包,按行为
断言不复制字面量)。

**过期 issue 也值得看它的分布**。#209 本身按上面「第一步永远是版本核对」核一次就关掉了(第一档:
run 跑在 `cbd0512` 上、早于 `c07ba58`),但版本核对回答的是「**这一条** issue 还需不需要动作」,
它不回答「**这一类** issue 为什么还在被开」。判据:处理一个已经被修掉的 issue 时,核完版本再问
一次「为什么这类还在被开」,答案有三种——① 修法还没进 nightly 跑的那个 commit(等一轮就行)、
② 修法修错了位置(回到上面「一批 crasher 先问会不会被同一个改动一起解决」)、③ 修法对但没有被
固定住(本节)。第三种最容易被漏掉,因为它长得完全像第一种。反思
[[2026-07-29-issue209-stale-crasher-threshold-pin]] 教训 1/4。

### 2. 诊断硬化 —— 让下次复发自带诊断

无声外部 kill 之所以难查,是因为它没留下任何 in-process 证据——Go runtime 什么都来不及打,artifact
里只有一句 `exit status 2`。正确的投资方向是**把外部 kill 转成 in-process fatal**,或者**在被外部
kill 前 dump 系统状态快照**。#123 轮的两条硬化:

- `GOMEMLIMIT=6GiB`(在 `scripts/go-fuzz.sh` 或等价脚本里):压低 heap 峰值、让 GC 更早更积极地
  归还内存,降低撞上 OS OOM killer 的概率。**注意它是纯软限制**:`runtime/debug.SetMemoryLimit`
  文档明确「the application may still make progress」,Go runtime 在任何情况下都不会因它主动
  fatal——#123 轮曾误以为它能把无声 kill 转成带栈的 Go OOM,这个理解是错的(2026-07-18 复核);
- worker 无声死时 dump `free -m` + `vm.max_map_count`(以及等价的 OS 状态量)进上传 artifact,
  下次复发时 artifact 里就有当时的系统状态快照,不用对着 CI 日志一句话硬猜。

**Why**:低频罕见事件的调查成本大头不是「修」,是「等下一次复发」。等的时候免费,但复发时若信息不够
又要再等,循环下去。投资应该投在「让复发时的信息一次性够用」,不是投在「这次尽力挖」。

**硬化层级的最新状态(2026-07-19)**:`GOMEMLIMIT` 软限制已被观察到接不住这族死亡——2026-07-18
轮的 #156/#157/#159 三个 run 都已带上 PR #154 的 `GOMEMLIMIT=512MiB`,p4 worker 仍在约 4150 万
execs 处无声消失;2026-07-19 轮的 #162(concat storm 家族第 10 例)同样在 `GOMEMLIMIT=512MiB`
在场时于约 1240 万 execs 处静默死,本地重放 4.6 秒干净。软限制只影响 GC 节奏,既不会主动
fatal,也防不住分配速率超过 GC 回收速度时
RSS 冲过限制被 SIGKILL,更防不住非内存死因。

**第三层:worker 取证设施(PR #165,2026-07-19)**。原构想「harness 按 seed 记 wall-clock」
已被超集机制取代,交付两个机制(`fuzz_forensics_test.go`):

- **机制 A(尸检)**:TestMain 检测到 `-test.fuzzworker` 时把 fd 2 dup 到
  `fuzz-forensics/worker-<pid>-stderr.log` 并加 `debug.SetTraceback("all")`,接住此前被
  /dev/null 丢弃的 Go fatal 完整栈迹(合成 fatal 探针已验证栈迹确实进日志);
- **机制 B(飞行记录仪)**:每次 fuzz 回调(在各 target 的长度/NUL 检查**之后**——被 skip
  的输入不执行、不可能是真凶)把 seq / 时间戳 / target / 输入以单次 `WriteAt` 覆盖写进定长
  20KiB 的 per-PID 记录文件(容量覆盖最大 gated 输入 16KiB + header,逐字节可恢复,有单元
  测试钉住;热路径 0 alloc,`AllocsPerRun` 断言)。动机:不撞崩的 mutation 不会进任何
  corpus,而 minimized 输入又屡次被证明不是真凶——飞行记录是恢复「进程死亡时刻真正在跑的
  输入」的唯一手段;定长覆盖写,无 I/O 累积。

配套:`scripts/go-fuzz.sh` 按 target 隔离目录 `fuzz-forensics/<FuzzTarget>/`(经
`WANGSHU_FUZZ_FORENSICS_DIR` 传入,只清自己的目录——p1 job 先跑 native fuzz 再跑 oracle
fuzz,共享目录会让后者删掉前者的证据)、静默死亡失败时把栈迹日志 dump 进日志流并指向随
artifact 上传的原始飞行记录文件(不做文本过滤,防腐蚀 NUL/非 UTF-8 字节);nightly 失败
artifact 上传 `**/fuzz-forensics/**`。下一次家族复发时
artifact 里即有完整栈迹与在飞输入;若 stderr 日志仍只有 header(不是 Go fatal),则死因在
Go runtime 之外(如 coordinator 侧 pipe 断裂),同样是决定性的排除信息。
每一层没接住都是新信息,不是浪费。反思实例见
`memory/reflections/2026-07-18-issue155-158-nightly-crasher-round.md` 教训 4、
`memory/reflections/2026-07-19-issue163-tostring-meta-round.md`(#162 一节)与
`memory/reflections/2026-07-19-fuzz-worker-forensics-round.md`(机制 A/B 交付轮)。

**观察:minimized 输入本身往往不是死因(2026-07-19,#162)**。concat storm 家族累计 10 例,
本地精确重放全部干净——落盘的 minimized 输入更像「进程死亡时刻恰好在跑的那个」,真实压力更
可能来自 4 个 parallel worker 的叠加峰值,或 minimization 之前某个更重的 mutation 变体。这
也解释了为什么重放总是干净:重放只复现「单个 worker 跑单个 minimized 输入」这种最轻的情况。

## concat storm 家族已根因定性并修复(2026-07-20,PR #168)——真因是 CPU 看门狗,不是内存

上文各节把 concat storm 家族(#123-#167)当作「不可复现 / 疑似进程级资源耗尽(内存)」处理,
历轮处置是 corpus 入库 + 诊断硬化。**这段历史叙事本身有价值**(它建立了正确的止损流程、并催生
了下面结算的取证设施投资),保留;但它对死因性质的具体判断**后续被证伪**——真因是 CPU
wall-clock,不是内存。

**取证栈迹给出的定性(2026-07-20)**:#166/#167 两个 nightly p3 crasher 的 headSha 都是 PR #165
取证设施上线之后的 master HEAD——取证设施上线后家族**第一次复发**。两个 run 各有恰好一个长出
header 的 worker stderr 日志(机制 A 尸检接住了此前被 /dev/null 丢弃的 fd 2),抓到完整栈迹:
`panic: deadlocked!` + 一个 runnable goroutine 卡在
`gc.Collector.stringMatches → Intern → crescent.doConcat → executeLoop`。关键定性:
`panic: deadlocked!` 来自 Go fuzz 的 **per-input 看门狗**——`internal/fuzz/worker.go` 里
`time.AfterFunc(10*time.Second, panic)`,即单个 fuzz 输入跑过 10 秒就被打死。**死因是 CPU
wall-clock 撞 10 秒看门狗,不是内存 OOM**;历轮 `GOMEMLIMIT` / 降 arena cap 从来不奏效、最小化
corpus 本地重放永远干净,都因为死因根本不在内存这一维度。

**根因**:`preempt()`(state.go)在每个指令边界只把 stepUsed 加 1,**不计 CONCAT / Intern 的字节
工作量**。`for i=1,N do glob=cat(i) end`、cat 内 `return "<~15KB 字面量>"..i`,每次迭代只扣约 2 步
却拷贝 + intern 约 15KB;1<<20 步预算允许约 50 万次迭代,单次 prog.Run 约 2.7 秒字节工作,
`FuzzAutoPromote` harness 每输入跑 4 次 Run(2 State × 2 轮),4×2.7s≈11s > 10 秒看门狗 → worker
panic → CI 报 `exit status 2` → 自动开 crasher issue。落盘的是最小化后的**轻**输入(单次 Run < 10
秒),所以本地重放永远干净。

**修复(PR #168)**:共享 `doConcat`(`internal/crescent/call.go`)调 `chargeBulkWork(len)`
(`internal/crescent/state.go` 新增),按 `len >> 6`(1 步 / 64 字节)把 CONCAT 字节工作量折算进
step budget,使预算成为字节工作量的度量;三层(P1 executeLoop / P3 wasm h_concat / P4 native
host.Concat)全部路由同一 doConcat,单点记账覆盖所有 backend。回归 `test/regression/issue166_concat_storm_test.go`
(默认 build,证明解释器路径按字节触预算——也是 #166/#167 真实 P3-build 执行路径,取证栈迹为
`executeLoop -> doConcat`)+ `test/regression/issue166_concat_storm_p4_test.go`(P4 专属,经 `PromotionCount>0` +
`crescent.ConcatHelperHits>0` 双证据证明升层 native concat 真被字节记账);P3 不单独覆盖——外部审查
时实测 P3 对这些 concat 形状不升层(`PromotionCount` 恒为 0),其 h_concat 经构造走同一记账 doConcat。
#166/#167 corpus 入 `testdata/fuzz/FuzzAutoPromote/`。
VM 行为侧的对账见 `docs/design/p1-interpreter/implementation-progress.md`。

**范围**:这解决的是这一族**已知形状**(byte-heavy concat 循环:单指令做与字节数成正比的无界工作、
只扣常数步数)。它**不**代表所有不可复现 crasher 都已解决——本 guide 的止损流程、静默死亡两步
分类、诊断硬化三层仍是遇到新的不可复现 crasher 时的默认路径。**当时点名的下一批候选**(string.rep /
string.format / table.concat 尚未按工作量记账)**已在 2026-08-03 结算,见下一节**。

**这印证了取证设施投资的价值**。PR #165 的赌注是「让复发时信息一次性够用」——取证设施上线到家族
复发之间隔了不到两天,而家族此前空转了数周。上文诊断硬化第三层的「每一层没接住都是新信息,不是
浪费」这条纪律在本轮正面结算:机制 A 的 worker stderr 栈迹 + 机制 B 的飞行记录合起来一步定性,把
横跨数周的「查不出死因」变成一行日志的「根因清晰」。反思
`memory/reflections/2026-07-20-concat-storm-root-cause-round.md`(取证兑现 + 静默死亡两步分类直接
给答案 + 家族级误分类可持续数周,真值来自第三方证据而非表象反推 + 指令预算须度量工作量而非条数)。

**How to apply**:遇到「本地无法复现 + 生产 / CI 出现」类问题,除了调查根因之外,并行考虑:能不能加
一个便宜的诊断改动,让下次复发时留下更多信息?能就先做诊断硬化,再看要不要继续挖这次。

**教训来源**:反思 [[2026-07-11-issue123-unreproducible-crasher-round]] 教训 3。

### 那三个候选算子已结算,而「预测被验证」这件事本身是判据(2026-08-03,#222)

上一节末尾按名字列出了三个「尚未按工作量记账」的候选。**它们确实是**:#222 是这个家族的下一批,
seed 是一个 777777776 次迭代的拼接循环(target `FuzzAutoPromote`,run 跑在上一轮的合并提交上,
所以是真的新问题)。实测三个算子各自的紧循环,在 1<<20 step budget 内、**并且完全没有触发预算**
的情况下分别跑了 **21 秒 / 20 秒 / 53 秒**——单次 `prog.Run` 就已经超过 10 秒看门狗,而
`FuzzAutoPromote` 每个输入要跑**四次** Run。修法与 CONCAT 一样:`internal/crescent/state.go` 导出
`ChargeBulkWork`,三个 stdlib 函数各自按**产出字节数**记账,21/20/53 秒变成 46/90/70 毫秒且预算
正确触发,而八种普通写法(含 1 MiB 的 `string.rep`、10000 元素的 `table.concat`)与 lua5.1 逐字节
一致。回归在 `test/regression/issue222_bulk_builder_test.go`。

**头条教训不是那三个算子,是我怎么才想到去看它们的(教训 1)**。我先怀疑「这个 seed 太重」——本地
重放只要 **0.77 秒**,而且在自己那个 corpus 里只是**第三重**的(1.22s / 1.21s / 0.77s),两个更重的
早就该先死,所以「太重」解释不了;又怀疑内存——单 seed 峰值 RSS 只有 **106 MB** 而 CI 用的是
`GOMEMLIMIT=512MiB`,整个 corpus 并行重放峰值 525 MB 只是擦到一个软限制。两条都被自己的测量否掉,
之后才回来读上一节——而上一节不但已经定性(CPU wall-clock 撞看门狗、**不是**内存),还把这三个算子
按名字列了出来。

**判据**:**处理一个明显属于已知家族的新 issue 时,第一步是 grep 那个家族在 guide / 反思里的既有
结论,特别是「范围」「后续候选」「已知未覆盖」这类小节——答案可能在 issue 被开出来之前就写好了。**
Why:一个跨越数周、累计十几例的家族,它的既有结论是**已经付过一次调查代价**的产物,而现象是每一例
各自的表面;从现象重新推导的起点通常就是那一例最显眼的那个量(这一次是「重」和「内存」),恰好是
家族历史上已经被证伪过的两条。这与本 guide 开篇「第一步永远是版本核对」同一层级——都是先用最便宜
的检查排除最便宜的解释,而「读一遍这个家族的结论」比跑任何一条测量都便宜。自查办法:动手做第一次
测量之前,先说出这个家族上一次的定性结论是什么;说不出来就说明还没读。

**教训 4:「本地重放干净」对这个家族天然无效,不构成任何证据**。上文「minimized 输入本身往往不是
死因」记的是这个**现象**,本条把它写成**判据**:这个家族的死因是单次 Run 的 wall-clock 撞 10 秒
看门狗,而落盘的必然是最小化之后的**轻**输入(单次 Run 远低于 10 秒,否则最小化过程自己就会被打死),
所以「重放干净」是这类 artifact 的**必然属性**,它不含任何关于有没有缺陷的信息。判据:遇到
`hung or terminated unexpectedly` / `panic: deadlocked!` 类死因时,不要用「单 seed 本地重放通过」
结案;要去量「harness 每个输入跑几次 Run × 单次耗时」是否逼近看门狗,并且把 seed 的写法抽成一个
**不最小化**的紧循环重新量(本轮那 21/20/53 秒就是这样量出来的)——轻输入量不出问题,重写法一量
就出来。

计量单位与「同类资源只该有一个计量器」这两条落在
[[prove-the-path-under-test]] §4.5d。反思 [[2026-08-03-issue221-222-bulk-builder-budget]]。

## 上限的余量:界住不等于够快(2026-08-04,#224/#225)

上面两节讲这个家族**有没有界**——`chargeBulkWork` 让 step budget 度量字节工作量,于是无界的 concat
循环变成有界的。本节讲**界住之后的下半句**:那个界允许的最坏耗时,还得与真正会杀掉进程的那个超时
差得够远。

**核心断言**:**一个资源上限只要「界住」还不够,它允许的量必须与外部看门狗差一个数量级。** 一个上限
不是孤立生效的,它上面还叠着若干外部超时(fuzz 的 10 秒 per-input 看门狗、CI job timeout、
`go test -timeout`),而这些超时量的是 **wall-clock**,不是步数。上限与超时之间有一个隐含的换算:
`上限允许的量 × 单位量的本地耗时 × 目标机器的慢速倍率 = 投射 wall-clock`。这三个因子里只有第一个
写在代码里。**余量不够时,输入从「无界」变成「只是太慢」,而看门狗分不出这两者**——症状与真正的无界
完全一样(`panic: deadlocked`),于是它会被反复开成 crasher issue,而每一次单看都找不到产品缺陷。

**#224/#225 实证**:`1<<20` 的 step budget 在 1 步 / 64 字节的比率下允许约 **64 MiB** 的 concat,
本地每个 fuzz 子测试花 **0.7–1.3 秒**。而 **CI 运行器比本地慢约 10 倍**——这个数字不需要重新测,
`internal/crescent/state.go` 的 `chargeBulkWork` 注释里早就写着(#168 那轮正是按最慢的 CI runner
把比率从 1 步/KiB 收紧到 1 步/64 字节的)。乘一下:

| 量 | 值 |
|---|---|
| 本地最慢的家族子测试 | 1.34 秒 |
| 投射到 CI(×10) | 12–13 秒 |
| go-fuzz 的 per-input 看门狗 | **10 秒** |
| 六个家族 seed 的余量 | **两个已经超过**,另外四个不到 **1.4 倍** |

修法是把 harness 的 step budget 从 `1<<20` 减到 **`1<<16`**(`internal/fuzzbudget` 的
`fuzzbudget.Steps`,`fuzz_auto_test.go` 与 `fuzz_p4_test.go` 四处 `SetStepBudget` 共用它)。**产品代码
一行没改**——这是 harness 的余量问题,不是 VM 的缺陷。corpus 全量重放 5.5 秒 → **0.85 秒**。

**判据**:定一个资源上限时,把「这个上限允许的最坏耗时」乘上目标机器的慢速倍率,再与那台机器上所有
外部超时比一遍;**余量小于一个数量级就等于没有余量**。自查办法:写下那个三项乘法,列出投射耗时要躲开
的每一个超时;任何一项余量不到 10 倍,这个上限就还没定完。这与本 guide「重 workload regression 测试
的裁判机制」是同一条判据在**测试侧**的形式(那条按误报余量分档:< 10× 立刻换成包级 timeout、
10×~100× 作观察项、> 100× 暂不动),与 [[prove-the-path-under-test]] §4.5b 的关系是:那条讲一个上限
防的是正确性还是资源,本节讲一个**资源**上限的数值还要对齐它所在机器上的超时。

### 上限要由「计费相同时最贵的写法」定,不能由「恰好被开成 issue 的那个写法」定

这一轮**第一版取的值是 `1<<19`(把预算减半),一次独立审计推翻了它**,而推翻的方式正是上面那条判据被
用错的方式:我把「这个上限允许的最坏耗时」当成了「手里那六个 corpus seed 里最慢那个的耗时」。按四次
Run × CI 慢 10 倍投射,在 `1<<19` 下实测:

| 写法 | 投射耗时 | 对 10 秒看门狗的余量 |
|---|---|---|
| `local out="" for i=1,777777776 do out=out.."x" end` | 14 秒 | 0.70 倍 |
| `local t={} for i=1,777777776 do t[tostring(i)]=i end` | 18 秒 | 0.56 倍 |
| `local s=string.rep("a",4096) for i=1,777777776 do s:gsub("%a","x") end` | **41 秒** | **0.24 倍** |

`1<<19` 只修好了那六个 seed,而它们的**邻域仍然在看门狗之上**——最后那条是看门狗的**四倍**,比被修的
seed 还糟。

**核心断言**:**一个上限要由「计费相同时最贵的写法」定,不能由「恰好被开成 issue 的那个写法」定。**
step budget 的单位是**计费额度**,看门狗量的是 **wall-clock**,而两者之间的换算率**逐算子不同**:
`gsub` 每次调用按大约两倍主串计费,可它要跑模式匹配、按匹配次数改写、再拼结果,实际工作远多于等额
计费的一次 concat——**等额计费不等于等额 wall-clock**。被 fuzzer 开成 issue 的那个 seed 只是这个上限
之下的一个采样点,而且是被最小化过程削轻过的采样点,用它定上限等于用一个偶然样本代表整个可达集合的
上界。

**判据**:定一个上限之前,把**同一计费额度之下最贵的几种写法**都量一遍,用最坏那条定值。自查办法:说
「这个上限够了」之前先列出「同样吃掉这份额度的写法还有哪几种」,逐个量投射耗时;列不出来就说明还没找到
这个上限之下真正可达的最坏情况。找候选的办法是沿着**计费口**枚举——凡是走同一个计费器(本仓是
`ChargeBulkWork`)的算子,每一个都写一个紧循环量一次。终值 `1<<16` 就是这样定的:上面最坏那条降到
**5.2 秒**、余量 **1.93 倍**,是第一个满足前面那条数量级判据的值。

### 一个「防住某个数值」的回归测试,必须用变异实测确认它真的会因为那个数值变化而变红

同一次审计的第二个发现:`test/regression/issue224_watchdog_margin_test.go` 第一版声称防住「预算被调
回去」,而**实测把预算调回 `1<<20`(正是那个回归)时它照旧通过**。两处都是「宽了一点」而不是「写错
了」,所以读起来完全正常:① 上界写了 1 秒,而它自己的注释里推导出的是 250 毫秒(`10 秒 / 10 倍 /
4 次 Run`)——注释推出一个值、代码用了它的四倍,而这个宽度正好把被防的回归放了进去;② 它拿「四次 Run
的投射」去比「一次 Run 的测量」,两个量不同量纲。再加上它只用被开成 issue 的那两个 seed,而那两个在
`1<<19` 下本来就够便宜,所以它连 `1<<16` 与 `1<<19` 都分辨不出来。

**判据**:写完一个「防住某个数值」的测试,**立刻把被防的值改回去跑一次;不变红就是没有防住任何东西**。
自查办法:变异一次之后再问两个问题——上界是不是就是推导出来的那个数(不是它的若干倍),以及被比较的
两个量是不是同一个量纲(投射比投射、测量比测量)。并且把「真正约束这个数值的写法」放进用例表,不要只
放「恰好被开成 issue 的写法」(同上一条)。现在这个测试的上界是推导出的 250 毫秒、用例表加进了上面
那三个写法,能同时抓住 `1<<19` 与 `1<<20`,**已用变异实测确认**。写法参见
[[prove-the-path-under-test]] §4.5c。

**降低 fuzz 预算不等于降低覆盖,但这句话要证明**。第一版这句话是推理出来的(「这一族每种写法在两个
预算下都会触发,fuzzer 走同样的路径、只是更早停下」),这一版改成实测:**harness 自己的 seed corpus 在
`1<<20` / `1<<19` / `1<<16` 三个预算下 `PromotionCount` 完全相同**——那些写法在几次调用之后就升层,
从不接近这三个上限中的任何一个。缩小的只是「单个输入能消耗多少 wall-clock」,而那恰好是看门狗量的
东西。为什么不能只靠推理:预算变小之后,某些输入可能在到达被测代码之前就退出,于是测试变绿的原因悄悄
从「两侧一致」换成「这个输入根本没跑完」,而表现是**测试照旧通过**。所以本轮还**注入了一个真实的
P1-vs-P4 分歧**(把升层侧的返回值截断),确认 `1<<16` 下 harness 仍然 FAIL、撤掉注入之后通过;90 秒
引导式 fuzz 干净。判据:调紧 harness 的资源限制之后,用「注入一个必须被发现的缺陷」证明检测能力、用
一个**白盒计数器**证明覆盖,而不是只看现有用例是否仍然通过;方法论见
[[prove-the-path-under-test]] §9.2。

**变窄的地方要如实记下**:`1<<20` 时 corpus 里有两个 seed 会把 arena 推到上限,`1<<16` 时没有,所以
那条 arena-cap 错误分支在这里覆盖到的写法变少了。这是**收窄而不是空洞**——
`test/regression/issue144_regression_test.go` 直接覆盖 arena cap,而且实测两个预算下 step budget 都
先于 arena cap 触发,这条路径本来就不是靠 step budget 到达的。

反思 [[2026-08-04-issue224-225-watchdog-margin]] 教训 2 / 3 / 4 / 5。

## CI 自动化本身的失败信号(2026-08-09,#236–#241)

前面各节管的是「nightly 报上来的 crasher 怎么分诊」,本节管**产生那些 issue 的那套机制**自己的
缺陷。它排在分诊之前:分诊的第一步是读 issue,而这两条决定那批 issue 是不是可信的输入。

### 一个失败步骤让后续步骤 skip,会造出「报红但什么都没测」

**实例(#236–#241)**:nightly 的 oracle 安装步骤失败,让后面三个差分 fuzz 步骤全部
`skipped`(step 5/7/9),于是那一轮报 failure 而**实际什么都没测**,那一晚的探索预算是零。

**为什么容易错**:红色的**默认含义**是「跑了并且发现了问题」,这个默认在绝大多数情况下成立,
所以看到红色的第一动作是去找「发现了什么」。依赖步骤失败造成的红色含义相反(「没跑」),而它与
真失败**在同一个信号上** —— 一个红叉、一封同样的通知;区分它们要点开去看每一步的状态,而那正是
人看到红色时最不会先做的事。危险方向是**乐观**的那一侧:探索预算变成零而没有任何东西说出这件事,
真分歧至少会被人读到。

**判据**:给 CI 加依赖步骤时,问一句「**这一步挂了之后,后面被 skip 的步骤里有没有本来该产生结论
的**」;有的话,失败信息里要写清「未执行」而不是只写「失败」。自查办法:把 workflow 的步骤列成两栏
——「准备类」(装依赖、取包、建缓存)与「产生结论类」(测试、fuzz、比对);凡是准备类失败会 skip 掉
结论类的,那条失败路径就需要一句显式的「本轮未测」。

本仓现状如实记:#236–#241 那一轮既修好了去重(按日期,见下),也补上了「本轮什么都没测」这句话 —— infra issue 的 body 读 `steps.difffuzz.outcome`,差分步骤被 skip 时明确写出「本轮未执行任何差分 fuzz」。

### 自动开 issue 的去重键,必须与「一次事件」的粒度对齐

**实例(#236–#241)**:infra issue 的标题原先嵌了 `${{ matrix.variant }}`,而 **infra 失败天然横跨
所有 tier**(装不上依赖、上游不可达,与被测的是 p1 还是 p4 无关)。p1/p3/p4 生成三个不同标题,
按标题去重的逻辑看不出它们是同一件事:**两次上游抖动 × 三个 tier = 六个 issue**。改成按
**日期**(而不是 run id,也不是 tier)之后,同一天的每一次触发与每一个 tier 都收拢成一个 issue:第一个报的 job 新开,其余评论。

**为什么容易错**:去重键最自然的写法是「把能唯一标识这次失败的东西都放进标题」,而这个直觉在
divergence 类事件上是对的(它天然属于某个 tier)。同一个标题模板服务两类性质不同的事件时,更细
的那一维会被无条件继承;而**多加一维只会让分组更细,永远不会报错**,所以这个错误没有任何自动
信号,全部代价落在读 issue 的人身上。

**判据**:给自动化加去重时,先问「**通知读者希望按什么恢复周期处理事件**」(这里是同一天的持续
基础设施故障),再选键。自查
办法:对每一维问「这一维变化时,是同一件事还是两件事」——答「同一件事」的维度不能进键(tier 变化
时仍是同一次上游抖动 → tier 不进键;同一天的 schedule run 变化仍属于同一次持续故障 → run id 也不
进键;日期变化才开启新的通知周期)。配套要点:**被去掉的那一维
不能丢**,挪进正文(现在 tier 在 body 的「首个报告的 tier」与后续 job 的评论里),去重键与信息量是
两件事。

## wall-clock 断言只有在机器间波动小于余量时才成立（2026-08-09）

共享 runner 曾把本地约 1.5 秒的 bulk-budget 用例跑到 24.2 秒，约 16 倍；测试预留 10 倍余量仍然
假失败。它真正要证明的是 step budget **会触发**，而这一离散性质已有直接断言。删除 wall-clock
失败条件后，同包审计又发现一个本地 3.63 秒、上限 5 秒（仅 1.38 倍余量）的兄弟断言；它依赖的
真正性质是两个阈值保持分离，也已有直接测试。

**判据**:先直接断言要保护的离散性质。只有已观察到的机器间波动小于断言余量时，wall-clock 才能
决定测试成败；否则只用 `t.Logf` 保留退化可见度。发现一处计时断言测到 runner 后，立即搜索同包、
同机制的兄弟断言，因为它们通常只是在等待下一台足够慢的机器。若时间本身就是产品契约，则仍应按
前文的方法测量最慢可达写法并保留足够的跨机器余量。

反思 [[2026-08-09-runner-wall-clock-assertion-audit]]。

反思 `memory/reflections/2026-08-09-issue236-241-curl-timeout-misread-as-enospc.md` 教训 3 / 4。

## 与其他 guide 的关系

- 与 [[prove-the-path-under-test]] 互补:那篇管**可复现问题的修复怎么修对**(证在测的路径真被
  走到、证被归因的路径真的存在、证收益来自稳态生效);本篇管**不可复现问题怎么止损**(判定为
  不可复现后不硬编修复,corpus 入库 + 诊断硬化让下次复发自带信息)。两篇在**验证一个 crasher
  是否真的修好**这一点上接口:本篇的「一批 crasher 先问会不会被同一个改动一起解决」负责分组,
  那篇 §9.6 负责给每一条定性(断逐字节 equal 不是断绿 + 在修复前的 base 上双向验证);差分
  harness 里 skip 也是绿的,把它读成「修好了」等于把输入又藏了一次。
- 与 [[cross-backend-semantic-fix-sweep]] 互补:那篇管**修同一语义类 bug 时枚举全部后端 × 通道**;
  本篇的判定前提是「input 决定的 VM bug 与进程级资源耗尽已经分开」,分开之后属于 VM bug 的那类才
  可能进入 cross-backend sweep 的范围。
- 反思实例:`2026-07-11-issue123-unreproducible-crasher-round`(七角度复现矩阵 + 分诊 + corpus
  入库 + `GOMEMLIMIT` 诊断硬化) · `2026-07-03-issue40-arm64-stopbleed-round` §「其它(较小)」
  fuzz 失败形式分诊纪律(deadline vs failing-input 判据的来源) ·
  `2026-08-02-issue212-219-fuzz-crasher-batch`(版本核对的三档是成本档不是缺陷档:八个 run 与 #209
  一样全落在旧 commit 上,实际重放后五个如实复现、三个真缺陷;八个 issue 只有六个不同 seed hash;
  重 workload 挪进 `test/regression/` 时要连 harness 的 step budget 一起抄,漏抄让 1.8 秒变 87 秒) ·
  `2026-08-03-issue221-222-bulk-builder-budget`(#221 是第一档但仍实际重放确认;#222 结算了 concat
  storm 家族点名的三个候选算子,而定下来的方式是**读家族自己的结论**而不是从现象推——先怀疑「太重」
  再怀疑内存,两条都被自己的测量否掉) ·
  `2026-08-04-issue224-225-watchdog-margin`(版本核对的第四步:把 seed 拿到旧 commit 上也跑一遍,
  发现它们在那里就已经被界住,于是「已被上一轮修好」这个归因被证伪;真实原因是 harness 的**余量**
  ——`1<<20` 允许的 64 MiB 投射到 CI 是 12–13 秒对 10 秒看门狗。**第一版按被开成 issue 的 seed 把预算
  减半到 `1<<19`,被一次独立审计推翻**:那六个 seed 修好了,而邻域里的 `gsub("%a","x")` 循环在 `1<<19`
  下投射到 41 秒、是看门狗的四倍;终值 `1<<16` 让最坏那条降到 5.2 秒、余量 1.93 倍,corpus 全量重放
  5.5 秒 → 0.85 秒,产品代码零改动。同一次审计还发现那个回归测试在预算调回 `1<<20` 时照旧通过,
  于是补上「上限按最贵写法定」与「防数值的测试要用变异确认」两条判据) ·
  `2026-08-05-issue232-234-address-width-and-gsub-escape`(定时巡检的**第一次实际处理轮**:三个
  `FuzzOracleDiff` crasher 全部真复现,而 #232 与 #233 是**同一个根因的两种可见度**——引用值地址的宽度,
  一个表现为原始输出不同且间歇、一个表现为长度不同且稳定;#234 是引擎侧 gsub 替换串的 `%` 转义,同一段
  `add_s` 逻辑的两个出口都没跟上,其中一半是既有缺陷) ·
  `2026-08-09-issue236-241-curl-timeout-misread-as-enospc`(**分诊自己判错的一轮**:`exit code 28` 被读成
  ENOSPC(errno 28),实际是 curl 的 `CURLE_OPERATION_TIMEDOUT` —— 那一步是 curl,退出码来自 curl 自己的
  表;而「apt 成功之后沉默 2 分 15 秒才失败」这条免费证据一开始就排除了磁盘写满,资源耗尽立刻失败、
  超时才会先卡。真根因是裸 `curl -sLO` 没有限时没有重试;它失败让三个差分 fuzz 步骤被 skip,于是 nightly
  报红而什么都没测;标题里嵌 `matrix.variant` 让一次抖动开了六个 issue)。
