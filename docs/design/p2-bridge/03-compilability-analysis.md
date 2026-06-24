# P2 §3:静态可编译性分析子系统(F1-F7 安全闸门)

> 状态:**设计阶段,详细设计已齐备**。本文是 [00-overview](./00-overview.md) §0 文档地图列出的 **P2 安全闸门单一事实源**——`Compilability` 枚举与缓存生命期、不升层形状清单 F1-F7 的完整定义、AST visitor 设计、yield 保守近似算法、F7 (P3 后端能力)查询协议。
> 上游种子:[00-overview](./00-overview.md) §4(静态可编译性分析器,本文大量扩展)。
> 上游耦合点(00 §3 关键耦合 4):**AST 保留协议**——本文给出三选一决策(§2.4)。
> P1 依赖面:[04 §1 / §3](../p1-interpreter/04-frontend-parser-codegen.md)(AST 节点定义 / 「P1 顺手产出 P2 复用」)、[02 §6 / §4](../p1-interpreter/02-bytecode-isa.md)(`VARARG` opcode / `IsVararg` 标记 / opcode 0..37 全集)、[01 §5.7](../p1-interpreter/01-value-object-model.md)(`Proto.Code/MaxStack/IsVararg` 字段)、[08](../p1-interpreter/08-coroutines.md)(协程 yield 语义,F2 保守判定的依据)、[10](../p1-interpreter/10-stdlib.md)(`debug.*` / `setfenv` / `coroutine.yield` 调用点识别)、[11 §1.3](../p1-interpreter/11-embedding-arena-abi.md)(`Compile` 可编译性探测占位,本文负责填实)。
> 上游原则面:[../roadmap.md](../roadmap.md) §5 原则 4(不可编译形状走 fallback,不做完备性)、[design-premises](../../../llmdoc/must/design-premises.md) 前提三原则 4。
>
> **本文定位一句话**:**判错的后果是灾难性的**——把不可编译形状判成可编译,P3 会编译出错误代码或运行期崩溃,而 fallback 机制根本不会被触发(因为没人知道这里该 fallback)。所以本文反复强调**保守第一,宁漏勿误**,这是 P2 全部设计中最严苛的一条铁律。

---

## 0. 定位:try-compile-fallback 零 deopt 的安全闸门

### 0.1 与 P2 整体安全性的关系

[00-overview](./00-overview.md) §3 关键耦合点列出 P2 的六个跨组件耦合,其中第 3 条「`Proto.compilable` 字段的初值约定」与第 4 条「AST 保留协议」**全部归本文管辖**。本文是 P2 安全闸门的单一事实源——任何让「不可编译形状被判成可编译」的设计都直接判否,**没有商量余地**。

这个安全性的级别可以这样类比:[p1-interpreter/12 §10](../p1-interpreter/12-testing-difftest.md) 的差分 fuzz 是 P1 防「投机错误静默错果」(JIT 最危险 bug 类别)的主防线;**本文的 F1-F7 形状判定是 P2 防「编译错误形状静默崩溃」的主防线**。两者地位等同——一旦放过一个误判,后果都不是「跑慢点」,而是「正确性崩溃」。

承 [00-overview](./00-overview.md) §4.1:

> 「这个分析器是整个 P2 的安全核心:**它决定哪些 Proto 能被 P3/P4 编译。** 因为望舒走 try-compile-fallback-interpret(LuaJ luajc 同款),编译层只编译『静态可保证能正确编译』的子集——分析器就是划这条线的人。**它判错的后果是灾难性的**:把一个不可编译形状判成可编译,P3 会编译出错误代码或运行期崩溃,而 fallback 机制根本不会被触发(因为没人知道这里该 fallback)。所以分析器的铁律是 **保守**:**宁可漏判(把可编译的判成不可编译,损失一点加速),绝不误判(把不可编译的判成可编译,出正确性事故)**。」

### 0.2 在 try-compile-fallback 流水线中的位置

承 [00-overview](./00-overview.md) §2 的总数据流图,本文的 `analyzeProto` 是状态机的**前置闸门**——它在 Proto 进入升层决策前就把不可编译形状永久挡在外面([04-try-compile-fallback](./04-try-compile-fallback.md) `considerPromotion` 入口的第一道判断):

```
                     Compile 阶段(11 §1.3)
                     ─────────────────────
                     Compile(src) → Program {*Proto, ...}
                                        │
                                        ▼  (P1 占位:全标 CompUnknown)
                            [本文 §5] analyzeProto(ast, proto)
                                        │
                            ┌───────────┴───────────┐
                            ▼                       ▼
                     CompCompilable          CompNotCompilable
                            │                       │
                            ▼                       ▼
                     可参与升层决策              永久 tier-0
                     (热度越阈值后              (无论多热,
                      请求 P3 编译)              永远走解释)

                     运行期(每次调用 considerPromotion)
                     ─────────────────────────────────
                     04 §3 状态机:
                        if proto.compilable == CompNotCompilable:
                           return  // 永远不升,零开销
                        if proto.profile.hot():
                           tryCompile(proto, feedback)
```

**关键纪律**:`analyzeProto` 一次分析、结果永久缓存进 Proto 旁(§5.3),后续每次 `considerPromotion` 直接读缓存——**热路径上不再做任何 AST 遍历**。这与 P2「自己不在执行热路径」(00 §1)的物理体现一致。

### 0.3 与 04(状态机)、05(P3 接口)的边界

| 关注点 | 本文(03)拥有 | 不属于本文 |
|---|---|---|
| **不升层形状清单** | ✅ F1-F7 定义、识别方法、保守边界 | — |
| **Compilability 枚举** | ✅ 三态枚举(Unknown/Compilable/NotCompilable)+ Proto 旁缓存生命期 | — |
| **AST visitor 设计** | ✅ `compilabilityVisitor` 结构 + 各 visit 方法 | — |
| **F7 (P3 后端能力)查询协议** | ✅ `SupportsAllOpcodes` 接口形状(本文定义) | P3 后端真实实现(交 [05-p3-p4-interface](./05-p3-p4-interface.md) §2 + [../p3-wasm-tier/02-translation](../p3-wasm-tier/02-translation.md)) |
| **状态机** | ❌ 不属本文 | [04-try-compile-fallback](./04-try-compile-fallback.md) §2 |
| **TierStuck / TierState 转移** | ❌ 不属本文 | [04-try-compile-fallback](./04-try-compile-fallback.md) §3 |
| **P3 编译实际触发** | ❌ 不属本文 | [05-p3-p4-interface](./05-p3-p4-interface.md) §1 |
| **fallback 日志格式** | ❌ 不属本文 | [04-try-compile-fallback](./04-try-compile-fallback.md) §6 |

本文负责回答的核心问题是:**给定一个 Proto,它是否「静态可保证能正确编译到 P3 后端」**——是个**静态判定**,不卷入运行期决策(那是 04 的事)、不卷入实际编译(那是 P3 的事)。

---

## 1. 铁律:保守第一,宁漏勿误

### 1.1 误判与漏判的非对称代价

本节是 P2 最重要的工程纪律。**两种判错的代价不对称**:

| 判错方向 | 后果 | 严重性 |
|---|---|---|
| **漏判**(可编译 → 判成不可编译) | 该 Proto 永久解释,**少赚加速**(可能就是几个百分点的性能差) | 可接受,与原则 3「每阶段独立交付,任何闸门处停下都不亏」一致 |
| **误判**(不可编译 → 判成可编译) | P3 编译时可能产出错误代码、运行期崩溃、**结果静默错**——而且 fallback 不会被触发(系统认为这是好的可编译形状),问题会以「数据错」「行为漂移」的形态隐藏 | **不可接受** —— 这是 [roadmap §5 原则 2 的「投机错误静默错果」](../roadmap.md)在 P2 的对应物,是整个工程的红线 |

「损失一点加速」与「正确性崩溃」之间没有可比性。**保守的代价是有界的**(慢一点),**误判的代价是无界的**(系统行为不可预测)。所以工程原则只有一条:

> **任何拿不准的形状,一律判 `CompNotCompilable`**。

### 1.2 与投机 JIT(P4)的根本区别

P2 的本文判定与 [p4-method-jit](../p4-method-jit/00-overview.md) 的类型投机有**结构性差异**,实现者必须分清:

| 维度 | **本文(P2 静态可编译性)** | P4 类型投机 |
|---|---|---|
| 判定时机 | **编译期一次**(`Compile` 时) | **运行期持续**(读 IC feedback) |
| 错误后果 | 误判 ⇒ **静默错果**,无补救 | 投机错 ⇒ **deopt 着陆解释器**,有补救 |
| 安全机制 | **零 deopt 路线**:不编译就是不编译,永久解释 | **有 deopt 路线**:guard 失败回 P1 |
| 工程取向 | **保守第一,宁漏勿误**(本文) | **激进有度,deopt 兜底**(P4) |
| 失败可观察 | 不可观察(系统会照常运行,只是结果是错的) | 可观察(deopt 着陆有日志,差分 fuzz 能抓) |

**关键差异**:P4 的投机有「deopt」兜底——投机错了能回到解释器、用正确语义重算;而**P2 的可编译性误判没有兜底**——一旦放过去,P3 编译出来的错误代码就是终态,用户看到的是错的结果,系统自己根本不知道错了。这是「fallback ≠ deopt」(00 §6 决策表 / 04 §1)的另一面:fallback 是**静态决定永久解释**,要在 `analyzeProto` 这一步就闭环。

> **再说一遍**:本文不允许「投机判可编译 + 编译期 guard」这类构造。要么静态可保证可编译,要么判 `CompNotCompilable`。**没有第三态**。

### 1.3 「漏判可接受」的工程量化

「漏判」(把可编译的判成不可编译)的代价是「该 Proto 永远解释,损失加速」。这条代价的可接受性来自[design-premises](../../../llmdoc/must/design-premises.md) 原则 4 的下半句:

> 「可静态分析的子集走快路径即可覆盖绝大多数真实负载(Pallene 是 typed-subset 路线先例;审计过的一个 **262 脚本生产库** 中绝大多数是简单形状)」

**262 脚本审计**给出的经验是:绝大多数热点函数是「简单形状」(无 vararg、无 coroutine、无 debug、无 setfenv)——保守判定的「漏判」面在真实负载上极小,**最坏也只是少编译一个边缘函数**。如果实测发现某个被高频判 `CompNotCompilable` 的形状漏判面太大(比如某真实负载里 30% 函数都被某条规则误漏),才考虑放宽该条规则——见 §9 缺口的「精确 yield 分析」「F5/F6 阈值定标」。

### 1.4 铁律的 review 纪律

本文的所有判定规则在实现期需要**强 review** —— 任何一条规则的「放宽」(从更保守变更激进)都需要:

1. **明确的差分基准**:放宽前的判定结果与放宽后的判定结果在 fuzz 注入下逐 Proto 比对,任何「从 NotCompilable 转 Compilable」的 Proto 都要单独证明可编译性。
2. **语义层论证**:为什么这个形状之前判不可编译是「过度保守」、放宽后什么不变量保证安全。
3. **回归测试**:加进 [06-testing-strategy](./06-testing-strategy.md) §3 的可编译性误判注入 fuzz 集,后续永远跑。

「先保守、再观察、有证据再放宽」是本文规则演进的唯一路径——任何「直接添加激进规则」的 PR 都直接拒。这与 P1 的「渐进 + 反复回归」方法论一脉相承(roadmap §5 原则 2 + P1 的差分 fuzz)。

