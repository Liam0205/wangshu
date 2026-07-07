# pineapple §6 spike:batch-wrapped item-mode + GetColumn 方案验证(反预期 + 衍生 wangshu 自家根因)

- **日期**:2026-06-17
- **任务类型**:跨 consumer perf 优化提案验证(spike)→ 三 backend × 三模式 × count=10 实测 → 方案否决 + 衍生 wangshu 自身 issue
- **branch**:`perf/pineapple-bench`(`4c905e6 ↔ c2bf705`,7 commits,2D benchmark 矩阵已落、§6 spike 改动 revert / 删,调查报告 `.code-review/pineapple-perf/2026-06-17-profile-investigation.md` 不进 git)

## 任务

用户起手「考虑一下,怎么提升在 pineapple 上的性能」——此时 v0.2.0-rc4 已完成(typed-array + GlobalsSlot,见 [[2026-06-16-issue13-parity-friendly-fastpath-round]]),pineapple #112/115/116 也接入,boundary 已被压到 ~100µs/1000-iter 量级。三路稳定数字:WangshuP1 593µs / WangshuP3Auto 605µs / Gopher 624µs,差距 ~5%,**落在噪声带内**。

§1-§5 调查结论:wangshu 在 pineapple 形式下**没有显著优化空间**——pineapple framework 占 87% allocs / 23% cpu,wangshu 自己只占 10% allocs / 27% cpu,任何 wangshu 内核优化反映到 pineapple ns/op 都淹在 3% 噪声里。

§6 是用户调查后期的二次提议:「如果 pineapple DF 加 GetColumn + LuaOp 加 batch-wrapped item-mode,会怎样?」**user lua_script 不变**(契约保留),adapter 在 SetMetadata 期自动生成 wrapper:

```lua
function __batch_f()
  local n = #__col_item_price
  local out = {}
  for i = 1, n do
    item_price = __col_item_price[i]
    out[i] = f()
  end
  return out
end
```

理论上 boundary 1000→1,且 f() 被同 entry 调 1000 次能让 wangshu p3 在 i=201 触发凸月升层。粗算预期 WangshuP1 -17% / WangshuP3 -34% / Gopher -30%。

本轮 spike 把这套完整实施在 `benchmarks/pineapple/.pineapple/` clone 内(不进 git,fetch 自动 reset):FrameReader 加 GetColumn + Row/Column 两实现 + LuaOp 加 batch_wrapper param + SetMetadata 期 lazy NewPool + buildBatchWrapper + executeForItemBatched + wangshuEngine.PromotionCount 探针;wangshu/benchmarks/pineapple/ 加 BatchWrapper Row/Column 入口 × 3 backend。count=10 benchtime=2s 稳定数字:

| backend | per-item row baseline | BatchWrapper Row | BatchWrapper Column |
|---|---:|---:|---:|
| WangshuP1 | 593 µs | 768 µs (**+30%**) | 918 µs (**+55%**) |
| WangshuP3Auto | 605 µs | 770 µs (**+27%**) | 923 µs (**+53%**) |
| Gopher | 624 µs | 761 µs (**+22%**) | 898 µs (**+44%**) |

**三路全部反预期,方案否决**。spike 代码已 revert 干净,调查报告 §6.5/§6.6/§6.8 留全过程。

## 预期 vs 实际

- 预期:方案估算来自一手机理模型(boundary 跨界 N→1 + 凸月升层 + zero-copy 列),三条加起来给到 -17%/-34%/-30% 量级,与三路实际 boundary cost 估算吻合。spike 应能验证 80% 上下,即便有 10-20% 偏差也在合理范围。
- 实际:**反向 46-89pp**。两条**独立且都致命**的根因——任何一条都足以让方案变反向优化:
  - **根因 1(wangshu 自家 issue)**:wangshu p3 当前 build 下 auto-lift 路径**根本不工作**。`analyze_on.go::analyzeCompilability` 用临时 Bridge(`b.p3 == nil`)→ F7 永远触发 → 所有 Proto 编译期被打 `CompNotCompilable + ReasonBackendUnsupp`。运行期 P3 注入后没有重跑 AnalyzeProto 的实现(注释自承"留 P3 PR 收口")。白盒 `PromotionCount() == 0` 证实:1000 次 inner-function call 不触发升层,即使热度远超 `HotEntryThreshold=200`。**预估的 -34% 凸月收益 = 0**。
  - **根因 2(perf 物理事实)**:boundary 已被 #112/115/116 压到 ~100µs 的形式下,把 host loop 翻进 VM 让 wrapper 内层每 iter 增加 ~7 opcodes(GETTABLE + SETGLOBAL + CALL + SETTABLE + FORLOOP),× 1000 = 净增 ~7000 opcodes,wangshu opcode dispatch ~80ns 算下来 **~560µs 反噬**——远超 boundary 节省的 ~100µs,净 +200µs。三 backend 实测都吻合此估算。

