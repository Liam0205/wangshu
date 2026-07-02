# P3 §07:协程与 P3 的边界——yield 不能穿越 gibbous 帧 + 线程级 tier 规则

> 状态:**设计阶段,详细设计已齐备**(规则定稿,P3 开工前置确认条目挂账,路线 A 兜底已论证)。
> 本文是 P3 与 coroutine 关系的**单一事实源**:yield 为何不能穿越 gibbous 帧的物理论证、线程级 tier 规则
> 定稿、对 P2 / P1 08 的回填请求、备选方案(P1 08 路线 A goroutine 化)的代价分析与启用条件。
>
> 上游契约:[../p1-interpreter/08-coroutines](../p1-interpreter/08-coroutines.md) §3.1-§3.6(路线 A/B 对比 +
> 路线 B 的 yield 信号冒泡机制 + §5 yield 不跨 C 边界)、[../p1-interpreter/08-coroutines](../p1-interpreter/08-coroutines.md)
> §3.6(路线 B 与 trace JIT 对齐的演进价值)、[../p2-bridge/04-try-compile-fallback](../p2-bridge/04-try-compile-fallback.md)
> §3(considerPromotion 入口契约,本文要求加线程上下文输入)、[04-trampoline](./04-trampoline.md) §4(错误经
> status 链单向冒泡可穿 gibbous 帧,与 yield 形成对照)、[../roadmap.md](../roadmap.md) §4(P3 阶段定义)、§5
> 原则 4(coroutine 是 fallback 形状)、§6(锁 Lua 5.1 不做 5.2 *k continuation)。
> 决策来源:[memory/decisions/2026-06-11-design-review-decisions.md](../../../llmdoc/memory/decisions/2026-06-11-design-review-decisions.md)
> 第 7 项「P3 协程不升层维持」、[memory/doc-gaps.md](../../../llmdoc/memory/doc-gaps.md) P3 开工前置确认条目。
>
> 下游衔接:[00-overview](./00-overview.md) §1(P2/P3/P4 边界表「协程升层」行)、§9 不变式清单第 9 条(协程
> 不升层),P4 继承本规则不引入新机制。
>
> **本文定位一句话**:**P3 与 coroutine 的边界,由「物理限制 + 工程决策」两层定死**——物理上 core Wasm 帧
> 无法挂起后复原,yield 信号无法穿越;工程上以「线程级 tier 规则」(只有主线程进 gibbous)使该物理限制
> 在 P3 阶段不会被触发。这是 P1 08 路线 B 的「yield 不跨 C 边界」物理限制在 Wasm 层的延伸。

对应 Go 包:`internal/gibbous/wasm`(线程级 tier 规则的检查点 = doCall gibbous 分支);影响 `internal/bridge`
(considerPromotion 入口加线程上下文输入,对 P2 04 的回填)。

---

## 0. 定位:P3 与 coroutine 的边界——物理限制 + 工程决策

### 0.1 一句话边界

**P3 不为 coroutine 出编译路径**。所有协程线程上的执行都走 crescent(P1 解释器),即便函数已经被升过
gibbous——主线程上同 Proto 的 gibbous 代码,**协程线程上不进入**。这把「yield 不能穿越 Wasm 帧」这一物理
限制的影响面,从「可能撞上的边角」收紧成「永远不会撞上的几何隔离」。

### 0.2 与 P1 08 / P2 04 / P3 04-trampoline 的关系

| 文档 | 主管 | 本文关系 |
|---|---|---|
| [P1 08-coroutines](../p1-interpreter/08-coroutines.md) | coroutine 设计单一事实源 — Thread/resume/yield/路线 A vs B | 本文是 P1 08 §5 「yield 不跨 C 边界」物理限制的 Wasm 层延伸,新增「gibbous 帧不可穿越 yield」一节(对 P1 08 §6 的回填请求,§5.2) |
| [P2 04-try-compile-fallback](../p2-bridge/04-try-compile-fallback.md) | 升层决策状态机 + considerPromotion 入口 | 本文要求 P2 04 §3 considerPromotion 入口加线程上下文输入(§2.4 / §5.1,对 P2 的回填请求) |
| [P3 04-trampoline](./04-trampoline.md) | crescent↔gibbous↔host 协议 + 错误冒泡 | 本文复用 doCall gibbous 分支作为线程级 tier 规则的检查点;trampoline 协议本身不变(§5.3) |
| [P3 02-translation](./02-translation.md) | 字节码→Wasm 翻译 | 不受影响;线程级规则在「调用进入 gibbous」处拦截,翻译器无须感知 |
| [P3 05-safepoint-gc](./05-safepoint-gc.md) | 跨层 safepoint | 不受影响;safepoint 在 gibbous 帧内,而本规则保证 gibbous 帧不会被协程触达 |

### 0.3 本文不解决什么

为防止"边界蔓延"造成本文目标失焦,显式列出本文**不**处理的项:

- **协程的 yield/resume 机制本身**:由 [P1 08](../p1-interpreter/08-coroutines.md) 单一事实源定义。本文只
  使用其结论(路线 B 的 yield 信号冒泡 + 不跨 C 边界限制),不再重述机制。
- **协程在 P5 trace JIT 下的形态**:trace JIT(P5)对 coroutine 的处理留 [../p5-trace-jit/00-overview.md](../p5-trace-jit/00-overview.md)
  专管。本文只覆盖 P3 阶段(method-style Wasm 编译)。
- **路线 A 的具体实装代码**:P1 08 §3.2 已论证 P1 选定路线 B,A 仅作风险兜底。本文只在 §4 给「若启用 A,
  P3 协程升层如何重新可行」的代价分析,不出实装代码。
- **P4 native JIT 下的协程规则**:P4 继承本规则(线程级 tier 规则),具体由 [../p4-method-jit/07-p3-retirement](../p4-method-jit/07-p3-retirement.md)
  §2 决策矩阵 + 不变式清单确认。本文给 P3 定稿,P4 沿用。

---

## 1. 物理论证:为什么 yield 不能穿越 Wasm 帧

### 1.1 路线 B 下 yield 信号冒泡的复原依赖

[P1 08 §3.3](../p1-interpreter/08-coroutines.md) 已定 P1 路线 B 的 yield 信号冒泡机制:

```
                    co 的 Lua 调用链(全在 arena CallInfo)
                    main → f1 → f2 → ... → fN(深 N 层 Lua 帧)
                                            │
        fN 里执行 coroutine.yield(...) (host)
                                            │
    ① yield host 把当前 frame 存回 fN 的 CallInfo(saveFrame),设 co.status=suspended
       返回特殊信号 callYield(经 callHost,P1 08 §3.4)
                                            │
    ② 主循环 case CALL 收到 callYield → return executeSignal{sigYield}
       ★ 直接 return 出 execute 这个 Go 函数 —— co 的 CallInfo 链原封不动留在 arena
                                            │
    ③ execute 返回到它的调用者 —— resume host 里的 `signal := vm.execute(co)` 那一行
                                            │
    ④ 下次 resume:reloadFrame 从栈顶 CallInfo 重建 frame(loadTopFrame)
                  从 yield 的下一条指令继续(savedPC 已存)
```

**这套机制成立的物理前提是:整条 Lua 调用链 + 当前 frame 都能"冻结-解冻"**——CallInfo 链冻结在 arena
(yield 不弹 CallInfo,P1 08 §3.3 关键差异表),frame 的 pc/top 经 saveFrame 存回 CallInfo 持久化。下次
resume 时 reloadFrame 从持久化状态重建 frame,**从 yield 的下一条指令继续**。

**核心要点**:yield 不只是"暂停",还要"之后从原地复原继续跑"——这要求**整条调用链上每一帧都是可暂停-可
复原的**。

### 1.2 错误穿 Wasm 帧可以(单向放弃)

