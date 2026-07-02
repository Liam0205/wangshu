# P5 §7:系统集成——三层执行阶梯 + P2 桥扩展 + trace 入口安装 + 世界边界复用

> 状态:**未立项图纸(启动判定见 [01](./01-launch-judgment.md))**。本文是 P5「系统集成」的单一
> 事实源:三层执行阶梯(crescent / gibbous / fullmoon)如何叠加、P2 桥状态机如何扩展、trace 入口安装、
> 与 P4 系统管线的世界边界复用、fullmoon 的 host 接口、代码缓存生命期、协程与线程规则、GC 交互。**P5
> 有零行代码**,所有结论表述为「建议 / 推荐 / 待 PT-N spike 验证」——本文任何具体协议在 PT0 spike 前
> 都不构成契约,只作为详细设计入口后展开细节的起点。
>
> 对应 Go 包:`internal/fullmoon/trace`(拟);build tag 建议 `wangshu_p5`(承 P4 `wangshu_p4` 命名)。
>
> 上游契约:
> [./00-overview.md](./00-overview.md)(§3 基建复用表 / §5 开放问题索引「fullmoon
> 与 gibbous 的热度交接」);
> [../p4-method-jit/05-system-pipeline.md](../p4-method-jit/05-system-pipeline.md)(P4 系统管线单一事实
> 源——本文所有物理边界叙述都以此为基准,PR #42 后与实现对账过);
> [../p4-method-jit/06-backends.md](../p4-method-jit/06-backends.md)(amd64/arm64 寄存器约定 / W^X mmap /
> icache flush——本文物理层默认继承,不重写);
> [../p2-bridge/00-overview.md](../p2-bridge/00-overview.md)(P2 决策加工厂定位 / P2 不加速)、
> [../p2-bridge/01-profiling.md](../p2-bridge/01-profiling.md)(回边 + 入口采样 / 挂 State 私有 profileTable)、
> [../p2-bridge/04-try-compile-fallback.md](../p2-bridge/04-try-compile-fallback.md)(TierState 单向 + 吸收 /
> TierStuck 不重试 / P4 才引入 deopt 反向边);
> [../p3-wasm-tier/07-coroutine-thread-rule.md](../p3-wasm-tier/07-coroutine-thread-rule.md)(**主线程独享
> 升层**规则,P4 已继承,P5 沿用);
> [../p1-interpreter/06-memory-gc.md](../p1-interpreter/06-memory-gc.md)(GC 根 R1-R9 / preemptFlag /
> safepoint 三类);
> [../p1-interpreter/05-interpreter-loop.md](../p1-interpreter/05-interpreter-loop.md)(CallInfo 布局 / bit50
> gibbous 位 / reloadFrame 纪律)。
>
> 下游同章:
> [06-snapshot-deopt.md](./06-snapshot-deopt.md)(exit stub 复用 ExitOSR + 寄存器 dump 区,本文 §4 引用);
> [08-testing-strategy.md](./08-testing-strategy.md)(测试策略——本文 §8 GC + coroutine 联合验的入口)。
>
> **本文定位一句话**:**P5 是叠在 P4 之上的第三执行层,系统管线一寸不重写——所有物理税继承 P4
> 全套(mmap+RX / trampoline / preemptFlag / RefreshJitCtxAddrs / GCRef 偏移寻址),只在 P4 骨架里加
> 「trace 段」这一种新的 mmap 段类型,以及扩展 host 接口一小把方法给 deopt 驱动用**。

---

## 0. 定位

### 0.1 一句话:trace 是叠加,不是替代

承 [00-overview §3 基建复用表](./00-overview.md):**P5 不推倒任何已有层**,fullmoon 是 gibbous 之上的第三
执行层。基建全复用,只**新增四件套**:①trace 录制器 ②SSA IR + 优化 pass ③trace 寄存器分配器 ④snapshot
+ deopt 机器。前三件是「编译期产物」,与本文的运行期系统集成关系不大;第四件是「运行期物化机器」,
本文只讲 exit 通道与 host 接口,详细语义留 [06-snapshot-deopt](./06-snapshot-deopt.md)。

**核心工程红利**(P5 从 P4 白继承的):

| 项 | P4 已解 | P5 直接沿用 |
|---|---|---|
| 自管代码页(mmap RW → mprotect RX / munmap) | [P4 05 §2.1](../p4-method-jit/05-system-pipeline.md) | 只多一个 trace 段类型 |
| W^X 翻面(含 macOS arm64 MAP_JIT 待 spike) | [P4 05 §2.2](../p4-method-jit/05-system-pipeline.md) | 不重写 |
| icache flush(arm64) | [P4 05 §2.3](../p4-method-jit/05-system-pipeline.md) | 不重写 |
| trampoline 进出 + status 码 + `CallJITSpec` | [P4 05 §2.4 / §4](../p4-method-jit/05-system-pipeline.md) | 只加两个 status 码 |
| jitContext 骨架(r15 固定 + arenaBase/vsBase 刷新)| [P4 05 §3.3](../p4-method-jit/05-system-pipeline.md) | 加 3 个字段(§4.3)|
| `RefreshJitCtxAddrs` 批量刷新(arena grow 后必调) | [P4 05 §3.5](../p4-method-jit/05-system-pipeline.md) | 沿用同款纪律 |
| preemptFlag inline 回边检查 | [P4 05 §6.2.3](../p4-method-jit/05-system-pipeline.md) | trace 循环回边 emit 同款序列 |
| GCRef 偏移寻址(无 Go 堆指针) | [P4 05 §1.4](../p4-method-jit/05-system-pipeline.md) | trace 内所有值也是 arena 偏移 |
| 主线程独享升层(协程线程恒解释) | [P3 07](../p3-wasm-tier/07-coroutine-thread-rule.md) | 直接继承(§7)|

**新增的四小项**(本文与 [06](./06-snapshot-deopt.md) 一起拆解):

1. trace 段类型 = 一种新的 mmap 段(§6),lifecycle 与 P4 per-Proto 段并行;
2. jitContext 加 3 个字段(register-dump 区指针 / snapshot 表指针 / trace 段 codePageAddr,§4.3);
3. `P4HostState` 接口扩 1 组 deopt 驱动方法(`PushDeoptFrame` / unsink 分配 hook,§5);
4. P2 状态机加「per-(proto, loop-pc) trace 锚点」侧表(§2);TierState 主枚举不动。

### 0.2 章节路标

| § | 主题 | 谁拥有 |
|---|---|---|
| §1 | 三层执行阶梯(crescent / gibbous / fullmoon)+ 层间协议 | 本文 |
| §2 | P2 桥扩展:trace 锚点表 + 热度交接 SEED §6 开放问题 | 本文 §2.1-§2.4 |
| §3 | trace 入口安装:crescent 分派点 + P4 段不 patch(v1) | 本文 |
| §4 | 世界边界复用:P4 全套 + jitContext 新增字段 | 本文 |
| §5 | Host 接口:`P4HostState` 扩展给 deopt 驱动用 | 本文 |
| §6 | 代码缓存管理:trace 段 lifecycle + eviction | 本文 |
| §7 | 协程 & 主线程规则(承 P3 07) | 本文 |
| §8 | GC 交互汇总(safepoint / 无 Go 堆指针 / gen invalidation) | 本文 |
| §9 | 开放问题 | 本文 |

---

## 1. 三层执行阶梯

### 1.1 结构

承 [00-overview §3 三层升降关系](./00-overview.md) 与 [evolution-roadmap](../../../llmdoc/architecture/evolution-roadmap.md) 的 tier 映射(crescent = tier-0,gibbous = tier-1,fullmoon = tier-2):

```
                          望舒三层执行阶梯(P5 启动后)
   ┌─────────────────────────────────────────────────────────────────────────┐
   │  crescent(tier-0,永不退役)                                             │
   │   - 解释器,语义 oracle                                                  │
   │   - trace 录制宿主(P5 独有,承 [02](./02-trace-recording.md))          │
   │   - **所有编译层 deopt / fallback 的唯一着陆点**                          │
   │   - 主线程 + 协程线程都跑                                                │
   └─────────────────────────────────────────────────────────────────────────┘
                    ↑ trampoline 出/进(P4 05 §4 复用)
                    │ status: OK / ERR / DEOPT / InlineHelper
                    ▼
   ┌─────────────────────────────────────────────────────────────────────────┐
   │  gibbous(tier-1)= P3 wasm + P4 native(共 tier,只换后端)                │
   │   - 函数级稳态编译层                                                     │
   │   - 覆盖:trace 黑名单函数 / trace 未覆盖的热函数                        │
   │   - deopt 落 crescent(P4)/ 无 deopt(P3)                              │
   │   - **只在主线程跑**(P3 07 规则)                                       │
   └─────────────────────────────────────────────────────────────────────────┘
                    ↑ trampoline 出/进(P4 05 §4 复用)
                    │ (fullmoon 的 guard 失败 **不落此层**;见 §1.3)
                    ▼
   ┌─────────────────────────────────────────────────────────────────────────┐
   │  fullmoon(tier-2,P5 若启动)                                            │
   │   - trace JIT,跨函数内联的最热路径                                       │
   │   - guard 失败 → **直接落 crescent**(§1.3),不经 gibbous                │
   │   - **只在主线程跑**(§7)                                                │
   └─────────────────────────────────────────────────────────────────────────┘
```

### 1.2 层间调用协议

**统一继承 P3 04 + P4 05 的 CallInfo 协议**(承 [P4 05 §3.6.3](../p4-method-jit/05-system-pipeline.md) 不变式 3):

- Lua 帧共享同一 CallInfo 结构([P1 05 §1.2](../p1-interpreter/05-interpreter-loop.md) word 布局);
- bit50 `callStatus_gibbous` 在 gibbous / fullmoon 帧上都置 1(区分靠帧上下文,不靠新增位);
- 跨层调用一律经 trampoline + status 码;
- **P5 不发明新的调用记录**——trace 内联的函数在录制时被内联到 trace IR 里,运行期没有物理帧;deopt
  时按 snapshot 逐帧物化(见 [06](./06-snapshot-deopt.md))。

### 1.3 fullmoon guard 失败为什么落 crescent 而非 gibbous(承 00-overview §3)

00-overview §3 字面:**「fullmoon 的 deopt 着陆点是 crescent 而非 gibbous——deopt 语义以解释器为 oracle
(原则 1),落到 P4 码反而要二次映射状态,得不偿失」**。展开三条工程理由:

1. **状态映射一次搞定**:fullmoon 的 snapshot 天然按「解释器视角」编码——slot 号 = R(i)、frames[] =
   CallInfo 链、exitPC = 字节码 pc。crescent 从 exitPC 处 reloadFrame 续跑([P1 05 §1.3](../p1-interpreter/05-interpreter-loop.md))就是「同一 pc 的解释器语义」,零适配。
2. **落 gibbous 要二次映射**:P4 也是模板编译,栈槽即真相([P4 04-osr-deopt §3.1](../p4-method-jit/04-osr-deopt.md)),但 P4 的模板入口地址与 pc 的映射表在 P4 内部,若 fullmoon 直落 P4 段,需要 fullmoon 编译器
   知道 P4 侧的物化入口——两个编译层耦合成本高、bug 面翻倍。
3. **deopt 后热度自然爬回**:crescent 续跑后,P2 的 profileTable 继续记录回边计数——若该 Proto 之前
   已在 `TierGibbous` 状态,后续调用**从 gibbous 入口开始跑**(P4 05 §4.1 出口 A 的对偶),完全不需
   要 fullmoon 侧做「回哪一层」的决策。**热度回爬由 P2 状态机自然完成**,fullmoon 只管把状态解回
   crescent 即可。

推论:**fullmoon guard 失败的 exit stub 拆栈后一律 status=DEOPT,与 P4 OSR exit 走同一 trampoline 出口
通道**——只是物化序列不同(P4 是空;fullmoon 是「按 snapshot 逐帧物化 + unsink」)。详见 [06](./06-snapshot-deopt.md)。

### 1.4 三层与 P2 状态机的关系

**关键澄清**:三层执行**不**等于 P2 三种 tierState。P2 的 tierState 是「per-Proto 分层意图」,fullmoon
是「per-(Proto, loop-pc) 或 per-trace 的路径投机」,两者维度正交:

| 维度 | 谁管 |
|---|---|
| per-Proto tierState(TierInterp / TierGibbous / TierStuck) | P2([P2 04](../p2-bridge/04-try-compile-fallback.md)) |
| per-Proto 已升 P4 且有 P4 段 | P2(installGibbous) |
| **per-(Proto, loop-pc) 有 fullmoon trace 锚点** | **P5 侧表(§2.2)**,与 P2 tierState 并存 |
| per-trace P5SpecState(录制中 / 生效 / 已 evict / 黑名单) | P5 侧表(§6.2)|

**同一 Proto 完全可能**:P2 tierState = TierGibbous(P4 段已装)+ 其某热循环 pc 有 fullmoon trace 锚点。
调用该 Proto:主线程从 P4 段入口跑;跑到该循环 pc 时,若锚点仍活,fullmoon trace 接管;guard 失败退到
crescent(不退回 P4 段)。这套复合关系正是 SEED §6 开放问题的核心——§2 展开。

---

## 2. P2 桥扩展:trace 锚点表 + 热度交接

### 2.1 现状(P4 时代)

承 [P2 04 §2](../p2-bridge/04-try-compile-fallback.md):P2 状态机三态(TierInterp/TierGibbous/TierStuck),
单向 + 吸收;P4 加了 deopt 反向边(gibbous → interp),但只在 P4 内部子状态机层面(承 [P4 04 §5](../p4-method-jit/04-osr-deopt.md) P4SpecState),**P2 主枚举不变**。

回边采样与升层入口([P2 01 §4](../p2-bridge/01-profiling.md) `onBackEdge` / `considerPromotion`)在 P4
build 下沿用不变。

### 2.2 P5 建议扩展:锚点侧表

**建议(待 PT1 spike 验证)**:P5 在 `internal/bridge`(或 `internal/fullmoon`)加一张**锚点侧表**,与
per-Proto `tierState` 并存:

```go
// 建议;PT1 spike 前不定字段与访问路径
type TraceAnchor struct {
    proto   *bytecode.Proto  // 拥有此锚点的 Proto
    loopPC  int32            // 循环起点 pc(字节码位置)
    trace   *fullmoonTrace   // 编译好的 trace 段(nil = 录制中或已 evict)
    hotBackEdge uint32       // per-锚点回边计数(独立于 P2 的函数级 backEdge)
    state   AnchorState      // Recording / Live / Blacklisted / Evicted
}
```

关键设计取舍(与 P2 决策速查表 §6 同款语气):

- **不动 P2 tierState 枚举**:trace 锚点是「路径级」而非「函数级」,加进 P2 枚举会破坏其单向+吸收的
  形式化;单独一张侧表让 P5 独立演进,与 P2 04 §0.3 的「P4 才引入降层,不修 P2 定稿」思路一致。
- **锚点是 (Proto, loop-pc)**:每个函数可能有多条内层循环,各自独立成 trace(与 LuaJIT 类似);顶层
  函数入口 trace(up-recursion)v1 不做,留 v3+(06-snapshot-deopt §4)。
- **AnchorState 有黑名单态**:trace 录制中断(NYI / 太长 / 失败)→ 该锚点标 Blacklisted,永不再试
  ——**这是 00-overview §3 原则 4 的 P5 兑现**([design-premises 原则 4](../../../llmdoc/must/design-premises.md)
  「走 fallback 不做完备性」的 P5 版本),防「同一热循环反复录制反复失败」的抖动。

### 2.3 SEED §6 开放问题:gibbous 与 fullmoon 热度交接

SEED §6 明确点名:**「fullmoon 与 gibbous 的热度交接细节(从 P4 码的回边直接热到 trace,还是必须经
解释器录制——LuaJIT 无此问题,它只有一层 JIT;望舒三层结构特有)——立项后定」**。

关键约束:P4 mmap 段的回边不再回调 `bridge.onBackEdge`(承 [P2 01 §2.5](../p2-bridge/01-profiling.md) 定
稿:tierState 非 TierInterp 时 onBackEdge 立即 return,gibbous 帧根本不会调进来)。**这就意味着:一个
proto 已升 P4 后,P2 侧的回边计数就停在升层时的值不再增长**。若期望 P5 用回边计数触发 trace 候选,得
换个办法。

**三条候选方案**(待 PT1 spike 决定):

| 候选 | 机制 | 优点 | 缺点 |
|---|---|---|---|
| **A**:P4 段回边 ping | P4 emit 时在回边加一次「每 N 次自增 jitContext 里的一个计数字段,越阈值经 exit-reason 通道请求 trace 候选评估」 | 精确按 P4 段真实回边计数 | 每 N 次回边多一次 memory store + 阈值比较,吃紧循环性能;实测收益未必值得(06-snapshot-deopt §4 明说「显式 guard 密集吃收益」);且要动 P4 emit 器,与「P5 不动 P4」相悖 |
| **B**:P2 提升期 profile 预判 | P2 从 TierInterp → TierGibbous 前,用当时的 backEdge 计数决定「顺便标 traceCandidate」;若已很热,先建 trace 锚点再升 P4 | 不动 P4 emit;复用 P2 已有计数 | 只覆盖「升 P4 时就已经跨过 trace 阈值」的 proto——如果一个循环是升 P4 之后才热起来的(常见),就抓不住 |
| **C**:demote-to-record(**推荐 v1**) | 该 proto 的**入口计数**(不是内层回边计数,是外层调用次数)仍在 P2 侧记(P4 段入口经 trampoline 进,`Bridge.Run` 是 Go 侧函数,天然可以 count);当入口计数 × N 后周期性一次调用**改从 crescent 起跑**(临时把 tierState 视为 TierInterp 但只此一次),同时录制器 armed → 拿到 trace 后建锚点 | 不动 P4 emit;只在极稀疏的「demote 时机」付成本;录制走 crescent 是 00-overview §3 已选路径 | 每次 demote 一次调用的性能损失(该次跑 crescent 慢);需要一个「一次性 demote」机制,状态机上是新增的短暂反向边(比 P4 的 P4Deoptimized 更轻,但仍打破 P2 单向不变式) |

**推荐 C,标为 PT1 验证项**。理由:

- C 是唯一「不动 P4 emit + 能抓 P4 期间才热的循环」的候选;
- 「一次性 demote」比 P4 的 deopt 更轻——不是 guard 失败被迫退,是**主动一次**;
- 性能开销可摊薄:如果 demote 频率 = 每 10 万次入口一次,单次 demote 慢 10x,平均只慢 0.01%,可忽略;
- 与 P2 04 §0.3 「P4 才引入降层」的界线保持一致——C 引入的不是 P2 tierState 的降层,而是**per-调用一次
  的临时路径切换**,tierState 仍是 TierGibbous;
- 若实测 C 撞出「demote 时机与 P4 的 SpecState 交互(比如 P4 正好在这次 demote 里被误 deopt)」类问题,
  fallback 到 B 也可接受(损失部分覆盖率,但语义更简单)。

**tradeoff 明说**(承 [prove-the-path-under-test](../../../llmdoc/guides/prove-the-path-under-test.md)
思路):C 的空测风险 = 「demote 触发了但录制器仍抓不到 trace(因为 NYI 或长度上限)」→ 锚点标 Blacklisted
后不再 demote,损失就此吃掉;不会反复 demote 造成累积开销。

### 2.4 阈值建议

**建议(待 PT1 校准)**:trace 阈值应**明显高于** P2 的升层阈值(承 SEED §2「比 P4 升层阈值更高」):

| 阈值 | P2 现状(P4)| P5 建议 |
|---|---|---|
| HotBackEdgeThreshold | 1000([P2 01 §5.1](../p2-bridge/01-profiling.md)) | 用于 P4 升层不变 |
| **TraceHotBackEdgeThreshold** | — | **建议 10000**(比 P4 升层高 10x,待 PT1 实测)|
| **TraceEntryDemoteInterval**(候选 C 用) | — | **建议 100000** 次入口一次 demote |
| MaxTraceLength(SEED §6 缺口) | — | 待 PT2 校准(LuaJIT 默认 1000 IR nodes,望舒建议起步 500)|

阈值不影响正确性(承 P2 01 §5.3 同款结论),晚建 trace 只是少赚不出错——这是 P5 内部第二闸门的实现
落点(06-snapshot-deopt §4「+2-4 人年」的中途校验)。

---

## 3. trace 入口安装

### 3.1 v1:crescent-only 入口

**建议**:v1 阶段 trace 入口**只从 crescent 触达**,P4 段**不 patch**。理由:

- v1 采用 §2.3 的 demote-to-record 方案,该次调用本就从 crescent 起跑,循环 back-edge 打到 fullmoon 是
  自然路径;
- P4 段一旦 mprotect PROT_EXEC 后不再写(W^X;PJ0 spike 待 macOS arm64 验证是否可省 MAP_JIT,承 [P4 06
  §2.2.2](../p4-method-jit/06-backends.md))——patching 需要重新回 mprotect RW 再翻回 EXEC,per-thread
  MAP_JIT 状态与 goroutine 调度交互复杂,PJ0 spike 明确点名待验;
- P5 v1 目标是「跑通四件套 + 撞出 snapshot bug」,不是「最大化 trace 触达」——crescent-only 入口足够。

**代价**:P4 期间才热起来的 proto,demote 一次的性能开销(§2.3);如果宿主 workload 的 90% CPU 在
demote 之间的 P4 段跑,fullmoon 加速面就是 10% 的 demote 那次——但那次进 fullmoon 后可能 loop 10 万次,
所以真实覆盖率并不像单次调用的 10% 那么低。**PT7 端到端验收阶段的 profile 才能给出真实数字**。

**v3+ 可选**:P4 段回边加 anchor probe(候选 A 一部分)—— 但只在 v1/v2 撞出「demote 覆盖率不够」的
证据后再启用。

### 3.2 crescent 侧的分派点

**建议(待 PT2 录制器实现时定)**:crescent 的主循环在每条回边 opcode(FORLOOP / TFORLOOP / 循环体内
向后 JMP)执行完成、执行下一条前,查一次 anchor 表。

**性能担忧**:每回边一次 map 查找(建议锚点表用 Proto 旁 side-map + `map[int32]*TraceAnchor`)对紧循环
是几十 ns 的持续开销,吃 crescent 本身的性能。

**优化方向(择一,待 PT2 实测)**:

| 方案 | 机制 |
|---|---|
| **B1**:Proto 旁 bitmap | 每 Proto 建一 `[]uint64` bitmap,per-pc 一位标「此 pc 是 anchor」;回边先查 bit(1 次 shift + AND),命中才查 map。冷 pc 位为 0,零开销 |
| **B2**:全局哈希缩过滤 | 一个 bloom filter 或按 proto ID 分桶,先查 filter 排除 no-anchor 的 proto,再进 map。适合 anchor 稀疏 |
| **B3**:热 pc 上打 opcode 变体 | 在字节码里 patch 该 pc 的 opcode 编号成 `FORLOOP_ANCHORED`(占用 P1 02 §4 编号 38..63 预留位一格),dispatch 时直接派到 fullmoon 入口 | 与 P2 定稿「路线 B 不占用 opcode」冲突;不推荐 |

**推荐 B1**(内存开销可接受、检查便宜、无需动字节码)。这也是 SEED §6 「trace 阈值 / trace 长度与深度
上限」缺口留给 PT2 校准的空间。

### 3.3 gibbous(P4)段的 anchor 检查

**建议 v1**:P4 段完全不查 anchor(承 §3.1 crescent-only)。demote 机制在 Go 侧 `Bridge.Run` 层触发,
P4 段自身零感知。

**建议 v3+**:若 v1/v2 撞出覆盖率不足证据,考虑在 P4 emit 器的 FORLOOP 模板尾加一次「读 jitContext 里
的 anchor probe 字段 + jne exit-reason」——但这条动 P4 emit 器,与本文「P5 不动 P4」的边界原则冲突,
标记为**高门槛提议**,需要专门的 PT-N spike 验证。

---

## 4. 世界边界复用

### 4.1 P4 全套原样继承

承 [P4 05 §3.1](../p4-method-jit/05-system-pipeline.md) 的两世界模型:

- Go 世界(goroutine 栈 + 正常 Go 帧,GC 可见 + 可抢占);
- 自管世界(Go 堆 + arena,GC 不可见 + 不可异步抢占,以 trampoline 为唯一边界)。

**P5 trace 段**在物理上属于自管世界的一个新段——每条 trace 是一个独立 mmap 段(§6),生命期与自管栈
布局([P4 05 §3.4](../p4-method-jit/05-system-pipeline.md))完全同款,只是内容不同(P4 段 = per-Proto
模板序列;fullmoon 段 = 线性 IR 优化后的机器码 + snapshot 表 + exit stubs)。

### 4.2 preemptFlag 回边检查 emit 同款序列

承 [P4 05 §6.2.3](../p4-method-jit/05-system-pipeline.md):

```
;; amd64 概念伪码;承 P4 06 §4.1.2 三固定寄存器纪律
cmpb byte ptr [r15 + preemptFlagOff], 0
jne  exit_to_safepoint    ;; 进 exit stub 走 ExitInlineHelper(HelperSafepoint)
```

trace 侧同款——**每条 trace loop 的回边必须 emit 一次**(承 SEED §2 「trace 回边零开销」的现实修正:
零开销做不到,只能承 P4 全显式 guard 密度)。这是 SEED §6 风险 3「全 Go 约束对 trace 收益的折损」的
物理成因,10-30x 加速带的上沿够不到的一部分就吃在这里。

### 4.3 jitContext 新增字段

**建议(待 PT4 寄存器分配器实现时定)**:P4 的 `JITContext`([P4 05 §3.3](../p4-method-jit/05-system-pipeline.md)
的六组字段)在 P5 build 下需要**加 3 个字段**:

| 字段 | 类型 | 用途 |
|---|---|---|
| `regDumpArea` | `uintptr` | 指向 trace 段的寄存器 dump 区(exit stub 前 spill 所有 IR 值的槽,承 [05](./05-register-allocation.md) / [06 §5](./06-snapshot-deopt.md))|
| `snapshotTable` | `uintptr` | 指向本 trace 的 snapshot 表基址(exit stub 按 guard index → snapshot 索引,承 [06 §3](./06-snapshot-deopt.md))|
| `traceCodePageAddr` | `uintptr` | 当前 fullmoon trace 段的 mmap 起点(与 P4 `codePageAddr` 语义同源,只是 P5 侧另存一份,让同 jitContext 可同时挂 P4 段与 trace 段)|

其它字段(arenaBase / valueStackBase / preemptFlag / exitReasonCode / exitArg0 / resumeOff / savedGoG /
hostRef / ciDepthAddr 等)**沿用不动**——trace 段用同一份 jitContext,共享 `RefreshJitCtxAddrs` 五地址
刷新纪律。

这一小段扩展是 P5 与 P4 唯一不共享字段的地方,守住 [00-overview §3 基建复用表](./00-overview.md) 「jitContext
原样复用」的字面承诺(只 +3 个字段不算破坏)。

### 4.4 GCRef 偏移寻址不变

承 [P4 05 §1.4](../p4-method-jit/05-system-pipeline.md) 「写屏障白赚」:trace 段所有对 arena 的读写同样
是 `[rbx + 8*slot]` / `[r11 + fieldOff]`(r11 是按需装 arenaBase 的 scratch 寄存器,承 [P4 06 §4.1.1](../p4-method-jit/06-backends.md))。fullmoon 优化器做完 sink / CSE / FOLD 后的 IR 值,最终落回栈槽或表槽时,依然
是「写 NaN-boxed u64 到自管内存里」,Go GC 完全不可见,无写屏障义务。

### 4.5 helper 调用的两条通道

承 [P4 05 §4.3](../p4-method-jit/05-system-pipeline.md) 实现勘误:P4 现有**两条通道并存**——(a) exit-reason
协议(主通道)+ (b) shim 直调(次通道,历史遗留)。

**建议**:fullmoon 全部走 (a) exit-reason 通道,不引入新 shim。理由:

- shim 通道在嵌套 + 多 State 并发下已知易碎(P4 05 §4.3.1b + issue #38),trace 内联天然嵌套更深,风险
  放大;
- exit-reason 通道的 dispatcher 循环([P4 05 §4.3.1a](../p4-method-jit/05-system-pipeline.md))已经在 P4
  跑通、passed 58+ difftest,不重复造轮子;
- fullmoon 侧需要在 exit-reason 上加一个新 code `ExitDeopt`(见 §5)与几个新 helper code,dispatcher 分派
  表加对应分支即可,不需要新通道。

**NYI 与 helper-recordable 的区分**:某些 op 在 trace 录制期完全无法处理(NYI,直接弃 trace 拉黑);另
一些 op 快路径可录制但慢路径必须调 helper(如算术慢路径元方法、`GETTABLE` miss 变 helper)——后者与 P4
同款走 exit-reason 通道。具体清单与 [02 §NYI](./02-trace-recording.md) 协调,本文只承诺物理通道。

---

## 5. Host 接口:P4HostState 扩展

### 5.1 复用与扩展的界线

P4 已有 `P4HostState` 接口(承 [P4 05 §4.3.3](../p4-method-jit/05-system-pipeline.md),约 30 个方法),
分布在 `internal/gibbous/jit/host.go`。**建议 P5 复用同一份接口 + 扩展一组 deopt 方法**,不建独立
`FullmoonHostState`。

方法分类表(建议,待 [06](./06-snapshot-deopt.md) 详细设计对齐):

| 方法 | 来源 | 用途 | trace 运行时 | deopt 驱动 |
|---|---|---|---|---|
| `Arith` / `GetTable` / `SetTable` / `NewTable` / `Concat` | 复用(P4 已有) | 慢路径元方法 / IC miss | ✓ | — |
| `DoReturn` / `CallBaseline` / `TailCall` | 复用 | 跨层调用尾巴 | ✓(录制期决定内联否) | ✓(unsink 后重放 return)|
| `RefreshJitCtxAddrs` | 复用 | arena grow / 值栈搬家后刷 5 地址 | ✓(每次 exit-reason 回来必刷) | ✓ |
| `GlobalsRaw` | 复用 | 编译期烧 globals u64 | ✓(与 P4 同款,同 State 生命期内不变)| — |
| `SaveGoG` / `SetHostRef` | 复用 | ABIInternal 恢复 | ✓ | ✓ |
| **`PushDeoptFrame`** | **新增** | 按 snapshot frames[] 逐帧补建 CallInfo 链 | — | ✓ |
| **`UnsinkAlloc`** | **新增** | 按 sink 配方真实分配 arena 对象,写字段 | — | ✓(见 [06 §4](./06-snapshot-deopt.md))|
| **`RestoreExitPC`** | **新增** | 写 exitPC 到当前 CallInfo.savedPC,让 crescent reloadFrame 续跑 | — | ✓ |

**关键设计取舍**:

- 新增的三个方法**只被 dispatcher 在 `ExitDeopt` 分支调用**,不进入 trace 段本体——保持「trace 段
  emit 的机器码里没有 Go 函数指针」的 P4 05 §1.1 不变式;
- `PushDeoptFrame` 与 `UnsinkAlloc` 都可能触发 arena grow,调用后 dispatcher 必须再调
  `RefreshJitCtxAddrs`——沿用 P4 05 §3.5 纪律,不发明新纪律。

### 5.2 ExitDeopt 状态码

`P4HostState` 接口不动的前提下,exit-reason 协议新增一个状态码:

```
const (
    ExitNormal       uint32 = 0    // P4 已有
    ExitError        uint32 = 1    // P4 已有
    ExitOSR          uint32 = 2    // P4 已有(P4 用)
    ExitInlineHelper uint32 = 3    // P4 已有
    ExitDeopt        uint32 = 4    // ★ P5 新增(fullmoon 用,trace guard 失败)
)
```

**dispatcher 分支**(建议,待 PT5 实现校准):

```
switch exitReasonCode {
case ExitDeopt:
    // exitArg0 打包 (snapshotIndex,  guardIndex, ...);
    // resumeOff 不用(deopt 不重入 trace 段)
    snap := traceContext.snapshotTable[snapshotIndex]
    for _, frame := range snap.frames {
        host.PushDeoptFrame(frame.protoID, frame.baseOff, frame.retPC)
    }
    for _, sink := range snap.sinks {
        host.UnsinkAlloc(sink)  // 真实分配 + 写字段
    }
    host.RefreshJitCtxAddrs(jitCtx, base)  // grow 可能触发
    host.RestoreExitPC(snap.exitPC)
    // 不 CallJITSpec 重入 trace;直接 return 让 crescent 接管
    return ExitNormal  // dispatcher 上层进 crescent.executeFrom(exitPC)
}
```

物理上与 P4 的 `ExitOSR` 同源(都是「退回 crescent」),只是 P4 的 exit 序列是空(栈槽即真相),
fullmoon 的 exit 序列非空(snapshot + unsink)。

---

## 6. 代码缓存管理

### 6.1 trace 段 lifecycle

**建议**:每条 fullmoon trace 是一个独立 mmap 段,与 P4 per-Proto 段并列:

```
Program 生命期内的 mmap 段类型:
  - P3 wazero memory(P3 build) —— arena backing,承 P3 03
  - P4 per-Proto 段 —— per-Proto 一段,承 P4 05 §2.1.2
  - fullmoon per-trace 段 —— per-trace 一段,承本节
```

lifecycle 五步(与 [P4 05 §2.1.1](../p4-method-jit/05-system-pipeline.md) 同款,只是内容不同):

1. 编译:trace IR 优化 → regalloc → emit → 段字节数已知;
2. mmap RW → 写入机器码 + snapshot 表 + exit stubs;
3. mprotect RX(W^X 翻面);
4. arm64 上 icache flush;
5. anchor 表 `Live` + code page addr 记入锚点。

### 6.2 代码缓存预算与 eviction

**建议(待 PT7 校准)**:

- **per-State trace 数量上限**:建议起步 256(LuaJIT 默认 1000,望舒紧点起步再放宽);
- **per-State trace 总字节上限**:建议 8 MiB(P4 段已存在,不重复算);
- **eviction 触发**:达到上限 → 找最少命中的 trace(LRU 或热度倒序)→ evict;
- **evict 机制**:锚点侧表状态置 `Evicted` + `trace = nil`;下次触达该 anchor 时,查表看到 nil 视同「无
  trace」按 §3.1 走原 crescent 路径。**不需要 patch trace 段** —— trace 段的进入路径是 anchor 表查表,
  查到 nil 就是无 trace,无需修改机器码;
- **实际 munmap**:evict 后不立即 munmap(承 [P4 05 §2.1.3](../p4-method-jit/05-system-pipeline.md) 多
  State 并发 Dispose 安全缺口),用「引用计数 + 延迟 munmap」——每次进 trace 段前 refcount++,退出
  时 refcount--,refcount 归零且状态 `Evicted` 时才真 munmap;
- **State teardown**:Program close 时遍历所有 trace 段 munmap,释放所有锚点。

### 6.3 黑名单与 evict 交互

**关键区分**:

| 状态 | 语义 | 可否再次录制 |
|---|---|---|
| `Live` | trace 有效,正常用 | — |
| `Evicted` | trace 因资源被清 | **可以重新录制**(anchor 状态回 Recording)|
| `Blacklisted` | 录制过一次撞 NYI / 长度超限 / 反复 deopt(承 [08 §5](./08-testing-strategy.md) V20 对偶) | **永不再试**(承 00-overview §3 原则 4)|

`Blacklisted` 是原则 4 的 P5 兑现,防抖。若实测发现 Blacklisted 过度(大量 anchor 被拉黑损失覆盖面),
PT7 校准阶段可考虑「跨 State 生命期清 Blacklisted」类工具,但**默认不做**——原则 4 是宁漏不误。

---

## 7. 协程 & 线程规则

### 7.1 主线程独享升层(承 P3 07)

承 [P3 07 §2.1](../p3-wasm-tier/07-coroutine-thread-rule.md):**只有主线程的执行进入 gibbous;协程线程
上调用一律走 crescent**。P4 沿用,P5 沿用不变:

- fullmoon trace 只从主线程录制、只在主线程触达;
- 协程线程即便触达某有 anchor 的 pc,anchor 检查(§3.2)加同款 `if th == mainThread` 守卫,协程线程直接
  跳过 anchor,走原 crescent 路径;
- `considerPromotion` 与 trace 候选评估的入口(§2.3 候选 C 的 demote 触发点)同样加线程上下文守卫,承
  [P3 07 §2.4](../p3-wasm-tier/07-coroutine-thread-rule.md) 已开的接口口子。

### 7.2 trace 不跨 yield(SEED §6 开放问题)

SEED §6:**「coroutine 与 trace 的关系(LuaJIT 选择 trace 不跨 yield,望舒大概率同款,沿 P2 F2 清单)——立
项后定」**。

**推荐:trace 不跨 yield,与 LuaJIT 同款**。物理理由(承 [P3 07 §1.3](../p3-wasm-tier/07-coroutine-thread-rule.md)
「core Wasm 无 continuation」在 P5 的对偶):

- fullmoon trace 是一段线性机器码,yield 需要「保存当前执行位置 + 恢复到调用方」——trace 内部没有可保存
  的执行位置(所有 IR 值在寄存器/spill 槽,IR 状态不映射到 CallInfo);
- 若 trace 内部允许 yield,需要在每个可能 yield 的调用点埋类似 snapshot 的「yield 点物化配方」,复杂度
  与 deopt 同量级但收益极小(yield 罕见);
- 与 §7.1 一致:协程线程根本不进 trace,主线程不能 yield —— 「yield 撞 trace」类似「yield 撞 gibbous 帧」
  的三层互锁(P3 07 §2.3.4),**机制上构造性消解**。

**实现点(建议)**:trace 录制器遇到 `coroutine.yield` / `coroutine.resume` 直接标 NYI 弃 trace(与 P2
F2 保守 yield 判定同源)。

### 7.3 与 P2 F2 清单一致

承 [P2 03 §3 F2](../p2-bridge/03-compilability-analysis.md):任何直接或间接可能 yield 的函数在 P2 侧就
标 `CompNotCompilable` → 不进 P4 → 不进 P5(fullmoon 只作用于已升 P4 的 proto 的循环)。这是双保险:P5
的 NYI 清单只是补 P2 保守判定漏掉的边角。

---

## 8. GC 交互汇总

### 8.1 safepoint 三类(承 P4 05 §6 + P3 05)

**三类 safepoint 在 fullmoon 上的物理形态**(承 [P4 05 §6](../p4-method-jit/05-system-pipeline.md) 表):

| 类别 | fullmoon 物理形态 |
|---|---|
| **分配点** | trace 内所有分配一律经 exit-reason 通道调 helper(未 sink 的分配)或按 sink 配方在 deopt 时物化(sink 优化产物);**trace 段本体 emit 的机器码永不分配** |
| **层边界** | trampoline 进出即 safepoint,与 P4 同款 |
| **回边** | trace loop 的回边 emit `cmpb byte ptr [r15+preemptFlagOff], 0; jne exit_to_safepoint`,承 §4.2 |

### 8.2 GC 根:零新增

承 [P4 05 §1.1.4](../p4-method-jit/05-system-pipeline.md) 「GC 根扫描在 P4 完全等价于 P1/P3」——fullmoon
沿用:所有 Lua 值仍在 arena 值栈 + CallInfo,IR 中间值仅在寄存器/spill,GC 根扫描零新增。R1-R9 集
([P1 06 §5.1](../p1-interpreter/06-memory-gc.md))对 fullmoon 原样适用。

**关键约束**:trace 段 mmap 页对 Go GC 是 `[]byte`(不含指针),Go GC 完全不看内容;jitContext 是 Go 堆
对象含 uintptr 字段(arenaBase / vsBase / regDumpArea / snapshotTable 等),它们指向 Go 堆的 arena backing
或 mmap 段,Go GC 视 jitContext 为「含 uintptr 字段的普通 struct」——**uintptr 不被 Go GC 追踪**,与 P4
同款(承 [P4 05 §1.3.3](../p4-method-jit/05-system-pipeline.md) jitContext 是 Go 堆对象但内部 uintptr
不被追踪的语义)。

### 8.3 unsink 分配可能触发 GC(承 [06](./06-snapshot-deopt.md))

deopt 期间 `UnsinkAlloc` 调 `Arena.Alloc`,若越 GC 阈值触发 collect——collect 期间 fullmoon 已退出 trace
段进入 Go 世界,一切按 P4 已建纪律走:

- Go 侧 helper 内 arena grow / GC 由 Go runtime 与望舒自管 GC 协作([P1 06 §8.2](../p1-interpreter/06-memory-gc.md));
- 完成后 `RefreshJitCtxAddrs` 刷新 5 个地址字段;
- deopt 分支已确定不重入 trace(§5.2),`RefreshJitCtxAddrs` 只是为 crescent 接管后 executeFrom 时使用
  正确的 arenaBase。

### 8.4 gen / IC invalidation 对 trace 的影响

**建议**:trace 编译期烧入的表 IC 状态(如 `NodeHit` 的 gen 编号)在 guard 上重现——每次进 trace,某表
读快路径先查 `cmp [tableRef + genOff], <compiled_gen>; jne exit_deopt`。gen bump([P1 05 §6.5.1](../p1-interpreter/05-interpreter-loop.md))后 guard 立即失败退 crescent,与 P4 IC 投机同款语义,只是 exit 语义
是 fullmoon 特有的 snapshot 恢复。

**关键澄清**:trace 内联的 NodeHit 与 P4 直达槽 IC(承 [P4 03 §6](../p4-method-jit/03-speculation-ic.md))
同款依赖 gen 单调递增+不复用,producer 契约(承 [P1 05 §6.5.1](../p1-interpreter/05-interpreter-loop.md))
对 fullmoon 原样适用——**这是 trace 内联的 IC 投机的物理前提**,若 gen 不遵守 BumpGen 契约,fullmoon 撞
静默错果。

---

## 9. 开放问题

- **热度交接的具体实现**(§2.3 候选 C):demote 触发点、demote 频次、demote 次数上限——PT1 spike 校准;
- **anchor 表数据结构**(§3.2 B1/B2/B3 之选):PT2 实现期实测确定;
- **jitContext 新字段的精确 layout**(§4.3):PT4 寄存器分配器落地时定,与 [05](./05-register-allocation.md)
  协调;
- **`P4HostState` 扩展方法的精确签名**(§5.1 三个新方法):PT5 snapshot 机器实现时定,与
  [06](./06-snapshot-deopt.md) 对齐;
- **代码缓存预算**(§6.2 上限数字):PT7 端到端校准;
- **黑名单跨 State 清理策略**(§6.3):PT7 实测数据支持后再定,默认不清;
- **trace 段与 P4 段共享 preemptFlag 的多锚点频度**(§4.2 每回边一次 store barrier):PT7 profile 后决定
  是否需要「多重锚点合并回边检查」类优化,与 [P4 05 §6.3](../p4-method-jit/05-system-pipeline.md) 同款
  权衡在 P5 侧展开;
- **macOS arm64 MAP_JIT 与 P4 段的共享/独立**:P4 侧 PJ0 spike 若确定不需 MAP_JIT(承 [P4 05 §2.2.4](../p4-method-jit/05-system-pipeline.md)),fullmoon trace 段同款不需;若需,需要 spike 验证同一线程能否
  在 P4 段与 fullmoon 段间无缝切换(pthread_jit_write_protect_np per-region);
- **协程侧的 trace 覆盖率损失**:主线程独享升层(§7.1)让协程线程零覆盖,若首个宿主 workload 里协程
  跑热(反例场景),需回 P3 07 §4 路线 A 兜底——但这是 P3 决策,P5 只继承结论;
- **NYI 清单的 P5 扩展**(与 [02](./02-trace-recording.md) 协调):除 P2 F1-F7 之外,fullmoon 独有的 NYI
  形状(如「循环内嵌 pcall」「循环内嵌 setmetatable」等)清单——PT2 录制器落地时枚举。

---

相关:
[./00-overview.md](./00-overview.md)(§3 基建复用表 / §5 开放问题索引「热度交接」) ·
[00-overview.md](./00-overview.md) ·
[01-launch-judgment.md](./01-launch-judgment.md)(启动判定框架,P5 是否启动) ·
[02-trace-recording.md](./02-trace-recording.md)(录制器 + NYI 清单 + 阈值) ·
[04-optimization-passes.md](./04-optimization-passes.md)(pass 开关支持差分,承 [08 §4](./08-testing-strategy.md)) ·
[05-register-allocation.md](./05-register-allocation.md)(regalloc + 寄存器 dump 区规格) ·
[06-snapshot-deopt.md](./06-snapshot-deopt.md)(snapshot 表 + unsink + ExitDeopt 语义) ·
[08-testing-strategy.md](./08-testing-strategy.md)(测试策略——本文 §8 联合验的入口) ·
[../p4-method-jit/05-system-pipeline.md](../p4-method-jit/05-system-pipeline.md)(P4 系统管线,本文物理层
基线) ·
[../p4-method-jit/06-backends.md](../p4-method-jit/06-backends.md)(P4 双后端寄存器约定) ·
[../p2-bridge/00-overview.md](../p2-bridge/00-overview.md)(P2 决策加工厂) ·
[../p2-bridge/01-profiling.md](../p2-bridge/01-profiling.md)(回边 + 入口采样) ·
[../p2-bridge/04-try-compile-fallback.md](../p2-bridge/04-try-compile-fallback.md)(TierState 单向 + 吸收) ·
[../p3-wasm-tier/07-coroutine-thread-rule.md](../p3-wasm-tier/07-coroutine-thread-rule.md)(主线程独享升层) ·
[../p1-interpreter/06-memory-gc.md](../p1-interpreter/06-memory-gc.md)(GC 根 / preemptFlag / safepoint) ·
[../p1-interpreter/05-interpreter-loop.md](../p1-interpreter/05-interpreter-loop.md)(CallInfo / reloadFrame) ·
[../../../llmdoc/must/design-premises.md](../../../llmdoc/must/design-premises.md)(原则 4 走 fallback 不
做完备性——P5 版本兑现于 anchor Blacklisted)
