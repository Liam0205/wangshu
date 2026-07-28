---
name: 2026-07-28-four-diff-divergence-issues
description: >
  四个既有差分歧 issue（#192 / #193 / #194 / #196）的处理轮，分支
  `fix/192-194-196-diff-divergences`，8 个 commit。结果修了 **6 个根因**：四个 issue
  报的四处，加上扫描过程中发现的两处（`tonumber(x, 10)` 整条路由错、`strtoul` 负数回绕
  与溢出饱和），再加一处本分支自己的 45 秒 fuzz 冒烟发现的（`, got no value` 从句缺失）。
  最值得记的是四件事：① **一个 issue 报的往往是某个结构性原因的一个实例**——#192 报的是
  `tonumber("nan(0)")`，按枚举通道的做法查下去发现 base 10 被错误送进逐字符解析循环，
  一个原因造出六条分歧，其中五条没有任何 issue 记录；② **`strtoul` 那处有一句注释声称
  「已登记为 diff 豁免」，但没有任何代码实现那个豁免**——一个没有执行体的「已豁免」声明
  比一个已知 bug 更糟，因为它读起来像已经处理过了；③ **`string.char(0/0)` 的机制不是范围
  检查**，`luaL_checkint` 是 `(int)luaL_checkinteger`，double 先变 `lua_Integer` 再窄化成
  int，x86-64 的 `cvttsd2si` 把 NaN 映射成 `INT64_MIN`、低 32 位为 0 所以 PUC 接受——我
  第一次探测直接转 int，漏了中间那一步，得出「PUC 应该拒绝」的错误结论；④ **「对齐 PUC」
  要先分清有定义还是 UB**：`strtoul` 的无符号取反与饱和是有定义的 C，应该对齐；double→int
  的越界转换是 UB，两个官方 build 自己都不一致，正确做法是产品侧钉一个 arch、harness 侧
  跳过那个区间。另外本轮有三处测试期望写错，每一次都是代码对、测试错，这个方向比反过来
  更危险，因为它会诱导去「修」一个本来正确的实现。扫描规模：8640 种带符号 verb 写法、
  164 种 tonumber/char/insert/concat 组合、45 种负数×base 组合，全部零差异；60 秒 fuzz
  无 crash；23 条 seed 入库并**故意包含负例**。
metadata:
  type: reflection
  date: 2026-07-28
---

# 四个差分歧 issue 修出六个根因（2026-07-28，分支 `fix/192-194-196-diff-divergences`）

> 范围：处理 #192 / #193 / #194 / #196 四个既有的 P1-vs-PUC 差分歧 issue，8 个 commit。
> 改动落在 `internal/crescent/number.go`（strtod 前缀的 `nan(...)`）、
> `internal/stdlib/stdlib.go`（`tonumber` 的 base 路由与 `strtoul` 语义）、
> `internal/stdlib/stringlib.go`（`%d`/`%i` 的 C 语义渲染 + `string.char` 的两步转换）、
> `internal/stdlib/tablelib.go`（`table.insert` 边界、`table.concat` 错误文本、
> `, got no value` 从句）、`internal/oracle/prelude.go`（char 转换 UB 区间的 skip）。
> 回归测试四个文件 + 23 条 corpus 入 `testdata/fuzz/FuzzOracleDiff/`。

## 任务

四个 issue 各报一处 P1 与 PUC 5.1.5 的差分歧，看起来是四个独立的小口径修复。实际修完是
六个根因，其中两个在扫描过程中发现、没有任何 issue 记录，另有一个是本分支自己的 45 秒
fuzz 冒烟撞出来的（在 master 上确认是既有问题，nightly 早晚会自动开 issue）。

## 本轮做了什么（六个根因 + 一个 fuzz 发现）

