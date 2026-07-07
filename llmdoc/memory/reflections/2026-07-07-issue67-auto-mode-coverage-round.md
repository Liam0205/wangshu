---
name: issue67-auto-mode-coverage-round
description: PR #75 auto-mode coverage round(2026-07-07):production 跑 auto promotion,但 CI 每套 P3/P4 测试都靠 SetForceAllPromote(true) 驱动,自然热度半边(阈值中途越线、recheckCompilabilityRuntime 自然路径、PromotionGater、地板+FloorExempter,均 auto-only)长期无网(issue #67 正是此类 auto-only bug)。未强制的测试也测不到——它们的脚本永远够不到生产阈值(entry 200 / back edge 1000),auto 退化成纯解释器。新增 Bridge.SetHotThresholds(entry, backEdge)(0=保持不变),与 SetForceAllPromote 同纪律的 testing-only 入口:只改「何时」触发决策不改「决策什么」。四类新覆盖(difftest/conformance/fuzz/nightly)全部 build-tag (wangshu_p3||wangshu_p4)&&wangshu_profile。两条教训:①「未强制的测试 ≠ auto 模式测试」——PromotionCount>0 兜底断言是解药,且在开发中现场应验(P3 首版语料因 WorthPromoting 密度门拒收全部短核而挂,逼语料换成长纯算术核);② 有状态差分 harness 的 run-for-run 基线——FuzzAutoPromote 上线首次 CI 跑就抓到 harness bug(seed 861f54880d2009d5,留作回归种子):脚本改写全局变量时,同一 State 后续 Run 合法产生不同行为,但 harness 只跑一次基线却跑两次 auto State,把跨 run 状态漂移误判成 tier 分歧;修法基线与被测同步逐 run 对比。
metadata:
  type: reflection
  date: 2026-07-07
---

# issue #67 auto-mode 覆盖轮反思(2026-07-07,PR #75)

> 范围:分支 `docs/llmdoc-auto-coverage` 的前序实现分支(合并为 master `199a215` + `3efa85b`)。目标是给 P3/P4 的自然热度(auto)升层路径补上此前完全缺失的测试网。

## 任务

生产环境跑 auto promotion(脚本自然越过入口/回边阈值才升层),但仓库里每一套 P3/P4 测试(difftest、conformance、`FuzzP4ForceAllPromote`、nightly legs)都用 `SetForceAllPromote(true)` 强制升层驱动。这意味着:

- **auto-only 决策链完全没有测试网**——阈值中途越线时机、`recheckCompilabilityRuntime` 在自然路径上的行为、`PromotionGater`(auto-only 可选接口)、短 proto 地板 + `FloorExempter`(同为 auto-only)。issue #67 本身就是一个 auto-only bug,只在这条链上才会触发。
- **未强制的测试也不构成 auto 覆盖**——它们的脚本从没长到能在生产阈值(entry 200 / back edge 1000)下自然越线,auto 在这些测试里退化成纯解释器,和真正测「auto 决策链」是两回事。

## 期望与实际

- 期望:给 auto 决策链补上不依赖 force-all 的测试覆盖,同时不改变生产 auto 的决策逻辑本身。
- 实际:**达成**。新增 `Bridge.SetHotThresholds(entry, backEdge)`(0 = 保持当前值),同 `SetForceAllPromote` 纪律的 testing-only 入口——只改「auto 决策何时跑」不改「跑起来决策什么」。调低阈值让短用例在 run 中途真实越阈值走完整 auto 决策链;调到 `MaxUint32` 得到同一 build 下保证不升层的解释器基线,可作差分基线。四类新覆盖全部 build-tag `(wangshu_p3 || wangshu_p4) && wangshu_profile`:

1. `test/difftest/auto_test.go` —— `TestAuto_Tiered`(14-kernel 语料,同一 State 上重复 Run,逐 run byte-equal 对比基线与 oracle,`PromotionCount>0` 兜底断言)、`TestAuto_SeedCorpus`、`TestAuto_RandomScripts`(生成器驱动,`WANGSHU_FUZZ_*` 可扩展,DIVERGENCE 标记供 nightly triage)、`TestAuto_GCStressTiered`。
2. `test/conformance/conformance_auto_test.go` —— 全量 conformance 语料在调低阈值下重跑 + 两个恒可升层的算术锚点 + `PromotionCount>0` 兜底断言。
3. `fuzz_auto_test.go` —— `FuzzAutoPromote`:P1 基线 vs auto-tiered 差分,每个输入在同一 State 上跑两次(中途翻转 + tier 混合稳态)。
4. `nightly-diff-fuzz.yml` 新增 auto-mode 滚动种子 leg(仅 p3/p4,轮数 1/3)。
5. `race_on`/`race_off` 的 `raceEnabled` 守卫从 `wangshu_p4`-only 扩到 `(wangshu_p3 || wangshu_p4)`。

设计文档记录在 `docs/design/p2-bridge/01-profiling.md` §5.3.1(合并提交中已同步,非本轮 llmdoc 待办)。

## 核心教训

### 1. 「未强制的测试 ≠ auto 模式测试」——沉默退化成纯解释器

