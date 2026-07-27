---
name: 2026-07-26-oracle-nan-render-redesign
description: >
  issue #184 / #185 的最终方向（分支 `proto/oracle-nan-render`，5 个 commit
  `07e2808..7dd1cb1`）：把「wangshu 与 PUC 的 NaN 符号差异」从「在 harness 侧识别并
  豁免」改成「在 oracle 的渲染处消除」。同一个问题此前已经在
  `fix/184-185-oracle-diff-crashers` 上走了 31 轮独立盲审、43+ 个 commit，其中 13 轮
  抓到的缺陷是我修上一轮时引入的，同一处吸收逻辑改了六个版本。转向的触发不是新的技术
  发现，是用户在第 31 轮之后问的一个问题：「这个事情已经跑了很久很久了对吧？好像一直有
  edge case 冒出来？这是不是暗示着，这么做可能是不对的？甚至上一个 PR 也是不对的？」
  回答它的过程中我去查了 oracle 的构成，查出一件 31 轮里从未查证过的事实：那份 PUC
  5.1.5 源码是我们自己 vendor 进仓、自己用 cgo 编译的（`internal/oracle/_lua515/`，
  `lua515.c` 是单编译单元），也就是说 **oracle 侧的 NaN 文本一直是我们能控制的**。
  我此前实测确认过「不能改产品侧对齐 PUC」（`value.NumberValue` 把所有 NaN 规范化为
  单一 `canonNaN`，符号位在值层就丢了；x86 上每个 NaN 运算都产出与 `TagNil` 逐位相同
  的位模式），这个结论是对的，但我把它错误地推广成了「两侧拼写必然不同，只能在 harness
  侧容忍」，从未想到第三个选项。新方案在 `internal/oracle/lua515.c` 里覆盖
  `lua_number2str`、并对 `lstrlib.c` 的 include 局部 shadow `sprintf`，把 NaN 渲染的
  符号去掉（保持字段宽度：buffer 分不清前导空格是 padding 还是空格 flag 的符号，所以把
  format spec 传进去按声明宽度决定；符号去掉后 glibc 的 `+` 与空格 flag 开始作用于 NaN，
  所以剥任何符号字符）；vendored 源码保持与记录 sha256 逐字节一致。同时清掉产品侧两处
  只为让 oracle 一致而存在的 glibc 模仿（大写 verb 硬编码 `-NAN` 导致 `%e` 与 `%E`
  自相矛盾；小写 NaN 按「声明宽度减一」补齐导致 `%5f` 与 `%5E` 不一致），而 arm64 的
  glibc 与 x86 还不同，这个模仿本来就不可移植。删掉整个 span 机制（`NaNSpan`、
  `DecodeOutput`、多行 readout header、`knownNaNSignDifference` 及四个辅助、
  `OutputKnownNaNSign`、prelude 的 span recorder 与 provenance FIFO）与整个
  `preludeNaNCoercion` 层（含 straddle 规则、八种拼写表、所有 cap 与 trigger），
  `CompareOutput` 回到「归一化地址后逐字节比较」，没有任何接受差异的路径。净效果：
  相对 master 约 −550 行（31 文件 325+/841−），两个 crasher 与整个「泄漏成数字」的家族
  （`string.len(0/0)` 是 4 对 3、`string.len(0/0)*100` 是 400 对 300、`#(""..(0/0))`、
  `(""..(0/0))=="nan"`）现在都是普通的 equal 而不是 skip，覆盖面是**增加**的。
  五条教训里最重的一条：**「反复出现 edge case」是选址错误的信号，不是实现不完善的
  信号** —— 差异应该在产生它的地方消除，而不是在下游识别，因为下游识别必须依赖判据，
  而判据的输入迟早会被污染。
metadata:
  type: reflection
  date: 2026-07-26
---

# issue #184 / #185：NaN 符号差异改在 oracle 渲染处消除（2026-07-26）

