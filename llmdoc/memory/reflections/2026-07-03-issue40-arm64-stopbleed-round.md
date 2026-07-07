---
name: issue40-arm64-stopbleed-round
description: issue #40 arm64 P4 调试轮阶段 1 止血过程教训:issue 把 HeavyArith 慢 ~20x / Fannkuch 慢 3.5-7.4x 归因于「PerOpCode head-op replay」跨界路径,但该路径在 arm64 二进制里根本不存在(build tag 排除)——原止血计划「收紧 arm64 升层接受面」若直接实施会是无效操作,因为接受面本来就在拒收;darwin/arm64 M5 Pro 基线 + force/auto 探针矩阵在 1 小时内定位真实根因:forceAll retry window(前轮 fd055e9 引入,让拒收 proto 停留在 TierInterp 不转 Stuck 以等 IC 预热)组合 forceAll 绕过 HotBackEdgeThreshold 使每条回边都触发 considerPromotion,而拒收结果 pd.Compilable 不写回,导致窗口期内每条回边都重跑 SupportsAllOpcodes→analyzeShape→AnalyzeNative(含 buildCFG 全量分配)——HeavyArith 单次进入 + 2M 回边形状触发 2M 次全量后端分析,实测 1.5 GB/op / 44M allocs/op,cpuprofile 证 recheckCompilabilityRuntime 占 22.38% CPU;修法(f921626)按「重试结果真正可能改变的粒度」去重(每次 entry 一次,IC 预热里程碑 count==1 与 count==HotBackEdgeThreshold 再武装),force 与 auto 对齐(HeavyArith 1125ms→49-51ms,Fannkuch 314ms→3.55-3.97ms);同一 make all 的 fuzz-p4 30s 内顺手抓到第二个既有 bug(bf65839)——walkFuncExpr 建 sub-visitor 隔离信号时把递归展开 guard 也隔离了(应拷贝继承而非留空),导致递归闭包字面量在 known-local 展开路径下无限展开触发 Go 1GB 栈上限 fatal(不可 recover,master 上可复现,arch 无关纯 AST 分析,影响所有 wangshu_profile build);M5 Pro 上 9-80s 抓到该 bug,amd64 上此前 120s/150s fuzz 多轮未抓到,新硬件先跑 fuzz smoke 是廉价高产动作。
metadata:
  type: reflection
  date: 2026-07-03
---

# issue #40 arm64 P4 调试轮阶段 1 止血反思(2026-07-02~07-03,retry-window dedup + fuzz 副产物 sub-visitor guard 修复)

> 范围:issue #40「arm64 P4 调试轮」阶段 1。前情:[[p4-beat-p3-opset-round]] 30 commits 把 amd64 P4 native op-set 经 exit-reason 协议扩面后,三平台 bench-acceptance 显示 arm64 P4 有「主动退化」:HeavyArith 慢 ~20x、Fannkuch 慢 3.5-7.4x + 13.7 MB/op 异常分配。issue #40 原根因假设:「arm64 的 SupportsAllOpcodes 正在接受一些它执行不好的形状,升层后走 PerOpCode head-op replay 逐 op 跨界路径」,止血计划是「收紧 arm64 升层裁决拒收算术密集形状」。用户决定跳过临时收紧、直接做 exit-reason 移植(认为收紧是会被移植覆盖的过渡工作)——本轮实测后证明这个决定歪打正着:**原止血计划(收紧接受面)会是无效操作,因为接受面本来就在拒收**。移植前调查见 `.llmdoc-tmp/2026-07-02-arm64-exit-reason-port-survey.md`。终态:PR #47 三平台 39/39 CI checks 全过 + review bot APPROVE,rebase merge 进 master(f921626/7168648/bf65839)。

## 核心教训(按强度排序)

### 1. 退化归因前先证「被怪罪的路径」真的存在/被执行