---

## 2. 分析层选择:AST 主分析 vs Proto 辅助

### 2.1 候选层与权衡

[00-overview](./00-overview.md) §4.2 给过这个表,本文重新论证一遍并补足细节:

| 分析层 | 优点 | 缺点 |
|---|---|---|
| **AST**(`internal/frontend/ast`) | ① 结构化信息丰富——`function(...)` 的 vararg 标记、`coroutine.yield` 调用点、`...` 表达式、`setfenv` 调用一眼可辨;② 04 已产出 AST([04 §1](../p1-interpreter/04-frontend-parser-codegen.md)),P2 复用纯增量;③ 「源级概念」(debug 库、setfenv)在 AST 上 1-1 对应,无需 opcode 反推 | AST 在 codegen 后默认被 GC(04 §1.1 「AST 节点用 Go 堆,编译后即可 GC」),需保留协议(§2.4) |
| **Proto**(`internal/bytecode`) | ① Proto 恒存(01 §5.7),总能分析;② `IsVararg` / `VARARG` opcode 暴露 F1;③ `Code` 长度暴露 F5(过大函数);④ 跨 P1/P2 边界稳定(Proto 字段不会因 AST 是否保留而变) | ① 字节码丢失源级语义——`coroutine.yield` 在字节码里只是「`GETGLOBAL` + `CALL`」,与「调用其它任意函数」无法区分;② F3/F4 在字节码层难精确识别;③ 行为分析(如检测「调用了未知函数」)需要构图 |

### 2.2 定稿:AST 主分析,Proto 辅助交叉校验

**定稿决策**:

- **主分析在 AST 上**:`compilabilityVisitor` 遍历 `*ast.FuncBody`,F1-F4 / F6 全部在此识别。
- **Proto 层做辅助交叉校验**:
  - F1 的 Proto.IsVararg 与 AST 的 `FuncExpr.IsVararg` 互校(差异即 codegen bug,断言);
  - F5 的 `len(Proto.Code) > MaxCompilableInsns` / `Proto.MaxStack > MaxRegs`;
  - F7 的 `p3.SupportsAllOpcodes(Proto)` 反查支持集。
- **F2(yield)、F3(debug)、F4(setfenv)只在 AST 上判**:Proto 层做这些判定的复杂度高、易漏(详见 §3.2-§3.4 各形状的论证)。

> 这一选择与 [00-overview](./00-overview.md) §4.2 的种子定稿一致,本文只是补足「为什么不全在 Proto 上做」的论证。

### 2.3 「AST 主」的两条不变式

主分析在 AST 上,但实现期必须守两条不变式以防失守:

1. **AST 与 Proto 必须语义对齐**(codegen 的正确性义务):任何 AST 上判「不可编译」的 Proto,在 Proto 层做交叉校验时(F1 / F5 / F7)结果必须**一致**(或更严格)。出现「AST 判可编译,Proto 判不可编译」(漏判,可接受)允许;出现「AST 判不可编译,Proto 判可编译」(误判,危险)直接 panic ——这是 codegen 把 AST 信息丢失的 bug,需立即修。
2. **AST 用完即弃**(详见 §2.4 决策):Proto.compilable 一旦写入,AST 即可释放。运行期不持 AST,这是 P2「不在热路径」的物理保证(避免 AST 占用内存、防被运行期代码意外引用)。

### 2.4 AST 保留协议:三选一决策

[00-overview](./00-overview.md) §3 关键耦合 4 与 §7 末尾留下了**最早决策点**:**04 是否保留 AST 给 P2**?三个候选方案:

| 方案 | 描述 | 优点 | 缺点 |
|---|---|---|---|
| **① Compile 时同步分析 + 缓存**(**本文定稿**) | `Compile` 把可编译性分析当作 codegen 的最后一步,产出 Proto.compilable 后 AST 立即丢弃 | ① AST 用完即弃,内存占用零增;② 04 无需暴露 AST 留存接口,**P1 公共 API 完全不变**;③ 分析时 AST 还在 codegen 上下文,自然可读;④ **P1→P2 升级时 wangshu.go 门面零修改**(原则 3 接口稳定) | Compile 时间略增(多一次 AST 遍历);但 Compile 是一次性的(11 §1.5),不在热路径 |
| ② 04 暴露 AST 留存接口 | Proto 持 `*ast.FuncBody` 引用,P2 升层时按需读 | 推迟分析,Compile 更快 | ① 04 公共 API 改动(+暴露 AST 类型);② AST 内存常驻;③ 升层路径首次扣分析时间(运行期开销);④ AST 跨 GC 边界存活,易引内存泄漏 |
| ③ P2 升层时按 chunkname 重新 parse | Compile 不存 AST,P2 升层时根据 Source 重新读源文件 + parse | AST 完全不存 | ① 需要源文件可访问(嵌入式 VM 多半源已不可访问);② parse 错误重现风险(Compile 已成功的源 parse 失败 = 不一致);③ 解析慢(整个文件全跑一遍) |

**定稿决策(本文)**:**方案 ①——Compile 时同步分析、缓存结果、AST 用完即弃**。

理由:

1. **接口稳定性是硬约束**(原则 3):方案 ② / ③ 都改动 04 公共 API 或要求源文件可访问,违反 P1→P2 升级零变化;方案 ① 让 P2 上线时 04 / 11 公共 API **完全不变**,wangshu.go 门面零修改。
2. **AST 短命是现状**(04 §1.1 已说「AST 节点可用 arena-free 的 Go 堆对象,编译后即可 GC」):方案 ① 与现状最契合,AST 在编译期内多活几纳秒(到 `analyzeProto` 跑完)即丢。
3. **运行期零开销**:方案 ② 把分析放运行期,首次升层路径变慢——P2 不在热路径的物理保证打折扣;方案 ① 把开销摊到一次性的 Compile,运行期升层只读缓存。
4. **错误一致性**:方案 ③ 重新 parse 可能与首次 parse 行为不一致(虽不太可能,但语义脆弱);方案 ① 与 codegen 共用一份 AST,无重新解析的风险。

**唯一代价**:Compile 多一次 AST 遍历——按 04 §10 端到端示例的小函数估算,每个 Proto 的 visitor 遍历是 O(AST 节点数)的,加成可忽略;大文件(几千行 Lua 脚本)估算 < 1ms 的额外编译时间,完全可接受。

### 2.5 对 04 的回填请求(单行接口规约)

定稿方案 ① 后,需在 04 写明:

> **04 §N(待补)**:`compile.Gen` 在产出 `Proto` 后,在返回前调用 `bridge.AnalyzeProto(funcBody, proto)`(若 `internal/bridge` 可用),把结果写进 `proto.compilable`。`bridge` 包不可用时(P1-only build,`!profile` build tag)跳过此步,`proto.compilable = CompUnknown`。

**详细回填请求**见 §10。这是本文对 04 的唯一接口要求——`compile.Gen` 在 codegen 末尾调一个回调,签名固定:

```go
// internal/bridge(P2)
func (b *Bridge) AnalyzeProto(fn *ast.FuncBody, proto *bytecode.Proto)
```

`compile.Gen` 持有刚产出的 `*bytecode.Proto` 与对应的 `*ast.FuncBody`,二者天然对齐(同一 codegen 调用),无需额外查找。

### 2.6 P1-only build 的 fallback 行为

[00-overview](./00-overview.md) §3 关键耦合 2 提到 `vm.profileEnabled` 编译期常量(P1-only 部署时关掉零开销)。本文与之对偶:

- P1-only build(`!profile` build tag)无 `internal/bridge` 包,`compile.Gen` 不调 `AnalyzeProto`,所有 Proto.compilable 留 `CompUnknown`(§5 枚举默认值)。
- P2 启用 build,`compile.Gen` 调 `AnalyzeProto`,所有 Proto.compilable 写真值。
- [04-try-compile-fallback](./04-try-compile-fallback.md) §3 的 `considerPromotion`:`CompUnknown` 视同 `CompNotCompilable`(再保守一层——还没分析就不升),保险起见。

这一约定让 P1-only 部署的 byte-equal 差分(00 §4 PB1 验收)不受 P2 引入影响。

---

## 3. 不升层形状清单 F1-F7(核心章节)

本节是本文的「单一事实源」实质——F1 到 F7 七种形状,**任一出现即判 `CompNotCompilable`**。每一节给出:**为什么不编译 / AST 与字节码识别方法 / Go visitor 代码骨架 / 保守边界(假阳性如何容忍)**。

> **总纲**:F1-F7 是「**或**」的关系——只要中任一成立,Proto 就判 `CompNotCompilable`,不需要全部出现。这与「**与**」的清单(全部要满足)语义相反,实现时务必清楚:**安全闸门是「不安全条件的并集」,不是「安全条件的并集」**。

### 3.1 F1:vararg 函数

#### 3.1.1 形状定义

Lua 5.1 vararg:

```lua
function f(...)             -- (a) 形参列表为 vararg
    return ...               -- (b) 函数体内含 ... 表达式
end

local function g(a, b, ...)  -- (c) 部分 vararg(混合显式参 + ...)
    print(a, b, ...)
end
```

任一(a)/(b)/(c) 成立即「该函数是 vararg」。

#### 3.1.2 为什么不编译

[02 §6 vararg 的寄存器约定](../p1-interpreter/02-bytecode-isa.md):**vararg 函数的多余实参存于 `base` 之下的负区**,`VARARG` opcode 按需把它们拷回正区寄存器。这条「负区」语义与:

1. **Wasm linear memory 的 P3 编译形态**(11 §11):P3 把 Lua 寄存器映射成 Wasm locals 的紧凑布局,「负区」需要额外间接寻址,与 P3 寄存器分配前提冲突;
2. **多值传播的 `B=0` 到 top 语义**(02 §3 / §6):VARARG 的 `B=0` 表示「拷到 top」,涉及运行期长度——P3 编译期固定 locals 数,与运行期变长 vararg 数语义错位;
3. **vararg 函数多是『胶水』**:工程经验表明 vararg 函数大多是一次性入口或日志包装(`print(...)` 之类),**不是热点**——把它们排除在编译外,损失加速面极小。

理由 1+2 是「形状不可编译」的本质;理由 3 是「漏判可接受」的工程支撑(§1.3 的 262 脚本审计经验)。

#### 3.1.3 三重识别(AST + Proto + opcode)

vararg 是七种形状里**最易识别**的——三个互校点:

| 识别点 | 数据来源 | 检查表达式 |
|---|---|---|
| AST | `*ast.FuncExpr.IsVararg` | `fn.IsVararg == true` |
| Proto | `bytecode.Proto.IsVararg`(01 §5.7) | `proto.IsVararg == true` |
| opcode | `Proto.Code` 含 `VARARG`(opcode 37,02 §4) | 扫一遍 Code,见 op==37 即记 |

**三重一致是 codegen 正确性义务**:任意两者出现差异(如 AST.IsVararg=true 但 Proto.IsVararg=false)即 codegen bug——本文 visitor 在 §5.4 的入口检查会 panic 报告。

主分析在 AST 上(§2.2),**`fn.IsVararg` 是 F1 的判定主语**;Proto 字段与 VARARG opcode 是辅助交叉校验。

#### 3.1.4 Go visitor 代码骨架(F1)

