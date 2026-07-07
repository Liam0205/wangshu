# PW10「零跨界」里程碑 Stage 0 SPIKE 决策报告 — gibbous→gibbous 帧建拆入 Wasm 可行性

> **本报告是 PW10 零跨界大修是否启动的依据,永久存档不删**(承 PW0 §0.1 / PW10-Phase0
> `spike/p3indirect/DECISION.md` 先例)。spike 代码:本目录(`spike/p3frame/`,独立 go
> module,不污染主库零依赖,手写 wasm binary)。

## 0. 背景与检查

PW10 R3/R3.5 后四核:loop 2.65x / table 1.26x / mixed 1.14x(均 ≥1x ✅),**唯 call
0.49x**(慢 2x)。profile(`/tmp/cnested.prof`,真 gibbous→gibbous 路径)定位:每次
gibbous→gibbous 调用仍做**两次 host 跨界**——`h_call`(`DoCall`)建帧 + `h_return`
(`DoReturn`)拆帧。逐行确认拆帧 71% 成本在不可搬的 `popCallInfo`,只搬 moveResults
救不了(~0.5%)。降 call 核的唯一杠杆 = **把帧建拆搬进 Wasm,host 跨界降到 0**。

但这要求把帧管理游标(ciDepth、th.cur 顶帧热态)迁进 linear memory,是 R2 同级的物理
迁移 + 高 UAF/GC 风险的里程碑级改动。**不盲启**——先 spike 验生死假设:

- **生死未知数**:Wasm 侧帧建拆(段字写 + `ciDepth` 字增减 + `maxOpenIdx` 守卫)是否
  **真比**当前单 `h_call` + 单 `h_return` 两次 host 跨界**快**(扣除 Wasm 内记账开销)?
  若 Wasm 内记账 ≥ 跨界节省,大修无意义 → 收口为已知限制。

**测量环境**:Intel Xeon 6982P-C / go1.26.2 / wazero v1.12.0 / 编译模式
(`NewRuntimeConfigCompiler`)/ `-benchtime=2s -count=3`。

## 1. S-F1 实测:帧建拆 in-Wasm vs 2 跨界(÷100000 摊销)

两 driver 形式共享同一 leaf 体(`leaf(x)=x*3+1`)+ 同一紧循环骨架(调 leaf 100000 次),
**只差「每次调用如何建拆帧」**;两形式的帧工作量刻意等价(都写 4 段字 @ `segBase+depth*32`
+ `ciDepth` 增减 + `maxOpenIdx` 守卫读),差异**只在这工作在 Wasm 内做还是经 host 跨界回
Go 做**——隔离「两次跨界本身」的净成本。host 桩用 `WithGoModuleFunction`(零分配,对齐 R3.5
生产)。

| 形式 | ns/op(driver 100000 次) | **单次 dispatch+建拆帧** | allocs/op | 判定 |
|---|---|---|---|---|
| **inwasm**(建拆帧全 Wasm 内,**本里程碑形式**) | ~852k–858k | **~8.5 ns** | **0** | ✅ |
| twocross(建拆帧经 h_call + h_return 两次 host 跨界) | ~8.99M–9.07M | **~90 ns** | **0** | 当前 R3.5 后形式 |

**inwasm 比 twocross 快 ~10.5x(8.5ns vs 90ns)**;差值 **~81ns/call** 正是「两次 host
跨界做等价工作」相对「Wasm 内做同样工作」的净成本。两形式 allocs/op 均 0(twocross 用零
分配 host API,公平)。

## 2. 检查裁决:🟢 GREEN — 零跨界里程碑放行

- **S-F1 检查 ✅**:in-Wasm 帧建拆 ~8.5ns ≪ 两跨界 ~90ns,**快 10.5x**,allocs 不回退(均 0)。
- **标尺核对(足以拉 call 核 ≥1x?)**:call 核 ~300k leaf 调用/轮(gibbous 38ms vs crescent
  19ms = 0.49x)。每调省 ~81ns × 300k ≈ **24ms** ⟹ gibbous 38ms → ~14ms,**反超 crescent
  19ms ⟹ ≥1x**。收益足以达成里程碑目标。

