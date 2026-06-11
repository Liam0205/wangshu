# P4:带 IC 反馈的投机 method JIT(JSC Baseline 风格)

> 状态:**设计阶段,架构决策深度**(对齐 [architecture](./architecture.md) §2 状态表:P4 是「架构决策」,
> 比 P2/P3 的详细设计粗一档——本文定方向、定边界、给关键机制概念设计与权衡论证,
> **不逐 opcode 展开模板**;详细设计待 P3 落地、P4 启动决策通过后展开)。
>
> 对应 Go 包:`internal/gibbous/jit`(amd64/arm64 双后端、OSR exit,[architecture](./architecture.md) §1)。
> 上游契约:[roadmap](./roadmap.md)(§4 P4 定义、§2 四项税、§1 校准测量、§7 prior art)、
> [evolution-roadmap](../../llmdoc/architecture/evolution-roadmap.md)(tier 映射:P4=gibbous tier-1,与 P3 同层)、
> [design-premises](../../llmdoc/must/design-premises.md)(四项税 / 第一天值表示承诺 / 五条贯穿原则)。
> 依赖面:[p2-bridge](./p2-bridge.md)(热度 / TypeFeedback / 可编译性——P2 是编译层共享前端,P4 直接消费)、
> [p3-wasm-tier](./p3-wasm-tier.md)(分层结构的第一个用户;P4 继承其全部结构,只换发射后端)、
> [01](./p1-interpreter/01-value-object-model.md)(NaN-box 值表示——P4 生成码直接操作同一编码)、
> [02](./p1-interpreter/02-bytecode-isa.md)(源 ISA + §7 IC slot)、
> [05](./p1-interpreter/05-interpreter-loop.md)(§1 CallInfo / §7 调用协议——OSR exit 的着陆面)、
> [12](./p1-interpreter/12-testing-difftest.md)(层间差分——投机错误的主防线)。

---

## 0. 定位与启动条件

### 0.1 在演进流水线与 tier 阶梯上的位置

```
P1 解释器 ──► P2 分层桥 ──► P3 Wasm 编译层 ──► P4 method JIT ──► P5 trace JIT
(2-4x)        (基建)        (4-8x)             (trace 收益~70%)   (10-30x,开放式)
```

P4 = **gibbous(tier-1)的第二个发射后端**。tier 比阶段粗一层([evolution-roadmap](../../llmdoc/architecture/evolution-roadmap.md)):P3 与 P4 **同属 gibbous**——同一个 tier、同一套分层结构(P2 的热度/feedback/可编译性 + 升层状态机 + 跨层调用协议),区别只在「热函数被编译成什么」:P3 发 Wasm 交 wazero 执行,P4 **直接发原生机器码**。[roadmap](./roadmap.md) (§4) 的措辞是精确的:「继承 P3 的全部分层结构,**只换发射后端**(Wasm 发射→原生发射)」。包布局也据此:`internal/gibbous/wasm` 与 `internal/gibbous/jit` 并列([architecture](./architecture.md) §1)。

人力估算 **+1-2 人年**——这是流水线从人月级(P1/P2/P3)跨入人年级的第一站。

### 0.2 两条进入路径

| 路径 | 触发条件 | P4 承担什么 |
|---|---|---|
| **常规路径:P3 之后** | P3 已上线但收益不够:wazero 执行 Wasm 仍隔一层(Wasm 语义约束、wazero 调用边界、生成码质量受 Wasm 表达力限制),列内核负载到不了 luajc 档 | 只换发射后端——分层机器(升降层/trampoline/差分接入)P3 已跑通,P4 是纯后端替换 |
| **跳跃路径:P3 被 spike 否决** | [roadmap](./roadmap.md) (§4) P3 前置 spike:wazero call boundary 实测 ≥150ns ⇒ **跳过 P3 直接做 P4** | P4 额外承担「首次跑通分层机器」的全部工作(原 P3 的战略价值——升层/降层/fallback 骨架——移入 P4),人年估算相应上浮 |

两条路径产出同一个 P4,但跳跃路径风险更高:P3 的战略价值正是「在不用调试机器码的后端上先把分层机器整体跑通」([roadmap](./roadmap.md) (§4)),跳过它意味着分层骨架与机器码后端两块硬骨头同时啃。**本文以常规路径为基准描述,跳跃路径下「继承 P3」各处读作「P4 自建」。**

### 0.3 验收(先立靶再设计)

