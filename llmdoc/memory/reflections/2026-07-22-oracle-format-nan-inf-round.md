---
name: 2026-07-22-oracle-format-nan-inf-round
description: >
  nightly-fuzz 巡检轮，PR #172，修复 2 个 FuzzOracleDiff crasher #170/#171
  （同根因）。`string.format` 对非有限浮点（NaN/Inf）在 f/e/E/g/G verb 下的
  渲染与 PUC 5.1.5 不一致：Go 的 fmt 拼成 NaN/+Inf/-Inf，而 PUC 走 C sprintf
  由宿主 glibc 决定（小写 verb → nan/inf，大写 → NAN/INF，且带符号列规则）。
  新增 `cFormatSpecialFloat` 在 NaN/Inf 时特判、逐字节复刻 glibc。本轮最值得记
  的是真值不是一次猜对的：从「NaN 符号规则简单」到「所有非有限值 width−1」到
  最终「glibc 只为 NaN 保留 1 个符号列、可见性随 verb 大小写变，Inf 是完整
  width」，连续修正了三次假设，全靠对内嵌 PUC oracle 构造覆盖矩阵（5 verb ×
  符号 × flag × width，93 组）实测逐字节真值逼出来。承 cross-backend
  semantic-fix-sweep 的「PUC 语义源码定义」纪律，本轮延伸到 libc 层。
metadata:
  type: reflection
  date: 2026-07-22
---

# nightly-fuzz 修 string.format NaN/Inf 渲染分歧（2026-07-22，PR #172，#170/#171）

> 范围：nightly-fuzz 巡检轮，PR #172。修复 2 个 FuzzOracleDiff crasher
> #170/#171（同根因），commit `a8e2444`。改动集中在
> `internal/stdlib/stringlib.go` 的浮点 verb 分支，新增 helper
> `cFormatSpecialFloat`；白盒单测 `internal/stdlib/format_special_test.go`
> 钉住完整真值表；#170/#171 corpus 入 `testdata/fuzz/FuzzOracleDiff/` 常驻。

## 任务

nightly 长时间运行的 FuzzOracleDiff（P1 vs 内嵌 PUC 5.1.5 oracle 差分测试）
撞出两个稳定可复现的 crasher，且 #170 多次复发：

- #170：`print(string.format("%E", 0%0))` → oracle `-NAN`，wangshu `NaN`
- #171：`print(string.format("%f", 0%0))` → oracle `nan`，wangshu `NaN`

（`0%0` = NaN。）任务是找准 PUC 对非有限浮点的确切渲染规则并对齐。

## 本轮做了什么

1. **根因**：`stringlib.go` 的浮点 verb（f/e/E/g/G）分支直接用 Go 的
   `fmt.Sprintf`，Go 把非有限值拼成 `NaN`/`+Inf`/`-Inf`；PUC 走 C `sprintf`，
   宿主 glibc 输出不同——小写 verb → nan/inf，大写 → NAN/INF，且有符号列规则。
2. **修复**：新增 `cFormatSpecialFloat`，在 NaN/Inf 时特判、逐字节复刻
   glibc 输出（含 width 语义）。`string.format` 是共享 stdlib，这一处单点修复
   覆盖 P1/P3/P4 所有 tier，差分对称性天然满足，无需按 arch/tier 分叉修复。
3. **测试**：`internal/stdlib/format_special_test.go` 白盒单测（default build，
   无 CGO，直接调包内 `cFormatSpecialFloat`）钉住完整真值表；#170/#171 corpus
   入 `testdata/fuzz/FuzzOracleDiff/` 常驻回归。

## 期望与实际

