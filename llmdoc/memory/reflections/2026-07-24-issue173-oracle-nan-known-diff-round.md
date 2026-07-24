---
name: 2026-07-24-issue173-oracle-nan-known-diff-round
description: >
  issue #173：#170/#171 修复（PR #172 `cFormatSpecialFloat` 硬编码 -NAN/nan/INF）
  只覆盖了 fuzz 当时撞到的负号路径，但 PUC 自身对 NaN 符号有正负两种可见路径
  （glibc 输出取决于 NaN 位模式 × verb 大小写），追那一个 CPU/libc 组合的具体
  显示需改 VM 值层 NaN-boxing 对齐 x86，收益低风险高。本轮把 #173 定性为 PUC/
  x86/libc 对 NaN 符号的已知平台差异（IEEE 754 不赋 NaN 符号数值语义），改为在
  oracle diff harness 里窄口径识别并跳过：prelude 输出捕获层加 `__nan_output`
  标志 + one-byte readout header 传给 Go 侧 `Result.KnownNaNSign`，`CompareOutput`
  只在两端都有 NaN 输出证据时对单一 nan/NAN token 做符号 spelling 精细比对
  （含 printf width 一列的对偶）。绝不在 `NormalizeOutput` 里全局替换 `nan`/
  `NAN` —— 会误吞脚本自己输出的普通字符串。保留正负例回归测试。过程中先撞到一个
  WIP commit（全局替换）直接违反 #173 明文约束，revert 回精细方案后重新走完。
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

### 2. Harness 侧机制化：`__nan_output` 证据链

`internal/oracle/prelude.go` `preludeCapture` + `preludeGuards` 加输出侧证据
（对称跑在 shim 与 wangshu 两侧）：

- `print(...)` / `io.write(...)` 收到 `number` 且 `v ~= v`（NaN 判据）时把
  Lua-local `__nan_output` 置 `true`；
- `string.format(fmt, ...)`：fmt 或任一参数是 NaN，且实际输出中含 `nan`/`NAN`
  子串时置 `__nan_output = true`；只标「NaN 参数真的走进了 conversion」的路径，
  不覆盖脚本自己拼字符串 `"NAN"` 的情形；
- `__oracle_readout()` 返回值的第 1 字节是 header：`"0"` = 无 NaN 证据，
  `"1"` = 有 NaN 证据；后续是累积的 print/write bytes。

Go 侧 `oracle.DecodeOutput(readout) → (output, nanEvidence, ok)` 剥 header。
`shim.Exec`（cgo 侧）与 `runWangshuSide`（fuzz_oracle_test.go）两条读出路径
都走 `DecodeOutput`，非法 header 直接判 `VerdictLimit`（不可比较、跳过）——
证据缺失或损坏永远不会静默降级成「视作无 NaN」。

### 3. 三态比较：`CompareOutput`

`internal/oracle/compare.go` 新增 `OutputComparison` 三态（`OutputEqual` /
`OutputKnownNaNSign` / `OutputDifferent`），fuzz 用 switch 处理：

- 先跑 `NormalizeOutput`（仅把 `table/function/thread/userdata: 0x...` 归一
  为 `0xADDR`）；两端相等 → `OutputEqual`；
- 否则只有两端 `nanEvidence` 都为真时才尝试 `knownNaNSignDifference` 精细
  匹配；单侧有证据、或证据都无的字面 `NAN`/`-NAN` 分歧 → `OutputDifferent`
  正常失败；
- `knownNaNSignDifference` 按 `splitNaNOutput` 把输出拆成 `(literal, upper,
  negative, leading, trailing)` 序列 + 尾巴 literal：token 数与顺序须一致、
  literal 与 case 逐字节相等，只允许「有一个 token sign 不同」的差异，且用
  `compatibleNaNPadding` 校 printf 宽度对齐（`-` 占一列）；其余字节全一致才归
  `OutputKnownNaNSign`，一被 harness 跳过并打印 `known platform difference: NaN
  sign spelling (#173)`。

**注意**：`NormalizeOutput` **仍然** 只做地址归一化。#173 明文要求「不在
`NormalizeOutput` 中全局替换 `NAN` / `-NAN`，避免误吞脚本自己输出的普通字符
串」；本轮严格遵守。

### 4. 正负例回归

`internal/oracle/compare_test.go` 17 组 `TestCompareOutput`：正例覆盖 lower /
upper / left-padded / right-padded / multiple tokens / 与 literal 相邻
（`NAN0` / `0NAN` / `BANANA vs BA-NANA`）；负例覆盖「plain text 无证据」/
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

### 教训 3：证据来自渲染出口，不是输入侧

第一版起草时曾考虑过在 `string.format` 入口用「fmt 字符串含 `%e/%E/%f/%g/%G`
且某参数是 NaN」判定，把这个位当证据。这个写法有一处静默漏洞：Lua
`string.format` 支持 fmt 是 number（会被 tostring 转字符串），且参数里的 NaN
未必真被消费（比如 `%d` verb 会先走 unsigned-cast UB guard 抛错、`%s` 走
tostring 而 `tostring(nan)` 输出 `nan`/`-nan` 又是 PUC/libc 分歧）。最终采用
**渲染出口置位**：`string.format` 里跑完 `__sformat`、检查 `out` 里含 `nan`/
`NAN` 子串且入参含 NaN 时才置证据位；`print`/`io.write` 直接看 `v ~= v` 也是
出口侧（渲染前一步）。核心判据：**证据要能证「NaN spelling 真出现在了输出
里」，不是「输入里有 NaN」**。首次样本暂留观察，是 [[prove-the-path-under-test]]
「度量单位与证据位」家族在 harness 证据侧的又一实例。

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
- **教训 3**（证据来自渲染出口不是输入侧）：首次样本暂留，是
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
- 写「证明差异来自某语义路径」的证据位时（教训 3：从**渲染出口**置位，不
  从输入侧猜）；
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
