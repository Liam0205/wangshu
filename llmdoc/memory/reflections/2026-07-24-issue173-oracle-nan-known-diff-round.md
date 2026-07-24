---
name: 2026-07-24-issue173-oracle-nan-known-diff-round
description: >
  issue #173：#170/#171 修复（PR #172 `cFormatSpecialFloat` 硬编码 -NAN/nan/INF）
  只覆盖了 fuzz 当时撞到的负号路径，但 PUC 自身对 NaN 符号有正负两种可见路径
  （glibc 输出取决于 NaN 位模式 × verb 大小写），追那一个 CPU/libc 组合的具体
  显示需改 VM 值层 NaN-boxing 对齐 x86，收益低风险高。本轮把 #173 定性为 PUC/
  x86/libc 对 NaN 符号的已知平台差异（IEEE 754 不赋 NaN 符号数值语义），改为在
  oracle diff harness 里 span-anchored 窄口径识别并跳过：prelude 输出捕获层
  按每次 NaN 渲染事件（print/io.write 的数值 NaN + `string.format` 有 NaN 参数
  时输出中每个 nan/NAN token 及相邻的 `-`/`+` 符号列与两侧 ASCII 空格）
  记录 `__nan_spans` FIFO，`__oracle_readout()` 用多行 header 
  （`<count>\n<off-end>\n...body`）把 span 数组和输出 body 传给 Go 侧
  `Result.NaNSpans`；`internal/oracle.CompareOutput` 三态归类（Equal / 
  KnownNaNSign / Different）逐 span walk：span 外的字节两侧必须 byte-equal
  （脚本自出字面 `NAN`/`-NAN` 差异一定被抓到），span 内允许
  `knownNaNSignDifference` 判定的 sign spelling 差异（含 printf 宽度一列
  偿还、对称 script padding、`-`/`+`/多符号 signRun 互换、reserved-sign-column
  padding 差 1）。绝不在 `NormalizeOutput` 里全局替换 `nan`/`NAN` —— 会误吞
  脚本自己输出的普通字符串。已知 harness 限制记录在
  `TestExec_NaNSpansKnownLimit`：Lua 5.1 无法区分 `string.format` 返回值与
  同值 script literal 的 identity，脚本先 emit 该 literal 会误消费
  provenance FIFO 首项，狭窄 collision 是有意识的 trade-off。保留正负例
  回归测试与 5 个 corpus seed（其中 3 个是 PR CI 上 fuzz 发现的边界形态：
  `%+E0` 符号列冲突、`%10f` reserved-sign-column、`+%E00000` 相邻 signRun）。
  过程中先撞到一个 WIP commit（全局替换）直接违反 #173 明文约束，revert 回
  精细方案后重新走完；PR review 迭代两轮后 span-anchored 方案落地。
metadata:
  type: reflection
  date: 2026-07-24
---

# issue #173：把 NaN 符号 spelling 定性为已知平台差异并机制化跳过（2026-07-24）

> 范围：分支 `fix/173-oracle-nan-known-diff`，3 个精细 commit（2daf5c0
> `fix(oracle): skip known NaN sign differences` + 934d818 `fix(oracle): cover
> adjacent NaN format text` + 8b9b324 `fix(oracle): require NaN output evidence`）；
> 中途一个 WIP commit（80f7b09）以 `NormalizeOutput` 里 `strings.ReplaceAll("-nan","nan")`
> 全局替换的形式回退了精细方案，与 #173 明文约束冲突，本轮把它 revert 掉回到
> 8b9b324；改动文件：`internal/oracle/{compare.go,oracle.go,prelude.go,compare_test.go,oracle_test.go}`
> + `fuzz_oracle_test.go` + `internal/stdlib/stringlib.go`（仅 godoc 更新，无
> 代码改动）。

## 任务

