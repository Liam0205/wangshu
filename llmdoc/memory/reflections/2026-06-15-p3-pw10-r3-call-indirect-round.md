---
name: p3-pw10-r3-call-indirect-round
description: P3 PW10 R3(call_indirect 直调消除 code.Run 重入)+ R3.5(host helper 改 wazero 零分配 API 消灭 reflection 装箱)续轮过程教训(承 R1-R2):性能 spike 必须量 allocs/op 而非只量 ns/op——S-A 探针量了 dispatch ns(call_indirect 2.5ns vs host 35ns)却从没量 host call 的分配,把 ~143ns 跨界税误归因为「分派延迟」,实为 wazero WithFunc 反射装箱(call 核 1,400,002 allocs/op),让整个 R3 里程碑去追错的机制(收益其实来自正交的 R3.5);出错点锚定错误行号——延迟标注 + 错误路径弹帧的潜伏失配只在「弹帧的帧≠标注的帧」时浮现,raiseGibbous 在失败点就地标注;difftest V1-V13 全成功语料对错误路径结构性失明;perf 里程碑「机制完成≠收益交付」须用 -benchmem 复测
metadata:
  type: reflection
  date: 2026-06-15
---

# P3 PW10 R3/R3.5 消除跨层调用税轮反思(call_indirect 直调 + host helper 零分配 API)

> 范围:**承 R1-R2** [[p3-pw10-r1-r2-callinfo-migration-round]](R1 共享 funcref 表 + R2 CallInfo 迁入 linear memory 已交付,R3 直调收益当时仍欠)。本轮交付 PW10 **R3**——消除 gibbous→gibbous 跨层调用税:把每次 gibbous→gibbous 调用经 `h_call` 做一次完整 `code.Run` **重入**,替换为经 R1 共享 funcref 表 + R2 linear-memory CallInfo 的 `call_indirect` **直接分派**;并意外发现并交付 **R3.5**(真正付清性能的步骤)。6 提交(`6f2712e..1bf9d53`):R3a(`6f2712e` ci-transfer 中转字 + `GibbousCode.Slot` 基建,零行为变更)→ R3b(`4f1002a` `h_callerr` 助手 + `DoReturn`/`DoCall` 3 路 i64 返回基建)→ R3c(`2abdf18` Wasm 侧 `emitCall` 重写 `call h_call`→`call_indirect`)→ **R3c-fix**(`86e39c9` 错误行号回归修复,surprise 1)→ **R3.5**(`1bf9d53` host helper `WithFunc`→`WithGoFunction` 消灭反射装箱,surprise 2 = 真正的收益)。中间夹一个 `836a3c9`(untrack 瞬态锁文件)。承 `04-trampoline.md` 跨层机制 + PW9 教训 4「真瓶颈是跨层调用税」+ `09-perf-roadmap` PW10 R3 立项。

## 核心教训

### 1. 性能 spike 必须量「每操作分配数」(allocs/op),不只量「每操作耗时」(ns/op)——只量时间维度会把成本归因到错误的机制,让整个里程碑追错的东西

这是本轮**头条**。PW10 的 Phase 0 spike(R1-R2 轮的 `spike/p3indirect/`)用 **S-A** 探针实测:intra-module `call_indirect` ≈**2.5ns**,裸 host 跨界 ≈**35ns**——据此裁定「跨层调用税 = ~143ns 的 host 跨界(分派延迟)」,R3 的全部使命就是用 `call_indirect` 替掉 `h_call` 里的 `code.Run` 重入来消除这个延迟。R3 做完且**逐字节一致**后,我重新跑调用核基准——它**纹丝没动**:gibbous 仍比 crescent **慢 ~6 倍**(115ms vs 19ms,与 R3 前的 117ms 基本一样)。一个 memprofile 立刻揭穿:调用核做 **1,400,002 allocs/op**(crescent 是 **2**)。分配**全部**来自 wazero 的 `callGoFunc → reflect.Value.call → reflect.unsafe_New/reflect.New`——即 wazero 的 `WithFunc` host-function 注册**用反射**,每次 host 跨界把每个 i32 实参**装箱成 `reflect.Value`**。spike 归给「边界」的 ~143ns,**主导项是 per-call 反射分配,不是分派延迟**。

