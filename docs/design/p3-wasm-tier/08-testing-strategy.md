# P3 §8:验收测试策略 —— 翻译正确口径总表 / 层间 byte-equal 差分 / 性能门 / GC 压力 / CI 门禁

> 状态:**设计阶段,详细设计已齐备**(依赖 P1/P2 落地 + 开工前置 spike 通过后 PW9 实装;凡涉 wazero API 与 build tag 落地处标注「P3 实装时定」)。本文是 [00-overview](./00-overview.md) §0 文档地图列出的 **P3 验收单一事实源**——P3 验收口径总表(正确性 + 性能 + 工程三轴)、crescent vs gibbous 逐字节差分(CI 门禁)、强制全量升层模式、GC 压力 fuzz 上 gibbous、性能基准(循环密集 ≥2x over P1)、坐标系警告、测试机制实装收口、测试入口暴露纪律。
> 上游种子:本文是 p3-wasm-tier.md 原稿 §7(层间差分)+ §8(验收与坐标系)的扩展;承 [00-overview](./00-overview.md) §4 PW9 验收(完成定义两轴)、§9 不变式、§10 缺口。
> 上游耦合面:[00-overview](./00-overview.md) §4 PW9(V1-V18 全过)、§4 末「PW9 verify 含两轴(正确性 byte-equal + 性能 ≥2x),任一不达标都不算 P3 交付完成」。
> 同主题对偶面:[../p2-bridge/06-testing-strategy](../p2-bridge/06-testing-strategy.md)(**P2 验收单一事实源,本文是同款「全 V 编号口径总表」形态对偶面**;P2 是「决策正确 + 退化等价」口径,P3 是「翻译正确 + 性能门」口径,**两者并行不替换**——见本文 §4.3 与 §6.6)。
> P1 依赖面:[../p1-interpreter/12-testing-difftest](../p1-interpreter/12-testing-difftest.md)(P1 差分测试矩阵 §3/§5 是 P3 接入的轨道;§10 验收口径总表对 gibbous 原样适用,**不为 gibbous 开新豁免**)。
> P3 内部收口面:[02-translation](./02-translation.md) ~ [07-coroutine-thread-rule](./07-coroutine-thread-rule.md)(本文是各文档验收口径的**总收口**——每篇文档承诺的不变式都在本文 §1 总表配可执行检查)。
> 上游原则面:[../roadmap.md](../roadmap.md) §4(P3 验收「循环密集 ≥2x over P1」)+ §5 原则 2(层间逐字节差分)。
> 坐标系警告源:[evolution-roadmap](../../../llmdoc/architecture/evolution-roadmap.md)(流水线图倍率 vs 阶段验收门槛**不在同一坐标系**——本文 §5.2 钉死)。
>
> **本文定位一句话**:**P3 验收是双轴硬门**——正确性轴(crescent vs gibbous 逐字节差分,主防线)+ 性能轴(循环密集 ≥2x over P1,价值兑现门)。任一不达标 P3 不算交付完成([00-overview](./00-overview.md) §4 PW9)。本文把这双轴 + 工程轴翻成 **V1-V18 可执行验收口径**,每条都对应一组测试,**不留无法验证的承诺**(承 P1 [12 §0](../p1-interpreter/12-testing-difftest.md) 「最终法庭」 + P2 [06 §0.3](../p2-bridge/06-testing-strategy.md) 「不留无法验证的承诺」铁律)。

对应目录:`test/difftest`(P3 接入层间差分,新增 `p3_test.go`)、`test/conformance`(P3 形状级单测,新增 `p3_test.go`)、`internal/gibbous/wasm`(翻译器内测)、`benchmarks/realworld`(P3 性能门实际脚本)。被测对象 `internal/gibbous/wasm`(翻译器 + wazero 执行)+ `internal/bridge`(P3Compiler 装载 + 强制全升模式测试入口)。

---

## 0. 定位:P3 验收双轴(正确性 + 性能,任一不达标不交付)

### 0.1 承 00-overview §4 的双轴完成定义

[00-overview](./00-overview.md) §4 PW9 验收一句话:**「P3 总验收:V1-V18 全过」**,并在 §4 末注明:**「PW9 verify 含两轴:正确性(byte-equal)与性能(≥2x);任一不达标都不算 P3 交付完成」**。本文把这两轴翻成具体口径:

1. **正确性轴**:crescent vs gibbous **逐字节差分**(本文 §1.1 V1-V13、§2 核心展开)。这是 P3 的主防线——**任何 crescent/gibbous 输出不一致都是阻断级 bug**(本文 §4.1)。
2. **性能轴**:循环密集脚本 **≥2x over P1**(本文 §1.2 V14-V16、§5 基准设计)。这是 P3 价值兑现门——P3 的战略价值不在倍率,在跑通分层机器([00-overview](./00-overview.md) §0),但**没有 ≥2x 就证明分层骨架没把 dispatch 消除的收益兑现出来**,价值落空。

外加一条工程轴(每条不变式都要可验证):

3. **工程轴**:三套 build tag 全 CI 通过(本文 §1.3 V17-V18)。P3 引入第三个 build tag `wangshu_p3`,与 P2 的 `wangshu_profile` 共存(本文 §6.4),三套 build(default / wangshu_profile / wangshu_profile+wangshu_p3)的零回归是 P3 阶段独立交付(roadmap §5 原则 3)的最强承诺。

### 0.2 与 P1/P2/P4 验收口径的根本不同

承 P2 [06 §0.1](../p2-bridge/06-testing-strategy.md) 的四阶段验收对照表,P3 的特异性:

| 阶段 | 性能验收 | 正确性验收 | 主防线 |
|---|---|---|---|
| P1 | ≥2x over gopher-lua 三档 + benchmark game 五项 + boundary mini([12 §6](../p1-interpreter/12-testing-difftest.md)) | 三方差分逐字节一致([12 §3](../p1-interpreter/12-testing-difftest.md)) | **byte-equal 差分 fuzz**(防 codegen / IC / GC 透明性偏差) |
| P2 | **❌ 无**(基建定位) | 决策正确([06](../p2-bridge/06-testing-strategy.md)) | 可编译性零误判注入 fuzz(防安全闸门失守) |
| **P3**(本文) | **循环密集 ≥2x over P1**(价值兑现门) | **crescent vs gibbous 层间差分逐字节一致** | **层间 byte-equal 差分 fuzz**(crescent 与 gibbous 同输入同输出) |
| P4 | 列内核 ≥ luajc 档 | 投机正确性(deopt 着陆点严格性) | 投机 + deopt 差分(P4 §测试) |

**P3 与 P1 的差分都是 byte-equal**,但**差分对象不同**:
- **P1 是「不同实现各跑各的字节码,只比最终输出」**(望舒解释器 vs gopher-lua vs 官方,三方各跑各的 Proto)——存在「实现本来就不同」的噪声,靠三方互校定位。
- **P3 是「同一份 Proto 走不同执行层」**(crescent 解释 vs gibbous Wasm 编译执行)——**同输入字节码,任何输出差异必是执行层(翻译)bug,不存在「实现本来就不同」的噪声**。这正是 P1 [12 §3.8](../p1-interpreter/12-testing-difftest.md) 预言「P3+ 接入的关键差异」与「更强的差分」的兑现。

**P3 与 P4 的差分面宽窄不同**(风险阶梯,本文 §4.3):**P3 是 try-compile 非投机**(快路径=语义分发,失败走助手得正确结果,零 deopt,[06-ic-feedback-consume §1](./06-ic-feedback-consume.md)),差分验的是**翻译正确性**;P4 引入投机 + deopt,差分要验**投机正确性 + 去优化着陆点严格性**(漏 guard 会静默错果)。P3 的验证面**比 P4 窄**——这是「先 P3 后 P4」的风险阶梯设计(本文 §4.3)。

### 0.3 「不留无法验证的承诺」铁律(承 P2 06 §0.3)

本文核心铁律(承 P1 [12 §0](../p1-interpreter/12-testing-difftest.md) 「最终法庭」 + P2 [06 §0.3](../p2-bridge/06-testing-strategy.md)):**P3 各子文档(02-07)的每条不变式都必须对应一组可执行测试**——若一条不变式只有「设计期应当」的措辞而无运行期验证,该不变式视为**未交付**。

本文 §1 总表是这条铁律的兑现:把散落在 P3 各子文档的不变式([02 §2.2/§2.4](./02-translation.md) memory-resident + pc 物化、[04 §1/§4](./04-trampoline.md) bit50 + status 链、[05 §3/§4](./05-safepoint-gc.md) safepoint + locals 写回、[06 §1](./06-ic-feedback-consume.md) IC 快照固化、[07](./07-coroutine-thread-rule.md) 线程级 tier、[00-overview §9](./00-overview.md) 九条不变式)集中在一处,每条配可执行检查路径。

### 0.4 与下游的接口(本文产出什么)

本文输出是 PW9 验收测试套清单(详见 §6)。`make all` 在 P3 实装后纳入的内容(承 [Makefile](../../../Makefile) `all: fmt lint test fuzz conformance difftest bench-test`):

**P3 退役协议预设位**(2026-06-28,承 [P4 implementation-progress §2 RJ-22](../p4-method-jit/implementation-progress.md) 跨文档回填请求 + [P4 07 P3 去留决议](../p4-method-jit/07-p3-retirement.md) §5.1 缺省倾向):若 P4 验收通过后决议 P3 退役,**V1-V18 编号保留,由 P4 接续承担**(P4 08 §2 V1-V18 接续 + V19-V22 增项)。本文 V1-V18 测试单测在 P3 退役后转为 P4 build 下不豁免的纪律承诺(承 [P4 08 §0.2](../p4-method-jit/08-testing-strategy.md) + [P4 08 §10 迁移协议](../p4-method-jit/08-testing-strategy.md))。**具体迁移协议见 P4 §8 §10**。

```
test/difftest/                     ← P1 差分套(P3 接入)
├── p3_test.go                     ← V1-V13 crescent vs gibbous 逐字节差分主体(本文 §2.6)
├── gcfuzz.go                      ← P1 GC 压力 fuzz(P3 加 gibbous 轴,本文 §3)
└── runner.go                      ← P1 已建 Runner 抽象;P3 加 WangshuGibbous runner(本文 §2.1)

test/conformance/
└── p3_test.go                     ← V1-V13 形状级单测(本文 §6.2)

internal/gibbous/wasm/             ← 翻译器内测(本文 §6.3)
├── compile_test.go                ← 翻译单位 / SupportsAllOpcodes 渐进白名单
├── emit_test.go                   ← opcode → WAT 发射黄金
├── memory_test.go                 ← arena 收养 wazero memory / grow 后 GCRef 不变
├── trampoline_test.go             ← 跨层入口 / status 链 / panic recover
└── helpers_test.go                ← imported 助手分派

benchmarks/realworld/              ← P3 性能门(本文 §5)
├── realworld_test.go              ← P1 已建;P3 加 gibbous 档对照
└── testdata/{fib,binarytrees,spectralnorm,nbody,fannkuch}.lua

.github/workflows/
└── nightly-diff-fuzz.yml          ← P1/P2 已建;P3 扩 gibbous 轴(本文 §4.2)
```

### 0.5 P3 测试金字塔(承 P1 12 §1,加层间轴)

承 P1 [12 §1](../p1-interpreter/12-testing-difftest.md) 测试金字塔,P3 在「差分 fuzz」宽腰**加一条层间轴**(crescent vs gibbous):

```
                    ┌──────────────────────────────────────┐
                    │   benchmarks/realworld (gibbous 档)    │  性能验收:loop ≥2x over P1,
                    │   (crescent vs gibbous 同机 A/B)       │  五脚本 ≥1.5x geomean(§5)
                    └──────────────────────────────────────┘
              ┌──────────────────────────────────────────────────┐
              │       test/difftest (P3 层间轴, fuzz)             │  主防线:同一 Proto 走两层,
              │  crescent(oracle) vs gibbous(被测, 强制全升)      │  逐字节一致(§2)
              │  + GC 压力 fuzz 上 gibbous(写回遗漏必现, §3)      │  CI 必过门禁(§4)
              │  + P1 三方差分仍跑(crescent vs gopher vs 官方)    │  (§1.5 不豁免)
              └──────────────────────────────────────────────────┘
        ┌────────────────────────────────────────────────────────────┐
        │            test/conformance/p3_test.go                       │  形状级正确性:每条 PW
        │  直线 op / 算术快慢路径 / for / 表 IC / CALL / 闭包 /        │  代表形状 crescent vs
        │  协程不升层 ... (V1-V13 形状级单测, §6.2)                     │  gibbous 逐字节(§6.2)
        └────────────────────────────────────────────────────────────┘
  ┌──────────────────────────────────────────────────────────────────────┐
  │       internal/gibbous/wasm/*_test.go(翻译器白盒内测)                │  地基:翻译单位 / WAT
  │  compile / emit / memory / trampoline / helpers(§6.3)                 │  发射黄金 / memory 收养
  │  + arena 收养 wazero memory grow 后 GCRef 不变 + -race(V18)          │  / 跨层 status 链(§6.3)
  └──────────────────────────────────────────────────────────────────────┘
```

金字塔与 P1 [12 §1](../p1-interpreter/12-testing-difftest.md) 同形(底层最宽、差分 fuzz 宽腰、基准在顶),P3 特异处:
- **差分 fuzz 宽腰加层间轴**:P1 的宽腰是「三方差分(不同实现各跑各的字节码)」,P3 在此**叠加层间轴**(crescent vs gibbous,同一份 Proto 走两层)。层间轴是 P3 的主防线(roadmap §5 原则 2 在 P3 第一次有「层间」含义)。
- **基准在顶但权重升级**:P1 基准是「≥2x over gopher-lua」(辅助验收),P3 基准是「≥2x over P1」(**双硬门之一**,§6.8)——不达标 P3 不交付,验收权重等同正确性。
- **底层翻译器内测是 P3 新增地基**:P1 没有「翻译器」,P3 的 `internal/gibbous/wasm` 白盒内测(§6.3)是新增的最底层(翻译单位、WAT 发射黄金、memory 收养),它在「运行期差分」之前就提前暴露翻译/发射偏差。

> **职责不重叠纪律**(承 P1 [12 §1](../p1-interpreter/12-testing-difftest.md) 末):同一个翻译 bug 应优先被**更低层、更快、更可定位**的测试抓住——翻译器内测(emit 黄金)能抓「某 opcode 发射的 WAT 错了」,不必等到层间差分跑出输出错才发现。层间差分 fuzz 撞出的翻译 bug,最小化后**优先固化为翻译器内测或 conformance 形状级用例**(§2.6),让回归在更低层兜住。

