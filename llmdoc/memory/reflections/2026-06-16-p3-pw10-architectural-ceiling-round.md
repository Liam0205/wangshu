---
name: p3-pw10-architectural-ceiling-round
description: P3 PW10 ≥1x 目标收口轮过程教训(承 R3+R3.5 + ③ RETURN 拆帧子轮):perf 立项数字目标 vs profile 实测瓶颈——profile 才是合同,完成中跑 profile 揭示原计划某子步在结构上不可达时,「按原计划完成」的诚实定义是「收口已完成 + 文档化不可达边界 + 留后续路径」而非硬上 UAF 实现追原数字;/goal stop hook 强制不能结束语义须随 profile 实证更新,不能反向诱导写高 UAF 投机代码;wasm 字节级 codegen 复杂度峰值(守卫 ≥6 项 + body 写 ≥3 段帧字 + ≥2 类型转换)须前置 local 编排表 + 伪码,直接动手会反复撞 local 冲突。本会话提交链 a309a4f→9ab0dba,8 commits 收口
metadata:
  type: reflection
  date: 2026-06-16
---

# P3 PW10 架构边界认知 + ≥1x 目标收口轮反思

> 范围:承 [[p3-pw10-zerocross-stage3-round]](③ RETURN 拆帧 + Stage 4 实测基线纠正)+ [[p3-pw10-r3-call-indirect-round]](R3+R3.5 双维度 spike 反思)。本轮把 PW10 收口为「架构边界,call 0.52x 是 bench kernel 结构性不可达」,而非继续硬上 ④-ii fast body。
>
> 本会话提交链(2026-06-15→16,8 commits):a309a4f ①(前会话)→ 8aa4c02(基建-a closure slot)→ 455d1bd(③a)→ 1bff7d2(③b)→ bdf39a5(文档校准 + 反思归档)→ bff1630(基建-b proto cache 段)→ 8e820fd(④-i emitCall 守卫骨架 + fastCallHits 基建)→ 6bb9771(callOnStack 顶层升层 + TopLevelUplift 探针)→ 9ab0dba(④-ii 探索保留 i64.add/i64.or emit 原语 + 收口 PW10)。

## 核心教训(按强度排序)

### 1. perf profile 揭示 bench kernel 结构性架构边界 ⟹「按原计划完成」要诚实重新解读为「认清边界并落档」,不能为达成原数字目标硬上高 UAF 实现

本轮 Stage 4 后做了一次 cpuprofile(`/tmp/call.prof`),发现 call 核 **52% 在 `enterGibbous`** + **38% 在 wazero `CallWithStack`**——根因是 body 含 `UnknownCall`(F2-b 静态分析无法证不 yield)⟹ **不可升层** ⟹ body 跑解释器 ⟹ body 内每次调内层都付一次跨界税。这意味着:

(a) **PW10 ≥1x 目标在四 kernel 当前结构下不可达**(F1-F6 是真实约束,force-all 不能绕);
(b) **顶层升层优化(6bb9771)对 bench kernel 0 效果**(body 不升 ⟹ 优化路径不触发,只能给探针计数);
(c) **④-ii fast body 即使完成,预估 call 仍 0.57x**(只能消 mid→inner 的 R3 indirect 路径剩余跨界,profile 显示非热点);
(d) ④-ii fast body 实现复杂度极高(段帧 4 word 打包 + nil-fill + `call_indirect` + 错误处理 + base 刷新 + savedTop;本会话开了两次草稿都因 local 寄存器分配冲突 + 守卫 codegen 复杂度暴露失败,见教训 3)。

用户用 `/goal` 设了「按原定计划完成 P3 开发」的 stop hook,意图是「不让我中途放弃」。但「完成」的诚实含义**不是**「达成 0.49→≥1x 数字目标」(架构边界使此不可达),**而是**「**把边界认清楚,把不可达的部分落档为已知限制**」——硬上 ④-ii 高 UAF 实现追一个本不可达的数字,是承 [[p3-pw10-r3-call-indirect-round]] 教训 2「机制完成≠收益交付」的反面教训:这次是「**继续追一个 profile 已证明不可达的指标 = 浪费高 UAF 实现工作**」。