1. **#192，C99 `nan(n-char-sequence)`**（`internal/crescent/number.go`）：strtod 前缀解析
   匹配到裸 `nan` 就返回，从不消费 C99 7.20.1.3 允许的可选 `nan(n-char-sequence)`，于是
   `tonumber("nan(0)")` 得 nil、PUC 得 nan。修法只在 `)` 真的存在时才消费整组；未闭合的
   `nan(` 保留裸词，让调用方照旧因尾随垃圾拒绝（与 strtod 一致）。`inf(...)` 在 C99 里
   没有这个形式，保持拒绝。
2. **base 10 整条路由错（无 issue，扫描发现）**（`internal/stdlib/stdlib.go`）：PUC 的
   `luaB_tonumber` 在校验范围之前、也在读 arg 1 之前就判 `base == 10`，把它路由到标准
   转换分支（与不给 base 完全同一条路）；只有非 10 的 base 才走 `strtoul` 的逐字符解析。
   wangshu 把 base 10 送进了逐字符循环，于是**凡是那个循环表达不了的写法全部返回 nil**：
   `tonumber("1.5",10)`、`("0x10",10)`、`("1e3",10)`、`("inf",10)`、`("nan",10)`、甚至
   `tonumber(1.5,10)`——一个结构性原因造出六条分歧，issue 里一条都没提。现在无 base 与
   base 10 共用一个 helper，不会再各自漂移。
3. **#196，`%d`/`%i` 精度 0 配值 0 丢符号**（`internal/stdlib/stringlib.go`）：这两个 verb
   委托给 Go 的 `fmt`，而 Go 与 C 在一个角落不一致——精度 0 配值 0 时，C 转换出零个数字
   （C99 7.19.6.1）但仍然输出 `+`/空格 flag 要求的符号（符号不属于被转换的数字），Go 把
   整个转换当空的、连符号一起丢。实测对照：`%+.0d` C 得 `"+"` / Go 得 `""`；`%+5.0d` C 得
   `"    +"` / Go 得 `"     "`。新增 `cSignedFormat`，只在这个角落手写，其余仍委托 fmt——
   这跟隔壁 `%u/%x/%o` 早就为同一类 Go-vs-C printf 分歧手写渲染（`cUnsignedFormat`）是
   一样的做法。
4. **#194，`table.insert` 边界与 `table.concat` 错误文本**（`internal/stdlib/tablelib.go`）：
   两处。`table.insert` 原本有 `pos < 1 || pos > n+1` 的检查并报 `position out of bounds`
   ——但那个错误属于 Lua **5.2+**，PUC **5.1 的 `tinsert` 根本没有任何边界检查**，于是每一个
   越界位置都分歧。5.1 的语义是 `e = #t+1`，`pos > e` 时把 e 抬到 pos（"grow the array if
   necessary"），把 `[pos, e-1]` 上移一格，再写 pos；`pos <= 0` 时循环从 e 递减到 pos+1、
   一次都不执行，所以只是写入。实测结果：`t={}` 配 pos 0 得 `t[0]=1, #t=0`；`t={"a","b"}`
   配 pos 0 得 `t[0]=z, t[1]=nil, t[2]=a, #t=0`；pos 99 得 `t[99]=z, #t=2`。另一处是
   `table.concat` 的错误文本：PUC 的 `addfield` 是
   `"invalid value (%s) at index %d in table for 'concat'"`、`%s` 是元素的
   `luaL_typename`，wangshu 把括号括错了范围而且完全没有类型名。
5. **#193，`string.char` 的两步转换**（`internal/stdlib/stringlib.go` +
   `internal/oracle/prelude.go`）：`string.char(0/0)` 报错、PUC 得 byte 0。机制值得记，
   因为它**不是**一个范围检查：`luaL_checkint` 是 `(int)luaL_checkinteger`，所以 double
   先变成 `lua_Integer`（`ptrdiff_t`，64 位）**再**窄化成 int；x86-64 的 `cvttsd2si` 把
   NaN 与一切超出 int64 范围的 double 映射成 `INT64_MIN`，其低 32 位是 0，于是 `c == 0`、
   `uchar(c) == c` 成立、PUC 接受。按既有先例处理：产品侧钉住 x86-64 结果（与
   `cUnsignedCast` 为 `%u/%x/%o` 做的一样），harness 侧把 UB 区间加进 skip（arm64 的
   `FCVTZS` 把 `+inf` 饱和到 `INT64_MAX`、低 32 位是 -1、`uchar` 检查失败而报错，两个官方
   build 互相不一致，所以比对这个区间没有意义）。in-range 的值照旧比对，包括 `2^53`
   （大但 int64 可表示，截断有定义，不跳过）。