---

## 1. P3 验收口径总表(本文最有价值的产出 —— 逐条收口)

下表对应 P2 [06 §1 验收口径总表](../p2-bridge/06-testing-strategy.md)与 P1 [12 §10](../p1-interpreter/12-testing-difftest.md)的形态——把 P3 全部不变式拉成「轴 / 断言 / 单测对应 / 差分 fuzz / 自动化检查」五栏,逐条钉死。**这张表是本文存在的核心理由**:它把散落在 P3 各子文档(02-07)的验收承诺集中在一处,每条配可执行检查路径。

### 1.1 正确性轴(V1-V13)—— 主防线,crescent vs gibbous 逐字节

| # | 类目 | 断言(P3 不变式) | 单测对应 | 差分 fuzz | 自动化检查 |
|---|---|---|---|---|---|
| **V1** | PW2 直线 opcode | 5 条直线 opcode(MOVE/LOADK/LOADBOOL/LOADNIL/JMP)的 Proto 升 gibbous 后,可观察输出与 crescent 解释**逐字节一致** | `conformance/p3_test.go::TestLinearOpcodeByteEqual`(table-driven 5 形状) | `difftest/p3_test.go` 强制全升下跑含直线 op 的随机脚本 | `bytes.Equal(crescentOut, gibbousOut)` 必为 true([02 §2.3](./02-translation.md) MOVE 示例) |
| **V2** | PW3 算术快路径 | 双 number 算术(ADD/SUB/MUL/DIV/MOD/POW/UNM)直发 f64 指令 + NaN 规范化([01 §3.4](../p1-interpreter/01-value-object-model.md) canonicalizeNaN)与解释器**逐字节一致**(含 `-0.0`/`inf`/`0/0`) | `conformance/p3_test.go::TestArithFastPathByteEqual`(笛卡尔积浮点边界 × op) | `difftest/p3_test.go` 算术密集脚本 | crescent vs gibbous byte-equal;NaN 位型固定 `0x7FF8…`([02 §2.3](./02-translation.md) ADD 示例) |
| **V3** | PW3 算术慢路径 | 算术慢路径(string coercion / `__add` 等元方法)走 imported 助手(`$h_arith`)仍 **byte-equal**;status 链错误位置(pc 物化)逐字节一致 | `conformance/p3_test.go::TestArithSlowPathByteEqual`(混合类型 + 元表) | `difftest/p3_test.go` 混合类型算术 | 慢路径助手回 Go 得正确结果([02 §2.3](./02-translation.md) else 分支 `$h_arith`) |
| **V4** | PW4 数值 for | 数值 for 循环(FORPREP/FORLOOP)编译后跑结果 **byte-equal**(含 step<0 反向、step=0 错误形态、limit 非 number 错误) | `conformance/p3_test.go::TestNumForByteEqual`(正/反向 + 边界 step) | `difftest/p3_test.go` for 循环脚本 | crescent vs gibbous byte-equal([02 §2.3](./02-translation.md) FORLOOP 示例) |
| **V5** | PW4 回边 GC | 回边 safepoint(`gcPending` 检查)触发 GC 时 **byte-equal**(GC 压力模式:每分配即 full GC) | `difftest/gcfuzz.go::TestGCStressGibbous`(本文 §3.2) | GC 压力 fuzz 上 gibbous | 高频 GC 下 gibbous 输出 == 正常 pacing gibbous 输出([05 §3](./05-safepoint-gc.md)) |
| **V6** | PW5 表 IC 命中 | 表 IC 单态命中(同表同代次)**跳过哈希查找**,与解释器 **byte-equal** | `conformance/p3_test.go::TestTableICHitByteEqual`(单态表访问) | `difftest/p3_test.go` 表访问脚本 | gibbous 快路径直达槽 vs crescent 解释 byte-equal([02 §2.3](./02-translation.md) GETTABLE 示例 then 分支) |
| **V7** | PW5 表 IC 失效 | 表 IC 失效(gen bump)走助手(`$h_gettable`)仍 **byte-equal**(含 `setmetatable` 触发的失效) | `conformance/p3_test.go::TestTableICMissByteEqual`(gen bump + setmetatable) | `difftest/p3_test.go` 表形状变化脚本 | 快照永久 miss 后每次走助手仍正确([06 §1](./06-ic-feedback-consume.md) 失效自然降级) |
| **V8** | PW6 跨层 CALL 链 | 跨层调用链(crescent → gibbous → crescent → host → ...)错误冒泡 **byte-equal**(错误穿越 gibbous 帧) | `conformance/p3_test.go::TestCrossTierCallByteEqual`(深嵌套混合层) | `difftest/p3_test.go` 多层调用脚本 | status 链冒泡([04 §3/§4](./04-trampoline.md))与 crescent 显式错误返回同构 byte-equal |
| **V9** | PW6 traceback | gibbous 帧 traceback(经 imported 助手取本帧 pc)与解释器**逐字节一致**(`chunk:line:` 前缀 + `(...tail calls...)`) | `conformance/p3_test.go::TestGibbousTracebackByteEqual` | `difftest/p3_test.go` error/pcall 脚本 | gibbous 帧错误位置 == crescent([02 §2.4](./02-translation.md) pc 物化,不开豁免) |
| **V10** | PW7 闭包 upvalue | 闭包构造(CLOSURE)+ 开放/关闭 upvalue(CLOSE)与解释器 **byte-equal** | `conformance/p3_test.go::TestClosureUpvalByteEqual`(开放/关闭/共享 upval) | `difftest/p3_test.go` 闭包脚本 | gibbous 闭包语义 == crescent([02 §3.5](./02-translation.md)) |
| **V11** | PW8 协程不升层 | 协程内代码**永远不升层**(TierState 恒 TierInterp 或 TierStuck);协程内同 Proto 即便 hot+Compilable 也不进 gibbous | `conformance/p3_test.go::TestCoroutineNoPromote`(协程内 hot Proto 断言 tier) | — | 协程线程上 `tierState != TierGibbous`([07](./07-coroutine-thread-rule.md) 线程级 tier 规则) |
| **V12** | 强制全升 byte-equal | 强制全升模式下,所有 `CompCompilable` Proto 走 gibbous,结果与解释器 **byte-equal**(消除热度时序不确定性) | `difftest/p3_test.go::TestForceAllPromoteByteEqual`(差分套全脚本) | **核心 fuzz**:强制全升 + 语法制导生成(本文 §2.2) | crescent-only vs force-all-gibbous 全脚本 byte-equal(本文 §2.6) |
| **V13** | GC 压力逼写回遗漏 | GC 压力模式跑 gibbous,逼出 locals 写回遗漏 / 助手内根登记缺漏(若 [PW9](./00-overview.md) 启用 [§2.2B](./02-translation.md) locals 缓存优化) | `difftest/gcfuzz.go::TestGCStressGibbous` 子测(locals 缓存开关 A/B 对照) | GC 压力 fuzz 上 gibbous | 漏写回必现为脏值/误回收([05 §4](./05-safepoint-gc.md) 写回纪律的兜捕) |

### 1.2 性能轴(V14-V16)—— 价值兑现门,以 P1 为基线

| # | 类目 | 断言(P3 性能门) | 单测对应 | 基准矩阵 | 自动化检查 |
|---|---|---|---|---|---|
| **V14** | 循环密集 ≥2x over P1 | 循环密集脚本(loop 档)在 [PW9](./00-overview.md) 实测 **≥2x over P1**(**以 P1 crescent 为基线,不是 gopher-lua**) | `benchmarks/realworld` loop 档 gibbous vs crescent 同机 A/B | realworld loop 档 | `ns_op(crescent) / ns_op(gibbous) >= 2.0`(本文 §5.1) |
| **V15** | realworld 五脚本整体加速 | realworld 五脚本(fib / binary-trees / spectral-norm / nbody / fannkuch)整体加速曲线 **≥1.5x**(部分脚本短板可接受) | `benchmarks/realworld/realworld_test.go` 五脚本 gibbous 档 | realworld 五脚本 | 几何均值 `geomean(speedup) >= 1.5`(本文 §5.4) |
| **V16** | 边界往返无退化 | 边界往返(空 Proto 单次调用)**≥ wazero call boundary spike 实测值的 95%**(无意外退化) | `benchmarks/realworld` boundary 档 gibbous | boundary 档(承 [01 §1.2](./01-spike-gate.md) S1/S2) | `boundary_actual <= spike_S2 / 0.95`(本文 §5.5) |

### 1.3 工程轴(V17-V18)—— 阶段独立交付

| # | 类目 | 断言(工程门) | 单测对应 | 检查方法 | 自动化检查 |
|---|---|---|---|---|---|
| **V17** | 三套 build tag 零回归 | `make all` 在双 build tag(default / wangshu_profile)+ P3 实装的 `wangshu_p3` 下**零回归**(P1 差分套不豁免,P2 V1-V22 不豁免) | `make all` × 三 build 组合 | `go build`/`go test` × {default, wangshu_profile, wangshu_profile+wangshu_p3} | 三套 build 全绿(本文 §6.4) |
| **V18** | -race 通过 | `-race` 通过(多 State 并发跑 gibbous;wazero Runtime 跨 State 共享或独立的并发约束) | `difftest/p3_test.go::TestGibbousRace`(`-race -count=10`) | `go test -race` | 多 State 并发 gibbous 无 race(本文 §6.3) |

### 1.4 收口统计

本表收口 **18 条**口径,覆盖三轴:

- **V1-V13(13 条)**:正确性轴,P3 主防线(对应 [02](./02-translation.md)~[07](./07-coroutine-thread-rule.md) 各 PW2-PW8 + 强制全升 + GC 压力)
- **V14-V16(3 条)**:性能轴,价值兑现门(对应 [00-overview §4](./00-overview.md) PW9 性能门 + roadmap §4)
- **V17-V18(2 条)**:工程轴,阶段独立交付(对应 build tag + -race)

每条 PW(P-Wasm 里程碑,[00-overview §4](./00-overview.md))与本表口径的对应:

| PW | 内容 | 本表口径 |
|---|---|---|
| PW2 | 5 条直线 opcode + trampoline 入口 | V1 |
| PW3 | 算术 + 比较 + NaN 规范化 + 慢路径助手 | V2 / V3 |
| PW4 | 控制流 + 回边 safepoint | V4 / V5 |
| PW5 | 表 IC opcode + feedback 消费 + 失效降级 | V6 / V7 |
| PW6 | CALL 系列 + 跨层互调 + status 链 | V8 / V9 |
| PW7 | CLOSURE + upvalue 编译 | V10 |
| PW8 | 线程级 tier 规则 | V11 |
| **PW9** | 端到端验收 + 测试套 | **V12-V18 全过 + V1-V11 回归全过** |

### 1.5 与 P1/P2 验收口径总表的关系

| 维度 | P1 [12 §10] | P2 [06 §1] | 本文 §1 |
|---|---|---|---|
| 收口对象 | 各种现象的口径决策(26 条) | 各条不变式的检查路径(V1-V22) | 各条不变式的检查路径(V1-V18) |
| 主防线 | byte-equal 三方差分(防投机错果) | 可编译性零误判注入 fuzz(防安全闸门失守) | **层间 byte-equal 差分**(crescent vs gibbous,防翻译错果) |
| 表项含义 | 「这种情况要 X」 | 「这条断言由 Y 测试覆盖」 | 「这条断言由 Y 测试覆盖」 |
| build tag | 单一 build | 双 build(`!profile` / `profile`) | **三 build**(default / wangshu_profile / +wangshu_p3) |

**三套并行不替换**(关键纪律,承 P2 [06 §9.1](../p2-bridge/06-testing-strategy.md)):P3 上线后——
- **P1 [12 §10] 全部 26 条口径仍要在 `wangshu_p3` build 下全部跑过**(差分套不因 P3 引入而豁免);
- **P2 [06 §1] 全部 V1-V22 仍要在 `wangshu_p3` build 下全部跑过**(P2 决策正确性不因 P3 落地而豁免);
- **本文 §1 的 V1-V18 是 P3 新增**,只在 `wangshu_p3` build 下被检查。

详见 §4.3 与 §6.6。

---

## 2. crescent vs gibbous 逐字节差分(本节核心)

> p3-wasm-tier.md 原稿 §7:**「roadmap §5 原则 2 在 P3 第一次有了『层间』含义」**。本节是这条的工程化——把「同一 Proto 走两层、逐字节比对」落成可运行的 harness、可入 CI 的门禁。

### 2.1 接入 P1 12 的 tier 矩阵:加一个 gibbous runner

P1 [12 §3.8](../p1-interpreter/12-testing-difftest.md) 早已预留 `Runner` 抽象,并明确「P3+ 只**新增 runner**接入同一比对框架」。P3 兑现:

```go
// test/difftest/runner.go —— P1 已建抽象,P3 加实现
type Runner interface {
    Name() string
    Run(src string, arena []byte) (capture, error)   // 跑脚本,返回 O1..O5(12 §3.1 五项可观察输出)
}

// P1 的三个 runner:
type WangshuInterp struct{}   // 被测:internal/crescent(纯解释)
type GopherLua struct{}        // 基准:github.com/yuin/gopher-lua
type OfficialLua struct{}      // oracle:exec lua5.1 子进程(或 golden 文件)

// P3 新增(本文落地):
type WangshuGibbous struct{    // 同一份 Proto 走 gibbous Wasm 层
    forceAllPromote bool        // 强制全升模式(§2.2)
}
func (WangshuGibbous) Name() string { return "wangshu-gibbous" }
func (g WangshuGibbous) Run(src string, arena []byte) (capture, error) {
    // 1. wangshu.NewState(opts) 在 wangshu_p3 build 下自动注入真 P3(§6.6)
    // 2. bridge.SetForceAllPromote(state, true)(§2.2 / §7.2)
    // 3. 跑脚本,捕获 O1..O5
}
```

**P1 三方差分与 P3 层间差分的关键差异**(承 P1 [12 §3.8](../p1-interpreter/12-testing-difftest.md)):
- P1 三方:「**不同实现各跑各的字节码**,只比最终输出」——存在「实现本来就不同」的噪声,P1 [12 §4](../p1-interpreter/12-testing-difftest.md) 用大量豁免(地址脱敏 / random / GC 数值 / locale / 措辞)处理。
- P3 层间:「**同一份 Proto**(同一份望舒 codegen 产物)走 crescent 解释 vs gibbous Wasm 编译执行」——**两层共享同一份 Proto、同一份 arena 值世界([03 §1](./03-memory-model.md))、同一套 NaN-box 编码([01 §3](../p1-interpreter/01-value-object-model.md))**。任何输出差异必是**翻译 bug**,不存在「实现本来就不同」的噪声。

