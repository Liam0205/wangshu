# P4 总览:gibbous/jit method JIT——文档地图 / 实现里程碑 / 验收 / 人月分解

> 状态:**实装阶段,PJ0-PJ7 amd64 端可达工程完整闭合**(2026-06-28):设计齐备 → 子目录 10 文件约 8200 行扩展轮 → 审查收口轮 → **PJ5 SELF + CALL Spike 1/2/3/4/5 全套真接入完整端到端 amd64 打通**(承 implementation-progress §9.20.13:Option B 帧建立内联 + zero-cross 优化 + 35 commits)+ PJ7 25+ 形态真接入 byte-equal + PJ11 luajc 档突破。剩 PJ8 arm64 物理 runner CI(物理依赖)+ PJ9 双架构差分套(待 PJ8)+ bit50 协议拍板(用户决策)+ P3 退役决议(PJ11 验收用户拍板)。本文是 P4 文档集(00-08 + implementation-progress)的导航与施工计划:每篇文档的定位、组件依赖、构建顺序、里程碑验收门槛、人月分解、跨文档定稿决策速查、与 P1/P2/P3 的桥接(已落地的前瞻义务对账)。
> 上游契约:[../roadmap](../roadmap.md)(§4 P4 定义、§2 四项税、§1 校准测量、§7 prior art)、[../architecture](../architecture.md)(§1 包布局 `internal/gibbous/jit`)、[../../../llmdoc/architecture/evolution-roadmap](../../../llmdoc/architecture/evolution-roadmap.md)(tier 映射:P4 = gibbous tier-1,与 P3 同层)、[../../../llmdoc/must/design-premises](../../../llmdoc/must/design-premises.md)(前提一负载形状 / 前提二四项税 / 前提三五原则 / 前提四第一天 NaN-box 承诺)。
> P1 依赖面:[01](../p1-interpreter/01-value-object-model.md)(NaN-box u64 + GCRef offset)、[02](../p1-interpreter/02-bytecode-isa.md)(源 ISA + §7 IC slot)、[05](../p1-interpreter/05-interpreter-loop.md)(§1 CallInfo / §7 调用协议——OSR 着陆面)、[12](../p1-interpreter/12-testing-difftest.md)(§3.8 Runner 抽象 / §7 P4 行 / §8 CI 门禁)。
> P2 依赖面:[../p2-bridge/00-overview](../p2-bridge/00-overview.md)(P4 是 P2 决策的消费者)、[../p2-bridge/02-ic-feedback](../p2-bridge/02-ic-feedback.md) §4(TypeFeedback shape + confidence)、[../p2-bridge/03-compilability-analysis](../p2-bridge/03-compilability-analysis.md) §3.7(F7 后端能力 SupportsAllOpcodes)、[../p2-bridge/04-try-compile-fallback](../p2-bridge/04-try-compile-fallback.md)(零 deopt 单向状态机基线——P4 在此基础上加 deopt 边)、[../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md)(P3Compiler 接口 + GibbousCode 抽象 + P4Feedback 反向读)。
> P3 依赖面:[../p3-wasm-tier/00-overview](../p3-wasm-tier/00-overview.md)(P4 继承 P3 全部分层结构;P3 PW0-PW10 已交付,本机 Xeon 6982P 实测基线 loop 2.95x / table 0.88x / call 0.52x / mixed 0.99x;P3 现状是 P4 立项的重要输入)、[../p3-wasm-tier/04-trampoline](../p3-wasm-tier/04-trampoline.md)(跨层协议;P4 继承 bit50 / status 链 / base 刷新)、[../p3-wasm-tier/05-safepoint-gc](../p3-wasm-tier/05-safepoint-gc.md)(三类 safepoint——P4 自付物理)。
> 下游 P5 衔接:[../p5-trace-jit/00-overview.md](../p5-trace-jit/00-overview.md)(下一站:只在 P4 收益不够时启动,P4 验收数据是 P5 立项输入)。
>
> P4 目标一句话:**gibbous(tier-1)的第二个发射后端,继承 P3 全部分层结构,只换发射后端——把 P2 决策机产出的 gibbous 代码请求兑现成原生机器码,首次让望舒走出 wazero 外包,达到 LuaJ-luajc 档(列内核负载 ≥164μs)**。

对应 Go 包:`internal/gibbous/jit`(amd64/arm64 双后端、OSR exit、自管机器栈、trampoline)。

---

## 0. 文档地图:谁定什么(单一事实源分工)

| 文档 | 定位 | 单一事实源(其它文档以它为准) |
|---|---|---|
| [01-launch-judgment](./01-launch-judgment.md) | 启动闸门 | luajc 档量化锚点(164μs)、两条进入路径(常规/跳跃)、立项三档策略(全启/部分前置/跳过)、立项判定决策树、立项前后判定权分离 |
| [02-template-direction](./02-template-direction.md) | 方向裁决 | JSC Baseline / V8 Sparkplug 风格 per-function 模板编译、候选谱系否决(无 IR / 无跨指令 regalloc)、F1-F7 闸门继续生效、模板编译可行性论证 |
| [03-speculation-ic](./03-speculation-ic.md) | 类型投机 | IC 反馈消费(P4Feedback 反向读)、按 FeedbackKind 五档投机模板、guard 显式比较硬约束(无信号陷阱)、状态机加 deopt 边(P2/P3 零 deopt 基线打破)、stableShape/stableIndex 直达槽、重训练协议 |
| [04-osr-deopt](./04-osr-deopt.md) | OSR exit 协议 | 函数级 exit 语义、物化 = memmove(第一天值表示承诺现金兑付)、栈槽真相不变式、exitPC↔字节码映射、再训练防 deopt 风暴、P4 内部 `p4SpecState[proto]` 子状态机(P4Speculative/P4Deoptimized/P4StuckSpeculation)|
| [05-system-pipeline](./05-system-pipeline.md) | 系统管线 | 四项税逐项自付方案(自管机器栈 / 回边抢占检查 / jitContext 稳定指针 / 写屏障白赚)、exec mmap/W^X/icache flush/trampoline 四件套、JIT 世界边界、trampoline 三出口(正常/OSR exit/慢路径)、arena base 重载协议 |
| [06-backends](./06-backends.md) | 双后端 | 共享骨架 + per-arch 发射器(否决宏汇编)、寄存器约定、模板分级表达、双架构 CI 双跑、PJ 里程碑、CI 含 Go 1.25/1.26/tip 矩阵 |
| [07-p3-retirement](./07-p3-retirement.md) | P3 去留决策框架 | 「留作可移植中层」表面论据 + 关键拆穿(wazero 编译引擎与 P4 共享平台约束)、决策矩阵、缺省倾向退役、P4 验收时定 |
| [08-testing-strategy](./08-testing-strategy.md) | 验收 + 测试 | luajc 档验收口径、V1-V22 总表(P1 V1-V18 + P4 增项 V19-V22)、同 Proto 差分主防线(P4 接续 P3 force-all 套)、OSR 状态等价 V19、deopt 注入 V20、双架构双跑 V21、prove-the-path 纪律继承 |
| [09-acceptance-checklist](./09-acceptance-checklist.md) | PJ11 验收勾选清单 | 三平台 × V1-V22 可勾表、决议项(D1 bit50 ✅ / D2 P3 退役 ⬜)、性能数字归档(V14/V15a/V15b/V16)、与设计文档差异点 |
| [10-per-op-translator](./10-per-op-translator.md) | PJ10 工程方向 | 逐 opcode 翻译器架构、与 P3 wasm 翻译器对照、双轨(fast path 保留 + 通用 per-op 路径新增)、寄存器 ABI、opcode 翻译表、sub-PJ 拆分(PJ10a-d)、与 P5 trace JIT 的接口 |
| [implementation-progress](./implementation-progress.md) | 进度 | 立项前置闸门检查、PJ 里程碑(预设占位)、设计期决策盘点(影响 × 不确定度三档)、**跨文档回填请求收口表**(本文档集 §回填请求节聚合) |

