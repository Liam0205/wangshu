---
name: p3-pw9-acceptance-perf-round
description: P3 PW9 端到端总验收轮过程教训:空测陷阱(tier-vs-tier 基准没证明被测层真被走到 → 测成 crescent==crescent 的 ≈1.0x,差点把真实 2.58x 的性能轴判成「memory-resident 根本限制」并立错的后续里程碑)、非空断言测试是解药且这是 PW5 inline-proof / PW6 TierStuck 之后同家族第 3 个实例(跨过提升阈值)、编译期 F7 是无后端注入的历史占位非能力陈述(检查裁决须在条件匹配处重判)、真瓶颈是跨层调用税非 memory-resident 寄存器(原计划 locals 缓存正交治不了)、里程碑级架构改动配 spike 检查绝不盲目重写
metadata:
  type: reflection
  date: 2026-06-15
---

# P3 PW9 端到端总验收轮反思(正确性轴交付 + 性能轴空测翻案)

> 范围:PW9 是 P3 翻译卷的端到端总验收。正确性轴 V1-V13(oracle/crescent/gibbous 三方逐字节)+ V17(4 build)+ V18(-race)+ V14(loop 核 ≥2x)全过;性能轴 V15(geomean ≥1.5x)**未过**,拆到新里程碑 PW10。五提交:`bb39b06`(PW9-a gcPending inline safepoint,回边零跨层)→ `e94a80e`(PW9-b force-all + 层间差分套)→ `f556c19`(性能基准 + V14/V15 实测口径)→ `419e278`(progress 对账)→ `ed0375a`(README 同步)。承 `08-testing-strategy.md` V1-V18 + `02-translation.md` §2.2B。

## 核心教训

### 1. 空测陷阱(vacuous-benchmark):tier-vs-tier 基准不证明被测层真被走到,就是 0 信息——它差点把真实过线的性能轴判成「根本限制」并立错里程碑

PW9 早期一个基准在 loop 核 `for i=1,1000 do s=s+i*2-1 end` 上测得 gibbous ≈ crescent(≈1.0x)。基于这个数,团队推出结论:「寄存器 memory-resident,两层 load/store 流量相同,**单靠 dispatch 消除不足以达 2x,必须上 locals 缓存**」,并准备把性能轴当作一条**根本限制**拆给后续里程碑。`bb39b06` 的提交注脚甚至已经把这条写进了 commit message(「实测纯算术循环 gibbous 仍≈crescent……dispatch 消除不足以达 2x」)。

**这个测量是假的**:那个 `for` 循环写在**顶层 Lua chunk** 里,顶层 chunk 是 vararg,被 F1 检查排除(`02-translation.md` 白名单 / F1 结构性排除),所以它**永远不会升 gibbous**——基准实际在测 crescent vs crescent,≈1.0x 是**构造性的必然**,不是发现。修正后(教训 2),loop 核真实数字是 **2.58x**(crescent 5.61ms vs gibbous 2.17ms),V14 ≥2x 立刻达标。

差一点的代价:一个**本来就过线**的性能轴被叙述成「memory-resident 的根本限制」,并据此立一个**错的后续里程碑**(locals 缓存)——而 locals 缓存与真实瓶颈(跨层调用税,见教训 4)**正交**,根本治不了它。一条假测量几乎同时污染了「现状判断」和「下一步方向」两件事。

**Why**:`≈1.0x` 在一个号称「加速层 vs 基线」的基准里,是一个**自相矛盾的数**——「加速路径和基线一模一样快」本身就该是红旗:要么加速没生效,要么根本没走加速路径。我们却把它当 finding 接受了,因为它**碰巧符合一个听起来合理的先验**(memory-resident 流量相同)。先验越合理,假数据越危险:它不会触发怀疑,反而被当作对先验的确认。基准测的是「两条命名为 A/B 的路径」,但**没有任何机制保证 B 真的被进入**——顶层 vararg 这条 F1 排除,把 B 静默替换成了第二份 A。