[roadmap](./roadmap.md) (§4):**列内核负载 ≥ LuaJ-luajc 档**。量化含义见 §7.1——luajc 档 = 校准测量 1 的 164μs 水位(≈4.4x over gopher-lua 同基准),而真 LuaJIT 仅比它快 6%(154μs)。**P4 达标即兑现 roadmap §0「逼近 LuaJIT 档」的近期含义**——这是整个项目「纯 Go 不必复刻 trace JIT 也能逼近顶档」核心论据([design-premises](../../llmdoc/must/design-premises.md) 前提一)的验收时刻。

---

## 1. 为什么是 JSC Baseline 风格(方向决策)

### 1.1 模板编译是什么

**Per-function 模板编译**:对一个热 Proto,编译器**线性扫一遍字节码**,给每条指令贴上一段预制的机器码模板(操作数槽位、常量、跳转目标在贴入时实例化),顺序拼接成该函数的原生代码。**没有 IR,没有跨指令的寄存器分配,没有指令调度**:

- **虚拟寄存器 = arena 值栈槽**,与解释器完全一致(`R(i)` = `valueStack[base+i]`,[05](./p1-interpreter/05-interpreter-loop.md) §1.3)。模板从栈槽 load 操作数到机器暂存寄存器、计算、store 回栈槽——机器寄存器只在**单条模板内部**短暂存活,不跨字节码边界。
- 控制流直译:字节码的 `JMP`/条件跳 → 机器跳转到对应模板起始地址(一遍扫描记 pc→机器地址映射,回填前向跳转)。
- 编译时间与字节码长度**线性**,单函数微秒级——升层停顿可忽略,无需后台编译流水线也成立。

**它消除的是解释器的恒定税**:取指、译码、dispatch 跳转、pc 维护——这些在 [05](./p1-interpreter/05-interpreter-loop.md) §2 的主循环里每条指令都付,占解释开销的大头。**它不消除**栈槽内存往返(值仍住 arena 栈)——那是 P5 寄存器分配的活(§1.4)。

### 1.2 在分层阶梯上的位置(prior art 对照)

[roadmap](./roadmap.md) (§7) 点名 V8 与 JSC 的分层阶梯为标准参照:

| 引擎 | 解释器 | **模板编译层(P4 的对标)** | 优化编译层 |
|---|---|---|---|
| JSC | LLInt | **Baseline JIT**(per-opcode 模板 + IC) | DFG → FTL |
| V8 | Ignition | **Sparkplug**(单遍、无 IR、直接从字节码发射) | Maglev → TurboFan |
| 望舒 | crescent(P1) | **gibbous/jit(P4,本文)** | fullmoon(P5) |

Sparkplug 论文式的自我定位「a compiler dispensing with the interpreter dispatch」同样是 P4 的定位:**P4 是 dispatch 消除器 + IC 投机注入器,不是优化编译器。**

### 1.3 为什么不是优化编译器(成本/收益曲线)

候选谱系与否决理由:

| 候选 | 人力 | 在望舒约束下的收益 | 判定 |
|---|---|---|---|
| **模板编译 + IC 投机(选定)** | +1-2 人年(模板×2 架构 + 系统管线 + OSR) | 消除 dispatch/译码 + 热点 f64 直算,拿到「trace 收益的 ~70%」(流水线图) | **选定** |
| 方法级优化编译器(SSA IR + regalloc,DFG/TurboFan 档) | 数人年起(IR 设计、regalloc、调度、与 deopt 耦合) | 在 P4 之上的边际:主要是栈槽→寄存器与跨指令优化;但列内核校准已示顶档之间差距极小(LuaJIT vs luajc 仅 6%,[roadmap](./roadmap.md) (§1)) | 否决:边际收益小、人年成本倍增、deopt 机器被迫升级成 snapshot 级(§3.3) |
| 直接做 trace JIT(跳过 P4) | +2-4 人年开放式(P5) | 上限最高 | 否决:违反「每阶段独立交付」(原则 3);P5 自身定位就是「只在 P4 收益不够时启动」([roadmap](./roadmap.md) (§4)) |

核心论证:**实现成本与收益在这条曲线上严重凸性**。模板编译用最少的人年拿走最大的一块(dispatch 税 + 类型投机),且其简单性**向下传导**——无跨指令寄存器分配 ⇒ deopt 不需要 snapshot 机器(§3.3),这是「deopt 简单」的结构性来源,不是偷工减料。

### 1.4 边界表:P4 做什么 / 不做什么

| 做(P4 边界内) | 不做(留给 P5 或永不做) |
|---|---|
| per-opcode 模板发射,函数为编译单元 | 跨函数内联(P5 trace 内联) |
| IC 反馈类型投机:f64 快路径 + guard(§2) | 投机失败的细粒度恢复(P5 snapshot) |
| 函数级 OSR exit 回解释器(§3) | 循环不变量外提 / CSE / 分配下沉(P5 IR 优化) |
| 栈槽直存直取(值住 arena) | 跨指令寄存器分配(P5) |
| amd64 + arm64 双后端(§5) | 其余架构(留给 P3 Wasm 层或不支持,§6) |
| 沿用 P2 可编译性闸门(F1..F7,[p2-bridge](./p2-bridge.md) §4.3) | 为 varargs/coroutine/debug 补编译路径(原则 4:走 fallback 不做完备性) |

