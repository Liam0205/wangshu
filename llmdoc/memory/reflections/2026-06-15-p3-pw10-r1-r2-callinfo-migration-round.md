---
name: p3-pw10-r1-r2-callinfo-migration-round
description: P3 PW10 消除跨层调用税(Phase0 spike + R1 共享 funcref 表 + R2 CallInfo 迁入 linear memory = 长期延后的 VS0-e;R3 直调收益尚未交付,PW10 在飞行中)过程教训:里程碑级架构改动配 spike 闸门(承 PW0/PW9)且第二探针解 architecture FORK 能挖出远比第一可行方案简单的设计(S-C 共享表 vs S-A/S-B rebuild-all)、高风险物理数据结构迁移的「收口→只写影子→翻转→退役」增量剧本并把唯一 UAF-风险翻转单独隔离一提交、th.cur 稳定地址洞见用结构性修复消解 design-claims §2 反复出现的 held-pointer 重定位雷区(而非靠纪律躲)、wangshu_trace 自检双职(真实打包正确性安全网 + 给 lint 误判「未使用」的代码正名,且 trace-gated 自检须随重构保持接线)、空测/不公平基准陷阱第 N 次复发(这次是 apples-to-oranges 工作负载错配,非未走加速路径)
metadata:
  type: reflection
  date: 2026-06-15
---

# P3 PW10 R1/R2 消除跨层调用税轮反思(spike 闸门 + 共享 funcref 表基建 + CallInfo 迁入 linear memory)

> 范围:PW10 要消除 PW9 暴露的 gibbous→gibbous 跨层调用税(每次 gibbous→gibbous 调用经 `h_call` 是一次 ~143ns 的**双 host 跨界** → 调用密集核比解释器**慢 7 倍**,call 核 0.14x)。本轮交付 Phase 0(spike 闸门)+ R1(共享 funcref 表基建)+ R2(完整 CallInfo 迁入 linear memory = 长期延后的 **VS0-e**)。**R3(真正付清性能的 `call_indirect` 直接分派)+ R4 + R5 尚未做——PW10 仍在飞行中**。13 提交(`457559b..9d70247`):Phase 0 spike(`spike/p3indirect/` S-A/S-B/S-C)→ R1 共享 funcref 表(各 Proto 模块 active element 段自注册 `run`)→ R2a(COLD 字段收口走 accessor)→ R2b-1(arena 段只写影子 + `wangshu_trace` 往返自检)→ R2b-2(`growCISeg` 动态增长盖满深度,仍只写)→ R2b-3(GC 根扫描翻转为从段 READ `cl`——唯一最高 UAF-风险步,单独隔离)→ R2b-4(退役 Go slice,`currentCI`→稳定 `th.cur` 镜像)。承 `04-trampoline.md` 跨层机制 + `02-translation.md` VS0-e + `09-perf-roadmap` PW10 立项。

## 核心教训

### 1. 里程碑级架构改动配 spike 闸门(镜像 PW0);而一个解 architecture FORK 的第二探针,能挖出比第一个被证可行方案远更简单的设计

PW10 的真解(gibbous→gibbous 直接分派、不经 host)被两条**码库 physics 事实**挡住(承 PW9 教训 5 / investigator):(a) 每个 Proto 编译进**自己独立的 wazero module** → 跨 module 调用**必须**穿 host;(b) Lua 调用帧活在 Go 里(`th.cis` 切片)。生死未知数:**wazero 能不能做增量提升?** 我建了 `spike/p3indirect/`(独立 go module,镜像 PW0 的 `spike/p3boundary`)。

- **S-A** 实测 intra-module `call_indirect` ≈ **2.5ns**(比 ~35ns 的裸 host 跨界便宜 **14 倍**,远在 30ns 目标之下);
- **S-B** 证明 rebuild-all 生命周期可行(整模块重编译 + 实例热替换,≈1.2ms@256fn,可重入提升安全)。

到这里 spike 是**绿的** → 本会就此承诺 **Arch-1「rebuild-all」**——但它**复杂**:代际实例、跨代分派守卫、O(N²) 重编译。**关键决策**:我没有止步于「难的那条路可行」,而是**补了 S-C** 去解一个我先前一笔带过的 architecture FORK——**各 Proto 模块能不能共享同一张 imported funcref 表**(每个模块经 active element 段把自己的 `run` 自注册进表,gibbous→gibbous = 经共享表 `call_indirect`,**无需 rebuild**)?**S-C 通过**(跨 module 经一张共享 imported 表 `call_indirect` 在 wazero 里可行,≈2.5ns,零跨 module 惩罚)。这条 **Arch-2「共享表」** 比 Arch-1 **戏剧性地简单**——保持现有「一模块一 Proto」编译路径几乎不动,无 rebuild、无实例生命周期、无 O(N²)。