阅读顺序建议:**实现者**先读 00→01(立项闸门通过才动手)→02(方向裁决,锁定模板编译不上 IR)→03(投机,P4 最危险面)→04(OSR exit,deopt 协议)→05(系统管线,四项税自付的物理)→06(双后端,寄存器约定与 CI 纪律)→07(P3 去留,P4 验收时决议)→08(测试,每 PJ 验收口径)→10(PJ10 逐 opcode 翻译器,2026-06-30 启动的覆盖率工程)→09(PJ11 验收勾选清单),implementation-progress 收口查。**评审者**先读 00→01→02 三篇拿到「P4 是不是该做、做什么、不做什么」三句,再按需深入 03/04(投机正确性)或 06/08(工程纪律)或 10(覆盖率工程方向)。

---

## 1. P4 与 P1/P2/P3/P5 的边界(谁拥有什么)

| 关注点 | P1(crescent) | P2(bridge) | P3(gibbous/wasm) | **P4(gibbous/jit)拥有** | P5(fullmoon/trace) |
|---|---|---|---|---|---|
| 编译哪些 Proto(热度 + 可编译性) | — | ✅ 决策 | 只接收 | 同 P3,只接收 | 同 P3,只接收 |
| 类型 feedback 产出 | IC 写入 | ✅ 聚合 | 可选消费(代码形状 hint) | ✅ **必需**消费(投机依据)| 必需消费(投机 + 路径稳定) |
| 字节码→Wasm 翻译 | — | — | ✅ | — | — |
| 字节码→原生码翻译 | — | — | — | ✅ [02-template-direction](./02-template-direction.md) + [06-backends](./06-backends.md) | trace IR → 原生 |
| **linear memory / arena backing** | arena Go 堆 backing | — | wazero memory 收养 | ✅ **切回纯 Go 堆 backing**([05 §3.5](./05-system-pipeline.md)) | 同 P4 |
| **trampoline / 互调协议** | enterLuaFrame | — | ✅ trampoline 协议 | 继承同款协议 + per-arch asm 落地([05 §4](./05-system-pipeline.md)) | 同 P4 |
| **跨层 safepoint** | 解释器主循环 | — | 三类 safepoint(wazero 外包) | ✅ 三类 safepoint 自付([05 §6](./05-system-pipeline.md)) | 同 P4 |
| **CallInfo bit50** | 不读不写 | 不写 | trampoline 写 1 | 同 P3(继承)| 同 P3 |
| **零 deopt(fallback 而非投机)** | 永久解释 | ✅ 单向状态机 | 严格遵守 | ❌ **引入 deopt 边**([03 §4](./03-speculation-ic.md))| 同 P4(并加 trace 黑名单)|
| **GibbousCode 实现方** | — | 接口定义 | wazero `api.Function` 包装 | ✅ 原生码段包装(同接口)| trace 段包装 |
| **解释执行(fallback 着陆点)** | ✅ 永不退役 | — | — | — | — |
| **协程升层** | ✅ 跑 crescent | 升层判定加 onMain bool | ❌ 不升层 | ❌ 不升层(继承 P3 线程级 tier 规则) | 同 P3/P4 |
| **OSR exit 着陆** | — | — | — | ✅ 函数级 exit 回解释([04](./04-osr-deopt.md))| 跨帧 snapshot exit |
| **自管机器栈** | — | — | — | ✅ Go-allocated `[]byte`,trampoline 切 SP | 同 P4 |

> **tier 坐标系警告**([../../../llmdoc/architecture/evolution-roadmap](../../../llmdoc/architecture/evolution-roadmap.md)):月相 tier 比阶段粗一层。P1=tier-0(crescent),P3/P4=tier-1(gibbous),P5=tier-2(fullmoon)。**P3 与 P4 同属 tier-1 但发射后端不同**:P3 发 Wasm(wazero 执行)、P4 发原生码(自管 codegen)。代码包名据此:`internal/gibbous/wasm`(P3)、`internal/gibbous/jit`(P4)。日志统一是 `function promoted to gibbous`(不区分子档)。

**一句话**:**P2 决策「编谁」+ 产「feedback 料」,P3/P4 都是 P2 决策的兑现机器**(不做热度/可编译性判定);**P4 与 P3 唯一区别是后端**(wazero Wasm vs 自管原生码)和**投机面**(P3 零 deopt / P4 引入 deopt)。投机是 P4 的核心新增,deopt 边是其结构性代价。

---

## 2. 总数据流(承 P2 决策 + 承继 P3 分层骨架)

