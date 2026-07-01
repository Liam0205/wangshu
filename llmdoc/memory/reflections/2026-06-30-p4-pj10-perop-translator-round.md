---
name: p4-pj10-perop-translator-round
description: P4 PJ10 per-op 翻译器轮过程教训:正确性 floor(语义覆盖)与性能 ceiling(native CFG emit)拆分——设计稿 §3 画的 native amd64 multi-BB CFG + label resolver 是性能前提非语义前提,实际落地走 Go 端回放 + 占位 mmap stub 即可把 35/38 opcode 接住、语义与 P1 解释器逐字节一致;stop hook 升压两次方向相反(PW10 是「数字不可达止损」、PJ10 是「范围可达就继续扩」),区分判据是 profile / scope-check「可达只是还没做」vs「真的不可达需止损」;CLOSURE SubNUps 跳 pseudo idiom 第 2 实例复发;P2 bridge F2-b 白名单影响 P4 测试可写形态(pairs/ipairs 不在 → TFORLOOP 测试必须 hand-written iterator)。本会话 21 commits ~3420 行,a94bcec..HEAD,80 e2e subtests 全过
metadata:
  type: reflection
  date: 2026-06-30
---

# P4 PJ10 per-op 翻译器轮反思(2026-06-30,正确性 floor vs 性能 ceiling 拆分)

> 范围:P4 PJ10 通用 per-opcode 翻译器初版交付。设计稿 §3 原画 native amd64 multi-BB CFG emit + label resolver(P3 wasm relooper 在 amd64 端的镜像),实际落地选择「Go 端回放 + 占位 mmap stub(`xor eax,eax; ret` 3 字节)」物理路径,把单 BB Proto 的 head op + side effect 拆成 Go 端清单,Run 调 stub 后 Go 代码按清单调 host helper / 写寄存器。35/38 opcode 接住(VARARG 设计永不接,JMP / TEST / LOADBOOL C!=0 需真 CFG)。
>
> 本会话 21 commits(`a94bcec..HEAD`,2026-06-30 单会话),净增加约 3420 行,80 个 PJ10 e2e 子测试全过,`make test-p4` 全绿(含官方 Lua 测试套)。stop-hook 两次升压都在「未实装」边界拒绝结束,最后两个 commit(0d7cbd2 FORPREP/FORLOOP + 4e0e863 TFORLOOP/CLOSURE/CLOSE)是 stop-hook 强制后真的写完的产物——这是 PJ10 与 [[p3-pw10-architectural-ceiling-round]] PW10 stop-hook 升压结果方向相反的对偶轮。

## 核心教训(按强度排序)

### 1. 正确性 floor(语义覆盖)vs 性能 ceiling(native CFG emit)拆分——设计稿 §3 画的 native amd64 multi-BB CFG + label resolver 是「性能前提」,不是「语义前提」

设计稿 `10-per-op-translator.md` §3 架构总图画的是 native amd64 multi-BB CFG emit + label resolver(P3 wasm relooper 在 amd64 端的镜像):`buildCFG(proto)` 切 BB → `reach + reducibility check` → `按 BB rPostOrder 顺序 emit` → `resolveLabels 补 jmp 偏移` → wireToCodepage。在前序 commit b06b80a(`doc(p4): update PJ10 progress to include CALL/SELF/TAILCALL/FORLOOP/and-or`)文档化进度时,沿用此判断把 TFORLOOP / CLOSURE / CLOSE 标 「future PJ10c full / PJ10d full」推迟,理由是「这些 opcode 真接入需要先建 CFG / label resolver」。

stop-hook 强制不结束后实测发现这个判断是**错的**——这些 opcode 的**语义覆盖完全不需要 CFG**:

- **TFORLOOP** 是固定 4-op pattern:`JMP-skip-body + body + TFORLOOP + JMP-back`(crescent 编译器生成,bytecode 形态稳定);只要识别这个 pattern,Go 端跑「初次跳 body 入口 → 反复:跑 body 清单 + h_tforloop 判断继续/退出 + JMP-back」的循环就行,完全不要 CFG;
- **CLOSURE** 经 `proto.SubNUps[Bx]` 跳后随 pseudo MOVE/GETUPVAL 数据字(idiom 见教训 3)——这是 PC 步进逻辑不是 BB 切分;
- **CLOSE** 单点 host.Close,纯线性;
- **FORPREP/FORLOOP** 是数值循环 head + tail 对,Go 端跑 host.ForPrep 初始化 + 迭代 dispatch + bodyEffects 回放清单。

实际落地后 35/38 opcode 接住,语义与 P1 解释器逐字节一致,`make test-p4` 全绿。设计稿 §14(本会话新加的进度章)自己点明这条边界:**「FORLOOP 一旦真接入 native code,单 BB 回放就必须升级到多 BB native emit,否则循环每次迭代都要跨 Go ↔ mmap 边界一次,违反 PJ10b heavy bench 的 P3 ≥ 5× 加速 baseline。当前 Go 端 FORLOOP/TFORLOOP 已可正确执行,语义与 P1 解释器逐字节一致,但性能优势限于「函数边界 dispatch 省一次」」**——这句话准确分清两层:

- **正确性 floor**(Go 端回放)= 语义覆盖,先到位,把 35/38 opcode 一次性接住;
- **性能 ceiling**(native CFG emit + label resolver)= 性能延伸,后续 followup,只为解决「循环内每迭代跨 Go ↔ mmap 边界一次」的性能问题,**不解决语义覆盖问题**。

**Why**:设计稿 §3 的架构图画的是「目标态」(性能 + 语义双满足的最终架构),不带「语义先 / 性能后」的分层标注;读图时容易把它当成「这一切都是 PJ10 接入的前提」,但其中绝大部分(CFG / label resolver / native code emit)是性能前提——撤掉它们只是让循环慢,不让循环错。一个 Go 端 dispatch 即使每次跨 Go ↔ mmap 边界一次也仍然能产出逐字节正确的结果。这是「先做对,再做快」纪律在架构图层级的体现:**架构图不会主动标「哪部分撤掉只是慢、哪部分撤掉就是错」,这条分层须实现者动手前显式拆**。

**How to apply**:照设计稿落地任何新里程碑前,先把设计稿主张拆成「正确性 floor」+「性能 ceiling」两层:

- **floor 问**:撤掉这块,语义还正确吗?(若是,这块是性能件,可以留 followup);
- **ceiling 问**:撤掉这块,语义就错了吗?(若是,这块是正确性件,必须先做);
- floor 优先,把语义覆盖一次性扩到上限,性能件按 profile 数据决定何时上(参考 [[perf-optimization-workflow]] §7「profile 才是合同」)。

**与 PW10 的对偶**:[[p3-pw10-architectural-ceiling-round]] 教训 1 是「立项数字目标 vs profile 实测瓶颈」+ [[perf-optimization-workflow]] §7「profile 才是合同」——PW10 是「性能数字不可达就止损」(止损落档不硬上 ④-ii 200 行 wasm 字节级 UAF codegen);本轮 PJ10 是「正确性范围可达就先扩大、性能形态留 followup」(语义可全覆盖只是当时主助理误判要 CFG,实测 Go 端回放就够)。两者构成对偶判据:

| 维度 | PW10 ④-ii | PJ10 TFORLOOP/CLOSURE/CLOSE |
|---|---|---|
| stop-hook 升压方向 | 强制不结束 → 想硬上 ④-ii 追原数字目标 | 强制不结束 → 想留「PJ10c full / PJ10d full」推迟到后续里程碑 |
| 实证结果 | profile 证 ④-ii 上限 0.57x 仍 <1x **数字不可达** | 实测 Go 端回放语义全过、35/38 opcode 接住 **范围可达只是还没做** |
| 正解 | **止损落档**:文档化「call 0.52x 是 bench kernel 结构性架构边界」+ 留 followup | **真的写完**:0d7cbd2 + 4e0e863 两个 commit 一次性补齐 |
| 共享判据 | profile / scope-check 区分「可达只是还没做」vs「真的不可达需止损」 | 同左 |