> 范围：分支 `proto/oracle-nan-render`，5 个 commit `07e2808..7dd1cb1`
> （`fix(oracle): normalize the oracle's own NaN text instead of exempting it` /
> `fix(stdlib): render NaN without a sign and pad it to the full width` /
> `refactor(oracle): drop the NaN span mechanism; comparison is exact again` /
> `test(oracle): land the two crashers plus the leaked-NaN family as corpus` /
> `docs: describe NaN normalization at the oracle, not exemption at the comparison`）。
>
> 被放弃的那条路记在
> [[2026-07-26-issue184-185-nan-coercion-noncomparable-round]]（分支
> `fix/184-185-oracle-diff-crashers`，共 31 轮独立盲审，该文档正文详录到第 26 轮）。
> 那份文档保留，它是那条路为何不可能收敛的完整证据；本文只讲最终方向与转向本身的教训。
>
> 改动文件：`internal/oracle/lua515.c`（新增 127 行）+ `internal/oracle/compare.go`
> + `internal/oracle/prelude.go` + `internal/oracle/oracle.go` +
> `internal/stdlib/stringlib.go` + 三处测试 + 16 个 corpus 文件 + 文档
> （`README.md`、`docs/design/engineering.md` §3.2、
> `docs/design/p1-interpreter/10-stdlib.md` §5.2.1、
> `docs/design/p1-interpreter/12-testing-difftest.md` §4.2）。

## 1. 问题是一个字节

`0/0` 转成文本时，glibc 的 printf 按符号位打 `-nan`，wangshu 打 `nan`。IEEE 754 不赋
NaN 符号位数值语义，两种输出都合规，所以这是**渲染写法的选择**，不是语义分歧。

差的就是这一个字节。围绕它写出来的机制最后有 800 多行。

## 2. 旧路走了多远

分支 `fix/184-185-oracle-diff-crashers`：43+ 个 commit，**31 轮独立盲审**（每轮都是新的
隔离快照 clone、`bwrap --unshare-net` 断网实测验证、新的无继承上下文 reviewer），
其中 **13 轮抓到的缺陷是我修上一轮时引入的**。缺陷集中在两处抽象上：

- span recorder 吸收符号列与 padding 的逻辑，改了**六个版本**，每一版都修好上一版
  暴露的那一个问题、又引入一个新的（逐字节 walk 太慢 → 加 offset 上限 → offset 本身
  两侧差 1 → 换基准点 → 还是差 1 → 一次 pattern match → 量词重试是平方、撞 matcher
  预算 → 换锚定位置 → 还是平方）。
- 「限制扫描规模」失败了**五次**，五次都是同一个错误：比较了一个两侧不相等的长度
  （输出长度 → 输入长度 → 分桶长度 → 放宽容差的长度 → 带内容例外的输入长度）。

同类缺陷的载体一路在换：needle 写成本引擎拼写 → 对称标记作用于不对称数据 → provenance
按字符串值索引 → 剥符号的 key 只剥第 1 字节 → `#t` 作预扫边界 → 判据自身的运行成本
两侧不同 → 分配量 → matcher 步数预算。每一次修完我都自认已经想清楚了。

**每一轮我都在把机制做得更精细，没有一轮去问这个机制该不该存在。**

## 3. 转向的触发：用户的一个问题

第 31 轮之后，用户问：

> 这个事情已经跑了很久很久了对吧？好像一直有 edge case 冒出来？这是不是暗示着，这么做
> 可能是不对的？甚至上一个 PR 也是不对的？我们到底都在处理什么问题？

这个问题没有提供任何新的技术信息。它促使我做的事情是：去查 oracle 到底是什么。

## 4. 被漏掉的事实：oracle 是我们自己编译的

查出来的是：那份 PUC 5.1.5 源码在 `internal/oracle/_lua515/`，是仓库自己 vendor 进来的；
`internal/oracle/lua515.c` 是一个单编译单元（官方 `etc/all.c` 手法），把它整个 include
进来经 cgo 编译进测试二进制。它已经为了确定性 stub 掉了 `os.time`、`math.random`、
`pairs` 的迭代顺序。

也就是说：**oracle 侧的 NaN 文本一直在我们的控制之内。** 这一整轮它都是。

我此前实测确认过两件事，两件都是对的：

