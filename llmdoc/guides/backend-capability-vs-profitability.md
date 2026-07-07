# Guide：后端接受面的能力/收益分层

> 适用：给某后端接入新 opcode / 新形式时；某后端 auto 模式性能回归但 forceAll 模式正常时；共享分析器（跨后端可编译性判据）改动后每后端复测出现分歧时；写「按 shape 拒收」类升层门时。
> 来源：四个独立实例——[[2026-07-02-p4-beat-p3-opset-round]] 教训 3「验收门与 emit 质量对性能贡献相当」（P4 amd64 CALL 密度门 + `MinPromotableLener` 尺寸地板）+ [[2026-07-03-issue40-arm64-stopbleed-round]] 教训 3「stay-and-retry 状态 × 高频触发点」（arm64 forceAll retry window × `recheckCompilabilityRuntime` 22.38% CPU）+ [[2026-07-03-issue45-issue39-round]] 教训 2 与教训 5（P3 wasm `PromotionGater` + 阈值 5→7 迭代 + nbody 43.5→89.7ms 回归驱动，issue #39）+ issue #67（P4 arm64 spectral-norm 热 proto `A(i,j)` 9 op 被 `MinPromotableLener` 地板永久钉死解释器，auto 16.6ms 慢 force 3.7x；地板的固定 `nativeCode.Run` 校准假设「每次跨界 Run」，但 seg2seg-eligible 被调方走段内直调从不进 `Run`，加 `FloorExempter` 反向豁免——豁免裁决只问一次,不豁免裁决在 `OnBackEdge` 预热里程碑重问一次防冷 IC 误判永久钉死）。四个实例共同收敛出的接口族：`SupportsAllOpcodes` / `WorthPromoting` / `MinPromotableLener` / `FloorExempter`。相关 issue：#21（`MinPromotableLener` 缘起）、#39（P3 nbody 回归收益门修复）、#67（`FloorExempter` 缘起,spectral-norm 半;含冷 IC 重问修复）。

**核心断言**：「后端能编」（capability）与「后端编了赚不赚」（profitability）是两个正交问题。前者是硬约束（编错就是不对），后者是软约束（编对了但净亏）。**塞进同一张 `opSupported` 表把 shape-independent 能编和 shape-dependent 净胜强绑，一旦某 op 的净胜性依赖 shape（IC 类别 / CALL 密度 / proto 长度 / op 构成密度），表就不够表达**——升层机制在这类形式下不是「编错」而是「不该编」。

## 1. 分层模型

bridge 侧 `considerPromotion` 依次咨询四类 per-backend 可选接口，每类各管一件事：

| 层 | 接口 | 判据 | 拒绝语义 | forceAll 是否绕过 |
|---|---|---|---|---|
| **能力（capability）** | `SupportsAllOpcodes(proto)` | 「这个 proto 里的 op 集，我这后端能不能编？」——static op set / analyzeShape 拒绝面 | 编错，永远拒 | **不绕**（差分测试对能力层拒收透明） |
| **收益（profitability）** | `WorthPromoting(proto)` | 「编了它，运行期净赚不赚？」——CALL 密度 / helper-bound 密度 / IC shape 等 shape 判据 | 编了净亏，不该编 | **绕过**（差分测试覆盖不因收益判断缩水） |
| **尺寸地板** | `MinPromotableLener` | 「proto 太短，固定 Run 成本摊薄不回来」——proto 长度 | 摊薄不回，不该编 | **绕过**（同收益类） |
| **地板豁免** | `FloorExempter` | 「这个 proto 虽短，但热路径派发通道不付固定 Run 成本」——per-proto 通道判据（P4：seg2seg-eligible） | 反向豁免：豁免则地板不拦；不豁免裁决在 `OnBackEdge` 预热里程碑重问一次（防冷 IC 误判永久钉死） | **不适用**（forceAll 本就绕地板） |

四层职责不重叠，接口签名同结构（都接 proto 或类似元数据、返 bool），可独立实现。收益/尺寸类拒绝 → 进入 `TierStuck` 吸收态（静态判断，重复问答案不会改变，进 Stuck 后不再重问）；地板豁免是尺寸地板的反向门，裁决按 proto 缓存 `ProfileData.floorExempt`（三态：未问/豁免/不豁免）——**豁免（Yes）不重问**（eligibility 只随 IC 预热增长，不会从 Yes 退回 No），但**不豁免（No）会在 `OnBackEdge` 的预热里程碑（回边计数 ==1 / ==HotBackEdgeThreshold，与 issue #40 的 recheck re-arm 同步）重置为未问、重问一次**——防止裁决发生在深 pc 分支 IC 仍冷时把 proto 永久钉死解释器（issue #67 PR #72 review 修复,binary-trees `check` 预热形状）。

