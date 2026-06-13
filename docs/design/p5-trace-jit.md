# P5:trace JIT(fullmoon,开放式)

> 状态:**设计阶段,架构决策深度,且为五阶段中唯一「开放式」阶段**(对齐 [architecture](./architecture.md) §2
> 状态表与 [roadmap](./roadmap.md) §4「+2-4 人年到可信 v1,开放式」)。本文比 [p4-method-jit](./p4-method-jit.md)
> 更粗一档:重点回答**何时做、做什么、护城河在哪、为什么无处抄**——是否做、何时做本身就是本文要守住的决策框架。
> 详细设计待 P4 落地且启动判定(§1)通过后才有资格展开。
>
> 对应 Go 包:`internal/fullmoon/trace`(trace 录制 / IR / 寄存器分配 / snapshot+deopt,[architecture](./architecture.md) §1)。
> 上游契约:[roadmap](./roadmap.md)(§4 P5 定义、§0 终局目标、§7 prior art:LuaJIT=trace JIT 架构范本)、
> [evolution-roadmap](../../llmdoc/architecture/evolution-roadmap.md)(P5=fullmoon tier-2;**仅在 P4 收益不够时启动**)、
> [design-premises](../../llmdoc/must/design-premises.md)(前提一的 6% 校准——P5 边际收益的达摩克利斯之剑)。
> 依赖面:[p4-method-jit](./p4-method-jit.md)(全部系统基建的来源)、[p2-bridge](./p2-bridge/00-overview.md)(热度/feedback 前端)、
> [05](./p1-interpreter/05-interpreter-loop.md)(解释器=trace 录制宿主与 deopt 着陆点)、
> [12](./p1-interpreter/12-testing-difftest.md)(投机最重的层,差分主防线在此最关键)。

---

## 0. 定位:终局档位的最后 ~30%

```
P1 解释器 ──► P2 分层桥 ──► P3 Wasm 编译层 ──► P4 method JIT ──► P5 trace JIT
(2-4x)        (基建)        (4-8x)             (trace 收益~70%)   (10-30x,开放式)
```

P5 = **fullmoon(tier-2)**,月相命名的终点(满月),也是 [roadmap](./roadmap.md) (§0) 终局目标的承载者:**列内核负载 10-30x over gopher-lua,逼近 LuaJIT 档**。流水线图标注 P4 已拿走「trace 收益 ~70%」——**P5 的理论空间从一开始就只有剩余的 ~30%**,这个先天的边际收益约束,加上 +2-4 人年的开放式投入,决定了 P5 与众不同的立项姿态:

> **P5 不是计划,是期权。** [roadmap](./roadmap.md) (§4) 与 [evolution-roadmap](../../llmdoc/architecture/evolution-roadmap.md) 速查表的唯一启动条件:「**只在 P4 的收益不够时启动**」。本文的首要职责不是设计 P5,而是把「P4 收益不够」定义成可判定的框架(§1),防止 P5 因技术浪漫主义而非真实需求被启动。

---

## 1. 启动判定框架:什么叫「P4 收益不够」

### 1.1 总闸门:负载证据,不是档位差距

P4 验收达标(列内核 ≥ luajc 档,[p4-method-jit](./p4-method-jit.md) §7.1)后,与 LuaJIT 的剩余差距按前提一的校准只有 **~6%**([design-premises](../../llmdoc/must/design-premises.md):154 vs 164μs)——在 Horner 这类标量算术内核上,**P5 几乎没有立项空间**。所以「收益不够」**不可能由这类基准证明**,只能由 P4 结构性吃不下的负载类别证明。启动判定必须同时满足:

1. **存在真实宿主负载**(非合成基准)落在 §1.2 的类别中,且在宿主端到端口径上占比显著(警惕前提一校准测量 2 的稀释教训:脚本级 -37% 被端到端 ±5-7% 噪声吞没);
2. P4 在该负载上与解释器的加速比**明显低于**其在标量内核上的加速比(说明瓶颈不在 dispatch/类型投机,而在 P4 的结构边界);
3. 该负载无法用更便宜的手段解决(stdlib 内建化、P4 的窥孔扩展、宿主侧改造 arena 形状)。

三者缺一,P5 不启动。这是原则 3(每阶段独立交付、任何闸门停下不亏)的最后一次套用:**P4 停下,项目已达近期目标**([roadmap](./roadmap.md) §0)。

### 1.2 method JIT 结构性吃不下的负载类别(P5 的真实猎物)

P4 的编译单元是函数、虚拟寄存器是栈槽、调用走通用协议——三条结构边界各对应一类负载:

| 类别 | 形状示例 | P4 为何吃不下(结构原因) | P5 的对应武器 |
|---|---|---|---|
| **跨函数热循环** | 列内核循环体里每轮调小函数(比较器、per-row 回调、`obj:method()` 链) | 函数边界 = 编译单元边界:每轮付完整调用协议(压 CallInfo、参数搬移、帧进出),小函数本体再快也被调用税主导 | trace 跨调用边界录制,被调函数体**内联进 trace**,调用税消失 |
| **循环携带的冗余** | 循环体内不变的表查找(`t.x` 每轮重查)、重复的 guard、跨迭代可复用的子表达式 | 模板编译无 IR,看不见跨指令/跨迭代的数据流,每轮老实重算;guard 每模板独立,不合并 | CSE / 循环不变量外提(LICM)把不变操作提出循环,guard 沿 trace 去重 |
| **分配密集循环** | 每轮迭代构造临时 table/字符串(中间结果打包、闭包逃逸) | P4 不做逃逸分析,每轮真实分配 + GC 压力(自管 mark-sweep,[06](./p1-interpreter/06-memory-gc.md)) | 分配下沉(sink)/逃逸分析:不逃出 trace 的分配彻底消除,字段拆成 IR 值 |
| **megamorphic 调用点的稳定子集** | 解释器/分发器型脚本:一个调用点多目标,但热路径上目标稳定 | P2 feedback 标 `FBTableMega`/`FBUnstable`([p2-bridge: 02-ic-feedback §4 TypeFeedback](./p2-bridge/02-ic-feedback.md)),P4 整点放弃投机 | trace 按**实际走过的路径**特化:每条 trace 只含一个目标 + guard,多态点裂成多条单态 trace |

> 列内核负载形状(前提一)与第一类高度相关:理想列内核是「循环体纯标量算术」(P4 已吃下),现实列内核常是「循环体调用一组小工具函数」——后者正是 trace 内联的主场。**启动判定的核心侦察任务:审计首个宿主(多运行时规则引擎)的真实热脚本里,这四类形状的端到端占比**(roadmap §5 原则 4 提到的 262 脚本生产库审计是先例方法)。

### 1.3 判定的量化口径(预登记,防事后挪标)

为防「P5 想做了再找理由」,把判定口径在 P4 验收时**预先登记**(具体阈值届时定,记入 [doc-gaps](../../llmdoc/memory/doc-gaps.md)):在选定的真实负载集上,若「P4 vs 解释器」的加速比不足「标量内核加速比」的某一比例(如一半),且该负载占宿主热时间超某阈值,则 P5 立项评审开启;否则维持 P4 终态。

---

## 2. trace JIT 原理概览(LuaJIT 范本)

P5 若启动,采用 LuaJIT 的总体架构([roadmap](./roadmap.md) §7 prior art):

```
   crescent 解释执行(P2 热度计数照常)
        │ 热回边越 trace 阈值(比 P4 升层阈值更高)
        ▼
   ① trace 录制:解释器切入录制模式,逐条执行的同时把「实际执行的指令 +
      实际观察的类型 + 实际选中的分支」记成线性 IR;CALL 不分界——
      跟进被调函数继续录(= 天然内联);回到起点回边 ⇒ 闭环成 loop trace
        │ 录制中断(NYI 形状 / 太长 / 异常)⇒ 弃 trace,回纯解释,记黑名单
        ▼
   ② IR 优化:SSA 形式线性 IR;CSE、循环不变量外提(LuaJIT 式 loop
      peeling:首轮 + 优化后的后续轮)、guard 去重、死代码消除、
      分配下沉/逃逸分析(P5 最深的优化,可后置到 v2)
        ▼
   ③ 寄存器分配:线性 trace 上的逆序扫描分配(LuaJIT 单遍风格)——
      IR 值驻留机器寄存器,栈槽往返消失(P4 结构税在此终于卸掉)
        ▼
   ④ 发射 + 安装:复用 P4 全套系统管线(§3);trace 入口 patch 到热回边;
      每个 guard 挂 side exit + snapshot(§4)
        │ 运行期 guard 失败
        ▼
   ⑤ side exit:按 snapshot 物化解释器状态(可能多帧)→ crescent 续跑;
      某 exit 自身变热 ⇒ 从该 exit 续录 side trace(trace 树生长)
```

与 P4 的本质区别一句话:**P4 编译「函数的全部可能路径」,投机只在类型维度;P5 编译「实际走过的那一条路径」,类型与控制流双维度投机**——投机更重 ⇒ 快路径更纯(无分支、全寄存器、跨函数);也 ⇒ 假设更脆,deopt 机器必须精细到指令级(§4)。