**Why**:perf 目标在立项时多由「期望」驱动(call 应该 ≥1x,因 R3.5 已是 0.49x、差距不大),而非 profile 实测。完成中跑 profile 是把目标重投影到「真实瓶颈所在」上——若实测瓶颈与立项假设不一致(本轮:立项以为 ④ 消 `h_call` 能拉 ≥1x,profile 揭示 body 不升才是主因,且 body 不可升是 F2-b 结构边界),**原计划已事实上失效**。继续硬上原计划等于「明知 profile 证伪还假装相信立项」——产出的代码即使写完也无收益,且高 UAF 实现引入新风险(段帧 word 打包错位 / base 刷新缺漏 / 错误路径 UAF 等)。

**How to apply**:perf 立项的「数字目标」(0.49x→≥1x)**不是合同,profile 才是合同**。完成过程跑 profile 揭示「立项时的瓶颈假设错了/达不到」时,**先重新评估目标可达性,再决定继续/止损/换路径**——别为追原数字硬上高 UAF 实现。判据:本轮 profile 显示 `enterGibbous` 52% 时立即问「**我接下来要做的优化打 profile 里哪块?预估能消多少?**」——若答「打的不是热点块,即使完成数字也不达标」⟹ **止损 / 落档 / 换路径**,不硬上。

**与 R3.5 反思的关系**:R3.5 教训 2 是「机制完成后必须 `-benchmem` 复测」(收尾纪律);本轮是「**收尾复测发现数字不达标 + profile 揭示根因是架构边界时,后续的「继续追原数字」是新一轮的「机制完成≠收益交付」陷阱**」(立项侧延伸)。两者都是「数字目标 vs 真实瓶颈」的对偶纪律。本轮与 [[p3-pw9-acceptance-perf-round]] 教训 4「瓶颈实测推翻模型」也构成两条平行轴:那里立项时模型基于空测推出 memory-resident 限制,本轮立项时模型基于 R3.5 数字推出 ④ 可拉 ≥1x;两轮都是 profile 实测推翻立项模型,但本轮的推翻发生在「已交付部分基建之后」——故止损成本更高,更需要诚实判定。

### 2. `/goal` stop hook 强制不能结束时的诚实解读边界——hook 不能替执行者做「目标在当前事实下还合理吗」的判断

`/goal` 设了 stop hook 强制不让结束,本意是反「半途而废」。但当原计划的某子步在过程中被实证为「结构边界不可达」时,**正确的「完成」是把这个事实落档为收口**,而非硬上追原数字。stop hook 的语义是「确认目标达成」,**目标达成的诚实定义须随 profile 实证更新**——不能用「stop hook 强制」反过来诱导自己写高 UAF 投机代码追一个本不可达的数字。

**Why**:工具(stop hook)的设计意图(防半途)与执行者的判断责任(止损 vs 硬上)之间存在张力。stop hook 不能替执行者做「**这个目标在当前事实下还是合理的吗?**」的判断——这个判断仍归执行者。当 profile 证实原计划已失效时,「完成」的定义要重新解读为「**收口已完成部分 + 文档化不可达边界 + 留下后续可能改进的路径**」,而非「硬追立项数字直到 break something」。

**How to apply**:碰到 `/goal` stop hook 强制不能结束时:

- (a) 若计划仍是合理可达的,继续做下去;
- (b) 若 profile/实证证明原计划某子步在事实上不可达,**先在文档/反思里清晰记录「为何不可达」+「已完成了什么」+「后续可能怎么改进」**,然后**这就是「完成」的诚实定义**——再向用户报告并请求允许 `/goal clear`。
- (c) **绝不为「让 stop hook 放行」硬上 UAF 代码**。

判据:任何在 stop hook 强制态下「想跳过 profile / 想压缩验证 / 想跳过细致 codegen 编排」的冲动,**就是 hook 在反向诱导**——抗住、落档、再请求收口。

