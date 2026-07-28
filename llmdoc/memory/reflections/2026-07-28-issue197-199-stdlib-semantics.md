---
name: 2026-07-28-issue197-199-stdlib-semantics
description: >
  三个 issue（#197 / #198 / #199）的处理轮，分支 `fix/197-199-stdlib-semantics`，7 个
  commit。最值得记的是 **#197 上一轮被我主动填成 issue 而不是硬凑**，这一轮真去做，证明当时
  的判断是对的：`error(msg, level)` 的位置前缀不是加个常数偏移就能对，需要两层认识——①
  **host 边界是一个 level，不是终点**（PUC 对 `pcall(f)` 的栈是 `[f, pcall(C), caller]`，
  level 2 落在 C 帧上是空前缀、level 3 走到 caller 有前缀，把边界当终止会让 level 3 及以上
  全错成裸消息）；② **一个边界可能代表多个叠起来的 host 帧**（`pcall(pcall, f)` 在 PUC 是
  两个 C 帧、在 wangshu 只有一次 re-entry，而 `st.nCcalls` 是运行总数区分不了它与
  `pcall(function() return pcall(f) end)`），所以每帧的 host 帧计数进了 `callInfo` 的打包位
  （bit 51-54）。#198 是 `%g` 用了 Go 的最短往返而不是 C 的默认精度 6（显式精度本来就对，
  `%e`/`%f` 的默认也本来就对，只有 `%g` 的默认不同）。#199 是一组七处分歧：gmatch 的前导
  `^` 是普通字符、`assert` 第二参数走 `luaL_optstring`、`math.modf` 对无穷、`math.frexp` 在
  DBL_MAX 附近、`math.rad` 溢出、`io.write` 返回布尔（**issue 里写的「返回文件句柄」是错的**，
  那是 5.2+）、`os.date` 的指令覆盖面与 `*t`/`!` 前缀。另有一处**故意不对齐**：`print` 对
  内嵌 NUL 不截断，因为 PUC 的 `luaB_print` 用 `fputs` 而它自己的 `io.write` 用带长度的
  `fwrite`，参照实现内部就不一致，对齐那个产物等于故意丢用户数据。修完 #197 顺手**撤掉了
  harness 里为它加的 skip**，24 种 error level 写法现在零 skip 参与比对。主动清扫生成 296 个
  探针直接与系统 `lua5.1` 比对，289 个可比对项零真实分歧。
metadata:
  type: reflection
  date: 2026-07-28
---

# `error` level / `%g` 默认精度 / 七处 stdlib 分歧（2026-07-28，分支 `fix/197-199-stdlib-semantics`）

> 范围：#197 / #198 / #199 三个 issue，7 个 commit。改动落在
> `internal/crescent/errors.go` + `state.go` + `frame.go` + `meta.go`（error level 走帧 +
> `callInfo` 打包位）、`internal/stdlib/stringlib.go`（`%g` 默认精度、gmatch 传参）、
> `internal/stdlib/pattern.go`（anchor 选项）、`internal/stdlib/stdlib.go`（`assert`、
> `print` 的 NUL 注释）、`internal/stdlib/mathx.go`（modf / frexp / rad）、
> `internal/stdlib/tablelib.go`（`os.date`、`io.write`）、`fuzz_oracle_test.go`（撤 skip）。

## 任务

三个 issue：#197 `error(msg, level)` 不认 level、#198 `%g` 精度、#199 一组分歧。其中 #197
是上一轮（`fix/192-194-196-diff-divergences`）我**主动填成 issue 而没有硬凑**的那一条，理由
写在当时的注释里：wangshu 不把 host 帧压进 cis，加个常数偏移只在「中间恰好是 C 帧」时才对。

## 本轮做了什么

### 1. #197 `error(msg, level)` 的位置前缀不认 level

`error` 一直解析了 level 却从不用它选帧，于是**每个 level ≥ 2 都拿到最内层的位置**。PUC 的
`luaB_error` 调 `luaL_where(L, level)`，它命名的是向上 LEVEL 步的那一帧，走到 C 帧时前缀为
**空**——这就是为什么 `pcall` 里的 `error("m", 2)` 报的是裸 `"m"`：那里 level 2 就是 `pcall`
自己。

修法需要两层认识，都不是走一遍帧链就有的：

- **host 边界是一个 level，不是终点。** PUC 对 `pcall(f)` 的栈是 `[f, pcall(C), caller]`，
  所以 level 2 落在 C 帧上（空前缀）、level 3 走到 caller 是**有**前缀的。我第一版把跨越
  边界当成终止，于是 level 3 及以上全错成裸消息。
