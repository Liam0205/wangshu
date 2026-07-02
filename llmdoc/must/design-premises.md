# 不可妥协的设计前提(MUST)

> 状态:**P1 + P2 + P3(PW0-PW10)全卷已收口,P4(method JIT)多 PJ 已落地(含 PJ10 native emit,2026-07-01)**。四组前提不变,且已获多轮实测确认(P1 三档 ≥2x over gopher-lua、NaN-box 去装箱即主力收益;P3 loop 核 7.2x over gopher-lua 超 luajc 档;P4 native 三本 heavy bench 超 P3 wasm)。本文档凝练的是 Wangshu(望舒)项目的设计意图与硬约束。
> 唯一源头:`docs/design/roadmap.md`。
> 这是每次任务都应先读的 MUST 文档:下面四组前提决定了几乎所有后续技术决策的合理性边界,违背它们的提案基本可以直接判否。

Wangshu 是一个**纯 Go(不依赖 cgo)、可交叉编译的高性能嵌入式 Lua VM**,采用分层 VM 架构(解释器 → 分层桥 → Wasm 编译层 → method JIT → trace JIT)。项目身份与目标见 [[project-overview]]。本文档只保留"为什么这些前提不可妥协"的因果链与量化锚点。

---

## 前提一:负载形状必须是「列内核」(由两个校准测量推出)

项目的全部收益,只在宿主以**列内核**形状调用 VM 时才能兑现:**循环写在 Lua 内,一次调用进一次 VM,整批数据在 VM 内迭代**,而不是 per-item 反复跨界。

这不是偏好,而是被两个同机同日隔离 A/B 测量(16 核 Intel Xeon 6982P-C,per-item 粒度调用,测 ns/op)钉死的结论:

- **校准测量 1(Horner 5 次多项式循环,1000 items)** —— `docs/design/roadmap.md` (§1):

  | 嵌入栈 | 绝对值 | 技术 |
  |---|---|---|
  | gopher-lua(Go) | 729μs | 纯解释,interface 装箱,switch dispatch |
  | LuaJ-luac(Java) | 259μs | JVM 解释器,本体被 C2 编译热 |
  | LuaJ-luajc(Java) | 164μs | Lua→JVM bytecode,C2 全套优化 |
  | LuaJIT(C++) | 154μs | trace JIT,NaN-boxing |

  **关键事实:真 LuaJIT 只比 luajc 快 6%(154 vs 164μs)。** 在 per-item 跨界形态下,**边界跨越 + 值装箱主导成本**,脚本本体再快也被钉死。
  → 这是「纯 Go 不必复刻 trace JIT 也能逼近顶档」的核心论据。

- **校准测量 2(某生产规则引擎,启用 luajc)** —— `docs/design/roadmap.md` (§1):
  - 隔离脚本级:**-37%**(脚本本身明显加速);
  - 宿主端到端 benchmark:**前后对照全部落在 ±5-7% 噪声带内**(加速端到端不可见)。
  - 原因:该宿主生产脚本绝大多数是**单行判断**,**边界成本主导**,VM 层加速被稀释到看不见。

**推论(不可妥协)**:若新提案让 VM 重新滑回 per-item 反复跨界形态,无论 VM 本体多快,端到端收益都会被边界成本吃光。宿主侧改造不在本项目范围,但本项目的嵌入接口必须**天然鼓励列内核形状**(见 [[embedding-contract]])。

## 前提二:Go runtime 四项税 → 边界跨越是几十~百 ns 固定成本

任何在 Go 进程内**生成/执行机器码**的路线都要过这四关。标准解法均「wazero 已验证」—— `docs/design/roadmap.md` (§2):

| 税 | 问题 | 标准解法(wazero 已验证) |
|---|---|---|
| GC 精确栈扫描 | JIT 帧无 stack map | JIT 代码跑自管非 Go 栈,边界按 syscall 语义 |
| 异步抢占 | 抢占信号可落在任意 PC | 生成代码在**循环回边插抢占检查点** |
| 栈移动 | morestack 拷贝 goroutine 栈 | JIT 代码**不持有指向 Go 栈的指针** |
| 写屏障 | 裸指针写破坏并发 GC 三色不变式 | 值世界放**自管 arena/linear memory**,边界拷贝 |

**关键推论:VM 边界跨越是几十~百 ns 的固定成本,短脚本会被吃光。** 这与前提一互相强化——边界既贵又有 GC 纪律成本,所以设计上**必须减少跨界次数**。四项税同时直接决定了:值世界不能住 Go 堆,必须放自管 arena(见 [[value-representation]]);编译层必须跑自管栈、边界拷贝、回边插检查点(见 [[evolution-roadmap]])。

## 前提三:五条贯穿原则(方法论底座,贯穿 P1-P5)

`docs/design/roadmap.md` (§5)。任何阶段的设计都须服从这五条:

1. **解释器永不退役** —— 它是所有编译层的 **deopt 着陆点和语义 oracle**。
2. **层间逐字节差分测试** —— 每个执行层的输出与解释器 **byte-equal**,持续 fuzz。这是防「**投机错误静默错果**」(JIT 最危险的 bug 类别)的**主防线**,直接对应 P1/P3 验收里的「差分 fuzz 逐字节一致」。
3. **每阶段独立交付价值** —— **任何闸门处停下都不亏**。
4. **不可编译/不可升层形状走 fallback,不做完备性** —— 可静态分析的子集走快路径即可覆盖绝大多数真实负载(Pallene 是 typed-subset 路线先例;审计过的一个 **262 脚本生产库** 中绝大多数是简单形状)。
5. (由 §3 锁定的)**第一天值表示承诺**,见前提四。

> 原则 4 是「走 fallback」与「做完备性」的分水岭:遇到 varargs / coroutine / debug 等形状,标记「不升层、永远走解释」,而不是为它们补编译路径。

## 前提四:第一天即锁定的值表示承诺(不可逆)

`docs/design/roadmap.md` (§3)。这是第 1 天就必须做对、日后无法低成本回头的决定:

- **选定:NaN-boxed u64 + 自管 arena**(线性内存 `[]uint64` / `[]byte`),而非 Go 原生 tagged struct(住 Go 堆)。
- **为什么不可逆**:分层 VM 的成败取决于各执行层能否**共享同一份值表示与对象模型**。NaN-boxing + 自管 arena 让解释器与未来编译层**读写同一块线性内存**,使上编译层成为**纯增量**;若第一天选了 Go tagged struct,日后上编译层等于**重写整个对象层**。
- **代价(必须自付)**:**自写 mark-sweep GC**,纪律约束是 **safepoint 限定在分配点与层边界**、**根放 shadow stack**。
- **部分补偿**:NaN-boxing 数字**零分配**,本身已显著快于 interface 装箱。

完整决策细节见 [[value-representation]];该选型如何在各阶段映射成同一块共见内存,见 [[evolution-roadmap]]。

---

相关:[[project-overview]] · [[evolution-roadmap]] · [[value-representation]] · [[embedding-contract]] · [[glossary]]
