---
name: issue50-emit-call-inline-round
description: issue #50 Spike 5(PJ10 native CALL,让 call 密集内核 fib/fannkuch/binary-trees 反超 gopher-lua)amd64 收口教训:分支 feat/issue50-emit-call-inline。核心做四件事——段内 GETUPVAL inline(emitGETUPVALInline,fib 经 OPEN upvalue 自引用,原先每次递归都 exit 段)+ 把双语义 RETURN 扩展到 MULTI-return proto(emitReturnDualSemantics,这是关键 bug:fib/pick 有 >1 个 RETURN 站点 ⟹ MultiReturn ⟹ 每个降级成 HelperReturn exit-reason,seg2seg 调用方误读成 plain return 把结果算错成「对函数值做算术」)+ ProtoSeg2SegEligible 把 seg2seg 被调方接受面从 never-exits 扩到 arith/compare(deopt 守卫)+ GETUPVAL(inline)+ 嵌套 CALL(门控在 dest A >= NumParams 不写参数寄存器,使 deopt 重跑读到完整实参)+ CALL 密度门对 seg2seg 合格 proto 放宽。实测 fib 10.5x / fannkuch 6.9x / binary-trees 1.35x over gopher(同机 -benchtime=2s -count=3 中位数),amd64 全部正确性门全绿(difftest-p4 / crescent / conformance-p4 / peroptranslator / -race / 20k 随机差分脚本)。arm64 因开发机是 amd64(无 qemu-aarch64)且 CI arm64 矩阵仅 master-push/PR 触发、用户说不建 PR,机器码本地无法验证,推迟到 issue #61（走 CI arm64 矩阵）。五条教训：多 RETURN proto 静默破坏 seg2seg（seg2seg 测试必须含 multi-BB/多 RETURN/递归被调方）/ eligibility predicate 调 analyzer 又被 analyzer 回调导致无限递归（拆纯 op-check 与合成 predicate）/ open-upvalue owner 解析不能通用 inline（owner 在 Go 侧 st.uvOwner map，upval 对象内 threadRef 为 0，仅单线程无协程可 inline，其余退 exit-reason/deopt）/ deopt 重跑幂等要求被调方不写参数寄存器 + 无副作用 / arm64 验证是物理/用户决策阻塞而非工作量。
metadata:
  type: reflection
  date: 2026-07-06
---

# issue #50 Spike 5 反思(2026-07-06,PJ10 native CALL,让 call 密集内核反超 gopher-lua）

> 范围：分支 `feat/issue50-emit-call-inline`。目标是让 call 密集内核（fib / fannkuch / binary-trees）反超 gopher-lua——做法是把帧的建立/拆除放进段内、并做段到段（seg2seg）CALL 分派，消掉每次调用一趟 exit-reason 往返。amd64 已交付；arm64 推迟到 issue #61。

## 任务

给 PJ10 native CALL 补上「call 密集内核反超 gopher-lua」这一块。原来每次 gibbous→gibbous 的调用都要退出段、经 exit-reason 往返回 Go 端 dispatcher 建帧再重入，call 密集内核（递归/分支多）被这趟往返的固定成本吃光。本轮做四件事把这趟往返消掉：

1. **段内 GETUPVAL inline**（`emitGETUPVALInline`，`emit_ops_amd64.go`）——fib 经一个 OPEN upvalue 自引用（递归期间该 upvalue 挂在主线程栈槽上），原先每次递归都因这个 GETUPVAL 退出段。新增 jitCtx ABI 字段（`currentClosureRef` / `threadStackBase0` / `inlineUpvalSafe`），由 `RefreshJitCtxAddrs` 填；seg2seg 调用方块在 `call [seg]` 前后把 `currentClosureRef` 设成被调方闭包。
2. **双语义 RETURN 扩展到 MULTI-return proto**（`emitReturnDualSemantics`）——本轮关键 bug（见教训 1）。
3. **`ProtoSeg2SegEligible`**（`call_ic.go`）——把 seg2seg 被调方接受面从 never-exits 扩到 arith/compare（deopt 守卫）+ GETUPVAL（inline）+ 嵌套 CALL，门控在「不写任何参数寄存器（dest A >= NumParams）」使 deopt 重跑读到完整实参。multret（b==0）的 RETURN 拒收。
4. **CALL 密度门对 seg2seg 合格 proto 放宽**。