---

## 2. 类型投机:IC 反馈 → f64 快路径 + guard

### 2.1 供料链:P1 写、P2 聚合、P4 消费

P4 的投机不自己采样,**直接消费 P2 的 `TypeFeedback`**([p2-bridge](./p2-bridge.md) §3.4/§7.2)。三棒接力在 P1/P2 文档早已铺好:

```
P1 解释器:算术 IC 写 numHits/metaHits(「写而不读」,05 §6.4)
            表/全局 IC 记 kind + 命中分布(05 §6.3)
   ↓
P2 聚合:TypeFeedback{ FBArithStableNumber / FBTableMono / FBTableMega / …, confidence }
   ↓
P4 投机:confidence 高 ⇒ 按 feedback 发投机模板;低 / FBTableMega ⇒ 发通用模板(等价解释器语义)
```

按 feedback 种类的投机动作(概念,均为「快路径 + guard,失败 OSR exit」):

| feedback([p2-bridge](./p2-bridge.md) §3.4) | 投机模板发什么 | guard 检查什么 |
|---|---|---|
| `FBArithStableNumber`(算术点恒 number) | 直接 f64 运算指令(如 `mulsd`/`fmul`)+ NaN 规范化 | 两操作数 `IsNumber`(NaN-box 单次 u64 无符号比较,[01](./p1-interpreter/01-value-object-model.md) §3.2)|
| `FBTableMono`(表点形状稳定) | 代次比对 + 直达槽 load/store(把解释器 IC 命中路径内联成机器码,免 IC 结构往返) | 同表 + 同代次([05](./p1-interpreter/05-interpreter-loop.md) §6.2/§6.3) |
| `FBGlobalStable`(全局读恒定) | 常量化 / 直达 globals 槽 | globals 代次 |
| `FBSelfMono`(方法点单态) | 内联方法查找结果 | metatable 代次 |
| `FBUnstable` / `FBTableMega` | **不投机**:发通用模板(调用解释器同款慢路径 helper) | 无 guard(语义完备) |

**注意 guard 的语义边界**:guard 只验证「投机前提」,验证失败**不是错误**,是回到全语义路径——这与 [12](./p1-interpreter/12-testing-difftest.md) §7 点名的「guard 漏判 ⇒ 静默错果」相对:guard **多判**(过于保守)只损性能,**漏判**(该查没查)直接产出错误结果且不崩溃,是 JIT 第一危险源,防线见 §7.2。

### 2.2 guard 形态的硬约束:显式检查,无信号陷阱

LuaJIT/V8 大量用硬件陷阱实现零成本 guard(让非法访问 SIGSEGV,信号处理器接管恢复)。**纯 Go 下此路不通**:Go runtime 拥有信号处理(SIGSEGV/SIGBUS/SIGFPE),落在非 Go PC 上的 fault 无法恢复,直接 fatal——这是 [roadmap](./roadmap.md) (§2) 四项税同族的「runtime 所有权」约束。wazero 同样全程显式边界检查,从不依赖陷阱。

**定稿:P4(及 P5)所有 guard 都是显式「比较 + 条件跳」**,每 guard 2-3 条指令的恒定成本。这压低了生成码密度的天花板(LuaJIT 同等 guard 免费),是「纯 Go 逼近而非追平 LuaJIT」的微观注脚之一——量化上已被前提一的 6% 校准吸收。

### 2.3 与 P2/P3 零 deopt 的分野:状态机加一条边

P2 精心构造的「零 deopt 状态机」([p2-bridge](./p2-bridge.md) §5/§6:升层单向、`TierGibbous` 是吸收态)在 P4 被**有意打破**——投机引入了「运行期假设可能被打破」,deopt 边必须出现:

```
P2/P3 状态机(零 deopt):                 P4 状态机(投机,新增两条边):
TierInterp ──► TierGibbous(吸收态)       TierInterp ──► TierGibbousJIT
TierInterp ──► TierStuck  (吸收态)            ▲                │ guard 失败(OSR exit)
                                              │ 再训练后重编译    ▼
                                          TierInterp(deopt 着陆,继续解释 + 重新积累 feedback)
                                              │ 反复 deopt 超阈值
                                              ▼
                                          TierStuck-speculation(该 Proto 拉黑投机:
                                          只发通用模板,或永久解释)
```