```go
// 在 compilabilityVisitor 上,F1 不需要遍历——直接检查 FuncExpr.IsVararg
// (这是为什么 analyzeProto 的入口签名是 *ast.FuncBody / *ast.FuncExpr,
//  而不是 *ast.Block——必须能拿到 IsVararg 标记)。

func (v *compilabilityVisitor) checkF1Vararg(fn *ast.FuncExpr) {
    if fn.IsVararg {
        v.reasons |= reasonVararg            // 见 §5.2 reasons 位图
    }
}

// VarargExpr 节点也要标记——某些边角情况(P1 codegen 是否在所有 vararg 函数
// 都置 IsVararg=true?待 04 落地后断言)需要兜底。
func (v *compilabilityVisitor) VisitVarargExpr(e *ast.VarargExpr) {
    v.reasons |= reasonVararg                // 防御性:看到 ... 就标记
}
```

> **保守兜底**:即使 `fn.IsVararg=false`(理论上不应有 `...` 表达式),只要 visitor 看到 `*ast.VarargExpr` 就标 `reasonVararg`。这是「保守第一」的体现——**永远做最严格判定,不依赖上游标记的完整性**。

#### 3.1.5 假阳性容忍

F1 几乎不产生「假阳性」(把非 vararg 误判为 vararg)——`IsVararg` 标记是确定的语法标记。**唯一的「漏」**:有些胶水 vararg 函数其实可以被简化处理(比如 vararg 数可静态确定的场景),但这类优化属于「精确分析」(§9 缺口),P2 初版不做。

**漏判面估算**:262 脚本审计中 vararg 主要出现在:① 入口主 chunk(`function(...)` 隐式);② `print` 包装类工具函数;③ 极少数 metaprogramming。这三类合计占比小(< 5%),**漏判可接受**。

主 chunk 的 vararg 是不可避免的(Lua 5.1 主 chunk 总是 vararg,`...` 是命令行参数)——这意味着主 chunk 永远 `CompNotCompilable`。这与 `00-overview` §1 的边界划分一致:**主 chunk 是胶水入口,不在加速目标内**;P2/P3 加速的对象是主 chunk 调用的子函数,这些大多不是 vararg。

### 3.2 F2:协程相关(最复杂的判定)

#### 3.2.1 形状定义

任一函数,**直接或间接可能 yield**——具体地:

(a) 函数体内**直接调用** `coroutine.yield(...)` 或 `coroutine.resume(...)`;
(b) 函数体内**间接可能 yield**——调用了一个本身可能 yield 的函数;
(c) 函数自身是被 `coroutine.create` 包装的协程主函数(从入口角度看,这函数随时可能 yield)。

#### 3.2.2 为什么不编译

[08 协程](../p1-interpreter/08-coroutines.md) §1 / §3 给出 yield 的语义:

- **yield 要挂起当前执行栈,把控制权交回 resume**(协程切换 = 切 Thread + 保存 frame 回 CallInfo);
- 这意味着 yield 点**对调用栈是可观察的**——从 yield 处恢复时,所有局部变量、寄存器、PC 都要按 yield 时的状态原样回复。

P3 把 Proto 编译成 Wasm 函数后:

1. **Wasm 函数的执行栈是 Wasm runtime 的**——P3 的 trampoline([../p3-wasm-tier/04-trampoline](../p3-wasm-tier/04-trampoline.md))能在「函数边界」切换 crescent / gibbous,但**不能跨 Wasm 函数挂起**(Wasm 1.0 没有 stack switching,P3 用的是 wazero 同步调用);
2. **yield 在 Wasm 编译形态下相当于「在函数中间挂起」**——必须是同步调用边界才能切,Wasm 函数内部点不能挂起。

所以:**任何可能 yield 的 Proto 都不能编到 Wasm**。这是 F2 的硬约束。

> **更精确的语义**:Lua 5.1 的 yield-across-C-boundary 限制(08 §1.3:「不允许在 pcall/metamethod 内 yield」)在编译层放大成「不允许在 Wasm 函数内 yield」——这是 P3 借用 Wasm runtime 的代价。P5 trace JIT 才有自管栈,理论上可支持 yield(但工程复杂度极高)。

#### 3.2.3 「保守近似」算法(F2 是 P2 最复杂的判定)

精确判定「Proto 是否可能 yield」需要**全程序调用图分析**——展开所有调用边、传递地标记「可 yield」函数、求闭包(到不动点)。这是经典的 **interprocedural reachability** 问题,涉及:

1. 间接调用(经 upvalue / 表字段 / 参数传入的 `f`)的目标解析;
2. 元方法调用的目标解析(`a + b` 可能调 `__add`,而 `__add` 可能 yield);
3. 跨 Proto 边界(子 Proto 是否 yield 影响父 Proto)。

**全程序分析在 P2 初版不做**(工程量过大,且静态精确分析在动态语言里本身有不可判定性,任何上界估计都是保守的)。**P2 初版用如下保守近似算法**:

#### 算法 F2-Conservative

```
INPUT:  *ast.FuncExpr fn
OUTPUT: yieldRisk ∈ {known_safe, unknown}

let visitor := compilabilityVisitor{}
ast.Walk(visitor, fn.Body)

if visitor.callsYield      → return unknown   // (a) 直接 yield
if visitor.callsResume     → return unknown   // (a') 调 resume(可能在被调函数 yield)
if visitor.callsCoroutine  → return unknown   // (a'') 任何 coroutine.* 调用都保守不编译
if visitor.callsUnknownFn  → return unknown   // (b) 调用了无法静态确定不 yield 的函数
return known_safe
```

其中:

- `callsYield` / `callsResume` / `callsCoroutine`:**AST 上看到 `coroutine.yield/resume/wrap/create/...`** 调用即标记。识别方式见 §3.2.4。
- **`callsUnknownFn`(本算法的核心保守点)**:函数体内调用了一个**无法静态确定不 yield** 的函数。具体地:
  - 调用 `f(args)`,其中 `f` 是 **upvalue / 全局 / 参数 / 表字段**——目标不可静态解析,**保守标记**;
  - 调用 `f(args)`,其中 `f` 是**显式 local**(声明可见)且指向当前 Proto 的某子 Proto,**不视为 unknown**(继续递归判该子 Proto);
  - 元方法调用(`+ - * /` 等运算可能触发 `__add` 等):**全部保守标记 callsUnknownFn**(因为 metamethod 的目标不可静态解析,且 metamethod 里可能 yield)。

> **关键保守点解释**:**任何不可静态解析目标的调用都视作可能 yield**。这把绝大多数有外部调用的函数判 `CompNotCompilable`——是个**很严苛的判定**,但安全。漏判面估算:262 脚本审计中,**绝大多数计算热点**(纯算术 / 表索引循环)**没有外部调用**,F2 不影响它们;**有外部调用的函数**(用 IO、用 os、用复杂库)本来就多半不是热点 —— **漏判面与加速面错位极小**。

#### 3.2.4 `coroutine.*` 调用的 AST 识别

主流 Lua 项目 `coroutine.yield` 的写法有几种:

```lua
-- (1) 全局直接调
coroutine.yield(v)

-- (2) 局部别名
local yield = coroutine.yield
yield(v)

-- (3) 间接(通过 table)
local co = require("coroutine")
co.yield(v)

-- (4) 经传入参数
local function caller(yieldFn, v)
    yieldFn(v)
end
caller(coroutine.yield, x)
```

**P2 初版能识别的写法**:(1) 直接调——AST 上是 `CallExpr{ Fn: IndexExpr{ Obj: NameExpr{"coroutine"}, Key: StringExpr{"yield"} } }` 的精确模式匹配。

**P2 初版判保守(callsUnknownFn)的写法**:(2) (3) (4) ——**只要 `f` 是 local / upvalue / 参数 / 表字段,而 f 的具体目标不在 AST 上可见**,统一标 callsUnknownFn(算法 F2-Conservative 的(b))。这等价于:**绝大多数有外部调用的函数都标不可编译**。

**结论**:F2 的精确识别只对(1)生效,但通过 callsUnknownFn 把(2)(3)(4)全保守覆盖——**保守正确,精确性留 §9 缺口**。

#### 3.2.5 元方法调用的保守处理

Lua 的算术 / 比较 / 索引等运算可能触发 metamethod:

```lua
local t = setmetatable({}, { __add = coroutine.yield })  -- 极端构造
return t + 1   -- 这里实际会调 yield
```

虽然这是极端构造,但语义上**任何 `+ - * / % ^ .. < <= ==` / `t[k]` / `t[k]=v` / `#t` / `-x` / `not x` 都可能调用 metamethod**——而 metamethod 是用户 Lua 函数,可能含 yield。

**保守处理**:`compilabilityVisitor` 看到任何 BinExpr / UnExpr / IndexExpr / Index 赋值时**保守标 callsUnknownFn**?——不,这样太严苛(几乎所有函数都判不可编译)。

**P2 初版的折中策略**:

- **对 `BinExpr` / `UnExpr` 不标 callsUnknownFn**——理由:绝大多数算术运算的操作数是 number(IC feedback 会确认),metamethod 调用是**罕见路径**,P3 编译时可生成 metamethod 边界检查,在边界处走解释器(详见 [05-p3-p4-interface](./05-p3-p4-interface.md) 的 P3 编译协议);
- **对 `IndexExpr` / Index 赋值同理**——P3 编译生成 `__index` / `__newindex` metamethod 边界检查;
- **不在 F2 一刀切**;**真正不可编译的形状由 F7 (P3 后端能力查询)兜底**——如果 P3 后端不支持某 metamethod 的边界编译,F7 标不可编译。

> **关键决策**:F2 不为 metamethod 一刀切——把这个责任推给 F7 与 P3 后端的能力查询。这让 F2 聚焦在「显式协程调用」上,保守面可控。**P3 编译时如果发现 metamethod 路径过复杂,在 SupportsAllOpcodes 阶段标不可编译**(F7),不需 F2 重复判。

#### 3.2.6 Go visitor 代码骨架(F2)

```go
// compilabilityVisitor 字段(部分,完整定义见 §4):
type compilabilityVisitor struct {
    callsYield     bool   // (a) coroutine.yield 直接调
    callsResume    bool   // (a') coroutine.resume 直接调
    callsCoroutine bool   // (a'') 任何 coroutine.* 调用
    callsUnknownFn bool   // (b) 调用了无法静态确定不 yield 的函数
    // F3 / F4 / F5 / F6 字段后续节定义
    // ...
}

// 识别 coroutine.* 调用
func (v *compilabilityVisitor) VisitCallExpr(e *ast.CallExpr) {
    if isCoroutineCall(e.Fn) {
        v.callsCoroutine = true
        if methodName(e.Fn) == "yield"  { v.callsYield = true }
        if methodName(e.Fn) == "resume" { v.callsResume = true }
        return
    }
    // 任何不能静态确定为「local 指向已知子 Proto」的调用
    // 都视作 unknown(保守 F2-Conservative 的(b))
    if !isKnownLocalCall(e.Fn) {
        v.callsUnknownFn = true
    }
}

// isCoroutineCall 检测精确模式 coroutine.<method>(...)
func isCoroutineCall(fn ast.Expr) bool {
    idx, ok := fn.(*ast.IndexExpr)
    if !ok { return false }
    obj, ok := idx.Obj.(*ast.NameExpr)
    if !ok || obj.Name != "coroutine" { return false }
    key, ok := idx.Key.(*ast.StringExpr)
    if !ok { return false }
    _ = key  // 任意 coroutine.X 都保守标 callsCoroutine
    return true
}

// methodName 取 coroutine.X 的 X
func methodName(fn ast.Expr) string {
    if idx, ok := fn.(*ast.IndexExpr); ok {
        if key, ok := idx.Key.(*ast.StringExpr); ok {
            return key.Val
        }
    }
    return ""
}

// isKnownLocalCall:f 必须是 local 且指向已知子 Proto
// (P2 初版只支持「直接 local 名指代某子 Proto」,其他情况一律 unknown)
func isKnownLocalCall(fn ast.Expr) bool {
    name, ok := fn.(*ast.NameExpr)
    if !ok { return false }
    // (实现期由 codegen 上下文提供 local 解析,P2 初版可全返 false 最保守)
    _ = name
    return false  // 默认全部保守标 unknown
}
```