**How to apply**:任何「tier A vs tier B」的性能基准,在读数之前**必须先证明 tier B 真的被执行了**,否则读数是 0 信息。把 kernel 包进**可升层的载体**(非 vararg 内层函数,反复调用使其过热度/force-all),而不是写在顶层 chunk。看到加速路径 `≈1.0x` 时,默认假设是「**没走加速路径**」而非「加速无效」——先去证明路径,再谈收益。这与 §4 的「数与模型矛盾时先重导模型」互补:这里是**数与基准前提矛盾**(加速却不快),那里是**数与成本模型矛盾**。

### 2. 非空断言测试是解药,且这是 PW5/PW6 之后同家族第 3 个实例——「绿色 ≠ 在测你以为在测的」已跨过提升阈值

教训 1 的修复不是「再跑一遍」,而是**经一条已证非空的路径重测**:kernel 包进非 vararg 内层函数反复调用(基准注释明写「首调 crescent,二调起 gibbous」),再加一个白盒测试 `TestPW9_ForceAllPromoteReal`——它经**真实公共 force-all 路径**断言 kernel proto **确实到达 `TierGibbous`**(注释:「防层间套退化为 crescent==crescent 假阳性」)。配套 `TestPW9_ForceAllRespectsStructuralGates` 断言 vararg 即便 force-all 也不升层(证 F1 真实检查没被绕)。证非空之后,loop 才量到 2.58x。

这是 P3 内**第 3 个**同家族实例,三者抽象一致:
- **PW5 inline-proof**:普通 e2e(结果正确)不能区分「inline 快路径走了」还是「助手回退走了」,要用**毒化/哨兵助手**让两条路径在断言上可分辨,才能证「跳过了哈希」这个验收口径达成。
- **PW6 TierStuck-on-deep-baseline**:深 baseline 自升进 `TierStuck` 吸收态,后续手工 promote 成 no-op、测试**静默 skip**——绿了,但 gibbous 路径根本没跑。
- **PW9 空测(本轮)**:vararg 顶层 chunk 不升层,层间差分/基准静默退化成 crescent==crescent。

统一原则:**绿色 ≠ 在测你以为在测的**(与 [[test-hardening-round]]「fuzz 目标空转是最危险的虚假安全感」同源)。对差分套和基准一视同仁——你必须**独立证明被测路径真被进入**,不能靠「结果对/没报错/没 skip」反推。三类失败的共同形式:存在一条**静默替身路径**(助手回退 / TierStuck no-op / vararg 不升层),它产出「看起来对」的结果,把真正要测的路径挤掉而不留痕迹。

**这已是第 3 个独立实例,跨过提升阈值**。建议提升为一篇测试类 guide(workname:`prove-the-path-under-test` / 「证明被测路径真被走到」),或并入既有 guide。三个实例提供了一套现成的反模式目录(静默替身路径)和对应解药(毒化助手 / 正向 tier 断言 / 非空载体 + 路径断言)。注:它与 [[design-claims-vs-codebase-physics]] 是**对偶**——那条管「实现前把设计主张对码库 physics 重验」,这条管「实现后证明测试真在测被测路径」;一前一后两道防线,可考虑相邻成篇或交叉引用。

### 3. 编译期 F7 是「无后端注入」条件下算出的历史占位,不是能力陈述——检查裁决必须在条件匹配处重判

force-all 若只绕**热度**检查,会**一个 proto 都升不动**。原因:编译期 `analyzeCompilability` 用一个**临时 throwaway Bridge** 跑 F7(后端 opcode 支持检查),那时 `b.p3 == nil`(没注入任何 P3 后端)→ F7 **恒触发** → 每个 proto 被烧成 `CompNotCompilable + ReasonBackendUnsupp`。运行期重分析被显式延后(`analyze_on.go`:「运行期 considerPromotion 在 P3 注入后重新调 AnalyzeProto 是 P3 完成后的扩展,当前不实现,**留 P3 PR 收口**」)。于是 force-all 即便绕过热度,`considerPromotion` 仍撞 `comp != CompCompilable → Stuck`,层间差分套**又一次空测**(与教训 1 同家族的 vacuity)。