issue #173（承 #170/#171 → PR #172 上一轮）：PR #172 在 `cFormatSpecialFloat`
里把大写 NaN 固定渲染为 `-NAN`，只覆盖了 fuzz 当时撞到的负号路径，但本机 PUC
Lua 5.1.5 与 wangshu 的实际差异是**双向**的——

| 表达式 | PUC/x86 | 当前 wangshu |
|---|---:|---:|
| `string.format("%E", 0/0)` | `-NAN` | `-NAN` |
| `string.format("%E", -(0/0))` | `NAN` | `-NAN` |
| `string.format("%E", 1/0-1/0)` | `-NAN` | `-NAN` |
| `string.format("%E", tonumber("-nan"))` | `-NAN` | `-NAN` |
| `string.format("%E", tonumber("nan"))` | `NAN` | `-NAN` |

要真正吃掉后两行差异得把 wangshu 的算术 NaN 位模式与 x86/glibc 对齐——改
NaN-boxing 值表示 + P1/P3/P4 全套浮点路径，为一颗 CPU/一个 libc 的显示细节
掀值层地板，收益低风险高。用户拍板：**接受为已知平台差异**，机制化让
`nightly-diff-fuzz` 继续跑但不再报此类问题，同时确保其他输出差异仍正常失败。

## 本轮做了什么

### 1. 定性：IEEE 754 不赋 NaN 符号数值语义

放弃「让 wangshu 追某 CPU/libc 显示」的方向，`internal/stdlib/stringlib.go`
`cFormatSpecialFloat` godoc 从「fully sign-correct NaN rendering needs the VM
value layer aligned to x86 first — tracked in #173」改成「the remaining
NaN-sign spellings are host-platform differences with no Lua numeric meaning;
the oracle fuzz harness classifies and skips them under #173 instead of
changing the VM value representation to imitate one CPU/libc combination」——
把「有一天要修 VM 值表示」的软承诺改成「已知平台差异 + harness 侧机制化跳过」
的稳定契约。

### 2. Harness 侧机制化：`__nan_spans` 逐渲染证据

`internal/oracle/prelude.go` `preludeCapture` + `preludeGuards` 记录**每次
NaN 渲染事件对应的字节区间**（span），对称跑在 shim 与 wangshu 两侧：

- `print(...)` / `io.write(...)` 见到 `number` 且 `v ~= v` 时，用
  `__record_nan_span(off, len)` 把该 tostring(NaN) 输出的字节区间记进
  `__nan_spans`；
- `string.format(fmt, ...)`：任一 NaN 参数被消费，用 `string.find` 逐个定位
  输出里的 `nan`/`NAN` token，**贪婪包含相邻的 `-` 前缀和两侧 ASCII 空格**（
  这样 printf width padding 落在 span 内），把区间记下；无 NaN 参数的调用
  一格都不记，脚本自拼 `"NAN"` 走这条路的完全在 span 之外；
- `__oracle_readout()` 用 `"<count>\n" + count 行 "off-end\n" + body` 的
  header 把 span 数组和 output body 一起返回。

Go 侧 `oracle.DecodeOutput(readout) → (output, spans []NaNSpan, ok)` 严格
剥 header：数量非数字 / 偏移越界 / 非单调 / start > end 都判 `ok=false`，
`shim.Exec`（cgo 侧）与 `runWangshuSide`（fuzz_oracle_test.go）两条读出路径
都在 header 损坏时判 `VerdictLimit`（不可比较、跳过）——证据缺失永远不会
静默降级成「视作无 NaN」。

**为什么用 spans 而不是一个执行级 bool**：初版曾用 `__nan_output` bool +
`CompareOutput(_, _, oracleNaN, wangshuNaN)`；PR 上 Codex reviewer 指出这
把「同次执行里任意位置观察到 NaN 就永久置位」的证据放宽到「授权比较器
忽略同次输出中另一处普通文本里的 `NAN`/`-NAN` 差异」——`print(0/0, "BANANA")`
vs oracle 输出 `nan\tBANANA`、wangshu 输出 `-nan\tBA-NANA`（假想真值差异）
就会被误吞。span 化后证据锚定到**具体字节区间**：span 内允许 sign spelling
差异，span 外必须逐字节相等。

