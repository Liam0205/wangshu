# P3-06 IC 与 TypeFeedback:非投机消费(与 P2 零 deopt 口径严格一致)

> 状态:**详细设计**。本文是 P3 文档集 [00-overview](./00-overview.md) §0 文档地图中「feedback 非投机消费」单一事实源,承 p3-wasm-tier 单文件原稿 §3(8 行原稿)的全卷展开。
> **本文定位一句话**:**P3 是 try-compile,不依赖 feedback 正确性 — 在代码层落实为两条铁律:① 快路径检查 = 语义分发,不是投机 guard;② IC 快照编译期固化,失效自然降级到助手(慢但正确)**。
>
> 上游种子:p3-wasm-tier 单文件原稿 §3(行 202-209,8 行)+ §10 不变式 1。
> 上游契约:
> - [00-overview](./00-overview.md)(P3 总览;§1 P3/P4 不对称消费 feedback、§3 关键耦合 5 IC 快照编译期固化、§9 不变式 1 语义分发非投机)
> - [02-translation](./02-translation.md)(P3 翻译器;本文是其 IC 翻译形态的细化扩展)
> - [04-trampoline](./04-trampoline.md)(慢路径 imported 助手回 Go,本文 IC miss 降级路径接入点)
> - [../p2-bridge/02-ic-feedback](../p2-bridge/02-ic-feedback.md)(P2 已落地的 TypeFeedback shape 完整定义,本文是消费侧)
> - [../p2-bridge/04-try-compile-fallback](../p2-bridge/04-try-compile-fallback.md) §1(fallback ≠ deopt)+ §1.3(P2/P3 静态保证 vs P4 投机)
> - [../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md) §1(P3/P4 不对称消费 feedback,P3 不依赖 feedback 正确性)
> - [../p1-interpreter/02-bytecode-isa](../p1-interpreter/02-bytecode-isa.md) §7(ICSlot 结构)
> - [../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md) §6(IC 执行机制 — P3 翻译时与之同构)
> - `internal/bridge/aggregator.go`(P2 已落地的聚合器,产出 PointFeedback)
>
> 下游衔接:[../p4-method-jit](../p4-method-jit.md) §3.4(P4 投机失败 deopt + 重训机制 — 本文「IC 失效是否重编译」的统一评估归属)。

对应 Go 包:`internal/gibbous/wasm`(本文消费方);上游契约方 `internal/bridge`(`PointFeedback` 产出)、`internal/bytecode`(`ICSlot` 读取)。

---

## 0. 定位:P3 是兑现机器,不是投机机器

### 0.1 P3 的角色一句话

承 [00-overview](./00-overview.md) §1 边界表与 [../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md) §1.2:

> P3 接到 P2 产出的 TypeFeedback **可选消费**——把它当作「代码形状提示」而非「类型断言」;P4 才把它当作「投机依据」(必需消费)。

这个差异不是文档分工说说而已,它在每一行 Wasm 翻译里都体现:**P3 翻译出来的代码,即便接到完全错误的 feedback 仍正确**(只是性能不优);P4 不行(feedback 严重失真会让 P4 频繁 deopt 直至放弃投机,P4 §3.4)。本文把这条原则切到代码层面,告诉翻译器实现者:**当你看到 `FBArithStableNumber` 时,你不是在「赌」操作数是 number,你是在『把双 number 快路径放在 then 分支』** —— 一个布局选择,不是一个语义承诺。

### 0.2 与 P2 零 deopt 口径的字面一致

[../p2-bridge/04-try-compile-fallback](../p2-bridge/04-try-compile-fallback.md) §1 已立的「fallback ≠ deopt」表里有一行:

> P2/P3 走 try-compile,**编译的子集对任何运行期输入都正确**。编译出的 gibbous 代码不依赖任何运行期类型假设,**所以根本不需要 deopt** ——没有「假设被打破」的可能。

「不依赖运行期类型假设」这八个字,是本文要在每个 IC 翻译形态上反复兑现的承诺。具体到代码层:

| 上游承诺 | 本文兑现的代码层动作 |
|---|---|
| 编译的子集对任何输入正确 | §1 「快路径检查 = 语义分发」:`IsNumber×2` 失败也是合法路径,落到慢路径助手得正确结果 |
| 不依赖运行期类型假设 | §3 「即便 feedback 完全错,P3 仍正确」:feedback 影响代码形状不影响语义覆盖面 |
| 没有「假设被打破」的可能 | §2 「IC 快照编译期固化,失效自然降级」:校验失败不是「投机失败」,只是「该走助手了」 |
| 根本不需要 deopt | §4 「失效降级到助手 ≠ deopt」:不离开 Wasm 函数,不需要 OSR exit 机器 |

### 0.3 两条铁律(本文展开骨架)

承 p3-wasm-tier 单文件原稿 §3 直接给出的两条:

1. **快路径检查 = 语义分发,不是投机 guard**(§1 详)。P3 翻译里所有「`IsNumber×2`」「同表同代次」「globals gen 校验」之类的运行期判定,都是**语义分发**——失败也是合法语义路径,无 deopt 概念。feedback 只决定「内联哪条快路径、固化哪份 IC 快照」,即代码形状,不影响语义覆盖面。

2. **IC 快照编译期固化,失效自然降级**(§2 详)。解释器 IC slot 是运行期可变的(mono IC 重填,[../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md) §6.3);gibbous 把编译时刻的快照烧进代码。表形状变化(gen bump)→ 快照永久 miss → 该点每次走助手 ≈ 解释器无 IC 的水平,**正确但慢**。

下面 §1-§5 就是这两条铁律分别在「翻译器入口」「单 opcode 翻译形态」「与 feedback 字段的接口」「不变式」上的兑现。

### 0.4 与 P3 单文件原稿 §3 的关系

p3-wasm-tier 单文件原稿 §3 是本文的种子(8 行,定方向);本文是它的详细设计扩展(覆盖六种 FeedbackKind 翻译形态、PointFeedback 字段消费协议、与 ICSlot 双源选取策略、缺口与 P4 接口归属)。两文档关系:

| 内容 | 单文件原稿 §3 | 本详细设计 |
|---|---|---|
| fallback ≠ deopt 在代码层的兑现 | 一行口径(「快路径检查 = 语义分发非投机 guard」) | §1 全展开(P4 投机 guard 对照表 + ADD/GETTABLE 两种翻译详例 + feedback 完全错仍正确论证) |
| IC 快照编译期固化 | 一行口径(「失效自然降级」) | §2 全展开(SNAP 立即数布局 + gen bump 物理失效路径 + 「正确但慢」是定式 + 失效计数留 P4 评估) |
| FeedbackKind 翻译形态 | (无,留 02-translation §3 隐式提及) | §3 六枚举值逐一展开(WAT 伪码 + 失效降级路径) |
| 翻译器接口 | (无) | §4 emit_<op> 函数签名 + race-tolerant 读 + 双源快照(feedback vs ICSlot)选取策略 |
| 不变式 | 单文件原稿 §10 不变式 1 一条 | §5 五条系统化(覆盖语义分发 / 编译期固化 / 不读 confidence / nil 容忍 / 零 deopt) |
| 缺口 | 单文件原稿 §11 末「IC 快照失效 → 重编译」一条 | §6 四条(失效重编译归属 P4 + 双源选取策略 + LT/LE 子分流 + megamorphic 主动识别接入) |

后续维护以本详细设计为准。

---

## 1. 「快路径检查 = 语义分发,不是投机 guard」(本节核心)

### 1.1 与 P4 投机 guard 的根本对比

承 [../p2-bridge/04-try-compile-fallback](../p2-bridge/04-try-compile-fallback.md) §1.3 与 [../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md) §1.3 的两张对照表,本节按「同一组运行期判定 → 完全不同的语义后果」给最终归并表。

考虑同一段比较 + 条件跳的机器码雏形(在 P3 / P4 物理形态可能完全相同):

```
;; 比较 b/c 是否都是 number(NaN-box 单 u64 比较)
local.set $vb (i64.load offset=8*B (local.get $base))
local.set $vc (i64.load offset=8*C (local.get $base))
local.set $is_num
  (i32.and
    (i64.lt_u (local.get $vb) (i64.const 0xFFF8000000000000))
    (i64.lt_u (local.get $vc) (i64.const 0xFFF8000000000000)))
```

P3 与 P4 后续路径的根本分野:

| 维度 | **P3 的 IsNumber×2(语义分发)** | **P4 的 IsNumber×2(投机 guard)** |
|---|---|---|
| 失败后续路径 | 调慢路径 imported helper(`call $h_arith`),走 metamethod / coercion → 正确返回 → 直线继续 Wasm | OSR exit:写回栈槽真相 + 跳出 JIT 世界 + crescent 接管该帧剩余执行 |
| 函数边界 | **不离开 Wasm 函数**(只是离开内联快路径,经 imported 调用回 Go,Go 助手返回后 Wasm 直线继续) | **离开 JIT 函数**,转给解释器 |
| 语义层定位 | 失败是「合法 Lua 语义路径」(Lua 5.1 允许字符串自动转数字 / 允许 `__add` 元方法) | 失败是「投机假设不成立」(原本假设此点恒为 number) |
| feedback 的角色 | 提示「该形态出现概率高 → 把快路径放 then 分支」,即代码布局 | 投机依据「该点恒为 number → 跳过类型分流,直接发 f64.add」,即代码省略 |
| feedback 错的后果 | 无可观察后果(慢路径仍正确返回 metamethod 结果) | 频繁 deopt → 性能塌陷 → 最终 P4 拉黑此点投机 |
| 是否需要 OSR/deopt 机器 | **不需要**(没离开 Wasm) | **需要**(P4 §3 OSR exit 机器) |