## 期望与实际

- 期望：call 密集内核反超 gopher-lua，amd64 全套正确性门保持全绿。
- 实际：**达成**。fib 10.5x / fannkuch 6.9x / binary-trees 1.35x over gopher（同机 `-benchtime=2s -count=3` 中位数）；amd64 所有正确性门全绿（difftest-p4 / crescent / conformance-p4 / peroptranslator / `-race` / 20k 随机差分脚本）。arm64 机器码本地无法验证，推迟到 issue #61。

## 核心教训（按强度排序）

### 1. 多 RETURN proto 会静默破坏 seg2seg——症状是错的值不是崩溃

任何 RETURN 站点 >1 的 Proto 都走 `MultiReturn` ⟹ 每个 RETURN 降级成一条 `HelperReturn` exit-reason。旧的 seg2seg 只对单 RETURN（never-exits 叶子）做过段内拆帧，多 RETURN 的被调方退出时打出的这条 exit RET，被 seg2seg 调用方**误读成 plain return**，结果算错。fib / pick 就是典型：它们有 >1 个 RETURN 站点。

修法：`emitReturnDualSemantics` 让 seg2seg 被调方对**单 RETURN 和多 RETURN 都在段内拆帧**，不再让多 RETURN 走 exit-reason 出段被调用方误读。

- **症状是错的值不是崩溃**：bug 表现为 `attempt to perform arithmetic on a function value`——被调方把 seg2seg exit RET 误当成一个函数值参与算术，是一个**语义错误值**，不是段错误。这类 bug 常规 e2e 抓不到，只有真跑一个递归/分支的被调方才暴露；之前的 seg2seg 测试用的是单 BB never-exits 叶子，结构上碰不到多 RETURN。
- **纪律**：seg2seg 测试语料必须包含 **multi-BB、多 RETURN、递归**的被调方；单 BB never-exits 叶子对多 RETURN 拆帧路径是结构盲区。

**Why**：seg2seg 调用方与被调方之间有一个隐含约定——「被调方退出时段栈上留的是返回值，不是 exit RET」。这个约定只在段内拆帧（单/多 RETURN 都拆）时成立；一旦某条 RETURN 走 exit-reason 出段，约定在这条路径上被破坏，而破坏的表现是把 exit RET 当返回值继续算，产出一个「看起来对」的错值。是 [[prove-the-path-under-test]] 家族「绿色 ≠ 在测你以为在测的」的又一情形：用错的载体（单 RETURN 叶子）证明的正确性，对真载体（多 RETURN 递归体）不成立。

### 2. eligibility predicate 调 analyzer、analyzer 又回调 predicate = 无限递归

`ProtoSeg2SegEligible` 调了 `AnalyzeNative`，而 `AnalyzeNative` 的密度门放宽逻辑又调回 `ProtoSeg2SegEligible` ⟹ 栈溢出。

修法：把纯 op 检查（`seg2segOpsEligible`，不调 `AnalyzeNative`）从合成 predicate 里拆出来。analyzer 内部只调纯 op 检查，不调合成版。

**Why**：合成 predicate（依赖 analyzer 结果的完整判据）和纯谓词（只看 op 构成）是两层。让 analyzer 依赖合成 predicate，就在「analyzer → predicate → analyzer」上引入了循环依赖。

**How to apply**：任何「eligibility predicate 里调 analyzer」的写法，都要检查 analyzer 内部是否会反过来回调这个 predicate；若会，把 predicate 里不依赖 analyzer 的那部分（纯 op/形状检查）拆成独立函数，让 analyzer 只调纯的那份。

### 3. open-upvalue 的 owner 解析不能通用 inline——owner 在 Go 侧 map，不在 upval 对象里

open upvalue 的 owner 线程记在 Go 侧的 `st.uvOwner` map 里，**不在 upval 对象内**（对象里存的 `threadRef` 是 0）。因此段内 GETUPVAL 的 open 路径只在单线程（无协程）时可证正确——用 `inlineUpvalSafe = (len(st.cos.cos)==0 && len(st.threadChain)==0)` 门控；owner 是外部线程时退回 exit-reason / deopt。

**Why**：inline 快路径要在段内直接算出 open upvalue 指向的栈槽地址，这要求 owner 线程的栈基地址在段内可拿到（`threadStackBase0`）。单线程时 owner 必是主线程，地址确定；一旦有协程，owner 可能是任意线程，而这个映射只在 Go 侧 map 里，段内拿不到，只能退出段让 Go 端解析。

