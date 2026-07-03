---
name: issue37-arm64-exit-reason-port-round
description: issue #37 / #40 阶段 2,把 P4 native 的 exit-reason 协议从 amd64 移植到 arm64,在 darwin/arm64 真机(Apple M5 Pro)单会话完成,分支 `feat/p4-arm64-exit-reason` 8 commits(677ceb2..a4993cc)。承接上一轮 stopbleed 收尾时预写在 issue #37 评论里的 7 步实施顺序,本轮开工时 `.llmdoc-tmp` 调查报告已丢失但 issue 评论落点清单完整存活,按其执行零返工——重要移植前调查结论应写进持久渠道(issue 评论)而非只留临时缓存。核心技术教训:移植不是复制,三个实例证明硬件语义差异要求逐 op 重判——(a) arm64 UNM 加的 -NaN e2e 探针抓出 canonNaN sign-flip 变 value.Nil 位模式的静默错果,回查发现 amd64 emitUNM 有同款既有 bug(移植轮给旧 arch 挖 bug);(b) 沿用旧 arm64 scaffold 的有符号整数比较条件码在 FCMPE unordered(NaN)时错判,换 FP 安全族 MI/PL/LS/HI 后正确,旧 scaffold 从未被 NaN 输入测过;(c) arm64 不需要 arith 结果 NaN guard(AArch64 default NaN 是正的且传播保留输入)而 amd64 SSE 需要,同一 guard 两 arch 一必需一多余不能机械镜像。另有 arch 差异带来简化机会(FCMPE unordered 单跳替代双跳、表槽地址寄存器复用免重算)、e2e 探针载体须避开无关 op 噪声(GETUPVAL 往返污染 inline 命中计数)、调试代码清理误用 `git checkout` 整文件回退未提交工作(教训:精确反向编辑或先 stash)、每步独立 commit + 每步 fuzz 的节奏在移植轮工作良好、HelperCompareSlow 首次出现 dispatcher 向段内回传数据的双向通道形态。
metadata:
  type: reflection
  date: 2026-07-03
---

# issue #37 arm64 exit-reason 协议移植轮反思(2026-07-03,分支 `feat/p4-arm64-exit-reason`,8 commits)

> 范围:issue #37(P4 native exit-reason 协议 arm64 移植)/ issue #40 阶段 2(「arm64 全部基准不差于 P3」验收口径)。前情:[[2026-07-03-issue40-arm64-stopbleed-round]](阶段 1 止血轮,同日,直接前序)——该轮收尾时在 issue #37 评论里预写了 7 步实施顺序 + 两套 exit-reason 协议并存警告 + 可复用件清单。本轮在 darwin/arm64 真机(Apple M5 Pro)单会话按该清单执行完成,终态:两 arch native 接受面重新对齐,`docs/design/p4-method-jit/implementation-progress.md` §14.5「arm64 分岔」口径自本轮起失效。技术细节见该文档 §14.6(本轮写入)。

## 核心教训(按强度排序)

### 1. 移植前调查结论要写进持久渠道(issue 评论),不能只留 `.llmdoc-tmp`

上一轮(stopbleed)收尾时在 issue #37 评论里预写了 7 步实施顺序 + 「两套 exit-reason 协议并存勿混」警告 + 可复用件清单(见 [[2026-07-03-issue40-arm64-stopbleed-round]] 关联节 `.llmdoc-tmp/2026-07-02-arm64-exit-reason-port-survey.md` 引用)。本轮开工时该 `.llmdoc-tmp` 文件已被清空(临时缓存易失,符合其定位),但 issue #37 评论里的落点清单完整存活,直接按其执行,零返工、零重新调查。

**Why**:`.llmdoc-tmp/` 明确定位为本地临时上下文缓存(见 llmdoc skill 约定),生命周期不跨会话保证。任何移植/延后工作如果只把关键结论沉淀在这里,下一轮开工时可能要重新调查一遍——本轮侥幸不受影响,是因为调查产出**同时**写了一份到 issue 评论(持久、跨会话、跨机器可读)。这不是巧合规避,是纪律执行:issue 评论天然具备"给未来某次开工的自己看"的受众定位。