- **一个边界可能代表多个叠起来的 host 帧。** `pcall(pcall, f)` 在 PUC 里是两个 C 帧，在
  wangshu 里只有一次 re-entry；而 `st.nCcalls` 是运行总数，区分不了它与
  `pcall(function() return pcall(f) end)`（两者总数相同、答案不同）。所以最终把**每帧的
  host 帧计数**存进 `callInfo` 的打包位（`state.go` 的 word2 bit 51-54，四位；段镜像的
  round-trip 等值自检 `verifyCISeg` 覆盖了新字段），由 host re-entry 时递增的
  `st.pendingHostFrames` 填充、下一次 Lua 帧压栈时消费并清零。

四位够用的理由：re-entry 深度上限远低于 15，超出即饱和。

验证：与系统 `lua5.1` 比对 30 种写法（五种嵌套 × level 0-5，含 `pcall(pcall,f)` 与嵌套
`pcall`）全部一致。

**连带撤掉了 harness 里为它加的 skip**：`fuzz_oracle_test.go` 原本跳过任何提到 error 第二
参数的输入（`errorLevelAtLeastTwo`），现在 24 种 error level 写法**零 skip** 参与比对，函数
改名 `errorLevelUBRange`、只剩「非有限 / 超出 int64 的 level」跳过——那是真正的 arch 相关
UB（`luaL_checkint` 的窄化）。

### 2. #198 `%g` 用了 Go 的最短往返而不是 C 的默认精度 6

`string.format("%g", 1/3)` 得 `0.3333333333333333`，C 得 `0.333333`。C 的 `%g` 默认精度是
6，Go 的默认是「能往返的最短表示」。**显式精度本来就对**，`%e`/`%f` 的默认也**本来就对**
（Go 对这两个的默认精度与 C 一样是 6）——只有 `%g` 的默认不同。修法是在没有显式精度时补
`.6`。验证 13 种写法与 `lua5.1` 一致（含 1e6 处转指数形式、`%.3g`、`%.0g`、`%G`、宽度、
左对齐、补零、1e-5 边界、`%#g` 保留尾零、需要舍入的值）。

### 3. #199 的七处分歧

- **`gmatch` 的前导 `^`**：PUC 的 `gmatch_aux` 直接调 `match()`、没有 anchor 处理（不像
  `str_find_aux`），所以 `^` 在 gmatch 里是**普通字符**，`gmatch("ab","^a")` 什么都不产出。
  给 `patternFind` 加了 `patternFindOpt` 的 allowAnchor 选项而不是写第二份实现；
  `find`/`match`/`gsub` 保持 anchor。
- **`assert` 的第二参数**：PUC 是
  `luaL_error(L, "%s", luaL_optstring(L, 2, "assertion failed!"))`，而 `luaL_optstring` 只
  接受字符串或数字（数字被强制转换），所以 table / boolean 消息是**参数错误**
  （`bad argument #2 (string expected, got table)`）而不是被 stringify。原来用
  `valueToString` 什么都收、并把 table 本身当错误值返回。
- **`math.modf` 对无穷**：C 的 `modf` 给无穷的小数部分是**带符号的 0**，Go 的 `math.Modf`
  返回 NaN，于是 `math.modf(1/0)` 是 `inf nan` 对 C 的 `inf 0`。
- **`math.frexp` 在 DBL_MAX 附近**：原来用 `floor(log2(|x|))+1` 推指数，那么靠近上界时
  `Log2` 会向上取整、后面的 `Ldexp` 除数又不可表示，于是报 1025 而 C 报 1024。改成直接用
  `math.Frexp`（它对 0 / inf / NaN 的约定与 C 一样）。
- **`math.rad` 溢出**：`x*pi/180` 在 x 接近 DBL_MAX 时**乘法**就溢出成 inf，而结果本身可
  表示（`math.rad(1.797e308)` 是 3.1375664143846e+306）。改成乘单个常数 `pi/180`，PUC 用的
  就是 `RADIANS_PER_DEGREE` 一个常数。
- **`io.write` 的返回值**：原来一个值都不返回，于是 `type(io.write(""))` 报
  `value expected`。5.1 的 `g_write` 压的是**布尔**成功标志（`lua_pushboolean(L, status)`），
  写失败返 false 而不是抬错。**issue 里写的「返回文件句柄」是错的**，那是 5.2+ 的行为。