6. **`strtoul` 的无符号取反与溢出饱和（无 issue，扫描发现）**（`internal/stdlib/stdlib.go`）：
   这个函数里原本有一句注释说负数回绕「已登记为 diff 豁免」——**但没有任何代码实现那个
   豁免**，所以分歧是活的，nightly 随时可能把它开成 crasher。PUC 直接调 C `strtoul`，
   继承两个行为：负号在**无符号**算术里取反（`tonumber("-7",8)` 得 2^64-7 而不是 -7；
   `("-ff",16)` 得 2^64-255），溢出**饱和**到 `ULONG_MAX`（20 个 `f` 配 base 16 得
   2^64-1，而 wangshu 之前一直累加得 1.2e24）。与别处那些 double→int 的 UB 不同，这是
   有定义的 C，所以应该**对齐而不是跳过**。两处都改成在 uint64 里算、最后只转一次
   float64——顺序要紧，我错了两次：2^64-7 不是 float64 可表示的，第一次我对已经舍入过的
   float 取反（得 9.2e18 而不是 1），第二次用 float 取模塌成 0。
7. **`, got no value` 从句缺失（无 issue，本分支自己的 45 秒 fuzz 冒烟发现）**
   （`internal/stdlib/tablelib.go`）：PUC 的 `luaL_typerror` 是 `"%s expected, got %s"`、
   `lua_typename` 把 `LUA_TNONE`（压根没传的参数）映射成字面量 `"no value"`，wangshu 整个
   `, got X` 从句都没有。`table.insert()` 不带参数就能触发。已在 master 上确认是既有问题。
   显式 `nil` 与「没传」是两种情况（`"nil"` vs `"no value"`），PUC 区分，现在也区分。

## 扫描规模与 corpus

- 8640 种带符号 verb 写法（`d`/`i` × 12 种 flag 组合 × 6 种宽度 × 6 种精度 × 10 个值）；
- 164 种 `tonumber` / `char` / `insert` / `concat` 组合；
- 45 种负数 × base 组合，加溢出写法；
- 各自的边界探测。全部零差异，60 秒 fuzz 无 crash。
- 23 条 seed 覆盖六个根因，**故意包含负例**（`nan(` 必须仍被拒、`char(-1)` 必须仍报错、
  `inf(0)` 仍拒），因为只钉住「修好的方向」的 seed 分不出「正确的修复」和「什么都接受的
  修复」。

## 期望与实际

- 期望：四个 issue 是四处独立的小口径修复，各改一处判断。
- 实际：#192 挖下去发现它只是 base 路由错误的一个实例（六条分歧里的一条），扫描又额外
  带出 `strtoul` 那一处活着的假豁免，冒烟 fuzz 再带出一处错误消息缺 `, got no value`。
  四个 issue 变成六个根因 + 一个 fuzz 发现，而多出来的三处都不是靠 issue 找到的。

## 教训

### 教训 1（一个 issue 报的往往是某个结构性原因的一个实例，不是那个原因本身）

#192 报的是 `tonumber("nan(0)")`。按 prove-the-path 的做法去枚举「哪些通道会到达这个
parser」时，发现 base 10 整条路由错了，造出六条分歧，其中五条没有任何 issue 记录。

**Why**：issue 是由某个输入偶然触发的，它的措辞描述的是那一个输入，不是产生它的那段
代码的接受面。修完报上来的输入之后，代码里那个结构性原因还在，只是暂时没有第二个输入
撞上它——而 fuzz 迟早会撞。

