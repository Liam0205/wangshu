# P5 §4:优化 pass——FOLD-on-emit / CSE / DCE / guard dedup / LICM(loop peeling)+ v2 sink

> 状态:**未立项图纸**(P5 尚未立项,本文是启动闸门 [01-launch-judgment](./01-launch-judgment.md) 通过后可以逐步照做的施工设计,不代表任何已实现代码)。
>
> 对应 Go 包:`internal/fullmoon/trace/opt`(fold engine + CSE + DCE + LICM 全部作用于 [03-ir-design](./03-ir-design.md) 的 IRBuf;每 pass 一个子文件,便于 §8 pass toggle 基建按 pass 独立开关)。
>
> 上游契约:
> [./00-overview.md](./00-overview.md)(00-overview §2 流水线图 ② IR 优化 SSA/CSE/LICM(loop peeling)/DCE/guard dedup / §4.4 「LuaJIT 的 IR、snapshot、regalloc 三者互为前提」——本文的正确性红线全部由此推出 / §6 sink 后置到 v2)、
> [../../../llmdoc/must/design-premises](../../../llmdoc/must/design-premises.md)(前提三原则 2 差分是投机层主防线,§9 semantic red lines 是差分之前的静态防线;前提四第一天 NaN-box 承诺——§2 FOLD 引擎不允许生成非规范 NaN 位模式,是「值表示不变式」在优化 pass 层的现金兑现)、
> [../roadmap](../roadmap.md)(§7 LuaJIT 范本:FOLD 引擎 + loop peeling = LuaJIT 三大标志之二)。
>
> P1 依赖面:
> [../p1-interpreter/01-value-object-model](../p1-interpreter/01-value-object-model.md)(§3.4 NaN 规范化不变式:任何 NaN 必须是 `0x7FF8_0000_0000_0000`——FOLD engine 若生成 raw NaN 直接违反 tag 空间,§2 详)、
> [../p1-interpreter/06-memory-gc](../p1-interpreter/06-memory-gc.md)(GC safepoint 位置——决定 §3 CSE 的 FENCE 点)、
> [../p1-interpreter/07-metatables-metamethods](../p1-interpreter/07-metatables-metamethods.md)(元方法可见副作用——§9 red line 主要源)。
>
> P4 对位:
> [../p4-method-jit/03-speculation-ic](../p4-method-jit/03-speculation-ic.md)(P4 无跨指令优化——§1 单 pass fold-on-emit 相对 P4 的核心增益就在这里;guard 硬约束继承)。
>
> 下游协作(同子目录):
> [./03-ir-design](./03-ir-design.md)(IR ins / IRRef / IRFlags 是本文的对象;§4 IR op 表是本文所有 fold rule / CSE key 的定义源)、
> [./05-register-allocation](./05-register-allocation.md)(逆序扫描消费本文 §4 DCE 之后的 IR 数组;LOOP marker / PHI 节点是 regalloc 的关键路标)、
> [./06-snapshot-deopt](./06-snapshot-deopt.md)(§4 DCE root:snapshot 引用的 IR value 不可删——本文 §4 硬耦合 06 的契约)、
> [./08-testing-strategy](./08-testing-strategy.md)(§8 pass toggle 是差分 fuzz 定位「哪 pass 引入错果」的一阶手段)。

---

## 0. 定位:一遍(半)扫,而非多遍 IR rewrite

### 0.1 pass pipeline 概览

```
录制期 IR emit                          录制结束后
──────────────────►──────────────────►──────────────────►
                    fold-on-emit      loop peeling         逆序 DCE + guard dedup
                     (§2 + §3)         (§6,只 loop trace)  (§4 + §5)
                                                                │
                                                                ▼
                                                        [05-register-allocation]
```

**核心决策 — 优化不是「emit → 多轮 rewrite → 最优」,而是「每次 emit 先过 fold + CSE」**(承06-snapshot-deopt §4「LuaJIT 折叠引擎」):

- 录制器 emit IR ins 前,fold engine 先看能否折成常量 / 复用已存在 ins;若成 ⇒ 不真 emit,返回已有 IRRef;
- fold miss ⇒ CSE hash 表查是否有等价 ins;命中 ⇒ 返回旧 IRRef;
- CSE miss ⇒ 真 emit;写入 CSE hash 表 + 更新类型 lattice。

这就是「FOLD-on-emit」——单趟录制期 + 单趟录制后清理(DCE + guard dedup),无多轮 iterative rewrite。这个策略的哲学是:**trace 是线性的,已经把控制流拍扁了;一趟顺 emit + 一趟逆 sweep 拿走 80% 的静态收益,剩下的 20% 需要 alias 分析、CFG 图算法、iterative worklist 的重优化不做**——它们该属于 v2 或永不做。

### 0.2 为什么不多 pass

三条:

1. **投机层不该背静态编译器的复杂度**——种子 §5 说「投机最重的层,主防线在此最关键」,pass 越多越难保证 §9 semantic red line;
2. **trace 一次性使用**——不像 P4 method JIT 一份代码常驻服役,trace 编完之后 side exit 会不断催生新 trace(v2/v3),把优化时间花在单条 trace 上边际收益递减;
3. **fold-on-emit 单趟已足够**——LuaJIT 十几年的实践证明单趟 fold + peeling + DCE 就能吃到多 pass 传统编译器 80-90% 的收益。

**唯一的例外是 loop peeling**——它需要在录制结束、知道整条 loop trace 后才能做(§6),是一次「跨 IR ins 的结构性 rewrite」;但它也只跑一次,不是 iterative。

### 0.3 章节路标

§1 pass 顺序与哲学 → §2 FOLD 引擎(含 f64/NaN 硬约束)→ §3 CSE(含 alias/fence 规则表)→ §4 DCE(含 snapshot 硬耦合)→ §5 guard dedup → §6 LICM via loop peeling(带 worked example)→ §7 分配下沉 sink(v2 sketch)→ §8 pass toggle 基建(差分 fuzz 一阶手段)→ §9 semantic red lines 表 → §10 开放问题。

---

## 1. pass 顺序与哲学

### 1.1 全体 pass 与触发时机

| Pass | 触发时机 | 复杂度 | 依赖 | v1/v2 |
|---|---|---|---|---|
| **FOLD** | 每次 emit 之前 | O(1) per emit(hash 表 + fold rules 查表) | 无 | v1 |
| **CSE** | 每次 emit 之后、fold 之后 | O(1) hash-cons + alias 规则检查 | 需要 § 3 alias/FENCE 规则 | v1 |
| **guard dedup** | emit 期(与 CSE 同款 hash-cons,专对 guard) | O(1) | dominates 是天然的(线性 trace) | v1 |
| **loop peeling** | 录制结束、闭合 loop trace 之后一次性 | O(trace 长度) | LOOP marker + PHI 生成 | v1 |
| **DCE** | loop peeling 之后、regalloc 之前 | O(trace 长度) 一趟逆扫 | snapshot ref 已冻结 | v1 |
| **sink / escape** | DCE 之前、loop peeling 之后 | O(trace 长度) 但含 use-def 追踪 | 完整 use-def 分析 | v2 |

**关键**:v1 的所有 pass 都是**线性时间**(O(N),N=IR ins 数)。没有 iterative fixed-point、没有 worklist。这与线性 trace + 单趟 emit 的结构完全对齐。

### 1.2 pass 之间的顺序不变式

- **CSE 只在 fold 之后**:fold 可能把 `MUL x 1` 变成 `x`,CSE 才有得可 dedupe;倒过来先 CSE 会保留冗余;
- **loop peeling 在 DCE 之前**:peeling 会引入 PHI 节点,某些 IR ins 变成 loop-carried;若先 DCE 可能把 peeling 需要的 「首轮特化输入」删掉;
- **snapshot 在 emit 期就固化**:pass 期间不再改;这是与 06 的核心契约,详 §4;
- **regalloc 在所有 pass 之后**:regalloc 依赖 DCE 完成之后的最终 IR 数组决定 live range,pass 之间的中间态不给 regalloc。

---

## 2. FOLD 引擎

### 2.1 emit-hook 形式

```go
// 简化伪码
func (b *IRBuf) Emit(op Opcode, typ IRType, op1, op2 IRRef) IRRef {
    // 1. 常量折叠尝试
    if r, ok := b.foldConst(op, op1, op2); ok {
        return r
    }
    // 2. 代数/身份 fold
    if r, ok := b.foldAlgebraic(op, op1, op2); ok {
        return r
    }
    // 3. CSE 尝试(§3)
    if r, ok := b.cseLookup(op, typ, op1, op2); ok {
        return r
    }
    // 4. 真 emit
    return b.appendIns(op, typ, op1, op2)
}
```

### 2.2 常量折叠(f64 硬约束)

**只允许 IEEE-754 语义下必然结果一致的 fold**。示例:

| pattern | 折成 | 允许? | 备注 |
|---|---|---|---|
| `ADD (KNUM a) (KNUM b)` | `KNUM (a+b)` | ✅ | 若 `a+b` 结果是 NaN,必须规范化(§2.3) |
| `MUL (KNUM a) (KNUM b)` | `KNUM (a*b)` | ✅ | 同上 |
| `NEG (KNUM a)` | `KNUM (-a)` | ✅ | 注意 `-0.0` != `+0.0`(位模式不同,§9 red line) |
| `ADD x (KNUM 0)` | `x` | **✗** | 违反 `x + (-0) = x` 而 `x + (+0)` 在 x=-0 时 = +0;IEEE-754 加零不是身份 |
| `SUB x (KNUM 0)` | `x` | ✅ | `x - (+0) = x` 恒成立(x=NaN 也满足,NaN != NaN 但位模式保留) |
| `SUB x x` | `KNUM 0` | **✗** | 若 x=NaN 则 `x-x=NaN` 不是 0 |
| `MUL x (KNUM 1)` | `x` | ✅ | `x * 1 = x` 恒(NaN * 1 = NaN 位模式保留) |
| `MUL x (KNUM 0)` | `KNUM 0` | **✗** | `NaN * 0 = NaN`,`Inf * 0 = NaN`,不能折 0 |
| `ADD (ADD x c1) c2` | `ADD x (c1+c2)` | **✗** | **f64 无 unsafe reassoc**——`(x+c1)+c2 != x+(c1+c2)` in general |

