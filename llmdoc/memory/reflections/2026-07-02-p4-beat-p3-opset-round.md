---
name: p4-beat-p3-opset-round
description: P4 must-beat-P3 op-set 扩面轮过程教训:2026-07-02 单会话 30 commits dc9baf0..79bd6dc(分支 feat/p4-beat-p3)把 P4 amd64 native 相对 P3 wasm 的对比面从少数几核推到全 27 对基准里 25 对 ≥2%(多在 +10%~+85%)、README perf 表更新;头条**「exit-reason 协议替 mmap 内 shim call」**——jitCtx.exitArg0 打包 (helperCode, a, b, c, pc) + RETs 出 ExitInlineHelper + Go 端 dispatcher 循环 + resumeOff 回入,一步解掉 mmap+morestack 物理不兼容,并把 CALL/UNM/GETUPVAL/SETUPVAL/GETGLOBAL/SETGLOBAL/多值 RETURN 全接住(18-bit Bx 拆两 9-bit payload / HelperReturn 终结而非重入);**三条 fuzz 直接抓到的正确性 bug**:(a) x86 SSE NaN 结果 0xFFF8… 与 NaN-box tag 空间别名 → arith inline 需 result guard 路由到 host.Arith canonicalize;(b) **insertNewKey Brent 重定位改 slot 但没 BumpGen**——解释器 icGetTable per-access 复验 NodeKey 掩盖,gen-only 快路径(P4 native GETGLOBAL NodeHit + 潜在 P3 wasm emitGetGlobal 同款 gen-only guard)当场 UAF,**最深教训:invariant consumer 若跳过 per-access 复验,对 producer 纪律的要求严于解释器路径**;(c) LT/LE inline UCOMISD 缺 IsNumber 守卫使 reg operands 静默比较垃圾;**验收门比 emit 质量同样重要**——GETGLOBAL 未 gate 使 Transform CallInto 290us→332us,revert 后再带 NodeHit-IC gate + inline 快路径回来变 +11%;CALL 密度门 totalOps/callCount ≥ 16(fib 类 CALL 密集 proto 单次 exit round trip ~15-25 解释器 op 单纯亏);MinPromotableLener hook 实测 P4 固定 Run 成本 ~111ns > 解释器 78ns 使 tiny proto 下地板停 10;**dataflow-track stdlib alias**(local sqrt = math.sqrt)让 nbody 升层,副作用 P3 wasm 侧同 shape 反而 43.5→89.7ms 慢(issue #39,shared-analyzer 改进可能是 per-backend 回归);**过程教训**:共享 80 用户机器上并发无限制 fuzz + pgrep 轮询壳把 load 打到 11+ 被 kill;bench 方法论踩三雷(P3/P4 并跑互扰须串行 / test-bin -race 二进制 profiling 把 racecall 顶出来须无 race 跑 / 不同 build tag bench 名不同 GibbousJIT* vs Gibbous* 且不匹配 regex 会静默 PASS 空输出);stop-hook 机械复核目标条件,tie 解释直到 README 明确记录才被接受
metadata:
  type: reflection
  date: 2026-07-02
---

# P4 must-beat-P3 op-set 扩面轮反思(2026-07-02,exit-reason 协议 + fuzz-driven 正确性收口 + 验收门收紧)

> 范围:分支 `feat/p4-beat-p3`,30 commits `dc9baf0..79bd6dc`。会话目标「P4 每一条已知 bench 相对 P3 ≥2%」被 stop-hook 机械复核。终态 25/27 对 ≥2%(多在 +10%~+85%),2 对 CallOnly-auto 是解释器-vs-解释器噪声平手,README perf 表整节更新。前序基底:[[p4-pj10-native-round]] 已把 native emit 装到 V15b heavy 三本 2.3x-3.0x,本轮把「op 集扩面 × 与 P3 全表对比」推平。

## 核心教训(按强度排序)

### 1. exit-reason 协议解掉 mmap+morestack 物理不兼容,把「shim call vs inline」的二选一变成三态

[[p4-pj10-native-round]] 教训 1 定的物理约束是:mmap 段内不能调 Go helper,因为 Go stack unwinder 走到未登记 code page 会撞死。当时的应对是「热路径 inline / 冷路径 saveGoG + Go 端 dispatch」二态划分,并因此把 opSupported 收窄到 18 个 mmap-safe op。

本轮 `16bd225` 引入**第三态**:**exit-reason 协议**。mmap 段不 call Go,而是把 `(helperCode, a, b, c, pc)` 打包进 `jitCtx.exitArg0`,状态字置 `ExitInlineHelper` 然后 `RET`;Go 端 `nativeCode.Run` 是 dispatcher 循环,解包 exitArg0 → 分派到 host 方法 → `RefreshJitCtxAddrs`(arena 可能已 grow)→ 从 `codePage + resumeOff` 重入。**这一步把 morestack 物理不兼容彻底绕过**——RET 出 mmap 段时 Go 栈是干净的,dispatcher 在正常 Go 栈上 call host,再重入 mmap 段。

物理效果:CALL(`host.CallBaseline` 同步)/ UNM / GETUPVAL / SETUPVAL / GETGLOBAL / SETGLOBAL(18-bit Bx 拆两 9-bit payload slot)/ 多 RETURN(`HelperReturn` 终结而非重入)全接住,opSupported 从 18 扩到当前面。

**Why**:前一轮的「shim call in mmap」和「exit + Go dispatch」被视作二选一,忽略了「mmap 内不需要 call Go,只需 RET 出去让 Go 端 call 完再重入」这条 U 形路径。二选一是**执行流方向**假设(段内向下嵌套),U 形是**段边界作为原子分派点**(段是「输入→输出」的纯函数,call 由外层做)。

**How to apply**:mmap / unregistered code page / 任何跨受限执行环境边界的 codegen 里想调用外层 runtime helper 时,先问「能不能改成 RET 出边界 + 携带指令包 + 外层 dispatch + 重入」,别默认「段内向下调」和「段外接管」二选一。exit-reason 协议是 P5 trace JIT / OSR exit / 任何 mmap codegen 通用形态,若 P5 再撞可升 guide「受限执行环境的 exit-reason dispatch 协议」。

### 2. Invariant consumer 若跳过 per-access 复验,对 producer 纪律的要求严于解释器路径

`cc6557e` 修的最深 bug:`insertNewKey` 的 Brent 式重定位——新 key 抢占某个 main position 时,把该位置的既有占用者搬到自由槽——**改了 key→slot 映射但没 BumpGen**。

这条在解释器路径**多年没被发现**,因为解释器 `icGetTable` 每次访问都复验 NodeKey(gen 只是快速否决,key 复验兜底)。P4 native GETGLOBAL 的 NodeHit inline 快路径把 node index 烧成编译期立即数,**只留 gen guard**——gen 不 bump ⟹ inline 命中原槽,读到搬进来的新占用者。P3 wasm emitGetGlobal 有同款 gen-only guard,是同一颗定时炸弹。Fuzz seed `4b3d10ff17c418d4` 抓出:`s=0 function add()A0=0>s end add(add())add()` P1 正确,P4 报 `attempt to compare boolean with number`。修法在 producer 侧(insertNewKey relocate 时 BumpGen)不在 consumer 侧,因为 consumer 已经把复验优化掉了、生态又有多个 consumer(P3 + P4 + 未来 P5)。

**Why**:同一条 invariant(「gen 未变 ⟹ key→slot 映射未变」)有两类 consumer:
- **完整复验型**(解释器 icGetTable):gen 只是快速否决,invariant 松了它也能兜底 → producer 的一次遗漏是「良性」;
- **快照型**(gen-only inline 快路径):gen 是唯一凭证 → producer 的一次遗漏是「致命 UAF」。

producer 侧代码历经多次 review 都过,是因为审的人是按松 consumer 的期望审的。**invariant 强度不是由 producer 单方面定义,是由最严格的 consumer 定义**。

**How to apply**:
- 引入 gen-only / 快照型 consumer 前,**逐条列 producer 侧所有能不 bump gen 但改 slot 语义的路径**(不只 insert / delete,还包括 relocate / rehash / resize-hidden-move),补 BumpGen;
- fuzz 是唯一现实的兜底(seed 4b3d10ff 就是 fuzz 直接给的);
- **文档**:invariant 契约必须写清「谁负责验证」——若 producer 承诺「不改语义时不 bump」,consumer 侧必须复验;若 producer 承诺「任何语义变化都 bump」,consumer 才能 gen-only。本 codebase 事实上是第二档,但从来没写清过。
- **多 consumer 尤其危险**:P3 + P4 同款 gen-only guard,一次 producer 修复救两个;若只在 P4 侧改 consumer(加复验),P3 wasm 侧的定时炸弹留着。修 producer 是唯一多消费者友好的落点。

这条**首次样本足够强**,值得推 promotion 候选进 [[design-claims-vs-codebase-physics]] 或独立立项「invariant contract 强度由最严 consumer 定义」,与 [[design-claims-vs-codebase-physics]] §2「跨层固定 token / 视图段重定位」构成对偶——那条管**空间性 held-pointer**,本条管**时序性 gen-guard**,都是「多 consumer 中的松档掩盖 producer 遗漏,严档暴露」。

### 3. 验收门(entry gate)与 emit 质量对性能收益贡献相当,且方向相反容易被误读

本轮三个「门」实证:

- **GETGLOBAL ungated 反回归**:`fe48aa7` 加了 GETGLOBAL exit-reason emit 后无脑接受任何 GETGLOBAL 蛋白,Transform CallInto 从 290us 掉到 332us(每次访问一次 exit round trip,~200ns/次 vs 解释器 IC 命中 ~10-20ns)。`ff6ac62` revert opSupported 接受(保留 emit 能力),`7b17b70` 再带**「NodeHit-IC gate + inline 快路径」**回来:仅当 IC.Kind == NodeHit 且能 inline 时才接受,回归变成 +11%。同一 opcode 同一 emit,**接受策略切换就是 -14% → +11% 的 25 个百分点全景**。
- **CALL 密度门**:`bebbd44` 观察到 fib 类 CALL 密集 proto(body ratio 高)在 native 反而慢——每次 CALL 一次 exit round trip ~15-25 解释器 op 的成本,body 里 CALL 占比高就吃亏。门:`totalOps / callCount >= 16`。CALL 密度 = f(工作负载 shape),不是 opcode 能不能 emit 的问题,是 emit 到 mmap 是否**净胜解释器 dispatch** 的问题。
- **MinPromotableLener hook**(`e2b5eca`):bridge 加 per-backend hook,P4 实测 fixed Run 成本(refcount atomics + RefreshJitCtxAddrs + saveGoG + SetHostRef + Go-side DoReturn ≈ 111ns)高于解释器时间(78ns/tiny proto),tiny proto 升层是净亏,地板停 10 op 以上。

**Why**:一个 op 「能翻译」不等于「翻译净胜」。emit 质量是**执行时每 op 加速**,验收门是**入口过滤只让净胜负载进来**;两者贡献大致对等,方向相反(emit 优化把上限提高、验收门把下限护住)。设计里把「接受面」和「emit 面」按 op 名称合成一张 opSupported 表是最简单的耦合形式,但**它把两个正交轴强绑在一起**——一旦某 op 的净胜性依赖 shape(IC 类别、CALL 密度、proto 长度),表就不够表达了。

**How to apply**:
- 新 op 接入 opSupported 前先自问「接受它是否 shape-independent 净胜」;若否,拆「emit 能力(始终有)」和「接受门(shape-dependent)」两层:emit 无条件写,gate 上带 shape 判据。
- 别把「revert 接受」当「emit 失败」——emit 可以留着供带门的接受;`ff6ac62`(drop 接受)+ `7b17b70`(带门再接)是正确形式,不是「重写」。
- 与 [[p4-pj10-native-round]] 教训 3「prefer-native 拦截过宽」是同类**入口判据窄化**教训的**op 粒度**版:那条管 native 整体入口(哪些 proto 走 native),本轮管每 op 入口(哪些 op 在 native proto 内被接);共同基础「实测再定判据」。

### 4. Fuzz 是**唯一**能兜底 gen-only 快路径 + inline 类快路径的现实防线,三个种子实证

本轮三条正确性 bug 全是 fuzz 抓的:

- `f7f0bb1a`:x86 SSE NaN 结果 `0xFFF8...` 与 NaN-box 的 tag 空间别名,inline arith 直接把 NaN 结果当 tagged 值写回,值语义静默乱码。修法:arith inline 加 **NaN result guard** 路由到 `host.Arith` canonicalize(canonical NaN 与 tag 空间不冲突)。
- `4b3d10ff`:insertNewKey Brent relocate 无 gen bump(见教训 2)。
- `d9bce2e`:LT/LE inline UCOMISD 缺 IsNumber 守卫,reg operands 是 boolean 时 P1 报 `attempt to compare number with boolean`,P4 静默比较位模式。

**Why**:三个共同结构——「inline 快路径为省成本把某条完整校验(canonicalize / re-verify NodeKey / IsNumber type check)省了」→ 「常规 test suite 覆盖的用例恰好不触发被省掉的那步」→ 「fuzz 的 shape 混合把它扫出来」。

**How to apply**:
- 任何 inline 快路径新增时,列「解释器路径做但 inline 省了的显式检查」清单,一条对一条评估「省了会怎样」并给 fuzz seed 主动覆盖;
- **fuzz seed 一旦抓到 bug,永远保留在 corpus**(本轮三个种子全进 `testdata/fuzz/FuzzP4ForceAllPromote/`);
- fuzz 的 shape 混合 > 手写 e2e 组合覆盖率(手写永远想不到 `add(add())add()` 这种 shape),别用「等 issue 报」当兜底。

### 5. 共享分析器改进可能是**per-backend 回归**——nbody 在 P4 加速 20x 但在 P3 反而慢 2x

`45b8b53` 的 dataflow-track stdlib alias(`local sqrt = math.sqrt` 类)是**bridge 层共享分析器**的改进,让原本 `ReasonUnknownCall` 的形态过了 F2-b 白名单,nbody 的 advance / energy / offsetMomentum 三 hot proto 在 P3 和 P4 都能升层。P4 效果:874ms → 43.2ms(**~20x**)。P3 效果:43.5ms → 89.7ms(**≈2x**,issue #39)。

**Why**:一个 proto「能升」不代表「升了就赚」。P4 native 的 per-op 成本足够低使得 43ms 是 20x 收益;P3 wasm 的 per-op 成本(尤其涉及 `h_call` 双跨界的形态)会把 nbody 特定 shape 从 81ms 拖到 89ms。共享分析器的改进只解决「能升」这一端,「升了赚」得靠 per-backend 净胜验证——但 P2 bridge 现在**没有 per-backend 净胜判断**(F2-b 白名单是全局的)。

**How to apply**:
- 共享分析器 / 通用可升层性判据改动后,**每个后端独立跑一次 all-bench 复测**;别只看主线后端(本轮 P4)的收益就 merge。
- 若发现 per-backend 分歧(A 赚 B 亏),分类三种应对:
  1. 收窄共享分析器判据(把亏方 shape 排除掉——影响面广);
  2. 加 per-backend 净胜门(bridge 引入 backend-hook,类似 `MinPromotableLener`——本轮 `e2b5eca` 的 hook 形态可扩);
  3. 记录成 issue 交给后续里程碑(本轮选)。选 3 时**在 memory 里显式记录 per-backend 分歧数字**,别只提「+P4」漏「-P3」。
- 与 [[p4-pj10-native-round]] 教训 3「入口判据窄到独占形式」邻接:那条管新 tier 入口不抢既有形式,本条管共享判据的 per-backend 分歧;都指向「跨 backend 加速面的判据要 per-backend 验证」。

### 6. 共享机器 + 无限制并发 fuzz + pgrep 轮询壳把 load 打爆的过程雷

会话中期同时跑了多个 `go test -fuzz`(每个默认 24 worker)+ 若干 `while pgrep` 轮询壳,load 打到 11+(共享 80 用户机器),被用户 kill 全场。规则已入用户 memory:

- fuzz 用 `-parallel=4`(不用默认全核);
- bench 串行 `-cpu=1`,永远不并发跑对比对象;
- 不写 `while pgrep` / `while sleep` 轮询壳(cwd 反正 reset,轮询没意义,run_in_background + 事件通知即可);
- 一次一个重活。

**Why**:自己机器上跑没事的模式,共享机器上是社会性 DoS。工具默认值(fuzz -parallel = NumCPU)是给独占机器设计的。

**How to apply**:共享/多租 CI runner 或多用户 dev box 上跑任何「默认并发」工具前,先看默认并发度是不是 NumCPU 或类似上限,强制降到个位数。工具输出可能变慢但社会成本变负→正。

### 7. Bench 方法论三雷:并跑互扰 / -race 二进制 profiling / 空 regex 静默 PASS

本轮三次踩到、都靠数字反常识兜住:

- **P3/P4 并跑互扰**:平行跑 `benchP3 &; benchP4` 数字紊乱、P3 快 P4 慢或反之无规律。必须严格串行、同 -cpu=1 参数、同硬件、时间上不重叠。
- **-race 二进制 profiling**:`build-test-bins.sh` 编 test 二进制默认带 `-race`,拿它跑 `pprof` 会看到 `racecall` 顶到 top,把真瓶颈完全盖住。perf 工作必须用 `go test` 无 -race build 的独立二进制或 `go test -bench=... -cpu=1 -count=3` 直接跑。
- **build tag → bench 名分歧**:P4 build tag 下 bench 名叫 `GibbousJIT*`,默认 build 叫 `Gibbous*`;若 -bench regex 不匹配,`go test` **静默 output `PASS`+ 0 iter**——**没有任何 error message,只是 0 行 output**,肉眼看像跑了。

**Why**:三条都是「工具默认行为」+「用户假设一致」的错位。工具没错,是 mental model 没跟上工具 flag 的组合。

**How to apply**:
- perf 工作会话开始时**先跑一次「探针命令」验证数字非零 + 名字匹配**(比如 `go test -bench=. -benchtime=1x -count=1 -run=^$` 快速点火,若 0 iter 立即调 regex);
- **bench-all 类聚合脚本**在 wrapper 层加「若某组 bench 输出行数 < 预期,fail 全 script」的 sanity;
- profiling 前显式检查 build:`go tool nm binary | grep racecall` 有内容就换 build。

## 其它(较小)

- **stop-hook 机械复核 + noise-tie 解释需要文档兜底**:stop-hook 每轮机械跑「目标是否满足」,tie 解释(2 对 CallOnly-auto 是解释器-vs-解释器噪声)不写进 README 之前,stop-hook 每次都判「未完成」再拉一轮迭代;写进 README 后 stop-hook 才 PASS。**规则**:「验收目标含例外或噪声解释时,例外必须写在 stop-hook 能读到的位置(README / 验收表),别只留在 conversation」。
- **多值 RETURN 用 HelperReturn 终结**:与其他 exit-reason 不同,HelperReturn 不 resume 而是**真正终结** proto 执行,让 dispatcher 走 return 路径写回值栈 + 弹帧,是 exit-reason 协议的「终态子集」。这条与教训 1 exit-reason 协议同族,是 dispatcher 侧的分支处理。
- **stdlib alias 追踪按 dataflow 而非按名**:`local sqrt = math.sqrt` vs `local sqrt = ...` 结构上都是 `local <name> = <RHS>`,分析器不能按 name 匹配(会误把 `local sqrt = user_func` 也当 stdlib),必须记 name→RHS AST binding,invalidate on reassignment / shadowing / scope-exit。用户直接问了「你怎么处理 `local x = math.sqrt` 后又 `local y = math.sqrt`」——答案是**追踪 binding 不追踪 name**,funcState.localAliasAsts 存 AST,analyzer 复用现有 `isSafeStdlibCall` 白名单过滤。

## 验证

- P4 amd64 vs P3 wasm:27 对基准里 25 对 ≥2%(多在 +10%~+85%),2 对 CallOnly-auto 平手(解释器-vs-解释器噪声,README 显式记录);
- fuzz 120s / 150s(-parallel=4)全绿,3 新种子入 corpus;
- V14 luajc 档无回归;
- V15b heavy P4>P3 未退;
- README perf 表全表更新(2026-07-02 f736c92,「≥1.5x 加粗」规则一致化 79bd6dc);
- follow-up 已开 / 已更:issue #37 arm64 exit-reason port(arm64 shim-free 设计天然适合)、issue #39 P3 nbody 回归、task #57 待办。

## promotion 候选

- **教训 1「exit-reason 协议」**:首次样本,但 unique perspective(受限执行环境跨边界 dispatch 协议),P5 trace JIT / arm64 port 若再撞可升 guide「受限执行环境的 exit-reason dispatch 协议」;**暂留观察**。
- **教训 2「invariant consumer 强度定义者」**:**首次样本足够强**(涉及 P3 + P4 双 consumer 的多消费者定时炸弹),可作 [[design-claims-vs-codebase-physics]] §2 的**时序对偶**补充(那节管空间 held-pointer,本条管时序 gen-guard);或独立立项 guide「invariant contract 强度 = 最严 consumer 定义」;**建议 P3 wasm 侧确认修复覆盖 + 若再有第 3 consumer(P5)出现即升 guide**。
- **教训 3「验收门与 emit 质量对等」**:与 [[p4-pj10-native-round]] 教训 3 是**同类家族第 2 实例**(那条 proto 粒度,本条 op 粒度),两者构成「入口判据窄化」家族;若 P5 再撞可合并升 guide「加速层入口判据的粒度分层与 shape 判据」;**首次以 op 粒度出现暂留**。
- **教训 4「Fuzz 兜底 inline 快路径」**:与 [[test-hardening-round]]「fuzz 目标空转」/ [[prove-the-path-under-test]]「错误路径 fuzz 覆盖」邻接;本轮三个种子实证「inline 快路径省校验 → fuzz shape 混合扫出」pattern;**首次样本暂留观察**,若 P5 再撞可升 guide「inline 快路径的 fuzz corpus 纪律」。
- **教训 5「共享分析器 per-backend 回归」**:issue #39 已 filed,首次样本;若 P5 再撞可作 [[perf-optimization-workflow]] §5「跨机器基线」的 per-backend 对偶补充「跨 backend 基线」;**暂留观察**。
- **教训 6「共享机器并发纪律」+ 教训 7「bench 方法论三雷」**:process-level,已入用户 memory,**不升 guide**(不属 code / doc / architecture,不合 llmdoc/guides 收录标准)。

## 触发场景

- mmap / 受限执行环境 codegen 想调 runtime helper 时(教训 1:U 形 exit-reason 协议是第三态,别陷入 shim vs saveGoG 二选一);
- 引入 gen-only / snapshot 类 invariant consumer 前(教训 2:producer 侧列全所有能改语义但不 bump gen 的路径 + fuzz corpus 主动覆盖 + 文档写清 invariant 强度契约);
- 新 opcode 接入 opSupported 前(教训 3:emit 能力 vs 接受面拆两层,shape-dependent 净胜的 op 带 shape 判据 gate,别粗表接受);
- 写 inline 快路径省了完整校验时(教训 4:列省了的 check,fuzz corpus 主动覆盖);
- 改共享分析器 / 通用可升层性判据时(教训 5:per-backend 全 bench 复测,per-backend 分歧数字显式记录);
- 共享 / 多租机器上跑并发工具前(教训 6:强制降 -parallel/-cpu 到个位数,不写轮询壳);
- perf 会话开始时(教训 7:探针命令验数字非零 + 名字匹配,profiling 前查 -race,bench-all wrapper 加输出行数 sanity);
- 验收目标含例外时(其它·stop-hook:例外写进 README / 验收表,不留 conversation)。

## 关联

[[p4-pj10-native-round]](**直接前序**:PJ10 native emit 已交付 mmap-safe 18 op inline 子集 + V15b heavy 3 本 P4>P3 2.3x-3.0x;本轮承接把 op 集扩面,教训 1 exit-reason 协议是那轮教训 1「mmap+morestack 物理不兼容」的**真解**——把 opSupported 从 18 op 扩到覆盖 CALL/GETUPVAL/SETUPVAL/GETGLOBAL/SETGLOBAL/UNM/多值 RETURN;本轮教训 3 是那轮教训 3「入口判据窄到独占形式」的 op 粒度版)· [[p4-pj10-perop-translator-round]](更前序:Go 端回放骨架 + 占位 stub 的 floor 交付)· [[design-claims-vs-codebase-physics]](教训 2 建议作 §2 空间 held-pointer 雷区的**时序对偶**补充:invariant gen-guard 也是同类跨快照 physics)· [[perf-optimization-workflow]] §7「profile 才是合同」(教训 5 per-backend 回归是「立项数字目标 vs 实测 per-backend 收益」的 backend 维度)· [[prove-the-path-under-test]](教训 4 fuzz corpus 保留纪律与「错误路径覆盖」邻接)· [[test-hardening-round]](教训 4 fuzz 兜底与那轮「fuzz 目标空转」形态互补)· `internal/gibbous/jit/peroptranslator/*.go`(exit-reason 协议 + 各 op 接入)· `internal/bridge/bridge.go`(MinPromotableLener hook + forceAll retry window)· `internal/crescent/rawtable.go`(insertNewKey BumpGen 修复)· 本会话 commits `dc9baf0..79bd6dc`(30 commits,分支 `feat/p4-beat-p3`,P4 vs P3 全表 25/27 ≥2%,README perf 表更新,follow-up issue #37/#39 + task #57)