**Why**:一个 spike 说「你那条难路可行」**不是终点**。S-A/S-B 证的是「Arch-1 这条**具体路径**能走通」,但「能走通」与「这是最简路径」是两件事。我先前把 FORK(rebuild-all vs 共享表)一笔带过,默认了第一个想到的可行方案(rebuild)。如果不补 S-C,我会基于「Arch-1 已被 spike 证绿」这个**真实但不完整**的结论,去建整个 Arch-1 的代际/跨代/O(N²) 复杂度——而那套复杂度**完全不必要**。S-C 的成本是**一个探针**;它省下的是**建整个 Arch-1 复杂度**的代价。这与 [[perf-optimization-workflow]] §3「可疑优化的 benchmark 否决门」同构,但发生在**架构选型**层:那里是「实测否决一个理论更快的优化」,这里是「实测挖出一个比已证可行方案更简单的架构分支」。

**How to apply**:任何**里程碑规模**的架构改动,spike 闸门先行(承 PW0/PW9 教训 5)。但 spike 绿灯**只意味着「这条具体路可行」,不意味着「这是该走的路」**——若存在一个你**没有审视**的 architecture FORK,**在动手建难路之前,先探那条更简单的分支**。判据:当 spike 证明的方案带着明显的复杂度(代际管理 / O(N²) / 生命周期机器),停下来问「有没有一个我默认排除了、但其实没验证过的更简单分支?」,用一个增量探针去打它。多花一个探针的成本,可能省下整个复杂架构的建造与日后维护。这是对 PW9 教训 5「里程碑级改动配 spike 闸门」的**延伸**:不仅要 spike 那条 make-or-break 的难路,还要 spike 那个未审视的 FORK。

### 2. 高风险物理数据结构迁移(VS0-e CallInfo 迁移)的增量剧本,并把唯一的 UAF-风险翻转单独隔离

把 CallInfo 从 Go `[]callInfo` 切片搬进 linear-memory arena 段,触及 GC 根扫描、growStack、协程栈、~108 个调用点。我把它拆成一串**可独立提交、零回归**的步骤:

- **R2a**:把所有 **COLD** 字段访问收口走 accessor(`base`/`pc` 仍热/直访)——纯收口,行为不变;
- **R2b-1**:并行 arena 段作为 **WRITE-ONLY 镜像** + `wangshu_trace` 往返自检——段**从不被读**,故行为**可证**不变;
- **R2b-2**:`growCISeg` 动态增长使段盖满全深度,**仍只写**;
- **R2b-3**:GC 根扫描**翻转**为从段 READ `cl`(closure)——**唯一最高 UAF-风险步**,**单独隔离一提交**;
- **R2b-4**:退役 Go slice,`currentCI`→稳定 `th.cur` 镜像。

关键在 R2b-3 的**隔离**:GC 根扫描如果漏读 `cl`(closure 没被标记)→ closure 被 GC 释放 → UAF,这是**灾难性失败模式**。我把它**单独放在两个安全提交之间**(R2b-2 只写不读 = 安全,R2b-4 退役 = 在 R2b-3 已证安全之后),这样一旦 R2b-3 出问题,**故障必然归因于这一步**,不会与任何其它改动混淆。

**Why**:一次物理搬迁里,绝大多数步骤是**语义中性的**(收口、加只写镜像、扩容),只有极少数步骤是**真正的翻转**(开始从新存储读、退役旧存储)。把「只写镜像先行」排在前面(R2b-1/2 **什么可观测的都不改**——段写了但没人读,wangshu_trace 往返自检证明打包/解包正确),让 R2b-3 成为**唯一一处真实翻转**,把风险**变得可读**(legible):reviewer 和 bisect 都能一眼看出「危险在这一个提交」。如果把「加段 + 读段 + 退役 slice」糅进一个大提交,GC 下的 UAF 将无法二分归因到是打包错了、读错了、还是退役早了。

