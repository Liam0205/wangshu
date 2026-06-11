# P2:分层桥(热度计数 / IC 反馈 / 可编译性分析 / try-compile-fallback)

> 状态:**设计阶段,详细设计(依赖 P1 落地后细化)**。本文比 P1 子系统文档(`p1-interpreter/`)
> 略前瞻——它依赖 P1 已交付的 IC slot、Proto、CallInfo、AST 与解释器主循环;**前瞻性结论标注
> 「依赖 P1 落地后定稿」**。本文是 P2「分层决策机器」的单一事实源:函数级热度计数、inline cache
> 反馈聚合、静态可编译性分析、try-compile-fallback-interpret 策略与升降层状态机。
>
> 对应 Go 包:`internal/bridge`(热度计数 / IC 反馈 / 可编译性分析 / 升降层决策,见 [architecture](./architecture.md) §1)。
> 上游契约:`docs/design/roadmap.md` (§4 P2 定义、§5 五条原则)、
> [evolution-roadmap](../../llmdoc/architecture/evolution-roadmap.md)(P2 无独立量化验收、tier 映射)、
> [design-premises](../../llmdoc/must/design-premises.md)(前提三原则 1 解释器永不退役 / 原则 4 走 fallback 不做完备性)。
> P1 依赖面:[02-bytecode-isa](./p1-interpreter/02-bytecode-isa.md)(§4 FORLOOP 热点回边、§7 IC slot、§4 编号 38..63 预留)、
> [05-interpreter-loop](./p1-interpreter/05-interpreter-loop.md)(§6 IC 执行、§6.4 算术 IC「P1 写不读纯供料」、§1 CallInfo/Frame)、
> [04-frontend-parser-codegen](./p1-interpreter/04-frontend-parser-codegen.md)(AST 利于可编译性分析)、
> [11-embedding-arena-abi](./p1-interpreter/11-embedding-arena-abi.md)(§1.3 Compile 的可编译性探测 P1 占位)。

---

## 0. 本文在演进流水线中的位置:基建,不是执行层

`docs/design/roadmap.md` (§4) 的流水线:

```
P1 解释器 ──► P2 分层桥 ──► P3 Wasm 编译层 ──► P4 method JIT ──► P5 trace JIT
(2-4x)        (基建)        (4-8x)             (trace 收益~70%)   (10-30x,开放式)
```

**P2 是括号里唯一写「基建」而非倍率的阶段。** 这不是疏漏,是定位:P2 **本身不加速任何脚本**——它在 P1 解释器之上加一台「分层决策机器」,产出三样东西(热度、IC 类型 feedback、可编译性判定)**喂给 P3/P4 编译层**,由编译层去兑现倍率。[evolution-roadmap](../../llmdoc/architecture/evolution-roadmap.md) 的速查表明确:**P2「文档未给独立量化门槛」**——没有「P2 要 ≥Nx」这种验收,因为 P2 不在执行热路径上发力。

这带来一个反直觉但关键的推论:**P2 的「成功」标准不是性能,而是「决策正确」**——

- 热度计数要**不漏报真热点、不误报冷函数**(漏报 = 该编译的没编译,损失上层收益;误报 = 浪费编译预算);
- IC 反馈要**忠实反映运行期类型**(feedback 错会误导 P4 投机,埋下「投机错误静默错果」的雷,违反 `docs/design/roadmap.md` (§5) 原则 2);
- 可编译性分析要**不把不可编译形状判成可编译**(判错 = 编译出错误代码或运行期崩溃,而 try-compile-fallback 的全部安全性依赖这个判定的保守正确)。

> **tier 坐标系警告**([evolution-roadmap](../../llmdoc/architecture/evolution-roadmap.md)):月相 tier 比阶段粗一层。P1=tier-0(crescent),P3/P4=tier-1(gibbous),P5=tier-2(fullmoon)。**P2 不分配月相**——它是基建,不是一个能执行字节码的层。代码包名也据此:执行层用月相(`crescent`/`gibbous`/`fullmoon`),P2 用功能名 `internal/bridge`([architecture](./architecture.md) §1)。日志里**不会**有 `function promoted to bridge`(bridge 不是落点);P2 触发的升层日志是 `function promoted to gibbous`(落点是 gibbous,见 §6.4)。

### 0.1 P2 与 P1/P3 的边界(谁拥有什么)

| 关注点 | P1(crescent)拥有 | P2(bridge)拥有 | P3+(gibbous)拥有 |
|---|---|---|---|
| IC slot 的**写入** | ✅ 解释器命中/失效时写(05 §6) | — | — |
| IC slot 的**读取消费** | P1 读表/全局 IC 加速取值;**算术 IC 写而不读**(05 §6.4) | ✅ 读全部 IC 聚合成类型 feedback(§3) | 读 P2 聚合的 feedback 做投机(P4) |
| **热度计数** | 提供采样点(FORLOOP 回边、函数入口,02 §4) | ✅ 计数器存储 + 阈值判定(§2) | — |
| **可编译性判定** | 提供 AST(04)与 Proto(02);`Compile` 占位恒「可解释」(11 §1.3) | ✅ 静态分析 pass + 判定结果(§4) | 读判定决定编译哪些 Proto(§7) |
| **升降层决策** | — | ✅ 状态机:何时升、何时不升、不降层(§6) | 接受「编译此 Proto」请求,产出 gibbous 代码 |
| **实际编译/执行加速** | 解释执行(永不退役,fallback 落点) | ❌ **不加速** | ✅ 字节码→Wasm,兑现倍率 |

**一句话**:P1 产料(IC 写、采样点、AST),P2 加工成决策(热度、feedback、可编译性),P3 消费决策去加速。P2 是中间的「决策加工厂」,自己不进热路径。

---

## 1. P2 的总数据流

```
                P1 解释器执行期(crescent)
                 │            │              │
       FORLOOP 回边 /      IC slot 命中      Compile 时 AST
       函数入口采样         /失效写入         (04 产出)
                 │            │              │
                 ▼            ▼              ▼
        ┌──────────────┐ ┌──────────┐ ┌──────────────────┐
        │ 热度计数器     │ │ IC 反馈   │ │ 可编译性分析 pass │   ← internal/bridge
        │ (Proto 旁/CI) │ │ 聚合器    │ │ (AST/Proto 上)   │     (P2 本体)
        │  §2           │ │  §3      │ │  §4              │
        └──────────────┘ └──────────┘ └──────────────────┘
                 │            │              │
                 └────────────┴──────┬───────┘
                                     ▼
                        ┌──────────────────────────┐
                        │  升降层决策状态机 §6        │
                        │  热 && 可编译 ⇒ try-compile│
                        │  不可编译 ⇒ 永久解释        │
                        │  编译失败 ⇒ fallback 解释   │
                        │  零 deopt(不降层)         │
                        └──────────────────────────┘
                                     │
                      ┌──────────────┴──────────────┐
                      ▼ (可编译 + 热)                ▼ (不可编译 / 冷 / 失败)
              请 P3 编译该 Proto              留在 P1 解释器
              (产出 gibbous 代码,§7)         (永不退役,原则 1)
```

四条输入(热度、IC 反馈、AST、Proto)汇聚到一台状态机,输出二元决策:**「请 gibbous 编译」或「留在 crescent 解释」**。下面逐块定稿。

---

## 2. 函数级热度计数(loop back-edge 计数)

### 2.1 采样点:为什么是回边 + 函数入口(承 02 §4)

[02](./p1-interpreter/02-bytecode-isa.md) §4 已点名 **`FORLOOP`(及循环体内向后 `JMP`)是 P2 热度计数的 back-edge 采样点**。为什么选回边而非「每条指令计数」:

- **回边是「循环又转了一圈」的信号**——热点的本质是「同一段代码反复执行」,循环回边精确捕捉这个重复,而绝大多数 CPU 时间花在循环里(列内核负载尤甚,`docs/design/roadmap.md` (§1) 的 Horner 形状就是一个紧循环)。每指令计数则把直线代码也算进去,信噪比低且开销大(每条指令一次自增)。
- **函数入口计数捕捉「被反复调用的小函数」**——有些热函数不含大循环,但被外层循环每轮调用(如比较器、map 回调)。只数回边会漏掉它们;补一个函数入口计数器,让「调用 N 次」也能触发升层。