对照表(承 [p2-bridge](./p2-bridge.md) §5.1 的表再延一列):

| | try-compile(P2/P3) | **投机 JIT(P4)** | trace JIT(P5) |
|---|---|---|---|
| 运行期假设 | 无 | 有(类型稳定) | 有且更重(路径稳定) |
| deopt 机器 | 不需要 | **函数级 OSR exit(§3)** | 精细 snapshot(P5 文档) |
| 假设错的代价 | 不存在 | 慢(exit + 再训练),不错 | 慢,不错 |
| 状态机 | 单向无环 | 有 `gibbous→interp` 边 | 同 P4 + trace 黑名单 |

P2 的可编译性闸门(F1..F7)**原样沿用**:P4 仍只编译静态可编译子集,投机叠加在这个子集**之内**——「不可编译形状走 fallback」与「可编译形状内做类型投机」是正交的两层(原则 4 不因 P4 而松动)。

---

## 3. OSR exit 概念设计:deopt 为什么「简单」

### 3.1 函数级 exit 的语义

**Exit 的单位是当前函数帧**:guard 失败时,该帧的**剩余执行整体交还解释器**——从 exit 对应的字节码 pc 起,由 crescent 继续解释到函数返回;调用链上层帧不受影响(各自维持自身 tier)。不做「编译码内恢复后继续跑编译码」,不做跨帧恢复。这就是 [roadmap](./roadmap.md) (§4)「deopt 简单(函数级 OSR exit 回解释器)」的展开。

### 3.2 物化 = memmove:值表示同一份的红利

Exit 要把「机器状态」变回「解释器状态」。解释器状态 =(arena 值栈槽 + CallInfo + pc)([05](./p1-interpreter/05-interpreter-loop.md) §1)。因为 P4 生成码与解释器**读写同一块 arena、同一种 NaN-box 编码**([01](./p1-interpreter/01-value-object-model.md) §7 不变式 1「值即 8 字节,跨 tier 拷贝是 memmove」),物化没有任何格式转换:把暂存寄存器里的 NaN-box u64 写回栈槽即是全部。**这是第一天值表示承诺([design-premises](../../llmdoc/must/design-premises.md) 前提四)在 P4 兑付的现金**:若 P1 当年选了 Go tagged struct,这里就要做「机器表示→Go 对象」的重建,deopt 机器复杂度直接上一个量级。

### 3.3 「栈槽真相」不变式:模板编译让 snapshot 不必存在

P4 的 deopt 之所以能薄到几乎没有,靠的是模板编译的结构性质,**升格为 P4 不变式**:

> **每条字节码边界处,全部 Lua 活值都已物化在 arena 值栈槽中**(模板把结果 store 回栈槽才结束);机器寄存器只在单条模板内部短暂持值。

推论:guard 布在模板**开头**(操作数检查先于任何副作用)⇒ guard 失败瞬间,栈槽即完整解释器状态,**待物化集合为空**。OSR exit 退化为:

```
osrExit(exitPC):
  1. 把 exitPC 写回当前 CallInfo(05 §1.2 的 pc 字段;JIT 执行期间不实时维护 pc,
     只在 exit 点按编译期记录的「机器地址→字节码 pc」映射回填)
  2. 记 deopt 统计(§3.4 再训练用)
  3. 经 trampoline 退出 JIT 世界(§4.2),解释器 reloadFrame 后从 exitPC 续跑(05 §1.3)
```

对照 P5:trace JIT 的优化把值留在寄存器、把分配下沉、把多帧内联——deopt 必须靠 snapshot 记录「IR 值→栈槽」映射并重建多帧。**P4 用「不优化跨指令」换掉了整台 snapshot 机器**——这正是 §1.3 说的「简单性向下传导」。代价是 P4 生成码保留栈槽内存往返;收益与代价的兑换率由验收(§7.1)裁决。

> 实现自由度:若某些模板为性能在边界间短暂缓存值(如 FORLOOP 的循环变量驻留寄存器),则相应 guard 的 exit 需补一段「寄存器→栈槽」写回序列(每 exit 点编译期生成,固定几条 store)——仍无需运行期 snapshot 解释器,只是「真相点」从「处处」收窄到「guard 处」。架构决策:**允许此类局部缓存,但 exit 物化序列必须编译期静态生成**,杜绝运行期映射查询。

### 3.4 exit 之后:再训练与防 deopt 风暴

