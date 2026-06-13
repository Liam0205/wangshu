# P3 §5:跨层 safepoint 与 GC——三类布点 + 收口 [06 §12](../p1-interpreter/06-memory-gc.md) 缺口 + locals 写回纪律 + GC 根零新增

> 状态:**设计阶段,详细设计已齐备**(依赖 P1/P2 落地与开工前置 spike 通过后细化;凡涉 wazero 抢占检查点内部机制处标注「待 spike 验证」)。本文是 [00-overview](./00-overview.md) §0 文档地图列出的 **P3 跨层 GC 单一事实源**——三类 safepoint 在 P3 的形态(分配点 / 层边界 / 回边)、收口 [06 §12](../p1-interpreter/06-memory-gc.md) 留下的「编译层接入时 safepoint 如何布点」缺口、locals 缓存写回纪律、写屏障 P3 不动。
> 上游种子:../p3-wasm-tier.md §6(跨层 safepoint,原稿主体 25 行)+ §10 不变式 5(基线 memory-resident + locals 写回纪律),本文大量扩展。
> 上游契约(P1):
> [06-memory-gc](../p1-interpreter/06-memory-gc.md) §5.1(GC 根 R1..R9 完整枚举)、§5.2(各对象类型扫的 GCRef 字段)、§7(safepoint 哲学:分配点 + 层边界)、§8.2(GC 主流程 STW 单趟)、§9.4(写屏障 P1 空实现 + 增量 GC 预留)、§12(跨层 safepoint 缺口——本文收口对象);
> [05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md) §5.1(布点原则:受控位置才允许 runtime 介入)、§5.2(分配点 opcode 清单)、§5.3(opcode 末尾 safepoint 检查机制 + gcPending);
> [12-testing-difftest](../p1-interpreter/12-testing-difftest.md)(GC 压力 fuzz「每分配即 full GC」模式)。
> 上游契约(P3 内部):
> [02-translation](./02-translation.md) §2A(基线 memory-resident 寄存器映射 = locals 写回纪律的源)、§2.2B(locals 缓存优化的纪律)、§3.5(FORLOOP 回边 gcPending 检查 WAT)、§4(pc 物化);
> [03-memory-model](./03-memory-model.md)(arena 收养 wazero memory:GC 根与寄存器同一块物理内存的物理基础);
> [04-trampoline](./04-trampoline.md) §2/§3(层边界是天然 safepoint:crescent↔gibbous↔host trampoline)、§4(status 链错误冒泡)。
> 上游原则面:[../roadmap.md](../roadmap.md) §2(四项税:异步抢占 / GC 精确栈扫描)、§3(safepoint 限定 + 根放 shadow stack)、§5 原则 3(每阶段一块硬骨头:P3 不动内存管理);[../../../llmdoc/must/design-premises.md](../../../llmdoc/must/design-premises.md)(前提三:safepoint 限定在分配点与层边界)。
> 下游契约:[08-testing-strategy](./08-testing-strategy.md)(GC 压力模式跑 gibbous,逼出 locals 写回遗漏 + 助手内根登记缺漏)。
>
> **本文定位一句话**:**P3 把解释器「受控位置才允许 runtime 介入」的 safepoint 哲学原样搬到编译层,落为三类静态布点(分配点 / 层边界 / 回边),并由此收口 [06 §12](../p1-interpreter/06-memory-gc.md) 留下的「编译层接入时 safepoint 如何布点」缺口**。最大的正确性红利来自基线 memory-resident([02 §2A](./02-translation.md)):gibbous 帧的活跃寄存器**就是** thread 值栈槽,[06 §5.1](../p1-interpreter/06-memory-gc.md) 的 R5 原样覆盖,**GC 根枚举代码一行不改**。P3 范围刻意只换执行引擎,不动内存管理——GC 仍是 P1 的 STW full GC,写屏障维持空实现。

---

## 0. 定位:跨层 safepoint 是 P3 内存安全的核心机制

### 0.1 这一篇要回答的问题

P1 [06 §12](../p1-interpreter/06-memory-gc.md) 倒数第三条缺口原话(节录):

> **层边界 safepoint 的具体形态**:§7.1 说层边界是可选 GC 检查点,但 P1 只有解释器层,层边界退化为 VM↔host 边界。「长时间纯计算不分配的循环如何周期 GC」——P1 靠分配点,无分配的死循环不会 GC(也无需,因没产生垃圾)。**P3+ 跨层时 safepoint 形态(回边检查点 vs 调用边界,对齐 `docs/design/roadmap.md` (§2) 异步抢占税解法)在 p3-wasm-tier 定。**

这条缺口把「编译层接入时 safepoint 如何布点」这个问题**显式委托给本文**。本文是这条缺口的收口处:**给出三类布点的精确形态 + WAT 实代码 + GC 根可见性论证,并把缺口标记关闭**(§2)。

### 0.2 一条不变的哲学:受控位置才允许 runtime 介入

P1 解释器 [05 §5.1](../p1-interpreter/05-interpreter-loop.md) 的 safepoint 布点原则:

> roadmap §3 / 前提四锁定:**safepoint 限定在分配点与层边界,根放 shadow stack**。……**为什么不能每条指令都设 safepoint**:① 多数指令(MOVE/算术/比较/跳转)不分配,设了是纯开销;② safepoint 要求把活跃寄存器作为 GC 根可见——若每指令都暴露根,代价极高。**限定在分配点意味着「只有可能制造垃圾的地方才需要可能回收」,这是精确且省的。**

这条哲学**在 P3 一字不改**。P3 不是「换了执行引擎所以要重新发明 safepoint 模型」——恰恰相反,P3 把同一套「受控位置」模型搬到 Wasm 直线代码上,只是「受控位置」的物理形态从「Go 解释器 switch 分支末尾的 `if vm.gcPending`」变成了「Wasm 函数里编译期静态插入的 `(if (i32.load $gcPending) ...)`」。**布点哲学不变,只是布点载体从解释器循环换成编译产物。**

### 0.3 P3 收益与 GC 的关系:GC 不在热路径

回顾 P3 的收益来源([02 §2A](./02-translation.md)):**消灭 dispatch 与译码**,而非寄存器提升。这条对 GC 设计有直接含义——

- gibbous 直线代码的热体(算术、MOVE、比较、跳转)**全部不分配**(§1.4),因此热循环体内**没有 safepoint 也没有 GC**。
- GC 只发生在「分配助手内」(§1.1)或「回边 pending 命中后」(§1.3),这两处都是**冷路径或低频点**(分支预测器友好的恒不跳分支)。
- 这意味着 P3 的 GC 设计**不需要为热路径性能让步**——可以选最简单、最易差分验证的形态(STW full GC,§5),把正确性推理压到最低。

**一句话**:P3 把 GC 留在受控的冷路径上,热路径是无 GC 的直线代码——这既是性能红利,也是正确性红利。

### 0.4 与 P3 其它子文档的边界(谁拥有什么)

| 关注点 | 本文(05)拥有 | 02-translation 拥有 | 04-trampoline 拥有 | 03-memory-model 拥有 |
|---|---|---|---|---|
| **三类 safepoint 形态定稿** | ✅ §1(分配点 / 层边界 / 回边) | 给 FORLOOP 回边 WAT(§3.5),以本文为准 | 给 trampoline 入口协议,层边界 safepoint 含义以本文为准 | — |
| **收口 [06 §12](../p1-interpreter/06-memory-gc.md) 缺口** | ✅ §2(显式标关闭) | — | — | — |
| **GC 根可见性论证** | ✅ §3(R5 原样覆盖,零新增) | 给寄存器映射(§2A),根可见性以本文为准 | — | 给 arena=wazero memory 物理基础 |
| **locals 写回纪律** | ✅ §4(纪律定稿 + 编译器算法) | 给 locals 缓存优化条件(§2.2B),纪律以本文为准 | 跨层调用前写回点本文列举 | — |
| **写屏障 P3 不动** | ✅ §5(STW 维持 + 空屏障) | — | — | — |
| **GC 压力差分验证** | 链 08(§6 转交) | — | — | — |

**关键边界**:**本文是「跨层 GC 安全」的单一事实源**——三类 safepoint 的形态、GC 根可见性、locals 写回纪律在本文定稿,其它文档(02 给 WAT、04 给 trampoline、03 给物理基础)的相关片段以本文为准。

---

## 1. 三类 safepoint 在 P3 的形态(本节核心,扩原 §6.1)

[06 §7.1](../p1-interpreter/06-memory-gc.md) 的两类 safepoint(分配点 + 层边界)在 P3 落地为**三类**:分配点、层边界、回边。多出来的「回边」(§1.3)正是收口 [06 §12](../p1-interpreter/06-memory-gc.md) 关心的「长时间不分配的循环如何周期 GC」——但 P3 的回边只在「确有 pending」时才介入,不是无条件 GC(§1.4 论证)。

总览表(详见各小节):

| 类别 | 物理位置 | 介入机制 | 触发频率 | 对齐 P1 |
|---|---|---|---|---|
| **§1.1 分配点** | gibbous 代码**自身从不分配**;分配全在 imported 助手内 | 助手回 Go,Go 侧同步 collect(同 [06 §8.2](../p1-interpreter/06-memory-gc.md) Alloc 内 collect) | 与 P1 同(分配频率) | [05 §5.2](../p1-interpreter/05-interpreter-loop.md) 分配点清单 |
| **§1.2 层边界** | crescent↔gibbous 的 trampoline([04 §2/§3](./04-trampoline.md)) | trampoline 进出是天然检查点 | 跨层调用频率 | [05 §5.1](../p1-interpreter/05-interpreter-loop.md) 调用边界 safepoint |
| **§1.3 回边** | 循环回边(FORLOOP/TFORLOOP/JMP 向后) | `(if (i32.load $gcPending) (call $h_safepoint))` | 每迭代一次(分支几乎恒不跳) | [05 §5.3](../p1-interpreter/05-interpreter-loop.md) opcode 末尾检查 |

### 1.1 分配点:gibbous 代码自身从不分配

**核心事实:gibbous 翻译产物里没有任何 arena 分配指令。** 所有「在 P1 解释器里会调 `Arena.Alloc`」的 opcode,在 P3 都被翻译为「调 imported Go 助手」——分配发生在助手内,Wasm 侧只看到一个 `call` 和一个 status 返回。

#### 1.1.1 触发分配的 opcode → 对应助手映射(全枚举)

承 [06 §7.2](../p1-interpreter/06-memory-gc.md) 的「会分配 arena 对象的 opcode」清单 + [05 §5.2](../p1-interpreter/05-interpreter-loop.md) 的分配点表,在 P3 全部经助手回 Go:

| 触发分配的 opcode | 分配什么(P1 06 §7.2) | P3 对应 imported 助手 | 助手内 GC |
|---|---|---|---|
| `NEWTABLE` | Table 头 + array 段 + node 段(3 次 Alloc) | `$h_newtable` | 助手内 `Arena.Alloc` 触发,同 [06 §8.2](../p1-interpreter/06-memory-gc.md) |
| `SETLIST` | 可能触发表 array 段 rehash(扩容→新 array 段) | `$h_setlist`(扩容时分配) | 同上 |
| `CLOSURE` | Lua Closure 对象(+ 可能新建 Upvalue) | `$h_closure` | 同上 |
| `CONCAT` | 拼接结果新字符串(+ 可能多个中间串) | `$h_concat` | 同上(中间串落寄存器槽,见 §1.1.3) |
| 字符串 intern(`LOADK` 取字符串常量) | 首次 intern 时入 string table(可能分配 String 对象) | `$h_loadk_str` / intern 助手 | 同上(intern 决策承 [01 §5.7](../p1-interpreter/01-value-object-model.md)) |
| `SETTABLE` 写新键触发 rehash | 新 array/node 段(rehash) | `$h_settable`(rehash 时分配) | 同上 |
| `SETGLOBAL` 写新键触发 rehash | 全局表 rehash | `$h_setglobal`(rehash 时分配) | 同上 |
| 算术/转换产生新字符串(慢路径 `tostring` 等) | 新字符串 | 慢路径助手(`$h_arith` 等) | 同上 |