**这意味着 crescent vs gibbous 差分比 P1 三方差分豁免面更窄**:地址脱敏(N1)仍需要(arena 收养 wazero memory 后偏移可能因 grow 时机不同而不同?——**不**,§2.4 论证 gibbous 不引入新的不确定性源);random/GC 数值/locale 仍按 P1 [12 §4](../p1-interpreter/12-testing-difftest.md) 豁免。**但 P1 §4 的口径总表对 gibbous 原样适用,不为 gibbous 开任何新豁免**(§2.4)。

#### 2.1.1 层间差分的判定矩阵(二方,crescent 为 oracle)

P1 [12 §3.3](../p1-interpreter/12-testing-difftest.md) 的三方判定矩阵(望舒/gopher/官方互校)在 P3 层间轴**退化为二方**——因为 crescent 已被 P1 三方验证过对齐官方,P3 层间只需 crescent(oracle)vs gibbous(被测):

| crescent(oracle) vs gibbous(被测) | 结论 |
|---|---|
| == | **通过**(翻译正确) |
| ≠ | **gibbous 翻译 bug**(crescent 是 P1 已验证 oracle,gibbous 孤立错——确定性信号,无实现噪声) |

**为什么二方就够**(不需要在 P3 重跑三方):P1 三方矩阵的价值在「定位谁错」——三个独立实现互校,孤立错的那个是 bug。但 P3 层间是**同一份 Proto 走两层**,crescent 的正确性是 P1 交付的前提(不是待验证项)。所以 P3 层间不存在「谁错」的歧义——**只要 gibbous ≠ crescent,必是 gibbous 翻译 bug**(crescent 不可能因为「换了执行层」就错,它根本没换)。这是 §2.1 「无实现噪声」的直接推论,也是 P3 差分比 P1 三方差分**判据更锐利**的原因。

> **P1 三方仍跑,但不在层间轴**:P3 build(`wangshu_p3`)下,P1 三方差分(crescent vs gopher vs 官方)**仍然全跑**(§1.5 不豁免)——它验「crescent 在 P3 build 下仍对齐官方」(防 P3 引入意外改了 crescent 行为)。层间轴(crescent vs gibbous)是**新增的第四方比对**,与 P1 三方并行(承 P2 [06 §9.3](../p2-bridge/06-testing-strategy.md) 「差分套分层扩展」形态)。

#### 2.1.2 可观察输出(O1-O5)在层间的特化

P1 [12 §3.1](../p1-interpreter/12-testing-difftest.md) 定的五项可观察输出(O1 print 字节流 / O2 顶层返回值 / O3 错误信息 / O4 副作用顺序 / O5 退出状态)在层间差分**全部适用**,且有 gibbous 特有的关注点:

| 可观察项 | 层间特有关注 | 对应 V 口径 |
|---|---|---|
| O1 print/io.write 字节流 | gibbous 的 print 经 imported 助手回 Go 调同一份 stdout sink——字节流必逐字节同 crescent | V1-V13 通用 |
| O2 顶层返回值 | gibbous RETURN 助手按 nresults 回填寄存器([04 §2](./04-trampoline.md)),返回值序列必同 crescent | V8(CALL/RETURN) |
| O3 错误信息(值+位置+traceback) | **gibbous 帧 pc 物化**([02 §2.4](./02-translation.md))——错误位置 `chunk:line:` 必同 crescent;traceback 经助手取本帧 pc | **V9**(traceback 主靶) |
| O4 副作用顺序 | gibbous 直线代码的副作用(print/表写)顺序必同 crescent 解释序——编译不重排可观察副作用 | V1-V13 通用 |
| O5 退出状态 | gibbous 错误冒泡到顶层的退出态必同 crescent(status 链冒泡,[04 §4](./04-trampoline.md)) | V8 |

**O4 副作用顺序为什么是层间差分的关键关注**:gibbous 是编译产物,理论上编译器可能重排指令(优化)。但 P3 基线**不做任何重排可观察副作用的优化**——gibbous 的每条 opcode 翻译是「逐 opcode 直译」([02 §2.3](./02-translation.md)),副作用顺序与字节码顺序严格一致,因此与 crescent 解释序逐字节同。**若未来 PW9 后引入指令调度优化**,O4 是第一道防线(重排了可观察副作用必被层间差分抓住)。

### 2.2 强制全量升层模式:消除热度时序不确定性

p3-wasm-tier.md 原稿 §7 第二点:**「difftest 跑 gibbous 侧时绕过热度阈值(所有 CompCompilable 的 Proto 直接编译)」**。

**为什么必须强制全升**:正常运行下,P2 决策机([../p2-bridge/01-profiling](../p2-bridge/01-profiling.md))按热度阈值(`HotBackEdgeThreshold=1000`)决定哪些 Proto 升层。这引入**时序不确定性**——同一脚本跑两次,因为调度 / GC 时机不同,被编译的 Proto 集合可能不同(尤其短脚本可能一个都没升够阈值)。差分若依赖「自然热度」,会出现「这次跑 Proto A 升了 gibbous、那次没升」的不可复现,差分结果失去意义。

**强制全升模式**绕过热度判定:gibbous runner 下,所有 `CompCompilable` 的 Proto **在首次调用前直接编译**(`SupportsAllOpcodes(proto)==true` 即编译,不看 backEdge 计数)。这保证:
- **可复现**:同一脚本每次跑,被编译的 Proto 集合完全确定(=全部 Compilable Proto)。
- **覆盖最大化**:差分覆盖到所有 Compilable Proto 的翻译,而非只覆盖恰好够热的那些。
- **最坏情况验证**:即便某 Proto 在真实负载下永远不够热(永不会升层),强制全升也会编译它并差分,提前暴露翻译 bug。

测试入口(本文 §7.2 列为 P3 新增):

```go
// internal/bridge —— 强制全升模式测试入口(P3 落地时实装,testing-only)
// bridge.SetForceAllPromote(state, true):此 State 上 considerPromotion 绕过热度阈值,
//   所有 CompCompilable Proto 在首次 doCall 前直接 P3Compiler.Compile + installGibbous。
func SetForceAllPromote(state *State, on bool)
```

> **与 P2 04 升层日志的交互**(本文 §9 缺口):强制全升模式下每个 Proto 都打 `function promoted to gibbous` 日志([../p2-bridge/04-try-compile-fallback §6](../p2-bridge/04-try-compile-fallback.md)),会刷屏。**限流策略留 [PW9](./00-overview.md) 实测后定**(候选:强制全升模式下抑制单 Proto 日志,只打总数)。

### 2.3 消除「热度时序导致哪些函数被编译」的不确定性

强制全升模式把差分的不确定性源砍到只剩「翻译是否正确」一个维度。展开论证为什么这是充分的:

正常运行下,gibbous 执行结果取决于三个变量:① 哪些 Proto 被编译(热度时序)② 编译产物是否正确(翻译)③ 运行期 IC 快照固化时刻(编译时刻的表形状,[06 §1](./06-ic-feedback-consume.md))。

- **①被强制全升消除**:全部 Compilable Proto 都编译,集合确定。
- **③在强制全升下仍有微妙性**:IC 快照固化在「编译时刻」,强制全升把编译时刻提前到「首次调用前」——此时 IC slot 可能还没被运行期填充(mono IC 重填发生在解释执行时,[05 §6.3](../p1-interpreter/05-interpreter-loop.md))。**这正是差分要验的**:gibbous 在「IC 快照为空/陈旧」时走助手完整查找(`$h_gettable`),与 crescent 解释结果仍必须 byte-equal(V7)。强制全升把这条慢路径覆盖率拉满(很多 Proto 编译时 IC 还没热,固化的是空快照),反而**强化**了「失效降级走助手」路径的差分覆盖。
- **②是唯一剩下的 bug 源**:翻译正确性。这就是 P3 差分的全部靶标。

> **结论**:强制全升不仅保证可复现,还**意外强化**了 IC 失效降级路径(V7)和慢路径助手(V3)的覆盖——因为编译时刻提前导致大量 Proto 固化空/陈旧快照,跑时大量走助手。这是强制全升模式的隐性红利。

#### 2.3.1 强制全升与 P2 可编译性闸门的交互(不可达路径验证)

强制全升模式**绕过热度阈值,但不绕过 P2 可编译性闸门**(F1-F7,[../p2-bridge/03](../p2-bridge/03-compilability-analysis.md))。这是关键边界——强制全升不是「强制编译一切」,而是「强制编译一切**可编译的**」:

- **F1-F7 排除的形状仍走 crescent**:vararg(F1)、coroutine(F2)、debug(F3)、setfenv(F4)、过大(F5)、深嵌套(F6)、后端不支持 opcode(F7)的 Proto,即便强制全升也**不编译**(`SupportsAllOpcodes` 返 false 或 P2 判 `CompNotCompilable`)。强制全升只把「热度」这一维强制拉满,可编译性维度不变。
- **PW7 的「不可达路径验证」**(承 [00-overview §4](./00-overview.md) PW7 验收「vararg 函数已在 P2 F1 拦下,本步只验『不可达路径不被走到』」):VARARG opcode 的翻译路径在 gibbous 里**永远走不到**(含 VARARG 的 Proto 被 F1 拦在编译前)。强制全升下验证的是「这条不可达路径确实不被走到」——即强制全升跑含 vararg 的脚本时,该 Proto **仍是 crescent 执行**(tier 断言 != Gibbous),gibbous 翻译器从未收到含 VARARG 的 Proto。

```go
// V11 邻接:强制全升下,F1-F7 排除形状仍走 crescent(不可达路径验证)
func TestForceAllPromoteRespectsCompilability(t *testing.T) {
    cases := []struct{ name, src string }{
        {"vararg-F1",    `local function f(...) return select("#", ...) end; for i=1,2000 do f(1,2) end`},
        {"coroutine-F2", `local function f() coroutine.yield() end; ...`},
        // ... F3-F7 各形状 ...
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            state := wangshu.NewState(nil)
            bridge.SetForceAllPromote(state, true)        // 强制全升仍尊重可编译性闸门
            prog, _ := wangshu.Compile([]byte(tc.src), tc.name)
            _ = prog.Call(state, nil)
            proto := prog.HotProto()
            pd := bridge.ProfileDataOf(state, proto)
            // 强制全升下,F1-F7 排除形状的 tier 仍非 Gibbous(走 crescent)
            if pd.TierState == bridge.TierGibbous {
                t.Fatalf("%s: F-excluded proto wrongly promoted under force-all", tc.name)
            }
        })
    }
}
```

> **为什么这条边界重要**:若强制全升错误地「强制编译一切」(连 F1-F7 排除的也编译),会把「翻译器收到不该收到的 Proto」当成正常——掩盖「P2 闸门失守 → 翻译器拿到 vararg Proto → 翻译出错误代码」这类灾难(P2 [06 §2.1](../p2-bridge/06-testing-strategy.md) 主防线对应物)。强制全升**只强制热度、不强制可编译性**,保证 gibbous 翻译器的输入集合在测试与生产下完全一致(都是 Compilable Proto),差分覆盖的是真实翻译路径而非人造的不可达路径。

### 2.4 P1 12 §10 验收口径总表原样适用,不为 gibbous 开新豁免

p3-wasm-tier.md 原稿 §7 第一点末:**「[12](../p1-interpreter/12-testing-difftest.md) 的口径总表原样适用,不为 gibbous 开新豁免」**。这是 P3 差分的铁律。

P1 [12 §4](../p1-interpreter/12-testing-difftest.md) 定的全部豁免(地址脱敏 N1 / random 禁用 N2 / GC 数值脱敏 N3 / locale 脱敏 N4 / `pairs` 序混合口径 / `%.14g` 严格 / 措辞三级口径)——**对 gibbous 一字不改地适用**。具体地:

| P1 §4 豁免项 | gibbous 是否需要新豁免 | 理由 |
|---|---|---|
| N1 地址脱敏(`table: 0xADDR`) | **不需要新豁免** | gibbous 不引入新的对象身份——`tostring(t)` 走 imported 助手回 Go 取 arena 偏移,与 crescent 同一份偏移([03 §1](./03-memory-model.md) arena=wazero memory)。两层地址脱敏后结构一致 |
| N2 random 禁用 | **不需要** | gibbous 不实现 random(NEWTABLE/math.random 全经助手回 Go,[05 §1](./05-safepoint-gc.md));生成器禁产 random 对两层同样生效 |
| N3 GC 数值脱敏 | **不需要** | `collectgarbage("count")` 经助手回 Go,两层同一份 arena 内存模型 |
| N4 locale 脱敏 | **不需要** | os.date 经助手回 Go,两层同一份实现 |
| `pairs` 序混合口径 | **不需要** | gibbous 不重排表——GETTABLE/next 经助手或固化快照,node 布局是 arena 里同一份([03 §1](./03-memory-model.md)),遍历序两层逐字节一致 |
| `%.14g` 严格 | **不需要** | gibbous 算术产 f64 后,tostring/CONCAT 经助手回 Go 调同一份 `%.14g` 实现 |
| 措辞三级口径 | **不需要** | gibbous 错误经助手写 `CallInfo.savedPC`(pc 物化,[02 §2.4](./02-translation.md)),错误措辞由同一份 crescent 错误模板生成 |

**核心论证**:gibbous **不引入任何私有值表示**([00-overview §9](./00-overview.md) 不变式 2)、**不自己分配**([05 §3](./05-safepoint-gc.md) 分配全经助手)、**不自己格式化**(tostring/format 全经助手回 Go)。所有「可能产生不确定性的操作」(分配、格式化、随机、地址)都**经 imported 助手回到 crescent 的同一份实现**——gibbous 只做「直线计算 + 共见栈槽读写」。因此 gibbous 的可观察输出**不可能引入 crescent 没有的不确定性源**,P1 §4 豁免表对它原样适用,**任何「想为 gibbous 加新豁免」的提案都要回到本文重新论证**(承 P1 [12 §4.8](../p1-interpreter/12-testing-difftest.md) 「严格口径项绝不豁免」纪律)。

### 2.5 差分 fuzz 接 gibbous:从 P1 500 种子门禁扩到双轴

P1 [12 §3](../p1-interpreter/12-testing-difftest.md) 的差分 fuzz(语法制导生成 §3.7 + 三方比对 §3.3)在 P3 扩展为 **crescent vs gibbous 双轴**:

```
P1 差分 fuzz(三方,各跑各的字节码):
  望舒 crescent  vs  gopher-lua  vs  官方 lua5.1
       ↑ 被测           ↑ 基准         ↑ oracle

P3 扩展(加层间轴,同一份 Proto 走两层):
  望舒 crescent  vs  望舒 gibbous(强制全升)
       ↑ oracle          ↑ 被测
   （crescent 此时是 oracle——它是 P1 已验证过的解释器）
  + 望舒 crescent vs gopher-lua vs 官方（P1 三方仍跑,不豁免）
```