**How to apply**：给某个「值的归属信息只存在 Go 侧数据结构、不存在被 inline 对象内」的操作做 inline 快路径时，先确认在哪些运行时情形下归属可以在段内确定性推导（本例：单线程 ⟹ owner 必是主线程）；只对那些情形开 inline，其余退慢路径，用一个运行时安全位（`inlineUpvalSafe`）门控。

### 4. deopt 重跑的幂等性——被调方不能改参数寄存器、不能有副作用

deopt 会在 baseline 上把整个顶层调用重跑一遍。所以 seg2seg 被调方必须：

- **不覆写自己的参数寄存器**——由 `dest A >= NumParams` 门控，保证 deopt 重跑时读到的实参完好；
- **无副作用**（不写 table / global），使重算安全。

这就是 `ProtoSeg2SegEligible` 把接受面限定在 arith/compare（deopt 守卫）+ GETUPVAL（inline）+ 嵌套 CALL、且 multret RETURN 拒收的物理原因。

**Why**：deopt 语义是「回到解释器从头重跑这次调用」。若被调方在 deopt 之前已经改了输入寄存器或写了外部状态，重跑读到的输入就不是原始输入、或副作用被执行两次，结果和不 deopt 时不一致。幂等性是 deopt-redo 正确的前提，接受面必须收窄到能保证幂等的形状。

**How to apply**：任何「投机执行 + 失败 deopt-redo 整段」的机制，被调方/被投机段的接受面判据要显式包含两条——(a) 不破坏 redo 要读的输入（不写参数寄存器）、(b) 无外部可见副作用（table/global/IO 写）。与 [[design-claims-vs-codebase-physics]] §2 是不同轴：那条管空间性 held-pointer，本条管「重放的幂等前提」。

### 5. arm64 验证是物理 / 用户决策阻塞，不是工作量

arm64 本轮未交付，原因是**物理 + 用户决策**而非工作量：

- 开发机是 amd64，无 `qemu-aarch64`，本地跑不了 arm64 机器码；
- CI 的 arm64 矩阵只在 master-push / PR 触发，用户明确说本轮不建 PR；
- 所以 arm64 的 seg2seg 机器码本地无从验证，推迟到 issue #61（届时走 CI arm64 矩阵）。

结构上 arm64 是镜像移植：arm64 的 native CALL/RETURN 也走 exit-reason 协议（不是 shim-call 特例），seg2seg 拆帧 + GETUPVAL inline + 双语义 RETURN 的结构在 arm64 上一一对应。

这符合用户 memory 里「真『不可达』只两类——物理依赖 + 用户决策」的判据：本轮 arm64 卡在「物理依赖（无 arm64 执行环境）+ 用户决策（不建 PR ⟹ 不触发 CI arm64 矩阵）」，是合法的推迟，不是拿工作量当退缩理由。

## 其它（较小）

- **jitCtx ABI 加字段的填充点单一化**：`currentClosureRef` / `threadStackBase0` / `inlineUpvalSafe` 三个新字段统一在 `RefreshJitCtxAddrs` 填，seg2seg 调用方块另在 `call [seg]` 前后设 `currentClosureRef`（被调闭包）——ABI 字段有单一写入点，段内只读，避免多处写造成状态不一致。
- **CALL 密度门对 seg2seg 合格 proto 放宽**是接受面收益门的对偶——[[backend-capability-vs-profitability]] 的密度门是「太密集不该编」，本轮是「seg2seg 合格时段内拆帧把密集调用的固定成本消掉了，可以放宽密度门」；同一个门，物理前提变了，阈值随之变。

## 验证

- fib 10.5x / fannkuch 6.9x / binary-trees 1.35x over gopher-lua（同机 `-benchtime=2s -count=3` 中位数）；
- amd64 全部正确性门全绿：difftest-p4 / crescent / conformance-p4 / peroptranslator / `-race` / 20k 随机差分脚本；
- arm64 本地无法验证（无 qemu-aarch64 + CI arm64 矩阵未触发），推迟 issue #61。

## promotion 候选

