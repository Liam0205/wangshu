# P2-02 IC 反馈聚合子系统:从 P1 旁路写到 P3/P4 投机供料

> 状态:**详细设计**。本文是 [../p2-bridge/00-overview §3](./00-overview.md) 的深度展开,所有跨文档接口以本文为准。
> 单一事实源覆盖:P1 IC 写入复用契约、算术 IC 双计数挪用规约(P1 IC 字段共享方案)、`TypeFeedback` shape 与 `confidence` 计算、megamorphic 标记的 P2 落地。
> 上游:[00-overview](./00-overview.md) §1(P1/P2/P3 边界:IC 写读分离)/§3(算术 IC 双计数关键耦合点)/§7(P1 前瞻义务对账,IC 双计数 ✅)。
> P1 依赖面:[02-bytecode-isa §7](../p1-interpreter/02-bytecode-isa.md)(`ICSlot` 结构 + 算术 IC 字段挪用登记)、[05-interpreter-loop §6](../p1-interpreter/05-interpreter-loop.md)(IC 执行机制定稿)、**[05-interpreter-loop §6.4](../p1-interpreter/05-interpreter-loop.md)(算术 IC「P1 写不读纯供料」是本文的核心契约)**、[01-value-object-model §3](../p1-interpreter/01-value-object-model.md)(NaN-box 数字识别)、[implementation-progress](../p1-interpreter/implementation-progress.md)(P1 已落地形态对账)。
> 下游:[03-compilability-analysis](./03-compilability-analysis.md)(可编译性闸门,与 feedback 正交)、[04-try-compile-fallback](./04-try-compile-fallback.md)(状态机消费 feedback 决定何时升)、[05-p3-p4-interface](./05-p3-p4-interface.md)(`P3Compiler.feedback` 可选 / `P4Feedback` 核心)、[../p4-method-jit](../p4-method-jit/00-overview.md)(投机消费方,confidence 决定激进度)。

---

## 0. 定位:三阶段供料链 —— P1 写、P2 读、P3/P4 消费

P2 IC 反馈子系统的存在理由,只用一句话表述:**把 P1 解释器执行期"顺手写下"的散落 IC 观测,聚合成 P3/P4 可消费的结构化类型 feedback,自己不消费**。这一句对应三个分工:

| 阶段 | 角色 | 落地证据 |
|---|---|---|
| **P1 crescent**(已落地) | 在每个 IC 点**写入观测**:GETTABLE/GETGLOBAL 等命中时刷 `ICSlot.{kind,index,shape,tableRef}`(05 §6.3);算术指令快路径 `numHits++` / 元方法慢路径 `metaHits++`(05 §6.4 双计数挪用) | M10 IC 接入:[implementation-progress](../p1-interpreter/implementation-progress.md) 表 IC 命中路径行 |
| **P2 bridge**(本文) | **只读不写**:遍历 Proto 的 IC slot 数组(按 pc 索引),把 `kind` + 双计数比例聚合成按 pc 索引的 `PointFeedback`,挂 Proto 旁 `ProfileData.feedback`,供下游取 | M0-PB2(本文设计阶段) |
| **P3 gibbous**(P2+) | **可选消费**:稳定点的紧凑 Wasm 翻译(锦上添花,不依赖正确性)。详见 [05-p3-p4-interface](./05-p3-p4-interface.md) §2 | P3 阶段 |
| **P4 fullmoon-method**(P4+) | **核心消费**:类型投机的输入,`confidence` 决定激进度,`FBArithStableNumber` 发 f64 快路径 + guard,`FBTableMono` 投机直达槽,`FBTableMega` 老实查哈希 | P4 阶段 |

> 这与 [00-overview](./00-overview.md) §2 总数据流的"算术 IC 写不读"一脉相承:**信息在每一层被生产,在下一层被消费**。P1 写 IC 时不知道 P2 会怎么用,只是忠实记录;P2 读 IC 写 feedback 时不知道 P4 会用 confidence 挑哪些点投机,只是忠实聚合。每层只做自己那一棒,跨层零反向耦合 —— 这是 [roadmap.md §3](../roadmap.md) "编译层是纯增量"在反馈维度的物理兑现。

**为什么 P2 不消费 feedback**(§7 详):因为 P2 不在执行热路径上([00-overview](./00-overview.md) §1 边界表),不发射代码、不跑 Proto;消费 feedback 的人是发射代码的人(P3/P4),不是产料的人(P2)。让 P2 消费 feedback 等于让 `internal/bridge` 出现"跑 Proto"逻辑,直接判否([00-overview](./00-overview.md) §1 末尾铁律)。

---

## 1. P1 已落地的 IC 写入概览(承 02 §7 / 05 §6)

P2 启动前置条件是 P1 已经把 IC 写满,本节对账 P1 实际落地的写入形态,P2 直接读取——P1 端无需任何新开发。

### 1.1 ICSlot 字段全表(P1 已落地形态)

来源:[02-bytecode-isa §7](../p1-interpreter/02-bytecode-isa.md) 与 [05-interpreter-loop §6.2](../p1-interpreter/05-interpreter-loop.md) 联合定稿,M10 IC 接入轮已实装。

```go
// internal/bytecode —— P1 已落地形态
type ICSlot struct {
    shape    uint32 // 表 IC:目标表的 gen 代次 / 算术 IC:numHits 双计数低 32 位(挪用)
    index    uint32 // 表 IC:命中槽位下标 / 算术 IC:metaHits 双计数低 32 位(挪用)
    tableRef uint32 // 表 IC:目标表 arena 偏移低 32 位身份比对(05 §6.2,非 GC 根)
                    // 算术 IC:留空(05 §6.4 双计数挪用占 shape/index 即够,本字段恒 0)
    kind     uint8  // 0 未初始化 / 1 array hit / 2 node hit / 3 mono-metamethod / 4 megamorphic
}
```

字段语义按 IC 点类型分流——这是「同一结构、按 kind 区分字段语义」的核心([02 §7](../p1-interpreter/02-bytecode-isa.md) 定稿表述,不增 ICSlot 尺寸)。具体的字段挪用规约见 §3。

### 1.2 P1 写入观测点对照表(P2 读什么的反查)

| 指令族 | P1 写入位置 | 写入字段 | 写入条件 | 文档 |
|---|---|---|---|---|
| GETTABLE / GETTABLE_K | 05 §6.3 `doGetTable`/`icGetTable` | `kind`={1,2,4} + `shape`/`index`/`tableRef` | 表查找命中(array/node)或多次换表降级 mega | 05 §6.3 |
| SETTABLE / SETTABLE_K | 同 doGetTable 对称的 doSetTable | 同上 | 写命中已存在键(改值不动 gen) | 05 §6.5 |
| GETGLOBAL | 05 §6.4 GETGLOBAL 段 | `kind`=2(globals 恒 node hit)+ `shape`(globals gen)+ `index` | globals 表查找命中 | 05 §6.4 |
| SETGLOBAL | 同上对称 | 同上 | 已存在全局键(新增键触发 globals rehash → bump gen → IC 失效) | 05 §6.4 |
| SELF(`obj:m()`) | 05 §6.4 SELF 段 | 与 GETTABLE 同构 | 方法常驻 metatable → 命中率极高 | 05 §6.4 |
| ADD / SUB / MUL / DIV / MOD / POW | 05 §4.1 `doArith` 快路径末 + §4.2 慢路径前 | **`shape`(numHits)** / **`index`(metaHits)** 挪用计数 | 快路径双 number 命中 → numHits++;走元方法 → metaHits++ | 05 §4.1 / §6.4 |
| UNM(一元负) | 同算术(05 §4.3 与 doArith 同模式) | 同算术(双计数挪用) | 单操作数 number → numHits;`__unm` 元方法 → metaHits | 05 §4.3 |
| 比较 LT / LE | 05 §4.4 比较快路径 | 算术 IC 同源(双计数挪用) | 双 number 或双 string 直比 → numHits;`__lt`/`__le` → metaHits | 05 §4.4 |

