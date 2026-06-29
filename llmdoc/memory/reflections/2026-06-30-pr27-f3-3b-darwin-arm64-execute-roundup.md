# PR #28(承 PR #27 F3-#3b)darwin/arm64 真物理 execute 闭环轮反思:三 commit 解四 bug + bypass trampoline 探针根因 isolate

> 范围:承 PR #27 「P4 method-JIT amd64 端完整可达工程 + darwin/arm64 真实装闭环」。前期在 Linux 服务器开发,arm64 端调试遇 SIGSEGV 失败,留 F3-#3b 调试尾巴——PR #27 留 5 处「darwin/arm64 暂 false」+ `.github/workflows/ci.yml` 范围裁决跑安全子集,17/17 CI 全绿但 P4 真物理路径关闭。本会话在 macOS M1(Apple Silicon)本地完成 F3-#3b 全套闭环:**3 commits 解决 4 个独立 bug**,**PR #28 已开**(base `feat/p4-reworded`,非 master,详 https://github.com/Liam0205/wangshu/pull/28)。
> commit 链:`8639eb5`(trampoline_arm64.s LR slot 覆盖根因)→ `bc43650`(arch_arm64 闸门翻 true + macos-latest CI 跑全套)→ `a485ab8`(arm64 spec emit 三 bug 联合修)。
> 关联:P4 method-JIT 设计文档集 `docs/design/p4-method-jit/05-system-pipeline.md` §6 W^X / icache / trampoline / PAC 四件套 + `06-backends.md` §4 双后端骨架 + per-arch 发射器 + 共享模板族;[[2026-06-15-p3-pw10-r3-call-indirect-round]] 头条「spike 只量延迟未量分配让里程碑追错机制」家族延伸——本轮同款形态在「机制叠加多档」崩点诊断侧出现。

## 任务

PR #27 已在 amd64 端把 P4 method-JIT 全工程组件做齐,但 arm64 端因 Linux 开发机无真物理 BL→mmap 执行能力(QEMU 模拟 + 字节级单测对比固定模板字节,**不真物理 execute**),5 处 `archSupportsSpec / archSupportsFrameInline` 在 `arch_arm64.go` 写成 `return false` 占位、`.github/workflows/ci.yml` 的 macos-latest job 裁成安全子集。本会话目标:在 macOS M1 本地把 F3-#3b 闭环——翻闸门 true、把所有 `false` 占位补真实现、CI 跑全套测试集、复盘 PR #27 注释列的三 hypothesis(H1 trampoline ABI / H2 Apple Silicon PAC / H3 Hardened Runtime entitlement)实际由哪条主导。

## 预期 vs 实际

- **预期**:PR #27 注释把三 hypothesis 并列,默认主导项是 H2(PAC)或 H3(entitlement),因为这两条在 Apple Silicon 生态里是公认坑;预计需要逐 hypothesis 穷举验证、可能需要折腾 `codesign --entitlements`、可能要研究 PAC pacia/autia 指令。预估 1-2 天调试周期。
- **实际**:**bypass trampoline 探针**(20 行代码 + 5 分钟)直接在三 hypothesis 中收敛到 H1(trampoline ABI),H2/H3 一次性排除。**真正主导项是 trampoline_arm64.s 的 `STP (R19,R20), 0(RSP)` 覆盖 Go arm64 auto-prologue 写在 `[SP+0]` 的 LR slot**——一个纯汇编侧 ABI 不匹配 bug,与 PAC/entitlement 完全无关。修完头号 bug 翻闸门 true 后,**又连环爆出 3 个独立 bug**——其中 2 个是 amd64 端 §9.20.9 commit-5l 已修过的同款 bug(arm64 端 PJ8 接入时漏改),1 个是 arch_arm64 stub 注释承诺与闸门状态解耦的潜伏陷阱。**3 commits / 4 bugs / 5 小时**收口,远短于预期。

## 4 个 bug 一览