> **注**:`LOADK` 取**非字符串**常量(number/bool/nil)是直线代码、不分配(常量值烧进代码),无需助手。只有「取字符串常量且该串尚未在本 State arena intern」才走 intern 助手——这与 [01 §5.7](../p1-interpreter/01-value-object-model.md) 的「字符串惰性 intern」决策一致。**编译器据 IC/feedback 不能假定串已 intern**,故 `LOADK` 字符串常量保守走助手(或在升层时预 intern,留 [02](./02-translation.md) PW5 实测后定)。

#### 1.1.2 分配 + GC 都发生在助手内

**gibbous 侧 WAT(分配点的全部「Wasm 实代码」)**:`NEWTABLE A B C` 翻译为一条 imported 助手调用 + status 检查——gibbous 代码里**没有任何分配指令、没有任何 GC 指令**,只有一个 `call` 和一个 `br_if $err`:

```wat
;; NEWTABLE A B C 的 gibbous 翻译(分配点:全部交给助手)
;; ——A/B/C 是编译期常量(arrayHint/hashHint),落为立即数;PC 是 pc 物化(02 §4)
(local.set $st (call $h_newtable (local.get $base) (i32.const PC)
                                 (i32.const A) (i32.const B) (i32.const C)))
(br_if $err (i32.eq (local.get $st) (i32.const 1)))  ;; status=1 ⇒ 错误冒泡(04 §4)
;; status=0:新表已写回 R(A)（即 valueStack[base+A]）,GC 已在助手内完成,直线继续
```

`$h_safepoint`(回边用,§1.3)与 `$h_newtable`(此处)都是**imported Go 函数**——它们的执行体就是 Go 侧的对应逻辑。`$h_newtable` 的 Go 侧执行体 = P1 的 `NEWTABLE` opcode 实现(同一份 `Arena.Alloc` 调用路径,[06 §8.2](../p1-interpreter/06-memory-gc.md)):

```
gibbous 直线代码执行到上面的 (call $h_newtable ...):
  └─ 一次跨层(gibbous → Go,04 §3)
       │
       ▼  Go 侧助手体(= P1 06 §8.2 的 Alloc 路径)
     th := vm.runningThread()              // 取 running thread(R5 现场)
     ref := arena.Alloc(tableSize)         // ← 越过 GC 阈值则置 gcPending
     if vm.gcPending {                     // 06 §8.2:Alloc 内同步 collect
         vm.gc.Collect()                   // STW mark-sweep,跑完才返回(§5.1)
         vm.gcPending = false              // 清 pending(本次已回收)
     }
     ci.savedPC = PC                       // pc 物化(02 §4):写回 CallInfo.savedPC
     初始化 table 头/array/node,写回 R(A)（base+8*A 偏移,共见值栈)
     return STATUS_OK
       │
       ▼  助手返回 gibbous(回到 br_if $err)
  gibbous 直线继续(R(A) 已是新表,GC 已完成)
```

**关键:助手返回时 GC 已完成,Wasm 侧无感**。gibbous 直线代码不需要知道「刚才助手内跑了一次 full GC」——它只看到 `$h_newtable` 返回了 status 0,R(A) 槽里是新表。这与 P1 解释器里「`NEWTABLE` opcode 调 Alloc → Alloc 内 collect → 继续下一条 opcode」**完全同构**,只是中间多了一次跨层 `call`。

> **为什么分配点的「Wasm 实代码」只有一行 call**:这正是 §1.1 的核心论断「gibbous 自身从不分配」的物理体现——分配点在 gibbous 代码里的全部形态就是「调助手 + 查 status」。GC 的全部逻辑(Alloc、collect、根枚举)都在 Go 侧助手里,Wasm 侧一条 GC 指令都没有。这与 §1.3 回边(Wasm 侧有 `i32.load $gcPending` 检查)形成对比:**分配点的 GC 完全在 Go 侧(Wasm 无感),回边的 GC 触发判定在 Wasm 侧(一次 load + 分支)、collect 在 Go 侧。**

#### 1.1.3 为什么 Wasm 侧无感是安全的:根天然可见

助手内 collect 时,GC 要枚举根([06 §5.1](../p1-interpreter/06-memory-gc.md) R1..R9)。gibbous 帧的活跃寄存器在基线 memory-resident 下**就是 thread 值栈槽**——R5(running thread 栈 + CallInfo)原样覆盖(详见 §3)。所以:

- 助手被调时,gibbous 帧的所有活跃值**已经在 arena 值栈里**(因为它们一直住那儿,gibbous 用 `i64.load/store offset=8*i (base)` 直接读写,从不缓存到 Wasm locals——基线形态下);
- GC 枚举 R5 时顺着 `valueStackRef` 遍历 `[0, top)`,**自动覆盖 gibbous 帧的所有活跃寄存器**;
- 助手自己若把某个待放入新表的中间值暂存在 Go 局部(如 CONCAT 中间串),按 [06 §7.2](../p1-interpreter/06-memory-gc.md) 的纪律落寄存器槽或登记 shadow stack(R7/R8)——这是助手体(= P1 opcode 实现)本就有的纪律,P3 不新增。

**一句话**:**基线 memory-resident 使 gibbous 帧的根与解释器帧的根是同一组栈槽,助手内 GC 看到的根图与解释执行时完全等价**(§3.4 详证)。所以「Wasm 侧无感」不是偷工减料,而是「根本就不需要 Wasm 侧做任何事」。

#### 1.1.4 与 locals 缓存优化的交互(前瞻 §4)

上述「根天然可见」**仅在基线 memory-resident 下成立**。若启用 [02 §2.2B](./02-translation.md) 的 locals 缓存优化(把循环局部热槽缓存进 Wasm locals),则缓存值对 GC 不可见 ⇒ **任何助手调用前必须写回栈槽**(§4.2 第一条)。**PW9 验收前 locals 缓存暂不启用**(§4.6),所以分配点的「根天然可见」在 P3 首版无条件成立。

### 1.2 层边界:trampoline 天然检查点

#### 1.2.1 trampoline 就是层边界

crescent ↔ gibbous 的 trampoline([04 §2 crescent→gibbous](./04-trampoline.md) / [04 §3 gibbous→crescent/host](./04-trampoline.md))是 P3 的「层边界」——对应 [06 §7.1](../p1-interpreter/06-memory-gc.md) 第 2 条「层边界:解释器 ↔ 编译层」。与 [05 §5.1](../p1-interpreter/05-interpreter-loop.md) 的「调用边界 safepoint」同位:

| trampoline 方向 | [04](./04-trampoline.md) 出处 | 层边界 safepoint 含义 |
|---|---|---|
| crescent → gibbous(升层函数入口) | [04 §2](./04-trampoline.md) | 进入 Wasm 前,crescent 侧栈一致(刚压完 CallInfo),是 GC 安全点 |
| gibbous → crescent(被调未编译 Proto) | [04 §3](./04-trampoline.md) | 出 Wasm 进解释器,经 `$h_call` 调度助手,Go 侧栈一致 |
| gibbous → host(被调 host fn) | [04 §3](./04-trampoline.md) | 出 Wasm 进 host,经 `callHost`([05 §7.6](../p1-interpreter/05-interpreter-loop.md))原样,host 侧 shadow stack 纪律不变 |

**gibbous 侧 WAT(层边界的「Wasm 实代码」)**:`CALL A B C` 翻译为经 `$h_call` 调度助手——这个 `call` 指令就是「gibbous → crescent/host」的层边界(出 Wasm),与 §1.1 分配点的 `call $h_newtable` 同形(都是一个 imported 助手调用 + status 检查):

```wat
;; CALL A B C 的 gibbous 翻译(层边界:经 $h_call 调度助手,04 §3)
;; ——出 Wasm 进 Go,Go 侧据被调者分派(gibbous/crescent/host,04 §3 分派表)
(local.set $st (call $h_call (local.get $base) (i32.const PC)
                             (i32.const A) (i32.const B) (i32.const C)))
(br_if $err (i32.eq (local.get $st) (i32.const 1)))  ;; status=1 ⇒ 错误冒泡(04 §4)
;; status=0:返回值已回填 R(A..)（共见值栈槽,被调方写),直线继续
```

这个 `call $h_call` 的执行边界(进出 Go)就是层边界 safepoint 的物理位置——Go 侧 `$h_call` 体内,被调的 crescent 帧/host fn 各自的分配点会触发 GC([04 §3](./04-trampoline.md) 分派表),与 P1 的调用边界 safepoint([05 §7](../p1-interpreter/05-interpreter-loop.md))同构。**层边界的 GC 安全性来自「出 Wasm 时本帧栈一致」**(参数已在 R(A..) 槽、返回值写回 R(A..) 槽,共见值栈,GC 扫得到,§3)。

#### 1.2.2 trampoline 是否需要主动调 safepoint:可选

**基线:trampoline 不主动调 safepoint**——因为:

- **crescent → gibbous 方向**:进入 gibbous 后,第一个分配点(§1.1)或回边(§1.3)就会处理 pending。trampoline 本身不分配,无需在此 GC。
- **gibbous → crescent 方向**:经 `$h_call` 调度助手出去,被调 crescent 帧自己跑解释器主循环([05 §5.3](../p1-interpreter/05-interpreter-loop.md)),其分配点会处理 pending。
- **gibbous → host 方向**:host fn 内部若分配,经其 `Arena.Alloc` 触发,与 P1 同。

所以**助手路径已覆盖**绝大多数回收时机,trampoline 主动 safepoint 是冗余的。

#### 1.2.3 一种极端形态的兜底(PW9 验收时评估)

**例外**:若 PW9 验收时发现存在「长 gibbous 函数,既不分配也无回边」的极端形态(例如一段超长的纯算术直线代码,无循环、无表操作、无调用),则该函数从进入到返回**全程无 safepoint**——若此时另一个分配源(理论上单线程下不存在并发分配,但跨多个 gibbous 调用累积)已置 pending,collect 会被推迟到下一个分配点/回边/层边界。

- **P1 口径下这不是问题**(单 goroutine,无并发分配):pending 只会由「本线程的分配」置,而本线程在这段无分配代码里**没有产生新垃圾**,推迟 collect 无害(没垃圾就不急着回收,§1.4)。
- **但若该长函数返回后立即又进入另一个不分配的长函数**,理论上可累积「该收而长期不收」。**这是低概率边角**,P3 首版不处理。
- **兜底方案的 WAT**:若 PW9 实测发现内存增长异常,在 gibbous 函数入口(`$base` 取到后)插一次与回边同形的 safepoint 检查——

  ```wat
  ;; gibbous 函数入口的可选层边界 safepoint(兜底,基线不启用)
  ;; ——与 §1.3 回边 safepoint 同形,只是位置在函数入口而非回边
  (func $proto_N (param $base i32) (result i32)
    (if (i32.load (global.get $gcPending))      ;; 进入时若有 pending
      (then (call $h_safepoint (local.get $base))))  ;; 先 collect,再跑函数体
    ;; ... 函数体(直线代码 + 回边)...
    )
  ```

  代价是每次跨层进入 gibbous 多一次 `i32.load` + 恒不跳分支——**留 PW9 验收时按实测定**(记入 §8 缺口)。基线下不插此检查(助手路径已覆盖)。

