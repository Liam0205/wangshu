# P4 §4:OSR exit 协议——guard 失败的着陆面 + 物化 + 再训练

> 状态:**详细设计**(P4 整体仍是「架构决策深度」,但 deopt 机制是 deopt vs snapshot 的分水岭,值表示承诺的现金兑现处,本文按详细设计深度展开)。本文是 P4 子文档集的「deopt 机制」单一事实源——guard 失败如何回到解释器、物化为什么是 memmove、与 P5 snapshot 的复杂度对照、再训练防 deopt 风暴。
>
> **方案 A 决议(承 [./03-speculation-ic.md](./03-speculation-ic.md) §4.2 + §8)**:**P2 `tierState` 三态枚举不变**(`TierInterp / TierGibbous / TierStuck`,单向无环);**P4 在 `internal/gibbous/jit` 内部维护独立子状态字段 `p4SpecState[proto]`**——枚举 `P4Speculative / P4Deoptimized / P4StuckSpeculation`——叠加在 P2 `TierGibbous` 之上,**P2 不感知**。OSR exit / 重训练 / 拉黑投机全部 P4 自管。本文 §5 的所有「降层 / 重编译 / 拉黑投机」操作都对应 P4 端 `p4SpecState[proto]` 的转移而非 P2 `pd.tierState` 的写入(承 §5.2 / §5.3 + 本文 §12 回填请求已撤回 RJ-8/9/10)。
>
> 上游契约:
> [../../../llmdoc/must/design-premises](../../../llmdoc/must/design-premises.md)(前提四第一天值表示承诺——物化 = memmove 的现金兑现)、
> [../roadmap](../roadmap.md)(§4「deopt 简单(函数级 OSR exit 回解释器)」的展开)。
>
> P3 对位:
> [../p3-wasm-tier/04-trampoline](../p3-wasm-tier/04-trampoline.md)(P3 跨层协议——本文 §8 复用其概念基线;§0.4 P4 继承 P3 跨层协议只换发射后端的承诺;P3 trampoline 永远不返回 status=2,§0.4)、
> [../p3-wasm-tier/05-safepoint-gc](../p3-wasm-tier/05-safepoint-gc.md)(回边 + 边界 safepoint 协议——本文 §6 exit stub 与 safepoint 同位)。
>
> P5 对位(复杂度对照的对偶面):
> [../p5-trace-jit](../p5-trace-jit.md)(§4 snapshot deopt——本文 §3.4 / §9 用它做 P4 简单性的反衬;§4.3 复杂度对照表的 P5 列即此处 P4 列的镜面)。
>
> P1 已落地的料(deopt 协议复用的现成基础设施):
> [../p1-interpreter/01-value-object-model](../p1-interpreter/01-value-object-model.md)(§7 不变式 1「值即 8 字节,跨 tier 拷贝是 memmove」——物化语义的物理基础)、
> [../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md)(§1 CallInfo + Frame 布局、§1.2 word2 bit50 callStatus_gibbous、§1.3 reloadFrame 协议、§7 调用约定 + §7.3 reentry 边界)。
>
> P2 依赖面:
> [../p2-bridge/04-try-compile-fallback](../p2-bridge/04-try-compile-fallback.md)(§7 不重试纪律——本文 §5.4 的「P4StuckSpeculation」与之对位)、
> [../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md)(§6 GibbousCode `Run` 返回 `status=2 DEOPT`——本文 §4 / §6 的 trampoline 出口)、
> [../p2-bridge/01-profiling](../p2-bridge/01-profiling.md)(§5 阈值定标——本文 §5.6 deopt 阈值留 P2 实测校准的对位)。
>
> 章节定位一句话:**本文是 P4 deopt 机制的单一事实源**。guard 失败如何 OSR exit、物化为什么 = memmove、为什么 P4 不需要 snapshot、deopt 后如何再训练防风暴——一次定稳。

对应 Go 包:`internal/gibbous/jit`(exit stub 在 `osr.go`,trampoline DEOPT 出口在 `trampoline_amd64.s` / `trampoline_arm64.s`;详见 [./05-system-pipeline.md](./05-system-pipeline.md) / [./06-backends.md](./06-backends.md))。

---

## 0. 定位:OSR exit 在 P4 中的位置

### 0.1 OSR exit 是 guard 失败的唯一退路

P4 是望舒第一个**有运行期假设可能被打破**的层(承 [./03-speculation-ic.md](./03-speculation-ic.md) §4 状态机:P2/P3 零 deopt,P4 「有意打破」)。**guard 失败的唯一退路是 OSR exit**——交还解释器,而不是 trampoline 内重试、不是 trace 式 side trace、不是 V8 Maglev 式编译码内补救。

> **核心断言**:在 P4,**所有 guard 失败的处理路径都收敛到「函数级 OSR exit + crescent 续跑」**。其余形态(side exit 跑另一段编译码、就地补救成通用模板继续跑)都不在 P4 的设计空间。

为什么不允许其它形态:

- **side exit 跑另一段编译码**(LuaJIT / TurboFan side trace 式):需要发射多份编译产物 + 编译期/运行期管理 side exit 跳转表,且 side trace 自身仍可能 deopt——递归复杂度迅速膨胀。这是 trace JIT 的必然形态(承 [../p5-trace-jit](../p5-trace-jit.md) §2 流水线图 ④/⑤),不是模板编译该背的复杂度。
- **就地补救为通用模板**:理论上「投机失败 → 同函数内回退到无投机的通用模板继续跑」可保留 dispatch 收益,但需要每条投机指令旁带一份通用模板入口 + 编译期生成「跨指令切换路径」的 stitching 代码,把 P4 的「不优化跨指令」简单性破坏(承 [./02-template-direction.md](./02-template-direction.md) §1.3 简单性向下传导)。**P4 的策略是退到解释器跑完本帧、再训练后整 Proto 重编译**(§5),把「补救」从单帧内的微观操作上移成 Proto 级的宏观状态转移。

### 0.2 与 P3 04-trampoline 的对位:P3 status 链 vs P4 OSR exit

[../p3-wasm-tier/04-trampoline](../p3-wasm-tier/04-trampoline.md) 已立稳跨层互调协议:升层入口 `(base i32) → status i32`、status 链冒泡错误、helper 三向分派。**P4 继承全部这套协议**(承 [../p3-wasm-tier/04-trampoline](../p3-wasm-tier/04-trampoline.md) §0.4「P4 继承本文跨层协议,只换发射后端」)。但「status 链」与「OSR exit」是**两个不同的事件**:

| 维度 | P3 status 链(错误冒泡) | P4 OSR exit(deopt) |
|---|---|---|
| 触发原因 | gibbous 内出错(对 nil 算术、table index nil/NaN、helper 设 pendingErr) | 投机 guard 失败(类型假设破裂——非语义错) |
| 出口 status | **status=1 ERR**:`state.pendingErr` 已置,走 P1 §9 错误冒泡 | **status=2 DEOPT**:无 pendingErr,走本文 §3.3 OSR 着陆 |
| 退到哪 | crescent 主循环 throwPending 一路 return 出 protected 边界(pcall) | crescent reloadFrame 从 exitPC 续跑同一帧剩余字节码 |
| 是否错误 | 是,语义错(脚本逻辑出错) | 否,投机失误(等价跑了通用模板,语义正确) |
| 调用链上层影响 | 上层帧若不在 protected 边界,继续冒泡 | **上层帧不受影响**(各自维持 tier,本文 §1.3) |
| 频次 | 真实脚本极少出错(分支预测器轻松命中) | 投机假设失误次数取决于 confidence;频繁则触发 §5 再训练 |

[../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md) §6.1 已为这一区分预留 status 编码:**P3 trampoline 永远不返回 2,P4 trampoline 才返回 2**。这是「P3 零 deopt vs P4 有意打破」状态机差(承 [./03-speculation-ic.md](./03-speculation-ic.md) §4)的物理兑现点。

> **形态对位一句话**:**P3 是出 wazero 进 Go,P4 是出 JIT 进解释器**——跨层协议形态对位,但 P4 多了一条「DEOPT 出口 + crescent 续跑同帧」的语义,这是本文的核心增量。

### 0.3 与 P5 snapshot deopt 的复杂度差(预告)

[../p5-trace-jit](../p5-trace-jit.md) §4 把 snapshot+deopt 机器称为「LuaJIT 真正的护城河,无处抄」,展开为「+2-4 人年开放式」的主成分。P4 的 deopt 之所以能薄到几乎没有,本质上是**「不优化跨指令」换来的简化**——值留在栈槽、单帧 exit、静态 exit 序列——使 snapshot 这台机器在 P4 整个不必存在。详细对照见 §3.4 与 §9.1 表;此处先点名:**P4 简单性的物理来源就是「不引入会拆毁栈槽真相不变式的优化」**。

### 0.4 章节路标

| 章节 | 主题 | 关键产物 |
|---|---|---|
| §1 | 函数级 exit 的语义 | exit 单位 = 当前帧;不跨帧、不编译码内恢复 |
| §2 | 物化 = memmove | 第一天值表示承诺的现金兑现 |
| §3 | 「栈槽真相」不变式 | snapshot 不必存在的物理来源 + osrExit 三步伪码 |
| §4 | exitPC 与字节码地址映射 | 编译期一次性产出,exit 时回填 CallInfo.savedPC |
| §5 | 再训练与防 deopt 风暴 | P4StuckSpeculation;承 P2 不重试纪律 |
| §6 | exit stub 的物理形态 | 寄存器写回 + 写 exitPC + 经 trampoline 出 |
| §7 | exit 之后的 crescent 接管 | reloadFrame + bit50 后续语义 |
| §8 | P4 OSR 接 P3 跨层协议 | status=2 DEOPT 出口扩展 |
| §9 | snapshot 复杂度对照 | P4 vs P5 五行对照表 |
| §10 | 不变式清单 | 五条聚合 |
| §11 | 风险与开放问题 | 着陆粒度终稿 / 阈值校准 / 线程模型 |
| §12 | 回填请求 | 方案 A 下仅保留 P1 05 / 跨层契约相关项(P2 04 / P2 01 加枚举类已撤回) |

---

## 1. 函数级 exit 的语义

本节定**「exit 是什么、不是什么」**——把 OSR exit 的语义展开为可施工的边界。

### 1.1 Exit 的单位 = 当前函数帧

**核心断言**:**OSR exit 的单位是当前函数帧——guard 失败时,该帧被整体放弃 JIT 执行,剩余字节码全部交还 crescent 解释器**。

物理含义:

- **不是单条指令**:不是「这条 ADD 投机失败 → 回退到通用模板的同一条 ADD 继续跑」(§0.1 已否决就地补救)。
- **不是函数调用链**:不是「这帧投机失败 → 调用者帧也一起退」(§1.3 论证上层帧不受影响)。
- **是当前帧从 exit 点起的剩余字节码**:从 exit 对应的字节码 pc 起,该帧由 crescent 续跑到自然 RETURN(或在续跑过程中再次进入新帧——新帧自有自己的 tier 决策)。

> **「函数」二字精确含义**:Lua 的「函数」即一个 Proto 的一次激活帧——`p4SpecState[proto] == P4Speculative`(承方案 A,P4 内部子状态;P2 视角看仍是 `pd.tierState == TierGibbous`)是 Proto 级状态,但 OSR exit 是帧级事件。同一个升 P4 的 Proto 可能有多个并发帧在调用栈上(递归 / 多 State),其中一个帧 exit 不影响其余帧——它们各自跑各自的编译码,直到自己的 guard 失败或自然 RETURN。