录制起点选择沿用 LuaJIT 经验:**热循环回边**是首要起点(loop trace 是列内核收益主体),热 side exit 续录次之,函数入口 trace(up-recursion 等)最后——P2 的回边计数基建([p2-bridge: 01-profiling §2 回边采样](./p2-bridge/01-profiling.md))直接复用,只是阈值另设。

---

## 3. 与 P4 的关系:基建全复用,新增四件套

P5 **不推倒任何已有层**,fullmoon 是叠在 gibbous 之上的第三执行层:

| 资产 | 来源 | P5 怎么用 |
|---|---|---|
| 自管机器栈 / trampoline / exec mmap / W^X / icache flush | [p4-method-jit](./p4-method-jit.md) §4 | **原样复用**——四项税解法与世界边界不因 trace 而变 |
| amd64/arm64 发射器(指令编码层) | [p4-method-jit](./p4-method-jit.md) §5 | 复用编码器;regalloc 之后的发射逻辑新写(trace 的码形与模板不同) |
| OSR 物化路径(寄存器→栈槽→CallInfo→解释器续跑) | [p4-method-jit](./p4-method-jit.md) §3 | 着陆机制复用,**物化内容升级**:从「栈槽已是真相」变成「按 snapshot 重建多帧真相」(§4) |
| 热度计数 / TypeFeedback / 可编译性闸门 | [p2-bridge](./p2-bridge/00-overview.md) | 复用:trace 阈值另设;feedback 辅助录制期决策;**NYI 黑名单沿用原则 4**——录制遇到不可处理形状(varargs/coroutine/debug 同款清单 + trace 特有的 NYI)弃 trace 走下层,不做完备性 |
| NaN-box 值表示 / arena / GC | [01](./p1-interpreter/01-value-object-model.md) | 不变式照守([architecture](./architecture.md) §4:值表示一次定死)——IR 值的装拆箱就是 NaN-box 位操作,GC safepoint 纪律同 P4 |
| 差分 harness | [12](./p1-interpreter/12-testing-difftest.md) §3.8 | 新增 `WangshuFullmoon` runner(12 已预留注释位) |
| **新增(P5 独有)** | — | ① trace 录制器(解释器内嵌录制模式)② SSA IR + 优化 pass ③ trace 寄存器分配器 ④ snapshot+deopt 机器 |

三层升降关系:crescent(录制宿主 + 全体 deopt 着陆点)→ gibbous(P4,函数级稳态层——trace 覆盖不到/被黑名单的热函数停在这里)→ fullmoon(trace 覆盖的最热路径)。层间调用沿用 P3/P4 的统一 CallInfo 协议与 trampoline。**fullmoon 的 deopt 着陆点是 crescent 而非 gibbous**——deopt 语义以解释器为 oracle(原则 1),落到 P4 码反而要二次映射状态,得不偿失;deopt 后热度再升回 gibbous/fullmoon 由 P2 状态机自然完成。

---

## 4. snapshot + deopt 机器:最难的一块

### 4.1 问题:trace 中途退出,真相已被优化拆散

P4 的 deopt 之所以薄([p4-method-jit](./p4-method-jit.md) §3.3),靠的是「每条字节码边界栈槽即真相」。P5 的三项核心优化**逐条拆毁这个前提**:

- 寄存器分配 ⇒ 活值在机器寄存器/spill 槽,**不在栈槽**;
- trace 内联 ⇒ 一条 trace 中途对应**多个逻辑 Lua 帧**(被调函数的帧从未真实压过 CallInfo);
- 分配下沉 ⇒ 某些「对象」**物理上不存在**(字段散在 IR 值里),deopt 时必须凭空重建(unsink)。

guard 失败时必须恢复出「逐条解释到此处会有的精确状态」——值栈、CallInfo 链、pc,逐字节正确,否则就是静默错果。

### 4.2 概念方案:snapshot = 「IR 值 → 解释器栈槽」的稀疏映射

沿 trace 在每个 guard 处记 snapshot(LuaJIT 同款思路):

```
snapshot@guard_k:
  exit_pc       : 回到解释器的字节码 pc(可能在被内联的函数体内)
  frames[]      : 逻辑帧链 [(protoID, base偏移, 返回pc), ...] —— 重建 CallInfo 用
  slots{}       : 稀疏映射 { 解释器栈槽号 → IR 引用 | 常量 | sunk对象重建配方 }
                  (只记从 exit_pc 起活跃的槽;死槽不记)
deopt(guard_k):
  1. 按 frames[] 补建被内联函数的 CallInfo 链(05 §1.2 布局)
  2. 按 slots{} 物化:寄存器/spill → NaN-box u64 写回 arena 栈槽;
     sunk 对象先真实分配再填字段(unsink)
  3. trampoline 出 JIT 世界,crescent 从 exit_pc 续跑
```