```
                P2 决策机(已交付)
                 │
                 ▼
   considerPromotion(proto, pd, onMain) 走 try-compile 路径
                 │
                 ▼
   P3Compiler.Compile(proto, fb) ── 入参签名见 [P2 05 §2.1]
   (P3 / P4 同一接口,build tag 选其一)
                 │
        ┌────────┴────────┐
        │                 │
   ① 翻译期            ② 失败路径
        │                 │
        ▼                 ▼
   per-function         TierStuck(永久解释)
   模板线性扫        ← P4 不区分错因
   ([02] / [06])
        │                 ↑
        ▼                 │
   exec mmap            │ panic 经 P2 [04 §5.2] defer recover 转
   PROT_RW→PROT_RX      │ *CompileError(Kind=BackendPanic)
   ([05] §2)              │
        │                 │
        ▼                 │
   GibbousCode 产物 ──────┘
        │
        ▼
   installGibbous([P2 04 §4.4])
        │
        ▼
   TierGibbous(P2 视角吸收态;P4 内部 p4SpecState=P4Speculative,直到 OSR exit 后转 P4Deoptimized)
        │
        ▼
   crescent.doCall 检测 tierState ⇒
        trampoline 切 SP 进自管机器栈
        装 jitContext 入固定寄存器
        跳入原生码
        ([05] §2 trampoline 进)
        │
        ▼
   原生码运行(共见 arena memory + 自管机器栈)
        ├── 直线模板:MOVE / 算术 / FORLOOP 等([02] §1 + [06] §3)
        ├── 投机模板:IC feedback 决策 + f64 快路径 + guard([03])
        ├── 慢路径 helper:经 trampoline 出 → Go → 回([05] §4.3)
        ├── safepoint:回边 preemptFlag 检查([05] §6.3)
        ├── guard 失败:跳 exit stub → 物化栈槽 → trampoline 出
        │                      ↓ OSR exit([04] §3 + §6)
        │            crescent.doCall 收 status=2 续解释 exitPC 起
        └── RETURN:结果回填寄存器(共见栈槽),return status=0
        │
        ▼
   trampoline 弹 CallInfo,继续 crescent 解释 / 或调用方原生码续跑
```

**核心物理事实**:① **值表示** NaN-box u64 与 P1/P3 逐位同一,P4 生成码直接操作(承 [../../../llmdoc/must/design-premises](../../../llmdoc/must/design-premises.md) 前提四);② **arena = 共见物理内存**(承 P3 [03 §1](../p3-wasm-tier/03-memory-model.md),P4 build 下 backing 切回纯 Go 堆,不经 wazero memory 中介,但偏移寻址协议同 P3);③ **跨层只传 base i32 / i64**(承 P3 [04](../p3-wasm-tier/04-trampoline.md) 协议),其余从共见栈槽自取(参数、返回值、IC slot);④ **OSR exit 物化 = memmove**(承 P1 [01 §7](../p1-interpreter/01-value-object-model.md) 不变式 1「跨 tier 拷贝是 memmove」,P4 兑付该承诺)。

---

## 3. 组件依赖与关键耦合点

依赖图见 [../architecture](../architecture.md) §1:`internal/gibbous/jit` 依赖
- `bytecode`(读 Proto + IC slot,翻译输入);
- `value`(NaN-box / GCRef 工具函数 — 与解释器、P3 共用);
- `arena`(共见值栈 + CallInfo,P4 build 下纯 Go 堆 backing);
- `bridge`(实现 P3Compiler + 经 P4Feedback 反向消费 TypeFeedback;[../p2-bridge/05](../p2-bridge/05-p3-p4-interface.md));
- `crescent`(被调:gibbous-jit→crescent 走 trampoline 助手 [05 §4.3](./05-system-pipeline.md);**反向依赖** — P4 必须能把控制权交回解释器跑未编译 Proto 与 OSR exit 续跑)。

设计定稿后的**关键耦合点**(实现时最易出错处):

1. **arena base 重载协议**([05 §5](./05-system-pipeline.md)):arena 搬迁(grow)只发生在分配慢路径(= 出了 JIT 世界);JIT 内联的 bump 分配快路径越界即出去,回来后从 jitContext 重载 base。这是 P1 [05 §1.3](../p1-interpreter/05-interpreter-loop.md) reloadFrame 纪律在机器码层的同构。**实测复核点**:PJ3 回边 safepoint + PJ5 跨层互调时验证。

2. **CallInfo bit50 `callStatus_gibbous`**([04 §0.2](./04-osr-deopt.md) + 继承 [P3 04 §1.2](../p3-wasm-tier/04-trampoline.md)):JIT 帧入口 trampoline 把 bit50 置 1,标识「此帧走原生码路径」。OSR exit 后 bit50 清 0 还是保留 1 倾向**清 0**(差分友好,P4 落地时实测确认,详 [04 §12 回填请求](./04-osr-deopt.md))。

3. **SupportsAllOpcodes 渐进白名单**([06 §3.8](./06-backends.md) + [P2 03 §3.7](../p2-bridge/03-compilability-analysis.md)):P4 后端开发期渐进扩充,初期只支持 ISA 0..37 的子集(LOADK / GETTABLE / CALL / RETURN / FORLOOP / JMP 等热路径 opcode)。任何含未支持 opcode 的 Proto 经 F7 闸门拒。**保守缺省**:supported 表只把明确实现的 opcode 加入,其他全部返 false。**与 P3 同款机制,但白名单内容独立**(P3 已覆盖 38 opcode 除 VARARG,P4 PJ1-PJ7 逐 PJ 扩充)。

4. **寄存器分配的 jitContext**([05 §3.3](./05-system-pipeline.md) + [06 §4](./06-backends.md)):JIT 代码所需的所有 Go 侧能力(arena base、helper 表、preemptFlag、exit reason code)装入 Go 堆上的 `jitContext` struct,经固定寄存器(amd64: r15;arm64: x28 待定)传入;Go 堆对象不移动,故 context 指针稳定(承 P1 [01 §2](../p1-interpreter/01-value-object-model.md)「GCRef 非 Go 指针」纪律的对偶面)。

5. **双架构 CI 双跑**([06 §5](./06-backends.md) + [08 §6](./08-testing-strategy.md)):amd64 与 arm64 物理 runner 各跑全套差分门禁(同 Proto crescent vs gibbous-jit byte-equal),交叉编译只能保证能构建不能代跑差分。**CI 矩阵增项**:Go 1.25 / 1.26 / tip 三版本(承 [06 §8.2 开放问题](./06-backends.md))。

