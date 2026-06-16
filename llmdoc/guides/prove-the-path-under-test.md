# Guide:证明在测的路径(绿色 ≠ 在测你以为在测的)

> 适用:写差分 / 对拍 / 性能 / IC 快路径 / wasm 快路径 / 错误冒泡类**任何对路径执行做断言的测试**时,以及加 e2e 语料 / 设计验收 oracle 前。
> 来源:六个独立实例聚合的家族纪律——`memory/reflections/2026-06-14-p3-pw5-table-ic-round.md`(inline-proof) + `2026-06-14-p3-pw6-crosslayer-call-round.md`(TierStuck no-op) + `2026-06-15-p3-pw9-acceptance-perf-round.md`(空测 vararg 顶层)+ `2026-06-15-p3-pw10-r3-call-indirect-round.md`(错误路径盲区)+ `2026-06-15-p3-pw10-r1-r2-callinfo-migration-round.md`(基准工作负载错配)+ `2026-06-15-p3-pw10-zerocross-stage3-round.md`(快路径命中盲区)+ `2026-06-16-vs0e-varargs-stack-underflow-round.md`(覆盖度先验证,正向侧)。

**核心断言**:**测试通过 ≠ 在测的路径被走到**。同一段绿色结果可能来自三类「静默替身路径」:① 静态分析挑剔(F1/F2 结构性排除)使被测路径根本没被编译/触发;② 测试 harness 自身跳过(对错误 `Fatalf` / vararg 顶层不升层 / 缓存命中前路径死);③ 被测对象语义等价两条路径(inline 快路径 vs helper 慢路径),输出 byte-equal 但**走的哪条不能从输出反推**。

测试绿、性能数字不动、新机制就位三件套**单独任何一个都不证明在测路径被执行**。必须有**正交于输出**的路径执行证据。

## 1. 反模式三档

| 档 | 形态 | 实例 |
|---|---|---|
| **空测 / 不公平基准** | 测的不是宣称在测的层 / 用相同负载形态对比不可比的两层 | PW9 loop `for` 写顶层 vararg chunk(F1 不升层),实测「crescent==crescent ≈1.0x」推出「memory-resident 根本限制」并准备立**错的**后续里程碑;PW10 R1-R2 bench 把 kernel 包内层函数调 50 次对裸顶层循环,工作负载错配致 loop「慢 20 倍」误读 |
| **静默替身** | 路径有显式 fallback / 等价语义,绿色来自 fallback 而非 happy path | PW5 IC inline 与 helper 输出 byte-equal,普通 e2e 区分不了 inline 走没走;PW6 TierStuck 吸收态使 force-all promoteProto 静默 no-op,深 baseline 测试自然 `proto.tier != TierGibbous` 但测试套不抓;PW10 R3 错误路径漂移在全成功语料 difftest 下结构性失明 |
| **覆盖度自欺** | 自行写 11 条语料看似全面,实则远不如已有官方/oracle 测试套 | VS0-e 子步 ⑥ 计划写 11 条 vararg 形态语料,但 `test/luasuite/testdata/vararg.lua`(官方 5.1 vararg 全套)+ `closure.lua`(NeedsArg + vararg + 协程多值 yield/resume 最复杂组合)已经字节级一致通过——**手写语料比官方测试权威性低 N 倍** |

## 2. 反向侧解药(证「路径真被走到」)

针对档 ①/② —— 加**正交于输出的路径执行证据**。三招:

**(a) 毒化 / 哨兵 helper(IC 快路径类)**
被测对象 inline 与 helper 输出等价时,把 helper 改成「无副作用但断言不该到」/「不写目标寄存器」/「返回 error」,使被测路径走到 helper 时**输出可分辨**。例:PW5 inline-proof 测试把 `h_getglobal` 改成毒化助手,inline 命中则 R(A) 写入正确值,inline 漏掉走 helper 时 R(A) 仍是 nil(哨兵不写),断言区分。