**关键观察**:**两边发的都可能是「比较 + 条件跳」**(物理形态),**但语义/机器码后续路径完全不同**。P3 的「IsNumber×2 + 内联 f64.add」是**完整的 ADD 翻译**——含所有合法 Lua 语义路径(快+慢);P4 的「IsNumber×2 (guard) + f64.add」是**裁剪版 ADD 投机**——只覆盖 number 路径,其他路径靠 OSR 把执行整体转给 crescent。

> **物理同形 ≠ 语义同义**:不要因为两者都发了 `i64.lt_u` + `if` 就以为 P3 也在投机。投机的字面定义是「省略某些合法语义分支,赌它不发生」——P3 不省略,慢路径助手就在 if 的 else 分支里,完整覆盖 metamethod / coercion。P4 才省略(慢路径不在 JIT 代码里,在 crescent 解释器里)。

### 1.2 P3 ADD 翻译里 `IsNumber×2` 是语义分发

承 [02-translation](./02-translation.md) §3.2 ADD 翻译形态(亦见原稿 §2.3 同源伪码),本节在 IC feedback 视角下重述:

```wat
;; emit_add(pc, A, B, C, fb.Points[pc]):
;;   fb.Points[pc].Kind 决定代码布局,但 if/else 两条路径都必发(全语义覆盖)
(local.set $vb (i64.load offset=8*B (local.get $base)))
(local.set $vc (i64.load offset=8*C (local.get $base)))
(if (i32.and  ;; IsNumber×2 — 语义分发,与解释器 §4.1 同款
      (i64.lt_u (local.get $vb) (i64.const 0xFFF8000000000000))
      (i64.lt_u (local.get $vc) (i64.const 0xFFF8000000000000)))
  (then  ;; 双 number 快路径(布局优化:fb=FBArithStableNumber 时放此分支)
    (local.set $r (f64.add (f64.reinterpret_i64 (local.get $vb))
                           (f64.reinterpret_i64 (local.get $vc))))
    ;; canonicalizeNaN(01 §3.4):f64 NaN 全部规范化为 0x7FF8000000000000
    (if (f64.ne (local.get $r) (local.get $r))
      (then (local.set $r (f64.reinterpret_i64 (i64.const 0x7FF8000000000000)))))
    (i64.store offset=8*A (local.get $base) (i64.reinterpret_f64 (local.get $r))))
  (else  ;; 慢路径:imported 助手(coercion/__add/string→number/...)
    (br_if $err (call $h_arith (local.get $base) (i32.const PC) (i32.const OP_ADD)))))
```

**论证「这是语义分发,不是投机 guard」的三个具体形态**:

1. **else 分支永远存在**——无论 fb 给什么 kind,`(else (call $h_arith ...))` 都必发。如果 P3 像 P4 那样省掉 else 把它转给 OSR exit,**那才是投机**。但 P3 不省。
2. **if 的判定与解释器同款**——[../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md) §4.1 的 `value.IsNumber(b) && value.IsNumber(c)` 是**解释器的快路径前置检查**(同样含「失败走慢路径」语义);P3 把这个判定原样翻译成 Wasm `i64.lt_u + i32.and`。**同一组判定**,差别只在「P1 用 Go 函数实现,P3 用 Wasm 指令实现」。
3. **慢路径助手得正确结果**——`$h_arith` 是 imported Go 函数(走 [04-trampoline](./04-trampoline.md) §3 helper 机制),内部调 `crescent.doArith` 慢路径(coercion + `__add` 元方法 + 错误抛出)。返回时 R(A) 已被正确写入,返回 status=0;Wasm 直线继续。**不存在「P3 算错」的可能**——慢路径就是 P1 慢路径同源。

### 1.3 P3 GETTABLE 翻译里「同表同代次」是语义分发

GETTABLE 的形态比 ADD 复杂(IC 快照固化是 §2 主题),但语义分发的原则一样。承 [02-translation](./02-translation.md) §3.4 GETTABLE 翻译(亦见原稿 §2.3 同源伪码):

```wat
;; emit_gettable(pc, A, B, C, fb.Points[pc]):
;;   fb.Points[pc].Kind = FBTableMono → 内联 IC 快照校验
;;   fb.Points[pc].Kind = FBTableMega / FBUnstable / nil → 直接发通用查找
(local.set $t (i64.load offset=8*B (local.get $base)))
(if (i32.and (call $is_table (local.get $t))
             (i32.and
                (i64.eq (call $gcref (local.get $t)) (i64.const SNAP_TABLEREF))
                (i32.eq (call $gen   (local.get $t)) (i32.const SNAP_GEN))))
  (then  ;; 同表同代次 ⇒ 直达槽(SNAP_KIND 决定 array/node)
    ;; (SNAP_INDEX 也是编译期立即数,§2.2)
    (i64.store offset=8*A (local.get $base) (call $slot_load ...)))
  (else  ;; miss/形状变了/初始非表 ⇒ 完整查找+元方法,正确性不依赖快照
    (br_if $err (call $h_gettable (local.get $base) (i32.const PC)))))
```

**论证「同表同代次校验是语义分发,不是投机 guard」的对照**:

| | P3 「同表 + 同 gen」校验 | P4 同款校验(若做) |
|---|---|---|
| 失败语义 | 「快照失效,该走完整查找了」(语义合法路径) | 「投机前提不成立,该 deopt 了」 |
| 失败动作 | call `$h_gettable`(走 [../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md) §6.3 完整查找,含 metamethod) | OSR exit |
| 失败时是否仍在 Wasm 函数 | **是**(call helper 是 imported 调用,Go 助手返回后继续 Wasm) | **否**(离开 JIT 函数,crescent 接管) |
| 解释器同款判定证据 | [../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md) §6.3 `slot.tableRef == ... && slot.shape == t.Gen()` 是**同一组判定** | (P4 复用此判定但语义异化为 guard) |

**关键**:P3 的「同表同代次」就是把解释器 IC 命中流程([../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md) §6.3 doGetTable 中段)的判定逻辑,把里面的运行期 `slot.tableRef`/`slot.shape` 替换成编译期立即数 `SNAP_TABLEREF`/`SNAP_GEN`。**判定逻辑同构,只是数据来源从「运行期 slot」变成「编译期立即数」**。

### 1.4 feedback 只影响代码形状,不影响语义覆盖面

「代码形状」与「语义覆盖面」是 P3 翻译器视角下的两个正交维度:

| 维度 | 说明 | feedback 影响吗? |
|---|---|---|
| **语义覆盖面** | 翻译产物是否覆盖该 opcode 的全部 Lua 5.1 语义(含元方法 / coercion / 错误抛出 / nil 处理) | **不影响**——P3 必须始终覆盖全语义,无论 feedback 是什么形态 |
| **代码形状** | 快路径放 then 还是 else / IC 快照是否内联 / 操作数顺序 / icache 友好度 / 慢路径的折叠程度 | **影响**——FBArithStableNumber 暗示「把双 number 路径放 then」、FBTableMono 暗示「内联 IC 快照」 |

**对照具体形态**(以 ADD 为例):

| feedback | 语义覆盖面 | 代码形状 |
|---|---|---|
| FBArithStableNumber | 双 number 快路径 + metamethod 慢路径(全覆盖) | 快路径放 if-then(主流路径在 hot path,branch predictor 友好) |
| FBUnstable | 双 number 快路径 + metamethod 慢路径(全覆盖) | 可同 FBArithStableNumber 形态(P3 实装可选「Unstable 也内联快路径」),也可全 helper |
| nil | 双 number 快路径 + metamethod 慢路径(全覆盖) | 同 FBUnstable 形态(等价于「无 hint」) |

**三档 feedback 的语义覆盖面相同,代码形状不同**——这是「feedback 影响代码形状不影响语义」的字面体现。

### 1.5 即便 feedback 完全错误,P3 仍正确

考虑一个极端反例:

```
设 feedback 完全反常:某 ADD 点 fb=FBArithStableNumber,但运行期 100% 来 string;
某 GETTABLE 点 fb=FBTableMono(stableShape=A.gen, stableIndex=5),
   但运行期 100% 访问的是另一张表 B 且 B 的形状与 A 不同。
```

P3 翻译产物的运行期表现:

- ADD 点:`IsNumber×2` 校验 100% 失败,**else 分支必走** ⇒ call `$h_arith` ⇒ 走 string coercion / `__add` 元方法 ⇒ **正确返回**。性能 = 100% 慢路径(失去 f64 加速);语义 = 完全正确(等价于解释器 ADD)。
- GETTABLE 点:「同表同代次」校验 100% 失败(tableRef 不同),**else 分支必走** ⇒ call `$h_gettable` ⇒ 走完整哈希查找 ⇒ **正确返回**。性能 = 100% helper(失去 IC 快照加速);语义 = 完全正确(等价于解释器 GETTABLE)。

**结论**:**feedback 错的代价仅是性能损失,正确性 100% 兜底**。这把 P3 与 P4 的安全责任画清:

| 阶段 | 安全责任在哪 | feedback 错的代价 |
|---|---|---|
| **P2 / P3** | **P3 翻译永远覆盖全语义**(语义层兜底) | 性能损失,无可观察错误 |
| **P4** | **运行期 guard + OSR exit**(运行期兜底) | 性能塌陷,无可观察错误(deopt 兜底) |

两阶段都不出错(原则 2「投机错误静默错果」从根上消除),但代价的可控性不同:P3 的「错 feedback 性能损失」只到「等同解释器」(无加速但无回退);P4 的「错 feedback deopt 风暴」可能比解释器还慢(deopt 自身有开销),P4 阶段才需要拉黑投机机制兜底。**P3 不需要这套兜底机制,因为压根没有「比解释器更慢」的退化路径**——这是 P3 选 wazero 的红利之一([00-overview](./00-overview.md) §8 四项税外包)。

---

## 2. IC 快照编译期固化(本节核心)

### 2.1 解释器 IC slot 是运行期可变的

承 [../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md) §6.3 doGetTable 的命中流程:解释器在 IC miss 时**重写** ICSlot 的 `kind` / `shape` / `tableRef` / `index` 字段(mono IC 重填):

```go
// internal/crescent —— P1 已落地形态(05 §6.3 复述)
v, where, idx := t.RawGetWithLoc(key)   // 完整查找
if where != locNone {
    slot.kind     = icKindOf(where)     // ← 运行期写
    slot.index    = idx                 // ← 运行期写
    slot.shape    = t.Gen()             // ← 运行期写
    slot.tableRef = uint32(value.GCRefOf(tbl)) // ← 运行期写
    f.stk[f.base+A(i)] = v
    return nil
}
```

这意味着同一个 pc 上的 ICSlot,**P1 解释器执行期可能反复刷新**:换表(tableRef 不同)即重填,rehash(shape 变)即下次校验失败再重填。这是 [../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md) §6.3 末尾「polymorphic 处理」的具体形态——mono IC 没有降级机制,频繁换表的点反复重填等价于「无 IC」。

**P3 翻译时遇到的第一个抉择**:**编译时刻的 ICSlot 快照,与运行期不断变化的 ICSlot,如何对齐?**

### 2.2 gibbous 把编译时刻的快照烧进代码

定稿:**P3 编译期读一次 ICSlot,把当时刻的快照固化为 WAT 立即数,后续运行期 ICSlot 怎么变与 gibbous 代码无关**。具体形态:

```
编译时刻:
  proto.IC[pc] 当前快照 = (kind=2 nodeHit, shape=42, tableRef=0xABCD0000, index=5)
                             ↓
  gibbous 翻译产物里嵌入 4 个 WAT 立即数:
     SNAP_KIND     = 2          (i32.const)  → 决定走 array 还是 node 直达
     SNAP_GEN      = 42         (i32.const)  → 校验代次时的比对值
     SNAP_TABLEREF = 0xABCD0000 (i64.const)  → 校验同表时的比对值
     SNAP_INDEX    = 5          (i32.const)  → 直达槽下标(取值时用)
                             ↓
  运行期(同 Proto 多次执行 pc):
     若实际 R(B) 是同一张表(arenaOff & 0xFFFFFFFF == 0xABCD0000)
        且当前 t.gen() == 42
        ⇒ 用 SNAP_INDEX=5 直达 node 槽,跳过哈希
     否则 ⇒ call $h_gettable 完整查找
```

**关键性质**:**SNAP_* 是 WAT 立即数,在 wazero 编译产物里就是机器码立即数**——即便后续运行期 P1 解释器把同一 ICSlot 写成 `(kind=2, shape=43, tableRef=0xABCD0000, index=8)`(rehash 之后),gibbous 代码读到的仍是 `SNAP_GEN=42`,失效后直接走 helper。**gibbous 不读运行期 ICSlot**,这与解释器的「读 slot + 校验」不同。

**为什么这样设计**:

1. **零间接寻址**:运行期不需要 load ICSlot 字段(每次 GETTABLE 命中省一次 ICSlot 内存访问 + 间接读)。
2. **编译期常量优化**:wazero 后端可对编译期立即数做优化(常量折叠、寄存器分配),`i32.eq` 比对常量比对运行期 load 来的值,在某些 ISA 上是单条 cmp-immediate 指令。
3. **简化协议**:gibbous 不维护「ICSlot 与代码绑定关系」,P3 编译产物完全自包含。

### 2.3 表形状变化(gen bump)→ 快照永久 miss

考虑同 §2.2 的快照(`SNAP_GEN=42`)。运行期发生:

```
Time T0: 编译 GETTABLE,固化 SNAP_GEN=42
         此时 t.gen() == 42 ⇒ gibbous 代码运行期校验通过 ⇒ 直达槽
         
Time T1: 表 t 因 SETTABLE 触发 rehash → t.bumpGen() → t.gen() == 43
         (按 [../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md) §6.5 失效机制,
          rehash 会 bump gen)

Time T2: gibbous 代码再次执行 GETTABLE,读到 t.gen() == 43
         比对 SNAP_GEN=42 ⇒ 失败 ⇒ 走 else 分支 ⇒ call $h_gettable

Time T3..Tn: 同样比对失败(SNAP_GEN 永远是 42,t.gen() 永远 ≥43)
             ⇒ 此点每次 GETTABLE 都走 helper ≈ 解释器无 IC 的水平
```

**「永久 miss」的物理含义**:gen 是单调递增的(`uint32 bumpGen` 只增不减,[../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md) §6.1),一旦 t.gen() 超过 SNAP_GEN,**永远不会回到等于 SNAP_GEN**(除非 gen 回绕 2^32,实际不可能)。所以快照失效是**不可逆**的——失效后直到 gibbous 代码被重编译(留 P4 评估)前,该 pc 永远走 helper。

**对比解释器的 IC 行为**:解释器在 miss 后会**重填 ICSlot**(slot.shape = t.Gen()),所以解释器的 IC 在 rehash 后过几次访问就重新命中(只要表形态相对稳定)。**gibbous 不重填**——这是 P3 与解释器 IC 行为的最大差异,也是 P3 「正确但慢」定式的物理基础。

### 2.4 「正确但慢」是定式 — 不引入 deopt 通道

承 §2.3 的「永久 miss」,P3 的设计选择有两条岔路:

| 选择 | 形态 | P3 是否选 |
|---|---|---|
| (A) 失效后走 helper,接受性能退化(等同解释器无 IC) | 简单——else 分支已经是 helper,不动语义 | ✅ **P3 基线** |
| (B) 失效后触发「IC 失效计数 → 阈值后 deopt 重编译」 | 复杂——需要计数器 + 阈值 + 重编译触发器 + 状态机扩展 | ❌ **P3 不做** |

**为什么 P3 选 (A) 而不是 (B)**:

1. **(B) 引入了 deopt 通道,违反 P2/P3 零 deopt 口径**。即使 (B) 的「deopt」只是「触发重编译」(不像 P4 那样回到 crescent 解释),仍是「运行期事件让 gibbous 状态发生变化」,这与 [../p2-bridge/04-try-compile-fallback](../p2-bridge/04-try-compile-fallback.md) §2.4 的「无 `Gibbous → Interp` 边」精神冲突——一旦 P3 引入「重编译边」,状态机就不再是单向了。
2. **(B) 的复杂度溢出 P3 范围**。重编译协议需要:① 失效计数器(挂 ICSlot 还是 ProfileData?);② 重编译预算(避免抖动:同一函数反复重编译);③ 重编译时的旧 gibbous 代码 disposal(避免泄漏);④ 重编译前后 trampoline 切换协议(crescent doCall 在重编译期间走哪个)。这套基建 P4 因投机失败 deopt 必然要建([../p4-method-jit](../p4-method-jit.md) §3.4),**P3 单独建一份没有摊薄收益**。
3. **(A) 的性能影响可忍受**。失效后等同解释器无 IC,而 P3 的核心收益不在 IC(IC 是解释器也有的优化),而在 dispatch 与译码的消除([02-translation](./02-translation.md) §2.2「收益来源不靠寄存器提升,而靠消灭 dispatch 与译码」)。即便所有 IC 全部失效,P3 仍比解释器快(每条指令省的「取指 + switch + 操作数解码」是 IC 无关收益)。

**结论:P3 接受「正确但慢」是定式**,失效降级到 helper 是**正常稳态行为**(不是异常,不需要日志),与「IC 命中走快路径」对偶——两者都是合法运行时形态。

### 2.5 失效计数 → 重编译留 P4 一并评估

承 §2.4 决策(2),P3 不引入失效计数与重编译机制——**留给 P4 一并评估**。理由:

1. **P4 必然有 deopt 基建**([../p4-method-jit](../p4-method-jit.md) §3):投机失败需要 OSR exit + 回 crescent + 后续可能重编译(deopt 风暴时拉黑投机)。
2. **P4 的 deopt 基建可顺带覆盖「IC 快照失效重编译」**:gibbous 代码片段过期(无论是 P3 的 IC 失效永久 miss,还是 P4 的投机 guard 反复失败)都是「该重编译这个 Proto 了」的同质事件,统一在 P4 的「再训练机制」(P4 §3.4)处理。
3. **P3 阶段做这件事会被 P4 推翻**:P3 自己做的「IC 失效 → 重编译」协议跟 P4 的「投机失败 → 重编译」协议形态不同(触发条件不同、状态机不同),P4 落地时必然得统一成一套。**P3 阶段先不做,P4 阶段一并设计**——避免重复劳动 + 避免协议分裂。