> 注 1:**EQ 不带 IC**——EQ 的快路径 `rawequal` 不存在「类型分布投机机会」(NaN-box 单比较即出结果,无 metamethod 是 P1 简化)。
>
> 注 2:**LEN/CONCAT 不带 IC**——LEN 的字符串/表/userdata 三分支用 NaN-box tag 直接拣选,无 IC 价值;CONCAT 全 string/number 走线性快路径,混合操作数走两两折叠,IC 无信息可记。
>
> 注 3:**TFORLOOP / FORLOOP / FORPREP 不带 IC**——它们是控制流而非取值/运算指令,但 FORLOOP 回边是**热度采样点**而非 IC 点(详见 [01-profiling](./01-profiling.md) §2),与本文 IC 反馈是正交维度。

### 1.3 P1 写入快路径绝对不动(本文核心契约)

[05-interpreter-loop §6.4](../p1-interpreter/05-interpreter-loop.md) 的原话:

> P1 写它、不读它分支……纯为 P2 类型 feedback 与 P4 f64 投机供料。

这里"P1 写不读"有两层含义:

1. **写不读分支**:P1 的算术快路径用 `value.IsNumber(b) && value.IsNumber(c)` 现场判定(05 §4.1),不读 IC slot 决定走哪条路。算术 IC 是**纯旁路写**,被改成什么样都不影响 P1 的取值正确性。
2. **P2 不写**:P2 是只读消费方(§0),不刷新 IC slot、不重置计数、不改 kind。这条铁律保证 P1 ↔ P2 的 IC 写入路径**单向**,P2 上线零回归 P1。

这两条联合给出了 P2 实现的最严苛约束:**P2 的聚合器必须容忍 IC slot 被并发写**(P1 解释器仍在跑),读取时不加锁(`atomic.LoadUint32` 或非原子 race-tolerant 读)。读"脏"了顶多让某次 confidence 计算偏一点,不影响后续聚合的最终收敛——这是「反馈聚合是统计性的、不要求强一致」的物理体现。详见 §5.4 并发读策略。

---

## 2. 各类 IC 点的 feedback 提取(详细对照表)

承 [../p2-bridge/00-overview §3.2](./00-overview.md) 起手表,本节给字段级提取规则。**P2 聚合器的输入是 P1 写好的 ICSlot 数组,输出是按 pc 索引的 `PointFeedback`** —— 提取逻辑就是「ICSlot.kind / 双计数比例 → FeedbackKind + confidence」的纯函数映射。

### 2.1 表 IC(GETTABLE / SETTABLE / SELF)

| ICSlot.kind | P2 提取的 FeedbackKind | confidence 来源 | stableShape / stableIndex |
|---|---|---|---|
| 0 未初始化 | 跳过(此点未被执行过 → 无 feedback) | n/a | — |
| 1 array hit | `FBTableMono` | 1.0(单态命中即稳定,无降级历史) | shape=ICSlot.shape, index=ICSlot.index |
| 2 node hit | `FBTableMono` | 1.0 | 同上 |
| 3 mono-metamethod | `FBTableMono`(以 `__index`/`__newindex` 元方法为单态目标) | 1.0 | shape=ICSlot.shape(metatable gen);index 为元方法槽位 |
| 4 megamorphic | `FBTableMega` | n/a(megamorphic 本身就是「别投机」标记) | 不填(P4 应忽略) |

**关键点**:表 IC 的 `kind` 字段已经把「单态/多态」二分清楚了——这是 [05-interpreter-loop §6.3](../p1-interpreter/05-interpreter-loop.md) 末尾「polymorphic 处理」的设计意图:P1 的 mono IC 退化为「换表即重填」,但 P2 标记 `kind=4 megamorphic` 给下游"此点别再投机"。P2 不需要重新计算多态比例,只需把 `kind=4` 翻译成 `FBTableMega`,把 `kind∈{1,2,3}` 翻译成 `FBTableMono`。

> 注:P1 当前的 mono IC 不主动从 `kind∈{1,2}` 升级到 `kind=4`(频繁换表的点会反复重填,kind 仍是 1 或 2)——05 §6.3 原话「P1 可不实现降级,留给 P2 标记『此点多态、别再投机』」。**P2 是否在聚合时识别"反复重填"模式并主动标 `FBTableMega`** 是一个开放设计点,详见 §6。

#### 2.1.1 端到端示例:GETTABLE 的 P1 写到 P2 读

考虑下面的 Lua 片段:

```lua
local t = {a=1, b=2, c=3}
for i=1,1000 do
  local x = t.a    -- 一个 GETTABLE 指令,常驻同表同键
end
```

P1 解释器执行期(M10 IC 实装后):

1. **第 1 次循环**(`i=1`):GETTABLE 走 cold path,完整 hash 查找,把 `t.a` 的 node 槽位下标写进 `ICSlot.index`,`shape=t.gen()`,`tableRef=arenaOff(t)`,`kind=2 (node hit)`。
2. **第 2 ~ 1000 次**:GETTABLE 走 IC 命中(同表 + 同代次双校验通过),直达 node 槽,跳过 hash。**ICSlot 不再被写**(命中无需更新)。
3. P1 不在循环内写 P2 任何反馈对象——仅 ICSlot 这一处状态自然驻留。

P2 在 considerPromotion 触发时(假设 1000 次回边累积满阈值,01-profiling §5):

1. 调用 `aggregator.aggregate(proto)`,遍历 Proto.Code,在 GETTABLE 指令处 `extractTableFeedback(pc, slot, opGetTable)`。
2. 读到 `slot.kind=2`,产出:
   ```
   PointFeedback{
     pc:           42,                        // 假设 GETTABLE 在 pc=42
     kind:         FBTableMono,
     confidence:   1.0,
     stableShape:  <t.gen() 当时值>,
     stableIndex:  <t["a"] 的 node 槽位下标>,
     observations: 1,                         // §5.1 表 IC 占位 1
   }
   ```
3. 把整份 TypeFeedback 经 `installFeedback` CAS 安装到 `proto.ProfileData.feedback`。
4. P3 接到这份 feedback 拿 stableIndex 直接发 Wasm 紧凑翻译;P4 接到后发 guard「t.gen()==stableShape」+ 直接 node 索引。

**性质 1:P2 聚合无副作用**——读完 ICSlot 写一份新 TypeFeedback,P1 解释器仍在跑同一份 ICSlot 不受影响。

**性质 2:聚合是统计快照**——若聚合发生在第 500 次循环时,而循环之后 t 改了形状(如 `t.d = 4` 触发 rehash → t.gen() 自增 → ICSlot 失效),P2 看到的是 **old gen**;P3/P4 用 old gen 的 stableShape 投机,运行期 guard 失败时会落 deopt(P4)或重走 hash(P3 通用翻译)。**正确性兜底由下游负责**,P2 只负责忠实快照。

### 2.2 全局 IC(GETGLOBAL / SETGLOBAL)

| ICSlot.kind | P2 提取的 FeedbackKind | 备注 |
|---|---|---|
| 0 未初始化 | 跳过 | 该点未被执行过 |
| 2 node hit | `FBGlobalStable`(globals 恒为 node hit,因 globals 表无数组段或全局键不走 array) | confidence=1.0;stableIndex=ICSlot.index 即 globals 节点槽 |
| 4 megamorphic | (理论上 globals 不会 megamorphic) | globals 是单一表,无换表问题;kind=4 在 globals IC 上不应出现 |

> **globals 不会 megamorphic 的直觉**:GETTABLE 的 megamorphic 来源是「同一指令多次访问不同表」。GETGLOBAL 的目标表恒为 globals(05 §6.4「目标表恒为 globals,tableRef 恒等」),不存在「换表」语义,kind 只在 {0, 2} 之间;实现侧 P1 不会写 kind=4 给 globals IC。**P2 聚合器对 GETGLOBAL/SETGLOBAL 见 kind=4 应当 panic**(违反 P1 写入契约),这是一条防护性不变式。

### 2.3 SELF(`obj:m()` 方法查找)