**How to apply**:任何一次"发现了但这次不做,留给下一轮"的调查结论,产出物要有至少一份写在持久渠道(issue/PR 评论、`llmdoc/memory/`、`docs/design/`),`.llmdoc-tmp` 可以有更详细的工作稿,但不能是唯一副本。收尾一轮工作、准备把某项工作显式推迟时,自查一遍"如果 `.llmdoc-tmp` 现在清空,下一轮开工者(可能是我自己)还能不能拿到落点清单"。

### 2. 移植不是复制——硬件语义差异要求逐 op 重新判断,而不是机械镜像

三个独立实例证明,把 amd64 的 emit 逻辑直接套到 arm64 上会产生静默错误,必须针对每个 op 重新推导硬件语义:

**(a) UNM 结果 guard 挖出 amd64 既有 bug**。arm64 移植 UNM 时加了 `-NaN` e2e 探针(`tostring(-(0/0))` 期待字符串 `"nan"`),抓出 canonNaN(`0x7FF8...`)sign-flip 后变成 `0xFFF8...`——这恰好是 `value.Nil` 的位模式,导致 `-(0/0)` 在 native 路径被静默算成 `nil`。回查发现 **amd64 的 `emitUNM` 有同款缺陷**,自 exit-reason op-set 扩面轮起就存在,多轮 fuzz 都没抓到(fuzz 靠随机输入命中特定 NaN 位模式的概率低)。本轮同一 commit(`508028a`)双 arch 一起修。

**(b) 比较条件码族选择错误**。沿用旧 arm64 scaffold 的有符号整数条件码(GE/LT/GT/LE)在 `FCMPE` unordered(比较数含 NaN)时会错判——例如 LT 用 `N!=V` 判定,在 unordered 状态(`N=0,Z=0,C=1,V=1`)下该表达式为真,把 NaN 比较误判成 true。换成 FP 安全条件码族(MI/PL/LS/HI)后,NaN 语义无需额外分支即正确解析为 false。旧 scaffold 从未被 NaN 输入测过——它当年设计时就因为无法处理非数字整体拒收,所以这个错误条件码族选择潜伏至今没被触发。

**(c) arith NaN 结果 guard 一个 arch 必需一个多余**。amd64 SSE 的浮点运算在某些路径产出负 indefinite NaN(`0xFFF8...`,即 tag-space 别名),需要显式结果 guard 路由到 `host.Arith` 做规范化;arm64 的 AArch64 default NaN 规则是正的、且 NaN 传播保留输入位模式,值世界里只会出现规范正 NaN,不会产生 tag-space 别名。同一个 guard 在两个 arch 里一个是正确性必需、一个是纯多余开销——机械镜像 amd64 的 guard 逻辑到 arm64 不会出错但会白白付出成本,反过来省略 amd64 侧的 guard 则是正确性 bug。

**Why**:三个实例的共同结构是"移植时写新 arch 的语义测试,顺手回答了『旧 arch 这里对吗』这个从未被单独问过的问题"。移植工作天然制造了一个语义对照的时机——写新 arch 代码时脑子里必须把两个 arch 的硬件行为并排放,这种并排比较状态是平时单 arch 开发不会主动触发的。这与 [[2026-07-03-issue40-arm64-stopbleed-round]] 教训 5(fuzz 机器多样性是覆盖度维度)是邻接的一个家族:那条讲"多一台机器就多一次独立随机探索",这条讲"移植到新 arch 时的强制语义对照是一种非随机、确定性更高的挖既有 bug 手段"——两者共同点是"接入新硬件/新 arch 本身构成一类独立的 bug 发现机会",但触发机制不同(fuzz 靠随机性,移植语义对照靠强制并排推导)。

**How to apply**:做跨 arch 移植时,不要把"新 arch 的 emit 逻辑照抄旧 arch 结构、只换指令编码"当作默认工作模式。对每个要移植的 op,显式问三个问题:①这个 op 在新硬件上的边界语义(NaN/溢出/符号位/舍入)与旧硬件是否一致?②旧 arch 现有的 guard/条件码选择,是不是从来没被这类边界输入测过(如果测试语料只覆盖"正常数字",边界语义 bug 会一直潜伏)?③新 arch 是否有旧 arch 没有的硬件保证,使某个 guard 变得多余(不要因为"两边都加了更安全"而无脑复制,要问清楚多余的 guard 是否只是死代码还是会引入错误路径)。写新 arch 的边界语义 e2e 测试时,顺手在旧 arch 上跑一遍同款测试。

