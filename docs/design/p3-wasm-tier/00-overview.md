# P3 总览:gibbous/wasm Wasm 编译层——文档地图 / 实现里程碑 / 验收 / 人月分解

> 状态:**设计阶段,详细设计已齐备**(依赖 P1/P2 完成与开工前置 spike 通过后细化;凡涉 wazero API 细节处标注「待 spike 验证」)。本文是 P3 文档集(00-08)的导航与施工计划:每篇文档的定位、组件依赖、构建顺序、里程碑验收门槛、人月分解、跨文档定稿决策速查、与 P1/P2 的桥接(已完成的前瞻义务对账)。
> 上游:`docs/design/roadmap.md` (§4 P3 定义、§2 四项税、§5 五条原则)、[architecture](../architecture.md)(§3 依赖图、`internal/gibbous/wasm` 包位置)、[evolution-roadmap](../../../llmdoc/architecture/evolution-roadmap.md)(tier-1 = gibbous,P3+P4 同 tier;P3 验收坐标系警告)。
> P1 依赖面:[01](../p1-interpreter/01-value-object-model.md)(NaN-box u64 + GCRef offset,两层逐位同一)、[02](../p1-interpreter/02-bytecode-isa.md)(源 ISA + IC slot)、[05](../p1-interpreter/05-interpreter-loop.md)(解释器主循环 + CallInfo + 调用约定)、[06](../p1-interpreter/06-memory-gc.md)(arena/GC/safepoint)、[08](../p1-interpreter/08-coroutines.md)(coroutine 路线 B yield 冒泡)、[12](../p1-interpreter/12-testing-difftest.md)(差分测试矩阵)。
> P2 依赖面:[../p2-bridge/00-overview](../p2-bridge/00-overview.md)(P3 是 P2 决策的消费者)、[../p2-bridge/03](../p2-bridge/03-compilability-analysis.md) §3.7(F7 后端能力 SupportsAllOpcodes)、[../p2-bridge/04](../p2-bridge/04-try-compile-fallback.md)(零 deopt 单向状态机)、[../p2-bridge/05](../p2-bridge/05-p3-p4-interface.md)(P3Compiler 接口 + GibbousCode 抽象)。
> 下游衔接:[../p4-method-jit](../p4-method-jit/00-overview.md)(继承 P3 的全部分层结构,只换发射后端;§6 P3 去留决策矩阵)。
>
> P3 目标一句话:**在「不用调试机器码」的后端上,把 P2 决策机产出的 gibbous 代码请求兑现成 Wasm 可执行码,跑通整套分层骨架(升层 / fallback / trampoline / 跨层差分)**——战略价值不在倍率本身,而在「分层机器第一次全链路运转」(`docs/design/roadmap.md` §4)。P3 把四项税(roadmap §2)外包给 wazero,自己专注「翻译 + 跨层协议」。

对应 Go 包:`internal/gibbous/wasm`(字节码→Wasm 编译器 + wazero 执行环境 + trampoline + 助手桥)。

---

## 0. 文档地图:谁定什么(单一事实源分工)

| 文档 | 定位 | 单一事实源(其它文档以它为准) |
|---|---|---|
| [01-spike-gate](./01-spike-gate.md) | 开工检查 | wazero call boundary spike 三档样本(S1/S2/S3)、150ns 检查论证、跨层摊销模型、三种出路(开工 P3 / 跳跃 P4 / 边缘混合策略) |
| [02-translation](./02-translation.md) | 翻译器 | 翻译单位(每 Proto 一 module 基线 + 批量 module 优化项)、寄存器映射(基线 memory-resident + locals 缓存优化)、opcode 翻译表(WAT 风格伪码)、pc 物化、`SupportsAllOpcodes` 渐进白名单 |
| [03-memory-model](./03-memory-model.md) | 共见内存 | arena 收养 wazero memory(对 06 的回填)、值编码两层逐位同一、GCRef offset 与 wasm32 寻址匹配、`memory.grow` 后视图重取协议 |
| [04-trampoline](./04-trampoline.md) | 跨层互调 | CallInfo bit50 `callStatus_gibbous`(对 05 的回填)、crescent→gibbous 入口协议、gibbous→crescent/host imported 助手分派、status 链错误冒泡、参数/返回值经共见值栈 |
| [05-safepoint-gc](./05-safepoint-gc.md) | 跨层 GC | 三类 safepoint 在 P3 的形式(分配点 / 层边界 / 回边)、收口 [06 §12](../p1-interpreter/06-memory-gc.md) 缺口、locals 缓存写回纪律、写屏障 P3 不动 |
| [06-ic-feedback-consume](./06-ic-feedback-consume.md) | feedback 非投机消费 | IC 快照编译期固化、失效自然降级、快路径 = 语义分发非投机 guard、与 P2 零 deopt 口径一致、GETTABLE/CALL 的 feedback-aware 翻译形式 |
| [07-coroutine-thread-rule](./07-coroutine-thread-rule.md) | 线程级 tier | yield 不能穿越 gibbous 帧的物理论证、线程级 tier 规则(主线程才升层)、协程线程一律走 crescent、对 [08](../p1-interpreter/08-coroutines.md) 与 P2 的回填请求 |
| [08-testing-strategy](./08-testing-strategy.md) | 验收 | P3 验收口径总表、crescent vs gibbous 逐字节差分(CI 门禁)、强制全升模式、GC 压力 fuzz 上 gibbous、P3 性能门(循环密集 ≥2x over P1)、坐标系警告 |
| [implementation-progress](./implementation-progress.md) | 进度 | 开工前置检查检查、P-Wasm 里程碑(预设占位)、设计期决策盘点(影响 × 不确定度三档)、跨文档回填请求收口表 |

