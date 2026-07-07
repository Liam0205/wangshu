# P2:热度计数子系统(采样点 / ProfileData / 阈值 / 多 State 并发归属)

> 状态:**设计阶段,详细设计已齐备**。本文是 P2「分层桥」的**计数单一事实源**(承 [00-overview](./00-overview.md) §0 文档地图):
> ① 回边/入口采样点的选择论证(承 [../p1-interpreter/02-bytecode-isa](../p1-interpreter/02-bytecode-isa.md) §4 的「热点回边」声明);
> ② `ProfileData` 字段级 spec(挂哪、装什么、生命期);
> ③ 计数路线 A(profile counter 伪指令)vs 路线 B(旁路计数)抉择 —— **定稿 B**;
> ④ 热度阈值与编译预算 pacing 的设计;
> ⑤ **多 State 并发 profile 归属**(挂 State 私有 vs Proto 共享,定稿挂 State 私有 + Proto 旁聚合表占位)。
>
> 上游契约:[00-overview](./00-overview.md)(§0 文档地图、§1 P1/P2/P3 边界、§3 关键耦合点 2/6、§6 决策速查表、§7 P1 前瞻义务对账、§9 风险);
> P1 依赖面:[../p1-interpreter/02-bytecode-isa](../p1-interpreter/02-bytecode-isa.md) §4(FORLOOP 回边、JMP 回跳、38..63 预留)、
> [../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md) §10.1(FORLOOP 执行侧)、§1.4(`enterLuaFrame` 入口钩)、§6.4(算术 IC「P1 写不读」纪律,本文不直接消费但同源决策)、
> [../p1-interpreter/01-value-object-model](../p1-interpreter/01-value-object-model.md) §1(Proto 住 Go 堆,与 ProfileData 同生命期论证)、
> [../p1-interpreter/11-embedding-arena-abi](../p1-interpreter/11-embedding-arena-abi.md) §1.4 / §8(多 State 并发语义,profile 归属的并发决策依据)。

对应 Go 包:`internal/bridge`(本子系统是其首批实现 —— [00-overview](./00-overview.md) §4 PB0/PB1)。

---

## 0. 本文在 P2 中的位置:基建里的「时钟」

[00-overview](./00-overview.md) §1 一句话定位 P2:**「P1 产料(IC 写、采样点、AST),P2 加工成决策(热度、feedback、可编译性),P3 消费决策去加速。P2 是中间的『决策加工厂』,自己不进热路径。」**——计数子系统就是这台决策加工厂的**输入采集层**:它把 P1 解释器执行期间散落的「这条回边又转了一圈」「这个函数又被调用了」事件累积成「这个 Proto 总共多热」的判断,喂给 [04-try-compile-fallback](./04-try-compile-fallback.md) 的状态机做升层决策。

打个不太严谨的比方:整个 P2 像一台带温控的工厂,**计数器是工厂里的时钟**——没有它,P2 不知道什么时候该让 P3 启动编译;但**时钟自己不生产任何东西**,它只在那里走着。这与 P2 整体「不加速、不在热路径」的定位完全一致([00-overview](./00-overview.md) §1)。

### 0.1 三条不变式(其余各节都在兑现这三条)

承 [00-overview](./00-overview.md) §1 的 P1/P2/P3 边界表与 §6 决策速查表,本子系统要在每一处实现细节里兑现:

1. **P2 不加速、不在热路径**——计数自增本身要尽量便宜(快路径若被计数拖慢就违反了),且要可被编译期常量关掉(P1-only 部署时零开销)。
2. **字节码不可动**——计数不能改 P1 的 0..37 字节码序号或语义,差分基准、黄金字节码([../p1-interpreter/12-testing-difftest](../p1-interpreter/12-testing-difftest.md) §2.5 寄存器分配同构)不允许因 P2 启用而漂移。
3. **接口稳定(填占位不改公共 API)**——P2 不改 [../p1-interpreter/11-embedding-arena-abi](../p1-interpreter/11-embedding-arena-abi.md) 的 `wangshu.go` 公共门面;`vm.profileEnabled` 翻 true 是内部行为切换,宿主代码 P1→P2 升级零修改。

### 0.2 与下游的接口(本文产出什么)

本文不直接给 P3/P4 接口(那是 [05-p3-p4-interface](./05-p3-p4-interface.md) 的事),只给「计数累积越阈值时往下游推什么」:

```
计数累积越阈值
       │
       ▼
b.considerPromotion(proto, pd)   ← §4 推到状态机入口
       │
       ▼
  [04-try-compile-fallback] §3:查 compilable → 试编译 → 升层 / 永久解释
```

「`considerPromotion` 函数签名」由 [04-try-compile-fallback](./04-try-compile-fallback.md) 定稿;本文只承诺「在合适的时点调它,且不重复调用、不在 TierStuck/TierGibbous 状态下调用」(§4.3 守卫纪律)。

### 0.3 与 P2 单文件原稿 §2 的关系

[../p2-bridge/00-overview](./00-overview.md) §2 是 P2 单文件原稿里的「函数级热度计数」节,本文是它的**详细设计扩展**(承 [00-overview](./00-overview.md) §0 的「计数单一事实源」分工)。两文档的关系:

| 内容 | 单文件原稿 §2 | 本详细设计 |
|---|---|---|
| 采样点论证 | §2.1 简版(回边 vs 总指令数 + 入口) | §1 全展开(§1.2 三条贵+脏论证、§1.3 入口热点漏点示例、§1.4 候选汇总表) |
| ProfileData 字段 | §2.2 给基本字段 | §2.2 字段级 + §2.3 稠密 vs 稀疏决策 + §2.4 生命期对账 |
| 路线 A/B | §2.3 决策(选 B) | §3 全展开(§3.1 A 优劣 + §3.2 B 优劣 + §3.3 对比表 + §3.4 三条决定性理由 + §3.5 P1 接入点) |
| 阈值 | §2.4 给常量 | §5 数值论证(prior art 对照)+ §5.3 不影响正确性论证 + §5.4 pacing 设计 |
| 多 State 并发 | §9 末尾留缺口(倾向私有) | §6 全定稿(§6.2 候选三表 + §6.3 (B) 三条理由 + §6.4 (C) 增强方案占位 + §6.5 实现) |
| 实现代码骨架 | (无) | §4 onBackEdge / onEnter / considerPromotion 契约,可直接照抄 |

**单文件原稿 §2 = 论证脉络 + 决策方向**;**本文 = 字段级 + 代码骨架 + 并发决策定稿 + 实现契约**。两者并存,后续维护以本详细设计为准(单文件原稿在 P2 总览替代后退役为参考)。

---

## 1. 采样点选择:回边 + 函数入口(承 [02 §4](../p1-interpreter/02-bytecode-isa.md))

### 1.1 候选信号空间

要判定「这个 Proto 热不热」,可观测的信号有:

| 候选 | 含义 | P1 暴露成本 |
|---|---|---|
| 总指令数 | 该 Proto 已执行的指令条数 | 每条指令一次自增(`pd.totalInsns++`) |
| **回边计数(选定 A)** | `FORLOOP` 成功回跳 + 循环体内向后 `JMP` 的次数 | 仅在回边成功时一次自增 |
| **入口计数(选定 B)** | 该 Proto 被作为 Lua 帧进入(`enterLuaFrame`)的次数 | 进帧时一次自增 |
| 出口计数 | RETURN 次数 | 与入口对偶,无独立信号(= 入口 - 异常退出) |
| 时间(wall-clock) | 该 Proto 累计执行时间 | 进出帧 timestamp 差,昂贵且不稳定 |
| 缓存未命中 | IC miss 比例 | 与「热度」正交(冷的可能 miss 率高) |

回边 + 入口是 P2 的最小完备集(§1.4 论证),其余信号要么贵(总指令数、时间)、要么不直接对应「函数热度」(IC miss)。

### 1.2 为什么不数总指令数

回边 vs 总指令数是 P2 计数选型的第一道分水岭。**用一句话定结论:回边是「循环又转了一圈」的高质量信号,总指令数是噪声含量极高的低质量信号。** 拆开论证:

#### 1.2.1 总指令数信噪比低

绝大多数 CPU 时间花在循环里(列内核负载尤甚,roadmap §1 的 Horner 形状就是一个紧循环)。「直线代码」的指令也被计入总数,会把「执行了一次大段初始化」误算成「热」——但初始化只跑一次,编译它没意义。

举例:一个 1000 行直线赋值的 `init` 函数 + 一个 5 行 for 循环 1000 次的核心函数。从总指令数看,init 占比可能比核心更高,却完全不该被编译;**回边计数让 init 永远是 0**(无回边),核心函数累积 1000 次,信号即决策。

#### 1.2.2 总指令数自增的开销不可忽略

每条指令一次自增意味着主循环([../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md) §2.3 的取指→译码→执行→safepoint 骨架)在每一轮都要多做一件事:`pd.totalInsns++`。对快路径(MOVE/算术/比较/JMP/FORLOOP 等不分配指令,占绝大多数)而言,这是相对它们当前「单次切片索引 + 一次 f64 运算」的常数级别开销 —— **并不可忽略**(05 §3 把每指令开销压到这一档是 ≥2x over gopher-lua 的关键)。

回边计数则只在「FORLOOP 成功回跳」与「循环体内 JMP 向后」时各自增一次。一个 1000 次的紧循环,前者是 1000 次回边自增;后者(while/repeat)同。直线代码零自增。**信号密度高、附带开销低**。

#### 1.2.3 prior art 全选回边

LuaJIT、V8 Ignition、JSC Baseline 都用 loop back-edge + 调用计数做热度,不数总指令——这是 prior art 的成熟选择(roadmap §7 阶梯参考)。望舒沿用,无独立创新。

> **小结**:**总指令数这个候选直接淘汰**——既贵又脏。下文不再回到它,但记入 §1.4 表用作参照。

### 1.3 为什么需要入口计数(回边不够)

只数回边会漏掉一类典型热点:**被外层循环每轮调用的小函数**。

这类函数自身**不含**循环(或循环很小),但**被反复调用**。例子:

```lua
local function score(x) return x*x + 1 end          -- 无循环,被外层每轮调用
for i=1,n do total = total + score(arr[i]) end       -- score 被 n 次调用
```

`score` 自身回边为零,只数回边永远不升层,但它确实是热点(被 n 次调用)。补一个**函数入口计数**,让「调用 N 次」也能触发升层,堵掉这个漏洞。

入口计数的自增点是 [05 §1.4](../p1-interpreter/05-interpreter-loop.md) 的 `enterLuaFrame`(每次进入 Lua 帧时调一次),与回边正交、互不重复。**两个计数器,两类热点**:

| 计数器 | 自增点 | 捕捉的热点形式 | 对应 opcode([02 §4](../p1-interpreter/02-bytecode-isa.md)) |
|---|---|---|---|
| **back-edge 计数** | 每次循环回跳 | 「函数内有热循环」 | `FORLOOP` 成功回跳;循环体内向后 `JMP`;`TFORLOOP` 后回跳 JMP |
| **入口计数** | 每次进入该函数帧 | 「函数被反复调用」 | `CALL`/`TAILCALL` 进入 Lua 帧时(05 §7.1 / §1.4 `enterLuaFrame`) |

> **TFORLOOP 的回边归在 JMP**:[02 §4](../p1-interpreter/02-bytecode-isa.md) `TFORLOOP` 自身不回跳 —— 它根据迭代器返回首值是否 nil 决定 `pc++`(退出)还是落到紧随的 `JMP`(回跳)。所以泛型 for 的回边采样点是它后面的那条 JMP,与「循环体内向后 JMP」归一类,无需新增 opcode 钩点。

### 1.4 候选汇总表与最终选定

承 [00-overview](./00-overview.md) §6 决策表「计数路线」一行(那行讲的是 A/B 路线,§3 详述);本表是「**采样点信号**」的选型记录:

| 信号 | 信噪比 | P1 暴露成本 | 是否选用 | 理由 |
|---|---|---|---|---|
| 总指令数 | 低 | 高(每指令一次) | ❌ | §1.2 全部三条 |
| **回边计数** | 高 | 低(仅回边时) | ✅ | §1.2 prior art / 信号密度高 |
| **入口计数** | 高(对小函数) | 低(仅进帧时) | ✅ | §1.3 堵漏:被反复调用的小函数 |
| 出口计数 | 与入口对偶 | 低 | ❌ | 信息冗余(= 入口 - 异常) |
| 时间 | 高但不稳定 | 高(timestamp) | ❌ | 测量噪声大、wall-clock 受调度影响,且我们走「决策正确」非「性能最大化」([00-overview](./00-overview.md) §8) |
| IC miss | 与热度正交 | 低 | ❌ | 信号语义错位:miss 率高不代表函数热,反而可能是冷的多态点 |

**定稿:回边 + 入口,二者皆要**。其余信号在 P2 阶段不采集;若未来出现「真热点没被这两个信号捕捉」的实证案例,再考虑补(留 §8 缺口)。

### 1.5 prior art 对照(承 [../p2-bridge/00-overview](./00-overview.md) §2.1)

为坚定信心,把成熟 VM 的热度计数选择列出来对照(roadmap §7 prior art 阶梯参考):

| VM | 回边 | 入口 | 总指令 | 时间 | 备注 |
|---|---|---|---|---|---|
| LuaJIT(2.x) | ✅ `hotloop` | ✅ `hotcall` | ❌ | ❌ | 阈值默认 56/56,可调;trace 录制起点是回边 |
| V8 Ignition→Sparkplug | ✅(OSR 触发) | ✅(invocation count) | ❌ | ❌ | tier-up 由两者共同推进 |
| JSC Baseline→DFG | ✅ | ✅(execution count) | ❌ | ❌ | 类似 V8 |
| HotSpot C1→C2 | ✅(BackedgeCounter) | ✅(InvocationCounter) | ❌ | ❌ | 阈值数千~数万 |
| **望舒 P2** | ✅ HotBackEdgeThreshold=1000 | ✅ HotEntryThreshold=200 | ❌ | ❌ | (§5.1) |

**全部主流 VM 都用「回边 + 入口」组合,且都不数总指令、都不用 wall-clock**。望舒沿用此选择,无独立创新 —— 这一项不是设计博弈点,而是工程通识。

---

## 2. ProfileData 字段级 spec(挂哪 / 装什么 / 怎么读)

### 2.1 候选存储位置

热度计数器有几个候选挂载位置,**核心选型是「Proto 旁 vs State 私有」**(本节)与「Proto 旁 vs CallInfo 帧级」(次要,先排除)。先排除次要的:

| 候选 | 存储 | 累积语义 | P2 主决策需要吗 |
|---|---|---|---|
| **Proto 旁 ProfileData**(主存储) | Go 堆,与 Proto 同生命周期(01 §1) | **跨调用累积**:函数级聚合 | ✅ 升层是函数级的 |
| **CallInfo 帧级计数** | arena(CallInfo 数组,05 §1.2) | **单次调用内**(帧活跃期),帧退出销毁 | ❌ 不跨调用累积 |

**先排除 CallInfo 帧级**:升层决策是函数级的(「把这个 Proto 升到 gibbous」),判据应是「这个 Proto 历史上总共多热」,而非「这一次调用多热」——累积语义匹配 Proto 级存储。CallInfo 是 per-call 临时记录(05 §1.2 标 32 字节,词典里的字段没有热度计数位),不为累积量服务。**P2 主决策不需要 CallInfo 帧级计数**——若未来「分析单次长循环」场景需要再补(留 §8 缺口)。

剩下的核心选型:Proto 旁 vs State 私有 —— 见 §6 详述(并发决策的核心)。**本节先按「逻辑挂 Proto」给字段 spec,§6 把『物理存储』搬到 State 私有 profileTable**(挂 Proto 是单 State 视角下的简化模型,字段不变)。

### 2.2 ProfileData 字段定义

```go
// internal/bridge —— 一个 Proto 的画像数据(物理存储位置见 §6:State 私有 profileTable)
//
// 设计要点:
//   - 不进 arena:计数是函数级聚合,与不可变代码同住 Go 堆(01 §1);无 GC 干扰
//     (这一项与 Proto 自身住 Go 堆一致,二者生命周期对齐)。
//   - 按 pc 索引回边计数(稀疏:只有回边 pc 有值)——避免与 [02 §7] 的 IC slot 数组混淆,
//     IC 是值加速、profile 是热度,二者按 pc 索引但语义正交,分两个数组。
//   - tierState 与 compilable 同住:升层决策入口 considerPromotion(§4) 一处取齐所需信号,
//     无需跨多个表查找。
type ProfileData struct {
    // —— 计数器(§1) ——
    entryCount uint32   // 函数入口计数:每次 enterLuaFrame 自增(§1.3 的入口热点形式)
    backEdge   []uint32 // 按回边 pc 索引的回跳计数(稀疏数组,§2.3 spar/dense 决策)

    // —— 状态机(详见 [04-try-compile-fallback] §2) ——
    tierState TierState // TierInterp / TierGibbous / TierStuck

    // —— 升层决策辅助 ——
    compileTried bool          // 是否已尝试过编译(防 TierStuck 反复重试,详见 [04] §3)
    compilable   Compilability // 可编译性判定缓存(详见 [03-compilability-analysis] §2)
                                // 初值 CompUnknown(P1 Compile 占位,11 §1.3),
                                // 首次 considerPromotion 时通过 [03] 的 analyzeProto 填实
    reasons      uint8         // CompNotCompilable 时的拒因位掩码(F1=0x01 / F2=0x02 / ...,详见 [03 §3])
                                // 用于 [04 §6.2](./04-try-compile-fallback.md) "stays interpreted (not compilable: F<n>)" 日志
                                // 与 PB7 验收 V1-V7 的归因诊断;CompCompilable / CompUnknown 时为 0
}

// TierState / Compilability 的定义在 [04] / [03],本文只用其符号常量。
```

### 2.3 backEdge 数组的稀疏 vs 稠密

`backEdge` 按 pc 索引,但 P1 字节码里**只有少数 pc 是回边**(FORLOOP、循环体回跳 JMP、TFORLOOP 后 JMP),其余位置无值。两种实现:

| 方案 | 内存占用 | 自增开销 | 选用 |
|---|---|---|---|
| **稠密 `[]uint32`**(长度 = `len(Proto.Code)`) | O(代码长度) ✕ 4 字节 / Proto | `pd.backEdge[pc]++` 一次切片索引 | ✅ |
| **稀疏 `map[int32]uint32`** | O(回边数) ✕ ~32 字节 / entry | 哈希查找 + 写,数倍于切片 | ❌ |

**定稿稠密**:① 自增是热路径,稀疏 map 的哈希查找会拖慢 onBackEdge(§4.1);② Proto 体不大(MaxCompilableInsns 阈值会限制函数大小,详见 [03] §3 F5),稠密数组的内存浪费可接受;③ 切片索引可被 Go 编译器寄存器化,与解释器主循环的 `f.stk[f.base+a]` 同档开销(05 §1.3)。

> **延迟分配**:`backEdge` 数组在 ProfileData 初始化时**不**分配 —— 只在第一次 onBackEdge 命中该 Proto 时按 `len(Proto.Code)` 一次性分配。这避免了「冷 Proto 永远没有回边、却为它预留了一个数组」的浪费;实测若有相反场景再调。

### 2.4 ProfileData 与 Proto 的生命期对账

[01 §1](../p1-interpreter/01-value-object-model.md) 表:**Proto 住 Go 堆**(不可变代码);**ProfileData 也住 Go 堆**(就是普通 Go 结构体,被 State 私有 profileTable 持有,详见 §6)。两者:

- **同生命期** —— Proto 在 Program 生命期内不变,ProfileData 在 State 生命期内不变(State 销毁则 profileTable 销毁,与 State arena 一同丢弃)。
- **不进 arena、不进 GC** —— 都是 Go 堆裸结构,Go GC 看 profileTable 是普通 map/slice,自然回收;**不参与 mark-sweep 根集合**([../p1-interpreter/06-memory-gc](../p1-interpreter/06-memory-gc.md) §5.1 R1..R9 都不含 ProfileData)。
- **零跨界共享** —— ProfileData 不被 P3/P4 编译码读(P3/P4 读的是 `TypeFeedback`,即 [02-ic-feedback](./02-ic-feedback.md) 的产物,不是 ProfileData)。计数器是「内部决策状态」,不跨层。