**How to apply**：修完一个报上来的输入之后，**枚举所有到达同一段代码的通道并逐个验证它
真的执行**，不要停在 reported case。承 [[prove-the-path-under-test]] 的「枚举全部站点／
通道」纪律——那篇原本是跨后端与跨 emit 通道，本条是同一原则在「参照实现的路由分支」维度
上的实例：PUC 的 `luaB_tonumber` 有两条分支，wangshu 只对了一条。

### 教训 2（「已登记为豁免」这类声明必须能指向执行它的代码）

`strtoul` 那处的注释写着负数回绕「已登记为 diff 豁免」，但仓库里没有任何代码实现那个
豁免，分歧是活的。

**Why**：一个没有任何代码执行的「已豁免」声明比一个已知 bug 更糟，因为它读起来像已经
处理过了——读到它的人（包括写下它的人自己）会跳过这一处不再核对，而 fuzz 不会跳过。

**How to apply**：写下「这是已知 / 已豁免 / 已接受」时，同时指出**哪一行代码或哪一个
测试执行了它**；指不出来就说明它只是一句安慰。反向动作：审计既有的这类注释时，第一步是
去找它声称的执行体。

### 教训 3（对参照实现的行为要照它实际的代码路径推，不要照你以为的语义推）

`string.char` 我第一次探测时直接把 double 转成 int，得出「PUC 应该拒绝」的错误结论，
漏了 `luaL_checkint` 是 `(int)luaL_checkinteger` 这一步——double 先变 `lua_Integer`
（64 位）再窄化成 int，而 `INT64_MIN` 的低 32 位恰好是 0，所以 PUC 接受。

**Why**：C 的多步转换链里每一步都可能改变结果，跳过中间一步得到的结论可以与真值完全
相反（这里是「拒绝」对「接受 byte 0」）。凭「这个参数应该是个 int，所以按 int 想」推理，
推的是手册语义，不是那段 C 实际做的事。

**How to apply**：分歧涉及 C 语义时，把参照实现那条链上的**每一次类型转换**都写出来
再判断。这是 [[cross-backend-semantic-fix-sweep]]「PUC 语义由 C 实现定义，不由手册定义」
的一个更细的刻度：读到源码还不够，源码里的隐式转换也要展开。

### 教训 4（「对齐 PUC」不总是对的，要先分清那个行为是有定义的还是 UB）

本轮两类都碰到了，处理方式相反：`strtoul` 的无符号取反与溢出饱和是**有定义的 C**，应该
对齐；`string.char` 的 double→int 越界转换是 **UB**，两个官方 build 自己都不一致
（x86-64 `cvttsd2si` → `INT64_MIN`，arm64 `FCVTZS` 饱和到 `INT64_MAX`），正确做法是
产品侧钉一个 arch、harness 侧跳过那个区间。

**Why**：「与 PUC byte-equal」这个目标默认假设 PUC 有唯一确定的行为。落进 UB 时这个假设
不成立——此时「对齐 PUC」这句话本身没有指称对象，硬对齐等于把某台机器的偶然结果写成
规范；而落在有定义区时跳过比对是白白丢掉覆盖面。两类必须分开判。

**How to apply**：查那个操作在 C 标准里是有定义、未指定还是未定义，再决定对齐还是跳过。
有定义 → 对齐；UB 且跨 arch 不一致 → 产品侧钉参照平台 + harness 侧跳过该区间，并且只跳
UB 区间（本轮 `2^53` 大但 int64 可表示，截断有定义，照旧比对）。

### 教训 5（测试期望写错的方向值得单独记）

本轮有三处期望写错：`% 5.0d` 的宽度（我以为像 NaN 那样按宽度减一补齐，实际是完整 5 个
字符，而空格符号与 padding 无法区分）、`insert(t,0,...)` 之后 `t[1]` 的值（我以为 pos 0
不动数组，实际那次上移把 `"a"` 从 1 搬到 2）、`strtoul` 取反的顺序（先取反后转 float 与
先转 float 后取反结果不同）。每一次都是**代码对、测试错**。

