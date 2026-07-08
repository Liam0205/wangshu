---
name: issue77-math-intrinsics-round
description: >
  issue #77(PR #87,分支 feat/issue77-math-intrinsics):给 P4 native JIT 加 math.* intrinsic
  emission —— CALL 站点的 IC 观察到被调是已知纯数值 host closure(sqrt/floor/ceil/abs/max/min)时,
  段内直接发射硬件指令(amd64 SQRTSD/ROUNDSD/sign-clear/UCOMISD-select;arm64 FSQRT/FRINTM/FRINTP/
  FABS/FCMP+FCSEL),不再 exit-reason 往返到 Go host closure。是 #67 的直接后续(NodeHit inline 后
  n-body 剩的 dispatch 几乎全是 sqrt)。双架构 + 完整测试 + CI 全绿 + bot 两轮 APPROVE。头条教训 1:把新
  快路径的 guard 挂在既有快路径的 guard-miss 慢路径上,换热路径零开销 —— math intrinsic 被调方是 host
  closure,protoID 字段是 hostFnID 而非 Lua protoID,必然过不了普通快路径的 protoID guard,直接落到
  guard-miss 慢路径,把 intrinsic 检查挂在那条慢路径上,热的 Lua 调用完全不碰 intrinsic guard。头条教训 2:
  数值 intrinsic 的 byte-equal 依据在值表示层的 NaN 规范化(value.NumberValue 把 NaN 规范成 canonNaN),
  不在指令语义;SSE/NEON 对 canonNaN 要么原样传播、要么产生负 indefinite 被 result-NaN guard 抓走路由到
  host,承 arith inline 先例(fuzz bug f7f0bb1a)。教训 3:机制只在 proto 升 native 后生效,测试脚本必须真
  升层(直接 math.sqrt(x) 过不了 F2-b unknown-call,须用 local sqrt = math.sqrt 别名),prove-the-path
  家族「未升层测试静默测了解释器」又一实例。教训 4:git add -A 把 .rebasechk hook 产物误扫进提交,注意到
  的当下就该加 .gitignore。
metadata:
  type: reflection
  date: 2026-07-08
---

# issue #77 math.* intrinsic emission 轮反思(2026-07-08,PR #87)

> 范围:分支 `feat/issue77-math-intrinsics`(7 个功能/测试/文档提交 `68709dc..56e17c7` + 1 个 `.rebasechk` 清理提交 `0e78ba6`)。给 P4 native JIT 加 `math.*` intrinsic emission。是 issue #67 的直接后续——NodeHit inline 修完后,n-body 剩下的 exit-reason dispatch 几乎全是 `sqrt`。

## 任务

给 P4 native JIT 加 `math.*` intrinsic emission:CALL 站点的 IC 观察到被调是已知纯数值 host closure(sqrt / floor / ceil / abs / max / min)时,段内直接发射硬件指令(amd64 SQRTSD / ROUNDSD / sign-clear / UCOMISD-select;arm64 FSQRT / FRINTM / FRINTP / FABS / FCMP+FCSEL),不再 exit-reason 往返到 Go host closure。双架构 + 完整测试 + 开 PR + CI 全绿 + bot 两轮 APPROVE。

## 期望与实际

- 期望:被调是已知数值 host closure 时段内直接算,不往返 host;双架构接受面同构;fib / spectral / fannkuch 等非 intrinsic 内核无回归。
- 实际:达成。双架构 intrinsic emission 交付,fib / spectral / fannkuch 实测无回归(见头条教训 1 —— guard 挂位置正是无回归的原因)。完整测试 + CI 全绿 + bot 两轮 APPROVE。

## 头条教训 1:给某快路径加新特化分支时,把新检查挂在「不适用它的输入本就会走到的既有 guard-miss 慢路径」上,换热路径零开销

最初我把 intrinsic guard 写成独立函数,在 `emitCALL` 里所有 `b==2/c==2` 的 CALL(包括 fib 的热自递归调用)前面都先跑一遍。这会给每个非 intrinsic 调用加约 7 条指令 + 一个 imm64 load 的开销,回归 fib。

