---
name: pr86-deep-recursion-nosplit-round
description: PR #86(fixes #85,files #89,2026-07-08)修复两个由深递归触发的 tiered-path bug,第二个由第一个的修复暴露。#85 是深升层递归报假 "C stack overflow"——解释器 Lua→Lua 递归平坦(仅受 maxLuaCallDepth=20000 约束),P4 路径每层真 Go 重入烧 nCcalls(上限 200),深度 1000 合法递归被误报;修法为 gibbousReentryCCallCap 软水位线,四个 gibbous 派发入口超水位回落解释器平坦执行。#89(存量 bug,被 #85 修复暴露成 CI 秒撞)是 seg2seg 段内直调在 NOSPLIT 窗口内的每层 sub sp 对 linker nosplit 记账不可见,segToSegDepthCap=128(~4KB)穿透 Go 栈守卫仅 800 字节的 NOSPLIT 允许量,踩坏相邻堆对象 → GC 报 zombie;修法 cap 降 16,提回需接自管 spill 栈(05 §3.4 设计过未接线)→ issue #89。
metadata:
  type: reflection
  date: 2026-07-08
---

# PR #86 深递归双 bug 连锁修复轮反思(2026-07-08)

> 范围:分支 `fix/issue-deep-native-recursion`,三个提交:`90ef26b`(#85 水位线)、`5ff42fb`(seg2seg NOSPLIT cap + review 对齐)、`d9ccadd`(回归测试自包含化)。

## 任务

PR #83 合入后 master CI fuzz-smoke 立刻撞出新 crasher `41aacb7ebe17996d`(#85);修好后 PR #86 自己的 CI fuzz-smoke (p4/arm64) 又撞出 `7f161a85c466adbf`(GC "found pointer to free object",→ #89 followup)。两个 bug 同轮修复。

## 期望与实际

- 期望:#85 修好、回归测试全绿后,PR CI 应当直接过。
- 实际:**第一个修复本身就是第二个 bug 的暴露器**——水位线让深升层递归从「~328 层报 C stack overflow 死掉」变成「活到 maxLuaCallDepth」,存量的 seg2seg NOSPLIT 栈溢出第一次有机会在腐蚀窗口内被反复驱动,CI fuzz 2 秒即撞。master 上也能复现(确认存量),但此前深递归活不到那个窗口,近乎不可达。

## 两个修复

### 1. #85 — 深升层递归报假 "C stack overflow"(commit `90ef26b`)

解释器的 Lua→Lua 递归是**平坦的**:单 `executeFrom` 循环驱动 CallInfo 链,每层不消耗 Go 栈,只受 `maxLuaCallDepth`(20000,PUC `LUAI_MAXCALLS`)约束。P4 路径在 seg2seg 深度上限之外每层递归都是真 Go 重入链(`Run → dispatcher → ExecutePlainCallInlineFrame → enterGibbous → Run …`),每层 `nCcalls++`(`maxCCallDepth`=200,PUC `LUAI_MAXCCALLS`)——深度 1000 的合法递归在升层后被误报,P1 语义下该错误不存在。nCcalls 计费本身**正确**(每层确实耗 Go 栈);错的是「深递归必须一直走 native 层」这个隐含假设。

修复:`frame.go` 新增软水位线 `gibbousReentryCCallCap = maxCCallDepth / 2`,四个 gibbous 派发入口(`doCall` 升层分支、`ExecutePlainCallInlineFrame` / `ExecuteCalleeFromInlineFrame` 的 zero-cross 分支、`executeFrom` 的 gibbous TAILCALL 派发)超水位改走解释器——升层 proto 保留字节码,剩余递归在同一个 executeFrom 循环里平坦解释,零额外 Go 重入,深度上限回到 20000,与 P1 逐字节一致。另一半 nCcalls 预算留给水位线以下的元方法 / host 重入。四个入口统一在自增前采样水位(review bot 指出的 1 级时机偏差,当轮对齐)。

### 2. #89 — seg2seg NOSPLIT 窗口栈溢出踩堆(commit `5ff42fb`)

seg2seg 段内直调每层在 goroutine 栈上 `sub sp`(arm64 32 B / amd64 ~24 B),发生在 morestack 不可触发的 NOSPLIT 窗口内,且**对 Go linker 的 nosplit 记账完全不可见**。Go 栈守卫只在检查点之下保留 `StackNosplitBase = 800` 字节(`internal/abi/stack.go`);旧 `segToSegDepthCap = 128`(~4 KB)在 Run 于 SP 贴近守卫时被进入(深 Go 重入链下正是常态)会静默穿透栈分配、踩坏相邻堆对象,GC sweep 时报 zombie pointer。

旧常量的注释声称「cap=128 tops out at ~4KB, comfortably within the stack the trampoline is entered with」——**对照的物理量错了**:它对照的是 goroutine 栈大小,而真正的预算是 NOSPLIT 允许量(800 B)。修复:cap 降到 16(worst case 96 B CallJITSpec NOSPLIT 帧 + 16×32 B = 608 B < 800 B)。实测边界(darwin/arm64,GC percent 1):16 从不崩(9/9),64/128 必崩(6/6)——与算术预测吻合。HeavyRecursion 基准两 cap 无差异(3.7-4.4 ms;真实负载递归浅)。要提回 cap 需先接上 05 §3.4 设计过但从未接线的自管 spill 栈(`jitCtx.spillBase/spillTop`)→ issue #89。

## 核心教训

### 教训 1([[prove-the-path-under-test]] §5 第五次验证,含新面):修复本身就是维度重置器

PR #83 反思记录了「测试网上线 / 切换驱动模式」触发 fuzz 探索空间维度重置;本轮补充第三种触发方式:**一个 bug 的修复扩大了可达状态空间,立刻暴露下一个此前不可达的存量 bug**。#85 修复让深递归「活到」maxLuaCallDepth,seg2seg 的 NOSPLIT 溢出窗口第一次被反复驱动,同一 PR 的 CI 就撞。判据:**修复让某类执行「活得更深/更久/更广」时,预期它顺带解锁新的存量 bug 暴露面,PR 自己的 fuzz 撞新 crasher 不是修复引入回归的证据,先在 master 上复现区分存量还是新引入**。本轮 master 复现确认存量,避免了错误回滚。

### 教训 2([[design-claims-vs-codebase-physics]] 新实例):栈预算声明必须对照正确的物理量

旧 `segToSegDepthCap` 注释的「~4KB comfortably within the stack」是一个听起来合理、但**对照错物理量**的声明:goroutine 栈有几 KB 到几 MB,而 NOSPLIT 窗口内可用的只有栈守卫保留的 800 字节,且段内 `sub sp` 对 linker 记账不可见 = 没有任何工具链兜底。与该 guide 既有实例(「$base 全程不变」「入口 base 当固定 token」)同构:**声明写下时没有对照它真正依赖的物理约束**。判据补充:凡涉及「在 NOSPLIT / nosplit-invisible 上下文里消耗机器栈」的设计,预算上限是 `StackNosplitBase`(800 B)减去链上所有 NOSPLIT 帧,不是栈大小;mmap 段内的 SP 移动 linker 看不见,必须人工记账。

### 教训 3(方法论,首次样本):依赖运行时内存布局的 crash,最小化时保持 harness 形状

复现此 crash 花的时间大半消耗在:简化版单 State repro 在坏 cap 下**不崩**——只有复刻 fuzz harness 的精确形状(baseline State 与 auto State 交错跑)才能把 goroutine SP 压到贴近栈守卫的位置。教训:**语义类 bug 可以激进最小化;依赖运行时内存状态(SP 位置、堆布局、GC 时机)的 bug,在根因明确前保持 harness 原形**,配合 `debug.SetGCPercent(1)` 把概率性撞击变成 3/3 确定性复现(回归测试内部自设 + `t.Cleanup` 还原,自包含)。二分序列(seg2seg off → 不崩;cap 8/16 → 不崩;cap 64/128 → 必崩)+ 读 `runtime/stack.go` 的 `stackNosplit=800` 做算术交叉验证,经验边界与解析预测吻合后才定 cap=16。

## 验证

- 两个 crasher seed 无修复时均复现失败、修复后 p3/p4 全过,入 `testdata/fuzz/FuzzAutoPromote/` 常驻语料;
- `deep_recursion_test.go` 六个回归测试:crasher 形状 d=300/1000、累加变体(值正确性)、maxLuaCallDepth 边界一致性(21000 层仍报纯 "stack overflow")、d=50000 真尾递归、GC-stress prove-the-path(cap=128 崩 3/3 / cap=16 过 3/3,GC percent 1 自包含);
- P1/P3/P4 全量测试 + 3000 轮 `TestAuto_RandomScripts` 滚动种子差分 + 本地 FuzzAutoPromote 摇测干净;
- HeavyRecursion 基准无回退。

## promotion 决策

- 教训 1:并入 [[prove-the-path-under-test]] §5 家族(第五次验证 + 「修复解锁暴露面」新触发方式),本反思记录即可,视后续复现再升级 guide 正文。
- 教训 2:[[design-claims-vs-codebase-physics]] 候选新实例(NOSPLIT 预算 vs 栈大小);建议下次 guide 修订时并入实例表。
- 教训 3:首次样本,暂留观察;若 P5 trace JIT 或后续 arena/GC 工作再现「简化 repro 不崩、原形 harness 崩」,升级为独立 guide 小节。

## 触发场景

- 修复让某类执行活得更深/更久后,同 PR fuzz 撞新 crasher 时(教训 1:先 master 复现区分存量 vs 新引入,不要反射性回滚)。
- 设计/审查任何在 mmap 段内或 NOSPLIT 链上消耗机器栈的机制时(教训 2:预算对照 `StackNosplitBase`,不是栈大小;linker 看不见的 SP 移动要人工记账)。
- 最小化依赖运行时内存状态的 crash 时(教训 3:保持 harness 形状 + SetGCPercent(1) 确定性化,二分 + 解析算术交叉验证)。

## 关联

[[prove-the-path-under-test]](§5 第五次验证 + 新触发方式)· [[design-claims-vs-codebase-physics]](候选新实例:NOSPLIT 预算)· [[2026-07-08-pr83-forprep-stackgrow-fallout-round]](直接前序:#85 crasher 是 PR #83 合入后首个 master fuzz 撞击;seg2seg valueStackEnd 守卫是本轮 cap 修复的姊妹守卫)· issue #85 · issue #89 · `internal/crescent/frame.go`(gibbousReentryCCallCap)· `internal/gibbous/jit/peroptranslator/call_ic.go`(segToSegDepthCap)· `deep_recursion_test.go`