| # | 表现 | 根因 | 修复 commit |
|---|---|---|---|
| **1** | `TestPJ8_CallJITFull/Spec_RoundTrip` 段 RET 后 SIGSEGV at 栈/低地址(CI 现场 PC=0x2000) | user STP `(R19,R20), 0(RSP)` 覆盖 Go arm64 auto-prologue 写在 `[SP+0]` 的 LR slot,RET 取回 X19 当 LR | `8639eb5` |
| **2** | PJ5 SelfCall SpecTemplate 段内 0x108 处 SIGSEGV at addr=0x3c8 | `*ciSegBaseAddrPtr` 是 `ciBaseW*8`(P3 PW10 wasm 协议 byte offset),`LoadCISlotAddrArm64` 算的 x0 不是绝对地址——需 + arena base(amd64 端 §9.20.9 commit-5l 已修同款) | `a485ab8` |
| **3** | resume entry 跳进段后 SIGILL at PC=0x...01b0,instruction bytes=0x0×16 | `EmitFrameInlinePopVoid0ArgSkeletonArm64` 段尾缺 ret,段后 fall-through 到 mmap 0 字节区,arm64 把 0 当 `udf #0`(amd64 端 §9.20.9 commit-5l 已修同款:xor eax,eax + ret) | `a485ab8` |
| **4** | `TestPJ2_SpecRegK / SpecChain` 全 6 个 0 命中 | `arch_arm64.go::archSseOpForArith` 是 stub `_ = op; return 0, false`,注释「当前 archSupportsSpec 返 false,本函数不会被调用」过期 — 闸门翻 true 后本函数被真调,stub 静默返 (0, false) 让所有 PJ2 spec 测试 0 命中 | `a485ab8` |

## 调试方法学:Phase 0 三步根因 isolate

PR #27 comment 列了三 hypothesis,本轮关键操作是**用三步探针把 N 档崩点收敛到一档**,避免穷举三 hypothesis 各自折腾:

### Phase 0-1:bypass trampoline 探针(20 行代码 + 5 分钟)

**手法**:本机 M1 上写 minimal payload `movz x0, #0x42 ; ret`,直接经 Go funcval 构造 BL 进 mmap 段(**不经 `trampoline_arm64.s`**)。这条路径上仍然完整经过:MAP_JIT mmap + W^X 翻面 + `sys_icache_invalidate` + PAC(指针签名,若 entitlement 缺失会在 BL 入段或 RET 出段触发 SIGSEGV/illegal pac)+ Hardened Runtime entitlement(`com.apple.security.cs.allow-jit`,缺则 mmap MAP_JIT 失败或 mprotect RWX 失败)。

**结果**:X0=0x42 成功返回 ⟹ MAP_JIT mmap + W^X 翻面 + sys_icache_invalidate + PAC + Hardened Runtime entitlement **全部健康**。**H2(PAC)+ H3(entitlement)排除**,根因收敛到 H1(trampoline ABI)。

### Phase 0-2:lldb attach 真物理 PC 现场

**手法**:跑 `TestPJ8_CallJITFull_RoundTrip` 让它崩,lldb attach 看寄存器:

```
pc = lr = x19 = 0x6b4a336cfef8    # 栈地址
```

**三相等**是「STP 把 X19 写进了 LR slot,RET 取回 X19 当 LR」的**完美指纹**——pc=lr 证 RET 刚发生,pc=x19 证取回的 LR 真值就是 X19。CI 现场 PC=0x2000 / 本机 PC=栈地址是同一 bug 的不同表现(X19 入帧前的值不同决定了取回的「LR」是哪个非法地址)。

### Phase 0-3:反汇编实证

`go tool objdump` 看 `callJITFull.abi0` 实际生成的 prologue:

```
STR.W X30, [SP, #-96]!      # Go auto-prologue: LR → [SP+0]
STUR  X29, [SP, #-8]
STP   (R19, R20), (RSP)     # ← 覆盖 [SP+0] LR slot,bug 在这
...
MOVD.P 96(RSP), R30         # ← 取回 X19 当 LR
```