| 事件 | 动作 |
|---|---|
| 单次 guard 失败 | OSR exit 回解释;该 Proto 的 deopt 计数 +1;解释执行继续写 IC(feedback 自然更新——「恒 number」假设被新观测稀释) |
| deopt 计数超低阈值 | 丢弃该编译产物,回 `TierInterp` 重新积累 feedback;再热后**重编译**,失效的投机点按新 feedback 降级为通用模板(去投机重编) |
| 重编译后仍反复 deopt | 标 `TierStuck-speculation`:该 Proto 永久只发无投机的通用模板(仍比解释快——dispatch 税照省),或干脆永久解释。**吸收态,防抖**(同 [p2-bridge](./p2-bridge.md) §6.3 的不重试纪律) |

阈值数值与 [p2-bridge](./p2-bridge.md) §2.5 同款待定:依赖真实负载校准,只影响时机不影响正确性。

---

## 4. 四项税的全额兑现(系统管线,wazero 采石场)

P3 把四项税外包给 wazero;P4 生成原生码,**四项税全额自付**。[roadmap](./roadmap.md) (§2) 的标准解法逐项落成概念方案,wazero(Apache 2.0)是每一项的参考实现采石场:

### 4.1 逐税概念方案

| 税([roadmap](./roadmap.md) §2) | P4 概念方案 | P1 已铺垫什么 | wazero 对应参考 |
|---|---|---|---|
| **GC 精确栈扫描**(JIT 帧无 stack map) | JIT 代码跑**自管机器栈**(Go 堆分配的 `[]byte`,trampoline 切 SP 进入);Lua 值帧本就在 arena 值栈 + CallInfo;Go 栈对 JIT 世界不可见,边界按 syscall 语义(进入 JIT = Go 栈停在可扫描的已知点) | [05](./p1-interpreter/05-interpreter-loop.md) §7「调用链状态全在 arena,Lua 调用不吃 Go 栈」——JIT 帧沿用同一 CallInfo 协议 | 自管 Go-allocated 栈 + 进入/退出汇编(compiler engine 的 native call 栈切换) |
| **异步抢占**(信号可落任意 PC) | 生成码在**循环回边插抢占检查点**:load `jitContext.preemptFlag`,置位则经 exit stub 退到边界(GC safepoint / 调度让出共用此点);直线段长度有界 ⇒ 不可抢占窗口有界 | [p2-bridge](./p2-bridge.md) §2.3 已确认回边是 P3/P4 翻译时自插检查点(不靠 P1 伪指令) | epoch interruption / checkexit 标志检查的生成模式 |
| **栈移动**(morestack 拷 goroutine 栈) | JIT 代码**不持任何 Go 栈指针**:进入前所有所需指针(arena base、值栈 base、helper 表)装入 Go 堆上的 `jitContext`,经固定寄存器传入;Go 堆对象不移动,故 context 指针稳定 | [01](./p1-interpreter/01-value-object-model.md) §2「GCRef 非 Go 指针」纪律同源 | trampoline 只传 module context 指针,生成码经 context 间接寻址 |
| **写屏障**(裸指针写破三色不变式) | **白赚**:值世界已在 arena,JIT 写栈槽/表槽写的是自管内存里的 u64,Go GC 不可见,无屏障义务 | P1 第一天承诺的直接红利([design-premises](../../llmdoc/must/design-premises.md) 前提四) | linear memory 即同款自管值世界 |

### 4.2 系统管线四件套(exec mmap / W^X / icache / trampoline)

| 件 | 概念方案 | wazero 参考指针 |
|---|---|---|
| **exec mmap** | 匿名 `mmap(PROT_READ\|PROT_WRITE)` 写入代码 → `mprotect(PROT_READ\|PROT_EXEC)` 翻面;代码页自管池化(per-Proto 段 + 释放策略) | `internal/platform` 的 MmapCodeSegment / 各 OS 变体 |
| **W^X** | 任何时刻不持 RWX 页;macOS arm64 走 `MAP_JIT` + `pthread_jit_write_protect_np` 等价物 | platform 层对 darwin/arm64 的 MAP_JIT 处理 |
| **icache flush** | arm64 写码后必须显式 flush(`IC IVAU`/`DC CVAU` 序列,经汇编 stub);amd64 硬件保证一致性,无操作 | arm64 发射路径上的 cache flush 汇编 |
| **trampoline** | 一对汇编 stub:**进**=保存 Go 被调方寄存器、切 SP 到自管机器栈、装 `jitContext` 入固定寄存器、跳入代码;**出**=反向恢复(正常返回 / OSR exit / 慢路径 helper 调用三种出口共用) | compiler engine 的 native entry 汇编(`arch_amd64.s` 等) |

### 4.3 JIT 执行上下文与世界边界(概念图)