**Lua 特有**:

| pattern | 折成 | 允许? | 备注 |
|---|---|---|---|
| `KPRI nil == KPRI nil` | `taken` | ✅ | GUARD_EQ_DIR 恒 taken → guard 消掉(§5 dedup) |
| `KGC str_a == KGC str_a` | `taken` | ✅ | 承 P1 字符串 intern,GCRef 相等即身份相等 |
| `NOT (NOT x)` | `x`(若 x 已是 boolean) | ✅ | 若 x 是任意值 truthy 语义两次 not 会规范化;严格看类型 |
| `NOT (KPRI nil)` | `KPRI true` | ✅ | Lua truthy 语义 |
| `NOT (KPRI false)` | `KPRI true` | ✅ | 同上 |
| `NOT (KNUM 0)` | `KPRI false` | ✅ | 0 不是 nil/false ⇒ truthy ⇒ NOT = false |
| `LEN (KGC str)` | `KNUM byteLen(str)` | ✅ | 字符串常量长度 P1 stdlib 语义 |

fold 表(实际实现 ~200 条起,按 emit 频率排序;LuaJIT fold table ~1500 条作参考锚)。表本身独立文件 `fold_rules.go`,便于 §8 pass toggle 单独关闭 fold 一档一档验证。

### 2.3 NaN 规范化不变式的现金兑付

承 [../p1-interpreter/01-value-object-model §3.4](../p1-interpreter/01-value-object-model.md):**值世界中任何 NaN 必须是规范正 qNaN `0x7FF8_0000_0000_0000`**。这条不变式在 P5 FOLD 引擎有具体现金流:

```go
func canonicalizeNaN(x float64) float64 {
    if math.IsNaN(x) {
        return math.Float64frombits(0x7FF8_0000_0000_0000)
    }
    return x
}
// 所有 KNUM fold 结果必须过一遍 canonicalizeNaN
```

**为什么这条硬约束**:承 memory reflection `2026-07-02-p4-beat-p3-opset-round` 教训 4 —— fuzz seed `f7f0bb1a` 抓到 x86 SSE arith 结果 `0xFFF8...` 与 NaN-box tag 空间别名(NaN-box tag 首位从 0xFFF8 起,若 f64 计算结果落到 `0xFFF8...` 且未规范化,该 u64 值会被下游误读成 `tag=nil` 或某种 GC 类型的箱)。P4 native 侧靠 arith inline 加 result guard 路由到 `host.Arith` 兜底;P5 FOLD 引擎是编译期,不能等到运行期兜底,**必须在 fold 时就规范化**。任何 fold rule 生成 raw f64 常量结果都过 canonicalize。

### 2.4 fold 与 guard 的交互

fold 若能证明 guard 恒成立(常量比较、类型静态可知):

- 若 `GUARD_EQ_DIR (KNUM 3) (KNUM 3) taken=true` ⇒ 消掉这条 guard(与 §5 guard dedup 同 mechanism);
- 若 `GUARD_NUM (KNUM 3.14)` ⇒ 常量类型静态是 num,guard 恒过,消掉;
- 若 `GUARD_EQ_DIR (KNUM 3) (KNUM 5) taken=true` ⇒ 恒失败 ⇒ **不消,反而应立即 abort trace**——录制期本不该走到这个假设(观察方向与静态事实矛盾)。

**注**:消 guard 时,对应的 snapshot 也不再被 guard 引用,DCE 阶段会连带清理其唯一引用的 IR value(承 §4)。

---

## 3. CSE(通用子表达式消除)

### 3.1 CSE hash key

```go
type cseKey struct {
    Op   Opcode
    Op1  IRRef
    Op2  IRRef
}
// hash 表 map[cseKey]IRRef,查表命中即复用旧 IRRef
```

Type 不进 key —— 相同 op/op1/op2 结果类型必然相同(是 IR type system 的确定性)。

### 3.2 hash-cons 命中的条件

- op 完全相同;
- op1、op2 完全相同;
- 且两次 emit 之间**没有跨 FENCE**(§3.4)。