**两个计数器,两类热点**:

| 计数器 | 自增点 | 捕捉的热点形态 | 对应 opcode([02](./p1-interpreter/02-bytecode-isa.md) §4) |
|---|---|---|---|
| **back-edge 计数** | 每次循环回跳 | 「函数内有热循环」 | `FORLOOP` 成功回跳、循环体内向后 `JMP`、`TFORLOOP` 后回跳 JMP |
| **入口计数** | 每次进入该函数帧 | 「函数被反复调用」 | `CALL`/`TAILCALL` 进入 Lua 帧时(05 §7.1 `enterLuaFrame`) |

> **back-edge 计数 vs 总指令数**:LuaJIT/V8 都用回边(loop back-edge)+ 调用计数做热度,不数总指令——这是 prior art 的成熟选择(`docs/design/roadmap.md` (§7) JSC Baseline/V8 Ignition 阶梯)。望舒沿用。

### 2.2 计数器存哪:Proto 旁 profile 数据 vs CallInfo

计数器的存储位置有两个候选,**各管一类计数**:

| 候选 | 存储 | 适合 | 权衡 |
|---|---|---|---|
| **Proto 旁 `ProfileData`(选定主存储)** | Go 堆,与 `Proto` 同生命周期(Proto 住 Go 堆,01 §1) | **入口计数 + 该 Proto 内各回边的累计计数** | 计数是「函数级聚合」语义——「这个函数总共热不热」,天然挂 Proto;Proto 不进 arena、不被 GC(01 §1),计数器随之稳定,无 GC 干扰 |
| **CallInfo 内的帧级计数** | arena(CallInfo 数组,05 §1.2) | 单次调用内的回边计数(帧活跃期) | 帧退出即销毁,**不跨调用累积**;只在「想分析单次长循环」时有用,P2 主决策不需要 |

**定稿:主存储是 Proto 旁的 `ProfileData`(Go 堆),按 pc 索引各回边计数 + 一个入口计数。** CallInfo 不存累积热度(它是 per-call 临时记录,05 §1.2)。理由:

- 升层决策是**函数级**的(「把这个 Proto 升到 gibbous」),判据应是「这个 Proto 历史上总共多热」,而非「这一次调用多热」——累积语义匹配 Proto 级存储。
- Proto 与 `State` 同生命周期(11 §1.4),计数器一路累积到阈值,跨多次 `Program.Call` 也连续(列内核典型:同一脚本被反复 Call 处理不同批次,热度应累加)。

```go
// internal/bridge —— 挂在 Proto 旁的画像数据(Go 堆,与 Proto 同生命周期)
//
// 不进 arena:计数是函数级聚合,与不可变代码同住 Go 堆(01 §1);无 GC 干扰。
// 按 pc 索引回边计数,避免与 02 §7 的 IC slot 数组混淆(IC 是值加速,profile 是热度)。
type ProfileData struct {
    entryCount   uint32          // 函数入口计数(每次 enterLuaFrame 自增,§2.1)
    backEdge     []uint32        // 按回边 pc 索引的回跳计数(稀疏:只有回边 pc 有值)
    tierState    TierState       // 当前升层状态(§6 状态机)
    // 升层尝试历史(防反复重试已失败的编译,§6.3):
    compileTried bool            // 是否已尝试过编译
    compilable   Compilability   // 可编译性判定缓存(§4 的结果,首次分析后缓存)
}

// 与 02 §7 ICSlot 数组的关系:ICSlot[] 按 pc 索引(值加速 + 类型 feedback,§3),
// ProfileData.backEdge[] 也按 pc 索引(热度),但语义正交,分两个数组。
```

### 2.3 计数策略:profile counter 伪指令 vs 旁路计数(权衡 02 的 38..63 预留)

[02](./p1-interpreter/02-bytecode-isa.md) §4 末:**「编号 38..63 预留:P2/P3 可能新增 tier guard、profile counter 等伪指令」**。计数自增有两种实现路线,这是 P2 的一个核心权衡:

**路线 A:profile counter 伪指令(占用 38..63 一个 opcode)**