- 期望：以为 NaN/Inf 符号规则简单（小写 nan、大写 NAN），一次特判就对齐。
- 实际：真值不是一次猜对的，是通过对内嵌 PUC oracle 做差分扫描逐步逼近的，
  连续修正了三次假设才把完整真值表定准：
  1. 先以为 NaN 符号规则简单（小写 nan / 大写 NAN），但 width 场景 `%10.3f`
     出现 1 字节差：oracle 内宽 9，wangshu 10。
  2. 于是以为「所有非有限值有效 width = 声明 width − 1」，给 Inf 也减了 1 →
     Inf 全错（Inf 是完整 width）。修正为「−1 只对 NaN」。
  3. 又发现大写 NaN（`%5E` → ` -NAN` 完整 width 5）也不该减 1 → 最终规律：
     **glibc 总为 NaN 保留 1 个符号列；小写 verb 符号不显示（那一列变空格被
     width 吸收，表现为 width−1），大写 verb 符号是可见的 `-`（已在 core 里
     占了那一列，表现为完整 width）**。Inf 符号一直在 core 里，也是完整 width。
  - 每一步都靠「构造覆盖矩阵的 corpus 直接跑真正的 FuzzOracleDiff、读 oracle
    的确切输出字节」来定真值，最终用 93 组（5 verb × 符号 × flag × width）
    扫描确认全过。

## 教训

### 教训 1（PUC 语义由 C 代码 + 宿主 libc 定义，非有限值的格式化尤其如此）

NaN/Inf 的 `string.format` 输出不是 Lua 手册定的，是 PUC 转发给 C `sprintf`、
再由宿主 glibc 决定的。要对齐必须以 oracle 实测字节为准，不能照 Go fmt 或凭
直觉。

**Why**：这是 [[cross-backend-semantic-fix-sweep]] 已有的「PUC 语义由 C 实现
定义」纪律的一个子类实例——比源码更进一步：`sprintf` 走宿主 libc，连 glibc
的 NaN 符号列 quirk 都是 PUC 语义的一部分，读 C 源码只能看到「转发给
`sprintf`」，真正的真值在 libc 里。

**How to apply**：与 PUC byte-equal 的分歧涉及格式化/数值转换时，先 grep
`_lua515/` 确认它转发给哪个 libc 函数；若真值最终由 libc 决定（`sprintf`
的非有限值渲染、`strtod` 的接受面等），别在源码里止步，直接以 oracle 实测
字节为准。

### 教训 2（glibc 非有限值格式化的 width 语义有反直觉 quirk，必须建覆盖矩阵实测，不能从单点外推）

我从「小写 NaN width−1」这一个样本直接外推到「所有非有限 width−1」，一步就
把 Inf 全改错；真值是「glibc 只为 NaN 保留符号列，可见性随 verb 大小写变，
Inf 是完整 width」。单点外推连续两次把规律带偏。

**Why**：宿主 libc 定义的格式化真值面里，几个维度（verb 大小写、符号、flag、
width）互相耦合，任意一两个样本都不足以约束出正确规律，看似成立的外推会在
没覆盖到的那一格翻车。

**How to apply**：对付「宿主 libc 定义的格式化」这类外部真值，别从一两个样本
猜规则，直接构造覆盖矩阵（verb × 符号 × flag × width）扫一遍，让 oracle 把
完整真值表交出来；规律要能解释矩阵里每一格才算定准。本轮 93 组矩阵就是这条
的落地形式。

### 教训 3（读外部 oracle 的确切输出时，走真正的 harness 路径，别手搓简化探针）

手写 `oracle.Exec` 探针因 prelude 拼接方式错（误把 prelude 拼进 src 参数）+
用空 GlobalSet（prelude 自身崩，table 被白名单刮掉）连续两次拿到空/崩溃输出，
浪费了几轮；而构造 corpus 文件跑真正的 `FuzzOracleDiff -run` 一次就对——harness
已经把 prelude / 白名单 / readout 都配好了。

**Why**：oracle 的正确输出依赖一整套环境（完整 prelude、globals 白名单、输出
捕获），这套环境是 harness 内部搭好的；手搓简化调用最容易在环境搭建上出错，
反而更慢，还会产出「空/崩溃」这种看似是被测输入问题、实则是探针自己没搭对的
伪信号。

**How to apply**：要读 oracle 对某输入的真值，优先复用既有 harness 的入口
（构造它吃的输入格式，比如往 `testdata/fuzz/FuzzOracleDiff/` 放 corpus 文件跑
`-run`），而不是重新拼一个简化调用。这是 [[prove-the-path-under-test]] 家族
在「读外部真值」侧的对应物——简化探针走的不是真正被测的那条路径。

### 教训 4（字节级差异用肉眼数空格不可靠，要用机器精确数）

