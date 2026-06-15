# PW10 Phase 0 SPIKE 决策报告 — gibbous→gibbous 跨层调用税收口可行性

> **本报告是 PW10 大修是否启动的依据,永久存档不删**(承 PW0 §0.1 spike 决策报告先例)。
> spike 代码:本目录(`spike/p3indirect/`,独立 go module,不污染主库零依赖)。

## 0. 背景与闸门

P3 PW9 实测:gibbous→gibbous 调用经 `h_call` 双跨层(Wasm→Go→Wasm,~143ns/次 × 2)
使调用密集核退化(call 核比 crescent 慢 7x)。真解是「单 module + 内部函数表 +
`call_indirect` 直调」,但被两条码库 physics 卡死(每 Proto 独立 module + Lua 帧住 Go),
且有一个**生死未知数**:wazero 能否支持增量升层的 module 生命周期。

本 spike 验两个生死假设,任一不过则大修方案需调整或回退:

- **S-A**(值不值得做):单 module 内 `call_indirect` 单次成本必须 ≪143ns(host 往返),目标 <30ns。
- **S-B**(能不能做):增量升层 = 重编整个 module + 新实例热交换,此生命周期必须可行且安全。

**测量环境**:Intel Xeon 6982P-C / go1.26.2 / wazero v1.12.0 / 编译模式
(`NewRuntimeConfigCompiler`)/ `-benchtime=2s`(S-A)`-benchtime=1s`(S-B)。

## 1. S-A 实测:dispatch 单次成本(÷10000 摊销)

三形态共享同一 leaf 体(`leaf(x)=x*3+1`)+ 同一循环骨架,**只差调用机制**,
driver 紧循环调 leaf 10000 次,ns/op ÷ 10000 = 单次 dispatch 摊销成本。

| 形态 | ns/op(driver 10000 次) | **单次 dispatch** | 闸门 | 判定 |
|---|---|---|---|---|
| **indirect**(`call_indirect`,本方案) | ~24186–26140 | **~2.5 ns** | <30ns | ✅ **远超** |
| direct(`call` 直调,地板) | ~10312–10512 | ~1.0 ns | — | 地板参考 |
| host(`call` imported Go,跨层) | ~352137–353749 | **~35 ns** | 对比基线 | — |

**结论**:`call_indirect` 单次 ~2.5ns,比单次 raw host 跨层(~35ns)**快 14x**;而真实
gibbous→gibbous 是**双** host 跨层(PW9 ~390ns 量级),`call_indirect` 直调省掉**两次**
跨层 + 中间 Go 帧管理。dispatch 机制切换的收益无可争议。

> 注:本 spike 的 host 地板(~35ns)低于 PW9 报告的 ~143ns——因 spike host fn 是平凡函数
> 体 + `WithGoFunction` 快注册路径,PW9 的 `h_call` 还含 doCall 解码 / CallInfo 压栈 / base
> 刷新等真实帧管理。两者各自诚实;S-A 的 apples-to-apples 对比是「indirect vs host raw
> dispatch」,2.5ns vs 35ns(14x),已足够定论。真实收益只会比这更大(双跨层 + 帧管理全省)。

## 2. S-B 实测:增量升层 module 生命周期

### 2.1 正确性/安全性(三个生命周期险点,全过)

| 测试 | 验证内容 | 结果 |
|---|---|---|
| **SB1** 跨实例共享 memory | 多 module 实例 import 同一 `env.memory`,一个写、另一个读到;互不 clobber | ✅ PASS |
| **SB2** 增量重编 + 双实例共存 | 编 module{1 leaf} 实例化 → 再编 module{4 leaf} 实例化为**新实例**,**两实例同时可调用**(旧实例不必 Close) | ✅ PASS |
| **SB3** re-entrant 升层 | 旧 driver 执行**中途**(Go 栈上有它的帧)经 imported 钩回 Go,编译+实例化+调用一个**新** module,再返回旧 driver 续跑 | ✅ PASS(旧帧续跑不崩,新实例正常执行) |