- 改 `formatLuaNumber` 无效 —— `internal/value/value.go` 的 `NumberValue` 把所有 NaN
  规范化成单一 `canonNaN`，符号位在值层就丢了；
- 改 NaN-boxing 保留符号位不可行 —— x86 上每个 NaN 运算都产出
  `0xFFF8_0000_0000_0000`，与 `TagNil` 逐位相同；负 signaling NaN 要在 P1/P3/P4 四条
  代码生成路径上给每个浮点结果做重映射，漏一处那个 NaN 就被读成 nil。

错的是我从这两件事推出的结论。我推的是「两侧拼写必然不同，所以只能在 harness 侧容忍」。
正确的推论只有「**wangshu 侧改不动**」——它没有排除「改 oracle 侧」。这个第三选项在
31 轮里一次都没被列出来过。

## 5. 新方案

两条渲染路径都要处理，都经 `lua_number2str`（`luaconf.h` 定义，本单编译单元可以在
include vendored 源码**之前**覆盖它）：

- `tostring` / `..` / `io.write` 一个数字（`lvm.c` 的 `luaO_tostring`）；
- 其他地方的 `LUA_NUMBER_FMT`。

`string.format` 的 `%e`/`%f`/`%g` 族直接调 `sprintf`、不走 `lua_number2str`，所以对
`lstrlib.c` 的 include **局部** shadow `sprintf`（`wangshu_sprintf` 转发给 `vsprintf`
再剥符号），include 之后立刻 `#undef`：其他 vendored 文件的 `sprintf` 调用不受影响。

vendored 源码保持与 `_lua515/README` 记录的 sha256 逐字节一致 —— 配置只在 `lua515.c`
与 CFLAGS 里做，这是仓库既有纪律。

三个实现要点，都是实测逼出来的：

1. **保持字段宽度**。`sprintf` 已经补过 padding，直接删符号会让字段短一位。而 buffer
   本身分不清前导空格是 padding（`%5E`）还是空格 flag 的符号（`% E`）—— 两者产出的
   字节一样。所以把 format spec 传进去（`wangshu_fixnan_spec`），按**声明宽度**重新
   补齐；两种读法于是落在同一个答案上，因为决定补多少的是宽度、不是 buffer。
2. **剥任何符号字符，不只是 `-`**。符号去掉之后，glibc 的 `+` 与空格 flag 开始作用于
   NaN，产生 `+NAN` / ` NAN`。
3. **Inf 保留符号**（那个符号有数值意义），脚本自己写的 `"-nan"` 字面量不动
   （函数只在缓冲区里认出一个 NaN 渲染时才动手，且要求它前面只有 padding 或一个符号）。

真值表见 `internal/oracle/oracle_test.go` 的 `TestExec_NaNRenderedWithoutSign`。

## 6. 顺带清掉产品侧的两处 glibc 模仿

`internal/stdlib/stringlib.go` 的 `cFormatSpecialFloat` 里有两处只为让 oracle 一致而
存在的模仿，两处都让 wangshu **自身**不一致：

- 大写 verb 硬编码 `-NAN` —— `%e` 得 `nan`、`%E` 得 `-NAN`，自相矛盾；
- 小写 NaN 按「声明宽度减一」补齐，复现 glibc 保留但不显示的那一列符号 —— `%5f` 补到
  4 列、`%5E` 补到 5 列，补齐方式不一致。

而 arm64 的 glibc 与 x86 还不同，这个模仿本来就不可移植。现在 NaN 在所有 verb 下都不带
符号、都按完整声明宽度补齐（`%5f` → `"  nan"`，`%5E` → `"  NAN"`），Inf 的规则不变。

**这两处是在追一个测试的期望值，而不是在实现一个语义。** 差异一旦在 oracle 侧消除，
它们连存在的理由都没了。

## 7. 删掉的东西

- span 机制整套：`NaNSpan`、`DecodeOutput`、多行 readout header、
  `knownNaNSignDifference` 及四个辅助、`OutputKnownNaNSign` 这个 verdict、prelude 里的
  span recorder 与 provenance FIFO；