前两条是纯语法条件,第三条是语义条件,靠 CSE hash 表在 FENCE 处清空该做的 subset 来强制(见 §3.4)。

### 3.3 Alias 规则表(Lua 内存模型下的 CSE 谨慎)

CSE 对**纯计算 op**(ADD/SUB/MUL/DIV/NEG/NOT/LEN 等)总是安全的。对 **load op**(SLOAD/ALOAD/HLOAD/ULOAD/GLOAD)需要考虑「中间有没有 store 打破 load 缓存」。

Alias 分析规则表(v1 保守):

| Load 类 | 会被下列 Store 阻断 CSE(设为 alias) |
|---|---|
| `SLOAD reg=r` | 同 slot 的 `ASTORE reg=r`;任何 CALL/RETURN(帧栈可能被搬,slot 语义可能变——保守清空所有 SLOAD)|
| `ALOAD tbl a, idx=i` | `ARSTORE tbl a, idx=i`(同 idx)/ `ARSTORE tbl a, idx=?`(未知 idx 保守 alias)/ `HSTORE tbl a`(可能触发 rehash 挪 array)/ 任何触发 rehash 的 op / 任何 CALLN(未声明纯) |
| `HLOAD tbl a, slot=s` | 同表任意 HSTORE / 触发 rehash / CALLN |
| `ULOAD cl u, idx=i` | 同 (cl, idx) 的 USTORE / CLOSE(可能关闭 upvalue,清 ULOAD) |
| `GLOAD idx=i` | 同 idx 的 GSTORE(等价 HSTORE 到 globals)|

**保守判据**:未知 idx 一律视作 alias(不做数组下标常量对比)。这个策略略保守,但避免了 alias 分析出错 = 静默错果的风险;若 PT3 测试显示某些形态错失明显,再针对性放松。

### 3.4 FENCE 的清空策略

FENCE(承 [03-ir-design §4.8](./03-ir-design.md) `GCSTEP` / `FENCE`)是通用副作用屏障。遇到 FENCE 时,CSE 表按类清空:

```go
func (c *cseTable) OnFence() {
    // 保留:所有纯计算(ADD/SUB/MUL/DIV/NEG/NOT/LEN 等)
    // 清空:所有 load 类(SLOAD/ALOAD/HLOAD/ULOAD/GLOAD)
    // 清空:所有 GUARD_TABLESHAPE(表可能已 rehash 变 gen)
}
```

哪些 op 触发 FENCE:

- 显式 `GCSTEP`(safepoint,承 [05-safepoint-gc](../p3-wasm-tier/05-safepoint-gc.md) 三类点);
- 显式 `FENCE`(record 期发的通用屏障,如 metamethod 触发点—— v1 一般 abort 而不发 FENCE;但 CALLN 未声明纯的会发);
- **CALL_TAIL / RETURN_INLINED / any CALLN**——除非 helper 声明纯函数;
- **任何 STORE**(ASTORE/ARSTORE/HSTORE/USTORE)按 §3.3 alias 规则局部清空,而非全清。

### 3.5 CSE 强度红线

- **允许**:字符串常量 KGC 相等即身份相等(承 P1 intern);
- **允许**:table KGC 若指向同一 GCRef,相等即身份相等(GC 未搬,对象存活期内 GCRef 稳定);
- **禁止**:两个不同 KNUM 常量若数值相等但 bit 不同(如 -0 vs +0)**不合并**(§9 red line)。

---

## 4. DCE(死代码消除)

### 4.1 一趟逆扫,标记 root

```go
func (b *IRBuf) DCE() {
    // 1. 标记 root:
    //    - 所有 FlagGuard = true 的 ins
    //    - 所有 FlagSideEffect = true 的 ins(store 类 / call 类)
    //    - 所有 snapshot 引用的 IR value(通过 snapshots + GuardMeta 遍历)
    // 2. 从 root 起逆序 propagate:
    //    每条 root ins 的 op1/op2(若指向 ins 而非常量)标 FlagDCEKeep
    // 3. 逆序扫,把未标 FlagDCEKeep 且非 root 的 ins 标 NOP(不真删,保留 IRRef 编号)
}
```

### 4.2 与 snapshot 的硬耦合契约

**红线**:snapshot 引用的 IR value 是 DCE root。若 DCE 把某个 snapshot 里出现的 IRRef 对应 ins 删了,guard 失败物化时无源可取(§4 [06-snapshot-deopt](./06-snapshot-deopt.md) 的物化协议依赖 snapshot 里每个引用都活)。

具体形式:

```
snapshot.slots[k] = {slot=r, value=IRRef=15}
IR ins 15 是 SLOAD #2

若 DCE 逻辑漏了 snapshot roots,把 ins 15 删掉,
guard failure 时物化到 slot r 拿不到 ins 15 的运行期值(regalloc 也不会为它保寄存器),
⇒ slot r 里是垃圾 ⇒ 静默错果
```