R3.5 的修复与 R3 **正交**:把全部 25 个 host helper 从 `WithFunc`(反射)改成 `WithGoFunction(api.GoFunc(func(ctx, stack []uint64)))`(零反射,实参经 `api.DecodeI32` 从 `stack[i]` 解、结果写 `stack[0]`)。分配立刻对齐解释器:loop 2602→2、table 1.6M→6810、call 1.4M→2;**每个核都翻面**:loop 2.65x ✅、table 1.26x ✅(原 0.64x)、call 0.49x(原 0.17x,6x→2x)、mixed 1.14x ✅(原 ~0.68x)。R3 仍是**正确且必要**的(它确实拿掉了 `code.Run` 重入,残留 call <1x 现在是真实的 h_call+h_return 双跨界,留给 R4/Option B),但**头条收益来自 R3.5**——只在 R3「完成」后用 `-benchmem` 复测才发现。

**Why**:一次 host 跨界的成本有**两个独立维度**——分派**延迟**(ns:跳转 + 边界握手)和分派**分配**(allocs:每次调用堆分配多少)。S-A 探针只量了延迟维度(2.5ns vs 35ns),从未量过 host call 的 allocs/op。两个维度里,**分配维度被完全漏看了**,而它恰恰是主导项(反射装箱 1.4M allocs/op vs 解释器 2)。一个只量「每操作时间」、不量「每操作分配」的性能 spike,会把一个**本质是分配开销**的成本,误归因为**分派延迟**——然后让一整个里程碑(R3)去攻打那个被误判的机制(用 `call_indirect` 省分派),而真正的成本来源(反射装箱)它**一行都没碰**。注意这与 [[p3-pw9-acceptance-perf-round]] / [[p3-pw10-r1-r2-callinfo-migration-round]] 的**空测/不公平基准家族相邻但不同**:那个家族是「**路径根本没被走到**」(vararg 不升层、wrapped×50 vs bare 工作负载错配)——0 信息;本轮路径**走到了**、spike 数**是真的**(call_indirect 确实 2.5ns)——问题是**只量了那条路成本的一个维度**,漏掉的另一维度才是大头。前者是「测了假路径」,后者是「真路径,量错了维度」。

**How to apply**:做性能 spike / micro-benchmark 时,**`-benchmem` 是默认而非可选**——同时报 ns/op 和 allocs/op,把分配维度与延迟维度并列看。尤其当被测机制跨越一个 FFI/host/reflect 边界(wazero `WithFunc`、cgo、`reflect.Value.Call`、interface 装箱)时,**默认假设它在每次调用堆分配**,在 spike 阶段就用 memprofile 把分配量钉死,而不是只比较 ns。判据:当一个 spike 给出「机制 A 比机制 B 便宜 N 倍」的**延迟**结论、并据此立一个里程碑去采用 A 时,先问「这个延迟数字背后,A 和 B 各自的 **allocs/op** 是多少?这条路真实的成本是延迟主导还是分配主导?」。一个延迟探针(S-A 量 dispatch ns)若不配一个分配探针(量 host call allocs/op),它给出的「成本归因」可能把里程碑指向错误的机制。这是对 [[perf-optimization-workflow]] §1「profile 先行」的**延伸维度**:§1 讲的是「立项前跑 cpuprofile 找最大 CPU 项」(找对象),本条讲的是「**spike 量成本时必须同时覆盖时间和分配两个维度**,否则归因会偏」(量对维度);也是 `prove-the-path-under-test` 家族的**邻接补充**——那个家族保证「路径被走到」,本条保证「路径被走到之后,量的是它成本的**全部维度**而非单一维度」。