### 3. arch 差异也能是简化机会,不只是要对抗的障碍

移植时除了处理"新 arch 要多做什么",也发现了"新 arch 反而更顺"的情形:`FCMPE` unordered 在 arm64 上直接置 `Z=0`,把 amd64 需要的 `jne`+`jp` 双跳合并成单条 `B.NE`;arm64 表 op 的槽地址计算结果存活在寄存器 X3,`SETTABLE` 不需要像 amd64 那样重新执行 `cvttsd2si` 把浮点键转回整数索引。

**Why**:两个 arch 的指令集/寄存器约定/标志位语义不是对称的,差异既可能要求"多做一步"也可能允许"少做一步"。只带着"移植 = 补齐 amd64 已有的东西"这个单向心态去写,容易漏掉后一类机会。

**How to apply**:移植某个 op 时,除了核对"amd64 这里做了什么、arm64 要不要也做",也主动问一句"arm64 的指令集/标志位/调用约定有没有让某个 amd64 步骤变得不必要"。不要机械翻译,值得花时间对照两份汇编看哪边指令数更少。

### 4. e2e 探针载体要避开无关 op 的噪声

`GETTABLE` ArrayHit 的 inline-hit 探针(断言 `dispatched < 100`)第一版失败,实测 `dispatched=1192`。不是 inline 快路径坏了,而是测试 kernel 用 upvalue 引用表,每次访问表之前先要经过一次 `GETUPVAL` 的 exit-reason 往返,把探针的 dispatch 计数淹没了。把表改成通过参数传入(纯寄存器路径,不经 upvalue)后,探针读数干净地反映了被测 op 本身的行为。

**Why**:「快路径命中计数」类断言的隐含假设是"测试 kernel 里只有被测 op 会产生 dispatch",但 kernel 里往往混有其它 op(为了让 kernel 能跑起来、能引用测试数据),这些无关 op 如果自己也走 exit-reason,会把计数污染成一个不可解释的数字。这是 [[prove-the-path-under-test]] guide「探针载体设计」维度的一个新实例——此前该 guide 的实例集中在"证明路径被走到",本条贡献的是"证明路径被走到之后,如何让计数只反映被测路径而非顺带路径"。

**How to apply**:写"快路径命中计数"类断言前,先枚举测试 kernel 里除被测 op 外,还有哪些 op 可能触发 exit-reason/dispatch(尤其是变量访问方式——upvalue vs 参数 vs 局部变量往往对应不同的底层路径)。载体要设计成"除了被测 op,尽量没有其它 op 会产生 dispatch",通常做法是把外部依赖(表、函数)通过参数传入而非闭包捕获,避免额外的 GETUPVAL/SETUPVAL 往返。

### 5. 调试代码清理误用 `git checkout` 回退了未提交的工作

在验证 GETTABLE inline 快路径是否真的 fire 时,往 emit 函数里插了 `println` 探针。验证完想清理这些临时探针,用了 `git checkout <file>` ——把同一个文件里 step 4 尚未提交的新代码也一起回退掉了,导致约 200 行代码需要重写一遍。

**Why**:`git checkout <file>` 是对文件整体的硬回退(回到最后一次 commit 或 index 的状态),它不区分"我想删的调试代码"和"我还没提交但想保留的正式代码"——只要它们在同一个文件里、都是相对上次 commit 的未提交变更,`git checkout` 会把两者一并抹掉。

**How to apply**:在一个已有未提交工作的文件上追加临时调试代码,清理时不要用 `git checkout <file>`(整文件回退)。正确做法二选一:①用 Edit 工具做精确的反向编辑,只删掉刚加的调试语句;②在插入调试代码前先 `git stash` 把已有未提交工作暂存,调试完之后 `git checkout` 清理探针,再 `git stash pop` 恢复正式工作。判断标准很简单:如果要清理的文件里还有其它未提交的、想保留的改动,`git checkout` 就是错误工具。

### 6. 每步独立 commit + 每步 fuzz 的节奏在移植轮工作良好

