---
name: 2026-07-23-oracle-arg-coercion-round
description: >
  nightly-fuzz 巡检轮，PR #176，修复 2 个 FuzzOracleDiff crasher #174/#175
  （不同表象、同一类根因：wangshu 的参数强制转换比 PUC 5.1.5 严）。#174
  `string.rep("...", "0X0")` 次数参数是 Lua 十六进制整数字符串，PUC 用
  `luaL_checknumber` 强制转成 0，wangshu 报「number expected」；根因是
  `toNumberStr`（stdlib 内部字符串→数字，约 28 个调用点）用裸
  `strconv.ParseFloat`，不认 Lua hex 整数，而仓库其实早有对齐 PUC
  `luaO_str2d` 的权威实现 `crescent.ParseLuaNumber` 却没被复用。#175
  `tonumber(0, "2")` 带 base 时 PUC 对第一参数用 `luaL_checkstring` 把
  number 强制转成字符串，wangshu 报「string expected」。修复把
  `toNumberStr` 改走 `crescent.ParseLuaNumber`（一处对齐 string/table/math
  各库全部数字强制转换）+ `tonumber(x, base)` 第一参数接受 string 或 number。
  最值得记的是：仓库里已有权威实现却在别处另写了个简化版，两份实现宽松度
  分叉直到 fuzz 撞出；且回归测试的期望值全部先跑 oracle 差分核对（如
  `tonumber(255,16)==597` 反直觉）再固化，不凭直觉写。
metadata:
  type: reflection
  date: 2026-07-23
---

# nightly-fuzz 修参数强制转换比 PUC 严的两处分歧（2026-07-23，PR #176，#174/#175）

> 范围：nightly-fuzz 巡检轮，PR #176。修复 2 个 FuzzOracleDiff crasher
> #174/#175（不同表象、同一类根因）。改动集中在
> `internal/stdlib/stdlib.go` 的 `toNumberStr` 与 `baseFnToNumber` 的 base
> 分支；`stdlib_test.go` 的 `TestStdlib_NumericArgCoercion` 钉住行为；
> #174/#175 corpus 入 `testdata/fuzz/FuzzOracleDiff/` 常驻回归。

## 任务

nightly 长时间运行的 FuzzOracleDiff（P1 vs 内嵌 PUC 5.1.5 oracle 差分测试）
撞出两处稳定可复现的分歧，都是 wangshu 的参数强制转换比 PUC 严：

- #174：`string.rep("0000000000000000", "0X0")` —— 次数参数是 Lua 十六进制
  整数字符串 `"0X0"`。PUC 用 `luaL_checknumber` 强制转（`"0X0"` → 0），
  wangshu 报错「number expected, got string」。
- #175：`tonumber(0, "2")` —— 带 base 时 PUC 对第一参数用 `luaL_checkstring`
  （number `0` 强制转成 `"0"`），wangshu 报错「string expected, got number」。

## 本轮做了什么

1. **根因（#174）**：`toNumberStr`（stdlib 内部的字符串→数字转换，被
   string/table/math 各库广泛调用——审计出约 28 个调用点）用裸
   `strconv.ParseFloat` 解析字符串，而 Go 的 `ParseFloat` 不认 Lua 十六进制
   整数 `"0X0"`/`"0xff"`（它只接受 C99 hex float，需要尾数/p 指数）。而
   wangshu 其实**早就有**正确的 `crescent.ParseLuaNumber`（对齐 PUC
   `luaO_str2d`，含 hex 整数 fallback + 前后空白容忍），`toNumberStr` 却没用
   它，自己用了个不一致的简化实现，于是 stdlib 侧所有字符串→数字强制转换比
   VM 侧宽松度低一截。
2. **根因（#175）**：`baseFnToNumber` 的 base 分支硬性要求第一参数是 string，
   没有按 PUC `luaL_checkstring` 语义接受 number。
3. **修复**：`toNumberStr` 改走 `crescent.ParseLuaNumber`（删掉裸
   `strconv.ParseFloat`，连带删了 `strconv` import）——一处修复使
   string/table/math 各库的字符串→数字强制转换全部对齐 PUC。
   `tonumber(x, base)` 第一参数接受 string 或 number（number 经
   `crescent.FormatLuaNumber` 转字符串），其它类型仍报错。