这是种子 §4.3 P4 vs P5 复杂度对照表「映射数据 = 每 guard 一份 snapshot」的直接现金流:snapshot 不是「记录 exit 时状态」的旁挂标签,**snapshot IS a use**——它对 IR value 施加保留义务。

### 4.3 DCE 不真删,只标 NOP

保留 IRRef 编号稳定(regalloc / 打印器 / snapshot 都按 IRRef 索引),被 DCE 的 ins 变 NOP:

```go
type Opcode uint8
const OpNOP Opcode = ...  // 占位,codegen 跳过
```

codegen 遍历 IR 数组时 `switch ins.Op { case OpNOP: continue; ... }`。IRRef 编号不动的好处:snapshot / GuardMeta / 打印器输出对比等所有跨 pass artifact 保持稳定,便于 §8 pass toggle 差分对账。

---

## 5. guard dedup

### 5.1 dedup 规则

同一条 trace 里,若 guard G1 dominates guard G2(在线性 trace 就是「G1 pc < G2 pc」),且:

- G1 与 G2 op 相同(如都是 `GUARD_NUM`);
- op1/op2 相同(如都对 `IRRef 42` 施加 GUARD_NUM);
- 之间没有可能使 G1 的收窄失效的 op(对 GUARD_NUM 而言:没有中间对 IRRef 42 重新赋值—— IRRef 42 是 SSA,不可能被赋值,所以恒成立;对 GUARD_TABLESHAPE 而言:同表没有 shape-bumping 操作);

⇒ 删 G2(标 NOP),下游对 op1 的类型认知继承自 G1。

### 5.2 与 loop peeling 的交互(核心洞察)

**loop peeling 后**(§6),loop 体的 guards 一部分被 hoist 到 peeled 首轮之后(loop-invariant guards 提到 loop 头之前的 peeled 部分),body 里再遇到同款 guard 时被 dedup 消掉。这是 LICM 「循环不变量提出循环」的 P5 具体现金流——**不是显式的 loop-invariant 移动 pass,而是 「peeling + CSE + guard dedup」三件事的复合效应**。§6 worked example 会展示这一现象。

### 5.3 dedup 与 snapshot 的交互

被 dedup 掉的 guard 有 snapshot——这个 snapshot 也随之 dead。**dedup 后必须清除该 snapshot 的引用义务**,让 DCE 能真删掉那些只被这个 snapshot 引用的 IR value。具体:dedup 把 G2 标 NOP 同时把 `GuardMeta[G2]` 清空,DCE root 计算就不会把该 snapshot 引用的 value 视为 root。

---

## 6. LICM via loop peeling(worked example)

### 6.1 机制概述

LuaJIT 风格 loop peeling:**不做经典的 loop-invariant code motion (LICM)**,而是**把 loop body 复制一遍作为「首轮 iteration」**,然后:

- 首轮里跑到的 loop-invariant loads / guards 是「首次真值」,继续留在首轮;
- loop 主体里再对同样 load / guard 用 CSE + dedup 消掉;
- loop-carried values 通过 PHI 节点连接首轮末尾 → loop body 开头。

结果:「循环不变量」不用显式识别,peeling + CSE 组合自然把它们「留在首轮」+「主体里消掉」。

### 6.2 worked example

源:

```lua
local t = {x=10, y=20}
local s = 0
for i=1,1000000 do
    s = s + t.x
end
```

录制期 IR(简化,只列关键):

```
0001    tab SLOAD    #t             ; t
0002 >  tab GUARD_TABLESHAPE 0001 gen=17
0003    num HLOAD    0001 slotX     ; t.x 首次读
0004 >  num GUARD_NUM 0003
0005    num SLOAD    #s             ; s 首次
0006 >  num GUARD_NUM 0005
0007    num SLOAD    #i             ; i 首次
0008 >  num GUARD_NUM 0007
0009    num ADD      0005 0003      ; s + t.x
0010    -   ASTORE   #s 0009
0011    num KNUM     1
0012    num ADD      0007 0011      ; i + 1
0013    -   ASTORE   #i 0012
0014 >  num GUARD_LT_DIR 0012 KNUM(1000000) dir=taken
0015    -   LOOP
                         ↓ loop peeling
```

peeling 之后(把 0001..0014 视为 peeled 首轮,LOOP marker 之后是主体,主体是 0001..0014 的克隆但引用 loop-carried 输入):

