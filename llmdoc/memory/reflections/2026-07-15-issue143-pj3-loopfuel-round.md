---
name: 2026-07-15-issue143-pj3-loopfuel-round
description: >
  issue #143 / #144 修复轮(2026-07-15)。PJ3 spec FORLOOP 模板保 idx/limit/step 在
  xmm/d 寄存器里全段内跑,只有 preemptFlag safepoint 出口——而 step budget 无 async
  producer,故 `for i=0,inf` 在 P4 上永远挂住。这是「段内无抢占点 → step-budget 计费
  缺口」不变式家族的第 3 个独立实例(#102 P4 native emit → #135/#140 P3 wasm →
  #143 P4 PJ3 spec template)。修法新增 loopSpill 字段 + loopFuel 段内自减 +
  specLoopFuelCode RAX 哨兵 + Run 派发循环就地恢复(resume 模式,保 xmm 寄存器状态,
  与 #102 同族但多了 xmm spill/reload 维度)。#144 按不可复现 crasher 分诊流程处置:
  headSha 已含 #135/#140 修复,20/20 replay 干净,按 #123 先例降为串行 Go 回归测试。
metadata:
  type: reflection
  date: 2026-07-15
---

# issue #143 PJ3 spec FORLOOP loopFuel 修复轮反思(2026-07-15)

> 范围:issue #143(PJ3 spec FORLOOP 模板计费缺口)+ #144(不可复现 crasher 分诊)。
> 第 3 个「段内无抢占点 → step-budget 计费缺口」不变式家族实例。

## 任务

PJ3 spec FORLOOP 模板在 P4 上把循环 idx/limit/step 保存在 xmm/d 寄存器,整个循环
在段内执行,仅 preemptFlag safepoint 出口。step budget 没有 async producer 写
preemptFlag,因此 `for i=0,inf` 在 P4 上永远挂住(解释器立即报 "instruction budget
exceeded")。

## 期望与实际

- 期望:按 #102(P4 native loopFuel)/ #135/#140(P3 wasm loopBudget)已有先例,应
  能快速复刻到 PJ3 spec 模板。
- 实际:根因 `grep loopFuel pj3_template.go` = empty 立刻确认,但 PJ3 spec 模板与
  #102 的 per-op native emit 路径是 P4 内部两条独立的 emit 通道——#102 修了一条,
  另一条从没被查。实现上比 #102 多一个维度:xmm 寄存器状态不能跨 host 往返存活,
  需要 spill/reload 到 jitCtx scratch 字段。

## 修复要点

- **数据结构**:`jitCtx.loopSpill0/1/2`(struct 尾部追加,无偏移位移)三个 64-bit
  字段,存放 xmm 寄存器的 idx/limit/step。
- **emit 侧(amd64)**:新原语 `EmitMovsdR15DispFromXmm` / `EmitMovsdXmmFromR15Disp`
  (REX.B for r15 addressing)+ `EmitSubDwordR15DispImm8`;arm64 对称:
  `EmitStrDtToXnDisp` / `EmitLdrDtFromXnDisp`(64-bit FP STR/LDR, V=1)。
- **协议**:`specLoopFuelCode` RAX 哨兵(不同于 `specDeoptCode`),段退出后
  `p4Code.Run` 派发循环识别并就地 spill→host LoopPreempt 计费/重灌/检查→reload→
  resume(从 `codePage + resumeOff` 重入段)。
- **callJITSpec 切换**:empty-const 模板路径从 `callJITFull` 改为 `callJITSpec`
  (需要 r15/x27 for fuel + spill 槽)。

## #144 分诊

headSha = 当前 master HEAD(已含 #135/#140 P3 loop-budget 修复),精确 replay 20/20
干净,heavy 工作量 ~670ms/Run。按 [[unreproducible-crasher-triage]] + #123 先例:
判为不可复现、入库为串行 Go 回归测试(非 `testdata/fuzz/` corpus)防并行 replay 放大。

## 教训

### 教训 1:计费缺口不变式家族已有 3 个独立实例,且 #143 证明该家族的维度不只是「跨 tier」还包括「tier 内跨通道」

家族:
1. **#102** — P4 native per-op emit 路径(FORLOOP / 负 sBx JMP)
2. **#135/#140** — P3 wasm 回边(FORLOOP / while JMP / repeat JMP)
3. **#143** — P4 PJ3 spec template 路径(FORLOOP 模板)

#102 修了 P4 per-op native emit 的计费缺口,但 PJ3 spec template 是 P4 内部的另
一条独立 emit 通道——两者生成完全不同的机器码,修一条不自动覆盖另一条。这扩展了
[[2026-07-14-p3-loop-stepbudget-round]] 教训 1「修一个 tier 的计费缺口要同 PR 查
所有 tier」的判据:**不只跨 tier,还要跨同 tier 内的独立 emit 通道**(P4 目前至少
有 PJ3 spec template / PJ5 SELF inline / PJ10 per-op native 三条通道)。

与 [[cross-backend-semantic-fix-sweep]] 已记录的「后端 × 通道」维度(#117/#118 NaN
FORLOOP 首次记录 spec-template vs per-op 双通道)完全吻合:语义修复要扫「后端 ×
通道」的全交叉,计费修复也一样。

### 教训 2:xmm-register 循环的 resume 需要显式 spill/reload,这是 #102 没有的新维度

#102 per-op native emit 路径的 idx/limit/step 活在值栈(内存),自然跨 host 往返
存活。PJ3 spec 模板把它们提到 xmm 寄存器,跨 host round trip 会被破坏——因此
resume 模式(区别于 deopt-to-interpreter)需要在 fuel 耗尽时 spill 到 jitCtx
scratch 字段、host 处理完后 reload 再重入。这是「段内 register-resident 状态跨 host
round trip」的首个实例,如果后续有更多 register-resident 快路径(例如未来 PJ10 的
寄存器分配器)也需要中途退出再恢复,同样需要 spill/reload 机制。

### 教训 3:empty-const 路径需要从 callJITFull 切到 callJITSpec,这是非显而易见的前置依赖

fuel 机制读 r15(jitCtx),callJITFull 也加载 r15,表面上似乎可以直接用。但 fuel
spill/reload + specLoopFuelCode 哨兵返回 + Run 派发循环的 resume 逻辑全依赖 spec
trampoline 的协议(RAX 哨兵 + codePage+resumeOff 重入)。empty-const 模板之前走
callJITFull 是因为它不需要这些——加了 fuel 就必须切。这种「基础设施假设随功能增长
而需要升级」的 pattern 在这个代码库出现过多次(PW6 `$base` 刷新 / #80 `vsBase`
重算),判据:给段内代码加任何新的「中途退出再恢复」机制时,先确认入口 trampoline
是否支持 resume 协议。

## Promotion 判断

- **教训 1(计费缺口家族 + tier 内跨通道)**:该家族现有 3 个独立实例,且
  [[cross-backend-semantic-fix-sweep]] 已有「后端 × 通道」维度(#117/#118 首记)。
  本轮是该维度在计费(而非语义)方面的首个实例。**暂留 memory 作该家族第 3 个数据
  点**。如果该 guide 下次修订,可以把计费缺口作为「后端 × 通道」维度的第 2 类适用
  对象(第 1 类是语义修复)写进去。
- **教训 2(xmm spill/reload for resume)**:首次样本暂留观察。如果 PJ10 寄存器分配
  或 P5 trace JIT 再撞,可升为 guide 条目。
- **教训 3(trampoline 协议升级)**:首次样本暂留。属 [[design-claims-vs-codebase-
  physics]] §2 家族(基础设施假设随功能增长失效)的又一形式。

## Follow-up

- CI 全绿后合入。
- 下次修订 [[cross-backend-semantic-fix-sweep]] 时,考虑把「计费/抢占不变量跨通道
  对称」作为语义修复对称的姊妹条目写入(三实例已足够支撑)。

## 触发场景

- 给某个 emit 通道修计费/抢占缺口时(教训 1:同 PR 枚举**同 tier 内所有独立 emit
  通道** + 跨 tier 全查,不只查当前通道);
- 给段内 register-resident 状态的快路径加「中途退出再恢复」机制时(教训 2:spill
  到 jitCtx scratch + reload 再 resume);
- 给段内代码加新的 exit-reason / 哨兵返回时(教训 3:先确认入口 trampoline 支持
  resume 协议)。

## 关联

[[2026-07-10-issue102-loop-fuel-round]](P4 native loopFuel,#102,家族第 1 实例) ·
[[2026-07-14-p3-loop-stepbudget-round]](P3 wasm loopBudget,#135/#140,家族第 2
实例) · [[cross-backend-semantic-fix-sweep]](「后端 × 通道」维度) ·
[[unreproducible-crasher-triage]](#144 分诊依据) · [[prove-the-path-under-test]]
(spill contiguity assertion + arm64 comment accuracy,review bot 抓出) ·
issue #143 · issue #144