### 3. 三态比较：`CompareOutput`

`internal/oracle/compare.go` 新增 `OutputComparison` 三态（`OutputEqual` /
`OutputKnownNaNSign` / `OutputDifferent`），fuzz 用 switch 处理：

- 先跑 `NormalizeOutput`（仅把 `table/function/thread/userdata: 0x...` 归一
  为 `0xADDR`）；两端相等 → `OutputEqual`；
- 否则要求两端 span 数量一致才进入分段比较；数量不一致或任一侧无 span →
  `OutputDifferent`；
- 逐 span walk：**gap 段**（相邻 span 之间的 script-owned 字节）跑
  `NormalizeOutput` 后必须逐字节相等；**span 内**先看 raw 相等，否则走
  `knownNaNSignDifference` 精细比较（token 数、literal、case、`compatibleNaNPadding`
  接受 printf 宽度一列偿还或对称 script padding）；所有 span 走完再比较
  trailing 段。任一步失败 → `OutputDifferent`。

**注意**：`NormalizeOutput` **仍然** 只做地址归一化。#173 明文要求「不在
`NormalizeOutput` 中全局替换 `NAN` / `-NAN`，避免误吞脚本自己输出的普通字符
串」；本轮严格遵守，且 span 机制让「脚本自出 `NAN` 落在 span 外一定被
byte-compare 抓到」有 `TestCompareOutput/nan_+_plain-text_NAN_diff_outside_span`
等 3 个 reviewer 提出的负例回归钉住。

### 4. 正负例回归

`internal/oracle/compare_test.go` 25 组 `TestCompareOutput`：正例覆盖 lower /
upper / left-padded / right-padded / multiple tokens / 与 literal 相邻
（`NAN0` / `0NAN` / `format word adjacency covered by span`）/ symmetric
script padding（`io.write(" ", 0/0, " ")` 家族 4 例，本地审计 B1 补上）；负例覆盖「plain text 无证据」/
「single-sided evidence」/ 「case differs」/ 「alignment differs」/ 「width
differs」/ 「other byte differs」/ 「non-sign insertion」——保证字面量
`"NAN"` / `"-NAN"` 或普通字符串对齐的真实差异仍然失败。

`fuzz_oracle_test.go` seed 加两条 NaN 输出形态实证 harness 走通：
`print(string.format("value=[%10E]", -(0/0)))` + `print(string.format("%E0", -(0/0)), string.format("0%E", -(0/0)))`。

### 5. 验证

- `go build ./...`（默认 build，零 cgo）✓
- `go test -count=1 ./internal/oracle/...`（compare/decode 白盒）✓
- `go test -tags wangshu_oracle_cgo -count=1 ./internal/oracle/...`（cgo 侧
  Exec 单测含 header 剥离）✓
- `go test -tags wangshu_oracle_cgo -run '^$' -fuzz FuzzOracleDiff -fuzztime 30s
  -parallel=4 .`（30s smoke，`-parallel=4` 按共享机器规则）✓，跑到 23k+ execs
  零 fatal，known NaN sign 分歧只在带证据链的 seed 上走跳过路径。

## 教训

### 教训 1（头条）：接受已知平台差异 = 定性 + 证据链 + 三态归类，不是全局替换

一旦一类差异被判定为「不该修的平台差异」（IEEE 无语义 / 只能追一个 CPU-libc
组合 / 修 VM 值层 ROI 过低），把它机制化跳过的最小系统由三件套构成：

- **定性**：文档改口，把「暂时对齐」的软承诺换成「已知差异 + harness 跳过」
  的稳定契约（本轮 `cFormatSpecialFloat` godoc）。软承诺留着会不断反复
  争夺 VM 层的注意力，稳定契约允许下游放心不修；
