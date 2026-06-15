---
name: p3-pw10-zerocross-stage3-round
description: P3 PW10 零跨界里程碑 ③ RETURN 拆帧子轮(承 R3/R3.5 双跨界消 h_return 侧)过程教训:计划阶段标记的「已识别未触发」风险须配套触发用例否则到落地时是测试盲区里第一次撞实(Option A 风险 #1 陈旧 &th.cur 由 memory 中的潜在风险撞实为 R3 indirect 三件套全挂的真 bug、gibCI wrapper 统一收口 syncCurFromSeg + currentCI)、difftest 结构盲区家族新维度「wasm 内拆帧快路径命中形态结构性失明」(临时禁 syncCurFromSeg 让 stale th.cur 必然腐蚀仍 difftest V1-V13 全绿、R3 indirect 三件套全挂——快路径命中实证须用 A/B 实验 + 命中探针,非靠 difftest 全绿)、跨机器 perf 基线漂移(memory/计划文件历史 perf 数字与码库当前实测不同硬件差 30%+、误判回归前必须同 commit 同硬件复测、memory 里写 perf 数字必须标硬件/参数/日期)。3 提交 8aa4c02..1bff7d2
metadata:
  type: reflection
  date: 2026-06-15
---

# P3 PW10 零跨界里程碑 ③ RETURN 拆帧子轮反思(消 h_return 半侧)

> 范围:承 [[p3-pw10-r3-call-indirect-round]] R3 + R3.5 已交付(`call_indirect` 直调消 `code.Run` 重入 + host helper `WithFunc`→`WithGoFunction` 消反射装箱;call 核 0.49x 是**真实** `h_call`+`h_return` 双跨界税)。本轮交付零跨界 ③ RETURN 拆帧侧(消 `h_return` 半侧)。提交链:**8aa4c02** 基建-a closure slot 缓存 → **455d1bd** ③a `savedTop` 基建 → **1bff7d2** ③b `emitReturn` 守卫快路径。Stage 0/1/2 此前已交付,本轮交付 Stage 3。R4 全消 `h_return` + Option B inline frame-build 消 `h_call` 仍在后续。

## 核心教训(按强度排序)

### 1. Option A 风险 #1「陈旧 &th.cur」从 memory 标记的「潜在风险」撞实为真 bug——计划阶段标记的「已识别未触发」风险须**同步配上触发用例**,否则到落地时它是「测试盲区里第一次撞实」

PW10 计划早在 [[p3-pw10-r1-r2-callinfo-migration-round]] 教训 3「`th.cur` 稳定地址」之后,把 Option A 风险 #1「陈旧 `&th.cur`」标为「最高 UAF 风险」——`Stage 1b` 据此设计了 `syncCurFromSeg`(段→`th.cur` 反向同步 wrapper)+ 「持有者审计」清单。但 Stage 1b 落地时是「**验证性 no-op**」(Wasm 不写、字恒等 ciDepth),`syncCurFromSeg` 仅接在 `enterGibbous` 一处(`code.Run` 返回后)——审计虽做、wrapper 虽存在,**但当时实际尚无 Wasm 路径会去改段 `ciDepth` 字**。

③b 是**第一个真正改段 `ciDepth` 字的 Wasm 路径**(emitReturnFast 守卫快路径在 Wasm 内弹帧)。落地后第一次撞实:R3 indirect 测试报 `attempt to call upvalue 'helper' (a number value)`——陈旧 `&th.cur` 让后续 Go helper 读旧帧、按错帧寻址、值栈索引腐蚀。修复:抽 `gibCI` wrapper(= `syncCurFromSeg(th)` + `currentCI(th)`)统一收口,`gibbous_host.go` 内 36 处 `currentCI`→`gibCI`(perl 批量 + 手工补 `PopErrFrame` / `SetSavedPC` 等错误路径站点)。

**Why**:计划阶段把风险标出来,只是「我知道这里会有雷」,**不等于**「雷在测试里会被踩到」。Stage 1b 落地时风险**尚未真实存在**(无 Wasm 写、字恒等),`syncCurFromSeg` 的接入点只覆盖了**当时已存在**的边界(`enterGibbous`);**潜在未来雷区**(Stage 2/3 之后所有读 `currentCI` 的 21 个 Go helper 入口)未被前瞻覆盖。等到 ③b 真把段 `ciDepth` 改了,陈旧 `th.cur` 的传染面就铺开到全部 helper。

这是 [[p3-pw9-acceptance-perf-round]] / [[p3-pw10-r3-call-indirect-round]] 「测试盲区」家族的新维度:**计划阶段标记的风险若没在标记轮就配上一个会撞响它的测试用例,到真撞响时已是「测试盲区里第一次撞实」**——本轮 R3 单元测试抓到了,但 V1-V13 difftest 全绿,完全靠 R3 那条单测「g→f→helper 全升 + helper 弹帧后 f 续算」凑出最小触发形态。**若 R3 当初没建那条用例**,bug 会跟着 ③b 一起合入,在生产里随机 UAF。

**How to apply**:写计划/spike 时**每标一个「风险已识别但未触发」**,同步问「**这个风险触发的最小用例长什么样?有没有现存测试会撞响?若没,本轮要不要预先补一个?**」——本轮 Option A 风险 #1 的最小触发 = R3 indirect 测试链(g→f→helper 全升、helper 在 Wasm 内弹帧后 f 续算)。这条用例 R3 轮就有(为验 indirect 直调),所以本轮撞响时抓到了——但纯运气。**纪律**:计划文件里每个「风险标记」末尾加一个「**触发该风险的最小测试用例 = ___**」,若空白,本轮就补;别留给「下次落地时再看」。这是 `prove-the-path-under-test` 家族的**前瞻维度**——既有家族(PW5/PW6/PW9/R1-R2/R3)管「测了没」,本条管「下次会触发该风险的路径,**这次就先建出来**」。

### 2. difftest 结构盲区家族新维度:「wasm 内拆帧快路径**命中形态**结构性失明」——A/B 实验实证 difftest 对快路径命中可达性零信号

承 [[p3-pw10-r3-call-indirect-round]] 教训 4「全成功语料对错误路径失明」。本轮做了**实证 A/B 实验**:暂时禁掉 `gibCI` 内的 `syncCurFromSeg`(让 stale `th.cur` 在 ③b 弹帧后必然腐蚀),跑 `difftest V1-V13 force-all` 仍**全绿**;但 R3 indirect 三件套**全挂**。证明 difftest 现行语料**结构上不触发 ③b 守卫命中形态**(caller=gibbous + 定额返回 nresults=nret + 无开放 upvalue)。

**Why**:V8 `p3_nested_call`(`outer(x) = inner(x) + inner(x+1)`)看起来应触发——但 difftest 顶层 `for i=1,40 do s = s + outer(i) end` 由解释器循环驱动 `outer`,`outer→inner` 是 **crescent→gibbous**(非 gibbous→gibbous),故 ③b 命中 **0 次**。R3 indirect 用 `g→f→helper` 三层 + 直接 `Call("g")`(`g` 经 `enterGibbous` 入口、`f` 跑 gibbous、`f→helper` 才是真 gibbous→gibbous),③b 守卫全过、真命中。**difftest 结构上不构造「顶层升层 gibbous + 内层 gibbous→gibbous + 定额返回」形态**,故 ③b 命中可达性对 difftest **完全不可见**。

**How to apply**:同 [[p3-pw10-r3-call-indirect-round]] 教训 4 的扩展——任何新加的「**快路径**」(条件命中后绕过 Go 慢路径的代码),发布前**必须**:
- **(a) 加一个专用命中探针**(本轮 `doReturnHits` 计数器 + `TestPW10ZeroCross_ReturnFastHit` 测,实证 `helper→f` 走快路径、`f→g` 经 `DoReturn` 慢路径);
- **(b) 用 A/B 实验实证 difftest 对该快路径的命中是否敏感**——临时关掉快路径副作用(本轮:禁 `syncCurFromSeg`)看 difftest 是否暴露,**若仍全绿,即证 difftest 对此结构盲区,必须补针对性语料**(留 followup)。

判据:「difftest 全绿 ⟹ 新路径正确」**只在 difftest 触发该路径时成立**——结构盲区下零信号。

**与上轮的关系**:[[p3-pw10-r3-call-indirect-round]] 教训 4 是「错误路径盲区」(语料全成功,错误路径不可达);本轮是「wasm 内快路径**命中形态**盲区」(语料结构上不触发 gibbous→gibbous 定额返回)。**两实例已足以强化 `prove-the-path-under-test` 家族**或新立 `difftest-structural-blindness` guide。recorder 定夺。

### 3. 跨机器 perf 基线漂移——memory/计划文件的历史 perf 数字与码库当前实测**不同硬件**会差 30%+,误判回归/收益前必须同 commit 同硬件复测

本轮 Stage 4 一开始误判 ③ 引起 table/mixed 回归。memory 记「R3.5 后 table 1.26x / mixed 1.14x」(那是 R3.5 反思轮的实测数),本机 HEAD 测 table **0.88x** / mixed **0.99x**,差 30%+。怀疑是 ③b/`gibCI` 引入开销 → 临时关 ③b 跑——数字几乎没动。直接 `git worktree` 切到 R3.5 commit `1bf9d53` 同环境跑:**R3.5 本机也是 table 0.89x / mixed 1.00x**。差异源自不同硬件(本机 Xeon 6982P)/ bench 参数,**非代码回归**。

**Why**:perf 数字是「**代码 × 硬件 × bench 参数 × 系统负载**」四元函数。memory/reflection 记的历史数字是**当时那台机器、当时参数下的快照**,**不具跨机器可比性**——但读者(包括我)默认它是「跨时间稳定的代码属性」。这是 [[p3-pw9-acceptance-perf-round]] 教训 4「瓶颈实测推翻模型」的**时效性维度**:那里是「立项时模型错了」(基于空测推出 memory-resident 根本限制),这里是「**历史快照与当前快照属性差异**误判为代码回归」。

**How to apply**:任何性能判定**回归/收益**前必须**同 commit 同硬件同参数对照**(本轮 `git worktree` 拉 R3.5 commit 同环境跑,3 分钟得对照)——**绝不**拿 memory/reflection 里的历史数字当现行基线下结论。判据:看到 perf 数字与历史不符时,**先问「这是同硬件同参数的对照吗?」**;若否,**先建对照**再判回归。

次一级纪律:**memory/reflection 里写性能数字时,必须标硬件 / 参数 / 日期**(本轮发现 `docs/design/p3-wasm-tier/implementation-progress.md` / `llmdoc/index.md` / `llmdoc/startup.md` 三处均缺标),否则数字会被未来读者误当跨机器属性。这是上轮 R3/R3.5 提到「-benchmem 双维度」之外的另一条 perf 判定纪律,可与之并列。

## 其它(较小的过程点)

- **用户在 Stage 4 后三选一拍板「先校准 memory + 文档,再做 ④」**(选项 3,而非直接做 ④ 或继续追 ③b 假回归)——承 [[p3-pw10-r3-call-indirect-round]] 「矛盾测量上抛给决策者」纪律。文档校准是「不修代码、纯认知校准」的轻动作,先做使后续 ④ 立项基于真实数字。

## 验证

四 build(默认 / `wangshu_p3` / `wangshu_p3+wangshu_profile` / `wangshu_p3+wangshu_profile+wangshu_trace`)+ V1-V13 difftest + R3 indirect 三件套 + `TestPW10ZeroCross_ReturnFastHit` 命中探针(实证 helper→f 走快路径、f→g 经 DoReturn 慢路径)+ `-race`×3 + GC-stress 双模,**全绿**。

Stage 4 实测对照:R3.5 commit `1bf9d53` vs HEAD `1bff7d2`,同机 Xeon 6982P `2s × 3 count`:
- **HEAD loop 2.95x**(+10% over R3.5 2.67x),table / call / mixed 持平
- **call 仍 0.52x** ⟹ ④ 必做(R4 消 `h_return` 全侧或 Option B inline frame-build 消 `h_call`)

## promotion 候选

- **教训 1**(计划阶段标记风险须配套测试用例)——可进 [[design-claims-vs-codebase-physics]] 或独立小条目,**首次样本但信号清晰**:计划阶段「已识别未触发」风险与落地时「测试盲区里第一次撞实」之间的鸿沟,是 `prove-the-path-under-test` 家族的前瞻维度。recorder 定夺独立条目 vs 并入 design-claims 作「时间维度」补充(上轮 PW7 已有「前序里程碑追溯性溶解后续难点」时间维度,本条是「前序里程碑追溯性产生后续盲区」对偶)。
- **教训 2**(difftest 结构盲区家族扩展)——与上轮 [[p3-pw10-r3-call-indirect-round]] 教训 4 配对,**该家族第 2 实例(快路径命中形态盲区,与错误路径盲区相邻不同)**。recorder 定夺:新立 `difftest-structural-blindness` guide 聚合「错误路径盲区(R3)+ 命中形态盲区(本轮)+ apples-to-oranges 工作负载错配(R1-R2 教训 5)+ vararg 不升层(PW9)」四个独立实例,或并入 `prove-the-path-under-test` 候选。已跨过提升阈值。
- **教训 3**(跨机器 perf 基线漂移)——可进 [[perf-optimization-workflow]] 新 §6,与 §1「profile 先行」 / 上轮 R3/R3.5「-benchmem 双维度」配对成「**perf 判定纪律三件套**」(找对项 / 量对维度 / 同 commit 同硬件对照)。首次样本但信号强,因为本轮 30% 漂移直接误导了「③ 引起回归」的初步判断。

## 触发场景

写计划/spike 时标记「风险已识别但未触发」(**每个风险末尾加「触发该风险的最小测试用例 = ___」,空白本轮补**)、新加「**快路径 / 守卫绕过**」代码发布前(用 A/B 实验 + 命中探针实证 difftest 对该路径是否敏感、不敏感则结构盲区须补针对性语料 followup)、看到 **perf 数字与 memory/reflection 历史不符**(先问「同硬件同参数对照了吗」、不是 → 先 `git worktree` 对照、再判回归)、memory/reflection 里**写 perf 数字时**(必须标硬件 / 参数 / 日期,否则被未来读者误当跨机器属性),看这篇。

## 关联

[[p3-pw10-r3-call-indirect-round]](**直接前序**:R3 + R3.5 教训 1+2「-benchmem 双维度 / 机制完成 ≠ 收益交付」+ 教训 4「错误路径盲区」是本轮三教训的同家族前序;本轮教训 3 是其 §4 收尾纪律的**时效性维度**扩展,教训 2 是其教训 4「difftest 错误路径盲区」的新邻接维度「快路径命中盲区」)· [[p3-pw10-r1-r2-callinfo-migration-round]](教训 3「`th.cur` 稳定地址」是本轮 Option A 风险 #1 标记的源头——稳定地址解了 held-pointer 重定位、但段 ciDepth 字仍可变,本轮 ③b 写段后陈旧 `&th.cur` 的传染面是其结构性修复**仅覆盖部分维度**的延伸教训)· [[p3-pw9-acceptance-perf-round]](教训 4「瓶颈实测推翻模型」是本轮教训 3「跨机器 perf 基线漂移」的立项侧对偶——立项侧时态 vs 时效性侧)· [[design-claims-vs-codebase-physics]](本轮教训 1「计划标记风险须配套测试」是其「设计稿假设落到码库 physics」的邻接——「未撞响的风险」与「未测的假设」同结构,前者按时间维度延展)· [[perf-optimization-workflow]](§1 profile 先行 / 上轮 spike 双维度——本轮教训 3 是其新 §6 候选「跨机器基线对照」,可与上轮「机制完成 ≠ 收益交付」「-benchmem 双维度」并列成 perf 判定纪律三件套)· `internal/crescent/gibbous_host.go` `gibCI`(`syncCurFromSeg` + `currentCI` 统一收口)/ `internal/gibbous/wasm/translate.go` `emitReturnFast`(③b 守卫快路径)/ `internal/crescent/gibbous_zerocross_return_test.go`(`TestPW10ZeroCross_ReturnFastHit` 命中探针,doReturnHits 计数)