这条对账是「P2 不加速、不在热路径」(§0.1 不变式 1)的物理体现:计数数据不进任何执行层的内存视图,纯属决策机的内部账本。

### 2.5 计数信号在升层后的去向

设计的完整性要求:升层后这些计数器**怎么处理**?候选:

| 选项 | 处理方式 | 利弊 |
|---|---|---|
| **(a)** 升 Gibbous 后停计但保留(选定) | 不再 onBackEdge/onEnter(P3 trampoline 接管),计数定格 | 留作诊断(`promoted to gibbous` 日志可附带「累计 N 次回边」) |
| (b) 升 Gibbous 后清零 | 释放 backEdge slice 内存 | 内存优化但失去诊断价值;且 backEdge 数组不大,清不清差别小 |
| (c) 升 Gibbous 后继续计 | gibbous 代码也调 onBackEdge | 违反「P2 不在热路径」—— gibbous 代码再调 P2 计数会拖慢编译后路径 |

**定稿 (a)**:

- onBackEdge 在 `tierState != TierInterp` 时 return(§4.1 守卫),自然不再累计 —— 这本身就是 (a) 行为。
- backEdge 数组保留(不清),为升层日志提供「累计 N 次回边」的诊断信息(详见 [04 §6](./04-try-compile-fallback.md) 升层日志格式)。
- gibbous 代码不调 P2(P3 trampoline 接管,P3 §5)—— (c) 自动避免。

> **TierStuck 后的计数处理**:同 (a),onBackEdge 守卫直接 return,backEdge 保留(虽然 Stuck 不再升层,但保留 backEdge 让诊断工具能看到「这个 Proto 累积了多少热度但被判 Stuck」,有助于「F2 协程保守判定漏判面」之类的实测分析,详见 [03 §3 F2](./03-compilability-analysis.md))。

---

## 3. 计数路线 A vs B(承 [02 §4](../p1-interpreter/02-bytecode-isa.md) 38..63 预留)

[02 §4](../p1-interpreter/02-bytecode-isa.md) 末:**「编号 38..63 预留:P2/P3 可能新增 tier guard、profile counter 等伪指令」**。计数自增有两种实现路线,**这是 P2 的核心字节码层决策**——决定 38..63 在 P2 阶段是否被占用,进而决定 P1/P2/P3 的字节码兼容矩阵。

### 3.1 路线 A:profile counter 伪指令(占用 38..63 一个 opcode)

