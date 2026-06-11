# 参考:宿主嵌入契约

> 状态:**设计阶段,字段级 spec 已定稿**于 `docs/design/p1-interpreter/11-embedding-arena-abi.md`。概念源:`docs/design/roadmap.md` (§8),量化背景见 (§1)。本文只保留契约形状,字段细节查 11。
> 这套接口**刻意设计为鼓励「列内核」形状**——为什么必须如此,见 [[design-premises]]。

## 设计意图:逼着宿主走列内核形状

列内核形状 = 循环写在 Lua 内,**一次调用进一次 VM,整批数据在 VM 内迭代**,而不是 per-item 反复跨界。两个校准测量(§1)证明 per-item 跨界会被边界成本吃光收益,因此嵌入接口被设计成天然鼓励列内核;宿主侧配套改造不在本项目范围。

## 核心 API

| 接口 | 语义 |
|---|---|
| `Compile(script) → Program` | **一次编译**,含**可编译性探测与层级决定** |
| `Program.Call(arena, args)` | **一次调用一次跨界**;批量数据经 arena 传递 |

- `Compile` 在编译期就完成可编译性探测与升层决策(对应 [[evolution-roadmap]] P2 的静态可编译性分析)。
- `Program.Call` 的设计要点是把「跨界」压缩到每批一次——这是列内核形状在 API 层面的落地。

## arena ABI

宿主直接写,**VM(解释器与编译层)零拷贝读**。布局:

- **类型化扁平列**:`[]float64` / `[]int64` / `[]bool`;
- **字符串区**;
- **presence bitmap**(标记每个槽位是否有值)。

> ABI 字段级细节已定稿于 `docs/design/p1-interpreter/11-embedding-arena-abi.md`:类型化扁平列编码(§3.3)、字符串区 offset 表+字节池(§3.3.4)、presence bitmap 位序(§3.4)、`args` 与 arena 的精确关系(§4.3)、句柄表(§6)、per-item 简易 API(§7)。本文不搬运字段细节,查 11。

这块 arena 与 [[value-representation]] 的自管线性内存是同一份内存的不同视角——「零拷贝读」之所以成立,正因为 VM 各层值世界本就住在这块共见内存里(Arrow 数据搬家模型,替代「让 Lua 直访 Go 堆」,见 [[project-overview]] 非目标)。

## 不强制 arena 的简易 API

- **per-item 风格的简易 API 照常提供**(对标 gopher-lua 易用性);
- 但文档**明确标注其性能档位**——它走的是 per-item 跨界形态,落在被边界成本主导的那一档(见 [[design-premises]] 前提一)。

## 宿主绑定与 drop-in

- **首个目标宿主**:一个**多运行时规则引擎**(其 Go 运行时现用 gopher-lua);但接口**不绑定任何宿主**。
- **P1 解释器即可作为 gopher-lua 的 drop-in 候选**(见 [[evolution-roadmap]] P1)。
- **stdlib 默认面对齐 gopher-lua 的 OpenLibs 提供面**(兑现 drop-in 宣称);宿主可经 Options 三层收紧:**LibsSafe 预设 / Libs 位掩码 / Exclude 函数级**——收紧能力是 VM 责任,收紧决策是宿主责任。细节见 `docs/design/p1-interpreter/10-stdlib.md` §12.1、`11-embedding-arena-abi.md` §1.2;决策背景见 `memory/decisions/2026-06-11-design-review-decisions.md` 第 6 项。

---

相关:[[design-premises]] · [[value-representation]] · [[evolution-roadmap]] · [[project-overview]] · [[glossary]]