修复:`recheckCompilabilityForce` 在 forceAll 下对**真实注入的后端**重判 F7(`b.checkF7BackendSupport`),同时**保留** F1-F6 结构性排除——后者已烧进 `proto.CompReasons`、不依赖 AST,直接 `ReasonsBitmap(proto.CompReasons) &^ ReasonBackendUnsupp` 取出。一句话:「**绕编译期 F7 占位、不绕真实 F1-F6 检查**」。F1-F6 任一仍置位即留 `CompNotCompilable`(vararg 不升层正是这条保住的)。

**Why**:F7 在编译期算出的裁决,是在「**没有后端**」这个条件下算的——它是那组条件的快照,**不是 ground truth**。一个在条件 X 下算出的检查裁决,被存下来当成永久事实使用,但运行期条件已变成 X'(后端已注入),快照就失效了。`ReasonBackendUnsupp` 这个名字甚至有误导性:它读起来像「后端不支持」的事实陈述,实际只是「**编译期还不知道运行期注入哪个后端**」的占位。危险与教训 1 同根:一个在错误条件下产生的值,被当作权威接受。

**How to apply**:当一个检查/缓存/分析结果的裁决**在与运行期不同的条件下算出**(这里:编译期无后端 vs 运行期有后端;也包括「分析时缺某依赖」「采样时负载不同」等),它存下来的裁决是**那些条件的快照而非真相**——必须在**条件匹配的点重新求值**。重判时要精确区分「占位维度」(条件相关、须重算,这里 F7)与「结构维度」(条件无关、可信赖,这里 F1-F6 已 AST-free 烧进字段),只重算前者、保留后者,既不沿用陈旧占位、也不丢掉真实排除。

### 4. 真瓶颈是跨层调用税,不是 memory-resident 寄存器;原计划的 locals 缓存正交、治错了成本

经修正后的基准,真实画面铺开:loop **2.58x** / mixed **1.60x**(两个计算密集核,过线),但 table **0.68x** / call **0.14x**(慢了 7 倍)——**调用密集 / 表密集核反而退化**。根因:gibbous→gibbous 调用经 `h_call` 是一次**双跨层**(Wasm→Go→Wasm,PW0 spike 实测 ~143ns/次,合 ~390ns/调含帧设置);一个被调 30 万次的小叶函数 = 纯边界税 ~43ms。这正是 `must/design-premises` 前提一/前提二**边界成本物理**的实证(`08` §5.1.2 摊销模型 k·T_cross):计算密集核兑现 dispatch 消除收益,调用密集核被边界成本吞没。

而早期(教训 1)定下来要立的 PW10「locals 缓存」(`02` §2.2B)**根本治不了这个**:它针对的是 memory-resident 寄存器的 load/store 流量,与**调用税正交**。把它当性能轴的解,是把火力对准了**错误的成本来源**——因为成本来源本身是被假测量(教训 1)误判的。一旦真实 profile 摊开,瓶颈从「寄存器流量」移到了「跨层调用」,药方也得跟着换。

**Why**:`≈1.0x` 的假数据(教训 1)不仅误判了**幅度**,还误判了**成本结构**——它让我们以为唯一变量是 dispatch/寄存器流量,于是「locals 缓存」成了顺理成章的下一步。真实的 per-kernel 分布(loop 快、call 极慢)才暴露出成本是**双峰**的:计算核已经赢,调用核在亏。这条印证了 [[design-claims-vs-codebase-physics]] §1/§3 的边界成本物理在**运行期实测**层面的兑现——前提里写的「per-item 跨界被边界成本吃光」不再是纸面预期,而是 call 核 0.14x 的实测数。

**How to apply**:当一个性能数与你的成本模型矛盾时,**先从 profile 数据重新推导模型,再承诺任何修复**——别让「看起来合理的下一步优化」(这里 locals 缓存)绑架方向,它可能瞄准的是**错误的成本**。尤其当原始模型本身是从一个可疑测量(教训 1 的空测)推出来的:模型错了,基于它的所有「显然修复」都得重审。把成本**按 kernel 形式分桶看**(计算密集 / 调用密集 / 分配密集),geomean 一个总数会把双峰结构抹平、掩盖「赢的核」和「亏的核」。

### 5. 里程碑级架构改动,配 spike 检查——绝不盲目多 session 重写一个核心假设未验证的方案