6. **OSR exit 物化静态生成**([04 §3.6 / §3.7](./04-osr-deopt.md) + [04 §6](./04-osr-deopt.md)):若某些模板为性能在边界间短暂缓存值(如 FORLOOP 循环变量驻留寄存器),则相应 guard 的 exit 需补一段「寄存器→栈槽」写回序列——**每 exit 点编译期生成,固定几条 store**,杜绝运行期 snapshot 解释器(否则 deopt 复杂度直升 P5 量级)。

---

## 4. 实现里程碑(细化 [02 §5.3](./02-template-direction.md) / [06 §6](./06-backends.md) PJ 编号施工顺序)

每步可独立编译 + 单测通过再进下一步。「验收」列是该步的完成定义;PJ 编号(P-JIT)供排期引用。**P4 核心验收门是性能(列内核负载 ≥164μs luajc 档)**,与 P3(≥2x loop) / P2(决策正确) 不同。

| PJ | 内容 | 对应文档 | 验收(完成定义) |
|---|---|---|---|
| **PJ0** | 立项判定 + 包骨架 | [01](./01-launch-judgment.md) + [06 §6.1](./06-backends.md) | **立项判定通过**(三档「全启 / 部分前置 / 跳过」决议,详 [01 §3.4](./01-launch-judgment.md))+ `internal/gibbous/jit/{amd64,arm64}` 包骨架 + build tag 隔离 + bridge 注入 P4Compiler 后 SupportsAllOpcodes 全 false ⇒ 所有 Proto 仍走 crescent |
| PJ1 | amd64 trampoline + 直线模板(MOVE/LOADK/LOADBOOL/LOADNIL/JMP/RETURN) | [05 §4](./05-system-pipeline.md) + [06 §3.1](./06-backends.md) | 一个 6-op 直线 Proto 升层后 byte-equal 解释结果;升层日志 `function promoted to gibbous` 触发;exec mmap + W^X 翻面工作;trampoline 进/出对称 |
| PJ2 | amd64 算术 + 比较模板 + **IsNumber×2 guard**(投机模板首落地)| [03 §2 / §5](./03-speculation-ic.md) + [06 §3.2](./06-backends.md) | 双 number 快路径直发 `mulsd`/`addsd`(以及对应 cmp)+ NaN 规范化;guard 失败 OSR exit 回解释;混合类型 fallback 走通用模板(语义完备) |
| PJ3 | amd64 控制流 + FORLOOP + 回边 safepoint | [05 §6.3](./05-system-pipeline.md) + [06 §3.3](./06-backends.md) | 数值 for 循环编译后 ≥**luajc 档**(列内核 ≥164μs 一档,详 [01 §1](./01-launch-judgment.md) + [08 §1](./08-testing-strategy.md));回边 preemptFlag 检查 byte-equal;长循环 STW GC 延迟 < 阈值 |
| PJ4 | amd64 表 IC 模板(GETTABLE/SETTABLE/GETGLOBAL/SETGLOBAL/SELF + NEWTABLE/LEN/CONCAT/SETLIST)+ **stableShape/stableIndex 直达槽投机** | [03 §6](./03-speculation-ic.md) + [06 §3.4](./06-backends.md) | 单态表(FBTableMono)guard 通过 + 直达槽 load/store 跳哈希;形状变化 deopt + 再训练降级通用模板;NEWTABLE/LEN/CONCAT/SETLIST 走 helper(承 06 §3.8 PJ4 行) |
| PJ5 | amd64 CALL/TAILCALL + 跨层互调 + **OSR exit 实装** | [04](./04-osr-deopt.md) + [05 §4.3](./05-system-pipeline.md) + [06 §3.5](./06-backends.md) | gibbous-jit 内调未编译 Proto 经 trampoline 走 crescent;gibbous-jit→gibbous-jit / gibbous-jit→host 三向分派;OSR exit 后续跑 exitPC 起的状态等价(V19) |
| PJ6 | amd64 CLOSURE/CLOSE + upvalue | [06 §3.6](./06-backends.md) | 闭包构造 + 开放/关闭 upvalue 与解释器 byte-equal(P3 同款经 helper 复用 makeClosure/closeUpvals) |
| PJ7 | amd64 端到端验收 + 性能基准 | [08 §1 / §3](./08-testing-strategy.md) | **单架构 V1-V22 全过**——正确性 V1-V13 byte-equal + V19 OSR 等价 + V20 deopt 注入 + V14 列内核 luajc 档 + V17 四 build + V18 -race |
| PJ8 | arm64 后端启动 + 同框架渐进交付 | [06 §6 / §3-§6](./06-backends.md) | arm64 各 opcode 模板按族落地(amd64 模板族的 per-arch 镜像);macOS arm64 `MAP_JIT` + `pthread_jit_write_protect_np` 工作;icache flush 序列写入 |
| PJ9 | arm64 端到端验收 + 双架构差分套 | [06 §5](./06-backends.md) + [08 §6](./08-testing-strategy.md) | **双架构 V1-V22 全过**——amd64/arm64 各自全套差分门禁 byte-equal;Go 1.25/1.26/tip 矩阵 CI 绿 |
| **PJ10** | 逐 opcode 翻译器(per-op translator) | [10](./10-per-op-translator.md) | **P4 覆盖率工程**:PJ0-PJ9 字节级模板路径保留作 fast path;新增「逐 opcode 翻译」通用路径让任意 Proto 都能升 P4(承 [10 §1](./10-per-op-translator.md));拆 PJ10a(直线 op)/ PJ10b(算术 + 比较)/ PJ10c(控制流 + 循环)/ PJ10d(表 + 函数调用),每档累积扩 supported 集 |
| **PJ11** | luajc 档验收 + 性能调优 | [01](./01-launch-judgment.md) + [08 §8](./08-testing-strategy.md) | **P4 总验收**:列内核负载 ≥luajc 档(≥164μs 水位 over gopher-lua 同基准);承 [01 §4](./01-launch-judgment.md) 四个分档约束(列内核形状 + 真实负载 + 双架构 + 不豁免差分) |