**一句话**:层边界 safepoint 在 P3 是「可选的额外触发机会」(同 [06 §7.1](../p1-interpreter/06-memory-gc.md) 对 P1 层边界的定性),基线不启用,助手路径已足够覆盖。

### 1.3 循环回边:gcPending 检查

#### 1.3.1 WAT 实装(承 [02 §3.5](./02-translation.md) FORLOOP)

回边 safepoint 是 P3 相对 P1 解释器**新增的一类布点**——因为 gibbous 把循环编译成直线代码 + 回边跳转,失去了解释器「每条 opcode 末尾都过一次主循环顶部」的天然检查点([05 §5.3](../p1-interpreter/05-interpreter-loop.md))。所以必须在回边显式插一个检查:

```wat
;; FORLOOP A sBx 的回边 safepoint(承 02 §3.5;本文是其单一事实源)
;; ——idx 推进、判界、回填三槽后,跳回循环体前的检查:
(if (i32.load (global.get $gcPending))   ;; ← 一次 i32.load + 分支(几乎恒不跳)
  (then
    (call $h_safepoint (local.get $base))))  ;; pending 命中:回 Go 同步 collect
(br $L_body)                                 ;; 不论是否 safepoint,继续下一迭代
```

完整的 FORLOOP 翻译(含 idx 推进、判界,承 [02 §3.5](./02-translation.md)):

```wat
;; FORLOOP A sBx —— 热回边(FORPREP 已保证三槽 number,05 §10.1)
(local.set $idx (f64.add (f64.load offset=8*A     (local.get $base))    ;; idx += step
                         (f64.load offset=8*(A+2) (local.get $base))))
;; 方向敏感判界;step 编译期常量时(常见)特化为单比较(02 §3.5)
(if (call $continue? (local.get $idx) (f64.load offset=8*(A+1) (local.get $base)) ...)
  (then
    (f64.store offset=8*A     (local.get $base) (local.get $idx))  ;; 回填内部 idx
    (f64.store offset=8*(A+3) (local.get $base) (local.get $idx))  ;; 回填外部循环变量
    ;; ── 回边 safepoint:仅检查标志(本文 §1.3.1)──
    (if (i32.load (global.get $gcPending))
      (then (call $h_safepoint (local.get $base))))
    (br $L_body)))
;; 循环结束:落出,直线继续
```

**TFORLOOP(泛型 for)与向后 JMP(while/repeat 回边)同理**:任何「向后跳的回边」翻译点都插同一个 `(if (i32.load $gcPending) (call $h_safepoint))`。编译器在生成回边 `br` 之前静态插入(§4.3 同款静态插入逻辑)。

#### 1.3.2 成本:一次 i32.load + 几乎恒不跳的分支

- **`i32.load (global.get $gcPending)`**:wazero 编译后是一次内存/全局读,纳秒级。
- **`(if ... (then (call $h_safepoint)))`**:`gcPending` 在「循环体不分配」时**恒为 0**(没产生垃圾就没人置 pending),分支**几乎恒不跳**——对现代 CPU 分支预测器极友好(始终预测「不跳」,几乎零误预测罚)。
- 仅当「循环体内经助手分配置了 pending」(§1.3.3)时分支才跳,进入 `$h_safepoint`——这是低频事件。

**对比 P1**:P1 解释器的 FORLOOP 回边**不是** safepoint([05 §5.3](../p1-interpreter/05-interpreter-loop.md) 末尾注:「FORLOOP 回边在 P1 不是 safepoint」)——因为解释器每条 opcode 末尾已有检查机会([05 §5.3](../p1-interpreter/05-interpreter-loop.md) 的 `if vm.gcPending`),回边无需额外设点。P3 直线代码没有「每条 opcode 末尾」,所以**把检查点搬到回边**——这正是「布点哲学不变,载体变」(§0.2)的具体体现:同一个 `gcPending` 检查,P1 在 opcode 末尾、P3 在回边。

#### 1.3.3 覆盖的回收时机:循环体内分配置了 pending 但 collect 被推迟

考虑一个循环体内有表写入的热循环:

```lua
for i = 1, 1000000 do
  t[i] = i * 2          -- SETTABLE 写新键:可能触发 rehash → 分配
end
```

翻译后,循环体内的 `SETTABLE` 经 `$h_settable` 助手回 Go。**绝大多数迭代**:键已在表内(无 rehash),助手不分配,`gcPending` 不变。**偶发迭代**:写新键触发 rehash,助手内 `Arena.Alloc` 越过阈值置 `gcPending`——但此时助手**可能选择不立即 collect**(为了不在每次 rehash 都 STW;具体 pacing 策略见 [06 §8.3](../p1-interpreter/06-memory-gc.md))。

> **澄清:Alloc 内 collect vs 推迟 collect 的口径**。[06 §8.2](../p1-interpreter/06-memory-gc.md) 的基线是「Alloc 越阈值即同步 collect」(本文 §1.1.2 即按此)。但 [06 §8.3](../p1-interpreter/06-memory-gc.md) 的 pacing 允许「置 pending,延到 safepoint 再 collect」的变体(解释器即如此:[05 §5.3](../p1-interpreter/05-interpreter-loop.md) 的 `if vm.gcPending` 在 opcode 末尾才 collect,Alloc 只置 pending)。**两种口径 P3 都支持**:
> - 若 P1/P3 取「Alloc 内立即 collect」:`gcPending` 在助手返回时已是 false,回边检查恒不跳——回边 safepoint 退化为纯保险(零开销,never taken)。
> - 若 P1/P3 取「Alloc 置 pending、safepoint 才 collect」:助手返回时 `gcPending=true`,**回边 safepoint 正是兑现这次推迟 collect 的地方**。
> **P3 的回边 safepoint 对两种口径都正确**——它只是「若有 pending 就 collect」,不关心 pending 是谁置的、何时置的。这与 [05 §5.3](../p1-interpreter/05-interpreter-loop.md) 解释器的 `if vm.gcPending` 语义完全一致。

**所以回边 safepoint 覆盖的精确时机**:「循环体内某次迭代经助手分配置了 `gcPending`、但助手选择推迟 collect,则回边在下一次(或本次)迭代尾把这次推迟的 collect 兑现」——保证「置了 pending 不会无限推迟」。

#### 1.3.4 gcPending 全局变量在 wazero memory 之外

`$gcPending` 是一个 **Wasm global**(`i32`),**不在 wazero 的 linear memory 里**(arena/值世界在 linear memory,[03](./03-memory-model.md))。原因:

- `gcPending` 是 **VM 控制状态**,不是「值世界」的一部分——它不该混进 arena(否则会被 GC 根扫描误当作值,且偏移寻址会被它占位)。
- **Go 侧 atomic 设 / Wasm 侧 load**:分配助手在 Go 侧通过 wazero API 写这个 global(或经 imported 函数的副作用),gibbous 回边通过 `(i32.load (global.get $gcPending))` 读。
  - **单 goroutine 口径下无并发**:P1/P3 是单 goroutine 解释/执行(roadmap),`gcPending` 的写(分配助手内,Go 侧)与读(回边,Wasm 侧)**串行发生在同一逻辑线程**——助手是 gibbous 用 `call` 同步调入的,助手返回后 gibbous 才继续到回边。所以「atomic」在此是形式上的(防 Go 编译器重排/可见性),实际无真并发。
  - **wazero 暴露 global 的精确 API**(`module.ExportedGlobal("gcPending").Set(...)` vs 经 imported 函数副作用)**待 spike 验证**(记入 §8 缺口;同 [03 §3](./03-memory-model.md) 的 wazero API 待验证项)。

> **设计选择:为什么用 global 而非 linear memory 里的一个约定偏移?** 两种都可行。用 global 的好处:① 语义清晰(VM 控制状态 ≠ 值世界),GC 根扫描天然不碰它;② wazero 对 global 的读优化可能优于任意内存读;③ 与 linear memory 的 grow/视图重取([03](./03-memory-model.md))解耦——`gcPending` 不随 arena grow 移动。**待 spike 确认 wazero global 的读成本 ≤ linear memory 读**(预期相当或更快),否则退化为 linear memory 约定偏移(记 §8)。

### 1.4 不分配的纯计算循环不触发 GC

**核心论断:没垃圾 ⇒ 不需要 collect。** 一个纯计算的热循环(只有算术、MOVE、比较、跳转,无表操作、无字符串构造、无调用):

```lua
local s = 0
for i = 1, 100000000 do
  s = s + i * i - i      -- 全是 number 算术:ADD/MUL/SUB,无分配
end
```

翻译后,循环体是**纯直线 f64/i64 指令**(§1.1 列举的分配 opcode 一个都没有),没有任何助手调用。所以:

- **没有任何分配** ⇒ 没有人置 `gcPending` ⇒ 回边的 `(i32.load $gcPending)` 恒为 0 ⇒ `$h_safepoint` **永不被调** ⇒ **整个循环跑完不触发一次 GC**。
- 这是**正确的**:循环没产生垃圾,没有需要回收的死对象,GC 跑了也是空转(全堆 mark 全部存活,sweep 回收零字节)。**不 GC 不是 bug,是省。**

**与 [06 §12](../p1-interpreter/06-memory-gc.md) 口径完全一致**。06 §12 的缺口原话:「无分配的死循环不会 GC(也无需,因没产生垃圾)」——P3 的回边 safepoint **保持这个口径**:回边只检查 `gcPending`,不无条件 GC。纯计算循环的 `gcPending` 恒 0,所以「不会 GC 也无需 GC」在 P3 原样成立。**这正是 §1.3.2「分支几乎恒不跳」的语义根据**:纯计算循环里这个分支永远不跳。

> **对比错误设计(反面教材)**:若回边设计为「每迭代无条件调一次 `$h_safepoint`」或「每 N 迭代强制 GC」,则纯计算循环会被无意义的 GC 拖垮(每次 collect 全堆 mark 全存活、零回收)。**P3 不这么做**——回边只在确有 pending 时介入,纯计算循环零 GC 开销。这是「受控位置才允许 runtime 介入」哲学的直接收益:**不是「到了回边就 GC」,而是「到了回边且有垃圾待收才 GC」**。

### 1.5 Go 调度器异步抢占是另一回事

**必须厘清:wazero 生成码自己的回边抢占检查点,与本文 §1.3 的 gcPending 检查,是两套互不相干的机制。**

| 维度 | §1.3 的 gcPending 检查(我们的 GC) | wazero 的回边抢占检查点(Go 调度) |
|---|---|---|
| **目的** | 望舒自管 GC 的回收时机(我们自己的 collect) | Go runtime 抢占长跑的 goroutine(防独占 P) |
| **谁插的** | 望舒编译器静态插(§4.3) | wazero 生成码时自己插(roadmap §2「税二」) |
| **检查什么** | `$gcPending` 全局(我们的 GC 标志) | Go 的 `g.preempt` / `stackguard0`(Go runtime 内部) |
| **触发后做什么** | 调 `$h_safepoint` → 我们的 STW collect | Go 调度器切走该 goroutine,稍后切回 |
| **roadmap 定位** | 我们自己 GC 的事(00-overview §8 注:「另算」) | 四项税之「异步抢占税」,wazero 已验证(p3-wasm-tier 原稿 §9) |

**为什么互不相干**:

- **异步抢占税由 wazero 外包**(p3-wasm-tier 原稿 §9 / [00-overview §8](./00-overview.md)):wazero 生成码在循环回边已有 Go 调度器的抢占检查点(已验证)。望舒**一分都不用管**——这是 P3 选 wazero 的本质(把四项税外包)。
- **我们的 gcPending 检查是望舒 GC 的事**:它跟 Go 调度器无关——Go 调度器抢占 goroutine 时,望舒的 GC 既没在跑也不会被触发(GC 只在 §1.1/§1.3 的受控点发生)。反过来,望舒的 GC collect 时(STW),也不依赖 Go 调度器做任何事(单 goroutine 同步跑完)。
- **物理上它们甚至可能在同一个回边**:wazero 在它生成的回边 `br` 前后可能插它的抢占检查;望舒在 `br` 前插 gcPending 检查。**两个检查叠加在同一回边,但语义正交**——一个管 Go 调度,一个管望舒 GC,各读各的标志,各做各的事。

**一句话**:**异步抢占 = wazero 的活(外包);gcPending 检查 = 望舒 GC 的活(自管)。两者在回边相邻,但永不混淆。** p3-wasm-tier 原稿 §9 把这条写进了四项税外包表(异步抢占税「望舒侧剩余义务:无」,并注「§5 §3 的 gcPending 检查是我们自己 GC 的事,另算」)——本节是那条注的展开。

---

## 2. 收口 [06 §12](../p1-interpreter/06-memory-gc.md) 缺口(本节是另一核心)

### 2.1 [06 §12](../p1-interpreter/06-memory-gc.md) 原话回顾

P1 设计 06-memory-gc 在 §12「文档缺口 / 待决」里,**预设了 P3 接入时何处布 safepoint** 这个问题,把它显式委托给 P3 文档。原话(完整):

> **层边界 safepoint 的具体形态**:§7.1 说层边界是可选 GC 检查点,但 P1 只有解释器层,层边界退化为 VM↔host 边界。「长时间纯计算不分配的循环如何周期 GC」——P1 靠分配点,无分配的死循环不会 GC(也无需,因没产生垃圾)。P3+ 跨层时 safepoint 形态(回边检查点 vs 调用边界,对齐 `docs/design/roadmap.md` (§2) 异步抢占税解法)在 p3-wasm-tier 定。

这条缺口包含三个子问题,本文逐一收口:

| [06 §12](../p1-interpreter/06-memory-gc.md) 子问题 | 本文收口处 |
|---|---|
| ① 编译层接入时层边界 safepoint 的具体形态 | §1.2(trampoline 天然检查点,基线可选) |
| ② 长时间纯计算不分配的循环如何周期 GC | §1.4(不分配 ⇒ 不 GC,口径不变)+ §1.3(有 pending 才 GC) |
| ③ safepoint 形态:回边检查点 vs 调用边界,对齐异步抢占税解法 | §1.3(回边 gcPending 检查)+ §1.5(与异步抢占税正交) |

### 2.2 三类布点如何精确收口缺口

本文 §1 的三类布点(分配点 / 层边界 / 回边)对 [06 §12](../p1-interpreter/06-memory-gc.md) 三个子问题的精确应答:

#### 子问题 ① 层边界 safepoint 形态 → §1.2

- **答**:层边界 = crescent↔gibbous↔host 的 trampoline([04 §2/§3](./04-trampoline.md)),与 [05 §5.1](../p1-interpreter/05-interpreter-loop.md) 的调用边界 safepoint 同位。
- **形态定稿**:**基线不主动调 safepoint**(助手路径已覆盖,§1.2.2),仅在 PW9 验收发现极端长函数形态时按实测加(§1.2.3)。这与 [06 §7.1](../p1-interpreter/06-memory-gc.md) 对 P1 层边界的定性(「可选的额外触发机会」)一脉相承。

#### 子问题 ② 纯计算循环周期 GC → §1.4

- **答**:**不需要周期 GC**。纯计算循环不分配 ⇒ 不产生垃圾 ⇒ 没有需要回收的对象 ⇒ 不 GC 是正确的(GC 跑了也是空转)。
- **口径不变**:这与 [06 §12](../p1-interpreter/06-memory-gc.md) 原话「无分配的死循环不会 GC(也无需,因没产生垃圾)」**完全一致**——P3 不改这个口径,回边只在确有 `gcPending` 时介入(§1.3),纯计算循环的 `gcPending` 恒 0。

#### 子问题 ③ 回边检查点 vs 调用边界 + 对齐异步抢占税 → §1.3 + §1.5

- **答(回边 vs 调用边界)**:**两者都用**——回边(§1.3)处理「循环体内分配置 pending 但推迟 collect」的兑现;调用边界(§1.2,即层边界)是天然检查点但基线不主动调。**回边是主力**(覆盖热循环),调用边界是兜底。
- **答(对齐异步抢占税解法)**:**形式上对齐,语义上正交**(§1.5)。「回边检查点」这个形态确实借鉴了异步抢占税的解法(JIT 在循环回边插检查点,roadmap §2),但我们的回边检查的是**望舒 GC 的 `gcPending`**,而非 Go 调度的抢占标志——**两套检查可以叠在同一回边,语义不混**。异步抢占税本身由 wazero 外包(p3-wasm-tier 原稿 §9),我们不管。

### 2.3 与 P1 解释器 safepoint 哲学的同构论证

本文 §1 的三类布点**不是新发明的 GC 模型**,而是 P1 解释器 safepoint 哲学([05 §5](../p1-interpreter/05-interpreter-loop.md) + [06 §7](../p1-interpreter/06-memory-gc.md))在编译层的同构落地。逐条对照:

| P1 解释器(05/06) | P3 编译层(本文) | 同构关系 |
|---|---|---|
| 布点哲学:**受控位置才允许 runtime 介入**([05 §5.1](../p1-interpreter/05-interpreter-loop.md)) | **受控位置才允许 runtime 介入**(§0.2) | **一字不改** |
| 分配点:分配 opcode 内调 Alloc,Alloc 内 collect([06 §8.2](../p1-interpreter/06-memory-gc.md)) | 分配点:分配 opcode 翻译为助手调用,助手内 collect(§1.1) | **载体变(opcode→助手),机制同** |
| 层边界:VM↔host 边界,可选检查点([06 §7.1](../p1-interpreter/06-memory-gc.md)) | 层边界:crescent↔gibbous↔host trampoline,可选(§1.2) | **多了 crescent↔gibbous,但定性同(可选)** |
| 回收时机检查:opcode 末尾 `if vm.gcPending`([05 §5.3](../p1-interpreter/05-interpreter-loop.md)) | 回收时机检查:回边 `(if (i32.load $gcPending))`(§1.3) | **同一个 gcPending 检查,位置从 opcode 末尾搬到回边** |
| 纯计算不分配 ⇒ 不 GC([06 §12](../p1-interpreter/06-memory-gc.md)) | 纯计算不分配 ⇒ 不 GC(§1.4) | **口径一致** |
| GC 根:R5 自动可达(栈在 arena)([06 §5.1](../p1-interpreter/06-memory-gc.md)) | GC 根:R5 原样覆盖(寄存器=栈槽)(§3) | **零新增(基线 memory-resident)** |
| 异步抢占:解释器无此问题(就是 Go 代码)([05 §5.3](../p1-interpreter/05-interpreter-loop.md) 注) | 异步抢占:wazero 外包,与 gcPending 正交(§1.5) | **都由「不是我们的活」处理** |

**核心同构**:**P1 的 `if vm.gcPending`(Go 侧,opcode 末尾)与 P3 的 `(if (i32.load $gcPending))`(Wasm 侧,回边)是同一个检查的两种载体**。检查的标志同源(`gcPending`)、触发后的动作同源(STW collect)、语义同源(有 pending 才收)。P3 没有发明任何新的 GC 触发逻辑——它只是把解释器里「藏在 opcode 末尾」的检查,在直线代码里**显式搬到回边**(因为直线代码没有「opcode 末尾」这个天然位置)。

[05 §5.3](../p1-interpreter/05-interpreter-loop.md) 末尾的预言性注脚已经点明这个同构:

> 它的 safepoint 布点哲学(分配点 + 层边界)与未来 JIT 的回边检查点同源,都是「受控位置才允许 runtime 介入」。

本文 §1/§2 是这条预言的兑现。

### 2.4 缺口本文标记关闭

**收口结论**:[06 §12](../p1-interpreter/06-memory-gc.md) 的「层边界 safepoint 的具体形态」缺口,**本文 §1 已完整收口**:

- 子问题 ①(层边界形态)→ §1.2 定稿(trampoline 天然检查点,基线可选)。
- 子问题 ②(纯计算循环)→ §1.4 定稿(不分配不 GC,口径不变)。
- 子问题 ③(回边 vs 调用边界 + 异步抢占)→ §1.3 + §1.5 定稿(回边为主,调用边界兜底,与异步抢占税正交)。

> **对 [06 §12](../p1-interpreter/06-memory-gc.md) 的回填请求**(记入 [memory/doc-gaps](../../../llmdoc/memory/doc-gaps.md)):把 [06 §12](../p1-interpreter/06-memory-gc.md) 的「层边界 safepoint 的具体形态」缺口条目改为一行收口标记——
>
> > ~~**层边界 safepoint 的具体形态**:……P3+ 跨层时 safepoint 形态在 p3-wasm-tier 定。~~ **【已收口】P3 三类布点(分配点 / 层边界 / 回边)在本文 §1 定稿:分配点经助手内 collect、层边界 trampoline 基线可选、回边 gcPending 检查;纯计算不分配不 GC 口径不变;与 wazero 异步抢占税正交。**
>
> **本期只记录回填请求,不主动改 P1 06**(承 [00-overview §7](./00-overview.md) 的「对 P1/P2 现有文档的回填请求本期只记录不主动改」纪律 + 用户裁决)。P3 落地时(PW4 回边 safepoint 实装)同批把 [06 §12](../p1-interpreter/06-memory-gc.md) 改为收口标记。

---

## 3. GC 根:零新增机制

### 3.1 基线 memory-resident 下,gibbous 帧的活跃寄存器就是 thread 值栈槽

P3 基线选 [02 §2A](./02-translation.md) 全 memory-resident:Lua 寄存器 `R(i)` 在 Wasm 侧用 `i64.load/store offset=8*i (base)` 直接读写 linear memory 栈槽——**寄存器物理上就是 arena 值栈槽**,与解释器逐槽同构([01 §5.6](../p1-interpreter/01-value-object-model.md) / [05 §1.3](../p1-interpreter/05-interpreter-loop.md))。

这意味着:**gibbous 帧运行时,它的所有活跃寄存器值一直住在 arena 值栈里,从不离开**。gibbous 代码读寄存器是 `i64.load`、写寄存器是 `i64.store`,中间值若需暂存也只在单条 opcode 翻译内的 Wasm locals(`$vb`/`$vc`/`$r` 等临时变量,如 [02 §3.2](./02-translation.md) ADD 示例)——这些临时变量**在 opcode 翻译结束时就消亡**,不跨越任何 safepoint(因为基线下助手调用前的活跃值都已 `i64.store` 回栈槽,§4 详)。

### 3.2 [06 §5.1](../p1-interpreter/06-memory-gc.md) R5(running thread 栈 + CallInfo)原样覆盖

[06 §5.1](../p1-interpreter/06-memory-gc.md) 的根集合 R1..R9 里,R5 是「解释器执行现场」:

> | R5 | **当前 running thread 的值栈与 CallInfo** | running Thread 的 valueStackRef / callInfoRef | 栈上 `[0,top)` 的所有 Value + 各 CallInfo 引用的 closure/Value | **解释器执行现场**;§5.2 详述 |

