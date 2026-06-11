# 参考:术语表

> 项目术语速查。源:`docs/design/roadmap.md`。状态:**设计阶段**,术语为设计语境下的定义。

## 核心概念

- **列内核负载形状(column-kernel)** —— 收益兑现的前提形状:循环写在 Lua 内,**一次调用进一次 VM,整批数据在 VM 内迭代**,而非 per-item 反复跨界。所有性能倍率均以此形状为口径。详见 [[design-premises]]。

- **NaN-boxing** —— 把 Lua 各类型值编码进一个 64 位 IEEE-754 双精度的 NaN 空间(`u64`),数字**零分配**。选定为第 1 天值表示,使分层各执行层共享同一份表示。详见 [[value-representation]]。

- **arena / 线性内存(linear memory)** —— 自管的 `[]uint64` / `[]byte` 块,值世界住在这里而非 Go 堆(规避 Go 写屏障税)。同一块内存贯穿解释器值世界、Wasm linear memory、宿主 ABI;宿主直接写、VM 零拷贝读。详见 [[value-representation]] / [[embedding-contract]]。

- **mark-sweep GC** —— 自写的标记-清扫垃圾回收器,是 NaN-boxing 自管内存的必付代价。safepoint 限分配点与层边界,根放 shadow stack。

- **shadow stack** —— 自管的 GC 根集合栈,使 GC 不依赖 Go 的精确栈扫描即可找到根。

- **safepoint** —— 允许 GC 介入的受控位置;本项目限定在**分配点与层边界**。

- **tier(执行层级)** —— 执行层的编号。tier 比演进阶段粗一层:tier-0=P1、tier-1=P3+P4、tier-2=P5;P2 是基建不分配 tier。详见 [[evolution-roadmap]]。

- **crescent / gibbous / fullmoon(新月/凸月/满月)** —— 执行层的月相命名,与项目名同一意象系,代码/文档/日志统一使用。crescent=tier-0(P1 解释器)、gibbous=tier-1(P3 Wasm/P4 method JIT)、fullmoon=tier-2(P5 trace JIT)。日志形如 `function promoted to gibbous`。

- **deopt(去优化)** —— 投机编译失败时从编译层回退到解释器执行;解释器永不退役,正因它是所有编译层的 deopt 着陆点与语义 oracle。P4 用函数级 **OSR exit** 实现。

- **列内核(column-kernel,与负载形状同义)** —— 见上「列内核负载形状」。

- **try-compile-fallback-interpret** —— P2 分层桥策略(LuaJ luajc 同款):尝试编译,不可编译/不可升层形状走 fallback 永远解释,换来**零 deopt 机器**。对应贯穿原则 4「走 fallback,不做完备性」。

- **inline cache(IC)** —— 全局/表访问的内联缓存;P2 记录其类型 feedback 为编译层供料,P4 据此做类型投机(f64 快速路径 + guard)。

- **arrow 数据搬家模型** —— 替代「让 Lua 直访 Go 堆」的方案:宿主把热数据放进双方共见的 arena,VM 零拷贝读。详见 [[project-overview]] 非目标。

## Prior art(参照项目与借鉴点)

源:`docs/design/roadmap.md` (§7)。

| 项目 | 借鉴点 |
|---|---|
| **wazero** | 纯 Go 运行时 codegen **存在性证明**;exec mmap / W^X / 自管栈 / trampoline 参考实现;系统管线采石场 |
| **LuaJ luajc** | 「编译到宿主已有 codegen 引擎」范式(P3 精神来源);**try-compile-fallback 策略** |
| **LuaJIT** | **NaN-boxing**、**trace JIT 架构**、**Lua 5.1 语义基准** |
| **gopher-lua** | **反面教材**:interface 装箱 + switch dispatch 的成本上限;**P1 的性能基准与同生态参照**(语义 oracle 是官方 Lua 5.1.5,gopher 偏离官方处登记豁免) |
| **goja(纯 Go ES5)** | 纯 Go 解释器路线的**天花板参照** |
| **Pallene** | **typed-subset 编译 + fallback** 的可行性先例(贯穿原则 4 的先例) |
| **V8 Ignition→TurboFan / JSC Baseline→DFG→FTL** | 分层 VM 标准阶梯;**解释器先行**;P4 取 JSC Baseline 风格 |

---

相关:[[design-premises]] · [[value-representation]] · [[evolution-roadmap]] · [[embedding-contract]] · [[project-overview]]
