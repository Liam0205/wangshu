---
name: issue77-math-intrinsics-round
description: >
  issue #77(PR #87,分支 feat/issue77-math-intrinsics,两轮推进):给 P4 native JIT 加 math.* intrinsic
  emission —— CALL 站点的 IC 观察到被调是已知纯数值 host closure(sqrt/floor/ceil/abs/max/min)时,
  段内直接发射硬件指令(amd64 SQRTSD/ROUNDSD/sign-clear/UCOMISD-select;arm64 FSQRT/FRINTM/FRINTP/
  FABS/FCMP+FCSEL),不再 exit-reason 往返到 Go host closure。是 #67 的直接后续(NodeHit inline 后
  n-body 剩的 dispatch 几乎全是 sqrt)。双架构 + 完整测试 + CI 全绿 + bot 两轮 APPROVE。头条教训 A(过程,
  最重要):不做 benchmark 形状的窄修复 —— 第二轮起初只让别名写法 local sqrt = math.sqrt 命中 intrinsic、
  直接 math.sqrt(x) 完全不生效,还用「benchmark 本来就 alias、perf-Lua 惯用法就是 hoist」合理化窄修复交付,
  用户强烈反弹;深挖才发现是密度门的真 bug。一个「加速某类调用」的功能若收益只在 benchmark 恰好用的那种写法
  上生效就是 hack 指标不是解决问题,发现「只对某形式管用」要立刻当红旗深挖根因。头条教训 B(技术):profitability
  启发式(CALL 密度门 totalOps/callCount<16)错放进 capability 门(AnalyzeNative/SupportsAllOpcodes),
  继承了 capability 的「永久拒 + forceAll 不绕 + auto 无重试」语义 —— auto 模式能力拒 → TierStuck 无重试窗口
  (retry 只对 forceAll)→ 一次拒永久钉死;是 [[backend-capability-vs-profitability]] 主张的反面实例。修法:
  前端 exprCall 把 math intrinsic 的 CALL pc 记进 Proto.IntrinsicCallPCs,两 arch 密度门把这些 pc 从
  callCount 排除(intrinsic CALL 是内联指令不是往返);修好后直接写法也升层 + intrinsic 命中,与别名写法、
  与解释器逐字节一致。教训 1:把新快路径的 guard 挂在既有快路径的 guard-miss 慢路径上,换热路径零开销 ——
  math intrinsic 被调方是 host closure,protoID 字段是 hostFnID 而非 Lua protoID,必然过不了普通快路径的
  protoID guard,直接落到 guard-miss 慢路径。教训 2:数值 intrinsic 的 byte-equal 依据在值表示层的 NaN
  规范化(value.NumberValue 把 NaN 规范成 canonNaN),不在指令语义;承 arith inline 先例(fuzz bug f7f0bb1a)。
  教训 3(更正):机制只在 proto 升 native 后生效,测试脚本必须真升层(PromotionCount>0 兜底);原稿归因
  「直接写法过不了 F2-b unknown-call」是错的 —— F2-b 其实接受 math.sqrt(x),真正的 block 是 CALL 密度门
  这道能力层 profitability 启发式,见头条教训 B。教训 4:git add -A 把 .rebasechk hook 产物误扫进提交,注意到
  的当下就该加 .gitignore。
metadata:
  type: reflection
  date: 2026-07-08
---

# issue #77 math.* intrinsic emission 轮反思(2026-07-08,PR #87,两轮推进)

> 范围:分支 `feat/issue77-math-intrinsics`。给 P4 native JIT 加 `math.*` intrinsic emission。是 issue #67 的直接后续——NodeHit inline 修完后,n-body 剩下的 exit-reason dispatch 几乎全是 `sqrt`。
> **本文经两轮推进合并成文**:第一轮交付双架构 intrinsic emission(教训 1/2/4);第二轮深挖「为什么直接 `math.sqrt(x)` 写法不升层、不命中 intrinsic」,推翻了第一轮对教训 3 的错误归因(不是 F2-b,是 CALL 密度门这道能力层 profitability 启发式),并修好使直接写法也升层命中。第二轮产出两条头条教训 A(过程)+ B(技术),放在最前。

## 任务