> **PJ0 启动条件**:P3 已交付(本机 Xeon 6982P 实测基线 **loop 2.95x over P1(= 7.2x over gopher-lua,已超 luajc 档 4.4x over gopher-lua) / table 0.88x / call 0.52x / mixed 0.99x;后三档仍 ≪ luajc 档列内核形态——P4 立项动机不在 loop 而在非 loop 形态**,详 [01 §3.3](./01-launch-judgment.md));**立项判定通过**(详 [01 §3.1 三条必备条件](./01-launch-judgment.md):真实宿主负载需求 + 资源到位 + 设计文档齐备)。立项判定本身可能否决 P4(详 [01 §3.4 跳过档](./01-launch-judgment.md))。
>
> **PJ7 验收口径**:V1-V22 总表见 [08 §2](./08-testing-strategy.md);P4 增项 V19(OSR 等价)/ V20(deopt 注入)/ V21(双架构)/ V22(guard 漏判 fuzz,详 [08 §2.4](./08-testing-strategy.md))。
>
> **PJ11 是 P4 总验收**:不达标视情况(详 [01 §4.3 第二闸门](./01-launch-judgment.md)):中途仍可止损改 P5 路径或退守 P3 永久基线。

---

## 5. 人月分解([../roadmap](../roadmap.md) §4:+1-2 人年,人年级首站)

按单人全职折算;区间下沿=顺利,上沿=含 spike 反复 + 双架构返工 + luajc 档逼近的实测调优:

| 里程碑段 | 内容 | 估算 |
|---|---|---|
| PJ0 | 立项判定 + 包骨架 + build tag 隔离 | 0.5 - 1 人月(立项判定本身可能耗 0.25-0.5 人月,即便否决也不亏——产出归档作未来再启的依据) |
| PJ1 | amd64 trampoline + 直线模板(6 opcode) | 1 - 2 人月(系统管线四件套首次落地,exec mmap / W^X / trampoline asm 是工程坎)|
| PJ2 | amd64 算术 + 比较 + IsNumber 投机 guard | 1 - 2 人月(首条投机模板,guard 形态硬约束验证)|
| PJ3 | amd64 控制流 + FORLOOP + 回边 safepoint | 1 - 2 人月(数值 for 循环达 luajc 档单档,**P4 价值首次实证**)|
| PJ4 | amd64 表 IC + stableShape/Index 直达槽 + deopt 边 | 1.5 - 2.5 人月(P3 PW5 同款翻译复杂度峰值 + 投机 IC,deopt 状态机首次接入)|
| PJ5 | amd64 CALL 系列 + 跨层互调 + OSR exit 实装 | 1.5 - 3 人月(OSR exit 物化 + status 链 + 跨层错误冒泡 + pcall 边界透明,P3 PW6 同档峰值 + deopt 路径)|
| PJ6 | amd64 CLOSURE + upvalue | 0.5 - 1 人月(P3 PW7 同款,upvalue 已被 VS0-c 解掉物理协议)|
| PJ7 | amd64 端到端验收 + V1-V22 + 性能基准 | 1 - 2 人月(差分 fuzz + deopt 注入 + GC 压力,**P4 单架构总验收**)|
| PJ8 | arm64 后端启动 + 渐进交付 | 2 - 3 人月(per-arch 发射器全栈镜像 + `MAP_JIT` / icache flush 工程坎)|
| PJ9 | arm64 端到端验收 + 双架构差分套 | 1 - 2 人月(amd64/arm64 行为差消解,Go 版本矩阵 CI)|
| **PJ10** | 逐 opcode 翻译器(per-op translator) | 2.5 - 4 人月(PJ10a 直线 op 0.5-1 / PJ10b 算术比较 1-1.5 / PJ10c 控制流循环 0.5-1 / PJ10d 表与函数调用 1-1.5;承 [10](./10-per-op-translator.md))|
| PJ11 | luajc 档验收 + 性能调优 | 2 - 4 人月(逼近 luajc 档的实测反复 + 真实宿主负载校准 + locals 寄存器缓存可能展开 + guard 合并窥孔)|
| **合计** | | **+14.5 - 28 人月 ≈ +1.2-2.3 人年** |

与 [../../../llmdoc/architecture/evolution-roadmap](../../../llmdoc/architecture/evolution-roadmap.md) 的「+1-2 人年」基本吻合(PJ10 加入后上沿略超,但 PJ0-PJ9 字节级模板路径继续以 fast path 形态保留,实际 PJ11 验收复用 PJ0-PJ9 资产,不是叠加新工作)。**PJ8 arm64 + PJ10 覆盖率工程 + PJ11 性能调优是大头**——双架构维护、覆盖率扩 SAO 白名单、luajc 档逼近的实测反复都不可计划:arm64 是 per-arch 全栈第二轮(`MAP_JIT` / icache 是 amd64 不会碰的工程面);PJ10 是 V15b heavy 实测暴露后的方向修正,从「按形态写字节级模板」改成「逐 opcode 通用翻译」(详 [10](./10-per-op-translator.md));PJ11 调优可能要展开 locals 寄存器缓存(详 [04 §3.6 / §3.7](./04-osr-deopt.md) / [06 §4](./06-backends.md))或 guard 合并窥孔(详 [03 §3.6](./03-speculation-ic.md))。**PJ0 立项判定**:即便否决也不亏(产出 = 立项报告永久存档,见 [01 §0.3](./01-launch-judgment.md)「闸门双向性」)。

---

## 6. 跨文档定稿决策速查(实现前必读)

设计期在 8 篇 P4 子文档间协商定稿的关键决策,集中列出防止实现时只读单篇而漏掉(每行带出处):