- **`os.date` 指令覆盖面**：原来用 `strings.Replacer` 处理六个指令，其余原样透出
  （`os.date("%j")` 返回 `"%j"`），而且 `*t` 与前导 `!` 压根不认（表形式与 UTC 都到不了）。
  改成指令循环，覆盖 `Y y m d e H M S I p j a A b B c x X Z w n t %%`，未定义指令原样输出
  （glibc 就是这样）；`*t` / `!*t` 返回九个字段的表（wday 以周日为 1、yday 从 1 起）。验证
  九种写法与 `lua5.1` 逐字节一致，含完整 `*t` 字段集、午夜的 `%I`/`%p`（12 AM 不是 00）、
  `%e` 的空格补齐、一个未定义指令。

### 4. `print` 对内嵌 NUL 故意不改（理由记在代码里）

PUC 的 `luaB_print` 用 `fputs`、停在第一个 NUL，所以 `print("a\0b")` 只输出 `"a"`、后面
静默丢掉。这是 C 调用的产物而不是 Lua 语义——5.1 的字符串明确是 8-bit clean、可以含 NUL，
而 PUC **自己的** `io.write` 用带长度的 `fwrite`、不截断，**参照实现内部就不一致**。对齐这个
产物等于故意丢用户数据。与上一轮「参数求值顺序未指定」（`f9425ab`）属同一类：**参照实现的
某些行为不该被复制**。差分 harness 用自己的累积器捕获 print、不经 C `FILE*`，所以这里不会
表现为分歧。

### 5. 主动清扫

生成 296 个探针直接与系统 `lua5.1` 比对：`%g` 与浮点 verb（12 个值 × 13 种 spec）、
gmatch / find / gsub（× 9 种 pattern）、assert（× 7 种消息）、math（9 个值 × 7 个函数）、
`os.date`（16 种格式）、`io.write`。**289 个可比对项零真实分歧**；4 个差异是探针本身的产物
（地址文本、表里的 `tostring(nil)`、`io.write` 的副作用落到 stdout）。

## 期望与实际

- 期望：#197 是「补一段走帧链的代码」，#198/#199 是几处小口径修复。
- 实际：#197 的走帧链只是骨架，两层认识（边界算一级、一个边界可代表多个 host 帧）才是它
  为什么不能用常数偏移解决的原因，第二层还要求给 `callInfo` 加一个持久化字段。#199 里
  `io.write` 的 issue 描述本身是错的，`print` 的那一处正确做法是**不改**。

## 教训

### 教训 1（主动填成 issue 的判断得到了正向验证）

上一轮我拒绝给 #197 加常数偏移、写清「需要改调用机制」并开了 issue。这一轮真去做，发现
**确实**需要两处非平凡认识（边界算一级、一个边界可代表多个 host 帧），常数偏移在
`pcall(pcall,f)` 上必然错。

**Why**：一个「只在恰好如此的情况下成立」的修法能通过当前用例，但它把错误的模型写进代码，
下一个输入撞上时表现是新的分歧、看起来像新问题。填 issue 并写下**为什么不是小修**，那份
说明本身就是下一轮的起点——本轮的两层认识都是从那句「加个常数偏移只在中间恰好是 C 帧时才
对」推下去的。

**How to apply**：判据 = 当一个修法只在「恰好如此」的情况下成立时，填 issue 并写下为什么
不是小修，比硬凑一个能过当前用例的偏移更省后续成本。写下的理由要具体到**哪个前提不成立**，
不要只写「需要重构」。

### 教训 2（为某个 bug 加的 harness skip 必须随 bug 一起撤掉）

`fuzz_oracle_test.go` 里为 #197 加的 skip 覆盖任何提到 error 第二参数的输入。#197 修好之后
如果不撤，整个 level ≥ 2 的区间就一直在比对之外，而它读起来像「已处理」。

**Why**：这是 [[prove-the-path-under-test]] §4.2「已豁免声明必须有执行体」的**时间对偶**：
那条讲「声明要有执行体」，这条讲「执行体要随 bug 撤」。一个还活着的 skip 与一句没有执行体的
豁免声明在阅读上是一样的——都表示「这里不用看了」，区别只是前者真的在运行。

**How to apply**：修完一个 bug，`grep` 一遍为它加过的 skip / 豁免 / 已知边界条目，逐条撤或
收窄，并跑一遍确认相应用例现在**零 skip** 参与比对。本轮撤后剩下的那条要连名字一起改
（`errorLevelAtLeastTwo` → `errorLevelUBRange`），因为旧名字描述的是已经不存在的理由。

### 教训 3（参照实现的默认值差异只在「没有显式参数」时暴露）

`%g` 的显式精度一直是对的，`%e`/`%f` 的默认也一直是对的，只有 `%g` 的默认不同。这类差异在
带参数的测试里完全看不见。