给 P4 native JIT 加 `math.*` intrinsic emission:CALL 站点的 IC 观察到被调是已知纯数值 host closure(sqrt / floor / ceil / abs / max / min)时,段内直接发射硬件指令(amd64 SQRTSD / ROUNDSD / sign-clear / UCOMISD-select;arm64 FSQRT / FRINTM / FRINTP / FABS / FCMP+FCSEL),不再 exit-reason 往返到 Go host closure。双架构 + 完整测试 + 开 PR + CI 全绿 + bot 两轮 APPROVE。

## 期望与实际

- 期望:被调是已知数值 host closure 时段内直接算,不往返 host;双架构接受面同构;fib / spectral / fannkuch 等非 intrinsic 内核无回归;**intrinsic 对所有写法(直接 `math.sqrt(x)` 与别名 `local sqrt = math.sqrt`)一致生效**。
- 实际:第一轮达成前半——双架构 intrinsic emission 交付,fib / spectral / fannkuch 实测无回归(见教训 1),但**只有别名写法命中 intrinsic、直接写法完全不生效**,我一度把这当成完成交付并用「benchmark 惯用法本来就 alias」合理化(见头条教训 A)。第二轮深挖到密度门真 bug 并修好后,直接写法也升层 + 命中 intrinsic,与别名写法、与解释器逐字节一致。完整测试 + CI 全绿 + bot 两轮 APPROVE。

## 头条教训 A(过程,最重要):不做 benchmark 形状的窄修复,「只对某形式管用」要当红旗深挖根因

第一版实现里,只有别名写法 `local sqrt = math.sqrt; sqrt(x)` 命中 intrinsic,直接 `math.sqrt(x)` 完全不生效。我不但没深挖,还主动用「n-body / benchmark-game 本来就 alias、perf-Lua 惯用法就是 hoist stdlib 函数」把窄修复合理化成完成交付。用户强烈反弹:「我要骂娘了……我们的目的不是去 hack 某几个 benchmark 的指标,而是去解决真正的问题。」

深挖后发现两种写法的差异根本不是「惯用法」问题,而是密度门的一个真 bug(见头条教训 B):直接写法的短函数 12 op / 1 call 被 CALL 密度门拒收,永远升不了层,intrinsic 路径到不了。

**洞察**:一个「加速某类调用」的功能,如果收益只在 benchmark 恰好用的那种写法上生效,那是 hack 指标不是解决问题。发现「只对某形式管用、换个等价写法就失效」时,要立刻把它当红旗深挖根因,不要用「idiomatic / 惯用法 / 性价比不划算」合理化窄修复——这类合理化往往正好掩盖了一个真 bug。已存进用户 memory `feedback_no_benchmark_shaped_fixes`。

## 头条教训 B(技术):profitability 启发式混进 capability 门 + 能力拒无 auto 重试 = 永久钉死

直接 `math.sqrt(x)` 不升层的真正原因是 **`AnalyzeNative` 的 CALL 密度门**(`totalOps/callCount < 16` 就拒),它的前提假设是「每个 CALL 都是一次 ~15-25 op 的 exit-reason 往返,CALL 太密则升层比解释器还慢」。

- **F2-b 其实接受 `math.sqrt(x)`**:analyzer 的 `isSafeStdlibCall` 认得 `lib.method` 形式,已有测试 `TestAnalyze_F2_StdlibSafeCalls` 的 `math-sqrt` case 断言它不设 `ReasonUnknownCall`。所以原稿把 block 归因于 F2-b unknown-call 是错的。
- 直接写法的短函数 12 op / 1 call = 12 < 16 → 被密度门拒;而且密度门在**能力层**(`AnalyzeNative` / `SupportsAllOpcodes`),auto 模式下能力拒**没有重试窗口**(`bridge.go` 的 retry 只对 forceAll)→ 一次拒就永久 `TierStuck`。别名写法能升,是因为它 callee 走 GETUPVAL、proto 是 seg2seg-eligible → 密度门被放宽;直接写法有 GETGLOBAL+GETTABLE(非 seg2seg-eligible)→ 门不放宽。
- **修法**:前端 `exprCall` 把 math intrinsic 的 CALL pc 记进 `Proto.IntrinsicCallPCs`,两 arch 密度门把这些 pc 从 callCount 排除——intrinsic CALL 是内联指令不是 exit-reason 往返,不该计入密度惩罚。修好后直接写法也升层 + intrinsic 命中,与别名写法、与解释器逐字节一致。fib 等真·CALL 密集 proto 不受影响(它们的调用不是 math 名)。