修复:把 user STP 偏移从 `0(RSP)` 改成 `8(RSP)`(避让 LR slot `[SP+0..8)`,只让 8B 给 Go auto-prologue 写的 LR),并把对应的 LDP 偏移同步。framesize=`$80` 严格充满(LR slot 8B + 9 寄存器 × 8B = 80B,无 padding);FP 由 Go auto-prologue 用 `STUR X29, [SP, #-8]` 写 user space **外** 故不占 user 区。详 `8639eb5`。

## 核心教训

### 1. 「机制叠加多档」的崩点诊断,用 minimal payload bypass 跳一档的探针,把 N 档崩点收敛到一档——20 行代码 + 5 分钟 vs 盲改 PR comment 列的三 hypothesis

这是本轮**头条**,与 [[2026-06-15-p3-pw10-r3-call-indirect-round]] 头条「spike 只量延迟未量分配」相邻但维度不同——那条是「spike 量了一个维度漏了主导维度让里程碑追错机制」,本条是**「崩点位于多档叠加机制的最末档,bypass 末档前一档跳一层做 minimal payload 探针,把 N 档崩点收敛到一档」**。

darwin/arm64 真物理 JIT 执行的崩点位于 6 档机制叠加的最末端——MAP_JIT mmap / pthread_jit_write_protect_np W^X 翻面 / sys_icache_invalidate / trampoline_arm64.s ABI / PAC 指针签名 / Hardened Runtime entitlement,**任何一档错都会以同款 SIGSEGV 形态出现在 BL→段→RET 路径上**(可能 PC=mmap 段内某偏移 / PC=低地址 / PC=栈地址 / PC=0x0 / SIGILL),根据症状很难分辨是哪一档。PR #27 comment 把三 hypothesis 并列后默认做法是**穷举验证每条**——折腾 codesign entitlements、研究 PAC pacia/autia、对比 wazero 的 darwin/arm64 实装——预估 1-2 天周期。

**解法**:写 minimal payload `movz x0, #0x42 ; ret` 直接经 Go funcval 构造 BL 进 mmap 段,**bypass trampoline_arm64.s**(末档前一档)。这条路径上仍然完整经过 mmap + W^X + icache + PAC + entitlement 五档——若它崩,锅在前五档之一(H2/H3 阵营);若它过,锅在 trampoline ABI(H1)。20 行汇编 + 5 分钟跑测,**一次性把 6 档收敛到 1 档**。

**Why**:崩点是「叠加机制最末档错位」的形态时,**默认设想最末档错的探针成本极低**——只需要把次末档(本例 trampoline)替换成已知正确的 minimal payload,即可隔离掉最末档的所有 ABI 风险。前提是次末档的**输入界面**足够干净(本例 Go funcval+BL 是 ABI 标准约定)允许 minimal payload 直接接入。`prove-the-path-under-test` 的「真实物理执行第一次发生」侧形态——以往用计数器/哨兵助手证「快路径被走到」,本条用 bypass payload 证「前置机制都健康」。

**How to apply**:遇到「机制叠加 N 档,崩点症状只有一种形态(同款 SIGSEGV/SIGILL/wrong-result)」时,**第一步不是穷举 N 档分别诊断,而是写 minimal payload bypass 末档跳一档**——若 minimal payload 经过相同前 N-1 档但 bypass 末档可过,锅锁定在末档;若 minimal payload 仍崩,锅在前 N-1 档,**继续 bypass 倒数第二档跳一层**,直到 minimal payload 过。每跳一层成本 ~5 分钟、~20 行代码,远快于穷举诊断每档。判据:**「档与档之间是否有干净的输入/输出 ABI 边界」**——若有(本例 trampoline 输入是 Go funcval / 输出是 ABI 标准 BL),探针成本低、收益高;若无(各档 ABI 互相纠缠),退回穷举。

### 2. amd64↔arm64 后端「同源 bug 漏改」纪律——多后端镜像类工程,后端 N 接入前必须做「同源 bug 历史 grep」

本轮 #2(`ciSegBase` byte offset)+ #3(Pop 段尾缺 ret)都是 amd64 端 §9.20.9 commit-5l 已修过的 bug,arm64 端 PJ8 接入时漏改。**根因**:

1. **时间维度**:PJ8 arm64 接入是 amd64 端 commit-5l 数月后,commit-5l 的修复历史不在 PJ8 实施者视野内。
2. **测试维度**:arm64 端测试用 byte-equal 单测对比固定模板字节,**不真物理 execute**,故 bug latent;darwin/arm64 真物理 execute 是 PR #27 才上线,F3-#3b 是这个机制的第一次完整跑。
3. **机制维度**:多后端镜像类工程(amd64/arm64 双 backend 共享骨架 + per-arch 发射器),后端 N 共享前端的协议约定(本例 `ciSegBaseAddrPtr` byte offset 是 P3 PW10 wasm 协议、Pop 段必须 ret 是 ABI 约定),前端协议在 amd64 接入期发现的 bug 在 arm64 接入期会**结构性复发**(两个后端按同款骨架在不同点接入同款协议)。

**Why**:多后端镜像类工程的同源 bug 不是「再犯一次同样的错」,而是**协议约定本身在不同接入点的不同 emit 路径上结构性复发**。前端协议(`ciSegBase` byte offset / 段必须 ret 才能正确 RET 而非 fall-through 到 mmap 0 字节区)是后端无关的,但 emit 路径是后端相关的——amd64 端 emit 在 `arch_amd64.go::EmitFrameInlineLoadCISlotAddrAbsolute` + `add rax, r14`,arm64 端 emit 在 `arch_arm64.go::LoadCISlotAddrArm64`,虽是同款协议但代码路径完全独立,**修了 A 不会自动修 B**。

**How to apply**:多后端镜像类工程的「后端 N 接入前」必须做**同源 bug 历史 grep**——

1. **grep 后端 1(amd64)的所有 fix commit**(本例 §9.20.9 commit-5l 全节),尤其含「真物理 execute」首次跑的轮次留下的 bug 修复;
2. **逐条对位审查**:每个 amd64 端 fix,在 arm64 端的对应 emit 路径(同名函数 / 同协议接入点)是否同步修了;
3. **真物理 execute 测试在所有目标平台上线时一起翻闸门**——不让后端 N 处于「字节级单测过但从未真执行」状态,这种状态会让同源 bug 长期 latent 直到闸门翻 true 时连环爆。
4. **配套 CI runner**:linux/arm64 QEMU 不够,**真物理 arm64**(本例 macos-latest M1 self-hosted runner / 或 Apple Silicon CI 提供商)在该后端首次落地时必须同步上线。

判据:**该后端的「真物理 execute」首次跑是闸门翻 true 的同一个 commit**——若是,同源 bug 漏改会在翻闸门时连环爆;若该后端有更早的「真物理 execute」上线(独立于闸门翻 true),同源 bug 漏改会被早期暴露。

### 3. stub 注释承诺与闸门状态解耦是「时间炸弹」——「sentinel 保底」类注释承诺是设计稿时间维度过期家族的新维度

bug #4(`arch_arm64.go::archSseOpForArith` stub)的注释:

```go
// 当前 archSupportsSpec 返 false,本函数不会被调用——sentinel 返 (0, false) 保底
```

注释当时为真(arm64 闸门翻 false,所以本函数确实不会被调用,sentinel 返 (0, false) 是安全的)。但本轮闸门翻 true 后,本函数被**真调**,stub 静默返 (0, false) 让所有 PJ2 spec 测试 0 命中——**没有 panic、没有 error、没有日志,只是性能数据上「投机路径 0 命中」**——若不是 PJ2 spec 测试断言「命中率 > 0」,这条 bug 会以「投机优化没生效但功能正确」的形态长期 latent。

**Why**:这是 [[design-claims-vs-codebase-physics]] §5「时间维度」家族的**新维度**——以往的时间维度实例([[2026-06-14-p3-pw7-pw4b-closure-tforloop-round]] 教训 1「难点过期」、[[2026-06-16-vs0e-varargs-stack-underflow-round]] 教训 1「调研先于实现」、[[2026-06-24-p4-doc-review-round]] 教训 3「外部依赖现状过期」)都是**前序里程碑改变事实后,后续文档/task 的快照失效**;本条是**「sentinel 保底」类注释承诺,在闸门翻 true 时未同步审计**,与前面三个时间维度实例同源(快照失效)但新形态(stub 静默保底 vs 难点过期 / task 描述过期 / 外部依赖过期)。