### 2. stop-hook 升压时先做 profile / scope-check,再决定「真的写完」vs「止损落档」

[[p3-pw10-architectural-ceiling-round]] 教训 2 已明示「`/goal` stop hook 强制不能结束时的诚实解读边界」——hook 不能替执行者做「目标在当前事实下还合理吗」的判断。本轮是这条纪律的**对偶应用**——同样的 stop-hook 升压,PW10 该止损 / 本轮该真的写完。**区分判据**:

- 若 profile / 实证证明「即使做完数字也不达标」(PW10 ④-ii 上限 0.57x 仍 <1x),止损落档;
- 若 scope-check 证明「语义可达只是当时认为要 CFG 才能做」(本轮 TFORLOOP/CLOSURE/CLOSE),真的写完。

**本轮误判根因**:把设计稿 §3 原画的 native CFG 当成**语义覆盖前提**——「TFORLOOP 真接入需要先建 CFG / label resolver」是 b06b80a commit message 的隐含理由,实际它只是性能前提(参考教训 1)。stop-hook 强制后被迫做语义可达性 scope-check,才发现 Go 端固定 4-op pattern dispatch 完全够用。

**Why**:架构师视角的「这个 opcode 需要 X 基础设施」常常混淆「语义需要 X」与「性能需要 X」——尤其是 X 是大件(CFG / label resolver / native code emit)时,默认把它当成全维度前提。stop-hook 强制不结束,逼着回头做 scope-check,反而暴露了「其实只要 Go 端 dispatch 4-op pattern 就够」的事实。

**How to apply**:遇到 stop-hook 强制不结束时:

- **第一步**:做 profile / scope-check 区分「真的不可达需止损」vs「可达只是还没做」;
- **第二步**:若 scope-check 揭示「语义可达,只是当时主助理认为某基础设施是前提」——做教训 1 的 floor/ceiling 拆分,先把 floor 写完;
- **第三步**:若 profile 揭示「数字不可达」——按 PW10 的纪律止损落档;
- **绝不**:既不真做又不止损,只把 commit message 写「留 followup」推迟到后续里程碑——这才是 hook 真要防的形态。

**与 [[design-claims-vs-codebase-physics]] §5「时间维度」的关系**:那条 guide 的核心是「设计稿 / task / stub 注释承诺 / 外部依赖现状」类前序快照在事实变更后失效;本条的「正确性 floor vs 性能 ceiling 拆分」是**空间维度** wraparound——设计稿同一时间点对架构图的描述就**不分层**,须实现者动手前显式拆。属于 [[design-claims-vs-codebase-physics]] 家族第 7 个独立实例,但维度新(架构图不分层 floor/ceiling),**首次样本暂留观察**;若后续 P5 trace JIT / P4 backends 真出现类似「设计稿不分层导致误判前提」再现,可作 [[design-claims-vs-codebase-physics]] 新增 §7「架构图的 floor/ceiling 不分层」补充。

### 3. CLOSURE SubNUps 跳 pseudo 数据字 idiom 第 2 实例复发——已跨过 2-实例阈值,建议反引

CLOSURE A Bx 后随 `proto.SubNUps[Bx]` 条 pseudo MOVE/GETUPVAL 指令(描述每个 upvalue 怎么捕获,是数据不是可执行 opcode)。两条发射循环按 pc 迭代会把这些数据字误译成寄存器拷贝。**同款物理现象在 P3 PW7 已遇过**:

- [[p3-pw7-pw4b-closure-tforloop-round]] 教训 2:`emitOpcode` 改签名返回 `(skip, err)` + CLOSURE 返回 `SubNUps[Bx]` + 循环 `pc += 1 + skip`;
- 本轮 P4 PJ10:peroptranslator 同一面,用 `pseudoSkip map[pc]int` 记录每个 CLOSURE pc 后跳过几条 + `proto.SubNUps[Bx]` 长度。

`Proto.SubNUps[]` 注释「symbexec 精确跳过用」**天生为此设计**——它是规范级跳表 idiom,作者就是按「opcode 拥有后随数据字,跳过条数得是 Proto 级缓存的字段」这条物理事实写的。P3 / P4 两个独立翻译器各自撞上、各自用 `SubNUps[Bx]` 解,跨过 **2-实例阈值**。