- `preludeNaNCoercion` 整层：`tostring` / `string.len` / `sub` / `byte` / `upper` /
  `lower` / `reverse` / `rep` / `find` / `match` / `gmatch` / `gsub` / `table.concat` /
  `%s` / `%q` 的入口拦截，straddle 规则，八种拼写表，所有 cap 与 trigger。

`CompareOutput` 于是回到「归一化地址后逐字节比较」，**没有任何接受差异的路径**；测试
明确断言 NaN 符号差异**不被豁免**。

保留下来的两类豁免性质不同，判据也不一样：

- **地址归一**（`table: 0x…` → `0xADDR`）：两个引擎的堆布局本来不同，没有「正确值」
  可对齐，只能在比较时归一；
- **实现常数类护栏**（`stack overflow`、`too many syntax levels`、200 local variables
  等）：两侧独立选定的实现限制，触发点差几个输入；改 oracle 的常数去凑 wangshu 会让它
  不再是独立基准，只能跳过。

## 8. 净效果

- 相对 master **约 −550 行**（31 文件，325 插入 / 841 删除；`internal/oracle` 一处是
  230/779）。
- 两个 crasher（`print(string.format("NA%E",-(0%0)))`、`print(tostring(0%0))`）与整个
  「泄漏成数字」的家族现在都是**普通的 equal**，不是 skip：`string.len(0/0)` 4 对 3、
  `string.len(0/0)*100` 400 对 300、`#(""..(0/0))`、`(""..(0/0))=="nan"`。这些输入在
  旧机制下会连同**同一次运行里的真差异**一起被丢掉，所以覆盖面是**增加**的。
- 16 个 corpus 文件入 `testdata/fuzz/FuzzOracleDiff/`（含 Inf 的对照与脚本自出
  `print("-nan","-NAN")` 的负例）。
- 60 秒 `FuzzOracleDiff` 无 crash，全套测试绿。

## 教训

### 教训 1（头条）：「反复出现 edge case」是选址错误的信号，不是实现不完善的信号

31 轮盲审、13 轮抓到的缺陷是我修上一轮时引入的、同一处代码六个版本 —— 这些都不是
「再仔细一点」能解决的量。我当时把每一个新缺陷都读成「这里还有一种情况没想到」，于是
每一轮的动作都是把机制做得更精细。

判据：**如果一个机制的缺陷密度长期不下降，去问「这个机制装在了正确的位置吗」，而不是
继续修它。** 缺陷密度不降本身就是信号，不需要等到能说清哪里错了。

具体到差分类问题，这条有一个可直接用的形式：**差异应该在产生它的地方消除，而不是在
下游识别。** 下游识别必须依赖判据，而判据的输入迟早会被污染 —— 本轮的证明是可构造的：
那个符号字节一旦进入字符串就是普通数据，`#`、`==`、`string.sub`、`..`、算术能把它搬到
任何地方，`string.len(0/0)*100` 的输出里连一个 nan 字节都不剩。没有可锚定的东西，任何
下游规则都必须读一个两侧不相等的量，所以都做不到对称。

这条与被放弃那轮的教训 1（「同一个机制上第二次出现同类缺陷就停止修补，去证明这个机制
在这个约束下能不能做对」）是同一件事的两个尺度：那一条管**一个机制内部**该不该继续修，
这一条管**整个方向**该不该继续走。那一条我在第 5 轮之后是真的用上了，用了之后也确实
一步就收敛了那一处 —— 但它的作用域是「这个机制」，所以用它只会让我把机制修得更好，
不会让我问「这个机制装在哪一层」。**作用域比判据本身更要紧。**

### 教训 2：一个否证结论的适用范围必须显式界定，否则会被悄悄外推

我实测证明了「改 wangshu 的值层不可行」，两次实测都做对了，结论也对。但我把它外推成了
「只能在 harness 侧容忍」，跳过了「改 oracle 侧」这个从未被考虑的选项。

外推是静默发生的：我从来没有写下过「所以只剩 harness 一条路」这句话，也从来没有列过
候选清单。它以「这件事已经查过了」的形式留在上下文里，后面 31 轮都建立在它上面。