- **证据链**：harness 侧加一个「差异来自被识别的语义路径」的开关，且开关
  由 print/write/format 等**具体渲染出口**置位——不是从**输入侧** 猜
  「这个脚本会不会算 NaN」；证据缺失就判 skip/limit，不静默走 known-diff
  路径；
- **窄口径归类**：只在两端都有证据 + 差异形态限于「单一 token sign
  spelling ± printf width 一列偿还」时归 known-diff；case / literal / alignment
  / 其他字节的差异一律回到「正常失败」。差异形态判据须能被负例回归钉住，
  绝不能是「全局 `-nan` → `nan` 替换」这种会误吞脚本字面量的粗暴规则。

家族继承：这是 [[cross-backend-semantic-fix-sweep]]「PUC 语义由 C 实现定义」
+「PUC 语义由 libc 定义」（承 [[2026-07-22-oracle-format-nan-inf-round]]）的
**退让侧**——上一轮那两处教训说「与 PUC 分歧先 grep `_lua515/` 源码 / 追到
libc 层实测」，本轮说「追到 IEEE 无语义或 x86 值层地板时，正解是文档化 +
harness 机制化跳过，而非继续追」。首次以「退让侧」形式出现，**暂留观察**，
若再现可作那 guide 一节的对偶补充。

### 教训 2：`NormalizeOutput` 是「无损归一」，跳过判定不进这一层

分支上出现过一个 WIP commit（80f7b09）把精细方案回退成
`strings.ReplaceAll(s, "-nan", "nan")` 全局替换 + 移除负例回归，被本轮 revert
掉。这种写法有三条独立的错：

- **违反 #173 明文**「不在 `NormalizeOutput` 中全局替换 `NAN` / `-NAN`，避免
  误吞脚本自己输出的普通字符串」；
- **对称性缺失**：只处理小写 `-nan`，漏了 wangshu `cFormatSpecialFloat` 硬编码
  的 `-NAN` 大写路径；
- **证据缺失**：不区分「NaN 参数走进了 format」与「脚本 print 字面量
  `"-nan"`」；后者被静默吞掉，就把 fuzz 从「能证明差异是 NaN 语义路径产生」
  降级成「什么字符串对上就跳」。

`NormalizeOutput` 只能做「两端语义等价的字节归一」（地址、engine-agnostic
的 tostring 前缀），跳过判定属于**上一层**（有证据 + 三态归类）；把跳过混
进 `NormalizeOutput` 会让所有下游比对都失去「什么被视作等价」的可解释性。
承 [[design-claims-vs-codebase-physics]] §2「视图归一层不应吞掉语义差异」的
邻接教训，首次样本暂留观察。

### 教训 3：证据的粒度必须与「被允许差异的边界」严格重合

三级演进：

- **v1（想过没实现）** 把证据挂在 `string.format` 入口——「fmt 字符串含
  `%e/%E/%f/%g/%G` 且某参数是 NaN」置位。漏洞：Lua `string.format` 支持
  fmt 是 number（tostring 后转字符串），参数里的 NaN 也未必真被消费
  (`%d` 走 UB guard 抛错、`%s` 走 tostring 又是 PUC/libc 分歧)。证据锚
  在输入侧证不了「渲染真的产生了 NaN spelling」。
- **v2** 改**渲染出口置执行级 bool** `__nan_output`：`print`/`io.write`
  见 `v ~= v` 时置位；`string.format` 跑完后检查输出真含 `nan`/`NAN` 子
  串且入参有 NaN 才置位。证据能证「NaN spelling 真出现在了输出里」了，
  但**授权范围过宽**——bool 一置位就允许比较器对整段输出做 NaN sign
  spelling 容忍。PR reviewer 抓到具体反例：`print(0/0, "BANANA")`（假
  想 wangshu 端为 `-nan\tBA-NANA`），`BANANA` 里插入的 `-` 会被
  `splitNaNOutput` 误识别为 NaN token，然后被 bool 授权吞掉。
