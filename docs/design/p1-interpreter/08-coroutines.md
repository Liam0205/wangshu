# P1 脊柱:协程 / thread 对象 / resume-yield

> 状态:**设计阶段,可实现深度**。本文是 Lua 5.1 **非对称协程**(coroutine)的单一事实源:
> Thread 状态机、resume/yield 在 reentry 模型上的实现、参数/返回值跨 Thread 搬运、`coroutine.*`
> 库语义、协程错误边界、与 GC 的根可达关系、主线程语义、yield-across-C-boundary 的 5.1 限制。
> 上游契约:[05-interpreter-loop](./05-interpreter-loop.md) **§7 是本文全部可行性的地基**——
> §7.1 Lua-call-Lua 用 reentry(不吃 Go 栈)、调用链状态全住 arena 的 CallInfo(§1.2),
> 所以"切协程 = 切 Thread 指针 + 切 frame,不拷 Go 栈"(§7.1 末已点名协程);§7.3 reentry 边界
> (`entryCi` / host→Lua 重入 / `callStatus_fresh`)是 resume 进入 `execute` 的机制;§7.4 `nCcalls`
> /host 帧上限是 yield-across-C-boundary 检测的依据;§1.2 CallInfo 布局、§1.5 栈扩容、§9 错误传播。
> **本文基于 05 的 reentry 模型实现 resume/yield,不引入与之冲突的机制**。
> 值/对象侧:[01](./01-value-object-model.md) §5.6 Thread 布局(`word1 status` / `word2 valueStackRef`
> / `word4 callInfoRef` / `word6 openUpvalRef` / `word7 errorJmp` / `word8 resumeFrom`)是核心数据结构,
> 本文把每个字段的精确用法定死。错误跨 resume 边界:[09](./09-errors-pcall.md) §12(resume 边界 =
> 错误停靠站)已给定稿,本文呼应并展开机制侧。GC 根:[06](./06-memory-gc.md) §5.1 R3/R4/R5。
> coroutine 库 host functions 的清单与注册在 [10-stdlib](./10-stdlib.md)(可能尚在起草,前向引用占位);
> **本文定义协程机制,`coroutine.*` 是机制的消费者**。语言面锁 Lua 5.1(`docs/design/roadmap.md` (§6):
> 不做 5.2+ 的"pcall/metamethod 内 yield",5.1 的 yield-across-C-boundary 限制硬记)。

对应 Go 包:Thread 对象布局在 `internal/object`(承 [01](./01-value-object-model.md) §5.6);
resume/yield 机制、Thread 状态机、跨 Thread 值搬运在 `internal/crescent`(与 [05](./05-interpreter-loop.md)
同包,因 resume = 在 reentry 模型上多起一层 `execute`);`coroutine.create/resume/yield/status/wrap/running`
是 host functions,在 `internal/stdlib`(见 [10-stdlib](./10-stdlib.md))。

---

## 0. 本文在 P1 中的位置与设计张力

协程是 Lua「机制开放」的第三块(与元方法、错误处理并列):它把**控制流的挂起与恢复**暴露成普通可调用值,
脚本可以把"一个能暂停的函数"当数据传递、当迭代器、当生产者-消费者管道。这一点直接决定了本文的核心张力——
**如何在一个 Go 写的解释器里实现"挂起一个 Lua 执行流、稍后恢复"而不背叛 05 的架构纪律**。

05 §7.1 末尾已经给了答案的一半,并**点名协程**:

> **协程切换可行**([08](./08-coroutines.md)):挂起 = 保存当前 frame 回 CallInfo + 切到另一 Thread 的
> CallInfo 链;因为状态全在 arena,切协程不需要拷 Go 栈。

这句话是本文的**起点与约束**:每个协程是一个独立 Thread(独立 arena 值栈 + 独立 CallInfo 链,[01](./01-value-object-model.md)
§5.6),挂起一个协程**不需要保存 Go 栈**——因为它的整条 Lua 调用链根本不在 Go 栈上,而在 arena 的 CallInfo 数组里
(05 §1.2)。这是望舒相对 gopher-lua(用 goroutine + channel 实现协程,每协程一个 Go 栈)的**架构优势的兑现点**,
也是本文最难、最核心的部分(§3)。

本文的张力来自三条约束的夹击:

1. **不背叛 05 的 reentry 模型**。05 §7 已经把"Lua 调用链不吃 Go 栈、host→Lua 才加 Go 栈、`entryCi`/`fresh` 边界、
   `nCcalls` 上限"定死。本文实现 resume/yield **必须复用这套机制**,不能引入第二套独立的栈管理。具体地:resume
   = 在 reentry 模型上"多起一层 `execute` 跑目标 Thread"(类似 05 §7.3 的 host→Lua 重入);yield = 从那层
   `execute` 带信号 `return` 出来(类似 05 §9 的 `*LuaError` 冒泡,但冒泡的是 yield 而非 error)。

2. **必须严格 Lua 5.1**(`docs/design/roadmap.md` (§6))。协程是 5.1 与 5.2+ 差异密集区:5.1 **不允许跨 C-call
   边界 yield**(`attempt to yield across C-call boundary`),也**不允许在 pcall/metamethod 内 yield**(5.2+ 才用
   可恢复的 `lua_pcallk`/`lua_callk` 放宽)。本文**每一处 5.2+ 放宽都显式标注以 5.1 为准**——否则差分基准
   ([12](./12-testing-difftest.md))会因"行为偏移一个版本"与官方 5.1 分叉。

3. **协程是 fallback 形状,但机制必须正确**。`docs/design/roadmap.md` (§5) 原则 4 把 coroutine 列为"不升层、
   永远走解释"的典型形状(P2+ 的编译层遇到含 yield 的函数标记不可编译,走解释)。所以协程**只在 tier-0
   解释器(crescent)实现**,P3/P4/P5 不为它出编译路径。但"不升层"不等于"可以做错"——协程机制是 P1 解释器
   的一等公民,语义必须与官方 5.1 逐字节一致(差分主防线,原则 2)。

> 一句话定位:本文把 05 §7 的 reentry 模型**再叠一层**,实现"挂起/恢复整个 Lua 执行流"。05 定"Lua 调用怎么
> 不吃 Go 栈",本文定"基于这一点,协程切换怎么不拷 Go 栈"。Thread 是这一切的载体,resume/yield 是控制权的
> 显式转移,`coroutine.*` 是把这套机制包装成脚本可见的库。

---

## 1. 协程模型总览

### 1.1 非对称协程(asymmetric / stackful)

Lua 协程是**非对称的、有栈的**(asymmetric stackful coroutine):

- **非对称**:控制权转移是**显式且有方向的**——`resume` 进入一个协程(调用方 → 协程),`yield` 让出回
  **恢复它的那一方**(协程 → 调用方)。协程不能任意跳到"某个协程",只能 yield 回它的 resumer。这与**对称
  协程**(symmetric,任意 `transfer(co)` 到任一协程)相对。Lua 选非对称,因为它更简单、更安全(控制流是
  树状的 resume 链,§7),且足以表达生成器/迭代器/协作多任务。
