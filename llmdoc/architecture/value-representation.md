# 架构:值表示与内存模型

> 状态:**第 1 天架构承诺,已在 P1 完成**(NaN-boxed u64 + 自管 arena + mark-sweep GC + arena 原生表存储均已实现;长稳承诺轮后 **freelist 内存复用已完成**——20 个 size-class 定长桶 + LARGE 首次适配(`internal/arena/freelist.go`),sweep 真正归还字节供复用,并配套调用深度上限(Lua 20000 / host→Lua 重入 200,超限抛可恢复错误,proper tail call 不受限)与对象尺寸单一事实源(`internal/object/size.go`);值栈/CallInfo 仍为 Go slice,P3 迁移留口不变,清单见 `docs/design/p1-interpreter/implementation-progress.md`)。源:`docs/design/roadmap.md` (§3),硬约束背景见 (§2)。
> 这是整个分层 VM 的中枢决策——一块自管线性内存贯穿值表示、各执行层、宿主 ABI。前置约束见 [[design-premises]]。

## 岔路口决策:NaN-boxing vs Go tagged struct

分层 VM 的成败取决于各执行层能否**共享同一份值表示与对象模型**。第 1 天必须在两条路里选一条,且日后无法低成本回头:

| 方案 | 值住哪 | 后果 |
|---|---|---|
| Go 原生 tagged struct | Go 堆 | 上手快;但**日后上编译层等于重写整个对象层** |
| **NaN-boxed u64 + 自管 arena**(**选定**) | 自管线性内存(`[]uint64` / `[]byte`) | 解释器与未来编译码**读写同一块内存**,编译层是**纯增量** |

**选定理由**:NaN-boxed u64 + 自管 arena 让解释器和未来编译层**共见同一块线性内存**,使上编译层成为**纯增量而非重写**。这是分层架构能逐阶段独立交付的物理基础。

## 自管 arena / 线性内存

- 值世界放在**自管 arena / linear memory**(`[]uint64` / `[]byte`),不住 Go 堆。
- **硬约束动因**(§2):Go runtime 的写屏障税要求——裸指针写会破坏并发 GC 的三色不变式,所以值世界必须放自管 arena,边界处做拷贝(四项税完整表见 [[design-premises]])。
- 这同一块内存正是 [[evolution-roadmap]] 中 P3 的 Wasm linear memory(「P1 的 arena 直接映射,两层共见」),也是 [[embedding-contract]] 中宿主直接写、VM 零拷贝读的 arena。

## 选定方案的代价:自写 mark-sweep GC

NaN-boxing 不让值住 Go 堆,Go GC 就不再替我们管这块内存,因此**必须自写 mark-sweep GC**。其纪律约束(与 §2 四项税中的「GC 精确栈扫描」「异步抢占」相扣):

- **safepoint 限定在分配点与层边界**——只在这些受控位置允许 GC 介入;
- **根放 shadow stack**——GC 根集合维护在自管 shadow stack 上,而非依赖 Go 的精确栈扫描。

## 部分补偿

- **NaN-boxing 数字零分配**,本身就显著快于 gopher-lua 的 interface 装箱——这部分性能是「自付代价」之外白赚的。
- **P1 实测确认**:M14 时点 table 还走 Go map 旁路,仅靠去装箱三档基准已全数过 ≥2x 门槛(simple 2.28x / arith 2.40x / loop 2.30x);收尾轮完成 arena 原生表存储 + IC 后,simple/arith 升至 3.1-3.2x 而 loop 持平(FORLOOP 回边不走 IC)——两段数据共同印证「去装箱是主力、IC 是表访问档的辅力」的结构(05 §3)。P1 性能轮后现为 simple 9.0x / arith 7.0x / loop 2.45x,增量主因是固定开销消除(thread 复用、closeUpvals 快路径),不改变上述结构结论。

## 这块内存为什么使编译层成增量

一条主线贯穿全程:**同一块自管线性内存**被值表示、各执行层、宿主 ABI 共用——

```
NaN-boxed arena(§3 值表示)
   → P1 值世界(§4 解释器)
   → P3 linear memory(§4 Wasm 编译层,两层共见)
   → 嵌入契约 arena ABI(§8 宿主零拷贝读)
```

因为解释器与编译层读写同一份表示,上一个新执行层时**无需重新设计对象模型**,只需新增「发射后端」(见 [[evolution-roadmap]] P4「只换发射后端」)。这正是「编译层是纯增量」的含义。

---

相关:[[design-premises]] · [[evolution-roadmap]] · [[embedding-contract]] · [[glossary]]