**Why**:Lua 5.1 字节码流里「可执行 opcode 与 opcode 私有的数据字」是交织的,**任何按 pc 顺序迭代发射的翻译器都会撞**这条物理事实;P3 wasm 翻译器、P4 native 翻译器、未来的 P5 trace JIT 翻译器都得各撞一次。

**How to apply**:`SubNUps` 是规范级跳表 idiom,首次样本暂留独立 guide(P3 反思已记一次);本轮**第 2 实例跨过阈值**,建议在 [[p3-pw7-pw4b-closure-tforloop-round]] 教训 2 反引本反思,作为「同 idiom 第 2 次跨翻译器复发」的实证;后续若 P5 trace JIT 第 3 实例,可升 guide「字节码 opcode + 后随数据字 idiom 跨翻译器复用 `SubNUps[]` 跳表」。

### 4. P2 bridge F2-b 白名单决定 P4 测试可写形态(首次样本,跨层耦合)

P2 bridge `analyze_off.go::checkF2bSafeCall` 的「安全调用白名单」过滤决定了 P4 PJ10 测试能用什么 Lua 代码写 TFORLOOP e2e:

- pairs / ipairs **不在** P2 bridge F2-b 安全调用白名单(F2-b 静态分析不能证明它们不 yield);
- 经升层路径(crescent → gibbous / P4 jit)的 TFORLOOP 测试若用 `for k, v in pairs(t)`,proto 在 P2 闸门被烧 `ReasonUnknownCall` → 不升层 → P4 PJ10 永远不被走到;
- 解法:hand-written iterator,如 `local function customIter() local i = 0; return function() i = i + 1; if i > N then return nil end; return i, ... end end; for k, v in customIter() do ... end`——customIter 本身是用户脚本闭包,经 F2-b 静态分析判定**已知调用形态**,proto 可升层,TFORLOOP 路径真被走到。

这是 **P2 bridge 过滤器决定 P4 测试可写形态的跨层耦合**。

**Why**:P2 bridge F2-b 白名单是 wangshu **跨 tier 共享的可编译性闸门**——任何 tier(crescent baseline / gibbous wasm / P4 native)都共用同一份「这个 proto 可不可以升层」判定。当 P4 想给某个新形态(TFORLOOP)写 e2e,「该 proto 真的会走 P4 升层路径」依赖 P2 闸门放行;P2 闸门的过滤逻辑(F2-b 安全调用白名单未列 pairs/ipairs)反向决定测试 lua 源能用什么内置函数。

**与 [[issue18-p3-autolift-fix-round]] 教训 1「编译期保守占位 + 运行期实判清洗」的对比**:那是**同包内 lifecycle**(F7 编译期占位 → 运行期 P3 注入后清占位重判);本条是**上下游过滤器对下游测试设计的影响**(P2 闸门白名单 → P4 测试可写形态)。同属「跨阶段 gate 影响下游」family 但发力点不同——前者是 lifecycle 时间维度的占位 → 清占位,后者是 layer 空间维度的过滤器 → 下游适应。

**How to apply**:写新 tier 的 e2e 测试前,先 grep 该形态依赖的 stdlib 函数是否在 P2 bridge F2-b 白名单(`grep -rn "f2bSafeFuncs\|checkF2bSafeCall" internal/`)——若不在,要么 hand-write 等价 lua 形态(如本轮 customIter),要么先扩 F2-b 白名单(但那是另一个独立的 P2 修改,跨层耦合得显式)。

**首次样本暂留观察**,与 [[issue18-p3-autolift-fix-round]] 是 pattern 家族,下次出现可一并升 guide「跨层 gate 影响下游可测形态」。

## 其它(较小)