**关键发现**:wazero 确实**不支持向已实例化 module 追加函数**(core Wasm 无此机制),故
增量升层只能「重编含新函数的整个 module + 实例化为新实例」。但这条路**可行且安全**——
所有 gibbous module 实例 import 同一块 `env.memory`(arena 收养的 linear memory),内存是
它们的公共底座,多份实例可共存;旧实例上有 in-flight 帧时**不 Close 即可**(SB2/SB3 坐实)。
生死未知数解除。

### 2.2 成本(重编 = 每次升层一次性事件,非热路径)

| 操作 | 成本 | 评估 |
|---|---|---|
| CompileModule(1 leaf) | ~0.11 ms | |
| CompileModule(16 leaf) | ~0.18 ms | 典型 Program |
| CompileModule(64 leaf) | ~0.39 ms | |
| CompileModule(256 leaf) | ~1.23 ms | 大 Program |
| InstantiateModule(16 leaf) | ~12 µs | |

**结论**:即便 256 函数的大 Program,整体重编 ~1.2ms;典型规模(16 函数)~0.18ms。升层
不在热路径(P2 只在热度越阈值时一次性触发,04 §3),sub-ms~低 ms 完全可接受。

**增量升层的成本模型**:每次新 Proto 升层 → 重编整个 module(O(已升函数数))。N 个 Proto
依次升层 = O(N²) 累计编译工作。缓解(留 PW10 实现期评估):① 批量升层(攒一批再编)②
仅在「函数数翻倍」时重编(均摊 O(N))③ 升层稳定后不再重编(热集有限)。这是**实现期优化**,
不影响 Phase 0 可行性结论。

## 3. 决策:🟢 GREEN — PW10 大修放行

- **S-A 闸门 ✅**:`call_indirect` ~2.5ns ≪ 30ns 目标,比 host 跨层快 14x(真实双跨层省更多)。
- **S-B 闸门 ✅**:增量重编 + 新实例热交换可行且安全(SB1/SB2/SB3 全过);重编成本 sub-ms~低 ms,
  升层一次性事件可接受;生死未知数(wazero 增量 module)解除——「重编整个 module」是确定可行的路径。

**放行 PW10 Phase 1+ 重写**(R1 单 module 编译器 + R2 轻量 CallInfo 进 linear memory + R3/R4
Wasm 侧 CALL/RETURN 直调 + R5 re-benchmark)。Phase 1+ 分片细节据本 spike 数据在实现期重新规划。

**仍需在 Phase 1+ 解决的工程问题**(spike 已证可行,非阻塞):
1. **CallInfo 进 linear memory**(R2 = VS0-e 子集):S-B 只测了「函数在同 module 可 call_indirect」,
   但帧管理(压 CallInfo / nresults 多值调整)仍需让 Wasm 侧可读写帧元数据——这是 R2 的核心,
   牵连 growStack 段重定位(design-claims-vs-codebase-physics §2)+ GC 根可达性(§4)+ 协程栈。
2. **Proto→table index 映射 + 重编时机**:R1 需把 one-module-per-Proto 改累积单 module,定重编策略
   (§2.2 成本模型的 O(N²) 缓解)。
3. **三向分派保留**:gibbous→gibbous 走 `call_indirect`,但 gibbous→{crescent,host} 仍回 `h_call`
   (R3)——`call_indirect` 只覆盖「被调者也已升 gibbous 且同 module」。

## 4. 附:回退路径(本 spike 绿,不启用)

若 S-A 或 S-B 红,退路是「升层启发式拒小叶函数」:只在回边热(循环体)升层,纯调用热的
小叶函数留 crescent(零跨层,1.0x),消除退化但不拿直调收益。本 spike 双绿,**采用大修不退路**。
