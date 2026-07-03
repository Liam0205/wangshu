---
name: issue45-issue39-round
description: issue #45(P4 amd64 exit-reason 收尾)+ issue #39(P3 helper 密度收益检查)双收口教训:PR #53 把 amd64 emit_ops_amd64.go 里最后的段内 mmap→Go shim 调用(EQ 直调 / LT/LE 非数字回退 / arith 慢路径 / LEN / CONCAT / SELF / TAILCALL / CLOSURE / CLOSE / TFORLOOP 遗留 emitter)全部迁到 exit-reason 协议(EQ 复用 HelperCompareSlow),删掉 nativeCode.Run 的 defer recover() 兜底(**删除本身就是验证**:shim 面若真的清零,兜底移除后全量测试 + fuzz 保持绿才成立);LEN/MOD/POW 进 opSupported 后 90s fuzz **立刻**抓到 master 上就有的老 bug(seed 21c645c46a1268c6,`function f()return{0}end f(f())` → P4 报 SETLIST: not a table 而 P1 正常)——PerOpCode.Run 的「副作用先跑、head-op 后物化」回放模型隐含「head 写与副作用读互不依赖」假设,NEWTABLE 一进 head-op 集合假设就破了,三种破坏形式(NEWTABLE head + SETLIST 副作用报错 / LOADK head + SETGLOBAL 或 SETTABLE 副作用静默存旧值);先用最小 Lua 探针家族(A-setlist/setglob/settable/setupval/call 与 B-scratch/arith)扫全 hazard 面(A 类三处中招,B 类因 head 在副作用之前分类天然安全),AnalyzeShape 预计算 lastReplayPC,任何 pc < lastReplayPC 的 head op 降级为有序副作用 + slotKindReg 读回(第一版 sawDeferredHead 全拒误杀 TestPJ10_SetList,降级才保住既有接受面);回归测试 TestPJ10_DeferredHeadOrdering 断言实际观测值,git stash 验证在未修复 analyzer 上真的失败(prove-the-path 正向验证);PR #55 给 P3 加 PromotionGater 可选接口(WorthPromoting(proto) bool),在可编译性检查之后 try-compile 之前咨询,仅 auto 模式生效 forceAll 绕过保差分测试覆盖,拒绝 → TierStuck 吸收;wasm Compiler 实现 op 构成密度地板 total/helperBound ≥ 7,阈值靠白盒 op 构成统计先行 + 实测迭代(先试 5 fib 密度 6 仍升层输 2.3 倍,调 7 后全部回归内核被拒);实测 nbody auto 89.7→43.2ms(与解释器同等)、fib 24.9→10.9、binary-trees 104→38.4、spectral-norm 40→20.7、heavy 三本 P3 赢面不变;两 PR 均 39/39 CI 全绿 + review bot APPROVE 后 rebase 合并;核心教训家族:接受面/硬件/参数任一维度变化都等于新一轮 fuzz 探索(第三个实例)+ 后端能力与收益按分层接口拆(SupportsAllOpcodes capability + WorthPromoting profitability + MinPromotableLen 尺寸地板,forceAll 绕过收益类保覆盖)+ 推迟执行模型必须审计执行顺序 hazard(先用最小探针家族扫全再选拒绝 vs 降级档位)+ 删兜底本身是主张的测试 + 收益阈值靠实测迭代不要一次定死。
metadata:
  type: reflection
  date: 2026-07-03
---

# issue #45 + issue #39 双收口反思(2026-07-03,P4 amd64 exit-reason 收尾 + P3 helper 密度收益检查)

