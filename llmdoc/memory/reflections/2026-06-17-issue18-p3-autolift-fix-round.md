# issue #18 P3 自然热度升层路径修复轮(运行期 `recheckCompilabilityRuntime` 接通)

- **日期**:2026-06-17
- **任务类型**:wangshu 自家 issue 修复 → 编译期 F7 占位 + 运行期 P3 注入后重判 → 闭环 p3 自然热度升层路径
- **branch**:`fix/issue-18-p3-autolift`(本会话切出,3 commits 在 master 上:bridge 守卫扩展 + analyze_on godoc 同步 + 新增白盒测试)
- **issue**:#18(本会话开,本轮收口)
- **来源链**:[[2026-06-17-pineapple-bench-batch-wrapper-spike]] 教训 1 → issue #18 → 本反思

## 任务

[[2026-06-17-pineapple-bench-batch-wrapper-spike]] 教训 1 衍生发现:wangshu p3 build 自然热度升层路径**根本不工作**——`internal/frontend/compile/analyze_on.go::analyzeCompilability` 用临时 `bridge.NewBridge()`(无 P3)跑 `AnalyzeProto`,F7 永远触发,所有 Proto 在编译期被烧成 `CompNotCompilable + ReasonBackendUnsupp`。运行期 `considerPromotion` 看 `comp != CompCompilable && !forceAll` → 直接 `TierStuck`,**热度过 `HotEntryThreshold=200` 也不升层**。`analyze_on.go:31` 注释自承"留 P3 PR 收口"。

用户 promotion 反思立即 actionable —— 1000 次 inner-function call PromotionCount 实测 0(白盒探针证实),且**影响所有 wangshu p3 build 外部 embedding**(不止 pineapple)。开 issue #18 提了三方案 A/B/C,用户选 B(轻量):**改 `considerPromotion` 守卫,允许"编译期 ReasonBackendUnsupp 占位 + 运行期 P3 已注入"的子集调 `recheckCompilabilityRuntime` 重判**。已有 `recheckCompilabilityForce`(原 forceAll 专用)已实现"清 F7 占位 + 对真实后端重判 + F1-F6 保守保留"逻辑,只需把守卫从 `forceAll only` 扩展到 `forceAll || (有占位 && P3已注入)`,并把函数 rename 反映新通用语义。

本轮交付:
- `internal/bridge/bridge.go::considerPromotion` 守卫扩展两路(forceAll + 自然热度路径)+ `recheckCompilabilityForce` rename `recheckCompilabilityRuntime` + godoc 重写说明两种调用场景
- `internal/frontend/compile/analyze_on.go` godoc F7 行为段同步,移除"留 P3 PR 收口"的遗留承诺
- `internal/crescent/gibbous_e2e_p3_test.go` 4 处 rename 引用同步
- `promotion_count_p3_test.go` 加 `TestPromotionCount_P3_NoForce_HotEntry_Lifts` 白盒断言修复后 PromotionCount > 0
- pineapple bench 重测验证:p3 自然热度路径**真的升层了**(promotion > 0);但 boundary-dominated 形态下 p3 实测**反而比 p1 慢 20%**——升层后 gibbous→wasm 边界成本 > 解释器 dispatch 成本(详见教训 2)
- 全套 p3 build 测试 `go test -tags="wangshu_p3 wangshu_profile" ./...` 全绿、conformance/difftest/luasuite 全过

## 预期 vs 实际