**Why**：这个方向比反过来更危险。期望与实现不符时的默认反应是怀疑实现，于是会去「修」
一个本来正确的实现——修完测试绿了，但产品行为被改坏了，而且这次改坏有一条绿色测试替它
背书。

**How to apply**：期望与实现不符时，先去参照实现（或直接写 C / 跑 oracle 探测）取事实，
不要从推理里取。承 [[2026-07-23-oracle-arg-coercion-round]] 教训 3（差分类回归测试的期望值
以 oracle 实测为准）——那轮讲的是**写**期望时别凭直觉，本条讲的是期望与实现**冲突**时
该先怀疑哪一边。

## Promotion 决策

- **教训 1** 已补进 [[prove-the-path-under-test]] §4.1（「一个 reported case 是接受面的
  一个采样」），与该 guide 的枚举纪律同域。
- **教训 2** 已补进 [[prove-the-path-under-test]] §4.2（豁免声明要能指向执行体），
  与 §9 家族的「绿灯 / skip 不携带判据信息」同一物理基础：一句无执行体的豁免声明和一个
  单侧 skip 一样，什么都不说。
- **教训 3** 已补进 [[cross-backend-semantic-fix-sweep]]「PUC 语义由 C 实现定义」节
  作为更细刻度（展开每一次隐式转换）。
- **教训 4** 已补进 [[cross-backend-semantic-fix-sweep]] 新节「有定义 vs UB：对齐还是
  跳过」，与 #158 的 `cUnsignedCast` 一起构成两个实证。
- **教训 5** 已补进 [[prove-the-path-under-test]] §4.3（期望与实现冲突时先取事实），
  与 [[2026-07-23-oracle-arg-coercion-round]] 教训 3 合成同族第二实例。

## 触发场景

- **拿到一个差分歧 issue、修完它报的那个输入之后**：枚举所有到达同一段代码的通道，
  逐个验证它真的执行；参照实现有多条路由分支时逐条对（教训 1）。
- **读到 / 写下「已知差异」「已登记豁免」「已接受」这类注释时**：立刻找它的执行体
  （代码行或测试），指不出来就当成活着的 bug 处理（教训 2）。
- **调查涉及 C 类型转换的分歧时**：把参照实现那条链上每一次转换（含 `luaL_checkint`
  这种宏背后的隐式两步）都写出来再判断，别按「这个参数应该是什么类型」推（教训 3）。
- **决定一处 C 行为要对齐还是跳过时**：先查它在 C 标准里是有定义、未指定还是未定义；
  只跳 UB 区间，有定义的区间对齐（教训 4）。
- **回归测试期望与实现冲突时**：先跑 oracle / 写 C 探测取事实，再决定改哪一边；默认
  怀疑实现会诱导「修」一个正确的实现（教训 5）。

## 关联

[[prove-the-path-under-test]]（教训 1／2／5 的落点；枚举通道纪律的来源）·
[[cross-backend-semantic-fix-sweep]]（教训 3／4 的落点；「PUC 语义由 C 实现定义」与
「有定义 vs UB」）· [[2026-07-23-oracle-arg-coercion-round]]（同一函数族的上一轮：
`toNumberStr` 走 `crescent.ParseLuaNumber`；教训 5 承其教训 3）·
[[2026-07-18-issue155-158-nightly-crasher-round]]（`cUnsignedCast` 先例：UB 区产品侧钉
参照平台 + harness 侧跳过，本轮 `string.char` 照此办理）·
[[2026-07-26-oracle-nan-render-redesign]]（两类平台差异的判据；本轮的 UB 区间跳过属于
「两侧本来就是不同的东西」那一类）· `internal/crescent/number.go` ·
`internal/stdlib/stdlib.go` · `internal/stdlib/stringlib.go` ·
`internal/stdlib/tablelib.go` · `internal/oracle/prelude.go` ·
`testdata/fuzz/FuzzOracleDiff/`（23 条 seed）

## 后续：两轮独立审计与一次主动清扫