**关键设计**:P3 层间轴里 **crescent 是 oracle**(它已被 P1 三方差分验证过对齐官方),gibbous 是被测。这让 P3 差分不必再三方比对——只需「gibbous vs crescent byte-equal」,因为 crescent 的正确性是 P1 已交付的前提。

fuzz 预算(承 P1 [12 §11](../p1-interpreter/12-testing-difftest.md) 「PR 门禁固定时长 + nightly 长跑」分工 + 扩展):
- **PR 门禁**:`make difftest` 一轮固定时长(`-fuzztime`,P1 已建,P3 加 gibbous runner);PR 门禁种子集([12 §11](../p1-interpreter/12-testing-difftest.md) 固定时长一轮)在 P3 扩为 crescent vs gibbous 双轴(每个种子既跑 P1 三方,也跑层间)。
- **nightly**:P1/P2 已建的独立长跑任务(`.github/workflows/nightly-diff-fuzz.yml`,承 [12 §11](../p1-interpreter/12-testing-difftest.md) 「持续 fuzz 由 nightly / 专用 fuzz 机承担」)在 P3 扩到层间轴(本文 §4.2)。
- **生成器复用**:P1 [12 §3.7](../p1-interpreter/12-testing-difftest.md) 的语法制导生成器(`test/difftest/gen/grammar.go`)产出的脚本对两层通用——**但生成器需偏向「产生 Compilable Proto」**(避开 P2 F1-F7 排除的 vararg / coroutine / debug 等,否则强制全升下该 Proto 走不到 gibbous,差分退化为 crescent vs crescent 无意义)。这是 P3 对生成器的回填请求(本文 §7.2)。

### 2.6 实装骨架:test/difftest/p3_test.go

仿 P2 [06 §3.X](../p2-bridge/06-testing-strategy.md) 的实装形态,给出 `test/difftest/p3_test.go` 骨架:

```go
//go:build wangshu_p3

// test/difftest/p3_test.go —— crescent vs gibbous 逐字节差分主体(V1-V13)
package difftest_test

import (
    "bytes"
    "testing"
    "github.com/<...>/wangshu"
    "github.com/<...>/wangshu/internal/bridge"
)

// TestForceAllPromoteByteEqual —— V12 核心:强制全升下,差分套全脚本 crescent vs gibbous byte-equal
func TestForceAllPromoteByteEqual(t *testing.T) {
    // 复用 P1 差分套脚本资源(承 12 §6:三档 + benchmark game 五项 + boundary mini)
    // + realworld 五脚本(本文 §5.4)
    scripts := loadAllDifftestScripts()
    for _, sc := range scripts {
        t.Run(sc.Name, func(t *testing.T) {
            outCrescent := runCrescentOnly(sc.Source)          // oracle:纯解释(P1 已验证)
            outGibbous  := runForceAllGibbous(sc.Source)        // 被测:强制全升
            // P1 §4 normalize 管线原样适用(地址脱敏等,§2.4 不开新豁免)
            outCrescent = normalize(outCrescent, sc.normOpts)
            outGibbous  = normalize(outGibbous,  sc.normOpts)
            if !bytes.Equal(outCrescent, outGibbous) {
                t.Fatalf("LAYER byte mismatch in %s:\nlen(crescent)=%d len(gibbous)=%d\nfirst diff at %d:\n  crescent: %q\n  gibbous:  %q",
                    sc.Name, len(outCrescent), len(outGibbous), firstDiff(outCrescent, outGibbous),
                    snippetAround(outCrescent, firstDiff(outCrescent, outGibbous)),
                    snippetAround(outGibbous,  firstDiff(outCrescent, outGibbous)))
            }
        })
    }
}

// runForceAllGibbous:强制全升 + 跑脚本,捕获可观察输出(O1..O5)
func runForceAllGibbous(src []byte) []byte {
    state := wangshu.NewState(nil)               // wangshu_p3 build 下自动注入真 P3(§6.6)
    bridge.SetForceAllPromote(state, true)       // §2.2 / §7.2 测试入口
    prog, _ := wangshu.Compile(src, "p3diff")
    var buf bytes.Buffer
    state.SetStdout(&buf)
    _ = prog.Call(state, nil)                    // 错误也是可观察输出(O3,12 §3.2)
    return buf.Bytes()
}

// FuzzCrescentVsGibbous —— 层间差分 fuzz(§2.5 双轴的层间轴)
func FuzzCrescentVsGibbous(f *testing.F) {
    f.Add(uint32(0xdeadbeef))
    f.Fuzz(func(t *testing.T, seed uint32) {
        src := genCompilableScript(seed)         // 生成器偏向 Compilable Proto(§2.5 回填请求)
        if !isValidP1(src) { return }            // 复用 P1 §3.7 合法性 pre-check
        outCrescent := normalize(runCrescentOnly([]byte(src)), defaultNorm)
        outGibbous  := normalize(runForceAllGibbous([]byte(src)), defaultNorm)
        if !bytes.Equal(outCrescent, outGibbous) {
            // 任一层间不一致都是阻断级 bug(本文 §4.1)
            t.Errorf("[LAYER MISMATCH] crescent vs gibbous diverge:\n%s\ncrescent=%q gibbous=%q",
                src, outCrescent, outGibbous)
        }
    })
}
```

> **失败用例固化**(承 P1 [12 §3.6](../p1-interpreter/12-testing-difftest.md) 「fuzz 撒网 → conformance 固化」闭环):层间 fuzz 撞出的差异,最小化后固化为一条确定性 conformance 用例入 `test/conformance/own/`(P3 用例组),golden = crescent 输出。回归不再依赖随机种子撞中。

---

## 3. GC 压力 fuzz 同样上 gibbous

> p3-wasm-tier.md 原稿 §7 第三点:**「GC 压力 fuzz 同样上 gibbous:逼出 locals 写回遗漏([§6.3](./05-safepoint-gc.md))与助手内根登记缺漏」**。本节承 P1 [12 §5](../p1-interpreter/12-testing-difftest.md) GC 压力 fuzz 形态,扩到 gibbous 执行层。

### 3.1 GC 压力模式回顾(承 P1 12 §5)

P1 [12 §5](../p1-interpreter/12-testing-difftest.md) 定的 GC 压力 fuzz = **把 GCPAUSE 设到极小(每次/每几次分配就 full GC),反复跑同一程序**,验证两件事:
- **GC 透明性**:同一脚本在「正常 pacing」与「极高频 GC」下可观察输出 byte-equal。
- **不崩溃**:极高频 GC 下脚本跑完不 panic / 不脏读。

为什么高频 GC 是必现手段(承 P1 [12 §5.2](../p1-interpreter/12-testing-difftest.md)):漏根/漏扫在正常 pacing 下偶发(GC 恰好在「对象被持有但未上根」窗口触发概率低),高频 GC **把概率拉到 1**——每次分配都 full GC,漏根的对象必被回收,后续用必崩/脏读。**把偶发 bug 变成确定 bug**。

机制锚点(承 P1 [12 §5.3](../p1-interpreter/12-testing-difftest.md)):[06 §8.3](../p1-interpreter/06-memory-gc.md) 的 `threshold = live * GCPAUSE / 100`,GCPAUSE 设 1 即「存活量 1% 增量就 GC」≈ 每次分配 GC。这需要 collector 暴露测试可调的 GCPAUSE(`testOpts` 注入,P1 已建)。

### 3.2 在 gibbous 上跑相同模式——漏写回必现为脏值/误回收

P3 把 GC 压力模式套到 gibbous 执行层。**靶标从 P1 的「crescent shadow stack 漏 Pin / mark 漏扫」扩到 gibbous 特有的两类漏根**:

| gibbous 特有漏根 | 来源 | 高频 GC 如何必现 |
|---|---|---|
| **locals 缓存写回遗漏** | 若 [PW9](./00-overview.md) 启用 [§2.2B](./02-translation.md) locals 缓存优化:缓存进 Wasm locals 的值对 GC 不可见,任何 safepoint/助手调用前必须写回栈槽([05 §4](./05-safepoint-gc.md))。**漏写回** = GC 扫到的栈槽是陈旧值 → 缓存的活对象未被根登记 → 被误回收 | 每分配即 GC ⇒ 缓存活对象期间的任一分配点必撞 GC ⇒ 漏写回的对象必被回收 ⇒ 后续读栈槽是脏值/已回收 → 输出错(透明性破)或崩溃 |
| **助手内根登记缺漏** | imported 助手(`$h_call`/`$h_gettable`/`$h_arith`)内分配中间对象时,若漏 push shadow stack([06 §6.3](../p1-interpreter/06-memory-gc.md)),分配窗口内被回收 | 助手内分配触发 GC ⇒ 漏 Pin 的中间对象必被回收 ⇒ 助手返回后用必崩 |

```go
//go:build wangshu_p3

// test/difftest/gcfuzz.go —— P1 已建,P3 加 gibbous 轴
func TestGCStressGibbous(t *testing.T) {
    // 分配密集脚本(承 12 §3.7 生成器偏向 table/concat/closure)
    // 强制全升,使分配密集 Proto 都走 gibbous
    for _, script := range allocHeavyCompilableScripts(t) {
        // 基线:gibbous 正常 pacing 跑一次
        baseOut := runGibbousWithGCPause(t, script, /*GCPAUSE=*/200)   // 默认 2.0x(06 §8.3)
        // 压力:gibbous + 极小 GCPAUSE 反复跑
        for _, pause := range []int{1 /*每分配即 GC*/, 2, 5} {
            stressOut, err := runGibbousWithGCPauseSafe(t, script, pause)
            if err != nil {
                t.Errorf("GIBBOUS GC stress CRASH @pause=%d %s: %v", pause, script, err)  // 漏根崩溃
            }
            if !bytes.Equal(stressOut, baseOut) {
                t.Errorf("GIBBOUS GC NOT transparent @pause=%d %s\nbase:%s\nstress:%s",   // 漏写回脏值
                    pause, script, baseOut, stressOut)
            }
        }
    }
}

// V13 子测:locals 缓存 A/B 对照(若 PW9 启用 §2.2B 优化)
func TestGCStressGibbousLocalsCacheAB(t *testing.T) {
    // A:全 memory-resident(基线,根天然可见,§6.2)
    // B:locals 缓存(需写回纪律,§6.3)
    // 对照:A 与 B 在高频 GC 下输出都应 == crescent;若 B 破而 A 不破,说明写回遗漏
    for _, script := range allocHeavyCompilableScripts(t) {
        outA := runGibbousLocalsCacheMode(t, script, /*cache=*/false, /*pause=*/1)
        outB := runGibbousLocalsCacheMode(t, script, /*cache=*/true,  /*pause=*/1)
        outCrescent := runCrescentOnly(script)
        if !bytes.Equal(outA, outCrescent) {
            t.Errorf("[A memory-resident] gibbous diverge from crescent under GC stress: %s", script)
        }
        if !bytes.Equal(outB, outCrescent) {
            t.Errorf("[B locals-cache] WRITE-BACK MISS suspected: %s\n(A passed, B failed ⇒ 写回纪律漏插)", script)
        }
    }
}
```

> **基线对照是关键**(承 P1 [12 §5.3](../p1-interpreter/12-testing-difftest.md)):不是「高频 GC 跑通就行」,而是「gibbous 高频 GC 输出 == gibbous 正常 pacing 输出 == crescent 输出」。透明性比不崩溃更强——不崩溃只证明没 use-after-free,透明性还证明没「悄悄回收了不该回收的、导致输出错」。

### 3.3 longevity 测试套接 gibbous

承 P1 [12 §11](../p1-interpreter/12-testing-difftest.md) 的长跑任务形态(独立长跑、长时间随机撞角落)。P3 把 longevity 套接到 gibbous——长跑不仅撞「新角落」,还验「累积状态稳定」:

- **长跑 + 强制全升**:跑分配密集脚本数百万迭代,全程 gibbous + 中等 GC pacing,验证:① 无内存增长(arena live set 稳定,gibbous 不泄漏根)② 输出始终 == crescent(无累积性写回脏化)。
- **gibbous 帧 + 跨层往返的累积验证**:longevity 重点轰炸跨层边界(gibbous → crescent → gibbous,[04 §3](./04-trampoline.md))——每次往返压/弹 CallInfo(bit50,[04 §1](./04-trampoline.md)),长跑验证 CallInfo 栈无泄漏、bit50 标志无残留污染。

### 3.4 freelist 复用 + gibbous(GCRef 偏移寻址使复用语义不变)

承 [06](../p1-interpreter/06-memory-gc.md) 的 freelist 复用机制。P3 验证「freelist 复用 + gibbous」组合下语义不变:

- **关键物理事实**:GCRef = 48-bit 字节偏移([01 §2](../p1-interpreter/01-value-object-model.md)),在 Wasm 侧就是 linear memory 地址(offset 寻址,[03 §1](./03-memory-model.md))。**freelist 复用一块内存时,新对象拿到的是同一偏移**——gibbous 代码用偏移寻址读写,**复用前后偏移语义完全不变**(gibbous 不缓存任何「指针」,每次都从栈槽取 GCRef 偏移再寻址)。
- **测试**:GC 压力 + freelist 高频复用脚本(频繁分配-释放-再分配同尺寸对象),强制全升跑 gibbous,验证复用后 gibbous 读到的是新对象内容(不是被复用前的陈旧内容)。这与 crescent 行为必须 byte-equal——因为两层用同一份 freelist、同一套偏移寻址。

> **为什么 gibbous 不破坏 freelist 复用语义**:gibbous 的「值世界 = linear memory」([03](./03-memory-model.md))意味着它和 crescent 共享同一个 arena 分配器(含 freelist)。gibbous 不自己分配([05 §3](./05-safepoint-gc.md) 分配全经助手回 Go),所以 freelist 的复用决策完全发生在 crescent 侧的同一份分配器里——gibbous 只是「读写偏移」,对它而言一块内存是新分配还是 freelist 复用毫无区别(都是偏移寻址)。复用语义不变是 [03](./03-memory-model.md) 「两层共见」的直接推论。

### 3.5 gibbous GC 压力的靶标分层(crescent 已验证项 vs gibbous 新增项)

P3 的 GC 压力 fuzz 比 P1 多一层靶标。把两层靶标分清(避免重复验证 crescent 已验证的、漏验 gibbous 新增的):