### 2. perf 里程碑「机制完成」≠「收益交付」——一个加速里程碑做完(且逐字节一致)后,必须用 -benchmem 复测确认收益真的兑现了

R3 把 `code.Run` 重入换成 `call_indirect`、e2e 逐字节一致、每步绿——按一切常规标准它**完成**了。但**收益是零**(call 核仍 ~6x 慢)。真正的收益(R3.5)是在 R3「完成」**之后重新跑基准**、看到数字没动、再挖 memprofile 才发现的——它与 R3 正交,若不复测就会漏掉,而 R3 会被当成「已交付性能」收尾(实际只交付了机制)。

**Why**:一个性能里程碑有两件不同的事——**机制实现**(把 X 换成理论更快的 Y)和**收益兑现**(基准上真的快了)。两者**很容易被默认等同**:Y 理论更快 + 测试全绿 + 逐字节一致 ⟹ 心理上「这步成了」。但「机制就位」只证明**重写正确**,不证明**瓶颈被移除**——如果真实瓶颈在别处(本轮:反射装箱,R3 没碰),机制可以完美就位而基准纹丝不动。这与 [[p3-pw9-acceptance-perf-round]] 教训 4「瓶颈实测推翻模型」同源,但发生在**里程碑收尾**而非立项:那里是「立项时模型错了」,这里是「里程碑做完后,别假设收益自动兑现,去量」。

**How to apply**:任何**以性能为目的**的里程碑,「完成」的定义必须包含**一条 `-benchmem` 复测**,而不止于「机制就位 + 测试绿 + 逐字节一致」。复测要对**目标核**(这里调用密集核)直接量改动前后的 ns/op **和** allocs/op。若数字**没动**(本轮 117→115ms 基本不变),那是红旗——机制就位但收益没来,说明真实瓶颈在别处,**回到 profile 重新找**(本轮挖出反射装箱)。绝不在「机制完成」处把性能里程碑判为「收益交付」——两者是两件事,后者只能由复测的数字证明。

### 3. 出错点锚定错误行号——延迟标注 + 错误路径弹帧的潜伏失配,只在「弹帧的帧 ≠ 标注的帧」时浮现(surprise 1)

R3c 之后,嵌套 gibbous→gibbous 错误报了**错的源码行**和**截断的 traceback**。根因:gibbous 错误被**未标注**地存进 `gibbousPendingErr`,只在顶层 `executeFrom` 用 `currentCI` **延迟标注**。但 R3 引入了**逐层弹帧**(失败的 `call_indirect` 后,Wasm 调 `PopErrFrame` host shim 弹掉孤立的 gibbous callee 帧 + `enterGibbous` 自身错误路径的弹帧,各自精确复刻 baseline `enterGibbous` 的逐层弹帧)——于是错误冒泡到顶层时,`currentCI` **已不再是失败的那一帧** → `annotateError` 读到了**错误帧的行号**。解释器**没有这个问题**:它在错误路径上**不弹帧**(帧活到 pcall 边界才清)。修复:新增 `raiseGibbous(e)` 收口点,在**失败点就地标注**(失败帧仍是 `currentCI` 时)+ 物化 traceback + 标记「已标注」;顶层 `executeFrom` 据标记**跳过重标注**。还顺带改了 arith 族 helper 的 `ci.pc` 从 `pc` 到 `pc+1`(对齐解释器「pc 在执行中已自增」约定),使 `errWithName` 的 `ci.pc-1` 指向失败指令(否则 `:0:` 行号 + 丢 `local 'x'` 名)。净结果:gibbous→gibbous 错误现在**逐字节等于纯解释器**——**严格优于**旧 PW6c crescent→gibbous baseline(那个有截断 traceback、无变量名)。