> 范围:单会话双 PR。PR #53 收 issue #45(P4 amd64 段内 shim 面清零 + exit-reason 协议一统 + LEN/MOD/POW 扩接受面),四个 commit `dad0115`(EQ / LT-LE / arith / LEN / CONCAT / SELF 迁移 + 遗留 emitter 删除)/`3508a6c`(删 nativeCode.Run 的 defer recover 兜底)/`c31261b`(扩面 fuzz 抓到的 PerOpCode 回放乱序 bug 修复)/`6ab9f2e`(LEN/MOD/POW 进 opSupported)。PR #55 收 issue #39(P3 nbody 升层反而慢约 2 倍),给 bridge 加 `PromotionGater` 可选接口 + wasm Compiler 实现 op 构成密度地板(阈值 7)。两 PR 均 39/39 CI checks 全绿 + review bot APPROVE 后 rebase 合并进 master。前情:issue #45 是 [[2026-07-03-issue37-arm64-exit-reason-port-round]] 之后 amd64 侧的对称收尾;issue #39 由 [[p4-beat-p3-opset-round]] 的 45b8b53(safe stdlib alias 追踪)让 nbody 通过 F2-b 检查后暴露——能力(capability)说可以编,收益(profitability)从来没人问。

## 核心教训(按强度排序)

### 1. 接受面/硬件/参数任一维度变化 = 新一轮独立的 fuzz 探索

LEN/MOD/POW 三个 op 进 opSupported 后 90s fuzz **立刻**抓到潜伏的 PerOpCode 回放乱序 bug(seed `21c645c46a1268c6`,最小形式 `function f()return{0}end f(f())` → P4 报 `SETLIST: not a table` 而 P1 正常)。该 bug 在旧接受面下也存在(用 `git stash` 换回旧 opSupported 表在 master 可复现),但之前多轮 120s+ fuzz 都没抓到——不是 fuzz 不够长,是 LEN/MOD/POW 之前不在接受面里,fuzz 引擎从来没有理由把探索预算花在会带出这三个 op 的语料上,回放乱序的头部 op 场景也就一直不被点亮。

这与 [[p4-beat-p3-opset-round]] 教训 4「fuzz 语料保留纪律」中三个 seed 都在扩面时抓到、[[2026-07-03-issue40-arm64-stopbleed-round]] 教训 5「同一 corpus 换 M5 Pro 硬件抓到 amd64 侧 120s/150s 多轮没抓到的 fatal stack overflow」共同构成同一家族的**第三个实例**。

**Why**:fuzz 引擎的覆盖度依赖两类信号——(a)语料的语法结构多样性、(b)所走代码路径的分支覆盖反馈。改一件事就等于改了 (b):

- 扩接受面 = 改分支覆盖反馈的目标集(哪些代码路径此前根本进不去,现在能进去了);
- 换硬件/换架构 = 改 goroutine 调度时序 / 内存布局(ASLR)/ SIMD 指令流,从而改具体触发的分支组合;
- 换 fuzz 参数(-parallel、-race、GOMAXPROCS)= 改并发交织顺序。

任一维度动一下,「已跑过 N 分钟没抓到 → 就是安全」这个直觉都要作废。fuzz 覆盖度不是「时长的单调函数」,是「时长 × (语料 × 接受面 × 硬件 × 参数) 的乘积探索空间的一部分」——空间维度动一格,「已探索过」这个状态就重置了。

**How to apply**:任何时候把新东西塞进 opSupported、换新硬件跑基线、改 fuzz -race/-parallel/GOMAXPROCS 参数,把「跑一轮 60~120s 的相关 fuzz target」当作等价于 `go test` 般的必经步骤,不要归到「专门的 fuzz 里程碑」延后。三个独立实例都在扩探索空间的第一次 fuzz 里就抓到既有 bug——这个动作的成本很低(1~2 分钟),命中率极高。

三个实例的强度已足够跨提升阈值,建议 recorder 评估是否**升 guide**(候选路径:进 [[prove-the-path-under-test]] 作正向侧解药新节「fuzz 探索空间维度」,或与 [[perf-optimization-workflow]] §5「跨机器基线对照」邻接单开一篇「探索空间维度动了就重探」的短 guide)。

### 2. 「能编」与「编了赚不赚」是两个独立问题,后端接口按此分层

