# P4 §5:系统管线——四项税全额自付 + W^X/icache/trampoline 四件套

> 状态:**架构决策深度,系统管线单一事实源**(对齐 P4 子文档「设计阶段,架构决策深度」基线;具体 amd64/arm64 asm 与 jitContext/自管机器栈精确布局留 [./06-backends](./06-backends.md) 详细设计阶段;wazero 对应 API 与 platform 子包细节标注「待 spike 验证」与「采石场参考」)。本文是 **P4 子目录第五篇**,展开系统管线的完整设计:**P4 收回 P3 外包给 wazero 的四项税后,如何全额自付**——逐税方案 + W^X/icache/trampoline 四件套 + JIT 执行上下文 + 世界边界 + trampoline 三出口协议 + arena base 重载协议 + 两个 safepoint 在 P4 的物理形式。
>
> 上游契约(本文严格遵守):
> [./00-overview.md](./00-overview.md)(P4 定位、tier 映射、跳跃路径);
> [../roadmap.md](../roadmap.md) §2(四项税完整定义 + 标准解法「wazero 已验证」);
> [../../../llmdoc/must/design-premises.md](../../../llmdoc/must/design-premises.md)(前提二 Go runtime 四项税 / 前提四 NaN-box 第一天承诺);
> [../../../llmdoc/architecture/evolution-roadmap.md](../../../llmdoc/architecture/evolution-roadmap.md)(tier 映射:P3/P4 同属 gibbous tier-1)。
>
> P3 物理基础(P4 复用):
> [../p3-wasm-tier/03-memory-model](../p3-wasm-tier/03-memory-model.md)(arena 收养 wazero memory:P3 时段 backing 来源 wazero;§0.4 已写明 P4 build 下 backing 切回 Go 堆 `make`,GCRef 偏移寻址语义同一);
> [../p3-wasm-tier/04-trampoline](../p3-wasm-tier/04-trampoline.md)(跨层互调协议:bit50 / 升层入口签名 `(base i32) → status i32` / status 链错误冒泡 / 三向分派——本文 §3.6/§4 直接继承,只换物理通道);
> [../p3-wasm-tier/05-safepoint-gc](../p3-wasm-tier/05-safepoint-gc.md)(三类 safepoint:分配点 / 层边界 / 回边——本文 §6 一样的,只是「物理形式」从 wazero 内部机制切换为自管原生码)。
>
> P1 已铺垫的料:
> [../p1-interpreter/01-value-object-model](../p1-interpreter/01-value-object-model.md) §2(GCRef 非 Go 指针纪律,源头不变式——P4 写屏障税白赚的根因);
> [../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md) §1(CallInfo + Frame 双层概念)、§7(Lua 调用链状态全在 arena,Lua 调用不吃 Go 栈——P4 帧沿用同一 CallInfo 协议);
> [../p1-interpreter/06-memory-gc](../p1-interpreter/06-memory-gc.md) §1.3(reloadFrame stk 重载纪律——P4 一样的的 arena base 重载);
> [../p2-bridge/01-profiling](../p2-bridge/01-profiling.md) §3 路线 B(回边检查点为 P3/P4 翻译时自插)。
>
> 下游对位:
> [./04-osr-deopt](./04-osr-deopt.md)(trampoline 三出口共用——本文 §4.2 给协议骨架,具体物化语义引用 04);
> [./06-backends](./06-backends.md)(amd64/arm64 后端实现——本文先给协议骨架,具体 asm/寄存器约定/指令编码留 06)。
>
> **本文定位一句话**:**P3 把四项税外包给 wazero,P4 收回这层外包,全额自付。本文是 P4 系统管线的单一事实源——wazero 从依赖转角色为采石场(参考实现指针)**。

对应 Go 包:`internal/gibbous/jit`(系统管线主体:trampoline.go / mmap.go / icache.go / context.go / helpers.go,与 [./00-overview.md](./00-overview.md) §0.1 共题)、`internal/gibbous/jit/<arch>`(per-arch 发射器与 asm stub,详见 [./06-backends](./06-backends.md))。

---

## 0. 定位

### 0.1 一句话:P3 把四项税外包给 wazero,P4 收回这层外包,全额自付

P3 阶段([../p3-wasm-tier/00-overview](../p3-wasm-tier/00-overview.md) §8 共述):**Go runtime 四项税(GC 精确栈扫描 / 异步抢占 / 栈移动 / 写屏障,[../roadmap.md](../roadmap.md) §2)被外包给 wazero**——wazero 自带 exec mmap、自管栈、回边抢占检查点、linear memory 隔离,P3 翻译产物只需「翻译到 Wasm」即可,所有跨 Go runtime 边界的物理代价由 wazero 承担。

P4 阶段:wazero 不再上场,P4 直接发射原生机器码 ⇒ 四项税**整笔账由 P4 自付**。这不是「重做 wazero」——本文 §1 逐项给出 P4 的概念方案,其每一项的物理可行性在 wazero 当前实现中已被验证,wazero 的 platform 子包、wazevo backend、`internal/engine/wazevo/backend/isa/{amd64,arm64}/` 入口 asm 等是采石场(参考实现的代码出处)。

> **wazero v1.x 已切到 wazevo**:wazero 早期使用 `internal/engine/compiler` 子包,v1.x 已切到 wazevo 引擎(SSA + per-ISA backend,位于 `internal/engine/wazevo/backend/isa/{amd64,arm64}/`)。本文及后续所有 wazero 采石场指针指的是当前一代 wazevo 引擎。

### 0.2 wazero 转角色:依赖 → 采石场(参考实现)

| 阶段 | wazero 的角色 | 边界 |
|---|---|---|
| **P1 / P2** | 不引用(P1 build 下 wazero 完全不被链接,[../p3-wasm-tier/03-memory-model](../p3-wasm-tier/03-memory-model.md) §1.3 build tag 分流) | 无 |
| **P3** | **依赖**:作为 Wasm 运行时,P3 翻译产物经 wazero 的 wazevo backend 跑成原生码;arena 收养 wazero memory 作为共见物理内存([../p3-wasm-tier/03-memory-model](../p3-wasm-tier/03-memory-model.md));四项税由 wazero 内部机制承担 | 四项税外包 |
| **P4** | **采石场**:wazero 不在 P4 build 链路上(若 §6 决定退役 P3,则连构建产物也不带),但其 platform / wazevo backend(`internal/engine/wazevo/backend/isa/*`)是 P4 系统管线每件套的参考实现来源 | 四项税自付 |

「采石场」是工程上的精确含义——**P4 的 trampoline asm、mmap+mprotect 翻面序列、icache flush 序列、自管栈切换 SP 序列,都从 wazero 同位实现移植/借鉴**(Apache 2.0,license 兼容)。每税/件,本文都给出 wazero 对应实现位置的指针。

### 0.3 P4 物理可行性的核心:Go runtime 四项税 + 三件套(W^X/icache/trampoline)逐项可解

P4 的存在性证明分两步走:

**第一步:四项税逐项可解**——本文 §1 给概念方案,每项都满足两个条件:
- (a) 在纯 Go 约束下可实现(不依赖 cgo,不复刻 Go runtime 内部符号,[../roadmap.md](../roadmap.md) §6 非目标);
- (b) wazero 已完成一样的机制,作为存在性证明 + 移植蓝本。

**第二步:系统管线四件套(exec mmap / W^X / icache flush / trampoline)逐件可解**——本文 §2 给概念方案,每件同样满足 (a)(b)。

二者合起来,对齐 [../../../llmdoc/must/design-premises.md](../../../llmdoc/must/design-premises.md) 前提二:「VM 边界跨越是几十~百 ns 的固定成本」是 P4 收益模型的硬约束(per-item 短脚本被吃光),而四项税的物理可行性是 P4 收益模型的硬前提(没有合规的运行时机器码,谈不上收益)。

### 0.4 章节路标

| § | 主题 | 谁拥有 |
|---|---|---|
| **§1** | 四项税的逐项概念方案 | 本文 |
| **§2** | 系统四件套:exec mmap / W^X / icache / trampoline | 本文 |
| **§3** | JIT 执行上下文与世界边界(jitContext 字段表 / 自管机器栈布局 / 三条不变式) | 本文 |
| **§4** | trampoline 三出口协议(正常返回 / OSR exit / 慢路径 helper 共用骨架) | 本文 §4.1-§4.4;OSR exit 物化语义详见 [./04-osr-deopt](./04-osr-deopt.md) §3.3 |
| **§5** | arena base 重载协议(承本文 §3.6 不变式 2) | 本文 |
| **§6** | 两个 safepoint 在 P4 的物理形式(承 [../p3-wasm-tier/05-safepoint-gc](../p3-wasm-tier/05-safepoint-gc.md) 三类布点) | 本文 |
| **§7** | 不变式清单(本文承担 5 条) | 本文 |
| **§8** | 风险与开放问题 | 本文 |
| **§9** | 回填请求(若有) | 本文 |

**关键边界**:本文是「P4 系统管线」的单一事实源——四项税逐项方案、四件套、jitContext、世界边界三不变式、trampoline 三出口、arena base 重载、两 safepoint 物理形式全部在本文定稿;具体 asm 与 jitContext/机器栈精确布局留 [./06-backends](./06-backends.md);OSR exit 物化语义留 [./04-osr-deopt](./04-osr-deopt.md)。

---

## 1. 四项税的逐项概念方案

[../roadmap.md](../roadmap.md) §2 列出 Go 进程内生成/执行机器码必过的四关。本节逐项给 P4 的概念方案——每项都对照 (问题 / P4 解法 / P1 已铺垫 / wazero 采石场指针) 四要素。

### 1.1 GC 精确栈扫描——JIT 帧无 stack map

#### 1.1.1 问题:Go GC 扫栈期间生成码帧 frame layout 不可见

Go 的 GC 是**精确**的——扫描 goroutine 栈时,runtime 据每个 Go 函数的 stack map(编译期生成的栈帧布局元数据)逐字判断是否是指针。**生成码帧没有 stack map**:Go runtime 不知道这帧从哪来、哪几个字是指针。若放任不管,GC 扫到 JIT 帧时要么 panic(无 stack map ⇒ 不可恢复),要么误读栈字为指针(读出非法指针 ⇒ memory corruption)。

#### 1.1.2 P4 解法:JIT 跑「自管机器栈」(Go 堆 []byte,trampoline 切 SP 进入)

JIT 代码不在 goroutine 栈上跑,而是跑在「自管机器栈」上——一段从 Go 堆 `make([]byte, N)` 分配的 N 字节缓冲区(详见 §3.4 布局)。trampoline「进」asm 在跨入 JIT 世界前,把 SP 寄存器从当前 goroutine 栈切到该自管栈缓冲区的尾部(向下生长架构如 amd64/arm64),JIT 函数的所有 push/pop/帧建立都在该自管栈上完成;trampoline「出」asm 在 JIT 返回时把 SP 切回原 goroutine 栈。

**Go GC 看不到自管栈**:那段 `[]byte` 对 Go GC 是普通字节数组(无指针),GC 扫到的是 trampoline 进入前的 goroutine 栈帧——而那帧是普通 Go 函数(trampoline 进入 stub 的 caller),有正确的 stack map。GC 扫栈在 trampoline「进」之前的最后一个 Go 栈帧停下,**永不进入 JIT 世界**。

#### 1.1.3 边界纪律:进入 JIT = Go 栈停在已知点(扫描点边界)

进 JIT 前,trampoline 把所有 caller-saved Go 寄存器 spill 到 goroutine 栈的固定位置(per-arch 调用约定,留 [./06-backends](./06-backends.md));进入 JIT 后 goroutine 栈对 GC 看到的就是「trampoline 进入 stub 的栈帧 + 其上若干 Go 调用方栈帧」,全部 stack map 完整。**这就是 [../roadmap.md](../roadmap.md) §2「边界按 syscall 语义」的物理含义**:JIT 世界对 Go runtime 等价于一次同步系统调用——边界处栈一致,内部不可见。

#### 1.1.4 Lua 值帧本就在 arena + CallInfo(承 [../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md) §7)

需要重点澄清:**自管机器栈不持 Lua 值**——Lua 值帧(R(0..n) 寄存器、CallInfo 链)从 P1 起就活在 arena([../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md) §7「Lua 调用链状态全在 arena,Lua 调用不吃 Go 栈」),P4 沿用同一 CallInfo 协议(P3 PW10 R2 完成的 VS0-e 让 CallInfo 也住 arena 的 4 word/帧打包形式,P4 直接复用)。**自管机器栈只持「机器寄存器 spill / 返址 / 调用约定保留位」**——这些是发射后端为完成机器级调用而需要的临时存储,与 Lua 语义无关。

这条划界之所以重要:它让 GC 根扫描在 P4 完全等价于 P1/P3——根仍在 arena 值栈 + CallInfo([../p1-interpreter/06-memory-gc](../p1-interpreter/06-memory-gc.md) §5.1 R5),自管机器栈对根可见性零贡献。承 [../p3-wasm-tier/05-safepoint-gc](../p3-wasm-tier/05-safepoint-gc.md) §3「GC 根零新增」,P4 同样零新增。

#### 1.1.5 wazero 采石场:wazevo backend native call 栈切换

参考实现位置:wazero `internal/engine/wazevo` 的 native call 路径——每次进 Wasm 函数前,wazevo backend 把执行栈从 goroutine 栈切到 wazero 自管栈(Go 堆 `[]uint64`),用一段 arch-specific 入口 asm 调整 SP 寄存器。其 amd64/arm64 入口 asm(`internal/engine/wazevo/backend/isa/amd64/abi_entry_amd64.s` / `internal/engine/wazevo/backend/isa/arm64/abi_entry_arm64.s`)的 SP 切换 + caller-saved 保存序列即 P4 trampoline「进」的直接蓝本。

> **风险标注**:wazero 自管栈的元素是 `[]uint64`(对齐 wazero IR 的 64-bit 栈),P4 自管机器栈的元素是 `[]byte`(机器寄存器宽度 + spill 槽对齐由 per-arch 决定,留 [./06-backends](./06-backends.md))。形式相似但元素类型不同——移植时要按 P4 的发射后端约定重整布局,不是字节对字节抄。

### 1.2 异步抢占——信号可落任意 PC

#### 1.2.1 问题:Go 异步抢占信号落在生成码 PC 上不可恢复