> **缺口登记**:本节的「P3 IC 快照失效是否触发重编译」决策点登记进 [00-overview](./00-overview.md) §10 风险与未决缺口,链 P4 §3.4 一并评估。详见本文 §6 缺口节。

## 3. 各 FeedbackKind 的 P3 消费方式

承 [../p2-bridge/02-ic-feedback](../p2-bridge/02-ic-feedback.md) §4.3 的 FeedbackKind 枚举(已在 `internal/bridge/feedback.go` 落地),本节按枚举值逐一展开 P3 翻译形态。**口径**:每个枚举值给「快路径布局」+「失效降级路径」+「与 P4 投机模板的对照」三档信息。

### 3.1 FBArithStableNumber

**触发条件**:[../p2-bridge/02-ic-feedback](../p2-bridge/02-ic-feedback.md) §3 算术 IC 双计数比例 ≥ 0.99 且 numHits+metaHits ≥ 100。

**P3 翻译形态**:把双 number 快路径放在 if-then 分支(branch predictor 友好,代码布局优化),metamethod 慢路径放 else 分支:

```wat
;; emit_add(pc, A, B, C, fb=FBArithStableNumber):
(local.set $vb (i64.load offset=8*B (local.get $base)))
(local.set $vc (i64.load offset=8*C (local.get $base)))
(if (i32.and  ;; IsNumber×2 — 主流路径预期成功(fb 提示稳定)
      (i64.lt_u (local.get $vb) (i64.const 0xFFF8000000000000))
      (i64.lt_u (local.get $vc) (i64.const 0xFFF8000000000000)))
  (then  ;; ★ 主流路径 ★ — branch predictor 预测命中,icache 友好
    (local.set $r (f64.add (f64.reinterpret_i64 (local.get $vb))
                           (f64.reinterpret_i64 (local.get $vc))))
    (if (f64.ne (local.get $r) (local.get $r))
      (then (local.set $r (f64.reinterpret_i64 (i64.const 0x7FF8000000000000)))))
    (i64.store offset=8*A (local.get $base) (i64.reinterpret_f64 (local.get $r))))
  (else  ;; 边角路径 — 折叠到边角,不污染主流 cache line
    (br_if $err (call $h_arith (local.get $base) (i32.const PC) (i32.const OP_ADD)))))
```

**与 FBUnstable 的代码层差异**:可视实装成本而定。最简单形态是「FBUnstable 也走同样布局」(P3 默认始终内联 IC 快路径,Unstable 仅是「不如 Stable 那么坚定」的提示);更精细形态是 FBUnstable 时直接发 helper(不内联快路径,代码体积更小)。**P3 PW3 基线推荐前者**(简单 + 一致),后者待 PW9 性能调优时考虑。

**即便 feedback=FBArithStableNumber 但运行期来 string,慢路径 helper 照样工作**:`$h_arith` 走 [../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md) §4.1/§4.2 慢路径(coercion + `__add`),正确返回 metamethod 结果。性能 = 100% 慢路径(失去 f64 加速),但**语义 100% 正确**。

**不读 confidence**:P3 emit_add 只看 `fb.Kind == FBArithStableNumber`,不看 `fb.Confidence`。承 [../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md) §1.4 — confidence 是 P4 才用的旋钮(P4 用 ≥0.99 阈值决定是否发投机模板),P3 不投机所以不需要这个旋钮。**这条对 P3 实装是简化**——P3 emit_<op> 函数签名里 `fb` 参数可以只传 `Kind`,不传 `Confidence`(本文 §4.1 接口形态)。

| | **P3 FBArithStableNumber 翻译**(本文) | **P4 FBArithStableNumber 投机模板**([../p4-method-jit](../p4-method-jit.md) §2.1) |
|---|---|---|
| if-then 分支 | f64.add 快路径 | f64.add 快路径(同形) |
| if-else 分支 | call $h_arith helper(完整慢路径) | OSR exit:写回栈 + 跳出 JIT + crescent 接管 |
| confidence 影响 | 无(只看 Kind) | confidence ≥ 0.99 才发模板;<0.99 退化为通用模板(同 P3 形态) |
| feedback 错的代价 | 100% 走 helper(慢但正确) | OSR 风暴 → 拉黑投机 |

### 3.2 FBTableMono / FBGlobalStable / FBSelfMono

**触发条件**:承 [../p2-bridge/02-ic-feedback](../p2-bridge/02-ic-feedback.md) §2.1 / §2.2 / §2.3:
- FBTableMono:GETTABLE/SETTABLE 上 ICSlot.kind ∈ {1 array hit, 2 node hit, 3 mono-metamethod} 且 Refill 计数 < 阈值;
- FBGlobalStable:GETGLOBAL/SETGLOBAL 上 ICSlot.kind = 2 node hit;
- FBSelfMono:SELF 上 ICSlot.kind ∈ {1, 2, 3}。

三者 P3 翻译形态结构相同,差别只在助手名(`$h_gettable` / `$h_settable` / `$h_getglobal` / `$h_setglobal` / `$h_self`)与立即数选取细节。下面给三档典型 WAT 伪码。

#### 3.2.1 GETTABLE 的 FBTableMono 翻译形态

```wat
;; emit_gettable(pc, A, B, C, fb=FBTableMono):
;;   编译期立即数(从 fb 与 ICSlot 双源选取,§4.4):
;;     SNAP_TABLEREF = uint64(slot.tableRef)        ;; 64 位扩展(从 32-bit ICSlot 字段扩)
;;     SNAP_GEN      = uint32(slot.shape)           ;; 表 gen 代次
;;     SNAP_KIND     = uint8(slot.kind)             ;; 1=array / 2=node / 3=mono-meta
;;     SNAP_INDEX    = uint32(slot.index)           ;; 直达槽下标
(local.set $t (i64.load offset=8*B (local.get $base)))

;; 1. 类型校验:R(B) 必须是 table(NaN-box tag 比对)
(if (call $is_table (local.get $t))
  (then
    ;; 2. 同表校验:tableRef(arena 偏移低 32 位)
    (if (i64.eq (call $gcref_off (local.get $t)) (i64.const SNAP_TABLEREF))
      (then
        ;; 3. 同代次校验:t.gen() == SNAP_GEN
        (if (i32.eq (call $table_gen (local.get $t)) (i32.const SNAP_GEN))
          (then  ;; ★ 三校验通过 ⇒ 直达槽
            ;; SNAP_KIND 在编译期已知,switch 可静态展开为单分支:
            ;; 当 SNAP_KIND=1: i64.store ... (call $array_at (local.get $t) (i32.const SNAP_INDEX))
            ;; 当 SNAP_KIND=2: i64.store ... (call $node_val_at (local.get $t) (i32.const SNAP_INDEX))
            ;; 当 SNAP_KIND=3: 走 mono-metamethod 直达(__index 元方法)
            (i64.store offset=8*A (local.get $base)
              (call $slot_load_<KIND>  ;; 编译期单态选定:array_at / node_val_at / meta_invoke
                    (local.get $t)
                    (i32.const SNAP_INDEX))))
          (else  ;; gen 不同 ⇒ 表已 rehash,快照永久 miss(§2.3)
            (br_if $err (call $h_gettable (local.get $base) (i32.const PC)))))
        ;; ↑ end SNAP_GEN check
      )
      (else  ;; 不是同一张表 ⇒ 缓存目标表已被换/释放
        (br_if $err (call $h_gettable (local.get $base) (i32.const PC)))))
    ;; ↑ end SNAP_TABLEREF check
  )
  (else  ;; R(B) 不是 table ⇒ 走 __index 元方法
    (br_if $err (call $h_gettable (local.get $base) (i32.const PC)))))
```

**关键性质**:

1. **三层校验依次失败都走同一个 helper**(`$h_gettable`)——helper 内部走 [../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md) §6.3 完整 doGetTable(含哈希查找 + `__index` 元方法 + nil 处理)。**任何 IC miss 形态都正确**。
2. **SNAP_KIND 编译期单态选定**——在 emit_gettable 内 `switch fb.kind { case 1: emit_array_at; case 2: emit_node_val_at; case 3: emit_meta_invoke }`,**不发 runtime switch**(代码体积省 + 分支预测无干扰)。
3. **校验顺序遵循 [../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md) §6.3 的 doGetTable**:先 IsTable → 再 tableRef 同表 → 再 shape 同代次。**三层与解释器同款,语义分发**。

#### 3.2.2 GETGLOBAL 的 FBGlobalStable 翻译形态

承 [../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md) §6.4「GETGLOBAL 目标表恒为 globals,tableRef 恒等,只需代次比对」:

```wat
;; emit_getglobal(pc, A, Bx, fb=FBGlobalStable):
;;   编译期立即数:
;;     SNAP_GEN_GLOBALS = slot.shape  ;; globals 表 gen
;;     SNAP_INDEX       = slot.index  ;; globals node 槽位
;;   注意 GETGLOBAL 不需要 SNAP_TABLEREF(目标恒为 globals,身份恒等)
;;   也不需要 SNAP_KIND(globals 恒为 node hit,kind 恒等于 2)

;; 1. 取 globals 表(从 closure 的 _ENV upvalue,常量化为编译期可知)
(local.set $g (call $get_globals_table))

;; 2. 同代次校验
(if (i32.eq (call $table_gen (local.get $g)) (i32.const SNAP_GEN_GLOBALS))
  (then  ;; ★ 命中 ⇒ 直达 globals node 槽
    (i64.store offset=8*A (local.get $base)
      (call $node_val_at (local.get $g) (i32.const SNAP_INDEX))))
  (else  ;; globals 已 rehash(SETGLOBAL 插入新键触发,05 §6.4)
    (br_if $err (call $h_getglobal (local.get $base) (i32.const PC)))))
```

