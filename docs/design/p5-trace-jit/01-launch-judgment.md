# P5 §1:启动判定框架——什么叫「P4 收益不够」

> 状态:**未立项**。本文是「P5 该不该做、什么时候做」的单一事实源,严格承接 [../roadmap.md](../roadmap.md) §4 与 [../../../llmdoc/architecture/evolution-roadmap.md](../../../llmdoc/architecture/evolution-roadmap.md) 速查表「只在 P4 收益不够时启动」这条立项原则。任何 P5 施工都不能先于本文定下的判定通过。**判定通过前,后续章节都是「已经决定启动」这一前提下的设计,不产生代码**。
>
> 对应 Go 包:未立项前不新建;如果立项则是 `internal/fullmoon/trace`(见 [./00-overview.md](./00-overview.md) §1)。
>
> 上游依据:[../roadmap.md](../roadmap.md)(§4 P5 定义、§0 终局目标;§1 校准测量——LuaJIT vs luajc 只差 6% 是本文所有判据的物理基础;§5 五条贯穿原则,尤其是原则 3「每阶段独立交付,不亏」);[../../../llmdoc/must/design-premises.md](../../../llmdoc/must/design-premises.md)(前提一 6% 校准、前提三五条原则);[../../../llmdoc/architecture/evolution-roadmap.md](../../../llmdoc/architecture/evolution-roadmap.md)(P5 = fullmoon tier-2,启动条件只有一个)。
>
> P4 承接面:[../p4-method-jit/00-overview.md](../p4-method-jit/00-overview.md) + [../p4-method-jit/01-launch-judgment.md](../p4-method-jit/01-launch-judgment.md)(P4 立项判定作为 P5 立项判定的形式参照——两者都是流水线下一阶段的开工检查点,共享同样的「立项判定先于实施」结构,见 [../p4-method-jit/01-launch-judgment.md](../p4-method-jit/01-launch-judgment.md) §6.4 对偶面);[../p4-method-jit/09-acceptance-checklist.md](../p4-method-jit/09-acceptance-checklist.md)(P4 验收数据是本文判定的核心输入);[../p4-method-jit/implementation-progress.md](../p4-method-jit/implementation-progress.md)(P4 交付现状与已知的结构性损失)。
>
> 本文定位一句话:**P5 立项判定是双向的——通过则达到终局目标(10-30x over gopher-lua);不通过则 P4 已交付即项目达到近期目标,原则 3 得到实现**。

---

## 1. 总条件:三条并集

### 1.1 三条并集条件(原始表述)

按照 [../roadmap.md](../roadmap.md) §4 和 [../../../llmdoc/architecture/evolution-roadmap.md](../../../llmdoc/architecture/evolution-roadmap.md) 速查表的原则,P4 验收达标(列内核 ≥ luajc 档,见 [../p4-method-jit/08-testing-strategy.md](../p4-method-jit/08-testing-strategy.md) §1)之后,按前提一的校准,与 LuaJIT 的剩余差距只有 **~6%**([../../../llmdoc/must/design-premises.md](../../../llmdoc/must/design-premises.md):154 vs 164μs)——在 Horner 这类标量算术内核上,**P5 几乎没有立项的空间**。所以「收益不够」**不可能由标量内核基准来证明**,只能由 P4 结构上吃不下的负载类别来证明。

P5 立项启动**必须同时满足以下三条**:

