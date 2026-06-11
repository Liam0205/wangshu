# 项目概览:Wangshu(望舒)

> 状态:**设计文档集全卷齐备,无代码实现**。`docs/design/` 共 19 篇约 1.37 万行:P1 全卷 00-12 可实现深度、P2/P3 详细设计、P4/P5 架构决策。本文档描述的是项目身份与设计意图,不是已交付的能力。

## 一句话定位

Wangshu(望舒)是一个**纯 Go 实现的高性能嵌入式 Lua 虚拟机**,关键约束是**不依赖 cgo,以保持交叉编译能力**(`docs/design/roadmap.md` §0)。它采用分层 VM 架构,目标是把 Go 生态的 Lua 执行性能从 gopher-lua 档拉到 LuaJIT 档。

## 命名意象

- **望舒**:中国神话中为月亮驾车的神,首见于《楚辞·离骚》("前望舒使先驱"),后世直接用作月亮代称(诗人戴望舒笔名同源)。
- **意象闭环**:Lua 在葡萄牙语中意为「月亮」→「为月亮驾车,即驱动 Lua 的引擎」→ 这正是一个 Lua VM 的本职;命名与项目本质同构。
- **巧合加持**:系外行星 HD 173416 b 在 IAU 2019 全球命名活动中被命名为 **Wangshu**,其宿主恒星名为 **Xihe(羲和,太阳御者)**——日月御者成对挂在天上。
- 这套意象**贯穿到执行层命名**:各执行层以月相命名(crescent / gibbous / fullmoon),详见 [[evolution-roadmap]]。

命名空间核查(2026-06-11,`docs/design/roadmap.md` §0):pkg.go.dev / LuaRocks / PyPI / Homebrew 均空闲,无同名软件项目;GitHub 个人用户名被占,组织名可用。

## 三层目标(§0)

| 目标层 | 内容 | 量化锚点 |
|---|---|---|
| 近期目标 | 把 Go 生态的 Lua 执行性能从 **gopher-lua 档** 提升到 **LuaJ-luajc 档以上** | — |
| 终局目标 | 在「列内核」负载形状下逼近 **LuaJIT 档** | 纯 Go 约束下 **10-30x over gopher-lua** |
| 语言面 | **Lua 5.1 核心**(与 LuaJIT 一致;嵌入式宿主生态的最大公约数)| 不追语言完备性 |

> 所有倍率均以「**over gopher-lua,列内核负载形状下**」为口径。为什么收益依赖列内核形状,见 [[design-premises]]。

## 边界:非目标(§6)

项目刻意**不做**以下三类事,这是边界,不是待办:

| 非目标 | 为什么不做 | 替代方案 |
|---|---|---|
| **让 Lua 直接访问 Go 堆对象** | 需全套 GC 纪律税(handle 根表、`runtime.Pinner`、指针写 host call、宿主布局影子描述符);且 Go 内置容器内存布局**每版漂移** | **数据搬家**:宿主把热数据放进双方共见的 arena(**Arrow 模型**),VM 零拷贝读。本项目只需定义 arena ABI(见 [[embedding-contract]]) |
| **复刻 Go runtime 内部机制** | 绝不 inline 复刻 `runtime.gcWriteBarrier` / `runtime.mallocgc` 等内部符号(`go:linkname` **每个 Go 版本都可能碎**) | 直接禁止 |
| **Lua 5.2+ 特性**(goto / `_ENV` / 整数子类型 / 位运算符 / utf8 库等) | — | **5.1 核心是嵌入生态事实标准,LuaJIT 同样停在这里** |

## 首个目标宿主与定位

- **首个目标宿主**:一个**多运行时规则引擎**(其 Go 运行时现用 gopher-lua);但接口**不绑定任何宿主**。
- **drop-in 定位**:P1 解释器即可作为 **gopher-lua 的 drop-in 候选**(见 [[evolution-roadmap]] P1、[[embedding-contract]])。

## 当前状态

- **设计文档集全卷齐备**(2026-06-11):`docs/design/` 19 篇约 1.37 万行——`roadmap.md`(战略)+ `architecture.md`(跨阶段总览/包布局,§0 是文档集地图)+ `p1-interpreter/` 13 篇(00 总览/跨文档决策速查 + 01-12 全部子系统,可实现深度,12 为验收口径收口点)+ p2-bridge / p3-wasm-tier(详细设计)+ p4-method-jit / p5-trace-jit(架构决策)。
- `README.md` 已重写为项目简介 + 文档导航。
- **仍无任何 Go 源码实现**、无 `benchmarks/baseline/`(校准测量原始数据留存于发起方仓库工作区,入库规划见 `docs/design/p1-interpreter/12-testing-difftest.md` §6)。

---

相关:[[design-premises]] · [[evolution-roadmap]] · [[value-representation]] · [[embedding-contract]] · [[glossary]]