issue #39 的表面症状是 nbody auto 升层后比解释器慢约 2 倍(43.5ms → 89.7ms),根因不是「编错了」或「编了跑不好」,是**根本不该编**——nbody advance 是 74 op 里 26 个 helper-bound(约 1/2.8),P3 wasm 每个 helper-bound op 都要跨 host 边界,单次跨界成本高于解释器同一 op 的开销,op 数量再多也追不回来。能力(capability,SupportsAllOpcodes)说可以编,收益(profitability)从来没人问。

修法:给 bridge 加可选接口 `PromotionGater { WorthPromoting(proto) bool }`,在 `considerPromotion` 的可编译性检查之后、try-compile 之前咨询。三条设计约束:

- **仅 auto 模式生效,forceAll 绕过**——差分测试(diff test)覆盖不因收益判断缩水,forceAll 语义仍是「所有能编的都真编一次」,收益门只是 auto 模式的入口过滤;
- **拒绝 → TierStuck 吸收**——判断是静态 op 构成密度,重复问答案不会改变,进入 Stuck 后不再重问;
- **状态机不动**——入口检查层扩展,无新状态、无反向边。

与 [[p4-beat-p3-opset-round]] 教训 3「验收门与 emit 质量对性能贡献相当」的 CALL 密度门(totalOps/callCount ≥ 16)、issue #21 的短 proto 地板(MinPromotableLener,固定 Run 成本 ~111ns > 解释器 78ns 时 tiny proto 拒收)构成**第三个实例**——共同家族名:**接受面收益分层**。每个后端有自己的性能物理特性(mmap+morestack 冲突、跨界成本、固定 Run 开销、per-op helper 密度),bridge 不该把这些内化到自己的状态机里,应该以 per-backend 可选接口族的形式让后端各自回答「我这类形状要不要接」。

三个实例 + 明确的接口族收敛(SupportsAllOpcodes / WorthPromoting / MinPromotableLener 三条同结构 hook),建议 recorder 评估**升 guide**(候选:在 [[perf-optimization-workflow]] 或新写一篇 `backend-capability-vs-profitability.md` 中固化「per-backend 可选接口族 + forceAll 只绕收益类不绕能力类」这条设计约束)。

**Why**:capability 和 profitability 是正交轴——capability 是硬约束(编错就是不对),profitability 是软约束(编对了但净亏)。把两者塞进同一张 opSupported 表会把「shape-independent 能编」和「shape-dependent 净胜」两件事强绑,一旦某 op 的净胜性依赖 shape(IC 类别 / CALL 密度 / proto 长度 / op 构成密度),表就不够表达。差分测试覆盖只关心「所有 capability 上能到的形状 P4/P3 结果一致」,不关心「auto 模式实际会不会走它」——所以 forceAll 该绕过收益类检查、不该绕过 capability 类检查。

**How to apply**:后端要拒收某类 proto 时,先分清楚拒收依据是「编错」还是「编了净亏」。前者进 SupportsAllOpcodes / analyzeShape 拒绝面(forceAll 也不能绕),后者进 profitability hook(仅 auto 生效)。差分测试对拒收面透明,对收益门透明——两个透明性由 forceAll 语义保证,不能混。

### 3. 推迟执行模型(replay/物化分离)必须审计执行顺序 hazard

PerOpCode 的执行模型是「有序回放阶段先跑全部副作用、最后物化 head-op 源」——设计意图是让 head op(如 NEWTABLE / LOADK 类值构造)的求值集中在 pattern 尾部,方便寄存器分配。这个模型隐含一个**从来没被显式写下也没被测试**的假设:「head 写(head op 的目标寄存器)与副作用读(有序回放阶段读的寄存器)互不依赖」。NEWTABLE 一进 head-op 集合假设就破了——`function f()return{0}end` 编成 NEWTABLE(head)+ LOADK + SETLIST(副作用),回放先跑 SETLIST 时表还没物化,直接报 `SETLIST: not a table`。