Go 1.14+ 启用基于信号的抢占——runtime 经 SIGURG 异步打断 goroutine,落点是「下一条 Go 指令」。**抢占信号若落在生成码 PC 上,runtime 完全不认识这个 PC**(无 funcInfo / 无 unwinding 信息),栈展开失败,fatal。

> **承 [../p3-wasm-tier/implementation-progress.md §0.1](../p3-wasm-tier/implementation-progress.md) PW0 spike 修正的关键事实**:wazero 在 spike 时被一同测出**也是 async-preemption-unsafe**——抢占信号落在 wazero 生成码上同样不可恢复,wazero 靠 context cancellation 协作终止(`WithCloseOnContextDone`)而非依赖抢占。这是 P3 时段四项税「外包给 wazero」措辞的精确含义:wazero 解决方式与 P4 一样的,不是真有什么魔法。**P4 没有更难的问题,只是要把一样的解法自付一次**。

#### 1.2.2 P4 解法:回边插抢占检查点(load preemptFlag + 置位则经 exit stub 退到边界)

P4 在生成码的**循环回边**(FORLOOP / TFORLOOP / 向后 JMP)插入抢占检查点——一段固定几条指令的「检查 + 条件跳」序列:

```
;; 概念伪码,具体 asm 在 06-backends
load   reg, [jitContext.preemptFlag]   ;; 读 jitContext 中的抢占标志(§3.3)
test   reg, reg
jnz    exit_to_preempt_handler          ;; 置位 ⇒ 跳到 exit stub,经 trampoline 出
;; 否则直线继续
```

`exit_to_preempt_handler` 是 trampoline「出」的 stub(§4.3 慢路径出口共用),它把控制权交回 Go 世界的 helper,helper 内 Go runtime 处理抢占(等同 `runtime.Gosched()` 让出语义),然后 trampoline「进」回 JIT 续跑。`preemptFlag` 由协作机制置位(详见 §1.2.5),不是 Go runtime 的真信号——真信号永不落在 JIT PC 上。

#### 1.2.3 直线段长度有界 ⇒ 不可抢占窗口有界

「不可抢占窗口」= 两次回边检查点之间的最长直线段长度。Lua 字节码的直线段(回边到回边、调用到调用)长度天然有界([../p2-bridge/03-compilability-analysis](../p2-bridge/03-compilability-analysis.md) F5 大函数检查已限编译单元尺寸),且每次跨层调用 / 助手调用 / 分配点都是隐式抢占点(都经 trampoline 出 ⇒ 回 Go 世界 ⇒ Go runtime 可抢占)。所以抢占延迟有上界,符合 Go runtime 对协作式抢占的隐含约定(等价 STW 延迟数十微秒到毫秒级,与 wazero 同档)。

#### 1.2.4 与 P3 PW9-a gcPending inline 的对位

P3 在 [../p3-wasm-tier/05-safepoint-gc](../p3-wasm-tier/05-safepoint-gc.md) §1.3 已定:回边 inline 一次 `i32.load $gcPending` + 条件跳助手——一样的形式,只是物理通道是 wazero linear memory 加一次条件分支,P4 是机器寄存器加一次条件分支。`gcPending` 与 `preemptFlag` 在 §3.3 的 jitContext 是相邻字段(都是 i32),回边检查点可一次 load 两字 + 单次 test(per-arch 优化,留 [./06-backends](./06-backends.md))。

跨层退出语义对位:
- **P3 PW9-a**:gcPending 命中 ⇒ 调 imported helper $h_safepoint ⇒ 经 wazero ↔ Go 边界出去 ⇒ Go 侧 collect ⇒ 回 wazero 续跑;
- **P4**:preemptFlag/gcPending 命中 ⇒ 经 trampoline 出 ⇒ Go 侧 helper(Gosched 或 collect)⇒ trampoline 进回 JIT 续跑。

**两层在「回边检查 + 跨层让出」语义上同构**,只是 P4 把 wazero 内部的边界跨越展开成自管 trampoline 的进出。

#### 1.2.5 与 context cancellation(WithCloseOnContextDone 一样的)的协作终止

宿主侧的取消(`Program.Call(ctx, ...)` 的 ctx done)在 P4 经同一通道生效:当 ctx 被取消,Go 侧的取消钩子置 `jitContext.preemptFlag` ⇒ 下一次回边检查点命中 ⇒ JIT 退到边界 ⇒ Go 侧助手发现 ctx 已 done ⇒ 抛 cancel error 经 status 链冒泡。这是 issue #4 完成的 `SetCancelHook` 机制([../p1-interpreter/12-testing-difftest.md](../p1-interpreter/12-testing-difftest.md) 公共面契约)在 P4 的物理兑现——P3 经 wazero 的 `WithCloseOnContextDone`(wazero API),P4 自付一样的机制,通道是 jitContext.preemptFlag。

#### 1.2.6 wazero 采石场:epoch interruption / checkexit 标志检查

参考实现:wazero `internal/engine/wazevo` 的 epoch interruption / `checkExit` 机制——同样在循环回边 emit 一次 load 标志 + 条件分支跳到 exit handler。wazero 的标志位住 wazevo 的 ExecutionContext / module instance 字段,P4 的住 `jitContext`,语义同位。

### 1.3 栈移动——morestack 拷 goroutine 栈

#### 1.3.1 问题:Go runtime 的 morestack 会重定位 goroutine 栈,JIT 持栈指针即 UAF

Go 的 goroutine 栈是动态增长的(从 2 KB 起,按需 morestack 把栈扩到新地址 + 拷旧栈过去)。**任何指向 goroutine 栈的指针在 morestack 后立即失效**——这就是「栈移动税」。若 JIT 代码在某处持有指向 Go 栈帧字段的指针(例如把 Go 局部变量地址传进 JIT),一次 morestack 后这指针就指向已 free 的旧栈区域,UAF。

#### 1.3.2 P4 解法:JIT 代码不持任何 Go 栈指针

绝对纪律:**JIT 生成码不接受、不缓存、不写出任何 Go 栈指针**。所有跨边界传入的指针/引用必须是 Go 堆对象指针(Go 堆对象不被 morestack 重定位)或自管内存偏移(arena GCRef / 自管栈偏移,绝对不动)。

#### 1.3.3 进入前所需指针(arena base / 值栈 base / helper 表)装入 jitContext(Go 堆对象)

JIT 函数需要的所有「外部入口」装入一个 `jitContext` 结构(§3.3 完整字段表):arena base、值栈 base、preemptFlag/gcPending 标志、helper 函数表、exit 原因码等。`jitContext` 是从 Go 堆 `&jitCtx{...}` 分配的对象,Go GC 管它(对 Go GC 是普通含指针的 struct),**且 Go 堆对象不被 morestack 重定位**——morestack 只搬 goroutine 栈,Go 堆地址恒定。

#### 1.3.4 jitContext 经固定寄存器传入,Go 堆对象不移动 → context 指针稳定

trampoline「进」asm 把 jitContext 的指针装入一个**固定寄存器**(per-arch 约定,如 amd64 用 R15、arm64 用 X28——具体留 [./06-backends](./06-backends.md)),JIT 生成码经该寄存器间接寻址访问 jitContext 字段:

```
;; 概念伪码,具体 asm 在 06-backends
mov  rax, [r15 + offset_arenaBase]    ;; 读 jitContext.arenaBase
mov  rbx, [r15 + offset_helperTab]     ;; 读 helper 表起点
;; ...
```

这条「固定寄存器存 jitContext」纪律是「JIT 不持 Go 栈指针」的**结构性兑现**——jitContext 自身不在 Go 栈、固定寄存器恒指向同一 Go 堆对象、Go 堆对象不被 morestack 搬,三件事合起来让「持指针」从「需小心避免」升级为「物理上不可能」。

#### 1.3.5 与 [../p1-interpreter/01-value-object-model](../p1-interpreter/01-value-object-model.md) §2 GCRef 非 Go 指针纪律同源

P1 第一天就定:**arena 内对象互引用是 GCRef = 48-bit 字节偏移,不是 Go 指针**([../p1-interpreter/01-value-object-model](../p1-interpreter/01-value-object-model.md) §2 与本文 §1.4 写屏障白赚的一样的根因)。这条纪律本就为「不让 Go GC 看到 arena 内部图」设计,顺便让 arena 内部引用对栈移动也免疫——arena 对象间用偏移寻址,arena 整体在 Go 堆上的地址变化(grow 后 realloc 到新 backing)不影响内部引用,只需 Go 侧重取一次 backing slice 视图([../p1-interpreter/06-memory-gc](../p1-interpreter/06-memory-gc.md) §3)。

P4 把这条纪律延伸到机器寄存器层:JIT 生成码持有的「arena 内位置」全部是相对 jitContext.arenaBase 的偏移(R(i) 槽 = arenaBase + valueStackByteOffset + 8*i),从 jitContext 重读 arenaBase 即可在 grow 后续跑(§5)。**P4 没有「指针失效」概念,只有「偏移基址重读」概念**。

#### 1.3.6 wazero 采石场:trampoline 只传 module context 指针

参考实现:wazero `internal/engine/wazevo` 的 native entry 只传一个执行上下文指针入口(wazevo 的 `ExecutionContext`),生成码经该指针读 module 状态(linear memory base、function table、tier 标志等)。P4 jitContext 是 wazero ExecutionContext 的同位物——单一 Go 堆对象、固定寄存器、间接寻址。

### 1.4 写屏障——裸指针写破三色不变式

#### 1.4.1 问题:JIT 直接写 Go 堆指针会绕过 Go GC 写屏障(三色失序)

Go 的并发 GC 经写屏障维护三色不变式——所有「黑色对象写白色对象引用」必须通过 `runtime.gcWriteBarrier` 通知 GC,否则会让本应回收的白对象漏标活、或本应活的对象漏标可达。**JIT 直接 mov 一个 Go 堆指针到 Go 堆字段就是绕过写屏障**——Go runtime 完全不知道这次写,三色失序后果是 GC 误回收活对象 ⇒ memory corruption。

更糟:Go runtime 对 `runtime.gcWriteBarrier` 的内部实现版本漂移([../roadmap.md](../roadmap.md) §6 非目标已点名「绝不 inline 复刻 runtime 内部符号」),P4 不能在生成码里 inline 复刻,也不能 `go:linkname` 绕进去。**唯一解法是不写 Go 堆指针**。

#### 1.4.2 P4 解法:白赚——值世界已在 arena,JIT 写栈槽/表槽写的是自管内存里的 u64,Go GC 不可见,无屏障义务

承 [../p1-interpreter/01-value-object-model](../p1-interpreter/01-value-object-model.md) §2 + [../../../llmdoc/must/design-premises.md](../../../llmdoc/must/design-premises.md) 前提四:**Lua 值表示从第一天起就是 NaN-boxed u64,值世界住自管 arena**(Go 堆 `make([]uint64, n)` 或 P3 build 下的 wazero linear memory)。arena 内对象互相引用是 GCRef = 字节偏移,不是 Go 指针。

由此推论:**JIT 生成码所有写操作的目标都是「自管内存里的一个 u64 槽」**——值栈槽 `R(i) = arena.words[base + i]`(写 NaN-boxed u64)、表槽(写 GCRef = u48 偏移)、upvalue 槽(同上)。这些写**对 Go GC 完全不可见**——Go GC 看到的只是一段 `[]uint64` backing(无指针),内部偏移无意义。**写屏障义务在物理上不存在**。

这条「白赚」是第一天承诺的最大现金红利,也是 P4 系统管线最不需要操心的一项。它的兑现条件:严格遵守 [../p1-interpreter/01-value-object-model](../p1-interpreter/01-value-object-model.md) §7 不变式 1「值即 8 字节,跨 tier 拷贝是 memmove」——P4 生成码与 P1 解释器、P3 wazero 生成码读写**完全同一字节布局的同一块物理内存**,无格式转换。

#### 1.4.3 与 P1 第一天承诺的红利(承 [../../../llmdoc/must/design-premises.md](../../../llmdoc/must/design-premises.md) 前提四)

回顾设计承诺源:[../../../llmdoc/must/design-premises.md](../../../llmdoc/must/design-premises.md) 前提四「第一天即锁定的值表示承诺」原文:

> **代价(必须自付)**:**自写 mark-sweep GC**,纪律约束是 **safepoint 限定在分配点与层边界**、**根放 shadow stack**。

「自写 GC」的代价是真的(P1 06 已付),但它换来的是「写屏障税在 P4 物理上不存在」——这是当年那个不可逆决策最直接的 P4 兑现。若 P1 当年选了 Go tagged struct,这里 P4 的写屏障要么走 Go runtime 内部符号(被 [../roadmap.md](../roadmap.md) §6 非目标禁掉),要么 P4 直接做不了。**写屏障税从「四项税之一」变成「白赚」**,是前提四在 P4 兑付的现金。

#### 1.4.4 wazero 采石场:linear memory 即一样的自管值世界

参考实现:wazero linear memory 是 `[]byte` backing,Wasm 生成码所有 i32.store/i64.store 指令的目标都是该 backing 内偏移——Go GC 完全不可见。P4 arena 与 wazero linear memory 在「自管值世界 ⇒ 写屏障白赚」这条上**物理同源**:都是「Go 堆上一段不含指针的字节数组」,Go GC 把它当普通整数数组看,内部如何被 JIT 写入与 Go GC 无关。这也是 [../p3-wasm-tier/03-memory-model](../p3-wasm-tier/03-memory-model.md) §1.2「为什么是『收养』而非『双份』」的对偶面——值世界一旦统一,写屏障同时对两个执行层都白赚。

### 1.5 总结:四项税的物理可行性已被 wazero 验证(采石场可逐项参考)

四项税逐项归档:

| 税 | P4 概念方案 | 在 P1 已付的代价 | 在 P4 自付的成本 | wazero 采石场 |
|---|---|---|---|---|
| **GC 精确栈扫描** | 自管机器栈 + trampoline 切 SP,边界 syscall 语义 | 调用链状态全在 arena(P1 05 §7) | trampoline 进出 asm + 自管栈池化 | wazevo backend native call 栈切换 |
| **异步抢占** | 回边插 preemptFlag 检查 + 经 trampoline 让出 | 解释器 opcode 末尾检查机制(P1 05 §5.3) | 回边检查点指令(几条)+ exit stub | epoch interruption / checkexit 标志 |
| **栈移动** | jitContext 经固定寄存器,Go 堆对象不移动 | GCRef 非 Go 指针(P1 01 §2) | 固定寄存器约定 + 间接寻址纪律 | trampoline 只传执行上下文指针(wazevo ExecutionContext)|
| **写屏障** | **白赚**:值世界已在 arena,Go GC 不可见 | 自写 mark-sweep GC(P1 06 全卷) | 0(物理上不存在) | linear memory 同源 |