跨层调用税的真解是 **gibbous→gibbous 直接分派**(不经 host)。investigator 确认它被两条**码库 physics 事实**挡住:(a) 每个 Proto 编译进**自己独立的 wazero module** → 跨 module 调用**必须**穿 host;(b) Lua 调用帧活在 Go 里(`th.cis` 切片)→ 帧设置即便 intra-module 也需要 host,除非 CallInfo 迁进 linear memory(被延后的 VS0-e)。**生死未知数**:wazero 能不能往一个**已实例化的 module 追加函数**?(当前证据:不能,只能整体重编译——与「逐 Proto 增量升层」直接冲突。)

决策:**不盲目开干**。用 spike 检查(对齐 PW0 precedent——PW0 spike 当初闸住了整个 P3)先验核心假设:S-A 实测 intra-module `call_indirect` 成本(目标 <30ns),S-B 验证「增量 module 重编译 + 实例热替换」的生命周期。绿 → 重写;红 → 退回一个**升层启发式**:对**极小叶函数**直接拒升(它们走 crescent 跑 1.0x,消除 0.14x 退化)。

**Why**:一个里程碑级架构重写的成本,主要不在「写」,而在「**写到一半撞上 make-or-break 的墙**」——这里 wazero 增量 module 不可行的话,整个「单 module 直调」方向立刻作废,而你可能已经投了好几个 session 把 CallInfo 往 linear memory 搬。spike 的成本是**一个 session**;盲目重写中途撞墙的成本是**许多 session + 一堆需要回退的改动**。PW0 已经立过这个范式:在确定 wazero call boundary 实测可行之前,P3 一行都没写。

**How to apply**:任何**里程碑规模**的架构改动,若它的**核心可行性假设未经验证**(这里:wazero 能否增量追加/热替换 module),先用一个 spike 把那个**生死假设**单独打掉,再决定全面铺开。spike 必须瞄准 make-or-break 的那一条(不是边角),并**预先写好红灯的退路**(这里:拒升小叶函数的启发式)——这样即便 spike 红了,也是「一个 session 换来一个明确的方向收窄」,而非沉没成本。「重写 vs 启发式」的选择,由 spike 结果裁决,不由先验偏好裁决。

## 其它(方法论纪律,做对了的)

- **把基准 harness 当独立证据先提交,再做大决策**:`f556c19` 单独提交性能基准(连同推翻空测的实测),让「2.58x / 0.14x」成为可复现的、与决策解耦的证据物,而非夹在某个实现 diff 里的口头数字。
- **把矛盾测量主动上抛给用户,而非在陈旧前提上静默推进**:用户**早先已定下来**「先交正确性,性能拆后续」。修正后的数据(性能轴其实**过线** loop 2.58x、真瓶颈是调用税不是 memory-resident)与那个决策的**前提**矛盾时,把它**显式上抛**,用户据此**反转了决策**(从「memory-resident 根本限制、locals 缓存」改为「跨层调用税、PW10 spike 先行」)。纪律:当新数据动摇的是一个**已定下来决策所依赖的前提**(而非决策本身),要把前提的动摇显式交还决策者,不能拿旧决策当挡箭牌继续跑。

## 验证

正确性轴:4 build(default/profile/p3/p3+profile)+ -race(V18)+ oracle/crescent/gibbous **三方**逐字节(V1-V13 各形状 23 核 + 71 种子层间套 + GC stress 层间 + 并发 force-all);非空保证 `TestPW9_ForceAllPromoteReal`(核函数真达 TierGibbous)+ `TestPW9_ForceAllRespectsStructuralGates`(vararg force-all 仍不升层)。性能轴:force-all 入口对比同核 crescent vs gibbous——loop 2.58x ✅(V14)/ mixed 1.60x / table 0.68x / call 0.14x / geomean 0.79x(V15 未过,拆 PW10)。

## 促成的稳定文档更新

- `docs/design/p3-wasm-tier/implementation-progress.md`:PW9 行 ✅ + §11 PW9 对账(PW9-a gcPending inline / PW9-b force-all + recheckCompilabilityForce 绕 F7 占位不绕 F1-F6 / 性能实测推翻空测的方法论修正)+ RW-2/RW-8/RW-10/RW-11 回填 + PW10 立项(消除跨层调用税,spike 检查先行)。
- `README`:当前状态同步 P3 PW0-PW9 全过线 + PW10 立项。

