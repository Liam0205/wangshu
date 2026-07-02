# P5 §2:trace 录制器——解释器内嵌录制模式、起点选择、per-opcode 语义、NYI 与黑名单

> 状态:**未立项图纸**(P5 尚未立项,本文是启动检查点 [01-launch-judgment](./01-launch-judgment.md) 通过后可以逐步照做的施工设计,不代表任何已实现代码)。
>
> 对应 Go 包:`internal/fullmoon/trace`(录制器主体挂在 `recorder` 子模块,与解释器 `internal/crescent` 通过函数指针、build tag 或运行期检查连接;宿主关系详见 §7 数据结构)。
>
> 上游依据:
> [./00-overview.md](./00-overview.md)(§2 流水线图 ① / §3 复用基建表 / §5 风险与开放问题索引——本文的每一条决策都在这里展开,不回改上层裁决)、
> [../../../llmdoc/must/design-premises](../../../llmdoc/must/design-premises.md)(前提三原则 4「NYI 走下层不做完备性」——本文 §5 黑名单机制的依据;前提二的四项税——本文 §6 录制开销预算的根源)。
>
> P1 依赖面(录制器的宿主):
> [../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md)(§1 CallInfo + Frame 布局、§1.2 word2 bit50 callStatus_gibbous、§1.3 reloadFrame、§2 dispatch 策略 = 大 switch on opcode、§6 IC 命中路径、§7 CALL/RETURN reentry 模型——本文 §1 录制模式挂钩的物理位置)、
> [../p1-interpreter/02-bytecode-isa](../p1-interpreter/02-bytecode-isa.md)(§4 38 opcode 语义表 + §7 IC slot——本文 §3 逐 opcode 录制表的输入 ISA)、
> [../p1-interpreter/01-value-object-model](../p1-interpreter/01-value-object-model.md)(§3 NaN-box tag + §3.2 单比较判 number——录制观察类型时读的是 tag)。
>
> P2 依赖面(热度信号的复用):
> [../p2-bridge/01-profiling](../p2-bridge/01-profiling.md)(§1 back edge 加入口采样点、§2 ProfileData 字段、§5 阈值——本文 §2 独立的 P5 阈值叠加在同一套采样基础设施上)、
> [../p2-bridge/02-ic-feedback](../p2-bridge/02-ic-feedback.md)(§4 PointFeedback shape/confidence——录制期作为 hint 而不是强判据,详见 §3)、
> [../p2-bridge/03-compilability-analysis](../p2-bridge/03-compilability-analysis.md)(F1-F7 检查点——录制期沿用同一套 NYI 谱系,遵循原则 4)。
>
> P4 对位(deopt 语义的复用):
> [../p4-method-jit/03-speculation-ic](../p4-method-jit/03-speculation-ic.md)(§3 guard 显式比较硬约束——P5 guard 直接沿用)、
> [../p4-method-jit/04-osr-deopt](../p4-method-jit/04-osr-deopt.md)(§0.2 status 链与 OSR exit 的分工——P5 deopt 沿用同一套协议,但恢复目标是 crescent,而不是 P4 的当前帧栈槽)。
>
> 下游协作(同一子目录):
> [./03-ir-design](./03-ir-design.md)(录制的产物是本文 §3 表格右列的 IR ins——IR 形式由 03 定义)、
> [./06-snapshot-deopt](./06-snapshot-deopt.md)(录制期每个 guard 处采一份 snapshot——本文 §7 给出 snapshot 数据结构的骨架,具体压缩协议在 06)、
> [./07-system-integration](./07-system-integration.md)(P2/P3/P4 与 fullmoon 之间三层升降协议——本文 §2 起点选择与升降交接的直接消费方)。

---

## 0. 定位:trace 录制器是「解释器 + 同时记录」的两位一体

### 0.1 一句话

trace 录制器是**运行在 crescent 解释器内的一个副产物层**:解释器该跑什么还跑什么(正常取值、正常 dispatch、正常调 IC 快路径),同时在每条指令执行前后**记录 IR、观察类型、记下选中的分支、跟进被调函数**;从热点 back edge 开始录,回到同一 pc 就闭环成 loop trace;录制中断就丢掉 IR,退回纯解释。

一句话关键点:**「录制」不是「再解释一遍」,而是「在正在解释的同时把发生了什么写下来」**。录制器不改语义、不改控制流、不改栈状态——它只是把解释器已经在做的事情**观察并转写成 IR**,前提是这条正在跑的路径未来可能变成一条 trace。这跟 P4 模板编译期的「离线翻译」有本质区别:P4 编译期看的是 Proto 的静态 CFG,P5 录制期看的是运行期实际发生的动态直线。

### 0.2 录制器与其他层的位置

```
                       P2 决策机(已交付)
                            │ 热点 back edge 计数超过 P4 阈值(HotBackEdge=1000)
                            ▼
                       gibbous(P4,method JIT,已交付)
                            │ 若此 Proto 已经在 gibbous 中运行,back edge 计数继续累加
                            │ 超过 P5 阈值(TraceHotBackEdge,§2.2)
                            ▼
                    ┌───────────────────────┐
                    │ tier 交接:降回 crescent │  ← 详见 [07-system-integration §3]
                    │ 打开 recorder 副产物层  │
                    └───────────────────────┘
                            │
                       crescent 解释执行 + trace 录制器 同时记录 IR
                            │
                            ├─ back edge 跳回起点 pc → loop trace 闭合(§4)
                            ├─ 太长 / NYI / 异常 → 丢掉 trace + 加黑名单(§5)
                            └─ 走出 root proto → linear trace(§4)
                            │
                            ▼
                       IR (§3) → 03-ir-design 的双数组
                            │
                            ▼
                       [04-optimization-passes] → [05-register-allocation] → [06-snapshot-deopt] → fullmoon 码
```

