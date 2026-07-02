# P5 §5:trace 寄存器分配器——单遍逆序线性扫描

> 状态:**未立项图纸(启动判定见 [01](./01-launch-judgment.md))**。本文把 00-overview §2 流水线 ③「寄存器分配」展开为可以直接施工的算法和寄存器池方案:包括线性 trace 上的单遍逆序扫描分配、望舒约束下的寄存器池具体切分、PHI 合并、跨调用点的寄存器约定,以及与 snapshot 的双向配合约定。P5 目前还没有一行代码,本文以「建议 / 推荐 / 待 PT-4 验证」的说法为准;引用的 P1/P4 事实项(寄存器约定、JITContext 字段、CallInfo 布局)已按 PR #42(post-2026-07-02 实现勘误)校准。
>
> 对应 Go 包:`internal/fullmoon/trace/regalloc`(建议命名,主包 `internal/fullmoon/trace`,见 [architecture](../architecture.md) §1)。
>
> 上游依据:
> [./00-overview.md](./00-overview.md)(00-overview §2 流水线 ③ 寄存器分配 / §4.4 逐组件难度评估「中偏难」);
> [00-overview.md](./00-overview.md)(P5 定位 / 分阶段 gate / v1 范围包含 regalloc);
> [03-ir-design.md](./03-ir-design.md)(LuaJIT 式两数组线性 SSA IR / IRRef 索引 / 64-bit IRIns 记录 / guard 位——寄存器分配的输入形式);
> [04-optimization-passes.md](./04-optimization-passes.md)(CSE/LICM/DCE/sink 后交给 regalloc 的 IR 已经是 SSA,并且 snapshot 已经合并为 DCE 根)。
>
> P4 依赖面(资产复用):
> [../p4-method-jit/06-backends.md](../p4-method-jit/06-backends.md) §4(**post-PR-#42 实现勘误**——amd64 r15 = jitContext / rbx = valueStackBase / **r14 = Go G(Go 拥有,P4/P5 都不使用)** / arenaBase 没有专属寄存器;arm64 x27 = jitContext / x26 = vsBase / **x28 = Go G**——本文 §2 两个架构的寄存器池以这份勘误为准);
> [../p4-method-jit/05-system-pipeline.md](../p4-method-jit/05-system-pipeline.md) §3.3(JITContext 真实字段:`arenaBase` / `valueStackBase` / `preemptFlag` / `exitReasonCode` / `exitArg0` / `resumeOff` / `codePageAddr` / `savedGoG` / `hostRef` / `spillBase`——本文 §3 spill 区借用 `spillBase`);
> [../p4-method-jit/05-system-pipeline.md](../p4-method-jit/05-system-pipeline.md) §4.3(exit-reason 协议、ExitInlineHelper 状态、Go dispatcher 循环、`RefreshJitCtxAddrs`——本文 §5 跨调用点约定借它作为主通道);
> [../p4-method-jit/04-osr-deopt.md](../p4-method-jit/04-osr-deopt.md)(P4 exit stub 编译期静态生成——本文 §6 与 06 snapshot 配合的对应面)。
>
> P1 依赖面:
> [../p1-interpreter/01-value-object-model.md](../p1-interpreter/01-value-object-model.md)(NaN-box `uint64` 值表示——寄存器持值形式);
> [../p1-interpreter/05-interpreter-loop.md](../p1-interpreter/05-interpreter-loop.md) §1(CallInfo / 值栈布局——deopt 落点,与 06 snapshot 配合)。
>
> 姊妹章节:[06-snapshot-deopt.md](./06-snapshot-deopt.md)(snapshot 引用 IR 值,所以 regalloc 必须让这些 IR 值「可以恢复」;两篇文档互为约定的两端)。

---

## 0. 定位:线性 trace 上的最简寄存器分配

### 0.1 一句话

**P5 寄存器分配 = 单遍逆序线性扫描,LuaJIT 风格**——沿着 trace 从尾往头走一遍,每条 IR 指令按「操作数在逆序中首次出现 = 最后一次真实使用」的原则分配寄存器,遇到定义就释放。没有独立的 liveness 分析,没有图着色,也没有跨基本块的合并——**因为 trace 就是一条线,除了循环头的 PHI,没有别的控制流汇合**。

### 0.2 与 P4 的对照:P5 首次引入「跨字节码边界的寄存器」

P4 严格遵守「栈槽真相不变式」(见 [../p4-method-jit/04-osr-deopt.md](../p4-method-jit/04-osr-deopt.md) §3.1):**在每条字节码边界处,所有 Lua 活值都在 arena 值栈槽里**;模板内的寄存器只在单条模板内短暂持值,模板出口一定要 store 回栈槽。这是 P4 deopt 之所以简单的物理原因。

P5 放弃了这个不变式——寄存器分配让 IR 值**跨多条 Lua 字节码**驻留在寄存器里,栈槽的内存来回读写没有了(00-overview §2:「IR 值驻留在机器寄存器里,栈槽的来回读写没有了——P4 的结构税终于卸掉」)。代价是:栈槽不再是真相,deopt 时必须依赖 snapshot 恢复。regalloc 和 snapshot 是同一枚硬币的两面(§6)。

### 0.3 章节路标

| § | 内容 |
|---|---|
| §1 | trace 的形状 → 为什么单遍逆序扫描就够用 |
| §2 | 两个架构的寄存器池(post-PR-#42 勘误)+ Go ABI 兼容位 |
| §3 | 算法主体:数据结构、逆序走法、驱逐策略、spill 区落点 |
| §4 | PHI 合并:循环回跳(back edge)的 parallel move + 寄存器 hint |
| §5 | 跨调用点约定:exit-reason 与 shim 两条通道的 clobber 集 |
| §6 | 与 snapshot 的配合:snapshot 引用扩展活性、location descriptor 约定 |
| §7 | 与代码发射的接口:推荐两遍(allocate then emit),fused 后置 |
| §8 | 调试与验证:regalloc trace dump、snapshot 可恢复性 verifier |
| §9 | 开放问题 |

---

## 1. 问题的形状:线性 trace 只有一处控制流汇合

### 1.1 trace = 一条直线 + 一条可能存在的 back edge

见 [03-ir-design.md](./03-ir-design.md) 和 00-overview §2 的流水线描述,P5 的 trace 是「实际执行过的那一条路径」的线性 IR 记录:

```
trace 形状(loop trace 的典型):
   [entry snapshot]
        │
        ▼
   IR#1   IR#2   IR#3 ... IR#k     (直线段,可能跨 Lua 帧,一条边)
        │
        ▼
   [loop 头 PHI]  ─────────────► (back edge:回到 trace 头,IR#1 之前)
        │
        ▼
   IR#k+1 ...   IR#n   (循环体,每条 IR 可能挂 guard)
        │
        ▼
   [tail = back edge 条件为真,跳回 loop 头]
```

关键性质:

- **除了循环头 PHI,没有其他控制流汇合**:trace 内的分支已经被投机成 guard——分支的另一支不在 trace 里,而是走 side exit 出去(见 [06-snapshot-deopt.md](./06-snapshot-deopt.md) §6)。所以直线段的 liveness 是**平凡的**——一个值从定义到最后一次使用,中间不需要合并控制流。
- **循环头是唯一的 join 点**:上一轮迭代的循环变量与 PHI 定义在这里汇合。这也是唯一需要「跨路径协调寄存器位置」的地方(§4)。

### 1.2 为什么用单遍逆序:liveness 是隐式的

正向线性扫描(比如 Wimmer/LSRA)需要先跑 liveness 分析(每个变量的 live interval),再从头到尾按 interval 端点管理寄存器。P5 用逆序扫描把这一步省掉:

```
逆序扫描的核心不变式:
  「访问一条 IR 时,该 IR 的操作数马上要在此之前的某处被定义;
   操作数在这条 IR 处首次(逆序意义上的首次)见到,就是它的**最后一次使用**——
   把它当前的位置给一个寄存器即可;
   IR 的定义(结果)在这条 IR 处已经完成使用(或已 spill 进 snapshot),
   遇到定义就释放这条 IR 占用的寄存器。」
```

这就是 LuaJIT 单遍风格的核心(06-snapshot-deopt §4 提到「LuaJIT 单遍逆序 regalloc」):**逆向扫描把 liveness 分析和寄存器分配合并**,一遍走完。

**成立前提**:trace 是 SSA(每个 IR 值只被定义一次)+ 线性(内部没有 join)。前者由 03 IR 设计保证;后者由 trace 形状本身保证。

### 1.3 图着色没有必要

图着色寄存器分配是为「任意 CFG + 没有形状约束」设计的通用武器。P5 用它属于杀鸡用牛刀:

| 因素 | 图着色的需求 | P5 trace 的实际形状 |
|---|---|---|
| 复杂 CFG(if / switch / loop 嵌套) | 需要 liveness + 图构造 + 着色 | 一条线 + 一条 back edge,不构成图 |
| 变量数 vs 寄存器数悬殊 | 需要智能选择溢出 | 单条 trace ≤ 4000 IR 指令(见 06-snapshot-deopt §4「trace 长度上限」推论),同时活跃的变量数一般小于 20 |
| 跨基本块合并 | 必须做 | 没有基本块要合并 |

**推荐**:v1 直接选 LuaJIT 单遍逆序;以后如果发现某类 trace(比如深层 sink 之后的 IR 密度过高)扫描得不出最优分配,再考虑局部图着色补丁——但那属于「v2 / v3 优化」范畴,不进 PT-4 主线。

---

## 2. 寄存器池(post-PR-#42 勘误之后的真实池)

**这里是本文最容易出错的地方**——寄存器约定不是设计选择,而是 Go ABI 强约束下的**真实事实**。以 [../p4-method-jit/06-backends.md](../p4-method-jit/06-backends.md) §4 的 2026-07-02 实现勘误作为唯一事实来源,直接照搬到 P5,不重新设计。

### 2.1 amd64 寄存器池

**mmap 段生命周期独占(P5 也照此)**:

| 寄存器 | 用途 | 是否可以分配给 IR 值 |
|---|---|---|
| `r15` | jitContext 指针,mmap 段生命周期内不动 | **否**——保留 |
| `rbx` | valueStackBase(prologue 从 `[r15+vsBaseOff]` 装入;shim 之后需要重新装载) | **否**——保留(即使 shim 之后要重新装载,也不能挪去存 IR 值) |
| `r14` | **Go 拥有的 G 寄存器**;shim 之前需要 `mov r14, [r15+savedGoGOff]` 恢复 G | **否**——绝对不写 |
| `rsp` | 当前 goroutine 栈 SP(PJ10 和 P5 都不切自管栈) | **否**——Go 拥有 |
| `rbp` `r12` `r13` | Go callee-saved,P4 不使用 | 保留(见 §2.1.1 讨论) |

**GPR IR 值池(P5 可以分配)**:

```
GPR pool (amd64) = { rax, rcx, rdx, rsi, rdi, r8, r9, r10, r11 }  ⟹ 9 个
```

**FPR IR 值池**:

```
FPR pool (amd64) = { xmm0, xmm1, ..., xmm15 }  ⟹ 16 个
```

xmm 系列全部是 caller-saved,P4/P5 不需要保存,直接 16 个全部可分配。P5 的 f64 IR 值(算术类型稳定的中间值)几乎全部走 FPR 池。

#### 2.1.1 r12/r13/rbp 是否也纳入 IR 池?

**开放问题**——它们是 Go ABIInternal 的 callee-saved 寄存器(Go 函数会保留),P5 mmap 段进入之后 Go 不会改动;但 mmap 段调 Go shim 时,shim 函数的序言可能写入(为了保存它们自己)。有两条路径:

- **保守方案(建议 v1 采用)**:不纳入 IR 池,回避与 Go ABI 的任何交互面。GPR 池 9 个已经够绝大多数 trace 使用。
- **激进方案(v2 如果寄存器压力大再评估)**:纳入 IR 池,shim 调用前 spill,shim 调用后 reload——多出 3 个寄存器,但增加 shim 边界的 spill 序列。P4 backends §4.1.1 表把 r12/r13 标为「保留」,推荐 P5 v1 沿用。

最终决定留给 PT-4 spike 实测——如果某类 trace(比如深层循环体带很多常量)频繁撞到 9 个 GPR 上限,再评估是否纳入 r12/r13。

### 2.2 arm64 寄存器池

**mmap 段生命周期独占**:

| 寄存器 | 用途 | 是否可以分配给 IR 值 |
|---|---|---|
| `x27` | jitContext 指针 | **否**——保留 |
| `x26` | valueStackBase(shim 之后需要重新装载) | **否**——保留 |
| `x28` | **Go 拥有的 G 寄存器**(Go arm64 ABIInternal 强制要求;Go 自动保留) | **否**——绝对不写 |
| `x29 (fp)` `x30 (lr)` | 帧指针 / 链接寄存器 | **否**——Go 管 |
| `sp` | goroutine 栈 SP | **否**——Go 拥有 |
| `x16` `x17` | IP 平台寄存器,链接器 veneer 会用 | **否**——保留(回避链接器干扰) |
| `x18` | 平台寄存器(darwin 用作 TLS,linux 未定义) | **否**——保留 |
| `x19..x25` | Go callee-saved 池 | 保留(同 amd64 r12/r13 讨论,v1 保守不使用) |

**GPR IR 值池(P5 可以分配)**:

```
GPR pool (arm64) = { x0, x1, x2, ..., x15 }  ⟹ 16 个
```

其中 x0..x7 同时是 AAPCS 参数寄存器,helper 调用之前会用到,分配器需要在 helper 调用点把它们视为 clobber(§5)。

**FPR IR 值池**:

```
FPR pool (arm64) = { v0, v1, ..., v31 }  ⟹ 32 个
```

v0..v31 全部是 caller-saved(实际上 v8..v15 在 AAPCS 里是 callee-saved 的低 64 位,但 Go arm64 ABIInternal 把全部 v 视为 caller-saved,见 P4 06 backends §4.2.5)。

### 2.3 两个架构对照表

| 项 | amd64 | arm64 |
|---|---|---|
| jitContext 固定 | `r15` | `x27` |
| valueStackBase 固定 | `rbx` | `x26` |
| Go G(不使用) | `r14`(shim 之前恢复) | `x28`(Go 自动保留) |
| GPR IR 池大小(v1 保守) | **9**(rax/rcx/rdx/rsi/rdi/r8-r11) | **16**(x0-x15) |
| FPR IR 池大小 | **16**(xmm0-xmm15) | **32**(v0-v31) |
| helper 调用 clobber(§5) | 全部 caller-saved GPR + xmm | x0-x18 + v0-v7,v16-v31 |
| arenaBase 寄存器 | 无(现算,scratch 一般用 r11) | 无(现算,scratch 一般用 x9) |

**P5 的寄存器压力评估**:典型列内核 loop trace 长度 ~50-200 IR 指令、同时活值小于 20,GPR 池 9-16 就够用;如果个别 trace 撞到上限,首选 spill(§3.4)而不是扩池——扩池需要评估 shim 边界的成本。

---

## 3. 算法主体

### 3.1 数据结构

```go
// internal/fullmoon/trace/regalloc/state.go —— suggested location

type Loc uint8               // location descriptor (shared by snapshot and emitter)
const (
    LocNone    Loc = iota    // value not in reg / spill (compile-time const → LocConst)
    LocReg                   // value in physical register regID
    LocSpill                 // value in spill slot spillSlot
    LocConst                 // value is compile-time const, idx → const pool
    LocSunkRecipe            // value is a sunk object (unsink recipe index) — snapshot only, regalloc does not emit
)

type Assignment struct {
    Kind  Loc
    RegID uint8    // when Loc == LocReg, physical register id (GPR/FPR mixed, indexed by pool)
    Slot  uint16   // when Loc == LocSpill, spill slot ordinal (<<3 = byte offset)
    Idx   uint16   // when Loc == LocConst / LocSunkRecipe, index
}

// primary table: IR#i → Assignment (len == number of IR insns, one slot per IR value)
irAssign []Assignment

// reverse table: physical reg → currently bound IRRef (-1 = unbound)
regBind [numRegs]int32     // one for GPR, one for FPR

// spill usage bitmap: bit i = whether spill slot i is allocated (for reuse)
spillUsed bitmap.Bitmap
```

**注**:`RegID` 是 GPR/FPR「本池索引」——比如 amd64 GPR 池第 0 号 = rax,第 8 号 = r11;FPR 池第 0 号 = xmm0。全局唯一编号 = 池 ID + 池内索引;`Loc` 字段之外用 `RegKind` bit 区分(v1 简单起见,`Assignment` 加一个 `IsFP bool` 位,或者直接由 IR 的类型推断——IR 的 `Type` 字段见 03-ir-design)。

### 3.2 逆序主循环

伪码(伪 Go,建议实现在 `regalloc.Allocate`):

```go
func Allocate(tr *Trace) {
    initFreePools()                              // GPR / FPR pools start fully free
    // walk from trace tail to head
    for i := len(tr.IR) - 1; i >= 0; i-- {
        ins := &tr.IR[i]
        // 1. If IR#i's result currently holds some register (meaning earlier uses
        //    were already assigned), then the definition of IR#i is done — release
        //    that register and record the final Assignment for this IRRef (for the
        //    emit stage to back-fill store or propagate).
        //    If IR#i's result holds no register (no later use → dead), the DCE
        //    in 04-optimization-passes should have removed it; assert unreachable.
        if a := irAssign[i]; a.Kind == LocReg {
            releaseReg(a.RegID, isFP(ins.Type))
        } else if a.Kind == LocSpill {
            freeSpill(a.Slot)
        }
        // 2. Handle guard (when ins.IsGuard): all IRRefs referenced by the snapshot
        //    are considered "implicit uses" here — their liveness extends to this
        //    guard, and every IRRef in the snapshot must have an Assignment (§6).
        if ins.IsGuard {
            extendSnapshotLive(i, ins.SnapID)
        }
        // 3. Handle operands: for each IRRef operand of ins (op1 / op2 / ...),
        //    if that IRRef is not yet assigned (first-in-reverse = last use):
        //      pickReg or spill to allocate a location, record in irAssign.
        for _, opRef := range ins.Operands() {
            if opRef.IsConst() {
                irAssign[opRef.Idx] = Assignment{Kind: LocConst, Idx: opRef.ConstIdx}
                continue
            }
            if irAssign[opRef].Kind == LocNone {
                r, ok := allocReg(isFP(tr.IR[opRef].Type), hintFor(opRef))
                if !ok {
                    // pool empty — evict (§3.4)
                    r = evictAndReuse(isFP(tr.IR[opRef].Type))
                }
                irAssign[opRef] = Assignment{Kind: LocReg, RegID: r}
                regBind[r] = opRef
            }
        }
    }
    // done: all IRRefs have their Assignment in place; pools should be empty (assertion).
}
```

### 3.3 寄存器 hint

hint 是软性倾向,不是硬约束——用来减少 PHI move 和 helper 装参时多余的搬运:

- **PHI hint**(§4):PHI 的两个来源(loop 头 old value 和 loop 尾 new value)hint 到同一个寄存器,让分配器优先把它们放到同一个位置,PHI move 就变成 no-op。
- **helper 装参 hint**:helper 调用的第 i 个参数 hint 到这个架构的第 i 个参数寄存器(amd64 SystemV:rdi/rsi/rdx/rcx/r8/r9;arm64 AAPCS:x0-x7)。
- **返回值 hint**:helper 返回值默认在 rax / x0,IR「helper 返回」的结果 hint 到那个位置。

hint 命中率的实测由 PT-4 校准;不命中时 regalloc 就按池顺序正常分配。

### 3.4 驱逐策略:池空时选谁 spill

**LuaJIT 的启发式(06-snapshot-deopt §4「LuaJIT 式启发」的具体做法)**:池空时,选择**当前绑定的 IRRef 编号最低的那个**驱逐。这样做的动机:

- 在逆序扫描下,IRRef 编号最低 = 在 trace 中最早定义 = 一般来说「下一次(逆序意义上的下一次,也就是执行意义上的**更早**)使用」离得最远——启发式意义上损失最小。
- 实现极其便宜:池空时扫一遍 `regBind` 找 min 即可,O(池大小)。

**驱逐动作**:

1. 分配一个 spill 槽(§3.5),把被驱逐者的 Assignment 从 `{LocReg, r}` 改为 `{LocSpill, slot}`;
2. 在 emit 时,**从 spill 槽 load 到 reg 的 mov** 需要插在「这个 IRRef 的 use 点之前」——但 use 点相对逆序扫描是「已经走过的更晚位置」——所以 emit 阶段两遍(§7)时,forward emit 会在 use 点自然插入 load。
3. 释放 reg,分配给当前需要分配的新 IRRef。

**取舍**:LuaJIT 启发式不是最优——最优的「Belady's algorithm」需要预测「下一次(执行方向)使用距离」,在逆序扫描下相当于查「上一次(逆序方向)已经见到但未来还会用到」。P5 v1 沿用 LuaJIT 启发式,后续 PT-4 如果性能不足再评估。

### 3.5 spill 区落点:借用 JITContext.spillBase

**关键决策**——spill 存哪里,是本节要在 P4 现有 JITContext 上定下来的:

**候选方案**:

| 方案 | 位置 | 优点 | 缺点 |
|---|---|---|---|
| (a) goroutine 栈 | 分配器管 RSP 递增/递减 | 与 Go 栈同管理 | Go 抢占 / morestack 会搬 goroutine 栈,mmap 段跑时不能容忍——必须先 syscall 迁栈,P5 mmap 段承受不了 |
| (b) 每-trace mmap 数据页 | 独立 mmap RW 页,记入 trace metadata | 与代码页对齐、生命周期相同 | 增加 mmap 数量,回收路径复杂 |
| **(c) 借用 JITContext.spillBase**(**推荐**) | JITContext 里已经有的 `spillBase uintptr`(见 [../p4-method-jit/05-system-pipeline.md](../p4-method-jit/05-system-pipeline.md) §3.3 第 4 组「自管机器栈 backing」),指向一段 Go 堆分配的 `[]uint64` | 复用 P4 已有基础设施;寻址一次 `[r15+spillBaseOff]` → base;Go GC 视为普通 []uint64 数组 | 需要在 trace 编译期定下 spill 区大小上限(spillCap) |

**推荐 (c)**:P4 已经有 `spillBase` 字段(PJ10 阶段保留但没有真正切 SP);P5 直接把它「正式启用」作为 trace 的 spill 区。每个 trace 编译时定 spillCap(v1 建议 64 个 slot × 8 bytes = 512 字节,超出就拒绝 trace 或触发 v2 优化)。

**寻址方式(amd64)**:

```
;; store IRRef#k into spill slot#s:
mov  r11, [r15 + spillBaseOff]     ;; load base once (scratch r11)
mov  [r11 + 8*s], <regK>           ;; store

;; reload from spill#s to reg:
mov  r11, [r15 + spillBaseOff]
mov  <regK>, [r11 + 8*s]
```

arm64 同构(x9 作 scratch)。**优化**:在直线段内可以把 spillBase 缓存到某个 scratch 寄存器(但注意 shim 调用之后 clobber,需要重新 load)——这是 emit 阶段的窥孔优化,不进 v1。

**GC 安全**:spill 存放的是 NaN-box `uint64`——如果某个 slot 装了 GCRef 类的值,GC 根扫描需要能看到这些槽。**这是 P5 和 P4 的关键差异**:P4 spill 承诺不跨字节码边界、不跨 safepoint,所以 GC 看不到 spill;P5 spill 会跨字节码边界和 back edge safepoint,**必须让 GC 能扫描 spill 区**。方案:trace 编译时生成「spill live map @ safepoint」——每个 safepoint 处,哪些 spill 槽装着 GCRef 类值。**这是 GC 联动的开放问题**(§9),留给 PT-4 与 06 snapshot 联合评估。

---

## 4. PHI 处理:循环 back edge 的 parallel move

### 4.1 loop 头 PHI 的语义

见 [03-ir-design.md](./03-ir-design.md),loop trace 在 head 处有 PHI:

```
IR#p: PHI  ir_entry_val,  ir_loop_body_new_val    ;; loop head phi
```

`ir_entry_val` 是 trace 首次进入时的初值(比如 for i = 1 里的 i=1),`ir_loop_body_new_val` 是循环体末尾算出来的新值(比如 i = i + 1)。语义上,back edge 发生时,PHI 的两个来源需要汇入同一个「i」——分配器需要把它们放到**同一个物理位置**,否则每次 back edge 都要一次 move。

### 4.2 用 hint 让 PHI 尽量变成 no-op

**分配策略**:

1. 逆序扫描先见到循环体末尾的 `ir_loop_body_new_val`——它是循环体最后一条 IR 之一,分配一个寄存器,记作 `regNew`;
2. 继续逆序走,走到 `IR#p: PHI` 时,PHI 的两个操作数 hint 都指向 `regNew`;
3. `ir_entry_val` 的分配随后(继续逆序走到它)见到 hint,尝试同样分配 `regNew`——如果 `regNew` 此时空闲,分配成功,PHI 就变成 no-op。

**hint 失败的情况**:`regNew` 在扫描到 `ir_entry_val` 时已经被别的 IRRef 占用——那么 `ir_entry_val` 分配到另一个 reg,需要在 back edge 处插一次 move,让 `regNew` 与 `ir_entry_val` 交换或复制。

### 4.3 parallel move 与循环消解

多个 PHI 同时存在(典型 for 循环有 idx / limit / step 三个),各自的 old→new 可能形成搬运图:

```
PHI 1: reg_a  ←  reg_b   ;; (back edge: reg_b 的值当作 reg_a 使用)
PHI 2: reg_b  ←  reg_a   ;; (同时:reg_a 的值当作 reg_b 使用)
```

这形成了 2 个环。**parallel move 消解**:

1. 拓扑排序搬运图。没有环就直接按顺序 emit move。
2. 有环就用一个 scratch reg 做「三点交换」:选一个不参与环的空闲 reg(或临时借 spillBase 的一个 slot)。

伪码:

```go
func emitParallelMove(moves []Move, scratch Reg) {
    // moves: list of {dst, src}
    for len(moves) > 0 {
        // find a move whose dst is not another move's src, emit first (no hazard)
        idx := findLeafMove(moves)
        if idx >= 0 {
            emit(moves[idx])
            moves = removeAt(moves, idx)
            continue
        }
        // only cycles remain — break one with scratch
        emit(Move{scratch, moves[0].src})
        moves[0].src = scratch
    }
}
```

**成本**:循环体内平均少于 3 个 PHI,消解开销可以忽略;实测由 PT-4 确认。

---

## 5. 跨调用点:两条通道的 clobber 集

P5 mmap 段调 Go 世界有两条通道(见 [../p4-method-jit/05-system-pipeline.md](../p4-method-jit/05-system-pipeline.md) §4.3):

- **通道 (a) exit-reason(主通道,主协议)**:JIT 写 `exitReasonCode` + `exitArg0` + `resumeOff`,直接 `ret` 出段,Go dispatcher `switch helperCode` 分派,dispatch 完 `RefreshJitCtxAddrs` + 经 `codePageAddr + resumeOff` 重入。
- **通道 (b) shim 直接 call(次通道,历史遗留)**:mmap 段 emit `call <shimAddr>`,按 ABIInternal 序列传参。

**对分配器的核心影响**:两条通道都是**跨 Go 边界**——所有活值必须在跨边界前**离开寄存器**,存到「Go dispatcher 和 mmap 段共享的」位置。

### 5.1 通道 (a) exit-reason 的 clobber

**约定**:每次 `ret` 出段(exit-reason),mmap 段假设**所有物理寄存器**(除了 P5 独占的 r15/rbx-x27/x26,以及 Go 自动保留的 r14/x28)在重入时都可能被 Go 侧改动。

**分配器必须做的事**:

1. `ret` 出段前,所有当前活着的 IR 值(在寄存器或 spill 都行)——**在寄存器里的必须先 spill 到 `spillBase` 槽**(或者写到栈槽,如果值已经就位);
2. 回段之后(`codePageAddr + resumeOff`),需要用到的 IR 值从 spill reload 到 reg;
3. `rbx` / `x26`(valueStackBase)必须重新装载(见 P4 backends §4.1.2):Go dispatcher 在 refresh 之后可能改动这两条基址,重入前分配器 emit `mov rbx, [r15+valueStackBaseOff]`(amd64)或 `ldr x26, [x27, #vsBaseOff]`(arm64)。
4. **arenaBase / valueStackBase / ciDepthAddr / ciSegBaseAddr / topAddr** 五个基址字段,在 dispatcher 调用 `RefreshJitCtxAddrs` 之后已经刷新到 JITContext 里——mmap 段照常从 `[r15+*]` 间接寻址即可(P5 沿用 P4「不缓存 arenaBase」的纪律)。

**clobber 集大小**:通道 (a) 假设**全部 caller-saved 寄存器都 clobber**(等价于「彻底出段」)。分配器把 exit-reason 视为「trace 分段边界」——两段之间寄存器状态不互通,类似 P4 的 helper 调用点但更彻底。

### 5.2 通道 (b) shim 直接 call 的 clobber

**约定**:shim 调用是普通的 Go ABIInternal `call` 指令,寄存器保留规则遵循 ABIInternal:

**amd64 shim clobber(见 P4 backends §4.1.5 和 emit_shim_amd64.go 头注)**:

| 寄存器 | shim 调用之后的状态 |
|---|---|
| `r15` | Go 保留(callee-saved by ABIInternal) → **survive** |
| `rbx` | ABIInternal arg1 slot,shim 之后 clobber → **需要 reload** `[r15+vsBaseOff]` |
| `r14` | shim 前需要 `mov r14, [r15+savedGoGOff]` 恢复 G;shim 后 Go 保留 → survive as G |
| `rax` `rcx` `rdx` `rsi` `rdi` `r8`-`r11` | caller-saved → **全部 clobber** |
| `xmm0`-`xmm15` | caller-saved → **全部 clobber** |
| `rbp` `r12` `r13` | Go 保留 → survive(但 P5 v1 不使用,见 §2.1.1) |

**arm64 shim clobber**:

| 寄存器 | shim 调用之后的状态 |
|---|---|
| `x27` | Go 保留 → survive |
| `x26` | 头注注明 caller-saved,shim 之后 clobber → **需要 reload** |
| `x28` | Go 保留 as G → survive |
| `x0`-`x18` `v0`-`v31` | 全部 caller-saved → **全部 clobber** |
| `x19`-`x25` | Go 保留 → survive(P5 v1 不使用) |

**分配器必须做的事(shim 通道)**:

1. shim 调用前:所有活值中,处于 clobber 集寄存器里的,spill 到 spillBase;处于 survive 寄存器里的活值(v1 不使用,不涉及)不动。
2. shim 调用前(仅 amd64):emit `mov r14, [r15+savedGoGOff]` 恢复 G。
3. shim 调用后:emit `mov rbx, [r15+vsBaseOff]`(amd64)或 `ldr x26, [x27, #vsBaseOff]`(arm64)重新装载 vsBase。
4. shim 调用后:需要用的活值从 spill reload 到 reg。

**风险**:见 P4 backends §8.1 和 issue #38——shim 通道在嵌套 + 并发压力下已知易碎。**P5 建议:v1 完全不使用 shim 通道,所有 host 调用都走 exit-reason 通道**(与 PJ10 决定相同,见 P4 05 §4.3.1a 头注「新 op 一律走通道 (a)」)。这样 clobber 集就简化为「exit-reason 分段边界」一种。

### 5.3 clobber 集小结表

| 场景 | 分配器视为 | 需要 spill 的活值 |
|---|---|---|
| trace 直线段内(没有 host call) | 无 clobber | 无 |
| **exit-reason `ret`(推荐主通道)** | 全部 caller-saved clobber | **所有活值** |
| shim `call`(v1 不启用) | 全部 caller-saved clobber(同上) | 所有活值 |
| guard(条件跳到 exit 之外)** | 无 clobber(直线继续) | 无(但见 §6:snapshot 引用扩展活性) |

** guard 不是 clobber 点,只是 exit 分岔点:guard 失败走 exit stub → 出段(这时按 exit-reason 主通道 clobber);guard 成功继续直线,不动寄存器。

---

## 6. 与 snapshot 的配合(THE 硬约定,与 06 双向对接)

### 6.1 核心不变式

**P5 regalloc / snapshot 联合不变式**:

> **guard 处,snapshot 引用的每个 IRRef 都必须「可以恢复」——即:该 IRRef 的 Assignment 在 guard 时是 {LocReg | LocSpill | LocConst | LocSunkRecipe} 之一,并且这个位置的物理状态在 guard 触发瞬间与 IR 语义一致**。

这是 06 snapshot deopt 的物理前提。regalloc 的责任是**在整个逆序扫描过程中始终保持这个不变式成立**——从每个 guard 起,snapshot 引用的所有 IRRef 都视为「活到 guard 处的隐式使用」。

### 6.2 snapshot 扩展活性:逆序扫描的处理

见 §3.2 主循环 step 2:

```go
if ins.IsGuard {
    for _, ref := range tr.Snapshots[ins.SnapID].Entries {
        if ref.Kind == SnapRefIRRef {
            // if this IRRef has no assignment yet, assign now (treated as a use at this guard)
            if irAssign[ref.IRRef].Kind == LocNone {
                r, ok := allocReg(isFP(tr.IR[ref.IRRef].Type), noHint)
                if !ok { r = evictAndReuse(isFP(tr.IR[ref.IRRef].Type)) }
                irAssign[ref.IRRef] = Assignment{Kind: LocReg, RegID: r}
                regBind[r] = ref.IRRef
            }
            // if already assigned, leave alone (keep it until this IRRef's definition frees it)
        }
    }
}
```

**推论**:snapshot 引用的 IRRef 与普通操作数同权——一起分配、一起驱逐、一起竞争寄存器池。这样自然就实现了「snapshot 引用扩展了寄存器的持有期」。

### 6.3 exit stub metadata 的格式(regalloc → 06 的交付)

每个 guard 需要产出一份「exit 时的 location map」——供 06 的 Go dispatcher 按此读回值。**建议格式**(与 06 §7 register dump area 设计对齐):

```go
// internal/fullmoon/trace/snapshot.go —— delivered to 06

type SnapEntry struct {
    Slot uint16  // target interpreter stack slot (bytecode reg number, see [../p1-interpreter/05] §1.2)
    Ref  IRRef   // IR value to restore into this slot (or const / sunk-recipe)
}

type ExitStubMeta struct {
    GuardID    uint16
    SnapID     uint16                // index into shared snapshot table (delta-encoded, see 06 §5)
    // per-IRRef Assignment snapshot (its location at this guard)
    // filled by regalloc, read by 06 dispatcher
    Locs       []Assignment          // 1:1 with tr.Snapshots[SnapID].Entries
}
```

**为什么需要 per-guard 复制一份 `Locs`**:同一个 IRRef 在不同的 guard 处 Assignment 可能不同(比如中间被 spill 了 → LocReg 变成 LocSpill,或反过来 reload)——每个 guard 对应的位置必须冻结到该 guard 的 metadata,而不是共用 IRRef 层的全局 Assignment。

**规模估算**:trace ≤ 4000 IR 指令,guard 密度 ~1/5-10 → ~500 guards;每个 guard snapshot 引用 ~10 个 IRRef → 每个 guard 10 × sizeof(Assignment) ≈ 40 字节;总 ~20 KB per trace,可以接受。**压缩机会**在 06 §5(snapshot delta 编码)——两条相邻 guard 的 Locs 大概率高度重合。

### 6.4 register dump area(与 06 §7 的对应设计)

06 §7 提议「所有 caller-saved 寄存器在 exit stub 里 bulk spill 到 JITContext 里一段固定 dump area,Go dispatcher 从 dump + spillBase 统一读位置」。regalloc 对应的责任:

- **不做特别的事**——因为 exit stub 是「保守假设 exit 时所有寄存器都要保留」的路径:stub 里 emit 一段固定的 `mov [r15+dumpOff+0], rax; mov [r15+dumpOff+8], rcx; ...` 序列(每个 guard 复用同一段 dump code,通过 jmp 跳到共享 dump stub 实现,减少代码膨胀)。
- **regalloc 出的 `ExitStubMeta.Locs`**:LocReg 的位置解释为「从 dump area 对应偏移读回」,LocSpill 的位置解释为「从 spillBase 读」——Go dispatcher 用同一段代码处理(§6.3)。

这个设计让 regalloc 不需要为每个 guard 生成定制 exit code——**exit stub 是通用的,per-guard 的差异只在 metadata**。这是 P5 相对于「per-guard 定制 exit code」的关键简化(与 LuaJIT 大体相同)。

---

## 7. 与代码发射的接口

### 7.1 两条路线

- **路线 A(推荐 v1):regalloc 与 emit 分两遍**——先逆序跑 regalloc 得到 `irAssign[]`,再正序跑 emit 遍历 IR,每条 IR 处按 `irAssign` 查位置发射对应机器码。
- **路线 B(LuaJIT 的做法,v2 优化):fused 逆序 emit**——一遍从 trace 尾往头把机器码发射到 buffer 尾部,写完之后 reverse buffer。省一次遍历,但需要 emit 的 code buffer 支持「从后往前写」+ 前向跳转回填(逆序发射时,跳转目标已经 emit 过了)。

**推荐 v1 走路线 A** 的理由:

1. **复用 P4 现有的 codebuf/label 机制**:P4 有 `internal/gibbous/jit/peroptranslator/codebuf.go` 和 label resolver(见 project_pj10_native_longtask 备忘),已经是正序 emit。P5 沿用同样的正序发射,复用度高。
2. **调试可读**:两遍分离,regalloc 的 assignment 表可以 dump 出来看,emit 的问题与 regalloc 的问题分开。
3. **性能损失微小**:多一遍 IR 遍历是 O(n),相对 emit 本身的成本可以忽略。

**路线 B 何时值得**:P5 编译时间成为瓶颈时(v3+)。06-snapshot-deopt §4 提到「LuaJIT 单遍 regalloc」——LuaJIT 是 fused 的,而且从末尾往前发射;望舒 v1 不必立刻做到这一步。

### 7.2 emit 接口草案

沿用 P4 backends §2.4 的 Emitter interface 思路,P5 加一层 `traceEmitter`:

```go
type TraceEmitter interface {
    EmitTraceProlog(entryLocs []Assignment) // load IR#1..IR#k initial values from entry snapshot to regs/spills
    EmitIR(ir *IRIns, assign Assignment, operandLocs []Assignment) // emit a single IR
    EmitGuard(ir *IRIns, meta *ExitStubMeta)                       // guard + associated exit stub metadata
    EmitLoopHead()                                                 // loop head label
    EmitLoopTail(phis []PHIMove)                                   // loop back edge parallel move + jmp
    EmitTraceEpilog()                                              // trace exit (link to interpreter / side trace)
    Finalize() (*TraceCode, error)
}
```

per-arch 实现(amd64/arm64)沿用 P4 的分包组织(`internal/fullmoon/trace/amd64` / `arm64`)。

---

## 8. 调试与验证

### 8.1 regalloc trace dump

标准输出格式(建议在 `-dump=regalloc` 构建 tag 下开启):

```
=== TRACE #7 regalloc dump ===
IR#0    LOADK    k=1.0        →  xmm0        [const-hoisted from entry snap slot 3]
IR#1    LOAD_R   slot=1       →  xmm1        [entry snap slot 1]
IR#2    FADD     IR#1, IR#0   →  xmm1        [reuse dst = op1]
IR#3    GUARD_ISNUMBER IR#1   →  (no assign; snap ref = IR#1 @ xmm1)
IR#4    STORE_R  slot=1, IR#2 →  (mem write; xmm1 live-out to next iter as PHI src)
...
SPILL used: 0/64
GPR pool max concurrent: 3
FPR pool max concurrent: 5
```

### 8.2 snapshot 可恢复性 verifier(build-tag 测试)

**建议实现为 `internal/fullmoon/trace/verify_regalloc.go`**(在 build tag `verify` 下启用):

```go
func VerifyRegalloc(tr *Trace) error {
    for i, ins := range tr.IR {
        if !ins.IsGuard {
            continue
        }
        snap := tr.Snapshots[ins.SnapID]
        for _, ref := range snap.Entries {
            if ref.Kind != SnapRefIRRef {
                continue
            }
            a := tr.PerGuardLocs(i, ref.IRRef)
            if a.Kind == LocNone {
                return fmt.Errorf("guard @IR#%d: snap ref IR#%d unrecoverable", i, ref.IRRef)
            }
            // cross-check: LocReg's regID not held by any other IRRef at this guard
            // LocSpill's slot not held by any other IRRef at this guard
            if err := crossCheck(tr, i, ref.IRRef, a); err != nil {
                return err
            }
        }
    }
    return nil
}
```

fuzz 与差分主套(见 [08-testing-strategy.md](./08-testing-strategy.md))应当在 verify build tag 下跑,任何违反 §6.1 不变式的分配立刻 fail——这是防止「静默的错误结果」的第一道 assertion 防线(见 06-snapshot-deopt §4「snapshot 正确性无法靠评审保证」)。

### 8.3 与 06 的联合断言

- **guard 元数据一致性**:regalloc 出的 `ExitStubMeta.Locs.length == snapshot.Entries.length`(每个 snap entry 有一份 Loc)。
- **register dump area 大小上限**:所有 caller-saved GPR + FPR 的字节数——amd64:9 GPR × 8 + 16 FPR × 8 = 200 字节;arm64:16 GPR × 8 + 32 FPR × 8 = 384 字节。dump area 是 JITContext 里一段固定块,大小编译期定。

---

## 9. 开放问题

- **r12/r13/rbp(amd64)与 x19-x25(arm64)是否纳入 IR 池**(§2.1.1 / §2.2):v1 保守不使用;PT-4 spike 如果频繁撞到池空再评估。
- **驱逐启发式**(§3.4):LuaJIT「lowest IRRef」在望舒 trace 形状下是否最优;或者改用「距离下次(执行方向)使用最远者」这类更精细的启发。留给 PT-4 微基准比较。
- **spill 区大小与 GC 联动**(§3.5 末):spill 装 GCRef 时,GC 根扫描如何看见 spill——「spill live map @ safepoint」的具体表示形式与 06 §9 unsink-GC 联合评估。
- **fused emit 何时值得**(§7.1):编译时间成为瓶颈时才启用;v1 不做。
- **PHI hint 的成功率**(§4.2):PT-4 实测循环体 PHI 变成 no-op 的比例;如果偏低,评估更强的 hint 传播(比如反向传播 hint 穿多个 use)。
- **多 State 并发下的 spillBase 生命周期**:P5 mmap 段跨 State 是否复用——如果每个 State 独立 JITContext,spillBase 各自一份,不冲突;如果共享,就需要锁。P4 现在的 `spillBase` 语义与多 State 复用协议留给 06 与 07 联合确认。
- **register dump area 是每 trace 一份还是全局共享**:每 trace 一份(挂在 trace metadata 或 JITContext 的 per-trace 区)更简单;全局共享省内存但要求 exit 时刻不重入。建议每 trace,PT-4 交付时定下来。

---

相关:
[./00-overview.md](./00-overview.md)(00-overview §2 / §4.4,本文承接 ③ regalloc 展开与「中偏难」评估) ·
[00-overview.md](./00-overview.md)(v1 范围 / PT-4 阶段 gate) ·
[03-ir-design.md](./03-ir-design.md)(SSA IR / IRRef / guard 位,分配器输入) ·
[04-optimization-passes.md](./04-optimization-passes.md)(优化后的 IR 交给 regalloc / snapshot 引用是 DCE 根) ·
[06-snapshot-deopt.md](./06-snapshot-deopt.md)(与本文互为约定的两端,§6 双向配合) ·
[07-system-integration.md](./07-system-integration.md)(与 gibbous / crescent 的层间协议) ·
[08-testing-strategy.md](./08-testing-strategy.md)(verifier + fuzz + deopt 注入,§8 验证接入) ·
[../p4-method-jit/06-backends.md](../p4-method-jit/06-backends.md)(post-PR-#42 寄存器约定,§2 池切分的事实来源) ·
[../p4-method-jit/05-system-pipeline.md](../p4-method-jit/05-system-pipeline.md)(JITContext 字段 / exit-reason 协议,§3 spillBase / §5 clobber 集依据) ·
[../p4-method-jit/04-osr-deopt.md](../p4-method-jit/04-osr-deopt.md)(P4 exit stub 静态生成,与本文 §6 的对应设计) ·
[../p1-interpreter/01-value-object-model.md](../p1-interpreter/01-value-object-model.md)(NaN-box 值表示) ·
[../p1-interpreter/05-interpreter-loop.md](../p1-interpreter/05-interpreter-loop.md)(CallInfo / 值栈布局,deopt 落点)