issue #40 把 20x 退化归因于「PerOpCode head-op replay」,但该路径在 arm64 二进制里根本不存在——`peropcode.go`/`translator.go` 是 amd64-only build tag,`register_arm64.go` 头注明确 native-only。移植前调查(investigator 子代理)静态追踪时已把这个矛盾显式列为 Gap:代码路径不存在,与 issue 描述的数字矛盾,应 root-cause-first。原止血计划(收紧 arm64 升层接受面拒收算术密集形状)完全基于这个错误归因,若直接实施是无效操作——本轮实测证明 arm64 有效接受面只剩 12 op,**接受面本来就在拒收**,收紧一个已经在拒收的东西不会改变任何数字。

profile-first + force/auto 差分在 1 小时内定位了真实路径:探针矩阵立即暴露 force/auto 不对称(HeavyArith force 1083ms vs auto 49ms vs P1 53.6ms),auto 正常直接排除「arm64 emit 慢」「接受面放坏形状」两类假设(这两类机制会同时影响 force 和 auto),把嫌疑指向 force 专属机制,代码追踪很快锁定 forceAll retry window + considerPromotion 组合(见教训 3)。

**Why**:这是 [[prove-the-path-under-test]] 家族的**诊断侧对偶**。该 guide 现有全部实例都在测试侧——「绿色 ≠ 在测你以为在测的」,证明的是「测试真的在测某条路径」。本条方向相反:issue 描述的数字异常,先要证明「你以为它慢在的那条路径」真的存在且被执行,再动手修。两者共享同一物理基础:**输出(测试绿 / 性能慢)本身不携带路径信息,必须用独立证据(build tag 追踪 / 静态代码追踪 / force-vs-auto 差分)反推路径**。也是 [[perf-optimization-workflow]] §1「profile 先行」的又一确认——不是"先假设瓶颈再优化",是"先证明瓶颈在哪再优化"。

**How to apply**:收到「某条路径导致 N 倍退化」类归因(不管来自 issue、用户描述,还是自己的第一直觉)后,动手修复前先用一个廉价动作验证该路径确实存在且会被执行——grep build tag / 读函数头注 / 跑一次白盒探针。若验证失败(路径不存在,或存在但静态分析显示不该被触发),不要顺着错误归因去修,先重新定位。

### 2. force/auto(或任何模式开关)不对称是免费的根因二分器

emit 质量、接受面这类机制会影响两种运行模式(force 和 auto 都会经过 emit、都会经过接受面判断);只有 force 专属机制(绕开阈值触发的逻辑、retry window 这类只在 force 下才会激活的状态)才会造成 force/auto 数字不对称。跑一轮便宜的 `-benchtime=1x` 探针矩阵(HeavyArith / Fannkuch 各测 force 和 auto)就能把「emit 慢」「接受面放坏形状」这整类假设消掉,把嫌疑收窄到 force 专属代码路径。

**Why**:这是对称性推理的一个具体应用——如果两个模式共享大部分机制,只在少数几个点分叉,那么两个模式之间的数字差异只能来自分叉点,不可能来自共享部分。

**How to apply**:遇到「模式 A 比模式 B 慢很多」类退化,第一件事是跑一次两个模式的最小探针(同 proto、同硬件、`-benchtime=1x` 甚至更小),用数字先把「共享机制」和「模式专属机制」分开,再去共享部分之外找根因。

### 3. 「停留重试」状态 × 高频触发点 = 意外的每事件全量重算

retry window(前轮 `fd055e9` 引入)让被拒收的 proto 停留在 TierInterp 且不写回任何「已拒收」标记(设计意图是给 IC-gated 后端一个机会等 IC 预热后重判);forceAll 让每条回边都成为重入触发点(绕过正常需要攒够 HotBackEdgeThreshold=1000 次的门槛)。两者组合成 O(回边数) 次全量后端分析:`OnBackEdge` 每条回边调 `considerPromotion`,拒收后 `pd.Compilable` 不写回,窗口内每条回边都重跑 `recheckCompilabilityRuntime`→`SupportsAllOpcodes`→`analyzeShape`+`AnalyzeNative`(含 buildCFG 全量内存分配)。HeavyArith 是单次进入 + 2M 回边形状,窗口期内触发 2M 次全量分析,实测 1.5 GB/op / 44M allocs/op;cpuprofile `pprof -peek` 证 `OnBackEdge→considerPromotion→recheckCompilabilityRuntime` 占 22.38% CPU(1.45s/6.48s),`buildCFG` cum 17.44%,`kevent`/`madvise` 38%/16%(分配风暴引发的 GC/调度损耗)。Fannkuch 的 13.7 MB/op 异常分配同根因。