阅读顺序建议:实现者先读 00→01(检查通过才动手)→03(内存模型,翻译器骨架的物理基础)→02(翻译器主体)→04(跨层协议,与 02 同期完成)→05(safepoint,跨层 GC)→06(feedback 消费,02 翻译细化)→07(coroutine 线程规则,边界场景)→08(验收口径,每步收口查)。

---

## 1. P3 与 P1/P2/P4 的边界(谁拥有什么)

| 关注点 | P1(crescent) | P2(bridge) | **P3(gibbous/wasm)拥有** | P4(gibbous/jit) |
|---|---|---|---|---|
| 编译哪些 Proto(热度 + 可编译性) | — | ✅ 决策 | 只接收,不判定 | 同 P3 |
| 类型 feedback 产出 | IC 写入 | ✅ 聚合 | **可选**消费(代码形状 hint,非语义依赖) | **必需**消费(投机依据) |
| 字节码→Wasm 翻译 | — | — | ✅ [02-translation](./02-translation.md) | — |
| 字节码→原生码翻译 | — | — | — | P4 自己 |
| **linear memory 共见** | arena Go 堆 backing | — | ✅ 收养 wazero memory([03](./03-memory-model.md)) | 复用(原生码读同一份) |
| **trampoline / 互调协议** | enterLuaFrame | — | ✅ [04-trampoline](./04-trampoline.md)(crescent↔gibbous↔host) | 继承一样的协议,换发射后端 |
| **跨层 safepoint** | 解释器主循环 | — | ✅ [05-safepoint-gc](./05-safepoint-gc.md)(收口 [06 §12](../p1-interpreter/06-memory-gc.md)) | 同 P3(原生码自身回边检查点 +写屏障) |
| **CallInfo bit50 写入** | 不读不写 | 不写 | ✅ trampoline 在 gibbous 帧入口写 1([04 §1](./04-trampoline.md)) | 同 P3 |
| **零 deopt(fallback 而非投机)** | 永久解释 | ✅ 单向状态机 | 严格遵守:快路径 = 语义分发,失败走助手非 deopt | ❌ 引入 deopt(投机失败 OSR exit 回 crescent) |
| **GibbousCode 实现方** | — | 接口定义 | ✅ wazero `api.Function` 包装 | 原生码段包装(同接口) |
| **解释执行(fallback 着陆点)** | ✅ 永不退役 | — | — | — |
| **协程升层** | ✅ 跑 crescent | 升层判定加线程上下文 | ❌ **协程线程一律走 crescent**([07](./07-coroutine-thread-rule.md)) | 同 P3(继承线程级 tier 规则) |

**一句话**:**P2 决策「编谁」+ 产「feedback 料」,P3 是 P2 决策的兑现机器(把 Proto 翻译成 Wasm 跑起来),不做任何运行期投机** —— 投机面 + deopt 全留 P4。

> **tier 坐标系警告**([evolution-roadmap](../../../llmdoc/architecture/evolution-roadmap.md)):月相 tier 比阶段粗一层。P1=tier-0(crescent),P3/P4=tier-1(gibbous),P5=tier-2(fullmoon)。**P3 与 P4 同属 tier-1 但发射后端不同**:P3 发 Wasm(wazero 执行)、P4 发原生码(自管 codegen)。代码包名据此:`internal/gibbous/wasm`(P3)、`internal/gibbous/jit`(P4)。日志统一是 `function promoted to gibbous`(不区分子档)。