**How to apply**:做物理数据结构迁移(把某结构从 Go 堆搬进 arena / linear memory / 池)时,套 **「收口 → 并行只写影子 → 翻转 → 退役」** 四段(这是 VS0-a/b/c/d 早期 P3 工作确立的 house style,本轮 VS0-e 再次确认)。其中若存在**单一灾难性失败模式步骤**(GC 根漏标 = UAF),把它**单独隔离在一个提交里**,前一提交安全(只写不读)、后一提交安全(在它已证 OK 之后才退役),使该步的失败**不可能与其它改动混淆**。只写影子阶段务必配一个往返自检(见教训 4),证明搬迁的**打包正确性**先于翻转读取。

### 3. `th.cur` 稳定地址洞见:用结构性修复**消解** design-claims §2 反复出现的 held-pointer 重定位雷区,而非靠纪律躲它

热循环把 `ci := currentCI(th)` 持有跨越**整个帧 + CALL 分派**。旧的 `currentCI` 返回 `&th.cis[len-1]`——一个**指进 Go 切片**的指针,切片 append 时会搬家(就是 [[design-claims-vs-codebase-physics]] §2 反复警告的那个重定位陷阱的**对偶**:那里是 arena 段 grow 搬走 `$base`,这里是 Go slice append 搬走 `&th.cis[i]`)。R2b-4 的设计:`th.cur callInfo` 是**每线程的当前帧 scratch 结构**;`currentCI` 返回 `&th.cur`,其地址**稳定**(永不重定位——它是一个**固定的 struct 字段**,既不是 slice 元素、也不是 arena 偏移)。非当前帧经 `ciAt(depth)` 作**只读值快照**读取(form-Y:每次访问从 depth 重算,绝不缓存指针)。

**Why**:design-claims §2 此前的解法(PW6 `$base` / PW7 TFORLOOP)都是「**翻转后回传刷新值**」——即承认 token 会失效,然后在每个失效点刷新它。那是**纪律性**修复:你必须**记得**在每次跨层调用后重新 fetch。但 `th.cur` 是**结构性**修复:把当前帧放进一个**地址恒定的存储**(固定 struct 字段),重定位雷区就**不存在了**——`&th.cur` 永远有效,因为它指向的东西从不搬家;非当前帧用值快照(form-Y),从不持有会失效的指针。雷区不是被「小心地绕过」,而是被**构造性地消除**(stable scratch + value snapshots)。这是 §2 雷区的一次**升级应对**:从「记得刷新」进到「让它无从失效」。

**How to apply**:当一个重定位/held-pointer 雷区**反复出现**(同一个 §2 已经踩了 PW6/PW7/本轮多次),不要再加一轮「记得在 X 之后重新 fetch」的纪律,而是找一个**结构性修复**——一个**稳定的间接层**(地址恒定的 scratch 字段)+ 值快照(form-Y 重算寻址),让那个指针**无从失效**。判据:如果某个指针的危险在于「它指的东西会搬家」,问「我能不能让它指向一个**永远不搬家**的东西(固定 struct 字段),并把所有'可能搬家'的访问改成每次从索引/深度现算的值快照?」。这是对 [[design-claims-vs-codebase-physics]] §2 的**正面扩展**——§2 教「token 会失效,翻转后回传刷新值」,本轮补「若雷区反复,优先改为稳定地址 + 值快照,让它根本不会失效」。

### 4. `wangshu_trace` 自检双职:既是真实的打包正确性安全网,又给 lint 误判「未使用」的代码正名(且 trace-gated 自检须随重构保持接线)

R2b-1 的 `readCISegInto` 当时**只被测试用**(生产侧的读翻转要到 R2b-3 才来),于是 lint 标它 unused。我没有 suppress,而是把一个 `verifyCISeg`(往返读回、断言段与 Go 权威一致)接进 `enterLuaFrame`,门控在一个 `ciMirrorCheck` const 下(wangshu_trace 下为 true,默认编译掉为 false)。这一手同时做到三件事:

- **(a) 真实安全网**:打包/解包 bug **在 push 现场立即 panic**,而不是在 R2b-3 翻转读取之后**远处**才浮现;且这次 trace-tag 跑**贯穿整个 difftest 层间套 + crescent 测试**(每一次帧 push 都被验证)——比单元往返测试强得多;
- **(b) lint 正名**:`readCISegInto` 变成**生产引用**,lint 通过;
- **(c) 复用既有模式**:同样的「build-tag 门控 debug const」模式 `traceExec` 早就有。