写完后立刻意识到这点并重构。关键物理事实:一个 math intrinsic 的被调方是 host closure,它的 `protoID` 字段是 `hostFnID` 而非 Lua `protoID`,**必然过不了普通快路径的 protoID guard**,直接落到 guard-miss 慢路径。所以把 intrinsic 检查挂在那条慢路径上——热的 Lua 调用过 protoID guard 走 seg2seg,完全不碰 intrinsic guard,零开销;只有本来就要 exit-reason 往返的 host 调用才付 intrinsic guard 成本。fib / spectral / fannkuch 无回归实测证实。

**通用洞察**:给某个 tier / 快路径加一个新的特化分支时,先问「新分支要处理的输入,在既有 guard 序列里天然落到哪个 miss 分支?」把新检查放到那个已经要走慢路径的点,而不是放在所有输入都要过的入口——这样特化对不适用它的热路径零成本。这不是「加个 fast-path 检查」的直觉写法(那会把成本平摊到所有输入),而是「把特化寄生在既有的分流点上」。

## 头条教训 2:数值 intrinsic 的 byte-equal 依据落在值表示层的 NaN 规范化,不在指令语义

JIT 段内发射 SSE / NEON 指令算出的浮点结果,要与 host(Go `math.*`)逐字节一致。关键不在指令本身(SQRTSD 就是 Go `math.Sqrt` 编译出来的),而在 **NaN 规范化**:

- `value.NumberValue` 把任何 NaN 规范成 `canonNaN`(`0x7FF8...`),所以值世界里唯一存在的 NaN 数就是 `canonNaN`。
- SSE / NEON 对 `canonNaN` 的运算要么原样传播(结果 `< qNanBoxBase`,直接存,与 host 一致),要么产生负 indefinite(结果 `≥ qNanBoxBase`,被 result-NaN guard 抓走路由到 host 规范化)。
- 这道 result guard 是从 arith inline 先例(fuzz bug `f7f0bb1a`,见 [[2026-07-02-p4-beat-p3-opset-round]] 教训 4)继承的。
- abs / max / min 结果必是某个 `< qNanBoxBase` 的输入,不会落进 tag 区,不需要 guard。

**洞察**:给 JIT 加任何产生浮点结果的快路径前,先查值表示层怎么规范化 NaN / 特殊值——正确性依据在那里,不在指令语义。指令本身「算得对」不等于「结果的位模式落在值世界合法区间内」;NaN-boxing 值表示把浮点值的位空间切了一块给 tag,任何段内浮点运算都要对着这块 tag 区重判结果。

## 教训 3:机制只在 proto 升 native 后生效时,测试脚本必须真的能升层

第一版 e2e 测试用直接 `math.sqrt(i)` 调用,`promotions=0`、intrinsic 完全不命中。查了才发现:直接 `math.sqrt(x)` 过不了 F2-b unknown-call 检查,proto 根本不升 native,intrinsic 路径永远到不了。必须用 `local sqrt = math.sqrt` 别名(analyzer 的 stdlib alias 追踪认得,见 [[2026-07-02-p4-beat-p3-opset-round]] 教训 5),proto 才升层。n-body 真实代码本来就这么写。

**洞察**:给一个只在「proto 升 native 后」才生效的机制写测试时,先确认测试脚本真的能升层(`PromotionCount>0`),否则测的是解释器。这是 [[prove-the-path-under-test]] 「未强制 / 未升层测试静默测了解释器」的又一实例——承 [[2026-07-07-issue67-auto-mode-coverage-round]](未强制 auto 测试悄悄滑回纯解释器,解药 `PromotionCount>0` 兜底断言)。本轮是同一解药的又一具体形式:不只「auto 模式没到阈值」会静默降级到解释器,「测试脚本的调用形式过不了 F2-b 静态门」也会,两者都要靠 `PromotionCount>0` 兜住。

## 教训 4(过程):已知不该提交的 untracked 产物,注意到的当下就加 .gitignore

