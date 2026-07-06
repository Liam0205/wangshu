# 项目概览:Wangshu(望舒)

> 状态:**P1(crescent 解释器)+ P2(分层桥)+ P3(Wasm 编译层 PW0-PW10)全卷已收口;P4(method JIT)多 PJ 已落地(PJ0-PJ11 + PJ10 native emit,2026-07-01),P5 仍为规划**。`docs/design/` 约 44 篇约 3.8 万行设计文档仍是规范源:P1/P2/P3/P4 全卷 + implementation-progress 详细设计深度、P5 架构决策。本文档描述项目身份与边界;实现现状见下「当前状态」节。

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

- **P1(crescent 解释器)完整交付**(2026-06-12):M0-M14 全里程碑 + 收尾轮完成,后续历经测试加固轮、完整性补全轮、长稳承诺轮(freelist 内存复用/调用深度上限/GC 根加固/并发验证)、外部审查修复轮(12 轮审查 22+ 项发现全核销)与官方测试套与性能轮(官方 5.1.5 套移植扫出 20 项分歧全修 + profile 驱动六项优化)。验收:三档基准 **simple 9.0x / arith 7.0x / loop 2.45x** over gopher-lua(性能轮主增益来自 State.Call 复用主 thread 消短脚本固定开销与 closeUpvals 快路径,归因见 `benchmarks/` 与 README);realworld 五项中四项反超 gopher-lua(fannkuch 0.82x 为剩余短板);与官方 Lua 5.1.5 difftest **70 种子 + 200 随机脚本逐字节一致**;`make all` 全绿。代码:`internal/`(frontend/crescent/stdlib 等)+ `wangshu.go` / `arena_abi.go`(公共 API 含 `Program.Call(state, arena, args)` 与 arena 列接口)+ `test/conformance` + `test/difftest`(固定用例 + 随机生成器 + probe corpus)+ `test/luasuite`(官方测试套,stopAt 棘轮)+ `benchmarks/baseline` 三档 + `benchmarks/realworld` 五脚本。
- **收尾轮已把原「已知简化」清单全部落地**(arena 原生表存储、IC 命中路径、协程、pattern matcher、stdlib 补全、错误前缀+traceback、弱表/finalizer、arena ABI 列接口、difftest 随机生成器)。实现形态与设计文档的差异(均接口等价)及 **P3 迁移留口**(值栈/CallInfo arena 化等)见 `docs/design/p1-interpreter/implementation-progress.md` 对账表。
- **P2(分层桥)+ P3(Wasm 编译层)全卷已收口**:P2 PB0-PB7 + 后续优化轮 #1-#4(热度计数 / IC 反馈聚合 / 可编译性闸门 / TierState 状态机);P3 PW0-PW10(spike 闸门 → 全 38 opcode 除 VARARG 翻译成 Wasm → 跨层互调 → 线程级 tier 规则 → V1-V18 端到端验收 → PW10 消除 gibbous→gibbous 跨层调用税)。**P3 列内核 loop 形态已超 luajc 档**(loop 2.95x over P1 = 7.2x over gopher-lua > luajc 4.4x);table/call/mixed 仍 ≪ luajc 档,P4 立项动机在非 loop 形态。详见各目录 `implementation-progress.md`。
- **P4(method JIT)多 PJ 已落地**(2026-07-01;amd64 段到段 CALL 于 2026-07-06):PJ0-PJ11 投机模板 + 表 IC + FORLOOP inline + **PJ10 通用 per-opcode 翻译器 + amd64/arm64 native emit 真接入**(V15b heavy 三本 P4 native > P3 wasm 达标);跨 arch 三平台矩阵 CI(linux/arm64 校验翻 true,darwin/arm64 codepage 真实装)。**amd64 段到段(seg2seg)CALL 分派已交付**(段内 GETUPVAL inline + 双语义 RETURN 扩到 MultiReturn proto + arith/compare/GETUPVAL/嵌套 CALL 接受面,消掉每次 gibbous→gibbous 调用的 exit-reason 往返):call 密集内核反超 gopher-lua,fib 10.5x / fannkuch 6.9x / binary-trees 1.35x over gopher-lua;arm64 段到段 CALL 因本地无执行环境推迟到 issue #61。剩 `archSupportsSpec` 物理 runner 打开 / OSR exit 接上。详见 `docs/design/p4-method-jit/implementation-progress.md` §15.7。
- **设计文档集仍是规范源**:`roadmap.md`(战略)+ `architecture.md`(跨阶段总览,§0 是文档集地图)+ `p1-interpreter/`(全卷 00-12)+ `p2-bridge/` / `p3-wasm-tier/` / `p4-method-jit/`(各子目录含 implementation-progress)+ `p5-trace-jit/`(子目录 11 文件,未立项图纸)。
- **P5 仍为规划**(仅在 P4 收益不够时启动)。

---

相关:[[design-premises]] · [[evolution-roadmap]] · [[value-representation]] · [[embedding-contract]] · [[glossary]]