codegen 在每个回边前插一条 `PROFILE_BACKEDGE`(假想 opcode #38),解释器执行它时自增计数 + 检查阈值:

```
L0: ... 循环体 ...
    PROFILE_BACKEDGE  R_x  -> threshold_handler   ; 伪指令:自增 + 越阈值则触发升层检查
L1: FORLOOP R2 -> L0
```

- **优点**:计数逻辑显式在字节码里,dispatch 时自然执行;阈值检查点明确;**与 P3 翻译对齐**——P3 把字节码翻成 Wasm 时,这条伪指令翻成「Wasm 里的回边计数 + 抢占检查」(P3 §6 回边检查点),P1 的伪指令位置正好是 P3 要插检查点的位置。
- **代价**:① **改字节码 = 改 Proto 形状**——会影响 [02](./p1-interpreter/02-bytecode-isa.md) §8 的黄金字节码([12](./p1-interpreter/12-testing-difftest.md) §2.5 对拍官方 luac 的寄存器分配同构),需要 P2 的字节码变体与 P1 基线区分(差分 harness 要能跑「带 profile 伪指令」与「不带」两种 Proto 都 byte-equal 于解释结果);② 每回边多一条 dispatch(即便是冷函数也付这个税)。

**路线 B:旁路计数(解释器在 FORLOOP 执行侧直接自增,不改字节码)**

解释器执行 `FORLOOP` 成功回跳时,顺手 `profile.backEdge[pc]++`,**不插任何伪指令**:

```go
// crescent 的 FORLOOP 执行侧(05 §10.1)末尾,P2 启用时追加:
case bytecode.FORLOOP:
    // ...(05 §10.1 的加 step、判界、回跳逻辑)...
    if cont {                                  // 回跳成功
        f.stk[f.base+a]   = value.NumberValue(idx)
        f.stk[f.base+a+3] = value.NumberValue(idx)
        f.pc += SBx(i)
        if vm.profileEnabled {                 // P2 旁路计数(P1-only 时编译期关掉,零开销)
            vm.bridge.onBackEdge(f.proto, f.pc) // 自增 + 阈值检查(§2.4)
        }
    }
```

- **优点**:① **不改字节码**——Proto 形状与 P1 基线完全一致,黄金字节码、寄存器分配同构、差分对拍 luac 全部不受影响([12](./p1-interpreter/12-testing-difftest.md) §2.5);② 计数开关可在编译期/运行期关掉(`profileEnabled`),P1-only 部署零开销。
- **代价**:计数逻辑藏在解释器执行侧(不在字节码里显式可见);P3 翻译时回边检查点要**另行**在「翻译 FORLOOP」时插入(但 P3 本来就要按 `docs/design/roadmap.md` (§2) 在回边插抢占检查点,见 P3 §6,所以这不是额外负担)。

**定稿:P2 选路线 B(旁路计数),保留路线 A 作为 P3 的可选手段。** 理由:

1. **字节码不可动是硬约束的延伸**——[02](./p1-interpreter/02-bytecode-isa.md) §4 强调「38..63 预留**不占用 0..37**,保证 P1 字节码向后兼容上层翻译」。旁路计数让 P1 的 0..37 字节码**一字不改**就能被 P2 计数,最大化向后兼容;伪指令路线则引入一个「P2 字节码变体」,增加差分维度。
2. **零开销可关**——`profileEnabled` 编译期常量(或 build tag)让 P1-only 部署完全不付计数税,符合 `docs/design/roadmap.md` (§5) 原则 3「每阶段独立交付」(P1 不被 P2 拖累)。
3. **P3 的回边检查点是独立需求**——P3 无论如何都要在回边插 GC 抢占检查点(`docs/design/roadmap.md` (§2) 异步抢占税解法),那是 P3 翻译 FORLOOP 时做的事(P3 §6),不依赖 P1 先插伪指令。所以伪指令的「与 P3 对齐」优势其实可有可无。

> **38..63 的最终用途**:P2 旁路计数不占用任何 opcode。**38..63 留给 P3 的 `TIER_GUARD`**(若 P3 需要在字节码层标记「此点已编译,跳到 gibbous」——但 P3 的 trampoline 倾向于在 Proto 元数据里记 tier 而非插字节码,见 P3 §5)。即 38..63 在 P2 阶段**保持全空**,P3 视需要启用。这是「预留即克制」——能不占 opcode 就不占。

### 2.4 热度阈值:触发「尝试升层」

```go
// internal/bridge —— 回边计数与阈值判定
const (
    HotBackEdgeThreshold = 1000   // 回边累计达此值 ⇒ 该函数「热」,触发升层尝试(可调,§2.5)
    HotEntryThreshold    = 200    // 入口累计达此值 ⇒ 同上(被反复调用的小函数)
)

func (b *Bridge) onBackEdge(proto *bytecode.Proto, pc int32) {
    pd := b.profileOf(proto)
    pd.backEdge[pc]++
    // 任一回边累计越阈值 ⇒ 该 Proto 候选升层(避免每次都求和,用单回边越阈值近似「函数热」)
    if pd.backEdge[pc] >= HotBackEdgeThreshold && pd.tierState == TierInterp {
        b.considerPromotion(proto, pd)        // §6:进入升层决策(先查可编译性)
    }
}

func (b *Bridge) onEnter(proto *bytecode.Proto) {
    pd := b.profileOf(proto)
    pd.entryCount++
    if pd.entryCount >= HotEntryThreshold && pd.tierState == TierInterp {
        b.considerPromotion(proto, pd)
    }
}
```

要点:

- **阈值是「尝试升层」的触发,不是「立即升层」**——越阈值后先走 §6 的可编译性检查,可编译才真升,不可编译则标记「永久解释」并停止计数(§6.2)。
- **单回边越阈值近似「函数热」**:不必每次把所有回边求和(开销),只要某一个回边累计够热,就认为函数值得编译(热循环通常集中在少数回边)。
- **阈值数值是可调旋钮**(§2.5),P2 阶段先取保守值,实测调优;**阈值大小不影响正确性**(只影响「何时编译」的时机),与 `docs/design/roadmap.md` (§5) 原则 3 一致(编译只是加速,晚编译只是少赚不出错)。

### 2.5 阈值调优与「编译预算」(依赖 P1 落地后定稿)

- **阈值太低**:冷函数被误判为热,浪费编译时间(P3 编译有成本——字节码→Wasm + wazero 实例化);**太高**:真热点迟迟不升,损失上层收益。
- **编译预算**:P2 可设「单位时间最多升 N 个函数」防编译风暴(一次性大量函数越阈值)。这是 pacing,不是正确性,P2 阶段先不做(STW 式「越阈值即尝试」),按实测加。
- **数值定标**留到 P1+P2 实现后用真实负载(首个宿主的规则脚本)校准——这是典型的「依赖 P1 落地后定稿」项(记 §9 缺口)。

---

## 3. IC 反馈记录:聚合成类型 feedback(承 02 §7、05 §6.4)

### 3.1 P2 读的是 P1 已经写好的 IC slot

关键:**IC 反馈的「写」是 P1 做的,P2 只「读 + 聚合」。** [05](./p1-interpreter/05-interpreter-loop.md) §6.4 已定稿:

- **算术 IC**(ADD 等):P1 快路径靠现场 `IsNumber` 取值,**不读 IC**;但每次命中时 `f.ic[pc].recordArithNumber()` 记录「这个算术点的操作数实际类型」——[05](./p1-interpreter/05-interpreter-loop.md) §6.4 原话「**P1 写它、不读它分支……纯为 P2 类型 feedback 与 P4 f64 投机供料**」。
- **表/全局 IC**(GETTABLE/GETGLOBAL/SETTABLE/SETGLOBAL/SELF):P1 读它加速取值(命中=代次比对+直达槽,05 §6.3),**同时**其命中分布(array hit / node hit / megamorphic)就是类型 feedback。

所以 P2 的 IC 反馈模块是一个**纯读取消费者**:遍历 Proto 的 IC slot 数组([02](./p1-interpreter/02-bytecode-isa.md) §7,按 pc 索引),把每个 slot 的 `kind` 与累计观测聚合成「这个程序点稳不稳定、是什么类型」的判断。

> **为什么 P1 就埋好 IC 写、P2 才读**:这是 `docs/design/roadmap.md` (§3)「编译层是纯增量」在反馈维度的兑现——P1 顺手写下的 IC 观测,P2 零成本复用(不需要 P2 再插桩一遍)。[05](./p1-interpreter/05-interpreter-loop.md) §6.4「算术 IC 纯为 P2/P4 供料」就是这个设计的伏笔:P1 写它时不知道 P2 会怎么用,只是忠实记录;P2 来了直接读。

### 3.2 IC slot 携带的原始观测(P1 写入的)

[02](./p1-interpreter/02-bytecode-isa.md) §7 / [05](./p1-interpreter/05-interpreter-loop.md) §6.2 的 `ICSlot`:

```go
type ICSlot struct {
    shape    uint32 // 目标 table 的 gen 代次(05 §6.1)
    index    uint32 // 命中槽位
    tableRef uint32 // 身份比对(05 §6.2 回填)
    kind     uint8  // 0 未初始化 / 1 array hit / 2 node hit / 3 mono-metamethod / 4 megamorphic
}
```

P1 在执行中维护这些。P2 关心的**反馈信号**藏在 `kind` 与「命中稳定性」里:

| IC 点类型 | P1 写入的观测 | P2 提取的 feedback |
|---|---|---|
| 算术(ADD/SUB/...) | `recordArithNumber()` 标「本次操作数都是 number」(05 §4.1) | 该点**是否恒为 number**(稳定 number ⇒ P4 可发 f64 快路径 + guard) |
| GETTABLE/SETTABLE | `kind` = array/node hit(单表)或 megamorphic(多表) | 该点**表 shape 是否稳定**(稳定 ⇒ 可投机直达槽;megamorphic ⇒ 别投机) |
| GETGLOBAL/SETGLOBAL | `kind` = node hit(globals 恒为单表,05 §6.4) | 该全局**是否被读成稳定值**(典型:全局函数引用,几乎恒定) |
| SELF(`obj:m()`) | `kind` = node hit(方法常驻 metatable,05 §6.4) | 该方法调用**是否单态**(单态 ⇒ 可内联方法查找) |

### 3.3 算术 IC 需要「计数」而非「布尔」(对 P1 写入的细化)

[05](./p1-interpreter/05-interpreter-loop.md) §6.4 的 `recordArithNumber()` 若只记一个布尔「曾经是 number」,P2 无法区分「恒为 number」与「偶尔是 number、偶尔走元方法」——而这个区分对 P4 投机至关重要(偶尔非 number 的点若被投机成 f64,guard 会频繁失败,投机得不偿失)。

**定稿:算术 IC slot 的 `kind` 字段(或扩展计数)记录类型分布的统计,而非单布尔。** 两种实现:

| 方案 | 记什么 | P2 判定 | 代价 |
|---|---|---|---|
| **(i) 双计数**(选定) | `numHits` / `metaHits` 两个小计数(操作数都是 number 的次数 / 走了元方法的次数) | `numHits / (numHits+metaHits) > 0.99` ⇒ 稳定 number | IC slot 多两个计数字段(回填 02 §7,见 §3.6) |
| (ii) 单状态机 | `kind`:未观测 / 纯 number / 纯 meta / 混合 | 纯 number ⇒ 稳定;混合 ⇒ 不投机 | 省字段,但「混合」丢失了比例(99% number 与 50% number 都叫混合) |

**选 (i) 双计数**——比例信息让 P2 能设「稳定阈值」(如 ≥99% number 才判稳定),比「一票否决的混合态」精确。这是「让 fallback 与投机的分水岭可调」的基础(`docs/design/roadmap.md` (§5) 原则 4)。

> **这是对 [05](./p1-interpreter/05-interpreter-loop.md) §6.4 / [02](./p1-interpreter/02-bytecode-isa.md) §7 的字段回填请求**(§3.6):算术 IC slot 需要 `numHits`/`metaHits` 双计数,而非单布尔。P1 的 `recordArithNumber()` 改为 `numHits++`,慢路径(元方法)加 `metaHits++`。**这只增字段、不改 P1 取值快路径**(P1 仍现场 `IsNumber`,IC 只旁路计数),与 [05](./p1-interpreter/05-interpreter-loop.md) §6.4「P1 写不读」一致。

### 3.4 聚合产物:TypeFeedback(喂给 P3/P4 的数据结构)

P2 把一个 Proto 的所有 IC 观测聚合成一份 `TypeFeedback`,挂在 `ProfileData` 旁:

```go
// internal/bridge —— 一个 Proto 的类型 feedback(P2 聚合,P3/P4 消费)
//
// 按 pc 索引每个程序点的类型稳定性判断。P4 用它做投机(f64 快路径 + guard);
// P3 用它判断「这个点能否走更快的特化翻译」(P3 是 try-compile 非投机,
// 但仍可对稳定点发更紧的 Wasm,见 P3 §3)。
type TypeFeedback struct {
    points []PointFeedback   // 按 pc 索引(只有 IC 点有值)
}

type PointFeedback struct {
    pc        int32
    kind      FeedbackKind   // ArithStableNumber / TableMono / TableMega / GlobalStable / SelfMono / Unstable
    confidence float32       // 稳定度(numHits/total 等,§3.3),P4 据此决定是否投机
    // 表点额外:稳定的 shape(代次)/ 槽位 —— 供 P4 投机直达槽(P4 §IC 投机)
    stableShape uint32
    stableIndex uint32
}

type FeedbackKind uint8
const (
    FBUnstable        FeedbackKind = iota // 多态/混合,别投机
    FBArithStableNumber                   // 算术点恒 number ⇒ P4 发 f64 快路径
    FBTableMono                           // 表访问单态稳定 ⇒ 可投机直达槽
    FBTableMega                           // 表访问 megamorphic ⇒ 别投机(05 §6.3)
    FBGlobalStable                        // 全局读恒定 ⇒ 可常量化/内联
    FBSelfMono                            // 方法调用单态 ⇒ 可内联方法查找
)
```

- **`confidence`** 是 §3.3 双计数算出的比例,P4 据此决定投机激进度(高 confidence 才投机)。
- **`FBTableMega`** 直接对应 [05](./p1-interpreter/05-interpreter-loop.md) §6.3 的 `kind=4 megamorphic`——[05](./p1-interpreter/05-interpreter-loop.md) §6.3 说「P1 可不实现降级,留给 P2 标记『此点多态、别再投机』」,这里就是那个标记的落地:P2 把 megamorphic 点标 `FBTableMega`,告诉 P4「这个点别投机直达槽,老实查哈希」。

### 3.5 P2 自己不消费 feedback(它只是供料)

强调:`TypeFeedback` 是 P2 **产出但不自用**的——P2 不加速(§0),所以它不会拿 feedback 去发 f64 快路径(那是 P4)。P2 的角色是「把 P1 散落的 IC 观测整理成一份结构化报告,交给编译层」。这与算术 IC 的「P1 写不读」一脉相承:**信息在每一层被生产,在下一层被消费**——P1 写 IC,P2 读 IC 写 feedback,P4 读 feedback 投机。每层只做自己那一棒。

### 3.6 对上游文档的回填请求(IC 计数字段)——**已兑现**

承 §3.3,P2 的算术 feedback 需要 P1 的算术 IC slot 提供**双计数**而非单布尔:

1. **[02](./p1-interpreter/02-bytecode-isa.md) §7 / [05](./p1-interpreter/05-interpreter-loop.md) §6.4 算术 IC**:`numHits`/`metaHits` 双计数,挪用算术 IC 闲置的 `shape`/`index`/`tableRef` 字段,不增 ICSlot 尺寸。**02 §7 与 05 §6.4 均已登记确认。**✅

> 这个回填**纯增语义、不改 P1 取值快路径**——P1 算术快路径仍现场 `IsNumber`(05 §4.1),IC 只旁路记两个计数。与 [05](./p1-interpreter/05-interpreter-loop.md) §6.4「算术 IC 不影响快路径取值」完全一致。

---

## 4. 静态可编译性分析器(承 roadmap §4 P2 + 原则 4)

### 4.1 定位:这是 try-compile-fallback「零 deopt」的安全闸门

`docs/design/roadmap.md` (§4) P2:**「静态可编译性分析器:varargs / coroutine / debug 等形状标记『不升层』,永远走解释」**。[design-premises](../../llmdoc/must/design-premises.md) 前提三原则 4:**「不可编译/不可升层形状走 fallback,不做完备性」**。

这个分析器是整个 P2 的安全核心:**它决定哪些 Proto 能被 P3/P4 编译。** 因为望舒走 **try-compile-fallback-interpret**(§5,LuaJ luajc 同款),编译层只编译「静态可保证能正确编译」的子集——分析器就是划这条线的人。**它判错的后果是灾难性的**:把一个不可编译形状判成可编译,P3 会编译出错误代码或运行期崩溃,而 fallback 机制根本不会被触发(因为没人知道这里该 fallback)。所以分析器的铁律是 **保守**:**宁可漏判(把可编译的判成不可编译,损失一点加速),绝不误判(把不可编译的判成可编译,出正确性事故)**。

### 4.2 在 AST 上做,还是在字节码上做?(承 04 §1)

[04](./p1-interpreter/04-frontend-parser-codegen.md) §1 已点名:**「AST 利于 P2 可编译性分析,P1 顺手产出 P2 复用」**。两个候选层:

| 分析层 | 优点 | 缺点 |
|---|---|---|
| **AST(选定主分析)** | ① 结构化信息丰富(能直接看到 `function(...)` 的 vararg 标记、`coroutine.yield` 调用、`...` 表达式、`setfenv` 调用);② 04 已产出 AST,P2 复用(纯增量);③ 分析一次,结果挂 Proto 缓存 | AST 在 codegen 后可能不保留(需 04 保留或重建) |
| **字节码(Proto)辅助** | Proto 恒存(01 §5.7),总能分析;`VARARG`/`CLOSURE` 等 opcode 暴露部分形状 | 字节码丢了源级语义(看不出 `pcall` 的语义意图,只看到 CALL);某些不可编译形状在字节码层难识别 |

**定稿:主分析在 AST 上(04 保留 AST 或 P2 触发时重新 parse),Proto 层做交叉校验。** 理由:

- **AST 直接对应「不升层形状」的源级特征**——vararg、coroutine、debug、setfenv 都是源级概念,在 AST 上一眼可辨;字节码层要从 opcode 序列反推这些语义,易漏。
- [04](./p1-interpreter/04-frontend-parser-codegen.md) §1 已承诺「P1 顺手产出 P2 复用」——P2 复用 04 的 AST 是设计预期(纯增量,`docs/design/roadmap.md` (§3))。
- **AST 保留的工程细节**(04 是否在 codegen 后保留 AST,还是 P2 升层时按 chunkname 重新 parse 源码)留 §9 缺口,依赖 04 落地后定。倾向「Compile 时顺手做一遍可编译性分析,结果缓存进 Proto 的 `compilable` 字段(§2.2)」——这样 AST 用完即弃,分析结果持久化。

### 4.3 「可编译子集」的精确定义(不升层形状清单)

一个 Proto **可编译** ⟺ 它**不含**下列任一「不升层形状」。这张清单是 try-compile-fallback 的边界线:

| # | 不升层形状 | 为什么不编译(走解释) | AST/字节码识别 |
|---|---|---|---|
| F1 | **vararg 函数**(`function(...)`、含 `...` 表达式) | vararg 的多值语义(02 §6 base 之下负区)在编译层处理复杂,且 vararg 函数多是「胶水」非热点 | AST:函数声明的 vararg 标记;字节码:`IsVararg` / `VARARG` opcode |
| F2 | **协程相关**(函数体内可能 `coroutine.yield`,或被 resume 的协程主函数) | yield 要挂起/恢复执行栈(08 协程),编译层的栈与解释器栈切换协议复杂(P3 §5 trampoline 不跨 yield) | AST:调用 `coroutine.yield`(直接或间接);保守:任何可能 yield 的调用 |
| F3 | **debug 库使用**(`debug.getlocal`/`sethook`/`getinfo` 等作用于本函数) | debug 要内省/篡改运行期帧状态,编译后帧布局变了(Wasm locals,P3 §3),debug 语义无法保证 | AST:调用 `debug.*`;保守:函数内引用了 `debug` 表 |
| F4 | **setfenv/getfenv**(改变函数环境) | 改 `_ENV` 让全局访问的目标表运行期可变,IC/编译特化的全局假设失效 | AST:调用 `setfenv`/`getfenv` |
| F5 | **过大函数**(指令数 / 寄存器数超阈值) | 编译大函数收益递减(编译时间长、Wasm module 大、icache 压力),且大函数常是「初始化」非热循环 | Proto:`len(Code)` 超阈值 / `MaxStack` 超阈值 |
| F6 | **深嵌套闭包 / 复杂 upvalue 捕获**(P2 初版保守排除) | upvalue 开放/关闭(05 §8.3)在编译层与解释器栈共享时协议复杂,初版保守不编译 | AST:嵌套函数深度 / upvalue 数超阈值(初版可选放宽) |
| F7 | **含 P1 未覆盖语义的形状**(如某些罕见 metamethod 组合,依赖 P3 后端能力) | P3 后端能力有限,某些 opcode 翻译未实现 ⇒ 标不可编译,走解释 | 按 P3 后端的「已支持 opcode 集」反查(P3 §3) |

> **F2(协程)的保守性**:Lua 的 yield 可以发生在任意函数调用链深处(被调函数 yield,调用者也被挂起)。**保守判定**:一个函数若**直接或间接可能 yield**(调用了 `coroutine.yield`,或调用了一个本身可能 yield 的函数),就标不可编译。静态精确判定「是否可能 yield」需要全程序调用图分析(复杂),P2 初版用**保守近似**:函数内**直接**出现 `coroutine.yield` 或调用了**无法静态确定不 yield 的函数**(如经 upvalue/参数传入的未知函数)⇒ 标不可编译。这会把一些其实不 yield 的函数误判为不可编译(漏判,可接受——损失加速不损失正确性)。精确的 yield 分析留 §9 缺口。

### 4.4 分析 pass 设计

```go
// internal/bridge —— 可编译性分析(AST 上,Compile 时做一遍,结果缓存进 Proto)
//
// 保守:任一不升层形状(F1..F7)出现即判 NotCompilable。
// 宁可漏判(可编译判成不可编译),绝不误判(不可编译判成可编译)——
// try-compile-fallback 的全部安全性依赖此保守性(§4.1)。
type Compilability uint8
const (
    CompUnknown      Compilability = iota // 未分析(P1 Compile 占位的初值,11 §1.3)
    CompCompilable                        // 可编译:不含任何不升层形状
    CompNotCompilable                     // 不可编译:含 F1..F7 之一 ⇒ 永久解释
)

// analyzeProto 在 AST 上跑一遍,判定可编译性(Compile 时调用,结果存 Proto.compilable)。
func (b *Bridge) analyzeProto(fn *ast.FuncBody, proto *bytecode.Proto) Compilability {
    v := &compilabilityVisitor{}
    ast.Walk(v, fn)                          // 遍历 AST,检查 F1..F6
    if fn.IsVararg                            { return CompNotCompilable } // F1
    if v.callsYield || v.callsUnknownFn       { return CompNotCompilable } // F2(保守)
    if v.usesDebug                            { return CompNotCompilable } // F3
    if v.usesSetfenv                          { return CompNotCompilable } // F4
    if len(proto.Code) > MaxCompilableInsns   { return CompNotCompilable } // F5
    if v.maxClosureDepth > MaxClosureDepth    { return CompNotCompilable } // F6(初版可放宽)
    if !b.p3.SupportsAllOpcodes(proto)        { return CompNotCompilable } // F7(查 P3 后端能力)
    return CompCompilable
}
```

- **一次分析,缓存结果**:`Compile` 时对每个 Proto 跑一遍 `analyzeProto`,结果存 `Proto.compilable`(§2.2 的 `ProfileData.compilable` 或 Proto 字段),后续升层决策直接读缓存,不重复分析。
- **嵌套 Proto 独立判定**:外层函数不可编译(如含 vararg)不代表内层嵌套函数不可编译——每个 Proto 独立分析。一个可编译的内层热函数,即便外层是 vararg 胶水,仍可被编译(只要它自己不含 F1..F7)。
- **保守缺省**:`SupportsAllOpcodes`(F7)默认严格——只有 P3 后端**明确支持**的 opcode 集合内的 Proto 才可编译,任何未支持 opcode 出现即不可编译。这随 P3 后端成熟而放宽(P3 支持更多 opcode ⇒ 更多 Proto 可编译)。

### 4.5 与 11 §1.3 的衔接:把 Compile 的占位填实

[11](./p1-interpreter/11-embedding-arena-abi.md) §1.3 / §1 明确:**P1 的 `Compile` 把「可编译性探测与层级决定」实现为「恒真占位——所有函数标 tier-0、恒解释」,接口预留,P2 填充**。

P2 就是来填这个占位的:

| 阶段 | `Compile` 的可编译性探测 | 层级决定 |
|---|---|---|
| **P1**(11 §1.3) | 恒「可解释」占位,不做真实分析 | 恒 tier-0(crescent),所有 Proto 标「解释」 |
| **P2**(本文 §4) | `analyzeProto` 真实判定 F1..F7,结果存 `Proto.compilable` | tier-0 起步,运行期热度触发升 tier-1(§6);不可编译者永久 tier-0 |

**接口不变**(11 §1.3 承诺):`Compile` 的返回签名、`Program` 的「可升层标记」字段在 P1 就定好,P2 只填充 `analyzeProto` 逻辑与 `compilable` 字段的真实值,**不改公共 API**。这是 `docs/design/roadmap.md` (§5) 原则 3「每阶段独立交付、接口稳定」的兑现——宿主代码(依赖 `Compile`/`Program`)在 P1→P2 升级时零修改。

---

## 5. try-compile-fallback-interpret:零 deopt 机器(承 roadmap §4 + LuaJ luajc)

### 5.1 策略本质:只编译能静态保证的子集,所以永不需要 deopt

`docs/design/roadmap.md` (§4) P2:**「try-compile-fallback-interpret(LuaJ luajc 同款策略),换来零 deopt 机器」**。这是 P2 最重要的设计抉择,**也是 P2 与 P4/P5 的根本分野**。

**核心洞察**:

- **P4/P5 是投机 JIT**——它们**假设**运行期类型稳定(如「这个点恒为 number」),据此发激进快路径(f64 直算),但假设可能被打破(某次真来了个 table),所以必须有 **deopt 机器**(去优化:guard 失败时 OSR exit 回解释器,P4 §deopt)。deopt 是投机 JIT 的标配,也是其**最危险**处(`docs/design/roadmap.md` (§5) 原则 2「投机错误静默错果」)。
- **P2/P3 是 try-compile,非投机**——P2 的可编译性分析(§4)**静态保证**编译的子集在**任何运行期输入下都正确**(不依赖运行期类型假设)。编译出的 gibbous 代码对所有合法输入都给正确结果,**不存在「假设被打破」的情况**,所以**根本不需要 deopt**。

**这就是「零 deopt 机器」的含义**:

| | try-compile(P2/P3) | 投机 JIT(P4/P5) |
|---|---|---|
| 编译什么 | 静态可保证正确的子集(§4) | 热代码 + 运行期类型假设 |
| 假设 | **无运行期假设**(对所有输入正确) | 假设类型稳定(f64 快路径) |
| 假设被打破 | **不可能**(无假设) | 可能(guard 失败) |
| deopt 机器 | **不需要**(零 deopt) | 必需(OSR exit 回解释器) |
| 不可处理的形状 | **fallback 永久解释**(编译前就排除) | deopt 回解释器(运行期退出) |
| 危险性 | 低(无投机,差分只验「翻译正确」) | 高(投机错误静默错果,差分是主防线) |

> **fallback ≠ deopt**(关键区分):**fallback** 是「编译**前**就决定不编译,永久走解释」(静态、一次性、§4 判定);**deopt** 是「编译**后**运行期假设失败,从编译码退回解释器」(动态、可能反复、P4)。P2 只有 fallback,没有 deopt。一个 Proto 要么静态判定可编译(编译后永远跑 gibbous),要么判定不可编译(永远跑 crescent)——**没有「跑着跑着退回来」**。这把「投机错误静默错果」这一 JIT 最危险 bug 类(`docs/design/roadmap.md` (§5) 原则 2)在 P2/P3 **从根上消除**(没有投机就没有投机错误)。

### 5.2 「零 deopt」为什么是 P3 的战略价值(承 roadmap §4)

`docs/design/roadmap.md` (§4) P3 战略价值:**「在不用调试机器码的后端上,先把分层机器(升层/降层/fallback)整体跑通」**。P2 的 try-compile-fallback 让 P3 能在 **wazero(不需调试机器码)** 上跑通整套分层逻辑,**而不必先攻克 deopt 这个最难的部分**:

- P3 用 wazero 执行 Wasm,**不生成原生机器码**(`docs/design/roadmap.md` (§4) P3「不用调试机器码的后端」)——系统管线(exec-mmap/W^X/icache)由 wazero 解决(P3 §10)。
- P3 因为是 try-compile(零 deopt),**不需要实现 OSR exit、snapshot、guard 失败恢复**这些投机 JIT 的复杂机器——这些留到 P4(`docs/design/roadmap.md` (§4) P4「deopt 简单,函数级 OSR exit」)。
- 所以 P3 的工作聚焦在「字节码→Wasm 翻译 + trampoline + linear memory 共享」(P3 §3/§5/§4),**分层决策的复杂度全在 P2(本文),投机的复杂度全在 P4**。P3 夹在中间,享受 P2 的零 deopt(不操心去优化)与 wazero 的成熟后端(不操心机器码),专心把「升层→编译→执行→fallback」的骨架跑通。

**这是分阶段策略的精髓**:把「分层骨架」「投机」「机器码后端」三个难点解耦,P2 攻分层骨架(零 deopt 简化它),P3 在成熟后端上验证骨架,P4 才加投机,P5 才加 trace。每阶段只啃一块硬骨头(`docs/design/roadmap.md` (§5) 原则 3)。

### 5.3 升层 / 降层 / fallback 决策流程图

```
                    ┌─────────────────────────────┐
                    │ Proto 在 crescent 解释执行    │  ← 起点(所有 Proto,11 §1.3)
                    │ tierState = TierInterp        │
                    └─────────────────────────────┘
                                  │
                    热度计数(§2)累计越阈值
                                  │
                                  ▼
                    ┌─────────────────────────────┐
                    │ 查 Proto.compilable(§4 缓存)│
                    └─────────────────────────────┘
                          │                    │
              CompCompilable              CompNotCompilable
                          │                    │
                          ▼                    ▼
            ┌──────────────────────┐   ┌──────────────────────────┐
            │ try-compile:         │   │ 标 tierState = TierStuck  │
            │ 请 P3 编译该 Proto    │   │ (永久解释,停止热度计数)   │  ← fallback(静态)
            │ (P3 §3 字节码→Wasm)  │   │ 不再尝试升层(§6.2)        │
            └──────────────────────┘   └──────────────────────────┘
                  │            │                    │
            编译成功      编译失败                  留在 crescent
                  │       (P3 后端遇到             (永不退役,原则 1)
                  │        未支持的情况)
                  ▼            │
    ┌──────────────────────┐  ▼
    │ tierState = TierGibbous│ ┌──────────────────────────┐
    │ 该 Proto 后续走 gibbous │ │ 标 TierStuck(编译失败也   │
    │ (P3 trampoline,§7)   │ │ fallback 永久解释)         │  ← fallback(编译失败)
    │ 日志:promoted to      │ │ 不再重试(§6.3)            │
    │ gibbous(§6.4)         │ └──────────────────────────┘
    └──────────────────────┘            │
                  │                  留在 crescent
            (零 deopt:无               (永不退役)
             运行期退回路径,§5.1)
                  ▼
       gibbous 代码对所有合法输入正确
       (静态保证,无 guard 失败)
```

**三条「不升层/留解释」的路径**汇聚到同一个着陆点 **crescent 解释器**(永不退役,`docs/design/roadmap.md` (§5) 原则 1):

1. **静态 fallback**:可编译性分析判 `CompNotCompilable`(含 F1..F7)→ 永久解释。
2. **编译失败 fallback**:try-compile 时 P3 后端遇到未支持情况(F7 没拦住的边角)→ 永久解释。
3. **冷代码**:热度没越阈值,从不尝试升层,自然一直解释(占绝大多数 Proto)。

而**升层成功的 Proto 走 gibbous 后没有任何「退回 crescent」的路径**——这就是零 deopt:**升层是单向的**(crescent → gibbous,不回头),因为 gibbous 代码静态保证正确,没有运行期退出的理由。

> **解释器作为 fallback 着陆点(原则 1 的物理兑现)**:`docs/design/roadmap.md` (§5) 原则 1「解释器永不退役——它是所有编译层的 deopt 着陆点和语义 oracle」。在 P2 这里,解释器是**三条 fallback 路径的共同着陆点**(虽然 P2 是零 deopt,但「不可编译/编译失败/冷」的 Proto 都落在解释器上)。[05](./p1-interpreter/05-interpreter-loop.md) §0 早已声明 crescent「既是 P1 的唯一执行层,也是 P3/P4/P5 所有编译层的 deopt 着陆点与语义 oracle」——P2 的 fallback 机制就是依赖这个「解释器始终在」的前提:任何 Proto 在任何时候都保有可解释执行的字节码([architecture](./architecture.md) §4 不变式 1),所以 fallback 永远有地方落。

---

## 6. 升降层决策状态机

### 6.1 TierState 状态机(定稿)

```go
// internal/bridge —— 升降层状态机(挂 ProfileData.tierState,§2.2)
type TierState uint8
const (
    TierInterp  TierState = iota // 解释执行中(起点,所有 Proto)
    TierGibbous                  // 已升 gibbous(编译成功,走 Wasm 层)
    TierStuck                    // 卡在解释(不可编译 / 编译失败,永久解释,停止计数)
)
```

状态转移(**注意:无任何「回到 TierInterp」的边——这是零 deopt 的状态机体现**):

```
                  热度越阈值 + CompCompilable + 编译成功
   TierInterp ──────────────────────────────────────────► TierGibbous
       │                                                    (单向,不回头)
       │  热度越阈值 + (CompNotCompilable 或 编译失败)
       └──────────────────────────────────────────────► TierStuck
                                                            (单向,停止计数)

   TierGibbous:无出边(零 deopt,不降层,§6.5)
   TierStuck:  无出边(永久解释,不重试,§6.3)
```

**三个状态都是「吸收态」或单向流入**:

- `TierInterp` → `TierGibbous`(升层成功)或 `TierStuck`(不可编译/失败),**永不反向**。
- `TierGibbous` 是终态——零 deopt,不降层(§6.5)。
- `TierStuck` 是终态——永久解释,不重试(§6.3)。

这个「无环、单向、有吸收态」的状态机,正是「零 deopt 机器」的形式化:**没有任何运行期事件能让一个 Proto 从 gibbous 退回 interp**。对比 P4 的状态机(P4 会有 `gibbous → interp` 的 deopt 边),P2/P3 的状态机干净得多。

### 6.2 何时升 / 何时不升

| 条件 | 决策 | 转移 |
|---|---|---|
| 热度越阈值(§2.4)+ `CompCompilable`(§4)+ P3 编译成功 | **升** | `TierInterp → TierGibbous` |
| 热度越阈值 + `CompNotCompilable`(含 F1..F7) | **不升,永久解释** | `TierInterp → TierStuck` |
| 热度越阈值 + 可编译但 P3 编译失败 | **不升,永久解释** | `TierInterp → TierStuck` |
| 热度未越阈值 | **不升**(继续解释,继续计数) | 留 `TierInterp` |
| `compilable == CompUnknown`(未分析) | 先触发 §4 分析,再按结果决策 | — |

```go
// §2.4 的 considerPromotion:热度越阈值后的升层决策入口
func (b *Bridge) considerPromotion(proto *bytecode.Proto, pd *ProfileData) {
    if pd.tierState != TierInterp { return }           // 已升或已卡,不重复决策
    // 1. 取可编译性(§4,Compile 时已缓存;若 Unknown 则现在分析)
    if pd.compilable == CompUnknown {
        pd.compilable = b.analyzeProto(astOf(proto), proto)
    }
    if pd.compilable == CompNotCompilable {
        pd.tierState = TierStuck                        // 不可编译 ⇒ 永久解释(§6.2)
        b.diag.Logf("function %s stays interpreted (not compilable)", proto.Name)
        return
    }
    // 2. 可编译 ⇒ try-compile(§5,请 P3,§7)
    pd.compileTried = true
    code, err := b.p3.Compile(proto, b.feedbackOf(proto)) // P3 §3:字节码→Wasm + 类型 feedback
    if err != nil {
        pd.tierState = TierStuck                        // 编译失败 ⇒ fallback 永久解释(§5.3)
        b.diag.Logf("function %s compile failed, stays interpreted: %v", proto.Name, err)
        return
    }
    // 3. 编译成功 ⇒ 升 gibbous(单向,§6.1)
    b.installGibbous(proto, code)                       // 装 gibbous 代码 + trampoline(P3 §5)
    pd.tierState = TierGibbous
    b.diag.Logf("function %s promoted to gibbous", proto.Name) // §6.4 自释日志
}
```

### 6.3 不重试:TierStuck 是终态(防抖)

一旦标 `TierStuck`(不可编译或编译失败),**永不再尝试升层**:

- **停止热度计数**:`TierStuck` 的 Proto 不再触发 `considerPromotion`(§2.4 的 `tierState == TierInterp` 守卫),计数也可停(它永远是解释,数热度无意义)。
- **防抖动**:若不记「已尝试失败」,一个不可编译的热函数会**每次越阈值都重试编译**(反复失败、反复浪费),`compileTried`/`TierStuck` 防止这个(`docs/design/roadmap.md` (§5) 原则 3「不亏」——失败一次就认账,不反复折腾)。

> **为什么编译失败也永久 stuck 而非「下次再试」**:try-compile 的失败是**确定性**的——同一个 Proto、同一个 P3 后端,这次编译失败下次也会失败(F7 类的「后端不支持某 opcode」不会因为再试一次就支持)。所以失败即永久 stuck 是正确的(不是「暂时失败」)。**例外**:若 P3 后端**升级**(支持了更多 opcode),理论上之前 stuck 的 Proto 可重新评估——但这是「换了 P3 版本」级别的事件,不在单次运行内,P2 不为此设运行期重试(记 §9 缺口)。

### 6.4 升层日志:promoted to gibbous(自释诊断)

[evolution-roadmap](../../llmdoc/architecture/evolution-roadmap.md) / `docs/design/roadmap.md` (§4):**「日志/诊断输出形如 `function promoted to gibbous`,比裸 tier 编号自释」**。P2 的状态转移产出这类日志(经 `internal/diag`,[architecture](./architecture.md) §1):

| 转移 | 日志 |
|---|---|
| `TierInterp → TierGibbous` | `function <name> promoted to gibbous`(升层成功) |
| `TierInterp → TierStuck`(不可编译) | `function <name> stays interpreted (not compilable: <形状>)` |
| `TierInterp → TierStuck`(编译失败) | `function <name> compile failed, stays interpreted: <err>` |

**注意**:**没有 `promoted to bridge`**——bridge 不是执行层(§0),不是升层落点。P2 触发的升层落点是 **gibbous**(P3 的 Wasm 层)。日志说「promoted to gibbous」而非「promoted to bridge」,正确反映「P2 决策、P3 落地」的分工。

### 6.5 P2 本身不降层(零 deopt 的另一面)

**P2 没有任何降层逻辑**——这是 §5.1「零 deopt」的直接后果:

- 升到 gibbous 的 Proto **永远跑 gibbous**(静态保证正确,无退回理由)。
- 没有「gibbous 跑着跑着发现不对、退回 crescent」的路径(那是 P4 的 deopt,P2 没有)。
- 唯一的「不跑 gibbous」是**从一开始就没升**(TierStuck 或冷),不是「升了又降」。

> **对比 P4 的降层**:P4 是投机 JIT,有 `gibbous → interp` 的 OSR exit(`docs/design/roadmap.md` (§4) P4「deopt 简单,函数级 OSR exit 回解释器」)——P4 的 gibbous 代码含投机 guard,guard 失败就 deopt 回 crescent。**P2 的 gibbous(经 P3 try-compile)没有 guard**(无投机),所以没有 deopt 边。这是 P2/P3 与 P4 在状态机上的本质区别:**P2/P3 单向升,P4 可升可降。** 同样跑在 gibbous tier(tier-1,[evolution-roadmap](../../llmdoc/architecture/evolution-roadmap.md)),但 P3 的 gibbous 代码与 P4 的 gibbous 代码在「是否可被 deopt」上不同——P3 代码不可 deopt(零 deopt),P4 代码可 deopt。

---

## 7. 与 P3 / P4 的接口:P2 产出三样东西

P2 产出**热度、类型 feedback、可编译性判定**三样,喂给编译层:

### 7.1 喂给 P3(决定编译哪些、怎么编译)

```go
// internal/bridge → internal/gibbous/wasm 的接口(P2 调 P3)
//
// P2 决定「编译哪个 Proto」(热 + 可编译),把 Proto + 类型 feedback 交给 P3。
type P3Compiler interface {
    // SupportsAllOpcodes:P3 后端能力查询(§4 F7 用)——这个 Proto 的所有 opcode 都支持吗?
    SupportsAllOpcodes(proto *bytecode.Proto) bool
    // Compile:把 Proto 编译成 gibbous 代码(P3 §3 字节码→Wasm)。
    //   feedback:P2 聚合的类型 feedback(§3.4),P3 可用它发更紧的 Wasm(P3 §3,可选)。
    //   返回编译产物 + err(err 非 nil ⇒ try-compile 失败,fallback 解释,§5.3)。
    Compile(proto *bytecode.Proto, feedback *TypeFeedback) (GibbousCode, error)
}
```

P2 给 P3 的:

- **哪些 Proto 要编译**:热度越阈值 + `CompCompilable` 的 Proto(P2 筛选,P3 不操心热度/可编译性)。
- **类型 feedback**(§3.4):P3 是 try-compile 非投机,**不依赖** feedback 正确性(没有 feedback 也能正确编译可编译子集);但 feedback **可选**用来发更紧的 Wasm——如某算术点 `FBArithStableNumber`,P3 可直接发 `f64.add`(而非通用的「检查类型再分支」),因为可编译子集保证了这点的语义(P3 §3)。**注意**:P3 用 feedback 优化时仍须保证「对所有合法输入正确」(若 feedback 说恒 number 但实际来了非 number,P3 的代码要么仍正确处理、要么这点本就被 §4 判不可编译)——这与 P4「投机 + guard」不同,P3 用 feedback 是「锦上添花的紧凑翻译」,不是「赌类型稳定」。

### 7.2 喂给 P4(类型投机供料)

```go
// internal/bridge → internal/gibbous/jit 的接口(P4 读 P2 的 feedback 做投机)
//
// P4 是投机 JIT,直接消费 P2 的 TypeFeedback 做类型投机(f64 快路径 + guard)。
type P4Feedback interface {
    // FeedbackFor:取某 Proto 的类型 feedback(§3.4),P4 据 confidence 决定投机激进度。
    FeedbackFor(proto *bytecode.Proto) *TypeFeedback
}
```

P4 与 P3 的关键差异(都消费 P2 产出,但用法不同):

| | P3 用 P2 的 feedback | P4 用 P2 的 feedback |
|---|---|---|
| 用途 | 可选的紧凑翻译(对可编译子集) | **核心**:类型投机(f64 快路径 + guard) |
| 依赖 feedback 正确性 | 不依赖(try-compile 对所有输入正确) | 依赖(投机基于 feedback;但有 guard 兜底 + deopt) |
| feedback 错的后果 | 无(P3 不赌类型) | guard 失败 → deopt 回解释器(P4,不出错只是慢) |
| `confidence`(§3.4)作用 | 弱(P3 不投机) | 强(高 confidence 才投机) |

> **同一份 feedback,两种消费**:P2 不知道下游是 P3(保守用)还是 P4(投机用),它只忠实产出 feedback。P3「锦上添花」、P4「据此投机」——再次体现「信息在 P2 生产,在编译层按各自策略消费」(§3.5)。这也是为什么 P2 的 feedback 要带 `confidence`(§3.3 双计数):P4 需要比例判投机,P3 不需要但拿了也无害。

### 7.3 P2 是「编译层的前端」

总结 P2 与 P3/P4 的关系:**P2 是所有编译层的共享前端**——它把「该编译什么、什么类型、多热」算好,P3/P4/P5 共享这个前端,只换「后端怎么发射代码」(P3 发 Wasm、P4 发原生、P5 发 trace IR)。这呼应 `docs/design/roadmap.md` (§4) P4「**继承 P3 的全部分层结构,只换发射后端**」——而「分层结构」的核心(热度 + feedback + 可编译性 + 升降决策)正是 P2 提供的。P2 一次建好,P3/P4/P5 复用。

---

## 8. 不变式清单(实现与决策须守)

1. **P2 不加速、不在热路径**:P2 是基建(§0),产出决策喂编译层,本身不执行字节码、不发射代码。`internal/bridge` 不含任何「跑 Proto」的代码。
2. **算术 IC「P1 写、P2 读」**:IC 的写入是 P1 的事(05 §6.4);P2 只读 IC 聚合 feedback,不改 IC 写入逻辑、不影响 P1 取值快路径(§3.1/§3.6)。
3. **可编译性分析保守**:宁可漏判(可编译判不可编译,损失加速),绝不误判(不可编译判可编译,出正确性事故)——try-compile-fallback 的全部安全性依赖此(§4.1)。
4. **零 deopt(单向升层)**:P2/P3 是 try-compile 非投机,只编译静态可保证正确的子集,无运行期假设,故无 deopt;升层单向(crescent→gibbous 不回头),P2 无降层逻辑(§5.1/§6.5)。
5. **fallback ≠ deopt**:fallback 是编译前的静态决定(永久解释),deopt 是编译后的运行期退回(P4 才有)。P2 只有 fallback(§5.1)。
6. **解释器永不退役、是 fallback 着陆点**:三条「留解释」路径(不可编译/编译失败/冷)共同着陆于 crescent(`docs/design/roadmap.md` (§5) 原则 1、[architecture](./architecture.md) §4 不变式 1);任何 Proto 始终保有可解释字节码(§5.3)。
7. **字节码不可动**:P2 旁路计数(路线 B,§2.3)不改 P1 的 0..37 字节码;38..63 在 P2 阶段保持全空(留 P3 视需要用,§2.3)。P1 字节码向后兼容上层翻译(02 §4)。
8. **接口稳定(填占位不改 API)**:P2 填 [11](./p1-interpreter/11-embedding-arena-abi.md) §1.3 的 Compile 可编译性探测占位,**不改公共 API**(§4.5);宿主代码 P1→P2 零修改。
9. **升层日志说 gibbous 不说 bridge**:bridge 非执行层非升层落点;升层落点是 gibbous(§6.4)。
10. **P2 是编译层共享前端**:热度+feedback+可编译性+升降决策一次建好,P3/P4/P5 复用(只换发射后端,§7.3,`docs/design/roadmap.md` (§4) P4「继承 P3 全部分层结构」)。

---

## 9. 文档缺口 / 待决(记入 [memory/doc-gaps](../../llmdoc/memory/doc-gaps.md))

- **热度阈值数值定标**(§2.4/§2.5):`HotBackEdgeThreshold`/`HotEntryThreshold` 的具体值需 P1+P2 实现后用真实负载(首个宿主规则脚本)校准。当前给的 1000/200 是建议值,**依赖 P1 落地后定稿**。编译预算(防编译风暴的 pacing)P2 初版不做,按实测加。
- **算术 IC 双计数字段挪用**(§3.3/§3.6):请 [02](./p1-interpreter/02-bytecode-isa.md) §7 / [05](./p1-interpreter/05-interpreter-loop.md) §6.4 确认:算术 IC slot 的闲置字段(`shape`/`index`/`tableRef`,算术点无表)可否挪用存 `numHits`/`metaHits`,以不增 ICSlot 尺寸的方式提供双计数。**依赖 02/05 确认字段挪用语义**。
- **AST 保留 vs 重新 parse**(§4.2):可编译性分析在 AST 上做,但 [04](./p1-interpreter/04-frontend-parser-codegen.md) 是否在 codegen 后保留 AST,还是 P2 升层时按 chunkname 重新 parse——倾向「Compile 时顺手分析、结果缓存进 Proto.compilable,AST 用完即弃」,**依赖 04 落地后定**(04 的 AST 生命周期)。
- **精确 yield 分析**(§4.3 F2):协程不可编译的判定 P2 初版用保守近似(直接 `coroutine.yield` 或调用未知函数即判不可编译)。精确的「是否可能 yield」需全程序调用图分析,**留 P2+ 或 P4 实现**(漏判可接受,损失加速不损失正确性)。
- **过大函数/嵌套闭包阈值**(§4.3 F5/F6):`MaxCompilableInsns`/`MaxClosureDepth`/`MaxStack` 阈值需实测定标。F6(深嵌套闭包)初版保守排除,upvalue 编译协议成熟后(P3 §3/§4)放宽。
- **P3 后端能力查询的粒度**(§4.4 F7/§7.1):`SupportsAllOpcodes` 是 opcode 级还是更细(某 opcode 的某些操作数组合)——依赖 P3 后端实现(P3 §3 的 opcode 翻译覆盖面)。P3 成熟则更多 Proto 可编译。
- **P3 后端升级后的 stuck 重评估**(§6.3):若 P3 后端升级支持了更多 opcode,之前 `TierStuck` 的 Proto 理论上可重新评估。P2 单次运行内不做运行期重试(失败即永久 stuck);跨版本的重评估机制待定。
- **CallInfo 帧级计数是否需要**(§2.2):主存储是 Proto 旁 ProfileData;CallInfo 帧级回边计数(单次调用内)P2 主决策不用,是否有「分析单次长循环」的场景需要,**待真实负载反馈**。
- **多 State 共享 Program 的 profile 归属**(承 11 §1.4/§8):一个 `Program` 可被多个 `State` 并发 Call(11 §8)。热度计数/feedback 是挂 Proto(Go 堆,Program 持有)还是挂 State——若挂 Proto 则多 State 共享同一份 profile(累积更快,但并发写计数需同步);若挂 State 则各 State 独立 profile(无竞争但累积慢)。**依赖 11 §8 并发语义落地后定**(倾向:profile 挂 State 私有,避免并发写 Proto 旁计数的数据竞争——但 Proto 是只读共享的,计数得另置 State 私有的 profile 表)。**这是 P2 的一个重要并发缺口**。

---

相关:[architecture](./architecture.md)(包布局:bridge 是基建非执行层 / tier 映射) ·
[02-bytecode-isa](./p1-interpreter/02-bytecode-isa.md)(FORLOOP 热点回边 §4 / IC slot §7 / 38..63 预留) ·
[05-interpreter-loop](./p1-interpreter/05-interpreter-loop.md)(IC 执行 §6 / 算术 IC 写不读 §6.4 / CallInfo §1) ·
[04-frontend-parser-codegen](./p1-interpreter/04-frontend-parser-codegen.md)(AST 利于可编译性分析) ·
[11-embedding-arena-abi](./p1-interpreter/11-embedding-arena-abi.md)(Compile 可编译性探测占位 §1.3 / 并发语义 §8) ·
[12-testing-difftest](./p1-interpreter/12-testing-difftest.md)(IC 反馈正确性差分 §7 / 黄金字节码 §2.5) ·
[p3-wasm-tier](./p3-wasm-tier.md)(P2 喂 P3:编译哪些 + feedback) ·
[p4-method-jit](./p4-method-jit.md)(P2 喂 P4:类型投机供料) ·
[roadmap.md](./roadmap.md) (§4 P2 定义 / §5 五条原则) ·
[evolution-roadmap](../../llmdoc/architecture/evolution-roadmap.md)(P2 无独立量化验收 / tier 映射) ·
[design-premises](../../llmdoc/must/design-premises.md)(原则 1 解释器永不退役 / 原则 4 走 fallback 不做完备性)
