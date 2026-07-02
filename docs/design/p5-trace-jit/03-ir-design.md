# P5 §3:IR 设计——线性 SSA + 双数组布局 + 类型 lattice + guard/snapshot 耦合

> 状态:**未立项图纸**(P5 尚未立项,本文是启动闸门 [01-launch-judgment](./01-launch-judgment.md) 通过后可以逐步照做的施工设计,不代表任何已实现代码)。
>
> 对应 Go 包:`internal/fullmoon/trace/ir`(IR 数据结构 + 编解码 helper;IR 打印器详见 §7)。IR 与录制器耦合但独立成包,便于 [04-optimization-passes](./04-optimization-passes.md) / [05-register-allocation](./05-register-allocation.md) / [06-snapshot-deopt](./06-snapshot-deopt.md) 分别引用而不循环依赖。
>
> 上游契约:
> [./00-overview.md](./00-overview.md)(00-overview §2 流水线图 ② SSA 线性 IR / §4.4 「双数组 SSA、折叠引擎、单遍 regalloc 三者互为前提」的核心洞察 / §6 开放问题:「IR 具体形式(LuaJIT 式双数组 vs 常规 SSA)」——本文给出方案 A 提议 + PT2 验证协议)、
> [../../../llmdoc/must/design-premises](../../../llmdoc/must/design-premises.md)(前提二 Go runtime 四项税:GC 触及点越少越好——本文 §1 选双数组的最强驱动力;前提四第一天 NaN-box 承诺——本文 §3 IR 类型 lattice 与 NaN-box tag 单一对应)、
> [../roadmap](../roadmap.md)(§7 LuaJIT 范本)。
>
> P1 依赖面:
> [../p1-interpreter/01-value-object-model](../p1-interpreter/01-value-object-model.md)(§3.3 tag 表 8 个非 number 类型 + §3.4 NaN 规范化不变式——本文 §3 lattice / §5 constant KGC / §9 semantic red lines 直接引用)、
> [../p1-interpreter/02-bytecode-isa](../p1-interpreter/02-bytecode-isa.md)(§7 ICSlot 里 shape/index/tableRef 字段——本文 §4 ALOAD/HLOAD/GUARD_TABLESHAPE 的物理复用)、
> [../p1-interpreter/06-memory-gc](../p1-interpreter/06-memory-gc.md)(arena 布局 + gen 代次——本文 §4 表 IR 的 memory model 基础)。
>
> P4 对位:
> [../p4-method-jit/03-speculation-ic](../p4-method-jit/03-speculation-ic.md)(§3 guard 显式比较硬约束、§2 五档 FeedbackKind 投机模板——本文 §5 guard 类型直接继承,拓展到分支方向、call target identity)、
> [../p4-method-jit/04-osr-deopt](../p4-method-jit/04-osr-deopt.md)(§3 物化 = memmove、§6 exit stub——本文 §5 guard-snapshot 耦合契约的下游)。
>
> 下游协作(同子目录):
> [./02-trace-recording](./02-trace-recording.md)(录制器发射本文 IR ops——§3 表每行右列都是本文 §4 op 定义源)、
> [./04-optimization-passes](./04-optimization-passes.md)(FOLD / CSE / DCE / LICM 全部作用于本文 IR)、
> [./05-register-allocation](./05-register-allocation.md)(单遍逆序扫描消费本文 IR,06-snapshot-deopt §4 「三者互为前提」的第三顶点)、
> [./06-snapshot-deopt](./06-snapshot-deopt.md)(每个 guard 挂 snapshot ref,协议在此,压缩机制在 06)。

---

## 0. 定位:P5 的中间语言

### 0.1 IR 是什么、不是什么

**是**:一条 trace 录制期产生的、静态单赋值(SSA)形式的线性指令流;每条 IR ins 编码为一个固定大小的 64-bit 记录;操作数用 `IRRef`(uint16 或 uint32,§2 讨论)。整条 trace 是一个 `[]IRIns` 数组 + 一个常量池 + 一批 snapshot。

**不是**:传统编译器教科书的「基于图 + 指针」SSA(带 def-use 链、基本块、控制流图节点)。P5 IR 是**线性的**——一条 trace 内没有多个基本块、没有 phi 节点混杂控制流(loop 头的 PHI 是特殊标记见 §4),没有指针跳转。这是承 LuaJIT 的核心洞察:**trace 已经把控制流拍扁成一条线,数据结构不该再引入图**。

### 0.2 为什么不是「传统 SSA + 指针图」

三条 Go 相关的硬理由:

1. **GC 触及点(前提二第 3 税)**:传统 SSA 每 IR 节点是 `*Node` + `[]*Node` 操作数指针切片——一条 4000 ins 的 trace ⇒ 数千 heap 指针 + 数千 slice header,GC 每次 mark 都要扫。双数组把整条 trace 压成两个 `[]byte`(或 `[]uint64`)大 slice,GC 只看两个根,mark 成本几乎不随 trace 长度增长。
2. **cache 局部性**:线性数组顺序访问命中 L1;指针图跳跳转转命中率打折。fold / CSE / DCE 每 pass 都是线性扫的 IR ins 数组——数组形式是 cache friendly 的天然形态,指针图形式反之。
3. **snapshot 编码**:snapshot 里引用 IR value 用 `IRRef`(uint16/uint32)比 `*Node` 便宜一半空间且免 GC,与 [06-snapshot-deopt](./06-snapshot-deopt.md) 的压缩目标契合。

传统 SSA 的一个可能反驳:「便于优化 pass 修改 IR」——但 P5 的优化策略是 FOLD-on-emit(承 [04-optimization-passes §1](./04-optimization-passes.md)),**不 rewrite 已发射的 IR**,只在 emit 时 fold + CSE。IR 数组一旦发射即基本不动,DCE 是**标记而非删除**(§4 flag bit)。指针图适合的「reassociate rewrite」等重优化 P5 v1 都不做。

### 0.3 「按望舒约束重设计」的边界

06-snapshot-deopt §4 定死:**「LuaJIT 的 IR、snapshot、regalloc 三者互为前提、高度耦合,除源码注释与零散邮件列表外无系统文档」;「只能读懂原理后按望舒约束重设计,不能移植」**。本文的每一项决策都要过一遍「望舒约束」筛:

- Go 语言约束:无 union、无位域、struct field 对齐固定,不能像 LuaJIT C 那样把 IRIns 打成 32-bit `union { struct{u8 o,t; u16 op1,op2}; ...}` —— 望舒得用显式打包函数;
- NaN-box 值表示(前提四,已 现金化):IR 类型 lattice 必须与 [01-value-object-model §3.3](../p1-interpreter/01-value-object-model.md) tag 表一致,不能引入其他 tag;
- 自管 arena / mark-sweep GC:IR 里的 GCRef 保留 P1 §5 语义,不 rewrite;
- 无信号陷阱(前提二第 4 税):所有 guard 显式(承 P4 03 §3),IR 层不允许 implicit trap;
- P5 IR 只服务一条 trace 生命期,不跨 trace 复用(简化设计,承种子 §5 「侧防线在此最关键」——每条 trace 独立差分 fuzz)。

### 0.4 章节路标

§1 IR 表现形式的两条候选 + 定稿双数组 → §2 IRIns 打包与 IRRef 编码 → §3 IR 类型 lattice(IRT)→ §4 IR op 全表(约 50 op,按类别分组)→ §5 guard 作 IR ins(与 snapshot 的耦合契约)→ §6 常量与 interning → §7 IR 打印器(必须的 PT2 交付物)→ §8 开放问题。

---

## 1. IR 表现形式的两条候选

### 1.1 候选 A — 传统 SSA + 指针图

```go
type IRNode struct {
    Op       Opcode
    Type     IRT
    Operands []*IRNode
    Uses     []*IRNode  // 反向 def-use 链
    Snap     *Snapshot
    ...
}
```

优点:直观、便于图上 rewrite、和教科书一致。缺点:§0.2 三条(GC 触及、cache miss、snapshot 编码贵)全踩。

### 1.2 候选 B — LuaJIT 式双数组(定稿提议)

**数组一:IR 指令流**,顺序线性,索引即 `IRRef`。**数组二:常量池**,从相反方向增长,共用同一段 `IRRef` 编号空间。

```
                  bias
                    ▼
  常量区(向下增长)                     指令区(向上增长)
  ────────────────────────┼──────────────────────────────►
   ...  K2  K1  K0        │        I0   I1   I2   ...   IN
  IRRef= REF_BIAS-1..K0           REF_BIAS   ...   REF_BIAS+N-1
   (常量 IRRef 数值上小于 bias)   (指令 IRRef 数值上 ≥ bias)
```

- `IRRef` 是 int32(或 int16,若 trace 长度上限 ≤ 32k),`bias = 0x8000`(64k 编号空间对称二分);
- 常量与指令共用同一段编号空间 ⇒ 「值」的引用完全统一为一个 `IRRef`,不需区分「这是操作数是不是常量」;
- fold engine 判「操作数是不是常量」= 单个 `ref < bias` 比较,极快;
- **双向增长**:一条 trace 录制期,常量往前 push,指令往后 push;两侧都不会溢出对方——若真溢出说明 trace 太大 → 走 §4.4 hardcap abort。