**关键结论:四项税在 P4 不是新发明的难题,而是 P1 第一天承诺 + wazero 已验证的存在性 + 一笔系统管线工程**——逐项可解,且每项都有现成蓝本。这是 [./02-template-direction.md](./02-template-direction.md) §1.3 「实现成本与收益在曲线上严重凸性」论证的物理依据:四项税自付的工程量是固定的(几千行 trampoline + 系统管线),不会因 P4 后端复杂度而成倍数增加;P4 真正的人年级投入(+1-2 人年)在生成码质量与 IC 投机的微调上。

---

## 2. 系统管线四件套

四件套:exec mmap / W^X / icache flush / trampoline。本节给出每件的概念方案、wazero 采石场指针、平台兼容矩阵。

### 2.1 exec mmap

#### 2.1.1 协议:匿名 mmap PROT_READ|PROT_WRITE 写入 → mprotect PROT_READ|PROT_EXEC 翻面

代码页生命周期五步:

```
Step 1: 编译器决定要发射的字节数 N(per-Proto 段大小,§2.1.2 池化)
Step 2: mmap(NULL, N, PROT_READ|PROT_WRITE, MAP_ANON|MAP_PRIVATE, -1, 0)
        ⇒ 拿到 N 字节可读可写匿名页 codePage(不可执行)
Step 3: 把发射的字节序列(机器指令编码)写入 codePage[0..N)
        ⇒ 此时该页对 GC/runtime 是普通 []byte
Step 4: mprotect(codePage, N, PROT_READ|PROT_EXEC)
        ⇒ 翻面:从「可写不可执行」到「可读可执行不可写」
Step 5: arm64 上额外做 icache flush(§2.3);amd64 硬件保证一致性,跳过
        ⇒ 翻面后该 codePage 即可作为函数地址被 trampoline 跳入
```

第 4 步的 mprotect 是核心:翻面**之前**该页不可执行(防止半成品被误执行)、翻面**之后**该页不可写(防止 JIT 后被恶意篡改)。两态分离即 W^X(§2.2)。

#### 2.1.2 代码页池化(per-Proto 段 + 释放策略)

每个被升层的 Proto 拿一段 codePage,大小由发射器估算(linear scan 完知道总字节数)。代码页**不混合 Proto**:每段一个 mmap call,边界对齐到 OS 页大小(通常 4 KB / 16 KB)。原因:

- (a) **释放粒度** = 单 Proto:Proto 被回收(P2 fallback 后 GibbousCode.Dispose,[../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md) §6.1)时,直接 munmap 该段即归还内存;若多 Proto 共段,得做内部 freelist + 碎片管理,工程量倍增;
- (b) **mprotect 粒度** = 单 Proto:翻面只针对当前 Proto 的段,不影响其它已运行段(否则要么停掉所有其它段、要么放弃 W^X);
- (c) **可观测性**:profile / pprof 看到的是「per-Proto 一段连续指令」,符号化与 backtrace 符合预期(详见 [./06-backends](./06-backends.md) 调试支持节)。

代价:小 Proto 单独占一页(最小分配粒度 = OS 页),内存浪费。缓解:P2 F5 大函数检查已限编译单元尺寸下限(太小不升层),最小升层目标尺寸的 Proto 至少能填半页;若实测浪费超阈值再做合并(留 [./06-backends](./06-backends.md) tuning)。

#### 2.1.3 释放策略与 GibbousCode.Dispose 协议对接(承 [../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md) §6.1)

`GibbousCode` 接口在 P2 已定:`Dispose()` 释放后端资源。P3 实现下 Dispose ⇒ wazero module Close;P4 实现下 Dispose ⇒ munmap 当前段 + 释放 jitContext。

释放时机:
- **fallback 触发**:P2 状态机决定该 Proto 永久解释([../p2-bridge/04-try-compile-fallback](../p2-bridge/04-try-compile-fallback.md) §1 fallback 单向)⇒ Dispose 该 Proto 的代码段;
- **deopt 重编译触发**(P4 独有,[./04-osr-deopt.md](./04-osr-deopt.md) §5):旧投机失败 ⇒ Dispose 旧段 + 编译新段(去投机);
- **Program 整体回收**:Program close ⇒ 遍历所有 Proto Dispose。

**关键纪律**:Dispose 的实际 munmap 必须保证「该段的所有调用者都已退出」——若有 goroutine 正在该段执行,munmap 等于 UAF(SIGSEGV)。在 P2 状态机里 Dispose 触发点都是「升层失败/降层」时刻,该段此刻没有活跃调用(状态转换前已经 quiesce),工程上由 P2 状态机保证;但**多 State 并发**下若某 State A 触发某 Proto Dispose,而 State B 仍在该段执行,则不安全——这是「多 State 并发下 JIT 代码与 profile 的共享语义」开放缺口的一部分,留 [./06-backends](./06-backends.md) tuning(可能解法:引用计数 + 延迟 munmap)。

#### 2.1.4 wazero 采石场指针

参考实现:wazero `internal/platform` 的 `MmapCodeSegment` 函数族——平台分流(linux_amd64.go / linux_arm64.go / darwin.go / windows.go / freebsd.go),封装了「mmap RW + 写入 + mprotect RX」三步。每平台对系统调用的 syscall 包装是 P4 直接可移植的代码。

### 2.2 W^X(Write-XOR-Execute)

#### 2.2.1 任何时刻不持 RWX 页(macOS/iOS/Android 强制 + Linux 安全收紧)

W^X 原则:**任何代码页要么可写不可执行,要么可读可执行不可写,从不同时可写又可执行**。这是现代 OS 的硬约束:

- **macOS / iOS / Android**:OS 层强制(macOS arm64 下 default 不允许 RWX 页;iOS 完全禁 mmap with PROT_EXEC + PROT_WRITE);若程序硬要 RWX,被 sandbox / Hardened Runtime 直接拒;
- **Linux**:不强制但安全敏感场景(systemd 设 `MemoryDenyWriteExecute=yes`、selinux `execmem` 关闭)同样禁;
- **Windows**:DEP(Data Execution Prevention)默认行为允许翻面,但 ACG(Arbitrary Code Guard)启用下禁 RWX。

P4 必须无条件遵守 W^X——这不是「为了好看」,是「不遵守就 deploy 不出去」的硬门槛。

#### 2.2.2 macOS arm64 走 MAP_JIT + pthread_jit_write_protect_np 等价物

macOS arm64(Apple Silicon)对 W^X 的要求最严:即便走 mmap+mprotect,在「写代码 → 翻执行 → 改代码」(自修改 / patch 入口)场景下,每次切换都要经特殊系统调用 `pthread_jit_write_protect_np(false)` / `(true)`(per-thread JIT region 的可写性翻转)。

P4 在 macOS arm64:
- mmap 时加 `MAP_JIT` flag(声明这页是 JIT 用);
- 写入前调 `pthread_jit_write_protect_np(false)` 让本线程允许写 JIT region;
- 写完调 `(true)` 关闭可写;
- 之后该 region 对本线程恒可执行不可写(其它线程同样不可写)。

实现细节(per-thread 状态、与 goroutine 调度的交互):依赖 `golang.org/x/sys/unix` 的对应包装或经 `syscall.Syscall` 直调,**不依赖 cgo**(否则违反项目「纯 Go 不依赖 cgo」基线,[../roadmap.md](../roadmap.md) §0)。具体调用骨架留 [./06-backends](./06-backends.md) macOS arm64 节。

#### 2.2.3 各 OS 平台对 W^X 的硬约束清单(平台兼容矩阵)

| 平台 | exec mmap 可用? | RWX 同时可? | 特殊机制 | 备注 |
|---|---|---|---|---|
| linux/amd64 | ✅ | 否(默认禁,可放宽但不应) | 标准 mmap+mprotect | 主要测试平台 |
| linux/arm64 | ✅ | 否 | 标准 mmap+mprotect + icache flush | 双后端 CI 平台 |
| darwin/amd64 | ✅ | 否 | 标准 mmap+mprotect | Intel Mac |
| darwin/arm64 | ✅(MAP_JIT) | 否(强制) | MAP_JIT + pthread_jit_write_protect_np | Apple Silicon,W^X 最严 |
| windows/amd64 | ✅ | 否 | VirtualAlloc + VirtualProtect | 替代 mmap/mprotect 的 Win API |
| windows/arm64 | ✅ | 否 | 同上 + icache flush | Surface Pro X 类 |
| freebsd/amd64 | ✅ | 否 | 标准 mmap+mprotect | 同 linux |
| ios | ❌(系统禁) | 否 | 无解(运行时无法 JIT) | P4 不支持,P3 退守解释模式([./07-p3-retirement.md](./07-p3-retirement.md) §2) |
| android(non-rooted) | ⚠️(取决于 selinux) | 否 | 取决于设备 | 同 ios 边角 |
| 其它架构(riscv64 / ppc64le 等) | 取决于 OS,但 P4 无后端 | — | — | P4 不支持,退守 P3/解释模式 |

**总结**:P4 兼容平台 = `{linux,darwin,freebsd,windows} × {amd64,arm64}` 的 ~7 种组合(具体数依发布口径定);其它平台或退守 P3 wazero 解释模式 / crescent 直接解释([./07-p3-retirement.md](./07-p3-retirement.md) §2)。

#### 2.2.4 wazero 采石场指针 + Apple 公开 API

参考实现:wazero `internal/platform/mmap_*.go` 各平台的 mmap 子文件——封装了「mmap RW + 写入 + mprotect RX」三步,但**实测 wazero v1.x 的 platform 子包没有 `mmap_darwin_arm64.go`**——wazero 在 darwin/arm64 上走的是普通 mmap + mprotect 翻面(与其他 unix non-linux 共用 `mmap_other.go` / `mmap_unix.go` 路径),**没有 MAP_JIT 标志、没有 `pthread_jit_write_protect_np` 调用**。换言之,**MAP_JIT + pthread_jit_write_protect_np 在 wazero 中无 Go 实现先例可直接移植**。

P4 走 macOS arm64 的 W^X 翻面需求时:**P4 自付一次 spike**(PJ8 阶段):

- **Apple 公开 API**:Darwin pthread(3) man page 字面化的 `pthread_jit_write_protect_np` 函数,经 `syscall.Syscall1` 直调路径(不依赖 cgo);
- **参考其他语言运行时踩坑记录**:.NET runtime issue #41991 / #64880(.NET CoreCLR JIT 在 Apple Silicon 上的实战教训)、Dart sdk #45793(Dart VM JIT)、Bun(JavaScriptCore on macOS arm64 的封装层)——这些项目都自付了 MAP_JIT + pthread_jit_write_protect_np 的实现,P4 可借鉴其 spike 方案与避坑细节;
- **PJ0 / PJ8 开放问题(承 §8.2)**:**P4 是 template JIT,一次 seal 后不再 patch 代码段**——是否真需要 MAP_JIT,还是 RW → 一次性 mprotect RX 即可?PJ0 spike 验证(若纯走 mprotect 翻面 + 一次性 sealing 在 darwin/arm64 上能过 hardened runtime / iOS sandbox,则可省掉 pthread_jit_write_protect_np 调用)。

### 2.3 icache flush

#### 2.3.1 arm64 写码后必须显式 flush(IC IVAU/DC CVAU 序列,经汇编 stub)

arm64 的 i-cache(指令缓存)与 d-cache(数据缓存)默认**不一致**——CPU 执行 store 指令写入内存(经 d-cache),但 i-cache 不知道这字节也是「指令」,会继续按旧 i-cache 内容取指。**写完代码不 flush i-cache,新代码不会被执行**——CPU 跳到新地址,可能取到旧 i-cache 里的过时指令(随机 corruption)或未初始化字节(SIGILL)。

flush 序列(arm64 典型):

```
DC CVAU,Xn        ;; 把 d-cache 里 [Xn] 这行清回内存(clean to point of unification)
DSB ISH           ;; 数据同步屏障,确保 DC 完成
IC IVAU,Xn        ;; 让 i-cache 中 [Xn] 这行失效(invalidate to point of unification)
DSB ISH           ;; 屏障
ISB               ;; 指令同步屏障,让 CPU 重新 fetch
```

实际实现通常按 cache line(64 B 典型)循环这套序列覆盖整个段。具体 asm 与 cache line size 检测留 [./06-backends](./06-backends.md) arm64 节。这段序列必须经 Go 汇编 stub 调出(Go 没有 builtin 暴露这些指令),与 trampoline asm 同源管理。

#### 2.3.2 amd64 硬件保证一致性,无操作

amd64(x86-64)的 i-cache 与 d-cache 在硬件层保证一致——CPU 自动 snoop 写到代码段的 store,无需软件 flush。**P4 在 amd64 上 icache flush 是 no-op**。这是 amd64 内存模型的红利(对 JIT 而言),省一段 asm + 几条指令的运行成本。

但仍有约束:翻面后(mprotect 设 PROT_EXEC),首次执行该段前的 store 必须**对该 CPU 可见**——通常 mprotect 系统调用本身已经在内核里发了必要屏障(确保返回用户态前 store buffer 全 drain),用户态无需额外指令。这是「硬件保证一致性」的精确含义。

#### 2.3.3 与代码页翻面的协同时序

完整翻面 + flush 时序(per-Proto):

```
P4 编译器完成发射:codePage[0..N) 全部写入(CPU 经 d-cache + store buffer)
   │
   ▼
mprotect(codePage, N, PROT_READ|PROT_EXEC)
   │  amd64:内核屏障已让 store 对 CPU 可见;无 i-cache 问题。
   │  arm64:内核屏障让 store 落到内存;但 i-cache 仍可能持旧/空数据。
   ▼
arm64 only: 调用 icache flush stub(per-cache-line 循环 DC CVAU + IC IVAU + DSB + ISB)
   │
   ▼
codePage 现在「可读可执行 + 一致」,可作为函数地址被 trampoline 跳入
```

