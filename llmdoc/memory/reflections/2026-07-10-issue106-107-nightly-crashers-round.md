---
name: 2026-07-10-issue106-107-nightly-crashers-round
description: >
  issue #106/#107 修复轮(2026-07-10,PR #108):nightly-diff-fuzz run 29053951054(跑在 PR #105 合入前的
  1ac8aa4)p3/p4 各撞一个 crasher。#106(p4 seed bdb2e91ab3c4b224,3.3e23 次迭代 intrinsic 循环)重放证实
  已被 #105 的 loopFuel 修复,seed 入 corpus 作常驻回归;#107(p3 seed 3c6a28fa1b2a41f0)是真 bug——P3 wasm
  emitUnm 快路径裸 f64.neg 把 canonNaN(0x7FF8...)的符号位翻成 0xFFF8_0000_0000_0000,恰好是 value.Nil 的
  位模式,`-(0%0)` 把 NaN 写成 Nil,升层后第 2 次 Run 起误报 arithmetic-on-nil。修法镜像 P4 emitUNM 在
  issue #37 端口轮的 result guard。核心教训:「双后端修复不对称」第 3 实例(#67 → #103 → 本轮),按 #103
  反思的预告升 guide;canonNaN 双刃剑的第二家族(位运算 sign-flip,补比较类位相等之外);nightly 失败先查
  run headSha 的 commit 归属已固化为处置动作。
metadata:
  type: reflection
  date: 2026-07-10
---

# issue #106/#107 nightly crasher 修复轮反思(2026-07-10,PR #108)

> 范围:分支 `fix/issue106-107-nightly-crashers`。nightly-diff-fuzz run 29053951054 的两个自动开出的
> crasher issue,一个验证归属后确认已修(#106→PR #105),一个是 P3 wasm UNM 快路径的真 bug(#107)。

## 任务

处置 nightly 长跑撞出的两个 crasher:定位根因、修复或归属、seed 入 corpus、补回归防线。

## 期望与实际

- 期望:两个都是新 bug,各修各的。
- 实际:#106 重放证实已被 4.5 小时后合入的 PR #105 修复(run 跑在合入前的 `1ac8aa4` 上)——只需 seed 入
  corpus;#107 是真 bug,而且是**第三次**踩「双后端修复不对称」:P4 的 emitUNM 早在 issue #37 端口轮
  (2026-07-03)就修过一模一样的 NaN 别名问题,注释还写着 "fixed on both arches in the same change"——
  但 "both arches" 只覆盖 P4 的 amd64+arm64,**没人查 P3 wasm 的同名快路径**。

## #107 根因与修法

最小化复现:`function s()for A=0,3 do A=0%0 A=-A%0 end end s()`——P1 无错;P3 force-all 第 1 次 Run 无错、
第 2 次起报 "attempt to perform arithmetic on a nil value"(升层发生在首次入帧后,第 2 次 Run 才执行 wasm)。

根因:wasm `emitUnm` 快路径是裸 `f64.neg`,头注声称「不产生新 NaN,不需规范化」。前半句对,结论错——
`f64.neg` 把 canonNaN(`0x7FF8...`)的符号位翻成 `0xFFF8_0000_0000_0000`,恰好是 `value.Nil` 的位模式
(`TagNil<<48`)。`A=0%0` 产生 canonNaN,`-A` 直存把它变成 Nil,下一个 `%` 就报 nil 参与算术。

修法:镜像 P4 `emitUNM` 的 result guard(issue #37 端口轮,fuzz seed `f7f0bb1a` NaN 别名家族)——先算
negged,快路径条件收紧为 `IsNumber(vb) && negged < qNanBoxBase`,翻转结果落回 tag 空间就改走 `h_unm`
(host 端 `NumberValue` 规范化回 canonNaN)。

## 核心教训

### 教训 1(「双后端修复不对称」第 3 实例——升 guide 触发)

三个实例的时间线:

| 轮次 | 修了哪边 | 漏了哪边 |
|---|---|---|
| issue #67(2026-07-08) | arm64 NodeHit guard 改良 | 未移植回 amd64 |
| issue #103(2026-07-09) | arm64 unordered 条件码(#37 时) | 未回查 amd64 裸 jcc |
| issue #107(本轮) | P4 amd64+arm64 emitUNM(#37 时) | 未查 P3 wasm emitUnm |

共同结构:同一语义风险(NaN 别名 / unordered / guard 失效)在多个后端(P3 wasm / P4 amd64 / P4 arm64)
各有一份**独立实现**的 inline 快路径;修 bug 时的心理边界停在「当前在改的后端」,而 bug 的真实边界是
「所有绕过 host 语义的 inline 站点」。本轮的教训比前两轮更尖锐:#37 的修复注释明确写了 "both arches",
说明当时**以为**扫全了——「后端」的枚举本身漏了 P3。

可操作纪律(已按 #103 反思的预告升 guide,见 [[cross-backend-semantic-fix-sweep]]):在任一后端修语义类
bug 时,grep **所有**后端的同名 op emit——P3 `translate.go emitXxx` / P4 `emit_ops_amd64.go emitXXX` /
P4 `translator_native_arm64.go emitXxxArm64`——逐一确认同类风险;「后端清单」以 bridge 注册的 Compiler
实现为准,不凭记忆枚举。

### 教训 2(canonNaN 双刃剑的第二家族——位运算 sign-flip)

连 #103 教训 2(「谁的正确性恰好依赖不变式不成立」):canonNaN 规范化使「sign-flip 后恰好落在 TagNil」
100% 确定性复现——若 NaN 位模式随机,这个 bug 几乎撞不上,但也不会静默存在。#103 时识别的受害站点是
**比较类**(EQ 位相等);本轮补上第二家族:**直接位运算类**——neg 的 sign flip、abs 的 mask,任何对
NaN-box 位模式做位级变换的 inline 都可能把 canonNaN 移进/移出 tag 空间。P4 #37 的 result-guard 手法
(变换后重查 tag 边界)是这一家族的通用解药,本轮再次验证。

### 教训 3(nightly 失败先查 commit 归属——处置动作已固化)

run 的 headSha 是 `1ac8aa4`,PR #105 在 run 触发后 4.5 小时才合入——#106 在被开出来之前就已经修好了。
与 #97 事件同构(那次 run 跑的是 `b824f42`,bug 已被 PR #97 修复)。处置动作序列固化:

1. `gh run view --json headSha` 拿 run 实际跑的 commit;
2. `git log` 确认候选修复 commit 与 headSha 的包含关系;
3. `git worktree add` 在旧 commit 上重放,验证「当时真的挂」(本轮:180s 超时);
4. 当前 master 重放,验证「现在真的好」(本轮:0.12s PASS);
5. seed 入 corpus 作常驻回归,issue 经 PR 引用关闭。

第 3/4 步不可省——没有旧 commit 的阳性对照,「现在 PASS」区分不了「已修好」和「复现条件没凑齐」。

## 过程记录

- 最小化:手工收缩 seed 经约 8 个变体(每变体一个单行探针测试),钉住三个复现条件:(a) 错误只在第 2 次
  Run 起出现(升层后);(b) 需要 `A=0%0` → `-A` → 对结果再做算术,三步缺一不可;(c) 每次 Run 换新 State
  会掩盖问题(升层计数被重置)。
- difftest 载体 `p3_unm_nan_alias` 循环 40 次调用,保证升层后的函数体真被执行(prove-the-path:首次调用
  跑 crescent,升层在入帧时发生,第二次起才走 wasm)。
- P3 emit 面动了 → 按 fuzz 维度纪律跑 60s smoke,干净。