```
   Go 世界(goroutine 栈,正常 Go 帧)            自管世界(Go 堆 + arena)
  ┌──────────────────────────┐                ┌────────────────────────────────┐
  │ Program.Call / bridge     │  trampoline 进 │ 自管机器栈([]byte):spill/返址  │
  │ (升层决策、编译、装载)      │ ─────────────► │ jitContext:arenaBase/preempt/  │
  │                          │ ◄───────────── │   helper 表 / exit 原因码        │
  │ 慢路径 helper(Go 函数:    │  trampoline 出 │ arena:值栈 + CallInfo + 对象     │
  │  元方法/分配慢路径/抛错)    │  (3 种出口)    │   ——与 crescent 同一块内存       │
  └──────────────────────────┘                └────────────────────────────────┘
```

关键纪律(升格为不变式):

1. **JIT 码内不调用任何普通 Go 函数**——需要 Go 侧能力(元方法分派、arena 扩容、抛错、host call)一律经 trampoline 出去,以「慢路径 helper」形式回 Go 世界执行,返回后重入。helper 表地址在 `jitContext` 中。
2. **arena base 在两个 safepoint 之间稳定**:arena 搬迁(扩容)只发生在分配慢路径(= 出了 JIT 世界);JIT 内联的 bump 分配快路径越界即出去,回来后从 context 重载 base。这是 [05](./p1-interpreter/05-interpreter-loop.md) §1.3「重载 stk」纪律在机器码层的同构。
3. **混层调用走统一 CallInfo 协议**:JIT 函数 CALL 目标若也有 JIT 码,同世界内直跳;若是解释 tier,经 trampoline 出去由 crescent 执行,RETURN 后回来——协议即 P3 的解释器↔编译层互调协议([p3-wasm-tier](./p3-wasm-tier.md)),只是被调方从 wazero 换成本地码。

---

## 5. amd64 + arm64 双后端

### 5.1 后端抽象:结构共享、发射分架构

两个候选:

| 候选 | 形态 | 权衡 |
|---|---|---|
| (a) 架构中立宏汇编器 | 模板用统一「虚拟指令」写一遍,宏汇编器翻两架构 | 写一次爽,但中立层会泄漏(寻址模式/标志位/寄存器约定差异),生成码质量与可调试性都受损 |
| **(b) 共享骨架 + per-arch 发射器(选定)** | 编译器骨架(线性扫描、pc→地址映射、guard/OSR 接线、feedback 决策)架构无关;每 opcode 的发射函数按架构各实现一份,后接小型指令编码器(emitter) | 代码量 ≈ 模板数 ×2,但每架构可写出地道指令序列;wazero 同款组织(共享 IR 化骨架 + amd64/arm64 各自 compiler) |

选 (b) 的决定性理由:P4 的全部价值在生成码质量与系统管线正确性,中立宏层在这两点上都是负资产;且模板总量可控——[02](./p1-interpreter/02-bytecode-isa.md) 的 ISA 共 38 个 opcode,加上 guard/exit/trampoline 胶水,**每架构数十段发射函数**,在 +1-2 人年预算内。

共享与分架构的切分:

| 架构无关(写一次) | per-arch(各一份) |
|---|---|
| 编译驱动:线性扫字节码、标签/回填、IC feedback → 模板选型 | 每 opcode 发射函数(指令选择) |
| guard 语义与 OSR exit 物化序列的**逻辑**(§3.3) | guard 比较/条件跳的**指令**、exit stub |
| `jitContext` 布局、helper 表、调用协议 | 寄存器约定(context 固定寄存器、暂存分配)、trampoline 汇编 |
| 代码页管理、W^X 策略 | icache flush(arm64)、指令编码器 |

### 5.2 双架构测试纪律

- **差分门禁双跑**:[12](./p1-interpreter/12-testing-difftest.md) §8 的 CI 门禁在 amd64 与 arm64 物理 runner 上**各跑全套**(同 Proto crescent vs gibbous-jit byte-equal)。交叉编译只能保证能构建,不能代跑差分——CI 必须有真 arm64 机器。
- **先 amd64 后 arm64,但骨架先行**:第一架构落地时就按 (b) 切分好接口,避免「amd64 写完再抽象」的返工。arm64 验证抽象是否真架构无关。

---

## 6. P3 Wasm 层的去留(决策框架)

[roadmap](./roadmap.md) (§4) P4 验收给了两个选项:「Wasm 层退役,**或**留作可移植中层(未移植架构、禁 exec-mmap 环境)」。这是 P4 上线时必须做的决策,本节给框架而非结论。

### 6.1 决策输入,含一个关键拆穿

「留作可移植中层」的表面论据是:P4 只做 amd64/arm64,且需要 exec mmap;其余架构与禁 exec 环境(iOS、受限沙箱)由 P3 兜底。**但要拆穿一层:wazero 的编译引擎与 P4 共享同一组平台约束**——它同样只有 amd64/arm64 后端、同样需要 exec mmap。在「P4 不可用」的环境里,wazero 只剩**解释模式**(逐条解释 Wasm)。于是真正的比较是:

> 在 P4 不可用的平台上:**crescent 直接解释 Lua 字节码** vs **wazero 解释模式解释「从 Lua 字节码翻译来的 Wasm」**——后者隔了一层翻译再解释,大概率不快反慢。

### 6.2 决策矩阵

| 环境 | P4 可用? | P3(wazero 编译模式)可用? | 该环境下最优层 |
|---|---|---|---|
| linux/darwin/windows amd64/arm64,允许 exec | ✅ | ✅(但被 P4 替代) | **gibbous/jit** |
| riscv64 / ppc64le / s390x 等 Go 支持架构 | ❌ | ❌(wazero 编译引擎同样不支持 → 解释模式) | **crescent**(大概率;待实测) |
| 禁 exec-mmap(iOS、seccomp 沙箱等) | ❌ | ❌(同上 → 解释模式) | **crescent**(同上) |

### 6.3 缺省倾向与决策时点

- **缺省倾向:P4 验收通过后,P3 退役**(代码留在版本史,difftest 移除 wasm runner,降低差分矩阵与维护面)。留下的唯一翻案条件:实测证明「wazero 解释模式跑翻译后的 Wasm」在某类真实负载上仍显著快于 crescent(可能性低),或出现真实宿主需求(如 iOS 嵌入)且实测翻案。
- **结构上不预设结论**:`gibbous/wasm` 与 `gibbous/jit` 同 tier 同接口([architecture](./architecture.md) §1 的包布局已并列;P2 的 `P3Compiler` 接口对两后端同形,[p2-bridge](./p2-bridge.md) §7.1),**共存的结构自由度零成本保留**——这正是架构决策与策略决策的分离:结构允许共存,策略按上表数据裁剪,决策推迟到 P4 验收时用数据定,记入 [doc-gaps](../../llmdoc/memory/doc-gaps.md)。
- 若走了 §0.2 的跳跃路径(P3 从未存在),本节自动消解。

---

## 7. 验收与差分接入

### 7.1 量化锚点:luajc 档的精确含义

[roadmap](./roadmap.md) (§1) 校准测量 1(Horner 5 次多项式,1000 items,同机同日 A/B):

| 档位 | 绝对值 | 相对 gopher-lua |
|---|---|---|
| gopher-lua | 729μs | 1x(基线) |
| LuaJ-luajc | 164μs | ≈4.4x |
| LuaJIT | 154μs | ≈4.7x |

**P4 验收 = 列内核负载达到 164μs 那一档的水位**(同等工作量基准上 ≥ 该档)。而 LuaJIT 只比 luajc 快 6%——**达标即「逼近 LuaJIT 档」**,项目近期目标([roadmap](./roadmap.md) §0)在 P4 兑现。注意坐标系([evolution-roadmap](../../llmdoc/architecture/evolution-roadmap.md) 速查表警告):流水线图的「trace 收益 ~70%」是另一坐标——它说的是 P4 能拿到 trace JIT 理论收益的约七成,剩余 ~30% 是 P5 的理论边际(P5 文档 §0 接着用这个锚点)。

基准必须是列内核形状([12](./p1-interpreter/12-testing-difftest.md) §6.1 硬约束):一次 Call 进 VM 整批迭代——per-item 形状下边界成本主导,测不出 P4(前提一/前提二)。

### 7.2 差分:投机错误的主防线([12](./p1-interpreter/12-testing-difftest.md) 接入)

P4 是望舒第一个**会说谎的层**——投机错误(guard 漏判)静默产错果、不崩溃、不报错,有限用例测不出([roadmap](./roadmap.md) (§5) 原则 2 点名其为 JIT 最危险 bug 类)。防线全部在 [12](./p1-interpreter/12-testing-difftest.md) 已铺好的轨道上加车:

| 机制 | 内容 |
|---|---|
| **同 Proto 差分**(主防线) | [12](./p1-interpreter/12-testing-difftest.md) §3.8 Runner 抽象新增 `WangshuGibbousJIT`:同一 Proto 走 crescent vs gibbous-jit,输出 O1..O5 byte-equal;持续 fuzz;CI 硬门禁([architecture](./architecture.md) §4 不变式 2) |
| **OSR 状态等价专项** | [12](./p1-interpreter/12-testing-difftest.md) §7 P4 行预留:guard 失败 exit 后的最终输出,与「同输入一路解释」byte-equal——验证物化(§3.3)与着陆(§3.1)无损 |
| **deopt 注入模式** | deopt 路径天然触发稀疏,被动 fuzz 覆盖不足:提供「每个 guard 强制失败一次 / 每 N 次失败一次」的测试构建(V8 `--deopt-every-n` 同款思路),让 fuzz 把每条 exit 路径都踩热 |
| **GC 压力 fuzz 延伸** | [12](./p1-interpreter/12-testing-difftest.md) §5 的高频 GC 模式叠加 JIT:专打「JIT 持 arena base 跨 safepoint」「回边检查点漏布」类时序 bug |
| **双架构双跑** | §5.2,amd64/arm64 各自全套 |