| GC 压力靶标 | crescent(P1 已验证) | gibbous(P3 新增需验) |
|---|---|---|
| mark 漏扫某字段 | ✅ P1 [12 §5](../p1-interpreter/12-testing-difftest.md) 已验(crescent 对象遍历完整) | **不新增**——gibbous 用同一份对象布局 + 同一份 mark([05 §2](./05-safepoint-gc.md) 根枚举一行不改),crescent 验过即 gibbous 验过 |
| host function shadow stack 漏 Pin | ✅ P1 [12 §5.3](../p1-interpreter/12-testing-difftest.md) 已验(stdlib 分配类) | **部分新增**——gibbous 的 imported 助手(`$h_call`/`$h_gettable`)内分配若漏 Pin,是新代码路径,需验 |
| **locals 缓存写回遗漏** | — crescent 无此概念(寄存器=值栈槽) | **全新增**(V13)——gibbous [§2.2B](./02-translation.md) locals 缓存特有,漏写回 = 缓存活对象未上根 |
| **gibbous 帧根可见性** | — | **全新增**(V5)——gibbous 帧活跃寄存器=值栈槽([05 §2](./05-safepoint-gc.md) 基线 memory-resident),验「gibbous 帧在 GC 时根天然可见」 |

**核心论证**(为什么 gibbous GC 压力靶标主要是「写回遗漏」):gibbous 基线选 memory-resident([02 §2.2A](./02-translation.md)),**寄存器就是共见值栈槽,GC 根枚举原样覆盖**([05 §2](./05-safepoint-gc.md) 「零新增机制」)——所以基线下 gibbous 帧的根可见性与 crescent 同构,GC 压力不会暴露新 bug(V5 验的是「确认这条红利成立」)。**唯一新增靶标是 locals 缓存优化([§2.2B](./02-translation.md))**:一旦把热槽缓存进 Wasm locals(对 GC 不可见),就引入「写回纪律」——这是 gibbous 特有的、P1 从未有过的 bug 类。**V13 的 A/B 对照(memory-resident vs locals 缓存)正是为了隔离这条新增靶标**:A(基线)在 GC 压力下应永远 byte-equal(根天然可见),B(缓存)若 GC 压力下破而 A 不破,精确定位「写回纪律漏插了哪个 safepoint」。

> **gibbous GC 压力的隐性红利**:正因基线 memory-resident 使 gibbous GC 根与 crescent 同构,**P3 GC 压力 fuzz 的大部分覆盖是「免费的」**——crescent 已验过的 mark 完整性、对象遍历,gibbous 原样继承。P3 只需把 fuzz 算力集中在「locals 缓存写回」和「imported 助手内 Pin」两条新增路径。这是 [02 §2.2](./02-translation.md) 选「基线 memory-resident、locals 缓存作受纪律的优化」在测试维度的回报:**正确性面最小化,GC 压力验证面最小化**。

---

## 4. 持续 fuzz + CI 必过

### 4.1 任何 crescent/gibbous 输出不一致都是阻断级 bug

p3-wasm-tier.md 原稿 §7 第四点:**「持续 fuzz + CI 必过:任何 crescent/gibbous 输出不一致都是阻断级 bug」**。

这与 P1 [12 §3](../p1-interpreter/12-testing-difftest.md) 的 byte-equal 差分主防线 + P2 [06 §1 T2](../p2-bridge/06-testing-strategy.md) 的「byte-equal 不可破」**同地位**。具体纪律:

- **层间差分不一致 = 翻译 bug = 立即停 PR**:gibbous vs crescent 任一脚本输出不同,PR 不合入。根因分析后,最小化复现固化为 conformance 用例(§2.6)。
- **不存在「差不多」**:gibbous 不开新豁免(§2.4),所以「不一致」就是真 bug,没有「这是已知 gibbous 偏差」的退路。
- **GC 压力透明性破 = 写回遗漏 / 漏根 = 阻断**:V5/V13 任一脚本透明性破(gibbous 高频 GC 输出 ≠ 正常 pacing),即写回纪律或根登记有漏,立即停。

### 4.2 nightly diff fuzz 上 gibbous(扩 nightly-diff-fuzz.yml)

承 P1/P2 已建的 `.github/workflows/nightly-diff-fuzz.yml`(每晚 2 万种子差分 fuzz)。P3 扩 gibbous 轴:

```yaml
# .github/workflows/nightly-diff-fuzz.yml —— P3 扩展(示意)
jobs:
  nightly-diff-fuzz:
    steps:
      # P1/P2 已有:三方差分 fuzz(crescent vs gopher-lua vs 官方)
      - name: P1 three-way diff fuzz
        run: go test -fuzz=FuzzThreeWay -fuzztime=2h ./test/difftest/

      # P3 新增:层间差分 fuzz(crescent vs gibbous,强制全升)
      - name: P3 layer diff fuzz (gibbous)
        run: go test -tags wangshu_p3 -fuzz=FuzzCrescentVsGibbous -fuzztime=2h ./test/difftest/

      # P3 新增:GC 压力 fuzz 上 gibbous
      - name: P3 GC stress fuzz (gibbous)
        run: go test -tags wangshu_p3 -run=TestGCStressGibbous -count=20 ./test/difftest/
```

> **nightly triage**:承 [recent commit 9bc5154](../../../) (`nightly-fuzz triage 加 go-fuzz crash 第三档识别`)的 triage 形态——层间差分 fuzz 的 crash/mismatch 报告纳入同一 triage 流(自动最小化 + 分类:翻译 bug / 写回遗漏 / 跨层错误冒泡)。**层间 mismatch 单列一档**(与 P1 三方 mismatch、go-fuzz crash 区分),因为它必是翻译 bug(无实现噪声,§2.1)。

### 4.2.1 PR 门禁 vs nightly 的时序分工

承 P1 [12 §11](../p1-interpreter/12-testing-difftest.md) 「PR 门禁防回归 + nightly 拓新」分工 + P2 [06 §11 GAP-T6](../p2-bridge/06-testing-strategy.md) 「CI 集成时序排期」。P3 把 build tag 维度叠进时序:

| 阶段 | 跑什么 | 时间预算 | build tag |
|---|---|---|---|
| **PR check** | V1-V13 形状级单测(conformance/p3_test.go)+ 翻译器内测 + 层间差分**短** fuzz(`-fuzztime=30s`)+ V18 -race | < 5 min | `wangshu_profile wangshu_p3` |
| **PR check(P1/P2 不豁免)** | P1 三方差分一轮 + P2 V1-V22(§1.5) | < 5 min | 同上 |
| **nightly** | 层间差分**长** fuzz(2h)+ GC 压力上 gibbous(`-count=20`)+ longevity(§3.3)+ 性能基准全档(V14-V16) | 数小时 | 同上 |
| **release 前** | 全套 + benchmark 矩阵三档实测产出(§5.5 数据列) | 不限 | 三套 build 全跑 |

**关键纪律**(承 P2 [06 §10 T7](../p2-bridge/06-testing-strategy.md) 「fuzz 长跑预算」):PR check 短 fuzz(30s)+ nightly 长 fuzz(2h)**同时存在**,任一缺失视为 fuzz 不充分。PR check 用短 fuzz 保反馈速度(防回归),nightly 用长 fuzz 拓新(撞角落)。**性能基准(V14-V16)不进 PR check**(基准噪声大、耗时长,放 nightly + release 前),只在 nightly 跑——但 nightly 性能回归(gibbous 加速跌破门槛)同样阻断(进 triage)。

### 4.3 P3 是 try-compile 非投机,差分面比 P4 窄——风险阶梯

p3-wasm-tier.md 原稿 §7 第四点末:**「P3 是 try-compile 非投机,差分验的是翻译正确性(比 P4 验投机正确性的面窄,这也是先 P3 后 P4 的风险阶梯)」**。

| 维度 | P3(本文) | P4 |
|---|---|---|
| 编译范式 | try-compile(快路径=语义分发,失败走助手,[06 §1](./06-ic-feedback-consume.md)) | 投机(speculative guard + deopt) |
| 差分验什么 | **翻译正确性**(直线代码语义 == 解释器) | **投机正确性 + 去优化着陆点严格性**(漏 guard 静默错果) |
| 差分失败的后果 | 翻译 bug(确定性,gibbous 必现) | 投机错果(偶发,只在 guard 漏的罕见路径触发) |
| 验证难度 | 较低(同输入字节码,无投机,差分必现) | 高(投机路径 + deopt 着陆点要专门 fuzz) |
| 零 deopt | ✅ gibbous 无运行期退回路径([00-overview §9](./00-overview.md) 不变式 6) | ❌ 引入 deopt(OSR exit 回 crescent) |

**风险阶梯设计的意义**:P3 先在「不用调试机器码 + 非投机」的后端上跑通整套分层骨架(升层/fallback/trampoline/跨层差分),把**最危险的 JIT bug 类(投机错果)排除在外**——P3 的差分只验翻译,验证面窄、必现、好定位。P4 在 P3 已验证的分层骨架上**只换发射后端 + 引入投机**([p4-method-jit/01-launch-judgment](../p4-method-jit/01-launch-judgment.md) §2),此时差分面才扩到投机正确性。**先把窄面的 P3 验透,再上宽面的 P4**——这是「每阶段一块硬骨头」(roadmap §5 原则 3)在测试维度的体现。

> **P3 差分套是 P4 的遗产**:P3 建好的 `Runner` 抽象(§2.1)、强制全升模式(§2.2)、层间 fuzz(§2.5)、GC 压力上 gibbous(§3),P4 **原样继承**——P4 只需加 `WangshuFullmoon`/`WangshuJIT` runner + 投机差分专项(deopt 着陆点 fuzz)。P3 把层间差分框架建好,是给 P4 的「投机主防线」提前铺好轨道(承 P1 [12 §3.8](../p1-interpreter/12-testing-difftest.md) 「P1 给 P3+ 铺轨道」的递归)。

### 4.4 层间差分作为 P4 投机正确性验证的预演

P3 的层间差分(同一 Proto 走两层逐字节比)与 P4 的投机正确性验证**形态同构、靶标不同**——P3 是 P4 的预演。展开这层关系:

| 维度 | P3 层间差分 | P4 投机差分 |
|---|---|---|
| 比对形态 | 同一 Proto 走 crescent vs gibbous | 同一 Proto 走 crescent vs fullmoon(投机) |
| oracle | crescent(P1 已验证) | crescent(P1 已验证) |
| 被测的 bug 类 | 翻译 bug(直线代码语义错) | **投机 bug(guard 漏了某条件,投机路径静默错果)** |
| 失败的触发性 | **确定性**(翻译错则任何输入必现) | **条件性**(只在投机假设不成立的罕见输入触发) |
| 差分够不够 | **够**(强制全升 + 语法制导生成即覆盖全部翻译路径) | **不够**(需专门构造「打破投机假设」的输入 + deopt 着陆点 fuzz) |

**为什么 P3 验证面窄是好事**(承 §4.3 风险阶梯):P3 的翻译 bug 是**确定性**的——某 opcode 翻译错了,任何走到它的输入都会暴露(强制全升 + 充分 fuzz 必撞)。P4 的投机 bug 是**条件性**的——guard 漏了「x 可能不是 number」这个条件,只有当 x 真的不是 number 的罕见输入才暴露,普通输入下投机路径「碰巧对」。**条件性 bug 比确定性 bug 难撞几个数量级**——这是 JIT「投机错误静默错果」之所以是「最危险 bug 类」(roadmap §5 原则 2)的根源。

**先 P3 后 P4 的测试意义**:P3 把「同一 Proto 走两层 byte-equal」这套**机制**(Runner 抽象、强制全升、层间 fuzz、最小化固化)在**确定性 bug** 上验透——机制本身的正确性(差分管线没漏报、最小化收敛、固化回流)在 P3 阶段就被压力测试过。P4 接手这套已验证的机制,**只需把靶标从「翻译」换到「投机」**(加 deopt 着陆点专项 fuzz、构造打破投机假设的输入生成器)。**机制在 P3 验透,靶标在 P4 升级**——避免「P4 同时调试投机 bug + 调试差分机制本身」的双重不确定性。这是风险阶梯在测试机制维度的完整含义。

**P4 视角具体验证形态**(2026-06-28,承 [P4 implementation-progress §2 RJ-23](../p4-method-jit/implementation-progress.md) 跨文档回填请求):P4 已兑现该预演机制,具体形态见 [P4 08 §4 V1-V13 正确性轴](../p4-method-jit/08-testing-strategy.md) + [P4 08 §5 V17-V22 prove-the-path + OSR exit + guard 漏判 fuzz](../p4-method-jit/08-testing-strategy.md):
- V17 prove-the-path 字节级路径命中实证(26 e2e + 11 difftest + 18 单测 + 5 V18 -race)
- V19 OSR exit 状态等价(SpecP4DeoptHits 增长实证)
- V20 deopt 风暴(5 caller 独立累积)
- V22 guard 漏判 fuzz harness(FuzzP4ForceAllPromote,1.5M execs / 24 seeds)
- R14 ABI 后验(GCStress + ConcurrentGC + DeepStack,morestack/抢占下 Go G 正确性)

---

## 5. 性能基准设计

### 5.1 验收门:循环密集 ≥2x over P1(以 P1 为基线)

承 [00-overview §4](./00-overview.md) PW9 性能门 + roadmap §4「循环密集脚本相对 P1 再 ≥2x」。V14 的精确口径:

- **基线 = P1 crescent**(纯解释,**不是 gopher-lua**)。
- **被测 = gibbous**(强制全升,同机同脚本 A/B)。
- **门槛**:`ns_op(crescent) / ns_op(gibbous) >= 2.0`,在 loop 档(循环密集形状)。
- **同机 A/B**:固定 CPU 频率([01 §1.2](./01-spike-gate.md) spike 同款方法论),同一进程内先跑 crescent 档再跑 gibbous 档,消除机器差异。

```go
// benchmarks/realworld/realworld_test.go —— P3 加 gibbous 档对照(示意)
func BenchmarkLoopHeavy(b *testing.B) {
    src := loadScript("loop_heavy.lua")              // 循环密集形状(列内核代理)
    b.Run("crescent", func(b *testing.B) {           // 基线:纯解释
        runBench(b, src, /*gibbous=*/false)
    })
    b.Run("gibbous", func(b *testing.B) {            // 被测:强制全升
        runBench(b, src, /*gibbous=*/true)
    })
}
// 验收脚本读两档 ns/op,断言 speedup = crescent/gibbous >= 2.0
```

**收益来源**(承 [02 §2.2](./02-translation.md)):≥2x 不靠寄存器提升,靠**消灭 dispatch 与译码**——解释器每条指令付「取指 + switch 间接跳 + 操作数位运算」([05 §2.1](../p1-interpreter/05-interpreter-loop.md)),gibbous 编译后是直线代码,操作数是编译期立即数。循环密集形状里这个差距被回边次数放大,故 ≥2x 在 loop 档最易兑现。