> **P3 与 P4 的两个跨阶段决策视角**(2026-06-28,承 [P4 implementation-progress §2 RJ-17](../p4-method-jit/implementation-progress.md) 跨文档回填请求):上表只列 P3 实施层(同 tier 不同后端的边界划分),但 P4 视角下 P3 与 P4 还有两个跨阶段决策视角:
> - **P4 立项检查**([P4 01-launch-judgment](../p4-method-jit/01-launch-judgment.md)):决定 P4 是否启动 + 何时启动,输入含 P3 实际表现 + 真实宿主负载证据
> - **P4 验收时 P3 去留**([P4 07-p3-retirement](../p4-method-jit/07-p3-retirement.md)):P4 验收通过后 P3 退役 / 留中层二选一,输入含 P3 在 P4 不可用平台上的实际表现
>
> 两个决策视角是 P3/P4 共生关系的两端:立项决定 P4 是否要做(P3 后);去留决定 P4 上线时 P3 怎么办(P4 后)。本文 §1 上表只列同 tier 不同后端的实施层边界;立项 + 去留两个决策视角的详细框架见上述两个 P4 子文档。

---

## 2. 总数据流(承 P2 决策 + 产 GibbousCode 给 crescent 调)

```
                P2 决策机(已完成)
                 │
                 ▼
   considerPromotion(proto, pd) 走 try-compile 路径
                 │
                 ▼
   P3Compiler.Compile(proto, fb) ── 入参签名见 [P2 05 §2.1]
                 │
        ┌────────┴────────┐
        │                 │
   ① 翻译期            ② 失败路径
        │                 │
        ▼                 ▼
  字节码→Wasm        TierStuck(永久解释)
  ([02] §2-§7)        ← P3 不区分错因
        │                 ↑
        ▼                 │
  wazero.Compile          │
  + Memory 收养           │ panic 经 P2 [04 §5.2] defer recover 转
  ([03] §1-§3)            │ *CompileError(Kind=BackendPanic)
        │                 │
        ▼                 │
  GibbousCode 产物 ────────┘
                 │
                 ▼
        installGibbous([P2 04 §4.4])
                 │
                 ▼
        TierGibbous(吸收态)
                 │
                 ▼
   crescent.doCall 检测 tierState ⇒
       trampoline 跳 wazero.Function.Call(ctx, base)
            ([04] §2 crescent → gibbous)
                 │
                 ▼
        Wasm 函数运行(共见 arena memory,[03])
        ├── 直线代码:MOVE / 算术 / FORLOOP 等([02] §3)
        ├── 慢路径:imported 助手回 Go([04] §3)
        ├── safepoint:回边 gcPending 检查([05] §3)
        └── RETURN:结果回填寄存器(共见栈槽),return status
                 │
                 ▼
        trampoline 弹 CallInfo,继续 crescent 解释
```

**核心物理事实**:① P3 不发明新值表示——`NaN-box u64` 在 Wasm 侧逐位同一([01](../p1-interpreter/01-value-object-model.md));② linear memory **就是** arena backing(grow 后偏移寻址不变);③ 所有跨层只传 `base i32`,其余从共见栈槽自取(参数、返回值、IC slot)。

---

## 3. 组件依赖与关键耦合点

依赖图见 [architecture](../architecture.md) §3:`internal/gibbous/wasm` 依赖
- `bytecode`(读 Proto + IC slot,翻译输入);
- `value`(NaN-box / GCRef 工具函数 — 与解释器共用);
- `arena`(收养 wazero memory 的 backing;[03 §1](./03-memory-model.md));
- `bridge`(实现 P3Compiler + 消费 TypeFeedback;[../p2-bridge/05](../p2-bridge/05-p3-p4-interface.md));
- `crescent`(被调:gibbous→crescent 走 trampoline 助手 [04 §3](./04-trampoline.md);**反向依赖** — P3 必须能把控制权交回解释器跑未编译 Proto)。

设计定稿后新增的跨组件**关键耦合点**(实现时最易出错处):

1. **arena backing 收养 wazero memory**([03 §1.2](./03-memory-model.md) + [对 06 的回填](../p1-interpreter/06-memory-gc.md) §1.1):P1 的 backing 是 Go 堆 `[]uint64`,P3 起改为「以 wazero memory 的底层 buffer 为来源」。NewState 时即经 wazero 分配 memory(P3 build 下),arena 的 `words/bytes` 视图从该 buffer 派生;`memory.grow` 后 Go 侧视图 slice 重取(偏移寻址使所有 GCRef/链表/bump 一字不改)。**P1 已留 `arena.Options.NewBacking` 注入点**,P3 替换为 wazero memory adapter,P1 实代码侧零改动。