**踩了同一个 lint 坑两次**(R2b-1 和 R2b-4——R2b-4 重写时把 `verifyCISeg` 调用**丢了**,`readCISegInto` 又变 unused)。说明:**一个 trace-gated 自检必须随重构保持接线**,否则它会重新变成「未使用」。

**Why**:引入一个**并行镜像**数据结构时,最大的风险是「镜像与权威悄悄不一致」——而这种不一致在镜像**还没被读**的阶段是**不可观测的**(R2b-1/2 段只写)。一个往返自检(写进去、立刻读回来、断言等于权威)把这个**潜伏**的打包错误变成**即时** panic,且把它钉在**离 bug 最近**的地方(push 现场)而非翻转读取之后的远端。它的副产品是给「为未来翻转准备、当前还没被生产读」的代码一个**合法的生产引用**,绕开 lint 的 unused 误报——这比 suppress 干净,因为它真的在**用**那段代码(用来自检)。但门控在 trace const 下意味着:任何重构若把自检调用点删了,正名也随之失效,lint 坑复发。

**How to apply**:引入并行镜像数据结构(影子段 / 副本)时,配一个 **build-tag 门控的往返自检**(写→立即读回→断言等于权威),接在写入点(这里 `enterLuaFrame`)。它一举两得:(1) 把潜伏的打包不一致变成即时 panic,且在 trace tag 下**贯穿整个测试套**跑比单元往返强;(2) 让「为未来读翻转准备、当前未被生产读」的读函数获得合法生产引用,绕开 lint unused 误报。⚠️ 注意:trace-gated 自检的接线**会在重构中被无意删掉**(本轮 R2b-1/R2b-4 各踩一次),重写涉及自检点的代码时,清单化确认自检调用仍在。

### 5. 空测/不公平基准陷阱第 N 次复发——这次是 apples-to-oranges 工作负载错配,不是「未走加速路径」

给 4 个 benchmark 文件加 gibbous 列时,跳出**惊人**的数字:baseline loop「慢 20 倍」、arith「慢 5 倍」。两个都**不是真回归**:

- **(a) baseline gibbous「慢 20 倍」**:gibbous 把 kernel 包进一个内层函数**调 50 次**(为强制提升——顶层 chunk 是 vararg、被 F1 排除,正是 PW9 那个陷阱),但拿来对比的是**既有的 `_Wangshu` bench**,它跑**裸的顶层循环一次**——**apples-to-oranges**(wrapped×50 vs bare)。修复:加一个**匹配的 `_WangshuKernel` 列**(同样的 wrapper,force=false);
- **(b) arith「慢 5 倍」**:是**调用入口**开销——多项式**每次 kernel 调用只算一遍**,所以 50 次 gibbous→gibbous `h_call` 跨界(R3 没做)**主导**了便宜的算术。隔离探针证明:**同样的算术体**放进一个**循环**里,gibbous 跑**快 1.88 倍**。

于是每个数字都**自洽**了:gibbous 在**计算/循环密集**上赢(1.5-2.5x),在**调用密集**上输(h_call 税,R3 将修)。

**Why**:这是 P3 内**空测/基准不公平**家族的又一实例(PW5 inline-proof / PW6 TierStuck no-op / PW9 vararg 空测),但**新形态**:PW9 是「**加速路径根本没被走到**」(vararg 不升层 → crescent==crescent),本轮是「**两列测的根本不是同一种工作负载形状**」(wrapped×50 vs bare;call-entry vs in-loop)。两者都让一个**与架构矛盾**的数字（「加速却慢 20 倍」）被生产出来,但根因不同:前者是路径替身,后者是工作负载错配。一个「慢 Nx」**与架构预期矛盾**时(spike 已证 intra-module 直调 14 倍便宜、算术在循环里 1.88x),正确反应是**隔离探针**(把算术单独放循环里量),不是 panic 接受那个数。

**How to apply**:读一个 tier-vs-tier 或 A-vs-B 基准数之前,验**两件事**:(1) 加速路径**真被走到**(PW9 教训:正向 tier 断言 / 非空载体);(2) 两列测的是**同一种工作负载形状**(本轮延伸:wrapper 包法、调用次数、算到 in-loop 还是 call-entry 必须对齐——否则加一个**匹配列**如 `_WangshuKernel`)。一个「慢 Nx」**与架构矛盾**时,触发**隔离探针**(把可疑成分单独拎出来量,如「同样算术放循环里」),而非接受那个数或据此立错结论。这已是该家族**第 4 个独立实例**,**强化** PW9 留下的「prove-the-path-under-test」promotion 候选——基准公平性纪律(被走到的路径 + 匹配的工作负载 + 惊人数字触发隔离探针)三件套,跨过了应当成为 first-class guide 或强力并入 [[perf-optimization-workflow]] 的阈值。