**Why**：格式化 / 转换类接口的「默认值」是参照实现的一段独立逻辑，与「给了参数怎么用」不是
同一段代码。按参数值扫矩阵（各种精度 × 各种值）会把默认路径整条漏掉，因为矩阵的每一格都
带着参数。

**How to apply**：测格式化 / 转换类接口时，把**「不给可选参数」当成一个独立维度**扫，而不
只是扫各种参数值。对每个可选参数问一句「省略它时参照实现取什么，那个取值是谁定的」。

### 教训 4（同一个函数族里「参照实现自己不一致」是不该对齐的强信号）

`print` 截断内嵌 NUL 而 `io.write` 不截断，两者都在 PUC 里、处理的都是同一种 8-bit clean
字符串。这种内部矛盾说明其中一个是实现产物而不是语义。

**Why**：「与 PUC byte-equal」这个目标假设 PUC 对同一件事有唯一答案。同族的两个函数给出
相反答案时这个假设不成立，此时「对齐 PUC」没有唯一指称对象——照抄哪一个都是在把某条 C 调用
的副作用写成语义。这与 [[cross-backend-semantic-fix-sweep]]「有定义 / UB / 未指定」那套判据
是同一层的判断，只是证据来源不同：那三类看 C 标准怎么规定，这一类看参照实现自己是否自洽。

**How to apply**：发现参照实现在同类操作上自相矛盾时，去查语言规范怎么说（这里是 5.1 手册
明确字符串 8-bit clean 可含 NUL），按规范选，并把偏离**记进代码注释**，注明哪个是产物、
为什么不对齐、以及在差分侧为什么不表现为分歧。

### 教训 5（issue 里的事实描述也可能是错的，要独立核对）

#199 写着 `io.write` 「返回文件句柄」，实测 5.1 返回**布尔**（`lua_pushboolean(L, status)`），
句柄是 5.2+ 的行为。

**Why**：issue 正文是写的人当时的理解，不是核实过的事实；混进一个别版本的行为很容易，尤其
在 5.1/5.2 差异密集的地方。照 issue 正文实现会得到一个「按要求做完」但与参照实现不符的结果，
而且它有一条 issue 替它背书——与 [[prove-the-path-under-test]] §4.3「测试期望写错时默认怀疑
实现」是同一个陷阱的另一个入口。

**How to apply**：修 issue 时对它陈述的每一条参照行为都亲自跑一遍（跑 oracle / 读
`_lua515/` 对应实现），不要把 issue 正文当作已核实的事实——**哪怕那个 issue 是我自己开的**。

## Promotion 决策

- **教训 2** 已补进 [[prove-the-path-under-test]] §4.6（skip 要随 bug 一起撤），与 §4.2
  「豁免声明要有执行体」构成同族的时间对偶，两节互相指引。
- **教训 3** 已补进 [[prove-the-path-under-test]] §4.7（「不给可选参数」是独立的覆盖维度），
  属 §4「覆盖度先验证再补」家族。
- **教训 5** 已补进 [[prove-the-path-under-test]] §4.3 末尾（issue 正文不是已核实的事实），
  与该节「默认怀疑实现」是同一陷阱的两个入口。
- **教训 4** 已补进 [[cross-backend-semantic-fix-sweep]]「有定义 vs UB」节，作为该套判据的
  第三格：**参照实现自相矛盾**（同族两个函数给相反答案）→ 按语言规范选，不照抄任一侧，并把
  偏离记进注释。与上一轮的「参数求值顺序未指定」同格。
- **教训 1** 是 process-level 首次样本，暂留反思。若再出现一次「上一轮填的 issue，这一轮
  证明当时不该硬凑」，可与它一起升成一条独立纪律。

## 触发场景

- **一个修法只在「恰好如此」的情况下成立时**：填 issue 并写下**哪个前提不成立**，别硬凑一个
  能过当前用例的偏移；那份说明是下一轮的起点（教训 1）。
- **修完一个 bug 准备收工时**：`grep` 为它加过的 skip / 豁免 / 已知边界，逐条撤或收窄，并
  确认相应用例现在零 skip 参与比对；撤剩下的那条要连名字一起改（教训 2）。
- **测格式化 / 转换类接口时**：把「不给可选参数」当独立维度扫，对每个可选参数问「省略时
  参照实现取什么」（教训 3）。
- **发现参照实现在同类操作上自相矛盾时**：去查语言规范，按规范选，把偏离记进注释（教训 4）。
- **修 issue 时**：对它陈述的每一条参照行为亲自跑一遍，哪怕那个 issue 是自己开的（教训 5）。