| 决策 | 定稿 | 出处 |
|---|---|---|
| **模板编译,不做优化编译器** | per-function 线性扫 + 模板贴入,无 IR / 无 SSA / 无跨指令 regalloc | [02 §1 / §3](./02-template-direction.md) |
| **全显式 guard,无信号陷阱** | 每 guard = 2-3 条「比较 + 条件跳」恒定成本(纯 Go 禁陷阱) | [03 §3](./03-speculation-ic.md) |
| **函数级 OSR exit,不跨帧 / 不跨指令** | exit 单位 = 当前函数帧;exitPC 起整体交还解释器 | [04 §1](./04-osr-deopt.md) |
| **物化 = memmove** | 第一天值表示承诺现金兑付;NaN-box u64 跨 tier 拷贝零格式转换 | [04 §2](./04-osr-deopt.md) + [P1 01 §7](../p1-interpreter/01-value-object-model.md) |
| **栈槽真相不变式** | 每条字节码边界处,全部 Lua 活值已物化在 arena 值栈槽;机器寄存器只在模板内部短暂持值 | [04 §3.1](./04-osr-deopt.md) |
| **自管机器栈 + jitContext** | JIT 跑 Go-allocated `[]byte` 栈,trampoline 切 SP;jitContext 经固定寄存器传(amd64 r15)| [05 §1.1.2 / §3.3](./05-system-pipeline.md) |
| **共享骨架 + per-arch 发射器,不用宏汇编** | 编译驱动 + guard/OSR 逻辑架构无关;每 opcode 发射函数按架构各一份 | [06 §1](./06-backends.md) |
| **双架构 CI 双跑** | amd64 / arm64 物理 runner 各跑全套差分门禁;交叉编译只验构建 | [06 §5.1](./06-backends.md) + [08 §6](./08-testing-strategy.md) |
| **引入 deopt 边** | P2 状态机仍三态单向无 deopt 边;**P4 内部状态机叠加 deopt 边**(`p4SpecState[proto]`:P4Speculative ─guard失败OSR exit─► P4Deoptimized),不冲击 P2 单一事实源 | [03 §4](./03-speculation-ic.md) + [04 §5.2](./04-osr-deopt.md) |
| **F1-F7 闸门继续生效** | 投机叠加在「可编译子集之内」,与不可编译走 fallback 正交 | [02 §4.4](./02-template-direction.md) + [03 §4.5](./03-speculation-ic.md) |
| **P3 去留 P4 验收时定** | 结构上共存零成本(`internal/gibbous/{wasm,jit}` 并列);策略上缺省退役 | [07](./07-p3-retirement.md) |
| **luajc 档 = 列内核 ≥164μs 一档** | 校准测量 1(Horner 5 次多项式)的 LuaJ-luajc 水位;LuaJIT 仅快 6%,达标即「逼近 LuaJIT 档」 | [01 §1](./01-launch-judgment.md) + [08 §1](./08-testing-strategy.md) + [../roadmap](../roadmap.md) §1 |
| **P4 build 下 arena backing 切回 Go 堆** | 不经 wazero memory 中介,但偏移寻址协议同 P3 | [05 §3.5](./05-system-pipeline.md) + [P3 03 §0.4](../p3-wasm-tier/03-memory-model.md) |
| **协程不升层(继承 P3 线程级 tier 规则)** | 主线程才允许走 gibbous;协程线程一律走 crescent;onMain bool 经 considerPromotion 透传 | [P3 07](../p3-wasm-tier/07-coroutine-thread-rule.md) |
| **P4 自管投机生命周期,P2 状态机不感知** | 方案 A:P2 三态 `TierInterp/TierGibbous/TierStuck` 不变;P4 内部 `p4SpecState[proto]` 子状态机自管(P4Speculative/P4Deoptimized/P4StuckSpeculation);OSR exit / 重训练 / 拉黑投机全 P4 实装 | [03 §4.2 / §8](./03-speculation-ic.md) + [04 §5](./04-osr-deopt.md) |

---

## 7. P1/P2/P3 已落地的前瞻义务对账(P4 启动前置)

P1 全卷已交付(M0-M14) + P2 PB0-PB7 + 后续优化轮 #1-#4 + P3 PW0-PW10 + VS0-e 全卷已交付(2026-06-16)。P4 依赖的「前瞻留口」状态:

| 前瞻义务 | 落地状态 | 出处 | P4 消费方式 |
|---|---|---|---|
| **Proto 旁 IC slot 数组(按 pc 索引)** | ✅ P1 已落地 | [P1 02 §7](../p1-interpreter/02-bytecode-isa.md) | PJ4 编译期读 IC + feedback 选投机模板 |
| **TypeFeedback shape**(P4 可用) | ✅ P2 PB2 已落地 | [P2 02 §4](../p2-bridge/02-ic-feedback.md) | PJ2/PJ4 反向读做投机决策(FBArithStableNumber / FBTableMono / ...) |
| **P3Compiler 接口**(P4 复用) | ✅ P2 PB6 已定义 | [P2 05 §2](../p2-bridge/05-p3-p4-interface.md) | PJ0 起 `internal/gibbous/jit` 实现该接口 |
| **GibbousCode 抽象** | ✅ P2 PB6 已定义 | [P2 05 §6](../p2-bridge/05-p3-p4-interface.md) | PJ0 起包装原生码段 |
| **GibbousCode.Run status=2 编码**(DEOPT)| ✅ P2 PB6 已预留(`P3 永不返回 2,P4 才返回 2`)| [P2 05 §6.1](../p2-bridge/05-p3-p4-interface.md) | PJ5 OSR exit 返回 status=2 触发 doCall 续解释 |
| **TierState 单向 + 吸收态** | ✅ P2 PB4 已落地 | [P2 04 §2](../p2-bridge/04-try-compile-fallback.md) | P4 在 `TierGibbous` 内自管投机状态机(`p4SpecState[proto]`,承 [03 §4.2](./03-speculation-ic.md));P2 三态枚举不动 |
| **installGibbous** | ✅ P2 PB4 已落地 | [P2 04 §4.4](../p2-bridge/04-try-compile-fallback.md) | PJ0 起被 P2 调用 |
| **F1-F7 + SupportsAllOpcodes** | ✅ P2 PB3 + PB6 已落地 | [P2 03 §3.7](../p2-bridge/03-compilability-analysis.md) + [P2 05](../p2-bridge/05-p3-p4-interface.md) | PJ0 起 supported 表初空,逐 PJ 扩充 |
| **CallInfo bit50 `callStatus_gibbous`** | ✅ P3 PW6 已落地 | [P1 05 §1.2](../p1-interpreter/05-interpreter-loop.md) + [P3 04 §1](../p3-wasm-tier/04-trampoline.md) | P4 trampoline 进帧时同款写 1 |
| **协程升层判定加 onMain bool** | ✅ P3 PW8 已落地 | [P3 07](../p3-wasm-tier/07-coroutine-thread-rule.md) + [P2 04 §3](../p2-bridge/04-try-compile-fallback.md) | P4 沿用线程级 tier 规则(主线程才允许 gibbous-jit) |
| **arena backing**(P4 切回 Go 堆) | ✅ P3 [03 §0.4](../p3-wasm-tier/03-memory-model.md) 已留 P4 build 下 BackingFn = DefaultBacking | [P3 03 §0.4 / §1](../p3-wasm-tier/03-memory-model.md) | P4 不经 wazero memory,Go 堆 backing |
| **跨层互调协议**(P4 继承) | ✅ P3 PW6 已落地 | [P3 04](../p3-wasm-tier/04-trampoline.md) | P4 同款 trampoline 入口 + status 链 + 错误冒泡 |
| **差分轨道**(P1 V1-V18 + P3 V1-V18) | ✅ P3 PW9 已落地 | [P3 08](../p3-wasm-tier/08-testing-strategy.md) + [P1 12 §3.8 / §7](../p1-interpreter/12-testing-difftest.md) | P4 接续 V19-V22 增项(承 [08](./08-testing-strategy.md)),`WangshuGibbousJIT` runner 新增 |
| **prove-the-path 纪律 first-class guide** | ✅ 已 promote | [../../../llmdoc/guides/prove-the-path-under-test](../../../llmdoc/guides/prove-the-path-under-test.md) | P4 投机命中 / OSR exit 命中 / deopt 注入路径全要白盒计数器(承 P3 七实例) |