修法(`f921626`):`ProfileData.recheckedAtEntry` 存 `EntryCount+1`(0=从未跑过哨兵),同一进入内的后续回边直接 return;`OnBackEdge` 在每 pc 的 `count==1`(循环体首轮跑完、IC 已被观测——IC-gated 后端最早可能改判的点)与 `count==HotBackEdgeThreshold`(auto 模式自身触发点,IC 到顶)两个升温里程碑清零重新武装。升层时机与修复前完全一致,只去掉窗口内每回边重复分析。修复位置 `internal/bridge/bridge.go`(OnBackEdge 再武装 + considerPromotion dedup)、`internal/bridge/profile.go`(字段 + `resetCountersForReuse` 清零)。3 个新测试(`internal/bridge/state_machine_test.go`):dedup 上限(10k 回边内 SupportsAllOpcodes ≤5 次)/ warm-IC 首回边升层不变(flippingP3 mock)/ entry-4 吸收 Stuck 不变(decliningP3 mock)。

**Why**:retry window 设计时只考虑了「多给几次机会」的语义,没考虑「机会」的触发频率上限——它默认「重试」是低频事件,但组合了 forceAll(绕阈值)之后,「重试」的触发频率跟回边频率一样高。这是一类通用陷阱:给状态机加「停留重试」语义时,重试触发点的频率取决于**触发它的外部信号**(本例是回边),不取决于「重试」这个概念本身看起来应该多低频。

**How to apply**:给状态机加 stay-and-retry 语义时,枚举所有能重新触发这个 retry 的调用点、以及每个调用点的实际触发频率(不是设计意图里假设的频率)、以及每次重试的计算成本,三者相乘就是最坏情况总成本。若某个调用点频率可以被外部开关(本例 forceAll)推到极高,dedup key 必须绑定「结果真正可能变化所必需的最小状态变化」(本例是「同一次 entry」,不是「同一条回边」)。

### 4. 树遍历器的递归 guard 必须沿递归链(动态范围)传递,不能绑在 walker 实例上

`make all` 的 fuzz-p4 在 30s 内(`FuzzP4ForceAllPromote`)抓到 fatal stack overflow,种子 `176669ef9b6894ec`(`local function A()return function()A()end en(` 编译错误变体);120s 复跑抓到最小形式 `648e96a2d9661b88`(`local function A()return function()A()end end`)。在 master 上直接复现确认是**既有 bug**(非本轮引入)。崩溃链:`visitCallExpr` known-local 展开 A(`inlinedKnownCalls` 标记消耗一次展开预算)→ walk A body → 遇到 FuncExpr 字面量 → `walkFuncExpr` 建一个 fresh sub-visitor,继承 localFuncs/safeAliases,**但不继承递归 guard** → sub-visitor 里再遇到 A() → guard 是空的(sub-visitor 视角里 A 一次都没被展开过)→ 再次展开 → 两个展开点交替直到 Go 1GB 栈上限 fatal(编译期不可 recover)。影响所有 `wangshu_profile` build(P3 也中招,因为纯粹是 AST 分析,与 arch 无关)。

修法:`walkFuncExpr` 把祖先 guard 表**拷贝**(非共享引用)进 sub-visitor——sub-visitor 里的展开消耗的是它自己的预算,不消耗父 visitor 的单次展开预算(父信号是 load-bearing,必须保留;sub 信号除 maxClosureDepth 外可以丢弃)。位置 `internal/bridge/analyzer.go` `walkFuncExpr`。回归测试 `TestAnalyze_F2_RecursiveClosureLiteral_NoStackOverflow`(`internal/bridge/analyzer_test.go`)——用 `git show master:internal/bridge/analyzer.go` 换回旧版验证测试在修复前会直接崩进程(不是 assert 失败)。两个 crasher 种子入 corpus `testdata/fuzz/FuzzP4ForceAllPromote/`。