- **预期**:方案 B 改动小、语义清晰,仅放开守卫 + rename。改完后 p3 自然热度路径打通,白盒 `PromotionCount() > 0`。pineapple 形态期望"p3 终于稳定快于 p1"(spike 期间 p3 与 p1 几乎相等,假设升层后会有 5-10% 收益)。
- **实际**:守卫 + rename 改动确实小(bridge.go 9 行 + godoc 重写 + 4 处测试注释 rename)。白盒**修复确认有效**——`TestPromotionCount_P3_NoForce_HotEntry_Lifts` 跑 1000 次 inner-function call 后 PromotionCount > 0,reflection [[2026-06-17-pineapple-bench-batch-wrapper-spike]] 教训 1 的反向断言转正。**但 pineapple 形态 p3 反而慢 20%**(p1 ~563 µs vs p3 ~679 µs)——升层后每次 `eng.CallInto("f")` 路径 host→crescent→**gibbous wasm**,wasm boundary(linear memory 数据拆解 + module call_indirect)成本超过解释器 4-opcode dispatch。这跟 [[2026-06-17-pineapple-bench-batch-wrapper-spike]] 教训 3「短工作量翻 VM 反噬」是同一类问题,但根因不同:那是 wrapper opcode 净增,本轮是 gibbous→wasm 边界 cost。

**关键判断**(用户中途明确):**先把事情做对,再优化**。issue #18 修复**正确性意义重大**(p3 自然热度升层路径接通,影响所有外部 embedding 的 p3 perf benchmark 解读),pineapple 形态下 boundary 反噬是 perf 优化后续——不阻塞本 PR,留 follow-up。本 PR 收口范围:打通正确性 + 白盒证伪原 reflection 教训 1 反向断言。

## 教训(每条首句为「下次什么场景会触发」)

### 1. 编译期 + 运行期共担 Compilability 判定的"占位 → 实判"模式,适用所有需要后端能力动态决定可编译性的形态

**触发场景**:Compile 期不知道运行期会注入哪个后端 / 后端是 plugin 形态 / 同一 Proto 跨多个不同能力的后端实例时,可编译性的"早判"和"晚判"必须有显式接力机制。

本期事实链:

- `analyze_on.go::analyzeCompilability` Compile 期跑 `AnalyzeProto`,得益:AST 用完即弃(03 §2.4 决策方案 ① 简化),Proto 字段一次写入跨 State 共享只读
- 代价:Compile 期临时 `bridge.NewBridge()` 没注入 P3 → F7(`checkF7BackendSupport` 看 `b.p3==nil` 返 true)恒触发 → 所有 Proto 都被烧 `ReasonBackendUnsupp` 占位
- 这不是 bug,是**有意的占位**:`ReasonBackendUnsupp` 字面意思就是"后端不支持",但实际语义是"编译期还不知道运行期注入谁,保守标'不支持',等运行期重判"
- 运行期 `considerPromotion` 必须区分:(a) F1-F6 结构性排除(真不可编译,vararg/coroutine/debug 等)— 永久解释;(b) F7 ReasonBackendUnsupp 占位 + 已注入 P3 — 调真实后端 `SupportsAllOpcodes` 重判
- `recheckCompilabilityRuntime` 实装(b),保守保留 (a):`structural := ReasonsBitmap(proto.CompReasons) &^ ReasonBackendUnsupp; if structural.HasAny() return CompNotCompilable; if b.checkF7BackendSupport(proto) return CompNotCompilable; return CompCompilable`

**抽象成一个 pattern**:**"编译期保守占位 + 运行期实判清洗"**。Compile 期不知道完整信息时,**用一个具命名占位位**(不是"未判定" CompUnknown,因为那语义会被其他路径误读),运行期注入真实信息后**专门清这一位**再补判。守卫条件必须明确两路:`forceAll || (placeholder_bit && actual_backend_available)`。

**修正纪律**:
- **占位位的命名要表达"为什么是这位"**——`ReasonBackendUnsupp` 字面意思是"不支持",但实际是"编译期无法判断"。godoc 必须说清两层语义(本轮在 `recheckCompilabilityRuntime` godoc 第二段实装)
- **运行期重判函数名不能带 "Force"** —— `recheckCompilabilityForce` 原名暗示"强制全升"专用,但实际逻辑是"对真实后端重判",改名 `recheckCompilabilityRuntime` 与编译期 `analyzeCompilability` 对称
- **守卫条件双路要可读**:`b.forceAll || (b.p3 != nil && bitmap & placeholder != 0)`,在 considerPromotion 内 inline 解释两路的语义(本轮在 line 244 段 inline 5 行 godoc)