这段时序写入 [./06-backends](./06-backends.md) arm64 节;本文只规定「mprotect 后 → flush(arm64)→ 可调用」的协议骨架。

#### 2.3.4 wazero 采石场指针

参考实现:wazero `internal/engine/wazevo/backend/isa/arm64/` 与 `internal/platform` 的 arm64 cache flush 路径——wazero 同样在每段 Wasm function 编译完后做一次 IC IVAU + DC CVAU 序列。其 cache line size 检测、按 line 循环、屏障指令选择(DSB ISH vs DSB SY)是 P4 直接可借鉴的。

### 2.4 trampoline

#### 2.4.1 一对汇编 stub:进 + 出

trampoline 是一对 Go 汇编 stub(per-arch),负责跨 Go 世界与 JIT 世界的物理边界。**「进」与「出」对称,是同一段栈语义的正反操作**:

- **进**(`jitEnter` stub):Go 调用方 → JIT 世界。保存 Go callee-saved 寄存器、切 SP 到自管栈、装 jitContext 入固定寄存器、跳入 JIT 段地址。
- **出**(`jitExit` stub):JIT 世界 → Go 调用方。反向恢复:还原 Go SP、还原 callee-saved、按 status 码返回到 Go 调用方。

#### 2.4.2 「进」=保存 Go 被调方寄存器 + 切 SP 到自管栈 + 装 jitContext 入固定寄存器 + 跳入代码

「进」详细序列(概念伪码,具体 asm 在 [./06-backends](./06-backends.md)):

```
jitEnter(jitContext *jitCtx, codeAddr uintptr, base uintptr) {
  // 保存 Go 调用方的 callee-saved 寄存器(per-arch ABI)
  push_callee_saved
  // 把 Go SP 暂存到 jitContext.savedGoSP(出口用)
  jitCtx.savedGoSP = SP
  // 切 SP 到自管栈尾部(自管栈在 jitContext.machineStack)
  SP = jitCtx.machineStack + len(jitCtx.machineStack)
  // 装 jitContext 入固定寄存器(amd64 R15 / arm64 X28,per-arch)
  CTX_REG = jitContext
  // 把 base(arena 内值栈起点字节偏移)装入约定的「入参寄存器」(per-arch ABI)
  ARG0_REG = base
  // 跳入 JIT 代码段
  jmp codeAddr
}
```

JIT 函数体执行期间,SP 在自管栈上、CTX_REG 恒指向 jitContext、ARG0_REG 是当前帧 base。

#### 2.4.3 「出」=反向恢复:正常返回 / OSR exit / 慢路径 helper 调用三种出口共用

「出」三种出口共用同一段恢复 stub(§4.4):JIT 函数从任一出口跳到该 stub,stub 反向恢复:

```
jitExit(status int32) -> Go 世界 {
  // 恢复 Go SP(从 jitContext.savedGoSP)
  SP = jitCtx.savedGoSP
  // 恢复 Go callee-saved 寄存器
  pop_callee_saved
  // 把 status 装入 Go 函数返回值寄存器
  RET_REG = status
  // 返回到 Go 调用方
  ret
}
```

status 码区分三出口(承 [../p3-wasm-tier/04-trampoline](../p3-wasm-tier/04-trampoline.md) §0.4 + P4 扩展):
- `STATUS_OK = 0`:正常返回(出口 A)
- `STATUS_ERR = 1`:错误冒泡(同 P3,经 status 链)
- `STATUS_DEOPT = 2`:OSR exit(出口 B,P4 独有,[./04-osr-deopt](./04-osr-deopt.md) §3.3)
- `STATUS_HELPER_CALL = 3`:慢路径 helper 调用(出口 C,§4.3)
- (其它 status 码留 §4 协议补充)

详细出口协议见本文 §4。

#### 2.4.4 trampoline 形式参数化:per-arch(具体 asm 在 06-backends)

trampoline 的两段 stub 是 P4 中**唯二**完全 per-arch 的代码段:每架构一份 `jitEnter` + `jitExit`,寄存器约定、SP 切换、callee-saved 保存集合全按该架构 ABI 写。

但 trampoline 之外,JIT 生成码内部「该用哪个寄存器存 jitContext / 该用哪个寄存器存 base」这些 ABI 约定一旦在 trampoline 定下,生成码侧就照该约定发射,**架构无关骨架(发射驱动 / 模板选型 / OSR 接线)零变更**。这是 [./06-backends.md](./06-backends.md) §5.1 候选 (b)「共享骨架 + per-arch 发射器」的物理体现。

#### 2.4.5 wazero 采石场:wazevo backend abi_entry asm 等

参考实现:wazero `internal/engine/wazevo/backend/isa/amd64/abi_entry_amd64.s` 与 `internal/engine/wazevo/backend/isa/arm64/abi_entry_arm64.s` 的 native entry / exit asm——即「Go 调用方 → wazero 自管执行环境 → Go 调用方」的两段 stub,寄存器保存 + SP 切换 + 跳入 / 返回的完整序列。P4 可以直接以这两段为骨架做 wholesale 移植,只需替换 ABI 寄存器选用与 jitContext 字段偏移。

---

## 3. JIT 执行上下文与世界边界

### 3.1 两个世界:Go 世界(goroutine 栈 + Go 帧)vs 自管世界(Go 堆 + arena)

P4 运行时存在两个泾渭分明的世界,以 trampoline 为唯一边界:

```
                                          P4 世界边界全图
   Go 世界(goroutine 栈,正常 Go 帧)                     自管世界(Go 堆 + arena,JIT 执行)
   有 stack map, GC 看得见, 可被抢占                       Go GC 不可见, 不可被异步抢占
  ┌─────────────────────────────────────┐               ┌──────────────────────────────────────────────┐
  │                                     │               │                                              │
  │  Program.Call / bridge              │               │  自管机器栈 ([]byte, Go 堆):                  │
  │   - 升层决策 (P2 状态机)             │               │   ┌──────────────────────────────┐           │
  │   - 编译 / 装载 (mmap+mprotect)      │  trampoline 进 │   │ spill 区: caller-saved 暂存   │           │
  │   - 装 jitContext, 调 jitEnter stub  │ ──────────────►│   │ 返址区: per-call return addr  │           │
  │                                     │   ARG0=base   │   │ 调用约定保留位 (red zone 等)   │           │
  │                                     │   CTX_REG=ctx │   │ JIT 函数体局部 (机器寄存器溢出) │           │
  │                                     │               │   └──────────────────────────────┘           │
  │  慢路径 helper (Go 函数):            │  trampoline 出 │                                              │
  │   - 元方法分派 ($h_arith 等)         │ ◄──────────── │  jitContext (Go 堆 *jitCtx):                  │
  │   - arena 扩容 (Alloc 慢路径)        │   3 种出口     │   ┌─────────────────────────────────────┐    │
  │   - 抛错 / pcall 边界清理            │  status 码区分 │   │ arenaBase: arena.bytes 起点          │    │
  │   - host call (callHost)             │  A 正常返回    │   │ valueStackBase: 当前 thread 值栈偏移 │    │
  │   - GC collect (Alloc 内 触发)       │  B OSR exit   │   │ preemptFlag (i32):                  │    │
  │                                     │  C helper 调用 │   │   0=正常, 1=ctx done/抢占请求         │    │
  │                                     │               │   │ gcPending (i32):                     │    │
  │  goroutine 栈对 GC 完全可见:         │               │   │   0=正常, 1=本线程已置 STW 待行       │    │
  │   trampoline 进入 stub 是 Go 函数,    │               │   │ helperTab: helper 函数指针表 (Go 堆) │    │
  │   有正常 stack map; 其下 Go 帧 同样.   │               │   │ exitReason (i32): exit 时由 JIT 写   │    │
  │   GC 扫栈在 trampoline 进入帧停止.    │               │   │ exitPC (i32): exit 时记录 bytecode pc │    │
  │                                     │               │   │ savedGoSP (uintptr): jitEnter 暂存    │    │
  │                                     │               │   │ machineStack []byte: 自管栈 backing   │    │
  │                                     │               │   │ ... (per-arch 扩展, 留 06-backends)   │    │
  │                                     │               │   └─────────────────────────────────────┘    │
  │                                     │               │                                              │
  │                                     │               │  arena (= P3 的一样的共见值世界):              │
  │                                     │               │   ┌─────────────────────────────────────┐    │
  │                                     │               │   │ 值栈: NaN-boxed u64 槽 (R(0..n))     │    │
  │                                     │               │   │ CallInfo 段: 每帧 4-5 word, 链式      │    │
  │                                     │               │   │ Table / Closure / Upvalue / String 等 │    │
  │                                     │               │   │ ——与 crescent / wazero (P3) 共见同一  │    │
  │                                     │               │   │   块物理内存 ([P1 06] §1)              │    │
  │                                     │               │   └─────────────────────────────────────┘    │
  └─────────────────────────────────────┘               └──────────────────────────────────────────────┘
   ↑ Go runtime 管这一侧                                  ↑ P4 自管这一侧
   ↑ Go GC / 抢占 / morestack 自由穿越                    ↑ Go GC / 抢占 / morestack 不可见
```

### 3.2 边界穿越的方向:Go → JIT(经 trampoline 进)/ JIT → Go(经 trampoline 出 / 三种出口)

边界**单向跨越,成对出现**:每次 `jitEnter` 终将以一次 `jitExit` 结束(可能经多次「helper 出 / 进」嵌套,但最外层一定平衡)。穿越次数与 Lua 跨层调用次数同序,与 P3 一致(差异只是物理通道是原生 call 不是 wazero imported)。

| 方向 | 触发 | 物理动作 | 频率 |
|---|---|---|---|
| Go → JIT | P2 状态机决定升层后首次入函数 / OSR exit 后回 JIT 续跑 / 慢路径 helper 完成回 JIT | trampoline 进 stub:保存 Go 寄存器、切 SP、装 jitContext、跳入代码地址 | 每次进入热函数一次 + helper 出后回来一次 |
| JIT → Go(出口 A 正常返回) | RETURN 指令模板尾部 | jitExit stub:status=0,反向恢复 | 每次函数返回 |
| JIT → Go(出口 B OSR exit) | guard 失败(类型投机错) | jitExit stub:status=2 + 写 exitReason/exitPC,然后由 crescent 接管续跑([./04-osr-deopt](./04-osr-deopt.md) §3.3) | 投机失败时 |
| JIT → Go(出口 C 慢路径 helper) | 元方法 / 分配慢路径 / arena 扩容 / 抛错 / host call | jitExit stub:status=3 + 写 helper id 到 exitReason,Go 侧 dispatcher 调对应 helper,helper 返回后 trampoline 进回 JIT | 每次助手调用一次 |

### 3.3 jitContext 字段(完整字段表)

`jitContext` 是 P4 系统管线的核心数据结构——Go 堆对象,生命周期与 Thread 同(每 Thread 一份,与 P3 wazero 的 ExecutionContext 同位)。

**2026-07-02 实现勘误——字段以 `internal/gibbous/jit/jitcontext.go` 为准**:早前草稿列的 `callInfoBase` / `gcPending` / `helperTab` / `exitPC` / `exitHelperID` / `exitArg1` / `machineStack` / `savedGoSP` 等字段并未完成,当前实现用不同的机制承担同一职责(见 §4.3 exit-reason 协议 + `RefreshJitCtxAddrs` 批量刷新)。真实字段如下:

```go
// internal/gibbous/jit/jitcontext.go(P4 build,//go:build wangshu_p4)
type JITContext struct {
    // ----- 第 1 组:arena 寻址基址(承 §1.3.4 + §5,由 RefreshJitCtxAddrs 批量写) -----
    arenaBase      uintptr        // arena []byte 起点的绝对字节地址
    valueStackBase uintptr        // 当前帧 R0 的绝对字节地址(不是相对偏移)

    // ----- 第 2 组:safepoint / 抢占协作 -----
    preemptFlag    atomic.Uint32  // 抢占 / ctx-cancel 协作位;JIT 段回边 inline cmpb + jne 检查

    // ----- 第 3 组:exit-reason 通信字段(承 §4.3;JIT 写,dispatcher 读) -----
    exitReasonCode uint32         // ExitNormal=0 / ExitError=1 / ExitOSR=2 / ExitInlineHelper=3
    exitArg0       uint64         // 打包字段(§4.3):bits 0..15 helper code,bits 16..23 A,
                                  //                  bits 24..32 B,bits 33..41 C,bits 42..63 pc
    resumeOff      uint32         // ExitInlineHelper 续跑入口:codePageAddr + resumeOff
    _              [4]byte        // 8 字节对齐 padding
    codePageAddr   uintptr        // mmap 段起点(dispatcher 用它 + resumeOff 算续跑绝对地址)

    // ----- 第 4 组:自管 spill 栈 backing(issue #89 已接线,trampoline 进段切 SP) -----
    spillBase      uintptr        // 自管机器栈 backing 高地址端(对齐后的 SP 进入点)
    spillTop       uintptr        // 自管机器栈低地址端(向下生长上界)
    savedGoSP      uintptr        // 切 SP 期间暂存的 goroutine SP,出段后切回用

    // ----- 第 5 组:crescent 镜像字 host 地址(承 P3 PW10 R2 复用) -----
    ciDepthAddr    uintptr        // thread.ciDepth 镜像字的 host 绝对地址
    ciSegBaseAddr  uintptr        // CI 段可重定位基址的 host 绝对地址
    topAddr        uintptr        // thread.top 镜像字的 host 绝对地址

    // ----- 第 6 组:PJ10 native emit ABI 支持字段 -----
    savedGoG       uintptr        // Run 入口经 saveGoG snapshot 的 Go G(amd64 R14);
                                  // shim 调用路径前 emit `mov r14, [r15+savedGoGOff]` 恢复
    hostRef        [2]uintptr     // P4HostState 接口头(itab + data);shim 反构接口做方法分派
}

// exit-reason 状态码(承 §4.3 dispatcher 分派):
const (
    ExitNormal       uint32 = 0
    ExitError        uint32 = 1
    ExitOSR          uint32 = 2
    ExitInlineHelper uint32 = 3
)

// helper 码(exitArg0 的低 16 bit,承 §4.3):
const (
    HelperRunCallee   uint64 = 1   // Spike 1 遗留
    HelperGrowStack   uint64 = 2
    HelperGCBarrier   uint64 = 3
    HelperGetTable    uint64 = 10  // PJ10 exit-reason 用的 op helper 码起点
    HelperSetTable    uint64 = 11
    HelperGetGlobal   uint64 = 12
    HelperSetGlobal   uint64 = 13
    HelperGetUpval    uint64 = 14
    HelperSetUpval    uint64 = 15
    HelperNewTable    uint64 = 16
    HelperSelf        uint64 = 17
    HelperUnm         uint64 = 18
    HelperLen         uint64 = 19
    HelperConcat      uint64 = 20
    HelperSetList     uint64 = 21
    HelperArithSlow   uint64 = 22
    HelperCompareSlow uint64 = 23
    HelperCall        uint64 = 24
    HelperReturn      uint64 = 25  // 终止性:dispatcher 走 DoReturn 后不再回段
)
const HelperCodeMask uint64 = 0xFFFF
```