> **初版极限保守**:`isKnownLocalCall` P2 初版**直接返 false**——所有调用都标 callsUnknownFn,意味着**任何含调用的函数都判不可编译**。这看起来很严,但 §9 缺口讨论的「精确 yield 调用图分析」(P2+)上线后再放宽。**先建好闸门、再调精度**是工程纪律。

#### 3.2.7 假阳性容忍与漏判预测

**预期漏判面**(初版 isKnownLocalCall 全返 false):任何含调用的函数都判 F2 不可编译。在 262 脚本审计的负载里:

- **纯计算热点**(数值循环、表遍历、比较运算)无外部调用 → **不受 F2 影响**,正常可编译;
- **有少量调用的辅助函数**(调 `string.format` / `math.max` / 等内置)→ **被 F2 误漏**,判不可编译。

漏判的「损失加速」面**取决于热点是否含调用**——纯热循环不含调用是常态,F2 的初版严格度对热点加速面影响小。**实测后再观察是否需要在 §9 缺口推进精确分析**。

#### 3.2.8 与 P5 trace JIT 的边界

P5 trace JIT(roadmap §4 P5)有自管栈,理论上能支持 yield(trace 在 yield 点出 trace,回 P1 解释器再续 trace)。**本文 F2 不为 P5 服务**——本文只判 P3 (Wasm) 编译能力。P5 上线时会另开一篇「trace 可编译性」文档,不复用本文规则。

### 3.3 F3:debug 库使用

#### 3.3.1 形状定义

函数体内**引用了 `debug` 表**——任一形式:

```lua
debug.traceback()                  -- (a) 直接调
debug.getlocal(1, 1)               -- (b) 内省调用栈
debug.sethook(handler, "c")        -- (c) 注册 hook
local d = debug                    -- (d) 把 debug 表传出(后续可能内省)
```

#### 3.3.2 为什么不编译

[10 §debug 库](../p1-interpreter/10-stdlib.md):`debug.*` 提供运行期内省/篡改能力——`getlocal/setlocal` 读写当前帧的局部变量、`getinfo` 读 PC/源行/函数信息、`sethook` 在 opcode/call/return 触发 Lua callback。

P3 把 Proto 编译成 Wasm 函数后:

1. **帧布局变成 Wasm locals**([../p3-wasm-tier/02-translation](../p3-wasm-tier/02-translation.md) §2),不再是 P1 的「值栈寄存器」(01 §5.6)——`debug.getlocal` 的 `(level, idx)` 寻址协议无法对应到 Wasm locals;
2. **PC 在 Wasm 编译形态下不存在**——P3 用 Wasm 控制流,无 Lua opcode PC,`debug.getinfo().currentline` 拿不到;
3. **sethook 需要在 opcode 边界回调**——Wasm 编译后无 opcode 边界,sethook 无落点。

任一 debug 内省能力在编译层都**无法保证语义**。**统一保守:任何函数引用了 `debug` 表就不编译**。

#### 3.3.3 AST 识别

**P2 初版策略**:**任何 `NameExpr{"debug"}` 出现即标记**——不区分是真的调 `debug.*` 还是只是把表名传进函数,统一保守。

```go
func (v *compilabilityVisitor) VisitNameExpr(e *ast.NameExpr) {
    if e.Name == "debug" {
        v.usesDebug = true
    }
    // setfenv/getfenv 同理(F4),见 §3.4
}
```

> **可能的假阳性**:用户定义了一个**局部变量**也叫 `debug`(`local debug = {}`),visitor 也会标 `usesDebug`——这是「漏判」(把用户的合法变量误判为 debug 库使用)。**P2 初版接受**,因为局部变量与全局重名的情况罕见,且用 `debug` 命名局部变量是反模式。

**精确识别留 §9 缺口**:scope-aware AST 分析(局部 `debug` 屏蔽全局)——但这需要 codegen 的作用域信息,P2 初版不做。

#### 3.3.4 与 traceback 的特殊关系

`debug.traceback`(09 §13)是常用错误处理工具,经常出现在 `xpcall(fn, debug.traceback)` 这类形态中。**P2 初版仍判不可编译**——理由:

- `xpcall(fn, debug.traceback)` 的 **fn 才是热点**,traceback 只是 error handler;
- error handler 不在主路径上;
- 即使 `fn` 调了 `debug.traceback`(自己捕获 trace),也是错误路径,不是性能热点。

**漏判面极小**:debug 库使用通常是错误处理 + 调试工具,不是计算热点。

#### 3.3.5 Go visitor 代码骨架(F3)

```go
// compilabilityVisitor 字段:usesDebug bool
// (与 F2 共用 visitor,统一遍历一次 AST)

func (v *compilabilityVisitor) VisitNameExpr(e *ast.NameExpr) {
    switch e.Name {
    case "debug":
        v.usesDebug = true
    case "setfenv", "getfenv":
        v.usesSetfenv = true            // F4
    }
}
```

### 3.4 F4:setfenv / getfenv

#### 3.4.1 形状定义

函数体内调用 `setfenv` 或 `getfenv`:

```lua
setfenv(1, newEnv)                 -- 改当前函数环境
local oldEnv = getfenv(2)          -- 取上层函数环境
setfenv(f, sandboxEnv)             -- 改其他函数环境
```

#### 3.4.2 为什么不编译

`setfenv` 改 `_ENV`(Lua 5.1 的环境表),让全局访问的目标表**运行期可变**——**IC slot 的全局访问特化**(02 §7 / 05 §6:GETGLOBAL/SETGLOBAL 的 IC 缓存了 `_ENV[k]` 的 shape)在 setfenv 后失效。

更严重的是 **P4 类型投机**:P4 用 IC feedback 假设全局表稳定,setfenv 让这个假设无效——P4 的 deopt 着陆机制能兜底,但 P3 没有 deopt,**只能不编译**。

#### 3.4.3 AST 识别

`NameExpr{"setfenv"}` / `NameExpr{"getfenv"}` 任一出现即标记——见 §3.3.5 的 visitor 代码。

> **范围**:Lua 5.1 的 `setfenv` 是全局函数,**任何引用名 `setfenv` 都保守标 F4**。同 F3 的 `debug` 表名一样,可能漏掉「用户重定义局部 setfenv」(罕见反模式),P2 初版接受。

#### 3.4.4 漏判面预测

`setfenv` / `getfenv` 在生产代码里主要见于**沙箱 / 模板渲染**等场景,**不是计算热点**——漏判面极小,与 F3 同档。

### 3.5 F5:过大函数

#### 3.5.1 形状定义

Proto 的指令数 / 寄存器数超阈值:

| 阈值常量 | 值(初版建议) | 来源 |
|---|---|---|
| `MaxCompilableInsns` | 2000 | `len(Proto.Code)` 上限 |
| `MaxCompilableRegs` | 200 | `Proto.MaxStack` 上限(寄存器数) |

任一超出即判不可编译。

#### 3.5.2 为什么不编译

「过大」是工程性而非语义性约束:

1. **编译时间长**:P3 编译大函数生成 Wasm module 时间随指令数线性增长——大函数编译占用编译预算多,降低系统响应性;
2. **Wasm module 体积大**:每个 Wasm function 的字节码占 instruction count × 平均字节数(估 4-8 bytes/insn),大函数膨胀 Wasm module,增加 wazero 解析与加载开销;
3. **icache 压力**:大函数的执行模式不利于 CPU 指令缓存——P1 解释器的字节码 fetch 已经是流水,P3 编译大函数后机器码占用更多 icache 行;
4. **大函数常是「初始化」非热循环**——工程经验:大函数(几千指令)多是 main / setup / table literal,不是高频小循环(高频小循环往往几十到几百指令)。

理由 1+2+3 是「编译大函数收益递减」的本质;理由 4 是「漏判可接受」的工程支撑。

#### 3.5.3 阈值定标

`MaxCompilableInsns = 2000` / `MaxCompilableRegs = 200` 是初版建议值,理由:

- 2000 指令大约对应 100-300 行 Lua 代码——绝大多数热点函数远小于此;
- 200 寄存器是 Lua 5.1 单函数的合理上限(02 §1 max regs ≈ 250),设 200 给 50 的余量。

**实测后定标**:在 pineapple 真实负载上跑一次,看 90% 分位的热点函数指令数与寄存器数,据此调整。详见 §9 缺口。

#### 3.5.4 Proto 层识别(无需 AST)

F5 完全在 Proto 层判,**不需要 AST visitor**:

```go
const (
    MaxCompilableInsns = 2000
    MaxCompilableRegs  = 200
)

func (b *Bridge) checkF5OverSize(proto *bytecode.Proto) bool {
    if len(proto.Code) > MaxCompilableInsns {
        return true                         // 过大
    }
    if int(proto.MaxStack) > MaxCompilableRegs {
        return true
    }
    return false
}
```

`analyzeProto` 在 visitor 跑完后调一次 `checkF5OverSize`(§5.4 完整入口)。

#### 3.5.5 假阳性容忍

F5 的「漏判」(把可编译的大函数判不可编译)预期面小——大函数本来就不是热点。如果实测发现某真实负载的热点是大函数(罕见),再调高阈值。

**误判方向**:F5 不可能误判——指令数与寄存器数是确定数。

### 3.6 F6:深嵌套闭包 / 复杂 upvalue 捕获

#### 3.6.1 形状定义

嵌套闭包深度 / upvalue 数超阈值:

| 阈值常量 | 值(初版建议,**严**) | 来源 |
|---|---|---|
| `MaxClosureDepth` | 3 | 函数嵌套深度(`function` 内套 `function`) |
| `MaxUpvalCount` | 8 | `Proto.UpvalDescs` 长度上限 |

任一超出即判不可编译。

#### 3.6.2 为什么不编译

`upvalue` 在 Lua 5.1 有「**开放 / 关闭**」两态(05 §8.3):

- **开放 upvalue** 指向**栈上槽位**(外层函数的 local 还活在栈上时);
- **关闭 upvalue** 指向**堆上独立 cell**(外层函数返回后,upvalue 被「关闭」搬到堆);
- 一个 upvalue 在生命期内可能从开放转关闭(`CLOSE` opcode,02 §4 编号 36)。

**为什么 P3 编译复杂 upvalue 难**:

1. **开放 upvalue 共享栈槽**——P3 把 Lua 寄存器编进 Wasm locals 时,「这个 local 同时被外层函数的 upvalue 指向」语义不能编译成纯 Wasm locals(Wasm locals 是值,不是引用);
2. **upvalue 关闭时机**——Lua 5.1 在 block 退出时关闭对应 upvalue,P3 编译形态下需要在 Wasm 函数边界明确「关闭」时机;
3. **嵌套层数越深,上述协议越复杂**。