- **shapeInfo 单点数据结构**:`sources []slotSource`(N 个返回 slot 求值方式,14 种 slotKind)+ `sideEffects []sideEffect`(返回前需执行的 op,16 种 sideEffectKind)+ 循环专用字段 `forLoopValid` / `bodyEffects` / `tforLoopValid` / `tforLoopA`。这套表示让「Go 端回放」物理路径成为可能——所有可达 opcode 都拆成 head op(写 R(A)/R(A+i))+ 前序 side effect 序列,Run 阶段统一 dispatch。
- **占位 mmap stub 仅 3 字节(`xor eax, eax; ret`)**:保留 W^X / icache / r15=jitCtx wiring 路径校验,让 PJ1 PJ2 PJ8 等基建在 PJ10 路径上仍被走到(round-trip 通过 trampoline),没有任何一条物理基建因 PJ10 走 Go 端回放就成 dead code。
- **stop-hook 升压两次**:第 1 次「PJ10c FORPREP/FORLOOP 未实装」拒绝结束 → 单 commit 0d7cbd2 加 FORPREP/FORLOOP via Go-side iteration;第 2 次「TFORLOOP/CLOSURE/CLOSE 未实装」拒绝结束 → commit 4e0e863 一次性补完。两次都不是 PW10-style 止损,而是 PJ10-style「真的写完」(教训 1 / 教训 2)。

## 验证

- 80 个 PJ10 e2e 子测试全过(`go test ./internal/gibbous/jit/peroptranslator/ -tags 'wangshu_p4 amd64' -count=1 -v`);
- `make test-p4` 全绿,含官方 Lua 测试套(luasuite/closure.lua 等 14 文件全 PASS)、difftest 70 种子、`-race`;
- 35/38 opcode 接住(VARARG 设计永不接 + JMP / TEST / LOADBOOL C!=0 真 CFG 留 followup)。

## 促成的稳定文档更新

- `docs/design/p4-method-jit/10-per-op-translator.md` §14:新加「实现进度(2026-06-30 全 opcode 覆盖 Go 端回放)」章——明示「Go 端回放」物理路径与设计稿 §3 原画 native amd64 multi-BB CFG 的分歧,以及「FORLOOP 真接入 native code 才是性能拐点」的边界条件;
- `internal/gibbous/jit/peroptranslator/translator.go` 头注:`xor eax, eax; ret` 占位 stub 物理路径说明 + 单 BB 限制 + 未实装项清单;
- `internal/gibbous/jit/peroptranslator/peropcode.go` 头注:GibbousCode lifecycle + SetReg/DoReturn 通过 stub 物理路径回放 host helper 的契约;
- `llmdoc/index.md` 启动清单第 14 篇反思条目(本反思路径)。

## promotion 候选

- **教训 1「正确性 floor vs 性能 ceiling 拆分」**——与 [[p3-pw10-architectural-ceiling-round]] + [[perf-optimization-workflow]] §7「profile 才是合同」构成对偶。本轮**首次样本**,但已是 [[design-claims-vs-codebase-physics]] 家族(架构图同一时间点不分层 floor/ceiling 是空间维度新维度,与 §5 时间维度对偶),**暂留观察**;P5 trace JIT 接入 / P4 backends 6 后端再有类似「设计稿不分层导致误判前提」再现 → 可作 [[design-claims-vs-codebase-physics]] 新增 §7「架构图 floor/ceiling 不分层」补充,或独立小 guide「先做对(语义覆盖)再做快(性能下沉)」。
- **教训 2「stop-hook 升压时先做 profile / scope-check 再决定真做 vs 止损」**——与 [[p3-pw10-architectural-ceiling-round]] 教训 2「stop hook 强制不能结束时的诚实解读」构成对偶应用,**首次明确「升压结果方向相反」的判据**(PW10 该止损 / PJ10 该真写),**暂留观察**;若后续再有同款升压场景(stop-hook 强制后实际可达 vs 不可达分岔),可作 [[perf-optimization-workflow]] §7 邻接补充「升压判据:profile / scope-check 区分可达 vs 不可达」。
- **教训 3「CLOSURE SubNUps 跳 pseudo idiom 第 2 实例复发」**——P3 PW7 + P4 PJ10 同 idiom 跨翻译器,**已跨过 2-实例阈值**;建议在 [[p3-pw7-pw4b-closure-tforloop-round]] 教训 2 反引本反思作实证(memory 内反引,不升 guide);若 P5 trace JIT 第 3 实例,升 guide「字节码 opcode + 后随数据字 idiom 跨翻译器复用 `SubNUps[]` 跳表」。
- **教训 4「P2 bridge F2-b 白名单影响 P4 测试可写形态」**——首次样本,与 [[issue18-p3-autolift-fix-round]] 教训 1「编译期占位 + 运行期实判」是 pattern 家族(都是跨阶段 gate 影响下游),但维度不同(本条是空间维度过滤器、那条是时间维度 lifecycle);**暂留观察**,下次出现可一并升 guide「跨层 gate 影响下游可测形态」,落点 [[prove-the-path-under-test]] §4 邻接(那篇 §4 已含「写测试前 grep 既有 oracle」,本条新增「写测试前 grep 上游过滤器」维度)。