字段分组说明:
- **第 1 组**:JIT 段经 `[r15+arenaBaseOff]` / `[r15+valueStackBaseOff]` 间接寻址;这两字段的刷新走 `P4HostState.RefreshJitCtxAddrs`(§3.5),Run 入口与每次 exit-reason 回段前各刷一次。
- **第 2 组**:回边 inline 一次 byte-load + jne(§1.2.2 + §6.3);`preemptFlag` 是 `atomic.Uint32` 但只取 0/1,故字节比较正确。
- **第 3 组**:exit-reason 协议(§4.3)——JIT 段把 helper code + 打包参数写 `exitArg0`,把续跑偏移写 `resumeOff`,`ret` 出段;`nativeCode.Run` 的 dispatcher 读这些字段决定下一步,并经 `codePageAddr + resumeOff` 重入段。旧稿的独立字段 `helperTab` / `exitPC` / `exitHelperID` / `exitArg1` 都由这个统一通道承担。
- **第 4 组**:自管 spill 栈的 backing 字段(issue #89 已接线)。`NewJITContext` 调 `AllocSpillStack` 从 Go 堆分配 64 KiB `[]byte`,`spillBase` = 对齐后的高地址端,`spillTop` = 低地址端;trampoline 进段前把 goroutine SP 暂存到 `savedGoSP` 再把 SP 切到 `spillBase`,出段后切回。深度 seg2seg 递归的每层 `sub sp` 消耗这块自管栈,不再吃 goroutine 栈的 ~800 B NOSPLIT 余量,因此 `segToSegDepthCap` 从 PR #86 的保守值 16 抬回 128(承 §7.5 + [06 §4.1.5](./06-backends.md))。偏移常量 `JITContextSpillBaseOffset` / `JITContextSavedGoSPOffset` + `TestSpillStackLayout` 断言 `.s` 文件里硬编码的偏移与 struct 一致。
- **第 5 组**:承 P3 PW10 R2 的镜像字机制(`crescent.State.ciDepthRef` / `ciSegBaseRef` / `topRef`),这里存的是 host 绝对字节地址,mmap 段解引后直接 inc/dec/写。
- **第 6 组**:PJ10 native emit 引入。`savedGoG` 承 Go ABIInternal 对 R14 = G 的不变约束——mmap 段调 Go shim 前必须先把 R14 恢复成 G,否则 Go 函数序言 `morestack` / `getg` / stack-guard 会读到垃圾。`hostRef` 是把 `P4HostState` 接口头以 `[2]uintptr` 存放,shim 反构后做方法分派,以免 `jit` 包硬 import `peroptranslator`。

### 3.4 自管机器栈布局:spill 区 / 返址区 / 调用约定保留位

自管机器栈是 Go 堆 `[]byte`(`make([]byte, JitMachineStackSize)`),典型 size 64 KiB(per-Proto 相关的栈深度估算 + 余量,具体见 [./06-backends](./06-backends.md))。栈布局(向下生长架构如 amd64/arm64,栈底为 backing 末端):

```
               自管机器栈布局(amd64/arm64,向下生长)
   高地址端(栈底, machineStack[len-1])
  ┌─────────────────────────────────────────┐
  │ trampoline 进入时 SP 起点(stack base)   │   ← jitContext.machineStackBase
  ├─────────────────────────────────────────┤
  │                                         │
  │  JIT 函数 1 帧:                         │
  │   ├ caller-saved 寄存器 spill 区         │
  │   ├ return address(call helper 时压)     │
  │   ├ 局部寄存器溢出槽                      │
  │   └ 调用约定保留位(red zone / 对齐填充)  │
  │                                         │
  ├─────────────────────────────────────────┤
  │  JIT 函数 2 帧(被 1 调用):              │
  │   ├ ...                                  │
  ├─────────────────────────────────────────┤
  │  ...                                    │
  ├─────────────────────────────────────────┤
  │  当前 SP                                 │   ← 当前 JIT 执行 SP
  ├─────────────────────────────────────────┤
  │ ...未使用空间 (栈深度上限保护)            │
  └─────────────────────────────────────────┘
   低地址端(machineStack[0], 栈溢出 guard 页可选)
```

关键约束:
- **不放任何 Lua 值**:Lua 值都在 arena 值栈,自管机器栈只存「机器级临时数据」(寄存器溢出 / 返址 / 对齐);
- **栈深度有界**:由 P4 编译器 linear scan 时计算每个 Proto 的最大栈深(per-call helper 嵌套的临时数据栈帧数),超过 size 上限 ⇒ 自管栈溢出 ⇒ 经 helper 触发 deopt(降回 crescent 解释,工程上是边角,留 [./06-backends](./06-backends.md));
- **栈不被 GC 看到**:`[]byte` 对 Go GC 是字节数组,不持指针(spill 的是寄存器值,寄存器值要么是 NaN-boxed u64 / 偏移 / 数值,无 Go 堆指针——纪律承 §1.3.2)。

### 3.5 arena = 共见物理内存(承 [../p3-wasm-tier/03-memory-model](../p3-wasm-tier/03-memory-model.md) + [../p1-interpreter/06-memory-gc](../p1-interpreter/06-memory-gc.md))

P4 阶段的 arena 物理形式:
- **P3 退役场景**(参 [./07-p3-retirement.md](./07-p3-retirement.md) §3 缺省倾向):arena backing 切回 P1 形式,纯 Go 堆 `make([]uint64, n)`(承 [../p3-wasm-tier/03-memory-model](../p3-wasm-tier/03-memory-model.md) §0.4「P4 唯一的差异是不再经 wazero memory 中介」),`BackingFn` 切回 `DefaultBacking`;
- **P3 留作可移植中层场景**(同上,翻案条件留下):P3 build 与 P4 build 共存,arena backing 仍走 wazero memory(P3 build 路径),P4 与 P3 共见同一块 wazero memory——这是**结构允许共存**的物理体现([./07-p3-retirement.md](./07-p3-retirement.md) §3)。

无论哪种形式,**P4 生成码读写 arena 的语义同一**:经 jitContext.arenaBase 间接寻址,GCRef 偏移寻址语义同一。这是 P3 P4 同 tier「只换发射后端」的物理含义。

**2026-07-02 实现勘误——jitCtx 地址刷新时机**:实现里 arena 相关的五个字段(`arenaBase` / `valueStackBase` / `ciDepthAddr` / `ciSegBaseAddr` / `topAddr`)由 `P4HostState.RefreshJitCtxAddrs(ctx, base)` 批量写入,统一调用点两处:(a) `nativeCode.Run`(以及 `p4Code.Run` / `PerOpCode.Run`)入口——保证首次进入 mmap 段前地址反映当前 arena backing;(b) 每次 exit-reason 出口回来后、再次进入 mmap 段前(§4.3 循环里)——因为任一 host 方法都可能触发 arena grow / 值栈搬家。「批量刷新」是把之前每字段单独一次 host 调用(五次 arena.Words() slice header 派生)合成一次,减轻边界重的 op 的 host 调用成本。这条纪律正是本文 §3.6 不变式 2 与 §5 arena base 重载协议的完成路径。

### 3.6 边界纪律(三条不变式)

升格成 P4 不变式(§7 聚合):

#### 3.6.1 不变式 1:JIT 码内不调用任何普通 Go 函数(承旧 §4.3.1)

JIT 生成码的「调用」指令只允许:
- (a) 调另一段 JIT 代码(被调函数也已升 gibbous-jit ⇒ 同世界内直跳,共享自管栈);
- (b) 调 helper 表中的 Go 函数(经 trampoline 出 + helper 跑 Go + trampoline 进,§4.3);
- (c) 退到 trampoline 出口(三出口任一)。

**绝对禁止**:JIT 生成码不能直接 `call <Go 函数地址>`——这违反 (a) 因为 Go 函数有 stack map / 用 goroutine 栈 / 可被抢占,JIT 直接跳进去会让所有四项税防线崩塌。所有 Go 侧能力(元方法 / arena 扩容 / 抛错 / host call / collect)一律经 trampoline + helper 表(§4.3)。

#### 3.6.2 不变式 2:arena base 在两个 safepoint 之间稳定(承旧 §4.3.2)

arena 整体可能在分配慢路径触发 grow ⇒ realloc 到 Go 堆新地址 ⇒ arena.bytes 起始指针变化。但这种重定位**只在分配慢路径(出 JIT 世界)发生**:

- JIT 内联的 bump 分配(若启用,留 [./06-backends](./06-backends.md))只做「base + bump 比对 cap」并落槽,**越界即出去**(经 helper $h_alloc_slow);
- 越界出去 ⇒ Go 侧 helper 触发 arena.grow ⇒ helper 把新 arenaBase 写入 jitContext ⇒ helper 返回 ⇒ trampoline 进回 JIT ⇒ JIT 重新 load jitContext.arenaBase。

**两个 safepoint 之间(无 helper 出)arenaBase 恒定**——JIT 生成码可以一次 load arenaBase 到机器寄存器,在直线段内复用(per-基本块,跨 helper 调用必须重 load)。详见 §5。

#### 3.6.3 不变式 3:混层调用走统一 CallInfo 协议(承旧 §4.3.3)

P4 帧与 crescent 帧、P3 wazero 帧共享同一 CallInfo 结构([../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md) §1.2 word 布局,bit50 `callStatus_gibbous` 在 P4 同样标 1,承 [../p3-wasm-tier/04-trampoline](../p3-wasm-tier/04-trampoline.md) §0.4)。**跨层调用经统一 CallInfo,不发明新调用记录**——

- gibbous-jit 帧 CALL 一个未升层的 Proto ⇒ 经 trampoline 出到 crescent,`doCall` 跑解释;
- gibbous-jit 帧 CALL 一个 host fn ⇒ 经 trampoline 出到 callHost,host shadow stack 纪律不变;
- gibbous-jit 帧 CALL 另一个 gibbous-jit Proto ⇒ 同世界内直跳(若启用 P4 内 inline 跨调用优化,留 [./06-backends](./06-backends.md))或经 trampoline 出到 helper 再 dispatch。

**P3 与 P4 在「跨层调用走统一 CallInfo」这条上同形式**(承 [../p3-wasm-tier/04-trampoline](../p3-wasm-tier/04-trampoline.md) §0.4 协议规范);P4 唯一差异是「同世界内直跳」可优化(P3 PW10 R3 已完成的 call_indirect 一样的优化的 P4 对位,留 [./06-backends](./06-backends.md))。

---

## 4. trampoline 三出口协议

JIT 函数从 JIT 世界出 Go 世界有三种出口,共用同一段 jitExit asm stub(§2.4.3)。本节定义三出口的协议骨架,具体 OSR exit 物化语义在 [./04-osr-deopt](./04-osr-deopt.md) §3.3。

### 4.1 出口 A:正常返回(RETURN 指令模板的尾部)

**触发**:RETURN 指令模板执行到尾部——按 [../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md) §7.2 的语义:关闭本帧 upvalue、搬移返回值到调用者期望位置、弹 CallInfo,然后:

- 若调用者是另一个 gibbous-jit 帧(同世界):直接跳到调用者的对应 pc(JIT 内续跑);
- 若调用者是 crescent 帧(在 trampoline 进入栈深 ≥ 2 时不会出现——caller 必在 Go 世界):等价于 jitExit;
- 若退到了 entryCi(JIT 进入帧之下 = `ciTop == enterCi`):走 jitExit(status=STATUS_OK)。

**协议**:
```
出口 A 协议:
  exitReason = STATUS_OK = 0
  exitArg0 = nResults (当前帧返回值数量,Go 侧已知由 caller CallInfo 决定,这里只为对齐三出口字段)
  jmp jitExit_stub
```

Go 侧 trampoline 出口处理(`jitExit` 后):返回到 P2 状态机的调用方(通常是 `Bridge.Run`),由调用方按 caller CallInfo 续跑或最终返回 host。

### 4.2 出口 B:OSR exit(承 [./04-osr-deopt](./04-osr-deopt.md) §6.5)

**触发**:guard 失败(类型投机错,[./03-speculation-ic.md](./03-speculation-ic.md) §2 投机模板的 guard 检查未通过)。

#### 4.2.1 status=2 DEOPT 编码(承 [../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md) §6.1)

P2 与 P4 共定的 status 码:`STATUS_DEOPT = 2` 表示「JIT 自愿退到解释器」。这个码 P3 永不返回(P3 不投机,无 deopt 概念,承 [../p3-wasm-tier/04-trampoline](../p3-wasm-tier/04-trampoline.md) §0.4),是 P4 独有的出口。

#### 4.2.2 写 exitPC + 经 trampoline 出去 + crescent 接管

**协议**:
```
出口 B 协议(OSR exit):
  exitReason = STATUS_DEOPT = 2
  exitPC = <bytecode pc>  (本 guard 失败对应字节码指令的 pc,
                           编译期登记的「机器地址 → bytecode pc」映射查表)
  exitArg0 = <reserved for snapshot>  (P4 不需要 snapshot,留 0;
                                        P5 trace JIT 用此字段记 IR 值集合)
  jmp jitExit_stub
```

物化语义详见 [./04-osr-deopt](./04-osr-deopt.md) §3.3:
- 因 P4 「栈槽真相」不变式([./04-osr-deopt.md](./04-osr-deopt.md) §3.1),guard 布在模板**开头**,失败时栈槽即完整解释器状态,**待物化集合为空**——出口 B 只需写 exitPC,无需写任何寄存器到栈槽;
- 若启用了循环变量寄存器驻留(局部缓存),则该 guard 的 exit 需补一段「寄存器→栈槽」写回序列,编译期静态生成,留 [./04-osr-deopt](./04-osr-deopt.md) §3.3 注。

Go 侧 trampoline 出口处理:
- 读 jitContext.exitPC ⇒ 写入当前 CallInfo.savedPC([../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md) §1.2);
- 记 deopt 计数([./04-osr-deopt.md](./04-osr-deopt.md) §5 再训练用);
- 把当前帧从 gibbous-jit 「降回」 crescent:写 CallInfo bit50 = 0(本帧不再是 gibbous,P4 退出后该帧由解释器接管);
- 调用 crescent 的 `executeFrom(exitPC)` 续跑(承 [../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md) §1.3 reloadFrame)。

### 4.3 出口 C:慢路径 helper 调用

**触发**:JIT 生成码内嵌的快路径需要回 Go 侧执行慢路径——元方法分派 / arena 扩容 / 抛错 / host call / collect / 全局表 rehash 等。

**2026-07-02 实现勘误——helper 协议早已从「helperTab 三段式跳板」演进为两条通道并存**:早前草稿把 helper 调用画成「JIT 写 exitReason=3 + helperID → jitExit stub → Go 侧 dispatcher 读 helperTab[helperID] indirect call → helper 跑完 → jitEnter 续跑」的三段式,同时 jitContext 里挂一个函数指针表 `helperTab`。这套「helperTab + jitExit_stub」在实现里从未完成。真正在跑的是两条通道:

- **(a) exit-reason 协议(主通道,PJ10 native emit 起做为默认路径)**:承 §4.3.1a 详解。适用 op:`GETTABLE` / `SETTABLE` / `NEWTABLE` / `SETLIST` / `CALL` / `UNM` / `GETUPVAL` / `SETUPVAL` / `GETGLOBAL` / `SETGLOBAL`,以及多返值 Proto 的 `RETURN`。此路径不走 SP 切换、不走 shim 调用,mmap 段直接 `ret` 到 Go 世界,由 `nativeCode.Run` 里的 dispatcher 循环处理并重入段。这就是 §4.3 的实际主协议。
- **(b) shim 调用路径(次通道,历史遗留)**:承 §4.3.1b 详解。适用 op:`LEN` / `CONCAT` / `SELF` / `TAILCALL` / `CLOSURE` / `CLOSE` / `TFORLOOP` / `MOD` / `POW` 的算术慢路径,以及 `EQ` / `LT` / `LE` 比较的 shim 尾巴。此路径直接从 mmap 段 emit 一段 ABIInternal `call` 序列跳进 Go shim,shim 完成后返回 mmap 段续跑。在嵌套 + 并发压力下已知易碎(issue #38),故新 op 一律走通道 (a);现存 shim op 待后续渐进迁移。

两条通道共享同一份 `P4HostState` 接口(`internal/gibbous/jit/host.go`,~30 个方法)——接口方法就是 P3 时段的 imported helper 集,只是 P4 build 下用 Go 方法调用而非 wazero import。旧稿的「helperTab 索引 + indirect call」概念被「`P4HostState` 接口 + 编译期直接方法引用」替代;通道 (a) 里,mmap 段甚至不需要引用 Go 函数指针,只写打包好的 helper code + args 就够,dispatcher 端才做 `switch` 分派到具体方法。

#### 4.3.1a exit-reason 协议(主通道)

物理流程:

```
JIT 生成码(mmap 段,一次某个 op 的 exit-reason 尾部):
    ;; 打包 exit-reason 参数进 exitArg0:
    ;;   bits  0..15 = helper code (jit.HelperGetTable / SetTable / NewTable / ...)
    ;;   bits 16..23 = op arg A (0..255)
    ;;   bits 24..32 = op arg B (0..511,含 RK 常量位)
    ;;   bits 33..41 = op arg C (0..511)
    ;;   bits 42..63 = bytecode pc(0..~4M)
    mov qword ptr [r15 + exitArg0Off], <packed>
    ;; resumeOff = 下一条 op 在 mmap 段内的字节偏移(编译期已知)
    mov dword ptr [r15 + resumeOffOff], <next_op_off>
    ;; exitReasonCode = ExitInlineHelper(3)
    mov dword ptr [r15 + exitReasonCodeOff], 3
    ret                                    ;; 直接 ret 出段(不走独立 exit stub、不调 Go 函数);
                                           ;; SP 由 trampoline 切回 goroutine 栈(issue #89)
    │
    ▼
Go 侧 nativeCode.Run 的 dispatcher 循环(translator_native.go):
    for status == ExitInlineHelper {
        //(HelperReturn 分支:读 exitArg0 里的 (a, b, pc),
        //  调 host.DoReturn 后直接 return,不重入段)
        if arg0 & HelperCodeMask == HelperReturn { ... return }
        resumeOff := jitCtx.ResumeOff()      // ★ 先快照:递归 Run 会覆盖同 jitCtx
        ok := dispatchHelper(base)           // switch helperCode → host.<Method>(…)
        if !ok { return 1 }                  // host 方法 raise → 段整体 ERR
        host.RefreshJitCtxAddrs(jitCtx, base) // ★ arena 可能 grow,必刷五个 addr 字段
        saveGoG(jitCtx.SavedGoGSlot())        // shim 通道用到,一并刷
        jitCtx.SetHostRef(hostIfaceHeader(host))
        vsBase := jitCtx.ValueStackBase()
        resumeAddr := codePage.Addr() + uintptr(resumeOff)
        status = CallJITSpec(resumeAddr, jitCtxAddr, vsBase)  // ★ 重入段
    }
```

关键点:

- **exit-reason 出口是普通 `ret`,不发独立 exit stub**:exit-reason 通道的出段不需要 §2.4.3 描绘的「反向恢复 stub」——mmap 段把 exitReasonCode / exitArg0 / resumeOff 写好后直接 `ret` 回 `CallJITSpec` 的 caller,由 trampoline 统一在段返回后把 SP 从自管 spill 栈切回 goroutine 栈(issue #89 已接线,见 §7.5)。这一简化只针对 exit-reason 通信本身;SP 切换已由 trampoline 承担,不再牺牲「自管机器栈」不变式。
- **`resumeOff` 快照必须先做**:如果 helper 是 `HelperCall`,dispatcher 会同步跑 callee;若 callee 递归回到同一个 `nativeCode`,会共用同一份 per-Proto `jitCtx`,把 `resumeOff` / `exitArg0` / 各 addr 字段全覆盖掉——外层 dispatcher 事先快照 `resumeOff` 才能正确重入外层的续跑点。
- **`RefreshJitCtxAddrs` 必调**:任一 host 方法都可能触发 arena grow(承 §5)。重入段前必须把 `arenaBase` / `valueStackBase` / `ciDepthAddr` / `ciSegBaseAddr` / `topAddr` 五个字段刷一次——这就是 §3.5 勘误里点名的两处调用点之一。
- **`HelperReturn` 是终止性 helper**:多返值 Proto 把每处 `RETURN` 都 lower 成一次 `ExitInlineHelper + HelperReturn`,dispatcher 直接调 `host.DoReturn(base, pc, a, b)` 然后 return,不重入段。单返值 Proto 保留了老的快速路径——mmap 段 `xor eax, eax; ret` 直接返 `ExitNormal`,Go 侧再做一次 `host.DoReturn(retPC, retA, retB)`(编译期就已烧进 `nativeCode.retA/retB/retPC`),省去打包 `exitArg0`。

#### 4.3.1b shim 调用路径(次通道)

这条路径 PJ0-PJ9 阶段的所有 op 都走过,PJ10 native emit 只对下述 op 保留:`LEN` / `CONCAT` / `SELF` / `TAILCALL` / `CLOSURE` / `CLOSE` / `TFORLOOP`,以及 `MOD` / `POW` 算术慢路径尾巴和 `EQ` / `LT` / `LE` 比较 shim 尾巴。物理流程:

```
;; mmap 段内(shim 调用序列,一处约 12-30 字节,视 arg 数而定):
mov r14, [r15 + savedGoGOff]           ;; ★ 恢复 R14 = Go G(承 §7.5 + §3.3 第 6 组)
mov rax, r15                            ;; ABIInternal arg0 = jitCtx
mov rbx, <baseImm32>                   ;; arg1
mov rcx, <pcImm32>                     ;; arg2
;; ...其余按 abiIntArgRegs 顺序装填
mov rax, <shimAddr>                    ;; shim 函数绝对地址(编译期烧入)
call rax                                ;; 直接 call 进 Go 函数(不出段、不切 SP)
mov rbx, [r15 + valueStackBaseOff]     ;; ★ 恢复 RBX = vsBase(ABIInternal 用 RBX 传 arg1,不保 caller)
```

风险与守则(承本文 §8.1.4 与 issue #38):shim 序列直接从 mmap 段 `call` 进 Go 函数,依赖 Go 1.17+ ABIInternal 的寄存器传参约定。这套在**嵌套调用 + 多 State 并发**下压力测试时会摧毁 Go 栈展开(表现为在 `defer recover` 之外的 SIGSEGV),这是 PJ10 决定把新 op 全部走 exit-reason 通道的直接动机。现存 shim op 待迁移完成后本节可删。

#### 4.3.2 helper 表:metamethod 分派 / arena 扩容 / 抛错 / host call

helper 表(jitContext.helperTab)的典型条目(承 [../p3-wasm-tier/04-trampoline](../p3-wasm-tier/04-trampoline.md) §3.3 helper 列举,P4 一样的集):

| helper | 触发 | Go 侧逻辑 | 对应 P3 imported helper |
|---|---|---|---|
| `h_arith` | 算术快路径 guard 失败(混合类型 / 元方法) | [../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md) §6.4 metamethod | $h_arith |
| `h_gettable` / `h_settable` | 表 IC miss(形状变化、键非 number/string) | [../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md) §6.2/§6.3 | $h_gettable / $h_settable |
| `h_alloc_slow` | bump 分配越界 → grow + alloc | [../p1-interpreter/06-memory-gc](../p1-interpreter/06-memory-gc.md) §3 | $h_newtable / 等(每对象类型一个) |
| `h_safepoint` | 回边检查命中(gcPending / preemptFlag) | [../p1-interpreter/06-memory-gc](../p1-interpreter/06-memory-gc.md) §8.2 collect / Gosched | $h_safepoint |
| `h_throw` | 抛错(运行期错误如 nil 算术 / 索引非表) | [../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md) §9 错误冒泡 | (P3 经 status=1) |
| `h_call_unknown` | CALL 目标未升层(crescent / host) | [../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md) §7 doCall | $h_call(P3 三向分派的对位) |
| `h_close_upvals` | CLOSE / RETURN 时 close upvalues | [../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md) §8.3 closeUpvals | $h_close |
| ...其它 | (per-opcode 慢路径) | (P1 一样的) | (per-opcode 一样的) |

helper 集与 P3 一样的,正反映 [./02-template-direction.md](./02-template-direction.md) §4 P4 边界:**只换执行引擎,不重新发明慢路径逻辑**。Go 侧的 helper 函数与 P3 imported helpers 是同一份代码(同样调 P1 的 `Arena.Alloc` / `metamethodCall` / 等),区别仅在「物理通道」:

| | P3 通道 | P4 通道 |
|---|---|---|
| 跨层调用机制 | wazero imported function call | trampoline jitExit + Go dispatch + jitEnter |
| 参数传递 | wazero linear memory + i32/i64 args | jitContext.exitArg0/1 + helper 表 indirect call |
| 返回 | wazero call return | jitContext.exitArg0 写回 + jitEnter 续跑 |

**逻辑层完全共享,物理层各换一对桩**——这是 P3/P4 同 tier 的工程红利,helper 表的逻辑成熟度直接复用 P3 的实战验证。

#### 4.3.3 helper 表 → `P4HostState` 接口(2026-07-02 实现勘误)

旧稿说 helper 表本身是 Go 堆分配的 `[NumHelpers]unsafe.Pointer` 数组、放 `jitContext.helperTab` 字段、JIT 段经该字段 indirect call。这份「函数指针表」在实现里从未完成——它被 `P4HostState` 接口(`internal/gibbous/jit/host.go`)替代:

- **exit-reason 通道**(§4.3.1a):mmap 段完全不引用任何 Go 函数指针,只写打包好的 `helper code + args`;dispatcher 端 `switch helperCode` 后直接 `c.host.<Method>(...)` 走 Go 接口方法调用。因为 helper 集是编译期就完全枚举的常量集合(见第 3 组常量 `HelperGetTable=10..HelperCall=24`),`switch` 走静态分派开销可忽略。
- **shim 通道**(§4.3.1b):mmap 段的确直接 `call <shimAddr>` 跳进 Go 函数;但 shim 函数是编译期就烧进的立即数(`internal/gibbous/jit/peroptranslator/shims.go` 里几个 ABI0 包装),不是从 jitContext 表里查——jitContext 里连这个表都没有。

`P4HostState` 里的方法名与 P3 `HostState`(`internal/gibbous/wasm/helpers.go`)几乎逐个对齐(`GetTable` / `SetTable` / `NewTable` / `Arith` / `DoReturn` / `Safepoint` / `CallBaseline` / `TailCall` / `Self` / …),这是 P3/P4 helper 集一样的的实现体现——interface 方法替代了 helperTab 索引,概念上仍是「helper 集是通用语义层,通道是物理层」,只是「通道」现在是「接口方法调用 + exit-reason 通信」而非「imported function / 函数指针表」。

另外,`P4HostState.GlobalsRaw() uint64` 在 PJ10 native emit 的 `GETGLOBAL` / `SETGLOBAL` `NodeHit` 快路径里被用来在编译期把 globals 表的 NaN-boxed u64 直接烧进指令流——同一个 State 生命周期内 globals 表身份不变、arena 对象不移动,承 P3 wasm 编译器一样的保守铺垫。

#### 4.3.4 P3 / P4 helper 集对位(2026-07-02 实现勘误)

承 §4.3.2 表的对位列。**P3 wazero imported helper 的所有 Go 侧实现在 P4 build 下几乎原样复用**——helper 的核心语义(分配 / 分派 / 错误 / 抢占协作)不变。差异在**物理通道**:

| | P3 通道 | P4 通道(exit-reason) | P4 通道(shim) |
|---|---|---|---|
| 跨层调用机制 | wazero imported function call | mmap `ret` + Go dispatcher 循环 + `CallJITSpec` 重入 | mmap 段 ABIInternal `call` 进 Go 函数 + `ret` 回段 |
| 参数传递 | wazero linear memory + i32/i64 args | `jitContext.exitArg0` 打包 (code, A, B, C, pc) + `resumeOff` | ABIInternal 寄存器(RAX=jitCtx, RBX/RCX/RDI/... = args) |
| 返回 | wazero call return | `jitContext.exitArg0` 语义按 helper 而定 + dispatcher 重入 | Go 函数正常 return + `mov rbx, [r15+valueStackBaseOff]` 恢复 |
| G / vsBase 恢复 | wazero 内部处理 | `RefreshJitCtxAddrs` + `SetHostRef` 在重入前 | `mov r14, [r15+savedGoGOff]`(shim 前) + `mov rbx, [r15+vsBaseOff]`(shim 后) |

**语义等价 / 物理各异**——这是 P3/P4 同 tier 的工程红利。exit-reason 通道是当前主推(所有 PJ10 新 op 都走这一条),shim 通道是历史遗留 + 待迁移。

### 4.4 三出口共用同一段恢复代码(节省码段 + 一致性)

三出口共用 jitExit asm stub 的物理体现:

```asm
;; 概念伪码,具体 asm 在 06-backends
jitExit:
   ;; 此时 jitContext.exitReason 已由 caller(JIT 生成码)写好
   ;; 此时 jitContext.exitArg0/1 已由 caller 写好
   ;; SP 仍在自管栈上,Go SP 在 jitContext.savedGoSP

   ;; ① 反向恢复 SP
   mov SP, [r15 + offset_savedGoSP]

   ;; ② 反向恢复 Go callee-saved 寄存器(进入时压的)
   pop_callee_saved

   ;; ③ 把 jitContext 指针装入 Go 函数返回值寄存器(给 dispatcher 用)
   mov RET_REG, r15

   ;; ④ 返回到 trampoline 进入 stub 的 caller(Go 世界)
   ret
```

caller(Go 世界的 dispatcher)收到 jitContext 后,据 exitReason 分派:

```go
// internal/gibbous/jit/trampoline.go(P4 build)
func dispatchAfterExit(ctx *jitContext) (next action, err error) {
    switch ctx.exitReason {
    case STATUS_OK:           // 出口 A
        return actionReturn, nil
    case STATUS_ERR:          // 错误冒泡(承 P3 status=1)
        return actionRaiseErr, errFromExit(ctx)
    case STATUS_DEOPT:        // 出口 B(P4 独有)
        return actionDeoptToCrescent, nil  // crescent.executeFrom(ctx.exitPC)
    case STATUS_HELPER_CALL:  // 出口 C
        helperFn := ctx.helperTab[ctx.exitHelperID]
        // 调 helper(Go 函数指针),helper 完成后回到 jitEnter 续跑
        return actionCallHelperAndContinue, nil
    default:
        panic("unknown exit reason")
    }
}
```

**三出口共用同一段恢复代码的好处**:
- (a) 码段节省:每个 Proto 不需要为不同出口生成多份 epilogue;
- (b) 一致性:三出口经同一通道走完恢复(SP/寄存器/jitContext 写出),Go 侧 dispatcher 拿到的 jitContext 状态语义同一,只看 exitReason 字段分派;
- (c) 调试友好:single point of exit 让 backtrace / pprof 看到的退出路径恒一致。

---

## 5. arena base 重载协议(承本文 §3.6 不变式 2 + 重要细节)

### 5.1 arena 扩容触发条件(分配慢路径越界)

arena 扩容(grow)在 P4 build 下与 P1 一致([../p1-interpreter/06-memory-gc](../p1-interpreter/06-memory-gc.md) §3):
- 任一 Alloc 调用发现 `bump + size > cap` ⇒ 触发 grow;
- grow 经 `make([]uint64, newCap/8)` + `copy(new, old)` 实现 realloc;
- 新 backing 在 Go 堆**新地址**,arena.bytes 起始指针变化;
- 所有 GCRef(48-bit 字节偏移)语义不变,无需修补。

P4 阶段 grow 触发点:
- (a) JIT 内联的 bump 分配快路径越界(若启用,留 [./06-backends](./06-backends.md));
- (b) helper 内的 Alloc 调用越界(常见,与 P3/P1 一样的触发)。

### 5.2 扩容期间 = JIT 必出去:JIT 内不直接持 backing slice

**关键纪律**:JIT 生成码**不直接持 backing 起始指针**——只持 jitContext 的指针,经 jitContext.arenaBase 间接寻址。原因:

- arena.grow 后 arenaBase 变(新 Go 堆地址),若 JIT 缓存了旧 arenaBase 到机器寄存器并跨 helper 调用复用,则 helper 出去 grow 完回来后旧寄存器值是 stale,UAF;
- 解法是「**每次 helper 调用后从 jitContext 重 load arenaBase 到寄存器**」——这是不变式 2(§3.6.2)的工程兑现。

JIT 编译器(具体留 [./06-backends](./06-backends.md))实施:
- 每个基本块入口 load arenaBase 到固定寄存器(per-arch);
- 任何 helper 调用后(或调到可能 grow 的 helper 后)在该 helper 调用点的「续跑入口」重新 load arenaBase;
- 这是 [../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md) §1.3「每个 safepoint 后 reloadFrame stk」的机器层同构。

### 5.3 helper 出去 + 扩容 + 回来 → 从 jitContext 重载 base

完整 grow 触发流程(以 helper 内 grow 为例):

```
JIT 生成码:
   ;; ... 直线代码
   ;; 经 §4.3.1 调 h_alloc_slow
   mov dword [r15 + offset_exitHelperID], h_alloc_slow_id
   mov dword [r15 + offset_exitReason], 3
   jmp jitExit_stub
   │
   ▼  trampoline 出
Go: dispatcher 调 h_alloc_slow
   ▼
   h_alloc_slow 体:
     ref := arena.Alloc(size)          ;; 内部触发 grow
     ;; ★ grow 后 arena.bytes 起点变!
     ;; 把新 arenaBase 写回 jitContext
     ctx.arenaBase = unsafe.Pointer(&arena.bytes[0])
     ctx.exitArg0 = uint64(ref)         ;; 返回值经 exitArg0
     ;; ★ 同时若 thread 值栈段也搬家(因为它住 arena),
     ;;   helper 自动经同一通道更新(arena 视图重取后 valueStackBase 不变,
     ;;   因为它是 arena 内字节偏移,grow 不动偏移)
   ▼
   dispatcher 调 jitEnter_stub 续跑
   │
   ▼  trampoline 进
JIT 生成码续跑入口:
   ;; ★ 重 load arenaBase 到机器寄存器
   mov rax, [r15 + offset_arenaBase]
   ;; 读 helper 返回值
   mov rbx, [r15 + offset_exitArg0]
   ;; 后续访问 arena 内对象用「arenaBase + offset」寻址
   mov rcx, [rax + valueStackOffset + 8*A]   ;; R(A)
```

要点:
- helper 内若 grow,helper 责任写新 arenaBase 到 jitContext;
- JIT 续跑入口必须重 load arenaBase——这条是发射器的硬纪律,留 [./06-backends](./06-backends.md) 详细生成模板;
- valueStackBase / callInfoBase 是**字节偏移**,grow 不动(承 §1.3.5 GCRef 偏移寻址语义同一),无需 helper 重写。

### 5.4 与 [../p3-wasm-tier/03-memory-model](../p3-wasm-tier/03-memory-model.md) §1.7 grow 后视图重取的对位

P3 wazero memory grow 后 Go 侧需重取 backing slice 视图([../p3-wasm-tier/03-memory-model](../p3-wasm-tier/03-memory-model.md) §1.7);P4 一样的机制——只是「视图」是机器寄存器中的 arenaBase 值。两层在「grow 后须重取基址」这条物理上同源。

具体对位:
- **P3**:wazero `memory.grow` 后 Go 侧重取 `mem.UnsafeUnderlyingBuffer()` ⇒ 新 `[]byte` 起点 ⇒ 重取 `[]uint64` 别名视图;
- **P4**:JIT 内 helper 调用越界 ⇒ Go 侧 grow ⇒ 写新 `arenaBase` 到 jitContext ⇒ JIT 续跑重 load。

### 5.5 与 P3 PW6 base 刷新解 growStack 段重定位 UAF 的对位(承 P3 implementation-progress)

P3 PW6 完成了「`h_call` 返回 i64 同载新 base 刷新 + 错误负哨兵」机制,根因是嵌套调用的 growStack 重定位值栈段在 arena 内的位置([../p3-wasm-tier/04-trampoline](../p3-wasm-tier/04-trampoline.md) base 刷新协议)。

**P4 在这条上比 P3 简单**——因为:
- (a) P3 wazero 生成的 Wasm 代码中 `$base`(linear memory 字节偏移)是 wazero local,helper 调用中途无法自刷新,需要 helper 返回新 base 到 wazero local;
- (b) P4 jitContext 在 Go 堆地址恒定,helper 直接写 `ctx.valueStackBase`(若 thread 切换或值栈搬家)/ `ctx.arenaBase`(若 arena grow),JIT 续跑时重 load 即可;**无需经 helper 返回值通道传 base**。

这是 P4 物理层比 P3 wazero 层「更直接」的一处——helper 与 JIT 经 jitContext 字段共享状态,任何字段都可由 helper 单向更新,JIT 续跑时一律重 load,无需特殊返回值约定。

---

## 6. 两个 safepoint 的协议(承 [../p3-wasm-tier/05-safepoint-gc](../p3-wasm-tier/05-safepoint-gc.md) + P4 自付)

### 6.1 三类 safepoint(承 P3 05):分配点 + 层边界 + 回边

[../p3-wasm-tier/05-safepoint-gc](../p3-wasm-tier/05-safepoint-gc.md) §1 已定的三类 safepoint 模型在 P4 一字不改,只是物理形式从「Wasm 直线代码 + imported helper」切换为「原生码 + trampoline + helper」。

| 类别 | P3 物理形式 | P4 物理形式 |
|---|---|---|
| **分配点** | gibbous 直线代码自身从不分配,经 imported `$h_alloc_*` 助手 | gibbous-jit 自身从不分配(快路径 bump 越界即出),经 trampoline + `h_alloc_slow` helper |
| **层边界** | crescent ↔ gibbous 经 wazero `$h_call` imported helper | crescent ↔ gibbous-jit 经 trampoline + `h_call_unknown` helper |
| **回边** | `(if (i32.load $gcPending) (call $h_safepoint))` Wasm 序列 | 几条机器指令的 load preemptFlag/gcPending + test + 条件跳到 exit stub |

### 6.2 P4 自付:每类 safepoint 在 P4 的物理形式

#### 6.2.1 分配点

**JIT 生成码自身从不分配**(承 [../p3-wasm-tier/05-safepoint-gc](../p3-wasm-tier/05-safepoint-gc.md) §1.1)——所有 NEWTABLE / SETLIST / CLOSURE / CONCAT / 字符串 intern / 表 rehash 触发的分配,都经 trampoline 出到对应 helper。helper 内 `Arena.Alloc` 触发,Alloc 内若越 GC 阈值则 collect,完成后 helper 返回(经 §4.3.1 续跑通道),JIT 重 load arenaBase 续跑。

**P4 与 P3 的逻辑等价**:同一个 Go 侧 helper 函数体(同一份 `Arena.Alloc` + `gc.Collect` 调用),只换跨层物理通道。

#### 6.2.2 层边界

trampoline 进出本身就是层边界 safepoint——出 JIT = 进 Go 世界 = 一切 Go 侧分配/collect/抛错都可发生,与 [../p3-wasm-tier/05-safepoint-gc](../p3-wasm-tier/05-safepoint-gc.md) §1.2「trampoline 天然检查点」同位。

P4 trampoline 经 jitContext 的 exit/enter 通道实现这一边界:
- **进**:Go 调用方栈一致(trampoline 进入 stub 是普通 Go 函数,有正常 stack map);
- **出**:经 jitExit stub 后,SP 恢复到 Go 世界,Go 侧自由 collect/Gosched/抛错。

#### 6.2.3 回边

P4 在每个循环回边发射一组「检查 + 跳」序列:

```
;; 概念伪码,具体 asm 在 06-backends
;; 回边检查(FORLOOP / TFORLOOP / 向后 JMP 末尾发射)
loop_back_edge:
   ;; ① 一次 32-bit load 同时读 gcPending + preemptFlag(若两字段相邻)
   ;;    或两次 load(per-arch 优化)
   mov  eax, [r15 + offset_gcPending]
   or   eax, [r15 + offset_preemptFlag]
   ;; ② test + 跳
   test eax, eax
   jnz  exit_for_safepoint     ;; 任一置位则跳到 exit stub
   ;; ③ 直线继续到循环体头
   jmp  loop_body_start

exit_for_safepoint:
   ;; 走 §4.3.1 出口 C 协议(helper id = h_safepoint)
   mov  dword [r15 + offset_exitHelperID], h_safepoint_id
   mov  dword [r15 + offset_exitReason], 3
   jmp  jitExit_stub
```

`h_safepoint` helper 在 Go 侧:
- 若 `gcPending`:跑一次 STW collect([../p1-interpreter/06-memory-gc](../p1-interpreter/06-memory-gc.md) §8.2);
- 若 `preemptFlag`:经 ctx 取消钩子检查 ctx.Done(若 done 则置 STATUS_ERR + 抛 cancel error),否则等价 Gosched(让出当前 P);
- 完成后 helper 返回,trampoline 进回 JIT 续跑(§4.3.1)。

### 6.3 回边检查点的密度与吃性能权衡(本文 §8 风险节)

回边检查点的成本:每次循环回跳付几条指令(2 次 load + 1 次 test + 1 次条件跳)。在分支预测器友好(check 几乎恒不跳)情况下,这是几条 cycle 的开销;紧循环里(几十 ns/iter)占比仍可观。

权衡:
- **密度过高**(每 op 一次回边检查):成本主导循环开销;
- **密度过低**(每 N 次回边一检查):抢占延迟 / GC 延迟变长,与 Go runtime 协作式抢占的隐含期望不一致(预期数十微秒级响应)。

**默认策略**:每个回边一次检查(per-iter 一次),与 [../p3-wasm-tier/05-safepoint-gc](../p3-wasm-tier/05-safepoint-gc.md) §1.3 P3 一样的。若实测吃性能(amd64 实测后定),可优化:
- (a) 多重循环嵌套时,内层循环周期性触发外层回边时机检查(减少内层频率);
- (b) 短直线循环可放宽到 N 次一查,但 N 必须有上界(确保抢占延迟有界)。

具体调优留 [./06-backends](./06-backends.md) per-arch tuning。

### 6.4 与 Go GC STW 的协调:JIT 长循环不阻塞 STW(回边经 helper 让出)

Go 的 STW(即便是 sub-ms 级 short STW)需要所有 goroutine 在 safepoint 停止。**JIT 长循环若没有回边检查就是「无 safepoint 直线段」**,可能阻塞 STW 数十毫秒甚至更长(取决于循环 N)。

回边检查机制的本质就是在长循环里**周期性给 Go runtime 一个让出窗口**:任何 STW 请求经 `preemptFlag` 置位 ⇒ 下一次回边命中 ⇒ JIT 经 trampoline 出去 ⇒ Go 侧 helper 立即响应 STW 请求(在 helper 内 Go runtime 自由介入)。

**P4 不直接处理 Go runtime 的抢占信号**——它经 `preemptFlag` 的协作机制让出。这条与 [../p3-wasm-tier/implementation-progress.md](../p3-wasm-tier/implementation-progress.md) §0.1 PW0 spike 修正的事实(wazero 一样的不可被异步抢占,靠 ctx 协作)同源:**P4 在这条上不弱于 wazero,只是把一样的解法自付了一次**。

---

## 7. 不变式清单(本文承担)

本文承担 5 条不变式,聚合 §3.6 + §1.1.3 + §2.2:

### 7.1 「JIT 码不调普通 Go 函数」(聚合 §3.6.1)

> JIT 生成码的「调用」指令只允许调另一段 JIT 代码、调 helper 表中的 Go 函数、退到 trampoline 出口三种;**绝对禁止 JIT 直接 call 任何普通 Go 函数地址**。

物理后果:四项税防线由 trampoline 边界保住,JIT 内不需要为每税单独发明对策。违反此不变式的提案直接判否——它会让 GC 精确栈扫描税(§1.1)与栈移动税(§1.3)同时失效。

### 7.2 「arena base safepoint 间稳定」(聚合 §3.6.2 + §5)

> arena 整体重定位(grow)只在分配慢路径(出 JIT 世界)发生;两个 safepoint 之间(无 helper 出)arenaBase 恒定,JIT 可缓存到机器寄存器。每次 helper 调用后必须重 load arenaBase。

物理后果:JIT 生成码可以在直线段内复用 arenaBase 寄存器,无需每条访存指令都重 load——这是 P4 收益的一部分(避免 helper 边界不必要的 load 风暴)。违反此不变式(JIT 持 stale base)= UAF。

### 7.3 「混层走统一 CallInfo 协议」(聚合 §3.6.3)

> P4 帧与 crescent 帧、P3 wazero 帧共享同一 CallInfo 结构(bit50 `callStatus_gibbous` 在 P4 同样标 1);跨层调用走 [../p3-wasm-tier/04-trampoline](../p3-wasm-tier/04-trampoline.md) 一样的协议,只换物理通道。

物理后果:错误 traceback / pcall 边界清理 / 协程切换在 P4 与 P1/P3 完全对位,差分测试([./04-osr-deopt](./04-osr-deopt.md) + [../p1-interpreter/12-testing-difftest.md](../p1-interpreter/12-testing-difftest.md))逐字节一致。

### 7.4 「W^X 任何时刻不持 RWX」(承 §2.2)

> 任何时刻 P4 不持有同时 PROT_WRITE + PROT_EXEC 的代码页;翻面经 mprotect 单点完成,翻面前不可执行,翻面后不可写。

物理后果:macOS arm64 / iOS / Android / 严格 Linux 等强制 W^X 平台可部署。违反此不变式 = 部分平台直接 deploy 不出去。

### 7.5 「自管机器栈不持 Go 栈指针」(承 §1.3.2,聚合 §3.4)

> 自管机器栈上存放的所有字均为「机器寄存器溢出 / 返址 / 对齐填充」,**绝不持有指向 goroutine 栈的指针**。所有外部入口(arena base / helper 表 / 标志位)经 jitContext 间接寻址。

物理后果:morestack 栈移动对 P4 物理透明——goroutine 栈如何重定位,JIT 世界都不受影响。违反此不变式(JIT 把 Go 栈地址写到自管栈或机器寄存器跨 helper 复用)= 栈移动税复发 = UAF。

**issue #89 接线状态(2026-07-08)**:此不变式此前只是设计原则,自管 spill 栈曾长期未接线——mmap 段直接跑在 goroutine 栈上,深度 seg2seg 递归的每层 `sub sp` 吃 goroutine 栈的 NOSPLIT 余量,`segToSegDepthCap` 因此被 PR #86 收紧到保守值 16。issue #89 把 SP 切到 per-jitCtx 的 64 KiB 自管 spill 栈(Go 堆 `[]byte`,`spillBase` = 对齐后的高地址端 / `spillTop` = 低地址端):trampoline 进段前把 goroutine SP 暂存到 `savedGoSP` 再切到 `spillBase`,出段后(恢复 callee-saved 之前)切回。深度递归的 `sub sp` 从此消耗自管栈而非 goroutine 栈的 ~800 B NOSPLIT 余量,`segToSegDepthCap` 抬回 128。两条汇编硬约束:① 切 SP 前先把 codeAddr 读进寄存器(`+N(FP)` 是 SP 相对寻址,切 SP 后会指向自管栈垃圾);② 出段恢复时不能覆写 RAX/R0(它带段的 exit-reason status)。空 jitCtx 要保护(底层模板单元测试传 jitCtx=0 时 `spillBase==0` 则跳过切换)。实现见 `internal/gibbous/jit/jitcontext.go`(`AllocSpillStack` / `JITContextSpillBaseOffset` / `JITContextSavedGoSPOffset` / `TestSpillStackLayout`)+ `internal/gibbous/jit/amd64/trampoline_spec_amd64.s` + `internal/gibbous/jit/arm64/trampoline_arm64.s`。amd64 端 `TestI86_DeepRecursionGCStress`(cap=128 GOGC=1)3/3 不崩、全 p4 单元 + difftest + conformance 绿、FuzzAutoPromote 90s 干净;arm64 为镜像实现 + 交叉编译通过,执行正确性交 CI arm64 矩阵。

---

## 8. 风险与开放问题

### 8.1 风险

#### 8.1.1 icache 压力(模板展开膨胀)

P4 模板展开比 Wasm 字节码膨胀一个量级(每条 Lua 字节码 → 几条到几十条机器指令);热函数过大时 icache 压力反噬。缓解:
- P2 F5 大函数检查已限编译单元尺寸;
- per-Proto 段池化(§2.1.2)使热函数与冷函数不混占 icache 行;
- 进一步的 icache 友好优化(模板复用、热代码紧凑布局)留 [./06-backends](./06-backends.md) tuning。

#### 8.1.2 Go 调度交互(回边检查点密度)

回边检查点的密度调优是 Go runtime 版本敏感的——Go 1.x 系列的抢占机制随版本演进(1.14 引入异步抢占、1.21 调度器优化等),P4 行为可能随 Go 版本漂移。wazero 的跟进历史(一样的问题)是预警源:每次 Go 大版本升级前,P4 须跑 GC 压力 fuzz + 长循环抢占测试,确认行为未漂移。

#### 8.1.3 多 State 并发下的 Dispose 安全

§2.1.3 已点名:多 State 并发下若某 State 触发某 Proto Dispose(munmap),而另一 State 仍在该段执行,则 UAF。缓解候选(留 [./06-backends](./06-backends.md) tuning):
- (a) 引用计数 + 延迟 munmap(代码段持引用,所有引用退后才 munmap);
- (b) 全 State quiesce(STW)后做 Dispose;
- (c) 锁定 Dispose 时机到「无活跃 JIT 调用」窗口。

承 [../p2-bridge/00-overview](../p2-bridge/00-overview.md) §9 一样的并发缺口。

### 8.2 开放问题(留 [./06-backends](./06-backends.md) 详细设计阶段或记入 [../../../llmdoc/memory/doc-gaps.md](../../../llmdoc/memory/doc-gaps.md))

- **jitContext 精确字段顺序与对齐**:§3.3 字段表是逻辑分组,实际 struct layout(per-arch cache line 对齐、热字段集中)留 [./06-backends](./06-backends.md);
- **自管机器栈精确布局**:§3.4 是逻辑布局;size 已定为 64 KiB(issue #89,承 §7.5),`segToSegDepthCap = 128` 兜底超限走 exit-reason fallback;实际 red zone / guard 页机制 / 溢出检测 per-arch 细化留 [./06-backends](./06-backends.md);
- **helper 表 ABI**:§4.3.3 是协议骨架,实际 indirect call 寄存器约定 / 参数 marshalling per-arch 一份,留 [./06-backends](./06-backends.md);
- **trampoline 寄存器约定**:§2.4 是骨架,实际 callee-saved 集合 / SP 切换序列 / red zone 处理 per-arch 一份,留 [./06-backends](./06-backends.md);
- **macOS arm64 MAP_JIT 的 per-thread 状态与 goroutine 调度交互**:§2.2.2 给方向,实际 pthread_jit_write_protect_np 调用与 goroutine.lock 的协调留 [./06-backends](./06-backends.md);**PJ0 / PJ8 spike 验证(承 §2.2.4)**:P4 是 template JIT,一次 seal 后不再 patch 代码段——是否真需要 MAP_JIT,还是 RW → 一次性 mprotect RX 即可绕过 pthread_jit_write_protect_np 调用?spike 验证若纯走 mprotect 翻面 + 一次性 sealing 在 darwin/arm64 + iOS hardened runtime 下能过,则可省此调用;
- **自管栈池化与生命周期对齐**:issue #89 现实现是每个 jitCtx 一份 64 KiB spill 栈(`AllocSpillStack` 在 `NewJITContext` 里分配);「全局池 vs per-jitCtx」的进一步 tuning 留 [./06-backends](./06-backends.md);
- **bump 分配快路径 inline 与否**:§5.1(a) 提到「若启用」,实测开启 inline bump 是否净收益(vs 全经 helper)留 amd64 spike 后定。

---

## 9. 回填请求

本文几乎全是 P4 自身机制(系统管线 + 世界边界 + 三出口),与 P1 / P2 / P3 现稿无回填需要——

- P1 [01](../p1-interpreter/01-value-object-model.md) §2 GCRef 非 Go 指针纪律 ⇒ 本文 §1.3.5 / §1.4.2 直接复用,源头不变式不动;
- P1 [05](../p1-interpreter/05-interpreter-loop.md) §1 CallInfo 与 §7 Lua 调用不吃 Go 栈 ⇒ 本文 §3.6.3 / §1.1.4 直接复用;
- P1 [06](../p1-interpreter/06-memory-gc.md) §1.3 reloadFrame 纪律 ⇒ 本文 §5 直接复用;
- P2 [01](../p2-bridge/01-profiling.md) §3 路线 B 回边检查点 ⇒ 本文 §1.2.2 + §6.3 直接复用;
- P2 [05](../p2-bridge/05-p3-p4-interface.md) §6 GibbousCode 接口 + status 码体系 ⇒ 本文 §2.1.3 + §4.2 直接复用;
- P3 [03](../p3-wasm-tier/03-memory-model.md) §0.4 P4 build 切回 Go 堆 backing ⇒ 本文 §3.5 直接复用;
- P3 [04](../p3-wasm-tier/04-trampoline.md) §0.4 P3/P4 共用跨层协议 ⇒ 本文 §3.6.3 / §4.3.4 直接复用;
- P3 [05](../p3-wasm-tier/05-safepoint-gc.md) 三类 safepoint ⇒ 本文 §6 直接复用,只换物理形式。

**无回填请求登记**。若 [./06-backends](./06-backends.md) 详细设计阶段发现现稿需修订(例如 jitContext 字段需在 P3 PW10 R2 的 CallInfo 段布局上做对齐扩展),届时回填到 §8.2 开放问题或 [../../../llmdoc/memory/doc-gaps.md](../../../llmdoc/memory/doc-gaps.md)。

---

相关:
[./04-osr-deopt](./04-osr-deopt.md)(OSR exit 物化语义 §3.3 / 三出口物化协议 §6.5) ·
[./06-backends](./06-backends.md)(amd64/arm64 后端实现 / per-arch trampoline asm / jitContext 精确字段 layout / 自管机器栈精确布局 / helper 表 ABI / icache flush asm / MAP_JIT 调用骨架) ·
[../p3-wasm-tier/03-memory-model](../p3-wasm-tier/03-memory-model.md)(arena 收养 wazero memory / §0.4 P4 backing 切回) ·
[../p3-wasm-tier/04-trampoline](../p3-wasm-tier/04-trampoline.md)(P3/P4 共用跨层协议 / bit50 / status 链) ·
[../p3-wasm-tier/05-safepoint-gc](../p3-wasm-tier/05-safepoint-gc.md)(三类 safepoint 模型,P4 一样的) ·
[../p3-wasm-tier/implementation-progress.md](../p3-wasm-tier/implementation-progress.md)(§0.1 PW0 spike 修正:wazero async-preemption-unsafe,P4 一样的) ·
[../p1-interpreter/01-value-object-model](../p1-interpreter/01-value-object-model.md)(§2 GCRef 非 Go 指针 / §3 NaN-boxing / §7 值表示不变式) ·
[../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md)(§1 CallInfo / §7 Lua 调用不吃 Go 栈) ·
[../p1-interpreter/06-memory-gc](../p1-interpreter/06-memory-gc.md)(§3 grow / §5 GC 根 / §8.2 collect) ·
[../p2-bridge/01-profiling](../p2-bridge/01-profiling.md)(§3 回边检查点路线 B) ·
[../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md)(GibbousCode 接口 / status 码 / jitTrampolineEnter) ·
[../roadmap.md](../roadmap.md)(§2 四项税 / §6 不复刻 Go runtime 内部符号) ·
[../../../llmdoc/must/design-premises.md](../../../llmdoc/must/design-premises.md)(前提二 Go runtime 四项税 / 前提四 NaN-box 第一天承诺)