**录制期由 crescent 承担宿主角色**:即便被录的 Proto 之前已经升到 gibbous(P4 method JIT),录制期也必须**降层回 crescent**——不然 IC 命中路径、类型判定、CALL 帧动态都被 P4 编译码的机器码盖住了,录制器看不见语义。tier 交接的具体协议在 [07-system-integration §3](./07-system-integration.md);本文 §2.4 只承诺「录制期的宿主永远是 crescent」这一硬约束。

### 0.3 章节路标

§1 录制器的物理形式(挂钩位置:hook 层还是 build-tag 副本;推荐 hook 层加编译期零开销守卫)→ §2 起点选择与阈值 → §3 逐 opcode 录制语义表(38 opcode 全表)→ §4 trace 闭合与结束条件 → §5 abort 与黑名单机制 → §6 录制开销预算 → §7 录制器数据结构骨架 → §8 开放问题。

---

## 1. 录制器的物理形式:副本还是 hook 层

### 1.1 两个备选方案

录制器要在「解释器每条指令执行前后能看到操作数、结果、控制流选择、类型 tag」的位置观察数据。物理形式有两个备选方案:

**备选 A——复制一份解释器主循环作为「录制版」**。当录制开始时 crescent 切进 `executeLoopRecording(th, entryDepth)`,与 [`executeLoop`](../p1-interpreter/05-interpreter-loop.md#executeLoop) 逻辑一致,但每个 case 分支旁边插入 IR 发射加类型观察。录制结束再切回普通 `executeLoop`。

- 优点:普通 `executeLoop` 保持零开销(P1「dispatch 每指令毫秒级敏感」的硬约束不受影响,依据 05 §2 的决策)。
- 缺点:两份实现必须保持逐 opcode 同构——任何 crescent bugfix 或语义调整都要同步改两份,反而成为 [../p1-interpreter/12-testing-difftest](../p1-interpreter/12-testing-difftest.md) 差分套的额外负担;违反 [../roadmap](../roadmap.md) §5 原则 1「解释器永不退役,唯一 oracle」的精神(存在第二个「非常像解释器」的东西,不同步就是语义分歧的种子)。

**备选 B——单份解释器,每指令顶端加一条 recorder 检查**。`executeLoop` 顶部加一句:

```go
for {
    ...取指...
    if recorder != nil { recorder.observe(th, ci, i) }  // <-- 新增,一次 nil 判断
    ...dispatch...
    if recorder != nil { recorder.commit(th, ci, i) }   // <-- 新增,可选后置观察
}
```

- 优点:一份实现,零重复;语义永远与解释器同步;差分套零改动。
- 缺点:普通(非录制)执行付出「一次 nil 判断加一次分支预测正确的 jmp」的代价——每指令大约 1 cycle,如果解释器每指令目标是 5-10 cycle,占比就明显了。

### 1.2 定稿:选备选 B 加编译期零开销守卫

选 B,但用 **build tag `p5trace`** 把 `recorder` 相关引用整体消掉,让**没编 P5 的 build** 里 `if recorder != nil` 整条被 dead-code 消除:

```go
//go:build p5trace
package crescent
// executeLoop 里的 recorder != nil 检查、observe 调用、recorder 字段访问,
// 仅在此 tag 下编入。

//go:build !p5trace
package crescent
// 提供空 stub:recorder 字段类型为空 struct{},observe/commit 是空方法。
// Go 编译器把 if false 分支消掉,同分支的 recorder.observe 也随之死代码消除。
```

具体做法参照 P2 `vm.profileEnabled` 的编译期切换手法([../p2-bridge/00-overview](../p2-bridge/00-overview.md) §1.3「接口稳定,占位不改公共 API」):`recorder` 字段挂在 `State` 上,类型在两个 tag 下不同——`p5trace` 下是 `*traceRecorder`,`!p5trace` 下是空 struct{}。`observe` 方法在 `!p5trace` 下方法体为空,编译器 inline 加 DCE 之后主循环里没有任何残留开销。

**为什么不选 A**:主要是 P1「唯一 oracle」的纪律。差分套断言 P4/P5 输出**逐字节等于 crescent 输出**([../p1-interpreter/12-testing-difftest](../p1-interpreter/12-testing-difftest.md) §3.8),如果存在一份「录制版 crescent」,它的输出就是第三方 oracle,还得再加一档差分「recording crescent vs normal crescent」——**扩大而不是减少投机面**。B 用 build tag 把录制路径与主路径物理分开,普通 build 里根本没有第二份实现。

### 1.3 recorder 挂在哪一层

**挂在 State 上,不挂在 thread 上**。原因:录制是 process 级别的独占资源(§6 录制并发只允许一份),挂在 thread 级别会漏掉「一个 thread 触发录制,另一个 thread 想触发时怎么仲裁」——挂在 State 上直接由 State 级别的互斥仲裁。

对协程的处理(沿用 P3/P4「协程不升层」的一致纪律,见 [../p3-wasm-tier/07-coroutine-thread-rule](../p3-wasm-tier/07-coroutine-thread-rule.md)):**录制只在主 thread 上做**;`th != mainTh` 时 recorder observe 直接短路返回。这跟 P4 `onMain` 判定同构([../p2-bridge/01-profiling](../p2-bridge/01-profiling.md) §6);跨 yield 的 trace 不在 P5 v1 目标里(见各章末尾开放问题第 5 条:LuaJIT 选择 trace 不跨 yield,望舒采用一样的做法)。

### 1.4 与 P4 的共存

被录的 Proto 之前可能已经升到 P4(gibbous method JIT)——也就是 `p4SpecState[proto]` 为 `P4Speculative`。录制期必须**回退到 crescent 跑这个 Proto**,协议如下:

1. State 触发录制起点决策时,如果 root proto 当前挂在 P4,通过 [`p4SpecState[proto].requestForRecording()`](../p4-method-jit/04-osr-deopt.md) 请求「借下来给 P5 录制」,该 Proto 的 P4 状态临时降到「录制期用 crescent」;录制成功并且编成 fullmoon 之后,P4 gibbous 码继续挂着但不再被走(fullmoon 优先)。录制失败并进入黑名单时,P4 恢复接管,与录制无关的其他 Proto 不受影响。
2. 录制中被调用进入的其他 Proto(内联进 trace)——如果被调 Proto 也在 P4,同样降层;如果不在 P4 那就本来就在 crescent,无操作。
3. P4 与 P5 最终的共存形式是「同一 Proto 三个 tier 副本,优先级 fullmoon > gibbous > crescent,fullmoon 进黑名单则 gibbous 顶上」——详见 [07-system-integration §4](./07-system-integration.md)。

---

## 2. 起点选择、阈值、tier 交接

### 2.1 三类起点(按 LuaJIT 经验,重要性递减)

沿用 LuaJIT 的起点选择(00-overview §2「录制起点选择沿用 LuaJIT 经验」):

| 起点 | 触发信号 | 起点 pc | 收益类别(种子 §1.2) |
|---|---|---|---|
| **热循环 back edge(loop trace)** | `FORLOOP` 成功回跳,或循环体内向后 `JMP` 超过阈值 | back edge 目标 pc(循环体入口) | 主要收益:跨调用的热循环、循环携带的冗余、分配密集的循环 |
| **热 side exit(side trace)** | 某条已编译 fullmoon trace 的某个 guard 反复失败 | 该 guard 的 exit pc | 补丁:多态调用点的稳定子集(§4 末尾提到 stitching) |
| **函数入口(up-recursion trace)** | 某 Proto 从入口被反复调用超过阈值,并且没有被内联到其他 trace 里 | Proto pc=0 | 递归形式(尾递归以外的深递归),v1 可以推后 |

**loop trace 是 v1 主战场**,side trace 加 up-recursion 属于 P5 v1 内部的第二、第三个检查点(06-snapshot-deopt §4 末尾)。

### 2.2 阈值:TraceHotBackEdgeThreshold

复用 P2 的 back edge 采样基建([../p2-bridge/01-profiling](../p2-bridge/01-profiling.md) §1.2:`onBackEdge(proto, targetPC)` 已经存在),**只是把「记账标准」加一档**。

三档阈值层叠:

```
P4 采样(已交付):
  proto.pd.BackEdgeCount++;
  if BackEdgeCount == HotBackEdgeThreshold (=1000) {
      considerPromotion(proto, ...) → 升 gibbous
  }
P5 追加档(未立项):
  if BackEdgeCount == TraceHotBackEdgeThreshold {
      considerTraceRecording(proto, targetPC) → 尝试开录
  }
```

`TraceHotBackEdgeThreshold` 具体数值 **TBD,等 PT0 实测校准**。参考锚点:

- **LuaJIT 默认 `hotloop=56`**——但那是叠在 LuaJIT 本身极快的解释器上,望舒 crescent 加 gibbous 已经消化了大部分冷路径和温路径,阈值应该更高。
- **应该显著大于 `HotBackEdgeThreshold=1000`**(不然每个升到 P4 的 loop 都会被 P5 顺手录一遍,录制开销见 §6,会反噬 gibbous 的收益)。
- 起始猜测:`TraceHotBackEdgeThreshold = 10 * HotBackEdgeThreshold = 10000`,等 PT0 spike 期间用真实宿主脚本校准(依据 [01-launch-judgment §1.3](./01-launch-judgment.md) 立项判定的标准预登记)。

同样为 `TraceHotEntryThreshold`(up-recursion 起点)预留一档,起始锚 `10 * HotEntryThreshold`(=2000);LuaJIT 默认 `hotcall=200` 可以作为反向 sanity。

### 2.3 起点仲裁

多个候选起点在同一时刻越过阈值时的处理:

```
considerTraceRecording(proto, targetPC):
  if state.recorder != nil { return }           // 已经在录制,让位(§6 单会话)
  if isBlacklisted(proto, targetPC) { return }  // §5 黑名单
  if state.recorderCooldown > now { return }    // 冷却窗口,防止连开
  if th != state.mainTh { return }              // 只在主 thread 录(§1.3)
  state.recorder = newRecorder(proto, targetPC, RecStartBackEdge)
```

冷却窗口(比如 100ms,或者 N 次失败后指数退避)防止「一失败就再开、录一半又失败」的死循环,与 §5 黑名单配合形成两级防护。

### 2.4 tier 交接细节

如果 root proto 当前挂在 P4:

```
considerTraceRecording:
  if p4SpecState[proto] == P4Speculative:
      // 请 P4 把这个 Proto 借给 P5 录制
      p4SpecState[proto] = P4RecordingLoan   // 新增子状态,P4 端识别
      state.recorder.p4Owned = true
  // 无论 P4 是否在,录制期都走 crescent
  state.tierOverride[proto] = TierCrescent
```

`P4RecordingLoan` 是 [../p4-method-jit/04-osr-deopt](../p4-method-jit/04-osr-deopt.md) §5.3 `p4SpecState` 三态之外的一个新子状态,属于**P4 内部**,P2 三态不感知(沿用方案 A 硬纪律)。**tier 交接协议的完整规约由 [07-system-integration §3](./07-system-integration.md) 拥有**,本文只承诺「录制期宿主是 crescent」这一硬约束,不重复展开状态图。

---

## 3. 逐 opcode 录制语义表(P1 38 opcode 全表)

本节是本文档最重的技术表:38 opcode 分别对应发射什么 IR、记什么 guard、什么时候 abort。IR op 名与语义在 [03-ir-design](./03-ir-design.md) §4 定义,本表引用其中的 IR op 记号(如 `SLOAD`/`ALOAD`/`ADD`/`GUARD_NUM`/`LOOP`),尚未在 03 里给出具体位编码。

命名约定:

- `SLOAD r ⇒ v` 表示「以类型 tag 为 T 的 slot load 从 stack slot `r` 读到 IR value `v`,同时插入 `GUARD_TYPE t`」;
- `KNUM k ⇒ v` 表示常量号 `k` 加载为 IR 值;
- `ASTORE r, v` 表示写回 stack slot `r`;
- `GUARD_TABLESHAPE tbl, gen` 表示表形状 gen guard;
- `EXIT_pc` 表示该 guard 关联 snapshot 的 exit pc(默认 = 当前 pc)。

### 3.1 常量与移动(4 op)

| Op | 录制发射 | Guard | Abort 条件 | 备注 |
|---|---|---|---|---|
| `MOVE A B` | `v := SLOAD B (tag observed)`;`ASTORE A, v` | `GUARD_TYPE B, tag` | — | 类型 tag 观察到什么就 guard 什么 |
| `LOADK A Bx` | `v := KNUM/KGC Bx`;`ASTORE A, v` | — | — | 常量类型静态可知 |
| `LOADBOOL A B C` | `v := KPRI bool(B)`;`ASTORE A, v`;如果 `C≠0` 记 pc 增量(线性跳) | — | — | C 语义在录制期展开成线性 pc 跳 |
| `LOADNIL A B` | 对 r∈[A,B] 逐个 `ASTORE r, KPRI nil` | — | — | 展开成 B-A+1 条 ASTORE |

### 3.2 upvalue 读写(2 op)

| Op | 录制发射 | Guard | Abort 条件 | 备注 |
|---|---|---|---|---|
| `GETUPVAL A B` | `v := ULOAD B`(带类型 guard);`ASTORE A, v` | `GUARD_TYPE uv, tag` | closure 逃出 trace(见 §3.10 CLOSURE) | |
| `SETUPVAL A B` | `ULOAD-side 无`;`USTORE B, SLOAD A` | — | closure 是被 sink 的 v2 情形则 abort v1 | Open upvalue 引用值栈——同一 trace 内可以 CSE(详见 [04-optimization-passes §3](./04-optimization-passes.md)) |

### 3.3 全局读写(2 op)

| Op | 录制发射 | Guard | Abort | 备注 |
|---|---|---|---|---|
| `GETGLOBAL A Bx` | 如果 IC kind=NodeHit:`GUARD_TABLESHAPE globals, gen`;`v := HLOAD_DIRECT globals, index`;`ASTORE A, v` 加 `GUARD_TYPE v, tag` | globals gen 加 value type | globals mega(kind=4),或者首次未命中 | 复用 P1 IC 直达槽 index |
| `SETGLOBAL A Bx` | 同上镜像:`GUARD_TABLESHAPE globals, gen`;`HSTORE_DIRECT globals, index, SLOAD A` | globals gen | 同上 | 需要写屏障 IR(见 03 §4 GCSTEP) |

### 3.4 表读写(3 op)

| Op | 录制发射 | Guard | Abort | 备注 |
|---|---|---|---|---|
| `GETTABLE A B C` | ① `tbl := SLOAD B (tag=table)`;② 依 IC kind 分流:<br>ArrayHit → `GUARD_TABLESHAPE tbl, gen` 加 `v := ALOAD tbl, index` 加 `GUARD_TYPE v, tag`<br>NodeHit → `GUARD_TABLESHAPE tbl, gen` 加 `v := HLOAD tbl, index` 加 `GUARD_TYPE v, tag`<br>MonoMeta → abort(需要 `__index` 元方法调用,v1 不做 metamethod 内联) | 表 shape gen 加结果 type | IC=mega 或首访 | 键为 K 时键身份也进 guard(常量键在 IR 里作为立即数写死) |
| `SETTABLE A B C` | ① `tbl := SLOAD A (tag=table)`;② IC 分流同 GETTABLE,写侧改 `ASTORE`/`HSTORE`;③ 如果可能 rehash(新键)→ abort;④ `GCSTEP` 写屏障 IR | 表 shape gen | 写触发 rehash / mega / `__newindex` | 写侧对 CSE 的 FENCE 效果详见 [04 §3](./04-optimization-passes.md) |
| `SELF A B C` | 等价于 GETTABLE 到 `R(A)` 加 `MOVE B → R(A+1)`;录制期展开成两条 IR ins:`ASTORE A+1, SLOAD B` 加 GETTABLE 序列 | GETTABLE 全部 | GETTABLE 全部 | 方法查找命中率高,是 up-recursion trace 的重要入口 |

### 3.5 表构造(2 op)

| Op | 录制发射 | Guard | Abort | 备注 |
|---|---|---|---|---|
| `NEWTABLE A B C` | `tbl := TNEW arrayCap(B), hashCap(C)`;`ASTORE A, tbl` | — | trace 长度超限或 v2 sink 前 abort | v2 加入 sink 才有优化价值,v1 老实分配 |
| `SETLIST A B C` | 展开成 `HSTORE tbl, index_i, SLOAD A+i` × B(常量 index) | — | `B=0` 到 top → abort(涉及多值 top,见 §3.9) | C 是 block 号,静态常量 |

### 3.6 算术与 CONCAT(9 op)

| Op | 录制发射 | Guard | Abort | 备注 |
|---|---|---|---|---|
| `ADD A B C` | 如果算术 IC numHits/(numHits+metaHits) ≥ 0.99:<br>`b := SLOAD B (num)`;`c := SLOAD C (num)`;`r := ADD b, c`;`ASTORE A, r`<br>否则 abort | `GUARD_NUM B` 加 `GUARD_NUM C` | IC 不稳(metaHits 高),或者首访 | 沿用 P4 03 §2.2 的 f64 快路径,写法一样 |
| `SUB/MUL/DIV/MOD/POW` | 同 ADD 镜像,IR op 换成 SUB/MUL/DIV/MOD/POW | 同 ADD | 同 ADD | POW 慢路径较大,能开就开 |
| `UNM A B` | `b := SLOAD B (num)`;`r := NEG b`;`ASTORE A, r` | `GUARD_NUM B` | metaHits 高 | |
| `NOT A B` | `b := SLOAD B`;`r := NOT b`(基于 truthy 语义);`ASTORE A, r` | — | — | 没有 metamethod,不需要 guard number |
| `LEN A B` | 依 tag 分流:string → `LEN_STR`,table → `LEN_TAB`(border,无 `__len` 是 P1 5.1 约束) | `GUARD_TYPE B, tag` | userdata 有 `__len` → abort v1 | |
| `CONCAT A B C` | 逐对折叠 `r := CONCAT b_i, b_{i+1}` × (C-B) | 每对 `GUARD_TYPE (num|str)` | 遇到 `__concat` metamethod → abort | 生成中间字符串,写屏障 |

### 3.7 比较(3 op)

比较是成组处理的:字节码保证 `EQ/LT/LE` 后面必跟 `JMP`([02-bytecode-isa §9](../p1-interpreter/02-bytecode-isa.md) 不变式 3)。录制期把两条一起消耗:

| Op 对 | 录制发射 | Guard | Abort | 备注 |
|---|---|---|---|---|
| `EQ A B C` + `JMP` | `b := SLOAD B`;`c := SLOAD C`;发射 `GUARD_EQ_DIR b, c, expected_dir`,expected_dir = 实际观察的方向(taken/not-taken) | `GUARD_EQ_DIR` | — | 这个 guard **本身就是 exit**:走错方向就 deopt 到 JMP 目标或后续 pc |
| `LT A B C` + `JMP` | 如果两边都 num 或都 string(P1 快路径):发射 `GUARD_LT_DIR` | `GUARD_LT_DIR` | 混合类型,或者有 `__lt` metamethod → abort | 依据 05 §4.4 快路径 |
| `LE A B C` + `JMP` | 同 LT | 同 LT | 同 LT | |

**核心**:比较 IR **不产生 boolean 值**,而是直接产生**方向 guard**。走错方向就 exit——这跟 P4 的 `GUARD_NUM` 之后继续跑 f64.add 是**同一种 guard 哲学**,只是从「投机操作数类型」升级到「投机分支方向」。

### 3.8 控制流(3 op)

| Op | 录制期动作 | Guard | Abort | 备注 |
|---|---|---|---|---|
| `JMP sBx` | 修改录制 pc:pc += sBx + 1(该指令不产生 IR);如果跳回 startPC,则 **loop 闭合**(§4) | — | 跳出录制窗口 → 线性 trace 结束 | 无条件跳,如果单独出现(不是比较对的一部分)也可能是循环 back edge |
| `TEST A C` + `JMP` | 类似 §3.7 的比较对:`v := SLOAD A`;`GUARD_TRUTHY_DIR v, expected` | truthy guard | — | 用于 `and`/`or` 短路 |
| `TESTSET A B C` + `JMP` | `v := SLOAD B`;`GUARD_TRUTHY_DIR v, expected`;如果 expected 命中则 `ASTORE A, v` | 同 TEST | — | 与 TEST 同族 |

### 3.9 调用与返回(4 op)

**这是 P5 相对 P4 的核心增益:CALL 不停止 trace,而是跟进被调函数继续录**(种子 §1.2 第一类形式,内联可以消除调用开销)。

| Op | 录制期动作 | Guard | Abort | 备注 |
|---|---|---|---|---|
| `CALL A B C` | ① `callee := SLOAD A`;② `GUARD_CALLEE_ID callee, observed_closure_gcref`(方法 identity guard);③ 如果被调是 Lua closure:**push 一个逻辑 frame 到录制器 frameStack(仅 recorder 状态,不是 crescent CallInfo)**,并递归进入被调 Proto 继续录;如果被调是 host function:abort v1(host fn 不透明,见各章末尾开放问题);④ 如果尾调用继续深挖,达到 `MaxInlineDepth`(§7)则 abort | callee identity | host fn、内联深度超限、`B=0` 到 top(多值传参无法静态展开) | 帧 push 数据留给 06-snapshot-deopt |
| `TAILCALL A B` | 类似 CALL,但 record 复用父 frame(不 push 新 frame,pc 切到被调),避免帧栈无限增长 | 同 CALL | 同 CALL,加上 P4 已经用了的尾调用复用父帧一样的处理 | 与 05 §7.2 尾调用协议一致 |
| `RETURN A B` | 如果当前录制帧不是 root:pop 录制器 frame,pc 回到 caller 的 CALL 后一条;如果是 root:**线性 trace 结束**(§4)| — | `B=0` 到 top → abort(多值返回无法静态展开) | 返回值搬移展开成 ASTORE(常量 nresults) |
| — | — | — | — | — |

### 3.10 CLOSURE / CLOSE / VARARG(3 op)

| Op | 录制期动作 | Guard | Abort | 备注 |
|---|---|---|---|---|
| `CLOSURE A Bx` | v1:abort(闭包创建 = 分配 + upvalue 捕获,能否内联进 trace 依赖 `close` 语义加 escape 分析,v2 sink 时启用) | — | 恒 abort | 见各章末尾开放问题 |
| `CLOSE A` | v1:abort(如果 A 及以上有 open upvalue 被闭包捕获且逃出 trace,语义复杂) | — | 恒 abort v1 | v2 打开 |
| `VARARG A B` | **恒 abort**——vararg 展开涉及多值加静态未知的实参个数,沿用 P2 F1 的排除 | — | 恒 abort | 与 P4 F1 排除同源 |

### 3.11 数值 for(2 op)

| Op | 录制期动作 | Guard | Abort | 备注 |
|---|---|---|---|---|
| `FORPREP A sBx` | `init := SLOAD A (num)`;`limit := SLOAD A+1 (num)`;`step := SLOAD A+2 (num)`;`v := SUB init, step`;`ASTORE A, v`;pc += sBx + 1 → 跳到 FORLOOP | `GUARD_NUM × 3` | 三槽不全是 num | 依据 05 §10.1 FORPREP 语义 |
| `FORLOOP A sBx` | `v := ADD SLOAD A, SLOAD A+2`;`ASTORE A, v`;发射 `GUARD_LOOP_CONT v, SLOAD A+1, step_sign`(方向 guard,取决于观察到的 taken/not-taken);taken 则 `ASTORE A+3, v`,pc += sBx + 1 → 如果目标 = startPC 则 loop 闭合 | 方向 guard | step 不是常量或方向不稳,则该 guard 走冷路径 fallback | FORLOOP 是本轮 loop trace 天然的 back edge 终结子 |

### 3.12 TFORLOOP(1 op)

| Op | 录制期动作 | Guard | Abort | 备注 |
|---|---|---|---|---|
| `TFORLOOP A C` | 与 CALL 同结构:调用 `R(A)(R(A+1),R(A+2))` 拿 C 个返回值;能否内联依赖迭代器是否是 pure Lua closure 加 F2-b 白名单 | callee identity | 迭代器是 host fn(pairs/ipairs 是 host)→ abort v1 | 开放问题:pairs/ipairs 如果移入 Lua 侧,或者加白名单,可以 v2 打开 |

### 3.13 全部 NYI 清单汇总

综合起来,P5 v1 NYI 清单:

- `VARARG`(恒 abort,依据 F1)
- `CLOSURE` / `CLOSE`(v1 abort,v2 sink 时打开)
- `TFORLOOP` 遇到 host 迭代器(pairs/ipairs)abort
- `CALL` / `TAILCALL` 到 host function abort v1
- `CALL` / `RETURN` `B=0` 或 `C=0`(多值到 top,静态不可展开)abort
- 元方法路径(任何 metamethod 触发)abort v1
- CONCAT 触发 `__concat` abort
- 内联深度 > `MaxInlineDepth` abort
- trace IR ins 数量 > `MaxTraceIns` abort

与 P2 [03-compilability-analysis](../p2-bridge/03-compilability-analysis.md) 的 F1-F7 是**同一血脉但更严**——F1-F7 判「整个 Proto 能不能升 gibbous」,P5 NYI 判「trace 录制期遇到就 abort 这一条 trace」;粒度从 Proto 级降到指令级,原则 4「不做完备性」仍然成立(依据 [00-overview §3](./00-overview.md) 表格)。

---

## 4. trace 闭合、结束、树生长

### 4.1 loop 闭合(v1 的主要形式)

从 startPC 开始录,遇到跳回 startPC 的 JMP / FORLOOP taken → **loop 闭合**:

```
recorder.observe(op JMP sBx):
  target := pc + sBx + 1
  if target == recorder.startPC && recorder.frameStack.depth == 0:
      → 闭合 loop trace
      → 发射 LOOP marker IR ins(为 [04-optimization-passes §6] loop peeling 提供 anchor)
      → recorder → optimizing pipeline
```

**要求 frameStack.depth == 0**(也就是录制期已经从任何内联的被调函数 return 回 root frame),否则闭合是错位的——相当于在被调函数体内跳回 root 的 startPC,语义不成立。

### 4.2 线性 trace 结束(次要形式)

`RETURN` 到达 root frame 时,走出了整个录制起点函数,产生一条**没有 back edge** 的线性 trace。这种 trace 也可以编,但只在下面两种情况下用得上:

- side trace(从热 side exit 继续录,终止于回到主 trace 或走出)
- up-recursion trace(函数入口起点)

单纯 loop trace 起点走成线性一般是失误(startPC 是 back edge 目标,不应该能走到 return 而不 back edge),记 `TraceEndedLinearlyFromLoopStart` 作为 abort 类别送到 §5 黑名单。

### 4.3 trace 链接与树生长(v2/v3)

如果一条已经编好的 trace 的某个 guard 反复失败,从该 exit 起录 side trace,尾部如果能跳回主 trace 某个位置,则 side trace 结束于「链接回主 trace」这一条 IR ins。多次生长就形成 trace 树(依据 [00-overview §2 流水线图 ⑤](./00-overview.md))。

v1 里 side trace **不实现**,所有 exit 都返回 crescent。v2 打开 side trace(依据 [implementation-progress.md](./implementation-progress.md) 的 PT 阶段划分,由另一个 agent 拥有)。

### 4.4 长度与深度上限

对齐 LuaJIT 的常量,作为 PT2 后期实测校准的锚点:

| 常量 | 提议初值 | LuaJIT 参考 |
|---|---|---|
| `MaxTraceIns` | 4000 | LuaJIT `MAXIRINS` 是 65535,但实际 loop 一般远低于此;望舒起始严一档 |
| `MaxInlineDepth` | 8 | LuaJIT `MAXCALLDEPTH` 是 60,望舒起始严 |
| `MaxTraceExits` | 16 | 每条 trace 平均 guard 数,过多说明投机面太散 |
| `MaxSnapshots` | 32 | 每条 trace snapshot 上限(与 06-snapshot-deopt 耦合,详见 §7 数据结构) |

超限一律 abort,记 `TraceTooLong` / `InlineTooDeep` 等 abort 类别。

---

## 5. abort 与黑名单

### 5.1 abort 分类

```go
type AbortReason uint8
const (
    AbortUnspecified AbortReason = iota
    AbortNYIOpcode             // VARARG/CLOSURE/CLOSE 等 §3.10/§3.13
    AbortMetamethod            // 遇到元方法
    AbortHostCall              // CALL 到 host function
    AbortMultiValueTop         // B=0 / C=0 多值到 top
    AbortTypeMismatch          // guard 观察到的与已有 IR 类型不一致
    AbortTraceTooLong          // IR ins > MaxTraceIns
    AbortInlineTooDeep         // frame depth > MaxInlineDepth
    AbortTooManyExits          // guard 数 > MaxTraceExits
    AbortLinearFromLoopStart   // §4.2 loop 起点线性走出
    AbortRehash                // SETTABLE 触发 rehash
    AbortRecursionBudget       // 递归录制预算耗尽(§6)
    AbortExternalRequest       // 上层显式取消(GC/协程/State 关闭)
)
```

### 5.2 黑名单结构

每个 Proto 内部按 startPC 索引一张失败表:

```go
type traceBlacklist struct {
    failures map[protoPCKey]*blacklistEntry
}
type protoPCKey struct { proto *bytecode.Proto; pc uint32 }
type blacklistEntry struct {
    failCount  uint32  // 累计失败次数
    lastReason AbortReason
    banned     bool    // 一旦置位,永远不再从此 (proto,pc) 起录
    cooldownUntil int64 // 未 ban 时的下次可尝试时间
}
```

策略:

1. 单次 abort:`failCount++`,`cooldownUntil = now + backoff(failCount)`(指数退避,起始 100ms,封顶 10s);
2. `failCount ≥ MaxTraceFailPerPC`(建议 3-5):`banned = true`,永远不再尝试;
3. `banned` 的 (proto, startPC) 上,`considerTraceRecording` 直接 return——那条 back edge 永远只由 crescent 加 gibbous 服务。

黑名单本身在 State 上,进程生命期。如果 Proto 被 GC(所有 closure 死了),对应黑名单条目也随之释放(用 weakref 或 GC finalize hook,依据 [../p1-interpreter/06-memory-gc](../p1-interpreter/06-memory-gc.md) 的对象生命期管理)。

### 5.3 与 P2 F1-F7 的关系

**P2 F1-F7 判「整个 Proto 能不能升 gibbous」,结果永久写进 `proto.CompReasons`**([../p2-bridge/03-compilability-analysis §3.6](../p2-bridge/03-compilability-analysis.md)):某个 Proto F1 触发就无论升 gibbous 还是升 fullmoon 都拒。P5 黑名单是 P2 检查点之下的第二层筛:F1-F7 通过的 Proto 里,某条具体 back edge pc 反复 abort,则那条 back edge 进黑名单。**没有回改 P2 状态**——P2 依然把整个 Proto 标为 `TierGibbous`(P4 method JIT 挂着运行),只是 P5 拒开这一条 trace。

---

## 6. 录制开销预算

**录制期比纯解释慢 10-100 倍是行业常识**(LuaJIT 类似)。控制手段:

- **全局单例**:任何时刻只有一份录制在跑(§2.3 `state.recorder != nil` 检查);
- **length hardcap**:`MaxTraceIns`(§4.4)兜底 pathological trace;
- **recursion budget**:record 内的递归深度不算 CALL 内联深度,而是「observe→emit 递归 IR」的调用栈上限;
- **allocation 预算**:每条 trace 一个固定大小的 IR buffer,不 grow——满了直接 abort(与 P4「不为热路径分配」纪律同源);
- **cooldown**:黑名单退避(§5.2),防止连开死循环。

**tier 阈值本身也是开销检查点**:`TraceHotBackEdgeThreshold` 越高,越少的 back edge 触发录制。§2.2 起始 10000 = 10 倍 P4 阈值,也就是每 10 个升 gibbous 的 loop 只有 1 个会尝试升 fullmoon。

---

## 7. 数据结构骨架

```go
// 挂在 State 上,build tag p5trace 下才实体化
type traceRecorder struct {
    // 起点
    startProto *bytecode.Proto
    startPC    uint32
    kind       RecStartKind  // BackEdge / SideExit / FunctionEntry

    // 正在录制的当前指令视野
    ir        []IRIns        // 主 IR buffer,cap = MaxTraceIns(§4.4),不 grow
    snapshots []Snapshot     // 每个 guard 一份,cap = MaxSnapshots
    frameStack []recFrame    // 内联被调函数的逻辑帧栈,cap = MaxInlineDepth+1

    // 类型观察聚合(可以用 P2 feedback 作为起点)
    typeHints map[slotKey]value.Tag  // slot ↔ 观察到的 tag

    // 常量池(IR 侧,依据 03-ir-design §6)
    kNum      []float64
    kGC       []value.GCRef

    // 借用 P4
    p4Owned   bool           // §1.4 从 P4 借来录制

    // 录制统计
    icount    uint32          // IR emit 计数
    exitCount uint32          // guard emit 计数
    startedAt int64           // 时间戳,用于超时保护
}

type recFrame struct {
    proto     *bytecode.Proto
    calleePC  uint32         // caller pc(CALL 那一条,用于 snapshot 里的 returnPC 重建)
    baseSlot  int32          // 该内联帧的 base slot(相对 root frame 起点的偏移)
    savedTop  int32          // 用于多值处理(实际 v1 已经 abort 多值,占位)
}

type slotKey struct { frameDepth int32; regIdx int32 }
```

**注**:`ir` / `snapshots` / `frameStack` 全部预分配,cap 不 grow——超限就 abort,严守 §6「不为热路径分配」的纪律。

recorder observe 接口:

```go
// executeLoop 顶部调用,记完就返回,不改 crescent 状态
func (r *traceRecorder) Observe(th *thread, ci *callInfo, i bytecode.Instruction) {
    if r == nil || r.exitCount >= MaxTraceExits { r.Abort(AbortTooManyExits); return }
    if r.icount >= MaxTraceIns { r.Abort(AbortTraceTooLong); return }
    op := bytecode.Op(i)
    // dispatch 到 §3 表对应的 recordXxx(r, th, ci, i)
}

func (r *traceRecorder) Abort(reason AbortReason) {
    r.state.blacklist.Record(r.startProto, r.startPC, reason)
    r.state.recorder = nil
    // 保留冷却窗口
}
```

Snapshot 结构由 [06-snapshot-deopt](./06-snapshot-deopt.md) 定稿;此处只用一个不透明的 `Snapshot` 类型占位。

---

## 8. 开放问题(记入 doc-gaps 等待 PT0/PT1 实测)

- **TraceHotBackEdgeThreshold 具体数值**——初值 10000 是猜的,PT0 用真实宿主脚本(依据 [01-launch-judgment §3](./01-launch-judgment.md) 判定标准)校准。
- **TFORLOOP 到 pairs/ipairs 的处理**——各章末尾开放问题已经列出;备选方案:(a) 保持 abort v1;(b) 特判 pairs/ipairs 展开成 Lua 侧同等 loop;(c) 加白名单 host 迭代器,以「纯、参数少」为准入条件。
- **CALL 到 host function**——v1 abort;如果首个宿主真实 hot trace 频繁触发,考虑允许 pure host fn(依据 [../p2-bridge/03-compilability-analysis](../p2-bridge/03-compilability-analysis.md) F2-b 白名单同源)作为 opaque 值消费者进入 trace。
- **跨 pcall 的 trace**——录制期遇到 pcall / xpcall abort 是保守形式;如果 pcall 内错误路径可以静态证明不触发 metamethod,可以考虑一并录进 trace 加 guard「没有 error」的形式,各章末尾开放问题已经列出。
- **recorder observe 单一 nil 判断的实测开销**——PT0 spike 阶段用 build tag 双跑基准,验证 !p5trace build 完全零开销,并且 p5trace build 但 recorder==nil 时的开销 < 5% 主循环成本。
- **CLOSURE / CLOSE 打开时机**——v2 escape 分析加 sink 之后是否值得启用,需要 PT7 实测决定;如果真实宿主 hot loop 高频创建闭包(P4 已经通过 F 检查点证明这种形式占比高),v2 优先级前置。
- **黑名单粒度**——目前是 (proto, startPC),是否需要 (proto, startPC, kind) 三元组以区分 loop 起点与 side exit 起点,PT2 后期决定。

---

相关:
[./00-overview.md](./00-overview.md)(§2 起点选择 / §3 复用基建 / §5 开放问题索引) ·
[./01-launch-judgment](./01-launch-judgment.md)(阈值标准预登记) ·
[./03-ir-design](./03-ir-design.md)(§3 每行 IR 记号的定义源) ·
[./04-optimization-passes](./04-optimization-passes.md)(§6 loop peeling 的输入 = §4.1 闭合 loop trace) ·
[./06-snapshot-deopt](./06-snapshot-deopt.md)(§7 Snapshot 结构加 guard 与 snapshot 的配合约定) ·
[./07-system-integration](./07-system-integration.md)(§3 tier 交接协议加 §4 三层共存) ·
[./08-testing-strategy](./08-testing-strategy.md)(录制期差分:crescent+record 与 crescent 输出逐字节比对) ·
[../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md)(录制器宿主) ·
[../p2-bridge/01-profiling](../p2-bridge/01-profiling.md)(back edge 计数复用) ·
[../p4-method-jit/03-speculation-ic](../p4-method-jit/03-speculation-ic.md)(guard 硬约束继承)
