---
name: pr95-spill-stack-fuel-round
description: PR #95(fixes #89,2026-07-08)接线 05 §3.4 自管 spill 栈(trampoline 切 SP 到 64 KiB Go 堆 buffer),segToSegDepthCap 16→128;darwin/arm64 真机接力验证全绿。接力中 FuzzAutoPromote 12 秒撞出第三环 bug:step budget 无异步生产者,计费只在 st.preempt()(解释器 call/回边点),seg2seg 段内直调全绕过——cap=128 后深度 ≤128 子树整棵段内跑(~φ^128 次调用零计费),fib(5510)+budget 永久挂死。修法 JITContext.segCallFuel 燃料计数:段内派发递减、归零回退 host 路径恢复计费点;有 budget/context 时灌 4096,否则 1<<31(无预算零感知)。#85→#89→本 bug 三连:每个修复扩大可达状态空间立刻暴露下一个存量 bug。
metadata:
  type: reflection
  date: 2026-07-08
---

# PR #95 spill 栈接线 + 燃料计费轮反思(2026-07-08)

> 范围:分支 `fix/issue89-spill-stack`(amd64 侧 8 commits 由另一会话完成,本会话接力 darwin/arm64 验证 + 2 commits:`3727a7b` 燃料计费、`71fa5fe` 计费边界)。

## 任务

PR #95 在 amd64 上完成了 issue #89 的正道修复(接线 05 §3.4 设计过但从未接线的自管 spill 栈,trampoline 进段切 SP 到 64 KiB Go 堆 buffer,`segToSegDepthCap` 16→128),arm64 交叉编译过但执行验证交 darwin/arm64 真机接力。本会话在 M5 Pro 上跑接力清单。

## 期望与实际

- 期望:跑完四项验证清单(p4 全套 / 深递归回归 / spike / 效率),全绿即收工。
- 实际:清单四项全绿,**但真机 FuzzAutoPromote 12 秒撞出新 crasher**——`fib(5510)` + `SetStepBudget` 在 tiered 路径永久挂死(解释器 50ms 报 "instruction budget exceeded")。这是 #85→#89 链上的第三环:#85 修复(水位线)让深递归活到 NOSPLIT 溢出窗口暴露 #89;#89 修复(spill 栈 + cap 回 128)让段内子树深度膨胀暴露本 bug。

## 根因与修复

**根因**:step budget 没有异步生产者——计费只同步发生在 `st.preempt()`(解释器 call / 回边点),seg2seg 段内直调全部绕过。cap=16 时段内子树最多 ~φ^16 次调用,很快回 Go 计费(同形状 ~12s 后也能报错,旧行为同样错但 fuzz 超时没撞见);cap=128 后深度 ≤128 的子树整棵在段内跑,~φ^128 ≈ 5.6e26 次调用零计费点,预算永不触发。升层 FORLOOP 回边**不受影响**(probe 验证:循环按批次回 Go 正常计费),只有段内 CALL 树不计费。

**修复**(`3727a7b`):`JITContext.segCallFuel` 燃料计数。seg2seg 快路径在栈上界检查之后、depth++ 之前每次派发递减(amd64 `sub dword [r15+off],1; jz skip_seg` / arm64 `ldr-sub-str-cbz` 镜像),归零跳 `skip_seg` 回退 exit-reason host 路径——`enterLuaFrame` → `st.preempt()` 照常计费。host 每次 Run 入口 / dispatcher resume 重灌:有 budget/cancel context 时 `SegCallFuelBudgeted`(4096),否则 `SegCallFuelUnlimited`(1<<31,无预算负载只多一对 dec+jz,基准无变化)。消耗燃料在重灌时记入 `stepUsed`,段内 CALL 与解释器同口径计费。review 边界修正(`71fa5fe`):上一灌是 Unlimited 时 `SegCallFuelSpent` 返 0,防止运行中途 `SetContext` arm budget 后把无预算期的派发一并误扣。