SELF 的 IC 与 GETTABLE 同构(05 §6.4「先做 R(A+1):=R(B),再走与 GETTABLE 同构的 IC 取方法」),提取规则同 §2.1 表 IC,但 P2 输出 `FeedbackKind` 改用 `FBSelfMono`(语义上是「方法调用单态 → 可内联方法查找」,与一般表读区分,P4 用作内联点的输入)。

| ICSlot.kind | P2 提取的 FeedbackKind |
|---|---|
| 0 未初始化 | 跳过 |
| 1 array hit | `FBSelfMono`(罕见,但理论可能:数组段存方法的奇异表) |
| 2 node hit | `FBSelfMono`(主流形态:方法在 metatable 的 `__index` 表里) |
| 3 mono-metamethod | `FBSelfMono`(`__index` 是函数而非表的形态) |
| 4 megamorphic | `FBTableMega`(SELF 多态等同于普通表多态——别投机) |

### 2.4 算术 IC(ADD/SUB/MUL/DIV/MOD/POW/UNM 与比较 LT/LE)

算术 IC 走「双计数比例」而非「kind 字段」,详见 §3。本节先给提取结论(承 §3 的实现细节):

| 双计数 | P2 提取的 FeedbackKind | confidence |
|---|---|---|
| `numHits == 0 && metaHits == 0` | 跳过(未被执行过) | n/a |
| `numHits + metaHits` 总命中过低(< 阈值,§5.3) | `FBUnstable`(样本太少,不可信) | n/a |
| `numHits / total ≥ 0.99`(稳定阈值,§5.3) | `FBArithStableNumber` | numHits/total |
| 0.5 ≤ ratio < 0.99 | `FBUnstable`(混合态,P4 不应投机 f64 快路径) | ratio(供观测,但 kind 已是 Unstable) |
| ratio < 0.5 | `FBUnstable`(主走元方法 → 甚至有可能是「这点根本不是数算」) | 1 - ratio(metaHits 占优) |

> **比较 LT/LE 的特殊**:LT/LE 快路径有「双 number」与「双 string」两种(05 §4.4),P1 的 numHits 只记「快路径命中」不区分二者。P2 当前仅产 `FBArithStableNumber` 标识——粒度不够时,LT/LE 上的 `FBArithStableNumber` 应理解为「快路径稳定」(可能是 number 也可能是 string),P4 投机时还需现场再分流。**这是已知的精度损失**,详见 §9 缺口。

#### 2.4.1 端到端示例:算术 IC 双计数到 FBArithStableNumber

```lua
local function dot(xs, ys, n)
  local s = 0
  for i=1,n do
    s = s + xs[i] * ys[i]   -- ADD pc=A, MUL pc=B
  end
  return s
end
dot({1,2,3}, {4,5,6}, 1000)  -- xs/ys 全是 number,稳态算术
```

`xs[i]*ys[i]` 落到 MUL 指令的 ICSlot:循环 1000 次,每次都是双 number → 1000 次 numHits++,metaHits 始终 0。聚合时:

```
total = numHits + metaHits = 1000 + 0 = 1000
ratio = numHits / total = 1.0
total >= minObservations (100)  ✓
ratio >= stableArithThreshold (0.99)  ✓
→ FeedbackKind = FBArithStableNumber
→ confidence = 1.0
```

P4 据此发 f64 快路径 + guard,典型加速 5-10x(详见 P4 §IC 投机)。

对比一个混合形态:

```lua
local function maybeAdd(a, b)
  return a + b   -- ADD pc=C
end
maybeAdd(1, 2)               -- numHits=1
maybeAdd("hello", " world")  -- metaHits=1(字符串走 __add 兜底,实际错;假设走 string 拼接元方法)
... 10000 次混合调用,number 7000 次、其它 3000 次
```

聚合:
```
total = 7000 + 3000 = 10000
ratio = 0.70
ratio < 0.99
→ FBUnstable(P4 不应投机此点)
confidence = 0.70(诊断字段,Unstable 时也带上)
```

P4 拿到 FBUnstable → 不发 f64 快路径,P3 发通用 ADD 翻译(完整类型分流 + 元方法路径)——保守正确。

---

## 3. 算术 IC 双计数挪用规约(核心)

本节是本文最重的一节,承 [../p2-bridge/00-overview §3.3](./00-overview.md) 的双计数 vs 单布尔之争,落到字段级实现规约。

### 3.1 为什么算术 IC 不能用单布尔

[05-interpreter-loop §6.4](../p1-interpreter/05-interpreter-loop.md) 已经把"P1 写不读"这条立住了——算术快路径靠 `IsNumber` 现场判定,IC 只是旁路写。但旁路写**记什么**是关键:

- **方案 A**(被否):IC slot 加一个 `bool seenNumber`,曾经是 number 就置 true。
  - 致命缺陷:**「曾经是 number」与「恒为 number」无法区分**。一个算术点若 99% 命中 number、1% 走元方法(典型形态:循环里偶尔来一个 string 操作数走 `__add`),`seenNumber=true` 给出和「100% 都是 number」一样的输出——P4 据此投机 f64 快路径,1% 的 guard 失败会反复触发 deopt,投机得不偿失(P4 §IC 投机)。
- **方案 B**(选定):双计数 `numHits` / `metaHits`,记次数。
  - 给 P2 提供「比例」信息:99% number 的点 vs 50% number 的点的 confidence 截然不同,P4 可设阈值(如 ≥99% 才投机)精准筛选,落到稳定点投机命中率更高。

> **比例信息的可调性**:这是 [roadmap.md §5](../roadmap.md) 原则 4「fallback 与投机的分水岭可调」在反馈维度的物理兑现——双计数让阈值是一个**实现期可调的旋钮**,不同负载可以有不同的稳定阈值(算术密集脚本可调激进,混合负载调保守)。单布尔则把这个旋钮焊死在「曾经过 = 投机」上。

### 3.2 字段挪用方案(P1 已落地形态)

[02-bytecode-isa §7](../p1-interpreter/02-bytecode-isa.md) 与 [05-interpreter-loop §6.4](../p1-interpreter/05-interpreter-loop.md) 已联合定稿:**算术 IC 的 ICSlot 闲置字段挪用为双计数,不增 ICSlot 尺寸**。

物理布局(P1 已实装):

```
ICSlot 字段           | 表 IC(kind∈{1,2,3,4})        | 算术 IC(kind 总用 1 或 0)
---------------------|---------------------------------|----------------------------------
shape    uint32      | 目标表 gen 代次                  | numHits   uint32(双计数挪用)
index    uint32      | 命中槽位下标                     | metaHits  uint32(双计数挪用)
tableRef uint32      | 目标表 arena 偏移低 32 位身份    | (留空,恒 0;算术无表无身份)
kind     uint8       | 0/1/2/3/4(见 §1.1)              | 0(未观测)或 1(已被执行过)
```

P2 读取时按 `kind` 分流即可——分流逻辑见 §3.4 聚合器骨架。

> **为什么 tableRef 留空而不也挪用**:挪用 tableRef 作 `numHits` 高位扩展(把双计数从 32-bit 升到 64-bit)在数学上可行,但现实负载里单个程序点的执行计数不会越过 2^32(2^32 ≈ 42 亿次,即使 1ns/次也得 42 秒持续在同一指令)。32-bit 双计数是「够用又不浪费」的平衡点,留 tableRef 空字段给未来扩展(如「记录元方法槽位」用作 SELF mono 优化的回填,详见 §3.5)。

### 3.3 计数语义与饱和处理

P1 的 `recordArithNumber()` 实装(M10 IC 接入轮)按以下语义写入:

```go
// internal/crescent —— P1 已落地形态(05 §4.1 doArith 末尾调用)
func (s *ICSlot) recordArithNumber() {
    // numHits 在 shape 字段(算术 IC kind 不用 shape)
    if s.shape != ^uint32(0) {  // 防止饱和后回绕(0xFFFFFFFF 即 ~0)
        s.shape++
    }
    s.kind = 1                  // 标记此 slot 已被执行过(0→1,只升不降)
}

func (s *ICSlot) recordArithMeta() {
    // metaHits 在 index 字段
    if s.index != ^uint32(0) {
        s.index++
    }
    s.kind = 1
}
```