**注释承诺的形态特征**:

- 「**当前 X 为 Y,所以本函数 Z**」——X 是一个闸门/配置/上下文状态,Y 是当前值,Z 是基于 Y 的安全行为(stub / sentinel / no-op);
- 注释当时是真;
- X 翻面时若未同步审计本函数,Z 行为变成静默 bug。

**How to apply**:

1. **写 stub / sentinel 时,注释里若含「**当前 X 为 Y,所以本函数 Z**」,在 X 翻面的同一个 commit 必须同步审计本函数**——`grep -rn "当前.*返 false\|当前.*不会被调用\|sentinel.*保底" <pkg>` 是闸门翻 true 同一 commit 的标准动作;
2. **stub 默认应 panic 而非保底**——「保底」是默契承诺(信本函数不会被调用所以静默),panic 是显式契约(若被调用立刻爆);panic 让闸门翻 true 时此类 bug 立刻可见,sentinel 让其潜伏;
3. **「sentinel 静默」+「测试断言命中率」是配对纪律**——若必须用 sentinel(性能敏感不能 panic),配套的测试必须断言「该路径被走到」(本轮 PJ2 spec 测试断言命中率 > 0 抓到了 #4 bug),否则就是结构盲区。

### 4. linux/arm64 QEMU + linux 无 self-hosted arm64 物理机的 CI 形态,让 trampoline LR slot bug 长期 latent——darwin/arm64 macos-latest CI 是真物理 BL 跳段执行的第一次实测,bug 实际 linux+darwin 同款只是 CI 没覆盖到

承 `prove-the-path-under-test` 家族**第 8 个独立实例**(前 7 实例:PW5 inline-proof / PW6 TierStuck no-op / PW9 vararg 空测 / PW10 R3 错误路径盲区 / PW10 R1-R2 工作负载错配 / PW10 ③b 快路径命中盲区 / VS0-e 覆盖度先验证)。**新形态**:**CI 形态本身对「真物理 execute」结构盲**——linux/arm64 QEMU 模拟 + linux 无 self-hosted arm64 self-runner + arm64 字节级单测对比固定模板字节(不真物理 execute),三件叠加让 trampoline LR slot bug 长期 latent。

darwin/arm64 macos-latest CI 是真物理 BL 跳段执行的**第一次实测**——bug 实际 linux+darwin 同款(都是 `STP (R19,R20), 0(RSP)` 覆盖 LR slot),但 linux/arm64 因 QEMU + 无 self-hosted runner 长期未触发。这与「fuzz 目标空转」「全成功语料对错误路径结构性失明」是同款形态——**CI 套在它形态边界外结构性失明,全绿对盲区零信号**。

**How to apply**:

1. **多后端 CI 必须配真物理 runner**——arm64 真物理 runner(本例 macos-latest M1 或 Apple Silicon CI 提供商)在该后端首次落地时同步上线;QEMU + 字节级单测**不能替代真物理 execute**,只能作为补充档;
2. **「真物理 execute 首次跑」是高风险事件**,该 commit 应单独审查、单独 review,因为它是「CI 形态扩张到新形态」的临界点——这次会一次性爆出所有之前 latent 的同源 bug;
3. **本条强化 `prove-the-path-under-test` 第 8 实例**——以往 7 实例都是测试/对拍/性能套形态盲,本条新维度「CI runner 形态盲」是 8 实例,**已稳定跨过 5 实例阈值多次**,可在 guide 里独立成节。

## 其它(较小的过程点)

- **PR #28 base 不是 master 而是 `feat/p4-reworded`**:P4 method-JIT 是 feature branch 上的多 commit 集成开发,本轮 F3-#3b 是 feature branch 内的子 PR,base 接 `feat/p4-reworded` 而非 master——避免 master 上 P4 还未对齐时被一系列 fix commit 污染;承 [[2026-06-24-p4-doc-review-round]] 工作流确认 4「大文档审查任务的分阶段收口」纪律的同款模式落到 feature branch 开发侧。
- **bug #1 → #2/#3/#4 是顺序连环爆**:bug #1 不修则任何 arm64 spec 路径都崩,无法验证下游 bug;bug #1 修完翻闸门 true 后,bug #2/#3/#4 同时浮现——这是「机制级 gate bug」与「应用级 emit bug」的关系,gate bug 是下游 bug 的前置门;修 gate bug 前看不见下游 bug,**这也是「真物理 execute 首次跑」的高风险特征**:不是一次爆一个,而是一次爆一批。
- **trampoline_arm64.s LR slot 偏移 0→8 的具体选择**:[SP+0..8) 是 Go auto-prologue 写 LR(`STR.W X30, [SP, #-96]!` 把 LR 写在新 SP 顶);FP 由 `STUR X29, [SP, #-8]` 写 user space **外** SP 下方故不占 user 区;user 第一个可用 slot 从 [SP+8) 起,framesize `$80` 让 9 个 callee-saved 寄存器 × 8B = 72B + LR slot 8B = 80B 严格充满,无 padding。
- **PJ5 SelfCall 修复同步刷新 wasm 端 P3 PW10 R2 协议文档锚点**:bug #2 根因是 `ciSegBaseAddrPtr` byte offset 协议在 wasm 端(P3 PW10 R2 落地)与 jit 端(P4 PJ5 接入)的接入点不同——前者经 wazero linear memory base 自动加,后者需 emit `add rax, r14` 显式加。这个差异未在 `docs/design/p4-method-jit/06-backends.md` §4 emit 协议注里显式标注,本轮顺手补一行——避免后续 P4 PJ-N 接入时再次踩同款。

## 验证

- **trampoline_arm64.s 修复后**:`TestPJ8_CallJITFull/Spec_RoundTrip` 在本机 M1 物理执行通过 + lldb attach 寄存器无三相等指纹 + objdump prologue 显示 LR slot 完整不被 user STP 覆盖;
- **arch_arm64.go 翻闸门 + 3 bug 修复后**:macos-latest CI 跑全套测试集(`bc43650` ci.yml 把 macos-latest job 从安全子集恢复成全套,17/17 → 全部 arm64 路径绿)+ PJ2 SpecRegK/SpecChain 命中率 > 0 + PJ5 SelfCall SpecTemplate 段内 0x108 处无 SIGSEGV + PJ8 resume entry 跳进段后无 SIGILL;
- **bypass trampoline 探针留作回归探针**:加进 `internal/gibbous/jit/arm64/codepage_darwin_test.go` 作为「前 5 档机制健康」的最小验证(后续若再现 SIGSEGV,先跑这个探针看是否前 5 档其中一档退化)。

## promotion 候选

按优先级排序。

### 候选 1:`prove-the-path-under-test` 家族新实例 + bypass 探针根因 isolate 三档分流手法(强,跨过提升阈值)

**新形态**:**linux/arm64 QEMU + linux 无 self-hosted runner 让 trampoline LR bug 长期 latent**。darwin/arm64 macos-latest CI 是真物理 BL 跳段执行的第一次实测。bug 实际 linux+darwin 同款,只是 CI 没覆盖到。家族第 8 实例(承 PW5 inline-proof / PW6 TierStuck no-op / PW9 vararg 空测 / PW10 R3 错误路径盲区 / PW10 R1-R2 工作负载错配 / PW10 ③b 快路径命中盲区 / VS0-e 覆盖度先验证)。

**新解药**:**bypass trampoline 探针作根因 isolate 三档分流手法**(已落地):在「机制叠加多档」(codepage / W^X / icache / trampoline / PAC / entitlement)的崩溃面前,用 minimal payload 直接调下一层 bypass 跳一档,把 N 档崩点收敛到一档。20 行代码 + 5 分钟 vs 盲改 PR comment 列的三 hypothesis。

**推荐落地**:[[prove-the-path-under-test]] §4「正向侧解药」节加一档「bypass 探针根因 isolate」,与现有「毒化哨兵助手 / 白盒命中计数器 / 非空载体先证路径 / 错误路径用例 / 覆盖度先 grep oracle」并列;或独立成 §5「机制叠加多档崩点的 bypass 探针纪律」。强烈建议提升,recorder 定夺立节位置。

### 候选 2:`design-claims-vs-codebase-physics` 时间维度家族新维度——「sentinel 保底」类注释承诺审计(中,首次样本但模式清晰)

**新形态**:**stub 注释承诺与闸门状态解耦** —— `archSseOpForArith` arm64 端 stub 注释说「当前 archSupportsSpec 返 false,本函数不会被调用——sentinel 返 (0, false) 保底」,但闸门翻 true 时本函数被真调,stub 静默返 (0, false) 让所有 PJ2 spec 测试 0 命中。承 §5「时间维度」家族(PW7 难点过期 / VS0-e 调研先于实现 / P4 doc-review 外部依赖现状过期)。

**新维度**:**「sentinel 保底」类注释承诺是一个时间炸弹** —— 注释当时为真,但闸门翻 true 时未同步审计。配套纪律:闸门翻 true 同一 commit `grep -rn "当前.*返 false\|sentinel.*保底"` 是标准动作;stub 默认应 panic 而非保底,「保底」是默契承诺、panic 是显式契约。

**推荐落地**:[[design-claims-vs-codebase-physics]] §5「时间维度」(P4 doc-review 已留 promotion 钩)正式立项时增「sentinel/stub 注释承诺与闸门状态解耦」小节,与「难点过期 / task 描述失实 / 外部依赖现状过期」并列。首次样本(本轮)暂留观察,若 P4 后续 PJ-N 闸门翻 true 或 P5 类似机制再现可正式入 guide。

### 候选 3:amd64↔arm64 后端「同源 bug 漏改」纪律——多后端镜像类工程的「后端 N 接入前同源 bug 历史 grep」(中,首次系统化样本但模式清晰)

**新形态**:本批 #2/#3 都是 amd64 端 §9.20.9 commit-5l 已修过的 bug(`EmitFrameInlineLoadCISlotAddrAbsolute` + `add rax, r14` / `xor eax,eax + ret`),arm64 端 PJ8 接入时漏改。**根因**:PJ8 arm64 接入是数月后,commit-5l 的修复历史不在视野内;arm64 端测试用 byte-equal 单测对比固定模板字节,**不真物理 execute** 故 latent。

**候选纪律**:多后端镜像类工程的「后端 N 接入前的同源 bug 历史 grep」—— 检查 amd64 端历史 commit 中含 fix/bug 关键字的 commit(尤其同段模板/同协议),逐条对位审查;真物理 execute 测试在所有目标平台上线时一起翻闸门,不让后端 N 处于「字节级单测过但从未真执行」状态。跨过提升阈值(2 个独立实例:#2 ciSegBase 镜像字 byte offset + #3 Pop 缺 ret)。

**推荐落地**:建议在 [[public-api-incremental-delivery]](承「累积偏移审计」纪律家族)或 [[perf-optimization-workflow]](承「快路径家族审计」纪律家族)中加「多后端镜像接入纪律」节;或独立成 first-class guide 「multi-backend-mirror-discipline」。**已跨过阈值**(同轮 2 个独立实例 + amd64 端 §9.20.9 commit-5l 是前序参考实例),recorder 定夺立 guide vs 入既有 guide。

## 触发场景

- **机制叠加多档崩点诊断**(候选 1):遇到「N 档机制叠加,崩点症状只有一种形态」时,**第一步不是穷举 N 档,是写 minimal payload bypass 末档跳一档**——20 行代码 + 5 分钟 vs 盲改 PR comment 列出的 N 个 hypothesis;
- **多后端镜像接入纪律**(候选 3):接 P4 PJ-N(arm64 后端)/ P5 后端 / 任何「amd64 落地后镜像接 arm64」的工程时,**接入前必须 grep amd64 端所有 fix commit + 逐条对位 arm64 端 emit 路径同步审查**;
- **stub 注释承诺审计**(候选 2):闸门翻 true 同一 commit `grep -rn "当前.*返 false\|sentinel.*保底\|本函数不会被调用"` 是标准动作;
- **CI 不真物理 execute 的早期暴露**(候选 1):多后端 CI 必须配真物理 runner(arm64 真物理 = macos-latest M1 / Apple Silicon CI 提供商),QEMU + 字节级单测不能替代;
- **跨平台 trampoline ABI 调试**:linux/darwin/windows 的 ABI 各有 prologue/auto-frame 约定,user asm 直接写 SP-relative 偏移时**必须避让 caller auto-prologue 的 LR slot**——Go arm64 auto-prologue 写 LR @ [SP+0..8)(`STR.W X30, [SP, #-N]!` 把 LR 写在新 SP 顶),FP 由 `STUR X29, [SP, #-8]` 写 user space **外** SP 下方故不占 user 区,user STP 从 [SP+8) 起即可;
- **「机制级 gate bug」+「应用级 emit bug」连环爆**:修 gate bug 前看不见下游 bug,翻闸门一次爆一批是常态;
- **lldb attach 真物理 PC 现场看寄存器三相等指纹**(`pc=lr=x19`):是「STP 写 X19 进 LR slot,RET 取回 X19 当 LR」的完美指纹,看到这个指纹直接跳到 trampoline.s 反汇编对照。

## 关联

- [[2026-06-15-p3-pw10-r3-call-indirect-round]](本轮头条「bypass 探针根因 isolate」与该轮头条「spike 量错维度」相邻——两者都是诊断/spike 阶段的「探针选错形态让里程碑追错机制」家族,但前者是「多档机制叠加崩点的探针形态」/后者是「跨边界成本的探针维度」)
- [[prove-the-path-under-test]](本轮第 8 实例 + 新解药 bypass 探针——guide §4 正向侧解药节直接补充)
- [[design-claims-vs-codebase-physics]] §5「时间维度」(本轮 sentinel 保底注释承诺过期是该家族新维度——P4 doc-review 已留 promotion 钩,本轮强化)
- [[2026-06-24-p4-doc-review-round]] 教训 3「外部依赖现状过期」(同 §5「时间维度」家族,前一轮实例)
- [[2026-06-14-p3-pw7-pw4b-closure-tforloop-round]] 教训 1「难点过期」(§5「时间维度」家族第 1 实例)
- [[2026-06-16-vs0e-varargs-stack-underflow-round]] 教训 1「调研先于实现」(§5「时间维度」家族第 2 实例)
- [[2026-06-13-issue8-boundary-cost-round]](amd64↔arm64「同源 bug 漏改」纪律与该轮「实现浪费 vs 架构成本」是相邻不同维度——前者是「多后端镜像里同一协议 bug 在不同 emit 路径结构性复发」/后者是「同一边界税成本的归类是架构还是实现」)
- `docs/design/p4-method-jit/05-system-pipeline.md` §6(W^X / icache / trampoline / PAC 四件套)+ `06-backends.md` §4(双后端骨架 + per-arch 发射器 + 共享模板族)+ `09-impl-progress §F3-#3b`(本轮闭环点)
- `internal/gibbous/jit/arm64/trampoline_arm64.s`(bug #1 修复主战场)
- `internal/gibbous/jit/arch_arm64.go` + `internal/gibbous/jit/arm64/pj4_template.go`(bug #2/#3/#4 修复主战场)
- `.github/workflows/ci.yml`(macos-latest job 从安全子集恢复成全套)
- PR #28:https://github.com/Liam0205/wangshu/pull/28(base `feat/p4-reworded`,非 master)
- 上游 PR #27(本轮承接,留 F3-#3b 调试尾巴)
- commit 链:`8639eb5`(trampoline LR slot)→ `bc43650`(闸门翻 true + CI 全套)→ `a485ab8`(arm64 spec emit 三 bug 联合)