三种破坏形式(用最小 Lua 探针家族扫出):

- **NEWTABLE head + SETLIST 副作用** → 报错(表不存在);
- **LOADK head + SETGLOBAL 副作用** → 静默存入旧值(完全无报错,只有对比 P1 输出才能发现);
- **LOADK head + SETTABLE 副作用** → 同上,静默存旧值。

探针家族分两类:A 类(副作用**读**推迟写)—— A-setlist / A-setglob / A-settable / A-setupval / A-call;B 类(副作用**写** head 源要读的寄存器)—— B-scratch / B-arith。实测:A 类三处中招(setlist/setglob/settable),setupval/call 因语义上写目标不同不受影响;B 类全部天然安全——head 分类时 head op 排在副作用之前,head 源读在副作用写发生**之前**已完成,依赖方向反过来不成 hazard。

修法(两版迭代):

- **第一版(错):** 加 `sawDeferredHead` 标志,任何 pc < lastReplayPC 的 head op 直接拒绝该 shape。回归测试跑挂:`TestPJ10_SetList`(NEWTABLE+SETLIST,常见的表构造模式)误杀,既有接受面倒退。
- **第二版(对):** `AnalyzeShape` 预计算 `lastReplayPC`(有序回放阶段最后一个 pc),任何 `pc < lastReplayPC` 的 head op **降级**为有序副作用 + `slotKindReg` 读回。这个降级机制与 CALL / SELF / CLOSURE 结果的既有处理方式一致——它们本来就是「先执行、结果落 reg、后续读 reg」,把 NEWTABLE / LOADK / 等 head op 走同一条降级路径就行。无副作用形式的 head(比较 diamond / and-or diamond)因为没有物化步骤,继续走拒绝路径。

回归测试 `TestPJ10_DeferredHeadOrdering` 断言**实际观测值**(不只是「无错」——静默存旧值就是无错;必须断言存进去的值确实是新写入的那个),并用 `git stash` 换回未修复的 analyzer 验证测试在旧代码上**真的失败**(prove-the-path 正向验证)。seed 入 corpus,新一轮 120s fuzz 绿。

**Why**:任何「A 阶段先跑、B 阶段后跑」的推迟执行模型,只要 A 阶段与 B 阶段有共享状态(本例是寄存器),就必然存在跨阶段数据依赖 hazard。设计时如果只考虑了「哪些 op 适合当 head」(容易分类),没考虑「head op 的执行时机在语义上是否可以推迟」(依赖分析),就会在第 N 次扩 head op 集合时踩到。这不是 PerOpCode 独有——所有 delayed materialization / lazy evaluation / batched write-back 模型都同族。

**How to apply**:给推迟执行模型加新 op 到「延后」集合前,先列 hazard 面表格:

- 谁写(head op 目标) × 谁读(副作用阶段读的位置) → 若交集非空,是 read-after-write hazard 候选;
- 谁读(head op 源) × 谁写(副作用阶段写的位置) → 若交集非空,是 write-after-read hazard 候选;
- 谁写 × 谁写 → write-after-write hazard 候选。

用最小 Lua/输入探针家族**先扫全**每个候选的实际是否命中(而不是一个个理论推导),再决定拒绝(整类不接)还是降级(走既有的「先执行落 reg」通道)——**第一版全拒会误杀既有接受面,降级才是对的**(现网既有 shape 数量大于新 hazard 面数量,拒绝的成本远高于降级的实现成本)。

### 4. 删兜底代码本身是主张的测试

`3508a6c` 删掉 `nativeCode.Run` 的 `defer recover()` 兜底,判断依据不是「代码整洁性」,是**「段内 shim 调用面清零」这个主张的测试**——历史 recover 的价值是接住段内 mmap 调 Go 时 stack unwinder 撞死等崩溃(见 [[p4-pj10-native-round]] 教训 1),shim 面清零后这类崩溃的触发点全没了;但只要 recover 还在,「shim 面真的清零了」这个主张就没被验证过——保留 recover 会把未来任何真 bug 吞成静默 error,与差分测试的初衷相反。