几次靠肉眼数空格数来判断 width 规律，几次数错导致误判。改用 `[...]` 包裹定位
边界 + `${#inner}` 精确数字节后才定准。

**Why**：带前导/尾随空白的字节串肉眼数长度极易出错，一次数错就把整条 width
规律带偏，且这种错误很难自查（看起来就是差一格）。

**How to apply**：比对带空白的字节串差异时，用 `[...]` 包裹定位边界 + 程序数
长度（`${#x}` / `len()`），不要肉眼数。附带一条本轮踩的 quoting 坑：corpus 文件
里的 `string("...")` 需要 Go 语法 quoting，用 shell `printf %q` 会多转义一层，
改用 python 手写 goquote 才对。

## 缺失的文档或信号

- 「PUC 语义由 libc 定义」这一子类在 [[cross-backend-semantic-fix-sweep]] 的
  「PUC 语义由 C 实现定义」节里已有雏形（那里已提到 `sprintf`/`strtod` 走宿主
  libc），本轮是它的又一具体实例；教训 1 落 memory + 反引即可，暂不单独扩节。
- 「读外部 oracle 真值优先复用 harness 入口、别手搓简化探针」是
  [[prove-the-path-under-test]] 家族在读真值侧的对偶，目前散落，教训 3 首次以
  这个角度出现，暂留观察。

## Promotion 候选

- **教训 1**（PUC 语义由 libc 定义）：是 [[cross-backend-semantic-fix-sweep]]
  既有「PUC 语义由 C 实现定义」纪律的子类实例，不升 guide，memory 内反引即可；
  若后续再撞几处「真值最终落在 libc」的分歧，可在该 guide 那一节里补一句
  「转发给 libc 的函数以 oracle 实测为准，别在 C 源码止步」。
- **教训 2**（覆盖矩阵实测、不从单点外推）：首次以独立教训形式出现，**暂留
  观察**；若下一轮再撞「宿主 libc / 外部真值面从单点外推翻车」，可作
  [[cross-backend-semantic-fix-sweep]] 的「外部真值面须建覆盖矩阵」新条款。
- **教训 3**（读外部真值走真正 harness、别手搓探针）：首次样本，**暂留观察**，
  是 [[prove-the-path-under-test]] 家族在读真值侧的候选对偶。
- **教训 4**（机器精确数字节，别肉眼数空格）：首次样本，**暂留观察**，与教训 3
  同属「读外部真值的操作纪律」，若再现可与教训 3 合并成一条小 guide。

## 触发场景

- 与 PUC 5.1.5 byte-equal 的分歧涉及格式化 / 数值转换时——先 grep `_lua515/`
  看转发给哪个 libc 函数，真值落 libc 就以 oracle 实测字节为准（教训 1）；
- 对付「宿主 libc / 外部真值面」猜规则时——别从一两个样本外推，构造覆盖矩阵
  让 oracle 交出完整真值表，规律要能解释每一格（教训 2）；
- 要读某个外部 oracle 对某输入的确切输出时——复用既有 harness 入口（构造它吃
  的输入格式跑），别手搓简化调用（教训 3）；
- 比对带空白的字节串差异时——用 `[...]` 包裹 + 程序数长度，别肉眼数空格；
  corpus 里 Go 语法字符串用 python goquote 而非 shell `printf %q`（教训 4）。

## 关联

[[cross-backend-semantic-fix-sweep]]（教训 1 是其「PUC 语义由 C 实现定义」节
的 libc 层子类实例）· [[2026-07-12-cgo-oracle-fuzz-round]]（那一轮建立了 cgo
oracle + FuzzOracleDiff 差分设施，本轮是它持续产出的又一个分歧修复；那轮教训 2
「PUC 语义读 C 源码」在本轮延伸到 libc 层）· [[prove-the-path-under-test]]
（白盒单测 `format_special_test.go` 直接钉 helper 真值表；教训 3 是其在读外部
真值侧的对偶）· PR #172 · commit `a8e2444` · `internal/stdlib/stringlib.go`
`cFormatSpecialFloat` · `internal/stdlib/format_special_test.go` ·
`testdata/fuzz/FuzzOracleDiff/`（#170/#171 corpus）