这个布局是 LuaJIT 的核心 idea(06-snapshot-deopt §4 提到但无文档);望舒沿用同款,以 Go 数据结构表达:

```go
type IRBuf struct {
    ins   [MaxTraceIns]IRIns   // 从 bias 起顺向填,ins[i] 的 IRRef = bias + i
    kBuf  [MaxTraceKs]IRIns    // 从 bias 起反向填,kBuf[i] 的 IRRef = bias - 1 - i
    nIns  uint16               // 下一 ins 写入位置
    nK    uint16               // 下一 kBuf 写入位置
}
const (
    BiasRef     IRRef = 0x8000
    MaxTraceIns       = 4000     // 与 [02 §4.4] 同源
    MaxTraceKs        = 512      // 常量池上限
)

func (b *IRBuf) LookupInsRef(ref IRRef) *IRIns {
    if ref >= BiasRef {
        return &b.ins[ref-BiasRef]
    }
    return &b.kBuf[BiasRef-1-ref]
}
```

**注**:LuaJIT 用 `MRef`(machine ref)在 C 里同时表达 IR ref、常量 ref、内部标记;Go 里没有 union,直接一个 `int32/uint16` 类型的 `IRRef` 加边界判定即可,没有丢失表达力。

### 1.3 决策:选 B,标为提议,PT2 验证

**决策**:双数组布局是**提议**,PT2 (IR + fold) 里程碑真跑起来后须验证:

- fold engine 判「操作数是常量」的实测成本(应是 int 比较 + 1 slice index,~1-2 cycle);
- 一条 4000 ins trace 的两 slice 总大小(4000 × 8 + 512 × 8 ≈ 36 KB,应能全进 L1D);
- IR emit 时 `LookupInsRef` 的分支预测(常量 vs 指令是二值,BTB 应能预测好)。

若 PT2 实测失败(极不可能但可能),回退到候选 A;此时本文 §2 起余章需重写。**默认提议为定稿**,回退是 fallback。

---

## 2. IRIns 编码

### 2.1 单条 IRIns 打包(Go 版)

Go 不能像 LuaJIT C 那样直接 union 打包;显式 struct 但字段紧凑:

```go
// IRIns is a single IR instruction, 8 bytes (fits one uint64 slot).
// Layout (as if uint64 bit fields):
//   [63:56] op      Opcode (uint8)
//   [55:48] type    IRType (uint8) — result type
//   [47:32] flags   IRFlags (uint16) — guard, side effect, gen, dce mark
//   [31:16] op1     IRRef (uint16)
//   [15:0]  op2     IRRef (uint16)
type IRIns struct {
    Op    Opcode   // uint8
    Type  IRType   // uint8
    Flags IRFlags  // uint16
    Op1   IRRef    // uint16
    Op2   IRRef    // uint16
}
// Compile-time assertion: unsafe.Sizeof(IRIns{}) == 8

type Opcode uint8
type IRType uint8
type IRFlags uint16
type IRRef uint16
```

### 2.2 IRRef 宽度(uint16 vs uint32)

`uint16` = 65536 IRRef,与 `MaxTraceIns=4000 + MaxTraceKs=512` 有充裕 headroom;若 side trace 树生长时 IRRef 需跨 trace 编号(v3),可改 uint32。v1 定 uint16。

### 2.3 IRFlags 位定义

```go
const (
    FlagGuard        IRFlags = 1 << 0 // 这是 guard,失败时 exit
    FlagSideEffect   IRFlags = 1 << 1 // 有可观察副作用(HSTORE / ASTORE / CALLN 等)
    FlagDCEKeep      IRFlags = 1 << 2 // DCE 标记「必须保留」(root:guard/side effect/snapshot ref)
    FlagCSEDone      IRFlags = 1 << 3 // 已参与 CSE hash
    FlagIsPhi        IRFlags = 1 << 4 // loop header PHI 节点(见 §4.4)
    FlagSunk         IRFlags = 1 << 5 // v2:allocation sink 标记
    // 6..15 reserved
)
```

### 2.4 操作数编码约定

- 双操作数 op(ADD/SUB/等):`Op1` `Op2` 均是 IRRef,指向另一 ins 或常量;
- 单操作数 op(NEG/NOT/LOAD 类):`Op1` = 输入 IRRef,`Op2` 存辅助信息(如 slot 号、shape 号,§4 详);
- 零操作数 op(常量 KNUM/KGC/LOOP marker):`Op1`/`Op2` 空或存立即数(常量号)。

对不能用 16 bit 装下的辅助数据(如 32-bit hash、64-bit constant),`Op1`/`Op2` 存**一个 IRRef 指向 kBuf 的常量入口**——常量池是变长 payload 的间接层,这与「所有值统一 IRRef 引用」保持一致。