## 2. 设计不变式

**forceAll 只绕收益类检查，不绕能力类**——`forceAll` 语义是「所有能编的都真编一次」，差分测试覆盖面由能力层决定，不因收益判断缩水。收益层是 auto 模式的入口过滤，不是能力面的一部分。

**收益门是入口检查层扩展，不引入新状态/反向边**——收益类咨询发生在可编译性检查之后、`try-compile` 之前；拒绝直接进 `TierStuck`（复用现有吸收态），不新增 tier 状态、不新增反向边。参见 `docs/design/p2-bridge/04-try-compile-fallback.md` addendum 第 4 条。

**per-backend 独立**——每个后端有自己的性能物理特性（mmap+morestack 冲突 / 跨界成本 / 固定 Run 开销 / per-op helper 密度），bridge 不该把这些内化到自己的状态机里，应该以 per-backend 可选接口族的形式让后端各自回答「我这类形状要不要接」。

## 3. 四个实例

**（a）P4 amd64 CALL 密度门（[[2026-07-02-p4-beat-p3-opset-round]] 教训 3）**：CALL ungated 加进 `opSupported` 造成 Transform CallInto 290us → 332us 反回归（fib 类 CALL 密集 body 单次 exit round trip 摊 15~25 解释器 op 单纯亏）。修法不是把 CALL 从 `opSupported` 拿掉（能力上完全能编），而是加 shape 判据「`totalOps/callCount >= 16`」——密度不够就不升，密度够就走 NodeHit-IC gated inline + Compile fast path。同 emit 接受策略切换 25 个百分点全景。

**（b）P4 amd64 `MinPromotableLener` 尺寸地板（issue #21，[[2026-07-02-p4-beat-p3-opset-round]] 教训 3）**：P4 native fixed Run 成本 ~111ns > 解释器 78ns，tiny proto 单次运行不摊薄。给 bridge 加 `MinPromotableLener` 可选 hook，`SetP3Compiler` 时快照；P4 覆盖返 10，短于 10 op 的 proto 收益层拒收。

**（c）P3 wasm helper 密度地板（issue #39，[[2026-07-03-issue45-issue39-round]] 教训 2）**：nbody advance 74 op 里 26 个 helper-bound（密度 74/26 ≈ 2.85），P3 wasm 每个 helper-bound op 都要跨 host 边界；单次跨界成本 > 解释器同一 op 的开销，op 数量再多也追不回来。auto 升层后反 43.5 → 89.7ms（2× 慢）。修法：给 bridge 加 `PromotionGater { WorthPromoting(proto) bool }` 可选接口，wasm Compiler 实现「`total/helperBound >= 7`」——nbody 密度 2.85 被拒（不升层，走解释器），零 helper-bound 内核（heavy_arith / heavy_floatloop，密度无穷）不受影响仍升层。

**（d）P4 `FloorExempter` 地板豁免（issue #67，arm64 M5 Pro 上发现，机制两架构共用）**：spectral-norm 热 proto `A(i,j)`（144k 次调用/次运行）是 9 个 opcode——比 `MinPromotableLener` 地板（10）少一个——auto 模式把它永久钉死解释器，而它的已升层调用方 `Av`/`Atv` 每次调用都要付一次 `ExecutePlainCall` exit-reason 往返，auto 比 force 慢 3.7x（16.6ms vs 4.5ms）。地板的固定成本校准（`nativeCode.Run` 每次调用 ~111ns vs 解释器 78ns）量的是 HOST 派发通道；但 seg2seg-eligible 被调方（PR #62 引入）的热通道是「已升层调用方发起的段内直调」，从不进入 `Run`，固定成本假设不成立。修法：给 bridge 加 `FloorExempter { ExemptFromFloor(proto) bool }` 可选接口——仅在 auto 模式、仅对已低于地板的 proto、在热度阈值之后咨询，裁决缓存 `ProfileData.floorExempt`（三态:未问/豁免/不豁免），**豁免（Yes）不再重问，不豁免（No）在后续 `OnBackEdge` 预热里程碑重置重问一次**（PR #72 review 修复:首次裁决时深 pc 分支 IC 可能仍冷导致误判 No 且永久钉死,binary-trees `check` 预热形状触发）；P4 实现 `ExemptFromFloor = ProtoSeg2SegEligible`。结果 spectral-norm P4 auto 16.65ms → 4.2-4.4ms,与 force 打平。