**首次样本暂留观察**——本轮第一次把"编译期占位 + 运行期实判"模式提到 pattern 级。下次接 P3 多后端 / P4 后端开发期(SupportsAllOpcodes 不断扩面)/ JIT 类后端 lazy compile 类场景时验证。复发后可促成 [[p2-bridge-compilability-protocol]] 单独立 reference 收口此 pattern。

### 2. "白盒接通正确性"≠"宿主形态性能加速"——pineapple boundary-dominated + 短工作量是 gibbous 升层的不利形态

**触发场景**:修了一个底层机制(本期 p3 自然热度升层路径接通)后跑下游 bench 实测,**性能反而变差**,但白盒探针(PromotionCount / 升层次数 / wasm execution count)证实机制本身工作正确时。

实测对比:

| | p1 (interpreter only) | p3 (auto-lift fixed) |
|---|---:|---:|
| pineapple per-item L2 算术,1000 items | **~563 µs** | **~679 µs (+20%)** |

升层**确实发生了**——`TestPromotionCount_P3_NoForce_HotEntry_Lifts` PromotionCount > 0 白盒证实。但 net 性能反而差,因为:

- pineapple per-item 形态:host 1000 次 `eng.CallInto("f")`,每次 wangshu boundary `core.CallOnStack`
- f() 是 4 个 opcode 的纯算术(GETGLOBAL/MUL/ADD/RETURN)
- 升层前:host→crescent→**interpreter dispatch 4 opcodes** → 返回
- 升层后:host→crescent→**gibbous wasm**(linear memory 装/拆数据 + module `call_indirect`)→ 返回
- 短工作量(4 opcodes)下,**wasm boundary cost > interpreter dispatch cost**

这跟 [[2026-06-17-pineapple-bench-batch-wrapper-spike]] 教训 3「短工作量翻 VM 反噬」是同家族问题:**任何把"再多一层运行时机制"的优化对短工作量都可能反噬**——wrapper Lua 翻进 VM 反噬 wrapper opcode 净增,gibbous 升层反噬 wasm boundary cost。

但这**不否定本轮修复价值**——issue #18 是把"事情做对",p3 自然热度升层路径接通是正确性闭环(影响所有外部 embedding 的 p3 perf 解读),pineapple 形态下的 boundary 反噬是后续 perf 优化的事。**纪律:把正确性补丁和性能加速补丁分开评估**,正确性补丁不应该被"宿主形态性能未提升"反向否决,反之亦然。

**修正纪律**:
- **本轮 PR 的"成果"叙述要回归正确性视角**:白盒 PromotionCount > 0、影响所有 embedding 的 p3 perf benchmark 解读、原 reflection 教训 1 反向断言转正——这些是正确性闭环
- **不在本 PR 范围里把 p3 vs p1 数字优化掉**——留 perf follow-up issue(评估 gibbous 升层的最小工作量阈值,可能需要在 `considerPromotion` 加 proto 复杂度过滤:`proto.Code 长度 < N 时即便达 HotEntryThreshold 也不升`)
- **修底层机制后 bench 反差,先看白盒**:有没有走到新路径?走到了 net 反差就是"机制工作但宿主形态不利",没走到才是"机制 silently fail"。两者诊断方向完全不同

**首次样本暂留观察**——本轮第一次有"机制接通但宿主反噬"的实测数据点。Perf follow-up issue 评估"gibbous 升层最小工作量阈值"时可验证。复发后可促成 [[p2-bridge-promotion-thresholds]] 加"工作量阈值"段。

### 3. 把"留 PR 收口"的 godoc 承诺当**正式 issue 信号**对待——这是 codebase 内置的 follow-up debt 追踪机制

**触发场景**:godoc / inline comment 出现"留 XX PR 收口"/ "X 落地后再实装"/ "本期不实装"等措辞时。