## 其它(较小的过程点)

- **profiling 税要诚实拆分**:`wangshu_profile` 给 crescent 加 ~28%(OnEnter/OnBackEdge 采样),所以 bench-all 目标让 **crescent/gopher 跑默认 tag、gibbous 跑 p3+profile 分开**——统一 tag 会**扭曲 crescent 基线**(给基线背上它实际不付的采样税)。诚实的数据要求拆开跑,不能图省事统一。
- **机械 ~108 站点转换委托 worker、安全关键站点亲验(trust-but-verify)**:把 `th.cis`→`ciDepth`/`ciAt`/`truncateCI` 的机械转换给 worker 配精确规则,但**协程 resume + traceback 站点亲自验**(安全关键)。worker 正确地**标记**了它不得不动一个 `_test.go` 文件(那个测试拿被退役的字段当 oracle)——一次对「别动测试」的**被迫偏离,透明上报**。
- **基准 harness bug 暴露为硬 FAIL(好事)**:一个 transform warmup 在设置输入 global **之前**就调了 `evaluate()` → nil 算术,作为**硬 FAIL** 跳出来——**好事**,说明 force-all 全提升路径被**充分行使**到能抓住 harness 自身的错误(承 PW9「force-all 入口」纪律)。

## 验证(每子里程碑)

R1/R2 各步:4 build 组合(default / wangshu_profile / wangshu_p3 / both)+ `-race` + difftest 层间套逐字节一致 + GC 压力;R2b-1/2 `wangshu_trace` 下 `verifyCISeg` 往返自检贯穿整套(每帧 push 验证打包正确)+ R2b-3 GC 根扫描翻转后 closure 可达性 stress(证段读出的 `cl` 被正确标记、无 UAF)。Phase 0 spike:`spike/p3indirect/` 独立 module S-A(intra-module call_indirect ≈2.5ns)/ S-B(rebuild-all ≈1.2ms@256fn 可重入)/ S-C(共享 imported 表跨 module call_indirect ≈2.5ns 零惩罚)。基准:加 `_WangshuKernel` 匹配列后数字自洽(loop/compute 1.5-2.5x;call-dense 仍亏,待 R3)。**R3/R4/R5 未做,性能收益未兑现。**

## 促成的稳定文档更新

- `docs/design/p3-wasm-tier/implementation-progress.md`:PW10 行(R1/R2 ✅,R3/R4/R5 pending)+ §12 PW10 对账(Phase0 spike S-A/S-B/S-C 三探针裁决与 Arch-2 共享表选型 / VS0-e CallInfo 迁移「收口→只写影子→翻转→退役」+ R2b-3 UAF-风险隔离 / `th.cur` 稳定地址消解 §2 重定位雷区 / `verifyCISeg` 自检双职 / 基准工作负载错配修复)+ VS0-e 标记完成。

## promotion 候选