4. **测试**：`stdlib_test.go` 的 `TestStdlib_NumericArgCoercion` 钉住两个行为
   （加十进制字符串、base-16 对照）；期望值先用 oracle 核对过
   （`string.rep("ab","0X0")` → `""`、`tonumber(0,"2")` → `0`、
   `tonumber(255,16)` → `597`），没有凭直觉写。#174/#175 corpus 入
   `testdata/fuzz/FuzzOracleDiff/` 常驻。全套件（default/oracle/p3/p4）绿，
   45s oracle fuzz smoke 无新分歧（接受面放宽后按 [[prove-the-path-under-test]]
   §5 重探）。

## 期望与实际

- 期望：以为是两个独立的小口径修复，各改一处判断即可。
- 实际：#174 挖下去才发现根因不是「少判了一种字符串」，而是仓库里同一个
  能力（Lua 字符串→数字）存在两份实现——VM 侧权威的
  `crescent.ParseLuaNumber` 与 stdlib 侧简化的裸 `strconv.ParseFloat`，两者
  宽松度长期分叉，stdlib 的所有数字强制转换都偏严直到 fuzz 撞出。修复不是
  补一种字符串，而是让 stdlib 复用权威实现、消掉这份分叉。

## 教训

### 教训 1（一个能力已有权威实现时，别在别处另写一个简化版——两份实现迟早分叉）

`crescent.ParseLuaNumber` 是 VM 侧对齐 PUC `luaO_str2d` 的字符串→数字权威
实现，但 stdlib 的 `toNumberStr` 自己用裸 `strconv.ParseFloat` 又实现了一遍，
少了 hex 整数支持，于是 stdlib 的所有数字强制转换都比 VM 侧宽松度低一截，
直到 fuzz 撞出来。

**Why**：语义敏感的基础转换（字符串→数字这类）里，标准库（如 Go
`strconv`）的接受面往往与 Lua/PUC 语义不一致——hex 整数、前后空白、特殊
值都可能不同。图省事用标准库简化版，就等于在仓库里埋了第二套语义，它与
权威实现的差异不会立刻暴露，要等某个恰好落在差异区的输入被 fuzz 撞到。

**How to apply**：遇到「X → Y」这类语义敏感的基础转换操作，先 grep 全仓有
没有已存在的权威实现（尤其 VM 侧 / crescent 包），有就复用，不要另写标准库
简化版。承 [[cross-backend-semantic-fix-sweep]]「语义要经统一入口而不是绕过」
——那篇管跨后端的语义站点收敛，本条是同一原则在「同一进程内同一能力两份
实现」维度的实例。

### 教训 2（共享 helper 的单点修复能一次对齐一大批调用点，但也要意识到影响面）

`toNumberStr` 被约 28 处调用（string/table/math 三库），改它是收敛（全部
对齐 PUC）而非发散，但正因影响面大，改完必须跑全套件 + oracle fuzz smoke
确认没有别的调用点依赖旧的宽松度差异。

**Why**：共享 helper 的修改会一次改变所有调用点的行为，方向对（收敛到
PUC）是好事，但也意味着任何一个调用点若曾无意依赖旧的偏严行为，都会在这
次一起改变；只测触发 bug 的那一个调用点看不见其它调用点的连带变化。

**How to apply**：改共享 helper 前先 grep 清全部调用点、数清影响面；改后
全套件 + 定向 fuzz 一起上，别只测触发 bug 的那一个调用点。

### 教训 3（回归测试的期望值要用 oracle 核对，不能凭直觉写）

`tonumber(255,16)` = `597`（把 `"255"` 按 16 进制读）这种反直觉结果，靠脑补
容易写错；本轮所有期望值都先构造 corpus 跑真正的 FuzzOracleDiff 确认
wangshu == PUC，再写进单测。