判据：**写下一个否证结论时，同时写下它没有排除什么。** 形式是两行：① 被否证的是哪个
具体选项（「改 wangshu 的值层」，不是「改产品侧」，更不是「在源头修」）；② 与它并列的
其他选项各是什么、当前状态是什么（未查证 / 已查证不可行 / 未想到 —— 「未想到」这一格
写不出来，正是要靠列清单逼出来的）。

被否证的选项越是查得扎实，它越容易被当成整个方向的定论。**「我实测过了」保护的是那一个
选项的结论，不保护由它推出的范围。**

### 教训 3：先查清被测系统的构成，再设计对策

oracle 是我们自己 vendor、自己用 cgo 编译的 —— 这件事 31 轮里从未查证。它是一条能在
第一轮就问出来的事实：**「这个基准是谁提供的，我们能改吗」**。一旦查清，整个问题的形状
就变了：从「两个不受控的实现之间的差异」变成「一个受控实现的渲染选择」。

我当时对 oracle 的心智模型是「外部真值」。这个模型在**语义**上是对的（它就是我们要对齐
的那个真值），但在**物理**上是错的（它是我们编译的一个测试目标）。心智模型错在物理层，
于是把一整类方案排除在视野之外，而这一步排除没有留下任何痕迹。

判据：处理与某个参照实现的差异之前，先把这个参照的**供给链**查清：源码在哪、谁编译的、
编译时有没有已经打过的补丁、改它会不会破坏它作为基准的意义。本轮最后一问的答案是「不会」
—— oracle 已经为确定性 stub 掉了 `os.time`、`math.random`、`pairs` 顺序，去掉一个不携带
数值意义的符号字符是同一类交易。**这个先例本身就在源码里，31 轮都没去看。**

### 教训 4：用户的「这是不是方向错了」值得当作技术信号处理

那个问题没有提供任何新的技术信息，也没有指向具体的代码。但它促使我去查一个我一直假设为
不可变的前提，而那一步查证直接换掉了整个方案。

判据：**当一件事的进展长期与投入不成正比时，重新检查前提比继续推进更有价值。** 用户
（或任何外部视角）提出「这个方向是不是不对」时，正确的第一动作不是解释当前方案为什么
合理、也不是罗列已经解决了多少问题，而是**把方案依赖的前提逐条列出来，标出哪些是查证过
的、哪些是假设的**，然后去查那些假设。

本轮我一开始的反应是想解释「机制现在已经收敛了」。真正有用的动作是去读
`internal/oracle/` 的构成。**「已经投入了 31 轮」是继续走下去的最差理由，也是最容易被
当成理由的那一个。**

### 教训 5：区分两类平台差异（可复用判据）

- **「同一个抽象值的不同书写方式」** → 在**渲染处**消除。本轮的 NaN 符号：同一个 NaN，
  两种合规拼写，IEEE 754 不赋那个位数值语义。
- **「两侧本来就是不同的东西」** → 在**比较时**归一或跳过。引用值地址（两个引擎的堆
  布局本来不同，没有「正确值」可对齐，归一）；两侧独立选定的实现上限（`stack overflow`、
  `too many syntax levels`、200 local variables，触发点差几个输入，跳过 —— 改 oracle
  的常数去凑会让它不再是独立基准）。

**前者如果放到比较侧处理，就会像本轮一样不收敛**，因为那个差异会流入普通数据：同一个
抽象值的不同书写会被 `#`、`==`、`..`、算术搬进长度、布尔和数字，输出里不再留下任何可
锚定的标记。后者不会 —— 地址永远带着 `table: 0x` 前缀，护栏永远以一条固定错误消息出现，
标记是结构性的、不会被算术冲掉。

用法：设计任何「接受这类差异」的机制之前，先判这一格。判在渲染侧就去改渲染（并确认改的
那一侧是可控的，见教训 3）；判在比较侧才谈判据设计，然后才轮到
[[prove-the-path-under-test]] §9 那套对称性纪律。**顺序反了的代价是本轮的 31 轮。**

## Promotion 候选

