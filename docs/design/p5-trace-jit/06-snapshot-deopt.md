# P5 §6:snapshot + deopt 机器——最难的一块

> 状态:**未立项图纸(启动判定见 [01](./01-launch-judgment.md))**。本文把种子 §4「snapshot + deopt 机器:最难的一块」逐节展开为可以施工的机器设计。§1-§4 严格对应种子 §4.1-§4.4(问题 / 概念方案 / P4-vs-P5 复杂度对照 / 无处抄难度评估),§5 及之后是详细设计增量(snapshot 压缩、deopt 执行路径、exit stub metadata、side trace、正确性)。**这一章是 P5 的核心难点**——种子明确点名「LuaJIT 真正的护城河,无处抄」,+2-4 人年开放式投入的主成分正是本机器的正确性收敛时间。
>
> 对应 Go 包:`internal/fullmoon/trace`(snapshot 生成)+ `internal/fullmoon/trace/deopt`(建议命名,Go 侧 dispatcher 与 unsink 驱动)。
>
> 上游依据:
> [./00-overview.md](./00-overview.md) §4(原种子 §4.1-§4.4 已合并到本文 §1-§4;§4.4「+2-4 人年开放式」的主成分);
> [00-overview.md](./00-overview.md)(v1 范围包含 snapshot;v2 gate = sink;v3 gate = side trace);
> [03-ir-design.md](./03-ir-design.md)(IRRef / IRIns / guard 位 / 常量池——snapshot entry 的引用形式);
> [04-optimization-passes.md](./04-optimization-passes.md)(snapshot 引用是 DCE 根,sink 优化产生 sunk-recipe——本文 §5-§6 消费);
> [05-register-allocation.md](./05-register-allocation.md)(与本文互为约定的两端,每个 guard 的 Locs 由 regalloc 出稿,本文 dispatcher 消费)。
>
> P4 依赖面(资产与对照):
> [../p4-method-jit/04-osr-deopt.md](../p4-method-jit/04-osr-deopt.md)(P4 薄 deopt 的栈槽真相不变式——本文 §1 论证这个不变式在 P5 被拆毁;§3 复杂度对照的对应面);
> [../p4-method-jit/05-system-pipeline.md](../p4-method-jit/05-system-pipeline.md) §3.3(JITContext 字段包含 `exitReasonCode` / `exitArg0` / `resumeOff` / `codePageAddr`——本文 §6 复用);§4.3(exit-reason 协议、Go dispatcher 循环、`RefreshJitCtxAddrs`——本文 §6 dispatcher 沿用相同模式);
> [../p4-method-jit/06-backends.md](../p4-method-jit/06-backends.md) §4(post-PR-#42 寄存器约定——本文 §7 register dump area 与之协作);
> `internal/gibbous/jit/jitcontext.go`(`ExitOSR = 2` 常量——本文 §6 复用作为 P5 deopt 的 status)。
>
> P1 依赖面:
> [../p1-interpreter/05-interpreter-loop.md](../p1-interpreter/05-interpreter-loop.md) §1(CallInfo 布局:4 字 = 32 字节,`base` / `funcIdx` / `savedPC` / `top` / `protoID` / `nresults`——本文 §2 frames[] 重建目标);
> [../p1-interpreter/01-value-object-model.md](../p1-interpreter/01-value-object-model.md)(NaN-box `uint64`——snapshot 物化写栈槽的形式);
> [../p1-interpreter/06-memory-gc.md](../p1-interpreter/06-memory-gc.md)(自管 mark-sweep GC——unsink 期间可能触发 GC,本文 §6.4 交互)。
>
> 姊妹章节:[05-register-allocation.md](./05-register-allocation.md)(regalloc 保证 snapshot 引用可以恢复——本文所有 §5-§7 的物化都以 05 §6 的 `ExitStubMeta.Locs` 作为输入)。

---

## 0. 定位:P5 核心难点的 1600 字总纲

**P5 的三项核心优化(regalloc / 内联 / sink)逐条拆毁 P4 的「栈槽真相不变式」**(见 [../p4-method-jit/04-osr-deopt.md](../p4-method-jit/04-osr-deopt.md) §3.1):

- P4:每条字节码边界处,全部 Lua 活值都在 arena 值栈槽——guard 失败时物化集合为空,exit stub 三步 O(1)。
- P5:活值可能在机器寄存器 / spill / 常量 / 未真实分配的 sunk 对象里——guard 失败时物化集合不为空,exit stub 需要重建 1..N 帧真相。

这是 +2-4 人年的主成分,也是「LuaJIT 真正的护城河,无处抄」的具体含义——LuaJIT 的 IR + snapshot + regalloc 三者互为前提高度耦合,精妙之处只能读懂原理后按望舒的约束重新设计,不能移植(06-snapshot-deopt §4)。

**章节路标(§1-§4 与种子严格对应)**:

| § | 与种子对应 | 内容 |
|---|---|---|
| §1 | 种子 §4.1 | 问题:三项优化如何拆毁栈槽真相(每条给一个具体 Lua 例子) |
| §2 | 种子 §4.2 | 概念方案:snapshot 结构与 deopt 步骤(展开到可以施工) |
| §3 | 种子 §4.3 | P4-vs-P5 复杂度对照表(种子表 + 若干增量行) |
| §4 | 06-snapshot-deopt §4 | 「无处抄」逐组件难度表 + 2-4 人年判断依据 |
| §5 | 增量 | snapshot 生命周期 + delta 压缩(内存预算数学) |
| §6 | 增量 | deopt 执行路径(guard → stub → dispatcher → 物化 → unsink → 续跑,逐步展开) |
| §7 | 增量 | register dump area 设计 + exit stub metadata 格式(与 05 §6 对应) |
| §8 | 增量(v3) | side trace 生长(exit 变热的 tree growth,格式必须兼容) |
| §9 | 增量 | 正确性策略(deopt 注入 + verifier + bug 衰减曲线) |
| §10 | 增量 | 开放问题 |

---

## 1. 问题:trace 中途退出,真相已经被优化拆散(种子 §4.1 展开)

### 1.1 三条真相拆毁通道

P4 deopt 之所以薄,是因为「每条字节码边界的栈槽就是真相」。P5 的三项核心优化各自拆毁这个前提:

#### 通道 A:寄存器分配 → 活值不在栈槽

**Lua 源(典型的循环内标量算术)**:

```lua
local sum = 0
for i = 1, n do
    sum = sum + i          -- 循环体
end
return sum
```

**trace 执行序列**(概念形式,见 [03-ir-design.md](./03-ir-design.md)):

```
IR#1: LOAD_R  R(sum_slot)           →  假设分配到 xmm2  (见 05 §3)
IR#2: LOAD_R  R(i_slot)             →  分配到 xmm3
IR#3: GUARD_ISNUMBER IR#1            (snap ref: sum @ xmm2, i @ xmm3, ...)
IR#4: GUARD_ISNUMBER IR#2
IR#5: FADD    IR#1, IR#2            →  xmm2(reuse dst)
;; --- 循环体末尾,PHI 让 sum_new = xmm2 back edge ---
;; --- 关键:栈槽 R(sum_slot) 从循环开始就没被 store,一直只在 xmm2 里 ---
```

**如果 IR#3 的 guard 失败**(假设某轮 i 变成 string,虽然本例语法上不会发生,但可能出现在别的循环形式里):

- 栈槽 `R(sum_slot)` 里存的是**循环开始时的初值** = 0,而不是**当前迭代应该的 sum 值**(可能已经累加到 42)——**栈槽是过期真相**。
- 当前的 sum 值在 xmm2 里。deopt 必须把 xmm2 里的值写回 R(sum_slot),否则续跑用 0 继续算——就成了静默的错误结果。

**这是 P5 regalloc 拆毁栈槽真相的直接后果**。P4 因为每条字节码结束都强制 store 回栈槽,不存在这个问题(P4 每轮循环末尾都写 R(sum_slot) = sum_new)。

#### 通道 B:trace 内联 → 多帧从未物理存在

**Lua 源(循环体调用小函数)**:

```lua
local function add(a, b) return a + b end
local sum = 0
for i = 1, n do
    sum = add(sum, i)      -- 每轮跨函数调用 add
end
```

**trace 内联之后**(种子 §1.2 描述的场景):

```
;; add 的调用被内联,一条 trace 跨两个 Lua 帧:
;;   outer 帧:for 循环所在的 chunk
;;   inner 帧:add 函数
;; 但 inner 帧的 CallInfo 从未压过 —— trace 直接把 IR 铺开
IR#k:   LOAD_R  R(sum_slot, outer)
IR#k+1: LOAD_R  R(i_slot, outer)
IR#k+2: FADD    IR#k, IR#k+1                 ;; 相当于 add 的 body
IR#k+3: STORE_R R(sum_slot, outer), IR#k+2   ;; 相当于外层 sum = ...
;; 从未 push add 的 CallInfo,从未真的调 add
```

**如果 IR#k+2 处埋了 guard 而且失败**:

- 从解释器的角度,当前应该在 `add` 函数体内(pc 指向 add 的 ADD 指令),CallInfo 栈应该有:outer(chunk)+ inner(add)两层。
- **但实际调用栈**:只有 outer 的 CallInfo(inner 从未压)。
- deopt 必须**重建 inner 帧的 CallInfo**——base、funcIdx、savedPC、protoID、nresults(见 [../p1-interpreter/05-interpreter-loop.md](../p1-interpreter/05-interpreter-loop.md) §1.2 布局)——从 snapshot 里的 `frames[]` 元数据推出来。

**这是 P5 trace 内联拆毁栈槽真相的直接后果**。P4 的编译单元是单 Proto,每帧都有真实的 CallInfo,exit 不需要补建。

#### 通道 C:分配下沉(sink)→ 对象物理上不存在

**Lua 源(循环内构造临时对象)**:

```lua
for i = 1, n do
    local pt = {x = i, y = i * 2}    -- 每轮构造 table
    consume(pt.x + pt.y)              -- 用完即弃
end
```

**优化之后**(见 [04-optimization-passes.md](./04-optimization-passes.md) sink 优化):

如果 `pt` 不逃出 trace(consume 不保留 pt 引用),04 会把 NEWTABLE 与两次 SETTABLE 「下沉」——不真的分配 table,把字段 `pt.x` 与 `pt.y` 拆成 IR 值直接参与后续计算:

```
;; sink 之前:
IR#a:   NEWTABLE                     →  某个 table 对象
IR#a+1: SETFIELD IR#a, "x", IR#i     →  写 pt.x
IR#a+2: SETFIELD IR#a, "y", IR#i*2   →  写 pt.y
IR#a+3: GETFIELD IR#a, "x"           →  读 pt.x
IR#a+4: GETFIELD IR#a, "y"           →  读 pt.y

;; sink 之后:
;; NEWTABLE 与 SETFIELD 都消除,直接用 IR#i 与 IR#i*2 参与后续算术
;; snapshot 里记「sunk recipe」:{type=Table, fields=[("x", IR#i), ("y", IR#i*2)]}
```

**如果这段代码里某处 guard 失败**:

- 从解释器的角度,pt 应该是一个真的 table 对象,住在 arena 里,GC 可见。
- **但实际上 pt 从未分配**——字段值散在 IR#i 和 IR#i*2 里。
- deopt 必须**按 sunk-recipe 真的分配 table + 填字段**(unsink)——期间可能触发 GC(自管 mark-sweep,见 [../p1-interpreter/06-memory-gc.md](../p1-interpreter/06-memory-gc.md))——填字段时可能又需要读其他 sunk 对象的引用——链式依赖必须小心处理(§6.4)。

**这是 P5 sink 拆毁栈槽真相的直接后果**。P4 不做 sink,所有对象都真实分配,exit 不需要 unsink。

### 1.2 三通道叠加的最坏情形

单独任一通道已经复杂;三通道叠加(regalloc + 内联 + sink 出现在同一条 trace 里)对 deopt 的要求:

- 某 guard 失败时,活值可能在**寄存器 / spill / 常量 / sunk-recipe(引用其他 sunk 对象)**四种位置之一;
- 需要重建 **1..N 帧** CallInfo(内联深度);
- unsink 时可能触发 GC,GC 根扫描期间已经物化的槽必须可见——**根可见性的顺序**是本机器最深的坑(§6.4)。

**逐字节正确性**:deopt 之后从 exitPC 续跑的行为,必须与「一路解释跑到 exitPC 时的状态」逐字节一致——任何槽映射错、unsink 漏字段、CallInfo 帧错——都是**静默的错误结果**。差分主套(见 [08-testing-strategy.md](./08-testing-strategy.md))是唯一的防线。

---

## 2. 概念方案:snapshot 是「解释器状态」的稀疏映射(种子 §4.2 展开)

### 2.1 种子伪码(见种子 §4.2)

```
snapshot@guard_k:
  exit_pc       : 回到解释器的字节码 pc(可能在被内联的函数体内)
  frames[]      : 逻辑帧链 [(protoID, base 偏移, 返回 pc), ...] —— 重建 CallInfo 用
  slots{}       : 稀疏映射 { 解释器栈槽号 → IR 引用 | 常量 | sunk 对象重建配方 }
                  (只记从 exit_pc 起活跃的槽;死槽不记)
deopt(guard_k):
  1. 按 frames[] 补建被内联函数的 CallInfo 链(05 §1.2 布局)
  2. 按 slots{} 物化:寄存器/spill → NaN-box u64 写回 arena 栈槽;
     sunk 对象先真实分配再填字段(unsink)
  3. trampoline 出 JIT 世界,crescent 从 exit_pc 续跑
```

### 2.2 formalize:结构定义

**建议的 Go 类型草案**(放在 `internal/fullmoon/trace/snapshot.go`):

```go
type Snapshot struct {
    ExitPC   uint32       // logical resume pc (possibly inside an inlined inner proto)
    Frames   []SnapFrame  // logical frame chain; len == inlining depth (1..N)
    Entries  []SnapEntry  // sparse slot map (dead slots excluded)
    // compression support (§5):
    PrevSnapID  int32     // -1 = full; >=0 = delta based on that snap
    DeltaOnly   bool      // true → Entries is delta relative to Prev
}

// SnapFrame captures everything needed to rebuild one CallInfo frame
type SnapFrame struct {
    ProtoID  uint32   // see [P1 05] §1.2 word2[31:0]
    BaseOff  int32    // absolute offset of R0 in thread.valueStack
    Nresults uint16   // see [P1 05] §1.2 word2[47:32]
    SavedPC  int32    // return pc for outer inlined frames; innermost uses ExitPC
    FuncIdx  int32    // = BaseOff - 1 (see [P1 05] §1.2 convention; stored redundantly for fast rebuild)
}

// SnapEntry describes where one interpreter stack slot's value comes from
type SnapEntry struct {
    Slot  uint16      // target slot number (bytecode reg id, relative to frame's BaseOff)
    FrameIdx uint8    // which frame (Frames[FrameIdx])
    Kind  SnapRefKind
    Ref   uint32      // RefIRRef → IRRef; RefConst → const pool idx; RefSunkRecipe → recipe idx (§6.4)
}

type SnapRefKind uint8
const (
    RefIRRef SnapRefKind = iota
    RefConst
    RefSunkRecipe
)

// sunk-recipe table (one per trace, shared across snapshots)
type SunkRecipe struct {
    Type    SunkType             // Table / Closure / ...
    Fields  []SunkField          // per field: key + value source (IRRef / const / another sunk)
    // special fields: table array-part length, metatable ref, etc.
}
```

### 2.3 deopt 步骤(§2.1 伪码的可施工展开)

**步骤(与 §6 详细执行路径的高层视图对齐)**:

1. **guard 失败**:mmap 段跳到该 guard 的 exit stub(§7)。
2. **exit stub**:把所有 caller-saved 寄存器 bulk spill 到 JITContext 的 register dump area;写 `exitReasonCode = ExitOSR (=2)`;写 `exitArg0 = snapshotID`(见 P4 jitcontext.go 常量);`ret` 出段。
3. **Go 侧 dispatcher**:`nativeCode.Run` 的循环见到 status = ExitOSR,进入 deopt 分支:
   - (a) 读 snapshot(delta 展开);
   - (b) 补建 `Frames[]` 的 CallInfo 链(注意先内到外还是先外到内的顺序,§6.2);
   - (c) 分配所有 sunk-recipe 引用的对象(初始为空,先让根可见);
   - (d) 物化所有 SnapEntry:LocReg 从 dump area 读、LocSpill 从 spillBase 读、LocConst 从常量池、LocSunkRecipe 用 (c) 已分配的对象引用——按 NaN-box `uint64` 写回 arena 栈槽;
   - (e) 填 sunk 对象的字段(此时值都可用,填字段可能触发 GC——但所有已物化的槽已经在栈上,GC 可见);
   - (f) 设置 curCI 和 pc = ExitPC;
   - (g) return status = 2,让上层 crescent 主循环 reload 并续跑。

### 2.4 概念方案完成的三个工程要点(种子 §4.2 末点名的坑)

- **snapshot 压缩**:每个 guard 一份全量映射会撑爆内存(§5 展开预算数学);LuaJIT 用增量 / 共享编码;望舒 v1 用 delta 编码,基于前一 snapshot。
- **与 regalloc 的配合**:snapshot 引用的 IR 值在 exit 时必须可以恢复——这个约束改变了 regalloc 的自由度(见 05 §6);snapshot 与 regalloc 的联合不变式是本机器正确性的核心。
- **unsink 与 GC 的交互**:deopt 途中分配 → 可能触发 GC;GC 需要看到已分配但字段未填的 sunk 对象,以及已经物化的栈槽——**根可见性的顺序**是最深的坑(§6.4)。

---

## 3. P4-vs-P5 复杂度对照(种子 §4.3 表 + 增量)

### 3.1 种子表(逐字保留)

| | P4 函数级 OSR | P5 snapshot deopt |
|---|---|---|
| exit 粒度 | 函数(整函数放弃) | **指令级**(trace 内任意 guard) |
| 恢复的帧数 | 1(当前帧,已在 CallInfo) | **1..N**(含从未物理存在的内联帧) |
| 值的位置 | 已在栈槽(真相点不变式) | 寄存器/spill/常量/被 sink 掉的对象字段 |
| 映射数据 | 无需(静态生成 exit 序列) | 每个 guard 一份 snapshot,需要压缩与生命周期管理 |
| 出错形式 | 几乎没有投机面 | 任一槽映射错 / unsink 漏字段 ⇒ **静默的错误结果** |

### 3.2 增量行(展开自本文 §2 和 P4 04-osr-deopt §9.1 表)

| 维度 | P4 | P5 |
|---|---|---|
| exit stub 大小 | ~5-7 条机器指令(见 [../p4-method-jit/04-osr-deopt.md](../p4-method-jit/04-osr-deopt.md) §6.2) | ~30-50 条(bulk dump reg + 写 status + jmp),但每个 guard 复用同一段 dump stub |
| 运行期分配 | 无(见 P4 04 §3.7 禁止) | **有**(unsink 期间必须分配真对象) |
| GC 交互 | 无(exit 不分配,根天然可见) | **有**(unsink 中途 GC 触发 + 根可见性顺序,§6.4) |
| CallInfo 操作 | 写 savedPC 一个字段 + 不弹 | **补建 N 帧 CallInfo + 设置 curCI** |
| exit metadata 大小 | ~16 字节 / guard(见 P4 04 §4.1) | 未压缩数百字节 / guard;delta 之后 ~几十字节 / guard(§5) |
| 正确性策略 | 差分 + 少量 assertion 就够 | **差分主套 + fuzz 长时间运行 + verifier 三条线并行**(§9) |
| 人年成本 | 已经纳入 P4 +1-2 人年 | **是 P5 +2-4 人年的主成分**(§4) |

**复杂度差**:每个维度 P5 都比 P4 复杂一个量级,合计约「一个数量级」——这就是种子把「不引入 regalloc/inline/sink」称为 P4「换掉整台 snapshot 机器」的具体量化含义。

---

## 4. 「无处抄」逐组件难度(06-snapshot-deopt §4 表 + 展开)

### 4.1 种子表(逐字保留)

| 组件 | 难度 | 评估依据 |
|---|---|---|
| trace 录制器 | **中等,相对可控** | 在解释器上加录制模式,机制直白(逐指令旁录 IR);难点在工程琐碎:NYI 清单、黑名单、trace 长度/深度限制、录制开销控制 |
| IR + 经典优化(CSE/LICM/DCE) | **中等,深坑在后** | 教科书算法成熟;坑在 Lua 语义的细节正确性——元方法可观察副作用、表别名、NaN/-0、GC 移动语义,任何一条优化越界就是静默的错误结果 |
| 寄存器分配 | **中偏难** | 线性 trace 上不需要图着色,LuaJIT 式单遍逆序扫描可行;与 snapshot 的配合(§2.4)是主要复杂度来源 |
| **snapshot + deopt** | **最难** | §3;正确性无法靠评审保证,只能靠差分 fuzz 长期撞(§9);LuaJIT 这里的 bug 史绵延多年,可以借鉴 |
| 分配下沉 / 逃逸 | **难,可以后置** | 收益集中在分配密集类的负载(种子 §1.2 第三类);v1 可以不做,作为 P5 内部的第二个 gate |

### 4.2 「+2-4 人年」的理据(种子末段展开)

种子把 P5 定为「+2-4 人年,开放式」——本节把这个区间的成因具体化:

**下界 2 人年**(v1 = 录制 + 基础优化 + regalloc + snapshot,不含 sink):

- 录制 + IR + 优化 passes:~6-8 人月(受 03/04 章 PT-1/PT-3 边界约束);
- regalloc:~2-3 人月(见 05 章 PT-4);
- **snapshot 机器骨架**:~4-6 人月(§6 全部执行路径 + register dump area + delta 压缩 + Go dispatcher);
- **snapshot 机器正确性收敛**:~6-8 人月(fuzz 长时间运行 + 差分 bug 追修——**这一块无法通过 review / 单测缩短**,见 §9);
- 系统集成 + 测试 + 文档:~2-3 人月。

**上界 4 人年 = 下界 × 2**:主要膨胀在 snapshot 正确性收敛——LuaJIT 十几年间累积的 snapshot bug 修复表明,复杂 trace 形状加上 sink 组合下,静默的错误结果的衰减曲线不可计划;望舒可能会撞到类似的 bug 群。

**为什么无处抄**:

- LuaJIT 的 IR + snapshot + regalloc 三者互为前提;
- 精妙之处(比如 sink 与 snapshot 的协同、snapshot 压缩的具体编码)只在 Mike Pall 个人风格的高密度 C 代码里,除源码注释与零散邮件外没有系统文档;
- Go 生态没有现成的 trace JIT 库(wazero 是 method 式 Wasm 编译器,IR/snapshot 帮不上);
- 只能读懂原理后按望舒的约束重新设计——但重新设计的正确性收敛时间无法被「重新设计」缩短(fuzz 时长是常数),这就是 +2-4 人年的物理下限。

---

## 5. snapshot 生命周期与压缩

### 5.1 什么时候 take snapshot

**规则**:**每个 guard 处 take 一次 snapshot**(逻辑上)。物理上:相邻的 guard 之间 snapshot 高度相似——只有槽 X 或槽 Y 发生了小变化——不必都存全量。

### 5.2 delta 编码:LuaJIT 式共享尾

**建议的编码**:

```go
type SnapshotStorage struct {
    // all SnapEntry values packed into one array; each snapshot stores a [start, end) range
    Entries      []SnapEntry
    // per-snapshot metadata:
    Meta []struct {
        ExitPC   uint32
        Frames   []SnapFrame       // frame count is small (inline depth <10); not delta-encoded
        EntryStart uint32           // index into Entries
        EntryEnd   uint32
        PrevID    int32             // id of previous snapshot (-1 = full)
        // diff list relative to PrevID:
        Adds   []SnapEntry          // new or changed slots
        Removes []uint16            // dead slots (slot id)
    }
}
```

**恢复算法**(Go dispatcher 侧):

```go
func materializeSnapshot(store *SnapshotStorage, id int32) []SnapEntry {
    if store.Meta[id].PrevID < 0 {
        return store.Entries[start:end]     // full
    }
    base := materializeSnapshot(store, store.Meta[id].PrevID)  // recurse on previous snap
    // apply diff
    result := applyDiff(base, store.Meta[id].Adds, store.Meta[id].Removes)
    return result
}
```

**递归深度控制**:如果 diff 链过长,定期插入全量 snapshot 作为「基线」——LuaJIT 的具体阈值留给 PT-5 校准。

### 5.3 内存预算数学(种子 §4.2 末点名的「撑爆内存」)

**未压缩规模**:

- 每个 guard 一份 snapshot;
- 每份 snapshot 平均槽数:~10-30(典型循环体活值数);
- 每个 SnapEntry:~8 字节(Slot uint16 + FrameIdx uint8 + Kind uint8 + Ref uint32);
- 每份 snap 的 frames:~5-10 帧 × 20 字节 ≈ 100-200 字节;
- **每份 snap 未压缩 ≈ 300-500 字节**。

trace 假设 4000 IR × guard 密度 1/5-10 → ~500 guards。**未压缩总量 = 500 × 500 = 250 KB / trace**。假设一百个热 trace ≈ 25 MB,不可接受(超出典型嵌入用户的预算)。

**delta 压缩之后**:两条相邻的 guard 之间通常只有 1-3 个槽变动 → 每份 snap ~30 字节(diff 元数据)+ 每 ~20 个 guard 一次全量基线 500 字节。总:500 × 30 + 25 × 500 ≈ 27 KB / trace,一百个 trace ≈ 2.7 MB,可以接受。

**收益 ~10x,与 LuaJIT 经验一致**。

---

## 6. deopt 执行路径(step by step,可以直接施工)

**本节是全文最长的一节——把种子 §4.2 的 3 步伪码展开为可以施工的物理路径**。

### 6.1 guard 失败 → exit stub → 出段

**mmap 段侧(amd64 伪码,arm64 同构)**:

```asm
;; a guard's conditional check
    cmp   rax, NanTag        ;; or any guard comparison
    je    guard_ok           ;; hit → continue
;; --- guard failed: jump to this guard's exit stub ---
    jmp   exit_stub_G7

guard_ok:
    ...                      ;; continue trace straight line

;; ============== exit stubs section (separate from hot path) ==============
exit_stub_G7:
    ;; 1. bulk spill all caller-saved regs to the register dump area (shared stub)
    jmp   bulk_dump_and_exit_osr

;; shared dump + exit tail (one per trace, reused by all guards)
bulk_dump_and_exit_osr:
    mov   [r15 + dumpOff + 0*8], rax
    mov   [r15 + dumpOff + 1*8], rcx
    mov   [r15 + dumpOff + 2*8], rdx
    mov   [r15 + dumpOff + 3*8], rsi
    mov   [r15 + dumpOff + 4*8], rdi
    mov   [r15 + dumpOff + 5*8], r8
    mov   [r15 + dumpOff + 6*8], r9
    mov   [r15 + dumpOff + 7*8], r10
    mov   [r15 + dumpOff + 8*8], r11
    movsd [r15 + dumpOff + 9*8 + 0*8], xmm0
    movsd [r15 + dumpOff + 9*8 + 1*8], xmm1
    ;; ... (xmm2..xmm15)
    mov   dword ptr [r15 + exitReasonCodeOff], 2       ;; ExitOSR (see P4 jitcontext.go)
    mov   qword ptr [r15 + exitArg0Off], G7             ;; snapshotID = G7 (per-guard number)
    ret                                                 ;; return, hand control back to Go dispatcher
```

**关键设计:每个 guard 只有 1-2 条指令 + 共享 dump stub**:每个 guard 的 stub 只需要 `mov exitArg0 <G7> + jmp bulk_dump_and_exit_osr`——避免 500 guard × 数十条 dump 指令造成的代码膨胀(推荐架构,见 §7)。

**为什么用 ExitOSR = 2**:见 [../p4-method-jit/05-system-pipeline.md](../p4-method-jit/05-system-pipeline.md) §3.3 常量表,`ExitOSR uint32 = 2` 已经存在——P4 的 OSR exit 与 P5 的 deopt exit 在 status 编码上一样(概念也一样:都是「投机失败,回到解释器状态」),复用最少的代码路径。dispatcher 侧按 exitReasonCode 分派之后,P5 走「snapshot 展开」子路径。

### 6.2 Go dispatcher 收到 ExitOSR:主循环

**Go 侧伪码**(放在 `internal/fullmoon/trace/deopt/dispatcher.go`):

```go
// follow P4 nativeCode.Run dispatcher loop model (see [P4 05] §4.3.1a)
func (tc *TraceCode) Run(state *State, base int32) int32 {
    host := tc.host
    host.RefreshJitCtxAddrs(&tc.jitCtx, base)      // refresh five base addrs once
    // enter mmap segment
    entry := tc.codePageAddr
    status := callTraceMmap(entry, &tc.jitCtx, base)
    for {
        switch status {
        case ExitNormal:
            return 0
        case ExitError:
            return 1
        case ExitOSR:
            snapID := uint32(tc.jitCtx.exitArg0)
            newBase, err := tc.deoptRestore(state, snapID)
            if err != nil {
                return 1
            }
            // deopt done — return so crescent main loop can reload
            return 2
        case ExitInlineHelper:
            // reuse P4 dispatch path (not on deopt route)
            handleHelper(tc, base)
            host.RefreshJitCtxAddrs(&tc.jitCtx, base)
            resumeOff := tc.jitCtx.ResumeOff()
            resumeAddr := tc.codePageAddr + uintptr(resumeOff)
            status = callTraceMmap(resumeAddr, &tc.jitCtx, base)
        }
    }
}
```

**关键:deopt 之后不重入 mmap 段**——直接 return 2,让 crescent 主循环按 status=2 走 reloadFrame + 续跑(见 [../p4-method-jit/04-osr-deopt.md](../p4-method-jit/04-osr-deopt.md) §8.3 `callDeoptResume` 出口——P4 已经定好的接口)。

### 6.3 deoptRestore 主体:六步

```go
func (tc *TraceCode) deoptRestore(state *State, snapID uint32) (int32, error) {
    // step 1: expand delta to full SnapEntry list
    snap := tc.snapStorage.Materialize(snapID)     // recursive diff apply (§5.2)

    // step 2: rebuild CallInfo chain — push from outermost to innermost
    //   outer: CallInfos already held by crescent before trace entry (untouched)
    //   middle: frames the trace inlined but never actually pushed (0..N-1)
    //   inner: the frame where exitPC lives
    for _, frame := range snap.Frames {
        ci := pushCallInfo(state.thread)
        ci.base    = uint32(frame.BaseOff)
        ci.funcIdx = uint32(frame.FuncIdx)
        ci.protoID = frame.ProtoID
        ci.nresults= frame.Nresults
        ci.savedPC = uint32(frame.SavedPC)   // middle frames: next pc after call; innermost: ExitPC
    }
    // innermost CallInfo.savedPC == snap.ExitPC (used by P1 05 §1.3 reloadFrame)

    // step 3: allocate all sunk objects first (empty; fields filled later)
    //   why first: filling a field may reference another sunk object (chained),
    //   so all references must be reachable before any field is filled
    sunkObjs := make(map[uint16]uintptr, len(snap.SunkRefs))
    for i, recipe := range snap.SunkRecipes {
        obj := allocateEmpty(state, recipe.Type, recipe.Fields)
        sunkObjs[uint16(i)] = obj
        // temporarily pin the new object as a root — see §6.4
        state.deoptScratchRoots = append(state.deoptScratchRoots, obj)
    }

    // step 4: materialize all SnapEntry → write back to arena stack slots
    //   note: this step does not trigger GC (step 5 unsink may)
    dump := tc.registerDump()                       // start of JITContext dump area
    spill := tc.jitCtx.SpillBase()
    for _, entry := range snap.Entries {
        val := resolveEntry(entry, dump, spill, tc.constPool, sunkObjs)
        writeSlot(state, entry.FrameIdx, entry.Slot, val, snap.Frames)
    }

    // step 5: unsink — fill sunk object fields
    //   this may trigger GC: sunkObjs are pinned in deoptScratchRoots,
    //   and materialized slots are already in arena → GC roots see everything, mark-sweep is safe
    for i, recipe := range snap.SunkRecipes {
        obj := sunkObjs[uint16(i)]
        for _, field := range recipe.Fields {
            fieldVal := resolveEntry(field.Value, dump, spill, tc.constPool, sunkObjs)
            setField(state, obj, field.Key, fieldVal)  // may trigger GC (table grow / rehash)
        }
    }

    // step 6: clear deopt scratch roots (sunk objects are now referenced by stack slots,
    //          naturally visible to GC, so scratch pin is no longer needed)
    state.deoptScratchRoots = state.deoptScratchRoots[:0]

    // step 7: curCI already at innermost frame (last pushed in step 2), pc = ExitPC
    //   crescent main loop resumes from curCI.savedPC
    return state.thread.ciTop, nil
}
```

### 6.4 GC 与根可见性:最深的坑

**问题**:step 5 的 unsink 填字段时,可能触发 GC(table grow / string intern);GC 需要能看到:

- **已经物化的栈槽**:天然可见(住在 arena 值栈,GC 根扫描的常规区域);
- **已分配但字段未填的 sunk 对象**:**不天然可见**——它们还没被任何栈槽 / CallInfo 引用(还没到 step 4 引用它们的机会,或者引用它们的槽在别的 sunk 对象里);
- **链式 sunk 依赖**:sunk 对象 A 的字段引用 sunk 对象 B;A 与 B 在填字段前都要可见。

**解决:step 3 的 `deoptScratchRoots` 临时根**——每分配一个 sunk 对象,立刻挂到 State 的 `deoptScratchRoots` 数组(GC 根扫描明确会遍历这个数组;这是 P5 新引入的根类型)。

**顺序性不变式**:

> deopt 期间任何一个 GC 触发点,当前所有已分配的 sunk 对象都必须在 `deoptScratchRoots` 里;当前所有已物化的槽都必须在 arena 值栈里(也就是说 step 4 单向前进,不回滚)。

**释放时机**:step 6——所有 sunk 对象此时已经被栈槽引用(step 4 把它们的引用写回了栈槽,或者 unsink 填的字段引用了其他 sunk,而那些又被某个槽引用)——scratch 根可以清了。

**风险**:如果某个 sunk 对象「没有栈槽引用它,只有 dead 引用」——按 04 优化的 DCE 应该已经消除掉(sunk 对象的 SnapEntry 也是 DCE 根);但如果 sink 优化把不该 sunk 的对象也 sunk 了(bug),step 6 之后该对象会立即被 GC 回收——如果续跑用到,读到的就是垃圾。这属于 **sink 优化的 bug 而不是 deopt 的 bug**,防线在 04 的 verifier 与差分。

**顺序不能反的一个具体案例**:「先 unsink 再物化槽」是错的——unsink 填 fieldVal 时可能触发 GC,而此时槽还没物化,GC 看到的是 trace 编译前的旧槽值,新 sunk 对象即使有 deoptScratchRoots 保护,链式引用的其他还未分配的 sunk 对象没有保护——违反了顺序性不变式。**必须先物化槽后 unsink** 是本机器的关键顺序约束(§10 开放问题跟踪具体的验证方式)。

### 6.5 slot 写入:直接寻址 vs 经 host

step 4 的「写回 arena 栈槽」有两条路径:

- **(a) 直接寻址**:Go dispatcher 用 arenaBase + valueStackByteOffset + 8*slot 直写 NaN-box `uint64`。P4 已经具备这条能力(见 [../p4-method-jit/05-system-pipeline.md](../p4-method-jit/05-system-pipeline.md) §5 arena base 重载协议)。
- **(b) 经 host SetReg**:host 接口新增一个 `SetSlot(frameIdx, slot, val)` 方法,dispatcher 调用它。

**建议 v1 选 (b) SetSlot**:

- **正确性优先**:host 侧的方法可以复用 crescent 已有的栈槽写入代码,避免 dispatcher 重复实现;
- **arena grow 兼容**:host 方法内部可以处理 arena grow(如果槽越界);
- **性能不是瓶颈**:deopt 是稀有路径(投机失误才发生);SetSlot 的方法调用开销可以忽略。

v2 如果发现 deopt 频次高影响 throughput,再评估 (a) 直接寻址优化(见 P5 sink v2 gate)。

### 6.6 与 crescent 的接口(host 扩展)

deopt 驱动需要 host 提供的方法(建议新增到 `internal/fullmoon/trace/host.go` 或复用 `P4HostState`):

```go
type P5HostState interface {
    // reuse all arena / CallInfo methods from P4HostState
    P4HostState

    // P5 additions:
    PushDeoptFrame(protoID uint32, base int32, funcIdx int32, nresults uint16, savedPC int32)
    SetSlot(frameIdx uint8, slot uint16, val uint64)
    AllocSunkTable(nfields int) uintptr            // allocate empty table with capacity nfields
    AllocSunkClosure(protoID uint32) uintptr
    SetTableField(obj uintptr, key uint64, val uint64)   // reuse P4 SetTable semantics
    // new root management:
    PinDeoptRoot(obj uintptr)                       // pin to deoptScratchRoots
    ReleaseDeoptRoots()                             // clear in one shot
}
```

**接口延伸方向**:P4HostState 已经提供了大部分 arena/CallInfo 能力;P5 主要新增 PushDeoptFrame + 根管理 + Alloc*Sunk*——这些都是「已有语义 + 新调用点」,不是全新语义。

---

## 7. exit stub metadata 格式与 register dump area

### 7.1 register dump area 设计(推荐)

**位置**:JITContext 新增一段固定字节区,大小 = |全部 caller-saved GPR| × 8 + |全部 caller-saved FPR| × 8。

- amd64:9 GPR × 8 + 16 FPR × 8 = 200 字节;
- arm64:16 GPR × 8 + 32 FPR × 8 = 384 字节。

**JITContext 扩展(建议)**:

```go
type JITContext struct {
    // ... existing P4 fields (see P4 05 §3.3)
    // P5 addition:
    dumpArea [384]byte    // covers the larger of the two arches; per-arch offsets differ
}
```

### 7.2 exit stub 两种设计对照

| 设计 | 每 guard 生成的机器码 | 代码大小 | dispatcher 读取路径 |
|---|---|---|---|
| (i) 每个 guard 定制 stub(每个 guard 只 spill 该 guard 用到的寄存器) | 每个 guard 20-50 条 | 500 guard × 30 = 15 KB / trace | 每个 guard 独立元数据 |
| **(ii) 共享 bulk dump stub(每个 guard 只写 exitArg0 + jmp)**(**推荐**) | 每个 guard 2 条 + 全 trace 一份 bulk dump(50 条) | 500 × 2 × ~7 字节 + 50 × 7 = 7 KB + 350 = ~7.5 KB / trace | dispatcher 从 dump area 统一读所有 caller-saved reg,SnapEntry 里 LocReg 的 regID 就是 dump area 的偏移索引 |

**推荐 (ii) 共享 bulk dump**:代码大小减半,dispatcher 逻辑统一(SnapEntry 无论从 dump 读还是从 spill 读,都是「从固定地址取 uint64」)。这与 LuaJIT 的 exit stub 设计接近,是简洁性与代码大小之间良好的折中。

### 7.3 exit stub metadata(regalloc → 06 的交付,复述 05 §6.3)

```go
type ExitStubMeta struct {
    GuardID    uint16
    SnapID     uint32
    // per-SnapEntry location (filled by 05 regalloc)
    Locs       []Assignment    // len == len(snap.Entries), one per entry
}
```

**LocReg 的解释**:`RegID` 是 GPR/FPR 池的索引,dispatcher 按此索引 dump area:

```
dump area layout (amd64):
  offset  0: rax
  offset  8: rcx
  offset 16: rdx
  ...
  offset 64: r11    (9 GPR, 72 bytes total)
  offset 72: xmm0
  offset 80: xmm1
  ...
```

`RegID = 0..8` → GPR;`RegID = 9..24` → FPR(offset = 72 + (RegID-9) * 8)。同一份约定让 dispatcher 与 regalloc 共用一套编码表。

### 7.4 side table 组织

per-trace 的 metadata 打包成 side table:

```go
type TraceCode struct {
    codePageAddr uintptr             // mmap segment
    entry        uintptr
    jitCtx       *JITContext
    // side table:
    snapStorage  *SnapshotStorage   // §5.2 stores all snapshots (delta-encoded)
    exitStubs    []ExitStubMeta     // per-guard metadata, indexed by GuardID
    sunkRecipes  []SunkRecipe       // all sunk-object recipes for this trace (see 06 §6.3)
    constPool    []uint64           // NaN-box const pool
    // ...
}
```

---

## 8. side trace(v3 gate,设计兼容位)

### 8.1 目标

**side trace**:某个 guard 的 side exit 变热(计数超过阈值)→ 从该 exit 状态起录制一条新的 trace,把它编译进 mmap 段,把原 guard 的失败跳转 patch 到新 trace 的入口——这样每次 side exit 就不用再走 dispatcher 回解释器。这是 LuaJIT 后期演化出来的关键特性,把 exit 变成「侧路径继续跑」而不是「回解释器」。

### 8.2 v1/v2 阶段的设计兼容位

**v1 不做 side trace**,但 snapshot 的格式必须**兼容 v3 需要**:

- **snapshot 定义 side trace 的入口状态**:v3 录制 side trace 时,起点就是「某个 guard 的 snapshot 恢复之后的状态」——所以 snapshot 必须精确到能重建一切执行需要的状态(§2 已经满足)。
- **每个 guard 的 exit counter**:v3 需要在 dispatcher 侧计数每个 guard 触发的次数(见 [../p4-method-jit/04-osr-deopt.md](../p4-method-jit/04-osr-deopt.md) §5.1 P4 deoptCount 的相同机制)——建议 `ExitStubMeta.HitCount uint32` 加一个字段,v1 只写不读,v3 用它来判定热 exit。
- **register dump area 让 side trace 入口容易实现**:side trace 入口 = 从 dump area 和 spill 读回状态。v3 side trace 编译时,把「读 dump + spill 装 reg + 跳 side trace 主体」拼接为新 trace 的 prolog——因为 dump area 是共享区域,side trace 天然继承主 trace 的 exit 状态形式。这是共享 dump 设计(§7.2)的次要好处。

### 8.3 link stitching(v3 具体形式)

v3 时,把主 trace 的 guard 失败跳转从「exit stub」patch 到「side trace entry」:

```
main trace (v3):
    jmp   exit_stub_G7    ;; v1 form
;;
;; after v3 patch:
    jmp   side_trace_S7_entry   ;; jump directly to side trace, no dispatcher
```

**这是运行期 patch**(见 P4 code page W^X 翻面协议)——需要短暂 W→X 切换。**snapshot 格式对此透明**——patch 只改一条 jmp 目标地址,不动 snapshot metadata。

### 8.4 v1 的兼容义务清单

v1 必须保留下来的属性(让 v3 不必回炉):

- snapshot storage 支持追加(v3 side trace 可能生成新的 snapshot,加到 storage);
- exitStubs table 支持 patch(每个 guard 的 jmp 目标可以运行期修改);
- exit stub 的位置对齐可预测(patch 时不需要重新计算偏移);
- register dump area 布局固定(side trace 编译时按 v1 定的布局读回)。

---

## 9. 正确性策略

### 9.1 差分主套的核心地位

**见 [08-testing-strategy.md](./08-testing-strategy.md)**:P5 差分主套(byte-equal 比对测试)是「同一份 Proto 在 crescent 与 fullmoon 上执行结果 byte-equal」,CI 强制检查。**任何 snapshot 恢复错都会在此暴露**——deopt 之后续跑 N 条指令产生的输出与一路解释产生的输出比对,byte-equal 才通过。

### 9.2 deopt 注入模式

**每个 guard 强制失败**(见种子 §5「deopt 注入」+ [../p4-method-jit/04-osr-deopt.md](../p4-method-jit/04-osr-deopt.md) §5 类似机制):

- 测试构建下开启 `-forceDeoptEveryGuard` 标志;
- 每个 guard 命中时假装失败,走 exit stub → dispatcher → snapshot 恢复 → 续跑;
- 续跑结果与「一路解释」byte-equal ⇒ 该 guard 的 snapshot 正确。

**为什么有效**:强制注入把「稀有的 guard 失败路径」变成「必走路径」——单元测试覆盖 500 个 guard 只需要一次运行。P4 04 §5 已经在 P4 用过同样的套路;P5 沿用并加码。

### 9.3 verifier assertion(与 05 §8.2 共享)

在 build tag `verify` 下,dispatcher 前置调用 `VerifySnapshot(snap)`:

- 所有 SnapEntry.Ref 指向的 IRRef 有 Assignment(不是 LocNone);
- 所有 sunk-recipe 的字段引用都能解析(不指向不存在的 IRRef / sunk 循环);
- Frames[] 每一层的 protoID 存在且 BaseOff 单调;
- ExitPC 在 Frames[len-1].Proto 的合法 pc 范围内;
- ...

任一违反 → panic + dump snapshot——这是「静默的错误结果」的最后一道 in-process assertion。

### 9.4 「bug 衰减曲线」的现实认知

**见 06-snapshot-deopt §4 末**:

> snapshot 机器的正确性收敛时间**本质上不可计划**——它由 fuzz 发现的 bug 衰减曲线决定,而不是由里程碑排期决定。

**具体含义**:

- v1 骨架能跑起来(简单 trace 差分绿)不代表机器正确;
- 需要 nightly fuzz 长时间运行数月,不断修「奇怪脚本形状 + 稀有 GC 时机 + 特殊 sunk 组合」引发的静默的错误结果;
- 这一阶段无法通过 review / 单测缩短,只能靠时间。

**对项目管理的含义**:

- P5 立项时预先声明「snapshot 稳定期 ≥6 月不可谈判」;
- v1 GA 不以「主套差分绿」为标准,而以「fuzz N 天没有差异」为标准(N 留给 PT-5 定,建议 30 天起);
- 如果 fuzz 差异率持续不衰减(比如 2 个月了还在稳定出新 bug),重新评估 P5 是否值得继续投入(见各章末尾开放问题 风险 2「人年开放式的失控面」)。

### 9.5 preemptFlag 与 deopt 的相互作用(开放但需要登记)

trace 直线段内可能有 back edge safepoint(见 [../p4-method-jit/05-system-pipeline.md](../p4-method-jit/05-system-pipeline.md) §3.5 preemptFlag)。如果 safepoint 触发时正处于一段「已经 spill 部分寄存器但没来得及物化槽」的中间状态——**是否也需要 snapshot 保护 safepoint 点**?

**建议方案**:safepoint 只放在「模板边界式的干净点」——由 trace 编译器保证 safepoint 处所有活值要么在寄存器要么在 spill,并且有对应 snapshot(等价于把 safepoint 视为一个「不会失败的 guard」)。这样 safepoint 触发时抢占 helper 走 exit-reason 通道,也能靠 snapshot 恢复——同一台机器共用。

**未定的细节**:safepoint snapshot 是否也走 delta 编码 / 与 guard snapshot 是否共用一套编码——留给 PT-5 联合评估(§10)。

---

## 10. 开放问题

- **snapshot 编码的最终形式**(§5):delta 深度上限;基线 snap 的插入频率;能否借鉴 LuaJIT 的「slot bitmap + 值列表」两段式表示进一步压缩。
- **unsink-GC 顺序性不变式的验证**(§6.4):是否有 stress test 能可靠触发「先 unsink 后物化」类顺序错;deoptScratchRoots 的实现需要与 GC 根扫描代码联合验证——留给 PT-5 启动时联合评估。
- **register dump area 大小与布局在两个架构间对齐**(§7.1):是否统一定 384 字节(amd64 浪费 184 字节)以简化代码,还是 per-arch 变长——推荐前者(内存开销可以忽略,代码统一)。
- **safepoint 与 deopt 的机器共用**(§9.5):safepoint snapshot 是否与 guard snapshot 在同一张表——如果同表,可能推高 snapshot 密度;如果异表,需要两套机器。
- **多 State 并发下 snapshot storage 的生命周期**:trace 编译产物跨 State 共享的边界(见 05 §9 相同的开放问题)——如果 storage 只读,共享没问题;delta 展开的结果缓存需要 per-State 或者不缓存。
- **sink 引入的 recipe 循环**:sunk 对象 A 字段引用 sunk 对象 B,B 字段又引用 A(理论上可能发生但 Lua 语义罕见)——是否禁止对这类形式做 sink(在 04 侧过滤),或者让 unsink 处理循环(先都分配空对象,再填字段——§6.3 step 3/step 5 顺序已经支持,但需要 verifier 断言 recipe DAG 没有循环——留给 PT-5 决定)。
- **v3 side trace 的启动阈值**:每个 exit 计数到多少才启动录制;LuaJIT 的值是几百次,望舒待 PT-5 实测——不影响 v1 格式设计,只影响 v3 启用时机。

---

相关:
[./00-overview.md](./00-overview.md) §4(原种子 §4.1-§4.4 已合并到本文 §1-§4) ·
[00-overview.md](./00-overview.md)(v1/v2/v3 阶段 gate / snapshot 是 v1 gate 之一) ·
[03-ir-design.md](./03-ir-design.md)(IRRef / IRIns / guard 位,snapshot entry 的引用形式) ·
[04-optimization-passes.md](./04-optimization-passes.md)(snapshot 引用作为 DCE 根 / sink 产生 sunk-recipe) ·
[05-register-allocation.md](./05-register-allocation.md)(与本文互为约定的两端;每个 guard 的 Locs 由 05 出稿,§6/§7 消费) ·
[07-system-integration.md](./07-system-integration.md)(P5 与 gibbous / crescent 的层间协议 / host 扩展) ·
[08-testing-strategy.md](./08-testing-strategy.md)(差分主套 + deopt 注入 + fuzz 长时间运行,§9 承接) ·
[09-acceptance-checklist.md](./09-acceptance-checklist.md)(snapshot 稳定期 ≥6 月是 v1 GA 强制条件) ·
[../p4-method-jit/04-osr-deopt.md](../p4-method-jit/04-osr-deopt.md)(P4 薄 deopt,§1 拆毁栈槽真相与之对应 / §3 复杂度对照的对应面 / §5 P4 deoptCount 机制) ·
[../p4-method-jit/05-system-pipeline.md](../p4-method-jit/05-system-pipeline.md)(JITContext / exit-reason 协议 / Go dispatcher,§6 沿用) ·
[../p4-method-jit/06-backends.md](../p4-method-jit/06-backends.md)(post-PR-#42 寄存器约定,§7 register dump area 与之协作) ·
[../p1-interpreter/05-interpreter-loop.md](../p1-interpreter/05-interpreter-loop.md) §1(CallInfo 32 字节布局,§2/§6.3 重建目标) ·
[../p1-interpreter/01-value-object-model.md](../p1-interpreter/01-value-object-model.md)(NaN-box `uint64`,物化写栈槽的形式) ·
[../p1-interpreter/06-memory-gc.md](../p1-interpreter/06-memory-gc.md)(自管 mark-sweep,§6.4 unsink 与 GC 交互)