**Why**:「延迟标注」(把错误攒着,到一个统一的高层点再用『当前帧』补行号/traceback)依赖一个**隐含不变式**:从产生错误到标注的那段路上,**「当前帧」一直是产生错误的帧**。解释器满足这个不变式(错误路径不动帧),所以延迟标注一直工作。R3 引入逐层弹帧后,这个不变式**被打破了**——标注时的「当前帧」已经被弹到了别处。这是一个**潜伏失配**:延迟标注和错误路径弹帧两个机制**各自都对**,只在它们**同时存在**且「弹帧弹走的帧 ≠ 标注要读的帧」时才暴露。错误的**上下文(哪一帧)必须在它仍然成立时被捕获**——一旦弹帧改变了「当前帧」,延迟到弹帧之后再读就晚了。修复的本质是把标注从「延迟到高层」改成「**就地、在失败帧仍是当前帧时**」。

**How to apply**:当一个机制**延迟**消费某个**上下文相关**的状态(这里:错误延迟到顶层才用『当前帧』标注),而另一个机制会在延迟期间**改变那个上下文**(这里:错误路径弹帧改变『当前帧』),二者就是一对潜伏失配——**在上下文仍成立时就地捕获**,别延迟。判据:任何「攒起来稍后用当前 X 处理」的模式,问「从攒到用的这段路上,**X 会不会被改动**?」——若会(尤其错误/清理/回滚路径常会动栈、动游标、动当前帧),把捕获**前移到 X 仍正确的点**。附带:`pc` vs `pc+1` 这类**约定不一致**(本轮 arith 族部分 helper 用 `pc`、部分用 `pc+1`)是预先存在的**潜伏 bug**,只在错误被**锚定到 helper**(而非某个统一高层点)后才有可观测后果——引入「就地锚定」时要顺手核对所有锚定点的约定一致。

### 4. 全成功语料的 difftest 对错误路径**结构性失明**——V1-V13 全绿没抓到 surprise 1,因为 `runWangshuTiered` 在出错时 `t.Fatalf`(语料全是成功用例)

surprise 1 的错误行号回归,**既有的 V1-V13 层间差分套一个都没抓到**。原因:差分 harness `runWangshuTiered` 在脚本出错时直接 `t.Fatalf`——也就是说**整个差分测试语料都是「成功执行」的脚本**,没有一个用例的**预期结果就是一条错误**(带特定行号/traceback)。于是「错误路径的 gibbous vs 解释器逐字节一致」这个维度,**从来没有被差分测试覆盖过**——是一个真实的覆盖缺口。抓到回归的是我自己写的一个**合成 3 层嵌套错误测试**(故意让最内层报错、断言行号与 traceback)。

**Why**:一个差分/差分测试套的**覆盖边界 = 它语料的形状**。如果语料构造上**全是成功用例**(harness 一遇错就 `Fatalf`),那么「错误信息(行号、traceback、变量名)在两个实现间是否一致」这整个维度**在结构上不可达**——不是测了没发现,是**根本没测**。这与 [[completeness-gap-round]]「随机生成器文法跟实现走 ⟹ 对缺特性结构性失明」、[[test-hardening-round]]「fuzz 目标空转」同一家族:**测试套在它语料/目标的形状之外结构性失明**,而「全绿」对这种盲区**零信号**。错误路径尤其容易落进盲区,因为「正常跑通」的语料天然不含「预期是一条错误」的用例。

**How to apply**:差分/差分测试套若 harness 对错误用例 `Fatalf`(语料全成功),要**显式补一类「预期结果就是错误」的差分测试用例**(断言两个实现产出**逐字节相同的错误**:行号 + traceback + 变量名),否则错误路径的一致性是结构盲区。判据:看一个差分测试套时问「**有没有一个用例的黄金输出本身是一条错误消息?**」——若没有,错误路径没被差分测试,任何只在错误路径浮现的回归(本轮 surprise 1 正是)都不会被它抓到。这条强化 `prove-the-path-under-test` 家族:不仅「加速路径要被走到」,**错误路径也要被差分测试语料覆盖**——「全成功语料全绿」不证明错误路径一致。

## 其它(较小的过程点)

