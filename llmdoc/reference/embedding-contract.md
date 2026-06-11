# 参考:宿主嵌入契约

> 状态:**设计阶段,接口为设计意图,字段级 spec 尚未定稿**。源:`docs/design/roadmap.md` (§8),量化背景见 (§1)。
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

> ABI 字段级细节(字符串区编码、presence bitmap 布局、`args` 与 arena 的精确关系)在源文档中**仅给概念,未给 spec**,属当前文档缺口。

这块 arena 与 [[value-representation]] 的自管线性内存是同一份内存的不同视角——「零拷贝读」之所以成立,正因为 VM 各层值世界本就住在这块共见内存里(Arrow 数据搬家模型,替代「让 Lua 直访 Go 堆」,见 [[project-overview]] 非目标)。

## 不强制 arena 的简易 API

- **per-item 风格的简易 API 照常提供**(对标 gopher-lua 易用性);
- 但文档**明确标注其性能档位**——它走的是 per-item 跨界形态,落在被边界成本主导的那一档(见 [[design-premises]] 前提一)。

## 宿主绑定与 drop-in

- **首个目标宿主**:一个**多运行时规则引擎**(其 Go 运行时现用 gopher-lua);但接口**不绑定任何宿主**。
- **P1 解释器即可作为 gopher-lua 的 drop-in 候选**(见 [[evolution-roadmap]] P1)。

---

相关:[[design-premises]] · [[value-representation]] · [[evolution-roadmap]] · [[project-overview]] · [[glossary]]