1. **存在真实的宿主负载**(不是合成基准)落在 [§2 的四大类别](#2-p4-结构上吃不下的负载类别)中,并且在宿主端到端的标准上占比显著(要警惕前提一校准测量 2 的稀释教训:脚本级 -37% 被端到端 ±5-7% 噪声吞没);
2. **P4 在该负载上相对解释器的加速比,明显低于其在标量内核上的加速比**(说明瓶颈不在 dispatch 或类型投机,而在 P4 的结构边界——method-JIT 没有 IR、没有跨迭代 CSE、函数是编译单元的边界);
3. **该负载无法用更便宜的手段解决**(stdlib 内建化、P4 的 peephole 扩展、宿主侧改造 arena 形状、P4 op-set 扩展——详见 [§4](#4-cheaper-alternatives-checklist))。

**三者缺一,P5 不启动**。这是原则 3 的最后一次运用:**P4 停下,项目就已经达到了近期目标**([../roadmap.md](../roadmap.md) §0)。

### 1.2 ~6% 校准的物理含义

Horner 5 次多项式 1000 items 的校准测量([../roadmap.md](../roadmap.md) §1):

| 嵌入栈 | 绝对值 | 相对 gopher-lua |
|---|---|---|
| gopher-lua | 729μs | 1x |
| LuaJ-luac | 259μs | ≈2.8x |
| **LuaJ-luajc** | **164μs** | **≈4.4x** |
| **LuaJIT** | **154μs** | **≈4.7x** |

标量内核上 LuaJIT 与 luajc 只差 **6%**。P4 立项验收锚定在 luajc 档(164μs,见 [../p4-method-jit/01-launch-judgment.md](../p4-method-jit/01-launch-judgment.md) §1.2),达标就意味着 P4 已经把标量内核上距 LuaJIT 的差距压到 ~6% 之内。这 6% 是 **P5 边际收益的绝对上限**——P5 在标量内核上再怎么优化,也不可能超过 6% 的绝对收益。**所以 P5 的价值不在标量内核**,而在 P4 结构上吃不下的负载类别(§2)。

### 1.3 原则 3 在 P5 处的具体运用

[../roadmap.md](../roadmap.md) §5 原则 3「每阶段独立交付价值,任何验收处停下都不亏」在 P5 立项判定这里体现为「双向判定」:

- **通过判定**:达到终局目标([../roadmap.md](../roadmap.md) §0 列内核 10-30x over gopher-lua);
- **不通过判定**:P4 已交付即项目达到近期目标(逼近 LuaJIT 档 ~94%),立项凭据写档,以后条件成熟再启动判定;P5 未启动就不消耗资源。

这一原则同时约束着判定内部:P5 启动后,内部仍然分 v1 / v2 / v3 三档独立验收,每档独立可停(见 [../roadmap.md](../roadmap.md) §5 原则 3 的递归运用,以及 [./09-acceptance-checklist.md](./09-acceptance-checklist.md) §2)——原则 3 从阶段级递归到 P5 内部子档。

---

## 2. P4 结构上吃不下的负载类别

### 2.1 四大负载类别(适合 P5 的负载)

P4 的编译单元是函数、虚拟寄存器是栈槽、调用走通用协议、guard 每个模板都独立——这四条结构边界各自对应一类负载。承接原单文件 §1.2,下面这个表格是 **P4 结构上吃不下的四类负载**:

| 类别 | 形式示例 | P4 为什么吃不下(结构原因) | P5 对应的手段 |
|---|---|---|---|
| **跨函数热循环** | 列内核循环体里每轮调用小函数(比较器、per-row 回调、`obj:method()` 链) | 函数边界 = 编译单元边界:每轮都要付完整的调用开销(压 CallInfo、参数搬移、帧进出),小函数本体再快也被调用开销主导 | trace 跨调用边界录制,被调函数体**内联进 trace**,调用开销消失 |
| **循环内的冗余** | 循环体内不变的表查找(`t.x` 每轮重查)、重复的 guard、跨迭代可复用的子表达式 | 模板编译没有 IR,看不见跨指令和跨迭代的数据流,每轮老实重算;guard 每个模板都独立、不合并 | CSE 和循环不变量外提(LICM)把不变的操作提到循环外,guard 沿 trace 去重 |
| **分配密集的循环** | 每轮迭代都构造临时 table 或字符串(中间结果打包、闭包逃逸) | P4 不做逃逸分析,每轮都真的分配 + 增加 GC 压力(自管 mark-sweep,见 [../p1-interpreter/06-memory-gc.md](../p1-interpreter/06-memory-gc.md)) | 分配下沉(sink)和逃逸分析:不逃出 trace 的分配彻底消除,字段拆成 IR 值 |
| **megamorphic 调用点的稳定子集** | 解释器 / 分发器型脚本:一个调用点有多个目标,但热路径上目标稳定 | P2 feedback 会标 `FBTableMega` 或 `FBUnstable`(见 [../p2-bridge/02-ic-feedback.md](../p2-bridge/02-ic-feedback.md) §4),P4 整点放弃投机 | trace 按**实际走过的路径**特化:每条 trace 只包含一个目标 + guard,多态点拆成多条单态 trace |

**关键要点**:列内核的负载形式(前提一)和第一类高度相关。理想的列内核是「循环体纯标量算术」(P4 已经吃下),现实的列内核常常是「循环体调用一组小工具函数」——后者正是 trace 内联的主场。

### 2.2 侦察任务:审计首个宿主的真实热脚本

启动判定的**核心侦察任务**:审计首个宿主(多运行时规则引擎,见 [../../../llmdoc/overview/project-overview.md](../../../llmdoc/overview/project-overview.md))的真实热脚本里,§2.1 四类形式的端到端占比。

方法参照 [../roadmap.md](../roadmap.md) §5 原则 4 提到的 **262 脚本生产库审计**这个先例。侦察输出物必须包含:

- 每条热脚本的 profile 数据(采样级);
- 按 §2.1 四类分档统计 CPU 时间占比;
- 端到端(而不是 per-item)标准——前提一校准测量 2 的教训是:per-item 或者脚本级数据被端到端稀释之后可能就看不见了,只有端到端的占比才是 P5 立项的判定依据;
- 数据落到 [../../../llmdoc/memory/decisions/](../../../llmdoc/memory/decisions/),格式对齐 P4 立项判定数据档([../p4-method-jit/09-acceptance-checklist.md](../p4-method-jit/09-acceptance-checklist.md) §3 归档模式)。

### 2.3 与 P4 的 call 核结构边界的关系

[../p4-method-jit/01-launch-judgment.md](../p4-method-jit/01-launch-judgment.md) §3.3 的关键追问指出:P4 原生后端不是 call 核 0.52x 的银弹——bench kernel body 里如果包含 ReasonUnknownCall(F2-b 静态分析不能确定被调函数不 yield)时,P4 仍然要跑跨层。这种情况和 §2.1 第一类「跨函数热循环」高度重叠,可能是 P5 的核心目标负载之一。

但**边界需要注意**:P4 的 call 核瓶颈的根本原因是 F2-b 静态分析的标准,即使 P5 用 trace 内联绕过 F2-b 的静态约束(trace 是按实际执行来观察的,不受静态分析限制),仍然需要确认:

1. 宿主的真实负载是否真的是 call 核形式(而不是 bench kernel 合成形式);
2. F2-b 静态分析限制在真实宿主上的 profile 占比是否显著;
3. 是否可以用更便宜的手段(P2 决策机侧改动)放宽 F2-b,而且这种手段已经被排除(见 [§4](#4-cheaper-alternatives-checklist))。

---

## 3. 量化预登记:阈值、落点、签署人、证据要求

### 3.1 为什么要预登记

为了防止「P5 想做了再找理由」,把判定标准在 **P4 验收时预先登记**(承接原单文件 §1.3)。登记的核心是把 §1.1 的三条并集条件从「文字承诺」升级到「可以判定的具体阈值」,并且锁定在 P4 验收时点,防止 P5 立项判定时倒推阈值凑数。

预登记本身也是一份产出物——即便 P5 从来没有启动过,预登记文档也作为「项目在 P4 时点对 P5 边界的判断」永久归档。

### 3.2 登记表(占位阈值,等 P4 验收时填入实际值)

**A. P4 在目标负载上的加速比阈值**

- 定义:`speedup_A = time_P1_interp / time_P4_jit`,在选定的真实负载集上测量。
- 参照基线:`speedup_ref = time_P1_interp / time_P4_jit_scalar_kernel`,即 P4 在 Horner 类标量内核上的加速比(P4 验收时归档)。
- **预登记阈值(占位,P4 验收时填入实际值)**:`speedup_A < 0.5 × speedup_ref`。含义:P4 在目标负载上的加速比不足其标量内核加速比的一半——说明 P4 结构边界是主导因素(§1.1 条件 2 成立)。
- 具体的 0.5 倍数字要等 P4 交付之后再校准——如果 P4 标量内核实测速比远超 luajc 档(现在 amd64 上 14.08x over gopher,见 [../p4-method-jit/09-acceptance-checklist.md](../p4-method-jit/09-acceptance-checklist.md) §3 V14),0.5 这个阈值可能太严;需要在 P4 验收时点根据具体数据重新校准。

**B. 目标负载在宿主热时间中的占比阈值**

- 定义:`share_A = sum(cpu_time_on_class_2.1) / total_host_cpu_time`,在真实宿主热脚本 profile 中测量。
- **预登记阈值(占位,P4 验收时填入实际值)**:`share_A ≥ 15%`。含义:§2.1 四类负载合计在宿主端到端 CPU 时间中占比至少 15%——说明目标负载不是边缘形式(§1.1 条件 1 成立)。
- 具体的 15% 数字同样要等 P4 验收时点根据首个宿主的实际数据再校准。

**C. cheaper alternatives 已经耗尽的证据**

- 定义:详见 §4 的 cheaper-alternatives checklist,已经系统评估过,四条中至少已经尝试了三条并且被证明不足以关闭差距。
- 阈值不是数字,而是**是否已经归档**四条的评估报告(每条包含「已经尝试的具体做法 + 评估结果 + 为什么不够」)。

### 3.3 登记落点

登记文档的形式和落点:

| 项 | 落点 | 用途 |
|---|---|---|
| 预登记表(§3.2 A/B/C 具体阈值) | [../../../llmdoc/memory/decisions/](../../../llmdoc/memory/decisions/) 新建一份 `p5-launch-preregistration-YYYY-MM-DD.md` | 立项判定时的判据来源 |
| 目标负载集选定 | 同上文档 + 首个宿主 profile 数据附件(测量原始文件) | §1.1 条件 1 的判定依据 |
| P4 标量内核速比基线 | 引用 [../p4-method-jit/09-acceptance-checklist.md](../p4-method-jit/09-acceptance-checklist.md) §3 V14 归档数字 + 引用日期 | §3.2 A 的参照基线 |
| 四类 cheaper alternatives 评估报告 | 同上文档 §4 节 | §1.1 条件 3 的判定依据 |

登记时点:**P4 PJ11 验收完成之后,P5 立项判定启动之前**。目前 P4 已经完成 PJ11 验收(2026-07-01),所以预登记应该在启动 P5 立项判定之前完成——如果首个宿主的 profile 数据还没到位,预登记也不能开始(此时 P5 立项判定同样不能开始)。

### 3.4 签署人

**用户决策 + 主助理归档**(承接 [../p4-method-jit/01-launch-judgment.md](../p4-method-jit/01-launch-judgment.md) §5.3 数据进档协议同样的纪律)。用户在预登记表签署确认之后,阈值锁定;P5 立项判定运行时,主助理照阈值判定,不再重新协商。

如果 P4 验收之后数据显示 §3.2 A/B 的占位阈值不合理(比如 P4 speedup_ref 远超预期,使得 0.5 阈值太严),预登记表可以做版本迭代——每次版本迭代仍然需要用户签署,并且在 `p5-launch-preregistration-YYYY-MM-DD.md` 里记录版本变更历史。

### 3.5 证据要求

判定运行时必须提交的证据物件(缺一项则判定不能启动):

| 证据 | 形式 | 来源 |
|---|---|---|
| **bench JSON** | 目标负载集在 P4 vs P1 解释器 vs gopher-lua 的实测数据,`benchstat` 兼容格式 | 项目 bench-acceptance 同样的工作流(参照 [../p4-method-jit/09-acceptance-checklist.md](../p4-method-jit/09-acceptance-checklist.md) §3) |
| **host profile** | 首个宿主运行真实负载的 CPU profile(pprof 或等价格式),包含 §2.1 四类分档统计 | 宿主侧侦察任务的产出(§2.2) |
| **P4 标量内核基线** | P4 验收时归档的 V14 数据 + 硬件、参数、日期标注 | 引用 [../p4-method-jit/09-acceptance-checklist.md](../p4-method-jit/09-acceptance-checklist.md) §3 |
| **cheaper alternatives 评估** | §4 四条的评估报告,每条包含尝试的做法 + 数据 + 结论 | §4 checklist 逐条执行的产出 |

---

## 4. Cheaper Alternatives Checklist

P5 是 +2-4 人年的开放式投资,启动前必须证明**没有更便宜的方案能关闭同一个 gap**。本节列出四条 cheaper alternatives,每条都必须在 P5 立项之前**先做评估**,评估报告落到 §3.2 C 归档。

### 4.1 stdlib 内建化

**含义**:把 §2.1 目标负载里高频调用的 Lua 层函数,下沉到 Go 原生 stdlib 实现,让宿主脚本调用 stdlib 而不是用户自定义的小函数;stdlib 内建化路径可以绕过 F2-b 静态分析的限制(stdlib 白名单是确定不 yield 的)。

**评估成本**:低——每个 stdlib 函数几十到几百行 Go 代码,可以增量交付;不需要 P5 全套机制。

**如何评价**:

1. 从宿主 profile 揭示的调用密集的小函数中挑出 top N 常用函数(比如 comparator、mapper、reducer 类);
2. 逐个考察是否可以下沉为 stdlib(有些是宿主业务特化的,不能下沉);
3. 对可下沉的估算收益:「下沉后跨层次数减少 × 每次跨层 ~143ns」的减法;
4. 如果估算收益已经能关闭 §3.2 A 阈值差距的大部分份额,则本条足以替代 P5,P5 不立项。

**决策**:评估报告需要给出「已下沉列表 + 待下沉但未做列表 + 剩余不可下沉的份额」三部分。

### 4.2 P4 peephole 扩展

**含义**:P4 method-JIT 现在的模板是 per-function 的线性扫描,没有 peephole 优化(guard 合并、常量传播的 peephole 形式、模板内寄存器缓存——见 [../p4-method-jit/03-speculation-ic.md](../p4-method-jit/03-speculation-ic.md) §3.6 和 [../p4-method-jit/04-osr-deopt.md](../p4-method-jit/04-osr-deopt.md) §3.6 已经识别但未启用)。扩展方向:

- **guard 合并**:同一操作数在直线段内多次 IsNumber guard 只查一次(见 [../p4-method-jit/03-speculation-ic.md](../p4-method-jit/03-speculation-ic.md) §3.6);
- **模板内寄存器缓存**:循环变量在 FORLOOP 模板内驻留寄存器(见 [../p4-method-jit/04-osr-deopt.md](../p4-method-jit/04-osr-deopt.md) §3.6,已经列在 PJ11 可能展开的调优里);
- **常量传播的 peephole**:模板内常量 folding(见 [../p4-method-jit/06-backends.md](../p4-method-jit/06-backends.md) §4)。

**评估成本**:中——每条 peephole 需要修改几十行 emit 逻辑,配合 fuzz 差分套。

**如何评价**:

1. 对 §2.1 第二类「循环内的冗余」profile 揭示的具体形式,考察 peephole 能否吃下(比如「同一个 table 的 x 字段每轮查」如果 IC 稳定,是否可以 peephole 缓存);
2. 估算收益边界:peephole 不越过 P4 结构边界——不做跨函数、不做跨迭代——所以只能吃 [§2.1](#2-p4-结构上吃不下的负载类别) 第二类的部分份额,吃不下第一、第三、第四类;
3. 如果估算收益能关闭 §3.2 A 阈值差距的显著份额,则本条可以推迟 P5,先做 P4 peephole 扩展。

**决策**:评估报告需要给出「peephole 可展开的具体列表 + 每条估算收益 + 剩余不可 peephole 的份额」。

### 4.3 宿主侧改造 arena 形状

**含义**:前提一校准测量 2 的教训——脚本级 -37% 加速被宿主端到端的 ±5-7% 噪声吞没,根本原因是宿主侧 VM 调用形式没有对齐列内核。如果宿主侧改造成「一次 Call 进 VM,批处理 N 个 item」的列内核形式,现有的 P4 / P3 收益就能端到端可见;这一改造与 P5 完全正交,可能已经足够关闭端到端差距。

**评估成本**:高——依赖宿主侧的改造工程量。

**如何评价**:

1. 与宿主侧对齐——当前调用形式是 per-item 还是已经是列内核;
2. 如果是 per-item,估算改造为列内核的宿主侧成本(可能小于 +2-4 人年的 P5 投入);
3. 估算改造之后 P4 收益端到端可见的规模——如果可见规模已经达到业务需求,则本条足以替代 P5。

**决策**:评估报告需要与宿主方交涉的记录,包含「宿主可以接受的改造程度 + 估算改造之后的端到端加速比」。

### 4.4 P4 op-set 扩展

**含义**:当前 P4 amd64 native 接了 26 op,arm64 接了 18 op 的线性子集([../p4-method-jit/implementation-progress.md](../p4-method-jit/implementation-progress.md) §14.5 现状)。仍然有一部分 opcode 走 shim path(saveGoG dispatch Go),shim 路径的成本高于 mmap-safe inline——扩展 mmap-safe inline 的覆盖或补齐 arm64 侧,可能显著降低宿主侧 shim 的频率。

**评估成本**:中——每个 op 从 shim 迁到 mmap-safe 需要 exit-reason 协议扩展 + 双 arch 对齐(issue #37/#40)。

**如何评价**:

1. 从宿主 profile 揭示,哪些 op 在热路径上频繁走 shim path;
2. 逐个考察是否可以 mmap-safe 化(部分 op 因为 Lua 语义天然需要 helper,不能 mmap-safe);
3. 估算收益边界:P4 op-set 扩展仍然不越过 P4 结构边界,只能吃 dispatch 层的成本份额,吃不下 §2.1 四类的核心结构损失;
4. 如果估算收益能关闭 §3.2 A 阈值差距的显著份额,则本条可以推迟 P5,先扩 P4 op-set + 闭合 arm64。

**决策**:评估报告需要给出「可 mmap-safe 化的 op 列表 + 每个估算收益 + arm64 端口成本」,并与 issue #37 / #40 状态挂钩。

### 4.5 综合评估的决策规则

四条评估报告齐备之后:

- **如果单条或组合能关闭 §3.2 A/B 阈值差距 ≥ 80%**,P5 立项不通过,先做 cheaper 方案;
- **如果组合能关闭 40%-80%**,P5 立项延后,先做 cheaper 方案交付一批——下一轮判定再看剩余的份额是否值得 P5;
- **如果组合能关闭 < 40%**,P5 立项 §1.1 条件 3 成立,判定继续走 §5 评审议程。

---

## 5. 启动评审议程

### 5.1 议程结构

P5 启动评审是决策会,不是设计评审。议程按以下顺序走(会前评审者应该已经读过 [./00-overview.md](./00-overview.md) + 本文全篇):

1. **§1 三条并集条件核对**——逐条对应证据物件(§3.5),缺一项则会议中止,回补证据后再开;
2. **§4 cheaper alternatives 综合评估**——四条评估报告的结论,决定进入 §5.2 还是转回 cheaper 方案实施;
3. **§5.2 维护性议程**——承接 [./00-overview.md](./00-overview.md) §5.1 风险 4,显性化讨论;
4. **§5.3 团队与资源议程**——人力预算 + fuzz 集群 + 双 arch CI(见 [./00-overview.md](./00-overview.md) §6 施工前置条件);
5. **§5.4 v1-v3 内部阶段验收预览**——见 [./09-acceptance-checklist.md](./09-acceptance-checklist.md) §2 定义;
6. **档位决议**——立项 / 推迟 / 不立项,写档到 [../../../llmdoc/memory/decisions/](../../../llmdoc/memory/decisions/) + [./implementation-progress.md](./implementation-progress.md) §3。

### 5.2 维护性议程(承接 [./00-overview.md](./00-overview.md) §5.1 风险 4)

trace JIT 是永久性的负债——LuaJIT 社区的维护困境是前车之鉴,即便做成了,团队是否长期养得起这台机器?本议程项需要回答:

- **正确性维护成本**:snapshot 机制的 bug 面在 §? 有讨论(见 [./06-snapshot-deopt.md](./06-snapshot-deopt.md) 复杂度评估),差分 fuzz 长时间运行是主防线,预算需要包含**长期 fuzz 集群运营**成本;
- **性能回归防线**:每个 P4 / P3 侧的改动都可能通过共享分析器影响 P5(见 [../../../llmdoc/memory/reflections/2026-07-02-p4-beat-p3-opset-round.md](../../../llmdoc/memory/reflections/2026-07-02-p4-beat-p3-opset-round.md) 教训 5「共享分析器改进可能是 per-backend 的回归」)——P5 加入后回归风险面扩大;
- **人员传承**:trace JIT 的复杂度使得人员离职后的接手成本极高,是否有明确的两人以上核心组来承担(而不是单人英雄工程)。

### 5.3 团队与资源议程

- **人力**:+2-4 人年,至少一名有 trace JIT 或深度 IR 编译器经验的 senior 参与前 6 个月;
- **fuzz 集群**:承接 [./00-overview.md](./00-overview.md) §5.1 风险 2「snapshot 机制正确性收敛不能排期」——正确性置信度是 fuzz 时长的函数,持续 fuzz 是 P5 正确性置信度的**唯一来源**(不是 code review、也不是设计评审);
- **CI**:双 arch(amd64 + arm64)+ 三平台(linux + darwin;win 视 P4 现状)+ Go 版本矩阵(见 [../p4-method-jit/06-backends.md](../p4-method-jit/06-backends.md) §5 双架构 CI 双跑)。

### 5.4 v1-v3 内部阶段验收预览

见 [./09-acceptance-checklist.md](./09-acceptance-checklist.md) §2 定义,P5 立项之后仍然分三档,每档独立可停:

| 档 | 内容 | 停止条件 |
|---|---|---|
| **v1** | 录制器 + 基础优化(FOLD / CSE / DCE / guard-dedup)+ regalloc + snapshot(不含 sink) | v1 达标就已经能实现 §2.1 第一、第二类的大部分收益;如果 v1 达标后 §2.1 第三类分配密集负载份额不足以支撑 v2 投入,可以停 |
| **v2** | 分配下沉 / 逃逸分析 | v2 是 §2.1 第三类的对应手段;如果真实负载中第三类份额小,可以停 |
| **v3** | side trace 树 | v3 处理已录 trace 的热 side exit;如果 v1 / v2 之后 side exit 分布均匀没有热点,可以停 |

评审时必须明确:v1 立项 = 承诺完成 v1 一档;v2 / v3 是 v1 达标之后另判定的续期方案,不在本次立项范围内。

---

## 6. 与 P4 验收数据的关系

### 6.1 P4 验收是 P5 立项的基线

P4 验收数据是 P5 立项判定的**输入 baseline**——具体来自 [../p4-method-jit/09-acceptance-checklist.md](../p4-method-jit/09-acceptance-checklist.md) §3 + 项目 `README.md` perf table。以下数据在 P5 立项判定时**必须**引用:

- **V14 luajc 档三平台数据**:amd64 14.08x / linux-arm64 25.28x / darwin-arm64 13.34x over gopher-lua(见 [../p4-method-jit/09-acceptance-checklist.md](../p4-method-jit/09-acceptance-checklist.md) §3 V14),作为 §3.2 A 阈值的参照基线;
- **V15b heavy 三平台数据**:amd64 5.53x / linux-arm64 5.45x / darwin-arm64 4.00x geomean over gopher-lua(同上 §3 V15b),作为「P4 已经实现列内核核心 loop / heavy 形式」的证据;
- **V15a realworld 三平台数据**:P4/gopher geomean 0.83x / 0.84x / 0.84x(同上 §3 V15a),作为「P4 在 helper-bound 负载上端到端弱于 gopher」的证据——如果首个宿主的真实负载类似 realworld(表 / 字符串 / CALL 重型),P5 立项的动机就减弱了(P5 也是 VM 层的加速,端到端仍然受 helper-bound 限制)。

### 6.2 已知的 P4 结构性损失作为 P5 立项证据候选

以下已知的 P4 结构性损失可以作为 P5 立项 §1.1 条件 2 的证据(每一条都是「P4 在该形式上收益不足其标量内核收益」的实证):

- **fannkuch / nbody 类 helper-bound 负载**:V15a 表显示 P4/P3 ≈ 1x——P4 native emit 在 helper-bound 上没有边际收益;如果宿主负载类似,P5 也无解(P5 同样是 VM 层加速),转到 cheaper alternative 4.3 宿主侧改造;
- **arm64 P4 HeavyArith 主动回归**(issue #40):amd64 op 集扩面没有 port 到 arm64,arm64 P4 反而比 P3 慢 ~20x——与 P5 立项无关(单纯是 P4 amd64/arm64 分岔),但 P5 立项前需要先闭合(见 [./00-overview.md](./00-overview.md) §3.3);
- **P3 nbody 回归**(issue #39):共享 analyzer alias 追踪改进的 per-backend 副作用(P3 43.5→89.7ms,慢了 2x)——与 P5 立项无关,但示范了「共享分析器修改是 per-backend 回归的来源」这一维护性风险(§5.2 议程项);
- **F2-b 静态分析限制**:call 核 body 里包含 ReasonUnknownCall 时 P4 / P3 都不升(见 [../p3-wasm-tier/implementation-progress.md](../p3-wasm-tier/implementation-progress.md) §14.10);如果宿主的真实热脚本大量触发 F2-b,是 P5 立项 §2.1 第一类的关键证据,同时也是 cheaper alternative 4.1 stdlib 内建化 + 4.4 F2-b 放宽的候选目标。

### 6.3 P4 立项判定与 P5 立项判定的对偶

见 [../p4-method-jit/01-launch-judgment.md](../p4-method-jit/01-launch-judgment.md) §6.4 对偶表,两个阶段的立项判定在形式上是平行的:

| 维度 | P4 立项(见 P4 §1) | P5 立项(本文) |
|---|---|---|
| 硬前置 | P3 已交付 + 宿主负载证据 + 资源到位 | P4 已交付 + 宿主负载证据 + 资源到位 |
| 反向问题 | P3 收益是否已经够了? | P4 收益是否已经够了? |
| 关键追问 | P4 原生后端能否突破 P3 架构边界? | P5 trace JIT 能否突破 P4 结构边界?(§2 四类) |
| 三档策略 | 全启 / 部分前置 / 跳过 | 立项(v1) / 推迟(先做 cheaper) / 不立项 |

**结构上相似 + 判据内容不同**是设计意图——wangshu 项目流水线后段(P3 之后)统一采用「立项判定先于实施」的模式,这是 [../roadmap.md](../roadmap.md) §5 原则 3 的工程化体现。

---

## 7. 开放问题

以下问题在 P5 立项判定运行之前无法完全确定,记入 [../../../llmdoc/memory/doc-gaps.md](../../../llmdoc/memory/doc-gaps.md),属于有意推迟:

| 问题 | 待解时点 |
|---|---|
| §3.2 A/B 阈值的具体数字校准 | P4 验收数据到位之后,P5 立项判定启动之前的预登记会 |
| 首个宿主真实热脚本清单与 profile 数据 | 宿主侧提供之后 |
| §2.1 四类负载在宿主端到端的实际占比 | 侦察任务完成之后(§2.2) |
| §4 四条 cheaper alternatives 评估报告 | 各条评估执行之后 |
| v1 / v2 / v3 阈值细节(见 [./09-acceptance-checklist.md](./09-acceptance-checklist.md) §2) | 立项通过之后设计定稿时 |
| P5 与 P4 arm64 现状(issue #37 exit-reason 端口未闭)的兼容处理 | issue #37 闭合之后 |
| P5 与 P3 主动保留的差分矩阵扩展影响 | 立项后 [./08-testing-strategy.md](./08-testing-strategy.md) 完成时 |

### 7.1 元风险:立项判定本身的风险

承接 [../p4-method-jit/01-launch-judgment.md](../p4-method-jit/01-launch-judgment.md) §7.6 元风险模板,P5 立项判定的元风险:

- **判据局部化**:只看标量内核数据(P4 已经很接近 luajc 档)或者只看合成 bench(P3/P4 bench 中 call 核 0.52x)就下结论,忽略 §2.2 宿主真实负载证据——决策树(§1.1 三条并集)强制不允许跳分支;
- **乐观估算**:P5 收益估高了——把 P5 收益锚在 LuaJIT 档而不是真实 trace JIT 能吃到的份额(受到 §5.1 风险 3 纯 Go 全部显式 guard 折损的影响);
- **悲观估算**:因为宿主负载证据不完整或 profile 数据有噪声就否决 P5——应该回补数据,而不是直接否决;
- **不可逆性带来的过度谨慎**:立项后 +2-4 人年是大投入,倾向于「再等等」推迟到下一轮——但等到宿主已经适配了 gopher-lua 或 luajc,P5 立项的机会窗口可能已经关闭。

**纪律**:元风险共同决定了立项判定不是孤立判断,而是流程化决策——按 §5.1 议程走完每个节点,数据进档,签署人锁定阈值,才算完整。

---

## 8. 不变式清单(本文承担)

承接 [./00-overview.md](./00-overview.md) 的全 P5 不变式,本子文档承担以下三条立项判定级别的不变式:

### 8.1 「P5 是备选方案,不是既定计划」

承接 [./00-overview.md](./00-overview.md) §1.2。P5 不是流水线上 P4 之后自动启动的项目。启动条件 = §1.1 三条并集;不启动的代价 = 零(立项判定过程本身的产出就有独立价值)。任何「P4 完成了下一阶段就是 P5」的工程惯性思维,都违反本条不变式。

### 8.2 「立项前先证明负载证据,而不是工程上的想法」

承接 §1.1 条件 1 + §2.2 侦察任务 + [../../../llmdoc/guides/design-claims-vs-codebase-physics.md](../../../llmdoc/guides/design-claims-vs-codebase-physics.md) 同源的纪律。P5 立项的核心驱动力必须是首个目标宿主的真实负载证据,而不是「wangshu 已经实现了 P4,自然下一步就是 P5」这种工程叙事。正确的立项叙事是「宿主有真实需求 + P4 收益不到位 + P5 后端能解决 + cheaper alternatives 已经耗尽」。

### 8.3 「10-30x 是宽带,不是精确锚」

承接 [./00-overview.md](./00-overview.md) §5.1 风险 3 + [../roadmap.md](../roadmap.md) §4「10-30x over gopher-lua」验收区间。P5 验收 10-30x 是一个宽区间,反映的是纯 Go 全部显式 guard 对 trace 收益折损的不确定性。任何「P5 必须做到 30x 上沿」的提案都违反本条不变式——上沿不达也不等于 P5 失败,达到 10x 就已经完成了 [../roadmap.md](../roadmap.md) §0 终局目标的下沿。

---

## 相关

- [./00-overview.md](./00-overview.md)(P5 总览,备选方案本质 + 章节地图)
- [./09-acceptance-checklist.md](./09-acceptance-checklist.md)(v1-v3 内部阶段验收定义 + T1-T? 验证矩阵)
- [./implementation-progress.md](./implementation-progress.md)(施工分档 PT0-PT9;立项凭据归档点)
- [../roadmap.md](../roadmap.md)(§4 P5 定义、§0 终局目标、§1 校准测量、§5 五条贯穿原则)
- [../../../llmdoc/must/design-premises.md](../../../llmdoc/must/design-premises.md)(前提一 6% 校准、前提三五条原则)
- [../../../llmdoc/architecture/evolution-roadmap.md](../../../llmdoc/architecture/evolution-roadmap.md)(P5 速查行、启动条件)
- [../../../llmdoc/overview/project-overview.md](../../../llmdoc/overview/project-overview.md)(项目身份 + 首个目标宿主)
- [../../../llmdoc/guides/design-claims-vs-codebase-physics.md](../../../llmdoc/guides/design-claims-vs-codebase-physics.md)(主张必须有证据,不能凭直觉——本文 §1.1 + §8.2 同源纪律)
- [../../../llmdoc/guides/perf-optimization-workflow.md](../../../llmdoc/guides/perf-optimization-workflow.md)(profile 才是合同——§4 alternative 评估同源)
- [../../../llmdoc/memory/reflections/2026-07-02-p4-beat-p3-opset-round.md](../../../llmdoc/memory/reflections/2026-07-02-p4-beat-p3-opset-round.md)(P4 amd64/arm64 分岔 + 共享分析器 per-backend 回归——§5.2 维护性议程 + §6.2 已知 P4 损失候选来源)
- [../p4-method-jit/00-overview.md](../p4-method-jit/00-overview.md)(P4 总览,§6.4 对偶表参照)
- [../p4-method-jit/01-launch-judgment.md](../p4-method-jit/01-launch-judgment.md)(P4 立项判定,本文的形式参照)
- [../p4-method-jit/09-acceptance-checklist.md](../p4-method-jit/09-acceptance-checklist.md)(V14/V15a/V15b 验收数据——§6.1 P5 立项 baseline)
- [../p4-method-jit/implementation-progress.md](../p4-method-jit/implementation-progress.md)(P4 交付现状 + 已知 arm64 分岔 / P3 nbody 回归——§6.2 证据候选)
- [../p3-wasm-tier/implementation-progress.md](../p3-wasm-tier/implementation-progress.md)(§14.10 call 核架构边界——§6.2 F2-b 限制)
- [../p2-bridge/00-overview.md](../p2-bridge/00-overview.md)(热度 + feedback 前端,§2.1 类别 4 megamorphic 数据源)
- [../p2-bridge/02-ic-feedback.md](../p2-bridge/02-ic-feedback.md)(§4 FBTableMega/FBUnstable——§2.1 类别 4 定义)
- [../p1-interpreter/06-memory-gc.md](../p1-interpreter/06-memory-gc.md)(自管 mark-sweep,§2.1 类别 3 分配密集负载依赖)
- [./02-trace-recording.md](./02-trace-recording.md)(录制机制,§2 类别对应手段的落点)
- [./04-optimization-passes.md](./04-optimization-passes.md)(CSE/LICM 落点)
- [./06-snapshot-deopt.md](./06-snapshot-deopt.md)(sink pass 落点)