---

## 8. 风险与开放问题

**风险:**

1. **人年级投入的中途校验**:+1-2 人年是 P1-P3 总和级别的投入。设中途闸门:单架构(amd64)+ 仅算术投机的最小 P4 先打通全管线并测 Horner 档位,若距 luajc 档仍远(说明瓶颈不在 dispatch/投机),立即停下重评——这是原则 3「任何闸门停下不亏」在 P4 内部的套用。
2. **无信号陷阱的密度天花板**(§2.2):全显式 guard 的成本若实测吃掉投机收益的大头,f64 快路径的净收益要重新核算(缓解:guard 合并——同一操作数在直线段内只查一次,这是不引入 IR 前提下可做的窥孔级优化)。
3. **Go 调度交互的未知数**:长直线段 + 回边检查点粒度不当 ⇒ GC STW 延迟毛刺;检查点过密 ⇒ 吃性能。布点密度需实测调,且行为随 Go 版本演进(非公开契约,wazero 的跟进历史是预警源)。
4. **代码体积与 icache**:模板展开比 Wasm/解释器字节码膨胀一个量级,热函数过大时 icache 压力反噬(缓解:P2 F5 的大函数闸门已天然限制编译单元尺寸)。
5. **arm64 维护矩阵**:双后端 + 双架构 CI 是长期固定成本;若资源紧张,arm64 滞后交付不阻塞 P4 验收(验收平台定 amd64),但发布口径须如实标注。

**开放问题(记入 [doc-gaps](../../llmdoc/memory/doc-gaps.md)):**

- OSR exit 着陆粒度的终稿:纯「guard 即栈槽真相」vs 允许循环变量寄存器驻留 + 静态物化序列(§3.3 的注),待 amd64 原型实测定。
- deopt 再训练阈值、去投机重编译的具体策略(§3.4)与 P2 `ProfileData` 的字段扩展(deopt 计数挂哪),依赖 P2 落地后回填。
- 编译执行的线程模型:升层触发线程同步编译(模板编译微秒级,可能够用)vs 后台 goroutine 编译 + 安装屏障——继承 P3 的同款决策([p3-wasm-tier](./p3-wasm-tier.md)),若 P3 被跳过则 P4 自决。
- 多 State 并发下 JIT 代码与 profile 的共享语义(承 [p2-bridge](./p2-bridge.md) §9 同款并发缺口)。
- `jitContext`/自管机器栈的精确布局、helper 表 ABI、trampoline 寄存器约定——详细设计阶段展开(每架构一篇)。
- P3 去留的最终裁决(§6.3):P4 验收时用数据定。

---

相关:[roadmap](./roadmap.md)(§4 P4 定义 / §2 四项税 / §1 校准测量 / §7 prior art) ·
[architecture](./architecture.md)(§1 包布局 `internal/gibbous/jit` / §2 tier 映射 / §4 三不变式) ·
[p2-bridge](./p2-bridge.md)(§3.4 TypeFeedback / §7.2 P4 供料接口 / §5 零 deopt 对比基线) ·
[p3-wasm-tier](./p3-wasm-tier.md)(被继承的分层结构与互调协议;§6 去留决策的对象) ·
[p5-trace-jit](./p5-trace-jit.md)(下一站:P4 收益不够时的开放式选项) ·
[01-value-object-model](./p1-interpreter/01-value-object-model.md)(NaN-box 编码——生成码直接操作 / §7 值表示不变式) ·
[02-bytecode-isa](./p1-interpreter/02-bytecode-isa.md)(源 ISA / §7 IC slot) ·
[05-interpreter-loop](./p1-interpreter/05-interpreter-loop.md)(§1 CallInfo / §7 调用协议——OSR 着陆面) ·
[12-testing-difftest](./p1-interpreter/12-testing-difftest.md)(§3.8 Runner 抽象 / §7 P4 行 / §8 CI 门禁) ·
[evolution-roadmap](../../llmdoc/architecture/evolution-roadmap.md)(速查行 / 坐标系警告) ·
[design-premises](../../llmdoc/must/design-premises.md)(四项税 / 值表示承诺 / 五原则)