- **v3（当前）** 证据升到**渲染事件级 span 列表**：每次 print/write 的
  NaN tostring 输出记 `[off, end)` 精确区间；每次 `string.format(fmt, NaN
  arg)` 里每个 `nan`/`NAN` token 记一个区间（贪婪吸收相邻 `-` 与两侧
  ASCII 空格，让 printf width padding 落进 span）。`CompareOutput` 逐
  span walk：span 内允许 sign spelling，**span 外必须 gap-byte-equal**。
  `print(0/0, "BANANA")` 反例：只有 `0/0` 那一段进 span，`BANANA` 完全
  在 span 外，逐字节比较 `BANANA` vs `BA-NANA` 立刻 `OutputDifferent`。

通用判据：**证据的粒度必须与「被允许差异的边界」严格重合**。按输入 /
输出整段 / 执行整体给证据都是宽了；按引发差异的具体渲染事件给证据才恰
好。是 [[prove-the-path-under-test]] §8「度量单位选对」家族的**证据侧
对偶**——那里说「命中数要跨 Run 稳态计数而非单 Run」，本轮说「证据要
按渲染事件粒度而非执行级」，两者本质都是**度量/证据的时空粒度与验证
边界严格重合**。**已跨 2 实例阈值**，建议在下一实例出现时把 §8 扩成
「度量/证据粒度」通用条并把两轮反引进去，本轮暂留观察。

**Known limit（Codex round-2 review 抓出）**：v3 provenance FIFO 以字符串
**值**为 key（Lua 5.1 无 string identity primitive），当 script literal
与 `string.format(NaN)` 结果**同值**时，先 emit 的 script literal 会误消费
FIFO 首项、把 span 记到错误位置，导致同值 collision 场景下 sporadic
`OutputDifferent`。扩宽 provenance 匹配判据（如"值不等也放行"）会反过来
误吞脚本自出的字面差异——违反 issue #173 明文契约。这是 Lua 5.1 值层设计的
硬边界：**要么区分 identity（脚本 semantics 允许，harness 无法做到）要么
接受 sporadic collision（保护脚本 literal 差异）**。工程上取后者。
`TestExec_NaNSpansKnownLimit` 明确固化 collision 场景 spans 落错行为，
文档（README §5 第 5 层 / `docs/design/engineering.md` §3.2 / `docs/design/p1-interpreter/12-testing-difftest.md` §4.2 / prelude.go godoc）
明记该限制存在。fuzz 撞到的概率极低——需要 script 精心构造 literal 与 format
结果同值——但真的撞到就是 hard-failure，作为 harness 表达能力边界的**公开
表达**留在那里。**推论式教训**：证据粒度不能与被允许差异边界严格重合的极
限值即 identity（原子性能被外部区分）；当 host 语言无 identity primitive 时
证据机制必然存在**语义歧义窗口**——工程决策要在"宽而误吞脚本层次差异"与
"窄而漏过 host identity ambiguity"间做取舍，本轮取后者。

### 教训 4（过程）：WIP commit 不该混进 issue-tracking 分支

WIP commit 80f7b09 title 就是 `WIP`，没有 issue 引用、没有说明性 message，
`fix(oracle)` 三 commit 之后突然冒出来大幅回退方案；本轮 revert 时靠 `git
show 80f7b09 --stat` 与前后 commit 的方向对比才认出它是「回退成简单版」而
非「新的修复」。纪律：一个已经用 `fix(...)` 前缀讲清方向的分支上，中途要
探索「简单版可不可行」时应开独立分支或至少给 WIP commit 一个说明性 message
（`explore: simple ReplaceAll variant, TBD against #173 constraints`），不要
只留 `WIP`；否则下游（或未来的自己）在压力下 rebase 时可能把 WIP 当成「后
续修复」保留。首次样本暂留观察。