#### 5.1.1 同机 A/B 方法论(消除机器差异)

承 [01 §1.2](./01-spike-gate.md) spike 同款基准方法论(固定 CPU 频率、足量 `-benchtime`、wazero 编译模式非解释模式)。V14/V15 的 A/B 必须**同机同进程内对照**,而非「crescent 跑一台机、gibbous 跑另一台」:

- **同一进程内先后跑**:`b.Run("crescent")` 与 `b.Run("gibbous")` 在同一 `go test -bench` 进程内顺序执行,共享同一 CPU 频率档、同一内存布局、同一 GC 设置——消除「不同机器主频/缓存差异」噪声。
- **固定 CPU 频率**:禁用 turbo/频率调节(基准前 `cpupower frequency-set` 或等价),否则 gibbous 跑时若恰好降频会虚低加速比。
- **足量 benchtime + 多次取中位**:`-benchtime` 足够长(让 JIT 编译开销摊薄)、`-count` 多轮取中位数(抗偶发抖动)。**gibbous 的编译开销(wazero.Compile + 实例化)发生在升层时刻一次性**([02 §1.1](./02-translation.md)),不应计入热循环 ns/op——基准应在「已升层稳态」测量(预热跑几轮触发升层,再测稳态)。
- **强制全升保证被测的是 gibbous**:基准的 gibbous 档用 `SetForceAllPromote`(§2.2)保证热 Proto 确实走 gibbous(不依赖自然热度,避免「基准脚本没跑够阈值导致测的还是 crescent」)。

#### 5.1.2 摊销模型在基准上的体现(承 01 §1.3)

[01 §1.3](./01-spike-gate.md) 的跨层摊销模型在基准上的可观察形态:

```
每迭代净收益 = I·(c_interp − c_wasm) − k·T_cross
  I = 每迭代指令数, k = 每迭代跨层次数, T_cross = 单次跨层成本(spike S2 实测)
```

- **loop 档(V14 ≥2x)= 低 k 形状**:循环体纯算术/表访问,k≈0(仅偶发分配/IC miss),`I·Δc` 主导,收益完整兑现 ⇒ ≥2x。基准脚本应选「自包含热循环」(列内核理想形状)。
- **realworld 五脚本(V15 ≥1.5x)= 混合 k 形状**:fib/binarytrees 递归+分配密集,k≥1(每次递归调用/分配跨层),`k·T_cross` 吃掉部分收益 ⇒ 加速低于 loop 档。**这就是 V15 门槛(1.5x)低于 V14(2x)的摊销模型解释**——真实负载混入更多跨层,收益被摊薄。
- **基准矩阵验证摊销模型**:baseline 档(单 opcode)测 `c_wasm`(无跨层,纯指令开销),boundary 档测 `T_cross`(纯跨层,无指令),realworld 档测 `I·Δc − k·T_cross`(混合)——三档数据可反推摊销模型各项,定位「某脚本加速低是因为 k 高还是 Δc 低」。

### 5.2 坐标系警告:流水线图 4-8x vs 验收 ≥2x 不可混用

p3-wasm-tier.md 原稿 §8 末「坐标系警告」+ [evolution-roadmap](../../../llmdoc/architecture/evolution-roadmap.md):**流水线图的倍率与阶段验收门槛不在同一坐标系**。

| 数字 | 基线 | 含义 | 出处 |
|---|---|---|---|
| **流水线图「4-8x」** | **gopher-lua** | gibbous 相对 gopher-lua 的总加速(P1 链乘 P3) | [evolution-roadmap](../../../llmdoc/architecture/evolution-roadmap.md) 流水线图 |
| **验收门「≥2x」** | **P1 crescent** | gibbous 相对 P1 自身解释器的增量加速 | roadmap §4 + 本文 V14 |

**两者不可混用**——验收用 ≥2x(以 P1 为基线),流水线图的 4-8x(以 gopher-lua 为基线)是另一个坐标系。混用会导致「拿 gopher-lua 基线的 4-8x 去验收 gibbous」(门槛虚高,gibbous 永远过不了)或「拿 P1 基线的 2x 去宣传总加速」(数字虚低)。

> **坐标系混用是 [evolution-roadmap](../../../llmdoc/architecture/evolution-roadmap.md) 点名的警告项**:流水线图(展示总演进倍率)与阶段验收门槛(展示单阶段增量)服务不同目的,**画在一起会被误读为同一坐标系**。本文 V14 钉死「以 P1 为基线」,任何性能讨论引用倍率时**必须标注基线**。

### 5.3 P1 自身 ≥2x over gopher-lua,链乘后量级自洽

承 roadmap §4(P1 验收「三档脚本 ≥2x over gopher-lua」)+ p3-wasm-tier.md 原稿 §8 末括注:

```
P1:   crescent ≥ 2x over gopher-lua    (P1 已验收交付)
P3:   gibbous  ≥ 2x over crescent      (本文 V14 验收)
链乘:  gibbous ≥ 4x over gopher-lua     (= 2x × 2x,流水线图下沿)
```

**链乘后量级自洽**:P1 的 ≥2x(over gopher-lua)× P3 的 ≥2x(over P1)= gibbous 至少 4x over gopher-lua,正是流水线图「4-8x」的下沿。上沿 8x 留给「循环密集形状里 P1 和 P3 各自超过 2x」的叠加(列内核理想形状)。**这套链乘是两个坐标系的桥**——把 V14 的 P1-基线门槛翻译回流水线图的 gopher-lua-基线时,用链乘换算,不直接比较。

### 5.4 benchmark-game 五脚本作为代理负载

承 [benchmarks/realworld/testdata](../../../benchmarks/realworld/testdata/) 已有的五脚本(实际文件:`fib.lua` / `binarytrees.lua` / `spectralnorm.lua` / `nbody.lua` / `fannkuch.lua`)。V15 用它们作代理负载:

| 脚本 | 形状 | gibbous 加速预期 |
|---|---|---|
| `fib.lua` | 深递归 + 整数算术 | 中(递归跨层调用频繁,但每帧算术少) |
| `binarytrees.lua` | 分配密集(树节点)+ 递归 | 低-中(分配经助手回 Go,跨层多;GC 压力大) |
| `spectralnorm.lua` | 浮点密集 + 嵌套循环 | **高**(浮点算术快路径 + 回边密集,gibbous 强项) |
| `nbody.lua` | 浮点密集 + 固定循环 | **高**(同 spectralnorm) |
| `fannkuch.lua` | 数组操作 + 整数算术 + 循环 | 中-高(表 IC + 循环) |

**V15 门槛**:五脚本整体加速曲线 **≥1.5x**(几何均值),**部分脚本短板可接受**——分配密集(binarytrees)和深递归(fib)因跨层往返多,加速低于循环密集脚本是预期的([01 §1.3](./01-spike-gate.md) 摊销模型:k 次跨层每次 T_cross,跨层多则收益被吃)。门槛设 1.5x(低于 loop 档的 2x)反映「真实负载比纯循环代理混入更多跨层」。

```go
// 验收:五脚本几何均值加速 >= 1.5
func TestRealworldSpeedupGeomean(t *testing.T) {
    scripts := []string{"fib", "binarytrees", "spectralnorm", "nbody", "fannkuch"}
    var speedups []float64
    for _, name := range scripts {
        nsC := benchCrescent(name)
        nsG := benchGibbous(name)
        speedups = append(speedups, float64(nsC)/float64(nsG))
    }
    if geomean(speedups) < 1.5 {
        t.Errorf("realworld geomean speedup %.2f < 1.5 (per-script: %v)", geomean(speedups), speedups)
    }
}
```

### 5.5 baseline / realworld / boundary 三档基准矩阵在 P3 接入

承 [Makefile](../../../Makefile) `bench` 目标的四档基准(纯 VM micro / 真实负载纯 VM / 边界 mini / 真实负载 embedded)。P3 把其中三档接入 gibbous 对照:

| 档 | 目录 | P3 验收口径 | 测什么 |
|---|---|---|---|
| **baseline**(纯 VM micro) | `benchmarks/baseline` | 辅助(定位收益构成) | 单 opcode 级 micro(MOVE/ADD/FORLOOP),gibbous vs crescent 单指令开销 |
| **realworld**(真实负载纯 VM) | `benchmarks/realworld` | **V14 / V15 主验收** | loop 档(V14 ≥2x)+ 五脚本(V15 ≥1.5x geomean) |
| **boundary**(边界 mini) | `benchmarks/embedded` | **V16** | 空 Proto 单次调用往返,gibbous ≥ spike S2 的 95%(无退化) |

V16 边界往返口径:gibbous 的边界往返(crescent → gibbous 空 Proto → 返回)实测值 **≥ [01 §1.2](./01-spike-gate.md) spike S2 实测值的 95%**——即 PW9 实装后的真实跨层成本不应比 spike 阶段测的劣化超过 5%。这是「spike 闸门通过 ≠ 实装后仍达标」的回归防护(spike 测的是裸 wazero call boundary,实装后边界上要压 CallInfo + 写 bit50 + memory adapter 取视图,这些额外成本不应吃掉 spike 的余量)。

> **三档矩阵的具体数据列展开留 [PW9](./00-overview.md) 实测产出**(本文 §9 缺口):baseline 各 opcode 的 gibbous/crescent 比值、realworld 五脚本的实测 speedup、boundary 的实测 ns 值——这些数据列在 P3 build 下实测后填入 [implementation-progress](./implementation-progress.md)。本文只定**门槛与口径**,不预填数据(避免凭空捏造基线)。

### 5.6 性能基准与正确性差分的隔离

性能轴(V14-V16)与正确性轴(V1-V13)**用不同的运行配置,不可混跑**:

| 维度 | 正确性差分(V1-V13) | 性能基准(V14-V16) |
|---|---|---|
| 运行模式 | `go test`(单测 + fuzz) | `go test -bench`(benchmark) |
| GC 设置 | 含 GC 压力档(GCPAUSE=1) | 默认 pacing(GCPAUSE=200,[06 §8.3](../p1-interpreter/06-memory-gc.md)) |
| 输出捕获 | 捕获 O1-O5 逐字节比对 | 只测 ns/op,不捕获输出 |
| -race | V18 开 `-race` | **基准不开 -race**(race detector 拖慢,污染 ns/op) |
| 进 PR check | ✅(短 fuzz + 形状级) | ❌(放 nightly + release 前,§4.2.1) |

**为什么必须隔离**:
- **race detector 污染基准**:`-race` 让程序慢 2-10 倍,若性能基准在 `-race` 下跑,测出的 ns/op 毫无意义(V14 的 ≥2x 会被 race 开销淹没)。所以 V18(`-race`)与 V14-V16(基准)**永不同进程跑**。
- **GC 压力污染基准**:正确性差分的 GC 压力档(GCPAUSE=1,每分配即 full GC)是为「逼出漏根」设计的,它让程序慢几个数量级。性能基准必须用默认 pacing(GCPAUSE=200),否则测的是「GC 开销」而非「gibbous 加速」。
- **输出捕获污染基准**:正确性差分捕获 stdout 到 buffer 逐字节比,这个捕获 + 比对开销不应计入性能 ns/op。性能基准的脚本应**最小化可观察输出**(纯计算,不 print),只测执行速度。

> **隔离的工程后果**:`make test-p3`(正确性)与 `make test-p3-bench`(性能)是**两个独立目标**(§6.7),分别在不同 CI 阶段跑(正确性进 PR check,性能进 nightly)。**双轴验收(§6.8)是两套独立测量的 AND**——不存在「一次跑既验正确又验性能」的混合运行。这与 P1 [12](../p1-interpreter/12-testing-difftest.md) 把「差分套」与「基准套」分目录(`test/difftest` vs `benchmarks/baseline`)同一哲学:**正确性与性能是正交维度,测量配置必须隔离**。


## 6. 测试机制实装(总收口)

本节是 P3 各文档验收口径的**总收口** —— 给出每套测试机制的目录、build tag、测试入口。

### 6.1 test/difftest/p3_test.go:差分主体

承 §2.6 骨架。职责:V1-V13 的 crescent vs gibbous 逐字节差分主体 + 层间 fuzz(`FuzzCrescentVsGibbous`)+ 强制全升全脚本对拍(`TestForceAllPromoteByteEqual`)。build tag `wangshu_p3`(无此 tag 时 gibbous runner 不存在,文件不编译)。

### 6.2 test/conformance/p3_test.go:形状级单测

承 P1 [12 §2](../p1-interpreter/12-testing-difftest.md) conformance 形态。职责:V1-V13 的**形状级单测**(table-driven,每条 PW 的代表形状)——不靠 fuzz 随机撞,而是人写「这个直线 op / 这个算术快路径 / 这个 IC 命中」的确定性用例,crescent vs gibbous 逐字节比。这是「已知形状」兜底(对应 P1 [12 §0](../p1-interpreter/12-testing-difftest.md) conformance 与 fuzz 互补)。

```go
//go:build wangshu_p3

// test/conformance/p3_test.go —— 形状级单测(V1-V13)
func TestLinearOpcodeByteEqual(t *testing.T) {        // V1
    cases := []struct{ name, src string }{
        {"MOVE",     `local function f(a) local b=a; return b end; return f(42)`},
        {"LOADK",    `local function f() return 3.14 end; return f()`},
        {"LOADBOOL", `local function f() return true, false end; return f()`},
        {"LOADNIL",  `local function f() local a,b,c; return a,b,c end; return f()`},
        {"JMP",      `local function f(x) if x then return 1 else return 2 end end; return f(true)`},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            outC := runCrescentOnly([]byte(tc.src))
            outG := runForceAllGibbous([]byte(tc.src))
            if !bytes.Equal(outC, outG) {
                t.Fatalf("%s: crescent=%q gibbous=%q", tc.name, outC, outG)
            }
        })
    }
}
// TestArithFastPathByteEqual(V2) / TestArithSlowPathByteEqual(V3) / TestNumForByteEqual(V4)
// TestTableICHitByteEqual(V6) / TestTableICMissByteEqual(V7) / TestCrossTierCallByteEqual(V8)
// TestGibbousTracebackByteEqual(V9) / TestClosureUpvalByteEqual(V10) / TestCoroutineNoPromote(V11)
// ... 各 PW 形状逐条 ...
```

#### 6.2.1 PW 增量验收节奏(形状级单测随翻译器扩充)

形状级单测**不是 PW9 一次性写齐**,而是**随翻译器逐 PW 扩充**——每个 PW 落地一组 opcode,同步落地对应的形状级单测(承 [00-overview §4](./00-overview.md) PW2-PW8 的「验收(完成定义)」列):