### 1.2 该帧剩余执行整体交还解释器

**Exit 后的执行模型**(承 [../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §1.3 reloadFrame 协议):

```
exit 点的物理状态(guard 失败瞬间):
  arena 值栈槽:本帧所有活值已物化(§3.2 栈槽真相不变式)
  CallInfo:    本帧 CallInfo 已存在(进入 P4 帧时压的,与 enterLuaFrame 同款)
  savedPC:     由 exit stub 回填为 exitPC(§4.2)

trampoline 出 JIT 后:
  状态 = (CallInfo, savedPC, base, proto, cl, valueStack)
       = 解释器要 reloadFrame 续跑所需的全部信息

crescent 主循环接管:
  reloadFrame(f) → f.pc = ci.savedPC = exitPC
  从 exitPC 起继续 fetch-decode-execute,直到本帧 RETURN
```

**关键:这与「一路解释跑同一脚本会有的状态」逐字节一致**——exit 对解释器是不可分辨的(承 [./08-testing-strategy.md](./08-testing-strategy.md) §7.2 差分主防线)。差分接入测试见 [./08-testing-strategy](./08-testing-strategy.md) §7.2(本文不展开)。

### 1.3 调用链上层帧不受影响(各自维持 tier)

**纪律**:exit 只影响当前帧——**调用链上层帧的 tier 状态、CallInfo 与值栈完全不动**。

```
exit 前调用栈(假设三层):
  CallInfo[N-2]: chunk 帧(crescent,bit50=0)            ← 顶层 chunk
  CallInfo[N-1]: outer 帧(P4 编译码,bit50=1,jit 标识)  ← 中间 outer 也升了 P4
  CallInfo[N]:   inner 帧(P4 编译码,bit50=1,jit 标识)  ← 当前帧 inner 投机失败

exit 后调用栈:
  CallInfo[N-2]: chunk 帧(crescent,bit50=0)            ← 不变
  CallInfo[N-1]: outer 帧(P4 编译码,bit50=1,jit 标识)  ← 不变,outer 还在跑 P4
  CallInfo[N]:   inner 帧(crescent 接管,从 exitPC 续跑) ← bit50 状态见 §7.2
```

要点:

- **outer 帧不退到解释器**:outer 仍在 wazero/JIT 世界跑,inner 的 deopt 不传播。等 inner 自然 RETURN(从 crescent 跑完剩余字节码后),outer 收到返回值继续跑自己的 P4 编译码(就像 outer 调了一个 host fn / crescent 帧返回一样,outer 视角无可观察差异)。
- **chunk 帧的 tier 也不变**:exit 不沿调用链向上传播——只有当前帧改变执行引擎,其它帧维持原引擎。
- **物理可行性来源**:CallInfo 是 arena 唯一真相(承 [../p3-wasm-tier/04-trampoline](../p3-wasm-tier/04-trampoline.md) §0.1),outer/chunk 帧的 CallInfo 都在 arena 上,inner 帧 exit 只动 inner 的 CallInfo.savedPC——外层帧的 CallInfo 在物理上没有任何字段被本次 exit 写入。

### 1.4 不做「编译码内恢复后继续跑编译码」

**否决项**:trace JIT 风格的「side trace」——guard 失败后跑另一段为 exit 路径专门编译的代码,直到与主 trace 重新汇合。

否决理由(承 [./02-template-direction.md](./02-template-direction.md) §1.3):

- **side trace 是 trace JIT 的本质**:trace 形态把「函数」抽象成「实际走过的路径」,所以 deopt 自然是「换路径继续跑」。method JIT 的 「函数」抽象不支持这种语义——一个函数的所有可能路径都已编译,投机失败的路径要么有通用模板兜底(就地补救,§0.1 否决),要么退回解释器跑(本文方案)。
- **侧道编译码会再次 deopt**:LuaJIT 的 side trace 本身仍可能 guard 失败,衍生出 side-of-side trace 的 NYI 复杂度——P4 不引入这条递归。
- **结构性简化向下传导**:不做 side trace ⇒ exit 只有一种形态(回解释器);exit 形态唯一 ⇒ trampoline 的 DEOPT 出口只需一个(`status=2`,§8.3);trampoline 出口少 ⇒ 系统管线复杂度低([./05-system-pipeline](./05-system-pipeline.md))。

### 1.5 不做跨帧恢复

**否决项**:guard 失败时,顺手把调用链上**多个**帧一起 deopt(类似 P5 trace 内联导致 deopt 时要重建 N 帧)。

否决理由:

- **P4 没有跨帧编译**:P4 的编译单元是「单 Proto」(承 [./02-template-direction.md](./02-template-direction.md) §4 边界表「不做跨函数内联」),所以一帧的 exit 永远只涉及一帧的状态——无需重建多帧。
- **跨帧 deopt 是 trace 内联的产物**:P5 因为「一条 trace 跨多帧内联」,deopt 时必须凭空重建从未物理存在的内联帧 CallInfo(承 [../p5-trace-jit](../p5-trace-jit.md) §4.1 / §4.2 frames[] 重建)。P4 没有内联,每帧都有真实 CallInfo,exit 不需要补建任何 CallInfo。
- **「单向、当前帧、整体放弃」三个性质合在一起**让 P4 的 deopt 态空间退化到「最简形态」——其它复杂度都从这三个 NO 上消解。

### 1.6 与 P3 错误冒泡 status 链的对位:语义不同

承 §0.2 的对位表,具体场景对照:

```
场景 A:P3 错误冒泡(status=1 ERR)
  Lua:  对 nil 算术(脚本逻辑错)→ helper 设 pendingErr → return 1
  trampoline:throwPending → 错误冒泡到 protected 边界

场景 B:P4 OSR exit(status=2 DEOPT)
  Lua:  IC 记 a/b 恒 number → 升 P4 → 实际跑时见非 number(投机假设破裂)
  P4:   guard 失败 → exit stub 写 exitPC + 设 status=2 → 经 trampoline 出
  doCall:收 status=2 → reloadFrame + 从 exitPC 续跑(crescent 走 arith 慢路径)
        若慢路径仍失败(如字符串强转失败),由 crescent 抛错——这才是「错误」
```

要点(对位 §0.2 表的「是否错误」行):

- **场景 A**:gibbous 内已构造 `*LuaError`,trampoline 是「错误传出来」的载体——错误事件发生在 gibbous 内。
- **场景 B**:P4 编译码内**没有错误**——只是「不能继续跑投机模板」,需要交回解释器走完整语义。错误若发生,由 crescent 抛(§7.4)。
- **OSR exit 路径不应触发 pendingErr 设置**(§7.4):exit 时 `state.pendingErr == nil`;若 deopt 与错误罕见同发,错误优先。

---

## 2. 物化 = memmove:第一天值表示承诺的现金

本节把「物化 = memmove」从一句话延展成对前提四的现金兑付论证。

### 2.1 物化的语义:把「机器状态」变回「解释器状态」

**物化(materialization)**:OSR exit 把 JIT 编译码的「机器状态」变成 crescent 解释器能直接续跑的「解释器状态」的过程。

物化的输入(机器状态):

- 机器寄存器(rax/rbx/r10/... amd64;x0/x1/... arm64)中可能持有的暂存值
- 机器栈(自管 `[]byte` 上的 spill 槽)中可能溢出的暂存值
- exit 时的机器 PC(进入 exit stub 那一刻的代码地址)

物化的输出(解释器状态,承 [../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §1):

- arena 值栈槽:`th.valueStack[base..base+MaxStack)`,所有活值就位
- CallInfo:本帧 CallInfo,字段就位(savedPC = exitPC,§4.2)
- pc:解释器主循环 reload 时取 CallInfo.savedPC

### 2.2 解释器状态 = arena 值栈槽 + CallInfo + pc

[../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §1 已立稳解释器状态形式:

| 状态字段 | 物理位置 | exit 时由谁设置 |
|---|---|---|
| arena 值栈槽 (`R(0..MaxStack)`) | `thread.valueStack` 段(arena 内) | exit stub 的「寄存器→栈槽写回」(§6.2) |
| CallInfo | `thread.callInfoRef` 数组(arena 内) | 进入 P4 帧时压(与 enterLuaFrame 同款),exit 不动 CallInfo 数组本身 |
| `CallInfo.savedPC` | CallInfo word1 [31:0] | exit stub 写 exitPC(§4.2) |
| `CallInfo.top` | CallInfo word1 [63:32] | 大多数 opcode 边界 top 已就位;exit 在 opcode 边界 ⇒ top 不需要额外更新 |
| `Frame.stk / k / ic`(主循环局部缓存) | Go 侧栈(execute 局部) | 由 reloadFrame 重建(§7.1) |

**关键:除了「寄存器→栈槽写回」与「写 savedPC」两步,其余字段在 exit 时不需要任何额外动作**——它们要么本就在 arena(CallInfo 字段、值栈 base/proto/cl/nresults),要么在 exit 后由解释器主循环 reload 时重建(Frame 缓存)。

### 2.3 P4 生成码与解释器同 NaN-box 编码 → 没有格式转换

**核心物理事实(承 [../p1-interpreter/01](../p1-interpreter/01-value-object-model.md) §7 不变式 1)**:

> 任何 Lua 值都是单一 `uint64`(NaN-box 编码),栈/寄存器/表槽/upvalue 同构 —— 跨 tier 拷贝是 memmove。

P4 生成码读写值的形态:

- **寄存器持值**:机器寄存器 rax/rbx/... 持的就是 NaN-box `uint64`(整数寄存器);算术快路径里 xmm0/xmm1 持 f64,但快路径出口写回栈槽前已 NaN 规范化(承 [./03-speculation-ic](./03-speculation-ic.md) 算术快路径出口纪律)。
- **栈槽读写**:`mov rax, [rsi + 8*reg]`(amd64;rsi=base 指针,reg=寄存器号)读出 NaN-box `uint64`;`mov [rsi + 8*reg], rax` 写回——**位 by 位、字节对字节**与解释器 `f.stk[f.base+a]` 读写的是同一个 8 字节单元。
- **常量加载**:NaN-box 常量编译期烧成 `mov rax, imm64` 的立即数(浮点常量经 NaN 规范化后烧入)。

**没有格式转换**——这意味着 exit 时的物化是**纯 memmove**:寄存器持的 `uint64` 直接 `mov [栈槽], reg` 即完成。无需任何「机器表示 → Lua 表示」的解码。

### 2.4 物化操作 = 把暂存寄存器 NaN-box u64 写回栈槽

**精确物化序列(单条 exit 点的固定操作)**:

```
针对该 exit 点缓存的每个寄存器值 (regHW, regLua):
   mov [base_ptr + 8*regLua], regHW   ; NaN-box u64 写回栈槽
针对该 exit 点 spill 到机器栈的暂存值(若有):
   mov rax, [machineStack + spillSlot]
   mov [base_ptr + 8*regLua], rax
```

每一条物化指令是**单条 store**,O(被缓存的活值数)。**不是 LuaJIT/V8 那种「解析 snapshot 配方按记录重建」**——是编译期已知的固定指令序列(§3.6)。

### 2.5 设若 P1 选了 Go tagged struct:物化复杂度上升一个量级

**反事实推演**(承 [../../../llmdoc/must/design-premises](../../../llmdoc/must/design-premises.md) 前提四「为什么不可逆」):假如 P1 当年选 Go `interface{}` 装箱或 tagged struct(`type Value struct { tag int8; data uint64; ref *Object }`),P4 物化将变成:寄存器表示从 u64 变成多寄存器(tag + data + ref);物化操作从单条 store 变成多 store + 可能调 helper 分配 Go 堆对象;栈槽写需写屏障(指向 Go 堆);exit stub 大小从几条 mov 变成数十条混合操作;调试与差分上,任一字段错都是静默错果候选。

**这就是前提四不可逆的具体含义**——P1 第一天选 NaN-box,P4 物化是 memmove;若选 tagged struct,P4 物化代码量与 bug 面是十倍量级,且每个 deopt exit 点都是潜在静默错果源(承 [./08-testing-strategy.md](./08-testing-strategy.md) §7.2「P4 是望舒第一个会说谎的层」)。

### 2.6 这是 design-premises 前提四「值表示一次定死」在 P4 兑付的现金

**升格为不变式**:**P4 OSR exit 的物化 = NaN-box u64 store——这是值表示第一天承诺在 P4 阶段的现金兑付**。

它不是「P4 设计得好」——是 P1 第一天选 NaN-box + 自管 arena 的连锁红利在 P4 阶段第一次正式入账:

- P1:解释器值表示选 NaN-box([../p1-interpreter/01](../p1-interpreter/01-value-object-model.md) §3)。
- P3:wasm 编译层共享 arena 与 NaN-box 编码,跨层只传 base i32([../p3-wasm-tier/03-memory-model](../p3-wasm-tier/03-memory-model.md))。
- **P4:OSR 物化 = memmove**(本文)。
- P5:trace JIT 继承,但 snapshot 重建仍是 NaN-box u64 → 栈槽,基础位编码同款([../p5-trace-jit](../p5-trace-jit.md) §4.2 物化部分)。

> **承的对偶论点**:P4 物化简单是「P1 值表示选对了 + P4 不优化跨指令」**两条**共同贡献的——前者保证「同编码无转换」,后者保证「值在栈槽真相点」。少了任一条,物化都不会这么薄。本文 §3 的栈槽真相不变式与本节合并构成这个完整论证。

---

## 3. 「栈槽真相」不变式:模板编译让 snapshot 不必存在

本节把「栈槽真相」从一句话升格为 P4 不变式,并给出 osrExit 的三步伪码。

### 3.1 不变式正式陈述

**P4 不变式 1(栈槽真相,松弛版基线)**:

> **guard 边界处**,本帧全部 Lua 活值在 arena 值栈槽 ∪ 寄存器写回序列(编译期静态可达)中——guard 失败时物化集合 = 编译期已知的「该 guard 对应的寄存器→栈槽写回脚本」(无运行期解析);严格栈槽真相只在「不启用 §3.6 局部缓存」的模板段成立,启用局部缓存的模板段(如 FORLOOP 循环变量驻留)收窄到「guard 处真相点」。

形式化(用「字节码边界」「guard 边界」与「活值集」三个概念):

- **字节码边界**:连续两条字节码指令之间的时间点(模板 N 结束、模板 N+1 开始之间)。
- **活值集 `Live(pc)`**:在 pc 对应的字节码边界处,后续执行可能读到的 Lua 寄存器集(由 codegen liveness 分析得出,[../p1-interpreter/02](../p1-interpreter/02-bytecode-isa.md) §6 寄存器使用约定)。
- **不变式**:对所有字节码边界 pc,`∀reg ∈ Live(pc), arena.valueStack[base + reg] 持有该寄存器在 pc 时的合法 Lua 值`。

谁来兑现这个不变式:**模板编译器**——每条字节码模板的最后一步必须把它修改的输出寄存器 store 回栈槽([./02-template-direction](./02-template-direction.md) §3「不优化跨指令」直接保证)。具体形态(**伪汇编中 `rsi`/`rbx` 等寄存器仅作示例占位,具体寄存器约定见 [05 §3.3 / 06 §4.1](./05-system-pipeline.md)**):

```
ADD A B C 模板(amd64,概念形态):
  ; load 操作数
  mov rax, [rsi + 8*B]      ; rax = R(B) NaN-box
  mov rbx, [rsi + 8*C]      ; rbx = R(C) NaN-box
  ; guard 双 number([./03-speculation-ic] §2):IsNumber ≡ u64 < NAN_THRESHOLD,
  ; 失败 = >= NAN_THRESHOLD,跳出走 jae(高位 tag 命中即出 number 域,与 03 §3.4 一致)
  cmp rax, NAN_THRESHOLD
  jae exit_stub_42          ; >= 阈值 ⇒ 不是 number ⇒ guard 失败跳出
  cmp rbx, NAN_THRESHOLD
  jae exit_stub_42
  ; f64 fast path
  movq xmm0, rax            ; reinterpret 为 f64(IsNumber 已确保)
  movq xmm1, rbx
  addsd xmm0, xmm1
  ; ★ 出口:NaN 规范化后 store 回 R(A) 栈槽
  ;   (NaN 规范化:若结果是 NaN,替换为 NaN_CANON,[../p1-interpreter/01] §3.4)
  call canonicalize_nan      ; 或 inline 的几条比较+cmov
  movq [rsi + 8*A], xmm0     ; ★ store 回栈槽——这是不变式 1 的兑现点
  ; 跳下一条模板
  jmp next_template
```

**关键**:模板出口必须 store 回栈槽——否则下一条模板可能 load 同一寄存器,会读到旧值。**这条「store 必须发生」纪律是栈槽真相不变式的物理来源**。

### 3.2 推论:guard 失败瞬间,栈槽即完整解释器状态

**推论(栈槽真相 → 物化集合空)**:

如果 guard 布在模板**开头**(操作数检查先于任何副作用——承 [./03-speculation-ic](./03-speculation-ic.md) §2 guard 布点纪律),那么 guard 失败瞬间:

1. **本帧所有活值仍在栈槽**(不变式 1):上一条模板的 store 已完成,本条模板尚未开始 store。
2. **本条模板的输出未写入**:guard 在 store 之前,所以 R(A) 仍是 ADD 之前的旧值——这正是「一路解释跑」在该 pc 边界看到的栈状态(R(A) 还没被 ADD 这条指令更新)。
3. **机器寄存器持的是「即将被使用但尚未生效」的暂存值**:rax/rbx 里有 `R(B)/R(C)` 的副本——但这些副本与栈槽里的值**位 by 位相同**(刚 load 自栈槽,§3.1 模板形态),所以**这些寄存器即使丢弃也无信息丢失**。

**结论**:guard 失败 → **待物化集合 = 空**。物化在最常见情形下退化为零操作(承 §3.6 实现自由度)。

### 3.3 OSR exit 退化为三步

**osrExit 的三步伪码**(承本文 §3.1 栈槽真相不变式):

```
osrExit(exitPC):
  // step 1: 写 exitPC 到本帧 CallInfo.savedPC
  //   exitPC 是编译期映射(§4)查得的「该 guard 对应的字节码 pc」
  //   ★ 直接 store 立即数到 CallInfo arena 偏移,几条 mov,O(1)
  ci.savedPC = exitPC

  // step 2: 记 deopt 计数到 ProfileData(§5 再训练用)
  //   atomic increment(多 State 共享 ProfileData,[../p2-bridge/01-profiling] §5.1)
  //   ★ 多数情况下也是 O(1) 单条 atomic add
  atomic_add(&proto.profileData.deoptCount, 1)

  // step 3: 经 trampoline 退出 JIT 世界(§6.2 / §8)
  //   设 status=2(DEOPT,[../p2-bridge/05] §6.1)
  //   解释器主循环 reloadFrame(§7.1)后从 exitPC 续跑
  return status=2
```

要点:

- **三步全是 O(1)**:写一个字段 + atomic add + 设置 status——总计十几条机器指令,远少于 P5 snapshot 重建的几百条(§9.1 复杂度对照)。
- **不写值栈、不重建任何状态**:因为「待物化集合为空」(§3.2)——这是 P4 简单性的核心。
- **不同帧的 exit 都共享这三步骨架**:exit stub 的差异只在「step 1 的 exitPC 立即数不同」(§6.3 stub 复用)。

### 3.4 与 P5 trace JIT 对照

承 [../p5-trace-jit](../p5-trace-jit.md) §4.1——P5 因三项核心优化拆毁栈槽真相不变式,deopt 必须靠 snapshot 重建多帧真相:

| 拆毁来源 | P4 是否引入 | 后果 |
|---|---|---|
| 寄存器分配(活值在机器寄存器/spill,不在栈槽) | **不引入**——P4 不做跨指令 regalloc(承 [./02-template-direction.md](./02-template-direction.md) §4) | exit 时活值已在栈槽,无需「寄存器→栈槽」重建 |
| trace 内联(一条 trace 跨多个逻辑 Lua 帧) | **不引入**——P4 编译单元是单 Proto(§1.5) | exit 涉及单帧,无需「frames[] 重建被内联帧的 CallInfo」 |
| 分配下沉(对象字段散在 IR 值,需 unsink 重建) | **不引入**——P4 不做 sinking(承 [./02-template-direction.md](./02-template-direction.md) §4 边界表) | exit 时所有对象都已真实分配在 arena,无需「unsink 重建对象」 |

**P4 用「不引入这三项」换掉了整台 snapshot 机器**(§9.1 表)。代价是 P4 生成码保留栈槽内存往返(每条模板 load + store)——这是 P5 优化的猎物,也是 P4 验收时定的「拿 trace 收益的 ~70%」位置(承 [./00-overview.md](./00-overview.md) §0.1 流水线图)。

### 3.5 这是「不优化跨指令换掉整台 snapshot 机器」的具体兑现

**承 [./02-template-direction.md](./02-template-direction.md) §1.3「简单性向下传导」论证**:

```
                        ┌─ 不引入 regalloc ─┐
模板编译每条指令独立  ──┤  不引入 inline    ├──► 栈槽真相不变式  ──► snapshot 不必存在
                        └─ 不引入 sinking   ┘
                                                                    └──► OSR exit 三步 O(1)
                                                                    └──► 物化 = memmove
                                                                    └──► exit stub 编译期静态生成
```

**这是设计上的复利**:在 method 层做「不优化跨指令」的选择,把 deopt 复杂度也一起去掉了。如果项目想在 P4 之上「再加点小优化」(比如循环不变量提升、跨指令寄存器分配),会同时把 deopt 机器从「O(1) 静态序列」推向「snapshot 机器」——边际收益小但复杂度阶跃。**这是 P4 不演化为「mini DFG」的工程理由**:边界一旦松,deopt 复杂度一阶跃,简单性红利全失。

### 3.6 实现自由度:某些模板边界间短暂缓存值

**前瞻**:某些模板为性能在边界间短暂缓存值(典型如 FORLOOP 的循环变量驻留寄存器,见 [./06-backends](./06-backends.md) §5)。这种局部缓存破坏「严格栈槽真相」——某些 pc 边界处,某些活值短暂只存在于寄存器,栈槽是旧值。

**解决方式**:相应 guard 的 exit 序列**补「寄存器→栈槽」写回**——每 exit 点编译期生成,固定几条 store,运行期无解析。例如 FORLOOP 内若循环变量 i 驻留 r12,则该循环体内任意 guard 的 exit stub 多一条 `mov [rsi + 8*loopVar], r12` 写回。

要点:

- **写回序列编译期静态生成**:每个 exit 点的写回序列在该模板编译时就确定(哪些寄存器要写回到哪些栈槽),写成 stub 内的固定 store 指令。**没有运行期映射查询**——与 P5 snapshot 的「运行期解析配方」形成对照(§9.1)。
- **「真相点」从「处处」收窄到「guard 处」**:严格栈槽真相是「处处真相点」;允许局部缓存后是「guard 处真相点」。后者仍足够——OSR exit 只在 guard 处发生,只需 guard 处栈槽是完整真相。
- **这条优化的 ROI 由 [./06-backends](./06-backends.md) §5 实测裁决**:省一次内存往返 vs 多几条 exit stub 写回的成本。本文不裁决,只定「若启用,exit 序列编译期静态生成」的纪律(§3.7)。

### 3.7 架构决策:exit 物化序列必须编译期静态生成

**升格为 P4 不变式**:

> **OSR exit 的物化序列必须编译期静态生成,杜绝运行期映射查询、杜绝运行期分配**。

边界:

- **允许**:每个 exit 点编译期烧入「该 exit 要写回哪几个寄存器到哪几个栈槽」的固定 store 序列(§3.6)。
- **禁止**:任何「运行期查 snapshot 配方 → 解码 → 物化」形态(那是 P5 的事,§9.1)。
- **禁止**:exit 路径上做任何分配(可能触发 GC、违反 [../p3-wasm-tier/05-safepoint-gc](../p3-wasm-tier/05-safepoint-gc.md) 的「分配只在受控点」哲学)。

**这条不变式守住的是 P4 deopt 简单性的工程边界**——任何把 exit 复杂化的提案(运行期映射 / 动态查表 / exit 内分配)都直接判否。

---

## 4. exitPC 与字节码地址映射

### 4.1 编译期记 (机器地址 → 字节码 pc) 映射

**P4 编译时唯一一次产出**:线性扫字节码生成机器码时,每发射一条字节码模板,记下「该模板起始机器地址 → 该模板对应的字节码 pc」对。结构:

```go
// internal/gibbous/jit —— P4 编译产物的 exitPC 映射
type p4Code struct {
    codeSeg    []byte                // 可执行段(承 [../p2-bridge/05] §6.3 p4Code)
    entry      uintptr                // 入口 trampoline stub
    osrExits   []osrExitInfo          // ★ exit 点 → exitPC 映射
    proto      *bytecode.Proto
}

// 每个 guard exit 点一条记录(编译期静态填充)
type osrExitInfo struct {
    stubStartAddr uintptr   // 该 guard 对应的 exit stub 起始地址(机器地址)
    exitPC        int32     // 该 guard 失败时回填给 CallInfo.savedPC 的字节码 pc
    // 寄存器写回序列已烧入 stub 自身(§6.2),此结构无需记
}
```

要点:

- **映射数据极少**:每个 guard 一条记录(2 字段,16 字节量级)——与 P5 snapshot 的「每 guard 一份稀疏映射 + frames[] + slots{}」形成对照(§9.1)。
- **静态烧入,无需运行期管理**:`osrExits` 数组在编译时填好后只读,不随运行期 deopt 事件变化。
- **可选优化:不存映射,exitPC 直接编入 stub**:更简形态——每个 stub 的「写 exitPC」步骤就是 `mov [ci_savedPC_offset], <立即数>`,立即数本身就是 exitPC,无需查表。这是 §6.2 的实装基线;`osrExits` 数组主要用于诊断与统计(知道哪些 stub 触发了 deopt)。

### 4.2 exit 时按映射回填 CallInfo.savedPC

承 §3.3 的 osrExit step 1:

```
exit stub 内(amd64 概念):
  mov rax, exitPC_immediate         ; 编译期立即数
  mov [ci_ptr + savedPC_offset], rax  ; 写回 CallInfo.savedPC
  ; ★ 这一步使「机器世界 vs 解释器世界」的 PC 概念对齐——机器 PC 已无意义,
  ;   解释器世界的 pc 由 savedPC 持有,后续 reloadFrame 用它续跑(§7.1)。
```

为什么不在每条字节码模板入口都同步 pc 到 CallInfo:

- **运行期同步 pc 是浪费**:正常路径下从不读 CallInfo.savedPC(只在 host call / yield / exit / traceback 时读),每条模板入口都写一次纯属浪费指令。
- **只在「需要 pc」的点物化**:与 P3 的 pc 物化策略([../p3-wasm-tier/02-translation](../p3-wasm-tier/02-translation.md) §4)同款——helper 调用入口、guard exit 点、回边 safepoint 这些「需要让外部世界看到当前 pc」的点才把编译期已知 pc 写入 CallInfo。

### 4.3 与 P3 02-translation §4.2 pc 物化的对位

**P3 与 P4 pc 物化的形态对位**:

| 维度 | P3([../p3-wasm-tier/02-translation](../p3-wasm-tier/02-translation.md) §4) | P4(本文) |
|---|---|---|
| pc 物化时机 | 调 helper 时(`call $h_call(base, pc)`) | 调 helper 时(同 P3,本文 §6.4 helper 协议同) + **guard exit 点(本文增量)** |
| pc 物化方式 | 把编译期已知 pc 作为 helper 的 i32 立即数参数,helper 入口写 `ci.savedPC = pc` | 把编译期已知 pc 作为 stub 内的 mov 立即数,直接写 `ci.savedPC = exitPC_imm` |
| 直线代码维护 pc | 不维护(承 [../p3-wasm-tier/02-translation](../p3-wasm-tier/02-translation.md) §4) | 不维护(承本文 §4.2) |
| 同型不变式 | savedPC 在「外部世界需要 pc」的所有点都正确 | 同左,exit 也是「外部世界需要 pc」之一 |

**关键:P4 对 P3 增量是「guard exit 点也算外部世界需要 pc 的时机」**——P3 没有 guard,P4 有。其余 pc 物化时机(helper / safepoint / yield)P4 与 P3 一致。

### 4.4 traceback 接入:exit 后由 crescent 续跑,traceback 经 CallInfo.savedPC 与解释器同型

**纪律(承 [../p3-wasm-tier/04-trampoline](../p3-wasm-tier/04-trampoline.md) §4.5 + [./08-testing-strategy](./08-testing-strategy.md) §2)**:

- exit 完成后,本帧 CallInfo.savedPC = exitPC——即「出 guard 那条字节码的 pc」。
- 解释器从 exitPC 续跑,期间任何错误产生的 traceback,都按 CallInfo 链遍历,本帧的 savedPC 与「一路解释跑同帧」是一样的(都指向当前指令)。
- **差分逐字节一致由此自然成立**:exit 后的执行路径与「一路解释」共享同一段解释器主循环代码,traceback 输出同型。

> **「差分主防线见 [./08-testing-strategy](./08-testing-strategy.md)」**:具体差分 fuzz 形态、deopt 注入测试、exit 状态等价专项测试的实装由 08 承担,本文不展开。本文只点名:exit 后续跑的逐字节一致是 P4 投机错误防线的关键依赖,因此 §3.7 的「exit 物化序列编译期静态生成」纪律不能松——任何运行期映射都引入 codegen-time 与 exit-time 的解释偏差,直接威胁差分主防线。

---

## 5. exit 后:再训练与防 deopt 风暴

本节把「事件 → 动作」三条延展为完整的状态机变迁、阈值与不重试纪律。

### 5.1 单次 guard 失败 → OSR exit + 计数 + IC 更新

**单次失败的三件事**:

| # | 动作 | 物理位置 |
|---|---|---|
| 1 | OSR exit 回解释 | 本文 §3.3 三步;crescent 接管(§7) |
| 2 | Proto deopt 计数 +1 | atomic add 到 `ProfileData.deoptCount`(§5.6 的字段;P2 ProfileData 扩展点) |
| 3 | 解释期 IC 写入更新 feedback | 解释器跑剩余字节码时 IC 自然采样新观测,confidence 被新数据稀释([../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §6.4 IC 写入纪律) |

要点:

- **三件事是「单次 deopt 的全部副作用」**:本帧不再返回 P4 编译码;调用链上层不动。
- **IC 更新是 P4 与 P2 自动的接力**:解释器跑续跑路径时,IC 写入会自然记录「这个算术点见过非 number」(承 [../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §6.4 numHits/metaHits 写入)——这对应 P2 阶段聚合 TypeFeedback 时 confidence 被稀释,使下次重编译看到的 feedback 已不再判定「恒 number」。**这就是「再训练」的物理通道**——不需要 P4 主动通知 P2,IC 写入是常驻的 P1 行为,exit 后续跑的几条 IC 写入即完成训练样本更新。
- **confidence 稀释速度由 IC 阈值机制决定**:具体阈值(几次稀释 confidence 才掉到不投机线)留 P2 实测校准(§5.6),与不变性论证无关。

### 5.2 deopt 计数超低阈值 → P4 内 P4Deoptimized + 重训练后重编译

**P4 内部子状态机增量(承 [./03-speculation-ic.md](./03-speculation-ic.md) §4.2 状态图;方案 A——P2 三态不变)**:

```
                          guard 失败 ─┐
                                     ▼
P2 状态机(不变):              P4 内部 p4SpecState 子状态机:
TierInterp ──► TierGibbous     P4Speculative
                (吸收态)            ▲              │
                                    │              ▼
                                    └ 再训练 ── P4Deoptimized
                                                 (P4 内「降层」语义,
                                                  解释续跑 + 计数 +1)
                                                    │ 反复 deopt
                                                    ▼
                                              P4StuckSpeculation
                                                (P4 内吸收态,§5.4;
                                                 P2 看仍 TierGibbous)
```

**deopt 计数超阈值的处理(P4 端伪码,P2 实装零修改)**:

```
onOSRExit(proto, exitInfo):
  // P4 内部状态字段(见 internal/gibbous/jit/p4state.go,P2 不感知)
  s := p4SpecState[proto]
  s.deoptCount++
  if s.deoptCount < DeoptThreshold:
      return                              // 单次失败,继续观察

  // 阈值触达:P4 端 GibbousCode 失效——丢弃当前投机版编译产物,
  // P4 子状态置 P4Deoptimized;P2 tierState 不动(仍是 TierGibbous)
  oldCode := proto.gibbousCode             // 留给后台 GC / Dispose
  proto.gibbousCode = nil                  // 经 bridge.installGibbous(nil) 撤销 P4 版安装
  p4SpecState[proto] = P4Deoptimized       // ★ P4 端字段,P2 不感知
  scheduleDispose(oldCode)                 // 异步释放 codeSeg([./05-system-pipeline] §6)

  // ★ feedback 自然由 IC 写入更新——不显式 reset feedback,
  //   因为「IC 已记到此刻的最新观测」就是新训练样本(§5.1)
  //   p4SpecState[proto].deoptCount 在再升时清零(下一次 install 时重置)
```

要点:

- **P4 端「降层」语义不写 P2 tierState**:P2 §2.4 的「无 Gibbous→Interp 边」(单向无环)在方案 A 下被严格遵守——P4 阶段「有意打破投机假设」是**P4 内部状态机增量**,落到 `p4SpecState[proto]` 字段而非 P2 `pd.tierState` 枚举值;P2 视角看该 Proto 仍是 `TierGibbous`,只是当前未安装投机版 GibbousCode(下次升时由 P4 重编后再装,或 stuck 后装通用版)。
- **再热后重编译,失效投机点降级为通用模板**:重编译时 P4 看到的 TypeFeedback 已被 §5.1 的 IC 更新稀释——之前判 `FBArithStableNumber` 的点现在可能是 `FBUnstable`,P4 据此发**通用模板**(走 helper 慢路径,无 guard,语义完备,承 [./03-speculation-ic.md](./03-speculation-ic.md) §2.1 表)。
- **新编译产物也可能再 deopt**:若新观测仍未稳定,新编译码的某些点仍会触发 deopt——计数继续累加(deoptCount 在再编译后清零 vs 累计的取舍见 §5.6),累到阈值再回 P4Deoptimized 一次。这条循环最多走两次,因为 §5.3 的吸收态会在循环失控前接管。

### 5.3 重编译后仍反复 deopt → P4StuckSpeculation

**纪律**:重编译后(同 Proto 在 P4 上第二次升 + 再次 deopt 累到阈值)仍反复 deopt → P4 端把 `p4SpecState[proto]` 标 `P4StuckSpeculation`(P2 端 `tierState` 仍是 `TierGibbous`,不动)。

```
onOSRExit reaches threshold a second time:
  s := p4SpecState[proto]
  if s.recompileCount >= MaxRecompileTries:   // 通常 1-2 次
      p4SpecState[proto] = P4StuckSpeculation  // P4 内吸收态,§5.4
      // 该 Proto 永久只发无投机的通用模板(P4 端覆写 GibbousCode 为通用版),
      // P2 视角仍是 TierGibbous(下次 doCall 经统一 P3Compiler 接口装通用版)
      installNonSpeculativeGibbous(proto)
```

`P4StuckSpeculation` 的物理含义:

- **该 Proto 永久不再发投机模板**:无 f64 fast path、无 IC 表槽直达、无 confidence 决策——所有点都发通用模板(等价解释器语义,通过 helper 实现的慢路径);**P2 视角看仍是 TierGibbous,只是 P4 端永久供应「通用版 GibbousCode」**。
- **仍比解释快**:通用模板仍消除 dispatch 税(承 [./02-template-direction.md](./02-template-direction.md) §1.1)——取指/译码/dispatch 跳转/pc 维护仍由编译码静态化,只是不再做类型投机。
- **相对 P2 `TierStuck` 的差异**:P2 `TierStuck`(P2 实装管,F1-F7 拒)= 永久解释 + 不再编译;`P4StuckSpeculation`(P4 实装管,投机收敛失败)= 永久不投机 + 仍发通用版编译码。两者位于不同状态机,不互斥(承 [./03-speculation-ic.md](./03-speculation-ic.md) §4.3 表)。

### 5.4 与 P2 04-try-compile-fallback §7 不重试纪律的对位

**对位表**(承 [../p2-bridge/04-try-compile-fallback](../p2-bridge/04-try-compile-fallback.md) §7;**两个吸收态在两个不同的状态机里**):

| 维度 | P2 PB0 不重试纪律(`TierStuck`)| P4 不重试纪律(`P4StuckSpeculation`)|
|---|---|---|
| 状态机归属 | P2 `tierState` 枚举(P2 实装) | P4 内部 `p4SpecState[proto]` 字段(P4 实装,P2 不感知)|
| 触发场景 | try-compile 失败(F1-F7 不可编译 / Compile err) | 重编译后仍反复 deopt(类型假设始终失败) |
| 物理依据 | 同 Proto 同 P3/P4 后端 = 同结果(§7.1) | 同 Proto 类型行为不稳定 = 重编译仍 deopt(§5.3) |
| 工程价值 | 防止「不可编译热函数」无限重试 | 防止「投机不稳定 Proto」反复编译/deopt 抖动 |
| 重评估边界 | 进程重启自然边界 | 同左 |

**两个不重试纪律是同一思想的两根支柱**——P2 守 `TierStuck` 不重试编译,P4 守 `P4StuckSpeculation` 不重试投机。少了任一,反复抖动。

### 5.5 deopt 风暴的物理学

**deopt 风暴**:同一 Proto 在短时间内大量 OSR exit 的现象——表征是 deoptCount 飙升,反向影响 throughput。

物理来源:

- **类型假设过强,真实数据多态**:IC 在某个采样窗口看到的恒 number 只是巧合,真实数据是 number/string 混合。第一次升 P4 时 confidence 高 → 投机;实际跑时频繁 deopt。
- **窗口偏置**:训练样本(IC 在升层前的写入)与生产样本(升层后真实负载)分布不一致——这是所有投机式 JIT 的共性问题。

物理后果:

- **throughput 下降**:每次 deopt 是 exit + reloadFrame + 解释续跑——比一路解释还慢(多了 trampoline 出入)。
- **CPU 浪费在编译/重编译**:回 TierInterp + 重编译再再 deopt 的循环最坏 N 倍单次编译成本。

**P4 的防风暴策略**(本节 §5.2-§5.4 总览):

1. **deoptCount 阈值低**:几次 deopt 就置 `P4Deoptimized` + 撤当前投机版编译产物,不让 deopt 一直发生在线上。
2. **重编译次数硬上限**:`MaxRecompileTries`(典型 1-2 次)——失败次数到了就吸收。
3. **吸收态 P4StuckSpeculation 防抖**:不再尝试投机,P4 端永久供应通用版 GibbousCode。

### 5.6 阈值数值待 P2 实测校准

**留缺口(承 [../p2-bridge/01-profiling](../p2-bridge/01-profiling.md) §5)**:

| 阈值 | 含义 | 默认值预算(待校准) |
|---|---|---|
| `DeoptThreshold` | 单次 P4 编译产物上累计 deopt 多少次后置 P4Deoptimized | 数十次量级(类比 V8 `--max-opt-count`) |
| `MaxRecompileTries` | 同 Proto 在 P4 上重编译的最大次数 | 1-2 次 |
| 重编译间冷却期 | deopt 触阈到置 P4Deoptimized,新一轮 considerPromotion 触发前最少观察的 onBackEdge 数 | 数千次回边(让 IC 充分稀释) |

**校准依赖**:列内核负载 + 实战脚本的 deopt 频次分布——这是 P2 实测后才能定的工程数字,不影响本文协议正确性,只影响时机(承 [../p2-bridge/04-try-compile-fallback](../p2-bridge/04-try-compile-fallback.md) §7.4 跨版本重评估同款)。

**P4 端内部状态字段(方案 A,P2 实装零修改)**:

- `p4SpecState[proto].deoptCount uint32`:本 Proto 在当前 P4 编译产物上的累计 deopt 次数(每次重编译时 reset);住 `internal/gibbous/jit` 内部 map,P2 不感知。
- `p4SpecState[proto].recompileCount uint8`:本 Proto 在 P4 上的重编译次数(累计,不 reset,达 `MaxRecompileTries` 后吸收 P4StuckSpeculation);住同款 P4 端 map。
- **P2 `tierState` 枚举不动**——不新增 `TierGibbousJIT` / `TierStuckSpeculation`(承本文头注方案 A 决议 + §12 RJ-8/9/10 撤回);P4 内部 `P4Speculative / P4Deoptimized / P4StuckSpeculation` 三态住 P4 实装。

---

## 6. exit stub 的物理形态

本节给 exit stub 的具体形态——每个 guard 处编译期生成一段「exit 着陆点」的物理代码。形态对位 [./05-system-pipeline](./05-system-pipeline.md) §4.2 的 trampoline 三种出口与 [./06-backends](./06-backends.md) §5 的 amd64/arm64 实装。

### 6.1 每个 guard 处编译期生成一段 exit stub

**exit stub 的位置**(amd64 概念,arm64 同形态;**伪汇编中 `r15`/`r14` 等寄存器仅作示例占位,具体寄存器约定见 [05 §3.3 / 06 §4.1](./05-system-pipeline.md);不同节中同一寄存器名指代物在 03/04/05 间不一致——以 05 §3.3 jitContext 寄存器固定纪律为单一事实源**):

```
某条投机模板的发射形态:
  template_start:
    ; load 操作数
    mov rax, [rsi + 8*B]
    mov rbx, [rsi + 8*C]
    ; guard 双 number(IsNumber 单 u64 比较,[../p1-interpreter/01] §3.2):
    ; IsNumber ≡ u64 < NAN_THRESHOLD,失败 ≡ >= 阈值 ⇒ 用 jae 跳出(承 03 §3.4)
    movabs rcx, NaN_HIGH_BITS
    cmp rax, rcx
    jae exit_stub_42         ; ← guard 失败(>= 阈值,不是 number)跳本模板专属 exit stub
    cmp rbx, rcx
    jae exit_stub_42
    ; f64 fast path
    movq xmm0, rax
    ...
    movq [rsi + 8*A], xmm0   ; store 回栈槽,模板结束
    jmp template_next

  ; ... 函数其余模板 ...

  ; ── exit stub 段(函数末尾或独立段)──
  exit_stub_42:
    ; step 1: 寄存器→栈槽写回(若有缓存,§3.6;此例无缓存,跳过)
    ; step 2: 写 exitPC
    mov dword ptr [r15 + savedPC_offset], 42  ; 42 是该 guard 对应的字节码 pc
    ; step 3: atomic deopt 计数
    lock inc dword ptr [r14 + deoptCount_offset]
    ; step 4: 经 trampoline 出 JIT 世界(§6.2 / §8)
    mov eax, 2                                ; status=2 DEOPT
    jmp deopt_exit_trampoline
```

要点:

- **每个 guard 一个专属 stub 标签**(`exit_stub_42`):因为不同 guard 的 exitPC 立即数不同,不能直接共享。
- **stub 段单独放函数末尾**:与热路径分离,不污染 icache(承本文 §6 风险节「icache 压力」)。
- **stub 内部是固定指令序列**:几条 mov/inc/jmp,O(1) 大小。

### 6.2 stub 内容:三步具象化

承 §3.3 的三步,具象到机器指令(amd64 形态):

```
exit_stub(guardId):
  ; ── step 1(可选):寄存器→栈槽写回 ──
  ;   仅当本 guard 所在模板段启用了 §3.6 局部缓存时存在;
  ;   每条指令固定 store 一个寄存器到一个栈槽。
  mov [rsi + 8*loopVar], r12     ; (示例)循环变量 i 写回
  mov [rsi + 8*loopLim], r13     ; (示例)循环上限写回

  ; ── step 2:写 exitPC + atomic deopt 计数 ──
  mov dword ptr [r15 + ci_savedPC_offset], <exitPC_imm>  ; 写 CallInfo.savedPC
  ; ★ deoptCount 住 P4 端 p4SpecState[proto](方案 A,P4 实装内部 map),
  ;   非 P2 ProfileData 字段;jitContext 经 r14 间接寻址到本 proto 槽
  lock inc dword ptr [r14 + p4_deoptCount_offset]        ; p4SpecState[proto].deoptCount++

  ; ── step 3:经 trampoline 出 JIT 世界 ──
  ;   设 status=2(DEOPT,[../p2-bridge/05] §6.1 status 编码)
  ;   跳到 trampoline 的统一退出点(§6.5 三出口共用)
  mov eax, 2
  jmp deopt_exit_trampoline
```

寄存器约定(伪汇编寄存器名仅作示例占位,具体寄存器约定见 [05 §3.3 / 06 §4.1](./05-system-pipeline.md)——本文 r15/r14 与 05 jitContext 单一事实源不一致,以 05 为准):

- `rsi` = base pointer(arena 值栈段的本帧起点;05 实际用 `rbx`)
- `r15` = 当前 CallInfo 指针(进入帧时由 trampoline 装入;05 实际用 `r15` 装 jitContext,CallInfo 字段经 jitContext 间接寻址)
- `r14` = P4 端 p4SpecState 槽指针(deopt 计数挂处;§5.6;05 实际用 `r14` 装 arena base,p4SpecState 经 jitContext 间接寻址)
- `eax` = status 返回值

stub 大小估算:

- 无寄存器写回:**约 5 条指令**(2 个 mov 立即数 + 1 个 lock inc + 2 个 mov+jmp)
- 有寄存器写回(平均 2 个):**约 7 条指令**

**比 P5 snapshot 重建几十~几百条指令小 10-100 倍**(承 §9.1 复杂度对照)。

### 6.3 stub 的代码复用:相邻 guard 经常共享同一 exit stub

**优化空间**:相邻多条投机模板的 exit 操作往往相同——同一字节码 pc(同条指令的多个 guard)、同一寄存器写回集合——可共享 exit stub。同条 ADD 的 B/C 两个 IsNumber guard 都跳同一 `exit_stub_for_ADD_42`,exitPC=42,语义同。进一步:不同 exitPC 的 guard 通过把 exitPC 装入寄存器后跳到共享 stub 实现(多一条 mov + 跳的成本)。ROI 实测决定([./06-backends](./06-backends.md) §5);本文不裁决形态,只承「stub 复用是合法优化方向,不破坏 §3.7 不变式」。

### 6.4 amd64 / arm64 各自的 stub 形态对照

承 [./06-backends.md](./06-backends.md) 双后端纪律——同形态、各自指令序列。amd64 见 §6.2。arm64 概念差异:

- **寄存器写回用 `stp`(store pair)**:多寄存器写回时省一半指令。
- **立即数编码**:exitPC ≤ 16-bit 直接 `movz`,大于则 `movz + movk` 组合。
- **atomic 计数用 LL/SC**(`ldaxr / stlxr`)替代 amd64 的 `lock inc`。
- **icache flush 在整段编译产物写完后做一次**:exit stub 不需要单独 flush。

具体 stub 实装(寄存器分配、emitter)由 [./06-backends](./06-backends.md) §5 承担,本文给概念形态。

### 6.5 stub 与 trampoline 出口共用

**承 [./05-system-pipeline](./05-system-pipeline.md) §4.2 trampoline 三种出口**:

```
trampoline 出口(三种):
  ┌──────────────────────────────────────────────────┐
  │  正常返回(status=0 OK)                          │
  │     被调函数 RETURN 后跳来,返回值已在共见栈槽   │
  │  慢路径 helper 调用(中间出口,非真正退出)      │
  │     调 Go helper(元方法/分配/host call)        │
  │     helper 返回后回 trampoline 重入 JIT          │
  │  ★ OSR exit(status=2 DEOPT,本节)             │
  │     guard 失败,exit stub 跳来                   │
  └──────────────────────────────────────────────────┘
                       │
                trampoline 出口逻辑(汇编 stub):
                  恢复 Go 被调方寄存器
                  切回 Go SP
                  返回 status 给 P4code.Run([../p2-bridge/05] §6.1)
```

**三出口共用同一段 trampoline 退出代码**——只在 status 寄存器(eax/w0)的值上区分。这就让 exit stub 的「step 3 出 JIT 世界」退化为「设 status=2 + 跳 trampoline 退出点」,无需单独的 deopt 退出汇编路径。

物理收益:

- **trampoline 代码段大小不膨胀**:加一种出口语义,不加一段汇编。
- **status 编码统一**:`0=OK / 1=ERR / 2=DEOPT`(承 [../p2-bridge/05](../p2-bridge/05-p3-p4-interface.md) §6.1)——P3 用前两个,P4 用全三个。
- **GibbousCode.Run 接口不动**:`func (c *p4Code) Run(state *State, base int32) int32` 返回 status int32 即可,P2 看到的是统一抽象(承 [../p2-bridge/05](../p2-bridge/05-p3-p4-interface.md) §6 GibbousCode 接口)。

具体 trampoline asm stub 实装见 [./05-system-pipeline](./05-system-pipeline.md) §4.2 + [./06-backends](./06-backends.md) §5——本文不展开。

---

## 7. exit 之后的 crescent 接管

本节定 exit 完成后,crescent 解释器如何接管该帧续跑。

### 7.1 reloadFrame 协议

承 [../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §1.3:

```go
// crescent 主循环看到 doCall 收到 status=2(DEOPT)后调用
// 等价于 RETURN 后 doReturn 调 reloadFrame(切到调用者帧)的镜像版,
// 但这里不切帧——而是「重 load 当前帧的 frame 缓存」,因为 frame 缓存
// 在跨层时(P4 编译执行期间)未维护,exit 后必须从 CallInfo 重建。
func reloadFrameAfterDeopt(f *frame, th *Thread) {
    ci := th.curCI()                         // 当前帧 CallInfo(P4 帧未弹)
    f.base    = ci.base
    f.proto   = th.protos[ci.protoID]
    f.cl      = ci.cl
    f.pc      = ci.savedPC                   // ★ exitPC——从 stub 写入的字段
    f.stk     = th.valueStack[:]             // 重取 arena 栈 slice 别名
    f.k       = f.proto.Consts
    f.ic      = f.proto.IC                   // (若 P4 期间 IC 仍维护;§7.2 开放)
}
```

要点:

- **reloadFrame 与 enterLuaFrame / RETURN 的 reloadFrame 是同一函数**:解释器主循环本就在 fresh reentry / RETURN 切帧时调 reloadFrame——P4 deopt 复用同款 reloadFrame,语义同型。
- **frame 缓存的 stk/k/ic 三字段必须重取**:P4 执行期间不维护 Frame(Frame 是 Go 侧 execute 局部缓存,P4 跑的是机器码,不动 Frame)——所以 exit 后必须从 CallInfo + Proto 重建。
- **f.pc = ci.savedPC = exitPC**:这是 §4.2 的 stub 写入字段在解释器侧的取用点——exit 与续跑通过这个字段建立逻辑连接。

### 7.2 CallInfo 状态:bit50 callStatus_gibbous 在 exit 后是否还需置 1

**开放问题**:CallInfo bit50(callStatus_gibbous,承 [../p3-wasm-tier/04-trampoline](../p3-wasm-tier/04-trampoline.md) §1.2)在该帧 OSR exit 后,后续由 crescent 续跑——bit50 应保持 1 还是清零?

两个候选:

| 候选 | 含义 | 后果 |
|---|---|---|
| (a) bit50 保持 1 | 该帧仍标识为「曾经是 gibbous」 | traceback 显示有差异(若开 gibbous 帧豁免);部分 yield 路径检查会基于 bit50 判定 |
| (b) bit50 清 0 | exit 后该帧回归「crescent 帧」 | 与「一路解释」帧状态完全一致——差分友好 |

**倾向 (b)**(承 [./08-testing-strategy.md](./08-testing-strategy.md) §7.2 差分主防线 + [../p3-wasm-tier/04-trampoline](../p3-wasm-tier/04-trampoline.md) §4.5 traceback 不为 gibbous 帧开豁免):

- 差分口径要求 traceback 逐字节一致——若 bit50 触发任何特殊显示,exit 后续跑的 traceback 与「一路解释」就有可观察差异。
- exit 后帧的执行引擎已是 crescent,逻辑上应清 bit50(它的语义是「正在 wasm/jit 执行」)。
- 实装代价小:exit stub 在 step 1 时顺手清 bit50(一条 mov 即可)。

**留 P3/P4 落地时实测确认**:目前 [../p3-wasm-tier/04-trampoline](../p3-wasm-tier/04-trampoline.md) §4.5 已承「traceback 不为 gibbous 帧开豁免」,所以 (a) 与 (b) 在 traceback 输出上等价;但 yield/coroutine 路径与未来 P5 录制宿主可能依赖 bit50 的某个语义——本文只点名开放问题,留 P4 落地时定。

### 7.3 解释器从 exitPC 续跑:与「一路解释」逐字节一致

**纪律**:exit 后续跑路径与「同输入一路解释跑」**逐字节一致**(差分主防线,承 [./08-testing-strategy.md](./08-testing-strategy.md) §7.2):

- 同样的字节码 pc 序列(从 exitPC 起到本帧 RETURN)。
- 同样的栈槽读写序列(NaN-box u64 同编码)。
- 同样的 CallInfo 字段值。
- 同样的 traceback 输出(承 §7.2 (b) 候选)。

**这条不变式由两件事保证**:

1. **物化把状态变得无差异**(§2):exit 后栈状态 = 一路解释跑到此 pc 时的栈状态。
2. **解释器代码同一份**:exit 后用的是 crescent 主循环代码——与「一路解释」用同一段 Go 代码,执行行为同型。

**不一致的唯一可能性**:exit 物化漏写某个栈槽 / 物化的值与一路解释跑的不一致。这是 P4 投机错误的「静默错果」候选——防线全部交给差分接入测试([./08-testing-strategy](./08-testing-strategy.md) §7.2 deopt 注入测试,把每条 exit 路径强制走一遍 byte-equal 验证)。

### 7.4 错误冒泡的承接:OSR exit 不是错误

**纪律**:OSR exit 路径**不应**设置 `state.pendingErr`——exit 是「投机失误」,不是「语义错误」,与 status=1 ERR(P3 错误冒泡)是两个互斥事件。

```
exit 与错误的关系:
  trampoline 出口处理 status:
    case 0: 正常返回(返回值已在栈槽);state.pendingErr 必须为 nil
    case 1: 错误冒泡;state.pendingErr 必须非 nil(由 helper 设置)
    case 2: OSR exit;state.pendingErr 必须为 nil
            ↓
            crescent 主循环见 status=2 → reloadFrame + 续跑
            (不调 throwPending,因为没错误)
```

**罕见同发情况**:exit 与错误同时发生(如投机失败 + context 取消同时落点)——

- 优先级:**错误优先**——若 `state.pendingErr` 非 nil(可能是 helper callback 在 exit 之前的某次调用设置的),trampoline 出口按 status=1 处理,不走 OSR 着陆。
- 物理依据:错误是确定要传递的语义事件,不能因为 exit 路径忘记冒泡而吞掉。

**实装防线**:OSR exit stub 在 step 1 时检查 `state.pendingErr == nil`,若非 nil 则修改 status 为 1 并跳错误出口——这条防线由 [./05-system-pipeline](./05-system-pipeline.md) §4 trampoline 实装承担,本文承「exit 不应同时携错」的语义。

---

## 8. P4 OSR 接 P3 跨层协议:复用 04-trampoline 哪些部分

本节定 P4 OSR 与 P3 跨层协议的关系——**P4 在概念层面继承 P3 跨层协议,在物理层面用原生 asm trampoline 替代 wazero trampoline,在 status 编码上扩展 status=2 DEOPT 出口**。

### 8.1 trampoline 出/入门径:P4 复用 wazero trampoline 协议在「概念」层面

**承 [../p3-wasm-tier/04-trampoline](../p3-wasm-tier/04-trampoline.md) §0.4 P4 继承承诺**:

| 协议点 | P3 形态 | P4 形态 | 复用关系 |
|---|---|---|---|
| 入口签名 | `(base i32) → status i32`(单 i32 入参 + i32 返回) | 同左,原生 asm trampoline 实装 | 概念同款,物理不同 |
| CallInfo bit50 | gibbous 帧入口写 1 | 同左 | 概念同款 |
| 跨层只传 base | `base` 字节偏移 | 同左 | 概念同款 |
| 错误冒泡 | status 链单向冒泡到 protected 边界 | 同左 | 概念同款 |
| helper 三向分派 | gibbous/crescent/host 三向(`h_call`) | 同左,原生 call 替代 imported call | 语义同款 |

**这就是「P4 是 P3 同 tier 的另一发射后端」的协议侧兑现**(承 [./00-overview.md](./00-overview.md) §0.1 tier 表)——不动协议,只换执行引擎。

### 8.2 但物理实现完全不同

**P4 vs P3 物理差异**:

| 维度 | P3 wazero trampoline | P4 原生 asm trampoline |
|---|---|---|
| 跨层机制 | wazero `fn.Call(ctx, base)`(Go→Wasm 边界) | asm stub 切 SP + 直接跳目标地址(Go→机器码边界) |
| 入参传递 | wazero 经栈传 i32 | 寄存器约定(amd64: rsi=base / arm64: x0=base) |
| context 取消 | wazero ctx 透传给 imported callback | jitContext 字段 + 回边检查([./05-system-pipeline.md](./05-system-pipeline.md) §4.1 抢占检查) |
| helper 调用 | imported function via wazero | 原生 call Go 函数(经 jitContext.helperTable) |
| icache 一致性 | wazero 自管 | arm64 显式 IC IVAU/DC CVAU([./05-system-pipeline.md](./05-system-pipeline.md) §4.2) |

**P4 不写 trampoline asm stub 实装**(那是 [./05-system-pipeline](./05-system-pipeline.md) §4.2 + [./06-backends](./06-backends.md) §5 的事)——本文只承「概念协议同 P3,物理实装 P4 自付」。

### 8.3 status 编码扩展:P4 trampoline status=2 DEOPT 出口

**承 [../p2-bridge/05](../p2-bridge/05-p3-p4-interface.md) §6.1 GibbousCode.Run 返回 status 编码**:

```
status 编码语义(GibbousCode.Run 返回值):
  0 = OK    (正常返回,返回值已回填 R(A..);P3/P4 均用)
  1 = ERR   (state.pendingErr 已置,走错误冒泡;P3/P4 均用)
  2 = DEOPT (OSR exit,P4 OSR 着陆;★ P4 独有,P3 永远不返回 2)
```

**P4 状态机增量在 status=2**:

- P3 trampoline 永远不返回 2(承 [../p3-wasm-tier/04-trampoline](../p3-wasm-tier/04-trampoline.md) §0.4)——P3 是零 deopt 设计。
- P4 trampoline 返回 2 时,crescent doCall 见 status=2 走 OSR 着陆(reloadFrame + 续跑,不弹 CallInfo)——这与 status=0(弹 CallInfo + 主循环继续解释调用者帧)、status=1(弹 CallInfo + throwPending)是三条不同的处理路径。

doCall 收到 status 后的处理(三分支):

```
crescent doCall 的 enterGibbous 收到 GibbousCode.Run 返回:
  switch status {
  case 0:                     // OK,正常返回
      popCallInfo(th)
      return callReturnedGibbous   // 主循环不重载 code(没切到新 crescent 帧)
  case 1:                     // ERR,错误冒泡
      popCallInfo(th)              // gibbous 帧由 trampoline 弹出
      return vm.throwPending(f)    // 错误一路 return 出 protected 边界
  case 2:                     // ★ DEOPT,OSR 着陆(P4 独有)
      // 不弹 CallInfo——本帧的 CallInfo 留给 reloadFrame 用
      // savedPC 已由 exit stub 写为 exitPC(§4.2)
      reloadFrameAfterDeopt(f, th)  // §7.1
      return callDeoptResume         // ★ 新出口:主循环重载 code,从 exitPC 续跑
  }
```

**`callDeoptResume` 是 P4 阶段的 doCall 新出口**——P3 阶段只有 `callReturnedGibbous / callReturnedHost / callEnteredLua / throwPending` 几个,P4 增一个。这是对 [../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §7 doCall 接口的扩展(P4 落地时同批补)。

### 8.4 helper 调用协议:P4 慢路径助手的形态

承 [./05-system-pipeline](./05-system-pipeline.md) §4.3,P4 helper 与 P3 helper 形态对位:

| 维度 | P3 helper(`h_call/h_arith/...`) | P4 helper(同名,原生形态) |
|---|---|---|
| 调用机制 | imported function via wazero | 原生 call(经 jitContext.helperTable[idx]) |
| 入参 | i32 立即数(base/pc/op) | 寄存器约定(同 P4 trampoline 入口形态) |
| pc 物化 | helper 入口写 `ci.savedPC = pc` | 同左 |
| 三向分派(h_call) | callee 类型 switch(gibbous/crescent/host) | 同左 |
| 慢路径复用 crescent | 调 `state.arithMeta` 等 crescent 同款实现 | 同左 |

**P4 helper 表与 P3 imported 表是「概念同款,实装方式不同」的对偶**——这是 P4 系统管线的细节,详见 [./05-system-pipeline](./05-system-pipeline.md) §4.3,本文不展开。

> **本文与 helper 协议的接点**:OSR exit stub 在 step 3 经 trampoline 出口时,**不调任何 helper**——deopt 出口是纯汇编序列(写字段 + 设 status + 跳 trampoline 退出点)。这与 status=1 ERR 路径常见经 helper 调用形成的(helper 设 pendingErr 后 return 1)不同——deopt 路径无需 helper,因为 exit 不是错误,不需要构造 LuaError,不需要语义动作。

---

## 9. snapshot 复杂度对照(承旧 §3.3 末 + 与 P5 边界)

本节是本文的核心对照表——把 P4 与 P5 deopt 机器的复杂度逐维度并列,作为「P4 简单性向下传导」论证的量化兑现。

### 9.1 P4 函数级 OSR vs P5 snapshot deopt 的复杂度对照表

承本文 §3 与 [../p5-trace-jit](../p5-trace-jit.md) §4.3 的核心对照,本节给完整版:

| 维度 | P4 函数级 OSR | P5 snapshot deopt |
|---|---|---|
| **exit 粒度** | 函数(整帧放弃 P4 编译码) | 指令级(trace 内任意 guard) |
| **恢复的帧数** | 1(当前帧,且已在 CallInfo) | 1..N(含从未物理存在的内联帧) |
| **值的位置** | 已在栈槽(栈槽真相不变式,§3.1) | 寄存器/spill/常量/被 sink 的对象字段 |
| **映射数据** | 无需(静态生成 exit 序列,§4.1 极少) | 每 guard 一份 snapshot,需压缩与生命周期管理 |
| **出错形态** | 几乎无投机面(物化 = memmove,§2) | 任一槽映射错 / unsink 漏字段 ⇒ 静默错果 |
| **exit stub 大小** | ~5-7 条机器指令(§6.2) | ~50-200 条(snapshot 解析 + 多帧重建 + unsink) |
| **运行期分配** | 无(§3.7 不变式) | 可能(unsink 重建对象需新分配) |
| **GC 交互** | 无(exit 不分配,根天然可见) | unsink 中途可能触发 GC(承 [../p5-trace-jit](../p5-trace-jit.md) §4.2 末) |
| **CallInfo 操作** | 写 savedPC 一字段 + 不弹 | 弹/补建 N 帧 CallInfo |
| **人年成本** | 已纳入 P4 +1-2 人年 | 是 P5 +2-4 人年的主成分(承 [../p5-trace-jit](../p5-trace-jit.md) §4.4) |

**复杂度差大约一个量级**——这就是「P4 用『不优化跨指令』换掉整台 snapshot 机器」的具体含义(§3.5)。

### 9.2 P5 必须靠 snapshot 的物理原因

承 [../p5-trace-jit](../p5-trace-jit.md) §4.1,P5 三项核心优化都拆毁栈槽真相:

| P5 优化 | 拆毁栈槽真相的方式 | snapshot 必须做什么 |
|---|---|---|
| 寄存器分配 | 活值在机器寄存器/spill,不在栈槽 | 记 IR 值 → 寄存器/spill 槽的映射;exit 时按映射 store 回栈槽 |
| trace 内联 | 一条 trace 跨多帧,被内联帧从未压 CallInfo | 记 frames[] = (protoID, base 偏移, 返回 pc) 列表;exit 时按列表 push CallInfo |
| 分配下沉(sinking) | 「对象」物理上不存在,字段散在 IR 值 | 记 sunk 重建配方 = (类型, 字段值/IR 引用) 列表;exit 时分配真对象再填字段(unsink) |

**三者「互为前提、高度耦合」**(承 [../p5-trace-jit](../p5-trace-jit.md) §4.4):snapshot 引用的 IR 值必须在 exit 时可恢复,约束 regalloc 自由度;sink 优化要不损失正确性必须配合 snapshot;snapshot 压缩与 regalloc 的耦合是 LuaJIT 多年精炼的产物。

### 9.3 P4 的 deopt 是「真相点处处」的退化

**P4 deopt 简单的四个连锁原因**:

```
不引入 regalloc/inline/sink           ──► (1) 栈槽真相不变式(§3.1)
不引入 regalloc/inline/sink + P1 NaN-box ──► (2) 物化 = memmove(§2)
栈槽真相 + 编译期 exit 序列            ──► (3) exit 静态生成,无运行期解析(§3.7 / §4.1)
单帧编译 + 函数级 exit                  ──► (4) 单帧弹/补,CallInfo 操作 O(1)(§1.3 / §3.3)
```

每条链路独立提供一份简化,合起来让 P4 deopt 在每个维度都比 P5 简单一个量级。

### 9.4 「不优化跨指令」是这条简化的物理来源

承 [./02-template-direction](./02-template-direction.md) §3.3「不优化跨指令」的定稿——

> 模板编译每条字节码独立发射,机器寄存器只在单条模板内部短暂存活,栈槽真相在每个字节码边界都成立。

这条选择的**直接代价**是 P4 生成码保留栈槽内存往返(每条模板 load + store);**直接红利**是栈槽真相不变式 + 物化 = memmove + exit 序列静态生成。

**收益与代价兑换率由验收裁决**(承 [./08-testing-strategy.md](./08-testing-strategy.md) §7.1 luajc 档):
- 若验收达标(列内核负载 ≥ luajc),说明这条兑换在望舒约束下值得,P4 收口。
- 若不达标,需评估是否引入轻度 regalloc(只在直线段内),代价是 deopt 机器上升半个量级——但仍远低于 P5 全套 snapshot。

**不裁决的边界**:本文 §9 只对照「P4 现选 vs P5 全套 snapshot」两个端点。中间形态(轻度 regalloc 但无 inline/sink 的 mini-snapshot)留 [./08-testing-strategy.md](./08-testing-strategy.md) 验收回填。

---

## 10. 不变式清单

本文升格为 P4 不变式的五条聚合(覆盖 §1-§7):

1. **函数级 exit:整帧交还,不跨帧**(§1)
   - guard 失败 → 当前帧整体放弃 P4 编译码,从 exitPC 起由 crescent 续跑;调用链上层帧不动,各自维持 tier。
   - 否决:就地补救成通用模板继续跑;side trace;跨帧 deopt 重建多帧 CallInfo。

2. **栈槽真相:边界处全活值已物化**(§3.1)
   - 每条字节码边界,本帧全部 Lua 活值已在 arena 值栈槽;模板出口 store 是兑现点;机器寄存器只在单条模板内部短暂存活。
   - 推论:guard 失败瞬间待物化集合空(§3.2);snapshot 不必存在(§3.4)。

3. **物化 = memmove:同 NaN-box 编码,无格式转换**(§2)
   - P4 寄存器/栈槽/常量都是 NaN-box `uint64` 同编码;物化操作 = 单条 store;
   - 这是 P1 第一天值表示承诺([../../../llmdoc/must/design-premises](../../../llmdoc/must/design-premises.md) 前提四)在 P4 阶段的现金兑付。

4. **exit stub 编译期静态生成:无运行期 snapshot**(§3.7 / §4.1)
   - 每个 exit 点编译期烧入固定 store 序列与 exitPC 立即数;
   - 禁止运行期映射查询、禁止运行期解析配方、禁止 exit 路径分配。

5. **不重试纪律:反复 deopt 拉黑投机,吸收态**(§5.4)
   - 重编译次数到 `MaxRecompileTries` 后,P4 端把 `p4SpecState[proto]` 标 `P4StuckSpeculation`,永久不再投机(发通用模板或纯解释);P2 `tierState` 不动(仍 `TierGibbous`);
   - 与 P2 04-try-compile-fallback §7 不重试纪律对位——同款防抖,不同触发原因 + 不同状态机归属(P2 vs P4 实装)。

**P4 不变式 6(松弛版栈槽真相)**(承 §3.1 修订):

   - **guard 边界处**全活值在栈槽 ∪ 寄存器写回序列(编译期静态可达);
   - 严格栈槽真相在「不启用 §3.6 局部缓存」的模板段成立;启用局部缓存的模板段(如 FORLOOP 循环变量驻留)收窄到「guard 处真相点」+ exit 序列编译期静态生成的「寄存器→栈槽」写回脚本(§3.6 / §3.7);
   - 否决:exit 路径运行期解析配方;exit 路径分配。

这五条不变式是 P4 deopt 机制的协议骨架——任何实现倾向先过这五条。

---

## 11. 风险与开放问题

本文承担 P4 整体开放问题中「OSR 子集」的具体登记。

### 11.1 locals 寄存器跨指令缓存的开放

**承本文 §3.1 注 + [./06-backends](./06-backends.md) §5**:

- **「真相点处处」**(严格栈槽真相,无寄存器缓存):每个字节码边界栈槽完整;exit 序列零寄存器写回。
- **「真相点 guard 处」**(允许局部缓存,exit 处补写回):FORLOOP 循环变量驻留寄存器,exit stub 多几条 store 写回。

**裁决条件**:[./06-backends](./06-backends.md) §5 实测——循环变量驻留省下的内存往返 vs exit stub 多几条写回的成本。本文只承「若启用,exit 序列编译期静态生成」(§3.7 不变式)。

### 11.2 exit 着陆粒度的终稿待 amd64 原型实测

**开放问题:exit 单位的精化**:

- 「函数级 exit」的「函数」是否真等于「Lua Proto 一次激活」?多 closure 共享 Proto 时的 exit 行为?
- exit stub 的具象大小、stub 复用的 ROI、stub 段相对热路径的位置(同段 vs 独立段)。

留 amd64 原型实测——本文承概念形态,不裁决实装细节。

### 11.3 deopt 阈值数值与 P2 ProfileData 字段扩展的回填

**开放问题:阈值校准**(参 [../p2-bridge/01-profiling](../p2-bridge/01-profiling.md) §5):

- `DeoptThreshold` / `MaxRecompileTries` / 重编译间冷却期——三个阈值的具体数值,依赖列内核 + 实战脚本实测。
- ProfileData 字段扩展(`deoptCount` / `recompileCount` / 新 tierState 枚举值)——P2 落地后回填(§12)。

### 11.4 P4 编译执行的线程模型

**开放问题:编译执行线程模型**:

- 同步编译(升层触发 → 当前线程编译 → 安装 → 立即用):模板编译微秒级,可能够用。
- 后台 goroutine 编译 + 安装屏障:与 P3 / wazero 路线图同款决策。

**与 OSR 的接点**:无论同步 / 后台编译,deopt 后的「再编译」(§5.2)都遵循同样的编译模型——本文不增模型,只承「重编译 = 一次正常编译过程,从 TierInterp 重新走 considerPromotion」。

### 11.5 多 State 并发下 deopt 计数与重编译竞态

**新登记开放问题**:

- 多 State 共享同一 Proto——deopt 计数 atomic add 已防写竞态,但「触阈 → P4Deoptimized + 重编译」的子状态转移可能多线程并发(住 P4 端 `p4SpecState[proto]` map,P2 不卷入)。
- 候选纪律:重编译用 sync.Once 或 compileMu(承 [../p2-bridge/04](../p2-bridge/04-try-compile-fallback.md) §4.5 同款锁)守 single-flight。
- 留 P4 多 State 实测确认。

---

## 12. 回填请求

承 [../../../llmdoc/memory/doc-gaps](../../../llmdoc/memory/doc-gaps.md) 跟踪机制,本文向上游文档登记的回填请求:

> **方案 A 决议(承本文头注 + §5)**:P4 投机生命周期 P4 自管,P2 实装零修改——故撤回原 RJ-8(P2 04 加 `TierGibbousJIT/TierStuckSpeculation` 枚举)/ RJ-9(P2 01 加 `ProfileData.deoptCount`)/ RJ-10(P2 01 加 `ProfileData.recompileCount`)三项;P4 端在 `internal/gibbous/jit` 内部 map `p4SpecState[proto]` 自管(`P4Speculative / P4Deoptimized / P4StuckSpeculation` + `deoptCount` + `recompileCount` 字段)。本文保留下表中跨层契约相关的回填项(P1 05 doCall 出口 / 错误冒泡纪律 / P3 04 bit50 / P2 05 status=2 编码),这些与 tier 状态机分离。

| 回填项 | 上游落点 | 内容 | 状态 |
|---|---|---|---|
| ~~`TierGibbousJIT` / `TierStuckSpeculation` 枚举~~ | ~~P2 04 §2.1~~ | **撤回(方案 A)**——P4 内部 `p4SpecState[proto]` 子状态 `P4Speculative/P4Deoptimized/P4StuckSpeculation`,P2 `tierState` 三态不动 | ✅ 撤回 |
| ~~`ProfileData.deoptCount uint32`~~ | ~~P2 01 §2.2~~ | **撤回(方案 A)**——P4 端 `p4SpecState[proto].deoptCount`,P2 `ProfileData` 不动 | ✅ 撤回 |
| ~~`ProfileData.recompileCount uint8`~~ | ~~P2 01 §2.2~~ | **撤回(方案 A)**——P4 端 `p4SpecState[proto].recompileCount`,P2 `ProfileData` 不动 | ✅ 撤回 |
| `callDeoptResume` doCall 出口 | [../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §7 doCall 接口 / callResult 枚举 | doCall 收到 GibbousCode.Run 返回 status=2 时的处理出口(reloadFrame + 续跑同帧);P4 阶段新增,P1/P2/P3 不需要(承 §8.3) | **记录,P4 落地时同批补** |
| CallInfo bit50 在 OSR exit 后的语义 | [../p3-wasm-tier/04-trampoline](../p3-wasm-tier/04-trampoline.md) §1.2 / §1.4 bit50 写入纪律 | exit 后 bit50 是清 0 还是保留 1(§7.2)——倾向清 0(差分友好);P4 落地时实测确认 | **记录,P4 落地时定** |
| GibbousCode.Run status=2 编码 | [../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md) §6.1(已有,本文承认接口) | 该字段已在 §6.1 注释中预留(`2=DEOPT(P4 OSR exit;P3 永远不返回 2)`) | **已存在,本文承用** |
| OSR exit 路径不应设置 `state.pendingErr` | [../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §9 错误冒泡纪律 | exit 是「投机失误」非「语义错误」,不与错误冒泡互斥;若同发以错误优先(§7.4) | **记录,P4 落地时同批补** |

> **登记纪律(承 multi-doc-drafting 协议)**:本节回填请求是本文起草过程中识别的上游字段缺口,**不主动改 P1/P2 现稿**——记入 [../../../llmdoc/memory/doc-gaps](../../../llmdoc/memory/doc-gaps.md) 后,由 P4 落地时(或更早的字段预留批次)统一补。

---

相关:
[./02-template-direction](./02-template-direction.md)(模板编译方向,§3.3「不优化跨指令」是栈槽真相不变式的根) ·
[./03-speculation-ic](./03-speculation-ic.md)(IC 投机 + guard 形态——guard 失败的源头,本文 §1.6 / §3.1 接) ·
[./05-system-pipeline](./05-system-pipeline.md)(§4.2 trampoline 三种出口 + §4.3 helper 调用协议——本文 §6.5 / §8 对接) ·
[./06-backends](./06-backends.md)(amd64/arm64 双后端,§5 exit stub 实装 + §5 局部缓存优化——本文 §6.4 / §11.1 对接) ·
[./08-testing-strategy](./08-testing-strategy.md)(§7.2 deopt 注入测试 + 差分主防线——本文 §4.4 / §7.3 链过去) ·
[../p3-wasm-tier/04-trampoline](../p3-wasm-tier/04-trampoline.md)(P3 跨层协议,§0.4 P4 继承 / §1 bit50 / §4 status 链——本文 §0.2 / §8 对位) ·
[../p3-wasm-tier/05-safepoint-gc](../p3-wasm-tier/05-safepoint-gc.md)(回边 + 边界 safepoint——本文 §6 exit stub 与 safepoint 同位) ·
[../p5-trace-jit](../p5-trace-jit.md)(§4 snapshot deopt——本文 §3.4 / §9 复杂度对照的对偶面) ·
[../p1-interpreter/01-value-object-model](../p1-interpreter/01-value-object-model.md)(§7 值表示不变式 1——物化 = memmove 的物理基础) ·
[../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md)(§1 CallInfo + Frame / §1.3 reloadFrame / §7 调用约定 / §7.3 reentry 边界) ·
[../p2-bridge/04-try-compile-fallback](../p2-bridge/04-try-compile-fallback.md)(§7 不重试纪律——本文 §5.4 对位) ·
[../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md)(§6 GibbousCode.Run status 编码——本文 §8.3 接) ·
[../p2-bridge/01-profiling](../p2-bridge/01-profiling.md)(§5 阈值定标——本文 §5.6 对位) ·
[../../../llmdoc/must/design-premises](../../../llmdoc/must/design-premises.md)(前提四第一天值表示承诺——本文 §2.6 现金兑付) ·
[../../../llmdoc/memory/doc-gaps](../../../llmdoc/memory/doc-gaps.md)(回填请求 + 缺口跟踪)