**Why**:sub-visitor 隔离模式本身是为了隔离信号(比如 localFuncs 的作用域边界),设计是正确的。但递归 guard 的正确作用域是**展开链**(动态调用栈上"这个符号已经在被展开的路径上"这件事),不是 visitor 实例的静态生命周期。把 guard 绑在实例上等价于把"是否在递归展开中"这个属性错误地作用域化到了"哪个 visitor 对象在跑"而不是"哪条调用链正在被走"。

**How to apply**:任何递归遍历器,凡是新建子遍历器实例(为了隔离某类信号)时,先分清楚:哪些状态是"信号"(可以隔离、每个子实例独立)、哪些状态是"预算/guard"(必须沿递归链传递、子实例消耗的是同一份预算)。拷贝继承(值拷贝,不是共享引用)是保住父预算语义的正确做法——子实例改自己那份拷贝不影响父,但子实例开始时看到的初始状态与父一致。

### 5. fuzz 的机器/架构多样性本身是覆盖度维度

同一份 corpus、同一个 harness(`FuzzP4ForceAllPromote`),在 Apple M5 Pro 上 9-80s 内抓到了 amd64 侧此前 120s/150s 多轮都没抓到的既有 fatal bug(教训 4)。新硬件/新架构上装好环境后先跑一轮 fuzz smoke 是廉价高产动作。

**Why**:fuzz 的随机数种子、goroutine 调度时序、内存布局(ASLR)等都受底层硬件/OS 影响,同一份语料在不同机器上探索路径的顺序不同,首次命中某条深路径的时间会有很大方差。这不是「M5 Pro 比 amd64 CI 机器强」,是「多一台不同的机器就多一次独立的随机探索」。

**How to apply**:任何时候接入新硬件/新架构(本例:阶段 1 止血就是为了搭 arm64 基线),环境装好后,在正式工作开始前先花几十秒到几分钟跑一轮既有 fuzz target 的 smoke test,不要等到专门的 fuzz 里程碑才想起来跑。与 [[pr28-f3-3c-tri-platform-matrix-ci]] 的「CI runner 形式盲」邻接——两者都指向"跨硬件/跨平台的物理执行差异是一种真实的覆盖度维度,不能靠单一环境的重复跑弥补"。

## 其它(较小)

- fuzz 失败形式分诊:`context deadline exceeded` + 无 failing input 文件 = fuzz 引擎 30s 窗口收尾时的 flake(单独复跑即过);`Failing input written to testdata/...` + `fuzzing process hung or terminated unexpectedly` = 真 crasher。本轮两种都遇到,前者 FuzzLexer/FuzzCompileRun 各一次,判断标准是看有没有 failing-input 文件被写出来。
- macOS 本机跑 difftest 的 oracle 供给:`check-oracle.sh` 只提示 apt 安装(Linux 向)。macOS 下从源码 `make macosx` 编 lua-5.1.5 + PATH override 即可,CI 的 macos job 里已有一样的做法,但本机开发流程没有文档记录这一步——是一个 doc-gap 候选(不是本轮教训主线,记在这里供后续 recorder 判断是否要补进某篇 guide 或 README)。
- 用户过程反馈(已入用户级 memory,不重复入 llmdoc):提交更频繁 + 单域;开 PR 后立即 `make check-pr-ci`。

## 验证

- `make all` 全绿(fmt/lint/build/test/fuzz/conformance ×3 variants,0 FAIL);
- difftest 因本机(macOS)无 lua5.1 oracle 挡住,从源码编 lua-5.1.5 到 /tmp + PATH override 后三 variant 全部逐字节一致;
- PR #47 三平台 39/39 CI checks 全过 + review bot APPROVE,rebase merge 进 master(f921626/7168648/bf65839);
- HeavyArith force 1125ms→49-51ms(124 B/op / 3 allocs)、Fannkuch force 5.18ms→3.55-3.97ms(382 B/op),force 与 auto 对齐,amd64 行为不变(其 proto 首次 recheck 即被接受,dedup 不改变 amd64 行为);
- issue #40 评论已记录:根因假设证伪 + 止血项勾选 + 数字表 + 「不差于 P3」缺口显式留阶段 2;
- issue #37 评论已记录移植落点清单 + 7 步实施顺序 + 两套 exit-reason 协议并存警告(见下)。
- 文档更新:`docs/design/p2-bridge/04-try-compile-fallback.md` addendum 第 3 条加 dedup 子弹 + `implementation-progress.md` 表 #6 行(7168648)。