7 个实施步骤各自独立 commit,每步跑 fuzz smoke(60-150s,`-parallel=4`)+ 全测试套,没有一次把某步的回归漏到下一步才发现。UNM 的 NaN bug 修复(教训 2(a))独立成一个 commit,同一 commit 里双 arch 一起修。这是用户在上一轮(stopbleed)过程反馈"提交更频繁 + 单域"的直接应用,本轮验证了它在移植类工作里同样适用。

**Why**:移植类工作天然分步骤(一个 op 一步),步与步之间耦合度低,独立 commit + 独立验证使每步的回归责任边界清晰——如果某一步之后测试挂了,不需要在多步累积的 diff 里定位,直接就是当前这一步的问题。

**How to apply**:接到有清晰步骤划分的移植/扩面类任务时,默认按步骤切 commit 边界,每个 commit 配一次针对性验证(至少跑受影响的测试子集,理想情况下加一轮短 fuzz smoke)。不要攒到最后一次性 commit 大改动。

### 7. `HelperCompareSlow` 首次出现 dispatcher 向段内回传数据的双向通道

`host.Compare` 是 terminator 语义(比较结果要驱动分支跳转,需要 BB 目标,但 dispatcher 本身没有 BB 概念)。解法是 dispatcher 把 `host.Compare` 的结果(packed,bit0 是比较结果)写回 `exitArg0`,原生码段内的 resume 块读回这个值,自己做分支判断——这是 exit-reason 协议第一次出现"dispatcher 向段内回传数据"的形态,此前所有已交付的 exit-reason 场景都是单向(段内请求→dispatcher 处理→段内继续,不需要 dispatcher 传回计算结果)。

**Why**:exit-reason 协议原本的设计意图是把段内做不到的事情(涉及 Go 侧状态、栈可能增长的调用等)转交给 host,通常只需要 host"做完就好",段内继续往下走。但比较类操作的结果本身要参与段内的控制流决策,这就要求协议支持"host 算出一个值,交回给段内让它自己决定往哪跳"。