删掉后全量测试 + fuzz 在无兜底状态下保持绿,才是主张成立的证据。arm64 的 recover 保留——其遗留 shim emitter 还在,留 issue #37 followup 一起清。

**Why**:兜底代码有两种角色——(a)真的防护(某类崩溃在生产上会真的发生,兜底是有效的最后一道防线),(b)历史遗迹(触发条件已经在上游被消除,兜底再也不会真的接住任何东西)。两种角色**代码上看不出差别**,只有把兜底删掉、跑测试和 fuzz 观察是否有东西真的被接住过,才能区分。删除是低成本的确认动作;保留则永远保留「主张未验证」的不确定性。

**How to apply**:任何 `defer recover()` / try-catch / fallback path,只要上游的触发条件被认为已消除,就把它作为「主张验证」候选清单登记;每收敛一类上游触发条件,就删一次对应的兜底,跑完整测试 + fuzz 观察。如果绿灯,主张成立、代码整洁;如果爆了,说明主张不成立、需要重新定位触发点(比删掉之前藏着更好——爆点会给你堆栈)。这是一次性的低成本主张验证,不是持续维护成本。

### 5. 收益阈值靠白盒统计先行 + 实测迭代,不要一次定死

`WorthPromoting` 的密度阈值(total op / helper-bound op)从 5 迭代到 7:

- 先写临时 in-package 测试统计五个 benchmark-game 脚本的 per-proto op 构成:nbody advance 74/26≈2.85、fannkuch 93/30≈3.10、fib 12/2=6.0、spectral-norm 内层 20/3≈6.67、heavy_arith / heavy_floatloop 内核零 helper-bound op(密度无穷大);
- 阈值 5:fib(密度 6)漏网,升层后输约 2.3 倍;
- 阈值 7:fib 被拒,全部回归内核都被拒收(密度均 ≤ 6.7),全部 P3 赢面内核(零 helper-bound op → 密度无穷,提前返回路径)不受影响。

关键顺序:**先统计再选阈值**,不是「随便选个数字跑一遍看结果」。统计给出的是这批工作负载的密度分布,阈值选在分布的哪个位置决定拒绝/接受的分界——选定后再实测确认边界样本(fib 密度 6.0 在阈值 5 时通过、阈值 7 时被拒)行为符合预期。

**Why**:阈值是一个连续参数,一次定死等价于赌你选的那个点正好切在期望的分类边界上——赌错代价是要么切太紧误杀赢面(阈值 ≥ 8 会把 spectral-norm 6.67 也拒了,而它 P3 赢面 40→20.7ms 真实存在)、要么切太松放过输面(阈值 ≤ 5 fib 漏网)。分布统计告诉你候选阈值的可选区间,实测确认区间内哪个点符合当前工作负载。

**How to apply**:任何性能相关的数值阈值(密度门、长度地板、密度上限、时间阈值等),动手前先跑一次白盒统计给出候选工作负载在这个维度上的实际分布,再从分布上选阈值,再实测边界样本。不要「凭直觉选个 5」——直觉是「同数量级」的确认工具,不是选定工具。

## 其它(较小)

- **PR 分支流水:** master 是保护分支,push 被拒后立即改走分支 + PR。PR #55 期间 master 有新提交(arm64 LEN/MOD/POW 的 issue #37 收尾),rebase 后 `--force-with-lease` 推,是安全的例行操作。
- **review bot 反馈处理:** PR #55 bot 指出注释措辞不准(公式是 `total/helperBound`,注释写成 plain-ops-per-helper-bound-op 造成歧义),纯文字修正一版 commit `5cf4b8e`。measurement 型 comment 的措辞与公式方向一致是硬要求,不是文风偏好。
- **darwin/arm64 README perf 表过期标注:** issue #39 收益门合入后 P3 nbody 数字变了,但当前 CI 没重跑 darwin/arm64 全表,给该栏加「测于收益门合入之前」的过期标注即可,不阻塞合并。
- 用户过程反馈(已入用户级 memory,不重复入 llmdoc):新建 PR 后立即 `make check-pr-ci`;输出用 `2>&1` 全量收集不要 `tail`;等命令(含后台返回)执行完读全部输出再按指令行事。