**P2 初版保守排除**:嵌套深度 > 3 或 upvalue 数 > 8 即判不可编译。这两个阈值「严」是故意的——`MaxClosureDepth=3` 几乎排除了所有有嵌套的函数,工程经验里嵌套 1-2 层是绝大多数,3 层以上就稀有了。

> **upvalue 编译协议成熟后放宽**:P3 上线时如果 upvalue 编译协议([../p3-wasm-tier/02-translation](../p3-wasm-tier/02-translation.md) §3.7 待补)定义清楚,可以放宽 F6 阈值——届时本文 §9 缺口会引导调整。**P2 初版极保守是为了不踩 P3 还没解决的工程问题**。

#### 3.6.3 AST + Proto 双层识别

- **嵌套深度**:AST visitor 进入 `*ast.FuncExpr` 时栈 +1,退出时 -1,记录最大值;
- **upvalue 数**:Proto 的 `UpvalDescs` 长度直接给。

```go
// compilabilityVisitor 字段:
//   maxClosureDepth int  // AST 嵌套深度
//   currentDepth    int  // 当前递归深度

func (v *compilabilityVisitor) VisitFuncExpr(e *ast.FuncExpr) {
    v.currentDepth++
    if v.currentDepth > v.maxClosureDepth {
        v.maxClosureDepth = v.currentDepth
    }
    ast.Walk(v, e.Body)                     // 递归进入子函数
    v.currentDepth--
}

const (
    MaxClosureDepth = 3
    MaxUpvalCount   = 8
)

func (b *Bridge) checkF6Closure(proto *bytecode.Proto, v *compilabilityVisitor) bool {
    if v.maxClosureDepth > MaxClosureDepth {
        return true
    }
    if len(proto.UpvalDescs) > MaxUpvalCount {
        return true
    }
    return false
}
```

#### 3.6.4 关于「嵌套 Proto 独立判定」的与 F6 的张力

§7 会论证**嵌套 Proto 独立判定**——外层函数不可编译不等于内层不可编译。但 F6 的「嵌套深度 > 3」是**针对当前 Proto 自身的嵌套深度**——不是看外层有没有把它套住。

举例:

```lua
-- 主 chunk(外层)
function outer()                            -- 深度 0
    return function()                       -- 深度 1
        return function()                   -- 深度 2
            return function()               -- 深度 3
                return function() end       -- 深度 4 ← 触发 F6
            end
        end
    end
end
```

最内层那个空函数自身的嵌套深度是 4(因为它自身是 function expr 的最深层),触发 F6。

**实际上的语义**:F6 的 `maxClosureDepth` 是「**当前 Proto 内部还嵌了几层**」,不是「**当前 Proto 在外面被嵌了几层**」。这个细节实现期容易搞反——visitor 在进入子函数时 +1 / 退出时 -1 / 记最大值,得到的是「当前函数体内最大嵌套深度」(不含自身)。

> **建议实现**:visitor 在 `VisitFuncExpr` 入口先记 `maxClosureDepth = max(maxClosureDepth, currentDepth)`,然后 `currentDepth++` 进入子函数。这样 currentDepth 表示「我在第几层 function 里」,maxClosureDepth 表示「我看到的最深层」。`analyzeProto` 调用时 currentDepth=0,因此对自身函数的嵌套深度从 0 开始计。

### 3.7 F7:P3 后端能力查询

#### 3.7.1 形状定义

Proto 的 opcode 集合**不全在** P3 后端的「已支持 opcode 集」内。

#### 3.7.2 为什么不编译

P3 后端([../p3-wasm-tier/02-translation](../p3-wasm-tier/02-translation.md))是渐进开发的——上线初期可能只支持 ISA 0..37 的子集(比如 `LOADK / GETTABLE / CALL / RETURN / FORLOOP / JMP` 等热路径 opcode),其他 opcode 翻译尚未实现。**任何含未支持 opcode 的 Proto 都不能编译**——直到 P3 后端把对应 opcode 翻译写完。

> 注意 [02 §4](../p1-interpreter/02-bytecode-isa.md) 的 opcode 编号 0..37 全部已分配,38..63 预留(00 §3 关键耦合 5)——F7 检查的是 0..37 这个集合的子集覆盖。

#### 3.7.3 接口协议:`P3Compiler.SupportsAllOpcodes`

本文定义 P2 调 P3 的查询接口形状(实际实现见 [05-p3-p4-interface](./05-p3-p4-interface.md) §2):

```go
// P3Compiler 是 P2 与 P3 之间的接口(05 §2 单一事实源)
type P3Compiler interface {
    // SupportsAllOpcodes 检查 Proto 中的所有 opcode 是否都在 P3 后端的支持集内。
    // 实现者:扫一遍 proto.Code,对每条 instruction 取 op,查 P3 后端的支持表。
    // 任一未支持即返回 false。
    SupportsAllOpcodes(proto *bytecode.Proto) bool

    // Compile 实际触发编译(05 §1 完整签名)
    Compile(proto *bytecode.Proto, fb *TypeFeedback) (*GibbousCode, error)
    // ...
}
```

P2 的 `Bridge` 持 `p3 P3Compiler`,`analyzeProto` 在 visitor 跑完 + 各形状判定后调 `b.p3.SupportsAllOpcodes(proto)`,任一未支持即判 `CompNotCompilable`(F7)。

#### 3.7.4 「保守缺省」原则

**P3 后端的支持集默认严格**——只把**明确实现的 opcode** 加入支持集,其他全部不支持。这与「保守第一」一脉相承:**未明确支持的就不编译**。

P3 后端开发期会逐步扩充支持集——每实现一条 opcode 翻译就把它加进支持表,F7 自动放行更多 Proto。这让 P3 后端能力的成长**自然反映**到 P2 的可编译性判定上。

#### 3.7.5 与 F1-F6 的关系:F7 是兜底

F1-F6 是**形状级**判定——不可编译的形状一开始就排除;F7 是**opcode 级**判定——形状 OK 但 opcode 没全实现也不能编译。**F7 是 F1-F6 的兜底**,不重复判:

- F1 (vararg) 已经把含 VARARG opcode 的 Proto 排除——但 F7 会再确认一次(纵深防御);
- F2 (yield) 没在 opcode 层判——只有 AST 层;若 P3 后端不支持某些 metamethod 路径(F2 §3.2.5 提到推给 F7),F7 把它们排除;
- F3 / F4 (debug / setfenv) 同 F2,opcode 层无对应;
- F5 / F6 是工程量阈值,与 opcode 集无关;
- F7 兜住所有「形状 OK 但 P3 还没实现」的 Proto。

**实现顺序**:`analyzeProto` 先跑 F1-F6,**全部通过才调 F7**——因为 SupportsAllOpcodes 要扫整个 Code,有点贵,前置形状判能快速拒掉大半 Proto。

#### 3.7.6 Go 代码骨架(F7)

```go
// internal/bridge
func (b *Bridge) checkF7BackendSupport(proto *bytecode.Proto) bool {
    if b.p3 == nil {
        // mock 或 P1-only build:无 P3,F7 永远判不可编译
        // (不可编译就永远走解释,与 P1 行为等价)
        return true
    }
    return !b.p3.SupportsAllOpcodes(proto)
}
```

**P1-only build 退化**:`b.p3 == nil` 时 F7 恒判不可编译——所有 Proto 永久解释,与无 P2 行为一致。这与 §2.6 的 P1-only fallback 闭环。

---

## 4. compilabilityVisitor 完整设计

### 4.1 visitor 总体结构

把 §3 的各形状 visitor 逻辑合在一起,得到完整的 `compilabilityVisitor`:

```go
package bridge

import (
    "github.com/<...>/wangshu/internal/frontend/ast"
)

// compilabilityVisitor 在一次 AST 遍历中收集所有不升层形状信号(F1-F4 + F6)。
// F5 / F7 在 Proto 层判,不进 visitor。
//
// 单线程使用:每次 analyzeProto 新建一个 visitor,跑完即弃(visitor 不复用)。
type compilabilityVisitor struct {
    // F1: vararg
    // (主判定看 FuncExpr.IsVararg,visitor 兜底捕捉 VarargExpr)
    sawVararg bool

    // F2: 协程相关
    callsYield     bool   // 直接调 coroutine.yield
    callsResume    bool   // 直接调 coroutine.resume
    callsCoroutine bool   // 任何 coroutine.* 调用
    callsUnknownFn bool   // 调用了无法静态确定不 yield 的函数

    // F3: debug 库
    usesDebug bool

    // F4: setfenv / getfenv
    usesSetfenv bool

    // F6: 嵌套深度
    currentDepth    int    // 当前 AST 递归深度(进 FuncExpr +1, 退 -1)
    maxClosureDepth int    // 见过的最大深度

    // 内部:visitor 错误恢复(panic-safe)
    err error
}

// reasonsBitmap 收集 visitor 的判定信号位图(§5.2 用)
type reasonsBitmap uint16
const (
    reasonVararg     reasonsBitmap = 1 << iota
    reasonYield
    reasonResume
    reasonCoroutine
    reasonUnknownCall
    reasonDebug
    reasonSetfenv
    reasonOverSize
    reasonOverRegs
    reasonNestedDeep
    reasonOverUpval
    reasonBackendUnsupp
)
```

### 4.2 visitor 的 visit 方法

```go
// Visit 实现 ast.Visitor 接口(走访每个 AST 节点)
func (v *compilabilityVisitor) Visit(n ast.Node) ast.Visitor {
    switch e := n.(type) {
    case *ast.VarargExpr:
        v.sawVararg = true
        return v
    case *ast.NameExpr:
        v.visitNameExpr(e)
        return v
    case *ast.CallExpr:
        v.visitCallExpr(e)
        return v
    case *ast.MethodCallExpr:
        v.visitMethodCallExpr(e)
        return v
    case *ast.FuncExpr:
        v.visitFuncExpr(e)
        return nil  // 子函数已在 visitFuncExpr 内独立递归(深度计数)
    }
    return v   // 默认:递归子节点
}

// visitNameExpr 捕获 debug / setfenv / getfenv / coroutine 等关键名字
func (v *compilabilityVisitor) visitNameExpr(e *ast.NameExpr) {
    switch e.Name {
    case "debug":
        v.usesDebug = true
    case "setfenv", "getfenv":
        v.usesSetfenv = true
    }
}

// visitCallExpr 处理 f(args) 形态
func (v *compilabilityVisitor) visitCallExpr(e *ast.CallExpr) {
    // 1. 识别 coroutine.* 调用
    if isCoroutineCall(e.Fn) {
        v.callsCoroutine = true
        m := methodName(e.Fn)
        if m == "yield"  { v.callsYield = true }
        if m == "resume" { v.callsResume = true }
        return
    }

    // 2. 识别 debug.* 调用(F3 加强:即使 NameExpr 已捕获也再确认)
    if isDebugCall(e.Fn) {
        v.usesDebug = true
        return
    }

    // 3. 识别 setfenv/getfenv 直接调用(F4 加强)
    if name, ok := e.Fn.(*ast.NameExpr); ok {
        if name.Name == "setfenv" || name.Name == "getfenv" {
            v.usesSetfenv = true
            return
        }
    }

    // 4. 一般调用:无法静态确定目标 → 保守标 unknown
    if !isKnownLocalCall(e.Fn) {
        v.callsUnknownFn = true
    }
}

// visitMethodCallExpr 处理 obj:m(args) 形态——同样保守
func (v *compilabilityVisitor) visitMethodCallExpr(e *ast.MethodCallExpr) {
    // obj:m() 几乎总是 unknown(对象的方法表无法静态解析)
    v.callsUnknownFn = true
}

// visitFuncExpr 处理嵌套函数定义,管理 currentDepth / maxClosureDepth
func (v *compilabilityVisitor) visitFuncExpr(e *ast.FuncExpr) {
    // 进入子函数前先 +1,记录当前深度
    v.currentDepth++
    if v.currentDepth > v.maxClosureDepth {
        v.maxClosureDepth = v.currentDepth
    }
    // 子函数自身若是 vararg / 含 yield ——它会被独立 analyzeProto,
    // 这里**只判嵌套深度**,不递归判其内容(嵌套 Proto 独立判定,§7)。
    // 但深度本身要传递,所以仍要遍历 e.Body 以拿到深度峰值。
    ast.Walk(v, e.Body)
    v.currentDepth--
}
```