**How to apply**:这是为 arm64 后续 op(issue #37 未覆盖的 SELF / LEN / CONCAT 等,如果未来也走 exit-reason 化)记录的一条可复用参考——遇到"某个 op 的慢路径结果需要驱动段内分支"这类需求时,直接复用"packed 结果写入 exitArg0 + 段内 resume 块读回自行分支"这个既有形态,不需要重新设计协议扩展点。

## 其它(较小)

- 8 个 commit 均在 `feat/p4-arm64-exit-reason` 分支,`git log --oneline` 可查(`677ceb2`..`a4993cc`)。
- 沿用旧 arm64 scaffold 时要留意:某段代码"从未出错"可能只是因为它此前从未被特定边界输入(如 NaN)覆盖过,而不是它真的正确——教训 2(b)的旧比较条件码族就是这样潜伏的。

## 验证

- 8 commits 在 `feat/p4-arm64-exit-reason` 分支(`677ceb2` dispatcher 骨架 + GETUPVAL/SETUPVAL → `aad27bd` CALL 密度门 → `c83db6b` GETGLOBAL/SETGLOBAL → `634b1a3` GETTABLE/SETTABLE/NEWTABLE → `5751ffa` UNM → `f7d117d` 多值 RETURN → `508028a` UNM NaN bug 双 arch 修复 → `f8dcc41` arith/LT/LE 恢复 → `a4993cc` README + progress doc 收口)。
- 全测试套 + 每步 fuzz(`FuzzP4ForceAllPromote -parallel=4`,60-150s)+ difftest / conformance / luasuite 全绿,对照官方 5.1.5 oracle(darwin/arm64;oracle 从源码编 lua-5.1.5 到 `/tmp` + PATH override)。
- bench(Apple M5 Pro,go1.26.4,`-benchtime=2s -count=3` median,2026-07-03):HeavyArith 25.3ms vs P3 51.3ms(2.03×)/ HeavyFloatloop 25.3ms vs P3 62.4ms(2.47×)/ realworld 五本(fib/binary-trees/spectral-norm/n-body/fannkuch)全部 ≥ P3(fannkuch 与 HeavyRecursion 打平在噪声内)。
- README 中英双语 darwin/arm64 perf 表已更新;`docs/design/p4-method-jit/implementation-progress.md` §14.6 已写入本轮技术对账。
- `make all` 收尾运行中(收尾时状态,若失败需后续跟进)。

## promotion 候选

- **教训 1(移植前调查结论写 issue 评论、不留 `.llmdoc-tmp`)**:首次样本,暂留观察。若后续再出现"临时缓存丢失但持久渠道保住落点清单"或反例("只留 `.llmdoc-tmp` 结果下一轮要重新调查"),可考虑并入某篇 guide 的"移植/延后工作交接协议"节。
- **教训 2(移植 = 逐 op 语义重判,新 arch 测试给旧 arch 挖 bug)**:与 [[2026-07-03-issue40-arm64-stopbleed-round]] 教训 5(fuzz 机器多样性是覆盖度维度)邻接,同属"接入新硬件/新 arch 本身构成独立 bug 发现机会"这一更大家族,但触发机制不同(fuzz 随机性 vs 移植语义对照的确定性并排推导),注明家族关系、不建议合并。本条三个子实例(NaN sign-flip / 比较条件码族 / guard 必需性不对称)内部结构一致,建议下次再出现跨 arch 移植类工作时留意是否复现,累计 ≥2 个跨轮独立样本后考虑升 guide(候选落点:`design-claims-vs-codebase-physics` 或独立的"跨 arch 移植纪律"guide)。
- **教训 4(e2e 探针载体须避开无关 op 噪声)**:是 [[prove-the-path-under-test]] 现有"探针载体设计"维度的新实例,建议 recorder 评估是否作该 guide 补充实例(该 guide 已有九个独立实例,本条贡献的是"载体设计"这一具体子技巧而非新的反模式大类,可能适合作既有节的补充说明而非新增顶层节)。
- **教训 5(调试代码清理误用 `git checkout` 回退未提交工作)**:过程级小教训,单纯的工具使用纪律,暂不建议升 guide,留存供后续同类事故复核。

## 触发场景

- 一轮工作收尾时决定"某项调查/发现留给下一轮"(教训 1:确保落点清单写进持久渠道,不能只留 `.llmdoc-tmp`)。
- 做跨 arch(或任何跨硬件平台)移植类工作时(教训 2:逐 op 问「边界语义是否一致 / 旧 arch guard 是否被边界输入测过 / 新硬件是否让某 guard 变多余」三问题;教训 3:同时找简化机会不要只想着补齐)。
- 写「快路径命中计数」类白盒探针断言时(教训 4:先枚举 kernel 里有没有无关 op 会产生同类 dispatch,载体设计成只有被测 op 可能触发)。
- 在有未提交工作的文件上插入临时调试代码、准备清理时(教训 5:精确反向编辑或先 stash,不要 `git checkout` 整文件)。
- 有清晰步骤划分的移植/扩面类任务开工时(教训 6:默认按步骤切 commit 边界 + 每步验证)。
- 给受限执行环境(mmap 段/exit-reason 协议类)扩展新 op、且该 op 的慢路径结果需要驱动段内分支决策时(教训 7:复用「packed 结果写入交换字 + 段内 resume 块自行读回分支」既有形态)。

## 关联

[[2026-07-03-issue40-arm64-stopbleed-round]](直接前序:阶段 1 止血 + 预写 7 步实施顺序,本轮的执行蓝本)· [[2026-07-02-p4-beat-p3-opset-round]](exit-reason 协议 amd64 侧首次交付轮,本轮移植的源头,该轮教训 1「exit-reason 协议解 mmap+morestack 不兼容」/ 教训 2「invariant consumer 定义强度」/ 教训 4「fuzz 兜底 gen-only 快路径」与本轮教训 2 的 NaN 三实例同属"fuzz/新语境暴露既有快路径隐藏 bug"家族)· [[prove-the-path-under-test]](教训 4 候选补充实例;本轮 e2e 全程配 inline 命中 / 慢路径触达探针,是该 guide 纪律在移植轮的又一次应用)· [[perf-optimization-workflow]] §5(跨机器/跨参数基线对照——本轮 bench 数字已标 Apple M5 Pro / go1.26.4 / `-benchtime=2s -count=3` / 日期)· `docs/design/p4-method-jit/implementation-progress.md` §14.6(本轮技术对账落点)· issue #37 / issue #40 · PR(分支 `feat/p4-arm64-exit-reason`,8 commits `677ceb2..a4993cc`)