- **用户在「先修零分配 helper」与「先做 R4」之间显式选了前者**:R3.5 finding(call 残留 0.49x 是真实 h_call+h_return 双跨界、R4/Option B 的靶子)摆给用户后,用户选择**先把零分配 helper 修掉**(R3.5)而非直接上 R4——承 PW9「把矛盾测量上抛给决策者」纪律,收益正交项优先于结构性下一步,由用户定下来。
- **memory project 文件随结论修正保持更新**:`project_pw10_r3_mechanism.md` 在 surprise 2 推翻 spike 前提后被更新为**修正后的结论**(税的主导项是反射装箱不是分派延迟),不留陈旧判断。
- **`git add -A` 误扫瞬态锁文件**:`.claude/scheduled_tasks.lock` 被 `git add -A` 卷进暂存又 untrack(`836a3c9`)——本码库约定是**按文件名逐个 add**(见 [[issue1-api-gap-round]] 单域物理隔离手法),`-A` 会把 stray 文件一起扫进来,小过程提醒。

## 验证(每子里程碑)

R3a/b/c 各步:e2e 逐字节一致(HappyPath 经 `indirectCalls` 计数器**实证 indirect 路径真被走到** = `prove-the-path` 纪律、ErrorByteEqual、BaseRefresh 经 growStack)+ 4 build + `-race`。R3c-fix:合成 3 层嵌套错误测试断言行号 + traceback + 变量名**逐字节等于纯解释器**(补上全成功语料 difftest 的错误路径盲区)。R3.5:`-benchmem` 复测——allocs/op 对齐解释器(loop 2602→2 / table 1.6M→6810 / call 1.4M→2),四核全翻面(loop 2.65x / table 1.26x / call 0.49x / mixed 1.14x)。**call 核残留 0.49x(<1x)是真实 h_call+h_return 双跨界,留给 R4(消 h_return)/ Option B(inline frame-build 消 h_call)。**

## promotion 候选

- **教训 1**(性能 spike 必须量 allocs/op 不只量 ns/op)——**本轮头条,强 promotion 信号**。它与 [[perf-optimization-workflow]] §1「profile 先行」**相邻但是新维度**(§1 找对象 / 本条量对维度),也与 `prove-the-path-under-test` 家族**相邻但不同**(那个家族是「路径没走到 = 0 信息」,本条是「真路径走到了,但只量了成本的一个维度、漏了主导的分配维度」)。建议**新增一节进 [[perf-optimization-workflow]]**(workname:「spike 须量时间 + 分配双维度,跨 FFI/host/reflect 边界默认每调用堆分配」),或作 §1 的「量对维度」补充。这是**首次样本但信号极强**(一个漏量直接让整个 R3 里程碑追错机制),recorder 定夺立节 vs 暂留。
- **教训 2**(机制完成 ≠ 收益交付,perf 里程碑须 `-benchmem` 复测)——与教训 1 配对,是「里程碑收尾纪律」。可与教训 1 同节进 [[perf-optimization-workflow]],或并入 [[p3-pw9-acceptance-perf-round]] 教训 4「瓶颈实测推翻模型」作「收尾侧」对偶(立项侧 vs 收尾侧)。
- **教训 3 + 教训 4**(出错点就地锚定上下文相关状态 / 全成功语料 difftest 对错误路径结构性失明)——教训 4 **强化 `prove-the-path-under-test` 家族**(继 PW5 inline-proof / PW6 TierStuck / PW9 vararg 空测 / R1-R2 工作负载错配之后,贡献**新维度「错误路径须被差分测试语料覆盖,全成功语料全绿是盲区」**);教训 3 的「延迟消费上下文相关状态 + 中途上下文被改 = 潜伏失配」可作 [[design-claims-vs-codebase-physics]] 的邻接补充(设计稿『顶层统一标注错误』的抽象,落到『错误路径会弹帧』的码库 physics 时失效——同 §2「固定 token 被 grow 搬走」的结构,只是这里被搬走的是『当前帧 = 标注上下文』)。**教训 3 首次样本暂留观察**;教训 4 可并入 `prove-the-path-under-test` guide 立项时的反模式目录。