**结论**:**P4 启动是条件增量**——P1/P2/P3 全卷已交付,所有前瞻留口已落地;**唯一阻塞**是 P4 立项判定本身(承 [01](./01-launch-judgment.md) + [implementation-progress §0](./implementation-progress.md))。立项前的所有 P1/P2/P3 工作都已铺好;立项即开 PJ0。

---

## 8. 「P4 选自管 codegen 的本质」是收回 wazero 的四项税外包

[../roadmap](../roadmap.md) §2 的四项税,P3 把全部外包给 wazero(详 [P3 00-overview §8](../p3-wasm-tier/00-overview.md));P4 **收回这层外包,逐项自付**:

| 税([../roadmap](../roadmap.md) §2) | P3 外包给 wazero | P4 自付方案 |
|---|---|---|
| GC 精确栈扫描 | wazero 自管栈,Go GC 不扫生成码帧 | **自管机器栈**(Go-allocated `[]byte`),JIT 不写 Go 栈;Lua 值帧本就在 arena |
| 异步抢占 | wazero 用 context cancellation 协作终止(实测见 P3 [implementation-progress §0.1 认知修正 2](../p3-wasm-tier/implementation-progress.md))| **生成码回边插 preemptFlag 检查**;直线段长度有界 ⇒ 不可抢占窗口有界 |
| 栈移动 | Wasm 栈不在 Go 栈上,morestack 无关 | **JIT 不持任何 Go 栈指针**;jitContext 在 Go 堆(不移动) |
| 写屏障 | 值世界在 linear memory,wazero 无 Go 指针写 | **白赚**(承 P1 第一天承诺):值世界在 arena,JIT 写栈槽/表槽写自管内存 u64,Go GC 不可见 |

**P4 选自管 codegen 的本质 = 收回 wazero 的四项税外包,自付**,自己专注「翻译 + 投机 + OSR + 系统管线」。**wazero 转角色**:依赖 → 采石场(参考实现)——四件套(exec mmap / W^X / icache / trampoline)的每件都有 wazero 同款落地可参照,但代码独立写(承 [05 §2](./05-system-pipeline.md))。**P3 去留**在 P4 验收时用数据定([07 §5](./07-p3-retirement.md) 决策矩阵,缺省倾向退役)。

---

## 9. 不变式清单(实现与差分须守)

[01](./01-launch-judgment.md)~[08](./08-testing-strategy.md) 各篇分别承担,本节聚合呈现:

1. **快路径 + guard,失败 OSR exit,不出错**:投机验证失败**不是错误**,是回到全语义路径——guard 漏判(该查没查)直接静默错果,是 JIT 第一危险源,防线在 [08 §3](./08-testing-strategy.md)(承 [03 §3.5](./03-speculation-ic.md))。
2. **guard 物理 = 显式比较 + 条件跳,无信号陷阱**:纯 Go 下 SIGSEGV/SIGFPE 不可恢复,陷阱式 guard 此路不通([03 §3](./03-speculation-ic.md))。
3. **函数级 exit,栈槽真相,物化 = memmove**:OSR exit 单位是当前函数帧;每条字节码边界处 Lua 活值已物化在 arena 栈槽;NaN-box u64 跨 tier 零格式转换([04 §1 / §2 / §3](./04-osr-deopt.md))。
4. **JIT 码内不调用任何普通 Go 函数**:需要 Go 侧能力一律经 trampoline 出去,以「慢路径 helper」形式回 Go 世界执行,返回后重入([05 §4.3](./05-system-pipeline.md))。
5. **arena base 在两个 safepoint 之间稳定**:arena 搬迁(扩容)只发生在分配慢路径(= 出了 JIT 世界);JIT 内联 bump 越界即出去,回来从 jitContext 重载 base([05 §5](./05-system-pipeline.md))。
6. **混层调用走统一 CallInfo 协议**:JIT 函数 CALL 目标若有 JIT 码则同世界内直跳;若是解释 tier 则经 trampoline 出由 crescent 执行;协议同 P3,只换发射后端([05 §4 / §4.3](./05-system-pipeline.md))。
7. **共享骨架 + per-arch**:编译驱动 + guard/OSR 逻辑架构无关;每 opcode 发射函数按架构各一份;不写宏汇编([06 §1 / §2](./06-backends.md))。
8. **双架构 CI 双跑**:amd64 / arm64 物理 runner 各跑全套;交叉编译只验构建;Go 版本矩阵 1.25/1.26/tip([06 §5](./06-backends.md) + [08 §6](./08-testing-strategy.md))。
9. **P4 自管投机生命周期,P2 状态机不感知**:**P2 看 `TierGibbous` 即吸收态**;P4 内部 `p4SpecState[proto]` 子状态机自管(P4Speculative/P4Deoptimized/P4StuckSpeculation),OSR exit 后 P4 端清自身投机产物 + 重训练,P2 `tierState` 不变([03 §4.2 / §8](./03-speculation-ic.md) + [04 §5](./04-osr-deopt.md))。
10. **P3 去留结构共存零成本**:`internal/gibbous/{wasm,jit}` 并列,P2 `P3Compiler` 接口对两后端同形,策略数据定([07 §5.4](./07-p3-retirement.md))。
11. **解释器永不退役**:任何 Proto 始终保有可解释字节码(承 [../architecture](../architecture.md) §4 不变式 1);gibbous-jit 只是可选加速面,且 OSR exit 着陆点必为解释器([../../../llmdoc/must/design-premises](../../../llmdoc/must/design-premises.md) 原则 1)。
12. **luajc 档 = 列内核形状**:基准必须是列内核形状(一次 Call 进 VM 整批迭代,per-item 测不出 P4);承 [P1 12 §6.1](../p1-interpreter/12-testing-difftest.md) 硬约束([01 §4.2](./01-launch-judgment.md) + [08 §1](./08-testing-strategy.md))。