## promotion 候选

- **教训 2**(非空断言:绿色 ≠ 在测你以为在测的)——**第 3 个独立实例,已跨过提升阈值**(PW5 inline-proof / PW6 TierStuck no-op / PW9 空测)。**强烈建议提升**为测试类 guide(workname:`prove-the-path-under-test`),把三类**静默替身路径**反模式(助手回退 / TierStuck no-op / vararg 不升层)及对应解药(毒化哨兵助手 / 正向 tier 断言 / 非空载体+路径断言)聚合为一个判断框架;或并入 [[test-hardening-round]]「fuzz 目标空转」框架。它与 [[design-claims-vs-codebase-physics]] 构成「实现前重验主张 / 实现后证明在测路径」的对偶双防线,可相邻成篇。recorder 定夺新立 guide vs 并入。
- **教训 1**(空测陷阱:tier-vs-tier 基准须先证被测层被走到)是教训 2 在**基准**维度的具体形式,随教训 2 一并进 guide(差分套 + 基准都适用「先证路径,再读数」)。「加速路径 ≈1.0x 是红旗不是发现」这条独立可记。
- **教训 3**(检查裁决在条件 X 下算出 → 须在条件匹配处重判,区分占位维度 vs 结构维度)——更偏 P2/P3 bridge 的具体机制,但「条件相关的缓存裁决是快照非真相」有一般性。**首次样本,暂留观察**;若后续再现(如运行期重分析正式收口、或其它编译期占位被运行期翻案)可考虑提升或并入 design-claims guide 作「**条件维度**」补充(继空间不变式 / 时间维度之后)。
- **教训 5**(里程碑级架构改动配 spike 检查,绝不盲目重写未验证核心假设)——PW0 已是 precedent,PW10 是第 2 次自觉援用。**首次以「教训」形式记录,暂留观察**;若 PW10 spike 真正执行并产生明确红/绿裁决,这条会变成一个完整的「spike-gate 范式」候选,届时提升为工作流类 guide(可并入 [[perf-optimization-workflow]] 或独立)。
- 教训 4(瓶颈实测推翻模型,locals 缓存正交)更偏本轮的具体性能归因,暂留 memory;其「数与模型矛盾先重导模型」的内核已在教训 1/guide §1 覆盖。

## 触发场景

写任何 tier-vs-tier(加速层 vs 基线)的性能基准或差分套时、看到加速路径 ≈1.0x 的读数时(默认「没走加速路径」)、评估一个检查/缓存/分析结果的存储裁决是否仍有效时(尤其它在与运行期不同的条件下算出)、一个性能数与你的成本模型矛盾时(先重导模型再定修复)、为一个架构改动在「全面重写 vs 退守启发式」之间抉择时、或开始任何里程碑规模、核心可行性假设未验证的改动时(先 spike 检查),看这篇。

## 关联

[[p3-pw5-table-ic-round]](inline-proof 毒化哨兵助手,教训 2 同家族第 1 实例 / 边界成本物理)· [[p3-pw6-crosslayer-call-round]](TierStuck no-op 静默 skip,教训 2 同家族第 2 实例 / h_call 双跨层 ~143ns 是教训 4 的成本源)· [[test-hardening-round]](绿色≠在测你以为在测的 / fuzz 目标空转,教训 2 框架奠基)· [[design-claims-vs-codebase-physics]](实现前重验主张——与教训 2「实现后证明在测路径」对偶;§1/§3 边界成本物理是教训 4 实测兑现 / 教训 3 可作「条件维度」候选补充)· [[perf-optimization-workflow]](教训 5 spike-gate 范式候选归属)· `must/design-premises`(前提一/前提二边界成本,教训 4 实证)· `docs/design/p3-wasm-tier/08-testing-strategy.md` V1-V18 · `02-translation.md` §2.2B locals 缓存 · `implementation-progress.md` §11 PW9 对账 / PW10 立项