2. **CallInfo word2 bit50 `callStatus_gibbous`**([04 §1](./04-trampoline.md) + [对 05 的回填](../p1-interpreter/05-interpreter-loop.md) §1.2):gibbous 帧入口 trampoline 把 bit50 置 1,标识「此帧走 Wasm 路径」。P1 当前是普通 Go struct,P3 完成时引入位打包(或保留 bool 字段简化版,P3 完成时定);**对 P1 05 的回填请求 = bit50 字段语义登记**。P1 本期已加占位(commit ecffcb9 删除后等 P3 实现时再加,留语义于 installGibbous 注释)。

3. **`SupportsAllOpcodes` 渐进白名单**([02 §1.3](./02-translation.md) + [../p2-bridge/03 §3.7](../p2-bridge/03-compilability-analysis.md)):P3 后端开发期渐进扩充,初期只支持 ISA 0..37 的子集(LOADK / GETTABLE / CALL / RETURN / FORLOOP / JMP 等热路径 opcode)。任何含未支持 opcode 的 Proto 经 [F7 检查](../p2-bridge/03-compilability-analysis.md) 拒。**保守缺省**:supported 表只把明确实现的 opcode 加入,其他全部返 false。

4. **线程级 tier 规则**([07](./07-coroutine-thread-rule.md) + [对 08 / P2 的回填](../p1-interpreter/08-coroutines.md)):只有主线程的执行进入 gibbous;协程线程一律走 crescent。doCall 的 gibbous 分支多查一个 `th == mainThread`。**对 P2 [04](../p2-bridge/04-try-compile-fallback.md) 的回填请求**:升层判定加线程上下文输入(协程内即便函数 CompCompilable + 热,也判 TierStuck 或不 considerPromotion)。**对 [08](../p1-interpreter/08-coroutines.md) 的回填请求**:gibbous 帧不可穿越 yield 节(已在 P1 08 §6 注脚预留前瞻引用)。

5. **IC 快照编译期固化**([06 §1](./06-ic-feedback-consume.md) + [02 §3.4 GETTABLE](./02-translation.md)):解释器 IC slot 是运行期可变的(mono IC 重填,[05 §6.3](../p1-interpreter/05-interpreter-loop.md));gibbous 把编译时刻的 `(tableRef, gen, kind, index)` 烧进代码。表形状变化(gen bump)→ 快照永久 miss → 该点每次走助手 ≈ 解释器无 IC 的水平,**正确但慢**。失效计数 → 重编译留 P4 一并评估([../p4-method-jit/04-osr-deopt](../p4-method-jit/04-osr-deopt.md) §5)。

6. **pc 物化:traceback 逐字节一致**([02 §4](./02-translation.md)):直线代码无运行期 pc。每个可能出错/调用/safepoint 的点把编译期已知 pc 作为立即数传给助手,助手写回 `CallInfo.savedPC`。差分口径([12](../p1-interpreter/12-testing-difftest.md))不为 gibbous 开任何豁免。

---

## 4. 实现里程碑(细化 architecture §5 的施工顺序)

每步可独立编译 + 单测通过再进下一步。「验收」列是该步的完成定义;PW 编号(P-Wasm)供排期引用。**P3 的核心验收门是性能(循环密集 ≥2x over P1)**,与 P2(决策正确)不同。