- **教训 1（头条）+ 教训 2 + 教训 5**：已写进
  [[prove-the-path-under-test]] 新增 §9.0（「先判差异该在哪一侧消除」，放在 §9 那套判据
  对称性纪律**之前**，因为它决定后面那套要不要用）。理由：§9 现有内容全部是「已经决定在
  比较侧处理之后」的纪律，缺的正是上一层的选址判断；而 §9 那 31 轮的代价本身就是这条
  缺失的证据 —— 那一轮从来没有过一个环节会问「这个差异该在哪一侧消除」。
- **教训 3（先查清被测系统的构成）**：候选进
  [[design-claims-vs-codebase-physics]]，作为「心智模型错在物理层而非语义层」的实例
  （对 oracle 的模型在语义上对、在物理上错，于是排除掉一整类方案且不留痕迹）。本轮先在
  §9.0 里以一句判据的形式带过（「确认要改的那一侧是可控的」），独立条目等下一个实例。
- **教训 4（用户的方向质疑当技术信号）**：process-level，首次样本，暂留在本文。它的
  可操作部分（「把方案依赖的前提逐条列出来，标出哪些查证过、哪些是假设的」）与教训 2 的
  判据是同一个动作的两个触发时机，下一实例出现时合并成一条。

## 触发场景

- **发现自己在为同一个机制反复补 edge case 时**（教训 1）：先数一下缺陷密度有没有在降。
  没降就停下来问这个机制装在了正确的位置吗，而不是继续想下一种情况。差分类问题直接问
  「这个差异能不能在产生它的地方消除」。
- **写下「我实测过了，这条路不行」时**（教训 2）：同时写下被否证的是哪个**具体**选项，
  以及与它并列的其他选项各是什么状态。别让「已经查过了」以整个方向定论的形式留在上下文
  里。
- **要处理与某个参照实现 / 外部真值的差异之前**（教训 3）：先查这个参照的供给链 ——
  源码在哪、谁编译的、编译时已经打过什么补丁、改它会不会破坏它作为基准的意义。
- **用户或外部视角问「这个方向是不是不对」时**（教训 4）：第一动作是列出方案依赖的前提
  并标出哪些是假设，然后去查那些假设；不是解释当前方案为什么合理，也不是罗列已经解决了
  多少问题。
- **设计任何「接受某类平台差异」的机制之前**（教训 5）：先判这个差异是「同一个抽象值的
  不同书写方式」（→ 渲染处消除）还是「两侧本来就是不同的东西」（→ 比较时归一或跳过）。
  判在渲染侧就不要设计判据。

## 关联

- [[2026-07-26-issue184-185-nan-coercion-noncomparable-round]] —— 被放弃的那条路，31 轮
  独立盲审的完整记录。它是「为什么下游识别不可能收敛」的证据，也是本文教训 1 的全部
  样本量。
- [[2026-07-24-issue173-oracle-nan-known-diff-round]] —— 这个机制的起点（#173 把 NaN 符号
  差异定性为已知平台差异 + harness 侧窄口径跳过）。那一轮的教训 1「接受已知平台差异 =
  定性 + 证据链 + 窄口径归类三件套」现在要反过来读：先问差异能不能在产生处消除；教训
  2/3 关于分层与证据位的判断仍成立。
- [[2026-07-22-oracle-format-nan-inf-round]] —— #170/#171，`cFormatSpecialFloat` 的
  由来。那一轮「PUC 语义由 C 代码 + 宿主 libc 定义，以 oracle 实测字节为准」的纪律对
  **有数值语义**的部分仍然成立（Inf 的符号、verb 大小写、指数位数）；本轮撤掉的只是对
  **没有**数值语义那一位的模仿。
- [[prove-the-path-under-test]] §9 —— 差分 harness 判据的对称性纪律。本轮给它加了 §9.0
  作为前置判断：先判差异该在哪一侧消除，判在比较侧才用得上 §9.1 之后那套。
- `docs/design/engineering.md` §3.2、`docs/design/p1-interpreter/12-testing-difftest.md`
  §4.2、`docs/design/p1-interpreter/10-stdlib.md` §5.2.1 —— 现状口径。