## 核心教训

### 教训 1([[prove-the-path-under-test]] §5「修复即维度重置」第三连环,升级建议)

PR #86 反思首次记录「修复扩大可达状态空间 → 立刻暴露下一个存量 bug」;本轮是同一条 issue 链上的第三环(#85 水位线 → 暴露 #89 NOSPLIT 溢出 → spill 栈修复 → 暴露预算失联)。三连环的共同结构:**每一环都是「某个隐式安全网碰巧被上一环的限制罩住」——限制解除,安全网缺口显形**。cap=16 是 #89 的 workaround,同时碰巧充当了预算计费的兜底(强制每 ~φ^16 次调用回 Go);把它修正为 128,兜底消失。判据升级:**解除一个 workaround/限制时,列出该限制曾顺带提供的全部隐式保障(计费点、抢占点、边界检查频率),逐条确认正式机制仍覆盖**——与 PR #83 教训 3(段内快路径须显式复刻 host 隐式不变式)同族,建议下轮 guide 修订时合并为一节。

### 教训 2(异步信号 vs 同步计费的架构区分,首次样本)

`preemptFlag` 基建(05 §6.3 回边检查点)一直存在,但对 step budget 无用——budget 是**同步计费**模型(每个计费点 `stepUsed++` 后比较),没有任何东西会异步置 preemptFlag。段内执行要感知同步计费模型,要么每次派发出段(性能不可接受),要么**把计费额度预支进段内**(燃料模式:段内只做 dec+jz,溢出才出段结算)。判据:给绕过 host 的快路径设计抢占时,先分清上层模型是「异步信号」(flag 检查够用)还是「同步计费」(需要燃料/配额预支),两者的段内接线完全不同。

### 教训 3(真机接力验证的价值实证)

交叉编译 + CI 矩阵(QEMU linux/arm64 + macos M1)全绿的 PR,真机 M5 Pro 摇测 12 秒仍撞出真 bug——不是 arm64 汇编问题(bug 两 arch 共有),而是真机上 fuzz 吞吐量(~10 万 execs/s)让「有生之年不可达」的挂死形状被随机撞中。**接力清单里的「跑一轮 fuzz」不是可选项**;QEMU 不真模拟 mmap RWX 执行,CI 的 fuzz-smoke 时长也远短于本地摇测。

## 验证

- 接力清单四项全绿(p4 全套 / difftest / conformance / GC stress 3/3 / fib+n-body+HeavyRecursion 基准无回退);
- 新回归测试 `TestI89_BudgetPreemptsInSegment`(p3+p4):无燃料修复时 60s 超时杀,有修复 <100ms 报错(prove-the-path);
- crasher seed `f2165a93dd62892d` 入常驻语料;120s FuzzAutoPromote 摇测干净;
- review bot 三轮 APPROVE,全部小问题(计费边界)已修。

## 触发场景

- 解除任何 workaround/保守限制(cap、阈值、禁用开关)时(教训 1:列出该限制顺带提供的隐式保障,逐条确认正式机制覆盖)。
- 给绕过 host 通道的快路径设计抢占/取消/预算感知时(教训 2:先分清上层是异步信号还是同步计费模型)。
- 跨架构 PR 的接力验证(教训 3:真机 fuzz 摇测不可省,CI 矩阵不等价)。

## 关联

[[2026-07-08-pr86-deep-recursion-nosplit-round]](直接前序:#85→#89 链的前两环)· [[prove-the-path-under-test]](§5 家族:修复即维度重置,三连环)· [[backend-capability-vs-profitability]](教训 2 的通道二分远亲)· issue #89 · PR #95 · `internal/gibbous/jit/jitcontext.go`(segCallFuel)· `internal/crescent/gibbous_host_p4.go`(RefreshJitCtxAddrs 燃料重灌)· `test/regression/deep_recursion_test.go`(TestI89_BudgetPreemptsInSegment)