```
;; peeled 首轮(0001..0014,同上)
0015    -   LOOP
;; loop body(clone),开头 PHI 节点合并 loop-carried:
0016    num PHI      0005 0009      ; s: 首轮末=0009,loop-carried
0017    num PHI      0007 0012      ; i: 首轮末=0012,loop-carried
;; 克隆 body(注释显示 CSE / dedup 命中):
0018    tab SLOAD    #t             ; 同 0001,CSE 命中 ⇒ 复用 0001
                                   ; (SLOAD 若未跨 FENCE,复用)
                                   ; 假设无 FENCE,dedup 掉
0019 >  tab GUARD_TABLESHAPE 0001 gen=17   ; 同 0002,guard-dedup 消掉
0020    num HLOAD    0001 slotX     ; 同 0003,CSE 命中(0001 未被写)
                                   ; ⇒ 复用 0003,dedup 掉
0021 >  num GUARD_NUM 0003          ; 同 0004,dedup
0022    num ADD      0016 0003      ; s' = new_s + t.x(!用 PHI 0016)
0023    -   ASTORE   #s 0022
0024    num KNUM     1              ; CSE 命中 0011
0025    num ADD      0017 0011      ; i' = new_i + 1
0026    -   ASTORE   #i 0025
0027 >  num GUARD_LT_DIR 0025 KNUM(1000000) dir=taken
0028    -   JMP → LOOP
```

DCE 之后(把所有被 CSE/dedup 掉的 clone 变 NOP):

```
0001    tab SLOAD    #t
0002 >  tab GUARD_TABLESHAPE 0001 gen=17
0003    num HLOAD    0001 slotX
0004 >  num GUARD_NUM 0003
0005    num SLOAD    #s
0006 >  num GUARD_NUM 0005
0007    num SLOAD    #i
0008 >  num GUARD_NUM 0007
0009    num ADD      0005 0003
0010    -   ASTORE   #s 0009
0011    num KNUM     1
0012    num ADD      0007 0011
0013    -   ASTORE   #i 0012
0014 >  num GUARD_LT_DIR 0012 KNUM(1000000) dir=taken
0015    -   LOOP
0016    num PHI      0005 0009      ; s loop-carried
0017    num PHI      0007 0012      ; i loop-carried
0018..0021              NOP         ; SLOAD/GUARD/HLOAD/GUARD 全消
0022    num ADD      0016 0003      ; loop body: s + t.x(t.x 是 IRRef 0003,首轮已算)
0023    -   ASTORE   #s 0022
0024              NOP                ; KNUM 1 复用
0025    num ADD      0017 0011      ; i + 1
0026    -   ASTORE   #i 0025
0027 >  num GUARD_LT_DIR 0025 KNUM(1000000) dir=taken
0028    -   JMP → LOOP
```

**关键收益**:

- `t.x` 的 HLOAD(0003)只在 peeled 首轮做一次,主体不再重读——**LICM 效果自然达成,没写显式 loop-invariant 分析**;
- `t.gen` 的 GUARD_TABLESHAPE(0002)同样只在首轮做,主体的 GUARD_TABLESHAPE 被 dedup 消掉——**guard 也提出循环**;
- loop 主体的 hot inner loop 变成:PHI + ADD + ASTORE + ADD + ASTORE + GUARD_LT_DIR + JMP——**7 条 IR ins,几乎全在寄存器内**,是 P5 相对 P4 method JIT 的核心增益具体化。

### 6.3 peeling 的边界条件

- 只对**闭合 loop trace** 做([02-trace-recording §4.1](./02-trace-recording.md));
- 若首轮内就 abort(比如首轮某 guard fail 到不同状态),peeling 不发生;
- PHI 节点只出现在 LOOP marker 之后紧接;
- 首轮里的 store 副作用**保留**(不因 peeling 而变 dead——它是真实执行的第一轮);
- **不递归 peeling**:嵌套 loop trace 不 peeling 内 loop,内 loop 视作直线控制流,若内 loop 长度已知就展开,未知就 abort。

### 6.4 PHI 的类型规则

`PHI a b`:类型 = `a` 和 `b` 的合并;若 `a.Type == b.Type` 直接取该类型;否则 `IRTUnknown` —— 而 IRTUnknown 说明 loop 内该值类型不稳,应当在首轮之末就 guard(不然主体 GUARD_TYPE 每轮都跑,LICM 失效)。**录制器应确保 loop-carried 值类型一致**,若不一致 abort。

---

## 7. 分配下沉 / 逃逸(v2 sketch)

### 7.1 目标

trace 内 `TNEW`(表构造)产生的 IR value:

- 若该 IR value 从未被 store 到 escaping location(不属于 trace 外可见状态)、从未被 CALLN 参数化传出、从未被 USTORE 到 upvalue、从未被 ASTORE 到值栈(值栈是 trace 外可见)⇒ 认定为 **non-escaping**;
- non-escaping 的 TNEW 可以 **sunk**:machine code 不真实分配,IR 层用「散字段」代替(把 t.x, t.y 拆成独立 IR value);
- sunk 对象的 snapshot 记录「重建配方」(如「TNEW arr=2 hash=0,slot 0 = value ref 15,slot 1 = value ref 22」),deopt 时按配方真实分配 + 填字段(unsink)。

### 7.2 例子(v2)