不带 `SetForceAllPromote` 的测试**看起来**是 auto 覆盖,实则悄悄退化成解释器专属测试——因为生产阈值对短脚本永远不可达。解药是 **`PromotionCount>0` 兜底断言**:任何声称在测 auto 决策链的测试套必须显式断言至少发生过一次真实升层,否则绿色只证明「跑完了」不证明「auto 决策链被走到」。

这条纪律在开发过程中**当场应验**:第一版语料草稿在 P3 上就挂在 `PromotionCount>0` 断言上——`WorthPromoting` 收益密度门拒绝了语料里每一个短核,逼着把语料换成长纯算术核才通过。断言不是摆设,它在写语料的当下就抓住了一次「看似覆盖、实则没升层」的语料设计错误。

**Why**:是 [[prove-the-path-under-test]] 家族「绿色 ≠ 在测你以为在测的」的又一实例——具体形式是「同一套测试代码在不同后端/参数下静默滑向另一条路径(auto 退化成纯解释器)」,家族已跨过 12 实例阈值,本条作**第 13 个独立实例**收录进该 guide。

**How to apply**:任何测试套自称覆盖 auto/natural 升层路径,必须配一个白盒计数器(`PromotionCount` 或等价物)兜底断言「确实升层过」;没有这个断言,过线的短用例语料在下一次收益门调整时可能悄悄滑回纯解释器而没人发现。

### 2. 有状态差分 harness 的 run-for-run 基线——同一 State 多次 Run 时,基线必须逐 run 同步对比

`FuzzAutoPromote` 第一次真实 CI 跑就抓到一个 **harness bug**(不是 VM bug),种子 `861f54880d2009d5` 保留作回归种子:被测脚本会合法地改写全局变量(如 `setmetatable({A00}, A)`),在同一 State 上第二次 Run 时,由于第一次 Run 已经把全局 `A` 覆写成非 table,第二次 Run **理应**报错——这是脚本语义的正常跨 run 状态漂移,不是 tier 分歧。但原 harness 只跑了一次基线 State、却跑了两次 auto State,于是把这个正常漂移误判成「auto 与基线不一致」的分歧。

修法:基线 State 与被测 auto State **逐 run 同步跑**(lockstep),run N 只与基线的 run N 比较,不与基线的单次快照比较。已应用到 `FuzzAutoPromote` 与全部四个 `TestAuto_*` difftest 套件(conformance 套件按固定期望字节逐 run 比较、已隐含 run-independence,不受影响)。

**Why**:任何「在同一个有状态对象上重复驱动被测系统、并拿一个独立跑的基线做对照」的差分 harness,只要被测系统的状态会跨 run 累积(全局变量、缓存、GC 计数……),基线就必须以**完全相同的 run 序列**驱动,否则运行次数不对称本身就会制造出「看起来像分歧」的假阳性——这不是被测对象的 bug,是 harness 设计没有对齐两侧的状态演化节奏。

**How to apply**:写任何「同一个有状态对象反复调用 + 独立基线对照」的差分/差分测试 harness 时,先问「基线与被测是否跑了完全相同次数的操作、且顺序一致」;如果被测侧会在同一个持久对象上多次调用(而不是每次都新建全新状态),基线必须逐次同步伴跑,不能只跑一次当全程快照。

## 验证

- amd64/arm64 全部正确性门(difftest-p3/p4、conformance、`-race`)绿;`FuzzAutoPromote` 60s p4 + 30s p3 smoke 干净;CI crasher 种子 `861f54880d2009d5` 作为回归种子通过。

## promotion 决策

- **教训 1**:是 [[prove-the-path-under-test]] 家族第 13 个独立实例,**本轮直接 promote** 进该 guide(§1 反模式三档表追加一行 + 触发场景速查追加一条),因为家族早已跨过阈值、且贡献了明确的解药形式(`PromotionCount>0` 兜底断言),不需要再等第二次样本验证。
- **教训 2**(run-for-run 基线):**首次样本,暂留观察**。是一条通用的「有状态差分 harness 设计」纪律,若后续再有一个独立的有状态差分/差分测试 harness(不限于 tier 升层)撞上同族「基线与被测运行次数不对称」问题,再考虑并入 [[prove-the-path-under-test]] 或独立立一条「有状态差分 harness 设计」guide 条目。

## 触发场景

- 给某个自然触发路径(auto 升层、自然热度、非强制模式)写测试套时(教训 1:必须配白盒计数器兜底断言该路径真被走到,否则短用例语料可能悄悄退化成另一条路径的测试)。
- 写「同一个有状态对象反复调用 + 独立基线对照」的差分/差分测试 harness 时(教训 2:检查基线是否与被测逐 run 同步跑,而非只跑一次当全程快照)。

## 关联

[[prove-the-path-under-test]](教训 1 落点,第 13 实例)· [[backend-capability-vs-profitability]](`PromotionGater`/`FloorExempter`/地板同属该 guide 描述的 auto-only 可选接口家族,本轮补的正是它们的测试网)· issue #67 · `internal/bridge/bridge.go`(`SetHotThresholds`)· `internal/crescent/state.go` · `wangshu.go` · `docs/design/p2-bridge/01-profiling.md` §5.3.1 · `test/difftest/auto_test.go` · `test/conformance/conformance_auto_test.go` · `fuzz_auto_test.go` · `.github/workflows/nightly-diff-fuzz.yml`