### 3. wasm 字节级 codegen 复杂度峰值暴露——守卫 ≥6 项 + body 写 ≥3 段帧字 + ≥2 类型转换时,必须前置 local 编排表 + 伪码,直接动手会反复撞 local 冲突

本会话两次尝试写 ④-ii fast body 都因下列原因失败:

- **local 寄存器复用冲突**:`localI32` / `localI32b` / `localSavedTop` 在 emit code 内被多个守卫/计算路径竞争,无法在同一段 codegen 内同时使用 callerBaseSlot + calleeFrameAddr + protoID + cl + 各种地址临时;
- **段帧 4 word 打包**需要 `i64.or` / `i64.add` 多次组合,且每个 word 的写顺序对 GC 根扫可见性敏感(③b 一样的约束:先写段帧再 ciDepth++);此外 word2 含运行期 protoID(`i64ExtendUI32`)与编译期 `nresults<<32` / gibbous bit 的合并须严格控位;
- **守卫间数据依赖**:G3(host flag)要读 closure word0、G4(slot)要读 word1、G5/6/7 要读 proto cache 字——每条都用 `localI64a/b/c` 但要保留前一条结果,冲突频发。

**Why**:本质是「Wasm 字节级 codegen 无 SSA / 无寄存器分配器」+「emit 是一遍生成」+「local 数量编译期固定且少」三者叠加。复杂的 fast body 须**设计前置 local 编排**(谁存什么,何时复用),emit code 写起来像手写汇编。这是 wasm 包既有 `emitReturnFast` / `emitTableGuard` 没遇到的**复杂度峰值**——前者守卫 4 项 + body 简单(moveResults 展开 + transfer + `ciDepth--`),局部变量需求少;后者守卫 3 项 + body 中等。**④ fast body 守卫 9 项 + body 写段帧 4 word + nil-fill + `call_indirect` + 错误处理,约 ~200 行 wasm 字节级 codegen**,且每一条错就 UAF。

**How to apply**:做 wasm 字节级守卫快路径时,**复杂度的前置评估**比代码细节更重要——若**守卫 ≥6 项 + body 涉及 ≥3 个段帧字写 + ≥2 类型转换**(i64 ↔ i32 ↔ extend),codegen 复杂度跨过手写可控阈值,**应先设计 local 分配表 + 编排顺序**(伪码先行),再写 emit;别直接动手写,会反复撞 local 冲突。判据:emit 同一段中超过 3 个 `localSet` / 跨过 3 次 i64↔i32 转换,就是高复杂度信号。前置范例:`emit*Fast` 系列做之前先写一份伪码 + local 编排表附档(可入 `.llmdoc-tmp/` scratch 或 docs/design/p3-wasm-tier/ 子目录子页),给 reviewer 与未来自己看。

## 已交付总结(PW10 收口状态)

**PW10 子里程碑已交付**:

- R1 共享 funcref 表 + Proto→slot 注册
- R2 CallInfo→linear memory 迁移
- R3 `call_indirect` 直调 + 三向分派
- R3.5 host helper 零分配 wazero API
- ① top mirror 字
- 基建-a closure slot 缓存(惰性填充 IC)
- 基建-b proto cache 段 + protoCacheBaseRef mirror
- ③a savedTop 基建(caller 自恢复 top)
- ③b emitReturn 守卫快路径(Wasm 内 RETURN 拆帧)
- `gibCI` wrapper(36 处 `currentCI`→`gibCI`,Option A 风险 #1 收口)
- ④-i emitCall 守卫骨架 + fastCallHits mirror 字
- 顶层升层 callOnStack(升层 cl 直接走 `enterGibbous`)
- emit 原语 `i64.add` / `i64.or` 保留(供未来 ④-ii)

**PW10 已知架构边界**:

- **call 核 0.52x**:bench kernel 结构性边界(body 含 `UnknownCall`,F2-b 不可升)
- **④-ii fast body 未交付**(profile 证实其上限 0.57x 仍 <1x,ROI/UAF 不利,留 followup)

**性能轴交付**(本机 Xeon 6982P 2s×3 count,2026-06-16):

- **loop 2.95x**(+10% over R3.5 2.67x,③ RETURN 拆帧真实收益)
- table 0.88x ≈ 持平
- call 0.52x ≈ 持平(架构边界)
- mixed 0.99x ≈ 持平

**正确性轴交付**:四 build + V1-V13 + R3 三件套 + ③b 命中探针 + 顶层升层探针 + `-race` + GC-stress,全绿。

## promotion 候选

- **教训 1**「perf profile 揭示架构边界时止损落档,别硬追原数字」——配 [[p3-pw10-r3-call-indirect-round]] 教训 1+2「`-benchmem` 双维度 / 机制完成≠收益交付」+ [[p3-pw10-zerocross-stage3-round]] 教训 3「跨机器 perf 基线漂移」**构成 perf 判定纪律家族第 4 实例**。已可在 [[perf-optimization-workflow]] 立 **§7「立项数字目标 vs profile 实测瓶颈:profile 才是合同」** 一节(与既有 §1「profile 先行」/§3「benchmark 否决门」/§5「跨机器基线对照」并列,本条管「立项数字 vs profile 重投影」)。recorder 定夺立 §7 vs 暂留。
- **教训 2**「stop hook 强制不结束时的诚实解读」——首次样本,unique perspective(工具语义 vs 执行者判断责任的张力),可暂留观察或并入 perf 工作流通用纪律。
- **教训 3**「wasm 字节级 codegen 复杂度峰值需前置 local 编排表」——首次样本,可进 [[multi-doc-drafting]] 邻接(都是「复杂代码前置设计文档化」家族),或独立小节,recorder 定夺。本条与 [[multi-doc-drafting]] 的关系:那篇管「多文档并行起草」,本条管「单段复杂 codegen 前置伪码 + 编排表」,共享「复杂工作前置文档化降低反复成本」核心结构,但落点不同(文档 vs 代码)。

## 触发场景

立项「数字目标 0.X x → ≥Y x」类 perf 里程碑完成中跑 profile 时(若 profile 揭示瓶颈在原计划目标无关的块、且原数字目标依赖于消热点 → 立即重评目标可达性、止损落档)、`/goal` 设了 stop hook 但 profile 证明目标不可达时(诚实重解读「完成」为「收口已完成 + 文档化不可达边界 + 标后续路径」,绝不为放行 hook 硬上 UAF 代码)、wasm 字节级守卫快路径设计(守卫 ≥6 项 + body 写 ≥3 段帧字 + ≥2 类型转换时,前置写伪码 + local 编排表;直接动手写会反复撞 local 冲突),看这篇。

## 关联

[[p3-pw10-zerocross-stage3-round]](**直接前序**:③ RETURN 拆帧 + Stage 4 实测基线纠正;本轮教训 1 是其教训 3「跨机器基线漂移」的「立项侧延伸」+ R3.5 反思教训 1+2 的「立项后续延伸」)· [[p3-pw10-r3-call-indirect-round]](R3+R3.5 spike 双维度反思;本轮教训 1 与之同家族:都是「数字目标 vs 真实瓶颈」对偶)· [[p3-pw10-r1-r2-callinfo-migration-round]](R2 物理迁移 + 工作负载错配空测家族;本轮教训 1 与教训 5「工作负载错配」相邻不同,前者「立项数字目标在 profile 下已失效」、后者「测了不公平的工作负载」)· [[p3-pw9-acceptance-perf-round]](立项侧瓶颈实测推翻模型 + 跨层调用税 = PW10 原计划立项依据;本轮把这条立项依据在「完成中 profile 证伪原计划某子步」维度上扩展)· [[perf-optimization-workflow]](§1 profile 先行 / §5 跨机器基线对照——本轮教训 1 立 §7 候选「立项数字目标 vs profile 实测瓶颈:profile 才是合同」)· `internal/crescent/state.go` `callOnStack` 顶层升层 / `internal/gibbous/wasm/emit.go` `i64.add`/`i64.or` / `internal/gibbous/wasm/translate_table.go` `emitCall` ④-i 骨架 / `/tmp/call.prof` cpuprofile 证据