源:

```lua
for i=1,1000000 do
    local p = {x=i, y=i*i}
    process(p)
end
```

若 `process` 内联进 trace 且没有把 `p` 存到 escaping location(例如它只读 p.x/p.y 不存 p 到全局或 upvalue),则 `p` 是 non-escaping ⇒ TNEW 被 sink,machine code 直接把 `x=i` `y=i*i` 保留在 IR value / 寄存器,不真实分配。每轮迭代省一次 arena 分配 + GC 压力。

### 7.3 v1 状态

- v1 **不做** sink;`TNEW` 老实分配;
- v1 也不做完整 escape 分析(承06-snapshot-deopt §4 sink 是「P5 最深的优化,可后置到 v2」);
- v1 里 `TNEW` 的存在本身就使 trace 变长、guard 变多——若真实宿主 hot loop 高频 TNEW,v1 收益打折,是 v2 优先级前置的信号(承 [01-launch-judgment §2](./01-launch-judgment.md) 第三类形状);
- **v2 交付协议**:sink 的具体机制、unsink 于 deopt 的细节、与 GC 的交互全部由 [06-snapshot-deopt](./06-snapshot-deopt.md) v2 补章拥有;本文只承诺「若 sink 打开,DCE 要认 sunk 状态的 snapshot 引用义务」。

---

## 8. pass toggle 基建(硬要求,不是 nice-to-have)

### 8.1 为什么是硬要求

**差分 fuzz 抓到「wangshu fullmoon 与 crescent 输出不一致」时,须能定位到「哪个 pass 引入了错果」**。若所有 pass 是一个开关(启用/禁用整个 fullmoon),定位只能靠人肉逐 pass 复现;pass toggle 就是自动化的一阶手段。

承 memory reflection `2026-06-15-p3-pw9-acceptance-perf-round` 教训 2 「prove-the-path-under-test」家族已在望舒工程被反复确认——p5 的每一 pass 都是投机层,静默错果的第一防线是差分,差分能否收敛到根因取决于 pass 是否可独立关闭。

### 8.2 toggle 接口

```go
type OptConfig struct {
    Fold       bool  // §2 FOLD-on-emit
    CSE        bool  // §3 CSE(alias 规则完整,仅关 hash-cons 命中判定)
    DCE        bool  // §4 DCE
    GuardDedup bool  // §5 guard dedup
    LoopPeel   bool  // §6 loop peeling
    Sink       bool  // §7 v2 sink
}

// 环境变量:WANGSHU_P5_OPT=fold,cse,dce,dedup,peel  以逗号列表选择开启
// 默认全开(生产);差分套按 subset 组合驱动
```

### 8.3 pass matrix 差分

[08-testing-strategy](./08-testing-strategy.md) §5 将定义:

- **A**:crescent 一路解释(baseline)
- **B**:gibbous(P4)
- **C**:fullmoon,全 pass 关(录制 + 直翻,无优化)
- **D**:fullmoon,只 FOLD
- **E**:fullmoon,FOLD + CSE
- **F**:fullmoon,FOLD + CSE + DCE + guard dedup
- **G**:fullmoon,全 pass
- **H**(v2):fullmoon,全 pass + sink

差分要求 A vs B vs C..H 逐字节等价。若某档挂,直接把「wrong result」归到最后加入的 pass。这是 [../p1-interpreter/12-testing-difftest](../p1-interpreter/12-testing-difftest.md) 差分套的一次结构性增强,承种子 §5「优化 pass 分级差分」直接落地。

### 8.4 与 pass 顺序不变式(§1.2)的关系

某些 pass 组合可能不合法(如「关 FOLD 开 CSE」——CSE 依赖 FOLD 提供的 canonical form?——PT2 定):toggle 应当在无效组合时 fail-fast(启动时 log 报错并 abort),不静默降级。

---

## 9. 语义红线表(什么优化必须永远不做)

综合 §2 f64 硬约束、Lua 语义、[../p1-interpreter/07-metatables-metamethods](../p1-interpreter/07-metatables-metamethods.md) 元方法可见副作用、prior art 教训。