**洞察**:profitability 判据(值不值得升)和 capability 判据(能不能编)要分清。密度门本质是 profitability(避免升层比解释器慢的 CALL 密集 proto),却放在能力门 `AnalyzeNative` 里,于是继承了 capability 门的三条语义——① forceAll 不绕能力门,所以它连 force 也挡;② auto 模式能力拒直接 `TierStuck` 且无重试窗口;两者叠加把一个本该「这次先不升、以后可再评估」的收益判断,变成了「永久不可编译」的能力级后果。profitability 逻辑一旦混进 capability 门,就会把「暂缓」误升成「永久钉死」。

本轮是 [[backend-capability-vs-profitability]] 主张的一个**反面实例**——那篇 guide 讲的正是「能力(编不编得对)与收益(编了赚不赚)是两个正交问题,收益门必须仅在 auto 生效、forceAll 绕过、拒收进 TierStuck 而不永久卡死」;密度门这个收益判据错放在能力层,恰好把 guide 警告的后果演了一遍。

## 教训 1:给某快路径加新特化分支时,把新检查挂在「不适用它的输入本就会走到的既有 guard-miss 慢路径」上,换热路径零开销

最初我把 intrinsic guard 写成独立函数,在 `emitCALL` 里所有 `b==2/c==2` 的 CALL(包括 fib 的热自递归调用)前面都先跑一遍。这会给每个非 intrinsic 调用加约 7 条指令 + 一个 imm64 load 的开销,回归 fib。

写完后立刻意识到这点并重构。关键物理事实:一个 math intrinsic 的被调方是 host closure,它的 `protoID` 字段是 `hostFnID` 而非 Lua `protoID`,**必然过不了普通快路径的 protoID guard**,直接落到 guard-miss 慢路径。所以把 intrinsic 检查挂在那条慢路径上——热的 Lua 调用过 protoID guard 走 seg2seg,完全不碰 intrinsic guard,零开销;只有本来就要 exit-reason 往返的 host 调用才付 intrinsic guard 成本。fib / spectral / fannkuch 无回归实测证实。

**通用洞察**:给某个 tier / 快路径加一个新的特化分支时,先问「新分支要处理的输入,在既有 guard 序列里天然落到哪个 miss 分支?」把新检查放到那个已经要走慢路径的点,而不是放在所有输入都要过的入口——这样特化对不适用它的热路径零成本。这不是「加个 fast-path 检查」的直觉写法(那会把成本平摊到所有输入),而是「把特化寄生在既有的分流点上」。

## 教训 2:数值 intrinsic 的 byte-equal 依据落在值表示层的 NaN 规范化,不在指令语义

JIT 段内发射 SSE / NEON 指令算出的浮点结果,要与 host(Go `math.*`)逐字节一致。关键不在指令本身(SQRTSD 就是 Go `math.Sqrt` 编译出来的),而在 **NaN 规范化**:

- `value.NumberValue` 把任何 NaN 规范成 `canonNaN`(`0x7FF8...`),所以值世界里唯一存在的 NaN 数就是 `canonNaN`。
- SSE / NEON 对 `canonNaN` 的运算要么原样传播(结果 `< qNanBoxBase`,直接存,与 host 一致),要么产生负 indefinite(结果 `≥ qNanBoxBase`,被 result-NaN guard 抓走路由到 host 规范化)。
- 这道 result guard 是从 arith inline 先例(fuzz bug `f7f0bb1a`,见 [[2026-07-02-p4-beat-p3-opset-round]] 教训 4)继承的。
- abs / max / min 结果必是某个 `< qNanBoxBase` 的输入,不会落进 tag 区,不需要 guard。

**洞察**:给 JIT 加任何产生浮点结果的快路径前,先查值表示层怎么规范化 NaN / 特殊值——正确性依据在那里,不在指令语义。指令本身「算得对」不等于「结果的位模式落在值世界合法区间内」;NaN-boxing 值表示把浮点值的位空间切了一块给 tag,任何段内浮点运算都要对着这块 tag 区重判结果。

## 教训 3(更正):机制只在 proto 升 native 后生效时,测试脚本必须真的能升层