`analyze_on.go:31` 的注释「运行期 considerPromotion 在 P3 注入后**重新**调 AnalyzeProto 是 P3 落地后的扩展(当前不实装,留 P3 PR 收口)」**在仓库里躺了 7 天**(2026-06-13 PB7-1 落地至 2026-06-17 spike 发现),没人主动 promote 成 issue,直到 pineapple bench spike 教训 1 把它**当 reflection 第一手发现**入档,才转化为 issue #18 + 本轮修复。

这种 godoc 承诺**比 TODO 更隐蔽**:
- TODO 是项目 grep 常态扫的(`grep -rn 'TODO' --include='*.go'`),review 工作流会逐个清
- "留 X PR 收口"是行文承诺,**没有约定的 grep 关键字**,review 工作流不会主动扫
- 落地的 PR(本期 PB7-1 `analyze_on.go`)已经合,reviewer 当时认为"等 P3 PR 收口"是合理 deferred,但**实际 P3 PR(PW0-PW10)收口时没有人回看哪些 PR 留了承诺**——形成"承诺孤儿"

**修正纪律**:
- **每次落地"留 X 收口"的 godoc 承诺时,**同时**开个 issue 锚定**(本期回顾:PB7-1 落地时应该开 issue 锚定"运行期 reanalyze 待 P3 落地后补",但实际没开,直到 7 天后由 pineapple bench spike 反向触发)
- **grep 关键字纪律**:本仓约定"留 X PR/issue 收口"类承诺统一用 `留 X 收口` 词组(grep 正则 `留 \S+ 收口`),每个 milestone 收口时强制 grep 一遍,确认是否有遗留承诺没转 issue
- **本轮 retroactively 兑现**:本反思 commit 同时清掉 `analyze_on.go:31` 那段"留 P3 PR 收口"的承诺(已实装,改成"已实装,见 bridge.recheckCompilabilityRuntime")

**首次样本暂留观察**——本轮第一次发现"godoc 承诺孤儿"现象。下次 milestone 收口前 grep `留 \S+ 收口` 看是否有遗留。复发后可促成 [[milestone-closeout-checklist]] 加"承诺孤儿扫描"项。

### 4. (与教训 3 同根)reflection 教训直接转 actionable issue 的工作流模式有效

**触发场景**:在 spike / 调研 / 性能审查类任务收尾写 reflection 时发现影响其他模块或后续工作的根因。

[[2026-06-17-pineapple-bench-batch-wrapper-spike]] 教训 1 写完 24h 内即转 issue #18 → 本轮修复 → 本反思闭环。同会话(实际不到 4h)完成:reflection 教训发现 → 提 issue + 三方案 → 用户选方案 → 修复 + 测试 + 重测 + 闭环 reflection。

这套工作流之所以快:
- reflection 教训 1 写的时候**已经把根因诊断到 file:line + 修复方向 + 复现路径**全文档化(白盒 PromotionCount 探针)
- 提 issue 时直接复用 reflection 段落(issue body 大段拷自 reflection)
- 修复时直接对照 issue body 中的"方案 B"段实施

这跟 [[2026-06-13-issue8-boundary-cost-round]] / [[2026-06-16-issue13-parity-friendly-fastpath-round]] 同款工作流:**调研轮的 reflection 直接做 issue 提案稿用**。

**修正纪律**:
- **写 reflection 时,主动标注哪些教训具备"直接转 issue"的成熟度**(file:line 级根因 + 复现路径 + 修复方向)。本反思教训 1 写得就**足够 issue ready**,issue 提交时大段拷过
- **reflection ↔ issue 双向链接**:reflection 教训内嵌 issue URL(reflection 是历史快照,issue URL 不变)、issue body 末尾的"来源"段链接 reflection(reflection 在 master 后,issue 可读取最新)
- 这套模式 [[public-api-incremental-delivery]] 第 N 条 candidate(从 reflection 教训直接 motivate issue 的工作流)— 首次样本来自 issue #13 轮,本轮二次验证,可促成立项

**首次样本暂留观察 → 二次验证**——issue #13 轮首次样本,本轮(issue #18)二次验证有效。第三次再复发可促成 [[public-api-incremental-delivery]] 立项或单独立 [[reflection-driven-issue-workflow]] guide。