**放行 Stage 1+ 重写(段为权威 + Wasm 侧帧建拆)**:
- **Stage 1** 帧游标迁 linear memory:`ciDepth` 字只写影子(1a)→ `th.cur` 降缓存 + 跨界
  resync(1b,段为权威的语义翻转)→ GC 根扫读 `ciDepth` 字(1c,最高 UAF 风险,隔离提交)。
- **Stage 2(R4/A)** Wasm 侧快路径 RETURN 免 h_return(读段字 + moveResults + 减 ciDepth +
  maxOpenIdx 守卫;有 upvalue/vararg/多值回退 h_return)。
- **Stage 3(B)** Wasm 侧快路径 CALL 建帧免 h_call(写段字 + 增 ciDepth + nil-fill;
  vararg/grow/非 gibbous-slot 回退 h_call)。gibbous→gibbous 降到 0 host 跨界。
- **Stage 4(R5)** re-bench:call ≥1x、loop/table/mixed 无回归、geomean 改善。

## 3. spike 未覆盖、留 Stage 1+ 解决的工程难点(机制可行,非阻塞)

1. **`th.cur` 归宿(中心岔路)**:spike 直接对段字读写、无 `th.cur` 镜像;生产 `currentCI`
   返回 `&th.cur`(顶帧热态,可能比段新)。Stage 1b 须实现「段为权威 + Go 跨界边界惰性
   resync th.cur」(Option A),并审计所有跨 CALL 的 `currentCI` 持有者陈旧指针雷区。
2. **GC 根扫深度可见性**:spike 无 GC;生产 Wasm 增的 `ciDepth` 须对 Go GC 根扫可见
   (Stage 1c,陈旧深度=漏 closure 根=UAF),独立提交 + `-race` + GC-stress 守关。
3. **回退条件**:spike 帧建拆是稳态快路径(定 arity、无 grow、无 upvalue);生产须守卫
   这些条件,miss 回退 h_call/h_return(arity/vararg/NeedsArg/growStack/open-upvalue)。
4. **byte-equal 契约**:spike 只验计算自洽 + ciDepth 配平;生产每 Stage 须 V1-V13 层间
   byte-equal + 错误路径 byte-equal + 四 build + `-race`,守不住即收口为已知限制。

## 3.5 S-F3 实测:带完整运行期守卫的内联帧建拆——守卫不吞收益(Stage 2 设计期补测)

Stage 2(RETURN 快路径)设计时,生产快路径须读 **3 个镜像字**(ciDepth/段基址/maxOpenIdx)+
段帧做运行期守卫(段基址现读因段可重定位、maxOpenIdx 守卫分支判「无开放 upvalue」、
caller gibbous 位检查),比 spike 的 minimal inwasm 形式重。**承反思教训「先量再做」**,加
`driver_guarded` 档量「带完整守卫的内联帧建拆」是否仍显著快过 2 跨界:

| 形式 | 单次 dispatch+建拆帧 | vs twocross | 判定 |
|---|---|---|---|
| inwasm(minimal) | ~8.5 ns | 10.6x 快 | — |
| **guarded(段基址现读 + maxOpenIdx 守卫分支 + caller gibbous 位检查)** | **~9.4 ns** | **9.6x 快** | ✅ 守卫仅 +0.9ns |
| twocross(2 host 跨界) | ~90 ns | 基线 | — |

**结论**:完整运行期守卫只加 **~0.9ns**(8.5→9.4),guarded 仍比 2 跨界快 **9.6x**——
**守卫开销不吞收益**。Stage 2+3 可放心带全守卫实现(读 3 字 + 守卫分支),call 核 ≥1x
预期不变。这也定调 **Stage 2/3 合并实现**(共享段字机器 + 守卫,一次 re-bench 验收)。

## 4. 附:S-F2(Option A resync 税)备注

Option A(段为权威 + 跨界边界 resync `th.cur`)每次 Go 跨界(回退路径 / GC / traceback)
多一次 `readCISegInto`(4 word 读)。但**热路径(gibbous→gibbous 快路径)零 Go 跨界**,
resync 只发生在已经跨界的慢路径上,其成本相对跨界本身(~90ns)可忽略。spike 的 inwasm
形式已是「零 resync 的热路径」实测(8.5ns),Option A 的 resync 税只落在非热路径,不进
S-F1 的关键比较。生产 Stage 1b 完成后 Stage 4 re-bench 复核。