| PW | 翻译器落地 | 同步落地的形状级单测 | 该 PW 完成定义 |
|---|---|---|---|
| PW2 | 5 直线 opcode + trampoline 入口 | `TestLinearOpcodeByteEqual`(V1) | 5-op Proto 升 gibbous 后 byte-equal + 升层日志触发 |
| PW3 | 算术 + 比较 + NaN + 慢路径助手 | `TestArithFastPathByteEqual`(V2)+ `TestArithSlowPathByteEqual`(V3) | 双 number 快路径直发 f64;混合类型走助手 byte-equal |
| PW4 | 控制流 + 回边 safepoint | `TestNumForByteEqual`(V4)+ `gcfuzz` 回边(V5) | for 循环 byte-equal + ≥2x 解释器 + 回边 GC byte-equal |
| PW5 | 表 IC opcode + feedback + 失效降级 | `TestTableICHitByteEqual`(V6)+ `TestTableICMissByteEqual`(V7) | 单态命中跳哈希查找;gen bump 后走助手仍正确 |
| PW6 | CALL 系列 + 跨层 + status 链 | `TestCrossTierCallByteEqual`(V8)+ `TestGibbousTracebackByteEqual`(V9) | 跨层调用 + 错误冒泡 byte-equal |
| PW7 | CLOSURE + upvalue | `TestClosureUpvalByteEqual`(V10) | 闭包 + 开放/关闭 upvalue byte-equal |
| PW8 | 线程级 tier | `TestCoroutineNoPromote`(V11)+ `TestForceAllPromoteRespectsCompilability` | 协程内不升层 |
| **PW9** | 端到端 | `TestForceAllPromoteByteEqual`(V12)+ `TestGCStressGibbousLocalsCacheAB`(V13)+ 全部回归 | V1-V18 全过 |

**增量节奏的纪律**(承 [architecture](../architecture.md) §5「每步可独立编译 + 单测通过再进下一步」):
- **每个 PW 的形状级单测在该 PW 落地时就必须过**——不是攒到 PW9 才验。这样翻译 bug 在引入它的 PW 就被抓住(更早、更可定位),而非堆到端到端阶段才暴露一堆混在一起的 bug。
- **形状级单测先于 fuzz**:每个 PW 先写确定性形状级单测(人知道该测什么),PW9 的层间 fuzz(`FuzzCrescentVsGibbous`)再撒网撞「人没想到的」。两者互补(承 P1 [12 §0](../p1-interpreter/12-testing-difftest.md))。
- **`SupportsAllOpcodes` 白名单随 PW 扩**:PW2 只 supported 5 直线 op,PW3 加算术,……逐 PW 扩。**保守缺省**(承 [00-overview §3](./00-overview.md) 关键耦合点 3):未实现的 opcode 全返 false,含未支持 opcode 的 Proto 被 F7 拦下走 crescent——所以「翻译器只支持一半 opcode」的中间状态下,只有「全部 opcode 都支持」的 Proto 才升 gibbous,差分覆盖随白名单扩充自然增长,不会出现「翻译器拿到不支持的 opcode 翻译出错」。

### 6.3 internal/gibbous/wasm/{compile,emit,memory,trampoline,helpers}_test.go:翻译器内测

承 [00-overview §3](./00-overview.md) 组件依赖。翻译器**白盒内测**(不进 conformance/difftest,属包内单测,口径由本文统一):

| 文件 | 测什么 | 对应文档 |
|---|---|---|
| `compile_test.go` | 翻译单位(每 Proto 一 module 基线)、`SupportsAllOpcodes` 渐进白名单(初空 → 逐 PW 扩) | [02 §1](./02-translation.md) |
| `emit_test.go` | opcode → WAT 发射黄金(MOVE/ADD/FORLOOP/GETTABLE 的发射形态固化) | [02 §2.3](./02-translation.md) |
| `memory_test.go` | arena 收养 wazero memory、`memory.grow` 后 GCRef/链表/bump 一字不改、视图重取 | [03 §1](./03-memory-model.md) |
| `trampoline_test.go` | 跨层入口签名 `(param $base i32)(result i32)`、status 链、CallInfo bit50、panic recover | [04](./04-trampoline.md) |
| `helpers_test.go` | imported 助手分派(gibbous/crescent/host 三路)、参数/返回值经共见栈槽 | [04 §3](./04-trampoline.md) |

**V18 -race**:`TestGibbousRace`(多 State 并发跑 gibbous)在 `compile_test.go` 或 `trampoline_test.go`,`go test -race -count=10`(承 P2 [06 §5 T6](../p2-bridge/06-testing-strategy.md) 的 `-count=10` 纪律,提高 race detector 命中率)。重点验:wazero Runtime 跨 State 共享/独立的并发约束(待 spike 验证,[03 §3](./03-memory-model.md))在 -race 下无 race。

### 6.4 wangshu_p3 build tag(与 wangshu_profile 共存)

P3 引入第三个 build tag `wangshu_p3`,与 P2 的 `wangshu_profile` 共存:

| build 组合 | P2 bridge | P3 gibbous | 等价于 |
|---|---|---|---|
| `default`(无 tag) | mock(空实现) | 不存在 | P1-only |
| `wangshu_profile` | 真 bridge(决策机) | mock P3(`SupportsAllOpcodes` 全 false) | P2(无升层) |
| `wangshu_profile,wangshu_p3` | 真 bridge | **真 P3**(wazero 翻译执行) | P3 完整 |

**为什么 `wangshu_p3` 依赖 `wangshu_profile`**:P3 是 P2 决策的消费者([00-overview §1](./00-overview.md))——没有 P2 的决策机(profiling + 可编译性分析 + considerPromotion),P3 的翻译器无从被调用。所以 `wangshu_p3` 单独存在无意义,必须 `wangshu_profile + wangshu_p3` 一起。

**V17 三套 build 零回归**(承 P2 [implementation-progress](../p2-bridge/implementation-progress.md) 「`make all` 双 build tag 全绿」+ P3 加第三套):

```makefile
# make all 在 P3 实装后扩展(承 Makefile all: 入口)
.PHONY: all-p3
all-p3:
	make all                                          # default build(P1-only 等价)
	go test -tags wangshu_profile ./...               # P2 build(V1-V22)
	go test -tags 'wangshu_profile wangshu_p3' ./...  # P3 build(V1-V18 + P1/P2 不豁免)
```

### 6.5 强制全升模式 helper:bridge.SetForceAllPromote(state, true)

承 §2.2 / §7.2。测试入口(P3 落地时实装,testing-only,只在 `internal/bridge` 暴露,**不进 wangshu 公共 API**,§7.3):

```go
// internal/bridge —— 强制全升模式(testing-only)
func SetForceAllPromote(state *State, on bool)
    // on=true:此 State 上 considerPromotion 绕过热度阈值,
    //   所有 CompCompilable Proto 在首次 doCall 前直接 Compile + installGibbous。
    // 仅供 test/difftest/p3_test.go、test/conformance/p3_test.go、benchmarks 调用。
```

### 6.6 测试入口标准化:wangshu.NewState(opts) 在 wangshu_p3 下自动注入真 P3

承 P2 [implementation-progress 06-T5](../p2-bridge/implementation-progress.md):**「`wangshu.NewState` 内部已建 Bridge,wangshu_profile build 时自动生效」**。P3 延续这个标准化——

- `wangshu.NewState(opts)` 在 `wangshu_p3` build 下**自动注入真 P3**(`internal/gibbous/wasm.Compiler` 装入 bridge 的 P3Compiler 槽)。
- `wangshu_profile`-only build 下(无 `wangshu_p3`)仍是 **mock P3**(`SupportsAllOpcodes` 全 false,承 P2 [06 §1 V22](../p2-bridge/06-testing-strategy.md) 的 mock 形态)。

**这意味着测试代码不需要手动装配 P3**:`wangshu.NewState(nil)` 在 P3 build 下就是「带真 gibbous 的 State」,测试只需 `bridge.SetForceAllPromote` 控制是否强制全升。这与 P2 的「NewState 自动建 Bridge」对偶——**测试入口的一致性**让 P1/P2/P3 三套差分套共用同一个 `NewState` 门面,只靠 build tag 切换执行层。

> **P3 build 下 P1/P2 测试套如何复用**(承 §1.5 三套并行):`wangshu_p3` build 下跑 `go test ./...` 时,P1 差分套(`test/difftest` 的 P1 三方)+ P2 验收套(`test/p2` 的 V1-V22)**都在跑**——但它们用 `wangshu.NewState` 时拿到的是「带真 P3 的 State」。**P1/P2 套不主动强制全升**(`SetForceAllPromote` 默认 false),所以它们看到的是「自然热度下偶尔升层」的 State——这恰好验证了「P3 在真实热度路径下不破坏 P1/P2 行为」(P1 三方仍 byte-equal、P2 决策仍正确)。只有 `test/*/p3_test.go` 主动开强制全升,把 gibbous 覆盖拉满。

### 6.7 make 入口(承 Makefile,P3 实装时扩展)

承 [Makefile](../../../Makefile) `all: fmt lint test fuzz conformance difftest bench-test` 入口。P3 实装时扩展(示意,目标名 P3 落地时定):

```makefile
# Makefile —— P3 实装时新增目标(与现有 default/wangshu_profile 共存)
.PHONY: test-p3 test-p3-fuzz test-p3-race test-p3-bench all-p3

# P3 主测试套(单测 + 短 fuzz),wangshu_p3 build
test-p3:
	go test -tags 'wangshu_profile wangshu_p3' ./test/conformance/...   # V1-V13 形状级
	go test -tags 'wangshu_profile wangshu_p3' ./internal/gibbous/wasm/...  # 翻译器内测
	go test -tags 'wangshu_profile wangshu_p3' ./test/difftest/...      # V12 强制全升对拍
	# 层间差分短 fuzz(30s)
	go test -tags 'wangshu_profile wangshu_p3' -fuzz=FuzzCrescentVsGibbous -fuzztime=30s ./test/difftest/

# 层间长 fuzz(nightly / 发布前)
test-p3-fuzz:
	go test -tags 'wangshu_profile wangshu_p3' -fuzz=FuzzCrescentVsGibbous -fuzztime=2h ./test/difftest/
	go test -tags 'wangshu_profile wangshu_p3' -run=TestGCStressGibbous -count=20 ./test/difftest/

# -race 套(V18,多 State 并发 gibbous)
test-p3-race:
	go test -tags 'wangshu_profile wangshu_p3' -race -count=10 ./test/difftest/
	go test -tags 'wangshu_profile wangshu_p3' -race -count=10 ./internal/gibbous/wasm/

# 性能基准(V14-V16),benchmarks 子模块
test-p3-bench:
	cd benchmarks && go test -tags 'wangshu_profile wangshu_p3' -bench=. -benchmem -count=5 -run='^$$' ./realworld/...

# P3 总验收:三套 build 零回归 + V1-V18 全过
all-p3:
	make all                                          # default build(P1-only 等价,V17 第一套)
	go test -tags wangshu_profile ./...               # P2 build(V1-V22,V17 第二套)
	make test-p3 test-p3-race                         # P3 build(V1-V13 + V18,V17 第三套)
	make test-p3-bench                                # 性能门 V14-V16
	@echo "✓ PW9 acceptance: V1-V18 verified across three build tags"
```

### 6.8 PW9 验收的「过/不过」判据(对偶 P2 06 §8.2)

承 [00-overview §4](./00-overview.md) PW9 完成定义 + §4 末「两轴任一不达标都不算 P3 交付完成」。把 V1-V18 收口成 PW9 验收判据(对偶 P2 [06 §8.2](../p2-bridge/06-testing-strategy.md) PB7 四项判据):

| 验收轴 | 对应口径 | 判据 |
|---|---|---|
| (a) 正确性:层间 byte-equal | V1-V13 | `make test-p3` 全过 + 层间长 fuzz(`test-p3-fuzz`)无 mismatch |
| (b) 性能:循环密集 ≥2x over P1 | V14 | `test-p3-bench` loop 档 `speedup >= 2.0` |
| (c) 性能:realworld 整体 ≥1.5x | V15 | `test-p3-bench` 五脚本 geomean `>= 1.5` |
| (d) 性能:边界无退化 | V16 | boundary 档 `>= spike S2 × 0.95` |
| (e) 工程:三套 build 零回归 | V17 | `all-p3` 三套 build 全绿(P1/P2 套不豁免) |
| (f) 工程:-race 通过 | V18 | `test-p3-race` 无 race |

**(a)-(f) 全过才算 PW9 验收**。任一项失败,PW9 不收单——P3 不交付。其中 **(a) 正确性轴与 (b)-(d) 性能轴是双硬门**(承 [00-overview §4](./00-overview.md) 末):正确性不达标(层间 mismatch)P3 翻译有 bug 不可交付;性能不达标(< 2x)P3 价值未兑现不可交付。**双轴是 AND 关系,不是 OR**——不能「正确但慢」也不能「快但错」。

---

## 7. 测试入口暴露(对 P2 06 与 wangshu 主包的回填请求)

### 7.1 P2 06 §11.1 已落地的 6 个测试入口在 P3 复用

P2 [06 §11.1](../p2-bridge/06-testing-strategy.md) 已落地的测试入口(承 P2 [implementation-progress](../p2-bridge/implementation-progress.md) PB7 测试入口 T1-T6),P3 复用:

| P2 测试入口 | P2 用途 | P3 复用方式 |
|---|---|---|
| `bridge.ProfileDataOf(state, proto)` | 取 Proto 的 profile 数据(计数/tier) | P3 验 V11(协程内 tier 断言)、强制全升后查 tier 是否 Gibbous |
| `bridge.ConsiderPromotion(proto, pd)` | 直接触发升层判定 | P3 强制全升模式内部复用(绕过热度直调 Compile) |
| `bridge.TrySetTierState(pd, to)` | 状态机转移(testing-only) | P3 验「gibbous 升层后 tier == TierGibbous」 |
| `wangshu.NewState` 自动建 Bridge | P2 测试无需手动装配 | P3 延续(§6.6,P3 build 下自动注入真 P3) |
| `bridge.SetGlobalDiag(diag)` | 抓升层日志 | P3 验强制全升日志限流(§9 缺口)、`function promoted to gibbous` 落点 |
| mock P3 工厂(`mockP3RejectAll`/`mockP3DummyCompile`) | P2 差分用 mock P3 | P3 build 下被真 P3 替换;P2-only build 仍用 mock |

### 7.2 P3 新增测试入口需求(留 P3 落地时实装)