| # | 红线 | 影响的 pass | 违反后果 |
|---|---|---|---|
| 1 | **不 reassoc f64**:`(a+b)+c ≠ a+(b+c)` in general | FOLD | 结果与 crescent 差异 → 静默错果 |
| 2 | **不合并 -0 与 +0** | FOLD / CSE | `1/(-0) = -Inf` 而 `1/(+0) = +Inf`,合并后错果 |
| 3 | **不生成 non-canonical NaN**(承 §2.3) | FOLD | 结果 u64 与 NaN-box tag 空间碰撞,tag 系统崩 |
| 4 | **不将 `x*0` fold 为 `0`** | FOLD | NaN * 0 = NaN,Inf * 0 = NaN,不是 0 |
| 5 | **不将 `x-x` fold 为 `0`** | FOLD | NaN - NaN = NaN |
| 6 | **不 reorder 有可观察副作用的 op** | CSE / DCE / peeling | metamethod 打印顺序变化,用户可见 |
| 7 | **不 reorder 跨 FENCE 的 load/store**(承 §3.4) | CSE | GC 之后 load 陈旧值,静默错果 |
| 8 | **不改 table 别名假设**(超出 §3.3 的规则) | CSE / DCE | 表被别名写而未失效 load 缓存 → 陈旧值 |
| 9 | **不通过身份重写改变 GC 对象生存** | 全体 | 影响 __gc 元方法调用时机(P1 5.1 有支持,但 P5 若变对象生死 = 副作用) |
| 10 | **不 fold `NOT (NOT x)` = x 若 x 非 boolean 类型** | FOLD | `NOT (NOT nil)` 应为 `false` 不是 `nil` |
| 11 | **不 rewrite 已 emit 的 IR** | pass 结构 | 违反「一趟顺 emit + 一趟逆扫」的 pipeline 原则 |
| 12 | **不删 snapshot 引用的 IR value**(承 §4.2) | DCE | 物化时无源 → 静默错果 |
| 13 | **不 hoist 出 loop 的 store**(peeling 只 hoist load / guard,不 hoist store) | LICM | 存进 loop 外的表会被别处观察,副作用可见 |
| 14 | **允许**:字符串 GCRef 身份相等 = 逻辑相等(承 P1 intern)——这是**允许的 strength**(不是禁止),CSE 应利用它加速 EQ guard fold | — | — |

---

## 10. 开放问题(记入 doc-gaps 待 PT2-PT7 实测)

- **FOLD rule 表规模的起点** —— 200 条起够不够?对哪一类真实宿主脚本形态覆盖率不足需要扩?PT2 早期用「dump 未 fold 的 pattern」出频度报告,按需扩表。
- **CSE 严格程度 vs alias 精度** —— §3.3 保守规则(未知 idx 一律 alias)会错失多少?PT3 差分对比「严格 CSE」vs「精细 alias CSE」的正确性一致性 + 性能差异;若精细版能过 fuzz 且明显更快,提升。
- **peeling 是否应对某些形态跳过** —— 极短 loop(body <10 ins)peeling 未必划算(增 code size 换微小 LICM 收益);实测阈值 PT6 定。
- **PHI 类型不稳时的处理** —— 强制 abort or 走 IRTUnknown 加运行期 dispatch;v1 选 abort,若真实宿主频繁触发 PT6 复看。
- **CSE hash 表规模与内存管理** —— 每 trace 一份 hash 表,peeling 后 body 阶段是否重建?PT3 早期定。
- **sink 与 GC safepoint 的交互** —— sunk 对象在 unsink 时可能触发 GC,GC 可能又扫到未 unsink 的 root(空)…细节留 v2 补章。
- **guard 对 CSE 的角色是否需分级** —— GUARD_NUM 是 idempotent 的(重复无副作用),但 GUARD_TABLESHAPE 依赖 table gen(gen 每次读时是运行期值);dedup 时是否需读时 recheck?PT5 差分定。
- **fold 与 constant propagation 的边界** —— IR 常量 propagation 是隐含在 fold 里(kBuf 命中 = 已知常量),还是需要独立 CP pass?v1 定隐含,若 PT2 实测某些 pattern 需要独立 CP,再拆。

---

相关:
[./00-overview.md](./00-overview.md)(00-overview §2 优化 pass / §4.4 三顶点耦合 / §6 sink 后置 v2) ·
[./02-trace-recording](./02-trace-recording.md)(§3 逐 op 录制表 = 本文 IR 输入源) ·
[./03-ir-design](./03-ir-design.md)(§4 IR op 全表 = 本文 fold / CSE / DCE 的对象;§5 guard 硬耦合 snapshot;§6 常量 interning) ·
[./05-register-allocation](./05-register-allocation.md)(逆序扫描消费本文 DCE 之后的 IR;LOOP marker / PHI 是路标) ·
[./06-snapshot-deopt](./06-snapshot-deopt.md)(§4 snapshot IS a use 硬耦合;v2 unsink 由 06 补章拥有) ·
[./08-testing-strategy](./08-testing-strategy.md)(§8 pass toggle = pass matrix 差分的一阶手段) ·
[../p1-interpreter/01-value-object-model](../p1-interpreter/01-value-object-model.md)(§3.4 NaN 规范化 = §2.3 现金兑付) ·
[../p1-interpreter/07-metatables-metamethods](../p1-interpreter/07-metatables-metamethods.md)(§9 red line 6 副作用可见性) ·
[../p4-method-jit/03-speculation-ic](../p4-method-jit/03-speculation-ic.md)(P4 无跨指令优化 = 本文 §1 相对增益的对偶面)