### 4.3 辅助识别函数

```go
// isCoroutineCall 检测 coroutine.<method>(...) 模式
func isCoroutineCall(fn ast.Expr) bool {
    idx, ok := fn.(*ast.IndexExpr)
    if !ok { return false }
    obj, ok := idx.Obj.(*ast.NameExpr)
    if !ok || obj.Name != "coroutine" { return false }
    _, ok = idx.Key.(*ast.StringExpr)
    return ok
}

// isDebugCall 检测 debug.<method>(...) 模式
func isDebugCall(fn ast.Expr) bool {
    idx, ok := fn.(*ast.IndexExpr)
    if !ok { return false }
    obj, ok := idx.Obj.(*ast.NameExpr)
    if !ok || obj.Name != "debug" { return false }
    _, ok = idx.Key.(*ast.StringExpr)
    return ok
}

// methodName 取 <table>.<method> 中的 method
func methodName(fn ast.Expr) string {
    if idx, ok := fn.(*ast.IndexExpr); ok {
        if key, ok := idx.Key.(*ast.StringExpr); ok {
            return key.Val
        }
    }
    return ""
}

// isKnownLocalCall:f 是否是「已知 local 名,指向当前 Proto 的某子 Proto」
//
// P2 初版:**直接返 false**(全部保守标 unknown)。
// 精确实现需要 codegen 上下文(local 名表 + 子 Proto 解析),P2 初版不引入。
// §9 缺口讨论的「精确 yield 调用图分析」会引入它。
func isKnownLocalCall(fn ast.Expr) bool {
    return false
}
```

### 4.4 visitor 与 ast.Walk 的协议

`ast.Walk(visitor, node)` 是 [04 §3](../p1-interpreter/04-frontend-parser-codegen.md) 的 AST 遍历入口(细节待 04 落地后定),约定:

- 调 `visitor.Visit(node)` ——返回值是「是否继续递归子节点」;
- 返回 `nil` 表示「这个节点的子节点不递归」(visitor 自己处理子节点,如 visitFuncExpr 自己递归 Body);
- 返回非 nil(通常是 visitor 自身)表示「按 ast.Walk 默认递归子节点」。

这是标准的 Go AST visitor pattern(对应 `go/ast.Walker`)——visitor 通过返回值控制遍历策略。

### 4.5 panic safety 与错误处理

visitor 在遍历期不抛 Go panic——任何识别失败(意外的 AST 形状)走「**保守路径**」:**记不下来的形状视作不可编译**。具体地:

- visitor 见到未知 AST 节点类型(switch 没匹配)→ 默认递归(保守)+ 不影响判定;
- AST 树深度过深(避免栈溢出)→ visitor 加最大递归深度限制(比如 1000),触发即直接标 callsUnknownFn(保守判不可编译);
- visitor 内部出现意外状态 → 记 `v.err`,`analyzeProto` 检查 `err != nil` 即判 `CompNotCompilable`(保守)。

「保守优于完美」是 visitor 的工程铁律——**任何不确定的情况都判不可编译**。

---

## 5. analyzeProto 入口函数 + Compilability 枚举 + Proto 旁缓存

### 5.1 Compilability 枚举

```go
// internal/bridge/compilability.go

// Compilability 描述一个 Proto 的静态可编译性判定结果。
// 三态:Unknown(未分析)/ Compilable(可编)/ NotCompilable(不可编)。
type Compilability uint8

const (
    // CompUnknown:Compile 尚未跑 analyzeProto(或 P1-only build 未启 P2)。
    // 04 状态机视同 NotCompilable(§2.6 + 04 §3),保守不升层。
    CompUnknown      Compilability = iota

    // CompCompilable:可编译——F1-F7 全部通过,可参与升层决策。
    // 升层后 04 状态机可调 P3 编译。
    CompCompilable

    // CompNotCompilable:不可编译——F1-F7 任一触发,永久解释。
    // 04 状态机的 considerPromotion 直接跳过此 Proto(永远 tier-0)。
    CompNotCompilable
)

func (c Compilability) String() string {
    switch c {
    case CompCompilable:    return "Compilable"
    case CompNotCompilable: return "NotCompilable"
    default:                return "Unknown"
    }
}
```

### 5.2 analyzeProto 入口函数

```go
// AnalyzeProto 是 Compile 时被 codegen 回调的可编译性分析入口。
// 与 04 的 compile.Gen 对接(§2.5 接口规约 + §10 回填请求):
//   compile.Gen 在产出 Proto 后,调 b.AnalyzeProto(funcBody, proto),
//   把结果写进 proto 旁的 ProfileData.compilable(§5.3)。
//
// 不变式:
//   1. 一次分析,结果不变——Compile 完成后 compilable 不再修改;
//   2. 保守优先——任一信号触发即判 NotCompilable;
//   3. AST 用完即弃——本函数返回后不再持有 fn 引用(§2.4 决策方案①)。
func (b *Bridge) AnalyzeProto(fn *ast.FuncExpr, proto *bytecode.Proto) Compilability {
    // visitor 遍历 AST 收集 F1-F4 + F6 信号
    v := &compilabilityVisitor{}
    ast.Walk(v, fn.Body)

    // 收集判定 reasons(便于 §5.3 调试日志)
    var reasons reasonsBitmap

    // F1: vararg(三重识别 §3.1.3)
    if fn.IsVararg || v.sawVararg || protoIsVararg(proto) {
        reasons |= reasonVararg
    }
    // **AST 与 Proto 必须一致**(§2.3 不变式 1):
    //   AST.IsVararg 与 Proto.IsVararg 不一致 = codegen bug,panic
    if fn.IsVararg != proto.IsVararg {
        panic(fmt.Sprintf("compilability: AST/Proto IsVararg mismatch (ast=%v, proto=%v)",
            fn.IsVararg, proto.IsVararg))
    }

    // F2: 协程相关(§3.2 保守近似算法)
    if v.callsYield     { reasons |= reasonYield }
    if v.callsResume    { reasons |= reasonResume }
    if v.callsCoroutine { reasons |= reasonCoroutine }
    if v.callsUnknownFn { reasons |= reasonUnknownCall }

    // F3 / F4: debug / setfenv
    if v.usesDebug   { reasons |= reasonDebug }
    if v.usesSetfenv { reasons |= reasonSetfenv }

    // F5: 过大函数(Proto 层)
    if len(proto.Code) > MaxCompilableInsns {
        reasons |= reasonOverSize
    }
    if int(proto.MaxStack) > MaxCompilableRegs {
        reasons |= reasonOverRegs
    }

    // F6: 深嵌套 / 复杂 upvalue
    if v.maxClosureDepth > MaxClosureDepth {
        reasons |= reasonNestedDeep
    }
    if len(proto.UpvalDescs) > MaxUpvalCount {
        reasons |= reasonOverUpval
    }

    // F7: P3 后端能力查询(放最后,F1-F6 全过才查)
    if reasons == 0 && b.checkF7BackendSupport(proto) {
        reasons |= reasonBackendUnsupp
    }

    // 决策与缓存
    var result Compilability
    if reasons != 0 {
        result = CompNotCompilable
    } else {
        result = CompCompilable
    }

    // 写进 Proto 旁缓存(§5.3)
    b.setCompilability(proto, result, reasons)

    // visitor 错误兜底(§4.5)
    if v.err != nil {
        b.setCompilability(proto, CompNotCompilable, reasonBackendUnsupp /* 占位 */)
        return CompNotCompilable
    }

    return result
}

// protoIsVararg 是 §3.1.3 的辅助:扫 Code 看是否含 VARARG opcode(纵深防御)
func protoIsVararg(proto *bytecode.Proto) bool {
    for _, ins := range proto.Code {
        if ins.Op() == bytecode.OpVararg {  // op = 37
            return true
        }
    }
    return false
}
```

### 5.3 Proto 旁缓存与生命期

承 [00-overview](./00-overview.md) §6 决策表「计数器存储:Proto 旁 ProfileData(主)」——`compilable` 字段也挂在 `ProfileData` 上(与计数器同生命期):

```go
// internal/bridge/profile.go(完整定义见 [01-profiling] §1)
type ProfileData struct {
    // (01-profiling 主管的字段)
    BackEdgeCount uint32
    EntryCount    uint32

    // (本文主管的字段)
    Compilable    Compilability
    Reasons       reasonsBitmap   // 仅调试/日志用,非热路径
}

// setCompilability 写入分析结果(Compile 时一次写,后续只读)
func (b *Bridge) setCompilability(p *bytecode.Proto, c Compilability, r reasonsBitmap) {
    pd := b.profileData(p)             // 取 Proto 旁 ProfileData
    pd.Compilable = c
    pd.Reasons = r
}

// CompilabilityOf 是 04 状态机查询入口(只读)
func (b *Bridge) CompilabilityOf(p *bytecode.Proto) Compilability {
    return b.profileData(p).Compilable
}
```

### 5.4 缓存生命期

| 阶段 | `Compilable` 字段值 | 操作 |
|---|---|---|
| 1. `Compile` 调 codegen | (字段尚未存在,Proto 刚创建) | — |
| 2. codegen 末尾 `compile.Gen → b.AnalyzeProto(fn, proto)` | 写真值(`CompCompilable` 或 `CompNotCompilable`) | 一次写 |
| 3. 运行期 04 `considerPromotion(proto)` | 只读 | 读判断 |
| 4. P3 编译失败(04 §3 TierStuck) | 不重写 `Compilable`(它仍是 `CompCompilable`)——TierStuck 是另一字段(04 §3)管 | 不写 |
| 5. Program GC | `ProfileData` 随 Proto 一起回收 | 析构 |

**关键不变式**:`Compilable` 字段**编译期一次写,运行期只读**——不存在并发写,因此无需 atomic / mutex,纯字节读写即可。这是「P2 不在热路径」的具体兑现:`considerPromotion` 读 `Compilable` 是热路径,但只读字段无并发写竞争,**Go memory model 自动保证可见性**(write-once before any reader)。

### 5.5 P1 占位的初值约定

承 [11 §1.3](../p1-interpreter/11-embedding-arena-abi.md) 与 [00-overview](./00-overview.md) §3 关键耦合 3:

- **P1 实现**:`Compile` 不调 `AnalyzeProto`(P1 没有 `internal/bridge`),所有 Proto 的 `ProfileData.Compilable` 留 `CompUnknown`(默认零值);
- **P2 实现**:`Compile` 调 `AnalyzeProto`,所有 Proto 的 `Compilable` 写真值(`CompCompilable` 或 `CompNotCompilable`);
- **04 状态机**:`CompUnknown` 视同 `CompNotCompilable`(再保守一层),`considerPromotion` 直接跳过。