`git add -A` 把一个 pre-push hook 产物空文件 `.rebasechk` 误扫进提交,被 bot 抓到。之前我明明注意到它是 untracked hook 残留、还特意没 commit,但后面一次 `git add -A` 又把它带进去了。已 `git rm` + 加 `.gitignore`。

**洞察**:已知的「不该提交的 untracked 产物」,注意到的当下就加 `.gitignore`,别只在脑里记着不 add——下一次 `git add -A` 会背叛你。

## Promotion 判断

### 教训 1(guard 挂在既有 miss 分支上换热路径零开销)——暂留观察

这是一个新的、可复用的 JIT / 性能手法,以前的反思里没有。它与 [[perf-optimization-workflow]] 的「profile 先行 / benchmark 否决门」不同层——那些是流程纪律,这条是 codegen 结构手法。**首次样本,暂留观察**:写清在本反思里,等第二个实例(P5 trace JIT / 新特化档 / 新后端再遇「给热路径加特化分支」)再评估是否升 [[perf-optimization-workflow]] 或独立立 guide。理由:单一样本的结构手法容易过拟合到本例的具体 guard 布局(protoID guard 恰好把 host closure 分流出去),需要第二个不同形状的实例确认它是通用手法而非本例巧合。

### 教训 2(数值快路径 byte-equal 依据在值表示层的 NaN 规范化)——暂留观察,倾向后续并入 must/design-premises 的值表示承诺对偶面

这条与 [[design-premises]] 第四组「第一天 NaN-boxing 值表示承诺」直接对偶——那篇讲值表示的架构承诺,这条讲该承诺在 JIT 段内浮点快路径上的正确性落点。已有先例:arith inline 的 result guard(`f7f0bb1a`)。本轮是第二个「段内浮点结果撞 NaN-box tag 区」的实例(arith 是第一个)。**暂留观察**但接近阈值:arith inline result guard 已是先例,本轮 intrinsic 是同一物理事实的第二个应用面。若再有第三个段内浮点快路径(P5 / 新超越函数 intrinsic)撞同一 tag 区,建议把「段内浮点结果必须对 NaN-box tag 区重判」升为 [[design-claims-vs-codebase-physics]] 或 [[design-premises]] 的显式条款,不再每轮各自重新发现。本轮先在 memory 内反引 [[2026-07-02-p4-beat-p3-opset-round]] 教训 4 记录家族关系。

### 教训 3(机制只在升层后生效 → 测试必须真升层)——memory 内反引,不新增 guide 正文

[[prove-the-path-under-test]] 已有 14 个独立实例,其中第 13 个(issue #67 auto-mode)的解药 `PromotionCount>0` 兜底断言与本轮完全同源。本轮只是同一解药在「F2-b 静态门」这个新触发维度上的再次确认,不是新反模式。**倾向 memory 内反引不新增 guide 正文**:guide 已把「未强制 / 未升层测试静默测解释器」和 `PromotionCount>0` 解药讲清,本轮无新解药、无新反模式形式,只是又一次验证。若 recorder 认为值得在 guide 的实例列表里加一笔作实证(「不只 auto 阈值,静态门也会让升层落空」),那是廉价的强化,但不改变 guide 正文结论。

### 教训 4(git add -A 误扫 hook 产物)——暂留观察,或并入某 git 纪律 memory

过程小教训,首次样本。可并入既有 git 纪律 feedback(如 [[feedback_commit_frequency]] 或独立 git 卫生条目),但单独看不够格立 guide。**暂留观察**。

## Follow-up

- 无未完成实现项:双架构 intrinsic emission 已交付,CI 全绿,bot 两轮 APPROVE。
- 若 P5 trace JIT / 新特化档再遇「给热路径加特化分支」,回看教训 1 评估是否升 guide。
- 若第三个段内浮点快路径撞 NaN-box tag 区,把教训 2 升为显式条款。
- `.rebasechk` 已加 `.gitignore`(commit `0e78ba6`),该产物不会再被 `git add -A` 扫入。