| PW | 内容 | 对应文档 | 验收(完成定义) |
|---|---|---|---|
| **PW0** | 开工前置 spike(wazero call boundary 实测 S1/S2/S3) | [01](./01-spike-gate.md) | S2 < 150ns 且 S3 同档 ⇒ 开工;否则跳 P4 或走边缘混合策略 |
| PW1 | `internal/gibbous/wasm` 包骨架 + arena 收养 wazero memory + 零 opcode 翻译器(`SupportsAllOpcodes` 永远返 false) | [02](./02-translation.md) §1 + [03](./03-memory-model.md) §1 | bridge 注入 P3Compiler 后所有 Proto 仍走 crescent(F7 拦下);arena/wazero memory 共见验证(grow 不破 GCRef) |
| PW2 | 翻译器骨架 + 5 条直线 opcode(MOVE/LOADK/LOADBOOL/LOADNIL/JMP)+ trampoline 入口 | [02](./02-translation.md) §2-§3 + [04](./04-trampoline.md) §2 | 一个 5-op Proto 升层后 byte-equal 解释结果;升层日志 `function promoted to gibbous` 触发 |
| PW3 | 算术 opcode(ADD/SUB/MUL/DIV/MOD/POW/UNM)+ 比较(EQ/LT/LE)+ NaN 规范化 + 慢路径助手回 Go | [02 §3.2](./02-translation.md) + [04 §3](./04-trampoline.md) | 双 number 算术快路径直发 f64 指令;混合类型走助手且结果与解释器逐字节一致 |
| PW4 | 控制流(FORPREP/FORLOOP/TFORLOOP)+ 回边 safepoint(gcPending 检查) | [02 §3.3](./02-translation.md) + [05 §3](./05-safepoint-gc.md) | 数值 for 循环编译后跑 ≥2x 解释器;回边 GC 触发 byte-equal |
| PW5 | 表 IC opcode(GETTABLE/SETTABLE/GETGLOBAL/SETGLOBAL/SELF)+ feedback 消费(IC 快照固化)+ 失效降级走助手 | [02 §3.4](./02-translation.md) + [06](./06-ic-feedback-consume.md) | 单态表访问编译后跳过哈希查找;形状变化(gen bump)后该点走助手仍正确 |
| PW6 | CALL/TAILCALL/RETURN + 跨层互调协议(crescent↔gibbous↔host) + status 链错误冒泡 | [04](./04-trampoline.md) §2-§4 | gibbous 内调未编译 Proto 经 trampoline 出去由 crescent 跑;错误从 Wasm 帧穿越冒泡到 pcall 边界 |
| PW7 | CLOSURE/CLOSE/VARARG + 闭包/upvalue 编译协议 | [02 §3.5](./02-translation.md) | 闭包构造 + 开放/关闭 upvalue 与解释器 byte-equal;vararg 函数已在 P2 F1 拦下,本步只验「不可达路径不被走到」 |
| PW8 | 线程级 tier 规则 + 协程不升层 | [07](./07-coroutine-thread-rule.md) + 对 P2 04 的接线 | 协程内即便 hot + Compilable 也保持 TierInterp;主线程同 Proto 正常升层 |
| PW9 | 端到端验收 + 测试套(crescent vs gibbous 逐字节差分 + 强制全升模式 + GC 压力 fuzz + 性能基准 ≥2x) | [08](./08-testing-strategy.md) | **P3 总验收**:V1-V18 全过(详见 [08 §1](./08-testing-strategy.md)) |

> **PW0 启动条件**:P1 全卷已交付(M0-M14)+ P2 PB0-PB7 全过线 + 后续优化轮 #1-#4 全过线(2026-06-13 已达成);P3 PW0 spike 阻塞所有后续,先于一切翻译工作。
>
> **PW1 阻塞验证**:bridge 当前 mock P3 装载形式(`internal/bridge/mock`)在 PW1 用真 P3 占位(SupportsAllOpcodes 全 false)替换,验证「无任何 Proto 升层」与 P1-only 等价。
>
> **PW9 verify 含两轴**:正确性(byte-equal)与性能(≥2x);任一不达标都不算 P3 交付完成。

---

## 5. 人月分解(roadmap §4:6-12 人月,执行层定位)

按单人全职折算;区间下沿=顺利,上沿=含 spike 反复 + 跨层协议返工 + locals 缓存优化 + 性能调优:

| 里程碑段 | 内容 | 估算 |
|---|---|---|
| PW0 | spike(三档 + memory 共享 + 四项税顺带验证) | 0.5 - 1 人月(若不达标直跳 P4,该投入仍值得 — 决策依据落实) |
| PW1 | 包骨架 + arena 收养 + 零 opcode | 0.5 - 1 人月 |
| PW2 | 翻译器骨架 + 5 直线 opcode + trampoline 入口 | 0.5 - 1 人月 |
| PW3 | 算术 + 比较 + NaN 规范化 + 慢路径助手 | 0.5 - 1 人月 |
| PW4 | 控制流 + 回边 safepoint | 0.5 人月 |
| PW5 | 表 IC opcode + feedback 消费 + 失效降级 | 1 - 2 人月(IC 快照固化 + 同表同代次校验是 P3 翻译复杂度峰值) |
| PW6 | CALL 系列 + 跨层协议 + status 链 | 1 - 2 人月(gibbous→gibbous / gibbous→crescent / gibbous→host 三路分派 + 错误冒泡 + pcall 边界清理) |
| PW7 | CLOSURE + upvalue 编译 | 0.5 - 1 人月(开放/关闭 upvalue 协议是工程难点) |
| PW8 | 线程级 tier 规则 | 0.25 人月 |
| PW9 | 测试套 + 验收 + 性能调优 | 1 - 2 人月(差分 fuzz + GC 压力 fuzz + ≥2x 性能调优可能反复) |
| 合计 | | **6.25 - 12.75 人月** |