**P1→P2 升级时 Proto 字段不变**(`ProfileData` 字段在 P1 阶段就预留好,P1 不写 `Compilable`,P2 开始写)——这是「接口稳定」(原则 3)在数据布局层的兑现。

---

## 6. 与 11 §1.3 衔接:把 Compile 占位填实

### 6.1 P1 占位的精确语义

[11 §1.3](../p1-interpreter/11-embedding-arena-abi.md) 给 P1 `Compile` 留的占位:

> 「**可编译性探测 P1 占位**:roadmap §8 把『可编译性探测与层级决定』放进 `Compile`,但那是 P2 能力。P1 的 `Compile` 把这步实现为**恒真占位**:所有函数标 tier-0、恒『解释』。接口形状(返回的 Program 带每个 Proto 的『可升层标记』字段)P1 就定好,P2 只填充探测逻辑,不改 API。」

P1 落地时(参考 implementation-progress)`Compile` 的实际行为是「全标可解释,所有 Proto tier-0」——这意味着 P1 的 `Proto.ProfileData.Compilable` 全是 `CompUnknown`(不调 `AnalyzeProto`)。

### 6.2 P2 把占位填实

P2 上线后,**`Compile` 的行为升级**(P1 公共 API 不变,只改实现):

| 维度 | P1(11 §1.3 占位) | P2(本文 §5 填实) |
|---|---|---|
| `wangshu.Compile` 签名 | `func Compile([]byte, string) (*Program, error)` | **完全不变** |
| `wangshu.Program` 公共类型 | `struct{ ... unexported ... }` | **完全不变** |
| Proto.ProfileData.Compilable 初值 | 全 `CompUnknown` | 全真值(`CompCompilable` / `CompNotCompilable`) |
| 谁调 `analyzeProto` | 不调 | `compile.Gen` 末尾调 |

**关键纪律**:**P1 公共 API(wangshu.go 门面)零修改**——这是 [00-overview](./00-overview.md) §3 关键耦合 3 与原则 3「接口稳定」的兑现。宿主代码(`wangshu.Compile(src) → Program; prog.Call(state)`)在 P1→P2 升级时**不需要任何改动**——Compile 内部多跑了一段分析,Program 内部多了一些缓存值,公共形态不变。

### 6.3 接口零变化的实现路径

```go
// wangshu.go(P1 已落地,P2 不改)
func Compile(source []byte, chunkname string) (*Program, error) {
    return compile.CompilePublic(source, chunkname)  // 委托 internal
}

// internal/frontend/compile/compile.go
//   - P1 实现:Gen 不调 bridge
//   - P2 实现:Gen 在末尾调 bridge.AnalyzeProto(若 build tag 启用)
func CompilePublic(source []byte, chunkname string) (*wangshu.Program, error) {
    // ...lex / parse...
    block, err := parse.Parse(lx, chunkname)
    if err != nil { return nil, err }

    proto, err := Gen(block, chunkname)        // codegen
    if err != nil { return nil, err }

    return &wangshu.Program{
        protos: collectProtos(proto),
        // ...
    }, nil
}

// Gen(block) → Proto:codegen 内部
//   - P1:产 Proto 即返回
//   - P2:产 Proto 后调 bridge.AnalyzeProto,把结果写 Proto.ProfileData.Compilable
func Gen(block *ast.Block, chunkname string) (*bytecode.Proto, error) {
    // ...codegen...
    proto := /* ... */

    // ↓↓↓ P2 新增的一步(P1-only build 通过 build tag 跳过)
    if bridge.Enabled() {
        wrapAsFuncExpr := &ast.FuncExpr{        // 主 chunk 包装成 FuncExpr 给 visitor
            Body:     block,
            IsVararg: true,                     // 主 chunk 总是 vararg(命令行参数 ...)
        }
        bridge.AnalyzeProto(wrapAsFuncExpr, proto)
    }
    // ↑↑↑

    return proto, nil
}
```

**实现要点**:

1. **P1-only 部署**通过 build tag(`!profile`)关掉 `bridge.Enabled()`,`AnalyzeProto` 不被调,所有 Proto 留 `CompUnknown`——byte-equal 差分不受影响(§2.6);
2. **P2 部署**通过 `profile` build tag 启用 bridge 包,`AnalyzeProto` 被调,所有 Proto 写真值;
3. **wangshu.go 门面零改**——`Compile / Program` 签名与字段都不动。

> **「主 chunk 总是 vararg」的细节**:Lua 5.1 主 chunk 隐式接受命令行参数 `...`,所以主 chunk 的 IsVararg 永远是 true——这意味着 **主 chunk 永远 `CompNotCompilable`**(F1)。这与 §3.1.5 的论证一致——主 chunk 是胶水入口,不在加速目标内。**真正可编译的 Proto 是主 chunk 调用的子函数**,这些子函数的 IsVararg 由用户写法决定。

---

## 7. 嵌套 Proto 独立判定

### 7.1 不变式

承 [00-overview](./00-overview.md) §4.4 末尾的「嵌套 Proto 独立判定」:

> 「**嵌套 Proto 独立判定**:外层函数不可编译(如含 vararg)不代表内层嵌套函数不可编译——每个 Proto 独立分析。一个可编译的内层热函数,即便外层是 vararg 胶水,仍可被编译(只要它自己不含 F1..F7)。」

这一不变式在工程上**至关重要**——否则主 chunk 不可编译(F1 主 chunk 永远 vararg)就会污染所有子函数,P3 加速面归零。

### 7.2 实现:visitor 进入子函数不传染信号

§4.2 的 `visitFuncExpr` 关键细节:

```go
func (v *compilabilityVisitor) visitFuncExpr(e *ast.FuncExpr) {
    v.currentDepth++
    if v.currentDepth > v.maxClosureDepth {
        v.maxClosureDepth = v.currentDepth
    }
    // 子函数自身被独立 analyzeProto 调用
    // (codegen 对每个嵌套 FuncExpr 都产一个独立的 *bytecode.Proto,
    //  对每个 Proto 都独立调一次 b.AnalyzeProto)。
    //
    // 这里只为父函数收集**嵌套深度**信号,
    // **不**把子函数的 yield/debug/setfenv 信号传染给父——
    // 父函数的 visitor 见到的「子函数体」只用来计数深度,
    // 子函数的内容判定由它自己的 AnalyzeProto 调用承担。
    ast.Walk(v, e.Body)        // 仍递归遍历(为深度计数)
    v.currentDepth--
}
```

**注意**:这里的 `ast.Walk(v, e.Body)` **会递归进入子函数体**——会把子函数体内的 `yield` 调用也标到父 visitor 的 `callsYield` 上。**这是错误的**——父函数自己没 yield,只是它内部嵌套的子函数 yield。

### 7.3 修正:visitor 在进子函数时挂起信号收集

正确实现需要**信号传染隔离**——父 visitor 见到 `FuncExpr` 时,只收嵌套深度信号,不收 yield/debug/setfenv 等内容信号:

```go
func (v *compilabilityVisitor) visitFuncExpr(e *ast.FuncExpr) {
    v.currentDepth++
    if v.currentDepth > v.maxClosureDepth {
        v.maxClosureDepth = v.currentDepth
    }

    // 子函数体单独跑一个 sub-visitor,不污染父信号
    sub := &compilabilityVisitor{
        currentDepth:    v.currentDepth,
        maxClosureDepth: v.maxClosureDepth,
    }
    ast.Walk(sub, e.Body)

    // 只把**嵌套深度**信号回写父 visitor
    if sub.maxClosureDepth > v.maxClosureDepth {
        v.maxClosureDepth = sub.maxClosureDepth
    }
    // 不回写 callsYield / callsUnknownFn / usesDebug / usesSetfenv ——
    // 这些是子函数 Proto 自己的事,由子函数的 AnalyzeProto 调用独立判定。

    v.currentDepth--
}
```

这一处理让父函数的可编译性判定**只看父函数体内的直接信号**,不被子函数的内容污染。

### 7.4 子 Proto 何时被 analyzeProto

每个 `*ast.FuncExpr` 在 codegen 时产生一个 `*bytecode.Proto`(嵌套 Protos,01 §5.7)——`compile.Gen` 在递归处理 FuncExpr 时**对每个产出的 Proto 独立调 `b.AnalyzeProto(fnExpr, proto)`**:

```go
// compile.Gen 内部(伪码,实际细节见 04 落地版):
//
// codegen 递归到 FuncExpr 时:
//   subProto := genFuncBody(fn)            // 产子 Proto
//   bridge.AnalyzeProto(fn, subProto)       // ← 对子 Proto 独立分析
//   parentProto.Protos = append(parentProto.Protos, subProto)
```

这样**每个 Proto(主 + 嵌套)都有独立的 Compilability 判定**——主 chunk 即使不可编译,内层热函数仍可独立可编译。

### 7.5 跨层信号的边界情况

虽然 §7.3 确保子函数信号不污染父函数,但**有一个反向例外**:如果父函数体内**直接持有**某子 FuncExpr 引用并调它,父函数的 `callsUnknownFn` 应被激活(因为这是 unknown call)——但这不需要看子函数内容,只需要看「父函数是否调了 something」。

```lua
function parent()
    local f = function() coroutine.yield() end   -- 子 FuncExpr 内含 yield
    f()                                           -- 父函数调 f → callsUnknownFn
end
```

父函数的 visitor 见到 `f()` 这个 CallExpr → `isKnownLocalCall(f)` P2 初版返 false → `callsUnknownFn=true` → 父函数判 F2 不可编译。**子函数自身因含 yield 也判 F2 不可编译**——两者独立判定但结果一致。

> 这正是 §1 「保守第一」的体现——多个独立判定路径都把这种形状判不可编译,**冗余 = 安全**。

---

## 8. 不变式清单(本文一致性检查表)

实现期与日常 review 用本表自检:

| 不变式 | 含义 | 一旦违反的后果 |
|---|---|---|
| **I1 保守优先** | 任一 F1-F7 信号触发即判 NotCompilable;visitor 错误也走保守路径 | 失守即可能误判 → 正确性崩溃 |
| **I2 不动 P1 公共 API** | P1→P2 升级 wangshu.go / Compile / Program 公共形态零变化 | 失守即破坏「每阶段独立交付」(原则 3) |
| **I3 AST 用完即弃** | AnalyzeProto 返回后不再持有 fn 引用,Compile 完成 AST 即可 GC | 失守即 AST 内存常驻、运行期意外引用风险 |
| **I4 缓存与运行期变化无关** | Compilable 字段编译期一次写,运行期只读,不依赖运行期任何状态 | 失守即引入并发写竞争、判定漂移 |
| **I5 嵌套 Proto 独立判定** | 父函数判定不传染子函数;每个 Proto 独立 AnalyzeProto | 失守即主 chunk vararg 污染所有子函数,P3 加速面归零 |
| **I6 AST/Proto 一致性** | AST.IsVararg 与 Proto.IsVararg 必须一致;不一致即 codegen bug,panic | 失守即可能漏掉 vararg(F1 误判) |
| **I7 P1-only build 不引入 P2** | `!profile` build tag 下 bridge 包不存在,Compile 行为与 P1 完全一致 | 失守即破坏 P1-only byte-equal 差分 |
| **I8 F7 在 F1-F6 全过后才查** | SupportsAllOpcodes 仅在 F1-F6 全 OK 时调用(性能 + 简化) | 失守即每个 Proto 都跑一遍 SupportsAllOpcodes(慢) |
| **I9 visitor 不复用** | 每次 AnalyzeProto 新建 visitor,跑完即弃 | 失守即跨 Proto 信号污染(maxClosureDepth 残留等) |