工程要点(决定成败的细节,详细设计阶段展开):snapshot 的**压缩**(每 guard 一份全量映射会撑爆内存——LuaJIT 用增量/共享编码)、与 regalloc 的**耦合**(snapshot 引用的 IR 值必须在 exit 时可恢复,约束寄存器分配的自由度)、unsink 与 GC 的交互(deopt 中途分配可能触发 GC)。

### 4.3 复杂度对照(为什么这是「+2-4 人年」的主成分)

| | P4 函数级 OSR | P5 snapshot deopt |
|---|---|---|
| exit 粒度 | 函数(整函数放弃) | **指令级**(trace 内任意 guard) |
| 恢复的帧数 | 1(当前帧,且已在 CallInfo) | **1..N**(含从未物理存在的内联帧) |
| 值的位置 | 已在栈槽(真相点不变式) | 寄存器/spill/常量/被 sink 的对象字段 |
| 映射数据 | 无需(静态生成 exit 序列) | 每 guard 一份 snapshot,需压缩与生命周期管理 |
| 出错形态 | 几乎无投机面 | 任一槽映射错 / unsink 漏字段 ⇒ **静默错果** |

### 4.4 为什么「无处抄」(逐组件难度评估)

[roadmap](./roadmap.md) (§4) 称 snapshot+deopt 机器是「LuaJIT 的真正护城河,无处抄」。展开:LuaJIT 的 IR(双数组 SSA、折叠引擎)、snapshot 压缩、单遍逆序 regalloc 三者**互为前提、高度耦合**,且除源码注释与零散邮件列表外无系统文档;其精妙处(如 sink 优化与 snapshot 的协同)是 Mike Pall 个人风格的高密度 C,**只能读懂原理后按望舒约束重设计,不能移植**。Go 生态没有现成 trace JIT 库可借(wazero 是 method 式 Wasm 编译器,帮不上 IR/snapshot)。逐组件难度:

| 组件 | 难度 | 评估依据 |
|---|---|---|
| trace 录制器 | **中,相对可控** | 在解释器上加录制模式,机制直白(逐指令旁录 IR);难点在工程琐碎:NYI 清单、黑名单、trace 长度/深度限制、录制开销控制 |
| IR + 经典优化(CSE/LICM/DCE) | **中,但深坑在后** | 教科书算法成熟;坑在 Lua 语义的细节正确性——元方法可观察副作用、表别名、NaN/-0、GC 移动语义,任何一条优化越界即静默错果 |
| 寄存器分配 | **中偏难** | 线性 trace 上无须图着色,LuaJIT 式单遍逆序扫描可行;与 snapshot 的耦合(§4.2)是主要复杂度来源 |
| **snapshot + deopt** | **最难** | §4.3;正确性无法靠评审保证,只能靠差分 fuzz 长期撞(§5);LuaJIT 此处 bug 史绵延多年可为镜鉴 |
| 分配下沉/逃逸 | **难,可后置** | 收益集中在分配密集类负载(§1.2 第三类);v1 可不带,作为 P5 内部的第二闸门 |

> 这也是「+2-4 人年,开放式」的含义:范围下界(录制 + 基础优化 + regalloc + snapshot,不含 sink)约 2 人年;上界开放,因为 snapshot 机器的正确性收敛时间**本质上不可计划**——它由 fuzz 撞出的 bug 衰减曲线决定,而非里程碑排期决定。

---

## 5. 验收与差分:投机最重的层,主防线在此最关键

**验收**([roadmap](./roadmap.md) §4):列内核负载 **10-30x over gopher-lua**——终局目标线。基准口径仍是 [12](./p1-interpreter/12-testing-difftest.md) §6.1 的列内核硬约束;在 §1.2 选定的「P5 猎物负载」上,还须证明显著优于 P4(否则 P5 没有兑现自己的立项理由——这是比 10-30x 更针对性的内部验收)。

**差分**(原则 2 在 P5 到达顶点):P5 的每一项机制——录制的类型假设、每个优化 pass 的语义保持、snapshot 的状态重建——都是「静默错果」候选,且组合空间远超 P4。全套接入 [12](./p1-interpreter/12-testing-difftest.md) 既有轨道(§3.8 Runner 新增 fullmoon、§7 P5 行已预留),并加码:

- **同 Proto 三层差分**:crescent vs gibbous vs fullmoon 两两 byte-equal,CI 硬门禁;
- **deopt 注入**:每 guard 强制失败模式(承 [p4-method-jit](./p4-method-jit.md) §7.2),把每条 side exit + snapshot 恢复路径踩热;snapshot 恢复后的最终输出与一路解释 byte-equal;
- **优化 pass 分级差分**:按 pass 开关组合跑差分(只录制不优化 / +CSE / +LICM / +sink…),把「哪个 pass 引入错果」定位成一阶问题;
- **持续 fuzz 作为常驻基础设施**:P5 的正确性置信度 = fuzz 时长的函数([12](./p1-interpreter/12-testing-difftest.md) §8 的 nightly 长跑在 P5 期间应升格为专用 fuzz 集群)——这是 §4.4 末「收敛时间不可计划」的对策。

---

## 6. 风险与开放问题

**风险:**

1. **最大风险是「不该做而做了」**:P4 达 luajc 档后,标量内核上距 LuaJIT 仅 ~6%(前提一)——若宿主真实负载不落在 §1.2 的类别里,P5 的 +2-4 人年买不到端到端可见的收益(校准测量 2 的稀释教训)。§1 的判定框架就是本风险的全部对策:**让负载证据而非工程野心做决定**。这正是 roadmap「只在 P4 收益不够时启动」的深意。
2. **人年开放式的失控面**:snapshot 机器正确性收敛不可排期(§4.4)。对策:P5 内部分闸(录制+基础优化+regalloc+snapshot 为 v1;sink/逃逸为 v2;side trace 树为 v3),每闸独立可停——原则 3 在 P5 内部的递归套用。
3. **纯 Go 约束对 trace 收益的折损**:全显式 guard(无信号陷阱,承 [p4-method-jit](./p4-method-jit.md) §2.2)在 guard 密集的 trace 码里成本占比高于 method JIT;trace 越长 guard 越多,10-30x 区间的上沿可能因此够不到——验收区间本身已用「10-30x」的宽带表达了这层不确定。
4. **维护性风险**:trace JIT 的复杂度是永久性负债(LuaJIT 社区维护困境是前车)——即便做成,团队是否长期养得起这台机器,应作为启动评审的显性议题。

**开放问题(记入 [doc-gaps](../../llmdoc/memory/doc-gaps.md);多数有意推迟到启动判定之后):**

- §1.3 判定口径的具体阈值与负载集选定——P4 验收时预登记;
- IR 具体形式(LuaJIT 式双数组 vs 常规 SSA 结构)、snapshot 编码方案、regalloc 与 snapshot 的耦合协议——立项后的详细设计主体;
- trace 阈值 / trace 长度与深度上限 / side trace 树的生长与回收策略 / 黑名单与 NYI 清单的 P5 扩展——依赖录制器原型实测;
- 分配下沉与自管 GC([06](./p1-interpreter/06-memory-gc.md))的交互(unsink 中途 GC、sunk 对象的根可见性)——v2 阶段展开;
- coroutine 与 trace 的关系(LuaJIT 选择 trace 不跨 yield,望舒大概率同款,沿 P2 F2 清单)——立项后定;
- fullmoon 与 gibbous 的热度交接细节(从 P4 码的回边直接热到 trace,还是必须经解释器录制——LuaJIT 无此问题,它只有一层 JIT;望舒三层结构特有)——立项后定。

---

相关:[roadmap](./roadmap.md)(§4 P5 定义 / §0 终局目标 / §7 prior art) ·
[architecture](./architecture.md)(§1 包布局 `internal/fullmoon/trace` / §2 tier 映射 / §4 三不变式) ·
[p4-method-jit](./p4-method-jit.md)(基建来源 / §3 OSR 对照 / §7.1 luajc 档与 ~70% 锚点) ·
[p2-bridge](./p2-bridge/00-overview.md)(热度与 feedback 前端 / F1..F7 闸门 / §5.1 投机谱系表) ·
[p3-wasm-tier](./p3-wasm-tier.md)(分层机器的首个验证场) ·
[05-interpreter-loop](./p1-interpreter/05-interpreter-loop.md)(录制宿主 / CallInfo——snapshot 重建目标) ·
[06-memory-gc](./p1-interpreter/06-memory-gc.md)(分配下沉与 GC 交互) ·
[12-testing-difftest](./p1-interpreter/12-testing-difftest.md)(§3.8 fullmoon runner 预留 / §7 P5 行 / 持续 fuzz) ·
[evolution-roadmap](../../llmdoc/architecture/evolution-roadmap.md)(P5 速查行 / 启动条件) ·
[design-premises](../../llmdoc/must/design-premises.md)(前提一 6% 校准 / 五原则)