第一版 e2e 测试用直接 `math.sqrt(i)` 调用,`promotions=0`、intrinsic 完全不命中,必须用 `local sqrt = math.sqrt` 别名才升层。

**原稿归因错误须更正**:第一版反思说直接写法不升层是因为「过不了 F2-b unknown-call 检查」。这个归因是错的,第二轮深挖推翻了它——F2-b 其实**接受** `math.sqrt(x)`(analyzer 的 `isSafeStdlibCall` 认得 `lib.method` 形式,`TestAnalyze_F2_StdlibSafeCalls` 的 `math-sqrt` case 断言不设 `ReasonUnknownCall`)。真正的 block 是 **CALL 密度门这道能力层 profitability 启发式**(`totalOps/callCount < 16`),详见头条教训 B(现已修复,直接写法也升层命中)。

**洞察(仍成立,触发维度更正)**:给一个只在「proto 升 native 后」才生效的机制写测试时,先确认测试脚本真的能升层(`PromotionCount>0` 兜底断言),否则测的是解释器。这是 [[prove-the-path-under-test]] 「未强制 / 未升层测试静默测了解释器」的又一实例——承 [[2026-07-07-issue67-auto-mode-coverage-round]](未强制 auto 测试悄悄滑回纯解释器,解药 `PromotionCount>0` 兜底断言)。本轮触发维度不是 F2-b,而是 CALL 密度门:不只「auto 模式没到阈值」会静默降级到解释器,「调用形式撞上能力层的收益启发式(密度门)」也会——两者都要靠 `PromotionCount>0` 兜住。

**诊断过程(呼应 [[prove-the-path-under-test]] §7 诊断侧)**:根因是一路 instrument 追出来的——先怀疑 F2-b(证伪:analyzer 接受)→ 查 `AnalyzeNative` 能力面(GETGLOBAL/GETTABLE/CALL 都过)→ 查 `considerPromotion` / `recheckCompilabilityRuntime` → 最后定位到密度门 `totalOps/callCount < 16` + auto 无重试。中途踩了几个错误假设(F2-b、冷 IC 时机)。§7 诊断侧的教训在此复现:归因前先证「被怪罪的路径」真的在起作用——我一开始怪 F2-b,就是没先证它真的在拒。

## 教训 4(过程):已知不该提交的 untracked 产物,注意到的当下就加 .gitignore

`git add -A` 把一个 pre-push hook 产物空文件 `.rebasechk` 误扫进提交,被 bot 抓到。之前我明明注意到它是 untracked hook 残留、还特意没 commit,但后面一次 `git add -A` 又把它带进去了。已 `git rm` + 加 `.gitignore`。

**洞察**:已知的「不该提交的 untracked 产物」,注意到的当下就加 `.gitignore`,别只在脑里记着不 add——下一次 `git add -A` 会背叛你。

## Promotion 判断

### 头条教训 A(不做 benchmark 形状的窄修复)——process 教训,已入用户 memory,反思内记录即可

这是 process / feedback 教训,已存进用户 memory `feedback_no_benchmark_shaped_fixes`。反思里记录本轮实证即可,**不升 llmdoc guide**(process 教训归用户 memory,不进项目 guide 正文)。

### 头条教训 B(profitability 启发式错放 capability 门 → auto 永久钉死)——建议给 [[backend-capability-vs-profitability]] 记一笔实证,或暂留观察由 recorder 定

本轮是 [[backend-capability-vs-profitability]] 主张的一个**反面实例**:该 guide 讲的「capability(能不能编)与 profitability(编了赚不赚)是两个正交问题,收益门必须仅在 auto 生效、forceAll 绕过、拒收进 TierStuck 而非永久卡死」,正好被密度门这个收益判据错放在能力层演了一遍——继承了 capability 门的「forceAll 不绕 + auto 无重试 + 永久拒」语义,把「暂缓」误升成「永久钉死」。guide 现有四实例都是**正确分层**的样本(收益判据正确地待在收益层),本轮提供第一个「收益判据误放能力层导致的后果」反面样本,且带一个可复用的修法(把 intrinsic CALL pc 从密度 callCount 排除,而不是把整道门搬走)。**建议**:在 [[backend-capability-vs-profitability]] 记一笔实证(密度门错放能力层导致 auto 永久钉死 + 修法),或暂留观察由 recorder 决定落点。