- **有栈**:协程可以在**任意嵌套深度**的函数调用中 yield(只要不跨 C-call 边界,§5),不只在协程主函数顶层。
  这要求协程保存**整条调用栈**(不只一个 PC)——在望舒里就是保存整条 CallInfo 链 + 值栈(都在 arena)。
  与**无栈协程**(stackless,只能在顶层 yield,如 C# 的 `yield return`)相对。

### 1.2 每个协程一个独立 Thread

**每个协程 = 一个独立的 Thread 对象**([01](./01-value-object-model.md) §5.6),拥有:

- **独立的值栈**(`valueStackRef`,arena 内的 `Value[stackCap]`):该协程的所有寄存器/局部/临时值。
- **独立的 CallInfo 链**(`callInfoRef`,arena 内的 `CallInfo[ciCap]`):该协程当前挂起/运行时的整条 Lua 调用链。
- **独立的开放 upvalue 链**(`openUpvalRef`):该协程栈上变量的开放 upvalue。
- **独立的状态机字段**(`status` / `resumeFrom` / `errorJmp`)。

两个协程的值栈与 CallInfo 链**完全隔离**(各自 arena 分配,互不重叠)。resume/yield 在它们之间转移控制权时,
**值要在两个 Thread 的值栈之间显式搬运**(§4)——这是"独立栈"的代价,也是协程隔离的体现。

### 1.3 与 OS 线程无关(协作式,单 OS 线程内切换)

**Lua 协程不是 OS 线程,也不是 goroutine**。它是**协作式**的:

- 同一时刻只有**一个**协程在跑(running 态,§2),其余挂起(suspended)或等待(normal,§2)。
- 切换点是**显式的**(resume/yield),不是抢占式的(无时间片、无调度器)。
- 单 OS 线程(单 goroutine,见 §3 路线 B)内完成所有协程切换。**无并发、无数据竞争、无锁**——这正是协程
  相对线程的简单性来源,也让望舒的 STW GC(06 §7.3)在协程场景下依然"天然无需停顿协调"(GC 触发时只有一个
  协程在 Alloc,其余协程的栈是静止的根,§6)。

> **与 gopher-lua 的关键差异(架构优势点)**:gopher-lua 用 **goroutine + channel** 实现协程(每个协程跑在
> 自己的 goroutine 里,resume/yield 用 channel 握手阻塞)。这意味着 ① 每协程一个 Go 栈(内存开销 + Go
> 调度器介入);② 与"不拷 Go 栈"的纯粹性有张力(goroutine 各有独立 Go 栈,切换由 Go runtime 调度)。望舒因
> **调用链全在 arena**(05 §1.2),可以选**单 goroutine + 显式栈切换**(§3 路线 B):切协程 = 切 Thread
> 指针 + 重建 frame,**不开 goroutine、不拷 Go 栈**。这是 roadmap §2「栈移动税:不持有指向 Go 栈的指针」在
> 协程场景的最终兑现——协程的 Lua 调用栈不在 Go 栈上,所以协程切换与 Go 栈彻底解耦。

### 1.4 关键优势:切协程不拷 Go 栈(扣 05 §7)

把 05 §7.1 的链条补全,落到协程:

| 若 Lua 调用链住 Go 栈(gopher-lua 风格的假想纯解释) | 望舒:Lua 调用链住 arena CallInfo(05 §1.2) |
|---|---|
| 协程挂起 = 保存整个 Go 栈(或每协程一个 goroutine 让 Go runtime 保存) | 协程挂起 = 把当前 frame 存回 CallInfo(05 §1.3 reloadFrame 的逆),Go 栈不参与 |
| 协程恢复 = 恢复 Go 栈 | 协程恢复 = 从目标 Thread 的栈顶 CallInfo 重建 frame(reloadFrame),继续 `execute` 循环 |
| 1000 层深的协程挂起 = 1000 层 Go 栈被保存 | 1000 层深的协程 = 1000 条 CallInfo(arena),Go 栈深度恒为 host→Lua 重入层数(§3.4) |
| 协程数 = goroutine 数(各占 Go 栈内存 + 调度) | 协程数 = Thread 对象数(各占 arena 值栈 + CallInfo,无 goroutine) |

**结论(本文的核心不变式)**:**协程切换不拷 Go 栈、不开 goroutine**。切换的全部工作是:① 把当前协程的 frame
存回它的 CallInfo;② 切换"当前 Thread"指针;③ 从目标协程的 CallInfo 重建 frame。三步都是 arena 内的字段读写
+ Go 局部变量赋值,**O(1)**,与协程的 Lua 调用深度无关。这是 05 §7.1 末"切协程不需要拷 Go 栈"的精确实现。

---

## 2. Thread 状态机

### 2.1 四个状态(01 §5.6 word1)

每个 Thread 的 `status`([01](./01-value-object-model.md) §5.6 word1 低 8 位)有四个值,对齐 Lua 5.1
`coroutine.status` 的返回字符串:

| status 值 | 含义 | `coroutine.status(co)` 返回 | 谁在跑这个 Thread |
|---|---|---|---|
| **`suspended`** | 已创建未启动,或 yield 后挂起,等待被 resume | `"suspended"` | 无(挂起,栈是静止的根) |
| **`running`** | **正在执行**(它的 `execute` 循环在 Go 栈顶活跃) | `"running"` | 当前唯一活跃的协程 |
| **`normal`** | 它 resume 了另一个协程,自己在等那个协程 yield/返回 | `"normal"` | 间接(它在 resume 链上,但控制权在更深的协程) |
| **`dead`** | 主函数已返回,或内部发生未捕获错误 | `"dead"` | 无(不可再 resume) |

> **`normal` 态的精确含义(易混)**:协程 A resume 协程 B 后,A 变 `normal`(不是 `suspended`!),B 变
> `running`。A 是"活的但暂时让出了控制权,在 resume 链上等 B"。区别于 `suspended`(A 主动 yield 了、不在
> 当前 resume 链上)。`normal` 协程**不能被 resume**(它已在调用栈上,resume 它会形成环,§2.3)。这是 Lua
> 5.1 的精确语义:同一时刻,resume 链上每个协程是 `normal`,链尾正在跑的是 `running`,链外挂起的是
> `suspended`。

### 2.2 状态转移表

| 触发事件 | 前置状态 | 后置状态 | 同时影响 | 定义 |
|---|---|---|---|---|
| `coroutine.create(f)` | —(新建) | **suspended** | 新 Thread,空 CallInfo 链,f 待作为首帧 | §4.1 |
| `coroutine.resume(co)` 成功进入 | co=suspended | co=**running** | **resumer 变 normal**;`co.resumeFrom = resumer`(§7) | §4.2 |
| `coroutine.yield(...)` | 当前=running | 当前=**suspended** | resumer(normal)恢复为 **running**;控制权回 resumer | §4.3 |
| 协程主函数正常 return | running | **dead** | resumer 恢复为 running,收到返回值;`co.resumeFrom` 清 | §4.4 |
| 协程内未捕获错误 | running | **dead** | resumer 恢复为 running,收到 (false, err);(09 §12) | §4.5 |
| resume 一个 normal/running/dead 协程 | (非法) | 不转移 | resume 返回 (false, err),报错措辞见 §2.3 | §2.3 |

**状态转移图**(文字版):

```
                    create
                      │
                      ▼
                 ┌─────────┐   resume(成功)    ┌─────────┐
                 │suspended│ ─────────────────► │ running │
                 │         │ ◄───────────────── │         │
                 └─────────┘   yield(让出)      └─────────┘
                      ▲                            │   │
                      │ (resumer 在 resume 链上)    │   │ 主函数 return / 未捕获错误
   resumer 视角:     │                            │   ▼
   running ──resume──►│ normal ◄──────────────────┘ ┌──────┐
            ◄─yield───┘ (等被 resume 的协程)          │ dead │
                                                     └──────┘
                                                  (不可再 resume)
```

### 2.3 非法转移与错误措辞

resume 的目标协程必须是 `suspended`。其余三态 resume 都报错(resume 返回 `(false, errmsg)`,**不抛**——
resume 本身是 protected 边界,§5.2):

| 目标 status | 报错措辞(Lua 5.1) | 原因 |
|---|---|---|
| `dead` | `cannot resume dead coroutine` | 协程已结束/出错,栈状态作废,无法恢复 |
| `running` | `cannot resume non-suspended coroutine` | 不能 resume 正在跑的协程(它就是当前在跑的,resume 自己无意义) |
| `normal` | `cannot resume non-suspended coroutine` | 它在 resume 链上(已被更外层 resume),再 resume 会形成环 |

> **`running` 与 `normal` 共用 `non-suspended` 措辞**:Lua 5.1 对"目标非 suspended"统一报
> `cannot resume non-suspended coroutine`,只有 dead 单独报 `cannot resume dead coroutine`。**精确措辞
> 待 [12](./12-testing-difftest.md) 差分核对**(冠词/标点),本文给骨架不编造(§9 呼应 09 的措辞纪律)。
> 防 resume 环是非对称协程"控制流是树状 resume 链"的保证——`normal` 态的存在就是为了让 resume 能检测出
> "你想 resume 的协程其实是你的祖先"(§7.2)。

---

## 3. 核心机制:resume/yield 如何在 reentry 模型上实现(本文最难、最核心)

### 3.1 关键设计抉择:两条实现路线

望舒解释器主循环 `execute` 是一个 Go 函数(05 §2.3):`func (vm *VM) execute(th *Thread) (err *LuaError)`。
协程切换("挂起一个 `execute` 跑的 Lua 流、恢复另一个")有两条根本不同的实现路线,**必须分析并选定**:

#### 路线 (A):每协程一个 goroutine + channel 同步

每个协程跑在自己的 goroutine 里;resume/yield 用一对 channel 握手:

```
co 的 goroutine:        for { <-resumeCh;  跑 execute(co);  yieldCh <- 结果 }
resumer(main goroutine): resumeCh <- args;  结果 := <-yieldCh
yield(实现):            yieldCh <- yield值;  args := <-resumeCh   // 阻塞,goroutine 挂起在这
```

- **优点**:实现**简单直白**——yield 就是 channel 发送后阻塞接收,Go runtime 自动保存该 goroutine 的整个
  执行状态(包括 Go 栈)。不需要手动管"yield 信号如何穿过多层 Lua 帧"。这是**很多纯 Go Lua 的实际选择**
  (gopher-lua 即用此法)。
- **缺点**:① **每协程一个 goroutine**——goroutine 有 Go 栈(初始 2-8KB,可增长)+ Go 调度器介入(park/ready)。
  创建 1 万个协程 = 1 万个 goroutine 的内存与调度压力。② **与"不拷 Go 栈"的纯粹性有张力**:goroutine 各有
  独立 Go 栈,切换由 Go runtime 调度(虽不是"拷"栈,但 goroutine 切换本身有上下文成本,且 Go 栈内存随协程数
  线性增长)。③ **GC 协调复杂化**:多 goroutine 意味着多个潜在的并发 Alloc 点,STW GC(06 §7.3)的"天然无需
  停顿协调"前提被打破——必须确保 GC 时只有一个 goroutine 在动 arena(需额外同步)。④ **与 05 的 reentry
  模型不共享机制**:A 的栈管理完全交给 Go runtime,而 05 §7 精心设计的"调用链在 arena、host→Lua 才加 Go 栈"
  那套机制在 A 下被旁路了(每协程的 Go 栈又回来了),架构上是两套并存。

#### 路线 (B):单 goroutine + 显式栈切换/重入(reentry 加一层)

所有协程跑在**同一个 goroutine** 里;resume = 在当前 Go 栈上**新起一个 `execute` 循环**跑目标 Thread(类似
05 §7.3 的 host→Lua 重入,加一层 Go 栈帧);yield = 从那个 `execute` **带信号 return 出来**(yield 信号像
05 §9 的 `*LuaError` 一样,一路冒泡 return 到 resume 的调用点):

```
resume(co, args):                      // host function(coroutine.resume)
  把 args 搬到 co 的值栈(§4.2)
  co.status = running; resumer.status = normal; co.resumeFrom = resumer
  vm.curThread = co                    // 切"当前 Thread"指针
  signal := vm.execute(co)             // ★ 在当前 Go 栈上新起一层 execute 跑 co(加一层 Go 栈)
  // execute 返回有三种可能(§3.3 的 executeSignal):
  //   - 正常返回(co 主函数 RETURN 到 entryCi):co 结束 → dead,收返回值
  //   - yield 信号:co 内某层 yield,信号一路 return 出 co 的 execute
  //   - error:co 内未捕获错误
  vm.curThread = resumer; resumer.status = running
  根据 signal 组装 resume 的返回值(§4.3/§4.4/§4.5)

yield(args):                           // host function(coroutine.yield)
  把 args 搬到 resumer 能取到的地方(§4.3)
  把当前 frame 存回 co 的栈顶 CallInfo(§9 saveFrame)
  co.status = suspended
  return yieldSignal                   // ★ 从 host 触发"execute 带 yield 信号 return"(§3.3)
```

- **优点**:① **不开 goroutine、不拷 Go 栈**——完全兑现 §1.4 的核心优势。协程数 = Thread 对象数(arena),
  无 goroutine 内存。② **与 05 的 reentry 模型同源**:resume 就是 05 §7.3 的"host→Lua 重入"再叠一层
  (host `coroutine.resume` 调 Lua = 起一个 `execute`),`entryCi`/`callStatus_fresh` 直接复用;yield 就是
  05 §9 的冒泡机制换个信号类型。**一套机制,不是两套**。③ **GC 纪律不变**:单 goroutine,STW GC 的"天然无需
  停顿协调"前提保持(06 §7.3),挂起协程的栈是静止的根(§6)。④ **与未来 trace JIT 对齐**:trace JIT(P5)
  录制/deopt 需要精确掌控执行流的挂起/恢复点,B 的"显式信号冒泡"模型比 A 的"goroutine 黑盒"更可控(§3.6)。
- **缺点**:① **yield 信号要"穿过" `execute` 的 Go 调用栈回到 resume**——yield 跨越多层 Lua 帧时,要把 yield
  信号像错误一样**一路冒泡 return**(§3.3),实现比 A 的"channel 阻塞"复杂(但与 05 §9 的 `*LuaError` 冒泡
  同构,复杂度可控)。② **yield 不能跨 host call 边界**(§5)——因为 host 是真 Go 栈帧,yield 的信号冒泡无法
  穿过它(Go 函数无法"从中间 return 一个信号让调用它的 Lua 帧继续")。这恰好是 Lua 5.1 的硬限制
  (`attempt to yield across C-call boundary`),B **天然吻合** 5.1 语义(A 反而要额外检测才能模拟这个限制)。

### 3.2 P1 选定:路线 (B),论证

**P1 选定路线 (B):单 goroutine + 显式栈切换/重入(reentry 加一层 execute,yield 信号冒泡)。**

论证(基于 05 的架构倾向,这是关键决策):

1. **架构纯粹性 / 与 05 的一致性是决定性因素**。05 §7 整套机制("Lua 调用链不吃 Go 栈、调用链全在 arena
   CallInfo、host→Lua 才加 Go 栈、`entryCi`/`fresh` 边界、`nCcalls` 上限")**就是为协程铺的路**——05 §7.1
   末明确点名"切协程不需要拷 Go 栈"。路线 (B) 是这套机制的**直接延伸**(resume = host→Lua 重入再叠一层,
   yield = `*LuaError` 式冒泡换信号),**不引入任何与 05 冲突的新机制**(扣合任务硬约束"基于 05 的 reentry
   模型实现 resume/yield,不能引入与之冲突的机制")。路线 (A) 则在 05 的机制之外**另起一套**(goroutine 各自
   的 Go 栈),架构上两套栈管理并存,且作废了 05 §7 为协程铺的路。

2. **yield-across-C-boundary 的 5.1 限制,(B) 天然吻合,(A) 反而别扭**。Lua 5.1 **禁止**跨 C-call 边界 yield
   (§5)。路线 (B) 下这是**免费的正确性**——yield 信号靠 `return` 冒泡,遇到 host(真 Go 帧)自然穿不过去
   (Go 函数不能从中间 return 一个 yield 信号让上层 Lua 帧续跑),检测一下 `nCcalls`/host 帧标记就能给出
   `attempt to yield across C-call boundary`(§5.2)。路线 (A) 下 goroutine 可以阻塞在任意深度(包括 host 调
   Lua 的深处),反而要**额外逻辑**去模拟"5.1 不许在这 yield"的限制——否则会实现出 5.2+ 才有的"跨 C 边界
   yield",与 5.1 差分失败。**(B) 让 5.1 限制成为机制的自然结果,(A) 让它成为额外负担**。

3. **GC 与并发简单性**。(B) 单 goroutine,06 §7.3 的"STW GC 天然无需停顿协调"前提保持;挂起协程的栈是静止
   的根(§6),无数据竞争。(A) 多 goroutine,要么用全局锁串行化 arena 访问(损失 goroutine 的意义),要么面对
   并发 GC 的复杂度(P1 明确不做,06 §9.4 写屏障 P1 空实现)。

4. **务实完成成本可控**。(B) 的难点(yield 信号冒泡)与 05 §9 的 `*LuaError` 冒泡**同构**——05 已经把
   "信号一路 return 出 execute、protected 边界捕获"的机制做出来了,yield 只是**复用同一冒泡通道、换一个信号
   类型**(§3.3 把 `execute` 的返回值从 `*LuaError` 泛化为 `executeSignal`)。所以 (B) 的增量实现成本不是
   "从零造栈切换",而是"在已有冒泡机制上加一个 yield 分支"。

> **路线 (A) 作为备选与演进讨论**:本文承认 (A) **实现更简单、是 gopher-lua 等的实际选择**,若 (B) 的 yield
> 冒泡在工程上遇到意外复杂度(如某些 stdlib 迭代器的 yield 路径难以纯 return 表达),(A) 是**兜底**。但 (A)
> 的引入会**作废 05 §7 为协程铺的架构投资**,且与 P5 trace JIT 的"精确掌控挂起点"方向相悖(§3.6),故**P1
> 定 (B),(A) 仅作为风险兜底记入缺口(§11)**。这是基于"05 的架构倾向 + 5.1 限制天然吻合 + GC 简单性"的综合
> 判断,**架构纯粹性优先于实现便利性**(与 05 §9.1 选"显式错误返回而非 panic"同一价值取向:契合 reentry
> 模型 > 实现省事)。

### 3.3 (B) 的完整机制:`execute` 的三态返回与 yield 冒泡

路线 (B) 要求 `execute` 的返回值从 05 §2.3 的 `(err *LuaError)` **泛化**为一个三态信号(本文对 05 §2.3 签名的
协程侧扩展,记入对 05 的回填请求 §10):

```go
// internal/crescent —— execute 的返回信号(泛化 05 §2.3 的 *LuaError 返回)
type executeSignal struct {
    kind  signalKind   // sigReturn / sigYield / sigError
    err   *LuaError    // kind==sigError 时有效(05 §9.2)
    // 返回值/yield 值不放这里——它们在 Thread 的值栈上(§4),由 resume 取
}
// signalKind:
//   sigReturn —— co 主函数 RETURN 退到 entryCi(05 §7.2 returnTerminate),co 正常结束
//   sigYield  —— co 内 coroutine.yield 触发,执行流要挂起并把控制权交还 resumer
//   sigError  —— co 内未捕获错误(05 §9 冒泡到 entryCi)

// 主循环签名扩展(05 §12 的 execute,协程侧):
func (vm *VM) execute(th *Thread) executeSignal {
    entryCi := th.ciTop          // 05 §7.3:reentry 边界(resume 进来时记的)
    f := vm.loadTopFrame(th)
    code := f.proto.Code
    for {
        i := code[f.pc]; f.pc++
        switch bytecode.Op(i) {
        // ... 05 §12 的所有 opcode ...

        case bytecode.RETURN:
            switch vm.doReturn(f, i) {
            case returnToCaller:  code = f.proto.Code
            case returnTerminate: return executeSignal{kind: sigReturn}  // 退到 entryCi → 正常结束
            }

        case bytecode.CALL:
            switch vm.doCall(f, i) {
            case callEnteredLua:  code = f.proto.Code
            case callReturnedHost:
                // ★ host 可能是 coroutine.yield → callHost 返回特殊的 callYield(§3.4)
            case callYield:       return executeSignal{kind: sigYield}   // ★ yield 信号冒泡出 execute
            case callError:       return executeSignal{kind: sigError, err: vm.pendingErr}
            }
        // ... 其余可能出错的 case 把 throw 改为 return executeSignal{sigError, e} ...
        }
    }
}
```

**yield 信号的冒泡路径(本文核心,与 05 §9 的 *LuaError 冒泡同构)**:

```
                    co 的 Lua 调用链(全在 arena CallInfo,05 §1.2)
                    main → f1 → f2 → ... → fN(深 N 层 Lua 帧)
                                            │
        fN 里执行到 coroutine.yield(...)(它是 host function)
                                            │
        ① yield host 把当前 frame 存回 fN 的 CallInfo(§9 saveFrame),设 co.status=suspended,
           返回特殊信号 callYield(经 callHost,§3.4)
                                            │
        ② 主循环 case CALL 收到 callYield → return executeSignal{sigYield}
           ★ 直接 return 出 execute 这个 Go 函数 —— 但 co 的 CallInfo 链【原封不动】留在 arena!
             (不像正常 RETURN 会弹 CallInfo;yield 只是"暂停",整条链保留以便恢复)
                                            │
        ③ execute 返回到它的调用者 —— 即 resume host 里的 `signal := vm.execute(co)` 那一行
                                            │
        ④ resume host 看到 signal.kind==sigYield:
           - 切回 resumer(vm.curThread = resumer,resumer.status=running)
           - 从 co 的值栈取 yield 的值,作为 resume 的返回值(§4.3)
           - resume 返回 (true, yield值...)
```

**关键对比 yield vs RETURN(都从 execute return,但语义相反)**:

| | 正常 RETURN(05 §7.2) | yield(本文) |
|---|---|---|
| CallInfo 链 | **弹掉**当前帧的 CallInfo(`popCallInfo`) | **保留**整条 CallInfo 链(协程要恢复) |
| 退到 entryCi | `returnTerminate` → execute 返回 `sigReturn`(协程结束→dead) | `sigYield` → execute 返回但 ciTop 不变(协程挂起→suspended) |
| 当前 frame | 丢弃(帧结束) | **存回 CallInfo**(§9 saveFrame),恢复时 reloadFrame 重建 |
| 下次进入 | 不会(帧已结束) | 下次 resume 从存回的 CallInfo 重建 frame,**从 yield 的下一条指令继续** |
| status 变化 | running → dead | running → suspended |

> **yield 冒泡为什么不弹 CallInfo**:正常 RETURN 是"这一帧干完了,弹掉它"。yield 是"这一帧(及它的整条
> 调用链)暂停,稍后从原地继续"。所以 yield 信号冒泡出 `execute` 时,**co 的 CallInfo 链完整保留在 arena**
> ——这正是"有栈协程能在任意深度 yield"的物理实现:整条栈(CallInfo 链 + 值栈)冻结在 arena,下次 resume
> 解冻。"从 yield 的下一条指令继续"靠 §9 的 saveFrame/reloadFrame(把 yield 时的 pc 存回栈顶
> CallInfo,恢复时重建)。

### 3.4 yield 如何从 host 触发 `execute` 的 yield 返回(承 05 §7.6 / §9.4)

`coroutine.yield` 是 host function(05 §7.6 `HostFn`),它**不能直接 return 出 `execute`**(host 在 Go 栈上,
execute 在它下面,05 §7.3)。这与 05 §9.4 `error` 经 `raise` 触发错误冒泡是**完全对称的机制**——yield 经一个
特殊信号让 `callHost` 把控制权以 `sigYield` 形式交还 execute 的冒泡:

```go
// internal/stdlib —— coroutine.yield 的 host 实现(机制侧,语义见 §4.3)
func hostCoroutineYield(vm *VM, th *Thread) int {
    // ① 检查:当前是否可 yield(不在主线程、不跨 C 边界,§5)
    if !vm.canYield(th) {
        vm.raise(vm.internString("attempt to yield across C-call boundary"))  // 或主线程措辞,§5.2
        return 0   // 不可达(raise 走 callError 路径)
    }
    // ② yield 的参数(arg(1..nargs))已在 th 值栈上;标记"待 yield 的值"区间(§4.3)
    //    yield 不在这里搬值到 resumer —— 搬运在 resume host 侧做(§4.3),这里只标记区间。
    vm.pendingYield = yieldInfo{base: th.argBase(), n: th.nargs()}   // yield 值在栈上的位置
    // ③ 设特殊信号:callHost 检查它,返回 callYield(而非正常 callReturnedHost)
    vm.yieldRequested = true
    return 0   // host 返回值个数无意义(yield 不"返回"给 Lua 调用者,而是挂起)
}

// callHost(05 §7.6)的协程侧扩展:同步调 host 后,除了检查 pendingErr(05 §9.4),
// 还检查 yieldRequested:
func (vm *VM) callHost(f *frame, a, nargs, nresults int) callResult {
    // ... 05 §7.6 的设置 host 帧、压 host CallInfo ...
    nret := vm.hostFns[hostFnIDOf(...)](vm, th)   // 同步 Go 调用(进 Go 栈)
    if vm.pendingErr != nil {                      // 05 §9.4:host 调了 raise
        return callError
    }
    if vm.yieldRequested {                          // ★ 本文:host 调了 coroutine.yield
        vm.yieldRequested = false
        // 注意:yield 时【不弹 host 帧的 CallInfo】常规清理也要小心 —— 见 §5.3 的"yield 时栈状态"
        return callYield                            // 主循环 case CALL 收到 → return sigYield
    }
    // ... 正常:moveResults、弹 host CallInfo、return callReturnedHost ...
    return callReturnedHost
}
```

> **机制对称性(yield ↔ error,都经 host 信号 + callHost 检查)**:05 §9.4 定下"host 经 `raise` →
> `pendingErr` → `callHost` 返回 `callError` → 主循环冒泡"。本文给 yield 一条**完全平行**的通道:
> "host 经 `yield`(设 `pendingYield`/`yieldRequested`)→ `callHost` 返回 `callYield` → 主循环
> `return sigYield` 冒泡"。两条通道共享"host 不直接 return 出 execute、经 callHost 转信号"的纪律
> (05 §7.6)。**这是路线 (B) 复用 05 机制的最直接体现**——不造新栈管理,只在已有的 host→信号→冒泡通道上
> 加一个 yield 信号类型。

### 3.5 resume 进入 execute:与 05 §7.3 reentry 边界的关系

`coroutine.resume` 是 host function,它要"跑目标协程的 Lua 流",这就是 05 §7.3 的 **host→Lua 重入**——起一个
新的 `execute` Go 栈帧。但与普通 host→Lua 重入(如 pcall)有**两点关键不同**:

1. **切 Thread**:普通 host→Lua 重入(pcall 调被保护函数)是在**同一个 Thread** 上继续;resume 是**切到另一个
   Thread**(co)跑它的 CallInfo 链。所以 resume 前要 `vm.curThread = co`,`execute(co)` 跑的是 co 的栈。
2. **entryCi 在 co 的链上**:`execute(co)` 进入时记的 `entryCi = co.ciTop`(05 §7.3)。对**首次 resume**,co 的
   CallInfo 链只有待启动的主函数帧(§4.1 create 时压的 fresh 帧);对**后续 resume**(从 yield 恢复),co 的
   CallInfo 链是上次 yield 时冻结的整条链,`entryCi` 记当前 ciTop,co 从栈顶 CallInfo(yield 点)恢复继续。

```go
// internal/stdlib —— coroutine.resume 的 host 实现(机制侧,语义见 §4.2)
func hostCoroutineResume(vm *VM, th *Thread) int {
    co := asThread(th.arg(1))            // 目标协程(arg(1) 必须是 thread,否则报错)
    // ① 状态检查(§2.3):co 必须 suspended
    if co.status != statusSuspended {
        th.pushBool(false)
        th.push(vm.internString(resumeErrMsg(co.status)))  // "cannot resume dead/non-suspended coroutine"
        return 2
    }
    // ② 搬 resume 的参数(arg(2..))到 co 的值栈(§4.2,跨 Thread 拷贝)
    transferArgs(th /*from resumer*/, co /*to coroutine*/, th.argsFrom(2))
    // ③ 切状态 + 切 Thread + 建 resume 链(§7)
    resumer := th
    co.status = statusRunning
    resumer.status = statusNormal
    co.resumeFrom = refOf(resumer)        // 01 §5.6 word8:resume 链
    vm.curThread = co
    vm.nCcalls++                          // 05 §7.4:host→Lua 重入,C 栈深度 +1(§5.3 防 C 栈爆)
    // ④ ★ 在当前 Go 栈上新起一层 execute 跑 co(reentry 加一层,§3.1 路线 B)
    signal := vm.execute(co)
    // ⑤ execute 返回:co yield 了 / co 结束了 / co 出错了 —— 切回 resumer,组装返回值
    vm.nCcalls--
    vm.curThread = resumer
    resumer.status = statusRunning
    return vm.assembleResumeResult(resumer, co, signal)   // §4.3/§4.4/§4.5
}
```

### 3.6 与 trace JIT / 未来对齐(路线 B 的演进价值)

路线 (B) 的"显式信号冒泡 + 调用链在 arena"模型,与未来 P5 trace JIT(`docs/design/roadmap.md`,
[../p5-trace-jit/00-overview.md](../p5-trace-jit/00-overview.md))的需求**同向**:

- **trace 录制需要精确掌控执行流的挂起/恢复点**。(B) 下 yield/resume 是**显式的、可观测的信号边界**
  (`sigYield`/`sigReturn`),trace 录制器能精确知道"执行流在哪挂起了"。(A) 下协程挂起是 goroutine 阻塞在
  channel 上的**黑盒**,trace 录制难以介入。
- **deopt 着陆(roadmap §5 原则 1:解释器是 deopt 着陆点)需要重建解释器状态**。(B) 下协程状态全在 arena
  (CallInfo + 值栈),deopt 时直接 reloadFrame 即可恢复解释执行;(A) 下若编译层涉及协程(虽 P2+ 标协程不
  升层,但 deopt 的状态重建模型仍应统一),goroutine 模型与 arena 模型不一致。
- **协程标"不升层"(roadmap §5 原则 4)**:含 yield 的函数 P2+ 标记不可编译、走解释。这意味着协程**永远在
  tier-0(crescent)跑**——(B) 让协程机制完全活在解释器的 reentry 模型里,与"永不升层"自洽;(A) 的 goroutine
  模型是解释器之外的东西,与分层 VM 的"统一值/执行模型"理念有缝。

> 故 (B) 不只是 P1 的务实选择,更是**与分层 VM 长期架构对齐**的选择。这呼应任务"把 B 作为与 trace JIT / 未来
> 对齐的演进方向讨论"——但本文判断 (B) **足够务实可直接完成**(因与 05 §9 冒泡同构,增量成本可控),故 P1
> **直接选 B**,而非"先 A 后 B"。若 yield 冒泡遇意外复杂度,(A) 兜底(§11 缺口)。

---

## 4. resume 的参数 / 返回值传递(跨两个 Thread 值栈的精确搬运)

协程的值传递是**跨两个独立 Thread 值栈的拷贝**(§1.2:每协程独立值栈)。四种搬运场景,精确定义每一种:

### 4.1 create:协程主函数与首帧

`coroutine.create(f)` 创建一个 suspended 协程,**不传参、不执行**(参数在首次 resume 时才传):

```go
// internal/stdlib —— coroutine.create(f) → thread
func hostCoroutineCreate(vm *VM, th *Thread) int {
    f := th.arg(1)
    if !isLuaClosure(f) && !isHostClosure(f) {       // f 必须是函数(5.1:create 接受任意函数)
        vm.raise(...) // "bad argument #1 to 'create' (function expected)"
        return 0
    }
    co := vm.newThread()                              // 分配新 Thread(arena,01 §5.6)→ safepoint
    //   新 Thread:status=suspended,独立值栈(初始小容量),空 CallInfo 链,openUpvalRef=0,resumeFrom=0
    // 把 f 作为 co 的"待启动主函数"压栈:co.valueStack[0] = f,并压一个 fresh CallInfo(05 §7.3)
    //   标 callStatus_fresh —— 这是 co 的 entryCi,首次 resume 时 execute 从这里启动 f。
    setupMainFrame(co, f)                              // §4.2 首次 resume 详述
    th.push(value.MakeGC(value.TagThread, refOf(co)))
    return 1
}
```

- **create 不执行 f**:只是把 f 放到 co 的栈上、压好 fresh 帧,co 处于 suspended。f 的执行推迟到首次 resume。
- **co 的初始栈很小**:create 时只需容纳 f + 首帧的 MaxStack;后续随 f 执行按需扩容(05 §1.5,在 co 的栈上)。

### 4.2 首次 resume:参数 → 主函数的参数

首次 resume(co 从未跑过)时,resume 的参数 `arg(2..)` 成为**协程主函数 f 的参数**:

```
首次 resume(co, a, b, c):
  co 的栈当前:[f]                            (create 时放的主函数,co.valueStack[0])
  ① transferArgs:把 a,b,c 从 resumer 栈拷到 co 栈,放在 f 之后:
     co 的栈变:[f, a, b, c]
  ② execute(co) 从 co 的 entryCi(create 压的 fresh 帧)启动:
     - enterLuaFrame 把 a,b,c 当作 f 的实参(05 §1.4 adjustVarargs):
       f 的形参 = a,b,c(多退少补,按 f 的 NumParams);base 指向 f 的 R0。
     - f 从第一条指令开始跑(pc=0)。
```

**与普通函数调用的一致性**:首次 resume 启动主函数,等价于"在 co 的栈上调用 `f(a,b,c)`"——复用 05 §1.4
`enterLuaFrame` + §7.1 `adjustVarargs` 的形参搬移逻辑,只是发生在 co 的栈上、由 resume host 触发 execute。

### 4.3 后续 resume / yield:yield 的参数 → resume 的返回值;resume 的参数 → yield 的返回值

协程跑起来后,resume 与 yield 形成**双向值传递**:

```
            resumer 侧                                      co 侧
  resume(co, x, y) ──── x,y 搬到 co 栈 ────►  yield(...) 处恢复:
                                              yield(p, q) 的返回值 = x, y
                                              (co 从 yield 的下一条继续,收到 x,y)
            ◄──── p,q 搬回 resumer 栈 ────  yield(p, q):
  resume 返回 (true, p, q)                    把 p,q 标记为 yield 值,挂起
```

**精确搬运(两个方向)**:

| 方向 | 触发 | 源 | 目标 | 搬运点 |
|---|---|---|---|---|
| **resumer → co** | `resume(co, x, y)`(后续) | resumer 栈的 `arg(2..)` | co 栈:成为**上次 yield 调用的返回值**(放在 yield 所在帧期望接收返回值的寄存器) | resume host 的 `transferArgs`(§3.5 step②) |
| **co → resumer** | `yield(p, q)` | co 栈的 `arg(1..)`(yield 的参数) | resumer 栈:成为 **resume 的第 2..个返回值** | resume host 的 `assembleResumeResult`(§3.5 step⑤),从 co 的 `pendingYield` 区间取 |

```go
// resume host 的 step⑤(§3.5):execute 返回 sigYield 后,把 co 的 yield 值搬回 resumer
func (vm *VM) assembleResumeResult(resumer, co *Thread, signal executeSignal) int {
    switch signal.kind {
    case sigYield:
        // co.pendingYield 记了 yield 值在 co 栈的 [base, base+n)(§3.4 hostCoroutineYield step②)
        resumer.pushBool(true)                                  // resume 第 1 返回值:true
        n := transferValues(co, resumer, co.pendingYield.base, co.pendingYield.n)  // 跨 Thread 搬
        return 1 + n                                            // (true, yield值...)
    case sigReturn:
        return vm.assembleReturnResult(resumer, co)             // §4.4
    case sigError:
        return vm.assembleErrorResult(resumer, co, signal.err)  // §4.5
    }
}

// 而 resume 的参数如何成为 yield 的返回值:在【下次】resume 的 transferArgs(§3.5 step②)里,
// 把 arg(2..) 搬到 co 栈"yield 所在帧期望接收返回值的寄存器"。这要求 yield 挂起时记下
// "yield 这条 host 调用(CALL coroutine.yield)期望几个返回值、落在哪个寄存器"——
// 即 yield 所在帧的 CALL 指令的 R(A)/C(05 §7.2 moveResults 的目标)。恢复时按此回填。
```

> **yield 的返回值落点(关键细节)**:`coroutine.yield(p,q)` 在 Lua 代码里写作 `local x,y = coroutine.yield(p,q)`。
> 它是一条 CALL 指令(调 host `coroutine.yield`)。挂起时,这条 CALL 期望的返回值寄存器(R(A..)、个数由 C 定,
> 05 §7.2)被记在 yield 帧的 CallInfo 里。**下次 resume** 时,resume 的参数 `x,y` 被搬到这些寄存器——于是
> `coroutine.yield(p,q)` "返回" `x,y`。这把"yield 让出 / resume 恢复"的值传递,统一进 05 §7.2 的"函数调用
> 返回值搬运"模型:yield 就是一个"返回值由下次 resume 提供"的特殊调用。

### 4.4 协程正常结束:主函数返回值 → resume 的返回值

协程主函数 RETURN(退到 entryCi,05 §7.2 `returnTerminate` → `sigReturn`)时,**主函数的返回值**成为 resume 的
返回值:

```go
func (vm *VM) assembleReturnResult(resumer, co *Thread) int {
    co.status = statusDead                  // §2.2:主函数返回 → co 变 dead
    co.resumeFrom = 0                        // 清 resume 链
    resumer.pushBool(true)                   // resume 第 1 返回值:true
    // 主函数的返回值此刻在 co 栈(RETURN 已把它们 moveResults 到 co 的 entryCi 帧的返回区,05 §7.2)
    n := transferReturnValues(co, resumer)   // 跨 Thread 搬主函数返回值
    return 1 + n                             // (true, 主函数返回值...)
}
```

- **co 变 dead**:正常结束的协程不可再 resume(再 resume 报 `cannot resume dead coroutine`,§2.3)。
- **返回值搬运**:主函数 RETURN 时(05 §7.2)返回值已落在 co 的 entryCi 帧返回区;resume 把它们跨 Thread 搬回
  resumer 栈,作为 resume 的第 2..个返回值。

### 4.5 协程内未捕获错误:错误对象 → resume 的 (false, err)(呼应 09 §12)

协程内未被协程内 pcall 捕获的错误,冒泡到 entryCi(co 的 execute 返回 `sigError`),resume **捕获**它(resume
是 protected 边界,§5.2),返回 `(false, errval)`,co 变 dead:

```go
func (vm *VM) assembleErrorResult(resumer, co *Thread, lerr *LuaError) int {
    co.status = statusDead                   // §2.2 / 09 §12:出错 → co 变 dead
    co.resumeFrom = 0
    resumer.pushBool(false)                  // resume 第 1 返回值:false
    resumer.push(lerr.value)                 // 第 2 返回值:错误对象(09 §1.2:可任意类型)
    // co 的栈整体作废(co 已 dead);不需要 recoverToProtectionPoint 回退到保护点 ——
    // 整个 co 报废,其 CallInfo/值栈留待 GC(§6:dead 协程可回收)。
    return 2                                 // (false, errval)
}
```

**这与 09 §12 的定稿一致**:resume 边界是错误的"停靠站"(与 pcall 并列),协程内错误**不**冒泡穿过 resume
打到 resumer 的调用者——被 resume 转成 `(false, err)` 返回值。错误对象 `lerr.value` 的**位置前缀 / 变量名后缀
已在 co 内构造错误时拼好**(09 §3/§8),resume 原样传递(跨 Thread 但错误对象本身是个 Value,搬运即拷贝)。

> **co 出错后的清理与 pcall 的差异(呼应 09 §12.2 表)**:pcall 捕获错误后 `recoverToProtectionPoint`(回退
> ciTop/top + closeUpvals,09 §5.3),因为 pcall 后**同一 Thread 还要继续用**。resume 捕获 co 的错误后**不回退
> co 到某保护点**——整个 co 报废(dead),它的 CallInfo 链/值栈/开放 upvalue 整体作废,留待 GC(§6)。区别根源:
> pcall 是"同 Thread 内的局部保护"(Thread 存活),resume 是"跨 Thread 的协程边界"(co 整体死亡)。**但开放
> upvalue 仍需关闭**:co dead 前应 `closeUpvals(co, 0)` 关闭其所有开放 upvalue(否则若有别处共享的 upvalue
> 指向 co 的栈,co 栈被 GC 后悬垂)——见 §6.3。

---

## 5. yield 不能跨 host call 边界(Lua 5.1 硬限制)

### 5.1 限制陈述(5.1 口径,显式标注)

**Lua 5.1 禁止跨 C-call(host call)边界 yield**。具体地,以下情况 yield 报错
`attempt to yield across C-call boundary`:

- 在 host function 内部(host 调 Lua、Lua 又 yield)——yield 信号要穿过那个 host 的真 Go 栈帧,**穿不过**。
- 经典触发:`table.sort(t, comp)` 的比较器 `comp` 里 yield;`pcall(f)` 的 `f` 里 yield;`string.gsub(s, p, repl)`
  的 `repl` 函数里 yield;元方法(`__index`/`__add` 等)里 yield。这些 Lua 函数都是被 **host function 回调**
  进入的,它们的 yield 要穿过 host 的 Go 帧。

> **P3+ 前瞻(承 [../p3-wasm-tier](../p3-wasm-tier/07-coroutine-thread-rule.md) 回填)**:gibbous(Wasm 编译码)帧与 host 帧同理——core Wasm 帧无法挂起后复原,yield 信号同样穿不过。P3 以「线程级 tier 规则」(协程线程上一律走 crescent)使该情形不会发生;对 P1 无影响。

```
为什么穿不过(路线 B 的物理原因):
  Lua 代码 → host function table.sort(真 Go 栈帧)→ callLuaFromHost(起新 execute)→ comp(Lua)
                     │ 真 Go 栈帧,不在 arena                        │
            comp 里 coroutine.yield ──► yield 信号要 return 出 comp 的 execute ──► 撞到 table.sort
            的 Go 栈帧。yield 信号是"让 execute return",但 table.sort 是个普通 Go 函数,它的
            Go 调用栈无法"接住一个 yield 信号并把控制权交还给调用 table.sort 的那个 Lua 帧" ——
            Go 函数只能正常 return(返回排序结果)或 panic,不能"暂停自己让 Lua 续跑"。
            ★ 这正是 5.1 限制的物理本质:host 是真 Go 栈帧,yield 的冒泡(return)穿不过它。
```

> **5.1 vs 5.2+(必须标注)**:Lua 5.2+ 引入 `lua_callk`/`lua_pcallk`(带 continuation 的可恢复 C 调用),
> 允许"跨 C 边界 yield"——host function 显式提供一个 continuation,yield 后恢复时从 continuation 续跑。**5.1
> 没有这套机制**,故 5.1 一律禁止跨 C 边界 yield。`docs/design/roadmap.md` (§6) 锁 5.1:**望舒 P1 不做
> `*k` continuation,跨 C 边界 yield 一律报错**。这意味着 ① `pcall(coroutine.yield)` 在 5.1 里报错(5.2+ 可
> 工作);② 元方法内不能 yield;③ stdlib 迭代器(如 `string.gmatch` 内部)不能 yield。**这些 5.2+ 放宽 P1
> 全部不做,显式记 5.1 口径**(§11 缺口:若宿主生态需要 5.2 可恢复性,记为未来评估)。

### 5.2 检测机制(nCcalls / host 帧标记,扣 05 §7.4)

yield 时(`hostCoroutineYield`,§3.4 step①的 `canYield`)检测"当前 co 是否能 yield",两类不可 yield:

```go
// internal/crescent —— yield 可行性检测
func (vm *VM) canYield(th *Thread) bool {
    // ① 主线程不能 yield(§8):主线程没有 resumer,yield 无处可去
    if th == vm.mainThread {                 // 06 §5.1 R3:主线程也是一个 Thread
        return false                          // → "attempt to yield from outside a coroutine"
    }
    // ② 跨 C-call 边界检测(本节核心):
    //    co 从【它自己的】entryCi(本次 resume 起的 execute)到当前帧之间,若【夹着 host 帧】,
    //    说明 yield 要穿过 host → 禁止。
    //    判据:co 本次 resume 进入 execute 时记的 nCcalls 基线,与当前 nCcalls 比较 ——
    //    若当前 nCcalls > resume 时的基线,说明 resume 之后又有 host→Lua 重入(host 帧)夹在中间。
    return vm.nCcalls == th.resumeNCcallsBaseline    // 无新增 host 重入层 → 可 yield
}
```

**检测原理(两套互补的判据)**:

| 判据 | 机制 | 检测什么 |
|---|---|---|
| **nCcalls 基线比对** | resume 进入 execute 时记 `th.resumeNCcallsBaseline = vm.nCcalls`(05 §7.4 nCcalls);yield 时比对当前 `nCcalls` 是否仍等于基线 | 若 yield 时 `nCcalls > 基线`,说明 resume 之后又发生过 host→Lua 重入(05 §7.3),即当前执行点在某个 host function 内部 → 跨 C 边界 → 禁止 |
| **host 帧扫描(可选,更精确)** | 从 co 的 entryCi 到当前 ciTop,扫 CallInfo 链有无 host 帧(`protoID==哨兵`,05 §1.2 word2) | 直接看 co 本次 resume 的调用链里有没有 host 帧夹在 yield 点之前 |

P1 用 **nCcalls 基线比对**(O(1),简单):resume 时存基线(§3.5 step③ 的 `nCcalls++` 之后或之前的值,精确点
在 §5.3),yield 时一次比较。若需更精确的错误定位(指出是哪个 host 函数挡了 yield),P2+ 可加 host 帧扫描
(记缺口 §11)。

> **nCcalls 的双重职责(扣 05 §7.4)**:05 §7.4 用 `nCcalls` 防"host↔Lua 无限交替重入打爆 Go 栈"
> (`C stack overflow`,上限 200)。本文**复用同一个 nCcalls** 做 yield-across-C-boundary 检测——因为两件事
> 同源:`nCcalls` 增长 = host→Lua 重入层数 = 当前执行点之上夹了几层 host Go 帧。yield 要求"co 本次 resume
> 之后没夹 host 帧"(nCcalls 回到 resume 基线),正是"yield 信号能纯 return 冒泡到 resume 而不撞 host 帧"的
> 充要条件。**一个计数器,两个用途**,无需新增机制(再次体现路线 B 复用 05 的纪律)。

### 5.3 yield 时的栈状态与 nCcalls 配平(细节)

yield 经 `hostCoroutineYield`(host)触发,而 host 调用本身会 `nCcalls`... 这里有个**配平细节**必须定死:

- `coroutine.yield` 是 host function,但它是**同步 host 调用**(05 §7.6 callHost),**不**起新 execute、**不**
  增 nCcalls(只有 host→Lua 重入即 `callLuaFromHost` 才 `nCcalls++`,05 §7.4)。所以 yield host 执行时,
  nCcalls 仍是"co 本次 resume 的基线 + 0"(假设 yield 点之前无其它 host→Lua 重入)。
- **resume 进入 execute 时**(§3.5 step③)`nCcalls++`(host→Lua 重入,co 的 execute 是被 resume host 起的)。
  所以 `th.resumeNCcallsBaseline` 应记为**这次 `nCcalls++` 之后**的值(即 co 的 execute 所在的 nCcalls 层级)。
  yield 时 `vm.nCcalls == baseline` 当且仅当 yield 点与 resume 起点在同一 nCcalls 层(中间无 host→Lua 重入)。
- **yield 冒泡出 execute 时**(§3.3 step②/③),execute return 到 resume host,resume host `nCcalls--`(§3.5
  step⑤),配平。**co 的 CallInfo 链不弹**(yield 保留,§3.3),但 nCcalls 这个"Go 栈深度计数"要配平(因为
  execute 的 Go 栈帧确实 return 了)。

> **yield 时 host 帧(yield 自己的 CallInfo)的处理**:`coroutine.yield` 作为 host 也压了一条 host CallInfo
> (05 §7.6 step②)。yield 触发 `callYield` 时(§3.4),**这条 yield 的 host CallInfo 要不要弹?** 定稿:
> **保留**——因为 yield 是"挂起",co 恢复时要从"yield 这条 CALL 的下一步"继续(§4.3:yield 的返回值落在
> 这条 CALL 的返回寄存器)。若弹掉 yield 的 host 帧,恢复时就找不到"yield 调用期望的返回值落点"了。所以
> yield 的 host CallInfo 与整条链一起冻结,恢复时这条 host 帧的"返回值搬运"(05 §7.2 moveResults)由下次
> resume 的 transferArgs 完成(§4.3)。**这是 yield 与正常 host 返回的关键差异**:正常 host 返回弹 host 帧
> (callReturnedHost),yield 保留 host 帧(callYield)。记入对 05 §7.6 的协程侧扩展(§10)。

---

## 6. 与 GC 的关系(扣 06 §5.1)

### 6.1 挂起协程作为 GC 根(R3/R4/R5)

06 §5.1 的根集合里,协程相关的是 R3/R4/R5:

| 根 | 06 §5.1 定义 | 协程语义 |
|---|---|---|
| **R3** | 主线程(main thread) | 主线程也是一个 Thread(§8),恒为根(连带其值栈/CallInfo) |
| **R4** | 所有活跃 thread | **所有可达的协程 Thread**——经 resume 链(R3→...)或被某变量引用 |
| **R5** | 当前 running thread 的值栈与 CallInfo | 当前正在跑的协程的执行现场(寄存器全可达) |

**挂起协程(suspended)如何被扫为根**——这是任务点名的关键澄清:

挂起协程**不会自动是根**。它被扫描当且仅当它**可达**,可达路径有二:

1. **被 resume 链引用**:一个 `normal` 协程在 resume 链上(某个更外层协程 resume 了它,`resumeFrom` 链,§7),
   它经 R3/R4 从主线程沿 resume 链可达。但**suspended 协程不在 resume 链上**(它 yield 了,不被任何运行中的
   协程"持有")——所以 suspended 协程的可达**只能靠路径 2**。
2. **被某个变量/数据结构引用**:协程 Thread 是一个 Value(`TagThread`,01 §3.3)。只要它被某个**可达的**
   Lua 变量(全局表、某活表的槽、某活栈的寄存器、某 upvalue)引用,它就可达 → 是根。

```
典型场景:
  local co = coroutine.create(f)   -- co 是局部变量(在主线程某帧的寄存器)
  coroutine.resume(co)             -- co 跑一会儿,yield,变 suspended
  -- 此刻 co 是 suspended,不在 resume 链上(已 yield 回主线程)。
  -- 但 co 这个 Value 还在主线程的局部变量 `co` 里(R5 可达)→ co 可达 → 是根。
  -- 于是 GC 标记 co(R4),连带遍历 co 的值栈/CallInfo(挂起的执行现场,§6.2)→ 全部存活。
```

**关键结论**:**suspended 协程靠"被可达变量引用"成为根,而非靠"在 resume 链上"**。若一个 suspended 协程
不再被任何变量引用(如 `co = nil` 后),它**不可达 → 被 GC 回收**(其值栈/CallInfo/开放 upvalue 一并回收,
§6.3)——即使它逻辑上"还能被 resume"(但没人能 resume 它了,因为没引用)。这是 Lua 5.1 语义:**无引用的
挂起协程会被 GC**(它代表的"暂停的计算"被丢弃)。

### 6.2 mark 要扫所有 thread 的值栈 + CallInfo(06 §5.2 Thread 行)

06 §5.2 的 Thread 行规定了"标记一个 Thread 时要扫的字段"。对**每个可达的协程 Thread**(不只 running,还有
suspended/normal),mark 要扫:

| 字段(01 §5.6) | 扫什么 | 协程特有注意 |
|---|---|---|
| `valueStack[0..top)` | 栈上每个可回收 Value | **挂起协程的栈也要全扫**——它冻结了协程的局部/临时值,都是活的(协程恢复时还要用) |
| `callInfoRef[0..ciTop)` | 每帧引用的 closure + 帧内 Value | 挂起协程的整条 CallInfo 链(冻结的调用栈)都要扫——每帧的 closure 都活 |
| `openUpvalRef` 链 | 每个开放 Upvalue 对象 | 挂起协程的开放 upvalue 链(它栈上变量的 upvalue)要扫 |
| `resumeFrom`(word8) | resume 链上的 resumer Thread | 若非 0,标记 resumer(但 resumer 通常已由其它根可达,§7) |

> **挂起协程的栈"只扫 [0,top)"**:06 §5.2 / 05 已定"栈只扫 `[0,top)`,top 之上是垃圾槽不扫"。挂起协程的
> `top` 是它 yield 时的栈顶(§9 saveFrame 保存),`[0,top)` 是它冻结的活跃区。**这要求 yield 时正确保存 top**
> (§9),否则 GC 要么漏扫活跃值(误回收)、要么多扫垃圾(把已死值当活、轻微泄漏)。

### 6.3 dead 协程可回收;开放 upvalue 跨 yield 存活

- **dead 协程**:status=dead 的协程(正常结束或出错,§4.4/§4.5),若不再被引用,**可回收**(其值栈/CallInfo
  整体作废)。回收前应已 `closeUpvals(co, 0)`(§4.5 注 / §9):dead 前关闭所有开放 upvalue,防止别处共享的
  upvalue 悬垂指向 co 已回收的栈。
- **开放 upvalue 跨 yield 存活**:协程 yield 挂起时,它栈上的开放 upvalue(01 §5.4)**仍然有效**——因为
  ① co 的栈没被回收(co 可达,§6.1);② 开放 upvalue 用 `(threadRef, stackIdx)` 逻辑定位(01 §5.4 / 05 §1.5),
  指向 co 栈的逻辑槽,co 栈在 arena 冻结着,逻辑定位依然有效。**恢复时这些开放 upvalue 自动继续指向 co 栈的
  同一逻辑位置**(co 栈若在挂起期间被 GC 搬迁——P1 不做 compaction,06 §2.4,故不会;未来若做,逻辑定位也免
  修正,05 §1.5)。这是"开放 upvalue 跨 yield 存活"的实现保证。

```
场景:协程里的闭包捕获协程局部,跨 yield:
  function f()
    local x = 10
    local g = function() return x end   -- g 捕获 x(co 栈上的开放 upvalue)
    coroutine.yield(g)                   -- yield 出 g,co 挂起,x 的开放 upvalue 冻结
    x = 20                                -- resume 后续跑,改 x
  end
  -- resumer 拿到 g,在 co 挂起期间调 g() → 读 co 栈的 x(=10,co 栈冻结着,开放 upvalue 有效)
  -- resume 后 co 续跑 x=20 → 再调 g() → 读到 20(同一开放 upvalue,指向 co 栈同一槽)
  -- ★ 全程 co 栈未回收(co 可达)、未搬迁(P1 不 compaction),开放 upvalue 逻辑定位始终有效。
```

> **与 06 §5.2 的衔接**:挂起协程的开放 upvalue 链(`openUpvalRef`)被 mark 扫到(§6.2),开放 upvalue 对象本身
> 存活;它指向的栈槽由 co 的栈扫描(§6.2)覆盖,**不在 upvalue 处重复扫**(06 §5.2 Upvalue 行:"开放态其指向
> 的栈槽不在此扫,随 Thread 栈扫到")。这避免重复标记,且保证开放 upvalue 与它指的栈槽都活。

---

## 7. resume 链 / caller 追踪(01 §5.6 word8 resumeFrom)

### 7.1 resume 链的用途

01 §5.6 word8 `resumeFrom`(caller thread ref)记录"谁 resume 了我"。它串成一条 **resume 链**,三个用途:

1. **`coroutine.running()`**:返回当前正在跑的协程(§9 库语义)。当前 running thread 就是 `vm.curThread`,
   resume 链用于回溯(主要是 running() 直接返回 curThread,链用于其它回溯场景)。
2. **yield 让出方向**:yield 把控制权交还给**当前协程的 resumer**(`co.resumeFrom`)——但路线 B 下这是隐式的
   (yield 信号 return 出 execute 自然回到 resume host,§3.3),`resumeFrom` 主要用于**状态恢复**(resume host
   切回 resumer 时知道 resumer 是谁,§3.5 step⑤其实直接用了 host 局部 `resumer`,链用于校验/调试)。
3. **防止 resume 循环**:resume 一个 `normal` 协程(它在 resume 链上、是当前协程的祖先)会形成环——状态检查
   (§2.3:normal 不可 resume)拦下。`resumeFrom` 链让"检测一个协程是否在当前 resume 链上"可行(沿链回溯)。

```
resume 链示例:
  main --resume--> coA --resume--> coB(当前 running)
  此刻:
    main.status = normal,  main.resumeFrom = 0(主线程无 resumer)
    coA.status  = normal,  coA.resumeFrom  = main
    coB.status  = running, coB.resumeFrom  = coA
  链:coB.resumeFrom → coA.resumeFrom → main.resumeFrom → 0
  - 若 coB 试图 resume(coA):coA 是 normal(在链上)→ 报 "cannot resume non-suspended coroutine"(防环)
  - coB yield:控制权回 coA(coB.resumeFrom),coA 变 running,coB 变 suspended,coB.resumeFrom 清 0
```

### 7.2 防 resume 环(与 §2.3 normal 态衔接)

非对称协程"控制流是树状 resume 链"的保证,靠 `normal` 态 + `resumeFrom` 链实现:

- resume 链上的每个协程(coA)是 `normal`(它 resume 了下游,在等)。
- resume 一个 `normal` 协程 = 试图 resume 一个"还在调用栈上的祖先"= 形成环。§2.3 的状态检查(normal 不可
  resume)直接拦下,报 `cannot resume non-suspended coroutine`。
- **为什么不需要显式遍历 resumeFrom 链查环**:因为"在 resume 链上"等价于"status==normal"(§2.1)。任何被
  更外层 resume 的协程必是 normal。所以**一次 status 检查(normal?)就等价于查环**,无需遍历链——`normal`
  态的设计就是为了把"查环"降为"查状态"。`resumeFrom` 链保留用于 running()/调试/未来跨协程 traceback
  (09 §12.2:debug.traceback(co) 可跨 Thread,但不沿 resume 链缝合)。

### 7.3 字段精确用法定死(01 §5.6 word8)

| 时机 | `co.resumeFrom` 操作 | 谁做 |
|---|---|---|
| create | = 0(无 resumer) | hostCoroutineCreate(§4.1) |
| resume 进入 | `co.resumeFrom = refOf(resumer)` | hostCoroutineResume(§3.5 step③) |
| yield 让出 | `co.resumeFrom = 0`(co 挂起,不再被持有) | yield 处理(§3.3 / §4.3,co 变 suspended 时清) |
| 正常结束/出错 | `co.resumeFrom = 0`(co dead) | assembleReturnResult/assembleErrorResult(§4.4/§4.5) |

> **yield 时清 resumeFrom 的理由**:co yield 后变 suspended,**不再在任何 resume 链上**(它让出了)。此刻
> `co.resumeFrom` 应清 0——co 不被任何运行中协程"持有"(它的可达性此后靠"被变量引用",§6.1)。若不清,
> co 会错误地"指向"它上次的 resumer,可能造成 GC 误判可达(把已无关的 resumer 当 co 的根)或 running()/
> traceback 回溯错误。**resumeFrom 只在 co 处于 running/normal(在 resume 链上)时有效,suspended/dead 时为 0**。

---

## 8. 主线程(06 §5.1 R3)

### 8.1 主线程也是一个 Thread

**主线程(执行宿主最初 `Program.Call` 的那个执行流)也是一个 Thread**(06 §5.1 R3,01 §5.6)。它与协程
Thread 的唯一区别:

- **主线程不是 `coroutine.create` 创建的**——它由 `State`/`Program` 初始化时创建(`State.mainThread`,06 §5.1
  R3),是 VM 的入口执行流。
- **主线程的 `resumeFrom` 恒为 0**——没有谁 resume 主线程(它是 resume 链的根,§7.1)。
- **主线程不能 yield**(§8.2)。

主线程一样有独立值栈 + CallInfo 链(都在 arena),一样被 GC 当根(R3)。`coroutine.running()` 在主线程里返回
特殊值(§8.3,5.1 返回 nil)。

### 8.2 主线程不能 yield

主线程 yield 报错 `attempt to yield from outside a coroutine`(§5.2 `canYield` 的第①检查):

```
主线程 yield 为什么非法:
  yield 的语义是"让出控制权给 resumer"。主线程【没有 resumer】(它是入口,resumeFrom=0)——
  yield 无处可去。物理上(路线 B):主线程的 execute 是 Program.Call 直接起的(11 §embedding),
  不是某个 resume host 起的。yield 信号若从主线程的 execute 冒泡出去,会撞到 Program.Call 的 Go
  栈(宿主的 Go 代码),那里没有"接住 yield 并恢复"的逻辑 → 等价跨 C 边界(§5)。所以主线程 yield
  统一报 "attempt to yield from outside a coroutine"(5.1 措辞,待 12 核对)。
```

> **措辞区分(待 12 核对)**:主线程 yield → `attempt to yield from outside a coroutine`;协程内跨 C 边界
> yield → `attempt to yield across C-call boundary`。两者都是"yield 非法",但措辞不同(前者"不在协程里",
> 后者"在协程里但隔着 C 帧")。`canYield`(§5.2)先查主线程(给前者措辞),再查 nCcalls 基线(给后者)。
> 精确措辞 [12](./12-testing-difftest.md) 钉死(09 §9.3 措辞纪律)。

### 8.3 `coroutine.running()` 在主线程

Lua 5.1 `coroutine.running()`:在协程里返回该协程(thread 值);**在主线程里返回 nil**(5.1 语义:主线程不被
视为"一个协程")。这是 5.1 与 5.2+ 的差异点之一(5.2+ `running()` 在主线程返回 main thread + true):

```
coroutine.running():
  if vm.curThread == vm.mainThread:  return nil          -- 5.1:主线程返回 nil
  else:                              return curThread     -- 协程里返回该协程 thread 值
```

> **5.1 vs 5.2+(标注)**:5.1 `running()` 主线程返回 **nil**;5.2+ 返回 `(mainthread, true)`。`docs/design/roadmap.md`
> (§6) 锁 5.1:**主线程 running() 返回 nil**。差分基准据此核对(§9 库语义表)。

---

## 9. 栈与 frame 的保存 / 恢复(呼应 05 reloadFrame)

### 9.1 yield 时:保存当前 frame 回 CallInfo

yield 挂起时,要把**当前正在执行的 frame**(05 §1.3 的 pc/base/proto 等 Go 局部缓存)**存回 co 的栈顶
CallInfo**(arena),这样下次 resume 能重建。这是 05 §1.3 `reloadFrame`(从 CallInfo 重建 frame)的**逆操作**
`saveFrame`(把 frame 存回 CallInfo):

```go
// internal/crescent —— yield 挂起前,把活跃 frame 存回 CallInfo(reloadFrame 的逆)
// 本文对 05 §1.3 的扩展:05 有 reloadFrame(CallInfo→frame),协程需要 saveFrame(frame→CallInfo)。
func (vm *VM) saveFrame(f *frame, co *Thread) {
    ci := co.ciAt(f.ci)                  // 当前帧的 CallInfo(栈顶,yield 所在帧)
    ci.savedPC = f.pc                     // ★ 保存当前 pc —— 恢复时从这里继续(yield 的下一条)
    // base/protoID 在 CallInfo 里本就有(enterLuaFrame 时写的,05 §1.4),无需重存
    // top:保存 co 当前栈顶(GC 扫 [0,top) 需要,§6.2)
    co.top = currentTop(f)                // 当前逻辑栈顶
    // f.proto/f.cl 等可由 CallInfo.protoID 在 reloadFrame 时重建,无需存
}
```

> **yield 保存 pc 的精确点**:yield 经 `coroutine.yield` host 触发,而 yield 是一条 CALL 指令(调 host)。yield
> 挂起时,co 的栈顶帧是**调用 coroutine.yield 的那个 Lua 帧**(yield host 帧之下)。这个 Lua 帧的 pc 应指向
> "CALL coroutine.yield 的下一条指令"(05 §2.3:pc 先自增,CALL 执行后 pc 已指向下一条)。saveFrame 存这个
> pc。**下次 resume 时**,reloadFrame 从这个 pc 继续——于是 `local x,y = coroutine.yield(p,q)` 的下一条指令
> 接着跑,x,y 是 resume 传入的值(§4.3)。这要求 yield host 触发 callYield 时,**调用它的 Lua 帧的 pc 已正确
> 指向下一条**(callHost 不改调用者 pc,05 §7.6),且 yield 的 host 帧保留(§5.3,使返回值落点不丢)。

### 9.2 resume 时:从栈顶 CallInfo 重建 frame

resume 进入 `execute(co)`(§3.5 step④),`execute` 开头 `vm.loadTopFrame(co)`(05 §2.3 / §12)从 co 的栈顶
CallInfo 重建 frame——这**直接复用 05 §1.3 的 reloadFrame**,无需协程专属逻辑:

```go
// execute(co) 开头(05 §12),resume 进入时:
func (vm *VM) execute(th *Thread) executeSignal {
    entryCi := th.ciTop
    f := vm.loadTopFrame(th)     // ★ 05 §1.3 reloadFrame:从 th 栈顶 CallInfo 重建 frame
    //   - 首次 resume:栈顶 CallInfo 是 create 压的 fresh 主函数帧,reloadFrame → f 指向主函数 pc=0
    //   - 后续 resume:栈顶 CallInfo 是上次 yield saveFrame 存的帧,reloadFrame → f 指向 yield 的下一条 pc
    code := f.proto.Code
    for { ... }                  // 从重建的 f 继续跑
}
```

**首次 resume vs 后续 resume 的统一**:两者都靠 `loadTopFrame`(reloadFrame)从栈顶 CallInfo 重建 frame——

- 首次:栈顶 CallInfo 是 create 时压的 fresh 帧(§4.1 setupMainFrame),reloadFrame 得到主函数的 frame(pc=0)。
- 后续:栈顶 CallInfo 是 yield 时 saveFrame 存的帧(§9.1),reloadFrame 得到 yield 点的 frame(pc=yield 下一条)。

**统一性是路线 B 的红利**:resume 进入 execute 的机制(loadTopFrame 重建 frame + 循环)对"首次启动"和"yield
恢复"**完全一致**——都是"从栈顶 CallInfo 重建 frame 继续跑"。差别只在栈顶 CallInfo 的内容(fresh 主函数帧 vs
冻结的 yield 帧),而 reloadFrame 不关心这个差别。这正是"调用链在 arena、frame 是其缓存"(05 §1.1)的威力:
挂起/恢复 = CallInfo(arena 持久)与 frame(Go 局部缓存)之间的 save/reload,O(1)。

### 9.3 saveFrame / reloadFrame 的对称性(对 05 §1.3 的回填)

05 §1.3 定义了 `reloadFrame`(CallInfo→frame,栈扩容/切帧后重建 frame)。协程需要它的逆 `saveFrame`
(frame→CallInfo,yield 时持久化)。**这两个是一对**:

| 操作 | 方向 | 05 已有? | 用途 |
|---|---|---|---|
| `reloadFrame(f)` | CallInfo → frame | ✅ 05 §1.3 | 栈扩容后重取 stk;切帧后重建 frame;**resume 恢复**(本文 §9.2) |
| `saveFrame(f, th)` | frame → CallInfo | ❌ 本文新增 | **yield 挂起**(本文 §9.1):把 pc/top 存回 CallInfo |

> **对 05 的回填请求(§10)**:05 §1.3 应补 `saveFrame`(frame→CallInfo)作为 `reloadFrame` 的对称操作,
> 供 yield 挂起用。saveFrame 存的核心是 `savedPC`(yield 点 pc)与 `top`(GC 扫描边界)。其余字段(base/
> protoID)CallInfo 本就有(05 §1.4 enterLuaFrame 写的)。这不改 05 的 CallInfo 布局(05 §1.2),只增一个
> 与 reloadFrame 对称的 helper。

---

## 10. coroutine 库语义(机制的消费者,本文定义,10 引用)

本文定义 `coroutine.*` 的**语义与实现机制**;[10-stdlib](./10-stdlib.md) 只列清单、做注册、指向本文。
所有 `coroutine.*` 是 host functions(05 §7.6 `HostFn`,在 `internal/stdlib`):

| 函数 | 签名 | 返回 | 机制(本文节) |
|---|---|---|---|
| `coroutine.create(f)` | f 是函数 | thread(suspended) | §4.1:新 Thread + setupMainFrame,不执行 f |
| `coroutine.resume(co, args...)` | co 是 thread | `(true, yields/returns...)` 或 `(false, err)` | §3.5/§4.2-4.5:切 Thread + 起 execute + 组装返回值;protected 边界 |
| `coroutine.yield(args...)` | 任意值 | `(下次 resume 的 args)` | §3.4/§4.3:标记 yield 值 + 触发 callYield 冒泡;挂起当前协程 |
| `coroutine.status(co)` | co 是 thread | `"suspended"/"running"/"normal"/"dead"` | §2.1:读 co.status |
| `coroutine.wrap(f)` | f 是函数 | function(resume 的包装) | §10.1:错误直接抛而非返回 false |
| `coroutine.running()` | — | 当前协程 thread,或主线程 nil | §8.3:读 vm.curThread(主线程返回 nil,5.1) |

### 10.1 `coroutine.wrap(f)` 的语义与错误传播差异(呼应 09 §12.3)

`coroutine.wrap(f)` 返回一个**函数**,调用它 = resume 对应协程,但**错误处理不同于 resume**:

```
coroutine.wrap(f):
  co := coroutine.create(f)
  return function(...)                    -- 返回一个包装函数
    local rets = {coroutine.resume(co, ...)}   -- 内部 resume
    if rets[1] == false then              -- resume 失败(co 出错)
      error(rets[2])                       -- ★ 直接【重抛】错误(传播到 wrap 函数的调用者),不返回 false
    end
    return unpack(rets, 2)                 -- 成功:返回 yield/return 的值(不含 true)
  end
```

**与 resume 的关键差异(09 §12.3 已点明,本文定稿)**:

| | `coroutine.resume(co, ...)` | `coroutine.wrap(f)` 返回的函数 |
|---|---|---|
| 协程内错误 | 捕获,返回 `(false, err)` | **不捕获,重抛**(`error(err)`)到调用者 |
| 成功返回 | `(true, yields/returns...)`(带 true) | `(yields/returns...)`(不带 true) |
| 错误后协程 | dead(同) | dead(同) |
| 调用者需检查 | 需检查第 1 返回值是否 true | 不需要(出错直接抛,用 pcall 在外层接) |

- **wrap 的错误直接传播到 resumer**(09 §12.3):wrap 函数内部 resume 若得 `(false, err)`,立即 `error(err)`
  重抛——错误传播到调用 wrap 函数的地方,被那里的 pcall 捕获(若有)或继续冒泡。这让 wrap 更适合"协程当
  普通函数/迭代器用"的场景(出错就抛,不用每次检查 true/false)。
- **实现机制**:wrap 返回的函数是一个 host closure(或 Lua closure),捕获 co(作为它的 upvalue)。调用它走
  正常 host/Lua 调用(05 §7),内部调 resume(§3.5),检查返回值,失败则 raise(09 §3.3)。**wrap 不是新机制**
  ——它是 resume 的薄包装 + 错误重抛,复用 resume(§3.5)与 raise(09 §3.3)。

> **wrap 出错后协程状态**:与 resume 一样,wrap 内部 resume 得到错误时 co 已变 dead(§4.5)。wrap 重抛错误后,
> 再调 wrap 函数会 resume 一个 dead 协程 → 报 `cannot resume dead coroutine`(§2.3)。这与 resume 一致(协程
> 出错即 dead,不可恢复)。

### 10.2 resume 是 protected 边界(呼应 09 §5 / §12)

`coroutine.resume` 本身是 **protected**(类似 pcall 边界,09 §12.1)——它捕获 co 内的未捕获错误,转成
`(false, err)`(§4.5)。机制上,resume 起 `execute(co)`(§3.5 step④)并**检查返回的 `sigError`**(§3.3),正是
09 §9.4 的"每个 host→Lua 重入点都是潜在的错误捕获点"——resume 是这种 host 的典范(与 pcall 并列)。

| | pcall(09 §5) | resume(本文) |
|---|---|---|
| 起 execute | 同一 Thread,callLuaFromHost(09 §5.2) | **切到 co**,execute(co)(§3.5) |
| 检查返回 | `*LuaError` → (false, err)(09 §5.2) | `sigError` → (false, err)(§4.5) |
| 出错后 | 同 Thread 健康,recoverToProtectionPoint(09 §5.3) | **co 整体 dead**,不回退保护点(§4.5) |
| 跨 Thread | 否 | **是**(co → resumer) |

> resume 与 pcall 都是"protected host→Lua 重入",但 resume **额外切 Thread** 且**出错后报废整个 co**(而非
> 回退到保护点)。这是协程"独立栈、出错即整体死亡"与 pcall"同栈、局部保护"的本质区别(09 §12.2 表)。

---

## 11. 不变式清单 + 文档缺口 / 待决

### 11.1 不变式清单(实现与差分须守)

1. **路线 B:单 goroutine + reentry 加一层**(§3.2):resume = 在当前 Go 栈起一层 `execute(co)`;yield = 从那层
   execute 带 `sigYield` 信号 return 冒泡。**不开 goroutine、不拷 Go 栈**(§1.4)。P1 关键决策。
2. **协程切换 O(1) 不拷 Go 栈**(§1.4):切换 = saveFrame(当前 co)+ 切 curThread 指针 + reloadFrame(目标 co)。
   与 Lua 调用深度无关。扣合 05 §7.1 + roadmap §2 栈移动税。
3. **每协程独立 Thread**(§1.2):独立值栈 + CallInfo 链 + openUpval 链 + 状态字段,全在 arena(01 §5.6)。
4. **yield 信号冒泡与 *LuaError 冒泡同构**(§3.3/§3.4):yield 经 host(yield)→ callHost 返回 callYield →
   主循环 return sigYield,与 09 §9.4 的 error 冒泡共享"host 信号 + callHost 检查 + 主循环冒泡"通道。
5. **yield 保留 CallInfo 链,RETURN 弹 CallInfo**(§3.3):yield 是挂起(整条链冻结在 arena),RETURN 是结束
   (弹帧)。yield 的 host 帧也保留(§5.3),使恢复时返回值落点不丢。
6. **yield 不跨 C-call 边界**(§5,Lua 5.1 硬限制):host 内/元方法内/pcall 内 yield → `attempt to yield
   across C-call boundary`。检测靠 nCcalls 基线比对(§5.2,复用 05 §7.4 nCcalls)。**5.1 不做 5.2+ 的 *k
   continuation**(§5.1)。
7. **主线程不能 yield**(§8.2):→ `attempt to yield from outside a coroutine`。主线程是 Thread(R3)但无
   resumer(resumeFrom=0)。
8. **resume 状态检查**(§2.3):仅 suspended 可 resume;dead → `cannot resume dead coroutine`;running/normal →
   `cannot resume non-suspended coroutine`。
9. **状态转移**(§2.2):create→suspended;resume:suspended→running(resumer→normal);yield:running→suspended
   (resumer normal→running);结束/出错→dead。
10. **跨 Thread 值搬运**(§4):首次 resume 参数→主函数参数;后续 resume 参数→yield 返回值;yield 参数→resume
    返回值;主函数返回值→resume 返回值;错误→resume (false,err)。都是两个 Thread 值栈间的拷贝。
11. **resume 是 protected 边界**(§10.2 / 09 §12):捕获 co 内未捕获错误→(false,err)+co dead。**wrap 重抛**
    (§10.1 / 09 §12.3),不捕获。
12. **co dead/出错后整体报废**(§4.5):不回退到保护点(区别于 pcall);dead 前 closeUpvals(co,0)关开放 upvalue。
13. **挂起协程靠"被可达变量引用"成根**(§6.1):不在 resume 链上的 suspended 协程,靠被变量引用可达(R4);
    无引用则被 GC 回收。mark 扫所有可达 thread 的值栈[0,top)+CallInfo+openUpval(§6.2)。
14. **开放 upvalue 跨 yield 存活**(§6.3):co 挂起时栈上开放 upvalue 仍有效(co 栈冻结、逻辑定位不变)。
15. **resumeFrom 仅在 running/normal 有效**(§7.3):suspended/dead 时清 0;normal 态 + resumeFrom 链实现防环
    (§7.2,查环降为查 status==normal)。
16. **saveFrame/reloadFrame 对称**(§9.3):reloadFrame(05 §1.3)恢复,saveFrame(本文)挂起;统一首次/后续
    resume 为"从栈顶 CallInfo 重建 frame"(§9.2)。
17. **running() 主线程返回 nil**(§8.3,5.1 口径,非 5.2+ 的 mainthread+true)。

### 11.2 文档缺口 / 待决(记入 memory/doc-gaps)

- **对 [05](./05-interpreter-loop.md) 的回填请求**(本文带来的协程侧扩展,只增不改 05 的核心机制):
  ① `execute` 返回值从 `*LuaError`(05 §2.3)**泛化为 `executeSignal`**(sigReturn/sigYield/sigError,§3.3)
  ——这是 05 的错误返回与协程 yield 返回的统一信号类型;② `callHost` 增 `callYield` 分支(检查
  `yieldRequested`,§3.4,与 05 §9.4 检查 `pendingErr` 对称);③ 新增 `saveFrame`(frame→CallInfo)作为
  `reloadFrame`(05 §1.3)的对称操作(§9.3);④ Thread 增 `resumeNCcallsBaseline` 字段(或运行期变量)供
  yield 跨 C 边界检测(§5.2)。**05 已确认并登记**:①/② 见 05 §2.3 签名注,③ 见 05 §1.3,④ 定为 VM 运行期变量(05 §13)。✅
- **对 [01](./01-value-object-model.md) §5.6 的确认**:本文用到 word8 `resumeFrom`(✅已有)、word1 `status`
  (✅已有,四值 suspended/running/normal/dead)、word7 `errorJmp`(协程内 pcall 保护点链,与 09 协作)。
  **`resumeNCcallsBaseline`** 若放 Thread 对象(而非 VM 运行期变量),需 01 §5.6 增字段(或复用现有 reserved);
  本文倾向放 VM 运行期(每次 resume 设/读,无需持久化到 Thread),记待定。
- **路线 A 兜底**(§3.2):P1 定路线 B。若 yield 信号冒泡在工程上遇意外复杂度(如某 stdlib 迭代器的 yield
  路径难以纯 return 表达,或与某 host 重入交互复杂),路线 A(goroutine+channel)是兜底——但引入 A 会作废 05
  §7 的协程架构投资,且与 P5 trace JIT 方向相悖(§3.6)。**记为风险兜底,P1 不预先实现 A**。
- **跨 C 边界检测精度**(§5.2):P1 用 nCcalls 基线比对(O(1),够用)。若需更精确的错误定位(指出是哪个 host
  函数挡了 yield,用于更友好的报错),P2+ 可加 host 帧扫描(从 entryCi 到 ciTop 找 host 帧)。P1 简化记缺口。
- **协程数量上限 / Thread 回收**(§6.3):大量 create 而不 resume(或 resume 后弃用)产生大量 suspended/dead
  Thread。dead 的可 GC(§6.3),suspended 的若无引用也可 GC(§6.1)。**但创建-丢弃高频场景下 Thread 对象
  (含值栈/CallInfo 初始分配)的 GC 压力**无数据,需实现后压测(对齐 06 §12 的 GC 缺口)。是否需要 Thread
  对象池(复用 dead Thread 的值栈/CallInfo 容量)待评估,记缺口。
- **错误措辞精确格式**(§2.3/§5.2/§8.2):`cannot resume dead coroutine`、`cannot resume non-suspended
  coroutine`、`attempt to yield across C-call boundary`、`attempt to yield from outside a coroutine` 的精确
  冠词/标点,**待 [12](./12-testing-difftest.md) 差分核对**与官方 Lua 5.1 逐字节对齐(呼应 09 §9.3 措辞纪律,
  本文给骨架不编造)。
- **5.2+ 可恢复性(*k continuation)是否提供**(§5.1):P1 锁 5.1,跨 C 边界 yield 一律报错。若宿主生态需要
  5.2 的"pcall 内 yield / 协程式迭代器穿 host"能力(如 `coroutine.wrap` 包装的 stdlib 迭代器需 yield),是否
  提供开关待定,**默认 5.1**(roadmap §6)。这影响 stdlib 哪些迭代器能在协程里用(§5.1 注),记缺口。
- **`coroutine.yield` 在 GC finalizer / `__gc` 内**(交 06 §10):finalizer 在 GC 安全点由 host 调用(06 §10.2),
  其内 yield 必然跨 C 边界(且 GC 期间禁重入),应被 §5.2 检测拦下。与 06 §10 的 finalizer 保护调用的交互
  细节待 06/本文交叉核对,记缺口。

---

相关:[01-value-object-model](./01-value-object-model.md)(§5.6 Thread 布局:status/valueStackRef/
callInfoRef/openUpvalRef/errorJmp/resumeFrom) · [05-interpreter-loop](./05-interpreter-loop.md)(§7 reentry
模型 = 本文地基:§7.1 不吃 Go 栈/§7.3 fresh 边界/§7.4 nCcalls;§1.2 CallInfo/§1.3 reloadFrame/§1.5 栈扩容;
§9 错误冒泡) · [06-memory-gc](./06-memory-gc.md)(§5.1 R3/R4/R5 根/§5.2 Thread 扫描行/§10 finalizer) ·
[07-metatables-metamethods](./07-metatables-metamethods.md)(元方法内不能 yield,§5.1) ·
[09-errors-pcall](./09-errors-pcall.md)(§12 resume 错误边界定稿,本文 §4.5/§10.2 呼应;§5 pcall 边界对照) ·
[10-stdlib](./10-stdlib.md)(coroutine 库 host functions 清单/注册,消费本文机制) ·
[11-embedding-arena-abi](./11-embedding-arena-abi.md)(主线程 Program.Call 入口,§8) ·
[12-testing-difftest](./12-testing-difftest.md)(协程错误措辞/状态转移逐字节核对) ·
[../p5-trace-jit/00-overview.md](../p5-trace-jit/00-overview.md)(路线 B 与 trace JIT 对齐,§3.6) ·
[design-premises](../../../llmdoc/must/design-premises.md) ·
roadmap:`docs/design/roadmap.md` (§6 锁 Lua 5.1 / §5 原则 1 解释器永不退役/原则 4 协程走 fallback)