## 关联

[[prove-the-path-under-test]]（教训 2/3/5 的落点；§4.2 豁免执行体、§4.3 先取事实）·
[[cross-backend-semantic-fix-sweep]]（教训 4 的落点；「有定义 / UB / 未指定 / 参照实现
自相矛盾」四格判据）· [[2026-07-28-four-diff-divergence-issues]]（上一轮：#197 就是在那一轮
被主动填成 issue 的，「参数求值顺序未指定」也在那一轮）·
`internal/crescent/errors.go` · `internal/crescent/state.go`（`callInfo` word2 bit 51-54）·
`internal/crescent/meta.go`（`pendingHostFrames`）· `internal/stdlib/stringlib.go` ·
`internal/stdlib/pattern.go` · `internal/stdlib/mathx.go` · `internal/stdlib/tablelib.go` ·
`internal/stdlib/stdlib.go` · `fuzz_oracle_test.go`（撤掉的 skip）

## 收尾：五轮审计，全部集中在 #197 的帧计数

三个 issue，最终 13 个 commit。五轮独立审计共找出 **13 处缺陷，其中 11 处在 #197 的帧计数机制里**——
这个分布本身就是结论：`%g`、`gmatch`、`assert`、三个 math 函数、`io.write` 一次就对了，而"数清 host
帧"这件事错了六次。

按轮次：

| 轮 | 找到 | 性质 |
| --- | --- | --- |
| 1 | 5 | 元方法多算一级、尾调用既不继承也不消耗、4 位截断而非饱和、`os.date` 的 isdst 与缺失指令 |
| 2 | 7 | 计数器下溢成 255、for-in 迭代器多算、状态级抑制吞掉了处理器内真正的 pcall、叠加尾调用只消耗一级 |
| 3 | 3 | **计数器永久泄漏**、`%Z` 无条件改写、isdst 的 min(一月,七月) 启发式漏了常年 DST |
| 4 | 2 | `%Z` 被我上一轮过度修正、打包位文档漏了 `tailDepth` |
| 5 | 1 | 4 位饱和可达，而我写的"远低于 15"的理由是假的 |

### 为什么同一处错了六次

因为「这是一个 host 边界吗」这个问题**对每种调用边界的答案都不同**，而我一直在一个共享的地方回答它：

- `callLuaFromHost`（pcall、sort 比较器、gsub 替换）——PUC 真的有 C 帧，**要算**；
- `callLuaFromHostNamed`（TFORLOOP）——PUC 派发迭代器不插帧，**不算**；
- 元方法派发——同样不插帧，**不算**，但处理器**内部**的调用要算。

计数放在共享的被调者里，迭代器就多了一级；从被调者里拿掉，元方法内部的 pcall 就少了一级——因为
pcall 走的是包装器而不是被调者。这两次都是「同一段代码服务了语义不同的三条路径」。

### 泄漏为什么躲过了两轮审计和我自己的全部清扫

**每一个探针都只调用一次。** 泄漏对单次调用在构造上就是不可见的，而我第二轮之后那个 96 形式的
组合清扫，是 96 次单次调用。第三轮的审计把同一形式在一个 chunk 里重复三次，立刻就复现了。

### 一个探针测了零个东西

第五轮那条"饱和可达"，我第一次去验证时把 50 层 pcall 写成了字面量嵌套——它先撞上 parser 的
syntax-level 上限，于是我量到"没有分歧"并差点据此驳回。深度必须在**运行时**构造。

追加三条教训：

10. **同一段代码服务语义不同的多条路径时，"在这里判断"本身就是缺陷。** 判据：给一个共享的
    helper 加条件之前，先列出它的**所有**调用方并问「这个条件对每一个都成立吗」；如果不成立，
    条件属于调用方而不属于 helper。本轮把它拆成三个入口之后就不再反复了。
11. **状态类缺陷（泄漏、漂移、累积）对单次调用不可见。** 判据：验证任何跨调用保存的状态时，
    把「同一形式重复 N 次并检查第 N 次」作为**独立**维度，而不是把 N 个不同形式各跑一次。前者
    抓泄漏，后者抓不到。
12. **"这个上限远高于任何真实用法"这类理由必须能被构造出的反例检验。** 判据：写下一个宽度或阈值
    够用的理由时，去**构造**超过它的输入；构造不出来才算证明。我写的"reentry 上限远低于 15"从来
    没被构造检验过，而它是假的。另外要注意构造本身可能撞上别的限制（字面量嵌套撞 syntax level），
    那时测到的"没有分歧"是探针失效而不是结论。