本节列举 P3 新增的测试入口需求,**留 P3 PW9 落地时实装**(本文 §9 缺口):

| P3 新增测试入口 | 用途 | 暴露位置 |
|---|---|---|
| `bridge.SetForceAllPromote(state, bool)` | 强制全升模式(§2.2):绕过热度阈值,所有 Compilable Proto 直接编译 | `internal/bridge`(testing-only) |
| wazero memory adapter 健康检查 helper | 验 arena 收养 wazero memory 后视图一致([03 §1](./03-memory-model.md)):`memory.grow` 后 Go 侧视图与 wazero buffer 同步 | `internal/gibbous/wasm`(testing-only) |
| gibbous 帧 panic 注入 helper | 测 trampoline panic recover([04](./04-trampoline.md)):故意让某 imported 助手 panic,验 trampoline 把 panic 转 status=ERR 而非 Go 进程崩 | `internal/gibbous/wasm`(testing-only) |
| 跨层错误注入 helper | 测 status 链冒泡([04 §4](./04-trampoline.md)):在跨层链某层注入错误,验冒泡到 pcall 边界 byte-equal | `internal/gibbous/wasm` 或 `internal/bridge`(testing-only) |

### 7.3 测试入口暴露的纪律(同 P2 06 §11.1)

承 P2 [06 §11.1](../p2-bridge/06-testing-strategy.md) 末「回填请求不影响公共 API,只是 internal/testing 包或 testing-only 暴露」+ P2 [implementation-progress 06-T5](../p2-bridge/implementation-progress.md):

- **不进 wangshu 公共 API**:所有 P3 测试入口(`SetForceAllPromote`、memory adapter 健康检查、panic 注入、错误注入)**只在 `internal/bridge` 或 `internal/gibbous/wasm` 暴露**,不进 `wangshu` 包门面。
- **testing-only 标记**:这些入口用 `// testing-only` godoc 标注,或放 `internal/.../testing` 子包,防被生产代码误用。
- **`wangshu.NewState` 是唯一公共测试入口**:测试只经 `NewState`(P3 build 下自动注入真 P3,§6.6)+ `internal/bridge` 的 testing helper 驱动,不暴露任何「装配 P3」的公共 API。

> **为什么纪律这么严**(承 P2 06 §11.1 的同款理由):测试需要「钩子」碰 internal 状态(强制全升、注入 panic),但这些钩子若进公共 API,会让用户误以为「强制全升」是支持的运行模式(它不是,它是测试用消除时序不确定性的开关)。把它们锁在 `internal` 是「测试能力 vs 公共契约」的边界纪律——P2 已立此规(`NewState` 自动建 Bridge 而非暴露 `NewStateWithBridge` 公共入口),P3 原样遵守。

---

## 8. 不变式清单(测试维度)

实现期与日常 review 用本表自检——**P3 测试自身的不变式**(承 [00-overview §9](./00-overview.md) 九条 + P2 [06 §10 T1-T10](../p2-bridge/06-testing-strategy.md) 形态):

| 不变式 | 含义 | 一旦违反的后果 |
|---|---|---|
| **1. crescent vs gibbous byte-equal**(差分主防线) | V1-V13 任一脚本 gibbous 输出 ≠ crescent 即破;层间 fuzz 任一 mismatch 即阻断(§4.1) | 失守即翻译 bug → gibbous 静默错果(roadmap §5 原则 2 红线在 P3 的兑现) |
| **2. 性能 ≥2x over P1**(性能门) | V14 循环密集脚本 gibbous/crescent < 2.0 即破;V15 五脚本 geomean < 1.5 即破 | 失守即「分层骨架没把 dispatch 收益兑现」,P3 价值落空([00-overview §4](./00-overview.md) PW9 性能轴) |
| **3. GC 压力下 gibbous 不破坏 GC 协议** | V5/V13 任一脚本 gibbous 高频 GC 输出 ≠ 正常 pacing 即破(透明性) | 失守即 locals 写回遗漏 / 助手内根登记缺漏 → 误回收 / 脏读([05 §4](./05-safepoint-gc.md)) |
| **4. 三套 build tag 全 CI 通过** | V17:default(P1-only)/ wangshu_profile(P2)/ +wangshu_p3(P3)三套 build 全绿,P1/P2 套不豁免(§1.5) | 失守即 P3 引入破坏了阶段独立交付(roadmap §5 原则 3)——某 build 下回归 |
| **5. nightly fuzz 上 gibbous 永久跑** | 层间差分 fuzz + GC 压力上 gibbous 进 nightly,永久跑不下线(§4.2) | 失守即偶发翻译 bug / 写回遗漏不被持续撒网撞出(承 P1 [12](../p1-interpreter/12-testing-difftest.md) 「持续 fuzz」) |
| **6. gibbous 不开新豁免**(承 §2.4) | P1 [12 §4](../p1-interpreter/12-testing-difftest.md) 豁免表对 gibbous 原样适用;任何「为 gibbous 加新豁免」提案回本文重新论证 | 失守即掏空层间差分强度(gibbous 引入隐性不确定性被豁免掩盖) |
| **7. 强制全升消除时序不确定性**(承 §2.2) | 层间差分必须用强制全升(`SetForceAllPromote`),不依赖自然热度 | 失守即差分不可复现(「这次 Proto 升了那次没升」) |
| **8. crescent 是 P3 差分 oracle**(承 §2.5) | P3 层间差分以 crescent 为 oracle(P1 已验证),gibbous 为被测;不重复三方比对 | 失守即把未验证的 gibbous 当基准,差分失去判据 |
| **9. 测试入口锁 internal**(承 §7.3) | P3 测试入口只在 `internal/bridge`/`internal/gibbous/wasm` 暴露,不进公共 API | 失守即用户误把「强制全升」当支持的运行模式 |
| **10. P3 验证面比 P4 窄**(承 §4.3) | P3 差分只验翻译正确性(非投机);投机正确性 + deopt 着陆点留 P4 | 失守即把 P4 的投机验证负担提前压到 P3(违背风险阶梯) |

---

## 9. 文档缺口(待决)

| # | 缺口 | 触发条件 | 计划处理 |
|---|---|---|---|
| GAP-1 | **测试入口暴露的实装细节**(本文 §7) | P3 PW9 落地时实装 `SetForceAllPromote` / memory adapter 健康检查 / panic 注入 / 错误注入 helper | PW9 落地时在 `internal/bridge` + `internal/gibbous/wasm` 实装,testing-only 标记;回填本文 §7.2 为「已落地」 |
| GAP-2 | **强制全升模式与 P2 04 升层日志的交互**(本文 §2.2) | 每个 Proto 都打 `function promoted to gibbous` 日志([../p2-bridge/04 §6](../p2-bridge/04-try-compile-fallback.md))会刷屏 | PW9 实测后定限流(候选:强制全升模式下抑制单 Proto 日志,只打总数 `N protos force-promoted`) |
| GAP-3 | **benchmark 基准矩阵在 P3 build 下的具体数据列**(本文 §5.5) | 三档矩阵(baseline/realworld/boundary)的实测 speedup / ns 值需 P3 build 实测产出 | PW9 实测后填入 [implementation-progress](./implementation-progress.md);本文只定门槛口径,不预填数据 |
| GAP-4 | **生成器偏向 Compilable Proto**(本文 §2.5) | 层间 fuzz 需生成器产出能升 gibbous 的脚本(避开 P2 F1-F7 排除形状),否则强制全升下走不到 gibbous,差分退化 | 回填 P1 [12 §3.7](../p1-interpreter/12-testing-difftest.md) 生成器:加「Compilable 偏向」开关(产出无 vararg/coroutine/debug 的纯算术 + 循环 + 表访问脚本) |
| GAP-5 | **wazero Runtime 跨 State 并发约束**(本文 §6.3 V18) | -race 验多 State 并发 gibbous;wazero Runtime 共享还是 per-State 待定 | 待 spike 验证([03 §3](./03-memory-model.md));-race 套在 PW9 落地时按实际 Runtime 装配方式验 |
| GAP-6 | **层间 mismatch 的 nightly triage 第三档**(本文 §4.2) | 层间 mismatch 与 P1 三方 mismatch、go-fuzz crash 需分类(承 [commit 9bc5154](../../../)) | PW9 扩 `.github/workflows/nightly-diff-fuzz.yml` triage:层间 mismatch 单列一档(必是翻译 bug) |

### 9.1 本文不主管的相邻缺口(指向其它文档)

- **locals 缓存的槽选择与写回插入算法**([02 §2.2B](./02-translation.md) / [05 §4](./05-safepoint-gc.md)):V13 验「漏写回必现」,但写回算法本身由 [02](./02-translation.md)/[05](./05-safepoint-gc.md) 定,本文只验其正确性。
- **IC 快照失效 → 重编译**([06 §3](./06-ic-feedback-consume.md)):V7 验「失效降级走助手仍正确」,但重编译机制留 P4 统一评估([p4-method-jit/04-osr-deopt](../p4-method-jit/04-osr-deopt.md) §5),不在 P3 验收范围。
- **批量 module 优化**([02 §1.2](./02-translation.md)):若 PW9 后启用批量编译,差分套需加「批量 module vs 每 Proto 一 module」对照;本文当前只验基线(每 Proto 一 module)。

---

## 10. 篇尾速查:V1-V18 一行总结(对偶 P2 06 §12)

| # | 轴 | 一行断言 | 测试入口 |
|---|---|---|---|
| V1 | 正确性 | 5 直线 opcode 升 gibbous 后 byte-equal 解释 | `conformance/p3_test.go::TestLinearOpcodeByteEqual` |
| V2 | 正确性 | 双 number 算术直发 f64 + NaN 规范化 byte-equal | `TestArithFastPathByteEqual` |
| V3 | 正确性 | 算术慢路径(coercion/元方法)走助手仍 byte-equal | `TestArithSlowPathByteEqual` |
| V4 | 正确性 | 数值 for(含 step<0/step=0 错误)byte-equal | `TestNumForByteEqual` |
| V5 | 正确性 | 回边 GC 触发时 byte-equal(GC 压力) | `gcfuzz.go::TestGCStressGibbous` |
| V6 | 正确性 | 表 IC 单态命中跳过哈希查找 byte-equal | `TestTableICHitByteEqual` |
| V7 | 正确性 | 表 IC 失效(gen bump/setmetatable)走助手仍 byte-equal | `TestTableICMissByteEqual` |
| V8 | 正确性 | 跨层 CALL 链错误冒泡 byte-equal(穿越 gibbous 帧) | `TestCrossTierCallByteEqual` |
| V9 | 正确性 | gibbous traceback(经助手取 pc)byte-equal | `TestGibbousTracebackByteEqual` |
| V10 | 正确性 | 闭包 + 开放/关闭 upvalue byte-equal | `TestClosureUpvalByteEqual` |
| V11 | 正确性 | 协程内不升层(tier 恒 Interp/Stuck) | `TestCoroutineNoPromote` |
| V12 | 正确性 | 强制全升下全 Compilable Proto byte-equal | `difftest/p3_test.go::TestForceAllPromoteByteEqual` |
| V13 | 正确性 | GC 压力逼出 locals 写回遗漏/根登记缺漏 | `TestGCStressGibbousLocalsCacheAB` |
| V14 | 性能 | 循环密集 ≥2x over P1(P1 基线) | `benchmarks/realworld` loop 档 |
| V15 | 性能 | realworld 五脚本 geomean ≥1.5x | `TestRealworldSpeedupGeomean` |
| V16 | 性能 | 边界往返 ≥ spike S2 × 0.95(无退化) | `benchmarks` boundary 档 |
| V17 | 工程 | 三套 build tag 零回归(P1/P2 不豁免) | `make all-p3` |
| V18 | 工程 | -race 通过(多 State 并发 gibbous) | `TestGibbousRace`(`-race -count=10`) |

任一失败 → PW9 验收不收单 → P3 不交付。正确性轴(V1-V13)与性能轴(V14-V16)是**双硬门 AND 关系**(§6.8):不能「正确但慢」也不能「快但错」。


[00-overview](./00-overview.md)(P3 总览 §4 PW9 验收双轴 / §9 不变式 / §10 缺口) ·
[01-spike-gate](./01-spike-gate.md)(spike S1/S2/S3,V16 边界基线来源) ·
[02-translation](./02-translation.md)(翻译器,V1-V10 翻译正确性来源 + pc 物化) ·
[03-memory-model](./03-memory-model.md)(arena 收养 wazero memory,§2.4 不开新豁免 + §3.4 freelist 复用 + V18 并发约束) ·
[04-trampoline](./04-trampoline.md)(跨层互调,V8/V9 跨层 CALL + status 链 + panic recover) ·
[05-safepoint-gc](./05-safepoint-gc.md)(跨层 GC,V5/V13 回边 GC + locals 写回纪律) ·
[06-ic-feedback-consume](./06-ic-feedback-consume.md)(feedback 非投机消费,V6/V7 IC 命中/失效) ·
[07-coroutine-thread-rule](./07-coroutine-thread-rule.md)(线程级 tier,V11 协程不升层) ·
[implementation-progress](./implementation-progress.md)(进度对账,§5/§9 基准数据列 + 测试入口落地状态) ·
[../p2-bridge/06-testing-strategy](../p2-bridge/06-testing-strategy.md)(**P2 验收单一事实源,本文同款全 V 编号形态对偶面**;§1.5 三套并行不替换) ·
[../p2-bridge/implementation-progress](../p2-bridge/implementation-progress.md)(P2 测试入口 T1-T6 已落地,§7.1 P3 复用) ·
[../p1-interpreter/12-testing-difftest](../p1-interpreter/12-testing-difftest.md)(P1 差分测试矩阵,§2 接入 + §3 GC 压力承接 + §4 豁免表原样适用) ·
[../p4-method-jit](../p4-method-jit/00-overview.md)(P3 差分套是 P4 遗产,§4.3 风险阶梯) ·
[../roadmap.md](../roadmap.md)(§4 P3 验收 ≥2x over P1 / §5 原则 2 层间差分 + 原则 3 阶段独立交付) ·
[../../../Makefile](../../../Makefile)(make all 入口,§6.4 三套 build tag) ·
[../../../benchmarks/realworld](../../../benchmarks/realworld/)(P3 性能门实际脚本,§5.4 五脚本) ·
[../../../llmdoc/architecture/evolution-roadmap](../../../llmdoc/architecture/evolution-roadmap.md)(坐标系警告:流水线图倍率 vs 阶段验收门槛不同坐标系,§5.2) ·
[../../../llmdoc/must/design-premises](../../../llmdoc/must/design-premises.md)(原则 2 投机错误静默错果,§4.3 P3 非投机风险阶梯)