codegen(或 P2 上线时的字节码改写)在每个回边前插一条 `PROFILE_BACKEDGE`(假想 opcode #38),解释器执行它时自增计数 + 检查阈值:

```
L0: ... 循环体 ...
    PROFILE_BACKEDGE  R_x  -> threshold_handler   ; 伪指令:自增 + 越阈值则触发升层检查
L1: FORLOOP R2 -> L0
```

**优点**:

1. **计数逻辑显式在字节码里** —— dispatch 时自然执行,阈值检查点明确;
2. **与 P3 翻译对齐** —— P3 把字节码翻成 Wasm 时,这条伪指令翻成「Wasm 里的回边计数 + 抢占检查」(详见 P3 §6 回边检查点),P1 的伪指令位置正好是 P3 要插检查点的位置;
3. **覆盖一致** —— 所有回边都被字节码层标记,无遗漏。

**代价**:

1. **改字节码 = 改 Proto 形状** —— 影响 [02 §8](../p1-interpreter/02-bytecode-isa.md) 的黄金字节码([../p1-interpreter/12-testing-difftest](../p1-interpreter/12-testing-difftest.md) §2.5 差分测试官方 luac 的寄存器分配同构),需要 P2 的字节码变体与 P1 基线区分(差分 harness 要能跑「带 profile 伪指令」与「不带」两种 Proto 都 byte-equal 于解释结果);
2. **每回边多一条 dispatch** —— 即便冷函数也付这个税(冷函数也要 dispatch 这条伪指令);
3. **开关切换的字节码生成时机** —— 是 codegen 时插还是 P2 启动时改写?前者让 P1-only 部署也带这条指令(浪费 dispatch);后者要写一套字节码改写器(再多一个 bug 面)。

### 3.2 路线 B:旁路计数(解释器在执行侧直接自增,不改字节码)

解释器执行 `FORLOOP` 成功回跳时,顺手 `b.onBackEdge(proto, pc)`,**不插任何伪指令**:

```go
// crescent 的 FORLOOP 执行侧(05 §10.1)末尾,P2 启用时追加:
case bytecode.FORLOOP:
    // ...(05 §10.1 的加 step、判界、回跳逻辑)...
    if cont {                                  // 回跳成功
        f.stk[f.base+a]   = value.NumberValue(idx)
        f.stk[f.base+a+3] = value.NumberValue(idx)
        f.pc += SBx(i)
        if vm.profileEnabled {                 // P2 旁路计数(P1-only 时编译期关掉,零开销)
            vm.bridge.onBackEdge(f.proto, f.pc) // 自增 + 阈值检查(§4.1)
        }
    }
```

**优点**:

1. **不改字节码** —— Proto 形状与 P1 基线完全一致,黄金字节码、寄存器分配同构、差分差分测试 luac 全部不受影响([../p1-interpreter/12-testing-difftest](../p1-interpreter/12-testing-difftest.md) §2.5);
2. **零开销可关** —— `vm.profileEnabled` 编译期常量(或 build tag)让 P1-only 部署完全不付计数税,符合 roadmap §5 原则 3「每阶段独立交付」(P1 不被 P2 拖累);
3. **38..63 不占用** —— P2 阶段保持全空,留给 P3 视需要使用(`TIER_GUARD` 等)。这是「预留即克制」——能不占 opcode 就不占;
4. **粒度可调** —— 旁路逻辑写在 Go,改阈值/改采样策略不重新生成字节码。

**代价**:

1. **计数逻辑藏在解释器执行侧** —— 不在字节码里显式可见,P3 翻译时回边检查点要**另行**在「翻译 FORLOOP」时插入(但 P3 本来就要按 roadmap §2 在回边插抢占检查点,见 P3 §6,所以这不是额外负担);
2. **多一层条件分支** —— `if vm.profileEnabled` 在编译期常量假设下被 Go 编译器消去(`profileEnabled` 是 `const` 时整个 if 块消失);若是运行期开关则有一次预测良好的分支(永远 true 或永远 false,BTB 几乎 100% 命中)。

### 3.3 决策对比表

| 维度 | 路线 A(伪指令) | 路线 B(旁路) | 取舍 |
|---|---|---|---|
| 字节码改动 | 占 38..63 一个 opcode | **不改** | B 胜(§0.1 不变式 2) |
| 黄金字节码影响 | 与 P1 基线分叉,需双轨差分 | **零影响** | B 胜 |
| 冷函数开销 | 每回边一次 dispatch | **零**(条件常量假) | B 胜 |
| 热函数自增开销 | 一次 dispatch + 一次自增 | 一次条件 + 一次函数调用 + 一次自增 | 接近(B 略贵于 A 的纯自增,但有 inlining 余量) |
| 与 P3 对齐 | 伪指令与 P3 检查点同位 | P3 自己在翻译 FORLOOP 时插 | 平局(P3 §6 不依赖 P1 先插) |
| 关闭开关 | 需要重新生成字节码 | **`profileEnabled = false`** | B 胜 |
| 38..63 用途 | 占用一个 | **保留全空** | B 胜(留给 P3) |
| 实现复杂度 | codegen + 解释器双改 | 仅解释器改 | B 胜 |

### 3.4 定稿:路线 B,A 留给 P3 备选

**P2 选路线 B(旁路计数),38..63 在 P2 阶段保持全空,留 A 作为 P3 的可选手段。** 三条决定性理由:

1. **字节码不可动是硬约束的延伸** —— [02 §4](../p1-interpreter/02-bytecode-isa.md) 强调「38..63 预留**不占用 0..37**,保证 P1 字节码向后兼容上层翻译」。旁路计数让 P1 的 0..37 字节码**一字不改**就能被 P2 计数,最大化向后兼容;伪指令路线则引入一个「P2 字节码变体」,增加差分维度。
2. **零开销可关** —— `vm.profileEnabled` 编译期常量(或 build tag)让 P1-only 部署完全不付计数税,符合 roadmap §5 原则 3「每阶段独立交付」。
3. **P3 的回边检查点是独立需求** —— P3 无论如何都要在回边插 GC 抢占检查点(roadmap §2 异步抢占税解法),那是 P3 翻译 FORLOOP 时做的事(P3 §6),不依赖 P1 先插伪指令。所以伪指令的「与 P3 对齐」优势其实可有可无。

> **38..63 的最终用途**(承 [00-overview](./00-overview.md) §7 P1 前瞻义务对账第 4 行):**P2 旁路计数不占用任何 opcode**。38..63 留给 P3 的 `TIER_GUARD`(若 P3 需要在字节码层标记「此点已编译,跳到 gibbous」——但 P3 的 trampoline 倾向于在 Proto 元数据里记 tier 而非插字节码,见 P3 §5)。**38..63 在 P2 阶段保持全空,P3 视需要启用**——这与 [../p1-interpreter/00-overview](../p1-interpreter/00-overview.md) §5 项 2「opcode 38..63 预留」前瞻义务对账一致(P2 不消费此预留)。

### 3.5 路线 B 与 P1 的接入点(承 [00-overview](./00-overview.md) §3 关键耦合点 2)

**P1 当前实现是「关」**(`vm.profileEnabled = false`),P2 PB0 启动时翻成「开」并实现 `bridge.onBackEdge`/`onEnter` 回调。这是「每阶段独立交付」(原则 3)在 P1↔P2 边界的物理兑现:

| 阶段 | profileEnabled | onBackEdge / onEnter | 字节码 |
|---|---|---|---|
| **P1**(已完成) | `false`(编译期 const 或 build tag) | 空函数(占位) | 0..37 不变 |
| **P2 PB0+**(本文) | `true` | 实现(本文 §4) | 0..37 不变(B 路线) |

切换不改公共 API、不改字节码、不改 Proto 形状 —— 只是 `internal/crescent` 与 `internal/bridge` 之间的内部接线。验收口径:**「P1-only 部署关 profileEnabled 仍 byte-equal(差分回归);P2 启用 profileEnabled 计数累积非零」**(对应 [00-overview](./00-overview.md) §4 PB0 验收第 1/2 项)。

---

## 4. onBackEdge / onEnter 实现细节(代码骨架)

本节给计数自增 + 阈值判定的 Go 代码骨架。骨架可直接照抄 PB0/PB1 实现;实测发现性能问题再调。

### 4.1 onBackEdge 代码骨架

```go
// internal/bridge —— 回边采样钩,P1 的 FORLOOP 执行侧调用(见 §3.5)。
// 调用契约:
//   - 仅在 vm.profileEnabled == true 时被调(否则 if 整块在编译期消去)。
//   - proto 是当前帧的 Proto(05 §1.3 frame.proto)。
//   - pc 是回边目标 pc(已 += SBx 后的值,即「回跳到的指令」的下标)。
//   - 调用方持有 frame 局部缓存(stk/k/ic),本函数不重载 stk(无分配)。
func (b *Bridge) onBackEdge(proto *bytecode.Proto, pc int32) {
    pd := b.profileOf(proto)            // §6:从 State 私有 profileTable 取(或惰性建)
    if pd.tierState != TierInterp {     // 守卫:已升 Gibbous 或已卡 Stuck,无需再计数
        return                          //   (Gibbous 走 P3 trampoline,本钩根本不会被调;
                                        //    Stuck 是终态,不重试,§6.3 [04])
    }
    // 延迟分配 backEdge 数组(§2.3):首次命中该 Proto 时按 Code 长度一次性分配
    if pd.backEdge == nil {
        pd.backEdge = make([]uint32, len(proto.Code))
    }
    pd.backEdge[pc]++

    // 任一回边累计越阈值 ⇒ 该 Proto 候选升层(§5)
    // 单回边越阈值近似「函数热」(§5.3),不必每次求和所有回边
    if pd.backEdge[pc] >= HotBackEdgeThreshold {
        b.considerPromotion(proto, pd)  // 推到 [04-try-compile-fallback] 状态机
    }
}
```

要点:

- **TierInterp 守卫**:`pd.tierState != TierInterp` 立即返回。这一行守卫覆盖三种情形:
  - `TierGibbous`:Proto 已编译,P3 trampoline 接管执行,本钩根本不会再被 P1 解释器调用(防御性守卫,实际不会触发,但守着无害)。
  - `TierStuck`:不可编译或编译失败的终态,**不再尝试升层**——继续累积计数无意义(信号不会被消费),但简单计数本身无害(只是浪费一次自增)。守卫让 Stuck 的 Proto 直接 return,**节省自增开销**——这一项实测下来对「大量 Stuck Proto 在热循环里被回调」的场景有意义(详见 §5.4 阈值不影响正确性论证)。
  - 极少数边角:升层进行中(P3 编译中,详见 [04] §3 的事务态)—— [04] 定稿用 `tierState` 切到 Stuck/Gibbous 之后才解锁,中间状态不进入本函数。
- **延迟分配**:`pd.backEdge == nil` 时才分配,避免冷 Proto 浪费内存(§2.3)。第一次分配是一次性的,后续同 Proto 再调直接走切片索引快路径。
- **避免重复触发**:**没有显式 `compileTried` 检查**——靠 `tierState` 的转移(`TierInterp → TierGibbous/TierStuck`)保住「成功一次就不再走 considerPromotion」。`considerPromotion` 内部还会再守一层(详见 [04] §3 的状态机入口),双重守卫防抖。

### 4.2 onEnter 代码骨架

```go
// internal/bridge —— 函数入口采样钩,[05 §1.4] enterLuaFrame 调用。
// 调用契约:
//   - 仅在 vm.profileEnabled == true 时被调。
//   - 在 ensureStack 后、形参 nil 填充后、压 CallInfo 前后均可,定稿:在 enterLuaFrame
//     完成后(CallInfo 已压、frame 已重载)的最后一步调,与 onBackEdge 的「回边后」对齐。
func (b *Bridge) onEnter(proto *bytecode.Proto) {
    pd := b.profileOf(proto)
    if pd.tierState != TierInterp {
        return
    }
    pd.entryCount++
    if pd.entryCount >= HotEntryThreshold {
        b.considerPromotion(proto, pd)
    }
}
```

要点:

- 与 onBackEdge **结构对称**:同样的 TierInterp 守卫、同样的「越阈值则推到 considerPromotion」。
- **没有 backEdge 数组那种延迟分配** —— `entryCount` 是 ProfileData 里的标量字段,无需延迟。
- **不区分调用形式**:CALL 与 TAILCALL 都经 `enterLuaFrame`(05 §1.4 与 §7.5),都计入 entryCount。这是有意为之 —— 尾调用替换帧但仍是「一次函数被启动」的事件,语义上算一次入口;且 TAILCALL 不增 CallInfo 但仍重载 proto/cl(05 §7.5 doTailCall 末尾的 `f.proto, f.cl, f.pc = proto, cl, 0; reloadFrame(f)`),自然进入 enterLuaFrame 的钩点。

### 4.3 considerPromotion 在本文的契约

`considerPromotion` 的实现属于 [04-try-compile-fallback](./04-try-compile-fallback.md) §3,本文只承诺**调用契约**:

```go
// 由 [04-try-compile-fallback] 实现;本文给契约,不给实现。
func (b *Bridge) considerPromotion(proto *bytecode.Proto, pd *ProfileData)
```

契约:

1. **幂等**:多次调用不出错 —— 即便 onBackEdge 与 onEnter 在同一指令窗口里都越阈值并各自调一次,considerPromotion 内部用 `pd.tierState != TierInterp` 守卫(详见 [04] §3),第二次调用直接 no-op。
2. **不重载 frame**:considerPromotion 是 P2 内部决策机,**不分配新对象**(分析+查可编译性+请 P3 编译,后两项可能分配但归 [03]/[05]),也不动当前 frame 的 stk/k/ic。本钩调用方不需要 reloadFrame。
3. **不在热路径**:即便它分配/调 P3 编译(可能慢,数百 µs),也只在「越阈值的临界点」发生 —— 阈值默认 1000 次回边后才一次,**摊薄到每回边几十 ns**(实测后调阈值)。
4. **无返回值**:升层成功/失败/不可编译都通过修改 `pd.tierState` 表达,onBackEdge/onEnter 调用方不读返回值。

### 4.4 钩点的接入位置(对 [05 §10.1 / §1.4] 的回填关系)

[00-overview](./00-overview.md) §3 关键耦合点 2 与 §7 P1 前瞻义务对账行 2/3 已说明:**P1 已完成两处钩点**(`vm.profileEnabled` 当前 false 占位,P2 翻 true 即生效):

| 钩点 | P1 完成状态 | P2 接入 |
|---|---|---|
| FORLOOP 回边(05 §10.1) | 字节码层已暴露;执行侧已可挂钩;`vm.profileEnabled` 当前 false | 翻 true + 实现 onBackEdge |
| 循环体内 JMP 回跳 | 同 FORLOOP(JMP 在 05 §2.3 主循环内,执行侧加同样的 if profileEnabled 守卫) | 同上 |
| TFORLOOP 后 JMP 回跳 | 同 JMP 一类 | 同上 |
| `enterLuaFrame`(05 §1.4) | 已完成;P1 内未实际计数(占位) | 实现 onEnter |
| TAILCALL 中的 frame 重载(05 §7.5) | 已完成(末尾 reloadFrame) | 进入 enterLuaFrame 的等价路径 —— 在 doTailCall 末尾也调一次 onEnter(本文要求) |

> **TAILCALL 路径的回填请求**:onEnter 在 TAILCALL 上需被调用 —— 但 [05 §7.5](../p1-interpreter/05-interpreter-loop.md) doTailCall 末尾**未直接调 enterLuaFrame**(它原地改 CallInfo + 重载 frame,绕过 enterLuaFrame)。**这是本文对 P1 的潜在回填请求**(详见 §9 缺口节)。**预期解决方案**:在 doTailCall 末尾的 `reloadFrame(f)` 之后追加 `if vm.profileEnabled { vm.bridge.onEnter(f.proto) }`,与 enterLuaFrame 的钩点对齐。**目前不主动改 [05]**,等 PB0/PB1 实现时一并完成;若 [00-overview](./00-overview.md) §7 对账「enterLuaFrame 钩已完成」已涵盖 TAILCALL 路径(实读 P1 实现代码再确认)则无需回填。

### 4.5 onBackEdge 的性能预算估算

P2 的「不在热路径」(§0.1 不变式 1)需要可量化的性能预算 —— 给个具体数字有助于实现时回头检查没有偏离。

`profileEnabled=true` 时,onBackEdge 的单次开销估算(纯纸面分析,实测后修正):

| 操作 | 估算 ns | 备注 |
|---|---|---|
| `if vm.profileEnabled` 分支 | 0(常量假) | 编译期消去;若运行期开关则 ~1ns(BTB 命中) |
| `b.profileOf(proto)` map 查找 | ~20 | Go map 查找平均开销;hot proto 可能命中 CPU L1 cache |
| `pd.tierState != TierInterp` 比较 | ~1 | 寄存器比较 |
| `pd.backEdge == nil` 检查 + 切片索引 | ~1 | 不分配时 |
| `pd.backEdge[pc]++` | ~1 | 写内存 |
| `if >= HotBackEdgeThreshold` 比较 + 分支 | ~1 | 预测良好,几乎总不进 |
| **合计(常态,未越阈值)** | **~24 ns / 回边** | 主要开销在 map 查找 |

参照点:[../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md) §10.1 FORLOOP 本身的执行开销 —— 三次 `value.AsNumber`(无分支位运算)+ 一次加法 + 一次比较 + 两次写栈 + 一次 pc 加。粗估 ~5-10ns。

**预算冲击**:onBackEdge 大约让 FORLOOP 单次执行**慢 2-3 倍**(从 ~5ns 加到 ~30ns)。这听起来很多,但要看绝对值:

- 列内核典型负载里,FORLOOP 本身只是循环骨架,循环体内通常还有几条算术 + 表访问 + IC,合计 ~50-200ns。**FORLOOP 慢 25ns 是循环体总开销的 12-50%**。
- 这与 `profileEnabled=false` 编译期消去整段相比仍是显著开销。**所以 P2 启用时性能 < P1-only**——这是 P2 「时钟」性质的物理代价(§0)。
- **但 P2 启用是为了让 P3 介入**——P3 编译后 gibbous 代码不走解释器主循环,这 25ns 预算只在 crescent 解释期间消耗;升 gibbous 后此 Proto 的回边再也不进 onBackEdge。**「P2 期开销 → P3 加速」是预期路径**。

**优化路径**(若实测发现 onBackEdge 是瓶颈):

- map 查找 → frame 内缓存 ProfileData 指针(类似 frame.proto 缓存,§6.5)。每帧首次 onBackEdge 查 map,后续直接读缓存。
- onBackEdge 改成 inline 调用(Go 编译器可能不内联跨包调用,改成同包/同结构方法或 hint)。
- 阈值检查改成「ID 比较」:用单调递增的版本号代替 `>= HotBackEdgeThreshold` 的具体值,但收益不大。

**当前不优化,等 PB7 验收时实测**(对应 [00-overview](./00-overview.md) §4 PB1 第二项「profile 开关切换不改 byte-equal」是正确性验收;性能验收 P2 没有 [00-overview](./00-overview.md) §8)。

---

## 5. 阈值与编译预算 pacing

### 5.1 阈值常量(建议值)

```go
// internal/bridge —— 热度阈值常量(§5.4 阈值不影响正确性论证)
//
// 数值是 P2 阶段的建议值,实测后用真实负载校准(详见 §8 缺口与 [00-overview] §9)。
// 阈值大小不影响正确性 —— 只影响「何时编译」的时机,与 roadmap §5 原则 3 一致
// (编译只是加速,晚编译只是少赚不出错)。
const (
    HotBackEdgeThreshold uint32 = 1000  // 单回边累计达此值 ⇒ 候选升层(§5.2)
    HotEntryThreshold    uint32 = 200   // 入口累计达此值 ⇒ 候选升层(§5.2)

    // MinPromotableCodeLen(issue #21):Proto 升层候选的最小 opcode 数下限。
    // Proto.Code 长度 < 本值时,considerPromotion 被跳过——但**热度计数仍照常
    // 累积**(OnBackEdge/OnEnter 入口无条件累加,只在越阈值后调 considerPromotion
    // 之前才用本地板过滤),profile 诊断完整。
    //
    // 物理理由:短 proto 的 fixed Run 成本(trampoline + 保存/恢复 callee-saved
    // + 边界税)超过其解释器循环的每次迭代成本,升层反而变慢(P4 实测 fixed
    // Run cost ~111ns > 解释器 78ns,承 backend override 与本节末 MinPromotableLener)。
    MinPromotableCodeLen = 10
)
```

**Backend 覆写钩子 `MinPromotableLener`**(承 `internal/bridge/bridge.go`):后端可通过实现可选接口 `MinPromotableLener { MinPromotableCodeLen() int }` 提出比包级常量更严格的下限,在 `SetP3Compiler` 调用时**快照进 Bridge 私有字段** `minPromotableLen`(不做每次查询),运行期由 `effectiveMinPromotableLen()` 读:

- **fallback**:若后端未实现该接口,回落到包级 `MinPromotableCodeLen = 10`;
- **P4 覆写**:P4 Compiler 实现该接口,当前也返回 10(与包级一致),但通道保留供未来根据 native emit 的 fixed Run cost 调整;
- **不覆盖 `!b.forceAll` 分支**:两阈值分支中,`forceAll=true`(测试入口)绕过下限——force-all 语义就是「不管热度、不管长度,能编都编」,承 [../p4-method-jit/08-testing-strategy.md](../p4-method-jit/08-testing-strategy.md) §3.7 与 P3 08 §2.2;
- **下限调整的正确性论证**:同 §5.3 阈值不影响正确性论证——只影响「哪些 Proto 有资格升」,短 proto 走解释器仍产出正确结果,只是不赚编译收益;调高下限只是保守,调低只是激进(甚至反噬,故实测锚定)。

**Per-proto 豁免钩子 `FloorExempter`**(issue #67,承 `internal/bridge/bridge.go`):地板的物理理由是「fixed Run 成本摊薄不回」,但这个成本只属于**宿主派发通道**(`nativeCode.Run`)。P4 的 seg2seg 被调方走**段内派发通道**(已升层的调用方段内直接 `call`/`blr` 进被调方段),完全不经过 `Run`——对这类 proto 地板的标定前提不成立,反而把它拦在解释器上会让每次来自已升层调用方的调用都付一次 `ExecutePlainCall` 出段往返(spectral-norm 的 9-op `A(i,j)`:144k 次/run,auto 比 force 慢 3.7×)。后端可实现可选接口 `FloorExempter { ExemptFromFloor(proto) bool }` 对特定 proto 豁免地板:

- **只在 auto 模式咨询**(forceAll 本来就绕地板),且只对**低于地板、已越热度阈值**的 proto 咨询——此时 IC 已充分预热,判定稳定;
- **裁决按 proto 缓存**在 `ProfileData.floorExempt`(三态:未问/豁免/不豁免),稳态路径只剩长度比较 + 一个字节读——eligibility 扫描(CFG 构建 + opcode 遍历)不上最热路径;**不豁免裁决在 OnBackEdge 预热里程碑(回边计数 ==1 / ==HotBackEdgeThreshold)重置一次**,与 issue #40 的 recheck re-arm 同步——豁免判定读 IC 状态(P4 的 `ProtoSeg2SegEligible` 要求 GETTABLE 站点 ArrayHit),冷 IC 时缓存的「不豁免」在 IC 预热后需要一次重问,否则递归/深 pc 形状(binary-trees `check` 类)被永久钉死;
- **P4 实现**:`ExemptFromFloor = ProtoSeg2SegEligible`(经 `RegisterPerOpSeg2SegAnalyzer` 钩子接线,保持 jit 主包不 import peroptranslator 的方向约束);
- **fallback**:后端不实现该接口时地板无条件生效(历史行为不变)。

### 5.2 阈值数值的论证

**HotBackEdgeThreshold = 1000**:

- **下限**(避免误报):太低则瞬时短循环被误判热(如 `for i=1,10 do ... end` 跑 100 次 = 1000 回边,但这种短循环没有编译收益,因为单次循环耗时就已经低于编译耗时);太高则真热点迟迟不升。
- **上限**(避免漏报):列内核负载里典型回边数是 1e6+ 量级(批量数据处理),1000 远低于,任何真热点都能在毫秒级越过。
- **prior art 对照**:LuaJIT 默认 `hotloop=56`(回边阈值)、V8 Ignition 在 `--use-osr` 下默认 ~256 —— 它们的阈值都低于我们,因为它们有 OSR(进入运行中的循环编译),P2/P3 是 try-compile 非 OSR,可以保守一点(等到下次进入函数才编译,中间这次先解释)。**1000 是「保守且足够快越」的折中**。

**HotEntryThreshold = 200**:

- 入口热点是「被反复调用的小函数」,200 次足以判热(典型场景:外层循环 1000 次每次调一个 helper,200 次时已确认这个 helper 是热点)。
- 入口阈值远低于回边阈值,因为「调用」是更粗粒度的事件 —— 一次入口可能等价于一次循环内的多次回边。

**两阈值的关系**:不是「同时满足」也不是「任一满足」的简单组合 —— 它们各自独立判热,**任一越阈值就触发 considerPromotion**。组合语义:

```
对每个 Proto pd:
   pd.entryCount     >= 200    ⇒  触发(被反复调用的小函数)
   max(pd.backEdge[]) >= 1000  ⇒  触发(函数内有热循环)
```

两个判定条件**逻辑或**关系。极端情况:一个被调用 300 次、内含 10 次循环的函数,先在第 200 次调用时由入口阈值触发(当时 backEdge 累计 ~2000,实际上回边阈值更早就该触发) —— 这种情况下两个阈值都贡献了「让升层尽早发生」,无副作用。

**单回边越阈值近似「函数热」**:不必每次把所有回边求和(O(回边数) 的开销 + 与单点计数共享的 cache miss),只要某一个回边累计够热,就认为函数值得编译。论证:

- 热循环通常**集中在少数回边**(典型:一个外层 for + 内嵌一个 if 跳转回前面)。任何一个回边越阈值都意味着该函数总体已积累足够多次循环。
- 求和不增加正确性,只可能让升层略微提前(累计 500+500 比单点 1000 早) —— 但提前不影响正确性(§5.4)。
- 简化大头:onBackEdge 是热路径,省一次循环遍历是有意义的常数因子。

### 5.3 阈值不影响正确性的论证

**阈值大小、阈值是否被越过、越过多快**——这三件事**都不影响正确性**,只影响「何时升层」的时机。论证:

- **没越阈值** ⇒ Proto 留在 crescent 解释,正确性恒成立(P1 已差分逐字节一致)。
- **越阈值后查可编译性**:可编译 + 编译成功才升 gibbous;P3 try-compile 静态保证 gibbous 代码与解释器对所有合法输入等价(详见 [04] §1)——升或不升都正确。
- **早升 vs 晚升**:阈值更低 = 更早升 = 更早收益(若可编译),但也更可能把短循环误判为热而浪费编译预算;阈值更高 = 反之。**两端都不出错,只是收益曲线不同**。

这条「阈值不影响正确性」是 P2 的**关键自由度**:数值定标可以放到 P2 实现后用真实负载(首个宿主的规则脚本)校准,**无需在设计阶段定死**(详见 §8 缺口)。

> **正确性 vs 性能的解耦**(承 roadmap §5 原则 3 / [00-overview](./00-overview.md) §1):P2 验收口径是「决策正确」非「性能」([00-overview](./00-overview.md) §8)。所以「阈值数值」属于性能调优旋钮,不属于决策正确性范畴 —— 它一旦校准就稳定,但即便没校准、用建议值,P2 也是「正确」的(只是收益不一定最优)。

### 5.3.1 一个反直觉的推论:阈值大小可以非常激进

承 §5.3 推论:既然阈值不影响正确性,**理论上 HotBackEdgeThreshold 可以低到 1**(每次回边都尝试升层) —— P2 也不会「错」。当然实践中不这样,因为:

- **阈值=1 时**:第一次回边就 considerPromotion,而 considerPromotion 至少要查一次 compilable 缓存(从 CompUnknown 触发首次 analyzeProto,详见 [03])—— 这次分析可能是数百 µs 的开销,放在每个 Proto 第一次回边后承受是浪费(冷函数永远只跑一次回边,白付分析成本)。
- **合理阈值范围**:让大量冷 Proto **永远不**触发 considerPromotion,只让真热点触发 —— 这是阈值设计的目标,而非「保证正确性」(正确性自动满足)。

**所以阈值的下限实际上是「让冷 Proto 不被 considerPromotion 误中」的开销门槛**,而不是「确保只编译该编译的」。这个解耦让阈值定标完全可以放到实测阶段,设计期只给保守建议值。

### 5.3.2 阈值边界值的语义

代码骨架(§4.1):

```go
if pd.backEdge[pc] >= HotBackEdgeThreshold {
    b.considerPromotion(proto, pd)
}
```

注意是 `>=` 而非 `>`,即「累计到 1000 次时**第 1000 次**回边触发」。这与 LuaJIT 的 `hotloop` 阈值语义一致(`hotloop=56` 表示第 56 次回边触发)。

边界细节:

- **首次触发时计数已含本次**:`pd.backEdge[pc]++` 自增后再判定,所以 `>= 1000` 时这一次回边已被计入。
- **重复触发的防止**:第一次 considerPromotion 后,若 [04] 把 tierState 从 TierInterp 转走,后续即便 `pd.backEdge[pc]` 继续增长也不会再触发(被 §4.1 守卫拦下)。
- **若 [04] 推迟决策**(未来 (C) pacing 启用时):tierState 保持 TierInterp,后续每次回边都会重新越阈值并再调 considerPromotion —— **considerPromotion 必须幂等**(§4.3 契约 1),否则要么浪费编译尝试要么重复升层。

### 5.4 编译预算 pacing(P2 初版不做)

「**编译预算**」指「单位时间最多升 N 个函数」的限速机制,目的是防**编译风暴** —— 一次性大量函数越阈值并同时请 P3 编译,导致 cold-start 长尾(用户感知:启动慢)。

例子:一个脚本启动后短时间内有 100 个不同 Proto 都越阈值(批量计算的多个核函数同时被调),若 P2 立刻全部 try-compile,P3 编译总耗时可能 100×10ms = 1s 的 cold-start 卡顿。

**P2 初版不做 pacing,STW 式「越阈值即尝试」**——理由:

1. **正确性不依赖 pacing** —— 编译风暴只影响 cold-start 体验,不影响正确性(§5.3 同源)。
2. **首个宿主的规则脚本不大** —— 单脚本 Proto 数有限(< 50),即使全部越阈值同时编译也是百毫秒级,可接受。
3. **过早抽象** —— 不知道实际负载长尾形式前,设计 pacing 算法(令牌桶?滑动窗口?优先级队列?)是猜的。**先实测再加**。

**未来加 pacing 的接口形状预想**(若实测有 cold-start 长尾):

```go
// 未实现,占位预想 —— 实测发现 cold-start 长尾时再启用
type CompileBudget struct {
    perSecond int                 // 每秒最多升层数
    pending   []*bytecode.Proto   // 排队的 Proto(已越阈值,等编译)
}

func (b *Bridge) considerPromotion(proto *bytecode.Proto, pd *ProfileData) {
    if b.budget != nil && b.budget.exhausted() {
        b.budget.pending = append(b.budget.pending, proto)  // 推迟
        return
    }
    // ... 当前实现 ...
}
```

接口预留意义:**当前 considerPromotion 调用方(onBackEdge/onEnter)与状态机([04])都不假设它「立即升层」**——可能立即升、可能推迟、可能永久 stuck,调用方都不区分。这让未来加 pacing 是纯增量,不改本文上游。**记 §8 缺口**。

---

## 6. 多 State 并发 profile 归属(关键并发决策)

[00-overview](./00-overview.md) §3 关键耦合点 6:**多 State 共享 Program 的 profile 归属是 P2 的核心并发缺口**。本节定稿。

### 6.1 问题陈述

[../p1-interpreter/11-embedding-arena-abi](../p1-interpreter/11-embedding-arena-abi.md) §1.4 / §8 已完成的并发模型:

- **`*Program`**:不可变(编译后固定),**可多 goroutine 并发只读共享**。Program 持 Proto(Go 堆,代码不进 arena、不 GC)。
- **`*State`**:可变(arena/栈/globals/句柄表/GC),**每 goroutine 一个,不可并发**。一个 `*Program` 可被多个 `*State` 并发 Call。

**问题**:Profile 计数挂哪?

- 若挂 **Proto 共享**(所有 State 写同一份)—— 多 State 并发写需要原子操作或锁,且要确保 [11 §8](../p1-interpreter/11-embedding-arena-abi.md) 的 `-race` 通过。
- 若挂 **State 私有**(每个 State 一份,本 State 看本 State 的)—— 无并发,但累积速度被均分(N 个 State 各跑 1000 次,各自累积 1000,而非合并成 N×1000)。

### 6.2 候选方案对比

| 方案 | 计数挂哪 | 累积速度 | 并发开销 | 升层时机 | 实现复杂度 |
|---|---|---|---|---|---|
| **(A) 挂 Proto 共享** | Proto 旁(Go 堆,Program 持有) | **快**(各 State 累积合并) | atomic uint32 自增(每回边一次原子) | 整批数据快越阈值 | 中(原子操作) |
| **(B) 挂 State 私有(选定)** | State 私有 profileTable(map[*Proto]*ProfileData) | **慢**(每 State 独立累积) | **零**(无竞争) | 各 State 独立越阈值 | 低(普通 map) |
| (C) 双表混合 | 主计数 State 私有 + 旁聚合表挂 Proto(原子) | 快(聚合)+ 私有(主) | 中(写两份,聚合表用原子) | 双轨任一越阈值 | 高 |

### 6.3 定稿:(B+C) 嵌套——(B) 默认运行 + (C) 接口预设并占位实现

**P2 PB0 选 (B+C) 嵌套方案**(用户裁决):

- **运行期默认行为**:profile 计数挂 State 私有 `profileTable`,(B) 方案——避免并发写 Proto 旁计数的数据竞争,与 [11 §8](../p1-interpreter/11-embedding-arena-abi.md) 「跨 goroutine 共享只有 Program(不可变)+ 输入 Arena(只读),可变世界 State 私有」一致;
- **接口预设 (C) 入口**:Proto 旁聚合表的字段、API 签名、生命期协议在 PB0 即按 (C) 形式预留(空实现或 nop)。理由:**预设接口比事后扩接口便宜得多**——若 PB7 实测发现 (C) 必要,只需替换 nop 为真聚合,而不是改 ProfileData / Bridge / 状态机的接口面;
- **(C) 启用条件**:pineapple sync.Pool 形式实测出现「累积速度均分 → 热阈值难以生效」时,把 (C) 的 nop 实现替换为真聚合(详见 §6.4 引入路径与 §6.7 (C) 启用前避坑清单)。

详细论证:

#### 6.3.1 为什么不选 (A) Proto 共享 atomic 计数

1. **数据竞争 vs 原子操作的代价**:atomic.AddUint32 在 x86 是 LOCK 前缀的 XADD 指令,延迟 ~10ns;在多 State 并发热循环里,**每回边一次原子操作 = 每回边 ~10ns 额外开销**。这与「P2 不在热路径」(§0.1 不变式 1)冲突 —— P2 的计数本应是**完全免费**的(`profileEnabled=false` 时编译期消去,`true` 时也只是普通自增)。
2. **`-race` 复杂度**:[11 §8](../p1-interpreter/11-embedding-arena-abi.md) 已完成 `-race` 通过的并发约定。Proto 共享计数若用普通 `++` 会触发 race detector;改 atomic 后通过,但每个字段都要 atomic(`entryCount` / `backEdge[pc]` / `tierState` 三个),且 `tierState` 的状态机转移需要 CAS(防两个 State 同时 promote)—— **代码复杂度显著上升**。
3. **共享计数的真实收益不大**:多 State 并发跑同一 Program 的典型场景是「pineapple-like 每请求新 State + sync.Pool 复用」(详见 §6.4) —— 每个 State 处理一个请求,请求内部的循环已经足够触发本 State 内的越阈值,**累积合并的需求并不强**。
4. **设计前置反原则**(roadmap §5 原则 3):在没有实测证据「累积合并能显著提速 cold-start」之前,先选简单方案;但**接口必须预设 (C) 入口**(本节定稿),实测后若发现 cold-start 长尾再启用 (C) 不需要改接口。

#### 6.3.2 为什么 (B) 默认 + (C) 接口预设

1. **并发零开销**:每个 State 自己的 profileTable,普通 map + 普通 `++`,无锁、无原子、无竞争。
2. **`-race` 自然通过**:不同 State 不共享可变数据,`-race` 不会报 warning。
3. **决策正确性不受影响**:每个 State 独立判断「本 State 内这个 Proto 是否热」 —— 若本 State 内热,本 State 升层,得到本 State 内的加速;其他 State 不受影响。**正确性 = 各 State 独立看都对**。
4. **(C) 接口在 (B) 之上的零成本叠加**:Proto 旁聚合表的字段(`*ProtoAggregate`)在 PB0 加到 Proto,初始 nil;(C) nop 实现是「Bridge.Aggregate(state) 调用时不做任何事」。**实现代价 ≈ 0,启用代价 = 替换 nop 为真聚合**。
5. **避免「本来要 (B) 后来发现 (C),回头改接口」**:与 P1 PB 阶段的「前瞻义务」纪律(00-overview §7)一脉相承——**预设 (C) 接口比事后扩接口便宜 100 倍**。

### 6.4 sync.Pool 形式的潜在问题与 (C) 的引入路径

[../p1-interpreter/11-embedding-arena-abi](../p1-interpreter/11-embedding-arena-abi.md) §8.2 给出的并发典型模式:

```
prog, _ := wangshu.Compile(src, "chunk")
for w := 0; w < runtime.NumCPU(); w++ {
    go func(shard *wangshu.Arena) {
        state := wangshu.NewState(opts)         // 每 goroutine 独立 State
        results, _ := prog.Call(state, shard)
    }(shardOf(arena, w))
}
```

这是「常驻 worker」形式 —— 每个 worker 一个 State,跑很多请求,profileTable 累积充分,(B) 方案下表现良好。

**潜在问题:每请求新 State + sync.Pool 复用**(类似 pineapple 场景):

```
// 每请求新建 State,处理完归还 pool
state := statePool.Get().(*wangshu.State)
defer func() { state.Reset(); statePool.Put(state) }()
prog.Call(state, arena)
```

在这种形式下,**每个 State 的生命期短**,profileTable 累积可能在 reset 时被清空(若 Reset 全清),热度信号永远累不到阈值 —— **这是 (B) 方案的退化场景**。

**应对方案** —— 当前不实现,留作未来增强:

#### 6.4.1 方案 (C) 双表混合(增强方案占位)

主表 State 私有(本 State 的累积),旁聚合表挂 Proto(全局累积,原子写),Pool 归还时**累积入 Proto 旁聚合表**:

```go
// internal/bridge —— 未来增强:Pool 归还时累积入 Proto 旁聚合表(原子)
type AggregateProfile struct {
    entryCount atomic.Uint32   // 跨 State 累计入口数
    backEdge   []atomic.Uint32 // 跨 State 累计回边数(按 pc 索引)
}

func (s *State) Reset() {
    if s.profileEnabled {
        for proto, pd := range s.profileTable {
            agg := s.bridge.aggregateOf(proto)  // 取该 Proto 的全局聚合表
            agg.entryCount.Add(pd.entryCount)
            for pc, c := range pd.backEdge {
                if c > 0 {
                    agg.backEdge[pc].Add(c)
                }
            }
        }
        s.profileTable = nil  // 私有表清空
    }
}

// considerPromotion 在 (C) 下要查双表:本 State 私有 + 全局聚合,任一越阈值即触发
func (b *Bridge) considerPromotion(proto *bytecode.Proto, pd *ProfileData) {
    privateBack := pd.maxBackEdge()
    aggBack := b.aggregateOf(proto).maxBackEdge()  // 原子读
    if privateBack >= HotBackEdgeThreshold || aggBack >= HotBackEdgeThreshold*N {
        // 升层 ...
    }
}
```

注意:**(C) 下 Proto 旁聚合表不是 GC 根**(承 §2.4:ProfileData 不进 GC 根集合),但它需要在 Program 销毁时一并销毁(Program 的析构同时清聚合表)。

**(C) 何时启用**:实测发现 sync.Pool 场景下升层从不发生(profileTable 被频繁清空导致永远累不够)—— 当前**未实现,记 §8 缺口**。**(B) 是 PB0/PB1 的实现方案**。

#### 6.4.2 当前 (B) 下 sync.Pool 用户的注意事项

公共 API 文档应明示:**「短生命期 State + Pool 复用形式下,P2 升层可能不触发,导致全程解释执行(crescent)」**——这是 (B) 的已知限制,但不影响正确性(只影响性能;crescent 解释器 ≥2x over gopher-lua 的基线已经成立,未升 gibbous 只是失去 P3+ 的额外加速)。

> 实测后若该限制造成显著性能损失,启动 (C) 方案。

### 6.5 profileTable 的实现细节

```go
// internal/crescent —— State 内嵌 profileTable(P2 PB0+ 启用)
type State struct {
    // ... 已有字段(arena/mainThread/globals/...)...

    // P2 计数表(State 私有,与 arena 同生命期)
    profileTable map[*bytecode.Proto]*ProfileData  // 惰性建,首次 onBackEdge/onEnter 时分配
    bridge       *bridge.Bridge                     // 指回 internal/bridge 入口
}

// internal/bridge —— Bridge 通过 vm 查 State 的 profileTable
func (b *Bridge) profileOf(proto *bytecode.Proto) *ProfileData {
    if b.state.profileTable == nil {
        b.state.profileTable = make(map[*bytecode.Proto]*ProfileData)
    }
    pd, ok := b.state.profileTable[proto]
    if !ok {
        pd = &ProfileData{compilable: CompUnknown}  // 初值见 §2.2
        b.state.profileTable[proto] = pd
    }
    return pd
}
```

要点:

- **profileTable 是 `map[*bytecode.Proto]*ProfileData`**:键是 Proto 指针(Go 堆唯一标识),值是 ProfileData 结构体指针。
- **惰性建**:State 启动时不分配 profileTable,首次 onBackEdge 或 onEnter 命中才建 —— P1-only 部署(`profileEnabled=false`)profileTable 永远 nil,**零开销**。
- **不是 GC 根**:profileTable 是普通 Go map(走 Go GC),不进 [../p1-interpreter/06-memory-gc](../p1-interpreter/06-memory-gc.md) §5.1 R1..R9 的 arena GC 根集合。State 销毁时 map 自然回收。
- **map 查找开销**:每次 onBackEdge/onEnter 都走一次 map 查找(~20ns)—— 这是 (B) 方案相对「直接挂 Proto 字段」的开销。**优化路径**(若实测瓶颈):缓存 frame 内 ProfileData 指针(类似 frame.proto 缓存),onBackEdge 接受 `pd` 参数而非每次查 map。**当前不优化,等实测**。

### 6.6 与 [11 §8] 并发约定的一致性

[../p1-interpreter/11-embedding-arena-abi](../p1-interpreter/11-embedding-arena-abi.md) §8.2 末:**「跨 goroutine 共享的只有 Program(不可变代码)与输入 Arena(只读数据);每 goroutine 的可变世界(State)是私有的。」**

P2 PB0 的 (B) 方案**完全兼容**这个并发约定:profileTable 挂 State 私有,不破坏「Program 跨 State 只读共享」(Program 自身不被 P2 写)、不破坏「State 私有」(profileTable 是 State 内字段)。**`-race` 自然通过**,这是 [00-overview](./00-overview.md) §4 PB7 验收第 (d) 项「多 State 并发 profileTable `-race` 通过」的实现基础。

### 6.7 (C) 方案的潜在风险点(增强方案启用前必读)

若实测后启用 (C) 双表混合方案,有几个工程细节要在设计阶段就预想清楚,避免「启用 (C) 时才发现接口不够」:

1. **聚合表分配时机**:Proto 旁聚合表(AggregateProfile)在何时为 Proto 分配?候选:
   - **(C-i) Compile 时一次性建表**:Program 持有 `map[*Proto]*AggregateProfile`,Compile 时按 Proto 数预分配。简单,但冷 Proto 也分配(浪费,但量小)。
   - **(C-ii) 首次 Reset 时惰性建**:第一个归还的 State 在累积时建表。开销摊薄,但需 sync.Once 或 CAS 防多 State 同时建。
   - **倾向 (C-i)**:简单可靠,内存代价小(每 Proto 一个 4-8 字段的小结构 ≈ 32 字节,即便 1000 个 Proto 也只 32KB)。
2. **Reset 粒度**:Reset 是「全清 profileTable」还是「保留 tierState 但清计数」?
   - **「全清」**:State 被 Pool 复用时完全像新建,profileTable 重新累积 —— 简单,但 (B) 下完全失去累积。
   - **「保留 tierState 但清计数」**:Reset 时把 `tierState=TierGibbous/TierStuck` 的 ProfileData 保留(避免重复升层判定),只清 entryCount/backEdge 给本次请求。需要明确「ProfileData 是否跨 Reset 持久化」,可能违反「State 是干净副本」的语义。
   - **(C) 下推荐:「全清 + 累积入聚合表」** —— Reset 完全清 profileTable,但归还时把数据累积入聚合表。下次 Get 出来的 State 看到的 profileTable 是空的,但下次 considerPromotion 会查全局聚合表,聚合表已含所有历史累积,自然越阈值并升层。
3. **聚合表读时机**:considerPromotion 何时查聚合表?
   - **每次都查**:onBackEdge/onEnter 越本地阈值就 considerPromotion,considerPromotion 内查双表 —— 多一次原子读(~5ns)。
   - **本地越阈值再查全局**:本地累计低于全局阈值时不查 —— 节省读但更复杂。
   - **倾向「每次查」**:considerPromotion 已经不在最热路径(只在阈值临界点),多一次原子读可以接受。
4. **聚合表 Reset 时 race**:多个 State 同时 Reset 同时累积入聚合表,需 atomic.AddUint32 保证 happens-before。这一项已在 §6.4.1 的代码骨架里用 `atomic.Uint32` 表达,但**全局阈值 N 的设计**(`HotBackEdgeThreshold*N`)需要校准 —— N=worker 数?N=NumCPU?N=固定常量?**这一项在 (C) 启用时定**,本文先记缺口。
5. **(C) 下的 `-race` 问题**:聚合表 atomic 写不会触发 race detector(atomic 是 race-free)。但若有「读聚合表 + 写本地表」的混合操作,需要确保读写顺序无依赖 —— 在 (C) 设计骨架里,「Reset 累积」与「Get 后 considerPromotion 查」是不同 goroutine 不同时刻,无并发交错;**(C) 下 `-race` 仍可通过**,前提是按 §6.4.1 的骨架实现(累积全用 atomic.Add,查也用 atomic.Load)。

> 上述五点不是设计期需要全定稿的项,而是 (C) 启用前必读的「**避坑清单**」。当前 (B) 实现时**完全不需要这些复杂度**;(C) 是未来增强,启用时一起定。

---

## 7. 不变式清单(实现与决策须守)

承 [00-overview](./00-overview.md) §1 表与 [00-overview](./00-overview.md) §8 的同源不变式,本子系统的具体兑现:

1. **P2 不加速、不在热路径**(§0.1 / [00-overview](./00-overview.md) §1):onBackEdge/onEnter 是普通函数调用 + 一次 ++ + 一次比较,无分配、无原子、无锁;`profileEnabled=false` 时整段在编译期消去。
2. **字节码不可动**(§0.1 / §3.4):路线 B 旁路计数,不占 0..37,不占 38..63;P1 黄金字节码与寄存器分配同构([../p1-interpreter/12-testing-difftest](../p1-interpreter/12-testing-difftest.md) §2.5)在 P2 启用前后**逐字节一致**。
3. **接口稳定(填占位不改公共 API)**(§0.1):`vm.profileEnabled` 翻 true 是内部行为切换;`wangshu.go` 公共门面零修改([../p1-interpreter/11-embedding-arena-abi](../p1-interpreter/11-embedding-arena-abi.md) §1.1 不变)。
4. **采样点完备**(§1.4):回边 + 入口二者皆要;TFORLOOP 后 JMP 归一类(回边),TAILCALL 等价于 enterLuaFrame(入口)。
5. **ProfileData 与 Proto 同生命期**(§2.4):都住 Go 堆,不进 arena、不进 GC 根、不跨界共享。
6. **延迟分配 backEdge 数组**(§2.3):冷 Proto 的 backEdge 永远 nil,首次命中再分配。
7. **TierInterp 守卫**(§4.1):onBackEdge/onEnter 在 tierState != TierInterp 时立即返回;阻止 Stuck/Gibbous 状态下重复触发 considerPromotion。
8. **多 State 并发 profile 归属:挂 State 私有**(§6.3):`-race` 通过,无并发开销;sync.Pool 短生命期 State 形式是已知限制(§6.4 留 (C) 方案占位)。
9. **阈值不影响正确性**(§5.3):阈值数值是性能旋钮,正确性恒成立;P2 验收口径是「决策正确」,不是「阈值最优」([00-overview](./00-overview.md) §8)。
10. **编译预算 pacing 初版不做**(§5.4):STW 式「越阈值即尝试」;接口形状预留,实测后再加。
11. **considerPromotion 幂等且非热路径**(§4.3):多次调用安全;即便慢(数百 µs)也只在临界点发生,摊薄到每回边几十 ns。
12. **profileTable 是 Go map,不是 GC 根**(§6.5):普通 Go GC 管理,与 arena GC 解耦;State 销毁则 map 自然回收。

---

## 8. 文档缺口 / 待决(记入上游 [doc-gaps](../../../llmdoc/memory/doc-gaps.md))

- **§5.1 阈值数值定标**:`HotBackEdgeThreshold=1000` / `HotEntryThreshold=200` 是建议值。**依赖 P2 PB0+ 实现后用真实负载(首个宿主规则脚本)校准**。当前文档给的数值不影响正确性(§5.3),只影响何时编译。
- **§5.4 编译预算 pacing**:防编译风暴的 pacing(令牌桶/滑动窗口/优先级队列)。P2 初版不做(STW「越阈值即尝试」)。**接口形状已预想,实现等实测发现 cold-start 长尾再启用**。
- **§6.4 sync.Pool 短生命期 State 下的累积失败**:(B) 方案下 profileTable 频繁被 Reset 清空,升层可能从不触发。当前限制写在公共 API godoc 里(短生命期 State + Pool 形式下,P2 升层可能不触发)。**实测若性能损失显著,启用 (C) 双表混合方案**(主表 State 私有 + 旁聚合表 Proto 共享原子写)。
- **§4.4 TAILCALL 钩点接入位置**:本文要求 onEnter 在 TAILCALL 后也调用一次。[05 §7.5](../p1-interpreter/05-interpreter-loop.md) doTailCall 末尾原地改 CallInfo + 重载 frame,**未直接调 enterLuaFrame**;P2 PB1 实现时需在 doTailCall 末尾追加 `if vm.profileEnabled { vm.bridge.onEnter(f.proto) }`。**实现时一并完成,不预先改 [05]**。若 [00-overview](./00-overview.md) §7 已含「enterLuaFrame 钩」可包含 TAILCALL 路径,无需回填(待实读 P1 实现代码再确认)。
- **§2.2 backEdge 是否需要按回边索引而非按 pc 索引**:当前方案按 `len(Proto.Code)` 长度的稠密数组,大部分位置浪费。若实测 Proto 较大且回边稀疏,可改成「编译期收集回边 pc 列表 + 短小密集索引」—— 但需要 codegen 暴露回边 pc 表(回填 [04 codegen](../p1-interpreter/04-frontend-parser-codegen.md))。**当前简化方案够用,实测后再考虑**。
- **§6.5 map 查找开销**:每次 onBackEdge 一次 map 查找(~20ns)。若实测瓶颈,优化路径是 frame 内缓存 ProfileData 指针(类似 frame.proto 缓存)。**当前不优化**。
- **CallInfo 帧级计数是否需要**(§2.1):P2 主决策不需要;若未来出现「分析单次长循环」场景再补。承 [00-overview](./00-overview.md) §9 同源缺口。

### 8.1 对上游(P1 各文档)的回填请求节

承任务要求,审视本文是否对 P1 各文档有回填请求:

| 候选回填项 | 来源 | 状态 |
|---|---|---|
| FORLOOP 回边钩点 | §3.5 / §4.4 | ✅ **已完成**([00-overview](./00-overview.md) §7 行 2:字节码层已暴露,执行侧已可挂钩,`vm.profileEnabled` 当前 false) |
| 循环体 JMP 回跳钩点 | §1.3 / §4.4 | ✅ **已完成**(同上,JMP 在 [05 §2.3](../p1-interpreter/05-interpreter-loop.md) 主循环执行侧) |
| TFORLOOP 后 JMP 钩点 | §1.3 | ✅ **已完成**(归 JMP 一类) |
| `enterLuaFrame` 入口钩 | §4.2 | ✅ **已完成**([00-overview](./00-overview.md) §7 行 3) |
| `vm.profileEnabled` 编译期常量 | §3.5 | ✅ **已完成**(P1 实现当前为 false 占位) |
| TAILCALL 路径补 onEnter 调用 | §4.4 | ⚠ **PB1 实现时完成**(若 P1 当前 enterLuaFrame 已涵盖 TAILCALL 则无需;否则在 doTailCall 末尾追加一行) |
| ProfileData 在 State 上的字段 | §6.5 | ⚠ **PB0 实现时完成**(State 加 `profileTable` + `bridge` 字段;不改公共 API,只改 internal/crescent 实现) |

**结论**:**本文对 P1 文档的回填请求基本为空** —— [00-overview](./00-overview.md) §7 已对账过的 P1 前瞻义务全部已完成。**唯一新增**是 §4.4 的 TAILCALL 路径钩点,这属于 PB1 实现阶段的微小补丁(一行 if),不是设计期的回填请求 —— **不开新回填请求节**,实现时一并完成即可。

---

## 9. 与下游文档的接口承诺

本文是 [00-overview](./00-overview.md) §0 文档地图里「计数」单一事实源。其他 P2 文档对本文的引用契约:

- **[02-ic-feedback](./02-ic-feedback.md)** 引用本文:无直接耦合。IC 反馈聚合的字段(`numHits`/`metaHits`)在 P1 IC slot 里,与本文 ProfileData 字段不重叠。两者并列,独立产出 ([00-overview](./00-overview.md) §2 数据流)。
- **[03-compilability-analysis](./03-compilability-analysis.md)** 引用本文:`ProfileData.compilable` 字段缓存可编译性判定结果,**字段属本文,但写入逻辑属 [03]**。本文承诺字段 spec(§2.2),[03] 承诺 `analyzeProto` 实现与 F1-F7 形状清单。
- **[04-try-compile-fallback](./04-try-compile-fallback.md)** 引用本文:`considerPromotion(proto, pd)` 入口 —— 调用契约本文给(§4.3),实现属 [04]。`ProfileData.tierState` 字段属本文(§2.2),**状态机转移逻辑属 [04]**(本文只承诺 onBackEdge/onEnter 在 TierInterp 时调 considerPromotion,在其他 tierState 时立即返回)。
- **[05-p3-p4-interface](./05-p3-p4-interface.md)** 引用本文:无直接耦合。P3/P4 不读 ProfileData(读的是 TypeFeedback,即 [02-ic-feedback](./02-ic-feedback.md) 的产物)。
- **[06-testing-strategy](./06-testing-strategy.md)** 引用本文:验收口径(累积合理 / 阈值生效 / 多 State 并发 `-race` 通过 / `profileEnabled` 切换 byte-equal)—— [00-overview](./00-overview.md) §4 PB0/PB1/PB7 已列。

---

相关:[00-overview](./00-overview.md)(P2 文档地图 §0 / 边界 §1 / 关键耦合 §3 / 决策表 §6 / 前瞻对账 §7) ·
[02-ic-feedback](./02-ic-feedback.md)(IC 双计数,与本文并列产出) ·
[03-compilability-analysis](./03-compilability-analysis.md)(ProfileData.compilable 写入侧) ·
[04-try-compile-fallback](./04-try-compile-fallback.md)(considerPromotion 实现 / ProfileData.tierState 状态机) ·
[../p1-interpreter/02-bytecode-isa](../p1-interpreter/02-bytecode-isa.md) §4(FORLOOP 回边 / 38..63 预留) ·
[../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md) §10.1(FORLOOP 执行侧) / §1.4(enterLuaFrame) / §7.5(TAILCALL) ·
[../p1-interpreter/01-value-object-model](../p1-interpreter/01-value-object-model.md) §1(Proto 住 Go 堆) ·
[../p1-interpreter/11-embedding-arena-abi](../p1-interpreter/11-embedding-arena-abi.md) §1.4 / §8(多 State 并发语义) ·
[../p2-bridge/00-overview](./00-overview.md) §2(P2 单文件原稿,本文的种子) ·
[../roadmap.md](../roadmap.md) (§4 P2 定义 / §5 五条原则) ·
[evolution-roadmap](../../../llmdoc/architecture/evolution-roadmap.md)(P2 无独立量化验收 / tier 映射)