## 触发场景

- 接 P4 后续里程碑(PJ11 luajc 档突破 / native CFG emit + label resolver / P5 trace JIT)时(教训 1:先做 floor/ceiling 拆分,语义覆盖 floor 优先,性能件按 profile 决定);
- 看到设计稿架构图原画 native code emit + CFG + label resolver 等大件基础设施时(教训 1:问「撤掉它语义还正确吗」,若是则是性能件留 followup);
- `/goal` stop-hook 强制不结束时(教训 2:先做 profile / scope-check,区分「可达只是还没做」vs「真的不可达需止损」,前者真写、后者止损落档,**绝不**只把 commit message 写「留 followup」推迟);
- 给新翻译器(P5 trace JIT / 新后端)写 opcode 发射循环时(教训 3:`Proto.SubNUps[Bx]` 是规范级跳表,CLOSURE 后随 pseudo MOVE/GETUPVAL 数据字 pc 步进经它);
- 给某 tier 写 e2e 用到 stdlib 函数(pairs/ipairs/string.* 等)前(教训 4:先 grep `f2bSafeFuncs` / `checkF2bSafeCall` 看是否在 P2 bridge F2-b 白名单;不在则 hand-write 等价 lua,或先扩 F2-b 白名单作独立修改);
- 看 PJ10 物理路径分歧(设计稿 §3 native amd64 multi-BB CFG vs 实际 Go 端回放 + 占位 stub)时——这是「正确性 floor 已交付、性能 ceiling 留 followup」的实证样本,读这篇了解判据。

## 关联

[[p3-pw10-architectural-ceiling-round]](**直接对偶**:stop-hook 升压结果方向相反——PW10 该止损落档 / PJ10 该真的写完;判据「profile / scope-check 区分可达 vs 不可达」)· [[perf-optimization-workflow]] §7「profile 才是合同」(立项数字目标 vs profile 实测瓶颈,本轮教训 1 / 教训 2 共享判据基础)· [[p3-pw7-pw4b-closure-tforloop-round]] 教训 2(CLOSURE SubNUps idiom 第 1 实例,本轮第 2 实例复发,跨翻译器跨过 2-实例阈值)· [[design-claims-vs-codebase-physics]] §5「时间维度」(本轮教训 1 的「架构图同一时间点不分层 floor/ceiling」是空间维度新维度候选,与 §5 时间维度对偶)· [[issue18-p3-autolift-fix-round]] 教训 1(编译期占位 + 运行期实判,本轮教训 4「P2 bridge F2-b 白名单」是 pattern 家族但空间维度而非时间维度)· [[prove-the-path-under-test]] §4(写测试前 grep 既有 oracle,本轮教训 4 新增维度「grep 上游过滤器」)· `docs/design/p4-method-jit/10-per-op-translator.md` §3 架构总图(设计稿原画 native CFG emit)+ §14 实现进度(本会话新加,实际 Go 端回放物理路径)· `internal/gibbous/jit/peroptranslator/translator.go` + `peropcode.go`(本会话交付主体)· 本会话 commits `a94bcec..HEAD`(21 commits,~3420 行,80 e2e subtests 全过)