---

## 9. 文档缺口

| # | 缺口 | 触发条件 | 计划处理 |
|---|---|---|---|
| GAP-1 | **精确 yield 调用图分析**(F2 §3.2.7) | 实测发现 isKnownLocalCall 全 false 的初版漏判面过大(真实负载里大量计算函数被保守判不可编译) | P2+ 新增「调用图分析」pass:从 `coroutine.yield` 反推可达 caller 集,精确判定;同时 isKnownLocalCall 实现 local 名 → 子 Proto 解析 |
| GAP-2 | **F5 阈值定标**(`MaxCompilableInsns` / `MaxCompilableRegs`) | pineapple 真实负载实测 | 实测 90% 分位的热点函数指令数 / 寄存器数,据此调整阈值;**阈值不影响正确性**,只影响何时编译 |
| GAP-3 | **F6 阈值定标**(`MaxClosureDepth` / `MaxUpvalCount`) | 同 GAP-2 | 同 GAP-2;另:P3 的 upvalue 编译协议成熟后([../p3-wasm-tier/02-translation](../p3-wasm-tier/02-translation.md) §3.7 待补)放宽 |
| GAP-4 | **F7 粒度细化** | P3 后端开发期 | P3 把 SupportsAllOpcodes 实现为「opcode 集 + 形状约束」(如「支持 GETTABLE 但仅当 key 是常量」),P2 的查询协议要适配 |
| GAP-5 | **scope-aware 名字解析**(F3 / F4 假阳性消除) | 用户重定义局部 `debug` / `setfenv` 触发误漏 | visitor 接入 codegen 的作用域信息(local 屏蔽全局),区分「真的是 debug 库」与「重名局部变量」 |
| GAP-6 | **元方法调用的 P3 编译协议** | F2 §3.2.5 推给 F7 + P3 后端 | P3 后端定义「metamethod 边界检查」编译形态;F7 反查 P3 是否支持某类 metamethod |
| GAP-7 | **AST visitor 接口最终形态** | 04 落地后 | 04 给 ast.Walker / ast.Visitor 的最终接口,本文 §4 的伪码对齐到真接口 |
| GAP-8 | **回填到 04 的「AnalyzeProto 调用点」** | 04 PR 接受 | 04 §N(待补)写入 `compile.Gen` 调 `bridge.AnalyzeProto` 的协议 |
| GAP-9 | **多 State 共享 Program 的 Compilability 字段** | Program 跨 State 共享(11 §1.3 / 11 §8) | Compilable 字段编译期一次写、跨 State 只读——**自动并发安全**(I4),无需特殊处理。但若 §9 GAP-1 的精确分析引入「按 State 决策」(不太可能),需重设计。**当前不视为缺口**,记录此处仅供 review |
| GAP-10 | **F7 跨版本 P3 升级的 Proto 重评估** | P3 后端能力跨版本扩展(实现新 opcode) | 见 [00-overview](./00-overview.md) §6「TierStuck 不重试」与 §9「P3 后端跨版本升级的 stuck 重评估」——本文与 04 共担,留 P2+ |

---

## 10. 对 04(`docs/design/p1-interpreter/04-frontend-parser-codegen.md`)的回填请求

**这是本文必有的回填请求**(题目要求 + 00 §3 关键耦合 4 / §7 末尾的最早决策点)。

### 10.1 请求节(建议插入位置:04 §13 末尾或 §1.1 后)

**04 §1.X 与 P2 的 AST 接口约定(P2 启动时落地)**

> 承 [03-compilability-analysis](../p2-bridge/03-compilability-analysis.md) §2.4 决策方案 ①「Compile 时同步分析、缓存结果、AST 用完即弃」:
>
> - **codegen `compile.Gen` 在产出 `*bytecode.Proto` 后,在返回前调一次 `bridge.AnalyzeProto(funcBody, proto)`** —— 把 P2 的可编译性分析结果写进 `proto.ProfileData.Compilable`(见 [03 §5](../p2-bridge/03-compilability-analysis.md));
> - **P1-only build**(`!profile` build tag)下 `bridge` 包不可用,`compile.Gen` 跳过此调用,所有 Proto 的 `Compilable` 留 `CompUnknown`(零值);
> - **嵌套 FuncExpr** 在递归 codegen 时各自产 Proto,对每个产出的 Proto 独立调 `bridge.AnalyzeProto`(嵌套 Proto 独立判定,见 [03 §7](../p2-bridge/03-compilability-analysis.md));
> - **AST 用完即弃**:`AnalyzeProto` 返回后 codegen 不持有 AST 引用,函数返回时 AST 可被 Go GC 回收。
>
> **本约定不改 04 公共 API**——`Gen` 的签名与 `Proto` 的字段都不动,仅在实现内部加一行 `bridge.AnalyzeProto` 调用(via build tag)。

### 10.2 回填请求清单

| # | 文档 | 节 | 内容 |
|---|---|---|---|
| RB-1 | `04-frontend-parser-codegen.md` | §1.X(待补) | §10.1 的 AST 接口约定全文 |
| RB-2 | `04-frontend-parser-codegen.md` | §3.X(visitor 接口) | 暴露 `ast.Walker` / `ast.Visitor` 接口供 P2 visitor 实现(见 §4.4 协议) |
| RB-3 | `04-frontend-parser-codegen.md` | §13 缺口 | 把「P2 是否需要保留 AST」缺口标关闭——本文 §2.4 已定方案 ①(不保留) |
| RB-4 | `00-overview.md`(P2) | §3 关键耦合 4 / §7 | 把「AST 保留协议」的「悬而未决」状态标定稿(决方案 ①) |
| RB-5 | `01-profiling.md`(P2) | §1 ProfileData 定义 | `ProfileData` 加 `Compilable` / `Reasons` 字段(本文 §5.3) |
| RB-6 | `04-try-compile-fallback.md`(P2) | §3 considerPromotion 入口 | 入口检查 `b.CompilabilityOf(proto) == CompCompilable` 才走升层路径;`CompUnknown` / `CompNotCompilable` 直接返回 |
| RB-7 | `05-p3-p4-interface.md`(P2) | §2 P3Compiler 接口 | 把 `SupportsAllOpcodes(proto) bool` 加进接口(本文 §3.7.3) |
| RB-8 | `06-testing-strategy.md`(P2) | §3 可编译性误判注入 fuzz | 注入测试覆盖 F1-F7 的零误判验证(PB7 验收) |

> **RB-1 与 RB-2 是 P2 启动的硬前置**——没它们 P2 PB3(可编译性分析器)开不了工。
> **RB-5 与 RB-6 是 P2 内部一致性**——本文写完后立刻协调 01 / 04 的 P2 文档同步。
> **RB-7 是 P3 实现期的接口约定**——P3 PR 上线时实现这个接口。
> **RB-8 是 P2 验收**(PB7),由 06 主管。

---

## 11. 与同篇章其它文档的协同与边界

| 文档 | 本文与之的关系 |
|---|---|
| [00-overview](./00-overview.md) | 本文是 §0 文档地图列出的「03 安全闸门单一事实源」;§3 关键耦合 4(AST 保留协议)与 §6 决策表「可编译性分析层」的精确论证在本文 §2 |
| [01-profiling](./01-profiling.md) | 共享 `ProfileData` 字段定义(本文加 `Compilable` / `Reasons`,01 加 `BackEdgeCount` / `EntryCount`);两者无热路径耦合 |
| [02-ic-feedback](./02-ic-feedback.md) | 无直接耦合——IC feedback 是 P3/P4 编译时读的供料,与可编译性判定独立(可编译性 = 「能编」,feedback = 「编得多好」) |
| [04-try-compile-fallback](./04-try-compile-fallback.md) | 本文是 04 的**前置闸门**——`considerPromotion` 入口先查 `Compilable`,`CompNotCompilable` 直接跳过 |
| [05-p3-p4-interface](./05-p3-p4-interface.md) | 本文定义 `P3Compiler.SupportsAllOpcodes` 的形状,05 是该接口的单一事实源 |
| [06-testing-strategy](./06-testing-strategy.md) | 本文的 F1-F7 形状清单是 06 §3 可编译性误判注入 fuzz 的输入——每条规则对应一组测试脚本 |
| [implementation-progress](./implementation-progress.md) | 本文的 PB3 验收条目(00 §4)由它跟踪 |

---

## 12. 篇尾速查:F1-F7 一行总结

| # | 形状 | 一行判定 | 主识别点 |
|---|---|---|---|
| F1 | vararg 函数 | `function(...)` 或函数体含 `...` | `FuncExpr.IsVararg` + `Proto.IsVararg` + VARARG opcode |
| F2 | 协程相关 | 调 `coroutine.*` 或调 unknown 函数 | `compilabilityVisitor.callsCoroutine / callsUnknownFn` |
| F3 | debug 库 | 引用名 `debug` | `NameExpr{"debug"}` |
| F4 | setfenv/getfenv | 引用名 `setfenv` / `getfenv` | `NameExpr{"setfenv"}` / `NameExpr{"getfenv"}` |
| F5 | 过大函数 | `len(Code) > 2000` 或 `MaxStack > 200` | Proto 字段 |
| F6 | 深嵌套闭包 | 嵌套深度 > 3 或 upvalue 数 > 8 | visitor.maxClosureDepth + `Proto.UpvalDescs` |
| F7 | P3 后端不支持 | `!p3.SupportsAllOpcodes(proto)` | 接口查询 |

任一成立 → `CompNotCompilable`;全部不成立 → `CompCompilable`。

---

相关:
[00-overview](./00-overview.md)(P2 总览 / 关键耦合 4 = 本文 AST 保留协议) ·
[01-profiling](./01-profiling.md)(共享 ProfileData 字段) ·
[04-try-compile-fallback](./04-try-compile-fallback.md)(本文是其前置闸门) ·
[05-p3-p4-interface](./05-p3-p4-interface.md)(`SupportsAllOpcodes` 接口本文定义) ·
[06-testing-strategy](./06-testing-strategy.md)(F1-F7 误判注入 fuzz) ·
[../p2-bridge/00-overview](./00-overview.md) §4(本文种子,大量扩展) ·
[../p1-interpreter/04-frontend-parser-codegen.md](../p1-interpreter/04-frontend-parser-codegen.md)(AST 节点定义 + §10 回填请求 RB-1/RB-2) ·
[../p1-interpreter/02-bytecode-isa.md](../p1-interpreter/02-bytecode-isa.md)(VARARG opcode 37 / IsVararg / opcode 0..37 全集) ·
[../p1-interpreter/01-value-object-model.md](../p1-interpreter/01-value-object-model.md)(Proto.Code / MaxStack / IsVararg 字段) ·
[../p1-interpreter/08-coroutines.md](../p1-interpreter/08-coroutines.md)(协程 yield 语义,F2 依据) ·
[../p1-interpreter/10-stdlib.md](../p1-interpreter/10-stdlib.md)(debug / setfenv / coroutine.yield 调用点识别) ·
[../p1-interpreter/11-embedding-arena-abi.md](../p1-interpreter/11-embedding-arena-abi.md) §1.3(Compile 占位填实) ·
[../roadmap.md](../roadmap.md) §5 原则 4(不可编译形状走 fallback) ·
[../../../llmdoc/must/design-premises.md](../../../llmdoc/must/design-premises.md)(原则 4)