## 验证

- PR #53 39/39 CI checks 全绿(fmt/lint/build/test/fuzz/conformance ×3 variants 三平台) + review bot APPROVE,rebase merge 进 master(`dad0115` / `3508a6c` / `c31261b` / `6ab9f2e`);
- PR #55 39/39 CI checks 全绿 + review bot APPROVE(含 `5cf4b8e` 注释修正),rebase merge 进 master;
- `TestPJ10_DeferredHeadOrdering` 断言实际观测值,`git stash` 换回未修复 analyzer 上验证测试真的失败(prove-the-path 正向验证);两个 crasher seed 入 `testdata/fuzz/` corpus,新一轮 120s fuzz 绿;
- `TestPJ10_SetList` 等既有回归测试保留通过,第一版全拒方案被回归测试挡下、第二版降级方案通过;
- 实测(Xeon Platinum,`-benchtime=2s -count=3 -cpu=1`):nbody auto 89.7→43.2ms(与 P1 解释器同等)、fib 24.9→10.9、binary-trees 104→38.4、spectral-norm 40→20.7、fannkuch 不变;heavy_arith / heavy_floatloop / heavy_stringrepeat 三本 P3 赢面数字与之前相当(收益门对零 helper-bound op 提前返回,不影响 P3 赢面);micro 三本噪声内;
- P3 forceAll 数字不受影响(收益门仅 auto 生效);P4 不受影响(其 Compiler 不实现 PromotionGater 接口);
- README(中英)perf 表刷新 + `[^p3-gate]` 脚注;darwin/arm64 表加过期标注;
- issue #45 / issue #39 均可 close(auto-close 关键字已在 PR body 用 `Closes #45` / `Closes #39`)。

## promotion 候选

- **教训 1(接受面/硬件/参数任一维度变化 = 新一轮 fuzz 探索)**:第三个实例(前两个 [[p4-beat-p3-opset-round]] 教训 4 扩面时抓到三个 seed + [[2026-07-03-issue40-arm64-stopbleed-round]] 教训 5 M5 Pro 硬件抓到 amd64 长期未抓到的 fatal),已跨提升阈值。建议 recorder **升 guide**——首选补进 [[prove-the-path-under-test]] 作正向侧解药新节(与 §4「覆盖度先验证再补」邻接,视角是「探索空间维度动了就重探」),次选与 [[perf-optimization-workflow]] §5「跨机器基线对照」并置新写一篇短 guide。
- **教训 2(能编 vs 编了赚不赚 分层接口)**:第三个实例(CALL 密度门 + MinPromotableLener + 本轮 PromotionGater),同构接口族已明确收敛(per-backend 可选接口 + 仅 auto + forceAll 只绕 profitability 不绕 capability),建议 recorder **升 guide**——候选新写一篇 `backend-capability-vs-profitability.md`,或补进 [[perf-optimization-workflow]] 作新节。
- **教训 3(推迟执行模型必须审计执行顺序 hazard)**:首次样本(PerOpCode 回放乱序 bug 是首例延后物化模型的 read-after-write hazard 命中),暂留观察。若后续在 P4 arm64 exit-reason emit / 未来任何 lazy evaluation 场景再遇同族,可与本条一并考虑升格。视角是**执行顺序对偶** —— [[design-claims-vs-codebase-physics]] §2 是空间维度(held pointer 跨 grow 失效),本条是时间维度(read/write 跨阶段乱序),两者对偶但结构不同,不建议合并。
- **教训 4(删兜底本身是主张的测试)**:首次样本,暂留观察。规模较小,可能永远不需要单独升 guide,后续遇到同族可直接引本条。
- **教训 5(阈值靠白盒统计 + 实测迭代)**:首次样本,暂留观察。与 [[perf-optimization-workflow]] §1「profile 先行」邻接但视角不同 —— profile 是「找瓶颈在哪」,本条是「参数选在哪」,两者互补。