要点:

1. **饱和而非回绕**:`numHits` 接近 2^32 时停止递增(`if != ~0 then ++`),不让计数回绕到 0 —— 否则某个超热点跑 2^32 次后 confidence 突变是难以诊断的 bug。饱和值 2^32-1 对 P2 计算 `numHits/total` 的影响完全可忽略(分子分母同尺度饱和,比例不变;若仅一边饱和,长期看 confidence 接近 1.0,符合「这点已经稳定到极致」的物理直觉)。
2. **`kind` 在算术 IC 上的轻量用法**:0 是「未观测」(P2 跳过此点,§2.4 第一行),1 是「已观测过」(P2 检查比例)。算术 IC 永远不会写 kind∈{2,3,4}——这些值是表 IC 专用,P2 见到算术 pc 上 kind∈{2,3,4} 应当 panic(违反 P1 写入契约,与 §2.2 globals megamorphic 防护性不变式同性质)。
3. **不区分快路径子分支**:Lua 5.1 算术快路径只有「双 number → f64 运算」一种(05 §4.1),所以 numHits 不需要细分;字符串自动转数字(`"10"+5`)走慢路径前置 coercion(05 §4.1 引文),会被记到 metaHits ——这是设计抉择(string→number coercion 不算"稳定 number"),P4 投机时把这种点判 `FBUnstable` 即可。

### 3.4 P2 算术 feedback 提取骨架

```go
// internal/bridge —— P2 算术 IC 聚合器
//
// 输入:Proto 的 ICSlot 数组(P1 已写)、pc 位置、IC 点类型(由 opcode 决定)
// 输出:PointFeedback(可能是 FBArithStableNumber / FBUnstable)
func (a *aggregator) extractArithFeedback(pc int32, slot *bytecode.ICSlot) PointFeedback {
    if slot.kind == 0 {
        return PointFeedback{} // 跳过:未观测
    }
    // 算术 IC 上 kind 必为 1,任何其它值都是 P1 写入契约违反
    if slot.kind != 1 {
        panic(fmt.Sprintf("bridge: arith IC at pc=%d has kind=%d, expected 0 or 1", pc, slot.kind))
    }
    numHits := slot.shape  // 双计数挪用:shape 字段存 numHits
    metaHits := slot.index // 双计数挪用:index 字段存 metaHits
    total := uint64(numHits) + uint64(metaHits)
    if total < a.minObservations {
        // 总命中数过低 → 样本不足,不下结论
        return PointFeedback{
            pc:         pc,
            kind:       FBUnstable,
            confidence: 0.0, // 显式 0,标识「不可信」
        }
    }
    ratio := float32(numHits) / float32(total)
    if ratio >= a.stableArithThreshold { // §5.3 默认 0.99
        return PointFeedback{
            pc:         pc,
            kind:       FBArithStableNumber,
            confidence: ratio,
        }
    }
    return PointFeedback{
        pc:         pc,
        kind:       FBUnstable,
        confidence: ratio, // 即便 Unstable 也带上,供诊断
    }
}
```

关键性质:

- **纯函数,无副作用**:输入 ICSlot 状态、输出 PointFeedback,不修改 slot。这是 §1.3「P2 不写 IC」的代码兑现。
- **与 P1 解释器并发安全**:`slot.shape` / `slot.index` 的读取是 race-tolerant 的(可能读到「半新半旧」的值,但 ratio 计算不会爆炸——分母总是非零,因为 kind=1 蕴含 total≥1)。详见 §5.4。
- **阈值 a.stableArithThreshold 与 a.minObservations 是聚合器构造时的参数**,不硬编码——见 §5.3 默认值与定标策略。

### 3.5 表 IC 与算术 IC 的字段语义分流(防混淆)

字段挪用最大的风险是**字段语义按 kind 分流**这件事在代码里隐藏太深,后续维护者读 `slot.shape` 时不知道究竟是「表 gen」还是「numHits」。三条规约控制风险:

1. **强制 helper accessor**:`internal/bytecode` 暴露 `ArithNumHits(s *ICSlot) uint32 { return s.shape }` / `ArithMetaHits(s *ICSlot) uint32 { return s.index }`,**所有访问方必须经 helper**,直接读 `slot.shape`/`slot.index` 在算术 IC 上下文应被 lint 标红(详见 [06-testing-strategy](./06-testing-strategy.md) 静态检查)。
2. **kind 即语义判别符**:任何读取 IC slot 的代码必须先看 `kind` 决定字段语义——P2 聚合器的 §3.4 骨架就是按这条做的;表 IC 路径(§2.1-§2.3)同样先 `switch slot.kind`。
3. **P3/P4 不直接读 ICSlot**:P3/P4 只读 P2 产出的 `TypeFeedback`(§4),不直接读 ICSlot——这层封装把字段挪用的复杂度锁在 `internal/bytecode` 与 `internal/bridge` 内,跨包零泄露。

### 3.6 «已兑现» 状态对账

承 [00-overview](./00-overview.md) §7「P1 已落地的前瞻义务对账」:

| 前瞻义务 | P1 落地状态 | 出处 |
|---|---|---|
| 算术 IC 双计数(numHits/metaHits 挪用 shape/index/tableRef) | ✅ M10 IC 接入轮实装,P1 写不读 | 02 §7 / 05 §6.4 / [implementation-progress](../p1-interpreter/implementation-progress.md) IC 命中路径行 |

**结论**:本节描述的所有 P1 端要求**已在 P1 全卷交付时同批兑现**,P2 PB2(IC 反馈聚合器,见 [00-overview](./00-overview.md) §4 里程碑)启动时直接读 ICSlot 即可,P1 端零新开发。

---

## 4. TypeFeedback shape:字段级定义

P2 把一个 Proto 的所有 IC 观测聚合成一份 `TypeFeedback`,挂在 `ProfileData` 旁,P3/P4 按 pc 索引消费。

### 4.1 TypeFeedback 顶层结构

```go
// internal/bridge —— 一个 Proto 的类型 feedback(P2 聚合,P3/P4 消费)
//
// 按 pc 索引每个程序点的类型稳定性判断。**P2 产出但不自用**(§7)。
type TypeFeedback struct {
    // points 按 pc 索引;长度等于 Proto.Code 长度;
    // 非 IC 点(opcode 不带 IC,如 LOADK / MOVE / RETURN)对应槽位 kind=FBUnstable,
    // confidence=0,P3/P4 应跳过。
    points []PointFeedback

    // generation 是 feedback 快照的代次,每次 P2 重新聚合时递增;
    // P3/P4 拿到的快照若 generation 落后于当前,说明聚合期间 P1 又新写了一批观测,
    // 但这不影响正确性——P3/P4 投机的 guard 总会兜住运行期实际偏差(P4 §IC 投机)。
    generation uint32

    // observedAt 是聚合发生时的 wall-clock 纳秒(诊断用,非语义)。
    observedAt int64
}
```

### 4.2 PointFeedback 字段定义

```go
type PointFeedback struct {
    // pc 是该点在 Proto.Code 中的下标(冗余字段,等于 TypeFeedback.points 的索引;
    // 保留是为了把 PointFeedback 单点传递给下游时不丢失位置信息,
    // 例如 P4 投机日志「pc=42 fbkind=ArithStableNumber」)。
    pc int32

    // kind 是该点的反馈类型,详见 §4.3 枚举。
    kind FeedbackKind

    // confidence 是稳定度,[0.0, 1.0]:
    // - 算术点:numHits/(numHits+metaHits)(§3.4)
    // - 表/全局点:1.0(命中即稳定;mono IC 无降级历史 → 全是 1.0)
    // - FBUnstable / FBTableMega:语义上无 confidence(填 0.0 或观察比例,P4 应忽略)
    confidence float32

    // stableShape:表/全局点的「稳定 shape」——即 ICSlot.shape(目标表 gen 代次)
    // 的快照值。P4 投机直达槽时,先 guard「当前表 gen == stableShape」,
    // 失败则 deopt 回 P1 解释。算术点不填(stableShape=0)。
    stableShape uint32

    // stableIndex:表/全局点的「稳定槽位下标」——即 ICSlot.index(命中时的
    // array/node 槽位)的快照。P4 投机命中时直接索引此槽,不查哈希。
    // 算术点不填(stableIndex=0)。
    stableIndex uint32

    // observations 是聚合时累计的观测次数(算术点 = numHits+metaHits;
    // 表点 = 命中次数,P1 当前实装不单独计数,默认填 1 或 P2 自累计)。
    // 用作下游消费方的样本量参考——例如 P3 可设「<100 次的点不发紧凑翻译」。
    observations uint32
}
```