**(b) 正向 tier / 命中计数器(升层 / 快路径类)**
对 tier-vs-tier 基准 / wasm 快路径 / inline IC,加**白盒计数器**(`atomic.AddInt64(&fastCallHits, 1)` 等),测试断言计数器单调增。例:PW10 ③b emitReturn 守卫快路径加 `doReturnHits` 计数,`TestPW9_ZeroCross_ReturnFastHit` 经 helper→f 不增计数验快路径命中;PW10 顶层升层加 `TopLevelUplift` 探针(DoReturn 增量证 wasm 入口被走)。也包含**直接断言 tier 状态**:`TestPW9_ForceAllPromoteReal` 断言 proto 真到达 `TierGibbous` 不是 `TierStuck` no-op skip。

**(c) 非空载体 + 路径载体证据(空测类)**
加速路径 ≈1.0x 是红旗不是发现——默认「没走加速路径」。tier A vs B 基准必须**先证 tier B 真被执行**:把 kernel 包进可升层载体(内层函数反复调用而非顶层 vararg chunk),再读数。改 bench 时**先证路径,再读数**。

## 3. 反向侧解药 — 错误路径覆盖

全成功语料的 difftest / 对拍套对错误路径**结构性失明**。harness 在出错时 `t.Fatalf` 则错误路径根本不被 byte-equal 校验。纪律:

- 任何对拍 / 差分套必须**含一例黄金输出本身是错误消息**的语料(`error("..."): chunk:line: <msg>` 字面对比 + traceback byte-equal)。
- 全成功语料绿不构成错误路径正确性的证据——R3 出错点锚定 bug 在 V1-V13 全成功语料下全绿,加错误用例才暴露漂移。

## 4. 正向侧解药 — 覆盖度先验证再补

加 e2e 语料前,**先 grep 既有 oracle 测试套是否已覆盖**,避免冗余:

- 仓内 `test/luasuite/testdata/*.lua` 是 PUC 5.1 官方测试套移植,**覆盖度与官方一致**(vararg / coroutine / closure / errors / events / gc / pm / strings / 14 文件)。
- 加新 e2e 语料前先 `grep -l <feature> test/luasuite/testdata/`——若已含 happy path + 重要边界,**不写冗余语料**,直接以「luasuite 通过」作覆盖度证据(权威性高于手写)。
- 真要补**官方未覆盖的项目特化路径**(如 wangshu IC inline 命中,毒化助手等),`(a)/(b)` 双向断言路径走到。

**例**:VS0-e 子步 ⑥ task 描述列 11 条 vararg 语料,实际 luasuite/closure.lua 已含 `{coroutine.yield(unpack(arg[i]))}`(NeedsArg + vararg + 协程多值 yield/resume + 解包到 _G,5.1 作者本人写的最复杂组合)——不写冗余,直接以「luasuite 14 文件全 PASS」作 vararg 覆盖度证据。

## 5. 触发场景速查

写新测试 / 改基准 / 看一个数字反常时,问自己:

- **看到加速路径 ≈1.0x 或机制就位但数字不动** → 默认「没走加速路径」,加白盒计数器或正向 tier 断言
- **快路径与 helper 输出等价** → 必须加毒化/哨兵 helper 测试,普通 e2e 测不出
- **基准 A vs B 跨结构** → 先证两侧负载形状一致,跨结构的「慢 N 倍」先归因不归因 perf
- **对拍 / 差分套全成功语料绿** → 加错误路径用例,否则 R3 类漂移 bug 结构性失明
- **加 e2e 语料前** → `grep test/luasuite/testdata/` 看是否已覆盖,优先官方套件而非手写
- **写 force-all / 缓存裁决 / IC 命中类** → 同时 (a) 白盒断言「真到达加速 tier 不是 stuck no-op」+ (b) 输入侧也加结构盲区用例(vararg 顶层 / 字符串常量值 / 协程不升层)

## 6. 与本仓其他 guide 的关系

- 与 [[design-claims-vs-codebase-physics]] 构成对偶双防线:那是**实现前**重验设计稿主张,本 guide 是**实现后**证明在测路径。
- 与 [[perf-optimization-workflow]] §1「profile 先行」§3「benchmark 否决门」配:profile 先行决定**做什么**,本 guide 决定「机制就位后基准/测试**真的在测**什么」,数字落地前必过两关。
- 与 [[multi-doc-drafting]] §"主动盘点不确定决策" 同源:都强调「绿色 / 通过」之外的正交证据维度。