R5 的覆盖范围是「running thread 值栈 `[0, top)` 的所有 Value」。gibbous 帧的活跃寄存器 = `valueStack[base .. base+MaxStack)`(本帧 R0..R(MaxStack-1)),**这个区间是 `[0, top)` 的子区间**(gibbous 帧也压在同一个 thread 值栈上,base/top 由 CallInfo 维护)。所以:

**GC 枚举 R5 时,顺着 `valueStackRef` 遍历 `[0, top)`,自动覆盖 gibbous 帧的所有活跃寄存器。无需任何 gibbous 专属的根登记代码。**

#### 3.2.1 根枚举代码一行不改的精确含义

[06 §5.2](../p1-interpreter/06-memory-gc.md) 的 Thread 对象扫描逻辑:

> | **Thread** | ① 值栈 `valueStack[0..top)` 每槽(若 `IsCollectable`)② CallInfo 数组 `[0..ciTop)` 每帧引用的 closure GCRef + 帧内保存的 Value ③ openUpvalRef 链 ④ resumeFrom/caller thread ref | **最复杂**;栈只扫 `[0,top)` |

这段扫描逻辑**完全不区分「这一帧是 crescent 解释的还是 gibbous 编译的」**——它只扫「值栈 `[0, top)` 的每个槽」和「CallInfo `[0, ciTop)` 每帧的 closure + Value」。gibbous 帧:

- 它的寄存器值在值栈 `[base, top)` 区间 → 被 Thread 值栈扫描覆盖(同 crescent 帧);
- 它的 CallInfo(标了 bit50 `callStatus_gibbous`,[04 §1](./04-trampoline.md))在 CallInfo 数组里 → 被 CallInfo 扫描覆盖(同 crescent 帧),帧引用的 closure GCRef 照扫;
- 它捕获的 upvalue(若有,CLOSURE 翻译)在 openUpvalRef 链里 → 被 openUpvalRef 扫描覆盖(同 crescent 帧)。

**bit50 对 GC 透明**:GC 扫 CallInfo 时只关心「这一帧引用了哪些 closure / Value」,**不读 bit50**(bit50 只供 trampoline 判帧形态,[04 §1](./04-trampoline.md))。所以即便 gibbous 帧标了 bit50,GC 扫描代码也**一行不用改**——它扫的是 closure/Value 字段,不是状态位。

### 3.3 这是基线方案最大的正确性红利

[02 §2A](./02-translation.md) 选基线 memory-resident 的多个理由里,**GC 根零新增**是最大的正确性红利:

| 若选方案 (B) locals 缓存(GC 不可见) | 基线 (A) memory-resident(GC 自动可见) |
|---|---|
| 必须实现「gibbous 帧根登记」机制:每个 safepoint 前把缓存的 locals 写回栈槽(§4),否则 GC 误根 | **无需任何额外机制**:寄存器一直在栈槽,GC 扫值栈自动覆盖 |
| 写回点遗漏 = 灾难性 bug(GC 误回收活对象 / 解释器读脏值,§4.4) | **不存在「写回遗漏」这个 bug 类**(没有缓存就没有写回) |
| GC 压力 fuzz 是主防线(§4.5),需要专门设计 fuzz 模式逼出遗漏 | GC 压力 fuzz 仍跑(§6),但只验「助手内根登记」(P1 已有的纪律),不验 gibbous 帧根(天然正确) |
| 根枚举代码可能需要按帧形态分支(gibbous 帧扫 locals?——但 locals 在 Wasm 栈,GC 扫不到!) | 根枚举代码**一行不改**(§3.2.1) |

**一句话**:**基线 memory-resident 把「gibbous 帧的 GC 根正确性」从一个需要编译器纪律 + fuzz 兜底的工程问题,降级为一个不存在的问题**——因为寄存器从不离开 arena 值栈,GC 看它们和看解释器寄存器没有任何区别。这是 P3「值世界 = linear memory」([03](./03-memory-model.md))物理兑现的直接红利。

### 3.4 与解释器形态完全等价的论证(同一 thread 槽数组、同一 CallInfo 链)

**论断:对 GC 而言,一个 gibbous 帧与一个 crescent 帧的根贡献完全等价——它们贡献的是同一个 thread 值栈的同一段槽、同一条 CallInfo 链。** 严格论证:

#### 3.4.1 值栈层面等价

- crescent 帧:解释器执行时,寄存器值住 `valueStack[base, base+MaxStack)`([05 §1.3](../p1-interpreter/05-interpreter-loop.md) Frame 是这段栈的视图,Go 侧缓存 `stk` 指针但值在 arena)。
- gibbous 帧:执行时,寄存器值住 `valueStack[base, base+MaxStack)`(基线 memory-resident,`i64.load/store offset=8*i (base)`)。
- **同一个 `valueStack`(同一 thread 的 `valueStackRef` 指向的同一块 arena 内存),同一段偏移 `[base, base+MaxStack)`**。GC 扫这段槽时,看到的是「某帧的活跃寄存器值」——它不在乎也无从区分这帧是谁执行的。

#### 3.4.2 CallInfo 链层面等价

- crescent 帧:压一个 CallInfo([05 §1.2](../p1-interpreter/05-interpreter-loop.md)),bit50 = 0(`callStatus_gibbous` 未置)。
- gibbous 帧:压一个 CallInfo([04 §1](./04-trampoline.md)),bit50 = 1。
- **同一条 CallInfo 链(同一 thread 的 `callInfoRef` 指向的同一数组),帧结构相同**(都含 base/top/savedPC/closure 引用等)。GC 扫 CallInfo 链时,逐帧取 closure GCRef + 帧内 Value([06 §5.2](../p1-interpreter/06-memory-gc.md))——bit50 不参与扫描(§3.2.1)。

#### 3.4.3 pc 物化保证 CallInfo.savedPC 在两层一致

[02 §4](./02-translation.md) 的 pc 物化:gibbous 帧在每个可能 safepoint/调用/出错的点,把编译期已知的 pc 作为立即数传给助手,助手写回 `CallInfo.savedPC`。这保证:

- gibbous 帧的 CallInfo 在任何 safepoint 时刻,`savedPC` 字段都是**有效的**(指向当前正在执行的 opcode)——与 crescent 帧的 `savedPC` 语义一致。
- GC 若需要据 savedPC 做任何事(P1 GC 不需要,但 traceback 需要,[09](../p1-interpreter/09-errors-pcall.md)),gibbous 帧提供的信息与 crescent 帧逐字节一致。

#### 3.4.4 等价性的差分验证锚点

这个「gibbous 帧根与 crescent 帧根等价」的论断,在 [08-testing-strategy](./08-testing-strategy.md) 的 GC 压力差分里被验证:同一 Proto 分别在 crescent 和强制 gibbous 下跑 GC 压力 fuzz,若 gibbous 帧的根贡献与 crescent 不等价(漏根 → 误回收 → 后续读脏),差分必炸(输出不一致或崩溃)。**这是论断的硬性验证锚点**——不是「我们论证它对所以它对」,而是「论证 + fuzz 双重保证」。

#### 3.4.5 一个具体的根扫描走查(gibbous 帧 ≡ crescent 帧)

把抽象论证落到一个具体场景。设 Lua 函数 `f` 升层为 gibbous,某时刻它在执行一个表写入,`R3` 持有一个活 Table、`R5` 持有一个活 String,base = 1024(字节偏移),MaxStack = 8:

```
arena linear memory(同一块物理内存,03):
  valueStack 区:
    ...
    offset 1024+8*3 = 1048: [Table GCRef = 0x......, tag=0xFFFB]   ← R3,活 Table
    offset 1024+8*5 = 1064: [String GCRef = 0x......, tag=0xFFFF]  ← R5,活 String
    ...
  CallInfo 数组:
    ci[k]: { base=1024, top=1024+8*top', savedPC=PC, closureRef=f的closure, word2.bit50=1 }
                                                                              └─ gibbous 帧标记
```

gibbous 此刻执行到 `(call $h_settable ...)`(§1.1),助手内 collect。GC 的 mark 走查(逐字照 [06 §5.2](../p1-interpreter/06-memory-gc.md) Thread 扫描逻辑):

```
markValue(R3 thread root) → 扫 Thread 对象:
  ① 值栈 [0, top) 每槽:
       ... offset 1048(R3):IsCollectable(tag=0xFFFB)=true ⇒ markValue(Table) → 标灰入栈 ✓
       ... offset 1064(R5):IsCollectable(tag=0xFFFF)=true ⇒ markValue(String) → 标灰(叶子,标黑)✓
  ② CallInfo [0, ciTop) 每帧:
       ci[k]:取 closureRef(f的closure)⇒ markValue → 标灰 ✓
              【注:此处只读 closureRef + 帧内 Value,不读 word2.bit50 —— §3.2.1】
  ③ openUpvalRef 链:f 若有开放 upvalue,逐个标灰 ✓
```

**关键观察**:这段 mark 走查**与 `f` 在 crescent 解释执行时的 mark 走查逐字相同**——因为:

- R3/R5 的活对象在**同一个 valueStack 的同一偏移**(1048/1064),无论 `f` 是 crescent 跑还是 gibbous 跑(基线 memory-resident,§3.1);
- CallInfo `ci[k]` 在**同一个 CallInfo 数组的同一位置**,`closureRef`/帧内 Value 字段相同(§3.4.2),`bit50` 不参与扫描(§3.2.1);
- mark 代码**没有任何 `if ci.bit50 == gibbous` 的分支**——它一视同仁扫值栈槽 + CallInfo,根本不知道(也不需要知道)这帧是谁执行的。

**所以 §3.4 的「完全等价」不是修辞,而是「同一块内存的同一段被同一段代码扫描」的物理事实。** gibbous 帧对 GC 而言,就是 crescent 帧——它们贡献给根集合的是同一组栈槽 + 同一条 CallInfo 链。这正是 §3.3「把 gibbous 帧 GC 根正确性降级为不存在的问题」的根据。

> **与 §3.4.4 验证锚点的闭环**:上面的走查论证「gibbous 帧根 ≡ crescent 帧根」;[08](./08-testing-strategy.md) 的 GC 压力差分把同一 Proto 在两层各跑一遍,逐字节比对输出——若论证有漏(某种 gibbous 帧的根没被覆盖),差分必炸。**论证给出「为什么对」,fuzz 给出「确实对」,两者闭环。**

> **与 §4(locals 缓存)的关系**:§3.4 的等价性**仅在基线 memory-resident 下成立**。一旦启用 locals 缓存(§4),gibbous 帧的部分寄存器值会暂时离开 arena 栈槽(进 Wasm locals),此时「值栈层面等价」被打破——必须靠 §4 的写回纪律在每个 safepoint 前恢复等价。**PW9 验收前 locals 缓存不启用(§4.6),所以 §3.4 的等价性在 P3 首版无条件成立。**

---

## 4. locals 缓存的写回纪律(若启用 [02 §2.2B](./02-translation.md) 优化)

> **前置声明**:本节描述的是 [02 §2.2B](./02-translation.md) 的 locals 缓存**优化**的安全纪律。**PW9 验收前此优化不启用**(§4.6),P3 首版只跑基线 memory-resident(§3,GC 根零新增,无需本节纪律)。本节是「若将来启用,纪律是什么」的定稿——也是 [02 §2.2B](./02-translation.md) 那句「写回点由编译器静态插入,漏写回=GC 误根/解释器读脏」的展开。

### 4.1 缓存进 Wasm locals 的值对 GC 不可见 ⇒ 必须写回

locals 缓存优化:把循环局部热槽(FORLOOP 的 idx/limit/step 三槽是首选,[02 §2.2B](./02-translation.md))缓存进 Wasm locals(wazero 编译为机器寄存器/栈槽),省去每次访问的 `i64.load/store`。