### 4.3 FeedbackKind 枚举

```go
type FeedbackKind uint8

const (
    // FBUnstable —— 「不投机」标识。覆盖三种来源:
    //   1. 该点未被 IC 观测过(kind=0)
    //   2. 算术点比例不达标(<0.99)
    //   3. 非 IC 点的默认填充
    FBUnstable FeedbackKind = iota

    // FBArithStableNumber —— 算术点恒为 number 操作数(>=99% numHits/total)。
    // P4 据此发 f64 快路径 + guard:进入指令前 IsNumber(b) && IsNumber(c),
    // 失败则 deopt 回 P1 解释执行该指令(P4 §IC 投机)。
    FBArithStableNumber

    // FBTableMono —— 表访问单态稳定(GETTABLE/SETTABLE,kind∈{1,2,3})。
    // P4 据此投机直达槽:进入指令前 guard「目标表 gen == stableShape」,
    // 命中则直接 array/node 索引取值,失败 deopt。
    FBTableMono

    // FBTableMega —— 表访问 megamorphic(05 §6.3 kind=4)。
    // 「别投机」明确标识——P4 见此点应当老老实实查哈希(走 P3 通用翻译,
    // 不发投机直达槽)。
    FBTableMega

    // FBGlobalStable —— 全局读恒定。GETGLOBAL/SETGLOBAL 的 globals 是单一表,
    // node hit 即稳定;P4/P3 可常量化(把 stableIndex 槽位的当前值
    // 内联进编译产物——但需 guard globals gen 等于 stableShape,失败 deopt)。
    FBGlobalStable

    // FBSelfMono —— 方法调用单态(SELF + 紧随 CALL 的方法分发点)。
    // P4 据此内联方法查找:guard metatable gen + 直达方法槽,
    // 命中则跳过 GETTABLE 步骤,直接进 CALL。
    FBSelfMono
)
```

### 4.4 不被覆盖的反馈类型(P1 简化)

下游若需要更细粒度的反馈(如「字符串拼接稳定性」「比较操作子类型分布」),P1 当前不写、P2 也产不出对应 `FeedbackKind`。已知缺位:

| 缺位反馈 | 当前替代 | 升级路径 |
|---|---|---|
| 比较 LT/LE 区分 number vs string | 统一归入 `FBArithStableNumber`(粒度损失,§2.4 注) | P4 投机时 guard 双 number,失败 deopt;若实测损失大,P1 端在比较 IC 写入加分流字段 |
| CONCAT 全 number/string 稳定性 | 不写 IC,P2 无 feedback | 留 P4(若 CONCAT 投机有收益再补) |
| 一元 NOT 的真值分布 | NOT 无元方法,P1 不写 IC,P2 无 feedback | 不预期需要(P4 投机价值有限) |
| `__index`/`__newindex` 链长度分布 | mono IC 退化为 `kind=3`,P2 标 `FBTableMono` | 长链常驻 metatable 的形态够用;深链优化留 P5 |

这些缺位**不影响正确性**——下游若没拿到对应 feedback,默认走 P3 通用翻译(非投机),即「损失加速,不损失正确性」(原则 4)。

### 4.5 ProfileData 的 feedback 槽

`TypeFeedback` 挂在 Proto 旁的 `ProfileData` 上,与 [01-profiling](./01-profiling.md) §3 的热度计数同居一处:

```go
// internal/bridge —— Proto 旁的 ProfileData(承 01-profiling §3)
type ProfileData struct {
    backEdgeCount uint64       // 回边计数(01-profiling)
    entryCount    uint64       // 入口计数(01-profiling)
    feedback      *TypeFeedback // 类型反馈(本文)—— 指针,惰性聚合后才非 nil
    compilable    Compilability // 可编译性判定(03-compilability-analysis)
    tier          TierState    // 状态机(04-try-compile-fallback)
}
```

`feedback` 字段语义:

- **初值 nil**:Proto 刚 Compile 出来时未聚合 feedback。
- **首次聚合时机**:由 [04-try-compile-fallback](./04-try-compile-fallback.md) 状态机驱动——`considerPromotion` 触发时(热度过阈值且可编译),先 `aggregator.aggregate(proto)` 算出 feedback,再喂给 P3/P4。
- **重聚合策略**:P2 初版**只聚合一次**(首次升层时),不重复聚合——P3 拿到 feedback 后编译完成,gibbous 代码就跑;**P4 在投机失败 deopt 后是否重聚合**是 P4 阶段的设计,P2 不预设。
- **多 State 共享 Proto 的并发**:Proto 是只读共享对象,`ProfileData.feedback` 写入需用 `atomic.CompareAndSwap`(避免两个 State 同时 considerPromotion 重复聚合),详见 §5.5。

---

## 5. confidence 计算细则与稳定阈值

### 5.1 confidence 在不同 FeedbackKind 上的语义

`confidence` 字段的物理含义随 `FeedbackKind` 变化:

| FeedbackKind | confidence 含义 | 计算方式 |
|---|---|---|
| FBArithStableNumber | numHits 占比(数值算术稳定度) | `float32(numHits) / float32(numHits+metaHits)` |
| FBTableMono | 命中稳定度(P1 mono IC 无降级 → 恒 1.0) | 1.0 |
| FBTableMega | 「别投机」明确标识,无 confidence 概念 | 0.0(填充) |
| FBGlobalStable | 命中稳定度(globals 也是 mono IC) | 1.0 |
| FBSelfMono | 命中稳定度 | 1.0 |
| FBUnstable | 「不投机」标识,有时附诊断比例 | 0.0 或 numHits/total(算术点诊断) |

**关键观察**:**只有算术 IC 的 confidence 是「真比例」,表 IC 的 confidence 是「布尔翻译成 0/1」**。原因是 P1 mono IC 不区分「连续多次命中」与「首次命中」——表 IC 的 ICSlot 没有命中计数,kind=2 即「上次命中」,P2 无法区分"上次"是 1 次还是 1 万次。

> **是否给表 IC 加命中计数**?可以,但代价是表 IC 也得挪用 ICSlot 字段——而表 IC 的 shape/index/tableRef 三字段已被占满(都是命中路径必需)。要给表 IC 加计数得**新增字段或扩 ICSlot 尺寸**,与「不增 ICSlot 尺寸」的设计基线冲突。当前权衡:**算术 IC 用真比例(精准 confidence)、表 IC 用 mono 标记(粗 confidence)**,后续若 P4 实测发现表 IC 的 confidence 粒度不够再开扩展。

### 5.2 算术稳定阈值(stableArithThreshold)

默认值 `0.99`(99% 命中数 number 才判稳定)。论证:

- **下界 0.95**:更松的阈值会让「混合 5% 元方法」的点也判稳定——P4 投机后每 20 次就一次 guard 失败 deopt,实测投机收益约等于零(deopt 开销 ≈ 几次解释执行,5% deopt 抵消 95% f64 加速)。
- **上界 0.999**:更严的阈值会让大部分真实算术点不达标(实际负载里 1% 以下的元方法触发率不算少见,如自定义数字类型),漏判过多导致 P4 失去投机机会。
- **0.99 的实证依据**:V8/SpiderMonkey 的 IC 稳定阈值都在 [0.97, 0.99] 区间(经验值,无文献固化);0.99 偏保守一档(宁漏勿误,与原则 4 一致)。

实现期参数化:

```go
type AggregatorConfig struct {
    StableArithThreshold float32 // 默认 0.99
    MinObservations      uint64  // 默认 100,样本量下限
    // ... 其它阈值待 §5.3 补
}
```