### 教训 1(guard 挂在既有 miss 分支上换热路径零开销)——暂留观察

这是一个新的、可复用的 JIT / 性能手法,以前的反思里没有。它与 [[perf-optimization-workflow]] 的「profile 先行 / benchmark 否决门」不同层——那些是流程纪律,这条是 codegen 结构手法。**首次样本,暂留观察**:写清在本反思里,等第二个实例(P5 trace JIT / 新特化档 / 新后端再遇「给热路径加特化分支」)再评估是否升 [[perf-optimization-workflow]] 或独立立 guide。理由:单一样本的结构手法容易过拟合到本例的具体 guard 布局(protoID guard 恰好把 host closure 分流出去),需要第二个不同形状的实例确认它是通用手法而非本例巧合。

### 教训 2(数值快路径 byte-equal 依据在值表示层的 NaN 规范化)——暂留观察,倾向后续并入 must/design-premises 的值表示承诺对偶面

这条与 [[design-premises]] 第四组「第一天 NaN-boxing 值表示承诺」直接对偶——那篇讲值表示的架构承诺,这条讲该承诺在 JIT 段内浮点快路径上的正确性落点。已有先例:arith inline 的 result guard(`f7f0bb1a`)。本轮是第二个「段内浮点结果撞 NaN-box tag 区」的实例(arith 是第一个)。**暂留观察**但接近阈值:arith inline result guard 已是先例,本轮 intrinsic 是同一物理事实的第二个应用面。若再有第三个段内浮点快路径(P5 / 新超越函数 intrinsic)撞同一 tag 区,建议把「段内浮点结果必须对 NaN-box tag 区重判」升为 [[design-claims-vs-codebase-physics]] 或 [[design-premises]] 的显式条款,不再每轮各自重新发现。本轮先在 memory 内反引 [[2026-07-02-p4-beat-p3-opset-round]] 教训 4 记录家族关系。

### 教训 3(机制只在升层后生效 → 测试必须真升层)——memory 内反引,不新增 guide 正文

[[prove-the-path-under-test]] 已有 14 个独立实例,其中第 13 个(issue #67 auto-mode)的解药 `PromotionCount>0` 兜底断言与本轮完全同源。本轮只是同一解药在「能力层收益启发式(密度门)让升层落空」这个新触发维度上的再次确认,不是新反模式。**倾向 memory 内反引不新增 guide 正文**:guide 已把「未强制 / 未升层测试静默测解释器」和 `PromotionCount>0` 解药讲清,本轮无新解药、无新反模式形式,只是又一次验证。若 recorder 认为值得在 guide 的实例列表里加一笔作实证(「不只 auto 阈值,密度门这类升层启发式也会让升层落空」),那是廉价的强化,但不改变 guide 正文结论。诊断侧(先证被怪罪的路径真在起作用,§7)在本轮也复现,但 §7 已有实例,同样只作 memory 记录。

### 教训 4(git add -A 误扫 hook 产物)——暂留观察,或并入某 git 纪律 memory

过程小教训,首次样本。可并入既有 git 纪律 feedback(如 [[feedback_commit_frequency]] 或独立 git 卫生条目),但单独看不够格立 guide。**暂留观察**。

## Follow-up

- 无未完成实现项:双架构 intrinsic emission 已交付、直接写法与别名写法都升层命中且逐字节一致,CI 全绿,bot 两轮 APPROVE。
- 本文经两轮推进合并:第一轮对教训 3 的 F2-b 归因**已更正**为 CALL 密度门(能力层收益启发式),新增头条教训 A(过程)+ B(技术)。
- recorder 定夺:头条教训 B 是否在 [[backend-capability-vs-profitability]] 记一笔「收益判据误放能力层 → auto 永久钉死」反面实证。
- 若 P5 trace JIT / 新特化档再遇「给热路径加特化分支」,回看教训 1 评估是否升 guide。
- 若第三个段内浮点快路径撞 NaN-box tag 区,把教训 2 升为显式条款。
- 头条教训 A 已入用户 memory `feedback_no_benchmark_shaped_fixes`。
- `.rebasechk` 已加 `.gitignore`(commit `0e78ba6`),该产物不会再被 `git add -A` 扫入。