与 [evolution-roadmap](../../../llmdoc/architecture/evolution-roadmap.md) 的 6-12 人月吻合。**PW5 与 PW6 是大头** — 不是因为 opcode 数多,而是「跨层语义保真」反复试错(IC 快照失效协议 + 跨层错误冒泡两条主线在真实负载下都会遇到边角)。**PW0 的不确定度最大**:若 spike 不达标,P3 整体跳过,PW1-PW9 全部不投入。

---

## 6. 跨文档定稿决策速查(实现前必读)

设计期在多篇文档间协商定稿的关键决策,集中列出防止实现时只读单篇而漏掉:

| 决策 | 定稿 | 出处 |
|---|---|---|
| 翻译单位 | 每 Proto 一个 module(基线);批量 module + `call_indirect` 直调留优化项 | [02 §1](./02-translation.md) |
| 寄存器映射 | 全 memory-resident(基线);locals 缓存只对循环局部热槽,有写回纪律 | [02 §2](./02-translation.md) |
| 值编码两层 | NaN-box u64 逐位同一(P1 设计第一天承诺) | [03 §2](./03-memory-model.md) |
| arena backing | P3 起收养 wazero memory;`memory.grow` 后 Go 视图重取 | [03 §1](./03-memory-model.md) |
| GCRef = wasm32 offset | 48-bit 字节偏移 ≤ 4 GiB,与 wasm32 寻址匹配 | [03 §2](./03-memory-model.md) |
| trampoline 入口签名 | `(func $proto_N (param $base i32) (result i32))`,return 0=OK / 1=ERR | [04 §2](./04-trampoline.md) |
| CallInfo bit50 写入 | trampoline 在新帧入口写 1;不改现存帧 | [04 §1](./04-trampoline.md) |
| 错误传播 | status 链冒泡(可穿 gibbous 帧);yield 不可穿(线程级 tier 规则) | [04 §4](./04-trampoline.md) + [07](./07-coroutine-thread-rule.md) |
| safepoint 三类 | 分配点(助手内)+ 层边界(trampoline)+ 回边(gcPending 检查);写屏障 P3 不动 | [05](./05-safepoint-gc.md) |
| feedback 消费 | 非投机消费 — IC 快照固化 + 失效自然降级;P3 不依赖 feedback 正确性 | [06 §1](./06-ic-feedback-consume.md) + [../p2-bridge/05 §1.2](../p2-bridge/05-p3-p4-interface.md) |
| 协程升层 | ❌ 不升层(线程级 tier 规则);P3 开工前向首个宿主确认列内核是否跑在协程里 | [07](./07-coroutine-thread-rule.md) + [memory/decisions](../../../llmdoc/memory/decisions/2026-06-11-design-review-decisions.md) 第 7 项 |
| 性能验收 | 循环密集脚本 ≥2x over P1(以 P1 为基线,不是 gopher-lua) | [08 §1](./08-testing-strategy.md) |
| 差分口径 | crescent vs gibbous 逐字节一致,gibbous 不开新豁免 | [08 §2](./08-testing-strategy.md) + [12 §10](../p1-interpreter/12-testing-difftest.md) |
| P3 与 P4 的关系 | P3 兑现分层骨架;P4 继承全部结构,只换发射后端;P3 去留留 P4 验收时定 | [../p4-method-jit/07-p3-retirement](../p4-method-jit/07-p3-retirement.md) |

---

## 7. P1/P2 已完成的前瞻义务对账(P3 启动前置)

P1 全卷已交付 + P2 PB0-PB7 全过线 + P2 后续优化轮 #1-#4 全过线(2026-06-13)。P3 依赖的「前瞻留口」状态:

| 前瞻义务 | 完成状态 | 出处 | P3 消费方式 |
|---|---|---|---|
| `arena.Options.NewBacking` 注入点 | ✅ P1 已完成 | [memory/doc-gaps](../../../llmdoc/memory/doc-gaps.md)(P3 迁移留口)、[../p1-interpreter/implementation-progress](../p1-interpreter/implementation-progress.md) | PW1 替换为 wazero memory adapter |
| Proto 旁 IC slot 数组(按 pc 索引) | ✅ P1 已完成(02 §7) | [../p1-interpreter/02-bytecode-isa](../p1-interpreter/02-bytecode-isa.md) §7 | PW5 编译期读 IC slot 取快照固化 |
| TypeFeedback shape(P3 可选消费) | ✅ P2 PB2 已完成 | [../p2-bridge/02-ic-feedback](../p2-bridge/02-ic-feedback.md) | PW5 读 PointFeedback 决定快路径形式(stable shape/index) |
| `P3Compiler` 接口形状(SupportsAllOpcodes + Compile) | ✅ P2 PB6 已定义 | [../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md) §2 | PW1 起 Compiler struct 实现 |
| `bridge.GibbousCode` 抽象类型 | ✅ P2 PB6 已定义 | [../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md) §6 | PW1 包装 wazero `api.Function` 实现 |
| TierState 单向 + 吸收态(零 deopt) | ✅ P2 PB4 已完成 | [../p2-bridge/04-try-compile-fallback](../p2-bridge/04-try-compile-fallback.md) §2 | P3 不引入降层路径 |
| `installGibbous` 安装协议 | ✅ P2 PB4 已完成 | [../p2-bridge/04-try-compile-fallback](../p2-bridge/04-try-compile-fallback.md) §4.4 | PW1 起被 P2 调用 |
| F7 渐进白名单(`SupportsAllOpcodes` 保守缺省) | ✅ P2 PB3 + PB6 已完成 | [../p2-bridge/03-compilability-analysis](../p2-bridge/03-compilability-analysis.md) §3.7 | PW1 起 supported 表初空,逐 PW 扩充 |
| **CallInfo bit50 `callStatus_gibbous`** | ⚠️ **P3 完成时同批补**(P1 PB0 占位被 lint 删,语义留 installGibbous 注释) | [../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md) §1.2 | PW6 trampoline 完成时引入 |
| **协程升层判定加线程上下文** | ⚠️ **P3 完成时回填 P2 04** | [../p2-bridge/04-try-compile-fallback](../p2-bridge/04-try-compile-fallback.md) §3 | PW8 完成时同批改 P2 04 §3.2 加 `if th != mainTh return` |
| **08 「gibbous 帧不可穿越 yield」节** | ✅ P1 08 §6 已留前瞻引用 | [../p1-interpreter/08-coroutines](../p1-interpreter/08-coroutines.md) §6 末尾 | PW8 完成时把 P1 08 §6 的前瞻引用替换为正文章节 |

**结论**:P3 启动是**条件增量** — 大部分前瞻义务已完成;但有 3 项「P3 完成时同批补」(bit50 / 协程升层判定 / 08 yield 不可穿越节)留 PW6/PW8 兑现。**对 P1/P2 现有文档的回填请求本期只记录不主动改**(承用户裁决) — 详见 [implementation-progress §回填](./implementation-progress.md)。

---

## 8. 「P3 选 wazero 的本质」是把四项税外包给已验证的实现

`docs/design/roadmap.md` (§2) 的四项税,P3 **一分自己的活都不干**:

| 税 | wazero 替我们解决的方式 | 望舒侧剩余义务 |
|---|---|---|
| GC 精确栈扫描 | Wasm 执行在 wazero 自管栈,Go GC 不扫生成码帧 | 无(值世界本就在 arena,[03](./03-memory-model.md)) |
| 异步抢占 | wazero 生成码循环回边已有抢占检查点(roadmap §2「已验证」) | 无([05 §3](./05-safepoint-gc.md) 的 gcPending 检查是我们自己 GC 的事,另算) |
| 栈移动 | Wasm 栈不在 Go 栈上,morestack 与生成码无关 | 无 |
| 写屏障 | 值世界在 linear memory,生成码无 Go 指针写 | 无(P1 已兑现,[03](./03-memory-model.md) 延续) |

**P3 选 wazero 的本质 = 把四项税外包给已验证的实现**,自己专注「翻译 + 分层协议」。P4 收回这层外包(原生发射),四项税才需要自己全额兑付([../p4-method-jit/05-system-pipeline](../p4-method-jit/05-system-pipeline.md))——届时 wazero 转为采石场(参考实现)而非依赖。**P3 的去留**(退役 vs 可移植中层)在 P4 验收时用数据定([../p4-method-jit/07-p3-retirement](../p4-method-jit/07-p3-retirement.md) §2 决策矩阵,缺省倾向退役)。

---

## 9. 不变式清单(实现与差分须守)

[01](./01-spike-gate.md)~[08](./08-testing-strategy.md) 各篇分别承担,本节聚合呈现:

1. **语义分发非投机**:gibbous 快路径判定与解释器一样的(IsNumber/同表同代次),失败走助手而非 deopt — 零 deopt 在代码层的兑现([06 §1](./06-ic-feedback-consume.md))。
2. **值编码/GCRef 两层逐位同一**:Wasm 侧不引入任何私有值表示([03 §2](./03-memory-model.md))。
3. **CallInfo 唯一真相**:gibbous 帧压 CallInfo(bit50),跨层只传 `base i32`;traceback/错误定位与解释器逐字节一致([04 §1](./04-trampoline.md) + [02 §4](./02-translation.md) pc 物化)。
4. **错误可穿越、yield 不可穿越**:status 链冒泡 vs 线程级 tier 规则([04 §4](./04-trampoline.md) / [07](./07-coroutine-thread-rule.md))。
5. **基线 memory-resident**:寄存器=共见栈槽;locals 缓存必须满足写回纪律([05 §4](./05-safepoint-gc.md))。
6. **升层单向**:gibbous 代码无运行期退回路径(零 deopt);fallback 都发生在编译前/编译时([../p2-bridge/04-try-compile-fallback](../p2-bridge/04-try-compile-fallback.md) §5)。
7. **解释器永不退役**:任何 Proto 始终保有可解释字节码([architecture](../architecture.md) §4 不变式 1);gibbous 只是可选加速面。
8. **arena = wazero memory**:同一块物理内存、同一套 NaN-box 编码、同一套偏移寻址([03 §1](./03-memory-model.md))。
9. **协程不升层**:线程级 tier 规则,主线程才允许走 gibbous([07](./07-coroutine-thread-rule.md))。

---

## 10. 风险与未决缺口汇总

各子文档 §缺口节 + [doc-gaps](../../../llmdoc/memory/doc-gaps.md):

- **wazero call boundary 实测**:S2 < 150ns 是 P3 生死检查。spike 不达标直跳 P4(详见 [01 §1.4](./01-spike-gate.md))。
- **wazero memory 共享 API 细节**(`memory.grow` 后 Go 视图稳定性 / import memory vs 读 module memory):待 spike 验证([03 §3](./03-memory-model.md))。
- **协程升层语义**:P3 开工前向首个宿主确认列内核是否跑在协程里([07 §3](./07-coroutine-thread-rule.md))。
- **批量 module 优化**:每 Proto 一 module 的实例化开销实测后定批量阈值([02 §1.2](./02-translation.md) + [11 缺口](#11))。
- **locals 缓存的槽选择算法**:FORLOOP 三槽之外是否扩展,待基线数据([02 §2.2](./02-translation.md))。
- **IC 快照失效 → 重编译**:与 P4 的再训练机制统一评估([06 §3](./06-ic-feedback-consume.md))。
- **边缘 spike 值的混合策略**:「调用密度」启发式进 P2 可编译性分析的精确定义([01 §1.4](./01-spike-gate.md))。
- **P3 去留**(退役 vs 中层):P4 验收时用数据定([../p4-method-jit/07-p3-retirement](../p4-method-jit/07-p3-retirement.md) §2 决策矩阵)。

---

相关:
[01-spike-gate](./01-spike-gate.md)(开工检查) ·
[02-translation](./02-translation.md)(翻译器) ·
[03-memory-model](./03-memory-model.md)(共见内存) ·
[04-trampoline](./04-trampoline.md)(跨层互调) ·
[05-safepoint-gc](./05-safepoint-gc.md)(跨层 GC) ·
[06-ic-feedback-consume](./06-ic-feedback-consume.md)(feedback 非投机消费) ·
[07-coroutine-thread-rule](./07-coroutine-thread-rule.md)(线程级 tier) ·
[08-testing-strategy](./08-testing-strategy.md)(差分验收) ·
[implementation-progress](./implementation-progress.md)(进度对账) ·
[../p2-bridge/00-overview](../p2-bridge/00-overview.md)(P2 总览,P3 是其消费者) ·
[../p4-method-jit](../p4-method-jit/00-overview.md)(P3 继承结构,只换发射后端) ·
[../p1-interpreter/00-overview](../p1-interpreter/00-overview.md)(P1 总览,P3 共享值表示与内存模型) ·
[../roadmap.md](../roadmap.md)(§2 四项税 / §4 P3 定义) ·
[../architecture.md](../architecture.md)(§3 包布局 + §4 不变式) ·
[../../../llmdoc/architecture/evolution-roadmap](../../../llmdoc/architecture/evolution-roadmap.md)(tier 映射 + 坐标系警告) ·
[../../../llmdoc/must/design-premises](../../../llmdoc/must/design-premises.md)(原则 1 解释器永不退役 / 原则 2 投机错误静默错果) ·
[../../../llmdoc/memory/doc-gaps](../../../llmdoc/memory/doc-gaps.md)(P3 开工前置确认 / P3 迁移留口)