**阈值不影响正确性**——只影响何时投机。守住「P4 投机失败 deopt 必须正确回到 P1」(P4 的不变式),阈值高低只是性能调旋。

### 5.3 minObservations 样本量下限

`numHits + metaHits < minObservations` 时,P2 把 confidence 视作不可信,直接判 `FBUnstable`(§3.4 骨架)。默认 `100`。

为什么需要下限:

- **极少观测样本无统计意义**:若一个算术点只跑了 3 次,3 次都是 number(numHits=3, metaHits=0,ratio=1.0)看起来「稳定」,但 3 次的样本根本不能代表运行期分布。冷启动期、刚进循环的几次迭代、罕见错误处理路径都属此类。
- **与热度阈值的解耦**:`HotEntryThreshold=200`(01-profiling §5)是函数入口热度,不蕴含函数内部某算术点也跑了 ≥100 次——例如循环外的 `local k = a + b` 进入函数 200 次仅累积 200 次 IC 观测,落后于循环内 100 次迭代×200 = 2 万次。`minObservations=100` 是 IC 点级别的独立下限,与函数热度互补而非冗余。

100 的实证依据:统计上 100 个观测的 95% 置信区间宽度 ≈ ±10%,足以区分「99% 稳定」与「90% 稳定」(后者必判 Unstable);更小的样本量(如 30)区间宽度 ±18%,容易误判。

### 5.4 与 P1 解释器的并发读策略(race-tolerant 读)

P2 聚合时 P1 解释器仍在跑(尤其多 State 场景:State A 在 considerPromotion 走聚合器,State B 仍在执行同一 Proto 写 IC slot)。聚合器对 IC slot 的读是**race-tolerant 的非原子读**,论证:

1. **写半新半旧不爆炸**:`numHits++` 是 32-bit 字段,Go race detector 会在 `-race` 标记 race,但 x86/arm64 上 32-bit 对齐写是原子的(不会读到「半字节新半字节旧」)。读到「数值在新旧之间」是可能的,但 ratio 计算只是浮点除法,不会因为读到旧的 numHits 与新的 metaHits 而爆炸。
2. **聚合产物本就是统计性的**:`confidence` 是估计值,不是断言;读到的 numHits/metaHits 即便落后 P1 当前值几次递增,聚合结论(稳定 vs 不稳定)在边界附近可能翻动一次,但**这翻动的代价仅是 P3/P4 多/少投机几次**,不影响正确性(P4 投机失败有 deopt 兜底,§4.3)。
3. **避免 atomic 的开销**:把所有 IC 写入改 `atomic.AddUint32` 会让 P1 算术快路径每次多一个 LOCK 前缀(x86)或 LDADD(arm64),对**算术密集脚本(simple/arith 档)**性能敏感(P1 验收门槛是 ≥2x over gopher-lua)。non-atomic 写换 race-tolerant 读是必要妥协。

实现侧的具体保护(避免 race detector 报警):

```go
// internal/bridge —— race-tolerant 读 helper
//
//go:nosplit
//go:noinline
func raceTolerantLoadUint32(p *uint32) uint32 {
    // 在算术 IC 上下文中读 numHits/metaHits;
    // -race 下用 atomic.LoadUint32 显式标 happens-before(避免 false positive);
    // 非 -race 下退化为普通读(零开销,但仍按 32-bit 对齐保证不撕裂)。
    return atomic.LoadUint32(p)
}
```

> **争议点**:也可以用 build tag(`//go:build race`)切两套实现,非 -race 下完全裸读。但 `atomic.LoadUint32` 的开销在现代 CPU 上是单条 MOV(无内存屏障,因为 x86/arm64 的 32-bit 对齐 load 本就有 acquire 语义),与裸读差异极小;统一用 atomic load 简化代码,**已足够**。

### 5.5 多 State 共享 Proto 的 feedback 写入(CompareAndSwap)

`ProfileData.feedback` 是 `*TypeFeedback` 指针,写入需 atomic CAS(避免两个 State 同时 considerPromotion 重复聚合写两份):

```go
// internal/bridge —— ProfileData.feedback 安装
func (pd *ProfileData) installFeedback(fb *TypeFeedback) {
    // 已有 feedback 不覆盖(P2 初版只聚合一次,§4.5);
    // 若被并发抢先安装,本次聚合产物丢弃即可——内容等价(读同一份只读 ICSlot)。
    if !atomic.CompareAndSwapPointer(
        (*unsafe.Pointer)(unsafe.Pointer(&pd.feedback)),
        nil, unsafe.Pointer(fb),
    ) {
        // 抢失败 → 已有 feedback,丢弃本次产物(GC 回收)
    }
}
```

要点:

- **CAS 失败不重试**:抢失败说明已有 feedback,等价于本次聚合白做——但代价小(纯 CPU,不分配 IC slot 写)。重试无意义。
- **只读消费方无需 CAS**:`ProfileData.feedback` 一旦从 nil 变非 nil 就不再变(P2 初版),P3/P4 只读取无需保护。**P4 阶段若引入「投机失败后重聚合」**才需要把消费方也改成 atomic load,P2 不预设。

> 这与 [01-profiling](./01-profiling.md) §6 的「profile 计数挂 State 私有」是不同维度——profile **计数**因频繁更新所以挂 State 私有(避免 atomic 写 Proto 旁);**feedback** 因一次性聚合所以挂 Proto 旁共享(让所有 State 复用同一份)。两个数据不同生命期,存储位置不同是合理的。

---

## 6. megamorphic 标记的 P2 落地

[05-interpreter-loop §6.3](../p1-interpreter/05-interpreter-loop.md) 末尾原话:

> `kind=4 megamorphic` 预留给 P2 标记「此点多态、别再投机」(供编译层用,P1 可不实现降级,见 §6.4)。

本节是这条预留的 P2 端落地。

### 6.1 P1 当前不写 kind=4

[implementation-progress](../p1-interpreter/implementation-progress.md) 的 IC 命中路径行明确「mono IC,array/node 直达」——P1 实装是**纯 mono**:换表即重填 ICSlot,kind 在 {0, 1, 2, 3} 间转移,**永不写 kind=4**。这意味着:

- **P2 不能仅靠"读 kind=4"识别 megamorphic**——P1 永远不会主动标这个 kind。
- **megamorphic 的识别责任在 P2**——P2 需要在聚合时主动判断「这个点是否反复重填」,然后把它翻成 `FBTableMega`。

这是 [00-overview](./00-overview.md) §1 边界表「IC slot 的写入 P1 拥有」与「读取消费 P2 拥有」的微妙之处:**P1 不写 kind=4 不等于 P2 没责任标 megamorphic**——只是责任从「P1 主动降级写 kind=4」转移到「P2 聚合时分析模式」。

### 6.2 P2 megamorphic 识别的两种实现

| 方案 | 输入 | 判定 | 代价 |
|---|---|---|---|
| (A) 单点单帧:仅靠当前 ICSlot 状态 | slot.kind, slot.shape, slot.tableRef | 无法识别(单帧只看到「当前命中」,看不到「过去几次也是不同表」) | 0(无开销) |
| (B) 历史窗口:为表 IC 也加重填计数 | slot.{kind,...} + 重填次数 | 重填次数 > 阈值 → 标 mega | 表 IC 需要扩字段(见 §5.1 的权衡) |
| (C) 运行期采样:P1 在 ICSlot miss 时累计「miss-after-fill」次数到旁路计数器 | 旁路计数器(非 IC slot) | miss-after-fill > 阈值 → 标 mega | 旁路计数,P1 写额外计数;但不增 ICSlot 尺寸 |

**P2 初版选 (A)**——即 P2 不主动识别 megamorphic,仅在 ICSlot.kind=4 时翻译成 FBTableMega(防御性兼容,实际 P1 当前不写)。论证:

1. **保守第一**:P2 不识别 mega 的代价是 P4 把多态点也投机(投机失败频繁 deopt),性能损失;但**正确性不损失**(P4 deopt 兜底)。
2. **方案 B/C 的复杂度溢出**:扩 ICSlot 字段 / 加 P1 旁路计数都是 P1 端改动,违反「P2 启动是纯增量,P1 零新开发」(00-overview §7 结论)。
3. **真实负载里 mono IC 退化的占比待定**:若实测发现 P4 投机失败率高,再加方案 B/C(把识别 mega 当作"P2 演进 PB+"而非 PB2 强制特性)。

> **方案 (A) 的物理体现**:`§3.4 extractTableFeedback`(对应算术 IC 骨架的表 IC 版本)的逻辑就是 `switch slot.kind { case 4: return FBTableMega; default: return FBTableMono }`——简洁明了,无识别复杂度。

### 6.3 表 IC 聚合骨架(对应 §3.4 算术版本)

```go
// internal/bridge —— P2 表 IC / 全局 IC / SELF 聚合器
func (a *aggregator) extractTableFeedback(pc int32, slot *bytecode.ICSlot, opType opTableKind) PointFeedback {
    if slot.kind == 0 {
        return PointFeedback{} // 跳过:未观测
    }
    pf := PointFeedback{
        pc:           pc,
        confidence:   1.0,                  // §5.1:表 IC mono 即 1.0
        stableShape:  slot.shape,
        stableIndex:  slot.index,
        observations: 1,                    // §5.1:表 IC P1 不计数,填占位 1
    }
    switch slot.kind {
    case 1, 2, 3: // array hit / node hit / mono-metamethod
        switch opType {
        case opGetTable, opSetTable:
            pf.kind = FBTableMono
        case opGetGlobal, opSetGlobal:
            pf.kind = FBGlobalStable
        case opSelf:
            pf.kind = FBSelfMono
        }
    case 4: // megamorphic(P1 当前不写,防御性翻译)
        pf.kind = FBTableMega
        pf.confidence = 0.0
        pf.stableShape = 0
        pf.stableIndex = 0
    default:
        panic(fmt.Sprintf("bridge: table IC at pc=%d has unexpected kind=%d", pc, slot.kind))
    }
    return pf
}
```

### 6.4 全 Proto 聚合入口

```go
// internal/bridge —— 一个 Proto 的全 feedback 聚合(由 considerPromotion 调用)
func (a *aggregator) aggregate(proto *bytecode.Proto) *TypeFeedback {
    fb := &TypeFeedback{
        points:     make([]PointFeedback, len(proto.Code)),
        generation: atomic.AddUint32(&a.globalGen, 1),
        observedAt: time.Now().UnixNano(),
    }
    for pc, ins := range proto.Code {
        op := bytecode.OpcodeOf(ins)
        slot := &proto.IC[pc]
        switch op {
        case bytecode.OpADD, bytecode.OpSUB, bytecode.OpMUL,
             bytecode.OpDIV, bytecode.OpMOD, bytecode.OpPOW,
             bytecode.OpUNM, bytecode.OpLT, bytecode.OpLE:
            fb.points[pc] = a.extractArithFeedback(int32(pc), slot)
        case bytecode.OpGETTABLE:
            fb.points[pc] = a.extractTableFeedback(int32(pc), slot, opGetTable)
        case bytecode.OpSETTABLE:
            fb.points[pc] = a.extractTableFeedback(int32(pc), slot, opSetTable)
        case bytecode.OpGETGLOBAL:
            fb.points[pc] = a.extractTableFeedback(int32(pc), slot, opGetGlobal)
        case bytecode.OpSETGLOBAL:
            fb.points[pc] = a.extractTableFeedback(int32(pc), slot, opSetGlobal)
        case bytecode.OpSELF:
            fb.points[pc] = a.extractTableFeedback(int32(pc), slot, opSelf)
        default:
            // 非 IC 指令:fb.points[pc] 保持零值(kind=FBUnstable,confidence=0)
        }
    }
    return fb
}
```

性质:

- **O(N) 单遍**:N=Proto.Code 长度;每个 pc 一次 switch + 一次提取。冷热路径一视同仁。
- **无副作用**:不写 ICSlot、不写 ProfileData(写入由 §5.5 `installFeedback` 单独负责)。
- **可重入**:同一 Proto 多线程并发调用 aggregate 安全(都是只读 ICSlot)——`installFeedback` 的 CAS 把竞争收口。

---

## 7. 「P2 写 feedback 不消费」原则

### 7.1 三层信息分层生产消费

P1 写 IC、P2 读 IC 写 feedback、P3/P4 读 feedback 投机——这是 [00-overview](./00-overview.md) §2 总数据流的字面体现。本节给 P2 维度的强化论证。

| 层 | 生产 | 消费 | 不消费什么 |
|---|---|---|---|
| P1 | ICSlot 命中观测 | ICSlot 表/全局命中(取值加速) | **不读算术 IC**(§1.3 写不读)、不读自己写的 feedback |
| **P2** | **TypeFeedback** | **ICSlot(只读聚合)** | **不读自己写的 feedback**(§7.2)、不发射代码、不投机 |
| P3 | gibbous Wasm 代码 | TypeFeedback(可选,锦上添花) | 不投机(P3 是 try-compile 非投机) |
| P4 | fullmoon-method 代码 + deopt 着陆点 | TypeFeedback(核心,confidence 决定激进度) | — |

**P2 是中间层,只生产不消费 feedback**——这一条对应 [00-overview](./00-overview.md) §1 末尾铁律「P2 是中间的『决策加工厂』,自己不进热路径」。

### 7.2 P2 不消费 feedback 的硬证据

用反证法:若 P2 消费 feedback,会发生什么?

- **可能形态 1:P2 在 considerPromotion 时根据 feedback 调整热度阈值**(如「feedback 全 unstable 的 Proto 提高阈值,延后编译」)。**否决**:阈值调整与 feedback 解耦,P2 状态机用「热度 + 可编译性」决定升层(04-try-compile-fallback §3),feedback 只是喂给 P3/P4 的供料,不参与决策。耦合二者会让 P2 状态机难以推理(混入投机收益估算)。
- **可能形态 2:P2 内置「假执行」根据 feedback 预测优化收益**。**否决**:这是 P3/P4 的工作(P3 的紧凑翻译收益估算 / P4 的投机点筛选),P2 做等于跨层职责越界——`internal/bridge` 一旦出现「跑 Proto / 模拟执行」就违反 [00-overview](./00-overview.md) §1 铁律。
- **可能形态 3:P2 用 feedback 优化自己的可编译性分析**。**否决**:可编译性分析是结构性的(F1-F7,03-compilability-analysis §3),不依赖运行期类型信息——一个函数有 vararg 就不可编译,与 feedback 是 ArithStableNumber 还是 Unstable 无关。

**结论**:P2 用不到 feedback,反过来说 P2 端的所有数据流都不应读 `ProfileData.feedback` 字段。这是设计期就能锁死的不变式——`internal/bridge` 内禁止任何函数读 `ProfileData.feedback`(only writers),违反就 lint 报错。

### 7.3 与 P1 算术 IC「写不读」的同构

[05-interpreter-loop §6.4](../p1-interpreter/05-interpreter-loop.md) 的「P1 写算术 IC 不读」与本节「P2 写 feedback 不消费」是**同一原则在不同层的兑现**:

| 维度 | P1 算术 IC 写不读 | P2 feedback 写不消费 |
|---|---|---|
| 写方 | P1 解释器 | P2 聚合器 |
| 不读方 | P1 解释器(自己) | P2(自己)|
| 读消费方 | P2 聚合器 | P3/P4 编译器 |
| 设计意图 | P1 不依赖算术 IC 取值 → IC 写入可被任意改 | P2 不依赖 feedback 决策 → feedback 仅供下游 |
| 物理性质 | 旁路写,不影响 P1 性能(05 §4.1) | 旁路写,不影响 P2 决策正确性(本节) |

这种「跨层供料」模式是分层桥的本质——每一层只关心自己的输入与输出,不窥探下一层怎么用。

---

## 8. 不变式清单

P2 IC 反馈子系统实现期必须守住的硬性约束,违反即设计失败:

1. **P1 IC 写入快路径绝对不动**:[05-interpreter-loop §6.4](../p1-interpreter/05-interpreter-loop.md) 已立的「写不读」P2 不破坏;P1 算术快路径仍是 `IsNumber(b) && IsNumber(c)` 现场判定 + numHits++;P2 上线后 P1 端字节码、IC 写入逻辑零修改。
2. **P2 只读 ICSlot 不写**:聚合器的所有路径都是纯读,不刷新 kind、不重置计数、不改 shape/index/tableRef。这是 P1↔P2 单向数据流的物理体现。
3. **同一 feedback P3 选用 P4 必用**:P3 可选(锦上添花的紧凑翻译,不依赖正确性),P4 核心(类型投机的输入,依赖 confidence 决定激进度);**两者读同一份 `TypeFeedback`,字段定义稳定** —— P2 不为 P3 P4 各产一份(浪费),也不在 P3 升层时省略 feedback(把决策推给 P4)。
4. **字段挪用按 kind 分流**:ICSlot.shape/index/tableRef 在算术 IC 与表 IC 上语义不同,所有访问方必须先看 kind 再决定字段含义;直接读字段在 lint 标红。
5. **P2 不消费 feedback**:`internal/bridge` 内禁止读 `ProfileData.feedback`(only writers),违反就 lint 报错。这是 §7 的代码级兑现。
6. **饱和不回绕**:numHits/metaHits 接近 2^32 时停止递增,不让计数回绕到 0(§3.3 论证)。
7. **race-tolerant 读不爆炸**:聚合器读 ICSlot 字段是非原子的(算术 IC 写也非原子,§5.4),读到「半新半旧」值时 ratio 计算不爆炸——这要求 numHits/metaHits 字段对齐 32-bit 边界(`ICSlot` 结构体已自然对齐)。
8. **CAS 安装 feedback 不重试**:多 State 并发 considerPromotion 时,首个 CAS 成功的版本胜出,后到的丢弃聚合产物(§5.5)。
9. **megamorphic 翻译要防御性**:即便 P1 当前不写 kind=4(§6.1),P2 表 IC 聚合的 switch 必须包含 case 4 → FBTableMega 的分支(防御 P1 演进后开始写 mega 时 P2 已就绪)。
10. **kind=0 即跳过**:任何 ICSlot.kind=0 的点 P2 都不产 feedback(返回零值 PointFeedback,即 FBUnstable + confidence=0),让下游一视同仁地"非 IC 点 == 未观测点 == 别投机"。

---

## 9. 文档缺口

承 [00-overview](./00-overview.md) §7 对账,本文涉及的 P1 端前瞻义务(算术 IC 双计数挪用)**已全部兑现**——理论上无需新回填请求。但本节列出 P2 实现期可能浮现的二阶细节,记入 [doc-gaps](../../../llmdoc/memory/doc-gaps.md) 待 P2 真实开发时校准。

### 9.1 表 IC 命中计数(精度损失记账)

§5.1 已论证「表 IC confidence 恒 1.0」是粗粒度——P4 投机时拿不到「这个点过去命中 1 次还是 1 万次」的信息。当前权衡:**算术 IC 真比例,表 IC 布尔**。

潜在升级:若 P4 实测发现表 IC 投机失败率高(mono IC 实际是「换表罕见但偶尔翻车」形态),需扩 ICSlot 字段或加旁路计数。**留 P2+ / P4 阶段补**,与「精确 yield 分析」(03-compilability-analysis F2 缺口)同性质。

### 9.2 比较 LT/LE 的 number vs string 分流

§4.4 注与 §4.4 缺位表已记:LT/LE 的 numHits 只记「快路径命中」不区分双 number / 双 string,P4 投机 f64 时仍需现场再 IsNumber——粒度损失。

潜在升级:P1 比较 IC 写入加分流字段(用 tableRef 字段挪用一个「快路径子分支编号」),P2 提取时分 `FBArithStableNumber` / 假设新增 `FBArithStableString`。**留 P2+ 或 P4 实测后补**。

### 9.3 megamorphic 主动识别(方案 B/C)

§6.2 已论证 P2 初版选方案 (A)(不主动识别 mega,仅翻译 ICSlot.kind=4 的防御性兼容)。

潜在升级:P4 实测多态点投机失败率,若高则补方案 (B)(表 IC 加重填计数)或 (C)(P1 旁路 miss-after-fill 计数)。**与 §9.1 同决策路径**——P2 演进项,非 PB2 强制。

### 9.4 Confidence 阈值的负载校准

§5.2 给的 0.99 / §5.3 给的 100 是经验默认值。**P2 PB7 端到端验收时**用 realworld 五脚本(benchmarks/realworld)校准:观察各算术点的 numHits/total 分布,看 0.99 阈值是否合理(过严漏判 vs 过松误投机)。**与 [01-profiling](./01-profiling.md) §5 的热度阈值校准并列**——都是「实现后再定标」的旋钮,不影响正确性。

### 9.5 重聚合策略(P4 投机失败后)

§4.5 说「P2 初版只聚合一次(首次升层时),不重复聚合」。但 P4 阶段若投机失败 deopt,运行期类型分布可能已经变化(例如循环过半数据类型从 number 变成 string)——此时**用旧 feedback 重新投机会再次失败**。

潜在升级:P4 投机失败回调 P2 触发重聚合,P2 把 ProfileData.feedback 字段从 atomic.LoadPointer 改为读 generation 戳判断时效。**留 P4 阶段决策**,P2 PB2 不预设。

### 9.6 多 State 共享 Proto 的 feedback 一致性

§5.5 的 CAS 安装确保只有一份 feedback 写入,但**两个 State 同时聚合产生的内容是否完全一致**取决于 ICSlot 在聚合瞬间的状态——若 State A 聚合时 State B 仍在写 IC,A、B 各自看到的 ICSlot 快照可能不同。

实际影响:即便 A 与 B 产出的 confidence 略有差异(例如 0.99 vs 0.995),CAS 抢到的版本胜出,另一个丢弃——下游消费一份就好。**这是 race-tolerant 的接受范围**(§5.4 已立),不视为缺口而是设计抉择;若实测发现 confidence 抖动让 P4 投机不稳定,再考虑「聚合期 P1 解释器暂停」(STW 风格)——代价大,目前无依据上。

---

## 10. 相关

- [00-overview](./00-overview.md)(P2 总览,§1 边界 / §3 关键耦合点 / §7 P1 落地对账 ✅)
- [01-profiling](./01-profiling.md)(热度采样,与本文 IC 反馈是 P2 的两条供料线)
- [03-compilability-analysis](./03-compilability-analysis.md)(可编译性闸门,与 feedback 正交;闸门开后才聚合 feedback)
- [04-try-compile-fallback](./04-try-compile-fallback.md)(状态机,considerPromotion 调用本文 aggregator)
- [05-p3-p4-interface](./05-p3-p4-interface.md)(P3 可选用 / P4 核心用 feedback 的接口契约)
- [06-testing-strategy](./06-testing-strategy.md)(IC 反馈聚合的合成脚本测试 + confidence 计算单测)
- [../p2-bridge/00-overview](./00-overview.md) §3(本文的种子,深度展开)
- [../p1-interpreter/02-bytecode-isa.md](../p1-interpreter/02-bytecode-isa.md) §7(ICSlot 结构 + 算术 IC 字段挪用登记)
- [../p1-interpreter/05-interpreter-loop.md](../p1-interpreter/05-interpreter-loop.md) §6(IC 执行机制定稿,§6.4 算术 IC 写不读契约)
- [../p1-interpreter/01-value-object-model.md](../p1-interpreter/01-value-object-model.md) §3(NaN-box 数字识别,算术快路径基石)
- [../p1-interpreter/implementation-progress.md](../p1-interpreter/implementation-progress.md)(P1 IC 已落地形态对账)
- [../p4-method-jit](../p4-method-jit/00-overview.md)(投机消费方 / confidence 决定激进度的下游)
- [../roadmap.md](../roadmap.md) §3(编译层是纯增量) / §5(原则 4 fallback 与投机分水岭可调)