**简化处**:相对 §3.2.1 GETTABLE 形态,GETGLOBAL 省了 IsTable + tableRef 两层校验(globals 身份恒等,无需运行期校验)。这是 globals IC 的物理优势(命中代码更短、cache 更友好)。

#### 3.2.3 SELF 的 FBSelfMono 翻译形态

承 [../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md) §6.4「SELF 先做 R(A+1):=R(B),再走与 GETTABLE 同构的 IC」:

```wat
;; emit_self(pc, A, B, C, fb=FBSelfMono):
;;   立即数同 GETTABLE 的 FBTableMono(§3.2.1)
;; 
;;   1. self 传递:R(A+1) := R(B)
(i64.store offset=8*(A+1) (local.get $base)
  (i64.load offset=8*B (local.get $base)))
;;   2. R(A) := R(B)[RK(C)] — 与 GETTABLE 翻译完全同构
(local.set $t (i64.load offset=8*B (local.get $base)))
(if (i32.and (call $is_table (local.get $t))
             (i32.and (i64.eq (call $gcref_off (local.get $t)) (i64.const SNAP_TABLEREF))
                      (i32.eq (call $table_gen (local.get $t)) (i32.const SNAP_GEN))))
  (then
    (i64.store offset=8*A (local.get $base)
      (call $slot_load_<KIND> (local.get $t) (i32.const SNAP_INDEX))))
  (else
    (br_if $err (call $h_self (local.get $base) (i32.const PC)))))
```

**SELF 的 IC 命中率极高**(方法常驻 metatable,实际负载里几乎不变),所以 FBSelfMono 是 P4 内联方法调用的核心入口([../p4-method-jit](../p4-method-jit.md) §2.1)——**P3 阶段先把这层翻译落实,P4 在此基础上加投机内联**。

#### 3.2.4 失效降级路径汇总

承 §3.2.1 / §3.2.2 / §3.2.3,三档表 IC 的失效降级路径**统一**:

| 失效场景 | 校验失败的层 | 走的 helper |
|---|---|---|
| R(B) 不是 table(GETTABLE/SELF) | IsTable 层 | `$h_gettable` / `$h_self` → 走 `__index` 元方法 |
| 缓存目标表已不是当前表 | tableRef 层(GETTABLE/SELF;GETGLOBAL 无此层) | `$h_gettable` / `$h_self` → 走完整哈希 |
| 表已 rehash(gen bump) | shape 层 | `$h_gettable` / `$h_getglobal` / `$h_self` → 走完整哈希 |

**helper 内部都是 [../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md) §6.3/§6.4 解释器完整流程**,语义层面与解释器逐字节一致([07-tests](./08-testing-strategy.md) 差分门保证)。

### 3.3 FBTableMega