## 触发场景

做性能 spike / micro-benchmark 时(`-benchmem` 默认,同时量 ns/op + allocs/op;跨 FFI/host/reflect/wazero `WithFunc`/cgo/interface 装箱边界时默认每调用堆分配,spike 阶段就用 memprofile 钉死分配)、一个 spike 给出「机制 A 比 B 便宜 N 倍」的**延迟**结论并据此立里程碑采用 A 时(先问 A/B 各自 allocs/op、这条路是延迟主导还是分配主导)、一个**以性能为目的**的里程碑「机制就位 + 测试绿 + 逐字节一致」后(别判为收益交付,用 `-benchmem` 对目标核复测;数字没动是红旗 → 回 profile 重找瓶颈)、一个机制**延迟**消费某上下文相关状态而另一机制会在延迟期改变该上下文时(就地捕获别延迟——错误/清理/回滚路径常会动栈/游标/当前帧)、引入「就地锚定」时(顺手核对所有锚定点的约定一致,如 `pc` vs `pc+1`)、看一个差分/差分测试套时(问「有没有一个用例的黄金输出本身是错误消息?」——若 harness 对错误 `Fatalf`、语料全成功,错误路径是结构盲区,显式补「预期是错误」的逐字节差分测试用例),看这篇。

## 关联

[[p3-pw10-r1-r2-callinfo-migration-round]](**直接前序**:R1 共享 funcref 表 + R2 CallInfo 迁 linear memory 是本轮 R3 `call_indirect` 直调的两块基建;R1-R2 轮教训 5 工作负载错配是空测家族第 4 实例,本轮教训 1「量错维度」与之**相邻但不同**——那是路径没走到,这是真路径量错维度)· [[p3-pw9-acceptance-perf-round]](教训 4「瓶颈实测推翻模型 + 真瓶颈是跨层调用税」是本轮 R3 立项依据 / 本轮教训 2「机制完成≠收益交付」是其收尾侧对偶 / 教训 4 错误路径盲区强化其 `prove-the-path-under-test` 候选)· [[p3-pw6-crosslayer-call-round]](`h_call` 双跨层 + crescent→gibbous baseline 的截断 traceback——本轮 R3c-fix 使 gibbous→gibbous 错误严格优于该 baseline、逐字节等于纯解释器 / `enterGibbous` 逐层弹帧是 R3 复刻 + 破坏延迟标注不变式的来源)· [[p3-pw5-table-ic-round]](inline-proof 毒化哨兵 = `prove-the-path` 第 1 实例;本轮 HappyPath 的 `indirectCalls` 计数器一样的「实证路径被走到」)· [[completeness-gap-round]](随机生成器对缺特性结构性失明——本轮全成功语料对错误路径失明是同家族)· [[test-hardening-round]](fuzz 目标空转 = 结构盲区家族奠基)· [[design-claims-vs-codebase-physics]](设计稿抽象落到码库 physics 失效——本轮教训 3「顶层统一标注」抽象落到「错误路径弹帧」physics 失效是其邻接)· [[perf-optimization-workflow]](§1 profile 先行——本轮教训 1「spike 量时间+分配双维度」是其新维度延伸,教训 2「机制完成≠收益交付」是收尾纪律候选)· `must/design-premises`(前提一/前提二边界成本——本轮揭示边界税主导项是反射装箱而非分派延迟,是其更精细的运行期实证)· `docs/design/p3-wasm-tier/04-trampoline.md`(跨层机制)· `internal/crescent/gibbous_host.go`(`raiseGibbous` 就地标注 / `DoCall`/`DoReturn` 3 路 i64 返回)· `internal/gibbous/wasm/helpers.go`(`WithGoFunction` 零分配 host helper)· `spike/p3indirect/`(S-A 量了 dispatch ns 却漏量 host call allocs/op)
