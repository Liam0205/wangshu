# issue #21 short proto 守卫:消除 pineapple p3 反噬 + 修正反思教训 2 估算错

- **日期**:2026-06-17
- **任务类型**:profile-driven perf 优化 → wangshu 自家 issue → 修正先轮反思教训
- **branch**:`perf/pineapple-p3-postfix`(本会话切出,3 commits 在 master 上)
- **issue**:#21(本会话开,本轮闭)
- **来源链**:[[2026-06-17-issue18-p3-autolift-fix-round]] 教训 2 → 实测重审 → 衍生 issue #21 → 本反思

## 任务

[[2026-06-17-issue18-p3-autolift-fix-round]] 教训 2 写「pineapple 形态 p3 vs p1 慢 20% 是后续 perf 优化的事,留 follow-up」。用户接着追问「先前的 pineapple 实验是否需要更新?能否继续找 lua 算子优化方法?」三步走:

1. **更新 12 项 2D 矩阵 + p3/p1 CPU profile 实证**:发现 spike 教训 2「采样钩税占 13%、wasm 反噬 6%」**估反了**——profile 实证 p3 build 中 `OnEnter` / `OnBackEdge` / `bridge.*` 不在 top 200,**采样钩税可忽略**;**wasm 路径**(`enterGibbous` + `callWithStack` + `p3Code.Run`)cum ~10% net 才是真主导
2. **立项 + 落地 issue #21**:加 `MinPromotableCodeLen = 10` 阈值,OnEnter/OnBackEdge 在 considerPromotion 调用前 short-proto fast-path return
3. **数字闭环**:p3 _Row 从 660 → 588 µs(-11%),vs 同时段 p1 575 µs 差 +2.3%(噪声内,**完全消除反噬**),issue #18 真升层路径仍工作(long proto white-box PromotionCount > 0)