**问题**:Wasm locals 不在 linear memory 里——它在 wazero 自管的 Wasm 执行栈上(机器寄存器或原生栈槽)。GC 扫描的根([06 §5.1](../p1-interpreter/06-memory-gc.md))**只覆盖 arena**(值栈、CallInfo、全局表等),**完全看不见 Wasm locals**:

- GC 扫 thread 值栈 `[0, top)` 时,看到的是 arena 栈槽的值——**若某寄存器的最新值缓存在 Wasm local 里、还没写回栈槽,则栈槽里是过期值**(GC 看到过期值)。
- 若过期值是「一个已被覆盖的旧 GCRef」,GC 会把那个旧对象当根(误根,过度保守,内存泄漏);
- 若过期值是「nil 但实际寄存器现在缓存着一个活 GCRef」,GC 看不到那个活 GCRef ⇒ **误回收活对象**(灾难,§4.4)。

**所以**:**任何 GC 可能介入的点之前,缓存在 Wasm locals 的寄存器值必须写回 arena 栈槽**——让 GC 扫栈时看到的是最新值。这与 [02 §2A](./02-translation.md) 表里 (B) 方案的「GC/解释器不可见,边界必须物化写回」「每边界一次写回」是同一回事。

### 4.2 必须写回的位置(全部静态可枚举)

写回点的集合是**编译期完全静态可枚举的**——因为「哪些点可能触发 GC / 可能读栈」在翻译时全部已知。完整清单:

| # | 写回位置 | 为什么 | 出处 |
|---|---|---|---|
| W1 | **全部 helper 调用之前** | helper(分配助手、慢路径助手)内可能 collect(§1.1),GC 扫栈需看到最新值;且 helper 可能读栈(如 `$h_call` 读参数槽) | §1.1 |
| W2 | **回边 safepoint 命中之前** | 回边 `$h_safepoint` 内 collect(§1.3),GC 扫栈需最新值。**注意:写回要在 `(if (i32.load $gcPending) ...)` 的 then 分支内、`$h_safepoint` 调用前**(不命中则不必写回,省开销) | §1.3 |
| W3 | **跨层调用之前** | crescent→gibbous 入口(进 Wasm 前 crescent 侧写回——但 crescent 不用 Wasm locals,故此项实为 gibbous→crescent/host 方向:出 Wasm 前把缓存写回,让被调方/GC 看到最新栈) | §1.2 / [04 §3](./04-trampoline.md) |
| W4 | **任何分支跳到「可能触发 GC 的路径」之前** | 若一个条件分支的某一侧会调 helper 或命中回边,则进入该侧前写回(编译器按控制流图分析,§4.3) | §4.3 |

#### 4.2.1 写回位置的统一刻画

W1..W4 可统一为一句话:**任何「控制权可能离开本 gibbous 帧的纯直线代码段、进入一个可能触发 GC 或可能读 arena 栈的上下文」的边界之前,写回所有脏 locals 缓存。** 这些边界全部是静态的(call 指令、回边 br、跨层 call)——编译器在生成这些指令前插写回,不存在「运行期才知道要不要写回」的情况。

#### 4.2.2 不需要写回的点(优化空间)

- **纯直线代码段内部**(两个 helper 调用之间的算术/MOVE):无需写回,缓存可一直用。
- **回边不命中时**(W2 的 `(if ...)` 不跳):无需写回——这正是 locals 缓存优化的收益所在(热循环的绝大多数迭代不命中回边 safepoint,缓存全程不写回)。
- **帧返回前**(RETURN):返回值要写回栈槽(供调用方读),但这是 RETURN 翻译本身的语义(不是 GC 写回);返回后本帧 locals 消亡,无 GC 可见性问题。

### 4.3 编译器静态插写回的算法

编译器在翻译一个启用了 locals 缓存的 Proto 时,按以下算法静态插入写回:

```
// 伪码:编译器对一个 Proto 的 locals 缓存写回插入(承 02 §2.2B 的槽选择)
//
// 输入:Proto p、被缓存的寄存器集合 cachedRegs(02 §2.2B 选的循环局部热槽)
// 输出:在翻译产物里,每个写回点前插入 (i64.store offset=8*r (base) (local.get $cache_r))

func insertWritebacks(p *Proto, cachedRegs set[Reg]) {
    cfg := buildCFG(p)                 // 控制流图(基本块 + 边)
    for each opcode op in p at pc:
        switch classifyGCBoundary(op) {
        case AllocHelper, SlowHelper:           // W1:helper 调用
            // helper 调用指令前,写回所有「在此点活跃且脏」的缓存寄存器
            for r in liveDirtyCached(pc, cachedRegs):
                emit(i64.store offset=8*r (base) (local.get cache[r]))
            // helper 可能改栈槽(如 $h_call 回填返回值)⇒ 调用后缓存失效,重 load
            invalidateCache(cache, op.clobberedRegs)

        case BackEdge:                          // W2:回边 safepoint
            // 仅在 gcPending 命中分支内写回(不命中不写,省开销)
            emit(if (i32.load $gcPending)
                   (then
                     <for r in liveDirtyCached: i64.store offset=8*r (base) (local.get cache[r])>
                     (call $h_safepoint (base))))

        case CrossLayerCall:                    // W3:跨层调用(gibbous→crescent/host)
            for r in liveDirtyCached(pc, cachedRegs):
                emit(i64.store offset=8*r (base) (local.get cache[r]))
            invalidateCache(cache, allRegs)     // 跨层后栈可能全变,保守全失效

        case Branch:                            // W4:分支到可能 GC 的路径
            for each successor blk in cfg.successors(pc):
                if blockReachesGCBoundary(blk):  // 该后继块内有 W1/W2/W3
                    insertWritebackOnEdge(pc → blk, liveDirtyCached(pc))

        default:                                // 纯直线 opcode:无需写回
        }
}
```

#### 4.3.1 算法的关键性质

- **保守正确优先**:`liveDirtyCached` 的活跃性分析若不确定,**宁可多写回**(写回一个其实没脏的缓存只是多一条 `i64.store`,无正确性损失;漏写回是灾难,§4.4)。这与 [03 §1](../p2-bridge/03-compilability-analysis.md) 的「保守第一,宁漏勿误」同源——GC 安全上「宁多写回勿漏」。
- **W4 的边上插写回**:分支到 GC 路径时,写回插在「边」上(进入该后继块前),而非块内——避免在不通往 GC 的另一侧分支也付写回开销。
- **helper 调用后失效缓存**:helper 可能改栈槽(`$h_call` 回填返回值、`$h_settable` 改表但不改本帧槽),编译器据 helper 语义标记「调用后哪些缓存寄存器失效需重 load」。

#### 4.3.2 算法精确定义留 PW5/PW9 实测后定

> 上述算法是**骨架**。精确定义(活跃性分析的精度、W4 的 CFG 分析深度、helper clobber 集的标注方式、缓存哪些槽——FORLOOP 三槽之外是否扩展)**留 PW5/PW9 实测后定**(承 [02 §2.2B](./02-translation.md) 「FORLOOP 三槽之外是否扩展,待基线数据」+ [00-overview §10](./00-overview.md) 「locals 缓存的槽选择算法待基线数据」)。**P3 首版不启用 locals 缓存(§4.6),此算法是前瞻设计。** 记入 §8 缺口。

### 4.4 漏写回 = GC 误根 / 解释器读脏(灾难性 bug)

漏写回的两类灾难,逐一推演:

#### 4.4.1 GC 误回收(漏写回一个活 GCRef)

```
场景:寄存器 R5 缓存在 Wasm local $cache_5,当前持有一个活 Table 的 GCRef。
      栈槽 valueStack[base+5] 里是旧值(nil,或一个已被覆盖的旧对象)。
漏写回:编译器漏在某个 helper 调用前写回 $cache_5 → 栈槽。
helper 内 collect:GC 扫 thread 值栈,valueStack[base+5] = nil(过期)
                 ⇒ GC 看不到 $cache_5 持有的活 Table ⇒ 若无其它根引用它 ⇒ 误回收!
helper 返回后:gibbous 用 $cache_5 访问那个 Table ⇒ 悬垂 GCRef
            ⇒ 访问到已回收/已复用的 arena 内存 ⇒ 崩溃或脏读(灾难)
```

这与 [06 §5.1](../p1-interpreter/06-memory-gc.md) 描述的 shadow stack 盲区(host 把 GCRef 暂存 Go 局部,GC 看不见 → 误回收)**同构**——只是这里 GCRef 暂存在 Wasm local 而非 Go 局部,GC 同样看不见。**locals 写回纪律之于 gibbous 帧,等价于 shadow stack 之于 host fn**(都是「补 GC 看不见的盲区」)。

#### 4.4.2 解释器读脏(漏写回后跨层进解释器)

```
场景:gibbous 帧的 R3 缓存在 $cache_3,持有最新值 X。栈槽 valueStack[base+3] 是旧值 Y。
漏写回:gibbous→crescent 跨层调用(§1.2/W3)前漏写回 $cache_3。
被调 crescent 帧:若它读到 base+3(如它是本帧的延续,或经某种栈共享)
              ⇒ 读到旧值 Y 而非最新值 X ⇒ 解释器算出错误结果(灾难)
            ——即便不直接读,GC 在跨层边界介入时(§1.2)也会看到旧值 Y(回到 §4.4.1)
```

**两类灾难的共性**:**Wasm locals 是 gibbous 帧的「私有视图」,arena 栈槽是「公共真相」。任何「公共真相被读」的时刻(GC 扫栈、解释器读栈、helper 读栈),私有视图必须已同步到公共真相——否则读者看到过期数据。** 写回纪律就是「在公共真相被读前同步私有视图」。

#### 4.4.3 为什么这是「灾难性」而非「普通」bug

- **不确定性**:漏写回不一定每次都炸——只有「恰好该寄存器持有活 GCRef + 恰好该点触发 GC + 恰好无其它根引用」三者同时满足才误回收。所以它可能在测试里偶发、在生产里随机崩——**最难调的一类 bug**。
- **跨层污染**:误回收后,悬垂 GCRef 可能被传播到其它帧、其它对象,崩溃点离 bug 点很远。
- **静默错果**:解释器读脏可能不崩溃,只是算出错误结果——**违反 [design-premises](../../../llmdoc/must/design-premises.md) 最忌的「投机错误静默错果」类**(虽然这里不是投机,但「漏写回导致静默错果」同样致命)。

**所以**:locals 缓存优化的「收益」(省 i64.load/store)必须**远大于**「写回开销 + 这类 bug 的风险」才值得启用——这是 [02 §2.2B](./02-translation.md) 说「byte-equal 且不更慢才采纳」+ §4.6「PW9 前不启用」的根据。

### 4.5 GC 压力 fuzz 是主防线

漏写回 bug 的不确定性(§4.4.3)使它**无法靠常规测试可靠捕获**——必须用 GC 压力 fuzz 强制把「恰好该点触发 GC」这个条件变成「每个点都触发 GC」:

- **GC 压力模式**([12](../p1-interpreter/12-testing-difftest.md) 的「每分配即 full GC」模式):把 GC 触发阈值降到最低(每次 Alloc 都 collect),使**每个 helper 调用、每个回边都触发一次 full GC**。
- **效果**:漏写回的「恰好触发 GC」条件被满足在**每一个 GC 边界**——若某点漏写回了一个活 GCRef,这个模式下该点的 GC 必然误回收它,后续访问必然炸/脏。**把偶发 bug 变成必现 bug。**
- **配合强制全升模式**([08 §2](./08-testing-strategy.md)):所有 CompCompilable 的 Proto 强制编译为 gibbous,使所有可能的 gibbous 帧都被 GC 压力覆盖,消除「热度时序导致哪些函数被编译」的不确定性。

