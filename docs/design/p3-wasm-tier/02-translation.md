# P3-02 字节码→Wasm 翻译器:施工指南

> 状态:**详细设计**(开工前置 spike 通过后落地)。本文是 [00-overview](./00-overview.md) §0 文档地图所定的「翻译器」单一事实源——翻译单位决策、寄存器映射、全 opcode 翻译表(WAT 风格伪码 + 解释器同构验证)、pc 物化协议、`P3Compiler` 接口实装、编译器内部架构、wazero API 适配层、`SupportsAllOpcodes` 渐进白名单。
>
> 上游契约:[00-overview](./00-overview.md)(P3 总览,本文遵守其章节番号与风格基线)、../p3-wasm-tier §2(原稿主体,本文按 §1→§1 / §2→§2 / §2.3→§3 / §2.4→§4 / §10 不变式 1/2/3 → §8 章节映射展开)。
>
> P1 依赖面:[../p1-interpreter/02-bytecode-isa](../p1-interpreter/02-bytecode-isa.md)(源 ISA 完整定义,翻译器输入)、[../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md)(解释器主循环,P3 翻译器输出与之 byte-equal)、[../p1-interpreter/01-value-object-model](../p1-interpreter/01-value-object-model.md) §3(NaN-boxing 编码,Wasm 侧逐位同一)、[../p1-interpreter/06-memory-gc](../p1-interpreter/06-memory-gc.md) §5/§7(GC 根枚举与 safepoint 布点)。
>
> P2 依赖面:[../p2-bridge/02-ic-feedback](../p2-bridge/02-ic-feedback.md)(`TypeFeedback` shape,P3 可选消费)、[../p2-bridge/03-compilability-analysis](../p2-bridge/03-compilability-analysis.md) §3.7(F7 `SupportsAllOpcodes`)、[../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md) §2(`P3Compiler` 接口签名)、[../p2-bridge/04-try-compile-fallback](../p2-bridge/04-try-compile-fallback.md) §5.2(panic recover + `CompileError`)。
>
> 下游协作:[03-memory-model](./03-memory-model.md)(共见 linear memory,翻译器读写的物理基础)、[04-trampoline](./04-trampoline.md)(CALL/TAILCALL/RETURN 的跨层互调形态,本文 §3.6 链至 04)、[05-safepoint-gc](./05-safepoint-gc.md)(回边 safepoint 与 locals 写回纪律,本文 §3.5 / §6.5 链至 05)、[06-ic-feedback-consume](./06-ic-feedback-consume.md)(IC 快照固化,本文 §3.4 / §6.5 链至 06)。

对应 Go 包:`internal/gibbous/wasm`(字节码→Wasm 编译器主包,含 `compile.go`/`emit.go`/`opcodes.go`/`memory.go`/`trampoline.go`/`helpers.go`,详见 §6.1)。

---

## 0. 定位:把 Proto.Code 翻译成 Wasm 函数,语义与解释器逐字节一致

P3 工作流是「P2 喂料 → P3 翻译 → wazero 执行」三段式([00-overview](./00-overview.md) §2 总数据流)。本文承担**中间一段**:输入是 P2 决策机判定「热且可编译」并经 F7 闸门确认「全 opcode 都支持」的 `*bytecode.Proto`,可选附 `*bridge.TypeFeedback`;输出是 `bridge.GibbousCode`(承 [../p2-bridge/05](../p2-bridge/05-p3-p4-interface.md) §6 抽象,P3 实现 = wazero `CompiledModule` + `api.Function` 包装)。

一句话职责:**把 `Proto.Code` 翻译成 Wasm 函数,语义与 [../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) 解释器逐字节一致**。所有「正确性 vs 翻译」问题都按这条原则裁决——若翻译产物与解释器输出有差异(即使是浮点精度、NaN bit pattern、错误消息位置),那是翻译 bug 而非可接受偏差(差分口径见 [08-testing-strategy](./08-testing-strategy.md) §2,gibbous 不开任何豁免)。

本文不重抄 [../p1-interpreter/02](../p1-interpreter/02-bytecode-isa.md) 的 opcode 语义表,只给「翻译侧」逐 opcode 的 WAT 形态 + 与解释器同构的论证。本文也不展开跨层调用机制(归 [04-trampoline](./04-trampoline.md))与 IC 失效后的运行期机制(归 [06-ic-feedback-consume](./06-ic-feedback-consume.md))。

> **不变式提前**(本文 §8 详):翻译输出 vs 解释器逐字节同源 / NaN-box 编码两层逐位同一 / pc 物化让 traceback 一致 / `SupportsAllOpcodes` 保守缺省 / 编译失败 panic 不穿越本接口 / 单 Proto 失败原子性。

---

## 1. 翻译单位决策

### 1.1 三档候选与决策表

「翻译单位」= 一次 `P3Compiler.Compile` 调用产出的 Wasm 编译实体形态。三档候选:

| 候选 | 物理形态 | 优点 | 缺点 |
|---|---|---|---|
| **(A) 每 Proto 一 module**(P3 基线) | `Compile(proto)` → 一个 `wazero.CompiledModule`,内含一个导出的入口函数 `proto_N` | 隔离强(单 Proto 编译失败只 fallback 自己,§1.4 失败原子性);升层时机自然;不需要批次切分策略 | module 实例化有固定开销(数十微秒级,实测后定标);Proto 间直调需经 Go 中转(`gibbous→Go→gibbous`,两次跨层) |
| **(B) N 个热 Proto 合一 module**(优化项) | 一组同 Program 的热 Proto 攒成批次,合成一个 `CompiledModule`,内部互相 `call`/`call_indirect` 直调 | gibbous→gibbous 调用免 Go 往返(§5.3 跨层助手退化为同 module 内部 call);摊薄实例化开销 | 升层时机不同步(批次内一个 Proto 还没到热度也得等 batch);批次划分引入策略复杂度;一损俱损(批内任一 Proto 编译失败,整批 fallback) |
| (C) 整 Program 一 module(过激,放弃) | 程序所有 Lua 函数一次性编入一个 module | 极致内联;函数间直调 | Program 规模大时编译时间数十秒,首启动严重劣化;任一函数编译失败拖垮整 Program;升层无意义(全编无所谓「升」) |

### 1.2 基线决策:每 Proto 一 module

**P3 基线选 (A) 每 Proto 一 module**。论证:

1. **隔离强、失败局部化**:升层是 P2 决策机驱动的逐 Proto 事件([../p2-bridge/04](../p2-bridge/04-try-compile-fallback.md) §3 `considerPromotion` 是按 Proto 触发的)。基线方案让「升层的粒度」与「编译的粒度」对齐——单 Proto 编译失败时,只该 Proto 标 `TierStuck` 永久解释,兄弟 Proto 不受影响。这与 [../p2-bridge/04](../p2-bridge/04-try-compile-fallback.md) §3 的「TierStuck 是吸收态、单 Proto 决议」自洽,把方案 (B) 的「批次内一损俱损」从根上避免。
2. **正确性优先于性能**:与 [../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §2.2 选「基线 switch 解释器」同一哲学——每段路上选最稳的方案,等基线跑通再做优化项。P3 阶段 ≥2x 的验收门由「翻译消除 dispatch」与「IC 快照固化」兜底就够(../p3-wasm-tier §1 跨层摊销模型),不靠跨 Proto 内联。
3. **实例化开销可摊**:module 实例化的固定开销发生在升层那一刻(一次性),计入 P2 的编译预算([../p2-bridge/01-profiling](../p2-bridge/01-profiling.md) §5),不在热路径。列内核形状下,批量计算函数会被反复调用数千万次,实例化的几十微秒对总耗时贡献可忽略——前提是不在主循环里反复实例化(P3 决策机要保证一个 Proto 一旦升层就 cache 到 `Proto.gibbousCode` 不重复编)。
4. **升层时机自然**:Proto A 的热度先到、Proto B 后到时,基线方案可以 A 先升、B 后升,中间 A 调 B 仍走解释器(经 trampoline 退回 crescent,[04-trampoline](./04-trampoline.md) §3 gibbous→crescent 形态),没有「等 B 也热了再一起升」的同步问题。

**(B) 留作优化项,spike 后评估**。具体决策延后到 PW9 验收期(§1.3 渐进表的最后一档)——批量编译能否兑现「免 Go 往返」收益取决于实测 trampoline 跨层成本(§7.4)与同 Program 内函数互调密度(P2 [01-profiling](../p2-bridge/01-profiling.md) 应能给出)。当前不在 P3 基线开工范围内,记入文档缺口(§9)。

### 1.3 SupportsAllOpcodes 渐进白名单(P-Wasm 进度表)

`P3Compiler.SupportsAllOpcodes` 是 [../p2-bridge/03](../p2-bridge/03-compilability-analysis.md) §3.7 F7 闸门的实装——P3 后端开发期渐进扩充支持集,**保守缺省**(任何不在白名单的 opcode 一律返 false,触发 F7 拦截、Proto 留 `CompCompilable=false`、不进入升层路径)。

每个 P-Wasm 里程碑负责扩充一档支持集:

| PW | 新增 opcode 集合(累积) | 翻译复杂度峰 | 章节归属 |
|---|---|---|---|
| **PW1** | ∅(空集) | — | §5.2 SupportsAllOpcodes 永远返 false,验证「无 Proto 升层」与 P1-only 等价 |
| **PW2** | + MOVE / LOADK / LOADBOOL / LOADNIL / GETUPVAL / SETUPVAL / JMP | 直线翻译 | §3.1 |
| **PW3** | + ADD / SUB / MUL / DIV / MOD / POW / UNM / NOT / EQ / LT / LE / TEST / TESTSET | 双 number 快路径 + NaN 规范化 + 元方法慢路径助手 | §3.2 / §3.3 |
| **PW4** | + FORPREP / FORLOOP / TFORLOOP | 回边 safepoint(§3.5 + [05-safepoint-gc](./05-safepoint-gc.md) §3) | §3.5 |
| **PW5** | + GETTABLE / SETTABLE / GETGLOBAL / SETGLOBAL / SELF / NEWTABLE / LEN / CONCAT / SETLIST | IC 快照固化 + 失效降级走助手(翻译复杂度峰值) | §3.4 + [06-ic-feedback-consume](./06-ic-feedback-consume.md) |
| **PW6** | + CALL / TAILCALL / RETURN | 跨层互调协议 + status 链错误冒泡(经 [04-trampoline](./04-trampoline.md)) | §3.6 |
| **PW7** | + CLOSURE / CLOSE | 闭包 + 开放/关闭 upvalue(VARARG 已被 P2 F1 拦下,本步只验「不可达路径不被走到」) | §3.7 |
| PW8-PW9 | (全集 0..37 已支持,只剩 VARARG 的「不应到达」断言 + 验收测试) | — | [08-testing-strategy](./08-testing-strategy.md) |

> **保守缺省的实装表达**(§5.2 详):`Compiler.supported [numOps]bool` 数组初值全 false,每个 PW 在初始化里把对应 opcode 标 true。`SupportsAllOpcodes` 单遍扫 `proto.Code`,任一 `OpCode` 不在 supported 即返 false。**未识别 opcode 编号(38..63 预留区)统一返 false**——违反契约的 panic 不发生,因为 supported 数组按 numOps 长度建,越界视作 false。

`SupportsAllOpcodes` 的接口契约见 [../p2-bridge/05](../p2-bridge/05-p3-p4-interface.md) §2.2.1:O(N) 单遍扫、纯只读、不修改 Proto、不持久化任何状态、**不应 panic**(遇到无法识别的 opcode 编号也走保守拒)。本文 §5.2 的实装严格遵守。

### 1.4 失败原子性

「失败」覆盖两个事件:① F7 漏判——`SupportsAllOpcodes` 返 true 但 `Compile` 实际编不了某 opcode 子情况(罕见,记 issue 修 §1.3 白名单);② 后端 panic——P3 编译器内部 bug 触发(由 §5.5 defer recover 兜底转 `*CompileError(Kind=BackendPanic)`)。

不论哪种,**该 Proto 单独 fallback,不影响兄弟 Proto**。具体落地:

```go
// internal/gibbous/wasm/compile.go —— 错误返回路径(§5.5 完整骨架)
func (c *Compiler) Compile(proto *bytecode.Proto, fb *bridge.TypeFeedback) (gc bridge.GibbousCode, err error) {
    defer func() {
        if r := recover(); r != nil {
            // 后端 bug → 转 CompileError(Kind=BackendPanic),不让 panic 穿接口
            err = &bridge.CompileError{
                Kind:   bridge.CompileErrBackendPanic,
                Proto:  proto,
                Reason: fmt.Sprintf("p3 backend panic: %v\nstack: %s", r, debug.Stack()),
            }
            gc = nil
        }
    }()
    // ... 翻译主流程,见 §6.2
}
```

`P2.tryCompile`([../p2-bridge/04](../p2-bridge/04-try-compile-fallback.md) §5.2)看到 `err != nil` ⇒ 把该 Proto 转 `TierStuck`(吸收态,永久解释,不重试)。**TierStuck 是单 Proto 决议**,Program 内其它 Proto 仍可正常 considerPromotion。这是基线方案 (A) 隔离价值的体现:每 Proto 一 module 让失败粒度自然就是 Proto。

> 与 (B) 批量方案的对照:若批量编译里某 Proto 失败,整批 module 的所有 Proto 都得标 TierStuck(因为 module 没成功实例化,所有入口都不可用)——这是批量方案的「一损俱损」原罪,只有当批量收益(免 Go 往返)显著超过整批 fallback 损失时才值得开启。**P3 基线不开。**

---

## 2. 寄存器映射

P1 寄存器 `R(i)` 物理上是 thread 值栈槽 `stack[base+i]`(见 [../p1-interpreter/01](../p1-interpreter/01-value-object-model.md) §5.6 与 [../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §1.3)。Wasm 侧把 `R(i)` 放到哪里有两种候选:**linear memory 栈槽**(方案 A)与 **Wasm locals**(方案 B)。

### 2.1 候选方案 A vs B 的成本分析

| 方案 | 读写指令 | 访存成本(wazero 编译后) | GC 可见性 | 解释器可见性 | 跨层切换成本 |
|---|---|---|---|---|---|
| **(A) linear memory 栈槽**(基线) | `i64.load offset=8*i` / `i64.store offset=8*i` | 一次内存访问(wazero 编译为单条 load/store,与 Go 原生切片访问同档) | **天然共见**——值就在 thread 值栈,GC 根扫描复用 [../p1-interpreter/06](../p1-interpreter/06-memory-gc.md) §5.1 R5(running thread + CallInfo),零新增机制 | **天然共见**——同一字节偏移、同一 NaN-box 编码,无需翻译 | **零成本**——base 字节偏移就是入口入参 i32,trampoline 不物化任何寄存器值 |
| (B) Wasm locals | `local.get $r_i` / `local.set $r_i` | 寄存器级(wazero 编译为机器寄存器或 Wasm 栈槽,比 memory load 快若干 ns) | **不可见**——locals 在 wazero 自管栈上,Go GC / 望舒 GC 扫不到 | **不可见**——解释器读 `stack[base+i]` 与 locals 不同源 | **每边界一次写回 + 一次读取**——所有跨层调用前要把 locals 落回 memory 栈槽(safepoint 写回纪律,§2.4) |

### 2.2 基线决策:全 memory-resident

**P3 基线选 (A) 全 memory-resident**。这不是「单纯求稳」,是因为 (A) 在四个维度同时占优:

1. **GC 根天然共见**:[../p1-interpreter/06](../p1-interpreter/06-memory-gc.md) §5.1 列举的 5 个根集合(R1..R5),对 P3 而言**一字不改**。R5 = 「所有 thread 的 valueStack[0..top) + CallInfo 链」——gibbous 帧的活跃寄存器**就是** thread 值栈槽,根枚举代码不需要额外感知 P3 的存在。这是基线方案最大的正确性红利。
2. **byte-equal 差分零额外面**:解释器写 `stack[base+a]`,gibbous 写 `i64.store offset=8*a (local.get $base)`——同一字节偏移 + 同一 NaN-box u64 → 同一物理地址同一 bit pattern。差分口径([08-testing-strategy](./08-testing-strategy.md) §2)逐位比对,零脏槽风险。
3. **trampoline 协议退化为「传 base 偏移」**:跨层互调([04-trampoline](./04-trampoline.md) §1)只需传一个 i32 base,参数/返回值全经共见值栈传递,无寄存器物化、无影子副本。
4. **收益不靠寄存器提升,而靠消灭 dispatch**:解释器每条指令付「取指 + switch 间接跳 + 操作数位运算」([../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §2.1),编译后是**直线代码**,操作数是编译期立即数。这正是 [../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §2.2 说「closure-threading 是 P3 翻译的中间形态」的完全体——P3 翻译 = 直接发射「展开后的 closure-threading」,跳过中间形态。

memory-resident 的「访存比 locals 慢若干 ns」代价,在「消灭 dispatch + 编译期立即数操作数」面前微不足道。Wasm linear memory 在 wazero 实现里走的是 Go heap 上一段连续 bytes,L1/L2 cache 命中率与 Go slice 访问同档——只在算术稠密热点(没有内存访问的纯计算)上才显出 locals 优势,而那种形态在 Lua 寄存器机里不存在(寄存器值必入栈才能被下条指令读到)。

> Prior art 对照:HotSpot C2 的方法 JIT 也是「寄存器分配到机器寄存器」+「OOP map 帮 GC 找根」组合;**我们走 (A) 是因为没有 OOP map 这层基建**——P1 的 GC 是基于「root-set + arena 内偏移寻址」的简化模型([../p1-interpreter/06](../p1-interpreter/06-memory-gc.md) §1),把寄存器藏到不可见的 locals 里就破坏了根集合的简洁性。等到 P4 自管 codegen 阶段引入 stackmap,locals 才有用武之地([../p4-method-jit/04-osr-deopt](../p4-method-jit/04-osr-deopt.md) §3)。

### 2.3 (B) Wasm locals 缓存:作为受纪律的优化

(B) 不被基线采纳,但留作 spike 后的优化项,**只对循环局部热槽缓存**。最自然的候选:

- **FORLOOP 三槽**(idx/limit/step,即 R(A)/R(A+1)/R(A+2),[../p1-interpreter/02](../p1-interpreter/02-bytecode-isa.md) §6):FORPREP 已校验三槽为 number(失败则报错前置),进入 FORLOOP 后整个循环体内三槽是 number 不变(§3.5),可缓存到三个 `f64` locals,回边判界完全在 locals 上做。
- **R(A+3) 循环变量 v**:每次回边 `setReg(v, idx)` 后才被循环体读;若编译器静态分析循环体不读 v(纯递增),v 可不写回(进一步优化,PW9 未需——loop 核 memory-resident 已 2.58x 达标;留后续 locals 缓存类优化一并评估)。

非循环上下文不上 (B):一般直线代码里,每条指令产出会被随后指令读,locals 节省的只是「单次访存」,不值得引入写回纪律。

### 2.4 写回纪律(若启用 §2.3 优化)

缓存进 Wasm locals 的值对 GC 与解释器都不可见 ⇒ **任何 safepoint、任何跨层调用、任何可能读栈的助手调用之前,缓存的 locals 必须写回 memory 栈槽**。漏写回 = GC 误根 / 解释器读脏值,GC 压力 fuzz([../p1-interpreter/12](../p1-interpreter/12-testing-difftest.md) 「每分配即 full GC」模式)是主防线。

写回点由编译器**静态插入**,人工定位算法:

```
WriteBackPoints(emitContext):
  对循环体中的每条 opcode,扫描其翻译形态中是否含:
    1. 任一 imported 助手调用(§2.3 ADD 慢路径 / GETTABLE 失效降级 / NEWTABLE / CONCAT 等)
    2. 任一可能 safepoint 的回边或边界(FORLOOP 的 gcPending 检查、CALL 的跨层入口)
    3. 任一可能 throw error 的点(如 LEN 读非 string/table)
  在该点之前插入 WriteBackLocal(slot) 序列,把缓存的 locals 写回:
    例:f64.store offset=8*A (local.get $base) (local.get $idx_local)
```

实现期记号:`emitter.WriteBackBefore(callsite)` 是统一的 helper,所有 emit_<op> 函数在生成跨层调用前调它,避免遗漏。详见 §6.3 emit helper。

> 漏写回的捕获机制:差分 fuzz([08-testing-strategy](./08-testing-strategy.md) §2)是事后兜底,但成本高。开发期可在 P3 编译器加 lint:任何 emit 跨层调用的代码路径必须经 `WriteBackBefore` 包装,否则编译器自身 panic(开发期 assert,Release 编译期消除)。这是「让 bug 在最短时间内暴露」的工程纪律,与 [../p1-interpreter/12](../p1-interpreter/12-testing-difftest.md) 的 GC 压力 fuzz 互补。

---

## 3. Opcode 翻译表

本章是本文核心。逐 opcode 给出:① WAT 风格伪码(三反引号 `wat` 代码块);② 与 [../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) execute.go 的同构验证;③ 边角 case。

**全函数签名**(承 [04-trampoline](./04-trampoline.md) §2):

```wat
(func $proto_N (param $base i32) (result i32)
  (local $vb i64) (local $vc i64) (local $vt i64)
  (local $r f64) (local $idx f64) (local $limit f64) (local $step f64)
  (local $st i32)  ;; status from helper calls
  ;; 函数体由翻译器逐 opcode 拼出
)
```

`$base` = 本帧 R0 在 linear memory 的**字节偏移**(由 trampoline 入口传入,详见 [04-trampoline](./04-trampoline.md) §2)。返回 status:`0=OK / 1=ERR`(P3 永远不返回 2;2 是 P4 的 DEOPT,见 [../p2-bridge/05](../p2-bridge/05-p3-p4-interface.md) §6.1)。

A/B/C 是编译期常量,落为静态 offset 立即数——这正是 [../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §3.4 列举的「翻译 vs 解释」常数因子优势之一。

记号:
- `$base` 是字节偏移,寄存器 i 的字节偏移 = `8*i`(NaN-box u64 = 8 bytes)。
- `(local.get $base)` 是 Wasm 局部读;`offset=8*A` 是 wat memory access 的静态偏移立即数,等价 `addr = base + 8*A`。
- 「imported 助手」(§3 各处的 `$h_xxx`)= 从 Go 侧 import 进 wazero 的回调函数,具体注册见 §6.4。

### 3.1 直线 opcode(PW2)

#### 3.1.1 MOVE A B —— `R(A) := R(B)`

```wat
;; MOVE A B
(i64.store offset=8*A (local.get $base)
  (i64.load offset=8*B (local.get $base)))
```

- 一次 i64.load + 一次 i64.store,无装箱、无 dispatch、无 type 分支。
- 与解释器同构验证([../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) execute.go 51-52 行):`setReg(th, ci, A, reg(th, ci, B))` ≡ `stk[base+A] = stk[base+B]` ≡ 上面一对 load/store。NaN-box u64 整体复制,标志位/payload 全保留 → byte-equal 必然。
- **边角 case**:A==B(自赋值)生成无操作语义但仍发一对 load/store,wazero 不优化掉(我们也不需要——次数极少)。

#### 3.1.2 LOADK A Bx —— `R(A) := K(Bx)`

```wat
;; LOADK A Bx —— K(Bx) 是编译期常量,直接烧立即数
(i64.store offset=8*A (local.get $base)
  (i64.const <K[Bx].rawU64>))
```

- 关键设计:常量 `K[Bx]` 在编译期已知,**整个 NaN-box u64 烧成 i64 立即数**——既没有「读常量表 + load」的间接,也没有运行期类型分支。这是 [../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §3.4 「编译期立即数操作数」的最具代表性体现。
- 字符串常量的特殊:`K[Bx]` 若是 GC 字符串,其 NaN-box u64 = `MakeGC(TagString, gcRef)`,gcRef 是 arena 偏移——arena 在 P3 起就是 wazero memory([03-memory-model](./03-memory-model.md) §1.2),偏移在 Compile 时刻已固定;但若 `memory.grow` 后 backing 重分配,偏移本身不变(物理偏移寻址 grow 不破),无需重编([03-memory-model](./03-memory-model.md) §3.2)。
- 与解释器同构验证(execute.go 54-55 行):`setReg(th, ci, A, ci.proto.Consts[Bx])` ≡ 上面 store。常量值在 NewState 时一次性写入 Consts 数组,Compile 时刻读到的就是运行期该值,bit pattern 一致。
- **边角 case**:`K[Bx]` 若是 nil,其 raw u64 = `0xFFFE_xxxx_xxxx_xxxx`(取决于 NaN-box 编码,见 [../p1-interpreter/01](../p1-interpreter/01-value-object-model.md) §3),仍按 i64.const 烧入,与 LOADNIL 翻译效果等价。

#### 3.1.3 LOADBOOL A B C —— `R(A) := bool(B); if C ≠ 0 then pc++`

```wat
;; LOADBOOL A B C —— B 是布尔值(0/1),C ≠ 0 则跳过下一条指令
(i64.store offset=8*A (local.get $base)
  (i64.const <BoolValue(B != 0).rawU64>))
;; if C != 0:
;;   编译期已知,直接跳过翻译下一条 Instruction,在编译时把后续的 emit 挪到目标位置
```

- 跳过下一条指令的语义在编译期消化:翻译器扫到 `LOADBOOL A B 1` 就跳过下一条 Instruction 不翻译(或翻译后用 Wasm `br` 跳过)。这是编译器的静态控制流构造,运行期没有 pc++ 概念。
- 与解释器同构验证(execute.go 57-61 行):`setReg + if C != 0: ci.pc++`。编译期把这两件事合并成「写一个 Bool 到 R(A) + 静态跳过下一条」,语义等价。
- **边角 case**:LOADBOOL 通常用于条件表达式末尾的 `LOADBOOL A 0 1; LOADBOOL A 1 0` 模式(把比较结果转布尔)。编译器识别这种成对模式时可以特化,但基线实装按通用规则翻译就行,wazero 自身的 dead-code 优化会清理。

#### 3.1.4 LOADNIL A B —— `R(A..B) := nil`(闭区间)

```wat
;; LOADNIL A B —— A..B 全置 nil
(i64.store offset=8*A     (local.get $base) (i64.const <NilValue.rawU64>))
(i64.store offset=8*(A+1) (local.get $base) (i64.const <NilValue.rawU64>))
;; ... 逐槽 store,直到 R(B)
(i64.store offset=8*B     (local.get $base) (i64.const <NilValue.rawU64>))
```

- 编译期展开为 (B-A+1) 条 store。区间通常很小(2..5 槽),展开成本可控。
- 与解释器同构验证(execute.go 63-67 行):`for r := a; r <= b; r++ { setReg(... value.Nil) }` ≡ 上面展开形态。
- **优化项**:大区间(>16 槽)可改用 `memory.fill`(Wasm bulk memory proposal),wazero 已支持。但这种大区间在实际 Lua 代码罕见,基线展开就行。

#### 3.1.5 GETUPVAL A B —— `R(A) := Upval(B)`

```wat
;; GETUPVAL A B —— B 是当前 closure 的 upvalue 索引
;; closure 的 upvalue 数组在 arena 内,需经助手定位(因为 closure 自身住 arena,
;; 偏移寻址跨过本帧栈 → 直发 i64.load 不行,需要先经 arena 视图取 closure 偏移)
(local.set $vb (call $h_getupval (local.get $base) (i32.const B)))
(i64.store offset=8*A (local.get $base) (local.get $vb))
```

- `$h_getupval` 是 imported 助手:`func(base int32, b int32) uint64`,内部 `object.ClosureUpvalRef(arena, ci.cl, b)` + `upvalGet(th, uv)`(execute.go 70-71 行的 Go 侧实装)。
- 为什么走助手:upvalue 可能是「开放」(指向某 thread 的栈槽)或「关闭」(指向 arena 内单独存储)状态,运行期才能区分;开放 upvalue 的目标可能在另一个 thread 的栈上,涉及多 thread 解析,不适合内联到 Wasm。
- 与解释器同构验证(execute.go 69-71 行):`uv := object.ClosureUpvalRef(...); setReg(... st.upvalGet(uv))` ≡ 助手调用的内部行为。助手返回值就是 NaN-box u64,store 到 R(A) 完成同构。
- **优化项**:稳定 closure(不会再 close upvalue 的)的 upvalue 可以在 Compile 时刻物化偏移,直发 i64.load offset=<closed_upval_offset>。但这要求闭包稳定性证明,基线不开,留 P4。

#### 3.1.6 SETUPVAL A B —— `Upval(B) := R(A)`

```wat
;; SETUPVAL A B —— 写当前 closure 的 upvalue B
(local.set $vb (i64.load offset=8*A (local.get $base)))
(call $h_setupval (local.get $base) (i32.const B) (local.get $vb))
```

- `$h_setupval` 是 imported 助手:`func(base int32, b int32, val uint64)`,内部 `object.ClosureUpvalRef + st.upvalSet`(execute.go 73-75 行的 Go 侧实装)。
- 同步写屏障:upvalue 的目标若是 arena 表/字符串,P1 的写屏障接口空实现([../p1-interpreter/06](../p1-interpreter/06-memory-gc.md) §9.4),P3 不动,所以助手内不做额外屏障——只把 NaN-box u64 写到 upvalue 目标位置。增量 GC 引入后这里需补屏障(P3 之后单独评估)。
- 与解释器同构验证(execute.go 73-75 行):同 GETUPVAL 对称。

#### 3.1.7 JMP sBx —— `pc += sBx`

```wat
;; JMP sBx —— 静态控制流跳转
;; 编译期目标 pc = pc_now + 1 + sBx 已知,翻译为 br/br_if 到对应基本块
;; 向后跳(sBx < 0)是回边,需 safepoint 检查;向前跳无 safepoint
(if (i32.lt_s (i32.const sBx) (i32.const 0))   ;; 编译期常量条件,实际不发指令
  (then
    ;; 回边 safepoint:gcPending 检查 + onBackEdge 通知
    (if (i32.load (global.get $gcPending))
      (then (call $h_safepoint (local.get $base) (i32.const PC))))
    ;; (P3 不调 OnBackEdge —— 那是 P1 解释器的 profile 入口,
    ;;  gibbous 帧已升层,不再做热度计数,见 §3.5)
    (br $L_target)))
;; (else 向前跳直接 br,无 safepoint)
(br $L_target)
```

- 关键设计:**控制流在编译期解决**——sBx 是编译期常量,目标 pc 已知,翻译器为 Proto.Code 每个跳转目标建一个 Wasm 基本块标签 `$L_<pc>`,JMP 翻译为 `br $L_<target>`。运行期没有 pc 算术。
- 回边 safepoint:回边是循环回跳,gcPending 检查在此触发(承 [05-safepoint-gc](./05-safepoint-gc.md) §3 三类 safepoint 之一)。FORLOOP 是热回边的主要形态,JMP 向后跳通常用于 while/repeat 循环。
- 与解释器同构验证(execute.go 201-210 行):`if SBx(i) < 0: preempt + OnBackEdge; ci.pc += SBx(i)`。
  - `preempt`(异步抢占)→ wazero 自管(roadmap §2 税二「已验证」),P3 不发等价指令([00-overview](./00-overview.md) §8 第二行),编译产物借用 wazero 回边检查点。
  - `OnBackEdge`(热度计数)→ gibbous 帧已升层,**不再计热**(详见 §3.5 论证)。
  - `ci.pc += SBx(i)` → 静态 br,运行期不需要写 pc;但 traceback 仍需 pc 物化到 CallInfo.savedPC,见 §4。
- **边角 case**:`JMP 0`(原地跳)在 codegen 中通常不出现,但若出现,翻译为无操作 + 回边 safepoint(因 sBx=0 ≥ 0 实际是「向前跳 0 步」,无回边)。

### 3.2 算术 opcode(PW3)—— ADD/SUB/MUL/DIV/MOD/POW/UNM

#### 3.2.1 ADD A B C —— `R(A) := RK(B) + RK(C)`(双 number 快路径 + NaN 规范化)

```wat
;; ADD A B C —— 标准翻译形态(feedback 无关版本)
(local.set $vb <load_RK_B>)   ;; <load_RK_B> = i64.load offset=8*B (base) 或 i64.const K[B-256]
(local.set $vc <load_RK_C>)
(if (i32.and  ;; IsNumber(vb) && IsNumber(vc):NaN-box 单比较(01 §3.2)
      (i64.lt_u (local.get $vb) (i64.const 0xFFF8000000000000))
      (i64.lt_u (local.get $vc) (i64.const 0xFFF8000000000000)))
  (then
    ;; 快路径:f64 加法
    (local.set $r (f64.add (f64.reinterpret_i64 (local.get $vb))
                           (f64.reinterpret_i64 (local.get $vc))))
    ;; canonicalizeNaN(01 §3.4):任何 NaN 结果统一为 canonical NaN bit pattern,
    ;; 否则与解释器侧 value.NumberValue 的规范化逻辑产生 bit 差异
    (if (f64.ne (local.get $r) (local.get $r))   ;; NaN 自身不等于自身
      (then (local.set $r (f64.reinterpret_i64 (i64.const 0x7FF8000000000000)))))
    (i64.store offset=8*A (local.get $base) (i64.reinterpret_f64 (local.get $r)))
    ;; 算术 IC 双计数 numHits++(02 §6.4):此处为 P2 写不读 feedback 通道
    ;; (gibbous 帧的 IC 写入是「白 写」——P2 已聚合完 feedback 不再读;
    ;;  但维持写入是为了保持「失败回退到解释器」时 IC 状态连续。诊断用,可关 build tag)
    ;; (call $h_record_arith_num (i32.const PC))   ;; 编译选项 P3_RECORD_IC_HITS
  )
  (else
    ;; 慢路径:imported 助手回 Go 走 doArithSlow(coercion / __add 元方法)
    (local.set $st (call $h_arith
                          (local.get $base)
                          (i32.const PC)
                          (i32.const OP_ADD)
                          (i32.const B)
                          (i32.const C)
                          (i32.const A)))
    (br_if $err (i32.eq (local.get $st) (i32.const 1)))))
```

- 关键设计:快路径是**语义分发非投机 guard**(承 ../p3-wasm-tier §3.1 + [06-ic-feedback-consume](./06-ic-feedback-consume.md) §1)——`IsNumber(vb) && IsNumber(vc)` 是 [../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §4.1 解释器快路径的同款判定,失败走慢路径助手得到正确结果,**不存在 deopt**。
- NaN 规范化是 byte-equal 必备:解释器侧 `value.NumberValue(r)` 内部对所有 NaN 强制为 canonical bit pattern(01 §3.4),Wasm 侧 `f64.add` 产生的 NaN 可能不是 canonical(IEEE 允许多种 NaN bit pattern,wazero 也不保证标准化),必须显式规范化。这条「显式规范化 NaN」是 §8 不变式 1 的具体执行点。
- IsNumber 的 NaN-box 实装:[../p1-interpreter/01](../p1-interpreter/01-value-object-model.md) §3.2 定 `IsNumber(v) ⇔ v < 0xFFF8_0000_0000_0000`(因为 NaN-box 把非 number 编入 0xFFF8 起的 NaN 高 bits),Wasm 侧用 `i64.lt_u` 直接对应。
- `<load_RK_B>` 占位的处理:RK 在编译期已知是寄存器还是常量(02 §2.1 RK 标志位)——若是寄存器(B<256),发 `i64.load offset=8*B`;若是常量(B≥256),发 `i64.const <K[B-256].rawU64>`。这是「编译期消化 RK 解码」的优势。
- `$h_arith` 助手签名:`func(base int32, pc int32, op int32, rkb int32, rkc int32, dst int32) int32`,返回 0=OK / 1=ERR。Go 侧实装就是 execute.go 的 `doArithSlow` 复用——参数包含 op 让一个助手覆盖 ADD/SUB/MUL/DIV/MOD/POW 六个 opcode,代码量小且一致性强。
- 与解释器同构验证(execute.go 141-148 行 + doArith 410-438 行):
  - `value.IsNumber(b) && value.IsNumber(c)` 与 Wasm `i64.lt_u && i64.lt_u` 同款判定。
  - `setReg(... value.NumberValue(r))` ≡ Wasm 的 store + 显式 canonicalize。
  - 慢路径通过助手回 Go 跑 `doArithSlow`,完全复用解释器逻辑——同构是天然的。

#### 3.2.2 SUB / MUL / DIV / MOD / POW —— 同 ADD 形态

各自的快路径只换 f64 操作:

```
SUB:  (f64.sub vb vc)
MUL:  (f64.mul vb vc)
DIV:  (f64.div vb vc)         ;; /0 = ±Inf,IEEE 同款
MOD:  Lua 语义 a - floor(a/b)*b,需展开:
      (local.set $q (f64.floor (f64.div vb vc)))
      (local.set $r (f64.sub vb (f64.mul $q vc)))
POW:  没有 wasm 直发指令,需经助手 $h_pow:
      (local.set $r (call $h_pow vb vc))   ;; 内部 math.Pow
      或 wazero 的 pow intrinsic(若有);basleline 走助手最简
```

慢路径的 `$h_arith` 助手用 op 参数分流,无新 helper。

- DIV 的特殊:`x/0` 在 Lua 语义返回 ±Inf(02 §4 注),IEEE 浮点除法直接得到 ±Inf,无须额外处理。NaN(0/0)经 §3.2.1 的规范化兜住。
- MOD 的特殊:Lua 5.1 用 `a - floor(a/b)*b` 而非 C 的 `fmod`(差异在负数取模符号,见 [../p1-interpreter/02](../p1-interpreter/02-bytecode-isa.md) §4 OpCode MOD 注)。Wasm 翻译用 `f64.floor + f64.mul + f64.sub` 三步,与解释器 execute.go 427 行 `r = x - math.Floor(x/y)*y` 同款。
- POW 不直发:Wasm 没有 `f64.pow` 指令;wazero 提供 `math.Pow` 经 imported intrinsic 调,基线就走 `$h_pow`(`func(x, y f64) f64`,内部 `math.Pow`)。同构性靠 Go math.Pow 与解释器 doArith 的 math.Pow 是同一个函数自然成立(execute.go 429 行)。

#### 3.2.3 UNM A B —— `R(A) := -R(B)`

```wat
;; UNM A B —— 一元负
(local.set $vb (i64.load offset=8*B (local.get $base)))
(if (i64.lt_u (local.get $vb) (i64.const 0xFFF8000000000000))   ;; IsNumber
  (then
    (local.set $r (f64.neg (f64.reinterpret_i64 (local.get $vb))))
    ;; UNM 不需要 canonicalizeNaN —— f64.neg 只翻 sign bit,不产生新 NaN
    (i64.store offset=8*A (local.get $base) (i64.reinterpret_f64 (local.get $r))))
  (else
    ;; 慢路径:string coercion + __unm 元方法
    (local.set $st (call $h_unm
                          (local.get $base)
                          (i32.const PC)
                          (i32.const B)
                          (i32.const A)))
    (br_if $err (i32.eq (local.get $st) (i32.const 1)))))
```

- 与解释器同构(execute.go 149-176 行 UNM 段):`IsNumber(b) → -value.AsNumber(b)` ≡ Wasm `f64.neg`;否则走 `toNumberCoerce` + `__unm` ≡ 助手。
- `f64.neg` 不会产生新 NaN(只翻符号位 bit 63),所以不需要 canonicalize。-NaN ≠ NaN 在 bit 层面,但 [../p1-interpreter/01](../p1-interpreter/01-value-object-model.md) §3.4 的 canonicalize 也不对一元负做强制——所以 Wasm 侧不做规范化才是 byte-equal。

#### 3.2.4 NOT A B —— `R(A) := not R(B)`(无 metamethod)

```wat
;; NOT A B —— 真值取反,无慢路径
(local.set $vb (i64.load offset=8*B (local.get $base)))
;; Truthy(v):v != Nil && v != False(01 §3.3)
(local.set $vt (i32.and
                 (i64.ne (local.get $vb) (i64.const <NilValue.rawU64>))
                 (i64.ne (local.get $vb) (i64.const <BoolValue(false).rawU64>))))
;; not Truthy → BoolValue(!truthy)
(if (i32.eqz (local.get $vt))
  (then (i64.store offset=8*A (local.get $base) (i64.const <BoolValue(true).rawU64>)))
  (else (i64.store offset=8*A (local.get $base) (i64.const <BoolValue(false).rawU64>))))
```

- 直线翻译,无元方法路径(NOT 在 Lua 5.1 不触发任何元方法)。
- 与解释器同构(execute.go 178-180 行):`setReg(... BoolValue(!Truthy(b)))`,Truthy 的两个比较与 Wasm `i64.ne` 双判定同源。
- **优化项**:可改为「Truthy 单 i32 + select」更紧凑;基线易读优先。

#### 3.2.5 LEN A B —— `R(A) := #R(B)`

```wat
;; LEN A B —— string len / table border / 异类报错
(local.set $vb (i64.load offset=8*B (local.get $base)))
;; tag = (vb >> 47) & 0xF —— NaN-box tag 提取(01 §3.1)
(local.set $vt (i32.and
                 (i32.wrap_i64 (i64.shr_u (local.get $vb) (i64.const 47)))
                 (i32.const 0xF)))
(block $done
  ;; case TagString
  (if (i32.eq (local.get $vt) (i32.const TagStringID))
    (then
      (local.set $r (f64.convert_i32_u (call $h_string_len (local.get $vb))))
      (i64.store offset=8*A (local.get $base) (i64.reinterpret_f64 (local.get $r)))
      (br $done)))
  ;; case TagTable
  (if (i32.eq (local.get $vt) (i32.const TagTableID))
    (then
      (local.set $r (f64.convert_i32_u (call $h_table_border (local.get $vb))))
      (i64.store offset=8*A (local.get $base) (i64.reinterpret_f64 (local.get $r)))
      (br $done)))
  ;; default:报错
  (local.set $st (call $h_err_len (local.get $base) (i32.const PC) (i32.const B)))
  (br $err))
```

- LEN 不带 IC(02 §1.2 注 2),三分支按 NaN-box tag 直接拣选。
- 与解释器同构(execute.go 182-193 行):TagString → `object.StringLen`、TagTable → `st.rawBorder`、default → 报错。Wasm 侧三个 case 通过助手回 Go 完成具体计算,helper 实装直接复用 execute.go 同款代码。
- **为什么 string_len / table_border 走助手而不内联**:string 长度需要读 arena 内的 string header(`StringLen` 实现细节),table border 需要在 array 段做二分查找——这两个的 Wasm 内联翻译过于复杂(且边角较多),走助手更稳。spike 验证 helper 跨层 < 50ns 后基本无收益空间。

#### 3.2.6 CONCAT A B C —— `R(A) := R(B) .. ... .. R(C)`(右结合)

```wat
;; CONCAT A B C —— 全经助手(基线最简)
(local.set $st (call $h_concat
                      (local.get $base)
                      (i32.const PC)
                      (i32.const A)
                      (i32.const B)
                      (i32.const C)))
(br_if $err (i32.eq (local.get $st) (i32.const 1)))
;; CONCAT 在 P1 后随 safepoint(因可能分配大字符串,execute.go 195-199 行);
;; 助手内部已收口 safepoint(在 Go 侧的 doConcat 末尾调 st.safepoint),Wasm 侧无需重复
```

- CONCAT 不带 IC(02 §1.2 注 2),全 string 还是混合类型快路径都没显著差异(都走线性折叠),内联翻译收益小。
- 与解释器同构(execute.go 195-199 行):整个 `doConcat` 逻辑住 Go 侧助手,直接复用,同构成立。
- **优化项**:稳态全 string 的二元 CONCAT(`a..b`)可能值得内联——但实际负载里 CONCAT 通常是字符串拼装路径(多个操作数),内联收益不大。基线全走助手。

### 3.3 比较 opcode(PW3)—— EQ/LT/LE

比较指令在 Lua 5.1 编码为「比较 + 条件跳过下一条 JMP」对(02 §9 不变式 3):`EQ/LT/LE` 后必跟 `JMP`,解释器对它们做「条件 pc++ 跳过 JMP」。P3 翻译时把这两条合并成「比较 → 选择跳转目标」的 Wasm 控制流。

#### 3.3.1 LT A B C —— `if (RK(B) < RK(C)) ≠ bool(A) then pc++`

```wat
;; LT A B C —— 双 number 快路径 + string 比较 + __lt 元方法
(local.set $vb <load_RK_B>)
(local.set $vc <load_RK_C>)
(if (i32.and  ;; IsNumber × 2
      (i64.lt_u (local.get $vb) (i64.const 0xFFF8000000000000))
      (i64.lt_u (local.get $vc) (i64.const 0xFFF8000000000000)))
  (then
    ;; 快路径:f64 比较,结果是 i32 (0/1)
    (local.set $vt (f64.lt (f64.reinterpret_i64 (local.get $vb))
                           (f64.reinterpret_i64 (local.get $vc)))))
  (else
    ;; 慢路径助手:string 比较 / __lt 元方法 / 报错。
    ;; 助手返回 packed:bit0=比较结果(0/1),bit1=错误标志
    (local.set $st (call $h_compare
                          (local.get $base)
                          (i32.const PC)
                          (i32.const OP_LT)
                          (i32.const B)
                          (i32.const C)))
    (br_if $err (i32.and (local.get $st) (i32.const 2)))   ;; bit1 = error
    (local.set $vt (i32.and (local.get $st) (i32.const 1)))))   ;; bit0 = result
;; 「比较 + JMP」合并:res != bool(A) 时 pc++ 跳过 JMP,否则执行 JMP
;; 编译期 bool(A) 已知,后随 JMP 的目标已知 → 两个分支静态确定
(if (i32.ne (local.get $vt) (i32.const <A != 0 ? 1 : 0>))
  (then (br $L_after_jmp))   ;; pc++ 跳过下一条 JMP → 落到 JMP 之后
  (else (br $L_jmp_target)))  ;; 执行后随 JMP → 跳到 JMP 的目标
```

- 关键设计:比较是**语义分发非投机**(承 §3.2 同理)——双 number 走 f64 快路径,其它走助手得到正确结果(string 比较 / __lt 元方法 / 报错),零 deopt。
- 「比较 + JMP」合并是 P3 控制流翻译的精华:解释器是「比较算 res → 条件 pc++ → 下条 JMP 执行/跳过」三步两指令(execute.go 247-249 行);P3 在编译期已知后随 JMP 的目标与 bool(A),把三步压成一个 `if (res != boolA) br else br`——零运行期 pc 算术、零额外取指。
- string 比较走助手而非内联:string 字典序比较需读 arena 内字符串字节做逐字节比较(execute.go 537-543 行 `stringCompare`),内联翻译复杂,走助手更稳。
- 与解释器同构(execute.go 212-249 行 + doCompare 507-590 行):
  - `value.IsNumber(b) && value.IsNumber(c)` → f64 比较,与 Wasm `f64.lt` 同款。
  - 否则 `st.doCompare` → 助手回 Go,完全复用解释器逻辑。
  - `res != (A != 0) → ci.pc++` → Wasm 的 `if (vt != boolA) br $L_after_jmp`。

#### 3.3.2 LE A B C —— `if (RK(B) <= RK(C)) ≠ bool(A) then pc++`

同 LT 形态,快路径换 `f64.le`,助手 op 参数换 `OP_LE`:

```wat
;; LE 快路径
(local.set $vt (f64.le (f64.reinterpret_i64 (local.get $vb))
                       (f64.reinterpret_i64 (local.get $vc))))
```

- LE 的元方法慢路径含「`__le` 缺失则回退 `not __lt(c,b)`」的 5.1 特有逻辑(execute.go 557-580 行),全在助手内处理,Wasm 侧无感。
- 与解释器同构:同 LT。

#### 3.3.3 EQ A B C —— `if (RK(B) == RK(C)) ≠ bool(A) then pc++`

```wat
;; EQ A B C —— 双 number 快路径 + raw 相等 + __eq 元方法
(local.set $vb <load_RK_B>)
(local.set $vc <load_RK_C>)
(if (i32.and  ;; IsNumber × 2:数字必须走浮点比较(canonNaN bits 相等但 NaN≠NaN)
      (i64.lt_u (local.get $vb) (i64.const 0xFFF8000000000000))
      (i64.lt_u (local.get $vc) (i64.const 0xFFF8000000000000)))
  (then
    ;; 双 number:f64.eq(处理 NaN≠NaN、+0==-0)
    (local.set $vt (f64.eq (f64.reinterpret_i64 (local.get $vb))
                           (f64.reinterpret_i64 (local.get $vc)))))
  (else
    ;; 非双 number:先 raw bit 相等(同 GCRef / 同 bool / 同 nil)
    (if (i64.eq (local.get $vb) (local.get $vc))
      (then (local.set $vt (i32.const 1)))
      (else
        ;; raw 不等 → 可能 __eq(仅两 table 且元方法同一函数,5.1 语义)
        (local.set $st (call $h_eq
                              (local.get $base) (i32.const PC)
                              (i32.const B) (i32.const C)))
        (br_if $err (i32.and (local.get $st) (i32.const 2)))
        (local.set $vt (i32.and (local.get $st) (i32.const 1)))))))
;; 比较 + JMP 合并(同 §3.3.1)
(if (i32.ne (local.get $vt) (i32.const <A != 0 ? 1 : 0>))
  (then (br $L_after_jmp))
  (else (br $L_jmp_target)))
```

- EQ 的双 number 必须用 `f64.eq` 而非 `i64.eq`:NaN-box 把 canonical NaN 编成固定 bit pattern,`i64.eq` 会判 `NaN == NaN` 为 true(bit 相等),但 IEEE 语义 `NaN ≠ NaN`;且 `+0` 与 `-0` 的 bit pattern 不同但 `+0 == -0`。`f64.eq` 正确处理两者。这是 execute.go 592-598 行 `rawEqual` 的「数字先走浮点比较」注释的 Wasm 对应。
- 非双 number 时,`i64.eq`(raw bit 相等)处理同 GCRef / 同 bool / 同 nil(因为这些 NaN-box u64 相等当且仅当语义相等),raw 不等再走 `__eq` 助手(仅两 table 且元方法同一函数,execute.go 517-527 行)。
- EQ 不带 IC(02 §1.2 注 1),所以无 numHits 记录。
- 与解释器同构(execute.go 212-249 行 EQ 分支 + doCompare 511-528 行):双 number → f64.eq;否则 rawEqual + __eq 助手。**注意**:execute.go 的 EQ 也走 doCompare(212 行的 case 含 EQ),但快路径只对 LT/LE 内联;P3 把 EQ 的双 number 也内联(因为 f64.eq 是单指令),这不改变语义只是更紧凑——同构仍成立(双 number 的 f64.eq 结果与 doCompare 的 rawEqual 对双 number 走 `AsNumber(a)==AsNumber(b)` 完全一致)。

#### 3.3.4 TEST A C —— `if Truthy(R(A)) ≠ bool(C) then pc++`

```wat
;; TEST A C —— 真值测试(and/or 短路),后随 JMP
(local.set $vb (i64.load offset=8*A (local.get $base)))
(local.set $vt (i32.and   ;; Truthy(R(A))
                 (i64.ne (local.get $vb) (i64.const <NilValue.rawU64>))
                 (i64.ne (local.get $vb) (i64.const <BoolValue(false).rawU64>))))
;; Truthy(R(A)) != bool(C) → pc++ 跳过 JMP
(if (i32.ne (local.get $vt) (i32.const <C != 0 ? 1 : 0>))
  (then (br $L_after_jmp))
  (else (br $L_jmp_target)))
```

- 与解释器同构(execute.go 251-255 行):`Truthy(reg(A)) != (C != 0) → ci.pc++`,Wasm 同款。
- TEST 无慢路径(纯真值判定),全内联。

#### 3.3.5 TESTSET A B C —— `if Truthy(R(B)) == bool(C) then R(A):=R(B) else pc++`

```wat
;; TESTSET A B C —— 条件赋值(and/or 取值)
(local.set $vb (i64.load offset=8*B (local.get $base)))
(local.set $vt (i32.and   ;; Truthy(R(B))
                 (i64.ne (local.get $vb) (i64.const <NilValue.rawU64>))
                 (i64.ne (local.get $vb) (i64.const <BoolValue(false).rawU64>))))
(if (i32.eq (local.get $vt) (i32.const <C != 0 ? 1 : 0>))
  (then  ;; 真值匹配 → R(A) := R(B),落到 JMP 执行
    (i64.store offset=8*A (local.get $base) (local.get $vb))
    (br $L_jmp_target))
  (else  ;; 不匹配 → pc++ 跳过 JMP
    (br $L_after_jmp)))
```

- 与解释器同构(execute.go 257-263 行):`if Truthy(reg(B)) == (C != 0): setReg(A, b) else ci.pc++`,Wasm 同款。
- TESTSET 无慢路径,全内联。

### 3.4 表 IC opcode(PW5)—— GETTABLE/SETTABLE/GETGLOBAL/SETGLOBAL/SELF

这是 **P3 翻译复杂度峰值**(承 [00-overview](./00-overview.md) §5 人月分解 PW5 = 1-2 人月)。核心机制:**编译期把 IC 快照固化进代码,运行期同表同代次直达槽,失效降级走助手**。本节给翻译形态;IC 失效后的运行期机制(降级语义、是否重编译)在 [06-ic-feedback-consume](./06-ic-feedback-consume.md) 展开。

#### 3.4.1 IC 快照固化的来源

P1 解释器的 IC slot 是运行期可变的(mono IC 重填,[../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §6.3 + ic.go 的 `icGetTable` 回填逻辑);gibbous 把**编译时刻的快照**烧进代码。快照内容来自 `Proto.IC[pc]`(bytecode/proto.go 的 `ICSlot` 结构):

```go
// internal/bytecode/proto.go —— ICSlot(P1 已落地形态)
type ICSlot struct {
    Shape    uint32 // 表 IC:目标表 gen 代次
    Index    uint32 // 表 IC:命中槽位下标(array 下标 / node 槽号)
    TableRef uint32 // 表 IC:目标表 arena 偏移低 32 位(身份比对)
    Kind     uint8  // 0 None / 1 ArrayHit / 2 NodeHit / 3 MonoMeta / 4 Megamorphic
    Refill   uint8  // 换表/换形重填计数(P2 megamorphic 主动识别)
}
```

编译期读 `Proto.IC[pc]` 取 `(Shape, Index, TableRef, Kind)`,作为 WAT 立即数固化。若该点 `Kind == ICKindNone`(从未命中过)或 P2 feedback 标 `FBTableMega`(megamorphic),则**不固化快照,直接发助手路径**(无快路径)——见 §3.4.4。

> **P2 feedback 的可选消费**:`P3Compiler.Compile` 入参带 `*TypeFeedback`(可能 nil)。若 feedback 在该 pc 标 `FBTableMono`,P3 用 `stableShape/stableIndex` 固化快照(更可信的快照源,因为是 P2 聚合后的稳定值,而非单次 IC slot 读取)。若 feedback 标 `FBTableMega`,P3 跳过快路径直发助手。feedback 为 nil 时退化为直接读 `Proto.IC[pc]`(承 [05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md) §2 「实现方必须容忍 nil」)。详见 [06-ic-feedback-consume](./06-ic-feedback-consume.md) §1。

#### 3.4.2 GETTABLE A B C —— `R(A) := R(B)[RK(C)]`(IC 快照内联)

```wat
;; GETTABLE A B C —— 固化快照 = 编译时 IC slot 的 (SNAP_TABLEREF, SNAP_GEN, SNAP_KIND, SNAP_INDEX)
(local.set $vt (i64.load offset=8*B (local.get $base)))   ;; 表对象
(local.set $vc <load_RK_C>)                               ;; 键
(if (i32.and
      ;; ① IsTable(vt):tag == TagTable(01 §3.1)
      (call $is_table (local.get $vt))
      (i32.and
        ;; ② 同表:vt 的 arena 偏移低 32 位 == 固化 SNAP_TABLEREF
        (i32.eq (call $gcref_low32 (local.get $vt)) (i32.const SNAP_TABLEREF))
        ;; ③ 同代次:vt 的 gen == 固化 SNAP_GEN(失效检测:rehash/setmetatable bump gen)
        (i32.eq (call $table_gen (local.get $vt)) (i32.const SNAP_GEN))))
  (then
    ;; 快路径:同表同代次。但还要校验 key 匹配(同一 pc 可能轮换不同 key,
    ;; 典型循环 t[i];见 ic.go icGetTable 的 "键不同退慢路径" 注释)
    ;; SNAP_KIND 决定 array 还是 node 校验:
    ;;   ArrayHit: 校验 arrayIndex(key) == SNAP_INDEX
    ;;   NodeHit:  校验 NodeKey(SNAP_INDEX) == key
    (if (call $ic_key_match  ;; SNAP_KIND/SNAP_INDEX 固化为助手立即数
              (local.get $vt) (local.get $vc)
              (i32.const SNAP_KIND) (i32.const SNAP_INDEX))
      (then
        ;; 命中:直达槽取值;但若槽值是 nil 仍要退慢路径(可能 __index)
        (local.set $vb (call $ic_slot_load (local.get $vt)
                             (i32.const SNAP_KIND) (i32.const SNAP_INDEX)))
        (if (i64.ne (local.get $vb) (i64.const <NilValue.rawU64>))
          (then
            (i64.store offset=8*A (local.get $base) (local.get $vb))
            (br $L_gettable_done))))))
  )
;; miss / 形状变了 / key 不匹配 / 槽值 nil ⇒ 完整查找 + __index 元方法
(local.set $st (call $h_gettable
                      (local.get $base) (i32.const PC)
                      (i32.const A) (i32.const B) (i32.const C)))
(br_if $err (i32.eq (local.get $st) (i32.const 1)))
(block $L_gettable_done)
```

- 关键设计:快路径检查链是「IsTable + 同表 + 同代次 + 同键」,与 ic.go `icGetTable` 22-47 行的 IC 命中校验**完全同款**——这是**语义分发非投机**(承 ../p3-wasm-tier §3.1),失败走 `$h_gettable` 助手得到正确结果(完整查找 + 回填 IC + __index 链),零 deopt。
- 三层校验对应 ic.go 的字段比对:
  - `slot.TableRef == uint32(t)` → Wasm `gcref_low32(vt) == SNAP_TABLEREF`。
  - `slot.Shape == object.TableGen(arena, t)` → Wasm `table_gen(vt) == SNAP_GEN`。
  - array hit: `arrayIndex(key) == slot.Index`;node hit: `keyEqual(NodeKey(slot.Index), key)` → Wasm `$ic_key_match`。
- 「槽值 nil 退慢路径」对应 ic.go 33-36 行 / 43-46 行的 `if v != value.Nil` 守卫——槽位虽命中但值为 nil 时,Lua 语义要查 `__index`(可能 metatable 有此键),故退慢路径。Wasm 侧 `if (i64.ne vb nil)` 守卫同款。
- `$h_gettable` 助手:`func(base int32, pc int32, a int32, b int32, c int32) int32`,内部 `tbl := reg(B); key := rk(C); v, e := st.icGetTable(...); setReg(A, v)`——完全复用 execute.go 98-108 行的 GETTABLE 段。助手内的 `icGetTable` 也会回填 IC slot(运行期 IC 持续更新),但 gibbous 固化的快照不变(失效后该点永久走助手 ≈ 解释器无 IC,正确但慢,[06-ic-feedback-consume](./06-ic-feedback-consume.md) §1)。
- 与解释器同构(execute.go 98-108 行 + ic.go 22-73 行):快路径 = IC 命中校验同款;慢路径 = 助手回 Go 跑 `icGetTable`,同构天然。

#### 3.4.3 SETTABLE A B C —— `R(A)[RK(B)] := RK(C)`(IC 快照内联)

```wat
;; SETTABLE A B C —— 改值快路径(改值不 bump gen,IC 持续有效;ic.go icSetTable 77-101 行)
(local.set $vt (i64.load offset=8*A (local.get $base)))   ;; 表对象
(local.set $vb <load_RK_B>)                               ;; 键
(local.set $vc <load_RK_C>)                               ;; 值
(if (i32.and
      (i64.ne (local.get $vc) (i64.const <NilValue.rawU64>))  ;; 删除(置 nil)走慢路径
      (i32.and (call $is_table (local.get $vt))
        (i32.and
          (i32.eq (call $gcref_low32 (local.get $vt)) (i32.const SNAP_TABLEREF))
          (i32.eq (call $table_gen (local.get $vt)) (i32.const SNAP_GEN)))))
  (then
    ;; 校验 key 匹配 + 当前槽值非 nil(改已存在键,ic.go 88/97 行守卫)
    (if (call $ic_key_match_nonnil
              (local.get $vt) (local.get $vb)
              (i32.const SNAP_KIND) (i32.const SNAP_INDEX))
      (then
        ;; 命中:直接写槽
        (call $ic_slot_store (local.get $vt)
              (i32.const SNAP_KIND) (i32.const SNAP_INDEX) (local.get $vc))
        (br $L_settable_done)))))
;; miss / 删除 / 新增键 ⇒ 完整写路径(__newindex 链)+ 回填 IC
(local.set $st (call $h_settable
                      (local.get $base) (i32.const PC)
                      (i32.const A) (i32.const B) (i32.const C)))
(br_if $err (i32.eq (local.get $st) (i32.const 1)))
(block $L_settable_done)
;; SETTABLE 后随 safepoint(execute.go 119 行,可能 rehash 分配);
;; 助手内已收口 safepoint(快路径改值不分配不需要)
```

- 关键设计:SETTABLE 的 IC 快路径只覆盖「改已存在键的值」(改值不 bump gen,IC 持续有效,ic.go 77 行注释)。删除(置 nil)、新增键(可能 rehash → bump gen)都走慢路径。
- `vc != nil` 守卫对应 ic.go 83 行 `if val != value.Nil`(删除走慢路径,可能 rehash 语义)。
- `$ic_key_match_nonnil` 比 GETTABLE 的 `$ic_key_match` 多一个「当前槽值非 nil」校验,对应 ic.go 88 行 `if object.TableArrayAt(...) != value.Nil` / 97 行 `object.NodeVal(...) != value.Nil`——改值快路径要求键已存在(槽值非 nil)。
- 快路径无 safepoint:改值不分配不 rehash,无 GC 触发可能(execute.go 119 行的 safepoint 在助手内,快路径跳过)。这是 [05-safepoint-gc](./05-safepoint-gc.md) §3 「分配点 safepoint」在 P3 的精确兑现——只在真正可能分配的助手内 safepoint。
- 与解释器同构(execute.go 110-119 行 + ic.go 77-127 行):快路径 = IC 改值命中同款;慢路径 = 助手回 Go 跑 `icSetTable`。

#### 3.4.4 GETGLOBAL / SETGLOBAL —— globals 表的特例

GETGLOBAL/SETGLOBAL 是「目标表恒为 globals」的表访问特例(02 §4):

```wat
;; GETGLOBAL A Bx —— R(A) := Gtable[K(Bx)]
;; 与 GETTABLE 同构,但表对象恒为 globals(编译期已知 globals 的 GCRef)
;; key 是常量 K(Bx)(编译期已知),所以 key 校验可省(同一 pc 的 key 恒定)
(local.set $vt (i64.const <globals.rawU64>))   ;; globals 表恒定,直接烧 NaN-box u64
(if (i32.and
      (i32.eq (call $gcref_low32 (local.get $vt)) (i32.const SNAP_TABLEREF))
      (i32.eq (call $table_gen (local.get $vt)) (i32.const SNAP_GEN)))  ;; 同代次校验失效
  (then
    ;; 命中:globals 恒为 node hit(globals 无 array 段;02-ic-feedback §2.2)
    (local.set $vb (call $ic_slot_load (local.get $vt)
                         (i32.const ICKindNodeHit) (i32.const SNAP_INDEX)))
    (if (i64.ne (local.get $vb) (i64.const <NilValue.rawU64>))
      (then
        (i64.store offset=8*A (local.get $base) (local.get $vb))
        (br $L_getglobal_done)))))
;; miss(globals rehash → bump gen)/ 槽 nil ⇒ 助手
(local.set $st (call $h_getglobal
                      (local.get $base) (i32.const PC)
                      (i32.const A) (i32.const Bx)))
(br_if $err (i32.eq (local.get $st) (i32.const 1)))
(block $L_getglobal_done)
```

- 与 GETTABLE 的差异:
  - 表对象恒为 globals(编译期烧 `globals.rawU64` 立即数,无 `i64.load`)。
  - key 是常量 `K(Bx)`(编译期已知),同一 pc 的 key 恒定,**可省 key 校验**(GETTABLE 因 RK(C) 动态而必须校验)。
  - globals 恒为 node hit(globals 表无 array 段,02-ic-feedback §2.2),`SNAP_KIND` 恒为 `ICKindNodeHit`。
- globals 失效:新增全局键触发 globals rehash → bump gen → 快照永久 miss → 该点走助手。这与 ic.go 的 globals IC 失效语义一致。
- SETGLOBAL 对称(改已存在全局键走快路径;新增键 rehash 走慢路径)。execute.go 88-96 行 GETGLOBAL/SETGLOBAL 经 `icGetTable`/`icSetTable` 复用,Wasm 助手同样复用。
- SETGLOBAL 后随 safepoint(execute.go 96 行),助手内收口。

#### 3.4.5 SELF A B C —— `R(A+1) := R(B); R(A) := R(B)[RK(C)]`(方法调用优化)

```wat
;; SELF A B C —— obj:m() 优化:先把 obj 放 R(A+1),再做与 GETTABLE 同构的方法查找
(local.set $vt (i64.load offset=8*B (local.get $base)))
;; ① R(A+1) := R(B)(obj 自身,作为方法的 self 参数)
(i64.store offset=8*(A+1) (local.get $base) (local.get $vt))
;; ② R(A) := R(B)[RK(C)](与 GETTABLE 同构的 IC 快照查找)
(local.set $vc <load_RK_C>)
(if (i32.and (call $is_table (local.get $vt))
      (i32.and
        (i32.eq (call $gcref_low32 (local.get $vt)) (i32.const SNAP_TABLEREF))
        (i32.eq (call $table_gen (local.get $vt)) (i32.const SNAP_GEN))))
  (then
    (if (call $ic_key_match (local.get $vt) (local.get $vc)
              (i32.const SNAP_KIND) (i32.const SNAP_INDEX))
      (then
        (local.set $vb (call $ic_slot_load (local.get $vt)
                             (i32.const SNAP_KIND) (i32.const SNAP_INDEX)))
        (if (i64.ne (local.get $vb) (i64.const <NilValue.rawU64>))
          (then
            (i64.store offset=8*A (local.get $base) (local.get $vb))
            (br $L_self_done))))))
  )
(local.set $st (call $h_self
                      (local.get $base) (i32.const PC)
                      (i32.const A) (i32.const B) (i32.const C)))
(br_if $err (i32.eq (local.get $st) (i32.const 1)))
(block $L_self_done)
```

- SELF 的第一步 `R(A+1) := R(B)` 必须在 IC 查找之前(execute.go 129-131 行先 `setReg(A+1, tbl)`),因为 IC 查找可能写 R(A),不能覆盖 obj。注意 A+1 与 A 是不同槽,无冲突。
- 第二步与 GETTABLE 完全同构(execute.go 129-139 行的 SELF 段就是「setReg(A+1) + icGetTable」)。方法常驻 metatable 时命中率极高(02-ic-feedback §2.3),是 IC 快照固化收益最大的点。
- 与解释器同构(execute.go 129-139 行):`setReg(A+1, tbl); v, e := st.icGetTable(...); setReg(A, v)`,Wasm 两步同款。

#### 3.4.6 NEWTABLE A B C —— `R(A) := {}`(经助手分配)

```wat
;; NEWTABLE A B C —— 分配新表,B/C 是 array/hash 预分配大小(int2fb 编码)
;; gibbous 代码自身从不分配(00-overview §8;分配 + GC 都在助手内),全经助手
(local.set $vb (call $h_newtable (local.get $base) (i32.const B) (i32.const C)))
(i64.store offset=8*A (local.get $base) (local.get $vb))
;; NEWTABLE 后随 safepoint(execute.go 127 行);助手内收口(分配即可能触发 collect)
```

- NEWTABLE 带「预分配」语义但不带 IC——它创建新表无 IC 价值。
- 关键设计:**gibbous 代码自身从不分配**(承 [00-overview](./00-overview.md) §8 + [05-safepoint-gc](./05-safepoint-gc.md) §3)。所有分配(NEWTABLE/CONCAT/CLOSURE/rehash)经 imported 助手回 Go,分配与 GC 都在助手内同步完成(同 P1 的 Alloc 内同步 collect,[../p1-interpreter/06](../p1-interpreter/06-memory-gc.md) §8.2),助手返回时 GC 已完成,Wasm 侧无感(memory-resident 寄存器使根天然可见)。
- `$h_newtable` 助手:`func(base int32, b int32, c int32) uint64`,内部 `asz := Fb2Int(b); hsz := Fb2Int(c); t := st.allocTable(asz, roundUpPow2(hsz)); return MakeGC(TagTable, t)`——复用 execute.go 121-127 行。
- 与解释器同构(execute.go 121-127 行):助手内完全复用,同构天然。

#### 3.4.7 SETLIST A B C —— 表构造批量填数组

```wat
;; SETLIST A B C —— R(A)[(C-1)*FPF + i] := R(A+i), i=1..B(FPF=50)
;; B=0 到 top;C=0 取下一指令为大批次号
(local.set $st (call $h_setlist
                      (local.get $base) (i32.const PC)
                      (i32.const A) (i32.const B) (i32.const C)))
(br_if $err (i32.eq (local.get $st) (i32.const 1)))
;; SETLIST 后随 safepoint(execute.go 376 行,批量写可能 rehash);助手内收口
```

- SETLIST 全经助手:批量写涉及多槽写入 + 可能 rehash + 多值边界(B=0 到 top),内联翻译复杂且收益低(表构造不是热循环主体),走助手最简。
- C=0 取下一指令为大批次号的特殊:助手内读 `Proto.Code[pc]` 的下一条作为批次号(execute.go 的 `doSetList` 处理),Wasm 侧只传 pc 让助手自取。
- 与解释器同构(execute.go 372-376 行):助手内复用 `doSetList`,同构天然。

### 3.5 控制流(PW4)—— FORPREP/FORLOOP/TFORLOOP

数值 for 占 4 个连续寄存器 R(A..A+3):idx/limit/step/v(02 §6)。FORPREP 校验三槽为 number 并预减一个 step;FORLOOP 加 step、判界、回跳并刷新 v。

#### 3.5.1 FORPREP A sBx —— `R(A) -= R(A+2); pc += sBx`(三槽校验)

```wat
;; FORPREP A sBx —— 准备数值 for,校验三槽为 number(可经 string coercion)
;; 三槽校验有失败可能(报错),走助手保证 byte-equal 错误消息
(local.set $st (call $h_forprep
                      (local.get $base) (i32.const PC) (i32.const A)))
(br_if $err (i32.eq (local.get $st) (i32.const 1)))
;; 校验通过 + 预减完成(助手内做 R(A) := init-step,三槽规范化为 number)
;; 静态跳到 FORLOOP(sBx 编译期已知)
(br $L_forloop)
```

- 为什么 FORPREP 走助手:三槽校验含 string coercion(5.1 对 for 也做 tonumber,execute.go 326-328 行 `toNumberCoerce`)与三种报错(`'for' initial value/limit/step must be a number`,execute.go 329-337 行)。这些错误消息要 byte-equal,走助手复用解释器逻辑最稳。校验只在循环入口执行一次,非热路径,助手开销可忽略。
- 助手内规范化三槽:`$h_forprep` 把三槽都转成 number(coercion 后)并写回 `R(A):=init-step, R(A+1):=limit, R(A+2):=step`——这样进入 FORLOOP 后三槽**保证是 number**,FORLOOP 快路径无须再校验类型(§3.5.2 的红利)。
- 与解释器同构(execute.go 323-341 行):助手内完全复用 FORPREP 段,同构天然。
- 控制流:FORPREP 后必跳到对应 FORLOOP(02 §8 示例 `FORPREP R2 -> L1`),编译期目标已知,翻译为 `br $L_forloop`。

#### 3.5.2 FORLOOP A sBx —— 数值 for 回边(热点)

```wat
;; FORLOOP A sBx —— 热回边。三槽已被 FORPREP 保证 number(§3.5.1),快路径全 f64
($L_forloop)
;; idx += step(三槽全 number,直发 f64,无类型校验)
(local.set $idx (f64.add (f64.reinterpret_i64 (i64.load offset=8*A     (local.get $base)))
                         (f64.reinterpret_i64 (i64.load offset=8*(A+2) (local.get $base)))))
(local.set $limit (f64.reinterpret_i64 (i64.load offset=8*(A+1) (local.get $base))))
(local.set $step  (f64.reinterpret_i64 (i64.load offset=8*(A+2) (local.get $base))))
;; 方向敏感判界:step >= 0 → idx <= limit;step < 0 → idx >= limit
;; step 是编译期常量时(常见,如 for i=1,n do)可特化为单比较
(if (if (result i32) (f64.ge (local.get $step) (f64.const 0))
      (then (f64.le (local.get $idx) (local.get $limit)))
      (else (f64.ge (local.get $idx) (local.get $limit))))
  (then  ;; continue:写回 idx,刷新 v,回跳
    (i64.store offset=8*A     (local.get $base) (i64.reinterpret_f64 (local.get $idx)))
    (i64.store offset=8*(A+3) (local.get $base) (i64.reinterpret_f64 (local.get $idx)))
    ;; 回边 safepoint:仅检查标志,一次 i32.load + 几乎恒不跳的分支(05 §3)
    (if (i32.load (global.get $gcPending))
      (then (call $h_safepoint (local.get $base) (i32.const PC))))
    ;; (P3 不调 OnBackEdge:gibbous 帧已升层,不再热度计数)
    (br $L_body)))   ;; sBx 编译期已知 → 回跳到循环体起点
;; 不 continue:落到 FORLOOP 之后(退出循环)
```

- 关键红利:**三槽已被 FORPREP 保证 number**(execute.go 323-341 行 FORPREP 把三槽都规范化为 number),FORLOOP 快路径直发 f64 算术与比较,**零类型校验**——这是数值 for 编译后的最大常数因子优势。execute.go 299-321 行 FORLOOP 段也是直接 `value.AsNumber`(不校验,因 FORPREP 已保证)。
- 方向敏感判界:`step >= 0 → idx <= limit` / `step < 0 → idx >= limit`,与 execute.go 306-310 行 `if step >= 0 { cont = idx <= limit } else { cont = idx >= limit }` 同款。
- **step 编译期常量特化**:绝大多数数值 for 是 `for i=1,n do`(step=1)或 `for i=n,1,-1 do`(step=-1),step 是常量。此时 `f64.ge step 0` 是编译期可判定的,翻译器直接发对应方向的单比较(`f64.le` 或 `f64.ge`),不发 step 符号判断分支。这是 §9 文档缺口提到的「locals 缓存槽选择」之外的另一个特化点。
- 回边 safepoint:gcPending 检查覆盖「循环体内经助手分配置了 pending、但 collect 被推迟」的回收时机(承 [05-safepoint-gc](./05-safepoint-gc.md) §3 + 对齐 execute.go 的 opcode 末尾检查)。一次 `i32.load` + 几乎恒不跳的分支,开销极小。
- 异步抢占借 wazero:execute.go 312 行的 `preempt()` 在 P3 不发等价指令(wazero 生成码回边已有抢占检查点,roadmap §2 税二「已验证」)。我们的 gcPending 检查是另一回事(自己 GC 的事),与 wazero 抢占互不相干、各管各的([00-overview](./00-overview.md) §8 第二行)。
- **OnBackEdge 不调的论证**:execute.go 318-320 行在解释器 FORLOOP 回边调 `st.bridge.OnBackEdge(proto, pc)` 做热度计数。但 gibbous 帧是**已升层**的——热度计数的目的是「决定该不该升层」,已升层的 Proto 再计热没有意义(它已经是 gibbous 了,不会「再升」,P3 无更高层)。所以 P3 翻译**不发 OnBackEdge 调用**。这与 ../p3-wasm-tier §2.3 FORLOOP 示例的省略一致。
  - 边角考量:若未来 P4 引入「gibbous → fullmoon 再升层」,热度计数可能要恢复——但那是 P4/P5 的事,P3 范围内 gibbous 是顶层,不计热。记入 §9 文档缺口的相关项(IC 快照失效重编译评估时一并考虑)。
- locals 缓存优化点(§2.3):FORLOOP 三槽(idx/limit/step)是 locals 缓存的首选——上面伪码已经把它们 load 到 `$idx/$limit/$step` locals,若启用 §2.3 优化,这三个 locals 跨迭代保持(不每次 load/store memory),只在回边写回 idx/v 到 memory(GC 可见性纪律,§2.4)。基线先全 memory-resident(每次 load),优化项 spike 后定。
- 与解释器同构(execute.go 299-321 行):idx+=step / 方向判界 / 写回 idx+v / 回跳,逐句同款。

#### 3.5.3 TFORLOOP A C —— 泛型 for(经助手)

```wat
;; TFORLOOP A C —— 调用迭代器 R(A)(R(A+1), R(A+2)),结果落 R(A+3..A+2+C)
;; 涉及函数调用(迭代器),全经助手(跨层调用归 04-trampoline)
(local.set $st (call $h_tforloop
                      (local.get $base) (i32.const PC)
                      (i32.const A) (i32.const C)))
;; 助手返回:0=继续(首值非 nil,控制变量已更新),1=ERR,3=退出循环(首值 nil)
(br_if $err (i32.eq (local.get $st) (i32.const 1)))
(if (i32.eq (local.get $st) (i32.const 3))
  (then (br $L_after_jmp))   ;; 首值 nil → 跳过回边 JMP,退出循环
  (else (br $L_jmp_target))) ;; 首值非 nil → 执行回边 JMP,继续循环
```

- TFORLOOP 经 imported 助手:它调用迭代器函数(execute.go 343-370 行 `callLuaFromHost(iter, [state, ctrl])`),涉及跨层调用(迭代器可能是 Lua closure / host 函数),全经助手回 Go 处理。跨层调用的具体形态归 [04-trampoline](./04-trampoline.md)(迭代器若是 gibbous 编译过的,助手内再 trampoline 进 Wasm)。
- 助手返回三态:0=继续 / 1=ERR / 3=退出(首值 nil)。对应 execute.go 365-370 行:首值非 nil → `setReg(A+2, results[0])` 落到回边 JMP;首值 nil → `ci.pc++` 跳过回边退出循环。Wasm 侧用 status 区分两种控制流。
- TFORLOOP 不带 IC(02 §1.2 注 3),是控制流指令。
- 与解释器同构(execute.go 343-370 行):助手内完全复用,同构天然;控制流(继续/退出)经 status 映射到 Wasm 分支。

### 3.6 调用(PW6)—— CALL/TAILCALL/RETURN

调用是跨层互调的核心,具体协议(crescent↔gibbous↔host 三路分派、status 链错误冒泡、参数/返回值经共见值栈)归 [04-trampoline](./04-trampoline.md)。本节只给翻译形态 + 链至 04。

#### 3.6.1 CALL A B C —— `R(A)(R(A+1..A+B-1))`(经调度助手)

```wat
;; CALL A B C —— 统一经调度助手(基线;按被调者分派详见 04-trampoline §3)
(local.set $st (call $h_call
                      (local.get $base) (i32.const PC)
                      (i32.const A) (i32.const B) (i32.const C)))
(br_if $err (i32.eq (local.get $st) (i32.const 1)))
;; status=0:返回值已回填 R(A..A+C-2)(共见值栈,04 §2),直线继续
```

- 关键设计:CALL 统一经 `$h_call` 调度助手,助手内按被调者分派(详见 [04-trampoline](./04-trampoline.md) §3):
  - 被调是 gibbous(已编译):Go 侧再 `fn.Call`(基线两次跨层);优化是同 module 内 `call_indirect` 直调(§1.2 批量编译时)。
  - 被调是 crescent(未编译/TierStuck):`vm.execute` 跑该帧(05 §7.3 fresh reentry),返回后回 Wasm。
  - 被调是 host fn:`callHost`(05 §7.6)原样。
- 参数/返回值全经共见值栈:CALL 约定参数在 R(A+1..A+B-1)、返回回填 R(A..A+C-2)(02 §3),这些都是值栈槽,gibbous 与助手共见同一份,**跨层只传 base**(承 [04-trampoline](./04-trampoline.md) §2)。B=0/C=0 的多值边界由助手内维护 top(02 §9 不变式 4)。
- 错误传播:`$h_call` 返回非 0 ⇒ Wasm 函数 `br_if $err` 自身返回非 0 ⇒ 上层继续冒泡(status 链,[04-trampoline](./04-trampoline.md) §4)。
- 与解释器同构(execute.go 265-277 行):`next, e := st.doCall(...)`,助手内复用 `doCall`,同构天然。

#### 3.6.2 TAILCALL A B C —— 尾调用(复用帧)

```wat
;; TAILCALL A B C —— 尾调用,复用当前帧(栈不增长)
(local.set $st (call $h_tailcall
                      (local.get $base) (i32.const PC)
                      (i32.const A) (i32.const B) (i32.const C)))
(br_if $err (i32.eq (local.get $st) (i32.const 1)))
;; TAILCALL 的帧复用语义在助手内处理;Wasm 侧 status=0 后通常直接 RETURN
;; (因为 TAILCALL 是函数最后一条调用,见 02 §8 注)
```

- TAILCALL 复用当前帧(02 §4),帧管理在助手内做(`doTailCall`,execute.go 279-289 行)。从 gibbous 帧发起的尾调用,被调者可能是另一个 gibbous/crescent/host——具体跨层语义归 [04-trampoline](./04-trampoline.md)。
- 与解释器同构(execute.go 279-289 行):助手内复用 `doTailCall`。

#### 3.6.3 RETURN A B —— 返回 `R(A..A+B-2)`

```wat
;; RETURN A B —— 返回值回填到调用者期望的位置,然后函数返回 status=0
;; gibbous 函数的「返回」= 把返回值按 nresults 回填 + return 0
(local.set $st (call $h_return
                      (local.get $base) (i32.const PC)
                      (i32.const A) (i32.const B)))
;; h_return 内部:按 nresults 把 R(A..A+B-2) 搬到调用者期望槽,弹本帧逻辑
;; 返回值 status 即本 gibbous 函数的最终 result
(return (local.get $st))
```

- RETURN 是 gibbous 函数的出口:把返回值按 `nresults`(调用者期望个数,CallInfo word2)回填到调用者帧,然后本 Wasm 函数 `return status`。
- 关键:gibbous 函数的 Wasm `return` 与 Lua RETURN 是两层概念——Lua RETURN 触发「返回值回填 + 弹 CallInfo」,这在助手内做;Wasm `return` 是把 status 交回 trampoline(crescent 的 `fn.Call` 接到 status 后弹 CallInfo,[04-trampoline](./04-trampoline.md) §2)。
- B=0 到 top 的多值边界由助手内维护(02 §9 不变式 4)。
- 与解释器同构(execute.go 291-297 行):`doReturn` 处理返回值回填与帧弹出;P3 把「弹本帧」交给 trampoline,「返回值回填」交给助手,合起来与 `doReturn` 同构(详见 [04-trampoline](./04-trampoline.md) §2 的 RETURN 协议)。

### 3.7 闭包(PW7)—— CLOSURE/CLOSE/VARARG

#### 3.7.1 CLOSURE A Bx —— `R(A) := closure(Proto[Bx])`(后随伪指令)

```wat
;; CLOSURE A Bx —— 创建闭包,后随 nupvals 条伪指令(MOVE/GETUPVAL)描述 upvalue 捕获
;; 闭包构造涉及分配 + upvalue 捕获,全经助手(分配在助手内,00-overview §8)
(local.set $vb (call $h_closure
                      (local.get $base) (i32.const PC) (i32.const A) (i32.const Bx)))
(i64.store offset=8*A (local.get $base) (local.get $vb))
;; CLOSURE 后随 safepoint(execute.go 384 行,分配);助手内收口
;; 后随的 nupvals 条伪指令(MOVE/GETUPVAL)由编译期解析,不作为独立 opcode 翻译
;; (它们在 codegen 里是 CLOSURE 的附属数据,描述每个 upvalue 从哪捕获)
```

- 关键设计:CLOSURE 后随 `nupvals` 条伪指令(MOVE/GETUPVAL,02 §4 CLOSURE 注),这些伪指令**不是独立 opcode**,而是 CLOSURE 的附属数据,描述每个 upvalue 从「父帧栈槽」(MOVE)还是「父 closure 的 upvalue」(GETUPVAL)捕获。
- 编译期解析:翻译器扫到 CLOSURE 时,读后随 `nupvals` 条伪指令作为 upvalue 捕获描述符,**整体交给 `$h_closure` 助手**(传 pc,助手自取后随伪指令)——助手内 `makeClosure`(execute.go 381-383 行)处理 upvalue 捕获(开放/关闭语义)。翻译器跳过这 `nupvals` 条伪指令不单独翻译(它们已被 CLOSURE 消化)。
- 为什么全经助手:upvalue 捕获涉及「开放 upvalue 链」管理([../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §8.3),且闭包分配走 arena,内联翻译过于复杂(开放/关闭 upvalue 协议是工程难点,[00-overview](./00-overview.md) §5 PW7 = 0.5-1 人月)。走助手最稳。
- 与解释器同构(execute.go 381-384 行):助手内复用 `makeClosure`,同构天然。
- **upvalue 编译协议的具体形态待定**:开放/关闭 upvalue 在 wazero linear memory 形态下的具体存储(upvalue cell 在 arena 哪里、开放 upvalue 如何指向栈槽)留 PW7 实测后定标,记入 §9 文档缺口。

#### 3.7.2 CLOSE A —— 关闭所有 ≥ R(A) 的开放 upvalue

```wat
;; CLOSE A —— 作用域退出,关闭所有 ≥ R(A) 的开放 upvalue
(call $h_close (local.get $base) (i32.const A))
```

- CLOSE 经助手 `closeUpvals(th, base+A)`(execute.go 378-379 行)——把所有栈位 ≥ base+A 的开放 upvalue 关闭(值从栈槽拷到 upvalue cell)。开放 upvalue 链管理在 Go 侧,走助手。
- CLOSE 无返回值无错误(纯状态操作),助手返回 void。
- 与解释器同构(execute.go 378-379 行):助手内复用 `closeUpvals`,同构天然。

#### 3.7.3 VARARG A B —— `R(A..A+B-2) := ...`(不可达路径)

```wat
;; VARARG A B —— 已被 P2 F1 拦下(03-compilability-analysis F1 排除 vararg 函数)
;; gibbous 函数永远不会含 VARARG(因为含 VARARG 的 Proto 是 vararg 函数,F1 不让升层)
;; 本步只验「不可达路径不被走到」:翻译器遇到 VARARG 应当 panic
;; (开发期 assert:若 SupportsAllOpcodes 放行了含 VARARG 的 Proto,是 §1.3 白名单 bug)
(unreachable)   ;; Wasm unreachable 指令:若运行期到达此处则 trap(永不应到达)
```

- 关键:VARARG **不应到达 gibbous**。含 VARARG 的 Proto 是 vararg 函数(`IsVararg`),被 [../p2-bridge/03](../p2-bridge/03-compilability-analysis.md) §3 的 F1 闸门排除(vararg 函数不可编译)。所以 gibbous 函数永远不含 VARARG。
- 翻译器的防御:`SupportsAllOpcodes` 应当对含 VARARG 的 Proto 返 false(§5.2 白名单本就不含 VARARG,自然返 false)。万一 F1 漏判 + 白名单 bug 双重失效让含 VARARG 的 Proto 进了 Compile,翻译器遇到 VARARG 应当 panic(开发期 assert),由 §5.5 defer recover 兜底转 `CompileError(BackendPanic)`,该 Proto fallback。
- 运行期防御:即便编译产物含 VARARG 翻译(理论不应发生),用 Wasm `unreachable` 指令——运行期到达即 trap(wazero 把 trap 转 Go error,经 GibbousCode.Run 返 status=1)。双重保险确保「不可达路径不被走到」(承 [00-overview](./00-overview.md) §4 PW7 验收)。
- VARARG 不带 IC(02 §1.2 注 3)。

### 3.8 其它 opcode 翻译形态小结

§3.1-§3.7 已覆盖全部 0..37 opcode。本节按 opcode 编号给一张「翻译形态速查表」,把分散在各节的形态归总(便于实现期对照):

| # | opcode | 翻译形态 | 快路径 | 慢路径助手 | 章节 |
|---|---|---|---|---|---|
| 0 | MOVE | 直线 load/store | — | — | §3.1.1 |
| 1 | LOADK | 烧 i64.const 立即数 | — | — | §3.1.2 |
| 2 | LOADBOOL | 烧 const + 静态跳过 | — | — | §3.1.3 |
| 3 | LOADNIL | 展开多 store | — | — | §3.1.4 |
| 4 | GETUPVAL | 经助手 | — | `$h_getupval` | §3.1.5 |
| 5 | SETUPVAL | 经助手 | — | `$h_setupval` | §3.1.6 |
| 6 | GETGLOBAL | IC 快照(globals 特例) | 同表同代次直达 | `$h_getglobal` | §3.4.4 |
| 7 | SETGLOBAL | IC 快照(globals 特例) | 改值直达 | `$h_setglobal` | §3.4.4 |
| 8 | GETTABLE | IC 快照内联 | 同表同代次同键直达 | `$h_gettable` | §3.4.2 |
| 9 | SETTABLE | IC 快照内联 | 改值直达 | `$h_settable` | §3.4.3 |
| 10 | NEWTABLE | 经助手(分配) | — | `$h_newtable` | §3.4.6 |
| 11 | SELF | setReg(A+1) + IC 快照 | 同表同代次同键直达 | `$h_self` | §3.4.5 |
| 12-17 | ADD/SUB/MUL/DIV/MOD/POW | 双 number 快路径 + NaN 规范化 | f64 算术 | `$h_arith`(op 分流) | §3.2.1/§3.2.2 |
| 18 | UNM | f64.neg 快路径 | f64.neg | `$h_unm` | §3.2.3 |
| 19 | NOT | 直线真值取反 | Truthy 双比较 | — | §3.2.4 |
| 20 | LEN | tag 分支 + 助手计算 | tag 拣选 | `$h_string_len`/`$h_table_border`/`$h_err_len` | §3.2.5 |
| 21 | CONCAT | 经助手 | — | `$h_concat` | §3.2.6 |
| 22 | JMP | 静态 br(回边 safepoint) | — | `$h_safepoint`(回边) | §3.1.7 |
| 23 | EQ | 双 number f64.eq + raw eq | f64.eq / i64.eq | `$h_eq`(__eq) | §3.3.3 |
| 24 | LT | 双 number f64.lt | f64.lt | `$h_compare`(op 分流) | §3.3.1 |
| 25 | LE | 双 number f64.le | f64.le | `$h_compare`(op 分流) | §3.3.2 |
| 26 | TEST | 直线真值测试 | Truthy 双比较 | — | §3.3.4 |
| 27 | TESTSET | 直线条件赋值 | Truthy 双比较 | — | §3.3.5 |
| 28 | CALL | 经调度助手 | — | `$h_call`(三路分派) | §3.6.1 |
| 29 | TAILCALL | 经助手 | — | `$h_tailcall` | §3.6.2 |
| 30 | RETURN | 经助手 + Wasm return | — | `$h_return` | §3.6.3 |
| 31 | FORLOOP | f64 判界回边(热点) | 三槽 number 直发 f64 | `$h_safepoint`(回边) | §3.5.2 |
| 32 | FORPREP | 经助手(三槽校验) | — | `$h_forprep` | §3.5.1 |
| 33 | TFORLOOP | 经助手(迭代器调用) | — | `$h_tforloop` | §3.5.3 |
| 34 | SETLIST | 经助手 | — | `$h_setlist` | §3.4.7 |
| 35 | CLOSE | 经助手 | — | `$h_close` | §3.7.2 |
| 36 | CLOSURE | 经助手(分配 + upvalue) | — | `$h_closure` | §3.7.1 |
| 37 | VARARG | unreachable(不可达) | — | — | §3.7.3 |

**翻译形态分三类**:
- **直线内联**(MOVE/LOADK/LOADBOOL/LOADNIL/NOT/TEST/TESTSET/JMP):无慢路径,纯 Wasm 指令展开,最大常数因子优势。
- **快慢分叉**(ADD 系列/UNM/EQ/LT/LE/GETTABLE/SETTABLE/GETGLOBAL/SETGLOBAL/SELF/LEN):快路径内联(语义分发非投机),慢路径经助手回 Go。
- **全经助手**(GETUPVAL/SETUPVAL/NEWTABLE/CONCAT/CALL/TAILCALL/RETURN/FORPREP/TFORLOOP/SETLIST/CLOSE/CLOSURE):涉及分配/跨层调用/复杂状态,内联收益低或复杂度过高,全交助手。

> **设计一致性**:所有「快慢分叉」的快路径判定都是**语义分发非投机 guard**(承 ../p3-wasm-tier §3.1)——与解释器快路径同款判定,失败走慢路径助手得到正确结果,零 deopt。这是 §8 不变式 1 的统一执行原则,跨所有 opcode 一致。

## 4. pc 物化协议

### 4.1 直线代码无运行期 pc

P3 翻译产物是**直线代码**(§2.2、§3.1.7),没有「pc 寄存器」概念——控制流是 Wasm 基本块跳转,操作数是编译期立即数。但 Lua 语义需要 pc:错误定位(`chunkname:line:` 前缀,[../p1-interpreter/09](../p1-interpreter/09-errors-pcall.md))、traceback、debug 信息都依赖「当前执行到哪条指令」。

解决:**编译期已知 pc 作为立即数传给助手**。§3 各处的 `(i32.const PC)` 就是这个机制——每个可能出错/调用/safepoint 的点,翻译器把该 opcode 在 Proto.Code 中的下标(编译期已知)烧成 i32 立即数,作为助手参数传入。

```wat
;; 例:ADD 慢路径助手调用(§3.2.1)
(local.set $st (call $h_arith
                      (local.get $base)
                      (i32.const PC)       ;; ← 编译期已知 pc,物化为立即数
                      (i32.const OP_ADD) ...))
```

### 4.2 助手写回 CallInfo.savedPC

助手收到 pc 立即数后,**写回 CallInfo.savedPC**(对 [../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §1.2 的回填请求语义):

```go
// internal/gibbous/wasm/helpers.go —— 助手入口统一动作
func (h *helpers) hArith(base int32, pc int32, op int32, rkb, rkc, dst int32) int32 {
    ci := h.st.currentCI()
    ci.savedPC = pc       // ← 物化 pc 写回 CallInfo,traceback/错误定位用
    // ... 复用 execute.go doArithSlow
}
```

- CallInfo.savedPC 是「返回本帧时恢复的 pc」([../p1-interpreter/05](../p1-interpreter/05-interpreter-loop.md) §1.2 word1[31:0]),在解释器里由 pc++ 自然维护;gibbous 帧没有 pc++,所以助手入口显式写回。
- 写回时机:任何可能 ① 报错 ② 触发 traceback ③ 重入 execute(crescent 帧)的助手,入口都要写 savedPC。直线内联的快路径(MOVE/ADD 快路径等)**不写** savedPC——它们不出错、不调用,没有 traceback 需求(若快路径后续某条指令出错,那条指令的助手会写自己的 pc)。

### 4.3 traceback 与解释器逐字节一致的论证

gibbous 帧的错误位置、traceback 与解释执行**逐字节一致**,论证链:

1. **pc 同源**:解释器的 savedPC = 出错指令的 pc(pc++ 后回退或精确记录);gibbous 的 savedPC = 助手收到的编译期 pc 立即数。两者都是「出错指令在 Proto.Code 的下标」,同一个值。
2. **chunkname:line 映射同源**:`chunkname:line:` 前缀由 `Proto.LineInfo[pc]` 映射([../p1-interpreter/09](../p1-interpreter/09-errors-pcall.md)),gibbous 与解释器用同一个 LineInfo 表 + 同一个 pc → 同一行号。
3. **错误冒泡同构**:gibbous 帧的错误经 status 链冒泡([04-trampoline](./04-trampoline.md) §4),crescent 接到后走 `annotateError`(execute.go 25-31 行)加位置前缀——与纯解释执行走同一个 `annotateError`,前缀格式同款。
4. **差分口径不开豁免**:[08-testing-strategy](./08-testing-strategy.md) §2 的差分测试逐字节比对错误消息,gibbous 不开任何豁免(承 [../p1-interpreter/12](../p1-interpreter/12-testing-difftest.md) 口径)——pc 物化是这条比对通过的前提。

> 这条「pc 物化让 traceback 一致」是 §8 不变式 3 的核心。没有它,gibbous 帧的错误会丢失行号(显示 `?:?:` 或错误行号),差分必炸。

---

## 5. P3Compiler 接口实现

### 5.1 Compile 函数签名

承 [../p2-bridge/05](../p2-bridge/05-p3-p4-interface.md) §2,P3 实现 `bridge.P3Compiler` 接口:

```go
// internal/gibbous/wasm/compile.go —— P2 §7.1 P3Compiler 的实现方
type Compiler struct {
    rt        wazero.Runtime       // wazero 运行时(持 memory 适配、imports,§7)
    supported [numOps]bool         // SupportsAllOpcodes 渐进白名单(§5.2)
    helpers   *helperRegistry      // imported 助手注册表(§6.4)
    // + 共享 imports、memory 适配(§7)
}

// Compile 把 Proto 编译成 GibbousCode(承 P3Compiler 接口)。
//   - proto:目标 Proto(已通过 F1-F7 闸门,可编译);
//   - feedback:类型反馈快照(可选,nil 时退化为无 feedback 提示编译,§3.4.1)。
// 返回:GibbousCode(wazero CompiledModule + api.Function 包装,§5.3)/ error。
func (c *Compiler) Compile(proto *bytecode.Proto, fb *bridge.TypeFeedback) (gc bridge.GibbousCode, err error) {
    defer c.recoverToCompileError(proto, &gc, &err)   // §5.5 panic 兜底
    wat := c.translate(proto, fb)                      // §6.2 翻译主流程,产 Wasm bytecode
    module, e := c.rt.CompileModule(c.ctx, wat)        // §7.1 wazero 编译
    if e != nil {
        return nil, &bridge.CompileError{Kind: bridge.CompileErrOutOfResources, Proto: proto, Reason: e.Error()}
    }
    inst, e := c.rt.InstantiateModule(c.ctx, module, c.instConfig)  // §7.1 实例化
    if e != nil {
        return nil, &bridge.CompileError{Kind: bridge.CompileErrOutOfResources, Proto: proto, Reason: e.Error()}
    }
    fn := inst.ExportedFunction(protoEntryName(proto))  // 取导出的入口函数
    return &p3Code{module: module, fn: fn, proto: proto}, nil
}
```

- Compile 是同步语义(返回时编译完成,GibbousCode 立即可用,承 [../p2-bridge/05](../p2-bridge/05-p3-p4-interface.md) §I2)。
- 不在热路径(只在升层时一次性调用,数毫秒级可接受,承 [../p2-bridge/05](../p2-bridge/05-p3-p4-interface.md) §2 性能要求)。
- 并发安全:可被多 State 并发调用同一 Proto(承接口契约),`Compiler.rt`/`supported`/`helpers` 都是只读或线程安全的,翻译过程无共享可变状态。

### 5.2 SupportsAllOpcodes 实装

```go
// internal/gibbous/wasm/opcodes.go —— SupportsAllOpcodes 实装(渐进白名单)
func (c *Compiler) SupportsAllOpcodes(proto *bytecode.Proto) bool {
    for _, instr := range proto.Code {
        op := bytecode.Op(instr)
        if int(op) >= len(c.supported) || !c.supported[op] {
            return false   // 保守拒:不在白名单 / 未识别编号(38..63 预留区)一律 false
        }
    }
    return true
}

// newCompiler 按当前 PW 里程碑初始化 supported 表(§1.3 渐进白名单)。
func newCompiler(...) *Compiler {
    c := &Compiler{...}
    // PW2:直线 opcode
    c.supported[bytecode.MOVE] = true
    c.supported[bytecode.LOADK] = true
    c.supported[bytecode.LOADBOOL] = true
    c.supported[bytecode.LOADNIL] = true
    c.supported[bytecode.GETUPVAL] = true
    c.supported[bytecode.SETUPVAL] = true
    c.supported[bytecode.JMP] = true
    // PW3:算术 + 比较(后续 PW 取消注释对应行)
    // c.supported[bytecode.ADD] = true ... POW, UNM, NOT, EQ, LT, LE, TEST, TESTSET
    // PW4:控制流
    // c.supported[bytecode.FORPREP] = true ... FORLOOP, TFORLOOP
    // PW5:表 IC
    // c.supported[bytecode.GETTABLE] = true ... SETTABLE, GETGLOBAL, SETGLOBAL, SELF, NEWTABLE, LEN, CONCAT, SETLIST
    // PW6:调用
    // c.supported[bytecode.CALL] = true ... TAILCALL, RETURN
    // PW7:闭包
    // c.supported[bytecode.CLOSURE] = true ... CLOSE
    // VARARG 永不加入(§3.7.3:vararg 函数被 P2 F1 拦下)
    return c
}
```

- 严格遵守 [../p2-bridge/05](../p2-bridge/05-p3-p4-interface.md) §2.2.1 契约:O(N) 单遍扫、纯只读、不修改 Proto、不持久化、**不 panic**(越界/未识别编号走保守拒)。
- 保守缺省的物理表达:`supported` 数组初值全 false,只把当前 PW 明确实装的 opcode 标 true。VARARG 永不标 true。
- 与 F7 闸门的关系:[../p2-bridge/03](../p2-bridge/03-compilability-analysis.md) §3.7 的 `AnalyzeProto` 在 F1-F6 全过后调 `SupportsAllOpcodes` 作 opcode 级兜底——任一 opcode 不支持 ⇒ F7 拒 ⇒ Proto `CompCompilable=false` ⇒ 不进升层路径。

### 5.3 GibbousCode wazero 包装

承 [../p2-bridge/05](../p2-bridge/05-p3-p4-interface.md) §6.2 的 P3 实现:

```go
// internal/gibbous/wasm/code.go —— P3 的 GibbousCode 实现
type p3Code struct {
    module wazero.CompiledModule    // wazero 编译产物
    fn     api.Function             // 入口函数句柄(proto_N)
    proto  *bytecode.Proto          // 反查用(诊断/日志)
}

func (c *p3Code) Proto() *bytecode.Proto { return c.proto }   // GibbousCode 接口要求

func (c *p3Code) Run(state *State, base int32) int32 {
    results, err := c.fn.Call(state.ctx, uint64(base))   // 一次跨层(目标 <150ns,01-spike-gate)
    if err != nil {
        state.pendingErr = err   // wazero 内部错误(罕见)→ ERR
        return 1
    }
    return int32(results[0])     // status 由 Wasm 函数返回(0=OK / 1=ERR)
}

func (c *p3Code) Dispose() error           { return c.module.Close(context.Background()) }
func (c *p3Code) GetTrampoline() unsafe.Pointer { return unsafe.Pointer(&c.fn) }
```

- `Run` 是 crescent→gibbous 的入口([04-trampoline](./04-trampoline.md) §2):传 base i32,wazero 执行 Wasm 函数,返回 status。
- `Dispose` 释放 wazero CompiledModule(幂等,承 [../p2-bridge/05](../p2-bridge/05-p3-p4-interface.md) §6.1)。
- `GetTrampoline` 返回 api.Function 句柄(P3 的 trampoline 由 wazero 自管入口 stub,承 [../p2-bridge/05](../p2-bridge/05-p3-p4-interface.md) §6.2)。

### 5.4 编译失败错误分类

对应 [../p2-bridge/05](../p2-bridge/05-p3-p4-interface.md) §2.2.2 / p3compiler.go 的四类 `CompileErrKind`:

| CompileErrKind | 触发场景 | P3 端如何产生 | 诊断动作 |
|---|---|---|---|
| `CompileErrUnsupportedOpcodeShape` | F7 漏判——`SupportsAllOpcodes` 放行但 Compile 实际编不了某 opcode 子情况(如 GETTABLE 的 key 是某种特殊形态) | translate 阶段遇到无法翻译的形态,主动返此类 | 记 issue 修 §1.3 白名单或 [../p2-bridge/03](../p2-bridge/03-compilability-analysis.md) F7 |
| `CompileErrOutOfResources` | wazero CompileModule / InstantiateModule 失败、内存不足、资源上限 | §5.1 的 `c.rt.CompileModule`/`InstantiateModule` 返 err 时包装 | 理论可重试,但 P2 不区分(不重试纪律) |
| `CompileErrBackendPanic` | P3 编译器内部 panic(实现 bug 或边角形态) | §5.5 defer recover 兜底转此类 | 记 issue 修 P3 |
| `CompileErrBackendDeclined` | P3 决定不编译(启发式判收益不够) | P3 PW0 基线不预期此类(P3 应在 SupportsAllOpcodes 阶段拒) | P2 不预期 |

不论哪类,`error != nil ⇒ P2 把 Proto 标 TierStuck`(永久解释,不重试,承 [../p2-bridge/04](../p2-bridge/04-try-compile-fallback.md) §3)。错因分类只影响诊断告警分流,不影响 fallback 行为(都是 TierStuck)。

### 5.5 panic recover 兜底

P3 后端实现 bug 不能让 P1 主循环崩溃([../p2-bridge/04](../p2-bridge/04-try-compile-fallback.md) §5.2):

```go
// internal/gibbous/wasm/compile.go —— panic 兜底
func (c *Compiler) recoverToCompileError(proto *bytecode.Proto, gc *bridge.GibbousCode, err *error) {
    if r := recover(); r != nil {
        *err = &bridge.CompileError{
            Kind:   bridge.CompileErrBackendPanic,
            Proto:  proto,
            Reason: fmt.Sprintf("p3 backend panic: %v\nstack: %s", r, debug.Stack()),
        }
        *gc = nil
    }
}
```

- 这层 defer recover 把 translate / wazero 调用中的任何 panic 转成 `*CompileError(Kind=BackendPanic)`,**不让 panic 穿越 Compile 接口**——P2 的 `tryCompile`([../p2-bridge/04](../p2-bridge/04-try-compile-fallback.md) §5.2)看到 err 后 fallback 该 Proto。
- 双层兜底:P2 的 `tryCompile` 本身也有 defer recover([../p2-bridge/04](../p2-bridge/04-try-compile-fallback.md) §5.2),P3 这层是「就近兜底 + 错因精确」(带 P3 内部 stack)。两层都在,防御纵深。
- 这是 §8 不变式 5(编译失败 panic 不穿越本接口)的执行点。

---

## 6. 编译器内部架构

### 6.1 包布局

`internal/gibbous/wasm` 包的文件职责划分:

| 文件 | 职责 |
|---|---|
| `compile.go` | `Compiler` struct + `Compile` 主入口 + translate 主流程(§6.2)+ panic 兜底(§5.5) |
| `emit.go` | emit helper 集合(`emitMove`/`emitArith`/`emitGetTable` 等,§6.3),逐 opcode 生成 Wasm bytecode |
| `opcodes.go` | `SupportsAllOpcodes` 实装 + supported 白名单初始化(§5.2)+ opcode → emit 函数的 dispatch 表 |
| `memory.go` | arena 收养 wazero memory 适配(详见 [03-memory-model](./03-memory-model.md);本文 §7.2 链)|
| `trampoline.go` | crescent↔gibbous 入口/出口(详见 [04-trampoline](./04-trampoline.md);本文 §3.6 链)|
| `helpers.go` | imported 助手 Go 侧实装(`$h_arith`/`$h_gettable`/`$h_call` 等,§6.4)+ 注册逻辑 |
| `code.go` | `p3Code`(GibbousCode 实现,§5.3) |

### 6.2 compile 主循环

```go
// internal/gibbous/wasm/compile.go —— translate 主流程
func (c *Compiler) translate(proto *bytecode.Proto, fb *bridge.TypeFeedback) []byte {
    em := newEmitter(proto, fb)        // emitter 上下文(§6.3)
    em.emitFunctionHeader()            // (func $proto_N (param $base i32) (result i32) ...)
    // 第一遍:扫描跳转目标,为每个被跳到的 pc 建基本块标签(§3.1.7 的 $L_<pc>)
    em.scanJumpTargets()
    // 第二遍:逐 opcode dispatch 到 emit_<op>
    for pc := 0; pc < len(proto.Code); pc++ {
        instr := proto.Code[pc]
        op := bytecode.Op(instr)
        em.placeLabel(pc)              // 若该 pc 是跳转目标,放标签
        emitFn := opcodeEmitTable[op]  // §6.3 dispatch 表
        if emitFn == nil {
            panic(fmt.Sprintf("p3: no emit for opcode %s at pc=%d", op, pc))  // §5.5 兜底
        }
        emitFn(em, int32(pc), instr)   // 生成该 opcode 的 Wasm bytecode
    }
    em.emitFunctionFooter()
    return em.bytes()
}
```

- 两遍扫描:第一遍建跳转目标标签(因为 Wasm 的 br 目标要前向声明),第二遍逐 opcode 生成。
- dispatch 表 `opcodeEmitTable [numOps]emitFunc`:把 opcode 编号映射到 emit 函数,O(1) dispatch。未实装的 opcode 对应 nil（开发期 panic 兜底,但 SupportsAllOpcodes 已在 F7 拦下,实际不到达）。

### 6.3 emit helper

emit 函数接收 emitter 上下文 + Instruction,输出 Wasm bytecode:

```go
// internal/gibbous/wasm/emit.go —— emit helper 形态
type emitter struct {
    proto    *bytecode.Proto
    fb       *bridge.TypeFeedback   // 可选 feedback(§3.4.1)
    buf      *wasmBuilder           // Wasm bytecode 累积器
    labels   map[int32]labelID      // pc → 基本块标签
    // locals 缓存状态(§2.3 优化,基线全 memory-resident 时为空)
}

// emitMove —— MOVE A B 翻译(§3.1.1)
func emitMove(em *emitter, pc int32, instr bytecode.Instruction) {
    a, b := bytecode.A(instr), bytecode.B(instr)
    em.buf.i64Store(8*a, em.buf.i64Load(8*b, em.localGet(baseLocal)))
}

// emitArith —— ADD/SUB/MUL/DIV/MOD/POW 翻译(§3.2.1)
func emitArith(em *emitter, pc int32, instr bytecode.Instruction) {
    op := bytecode.Op(instr)
    a, b, c := bytecode.A(instr), bytecode.B(instr), bytecode.C(instr)
    em.emitLoadRK(vbLocal, b)               // <load_RK_B>
    em.emitLoadRK(vcLocal, c)               // <load_RK_C>
    em.emitIsNumberGuard(vbLocal, vcLocal)  // IsNumber × 2 快路径前置
    em.emitArithFastPath(op, a)             // f64 算术 + NaN 规范化(§3.2.1)
    em.emitArithSlowPath(op, pc, a, b, c)   // else 分支:$h_arith 助手
}

// emitGetTable —— GETTABLE A B C 翻译(§3.4.2),IC 快照固化
func emitGetTable(em *emitter, pc int32, instr bytecode.Instruction) {
    a, b, c := bytecode.A(instr), bytecode.B(instr), bytecode.C(instr)
    snap := em.icSnapshot(pc)               // §6.5 从 Proto.IC[pc] / feedback 取快照
    if snap.kind == bytecode.ICKindNone || snap.mega {
        em.emitGetTableSlowOnly(pc, a, b, c)  // 无快照:直发助手(§3.4.4)
        return
    }
    em.emitGetTableFastPath(pc, a, b, c, snap)  // 同表同代次同键直达(§3.4.2)
    em.emitGetTableSlowPath(pc, a, b, c)        // else:$h_gettable 助手
}
```

- emit helper 一对一对应 §3 各 opcode 的 WAT 形态,把伪码落成 wasmBuilder 调用序列。
- 跨层调用前的写回纪律(§2.4):`em.emitHelperCall(...)` 内部统一调 `em.writeBackLocals()`(若启用 §2.3 locals 缓存)——保证所有助手调用前 locals 已写回 memory。
- emitter 持 `wasmBuilder`(底层 Wasm bytecode 编码器,可用现成库或自写),把高层 WAT 语义编成二进制 Wasm。

### 6.4 imported 助手注册

从 host(Go)侧 register 助手函数,Wasm 侧 import declaration:

```go
// internal/gibbous/wasm/helpers.go —— 助手注册
func (c *Compiler) registerHelpers(rt wazero.Runtime) {
    _, err := rt.NewHostModuleBuilder("env").
        NewFunctionBuilder().WithFunc(c.helpers.hArith).Export("h_arith").
        NewFunctionBuilder().WithFunc(c.helpers.hGetTable).Export("h_gettable").
        NewFunctionBuilder().WithFunc(c.helpers.hCall).Export("h_call").
        NewFunctionBuilder().WithFunc(c.helpers.hSafepoint).Export("h_safepoint").
        // ... 其余助手(§3.8 速查表的 $h_* 列)
        Instantiate(c.ctx)
    // ...
}
```

- Wasm 侧的 import declaration(translate 阶段生成):`(import "env" "h_arith" (func $h_arith (param i32 i32 i32 i32 i32 i32) (result i32)))`。
- 助手 Go 侧实装统一复用 execute.go 的慢路径函数(`doArithSlow`/`icGetTable`/`doCall` 等),保证「翻译输出与解释器逐字节一致」——慢路径根本就是同一份 Go 代码。
- 助手内 `unsafe.Pointer` 读 base offset:callback 收到 base i32 后,经 arena 视图把 base 还原为值栈槽指针(详见 §7.3)。

### 6.5 编译时 IC 快照

从 `Proto.IC[pc]` 读 `(Shape, Index, TableRef, Kind)`,固化为 WAT 立即数:

```go
// internal/gibbous/wasm/emit.go —— IC 快照提取(§3.4.1)
type icSnapshot struct {
    tableRef uint32  // SNAP_TABLEREF
    gen      uint32  // SNAP_GEN(= ICSlot.Shape)
    index    uint32  // SNAP_INDEX
    kind     uint8   // SNAP_KIND(ArrayHit/NodeHit/MonoMeta)
    mega     bool    // megamorphic:不固化快照,直发助手
}

func (em *emitter) icSnapshot(pc int32) icSnapshot {
    // 优先用 P2 feedback(更可信,聚合后稳定值);feedback nil 时退化读 Proto.IC[pc]
    if em.fb != nil {
        if pf := em.fb.PointAt(pc); pf.Kind == bridge.FBTableMono || pf.Kind == bridge.FBGlobalStable {
            return icSnapshot{tableRef: ..., gen: pf.StableShape, index: pf.StableIndex, kind: ...}
        }
        if pf := em.fb.PointAt(pc); pf.Kind == bridge.FBTableMega {
            return icSnapshot{mega: true}   // 别投机,直发助手
        }
    }
    // 退化:读 P1 落地的 ICSlot
    slot := &em.proto.IC[pc]
    if slot.Kind == bytecode.ICKindNone {
        return icSnapshot{kind: bytecode.ICKindNone}   // 从未命中,直发助手
    }
    if slot.Kind == bytecode.ICKindMegamorphic {
        return icSnapshot{mega: true}
    }
    return icSnapshot{tableRef: slot.TableRef, gen: slot.Shape, index: slot.Index, kind: slot.Kind}
}
```

- 快照源优先级:P2 feedback(聚合后的稳定值)> Proto.IC[pc](单次 IC slot 读取)。feedback 为 nil 时退化读 ICSlot(承 [../p2-bridge/05](../p2-bridge/05-p3-p4-interface.md) §2「实现方必须容忍 nil」)。
- megamorphic / 从未命中的点:不固化快照,直发助手(无快路径)——避免烧一个必然 miss 的快照浪费指令。
- 快照固化后烧成 WAT 立即数(§3.4.2 的 `SNAP_TABLEREF`/`SNAP_GEN`/`SNAP_KIND`/`SNAP_INDEX`)。运行期同表同代次直达,失效降级走助手(详见 [06-ic-feedback-consume](./06-ic-feedback-consume.md))。

---

## 7. wazero API 适配层

### 7.1 wazero.NewRuntime + Compile + Instantiate 流程

```go
// internal/gibbous/wasm/compile.go —— wazero 运行时初始化(NewState 时一次)
func newRuntime(ctx context.Context) wazero.Runtime {
    // 编译模式(非解释模式)——spike 验证用编译模式(01-spike-gate §1.2)
    cfg := wazero.NewRuntimeConfigCompiler()   // 编译模式(JIT to native)
    // ... memory 适配配置(§7.2)、优化选项(§7.4)
    return wazero.NewRuntimeWithConfig(ctx, cfg)
}
```

- 运行时初始化在 NewState 时一次性完成(P3 build 下),后续每个 Proto 升层走 Compile(§5.1)。
- 编译模式:wazero 提供 Compiler(编译到原生码)和 Interpreter(解释执行)两种;P3 用 Compiler 模式才有性能(spike S2 < 150ns 也是测编译模式,[01-spike-gate](./01-spike-gate.md) §1.2)。
- `CompileModule` → `InstantiateModule` 两步(§5.1):前者把 Wasm bytecode 编成 wazero 内部表示,后者实例化为可调用 module。

### 7.2 Memory 共享(arena 收养 wazero memory)

详见 [03-memory-model](./03-memory-model.md),本节只给链接 + 要点:

- arena 的 backing 在 P3 起改为「收养 wazero Memory 的底层 buffer」([03-memory-model](./03-memory-model.md) §1.2)——NewState 时即经 wazero 分配 memory,arena 的 words/bytes 视图从该 buffer 派生。
- `memory.grow` 后偏移寻址不变(所有 GCRef/链表/bump 一字不改,只 Go 侧视图 slice 重取,[03-memory-model](./03-memory-model.md) §3.2)。
- 这是「值世界 = linear memory」的物理兑现——gibbous 代码读写的 `i64.load/store offset=8*i` 直接落在 arena 同一块内存,与解释器共见(§2.2 红利)。

### 7.3 imported 函数注册的 Go 侧 binding

助手 Go 侧 binding 内,base offset 还原为值栈槽访问:

```go
// internal/gibbous/wasm/helpers.go —— 助手内 base offset 还原
func (h *helpers) hArith(base int32, pc int32, op int32, rkb, rkc, dst int32) int32 {
    // base 是值栈 R0 的字节偏移;经 arena 视图还原为 Go 侧值栈槽指针
    ci := h.st.currentCI()
    ci.savedPC = pc                                    // §4.2 pc 物化
    // arena 视图:base 字节偏移 → 值栈逻辑索引
    regBase := base / 8                                // 字节偏移 → 槽索引(每槽 8 bytes)
    // 复用 execute.go 的慢路径(传入还原后的 ci/regBase)
    instr := h.synthArithInstr(op, rkb, rkc, dst)      // 合成 Instruction 喂 doArithSlow
    if e := h.st.doArithSlowAt(h.th, ci, regBase, instr); e != nil {
        h.st.pendingErr = e
        return 1
    }
    return 0
}
```

- base i32 在助手内除以 8(NaN-box u64 = 8 bytes)还原为值栈槽逻辑索引,再经 arena 视图访问(`unsafe.Pointer` 读 backing buffer)。
- callback 内的 `unsafe.Pointer` 读 base offset:arena 的 words 视图([../p1-interpreter/06](../p1-interpreter/06-memory-gc.md) arena 视图)提供 `wordAt(idx)` 访问,助手经它读写值栈槽。
- 助手统一复用 execute.go 慢路径(`doArithSlow`/`icGetTable`/`doCall`)——这是「翻译输出与解释器逐字节一致」的实现保证(§8 不变式 1):慢路径根本是同一份代码。

### 7.4 wazero 优化选项

wazero 默认已开若干优化(闭包逃逸分析、内联等),**待 spike 验证**具体效果:

| 优化 | wazero 默认 | P3 期望 | 验证状态 |
|---|---|---|---|
| 编译模式(vs 解释模式) | 需显式 `NewRuntimeConfigCompiler` | 必开(性能前提) | [01-spike-gate](./01-spike-gate.md) §1.2 spike 验证 |
| 函数内联 | wazero 内部决定 | 助手调用不被内联(跨 module 边界),但 module 内 br 优化期望开 | 待 spike 实测 |
| 逃逸分析 / 寄存器分配 | wazero 自管 | memory-resident 访存期望被 wazero 寄存器分配优化(load 后驻留寄存器) | 待 spike 实测 |
| target 架构 | wazero 自动检测(amd64/arm64) | 跟随宿主架构 | 无需配置 |

> **文档缺口**:wazero 具体编译模式选项(优化级别、target 等)的精确配置待 spike 验证(§9)。基线先用 wazero 默认编译模式,优化旋钮在 PW9 性能调优期(≥2x 验收)按实测调整。

---

## 8. 不变式清单(实现与差分须守)

承 ../p3-wasm-tier §10 不变式 1/2/3 在翻译器层的展开,本文新增 4/5/6:

1. **翻译输出与解释器 byte-equal**:每个 opcode 的翻译形态(§3)与 execute.go 对应段逐句同构;快路径是语义分发非投机 guard(失败走慢路径助手,零 deopt);慢路径助手复用 execute.go 同款 Go 代码(§7.3)。NaN 规范化(§3.2.1)、EQ 的 f64.eq(§3.3.3)等是 byte-equal 的精确执行点。
2. **NaN-box 编码两层逐位同一**:Wasm 侧不引入任何私有值表示([../p1-interpreter/01](../p1-interpreter/01-value-object-model.md) §7「值表示一次定死」)——`IsNumber` 用 NaN-box 单比较(§3.2.1)、tag 提取用位移(§3.2.5)、常量烧 raw u64 立即数(§3.1.2),全是 P1 NaN-box 编码的逐位直译。
3. **pc 物化让 traceback 一致**:直线代码无运行期 pc,编译期已知 pc 作为立即数传给助手,助手写回 CallInfo.savedPC(§4)——gibbous 帧的错误位置、traceback 与解释执行逐字节一致,差分不开豁免。
4. **SupportsAllOpcodes 保守缺省**:不在白名单的 opcode 一律返 false(§5.2);未识别编号(38..63 预留区)返 false;VARARG 永不加入白名单(§3.7.3)。渐进白名单按 PW 扩充(§1.3)。
5. **编译失败 panic 不穿越本接口**:P3 后端实现 bug 经 §5.5 defer recover 转 `*CompileError(Kind=BackendPanic)`,不让 panic 穿越 Compile 接口让 P1 主循环崩溃。
6. **单 Proto 失败原子性**:单 Proto 编译失败只该 Proto fallback(TierStuck 单 Proto 决议),不影响兄弟 Proto(§1.4)——这是基线方案「每 Proto 一 module」隔离价值的体现。

---

## 9. 文档缺口 / 待决(记入 [memory/doc-gaps](../../../llmdoc/memory/doc-gaps.md))

- **wazero 具体编译模式选项**(§7.4):优化级别、target、内联策略等待 spike 验证([01-spike-gate](./01-spike-gate.md) §1.4 顺带项)。基线先用 wazero 默认编译模式。
- **locals 缓存的槽选择算法**(§2.3 / §3.5.2):FORLOOP 三槽(idx/limit/step)外是否扩展到其它循环局部热槽——**PW9 实测后判定:不需要**。loop 核全 memory-resident 下 gibbous 已 2.58x over 解释器(V14 ≥2x 达标),真实退化是跨层调用税(call 核 0.14x)而非寄存器访问,locals 缓存与之正交、治错成本。基线全 memory-resident 保留。
- **IC 快照失效后是否重编译**(§3.4 / [06-ic-feedback-consume](./06-ic-feedback-consume.md)):失效计数 → 重编译预算,与 P4 的再训练机制([../p4-method-jit/04-osr-deopt](../p4-method-jit/04-osr-deopt.md) §5)统一评估。P3 基线失效后该点永久走助手(正确但慢)。
- **批量 module + call_indirect 直调收益边界**(§1.2):每 Proto 一 module 的实例化开销实测后定批量阈值;批量编译的「免 Go 往返」收益 vs「一损俱损 + 批次划分复杂度」成本权衡——**PW9 实测坐实收益(call 核 7x 退化 = gibbous→gibbous 双跨层 ~143ns),拆 PW10 spike 闸门先行**(单 module + call_indirect 直调 + CallInfo arena,生死未知数 = wazero 增量 module 可行性)。
- **嵌套闭包 / upvalue 编译协议在 wazero linear memory 形态下的具体形态**(§3.7.1):开放/关闭 upvalue 的 cell 存储位置、开放 upvalue 如何指向栈槽、嵌套闭包的捕获链,留 PW7 实测后定标。基线全经 `$h_closure`/`$h_close` 助手。
- **gibbous 帧是否恢复热度计数**(§3.5.2):P3 范围内 gibbous 是顶层不计热(OnBackEdge 不调);若未来 P4/P5 引入「gibbous→更高层再升层」,热度计数可能要恢复——与 IC 快照失效重编译评估时一并考虑。
- **POW / 大区间 LOADNIL 等的内联 vs 助手边界**(§3.2.2 / §3.1.4):POW 走助手还是 wazero intrinsic、大区间 LOADNIL 是否用 memory.fill,留实测定。基线 POW 走助手、LOADNIL 展开。

---

相关:
[00-overview](./00-overview.md)(P3 总览,本文遵守其章节番号与风格) ·
[01-spike-gate](./01-spike-gate.md)(开工闸门,编译模式 spike) ·
[03-memory-model](./03-memory-model.md)(共见 linear memory,§7.2 arena 收养 wazero memory) ·
[04-trampoline](./04-trampoline.md)(跨层互调,§3.6 CALL/TAILCALL/RETURN) ·
[05-safepoint-gc](./05-safepoint-gc.md)(回边 safepoint 与 locals 写回纪律,§3.5 / §2.4) ·
[06-ic-feedback-consume](./06-ic-feedback-consume.md)(IC 快照固化与失效降级,§3.4) ·
[07-coroutine-thread-rule](./07-coroutine-thread-rule.md)(线程级 tier 规则) ·
[08-testing-strategy](./08-testing-strategy.md)(差分验收,byte-equal 口径) ·
../p3-wasm-tier(原稿主体,本文 §2 展开) ·
[../p1-interpreter/02-bytecode-isa](../p1-interpreter/02-bytecode-isa.md)(源 ISA 完整定义,翻译输入) ·
[../p1-interpreter/05-interpreter-loop](../p1-interpreter/05-interpreter-loop.md)(解释器主循环,byte-equal 基准) ·
[../p1-interpreter/01-value-object-model](../p1-interpreter/01-value-object-model.md)(NaN-box 编码,两层逐位同一) ·
[../p1-interpreter/06-memory-gc](../p1-interpreter/06-memory-gc.md)(GC 根枚举与 safepoint) ·
[../p2-bridge/02-ic-feedback](../p2-bridge/02-ic-feedback.md)(TypeFeedback shape,可选消费) ·
[../p2-bridge/03-compilability-analysis](../p2-bridge/03-compilability-analysis.md)(F7 SupportsAllOpcodes) ·
[../p2-bridge/05-p3-p4-interface](../p2-bridge/05-p3-p4-interface.md)(P3Compiler 接口签名 + GibbousCode 抽象)