本轮 3 commits 单分支:
- `bb304d0` doc(pineapple-bench):同步 wangshu_p3 doc.go(issue #18 修复后行为)
- `987876d` perf(bridge):MinPromotableCodeLen 守卫(本反思核心)
- `<reflection commit>`:本反思 + index 同步

## 预期 vs 实际

- **预期**(本轮开始时):重测 + profile 应揭示「钩税 + 升层反噬」双主导项,优化方向走「钩税 opt-out + proto 复杂度阈值」双管齐下,数字回到 ~p1 持平。
- **实际**:profile 实证**钩税单独占比可忽略**——p3 build 升层后 inner f 不跑解释器,钩税仅在 entry 时 sample 一次。**真主导是 wasm 路径**(enterGibbous + callWithStack + p3Code.Run cum ~10% net,vs p1 executeLoop cum ~5%)。**单走 proto 复杂度阈值守卫(方向 H)就解决了 80% 问题**——p3 从 +19.4% 回到 +2.3%。

**反思教训 2 估错的具体源头**(本轮发现):
- spike 期 CommonRow 形态 p3 vs p1 慢 13%,我以为 CommonRow 形态 f 入口只 1 次「不升层」→ 差距全是钩税 → 推算钩税占 13%
- **实际**:CommonRow 形态 f 内有 `for i = 1, n` 用户写的 1000 次回跳,`OnBackEdge` 累计 b.N × 999 ≈ 千万次 ≫ `HotBackEdgeThreshold=1000`,**f 真升层**;CommonRow 13% 慢同样是 wasm 反噬
- 没考虑用户脚本的循环触发 OnBackEdge 升层这条路径

## 教训(每条首句为「下次什么场景会触发」)

### 1. profile 实测 ≫ 机理推算:复合估算分项任何一条偏 50% 整体结论可能反向

**触发场景**:用「机理 + 量纲估算」做 perf 投资决策(立项 / 优化方向 / 工程量评估),特别是估算依赖多条机制叠加分项时。

[[2026-06-17-issue18-p3-autolift-fix-round]] 教训 2 我把 p3 vs p1 慢 20% 分项为「采样钩税 13% + wasm 反噬 6%」,逻辑链是:

- p3 CommonRow 形态慢 13%(我假设 f 不升层 → 全是钩税)
- p3 Row 形态慢 19%(我假设 = CommonRow 13% + 升层 6% wasm 反噬)
- 教训 2 写「采样钩税是主导项,wasm 反噬次要」

**v4 profile 直接 fail 这个分项**:
- p3 build cpu profile 中钩税路径(`OnEnter` / `bridge.*`)**完全不在 top 200**,因为升层后 inner f 不跑解释器,钩税只在 entry sample 一次(~几 µs 量级)
- p3 wasm 路径(`enterGibbous` / `callWithStack` / `p3Code.Run`)cum ~10% net,反映 ~ 80µs/iter,**主导项确实是 wasm**
- **CommonRow 形态 f 也升层**——用户写的 `for i = 1, n` 回跳累计 OnBackEdge 越过 HotBackEdgeThreshold=1000,**两个形态都升层只是触发路径不同**

我估错的具体源头:**漏算了用户脚本里的 `for` 循环回跳触发 `OnBackEdge` 升层这条路径**——只盯着 `OnEnter` 这条,以为「函数入口 1 次 → 不升层」。

**修正纪律**:
- **复合估算的每个分项都要 prove-the-path-under-test**——不只看总数字,还要白盒验「这个机制是不是真在路径上」(本轮:cpu profile 中钩税路径完全不出现 = 钩税不主导;反推先前估算 13% 必错)
- **estimation review 找漏路径**:估算前列出**所有可能的升层触发路径**(OnEnter / OnBackEdge / forceAll),逐个评估「在这个形态下是否触发」。本轮我漏了 OnBackEdge 这条,推理就崩了
- **机理估算只是 spike 前的 sanity check,不是结论**——上一轮 spike 教训 2 + 本轮 issue #18 的"教训 2 估错" + 本轮(分项估算错) = 三个独立样本,**机理估算不可靠这条已经够强**,可以升 [[perf-optimization-workflow]] 第 N 条「复合机理估算必经 profile/spike 验证」

**首次样本 → 三次复发**:[[2026-06-17-pineapple-bench-batch-wrapper-spike]] 教训 2 是第一次估错(机理 -17/-34/-30% → 实测 +22/+27/+30%),[[2026-06-17-issue18-p3-autolift-fix-round]] 教训 2 是第二次估错(钩税/wasm 主导比例),本轮纠偏是第三次复发。**可升 [[perf-optimization-workflow]] 作 first-class 纪律**。

### 2. 升层触发路径的多源性:`OnEnter` ≠ 唯一,`OnBackEdge` 同等重要

**触发场景**:评估「某形态下某 proto 是否升层」时,只考虑 host loop 调用次数(`OnEnter`),不考虑 proto 内部用户脚本写的循环(`OnBackEdge`)。

wangshu p3 升层有**两条独立触发路径**:

- **OnEnter**:每次 `enterLuaFrame` 累计 `pd.EntryCount`,达 `HotEntryThreshold=200` 触发
- **OnBackEdge**:每次 FORLOOP/JMP 回跳累计 `pd.BackEdge[pc]`,达 `HotBackEdgeThreshold=1000` 触发(单回边 pc 计数)

**关键不变式**:**两条独立**——只要任一过阈值就升层。host 端 N 次调函数 ⟹ OnEnter 路径触发;函数内 N 次 for 循环回跳 ⟹ OnBackEdge 路径触发。

**本轮发现的真实情况**:
- pineapple Row 形态(host loop 1000×f()):OnEnter 累计 b.N × 1000 ≫ 200 ⟹ f 升层
- pineapple CommonRow 形态(host 1 次 f,f 内 for 1000 iter):OnBackEdge 累计 b.N × 999 ≫ 1000 ⟹ f 升层
- **两个形态都升层只是触发路径不同**

**修正纪律**:
- **写"某形态升不升层"的论断前列**,列出 `OnEnter` 和 `OnBackEdge` 两条路径分别评估,**两条都不过阈值才能下「不升层」结论**
- **教训 2 估错的根因就是漏算 OnBackEdge 这条路径**:我以为 CommonRow 形态 f 入口 1 次 → 不升层,实际 f 内部 user 写的 `for` 循环触发 OnBackEdge 升层
- **profile.go 守卫位置也受这个影响**:本轮我把 `MinPromotableCodeLen` 守卫放在 OnEnter + OnBackEdge **两处**(不止 OnEnter 一处),避免漏路径

**首次样本暂留观察**——本轮发现「漏 OnBackEdge 路径」错误;下次评估某 proto 升层情况时验证。复发后促成 [[p2-bridge-promotion-paths]] 单独立 reference。

### 3. 守卫位置选择:`counter 累积后` vs `累积前` 是 profile 诊断完整性的边界

**触发场景**:在 OnEnter/OnBackEdge 等采样钩点加 fast-path 守卫,跳过 considerPromotion 调用时——守卫位置选 counter 累积前还是后,影响 profile 诊断完整性。

我第一次实施 issue #21 守卫放在 `pd.EntryCount++` **之前**,直接 return:
```go
if !b.forceAll && len(proto.Code) < MinPromotableCodeLen { return }  // 累积前
pd.EntryCount++
```

跑全套 p3 build 测试:**4 个 short proto 测试失败**——`TestProfile_BackEdgeAccumulates` 期望 MaxBackEdge ≥ 40,got 0;`TestProfile_EntryCountAccumulates` 期望 entryCount = 20,got 0。

**根因**:`profile_test.go` 是测**profile 诊断完整性**,期望 short proto 的 `EntryCount/BackEdge` counter 准确反映调用次数(diagnostic value);我把守卫放在累积前,short proto 的 counter 永远 0,**破坏了 profile 诊断**。

**修正**:守卫挪到 `counter 累积后但 considerPromotion 前`:
```go
pd.EntryCount++  // 仍累积(profile 诊断完整)
if pd.EntryCount >= HotEntryThreshold || b.forceAll {
    if !b.forceAll && len(proto.Code) < MinPromotableCodeLen { return }  // 跳过决策
    b.considerPromotion(proto, pd, onMain)
}
```

这样:
- short proto 的 counter 仍准确(profile_test 期望保持)
- considerPromotion 调用被跳过(perf 优化生效,避免 wasm 反噬)
- sample overhead 微增(counter 累加 ~几 ns),但 short proto 调用次数有限,总开销可忽略

**修正纪律**:
- **fast-path 守卫位置须区分「跳过决策」与「跳过 sampling」两层语义**——前者 perf 优化,后者破坏 profile 诊断
- **加守卫前 grep 所有 profile_test / mock_test 期望**——确认哪些假设依赖 counter 累积
- **本轮的纠偏**:我第一次放守卫前(出于"sample overhead 也省掉"的考虑),被 profile_test 反向纠正——这条 testing 红线**保护了 profile 诊断的完整性**

**首次样本暂留观察**——本轮第一次实施带 fast-path 守卫的 sampling 钩,profile_test 提前发现守卫位置错误。下次类似改动时复用「守卫前后区分」框架。复发后可促成 [[p2-bridge-sampling-discipline]] 单独段。

### 4. testing fixture 的 proto.Code 长度与生产路径守卫的耦合

**触发场景**:在 bridge / profile 等核心采样路径加新守卫(`MinPromotableCodeLen` 类),且守卫依赖 proto 形态某属性(opcode 长度 / kind 等);下游 testing fixture(makeProto / makeProtoWithCode / promoteProto)依赖**短**/特殊 fixture 测特定路径。

本轮 issue #21 加 `MinPromotableCodeLen=10` 守卫后,4 个测试文件总共 ~20+ 个测试失败:

- `bridge/state_machine_test.go`:`makeProtoWithCode(bytecode.ADD)` 创建 1-opcode proto 测 considerPromotion 四路径 → 被守卫拦
- `bridge/mock/mock_test.go`:`makeProto()` 创建 1-opcode proto 测 mock P3 compiler → 被守卫拦
- `crescent/gibbous_e2e_p3_test.go`:`promoteProto()` 手工驱动 short hot proto 升层 → 被守卫拦(15+ caller)

**两条修正路径**(本轮同时用):

1. **fixture padding**:`makeProto/makeProtoWithCode` 自动 padding 到 `MinPromotableCodeLen`(NOP 填充,生产 mock P3 compiler 不解析 proto.Code)
2. **forceAll 显式绕过**:`promoteProto` testing helper 显式调 `SetForceAllPromote(true) + defer false`,符合 forceAll 的"测试入口允许覆盖 perf 优化"语义

**修正纪律**:
- **新 sampling 守卫前先 grep 所有 testing fixture 看依赖**:`grep -rn "func makeProto\|func makeProtoWithCode" --include="*_test.go"` 找所有 fixture 工厂,evaluate 哪些需要 padding / 哪些 forceAll 绕过 / 哪些直接改测试用例
- **不要 retroactively 改测试断言** —— 测试断言原本是对的(测短 proto 路径仍要工作),改的是 fixture 而不是断言
- **forceAll 是合法的 testing escape hatch**:任何 sampling 守卫都该让 forceAll 绕过(它本身就是"覆盖 perf 优化的测试入口"),这是干净的 contract
- **fixture padding 是另一种解法**:对于 mock 测试 P3 compiler 不解析 proto.Code 的场景,padding 比 forceAll 更对偶语义(直接造个长度合规的 fixture,而不是绕守卫)

**首次样本暂留观察**——本轮第一次加 proto 形态依赖的 sampling 守卫,处理了 fixture 耦合。下次类似改动时复用「fixture padding vs forceAll 绕过」框架。