**这是 [02 §2.2B](./02-translation.md) 那句「漏写回=GC 误根/解释器读脏,GC 压力 fuzz([12](../p1-interpreter/12-testing-difftest.md))是主防线」的展开。详见 [08-testing-strategy](./08-testing-strategy.md)(§6 转交)。**

> **基线下 GC 压力 fuzz 仍跑,但验的面不同**(§3.3 表):基线 memory-resident 下没有 locals 缓存,所以 GC 压力 fuzz **不验 gibbous 帧根**(天然正确,§3.4)——它验的是「助手内根登记缺漏」(P1 已有的 shadow stack 纪律在 gibbous 调用助手时是否仍正确,§6)。**locals 缓存启用后**,GC 压力 fuzz 才额外承担「逼出 locals 写回遗漏」的职责。

### 4.6 PW9 验收前 locals 缓存暂不启用,只启基线 memory-resident

**定稿:P3 首版(PW1..PW9)只启用基线 memory-resident(§3),locals 缓存优化(本节 §4.1..§4.5)暂不启用。** 根据:

| 理由 | 出处 |
|---|---|
| 基线 memory-resident 的 GC 根零新增,正确性红利最大(§3.3) | §3.3 |
| locals 缓存的写回纪律引入一整类灾难性 bug(§4.4),需 fuzz 兜底,风险/收益比在基线数据出来前不明 | §4.4 |
| [02 §2.2B](./02-translation.md) 定「locals 缓存是受纪律的优化,byte-equal 且不更慢才采纳」——采纳前提是 spike 后 A/B 实测 | [02 §2.2B](./02-translation.md) |
| P3 收益来源是「消灭 dispatch 与译码」(§0.3),不靠寄存器提升——locals 缓存的边际收益可能不大 | [02 §2A](./02-translation.md) |
| 「每阶段一块硬骨头」(roadmap §5 原则 3):P3 首版先把分层骨架 + 翻译跑通,locals 缓存留作后续优化 | [roadmap](../roadmap.md) §5 |

**所以**:**本文 §4 是「若将来启用,纪律是什么」的前瞻定稿;P3 首版的 GC 安全完全靠 §3 的基线机制(零新增、自动可见),不依赖 §4 的任何写回纪律。** 这把 P3 首版的 GC 正确性风险压到最低——与 §3.3「把一个工程问题降级为不存在的问题」呼应。

> **启用决策留 PW9 实测后定**(记 §8):PW9 性能基准(循环密集 ≥2x over P1,[08 §1](./08-testing-strategy.md))若达标,则 locals 缓存无需启用(目标已达);若某些循环形态卡在 2x 以下且分析显示瓶颈在 `i64.load/store`,再评估对 FORLOOP 三槽启用 locals 缓存 + 本节写回纪律 + GC 压力 fuzz 兜底。

---

## 5. 写屏障与增量 GC:P3 不动

### 5.1 P3 的 GC 仍是 P1 的 STW full GC

**定稿:P3 不改动 GC 算法——仍是 [06 §7.3/§8.2](../p1-interpreter/06-memory-gc.md) 的 STW(stop-the-world)full GC。** P3 只换执行引擎(解释器循环 → wazero Wasm),**不换内存管理**:

- **mark**:从根 R1..R9 三色标记([06 §5](../p1-interpreter/06-memory-gc.md)),gibbous 帧的根经 R5 自动覆盖(§3),mark 算法一行不改。
- **sweep**:遍历 gcnext 链回收死白([06 §8.1](../p1-interpreter/06-memory-gc.md)),与 gibbous 无关(sweep 不关心对象是被谁创建的)。
- **STW 协调**:单 goroutine 下天然无需停顿协调([06 §7.3](../p1-interpreter/06-memory-gc.md))——gibbous 调助手是同步 `call`,collect 在助手内同步跑完(§1.1.2),执行 GC 时「没有其它代码在跑」这个 P1 红利**原样保留**(gibbous 帧此刻停在 `call $h_newtable` 上等返回,不在执行)。

#### 5.1.1 STW 红利在 gibbous 下原样保留

[06 §7.3](../p1-interpreter/06-memory-gc.md) 的三条 STW 红利,在 gibbous 下逐条保留:

| [06 §7.3](../p1-interpreter/06-memory-gc.md) STW 红利 | gibbous 下是否保留 | 为什么 |
|---|---|---|
| mark 看到的对象图静止(无 mutator 修改,无需写屏障) | ✅ 保留 | collect 在助手内同步跑,gibbous 帧停在 call 上,不修改对象图 |
| 根集合静止(R1..R9 在 GC 期间不变,枚举一次即可) | ✅ 保留 | gibbous 帧的根(值栈槽,§3)在 collect 期间不变(gibbous 没在执行) |
| sweep 静止(无并发分配干扰) | ✅ 保留 | 无并发(单 goroutine);gibbous 不在分配(它停在助手 call 上) |

**核心**:gibbous 调助手是**同步阻塞**的——gibbous 执行到 `call $h_newtable` 后,控制权完全交给 Go 助手,gibbous 帧「冻结」在那一点直到助手返回。助手内 collect 时,gibbous 帧是静止的(和 P1 解释器调 Alloc 时主循环静止一样)。所以**STW 的「正确性推理退化为单线程顺序逻辑」([06 §7.3](../p1-interpreter/06-memory-gc.md))在 gibbous 下完全成立**。

### 5.2 写屏障接口维持空实现

**定稿:P3 不启用写屏障——维持 [06 §9.4](../p1-interpreter/06-memory-gc.md) 的空实现。**

[06 §9.4](../p1-interpreter/06-memory-gc.md) 的写屏障接口是为「未来增量/分代 GC」预留的占位,P1 空实现(STW 不需要)。P3 **同样空实现**:

```go
// 承 06 §9.4:写屏障接口占位,P3 仍空实现
func (c *Collector) writeBarrier(parent value.GCRef, child value.Value) {
    // P1: no-op.
    // P3: 仍 no-op —— P3 是 STW full GC,无并发标记,无「黑指白且 mark 已略过」窗口。
    // P3+(增量 GC,留 P3 之后评估):届时填充 forward/back barrier。
}
```

#### 5.2.1 gibbous 写表槽不需要屏障(STW 下)

回顾 [06 §9.4](../p1-interpreter/06-memory-gc.md) 的屏障插桩点(SETTABLE/SETLIST/SETUPVAL/SETGLOBAL/setmetatable——「把可回收 child 存入 arena 对象 parent」的写)。这些写在 gibbous 里**全部经助手**(§1.1:SETTABLE/SETLIST/SETGLOBAL 写新键经 `$h_settable` 等;SETUPVAL/upvalue 关闭经 helper)——**助手体就是 P1 的对应 opcode 实现**,P1 在那里 no-op 调 `writeBarrier`,gibbous 经助手走同一路径,**同样 no-op**。

**关键**:**gibbous 直线代码本身不直接写表槽**(表写入是 `$h_settable` 助手的活,因为可能 rehash 分配,§1.1),所以「gibbous 写表槽需插屏障」这个问题在 P3 **根本不出现在 gibbous 代码里**——它出现在助手里,而助手 = P1 opcode 实现,P1 已 no-op。

> **澄清:即便 SETTABLE 不触发 rehash(写已有键,纯改槽值),P3 也经助手吗?** [02 §3.4](./02-translation.md) 给的 GETTABLE 是「IC 快照命中走直达槽 `$slot_load`」,SETTABLE 同理可有「IC 命中走直达槽 store」的快路径(写已有键不分配)。**若 SETTABLE 快路径在 gibbous 直线代码里直接 `i64.store` 写表槽**(不经助手),则这是「gibbous 直接写表槽」的情况——但 **STW 下仍 no-op**(无并发标记,无屏障需求,§5.1.1)。屏障只在增量 GC 才需要(§5.3),那时这条 SETTABLE 直达槽写就是一个屏障插桩点。**P3 STW 下无论 SETTABLE 走助手还是走直达槽,都 no-op。**

### 5.3 增量 GC 留 P3 之后单独评估

**定稿:增量 GC(及其所需的写屏障)留 P3 之后单独评估,不在 P3 范围内。**

增量 GC 的动机:STW full GC 的暂停时间 = 全堆 mark+sweep([06 §7.3](../p1-interpreter/06-memory-gc.md) 末:「代价是 GC 暂停时间 = 全堆 mark+sweep」)。对大堆,这个暂停可能不可接受,需增量化(把 mark 分多步,与 mutator 交错)。增量化要求写屏障(维持三色不变式,[06 §4.3/§9.4](../p1-interpreter/06-memory-gc.md))。

**P3 不做增量 GC**,因为:

- **范围纪律**(roadmap §5 原则 3「每阶段一块硬骨头」):P3 的硬骨头是「翻译 + 跨层协议 + 分层骨架」(§5.4),不是 GC。
- **增量 GC 是跨阶段的大工程**:它需要双白 + gray 链 + 写屏障 + 增量 mark 调度,且**对 gibbous 有特殊含义**——届时 gibbous 直接写表槽(SETTABLE 直达槽快路径,§5.2.1 澄清)需在 Wasm 代码里插逻辑屏障(`(call $h_writebarrier parent child)` 或内联屏障逻辑),这是 gibbous 翻译的新复杂度。
- **基础设施已就位但不启用**:[06 §4.3](../p1-interpreter/06-memory-gc.md) 的双白、[06 §9.4](../p1-interpreter/06-memory-gc.md) 的屏障接口、[01 §4](../p1-interpreter/01-value-object-model.md) 的 color 位都已为增量 GC 预留——**P3 之后接手时,基建已在,只需填充屏障实现 + 增量调度 + gibbous 屏障插桩**。

#### 5.3.1 届时 gibbous 写表槽需插逻辑屏障(前瞻)

> **前瞻(非 P3 范围)**:增量 GC 启用后,gibbous 的 SETTABLE/SETLIST 直达槽快路径(§5.2.1)需在 `i64.store` 写表槽后插逻辑屏障:
>
> ```wat
> ;; 增量 GC 启用后(P3 之后)的 SETTABLE 直达槽写 + 屏障(前瞻,非 P3)
> (i64.store offset=... (table_slot_addr) (local.get $child))   ;; 写表槽
> (call $h_writebarrier (local.get $parent_ref) (local.get $child))  ;; 逻辑屏障
> ```
>
> 这个 `$h_writebarrier` 助手在 STW 下 no-op(§5.2),增量 GC 下做 forward/back barrier([06 §9.4](../p1-interpreter/06-memory-gc.md))。**编译器是否插这个 call,由「是否启用增量 GC」的编译开关决定**——P3 范围内开关恒关,不插。记 §8 缺口(增量 GC 的 gibbous 屏障插桩留 P3 之后)。

### 5.4 P3 范围刻意只换执行引擎,不动内存管理

**这是 P3 的核心范围纪律,呼应 roadmap §5 原则 3。** P3 是「gibbous / tier-1 的第一个发射后端」([00-overview §0](./00-overview.md))——它的硬骨头是把分层骨架(升层 / fallback / trampoline / 跨层差分)跑通,后端是不用调试机器码的 wazero。**内存管理(arena/GC)是 P1 已经啃下的硬骨头,P3 原样继承,一字不改:**