## 触发场景

- 把新东西塞进 opSupported / 换新硬件跑基线 / 改 fuzz -race/-parallel/GOMAXPROCS 参数时(教训 1:立刻跑一轮 60~120s 相关 fuzz target,不要延后到「专门的 fuzz 里程碑」);
- 某个后端要拒收某类 proto 时(教训 2:先分清拒收依据是「编错」还是「编了净亏」,前者进 capability 拒绝面 forceAll 也不能绕,后者进 profitability hook 仅 auto 生效,差分测试对两类拒收都要透明);
- 给推迟执行模型 / lazy evaluation / batched write-back 加新 op 到「延后」集合前(教训 3:先列 hazard 面表格 R-A-W / W-A-R / W-A-W,用最小探针家族扫全实际命中面,再决定拒绝 vs 降级——第一版全拒容易误杀既有接受面);
- 某段 `defer recover()` / try-catch / fallback path 的上游触发条件被认为已消除时(教训 4:删掉跑完整测试 + fuzz,绿灯即主张成立;不删就是「主张未验证」永远悬着);
- 选性能相关数值阈值(密度门 / 长度地板 / 时间阈值)时(教训 5:先跑白盒统计给出候选工作负载在这个维度上的实际分布,再从分布选阈值,再实测边界样本;不要「凭直觉选个 5」)。

## 关联

[[p4-beat-p3-opset-round]](**直接前序**:45b8b53 引入的 safe stdlib alias 追踪让 nbody 通过 F2-b 检查后暴露 issue #39;教训 3「验收门与 emit 质量对性能贡献相当」是本轮教训 2 的第二个实例;教训 4 fuzz corpus 保留纪律与本轮教训 1 同族)· [[2026-07-03-issue37-arm64-exit-reason-port-round]](**对称前序**:arm64 exit-reason 移植轮,本轮 PR #53 是 amd64 侧的对称收尾——两平台 exit-reason 协议统一后 shim 面清零)· [[2026-07-03-issue40-arm64-stopbleed-round]](教训 5 fuzz 硬件多样性是本轮教训 1 的第二个实例;两者共同支撑升 guide)· [[p4-pj10-native-round]] 教训 1「mmap+morestack 物理不兼容」(本轮删 recover 兜底的物理前提就是这条约束的段内触发点被清零)· [[prove-the-path-under-test]] §4(教训 1 候选升 guide 位置)、§6(诊断侧对偶,与本轮各教训方向一致——都是「用独立证据证明主张」)· [[perf-optimization-workflow]](教训 2 与教训 5 候选升 guide 位置)· [[design-claims-vs-codebase-physics]] §2(空间维度,与本轮教训 3 时间维度对偶但结构不同)· issue #45 / issue #39 auto-close · PR #53 / PR #55 · master commits `dad0115` / `3508a6c` / `c31261b` / `6ab9f2e`(PR #53)+ PR #55 收益门 commits + `5cf4b8e` 注释修正 · `internal/gibbous/jit/peroptranslator/emit_ops_amd64.go`(EQ / LT-LE / arith / LEN / CONCAT / SELF exit-reason 迁移)· `internal/gibbous/jit/peroptranslator/analyzer.go`(AnalyzeShape 加 lastReplayPC + head op 降级)· `internal/bridge/bridge.go`(PromotionGater 接口 + considerPromotion 咨询点)· `internal/gibbous/wasm/compiler.go`(WorthPromoting op 密度地板 = 7)