四个 issue 之外，独立审计与我自己的清扫又找出 **11 个根因**，全部在本轮刚改过的代码里。

**第一轮审计**（三个，都是我引入的）：`table.insert` 的 pos 没有做 `luaL_checkint` 的
int32 窄化（大有限位置写到未窄化的键上；非有限位置让移位循环跑向 INT64_MIN）；strtoul 的
溢出饱和被放在无符号取反**之前**，于是每个溢出的负数都得 1；以及移位跨度无界——`pos = 2^31`
窄化成 `INT32_MIN`，官方 build 实测跑 **2m21s** 做约 21 亿次 rawget/rawset。第三条不是语义
分歧（两侧算出同一张表），而是它跑在 builtin 里、VM 的 step budget 到不了，嵌入式宿主会被
一行脚本挂住，所以按 12 §4.9 加上限并抬错误，harness 侧同步跳过。

审计给出的**原因诊断比缺陷本身更有用**：我的每一张测试表都只扫了一个轴，而缺陷住在两个轴的
交叉处——insert 表停在位置 99（正好在被删掉的边界检查覆盖的范围之内），strtoul 表把取反与
溢出放在不同的行、从来没有同时出现。

**第二轮审计**（四个）：那个移位上限量错了东西——它限制 `e2-pos`（元素个数），于是往一张大表
的位置 1 插入被拒绝，而两侧引擎都能在不到一秒内完成这个移位；真正的风险是位置远低于数组，
5.1 会在什么都没有的键上迭代 `|pos|` 次。harness 侧的 guard 抄了同一条错规则，所以它的 skip
**掩盖**了产品侧的回归而不是暴露它。guard 的 int32 窄化还被限制在 ±2^53 窗口内，窗口之外既不
窄化也不跳过，等于把原缺陷原样搬到高一个八度：`2^54+2^31` 仍让 oracle 磨、wangshu 立刻返回。
另外 `tonumber` 的 base 也走 `luaL_checkint`，以及 `, got X` 从句还没覆盖 `next`/`pairs`/
`ipairs`/`setmetatable`。

**主动清扫**（四个）：连续两轮审计都在我没看的地方找到同样那两类问题，所以第三轮之前我自己把
整个面枚举了一遍——所有 `"X expected"` 消息、所有 `int(float)` 索引站点，逐个测哪些真的分歧。
137 个里有 19 个分歧。缺失的 int32 窄化还在 `table.remove` 的 pos、`table.concat` 的 i/j、
`select` 的 index 上；`table.setn` 无条件抬 obsolete 错误而 PUC 先做 `luaL_checktype`；
`assert()` 无参数时 PUC 的 `luaL_checkany` 使它成为参数错误。`table.concat` 的内存上限还要两处
更正：它比较的是未窄化的浮点值，而且**抢在** PUC 对第一个元素报的错之前——PUC 停在第一个非字符
串、根本不会走那个循环，所以那里没有需要预防的开销。

清扫过程中我自己弄坏了一处：`select` 的窄化改动误落到 `string.rep` 的 count 上，把巨大的
count 缩小、于是它的 OOM 上限失效，被既有的 `TestHardening_StringRepOverflow` 立刻抓住。
**一个窄化在一处是修复、在另一处就是拆掉护栏**，同名的量不代表同一个语义。

追加两条教训：

6. **连续两轮审计在不同地方找到同一类缺陷，说明该自己把这一类枚举完，而不是继续等下一轮。**
   判据：同一类问题第二次在新位置出现时，停下来列出这个类在整个代码里的所有实例并逐个测，而不是
   只修被点到的那个。本轮 19/137 里只有 7 个是审计报的。
7. **harness 侧的 skip 与产品侧的规则必须逐字对应，否则 skip 会掩盖产品的错。** 那条移位上限
   在两边都写错成"按元素个数"，于是 skip 稳定地遮住了产品拒绝合法插入这件事。判据：为一条产品
   规则加 skip 时，两处必须由同一个表述推出，并且要有一个用例验证**产品规则不成立时 skip 不
   生效**。