**触发条件**:[../p2-bridge/02-ic-feedback](../p2-bridge/02-ic-feedback.md) §6.2 megamorphic 主动识别(已在 P2+ #4 落地)——`slot.Refill ≥ MegamorphicRefillThreshold`(默认 3 次重填)即标 mega。

**P3 翻译形态**:**不内联 IC 快照,直接发通用 GETTABLE 翻译**(完整哈希查找经 helper):

```wat
;; emit_gettable(pc, A, B, C, fb=FBTableMega):
;;   不内联 IC 快照(此点已知多态,内联快照的命中率 < Refill 阈值)
;;   直接发 helper 调用 — 等价于解释器无 IC 形态
(br_if $err (call $h_gettable (local.get $base) (i32.const PC)))
```

**与 FBTableMono 的代码层差异**:

| | FBTableMono(§3.2.1) | FBTableMega(本节) |
|---|---|---|
| WAT 代码体积 | ~12 行(三层校验 + 直达槽 + helper else) | 1 行(只发 helper) |
| 命中时性能 | 三次比较 + 直达槽(快) | helper 调用 + 哈希查找(慢) |
| miss 时性能 | helper 调用 + 哈希查找(慢) | helper 调用 + 哈希查找(慢) |
| 适用形态 | 命中率高的单态点 | 多态点(命中率低,内联快照浪费 icache) |

**「别投机」标识对 P3 是「别内联快路径,直接走通用」**——与 P4 「不发投机模板,只发通用模板」是同一原则在不同发射后端的体现:

| | **P3 接 FBTableMega** | **P4 接 FBTableMega** |
|---|---|---|
| 行为 | 不内联 IC 快照,只发 helper 调用 | 不发投机直达槽模板,发通用查哈希模板 |
| 物理形态 | helper 调用(imported 函数,跨 Wasm/Go 边界) | 内联哈希查找代码(Lua → 原生码,无跨层) |
| 收益放弃 | 放弃 IC 快路径加速 | 放弃投机直达槽加速 |
| 共同点 | 都是「这点别走快路径,老老实实走通用」 | 同 |

### 3.4 FBUnstable

**触发条件**:[../p2-bridge/02-ic-feedback](../p2-bridge/02-ic-feedback.md) §4.3 三种来源:① IC 未观测(slot.kind=0);② 算术比例不达标(<0.99);③ 算术样本量不足(<100)。

**P3 翻译形态**:**等同 FBTableMega**——发通用翻译,不内联快路径快照(因为没有可信的快照可固化):

```wat
;; emit_gettable(pc, A, B, C, fb=FBUnstable):
;;   未观测/不稳定 ⇒ 无快照可固化 ⇒ 等价于「无 IC 提示」
(br_if $err (call $h_gettable (local.get $base) (i32.const PC)))

;; emit_add(pc, A, B, C, fb=FBUnstable):
;;   两选一(P3 实装可选):
;;   (a) 仍内联 IsNumber×2 快路径(此 PC 即便 IC 未观测,仍可能是 number 操作)
;;        → 与 FBArithStableNumber 形态相同,只是 branch predictor 没那么有信心
;;   (b) 直接发 helper(更激进的代码体积优化)
;;        → 失去任何 f64 内联机会,但 helper 路径仍正确
;;   PW3 基线推荐 (a)(简单 + 一致 + 仍快)
```

**与 FBTableMega 的细微差异**:FBTableMega 是「明确多态」,FBUnstable 是「未知 / 不可信」——语义上不同但 P3 的处理一致(都退化为通用翻译)。**P4 处理不同**:FBTableMega P4 也不投机,FBUnstable P4 可能少量投机(若 P4 阈值更宽容)。P3 不区分二者,统一走通用。

### 3.5 nil feedback(P3 容忍)

**触发条件**:[../p2-bridge/02-ic-feedback](../p2-bridge/02-ic-feedback.md) §4.5 「P2 初版只聚合一次」可能让 P3 拿到 nil feedback——首次升层时聚合 + installFeedback,后续若 Proto 重新升层(理论上 P2 状态机不允许这个,但 P3 实装时可能遇到 P3Compiler.Compile 被调用时 fb=nil 的极端场景)P3 拿到的 fb 可能是 nil。

[../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md) §2.1 P3Compiler.Compile 接口契约里明确:

> feedback:类型反馈快照……可能为 nil(实现方必须容忍 nil ⇒ 退化为「无 feedback 提示」编译,仍正确)。

**P3 翻译形态**:fb=nil 时**所有 IC opcode 退化为通用翻译**(等价 §3.3 FBTableMega + §3.4 FBUnstable):

```wat
;; emit_gettable(pc, A, B, C, fb=nil):
;;   (实装侧通常用 fb.Points[pc] 取该点 PointFeedback,fb=nil 时此调用应返回零值)
;;   零值 PointFeedback{Kind=FBUnstable, Confidence=0, ...} ⇒ §3.4 处理路径
(br_if $err (call $h_gettable (local.get $base) (i32.const PC)))
```

**实装上的简化**:翻译器入口可统一处理 nil:

```go
// internal/gibbous/wasm —— emit_gettable 入口
func (e *Emitter) emitGettable(pc int32, ins bytecode.Instruction, fb *bridge.TypeFeedback) {
    var pf bridge.PointFeedback // 零值 = FBUnstable
    if fb != nil && int(pc) < len(fb.Points) {
        pf = fb.Points[pc]
    }
    // 此后按 pf.Kind switch case
    switch pf.Kind {
    case bridge.FBTableMono:
        e.emitGettableMono(pc, ins, pf)
    case bridge.FBTableMega, bridge.FBUnstable:
        e.emitGettableGeneric(pc, ins)
    default: // 极端情况:Kind 是其它(理论不应发生在 GETTABLE 上),保守走 generic
        e.emitGettableGeneric(pc, ins)
    }
}
```

**关键性质**:**一行 `if fb != nil && int(pc) < len(fb.Points)` 守卫**就把 nil/越界全部归一到 FBUnstable 路径——P3 实装零特殊路径,nil 容忍是「免费的」。

### 3.6 六枚举值汇总表

| FeedbackKind | P3 翻译形态 | 失效降级路径 | 关键引用 |
|---|---|---|---|
| FBArithStableNumber | 双 number 快路径放 if-then,helper 放 else | else 分支 → `$h_arith` → metamethod / coercion | §3.1 |
| FBTableMono | 三层校验(IsTable + tableRef + gen)+ SNAP_KIND 单态选定的直达槽 | 任一层失败 → `$h_gettable` → 完整哈希 + `__index` | §3.2.1 |
| FBGlobalStable | 单层校验(globals gen)+ SNAP_INDEX 直达 globals node 槽 | gen miss → `$h_getglobal` → 完整哈希 | §3.2.2 |
| FBSelfMono | self 传递 + GETTABLE 同构(三层校验 + 直达) | 同 FBTableMono → `$h_self` → 完整 SELF 流程 | §3.2.3 |
| FBTableMega | 不内联快照,直接发 helper 调用 | n/a(本来就是慢路径) | §3.3 |
| FBUnstable | 等同 FBTableMega(表 IC)/ 仍内联快路径(算术 IC,可选) | 同 FBTableMega(表)/ §3.1 路径(算术) | §3.4 |
| nil feedback | 等价 FBUnstable | 同 FBUnstable | §3.5 |

**六枚举值 + nil 共七档情况均覆盖**——P3 翻译器对 PointFeedback 的所有可能输入都有定义。

## 4. feedback 与翻译器的接口

承 [02-translation](./02-translation.md) §6.5 的入口形态(每个 emit_<op> 函数从 fb.Points[pc] 读 PointFeedback),本节深化字段消费协议、并发安全策略、双源快照选取规则。

### 4.1 编译器 emit_<op> 函数从 fb.Points[pc] 读 PointFeedback

**接口形态**:`internal/gibbous/wasm` 包内 emit_<op> 函数族签名(承 [02-translation](./02-translation.md) §6 emitter 入口):

```go
// internal/gibbous/wasm —— emitter
package wasm

import (
    "github.com/Liam0205/wangshu/internal/bridge"
    "github.com/Liam0205/wangshu/internal/bytecode"
)

// Emitter 持单 Proto 编译期状态(WAT 输出 buffer + 立即数池等)。
type Emitter struct {
    proto *bytecode.Proto
    fb    *bridge.TypeFeedback // 可能为 nil(§3.5 nil 容忍)
    // ... 其它编译期状态
}

// emitOp 派发到具体 opcode 的 emit 函数。
// 入口处统一从 e.fb 取 PointFeedback,后续 emit 函数只读传入 pf 不重读 fb。
func (e *Emitter) emitOp(pc int32, ins bytecode.Instruction) error {
    pf := e.pointFeedbackOf(pc) // 零值 PointFeedback(Kind=FBUnstable)如果 fb=nil 或越界
    op := bytecode.Op(ins)
    switch op {
    case bytecode.ADD, bytecode.SUB, bytecode.MUL,
         bytecode.DIV, bytecode.MOD, bytecode.POW, bytecode.UNM:
        return e.emitArith(pc, ins, op, pf)
    case bytecode.LT, bytecode.LE:
        return e.emitCompare(pc, ins, op, pf)
    case bytecode.GETTABLE, bytecode.SETTABLE:
        return e.emitTableAccess(pc, ins, op, pf)
    case bytecode.GETGLOBAL, bytecode.SETGLOBAL:
        return e.emitGlobalAccess(pc, ins, op, pf)
    case bytecode.SELF:
        return e.emitSelf(pc, ins, pf)
    default:
        // 非 IC opcode:不读 pf
        return e.emitNonIC(pc, ins, op)
    }
}

// pointFeedbackOf 安全取 pf,fb=nil 或越界都返回零值(§3.5)。
func (e *Emitter) pointFeedbackOf(pc int32) bridge.PointFeedback {
    if e.fb == nil || int(pc) >= len(e.fb.Points) {
        return bridge.PointFeedback{} // Kind=FBUnstable, Confidence=0
    }
    return e.fb.Points[pc]
}
```

**关键设计**:

1. **emit_<op> 函数只读 pf,不直接访问 e.fb**——这把 nil 容忍逻辑收口在 `pointFeedbackOf`,后续 emit 函数零特殊路径。
2. **PointFeedback 是值类型不是指针**——按值传递不引入 nil 检查心智负担,零值即 FBUnstable。
3. **不同 opcode 族用不同 emit 函数**——按 op 类别分组(arith / compare / table / global / self / non-IC),每组内 switch fb.Kind 决定形态。

### 4.2 PointFeedback nil(非 IC 点)→ 跳过快路径分支

**「nil PointFeedback」的物理形态**:实际不是 Go 的 nil(PointFeedback 是值类型),而是**零值**——`{PC: 0, Kind: FBUnstable, Confidence: 0, StableShape: 0, StableIndex: 0, Observations: 0}`。这是 [../p2-bridge/02-ic-feedback](../p2-bridge/02-ic-feedback.md) §6.4 aggregator.Aggregate 的输出协议:

> 非 IC 指令:fb.Points[pc] 保持零值(Kind=FBUnstable, Confidence=0)— P3/P4 应跳过此 pc。

**P3 处理**:

```go
// emitArith 的 pf=零值(FBUnstable)路径
//   → §3.4 FBUnstable 处理(可选:仍内联 IsNumber×2 快路径,或直接发 helper)
// emitTableAccess 的 pf=零值
//   → §3.4 FBUnstable 处理(发通用翻译 = §3.3 FBTableMega 形态)
```

**注意**:零值 pf 与「未观测」(IC slot.kind=0)的 pf 是同一个:[../p2-bridge/02-ic-feedback](../p2-bridge/02-ic-feedback.md) §3.4 extractArithFeedback 在 slot.Kind=0 时返回 `PointFeedback{}` 零值。**P3 不区分**「这点是非 IC opcode」与「这点是 IC opcode 但未观测过」——两种情况下处理一致(走通用翻译)。

### 4.3 race-tolerant 读

承 [../p2-bridge/02-ic-feedback](../p2-bridge/02-ic-feedback.md) §5.4「P2 聚合时 P1 解释器仍在跑,聚合器对 IC slot 的读是非原子的 race-tolerant 读」,以及 P3 编译期同样要面对的并发场景:

**P3 编译期遇到的并发形态**:

| 场景 | 并发实体 | P3 读什么 | P3 怎么处理 |
|---|---|---|---|
| (1) P3 编译期 P1 解释器仍在跑 | P1(写 ICSlot) vs P3 编译器(读 ICSlot) | proto.IC[pc] 字段 | race-tolerant 读(§4.3.1) |
| (2) P3 编译期同时 P2 聚合器在跑 | P2 聚合器(读 ICSlot 写 fb) vs P3 编译器(读 fb) | fb.Points[pc] 字段 | 单写者 + 编译期读已稳定的 fb(§4.3.2) |
| (3) 多 State 并发触发 P3.Compile | 多个 P3 编译器实例并发 | 都是只读 ICSlot/fb | 无写竞争,只读安全 |

#### 4.3.1 P3 编译期对 ICSlot 的 race-tolerant 读

P3 编译期若**直接读 ICSlot**(不只通过 fb,§4.4 双源选取),需要承担与 P2 聚合器同样的 race-tolerant 读责任:

```go
// internal/gibbous/wasm —— 编译期直接读 ICSlot 的形态
func (e *Emitter) snapshotICSlot(pc int32) bytecode.ICSlot {
    slot := &e.proto.IC[pc]
    // race-tolerant 读:多 State 并发场景下 P1 仍在写 IC,
    // 读到「半新半旧」的 ICSlot 不爆炸(语义层面 IC 快照失效就走助手,§3.2.4)。
    return bytecode.ICSlot{
        Shape:    atomic.LoadUint32(&slot.Shape),
        Index:    atomic.LoadUint32(&slot.Index),
        TableRef: atomic.LoadUint32(&slot.TableRef),
        Kind:     slot.Kind, // uint8 字段;现代 ISA 上对齐字节读是原子的
    }
}
```

**race-tolerant 读不爆炸的论证**(承 [../p2-bridge/02-ic-feedback](../p2-bridge/02-ic-feedback.md) §5.4 同源):

1. **写半新半旧不爆炸**:32-bit 对齐写在 x86/arm64 上是原子的(不会撕裂),读到「shape=42 + index=8(新)」或「shape=42 + index=5(旧)」是合法瞬时态,但**SNAP_INDEX 用旧值不会出错**——校验失败走 helper,正确性由 helper 兜底。
2. **半新半旧的 SNAP 立即数仍可用**:即便编译期捕获到「shape=42(刚被 P1 写)+ tableRef=0xCDEF(已是旧值)」这种内部不一致的快照,固化到 WAT 里后,运行期校验只会更早失败(实际运行的表 tableRef 是新值,与快照里的旧 tableRef 比对 fail),helper 兜底,**正确性不受影响**。
3. **kind 字节读的对齐性**:ICSlot 结构体里 kind 是 uint8,但 Go 编译器会自然对齐;非 byte ISA 上单字节读也是原子的。

**结论:P3 编译期读 ICSlot 是 race-tolerant 安全的**——读到任何「半新半旧」组合,固化进 WAT 后运行期都能正确处理(校验失败走 helper)。

#### 4.3.2 P3 编译期对 fb.Points[pc] 的读

P3 编译期通过 P2 产出的 fb 间接读 IC 信息——按 [../p2-bridge/02-ic-feedback](../p2-bridge/02-ic-feedback.md) §4.5 / §5.5 协议:

```
ProfileData.Feedback 的生命期:
  考虑升层时机 T0:
    T0 - ε:   considerPromotion 入口 → aggregator.Aggregate(proto) 产 fb
    T0:       installFeedback(proto, fb)  ← CAS 安装(P2 §5.5)
    T0 + ε:   P3.Compile(proto, fb) 启动 ← 此时 fb 已稳定,只读
    T0 ... 永远: ProfileData.Feedback 不再变(P2 初版只聚合一次,§4.5)
```

P3 编译期接到的 fb 是**已稳定的只读快照**——P2 不再写,P3 是唯一读者,**无并发竞争**。这是 P2 在 §4.5 / §5.5 给 P3 的「快照不可变」承诺,P3 实装时可放心读 fb 字段不加任何 atomic / lock。

**实装上的简化**:`func (e *Emitter) pointFeedbackOf(pc int32) bridge.PointFeedback` 是普通值返回,不需要 atomic load——这与 §4.3.1 直接读 ICSlot 的 atomic load 形成对照:**fb 是 P2 的「凝固快照」,ICSlot 是 P1 的「活体观测」,二者并发安全成本不同**。

### 4.4 IC 快照与 feedback 双源选取策略

P3 编译期同一份「快照」实际上有两个数据源:

| 数据源 | 时间戳 | 字段 |
|---|---|---|
| (A) **PointFeedback** (P2 聚合产出,fb.Points[pc]) | T0 - ε(P2 聚合时刻) | StableShape, StableIndex, Kind |
| (B) **ICSlot 直接读** (proto.IC[pc] 编译时刻) | T0 + ε(P3 编译时刻) | Shape, Index, TableRef, Kind |

二者在时间戳上略有差异(可能差几次循环迭代),内容**不强求一致**——都是统计性的快照。P3 选哪一个?

#### 4.4.1 选取定稿:编译时刻 ICSlot 直接读

**P3 PW5 基线选 (B) ICSlot 直接读**——更新(更接近编译瞬间的真实状态)。

**理由**:

1. **(A) 更旧**:T0-ε 是聚合时刻,T0+ε 是编译时刻——P2 聚合在 considerPromotion 入口立即做([../p2-bridge/04-try-compile-fallback](../p2-bridge/04-try-compile-fallback.md) §3),然后调 P3.Compile 才进入 P3 编译。两者间隔很短,但 (B) 总不晚于 (A)。
2. **(A) 缺少 tableRef**:[../p2-bridge/02-ic-feedback](../p2-bridge/02-ic-feedback.md) §4.2 PointFeedback 字段定义里没有 tableRef(只 stableShape + stableIndex)——P2 聚合器没把 tableRef 编入 fb(为节省 fb 体积)。但 §3.2.1 GETTABLE 翻译需要 SNAP_TABLEREF(校验同表),只能从 (B) ICSlot 读。
3. **(A) 缺少 SNAP_KIND 的细粒度**:fb.Kind 只区分 FBTableMono / FBTableMega 等粗粒度,而 §3.2.1 GETTABLE 的 SNAP_KIND 需要 ICSlot.Kind 的 1=array / 2=node / 3=mono-meta 三档细粒度——只能从 (B) 读。
4. **(A) 仍有用**:fb.Kind 决定**走哪条翻译路径**(emitGettableMono vs emitGettableGeneric);(B) 的 ICSlot 字段填**SNAP_* 立即数**。两者职责互补,不矛盾。

**实装形态**:

```go
// internal/gibbous/wasm —— emitGettableMono(承 §3.2.1)
func (e *Emitter) emitGettableMono(pc int32, ins bytecode.Instruction, pf bridge.PointFeedback) {
    // pf.Kind 已经是 FBTableMono/FBSelfMono(由 emitTableAccess 入口 switch 决定)
    // 此处只需要 SNAP_* 立即数 — 从 ICSlot 直接读(更新 + 含 tableRef + 含细粒度 kind)
    slot := e.snapshotICSlot(pc) // §4.3.1 race-tolerant 读
    if slot.Kind == 0 {
        // 极端场景:pf 说 Mono 但 ICSlot 已被重置(P1 解释器把 slot 写空过)
        // 退化为通用翻译,正确性兜底
        e.emitGettableGeneric(pc, ins)
        return
    }
    // 正常路径:固化 ICSlot 字段为 SNAP_* 立即数
    e.emitWAT_TableMono(pc, ins, slot.TableRef, slot.Shape, slot.Kind, slot.Index)
}
```

**关键守卫**:**`if slot.Kind == 0 { 退化为通用翻译 }`** ——即便 fb 说 Mono 但 ICSlot 被重置,P3 仍正确(走通用翻译)。这是双源不一致时的兜底:**正确性不依赖任何一份快照的特定状态**。

#### 4.4.2 双源不一致的实际场景

考虑这个时序:

```
T0:  P2 聚合时刻,ICSlot = {kind=2 nodeHit, shape=42, tableRef=0xABCD, index=5}
     fb.Points[pc] = {Kind=FBTableMono, StableShape=42, StableIndex=5, ...}
T0+1: 同 Proto 在另一 State 上跑,IC miss(换表)→ ICSlot 被重写为
     {kind=2, shape=99, tableRef=0xEF00, index=12}
T0+2: P3.Compile 开始,ICSlot 已是新值
     emit_gettable 读 fb.Points[pc].Kind = FBTableMono → 走 emitGettableMono
     emit_gettable 读 ICSlot → SNAP_GEN=99, SNAP_TABLEREF=0xEF00, SNAP_INDEX=12
     固化进 WAT
T0+3: 该 Proto 跑 gibbous 代码,运行期表 t 现在的 gen=99(同 T0+1 的状态)
     校验通过 ⇒ 直达槽
```

**结论**:fb 与 ICSlot 不一致(fb 是 T0 快照,ICSlot 是 T0+1 之后)时,P3 选 ICSlot(更新)是合理的——固化的 SNAP 反映 T0+2 编译时刻的真实表状态,运行期(T0+3)校验更可能命中。

**反例反证**:若 P3 选 (A) PointFeedback(更旧),固化的 SNAP_GEN=42 在 T0+3 运行期校验时(t.gen()=99)直接 fail——立刻走 helper,**正确但更慢**。性能上 (B) 更优。

#### 4.4.3 双源选取留 PW5 实测优化

§4.4.1 的「选 (B) ICSlot 直接读」是 PW5 基线推荐——但实测可能发现:

- 某些负载下 (A) 与 (B) 差异不大(fb 与 ICSlot 间隔短,内容一致),(B) 的 atomic load 反而是 PW5 编译期开销;
- 或某些负载下 (B) 反而更不稳定(编译时刻刚好 P1 写 IC 写到一半,读到「半新半旧」的快照),不如 (A) 平均化的 P2 聚合产物稳。

**留 PW5 实测后定稿**——这是本文的开放设计点之一,详见 §6 缺口节。

---

## 5. 不变式清单

P3 IC feedback 消费的实现期硬性约束,违反即设计失败:

1. **P3 不依赖 feedback 正确性**(零 deopt)。承 [../p2-bridge/04-try-compile-fallback](../p2-bridge/04-try-compile-fallback.md) §1.3 + [../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md) §1.2 + 本文 §1.5。物理表现:即便 fb 完全错(全标 FBArithStableNumber 但实际全是 string),P3 翻译产物运行起来仍正确(只是性能不优)。**这是 P3 与 P4 的根本分野**——P4 投机错会触发 deopt 风暴,P3 投机错……P3 没投机所以无所谓「投机错」。

2. **快路径检查 = 语义分发,不是投机 guard**。承 §1。物理表现:每个 IC 翻译形态的 if-else 中,**else 分支永远存在且永远调 helper**——不省略 else,不省略 helper。如果某条 emit_<op> 函数有「fb=stable 时省略 else 分支」的优化,这条优化就是投机化,违反不变式。

3. **IC 快照编译期固化,失效降级到 helper(慢但正确)**。承 §2。物理表现:gibbous 代码不重读 ICSlot,固化的 SNAP_* 立即数失效后(校验 fail)直接 helper,**不触发重编译 / 不切走 IC slot 副本 / 不走 deopt**。「正确但慢」是定式,helper 调用是稳态形态(不是异常)。

4. **P3 不读 confidence 字段**(那是 P4 的事)。承 §3.1 + [../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md) §1.4。物理表现:emit_<op> 函数只 switch `pf.Kind`,不读 `pf.Confidence`。confidence 是 P4 用来「投机激进度旋钮」的字段,P3 不投机所以不需要。**emit_<op> 函数签名层面甚至可以只接收 Kind,不接收 PointFeedback 整体**(实装上接收整体是为了未来扩展空间,但当前不读 confidence 字段)。

5. **nil feedback 容忍:退化为通用翻译**。承 §3.5 + [../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md) §2.1 P3Compiler.Compile 接口契约。物理表现:`pointFeedbackOf` 守卫将 nil/越界归一到零值 PointFeedback(Kind=FBUnstable),后续 emit_<op> 走通用翻译路径——零特殊处理逻辑。

6. **race-tolerant 读不爆炸**。承 §4.3。物理表现:P3 编译期对 ICSlot 的读用 atomic.LoadUint32(为兼容 -race 标记)或裸读(性能最优),读到「半新半旧」组合时固化的 SNAP_* 立即数仍可用——校验失败走 helper,正确性兜底。

7. **双源选取:fb 决定路径,ICSlot 填立即数**。承 §4.4。物理表现:`fb.Points[pc].Kind` 决定 emit_<op> 走 Mono / Mega / Generic 哪条翻译路径;ICSlot 字段填 SNAP_TABLEREF / SNAP_GEN / SNAP_KIND / SNAP_INDEX 立即数。两者职责互补不冲突,即便不一致时 P3 兜底退化(`if slot.Kind == 0 → emitGettableGeneric`)。

8. **六枚举值 + nil 全覆盖**。承 §3.6。物理表现:P3 翻译器对 PointFeedback 的所有可能输入(FBUnstable / FBArithStableNumber / FBTableMono / FBTableMega / FBGlobalStable / FBSelfMono + nil)都有对应翻译形态——**switch case 不留 default panic**,通用翻译是 fallback。

9. **解释器同款判定证据**。承 §1.2 / §1.3。物理表现:P3 IC 翻译形态里的 IsNumber×2 / 同表同代次 / globals gen 校验,与 [../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md) §4.1 / §6.3 / §6.4 解释器原版判定**逐字节同构**(Wasm 指令是 Go 代码的直译)。差分 fuzz([08-testing-strategy](./08-testing-strategy.md))保证两层 byte-equal 输出。

10. **失效后无重编译机制**。承 §2.4 / §2.5。物理表现:P3 翻译产物对「失效后的 helper 调用频率」**不计数 / 不监控 / 不触发任何动作**——helper 调用就是合法运行形态。失效计数 → 重编译协议留 P4 一并评估([../p4-method-jit](../p4-method-jit.md) §3.4)。

---

## 6. 文档缺口 / 待决

承 [00-overview](./00-overview.md) §10 风险与未决缺口,本文涉及的开放设计点:

### 6.1 IC 快照失效后是否重编译

- **现状**:P3 PW5 基线选择「失效后永久走 helper(等同解释器无 IC)」,不触发重编译。
- **挂起原因**:重编译协议是 deopt 基建的一部分(失效计数器 + 重编译预算 + 旧码 disposal),P4 因投机失败 deopt 必然要建一套,P3 单独建不摊薄成本(本文 §2.5)。
- **解决路径**:链 P4 §3.4「再训练机制」一并评估。P4 落地时统一处理「gibbous 代码片段过期」的两个来源(P3 IC 失效永久 miss + P4 投机 guard 反复失败)——同一套重编译触发器与状态机。
- **影响范围**:P3 阶段的性能上限由「失效后退化到无 IC 解释器水平」框定;若负载形态对 IC 命中率敏感(如频繁 rehash 的工作集),性能可能不达 ≥2x 验收门([08-testing-strategy](./08-testing-strategy.md));若实测达不到,可能提前到 P4 评估时把 IC 失效重编译纳入。
- **登记位置**:本文 §2.5 + [00-overview](./00-overview.md) §10 + [doc-gaps](../../../llmdoc/memory/doc-gaps.md)。

### 6.2 IC 快照固化的两份快照(feedback + ICSlot)选取策略

- **现状**:本文 §4.4 PW5 基线选择「fb 决定路径,ICSlot 直接读填立即数」。
- **挂起原因**:实测前无法判断 fb vs ICSlot 哪份更稳定 / 更准确——理论上 ICSlot 更新但有 race 风险,fb 更稳定但更旧且字段更少(不含 tableRef)。
- **解决路径**:PW5 实装时同时支持两种(或混合策略),用 [08-testing-strategy](./08-testing-strategy.md) 性能基准对比 byte-equal + 加速倍率。
- **影响范围**:仅影响 IC 命中率 / 编译期开销,不影响正确性(都是 race-tolerant 安全的)。
- **登记位置**:本文 §4.4.3 + [00-overview](./00-overview.md) §10。

### 6.3 比较 LT/LE 的 number vs string 子分流粒度损失

- **现状**:[../p2-bridge/02-ic-feedback](../p2-bridge/02-ic-feedback.md) §9.2 已记此缺口——LT/LE 的 numHits 计数不区分「双 number 快路径」与「双 string 快路径」,P3/P4 拿到的 FBArithStableNumber 在 LT/LE 上是「快路径稳定」(可能 number 也可能 string)。
- **P3 影响**:P3 emitCompare 函数若按 FBArithStableNumber 内联 `f64.lt` 等指令(假设双 number),运行期来双 string 时**走 else 分支 helper**——helper 内部走 [../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md) §4.4 完整 LT/LE(含 string 比较),正确返回。**性能损失**(string 比较走 helper 而非内联),**正确性 100% 兜底**(本文 §1.5 同源)。
- **解决路径**:留 P2+ 实测后补——若 LT/LE 的 string 比较占比高,P1 比较 IC 写入加分流字段(用 tableRef 字段挪用一个「快路径子分支编号」),P2 提取为 `FBArithStableString` 新枚举值,P3 emitCompare 据此发对应内联快路径。
- **登记位置**:本文 §6.3 + [../p2-bridge/02-ic-feedback](../p2-bridge/02-ic-feedback.md) §9.2 共享。

### 6.4 megamorphic 主动识别在 P3 翻译形态的接入点

- **现状**:P2+ #4 已落地 megamorphic 主动识别(`internal/bridge/aggregator.go` MegamorphicRefillThreshold=3,Refill 计数超阈值即标 FBTableMega)——P2 端已就绪。
- **P3 接入**:本文 §3.3 已给 FBTableMega 翻译形态(直接发 helper 调用,不内联 IC 快照)——**无需额外接入工作**,emitTableAccess 入口 switch fb.Kind = FBTableMega 即对应路径。
- **待优化**:P3 实装时可否「在 emitTableAccess 入口前先看 ICSlot.Refill 也判断 mega」(避免依赖 fb 的滞后)?**留 PW5 实测决定**——若 fb 滞后明显且实际负载 mega 比例高,P3 可加这条短路;否则保持「fb 唯一信号源」的简洁。
- **登记位置**:本文 §6.4 + [../p2-bridge/02-ic-feedback](../p2-bridge/02-ic-feedback.md) §9.3。

### 6.5 算术 IC 在 ADD/MUL 之外的子分流

- **现状**:P1 算术 IC 双计数对 ADD/SUB/MUL/DIV/MOD/POW/UNM/LT/LE 一视同仁([../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md) §4.1),P2 聚合产 FBArithStableNumber 也不区分具体算术 op。
- **P3 影响**:emitArith 对 ADD/MUL 等都按 FBArithStableNumber 内联 `f64.add`/`f64.mul` 快路径——运行期来双 number 命中,等同解释器快路径加速 + 省 dispatch。**这一档没有粒度损失**(算术 op 之间的快路径形态相同,仅 f64 指令不同)。
- **不视为缺口**,记此为「显式确认无缺口」一条。

### 6.6 P3 emit_<op> 是否也消费 confidence 字段

- **现状**:本文 §5 不变式 4 + §3.1 明确 P3 不读 confidence。
- **挂起原因**:理论上 P3 也可用 confidence 做更精细的代码布局决策(如 confidence ∈ [0.99, 1.0] 时把快路径优化得更激进)——但收益小且复杂度高。
- **解决路径**:PW9 性能调优时若发现「FBArithStableNumber 但 confidence 低」与「confidence 高」性能差异显著,可考虑 P3 也加 confidence 阈值。**当前定稿:不读**。
- **登记位置**:本文 §6.6,优先级低。

---

## 7. 相关

- [00-overview](./00-overview.md)(P3 总览,§3 关键耦合点 5「IC 快照编译期固化」+ §9 不变式 1 语义分发非投机)
- [02-translation](./02-translation.md)(P3 翻译器,§6.5 emitter 入口接 fb;本文是其 IC 翻译形态的细化扩展)
- [04-trampoline](./04-trampoline.md)(慢路径 imported 助手回 Go,本文 IC miss 降级路径接入点 `$h_arith`/`$h_gettable`/`$h_getglobal`/`$h_self`)
- [05-safepoint-gc](./05-safepoint-gc.md)(分配点 safepoint,本文 helper 内调 alloc 时 GC 触发的协议)
- [08-testing-strategy](./08-testing-strategy.md)(crescent vs gibbous 逐字节差分,本文 IC 翻译形态正确性的验收门)
- [../p2-bridge/02-ic-feedback](../p2-bridge/02-ic-feedback.md)(TypeFeedback shape 完整定义,本文是消费侧)
- [../p2-bridge/04-try-compile-fallback](../p2-bridge/04-try-compile-fallback.md) §1(fallback ≠ deopt)+ §1.3(P2/P3 静态保证 vs P4 投机)
- [../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md) §1(P3/P4 不对称消费 feedback;§1.4 P3 不读 confidence)
- [../p1-interpreter/02-bytecode-isa](../p1-interpreter/02-bytecode-isa.md) §7(ICSlot 结构;算术 IC 双计数挪用)
- [../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md) §6(IC 执行机制 — P3 翻译时与之同构)
- [../p4-method-jit](../p4-method-jit.md) §2.1(P4 投机模板,本文 §3 各 FeedbackKind 形态的对偶面)+ §3.4(P4 再训练机制,本文 §2.5 失效重编译归属)
- ../p3-wasm-tier(P3 单文件原稿;本文是其 §3 + §10 不变式 1 的详细设计扩展)
- [../../../llmdoc/must/design-premises](../../../llmdoc/must/design-premises.md)(原则 1 解释器永不退役;原则 2 投机错误静默错果是 JIT 最危险 bug 类别 — P3 通过零投机消除)