[04-trampoline §4](./04-trampoline.md) 已定 P3 错误传播路径:gibbous 函数返回非 0 status ⇒ 自身清理后向上层
冒泡 ⇒ protected 边界(pcall)捕获或最终被 entry 边界吸收。这与 P1 解释器的显式错误返回([../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md) §9)**同构**。

**错误能穿 gibbous 帧的关键事实**:错误冒泡是**单向放弃**——一旦冒泡出某个 gibbous 帧,**不需要再回来复原
那个帧**(它的 Wasm 局部变量、locals 缓存、value 栈槽全部丢弃,不要紧——错误处理不在乎"丢失的中间状态"
是什么)。Wasm 函数自身的 return 语义(返回 status code)就足够冒泡——上层只关心"成功/失败"两个语义,
不需要恢复任何中间执行点。

```
错误冒泡 vs yield 信号:语义对比
─────────────────────────────────
                          错误冒泡       yield 信号
是否需要复原原帧?         否(放弃)     是(下次 resume 必须从原地继续)
中间状态(Wasm locals)?    丢弃即可       必须能完整恢复
Wasm 帧能不能穿越?         能(自然 return) 不能(不能"半 return 半保留")
```

### 1.3 yield 不行——core Wasm 无 continuation

**core Wasm 没有 first-class continuation**:[Wasm 1.0 spec](https://www.w3.org/TR/wasm-core-1/) 的执行模型
是经典栈机,每个 function 调用是一次性的——`call` → 函数运行 → `return`,没有"挂起当前函数让控制权出去、
之后从原地恢复"的指令。Wasm 提案中的 `wasmfx` / `stack-switching` / `typed-continuations` 才提供这一能力,
但 P3 的目标后端 wazero 当前**不支持** continuation 类提案。

**物理后果**:gibbous 帧没有"挂起"机制——它要么自然 return(成功结束 / 错误冒泡),要么完全没运行
(根本没被调用)。**没有"半 return"的中间状态**——这与 P1 路线 B 的 yield 信号冒泡需求(**保留** CallInfo
链 + saveFrame 持久化中间状态以便下次 resume 复原)**根本冲突**。

具体地说:

```
若让 yield 信号穿越 gibbous 帧(假设场景):
─────────────────────────────────────────
  Lua 调用链:   ... → gibbous_f → gibbous_g → 调 host coroutine.yield
                       ↑ Wasm 帧    ↑ Wasm 帧
                                            │
       yield 触发 callYield ⇒ 主循环 return sigYield
                                            │
       但这里的"主循环"是谁?——
         若是 crescent 解释器:它在 gibbous 之外,本来就不该被进入(已升 gibbous)
         若是 gibbous 自己:Wasm 没有"主循环"概念 —— gibbous_g 是 wasm function,
                            它的 i32 return 值穿出去就是它的执行已结束,
                            状态全丢,无法复原
                                            │
                            ★ 死路 ★
```

**关键约束**:wazero 的 wasm function 调用是**普通的 Go function 调用**(`api.Function.Call(ctx, args...)`)。
若让 gibbous_g 在 yield 时返回特殊 status,gibbous_f 必须配合做完整的"局部状态保存到 arena"——这要求每个
Wasm 帧都把它的 locals/operand stack 都序列化到 arena。**而 core Wasm 不暴露 operand stack 给程序**——
runtime 用的是 wazero 内部表示,Go 代码无法读取。**根本写不出"保存 Wasm 帧再恢复"的代码**。

### 1.4 与「yield 不能跨 host(C)边界」是同一物理限制

[P1 08 §5.1](../p1-interpreter/08-coroutines.md) 的 5.1 限制陈述:

> Lua 5.1 禁止跨 C-call(host call)边界 yield。具体地,以下情况 yield 报错
> `attempt to yield across C-call boundary`:在 host function 内部、pcall 内、`__index`/`__add` 等元方法内、
> `string.gsub` repl 函数内 ……

**P1 08 §5.1 给的物理本质**:

> host 是真 Go 栈帧,yield 的冒泡(return)穿不过它。Go 函数只能正常 return(返回排序结果)或 panic,
> 不能"暂停自己让 Lua 续跑"。

**gibbous 帧与 host 帧的物理同构**:Wasm 帧虽然不在"Go 栈上"(wazero 自管 wasm 执行栈),但**对协程语义的影响
完全等价**——都是"无法挂起后复原的不可中断帧"。从协程的视角看:

| 帧类型 | 能否 yield 信号穿越 | 物理原因 |
|---|---|---|
| crescent Lua 帧(05 主循环 case CALL) | ✅ 可(return sigYield 出 execute,§3.3) | 主循环是单一 for 循环 + return,可以中途返回 |
| host(C 函数)帧 | ❌ 不可(P1 08 §5.1) | Go 函数无 first-class continuation,真 Go 栈帧无法挂起 |
| **gibbous(Wasm)帧** | ❌ **不可**(本节) | core Wasm 无 first-class continuation,Wasm 帧无法挂起 |

**统一原则**:**任何"非主循环 case CALL"的帧都不能让 yield 信号穿越**。crescent 解释器主循环是协程切换
点的唯一**自然**位置(`return executeSignal{kind: sigYield}` 是 Go 语言级的合法 return,主循环写法本就支持)。
host / gibbous 都不是"Go 写的主循环",都不行。

> **本文给 P1 08 §5.1 的回填**:把"5.1 限制"从"语言版本兼容"理由升级为"物理限制"理由——**core Wasm 与
> C/Go 同样无 continuation,P3/P4 都受同一物理约束**。即便 5.2+ 的 `lua_callk`/`lua_pcallk` 提供了 host
> 侧 continuation,Wasm 侧仍无 continuation,所以"5.1 限制"并不是 5.1 的局限,**是分层 VM 的物理底线**。

### 1.5 假设放任:语义分裂的差分必炸

**假设场景**(用反证法说明为何必须制定线程级 tier 规则):

```lua
local function f(x, k)
  -- ... 一些计算 ...
  k(x)              -- 调用 callback k
  -- ... 后续 ...
end

local function consumer(x)
  coroutine.yield(x)  -- 在 callback 里 yield
end

local co = coroutine.create(function()
  f(42, consumer)
  return "done"
end)

coroutine.resume(co)
```

`f` 和 `consumer` 都是纯 Lua 函数,不含任何 F1-F7 不可编译形状([../p2-bridge/03-compilability-analysis](../p2-bridge/03-compilability-analysis.md)
§3),可编译性分析判 `CompCompilable`。在 P2 阶段它们会被升 gibbous(若热度阈值越过)。

**若放任 gibbous 在协程里跑**(即不加线程级规则):

| 升层状态 | 行为 | Lua 5.1 期望 |
|---|---|---|
| f / consumer 都未升层(crescent) | yield 沿 case CALL 冒泡出 execute,resume 收到 yield 值,正常 | ✅ 正常 yield |
| f 已升层(gibbous),consumer 未升层 | consumer 想 yield,信号要穿过 gibbous 帧 f → **物理上不可能** → 报错或崩溃 | ❌ 应正常 yield |
| f / consumer 都升层 | 同上,gibbous 帧拦下 yield | ❌ 应正常 yield |

**这就是「解释执行能 yield、升层后同代码报错」的语义分裂**——**同一段 Lua 代码,因为热度和升层时机的差异,
行为出现 0/1 跳变**。

**为什么不能接受**(承 [../roadmap.md §5 原则 2](../roadmap.md) 的差分主防线):

1. **差分必炸**:[../p1-interpreter/12-testing-difftest](../p1-interpreter/12-testing-difftest.md) 的差分基准
   是"crescent 解释器与 gibbous 编译执行逐字节一致",一旦 yield 在 gibbous 路径报错而 crescent 路径成功,
   差分立即失败。**这是分层 VM 最危险的 bug 类(投机错误静默错果)的退化版本——不是静默,是显式错果但
   依赖运行期热度,出现概率与场景耦合,极难复现**。
2. **用户体验崩盘**:用户写的代码在测试期(冷,crescent)能跑,生产期(热,gibbous)突然报
   `attempt to yield across ???` ——错误种类还得现编,因为 5.1 没有"yield across Wasm boundary"措辞。
3. **修复路径不存在**:若要让 gibbous 支持 yield,需要 wasm continuation 提案落地 + wazero 后端实现支持
   + 复杂的 Wasm 帧序列化代码——**P3 阶段(6-12 人月)完全不切实际**(roadmap §4)。

**结论**:**必须从机制上让"协程内的执行不进入 gibbous 帧"**——这就是 §2 的线程级 tier 规则。

---

## 2. 线程级 tier 规则定稿

### 2.1 规则:**只有主线程的执行进入 gibbous;协程线程上调用一律走 crescent**

**P3 定稿**:在 P3 阶段,升层判定与 trampoline 跳转都附加一个**线程上下文条件**——

> 当且仅当**当前正在执行的 Thread 是主线程(`th == vm.mainThread`)**时,gibbous 代码才被触达。
> 协程线程(任何 `coroutine.create` 创建的 Thread)上的执行,即便目标 Proto 已升 `TierGibbous`,**仍走
> crescent 解释**。

```
                            P3 调用决策(crescent.doCall 的 gibbous 分支)
                            ────────────────────────────────────────
                            ┌─ proto.tierState == TierGibbous?
                            │
                            ├─ 否 → 走 crescent(原 P1 逻辑)
                            │
                            └─ 是 → ★ 多查一项:th == vm.mainThread?
                                      │
                                      ├─ 是 → trampoline 跳 gibbous(P3 §5.2)
                                      │
                                      └─ 否 → ★ 仍走 crescent ★(协程线程)
```

**这把"是否进入 gibbous"的决策从"按 Proto 单维"扩展为"按 (Proto, Thread) 二元"**——同一 Proto 在主线程上
跑 gibbous,在协程线程上跑 crescent。

### 2.2 实装点:doCall 的 gibbous 分支多查一个 `th == mainThread`

**实代码骨架**(承 [04-trampoline §2](./04-trampoline.md) 的 crescent → gibbous 入口协议):

```go
// internal/crescent —— doCall 的 gibbous 分支(本规则的实装点,§6.2 详)
//
// 不变式:这是 P3 阶段唯一的"是否走 gibbous"决策点。
// trampoline、translation、safepoint 等其它子系统都不重复决策。
func (vm *VM) doCall(f *frame, i bytecode.Instr) callResult {
    // ... 普通 Lua call / host call 的处理(05 §7)... 无关本规则

    // ★ gibbous 分支判定
    callee := f.calleeProto(i)  // 被调 Proto
    if callee.tierState == TierGibbous {
        // ★ 线程级 tier 规则:只有主线程才走 gibbous
        if vm.curThread == vm.mainThread {
            return vm.trampolineCallGibbous(f, callee)  // P3 04 §2
        }
        // 协程线程:即便已升 gibbous,本次调用走 crescent
        // 这一支等价于 callee 没升层时的 crescent 路径(P1 §7.1 enterLuaFrame)
    }

    // 正常 crescent 路径
    return vm.enterLuaFrame(f, callee)
}
```

**实装要点**:

1. **唯一决策点**:本规则的检查只发生在 `doCall` gibbous 分支——其它任何代码(translation、safepoint、
   trampoline 内部)都不重复检查。这把规则的影响面收紧到一行 `if`,易于审计与维护。
2. **`vm.mainThread` 字段已存在**:[../p1-interpreter/06-memory-gc §5.1 R3](../p1-interpreter/06-memory-gc.md)
   已定 R3 = 主线程,State 已持有 `mainThread *Thread` 字段(§6.1 详)。本规则不新增字段,只复用。
3. **协程线程上 callee 的 tierState 不变**:协程内调用一个升层 Proto,**Proto 的 `tierState` 仍是
   `TierGibbous`**——本规则不动 Proto 状态,只改"实际走的路径"。这避免「协程线程改 tierState 又被主线程
   读到不一致」的并发问题(§5 多 State 边角)。
4. **每次 doCall 都查 vm.curThread**:`vm.curThread` 是 VM 字段(P1 08 §3.5 resume host 的切换点),协程
   切换点恰是 resume host 入 / 出 execute——doCall 期间 `vm.curThread` 是稳定的。

### 2.3 规则自洽:三层互锁

线程级 tier 规则**自洽**——本节论证为什么这条规则一旦生效,「yield 撞上 gibbous 帧」永远不会发生。

#### 2.3.1 主线程 yield 本就非法

[P1 08 §8.2](../p1-interpreter/08-coroutines.md):

> 主线程 yield 报错 `attempt to yield from outside a coroutine`(`canYield` 的第①检查)。
> 主线程没有 resumer,yield 无处可去——物理上,主线程的 execute 是 `Program.Call` 直接起的,不是某个
> resume host 起的。yield 信号若从主线程的 execute 冒泡出去,会撞到 `Program.Call` 的 Go 栈。

所以**主线程上的执行,根本不可能触发 yield**(yield 在 host `coroutine.yield` 入口就被 `canYield` 拦下报错,
不会进入冒泡阶段)。

#### 2.3.2 主线程上的 gibbous 帧不会被 yield 穿越

由 §2.3.1,主线程不能 yield。**故主线程上即便存在多层 gibbous 帧,yield 信号也不会出现要穿越它们的需求**。
gibbous 帧在主线程上是"安全"的——它们之间的调用全经 04-trampoline 协议正常 return,从不经历"半 return"
的 yield 信号。

#### 2.3.3 协程线程上不进 gibbous

由本规则(§2.1):协程线程上调用一律走 crescent,**即便 Proto 已升 gibbous**。所以协程线程上**根本没有
gibbous 帧**——它的整条 Lua 调用链全是 crescent 帧。yield 信号在 crescent 帧间冒泡,正是 P1 08 §3.3 设计好
的路径,自然成立。

#### 2.3.4 三层互锁的几何形状

```
主线程   ─┬─ 走 gibbous(规则允许)        ─┬─ 但主线程不能 yield(P1 08 §8.2)
        │                                │  → gibbous 帧不会被 yield 穿越 ✓
        └─ 走 crescent(同 P1)            ┘
                                         
协程线程 ─── 走 crescent(规则强制)      ── yield 沿 crescent case CALL 冒泡
                                         (P1 08 §3.3 原路径)
                                         → 不会撞上 gibbous 帧 ✓
```

**结论(规则自洽性的形式化)**:在线程级 tier 规则下,**「yield 撞上 gibbous 帧」事件的出现概率为 0**——
不是"实测罕见",是"机制上构造性消解"。这与 P1 08 §3.2 论证路线 B 选择时的"5.1 限制天然吻合,(B) 让 5.1
限制成为机制的自然结果"是同一思路:**让物理限制在边界处被几何隔离吃掉,而不是每次运行期检测**。

### 2.4 升层判定改造:considerPromotion 入口加线程上下文输入

**对 P2 04 的回填请求**:[../p2-bridge/04-try-compile-fallback §3](../p2-bridge/04-try-compile-fallback.md)
当前的 considerPromotion 签名是:

```go
func (b *Bridge) considerPromotion(proto *bytecode.Proto, pd *ProfileData)
```

输入是 (Proto, ProfileData)——**没有线程上下文**。在线程级 tier 规则下,这不够:即便 Proto 在主线程上越阈值
触发升层,如果它**只**在协程线程上被调用过(主线程从未调过它),升层是浪费工作(协程线程不会用到 gibbous
代码)。

**改造方案**:considerPromotion 入口加线程上下文输入:

```go
// P3 阶段(本文 §2.4 / §5.1)对 P2 04 的扩展
func (b *Bridge) considerPromotion(proto *bytecode.Proto, pd *ProfileData, th *Thread) {
    // 现有 P1 守卫(吸收态)
    if pd.tierState != TierInterp {
        return
    }
    // ★ 本文新增:协程线程上即便热度越阈值,也不触发升层
    //   理由:协程线程上即便升了 gibbous 也用不上(§2.1 规则强制走 crescent),
    //         编译工作浪费;且若主线程从未调过此 Proto,升层后 mainThread 上的
    //         调用频次未必能摊薄编译成本。
    if th != b.vm.mainThread {
        return  // 协程线程上的回边采样不触发升层
    }
    // ... 现有的 P2/P3/P4 路径(03 可编译性查询 + try-compile + installGibbous)
}
```

**对 considerPromotion 调用方的影响**:

承 [../p2-bridge/01-profiling §4.3](../p2-bridge/01-profiling.md) 的调用契约,considerPromotion 由 onBackEdge /
onEnter 触发——**这两个采样点本身就在 P1 解释器主循环里执行,可以拿到当前 Thread**(`vm.curThread`)。
所以 P2 01 的 onBackEdge / onEnter 入口签名也要相应扩展(对 P2 01 §4 的连带回填):

```go
// 对 P2 01 §4 的连带回填
func (b *Bridge) onBackEdge(proto *bytecode.Proto, pd *ProfileData, pc int, th *Thread) {
    pd.backEdge[pc]++
    if pd.backEdge[pc] >= HotBackEdgeThreshold {
        b.considerPromotion(proto, pd, th)  // 把当前 Thread 透传
    }
}
```

**两种实现选择(P2 落地时定)**:

| 选择 | 处理 | 优劣 |
|---|---|---|
| (A) 协程线程上的回边/入口采样**也累加,但不触发 considerPromotion** | onBackEdge 仍累加 backEdge[pc],只在阈值越过时多查 `th == mainThread` 才进 considerPromotion | 简单,profile 数据完整(诊断价值);仅决策入口加线程门禁 |
| (B) 协程线程上的回边/入口采样**不累加** | onBackEdge 入口先查 `th != mainThread` 直接 return | 节省一点写带宽;但 profile 数据残缺,失去诊断信号 |

**当前定稿选 (A)**:与 P2 04 §7.3「累加无条件,决策入口守卫拦下」纪律一致——profile 累加由 P1 主导(无条件),
线程门禁在 P2 决策入口处。这条纪律在 Stuck 不重试场景已用过,本规则复用,保持 P2 内部纪律统一。

> **回填请求登记**:本节的 considerPromotion 签名扩展(加 `th *Thread`)与 onBackEdge / onEnter 入口签名扩展
> 是**对 P2 04 / P2 01 的回填请求**——P3 落地(PW8)时同批改 P2 文档与代码。承 [00-overview §3.4](./00-overview.md)
> 与 §7 的回填义务表登记,P2 01/04 当前是占位状态,等 P3 实装。

---

## 3. 代价分析

### 3.1 协程内代码永不升层 — 列内核目标可接受

**直接代价**:协程线程上的所有代码,**无论多热**,**永远走 crescent 解释**。这意味着:

- 协程内的循环、表访问、算术运算都不享受 P3 的「循环密集 ≥2x over P1」性能红利([08-testing-strategy §1](./08-testing-strategy.md))。
- 即便协程函数极热(例如长跑迭代器),也不被升层——只有主线程上的调用者会受益(若调用者本身被升)。

**对望舒目标的可接受性**:

[../roadmap §4 P3](../roadmap.md) 已定 P3 战略价值:**「分层机器第一次全链路运转」**——首要目标是把分层骨架
跑通,性能验收门(循环密集 ≥2x over P1)是工程门,不是 ROI 主轴。结合首个宿主用例:

| 维度 | 列内核目标 | 协程相关性 |
|---|---|---|
| **首个宿主**:规则引擎(pineapple) | 批量列计算 kernel 经 `Program.Call` 入口跑 | **主线程**(Program.Call 即 mainThread.execute) |
| **典型负载** | 数值循环 + 表读 + IC 命中(03 §0.1) | 与 coroutine 无关 |
| **协程出现位** | 边角(若有):宿主框架若用 coroutine 做迭代器 / 调度,但内核不在协程里 | 边角形态(roadmap §5 原则 4 已定 coroutine 走 fallback) |

**结论**:列内核目标下,**主线程承载全部热路径**——协程是边角形态,协程不升层不影响 P3 验收的性能门。

### 3.2 P3 开工前置确认(承 memory/decisions §7 与 doc-gaps)

**关键依赖**:本规则的可接受性**强依赖于"列内核确实在主线程上跑"这一假设**。如果首个宿主把热路径放在
协程里(例如把每个数据列的 kernel 包成协程,用 `coroutine.resume` 推进),线程级 tier 规则就**直接破产**——
所有 kernel 走 crescent,P3 升层完全失效,P3 性能门(≥2x over P1)拿不到。

**P3 开工前置确认条目**(承 [memory/doc-gaps](../../../llmdoc/memory/doc-gaps.md) 「P3 开工前置确认(待办)」):

> P3 开工前(PW0 spike 通过且开始 PW1 实代码之前)须向首个宿主(pineapple 团队)确认:**列内核是否跑在
> 协程里?**——决定线程级 tier 规则是否成立。

**确认结果与对应行动**:

| 答复 | 含义 | 行动 |
|---|---|---|
| **否(列内核在主线程跑)** | 规则有效,本文 §2 定稿可落地 | P3 PW1-PW9 按本文施工;PW8 落地线程级 tier 规则的实代码(`th == mainThread` 检查) |
| **是(列内核包在协程里)** | 规则破产,线程级 tier 规则使 P3 完全无法升层热代码 | **退到备选方案**(§4):路线 A goroutine 化或 P3 跳过直接做 P4(P4 §6 决策矩阵中"P3 去留"提前评估) |
| 部分(混合) | 部分 kernel 在协程里,部分在主线程 | 评估主线程承载占比;若 ≥80% 在主线程,规则仍可用,协程内热路径放弃升层;否则退备选 |

**确认时点**:[01-spike-gate](./01-spike-gate.md) 的 PW0 spike 通过(wazero call boundary < 150ns)之后、
[02-translation](./02-translation.md) PW1 翻译器骨架启动**之前**。这是 P3 阶段的"第二闸门"(spike 是第一,
本节是第二),与第一闸门一样,不通过则触发战略调整。

### 3.3 工程实测:首个宿主调研结果

**当前(2026-06-13)调研状态**:**未确认**——本文设计期不预设宿主答复。该确认条目挂在 doc-gaps "P3 开工
前置确认(待办)",由 P3 PW0 启动前主助理推动用户与宿主对齐。

**调研脚本(供 PW0 启动前参考)**:

1. 找出宿主的"批量数据 kernel"代码段(性能关键路径)。
2. 看这段代码是否被 `coroutine.create` / `coroutine.resume` / `coroutine.wrap` 包裹。
3. 若否(直接 `Program.Call` 入口或被主线程函数调用),线程级 tier 规则成立。
4. 若是,问宿主:"协程化是必需的吗?可以改成主线程 + 显式状态机吗?"——若可改,改宿主代码;若不可改,
   走备选方案(§4)。

**预期结果**(基于 roadmap §5 原则 4「coroutine 是 fallback 形状」与首个宿主目标"列内核紧凑翻译"):
**列内核大概率在主线程**(规则引擎本身没有 coroutine 需求,数据流推动是主线程上的循环);**确认主要是
排除意外**(宿主框架是否在某层包了 coroutine 而 kernel 作者没意识到)。

---

## 4. 备选方案:协程 goroutine 化(P1 08 路线 A 兜底)

若 §3.2 的开工前置确认结果是"是(列内核在协程里)",线程级 tier 规则破产。本节给备选方案——回到 P1 08 §3.1
**路线 A**(每协程一个独立 goroutine + channel 同步)的形态,论证为何路线 A 下协程升层重新可行,以及代价。

### 4.1 P1 08 路线 A 形态回顾

[P1 08 §3.1 路线 A](../p1-interpreter/08-coroutines.md):

```
co 的 goroutine:        for { <-resumeCh;  跑 execute(co);  yieldCh <- 结果 }
resumer(main goroutine): resumeCh <- args;  结果 := <-yieldCh
yield(实现):            yieldCh <- yield值;  args := <-resumeCh   // 阻塞,goroutine 挂起在这
```

每个协程跑在自己的 goroutine 里;resume / yield 用一对 channel 握手。yield 不是"让信号从 execute return 出
去",而是"让协程的 goroutine 阻塞在 channel recv 上"——goroutine 阻塞由 Go runtime 调度,**不需要 Wasm 帧
挂起**(整个 goroutine 在 Go 调度器视角是 parked)。

### 4.2 路线 A 下 coroutine 升层:为何重新可行

**关键差异**:路线 A 下,**每个协程拥有独立的 wazero Runtime / 独立的 Wasm 帧栈**——

| 路线 | gibbous 帧栈 | 跨协程影响 |
|---|---|---|
| 路线 B(P1 当前选定) | 单 goroutine 内,wazero 单 Runtime,Wasm 帧栈是该 Runtime 内部状态 | yield 需要从这一个 Wasm 帧栈"中间 return",物理不可能 |
| **路线 A**(本节) | 每协程独立 goroutine,可独立持有 wazero Runtime 实例,Wasm 帧栈是各 goroutine 内部状态 | yield = goroutine park,**Wasm 帧栈整个停在那个 goroutine 里**,不需要"中途 return" |

**路线 A 下 yield 的物理形态**:

```
协程 co 的 goroutine:
  ... 跑 execute(co) ... 跑到一半进了 gibbous 函数 g ...
  g 内部调 host coroutine.yield(...)
  host coroutine.yield 实现:yieldCh <- value; <-resumeCh   // 阻塞
                                            │
       goroutine 阻塞在 channel recv ─→ Go runtime park 该 goroutine
                                            │
       gibbous_g 的 Wasm 帧栈:【完整保留】 ─→ 这个 goroutine 的栈空间(Go 栈 + 该 goroutine
                                              持有的 wazero Runtime 内部状态)都不动
                                            │
       下次 resume:resumeCh <- args ─→ goroutine unpark ─→ 从 channel recv 返回
                                            │
       host coroutine.yield 返回 args ─→ 控制权回到 gibbous_g 的下一条 Wasm 指令
       (gibbous_g 的 Wasm 帧栈在 park 期间一字未动,unpark 后自然续跑)
```

**为什么这样可行**:Wasm 帧栈的"挂起-复原"由 **Go 调度器代替我们做**——Go runtime 知道怎么 park / unpark
一个 goroutine,而 goroutine 里持有的所有状态(包括 wazero Runtime 内部的 Wasm 帧栈)在 park 期间都是
冻结的。**我们没有"在用户态实现 Wasm 帧序列化"** —— Go 调度器把这件事接管了。

> 这正是路线 A 在 P1 08 §3.2 论证里被 P1 拒绝的原因(`docs/design/p1-interpreter/08-coroutines.md` §3.2:
> A 引入"goroutine 各自的 Go 栈",作废 05 §7 为协程铺的路);**但若 P3 协程升层是宿主刚需,A 是兜底——
> 用 Go 调度器接管 Wasm 帧的挂起/复原,这是 P3 自己造不出的能力**。

### 4.3 路线 A 实装代价

切换到路线 A,P1 / P3 / GC 各层都要改造,代价不小:

#### 4.3.1 协程切换从 saveFrame/restoreFrame 改成 channel send/recv

P1 路线 B 下,协程切换是**用户态 O(1)**:

- yield: saveFrame(几条字段写)+ 主循环 return sigYield(Go return)
- resume: vm.execute(co)(Go function call,reloadFrame)+ 几个状态字段写
- 总开销:**约 ns 级**(几次内存写 + 一次 Go call/return)

路线 A 下,协程切换变成 **channel-mediated goroutine park/unpark**:

- yield: `yieldCh <- value; <-resumeCh` —— 两次 channel 操作(send + recv)
- resume: 同上,镜像两次 channel 操作
- 每次 channel 操作触发 Go runtime 调度器(park/unpark goroutine)
- **典型开销:1-3 µs**(channel 同步 + scheduler 上下文切换,实测因 Go 版本 / GOMAXPROCS / 平台而异)

**延迟差距**:**ns 级 → μs 级,3 个数量级**。

| 场景 | 路线 B 切换 | 路线 A 切换 | 影响 |
|---|---|---|---|
| 高频生产者-消费者(每帧 yield 一次,千次/秒) | 几 µs 总开销 | 几 ms 总开销 | 协程作为高频迭代器形态时显著退化 |
| 低频协调(秒级 resume / yield) | 可忽略 | 可忽略 | 不敏感 |
| 列内核包成协程(每列 resume 一次) | 几 ns | 几 µs | 列数大时累积影响,但通常单列处理时间远大于切换开销 |

> **路线 A 仍有合理使用场景**:即便切换从 ns 退化到 µs,对"每协程承担批量计算"的形态(单次 resume 跑大段
> 代码,yield 不频繁)仍可接受。**P3 协程升层带来的「单列计算 ≥2x」收益,与「切换从 ns 到 µs」的成本是
> 不同量级**——若单列计算需要 ms 级,切换开销可忽略;若每协程只跑几 µs,A 反而更慢。需实测决定。

#### 4.3.2 每协程一个独立 wazero Runtime / Wasm 实例

路线 B 下,所有协程共享一个 wazero Runtime —— **共享 module、共享 linear memory(arena 收养)**。这是
[03-memory-model](./03-memory-model.md) 的物理基础:**arena 就是 wazero memory**。

路线 A 下,**每协程必须独立持有一个 wazero Runtime 实例**——否则两个协程的 gibbous 帧栈在同一个 Runtime
里,wazero 内部状态(stack pointer / locals)会被互相覆盖。

但**每协程独立 Runtime ⇒ 独立 linear memory**——这就**作废了 [03-memory-model](./03-memory-model.md) 的
"arena 收养 wazero memory"决策**:

| 决策 | 路线 B(P3 当前) | 路线 A(本节) |
|---|---|---|
| arena backing 来源 | wazero memory(单实例) | ??? — 多协程多 memory,arena 不能从单一来源收养 |
| GCRef 偏移寻址 | 全局唯一(单 memory) | 跨协程 GCRef 不可比较(不同 memory 偏移空间) |
| 协程间值传递 | 直接拷贝(同一 memory) | 跨协程要序列化反序列化(跨 memory) |

**结论**:路线 A 下,"arena = wazero memory"的设计基础消失,要么:

- **选项 X**:每协程独立 arena + 跨协程值传递经显式序列化(P1 08 §4 的"跨 Thread 值搬运"被升级为跨 memory
  搬运),改造面非常大。
- **选项 Y**:协程不升 gibbous,只主线程升;协程内的 Lua 代码仍走 crescent。**这等价于线程级 tier 规则的弱化版**——
  路线 A 没有解除"协程不升层",只是允许**主线程的 gibbous 帧被协程内的 yield 信号穿越成立**(因为协程的 yield
  实际是 goroutine park,不需要从主线程 gibbous 帧 return)。但若协程内代码不升,A 的最大价值(协程内升层)
  其实没拿到——只值得在"列内核就在主线程 + 协程做框架性调度"的场景启用。

#### 4.3.3 GC 协调复杂化

P1 06 §7.3 的 STW GC「天然无需停顿协调」前提:**单 goroutine,Alloc 在主流程内同步发生,GC 触发时整个系统
是静止的**。

路线 A 下,**多 goroutine 同时 Alloc / 同时跑 gibbous 代码可能**——必须引入额外同步:

- 全局锁串行化 arena 访问(损失 goroutine 并发的意义,且每个 Wasm 函数内部的 arena 写都要持锁,实装爆炸)
- 或并发 GC(P1 06 §9.4 写屏障 P1 空实现,要全面启用,且 trampoline / safepoint 协议都要重构)
- 或单时刻只允许一个 goroutine 跑 gibbous(全局 wasm-execution-mutex,本质退化成单 goroutine,A 的并发优势失效)

**结论**:路线 A 的 GC 协调改造**比路线 B 的整套实装**复杂——这正是 P1 选定路线 B 的核心理由之一(P1 08 §3.2 论证 3)。

### 4.4 路线 A 的边角:resume/yield 多值 + 错误传播经 channel 转发

路线 B 下,resume / yield 的**多个值**通过 Thread 值栈直接搬运(P1 08 §4)——**值在共享 arena memory 里,
搬运 = 拷贝几个 Value**,O(n) on n=值个数,简单。

路线 A 下,resume / yield 的多个值要经 **channel** 传递:

```go
type yieldEnvelope struct {
    kind   signalKind   // 同 P1 08 §3.3 的 sigReturn / sigYield / sigError
    values []Value      // 多个返回值/yield 值
    err    *LuaError    // sigError 时的错误对象
}
yieldCh chan yieldEnvelope
resumeCh chan []Value
```

每次 yield/resume 都要构造 envelope、send、recv、deconstruct。这本身不是技术难题,但**协议复杂度上升**:

- envelope 的字段需求会增长(yield 多值 / 错误对象 / status 链所有信息),每加一项都要改 channel 协议
- 错误传播原本是 `return *LuaError`,A 下变成 `yieldEnvelope{kind: sigError, err: ...}`——错误冒泡的统一
  通道(P1 09 §9.4)被打散
- pcall 边界 / status 链等 protected 边界协议都要重新映射到 channel 模型上

承 P1 08 §3.2 论证 4(路线 B 的 yield 冒泡与 *LuaError 冒泡同构,复用 05 §9 的同一通道):路线 A 下这一**架构同构性**消失,P1 09(错误处理)与 P1 08(协程)的统一性退化成两套独立机制。

### 4.5 路线 A 留作 P3 启用前置失败的兜底

**当前定稿**:路线 A **不在 P3 阶段实装**,留作:

1. **§3.2 P3 开工前置确认结果为"是"时的兜底**(列内核确实在协程里):
   - PW0 spike 通过 + 开工前置确认失败 ⇒ 在 PW1 启动前评估"启用路线 A vs P3 跳过(直接评估 P4)"
   - 决策依据:① 宿主切换协程化代价;② 路线 A 的实装代价(本节)与时间窗(可能延后 P3 6+ 个月);
     ③ P4 的人月预算(roadmap §4 P4 6-12 人年,显著高于 P3)
2. **未来宿主形态变化的演进路径**:
   - 当前(2026)首个宿主大概率列内核在主线程,本规则有效
   - 未来若宿主形态演进("协程式数据流"模式流行),路线 A 重新评估
3. **P5 trace JIT 启用时的备用考虑**:
   - P1 08 §3.6 已论证路线 B 与 trace JIT 长期对齐,但 trace 录制对协程的具体处理要 P5 详设;若 trace JIT
     需要打破"协程不升层"限制,A 是兜底候选

**记入文档缺口**(§8):路线 A 的具体实测代价(切换延迟实测 + GC 协调复杂度评估 + 多 wazero Runtime 内存占用)
留 P3+ / P5 阶段评估,P3 PW0 不预设。

---

## 5. 影响面

### 5.1 对 P2 04 considerPromotion 的影响

**回填请求**(承 §2.4):

```
P2 04 §3 considerPromotion 入口签名扩展:
   func (b *Bridge) considerPromotion(proto, pd)
   ─→ func (b *Bridge) considerPromotion(proto, pd, th *Thread)

P2 04 §3.2 入口加守卫:
   if pd.tierState != TierInterp { return }
   ★ 新增 ─→ if th != b.vm.mainThread { return }
   ... 现有路径 ...

连带 P2 01 §4 onBackEdge / onEnter 入口透传 th。
```

**实装时机**:P3 PW8(线程级 tier 规则的实代码)。在 PW8 之前,P2 04/01 维持现有签名,P3 PW1-PW7 不依赖此回填。
PW8 同批改 P2 04/01 文档与 internal/bridge 实代码。

**对 P2 04 §2 状态机的影响**:**无**——状态机仍是单向 + 吸收态(`TierInterp → TierGibbous` / `TierInterp → TierStuck`),
本规则不引入新状态、不引入"协程上不该升层"的状态机分支。本规则只在**升层判定入口加门禁**(决定 considerPromotion
是否真正进入决策),不动状态机的转移条件本身。

### 5.2 对 P1 08 的影响

**回填请求**:[P1 08-coroutines](../p1-interpreter/08-coroutines.md) 增「gibbous 帧 = 不可穿越边界」一节。

P1 08 §6 末尾(目前是 P3 前瞻引用占位)替换为正文章节,内容包含:

1. P1 08 §5 「yield 不跨 C 边界」的物理本质从"语言版本兼容"扩展为"分层 VM 物理底线"。
2. 新增 §5.X(具体编号待 P1 08 重新组织):**「gibbous 帧不可穿越 yield」**——核心 Wasm 无 first-class
   continuation,Wasm 帧无法挂起后复原(本文 §1.3),与 host(C/Go)帧同属"无 continuation 不可中断帧"。
3. P3 阶段的应对:线程级 tier 规则,见本文 §2。
4. 路线 A 兜底:若 P3 协程升层是宿主刚需,可启用路线 A;具体见 P3 07 §4。

**实装时机**:P3 PW8 同批,把 P1 08 §6 末尾的前瞻引用替换为正文。

**对 P1 08 路线 B 选定的影响**:**无**——P1 08 §3.2 的论证(架构纯粹性 / 5.1 限制天然吻合 / GC 简单性 / 实装
成本可控)依然成立。本文只是把"为什么 5.1 限制是物理底线"这条理由的论据扩充——从"5.1 vs 5.2+ 兼容"的层面,
扩充到"core Wasm 也无 continuation"的层面,**强化**而非动摇 P1 路线 B 选定。

### 5.3 对 P3 04-trampoline 的影响

**[04-trampoline](./04-trampoline.md) 的协议本身不变**——crescent → gibbous 入口签名、gibbous → crescent /
host imported 助手分派、status 链错误冒泡等都不动。

**唯一接口面**:[04-trampoline §2 crescent → gibbous 入口](./04-trampoline.md) 是 doCall 的 gibbous 分支
通过 `trampolineCallGibbous` 进入 gibbous 帧——本规则只在**进入这个 trampoline 之前加一道门**(`th == mainThread`
检查),trampoline 协议本身不变。

具体地说,§2.2 的 doCall 改造是这样:

```go
if callee.tierState == TierGibbous {
    if vm.curThread == vm.mainThread {
        return vm.trampolineCallGibbous(f, callee)  // ★ trampoline 入口未变
    }
    // 协程线程:fall through 到 crescent 路径
}
return vm.enterLuaFrame(f, callee)
```

**04-trampoline 视角**:它根本看不到本规则——它只在"被调用时"工作,本规则在"是否调用它"层面拦下。这样
04-trampoline 文档与代码可以不感知本规则,保持职责单一。

### 5.4 对其它 P3 子文档的影响

| 子文档 | 影响 |
|---|---|
| [01-spike-gate](./01-spike-gate.md) | 无;spike 测的是 wazero call boundary,与协程规则无关 |
| [02-translation](./02-translation.md) | 无;翻译器不感知 Thread,只翻 Proto |
| [03-memory-model](./03-memory-model.md) | 无;arena = wazero memory 假设的是路线 B 单 Runtime;若启用路线 A 则作废,但路线 A 不在 P3 阶段实装 |
| [05-safepoint-gc](./05-safepoint-gc.md) | 无;safepoint 在 gibbous 帧内部,本规则保证 gibbous 帧不会被协程触达,safepoint 协议自然不与协程交互 |
| [06-ic-feedback-consume](./06-ic-feedback-consume.md) | 无;IC 快照固化与线程无关,主线程升层时消费 feedback |
| [08-testing-strategy](./08-testing-strategy.md) | **加一项验收**:协程内 hot + Compilable 函数应保持 TierInterp,主线程同 Proto 正常升层。差分基线包含协程 yield/resume 全形态 |
| [implementation-progress](./implementation-progress.md) | **PW8** 实装本规则:doCall 检查 + considerPromotion 签名扩展 + P2 04/01 回填 |

### 5.5 对 P4 native JIT 的影响

[P4 native JIT](../p4-method-jit/00-overview.md) 继承 P3 的全部分层结构,只换发射后端([00-overview §1](./00-overview.md)
表「P3 与 P4 同属 tier-1 但发射后端不同」)。线程级 tier 规则**对 P4 同样适用**——

物理原因:**native code 也无 first-class continuation**——P4 发射的 x86_64 / arm64 native code 是普通函数
调用,no setjmp/longjmp magic 能让它"挂起后复原"。所以即便 P4 投机失败要 deopt(P4 §3.4 OSR exit),deopt
本身也是**单向放弃**(回到解释器),不是"挂起 + 之后复原"。

**P4 也走线程级 tier 规则**:协程线程上的执行不进入 P4 native code,与 P3 同。

> **统一原则**:**「协程线程不升层」是分层 VM 的物理底线,不是某一阶段的工程权宜**。P3/P4/P5 都受此约束,
> 除非启用路线 A 兜底(每协程独立 wazero/native 执行栈)。

---

## 6. 实装细节

### 6.1 mainThread 字段:State 已有(已落地)

[../p1-interpreter/06-memory-gc §5.1 R3](../p1-interpreter/06-memory-gc.md):

> R3:主线程(main thread)。主线程也是一个 Thread([01](../p1-interpreter/01-value-object-model.md) §5.6),
> 由 `State`/`Program` 初始化时创建(`State.mainThread`),是 VM 的入口执行流。

**字段已落地**:`State.mainThread *Thread`(Go 字段名待与代码对齐;P1 实装已有,本规则零新增字段)。

**访问路径**:`vm.mainThread`(VM 持有 State 引用,或 mainThread 直接挂在 VM 上,具体视代码组织)。
本规则的 `vm.curThread == vm.mainThread` 检查只是一次指针比较,O(1) 且无锁(单 State 内 mainThread 不变,
curThread 在 doCall 期间稳定)。

### 6.2 doCall 的 gibbous 分支检查实代码骨架

承 §2.2 给出的代码骨架,这里给出更详细的版本(标注与现有 P1 / P3 04 接口的对接点):

```go
// internal/crescent —— doCall 的 gibbous 分支(本规则的实装点)
//
// 调用方:[../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md)
//        主循环 case CALL(05 §7.1)。
// 输入:
//   - f: 当前 frame(05 §1.3)
//   - i: CALL 指令(包含 R(A) / R(B) / R(C),05 §7.2)
// 输出:
//   - callResult: callEnteredLua / callReturnedHost / callError / callYield(P1 08 §3.3)
//
// 不变式:
//   - 本函数是 P3 阶段唯一感知线程级 tier 规则的代码点;
//   - trampoline / translation / safepoint 都不重复检查;
//   - vm.curThread 在本函数执行期间稳定(协程切换只在 resume host 入/出 execute 时,
//     而 resume host 也是经 callHost 进 host 帧,doCall 期间 vm.curThread 不会改)。
func (vm *VM) doCall(f *frame, i bytecode.Instr) callResult {
    callee, isHost := vm.resolveCallee(f, i)  // 05 §7.2 解析 R(A)
    if isHost {
        return vm.callHost(f, i)  // 05 §7.6 host 路径,与本规则无关
    }
    proto := callee.proto  // *bytecode.Proto

    // ★ gibbous 分支判定
    if proto.tierState == TierGibbous {
        // ★ 线程级 tier 规则(本文 §2.1):只有主线程才走 gibbous
        if vm.curThread == vm.mainThread {
            // P3 04 §2 trampoline 入口
            return vm.trampolineCallGibbous(f, proto, callee)
        }
        // 协程线程:fall through 到 crescent 路径(下面 enterLuaFrame)
        // 注意:proto.tierState 仍是 TierGibbous,本规则不动状态;
        //   只是本次 doCall 不走 gibbous 路径。
    }

    return vm.enterLuaFrame(f, proto, callee)  // 05 §7.1 普通 Lua 调用路径
}
```

**性能影响**:协程线程上每次调用一个已升 gibbous 的 Proto,多一次比较 + 一次失败的 branch(实测应 < 1 ns)。
主线程上每次调用一个已升 gibbous 的 Proto,多一次比较 + 一次成功的 branch(同 < 1 ns)。**这条额外检查在
任何工作负载下都不会成为热点**——它远比"判 tierState == TierGibbous"本身的查表(可能 cache miss)便宜。

### 6.3 协程线程上的 considerPromotion 行为

承 §2.4 的 P2 04 §3 入口扩展,协程线程上的回边/入口越阈值时,considerPromotion 入口直接 return:

```go
// 对 P2 04 §3.2 的扩展(本文 §2.4)
func (b *Bridge) considerPromotion(proto *bytecode.Proto, pd *ProfileData, th *Thread) {
    // P1 守卫(吸收态)
    if pd.tierState != TierInterp {
        return
    }
    // ★ P0' 守卫:协程线程不升层(本文 §2.4)
    if th != b.vm.mainThread {
        return
    }
    // P2/P3/P4 路径(同 P2 04 §3.2)
    comp := b.CompilabilityOf(proto)
    if comp != CompCompilable {
        pd.tierState = TierStuck
        pd.compileTried = true
        b.diag.LogStuck(proto, pd, comp)
        return
    }
    // ... try-compile ...
}
```

**ProfileData 的更新行为**(承 §2.4 选 (A)):

- 协程线程上 onBackEdge / onEnter **仍累加** `pd.backEdge[pc]` / `pd.entryCount`(P1 主导,无条件)
- 但 considerPromotion 入口的 P0' 守卫拦下,不进决策路径,不调 `b.tryCompile`
- profile 数据完整保留,**诊断工具可以看到"这个 Proto 在协程线程上很热,但因线程级规则不升层"**——这是
  规则透明性的工程价值

**日志格式**:协程线程上越阈值的 Proto **不打 promoted / stays interpreted / compile failed 任一日志**——
本规则在 considerPromotion 入口直接 no-op,不触发任何日志。这避免日志被协程线程上的频繁阈值越过事件刷屏。

> **若需要诊断信号**(开发者想知道"这个 Proto 在协程上多热"):走独立的 profile 查询接口
> ([../p2-bridge/01-profiling](../p2-bridge/01-profiling.md) §X 待 P2 落地),不污染主升层日志。

---

## 7. 不变式清单

承 [00-overview §9](./00-overview.md) 不变式 9「协程不升层」,本文给出更精确的不变式:

1. **协程线程一律走 crescent**(线程级 tier 规则,§2.1):任何 `coroutine.create` 创建的 Thread,在它上面的
   执行不进入 gibbous,无论 callee 的 `tierState` 是什么。
2. **yield 永远不会撞上 gibbous 帧**(规则自洽,§2.3):由不变式 1 + 主线程不能 yield(P1 08 §8.2),「yield
   信号穿越 gibbous 帧」事件的出现概率为 0。这是机制构造性消解,不是运行期检测。
3. **错误可穿 gibbous,yield 不可穿**(§1.2-§1.4):错误冒泡是单向放弃(可穿),yield 信号需要复原(不可穿)。
   这两条是 04-trampoline §4 与本文的对偶口径。
4. **路线 A goroutine 化作为兜底,不在本期实装**(§4.5):P3 不实装路线 A。若 §3.2 开工前置确认结果是"列内核
   在协程里",评估退到路线 A 或跳 P3 直接 P4。
5. **本规则对 P4 / P5 同样适用**(§5.5):分层 VM 的物理底线,P4 native code / P5 trace JIT 都受此约束。除非
   启用路线 A 兜底,否则协程线程一律走 crescent。
6. **状态机不变**(§5.1):本规则不引入新 TierState、不动 P2 04 状态机的转移条件,只在升层判定入口加门禁。
7. **trampoline 协议不变**(§5.3):04-trampoline 协议本身不感知本规则;本规则在 doCall 进 trampoline 之前
   拦截,trampoline 视角不变。
8. **mainThread 字段已落地**(§6.1):本规则不新增字段,只复用 P1 已有的 `State.mainThread`。

---

## 8. 文档缺口

记入 [memory/doc-gaps](../../../llmdoc/memory/doc-gaps.md):

### 8.1 对 P2 04 的回填请求

- **[P2 04 §3] considerPromotion 入口签名扩展**:加 `th *Thread` 输入,P0' 守卫 `if th != b.vm.mainThread { return }`。
- **[P2 01 §4] onBackEdge / onEnter 入口签名扩展**:透传当前 Thread 给 considerPromotion。
- **实装时机**:P3 PW8 同批改 P2 文档与代码。
- **当前状态**:占位,P2 04 §3 当前签名 `(proto, pd)` 不含 `th`,等 P3 PW8 同批扩展。

### 8.2 对 P1 08 的回填请求

- **[P1 08 §6] 增「gibbous 帧不可穿越 yield」节**:把目前的前瞻引用占位替换为正文,内容承本文 §1.3-§1.4。
- **强化 P1 08 §5.1 「yield 不跨 C 边界」的物理本质**:从"5.1 vs 5.2+ 兼容"扩充到"core Wasm 也无 continuation"。
- **实装时机**:P3 PW8 同批。
- **当前状态**:P1 08 §6 末尾已留前瞻引用占位(commit 已落),等 P3 PW8 替换为正文。

### 8.3 P3 开工前置确认

- **条目**:P3 PW0 spike 通过且 PW1 启动前,向首个宿主(pineapple 团队)确认「列内核是否跑在协程里」。
- **依赖**:首个宿主答复;设计期无法收口。
- **后果**:
  - 答"否" → 线程级 tier 规则有效,P3 按本文施工
  - 答"是" → 触发战略调整(评估路线 A 兜底 vs P3 跳过直接评估 P4)
  - 答"部分" → 评估主线程承载占比,≥80% 则规则仍可用,< 80% 退备选
- **挂账**:[memory/doc-gaps](../../../llmdoc/memory/doc-gaps.md) 「P3 开工前置确认(待办)」条目已登记。

### 8.4 路线 A 的具体代价测量留 P3+ / P5

- **测量项**:
  - 切换延迟实测(channel 同步 + scheduler park/unpark,典型 ns→μs 级,但具体数值因 Go 版本 / 平台 / GOMAXPROCS 变化)
  - 多 wazero Runtime 的内存占用(每 Runtime 至少几 MB,N 协程线性增长)
  - GC 协调改造的代价评估(全局锁 vs 并发 GC vs wasm-execution-mutex 三选一,各自的实装 + 性能影响)
- **触发条件**:本规则破产(§3.2 开工前置确认失败)或宿主形态演进(协程式数据流流行)
- **计划**:P3+ / P5 阶段视情况展开;当前不预设

### 8.5 路线 A 启用条件下的协议改造记账

若未来真启用路线 A,本节列出需要改造的子系统(占位,实装时撑成独立子文档):

- arena 模型:每协程独立 arena vs 共享 arena 加并发协调(§4.3.2 选项 X / 选项 Y)
- GC 协调:STW 全局停顿点扩展到所有 goroutine(§4.3.3)
- 协程间值传递:序列化反序列化协议(§4.3.2)
- yield/resume 协议:envelope-based channel(§4.4)
- 错误传播:从 *LuaError return 改成 envelope channel 转发(§4.4)
- pcall / status 链:重新映射到 channel 模型

---

## 9. 章节映射(对原稿的对账)

承本子目录文档化的章节映射纪律(详见 [00-overview](./00-overview.md) §0 文档地图):

| 原稿(`docs/design/p3-wasm-tier.md`)位置 | 本文位置 | 内容 |
|---|---|---|
| §5.4 第 1 段(yield 不能穿越 Wasm 帧) | §1.1-§1.5 | 物理论证,从 P1 08 §3.3 路线 B 冒泡引申到 Wasm 帧无 continuation |
| §5.4 第 2 段(线程级 tier 规则定稿) | §2.1-§2.4 | 规则陈述 + 实装点 + 自洽论证 + 升层判定改造(对 P2 04 的回填) |
| §5.4 第 3 段(代价、自洽、备选) | §3 / §4 | 拆为代价分析 + P3 开工前置确认 + 路线 A 兜底分析 |
| §5.4 第 4 段(对 08/P2 的回填请求) | §5 / §8 | 影响面 + 回填请求清单 |
| §11 「对 08/P2 的回填请求」 | §8 | 整理为文档缺口节 |
| [memory/decisions/2026-06-11-design-review-decisions.md] §7 维持决策 | §2.1 | "P3 协程不升层维持"决策的落点,本文是该决策的详细论证文档 |
| [memory/doc-gaps] 「P3 开工前置确认(待办)」 | §3.2 / §8.3 | 把"列内核是否跑在协程里"的开工前置确认条目展开为本文 §3.2 的决策树 |

---

相关:
[00-overview](./00-overview.md)(P3 总览,§1 边界表「协程升层」行 + §9 不变式 9) ·
[01-spike-gate](./01-spike-gate.md)(开工闸门,与本文的开工前置确认共同构成 P3 启动的两道闸门) ·
[02-translation](./02-translation.md)(翻译器,与本规则解耦) ·
[03-memory-model](./03-memory-model.md)(arena = wazero memory,路线 A 启用时此基础消失) ·
[04-trampoline](./04-trampoline.md)(crescent↔gibbous 协议,本规则在 trampoline 入口前拦截不动协议) ·
[05-safepoint-gc](./05-safepoint-gc.md)(safepoint 在 gibbous 帧内,本规则保证协程不触达 gibbous 帧) ·
[06-ic-feedback-consume](./06-ic-feedback-consume.md)(IC 快照固化,与线程无关) ·
[08-testing-strategy](./08-testing-strategy.md)(验收口径,加协程不升层验收项) ·
[implementation-progress](./implementation-progress.md)(进度对账,PW8 实装本规则) ·
[../p1-interpreter/08-coroutines](../p1-interpreter/08-coroutines.md)(coroutine 单一事实源,§3.1-§3.6 路线 A/B 对比 + §5 yield 不跨 C 边界 + §8.2 主线程不能 yield) ·
[../p2-bridge/04-try-compile-fallback](../p2-bridge/04-try-compile-fallback.md)(升层决策状态机,本文要求扩展 considerPromotion 入口签名) ·
[../p2-bridge/01-profiling](../p2-bridge/01-profiling.md)(profile 采样,本文要求 onBackEdge/onEnter 透传 Thread) ·
[../p4-method-jit](../p4-method-jit/00-overview.md)(P4 native JIT,继承本规则) ·
[../roadmap.md](../roadmap.md)(§4 P3 阶段 + §5 原则 4 coroutine fallback 形状 + §6 锁 5.1) ·
[../../../llmdoc/memory/decisions/2026-06-11-design-review-decisions.md](../../../llmdoc/memory/decisions/2026-06-11-design-review-decisions.md)(§7 维持决策) ·
[../../../llmdoc/memory/doc-gaps](../../../llmdoc/memory/doc-gaps.md)(P3 开工前置确认条目)