**Why**：差分类修复的正确答案由 PUC 定义，不由直觉定义；反直觉的边界值
（hex 读法、强制转换后的结果）凭印象写下来的断言可能本身就是错的，那样单测
只是把一个错误固化下来，看着绿却没在钉住正确行为。

**How to apply**：写差分类修复的回归测试，期望值以 oracle 实测为准——先跑
一遍 oracle 差分（全过 = wangshu 与 PUC 一致）再把那个值固化进断言。承
[[2026-07-22-oracle-format-nan-inf-round]] 教训「读 oracle 确切输出走真正
harness」——那轮是 NaN/Inf 渲染真值靠 harness 逼出来，本轮是强制转换的期望值
靠 harness 核对，同一条纪律的两个实例。

## 缺失的文档或信号

- 「仓库里已有权威实现却在别处另写简化版」这类隐患没有现成信号能提前提醒；
  教训 1 落 memory 即可。若后续再撞几处「stdlib / 某子系统自写简化转换与
  VM 侧权威实现分叉」，可考虑在 [[cross-backend-semantic-fix-sweep]] 里补一条
  「同一能力单点权威实现、别处复用别另写」的条款。
- 「差分类回归测试期望值以 oracle 实测为准」目前散落在几轮反思里
  （2026-07-22 教训 3、本轮教训 3），是 [[prove-the-path-under-test]] 家族在
  「读真值 / 定期望值」侧的对偶，暂留观察。

## Promotion 候选

- **教训 1**（已有权威实现别另写简化版）：首次以这个角度出现，**暂留观察**；
  与 [[cross-backend-semantic-fix-sweep]]「语义经统一入口」同族但维度不同
  （那篇是跨后端多站点，本条是同进程同能力两份实现），若再现可作该 guide
  的一条补充条款。
- **教训 2**（共享 helper 单点修复的影响面）：首次样本，**暂留观察**。
- **教训 3**（差分回归测试期望值以 oracle 实测为准）：与
  [[2026-07-22-oracle-format-nan-inf-round]] 教训 3 同族第二实例，仍**暂留
  观察**；若第三次再现，可与那轮合并成 [[prove-the-path-under-test]] 家族在
  「读外部真值 / 定期望值」侧的一条小 guide。

## 触发场景

- 要给某个「X → Y」语义敏感转换加实现或改行为时——先 grep 全仓（尤其
  crescent / VM 侧）看有没有权威实现可复用，别另写标准库简化版（教训 1）；
- 要改一个被多处调用的共享 helper 时——先 grep 数清全部调用点与影响面，
  改后全套件 + 定向 fuzz 一起上，别只测触发 bug 的那一处（教训 2）；
- 写差分类修复的回归测试要定期望值时——先构造 corpus 跑真正的
  FuzzOracleDiff 确认 wangshu 与 PUC 一致，再把那个值固化进断言，别凭直觉
  写反直觉的边界值（教训 3）；
- 放宽某个接受面之后——按 [[prove-the-path-under-test]] §5 立刻跑一轮
  oracle fuzz smoke 重探。

## 关联

[[cross-backend-semantic-fix-sweep]]（教训 1 是其「语义经统一入口不绕过」在
「同能力两份实现分叉」维度的实例）· [[2026-07-12-cgo-oracle-fuzz-round]]
（FuzzOracleDiff 差分设施来源；那一轮就修过 `tonumber` 走 C `strtod` 接受面，
本轮是同一函数族的又一处 + `toNumberStr` 统一走权威实现）·
[[2026-07-22-oracle-format-nan-inf-round]]（上一轮 oracle 差分修复；教训 3
的「期望值走 harness 核对」纪律承自那轮）· [[prove-the-path-under-test]]
（§5 接受面放宽后重探 fuzz；教训 3 是其读真值侧对偶）· PR #176 ·
`internal/stdlib/stdlib.go`（`toNumberStr` / `baseFnToNumber`）·
`internal/stdlib/stdlib_test.go`（`TestStdlib_NumericArgCoercion`）·
`crescent.ParseLuaNumber` / `crescent.FormatLuaNumber` ·
`testdata/fuzz/FuzzOracleDiff/`（#174/#175 corpus）