## Promotion 候选

- **教训 1**（接受已知平台差异 = 定性 + 证据链 + 窄口径归类）：首次以完整
  三件套形式出现，是 [[cross-backend-semantic-fix-sweep]] guide 的**退让侧**
  候选；**暂留观察**，若下轮再撞「追到值层地板 / IEEE 无语义 / 只能追一个
  CPU-libc 组合」类分歧，可在该 guide 里新增一节「退让侧：定性 + 证据链 +
  窄口径归类」并把两轮反引进去。
- **教训 2**（`NormalizeOutput` 是无损归一，跳过判定分层）：首次样本暂留
  观察，是 [[design-claims-vs-codebase-physics]] §2 邻接维度（视图归一层 vs
  语义比较层），若再现可作独立小 guide「测试比对的分层：归一化 / 判等 /
  归类」。
- **教训 3**（证据粒度与被允许差异边界严格重合）：跨 2 实例阈值（本轮 v2→v3 演进 + [[prove-the-path-under-test]] §8 跨 Run 度量），下一实例出现时建议把 §8 扩成通用「度量/证据粒度」条；本轮暂留观察，是
  [[prove-the-path-under-test]]「证据位定位」家族候选，与已入 guide 的
  「跨 Run 稳态计数」（§8）同属「度量单位选对」教训链，下一实例出现时可与
  §8 合并成「证据位应产生于被证明的具体路径的出口」。
- **教训 4**（WIP commit 不该混进 issue-tracking 分支）：首次样本，
  process-level，暂留观察，不升 guide。

## 触发场景

- 与 PUC 5.1.5 byte-equal 的差异追到 IEEE 无语义 / 只能追一个 CPU-libc 组合 /
  修 VM 值层 ROI 过低时（教训 1：文档定性 + harness 证据链 + 三态归类，不追）；
- 想在测试比对里跳过某类差异时（教训 2：跳过判定放**归类层** `CompareOutput`
  不放归一层 `NormalizeOutput`；负例回归钉住字面量 / literal-adjacent 场景）；
- 写「证明差异来自某语义路径」的证据位时（教训 3：证据粒度必须与被允许差
  异的边界严格重合——按渲染事件锚定字节区间比按渲染出口置执行级 bool
  更窄，比按输入侧猜更准）；
- 一个已经用 `fix(...)` 讲清方向的分支中途要探索简化版时（教训 4：独立分支
  或至少给 WIP commit 说明性 message）。

## 关联

- [[cross-backend-semantic-fix-sweep]]（教训 1 的退让侧候选；本轮承其
  「PUC 语义由 C 实现定义 / libc 定义」纪律，追到值层地板则退让）
- [[2026-07-22-oracle-format-nan-inf-round]]（PR #172，上一轮把 NaN 硬编码
  成 `-NAN`；本轮把它未覆盖的「PUC 正号路径」定性为已知差异 + 机制化跳过）
- [[2026-07-23-oracle-arg-coercion-round]]（本仓最近一轮 oracle diff 分歧
  修复，同 harness）
- [[2026-07-12-cgo-oracle-fuzz-round]]（cgo 内嵌 oracle + FuzzOracleDiff 差分
  设施的建立轮，本轮是它持续产出的一次「机制化接受」处理）
- [[prove-the-path-under-test]]（教训 3 的证据位定位候选）
- [[design-claims-vs-codebase-physics]] §2（教训 2 的分层归一邻接）
- issue #170 · #171 · **#173** · PR #172 · commits 2daf5c0 / 934d818 / 8b9b324 ·
  `internal/oracle/{compare.go,oracle.go,prelude.go,compare_test.go,oracle_test.go}` ·
  `fuzz_oracle_test.go` · `internal/stdlib/stringlib.go` `cFormatSpecialFloat`
  godoc