---

## 10. 风险与未决缺口汇总

各子文档 §风险节 + [../../../llmdoc/memory/doc-gaps](../../../llmdoc/memory/doc-gaps.md):

- **投机错果(JIT 第 1 危险源)**:guard 漏判静默产错果,有限用例测不出;主防线在 [08 §3 同 Proto 差分](./08-testing-strategy.md) + [08 §5 deopt 注入](./08-testing-strategy.md)(承 [03 §10.1](./03-speculation-ic.md))。
- **全显式 guard 密度天花板**:guard 成本若实测吃掉投机收益,需展开 guard 合并窥孔(同操作数直线段内只查一次,不引入 IR 的前提下可做)([03 §3.6](./03-speculation-ic.md) + [08 §11.2](./08-testing-strategy.md))。
- **arm64 维护矩阵**:双后端 + 双架构 CI 是长期固定成本;PJ8/PJ9 可滞后交付不阻塞 PJ7 单架构验收(发布口径如实标注)([06 §5.7](./06-backends.md) + [08 §11.5](./08-testing-strategy.md))。
- **人年级投入中途校验**:+1-2 人年是 P1-P3 总和级别投入;**PJ3 内部第二闸门**:amd64 + 仅算术投机的最小 P4 先打通全管线测 Horner 档位,若距 luajc 档仍远立即停下重评([01 §4.3](./01-launch-judgment.md) + [08 §1.6](./08-testing-strategy.md))。
- **locals 寄存器跨指令缓存**:纯「guard 即栈槽真相」vs 允许循环变量寄存器驻留 + 静态物化序列,**PJ7 定终稿,PJ11 调优可能展开**([04 §3.6 / §3.7](./04-osr-deopt.md) + [06 §4](./06-backends.md))。
- **arena base 重载协议实测**:跨 safepoint 稳定假设需 PJ3/PJ5 实测验证([05 §5](./05-system-pipeline.md))。
- **P3 去留 P4 验收时定**:[07](./07-p3-retirement.md) 给框架不给结论;缺省倾向退役但留翻案条件(真实宿主 iOS / 解释模式实测翻盘)。
- **真实宿主需求待外部确认**:首个目标宿主(规则引擎)的列内核形状是否真在协程外、真在主线程、真触及 luajc 档需求——P4 立项前置硬条件([01 §3.4](./01-launch-judgment.md) + [07 §10.2](./07-p3-retirement.md))。
- **OSR exit 着陆粒度终稿**:纯函数级 vs 允许局部缓存 + 静态物化序列,PJ7 amd64 原型实测后定([04 §3.6 / §3.7](./04-osr-deopt.md))。
- **编译执行的线程模型**:升层触发线程同步编译(模板编译微秒级)vs 后台 goroutine 编译 + 安装屏障,PJ0 实测定(开放问题)。
- **多 State 并发下 JIT 代码与 profile 的共享语义**:承 [../p2-bridge/00-overview §9](../p2-bridge/00-overview.md) 同款并发缺口,PJ7 验收期落地。

---

相关:
[01-launch-judgment](./01-launch-judgment.md)(启动闸门 + luajc 档锚点) ·
[02-template-direction](./02-template-direction.md)(方向裁决:JSC Baseline 风格) ·
[03-speculation-ic](./03-speculation-ic.md)(类型投机:IC 反馈消费 + f64 快路径 + guard) ·
[04-osr-deopt](./04-osr-deopt.md)(OSR exit 协议 + 物化 + 再训练) ·
[05-system-pipeline](./05-system-pipeline.md)(系统管线:四项税 + W^X + icache + trampoline) ·
[06-backends](./06-backends.md)(双后端:共享骨架 + per-arch + 双架构 CI) ·
[07-p3-retirement](./07-p3-retirement.md)(P3 去留决策框架) ·
[08-testing-strategy](./08-testing-strategy.md)(测试 + 验收 + 差分 + deopt 注入) ·
[implementation-progress](./implementation-progress.md)(进度对账 + 跨文档回填请求收口表) ·
[../p2-bridge/00-overview](../p2-bridge/00-overview.md)(P2 总览,P4 是其消费者) ·
[../p3-wasm-tier/00-overview](../p3-wasm-tier/00-overview.md)(P3 总览,P4 继承全部分层结构) ·
[../p5-trace-jit/00-overview.md](../p5-trace-jit/00-overview.md)(下一站:P4 收益不够时的开放式选项) ·
[../p1-interpreter/00-overview](../p1-interpreter/00-overview.md)(P1 总览,P4 共享值表示与内存模型) ·
[../p1-interpreter/02-bytecode-isa](../p1-interpreter/02-bytecode-isa.md)(源 ISA + §7 IC slot) ·
[../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md)(§1 CallInfo / §7 调用协议——OSR 着陆面) ·
[../p1-interpreter/12-testing-difftest](../p1-interpreter/12-testing-difftest.md)(§3.8 Runner 抽象 / §7 P4 行 / §8 CI 门禁) ·
[../roadmap](../roadmap.md)(§4 P4 定义 / §2 四项税 / §1 校准测量 / §7 prior art) ·
[../architecture](../architecture.md)(§1 包布局 `internal/gibbous/jit` / §4 三不变式) ·
[../../../llmdoc/architecture/evolution-roadmap](../../../llmdoc/architecture/evolution-roadmap.md)(tier 映射 + 坐标系警告) ·
[../../../llmdoc/must/design-premises](../../../llmdoc/must/design-premises.md)(四项税 / 值表示承诺 / 五原则) ·
[../../../llmdoc/guides/prove-the-path-under-test](../../../llmdoc/guides/prove-the-path-under-test.md)(投机/OSR/deopt 路径白盒命中) ·
[../../../llmdoc/guides/design-claims-vs-codebase-physics](../../../llmdoc/guides/design-claims-vs-codebase-physics.md)(主张须证据,P4 立项前重核 P3 现状) ·
[../../../llmdoc/guides/perf-optimization-workflow](../../../llmdoc/guides/perf-optimization-workflow.md)(§7 profile 才是合同——P4 PJ11 调优纪律) ·
[../../../llmdoc/memory/doc-gaps](../../../llmdoc/memory/doc-gaps.md)(P4 启动前置确认 / P4 落地时回填项)