---

## 3. IR 类型 lattice(IRT)

### 3.1 与 NaN-box tag 表一一对应

承 [01-value-object-model §3.3](../p1-interpreter/01-value-object-model.md):

```go
type IRType uint8
const (
    IRTNil        IRType = iota  // 对应 NaN-box tag 0xFFF8
    IRTFalse                     // 0xFFF9 payload=0
    IRTTrue                      // 0xFFF9 payload=1
    IRTLightUD                   // 0xFFFA
    IRTStr                       // 0xFFFB
    IRTTab                       // 0xFFFC
    IRTFunc                      // 0xFFFD
    IRTUData                     // 0xFFFE
    IRTThread                    // 0xFFFF
    IRTNum                       // 数字(f64,无 NaN-box tag)
    IRTUnknown                   // 未观察或多态,只允许出现在 exit 边界的抽象值
)
```

**核心决策 — 数字只有 IRTNum(f64),不区分 int**:

- P1 值模型([01](../p1-interpreter/01-value-object-model.md))**只有 f64 一种数字类型**,Lua 5.1 语义 rope(承 [../roadmap §6](../roadmap.md) 拒绝 5.2+/integer subtype);
- 因此 P5 IR 也**不引入 integer narrowing**——ADD/SUB/MUL/DIV 一律 f64,即使循环变量整数化明显也不做(留 v2 或永不做);
- LuaJIT 的 `IRT_INT` 是 5.3+ 有 integer subtype 后才有意义,望舒 5.1 没这个必要。**v1 IR 就一种数字类型:IRTNum = f64。**

`IRTFalse` / `IRTTrue` 拆开是因为 P1 tag 是 `0xFFF9` payload 决定 true/false,IR 层保留同款可分辨性(便于常量折叠 `NOT true → false`)。

### 3.2 lattice 结构

```
                IRTUnknown  (⊤)
               /     |      \
           IRTNum  IRTStr  IRTTab  IRTFunc ...
            (每一个是叶子;无 subtyping,无 join 除 top)
```

**没有 subtyping**、没有中间格点——望舒 lattice 是**平的**,只有「已知类型」和「未知」二态。这与 [P4 03 §3.4](../p4-method-jit/03-speculation-ic.md) guard 硬约束一致:P5 快路径都是**单类型**,`IRTUnknown` 只能出现在 exit 抽象值或未 guard 过的位置。

### 3.3 类型 guard 的输出类型

- `GUARD_NUM x` : 若 x 类型 unknown,guard 后类型收窄成 `IRTNum`;若 x 类型已是 IRTNum 则 guard 冗余,fold engine 消掉(§4 CSE / guard-dedup);
- `GUARD_TYPE x, IRTStr` : 类似,窄化到 IRTStr;
- 类型 guard 本身的 IRIns.Type = 原类型(不产生新值),它只**收窄下游对 x 的类型认知**。

---

## 4. IR opcode 全表

按类别分组(约 50 op)。列:操作数、结果类型、副作用类、guard?、备注。

### 4.1 常量(3 op)

| Op | 操作数 | Type | 副作用 | Guard | 备注 |
|---|---|---|---|---|---|
| `KNUM` | op1=常量号(kBuf 索引) | IRTNum | 无 | 否 | f64 常量,数值本身存 kBuf entry |
| `KGC` | op1=GCRef 索引(kBuf) | IRTStr/IRTTab/IRTFunc/IRTUData/IRTThread | 无 | 否 | 指向 arena 已 intern 或已存在的 GC 对象 |
| `KPRI` | op1=原语(0=nil, 1=false, 2=true, 3=lightuserdata handle) | 对应类型 | 无 | 否 | nil / false / true / lightuserdata 立即数 |

### 4.2 slot / arena 读写(9 op)