| 维度 | P1 已定稿 | P3 的动作 |
|---|---|---|
| arena 分配器(bump + freelist) | [06 §2](../p1-interpreter/06-memory-gc.md) | **不动**(仅 backing 来源改为收养 wazero memory,[03](./03-memory-model.md);分配器逻辑不变) |
| GC 算法(STW full GC) | [06 §7/§8](../p1-interpreter/06-memory-gc.md) | **不动**(§5.1) |
| GC 根集合(R1..R9) | [06 §5.1](../p1-interpreter/06-memory-gc.md) | **不动**(gibbous 帧根经 R5 自动覆盖,§3,零新增) |
| 写屏障 | [06 §9.4](../p1-interpreter/06-memory-gc.md) 空实现 | **不动**(§5.2,仍空) |
| safepoint 哲学 | [05 §5.1](../p1-interpreter/05-interpreter-loop.md) / [06 §7.1](../p1-interpreter/06-memory-gc.md) | **不动**(§0.2,只换布点载体) |

**P3 对内存管理的唯一改动**是 [03](./03-memory-model.md) 的「arena backing 收养 wazero memory」——而那是**物理 backing 来源的替换**(P1 已留 `arena.Options.NewBacking` 注入点,[00-overview §7](./00-overview.md)),**不是内存管理逻辑的改动**(分配/GC/根/屏障逻辑全不变)。

**一句话**:**P3 = 换执行引擎(解释器→wazero)+ 换 backing 来源(Go 堆→wazero memory),其余内存管理一字不改。** safepoint 的三类布点(§1)是「把 P1 的 safepoint 哲学搬到新执行引擎」,不是「新的内存管理」——这是本文与 [06](../p1-interpreter/06-memory-gc.md) 关系的本质:**本文是 [06](../p1-interpreter/06-memory-gc.md) 在编译层的延伸,不是替代。**

---

## 6. GC 压力下的差分验证

本节是「跨层 GC 安全如何被验证」的入口,**详细验收口径转交 [08-testing-strategy](./08-testing-strategy.md)**(本文只声明验什么,08 定怎么验)。

### 6.1 GC 压力模式跑 gibbous

承 [12](../p1-interpreter/12-testing-difftest.md) 的 GC 压力 fuzz 模式 + p3-wasm-tier 原稿 §7(原稿 §7「GC 压力 fuzz 同样上 gibbous」),GC 压力下的 gibbous 差分验证逼出两类缺漏:

| 验证目标 | 逼出什么 | 主线(基线 vs locals 缓存启用) |
|---|---|---|
| **locals 写回遗漏**(§4) | 漏写回导致 GC 误根 / 解释器读脏(§4.4) | **基线下不验**(无 locals 缓存,§3.4 天然正确);**locals 缓存启用后**才是主验目标(§4.5) |
| **助手内根登记缺漏** | gibbous 调助手时,助手内若把 GCRef 暂存 Go 局部未登记 shadow stack(R7),GC 误回收(同 [06 §6](../p1-interpreter/06-memory-gc.md) host fn 盲区) | **基线 + locals 都验**——助手是 P1 opcode 实现,但 gibbous 经它的路径需确认 shadow stack 纪律仍正确 |

### 6.2 验证机制要点(详见 08)

- **每分配即 full GC 模式**([12](../p1-interpreter/12-testing-difftest.md)):把「恰好触发 GC」变成「每个 GC 边界都触发」,把偶发 bug 变必现(§4.5)。
- **crescent vs gibbous 逐字节差分**([08 §2](./08-testing-strategy.md)):同一 Proto 在 crescent 和强制 gibbous 下跑同样的 GC 压力负载,输出逐字节比对——任何根缺漏导致的误回收/读脏都会使输出偏离(或崩溃),差分必炸。
- **强制全升模式**([08 §2](./08-testing-strategy.md)):所有 CompCompilable Proto 强制编译,使所有可能的 gibbous 帧都被 GC 压力覆盖。

**转交声明**:**本节只声明「GC 压力下验 gibbous 的两类缺漏 + 用每分配即 full GC 模式逼出」;V 编号验收项、fuzz 种子策略、断言形式在 [08-testing-strategy](./08-testing-strategy.md) 定稿。** 本文 §3.4.4 / §4.5 已分别给出基线根等价性、locals 写回的验证锚点指向。

---

## 7. 不变式清单(实现与差分须守)

承 ../p3-wasm-tier.md §10 不变式 5(基线 memory-resident + locals 写回纪律),本文聚合 GC 相关不变式:

1. **gibbous 不直接分配,经助手回 Go**(§1.1):所有触发分配的 opcode(NEWTABLE/SETLIST/CLOSURE/CONCAT/字符串 intern/SETTABLE 写新键 rehash/SETGLOBAL rehash)翻译为 imported 助手调用,分配 + GC 都在助手内([06 §8.2](../p1-interpreter/06-memory-gc.md) Alloc 内 collect)。gibbous 直线代码里没有任何 arena 分配指令。

2. **三类 safepoint 不漏:分配点 / 层边界 / 回边**(§1):
   - 分配点(§1.1):助手内 collect,Wasm 侧无感。
   - 层边界(§1.2):trampoline 天然检查点,基线可选(助手路径已覆盖),极端长函数形态 PW9 按实测加。
   - 回边(§1.3):`(if (i32.load $gcPending) (call $h_safepoint))`,一次 i32.load + 几乎恒不跳分支;纯计算循环不触发(§1.4)。

3. **基线 memory-resident 下 GC 根零新增**(§3):gibbous 帧活跃寄存器 = thread 值栈槽,[06 §5.1](../p1-interpreter/06-memory-gc.md) R5(running thread 栈 + CallInfo)原样覆盖,根枚举代码一行不改(§3.2.1)。gibbous 帧与 crescent 帧根贡献完全等价(§3.4:同一 thread 槽数组、同一 CallInfo 链)。

4. **locals 缓存写回纪律由编译器静态保证**(§4):若启用 [02 §2.2B](./02-translation.md) 优化,写回点(W1 helper 前 / W2 回边命中前 / W3 跨层前 / W4 分支到 GC 路径前)全部静态可枚举,编译器静态插入(§4.3 算法)。漏写回 = GC 误根 / 解释器读脏(灾难,§4.4),GC 压力 fuzz 是主防线(§4.5)。**PW9 验收前 locals 缓存暂不启用,只启基线 memory-resident(§4.6)。**

5. **P3 不动写屏障,增量 GC 留 P3+**(§5):P3 的 GC 仍是 P1 STW full GC(§5.1),写屏障维持 [06 §9.4](../p1-interpreter/06-memory-gc.md) 空实现(§5.2),增量 GC(及 gibbous 写表槽逻辑屏障)留 P3 之后单独评估(§5.3)。P3 范围刻意只换执行引擎 + backing 来源,不动内存管理逻辑(§5.4,roadmap §5 原则 3)。

6. **gcPending 检查 = 望舒 GC 的事,与 wazero 异步抢占税正交**(§1.5):回边的 gcPending 检查是望舒自管 GC 的回收时机;wazero 生成码的回边抢占检查点是 Go 调度的事(异步抢占税由 wazero 外包,p3-wasm-tier 原稿 §9)。两者可叠在同一回边,语义不混。

---

## 8. 文档缺口 / 待决(记入 [memory/doc-gaps](../../../llmdoc/memory/doc-gaps.md))

- **对 [06 §12](../p1-interpreter/06-memory-gc.md) 的回填**(§2.4):把 [06 §12](../p1-interpreter/06-memory-gc.md) 的「层边界 safepoint 的具体形态」缺口条目改为收口标记(指向本文 §1)。**本期只记录回填请求,不主动改 P1 06**(承 [00-overview §7](./00-overview.md) 纪律);P3 落地时(PW4 回边 safepoint 实装)同批改。

- **locals 缓存写回算法的精确定义**(§4.3.2):活跃性分析精度、W4 的 CFG 分析深度、helper clobber 集标注、缓存槽选择(FORLOOP 三槽之外是否扩展)——**留 PW5/PW9 实测后定**(承 [02 §2.2B](./02-translation.md) + [00-overview §10](./00-overview.md))。P3 首版不启用 locals 缓存(§4.6),此为前瞻设计。

- **locals 缓存的启用决策**(§4.6):PW9 性能基准达标则无需启用;若某些循环形态卡在 2x 以下且瓶颈在 `i64.load/store`,再评估对 FORLOOP 三槽启用 + 写回纪律 + GC 压力 fuzz 兜底。**留 PW9 实测后定。**

- **助手内手工根登记 vs 编译器自动覆盖**(§1.1.3 / §6.1):助手体(= P1 opcode 实现)内若把 GCRef 暂存 Go 局部(如 CONCAT 中间串),需登记 shadow stack(R7)防误回收——这是 P1 已有的纪律,但 gibbous 经助手的路径需确认覆盖完整。**手工根登记是否需要为 gibbous 路径补充、还是 P1 纪律自然覆盖,留 PW3 实装时定**(PW3 = 算术 + 慢路径助手回 Go,[00-overview §4](./00-overview.md))。

- **`$gcPending` 的 wazero global 暴露 API**(§1.3.4):`module.ExportedGlobal("gcPending").Set(...)` vs 经 imported 函数副作用;global 读成本是否 ≤ linear memory 读(否则退化为 linear memory 约定偏移)——**待 spike 验证**(同 [03 §3](./03-memory-model.md) wazero API 待验证项,[01 §1.4](./01-spike-gate.md) 顺带项)。

- **层边界 safepoint 的极端长函数兜底**(§1.2.3):若 PW9 实测发现「既不分配也无回边的长 gibbous 函数」导致内存增长异常,在 trampoline 进出 gibbous 加 safepoint 检查——**留 PW9 验收时按实测定**。

- **增量 GC 的 gibbous 屏障插桩**(§5.3.1):增量 GC 启用后(P3 之后),gibbous SETTABLE/SETLIST 直达槽快路径需插逻辑屏障(`$h_writebarrier`),编译器据「是否启用增量 GC」开关决定是否插——**留 P3 之后增量 GC 评估时定**。

---

相关:
[00-overview](./00-overview.md)(P3 总览,本文是「跨层 GC」单一事实源) ·
[01-spike-gate](./01-spike-gate.md)(开工闸门,gcPending global API 待 spike) ·
[02-translation](./02-translation.md)(寄存器映射 §2A 基线 / §2.2B locals 缓存 / §3.5 FORLOOP 回边 WAT / §4 pc 物化) ·
[03-memory-model](./03-memory-model.md)(arena=wazero memory:GC 根与寄存器同一块物理内存的物理基础) ·
[04-trampoline](./04-trampoline.md)(层边界 = trampoline / CallInfo bit50 / status 链冒泡) ·
[06-ic-feedback-consume](./06-ic-feedback-consume.md)(IC 快照固化,GETTABLE/SETTABLE 直达槽) ·
[08-testing-strategy](./08-testing-strategy.md)(GC 压力差分验收,本文 §6 转交) ·
../p3-wasm-tier.md(P3 单文件原稿 §6 跨层 safepoint / §10 不变式 5) ·
[../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md)(§5 safepoint 布点原则 + opcode 末尾检查) ·
[../p1-interpreter/06-memory-gc](../p1-interpreter/06-memory-gc.md)(§5.1 GC 根 R1..R9 / §7 safepoint 哲学 / §8.2 Alloc 内 collect / §9.4 写屏障空 / §12 跨层 safepoint 缺口——本文收口对象) ·
[../p1-interpreter/12-testing-difftest](../p1-interpreter/12-testing-difftest.md)(GC 压力 fuzz 模式) ·
[../roadmap.md](../roadmap.md)(§2 四项税异步抢占 / §3 safepoint 限定 / §5 原则 3 每阶段一块硬骨头) ·
[../../../llmdoc/must/design-premises](../../../llmdoc/must/design-premises.md)(前提三:safepoint 限定在分配点与层边界)