- **教训 5**(基准公平性:被走到的路径 + 匹配的工作负载 + 惊人数字触发隔离探针)——**该家族第 4 个独立实例**(PW5 inline-proof / PW6 TierStuck no-op / PW9 vararg 空测 / 本轮工作负载错配),且本轮贡献了**新形态**(apples-to-oranges 工作负载,而非路径替身)。**强烈强化** PW9 留下的 `prove-the-path-under-test` promotion 候选——把「被测路径真被走到」与「两列测同一工作负载形状」与「与架构矛盾的数触发隔离探针」聚为一个**基准公平性判断框架**。recorder 定夺:新立 guide vs 并入 [[perf-optimization-workflow]](本轮的「隔离探针解矛盾数」与 perf §3「benchmark 否决门」、§4「归因诚实」天然相邻)。
- **教训 1**(里程碑级改动配 spike 闸门 + **探未审视的 FORK,而非只探第一可行路**)——PW0/PW9 已立「里程碑级改动配 spike 闸门」范式,本轮是第 2 次自觉援用 **且**首次贡献「spike 绿灯≠最简路径,要探 architecture FORK」这一**延伸维度**(S-C 挖出 Arch-2 远简于已证可行的 Arch-1)。若 PW10 R3 及后续里程碑再现「spike 出可行方案后又找到更简分支」,这条连同 PW9 教训 5 可提升为一篇完整的 **spike-gate / architecture-fork 工作流 guide**(可并入 [[perf-optimization-workflow]] 或独立)。**本轮使「spike-gate」从 PW9 的首次记录进到第 2 实例 + FORK 延伸,接近提升阈值。**
- **教训 2**(物理数据结构迁移「收口→只写影子→翻转→退役」+ 隔离单一灾难步)——这是 VS0-a/b/c/d/e 一以贯之的 house style,**已是该 pattern 第 5 次应用**(VS0-e)。其「只写影子先行使翻转成为唯一真实变更」与「灾难步单独隔离一提交」有一般性(任何「Go 堆→arena/池」物理搬迁、任何带单一 UAF/数据损坏失败模式的改动)。**建议提升**为一篇迁移类 guide,或并入既有 P3 迁移记述——VS0 系列已提供 5 个实例的现成剧本。
- **教训 3**(`th.cur` 稳定地址结构性消解重定位雷区)是 [[design-claims-vs-codebase-physics]] §2 的**正面扩展**(从「翻转后回传刷新值」进到「稳定地址 + 值快照使其无从失效」),建议作为 §2 的**升级应对**补进 guide,而非新立。它与 §2 既有的 PW6/PW7「回传刷新值」并列为同一雷区的两档解法(纪律档 vs 结构档)。
- **教训 4**(build-tag 门控往返自检双职 + 须随重构保持接线)——更偏并行镜像迁移的具体工具,但「自检给未来翻转代码正名 + trace-gated 接线会被重构删掉」有复用性。**首次样本,暂留观察**;若后续镜像迁移(其它结构迁 arena)再现,可并入教训 2 的迁移 guide 作「只写影子阶段的自检接线」一节。

## 触发场景

开始任何里程碑规模、核心可行性假设未验证的架构改动时(先 spike 闸门 **且** 探那个更简单的、未审视的 architecture FORK,别只探第一可行路)、做物理数据结构迁移时(套「收口→并行只写影子→翻转→退役」,把唯一 UAF/损坏-风险翻转单独隔离一提交)、引入并行镜像结构时(配 build-tag 往返自检,并确保自检随重构保持接线)、一个重定位/held-pointer 雷区**反复出现**时(找稳定地址 + 值快照的结构性修复,而非再加一轮「记得刷新」纪律)、加一个 tier-vs-tier 或 A-vs-B 基准列时(验**被走到的路径** + **匹配的工作负载形状**,与架构矛盾的数字触发**隔离探针**)、在一个会改变基线行为的 tag 下做基准时(拆开跑,别让基线背它不付的税),看这篇。

## 关联

[[p3-pw9-acceptance-perf-round]](PW9 暴露跨层调用税 call 核 0.14x = 本轮 PW10 要消除的根因 / 教训 5「里程碑级改动配 spike 闸门」本轮第 2 次援用 + FORK 延伸 / 空测家族第 3 实例 vararg 空测,本轮第 4 实例工作负载错配)· [[p3-pw6-crosslayer-call-round]](h_call 双跨层 ~143ns 是本轮要消除的税 / `$base` 回传刷新值是 §2 重定位雷区的纪律档解法,本轮 `th.cur` 是结构档 / 慢路径复用解释器 = 共享表直调的对偶取舍)· [[p3-pw7-pw4b-closure-tforloop-round]](TFORLOOP base 刷新是 §2 第 4 实例的纪律档,本轮 `th.cur` 升级为结构档)· [[design-claims-vs-codebase-physics]](§2 arena 段重定位 / held-pointer 雷区——本轮教训 3 `th.cur` 稳定地址是其结构性升级应对)· [[perf-optimization-workflow]](§3 benchmark 否决门——本轮教训 1 架构层的「实测挖出更简分支」与教训 5「隔离探针解矛盾数」可并入)· [[test-hardening-round]](绿色≠在测你以为在测的——教训 5 基准公平性家族的奠基)· `must/design-premises`(前提一/前提二边界成本——h_call 双跨层税 / 共享表直调消除它的物理依据)· `docs/design/p3-wasm-tier/04-trampoline.md`(跨层机制)· `02-translation.md`(VS0-e CallInfo 迁移)· `implementation-progress.md` §12 PW10 对账 · `spike/p3indirect/`(S-A/S-B/S-C 三探针)