- **教训 1（多 RETURN proto 静默破坏 seg2seg，症状是错值不是崩溃，seg2seg 测试须含 multi-BB/多 RETURN/递归被调方）**：是 [[prove-the-path-under-test]] 家族「绿色 ≠ 在测你以为在测的」的又一情形（用错的载体证正确性），但具体维度（seg2seg 约定被多 RETURN exit-reason 破坏）首次出现，**暂留观察**；若 arm64 移植（issue #61）或未来 seg2seg 扩接受面时再遇「用简单载体证不了复杂载体的约定」，可与本条一并考虑并入 [[prove-the-path-under-test]] 作「载体形状必须覆盖被测约定的所有分支」补充。
- **教训 2（eligibility predicate ↔ analyzer 互调无限递归）**：首次样本，规模较小，暂留观察，后续遇同族可直接引本条。
- **教训 3（归属信息只在 Go 侧 map 时 inline 快路径须按运行时情形门控）**：首次样本，暂留观察。与 [[design-claims-vs-codebase-physics]] §2「跨层固定 token」邻接（都是「段内拿不到的信息」），但本条是「归属映射在 Go 侧」而非「视图被 grow 搬走」，结构不同，不合并。
- **教训 4（deopt-redo 幂等前提：不写参数寄存器 + 无副作用）**：首次样本，暂留观察。是「投机执行接受面判据」的一条通用约束，若 P4 OSR exit 接上 / P5 trace JIT 再遇同族，可考虑升 guide。
- **教训 5（arm64 验证是物理 + 用户决策阻塞）**：已入用户 memory「真『不可达』只两类」，process-level 不升 guide，本轮作实证记录。

## 触发场景

- 给 seg2seg / 跨段直调机制写测试语料时（教训 1：必须含 multi-BB、多 RETURN、递归被调方；单 BB never-exits 叶子对多 RETURN 拆帧路径是结构盲区；症状是错值不是崩溃，常规 e2e 抓不到）；
- 写「eligibility predicate 调 analyzer」类判据时（教训 2：检查 analyzer 内部是否回调该 predicate，若会则拆出纯 op 检查给 analyzer 调）；
- 给某操作做 inline 快路径、而该操作依赖的归属/元信息只存在 Go 侧数据结构里时（教训 3：先确认哪些运行时情形下归属可段内确定性推导，只对那些情形开 inline，用运行时安全位门控）；
- 给「投机执行 + 失败 deopt-redo 整段」的机制定接受面时（教训 4：判据显式含「不写重放要读的输入」+「无外部副作用」两条幂等前提）；
- 某平台/架构本地无执行环境、且触发其 CI 验证的前置动作（建 PR 等）被用户否决时（教训 5：这是物理 + 用户决策的合法推迟，记 followup issue 走 CI 矩阵，不是拿工作量当退缩理由）。

## 关联

[[2026-07-01-p4-pj10-native-round]]（**直接前序**：PJ10 native emit 骨架 + inline 18 op mmap-safe 子集 + PreferNative 收窄门，本轮在其上加 seg2seg CALL）· [[2026-07-02-p4-beat-p3-opset-round]] 教训 1「exit-reason 协议解 mmap+morestack 物理不兼容的第三态」（本轮 seg2seg 是消掉这趟 exit-reason 往返的正交优化——不是不用 exit-reason，是让 gibbous→gibbous 的常见形状不必走它）· [[2026-07-03-issue45-issue39-round.md]] 教训 3「推迟执行模型审计执行顺序 hazard」（本轮教训 4 deopt-redo 幂等是「重放正确性」同族的另一维度：那条管回放阶段读写乱序，本条管整段重放的输入/副作用幂等）· [[prove-the-path-under-test]]（教训 1 候选落点：载体形状须覆盖被测约定的所有分支）· [[design-claims-vs-codebase-physics]] §2（教训 3/4 邻接但轴不同）· [[backend-capability-vs-profitability]]（CALL 密度门放宽是收益门的对偶）· issue #50 · issue #61（arm64 移植 followup）· 分支 `feat/issue50-emit-call-inline` · `internal/gibbous/jit/peroptranslator/emit_ops_amd64.go`（emitGETUPVALInline + emitReturnDualSemantics + seg2seg 调用方块）· `internal/gibbous/jit/peroptranslator/call_ic.go`（ProtoSeg2SegEligible + seg2segOpsEligible + CALL 密度门放宽）· `internal/gibbous/jit/peroptranslator/translator_native_dispatch.go` · `internal/gibbous/jit/peroptranslator/e2e_test.go`