| Op | 操作数 | Type | 副作用 | Guard | 备注 |
|---|---|---|---|---|---|
| `SLOAD` | op1=slot 号,op2=预期类型 tag | 对应 IRT | 无 | 隐含 GUARD_TYPE | 从值栈 slot 读到 IR value,同时插类型 guard |
| `ALOAD` | op1=tbl IRRef,op2=array index | IRTUnknown(下游需 GUARD_TYPE 收窄)| 无 | 否 | 表数组段读 |
| `HLOAD` | op1=tbl IRRef,op2=hash slot index | IRTUnknown | 无 | 否 | 表 hash 节点读 |
| `ASTORE` | op1=slot 号,op2=值 IRRef | — | ✅ | 否 | 写值栈 slot |
| `ARSTORE` | op1=tbl IRRef,op2=(index<<16)\|value_ref | — | ✅ | 否 | 表数组段写(命名:Array Ref STORE) |
| `HSTORE` | op1=tbl IRRef,op2=(slot<<16)\|value_ref | — | ✅ | 否 | 表 hash 节点写 |
| `ULOAD` | op1=closure IRRef,op2=upvalue 索引 | IRTUnknown | 无 | 否 | upvalue 读 |
| `USTORE` | op1=closure IRRef,op2=(idx<<16)\|value_ref | — | ✅ | 否 | upvalue 写 |
| `GLOAD` | op1=globals IRRef,op2=hash slot index | IRTUnknown | 无 | 否 | GETGLOBAL 直达槽读;实现上等价 HLOAD(globals, index),但保留独立 op 便于 pattern-match |

**注意**:`ARSTORE` / `HSTORE` / `USTORE` 参数打包成 (index<<16)|value_ref,因为 IRIns 的 op1/op2 各只有 uint16;若 index 需要 > 16 位,通过 kBuf 常量间接一层。同理写入的复合参数编码规则可以在 emit helper 里封装。

### 4.3 算术(f64,8 op)

| Op | 操作数 | Type | 副作用 | Guard | 备注 |
|---|---|---|---|---|---|
| `ADD` / `SUB` / `MUL` / `DIV` | op1, op2 = num IRRef | IRTNum | 无 | 否 | f64 算术,fold engine 折叠常量;§9 red line: 无 unsafe reassoc |
| `MOD` | op1, op2 | IRTNum | 无 | 否 | Lua 语义 `a-floor(a/b)*b`;div-by-zero 得 NaN(f64 语义) |
| `POW` | op1, op2 | IRTNum | 无 | 否 | 与 `math.pow` 一致,不 fold 到 x86 fpu(承 [../roadmap §6](../roadmap.md)) |
| `NEG` | op1 = num IRRef | IRTNum | 无 | 否 | 一元 UNM |
| `ABS` | op1 = num IRRef | IRTNum | 无 | 否 | 备用 op(供 fold 引擎生成,如 `|x|` 模式) |

### 4.4 比较(guard 形式,3 op)

比较 IR ins **本身就是 guard**(承 [02-trace-recording §3.7](./02-trace-recording.md)):不产 boolean 值,而是产 `taken/not-taken` 方向 guard。

| Op | 操作数 | Type | 副作用 | Guard | 备注 |
|---|---|---|---|---|---|
| `GUARD_EQ_DIR` | op1, op2 | 无输出 | 无 | ✅ FlagGuard | 观察方向失败 → exit;操作数类型必须已收窄一致 |
| `GUARD_LT_DIR` | op1, op2(均 num 或均 str) | 无输出 | 无 | ✅ | 承 P1 05 §4.4 快路径 |
| `GUARD_LE_DIR` | op1, op2 | 无输出 | 无 | ✅ | 同 LT |
| `GUARD_TRUTHY_DIR` | op1 | 无输出 | 无 | ✅ | TEST/TESTSET 用;truthy 语义:非 nil 且非 false 即 true |

### 4.5 控制流(3 op)

| Op | 操作数 | Type | 副作用 | Guard | 备注 |
|---|---|---|---|---|---|
| `LOOP` | — | — | 无 | 否 | Loop header marker,后续 PHI 节点从此起。[04-optimization-passes §6] loop peeling 的 anchor |
| `PHI` | op1=loop-invariant 侧 IRRef,op2=loop-carried 侧 IRRef | 依 op1/op2 类型 | 无 | 否 | 循环携带值的两侧融合;loop peeling 之后仅出现在 loop header 之后一段 |
| `NOP` | — | — | 无 | 否 | 占位(DCE 后不删,让 IRRef 编号不动;或 fold 产生的 dead 结果) |

### 4.6 调用与 host helper(4 op)

| Op | 操作数 | Type | 副作用 | Guard | 备注 |
|---|---|---|---|---|---|
| `GUARD_CALLEE_ID` | op1=callee IRRef,op2=kBuf GCRef | 无 | 无 | ✅ | 「被调 closure 身份 == 预期 closure」guard |
| `CALLN` | op1=helper ID(kBuf 或立即),op2=参数打包 IRRef | 依 helper 声明 | ✅(保守,除非声明纯函数) | 否 | 内联到 trace 内的 host 侧 helper 调用;类似 P3/P4 的 `h_arith` 类 helper。v1 极少用(NYI 一般 abort 而非 CALLN 退) |
| `RETURN_INLINED` | op1=返回值 IRRef | — | 无 | 否 | 被内联函数的 RETURN 展开;录制期真实 return 已 pop frameStack,这条 IR 仅 marker(在 snapshot 里为「exit 到 caller CALL 后一条」提供 pc) |
| `CALL_TAIL` | op1=callee IRRef | — | ✅ | 否 | 尾调用退回 crescent 的 marker(v1 一般直接 abort 而不用这个) |