## promotion 候选

- **教训 1(退化归因前先证路径存在)**:建议给 [[prove-the-path-under-test]] 补一条**诊断侧对偶**实例。该 guide 现有八个实例全是测试侧(证明「绿色 ≠ 在测你以为在测的」),本条贡献「性能退化数字 ≠ 慢在你以为的路径」这个反向诊断视角,首次样本但视角独特(诊断侧 vs 测试侧),建议 recorder 评估是否直接补进该 guide 而非等第二个样本——因为它填补的是该家族里明确缺失的一个对偶维度,不是同类重复。
- **教训 2(force/auto 不对称是免费根因二分器)**:首次样本,暂留观察。若后续再出现「模式开关不对称用来定位根因」的样本,可与教训 1 一并考虑升格。
- **教训 3(停留重试状态 × 高频触发点)**:首次样本,暂留观察。
- **教训 4(树遍历递归 guard 须沿递归链传递)**:首次样本,暂留观察。
- **教训 5(fuzz 机器/架构多样性是覆盖度维度)**:首次样本,暂留观察,与 [[pr28-f3-3c-tri-platform-matrix-ci]] 邻接但视角不同(那篇讲 CI runner 形式盲,本条讲 fuzz 探索路径的机器依赖性)。
- 教训 3 与 [[p4-beat-p3-opset-round]] 教训 3「验收门与 emit 质量对等」同属「机制交互面审计」这一松散家族(都是"两个独立引入的机制,单独看都合理,组合后产生意外的成本/行为"),但结构不同(那条是空间性的判据窄化,本条是时间性的触发频率爆炸),不建议合并,仅在关联节提及。

## 触发场景

- 收到「某条路径导致 N 倍退化」类归因(issue、用户描述、自己的第一直觉都算)时(教训 1:先用 build tag / 静态追踪 / force-vs-auto 差分验证路径真的存在且被执行,再动手修复)。
- 遇到「模式 A 比模式 B 慢很多」的性能退化时(教训 2:先跑两个模式的最小探针,把「共享机制」与「模式专属机制」分开)。
- 给状态机加「停留重试」/「重试窗口」语义时(教训 3:枚举所有重入触发点 × 实际触发频率 × 每次重试成本,dedup key 绑定「结果变化所必需的最小状态变化」)。
- 写递归树遍历器、需要新建子遍历器实例隔离某类信号时(教训 4:分清哪些状态是"信号"可以隔离、哪些是"预算/guard"必须沿递归链拷贝继承)。
- 接入新硬件/新架构环境后(教训 5:先跑一轮既有 fuzz target smoke test,别等到专门的 fuzz 里程碑)。

## 关联

[[p4-beat-p3-opset-round]](直接前序:`fd055e9` 引入 retry window;`45b8b53` 引入 alias 追踪;本轮两个真 bug 都在那轮埋下)· [[prove-the-path-under-test]](教训 1 的诊断侧对偶候选)· [[perf-optimization-workflow]] §1「profile 先行」/ §5「跨机器基线对照」· [[design-claims-vs-codebase-physics]] §5「时间维度」(issue 描述失实与时间维度家族邻接,但本轮教训 1 是「归因错误路径」而非「快照过期」,判断上不属于 §5 现有四种形式,不建议塞进去)· [[pr28-f3-3c-tri-platform-matrix-ci]](跨平台/硬件多样性,教训 5 邻接)· issue #40 评论 https://github.com/Liam0205/wangshu/issues/40#issuecomment-4872673328 · issue #37 评论 https://github.com/Liam0205/wangshu/issues/37#issuecomment-4872674196 · PR #44 / PR #47 · master commits 483deaf / f921626 / 7168648 / bf65839 · `.llmdoc-tmp/2026-07-02-arm64-exit-reason-port-survey.md`(临时调查报告,移植轮开工时验证后可复用)