两根因合并后,**§6 三方案(列存储 / common mode / batch wrapper)全部失败**:列存储与 per-item 架构 mismatch;common mode 输出契约不能写 item field;batch wrapper 反向。**§4 客观判断(路 #1 写 reflection 不写代码)是正确的**。

## 教训(每条首句为「下次什么场景会触发」)

### 1. wangshu p3 auto-lift 路径当前 build 不工作——所有"自然热度升层"假设失效,这是 wangshu 自身待解 issue

**触发场景**:任何依赖 wangshu p3 build 自然热度升层(`SetForceAllPromote(false)` 形式)产出 perf 数字的任务——pineapple bench、外部 embedding 性能测试、boundary-dominated 场景下"凸月升层应抵消采样钩税"类假设。

**事实链**(由 spike 白盒 `PromotionCount()` 探针证实):

- `internal/frontend/compile/analyze_on.go:37-40` 在 Compile 期对每个 Proto 跑 `analyzeCompilability`,**用一个临时 Bridge**(`tmp := bridge.NewBridge()`)调 `AnalyzeProto`。这个临时 Bridge **没注入 P3 Compiler**(`b.p3 == nil`)。
- `internal/bridge/analyzer.go::checkF7BackendSupport:104-109`:`b.p3 == nil` 时返回 `true`(F7 触发)。
- 结果:**所有 Proto 在 Compile 期被标 `CompNotCompilable + ReasonBackendUnsupp`**。
- 运行期 `bridge.go::considerPromotion:243-257`:`pd.Compilable != CompCompilable` 且 `!b.forceAll` → 直接 `TierStuck`,**永久解释**。
- `analyze_on.go:31` 注释自承:「运行期 considerPromotion 在 P3 注入后**重新**调 AnalyzeProto 是 P3 完成后的扩展(**当前不实现**,留 P3 PR 收口)」。
- 白盒验证:wangshu p3 build 干净 State 跑 `local function f(x) return x*2 end; for i=1,1000 do x = i; out[i] = f() end` 后 `st.PromotionCount() == 0`——1000 次 inner-function call **不触发升层**。

**影响范围**:
- **pineapple 本调研报告 v2 "p3 慢 p1 4.5%"的根因部分需要重审**——之前归因为「OnEnter map lookup 钩税」,实际更可能就是采样钩税本身、且**完全没有升层收益补救**,等同纯 p1 + profile 钩税。
- **任何外部 embedding 在 wangshu p3 build 下做 perf benchmark 都将测到"采样钩税"而**非"凸月路径"——除非显式调 `SetForceAllPromote(true)`(testing-only,不是生产 API)。
- 这是 [[project_pw10_zerocross_milestone]] 凸月升层故事的隐藏漏洞——VS0-d/PW10 R3 等架构机制已就绪,但**升层触发的判定链**还没接通运行期 P3 注入后 reanalyze。

**修正纪律**:
- **wangshu 这边**:给自己提 issue 收口运行期 AnalyzeProto 重跑(`considerPromotion` 在 P3 注入后看 `pd.Compilable == CompNotCompilable + ReasonBackendUnsupp` 时调 `b.AnalyzeProto(fn, proto)` 重判,或直接调 `recheckCompilabilityForce` 而不需要 forceAll 守卫)。这是 wangshu p3 故事完整闭环的最后一步,**不止影响 pineapple,影响所有 boundary-dominated 外部 embedding**。
- **embedding 端纪律**:任何"wangshu p3 升层会带来 XX% 收益"类假设,在跑 perf 数字前必须用 `PromotionCount()` 白盒验:跑完后 > 0 才是真升层路径,== 0 是纯解释器路径(对 boundary-dominated 形式会偷偷劣化、对 compute-dominated 形式等同 p1)。
- **历史 reflection 回看**:本轮发现否定了 [[2026-06-12-official-suite-perf-round]] 等历史轮次中默认"p3 build = 升层路径"的隐含前提——所有相关数字解读都该带上"是否调过 SetForceAllPromote"的口径标注。

**重要待办**(本反思直接 motivate 的下一步):给 wangshu 自家提 issue「P3 注入后运行期重跑 AnalyzeProto,接通自然热度升层路径」。这是 [[project_pw10_zerocross_milestone]] 故事链上的**关键缺口**,且 spike 的 PromotionCount 白盒探针给到了**完整复现路径**,issue 直接可 actionable。

**首次样本暂留观察**——本轮第一次以白盒探针证实 auto-lift 不工作;修复后跨外部 embedding 重测 perf,看 boundary-dominated 形式下凸月路径**是否**带来预期收益(若**仍**不工作意味着 wrapper opcode 净增吃掉所有收益、不只是升层问题,需 boundary 形式本身重判)。

### 2. perf 优化估算必须 prove-the-path-under-test:粗算只是 spike 前的 sanity check,不能作为方案 motivation

**触发场景**:接到「这个新方案大概能省 XX%」类机理估算驱动的优化提案,**且**估算依赖多条独立机制(boundary / VM dispatch / 升层等)叠加的复合估算时。

§6 估算用了三条机制叠加:**boundary 收益 + 列 zero-copy + 凸月升层**,每条单独都说得通,**叠加成 -17/-34/-30% 量级**。实测三路全部反向(+22% 到 +55%)——根因不是单条机制估算错,是:

- **第 1 条(boundary 收益)估算粗略**:认为 wangshu boundary cost ~100µs 全收回,**忽略了 wrapper Lua 自己执行的 opcode 数也是成本**。机理上一致但**量级判断错**——wrapper 内层 7+ opcodes/iter × 1000 = 7000+ opcodes × 80ns dispatch ≈ 560µs,**反向吃掉 boundary 收益 5+ 倍**。
- **第 2 条(列 zero-copy)收益微小且形式错配**:row mode 下根本 zero-copy 不了(还要 N 次 lookup + alloc),column mode 下 backing array zero-copy 但 wangshu typed-array 路径仍要 alloc `[]float64` 中间 buffer 转 `[]any` → 实际省 < 30µs,**而 column-frame BuildInput 本身比 row 慢 25%(~150µs)**,净负。
- **第 3 条(凸月升层)直接 0 收益**——见教训 1。

**修正纪律**:
- **多机制叠加估算 ≠ 方案 motivation**——它只是"值不值得做 spike"的 sanity check。三机制叠加估到 30% 这种量级,意味着**任一机制估算偏差 30% 整个方案就可能反向**。boundary 单条估算 +/- 50% 是常态(取决于 wrapper 自身 opcode 数估得准不准、dispatch ns/opcode 量级取多少)。
- **prove-the-path-under-test**:即便 spike 工程量大(本轮跨 pineapple + wangshu 两仓 + bench 端总改 ~300 行),做完才能真知道。**估算反向不可耻,可耻的是用估算 motivate cross-repo 工程**(本轮幸而 wangshu 这边只 PR 了 2D bench 矩阵,没把 cross-repo 推进出去,否则一旦真改了 pineapple 四引擎再回滚就尴尬了)。
- **"spike 工程量大但收益数字 30% 量级足以 motivate"是错的论断**——§6.6 当时写的这句话回头看就是没 spike 前的 motivated reasoning。**任何 cross-repo perf 提案在 spike 验证前都不应说"足以 motivate"**,只能说"值得 spike 验证"。

**首次样本暂留观察**——本轮第一次用完整 spike 否决一个看似自洽的复合估算;下次再遇到"三机制叠加估到 30%"类提案时直接套这条复盘。复发后可促成 [[perf-investigation-playbook]] 单独立 guide 收口"估算 → spike → 实测"的工作流检查。

### 3. boundary 已压死的形式下,把 host loop 翻进 VM 是反向优化的物理事实

**触发场景**:接到"把 host 端 N 次跨界压成 1 次"类优化提案,**且**已知 boundary 跨界单次 cost 接近底(host alloc + intern 都已优化)、wrapper 内层逻辑需要 VM 自己跑 opcodes 模拟原 host 控制流时。

**机理事实**:

| 形式 | host cost | VM 内 cost |
|---|---|---|
| per-item baseline | N×SetGlobal+CallInto + boundary,N=1000 × ~100ns = ~100µs | N×(GETGLOBAL+MUL+ADD+RETURN) = N×4 opcodes = ~320µs |
| batch wrapper | 1×SetGlobal(列)+1×Call + ~10µs | wrapper 自己 1000×(GETTABLE+SETGLOBAL+CALL+SETTABLE+FORLOOP) + 1000×(GETGLOBAL+MUL+ADD+RETURN) = ~11000 opcodes = ~880µs |

**净增 ~470µs(VM 内 opcode)对 ~90µs(boundary 省)**——5 倍反噬。这是 wangshu 解释器的**物理事实**,与方案聪明程度无关。

**修正纪律**:
- **boundary-dominated 形式下,boundary 优化的天花板由 host 侧 cost 上限定**——pineapple 形式 host 侧 #112/115/116 已经把 boundary 单次 cost 压到 ~100ns 量级,**整体 boundary 上限 100µs**,任何方案预期收益超过 100µs 都该警惕。
- **VM dispatch cost 不是免费的**——wangshu 解释器 opcode dispatch ~50-100ns 量级,wrapper 自己跑 1000 次内层意味着 ~5000-10000 opcodes,**至少 250-1000µs**。boundary 收益要打过这个数才赚。
- **唯一可能赚的形式**:boundary 单次 cost 极高(cross-process / RPC / JNI 形式,boundary 1ms+ 量级),那时 boundary 跨界 N→1 省 1000×1ms = 1s,VM 内 wrapper 多 7000 opcodes 才 0.5ms,赚翻;但 in-process Go embedding 形式(wangshu / gopher 都是)根本不在这区间。

**首次样本暂留观察**——本轮第一次有 boundary 压死 + spike 实测的"反向优化"数据点。下次接到类似提案时直接套这表估算可行域。复发后可促成 [[perf-investigation-playbook]] 加显式"VM dispatch cost lower bound"段。