### 4.7 分配(2 op,v2 用)

| Op | 操作数 | Type | 副作用 | Guard | 备注 |
|---|---|---|---|---|---|
| `TNEW` | op1=arrayCap(kBuf),op2=hashCap(kBuf) | IRTTab | ✅(除非 sunk)| 否 | 表构造,v2 可 sink |
| `TDUP` | op1=模板表 GCRef(kBuf) | IRTTab | ✅ | 否 | 表构造(带模板,SETLIST 模式) |

### 4.8 GC / barrier(2 op)

| Op | 操作数 | Type | 副作用 | Guard | 备注 |
|---|---|---|---|---|---|
| `GCSTEP` | — | — | ✅ FENCE | 否 | Safepoint;所有 memory CSE 到此为止(§3 详见 [04-optimization-passes]) |
| `FENCE` | — | — | ✅ FENCE | 否 | 通用副作用屏障(metamethod 触发点、CALLN 未声明纯) |

### 4.9 guard(独立分类,详 §5)

- `GUARD_NUM x`
- `GUARD_TYPE x, tagID`
- `GUARD_TABLESHAPE tbl, gen`
- `GUARD_UPVAL_OPEN uv`(open upvalue 未 close 前不变)
- `GUARD_LOOP_CONT idx, limit, step_sign`(FORLOOP 方向 guard)
- 以及 §4.4 比较类 guard

所有 guard 共同点:`FlagGuard` 置位;`Op1`/`Op2` 编码 guard 谓词的输入;guard 关联的 snapshot 通过 IR ins 在数组中的位置查(每个 guard emit 时,recorder 记录当前的 snapshot ref,详 §5 + [06-snapshot-deopt](./06-snapshot-deopt.md))。

---

## 5. guard 在 IR 中的位置 + snapshot 耦合契约

### 5.1 guard IR ins 的双重身份

guard IR 既是**语义节点**(它可以有类型 side effect,如 GUARD_NUM 之后其 op1 被视为 IRTNum),又是**exit 点**(FlagGuard 置位)。regalloc 与 codegen 都需知晓这个双重身份:发射机器码时,guard 前要保证「若在此 exit,需要的所有 IR value 都可被 snapshot 引用」(每 guard 的 snapshot 是 06-snapshot-deopt 的责任)。

### 5.2 每 guard 一份 snapshot,协议

录制期,每次 emit 一条 guard IR ins 时:

```
recorder.EmitGuard(op, op1, op2):
  snapRef := recorder.TakeSnapshot()   // 当前 slot 状态的稀疏映射,详见 06
  ins := IRIns{Op:op, Op1:op1, Op2:op2, Flags:FlagGuard}
  // snapRef 记录在旁存的 GuardMeta[irRef] 里,不占 IRIns 8 字节
  recorder.guardMeta[irRefOf(ins)] = snapRef
```

`GuardMeta` 是与 `IRBuf.ins` 并行的一段稀疏映射:

```go
type GuardMeta struct {
    SnapshotRef uint16   // 索引到 snapshots[] 数组
    ExitPC      uint32   // 冗余记录,便于 debug
}
// 稀疏:map[IRRef]GuardMeta 或与 ins 同长的 slice(全 trace 满 guard 也不过 MaxTraceExits ~16,slice 稀疏)
```

**契约(与 [06-snapshot-deopt](./06-snapshot-deopt.md) 硬耦合)**:

1. **guard 一旦 emit,其 snapshot 不再变化**;后续 pass 不允许 rewrite snapshot 内容;
2. **snapshot 引用的 IR value 是 DCE 的 root**(承 [04-optimization-passes §4](./04-optimization-passes.md)):若 snapshot 里出现 IRRef X,则 X 对应的 IR ins 不能被 DCE;
3. **guard 与其 snapshot 在 codegen 时同位生成**:regalloc 需保证 snapshot 里引用的所有 IR value 在 guard exit 点可恢复(spill 到栈或在寄存器);
4. **不同 guard 的 snapshot 可以共享子结构**(压缩),编码协议是 06 的事,本文 IR 层只承诺「guard→snapshot 是 1:1 关系」。

### 5.3 guard 种类清单(继承 P4 + 新增分支方向)

承 [P4 03 §2](../p4-method-jit/03-speculation-ic.md) 五档 FeedbackKind + 加分支方向:

| guard 种类 | IR op | 谓词 | 失败频率(录制期观察) |
|---|---|---|---|
| number 类型 | GUARD_NUM | IsNumber(u64) 单比较 | 罕见(承 IC numHits 高) |
| 一般 tag | GUARD_TYPE | (u64 >> 48) == expected_tag | 罕见 |
| 表 shape | GUARD_TABLESHAPE | tbl.gen == expected_gen | 罕见(表结构不常变) |
| callee identity | GUARD_CALLEE_ID | callee.GCRef == expected_gcref | 依 IC mono/mega |
| 分支方向 | GUARD_EQ_DIR / GUARD_LT_DIR / GUARD_LE_DIR / GUARD_TRUTHY_DIR | 比较结果 == 观察方向 | 依循环形态,某些 branch stable 极高 |
| loop continue | GUARD_LOOP_CONT | i <= limit(方向敏感) | 通常最后一轮才失败(loop 出口) |
| open upvalue | GUARD_UPVAL_OPEN | uv 未 close | 罕见,主要防御性 |

**关键**:所有 guard 都是**单次显式比较**(承前提二 4 税「无信号陷阱」+ P4 03 §3 硬约束),没有 implicit trap 或页面保护类机制。

---

## 6. 常量与 interning

### 6.1 kBuf entry 格式

kBuf 每条 entry 也是 8 字节 IRIns 形式,但语义是「常量」:

```go
// 复用 IRIns 结构,Op 为 K* 类,payload 存 op1/op2:
// KNUM: op1|op2 打包 f64 的低/高 32 位(f64 用 unsafe.BitsToUint64 打)
// KGC:  op1 存 GCRef 的低 16 位,op2 存 GCRef 的次 16 位(需要 32 位处理再看,若不够则拆两 entry)
// KPRI: op1 存原语号,op2=0
```

`GCRef` 是 48 位,kBuf 一 entry 只 32 位有效——拆成两条(payload = GCRef 高 16 + 低 32)或引入 32-bit kBuf(每条 IRIns 存 op1|op2 = 32 bit payload)。**PT2 定稿**:一条 kBuf entry 用两条 IRRef 索引连续两 entry 表示一个 GCRef(第一条低 32 位、第二条高 16 位) — 简单直接,GCRef 常量不多不影响 fold 效率。

### 6.2 常量 interning

- **f64 KNUM**:用 bit pattern 作 key hash-cons;规范化 NaN(承 [01 §3.4](../p1-interpreter/01-value-object-model.md))为 `0x7FF8_0000_0000_0000`;-0 与 +0 视作不同 KNUM(§9 red line: 不合并 -0 / +0);
- **GCRef KGC**:字符串已在 arena intern,GCRef 相等即身份相等,直接 hash-cons on GCRef;table/function 也 hash-cons on GCRef(允许「已 sunk table 的 KGC」——但 sunk 的 KGC 只出现在 snapshot 引用中,不参与 fold);
- **KPRI**:nil/false/true 各一个 IRRef,全 trace 复用。

interning 目的是让 FOLD engine `cnst_a + cnst_b` 判定两个 IRRef 是否指向同一常量能用整数比较,不用值比较。

---

## 7. IR 打印器(PT2 必须的调试基建)

**PT2 里程碑的硬交付物**——因为 04 / 05 / 06 每一个 pass 出错后调试全靠肉眼读 IR,没有 dumper 后续里程碑全部瘫痪。

参照 LuaJIT `-jdump` 输出格式(种子 §7 提到「trace 录制 / IR / 优化 / regalloc」全套调试 legend LuaJIT 已定型):

```
---- TRACE #1 start proto_id=42 start_pc=0x28
0000 [snap #0]
0001    num SLOAD   #1  [num]        ; R(1) → v0001, guard num
0002    num SLOAD   #2  [num]
0003    num ADD     0001 0002        ; f64.add
0004    > num GUARD_NUM 0003         ; guard: result must be num
0005    tab SLOAD   #3  [tab]
0006    > tab GUARD_TABLESHAPE 0005 gen=17
0007    num ALOAD   0005 5
0008    > num GUARD_NUM 0007
0009    num ADD     0003 0007
0010    -   ASTORE  #4 0009
0011    -   LOOP
0012    phi 0009 <- 0003              ; loop-carried
...
0042    > int GUARD_LT_DIR 0009 0100 dir=taken
---- TRACE #1 end (loop closed, 43 ins, 6 guards, 3 snapshots)
```

约定:

- 每行 IRRef(4 位十六 / 十进制)+ 结果类型(3 字符缩写:num/tab/str/fnc/nil/tru/fls/uda/thr/unk)+ op 名 + 操作数(用 4 位 IRRef)+ 备注;
- `>` 前缀标注 FlagGuard 的 ins;
- `[snap #k]` 每次 emit guard 时插入的伪行,标注 snapshot 编号,便于与 06 的 snapshot 编码对账;
- 常量以负值 IRRef 显示(如 `-0004` 表 kBuf[3])或以「立即数」形式(`num 3.14`)inline;
- 「trace start / end」行含起点 proto、pc、总 ins / guard / snapshot 数——利于差分 fuzz 的 minimizer 归类 trace。

### 7.1 输出通道

- 环境变量 `WANGSHU_JITDUMP=1` 打开(仅 build tag `p5trace` 下有效);
- 输出到 stderr(默认)或文件(`WANGSHU_JITDUMP_FILE=...`);
- 每 pass 后可选 dump(承 [04-optimization-passes §8](./04-optimization-passes.md) 的 pass toggle 基建),便于 diff `--before-cse` / `--after-cse` 定位是哪 pass 引入的 bug;
- 差分 fuzz 落 abort 或 wrong-result 时自动 dump(record + optimize + regalloc + machine code 全套 artifact 归档 [08-testing-strategy](./08-testing-strategy.md))。

---

## 8. 开放问题(记入 doc-gaps 待 PT2 验证)

- **IRIns 8 字节是否够用**——若 IRRef 需要 uint32(side trace 树跨 trace 编号),IRIns 变 12 字节,cache 密度损失多少 PT2 实测。
- **常量池布局**——是否值得把 KNUM 直接 inline 进 IRIns 的 op1/op2(f64 bit pattern 32+32 位打包)避免间接一层 kBuf?PT2 fold engine 实测。
- **guard 边元数据存哪**——GuardMeta 与 IRIns 并行 slice vs map,PT2 实测(guard 数少 slice 稀疏,可能 map 反而好)。
- **PHI 节点的位置约定**——是否强制紧接 LOOP marker 之后,还是可以散在 loop 体首段;后者更灵活但 loop peeling 生成更繁,PT6 决定。
- **RETURN_INLINED 是否值得存在**——录制期真实 return 已 pop frameStack,RETURN_INLINED 只是 marker 供 snapshot 记录 exit pc;若 snapshot 直接记 pc 不需 marker 可去掉。PT2 早期决。
- **CALLN 的使用范围**——v1 主要是 abort 而非 CALLN 退,但如果 P5 引入 pure host fn 白名单(§8 [02-trace-recording](./02-trace-recording.md) 开放问题第 3 条),CALLN 就有用武之地,届时需要为 CALLN 定 helper 声明格式(纯 / 只读 / 有副作用)。
- **IR emit 的 fold-on-emit 契约**——[04-optimization-passes §1] 承诺每次 emit 先过 fold,若结果是已存在 IRRef 直接返回该 ref 不 emit 新 ins;fold 表本身规模 PT2 定(LuaJIT fold table 约 1500 行 C 规则,望舒不复制,按最常见 f64 fold + Lua 特有 pattern 起步 ~200 条起)。
- **KGC 引用的 GCRef 是否需接 GC 根**——kBuf 里的 GCRef 是 arena 里的对象,若 trace 期间被 GC 掉则 KGC 悬垂。目前设想是:录制期 GC safepoint 关闭 或 kBuf 通过 State 常驻根接根,PT1 定。

---

相关:
[./00-overview.md](./00-overview.md)(00-overview §2 IR 决策 / §4.4 三顶点相互耦合 / §6 IR 形式待定) ·
[./02-trace-recording](./02-trace-recording.md)(录制器发射本文 IR ops,§3 表右列) ·
[./04-optimization-passes](./04-optimization-passes.md)(FOLD-on-emit / CSE / DCE / LICM 全部消费本文 IR) ·
[./05-register-allocation](./05-register-allocation.md)(单遍逆序扫描,06-snapshot-deopt §4 三顶点第三个) ·
[./06-snapshot-deopt](./06-snapshot-deopt.md)(guard→snapshot 契约的具体压缩 + 物化协议) ·
[../p1-interpreter/01-value-object-model](../p1-interpreter/01-value-object-model.md)(§3 NaN-box tag = IRType 输入源) ·
[../p1-interpreter/02-bytecode-isa](../p1-interpreter/02-bytecode-isa.md)(§7 ICSlot 是 SLOAD/ALOAD/HLOAD 的物理复用) ·
[../p4-method-jit/03-speculation-ic](../p4-method-jit/03-speculation-ic.md)(guard 硬约束) ·
[../p4-method-jit/04-osr-deopt](../p4-method-jit/04-osr-deopt.md)(memmove 物化,是 snapshot 的运行期出口)