四个实例共同收敛出同结构 hook 家族：**per-backend 可选接口、只在 auto 模式生效、forceAll 绕过（地板豁免除外——它本就只在越过地板判定之后才被咨询，forceAll 场景不经过地板）、拒绝进 TierStuck**（未豁免的短 proto 例外——它留在 TierInterp,地板检查发生在 considerPromotion 之前,裁决已缓存故不重复付扫描成本）。

## 4. 工作流

**新后端 / 新 op 接入时的分类问**：某后端拒收某类 proto，先分清拒收依据——

- 「编错」（正确性问题） → 进 `SupportsAllOpcodes` / `analyzeShape` 拒绝面（能力层，forceAll 也不能绕）；
- 「编了净亏」（性能问题） → 进 `WorthPromoting` hook（收益层，仅 auto 生效）；
- 「proto 太短单次 Run 成本摊薄不回」 → 进 `MinPromotableLener` 覆盖（尺寸地板，收益层的特化）；
- 「proto 虽短但热路径派发不付固定 Run 成本」（如 P4 seg2seg 被调方走段内派发） → 进 `FloorExempter` 豁免（issue #67，地板的 per-proto 反向门；裁决缓存在 `ProfileData.floorExempt`；豁免只问一次不重问，不豁免在 `OnBackEdge` 预热里程碑重问一次，防冷 IC 误判永久钉死）。

**收益阈值校准**：白盒 op 构成统计先行 + 实测迭代边界样本。参见 [[2026-07-03-issue45-issue39-round]] 教训 5：`WorthPromoting` 密度阈值从 5 迭代到 7 的三步——

1. 先写临时 in-package 测试统计五个 benchmark-game 脚本的 per-proto op 构成：nbody advance 74/26 ≈ 2.85、fannkuch 93/30 ≈ 3.10、fib 12/2 = 6.0、spectral-norm 内层 20/3 ≈ 6.67、heavy_arith / heavy_floatloop 内核零 helper-bound（密度无穷）；
2. 阈值 5：fib（密度 6）漏网，升层后输约 2.3 倍；
3. 阈值 7：fib 被拒，全部回归内核（密度均 ≤ 6.7）被拒收，全部 P3 赢面内核（零 helper-bound → 密度无穷，提前返回路径）不受影响。

**关键顺序**：**先统计再选阈值**，不是「凭直觉选个 5 跑一遍看结果」。分布统计给出候选阈值的可选区间，实测确认区间内哪个点符合当前工作负载。任何性能相关数值阈值（密度门、长度地板、时间阈值等）动手前先跑一次白盒统计给出候选工作负载在这个维度上的实际分布，再从分布上选阈值，再实测边界样本。

**共享分析器改动后每后端独立复测**：共享分析器（跨后端可编译性判据）改进可能是 per-backend 分歧。参见 [[2026-07-02-p4-beat-p3-opset-round]] 教训 5：`45b8b53` 的 safe stdlib alias dataflow 追踪（`local sqrt = math.sqrt` 类）让 nbody 通过 F2-b 白名单——P4 amd64 效果 874ms → 43.2ms（≈20× 收益），P3 wasm 侧同 shape 反 43.5 → 89.7ms（≈2× 回归，就是 issue #39）。一个 proto「能升」≠「升了赚」；共享判据改动后每后端跑一次全 bench 复测，若 per-backend 分歧优选：（a）收窄共享判据 / （b）加 per-backend 净胜门（`MinPromotableLener` hook 形式可扩） / （c）记 issue 交后续里程碑并在 memory 显式记录 per-backend 分歧数字。

## 5. 与其他 guide 的关系

- 与 [[perf-optimization-workflow]] §7「profile 才是合同」呼应：那节管「里程碑完成中数字不可达时止损」，本 guide 管「入口就用收益门挡掉不该升的 shape」，两条纪律都是「实测优先于机理估算」。
- 与 [[prove-the-path-under-test]] §5「fuzz 探索空间维度」配：接受面动了立刻跑一轮 fuzz（能力面覆盖）+ 接受面动了立刻跑一轮 bench（收益面复测），两个廉价动作配对；能力层扩一格是 fuzz 探索空间维度动一格，收益层动阈值不是（收益门只影响 auto 决策，不改差分测试覆盖）。
- 与 [[design-claims-vs-codebase-physics]] 同源：设计稿把「某 op 支持」画成一格布尔位，实际是「shape-independent 能编 × shape-dependent 净胜」二维；实现前须把二维拆开。
